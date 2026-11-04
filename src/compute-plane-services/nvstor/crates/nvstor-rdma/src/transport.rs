// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//! RDMA transport for inter-node data transfer.
//!
//! Features:
//! - Authenticated TCP bootstrap of QP info (connect by IP; see
//!   `crate::bootstrap`): the server builds nothing for a connector until
//!   it has proven a credential
//! - Multi-QP pipeline: N lanes per peer for parallel transfers
//! - Pre-registered memory pool for zero-copy
//! - Least privilege on the data plane: no pool address or rkey is ever
//!   advertised, a server's QPs grant peers no remote access, and a
//!   client's QPs grant only what its own pool was registered for
//! - Liveness: the bootstrap TCP stream stays open for the connection's
//!   life; end-of-stream on either side tears the peer down
//! - Admission limits on handshakes and peers (`AcceptLimits`)
//! - Bulk RDMA WRITE / READ posted as chained work requests: one
//!   `ibv_post_send` and one completion per chain, split at the port's
//!   message size limit
//! - Completions through one engine per transport (`crate::completion`):
//!   shared CQs on a completion channel, one poller, waiters parked per
//!   operation; any number of threads post to one lane at once
//! - Clean shutdown with listener thread join
//!
//! Usage:
//!   let transport = RdmaTransport::new(device, pool);
//!   transport.set_verifier(verifier);           // server
//!   transport.listen(9877)?;
//!   transport.set_credentials(creds);           // client
//!   let peer_id = transport.connect("10.1.2.3:9877")?;
//!   transport.write_remote_to(peer_id, data, lkey, remote_addr, rkey)?;

use std::collections::HashMap;
use std::io::Write;
use std::mem::ManuallyDrop;
use std::net::{IpAddr, Shutdown, SocketAddr, TcpListener, TcpStream, ToSocketAddrs};
use std::os::fd::AsRawFd;
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

use parking_lot::{Mutex, RwLock};
use tracing::{debug, error, info, warn};

use crate::bootstrap::{
    self, AcceptGate, AcceptLimits, AcceptTicket, Endpoint, HandshakeVerifier, Hello, PeerRole,
    SessionAdmitter, SessionClaim,
};
use crate::completion::{CompletionEngine, LaneState, Limits, OpError, PostFailure, SendQp};
use crate::connection::{
    plan_posts, split_pieces, split_transfer, OneSided, Piece, QueuePair, SendChain,
    INFLIGHT_POLL_TIMEOUT,
};
use crate::device::{Access, RdmaDevice};
use crate::ffi;
use crate::pool::MemoryPool;

/// Default number of QP lanes per peer.
const DEFAULT_LANES: usize = 4;

/// Depth of each lane's send and receive queues and CQs.
const LANE_QUEUE_DEPTH: u32 = 256;

/// Most work requests in one chained post. At most the send queue and
/// send CQ depth (see `connection::plan_posts`); half of it leaves the
/// lane's control SENDs room on a single-lane connection.
const MAX_CHAIN_WRS: usize = (LANE_QUEUE_DEPTH / 2) as usize;

/// Largest WR when the port's `max_msg_sz` cannot be read: the smallest
/// value current RoCE and InfiniBand HCAs report.
const FALLBACK_MAX_MSG: u32 = 1 << 30;

/// Bound on a client's TCP connect and on each read/write of its
/// handshake. A server's bound is `AcceptLimits::handshake_timeout`.
const DIAL_TIMEOUT: Duration = Duration::from_secs(5);

/// How often the liveness watcher looks at the bootstrap streams. Only
/// bounds how long a dead peer's resources linger, never a data path.
const WATCH_INTERVAL: Duration = Duration::from_millis(200);

static NEXT_PEER_ID: AtomicU64 = AtomicU64::new(1);

/// A single QP lane. On drop the QP is destroyed first (`Drop for
/// QpLane`), then the control channel's MRs, the lane's state and its
/// registration with the completion engine, so the engine's receive
/// budget, routing entry, the lane's abandoned operations and the buffers
/// it holds are released only once the QP (and any CQE of it) is gone.
/// If destroying the QP fails, the control channel and everything the
/// lane holds are leaked rather than reused.
struct QpLane {
    qp: QueuePair,
    /// Two-sided SEND/RECV channel for control messages (S3-over-RDMA
    /// request/response). Pre-posts a small ring of RECV buffers on
    /// lane creation so the kernel always has somewhere to land an
    /// incoming SEND. Dropped by `Drop for QpLane`, only after the QP is
    /// destroyed.
    control: ManuallyDrop<crate::control_channel::ControlChannel>,
    /// Send-queue credits, routed receives and failure state. Every
    /// operation takes a send-queue credit per WR before posting and gets
    /// them back when it completes, so any number of threads can post here
    /// at once without overrunning the queue. Each operation's completion
    /// is routed to it by `wr_id` (`crate::completion`), so no thread can
    /// consume another's CQE.
    state: Arc<LaneState>,
    reg: LaneRegistration,
}

impl Drop for QpLane {
    fn drop(&mut self) {
        let destroyed = self.qp.destroy();
        self.reg.qp_destroyed = destroyed.is_ok();
        match destroyed {
            // SAFETY: dropped here once; `control` is never used after.
            Ok(()) => unsafe { ManuallyDrop::drop(&mut self.control) },
            Err(code) => error!(
                lane = self.reg.key,
                code,
                "ibv_destroy_qp failed; leaking the lane's control channel, \
                                 abandoned operations and held buffers"
            ),
        }
    }
}

impl SendQp for QueuePair {
    fn stop(&self) -> Result<(), String> {
        self.to_error()
    }
    fn post_fence(&self, wr_id: u64) -> Result<(), i32> {
        QueuePair::post_fence(self, wr_id)
    }
}

impl QpLane {
    fn engine(&self) -> &CompletionEngine {
        &self.reg.engine
    }

    /// Post `segs` (at most the send queue's depth) as one chained post
    /// whose WRs all carry one completion slot, and wait for it (see
    /// `Dispatch::run`). Returns once the hardware is done with the local
    /// buffers, on success and on error alike, except when even stopping
    /// the QP brings no completion (`OpError::unobserved`: the caller must
    /// hold the buffers until the QP is destroyed, `LaneState::hold`).
    fn run_chain(&self, segs: &[crate::connection::Segment]) -> Result<(), OpError> {
        self.engine().dispatch().run(
            &self.state,
            &self.qp,
            segs.len() as u64,
            &LANE_LIMITS,
            |t| {
                let mut chain = SendChain::for_slot(segs, t.wr_id(false), t.wr_id(true));
                self.qp.post_chain(&mut chain).map_err(|e| PostFailure {
                    posted: e.posted,
                    msg: e.to_string(),
                })
            },
        )
    }

    /// One signaled SEND of `len` bytes at `addr`, waited for.
    fn send(&self, addr: u64, lkey: u32, len: u32) -> Result<(), OpError> {
        self.engine()
            .dispatch()
            .run(&self.state, &self.qp, 1, &LANE_LIMITS, |t| {
                self.qp
                    .send_with_id(addr, lkey, len, t.wr_id(true))
                    .map_err(|msg| PostFailure { posted: 0, msg })
            })
    }

    /// The next control message received on this lane: (buffer index,
    /// length). See `LaneState::recv`.
    fn recv(&self, deadline: Option<Instant>, reply_owed: bool) -> Result<(usize, usize), String> {
        self.state.recv(deadline, reply_owed)
    }
}

/// How long a lane operation waits for credits (before posting) and for
/// its completion (after posting). Separate bounds: a long wait for
/// credits never shortens the completion wait into stopping a healthy QP.
/// The server-side worst case of a control reply is built from these
/// (`crate::s3_rdma_proto::SERVER_REPLY_WORST`).
pub(crate) const LANE_LIMITS: Limits = Limits {
    credits: INFLIGHT_POLL_TIMEOUT,
    completion: INFLIGHT_POLL_TIMEOUT,
};

/// A lane's place in the completion engine: released when the lane drops,
/// after its QP is destroyed.
struct LaneRegistration {
    engine: Arc<CompletionEngine>,
    key: u64,
    /// Whether the lane's QP was destroyed (set by `Drop for QpLane`). A
    /// registration dropped without a `QpLane` (a lane that failed to
    /// finish) never posted anything through the engine.
    qp_destroyed: bool,
}

impl Drop for LaneRegistration {
    fn drop(&mut self) {
        self.engine.dispatch().unregister_lane(
            self.key,
            crate::control_channel::RECV_DEPTH as u64,
            self.qp_destroyed,
        );
    }
}

/// A failed bulk RDMA WRITE / READ. If [`Self::buffer_in_use`], the
/// operation's last completion was never seen and the HCA may still read
/// or write the local buffer: pass whatever owns that buffer to
/// [`Self::hold`], which keeps it until the lane's QP is destroyed,
/// instead of reusing it.
pub struct TransferError {
    msg: String,
    held_by: Option<Arc<LaneState>>,
}

impl TransferError {
    /// An error whose buffer `lane` must hold (tests of the callers).
    #[cfg(test)]
    pub(crate) fn held_by(msg: &str, lane: Arc<LaneState>) -> Self {
        Self {
            msg: msg.into(),
            held_by: Some(lane),
        }
    }

    /// Whether the local buffer may still be in use by the HCA.
    pub fn buffer_in_use(&self) -> bool {
        self.held_by.is_some()
    }

    /// Dispose of the owner of the operation's local buffer: dropped now
    /// if the hardware is done with it, otherwise kept by the lane until
    /// its QP is destroyed (leaked if that fails).
    pub fn hold<T: Send + 'static>(&self, owner: T) {
        match &self.held_by {
            Some(lane) => lane.hold(owner),
            None => drop(owner),
        }
    }
}

impl std::fmt::Display for TransferError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.msg)?;
        if self.buffer_in_use() {
            f.write_str(" (local buffer held until the QP is destroyed)")?;
        }
        Ok(())
    }
}

impl std::fmt::Debug for TransferError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        std::fmt::Display::fmt(self, f)
    }
}

impl std::error::Error for TransferError {}

impl From<String> for TransferError {
    fn from(msg: String) -> Self {
        Self { msg, held_by: None }
    }
}

impl From<&str> for TransferError {
    fn from(msg: &str) -> Self {
        msg.to_string().into()
    }
}

impl From<TransferError> for String {
    fn from(e: TransferError) -> String {
        e.to_string()
    }
}

/// A lane's QP before it is connected, with the engine its CQs belong
/// to (the QP drops first).
struct RawLane {
    qp: QueuePair,
    engine: Arc<CompletionEngine>,
}

/// Entries of a transport's shared send CQ: the most WRs in flight on all
/// its lanes together (the engine's credits enforce it).
const ENGINE_SEND_CQ: u32 = 32 * 1024;

/// Entries of a transport's shared receive CQ: room for every lane's RECV
/// ring at the accept limits' 4096 peers of 4 lanes.
const ENGINE_RECV_CQ: u32 = 4096 * 4 * crate::control_channel::RECV_DEPTH as u32;

/// Connection to a remote peer with multiple QP lanes.
struct PeerConnection {
    /// Lanes are `Arc` so a caller can clone the handle it needs and DROP
    /// the `peers` read guard before doing any RDMA I/O. Holding the
    /// guard across a post+poll — which can block for seconds, or minutes
    /// on an idle receive — stalls every writer, and parking_lot's RwLock
    /// is writer-preferring, so a single pending writer then blocks all
    /// new readers too: one slow peer freezes the whole transport.
    lanes: Vec<Arc<QpLane>>,
    next_lane: AtomicU64,
    addr: String,
    /// Serializes one round-trip on lane 0 (send_request → response).
    /// Concurrent transactions from multiple threads serialize on the
    /// control plane but parallelize on the data plane (RDMA WRITE/
    /// READ on lanes 1+). Held by `request_response` for the duration
    /// of one txn so the next thread's SEND doesn't race ahead and
    /// consume this thread's response.
    control_txn: Arc<Mutex<()>>,
    /// The authenticated role of a peer this transport accepted; `None`
    /// for a peer it dialed.
    role: Option<PeerRole>,
    /// The access key an accepted peer authenticated with (`None` for a
    /// peer this transport dialled).
    access_key: Option<String>,
    /// The bootstrap stream, kept open as the liveness channel. Shared
    /// with the watcher so its descriptor stays valid while polled.
    stream: Arc<TcpStream>,
    /// An accepted peer's claim on the accept limits, released on drop.
    _ticket: Option<AcceptTicket>,
    /// An accepted peer's hold on the owner's session limit
    /// (`set_session_admitter`), released on drop.
    _claim: Option<SessionClaim>,
}

impl PeerConnection {
    /// Pick a lane for a bulk RDMA WRITE/READ. Lane 0 is the control
    /// plane (SEND/RECV); when more than one lane exists, data uses
    /// lanes 1+ so bulk transfers never queue behind control traffic on
    /// one QP. With a single lane, everything shares lane 0; each
    /// operation's completion is routed to it by `wr_id`, so that is
    /// correct too.
    fn pick_lane(&self) -> Arc<QpLane> {
        let n = self.lanes.len();
        if n <= 1 {
            return Arc::clone(&self.lanes[0]);
        }
        let idx = 1 + (self.next_lane.fetch_add(1, Ordering::Relaxed) as usize % (n - 1));
        Arc::clone(&self.lanes[idx])
    }
}

/// Callback invoked exactly once for each peer that finishes the
/// RDMA handshake on the server side. Lets higher-level services
/// (the S3-over-RDMA dispatcher, replication, etc.) spawn per-peer
/// work without polling `peers`.
pub type OnPeerAccepted = Arc<dyn Fn(u64) + Send + Sync>;

/// Callback invoked once when a peer is removed, whatever the cause
/// (local disconnect, remote end-of-stream, shutdown). Lets services
/// release per-peer state such as parked pool slots.
pub type OnPeerClosed = Arc<dyn Fn(u64) + Send + Sync>;

/// State the liveness watcher shares with the transport.
struct Shared {
    peers: RwLock<HashMap<u64, PeerConnection>>,
    on_peer_closed: RwLock<Option<OnPeerClosed>>,
    shutdown: AtomicBool,
}

impl Shared {
    /// Remove a peer: move its QPs to ERR (so a thread blocked on its
    /// receive CQ gets a flush CQE and returns), close the bootstrap
    /// stream (so the remote side sees end-of-stream and tears down too),
    /// free the lanes not still borrowed by an in-flight operation, and
    /// tell the owner. Returns false if the peer was already gone.
    fn remove(&self, peer_id: u64) -> bool {
        let Some(conn) = self.peers.write().remove(&peer_id) else {
            return false;
        };
        retire_lanes(
            peer_id,
            conn.lanes.iter().map(|l| (&l.qp as &dyn SendQp, &*l.state)),
        );
        let _ = conn.stream.shutdown(Shutdown::Both);
        drop(conn);
        if let Some(cb) = self.on_peer_closed.read().clone() {
            cb(peer_id);
        }
        true
    }
}

/// Stop the lanes of a removed peer: each QP to the error state (its
/// posted WRs flush) and each lane shut down (failed, and its receive
/// queue closed), so a dispatcher waiting for a request with no deadline
/// wakes and drops its lane reference even if the stopped QP delivers no
/// flush completion; the last reference destroys the QP and releases
/// what the lane holds.
fn retire_lanes<'a>(peer_id: u64, lanes: impl Iterator<Item = (&'a dyn SendQp, &'a LaneState)>) {
    for (qp, state) in lanes {
        if let Err(e) = qp.stop() {
            // The QP stays able to receive until it is destroyed: a caller
            // relying on "no remote write lands after disconnect" should know.
            warn!(peer_id, error = %e, "QP to ERR on disconnect failed");
        }
        state.shutdown("peer removed");
    }
}

/// RDMA transport for cluster communication.
///
/// Drop order: peers → pool → device (QPs before MR before context).
pub struct RdmaTransport {
    shared: Arc<Shared>,
    listener_handle: Mutex<Option<thread::JoinHandle<()>>>,
    watcher_handle: Mutex<Option<thread::JoinHandle<()>>>,
    lanes_per_peer: usize,
    /// Bytes per control message buffer on every lane (both ends of a
    /// connection must agree): S3-over-RDMA's `CONTROL_BUF_SIZE` unless
    /// set otherwise.
    control_msg_size: usize,
    pool: Arc<MemoryPool>,
    device: Arc<RdmaDevice>,
    /// Optional hook fired after each successful server-side
    /// `accept_peer`. Set via `set_on_peer_accepted` before calling
    /// `listen`; the callback runs on the accept thread, so keep it
    /// fast (typically: spawn a dispatcher and return).
    on_peer_accepted: RwLock<Option<OnPeerAccepted>>,
    /// How long a server-side `recv_request` waits for a client's next
    /// request before giving up. `None` = wait indefinitely (the default,
    /// and correct for checkpoint clients that idle minutes between
    /// steps). A peer that goes away is detected through its
    /// bootstrap stream instead (see `Shared::remove`).
    idle_recv_timeout: Option<Duration>,
    /// Credentials this transport presents when it dials (the bootstrap
    /// AUTH) and signs control requests with.
    credentials: RwLock<Option<crate::auth::Credentials>>,
    /// Decides who may connect, and as what. `None` admits anyone as a
    /// `Client` (no remote memory access either way).
    verifier: RwLock<Option<HandshakeVerifier>>,
    /// Session limit checked after authentication, before any verbs
    /// object is built. `None` admits every authenticated connector.
    admitter: RwLock<Option<SessionAdmitter>>,
    gate: Arc<AcceptGate>,
    /// The port's `max_msg_sz`: no work request may carry more.
    port_max_msg: u32,
    /// Bytes per work request of a bulk WRITE / READ; transfers larger
    /// than this go out as a chain. At most `port_max_msg`.
    max_wr_bytes: AtomicU32,
    /// Created with the first lane; every lane holds it.
    engine: Mutex<Option<Arc<CompletionEngine>>>,
}

/// A listening socket on `addr`: dual stack for `::` (see
/// [`bind_dual_stack`]), that address alone otherwise.
pub(crate) fn bind_listener(addr: IpAddr, port: u16) -> std::io::Result<TcpListener> {
    match addr {
        IpAddr::V6(a) if a.is_unspecified() => bind_dual_stack(port),
        a => TcpListener::bind((a, port)),
    }
}

/// Listen on `port` (0 = any) on every address, IPv4 and IPv6: one
/// `[::]` socket with `IPV6_V6ONLY` off, so a source named by an IPv6
/// address (or a name that resolves to one) can be dialed. Falls back to
/// `0.0.0.0` where the host has no IPv6.
pub(crate) fn bind_dual_stack(port: u16) -> std::io::Result<TcpListener> {
    match bind_v6_any(port) {
        Ok(l) => Ok(l),
        Err(e)
            if matches!(
                e.raw_os_error(),
                Some(libc::EAFNOSUPPORT) | Some(libc::EPROTONOSUPPORT) | Some(libc::EADDRNOTAVAIL)
            ) =>
        {
            debug!(port, error = %e, "no IPv6; listening on IPv4 only");
            TcpListener::bind((std::net::Ipv4Addr::UNSPECIFIED, port))
        }
        Err(e) => Err(e),
    }
}

fn bind_v6_any(port: u16) -> std::io::Result<TcpListener> {
    use std::os::fd::{FromRawFd, OwnedFd};
    let last = std::io::Error::last_os_error;
    // SAFETY: plain socket(2); the result is checked.
    let fd = unsafe { libc::socket(libc::AF_INET6, libc::SOCK_STREAM | libc::SOCK_CLOEXEC, 0) };
    if fd < 0 {
        return Err(last());
    }
    // SAFETY: `fd` is a fresh descriptor owned by nothing else.
    let fd = unsafe { OwnedFd::from_raw_fd(fd) };
    let set = |level: i32, name: i32, value: i32| -> std::io::Result<()> {
        // SAFETY: `value` is a valid c_int of the size passed.
        let rc = unsafe {
            libc::setsockopt(
                fd.as_raw_fd(),
                level,
                name,
                (&value as *const i32).cast(),
                std::mem::size_of::<i32>() as libc::socklen_t,
            )
        };
        if rc == 0 {
            Ok(())
        } else {
            Err(last())
        }
    };
    // As std's TcpListener::bind: rebind a port in TIME_WAIT at once.
    set(libc::SOL_SOCKET, libc::SO_REUSEADDR, 1)?;
    set(libc::IPPROTO_IPV6, libc::IPV6_V6ONLY, 0)?;
    // SAFETY: sockaddr_in6 is plain data; zero is in6addr_any.
    let mut sa: libc::sockaddr_in6 = unsafe { std::mem::zeroed() };
    sa.sin6_family = libc::AF_INET6 as libc::sa_family_t;
    sa.sin6_port = port.to_be();
    // SAFETY: `sa` is a valid sockaddr_in6 of the size passed.
    if unsafe {
        libc::bind(
            fd.as_raw_fd(),
            (&sa as *const libc::sockaddr_in6).cast(),
            std::mem::size_of::<libc::sockaddr_in6>() as libc::socklen_t,
        )
    } != 0
    {
        return Err(last());
    }
    // SAFETY: a bound stream socket.
    if unsafe { libc::listen(fd.as_raw_fd(), 1024) } != 0 {
        return Err(last());
    }
    Ok(TcpListener::from(fd))
}

/// The first half of a client handshake: a TCP bootstrap connection and
/// the server's HELLO. See [`RdmaTransport::dial`].
pub struct Dialed {
    stream: TcpStream,
    hello: Hello,
    hello_raw: Vec<u8>,
}

impl Dialed {
    /// The GID the server's QPs use; tells a multi-NIC client which local
    /// transport can reach it (`crate::rail`).
    pub fn server_gid(&self) -> [u8; 16] {
        self.hello.gid
    }
}

impl RdmaTransport {
    pub fn new(device: Arc<RdmaDevice>, pool: Arc<MemoryPool>) -> Self {
        let port_max_msg = match device.max_msg_size() {
            Ok(n) if n > 0 => n,
            Ok(_) | Err(_) => {
                warn!(device = %device.name, fallback = FALLBACK_MAX_MSG,
                      "cannot read the port's max message size; assuming the fallback");
                FALLBACK_MAX_MSG
            }
        };
        Self {
            shared: Arc::new(Shared {
                peers: RwLock::new(HashMap::new()),
                on_peer_closed: RwLock::new(None),
                shutdown: AtomicBool::new(false),
            }),
            listener_handle: Mutex::new(None),
            watcher_handle: Mutex::new(None),
            lanes_per_peer: DEFAULT_LANES,
            control_msg_size: crate::s3_rdma_proto::CONTROL_BUF_SIZE,
            pool,
            device,
            on_peer_accepted: RwLock::new(None),
            idle_recv_timeout: None,
            credentials: RwLock::new(None),
            verifier: RwLock::new(None),
            admitter: RwLock::new(None),
            gate: AcceptGate::new(AcceptLimits::default()),
            port_max_msg,
            max_wr_bytes: AtomicU32::new(port_max_msg),
            engine: Mutex::new(None),
        }
    }

    /// This transport's completion engine, created on first use, and
    /// created anew if the current one's poller has stopped (its lanes
    /// have all failed; they keep the old engine until they drop).
    fn engine(&self) -> Result<Arc<CompletionEngine>, String> {
        let mut e = self.engine.lock();
        if let Some(e) = e.as_ref().filter(|e| !e.is_dead()) {
            return Ok(Arc::clone(e));
        }
        let engine = CompletionEngine::new(
            &self.device,
            ENGINE_SEND_CQ,
            ENGINE_RECV_CQ,
            &format!("rdma-cq-{}", self.device.name),
        )?;
        *e = Some(Arc::clone(&engine));
        Ok(engine)
    }

    /// Cap the bytes per work request of bulk WRITE / READ (clamped to
    /// 1..=the port's `max_msg_sz`, the default). A transfer larger than
    /// the cap is posted as a chain of WRs with one doorbell and one
    /// completion; this exists to measure chained against single-WR
    /// transfers on real hardware.
    pub fn set_max_wr_bytes(&self, bytes: u32) {
        self.max_wr_bytes
            .store(bytes.clamp(1, self.port_max_msg), Ordering::Relaxed);
    }

    /// Current bytes-per-WR cap (see [`Self::set_max_wr_bytes`]).
    pub fn max_wr_bytes(&self) -> u32 {
        self.max_wr_bytes.load(Ordering::Relaxed)
    }

    /// Register a callback fired once per successful server-side
    /// accept. The callback receives the new `peer_id` and runs on
    /// the accept thread — keep it fast (spawn a worker and return).
    /// Replaces any previously-registered callback.
    pub fn set_on_peer_accepted(&self, cb: OnPeerAccepted) {
        *self.on_peer_accepted.write() = Some(cb);
    }

    /// Register a callback fired once whenever a peer is removed.
    pub fn set_on_peer_closed(&self, cb: OnPeerClosed) {
        *self.shared.on_peer_closed.write() = Some(cb);
    }

    /// Require connectors to authenticate in the bootstrap; `verifier`
    /// maps each credential to a role (or `Denied`). Set before `listen`.
    pub fn set_verifier(&self, verifier: HandshakeVerifier) {
        *self.verifier.write() = Some(verifier);
    }

    /// Limit sessions on the listening side: after a connector
    /// authenticates, `admitter` either admits it (the claim it returns
    /// lives as long as the peer) or turns it away with BUSY before any
    /// QP exists. One admitter may be shared by several transports (one
    /// per NIC) to count sessions across all of them. Set before `listen`.
    pub fn set_session_admitter(&self, admitter: SessionAdmitter) {
        *self.admitter.write() = Some(admitter);
    }

    /// Bound handshakes and peers on the listening side.
    pub fn set_accept_limits(&self, limits: AcceptLimits) {
        self.gate.set_limits(limits);
    }

    /// (handshakes in progress, accepted peers) counted against the
    /// accept limits.
    pub fn accept_counts(&self) -> (usize, usize) {
        self.gate.counts()
    }

    /// Create with default pool (4MB × 64 = 256MB).
    pub fn with_defaults(device: Arc<RdmaDevice>) -> Result<Self, String> {
        let pool = MemoryPool::new(&device, 4 * 1024 * 1024, 64)?;
        Ok(Self::new(device, Arc::new(pool)))
    }

    /// Set number of QP lanes per peer (default: 4, at most
    /// `bootstrap::MAX_LANES`).
    pub fn set_lanes_per_peer(&mut self, n: usize) {
        self.lanes_per_peer = n.clamp(1, bootstrap::MAX_LANES);
    }

    /// Set the control message size of every lane this transport builds
    /// (the KV service: `kv_proto::KV_MSG_SIZE`). Both ends of a connection
    /// must use the same size: a SEND larger than the peer's RECV buffer
    /// fails at the NIC. Set before `listen` or `connect`.
    ///
    /// The size is not exchanged in the bootstrap: a mismatch shows only
    /// when a message exceeds the smaller side's buffer, and then fails
    /// both lanes cleanly (the sender sees a remote invalid request, its
    /// peer is removed, and the receiver tears down when the bootstrap
    /// stream closes). The KV service runs on its own listener with one
    /// fixed size; carrying the size in the handshake comes with the
    /// per-connection request key.
    pub fn set_control_msg_size(&mut self, n: usize) -> Result<(), String> {
        crate::control_channel::check_msg_size(n)?;
        self.control_msg_size = n;
        Ok(())
    }

    /// Peer ids are unique across every transport in the process, not
    /// just this one: a multi-NIC server runs one transport per NIC, and
    /// per-peer server state (the s3_rdma parked-slot table) is keyed by
    /// peer id alone.
    fn alloc_peer_id(&self) -> u64 {
        NEXT_PEER_ID.fetch_add(1, Ordering::Relaxed)
    }

    /// The NIC this transport's QPs, CQs and pool live on.
    pub fn device(&self) -> &Arc<RdmaDevice> {
        &self.device
    }

    /// The GID peers address this transport by (RoCE v2 routable GID when
    /// the port has one). Read from the device on every call, so it
    /// follows address changes (e.g. SLAAC completing after start).
    pub fn local_gid(&self) -> [u8; 16] {
        self.local_address().2
    }

    /// (sgid index, LID, GID) of this transport's port, read now.
    fn local_address(&self) -> (u8, u16, [u8; 16]) {
        let sgid_index = self.device.roce_gid_index();
        let gid = self.device.gid(i32::from(sgid_index)).unwrap_or_default();
        let lid = self.device.lid().unwrap_or(0);
        // SAFETY: every ibv_gid variant is 16 plain bytes.
        (sgid_index, lid, unsafe { gid.raw })
    }

    fn new_lane(&self) -> Result<RawLane, String> {
        let engine = self.engine()?;
        let qp = QueuePair::create_rc(
            &self.device,
            engine.send_cq(),
            engine.recv_cq(),
            LANE_QUEUE_DEPTH,
            LANE_QUEUE_DEPTH,
        )?;
        Ok(RawLane { qp, engine })
    }

    /// Connect a lane to the remote QP `remote_qpn` and pre-post its
    /// control RECV ring. `remote_access` is what the peer may do through
    /// it (see `QueuePair::to_init`).
    /// Lane 0 carries every control message and gets the transport's
    /// message size; data lanes never receive one and get the smallest ring.
    // Each argument is a distinct part of the QP state transition (the
    // lane, the peer's QP and endpoint, the access granted, the path MTU
    // and the GID index); bundling them would only rename the list.
    #[allow(clippy::too_many_arguments)]
    fn finish_lane(
        &self,
        index: usize,
        raw: RawLane,
        remote_qpn: u32,
        remote: &Endpoint,
        remote_access: Access,
        mtu: u32,
        sgid_index: u8,
    ) -> Result<Arc<QpLane>, String> {
        let RawLane { qp, engine } = raw;
        let remote_gid = ffi::ibv_gid { raw: remote.gid };
        qp.to_init(self.device.port, remote_access)?;
        qp.to_rtr(
            remote_qpn,
            remote.lid,
            &remote_gid,
            self.device.port,
            mtu,
            sgid_index,
        )?;
        qp.to_rts()?;
        let state = engine.dispatch().register_lane(
            u64::from(qp.max_send_wr()),
            crate::control_channel::RECV_DEPTH as u64,
        )?;
        let key = state.key();
        let reg = LaneRegistration {
            engine,
            key,
            qp_destroyed: true,
        };
        // Pre-post the control channel's RECV ring now that the QP is
        // RTS. Must happen before either side SENDs or the first message
        // gets RNR-retry.
        let msg_size = if index == 0 {
            self.control_msg_size
        } else {
            crate::control_channel::MIN_MSG_SIZE
        };
        let control =
            crate::control_channel::ControlChannel::new(&self.device, &qp, key, msg_size)?;
        Ok(Arc::new(QpLane {
            qp,
            control: ManuallyDrop::new(control),
            state,
            reg,
        }))
    }

    /// Start the liveness watcher if it is not running. One thread per
    /// transport polls every peer's bootstrap stream.
    fn ensure_watcher(&self) -> Result<(), String> {
        let mut handle = self.watcher_handle.lock();
        if handle.is_some() {
            return Ok(());
        }
        let shared = Arc::clone(&self.shared);
        let h = thread::Builder::new()
            .name(format!("rdma-liveness-{}", self.device.name))
            .spawn(move || watch(shared))
            .map_err(|e| format!("spawn liveness watcher: {e}"))?;
        *handle = Some(h);
        Ok(())
    }

    fn insert_peer(&self, conn: PeerConnection) -> Result<u64, String> {
        let peer_id = self.alloc_peer_id();
        self.shared.peers.write().insert(peer_id, conn);
        // A handshake that finishes during shutdown must not leave a peer
        // behind: either shutdown's sweep sees this one, or this sees the
        // flag (set before the sweep) and removes it.
        if self.shared.shutdown.load(Ordering::SeqCst) {
            self.shared.remove(peer_id);
            return Err("transport is shutting down".into());
        }
        if let Err(e) = self.ensure_watcher() {
            self.shared.remove(peer_id);
            return Err(e);
        }
        Ok(peer_id)
    }

    /// Connect to a remote peer. Returns peer ID.
    pub fn connect(&self, addr: &str) -> Result<u64, String> {
        let dialed = Self::dial(addr)?;
        self.connect_dialed(dialed, addr)
    }

    /// First half of a client handshake, before any local QP exists:
    /// open the TCP bootstrap connection and read the server's HELLO. Its
    /// GID tells a multi-NIC client which local transport can reach that
    /// server NIC (see `crate::rail`); finish with `connect_dialed` on
    /// that transport.
    pub fn dial(addr: &str) -> Result<Dialed, String> {
        let targets: Vec<SocketAddr> = addr
            .to_socket_addrs()
            .map_err(|e| format!("resolve {addr}: {e}"))?
            .collect();
        let mut last = format!("resolve {addr}: no addresses");
        let mut stream = None;
        for t in &targets {
            match TcpStream::connect_timeout(t, DIAL_TIMEOUT) {
                Ok(s) => {
                    stream = Some(s);
                    break;
                }
                Err(e) => last = format!("TCP connect to {addr} ({t}): {e}"),
            }
        }
        let mut stream = stream.ok_or(last)?;
        stream
            .set_read_timeout(Some(DIAL_TIMEOUT))
            .map_err(|e| format!("{addr}: {e}"))?;
        stream
            .set_write_timeout(Some(DIAL_TIMEOUT))
            .map_err(|e| format!("{addr}: {e}"))?;
        let (hello, hello_raw) =
            Hello::read_from(&mut stream).map_err(|e| format!("{addr}: {e}"))?;
        Ok(Dialed {
            stream,
            hello,
            hello_raw,
        })
    }

    /// Second half of a client handshake: build this transport's lanes,
    /// authenticate with the credentials set by `set_credentials`, and
    /// connect the lanes to the server's. Returns peer ID.
    pub fn connect_dialed(&self, dialed: Dialed, addr: &str) -> Result<u64, String> {
        let Dialed {
            mut stream,
            hello,
            hello_raw,
        } = dialed;
        let lanes_wanted = self.lanes_per_peer.min(usize::from(hello.max_lanes)).max(1);
        let (sgid_index, lid, gid) = self.local_address();
        let mtu = self.device.active_mtu()?;

        let raw: Vec<RawLane> = (0..lanes_wanted)
            .map(|_| self.new_lane())
            .collect::<Result<_, _>>()?;
        let local = Endpoint {
            lid,
            gid,
            qpns: raw.iter().map(|l| l.qp.qp_num).collect(),
        };
        let creds = self
            .credentials
            .read()
            .clone()
            .unwrap_or_else(|| crate::auth::Credentials::new("", ""));
        let auth = bootstrap::encode_auth(&hello_raw, &creds, &local)
            .map_err(|e| format!("{addr}: {e}"))?;
        stream
            .write_all(&auth)
            .map_err(|e| format!("send AUTH to {addr}: {e}"))?;
        let server = bootstrap::read_verdict(&mut stream, lanes_wanted)
            .map_err(|e| format!("{addr}: {e}"))?;

        // The server will RDMA-WRITE GET ranges into, and RDMA-READ PUT
        // sources out of, this transport's pool: grant exactly what the
        // pool was registered for.
        let access = qp_remote_access(Side::Dialed, self.pool.access());
        let lanes = raw
            .into_iter()
            .zip(&server.qpns)
            .enumerate()
            .map(|(i, (l, &qpn))| self.finish_lane(i, l, qpn, &server, access, mtu, sgid_index))
            .collect::<Result<Vec<_>, _>>()?;
        // The stream now only signals liveness; no more timed reads.
        let _ = stream.set_read_timeout(None);
        let _ = stream.set_write_timeout(None);

        let n = lanes.len();
        let peer_id = self.insert_peer(PeerConnection {
            lanes,
            next_lane: AtomicU64::new(0),
            addr: addr.to_string(),
            control_txn: Arc::new(Mutex::new(())),
            role: None,
            access_key: None,
            stream: Arc::new(stream),
            _ticket: None,
            _claim: None,
        })?;
        info!(peer_id, addr, lanes = n, "RDMA connected");
        Ok(peer_id)
    }

    /// Start listening for incoming peers on every IPv4 address
    /// (`0.0.0.0`), as the other nvstor TCP listeners do. See
    /// [`Self::listen_on`] for IPv6.
    pub fn listen(self: &Arc<Self>, port: u16) -> Result<SocketAddr, String> {
        self.listen_on(IpAddr::V4(std::net::Ipv4Addr::UNSPECIFIED), port)
    }

    /// Start listening for incoming peers on `addr`. `::` listens on every
    /// IPv4 and IPv6 address (dual stack; IPv4 alone where the host has no
    /// IPv6); any other address on that address only.
    pub fn listen_on(self: &Arc<Self>, addr: IpAddr, port: u16) -> Result<SocketAddr, String> {
        let listener =
            bind_listener(addr, port).map_err(|e| format!("bind {addr} port {port}: {e}"))?;
        let addr = listener
            .local_addr()
            .map_err(|e| format!("local_addr: {e}"))?;
        listener
            .set_nonblocking(true)
            .map_err(|e| format!("nonblocking: {e}"))?;

        let transport = Arc::clone(self);
        let handle = thread::Builder::new()
            .name(format!("rdma-listen-{port}"))
            .spawn(move || transport.accept_loop(listener, port))
            .map_err(|e| format!("spawn listener: {e}"))?;

        *self.listener_handle.lock() = Some(handle);
        Ok(addr)
    }

    fn accept_loop(self: Arc<Self>, listener: TcpListener, port: u16) {
        info!(port, "RDMA transport listening");
        loop {
            if self.shared.shutdown.load(Ordering::Relaxed) {
                break;
            }
            match listener.accept() {
                Ok((stream, a)) => {
                    // Admit before spending anything on the connector.
                    // An IPv4 connector on the dual-stack socket arrives as
                    // ::ffff:a.b.c.d; count and log it as the IPv4 address.
                    let a = SocketAddr::new(a.ip().to_canonical(), a.port());
                    let ticket = match self.gate.try_admit(a.ip()) {
                        Ok(t) => t,
                        Err(why) => {
                            warn!(%a, why, "RDMA connection refused");
                            continue;
                        }
                    };
                    debug!(%a, "incoming RDMA peer");
                    let t = Arc::clone(&self);
                    let spawned =
                        thread::Builder::new()
                            .name("rdma-accept".into())
                            .spawn(move || {
                                match t.accept_peer(stream, ticket) {
                                    Ok(_) => {}
                                    // A connect that closes before AUTH (a
                                    // reachability check, a port scanner) is not
                                    // a failed handshake.
                                    Err(e) if e.quiet => {
                                        debug!(%a, error = %e.msg, "connection closed before AUTH")
                                    }
                                    Err(e) => warn!(%a, error = %e.msg, "RDMA accept failed"),
                                }
                            });
                    if let Err(e) = spawned {
                        // The stream and ticket were moved into the
                        // closure, which the failed spawn dropped.
                        warn!(%a, error = %e, "RDMA accept: cannot spawn a handshake thread");
                    }
                }
                Err(ref e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(50));
                }
                Err(e) => {
                    warn!("accept error: {e}");
                    thread::sleep(Duration::from_millis(100));
                }
            }
        }
    }

    fn accept_peer(
        &self,
        mut stream: TcpStream,
        mut ticket: AcceptTicket,
    ) -> Result<u64, AcceptError> {
        stream
            .set_nonblocking(false)
            .map_err(|e| format!("blocking: {e}"))?;
        let timeout = self.gate.limits().handshake_timeout;
        stream
            .set_read_timeout(Some(timeout))
            .map_err(|e| format!("read timeout: {e}"))?;
        stream
            .set_write_timeout(Some(timeout))
            .map_err(|e| format!("write timeout: {e}"))?;
        let peer_addr = stream
            .peer_addr()
            .map(|a| SocketAddr::new(a.ip().to_canonical(), a.port()).to_string())
            .unwrap_or_default();

        let (sgid_index, lid, gid) = self.local_address();
        let max_lanes = u16::try_from(self.lanes_per_peer).unwrap_or(u16::MAX);
        let hello = Hello::new(max_lanes, lid, gid)?;
        let verifier = self.verifier.read().clone();
        let admitter = self.admitter.read().clone();

        // No verbs object exists until `serve_admitted` has authenticated
        // the connector, the admitter has room for it, and it calls this
        // closure.
        let (role, claim, access_key, lanes) = bootstrap::serve_identified(
            &mut stream,
            &hello,
            verifier.as_ref(),
            admitter.as_ref(),
            |_, remote| {
                let mtu = self.device.active_mtu()?;
                let lanes = remote
                    .qpns
                    .iter()
                    .enumerate()
                    .map(|(i, &qpn)| {
                        let raw = self.new_lane()?;
                        // A server never lets a peer touch its memory: GET
                        // pushes with RDMA WRITE and PUT pulls with RDMA READ,
                        // both initiated here.
                        self.finish_lane(
                            i,
                            raw,
                            qpn,
                            remote,
                            qp_remote_access(Side::Accepted, self.pool.access()),
                            mtu,
                            sgid_index,
                        )
                    })
                    .collect::<Result<Vec<_>, String>>()?;
                let local = Endpoint {
                    lid,
                    gid,
                    qpns: lanes.iter().map(|l| l.qp.qp_num).collect(),
                };
                Ok((local, lanes))
            },
        )
        .map_err(|e| AcceptError {
            quiet: closed_before_auth(&e),
            msg: format!("{peer_addr}: {e}"),
        })?;

        let _ = stream.set_read_timeout(None);
        let _ = stream.set_write_timeout(None);
        ticket.establish();
        let n = lanes.len();
        let peer_id = self.insert_peer(PeerConnection {
            lanes,
            next_lane: AtomicU64::new(0),
            addr: peer_addr.clone(),
            control_txn: Arc::new(Mutex::new(())),
            role: Some(role),
            access_key: Some(access_key),
            stream: Arc::new(stream),
            _ticket: Some(ticket),
            _claim: claim,
        })?;
        info!(peer_id, addr = %peer_addr, lanes = n, ?role, "RDMA peer accepted");

        // Notify any listener registered via `set_on_peer_accepted`.
        // Held lock is brief; callback runs on the accept thread, so
        // the callback contract is "spawn and return fast."
        if let Some(cb) = self.on_peer_accepted.read().clone() {
            cb(peer_id);
        }

        Ok(peer_id)
    }

    fn data_lane(&self, peer_id: u64) -> Result<Arc<QpLane>, String> {
        let peers = self.shared.peers.read();
        Ok(peers.get(&peer_id).ok_or("peer not found")?.pick_lane())
    }

    fn control_lane(&self, peer_id: u64) -> Result<Arc<QpLane>, String> {
        let peers = self.shared.peers.read();
        Ok(Arc::clone(
            &peers.get(&peer_id).ok_or("peer not found")?.lanes[0],
        ))
    }

    /// After an operation on `lane` of `peer_id` failed with `e`: if the
    /// lane is now unusable (its QP is in the error state, a control reply
    /// went missing, or the completion engine stopped), remove the peer,
    /// so callers see a disconnected peer ("peer not found") and the
    /// remote side sees end-of-stream, instead of every later operation
    /// failing on the dead lane. Nothing re-establishes the lane; a client
    /// reconnects (`RdmaSource::reconnect`, `nvstor-peer` failover).
    /// Error path only.
    fn lane_error(&self, peer_id: u64, lane: &QpLane, e: String) -> String {
        if lane.state.failed() && self.shared.remove(peer_id) {
            warn!(peer_id, error = %e, why = ?lane.state.failure(), "RDMA lane failed; peer removed");
        }
        e
    }

    /// RDMA WRITE `data` to `(remote_addr, remote_rkey)` on a connected
    /// peer: the server pushing a GET range into the client buffer the
    /// signed request named.
    pub fn write_remote_to(
        &self,
        peer_id: u64,
        data: &[u8],
        local_lkey: u32,
        remote_addr: u64,
        remote_rkey: u32,
    ) -> Result<(), TransferError> {
        self.one_sided(
            peer_id,
            OneSided::Write,
            data.as_ptr() as u64,
            data.len(),
            local_lkey,
            remote_addr,
            remote_rkey,
        )?;
        debug!(peer_id, len = data.len(), remote_addr, "RDMA WRITE");
        Ok(())
    }

    /// RDMA READ `(remote_addr, remote_rkey)` of a connected peer into
    /// `buf`: the server pulling a PUT source from the client buffer the
    /// signed request named.
    pub fn read_remote_from(
        &self,
        peer_id: u64,
        buf: &mut [u8],
        local_lkey: u32,
        remote_addr: u64,
        remote_rkey: u32,
    ) -> Result<(), TransferError> {
        self.one_sided(
            peer_id,
            OneSided::Read,
            buf.as_mut_ptr() as u64,
            buf.len(),
            local_lkey,
            remote_addr,
            remote_rkey,
        )?;
        debug!(peer_id, len = buf.len(), remote_addr, "RDMA READ");
        Ok(())
    }

    /// RDMA WRITE pieces of `data` (each its own offset, remote address
    /// and rkey) on a connected peer, as one list of WRs on one data lane:
    /// the server scattering a KV block's payload over the client's pages.
    /// Every piece must lie inside `data`. Returns once the hardware is
    /// done with `data`, as [`write_remote_to`](Self::write_remote_to).
    ///
    /// On error, any prefix of the pieces (down to part of one) may have
    /// landed: treat every remote byte the pieces name as undefined.
    pub fn write_scatter(
        &self,
        peer_id: u64,
        data: &[u8],
        lkey: u32,
        pieces: &[Piece],
    ) -> Result<(), TransferError> {
        self.post_pieces(
            peer_id,
            OneSided::Write,
            data.as_ptr() as u64,
            data.len(),
            lkey,
            pieces,
        )
    }

    /// RDMA READ each piece's remote bytes into `buf` at the piece's
    /// offset: the server gathering a KV block's pages from the client
    /// into one slot. Returns as [`read_remote_from`](Self::read_remote_from).
    ///
    /// On error, any part of `buf` the pieces name may have been written.
    pub fn read_gather(
        &self,
        peer_id: u64,
        buf: &mut [u8],
        lkey: u32,
        pieces: &[Piece],
    ) -> Result<(), TransferError> {
        self.post_pieces(
            peer_id,
            OneSided::Read,
            buf.as_mut_ptr() as u64,
            buf.len(),
            lkey,
            pieces,
        )
    }

    /// One bulk transfer on a data lane, as WRs of at most
    /// `max_wr_bytes` posted in chains (one doorbell, one completion per
    /// chain). Returns once the hardware is done with the local buffer,
    /// on success and on error alike, unless the error says the buffer is
    /// still in use ([`TransferError::buffer_in_use`]).
    #[allow(clippy::too_many_arguments)]
    fn one_sided(
        &self,
        peer_id: u64,
        op: OneSided,
        local_addr: u64,
        len: usize,
        lkey: u32,
        remote_addr: u64,
        rkey: u32,
    ) -> Result<(), TransferError> {
        let len = u64::try_from(len).map_err(|_| "transfer length does not fit in 64 bits")?;
        let segs = split_transfer(
            op,
            local_addr,
            lkey,
            remote_addr,
            rkey,
            len,
            self.max_wr_bytes(),
        )?;
        self.post_segments(peer_id, &segs)
    }

    /// Pieces of one local buffer: bounds-checked against it, then posted.
    fn post_pieces(
        &self,
        peer_id: u64,
        op: OneSided,
        local_addr: u64,
        local_len: usize,
        lkey: u32,
        pieces: &[Piece],
    ) -> Result<(), TransferError> {
        let local_len =
            u64::try_from(local_len).map_err(|_| "buffer length does not fit in 64 bits")?;
        let segs = split_pieces(op, local_addr, local_len, lkey, pieces, self.max_wr_bytes())?;
        self.post_segments(peer_id, &segs)
    }

    /// The WRs, in order, on one data lane, in chains of at most
    /// [`MAX_CHAIN_WRS`]; each chain is waited for before the next, and
    /// a failed chain ends the transfer. Every WR has one local buffer, so
    /// on an unobserved failure that buffer is the one held.
    fn post_segments(
        &self,
        peer_id: u64,
        segs: &[crate::connection::Segment],
    ) -> Result<(), TransferError> {
        if segs.is_empty() {
            return Ok(());
        }
        let lane = self.data_lane(peer_id)?;
        let max_batch = MAX_CHAIN_WRS.min(lane.qp.max_send_wr() as usize);
        for range in plan_posts(segs.len(), max_batch) {
            lane.run_chain(&segs[range]).map_err(|e| TransferError {
                held_by: e.unobserved.then(|| Arc::clone(&lane.state)),
                msg: self.lane_error(peer_id, &lane, e.msg),
            })?;
        }
        Ok(())
    }

    // ---------------------------------------------------------------
    // Two-sided control plane (S3-over-RDMA Request/Response over
    // SEND/RECV on lane 0).
    //
    // We pin control traffic to lane 0 so the SEND-side serialization
    // (one in-flight SEND per channel) doesn't bottleneck the
    // multi-lane WRITE/READ data path. Lane 0's RECV ring is
    // pre-posted at lane creation (8 buffers); each recv_request call
    // consumes one buffer and re-posts it, so depth stays steady.
    // ---------------------------------------------------------------

    /// Send a control-plane request to `peer_id`. Blocks until the
    /// SEND has completed (i.e. the bytes have left the local NIC).
    /// The caller typically follows up with `recv_response` on the
    /// same lane.
    pub fn send_request(
        &self,
        peer_id: u64,
        msg: &crate::s3_rdma_proto::Request,
    ) -> Result<(), String> {
        // Lane 0 by convention for control traffic. Clone the handle and
        // drop the peers guard before the post+poll.
        let lane = self.control_lane(peer_id)?;
        // The envelope is ALWAYS present so the frame format is
        // uniform and the server never has to guess whether a request is
        // signed. A client with no credentials sends an empty access key,
        // which the server rejects explicitly rather than treating as
        // anonymous.
        let creds = self
            .credentials
            .read()
            .clone()
            .unwrap_or_else(|| crate::auth::Credentials::new("", ""));
        let nonce = crate::auth::next_nonce()?;
        let (mut send_guard, addr, lkey, len) =
            lane.control.encode_signed_request(msg, &creds, nonce)?;
        lane.send(addr, lkey, len as u32).map_err(|e| {
            if e.unobserved {
                send_guard.poison();
            }
            self.lane_error(peer_id, &lane, e.msg)
        })?;
        // Release the SEND buffer now that the WR no longer references it
        // (on an unobserved failure it was poisoned above instead).
        drop(send_guard);
        debug!(peer_id, len, "control SEND request");
        Ok(())
    }

    /// Block waiting for the next control-plane response from
    /// `peer_id`. Returns the parsed `Response` and re-posts the
    /// RECV buffer so the ring stays full.
    pub fn recv_response(&self, peer_id: u64) -> Result<crate::s3_rdma_proto::Response, String> {
        let lane = self.control_lane(peer_id)?;
        // In-flight: we already sent the request, so the reply is owed.
        // The bound exceeds the server's worst case for answering
        // (`s3_rdma_proto::CONTROL_REPLY_TIMEOUT`), so a busy server is not
        // mistaken for a dead one. If it still does not come, the lane
        // fails and the peer is removed: a late reply left in the receive
        // queue would otherwise be taken as the reply to the next request.
        let (index, bytes_recvd) = lane
            .recv(
                Some(Instant::now() + crate::s3_rdma_proto::CONTROL_REPLY_TIMEOUT),
                true,
            )
            .map_err(|e| self.lane_error(peer_id, &lane, e))?;
        let msg = lane
            .control
            .with_recv_buf(index, bytes_recvd, |buf| {
                crate::s3_rdma_proto::Response::decode(buf)
            })
            .ok_or_else(|| format!("recv buffer {index} out of range"));
        // Re-post first: a message that does not decode must not shrink the ring.
        lane.control.repost_recv(&lane.qp, index)?;
        let msg = msg?.map_err(|e| format!("decode response: {e}"))?;
        debug!(peer_id, "control RECV response");
        Ok(msg)
    }

    /// Mirror of `recv_response` for the server side: wait for the
    /// next incoming `Request` from `peer_id` and return it.
    /// Returns the signed envelope: the auth header, the decoded request,
    /// and the raw body bytes the signature covers. The caller verifies via
    /// its `AuthFn` before acting on the request.
    pub fn recv_request(
        &self,
        peer_id: u64,
    ) -> Result<
        (
            crate::auth::AuthHeader,
            crate::s3_rdma_proto::Request,
            Vec<u8>,
        ),
        String,
    > {
        // Clone the lane and release the peers guard: this wait is
        // open-ended, and holding a read guard for minutes would freeze
        // every connect/disconnect on the transport.
        let lane = self.control_lane(peer_id)?;
        // IDLE wait: the client sends its next request on its own
        // schedule, and for checkpoint workloads that gap is legitimately
        // minutes, so there is no deadline here. When the client goes
        // away, its bootstrap stream closes, the peer is removed and its
        // QPs moved to ERR, and this wait ends with a flush CQE.
        let (index, bytes_recvd) = lane
            .recv(self.idle_recv_timeout.map(|t| Instant::now() + t), false)
            .map_err(|e| self.lane_error(peer_id, &lane, e))?;
        let msg = lane
            .control
            .with_recv_buf(index, bytes_recvd, |buf| {
                crate::s3_rdma_proto::decode_signed(buf)
            })
            .ok_or_else(|| format!("recv buffer {index} out of range"));
        // Re-post first: a message that does not decode must not shrink the ring.
        lane.control.repost_recv(&lane.qp, index)?;
        let msg = msg?.map_err(|e| format!("decode request: {e}"))?;
        debug!(peer_id, "control RECV request");
        Ok(msg)
    }

    /// Atomic one-shot transaction: send `req`, then wait for the
    /// matching `Response`. Held under a per-peer mutex so concurrent
    /// threads sharing this `RdmaTransport` see well-formed
    /// request/response pairs on lane 0 instead of stealing each
    /// other's responses.
    pub fn request_response(
        &self,
        peer_id: u64,
        req: &crate::s3_rdma_proto::Request,
    ) -> Result<crate::s3_rdma_proto::Response, String> {
        let txn_lock = {
            let peers = self.shared.peers.read();
            let peer = peers.get(&peer_id).ok_or("peer not found")?;
            Arc::clone(&peer.control_txn)
        };
        let _guard = txn_lock.lock();
        self.send_request(peer_id, req)?;
        self.recv_response(peer_id)
    }

    /// Send one control message to `peer_id` on lane 0, encoded by
    /// `encode` straight into the registered SEND buffer (which it returns
    /// the length of). Blocks until the SEND has completed. For protocols
    /// other than S3-over-RDMA (the KV service), which sign their own
    /// requests.
    pub fn send_message(
        &self,
        peer_id: u64,
        encode: impl FnOnce(&mut [u8]) -> std::io::Result<usize>,
    ) -> Result<(), String> {
        let lane = self.control_lane(peer_id)?;
        let (mut send_guard, addr, lkey, len) = lane.control.encode_with(encode)?;
        lane.send(addr, lkey, len as u32).map_err(|e| {
            if e.unobserved {
                send_guard.poison();
            }
            self.lane_error(peer_id, &lane, e.msg)
        })?;
        drop(send_guard);
        debug!(peer_id, len, "control SEND message");
        Ok(())
    }

    /// Wait for the next control message from `peer_id` on lane 0 and
    /// decode it with `decode`, then re-post its RECV buffer. `deadline`
    /// bounds the wait; `reply_owed` says a reply to our request is due
    /// (a missed deadline then fails the lane, as for `recv_response`),
    /// otherwise the wait is idle (as for `recv_request`).
    pub fn recv_message<T>(
        &self,
        peer_id: u64,
        deadline: Option<Instant>,
        reply_owed: bool,
        decode: impl FnOnce(&[u8]) -> std::io::Result<T>,
    ) -> Result<T, String> {
        let lane = self.control_lane(peer_id)?;
        // An owed reply is always bounded, so a lost one fails the lane
        // instead of pairing with the next request.
        let deadline = match (deadline, reply_owed) {
            (None, true) => Some(Instant::now() + crate::s3_rdma_proto::CONTROL_REPLY_TIMEOUT),
            (d, _) => d,
        };
        let (index, bytes_recvd) = lane
            .recv(deadline, reply_owed)
            .map_err(|e| self.lane_error(peer_id, &lane, e))?;
        let msg = lane
            .control
            .with_recv_buf(index, bytes_recvd, decode)
            .ok_or_else(|| format!("recv buffer {index} out of range"));
        // Re-post before reporting a decode error, so a bad message does
        // not shrink the ring.
        lane.control.repost_recv(&lane.qp, index)?;
        let msg = msg?.map_err(|e| format!("decode message: {e}"))?;
        debug!(peer_id, "control RECV message");
        Ok(msg)
    }

    /// One request/reply exchange on lane 0, under the peer's
    /// transaction lock: concurrent callers sharing a peer each get their
    /// own reply (the receive ring is not routed by request id). The
    /// reply is waited for until `deadline` (the standard reply timeout if
    /// `None`); a missed deadline fails the lane.
    pub fn message_exchange<T>(
        &self,
        peer_id: u64,
        encode: impl FnOnce(&mut [u8]) -> std::io::Result<usize>,
        deadline: Option<Instant>,
        decode: impl FnOnce(&[u8]) -> std::io::Result<T>,
    ) -> Result<T, String> {
        let txn_lock = {
            let peers = self.shared.peers.read();
            Arc::clone(&peers.get(&peer_id).ok_or("peer not found")?.control_txn)
        };
        let _guard = txn_lock.lock();
        self.send_message(peer_id, encode)?;
        self.recv_message(peer_id, deadline, true, decode)
    }

    /// Mirror of `send_request` for the server side: send a
    /// `Response` back over the same lane.
    pub fn send_response(
        &self,
        peer_id: u64,
        msg: &crate::s3_rdma_proto::Response,
    ) -> Result<(), String> {
        let lane = self.control_lane(peer_id)?;
        let (mut send_guard, addr, lkey, len) = lane.control.encode_response(msg)?;
        lane.send(addr, lkey, len as u32).map_err(|e| {
            if e.unobserved {
                send_guard.poison();
            }
            self.lane_error(peer_id, &lane, e.msg)
        })?;
        drop(send_guard);
        debug!(peer_id, len, "control SEND response");
        Ok(())
    }

    /// Set the credentials this transport authenticates with when it
    /// dials (bootstrap AUTH) and signs control requests with. Set
    /// before `connect`: a server with a verifier refuses a connector
    /// without valid credentials.
    pub fn set_credentials(&self, creds: crate::auth::Credentials) {
        *self.credentials.write() = Some(creds);
    }

    pub fn pool(&self) -> &MemoryPool {
        &self.pool
    }
    pub fn pool_arc(&self) -> Arc<MemoryPool> {
        Arc::clone(&self.pool)
    }
    pub fn peer_count(&self) -> usize {
        self.shared.peers.read().len()
    }

    /// The authenticated role of a peer this transport accepted; `None`
    /// for a dialed or unknown peer.
    pub fn peer_role(&self, peer_id: u64) -> Option<PeerRole> {
        self.shared.peers.read().get(&peer_id).and_then(|p| p.role)
    }

    /// The access key a peer this transport accepted authenticated with
    /// in the bootstrap; `None` for an unknown or dialled peer.
    pub fn peer_access_key(&self, peer_id: u64) -> Option<String> {
        self.shared
            .peers
            .read()
            .get(&peer_id)
            .and_then(|p| p.access_key.clone())
    }

    /// The bootstrap address of a peer.
    pub fn peer_addr(&self, peer_id: u64) -> Option<String> {
        self.shared
            .peers
            .read()
            .get(&peer_id)
            .map(|p| p.addr.clone())
    }

    pub fn is_available() -> bool {
        RdmaDevice::open_default().is_ok()
    }

    /// Tear a peer down on both ends: its QPs go to ERR, its bootstrap
    /// stream is closed (the remote sees end-of-stream and removes its
    /// side), and `on_peer_closed` runs. False if it was already gone.
    pub fn disconnect(&self, peer_id: u64) -> bool {
        self.shared.remove(peer_id)
    }

    /// Clean shutdown: stop listener, disconnect every peer, stop the
    /// liveness watcher.
    pub fn shutdown(&self) {
        self.shared.shutdown.store(true, Ordering::Relaxed);
        if let Some(h) = self.listener_handle.lock().take() {
            let _ = h.join();
        }
        let ids: Vec<u64> = self.shared.peers.read().keys().copied().collect();
        for id in ids {
            self.shared.remove(id);
        }
        if let Some(h) = self.watcher_handle.lock().take() {
            let _ = h.join();
        }
        info!("RDMA transport shut down");
    }
}

impl Drop for RdmaTransport {
    fn drop(&mut self) {
        self.shared.shutdown.store(true, Ordering::Relaxed);
        if let Some(h) = self.listener_handle.lock().take() {
            let _ = h.join();
        }
        let ids: Vec<u64> = self.shared.peers.read().keys().copied().collect();
        for id in ids {
            self.shared.remove(id);
        }
        if let Some(h) = self.watcher_handle.lock().take() {
            let _ = h.join();
        }
    }
}

/// Why an accept failed; `quiet` when the connector just closed before
/// sending AUTH (logged at debug, not warn).
struct AcceptError {
    msg: String,
    quiet: bool,
}

impl From<String> for AcceptError {
    fn from(msg: String) -> Self {
        Self { msg, quiet: false }
    }
}

/// Whether a bootstrap failed only because the connector closed the
/// connection before (or while) the HELLO / AUTH exchange: a plain TCP
/// connect, as the fetcher's reachability check or a port scanner does.
fn closed_before_auth(e: &bootstrap::BootstrapError) -> bool {
    matches!(e, bootstrap::BootstrapError::Io(io) if matches!(io.kind(),
        std::io::ErrorKind::UnexpectedEof | std::io::ErrorKind::ConnectionReset
        | std::io::ErrorKind::BrokenPipe | std::io::ErrorKind::ConnectionAborted))
}

/// Which end of a connection this transport is.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Side {
    /// We accepted it: we are the server.
    Accepted,
    /// We dialed it: we are the client.
    Dialed,
}

/// What the peer may do through one of our QPs. A server initiates every
/// one-sided operation (GET pushes with RDMA WRITE, PUT pulls with RDMA
/// READ), so its QPs grant nothing, whatever its pool allows and whatever
/// role the peer authenticated as. A client grants what its pool was
/// registered for: REMOTE_WRITE for GET targets, REMOTE_READ for PUT
/// sources.
fn qp_remote_access(side: Side, pool: Access) -> Access {
    match side {
        Side::Accepted => Access::LOCAL,
        Side::Dialed => pool,
    }
}

/// Whether a bootstrap stream's peer has gone: end-of-stream, a socket
/// error, or any byte at all (the stream carries nothing after the
/// handshake, so data is a protocol violation).
fn stream_closed(s: &TcpStream) -> bool {
    let mut b = [0u8; 1];
    // SAFETY: peeking at most 1 byte into a stack buffer we own.
    let r = unsafe {
        libc::recv(
            s.as_raw_fd(),
            b.as_mut_ptr().cast(),
            1,
            libc::MSG_PEEK | libc::MSG_DONTWAIT,
        )
    };
    if r >= 0 {
        return true;
    }
    let e = std::io::Error::last_os_error();
    !matches!(
        e.kind(),
        std::io::ErrorKind::WouldBlock | std::io::ErrorKind::Interrupted
    )
}

/// Liveness watcher: remove every peer whose bootstrap stream closes.
fn watch(shared: Arc<Shared>) {
    while !shared.shutdown.load(Ordering::Relaxed) {
        // Hold the streams while polling so no descriptor is closed (and
        // reused) under the poll.
        let snapshot: Vec<(u64, Arc<TcpStream>)> = shared
            .peers
            .read()
            .iter()
            .map(|(id, p)| (*id, Arc::clone(&p.stream)))
            .collect();
        if snapshot.is_empty() {
            thread::sleep(WATCH_INTERVAL);
            continue;
        }
        let mut fds: Vec<libc::pollfd> = snapshot
            .iter()
            .map(|(_, s)| libc::pollfd {
                fd: s.as_raw_fd(),
                events: libc::POLLIN,
                revents: 0,
            })
            .collect();
        // SAFETY: `fds` is a live array of `fds.len()` pollfds.
        let n = unsafe {
            libc::poll(
                fds.as_mut_ptr(),
                fds.len() as libc::nfds_t,
                WATCH_INTERVAL.as_millis() as i32,
            )
        };
        if n < 0 {
            let e = std::io::Error::last_os_error();
            if e.kind() != std::io::ErrorKind::Interrupted {
                warn!(error = %e, "RDMA liveness poll failed");
                thread::sleep(WATCH_INTERVAL);
            }
            continue;
        }
        for (pfd, (id, s)) in fds.iter().zip(&snapshot) {
            if pfd.revents != 0 && stream_closed(s) && shared.remove(*id) {
                info!(
                    peer_id = *id,
                    "RDMA peer closed its bootstrap stream; removed"
                );
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // An IPv6 --source must be able to reach the bootstrap: the listener
    // takes IPv4 and IPv6 connectors on one port.
    #[test]
    fn listener_is_dual_stack() {
        let l = bind_dual_stack(0).expect("bind");
        let port = l.local_addr().expect("addr").port();
        let v6_host = TcpListener::bind("[::1]:0").is_ok();
        for addr in [format!("127.0.0.1:{port}"), format!("[::1]:{port}")] {
            if addr.starts_with('[') && !v6_host {
                eprintln!("no IPv6 loopback on this host; only the IPv4 half is checked");
                continue;
            }
            let _c = TcpStream::connect(&addr).unwrap_or_else(|e| panic!("{addr}: {e}"));
            let (_, from) = l.accept().expect("accept");
            assert!(from.ip().to_canonical().is_loopback(), "{from}");
        }
        // The port is held for both families.
        assert!(bind_dual_stack(port).is_err());
    }

    // An explicit address listens there only: 0.0.0.0 (the default of
    // `listen`) takes IPv4 and not IPv6; 127.0.0.1 not other addresses.
    #[test]
    fn listener_on_an_explicit_address_stays_there() {
        let v4 = bind_listener(IpAddr::V4(std::net::Ipv4Addr::UNSPECIFIED), 0).expect("bind");
        let port = v4.local_addr().expect("addr").port();
        assert!(v4.local_addr().expect("addr").is_ipv4());
        TcpStream::connect(("127.0.0.1", port)).expect("v4 connects");
        if TcpListener::bind("[::1]:0").is_ok() {
            assert!(
                TcpStream::connect(("::1", port)).is_err(),
                "0.0.0.0 must not take IPv6"
            );
        }
        let any6 = bind_listener(IpAddr::V6(std::net::Ipv6Addr::UNSPECIFIED), 0).expect("bind");
        let port6 = any6.local_addr().expect("addr").port();
        TcpStream::connect(("127.0.0.1", port6)).expect(":: takes IPv4 too");
        let lo = bind_listener(IpAddr::V4(std::net::Ipv4Addr::LOCALHOST), 0).expect("bind");
        assert_eq!(
            lo.local_addr().expect("addr").ip(),
            IpAddr::V4(std::net::Ipv4Addr::LOCALHOST)
        );
    }

    // A transfer error whose completion was never seen
    // keeps the buffer's owner with the lane until the QP is destroyed;
    // any other error drops it at once.
    #[test]
    fn transfer_errors_hold_buffers_only_while_the_hardware_may_use_them() {
        struct Tracked(Arc<AtomicU64>);
        impl Drop for Tracked {
            fn drop(&mut self) {
                self.0.fetch_add(1, Ordering::SeqCst);
            }
        }
        let drops = Arc::new(AtomicU64::new(0));
        let done = TransferError::from("WC error");
        assert!(!done.buffer_in_use());
        done.hold(Tracked(Arc::clone(&drops)));
        assert_eq!(drops.load(Ordering::SeqCst), 1);

        let d = crate::completion::Dispatch::new(4, 16, 16);
        let lane = d.register_lane(4, 0).expect("lane");
        let pending = TransferError {
            msg: "no completion".into(),
            held_by: Some(Arc::clone(&lane)),
        };
        assert!(pending.buffer_in_use());
        assert!(pending
            .to_string()
            .contains("held until the QP is destroyed"));
        pending.hold(Tracked(Arc::clone(&drops)));
        assert_eq!((drops.load(Ordering::SeqCst), lane.held()), (1, 1));
        d.unregister_lane(lane.key(), 0, true);
        assert_eq!(drops.load(Ordering::SeqCst), 2);
    }

    // Removing a peer wakes a dispatcher blocked in an
    // idle receive (no deadline) on any of its lanes, even when stopping
    // the QP brings no flush completion, so it drops its lane reference.
    #[test]
    fn removing_a_peer_wakes_its_idle_receivers() {
        struct NoFlush;
        impl SendQp for NoFlush {
            fn stop(&self) -> Result<(), String> {
                Ok(())
            }
            fn post_fence(&self, _: u64) -> Result<(), i32> {
                Ok(())
            }
        }
        let d = crate::completion::Dispatch::new(4, 16, 16);
        let lane = d.register_lane(4, 2).expect("lane");
        let (tx, rx) = std::sync::mpsc::channel();
        let waiter = {
            let lane = Arc::clone(&lane);
            thread::spawn(move || {
                let _ = tx.send(lane.recv(None, false));
            })
        };
        thread::sleep(Duration::from_millis(20));
        retire_lanes(1, std::iter::once((&NoFlush as &dyn SendQp, &*lane)));
        let r = rx
            .recv_timeout(Duration::from_secs(5))
            .expect("the idle receiver woke");
        assert!(r.is_err());
        waiter.join().expect("join");
        assert!(lane.failed());
    }

    // A connect that closes before AUTH is logged at debug;
    // other handshake failures keep warn. (No log-capture seam exists, so
    // the classification is what is tested.)
    #[test]
    fn a_connect_closed_before_auth_is_quiet() {
        use bootstrap::BootstrapError as E;
        for kind in [
            std::io::ErrorKind::UnexpectedEof,
            std::io::ErrorKind::ConnectionReset,
            std::io::ErrorKind::BrokenPipe,
            std::io::ErrorKind::ConnectionAborted,
        ] {
            assert!(
                closed_before_auth(&E::Io(std::io::Error::from(kind))),
                "{kind:?}"
            );
        }
        assert!(!closed_before_auth(&E::Io(std::io::Error::from(
            std::io::ErrorKind::TimedOut
        ))));
        assert!(!closed_before_auth(&E::Denied));
        assert!(!closed_before_auth(&E::Malformed("x".into())));
        assert!(!closed_before_auth(&E::NotNvstor));
    }

    #[test]
    fn rdma_available_check() {
        println!("RDMA available: {}", RdmaTransport::is_available());
    }

    // Accepted QPs grant no remote access even on a transport whose
    // pool is remotely readable (nvstor-server rail 0, the source of its
    // replication pushes); dialed QPs grant exactly the pool's access.
    #[test]
    fn server_qps_grant_no_remote_access() {
        for pool in [
            Access::LOCAL,
            Access::REMOTE_WRITE,
            Access::REMOTE_READ,
            Access::REMOTE_READ_WRITE,
        ] {
            assert_eq!(
                qp_remote_access(Side::Accepted, pool).remote_bits(),
                0,
                "{pool:?}"
            );
            assert_eq!(qp_remote_access(Side::Dialed, pool), pool);
        }
    }

    // The liveness signal. A bootstrap stream whose peer closed (or
    // shut down its write side) reads as closed; an idle open one does not.
    #[test]
    fn stream_closed_detects_end_of_stream_only() {
        let l = TcpListener::bind("127.0.0.1:0").expect("bind");
        let c = TcpStream::connect(l.local_addr().expect("addr")).expect("connect");
        let (s, _) = l.accept().expect("accept");
        assert!(!stream_closed(&s), "idle open stream is alive");
        c.shutdown(Shutdown::Write).expect("shutdown");
        // EOF arrives asynchronously over loopback; poll briefly.
        let deadline = std::time::Instant::now() + Duration::from_secs(2);
        while !stream_closed(&s) {
            assert!(std::time::Instant::now() < deadline, "EOF never observed");
            thread::sleep(Duration::from_millis(5));
        }
        // Stray bytes after the handshake are a protocol violation.
        let mut c2 = TcpStream::connect(l.local_addr().expect("addr")).expect("connect");
        let (s2, _) = l.accept().expect("accept");
        c2.write_all(b"x").expect("write");
        let deadline = std::time::Instant::now() + Duration::from_secs(2);
        while !stream_closed(&s2) {
            assert!(std::time::Instant::now() < deadline, "byte never observed");
            thread::sleep(Duration::from_millis(5));
        }
    }

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn create_transport_with_pool() {
        let dev =
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device");
        let dev = Arc::new(dev);
        let pool = Arc::new(MemoryPool::new(&dev, 4096, 8).unwrap());
        let transport = RdmaTransport::new(dev, pool);
        assert_eq!(transport.peer_count(), 0);
        assert_eq!(transport.pool().available(), 8);
    }

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn transport_with_defaults() {
        let dev =
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device");
        let transport = RdmaTransport::with_defaults(Arc::new(dev)).unwrap();
        assert_eq!(transport.pool().slot_size(), 4 * 1024 * 1024);
        assert_eq!(transport.pool().total(), 64);
    }

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn transport_shutdown() {
        let dev =
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device");
        let dev = Arc::new(dev);
        let pool = Arc::new(MemoryPool::new(&dev, 4096, 4).unwrap());
        let transport = Arc::new(RdmaTransport::new(dev, pool));
        transport.listen(0).unwrap();
        transport.shutdown();
        assert_eq!(transport.peer_count(), 0);
    }

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn lanes_per_peer_config() {
        let dev =
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device");
        let dev = Arc::new(dev);
        let pool = Arc::new(MemoryPool::new(&dev, 4096, 4).unwrap());
        let mut transport = RdmaTransport::new(dev, pool);
        assert_eq!(transport.lanes_per_peer, DEFAULT_LANES);
        transport.set_lanes_per_peer(8);
        assert_eq!(transport.lanes_per_peer, 8);
    }

    fn secured_pair(secret: &'static str) -> (Arc<RdmaTransport>, Arc<RdmaTransport>, SocketAddr) {
        let open = || {
            let dev = Arc::new(
                RdmaDevice::open_first_active()
                    .expect("hardware test requires an active RDMA device"),
            );
            let pool =
                Arc::new(MemoryPool::with_access(&dev, 1 << 20, 4, Access::LOCAL).expect("pool"));
            Arc::new(RdmaTransport::new(dev, pool))
        };
        let server = open();
        server.set_verifier(Arc::new(move |a: &bootstrap::HandshakeAuth| {
            if a.access_key == "peer" && a.verify(secret) {
                PeerRole::Client
            } else {
                PeerRole::Denied
            }
        }));
        let addr = server.listen(0).expect("listen");
        (server, open(), addr)
    }

    // An unauthenticated connector is refused and
    // the server never builds a QP for it; an authenticated one connects.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn unauthenticated_connect_is_refused_before_any_qp() {
        let (server, client, addr) = secured_pair("transport-secret-01");
        let a = format!("127.0.0.1:{}", addr.port());
        client.set_credentials(crate::auth::Credentials::new("peer", "wrong"));
        assert!(client.connect(&a).expect_err("refused").contains("refused"));
        thread::sleep(Duration::from_millis(100));
        assert_eq!(server.peer_count(), 0);
        assert_eq!(server.accept_counts(), (0, 0));
        client.set_credentials(crate::auth::Credentials::new("peer", "transport-secret-01"));
        let id = client.connect(&a).expect("authenticated connect");
        thread::sleep(Duration::from_millis(100));
        assert_eq!(server.peer_count(), 1);
        // The client's disconnect closes the stream; the server reaps.
        // The peer leaves the map first and its accept ticket goes with the
        // connection after its QPs are stopped, so wait for both.
        assert!(client.disconnect(id));
        let deadline = std::time::Instant::now() + Duration::from_secs(3);
        while server.peer_count() != 0 || server.accept_counts() != (0, 0) {
            assert!(
                std::time::Instant::now() < deadline,
                "server never reaped the peer: {} peers, accept counts {:?}",
                server.peer_count(),
                server.accept_counts()
            );
            thread::sleep(Duration::from_millis(20));
        }
        client.shutdown();
        server.shutdown();
    }

    // Step 1 of the transport plan: any number of threads post on one
    // lane at once, and each returns only once its own transfer has
    // landed. 16 threads share the single data lane of one peer; each
    // RDMA-WRITEs a pattern of its own into its own client buffer and
    // RDMA-READs it back, as chains of 16 WRs, 200 times, checking the
    // bytes after every completion. A completion routed to the wrong
    // thread makes it return early and see stale bytes.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn concurrent_transfers_on_one_lane_each_see_their_own_completion() {
        const THREADS: usize = 16;
        const SLOT: usize = 1 << 20;
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let mut server = RdmaTransport::new(
            Arc::clone(&dev),
            Arc::new(
                MemoryPool::with_access(&dev, SLOT, THREADS, Access::LOCAL).expect("server pool"),
            ),
        );
        server.set_lanes_per_peer(2); // lane 0 control, lane 1 the only data lane
        let server = Arc::new(server);
        server.set_verifier(Arc::new(|a: &bootstrap::HandshakeAuth| {
            if a.access_key == "peer" && a.verify("transport-secret-02") {
                PeerRole::Client
            } else {
                PeerRole::Denied
            }
        }));
        let accepted = Arc::new(Mutex::new(None));
        let acc = Arc::clone(&accepted);
        server.set_on_peer_accepted(Arc::new(move |id| {
            *acc.lock() = Some(id);
        }));
        let addr = server.listen(0).expect("listen");
        // The client's pool is what the server writes into and reads from.
        let client = Arc::new(RdmaTransport::new(
            Arc::clone(&dev),
            Arc::new(MemoryPool::new(&dev, SLOT, THREADS).expect("client pool")),
        ));
        client.set_credentials(crate::auth::Credentials::new("peer", "transport-secret-02"));
        client
            .connect(&format!("127.0.0.1:{}", addr.port()))
            .expect("connect");
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        let peer = loop {
            if let Some(id) = *accepted.lock() {
                break id;
            }
            assert!(std::time::Instant::now() < deadline, "no accepted peer");
            thread::sleep(Duration::from_millis(10));
        };
        // 64 KiB WRs: each 1 MiB transfer is a chain of 16.
        server.set_max_wr_bytes(64 << 10);
        let handles: Vec<_> = (0..THREADS)
            .map(|t| {
                let (server, client) = (Arc::clone(&server), Arc::clone(&client));
                thread::spawn(move || {
                    let mut remote = client.pool().checkout().expect("client slot");
                    let mut local = server.pool().checkout().expect("server slot");
                    for round in 0..200u32 {
                        let byte = (t as u32 * 31 + round) as u8;
                        local.as_mut_slice().fill(byte);
                        server
                            .write_remote_to(
                                peer,
                                local.as_slice(),
                                local.lkey(),
                                remote.addr(),
                                remote.rkey(),
                            )
                            .expect("write");
                        assert!(
                            remote.as_slice().iter().all(|&b| b == byte),
                            "thread {t} round {round}: write returned before its bytes landed"
                        );
                        let back = byte.wrapping_add(1);
                        remote.as_mut_slice().fill(back);
                        let lkey = local.lkey();
                        server
                            .read_remote_from(
                                peer,
                                local.as_mut_slice(),
                                lkey,
                                remote.addr(),
                                remote.rkey(),
                            )
                            .expect("read");
                        assert!(
                            local.as_slice().iter().all(|&b| b == back),
                            "thread {t} round {round}: read returned before its bytes landed"
                        );
                    }
                })
            })
            .collect();
        for h in handles {
            h.join().expect("transfer thread");
        }
        let engine = server.engine().expect("engine");
        assert_eq!(engine.in_flight(), 0);
        assert_eq!(engine.stray_counts(), (0, 0), "no CQE went astray");
        client.shutdown();
        server.shutdown();
    }

    // 16 KiB control messages through send_message /
    // recv_message / message_exchange; a message that does not decode is
    // reported and leaves the ring at full depth; concurrent exchanges on
    // one peer each get their own reply; the peer's access key is known.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn raw_messages_of_the_kv_size_round_trip() {
        const SIZE: usize = crate::kv_proto::KV_MSG_SIZE;
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let pool =
            || Arc::new(MemoryPool::with_access(&dev, 4096, 2, Access::LOCAL).expect("pool"));
        let mut server = RdmaTransport::new(Arc::clone(&dev), pool());
        server.set_control_msg_size(SIZE).expect("size");
        let server = Arc::new(server);
        server.set_verifier(Arc::new(|a: &bootstrap::HandshakeAuth| {
            if a.access_key == "kv-peer" && a.verify("transport-secret-04") {
                PeerRole::Client
            } else {
                PeerRole::Denied
            }
        }));
        let accepted = Arc::new(Mutex::new(None));
        let acc = Arc::clone(&accepted);
        server.set_on_peer_accepted(Arc::new(move |id| {
            *acc.lock() = Some(id);
        }));
        let addr = server.listen(0).expect("listen");
        let mut client = RdmaTransport::new(Arc::clone(&dev), pool());
        client.set_control_msg_size(SIZE).expect("size");
        let client = Arc::new(client);
        client.set_credentials(crate::auth::Credentials::new(
            "kv-peer",
            "transport-secret-04",
        ));
        let cpeer = client
            .connect(&format!("127.0.0.1:{}", addr.port()))
            .expect("connect");
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        let speer = loop {
            if let Some(id) = *accepted.lock() {
                break id;
            }
            assert!(std::time::Instant::now() < deadline, "no accepted peer");
            thread::sleep(Duration::from_millis(10));
        };
        assert_eq!(server.peer_access_key(speer).as_deref(), Some("kv-peer"));
        // The server echoes each message with its first byte incremented;
        // a message starting with 0xff is "undecodable".
        let echo = {
            let server = Arc::clone(&server);
            thread::spawn(move || {
                let mut decode_errors = 0;
                for _ in 0..(2 + 8 * 3 + 1) {
                    match server.recv_message(speer, None, false, |b| {
                        if b[0] == 0xff {
                            Err(std::io::Error::other("undecodable"))
                        } else {
                            Ok(b.to_vec())
                        }
                    }) {
                        Ok(mut m) => {
                            m[0] = m[0].wrapping_add(1);
                            server
                                .send_message(speer, |buf| {
                                    buf[..m.len()].copy_from_slice(&m);
                                    Ok(m.len())
                                })
                                .expect("reply");
                        }
                        Err(e) => {
                            assert!(e.contains("undecodable"), "{e}");
                            decode_errors += 1;
                        }
                    }
                }
                decode_errors
            })
        };
        // A full-size message.
        let full: Vec<u8> = (0..SIZE).map(|i| (i % 200) as u8).collect();
        let back = client
            .message_exchange(
                cpeer,
                |b| {
                    b.copy_from_slice(&full);
                    Ok(SIZE)
                },
                None,
                |b| Ok(b.to_vec()),
            )
            .expect("exchange");
        assert_eq!(back.len(), SIZE);
        assert_eq!(back[0], 1);
        assert_eq!(&back[1..], &full[1..]);
        // An undecodable message: no reply, and the ring keeps its depth.
        client
            .send_message(cpeer, |b| {
                b[0] = 0xff;
                Ok(1)
            })
            .expect("send");
        // More exchanges than the ring has buffers, from three threads at once.
        let handles: Vec<_> = (0..3u8)
            .map(|t| {
                let client = Arc::clone(&client);
                thread::spawn(move || {
                    for i in 0..8u8 {
                        let tag = t * 16 + i;
                        let back = client
                            .message_exchange(
                                cpeer,
                                |b| {
                                    b[0] = tag;
                                    b[1] = t;
                                    Ok(2)
                                },
                                None,
                                |b| Ok(b.to_vec()),
                            )
                            .expect("exchange");
                        assert_eq!(back, [tag + 1, t], "thread {t} got another thread's reply");
                    }
                })
            })
            .collect();
        for h in handles {
            h.join().expect("client thread");
        }
        let last = client
            .message_exchange(
                cpeer,
                |b| {
                    b[0] = 7;
                    Ok(1)
                },
                None,
                |b| Ok(b.to_vec()),
            )
            .expect("exchange");
        assert_eq!(last, [8]);
        assert_eq!(echo.join().expect("echo"), 1);
        client.shutdown();
        server.shutdown();
    }

    // One contiguous server buffer is scattered over
    // non-adjacent, differently sized pieces of a client buffer (a KV
    // block over its layer pages) and gathered back; every byte lands
    // where its piece says, and bytes between the pieces stay untouched.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn scatter_and_gather_follow_their_pieces() {
        const SLOT: usize = 1 << 20;
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let mut server = RdmaTransport::new(
            Arc::clone(&dev),
            Arc::new(MemoryPool::with_access(&dev, SLOT, 2, Access::LOCAL).expect("server pool")),
        );
        server.set_lanes_per_peer(2);
        let server = Arc::new(server);
        server.set_verifier(Arc::new(|a: &bootstrap::HandshakeAuth| {
            if a.access_key == "peer" && a.verify("transport-secret-03") {
                PeerRole::Client
            } else {
                PeerRole::Denied
            }
        }));
        let accepted = Arc::new(Mutex::new(None));
        let acc = Arc::clone(&accepted);
        server.set_on_peer_accepted(Arc::new(move |id| {
            *acc.lock() = Some(id);
        }));
        let addr = server.listen(0).expect("listen");
        let client = Arc::new(RdmaTransport::new(
            Arc::clone(&dev),
            Arc::new(MemoryPool::new(&dev, SLOT, 2).expect("client pool")),
        ));
        client.set_credentials(crate::auth::Credentials::new("peer", "transport-secret-03"));
        client
            .connect(&format!("127.0.0.1:{}", addr.port()))
            .expect("connect");
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        let peer = loop {
            if let Some(id) = *accepted.lock() {
                break id;
            }
            assert!(std::time::Instant::now() < deadline, "no accepted peer");
            thread::sleep(Duration::from_millis(10));
        };
        // 256-byte WRs: each piece spans many WRs, and the transfer spans
        // several chains, with chain boundaries inside pieces.
        server.set_max_wr_bytes(256);
        let mut local = server.pool().checkout().expect("server slot");
        let mut remote = client.pool().checkout().expect("client slot");
        // Pieces of 36,864 and 8,448 bytes, as the example layout's pages, placed
        // with gaps and in reverse order in the client buffer.
        let sizes = [36_864u64, 8_448, 36_864, 8_448, 1];
        let mut pieces = Vec::new();
        let (mut off, mut at) = (0u64, SLOT as u64);
        for len in sizes {
            at -= len + 1000;
            pieces.push(Piece {
                local_offset: off,
                remote_addr: remote.addr() + at,
                rkey: remote.rkey(),
                len,
            });
            off += len;
        }
        for (i, b) in local.as_mut_slice().iter_mut().enumerate() {
            *b = (i % 251) as u8;
        }
        remote.as_mut_slice().fill(0xee);
        let lkey = local.lkey();
        server
            .write_scatter(peer, local.as_slice(), lkey, &pieces)
            .expect("scatter");
        let r = remote.as_slice();
        let mut covered = vec![false; SLOT];
        for p in &pieces {
            let (lo, ro) = (
                p.local_offset as usize,
                (p.remote_addr - remote.addr()) as usize,
            );
            assert_eq!(
                &r[ro..ro + p.len as usize],
                &local.as_slice()[lo..lo + p.len as usize],
                "piece at {ro}"
            );
            covered[ro..ro + p.len as usize]
                .iter_mut()
                .for_each(|c| *c = true);
        }
        assert!(
            r.iter().zip(&covered).all(|(&b, &c)| c || b == 0xee),
            "a byte outside every piece changed"
        );
        // Gather back into a cleared server buffer; the rest of it is a
        // sentinel the gather must not touch.
        let total = off as usize;
        local.as_mut_slice()[..total].fill(0);
        local.as_mut_slice()[total..].fill(0xa5);
        server
            .read_gather(peer, local.as_mut_slice(), lkey, &pieces)
            .expect("gather");
        assert!(
            local.as_slice()[..total]
                .iter()
                .enumerate()
                .all(|(i, &b)| b == (i % 251) as u8),
            "gathered bytes"
        );
        assert!(
            local.as_slice()[total..].iter().all(|&b| b == 0xa5),
            "the gather wrote past its pieces"
        );
        // A piece outside the local buffer is refused before anything is posted.
        let outside = [Piece {
            local_offset: SLOT as u64 - 1,
            remote_addr: remote.addr(),
            rkey: remote.rkey(),
            len: 2,
        }];
        assert!(server
            .write_scatter(peer, local.as_slice(), lkey, &outside)
            .is_err());
        let engine = server.engine().expect("engine");
        assert_eq!(engine.in_flight(), 0);
        assert_eq!(engine.stray_counts(), (0, 0), "no CQE went astray");
        client.shutdown();
        server.shutdown();
    }
}

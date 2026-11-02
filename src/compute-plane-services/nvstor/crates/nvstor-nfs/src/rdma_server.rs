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

//! Standard NFS/RDMA forward channel (RFC 8166, RFC 8267): RDMA-CM on port
//! 20049, SEND/RECV for RPC messages, RDMA READ for WRITE arguments, RDMA
//! WRITE for READ results.
//!
//! Zero copy: READ data is read from the file directly into a slot of the
//! device's registered buffer pool and RDMA-WRITTEN to the client from that
//! slot; WRITE data is RDMA-READ from the client into a pool slot and the file
//! is written from it. Only RPC/RDMA headers are copied. When the pool is
//! exhausted (or a transfer exceeds a slot) the transfer registers its own
//! memory instead of copying, within a per-connection and a process-wide
//! budget; a transfer that does not fit waits for earlier ones to complete.
//! Read chunks are size-checked, and for inline calls checked for DDP
//! eligibility, before any memory is allocated for them.
//!
//! Pipelining: each connection's I/O thread posts work and reaps completions;
//! it never waits for an individual transfer. Up to `DEFAULT_CREDITS` calls
//! per connection run concurrently on the storage pool, and their replies
//! (WRITE chains ending in a SEND) and Read chunk fetches stay queued on the
//! NIC together.
//!
//! Bounds: a call holds one of the connection's credits until its reply is
//! posted, so replies waiting for the send queue are bounded by the credits
//! and posted ones by the send slots. A connection whose posted work makes
//! no progress for `write_timeout` (a client that stopped taking replies) is
//! torn down, and shutdown waits at most DRAIN_GRACE for undelivered replies.

use super::*;
use crate::ddp::{DdpAllocator, DdpArg, DdpBuffer};
use crate::rpcrdma::{
    self,
    chunks::{self, ChunkUse, ReplyPlan, Source},
    RpcRdmaError,
};
use nvstor_rdma::cm::{
    BufferPool, Connection, Event, Limits, PendingConnection, PoolBuf, RegisteredVec, Wake, Wr,
};
use std::collections::{HashSet, VecDeque};
use std::io;
use std::sync::mpsc::{self as sync_mpsc, TryRecvError};
use std::time::Duration;
use tracing::trace;

const POLL_INTERVAL: Duration = Duration::from_millis(100);
/// Busy-poll the completion queue this long after activity before sleeping.
const SPIN: Duration = Duration::from_micros(50);
/// Pool slot size: the advertised maxread/maxwrite.
const POOL_SLOT: usize = 1 << 20;
/// Largest READ or WRITE payload (FATTR4_MAXREAD/MAXWRITE), so the largest
/// non-zero Read chunk a client has reason to send.
const MAX_IO: usize = POOL_SLOT;
/// RPC and COMPOUND headers around a maximal WRITE that is sent whole as a
/// position-zero long call (no DDP, e.g. under krb5i).
const LONG_CALL_HEADROOM: usize = 64 << 10;
/// Most Read chunk bytes one call may carry.
const MAX_CALL_CHUNK_BYTES: usize = MAX_IO + LONG_CALL_HEADROOM;
/// On-demand registrations one connection may hold: every credit's worth of
/// maximal transfers, so a compliant client is never limited by it.
const CONN_PINNED_BYTES: usize = chunks::DEFAULT_CREDITS as usize * MAX_CALL_CHUNK_BYTES;
/// On-demand registrations of all connections, as a multiple of the
/// per-device pool (`rdma_pool_bytes`): 4 GiB with the default pool.
const PINNED_POOL_MULTIPLE: usize = 16;
/// Send-queue entries per connection. A reply or fetch uses at most
/// MAX_SEGMENTS + 1 entries, so any single one always fits an empty queue.
const SEND_QUEUE: u32 = 1024;
/// Error replies beyond one per credit.
const SPARE_SENDS: u32 = 8;
/// Interval of the per-connection debug statistics line.
const STATS_INTERVAL: Duration = Duration::from_secs(2);
/// How long an accepted connection may go without its first call.
const FIRST_CALL_TIMEOUT: Duration = Duration::from_secs(15);
/// After shutdown starts, how long replies may wait for a client that has
/// stopped taking them. Admitted storage work is always waited for.
const DRAIN_GRACE: Duration = Duration::from_secs(5);

/// The standard NFS/RDMA port (RFC 8267).
pub const DEFAULT_NFS_RDMA_PORT: u16 = 20049;

/// None auto-enables the standard port on an active RDMA device. Some(0)
/// disables RDMA. An explicitly selected port must bind successfully.
pub fn bind_rdma_listener(port: Option<u16>) -> io::Result<Option<RdmaListener>> {
    let explicit = port.is_some();
    let port = match port {
        Some(0) => return Ok(None),
        Some(port) => port,
        None => match nvstor_rdma::device::RdmaDevice::open_first_active() {
            Ok(_) => DEFAULT_NFS_RDMA_PORT,
            Err(error) => {
                debug!(%error, "NFS/RDMA auto-detection found no active device");
                return Ok(None);
            }
        },
    };
    // Dual-stack: IPv4 peers (front-end NIC) and IPv6 peers (GPU rail
    // interfaces, whose only routable addresses are IPv6).
    match RdmaListener::bind((std::net::Ipv6Addr::UNSPECIFIED, port).into()) {
        Ok(listener) => {
            info!(port, "NFS/RDMA listener bound");
            Ok(Some(listener))
        }
        Err(error) if explicit => Err(io::Error::new(
            error.kind(),
            format!("NFS/RDMA port {port}: {error}"),
        )),
        Err(error) => {
            warn!(port, %error, "NFS/RDMA auto-detection could not bind an RC listener");
            Ok(None)
        }
    }
}

#[derive(Clone)]
struct Context {
    processor: Arc<CompoundProcessor>,
    policy: Arc<ServerPolicy>,
    metrics: Arc<NfsMetrics>,
    config: NfsServerConfig,
    runtime: tokio::runtime::Handle,
    stop: Arc<std::sync::atomic::AtomicBool>,
    /// On-demand registrations of every connection.
    pinned: Arc<Budget>,
}

struct ConnectionCount {
    active: Arc<AtomicUsize>,
    metrics: Arc<NfsMetrics>,
    _peer: PeerSlot,
}

impl Drop for ConnectionCount {
    fn drop(&mut self) {
        self.active.fetch_sub(1, Ordering::Relaxed);
        self.metrics
            .active_connections
            .fetch_sub(1, Ordering::Relaxed);
    }
}

impl NfsServer {
    /// Serve NFS/RDMA on `listener`, bound on `port`, until shutdown,
    /// surviving a fatal listener error (device removal) as `on_failure`
    /// says: with `Rebind` the failure is logged, `nvstor_nfs_rdma_listener_up`
    /// drops to 0 and the listener is re-created on `port` with backoff
    /// (1 s doubling to 30 s) while the rest of the server keeps serving;
    /// with `Exit` the error is returned.
    pub async fn run_rdma_supervised(
        &self,
        listener: RdmaListener,
        port: u16,
        on_failure: RdmaListenerFailure,
        shutdown: tokio::sync::watch::Receiver<bool>,
    ) -> io::Result<()> {
        super::supervise_listener(
            listener,
            || {
                bind_rdma_listener(Some(port))?.ok_or_else(|| {
                    io::Error::new(
                        io::ErrorKind::InvalidInput,
                        "NFS/RDMA port 0 cannot be rebound",
                    )
                })
            },
            |listener| self.run_rdma_until_shutdown(listener, shutdown.clone()),
            on_failure,
            (Duration::from_secs(1), Duration::from_secs(30)),
            &self.metrics,
            shutdown.clone(),
        )
        .await
    }

    /// Serve RDMA beside TCP using the same processor, policy, metrics, and
    /// global connection limit. RDMA-CM binding is explicit and never falls
    /// back to TCP when the selected device or address cannot serve RDMA.
    pub async fn run_rdma_until_shutdown(
        &self,
        mut listener: RdmaListener,
        shutdown: tokio::sync::watch::Receiver<bool>,
    ) -> io::Result<()> {
        let context = Context {
            processor: self.processor.clone(),
            policy: self.policy.clone(),
            metrics: self.metrics.clone(),
            config: self.config.clone(),
            runtime: tokio::runtime::Handle::current(),
            stop: Arc::new(std::sync::atomic::AtomicBool::new(false)),
            pinned: Budget::new(
                self.config
                    .rdma_pool_bytes
                    .saturating_mul(PINNED_POOL_MULTIPLE),
            ),
        };
        let active = self.active_connections.clone();
        let peers = self.peers.clone();
        tokio::task::spawn_blocking(move || {
            let mut workers: Vec<std::thread::JoinHandle<()>> = Vec::new();
            let mut failures = 0u32;
            let result = loop {
                if stopping(&shutdown) {
                    break Ok(());
                }
                let mut i = 0;
                while i < workers.len() {
                    if workers[i].is_finished() {
                        if workers.swap_remove(i).join().is_err() {
                            warn!("NFS/RDMA connection thread panicked");
                        }
                    } else {
                        i += 1;
                    }
                }
                let pending = match listener.next(POLL_INTERVAL) {
                    Ok(Some(pending)) => {
                        failures = 0;
                        pending
                    }
                    Ok(None) => continue,
                    Err(error) if listener_error_is_fatal(&error) => {
                        tracing::error!(%error, "NFS/RDMA listener failed; no further RDMA connections are accepted");
                        break Err(io::Error::new(error.kind(), format!("NFS/RDMA listener: {error}")));
                    }
                    Err(error) => {
                        // One connect request failed (and was rejected); the
                        // listener itself is fine. Back off so persistent
                        // fd or memory exhaustion does not spin.
                        failures = failures.saturating_add(1);
                        let delay = accept_backoff(failures);
                        warn!(%error, failures, delay_ms = delay.as_millis() as u64,
                            "NFS/RDMA connect request failed; continuing");
                        std::thread::sleep(delay);
                        continue;
                    }
                };
                debug!(addr = %pending.addr, private_len = pending.private.len(), "NFS/RDMA connect request");
                if !is_client_allowed(&pending.addr, &context.config.exports) {
                    warn!(addr = %pending.addr, "NFS/RDMA connect rejected: not in the export's client list (allow_clients / --allow-client)");
                    continue;
                }
                let Some(peer) = peers.acquire(pending.addr.ip()) else {
                    warn!(
                        addr = %pending.addr,
                        max = peers.limit,
                        "NFS/RDMA connect rejected: connection limit per client address reached"
                    );
                    continue;
                };
                if active
                    .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |n| {
                        (n < context.config.max_connections).then_some(n + 1)
                    })
                    .is_err()
                {
                    warn!(
                        addr = %pending.addr,
                        max = context.config.max_connections,
                        "NFS/RDMA connect rejected: max_connections reached"
                    );
                    continue;
                }
                context
                    .metrics
                    .active_connections
                    .fetch_add(1, Ordering::Relaxed);
                let guard = ConnectionCount {
                    active: active.clone(),
                    metrics: context.metrics.clone(),
                    _peer: peer,
                };
                let ctx = context.clone();
                let stop = shutdown.clone();
                let addr = pending.addr;
                match std::thread::Builder::new()
                    .name("nfs-rdma-io".into())
                    .spawn(move || {
                        let _guard = guard;
                        if let Err(error) = serve_connection(pending, ctx, stop) {
                            debug!(%addr, %error, "NFS/RDMA connection closed");
                        }
                    }) {
                    Ok(worker) => workers.push(worker),
                    Err(error) => warn!(%error, "NFS/RDMA could not start connection thread"),
                }
            };
            drop(listener);
            context.stop.store(true, Ordering::Release);
            // Connection shutdown waits for admitted storage work. Do not
            // report drained while a worker can still mutate persistent data.
            for worker in workers {
                if worker.join().is_err() {
                    warn!("NFS/RDMA connection thread panicked");
                }
            }
            result
        })
        .await
        .map_err(io::Error::other)?
    }
}

/// Whether an error from [`RdmaListener::next`] ends the accept loop. Only a
/// failure of the listener itself does: device removal (ENODEV), or its
/// event channel reporting an error or hangup (ECONNRESET) or being invalid
/// (EBADF, EINVAL). Everything else concerns one connect request, which the
/// bridge has already rejected: allocation failure (ENOMEM), fd exhaustion
/// creating the connection's event channel (EMFILE, ENFILE), a failed id
/// migration, an interrupted read, or a peer that is not an IP address.
fn listener_error_is_fatal(error: &io::Error) -> bool {
    matches!(
        error.raw_os_error(),
        Some(libc::ENODEV | libc::ECONNRESET | libc::EBADF | libc::EINVAL)
    )
}

/// Pause after the `failures`-th consecutive failed connect request: 10 ms,
/// doubling, at most 1 s.
fn accept_backoff(failures: u32) -> Duration {
    Duration::from_millis(10u64 << failures.saturating_sub(1).min(7)).min(Duration::from_secs(1))
}

fn stopping(shutdown: &tokio::sync::watch::Receiver<bool>) -> bool {
    *shutdown.borrow() || shutdown.has_changed().is_err()
}

fn bad(message: impl Into<String>) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message.into())
}

/// A limit on bytes registered on demand (outside the device pools).
struct Budget {
    used: AtomicUsize,
    limit: usize,
}

impl Budget {
    fn new(limit: usize) -> Arc<Self> {
        Arc::new(Self {
            used: AtomicUsize::new(0),
            limit,
        })
    }

    /// Charge `bytes` if they fit. A budget with nothing charged takes any
    /// single charge, so one transfer larger than the limit still progresses
    /// once everything before it is released.
    fn charge(&self, bytes: usize) -> bool {
        self.used
            .fetch_update(Ordering::AcqRel, Ordering::Acquire, |used| {
                let total = used.checked_add(bytes)?;
                (used == 0 || total <= self.limit).then_some(total)
            })
            .is_ok()
    }

    fn release(&self, bytes: usize) {
        self.used.fetch_sub(bytes, Ordering::AcqRel);
    }
}

/// Bytes charged to a connection's and the process-wide on-demand budgets,
/// returned to both when the registration they cover is dropped.
struct Pin {
    conn: Arc<Budget>,
    global: Arc<Budget>,
    bytes: usize,
}

impl Pin {
    fn take(conn: &Arc<Budget>, global: &Arc<Budget>, bytes: usize) -> Option<Self> {
        if !conn.charge(bytes) {
            return None;
        }
        if !global.charge(bytes) {
            conn.release(bytes);
            return None;
        }
        Some(Self {
            conn: conn.clone(),
            global: global.clone(),
            bytes,
        })
    }
}

impl Drop for Pin {
    fn drop(&mut self) {
        self.conn.release(self.bytes);
        self.global.release(self.bytes);
    }
}

/// Registered memory the NIC can use directly.
enum Dma {
    Pool(PoolBuf),
    /// Registered on demand; dropped (deregistered) before its charge.
    Owned {
        buf: RegisteredVec,
        _pin: Pin,
    },
}

impl DdpBuffer for Dma {
    fn bytes(&self) -> &[u8] {
        match self {
            Dma::Pool(buf) => buf.as_slice(),
            Dma::Owned { buf, .. } => buf.as_slice(),
        }
    }
    fn bytes_mut(&mut self) -> &mut [u8] {
        match self {
            Dma::Pool(buf) => buf.as_mut_slice(),
            Dma::Owned { buf, .. } => buf.as_mut_slice(),
        }
    }
    fn registration(&self) -> Option<(u64, u32)> {
        Some(match self {
            Dma::Pool(buf) => (buf.addr(), buf.lkey()),
            Dma::Owned { buf, .. } => (buf.addr(), buf.lkey()),
        })
    }
}

/// READ results leased from the device pool. Never blocks: an exhausted pool
/// makes the processor use an inline result, which the reply registers.
struct PoolAllocator(Arc<BufferPool>);

impl DdpAllocator for PoolAllocator {
    fn lease(&self, len: usize) -> Option<Box<dyn DdpBuffer>> {
        if len > self.0.slot_size() {
            return None;
        }
        self.0
            .lease()
            .map(|buf| Box::new(Dma::Pool(buf)) as Box<dyn DdpBuffer>)
    }
}

/// Memory of a posted reply, released when its SEND completes. Fields drop
/// in order: registrations before the charge that covers them.
struct ReplyHold {
    _payload: Option<RegisteredVec>,
    _items: Vec<Box<dyn DdpBuffer>>,
    _copies: Vec<RegisteredVec>,
    _pin: Option<Pin>,
    posted: Instant,
}

/// A call waiting for its Read chunks. `bufs` and `wrs` stay empty until
/// memory is available (see [`Session::allocate`]); then `bufs[i]` receives
/// chunk i.
struct Fetch {
    call: chunks::Call,
    /// How each non-zero chunk is delivered, when the inline reduced message
    /// let it be decided before the fetch (RDMA_MSG). None: position-zero
    /// call, classified once the reduced message itself has been fetched.
    uses: Option<Vec<ChunkUse>>,
    bufs: Vec<Dma>,
    wrs: Vec<Wr>,
    received: Instant,
    posted: Instant,
}

enum Outgoing {
    Reply(ReplyPlan, Instant),
    /// An RPC/RDMA header-only message (RDMA_ERROR).
    Message(Vec<u8>),
    Fetch(Box<Fetch>),
}

/// Bytes `post_reply` registers on demand for `plan`: the payload when a
/// transfer reads from it, and a copy of every unregistered item.
fn reply_registration_bytes(plan: &ReplyPlan) -> usize {
    let payload = if plan
        .transfers
        .iter()
        .any(|t| matches!(t.source, Source::Payload(_)))
    {
        plan.payload.capacity().max(1)
    } else {
        0
    };
    plan.items
        .iter()
        .filter(|item| item.registration().is_none())
        .fold(payload, |n, item| {
            n.saturating_add(item.bytes().len().max(1))
        })
}

/// Check a call's Read chunks before any memory is allocated or pinned for
/// them: every chunk within the advertised I/O size, and, for RDMA_MSG,
/// each non-zero chunk DDP-eligible in the inline reduced message (WRITE
/// data or a symlink target, RFC 8267 6.1). Returns the chunk uses when
/// they are known now. Errors are answered with ERR_CHUNK.
fn preflight(call: &chunks::Call) -> Result<Option<Vec<ChunkUse>>, RpcRdmaError> {
    let mut total = 0usize;
    for chunk in &call.reads {
        let len = chunk_len(chunk);
        let limit = if chunk.position == 0 {
            MAX_CALL_CHUNK_BYTES
        } else {
            MAX_IO
        };
        if len > limit {
            return Err(RpcRdmaError::InvalidHeader(format!(
                "Read chunk of {len} bytes exceeds {limit}"
            )));
        }
        total = total.saturating_add(len);
    }
    if total > MAX_CALL_CHUNK_BYTES {
        return Err(RpcRdmaError::InvalidHeader(format!(
            "Read chunks of {total} bytes exceed {MAX_CALL_CHUNK_BYTES}"
        )));
    }
    if call.reads.first().is_some_and(|c| c.position == 0) {
        return Ok(None);
    }
    rpcrdma::nfs::classify_read_chunks(&call.inline, &call.reads).map(Some)
}

/// Where a connection's time goes, reported at debug level every
/// STATS_INTERVAL and when the connection closes.
#[derive(Default)]
struct Stats {
    calls: u64,
    /// Bytes RDMA-written to the client (READ data, Reply chunks).
    written: u64,
    /// Bytes RDMA-read from the client (WRITE data, long calls).
    read: u64,
    /// Receive (or fetch completion) to reply ready: storage and NFS work.
    worker: Duration,
    /// Reply ready to posted: waiting for send-queue room.
    queued: Duration,
    /// Posted to SEND completion: the wire and the client.
    wire: Duration,
    replies: u64,
    /// Posted to RDMA READ completion.
    fetch: Duration,
    fetches: u64,
    /// On-demand registrations (pool exhausted or not applicable).
    registrations: u64,
    max_inflight: usize,
    max_outgoing: usize,
}

impl Stats {
    fn report(
        &self,
        addr: &std::net::SocketAddr,
        elapsed: Duration,
        pool: Option<&Arc<BufferPool>>,
    ) {
        let avg = |total: Duration, n: u64| {
            if n == 0 {
                0
            } else {
                (total / n as u32).as_micros() as u64
            }
        };
        let secs = elapsed.as_secs_f64().max(1e-9);
        debug!(%addr, calls = self.calls,
            write_gib_s = format!("{:.2}", self.written as f64 / secs / (1u64 << 30) as f64),
            read_gib_s = format!("{:.2}", self.read as f64 / secs / (1u64 << 30) as f64),
            worker_us = avg(self.worker, self.calls), queued_us = avg(self.queued, self.replies),
            wire_us = avg(self.wire, self.replies), fetch_us = avg(self.fetch, self.fetches),
            registrations = self.registrations, max_inflight = self.max_inflight,
            max_outgoing = self.max_outgoing, pool_free = pool.map(|p| p.available()),
            "NFS/RDMA stats");
    }
}

/// RFC 8166 4.3 flow control as the responder sees it. The client counts a
/// call against its credits until the reply arrives, so a call is owed from
/// its receive until its reply (or RDMA_ERROR) is posted: an unposted reply
/// cannot have been delivered. A compliant client therefore never has more
/// than the granted credits owed, however slowly it takes replies. One that
/// does is ignoring flow control and is disconnected rather than queued
/// for without bound. Posted replies are bounded by the send slots.
#[derive(Debug)]
struct Credits {
    owed: u32,
    granted: u32,
}

impl Credits {
    fn new(granted: u32) -> Self {
        Self { owed: 0, granted }
    }

    /// A message arrived that will be answered.
    fn take(&mut self) -> io::Result<()> {
        if self.owed >= self.granted {
            return Err(bad(format!(
                "RPC/RDMA client exceeded {} credits (replies not yet posted)",
                self.granted
            )));
        }
        self.owed += 1;
        Ok(())
    }

    /// The reply to an owed message was posted.
    fn release(&mut self) {
        debug_assert!(self.owed > 0, "reply posted for a call that was not owed");
        self.owed = self.owed.saturating_sub(1);
    }
}

/// What the connection loop does next, from [`Watchdog::check`].
#[derive(Debug, PartialEq, Eq)]
enum Verdict {
    Run,
    /// Close cleanly, for the given reason.
    Close(&'static str),
    /// Posted work has made no progress for the send timeout.
    Stalled,
}

/// Per-connection deadlines, checked once per I/O loop iteration.
///
/// A client that stops taking replies (no receive buffers posted, so every
/// SEND is RNR-NAKed, possibly forever: the passive QP's RNR retry count
/// comes from the client's CM request) leaves the send queue stuck. Such a
/// connection is never idle, so the idle timeout cannot catch it; instead it
/// is torn down after `write_timeout` without a completion, like a TCP
/// reply write that does not drain.
///
/// A connection that has sent no call FIRST_CALL_TIMEOUT after it was
/// accepted (the CM handshake never completed, or the client is only
/// holding a connection slot) is closed well before the idle timeout: a
/// real client sends its first call as soon as the connection is up.
struct Watchdog {
    opened: Instant,
    called: bool,
    last_activity: Instant,
    /// Since when posted work has completed nothing (None: nothing posted).
    stalled_since: Option<Instant>,
    draining_since: Option<Instant>,
}

impl Watchdog {
    fn new(now: Instant) -> Self {
        Self {
            opened: now,
            called: false,
            last_activity: now,
            stalled_since: None,
            draining_since: None,
        }
    }

    /// A message arrived from the client.
    fn activity(&mut self, now: Instant) {
        self.called = true;
        self.last_activity = now;
    }

    /// After a poll: whether any posted chain completed, and how many remain.
    fn progress(&mut self, now: Instant, completed: bool, outstanding: usize) {
        if outstanding == 0 {
            self.stalled_since = None;
        } else if completed || self.stalled_since.is_none() {
            self.stalled_since = Some(now);
        }
    }

    fn check(
        &mut self,
        now: Instant,
        idle: bool,
        draining: bool,
        idle_timeout: Duration,
        send_timeout: Duration,
    ) -> Verdict {
        if draining {
            let since = *self.draining_since.get_or_insert(now);
            if idle || now.duration_since(since) >= DRAIN_GRACE {
                return Verdict::Close("shutdown");
            }
        }
        if self
            .stalled_since
            .is_some_and(|since| now.duration_since(since) >= send_timeout)
        {
            return Verdict::Stalled;
        }
        if !self.called && now.duration_since(self.opened) >= FIRST_CALL_TIMEOUT.min(idle_timeout) {
            return Verdict::Close("no call after connecting");
        }
        if idle && now.duration_since(self.last_activity) >= idle_timeout {
            return Verdict::Close("idle timeout");
        }
        Verdict::Run
    }
}

fn chunk_len(chunk: &chunks::ReadChunk) -> usize {
    chunk.segments.iter().map(|s| s.length as usize).sum()
}

struct Session<'a> {
    conn: Connection,
    context: &'a Context,
    addr: std::net::SocketAddr,
    inline_limit: usize,
    pool: Option<Arc<BufferPool>>,
    alloc: Option<Arc<dyn DdpAllocator>>,
    wake: Arc<Wake>,
    tx: sync_mpsc::Sender<(chunks::Call, Option<RpcReply>, Instant)>,
    tasks: tokio::task::JoinSet<()>,
    /// Admitted calls not yet answered by a worker (duplicate xid check).
    inflight: HashSet<u32>,
    credits: Credits,
    outgoing: VecDeque<Outgoing>,
    /// This connection's on-demand registrations.
    pinned: Arc<Budget>,
    stats: Stats,
}

impl Session<'_> {
    /// On-demand registration of `data`, if the budgets allow it.
    fn register(&mut self, data: Vec<u8>) -> io::Result<Option<(RegisteredVec, Pin)>> {
        let Some(pin) = Pin::take(&self.pinned, &self.context.pinned, data.capacity().max(1))
        else {
            return Ok(None);
        };
        self.stats.registrations += 1;
        Ok(Some((self.conn.register(data)?, pin)))
    }

    fn reject(&mut self, xid: u32, error: &RpcRdmaError) {
        self.outgoing
            .push_back(Outgoing::Message(rpcrdma::build_error_reply(
                xid,
                chunks::DEFAULT_CREDITS,
                error,
            )));
    }

    /// A message from the client: dispatch it, or queue the fetch of its Read
    /// chunks. Nothing is allocated for a fetch here; see [`Self::allocate`].
    fn receive(&mut self, data: &[u8]) -> io::Result<()> {
        // RFC 8166 4.5: neither case permits a transport error reply.
        if data.len() < 28 || data[12..16] == rpcrdma::RDMA_ERROR.to_be_bytes() {
            return Ok(());
        }
        trace!(addr = %self.addr, len = data.len(), head = ?&data[..28], "NFS/RDMA receive");
        let xid = u32::from_be_bytes(
            data[..4]
                .try_into()
                .expect("invariant: complete transport header"),
        );
        // Every message from here on is answered, by a reply or RDMA_ERROR.
        self.credits.take()?;
        let mut call = match chunks::parse(data) {
            Ok(call) => call,
            Err(error) => {
                self.reject(xid, &error);
                return Ok(());
            }
        };
        if self.inflight.contains(&xid) {
            return Err(bad("RPC/RDMA duplicate in-flight xid"));
        }
        let received = Instant::now();
        if call.reads.is_empty() {
            let record = std::mem::take(&mut call.inline);
            self.admit(xid);
            self.dispatch(call, record, Vec::new(), received);
            return Ok(());
        }
        if self.conn.read_depth() == 0 {
            self.reject(
                xid,
                &RpcRdmaError::InvalidHeader("client allows no RDMA READ".into()),
            );
            return Ok(());
        }
        let uses = match preflight(&call) {
            Ok(uses) => uses,
            Err(error) => {
                debug!(addr = %self.addr, xid, %error, "NFS/RDMA Read chunks rejected");
                self.reject(xid, &error);
                return Ok(());
            }
        };
        self.admit(xid);
        let fetch = Fetch {
            call,
            uses,
            bufs: Vec::new(),
            wrs: Vec::new(),
            received,
            posted: received,
        };
        self.outgoing.push_back(Outgoing::Fetch(Box::new(fetch)));
        Ok(())
    }

    /// Memory for every Read chunk of `fetch`, and the RDMA READs that fill
    /// it. A chunk that fits a slot leases one from the device pool; others
    /// (and all when the pool is empty) register memory against the
    /// connection's and the process-wide budgets. Returns false, holding
    /// nothing, when a budget is exhausted: the fetch waits until completions
    /// release memory, instead of pinning ever more.
    fn allocate(&mut self, fetch: &mut Fetch) -> io::Result<bool> {
        let mut bufs = Vec::with_capacity(fetch.call.reads.len());
        let mut wrs = Vec::new();
        for chunk in &fetch.call.reads {
            let len = chunk_len(chunk);
            let leased = self
                .pool
                .as_ref()
                .filter(|p| len <= p.slot_size())
                .and_then(|p| p.lease());
            let buf = match leased {
                Some(buf) => Dma::Pool(buf),
                None => match self.register(vec![0; len])? {
                    Some((buf, pin)) => Dma::Owned { buf, _pin: pin },
                    None => return Ok(false),
                },
            };
            let (base, lkey) = buf.registration().expect("invariant: Dma is registered");
            let mut offset = 0u64;
            for s in &chunk.segments {
                if s.length > 0 {
                    wrs.push(Wr::new(base + offset, lkey, s.length, s.offset, s.handle));
                }
                offset += s.length as u64;
            }
            bufs.push(buf);
        }
        self.stats.read += wrs.iter().map(|w| w.len as u64).sum::<u64>();
        fetch.bufs = bufs;
        fetch.wrs = wrs;
        Ok(true)
    }

    fn admit(&mut self, xid: u32) {
        self.inflight.insert(xid);
        self.stats.calls += 1;
        self.stats.max_inflight = self.stats.max_inflight.max(self.inflight.len());
    }

    /// Read chunks arrived: rebuild the call, WRITE data left in place.
    fn fetched(&mut self, fetch: Box<Fetch>) -> io::Result<()> {
        let Fetch {
            mut call,
            uses,
            bufs,
            received,
            posted,
            ..
        } = *fetch;
        self.stats.fetch += posted.elapsed();
        self.stats.fetches += 1;
        let xid = call.xid;
        let zero = call.reads.first().is_some_and(|c| c.position == 0);
        let reduced = if zero {
            bufs[0].bytes()[..chunk_len(&call.reads[0])].to_vec()
        } else {
            std::mem::take(&mut call.inline)
        };
        let first = usize::from(zero);
        let uses = match uses {
            Some(uses) => Ok(uses),
            None => rpcrdma::nfs::classify_read_chunks(&reduced, &call.reads),
        };
        let rebuilt = uses.and_then(|uses| {
            let data: Vec<&[u8]> = bufs[first..]
                .iter()
                .zip(&call.reads[first..])
                .map(|(buf, chunk)| &buf.bytes()[..chunk_len(chunk)])
                .collect();
            let (record, placed) =
                chunks::rebuild(xid, &reduced, &call.reads[first..], &uses, &data)?;
            Ok((record, placed, uses))
        });
        let (record, placed, uses) = match rebuilt {
            Ok(rebuilt) => rebuilt,
            Err(error) => {
                self.inflight.remove(&xid);
                self.reject(xid, &error);
                return Ok(());
            }
        };
        let mut bufs: Vec<Option<Dma>> = bufs.into_iter().map(Some).collect();
        let mut args = Vec::with_capacity(placed.len());
        for (index, offset) in placed {
            debug_assert_eq!(uses[index], ChunkUse::Placed);
            let chunk = &call.reads[first + index];
            let buf = bufs[first + index]
                .take()
                .ok_or_else(|| bad("Read chunk placed twice"))?;
            args.push(DdpArg {
                offset,
                len: chunk_len(chunk),
                buf: Box::new(buf),
            });
        }
        self.dispatch(call, record, args, received);
        Ok(())
    }

    fn dispatch(
        &mut self,
        call: chunks::Call,
        record: Vec<u8>,
        args: Vec<DdpArg>,
        received: Instant,
    ) {
        let ctx = self.context.clone();
        let tx = self.tx.clone();
        let wake = self.wake.clone();
        let alloc = self.alloc.clone();
        self.tasks.spawn_on(
            async move {
                let input = RdmaInput { alloc, args };
                let reply = dispatch_record_on_transport(
                    record,
                    &ctx.processor,
                    &ctx.policy,
                    &ctx.metrics,
                    Some(input),
                )
                .await;
                let _ = tx.send((call, reply, received));
                wake.notify();
            },
            &self.context.runtime,
        );
    }

    /// A worker finished: plan the reply (RDMA WRITEs from where the data is).
    fn replied(
        &mut self,
        call: chunks::Call,
        reply: Option<RpcReply>,
        received: Instant,
    ) -> io::Result<()> {
        let xid = call.xid;
        self.inflight.remove(&xid);
        self.stats.worker += received.elapsed();
        let Some(reply) = reply else {
            return Err(bad("unparseable RPC request"));
        };
        trace!(addr = %self.addr, xid, reply = reply.bytes.len(), "NFS/RDMA reply");
        let credits = call.credits.clamp(1, chunks::DEFAULT_CREDITS);
        match chunks::plan_reply(
            &call,
            credits,
            reply.bytes,
            reply.data_items,
            self.inline_limit,
        ) {
            Ok(plan) => self
                .outgoing
                .push_back(Outgoing::Reply(plan, Instant::now())),
            Err(error) => self
                .outgoing
                .push_back(Outgoing::Message(rpcrdma::build_error_reply(
                    xid, credits, &error,
                ))),
        }
        Ok(())
    }

    /// Post queued work, in order, while the send queue, the send slots and
    /// the on-demand registration budgets have room.
    fn post(&mut self) -> io::Result<()> {
        self.stats.max_outgoing = self.stats.max_outgoing.max(self.outgoing.len());
        while let Some(item) = self.outgoing.pop_front() {
            if let Some(item) = self.try_post(item)? {
                self.outgoing.push_front(item);
                break;
            }
        }
        Ok(())
    }

    /// Post `item`, or hand it back when it cannot be posted yet.
    fn try_post(&mut self, item: Outgoing) -> io::Result<Option<Outgoing>> {
        match item {
            Outgoing::Reply(plan, ready) => {
                if !self.conn.can_post_reply(plan.transfers.len()) {
                    return Ok(Some(Outgoing::Reply(plan, ready)));
                }
                let pin = match reply_registration_bytes(&plan) {
                    0 => None,
                    bytes => match Pin::take(&self.pinned, &self.context.pinned, bytes) {
                        Some(pin) => Some(pin),
                        None => return Ok(Some(Outgoing::Reply(plan, ready))),
                    },
                };
                self.stats.queued += ready.elapsed();
                self.credits.release();
                self.post_reply(plan, pin)?;
            }
            Outgoing::Message(header) => {
                if !self.conn.can_post_reply(0) {
                    return Ok(Some(Outgoing::Message(header)));
                }
                self.credits.release();
                // SAFETY: no RDMA WRITEs; the header is copied before return.
                unsafe { self.conn.post_reply(&header, &[], Box::new(()))? }
            }
            Outgoing::Fetch(mut fetch) => {
                if fetch.bufs.is_empty() && !self.allocate(&mut fetch)? {
                    return Ok(Some(Outgoing::Fetch(fetch)));
                }
                if fetch.wrs.is_empty() {
                    // Only empty chunks: nothing to read.
                    self.fetched(fetch)?;
                } else if !self.conn.can_post_reads(fetch.wrs.len()) {
                    return Ok(Some(Outgoing::Fetch(fetch)));
                } else {
                    fetch.posted = Instant::now();
                    let wrs = std::mem::take(&mut fetch.wrs);
                    // SAFETY: every WR targets memory owned by `fetch.bufs`,
                    // which the connection holds until the READ chain completes.
                    unsafe { self.conn.post_reads(&wrs, fetch)? }
                }
            }
        }
        Ok(None)
    }

    /// Post a planned reply. `pin` covers its on-demand registrations
    /// ([`reply_registration_bytes`]).
    fn post_reply(&mut self, plan: ReplyPlan, pin: Option<Pin>) -> io::Result<()> {
        let ReplyPlan {
            payload,
            items,
            transfers,
            send,
        } = plan;
        let payload = if transfers
            .iter()
            .any(|t| matches!(t.source, Source::Payload(_)))
        {
            self.stats.registrations += 1;
            Some(self.conn.register(payload)?)
        } else {
            None
        };
        // Items come from this transport's allocator and are registered; any
        // other buffer is copied once into registered memory.
        let mut copies = Vec::new();
        let mut keys = Vec::with_capacity(items.len());
        for item in &items {
            keys.push(match item.registration() {
                Some(key) => key,
                None => {
                    self.stats.registrations += 1;
                    let copy = self.conn.register(item.bytes().to_vec())?;
                    let key = (copy.addr(), copy.lkey());
                    copies.push(copy);
                    key
                }
            });
        }
        let mut wrs = Vec::with_capacity(transfers.len());
        for t in &transfers {
            let (local, lkey) = match &t.source {
                Source::Payload(range) => {
                    let reg = payload
                        .as_ref()
                        .expect("invariant: payload registered for payload sources");
                    (reg.addr() + range.start as u64, reg.lkey())
                }
                Source::Item { item, range } => {
                    let (base, lkey) = keys[*item];
                    (base + range.start as u64, lkey)
                }
            };
            wrs.push(Wr::new(
                local,
                lkey,
                t.remote.length,
                t.remote.offset,
                t.remote.handle,
            ));
        }
        self.stats.written += wrs.iter().map(|w| w.len as u64).sum::<u64>();
        let hold = ReplyHold {
            _payload: payload,
            _items: items,
            _copies: copies,
            _pin: pin,
            posted: Instant::now(),
        };
        // SAFETY: every WR's local range lies in the payload registration, a
        // registered item, or a registered copy, all owned by `hold`.
        unsafe { self.conn.post_reply(&send, &wrs, Box::new(hold)) }
    }

    fn idle(&self) -> bool {
        self.inflight.is_empty() && self.outgoing.is_empty() && self.conn.outstanding() == 0
    }
}

fn serve_connection(
    pending: PendingConnection,
    context: Context,
    shutdown: tokio::sync::watch::Receiver<bool>,
) -> io::Result<()> {
    let inline_limit = chunks::peer_inline_limit(&pending.private);
    let addr = pending.addr;
    let conn = pending.accept(
        Limits {
            message_size: chunks::INLINE_SIZE,
            receives: chunks::DEFAULT_CREDITS,
            sends: chunks::DEFAULT_CREDITS + SPARE_SENDS,
            send_queue: SEND_QUEUE,
        },
        &chunks::private_data(),
    )?;
    let pool = match conn.pool(
        POOL_SLOT,
        (context.config.rdma_pool_bytes / POOL_SLOT).max(1),
    ) {
        Ok(pool) => Some(pool),
        Err(error) => {
            warn!(%addr, %error, "NFS/RDMA buffer pool unavailable; registering per transfer");
            None
        }
    };
    debug!(%addr, inline_limit, read_depth = conn.read_depth(),
        pool_free = pool.as_ref().map(|p| p.available()), "NFS/RDMA connection accepted");
    let (tx, rx) = sync_mpsc::channel();
    let mut session = Session {
        conn,
        context: &context,
        addr,
        inline_limit,
        alloc: pool
            .clone()
            .map(|p| Arc::new(PoolAllocator(p)) as Arc<dyn DdpAllocator>),
        pool,
        wake: Arc::new(Wake::new()?),
        tx,
        tasks: tokio::task::JoinSet::new(),
        inflight: HashSet::new(),
        credits: Credits::new(chunks::DEFAULT_CREDITS),
        outgoing: VecDeque::new(),
        pinned: Budget::new(CONN_PINNED_BYTES),
        stats: Stats::default(),
    };
    let mut events = Vec::with_capacity(64);
    let opened = Instant::now();
    let mut watchdog = Watchdog::new(opened);
    let mut window = (Instant::now(), 0u64);
    let result = (|| -> io::Result<()> {
        loop {
            while let Some(result) = session.tasks.try_join_next() {
                result.map_err(io::Error::other)?;
            }
            loop {
                match rx.try_recv() {
                    Ok((call, reply, received)) => session.replied(call, reply, received)?,
                    Err(TryRecvError::Empty) => break,
                    Err(TryRecvError::Disconnected) => {
                        return Err(bad("reply workers disconnected"))
                    }
                }
            }
            session.post()?;
            if window.0.elapsed() >= STATS_INTERVAL {
                if session.stats.calls != window.1 {
                    session
                        .stats
                        .report(&addr, window.0.elapsed(), session.pool.as_ref());
                    session.stats = Stats::default();
                }
                window = (Instant::now(), session.stats.calls);
            }
            let draining = stopping(&shutdown) || context.stop.load(Ordering::Acquire);
            let verdict = watchdog.check(
                Instant::now(),
                session.idle(),
                draining,
                context.config.idle_timeout,
                context.config.write_timeout,
            );
            match verdict {
                Verdict::Run => {}
                Verdict::Close(reason) => {
                    debug!(%addr, reason, "NFS/RDMA connection closing");
                    return Ok(());
                }
                Verdict::Stalled => {
                    warn!(%addr, secs = context.config.write_timeout.as_secs(), owed = session.credits.owed,
                        posted = session.conn.outstanding(), "NFS/RDMA client stopped taking replies; disconnecting");
                    return Err(io::Error::new(
                        io::ErrorKind::TimedOut,
                        "RDMA send queue made no progress",
                    ));
                }
            }
            events.clear();
            session
                .conn
                .poll(&mut events, POLL_INTERVAL, SPIN, Some(&session.wake))?;
            let mut completed = false;
            for event in events.drain(..) {
                match event {
                    Event::Receive { index, len } => {
                        let data = session.conn.received(index, len)?.to_vec();
                        session.conn.repost(index)?;
                        watchdog.activity(Instant::now());
                        // Draining admits no further calls.
                        if !draining {
                            session.receive(&data)?;
                        }
                    }
                    // Dropping the hold returns pool slots and registrations.
                    Event::Sent(hold) => {
                        completed = true;
                        if let Some(reply) = hold.downcast_ref::<ReplyHold>() {
                            session.stats.wire += reply.posted.elapsed();
                            session.stats.replies += 1;
                        }
                    }
                    Event::Read(hold) => {
                        completed = true;
                        let fetch = hold
                            .downcast::<Fetch>()
                            .map_err(|_| bad("unexpected RDMA READ completion"))?;
                        session.fetched(fetch)?;
                    }
                }
            }
            watchdog.progress(Instant::now(), completed, session.conn.outstanding());
        }
    })();
    // Destroy the QP first, so no DMA can touch memory released below; then
    // wait for every admitted NFS operation, including on disconnect and
    // timeout. Dropping a tokio task would not cancel RocksDB I/O.
    session
        .stats
        .report(&addr, window.0.elapsed(), session.pool.as_ref());
    debug!(%addr, secs = opened.elapsed().as_secs(), "NFS/RDMA connection done");
    let Session {
        conn,
        outgoing,
        mut tasks,
        ..
    } = session;
    drop(conn);
    drop(outgoing);
    drop(rx);
    context
        .runtime
        .block_on(async { while tasks.join_next().await.is_some() {} });
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn credits_count_calls_until_their_reply_is_posted() {
        let mut credits = Credits::new(2);
        credits.take().unwrap();
        credits.take().unwrap();
        // Both workers finished but neither reply is posted (send queue
        // stalled): the client is still owed both, so a third call is a
        // flow-control violation, not something to queue.
        assert!(credits.take().is_err());
        credits.release();
        credits.take().unwrap();
        assert_eq!(credits.owed, 2);
    }

    #[test]
    fn credits_bound_error_replies_too() {
        // Malformed messages are answered with RDMA_ERROR and owe a credit.
        let mut credits = Credits::new(chunks::DEFAULT_CREDITS);
        for _ in 0..chunks::DEFAULT_CREDITS {
            credits.take().unwrap();
        }
        assert!(credits.take().is_err());
    }

    const IDLE: Duration = Duration::from_secs(300);
    const SEND: Duration = Duration::from_secs(60);

    #[test]
    fn watchdog_tears_down_a_stalled_send_queue() {
        let t0 = Instant::now();
        let mut w = Watchdog::new(t0);
        w.progress(t0, false, 3);
        // Calls keep arriving, so the connection is never idle.
        w.activity(t0 + Duration::from_secs(59));
        assert_eq!(
            w.check(t0 + Duration::from_secs(59), false, false, IDLE, SEND),
            Verdict::Run
        );
        // Posting more work behind the stuck chain is not progress.
        w.progress(t0 + Duration::from_secs(30), false, 9);
        assert_eq!(
            w.check(t0 + SEND, false, false, IDLE, SEND),
            Verdict::Stalled
        );
    }

    #[test]
    fn watchdog_resets_on_completions_and_empty_queue() {
        let t0 = Instant::now();
        let mut w = Watchdog::new(t0);
        w.activity(t0);
        w.progress(t0, false, 1);
        w.progress(t0 + Duration::from_secs(50), true, 1);
        assert_eq!(
            w.check(t0 + Duration::from_secs(100), false, false, IDLE, SEND),
            Verdict::Run
        );
        w.progress(t0 + Duration::from_secs(100), false, 0);
        assert_eq!(
            w.check(t0 + Duration::from_secs(200), false, false, IDLE, SEND),
            Verdict::Run
        );
    }

    #[test]
    fn watchdog_shutdown_does_not_wait_for_a_client_that_takes_no_replies() {
        let t0 = Instant::now();
        let mut w = Watchdog::new(t0);
        w.progress(t0, false, 5);
        assert_eq!(w.check(t0, false, true, IDLE, SEND), Verdict::Run);
        assert_eq!(
            w.check(t0 + DRAIN_GRACE, false, true, IDLE, SEND),
            Verdict::Close("shutdown")
        );
        let mut w = Watchdog::new(t0);
        assert_eq!(
            w.check(t0, true, true, IDLE, SEND),
            Verdict::Close("shutdown")
        );
    }

    /// A COMPOUND (SEQUENCE, PUTFH, then `last`) as its reduced inline
    /// message, and the position where a WRITE's data would start.
    fn reduced_call(last: u32) -> (Vec<u8>, u32) {
        use crate::nfs4::{OP_GETATTR, OP_PUTFH, OP_SEQUENCE, OP_WRITE};
        let mut w = crate::xdr::XdrWriter::new();
        for n in [42, 0, 2, 100003, 4, 1, 0, 0, 0, 0, 0, 1, 3, OP_SEQUENCE] {
            w.put_u32(n);
        }
        w.put_raw(&[0; 32]);
        w.put_u32(OP_PUTFH);
        w.put_opaque(b"filehandle");
        w.put_u32(last);
        let position = if last == OP_WRITE {
            w.put_raw(&[0; 28]);
            w.put_u32(MAX_IO as u32);
            w.len() as u32
        } else {
            assert_eq!(last, OP_GETATTR);
            w.put_u32(0);
            w.len() as u32
        };
        (w.into_bytes(), position)
    }

    fn call(inline: Vec<u8>, reads: &[(u32, u32)]) -> chunks::Call {
        chunks::Call {
            xid: 42,
            credits: 128,
            reads: reads
                .iter()
                .map(|&(position, length)| chunks::ReadChunk {
                    position,
                    segments: vec![chunks::Segment {
                        handle: 1,
                        length,
                        offset: 0,
                    }],
                })
                .collect(),
            writes: Vec::new(),
            reply: None,
            inline,
        }
    }

    #[test]
    fn preflight_accepts_a_maximal_write_and_decides_its_use_up_front() {
        let (inline, position) = reduced_call(crate::nfs4::OP_WRITE);
        let uses = preflight(&call(inline, &[(position, MAX_IO as u32)])).unwrap();
        assert_eq!(uses, Some(vec![ChunkUse::Placed]));
    }

    #[test]
    fn preflight_rejects_oversize_read_chunks_before_allocation() {
        // Parsing allows 16 MiB per call; the advertised maxwrite is 1 MiB.
        let (inline, position) = reduced_call(crate::nfs4::OP_WRITE);
        let error = preflight(&call(inline.clone(), &[(position, MAX_IO as u32 + 4)])).unwrap_err();
        assert!(matches!(error, RpcRdmaError::InvalidHeader(_)));
        // The answer is ERR_CHUNK.
        let reply = rpcrdma::build_error_reply(42, 128, &error);
        assert_eq!(reply[16..20], rpcrdma::ERR_CHUNK.to_be_bytes());
        // Long calls: room for a whole 1 MiB WRITE, not for 16 MiB.
        assert!(
            preflight(&call(Vec::new(), &[(0, MAX_CALL_CHUNK_BYTES as u32 - 4)]))
                .unwrap()
                .is_none()
        );
        assert!(preflight(&call(Vec::new(), &[(0, MAX_CALL_CHUNK_BYTES as u32 + 4)])).is_err());
        assert!(preflight(&call(Vec::new(), &[(0, 16 << 20)])).is_err());
        // Many chunks that are each allowed cannot add up past the cap.
        assert!(preflight(&call(
            Vec::new(),
            &[(0, MAX_IO as u32), (position, MAX_IO as u32)]
        ))
        .is_err());
    }

    #[test]
    fn preflight_rejects_ineligible_chunks_before_allocation() {
        // A GETATTR has no DDP-eligible argument: a Read chunk on it would
        // have pinned memory and run an RDMA READ before being rejected.
        let (inline, position) = reduced_call(crate::nfs4::OP_GETATTR);
        assert!(preflight(&call(inline, &[(position, 4096)])).is_err());
    }

    #[test]
    fn budget_caps_charges_but_always_admits_the_first() {
        let budget = Budget::new(10);
        assert!(budget.charge(6));
        assert!(!budget.charge(5));
        assert!(budget.charge(4));
        budget.release(10);
        // One transfer larger than the whole budget still progresses alone.
        assert!(budget.charge(64));
        assert!(!budget.charge(1));
        budget.release(64);
        assert_eq!(budget.used.load(Ordering::Relaxed), 0);
    }

    #[test]
    fn pins_charge_both_budgets_and_release_on_drop() {
        let conn = Budget::new(100);
        let global = Budget::new(150);
        let other = Budget::new(100);
        let a = Pin::take(&conn, &global, 80).unwrap();
        // The connection's own limit.
        assert!(Pin::take(&conn, &global, 40).is_none());
        // The process-wide limit; a refused charge leaves no residue.
        let b = Pin::take(&other, &global, 60).unwrap();
        assert!(Pin::take(&other, &global, 20).is_none());
        assert_eq!(other.used.load(Ordering::Relaxed), 60);
        drop(a);
        assert_eq!(conn.used.load(Ordering::Relaxed), 0);
        assert_eq!(global.used.load(Ordering::Relaxed), 60);
        drop(b);
        assert_eq!(global.used.load(Ordering::Relaxed), 0);
    }

    #[test]
    fn reply_registration_counts_only_unregistered_memory() {
        use crate::ddp::HeapBuffer;
        let plan = |sources: Vec<Source>| ReplyPlan {
            payload: vec![0; 100],
            items: vec![Box::new(HeapBuffer(vec![0; 30])) as Box<dyn DdpBuffer>],
            transfers: sources
                .into_iter()
                .map(|source| chunks::Transfer {
                    remote: chunks::Segment {
                        handle: 1,
                        length: 4,
                        offset: 0,
                    },
                    source,
                })
                .collect(),
            send: Vec::new(),
        };
        // Items from another allocator are copied into registered memory.
        assert_eq!(reply_registration_bytes(&plan(Vec::new())), 30);
        let payload = plan(vec![Source::Payload(0..4)]);
        assert_eq!(
            reply_registration_bytes(&payload),
            payload.payload.capacity() + 30
        );
    }

    #[test]
    fn only_listener_failures_end_the_accept_loop() {
        for errno in [libc::ENODEV, libc::ECONNRESET, libc::EBADF, libc::EINVAL] {
            assert!(
                listener_error_is_fatal(&io::Error::from_raw_os_error(errno)),
                "{errno}"
            );
        }
        // Per-request failures from br_cm_next and Listener::next.
        for errno in [
            libc::ENOMEM,
            libc::EMFILE,
            libc::ENFILE,
            libc::EINTR,
            libc::EAGAIN,
            libc::ENOBUFS,
        ] {
            assert!(
                !listener_error_is_fatal(&io::Error::from_raw_os_error(errno)),
                "{errno}"
            );
        }
        assert!(!listener_error_is_fatal(&io::Error::other(
            "RDMA peer is not an IP address"
        )));
    }

    #[test]
    fn accept_backoff_grows_and_is_capped() {
        assert_eq!(accept_backoff(1), Duration::from_millis(10));
        assert_eq!(accept_backoff(2), Duration::from_millis(20));
        assert_eq!(accept_backoff(7), Duration::from_millis(640));
        assert_eq!(accept_backoff(8), Duration::from_secs(1));
        assert_eq!(accept_backoff(u32::MAX), Duration::from_secs(1));
    }

    #[test]
    fn watchdog_closes_a_connection_that_never_calls() {
        let t0 = Instant::now();
        let mut w = Watchdog::new(t0);
        assert_eq!(
            w.check(
                t0 + FIRST_CALL_TIMEOUT - Duration::from_secs(1),
                true,
                false,
                IDLE,
                SEND
            ),
            Verdict::Run
        );
        assert_eq!(
            w.check(t0 + FIRST_CALL_TIMEOUT, true, false, IDLE, SEND),
            Verdict::Close("no call after connecting")
        );
        // After the first call, only the idle timeout applies.
        let mut w = Watchdog::new(t0);
        w.activity(t0 + Duration::from_secs(1));
        assert_eq!(
            w.check(t0 + FIRST_CALL_TIMEOUT * 2, true, false, IDLE, SEND),
            Verdict::Run
        );
        // A shorter configured idle timeout still wins.
        let mut w = Watchdog::new(t0);
        let short = Duration::from_secs(2);
        assert_ne!(w.check(t0 + short, true, false, short, SEND), Verdict::Run);
    }

    #[test]
    fn watchdog_idle_timeout_only_when_idle() {
        let t0 = Instant::now();
        let mut w = Watchdog::new(t0);
        w.activity(t0);
        assert_eq!(w.check(t0 + IDLE, false, false, IDLE, SEND), Verdict::Run);
        assert_eq!(
            w.check(t0 + IDLE, true, false, IDLE, SEND),
            Verdict::Close("idle timeout")
        );
    }
}

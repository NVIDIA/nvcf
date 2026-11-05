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

//! Lightweight node-to-node RPC over TCP: inline replication fallback,
//! existence checks, inline fetch, bucket and delete propagation.
//!
//! Bulk replication over RDMA does not use this channel: it is the
//! S3-over-RDMA REPLICATE verb (`crate::s3_rdma`), where the replica
//! RDMA-READs the bytes from the writer. The former PERSIST (0x01) and
//! FETCH (0x02) ops, which addressed the peer's RDMA pool by offset,
//! are gone together with pool advertising; their opcodes stay reserved.
//!
//! Protocol (binary, little-endian):
//!   Request:  [op:u8] [payload_len:u32] [payload]
//!   Response: [status:u8] [payload_len:u32] [payload]
//!
//! Every connection costs the server a thread, so connections are
//! admitted through the same [`AcceptGate`] the RDMA bootstrap uses
//! (concurrent handshakes, total connections, connections per source
//! address, a bound on the AUTH exchange; see
//! [`RpcServer::default_accept_limits`]). A connection over a limit is
//! closed as soon as it is accepted, with a warning naming the limit.

use std::io::{Read, Write};
use std::net::{SocketAddr, TcpListener, TcpStream};
use std::sync::Arc;
use std::thread;
use std::time::Duration;

use crate::bootstrap::{AcceptGate, AcceptLimits, AcceptTicket};

use parking_lot::RwLock;
use tracing::{debug, info, warn};

/// Hard cap on an inline RPC payload, enforced before allocation.
/// The wire carries a `u32` payload_len straight into
/// `vec![0u8; payload_len]`; without a cap a single request forces up
/// to a ~4 GiB allocation, and a handful of concurrent connections
/// exhaust memory → `handle_alloc_error → abort()`. 256 MiB
/// comfortably exceeds any inline control/persist payload (bulk data
/// rides the RDMA pool, not this path).
const MAX_RPC_PAYLOAD: usize = 256 * 1024 * 1024;

/// Default bound on each read and write of an authenticated connection
/// (`RpcServer::set_idle_timeout`). A pooled connection idle for longer
/// is closed by the server (`RpcClient` then retries on a new one), and a
/// peer that stops reading a reply cannot hold its thread and admission
/// slot forever.
const CONNECTION_IO_TIMEOUT: Duration = Duration::from_secs(30);

/// RPC operation codes.
/// Connection-level authentication. Must be the FIRST frame on every
/// connection; anything else is refused and the connection dropped.
///
/// Authenticating per connection rather than per request suits this
/// protocol: connections are long-lived and pooled (`RpcClient::get_conn`),
/// the peer identity is what matters for a node-to-node channel, and it
/// avoids signing ten separate op encoders.
const OP_AUTH: u8 = 0x00;

/// Signed context for the AUTH frame. Domain-separates this signature from
/// an s3-rdma/s3-tcp request signature, so one cannot be replayed as the
/// other even with the same credential.
const RPC_AUTH_CONTEXT: &[u8] = b"nvstor-rpc-auth-v2";

// 0x01 (PERSIST from pool offset) and 0x02 (FETCH into pool offset) are
// retired: they needed the peer's RDMA pool address and rkey, which are
// no longer advertised. Never reuse them; an old peer sending them gets
// "unknown op".
const OP_BUCKET_PUT: u8 = 0x03;
const OP_BUCKET_DELETE: u8 = 0x04;
const OP_DELETE: u8 = 0x05;
const OP_PERSIST_INLINE: u8 = 0x06; // Data sent inline (TCP fallback, no RDMA pool)
const OP_EXISTS: u8 = 0x07; // Check if object exists on remote
const OP_FETCH_INLINE: u8 = 0x08; // Read object data inline over TCP
                                  // These five opcodes are reserved protocol vocabulary — server-side
                                  // dispatch landed in advance of the request-builder code that emits
                                  // them. `dead_code` is the warning we'd otherwise eat; keeping the
                                  // constants compiles them into the binary and stops a future engineer
                                  // from accidentally reusing 0x09-0x0D for an unrelated op.
#[allow(dead_code)]
const OP_STAT: u8 = 0x09; // Return file attrs (type, size, mode, uid, gid, mtime)
#[allow(dead_code)]
const OP_LIST_DIR: u8 = 0x0A; // Return directory listing
#[allow(dead_code)]
const OP_READLINK_OP: u8 = 0x0B; // Return symlink target
#[allow(dead_code)]
const OP_GETXATTR_OP: u8 = 0x0C; // Return xattr value
#[allow(dead_code)]
const OP_LISTXATTR_OP: u8 = 0x0D; // Return xattr name list

/// RPC status codes.
const STATUS_OK: u8 = 0x00;
const STATUS_ERR: u8 = 0x01;

/// Callback for persisting an object on this node.
pub type PersistFn = Arc<dyn Fn(&str, &[u8]) -> Result<(), String> + Send + Sync>;
/// Callback for reading an object on this node.
pub type FetchFn = Arc<dyn Fn(&str) -> Result<Vec<u8>, String> + Send + Sync>;
/// Callback for creating a bucket on this node.
pub type BucketPutFn = Arc<dyn Fn(&str) -> Result<(), String> + Send + Sync>;
/// Callback for deleting a bucket on this node.
pub type BucketDeleteFn = Arc<dyn Fn(&str) -> Result<(), String> + Send + Sync>;
/// Callback for deleting an object on this node.
pub type DeleteFn = Arc<dyn Fn(&str) -> Result<(), String> + Send + Sync>;
/// A handler of an op whose only argument is one name (bucket or object
/// id); the bucket put/delete and delete handlers are all this type.
pub type NameFn = Arc<dyn Fn(&str) -> Result<(), String> + Send + Sync>;

/// File stat result for OP_STAT.
#[derive(Debug, Clone)]
pub struct FileStat {
    pub file_type: u8, // 0=regular, 1=directory, 2=symlink, 3=other
    pub size: u64,
    pub mode: u32,
    pub uid: u32,
    pub gid: u32,
    pub mtime: i64,
    pub nlink: u32,
    pub rdev: u64,
}

impl FileStat {
    pub fn to_bytes(&self) -> Vec<u8> {
        let mut buf = Vec::with_capacity(45);
        buf.push(self.file_type);
        buf.extend_from_slice(&self.size.to_le_bytes());
        buf.extend_from_slice(&self.mode.to_le_bytes());
        buf.extend_from_slice(&self.uid.to_le_bytes());
        buf.extend_from_slice(&self.gid.to_le_bytes());
        buf.extend_from_slice(&self.mtime.to_le_bytes());
        buf.extend_from_slice(&self.nlink.to_le_bytes());
        buf.extend_from_slice(&self.rdev.to_le_bytes());
        buf
    }

    pub fn from_bytes(buf: &[u8]) -> Result<Self, String> {
        if buf.len() < 45 {
            return Err("FileStat too short".into());
        }
        Ok(Self {
            file_type: buf[0],
            size: u64::from_le_bytes(buf[1..9].try_into().expect("invariant: bounds checked")),
            mode: u32::from_le_bytes(buf[9..13].try_into().expect("invariant: bounds checked")),
            uid: u32::from_le_bytes(buf[13..17].try_into().expect("invariant: bounds checked")),
            gid: u32::from_le_bytes(buf[17..21].try_into().expect("invariant: bounds checked")),
            mtime: i64::from_le_bytes(buf[21..29].try_into().expect("invariant: bounds checked")),
            nlink: u32::from_le_bytes(buf[29..33].try_into().expect("invariant: bounds checked")),
            rdev: u64::from_le_bytes(buf[33..41].try_into().expect("invariant: bounds checked")),
        })
    }
}

/// Directory entry for OP_LIST_DIR.
#[derive(Debug, Clone)]
pub struct DirEntryRpc {
    pub name: String,
    pub file_type: u8, // 0=regular, 1=directory, 2=symlink
}

/// Filesystem operation callbacks for FUSE remote serving.
pub type StatFn = Arc<dyn Fn(&str) -> Result<FileStat, String> + Send + Sync>;
pub type ListDirFn = Arc<dyn Fn(&str) -> Result<Vec<DirEntryRpc>, String> + Send + Sync>;
pub type ReadlinkFn = Arc<dyn Fn(&str) -> Result<String, String> + Send + Sync>;
pub type GetXattrFn = Arc<dyn Fn(&str, &str) -> Result<Vec<u8>, String> + Send + Sync>;
pub type ListXattrFn = Arc<dyn Fn(&str) -> Result<Vec<String>, String> + Send + Sync>;

/// RPC server that handles persist/fetch/bucket requests from RDMA peers.
pub struct RpcServer {
    persist_fn: PersistFn,
    fetch_fn: FetchFn,
    bucket_put_fn: Option<BucketPutFn>,
    bucket_delete_fn: Option<BucketDeleteFn>,
    delete_fn: Option<DeleteFn>,
    /// Credential connections must present. `None` = refuse all.
    cluster_cred: Option<crate::auth::Credentials>,
    /// Rejects replayed AUTH frames within the clock-skew window.
    replay: Arc<crate::auth::ReplayGuard>,
    stat_fn: Option<StatFn>,
    list_dir_fn: Option<ListDirFn>,
    readlink_fn: Option<ReadlinkFn>,
    getxattr_fn: Option<GetXattrFn>,
    listxattr_fn: Option<ListXattrFn>,
    shutdown: Arc<std::sync::atomic::AtomicBool>,
    admission: Arc<crate::admission::Admission>,
    /// Bounds on connections (and so threads) this server holds.
    gate: Arc<AcceptGate>,
    /// Bound on each read and write once a connection is authenticated.
    idle_timeout: Duration,
}

impl RpcServer {
    /// Bound each read and write of an authenticated connection (default
    /// 30 s): an idle pooled connection is closed after this long. Set
    /// before `start`.
    pub fn set_idle_timeout(&mut self, timeout: Duration) {
        self.idle_timeout = timeout;
    }

    /// Connection limits a new server starts with. Peers are the other
    /// nodes of the cluster; each keeps one pooled connection per peer
    /// and opens more only while concurrent operations to this node
    /// overlap, so 256 per source address leaves wide headroom while
    /// bounding the server at 1024 connection threads.
    pub fn default_accept_limits() -> AcceptLimits {
        AcceptLimits {
            max_handshakes: 64,
            max_peers: 1024,
            max_peers_per_source: 256,
            handshake_timeout: Duration::from_secs(5),
        }
    }

    /// Replace the connection limits. Applies to connections accepted
    /// from now on; connections already admitted keep their slots.
    pub fn set_accept_limits(&self, limits: AcceptLimits) {
        self.gate.set_limits(limits);
    }

    /// (connections authenticating, connections established).
    pub fn connection_counts(&self) -> (usize, usize) {
        self.gate.counts()
    }

    /// Require connections to authenticate with this credential.
    /// Without it the server refuses every connection — the RPC port
    /// exposes raw chunk fetch by object id, so failing open would let
    /// anyone who can reach it read any object on the node.
    pub fn set_cluster_credential(&mut self, creds: crate::auth::Credentials) {
        self.cluster_cred = Some(creds);
    }

    /// An RPC server over `persist_fn` / `fetch_fn`. It needs no RDMA
    /// hardware, so cross-node integration tests run anywhere.
    pub fn new(persist_fn: PersistFn, fetch_fn: FetchFn) -> Self {
        Self {
            persist_fn,
            fetch_fn,
            bucket_put_fn: None,
            bucket_delete_fn: None,
            delete_fn: None,
            cluster_cred: None,
            replay: Arc::new(crate::auth::ReplayGuard::new()),
            stat_fn: None,
            list_dir_fn: None,
            readlink_fn: None,
            getxattr_fn: None,
            listxattr_fn: None,
            shutdown: Arc::new(std::sync::atomic::AtomicBool::new(false)),
            admission: crate::admission::Admission::new(crate::admission::default_max_in_flight()),
            gate: AcceptGate::new(Self::default_accept_limits()),
            idle_timeout: CONNECTION_IO_TIMEOUT,
        }
    }

    /// Set bucket put callback.
    pub fn set_bucket_put(&mut self, f: BucketPutFn) {
        self.bucket_put_fn = Some(f);
    }
    /// Set bucket delete callback.
    pub fn set_bucket_delete(&mut self, f: BucketDeleteFn) {
        self.bucket_delete_fn = Some(f);
    }
    /// Set object delete callback.
    pub fn set_delete(&mut self, f: DeleteFn) {
        self.delete_fn = Some(f);
    }
    /// Set filesystem stat callback.
    pub fn set_stat(&mut self, f: StatFn) {
        self.stat_fn = Some(f);
    }
    /// Set directory listing callback.
    pub fn set_list_dir(&mut self, f: ListDirFn) {
        self.list_dir_fn = Some(f);
    }
    /// Set readlink callback.
    pub fn set_readlink(&mut self, f: ReadlinkFn) {
        self.readlink_fn = Some(f);
    }
    /// Set getxattr callback.
    pub fn set_getxattr(&mut self, f: GetXattrFn) {
        self.getxattr_fn = Some(f);
    }
    /// Set listxattr callback.
    pub fn set_listxattr(&mut self, f: ListXattrFn) {
        self.listxattr_fn = Some(f);
    }

    /// Start the RPC server on the given port (all addresses). Runs in a
    /// background thread.
    pub fn start(&self, port: u16) -> Result<(), String> {
        let listener =
            TcpListener::bind(format!("0.0.0.0:{port}")).map_err(|e| format!("RPC bind: {e}"))?;
        self.start_on(listener).map(|_| ())
    }

    /// Serve on an already-bound listener; returns its local address.
    pub fn start_on(&self, listener: TcpListener) -> Result<SocketAddr, String> {
        let local = listener
            .local_addr()
            .map_err(|e| format!("RPC local addr: {e}"))?;
        listener
            .set_nonblocking(true)
            .map_err(|e| format!("nonblocking: {e}"))?;

        let handlers = Handlers {
            persist_fn: Arc::clone(&self.persist_fn),
            fetch_fn: Arc::clone(&self.fetch_fn),
            bucket_put_fn: self.bucket_put_fn.clone(),
            bucket_delete_fn: self.bucket_delete_fn.clone(),
            delete_fn: self.delete_fn.clone(),
            cluster_cred: self.cluster_cred.clone(),
            replay: Arc::clone(&self.replay),
            admission: self.admission.clone(),
            idle_timeout: self.idle_timeout,
        };
        let handlers = Arc::new(handlers);
        let shutdown = Arc::clone(&self.shutdown);
        let gate = Arc::clone(&self.gate);

        thread::Builder::new()
            .name(format!("rpc-listen-{}", local.port()))
            .spawn(move || accept_loop(listener, local.port(), &handlers, &gate, &shutdown))
            .map_err(|e| format!("spawn RPC listener: {e}"))?;

        Ok(local)
    }

    pub fn drained(&self) -> bool {
        self.admission.drained()
    }

    pub fn shutdown(&self) {
        self.admission.close();
        self.shutdown
            .store(true, std::sync::atomic::Ordering::Relaxed);
    }
}

/// What a connection thread serves with.
struct Handlers {
    persist_fn: PersistFn,
    fetch_fn: FetchFn,
    bucket_put_fn: Option<BucketPutFn>,
    bucket_delete_fn: Option<BucketDeleteFn>,
    delete_fn: Option<DeleteFn>,
    cluster_cred: Option<crate::auth::Credentials>,
    replay: Arc<crate::auth::ReplayGuard>,
    admission: Arc<crate::admission::Admission>,
    idle_timeout: Duration,
}

fn accept_loop(
    listener: TcpListener,
    port: u16,
    handlers: &Arc<Handlers>,
    gate: &Arc<AcceptGate>,
    shutdown: &std::sync::atomic::AtomicBool,
) {
    info!(port, "cluster RPC server listening");
    loop {
        if shutdown.load(std::sync::atomic::Ordering::Relaxed) {
            break;
        }
        match listener.accept() {
            Ok((stream, addr)) => {
                // Admit before spending a thread on the connection.
                // A refused stream is dropped here, which closes it.
                let ticket = match gate.try_admit(addr.ip()) {
                    Ok(t) => t,
                    Err(why) => {
                        warn!(%addr, why, "cluster RPC connection refused");
                        continue;
                    }
                };
                let h = Arc::clone(handlers);
                let handshake_timeout = gate.limits().handshake_timeout;
                let spawned = thread::Builder::new()
                    .name("rpc-conn".into())
                    .spawn(move || {
                        if let Err(e) = handle_connection(stream, &h, ticket, handshake_timeout) {
                            debug!(%addr, error = %e, "RPC connection error");
                        }
                    });
                if let Err(e) = spawned {
                    // The stream and ticket were moved into the closure,
                    // which the failed spawn dropped: closed and released.
                    warn!(%addr, error = %e, "cluster RPC: cannot spawn a connection thread");
                }
            }
            Err(ref e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                thread::sleep(Duration::from_millis(10));
            }
            Err(e) => {
                warn!(error = %e, "RPC accept error");
                thread::sleep(Duration::from_millis(100));
            }
        }
    }
}

/// Read and verify the mandatory AUTH frame that opens every RPC
/// connection.
///
/// Frame: [OP_AUTH][len][access_key_len:u16][access_key][timestamp:u64]
///        [nonce:u64][signature:32]
///
/// The signature covers a fixed context string rather than request bytes —
/// there is no request yet. Replay is bounded the same way as the S3
/// transports: a timestamp window plus a nonce cache, so a captured AUTH
/// frame cannot open a second connection.
fn authenticate_connection(
    stream: &mut TcpStream,
    expected: &crate::auth::Credentials,
    replay: &crate::auth::ReplayGuard,
) -> Result<(), String> {
    let mut op = [0u8; 1];
    stream
        .read_exact(&mut op)
        .map_err(|e| format!("read auth op: {e}"))?;
    if op[0] != OP_AUTH {
        return Err(format!("first frame must be OP_AUTH, got {:#x}", op[0]));
    }

    let mut len_buf = [0u8; 4];
    stream
        .read_exact(&mut len_buf)
        .map_err(|e| format!("read auth len: {e}"))?;
    let len = u32::from_le_bytes(len_buf) as usize;
    // Bound before allocating — the length is attacker-controlled.
    if len > 512 {
        return Err(format!("auth frame too large: {len}"));
    }
    let mut payload = vec![0u8; len];
    stream
        .read_exact(&mut payload)
        .map_err(|e| format!("read auth payload: {e}"))?;

    let mut r = 0usize;
    let take = |r: &mut usize, n: usize| -> Result<std::ops::Range<usize>, String> {
        let start = *r;
        let end = start.checked_add(n).ok_or("auth frame overflow")?;
        if end > len {
            return Err("auth frame truncated".into());
        }
        *r = end;
        Ok(start..end)
    };

    let ak_len = {
        let rg = take(&mut r, 2)?;
        u16::from_le_bytes([payload[rg.start], payload[rg.start + 1]]) as usize
    };
    let access_key = {
        let rg = take(&mut r, ak_len)?;
        String::from_utf8(payload[rg].to_vec()).map_err(|_| "auth access_key not utf8")?
    };
    let timestamp = {
        let rg = take(&mut r, 8)?;
        let mut b = [0u8; 8];
        b.copy_from_slice(&payload[rg]);
        u64::from_le_bytes(b)
    };
    let nonce = {
        let rg = take(&mut r, 8)?;
        let mut b = [0u8; 8];
        b.copy_from_slice(&payload[rg]);
        u64::from_le_bytes(b)
    };
    let mut signature = [0u8; crate::auth::SIGNATURE_LEN];
    {
        let rg = take(&mut r, crate::auth::SIGNATURE_LEN)?;
        signature.copy_from_slice(&payload[rg]);
    }

    if access_key != expected.access_key {
        return Err("RPC auth: unknown access key".into());
    }
    // Verify the signature BEFORE consuming the nonce. Recording first
    // would let anyone who knows an access key spam (key, nonce) pairs and
    // lock the legitimate holder out of its own nonce space — an unauth'd
    // denial of service. Only a validly-signed frame gets to burn a nonce.
    if !crate::auth::verify_signature(
        &expected.secret_key,
        timestamp,
        nonce,
        RPC_AUTH_CONTEXT,
        &signature,
    ) {
        return Err("RPC auth: signature mismatch".into());
    }
    replay
        .check_and_record(&access_key, timestamp, nonce)
        .map_err(|e| format!("RPC auth: {e}"))?;

    // Acknowledge so the client knows it may proceed.
    stream
        .write_all(&[0u8])
        .map_err(|e| format!("write auth ack: {e}"))?;
    Ok(())
}

/// Build the AUTH frame a client sends immediately after connecting.
fn encode_auth_frame(creds: &crate::auth::Credentials, nonce: u64) -> Vec<u8> {
    let header = crate::auth::sign_request(creds, nonce, RPC_AUTH_CONTEXT);
    let ak = header.access_key.as_bytes();
    let mut payload = Vec::with_capacity(2 + ak.len() + 8 + 8 + crate::auth::SIGNATURE_LEN);
    payload.extend_from_slice(&(ak.len() as u16).to_le_bytes());
    payload.extend_from_slice(ak);
    payload.extend_from_slice(&header.timestamp.to_le_bytes());
    payload.extend_from_slice(&header.nonce.to_le_bytes());
    payload.extend_from_slice(&header.signature);

    let mut frame = Vec::with_capacity(5 + payload.len());
    frame.push(OP_AUTH);
    frame.extend_from_slice(&(payload.len() as u32).to_le_bytes());
    frame.extend_from_slice(&payload);
    frame
}

/// Serve one connection. `ticket` is its admission slot, released when
/// this returns, whatever the reason.
fn handle_connection(
    mut stream: TcpStream,
    h: &Handlers,
    mut ticket: AcceptTicket,
    handshake_timeout: Duration,
) -> Result<(), String> {
    stream
        .set_nonblocking(false)
        .map_err(|e| format!("blocking: {e}"))?;
    // The AUTH exchange is bounded by the handshake timeout: a connector
    // that opens TCP and sends nothing holds a handshake slot only that
    // long.
    stream
        .set_read_timeout(Some(handshake_timeout))
        .map_err(|e| format!("timeout: {e}"))?;
    stream
        .set_write_timeout(Some(handshake_timeout))
        .map_err(|e| format!("timeout: {e}"))?;

    // The connection must authenticate before it can do anything. This
    // port serves raw chunk fetch by object id, so an unauthenticated peer
    // could read any object on the node. Fail closed: with no configured
    // credential, refuse every connection rather than serving anonymously.
    let Some(expected) = h.cluster_cred.as_ref() else {
        return Err("RPC auth not configured; refusing connection".into());
    };
    authenticate_connection(&mut stream, expected, &h.replay)?;
    ticket.establish();
    stream
        .set_read_timeout(Some(h.idle_timeout))
        .map_err(|e| format!("timeout: {e}"))?;
    stream
        .set_write_timeout(Some(h.idle_timeout))
        .map_err(|e| format!("timeout: {e}"))?;
    let (persist_fn, fetch_fn, admission) = (&h.persist_fn, &h.fetch_fn, &h.admission);
    let (bucket_put_fn, bucket_delete_fn, delete_fn) =
        (&h.bucket_put_fn, &h.bucket_delete_fn, &h.delete_fn);

    // Handle multiple requests on same connection.
    loop {
        // Read op code.
        let mut op_buf = [0u8; 1];
        match stream.read_exact(&mut op_buf) {
            Ok(()) => {}
            Err(ref e) if e.kind() == std::io::ErrorKind::UnexpectedEof => return Ok(()),
            Err(e) => return Err(format!("read op: {e}")),
        }

        // Read payload length.
        let mut len_buf = [0u8; 4];
        stream
            .read_exact(&mut len_buf)
            .map_err(|e| format!("read len: {e}"))?;
        let payload_len = u32::from_le_bytes(len_buf) as usize;

        // Reject oversized payloads before allocating. The framing is
        // length-prefixed, so a rejected connection is dropped (we
        // can't skip payload_len bytes we refuse to read); that's the
        // correct response to a malformed/hostile length.
        if payload_len > MAX_RPC_PAYLOAD {
            return Err(format!(
                "RPC payload_len {payload_len} exceeds MAX_RPC_PAYLOAD {MAX_RPC_PAYLOAD}"
            ));
        }

        // Read payload.
        let mut payload = vec![0u8; payload_len];
        stream
            .read_exact(&mut payload)
            .map_err(|e| format!("read payload: {e}"))?;

        let Some(_permit) = admission.acquire() else {
            return Ok(());
        };
        match op_buf[0] {
            OP_BUCKET_PUT => {
                let result = handle_string_op(bucket_put_fn, &payload, "bucket_put");
                send_response(&mut stream, &result)?;
            }
            OP_BUCKET_DELETE => {
                let result = handle_string_op(bucket_delete_fn, &payload, "bucket_delete");
                send_response(&mut stream, &result)?;
            }
            OP_DELETE => {
                let result = handle_string_op(delete_fn, &payload, "delete");
                send_response(&mut stream, &result)?;
            }
            OP_PERSIST_INLINE => {
                let result = handle_persist_inline(persist_fn, &payload);
                send_response(&mut stream, &result)?;
            }
            OP_EXISTS => {
                let result = handle_exists(fetch_fn, &payload);
                send_response(&mut stream, &result)?;
            }
            OP_FETCH_INLINE => {
                let result = handle_fetch_inline(fetch_fn, &payload);
                send_response(&mut stream, &result)?;
            }
            _ => {
                send_response(&mut stream, &Err("unknown op".into()))?;
            }
        }
    }
}

/// FETCH_INLINE: read object data and return inline.
/// Payload: [oid_len:u16] [object_id]
/// Response: full object data bytes
fn handle_fetch_inline(fetch_fn: &FetchFn, payload: &[u8]) -> Result<Vec<u8>, String> {
    if payload.len() < 2 {
        return Err("fetch_inline payload too short".into());
    }
    let oid_len =
        u16::from_le_bytes(payload[0..2].try_into().expect("invariant: bounds checked")) as usize;
    if payload.len() < 2 + oid_len {
        return Err("fetch_inline payload truncated".into());
    }
    let object_id = std::str::from_utf8(&payload[2..2 + oid_len])
        .map_err(|e| format!("invalid object_id: {e}"))?;

    fetch_fn(object_id)
}

/// EXISTS: check if an object exists.
/// Payload: [oid_len:u16] [object_id]
/// Response: [1] if exists, [0] if not
fn handle_exists(fetch_fn: &FetchFn, payload: &[u8]) -> Result<Vec<u8>, String> {
    if payload.len() < 2 {
        return Err("exists payload too short".into());
    }
    let oid_len =
        u16::from_le_bytes(payload[0..2].try_into().expect("invariant: bounds checked")) as usize;
    if payload.len() < 2 + oid_len {
        return Err("exists payload truncated".into());
    }
    let object_id = std::str::from_utf8(&payload[2..2 + oid_len])
        .map_err(|e| format!("invalid object_id: {e}"))?;

    match fetch_fn(object_id) {
        Ok(_) => Ok(vec![1]),
        Err(_) => Ok(vec![0]),
    }
}

/// PERSIST_INLINE: data sent inline over TCP (no RDMA pool).
/// Payload: [object_id_len:u16] [object_id] [data...]
fn handle_persist_inline(persist_fn: &PersistFn, payload: &[u8]) -> Result<Vec<u8>, String> {
    if payload.len() < 2 {
        return Err("persist_inline payload too short".into());
    }
    let oid_len =
        u16::from_le_bytes(payload[0..2].try_into().expect("invariant: bounds checked")) as usize;
    if payload.len() < 2 + oid_len {
        return Err("persist_inline payload truncated".into());
    }
    let object_id = std::str::from_utf8(&payload[2..2 + oid_len])
        .map_err(|e| format!("invalid object_id: {e}"))?;
    let data = &payload[2 + oid_len..];

    debug!(object_id, len = data.len(), "RPC PERSIST_INLINE");
    persist_fn(object_id, data)?;
    Ok(Vec::new())
}

/// Handle ops that take a single string argument (bucket name, object_id).
/// Payload: [name_len:u16] [name]
fn handle_string_op(
    handler: &Option<NameFn>,
    payload: &[u8],
    op_name: &str,
) -> Result<Vec<u8>, String> {
    let handler = handler
        .as_ref()
        .ok_or_else(|| format!("{op_name}: not configured"))?;
    if payload.len() < 2 {
        return Err(format!("{op_name}: payload too short"));
    }
    let name_len =
        u16::from_le_bytes(payload[0..2].try_into().expect("invariant: bounds checked")) as usize;
    if payload.len() < 2 + name_len {
        return Err(format!("{op_name}: payload truncated"));
    }
    let name = std::str::from_utf8(&payload[2..2 + name_len])
        .map_err(|e| format!("{op_name}: invalid name: {e}"))?;
    debug!(op_name, name, "RPC handling");
    handler(name)?;
    Ok(Vec::new())
}

fn send_response(stream: &mut TcpStream, result: &Result<Vec<u8>, String>) -> Result<(), String> {
    match result {
        Ok(payload) => {
            stream
                .write_all(&[STATUS_OK])
                .map_err(|e| format!("write status: {e}"))?;
            stream
                .write_all(&(payload.len() as u32).to_le_bytes())
                .map_err(|e| format!("write len: {e}"))?;
            stream
                .write_all(payload)
                .map_err(|e| format!("write payload: {e}"))?;
        }
        Err(msg) => {
            stream
                .write_all(&[STATUS_ERR])
                .map_err(|e| format!("write status: {e}"))?;
            let msg_bytes = msg.as_bytes();
            stream
                .write_all(&(msg_bytes.len() as u32).to_le_bytes())
                .map_err(|e| format!("write len: {e}"))?;
            stream
                .write_all(msg_bytes)
                .map_err(|e| format!("write msg: {e}"))?;
        }
    }
    Ok(())
}

/// RPC client for sending persist/fetch requests.
pub struct RpcClient {
    addr: String,
    conn: RwLock<Option<TcpStream>>,
    /// Credential presented when opening a connection. `None` means
    /// the peer will refuse us — set it before use in a real cluster.
    cluster_cred: RwLock<Option<crate::auth::Credentials>>,
}

impl RpcClient {
    pub fn new(addr: &str) -> Self {
        Self {
            addr: addr.to_string(),
            conn: RwLock::new(None),
            cluster_cred: RwLock::new(None),
        }
    }

    /// Set the credential used to authenticate new connections.
    pub fn set_cluster_credential(&self, creds: crate::auth::Credentials) {
        *self.cluster_cred.write() = Some(creds);
    }

    /// A new, authenticated connection. Authenticating here means every
    /// op can assume an authenticated channel; it runs once per
    /// connection, and connections are pooled, so the cost amortizes to
    /// ~zero across the ops that follow.
    fn connect(&self) -> Result<TcpStream, String> {
        let mut stream = TcpStream::connect(&self.addr)
            .map_err(|e| format!("RPC connect to {}: {e}", self.addr))?;

        let creds = self
            .cluster_cred
            .read()
            .clone()
            .ok_or("RPC client has no cluster credential configured")?;
        // The process-wide, randomly seeded nonce source. Every node signs
        // with the same cluster credential, so per-client counters that all
        // started at 1 collided in the peer's replay guard: a second node,
        // or a restarted one, was refused for the whole skew window.
        let nonce = crate::auth::next_nonce()?;
        let frame = encode_auth_frame(&creds, nonce);
        stream
            .write_all(&frame)
            .map_err(|e| format!("write auth: {e}"))?;
        let mut ack = [0u8; 1];
        stream
            .read_exact(&mut ack)
            .map_err(|e| format!("RPC auth rejected by {}: {e}", self.addr))?;
        Ok(stream)
    }

    /// Run one request/response on the pooled connection, or on a new one
    /// when none is pooled.
    ///
    /// The server closes a connection that has been idle for its idle
    /// timeout (30 s by default), so a pooled connection may be dead
    /// when it is reused. An I/O failure on a REUSED connection is
    /// therefore retried once on a new one. Not retried: a failure on a
    /// new connection (the server is really unreachable or refusing),
    /// and an error status from the server (the op ran and failed; the
    /// reply is passed on as is).
    ///
    /// Retrying is safe because every op is idempotent: PERSIST_INLINE
    /// writes the same bytes under the same object id, FETCH_INLINE and
    /// EXISTS only read, BUCKET_PUT / BUCKET_DELETE / DELETE converge to
    /// the same state when repeated. A request that reached the server
    /// before the connection failed may therefore run twice harmlessly.
    ///
    /// The connection goes back to the pool only after a complete,
    /// successful exchange; after an error status or an I/O failure it
    /// is dropped.
    fn call(&self, op: u8, payload: &[u8]) -> Result<Vec<u8>, String> {
        let pooled = self.conn.write().take();
        let reused = pooled.is_some();
        let mut stream = match pooled {
            Some(s) => s,
            None => self.connect()?,
        };
        let outcome = match exchange(&mut stream, op, payload) {
            Err(e) if reused => {
                debug!(addr = %self.addr, op, error = %e, "pooled RPC connection failed; retrying on a new one");
                drop(stream);
                stream = self.connect()?;
                exchange(&mut stream, op, payload)
            }
            other => other,
        };
        match outcome {
            Ok(Ok(resp)) => {
                *self.conn.write() = Some(stream);
                Ok(resp)
            }
            Ok(Err(remote)) => Err(remote),
            Err(io) => Err(io),
        }
    }

    /// Persist data inline over TCP (no RDMA pool needed).
    /// Used as fallback when RDMA data path isn't available.
    pub fn persist_with_data(&self, object_id: &str, data: &[u8]) -> Result<(), String> {
        let oid = object_id.as_bytes();
        let mut payload = Vec::with_capacity(2 + oid.len() + data.len());
        payload.extend_from_slice(&(oid.len() as u16).to_le_bytes());
        payload.extend_from_slice(oid);
        payload.extend_from_slice(data);
        self.call(OP_PERSIST_INLINE, &payload).map(|_| ())
    }

    /// Create a bucket on the remote node.
    pub fn bucket_put(&self, bucket_name: &str) -> Result<(), String> {
        self.send_string_op(OP_BUCKET_PUT, bucket_name)
    }

    /// Delete a bucket on the remote node.
    pub fn bucket_delete(&self, bucket_name: &str) -> Result<(), String> {
        self.send_string_op(OP_BUCKET_DELETE, bucket_name)
    }

    /// Delete an object on the remote node.
    pub fn delete(&self, object_id: &str) -> Result<(), String> {
        self.send_string_op(OP_DELETE, object_id)
    }

    /// Fetch object data inline from the remote node.
    /// Returns the full object bytes. Used for coordinated reads
    /// where we know which node holds the data.
    pub fn fetch_inline(&self, object_id: &str) -> Result<Vec<u8>, String> {
        self.call(OP_FETCH_INLINE, &name_payload(object_id))
    }

    /// Check if an object exists on the remote node.
    pub fn exists(&self, object_id: &str) -> Result<bool, String> {
        let resp = self.call(OP_EXISTS, &name_payload(object_id))?;
        Ok(resp.first().copied() == Some(1))
    }

    fn send_string_op(&self, op: u8, name: &str) -> Result<(), String> {
        self.call(op, &name_payload(name)).map(|_| ())
    }
}

/// `[len:u16][name]`, the payload of every single-name op.
fn name_payload(name: &str) -> Vec<u8> {
    let b = name.as_bytes();
    let mut payload = Vec::with_capacity(2 + b.len());
    payload.extend_from_slice(&(b.len() as u16).to_le_bytes());
    payload.extend_from_slice(b);
    payload
}

/// One request/response on `stream`. Outer `Err`: the connection failed
/// (I/O); inner `Err`: the server answered with an error status.
fn exchange(
    stream: &mut TcpStream,
    op: u8,
    payload: &[u8],
) -> Result<Result<Vec<u8>, String>, String> {
    let len =
        u32::try_from(payload.len()).map_err(|_| "RPC payload larger than 4 GiB".to_string())?;
    let mut frame = Vec::with_capacity(5 + payload.len());
    frame.push(op);
    frame.extend_from_slice(&len.to_le_bytes());
    frame.extend_from_slice(payload);
    stream
        .write_all(&frame)
        .map_err(|e| format!("RPC write: {e}"))?;

    let mut status = [0u8; 1];
    stream
        .read_exact(&mut status)
        .map_err(|e| format!("RPC read status: {e}"))?;
    let mut len_buf = [0u8; 4];
    stream
        .read_exact(&mut len_buf)
        .map_err(|e| format!("RPC read length: {e}"))?;
    let resp_len = u32::from_le_bytes(len_buf) as usize;
    let mut resp = vec![0u8; resp_len];
    stream
        .read_exact(&mut resp)
        .map_err(|e| format!("RPC read response: {e}"))?;
    if status[0] == STATUS_OK {
        Ok(Ok(resp))
    } else if resp.is_empty() {
        Ok(Err(format!("remote op {op:#x} failed")))
    } else {
        Ok(Err(String::from_utf8_lossy(&resp).into_owned()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // The RPC port serves raw chunk fetch by object id, so an
    // unauthenticated connection must get nothing.
    #[test]
    fn rpc_rejects_client_without_credentials() {
        let store: Arc<parking_lot::RwLock<std::collections::HashMap<String, Vec<u8>>>> =
            Arc::new(parking_lot::RwLock::new(std::collections::HashMap::new()));
        store
            .write()
            .insert("secret/object".into(), b"tenant data".to_vec());

        let s = Arc::clone(&store);
        let persist_fn: PersistFn = Arc::new(move |oid: &str, data: &[u8]| {
            s.write().insert(oid.to_string(), data.to_vec());
            Ok(())
        });
        let s2 = Arc::clone(&store);
        let fetch_fn: FetchFn = Arc::new(move |oid: &str| {
            s2.read()
                .get(oid)
                .cloned()
                .ok_or_else(|| "not found".to_string())
        });

        let mut server = RpcServer::new(persist_fn, fetch_fn);
        server.set_cluster_credential(crate::auth::Credentials::new("CK", "cs"));
        let port = server.start_on(loopback_listener()).expect("start").port();

        // No credential configured → the client can't even build the AUTH
        // frame, so it must fail rather than silently connecting.
        let anon = RpcClient::new(&format!("127.0.0.1:{port}"));
        assert!(
            anon.fetch_inline("secret/object").is_err(),
            "unauthenticated client must not read an object"
        );

        // Wrong secret → server rejects the signature.
        let bad = RpcClient::new(&format!("127.0.0.1:{port}"));
        bad.set_cluster_credential(crate::auth::Credentials::new("CK", "wrong"));
        assert!(
            bad.fetch_inline("secret/object").is_err(),
            "bad signature must not read an object"
        );

        // Correct credential → works, proving the rejections above are the
        // auth check and not a broken server.
        let good = RpcClient::new(&format!("127.0.0.1:{port}"));
        good.set_cluster_credential(crate::auth::Credentials::new("CK", "cs"));
        let data = good
            .fetch_inline("secret/object")
            .expect("authenticated fetch");
        assert_eq!(data, b"tenant data");

        // The retired pool-offset ops (0x01 PERSIST, 0x02 FETCH) persisted
        // or overwrote whatever sat at a client-chosen offset of the RDMA
        // pool, including other clients' GET slots. They are refused now.
        for retired in [0x01u8, 0x02] {
            let fresh = RpcClient::new(&format!("127.0.0.1:{port}"));
            fresh.set_cluster_credential(crate::auth::Credentials::new("CK", "cs"));
            let e = fresh
                .send_string_op(retired, "secret/object")
                .expect_err("retired op");
            assert!(e.contains("unknown op"), "{e}");
        }

        server.shutdown();
    }

    fn loopback_listener() -> TcpListener {
        TcpListener::bind("127.0.0.1:0").expect("bind loopback")
    }

    /// A server whose store holds one object, "k" = "v".
    fn limited_server(limits: AcceptLimits) -> (RpcServer, SocketAddr) {
        let persist_fn: PersistFn = Arc::new(|_: &str, _: &[u8]| Ok(()));
        let fetch_fn: FetchFn = Arc::new(|oid: &str| {
            if oid == "k" {
                Ok(b"v".to_vec())
            } else {
                Err("not found".into())
            }
        });
        let mut server = RpcServer::new(persist_fn, fetch_fn);
        server.set_cluster_credential(crate::auth::Credentials::new("CK", "cs"));
        server.set_accept_limits(limits);
        let addr = server.start_on(loopback_listener()).expect("start");
        (server, addr)
    }

    fn client(addr: SocketAddr) -> RpcClient {
        let c = RpcClient::new(&addr.to_string());
        c.set_cluster_credential(crate::auth::Credentials::new("CK", "cs"));
        c
    }

    fn limits(
        handshakes: usize,
        peers: usize,
        per_source: usize,
        timeout: Duration,
    ) -> AcceptLimits {
        AcceptLimits {
            max_handshakes: handshakes,
            max_peers: peers,
            max_peers_per_source: per_source,
            handshake_timeout: timeout,
        }
    }

    /// Wait until the server's (handshaking, established) counts equal
    /// `want`; panic after 5 s.
    fn wait_counts(server: &RpcServer, want: (usize, usize)) {
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while server.connection_counts() != want {
            assert!(
                std::time::Instant::now() < deadline,
                "connection counts {:?}, want {want:?}",
                server.connection_counts()
            );
            std::thread::sleep(Duration::from_millis(5));
        }
    }

    /// Whether the server closed `s` within `within`: a read sees EOF or
    /// a reset rather than timing out.
    fn closed_by_server(s: &mut TcpStream, within: Duration) -> bool {
        s.set_read_timeout(Some(within)).expect("timeout");
        let mut b = [0u8; 1];
        match s.read(&mut b) {
            Ok(0) => true,
            Ok(_) => false,
            Err(e) => !matches!(
                e.kind(),
                std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut
            ),
        }
    }

    // Connections over the handshake cap are closed at accept,
    // while the admitted ones stay open.
    #[test]
    fn connections_over_the_cap_are_refused() {
        let (server, addr) = limited_server(limits(2, 16, 16, Duration::from_secs(30)));
        let mut silent: Vec<TcpStream> = (0..2)
            .map(|_| TcpStream::connect(addr).expect("connect"))
            .collect();
        wait_counts(&server, (2, 0));
        let mut third = TcpStream::connect(addr).expect("connect");
        assert!(
            closed_by_server(&mut third, Duration::from_secs(2)),
            "third connection must be refused"
        );
        // An authenticated client is refused too while the cap is held.
        assert!(client(addr).fetch_inline("k").is_err());
        for s in &mut silent {
            assert!(
                !closed_by_server(s, Duration::from_millis(50)),
                "admitted connections stay open"
            );
        }
        assert_eq!(server.connection_counts(), (2, 0));
        drop(silent);
        wait_counts(&server, (0, 0));
        assert_eq!(client(addr).fetch_inline("k").expect("room again"), b"v");
        server.shutdown();
    }

    // An established connection holds its slot until the peer goes
    // away, then the slot is free for the next one.
    #[test]
    fn slot_is_released_when_the_peer_disconnects() {
        let (server, addr) = limited_server(limits(4, 1, 4, Duration::from_secs(5)));
        let first = client(addr);
        assert_eq!(first.fetch_inline("k").expect("first"), b"v");
        wait_counts(&server, (0, 1));
        let second = client(addr);
        assert!(second.fetch_inline("k").is_err(), "peer cap is 1");
        wait_counts(&server, (0, 1));
        drop(first); // closes its pooled connection
        wait_counts(&server, (0, 0));
        assert_eq!(
            second.fetch_inline("k").expect("second after release"),
            b"v"
        );
        wait_counts(&server, (0, 1));
        server.shutdown();
    }

    // One source address cannot take more than its share, and the
    // refusal is the per-source rule, not the total.
    #[test]
    fn per_source_cap_applies() {
        let (server, addr) = limited_server(limits(8, 100, 2, Duration::from_secs(5)));
        let a = client(addr);
        let b = client(addr);
        // Each client keeps its connection pooled after the op.
        assert!(a.fetch_inline("k").is_ok() && b.fetch_inline("k").is_ok());
        wait_counts(&server, (0, 2));
        let c = client(addr);
        assert!(
            c.fetch_inline("k").is_err(),
            "third connection from one source"
        );
        assert_eq!(
            server.connection_counts(),
            (0, 2),
            "the total limit was not the reason"
        );
        drop(a);
        wait_counts(&server, (0, 1));
        assert!(c.fetch_inline("k").is_ok());
        server.shutdown();
    }

    // The server closes a pooled connection after its idle timeout.
    // Every client op must survive that on its first call: an I/O error
    // on the reused connection is retried once on a new one.
    #[test]
    fn every_op_survives_an_idle_closed_pooled_connection() {
        let store: Arc<parking_lot::RwLock<std::collections::HashMap<String, Vec<u8>>>> =
            Arc::default();
        let fetches = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let s = Arc::clone(&store);
        let persist_fn: PersistFn = Arc::new(move |oid: &str, data: &[u8]| {
            s.write().insert(oid.to_string(), data.to_vec());
            Ok(())
        });
        let (s, n) = (Arc::clone(&store), Arc::clone(&fetches));
        let fetch_fn: FetchFn = Arc::new(move |oid: &str| {
            n.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            s.read()
                .get(oid)
                .cloned()
                .ok_or_else(|| "not found".to_string())
        });
        let mut server = RpcServer::new(persist_fn, fetch_fn);
        server.set_cluster_credential(crate::auth::Credentials::new("CK", "cs"));
        server.set_idle_timeout(Duration::from_millis(150));
        let s = Arc::clone(&store);
        server.set_delete(Arc::new(move |oid: &str| {
            s.write().remove(oid);
            Ok(())
        }));
        let buckets: Arc<parking_lot::Mutex<Vec<String>>> = Arc::default();
        let b = Arc::clone(&buckets);
        server.set_bucket_put(Arc::new(move |name: &str| {
            b.lock().push(name.to_string());
            Ok(())
        }));
        let addr = server.start_on(loopback_listener()).expect("start");
        let c = client(addr);

        // Let the pooled connection go idle until the server has closed it.
        let idle_out = |server: &RpcServer| {
            wait_counts(server, (0, 0));
            std::thread::sleep(Duration::from_millis(20));
        };
        c.persist_with_data("a", b"1").expect("first persist");
        idle_out(&server);
        c.persist_with_data("b", b"2").expect("persist after idle");
        idle_out(&server);
        assert_eq!(c.fetch_inline("a").expect("fetch after idle"), b"1");
        idle_out(&server);
        assert!(c.exists("b").expect("exists after idle"));
        idle_out(&server);
        c.delete("a").expect("delete after idle");
        idle_out(&server);
        c.bucket_put("bkt").expect("bucket_put after idle");
        assert!(!store.read().contains_key("a"));
        assert_eq!(*buckets.lock(), ["bkt"]);

        // An error status is the op's answer: passed on, not retried.
        let before = fetches.load(std::sync::atomic::Ordering::SeqCst);
        let e = c.fetch_inline("missing").expect_err("missing object");
        assert!(e.contains("not found"), "{e}");
        assert_eq!(
            fetches.load(std::sync::atomic::Ordering::SeqCst),
            before + 1,
            "not retried"
        );
        // A server that is really gone fails on the new connection too.
        assert!(c.exists("b").expect("exists"));
        server.shutdown();
        std::thread::sleep(Duration::from_millis(200));
        assert!(
            c.fetch_inline("b").is_err(),
            "no listener: the retry fails too"
        );
    }

    // A connector that never completes AUTH is dropped after the
    // handshake timeout and its slot released.
    #[test]
    fn slow_handshake_times_out() {
        let (server, addr) = limited_server(limits(1, 4, 4, Duration::from_millis(200)));
        let started = std::time::Instant::now();
        let mut silent = TcpStream::connect(addr).expect("connect");
        wait_counts(&server, (1, 0));
        assert!(
            closed_by_server(&mut silent, Duration::from_secs(3)),
            "silent connector must be dropped"
        );
        assert!(started.elapsed() < Duration::from_secs(3));
        wait_counts(&server, (0, 0));
        // A partial AUTH (op byte only) times out the same way.
        let mut partial = TcpStream::connect(addr).expect("connect");
        partial.write_all(&[OP_AUTH]).expect("write");
        assert!(closed_by_server(&mut partial, Duration::from_secs(3)));
        wait_counts(&server, (0, 0));
        assert!(
            client(addr).fetch_inline("k").is_ok(),
            "the handshake slot is free again"
        );
        server.shutdown();
    }
}

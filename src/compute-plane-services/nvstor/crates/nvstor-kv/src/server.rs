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

//! The KV block server.
//!
//! One thread per connection reads signed `kv_proto` requests on lane 0
//! and answers each before reading the next. A GET RDMA-WRITEs each hit
//! from its DRAM slot straight to the client's landing pages (one chain
//! per block) and replies only after every WRITE has completed; a PUT
//! RDMA-READs the client's pages into a reserved slot, verifies the
//! writer's block check and commits. Throughput comes from many
//! connections (the client opens several per rail), not from overlapping
//! requests on one.
//!
//! Every request is checked, in this order, before any data moves:
//! 1. the envelope's access key is the key the connection authenticated
//!    with in the bootstrap (a signed request is not accepted from another
//!    connection);
//! 2. the [`KvAuthFn`] (signature, then policy) allows the [`KvAction`] on
//!    the namespace. It is its own type, not S3-over-RDMA's `AuthFn`: an
//!    S3 policy evaluator would read a namespace as a bucket and object,
//!    and grant KV access by S3 rules;
//! 3. the nonce is fresh (the process-wide replay guard);
//! 4. `seq` is higher than the connection's last.
//!
//! A refused request gets a whole-request error and moves nothing. The
//! rules a request carries (layout registered once per id, at most
//! `max_request_bytes` moved, a budget measured from receipt) are checked
//! next; a block whose budget ran out before its data moved answers
//! `STATUS_EXPIRED`.
//!
//! Connections are limited per identity ([`ConnectionCap`], at the
//! bootstrap, before any QP exists), and every connection needs a
//! handshake verifier ([`kv_transport`] takes one). Not limited yet: DRAM
//! per tenant, so one tenant's PUTs can evict another's blocks (an
//! availability matter, not a correctness one; there are no per-tenant
//! quotas yet).

use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::{Duration, Instant};

use nvstor_core::xxh3::Xxh3;
use std::sync::Weak;

use nvstor_rdma::auth::{AuthError, AuthHeader, ReplayGuard};
use nvstor_rdma::bootstrap::{HandshakeVerifier, SessionAdmitter, SessionClaim};
use nvstor_rdma::connection::Piece;
use nvstor_rdma::device::{Access, MemoryRegion, RdmaDevice};
use nvstor_rdma::kv_proto::{
    self, GetResult, Layout, Request, Response, KV_MSG_SIZE, MAX_REQUEST_BYTES, NAMESPACE_LEN,
    STATUS_BAD_LAYOUT, STATUS_CHECK_MISMATCH, STATUS_ERROR, STATUS_EXISTS, STATUS_EXPIRED,
    STATUS_MISS, STATUS_NOT_STORED, STATUS_NO_SPACE, STATUS_OK, STATUS_PENDING,
    STATUS_UNAUTHORIZED,
};
use nvstor_rdma::pool::MemoryPool;
use nvstor_rdma::transport::{RdmaTransport, TransferError};
use nvstor_storage::kv_cache::{BlockId, KvCache, ReserveError};
use parking_lot::Mutex;
use std::sync::atomic::{AtomicUsize, Ordering};
use tracing::{debug, info, warn};

/// Most layouts one connection may register.
pub const MAX_LAYOUTS_PER_CONNECTION: usize = 64;

#[derive(Clone, Copy, Debug)]
pub struct KvServerConfig {
    /// Most bytes one GET or PUT may move (at most `kv_proto::MAX_REQUEST_BYTES`).
    pub max_request_bytes: u64,
    /// Verify each PUT's block check over the bytes read before storing
    /// it. One XXH3 pass over every stored byte.
    pub verify_puts: bool,
}

impl Default for KvServerConfig {
    fn default() -> Self {
        KvServerConfig {
            max_request_bytes: MAX_REQUEST_BYTES,
            verify_puts: true,
        }
    }
}

/// What a KV request asks to do, for policy.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum KvAction {
    Layout,
    HeadBlock,
    GetBlock,
    PutBlock,
}

/// What the server's [`KvAuthFn`] decides on.
pub struct KvAuthContext<'a> {
    pub header: &'a AuthHeader,
    /// The encoded request body the signature must cover.
    pub body: &'a [u8],
    pub action: KvAction,
    /// The namespace acted on; `None` for LAYOUT.
    pub namespace: Option<&'a [u8; NAMESPACE_LEN]>,
}

/// Verify a KV request's signature, then decide whether its access key may
/// take its action on its namespace. Returns the principal.
pub type KvAuthFn = Arc<dyn Fn(&KvAuthContext) -> Result<String, AuthError> + Send + Sync>;

/// At most `max_per_identity` connections per access key, across every
/// rail that shares it (the bootstrap's session admitter: a connector over
/// the limit is told BUSY before any QP exists).
pub struct ConnectionCap {
    max_per_identity: usize,
    held: Mutex<HashMap<String, usize>>,
}

struct CapClaim {
    cap: Arc<ConnectionCap>,
    identity: String,
}

impl Drop for CapClaim {
    fn drop(&mut self) {
        let mut held = self.cap.held.lock();
        if let Some(n) = held.get_mut(&self.identity) {
            *n -= 1;
            if *n == 0 {
                held.remove(&self.identity);
            }
        }
    }
}

impl ConnectionCap {
    pub fn new(max_per_identity: usize) -> Arc<ConnectionCap> {
        Arc::new(ConnectionCap {
            max_per_identity,
            held: Mutex::new(HashMap::new()),
        })
    }

    fn admit(self: &Arc<Self>, identity: &str) -> Option<SessionClaim> {
        let mut held = self.held.lock();
        let n = held.entry(identity.to_string()).or_insert(0);
        if *n >= self.max_per_identity {
            if *n == 0 {
                held.remove(identity);
            }
            return None;
        }
        *n += 1;
        Some(Box::new(CapClaim {
            cap: Arc::clone(self),
            identity: identity.to_string(),
        }))
    }

    pub fn admitter(self: &Arc<Self>) -> SessionAdmitter {
        let cap = Arc::clone(self);
        Arc::new(move |identity: &str, _role| cap.admit(identity))
    }

    /// Connections held by `identity`.
    pub fn held(&self, identity: &str) -> usize {
        self.held.lock().get(identity).copied().unwrap_or(0)
    }
}

/// One connection's one-sided operations, against the server's arena.
/// The real one is RDMA on the connection's transport; tests use memory.
pub trait BlockLink {
    /// Write pieces of `data` to the client (a GET).
    fn write_scatter(&self, data: &[u8], pieces: &[Piece]) -> Result<(), TransferError>;
    /// Read the client's pieces into `buf` (a PUT).
    fn read_gather(&self, buf: &mut [u8], pieces: &[Piece]) -> Result<(), TransferError>;
}

/// What the server shares across connections.
pub struct Shared {
    cache: Arc<KvCache>,
    auth: KvAuthFn,
    replay: Arc<ReplayGuard>,
    config: KvServerConfig,
}

impl Shared {
    pub fn new(
        cache: Arc<KvCache>,
        auth: KvAuthFn,
        config: KvServerConfig,
    ) -> std::io::Result<Shared> {
        if config.max_request_bytes == 0 || config.max_request_bytes > MAX_REQUEST_BYTES {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidInput,
                format!("max_request_bytes must be 1..={MAX_REQUEST_BYTES}"),
            ));
        }
        Ok(Shared {
            cache,
            auth,
            replay: nvstor_rdma::auth::process_replay_guard(),
            config,
        })
    }

    /// The arena's bytes for a slot: (offset, length) from a hit or a
    /// reservation, which keeps the slot from being reused.
    fn slot(&self, offset: usize, len: usize) -> *mut u8 {
        let (base, arena_len) = self.cache.arena();
        assert!(
            offset + len <= arena_len,
            "invariant: slots lie inside the arena"
        );
        // SAFETY: within the arena (checked above).
        unsafe { base.add(offset) }
    }
}

/// Why a connection stops being served.
#[derive(Debug)]
pub enum Fatal {
    /// A transfer failed: the connection's lanes are unusable.
    Transfer(String),
}

/// One connection's state.
pub struct Session {
    /// The access key the connection authenticated with.
    identity: String,
    layouts: HashMap<u32, Layout>,
    last_seq: Option<u64>,
}

/// The 64-bit block check: XXH3-64 over
/// (namespace ‖ key ‖ payload).
pub fn block_check(namespace: &[u8; NAMESPACE_LEN], key: &[u8], payload: &[u8]) -> u64 {
    let mut h = Xxh3::new();
    h.update(namespace);
    h.update(key);
    h.update(payload);
    h.finish()
}

impl Session {
    pub fn new(identity: String) -> Session {
        Session {
            identity,
            layouts: HashMap::new(),
            last_seq: None,
        }
    }

    /// Answer one request. `Err` only when the connection can no longer
    /// be used (a transfer failed); every refusal is an `Ok` response.
    pub fn handle(
        &mut self,
        shared: &Shared,
        link: &dyn BlockLink,
        header: &AuthHeader,
        req: Request,
        body: &[u8],
        received: Instant,
    ) -> Result<Response, Fatal> {
        let req_id = req.req_id();
        let refuse = |status| Ok(Response::Err { req_id, status });
        let (action, namespace) = match &req {
            Request::Layout { .. } => (KvAction::Layout, None),
            Request::Head { namespace, .. } => (KvAction::HeadBlock, Some(*namespace)),
            Request::Get { namespace, .. } => (KvAction::GetBlock, Some(*namespace)),
            Request::Put { namespace, .. } => (KvAction::PutBlock, Some(*namespace)),
        };
        if let Err(e) = self.authenticate(shared, header, body, action, namespace.as_ref()) {
            debug!(identity = %self.identity, error = %e, ?action, "KV request refused");
            return refuse(STATUS_UNAUTHORIZED);
        }
        if self.last_seq.is_some_and(|last| req.seq() <= last) {
            debug!(identity = %self.identity, seq = req.seq(), "KV request out of sequence");
            return refuse(STATUS_UNAUTHORIZED);
        }
        self.last_seq = Some(req.seq());
        match req {
            Request::Layout { req_id, layout, .. } => {
                // A block larger than a slot could never be stored.
                let status = if self.layouts.contains_key(&layout.layout_id)
                    || self.layouts.len() >= MAX_LAYOUTS_PER_CONNECTION
                    || layout.block_bytes() > shared.cache.slot_bytes() as u64
                {
                    STATUS_BAD_LAYOUT
                } else {
                    self.layouts.insert(layout.layout_id, layout);
                    STATUS_OK
                };
                Ok(Response::LayoutAck { req_id, status })
            }
            Request::Head {
                req_id,
                namespace,
                keys,
                ..
            } => {
                let statuses = keys
                    .iter()
                    .map(|k| match BlockId::new(namespace, k.as_bytes()) {
                        Some(id) if shared.cache.contains(&id) => STATUS_OK,
                        _ => STATUS_MISS,
                    })
                    .collect();
                Ok(Response::Head { req_id, statuses })
            }
            Request::Get {
                req_id,
                namespace,
                layout_id,
                budget_ms,
                entries,
                ..
            } => {
                let Some(layout) = self.layouts.get(&layout_id) else {
                    return refuse(STATUS_BAD_LAYOUT);
                };
                if !within_cap(shared, layout, entries.len()) {
                    return refuse(STATUS_ERROR);
                }
                let deadline = received + Duration::from_millis(u64::from(budget_ms));
                let mut results = Vec::with_capacity(entries.len());
                for e in &entries {
                    results.push(get_one(
                        shared,
                        link,
                        layout,
                        &namespace,
                        e.key.as_bytes(),
                        e.block,
                        deadline,
                    )?);
                }
                Ok(Response::Get { req_id, results })
            }
            Request::Put {
                req_id,
                namespace,
                layout_id,
                budget_ms,
                entries,
                ..
            } => {
                let Some(layout) = self.layouts.get(&layout_id) else {
                    return refuse(STATUS_BAD_LAYOUT);
                };
                if !within_cap(shared, layout, entries.len()) {
                    return refuse(STATUS_ERROR);
                }
                // A PUT holds its keys at most the store's cap, measured
                // from receipt like the budget.
                let deadline = received
                    + Duration::from_millis(u64::from(budget_ms)).min(shared.cache.max_pending());
                let mut statuses = Vec::with_capacity(entries.len());
                for e in &entries {
                    statuses.push(put_one(
                        shared,
                        link,
                        layout,
                        &namespace,
                        e.key.as_bytes(),
                        e.block,
                        e.check,
                        deadline,
                    )?);
                }
                Ok(Response::Put { req_id, statuses })
            }
        }
    }

    fn authenticate(
        &self,
        shared: &Shared,
        header: &AuthHeader,
        body: &[u8],
        action: KvAction,
        namespace: Option<&[u8; NAMESPACE_LEN]>,
    ) -> Result<(), AuthError> {
        // Fail closed on a missing key, as S3-over-RDMA does.
        if header.access_key.is_empty() || header.access_key != self.identity {
            return Err(AuthError::Missing);
        }
        (shared.auth)(&KvAuthContext {
            header,
            body,
            action,
            namespace,
        })?;
        // Only a validly signed request burns a nonce.
        shared
            .replay
            .check_and_record(&header.access_key, header.timestamp, header.nonce)
    }
}

fn within_cap(shared: &Shared, layout: &Layout, entries: usize) -> bool {
    layout
        .block_bytes()
        .checked_mul(entries as u64)
        .is_some_and(|b| b <= shared.config.max_request_bytes)
}

/// The block's pieces in the client's layout, as offsets into its payload.
fn pieces(layout: &Layout, block: u32) -> Option<Vec<Piece>> {
    Some(
        layout
            .block_pieces(block)?
            .map(|(remote_addr, len, offset)| Piece {
                local_offset: offset,
                remote_addr,
                rkey: layout.rkey,
                len: u64::from(len),
            })
            .collect(),
    )
}

fn get_one(
    shared: &Shared,
    link: &dyn BlockLink,
    layout: &Layout,
    ns: &[u8; NAMESPACE_LEN],
    key: &[u8],
    block: u32,
    deadline: Instant,
) -> Result<GetResult, Fatal> {
    let none = |status| {
        Ok(GetResult {
            status,
            length: 0,
            check: 0,
        })
    };
    let Some(id) = BlockId::new(*ns, key) else {
        return none(STATUS_MISS);
    };
    let Some(hit) = shared.cache.get(&id) else {
        return none(STATUS_MISS);
    };
    if u64::from(hit.len()) != layout.block_bytes() {
        return none(STATUS_BAD_LAYOUT);
    }
    let Some(pieces) = pieces(layout, block) else {
        return none(STATUS_BAD_LAYOUT);
    };
    // The budget bounds when a WRITE is posted, not when it lands.
    if Instant::now() >= deadline {
        return none(STATUS_EXPIRED);
    }
    // SAFETY: the hit pins its committed slot: nothing writes it, and it
    // is not reused until the hit is dropped (after the WRITE, or by the
    // lane if the WRITE's completion was never seen).
    let data = unsafe {
        std::slice::from_raw_parts(
            shared.slot(hit.offset(), hit.len() as usize),
            hit.len() as usize,
        )
    };
    let (length, check) = (hit.len(), hit.check());
    match link.write_scatter(data, &pieces) {
        Ok(()) => Ok(GetResult {
            status: STATUS_OK,
            length,
            check,
        }),
        Err(e) => {
            let msg = e.to_string();
            e.hold(hit);
            Err(Fatal::Transfer(msg))
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn put_one(
    shared: &Shared,
    link: &dyn BlockLink,
    layout: &Layout,
    ns: &[u8; NAMESPACE_LEN],
    key: &[u8],
    block: u32,
    check: u64,
    deadline: Instant,
) -> Result<u8, Fatal> {
    let Some(id) = BlockId::new(*ns, key) else {
        return Ok(STATUS_ERROR);
    };
    let Some(pieces) = pieces(layout, block) else {
        return Ok(STATUS_BAD_LAYOUT);
    };
    let len = layout.block_bytes() as u32;
    if Instant::now() >= deadline {
        return Ok(STATUS_EXPIRED);
    }
    let reservation = match shared.cache.reserve(id, len, check, deadline) {
        Ok(r) => r,
        Err(ReserveError::Exists) => return Ok(STATUS_EXISTS),
        Err(ReserveError::Pending) => return Ok(STATUS_PENDING),
        Err(ReserveError::NoSpace) => return Ok(STATUS_NO_SPACE),
        Err(ReserveError::TooLarge) => return Ok(STATUS_BAD_LAYOUT),
        Err(ReserveError::Expired | ReserveError::LengthMismatch) => return Ok(STATUS_ERROR),
    };
    // SAFETY: the reservation holds the slot for this PUT alone: no reader
    // sees it and it is not reused until the reservation is dropped (after
    // the READ, or by the lane if the READ's completion was never seen).
    let buf = unsafe {
        std::slice::from_raw_parts_mut(
            shared.slot(reservation.offset(), len as usize),
            len as usize,
        )
    };
    if let Err(e) = link.read_gather(buf, &pieces) {
        let msg = e.to_string();
        e.hold(reservation);
        return Err(Fatal::Transfer(msg));
    }
    if shared.config.verify_puts && block_check(ns, key, buf) != check {
        // Dropping the reservation frees the key and the slot.
        return Ok(STATUS_CHECK_MISMATCH);
    }
    Ok(match reservation.commit() {
        Ok(()) => STATUS_OK,
        // The bytes moved but the deadline passed before the commit.
        Err(_) => STATUS_NOT_STORED,
    })
}

/// A transport set up for the KV service on one NIC: KV-sized control
/// messages, two lanes per connection (control and one data lane: a
/// session serves one request at a time), a token pool (data moves from
/// the cache arena), the handshake `verifier` every connector must pass,
/// and `cap` (shared by every rail) limiting connections per identity.
/// Hand it to [`KvServer::start`], which listens on it.
pub fn kv_transport(
    device: Arc<RdmaDevice>,
    verifier: HandshakeVerifier,
    cap: &Arc<ConnectionCap>,
) -> Result<RdmaTransport, String> {
    let pool = Arc::new(MemoryPool::with_access(&device, 4096, 1, Access::LOCAL)?);
    let mut t = RdmaTransport::new(device, pool);
    t.set_control_msg_size(KV_MSG_SIZE)?;
    t.set_lanes_per_peer(2);
    t.set_verifier(verifier);
    t.set_session_admitter(cap.admitter());
    Ok(t)
}

/// The real [`BlockLink`]: RDMA on one connection, with the arena's lkey
/// on that connection's NIC.
struct RailLink {
    transport: Arc<RdmaTransport>,
    peer: u64,
    lkey: u32,
}

impl BlockLink for RailLink {
    fn write_scatter(&self, data: &[u8], pieces: &[Piece]) -> Result<(), TransferError> {
        self.transport
            .write_scatter(self.peer, data, self.lkey, pieces)
    }
    fn read_gather(&self, buf: &mut [u8], pieces: &[Piece]) -> Result<(), TransferError> {
        self.transport
            .read_gather(self.peer, buf, self.lkey, pieces)
    }
}

struct Rail {
    /// The arena registered on this rail's NIC: dropped (deregistered)
    /// before `cache` lets the arena go.
    arena_mr: MemoryRegion,
    _cache: Arc<KvCache>,
    transport: Arc<RdmaTransport>,
}

/// The KV server over a set of rails (one transport per NIC).
pub struct KvServer {
    // Rails before `shared`: an arena registration is dropped before the
    // last reference to the arena can be.
    rails: Vec<Arc<Rail>>,
    shared: Arc<Shared>,
    sessions: Arc<AtomicUsize>,
}

impl KvServer {
    /// Register the cache arena on every rail's NIC, start serving every
    /// peer each rail accepts, then listen on each `(transport, port)`
    /// (port 0: any). The transports come from [`kv_transport`] and must
    /// not be listening yet, so no peer is accepted before it can be
    /// served. Returns the server and the addresses it listens on.
    pub fn start(
        shared: Shared,
        transports: Vec<(Arc<RdmaTransport>, u16)>,
    ) -> Result<(KvServer, Vec<SocketAddr>), String> {
        let shared = Arc::new(shared);
        let (base, len) = shared.cache.arena();
        let mut rails = Vec::with_capacity(transports.len());
        for (t, _) in &transports {
            // Local access only: peers never touch server memory.
            let arena_mr = t.device().register_memory(base, len, Access::LOCAL)?;
            rails.push(Arc::new(Rail {
                arena_mr,
                _cache: Arc::clone(&shared.cache),
                transport: Arc::clone(t),
            }));
        }
        let sessions = Arc::new(AtomicUsize::new(0));
        for rail in &rails {
            // Weak: the transport keeps this callback, and the rail keeps
            // the transport.
            let (weak_shared, weak_rail) = (Arc::downgrade(&shared), Arc::downgrade(rail));
            let sessions = Arc::clone(&sessions);
            rail.transport.set_on_peer_accepted(Arc::new(move |peer| {
                accept(&weak_shared, &weak_rail, &sessions, peer);
            }));
        }
        let mut addrs = Vec::with_capacity(transports.len());
        for (t, port) in &transports {
            addrs.push(t.listen(*port)?);
        }
        info!(rails = rails.len(), arena_bytes = len, "KV server started");
        Ok((
            KvServer {
                rails,
                shared,
                sessions,
            },
            addrs,
        ))
    }

    pub fn cache(&self) -> &Arc<KvCache> {
        &self.shared.cache
    }

    /// Connections being served.
    pub fn sessions(&self) -> usize {
        self.sessions.load(Ordering::Relaxed)
    }

    /// Stop listening and disconnect every peer; each session thread ends
    /// as its peer goes.
    pub fn shutdown(&self) {
        for rail in &self.rails {
            rail.transport.shutdown();
        }
    }
}

/// Counted while a session thread runs; on exit, by return or by panic,
/// it uncounts the session and disconnects the peer.
struct SessionGuard<'a> {
    sessions: &'a AtomicUsize,
    transport: &'a RdmaTransport,
    peer: u64,
}

impl Drop for SessionGuard<'_> {
    fn drop(&mut self) {
        self.transport.disconnect(self.peer);
        self.sessions.fetch_sub(1, Ordering::Relaxed);
    }
}

fn accept(shared: &Weak<Shared>, rail: &Weak<Rail>, sessions: &Arc<AtomicUsize>, peer: u64) {
    let (Some(shared), Some(rail)) = (shared.upgrade(), rail.upgrade()) else {
        return;
    };
    sessions.fetch_add(1, Ordering::Relaxed);
    let thread_sessions = Arc::clone(sessions);
    let thread_rail = Arc::clone(&rail);
    let spawned = std::thread::Builder::new()
        .name(format!("kv-peer-{peer}"))
        .spawn(move || {
            let _guard = SessionGuard {
                sessions: &thread_sessions,
                transport: &thread_rail.transport,
                peer,
            };
            serve_peer(&shared, &thread_rail, peer);
        });
    if let Err(e) = spawned {
        warn!(peer, error = %e, "cannot start a KV session thread; disconnecting");
        rail.transport.disconnect(peer);
        sessions.fetch_sub(1, Ordering::Relaxed);
    }
}

fn serve_peer(shared: &Shared, rail: &Rail, peer: u64) {
    let transport = &rail.transport;
    let Some(identity) = transport.peer_access_key(peer) else {
        warn!(peer, "KV peer has no authenticated identity");
        return;
    };
    let link = RailLink {
        transport: Arc::clone(transport),
        peer,
        lkey: rail.arena_mr.lkey,
    };
    let mut session = Session::new(identity);
    loop {
        let msg = transport.recv_message(peer, None, false, kv_proto::decode_signed);
        let received = Instant::now();
        let (header, req, body) = match msg {
            Ok(m) => m,
            Err(e) => {
                if transport.peer_role(peer).is_some() {
                    // A message that does not decode has no req_id to answer.
                    warn!(peer, error = %e, "undecodable KV request; disconnecting");
                } else {
                    debug!(peer, "KV peer gone");
                }
                return;
            }
        };
        let resp = match session.handle(shared, &link, &header, req, &body, received) {
            Ok(r) => r,
            Err(Fatal::Transfer(e)) => {
                warn!(peer, error = %e, "KV transfer failed; connection closed");
                return;
            }
        };
        if let Err(e) = transport.send_message(peer, |buf| resp.encode_into(buf)) {
            debug!(peer, error = %e, "KV reply not sent; connection closed");
            return;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use nvstor_rdma::auth::{sign_request, verify_signature, Credentials};
    use nvstor_rdma::kv_proto::{GetEntry, Key, PutEntry, TensorRun};
    use nvstor_storage::kv_cache::KvCacheConfig;
    use std::cell::{Cell, RefCell};

    const NS: [u8; NAMESPACE_LEN] = [5; NAMESPACE_LEN];
    const SECRET: &str = "kv-secret";

    /// Client memory: a flat byte vector; remote addresses are offsets.
    struct MemLink {
        client: RefCell<Vec<u8>>,
        fail: bool,
        /// Transfers attempted.
        calls: Cell<usize>,
    }

    impl MemLink {
        fn new(fail: bool) -> MemLink {
            MemLink {
                client: RefCell::new(vec![0; 4096]),
                fail,
                calls: Cell::new(0),
            }
        }
    }

    impl BlockLink for MemLink {
        fn write_scatter(&self, data: &[u8], pieces: &[Piece]) -> Result<(), TransferError> {
            self.calls.set(self.calls.get() + 1);
            if self.fail {
                return Err("lane failed".into());
            }
            let mut c = self.client.borrow_mut();
            for p in pieces {
                let (r, l) = (p.remote_addr as usize, p.local_offset as usize);
                c[r..r + p.len as usize].copy_from_slice(&data[l..l + p.len as usize]);
            }
            Ok(())
        }
        fn read_gather(&self, buf: &mut [u8], pieces: &[Piece]) -> Result<(), TransferError> {
            self.calls.set(self.calls.get() + 1);
            if self.fail {
                return Err("lane failed".into());
            }
            let c = self.client.borrow();
            for p in pieces {
                let (r, l) = (p.remote_addr as usize, p.local_offset as usize);
                buf[l..l + p.len as usize].copy_from_slice(&c[r..r + p.len as usize]);
            }
            Ok(())
        }
    }

    /// Three tensors, pages of 100, 50 and 25 bytes, 8 blocks each,
    /// tensor t at offset t * 1000 in client memory.
    fn layout() -> Layout {
        Layout {
            layout_id: 1,
            rkey: 9,
            num_blocks: 8,
            tensors: vec![
                TensorRun {
                    base: 0,
                    stride: 100,
                    page: 100,
                },
                TensorRun {
                    base: 1000,
                    stride: 50,
                    page: 50,
                },
                TensorRun {
                    base: 2000,
                    stride: 25,
                    page: 25,
                },
            ],
        }
    }

    /// Signature, then policy: "tenant" may use NS only, and may not
    /// register layouts when `deny_layout`.
    fn auth(deny_layout: bool) -> KvAuthFn {
        Arc::new(move |c: &KvAuthContext| {
            if !verify_signature(
                SECRET,
                c.header.timestamp,
                c.header.nonce,
                c.body,
                &c.header.signature,
            ) {
                return Err(AuthError::BadSignature);
            }
            if c.namespace.is_some_and(|n| *n != NS)
                || (deny_layout && c.action == KvAction::Layout)
            {
                return Err(AuthError::Denied);
            }
            Ok(c.header.access_key.clone())
        })
    }

    fn cache(slots: usize, max_pending: Duration) -> Arc<KvCache> {
        KvCache::new(KvCacheConfig {
            slot_bytes: 4096,
            slots,
            shards: 2,
            max_pending,
        })
        .expect("cache")
    }

    fn shared(verify: bool) -> Shared {
        Shared::new(
            cache(4, Duration::from_secs(10)),
            auth(false),
            KvServerConfig {
                verify_puts: verify,
                ..KvServerConfig::default()
            },
        )
        .expect("shared")
    }

    struct Client {
        creds: Credentials,
        seq: u64,
        nonce: u64,
    }

    impl Client {
        fn new(key: &str, secret: &str) -> Client {
            Client {
                creds: Credentials::new(key, secret),
                seq: 0,
                nonce: rand_nonce(),
            }
        }
        /// Sign `req` (built with the next seq) and hand it to the session.
        fn call(
            &mut self,
            s: &mut Session,
            sh: &Shared,
            link: &MemLink,
            mk: impl FnOnce(u64) -> Request,
        ) -> Result<Response, Fatal> {
            self.seq += 1;
            self.nonce += 1;
            let req = mk(self.seq);
            let mut body = vec![0u8; KV_MSG_SIZE];
            let n = req.encode_into(&mut body).expect("encode");
            body.truncate(n);
            let header = sign_request(&self.creds, self.nonce, &body);
            s.handle(sh, link, &header, req, &body, Instant::now())
        }
    }

    fn rand_nonce() -> u64 {
        // Distinct per test client: the replay guard is process-wide.
        use std::sync::atomic::{AtomicU64, Ordering};
        static NEXT: AtomicU64 = AtomicU64::new(1 << 40);
        NEXT.fetch_add(1 << 20, Ordering::Relaxed)
    }

    fn key(n: u8) -> Key {
        Key::new(&[n; 36]).expect("key")
    }

    /// Fill block `b` of the layout in client memory with `fill`, return
    /// its payload.
    fn fill_block(link: &MemLink, l: &Layout, b: u32, fill: u8) -> Vec<u8> {
        let mut c = link.client.borrow_mut();
        let mut payload = Vec::new();
        for (addr, len, _) in l.block_pieces(b).expect("block") {
            for i in 0..len as usize {
                let v = fill.wrapping_add(i as u8);
                c[addr as usize + i] = v;
                payload.push(v);
            }
        }
        payload
    }

    fn read_block(link: &MemLink, l: &Layout, b: u32) -> Vec<u8> {
        let c = link.client.borrow();
        l.block_pieces(b)
            .expect("block")
            .flat_map(|(addr, len, _)| c[addr as usize..addr as usize + len as usize].to_vec())
            .collect()
    }

    fn setup(verify: bool) -> (Shared, Session, MemLink, Client) {
        setup_with(shared(verify))
    }

    fn setup_with(sh: Shared) -> (Shared, Session, MemLink, Client) {
        let mut s = Session::new("tenant".into());
        let link = MemLink::new(false);
        let mut c = Client::new("tenant", SECRET);
        let r = c
            .call(&mut s, &sh, &link, |seq| Request::Layout {
                req_id: 1,
                seq,
                layout: layout(),
            })
            .expect("layout");
        assert_eq!(
            r,
            Response::LayoutAck {
                req_id: 1,
                status: STATUS_OK
            }
        );
        (sh, s, link, c)
    }

    fn put(
        c: &mut Client,
        s: &mut Session,
        sh: &Shared,
        link: &MemLink,
        n: u8,
        block: u32,
        check: u64,
    ) -> Response {
        c.call(s, sh, link, |seq| Request::Put {
            req_id: 10,
            seq,
            namespace: NS,
            layout_id: 1,
            budget_ms: 1000,
            entries: vec![PutEntry {
                key: key(n),
                block,
                check,
            }],
        })
        .expect("put")
    }

    fn get(
        c: &mut Client,
        s: &mut Session,
        sh: &Shared,
        link: &MemLink,
        entries: Vec<GetEntry>,
    ) -> Response {
        c.call(s, sh, link, |seq| Request::Get {
            req_id: 11,
            seq,
            namespace: NS,
            layout_id: 1,
            budget_ms: 1000,
            entries,
        })
        .expect("get")
    }

    #[test]
    fn a_put_block_comes_back_on_get_scattered_over_its_pages() {
        let (sh, mut s, link, mut c) = setup(true);
        let l = layout();
        let payload = fill_block(&link, &l, 2, 7);
        let check = block_check(&NS, key(1).as_bytes(), &payload);
        assert_eq!(
            put(&mut c, &mut s, &sh, &link, 1, 2, check),
            Response::Put {
                req_id: 10,
                statuses: vec![STATUS_OK]
            }
        );
        // A second PUT of the same key is told EXISTS.
        assert_eq!(
            put(&mut c, &mut s, &sh, &link, 1, 2, check),
            Response::Put {
                req_id: 10,
                statuses: vec![STATUS_EXISTS]
            }
        );
        // GET it into another block; a missing key misses.
        let r = get(
            &mut c,
            &mut s,
            &sh,
            &link,
            vec![
                GetEntry {
                    key: key(1),
                    block: 5,
                },
                GetEntry {
                    key: key(2),
                    block: 6,
                },
            ],
        );
        let Response::Get { results, .. } = r else {
            panic!("{r:?}")
        };
        assert_eq!(
            results[0],
            GetResult {
                status: STATUS_OK,
                length: 175,
                check
            }
        );
        assert_eq!(results[1].status, STATUS_MISS);
        assert_eq!(read_block(&link, &l, 5), payload);
        assert_eq!(
            read_block(&link, &l, 6),
            vec![0; 175],
            "a miss writes nothing"
        );
        // HEAD sees it.
        let r = c
            .call(&mut s, &sh, &link, |seq| Request::Head {
                req_id: 12,
                seq,
                namespace: NS,
                keys: vec![key(1), key(2)],
            })
            .expect("head");
        assert_eq!(
            r,
            Response::Head {
                req_id: 12,
                statuses: vec![STATUS_OK, STATUS_MISS]
            }
        );
    }

    #[test]
    fn a_put_whose_bytes_do_not_match_its_check_is_not_stored() {
        let (sh, mut s, link, mut c) = setup(true);
        let l = layout();
        let payload = fill_block(&link, &l, 0, 1);
        let check = block_check(&NS, key(1).as_bytes(), &payload) ^ 1;
        assert_eq!(
            put(&mut c, &mut s, &sh, &link, 1, 0, check).clone(),
            Response::Put {
                req_id: 10,
                statuses: vec![STATUS_CHECK_MISMATCH]
            }
        );
        assert!(!sh
            .cache
            .contains(&BlockId::new(NS, key(1).as_bytes()).expect("id")));
        // The key is free again for a correct PUT.
        let good = block_check(&NS, key(1).as_bytes(), &payload);
        assert_eq!(
            put(&mut c, &mut s, &sh, &link, 1, 0, good),
            Response::Put {
                req_id: 10,
                statuses: vec![STATUS_OK]
            }
        );
        // With verification off, the check is stored as given.
        let (sh2, mut s2, link2, mut c2) = setup(false);
        fill_block(&link2, &l, 0, 1);
        assert_eq!(
            put(&mut c2, &mut s2, &sh2, &link2, 3, 0, 42),
            Response::Put {
                req_id: 10,
                statuses: vec![STATUS_OK]
            }
        );
    }

    #[test]
    fn requests_from_another_identity_namespace_or_signature_are_refused() {
        let (sh, mut s, link, _) = setup(true);
        let refused = |r: Result<Response, Fatal>| {
            matches!(
                r,
                Ok(Response::Err {
                    status: STATUS_UNAUTHORIZED,
                    ..
                })
            )
        };
        // A validly signed request under another access key.
        let mut other = Client::new("someone-else", SECRET);
        assert!(refused(other.call(&mut s, &sh, &link, |seq| {
            Request::Head {
                req_id: 1,
                seq,
                namespace: NS,
                keys: vec![key(1)],
            }
        })));
        // The right key with a wrong secret.
        let mut forger = Client::new("tenant", "wrong");
        assert!(refused(forger.call(&mut s, &sh, &link, |seq| {
            Request::Head {
                req_id: 1,
                seq,
                namespace: NS,
                keys: vec![key(1)],
            }
        })));
        // Another namespace, for every kind of request: nothing moves.
        let mut c = Client::new("tenant", SECRET);
        c.seq = 100;
        let other = [6; NAMESPACE_LEN];
        assert!(refused(c.call(&mut s, &sh, &link, |seq| Request::Head {
            req_id: 1,
            seq,
            namespace: other,
            keys: vec![key(1)]
        })));
        assert!(refused(c.call(&mut s, &sh, &link, |seq| Request::Get {
            req_id: 2,
            seq,
            namespace: other,
            layout_id: 1,
            budget_ms: 100,
            entries: vec![GetEntry {
                key: key(1),
                block: 0
            }]
        })));
        assert!(refused(c.call(&mut s, &sh, &link, |seq| Request::Put {
            req_id: 3,
            seq,
            namespace: other,
            layout_id: 1,
            budget_ms: 100,
            entries: vec![PutEntry {
                key: key(1),
                block: 0,
                check: 0
            }]
        })));
        assert_eq!(link.calls.get(), 0, "a refused request moves nothing");
        // A tenant not allowed to register layouts.
        let sh2 = Shared::new(
            cache(1, Duration::from_secs(1)),
            auth(true),
            KvServerConfig::default(),
        )
        .expect("shared");
        let mut s2 = Session::new("tenant".into());
        let mut c2 = Client::new("tenant", SECRET);
        assert!(refused(c2.call(&mut s2, &sh2, &link, |seq| {
            Request::Layout {
                req_id: 4,
                seq,
                layout: layout(),
            }
        })));
    }

    #[test]
    fn a_replayed_or_out_of_order_request_is_refused() {
        let (sh, mut s, link, mut c) = setup(true);
        let req = Request::Head {
            req_id: 1,
            seq: 50,
            namespace: NS,
            keys: vec![key(1)],
        };
        let mut body = vec![0u8; KV_MSG_SIZE];
        let n = req.encode_into(&mut body).expect("encode");
        body.truncate(n);
        let header = sign_request(&c.creds, 999_999, &body);
        assert!(matches!(
            s.handle(&sh, &link, &header, req.clone(), &body, Instant::now()),
            Ok(Response::Head { .. })
        ));
        // The same signed bytes again: the nonce is spent.
        assert!(matches!(
            s.handle(&sh, &link, &header, req, &body, Instant::now()),
            Ok(Response::Err {
                status: STATUS_UNAUTHORIZED,
                ..
            })
        ));
        // A fresh nonce but a seq at or below the last.
        c.seq = 49;
        assert!(matches!(
            c.call(&mut s, &sh, &link, |seq| Request::Head {
                req_id: 2,
                seq,
                namespace: NS,
                keys: vec![key(1)]
            }),
            Ok(Response::Err {
                status: STATUS_UNAUTHORIZED,
                ..
            })
        ));
    }

    #[test]
    fn layouts_are_registered_once_and_checked() {
        let (sh, mut s, link, mut c) = setup(true);
        let r = c
            .call(&mut s, &sh, &link, |seq| Request::Layout {
                req_id: 2,
                seq,
                layout: layout(),
            })
            .expect("layout");
        assert_eq!(
            r,
            Response::LayoutAck {
                req_id: 2,
                status: STATUS_BAD_LAYOUT
            },
            "same id twice"
        );
        // An unknown layout id refuses the whole request.
        let r = c
            .call(&mut s, &sh, &link, |seq| Request::Get {
                req_id: 3,
                seq,
                namespace: NS,
                layout_id: 77,
                budget_ms: 10,
                entries: vec![GetEntry {
                    key: key(1),
                    block: 0,
                }],
            })
            .expect("get");
        assert_eq!(
            r,
            Response::Err {
                req_id: 3,
                status: STATUS_BAD_LAYOUT
            }
        );
        // A block index outside the layout.
        let r = put(&mut c, &mut s, &sh, &link, 1, 8, 0);
        assert_eq!(
            r,
            Response::Put {
                req_id: 10,
                statuses: vec![STATUS_BAD_LAYOUT]
            }
        );
        // A layout whose block is larger than a slot is refused up front.
        let mut big = layout();
        big.layout_id = 2;
        // Slots are rounded up to 64 KiB: a 70,000-byte page exceeds one.
        big.tensors[0].page = 70_000;
        big.tensors[0].stride = 70_000;
        let r = c
            .call(&mut s, &sh, &link, |seq| Request::Layout {
                req_id: 5,
                seq,
                layout: big,
            })
            .expect("layout");
        assert_eq!(
            r,
            Response::LayoutAck {
                req_id: 5,
                status: STATUS_BAD_LAYOUT
            }
        );
        // At most MAX_LAYOUTS_PER_CONNECTION layouts.
        for id in 10..10 + MAX_LAYOUTS_PER_CONNECTION as u32 {
            let mut l = layout();
            l.layout_id = id;
            let r = c
                .call(&mut s, &sh, &link, |seq| Request::Layout {
                    req_id: 6,
                    seq,
                    layout: l,
                })
                .expect("layout");
            let want = if id < 10 + MAX_LAYOUTS_PER_CONNECTION as u32 - 1 {
                STATUS_OK
            } else {
                STATUS_BAD_LAYOUT
            };
            assert_eq!(
                r,
                Response::LayoutAck {
                    req_id: 6,
                    status: want
                },
                "layout {id}"
            );
        }
    }

    #[test]
    fn a_stored_block_of_another_size_is_not_sent_into_this_layout() {
        let (sh, mut s, link, mut c) = setup(true);
        let l = layout();
        let payload = fill_block(&link, &l, 0, 9);
        put(
            &mut c,
            &mut s,
            &sh,
            &link,
            1,
            0,
            block_check(&NS, key(1).as_bytes(), &payload),
        );
        // A second layout with a smaller block.
        let small = Layout {
            layout_id: 2,
            rkey: 9,
            num_blocks: 4,
            tensors: vec![TensorRun {
                base: 3000,
                stride: 10,
                page: 10,
            }],
        };
        c.call(&mut s, &sh, &link, |seq| Request::Layout {
            req_id: 2,
            seq,
            layout: small,
        })
        .expect("layout");
        let calls = link.calls.get();
        let r = c
            .call(&mut s, &sh, &link, |seq| Request::Get {
                req_id: 3,
                seq,
                namespace: NS,
                layout_id: 2,
                budget_ms: 100,
                entries: vec![GetEntry {
                    key: key(1),
                    block: 0,
                }],
            })
            .expect("get");
        let Response::Get { results, .. } = r else {
            panic!("{r:?}")
        };
        assert_eq!(results[0].status, STATUS_BAD_LAYOUT);
        assert_eq!(link.calls.get(), calls, "nothing written");
    }

    #[test]
    fn a_full_store_and_a_pending_key_are_reported() {
        // One slot.
        let (sh, mut s, link, mut c) = setup_with(
            Shared::new(
                cache(1, Duration::from_secs(10)),
                auth(false),
                KvServerConfig::default(),
            )
            .expect("shared"),
        );
        let l = layout();
        let p0 = fill_block(&link, &l, 0, 1);
        put(
            &mut c,
            &mut s,
            &sh,
            &link,
            1,
            0,
            block_check(&NS, key(1).as_bytes(), &p0),
        );
        // The block is pinned by a hit held elsewhere: no slot for a new one.
        let hit = sh
            .cache
            .get(&BlockId::new(NS, key(1).as_bytes()).expect("id"))
            .expect("hit");
        assert_eq!(
            put(&mut c, &mut s, &sh, &link, 2, 1, 0),
            Response::Put {
                req_id: 10,
                statuses: vec![STATUS_NO_SPACE]
            }
        );
        drop(hit);
        // A key another PUT holds pending.
        let pending = sh
            .cache
            .reserve(
                BlockId::new(NS, key(3).as_bytes()).expect("id"),
                175,
                0,
                Instant::now() + Duration::from_secs(5),
            )
            .expect("reserve");
        assert_eq!(
            put(&mut c, &mut s, &sh, &link, 3, 1, 0),
            Response::Put {
                req_id: 10,
                statuses: vec![STATUS_PENDING]
            }
        );
        drop(pending);
    }

    #[test]
    fn a_put_held_past_its_deadline_is_read_but_not_stored() {
        // A 1 ms cap on pending keys, and a link that takes 5 ms to read.
        struct Slow(MemLink);
        impl BlockLink for Slow {
            fn write_scatter(&self, d: &[u8], p: &[Piece]) -> Result<(), TransferError> {
                self.0.write_scatter(d, p)
            }
            fn read_gather(&self, b: &mut [u8], p: &[Piece]) -> Result<(), TransferError> {
                std::thread::sleep(Duration::from_millis(5));
                self.0.read_gather(b, p)
            }
        }
        let sh = Shared::new(
            cache(2, Duration::from_millis(1)),
            auth(false),
            KvServerConfig::default(),
        )
        .expect("shared");
        let mut s = Session::new("tenant".into());
        let link = Slow(MemLink::new(false));
        let mut c = Client::new("tenant", SECRET);
        c.call(&mut s, &sh, &link.0, |seq| Request::Layout {
            req_id: 1,
            seq,
            layout: layout(),
        })
        .expect("layout");
        let payload = fill_block(&link.0, &layout(), 0, 4);
        let check = block_check(&NS, key(1).as_bytes(), &payload);
        c.seq += 1;
        c.nonce += 1;
        let req = Request::Put {
            req_id: 7,
            seq: c.seq,
            namespace: NS,
            layout_id: 1,
            budget_ms: 1000,
            entries: vec![PutEntry {
                key: key(1),
                block: 0,
                check,
            }],
        };
        let mut body = vec![0u8; KV_MSG_SIZE];
        let n = req.encode_into(&mut body).expect("encode");
        body.truncate(n);
        let header = sign_request(&c.creds, c.nonce, &body);
        let r = s
            .handle(&sh, &link, &header, req, &body, Instant::now())
            .expect("put");
        assert_eq!(
            r,
            Response::Put {
                req_id: 7,
                statuses: vec![STATUS_NOT_STORED]
            }
        );
        assert!(!sh
            .cache
            .contains(&BlockId::new(NS, key(1).as_bytes()).expect("id")));
    }

    #[test]
    fn without_verification_a_get_returns_the_check_as_written() {
        let (sh, mut s, link, mut c) = setup(false);
        fill_block(&link, &layout(), 0, 1);
        put(&mut c, &mut s, &sh, &link, 1, 0, 42);
        let r = get(
            &mut c,
            &mut s,
            &sh,
            &link,
            vec![GetEntry {
                key: key(1),
                block: 1,
            }],
        );
        let Response::Get { results, .. } = r else {
            panic!("{r:?}")
        };
        assert_eq!(results[0].check, 42);
    }

    #[test]
    fn connections_are_capped_per_identity() {
        let cap = ConnectionCap::new(2);
        let a1 = cap.admit("a").expect("first");
        let a2 = cap.admit("a").expect("second");
        assert!(cap.admit("a").is_none(), "third refused");
        assert!(cap.admit("b").is_some(), "another identity is not affected");
        drop(a1);
        assert_eq!(cap.held("a"), 1);
        let a3 = cap.admit("a").expect("room again");
        drop((a2, a3));
        assert_eq!(cap.held("a"), 0);
        assert!(ConnectionCap::new(0).admit("a").is_none());
    }

    #[test]
    fn a_request_over_the_byte_cap_moves_nothing() {
        let sh = Shared::new(
            cache(4, Duration::from_secs(1)),
            auth(false),
            KvServerConfig {
                max_request_bytes: 300,
                verify_puts: true,
            },
        )
        .expect("shared");
        let mut s = Session::new("tenant".into());
        let link = MemLink::new(false);
        let mut c = Client::new("tenant", SECRET);
        c.call(&mut s, &sh, &link, |seq| Request::Layout {
            req_id: 1,
            seq,
            layout: layout(),
        })
        .expect("layout");
        // Two blocks of 175 bytes exceed 300.
        let r = get(
            &mut c,
            &mut s,
            &sh,
            &link,
            vec![
                GetEntry {
                    key: key(1),
                    block: 0,
                },
                GetEntry {
                    key: key(2),
                    block: 1,
                },
            ],
        );
        assert_eq!(
            r,
            Response::Err {
                req_id: 11,
                status: STATUS_ERROR
            }
        );
        assert_eq!(link.calls.get(), 0, "nothing moved");
        assert!(Shared::new(
            cache(1, Duration::from_secs(1)),
            auth(false),
            KvServerConfig {
                max_request_bytes: MAX_REQUEST_BYTES + 1,
                verify_puts: true
            }
        )
        .is_err());
    }

    #[test]
    fn an_expired_budget_moves_nothing() {
        let (sh, mut s, link, mut c) = setup(true);
        let l = layout();
        let payload = fill_block(&link, &l, 0, 3);
        put(
            &mut c,
            &mut s,
            &sh,
            &link,
            1,
            0,
            block_check(&NS, key(1).as_bytes(), &payload),
        );
        c.seq += 1;
        c.nonce += 1;
        let req = Request::Get {
            req_id: 4,
            seq: c.seq,
            namespace: NS,
            layout_id: 1,
            budget_ms: 1,
            entries: vec![GetEntry {
                key: key(1),
                block: 3,
            }],
        };
        let mut body = vec![0u8; KV_MSG_SIZE];
        let n = req.encode_into(&mut body).expect("encode");
        body.truncate(n);
        let header = sign_request(&c.creds, c.nonce, &body);
        // Received 5 ms ago with a 1 ms budget.
        let r = s
            .handle(
                &sh,
                &link,
                &header,
                req,
                &body,
                Instant::now() - Duration::from_millis(5),
            )
            .expect("get");
        let Response::Get { results, .. } = r else {
            panic!("{r:?}")
        };
        assert_eq!(results[0].status, STATUS_EXPIRED);
        assert_eq!(read_block(&link, &l, 3), vec![0; 175]);
    }

    #[test]
    fn a_failed_transfer_ends_the_connection_and_releases_the_slot() {
        let (sh, mut s, link, mut c) = setup(true);
        let l = layout();
        let payload = fill_block(&link, &l, 0, 3);
        let check = block_check(&NS, key(1).as_bytes(), &payload);
        let broken = MemLink::new(true);
        assert!(matches!(
            c.call(&mut s, &sh, &broken, |seq| Request::Put {
                req_id: 5,
                seq,
                namespace: NS,
                layout_id: 1,
                budget_ms: 1000,
                entries: vec![PutEntry {
                    key: key(1),
                    block: 0,
                    check
                }]
            }),
            Err(Fatal::Transfer(_))
        ));
        // The reservation was released (the error said the hardware was
        // done with the buffer): the key and slot are free.
        assert_eq!(
            put(&mut c, &mut s, &sh, &link, 1, 0, check),
            Response::Put {
                req_id: 10,
                statuses: vec![STATUS_OK]
            }
        );
        assert!(matches!(
            c.call(&mut s, &sh, &broken, |seq| Request::Get {
                req_id: 6,
                seq,
                namespace: NS,
                layout_id: 1,
                budget_ms: 1000,
                entries: vec![GetEntry {
                    key: key(1),
                    block: 1
                }]
            }),
            Err(Fatal::Transfer(_))
        ));
    }

    #[test]
    fn the_block_check_covers_namespace_key_and_payload() {
        let p = vec![1u8; 100];
        let a = block_check(&NS, &[1; 36], &p);
        assert_ne!(
            a,
            block_check(&[6; NAMESPACE_LEN], &[1; 36], &p),
            "namespace"
        );
        assert_ne!(a, block_check(&NS, &[2; 36], &p), "key");
        assert_ne!(a, block_check(&NS, &[1; 36], &[1u8; 99]), "payload");
        let mut all = NS.to_vec();
        all.extend_from_slice(&[1; 36]);
        all.extend_from_slice(&p);
        assert_eq!(a, nvstor_core::xxh3::xxh3_64(&all));
    }
}

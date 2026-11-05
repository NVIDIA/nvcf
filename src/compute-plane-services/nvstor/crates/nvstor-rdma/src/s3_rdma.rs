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

//! Object transfer over RDMA with SEND/RECV control and one-sided bulk data.
//!
//! GET is ranged: the server reads one range of the object straight into a
//! registered pool slot and RDMA-WRITEs it into the client's destination,
//! answering with the range length, object size, generation and CRC-32C.
//! PUT is server-pull: ALLOC reserves a server pool slot (admission only, no
//! address or key leaves the server), then PERSIST names the client buffer
//! holding the bytes and their CRC-32C; the server RDMA-READs them into the
//! slot, checks the CRC and persists. The server never exposes its memory:
//! its QPs grant peers no remote access (`crate::transport`).
//! REPLICATE is PERSIST into a separate replica store, accepted only from
//! peers that authenticated as cluster members in the bootstrap
//! (`crate::bootstrap::PeerRole::Cluster`).
//!
//! The three storage callbacks (`crate::object`) keep the transport
//! independent of the backend; nvstor-server wires both object transports to
//! one shared PmObjectStore, and REPLICATE to its chunk store.
//!
//! Current buffers are registered host allocations. PUT copies caller bytes into
//! a local pool slot for the server to read. `S3RdmaClient::get` walks an object
//! range by range through one local pool slot (verifying each range's CRC-32C
//! and generation) and returns a Vec; `get_range_into` is the per-range
//! primitive. This interface does not register GPU memory or provide
//! GPUDirect transfers.
//!
//! Use real hardware to validate this path: hardware-dependent unit tests are
//! `#[ignore]`d.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Once, OnceLock};
use std::thread;

use parking_lot::RwLock;
use tracing::{debug, info, warn};

use crate::bootstrap::PeerRole;
pub use crate::object::{DeleteFn, GetError, ObjectRead, ReadFn, WriteFn, ANY_GENERATION};
use crate::pool::PoolBuffer;
use crate::s3_rdma_proto::{
    Request, Response, ADMIT_WAIT, SLOT_WAIT, STATUS_CHECKSUM_MISMATCH, STATUS_ERROR,
    STATUS_GENERATION_CHANGED, STATUS_NOT_FOUND, STATUS_NO_SLOT, STATUS_UNAUTHORIZED,
    STATUS_UNSUPPORTED,
};
use crate::transport::RdmaTransport;

/// Server-side dispatcher. Holds the storage callbacks; the
/// transport (which carries data + control) is shared via `Arc`.
pub struct S3RdmaServer {
    write_fn: WriteFn,
    read_fn: ReadFn,
    delete_fn: DeleteFn,
    /// Store for REPLICATE (cluster replication). `None` refuses it.
    replicate_fn: Option<WriteFn>,
    /// Refuse every verb that reserves or changes server state (ALLOC,
    /// PERSIST, REPLICATE, DELETE). A read-only export cannot then be made
    /// to park pool slots at all.
    read_only: bool,
    transport: Arc<RdmaTransport>,
    /// Joined on `shutdown` so server threads don't outlive the
    /// transport.
    workers: RwLock<Vec<thread::JoinHandle<()>>>,
    /// Caps concurrently-executing requests. Acquired by the
    /// dispatcher before it spawns a worker, so worker threads can never
    /// outnumber the permits — a pipelining client is throttled instead of
    /// exhausting threads/memory. Also bounds how many pool slots can be
    /// in play at once, which pairs with the ALLOC/PERSIST slot TTL.
    admission: Arc<crate::admission::Admission>,
    /// Verifies each request's signature and applies policy.
    /// Supplied by the server binary so this crate stays IAM-agnostic.
    /// Required: a server constructed without one refuses every request,
    /// because failing closed is the only safe default for a transport
    /// that previously served anyone who could reach the port.
    auth_fn: Option<crate::auth::AuthFn>,
    /// Rejects replayed signatures within the clock-skew window. Shared
    /// by every server in the process by default, so a request captured
    /// on one rail cannot be replayed on another.
    replay: Arc<crate::auth::ReplayGuard>,
    /// Every rail (NIC) this node serves on, rail 0 first; returned by
    /// the RAILS request. Empty on a single-transport server.
    rails: Vec<crate::rail::RailInfo>,
}

/// The `req_id` of a request, so an auth rejection can be paired with the
/// request that caused it by a client demultiplexing on req_id.
fn req_id_of(req: &Request) -> u64 {
    match req {
        Request::Get { req_id, .. }
        | Request::Alloc { req_id, .. }
        | Request::Persist { req_id, .. }
        | Request::Replicate { req_id, .. }
        | Request::Delete { req_id, .. }
        | Request::Head { req_id, .. }
        | Request::Rails { req_id, .. } => *req_id,
    }
}

/// Map a request to the IAM action and object key it targets, so policy is
/// evaluated against what the caller is actually trying to do rather than
/// just "did they present a valid key".
///
/// `Alloc` reserves a server buffer and names no key yet; it is authorized
/// as a write intent, and the subsequent `Persist` — which does name a key
/// — is authorized against that key. Both are signed, so a caller cannot
/// Alloc under one identity and Persist under another.
fn action_and_key(req: &Request) -> (&'static str, &str) {
    match req {
        Request::Get { key, .. } => ("s3:GetObject", key.as_str()),
        Request::Alloc { .. } => ("s3:PutObject", ""),
        Request::Persist { key, .. } => ("s3:PutObject", key.as_str()),
        Request::Replicate { key, .. } => ("nvstor:ReplicateObject", key.as_str()),
        Request::Delete { key, .. } => ("s3:DeleteObject", key.as_str()),
        Request::Head { key, .. } => ("s3:GetObject", key.as_str()),
        // Topology is only handed to callers allowed to read the object
        // they are about to fetch across it.
        Request::Rails { key, .. } => ("s3:GetObject", key.as_str()),
    }
}

/// Whether a server serves `req` at all, decided before any work: a
/// read-only server refuses every verb that reserves or changes state,
/// and REPLICATE needs both a replica store and a peer that authenticated
/// as a cluster member (request signatures alone do not carry that role).
fn verb_allowed(
    req: &Request,
    read_only: bool,
    replicas: bool,
    role: Option<PeerRole>,
) -> Result<(), u8> {
    match req {
        Request::Replicate { .. } => {
            if role != Some(PeerRole::Cluster) {
                Err(STATUS_UNAUTHORIZED)
            } else if read_only || !replicas {
                Err(STATUS_UNSUPPORTED)
            } else {
                Ok(())
            }
        }
        Request::Alloc { .. } | Request::Persist { .. } | Request::Delete { .. } if read_only => {
            Err(STATUS_UNSUPPORTED)
        }
        _ => Ok(()),
    }
}

impl S3RdmaServer {
    pub fn new(
        write_fn: WriteFn,
        read_fn: ReadFn,
        delete_fn: DeleteFn,
        transport: Arc<RdmaTransport>,
    ) -> Arc<Self> {
        Self::with_max_in_flight(
            write_fn,
            read_fn,
            delete_fn,
            transport,
            crate::admission::default_max_in_flight(),
        )
    }

    /// Same as `new` but with an explicit in-flight cap.
    pub fn with_max_in_flight(
        write_fn: WriteFn,
        read_fn: ReadFn,
        delete_fn: DeleteFn,
        transport: Arc<RdmaTransport>,
        max_in_flight: usize,
    ) -> Arc<Self> {
        Self::with_admission(
            write_fn,
            read_fn,
            delete_fn,
            transport,
            crate::admission::Admission::new(max_in_flight),
        )
    }

    /// Same as `new` but drawing permits from `admission`, so the servers
    /// of a multi-NIC node (one per rail) share one in-flight cap.
    pub fn with_admission(
        write_fn: WriteFn,
        read_fn: ReadFn,
        delete_fn: DeleteFn,
        transport: Arc<RdmaTransport>,
        admission: Arc<crate::admission::Admission>,
    ) -> Arc<Self> {
        Arc::new(Self {
            write_fn,
            read_fn,
            delete_fn,
            transport,
            replicate_fn: None,
            read_only: false,
            workers: RwLock::new(Vec::new()),
            admission,
            auth_fn: None,
            replay: crate::auth::process_replay_guard(),
            rails: Vec::new(),
        })
    }

    /// A server that only serves reads (GET, HEAD, RAILS) from `read_fn`,
    /// drawing permits from `admission`. Every other verb is refused
    /// before it reserves anything.
    pub fn read_only(
        read_fn: ReadFn,
        transport: Arc<RdmaTransport>,
        admission: Arc<crate::admission::Admission>,
    ) -> Arc<Self> {
        let write_fn: WriteFn = Arc::new(|_, _| Err("read-only server".into()));
        let delete_fn: DeleteFn = Arc::new(|_| Err("read-only server".into()));
        let mut s = Self::with_admission(write_fn, read_fn, delete_fn, transport, admission);
        let inner = Arc::get_mut(&mut s).expect("invariant: just constructed, not shared");
        inner.read_only = true;
        s
    }

    /// Advertise this node's rails (rail 0 first) to RAILS requests.
    pub fn with_rails(mut self: Arc<Self>, rails: Vec<crate::rail::RailInfo>) -> Arc<Self> {
        let inner = Arc::get_mut(&mut self)
            .expect("invariant: with_rails is called before the server is shared");
        inner.rails = rails;
        self
    }

    /// Attach the request verifier. Without it every request is rejected.
    pub fn with_auth(mut self: Arc<Self>, auth_fn: crate::auth::AuthFn) -> Arc<Self> {
        let inner = Arc::get_mut(&mut self)
            .expect("invariant: with_auth is called before the server is shared");
        inner.auth_fn = Some(auth_fn);
        self
    }

    /// Accept REPLICATE from cluster peers into `replicate_fn`.
    pub fn with_replication(mut self: Arc<Self>, replicate_fn: WriteFn) -> Arc<Self> {
        let inner = Arc::get_mut(&mut self)
            .expect("invariant: with_replication is called before the server is shared");
        inner.replicate_fn = Some(replicate_fn);
        self
    }

    /// Use `replay` instead of the process-wide replay guard.
    pub fn with_replay_guard(
        mut self: Arc<Self>,
        replay: Arc<crate::auth::ReplayGuard>,
    ) -> Arc<Self> {
        let inner = Arc::get_mut(&mut self)
            .expect("invariant: with_replay_guard is called before the server is shared");
        inner.replay = replay;
        self
    }

    /// Start the server. The underlying `RdmaTransport` must already
    /// be `listen`-ing for incoming peers; this method registers the
    /// per-peer dispatcher with the transport's `on_peer_accepted`
    /// hook, so every QP handshake that completes gets a request-loop
    /// thread, and releases a peer's parked slots when it goes away.
    pub fn start(self: &Arc<Self>) {
        let outer = Arc::clone(self);
        self.transport.set_on_peer_accepted(Arc::new(move |peer_id| {
            let for_dispatch = Arc::clone(&outer);
            let spawned = thread::Builder::new()
                .name(format!("s3-rdma-peer-{peer_id}"))
                .spawn(move || for_dispatch.dispatch_loop(peer_id));
            match spawned {
                Ok(handle) => {
                    let mut workers = outer.workers.write();
                    workers.retain(|h| !h.is_finished());
                    workers.push(handle);
                }
                Err(e) => {
                    warn!(peer_id, error = %e, "S3-RDMA: cannot spawn a dispatcher; dropping peer");
                    outer.transport.disconnect(peer_id);
                }
            }
        }));
        self.transport.set_on_peer_closed(Arc::new(|peer_id| {
            let reclaimed = slot_drain_peer(peer_id);
            if reclaimed > 0 {
                info!(
                    peer_id,
                    reclaimed, "S3-RDMA: reclaimed parked slots on peer exit"
                );
            }
        }));

        // Spawn the TTL reaper once per process. Sweeps the in-flight
        // slot table every SLOT_TTL/2 so a "stuck client" leak surfaces
        // at most 1.5 × SLOT_TTL after the ALLOC. Daemonized; no join —
        // the table is process-wide.
        static REAPER: Once = Once::new();
        REAPER.call_once(|| {
            let spawned = thread::Builder::new()
                .name("s3-rdma-slot-reaper".into())
                .spawn(|| {
                    let interval = SLOT_TTL / 2;
                    loop {
                        thread::sleep(interval);
                        let n = slot_reap_expired(SLOT_TTL);
                        if n > 0 {
                            warn!(
                                reclaimed = n,
                                ttl_secs = SLOT_TTL.as_secs(),
                                "S3-RDMA: TTL reaper reclaimed stuck parked slots"
                            );
                        }
                    }
                });
            if let Err(e) = spawned {
                warn!(error = %e, "S3-RDMA: cannot spawn the slot reaper; \
                       parked slots are reclaimed only when their peer disconnects");
            }
        });

        info!(
            read_only = self.read_only,
            replication = self.replicate_fn.is_some(),
            "S3-over-RDMA server registered (real RDMA, control via SEND/RECV)"
        );
    }

    pub fn shutdown(&self) {
        self.admission.close();
        self.transport.shutdown();
    }

    pub fn drained(&self) -> bool {
        self.admission.drained()
    }

    fn dispatch_loop(self: &Arc<Self>, peer_id: u64) {
        info!(peer_id, "S3-RDMA dispatcher started");
        loop {
            let req = match self.transport.recv_request(peer_id) {
                Ok(r) => r,
                Err(e) => {
                    // The peer is gone (its bootstrap stream closed and its
                    // QPs were flushed) or its QP failed. Removing it is
                    // idempotent; the transport's on_peer_closed hook
                    // releases its parked slots either way.
                    if self.transport.disconnect(peer_id) {
                        debug!(peer_id, "S3-RDMA: removed dead peer from transport");
                    }
                    debug!(peer_id, error = %e, "S3-RDMA dispatcher exiting");
                    return;
                }
            };
            // Authenticate BEFORE doing any work. Rejecting here means
            // an unauthenticated caller never reaches a storage callback and
            // never consumes an admission permit or a worker thread, so a
            // flood of bad requests costs only signature verification.
            let (header, req, body) = req;
            if let Err(e) = self.authenticate(&header, &req, &body) {
                warn!(peer_id, access_key = %header.access_key, error = %e,
                      "S3-RDMA request rejected");
                let _ = self.transport.send_response(
                    peer_id,
                    &Response::Err {
                        req_id: req_id_of(&req),
                        status: STATUS_UNAUTHORIZED,
                    },
                );
                continue;
            }
            if let Err(status) = verb_allowed(
                &req,
                self.read_only,
                self.replicate_fn.is_some(),
                self.transport.peer_role(peer_id),
            ) {
                debug!(peer_id, status, "S3-RDMA verb refused");
                let _ = self.transport.send_response(
                    peer_id,
                    &Response::Err {
                        req_id: req_id_of(&req),
                        status,
                    },
                );
                continue;
            }

            // Hand the work to the admission's worker pool so the
            // dispatcher can pick up the next request while this one's
            // storage callback (object_write / object_read) is running.
            // The SEND for the response still serializes via the lane's
            // control SEND path, so responses on the same lane stay
            // well-formed; clients pair each request with its response
            // via the matching req_id.
            // Admission waits at most ADMIT_WAIT while every permit is
            // taken. The client already sent this request and waits
            // for its reply, so past that bound it gets NO_SLOT (retry
            // later) rather than no answer: a request is never left
            // unanswered for longer than SERVER_REPLY_WORST, which the
            // client's reply bound exceeds. The pool never starts more
            // workers than permits, and never one per request.
            let svc = Arc::clone(self);
            match admit(&self.admission, peer_id, req, ADMIT_WAIT, move |req| {
                if let Err(e) = svc.handle(peer_id, req) {
                    warn!(peer_id, error = %e, "S3-RDMA request failed");
                }
            }) {
                Admitted::Running => {}
                Admitted::Closed => return,
                Admitted::Refused(reply) => {
                    let _ = self.transport.send_response(peer_id, &reply);
                }
            }
        }
    }

    /// Verify a request: signature freshness, replay, then policy.
    ///
    /// Fails closed when no verifier is configured — an operator who
    /// forgets to wire auth gets a server that refuses traffic, not one
    /// that silently serves every tenant's data to anyone.
    fn authenticate(
        &self,
        header: &crate::auth::AuthHeader,
        req: &Request,
        body: &[u8],
    ) -> Result<String, crate::auth::AuthError> {
        let Some(ref auth_fn) = self.auth_fn else {
            return Err(crate::auth::AuthError::Missing);
        };
        // An empty access key is what an unconfigured client sends. Reject
        // it here rather than letting it reach the credential lookup, so
        // "no credentials" is never confusable with "bad credentials".
        if header.access_key.is_empty() {
            return Err(crate::auth::AuthError::Missing);
        }

        let (action, key) = action_and_key(req);
        // Verify (signature + policy) BEFORE consuming the nonce. Recording
        // first would let anyone who knows an access key spam (key, nonce)
        // pairs and lock the legitimate holder out of its own nonce space —
        // an unauthenticated denial of service. Only a validly-signed
        // request gets to burn a nonce.
        let principal = auth_fn(&crate::auth::AuthContext {
            header,
            body,
            action,
            key,
        })?;
        self.replay
            .check_and_record(&header.access_key, header.timestamp, header.nonce)?;
        Ok(principal)
    }

    fn handle(&self, peer_id: u64, req: Request) -> Result<(), String> {
        match req {
            // -----------------------------------------------------
            // GET: training-data fanout. Push the object bytes
            // directly into the client's registered destination.
            // -----------------------------------------------------
            Request::Get {
                req_id,
                key,
                offset,
                if_generation,
                client_addr,
                client_rkey,
                client_len,
            } => {
                // The pool slot is both the storage read target and the
                // RDMA WRITE source, so range bytes are never copied on
                // the server and never staged on the heap. No peer can
                // touch the slot meanwhile: server QPs grant no remote
                // access, so the CRC below covers exactly what is sent.
                let pool = self.transport.pool();
                let Some(mut slot) = pool.checkout_timeout(SLOT_WAIT) else {
                    return self.transport.send_response(
                        peer_id,
                        &Response::Err {
                            req_id,
                            status: STATUS_NO_SLOT,
                        },
                    );
                };
                let want = usize::try_from(client_len)
                    .unwrap_or(usize::MAX)
                    .min(slot.len());
                let read = match (self.read_fn)(&key, offset, &mut slot.as_mut_slice()[..want]) {
                    Ok(r) => r,
                    Err(e) => return self.send_read_error(peer_id, req_id, &key, &e),
                };
                // Only bytes the read placed in this buffer may leave the
                // slot: past `want` it holds an earlier request's data.
                if read.len > want {
                    warn!(key = %key, len = read.len, want, "ReadFn reported more bytes than its buffer; refusing");
                    return self.transport.send_response(
                        peer_id,
                        &Response::Err {
                            req_id,
                            status: STATUS_ERROR,
                        },
                    );
                }
                if if_generation != ANY_GENERATION && read.generation != if_generation {
                    return self.transport.send_response(
                        peer_id,
                        &Response::Err {
                            req_id,
                            status: STATUS_GENERATION_CHANGED,
                        },
                    );
                }
                let data = &slot.as_slice()[..read.len];
                let crc32c = nvstor_core::checksum::crc32c(data);
                if !data.is_empty() {
                    if let Err(e) = self.transport.write_remote_to(
                        peer_id,
                        data,
                        pool.lkey(),
                        client_addr,
                        client_rkey,
                    ) {
                        // A failed one-sided op leaves the QP in error:
                        // the connection is unusable, so free it now. If
                        // the WRITE's completion was never seen the NIC
                        // may still read the slot: the lane keeps it
                        // until its QP is destroyed.
                        self.transport.disconnect(peer_id);
                        return Err(dispose_after_failure(e, slot));
                    }
                }
                self.transport.send_response(
                    peer_id,
                    &Response::Range {
                        req_id,
                        length: read.len as u64,
                        object_size: read.size,
                        generation: read.generation,
                        crc32c,
                    },
                )
            }

            // -----------------------------------------------------
            // ALLOC: reserve a pool slot for the PUT that follows.
            // Only an opaque token goes back: the server pulls the
            // bytes itself at PERSIST.
            // -----------------------------------------------------
            Request::Alloc { req_id, size } => {
                let pool = self.transport.pool();
                let slot = match pool.checkout_timeout(SLOT_WAIT) {
                    Some(s) => s,
                    None => {
                        return self.transport.send_response(
                            peer_id,
                            &Response::Err {
                                req_id,
                                status: STATUS_NO_SLOT,
                            },
                        );
                    }
                };
                if usize::try_from(size).map_or(true, |s| s > slot.len()) {
                    drop(slot);
                    return self.transport.send_response(
                        peer_id,
                        &Response::Err {
                            req_id,
                            status: STATUS_ERROR,
                        },
                    );
                }
                let length = slot.len() as u64;
                let token = slot_park(peer_id, slot);
                self.transport.send_response(
                    peer_id,
                    &Response::Slot {
                        req_id,
                        slot: token,
                        length,
                    },
                )
            }

            Request::Persist {
                req_id,
                key,
                slot,
                client_addr,
                client_rkey,
                length,
                crc32c,
            } => {
                let store = Arc::clone(&self.write_fn);
                self.pull_and_store(
                    peer_id,
                    req_id,
                    &key,
                    slot,
                    client_addr,
                    client_rkey,
                    length,
                    crc32c,
                    &store,
                )
            }

            Request::Replicate {
                req_id,
                key,
                slot,
                client_addr,
                client_rkey,
                length,
                crc32c,
            } => {
                let Some(store) = self.replicate_fn.clone() else {
                    // verb_allowed refused this already; answer anyway.
                    return self.transport.send_response(
                        peer_id,
                        &Response::Err {
                            req_id,
                            status: STATUS_UNSUPPORTED,
                        },
                    );
                };
                self.pull_and_store(
                    peer_id,
                    req_id,
                    &key,
                    slot,
                    client_addr,
                    client_rkey,
                    length,
                    crc32c,
                    &store,
                )
            }

            Request::Delete { req_id, key } => {
                if (self.delete_fn)(&key).is_ok() {
                    self.transport.send_response(
                        peer_id,
                        &Response::Ok {
                            req_id,
                            length: 0,
                            etag: [0u8; 32],
                        },
                    )
                } else {
                    self.transport.send_response(
                        peer_id,
                        &Response::Err {
                            req_id,
                            status: STATUS_NOT_FOUND,
                        },
                    )
                }
            }
            Request::Head { req_id, key } => match (self.read_fn)(&key, 0, &mut []) {
                Ok(stat) => self.transport.send_response(
                    peer_id,
                    &Response::Range {
                        req_id,
                        length: 0,
                        object_size: stat.size,
                        generation: stat.generation,
                        crc32c: 0,
                    },
                ),
                Err(e) => self.send_read_error(peer_id, req_id, &key, &e),
            },
            Request::Rails { req_id, .. } => self.transport.send_response(
                peer_id,
                &Response::Rails {
                    req_id,
                    rails: self.rails.clone(),
                    instance: Some(process_instance()),
                },
            ),
        }
    }

    /// PERSIST / REPLICATE: RDMA-READ `length` bytes from the client
    /// buffer the signed request names into the slot ALLOC reserved,
    /// check them against the client's CRC-32C, then hand them to `store`.
    #[allow(clippy::too_many_arguments)]
    fn pull_and_store(
        &self,
        peer_id: u64,
        req_id: u64,
        key: &str,
        token: u64,
        client_addr: u64,
        client_rkey: u32,
        length: u64,
        crc32c: u32,
        store: &WriteFn,
    ) -> Result<(), String> {
        let err = |status| {
            self.transport
                .send_response(peer_id, &Response::Err { req_id, status })
        };
        let Some(mut slot) = slot_take(peer_id, token) else {
            return err(STATUS_ERROR);
        };
        // `length` is client-controlled: it may not exceed the slot. The
        // slot holds exactly the bytes pulled below, so nothing from a
        // previous use of the slot can be persisted.
        let Some(len) = usize::try_from(length).ok().filter(|&n| n <= slot.len()) else {
            warn!(
                key,
                length,
                slot_len = slot.len(),
                "PERSIST length exceeds slot; rejecting"
            );
            return err(STATUS_ERROR);
        };
        if len > 0 {
            let lkey = slot.lkey();
            if let Err(e) = self.transport.read_remote_from(
                peer_id,
                &mut slot.as_mut_slice()[..len],
                lkey,
                client_addr,
                client_rkey,
            ) {
                // The QP is in error after a failed READ; free the peer.
                // If the READ's completion was never seen the NIC may
                // still write the slot: the lane keeps it until its QP is
                // destroyed.
                self.transport.disconnect(peer_id);
                return Err(format!(
                    "PERSIST pull of {key}: {}",
                    dispose_after_failure(e, slot)
                ));
            }
        }
        let data = &slot.as_slice()[..len];
        let actual = nvstor_core::checksum::crc32c(data);
        if actual != crc32c {
            warn!(
                key,
                expected = crc32c,
                actual,
                "PERSIST checksum mismatch; nothing stored"
            );
            return err(STATUS_CHECKSUM_MISMATCH);
        }
        let result = store(key, data);
        let etag = etag_of(data);
        drop(slot);
        match result {
            Ok(written) => self.transport.send_response(
                peer_id,
                &Response::Ok {
                    req_id,
                    length: written,
                    etag,
                },
            ),
            Err(e) => {
                warn!(key, error = %e, "PERSIST failed");
                err(STATUS_ERROR)
            }
        }
    }

    /// NotFound is an ordinary answer; anything else is a server-side
    /// failure the operator needs to see, and must not masquerade as a
    /// missing key to the client.
    fn send_read_error(
        &self,
        peer_id: u64,
        req_id: u64,
        key: &str,
        e: &std::io::Error,
    ) -> Result<(), String> {
        let status = if e.kind() == std::io::ErrorKind::NotFound {
            STATUS_NOT_FOUND
        } else {
            warn!(key = %key, error = %e, "S3-RDMA read failed");
            STATUS_ERROR
        };
        self.transport
            .send_response(peer_id, &Response::Err { req_id, status })
    }
}

// ---------------------------------------------------------------------
// In-flight slot table: parks PoolBuffers between ALLOC and PERSIST.
// Global because slots are server-process scoped, not per-server.
//
// Two reapers keep this from leaking pool capacity:
//   * `slot_drain_peer(peer_id)` runs when the transport removes the
//     peer (its bootstrap stream closed, or it was disconnected).
//   * A background TTL reaper sweeps any slot whose `parked_at` is
//     older than `SLOT_TTL`. Handles "client connection alive but
//     never sent the matching PERSIST" (stuck thread, GC pause,
//     misbehaving SDK). Spawned once from `S3RdmaServer::start`.
// ---------------------------------------------------------------------

/// How long a parked slot can sit before the TTL reaper claws it
/// back. AI-checkpoint clients should call PERSIST within seconds
/// of ALLOC; 60 s leaves room for slow links while still bounding
/// the leak.
pub(crate) const SLOT_TTL: std::time::Duration = std::time::Duration::from_secs(60);

// How long a GET / ALLOC waits for a free server pool slot before
// answering NO_SLOT: `s3_rdma_proto::SLOT_WAIT`. Requests beyond the
// pool's size queue there instead of failing at once; it is one term of
// the server's worst-case reply time, which the client's reply bound
// (`s3_rdma_proto::CONTROL_REPLY_TIMEOUT`) exceeds, so a queued client
// gets an answer before it gives up on the connection.

struct ParkedSlot {
    buf: PoolBuffer,
    parked_at: std::time::Instant,
}

static IN_FLIGHT: OnceLock<parking_lot::Mutex<HashMap<(u64, u64), ParkedSlot>>> = OnceLock::new();

/// After a failed RDMA transfer from or into `slot`: give the slot to
/// the error, which drops it (back to the pool) if the hardware is done
/// with it and otherwise has the lane keep it until its QP is destroyed.
/// Returns the error text.
fn dispose_after_failure<B: Send + 'static>(e: crate::transport::TransferError, slot: B) -> String {
    let msg = e.to_string();
    e.hold(slot);
    msg
}

/// This server process's instance id, sent in every RAILS reply: random,
/// made once per process. It tells clients two servers apart even when
/// they share a host and its rail GIDs (two serve processes on one node)
/// or a reused address (a replacement pod), and tells them two addresses
/// of one server are one.
pub fn process_instance() -> [u8; 16] {
    static ID: std::sync::OnceLock<[u8; 16]> = std::sync::OnceLock::new();
    *ID.get_or_init(|| {
        let mut id = [0u8; 16];
        if getrandom::getrandom(&mut id).is_err() {
            // No entropy source: the clock and the pid still tell this
            // process apart from the ones before and next to it.
            let t = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos())
                .unwrap_or(0);
            id = (t ^ (u128::from(std::process::id()) << 96)).to_le_bytes();
        }
        id
    })
}

/// What [`admit`] did with a request.
#[derive(Debug, PartialEq)]
enum Admitted {
    /// A worker runs it; the worker answers.
    Running,
    /// Refused: send this reply (NO_SLOT) now.
    Refused(Response),
    /// The server is shutting down.
    Closed,
}

/// Hand `req` of `peer_id` to a worker (`run`), waiting at most `wait`
/// for one. If no worker frees up in time, or none can be started, the
/// request is refused with NO_SLOT (and a PERSIST / REPLICATE's parked
/// slot given back), so it is answered within the server's worst case
/// (`s3_rdma_proto::SERVER_REPLY_WORST`) instead of never.
fn admit(
    admission: &Arc<crate::admission::Admission>,
    peer_id: u64,
    req: Request,
    wait: std::time::Duration,
    run: impl FnOnce(Request) + Send + 'static,
) -> Admitted {
    let req_id = req_id_of(&req);
    let parked = parked_token(&req);
    match admission.execute_within(wait, move || run(req)) {
        Ok(()) => Admitted::Running,
        Err(crate::admission::ExecError::Closed) => Admitted::Closed,
        Err(crate::admission::ExecError::Busy) => {
            debug!(
                peer_id,
                req_id, "S3-RDMA: no worker free in time; answering NO_SLOT"
            );
            Admitted::Refused(refuse_busy(peer_id, req_id, parked))
        }
        Err(crate::admission::ExecError::Spawn(e)) => {
            warn!(peer_id, error = %e, "S3-RDMA: cannot start a worker; answering NO_SLOT");
            Admitted::Refused(refuse_busy(peer_id, req_id, parked))
        }
    }
}

/// The parked ALLOC slot a PERSIST / REPLICATE names, if any.
fn parked_token(req: &Request) -> Option<u64> {
    match req {
        Request::Persist { slot, .. } | Request::Replicate { slot, .. } => Some(*slot),
        _ => None,
    }
}

/// The reply to a request the server is too busy to run: NO_SLOT, which
/// clients treat as "retry later". A PERSIST / REPLICATE refused this way
/// will not use its parked slot, so it goes back to the pool now rather
/// than at the TTL.
fn refuse_busy(peer_id: u64, req_id: u64, parked: Option<u64>) -> Response {
    if let Some(token) = parked {
        drop(slot_take(peer_id, token));
    }
    Response::Err {
        req_id,
        status: STATUS_NO_SLOT,
    }
}

/// Slot tokens. Opaque to clients, unique per process; the table is keyed
/// by (peer, token), so a peer can only name its own reservations.
static NEXT_SLOT_TOKEN: AtomicU64 = AtomicU64::new(1);

fn in_flight() -> &'static parking_lot::Mutex<HashMap<(u64, u64), ParkedSlot>> {
    IN_FLIGHT.get_or_init(|| parking_lot::Mutex::new(HashMap::new()))
}
fn slot_park(peer_id: u64, slot: PoolBuffer) -> u64 {
    let token = NEXT_SLOT_TOKEN.fetch_add(1, Ordering::Relaxed);
    in_flight().lock().insert(
        (peer_id, token),
        ParkedSlot {
            buf: slot,
            parked_at: std::time::Instant::now(),
        },
    );
    token
}
fn slot_take(peer_id: u64, token: u64) -> Option<PoolBuffer> {
    in_flight().lock().remove(&(peer_id, token)).map(|p| p.buf)
}

/// Drop every parked slot for a peer. Called when the transport removes
/// the peer, so a client that dies between ALLOC and PERSIST doesn't
/// leak `PoolBuffer`s out of the pool. Returns the number of slots
/// reclaimed (≥ 0; logged for visibility).
fn slot_drain_peer(peer_id: u64) -> usize {
    let mut guard = in_flight().lock();
    let before = guard.len();
    guard.retain(|&(p, _token), _| p != peer_id);
    before - guard.len()
}

/// Drop slots parked for longer than `ttl`. Returns the count
/// reclaimed. Called by the background TTL reaper thread.
fn slot_reap_expired(ttl: std::time::Duration) -> usize {
    let now = std::time::Instant::now();
    let mut guard = in_flight().lock();
    let before = guard.len();
    guard.retain(|_, p| now.duration_since(p.parked_at) < ttl);
    before - guard.len()
}

fn etag_of(data: &[u8]) -> [u8; 32] {
    use sha2::{Digest, Sha256};
    let mut h = Sha256::new();
    h.update(data);
    let d = h.finalize();
    let mut out = [0u8; 32];
    out.copy_from_slice(&d);
    out
}

// ---------------------------------------------------------------------
// Client.
// ---------------------------------------------------------------------

/// Retries of a read-side control verb (GET range in [`S3RdmaClient::get`],
/// HEAD, DELETE, RAILS) the server answered busy (NO_SLOT) before the
/// caller sees the error; with [`BUSY_BACKOFF`] at most about 1.3 s of
/// pauses (`object::backoff`, jittered).
pub const BUSY_RETRIES: u32 = 6;
/// First pause after a busy answer; doubles per retry.
pub const BUSY_BACKOFF: std::time::Duration = std::time::Duration::from_millis(20);
/// PUT: busy answers (to ALLOC or PERSIST) are retried, from ALLOC, at
/// most this many times...
pub const PUT_BUSY_RETRIES: u32 = 2;
/// ...and only while less than this has passed since the PUT started.
/// Each busy answer costs the server up to `ADMIT_WAIT` + `SLOT_WAIT`
/// (3 s) of a permit, so this keeps one PUT from holding server permits
/// for long: its worst case is this budget plus one busy answer, about
/// 4 s, before it reports busy. REPLICATE never retries: the placement
/// layer falls back to the TCP RPC for that write instead.
pub const PUT_BUSY_BUDGET: std::time::Duration = std::time::Duration::from_secs(1);

/// Why a PUT or REPLICATE failed.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PushError {
    /// The server answered NO_SLOT (no free slot or worker): it is
    /// healthy but busy; the connection is fine.
    Busy(String),
    /// Anything else.
    Failed(String),
}

impl std::fmt::Display for PushError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            PushError::Busy(m) | PushError::Failed(m) => f.write_str(m),
        }
    }
}

impl From<String> for PushError {
    fn from(m: String) -> Self {
        PushError::Failed(m)
    }
}

impl From<PushError> for String {
    fn from(e: PushError) -> String {
        e.to_string()
    }
}

/// One request/response exchange on a connection: the transport's in
/// production, a script in tests. The verbs below are written against it.
pub(crate) trait Control {
    /// Send `req` and return the server's reply to it.
    fn call(&self, req: &Request) -> Result<Response, String>;
    /// A fresh request id.
    fn next_req_id(&self) -> u64;
}

pub struct S3RdmaClient {
    transport: Arc<RdmaTransport>,
    peer_id: u64,
    next_req_id: AtomicU64,
}

impl Control for S3RdmaClient {
    fn call(&self, req: &Request) -> Result<Response, String> {
        self.transport.request_response(self.peer_id, req)
    }
    fn next_req_id(&self) -> u64 {
        self.next_req_id.fetch_add(1, Ordering::Relaxed)
    }
}

impl S3RdmaClient {
    pub fn new(transport: Arc<RdmaTransport>, peer_id: u64) -> Self {
        Self {
            transport,
            peer_id,
            next_req_id: AtomicU64::new(1),
        }
    }

    /// The local slot a PUT / REPLICATE is pulled from, filled with
    /// `data`: (slot, CRC-32C of `data`).
    fn stage(&self, data: &[u8], verb: &str) -> Result<(PoolBuffer, u32), PushError> {
        let pool = self.transport.pool();
        if !pool.access().remote_read() {
            return Err(PushError::Failed(format!("{verb}: the local pool is not registered for remote read \
                (MemoryPool::with_access(.., Access::REMOTE_READ)), so the server cannot pull from it")));
        }
        if data.len() > pool.slot_size() {
            return Err(PushError::Failed(format!(
                "{verb}: data {} > local slot {}",
                data.len(),
                pool.slot_size()
            )));
        }
        let mut local = pool
            .checkout_timeout(std::time::Duration::from_secs(5))
            .ok_or_else(|| format!("{verb}: no local pool slot freed within 5 s"))?;
        local.as_mut_slice()[..data.len()].copy_from_slice(data);
        Ok((local, nvstor_core::checksum::crc32c(data)))
    }

    /// PUT: the bytes go into a local pool slot, ALLOC reserves a server
    /// slot, and PERSIST has the server RDMA-READ them and check their
    /// CRC-32C. Returns the server-reported written length. The local
    /// pool must be registered with remote read access. A busy server is
    /// asked again, from ALLOC, within [`PUT_BUSY_RETRIES`] and
    /// [`PUT_BUSY_BUDGET`].
    pub fn put(&self, key: &str, data: &[u8]) -> Result<u64, String> {
        let (local, crc) = self.stage(data, "PERSIST")?;
        // `local` stays alive until the last attempt's response: the
        // server reads it.
        let r = put_on(
            self,
            key,
            data.len() as u64,
            local.addr(),
            local.rkey(),
            crc,
        );
        drop(local);
        r.map_err(String::from)
    }

    /// REPLICATE: `put` into the server's replica store, once. The server
    /// accepts it only if this transport authenticated as a cluster peer.
    /// A busy replica is [`PushError::Busy`], not retried: the connection
    /// is fine and the caller sends this write another way.
    pub fn replicate(&self, key: &str, data: &[u8]) -> Result<u64, PushError> {
        let (local, crc) = self.stage(data, "REPLICATE")?;
        let r = replicate_on(
            self,
            key,
            data.len() as u64,
            local.addr(),
            local.rkey(),
            crc,
        );
        drop(local);
        r
    }

    /// One ranged GET: the server RDMA-WRITEs up to `len` bytes (capped
    /// at `dest.len()` and the server's slot size) of `key` from `offset`
    /// into the start of `dest`. Verifies the CRC-32C of the bytes that
    /// landed. Pass the generation from the first range as
    /// `if_generation` for every later range of the same object (or
    /// `ANY_GENERATION` for the first). A busy server is `GetError::Busy`.
    pub fn get_range_into(
        &self,
        key: &str,
        offset: u64,
        if_generation: u128,
        dest: &mut PoolBuffer,
        len: usize,
    ) -> Result<ObjectRead, GetError> {
        let len = len.min(dest.len());
        let (addr, rkey) = (dest.addr(), dest.rkey());
        range_on(
            self,
            key,
            offset,
            if_generation,
            addr,
            rkey,
            len,
            |n, crc| crate::object::verify_range(&dest.as_slice()[..n], crc),
        )
    }

    /// One ranged GET into `dest`, memory the CPU may not read (GPU
    /// memory): the server RDMA-WRITEs up to `len` bytes (capped at the
    /// slice and the server's slot size) of `key` from `offset` through
    /// the NIC at position `rail`, which must be this client's. The
    /// generation is checked; the bytes are returned unchecked, with the
    /// server's CRC-32C ([`crate::region::Landed`]).
    pub fn get_range_to(
        &self,
        key: &str,
        offset: u64,
        if_generation: u128,
        dest: &crate::region::RegionSlice<'_>,
        rail: usize,
        len: usize,
    ) -> Result<crate::region::Landed, GetError> {
        let (addr, rkey) = dest.target(rail).ok_or_else(|| {
            GetError::Transport(format!("the region is not registered on rail {rail}"))
        })?;
        let mut crc = 0;
        let read = range_on(
            self,
            key,
            offset,
            if_generation,
            addr,
            rkey,
            len.min(dest.len()),
            |_, c| {
                crc = c;
                Ok(())
            },
        )?;
        Ok(crate::region::Landed { read, crc32c: crc })
    }

    /// Whole object into memory, range by range through one local pool
    /// slot. Every range is CRC-checked and pinned to the first range's
    /// generation; a busy range is asked again ([`BUSY_RETRIES`]). For
    /// objects that don't fit in RAM, write ranges to a file instead.
    pub fn get(&self, key: &str) -> Result<Vec<u8>, String> {
        let pool = self.transport.pool();
        let mut dest = pool
            .checkout_timeout(std::time::Duration::from_secs(5))
            .ok_or_else(|| "GET: no local pool slot freed within 5 s".to_string())?;
        let slot = dest.len();
        get_whole(|offset, generation, out| {
            let r = self.get_range_into(key, offset, generation, &mut dest, slot)?;
            out.extend_from_slice(&dest.as_slice()[..r.len]);
            Ok(r)
        })
    }

    /// Object size and generation without moving data. A busy server is
    /// `GetError::Busy`; callers retry it.
    pub fn stat(&self, key: &str) -> Result<(u64, u128), GetError> {
        stat_on(self, key)
    }

    /// The server's rails (one per NIC, rail 0 first). Empty when the
    /// server runs a single transport. `key` is the object about to be
    /// fetched; the request is authorized as a read of it. A busy server
    /// is `GetError::Busy`; callers retry it.
    pub fn rails(&self, key: &str) -> Result<Vec<crate::rail::RailInfo>, GetError> {
        rails_on(self, key)
    }

    /// [`Self::rails`] and the server process's instance id (`None` from
    /// a server that does not send one).
    pub fn rails_and_instance(
        &self,
        key: &str,
    ) -> Result<(Vec<crate::rail::RailInfo>, Option<[u8; 16]>), GetError> {
        rails_and_instance_on(self, key)
    }

    /// Object size; a busy server is asked again ([`BUSY_RETRIES`]).
    pub fn head(&self, key: &str) -> Result<u64, String> {
        head_on(self, key)
    }

    /// Delete `key`; a busy server is asked again ([`BUSY_RETRIES`]).
    pub fn delete(&self, key: &str) -> Result<(), String> {
        delete_on(self, key)
    }
}

/// The reply to a request with id `req_id` that the server answered
/// NO_SLOT (busy).
fn is_busy(resp: &Response, req_id: u64) -> bool {
    matches!(resp, Response::Err { req_id: r, status: STATUS_NO_SLOT } if *r == req_id)
}

/// PUT under the busy policy: [`push_once_on`] again, from ALLOC, while
/// the server answers busy, at most [`PUT_BUSY_RETRIES`] times and only
/// within [`PUT_BUSY_BUDGET`] of the start.
fn put_on<C: Control + ?Sized>(
    c: &C,
    key: &str,
    len: u64,
    addr: u64,
    rkey: u32,
    crc32c: u32,
) -> Result<u64, PushError> {
    let started = std::time::Instant::now();
    let mut attempt = 0;
    loop {
        match push_once_on(c, key, len, addr, rkey, crc32c, false) {
            Err(PushError::Busy(m)) => {
                if attempt >= PUT_BUSY_RETRIES || started.elapsed() >= PUT_BUSY_BUDGET {
                    return Err(PushError::Busy(m));
                }
                attempt += 1;
                std::thread::sleep(crate::object::backoff(BUSY_BACKOFF, attempt));
            }
            r => return r,
        }
    }
}

/// REPLICATE, once: a busy replica is [`PushError::Busy`] at once.
fn replicate_on<C: Control + ?Sized>(
    c: &C,
    key: &str,
    len: u64,
    addr: u64,
    rkey: u32,
    crc32c: u32,
) -> Result<u64, PushError> {
    push_once_on(c, key, len, addr, rkey, crc32c, true)
}

/// One ALLOC + PERSIST (or REPLICATE) of `len` bytes the server pulls
/// from (`addr`, `rkey`).
fn push_once_on<C: Control + ?Sized>(
    c: &C,
    key: &str,
    len: u64,
    addr: u64,
    rkey: u32,
    crc32c: u32,
    replica: bool,
) -> Result<u64, PushError> {
    let verb = if replica { "REPLICATE" } else { "PERSIST" };
    let req_id = c.next_req_id();
    let resp = c.call(&Request::Alloc { req_id, size: len })?;
    if is_busy(&resp, req_id) {
        return Err(PushError::Busy("ALLOC: server busy (NO_SLOT)".into()));
    }
    let (slot, slot_len) = match resp {
        Response::Slot {
            req_id: r,
            slot,
            length,
        } if r == req_id => (slot, length),
        Response::Err { req_id: r, status } if r == req_id => {
            return Err(PushError::Failed(format!("ALLOC failed: status={status}")));
        }
        other => {
            return Err(PushError::Failed(format!(
                "ALLOC: unexpected response {other:?}"
            )))
        }
    };
    if len > slot_len {
        return Err(PushError::Failed(format!(
            "{verb}: data {len} > server slot {slot_len}"
        )));
    }
    let req_id = c.next_req_id();
    let key = key.to_string();
    let req = if replica {
        Request::Replicate {
            req_id,
            key,
            slot,
            client_addr: addr,
            client_rkey: rkey,
            length: len,
            crc32c,
        }
    } else {
        Request::Persist {
            req_id,
            key,
            slot,
            client_addr: addr,
            client_rkey: rkey,
            length: len,
            crc32c,
        }
    };
    let resp = c.call(&req)?;
    if is_busy(&resp, req_id) {
        return Err(PushError::Busy(format!("{verb}: server busy (NO_SLOT)")));
    }
    match resp {
        Response::Ok {
            req_id: r, length, ..
        } if r == req_id => Ok(length),
        Response::Err { req_id: r, status } if r == req_id => {
            Err(PushError::Failed(format!("{verb} failed: status={status}")))
        }
        other => Err(PushError::Failed(format!(
            "{verb}: unexpected response {other:?}"
        ))),
    }
}

/// One ranged GET into (`addr`, `rkey`), at most `len` bytes; `verify`
/// checks the first n bytes that landed against the server's CRC-32C.
#[allow(clippy::too_many_arguments)]
fn range_on<C: Control + ?Sized>(
    c: &C,
    key: &str,
    offset: u64,
    if_generation: u128,
    addr: u64,
    rkey: u32,
    len: usize,
    verify: impl FnOnce(usize, u32) -> Result<(), GetError>,
) -> Result<ObjectRead, GetError> {
    let req_id = c.next_req_id();
    let resp = c
        .call(&Request::Get {
            req_id,
            key: key.to_string(),
            offset,
            if_generation,
            client_addr: addr,
            client_rkey: rkey,
            client_len: len as u64,
        })
        .map_err(GetError::Transport)?;
    match resp {
        Response::Range {
            req_id: r,
            length,
            object_size,
            generation,
            crc32c,
        } if r == req_id => {
            let n = usize::try_from(length)
                .ok()
                .filter(|&n| n <= len)
                .ok_or_else(|| {
                    GetError::Transport(format!("server wrote {length} > requested {len}"))
                })?;
            if if_generation != ANY_GENERATION && generation != if_generation {
                return Err(GetError::GenerationChanged);
            }
            verify(n, crc32c)?;
            Ok(ObjectRead {
                len: n,
                size: object_size,
                generation,
            })
        }
        Response::Err { req_id: r, status } if r == req_id => Err(match status {
            STATUS_NOT_FOUND => GetError::NotFound,
            STATUS_GENERATION_CHANGED => GetError::GenerationChanged,
            STATUS_NO_SLOT => GetError::Busy,
            other => GetError::Server(other),
        }),
        other => Err(GetError::Transport(format!(
            "unexpected response {other:?}"
        ))),
    }
}

/// A whole object from `range(offset, generation, out)`, which fetches
/// one range and appends it to `out`. A busy range is asked again.
fn get_whole(
    mut range: impl FnMut(u64, u128, &mut Vec<u8>) -> Result<ObjectRead, GetError>,
) -> Result<Vec<u8>, String> {
    let mut first_bytes = Vec::new();
    let first = crate::object::retry_busy(BUSY_RETRIES, BUSY_BACKOFF, || {
        first_bytes.clear();
        range(0, ANY_GENERATION, &mut first_bytes)
    })?;
    let size = usize::try_from(first.size)
        .map_err(|_| format!("GET: object size {} exceeds address space", first.size))?;
    let mut out = Vec::new();
    out.try_reserve_exact(size)
        .map_err(|e| format!("GET: cannot buffer {size}-byte object: {e}"))?;
    out.extend_from_slice(&first_bytes);
    while out.len() < size {
        let at = out.len() as u64;
        let r = crate::object::retry_busy(BUSY_RETRIES, BUSY_BACKOFF, || {
            out.truncate(at as usize);
            range(at, first.generation, &mut out)
        })?;
        if r.len == 0 {
            return Err(format!(
                "GET: object ended at {} of {size} bytes",
                out.len()
            ));
        }
    }
    Ok(out)
}

fn stat_on<C: Control + ?Sized>(c: &C, key: &str) -> Result<(u64, u128), GetError> {
    let req_id = c.next_req_id();
    let resp = c
        .call(&Request::Head {
            req_id,
            key: key.to_string(),
        })
        .map_err(GetError::Transport)?;
    match resp {
        Response::Range {
            req_id: r,
            object_size,
            generation,
            ..
        } if r == req_id => Ok((object_size, generation)),
        Response::Err { req_id: r, status } if r == req_id => Err(match status {
            STATUS_NOT_FOUND => GetError::NotFound,
            STATUS_NO_SLOT => GetError::Busy,
            other => GetError::Server(other),
        }),
        other => Err(GetError::Transport(format!(
            "unexpected response {other:?}"
        ))),
    }
}

fn rails_on<C: Control + ?Sized>(c: &C, key: &str) -> Result<Vec<crate::rail::RailInfo>, GetError> {
    rails_and_instance_on(c, key).map(|(rails, _)| rails)
}

fn rails_and_instance_on<C: Control + ?Sized>(
    c: &C,
    key: &str,
) -> Result<(Vec<crate::rail::RailInfo>, Option<[u8; 16]>), GetError> {
    let req_id = c.next_req_id();
    let resp = c
        .call(&Request::Rails {
            req_id,
            key: key.to_string(),
        })
        .map_err(GetError::Transport)?;
    match resp {
        Response::Rails {
            req_id: r,
            rails,
            instance,
        } if r == req_id => Ok((rails, instance)),
        Response::Err {
            req_id: r,
            status: STATUS_NO_SLOT,
        } if r == req_id => Err(GetError::Busy),
        Response::Err { req_id: r, status } if r == req_id => Err(GetError::Server(status)),
        other => Err(GetError::Transport(format!(
            "unexpected response {other:?}"
        ))),
    }
}

fn head_on<C: Control + ?Sized>(c: &C, key: &str) -> Result<u64, String> {
    Ok(crate::object::retry_busy(BUSY_RETRIES, BUSY_BACKOFF, || stat_on(c, key))?.0)
}

fn delete_on<C: Control + ?Sized>(c: &C, key: &str) -> Result<(), String> {
    let mut failed = None;
    let r = crate::object::retry_busy(BUSY_RETRIES, BUSY_BACKOFF, || {
        let req_id = c.next_req_id();
        let resp = c
            .call(&Request::Delete {
                req_id,
                key: key.to_string(),
            })
            .map_err(GetError::Transport)?;
        if is_busy(&resp, req_id) {
            return Err(GetError::Busy);
        }
        match resp {
            Response::Ok { req_id: r, .. } if r == req_id => Ok(()),
            Response::Err { req_id: r, status } if r == req_id => {
                failed = Some(format!("DELETE failed: status={status}"));
                Ok(())
            }
            other => {
                failed = Some(format!("DELETE: unexpected response {other:?}"));
                Ok(())
            }
        }
    });
    match (r, failed) {
        (Err(GetError::Transport(e)), _) => Err(e),
        (Err(e), _) => Err(format!("DELETE: {e}")),
        (Ok(()), Some(f)) => Err(f),
        (Ok(()), None) => Ok(()),
    }
}

/// The bootstrap addresses of every rail the server at `addr` serves on,
/// asked over `c` (a busy RAILS is asked again, [`BUSY_RETRIES`]).
#[cfg(test)]
fn rail_addrs_on<C: Control + ?Sized>(
    c: &C,
    addr: &str,
    key: &str,
) -> Result<Vec<String>, crate::hydrate::HydrateError> {
    let advertised = crate::object::retry_busy(BUSY_RETRIES, BUSY_BACKOFF, || rails_on(c, key))
        .map_err(|error| crate::hydrate::HydrateError::Get { offset: 0, error })?;
    if advertised.is_empty() {
        return Ok(vec![addr.to_string()]);
    }
    let host = addr.rsplit_once(':').map(|(h, _)| h).ok_or_else(|| {
        crate::hydrate::HydrateError::Invalid(format!("{addr}: expected host:port"))
    })?;
    Ok(advertised
        .iter()
        .map(|r| format!("{host}:{}", r.port))
        .collect())
}

// ---------------------------------------------------------------------
// Hydration source.
// ---------------------------------------------------------------------

/// S3-over-RDMA [`RangeSource`](crate::hydrate::RangeSource): one peer
/// connection (its own QPs) per worker, on the local NIC that best
/// reaches the server rail the worker was assigned (`crate::rail`), plus
/// one registered slot from that NIC's pool. The server RDMA-WRITEs each
/// range into the slot and the hydrator pwrites straight from it — no
/// copy on the client. A transport failure tears the peer down and
/// connects anew, possibly on a different local NIC.
pub struct RdmaSource {
    rails: Arc<crate::rail::Rails>,
    addr: String,
    server_rail: usize,
    /// GIDs of all the server's rails, for pairing by plane.
    server_rails: Vec<[u8; 16]>,
    client: S3RdmaClient,
    slot: PoolBuffer,
    conn: crate::rail::RailPeer,
}

fn checkout_slot(conn: &crate::rail::RailPeer) -> Result<PoolBuffer, crate::hydrate::HydrateError> {
    conn.transport().pool().checkout().ok_or_else(|| {
        crate::hydrate::HydrateError::Invalid(format!(
            "local RDMA pool on {} has fewer free slots than connections on it",
            conn.transport().device().name
        ))
    })
}

impl RdmaSource {
    /// Connect to the server rail at `addr`, which is position
    /// `server_rail` in the server's advertised rail list.
    pub fn open(
        rails: &Arc<crate::rail::Rails>,
        addr: &str,
        server_rail: usize,
    ) -> Result<Self, crate::hydrate::HydrateError> {
        Self::open_rail(rails, addr, server_rail, &[])
    }

    /// [`Self::open`], knowing the GIDs of all the server's rails (its
    /// RAILS reply), so the local NIC is chosen by plane when routing
    /// does not tell (`crate::rail::plane_len`).
    pub fn open_rail(
        rails: &Arc<crate::rail::Rails>,
        addr: &str,
        server_rail: usize,
        server_rails: &[[u8; 16]],
    ) -> Result<Self, crate::hydrate::HydrateError> {
        let (conn, _) = connect_listing_rails(rails, addr, server_rail, server_rails, "")
            .map_err(|e| crate::hydrate::HydrateError::Connect(format!("{addr}: {e}")))?;
        let slot = checkout_slot(&conn)?;
        let client = S3RdmaClient::new(Arc::clone(conn.transport()), conn.peer_id());
        Ok(Self {
            rails: Arc::clone(rails),
            addr: addr.to_string(),
            server_rail,
            server_rails: server_rails.to_vec(),
            client,
            slot,
            conn,
        })
    }

    /// The local NIC (position in `Rails`) this source is connected on.
    pub fn local_rail(&self) -> usize {
        self.conn.rail()
    }

    /// [`crate::hydrate::RangeSource::fetch`] into `dest` instead of this
    /// source's pool slot: up to `len` bytes, landed unchecked
    /// ([`crate::region`]). `dest` must be a slice of a region registered
    /// on this source's rails. A reconnect keeps working: the region is
    /// registered on every NIC.
    pub fn fetch_to(
        &mut self,
        key: &str,
        offset: u64,
        generation: u128,
        len: usize,
        dest: &crate::region::RegionSlice<'_>,
    ) -> Result<crate::region::Landed, GetError> {
        if !dest.region().is_on(&self.rails) {
            return Err(GetError::Transport(
                "the region is registered on other rails than this source's".into(),
            ));
        }
        self.client.get_range_to(
            key,
            offset,
            generation,
            dest,
            self.conn.rail(),
            len.min(self.slot.len()),
        )
    }
}

impl crate::hydrate::RangeSource for RdmaSource {
    fn max_range(&self) -> usize {
        self.slot.len()
    }

    fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError> {
        self.client.stat(key)
    }

    fn fetch(
        &mut self,
        key: &str,
        offset: u64,
        generation: u128,
        len: usize,
    ) -> Result<&[u8], GetError> {
        let r = self
            .client
            .get_range_into(key, offset, generation, &mut self.slot, len)?;
        Ok(&self.slot.as_slice()[..r.len])
    }

    fn reconnect(&mut self) -> Result<(), crate::hydrate::HydrateError> {
        let (conn, _) = connect_listing_rails(
            &self.rails,
            &self.addr,
            self.server_rail,
            &self.server_rails,
            "",
        )
        .map_err(|e| crate::hydrate::HydrateError::Connect(format!("{}: {e}", self.addr)))?;
        if conn.rail() != self.conn.rail() {
            // The slot is registered on the old NIC; the server must
            // WRITE into memory the new NIC's QPs can address.
            self.slot = checkout_slot(&conn)?;
        }
        self.client = S3RdmaClient::new(Arc::clone(conn.transport()), conn.peer_id());
        // The old connection drops here, disconnecting its peer.
        self.conn = conn;
        Ok(())
    }
}

/// A server's answer to RAILS: its rails and, from servers that send it,
/// its process instance id.
pub type RailsAnswer = Result<(Vec<crate::rail::RailInfo>, Option<[u8; 16]>), GetError>;

/// Connect to the server rail at `addr` (position `server_rail`; the
/// server's rail GIDs when known) and ask it for its rails over the new
/// connection. The request is also the check that the path works: when it
/// never completes (the local NIC is on another plane), the connection is
/// moved to the next local NIC (`crate::rail::Pairing`). Returns the
/// connection and the server's answer, its rails and process instance id
/// (a refusal of the request, which still proves the path, is returned as
/// the answer).
pub fn connect_listing_rails(
    rails: &Arc<crate::rail::Rails>,
    addr: &str,
    server_rail: usize,
    server_rails: &[[u8; 16]],
    key: &str,
) -> Result<(crate::rail::RailPeer, RailsAnswer), String> {
    let mut answer = None;
    let conn =
        rails.connect_rail_probed(addr, server_rail, server_rails, &mut |transport, peer| {
            match S3RdmaClient::new(Arc::clone(transport), peer).rails_and_instance(key) {
                Err(GetError::Transport(e)) => Err(e),
                r => {
                    answer = Some(r);
                    Ok(())
                }
            }
        })?;
    let answer = answer.ok_or_else(|| "connected without an answer to RAILS".to_string())?;
    Ok((conn, answer))
}

/// Bootstrap addresses of every rail the server at `addr` serves on,
/// rail 0 (`addr` itself) first. Asked over one short-lived connection.
pub fn server_rail_addrs(
    rails: &Arc<crate::rail::Rails>,
    addr: &str,
    key: &str,
) -> Result<Vec<String>, crate::hydrate::HydrateError> {
    server_rails(rails, addr, key).map(|(addrs, _)| addrs)
}

/// [`server_rail_addrs`], with each rail's GID.
fn server_rails(
    rails: &Arc<crate::rail::Rails>,
    addr: &str,
    key: &str,
) -> Result<(Vec<String>, Vec<[u8; 16]>), crate::hydrate::HydrateError> {
    let (conn, advertised) = connect_listing_rails(rails, addr, 0, &[], key)
        .map_err(|e| crate::hydrate::HydrateError::Connect(format!("{addr}: {e}")))?;
    // The probe's answer, asked again on the same connection while the
    // server is busy (busy is load, not a broken path).
    let advertised = match advertised {
        Err(GetError::Busy) => {
            let client = S3RdmaClient::new(Arc::clone(conn.transport()), conn.peer_id());
            crate::object::retry_busy(BUSY_RETRIES, BUSY_BACKOFF, || rails_on(&client, key))
        }
        other => other.map(|(rails, _)| rails),
    };
    let advertised =
        advertised.map_err(|error| crate::hydrate::HydrateError::Get { offset: 0, error })?;
    if advertised.is_empty() {
        return Ok((vec![addr.to_string()], Vec::new()));
    }
    let host = addr.rsplit_once(':').map(|(h, _)| h).ok_or_else(|| {
        crate::hydrate::HydrateError::Invalid(format!("{addr}: expected host:port"))
    })?;
    Ok((
        advertised
            .iter()
            .map(|r| format!("{host}:{}", r.port))
            .collect(),
        advertised.iter().map(|r| r.gid).collect(),
    ))
}

/// Fetch `key` from the S3-over-RDMA server at `addr` into `dest` over
/// `opts.connections` peer connections spread round-robin across the
/// server's rails, each on the local NIC that reaches its rail (see
/// `crate::rail`). Each local NIC's pool needs a free slot per
/// connection placed on it; ranges are capped at the slot size. See
/// [`crate::hydrate`] for the integrity and durability contract.
pub fn hydrate_to_file(
    rails: &Arc<crate::rail::Rails>,
    addr: &str,
    key: &str,
    dest: &std::path::Path,
    opts: &crate::hydrate::HydrateOptions,
) -> Result<crate::hydrate::HydrateReport, crate::hydrate::HydrateError> {
    let (server, gids) = server_rails(rails, addr, key)?;
    let next = std::sync::atomic::AtomicUsize::new(0);
    crate::hydrate::hydrate_to_file(
        || {
            let k = next.fetch_add(1, std::sync::atomic::Ordering::Relaxed) % server.len();
            RdmaSource::open_rail(rails, &server[k], k, &gids)
        },
        key,
        dest,
        opts,
    )
}

#[cfg(test)]
mod tests {
    //! Run the ignored hardware tests on a provisioned RDMA host.
    //! Missing hardware or registration failures must fail that explicit run.

    use super::*;
    use crate::device::RdmaDevice;
    use crate::pool::MemoryPool;
    use std::time::Duration;

    fn pull(replica: bool) -> Request {
        let (req_id, key, slot, client_addr, client_rkey, length, crc32c) =
            (1, "k".to_string(), 1, 0, 0, 0, 0);
        if replica {
            Request::Replicate {
                req_id,
                key,
                slot,
                client_addr,
                client_rkey,
                length,
                crc32c,
            }
        } else {
            Request::Persist {
                req_id,
                key,
                slot,
                client_addr,
                client_rkey,
                length,
                crc32c,
            }
        }
    }

    // A read-only server refuses every verb that reserves or changes
    // state, before admission, so ALLOC can never park its pool slots.
    #[test]
    fn read_only_server_refuses_state_changing_verbs() {
        let client = Some(PeerRole::Client);
        for req in [
            Request::Alloc { req_id: 1, size: 1 },
            pull(false),
            Request::Delete {
                req_id: 1,
                key: "k".into(),
            },
        ] {
            assert_eq!(
                verb_allowed(&req, true, false, client),
                Err(STATUS_UNSUPPORTED),
                "{req:?}"
            );
            assert_eq!(verb_allowed(&req, false, false, client), Ok(()), "{req:?}");
        }
        let get = Request::Get {
            req_id: 1,
            key: "k".into(),
            offset: 0,
            if_generation: 0,
            client_addr: 0,
            client_rkey: 0,
            client_len: 0,
        };
        for req in [
            get,
            Request::Head {
                req_id: 1,
                key: "k".into(),
            },
            Request::Rails {
                req_id: 1,
                key: "k".into(),
            },
        ] {
            assert_eq!(verb_allowed(&req, true, false, client), Ok(()), "{req:?}");
        }
    }

    // REPLICATE needs the bootstrap's Cluster role, not just a valid
    // request signature, and a server with a replica store.
    #[test]
    fn replicate_needs_cluster_role_and_a_replica_store() {
        let r = pull(true);
        assert_eq!(
            verb_allowed(&r, false, true, Some(PeerRole::Cluster)),
            Ok(())
        );
        assert_eq!(
            verb_allowed(&r, false, true, Some(PeerRole::Client)),
            Err(STATUS_UNAUTHORIZED)
        );
        assert_eq!(
            verb_allowed(&r, false, true, None),
            Err(STATUS_UNAUTHORIZED)
        );
        assert_eq!(
            verb_allowed(&r, false, false, Some(PeerRole::Cluster)),
            Err(STATUS_UNSUPPORTED)
        );
        assert_eq!(
            verb_allowed(&r, true, true, Some(PeerRole::Cluster)),
            Err(STATUS_UNSUPPORTED)
        );
        assert_eq!(action_and_key(&r), ("nvstor:ReplicateObject", "k"));
    }

    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn loopback_put_get_delete_head() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let pool = Arc::new(
            MemoryPool::new(&dev, 4 * 1024 * 1024, 16)
                .expect("hardware test requires sufficient registered memory"),
        );

        let (write_fn, read_fn, delete_fn) = crate::object::mem_store();
        // PUT is one slot; seed multi-slot objects straight into the store.
        let seed = Arc::clone(&write_fn);

        let transport = Arc::new(RdmaTransport::new(dev, pool));
        transport.set_credentials(crate::auth::Credentials::new("RDMA-TEST", "test-secret"));
        let auth: crate::auth::AuthFn = Arc::new(|ctx| {
            if ctx.header.access_key != "RDMA-TEST" {
                return Err(crate::auth::AuthError::UnknownKey);
            }
            if !crate::auth::verify_signature(
                "test-secret",
                ctx.header.timestamp,
                ctx.header.nonce,
                ctx.body,
                &ctx.header.signature,
            ) {
                return Err(crate::auth::AuthError::BadSignature);
            }
            Ok("rdma-test-principal".into())
        });
        let server =
            S3RdmaServer::new(write_fn, read_fn, delete_fn, Arc::clone(&transport)).with_auth(auth);
        server.start();

        let listen_addr = transport.listen(0).expect("listen");
        thread::sleep(Duration::from_millis(50));
        let peer_id = transport
            .connect(&listen_addr.to_string())
            .expect("connect");
        thread::sleep(Duration::from_millis(100));
        let client = S3RdmaClient::new(Arc::clone(&transport), peer_id);

        let data = vec![0xAB; 256 * 1024];
        let n = client.put("ckpt/rank-0/step-100", &data).expect("put");
        assert_eq!(n, data.len() as u64);
        // No verifier: the peer is a Client, so REPLICATE is refused.
        let refused = client
            .replicate("ckpt/replica", &data)
            .expect_err("not a cluster peer");
        assert!(
            refused
                .to_string()
                .contains(&format!("status={STATUS_UNAUTHORIZED}")),
            "{refused}"
        );

        assert_eq!(
            client.head("ckpt/rank-0/step-100").expect("head"),
            data.len() as u64
        );

        let got = client.get("ckpt/rank-0/step-100").expect("get");
        assert_eq!(got, data);

        // Larger than one 4 MiB slot: walks several ranges, each
        // CRC-checked and pinned to one generation.
        let big: Vec<u8> = (0..10 * 1024 * 1024u32).map(|i| (i * 7) as u8).collect();
        seed("big", &big).expect("seed");
        assert_eq!(client.get("big").expect("multi-range get"), big);

        // A stale generation is rejected before any bytes move.
        let (_, generation) = client.stat("ckpt/rank-0/step-100").expect("stat");
        let mut dest = transport.pool().checkout().expect("slot");
        assert_eq!(
            client.get_range_into("ckpt/rank-0/step-100", 0, generation + 1, &mut dest, 1024),
            Err(GetError::GenerationChanged)
        );
        drop(dest);

        // Hydrate the multi-slot object to a file over 2 peer
        // connections, ranges written straight from the RDMA slots.
        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("big.bin");
        let rails = crate::rail::Rails::from_transports(vec![Arc::clone(&transport)]);
        let report = hydrate_to_file(
            &rails,
            &listen_addr.to_string(),
            "big",
            &dest,
            &crate::hydrate::HydrateOptions {
                connections: 2,
                ..Default::default()
            },
        )
        .expect("rdma hydrate");
        assert_eq!(report.bytes, big.len() as u64);
        assert_eq!(std::fs::read(&dest).expect("read"), big);

        client.delete("ckpt/rank-0/step-100").expect("delete");
        assert!(client.get("ckpt/rank-0/step-100").is_err());

        transport.shutdown();
    }

    /// Two server rails and two client rails. On a one-NIC host both
    /// rails of each side share the device (separate transports, PDs'
    /// pools and ports), which exercises the RAILS discovery and the
    /// per-rail connection spread end to end. Every server rail must
    /// serve ranges, and the file must be byte-identical.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn multi_rail_hydrate_spreads_connections() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        let open = || {
            let dev = Arc::new(
                RdmaDevice::open_first_active()
                    .expect("hardware test requires an active RDMA device"),
            );
            let pool = Arc::new(MemoryPool::new(&dev, 1024 * 1024, 8).expect("registered memory"));
            Arc::new(RdmaTransport::new(dev, pool))
        };
        let auth: crate::auth::AuthFn = Arc::new(|ctx| {
            if crate::auth::verify_signature(
                "rail-secret",
                ctx.header.timestamp,
                ctx.header.nonce,
                ctx.body,
                &ctx.header.signature,
            ) {
                Ok("rail-test".into())
            } else {
                Err(crate::auth::AuthError::BadSignature)
            }
        });
        let (write_fn, read_fn, delete_fn) = crate::object::mem_store();
        let data: Vec<u8> = (0..12 * 1024 * 1024u32)
            .map(|i| (i.wrapping_mul(31) >> 3) as u8)
            .collect();
        write_fn("model/shard", &data).expect("seed");

        let server: Vec<Arc<RdmaTransport>> = vec![open(), open()];
        let ports: Vec<u16> = server
            .iter()
            .map(|t| t.listen(0).expect("listen").port())
            .collect();
        let info: Vec<crate::rail::RailInfo> = server
            .iter()
            .zip(&ports)
            .map(|(t, &port)| crate::rail::RailInfo {
                port,
                gid: t.local_gid(),
            })
            .collect();
        let admission = crate::admission::Admission::new(64);
        let served: Vec<Arc<AtomicUsize>> = (0..2).map(|_| Arc::new(AtomicUsize::new(0))).collect();
        // On fast hardware the first workers can fetch all 12 ranges before
        // the workers bound for the other rail have connected. Hold each
        // rail's reads until both rails have started one (at most 5 s), so
        // the test checks that hydrate puts every rail to work, not which
        // workers happen to connect first.
        let started = Arc::new([AtomicUsize::new(0), AtomicUsize::new(0)]);
        for (rail, (t, count)) in server.iter().zip(&served).enumerate() {
            let inner = Arc::clone(&read_fn);
            let count = Arc::clone(count);
            let started = Arc::clone(&started);
            let counting: ReadFn = Arc::new(move |k, off, buf| {
                if !buf.is_empty() {
                    count.fetch_add(1, Ordering::Relaxed);
                    started[rail].store(1, Ordering::Relaxed);
                    let deadline = std::time::Instant::now() + Duration::from_secs(5);
                    while started.iter().any(|s| s.load(Ordering::Relaxed) == 0)
                        && std::time::Instant::now() < deadline
                    {
                        thread::sleep(Duration::from_millis(1));
                    }
                }
                inner(k, off, buf)
            });
            S3RdmaServer::with_admission(
                Arc::clone(&write_fn),
                counting,
                Arc::clone(&delete_fn),
                Arc::clone(t),
                Arc::clone(&admission),
            )
            .with_auth(Arc::clone(&auth))
            .with_rails(info.clone())
            .start();
        }
        thread::sleep(Duration::from_millis(50));

        let client: Vec<Arc<RdmaTransport>> = vec![open(), open()];
        for t in &client {
            t.set_credentials(crate::auth::Credentials::new("RAIL", "rail-secret"));
        }
        let rails = crate::rail::Rails::from_transports(client);
        let addr = format!("127.0.0.1:{}", ports[0]);
        assert_eq!(
            server_rail_addrs(&rails, &addr, "model/shard").expect("rails"),
            vec![addr.clone(), format!("127.0.0.1:{}", ports[1])]
        );

        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("shard");
        let report = hydrate_to_file(
            &rails,
            &addr,
            "model/shard",
            &dest,
            &crate::hydrate::HydrateOptions {
                connections: 4,
                range_size: 1024 * 1024,
                ..Default::default()
            },
        )
        .expect("multi-rail hydrate");
        assert_eq!(report.bytes, data.len() as u64);
        assert_eq!(std::fs::read(&dest).expect("read"), data);
        for (k, count) in served.iter().enumerate() {
            assert!(
                count.load(Ordering::Relaxed) > 0,
                "server rail {k} served no ranges"
            );
        }
        // Two rail-list lookups (the explicit one above and hydrate's
        // own), both to server rail 0 with equal loads, so both on local
        // rail 0; then 4 workers, balanced 2 + 2 by load.
        let opened: Vec<usize> = rails.opened().iter().map(|(_, n)| *n).collect();
        assert_eq!(opened, vec![2 + 2, 2], "{report:?}");
        rails.shutdown();
        for t in &server {
            t.shutdown();
        }
    }

    /// A server slot reused across requests and clients never exposes
    /// what an earlier request left in it: a short GET sends only the
    /// bytes the read placed, a PERSIST stores only the bytes it pulled,
    /// and a read that reports more bytes than its buffer is refused.
    /// The server pool has one slot, filled with a marker no object
    /// contains, so every request reuses that memory.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn reused_slot_never_exposes_earlier_bytes() {
        const STALE: u8 = 0x5A;
        let open = |slots| {
            let dev = Arc::new(
                RdmaDevice::open_first_active()
                    .expect("hardware test requires an active RDMA device"),
            );
            let pool =
                Arc::new(MemoryPool::new(&dev, 1024 * 1024, slots).expect("registered memory"));
            Arc::new(RdmaTransport::new(dev, pool))
        };
        let auth: crate::auth::AuthFn = Arc::new(|ctx| {
            if crate::auth::verify_signature(
                "slot-secret",
                ctx.header.timestamp,
                ctx.header.nonce,
                ctx.body,
                &ctx.header.signature,
            ) {
                Ok("slot-test".into())
            } else {
                Err(crate::auth::AuthError::BadSignature)
            }
        });
        let (write_fn, read_fn, delete_fn) = crate::object::mem_store();
        write_fn("short", &[0x11; 100]).expect("seed");
        // Breaks the ReadFn contract: claims one byte more than its buffer.
        let inner = Arc::clone(&read_fn);
        let lying: ReadFn = Arc::new(move |k, off, buf| {
            let r = inner(k, off, buf)?;
            Ok(if k == "liar" && !buf.is_empty() {
                ObjectRead {
                    len: buf.len() + 1,
                    ..r
                }
            } else {
                r
            })
        });
        write_fn("liar", &[0x33; 64]).expect("seed");

        let server = open(1);
        {
            let mut slot = server.pool().checkout().expect("server slot");
            slot.as_mut_slice().fill(STALE);
        }
        S3RdmaServer::new(Arc::clone(&write_fn), lying, delete_fn, Arc::clone(&server))
            .with_auth(auth)
            .start();
        let addr = server.listen(0).expect("listen");
        thread::sleep(Duration::from_millis(50));

        let client_t = open(2);
        client_t.set_credentials(crate::auth::Credentials::new("SLOT", "slot-secret"));
        let peer = client_t.connect(&addr.to_string()).expect("connect");
        let client = S3RdmaClient::new(Arc::clone(&client_t), peer);

        // Short GET into a cleared 64 KiB destination: 100 bytes arrive,
        // the rest of the destination is untouched.
        let mut dest = client_t.pool().checkout().expect("client slot");
        dest.as_mut_slice().fill(0);
        let r = client
            .get_range_into("short", 0, ANY_GENERATION, &mut dest, 64 * 1024)
            .expect("get");
        assert_eq!(r.len, 100);
        assert!(dest.as_slice()[..100].iter().all(|&b| b == 0x11));
        assert!(
            !dest.as_slice().contains(&STALE),
            "GET exposed an earlier request's bytes"
        );

        // The contract breach is refused, and nothing is written.
        dest.as_mut_slice().fill(0);
        assert!(client
            .get_range_into("liar", 0, ANY_GENERATION, &mut dest, 16)
            .is_err());
        assert!(
            dest.as_slice().iter().all(|&b| b == 0),
            "a refused GET wrote into the client"
        );
        drop(dest);

        // PUT pulls 100 bytes into the reused slot; exactly those are stored.
        client.put("put", &[0x22; 100]).expect("put");
        assert_eq!(client.get("put").expect("get put"), vec![0x22; 100]);

        client_t.shutdown();
        server.shutdown();
    }

    // A request the server is too busy to admit gets
    // NO_SLOT (which clients treat as Busy, retryable) instead of no
    // answer; only PERSIST / REPLICATE name a parked slot to give back.
    #[test]
    fn a_refused_request_gets_no_slot() {
        let get = Request::Get {
            req_id: 7,
            key: "k".into(),
            offset: 0,
            if_generation: ANY_GENERATION,
            client_addr: 0,
            client_rkey: 0,
            client_len: 0,
        };
        assert_eq!(parked_token(&get), None);
        let persist = Request::Persist {
            req_id: 8,
            key: "k".into(),
            slot: 42,
            client_addr: 0,
            client_rkey: 0,
            length: 0,
            crc32c: 0,
        };
        assert_eq!(parked_token(&persist), Some(42));
        // No parked slot under (peer, 42): nothing to give back, still NO_SLOT.
        assert_eq!(
            refuse_busy(0xB05E, 8, Some(42)),
            Response::Err {
                req_id: 8,
                status: STATUS_NO_SLOT
            }
        );
        assert_eq!(
            refuse_busy(0xB05E, 7, None),
            Response::Err {
                req_id: 7,
                status: STATUS_NO_SLOT
            }
        );
    }

    /// A scripted server: `reply` answers each request; every request is
    /// logged.
    type Reply = Box<dyn FnMut(&Request) -> Response + Send>;

    struct Script {
        reply: parking_lot::Mutex<Reply>,
        ids: AtomicU64,
        log: parking_lot::Mutex<Vec<Request>>,
    }

    impl Script {
        fn new(reply: impl FnMut(&Request) -> Response + Send + 'static) -> Self {
            Self {
                reply: parking_lot::Mutex::new(Box::new(reply)),
                ids: AtomicU64::new(1),
                log: parking_lot::Mutex::new(Vec::new()),
            }
        }
        fn verbs(&self) -> Vec<&'static str> {
            self.log
                .lock()
                .iter()
                .map(|r| match r {
                    Request::Alloc { .. } => "ALLOC",
                    Request::Persist { .. } => "PERSIST",
                    Request::Replicate { .. } => "REPLICATE",
                    Request::Get { .. } => "GET",
                    Request::Head { .. } => "HEAD",
                    Request::Delete { .. } => "DELETE",
                    Request::Rails { .. } => "RAILS",
                })
                .collect()
        }
    }

    impl Control for Script {
        fn call(&self, req: &Request) -> Result<Response, String> {
            self.log.lock().push(req.clone());
            Ok((self.reply.lock())(req))
        }
        fn next_req_id(&self) -> u64 {
            self.ids.fetch_add(1, Ordering::Relaxed)
        }
    }

    fn busy(req: &Request) -> Response {
        Response::Err {
            req_id: req_id_of(req),
            status: STATUS_NO_SLOT,
        }
    }

    /// Answers NO_SLOT to the first `n` requests, then `ok`.
    fn busy_then(n: usize, mut ok: impl FnMut(&Request) -> Response + Send + 'static) -> Script {
        let mut left = n;
        Script::new(move |r| {
            if left > 0 {
                left -= 1;
                busy(r)
            } else {
                ok(r)
            }
        })
    }

    fn served(r: &Request) -> Response {
        let req_id = req_id_of(r);
        match r {
            Request::Alloc { .. } => Response::Slot {
                req_id,
                slot: 5,
                length: 1 << 20,
            },
            Request::Persist { length, .. } | Request::Replicate { length, .. } => Response::Ok {
                req_id,
                length: *length,
                etag: [0; 32],
            },
            Request::Head { .. } => Response::Range {
                req_id,
                length: 0,
                object_size: 77,
                generation: 9,
                crc32c: 0,
            },
            Request::Delete { .. } => Response::Ok {
                req_id,
                length: 0,
                etag: [0; 32],
            },
            Request::Rails { .. } => Response::Rails {
                req_id,
                rails: vec![
                    crate::rail::RailInfo {
                        port: 9877,
                        gid: [0; 16],
                    },
                    crate::rail::RailInfo {
                        port: 9893,
                        gid: [0; 16],
                    },
                ],
                instance: None,
            },
            Request::Get { .. } => Response::Err {
                req_id,
                status: STATUS_ERROR,
            },
        }
    }

    // PUT starts over from ALLOC on a busy ALLOC or
    // PERSIST (the refused PERSIST's slot is gone), a bounded number of
    // times.
    #[test]
    fn a_busy_put_starts_over_from_alloc_a_bounded_number_of_times() {
        // ALLOC busy; ALLOC ok, PERSIST busy; ALLOC ok, PERSIST ok.
        let mut n = 0;
        let s = Script::new(move |r| {
            n += 1;
            if n == 1 || n == 3 {
                busy(r)
            } else {
                served(r)
            }
        });
        assert_eq!(put_on(&s, "k", 100, 0x1000, 7, 0), Ok(100));
        assert_eq!(s.verbs(), ["ALLOC", "ALLOC", "PERSIST", "ALLOC", "PERSIST"]);
        // Always busy: 1 + PUT_BUSY_RETRIES attempts, then Busy.
        let s = Script::new(busy);
        assert!(matches!(
            put_on(&s, "k", 100, 0x1000, 7, 0),
            Err(PushError::Busy(_))
        ));
        assert_eq!(s.verbs().len(), 1 + PUT_BUSY_RETRIES as usize);
    }

    // REPLICATE does not retry a busy replica; the
    // placement layer sends that write over the TCP RPC instead.
    #[test]
    fn a_busy_replicate_is_busy_at_once() {
        let s = Script::new(busy);
        assert!(matches!(
            replicate_on(&s, "k", 100, 0x1000, 7, 0),
            Err(PushError::Busy(_))
        ));
        assert_eq!(s.verbs(), ["ALLOC"]);
        let s = Script::new(served);
        assert_eq!(replicate_on(&s, "k", 100, 0x1000, 7, 0), Ok(100));
        assert_eq!(s.verbs(), ["ALLOC", "REPLICATE"]);
    }

    // DELETE, HEAD, RAILS (server_rail_addrs) and the
    // whole-object GET ask a busy server again.
    #[test]
    fn busy_control_verbs_are_asked_again() {
        let s = busy_then(2, served);
        assert_eq!(delete_on(&s, "k"), Ok(()));
        assert_eq!(s.verbs(), ["DELETE"; 3]);
        let s = busy_then(2, served);
        assert_eq!(head_on(&s, "k"), Ok(77));
        assert_eq!(s.verbs(), ["HEAD"; 3]);
        let s = busy_then(2, served);
        assert_eq!(
            rail_addrs_on(&s, "10.0.0.1:9877", "k").expect("rails"),
            ["10.0.0.1:9877", "10.0.0.1:9893"]
        );
        assert_eq!(s.verbs(), ["RAILS"; 3]);
        // Bounded: still busy after BUSY_RETRIES.
        let s = Script::new(busy);
        assert!(delete_on(&s, "k")
            .expect_err("busy")
            .contains("server busy"));
        assert_eq!(s.verbs().len(), 1 + BUSY_RETRIES as usize);
        // Whole-object GET: a busy range is fetched again.
        let data: Vec<u8> = (0..100u8).collect();
        let mut busy_left = 2;
        let got = get_whole(|offset, _, out| {
            if busy_left > 0 {
                busy_left -= 1;
                return Err(GetError::Busy);
            }
            let end = (offset as usize + 40).min(data.len());
            out.extend_from_slice(&data[offset as usize..end]);
            Ok(ObjectRead {
                len: end - offset as usize,
                size: data.len() as u64,
                generation: 3,
            })
        })
        .expect("get");
        assert_eq!(got, data);
    }

    // NO_SLOT counts as busy only as the reply to the
    // request it names; Busy reads the same for every verb.
    #[test]
    fn a_no_slot_for_another_request_is_not_busy() {
        let stale = |r: &Request| Response::Err {
            req_id: req_id_of(r) + 1000,
            status: STATUS_NO_SLOT,
        };
        let s = Script::new(stale);
        assert!(
            matches!(put_on(&s, "k", 1, 0, 0, 0), Err(PushError::Failed(ref m)) if m.contains("unexpected"))
        );
        assert_eq!(s.verbs(), ["ALLOC"], "not retried as busy");
        let s = Script::new(stale);
        assert!(delete_on(&s, "k")
            .expect_err("stale")
            .contains("unexpected"));
        assert_eq!(s.verbs(), ["DELETE"]);
        let s = Script::new(stale);
        assert!(matches!(
            range_on(&s, "k", 0, ANY_GENERATION, 0, 0, 16, |_, _| Ok(())),
            Err(GetError::Transport(_))
        ));
        let s = Script::new(busy);
        assert_eq!(
            range_on(&s, "k", 0, ANY_GENERATION, 0, 0, 16, |_, _| Ok(())),
            Err(GetError::Busy)
        );
        assert_eq!(
            GetError::Busy.to_string(),
            "server busy: no free slot or worker"
        );
    }

    // The dispatcher's admission. A request no worker is
    // free for within the bound is answered NO_SLOT and never run; with a
    // worker free it runs.
    #[test]
    fn a_saturated_server_answers_no_slot_within_the_bound() {
        let adm = crate::admission::Admission::new(1);
        let held = adm.acquire().expect("permit");
        let req = Request::Head {
            req_id: 31,
            key: "k".into(),
        };
        let ran = Arc::new(std::sync::atomic::AtomicBool::new(false));
        let r = Arc::clone(&ran);
        let (tx, rx) = std::sync::mpsc::channel();
        let a = Arc::clone(&adm);
        std::thread::spawn(move || {
            let _ = tx.send(admit(&a, 0xAD, req, Duration::from_millis(50), move |_| {
                r.store(true, std::sync::atomic::Ordering::SeqCst)
            }));
        });
        let got = rx
            .recv_timeout(Duration::from_secs(3))
            .expect("answered within the bound, not blocked");
        assert_eq!(
            got,
            Admitted::Refused(Response::Err {
                req_id: 31,
                status: STATUS_NO_SLOT
            })
        );
        assert!(!ran.load(std::sync::atomic::Ordering::SeqCst));
        drop(held);
        let (tx, rx) = std::sync::mpsc::channel();
        let req = Request::Head {
            req_id: 32,
            key: "k".into(),
        };
        assert_eq!(
            admit(&adm, 0xAD, req, Duration::from_millis(50), move |r| {
                let _ = tx.send(req_id_of(&r));
            }),
            Admitted::Running
        );
        assert_eq!(rx.recv_timeout(Duration::from_secs(5)).expect("ran"), 32);
        adm.close();
        assert_eq!(
            admit(
                &adm,
                0xAD,
                Request::Head {
                    req_id: 33,
                    key: "k".into()
                },
                Duration::ZERO,
                |_| {}
            ),
            Admitted::Closed
        );
    }

    // After a failed transfer whose completion was never
    // seen, the server's slot goes to the lane (kept until the QP is
    // destroyed), not back to the pool; after an ordinary failure it is
    // dropped at once.
    #[test]
    fn a_slot_of_an_unfinished_transfer_is_held_not_reused() {
        struct Tracked(Arc<std::sync::atomic::AtomicUsize>);
        impl Drop for Tracked {
            fn drop(&mut self) {
                self.0.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            }
        }
        let drops = Arc::new(std::sync::atomic::AtomicUsize::new(0));
        let n = || drops.load(std::sync::atomic::Ordering::SeqCst);
        let msg = dispose_after_failure(
            crate::transport::TransferError::from("WC error"),
            Tracked(Arc::clone(&drops)),
        );
        assert!(msg.contains("WC error"));
        assert_eq!(n(), 1, "observed failure: the slot goes back now");
        let d = crate::completion::Dispatch::new(4, 16, 16);
        let lane = d.register_lane(4, 0).expect("lane");
        let e = crate::transport::TransferError::held_by("no completion", Arc::clone(&lane));
        dispose_after_failure(e, Tracked(Arc::clone(&drops)));
        assert_eq!((n(), lane.held()), (1, 1), "unobserved: held by the lane");
        d.unregister_lane(lane.key(), 0, true);
        assert_eq!(n(), 2, "released once the QP is destroyed");
    }

    // The instance id is made once per process.
    #[test]
    fn the_process_instance_is_stable_and_not_zero() {
        assert_eq!(process_instance(), process_instance());
        assert_ne!(process_instance(), [0u8; 16]);
    }

    // The client's reply bound outlasts everything a healthy server may
    // spend before it answers (see the arithmetic in s3_rdma_proto).
    #[test]
    fn the_reply_bound_exceeds_the_servers_worst_case() {
        use crate::s3_rdma_proto::{
            CONTROL_REPLY_TIMEOUT, LANE_OP_WORST, SERVER_REPLY_WORST, STORAGE_ALLOWANCE,
        };
        assert_eq!(LANE_OP_WORST, Duration::from_secs(10));
        assert_eq!(
            SERVER_REPLY_WORST,
            ADMIT_WAIT + SLOT_WAIT + STORAGE_ALLOWANCE + LANE_OP_WORST * 2
        );
        assert_eq!(SERVER_REPLY_WORST, Duration::from_secs(33));
        assert!(CONTROL_REPLY_TIMEOUT > SERVER_REPLY_WORST);
        assert_eq!(CONTROL_REPLY_TIMEOUT, Duration::from_secs(38));
    }

    // A busy refusal of a PERSIST gives its parked slot back at once.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn a_refused_persist_releases_its_parked_slot() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let pool = Arc::new(MemoryPool::new(&dev, 64 * 1024, 1).expect("pool"));
        let peer = 0xB05E_0001_u64;
        let token = slot_park(peer, pool.checkout().expect("slot"));
        assert_eq!(pool.available(), 0);
        assert_eq!(
            refuse_busy(peer, 9, Some(token)),
            Response::Err {
                req_id: 9,
                status: STATUS_NO_SLOT
            }
        );
        assert!(!in_flight().lock().contains_key(&(peer, token)));
        assert_eq!(pool.available(), 1, "the slot is back in the pool");
    }

    /// TTL reaper claws back a slot whose PERSIST never arrives.
    /// Exercises the in-flight table with a hardware-registered pool slot.
    /// by parking a real `PoolBuffer`, sleeping past the test TTL,
    /// and confirming the reaper removed it.
    #[test]
    #[ignore = "requires a provisioned RC RDMA device and sufficient memlock"]
    fn slot_ttl_reaper_reclaims_expired() {
        let dev = Arc::new(
            RdmaDevice::open_first_active().expect("hardware test requires an active RDMA device"),
        );
        let pool = Arc::new(
            MemoryPool::new(&dev, 64 * 1024, 4)
                .expect("hardware test requires sufficient registered memory"),
        );

        // Use a unique (peer, addr) so concurrent tests don't fight
        // over the global IN_FLIGHT table.
        let peer = 0xCAFE_BABE_u64;
        let buf = pool.checkout().expect("pool slot");
        let addr = slot_park(peer, buf);

        assert!(in_flight().lock().contains_key(&(peer, addr)));

        // Force expiry by reaping with a zero TTL: any entry older
        // than 0 duration is dropped. Robust against a slow test
        // runner without sleeping for the real 60 s.
        let reclaimed = slot_reap_expired(Duration::from_nanos(0));
        assert!(reclaimed >= 1, "reaper should have caught our slot");
        assert!(!in_flight().lock().contains_key(&(peer, addr)));
    }
}

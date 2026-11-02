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

//! `S3TcpServer` — same callback shape as `S3RdmaServer`, dispatches
//! over a `TcpTransport` instead of an `RdmaTransport`. One per-peer
//! dispatcher thread loops on `recv_request`, runs the matching
//! storage callback, streams response + payload back. GET is ranged:
//! each response carries at most `MAX_GET_RANGE` bytes plus the object
//! size, generation, and the range's CRC-32C.

use std::sync::Arc;
use std::thread;

use parking_lot::RwLock;
use tracing::{debug, info, warn};

pub use crate::object::{DeleteFn, ObjectRead, PutStreamFn, ReadFn, ANY_GENERATION};
use crate::tcp::proto::{
    Request, Response, MAX_GET_RANGE, STATUS_ERROR, STATUS_GENERATION_CHANGED, STATUS_NOT_FOUND,
};
use crate::tcp::transport::TcpTransport;

pub struct S3TcpServer {
    put_fn: PutStreamFn,
    read_fn: ReadFn,
    delete_fn: DeleteFn,
    transport: Arc<TcpTransport>,
    workers: RwLock<Vec<thread::JoinHandle<()>>>,
    /// Caps concurrently-executing requests. Acquired by the
    /// dispatcher before it spawns a worker, so worker threads can never
    /// outnumber the permits and a pipelining client applies backpressure
    /// instead of exhausting threads/memory.
    admission: Arc<crate::admission::Admission>,
    /// Verifies each request's signature and applies policy.
    /// Absent = every request refused: failing closed is the only safe
    /// default for a transport that previously served anyone.
    auth_fn: Option<crate::auth::AuthFn>,
    /// Rejects replayed signatures within the clock-skew window.
    replay: Arc<crate::auth::ReplayGuard>,
}

impl S3TcpServer {
    pub fn new(
        put_fn: PutStreamFn,
        read_fn: ReadFn,
        delete_fn: DeleteFn,
        transport: Arc<TcpTransport>,
    ) -> Arc<Self> {
        Self::with_max_in_flight(
            put_fn,
            read_fn,
            delete_fn,
            transport,
            crate::admission::default_max_in_flight(),
        )
    }

    /// Same as `new` but with an explicit in-flight cap.
    pub fn with_max_in_flight(
        put_fn: PutStreamFn,
        read_fn: ReadFn,
        delete_fn: DeleteFn,
        transport: Arc<TcpTransport>,
        max_in_flight: usize,
    ) -> Arc<Self> {
        Arc::new(Self {
            put_fn,
            read_fn,
            delete_fn,
            transport,
            workers: RwLock::new(Vec::new()),
            admission: crate::admission::Admission::new(max_in_flight),
            auth_fn: None,
            replay: Arc::new(crate::auth::ReplayGuard::new()),
        })
    }

    /// Attach the request verifier. Without it every request is rejected.
    pub fn with_auth(mut self: Arc<Self>, auth_fn: crate::auth::AuthFn) -> Arc<Self> {
        let inner = Arc::get_mut(&mut self)
            .expect("invariant: with_auth is called before the server is shared");
        inner.auth_fn = Some(auth_fn);
        self
    }

    /// Verify signature freshness, replay, then policy. Fails closed when
    /// no verifier is configured.
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
        let (action, key) = match req {
            Request::Put { key, .. } => ("s3:PutObject", key.as_str()),
            Request::Get { key, .. } => ("s3:GetObject", key.as_str()),
            Request::Delete { key, .. } => ("s3:DeleteObject", key.as_str()),
            Request::Head { key, .. } => ("s3:GetObject", key.as_str()),
        };
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

    pub fn start(self: &Arc<Self>) {
        let outer = Arc::clone(self);
        self.transport
            .set_on_peer_accepted(Arc::new(move |peer_id| {
                let for_dispatch = Arc::clone(&outer);
                let for_record = Arc::clone(&outer);
                let handle = thread::Builder::new()
                    .name(format!("s3-tcp-dispatch-{peer_id}"))
                    .spawn(move || for_dispatch.dispatch_loop(peer_id))
                    .expect("spawn s3-tcp dispatcher");
                for_record.workers.write().push(handle);
            }));
        info!("S3-over-TCP server registered");
    }

    pub fn shutdown(&self) {
        self.admission.close();
        self.transport.shutdown();
    }

    pub fn drained(&self) -> bool {
        self.admission.drained()
    }

    fn dispatch_loop(self: &Arc<Self>, peer_id: u64) {
        info!(peer_id, "S3-TCP dispatcher started");
        loop {
            // Read just the request header. Single dispatcher per
            // peer because the TCP socket is a strict byte stream —
            // can't have two workers reading concurrently.
            let req = match self.transport.recv_request(peer_id) {
                Ok(r) => r,
                Err(e) => {
                    debug!(peer_id, error = %e, "S3-TCP dispatcher exiting");
                    self.transport.drop_peer(peer_id);
                    return;
                }
            };

            // PUT carries the payload inline after the header. We
            // MUST read it on the dispatcher thread (sequencing
            // requirement on the shared socket) before handing off
            // to a worker. The worker then does the disk write +
            // response, which lets the dispatcher pipeline the next
            // request while disk I/O is in flight on workers.
            // Authenticate BEFORE any work. An unauthenticated caller
            // never reaches a storage callback, never consumes an admission
            // permit, and — critically for PUT — never gets its payload read
            // into memory, so a flood of bad requests costs only signature
            // verification.
            let (header, req, body) = req;
            if let Err(e) = self.authenticate(&header, &req, &body) {
                warn!(peer_id, access_key = %header.access_key, error = %e,
                      "S3-TCP request rejected");
                let req_id = match &req {
                    Request::Put { req_id, .. }
                    | Request::Get { req_id, .. }
                    | Request::Delete { req_id, .. }
                    | Request::Head { req_id, .. } => *req_id,
                };
                let _ = self.transport.send_response(
                    peer_id,
                    &Response::Err {
                        req_id,
                        status: super::proto::STATUS_UNAUTHORIZED,
                    },
                );
                // A rejected PUT leaves its payload unread in the stream, so
                // the connection can no longer be framed correctly. Drop it
                // rather than trying to resynchronize.
                self.transport.drop_peer(peer_id);
                return;
            }

            match req {
                Request::Put {
                    req_id,
                    key,
                    length,
                } => {
                    // Admit BEFORE reading the payload: blocking here
                    // stops the dispatcher reading, which fills the socket
                    // buffer and pushes back on the client via TCP flow
                    // control. The payload must be consumed in order on
                    // this socket anyway, so the PUT streams on the
                    // dispatcher thread; the client runs one transaction
                    // per connection at a time.
                    let Some(_permit) = self.admission.acquire() else {
                        return;
                    };
                    let Some(resp) = self.receive_put(peer_id, req_id, &key, length) else {
                        // The socket failed mid-payload; the stream can't be
                        // re-framed.
                        self.transport.drop_peer(peer_id);
                        return;
                    };
                    if let Err(e) = self.transport.send_response(peer_id, &resp) {
                        debug!(peer_id, req_id, error = %e, "send_response failed (peer likely gone)");
                    }
                }
                other => {
                    // Non-PUT requests don't have inline payload and
                    // their handlers are cheap (one storage callback +
                    // one short response). Off-load anyway so the
                    // dispatcher never blocks even on slow storage.
                    let Some(permit) = self.admission.acquire() else {
                        return;
                    };
                    let svc = Arc::clone(self);
                    thread::spawn(move || {
                        let _permit = permit;
                        if let Err(e) = svc.handle(peer_id, other) {
                            warn!(peer_id, error = %e, "S3-TCP non-PUT request failed");
                        }
                    });
                }
            }
        }
    }

    /// Stream a PUT's `length` payload bytes from the socket into
    /// storage, CRC-32C-ing them on the way. Returns the response to
    /// send, or `None` if the socket failed (connection unusable). A
    /// storage failure drains the rest of the payload so the connection
    /// stays framed for the next request.
    fn receive_put(&self, peer_id: u64, req_id: u64, key: &str, length: u64) -> Option<Response> {
        let mut received = 0u64;
        let mut crc = 0u32;
        let mut socket_failed = false;
        let result = (self.put_fn)(key, length, &mut |buf| match self
            .transport
            .read_payload(peer_id, buf)
        {
            Ok(()) => {
                crc = nvstor_core::checksum::crc32c_append(crc, buf);
                received += buf.len() as u64;
                Ok(())
            }
            Err(e) => {
                socket_failed = true;
                Err(e)
            }
        });
        if socket_failed {
            warn!(peer_id, key = %key, received, length, "PUT payload read failed");
            return None;
        }
        match result {
            Ok(written) if received == length => Some(Response::Ok {
                req_id,
                length: written,
                crc32c: crc,
            }),
            other => {
                if let Err(e) = &other {
                    warn!(peer_id, key = %key, error = %e, "PUT storage write failed");
                } else {
                    warn!(peer_id, key = %key, received, length, "PUT store returned before consuming the payload");
                }
                self.drain_payload(peer_id, length - received).ok()?;
                Some(Response::Err {
                    req_id,
                    status: STATUS_ERROR,
                })
            }
        }
    }

    /// Read and discard `n` payload bytes.
    fn drain_payload(&self, peer_id: u64, mut n: u64) -> std::io::Result<()> {
        let mut scratch = vec![0u8; (n as usize).min(1024 * 1024)];
        while n > 0 {
            let k = (n as usize).min(scratch.len());
            self.transport.read_payload(peer_id, &mut scratch[..k])?;
            n -= k as u64;
        }
        Ok(())
    }

    fn handle(&self, peer_id: u64, req: Request) -> Result<(), String> {
        match req {
            Request::Put { req_id, .. } => {
                // PUT carries a payload that only the dispatcher may read
                // (it streams through `receive_put` in order); it never
                // reaches a worker.
                self.transport
                    .send_response(
                        peer_id,
                        &Response::Err {
                            req_id,
                            status: STATUS_ERROR,
                        },
                    )
                    .map_err(|e| format!("send Err: {e}"))
            }

            Request::Get {
                req_id,
                key,
                offset,
                length,
                if_generation,
            } => {
                // Stat first so the staging buffer is sized to what this
                // range can return, not to MAX_GET_RANGE for every small
                // object. The range is staged (not streamed) because its
                // CRC-32C goes in the header, ahead of the payload.
                let stat = match (self.read_fn)(&key, 0, &mut []) {
                    Ok(s) => s,
                    Err(e) => return self.send_read_error(peer_id, req_id, &key, &e),
                };
                let want = length
                    .min(MAX_GET_RANGE)
                    .min(stat.size.saturating_sub(offset)) as usize;
                let mut buf = vec![0u8; want];
                let read = match (self.read_fn)(&key, offset, &mut buf) {
                    Ok(r) => r,
                    Err(e) => return self.send_read_error(peer_id, req_id, &key, &e),
                };
                if if_generation != ANY_GENERATION && read.generation != if_generation {
                    return self
                        .transport
                        .send_response(
                            peer_id,
                            &Response::Err {
                                req_id,
                                status: STATUS_GENERATION_CHANGED,
                            },
                        )
                        .map_err(|e| format!("send GenerationChanged: {e}"));
                }
                let data = &buf[..read.len];
                let resp = Response::Range {
                    req_id,
                    length: read.len as u64,
                    object_size: read.size,
                    generation: read.generation,
                    crc32c: nvstor_core::checksum::crc32c(data),
                };
                self.transport
                    .send_response_with_payload(peer_id, &resp, data)
                    .map_err(|e| format!("send Range: {e}"))
            }

            Request::Delete { req_id, key } => match (self.delete_fn)(&key) {
                Ok(()) => self
                    .transport
                    .send_response(
                        peer_id,
                        &Response::Ok {
                            req_id,
                            length: 0,
                            crc32c: 0,
                        },
                    )
                    .map_err(|e| format!("send Ok: {e}")),
                Err(_) => self
                    .transport
                    .send_response(
                        peer_id,
                        &Response::Err {
                            req_id,
                            status: STATUS_NOT_FOUND,
                        },
                    )
                    .map_err(|e| format!("send NotFound: {e}")),
            },

            Request::Head { req_id, key } => match (self.read_fn)(&key, 0, &mut []) {
                Ok(stat) => self
                    .transport
                    .send_response(
                        peer_id,
                        &Response::Range {
                            req_id,
                            length: 0,
                            object_size: stat.size,
                            generation: stat.generation,
                            crc32c: 0,
                        },
                    )
                    .map_err(|e| format!("send Range: {e}")),
                Err(e) => self.send_read_error(peer_id, req_id, &key, &e),
            },
        }
    }

    /// NotFound is an ordinary answer; any other read failure is logged
    /// and reported as ERROR rather than masquerading as a missing key.
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
            warn!(peer_id, key = %key, error = %e, "S3-TCP read failed");
            STATUS_ERROR
        };
        self.transport
            .send_response(peer_id, &Response::Err { req_id, status })
            .map_err(|e| format!("send Err: {e}"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Test credentials + a verifier that accepts exactly them. Mirrors what
    /// nvstor-server builds from IAM, without pulling IAM into this crate.
    fn test_creds() -> crate::auth::Credentials {
        crate::auth::Credentials::new("AKIATEST", "test-secret")
    }

    fn allow_test_creds() -> crate::auth::AuthFn {
        Arc::new(|ctx: &crate::auth::AuthContext| {
            let c = test_creds();
            if ctx.header.access_key != c.access_key {
                return Err(crate::auth::AuthError::UnknownKey);
            }
            if !crate::auth::verify_signature(
                &c.secret_key,
                ctx.header.timestamp,
                ctx.header.nonce,
                ctx.body,
                &ctx.header.signature,
            ) {
                return Err(crate::auth::AuthError::BadSignature);
            }
            Ok("test-principal".to_string())
        })
    }

    use crate::object::GetError;
    use crate::tcp::client::S3TcpClient;

    /// Start an authenticated server over `fns` and return a connected
    /// client (plus the transports, so the test can shut them down).
    fn serve(
        fns: (crate::object::WriteFn, ReadFn, DeleteFn),
    ) -> (Arc<TcpTransport>, Arc<TcpTransport>, S3TcpClient) {
        let (write_fn, read_fn, delete_fn) = fns;
        let write_fn = crate::object::buffered_put(write_fn);
        let server_t = TcpTransport::new();
        let client_t = TcpTransport::new();
        let server = S3TcpServer::new(write_fn, read_fn, delete_fn, Arc::clone(&server_t))
            .with_auth(allow_test_creds());
        server.start();
        client_t.set_credentials(test_creds());
        let addr = server_t.listen(0).expect("listen");
        // `listen` binds the wildcard address; connecting back to 0.0.0.0 is
        // not reliably routable, so dial the loopback address explicitly.
        let peer_id = client_t
            .connect(&format!("127.0.0.1:{}", addr.port()))
            .expect("connect");
        let client = S3TcpClient::new(Arc::clone(&client_t), peer_id);
        (server_t, client_t, client)
    }

    /// Full PUT → HEAD → GET → DELETE round-trip on a loopback
    /// TcpTransport with an in-memory storage callback set.
    /// Exercises the same callback surface nvstor-server uses,
    /// just without the storage backend wiring.
    #[test]
    fn loopback_put_head_get_delete() {
        let (server_t, _client_t, client) = serve(crate::object::mem_store());

        // PUT a 4 MiB blob — exercises the inline-payload path.
        let data = vec![0xCDu8; 4 * 1024 * 1024];
        let n = client.put("checkpoint/rank-0/step-1", &data).expect("put");
        assert_eq!(n, data.len() as u64);

        // HEAD returns size.
        assert_eq!(
            client.head("checkpoint/rank-0/step-1").expect("head"),
            data.len() as u64,
        );

        // GET returns the same bytes.
        let got = client.get("checkpoint/rank-0/step-1").expect("get");
        assert_eq!(got, data);

        // DELETE then GET fails as not-found.
        client.delete("checkpoint/rank-0/step-1").expect("delete");
        assert_eq!(
            client.stat("checkpoint/rank-0/step-1"),
            Err(GetError::NotFound)
        );
        assert!(client.get("checkpoint/rank-0/step-1").is_err());

        server_t.shutdown();
    }

    /// An object larger than MAX_GET_RANGE comes back intact through
    /// several ranges, each CRC-checked and pinned to one generation.
    #[test]
    fn get_spans_multiple_ranges() {
        let fns = crate::object::mem_store();
        let seed = Arc::clone(&fns.0);
        let (server_t, _client_t, client) = serve(fns);
        let size = 2 * MAX_GET_RANGE as usize + 12_345;
        let data: Vec<u8> = (0..size as u32)
            .map(|i| (i.wrapping_mul(2_654_435_761) >> 11) as u8)
            .collect();
        seed("big", &data).expect("seed");
        assert_eq!(client.get("big").expect("get"), data);
        server_t.shutdown();
    }

    #[test]
    fn get_range_into_bounds() {
        let (server_t, _client_t, client) = serve(crate::object::mem_store());
        client.put("obj", b"0123456789").expect("put");
        client.put("empty", b"").expect("put empty");
        let (size, generation) = client.stat("obj").expect("stat");
        assert_eq!(size, 10);

        let mut buf = [0u8; 4];
        let r = client
            .get_range_into("obj", 3, generation, &mut buf)
            .expect("mid");
        assert_eq!((r.len, r.size, r.generation), (4, 10, generation));
        assert_eq!(&buf, b"3456");
        // Short only at the end of the object.
        let r = client
            .get_range_into("obj", 8, generation, &mut buf)
            .expect("tail");
        assert_eq!(&buf[..r.len], b"89");
        // At and past the end: zero bytes, still a valid answer.
        assert_eq!(
            client
                .get_range_into("obj", 10, generation, &mut buf)
                .expect("eof")
                .len,
            0
        );
        assert_eq!(
            client
                .get_range_into("obj", 99, ANY_GENERATION, &mut buf)
                .expect("past")
                .len,
            0
        );

        assert_eq!(client.get("empty").expect("get empty"), Vec::<u8>::new());
        assert_eq!(
            client.get_range_into("missing", 0, ANY_GENERATION, &mut buf),
            Err(GetError::NotFound)
        );
        server_t.shutdown();
    }

    /// A replace between ranges is reported before any stale bytes are
    /// accepted, and the connection stays usable afterwards.
    #[test]
    fn replaced_object_fails_with_generation_changed() {
        let (server_t, _client_t, client) = serve(crate::object::mem_store());
        client.put("obj", b"first").expect("put");
        let (_, g1) = client.stat("obj").expect("stat");
        client.put("obj", b"second").expect("replace");
        let mut buf = [0u8; 8];
        assert_eq!(
            client.get_range_into("obj", 0, g1, &mut buf),
            Err(GetError::GenerationChanged)
        );
        let r = client
            .get_range_into("obj", 0, ANY_GENERATION, &mut buf)
            .expect("fresh read");
        assert_ne!(r.generation, g1);
        assert_eq!(&buf[..r.len], b"second");
        server_t.shutdown();
    }

    /// A storage failure is an ERROR, not a missing key.
    #[test]
    fn read_failure_is_not_reported_as_not_found() {
        let read_fn: ReadFn = Arc::new(|_, _, _| Err(std::io::Error::other("disk on fire")));
        let (server_t, _client_t, client) = serve((
            Arc::new(|_, d: &[u8]| Ok(d.len() as u64)),
            read_fn,
            Arc::new(|_| Ok(())),
        ));
        assert_eq!(client.stat("k"), Err(GetError::Server(STATUS_ERROR)));
        let mut buf = [0u8; 4];
        assert_eq!(
            client.get_range_into("k", 0, ANY_GENERATION, &mut buf),
            Err(GetError::Server(STATUS_ERROR))
        );
        server_t.shutdown();
    }

    /// The client rejects bytes whose CRC-32C doesn't match the header.
    /// A hand-driven server sends a Range with a wrong checksum.
    #[test]
    fn client_detects_crc_mismatch() {
        let server_t = TcpTransport::new();
        let srv = Arc::clone(&server_t);
        server_t.set_on_peer_accepted(Arc::new(move |peer| {
            let srv = Arc::clone(&srv);
            thread::spawn(move || {
                while let Ok((_, req, _)) = srv.recv_request(peer) {
                    let payload = b"payload!";
                    let resp = Response::Range {
                        req_id: req.req_id(),
                        length: payload.len() as u64,
                        object_size: payload.len() as u64,
                        generation: 7,
                        crc32c: nvstor_core::checksum::crc32c(payload) ^ 1,
                    };
                    if srv
                        .send_response_with_payload(peer, &resp, payload)
                        .is_err()
                    {
                        return;
                    }
                }
            });
        }));
        let addr = server_t.listen(0).expect("listen");
        let client_t = TcpTransport::new();
        client_t.set_credentials(test_creds());
        let peer = client_t
            .connect(&format!("127.0.0.1:{}", addr.port()))
            .expect("connect");
        let client = S3TcpClient::new(Arc::clone(&client_t), peer);
        let mut buf = [0u8; 8];
        let err = client
            .get_range_into("k", 0, ANY_GENERATION, &mut buf)
            .unwrap_err();
        assert!(matches!(err, GetError::Corrupt { .. }), "{err:?}");
        assert!(err.is_retryable());
        // The payload was consumed, so the stream is still framed.
        assert!(matches!(
            client.get_range_into("k", 0, ANY_GENERATION, &mut buf),
            Err(GetError::Corrupt { .. })
        ));
        server_t.shutdown();
    }

    /// Serve with a custom streaming PUT over the in-memory store.
    fn serve_put(
        make_put: impl FnOnce(crate::object::WriteFn) -> PutStreamFn,
    ) -> (Arc<TcpTransport>, Arc<TcpTransport>, S3TcpClient, ReadFn) {
        let (write_fn, read_fn, delete_fn) = crate::object::mem_store();
        let server_t = TcpTransport::new();
        let client_t = TcpTransport::new();
        let server = S3TcpServer::new(
            make_put(write_fn),
            Arc::clone(&read_fn),
            delete_fn,
            Arc::clone(&server_t),
        )
        .with_auth(allow_test_creds());
        server.start();
        client_t.set_credentials(test_creds());
        let addr = server_t.listen(0).expect("listen");
        let peer = client_t
            .connect(&format!("127.0.0.1:{}", addr.port()))
            .expect("connect");
        let client = S3TcpClient::new(Arc::clone(&client_t), peer);
        (server_t, client_t, client, read_fn)
    }

    /// Store pulls the payload 1 MiB at a time, as PmObjectStore does.
    fn chunked_put(write_fn: crate::object::WriteFn) -> PutStreamFn {
        Arc::new(move |key, size, fill| {
            let mut out = Vec::new();
            let mut chunk = vec![0u8; 1 << 20];
            while (out.len() as u64) < size {
                let n = (size - out.len() as u64).min(chunk.len() as u64) as usize;
                fill(&mut chunk[..n])?;
                out.extend_from_slice(&chunk[..n]);
            }
            write_fn(key, &out).map_err(std::io::Error::other)
        })
    }

    #[test]
    fn streaming_put_from_reader_round_trips() {
        let (server_t, _c, client, _) = serve_put(chunked_put);
        let data: Vec<u8> = (0..20 * 1024 * 1024u32).map(|i| (i * 13) as u8).collect();
        let mut src = std::io::Cursor::new(data.clone());
        assert_eq!(
            client
                .put_from_reader("big", data.len() as u64, &mut src)
                .expect("put"),
            data.len() as u64
        );
        assert_eq!(client.get("big").expect("get"), data);
        server_t.shutdown();
    }

    /// The store fails after the first chunk: the server drains the rest
    /// of the payload, answers ERROR, and the connection stays usable.
    #[test]
    fn storage_failure_mid_put_keeps_connection_framed() {
        let (server_t, _c, client, _) = serve_put(|write_fn| {
            Arc::new(move |key, size, fill| {
                if key == "fails" {
                    let mut chunk = vec![0u8; 1 << 20];
                    fill(&mut chunk)?;
                    return Err(std::io::Error::other("disk full"));
                }
                let mut buf = vec![0u8; size as usize];
                fill(&mut buf)?;
                write_fn(key, &buf).map_err(std::io::Error::other)
            })
        });
        let err = client.put("fails", &vec![1u8; 5 << 20]).unwrap_err();
        assert!(err.contains("status=2"), "{err}");
        assert_eq!(client.stat("fails"), Err(GetError::NotFound));
        // Same connection, next request is framed correctly.
        client.put("ok", b"after").expect("put after failure");
        assert_eq!(client.get("ok").expect("get"), b"after");
        server_t.shutdown();
    }

    /// The client's source runs dry mid-PUT: the client drops the
    /// connection, nothing is published, and the server keeps serving.
    #[test]
    fn short_client_source_publishes_nothing() {
        let (server_t, _c, client, read_fn) = serve_put(chunked_put);
        let mut short = std::io::Cursor::new(vec![9u8; 1000]);
        let err = client
            .put_from_reader("partial", 1 << 20, &mut short)
            .unwrap_err();
        assert!(err.contains("PUT payload"), "{err}");
        // Give the server a moment to see the closed socket.
        thread::sleep(std::time::Duration::from_millis(50));
        let mut none = [0u8; 0];
        assert_eq!(
            read_fn("partial", 0, &mut none).unwrap_err().kind(),
            std::io::ErrorKind::NotFound
        );
        server_t.shutdown();
    }

    /// A server that reports a different CRC than the client sent is
    /// caught by the client.
    #[test]
    fn client_detects_put_crc_mismatch() {
        let server_t = TcpTransport::new();
        let srv = Arc::clone(&server_t);
        server_t.set_on_peer_accepted(Arc::new(move |peer| {
            let srv = Arc::clone(&srv);
            thread::spawn(move || {
                while let Ok((_, Request::Put { req_id, length, .. }, _)) = srv.recv_request(peer) {
                    let mut buf = vec![0u8; length as usize];
                    if srv.read_payload(peer, &mut buf).is_err() {
                        return;
                    }
                    let resp = Response::Ok {
                        req_id,
                        length,
                        crc32c: nvstor_core::checksum::crc32c(&buf) ^ 1,
                    };
                    if srv.send_response(peer, &resp).is_err() {
                        return;
                    }
                }
            });
        }));
        let addr = server_t.listen(0).expect("listen");
        let client_t = TcpTransport::new();
        client_t.set_credentials(test_creds());
        let peer = client_t
            .connect(&format!("127.0.0.1:{}", addr.port()))
            .expect("connect");
        let client = S3TcpClient::new(Arc::clone(&client_t), peer);
        let err = client.put("k", b"payload").unwrap_err();
        assert!(err.contains("crc32c"), "{err}");
        server_t.shutdown();
    }

    #[test]
    fn independent_clients_can_share_credentials_without_false_replay() {
        let transport = TcpTransport::new();
        let server = S3TcpServer::new(
            Arc::new(|_, size, fill| {
                let mut buf = vec![0u8; size as usize];
                fill(&mut buf)?;
                Ok(size)
            }),
            Arc::new(|_, _, _| Err(std::io::ErrorKind::NotFound.into())),
            Arc::new(|_| Ok(())),
            transport.clone(),
        )
        .with_auth(allow_test_creds());
        server.start();
        let address = transport.listen(0).unwrap();
        for index in 0..4 {
            let peer_transport = TcpTransport::new();
            peer_transport.set_credentials(test_creds());
            let peer = peer_transport
                .connect(&format!("127.0.0.1:{}", address.port()))
                .unwrap();
            let client = S3TcpClient::new(peer_transport.clone(), peer);
            assert_eq!(
                client.put(&format!("client-{index}"), b"payload").unwrap(),
                7
            );
            peer_transport.shutdown();
        }
        transport.shutdown();
    }

    /// Multiple back-to-back PUTs on the same connection — verifies
    /// the framing pipelines correctly without one transaction
    /// stealing another's bytes.
    #[test]
    fn loopback_pipelined_puts() {
        let (server_t, _client_t, client) = serve(crate::object::mem_store());

        for i in 0..16 {
            let key = format!("k-{i}");
            let data = vec![i as u8; 64 * 1024];
            client.put(&key, &data).expect("put");
        }
        // Verify all 16 entries landed with the right content.
        for i in 0..16 {
            let got = client.get(&format!("k-{i}")).expect("get");
            assert_eq!(got, vec![i as u8; 64 * 1024]);
        }

        server_t.shutdown();
    }
}

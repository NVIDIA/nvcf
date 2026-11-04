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

//! The KV client: one connection to one server rail.
//!
//! A [`KvClient`] registers the layout of its landing pool, then sends
//! signed, batched GET / PUT / HEAD requests and checks that each reply
//! answers its request (same req_id and kind, one result per entry). The
//! server writes a GET's blocks straight into the landing pages and
//! replies after the WRITEs completed; the caller verifies each block's
//! check over the bytes it then reads ([`crate::server::block_check`]).
//!
//! Each request carries the connection's next sequence number and a fresh
//! nonce; a request is answered before the next is sent (the server serves
//! one at a time per connection), so throughput comes from several
//! clients, one per connection.
//!
//! **Deadlines**. A GET or PUT waits for its reply at
//! most its budget plus [`REPLY_MARGIN`]; HEAD and LAYOUT at most
//! [`CONTROL_DEADLINE`]. A missed deadline fails the lane, which tears the
//! connection down. After any failed call, transport or protocol, the
//! client refuses further use ([`KvError::Broken`]): the caller drops it,
//! which destroys the connection's QPs, and only then may reuse the
//! landing slots of a GET that failed, since its WRITEs may still have
//! been landing until then. Slots are not quarantined without dropping the
//! connection.

use std::sync::Arc;
use std::time::{Duration, Instant};

use nvstor_rdma::auth::{next_nonce, Credentials};
use nvstor_rdma::device::{Access, RdmaDevice};
use nvstor_rdma::kv_proto::{
    self, GetEntry, GetResult, Key, Layout, PutEntry, Request, Response, KV_MSG_SIZE,
    NAMESPACE_LEN, STATUS_ERROR, STATUS_NOT_STORED, STATUS_OK,
};
use nvstor_rdma::pool::MemoryPool;
use nvstor_rdma::rail::{RailPeer, Rails};
use nvstor_rdma::transport::RdmaTransport;

/// Time allowed past a request's budget for its reply to arrive.
pub const REPLY_MARGIN: Duration = Duration::from_millis(200);
/// Deadline of a HEAD or LAYOUT reply.
pub const CONTROL_DEADLINE: Duration = Duration::from_secs(2);

/// How a client's request travels: one signed message out, its reply back.
pub trait Exchange {
    /// Send the message `encode` writes (into the transport's buffer,
    /// returning its length) and return the reply's bytes, failing if it
    /// has not arrived by `deadline`.
    fn exchange(
        &self,
        encode: &mut dyn FnMut(&mut [u8]) -> std::io::Result<usize>,
        deadline: Instant,
    ) -> Result<Vec<u8>, String>;
}

/// The real [`Exchange`]: lane 0 of one rail connection, under the
/// peer's transaction lock.
pub struct RailExchange {
    conn: RailPeer,
}

impl RailExchange {
    pub fn rail(&self) -> usize {
        self.conn.rail()
    }
}

impl Exchange for RailExchange {
    fn exchange(
        &self,
        encode: &mut dyn FnMut(&mut [u8]) -> std::io::Result<usize>,
        deadline: Instant,
    ) -> Result<Vec<u8>, String> {
        // Signed straight into the registered SEND buffer.
        self.conn.transport().message_exchange(
            self.conn.peer_id(),
            encode,
            Some(deadline),
            |reply| Ok(reply.to_vec()),
        )
    }
}

/// A client transport for one NIC: KV-sized control messages, two lanes,
/// the client's credentials, and a token pool (blocks land in the caller's
/// registered region, not in the pool). A dialled QP grants the server
/// what the pool's access allows, and the server both writes (GET) and
/// reads (PUT) the client's region, so the pool is registered for both;
/// its own rkey is never sent anywhere. The landing region must be
/// registered `REMOTE_READ_WRITE` too (`RailRegion::dmabuf_with`).
pub fn kv_client_transport(
    device: Arc<RdmaDevice>,
    creds: &Credentials,
) -> Result<RdmaTransport, String> {
    let pool = Arc::new(MemoryPool::with_access(
        &device,
        4096,
        1,
        Access::REMOTE_READ_WRITE,
    )?);
    let mut t = RdmaTransport::new(device, pool);
    t.set_control_msg_size(KV_MSG_SIZE)?;
    t.set_lanes_per_peer(2);
    t.set_credentials(creds.clone());
    Ok(t)
}

/// The client's NICs as [`Rails`], each with a [`kv_client_transport`].
/// Connect through these, not through `Rails::open` (whose transports
/// have S3-over-RDMA's smaller control messages).
pub fn kv_rails(devices: Vec<RdmaDevice>, creds: &Credentials) -> Result<Arc<Rails>, String> {
    let transports = devices
        .into_iter()
        .map(|d| kv_client_transport(Arc::new(d), creds).map(Arc::new))
        .collect::<Result<Vec<_>, String>>()?;
    Ok(Rails::from_transports(transports))
}

/// Connect to the server rail at `addr` (position `server_rail` of its
/// rails, whose GIDs are `server_rails` when known) on the best local NIC
/// of `rails` (from [`kv_rails`]). The layouts registered on the
/// connection must use the landing region's rkey for that NIC:
/// `region.rkey(exchange.rail())`.
pub fn connect_rail(
    rails: &Arc<Rails>,
    addr: &str,
    server_rail: usize,
    server_rails: &[[u8; 16]],
) -> Result<RailExchange, String> {
    Ok(RailExchange {
        conn: rails.connect_rail(addr, server_rail, server_rails)?,
    })
}

#[derive(Debug, PartialEq, Eq)]
pub enum KvError {
    /// The connection failed. The client is now broken.
    Transport(String),
    /// The reply did not decode or did not answer the request. The client
    /// is now broken.
    Protocol(String),
    /// The whole request was refused with this status; the client is fine.
    Refused(u8),
    /// The request could not be encoded (an empty or oversized batch, a
    /// bad budget or layout); nothing was sent and the client is fine.
    Invalid(String),
    /// An earlier call failed: drop this client (which destroys its
    /// connection) and connect again.
    Broken(String),
}

impl std::fmt::Display for KvError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            KvError::Transport(e) => write!(f, "KV transport: {e}"),
            KvError::Protocol(e) => write!(f, "KV protocol: {e}"),
            KvError::Refused(s) => write!(f, "KV request refused (status {s})"),
            KvError::Invalid(e) => write!(f, "KV request invalid: {e}"),
            KvError::Broken(e) => write!(f, "KV connection unusable after an earlier failure: {e}"),
        }
    }
}

impl std::error::Error for KvError {}

pub struct KvClient<E: Exchange> {
    link: E,
    creds: Credentials,
    seq: u64,
    req_id: u64,
    /// Why the client stopped, after a transport or protocol failure.
    broken: Option<String>,
}

/// A status this build does not know, as kv_proto requires: an error.
fn known(status: u8) -> u8 {
    if status <= STATUS_NOT_STORED {
        status
    } else {
        STATUS_ERROR
    }
}

/// A whole-request refusal; a refusal that claims success is an error.
fn refused(status: u8) -> KvError {
    KvError::Refused(if status == STATUS_OK {
        STATUS_ERROR
    } else {
        known(status)
    })
}

impl<E: Exchange> KvClient<E> {
    pub fn new(link: E, creds: Credentials) -> KvClient<E> {
        KvClient {
            link,
            creds,
            seq: 0,
            req_id: 0,
            broken: None,
        }
    }

    /// Whether an earlier failure made the client unusable.
    pub fn is_broken(&self) -> bool {
        self.broken.is_some()
    }

    pub fn link(&self) -> &E {
        &self.link
    }

    /// Register a landing layout on this connection.
    pub fn register_layout(&mut self, layout: Layout) -> Result<(), KvError> {
        match self.call(CONTROL_DEADLINE, |req_id, seq| Request::Layout {
            req_id,
            seq,
            layout,
        })? {
            Response::LayoutAck {
                status: STATUS_OK, ..
            } => Ok(()),
            Response::LayoutAck { status, .. } | Response::Err { status, .. } => {
                Err(refused(status))
            }
            other => Err(self.protocol(format!("unexpected reply {other:?}"))),
        }
    }

    /// GET blocks into the layout's slots: one result per entry, in order.
    /// On `Ok`, every WRITE has completed: an OK block's bytes are in its
    /// slot. On a transport or protocol error the slots named stay
    /// undefined and in use until this client is dropped (see the module
    /// doc); there is no client deadline yet.
    pub fn get(
        &mut self,
        namespace: [u8; NAMESPACE_LEN],
        layout_id: u32,
        budget_ms: u32,
        entries: Vec<GetEntry>,
    ) -> Result<Vec<GetResult>, KvError> {
        let wait = Duration::from_millis(u64::from(budget_ms)) + REPLY_MARGIN;
        match self.call(wait, |req_id, seq| Request::Get {
            req_id,
            seq,
            namespace,
            layout_id,
            budget_ms,
            entries,
        })? {
            Response::Get { mut results, .. } => {
                results.iter_mut().for_each(|r| r.status = known(r.status));
                Ok(results)
            }
            Response::Err { status, .. } => Err(refused(status)),
            other => Err(self.protocol(format!("unexpected reply {other:?}"))),
        }
    }

    /// PUT blocks from the layout's slots: one status per entry, in order.
    pub fn put(
        &mut self,
        namespace: [u8; NAMESPACE_LEN],
        layout_id: u32,
        budget_ms: u32,
        entries: Vec<PutEntry>,
    ) -> Result<Vec<u8>, KvError> {
        let wait = Duration::from_millis(u64::from(budget_ms)) + REPLY_MARGIN;
        match self.call(wait, |req_id, seq| Request::Put {
            req_id,
            seq,
            namespace,
            layout_id,
            budget_ms,
            entries,
        })? {
            Response::Put { statuses, .. } => Ok(statuses.into_iter().map(known).collect()),
            Response::Err { status, .. } => Err(refused(status)),
            other => Err(self.protocol(format!("unexpected reply {other:?}"))),
        }
    }

    /// Which keys are stored: one status per key, in order.
    pub fn head(
        &mut self,
        namespace: [u8; NAMESPACE_LEN],
        keys: Vec<Key>,
    ) -> Result<Vec<u8>, KvError> {
        match self.call(CONTROL_DEADLINE, |req_id, seq| Request::Head {
            req_id,
            seq,
            namespace,
            keys,
        })? {
            Response::Head { statuses, .. } => Ok(statuses.into_iter().map(known).collect()),
            Response::Err { status, .. } => Err(refused(status)),
            other => Err(self.protocol(format!("unexpected reply {other:?}"))),
        }
    }

    /// Mark the client broken by a bad reply.
    fn protocol(&mut self, msg: String) -> KvError {
        self.broken = Some(msg.clone());
        KvError::Protocol(msg)
    }

    fn call(
        &mut self,
        wait: Duration,
        build: impl FnOnce(u64, u64) -> Request,
    ) -> Result<Response, KvError> {
        if let Some(why) = &self.broken {
            return Err(KvError::Broken(why.clone()));
        }
        self.req_id += 1;
        self.seq += 1;
        let req = build(self.req_id, self.seq);
        // Encode once, before anything is sent: a request that does not
        // encode is the caller's mistake, not the connection's.
        let mut probe = [0u8; KV_MSG_SIZE];
        req.encode_into(&mut probe)
            .map_err(|e| KvError::Invalid(e.to_string()))?;
        let nonce = next_nonce().map_err(KvError::Invalid)?;
        let (creds, req_ref) = (&self.creds, &req);
        let deadline = Instant::now() + wait;
        let reply = match self.link.exchange(
            &mut |buf| kv_proto::encode_signed_into(req_ref, creds, nonce, buf),
            deadline,
        ) {
            Ok(r) => r,
            Err(e) => {
                self.broken = Some(e.clone());
                return Err(KvError::Transport(e));
            }
        };
        let resp = match Response::decode(&reply) {
            Ok(r) => r,
            Err(e) => return Err(self.protocol(format!("decode reply: {e}"))),
        };
        if !req.answered_by(&resp) {
            return Err(self.protocol(format!(
                "reply {resp:?} does not answer request {}",
                req.req_id()
            )));
        }
        Ok(resp)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::server::{
        block_check, BlockLink, KvAuthContext, KvAuthFn, KvServerConfig, Session, Shared,
    };
    use nvstor_rdma::auth::{verify_signature, AuthError};
    use nvstor_rdma::connection::Piece;
    use nvstor_rdma::kv_proto::{TensorRun, STATUS_EXISTS, STATUS_MISS};
    use nvstor_rdma::transport::TransferError;
    use nvstor_storage::kv_cache::{KvCache, KvCacheConfig};
    use std::cell::RefCell;

    const NS: [u8; NAMESPACE_LEN] = [3; NAMESPACE_LEN];

    /// Client memory shared by the fake link and the test.
    struct Mem(RefCell<Vec<u8>>);

    impl BlockLink for &Mem {
        fn write_scatter(&self, data: &[u8], pieces: &[Piece]) -> Result<(), TransferError> {
            let mut c = self.0.borrow_mut();
            for p in pieces {
                c[p.remote_addr as usize..][..p.len as usize]
                    .copy_from_slice(&data[p.local_offset as usize..][..p.len as usize]);
            }
            Ok(())
        }
        fn read_gather(&self, buf: &mut [u8], pieces: &[Piece]) -> Result<(), TransferError> {
            let c = self.0.borrow();
            for p in pieces {
                buf[p.local_offset as usize..][..p.len as usize]
                    .copy_from_slice(&c[p.remote_addr as usize..][..p.len as usize]);
            }
            Ok(())
        }
    }

    /// The whole path without a NIC: the client's signed bytes are decoded
    /// and answered by a real server session over the in-memory link.
    struct Loopback<'a> {
        shared: &'a Shared,
        session: RefCell<Session>,
        mem: &'a Mem,
    }

    impl Exchange for Loopback<'_> {
        fn exchange(
            &self,
            encode: &mut dyn FnMut(&mut [u8]) -> std::io::Result<usize>,
            _deadline: Instant,
        ) -> Result<Vec<u8>, String> {
            let mut msg = vec![0u8; KV_MSG_SIZE];
            let n = encode(&mut msg).map_err(|e| e.to_string())?;
            let (header, req, body) =
                kv_proto::decode_signed(&msg[..n]).map_err(|e| e.to_string())?;
            let resp = self
                .session
                .borrow_mut()
                .handle(self.shared, &self.mem, &header, req, &body, Instant::now())
                .map_err(|e| format!("{e:?}"))?;
            let mut out = vec![0u8; KV_MSG_SIZE];
            let n = resp.encode_into(&mut out).map_err(|e| e.to_string())?;
            out.truncate(n);
            Ok(out)
        }
    }

    fn shared() -> Shared {
        let cache = KvCache::new(KvCacheConfig {
            slot_bytes: 4096,
            slots: 8,
            shards: 2,
            max_pending: Duration::from_secs(5),
        })
        .expect("cache");
        let auth: KvAuthFn = Arc::new(|c: &KvAuthContext| {
            if verify_signature(
                "s3cret",
                c.header.timestamp,
                c.header.nonce,
                c.body,
                &c.header.signature,
            ) {
                Ok(c.header.access_key.clone())
            } else {
                Err(AuthError::BadSignature)
            }
        });
        Shared::new(cache, auth, KvServerConfig::default()).expect("shared")
    }

    /// Two tensors of 4 blocks: pages of 64 and 32 bytes.
    fn layout() -> Layout {
        Layout {
            layout_id: 1,
            rkey: 1,
            num_blocks: 4,
            tensors: vec![
                TensorRun {
                    base: 0,
                    stride: 64,
                    page: 64,
                },
                TensorRun {
                    base: 256,
                    stride: 32,
                    page: 32,
                },
            ],
        }
    }

    fn payload_of(mem: &Mem, l: &Layout, block: u32) -> Vec<u8> {
        let m = mem.0.borrow();
        l.block_pieces(block)
            .expect("block")
            .flat_map(|(a, n, _)| m[a as usize..][..n as usize].to_vec())
            .collect()
    }

    #[test]
    fn a_client_round_trips_blocks_through_a_server_session() {
        let sh = shared();
        let mem = Mem(RefCell::new((0..512).map(|i| (i * 7) as u8).collect()));
        let link = Loopback {
            shared: &sh,
            session: RefCell::new(Session::new("tenant".into())),
            mem: &mem,
        };
        let mut c = KvClient::new(link, Credentials::new("tenant", "s3cret"));
        let l = layout();
        c.register_layout(l.clone()).expect("layout");
        assert_eq!(
            c.register_layout(l.clone()),
            Err(KvError::Refused(kv_proto::STATUS_BAD_LAYOUT))
        );
        let key = Key::new(&[9; 36]).expect("key");
        let payload = payload_of(&mem, &l, 1);
        let check = block_check(&NS, key.as_bytes(), &payload);
        assert_eq!(
            c.put(
                NS,
                1,
                1000,
                vec![PutEntry {
                    key,
                    block: 1,
                    check
                }]
            )
            .expect("put"),
            vec![STATUS_OK]
        );
        assert_eq!(
            c.put(
                NS,
                1,
                1000,
                vec![PutEntry {
                    key,
                    block: 1,
                    check
                }]
            )
            .expect("put"),
            vec![STATUS_EXISTS]
        );
        let other = Key::new(&[8; 36]).expect("key");
        assert_eq!(
            c.head(NS, vec![key, other]).expect("head"),
            vec![STATUS_OK, STATUS_MISS]
        );
        // GET into block 3 and verify the landed bytes against the check.
        let r = c
            .get(NS, 1, 1000, vec![GetEntry { key, block: 3 }])
            .expect("get");
        assert_eq!(r[0].status, STATUS_OK);
        let landed = payload_of(&mem, &l, 3);
        assert_eq!(landed, payload);
        assert_eq!(block_check(&NS, key.as_bytes(), &landed), r[0].check);
        // A refused request comes back as Refused, not a protocol error.
        assert_eq!(
            c.get(NS, 77, 1000, vec![GetEntry { key, block: 0 }]),
            Err(KvError::Refused(kv_proto::STATUS_BAD_LAYOUT))
        );
    }

    /// Answers every request with a fixed reply.
    struct Canned(Response);
    impl Exchange for Canned {
        fn exchange(
            &self,
            encode: &mut dyn FnMut(&mut [u8]) -> std::io::Result<usize>,
            _deadline: Instant,
        ) -> Result<Vec<u8>, String> {
            let mut msg = vec![0u8; KV_MSG_SIZE];
            encode(&mut msg).map_err(|e| e.to_string())?;
            let mut out = vec![0u8; KV_MSG_SIZE];
            let n = self.0.encode_into(&mut out).expect("encode");
            out.truncate(n);
            Ok(out)
        }
    }

    fn k(n: u8) -> Key {
        Key::new(&[n; 36]).expect("key")
    }

    #[test]
    fn a_reply_that_does_not_answer_the_request_breaks_the_client() {
        // One status for two keys.
        let mut c = KvClient::new(
            Canned(Response::Head {
                req_id: 1,
                statuses: vec![STATUS_OK],
            }),
            Credentials::new("k", "s"),
        );
        assert!(matches!(
            c.head(NS, vec![k(1), k(2)]),
            Err(KvError::Protocol(_))
        ));
        assert!(c.is_broken());
        assert!(
            matches!(c.head(NS, vec![k(1)]), Err(KvError::Broken(_))),
            "no further use"
        );
        // Right kind and count, wrong req_id.
        let mut c = KvClient::new(
            Canned(Response::Head {
                req_id: 99,
                statuses: vec![STATUS_OK],
            }),
            Credentials::new("k", "s"),
        );
        assert!(matches!(c.head(NS, vec![k(1)]), Err(KvError::Protocol(_))));
        // A whole-request error for another req_id.
        let mut c = KvClient::new(
            Canned(Response::Err {
                req_id: 99,
                status: 4,
            }),
            Credentials::new("k", "s"),
        );
        assert!(matches!(c.head(NS, vec![k(1)]), Err(KvError::Protocol(_))));
        // Another kind.
        let mut c = KvClient::new(
            Canned(Response::Head {
                req_id: 1,
                statuses: vec![STATUS_OK],
            }),
            Credentials::new("k", "s"),
        );
        assert!(matches!(
            c.get(
                NS,
                1,
                10,
                vec![GetEntry {
                    key: k(1),
                    block: 0
                }]
            ),
            Err(KvError::Protocol(_))
        ));
    }

    #[test]
    fn unknown_statuses_are_errors() {
        let mut c = KvClient::new(
            Canned(Response::Head {
                req_id: 1,
                statuses: vec![200],
            }),
            Credentials::new("k", "s"),
        );
        assert_eq!(c.head(NS, vec![k(1)]).expect("head"), vec![STATUS_ERROR]);
        let mut c = KvClient::new(
            Canned(Response::Err {
                req_id: 1,
                status: STATUS_OK,
            }),
            Credentials::new("k", "s"),
        );
        assert_eq!(
            c.head(NS, vec![k(1)]),
            Err(KvError::Refused(STATUS_ERROR)),
            "a refusal claiming success"
        );
        assert!(!c.is_broken(), "a refusal leaves the client usable");
    }

    #[test]
    fn a_bad_request_is_invalid_and_leaves_the_client_usable() {
        let mut c = KvClient::new(
            Canned(Response::Head {
                req_id: 2,
                statuses: vec![STATUS_OK],
            }),
            Credentials::new("k", "s"),
        );
        assert!(
            matches!(c.head(NS, vec![]), Err(KvError::Invalid(_))),
            "an empty batch"
        );
        assert!(
            matches!(
                c.get(
                    NS,
                    1,
                    0,
                    vec![GetEntry {
                        key: k(1),
                        block: 0
                    }]
                ),
                Err(KvError::Invalid(_))
            ),
            "a zero budget"
        );
        assert!(!c.is_broken());
    }

    #[test]
    fn a_transport_failure_breaks_the_client() {
        struct Down;
        impl Exchange for Down {
            fn exchange(
                &self,
                _: &mut dyn FnMut(&mut [u8]) -> std::io::Result<usize>,
                _: Instant,
            ) -> Result<Vec<u8>, String> {
                Err("peer not found".into())
            }
        }
        let mut c = KvClient::new(Down, Credentials::new("k", "s"));
        assert_eq!(
            c.head(NS, vec![k(1)]),
            Err(KvError::Transport("peer not found".into()))
        );
        assert!(matches!(c.head(NS, vec![k(1)]), Err(KvError::Broken(_))));
    }

    #[test]
    fn requests_after_a_refusal_keep_their_sequence() {
        let sh = shared();
        let mem = Mem(RefCell::new(vec![0; 512]));
        let link = Loopback {
            shared: &sh,
            session: RefCell::new(Session::new("tenant".into())),
            mem: &mem,
        };
        let mut c = KvClient::new(link, Credentials::new("tenant", "s3cret"));
        c.register_layout(layout()).expect("layout");
        // Refused (unknown layout), then a valid request still goes through.
        assert!(matches!(
            c.get(
                NS,
                9,
                100,
                vec![GetEntry {
                    key: k(1),
                    block: 0
                }]
            ),
            Err(KvError::Refused(_))
        ));
        assert_eq!(c.head(NS, vec![k(1)]).expect("head"), vec![STATUS_MISS]);
    }
}

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

//! Who may fetch from a `serve`, and how many at once.
//!
//! A fetch is one session: every connection it opens (one per worker,
//! on every rail, plus the one that holds its place) presents the access
//! key `nvstor-peer:<session id>`, a random id the fetch picks once. The
//! key is covered by the bootstrap MAC, so a session cannot be borrowed
//! without the secret.
//!
//! [`FetcherCap`] counts sessions, not connections or QPs: a session is
//! admitted while fewer than `max` others hold a connection, and every
//! connection of an admitted session is admitted after it. Over the cap
//! a new session's first connection is answered BUSY in the bootstrap,
//! after authentication and before the server creates any QP, CQ or
//! memory registration for it (`nvstor_rdma::bootstrap::SessionAdmitter`),
//! so a refused fetcher costs the source's NIC no connection state. A
//! session's place is freed when its last connection is torn down, by a
//! clean disconnect or when the transport's liveness watcher sees its
//! bootstrap stream close (a killed fetcher).

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use nvstor_rdma::auth::{verify_signature, AuthError, AuthFn};
use nvstor_rdma::bootstrap::{
    HandshakeAuth, HandshakeVerifier, PeerRole, SessionAdmitter, SessionClaim,
};
use parking_lot::Mutex;
use tracing::{debug, info};

/// Access key prefix of every nvstor-peer connection.
pub const ACCESS_KEY: &str = "nvstor-peer";
/// Hex digits in a session id.
const SESSION_HEX: usize = 32;

/// A fresh random session id.
pub fn new_session_id() -> std::io::Result<String> {
    let mut r = [0u8; SESSION_HEX / 2];
    // SAFETY: the pointer and length name `r`.
    if unsafe { libc::getrandom(r.as_mut_ptr().cast(), r.len(), 0) } != r.len() as isize {
        return Err(std::io::Error::last_os_error());
    }
    Ok(r.iter().map(|b| format!("{b:02x}")).collect())
}

/// The access key a session's connections present.
pub fn session_key(id: &str) -> String {
    format!("{ACCESS_KEY}:{id}")
}

/// The session id in an access key, if it is a well-formed session key.
pub fn session_of(access_key: &str) -> Option<&str> {
    let id = access_key.strip_prefix(ACCESS_KEY)?.strip_prefix(':')?;
    (id.len() == SESSION_HEX && id.bytes().all(|b| b.is_ascii_hexdigit())).then_some(id)
}

/// Bootstrap verifier: a well-formed session key whose MAC was made with
/// `secret` is a `Client`; anything else is refused before any verbs
/// object exists.
pub fn verifier(secret: &str) -> HandshakeVerifier {
    let secret = secret.to_string();
    Arc::new(move |a: &HandshakeAuth| {
        if session_of(a.access_key).is_some() && a.verify(&secret) {
            PeerRole::Client
        } else {
            PeerRole::Denied
        }
    })
}

/// Request authenticator: every request is signed with `secret` under a
/// session key.
pub fn request_auth(secret: &str) -> AuthFn {
    let secret = secret.to_string();
    Arc::new(move |ctx| {
        if session_of(&ctx.header.access_key).is_none() {
            return Err(AuthError::UnknownKey);
        }
        if !verify_signature(
            &secret,
            ctx.header.timestamp,
            ctx.header.nonce,
            ctx.body,
            &ctx.header.signature,
        ) {
            return Err(AuthError::BadSignature);
        }
        Ok(ACCESS_KEY.to_string())
    })
}

/// The cap on concurrently served fetch sessions; shared by every rail.
pub struct FetcherCap {
    max: usize,
    /// Connections held, per admitted session.
    sessions: Mutex<HashMap<String, usize>>,
    refused: AtomicU64,
}

/// One connection's hold on its session's place.
struct Claim {
    cap: Arc<FetcherCap>,
    session: String,
}

impl Drop for Claim {
    fn drop(&mut self) {
        let mut s = self.cap.sessions.lock();
        if let Some(n) = s.get_mut(&self.session) {
            *n = n.saturating_sub(1);
            if *n == 0 {
                s.remove(&self.session);
                info!(
                    active = s.len(),
                    max = self.cap.max,
                    "fetcher finished or went away"
                );
            }
        }
    }
}

impl FetcherCap {
    /// At most `max` sessions at once (at least 1).
    pub fn new(max: usize) -> Arc<Self> {
        Arc::new(Self {
            max: max.max(1),
            sessions: Mutex::new(HashMap::new()),
            refused: AtomicU64::new(0),
        })
    }

    pub fn max(&self) -> usize {
        self.max
    }

    /// Sessions holding at least one connection.
    pub fn active(&self) -> usize {
        self.sessions.lock().len()
    }

    /// Connections refused with BUSY so far.
    pub fn refused(&self) -> u64 {
        self.refused.load(Ordering::Relaxed)
    }

    /// Admit one connection presenting `access_key`: always for a session
    /// already admitted, otherwise only while fewer than `max` sessions
    /// are. `None` means BUSY. The claim frees the place when dropped.
    pub fn admit(self: &Arc<Self>, access_key: &str) -> Option<SessionClaim> {
        let session = session_of(access_key)?;
        let mut s = self.sessions.lock();
        let active = s.len();
        match s.get_mut(session) {
            Some(n) => *n += 1,
            None if active < self.max => {
                s.insert(session.to_string(), 1);
                info!(active = s.len(), max = self.max, "fetcher admitted");
            }
            None => {
                let refused = self.refused.fetch_add(1, Ordering::Relaxed) + 1;
                debug!(active, max = self.max, refused, "fetcher refused: BUSY");
                return None;
            }
        }
        Some(Box::new(Claim {
            cap: Arc::clone(self),
            session: session.to_string(),
        }))
    }

    /// The bootstrap hook (`RdmaTransport::set_session_admitter`).
    pub fn admitter(self: &Arc<Self>) -> SessionAdmitter {
        let cap = Arc::clone(self);
        Arc::new(move |access_key: &str, _role| cap.admit(access_key))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use nvstor_rdma::auth::Credentials;
    use nvstor_rdma::bootstrap::{self, BootstrapError, Endpoint, Hello};
    use std::io::Write;
    use std::os::unix::net::UnixStream;

    const SECRET: &str = "peer-secret-0123456789";

    fn key() -> String {
        session_key(&new_session_id().expect("id"))
    }

    #[test]
    fn session_keys() {
        let k = key();
        assert_eq!(session_of(&k).map(str::len), Some(SESSION_HEX));
        assert_ne!(k, key(), "ids are random");
        for bad in [
            "nvstor-peer",
            "nvstor-peer:",
            "nvstor-peer:xyz",
            "other:0123456789abcdef0123456789abcdef",
            &format!("{ACCESS_KEY}:{}", "g".repeat(SESSION_HEX)),
        ] {
            assert_eq!(session_of(bad), None, "{bad}");
        }
        assert!(k.len() <= nvstor_rdma::s3_rdma_proto::MAX_ACCESS_KEY_LEN);
    }

    #[test]
    fn the_cap_counts_sessions_not_connections() {
        let cap = FetcherCap::new(2);
        let (a, b, c) = (key(), key(), key());
        // Session a opens many connections (workers on several rails).
        let a_conns: Vec<_> = (0..40).map(|_| cap.admit(&a).expect("a")).collect();
        let b1 = cap.admit(&b).expect("b");
        assert_eq!(cap.active(), 2);
        // The K+1th session is refused; the admitted ones still get more.
        assert!(cap.admit(&c).is_none(), "third session must be BUSY");
        assert_eq!(cap.refused(), 1);
        let b2 = cap.admit(&b).expect("b's second connection");
        // b's place is held until its last connection goes.
        drop(b1);
        assert!(cap.admit(&c).is_none());
        drop(b2);
        assert_eq!(cap.active(), 1);
        let c1 = cap.admit(&c).expect("c gets b's place");
        drop(a_conns);
        drop(c1);
        assert_eq!(cap.active(), 0);
        assert!(
            cap.admit("nvstor-peer").is_none(),
            "no session, no admission"
        );
    }

    /// One bootstrap handshake as the server runs it with the real
    /// verifier and cap; returns the server's result and the client's.
    fn handshake(
        cap: &Arc<FetcherCap>,
        creds: Credentials,
    ) -> (
        Result<Option<SessionClaim>, BootstrapError>,
        Result<Endpoint, BootstrapError>,
        bool,
    ) {
        let (mut server, mut client) = UnixStream::pair().expect("socketpair");
        let ep = Endpoint {
            lid: 0,
            gid: [1; 16],
            qpns: vec![10, 11],
        };
        let c = std::thread::spawn(move || -> Result<Endpoint, BootstrapError> {
            let (_, raw) = Hello::read_from(&mut client)?;
            client.write_all(&bootstrap::encode_auth(&raw, &creds, &ep)?)?;
            bootstrap::read_verdict(&mut client, 2)
        });
        let hello = Hello::new(4, 0, [2; 16]).expect("hello");
        let mut built = false;
        let r = bootstrap::serve_admitted(
            &mut server,
            &hello,
            Some(&verifier(SECRET)),
            Some(&cap.admitter()),
            |_, remote| {
                built = true;
                Ok((
                    Endpoint {
                        lid: 0,
                        gid: [2; 16],
                        qpns: remote.qpns.iter().map(|q| q + 100).collect(),
                    },
                    (),
                ))
            },
        );
        drop(server);
        (
            r.map(|(_, claim, ())| claim),
            c.join().expect("client"),
            built,
        )
    }

    // K fetchers are served; the K+1th is told BUSY in the bootstrap
    // before the server builds anything; a fetcher that goes away (its
    // connections torn down, cleanly or not, drops their claims) frees
    // its place for the next.
    #[test]
    fn the_k_plus_first_fetcher_is_told_busy_before_any_qp_exists() {
        let cap = FetcherCap::new(2);
        let creds = |k: &str| Credentials::new(k, SECRET);
        let (a, b, c) = (key(), key(), key());
        let mut held = Vec::new();
        for k in [&a, &b, &a] {
            let (server, client, built) = handshake(&cap, creds(k));
            held.push(server.expect("admitted").expect("claim"));
            assert!(client.is_ok() && built);
        }
        let (server, client, built) = handshake(&cap, creds(&c));
        assert!(matches!(server, Err(BootstrapError::Busy)), "{server:?}");
        let refused = client.expect_err("client sees BUSY");
        assert!(bootstrap::is_busy(&refused.to_string()), "{refused}");
        assert!(!built, "no QP may be built for a refused fetcher");

        // Wrong secret, or a key without a session: REJECT, not BUSY.
        for bad in [
            Credentials::new(&c, "not-the-secret-at-all"),
            creds(ACCESS_KEY),
        ] {
            let (server, client, built) = handshake(&cap, bad);
            assert!(matches!(server, Err(BootstrapError::Denied)), "{server:?}");
            assert!(
                matches!(
                    client,
                    Err(BootstrapError::Rejected(
                        bootstrap::RejectReason::Credentials
                    ))
                ),
                "{client:?}"
            );
            assert!(!built);
        }

        // b goes away: its only connection is torn down.
        held.remove(1);
        let (server, client, _) = handshake(&cap, creds(&c));
        let c_claim = server.expect("c admitted").expect("claim");
        assert!(client.is_ok());
        assert_eq!(cap.active(), 2);
        drop((held, c_claim));
        assert_eq!(cap.active(), 0);
    }
}

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

//! Request authentication for the s3-rdma / s3-tcp / RPC transports.
//!
//! Before this, all three transports bound `0.0.0.0`, carried no principal,
//! and served any Put/Get/Delete/Head to anyone who could reach the port —
//! across tenants. This module supplies the wire-level half of the fix: an
//! HMAC-SHA256 signature over each request, keyed by the caller's IAM
//! secret, that identifies the principal making it.
//!
//! ## What is signed
//!
//! The signature covers `timestamp || nonce || body`, where `body` is the
//! exact encoded request bytes. Signing the serialized body directly —
//! rather than a re-derived "canonical string" — removes a whole class of
//! bugs where signer and verifier disagree about canonicalization, and it
//! binds every field of the request (key, offsets, lengths) at once. An
//! attacker cannot alter the key of a signed PUT, or redirect a GET's
//! target address, without invalidating the signature.
//!
//! ## Replay
//!
//! A signature alone would be replayable: capture one valid DELETE and
//! resend it forever. Two bounds prevent that:
//!   * a timestamp that must be within [`MAX_CLOCK_SKEW`] of now, which
//!     caps how long any captured request stays useful, and
//!   * a nonce cache covering that window, so a request cannot be replayed
//!     even *inside* it.
//!
//! The cache only has to hold one skew-window of nonces, so it is bounded
//! by request rate rather than by uptime; see [`ReplayGuard`] for how it
//! stays cheap at hundreds of thousands of requests per second.
//!
//! ## Layering
//!
//! This crate is transport-only and deliberately does NOT depend on
//! nvstor-iam. Verification is supplied by the server as an [`AuthFn`] —
//! the same callback shape already used for `WriteFn`/`ReadFn`/etc. —
//! which resolves an access key to a secret and a principal, and then
//! applies policy. Keeping IAM out of the transport keeps the dependency
//! graph acyclic and lets the RPC path reuse the identical mechanism.

use std::collections::{BTreeMap, HashSet};
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use hmac::{Hmac, Mac};
use parking_lot::Mutex;
use sha2::Sha256;

type HmacSha256 = Hmac<Sha256>;

/// How far a request's timestamp may be from local time. Bounds the window
/// in which a captured request could be replayed if the nonce cache were
/// bypassed, and absorbs ordinary NTP drift between client and server.
pub const MAX_CLOCK_SKEW: Duration = Duration::from_secs(300);

/// Length of the signature in bytes (HMAC-SHA256 output).
pub const SIGNATURE_LEN: usize = 32;

/// Errors that reject a request before it reaches any storage callback.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum AuthError {
    /// No credentials presented at all.
    Missing,
    /// Access key is not known / user disabled.
    UnknownKey,
    /// Signature did not match.
    BadSignature,
    /// Timestamp outside the accepted window.
    Expired,
    /// Nonce already used within the window.
    Replay,
    /// Authenticated, but policy denies this action on this resource.
    Denied,
}

impl AuthError {
    /// Short, non-revealing reason for the wire. Deliberately coarse: a
    /// caller learns that it failed, not whether the access key exists,
    /// which would otherwise make the endpoint a key-enumeration oracle.
    pub fn wire_reason(&self) -> &'static str {
        match self {
            AuthError::Denied => "access denied",
            _ => "authentication failed",
        }
    }
}

impl std::fmt::Display for AuthError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let s = match self {
            AuthError::Missing => "missing credentials",
            AuthError::UnknownKey => "unknown access key",
            AuthError::BadSignature => "signature mismatch",
            AuthError::Expired => "timestamp outside accepted window",
            AuthError::Replay => "nonce already used (replay)",
            AuthError::Denied => "access denied by policy",
        };
        f.write_str(s)
    }
}

/// The credentials a client signs with. Mirrors the IAM user record.
#[derive(Debug, Clone)]
pub struct Credentials {
    pub access_key: String,
    pub secret_key: String,
}

impl Credentials {
    pub fn new(access_key: impl Into<String>, secret_key: impl Into<String>) -> Self {
        Self {
            access_key: access_key.into(),
            secret_key: secret_key.into(),
        }
    }
}

/// The signed preamble carried by every request.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AuthHeader {
    pub access_key: String,
    /// Unix seconds when the client signed.
    pub timestamp: u64,
    /// Unique per request within the skew window.
    pub nonce: u64,
    pub signature: [u8; SIGNATURE_LEN],
}

/// What the server needs to decide on a request. Passed to the [`AuthFn`].
pub struct AuthContext<'a> {
    pub header: &'a AuthHeader,
    /// The encoded request body the signature must cover.
    pub body: &'a [u8],
    /// Coarse action name, e.g. "s3:PutObject" — for policy evaluation.
    pub action: &'a str,
    /// Object key the action targets ("" when not key-scoped).
    pub key: &'a str,
}

/// Server-supplied verifier: resolve the access key, check the signature,
/// then apply policy. Returns the authenticated principal on success.
///
/// Implemented in nvstor-server against nvstor-iam; this crate only
/// defines the shape so the transport stays IAM-agnostic.
pub type AuthFn = Arc<dyn Fn(&AuthContext) -> Result<String, AuthError> + Send + Sync>;

/// Current unix time in seconds.
pub fn now_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}

/// Allocate nonces across all transports in this process. An OS-random starting
/// value separates independent client processes and restarts; the shared counter
/// prevents overlap between concurrent transport instances in this process.
/// Entropy failure and exhaustion fail the request without sending it.
pub fn next_nonce() -> Result<u64, String> {
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::sync::OnceLock;
    static NEXT: OnceLock<Result<AtomicU64, String>> = OnceLock::new();
    let counter = NEXT
        .get_or_init(|| {
            let mut seed = [0u8; 8];
            getrandom::getrandom(&mut seed)
                .map_err(|error| format!("nonce entropy unavailable: {error}"))?;
            Ok(AtomicU64::new(u64::from_ne_bytes(seed)))
        })
        .as_ref()
        .map_err(Clone::clone)?;
    counter
        .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |value| {
            value.checked_add(1)
        })
        .map_err(|_| "request nonce space exhausted".to_string())
}

/// Compute the signature over `timestamp || nonce || body`.
///
/// Note the length prefix on `body`: without it, two different
/// (timestamp, nonce, body) splits could produce identical input and
/// therefore identical signatures, letting a signature be lifted from one
/// request onto another.
pub fn sign(secret: &str, timestamp: u64, nonce: u64, body: &[u8]) -> [u8; SIGNATURE_LEN] {
    let mut mac = <HmacSha256 as Mac>::new_from_slice(secret.as_bytes())
        .expect("invariant: HMAC accepts keys of any length");
    mac.update(&timestamp.to_be_bytes());
    mac.update(&nonce.to_be_bytes());
    mac.update(&(body.len() as u64).to_be_bytes());
    mac.update(body);
    let out = mac.finalize().into_bytes();
    let mut sig = [0u8; SIGNATURE_LEN];
    sig.copy_from_slice(&out[..SIGNATURE_LEN]);
    sig
}

/// Verify a signature in constant time.
///
/// `hmac`'s `verify_slice` is constant-time; a naive `==` on the byte
/// arrays would leak, through timing, how many leading bytes were correct,
/// which is enough to forge a signature byte by byte.
pub fn verify_signature(
    secret: &str,
    timestamp: u64,
    nonce: u64,
    body: &[u8],
    signature: &[u8; SIGNATURE_LEN],
) -> bool {
    let mut mac = <HmacSha256 as Mac>::new_from_slice(secret.as_bytes())
        .expect("invariant: HMAC accepts keys of any length");
    mac.update(&timestamp.to_be_bytes());
    mac.update(&nonce.to_be_bytes());
    mac.update(&(body.len() as u64).to_be_bytes());
    mac.update(body);
    mac.verify_slice(signature).is_ok()
}

/// Build a signed header for `body`.
pub fn sign_request(creds: &Credentials, nonce: u64, body: &[u8]) -> AuthHeader {
    let timestamp = now_secs();
    AuthHeader {
        access_key: creds.access_key.clone(),
        timestamp,
        nonce,
        signature: sign(&creds.secret_key, timestamp, nonce, body),
    }
}

/// Rejects replayed requests within the clock-skew window.
///
/// A nonce is forgotten once its timestamp falls outside the window: a
/// request that old is already refused by the timestamp check, so
/// remembering it buys nothing. That keeps the cache proportional to
/// request rate over one window rather than growing for the life of the
/// process.
///
/// A replay is a resend of the exact signed bytes, so it carries the same
/// signed timestamp as the original. Nonces are therefore kept per signed
/// timestamp second, and a request is checked only against the set for its
/// own second; expiry drops a whole second's set at once. The sets are
/// split over [`REPLAY_SHARDS`] independently locked shards by nonce.
/// One set holding every nonce of the window behind one process-wide lock
/// would, at high request rates, reach tens of millions of entries, and
/// each time it doubled the rehash would hold the lock for seconds,
/// stalling every request of every rail. Here a set holds one second of
/// one shard's requests, so growing it is cheap, and requests on different
/// shards never contend.
///
/// Access keys are stored as a 64-bit keyed hash (SipHash with a random
/// key per guard), not as strings: no allocation per request. Two keys
/// collide with probability 2^-64, and without the random key nobody can
/// pick a key that collides with another tenant's.
pub struct ReplayGuard {
    shards: Box<[Mutex<ReplayShard>]>,
    keys: std::collections::hash_map::RandomState,
    window: Duration,
}

/// Lock shards of a [`ReplayGuard`] (a power of two).
pub const REPLAY_SHARDS: usize = 64;
const _: () = assert!(REPLAY_SHARDS.is_power_of_two());

#[derive(Default)]
struct ReplayShard {
    /// (access-key hash, nonce) pairs by their signed timestamp second.
    by_second: BTreeMap<u64, HashSet<(u64, u64)>>,
    /// The latest second whose set this shard has forgotten. A request
    /// dated at or before it is refused, whatever its caller's clock says:
    /// a caller that read the clock just before a newer request pruned
    /// that second, or a wall clock stepped back, would otherwise accept a
    /// replay of a nonce that is no longer remembered.
    forgotten_through: Option<u64>,
}

impl ReplayShard {
    /// Forget every second whose timestamp `now` would refuse: those older
    /// than `now - window`.
    fn prune(&mut self, now: u64, window: u64) {
        while let Some(entry) = self.by_second.first_entry() {
            if entry.key().saturating_add(window) >= now {
                break;
            }
            let second = *entry.key();
            entry.remove();
            self.forgotten_through = Some(self.forgotten_through.map_or(second, |f| f.max(second)));
        }
    }

    /// Whether this shard has already forgotten `second`'s nonces.
    fn forgot(&self, second: u64) -> bool {
        self.forgotten_through.is_some_and(|f| second <= f)
    }

    fn len(&self) -> usize {
        self.by_second.values().map(HashSet::len).sum()
    }
}

/// One replay guard for every server in this process. A multi-NIC node
/// runs one S3-over-RDMA server per rail; with a guard per server, a
/// request captured on one rail could be replayed once on every other.
pub fn process_replay_guard() -> Arc<ReplayGuard> {
    static GUARD: std::sync::OnceLock<Arc<ReplayGuard>> = std::sync::OnceLock::new();
    Arc::clone(GUARD.get_or_init(|| Arc::new(ReplayGuard::new())))
}

impl ReplayGuard {
    pub fn new() -> Self {
        Self::with_window(MAX_CLOCK_SKEW)
    }

    pub fn with_window(window: Duration) -> Self {
        Self {
            shards: (0..REPLAY_SHARDS)
                .map(|_| Mutex::new(ReplayShard::default()))
                .collect(),
            keys: std::collections::hash_map::RandomState::new(),
            window,
        }
    }

    /// Check the timestamp is fresh and the nonce unused, recording it.
    ///
    /// Nonces are scoped per access key so one tenant cannot deny another
    /// service by burning nonce values it guesses they will use.
    pub fn check_and_record(
        &self,
        access_key: &str,
        timestamp: u64,
        nonce: u64,
    ) -> Result<(), AuthError> {
        self.check_and_record_at(access_key, timestamp, nonce, now_secs())
    }

    fn check_and_record_at(
        &self,
        access_key: &str,
        timestamp: u64,
        nonce: u64,
        now: u64,
    ) -> Result<(), AuthError> {
        use std::hash::BuildHasher;
        let window = self.window.as_secs();
        // Compare in both directions so a client running ahead of the
        // server is treated the same as one running behind.
        let skew = now.abs_diff(timestamp);
        if skew > window {
            return Err(AuthError::Expired);
        }
        let key = self.keys.hash_one(access_key);
        let mut shard = self.shards[Self::shard_index(key, nonce)].lock();
        shard.prune(now, window);
        if shard.forgot(timestamp) {
            return Err(AuthError::Expired);
        }
        if !shard
            .by_second
            .entry(timestamp)
            .or_default()
            .insert((key, nonce))
        {
            return Err(AuthError::Replay);
        }
        Ok(())
    }

    /// The shard holding (`key`, `nonce`). Nonces of one client are
    /// consecutive; mixing spreads them evenly.
    fn shard_index(key: u64, nonce: u64) -> usize {
        ((nonce ^ key).wrapping_mul(0x9E37_79B9_7F4A_7C15) >> (64 - REPLAY_SHARDS.trailing_zeros()))
            as usize
    }

    /// Number of nonces currently remembered. Prunes every shard first: a
    /// request prunes only its own shard.
    pub fn tracked(&self) -> usize {
        self.tracked_at(now_secs())
    }

    fn tracked_at(&self, now: u64) -> usize {
        let window = self.window.as_secs();
        self.shards
            .iter()
            .map(|s| {
                let mut s = s.lock();
                s.prune(now, window);
                s.len()
            })
            .sum()
    }
}

impl Default for ReplayGuard {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    impl ReplayGuard {
        /// Nonces held now, without pruning.
        fn held(&self) -> usize {
            self.shards.iter().map(|s| s.lock().len()).sum()
        }

        fn shard_of(&self, access_key: &str, nonce: u64) -> usize {
            use std::hash::BuildHasher;
            Self::shard_index(self.keys.hash_one(access_key), nonce)
        }
    }

    fn creds() -> Credentials {
        Credentials::new("AKIATEST", "supersecret")
    }

    #[test]
    fn signature_round_trips() {
        let c = creds();
        let body = b"OP_PUT key=photos/cat.jpg len=4096";
        let h = sign_request(&c, 1, body);
        assert_eq!(h.access_key, "AKIATEST");
        assert!(verify_signature(
            &c.secret_key,
            h.timestamp,
            h.nonce,
            body,
            &h.signature
        ));
    }

    #[test]
    fn wrong_secret_fails() {
        let body = b"body";
        let h = sign_request(&creds(), 1, body);
        assert!(!verify_signature(
            "wrong",
            h.timestamp,
            h.nonce,
            body,
            &h.signature
        ));
    }

    // The signature must bind the whole request. Tampering with any
    // byte of the body — the object key, a length, an address — has to
    // invalidate it, otherwise a captured signature could be re-pointed at
    // another tenant's key.
    #[test]
    fn tampered_body_fails() {
        let c = creds();
        let body = b"OP_GET key=tenant-a/secret";
        let h = sign_request(&c, 1, body);

        let tampered = b"OP_GET key=tenant-b/secret";
        assert!(!verify_signature(
            &c.secret_key,
            h.timestamp,
            h.nonce,
            tampered,
            &h.signature
        ));
    }

    #[test]
    fn tampered_timestamp_or_nonce_fails() {
        let c = creds();
        let body = b"body";
        let h = sign_request(&c, 7, body);
        assert!(!verify_signature(
            &c.secret_key,
            h.timestamp + 1,
            h.nonce,
            body,
            &h.signature
        ));
        assert!(!verify_signature(
            &c.secret_key,
            h.timestamp,
            h.nonce + 1,
            body,
            &h.signature
        ));
    }

    // The length prefix exists so a signature can't be lifted between two
    // requests that would otherwise concatenate to the same bytes.
    #[test]
    fn body_length_is_bound_into_the_signature() {
        let secret = "s";
        let a = sign(secret, 1, 1, b"abcdef");
        let b = sign(secret, 1, 1, b"abc");
        assert_ne!(a, b);
    }

    #[test]
    fn replay_guard_rejects_reused_nonce() {
        let g = ReplayGuard::new();
        let ts = now_secs();
        assert_eq!(g.check_and_record("AK", ts, 42), Ok(()));
        assert_eq!(g.check_and_record("AK", ts, 42), Err(AuthError::Replay));
        // A different nonce is fine.
        assert_eq!(g.check_and_record("AK", ts, 43), Ok(()));
    }

    #[test]
    fn replay_guard_scopes_nonces_per_access_key() {
        let g = ReplayGuard::new();
        let ts = now_secs();
        assert_eq!(g.check_and_record("AK1", ts, 1), Ok(()));
        // Same nonce under a different key must not collide.
        assert_eq!(g.check_and_record("AK2", ts, 1), Ok(()));
    }

    #[test]
    fn replay_guard_rejects_stale_and_future_timestamps() {
        let g = ReplayGuard::with_window(Duration::from_secs(60));
        let now = now_secs();
        assert_eq!(
            g.check_and_record("AK", now - 3600, 1),
            Err(AuthError::Expired)
        );
        assert_eq!(
            g.check_and_record("AK", now + 3600, 2),
            Err(AuthError::Expired)
        );
        assert_eq!(g.check_and_record("AK", now, 3), Ok(()));
    }

    // The cache must stay bounded by request rate over one window, not
    // grow for the life of the process.
    #[test]
    fn replay_guard_prunes_entries_outside_the_window() {
        let g = ReplayGuard::with_window(Duration::from_secs(1));
        let now = now_secs();
        for n in 0..50 {
            let _ = g.check_and_record("AK", now, n);
        }
        assert_eq!(g.held(), 50);

        std::thread::sleep(Duration::from_millis(2100));
        // A request prunes its own shard: every stale nonce there goes.
        let shard = g.shard_of("AK", 999);
        let stale_elsewhere = g.held() - g.shards[shard].lock().len();
        let _ = g.check_and_record("AK", now_secs(), 999);
        assert_eq!(
            g.shards[shard].lock().len(),
            1,
            "stale nonces must be pruned by a request"
        );
        assert_eq!(
            g.held(),
            stale_elsewhere + 1,
            "other shards wait for their own requests"
        );
        assert_eq!(g.tracked(), 1);
    }

    // An entry is remembered exactly as long as its timestamp could still
    // be accepted, measured from the timestamp (not from arrival), so a
    // request signed early in the window cannot be replayed late in it.
    #[test]
    fn replay_guard_remembers_until_the_timestamp_expires() {
        let g = ReplayGuard::with_window(Duration::from_secs(60));
        let t = 1_000_000;
        assert_eq!(g.check_and_record_at("AK", t - 50, 1, t), Ok(()));
        assert_eq!(g.check_and_record_at("AK", t + 50, 2, t), Ok(()));
        // 10 s later: both still inside the window, both still replays.
        assert_eq!(
            g.check_and_record_at("AK", t - 50, 1, t + 10),
            Err(AuthError::Replay)
        );
        assert_eq!(
            g.check_and_record_at("AK", t + 50, 2, t + 10),
            Err(AuthError::Replay)
        );
        // At t+11 the first one's timestamp is refused, so it is forgotten;
        // the future-dated one is kept until t+110.
        assert_eq!(
            g.check_and_record_at("AK", t - 50, 1, t + 11),
            Err(AuthError::Expired)
        );
        assert_eq!(g.check_and_record_at("AK", t + 50, 3, t + 11), Ok(()));
        assert_eq!(g.tracked_at(t + 11), 2);
        assert_eq!(
            g.check_and_record_at("AK", t + 50, 2, t + 110),
            Err(AuthError::Replay)
        );
        assert_eq!(g.check_and_record_at("AK", t + 111, 4, t + 111), Ok(()));
        assert_eq!(
            g.tracked_at(t + 111),
            1,
            "everything dated t+50 expired at t+111"
        );
    }

    // Pruning must not rescan every remembered nonce per request.
    // 200k requests inside one window: with a full retain per request that
    // is ~2e10 entry visits (minutes); with expiry buckets it is linear.
    #[test]
    fn replay_guard_cost_per_request_does_not_grow_with_the_cache() {
        let g = ReplayGuard::with_window(Duration::from_secs(300));
        let t = 2_000_000;
        let started = std::time::Instant::now();
        for n in 0..200_000u64 {
            assert_eq!(
                g.check_and_record_at("AK", t + n / 1000, n, t + n / 1000),
                Ok(())
            );
        }
        assert_eq!(g.tracked_at(t + 199), 200_000);
        assert!(
            started.elapsed() < Duration::from_secs(10),
            "200k checks took {:?}: pruning is not amortized O(1)",
            started.elapsed()
        );
        // One request past everything's window drops its whole shard in
        // one go, without help from tracked().
        let shard = g.shard_of("AK", u64::MAX);
        let before = g.shards[shard].lock().len();
        assert!(before > 1000, "shard held {before}");
        assert_eq!(
            g.check_and_record_at("AK", t + 1000, u64::MAX, t + 1000),
            Ok(())
        );
        assert_eq!(g.shards[shard].lock().len(), 1);
        assert_eq!(g.tracked_at(t + 1000), 1);
    }

    // A request that read the clock just before another request on its
    // shard pruned its second must not re-insert a forgotten nonce: that
    // would accept a replay at the edge of the window. A wall clock
    // stepped back is the same case.
    #[test]
    fn replay_guard_refuses_a_second_already_forgotten() {
        let g = ReplayGuard::with_window(Duration::from_secs(60));
        let t = 5_000_000;
        assert_eq!(g.check_and_record_at("AK", t, 1, t), Ok(()));
        // A fresh request on the same shard prunes second t.
        let n = (2..)
            .find(|&n| g.shard_of("AK", n) == g.shard_of("AK", 1))
            .expect("a nonce on the same shard");
        assert_eq!(g.check_and_record_at("AK", t + 61, n, t + 61), Ok(()));
        // The replay still holds now = t + 60, inside its window.
        assert_eq!(
            g.check_and_record_at("AK", t, 1, t + 60),
            Err(AuthError::Expired)
        );
        assert_eq!(
            g.check_and_record_at("AK", t, 1, t),
            Err(AuthError::Expired),
            "clock stepped back"
        );
        // A later second this shard still remembers is unaffected.
        let m = (n + 1..)
            .find(|&m| g.shard_of("AK", m) == g.shard_of("AK", 1))
            .expect("a nonce on the same shard");
        assert_eq!(g.check_and_record_at("AK", t + 1, m, t + 61), Ok(()));
        assert_eq!(
            g.check_and_record_at("AK", t + 61, n, t + 61),
            Err(AuthError::Replay)
        );
    }

    // A replay resends the signed bytes, timestamp included, so a nonce is
    // checked within its own signed second only. A reused nonce under a
    // different timestamp is a different signed message, not a replay.
    #[test]
    fn replay_guard_checks_a_nonce_within_its_signed_second() {
        let g = ReplayGuard::with_window(Duration::from_secs(60));
        let t = 3_000_000;
        assert_eq!(g.check_and_record_at("AK", t, 7, t), Ok(()));
        assert_eq!(
            g.check_and_record_at("AK", t, 7, t + 30),
            Err(AuthError::Replay)
        );
        assert_eq!(g.check_and_record_at("AK", t + 1, 7, t + 30), Ok(()));
    }

    // One client's consecutive nonces spread over every shard, so a busy
    // client does not funnel through one lock, and no per-second set grows
    // past a small fraction of the traffic.
    #[test]
    fn replay_guard_spreads_one_clients_nonces_over_every_shard() {
        let g = ReplayGuard::with_window(Duration::from_secs(60));
        let t = 4_000_000;
        let n = 64 * 1024u64;
        for nonce in 0..n {
            assert_eq!(g.check_and_record_at("AK", t, nonce, t), Ok(()));
        }
        let sizes: Vec<usize> = g.shards.iter().map(|s| s.lock().len()).collect();
        let fair = n as usize / REPLAY_SHARDS;
        assert!(
            sizes.iter().all(|&k| k > fair / 2 && k < fair * 2),
            "uneven shards: {sizes:?}"
        );
    }

    // Many threads, overlapping nonces: every nonce is accepted exactly once.
    #[test]
    fn replay_guard_accepts_each_nonce_once_under_concurrency() {
        let g = Arc::new(ReplayGuard::with_window(Duration::from_secs(60)));
        let ts = now_secs();
        let accepted: usize = (0..8)
            .map(|_| {
                let g = Arc::clone(&g);
                std::thread::spawn(move || {
                    (0..20_000u64)
                        .filter(|&n| g.check_and_record("AK", ts, n).is_ok())
                        .count()
                })
            })
            .collect::<Vec<_>>()
            .into_iter()
            .map(|h| h.join().expect("join"))
            .sum();
        assert_eq!(accepted, 20_000);
        assert_eq!(g.tracked(), 20_000);
    }

    // Every server in the process shares one guard, so a nonce burned
    // on one rail is burned on all of them.
    #[test]
    fn process_replay_guard_is_shared() {
        let a = process_replay_guard();
        let b = process_replay_guard();
        assert!(Arc::ptr_eq(&a, &b));
        let ts = now_secs();
        let nonce = u64::from_ne_bytes(*b"rail-27!");
        assert_eq!(a.check_and_record("AK-27", ts, nonce), Ok(()));
        assert_eq!(
            b.check_and_record("AK-27", ts, nonce),
            Err(AuthError::Replay)
        );
    }

    #[test]
    fn error_reasons_do_not_leak_key_existence() {
        // Unknown key and bad signature must be indistinguishable on the
        // wire, or the endpoint becomes a key-enumeration oracle.
        assert_eq!(
            AuthError::UnknownKey.wire_reason(),
            AuthError::BadSignature.wire_reason()
        );
    }
}

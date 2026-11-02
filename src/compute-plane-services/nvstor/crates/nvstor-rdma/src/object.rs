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

//! Storage callback contract shared by the s3-rdma and s3-tcp servers.
//!
//! The transports stay storage-agnostic: nvstor-server hands each
//! server three closures over its `PmObjectStore`. Reads are ranged
//! and land directly in a caller-owned buffer (a registered RDMA pool
//! slot, or a socket staging buffer), so no server path ever holds a
//! whole object in memory.

use std::io;
use std::sync::Arc;

/// Outcome of one ranged read: bytes placed in the buffer (short only
/// at end of object), the object's total size, and the generation the
/// bytes came from — all from one pinned version of the object.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct ObjectRead {
    pub len: usize,
    pub size: u64,
    /// Opaque version token; changes whenever the object is replaced.
    /// Never 0, so 0 can mean "any generation" on the wire.
    pub generation: u128,
}

/// Store `bytes` under `key`, replacing any previous generation.
pub type WriteFn = Arc<dyn Fn(&str, &[u8]) -> Result<u64, String> + Send + Sync>;

/// Read up to `buf.len()` bytes of `key` at `offset` into `buf`. An
/// empty `buf` is a stat (HEAD). `ErrorKind::NotFound` maps to the
/// wire's NOT_FOUND status; every other error is reported as ERROR.
///
/// `ObjectRead::len` must be exactly the number of bytes written into
/// `buf[..len]` by this call. The S3-over-RDMA server sends `buf[..len]`
/// from a pool slot that is not cleared between requests, so a `len`
/// counting bytes the call did not write would send a previous request's
/// data. A short read returns the short
/// count (or zero-fills up to the count it returns).
pub type ReadFn = Arc<dyn Fn(&str, u64, &mut [u8]) -> io::Result<ObjectRead> + Send + Sync>;

/// Store `size` bytes under `key`, pulled from `fill` in chunks (each
/// call must fill its buffer completely). The streaming counterpart of
/// `WriteFn` for transports whose payload arrives on a socket: a
/// multi-GiB PUT never has to sit in server memory.
pub type PutStreamFn = Arc<
    dyn Fn(&str, u64, &mut dyn FnMut(&mut [u8]) -> io::Result<()>) -> io::Result<u64> + Send + Sync,
>;

/// Delete `key`. Idempotent.
pub type DeleteFn = Arc<dyn Fn(&str) -> Result<(), String> + Send + Sync>;

/// Wire generation value meaning "don't check the generation".
pub const ANY_GENERATION: u128 = 0;

/// Why a ranged GET failed, typed so a hydration driver can choose:
/// retry the range, restart the object, back off, or give up.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum GetError {
    /// The key does not exist.
    NotFound,
    /// The object was replaced since the first range; restart the whole
    /// object, not just this range.
    GenerationChanged,
    /// The server answered NO_SLOT: no free pool slot or worker (any
    /// verb, not only GET); retry after a pause.
    Busy,
    /// The bytes received do not match the server's CRC-32C.
    Corrupt { expected: u32, actual: u32 },
    /// The server reported a failure (status code).
    Server(u8),
    /// The request never completed: connection, framing, or protocol.
    Transport(String),
}

impl GetError {
    /// Worth retrying the same range on the same or a new connection.
    pub fn is_retryable(&self) -> bool {
        matches!(
            self,
            GetError::Busy | GetError::Corrupt { .. } | GetError::Transport(_)
        )
    }
}

impl std::fmt::Display for GetError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            GetError::NotFound => write!(f, "GET: not found"),
            GetError::GenerationChanged => write!(f, "GET: object replaced during transfer"),
            GetError::Busy => write!(f, "server busy: no free slot or worker"),
            GetError::Corrupt { expected, actual } => write!(
                f,
                "GET: crc32c mismatch (expected {expected:08x}, got {actual:08x})"
            ),
            GetError::Server(status) => write!(f, "GET failed: status={status}"),
            GetError::Transport(e) => write!(f, "GET transport: {e}"),
        }
    }
}

impl std::error::Error for GetError {}

impl From<GetError> for String {
    fn from(e: GetError) -> String {
        e.to_string()
    }
}

/// Verify `data` against the server's CRC-32C for it.
pub(crate) fn verify_range(data: &[u8], expected: u32) -> Result<(), GetError> {
    let actual = nvstor_core::checksum::crc32c(data);
    if actual == expected {
        Ok(())
    } else {
        Err(GetError::Corrupt { expected, actual })
    }
}

/// In-memory store implementing the callback contract, for transport
/// tests. Each PUT gets a fresh generation, like a PmStore `replace`.
#[cfg(test)]
pub(crate) fn mem_store() -> (WriteFn, ReadFn, DeleteFn) {
    use std::collections::HashMap;
    use std::sync::atomic::{AtomicU64, Ordering};
    type Map = parking_lot::RwLock<HashMap<String, (u128, Arc<Vec<u8>>)>>;
    let map: Arc<Map> = Arc::default();
    let next_gen = Arc::new(AtomicU64::new(1));
    let (w, r, d) = (Arc::clone(&map), Arc::clone(&map), map);
    let write_fn: WriteFn = Arc::new(move |key, data| {
        let generation = next_gen.fetch_add(1, Ordering::Relaxed) as u128;
        w.write()
            .insert(key.to_string(), (generation, Arc::new(data.to_vec())));
        Ok(data.len() as u64)
    });
    let read_fn: ReadFn = Arc::new(move |key, offset, buf| {
        let (generation, data) = r
            .read()
            .get(key)
            .cloned()
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "no such key"))?;
        let start = usize::try_from(offset)
            .unwrap_or(usize::MAX)
            .min(data.len());
        let len = (data.len() - start).min(buf.len());
        buf[..len].copy_from_slice(&data[start..start + len]);
        Ok(ObjectRead {
            len,
            size: data.len() as u64,
            generation,
        })
    });
    let delete_fn: DeleteFn = Arc::new(move |key| {
        d.write().remove(key);
        Ok(())
    });
    (write_fn, read_fn, delete_fn)
}

/// Adapt a slice-based `WriteFn` into a `PutStreamFn` by buffering the
/// payload, for the in-memory test store.
#[cfg(test)]
pub(crate) fn buffered_put(write_fn: WriteFn) -> PutStreamFn {
    Arc::new(move |key, size, fill| {
        let mut buf = vec![0u8; usize::try_from(size).map_err(io::Error::other)?];
        fill(&mut buf)?;
        write_fn(key, &buf).map_err(io::Error::other)
    })
}

/// Longest single retry pause [`backoff`] returns.
pub const MAX_BACKOFF: std::time::Duration = std::time::Duration::from_secs(2);

/// Pause before retry `attempt` (1 = first retry): `base` doubled per
/// attempt, capped at [`MAX_BACKOFF`], then jittered uniformly into
/// [half, full] so clients that were refused together do not all come
/// back together.
pub fn backoff(base: std::time::Duration, attempt: u32) -> std::time::Duration {
    jittered(
        base.saturating_mul(1u32 << attempt.saturating_sub(1).min(20))
            .min(MAX_BACKOFF),
    )
}

/// `full` jittered uniformly into [half, full].
pub fn jittered(full: std::time::Duration) -> std::time::Duration {
    let half = full / 2;
    half + half.mul_f64(jitter())
}

/// A uniform value in [0, 1) from a per-thread stream seeded from the
/// clock and the thread, so threads backing off together spread out.
pub fn unit_random() -> f64 {
    jitter()
}

/// A uniform value in [0, 1) from a per-thread splitmix64 stream seeded
/// from the clock and the thread.
fn jitter() -> f64 {
    use std::cell::Cell;
    thread_local! {
        static STATE: Cell<u64> = Cell::new({
            let t = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_nanos() as u64).unwrap_or(0);
            let id = {
                use std::hash::{Hash, Hasher};
                let mut h = std::collections::hash_map::DefaultHasher::new();
                std::thread::current().id().hash(&mut h);
                h.finish()
            };
            t ^ id.rotate_left(17)
        });
    }
    STATE.with(|s| {
        let mut x = s.get().wrapping_add(0x9E37_79B9_7F4A_7C15);
        s.set(x);
        x = (x ^ (x >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
        x = (x ^ (x >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
        x ^= x >> 31;
        (x >> 11) as f64 / (1u64 << 53) as f64
    })
}

/// Run `op` until it does not answer [`GetError::Busy`], at most
/// `retries` more times, pausing [`backoff`]`(base, attempt)` between
/// tries. Every other result is returned at once. For control verbs a
/// busy server answers NO_SLOT (`s3_rdma_proto::ADMIT_WAIT`).
pub fn retry_busy<T>(
    retries: u32,
    base: std::time::Duration,
    mut op: impl FnMut() -> Result<T, GetError>,
) -> Result<T, GetError> {
    let mut attempt = 0;
    loop {
        match op() {
            Err(GetError::Busy) if attempt < retries => {
                attempt += 1;
                std::thread::sleep(backoff(base, attempt));
            }
            r => return r,
        }
    }
}

#[cfg(test)]
mod tests {
    #[test]
    fn backoff_doubles_is_capped_and_jittered() {
        let base = std::time::Duration::from_millis(100);
        for attempt in 1..=6 {
            let full = (base * (1 << (attempt - 1))).min(MAX_BACKOFF);
            for _ in 0..50 {
                let d = backoff(base, attempt);
                assert!(
                    d >= full / 2 && d <= full,
                    "attempt {attempt}: {d:?} outside [{:?}, {full:?}]",
                    full / 2
                );
            }
        }
        let spread: std::collections::HashSet<_> = (0..20).map(|_| backoff(base, 3)).collect();
        assert!(spread.len() > 1, "jittered, not constant");
        assert!(backoff(base, 60) <= MAX_BACKOFF);
    }

    // A busy answer is retried, a bounded number of times;
    // any other answer ends it.
    #[test]
    fn retry_busy_retries_only_busy_and_is_bounded() {
        let ms = std::time::Duration::from_millis(1);
        let mut left = 2;
        let r = retry_busy(3, ms, || {
            if left > 0 {
                left -= 1;
                Err(GetError::Busy)
            } else {
                Ok(7)
            }
        });
        assert_eq!(r, Ok(7));
        let mut calls = 0;
        assert_eq!(
            retry_busy(3, ms, || -> Result<(), _> {
                calls += 1;
                Err(GetError::Busy)
            }),
            Err(GetError::Busy)
        );
        assert_eq!(calls, 4);
        calls = 0;
        assert_eq!(
            retry_busy(3, ms, || -> Result<(), _> {
                calls += 1;
                Err(GetError::NotFound)
            }),
            Err(GetError::NotFound)
        );
        assert_eq!(calls, 1);
    }

    use super::*;

    #[test]
    fn verify_range_accepts_match_and_flags_corruption() {
        let data = b"123456789";
        assert_eq!(verify_range(data, 0xE306_9283), Ok(()));
        assert_eq!(
            verify_range(b"123456780", 0xE306_9283),
            Err(GetError::Corrupt {
                expected: 0xE306_9283,
                actual: nvstor_core::checksum::crc32c(b"123456780")
            })
        );
    }

    #[test]
    fn retryable_classification() {
        assert!(GetError::Busy.is_retryable());
        assert!(GetError::Transport("reset".into()).is_retryable());
        assert!(GetError::Corrupt {
            expected: 1,
            actual: 2
        }
        .is_retryable());
        assert!(!GetError::NotFound.is_retryable());
        assert!(!GetError::GenerationChanged.is_retryable());
        assert!(!GetError::Server(2).is_retryable());
    }

    #[test]
    fn error_converts_to_string_for_legacy_callers() {
        let s: String = GetError::GenerationChanged.into();
        assert!(s.contains("replaced"));
    }
}

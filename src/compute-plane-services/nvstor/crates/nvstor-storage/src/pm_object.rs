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

//! PmStore-backed object KV for the RDMA/TCP object transfer paths.
//!
//! Keys map deterministically to UUIDs using the first 16 bytes of SHA256. PUT
//! writes a fresh generation and publishes it atomically; GET pins one complete
//! generation. Multiple protocol adapters for the same directory must share the
//! same store instance, including its lifecycle locks and cached file handles.
//!
//! SyncMode controls local persistence. This type and the production fast-path
//! callbacks do not replicate objects, create a checksum manifest, or encrypt
//! payloads. Those services require an explicit layer above this store. It also
//! does not implement content deduplication, versioning, or erasure coding.

use std::io;
use std::sync::Arc;

use sha2::{Digest, Sha256};
use uuid::Uuid;

use crate::pm::{Generation, PmStore, VersionedRead};
use crate::SyncMode;

/// Key→object store backed by `PmStore`. The store is the unit of
/// durability; flushes go through `PmStore::flush`.
pub struct PmObjectStore {
    pm: Arc<dyn PmStore>,
}

impl PmObjectStore {
    pub fn new(pm: Arc<dyn PmStore>) -> Self {
        Self { pm }
    }

    /// Map an S3 key to the volume UUID we'll store its bytes in.
    /// First 16 bytes of `sha256(key)` so the map is deterministic
    /// (no persistent lookup table needed) and uniform (no
    /// collisions in practice for our key space).
    fn key_uuid(key: &str) -> Uuid {
        let mut hasher = Sha256::new();
        hasher.update(key.as_bytes());
        let digest = hasher.finalize();
        let mut bytes = [0u8; 16];
        bytes.copy_from_slice(&digest[..16]);
        Uuid::from_bytes(bytes)
    }

    /// Atomically replace the object with a complete new generation.
    /// Readers see one complete value. Returns the byte count written.
    pub fn put(&self, key: &str, data: &[u8], sync: SyncMode) -> io::Result<u64> {
        let id = Self::key_uuid(key);
        self.pm.replace(id, data, sync)?;
        Ok(data.len() as u64)
    }

    /// `put`, streaming: the `size` bytes of the new generation are pulled
    /// from `fill` in chunks (each call fills its buffer completely), so a
    /// multi-GiB object never sits in memory. Atomic like `put`: if `fill`
    /// fails the previous generation stays published.
    pub fn put_from(
        &self,
        key: &str,
        size: u64,
        sync: SyncMode,
        fill: &mut dyn FnMut(&mut [u8]) -> io::Result<()>,
    ) -> io::Result<u64> {
        self.pm
            .replace_from(Self::key_uuid(key), size, sync, fill)?;
        Ok(size)
    }

    /// Read the full object bytes from one pinned generation.
    pub fn get(&self, key: &str) -> io::Result<Vec<u8>> {
        self.pm.read_all(Self::key_uuid(key))
    }

    /// Object size and [`Generation`] for `key`, read from one handle.
    pub fn stat(&self, key: &str) -> io::Result<(u64, Generation)> {
        self.pm.stat(Self::key_uuid(key))
    }

    /// Read up to `buf.len()` bytes at `offset` directly into `buf` (no
    /// intermediate allocation — callers pass a registered RDMA slot or a
    /// reusable socket buffer). Returns the byte count (short only at end
    /// of object), the object size, and the [`Generation`] the bytes came
    /// from. An empty `buf` is a stat. A caller
    /// assembling an object from several ranges must see one generation
    /// for every range; a different token means the object was replaced
    /// mid-transfer.
    pub fn read_at(&self, key: &str, offset: u64, buf: &mut [u8]) -> io::Result<VersionedRead> {
        self.pm.read_versioned(Self::key_uuid(key), offset, buf)
    }

    /// Object size for `key`, if it exists.
    pub fn head(&self, key: &str) -> io::Result<u64> {
        let id = Self::key_uuid(key);
        self.pm.size(id)
    }

    /// Delete the object's backing file. Idempotent.
    pub fn delete(&self, key: &str) -> io::Result<()> {
        let id = Self::key_uuid(key);
        match self.pm.delete(id) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(e),
        }
    }

    /// Flush an object's bytes to the configured durability mode.
    /// Used by `CompleteMultipartUpload` semantics — the per-part
    /// `put` can use `SyncMode::None` for speed, then `flush` once
    /// before acking the upload.
    pub fn flush(&self, key: &str, sync: SyncMode) -> io::Result<()> {
        let id = Self::key_uuid(key);
        self.pm.flush(id, sync)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[test]
    fn concurrent_replacements_never_return_mixed_generations() {
        let (_dir, store) = store();
        store.put("object", &vec![1; 8192], SyncMode::Full).unwrap();
        std::thread::scope(|scope| {
            for writer in 1..=4u8 {
                let store = &store;
                scope.spawn(move || {
                    for _ in 0..20 {
                        store
                            .put(
                                "object",
                                &vec![writer; 8192 * writer as usize],
                                SyncMode::None,
                            )
                            .unwrap();
                    }
                });
            }
            for _ in 0..4 {
                let store = &store;
                scope.spawn(move || {
                    for _ in 0..100 {
                        let data = store.get("object").unwrap();
                        let byte = data[0];
                        assert!((1..=4).contains(&byte));
                        assert_eq!(data.len(), 8192 * byte as usize);
                        assert!(data.iter().all(|&value| value == byte));
                    }
                });
            }
        });
        store.put("object", b"", SyncMode::Full).unwrap();
        assert_eq!(store.get("object").unwrap(), b"");
    }

    #[test]
    fn replacement_survives_reopen() {
        let directory = TempDir::new().unwrap();
        {
            let pm = Arc::new(crate::pm::posix::PosixPmStore::new(directory.path()).unwrap());
            let store = PmObjectStore::new(pm);
            store.put("object", b"old", SyncMode::Full).unwrap();
            store.put("object", b"replacement", SyncMode::Full).unwrap();
        }
        let pm = Arc::new(crate::pm::posix::PosixPmStore::new(directory.path()).unwrap());
        let store = PmObjectStore::new(pm);
        assert_eq!(store.get("object").unwrap(), b"replacement");
    }

    fn store() -> (TempDir, PmObjectStore) {
        let dir = TempDir::new().unwrap();
        let pm: Arc<dyn PmStore> =
            Arc::new(crate::pm::posix::PosixPmStore::new(dir.path()).unwrap());
        (dir, PmObjectStore::new(pm))
    }

    #[test]
    fn put_get_round_trip() {
        let (_d, s) = store();
        let payload = vec![0xABu8; 1024 * 1024];
        let n = s.put("foo/bar", &payload, SyncMode::None).unwrap();
        assert_eq!(n, payload.len() as u64);
        let got = s.get("foo/bar").unwrap();
        assert_eq!(got, payload);
    }

    #[test]
    fn head_returns_size() {
        let (_d, s) = store();
        s.put("k", &vec![0u8; 4096], SyncMode::None).unwrap();
        assert_eq!(s.head("k").unwrap(), 4096);
    }

    #[test]
    fn overwrite_shrinks() {
        let (_d, s) = store();
        s.put("k", &vec![0u8; 1024 * 1024], SyncMode::None).unwrap();
        s.put("k", &vec![1u8; 32], SyncMode::None).unwrap();
        let got = s.get("k").unwrap();
        assert_eq!(got.len(), 32);
        assert!(got.iter().all(|&b| b == 1));
    }

    #[test]
    fn delete_then_head_not_found() {
        let (_d, s) = store();
        s.put("k", b"hello", SyncMode::None).unwrap();
        s.delete("k").unwrap();
        // PosixPmStore::size returns NotFound for missing volumes.
        assert!(s.head("k").is_err());
    }

    #[test]
    fn delete_idempotent() {
        let (_d, s) = store();
        s.delete("never-existed").unwrap();
    }

    #[test]
    fn read_at_ranges_reassemble_one_generation() {
        let (_d, s) = store();
        let payload: Vec<u8> = (0..1_000_003u32).map(|i| (i * 31) as u8).collect();
        s.put("obj", &payload, SyncMode::None).unwrap();
        let (size, generation) = s.stat("obj").unwrap();
        assert_eq!(size, payload.len() as u64);
        let mut out = vec![0u8; payload.len()];
        let mut off = 0usize;
        for chunk in out.chunks_mut(65_536) {
            let r = s.read_at("obj", off as u64, chunk).unwrap();
            assert_eq!(r.generation, generation);
            assert_eq!(r.size, size);
            assert_eq!(r.len, chunk.len());
            off += r.len;
        }
        assert_eq!(out, payload);
    }

    #[test]
    fn read_at_end_and_past_end() {
        let (_d, s) = store();
        s.put("obj", b"hello", SyncMode::None).unwrap();
        let mut buf = [0u8; 8];
        assert_eq!(s.read_at("obj", 3, &mut buf).unwrap().len, 2);
        assert_eq!(&buf[..2], b"lo");
        assert_eq!(s.read_at("obj", 5, &mut buf).unwrap().len, 0);
        assert_eq!(s.read_at("obj", 99, &mut buf).unwrap().len, 0);
        // Empty buffer is a stat.
        let stat = s.read_at("obj", 0, &mut []).unwrap();
        assert_eq!((stat.len, stat.size), (0, 5));
        assert_eq!(stat.generation, s.stat("obj").unwrap().1);
    }

    #[test]
    fn read_at_and_stat_missing_key_not_found() {
        let (_d, s) = store();
        let mut buf = [0u8; 4];
        assert_eq!(
            s.read_at("nope", 0, &mut buf).unwrap_err().kind(),
            io::ErrorKind::NotFound
        );
        assert_eq!(s.stat("nope").unwrap_err().kind(), io::ErrorKind::NotFound);
    }

    #[test]
    fn replace_changes_generation() {
        let (_d, s) = store();
        s.put("obj", b"one", SyncMode::None).unwrap();
        let (_, g1) = s.stat("obj").unwrap();
        s.put("obj", b"two", SyncMode::None).unwrap();
        let (_, g2) = s.stat("obj").unwrap();
        assert_ne!(g1, g2);
        let mut buf = [0u8; 3];
        assert_eq!(
            s.read_at("obj", 0, &mut buf).unwrap(),
            VersionedRead {
                len: 3,
                size: 3,
                generation: g2
            }
        );
    }

    #[test]
    fn generation_stable_across_eviction_and_reopen() {
        let directory = TempDir::new().unwrap();
        let open = || {
            let pm = crate::pm::posix::PosixPmStore::new(directory.path())
                .unwrap()
                .with_handle_cap(1);
            PmObjectStore::new(Arc::new(pm))
        };
        let s = open();
        s.put("obj", b"payload", SyncMode::Full).unwrap();
        let (_, g) = s.stat("obj").unwrap();
        // Cap 1: touching another object evicts obj's handle.
        s.put("other", b"x", SyncMode::Full).unwrap();
        s.stat("other").unwrap();
        assert_eq!(s.stat("obj").unwrap().1, g);
        drop(s);
        assert_eq!(open().stat("obj").unwrap().1, g);
    }

    #[test]
    fn concurrent_replace_never_mixes_generations_within_a_read() {
        let (_d, s) = store();
        s.put("obj", &vec![1; 65_536], SyncMode::None).unwrap();
        let seen = parking_lot::Mutex::new(std::collections::HashMap::<Generation, u8>::new());
        std::thread::scope(|scope| {
            for writer in 1..=4u8 {
                let s = &s;
                scope.spawn(move || {
                    for _ in 0..25 {
                        s.put(
                            "obj",
                            &vec![writer; 65_536 * writer as usize],
                            SyncMode::None,
                        )
                        .unwrap();
                    }
                });
            }
            for _ in 0..4 {
                let (s, seen) = (&s, &seen);
                scope.spawn(move || {
                    let mut buf = vec![0u8; 32_768];
                    for i in 0..200u64 {
                        let r = s.read_at("obj", (i % 2) * 32_768, &mut buf).unwrap();
                        let g = r.generation;
                        assert_eq!(r.len, buf.len());
                        // Size and bytes agree: writer w writes 65_536 * w bytes of w.
                        assert_eq!(r.size, 65_536 * buf[0] as u64);
                        let byte = buf[0];
                        assert!(buf.iter().all(|&b| b == byte), "range mixed generations");
                        // One token always names one generation's content.
                        assert_eq!(*seen.lock().entry(g).or_insert(byte), byte);
                    }
                });
            }
        });
    }

    /// Stream `data` into `put_from` through a reader, as a socket would.
    fn put_streamed(s: &PmObjectStore, key: &str, data: &[u8]) -> io::Result<u64> {
        let mut src = data;
        s.put_from(key, data.len() as u64, SyncMode::Full, &mut |buf| {
            io::Read::read_exact(&mut src, buf)
        })
    }

    fn put_from_contract(s: &PmObjectStore) {
        // Larger than REPLACE_CHUNK so several fills are needed.
        let data: Vec<u8> = (0..(crate::pm::REPLACE_CHUNK * 2 + 777) as u32)
            .map(|i| (i.wrapping_mul(2_654_435_761) >> 7) as u8)
            .collect();
        assert_eq!(put_streamed(s, "obj", &data).unwrap(), data.len() as u64);
        assert_eq!(s.get("obj").unwrap(), data);
        let (_, g1) = s.stat("obj").unwrap();

        // A source that dies mid-stream leaves the old generation intact.
        let mut calls = 0;
        let err = s
            .put_from(
                "obj",
                3 * crate::pm::REPLACE_CHUNK as u64,
                SyncMode::Full,
                &mut |buf| {
                    calls += 1;
                    if calls == 2 {
                        return Err(io::Error::new(io::ErrorKind::ConnectionReset, "peer gone"));
                    }
                    buf.fill(0xEE);
                    Ok(())
                },
            )
            .unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::ConnectionReset);
        assert_eq!(s.stat("obj").unwrap().1, g1);
        assert_eq!(s.get("obj").unwrap(), data);

        // Empty object via the streaming path; the source is never called.
        assert_eq!(
            s.put_from("empty", 0, SyncMode::Full, &mut |_| panic!(
                "no bytes to fill"
            ))
            .unwrap(),
            0
        );
        assert_eq!(s.get("empty").unwrap(), b"");

        // Replace via put_from gets a new generation.
        put_streamed(s, "obj", b"small").unwrap();
        assert_ne!(s.stat("obj").unwrap().1, g1);
        assert_eq!(s.get("obj").unwrap(), b"small");
    }

    #[test]
    fn put_from_streams_and_is_atomic() {
        let (dir, s) = store();
        put_from_contract(&s);
        // No staged files left behind by the failed stream.
        let leftovers: Vec<_> = std::fs::read_dir(dir.path())
            .unwrap()
            .filter_map(|e| e.ok())
            .filter(|e| e.file_name().to_string_lossy().starts_with(".pending-"))
            .collect();
        assert!(leftovers.is_empty(), "{leftovers:?}");
    }

    #[cfg(all(target_os = "linux", feature = "io-uring"))]
    #[test]
    fn iouring_put_from_streams_and_is_atomic() {
        let dir = TempDir::new().unwrap();
        let pm = crate::pm::iouring::IoUringPmStore::new(dir.path()).unwrap();
        put_from_contract(&PmObjectStore::new(Arc::new(pm)));
    }

    #[cfg(all(target_os = "linux", feature = "io-uring"))]
    #[test]
    fn iouring_read_at_and_generation() {
        let dir = TempDir::new().unwrap();
        let pm = crate::pm::iouring::IoUringPmStore::new(dir.path()).unwrap();
        let s = PmObjectStore::new(Arc::new(pm));
        let payload: Vec<u8> = (0..300_000u32).map(|i| i as u8).collect();
        s.put("obj", &payload, SyncMode::None).unwrap();
        let (size, g1) = s.stat("obj").unwrap();
        assert_eq!(size, payload.len() as u64);
        let mut buf = vec![0u8; 100_000];
        assert_eq!(
            s.read_at("obj", 250_000, &mut buf).unwrap(),
            VersionedRead {
                len: 50_000,
                size: 300_000,
                generation: g1
            }
        );
        assert_eq!(&buf[..50_000], &payload[250_000..]);
        s.put("obj", b"new", SyncMode::None).unwrap();
        assert_ne!(s.stat("obj").unwrap().1, g1);
    }

    #[test]
    fn key_uuid_deterministic() {
        assert_eq!(
            PmObjectStore::key_uuid("foo/bar"),
            PmObjectStore::key_uuid("foo/bar"),
        );
        assert_ne!(
            PmObjectStore::key_uuid("foo/bar"),
            PmObjectStore::key_uuid("foo/baz"),
        );
    }
}

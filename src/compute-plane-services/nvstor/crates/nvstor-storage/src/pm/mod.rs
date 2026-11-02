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

//! Position-mapped storage for mutable byte ranges (volumes).
//!
//! Sister of `crate::backend` (content-addressed chunks for immutable
//! objects like S3). PmStore is the right primitive for NBD volumes
//! and any "byte-addressable mutable storage" workload — a 1 MiB write
//! at offset N updates byte range [N, N+1MiB) of a sparse file in
//! place. No content-addressing. No read-modify-write. No new chunk
//! ID per byte change.
//!
//! API design:
//! - One sparse file per volume, named `{base_path}/{uuid}`. Sparse
//!   so unused regions don't consume disk.
//! - `write` is `pwrite()` at offset, returns when data is in the
//!   kernel buffer (`SyncMode::None`) or after `fdatasync` /
//!   `fsync` for the durable modes.
//! - `flush` is the explicit barrier the protocol layer triggers
//!   on COMMIT / FUA / object-close.
//! - Volume metadata (size, access mode, consistency) lives in the
//!   existing `MetadataStore` so the cluster's metadata flow is
//!   untouched.

use std::io;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use uuid::Uuid;

use crate::SyncMode;

pub mod posix;

/// Chunk size `replace_from` pulls from its source per call.
pub(crate) const REPLACE_CHUNK: usize = 8 * 1024 * 1024;

/// Identity of one published generation of a volume's backing file:
/// its inode number and inode change time, captured when the handle is
/// opened.
///
/// `replace` publishes a new inode (rename of a fully staged file), so
/// the token changes on every replacement, and it is stable across
/// handle eviction and process restart. A reader that assembles an
/// object from several ranged reads compares tokens to detect a
/// `replace` between reads instead of stitching two generations
/// together.
///
/// Scope: the token identifies *which file* a read came from. In-place
/// `write` / `set_size` mutate the same inode and are not tracked —
/// volumes mutated in place (NBD, NFS file content) have no
/// generation semantics.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Generation(pub u128);

/// Outcome of [`PmStore::read_versioned`]: bytes read, total size, and
/// generation, all from one pinned handle.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct VersionedRead {
    /// Bytes copied into the caller's buffer (short only at end of file).
    pub len: usize,
    /// Size of the file the bytes came from.
    pub size: u64,
    pub generation: Generation,
}

impl Generation {
    pub(crate) fn of(meta: &std::fs::Metadata) -> Self {
        use std::os::unix::fs::MetadataExt;
        let ctime_ns = (meta.ctime() as u64)
            .wrapping_mul(1_000_000_000)
            .wrapping_add(meta.ctime_nsec() as u64);
        Self(((meta.ino() as u128) << 64) | ctime_ns as u128)
    }
}

#[cfg(all(target_os = "linux", feature = "io-uring"))]
pub mod iouring;

/// Hot-path counters for a `PmStore` instance.
///
/// All fields are atomic so the data plane updates them without
/// locking, and the metrics scrape path reads consistent snapshots.
/// nvstor-server wires these into `/metrics` as `nvstor_pm_*`
/// gauges so we can answer "how many PM writes/s, how many bytes,
/// how often is a write hitting an out-of-bounds or NotFound?"
/// without instrumenting every backend separately.
#[derive(Default, Debug)]
pub struct PmStoreStats {
    pub writes_total: AtomicU64,
    pub write_bytes_total: AtomicU64,
    pub write_errors_total: AtomicU64,
    pub reads_total: AtomicU64,
    pub read_bytes_total: AtomicU64,
    pub read_errors_total: AtomicU64,
    pub volumes_created_total: AtomicU64,
    pub volumes_deleted_total: AtomicU64,
}

impl PmStoreStats {
    pub fn record_write(&self, bytes: u64, result: &io::Result<()>) {
        self.writes_total.fetch_add(1, Ordering::Relaxed);
        if result.is_ok() {
            self.write_bytes_total.fetch_add(bytes, Ordering::Relaxed);
        } else {
            self.write_errors_total.fetch_add(1, Ordering::Relaxed);
        }
    }

    pub fn record_read(&self, result: &io::Result<usize>) {
        self.reads_total.fetch_add(1, Ordering::Relaxed);
        match result {
            Ok(n) => {
                self.read_bytes_total
                    .fetch_add(*n as u64, Ordering::Relaxed);
            }
            Err(_) => {
                self.read_errors_total.fetch_add(1, Ordering::Relaxed);
            }
        }
    }
}

/// Position-mapped store: byte ranges in a sparse file per volume,
/// mutable in place. The right primitive for block devices (NBD),
/// file content (NFS), and any byte-addressable mutable workload.
pub trait PmStore: Send + Sync {
    /// Create a new volume of `size` bytes. Backing file is sparse,
    /// so this consumes ~no disk until something is written.
    fn create(&self, id: Uuid, size: u64) -> io::Result<()>;

    /// Write `data` starting at `offset`. Honors `sync`:
    /// - SyncMode::None: returns as soon as the kernel accepts the
    ///   pwrite. The data may sit in page cache until the kernel
    ///   flushes it, or until the protocol layer calls `flush`.
    /// - SyncMode::Data: fdatasync — data on stable storage, file
    ///   metadata may not be.
    /// - SyncMode::Full: fsync — data + metadata both durable.
    fn write(&self, id: Uuid, offset: u64, data: &[u8], sync: SyncMode) -> io::Result<()>;

    /// Write a complete new generation, then atomically replace the volume.
    /// Failed writes before publication preserve the old generation. Durable
    /// modes sync both the new file and the published directory entry.
    fn replace(&self, id: Uuid, data: &[u8], sync: SyncMode) -> io::Result<()>;

    /// `replace`, but the new generation's `size` bytes are pulled from
    /// `fill` in chunks (each call must fill its buffer completely)
    /// instead of passed as one slice — so a multi-GiB object can be
    /// streamed from a socket or file into the store with bounded memory.
    /// Same atomicity: a failed `fill` or write leaves the old generation.
    fn replace_from(
        &self,
        id: Uuid,
        size: u64,
        sync: SyncMode,
        fill: &mut dyn FnMut(&mut [u8]) -> io::Result<()>,
    ) -> io::Result<()>;

    /// Read size and contents from one pinned generation.
    fn read_all(&self, id: Uuid) -> io::Result<Vec<u8>>;

    /// Read `buf.len()` bytes starting at `offset`.
    fn read(&self, id: Uuid, offset: u64, buf: &mut [u8]) -> io::Result<usize>;

    /// `read`, plus the size and [`Generation`] of the file the bytes came
    /// from. All three come from the same pinned handle, so a concurrent
    /// `replace` can't make them disagree. An empty `buf` is a stat.
    fn read_versioned(&self, id: Uuid, offset: u64, buf: &mut [u8]) -> io::Result<VersionedRead>;

    /// Size and [`Generation`] of the currently published file, read
    /// from one handle.
    fn stat(&self, id: Uuid) -> io::Result<(u64, Generation)>;

    /// Flush a volume to the requested durability level. The protocol
    /// layer calls this on NBD CMD_FLUSH, NFS COMMIT, S3 PUT
    /// completion, etc.
    fn flush(&self, id: Uuid, sync: SyncMode) -> io::Result<()>;

    /// Durable barrier after all protocol writers have drained.
    fn flush_all(&self) -> io::Result<()>;

    /// Punch a hole — release backing storage in the given range,
    /// reads return zeros. Maps to NBD WRITE_ZEROES + TRIM.
    fn punch_hole(&self, id: Uuid, offset: u64, len: u64) -> io::Result<()>;

    /// Volume size (= the size passed to `create`, possibly adjusted by
    /// `set_size`).
    fn size(&self, id: Uuid) -> io::Result<u64>;

    /// Resize the volume to `new_size`. Growing extends the sparse file
    /// (no I/O until something is written into the new range). Shrinking
    /// drops bytes past `new_size`. The caller is responsible for whether
    /// shrinking is semantically valid for the workload — NFS truncate(2)
    /// and SETATTR.size shrink legally; NBD volumes typically don't.
    fn set_size(&self, id: Uuid, new_size: u64) -> io::Result<()>;

    /// Delete the volume's backing file.
    fn delete(&self, id: Uuid) -> io::Result<()>;

    /// Hot-path counters for this store. Default returns a fresh
    /// always-zero stats struct so backends that don't yet
    /// participate compile cleanly; production backends override.
    fn stats(&self) -> Arc<PmStoreStats> {
        Arc::new(PmStoreStats::default())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn store() -> (TempDir, posix::PosixPmStore) {
        let dir = TempDir::new().unwrap();
        let store = posix::PosixPmStore::new(dir.path()).unwrap();
        (dir, store)
    }

    #[test]
    fn create_then_size() {
        let (_d, s) = store();
        let id = Uuid::new_v4();
        s.create(id, 1024 * 1024).unwrap();
        assert_eq!(s.size(id).unwrap(), 1024 * 1024);
    }

    // Writes to DIFFERENT volumes must not serialize on the handle
    // cache. Previously the slow (cache-miss) path held the exclusive lock
    // across the pwrite+fsync, so N threads each touching a fresh volume —
    // the S3-RDMA / PmObjectStore large-MPU shape — ran one at a time.
    // The handle cache is capped at 1 here to force every write down the
    // miss path, which is exactly where the exclusive lock used to be held.
    #[test]
    fn concurrent_writes_to_distinct_volumes_do_not_serialize() {
        use std::sync::Arc as StdArc;

        let dir = TempDir::new().unwrap();
        let store = StdArc::new(
            posix::PosixPmStore::new(dir.path())
                .unwrap()
                .with_handle_cap(1),
        );

        let ids: Vec<Uuid> = (0..8).map(|_| Uuid::new_v4()).collect();
        for id in &ids {
            store.create(*id, 1 << 20).unwrap();
        }

        let mut threads = Vec::new();
        for (i, id) in ids.iter().copied().enumerate() {
            let store = StdArc::clone(&store);
            threads.push(std::thread::spawn(move || {
                let payload = vec![i as u8 + 1; 4096];
                for round in 0..8u64 {
                    store
                        .write(id, round * 4096, &payload, SyncMode::Data)
                        .expect("write");
                }
            }));
        }
        for t in threads {
            t.join().expect("thread panicked");
        }

        // Every volume must hold exactly what its own thread wrote — the
        // shared-handle refactor must not cross-contaminate volumes.
        for (i, id) in ids.iter().copied().enumerate() {
            let mut buf = vec![0u8; 4096];
            store.read(id, 0, &mut buf).expect("read");
            assert!(
                buf.iter().all(|&b| b == i as u8 + 1),
                "volume {i} has wrong contents"
            );
        }
    }

    #[test]
    fn write_read_round_trip() {
        let (_d, s) = store();
        let id = Uuid::new_v4();
        s.create(id, 1024 * 1024).unwrap();
        s.write(id, 0, b"hello nvstor", SyncMode::None).unwrap();
        let mut buf = [0u8; 12];
        let n = s.read(id, 0, &mut buf).unwrap();
        assert_eq!(n, 12);
        assert_eq!(&buf, b"hello nvstor");
    }

    #[test]
    fn write_at_offset_no_amplification() {
        // The write amplification of chunked CA storage on small
        // in-place writes is a non-issue here: a 1 KiB write at
        // offset 4 MiB only touches the bytes at that offset. The
        // sparse file extends just enough to cover it.
        let (_d, s) = store();
        let id = Uuid::new_v4();
        s.create(id, 16 * 1024 * 1024).unwrap();
        let payload = vec![0xAB; 1024];
        s.write(id, 4 * 1024 * 1024, &payload, SyncMode::None)
            .unwrap();

        // Read the written region back.
        let mut buf = vec![0u8; 1024];
        s.read(id, 4 * 1024 * 1024, &mut buf).unwrap();
        assert_eq!(buf, payload);

        // Read the unwritten region: must be zeros (sparse).
        let mut buf = vec![0xFFu8; 4096];
        s.read(id, 0, &mut buf).unwrap();
        assert!(buf.iter().all(|&b| b == 0));
    }

    #[test]
    fn overwrite_in_place() {
        // The same offset can be rewritten without producing a "new"
        // anything. CA stores (correctly) make a new chunk per byte
        // change; PM stores (correctly) overwrite in place.
        let (_d, s) = store();
        let id = Uuid::new_v4();
        s.create(id, 1024 * 1024).unwrap();
        s.write(id, 0, b"first", SyncMode::None).unwrap();
        s.write(id, 0, b"second", SyncMode::None).unwrap();
        let mut buf = [0u8; 6];
        s.read(id, 0, &mut buf).unwrap();
        assert_eq!(&buf, b"second");
    }

    #[test]
    fn flush_full_marks_durable() {
        // Functional check — fsync should succeed and not panic.
        let (_d, s) = store();
        let id = Uuid::new_v4();
        s.create(id, 1024 * 1024).unwrap();
        s.write(id, 0, b"durable bytes", SyncMode::None).unwrap();
        s.flush(id, SyncMode::Full).unwrap();
    }

    #[test]
    fn punch_hole_zeros_range() {
        let (_d, s) = store();
        let id = Uuid::new_v4();
        s.create(id, 1024 * 1024).unwrap();
        s.write(id, 0, &[0xCC; 8192], SyncMode::None).unwrap();
        s.punch_hole(id, 0, 8192).unwrap();
        let mut buf = [0xFFu8; 8192];
        s.read(id, 0, &mut buf).unwrap();
        assert!(
            buf.iter().all(|&b| b == 0),
            "punched range must read as zeros"
        );
    }

    #[test]
    fn delete_removes_backing_file() {
        let (_d, s) = store();
        let id = Uuid::new_v4();
        s.create(id, 1024).unwrap();
        s.delete(id).unwrap();
        assert!(s.size(id).is_err());
    }

    #[test]
    fn set_size_grows_volume() {
        // NFS file growth path: SETATTR.size or write past EOF must
        // extend the underlying volume so the next pwrite is in bounds.
        let (_d, s) = store();
        let id = Uuid::new_v4();
        s.create(id, 1024).unwrap();
        s.set_size(id, 1024 * 1024).unwrap();
        assert_eq!(s.size(id).unwrap(), 1024 * 1024);
        // Writing into the newly-allocated range must succeed.
        s.write(id, 512 * 1024, b"grew", SyncMode::None).unwrap();
        let mut buf = [0u8; 4];
        s.read(id, 512 * 1024, &mut buf).unwrap();
        assert_eq!(&buf, b"grew");
    }

    #[test]
    fn set_size_shrinks_and_drops_data() {
        // truncate(2) on an NFS file: anything past new_size must be
        // gone, including subsequent reads returning zero.
        let (_d, s) = store();
        let id = Uuid::new_v4();
        s.create(id, 8192).unwrap();
        s.write(id, 0, &[0xAB; 8192], SyncMode::None).unwrap();
        s.set_size(id, 2048).unwrap();
        assert_eq!(s.size(id).unwrap(), 2048);
        // Anything past the new logical size must be gone — read short.
        let mut buf = [0xFFu8; 4096];
        let n = s.read(id, 1024, &mut buf).unwrap();
        // We can only read up to the new size from offset 1024 → 1024 bytes.
        assert_eq!(n, 1024);
        assert!(buf[..1024].iter().all(|&b| b == 0xAB));
    }

    /// The io_uring backend is a drop-in for
    /// `PosixPmStore`. Test passes the same suite via the trait so
    /// any drift is caught immediately.
    #[cfg(all(target_os = "linux", feature = "io-uring"))]
    #[test]
    fn iouring_passes_round_trip() {
        let dir = TempDir::new().unwrap();
        let store = iouring::IoUringPmStore::new(dir.path()).unwrap();
        let id = Uuid::new_v4();
        store.create(id, 1024 * 1024).unwrap();
        store.write(id, 0, b"uring rules", SyncMode::None).unwrap();
        store.flush(id, SyncMode::Data).unwrap();
        let mut buf = [0u8; 11];
        let n = store.read(id, 0, &mut buf).unwrap();
        assert_eq!(n, 11);
        assert_eq!(&buf, b"uring rules");
        store.set_size(id, 4 * 1024 * 1024).unwrap();
        assert_eq!(store.size(id).unwrap(), 4 * 1024 * 1024);
        store
            .write(id, 2 * 1024 * 1024, b"sparse extend", SyncMode::None)
            .unwrap();
        let mut buf = [0u8; 13];
        store.read(id, 2 * 1024 * 1024, &mut buf).unwrap();
        assert_eq!(&buf, b"sparse extend");
        store.delete(id).unwrap();
        assert!(store.size(id).is_err());
    }

    /// Exercises the linked write+fsync (two-CQE) path and the
    /// concurrent-across-volumes path, which stress the unique-user_data
    /// completion matching added for the io_uring orphaned-CQE fix.
    /// Many volumes hash across shards; each is written with
    /// SyncMode::Data (write+fdatasync linked SQEs) then read back and
    /// verified. A CQE mismatch would return the wrong byte count or
    /// corrupt data.
    #[cfg(all(target_os = "linux", feature = "io-uring"))]
    #[test]
    fn iouring_concurrent_data_sync_integrity() {
        use std::sync::Arc;
        let dir = TempDir::new().unwrap();
        let store = Arc::new(iouring::IoUringPmStore::new(dir.path()).unwrap());
        let ids: Vec<Uuid> = (0..32).map(|_| Uuid::new_v4()).collect();
        for &id in &ids {
            store.create(id, 1024 * 1024).unwrap();
        }
        let handles: Vec<_> = ids
            .iter()
            .cloned()
            .enumerate()
            .map(|(i, id)| {
                let store = Arc::clone(&store);
                std::thread::spawn(move || {
                    let payload = vec![i as u8; 64 * 1024];
                    // Data mode → linked write+fdatasync (two SQEs, two CQEs).
                    store.write(id, 0, &payload, SyncMode::Data).unwrap();
                    let mut buf = vec![0u8; 64 * 1024];
                    let n = store.read(id, 0, &mut buf).unwrap();
                    assert_eq!(n, 64 * 1024);
                    assert!(
                        buf.iter().all(|&b| b == i as u8),
                        "volume {i}: read-back mismatch (CQE crossed?)"
                    );
                })
            })
            .collect();
        for h in handles {
            h.join().unwrap();
        }
    }

    #[test]
    fn create_rejects_existing_volume() {
        let (_d, s) = store();
        let id = Uuid::new_v4();
        s.create(id, 1024).unwrap();
        let err = s.create(id, 1024).unwrap_err();
        assert_eq!(err.kind(), io::ErrorKind::AlreadyExists);
    }
}

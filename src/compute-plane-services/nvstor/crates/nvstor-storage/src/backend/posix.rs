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

//! POSIX file I/O storage backend.
//!
//! Stores chunks as individual files in a sharded directory hierarchy.
//! Uses standard filesystem operations (open, write, read, fsync, unlink).
//!
//! Path scheme: `{base_path}/{hex[0..2]}/{hex[2..4]}/{full_hex}.chunk`
//!
//! This is the baseline backend — works everywhere, no special kernel features needed.
//! For higher performance, use the io_uring backend (requires Linux 5.6+).
//!
//! Ported from C `posix_store.c`.

use std::fs;
use std::io::{Read, Write};
use std::path::PathBuf;

use super::{chunk_path, ensure_chunk_dir, fs_capacity, BackendCaps, BackendStats, StoreBackend};

/// POSIX file I/O storage backend.
pub struct PosixBackend {
    base_path: PathBuf,
}

impl PosixBackend {
    /// Create a new POSIX backend rooted at the given directory.
    ///
    /// Creates the directory if it doesn't exist.
    pub fn new(base_path: impl Into<PathBuf>) -> std::io::Result<Self> {
        let base_path = base_path.into();
        fs::create_dir_all(&base_path)?;
        Ok(Self { base_path })
    }

    /// Return the base path of this backend.
    pub fn base_path(&self) -> &std::path::Path {
        &self.base_path
    }
}

impl StoreBackend for PosixBackend {
    fn write_chunk_sync(
        &self,
        chunk_id: &[u8; 32],
        data: &[u8],
        sync: crate::SyncMode,
    ) -> std::io::Result<()> {
        let path = chunk_path(&self.base_path, chunk_id);

        // No existence check: a file left by an unsynced write before a
        // crash can be torn, and the rename below replaces it atomically.
        // The manager decides dedup from the chunk's metadata row.
        ensure_chunk_dir(&self.base_path, chunk_id)?;

        // Atomic write: write to temp file, then rename.
        // This prevents partial writes from being visible.
        //
        // The `sync` mode picks the durability barrier:
        // - SyncMode::Full → file.sync_all()  (fsync, data + metadata)
        // - SyncMode::Data → file.sync_data() (fdatasync, data only)
        // - SyncMode::None → no sync; caller is responsible for
        //   triggering a later flush. The rename still happens, so
        //   the chunk is visible under its content address; only the
        //   data-on-stable-storage guarantee is deferred.
        //
        // Skipping `sync_all` doesn't violate atomicity: any chunk
        // file at `chunk_path/X` is by definition fully written
        // because we only rename after the data has reached the
        // requested durability level (or zero, for `None`).
        let temp_path = path.with_extension("tmp");
        let mut file = fs::File::create(&temp_path)?;
        file.write_all(data)?;
        match sync {
            crate::SyncMode::None => {}
            crate::SyncMode::Data => file.sync_data()?,
            crate::SyncMode::Full => file.sync_all()?,
        }
        fs::rename(&temp_path, &path)?;
        // Durable modes must also persist the rename itself: fsync the parent
        // directory so the chunk's dir entry survives power loss. Without
        // this, a Full-mode S3 PUT can 200 and still lose the chunk file on
        // crash while the fsync'd manifest survives → NotFound on an ACK'd
        // object. None defers all durability to a later barrier, so skip it.
        if !matches!(sync, crate::SyncMode::None) {
            super::fsync_parent_dir(&path)?;
        }

        Ok(())
    }

    fn read_chunk(&self, chunk_id: &[u8; 32], buffer: &mut [u8]) -> std::io::Result<usize> {
        let path = chunk_path(&self.base_path, chunk_id);

        let mut file = fs::File::open(&path).map_err(|e| {
            if e.kind() == std::io::ErrorKind::NotFound {
                std::io::Error::new(std::io::ErrorKind::NotFound, "chunk not found")
            } else {
                e
            }
        })?;

        let n = file.read(buffer)?;
        Ok(n)
    }

    fn delete_chunk(&self, chunk_id: &[u8; 32]) -> std::io::Result<()> {
        let path = chunk_path(&self.base_path, chunk_id);
        match fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()), // Idempotent
            Err(e) => Err(e),
        }
    }

    fn sync_chunk(&self, chunk_id: &[u8; 32]) -> std::io::Result<()> {
        super::sync_chunk_file(&self.base_path, chunk_id)
    }

    fn flush(&self) -> std::io::Result<()> {
        // Per-chunk durability: write_chunk_sync for Data/Full writes,
        // sync_chunk for None writes. No backend-wide state to flush.
        Ok(())
    }

    fn stats(&self) -> std::io::Result<BackendStats> {
        let (total, avail) = fs_capacity(&self.base_path);
        Ok(BackendStats {
            total_bytes: total,
            used_bytes: total.saturating_sub(avail),
            available_bytes: avail,
        })
    }

    fn caps(&self) -> BackendCaps {
        BackendCaps {
            direct_io: false, // POSIX backend doesn't use O_DIRECT
            async_io: false,
            polling: false,
            requires_alignment: false,
            min_alignment: 0,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use sha2::{Digest, Sha256};

    /// Compute a SHA-256 chunk ID from data (matching production content-addressing).
    fn content_id(data: &[u8]) -> [u8; 32] {
        let mut hasher = Sha256::new();
        hasher.update(data);
        let result = hasher.finalize();
        let mut id = [0u8; 32];
        id.copy_from_slice(&result);
        id
    }

    #[test]
    fn test_write_read_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        let data = b"hello, nvstor storage engine";
        let id = content_id(data);

        backend.write_chunk(&id, data).unwrap();

        let mut buf = vec![0u8; 1024];
        let n = backend.read_chunk(&id, &mut buf).unwrap();
        assert_eq!(n, data.len());
        assert_eq!(&buf[..n], data);
    }

    // Durable-mode writes must fsync the parent directory after rename so
    // the chunk's dir entry survives power loss. We can't inject a crash in a
    // unit test, but we can assert the durable write path (which now issues a
    // parent-dir fsync) completes and the chunk is readable.
    #[test]
    fn test_full_sync_write_is_durable_and_readable() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        let data = b"durable checkpoint bytes";
        let id = content_id(data);

        backend
            .write_chunk_sync(&id, data, crate::SyncMode::Full)
            .expect("Full-mode write (with parent-dir fsync) must succeed");

        let mut buf = vec![0u8; 1024];
        let n = backend.read_chunk(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], data);
    }

    // Helper: fsync_parent_dir must succeed on a real file path and be a
    // safe no-op when the parent doesn't exist.
    #[test]
    fn test_fsync_parent_dir() {
        let dir = tempfile::tempdir().unwrap();
        let file_path = dir.path().join("a_file");
        std::fs::write(&file_path, b"x").unwrap();
        super::super::fsync_parent_dir(&file_path).expect("fsync of existing parent dir");

        // Missing parent → Ok (nothing to flush), not an error.
        let missing = dir.path().join("no_such_dir").join("child");
        super::super::fsync_parent_dir(&missing).expect("missing parent is a no-op");
    }

    // A torn file left at a chunk's address by an unsynced write
    // before a crash must not make a later write a no-op.
    #[test]
    fn test_write_replaces_torn_file() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();
        let data = vec![7u8; 8192];
        let id = content_id(&data);
        ensure_chunk_dir(&dir.path().join("chunks"), &id).unwrap();
        fs::write(chunk_path(&dir.path().join("chunks"), &id), &data[..100]).unwrap();

        backend
            .write_chunk_sync(&id, &data, crate::SyncMode::None)
            .unwrap();
        let mut buf = vec![0u8; 16384];
        let n = backend.read_chunk(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], &data[..]);
    }

    #[test]
    fn test_sync_chunk() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();
        let data = b"sync me later";
        let id = content_id(data);
        backend
            .write_chunk_sync(&id, data, crate::SyncMode::None)
            .unwrap();
        backend.sync_chunk(&id).unwrap();

        let missing = content_id(b"never written");
        let err = backend.sync_chunk(&missing).unwrap_err();
        assert_eq!(err.kind(), std::io::ErrorKind::NotFound);
    }

    #[test]
    fn test_content_addressed_dedup() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        let data = b"duplicate data";
        let id = content_id(data);

        // Write twice — the second write replaces the file with the same bytes.
        backend.write_chunk(&id, data).unwrap();
        backend.write_chunk(&id, data).unwrap();

        let mut buf = vec![0u8; 1024];
        let n = backend.read_chunk(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], data);
    }

    #[test]
    fn test_read_nonexistent() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        let id = [0xab; 32];
        let mut buf = vec![0u8; 1024];
        let result = backend.read_chunk(&id, &mut buf);
        assert!(result.is_err());
        assert_eq!(result.unwrap_err().kind(), std::io::ErrorKind::NotFound);
    }

    #[test]
    fn test_delete() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        let data = b"delete me";
        let id = content_id(data);

        backend.write_chunk(&id, data).unwrap();

        // Verify exists
        let mut buf = vec![0u8; 1024];
        assert!(backend.read_chunk(&id, &mut buf).is_ok());

        // Delete
        backend.delete_chunk(&id).unwrap();

        // Verify gone
        assert!(backend.read_chunk(&id, &mut buf).is_err());
    }

    #[test]
    fn test_delete_idempotent() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        let id = [0xff; 32];
        // Delete nonexistent — should succeed (idempotent)
        backend.delete_chunk(&id).unwrap();
        backend.delete_chunk(&id).unwrap();
    }

    #[test]
    fn test_atomic_write() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        let data = b"atomic write test - verify write-then-rename pattern";
        let id = content_id(data);

        backend.write_chunk(&id, data).unwrap();

        // Verify data is readable (write was atomic)
        let mut buf = vec![0u8; 1024];
        let n = backend.read_chunk(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], data);

        // Verify the chunk file exists at the correct path
        let path = super::chunk_path(dir.path().join("chunks").as_ref(), &id);
        assert!(path.exists());

        // Verify no .tmp file exists alongside it
        let tmp_path = path.with_extension("tmp");
        assert!(
            !tmp_path.exists(),
            "temp file should be cleaned up after rename"
        );
    }

    #[test]
    fn test_large_chunk() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        // 16 MiB chunk (default chunk size)
        let data = vec![0xABu8; 16 * 1024 * 1024];
        let id = content_id(&data);

        backend.write_chunk(&id, &data).unwrap();

        let mut buf = vec![0u8; 16 * 1024 * 1024];
        let n = backend.read_chunk(&id, &mut buf).unwrap();
        assert_eq!(n, data.len());
        assert_eq!(buf, data);
    }

    #[test]
    fn test_many_chunks() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        let mut ids = Vec::new();
        for i in 0u32..100 {
            let data = format!("chunk data {}", i);
            let id = content_id(data.as_bytes());
            backend.write_chunk(&id, data.as_bytes()).unwrap();
            ids.push((id, data));
        }

        // Verify all readable
        for (id, expected) in &ids {
            let mut buf = vec![0u8; 1024];
            let n = backend.read_chunk(id, &mut buf).unwrap();
            assert_eq!(&buf[..n], expected.as_bytes());
        }
    }

    #[test]
    fn test_concurrent_writes() {
        let dir = tempfile::tempdir().unwrap();
        let backend = std::sync::Arc::new(PosixBackend::new(dir.path().join("chunks")).unwrap());

        let handles: Vec<_> = (0..10)
            .map(|i| {
                let backend = backend.clone();
                std::thread::spawn(move || {
                    let data = format!("concurrent chunk {}", i);
                    let id = content_id(data.as_bytes());
                    backend.write_chunk(&id, data.as_bytes()).unwrap();

                    let mut buf = vec![0u8; 1024];
                    let n = backend.read_chunk(&id, &mut buf).unwrap();
                    assert_eq!(&buf[..n], data.as_bytes());
                })
            })
            .collect();

        for h in handles {
            h.join().unwrap();
        }
    }

    #[test]
    fn test_stats() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        let stats = backend.stats().unwrap();
        assert!(stats.total_bytes > 0);
        assert!(stats.available_bytes > 0);
    }

    #[test]
    fn test_caps() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        let caps = backend.caps();
        assert!(!caps.direct_io);
        assert!(!caps.async_io);
        assert!(!caps.requires_alignment);
    }

    #[test]
    fn test_directory_sharding() {
        let dir = tempfile::tempdir().unwrap();
        let backend = PosixBackend::new(dir.path().join("chunks")).unwrap();

        // Write chunks with different first bytes to verify sharding
        for prefix in 0u8..=5 {
            let mut id = [0u8; 32];
            id[0] = prefix;
            id[1] = prefix.wrapping_add(1);
            backend.write_chunk(&id, &[prefix; 100]).unwrap();
        }

        // Verify directory structure exists
        let chunks_dir = dir.path().join("chunks");
        assert!(chunks_dir.join("00/01").exists());
        assert!(chunks_dir.join("01/02").exists());
        assert!(chunks_dir.join("05/06").exists());
    }
}

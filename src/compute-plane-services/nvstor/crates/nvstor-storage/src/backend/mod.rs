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

//! Storage backend abstraction — pluggable I/O layer for chunk persistence.
//!
//! Ported from C `store_backend.h` vtable pattern to Rust traits.
//! Implementations: POSIX (std::fs), io_uring (O_DIRECT async I/O).
//!
//! Design goals (from C reference):
//! - Zero-cost abstraction (trait dispatch, no boxing on hot path)
//! - Backend-specific optimizations (O_DIRECT, aligned I/O)
//! - Hot-swappable via enum dispatch

mod posix;

#[cfg(feature = "io-uring")]
mod uring;

pub use posix::PosixBackend;

#[cfg(feature = "io-uring")]
pub use uring::IoUringBackend;

use std::path::Path;

/// Backend capabilities — declares what I/O features are supported.
///
/// Callers check capabilities before using backend-specific features.
/// Pattern adopted from OpenDAL's AccessorInfo capability model.
#[derive(Debug, Clone, Default)]
pub struct BackendCaps {
    /// Supports O_DIRECT (bypass page cache)
    pub direct_io: bool,
    /// Supports async I/O submission
    pub async_io: bool,
    /// Supports polled completions
    pub polling: bool,
    /// Requires buffer alignment (for O_DIRECT)
    pub requires_alignment: bool,
    /// Minimum buffer alignment in bytes (0 if no requirement)
    pub min_alignment: usize,
}

/// Storage statistics for a backend.
#[derive(Debug, Clone, Default)]
pub struct BackendStats {
    /// Total capacity in bytes
    pub total_bytes: u64,
    /// Used capacity in bytes
    pub used_bytes: u64,
    /// Available capacity in bytes
    pub available_bytes: u64,
}

/// Storage backend trait — the I/O interface for chunk persistence.
///
/// Every chunk is identified by a 32-byte content hash (SHA-256).
/// Backends store chunks as files in a sharded directory structure:
///   `{base_path}/{hex[0..2]}/{hex[2..4]}/{full_hex}.chunk`
///
/// Implementations must be thread-safe (Send + Sync).
pub trait StoreBackend: Send + Sync {
    /// Write chunk data to persistent storage with durability mode.
    ///
    /// `sync` controls whether the call returns before data is on
    /// stable storage. Pick the mode based on the protocol contract:
    /// `SyncMode::Full` matches today's behavior (full fsync per
    /// chunk); `SyncMode::Data` skips the metadata journal;
    /// `SyncMode::None` returns as soon as the data is queued
    /// (caller responsible for triggering a later flush).
    ///
    /// The chunk file is created atomically (write to temp, rename) and
    /// replaces any existing file at that address. Dedup is the caller's
    /// decision, made against the chunk's metadata row: a file left by an
    /// unsynced write before a crash can be torn, so its existence proves
    /// nothing.
    fn write_chunk_sync(
        &self,
        chunk_id: &[u8; 32],
        data: &[u8],
        sync: crate::SyncMode,
    ) -> std::io::Result<()>;

    /// Legacy entry point — preserves the historical behavior of
    /// every chunk write being fully durable. New / hot-path callers
    /// should use `write_chunk_sync` and pick a real `SyncMode`.
    fn write_chunk(&self, chunk_id: &[u8; 32], data: &[u8]) -> std::io::Result<()> {
        self.write_chunk_sync(chunk_id, data, crate::SyncMode::Full)
    }

    /// Read chunk data from storage into the provided buffer.
    ///
    /// Returns the number of bytes read.
    /// Returns io::ErrorKind::NotFound if chunk doesn't exist.
    fn read_chunk(&self, chunk_id: &[u8; 32], buffer: &mut [u8]) -> std::io::Result<usize>;

    /// Delete a chunk from storage.
    ///
    /// Returns Ok(()) even if chunk doesn't exist (idempotent delete).
    fn delete_chunk(&self, chunk_id: &[u8; 32]) -> std::io::Result<()>;

    /// Make one chunk durable: fdatasync its file, then fsync its parent
    /// directory so the rename survives too. `ChunkManager::flush` calls it
    /// for every chunk written with `SyncMode::None`.
    /// Returns io::ErrorKind::NotFound if the chunk doesn't exist.
    fn sync_chunk(&self, chunk_id: &[u8; 32]) -> std::io::Result<()>;

    /// Flush backend-wide state to stable storage. Per-chunk durability is
    /// `write_chunk_sync` for `Data`/`Full` writes and `sync_chunk` for
    /// `None` writes.
    fn flush(&self) -> std::io::Result<()>;

    /// Get storage statistics (capacity, used, available).
    fn stats(&self) -> std::io::Result<BackendStats>;

    /// Get backend capabilities.
    fn caps(&self) -> BackendCaps;
}

/// Convert a 32-byte chunk ID to a hex string.
pub fn chunk_id_to_hex(id: &[u8; 32]) -> String {
    let mut hex = String::with_capacity(64);
    for byte in id {
        hex.push_str(&format!("{:02x}", byte));
    }
    hex
}

/// Build the filesystem path for a chunk.
///
/// Path scheme: `{base}/{hex[0..2]}/{hex[2..4]}/{full_hex}.chunk`
/// Two levels of directory sharding to avoid too many files per directory.
/// With 256 × 256 = 65,536 leaf directories, each holds ~15K files at 1B chunks.
pub fn chunk_path(base: &Path, chunk_id: &[u8; 32]) -> std::path::PathBuf {
    let hex = chunk_id_to_hex(chunk_id);
    base.join(&hex[0..2])
        .join(&hex[2..4])
        .join(format!("{}.chunk", hex))
}

/// Ensure the parent directories for a chunk path exist.
pub fn ensure_chunk_dir(base: &Path, chunk_id: &[u8; 32]) -> std::io::Result<()> {
    let hex = chunk_id_to_hex(chunk_id);
    let dir = base.join(&hex[0..2]).join(&hex[2..4]);
    std::fs::create_dir_all(dir)
}

/// fsync the directory that contains `file_path` so a rename/create into it
/// is durable across power loss. `fsync` on a *file* persists the file's
/// data + inode but NOT the directory entry that names it — after a crash the
/// bytes can be on disk while the name is gone, leaving an fsync'd RocksDB
/// manifest pointing at a chunk file that no longer exists (post-crash
/// NotFound on an ACK'd object). Durable-mode writes must fsync the parent
/// directory after the rename to close that window.
///
/// fdatasync the chunk file at `base` and fsync its parent directory.
pub fn sync_chunk_file(base: &Path, chunk_id: &[u8; 32]) -> std::io::Result<()> {
    let path = chunk_path(base, chunk_id);
    std::fs::File::open(&path)?.sync_data()?;
    fsync_parent_dir(&path)
}

/// Best-effort on platforms/filesystems where opening a directory for fsync
/// isn't supported: a directory open that fails with an error other than a
/// hard I/O failure is treated as "nothing to flush" rather than failing the
/// write. On Linux (the production target) this always performs a real
/// directory fsync.
pub fn fsync_parent_dir(file_path: &Path) -> std::io::Result<()> {
    let Some(dir) = file_path.parent() else {
        return Ok(());
    };
    match std::fs::File::open(dir) {
        Ok(dir_file) => dir_file.sync_all(),
        // Some filesystems reject opening a directory as a file; there is no
        // dir entry to flush in that case, so don't fail the write.
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(e),
    }
}

/// Query filesystem capacity using statvfs.
#[cfg(unix)]
pub fn fs_capacity(path: &Path) -> (u64, u64) {
    use std::ffi::CString;
    use std::os::unix::ffi::OsStrExt;

    if let Ok(c_path) = CString::new(path.as_os_str().as_bytes()) {
        unsafe {
            let mut stat: libc::statvfs = std::mem::zeroed();
            if libc::statvfs(c_path.as_ptr(), &mut stat) == 0 {
                let total = stat.f_blocks as u64 * stat.f_frsize as u64;
                let avail = stat.f_bavail as u64 * stat.f_frsize as u64;
                return (total, avail);
            }
        }
    }
    (0, 0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    #[test]
    fn test_chunk_id_to_hex() {
        let id = [0xab; 32];
        let hex = chunk_id_to_hex(&id);
        assert_eq!(hex.len(), 64);
        assert!(hex.chars().all(|c| c.is_ascii_hexdigit()));
        assert_eq!(&hex[0..2], "ab");
    }

    #[test]
    fn test_chunk_path_structure() {
        let base = PathBuf::from("/data/chunks");
        let id = [
            0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
            0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
            0x00, 0x00, 0x00, 0x00,
        ];
        let path = chunk_path(&base, &id);
        let path_str = path.to_string_lossy();
        // Two levels of sharding
        assert!(path_str.starts_with("/data/chunks/ab/cd/"));
        assert!(path_str.ends_with(".chunk"));
    }

    #[test]
    fn test_chunk_path_deterministic() {
        let base = PathBuf::from("/tmp");
        let id = [0x42; 32];
        assert_eq!(chunk_path(&base, &id), chunk_path(&base, &id));
    }
}

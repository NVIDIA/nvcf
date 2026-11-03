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

//! io_uring storage backend with O_DIRECT and aligned I/O.
//!
//! Uses Linux io_uring for high-performance async I/O with:
//! - O_DIRECT: bypass page cache for predictable latency
//! - Aligned buffers: 4KB page-aligned for O_DIRECT requirements
//! - RAII buffer management: buffers are freed on every path
//! - Mutex-protected ring: thread-safe SQE/CQE access
//! - Atomic write pattern (write to temp, fsync, rename)

use std::fs;
use std::os::unix::fs::OpenOptionsExt;
use std::os::unix::io::AsRawFd;
use std::path::PathBuf;

use io_uring::{opcode, types, IoUring};
use parking_lot::Mutex;

use super::{chunk_path, ensure_chunk_dir, fs_capacity, BackendCaps, BackendStats, StoreBackend};

/// Page size for O_DIRECT alignment.
const PAGE_SIZE: usize = 4096;

/// Default io_uring queue depth.
const DEFAULT_QUEUE_DEPTH: u32 = 128;

/// RAII wrapper for page-aligned memory allocation.
///
/// Required for O_DIRECT I/O which needs buffers aligned to page boundaries.
/// Properly deallocates memory on drop — no mem::forget leaks.
struct AlignedBuf {
    ptr: *mut u8,
    layout: std::alloc::Layout,
    /// Allocated (aligned) size — may be larger than requested.
    allocated: usize,
    /// Logical data length — actual bytes of meaningful data.
    len: usize,
}

impl AlignedBuf {
    /// Allocate a page-aligned, zero-filled buffer of at least `size` bytes.
    fn new(size: usize) -> std::io::Result<Self> {
        let aligned_size = (size + PAGE_SIZE - 1) & !(PAGE_SIZE - 1);
        if aligned_size == 0 {
            // Special case: zero-size allocation.
            return Ok(Self {
                ptr: std::ptr::NonNull::dangling().as_ptr(),
                layout: std::alloc::Layout::from_size_align(0, PAGE_SIZE)
                    .expect("zero-size layout is valid"),
                allocated: 0,
                len: 0,
            });
        }

        let layout = std::alloc::Layout::from_size_align(aligned_size, PAGE_SIZE)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::Other, e))?;

        let ptr = unsafe { std::alloc::alloc_zeroed(layout) };
        if ptr.is_null() {
            return Err(std::io::Error::new(
                std::io::ErrorKind::OutOfMemory,
                "aligned allocation failed",
            ));
        }

        Ok(Self {
            ptr,
            layout,
            allocated: aligned_size,
            len: size,
        })
    }

    fn as_ptr(&self) -> *const u8 {
        self.ptr
    }

    fn as_mut_ptr(&mut self) -> *mut u8 {
        self.ptr
    }

    fn as_slice(&self) -> &[u8] {
        if self.allocated == 0 {
            return &[];
        }
        unsafe { std::slice::from_raw_parts(self.ptr, self.len) }
    }

    fn as_mut_slice(&mut self) -> &mut [u8] {
        if self.allocated == 0 {
            return &mut [];
        }
        unsafe { std::slice::from_raw_parts_mut(self.ptr, self.allocated) }
    }

    /// The aligned (padded) size for I/O operations.
    fn aligned_len(&self) -> usize {
        self.allocated
    }
}

impl Drop for AlignedBuf {
    fn drop(&mut self) {
        if self.allocated > 0 {
            unsafe {
                std::alloc::dealloc(self.ptr, self.layout);
            }
        }
    }
}

// Safety: AlignedBuf owns its memory exclusively.
unsafe impl Send for AlignedBuf {}

/// Pre-registered buffer pool for io_uring fixed I/O.
///
/// Registering buffers with io_uring_register_buffers() avoids per-op
/// page pinning/unpinning in the kernel, reducing syscall overhead.
#[allow(dead_code)]
struct RegisteredBufferPool {
    /// Pre-allocated aligned buffers.
    buffers: Vec<AlignedBuf>,
    /// Which buffers are currently in use.
    in_use: Vec<bool>,
    /// Buffer size (all same size).
    buf_size: usize,
}

#[allow(dead_code)]
impl RegisteredBufferPool {
    fn new(count: usize, buf_size: usize) -> std::io::Result<Self> {
        let mut buffers = Vec::with_capacity(count);
        for _ in 0..count {
            buffers.push(AlignedBuf::new(buf_size)?);
        }
        Ok(Self {
            buffers,
            in_use: vec![false; count],
            buf_size,
        })
    }

    /// Checkout a buffer. Returns (index, &mut AlignedBuf).
    fn checkout(&mut self) -> Option<usize> {
        for (i, used) in self.in_use.iter_mut().enumerate() {
            if !*used {
                *used = true;
                return Some(i);
            }
        }
        None
    }

    fn return_buf(&mut self, idx: usize) {
        if idx < self.in_use.len() {
            self.in_use[idx] = false;
        }
    }

    fn get_mut(&mut self, idx: usize) -> &mut AlignedBuf {
        &mut self.buffers[idx]
    }

    fn get(&self, idx: usize) -> &AlignedBuf {
        &self.buffers[idx]
    }
}

/// io_uring storage backend.
///
/// Thread-safe via mutex-protected ring. Each operation:
/// 1. Opens file (kept alive during io_uring operation)
/// 2. Submits SQE to ring
/// 3. Waits for CQE completion
/// 4. File and buffer freed after completion (RAII)
///
/// Optionally uses pre-registered buffer pool for reduced kernel overhead.
pub struct IoUringBackend {
    base_path: PathBuf,
    ring: Mutex<IoUring>,
    /// Pre-registered buffer pool (reduces per-op page pinning).
    buffer_pool: Mutex<Option<RegisteredBufferPool>>,
}

impl IoUringBackend {
    /// Create a new io_uring backend with default queue depth.
    pub fn new(base_path: impl Into<PathBuf>) -> std::io::Result<Self> {
        Self::with_queue_depth(base_path, DEFAULT_QUEUE_DEPTH)
    }

    /// Create with custom queue depth.
    pub fn with_queue_depth(
        base_path: impl Into<PathBuf>,
        queue_depth: u32,
    ) -> std::io::Result<Self> {
        let base_path = base_path.into();
        fs::create_dir_all(&base_path)?;

        let ring = IoUring::new(queue_depth).map_err(|e| {
            std::io::Error::new(std::io::ErrorKind::Other, format!("io_uring init: {e}"))
        })?;

        tracing::info!(
            path = %base_path.display(),
            queue_depth,
            "io_uring backend initialized"
        );

        let mut backend = Self {
            base_path,
            ring: Mutex::new(ring),
            buffer_pool: Mutex::new(None),
        };

        // Pre-register a pool of buffers for fixed I/O (16 buffers × 4MB each).
        // This reduces kernel overhead by avoiding per-op page pinning.
        if let Err(e) = backend.register_buffer_pool(16, 4 * 1024 * 1024) {
            tracing::warn!(error = %e, "io_uring buffer registration failed (using unregistered I/O)");
        }

        Ok(backend)
    }

    /// Register a pool of fixed buffers with io_uring.
    fn register_buffer_pool(&mut self, count: usize, buf_size: usize) -> std::io::Result<()> {
        let pool = RegisteredBufferPool::new(count, buf_size)?;

        // Build iovec array for registration.
        let iovecs: Vec<libc::iovec> = pool
            .buffers
            .iter()
            .map(|buf| libc::iovec {
                iov_base: buf.ptr as *mut libc::c_void,
                iov_len: buf.allocated,
            })
            .collect();

        // Register with io_uring.
        let ring = self.ring.lock();
        let submitter = ring.submitter();
        // SAFETY: iovecs point to valid, aligned, long-lived buffers in the pool.
        // The pool outlives the ring registration.
        unsafe {
            submitter.register_buffers(&iovecs).map_err(|e| {
                std::io::Error::new(
                    std::io::ErrorKind::Other,
                    format!("io_uring register_buffers: {e}"),
                )
            })?;
        }
        drop(ring);

        tracing::info!(count, buf_size, "io_uring buffer pool registered");
        *self.buffer_pool.lock() = Some(pool);
        Ok(())
    }

    /// Submit a write SQE; no fsync. Used by `SyncMode::None` callers.
    fn submit_write(&self, fd: &fs::File, buf: &AlignedBuf) -> std::io::Result<usize> {
        let raw_fd = types::Fd(fd.as_raw_fd());
        let write_op = opcode::Write::new(raw_fd, buf.as_ptr(), buf.aligned_len() as u32)
            .offset(0)
            .build()
            .user_data(0x10);

        let mut ring = self.ring.lock();
        while ring.completion().next().is_some() {}
        unsafe {
            ring.submission()
                .push(&write_op)
                .map_err(|_| std::io::Error::new(std::io::ErrorKind::Other, "SQ full (write)"))?;
        }
        ring.submit_and_wait(1)?;
        let mut write_result = 0i32;
        for cqe in ring.completion() {
            if cqe.user_data() == 0x10 {
                write_result = cqe.result();
                break;
            }
        }
        if write_result < 0 {
            return Err(std::io::Error::from_raw_os_error(-write_result));
        }
        Ok(write_result as usize)
    }

    /// Submit write + fsync as linked SQEs (one syscall for both ops).
    /// The IOSQE_IO_LINK flag chains the fsync after the write completes.
    fn submit_write_fsync(&self, fd: &fs::File, buf: &AlignedBuf) -> std::io::Result<usize> {
        let raw_fd = types::Fd(fd.as_raw_fd());

        let write_op = opcode::Write::new(raw_fd, buf.as_ptr(), buf.aligned_len() as u32)
            .offset(0)
            .build()
            .flags(io_uring::squeue::Flags::IO_LINK) // Chain to next SQE
            .user_data(0x10);

        let fsync_op = opcode::Fsync::new(raw_fd).build().user_data(0x11);

        let mut ring = self.ring.lock();

        // Drain any stale CQEs from previous operations.
        while ring.completion().next().is_some() {}

        unsafe {
            ring.submission()
                .push(&write_op)
                .map_err(|_| std::io::Error::new(std::io::ErrorKind::Other, "SQ full (write)"))?;
            ring.submission()
                .push(&fsync_op)
                .map_err(|_| std::io::Error::new(std::io::ErrorKind::Other, "SQ full (fsync)"))?;
        }

        // Submit both and wait for 2 completions.
        ring.submit_and_wait(2)?;

        let mut write_result = 0i32;
        let mut fsync_result = 0i32;
        let mut cqe_count = 0;
        for cqe in ring.completion() {
            match cqe.user_data() {
                0x10 => write_result = cqe.result(),
                0x11 => fsync_result = cqe.result(),
                _ => {} // Ignore unexpected CQEs.
            }
            cqe_count += 1;
            if cqe_count >= 2 {
                break;
            } // Only process our 2 CQEs.
        }

        if write_result < 0 {
            return Err(std::io::Error::from_raw_os_error(-write_result));
        }
        if fsync_result < 0 {
            return Err(std::io::Error::from_raw_os_error(-fsync_result));
        }

        Ok(write_result as usize)
    }

    /// Submit a read operation and wait for completion.
    /// Submit one read of `len` bytes into `ptr` starting at file `offset`.
    ///
    /// Returns the count the kernel actually transferred, which the caller
    /// MUST honour — a short read is normal at EOF and is also the signature
    /// of a partially failing device.
    ///
    /// O_DIRECT requires `ptr`, `len` and `offset` to be alignment-compatible;
    /// callers are responsible for keeping resume points page-aligned.
    fn submit_read_at(
        &self,
        fd: &fs::File,
        ptr: *mut u8,
        len: u32,
        offset: u64,
    ) -> std::io::Result<usize> {
        let raw_fd = types::Fd(fd.as_raw_fd());

        let read_op = opcode::Read::new(raw_fd, ptr, len)
            .offset(offset)
            .build()
            .user_data(0x02);

        let mut ring = self.ring.lock();

        // Drain any stale CQEs.
        while ring.completion().next().is_some() {}

        unsafe {
            ring.submission()
                .push(&read_op)
                .map_err(|_| std::io::Error::new(std::io::ErrorKind::Other, "io_uring SQ full"))?;
        }

        ring.submit_and_wait(1)?;

        let cqe = ring
            .completion()
            .next()
            .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::Other, "io_uring: no CQE"))?;

        if cqe.result() < 0 {
            return Err(std::io::Error::from_raw_os_error(-cqe.result()));
        }

        Ok(cqe.result() as usize)
    }
}

impl StoreBackend for IoUringBackend {
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

        // Prepare aligned buffer (RAII — freed on drop)
        let mut buf = AlignedBuf::new(data.len())?;
        buf.as_mut_slice()[..data.len()].copy_from_slice(data);

        // Atomic write: write to temp file with O_DIRECT, [fsync,] rename.
        let temp_path = path.with_extension("tmp");
        let file = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .custom_flags(libc::O_DIRECT)
            .open(&temp_path)?;

        // Submit write [+ fsync] as linked SQEs.
        // For SyncMode::None we still need the write to complete before
        // rename, but the fsync is dropped — the data is in kernel
        // writeback and a later flush op (or the kernel's own
        // background flush) will get it onto stable storage.
        let written = match sync {
            crate::SyncMode::None => self.submit_write(&file, &buf)?,
            crate::SyncMode::Data | crate::SyncMode::Full => {
                self.submit_write_fsync(&file, &buf)?
            }
        };
        if written < data.len() {
            return Err(std::io::Error::new(
                std::io::ErrorKind::WriteZero,
                format!("short write: {} of {} bytes", written, data.len()),
            ));
        }

        // Truncate to actual data length (O_DIRECT wrote aligned/padded size).
        file.set_len(data.len() as u64)?;
        drop(file); // Close before rename

        fs::rename(&temp_path, &path)?;
        // Durable modes must persist the rename too: fsync the parent
        // directory so the chunk's dir entry survives power loss.
        // None defers durability to a later barrier, so skip it.
        if !matches!(sync, crate::SyncMode::None) {
            super::fsync_parent_dir(&path)?;
        }

        // buf drops here — aligned memory freed
        Ok(())
    }

    fn read_chunk(&self, chunk_id: &[u8; 32], buffer: &mut [u8]) -> std::io::Result<usize> {
        let path = chunk_path(&self.base_path, chunk_id);

        // Get file size first (metadata doesn't need O_DIRECT).
        let metadata = fs::metadata(&path).map_err(|e| {
            if e.kind() == std::io::ErrorKind::NotFound {
                std::io::Error::new(std::io::ErrorKind::NotFound, "chunk not found")
            } else {
                e
            }
        })?;
        let file_size = metadata.len() as usize;

        // Open with O_DIRECT for aligned read.
        let file = fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_DIRECT)
            .open(&path)?;

        // Allocate aligned read buffer.
        let mut buf = AlignedBuf::new(file_size)?;

        // Read until we actually have `file_size` bytes.
        //
        // The count MUST be honoured. Discarding it and copying `file_size`
        // regardless meant a short read returned the unwritten tail of the
        // buffer as if it were data: zeros (AlignedBuf uses alloc_zeroed), but
        // reported as a complete, successful read. With `verify_checksums`
        // on that surfaced as a CRC error — misleading, since the disk was
        // fine and the read was short — and with it off it was silent
        // corruption. A short read is also precisely what a partially failing
        // device produces, so swallowing it defeats any device-health scheme
        // built on top.
        let mut total = 0usize;
        while total < file_size {
            // O_DIRECT needs the buffer address and file offset to stay
            // page-aligned, so resume at the page boundary at or below what
            // we have. Re-reading the few bytes after that boundary is
            // harmless and keeps the alignment contract intact.
            let resume = total & !(PAGE_SIZE - 1);
            let remaining = buf.aligned_len() - resume;
            // SAFETY: `resume < aligned_len` and the buffer owns `aligned_len`
            // bytes, so the offset pointer stays in bounds.
            let ptr = unsafe { buf.as_mut_ptr().add(resume) };
            let n = self.submit_read_at(&file, ptr, remaining as u32, resume as u64)?;
            if n == 0 {
                break; // EOF earlier than metadata claimed
            }
            let advanced = resume + n;
            if advanced <= total {
                break; // no forward progress — stop rather than spin
            }
            total = advanced;
        }

        if total < file_size {
            return Err(std::io::Error::new(
                std::io::ErrorKind::UnexpectedEof,
                format!("short read: got {total} of {file_size} bytes"),
            ));
        }

        let copy_len = file_size.min(buffer.len());
        buffer[..copy_len].copy_from_slice(&buf.as_slice()[..copy_len]);

        // file and buf drop here — fd closed, memory freed
        Ok(copy_len)
    }

    fn delete_chunk(&self, chunk_id: &[u8; 32]) -> std::io::Result<()> {
        let path = chunk_path(&self.base_path, chunk_id);
        match fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
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
            direct_io: true,
            async_io: true,
            polling: true,
            requires_alignment: true,
            min_alignment: PAGE_SIZE,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use sha2::{Digest, Sha256};

    fn content_id(data: &[u8]) -> [u8; 32] {
        let mut hasher = Sha256::new();
        hasher.update(data);
        let result = hasher.finalize();
        let mut id = [0u8; 32];
        id.copy_from_slice(&result);
        id
    }

    #[test]
    fn test_aligned_buf_raii() {
        // Verify no leak: create and drop many buffers.
        for size in [0, 1, 4095, 4096, 4097, 16 * 1024 * 1024] {
            let buf = AlignedBuf::new(size).unwrap();
            assert!(buf.allocated >= size);
            assert_eq!(buf.allocated % PAGE_SIZE, 0);
            drop(buf); // Should not leak
        }
    }

    #[test]
    fn test_aligned_buf_content() {
        let mut buf = AlignedBuf::new(100).unwrap();
        let slice = buf.as_mut_slice();
        slice[..5].copy_from_slice(b"hello");
        assert_eq!(&buf.as_slice()[..5], b"hello");
    }

    // `read_chunk` honours the byte count and resumes at a
    // page-aligned offset. Sizes straddling page boundaries are where that
    // arithmetic can go wrong — an off-by-one in `resume` would silently
    // corrupt the tail, which is exactly the failure class being fixed.
    // Non-page-multiple sizes also force the padded-tail path.
    #[test]
    fn test_read_is_byte_exact_across_page_boundaries() {
        let dir = tempfile::tempdir().unwrap();
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();

        for size in [
            1usize,
            PAGE_SIZE - 1,
            PAGE_SIZE,
            PAGE_SIZE + 1,
            2 * PAGE_SIZE,
            3 * PAGE_SIZE - 1,
            3 * PAGE_SIZE + 17,
            64 * 1024 + 7,
        ] {
            // Position-dependent pattern: a misplaced resume shows up as a
            // mismatch rather than coincidentally-equal bytes.
            let data: Vec<u8> = (0..size).map(|i| (i % 251) as u8).collect();
            let id = content_id(&data);

            backend.write_chunk(&id, &data).unwrap();

            let mut buf = vec![0xAAu8; size + PAGE_SIZE];
            let n = backend.read_chunk(&id, &mut buf).unwrap();

            assert_eq!(n, size, "wrong length for size {size}");
            assert_eq!(&buf[..n], &data[..], "content mismatch at size {size}");
            // Nothing beyond the reported length may be touched.
            assert!(
                buf[n..].iter().all(|&b| b == 0xAA),
                "wrote past the reported length at size {size}"
            );
        }
    }

    // A zero-length chunk must not spin in the read loop (`file_size == 0`
    // means the loop body never runs) and must report 0.
    #[test]
    fn test_read_empty_chunk_terminates() {
        let dir = tempfile::tempdir().unwrap();
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();

        let data: &[u8] = b"";
        let id = content_id(data);
        backend.write_chunk(&id, data).unwrap();

        let mut buf = vec![0u8; 4096];
        assert_eq!(backend.read_chunk(&id, &mut buf).unwrap(), 0);
    }

    #[test]
    fn test_write_read_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();

        let data = b"io_uring write test data for nvstor";
        let id = content_id(data);

        backend.write_chunk(&id, data).unwrap();

        let mut buf = vec![0u8; 4096];
        let n = backend.read_chunk(&id, &mut buf).unwrap();
        assert_eq!(n, data.len());
        assert_eq!(&buf[..n], data);
    }

    // A torn file left at a chunk's address by an unsynced write
    // before a crash must not make a later write a no-op.
    #[test]
    fn test_write_replaces_torn_file() {
        let dir = tempfile::tempdir().unwrap();
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();
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
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();
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
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();

        let data = b"dedup test via io_uring";
        let id = content_id(data);

        backend.write_chunk(&id, data).unwrap();
        backend.write_chunk(&id, data).unwrap(); // Replaces, same bytes

        let mut buf = vec![0u8; 4096];
        let n = backend.read_chunk(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], data);
    }

    #[test]
    fn test_read_nonexistent() {
        let dir = tempfile::tempdir().unwrap();
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();

        let id = [0xab; 32];
        let mut buf = vec![0u8; 4096];
        let result = backend.read_chunk(&id, &mut buf);
        assert!(result.is_err());
    }

    #[test]
    fn test_delete() {
        let dir = tempfile::tempdir().unwrap();
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();

        let data = b"delete me via io_uring";
        let id = content_id(data);

        backend.write_chunk(&id, data).unwrap();
        backend.delete_chunk(&id).unwrap();

        let mut buf = vec![0u8; 4096];
        assert!(backend.read_chunk(&id, &mut buf).is_err());
    }

    #[test]
    fn test_delete_idempotent() {
        let dir = tempfile::tempdir().unwrap();
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();

        let id = [0xff; 32];
        backend.delete_chunk(&id).unwrap();
        backend.delete_chunk(&id).unwrap();
    }

    #[test]
    fn test_large_chunk() {
        let dir = tempfile::tempdir().unwrap();
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();

        // 1 MiB chunk
        let data = vec![0xCDu8; 1024 * 1024];
        let id = content_id(&data);

        backend.write_chunk(&id, &data).unwrap();

        let mut buf = vec![0u8; 1024 * 1024];
        let n = backend.read_chunk(&id, &mut buf).unwrap();
        assert_eq!(n, data.len());
        assert_eq!(buf, data);
    }

    #[test]
    fn test_caps() {
        let dir = tempfile::tempdir().unwrap();
        let backend = IoUringBackend::new(dir.path().join("chunks")).unwrap();

        let caps = backend.caps();
        assert!(caps.direct_io);
        assert!(caps.async_io);
        assert!(caps.requires_alignment);
        assert_eq!(caps.min_alignment, PAGE_SIZE);
    }

    #[test]
    fn test_concurrent_io_uring() {
        let dir = tempfile::tempdir().unwrap();
        let backend = std::sync::Arc::new(IoUringBackend::new(dir.path().join("chunks")).unwrap());

        let handles: Vec<_> = (0..5)
            .map(|i| {
                let backend = backend.clone();
                std::thread::spawn(move || {
                    let data = format!("concurrent io_uring chunk {}", i);
                    let id = content_id(data.as_bytes());
                    backend.write_chunk(&id, data.as_bytes()).unwrap();

                    let mut buf = vec![0u8; 4096];
                    let n = backend.read_chunk(&id, &mut buf).unwrap();
                    assert_eq!(&buf[..n], data.as_bytes());
                })
            })
            .collect();

        for h in handles {
            h.join().unwrap();
        }
    }
}

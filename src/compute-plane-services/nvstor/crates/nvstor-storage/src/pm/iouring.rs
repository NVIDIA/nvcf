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

//! io_uring backend for `PmStore`.
//!
//! Same on-disk layout and per-volume sparse-file model as
//! [`super::posix::PosixPmStore`]; the difference is that pwrite /
//! pread / fsync go through io_uring SQEs instead of the matching
//! libc syscalls. The win shows up most on
//!
//! * **High-IOPS small writes** — submitting an SQE is cheaper than a
//!   `pwrite()` syscall, and we can chain a write+fsync as linked
//!   SQEs in one submit (`SyncMode::Data` / `SyncMode::Full`).
//! * **Many concurrent writers** — multiple operations share one
//!   ring; the kernel processes them as a batch.
//!
//! Each op submits and waits synchronously. That already removes the
//! syscall-per-op overhead and is the foundation for batched submission.
//!
//! This impl is gated on `feature = "io-uring"` (Linux-only).

#![cfg(target_os = "linux")]

use std::collections::HashMap;
use std::fs::{self, File, OpenOptions};
use std::io;
use std::os::unix::io::AsRawFd;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use io_uring::{opcode, types, IoUring};
use parking_lot::{Mutex, RwLock};
use uuid::Uuid;

use super::{Generation, PmStore, PmStoreStats, VersionedRead};
use crate::SyncMode;

/// Default submission queue depth for the shared ring.
///
/// Sized for "many concurrent in-flight ops on one volume" — NFS
/// session ca_maxops is typically 32, NBD `iodepth` per client is
/// commonly 32-128. 256 covers either with headroom.
const DEFAULT_QUEUE_DEPTH: u32 = 256;

/// Default LRU cap on cached file handles.
///
/// PmObjectStore opens one fd per S3 object, so a large multipart
/// upload with thousands of distinct keys would drive an unbounded
/// cache into the kernel's `ulimit -n` mid-upload with EMFILE. 1024
/// keeps hot working sets fast and bounds fd usage; tune via
/// `with_handle_cap` for workloads that touch more volumes
/// simultaneously than the cap.
const DEFAULT_HANDLE_CAP: usize = 1024;

/// Cached open file handle for a volume. `last_access` is a tick
/// counter (monotonic per store) so the LRU sweep finds the
/// stalest entry without keeping a separate order list.
struct OpenVolume {
    file: File,
    /// Atomic because the handle is shared via `Arc` — `set_size`
    /// updates it without needing exclusive access to the map entry.
    size: AtomicU64,
    last_access: AtomicU64,
    /// Which published file this handle has open. See [`Generation`].
    generation: Generation,
}

impl OpenVolume {
    fn size(&self) -> u64 {
        self.size.load(Ordering::Relaxed)
    }
}

/// io_uring-backed `PmStore`.
///
/// Sharded ring topology: one `IoUring` per shard, each behind its own
/// mutex. Volumes deterministically hash to a shard, so the SQE
/// stream for any single volume serializes (correctness — no races on
/// per-fd offsets), but unrelated volumes use independent rings.
/// Without sharding, one global Mutex<IoUring> caps the whole store at
/// roughly the single-ring submission rate; sharding lets N CPUs each
/// drive their own ring.
pub struct IoUringPmStore {
    base_path: PathBuf,
    /// Pins a volume's cached handle through I/O and excludes replacement,
    /// deletion, and resizing. Independent volumes use independent stripes.
    lifecycle: [RwLock<()>; 1024],
    rings: Vec<Mutex<IoUring>>,
    /// Handles are `Arc` so a caller clones the one it needs and DROPS the
    /// lock before submitting I/O. Holding the guard across a
    /// write+fsync serialized the entire store on NVMe latency; the slow
    /// path held the EXCLUSIVE lock, so a many-distinct-volumes workload
    /// (PmObjectStore / S3-RDMA large MPU) ran effectively single-threaded.
    handles: RwLock<HashMap<Uuid, Arc<OpenVolume>>>,
    stats: Arc<PmStoreStats>,
    /// LRU cap. When `handles.len() > handle_cap`, the next insert
    /// evicts the entry with the oldest `last_access`.
    handle_cap: usize,
    /// Monotonic tick for LRU ordering; stored into each
    /// `OpenVolume::last_access` on every access.
    next_tick: AtomicU64,
    /// Monotonic op-id source for io_uring `user_data`. Each submitted
    /// SQE gets a unique id so its completion is matched unambiguously
    /// — fixed tags made a stale/orphaned CQE from an earlier torn op
    /// indistinguishable from the current op's completion (a later op
    /// could consume it and return the wrong byte count/status).
    next_op_id: AtomicU64,
}

impl IoUringPmStore {
    pub fn new(base_path: impl Into<PathBuf>) -> io::Result<Self> {
        Self::with_queue_depth(base_path, DEFAULT_QUEUE_DEPTH)
    }

    /// Override the LRU cap on cached file handles. Useful for tests
    /// (set very small to force eviction) and for production tuning
    /// against `ulimit -n` on boxes that pack many concurrent
    /// volumes per node.
    pub fn with_handle_cap(mut self, cap: usize) -> Self {
        self.handle_cap = cap.max(1);
        self
    }

    pub fn with_queue_depth(base_path: impl Into<PathBuf>, queue_depth: u32) -> io::Result<Self> {
        // Default shard count: one ring per CPU, capped to keep memory
        // and fd usage sane on big boxes. Each ring holds `queue_depth`
        // SQEs; 16 rings × 256 SQEs = ~64KB of kernel memory total.
        let shards = std::thread::available_parallelism()
            .map(|n| n.get().min(16).max(1))
            .unwrap_or(4);
        Self::with_shards_and_queue_depth(base_path, shards, queue_depth)
    }

    pub fn with_shards_and_queue_depth(
        base_path: impl Into<PathBuf>,
        shards: usize,
        queue_depth: u32,
    ) -> io::Result<Self> {
        let base_path = base_path.into();
        fs::create_dir_all(&base_path)?;
        let mut rings = Vec::with_capacity(shards);
        for _ in 0..shards.max(1) {
            let r = IoUring::new(queue_depth)
                .map_err(|e| io::Error::new(io::ErrorKind::Other, format!("io_uring init: {e}")))?;
            rings.push(Mutex::new(r));
        }
        tracing::info!(
            path = %base_path.display(),
            shards = rings.len(),
            queue_depth,
            "io_uring PmStore initialized"
        );
        Ok(Self {
            base_path,
            lifecycle: std::array::from_fn(|_| RwLock::new(())),
            rings,
            handles: RwLock::new(HashMap::new()),
            stats: Arc::new(PmStoreStats::default()),
            handle_cap: DEFAULT_HANDLE_CAP,
            next_tick: AtomicU64::new(1),
            next_op_id: AtomicU64::new(1),
        })
    }

    /// Evict the LRU entry if the cache is over cap. Caller holds
    /// `handles.write()`. The dropped `OpenVolume` closes its `File`
    /// on drop, returning the fd to the OS.
    fn evict_lru_if_needed(&self, handles: &mut HashMap<Uuid, Arc<OpenVolume>>) {
        if handles.len() <= self.handle_cap {
            return;
        }
        // Single linear scan for the entry with the smallest
        // `last_access`. O(N) but N ≤ handle_cap, so a 1024-entry
        // sweep is on the order of a few µs — far cheaper than the
        // `open()` it lets us avoid amortizing.
        let mut oldest: Option<(Uuid, u64)> = None;
        for (id, v) in handles.iter() {
            let t = v.last_access.load(Ordering::Relaxed);
            match oldest {
                None => oldest = Some((*id, t)),
                Some((_, prev)) if t < prev => oldest = Some((*id, t)),
                _ => {}
            }
        }
        if let Some((victim_id, _)) = oldest {
            handles.remove(&victim_id);
        }
    }

    fn touch(&self, v: &OpenVolume) {
        v.last_access.store(
            self.next_tick.fetch_add(1, Ordering::Relaxed),
            Ordering::Relaxed,
        );
    }

    fn volume_path(&self, id: Uuid) -> PathBuf {
        self.base_path.join(format!("{}", id.hyphenated()))
    }

    /// Pick the ring shard for a given volume. Hashing on volume id
    /// keeps all SQEs for a single volume on one ring (so per-volume
    /// completion order matches submission order), while different
    /// volumes spread across shards.
    fn shard_for(&self, id: Uuid) -> &Mutex<IoUring> {
        // Last 8 bytes of the UUID are the most uniformly random.
        let bytes = id.as_bytes();
        let mut h = 0u64;
        for &b in &bytes[8..16] {
            h = h.wrapping_mul(0x100000001b3).wrapping_add(b as u64);
        }
        &self.rings[(h as usize) % self.rings.len()]
    }

    /// Get-or-open the cached file handle. Same shape as POSIX impl
    /// so the on-disk layout is interchangeable. Updates the LRU
    /// `last_access` tick on every call (hot path: the fast branch
    /// is one atomic store).
    fn handle<F, R>(&self, id: Uuid, f: F) -> io::Result<R>
    where
        F: FnOnce(&OpenVolume) -> io::Result<R>,
    {
        let _guard = self.lifecycle[id.as_u128() as usize % self.lifecycle.len()].read();
        // Clone the Arc and release the guard before calling `f` — `f`
        // submits a write/fsync, and a reader held across it blocks any
        // waiting writer (and, with writer-preference, all readers behind).
        let cached = {
            let handles = self.handles.read();
            handles.get(&id).map(Arc::clone)
        };
        if let Some(h) = cached {
            self.touch(&h);
            return f(&h);
        }
        let path = self.volume_path(id);
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .open(&path)
            .map_err(|e| {
                if e.kind() == io::ErrorKind::NotFound {
                    io::Error::new(
                        io::ErrorKind::NotFound,
                        format!("PM volume {id} not found at {path:?}"),
                    )
                } else {
                    e
                }
            })?;
        let meta = file.metadata()?;
        let size = meta.len();
        let generation = Generation::of(&meta);
        // Insert + evict under the lock (no I/O), then drop it and do the
        // work outside. Eviction runs *after* inserting the new entry so the
        // freshly-inserted volume isn't itself the victim. A racing opener
        // may have inserted first — use whichever handle is in the map so
        // both callers share one fd.
        let entry = {
            let mut handles = self.handles.write();
            let entry = Arc::clone(handles.entry(id).or_insert_with(|| {
                Arc::new(OpenVolume {
                    file,
                    size: AtomicU64::new(size),
                    last_access: AtomicU64::new(0),
                    generation,
                })
            }));
            self.evict_lru_if_needed(&mut handles);
            entry
        };
        self.touch(&entry);
        f(&entry)
    }

    /// Allocate a unique `user_data` for one SQE.
    fn op_id(&self) -> u64 {
        self.next_op_id.fetch_add(1, Ordering::Relaxed)
    }

    /// Wait until a completion for every id in `wanted` has been seen,
    /// returning each id's result. Robust against:
    ///   - EINTR: `submit_and_wait` interrupted by a signal is retried,
    ///     NOT propagated (propagating would return while the kernel op
    ///     is still in flight, freeing the caller's buffer under an
    ///     active DMA).
    ///   - orphaned/stale CQEs: unmatched `user_data` values are drained
    ///     and ignored, so a leftover completion can't be mistaken for
    ///     one of ours.
    /// The ring lock is held by the caller for the whole submit+wait, so
    /// no other op interleaves on this ring.
    fn wait_for(ring: &mut IoUring, wanted: &[u64]) -> io::Result<[i32; 2]> {
        debug_assert!(wanted.len() <= 2);
        let mut results = [0i32; 2];
        let mut got = [false; 2];
        let mut remaining = wanted.len();
        while remaining > 0 {
            match ring.submit_and_wait(1) {
                Ok(_) => {}
                Err(ref e) if e.raw_os_error() == Some(libc::EINTR) => continue,
                Err(e) => return Err(e),
            }
            // Drain every currently-ready completion.
            loop {
                let cqe = match ring.completion().next() {
                    Some(c) => c,
                    None => break,
                };
                let ud = cqe.user_data();
                if let Some(i) = wanted.iter().position(|&w| w == ud) {
                    if !got[i] {
                        got[i] = true;
                        results[i] = cqe.result();
                        remaining -= 1;
                    }
                }
                // Non-matching (stale/orphan) CQEs are dropped.
            }
        }
        Ok(results)
    }

    /// Submit one Write SQE and wait for completion.
    fn submit_write(
        &self,
        ring_lock: &Mutex<IoUring>,
        fd: i32,
        buf: *const u8,
        len: u32,
        offset: u64,
    ) -> io::Result<usize> {
        let ud = self.op_id();
        let op = opcode::Write::new(types::Fd(fd), buf, len)
            .offset(offset)
            .build()
            .user_data(ud);
        let mut ring = ring_lock.lock();
        // Drain any stale CQEs left from a prior op before submitting.
        while ring.completion().next().is_some() {}
        unsafe {
            ring.submission()
                .push(&op)
                .map_err(|_| io::Error::new(io::ErrorKind::Other, "PM uring SQ full (write)"))?;
        }
        let [r] = {
            let r = Self::wait_for(&mut ring, &[ud])?;
            [r[0]]
        };
        if r < 0 {
            return Err(io::Error::from_raw_os_error(-r));
        }
        Ok(r as usize)
    }

    /// Submit Write+Fsync as linked SQEs. The IO_LINK flag chains
    /// the fsync after the write — one syscall, both ops, atomic
    /// "data on stable storage" semantics.
    fn submit_write_fsync(
        &self,
        ring_lock: &Mutex<IoUring>,
        fd: i32,
        buf: *const u8,
        len: u32,
        offset: u64,
        full: bool,
    ) -> io::Result<usize> {
        let write_ud = self.op_id();
        let fsync_ud = self.op_id();
        let write_op = opcode::Write::new(types::Fd(fd), buf, len)
            .offset(offset)
            .build()
            .flags(io_uring::squeue::Flags::IO_LINK)
            .user_data(write_ud);
        // Fsync.flags(IORING_FSYNC_DATASYNC) for sync_data; no flag
        // for full fsync.
        let fsync_flags = if full {
            0
        } else {
            types::FsyncFlags::DATASYNC.bits()
        };
        let fsync_op = opcode::Fsync::new(types::Fd(fd))
            .flags(types::FsyncFlags::from_bits_truncate(fsync_flags))
            .build()
            .user_data(fsync_ud);

        let mut ring = ring_lock.lock();
        while ring.completion().next().is_some() {}
        unsafe {
            ring.submission()
                .push(&write_op)
                .map_err(|_| io::Error::new(io::ErrorKind::Other, "PM uring SQ full (write)"))?;
            ring.submission()
                .push(&fsync_op)
                .map_err(|_| io::Error::new(io::ErrorKind::Other, "PM uring SQ full (fsync)"))?;
        }
        let [write_r, fsync_r] = Self::wait_for(&mut ring, &[write_ud, fsync_ud])?;
        if write_r < 0 {
            return Err(io::Error::from_raw_os_error(-write_r));
        }
        if fsync_r < 0 {
            return Err(io::Error::from_raw_os_error(-fsync_r));
        }
        Ok(write_r as usize)
    }

    /// Submit a Read SQE and wait for completion.
    fn submit_read(
        &self,
        ring_lock: &Mutex<IoUring>,
        fd: i32,
        buf: *mut u8,
        len: u32,
        offset: u64,
    ) -> io::Result<usize> {
        let ud = self.op_id();
        let op = opcode::Read::new(types::Fd(fd), buf, len)
            .offset(offset)
            .build()
            .user_data(ud);
        let mut ring = ring_lock.lock();
        while ring.completion().next().is_some() {}
        unsafe {
            ring.submission()
                .push(&op)
                .map_err(|_| io::Error::new(io::ErrorKind::Other, "PM uring SQ full (read)"))?;
        }
        let [r] = {
            let r = Self::wait_for(&mut ring, &[ud])?;
            [r[0]]
        };
        if r < 0 {
            return Err(io::Error::from_raw_os_error(-r));
        }
        Ok(r as usize)
    }

    /// Submit a standalone Fsync SQE.
    fn submit_fsync(&self, ring_lock: &Mutex<IoUring>, fd: i32, full: bool) -> io::Result<()> {
        let flags = if full {
            types::FsyncFlags::empty()
        } else {
            types::FsyncFlags::DATASYNC
        };
        let ud = self.op_id();
        let op = opcode::Fsync::new(types::Fd(fd))
            .flags(flags)
            .build()
            .user_data(ud);
        let mut ring = ring_lock.lock();
        while ring.completion().next().is_some() {}
        unsafe {
            ring.submission()
                .push(&op)
                .map_err(|_| io::Error::new(io::ErrorKind::Other, "PM uring SQ full (fsync)"))?;
        }
        let [r] = {
            let r = Self::wait_for(&mut ring, &[ud])?;
            [r[0]]
        };
        if r < 0 {
            return Err(io::Error::from_raw_os_error(-r));
        }
        Ok(())
    }
}

impl IoUringPmStore {
    /// Write a new generation into a staged file via `body`, sync it per
    /// `sync`, then atomically publish it as volume `id`. Only the
    /// publication step excludes readers; a failure anywhere removes the
    /// staged file and leaves the old generation in place.
    fn replace_staged(
        &self,
        id: Uuid,
        size: u64,
        sync: SyncMode,
        body: &mut dyn FnMut(&File) -> io::Result<()>,
    ) -> io::Result<()> {
        let staged = self.base_path.join(format!(".pending-{}", Uuid::new_v4()));
        let result = (|| {
            let file = OpenOptions::new()
                .read(true)
                .write(true)
                .create_new(true)
                .open(&staged)?;
            file.set_len(size)?;
            body(&file)?;
            match sync {
                SyncMode::None => {}
                SyncMode::Data => self.submit_fsync(self.shard_for(id), file.as_raw_fd(), false)?,
                SyncMode::Full => self.submit_fsync(self.shard_for(id), file.as_raw_fd(), true)?,
            }
            let _guard = self.lifecycle[id.as_u128() as usize % self.lifecycle.len()].write();
            let path = self.volume_path(id);
            fs::rename(&staged, &path)?;
            self.handles.write().remove(&id);
            if sync != SyncMode::None {
                crate::backend::fsync_parent_dir(&path)?;
            }
            Ok(())
        })();
        if result.is_err() {
            let _ = fs::remove_file(&staged);
        }
        self.stats.record_write(size, &result);
        result
    }

    /// Write all of `data` to the staged `file` at `offset` through the
    /// volume's ring (≤ 1 GiB per SQE).
    fn write_staged(&self, id: Uuid, file: &File, data: &[u8], offset: u64) -> io::Result<()> {
        let mut written = 0;
        while written < data.len() {
            let length = (data.len() - written).min(1024 * 1024 * 1024) as u32;
            let n = self.submit_write(
                self.shard_for(id),
                file.as_raw_fd(),
                data[written..].as_ptr(),
                length,
                offset + written as u64,
            )?;
            if n == 0 {
                return Err(io::Error::new(
                    io::ErrorKind::WriteZero,
                    "short staged object write",
                ));
            }
            written += n;
        }
        Ok(())
    }
}

impl PmStore for IoUringPmStore {
    fn stats(&self) -> Arc<PmStoreStats> {
        Arc::clone(&self.stats)
    }

    fn create(&self, id: Uuid, size: u64) -> io::Result<()> {
        let _guard = self.lifecycle[id.as_u128() as usize % self.lifecycle.len()].write();
        let path = self.volume_path(id);
        if path.exists() {
            return Err(io::Error::new(
                io::ErrorKind::AlreadyExists,
                format!("PM volume {id} already exists at {path:?}"),
            ));
        }
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(&path)?;
        file.set_len(size)?;
        let generation = Generation::of(&file.metadata()?);
        let mut handles = self.handles.write();
        let entry = Arc::new(OpenVolume {
            file,
            size: AtomicU64::new(size),
            last_access: AtomicU64::new(self.next_tick.fetch_add(1, Ordering::Relaxed)),
            generation,
        });
        handles.insert(id, entry);
        self.evict_lru_if_needed(&mut handles);
        self.stats
            .volumes_created_total
            .fetch_add(1, Ordering::Relaxed);
        Ok(())
    }

    fn write(&self, id: Uuid, offset: u64, data: &[u8], sync: SyncMode) -> io::Result<()> {
        if data.is_empty() {
            return Ok(());
        }
        let ring = self.shard_for(id);
        let bytes = data.len() as u64;
        let result = self.handle(id, |h| {
            if offset + data.len() as u64 > h.size() {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidInput,
                    format!(
                        "PM uring write {id}: offset {offset} + len {} exceeds size {}",
                        data.len(),
                        h.size()
                    ),
                ));
            }
            let fd = h.file.as_raw_fd();
            let mut written = 0;
            while written < data.len() {
                let chunk = &data[written..];
                let len = chunk.len() as u32;
                let off = offset + written as u64;
                let n = match sync {
                    SyncMode::None => self.submit_write(ring, fd, chunk.as_ptr(), len, off)?,
                    SyncMode::Data => {
                        self.submit_write_fsync(ring, fd, chunk.as_ptr(), len, off, false)?
                    }
                    SyncMode::Full => {
                        self.submit_write_fsync(ring, fd, chunk.as_ptr(), len, off, true)?
                    }
                };
                if n == 0 {
                    return Err(io::Error::new(
                        io::ErrorKind::WriteZero,
                        "PM uring write: 0 bytes written",
                    ));
                }
                written += n;
            }
            Ok(())
        });
        self.stats.record_write(bytes, &result);
        result
    }

    fn read_all(&self, id: Uuid) -> io::Result<Vec<u8>> {
        let result = self.handle(id, |h| {
            let size = usize::try_from(h.size()).map_err(|_| {
                io::Error::new(
                    io::ErrorKind::InvalidData,
                    "object size exceeds address space",
                )
            })?;
            let mut data = vec![0; size];
            let mut read = 0;
            while read < data.len() {
                let n = {
                    let length = (data.len() - read).min(1024 * 1024 * 1024) as u32;
                    self.submit_read(
                        self.shard_for(id),
                        h.file.as_raw_fd(),
                        data[read..].as_mut_ptr(),
                        length,
                        read as u64,
                    )?
                };
                if n == 0 {
                    return Err(io::Error::new(
                        io::ErrorKind::UnexpectedEof,
                        "short object generation",
                    ));
                }
                read += n;
            }
            Ok(data)
        });
        self.stats.record_read(
            &result
                .as_ref()
                .map(Vec::len)
                .map_err(|error| io::Error::new(error.kind(), error.to_string())),
        );
        result
    }

    fn replace(&self, id: Uuid, data: &[u8], sync: SyncMode) -> io::Result<()> {
        self.replace_staged(id, data.len() as u64, sync, &mut |file| {
            self.write_staged(id, file, data, 0)
        })
    }

    fn replace_from(
        &self,
        id: Uuid,
        size: u64,
        sync: SyncMode,
        fill: &mut dyn FnMut(&mut [u8]) -> io::Result<()>,
    ) -> io::Result<()> {
        self.replace_staged(id, size, sync, &mut |file| {
            let mut buf = vec![0u8; (size as usize).min(super::REPLACE_CHUNK)];
            let mut off = 0u64;
            while off < size {
                let n = (size - off).min(buf.len() as u64) as usize;
                fill(&mut buf[..n])?;
                self.write_staged(id, file, &buf[..n], off)?;
                off += n as u64;
            }
            Ok(())
        })
    }

    fn read(&self, id: Uuid, offset: u64, buf: &mut [u8]) -> io::Result<usize> {
        if buf.is_empty() {
            return Ok(0);
        }
        self.read_versioned(id, offset, buf).map(|r| r.len)
    }

    fn read_versioned(&self, id: Uuid, offset: u64, buf: &mut [u8]) -> io::Result<VersionedRead> {
        let ring = self.shard_for(id);
        let result = self.handle(id, |h| {
            if buf.is_empty() {
                return Ok(VersionedRead {
                    len: 0,
                    size: h.size(),
                    generation: h.generation,
                });
            }
            let want = (h.size().saturating_sub(offset) as usize).min(buf.len());
            if want == 0 {
                return Ok(VersionedRead {
                    len: 0,
                    size: h.size(),
                    generation: h.generation,
                });
            }
            let fd = h.file.as_raw_fd();
            let mut total = 0;
            while total < want {
                let slice = &mut buf[total..want];
                // One SQE carries a u32 length; cap like read_all does.
                let len = slice.len().min(1024 * 1024 * 1024) as u32;
                let off = offset + total as u64;
                let n = self.submit_read(ring, fd, slice.as_mut_ptr(), len, off)?;
                if n == 0 {
                    for b in buf[total..want].iter_mut() {
                        *b = 0;
                    }
                    total = want;
                    break;
                }
                total += n;
            }
            Ok(VersionedRead {
                len: total,
                size: h.size(),
                generation: h.generation,
            })
        });
        self.stats.record_read(
            &result
                .as_ref()
                .map(|r| r.len)
                .map_err(|error| io::Error::new(error.kind(), error.to_string())),
        );
        result
    }

    fn stat(&self, id: Uuid) -> io::Result<(u64, Generation)> {
        self.handle(id, |h| Ok((h.size(), h.generation)))
    }

    fn flush_all(&self) -> io::Result<()> {
        for entry in std::fs::read_dir(&self.base_path)? {
            let entry = entry?;
            if Uuid::parse_str(&entry.file_name().to_string_lossy()).is_ok() {
                std::fs::File::open(entry.path())?.sync_all()?;
            }
        }
        std::fs::File::open(&self.base_path)?.sync_all()
    }

    fn flush(&self, id: Uuid, sync: SyncMode) -> io::Result<()> {
        let ring = self.shard_for(id);
        self.handle(id, |h| match sync {
            SyncMode::None => Ok(()),
            SyncMode::Data => self.submit_fsync(ring, h.file.as_raw_fd(), false),
            SyncMode::Full => self.submit_fsync(ring, h.file.as_raw_fd(), true),
        })
    }

    fn punch_hole(&self, id: Uuid, offset: u64, len: u64) -> io::Result<()> {
        if len == 0 {
            return Ok(());
        }
        // io_uring has fallocate but the syscall fastpath is plenty fast
        // for the relatively-rare punch path. Reuse the libc call.
        self.handle(id, |h| {
            let mode = 0x02 | 0x01; // FALLOC_FL_PUNCH_HOLE | FALLOC_FL_KEEP_SIZE
            let ret = unsafe {
                libc::fallocate(
                    h.file.as_raw_fd(),
                    mode,
                    offset as libc::off_t,
                    len as libc::off_t,
                )
            };
            if ret != 0 {
                return Err(io::Error::last_os_error());
            }
            Ok(())
        })
    }

    fn set_size(&self, id: Uuid, new_size: u64) -> io::Result<()> {
        let _guard = self.lifecycle[id.as_u128() as usize % self.lifecycle.len()].write();
        let path = self.volume_path(id);
        let mut handles = self.handles.write();
        if let Some(h) = handles.get(&id) {
            h.file.set_len(new_size)?;
            h.size.store(new_size, Ordering::Relaxed);
            h.last_access.store(
                self.next_tick.fetch_add(1, Ordering::Relaxed),
                Ordering::Relaxed,
            );
            return Ok(());
        }
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .open(&path)
            .map_err(|e| {
                if e.kind() == io::ErrorKind::NotFound {
                    io::Error::new(
                        io::ErrorKind::NotFound,
                        format!("PM volume {id} not found at {path:?}"),
                    )
                } else {
                    e
                }
            })?;
        file.set_len(new_size)?;
        let generation = Generation::of(&file.metadata()?);
        handles.insert(
            id,
            Arc::new(OpenVolume {
                file,
                size: AtomicU64::new(new_size),
                last_access: AtomicU64::new(self.next_tick.fetch_add(1, Ordering::Relaxed)),
                generation,
            }),
        );
        self.evict_lru_if_needed(&mut handles);
        Ok(())
    }

    fn size(&self, id: Uuid) -> io::Result<u64> {
        let _guard = self.lifecycle[id.as_u128() as usize % self.lifecycle.len()].read();
        if let Some(h) = self.handles.read().get(&id) {
            return Ok(h.size());
        }
        let path = self.volume_path(id);
        let m = fs::metadata(&path).map_err(|e| {
            if e.kind() == io::ErrorKind::NotFound {
                io::Error::new(io::ErrorKind::NotFound, format!("PM volume {id} not found"))
            } else {
                e
            }
        })?;
        Ok(m.len())
    }

    fn delete(&self, id: Uuid) -> io::Result<()> {
        let _guard = self.lifecycle[id.as_u128() as usize % self.lifecycle.len()].write();
        self.handles.write().remove(&id);
        self.stats
            .volumes_deleted_total
            .fetch_add(1, Ordering::Relaxed);
        let path = self.volume_path(id);
        match fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(e),
        }
    }
}

#[cfg(test)]
mod lru_tests {
    use super::*;
    use tempfile::TempDir;

    /// With cap=2, creating 3 volumes must drop the cache size back
    /// to 2 (the oldest LRU entry gets evicted). The file on disk
    /// stays; only the open fd is reclaimed.
    #[test]
    fn lru_evicts_when_over_cap() {
        let dir = TempDir::new().unwrap();
        let store = IoUringPmStore::new(dir.path()).unwrap().with_handle_cap(2);

        let a = Uuid::new_v4();
        let b = Uuid::new_v4();
        let c = Uuid::new_v4();
        store.create(a, 4096).unwrap();
        store.create(b, 4096).unwrap();
        // Touch `a` so `b` becomes the LRU victim when `c` lands.
        store.write(a, 0, b"x", SyncMode::None).unwrap();
        store.create(c, 4096).unwrap();

        let cached: Vec<Uuid> = store.handles.read().keys().copied().collect();
        assert_eq!(cached.len(), 2);
        assert!(
            cached.contains(&a),
            "a should stay cached (touched recently)"
        );
        assert!(cached.contains(&c), "c should stay cached (just created)");
        assert!(!cached.contains(&b), "b should have been evicted as LRU");

        // `b` is still on disk: a fresh read reopens its file.
        let mut buf = [0u8; 1];
        store.read(b, 0, &mut buf).unwrap();
    }

    /// Read on a cached volume should update `last_access` so a
    /// subsequent eviction picks a different victim.
    #[test]
    fn lru_read_updates_access_tick() {
        let dir = TempDir::new().unwrap();
        let store = IoUringPmStore::new(dir.path()).unwrap().with_handle_cap(2);

        let a = Uuid::new_v4();
        let b = Uuid::new_v4();
        store.create(a, 4096).unwrap();
        store.create(b, 4096).unwrap();
        // Re-touch `a` via a read — `b` is now LRU.
        let mut buf = [0u8; 1];
        store.read(a, 0, &mut buf).unwrap();

        let c = Uuid::new_v4();
        store.create(c, 4096).unwrap();
        let cached: Vec<Uuid> = store.handles.read().keys().copied().collect();
        assert!(!cached.contains(&b), "b should be evicted, not a");
    }
}

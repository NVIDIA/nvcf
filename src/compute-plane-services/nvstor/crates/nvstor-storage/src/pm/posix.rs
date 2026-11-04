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

//! POSIX (`pwrite` / `pread` / `fallocate`) backend for `PmStore`.
//!
//! One sparse file per volume at `{base_path}/{uuid_hex}`. No chunk
//! indirection. No content hash. A 1 MiB write at offset 4 MiB hits
//! exactly that byte range — none of the read-modify-write
//! amplification the legacy ChunkManager volume_write does today.

use std::collections::HashMap;
use std::fs::{self, File, OpenOptions};
use std::io;
use std::os::unix::fs::FileExt;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use parking_lot::RwLock;
use uuid::Uuid;

use super::{Generation, PmStore, PmStoreStats, VersionedRead};
use crate::SyncMode;

const DEFAULT_HANDLE_CAP: usize = 1024;

/// Cached open file handle for a volume.
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

pub struct PosixPmStore {
    base_path: PathBuf,
    /// Pins a volume's cached handle through I/O and excludes replacement,
    /// deletion, and resizing. Independent volumes use independent stripes.
    lifecycle: [RwLock<()>; 1024],
    /// LRU-bounded file handle cache. Keeps the volume's File open
    /// across multiple writes / reads (the open()+close() per
    /// request would otherwise dominate small-write workloads), but
    /// caps at `handle_cap` to bound `ulimit -n` exposure under
    /// "many distinct volumes touched" workloads (PmObjectStore /
    /// S3-RDMA on large MPU uploads).
    /// Handles are `Arc` so a caller can clone the one it needs and DROP
    /// the lock before touching the disk. Holding the guard across a
    /// pwrite+fsync serialized the whole store on NVMe latency — worst on
    /// the slow path, which held the EXCLUSIVE lock across the I/O, so a
    /// workload touching many distinct volumes (PmObjectStore / S3-RDMA
    /// large MPU) ran effectively single-threaded.
    handles: RwLock<HashMap<Uuid, Arc<OpenVolume>>>,
    stats: Arc<PmStoreStats>,
    handle_cap: usize,
    next_tick: AtomicU64,
}

impl PosixPmStore {
    pub fn new(base_path: impl Into<PathBuf>) -> io::Result<Self> {
        let base_path = base_path.into();
        fs::create_dir_all(&base_path)?;
        Ok(Self {
            base_path,
            lifecycle: std::array::from_fn(|_| RwLock::new(())),
            handles: RwLock::new(HashMap::new()),
            stats: Arc::new(PmStoreStats::default()),
            handle_cap: DEFAULT_HANDLE_CAP,
            next_tick: AtomicU64::new(1),
        })
    }

    /// Override the LRU cap. Useful for tests + production tuning.
    pub fn with_handle_cap(mut self, cap: usize) -> Self {
        self.handle_cap = cap.max(1);
        self
    }

    fn touch(&self, v: &OpenVolume) {
        v.last_access.store(
            self.next_tick.fetch_add(1, Ordering::Relaxed),
            Ordering::Relaxed,
        );
    }

    fn evict_lru_if_needed(&self, handles: &mut HashMap<Uuid, Arc<OpenVolume>>) {
        if handles.len() <= self.handle_cap {
            return;
        }
        let mut oldest: Option<(Uuid, u64)> = None;
        for (id, v) in handles.iter() {
            let t = v.last_access.load(Ordering::Relaxed);
            match oldest {
                None => oldest = Some((*id, t)),
                Some((_, prev)) if t < prev => oldest = Some((*id, t)),
                _ => {}
            }
        }
        if let Some((victim, _)) = oldest {
            handles.remove(&victim);
        }
    }

    fn volume_path(&self, id: Uuid) -> PathBuf {
        self.base_path.join(format!("{}", id.hyphenated()))
    }

    /// Get-or-open the cached file handle for a volume. Opens with
    /// `O_RDWR` so the same handle services both reads and writes.
    /// Updates LRU `last_access` on every call.
    fn handle<F, R>(&self, id: Uuid, f: F) -> io::Result<R>
    where
        F: FnOnce(&OpenVolume) -> io::Result<R>,
    {
        let _guard = self.lifecycle[id.as_u128() as usize % self.lifecycle.len()].read();
        // Fast path: already cached. Clone the Arc and release the guard
        // BEFORE calling `f` — `f` is a pwrite/fsync, and a reader held
        // across it blocks any waiting writer (eviction / new volume),
        // which with parking_lot's writer-preference then blocks every
        // reader queued behind it.
        let cached = {
            let handles = self.handles.read();
            handles.get(&id).map(Arc::clone)
        };
        if let Some(h) = cached {
            self.touch(&h);
            return f(&h);
        }
        // Slow path: open and cache.
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
        let handle = Arc::new(OpenVolume {
            file,
            size: AtomicU64::new(size),
            last_access: AtomicU64::new(self.next_tick.fetch_add(1, Ordering::Relaxed)),
            generation,
        });
        // Insert + evict under the lock (no I/O), then drop it and do the
        // actual work outside. A racing opener may have inserted first —
        // use whichever handle is in the map so both callers share one fd.
        let entry = {
            let mut handles = self.handles.write();
            let entry = Arc::clone(handles.entry(id).or_insert(handle));
            self.evict_lru_if_needed(&mut handles);
            entry
        };
        self.touch(&entry);
        f(&entry)
    }
}

impl PosixPmStore {
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
                SyncMode::Data => file.sync_data()?,
                SyncMode::Full => file.sync_all()?,
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
}

impl PmStore for PosixPmStore {
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
        // Sparse file: create + ftruncate to logical size. No data
        // is written to disk until the first pwrite touches a byte.
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(&path)?;
        file.set_len(size)?;
        let generation = Generation::of(&file.metadata()?);
        // Persist the new volume's directory entry so it survives power
        // loss: a later durable pwrite fsyncs the file's data, but
        // without a parent-dir fsync the name→inode link can vanish on
        // crash, losing the whole volume despite Full-mode writes.
        crate::backend::fsync_parent_dir(&path)?;
        // Cache the handle.
        let mut handles = self.handles.write();
        handles.insert(
            id,
            Arc::new(OpenVolume {
                file,
                size: AtomicU64::new(size),
                last_access: AtomicU64::new(self.next_tick.fetch_add(1, Ordering::Relaxed)),
                generation,
            }),
        );
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
        let bytes = data.len() as u64;
        let result = self.handle(id, |h| {
            // Bounds check against the volume's logical size.
            if offset + data.len() as u64 > h.size() {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidInput,
                    format!(
                        "PM write {id}: offset {offset} + len {} exceeds volume size {}",
                        data.len(),
                        h.size(),
                    ),
                ));
            }
            // pwrite_all: keep writing until all bytes are accepted.
            let mut written = 0;
            while written < data.len() {
                let n = h.file.write_at(&data[written..], offset + written as u64)?;
                if n == 0 {
                    return Err(io::Error::new(
                        io::ErrorKind::WriteZero,
                        "PM write: pwrite returned 0",
                    ));
                }
                written += n;
            }
            match sync {
                SyncMode::None => {}
                SyncMode::Data => h.file.sync_data()?,
                SyncMode::Full => h.file.sync_all()?,
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
                let n = h.file.read_at(&mut data[read..], read as u64)?;
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
            let mut written = 0;
            while written < data.len() {
                let n = file.write_at(&data[written..], written as u64)?;
                if n == 0 {
                    return Err(io::Error::new(
                        io::ErrorKind::WriteZero,
                        "short staged object write",
                    ));
                }
                written += n;
            }
            Ok(())
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
                file.write_all_at(&buf[..n], off)?;
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
        let result = self.handle(id, |h| {
            if buf.is_empty() {
                return Ok(VersionedRead {
                    len: 0,
                    size: h.size(),
                    generation: h.generation,
                });
            }
            // Reads past the end of the volume return short. read_at
            // for sparse holes returns zeros, which is what NBD /
            // FUSE / NFS callers expect.
            let want = (h.size().saturating_sub(offset) as usize).min(buf.len());
            if want == 0 {
                return Ok(VersionedRead {
                    len: 0,
                    size: h.size(),
                    generation: h.generation,
                });
            }
            let mut total = 0;
            while total < want {
                let n = h
                    .file
                    .read_at(&mut buf[total..want], offset + total as u64)?;
                if n == 0 {
                    // Past end — fill the rest with zeros and stop.
                    for b in buf[total..want].iter_mut() {
                        *b = 0;
                    }
                    total = want;
                    break;
                }
                total += n;
            }
            // Anything beyond `want` (i.e. past volume size) stays as
            // whatever the caller passed in. Most callers zero-init.
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
        self.handle(id, |h| match sync {
            SyncMode::None => Ok(()),
            SyncMode::Data => h.file.sync_data(),
            SyncMode::Full => h.file.sync_all(),
        })
    }

    fn punch_hole(&self, id: Uuid, offset: u64, len: u64) -> io::Result<()> {
        if len == 0 {
            return Ok(());
        }
        self.handle(id, |h| {
            use std::os::unix::io::AsRawFd;
            // FALLOC_FL_PUNCH_HOLE = 0x02; FALLOC_FL_KEEP_SIZE = 0x01.
            // Combined → release backing pages, reads return zeros, file
            // logical size unchanged. Linux-specific; if we ever target
            // non-Linux we'll add an emulation here.
            let mode = 0x02 | 0x01;
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
        // Need to update both the on-disk size and the cached
        // OpenVolume.size. Take the write lock once, do both.
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
        // Not cached yet — open, resize, cache.
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
        // Don't go through `handle` — we want to fail with NotFound
        // rather than open a missing file.
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
        let path = self.volume_path(id);
        let result = match fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()), // idempotent
            Err(e) => Err(e),
        };
        if result.is_ok() {
            self.stats
                .volumes_deleted_total
                .fetch_add(1, Ordering::Relaxed);
        }
        result
    }
}

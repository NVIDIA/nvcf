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

//! Pass-through FUSE filesystem.
//!
//! Modeled after CVMFS's FUSE layer (3,251 LOC C++), adapted for Rust.
//! Key design decisions from CVMFS:
//! - Pre-built directory listing buffers (opendir caches, readdir serves)
//! - Per-operation performance counters and latency histograms
//! - Inode reference counting via forget/forget_multi
//! - FOPEN_KEEP_CACHE for kernel page cache integration
//! - Extensive tracing for production debugging

use std::collections::HashMap;
use std::ffi::OsStr;
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{FileTypeExt, MetadataExt};
use std::os::unix::io::AsRawFd;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use fuser::{
    FileAttr, FileType, Filesystem, ReplyAttr, ReplyData, ReplyDirectory, ReplyEntry, ReplyOpen,
    ReplyStatfs, ReplyXattr, Request,
};
use parking_lot::RwLock;
use tracing::{debug, info, warn};

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

/// Default TTL for read-only filesystem (content doesn't change).
const DEFAULT_TTL: Duration = Duration::from_secs(3600);
const GENERATION: u64 = 1;

/// Mount options.
#[derive(Debug, Clone)]
pub struct MountOptions {
    pub allow_other: bool,
    pub attr_timeout_secs: u64,
    pub entry_timeout_secs: u64,
}

impl Default for MountOptions {
    fn default() -> Self {
        Self {
            allow_other: true,
            attr_timeout_secs: 3600,
            entry_timeout_secs: 3600,
        }
    }
}

// ---------------------------------------------------------------------------
// Per-operation metrics (like CVMFS perf::Inc + HighPrecisionTimer)
// ---------------------------------------------------------------------------

/// Atomic operation counters — lock-free, zero overhead.
pub struct FuseMetrics {
    pub lookups: AtomicU64,
    pub getattrs: AtomicU64,
    pub readdirs: AtomicU64,
    pub reads: AtomicU64,
    pub opens: AtomicU64,
    pub readlinks: AtomicU64,
    pub getxattrs: AtomicU64,
    pub listxattrs: AtomicU64,
    pub forgets: AtomicU64,
    pub opendirs: AtomicU64,
    pub releases: AtomicU64,
    pub statfs_calls: AtomicU64,
    pub errors: AtomicU64,
    // Latency tracking (sum of microseconds for averaging).
    pub lookup_us: AtomicU64,
    pub getattr_us: AtomicU64,
    pub read_us: AtomicU64,
    pub readdir_us: AtomicU64,
    pub bytes_read: AtomicU64,
    pub active_fh: AtomicU64,
}

impl FuseMetrics {
    pub fn new() -> Self {
        Self {
            lookups: AtomicU64::new(0),
            getattrs: AtomicU64::new(0),
            readdirs: AtomicU64::new(0),
            reads: AtomicU64::new(0),
            opens: AtomicU64::new(0),
            readlinks: AtomicU64::new(0),
            getxattrs: AtomicU64::new(0),
            listxattrs: AtomicU64::new(0),
            forgets: AtomicU64::new(0),
            opendirs: AtomicU64::new(0),
            releases: AtomicU64::new(0),
            statfs_calls: AtomicU64::new(0),
            errors: AtomicU64::new(0),
            lookup_us: AtomicU64::new(0),
            getattr_us: AtomicU64::new(0),
            read_us: AtomicU64::new(0),
            readdir_us: AtomicU64::new(0),
            bytes_read: AtomicU64::new(0),
            active_fh: AtomicU64::new(0),
        }
    }

    /// Average lookup latency in microseconds.
    pub fn avg_lookup_us(&self) -> u64 {
        let n = self.lookups.load(Ordering::Relaxed);
        if n == 0 {
            0
        } else {
            self.lookup_us.load(Ordering::Relaxed) / n
        }
    }

    pub fn avg_read_us(&self) -> u64 {
        let n = self.reads.load(Ordering::Relaxed);
        if n == 0 {
            0
        } else {
            self.read_us.load(Ordering::Relaxed) / n
        }
    }
}

// ---------------------------------------------------------------------------
// Cached directory listing (CVMFS DirectoryListing pattern)
// ---------------------------------------------------------------------------

/// Pre-built directory entry list, cached between opendir and releasedir.
struct DirListing {
    entries: Vec<(u64, FileType, String)>, // (ino, kind, name)
}

// ---------------------------------------------------------------------------
// PassthroughFs
// ---------------------------------------------------------------------------

/// Maximum concurrent open files (returns EMFILE when exceeded).
const MAX_OPEN_FILES: u64 = 65536;

pub struct PassthroughFs {
    source_dir: PathBuf,
    /// Inode → path mapping.
    ino_to_path: RwLock<HashMap<u64, PathBuf>>,
    /// Path → inode mapping.
    path_to_ino: RwLock<HashMap<PathBuf, u64>>,
    /// Inode reference counts (from kernel via forget).
    ino_refcount: RwLock<HashMap<u64, u64>>,
    /// Directory handles: fh → cached listing.
    dir_handles: RwLock<HashMap<u64, DirListing>>,
    /// Negative lookup cache: path → expiry time.
    /// Avoids re-statting paths known to not exist (critical for CDI hooks
    /// that probe thousands of non-existent files).
    neg_cache: RwLock<HashMap<PathBuf, Instant>>,
    /// Negative cache TTL (default: same as entry_ttl).
    neg_cache_ttl: Duration,
    /// Next directory handle ID.
    next_dh: AtomicU64,
    /// TTLs.
    attr_ttl: Duration,
    entry_ttl: Duration,
    /// Performance metrics.
    pub metrics: FuseMetrics,
}

impl PassthroughFs {
    pub fn new(source_dir: PathBuf) -> std::io::Result<Self> {
        if !source_dir.is_dir() {
            return Err(std::io::Error::new(
                std::io::ErrorKind::NotFound,
                format!("{} is not a directory", source_dir.display()),
            ));
        }

        let meta = std::fs::symlink_metadata(&source_dir)?;
        let root_ino = meta.ino();

        let mut ino_to_path = HashMap::new();
        let mut path_to_ino = HashMap::new();
        let mut ino_refcount = HashMap::new();

        ino_to_path.insert(1, source_dir.clone());
        path_to_ino.insert(source_dir.clone(), 1);
        ino_refcount.insert(1, u64::MAX); // Root never forgotten.
        if root_ino != 1 {
            ino_to_path.insert(root_ino, source_dir.clone());
        }

        info!(root = %source_dir.display(), root_ino, "FUSE passthrough initialized");

        Ok(Self {
            source_dir,
            ino_to_path: RwLock::new(ino_to_path),
            path_to_ino: RwLock::new(path_to_ino),
            ino_refcount: RwLock::new(ino_refcount),
            dir_handles: RwLock::new(HashMap::new()),
            neg_cache: RwLock::new(HashMap::new()),
            neg_cache_ttl: DEFAULT_TTL,
            next_dh: AtomicU64::new(1),
            attr_ttl: DEFAULT_TTL,
            entry_ttl: DEFAULT_TTL,
            metrics: FuseMetrics::new(),
        })
    }

    pub fn with_ttl(mut self, attr_secs: u64, entry_secs: u64) -> Self {
        self.attr_ttl = Duration::from_secs(attr_secs);
        self.entry_ttl = Duration::from_secs(entry_secs);
        self
    }

    pub fn mount(self, mountpoint: &Path, options: MountOptions) -> std::io::Result<()> {
        let mut fuse_options = vec![
            fuser::MountOption::RO,
            fuser::MountOption::DefaultPermissions,
            fuser::MountOption::FSName(String::from("nvstor")),
        ];
        if options.allow_other {
            fuse_options.push(fuser::MountOption::AllowOther);
        }
        fuser::mount2(self, mountpoint, &fuse_options)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::Other, e))
    }

    fn resolve(&self, path: &Path) -> std::io::Result<u64> {
        if let Some(ino) = self.path_to_ino.read().get(path) {
            return Ok(*ino);
        }
        let meta = std::fs::symlink_metadata(path)?;
        let ino = meta.ino();
        self.ino_to_path.write().insert(ino, path.to_path_buf());
        self.path_to_ino.write().insert(path.to_path_buf(), ino);
        self.ino_refcount.write().entry(ino).or_insert(0);
        Ok(ino)
    }

    fn path(&self, ino: u64) -> Option<PathBuf> {
        self.ino_to_path.read().get(&ino).cloned()
    }

    fn to_attr(&self, meta: &std::fs::Metadata, ino: u64) -> FileAttr {
        let kind = if meta.is_dir() {
            FileType::Directory
        } else if meta.file_type().is_symlink() {
            FileType::Symlink
        } else if meta.file_type().is_block_device() {
            FileType::BlockDevice
        } else if meta.file_type().is_char_device() {
            FileType::CharDevice
        } else if meta.file_type().is_fifo() {
            FileType::NamedPipe
        } else if meta.file_type().is_socket() {
            FileType::Socket
        } else {
            FileType::RegularFile
        };

        FileAttr {
            ino,
            size: meta.size(),
            blocks: (meta.size() + 511) / 512,
            atime: UNIX_EPOCH + Duration::from_secs(meta.atime().max(0) as u64),
            mtime: UNIX_EPOCH + Duration::from_secs(meta.mtime().max(0) as u64),
            ctime: UNIX_EPOCH + Duration::from_secs(meta.ctime().max(0) as u64),
            crtime: UNIX_EPOCH + Duration::from_secs(meta.mtime().max(0) as u64),
            kind,
            perm: (meta.mode() & 0o7777) as u16,
            nlink: meta.nlink() as u32,
            uid: meta.uid(),
            gid: meta.gid(),
            rdev: meta.rdev() as u32,
            blksize: 4096,
            flags: 0,
        }
    }

    /// Build directory listing (CVMFS opendir pattern).
    fn build_dir_listing(&self, dir_ino: u64) -> std::io::Result<DirListing> {
        let dir_path = self
            .path(dir_ino)
            .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::NotFound, "unknown inode"))?;

        let mut entries = Vec::new();

        // . and ..
        entries.push((dir_ino, FileType::Directory, ".".to_string()));
        let parent_ino = if dir_ino == 1 {
            1
        } else {
            dir_path
                .parent()
                .and_then(|p| self.path_to_ino.read().get(p).copied())
                .unwrap_or(1)
        };
        entries.push((parent_ino, FileType::Directory, "..".to_string()));

        for entry in std::fs::read_dir(&dir_path)? {
            let entry = entry?;
            let name = entry.file_name().to_string_lossy().to_string();
            let child_path = dir_path.join(&name);

            let meta = match std::fs::symlink_metadata(&child_path) {
                Ok(m) => m,
                Err(_) => continue,
            };

            let ino = self.resolve(&child_path).unwrap_or(meta.ino());
            let kind = if meta.is_dir() {
                FileType::Directory
            } else if meta.file_type().is_symlink() {
                FileType::Symlink
            } else {
                FileType::RegularFile
            };

            entries.push((ino, kind, name));
        }

        Ok(DirListing { entries })
    }
}

impl Filesystem for PassthroughFs {
    fn init(
        &mut self,
        _req: &Request,
        _config: &mut fuser::KernelConfig,
    ) -> Result<(), libc::c_int> {
        info!(source = %self.source_dir.display(), "FUSE filesystem initialized");
        Ok(())
    }

    fn destroy(&mut self) {
        info!(
            lookups = self.metrics.lookups.load(Ordering::Relaxed),
            reads = self.metrics.reads.load(Ordering::Relaxed),
            bytes = self.metrics.bytes_read.load(Ordering::Relaxed),
            errors = self.metrics.errors.load(Ordering::Relaxed),
            "FUSE filesystem destroyed"
        );
    }

    fn lookup(&mut self, _req: &Request, parent: u64, name: &OsStr, reply: ReplyEntry) {
        let start = Instant::now();
        self.metrics.lookups.fetch_add(1, Ordering::Relaxed);

        let parent_path = match self.path(parent) {
            Some(p) => p,
            None => {
                self.metrics.errors.fetch_add(1, Ordering::Relaxed);
                reply.error(libc::ENOENT);
                return;
            }
        };

        let child_path = parent_path.join(name);

        // Negative lookup cache: skip stat for known-missing paths.
        {
            let neg = self.neg_cache.read();
            if let Some(expiry) = neg.get(&child_path) {
                if Instant::now() < *expiry {
                    debug!(parent, name = %name.to_string_lossy(), "lookup: negative cache hit");
                    reply.error(libc::ENOENT);
                    return;
                }
            }
        }

        match std::fs::symlink_metadata(&child_path) {
            Ok(meta) => {
                let ino = match self.resolve(&child_path) {
                    Ok(i) => i,
                    Err(e) => {
                        warn!(path = %child_path.display(), error = %e, "lookup: resolve failed");
                        reply.error(libc::EIO);
                        return;
                    }
                };
                // Increment kernel reference count.
                *self.ino_refcount.write().entry(ino).or_insert(0) += 1;
                // Remove from negative cache if it was there.
                self.neg_cache.write().remove(&child_path);

                let attr = self.to_attr(&meta, ino);
                debug!(parent, name = %name.to_string_lossy(), ino, "lookup");
                reply.entry(&self.entry_ttl, &attr, GENERATION);
            }
            Err(e) => {
                // Cache the negative result.
                let expiry = Instant::now() + self.neg_cache_ttl;
                self.neg_cache.write().insert(child_path, expiry);
                debug!(parent, name = %name.to_string_lossy(), error = %e, "lookup: not found (cached)");
                reply.error(e.raw_os_error().unwrap_or(libc::ENOENT));
            }
        }

        self.metrics
            .lookup_us
            .fetch_add(start.elapsed().as_micros() as u64, Ordering::Relaxed);
    }

    fn forget(&mut self, _req: &Request, ino: u64, nlookup: u64) {
        self.metrics.forgets.fetch_add(1, Ordering::Relaxed);

        let mut refs = self.ino_refcount.write();
        if let Some(count) = refs.get_mut(&ino) {
            *count = count.saturating_sub(nlookup);
            if *count == 0 && ino != 1 {
                refs.remove(&ino);
                // Reclaim memory: remove inode from path maps.
                if let Some(path) = self.ino_to_path.write().remove(&ino) {
                    self.path_to_ino.write().remove(&path);
                }
                debug!(ino, "forget: inode released and cleaned up");
            }
        }

        // Periodically prune expired negative cache entries to bound memory.
        // Do this every 1000 forgets to amortize the cost.
        if self.metrics.forgets.load(Ordering::Relaxed) % 1000 == 0 {
            let now = Instant::now();
            self.neg_cache.write().retain(|_, expiry| *expiry > now);
        }
    }

    fn getattr(&mut self, _req: &Request, ino: u64, _fh: Option<u64>, reply: ReplyAttr) {
        let start = Instant::now();
        self.metrics.getattrs.fetch_add(1, Ordering::Relaxed);

        let path = match self.path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENOENT);
                return;
            }
        };

        match std::fs::symlink_metadata(&path) {
            Ok(meta) => {
                let attr = self.to_attr(&meta, ino);
                reply.attr(&self.attr_ttl, &attr);
            }
            Err(e) => {
                self.metrics.errors.fetch_add(1, Ordering::Relaxed);
                reply.error(e.raw_os_error().unwrap_or(libc::EIO));
            }
        }

        self.metrics
            .getattr_us
            .fetch_add(start.elapsed().as_micros() as u64, Ordering::Relaxed);
    }

    fn readlink(&mut self, _req: &Request, ino: u64, reply: ReplyData) {
        self.metrics.readlinks.fetch_add(1, Ordering::Relaxed);

        let path = match self.path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENOENT);
                return;
            }
        };

        match std::fs::read_link(&path) {
            Ok(target) => reply.data(target.as_os_str().as_bytes()),
            Err(e) => reply.error(e.raw_os_error().unwrap_or(libc::EIO)),
        }
    }

    fn open(&mut self, _req: &Request, ino: u64, flags: i32, reply: ReplyOpen) {
        self.metrics.opens.fetch_add(1, Ordering::Relaxed);

        if flags & (libc::O_WRONLY | libc::O_RDWR | libc::O_APPEND | libc::O_TRUNC) != 0 {
            reply.error(libc::EROFS);
            return;
        }

        if self.path(ino).is_none() {
            warn!(ino, "open: unknown inode");
            reply.error(libc::ENOENT);
            return;
        }

        // Enforce max open files limit.
        let current = self.metrics.active_fh.fetch_add(1, Ordering::Relaxed);
        if current >= MAX_OPEN_FILES {
            self.metrics.active_fh.fetch_sub(1, Ordering::Relaxed);
            self.metrics.errors.fetch_add(1, Ordering::Relaxed);
            warn!(current, max = MAX_OPEN_FILES, "open: too many open files");
            reply.error(libc::EMFILE);
            return;
        }

        // FOPEN_KEEP_CACHE: kernel caches file data. Safe for read-only FS.
        reply.opened(0, fuser::consts::FOPEN_KEEP_CACHE);
    }

    fn release(
        &mut self,
        _req: &Request,
        _ino: u64,
        _fh: u64,
        _flags: i32,
        _lock_owner: Option<u64>,
        _flush: bool,
        reply: fuser::ReplyEmpty,
    ) {
        self.metrics.releases.fetch_add(1, Ordering::Relaxed);
        self.metrics.active_fh.fetch_sub(1, Ordering::Relaxed);
        reply.ok();
    }

    fn opendir(&mut self, _req: &Request, ino: u64, _flags: i32, reply: ReplyOpen) {
        self.metrics.opendirs.fetch_add(1, Ordering::Relaxed);

        // Build and cache the directory listing (CVMFS pattern).
        match self.build_dir_listing(ino) {
            Ok(listing) => {
                let fh = self.next_dh.fetch_add(1, Ordering::Relaxed);
                self.dir_handles.write().insert(fh, listing);
                reply.opened(fh, 0);
            }
            Err(e) => {
                self.metrics.errors.fetch_add(1, Ordering::Relaxed);
                reply.error(e.raw_os_error().unwrap_or(libc::EIO));
            }
        }
    }

    fn releasedir(
        &mut self,
        _req: &Request,
        _ino: u64,
        fh: u64,
        _flags: i32,
        reply: fuser::ReplyEmpty,
    ) {
        self.dir_handles.write().remove(&fh);
        reply.ok();
    }

    fn readdir(
        &mut self,
        _req: &Request,
        _ino: u64,
        fh: u64,
        offset: i64,
        mut reply: ReplyDirectory,
    ) {
        let start = Instant::now();
        self.metrics.readdirs.fetch_add(1, Ordering::Relaxed);

        let handles = self.dir_handles.read();
        let listing = match handles.get(&fh) {
            Some(l) => l,
            None => {
                self.metrics.errors.fetch_add(1, Ordering::Relaxed);
                reply.error(libc::EBADF);
                return;
            }
        };

        for (i, (ino, kind, name)) in listing.entries.iter().enumerate().skip(offset as usize) {
            if reply.add(*ino, (i + 1) as i64, *kind, name) {
                break; // Buffer full.
            }
        }
        reply.ok();

        self.metrics
            .readdir_us
            .fetch_add(start.elapsed().as_micros() as u64, Ordering::Relaxed);
    }

    fn read(
        &mut self,
        _req: &Request,
        ino: u64,
        _fh: u64,
        offset: i64,
        size: u32,
        _flags: i32,
        _lock_owner: Option<u64>,
        reply: ReplyData,
    ) {
        let start = Instant::now();
        self.metrics.reads.fetch_add(1, Ordering::Relaxed);

        let path = match self.path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENOENT);
                return;
            }
        };

        let file = match std::fs::File::open(&path) {
            Ok(f) => f,
            Err(e) => {
                self.metrics.errors.fetch_add(1, Ordering::Relaxed);
                reply.error(e.raw_os_error().unwrap_or(libc::EIO));
                return;
            }
        };

        let mut buf = vec![0u8; size as usize];
        let n = unsafe {
            libc::pread(
                file.as_raw_fd(),
                buf.as_mut_ptr() as *mut libc::c_void,
                size as usize,
                offset as libc::off_t,
            )
        };

        if n < 0 {
            self.metrics.errors.fetch_add(1, Ordering::Relaxed);
            reply.error(
                std::io::Error::last_os_error()
                    .raw_os_error()
                    .unwrap_or(libc::EIO),
            );
        } else {
            self.metrics
                .bytes_read
                .fetch_add(n as u64, Ordering::Relaxed);
            reply.data(&buf[..n as usize]);
        }

        self.metrics
            .read_us
            .fetch_add(start.elapsed().as_micros() as u64, Ordering::Relaxed);
    }

    fn access(&mut self, _req: &Request, ino: u64, _mask: i32, reply: fuser::ReplyEmpty) {
        if self.path(ino).is_some() {
            reply.ok();
        } else {
            reply.error(libc::ENOENT);
        }
    }

    fn statfs(&mut self, _req: &Request, _ino: u64, reply: ReplyStatfs) {
        self.metrics.statfs_calls.fetch_add(1, Ordering::Relaxed);

        let c_path = std::ffi::CString::new(self.source_dir.to_string_lossy().as_bytes())
            .unwrap_or_default();
        let mut stat: libc::statvfs = unsafe { std::mem::zeroed() };
        let ret = unsafe { libc::statvfs(c_path.as_ptr(), &mut stat) };

        if ret == 0 {
            reply.statfs(
                stat.f_blocks,
                stat.f_bfree,
                stat.f_bavail,
                stat.f_files,
                stat.f_ffree,
                stat.f_bsize as u32,
                stat.f_namemax as u32,
                stat.f_frsize as u32,
            );
        } else {
            reply.statfs(
                1_000_000, 500_000, 500_000, 100_000, 50_000, 4096, 255, 4096,
            );
        }
    }

    fn getxattr(&mut self, _req: &Request, ino: u64, name: &OsStr, size: u32, reply: ReplyXattr) {
        self.metrics.getxattrs.fetch_add(1, Ordering::Relaxed);

        let path = match self.path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENOENT);
                return;
            }
        };

        let c_path = match std::ffi::CString::new(path.to_string_lossy().as_bytes()) {
            Ok(c) => c,
            Err(_) => {
                reply.error(libc::EINVAL);
                return;
            }
        };
        let c_name = match std::ffi::CString::new(name.as_bytes()) {
            Ok(c) => c,
            Err(_) => {
                reply.error(libc::EINVAL);
                return;
            }
        };

        if size == 0 {
            let ret = unsafe {
                libc::lgetxattr(c_path.as_ptr(), c_name.as_ptr(), std::ptr::null_mut(), 0)
            };
            if ret < 0 {
                reply.error(
                    std::io::Error::last_os_error()
                        .raw_os_error()
                        .unwrap_or(libc::ENODATA),
                );
            } else {
                reply.size(ret as u32);
            }
        } else {
            let mut buf = vec![0u8; size as usize];
            let ret = unsafe {
                libc::lgetxattr(
                    c_path.as_ptr(),
                    c_name.as_ptr(),
                    buf.as_mut_ptr() as *mut libc::c_void,
                    size as usize,
                )
            };
            if ret < 0 {
                reply.error(
                    std::io::Error::last_os_error()
                        .raw_os_error()
                        .unwrap_or(libc::ENODATA),
                );
            } else {
                reply.data(&buf[..ret as usize]);
            }
        }
    }

    fn listxattr(&mut self, _req: &Request, ino: u64, size: u32, reply: ReplyXattr) {
        self.metrics.listxattrs.fetch_add(1, Ordering::Relaxed);

        let path = match self.path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENOENT);
                return;
            }
        };

        let c_path = match std::ffi::CString::new(path.to_string_lossy().as_bytes()) {
            Ok(c) => c,
            Err(_) => {
                reply.error(libc::EINVAL);
                return;
            }
        };

        if size == 0 {
            let ret = unsafe { libc::llistxattr(c_path.as_ptr(), std::ptr::null_mut(), 0) };
            if ret < 0 {
                reply.error(
                    std::io::Error::last_os_error()
                        .raw_os_error()
                        .unwrap_or(libc::ENODATA),
                );
            } else {
                reply.size(ret as u32);
            }
        } else {
            let mut buf = vec![0u8; size as usize];
            let ret = unsafe {
                libc::llistxattr(
                    c_path.as_ptr(),
                    buf.as_mut_ptr() as *mut libc::c_char,
                    size as usize,
                )
            };
            if ret < 0 {
                reply.error(
                    std::io::Error::last_os_error()
                        .raw_os_error()
                        .unwrap_or(libc::ENODATA),
                );
            } else {
                reply.data(&buf[..ret as usize]);
            }
        }
    }

    // Write operations — all return EROFS.
    fn setattr(
        &mut self,
        _req: &Request,
        _ino: u64,
        _mode: Option<u32>,
        _uid: Option<u32>,
        _gid: Option<u32>,
        _size: Option<u64>,
        _atime: Option<fuser::TimeOrNow>,
        _mtime: Option<fuser::TimeOrNow>,
        _ctime: Option<SystemTime>,
        _fh: Option<u64>,
        _crtime: Option<SystemTime>,
        _chgtime: Option<SystemTime>,
        _bkuptime: Option<SystemTime>,
        _flags: Option<u32>,
        reply: ReplyAttr,
    ) {
        reply.error(libc::EROFS);
    }

    fn mkdir(&mut self, _req: &Request, _p: u64, _n: &OsStr, _m: u32, _u: u32, reply: ReplyEntry) {
        reply.error(libc::EROFS);
    }
    fn unlink(&mut self, _req: &Request, _p: u64, _n: &OsStr, reply: fuser::ReplyEmpty) {
        reply.error(libc::EROFS);
    }
    fn rmdir(&mut self, _req: &Request, _p: u64, _n: &OsStr, reply: fuser::ReplyEmpty) {
        reply.error(libc::EROFS);
    }
    fn write(
        &mut self,
        _req: &Request,
        _i: u64,
        _f: u64,
        _o: i64,
        _d: &[u8],
        _w: u32,
        _fl: i32,
        _l: Option<u64>,
        reply: fuser::ReplyWrite,
    ) {
        reply.error(libc::EROFS);
    }
    fn create(
        &mut self,
        _req: &Request,
        _p: u64,
        _n: &OsStr,
        _m: u32,
        _u: u32,
        _f: i32,
        reply: fuser::ReplyCreate,
    ) {
        reply.error(libc::EROFS);
    }
    fn rename(
        &mut self,
        _req: &Request,
        _p: u64,
        _n: &OsStr,
        _np: u64,
        _nn: &OsStr,
        _f: u32,
        reply: fuser::ReplyEmpty,
    ) {
        reply.error(libc::EROFS);
    }
    fn symlink(&mut self, _req: &Request, _p: u64, _l: &OsStr, _t: &Path, reply: ReplyEntry) {
        reply.error(libc::EROFS);
    }
    fn link(&mut self, _req: &Request, _i: u64, _np: u64, _nn: &OsStr, reply: ReplyEntry) {
        reply.error(libc::EROFS);
    }
    fn mknod(
        &mut self,
        _req: &Request,
        _p: u64,
        _n: &OsStr,
        _m: u32,
        _u: u32,
        _r: u32,
        reply: ReplyEntry,
    ) {
        reply.error(libc::EROFS);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    #[test]
    fn create_and_resolve() {
        let dir = tempfile::tempdir().expect("tmpdir");
        fs::write(dir.path().join("test.txt"), b"hello").expect("write");
        fs::create_dir(dir.path().join("sub")).expect("mkdir");
        fs::write(dir.path().join("sub/nested.txt"), b"nested").expect("write");

        let fs = PassthroughFs::new(dir.path().to_path_buf()).expect("create");
        assert!(fs.path(1).is_some());

        let ino = fs.resolve(&dir.path().join("test.txt")).expect("resolve");
        assert_ne!(ino, 0);
        assert_eq!(
            fs.resolve(&dir.path().join("test.txt"))
                .expect("re-resolve"),
            ino
        );
    }

    #[test]
    fn to_attr_complete_posix() {
        let dir = tempfile::tempdir().expect("tmpdir");
        fs::write(dir.path().join("f.txt"), b"12345").expect("write");

        let fs = PassthroughFs::new(dir.path().to_path_buf()).expect("create");
        let meta = fs::symlink_metadata(dir.path().join("f.txt")).expect("stat");
        let attr = fs.to_attr(&meta, 42);

        assert_eq!(attr.ino, 42);
        assert_eq!(attr.size, 5);
        assert_eq!(attr.kind, FileType::RegularFile);
        assert!(attr.perm > 0);
        assert!(attr.uid > 0);
        assert_eq!(attr.blocks, 1);
        assert_eq!(attr.blksize, 4096);
        assert!(attr.nlink >= 1);
    }

    #[test]
    fn dir_listing_cached() {
        let dir = tempfile::tempdir().expect("tmpdir");
        for i in 0..50 {
            fs::write(dir.path().join(format!("f{i:03}.txt")), format!("data{i}")).expect("write");
        }

        let fs = PassthroughFs::new(dir.path().to_path_buf()).expect("create");
        let listing = fs.build_dir_listing(1).expect("build listing");

        // 50 files + . + .. = 52 entries.
        assert_eq!(listing.entries.len(), 52);
        assert_eq!(listing.entries[0].2, ".");
        assert_eq!(listing.entries[1].2, "..");
    }

    #[test]
    fn symlink_type_detected() {
        let dir = tempfile::tempdir().expect("tmpdir");
        fs::write(dir.path().join("target.txt"), b"real").expect("write");
        std::os::unix::fs::symlink("target.txt", dir.path().join("link.txt")).expect("symlink");

        let fs = PassthroughFs::new(dir.path().to_path_buf()).expect("create");
        let meta = fs::symlink_metadata(dir.path().join("link.txt")).expect("stat");
        let attr = fs.to_attr(&meta, 99);
        assert_eq!(attr.kind, FileType::Symlink);
    }

    #[test]
    fn metrics_initialized() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let fs = PassthroughFs::new(dir.path().to_path_buf()).expect("create");
        assert_eq!(fs.metrics.lookups.load(Ordering::Relaxed), 0);
        assert_eq!(fs.metrics.reads.load(Ordering::Relaxed), 0);
    }

    #[test]
    fn nonexistent_source_fails() {
        assert!(PassthroughFs::new(PathBuf::from("/nonexistent/xyz")).is_err());
    }
}

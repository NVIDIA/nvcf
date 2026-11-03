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

//! Remote-backed FUSE filesystem — fetch on demand from an nvstor server.
//!
//! CVMFS-style architecture: FUSE client fetches files from a remote server
//! on first access, caches locally on disk.  Subsequent accesses hit cache.
//!
//! Architecture:
//!   FUSE kernel request → RemoteFs
//!     → Check local disk cache (by path hash)
//!     → Cache hit: serve from local file
//!     → Cache miss: fetch from remote server via RPC → cache → serve
//!
//! The remote server is nvstor-fs-serve (see fs_server.rs).

use std::collections::HashMap;
use std::ffi::OsStr;
use std::io::{Read, Write};
use std::net::TcpStream;
use std::os::unix::ffi::OsStrExt;
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use fuser::{
    FileAttr, FileType, Filesystem, ReplyAttr, ReplyData, ReplyDirectory, ReplyEntry, ReplyOpen,
    ReplyStatfs, ReplyXattr, Request,
};
use parking_lot::{Mutex, RwLock};
use tracing::{info, warn};

use crate::cache::SharedCache;

const DEFAULT_TTL: Duration = Duration::from_secs(3600);
const NEGATIVE_TTL: Duration = Duration::from_secs(60); // ENOENT cache — short, immutable layers
const GENERATION: u64 = 1;

// RPC opcodes (must match fs_server.rs).
const OP_STAT: u8 = 0x09;
// Older directory-listing opcode. Superseded by OP_LIST_DIR_STAT which returns
// stat data inline, but retained so fs_server.rs can speak to older clients.
#[allow(dead_code)]
const OP_LIST_DIR: u8 = 0x0A;
const OP_READLINK: u8 = 0x0B;
const OP_READ_FILE: u8 = 0x0C;
const OP_GETXATTR: u8 = 0x0D;
const OP_LISTXATTR: u8 = 0x0E;
const OP_LIST_DIR_STAT: u8 = 0x0F;

// Limits — prevent OOM from malicious or buggy server responses.
const MAX_RESPONSE_BYTES: usize = 256 * 1024 * 1024; // 256 MB
const MAX_READLINK_BYTES: usize = 4096;
const MAX_LISTDIR_BYTES: usize = 64 * 1024 * 1024; // 64 MB (list_dir_stat is larger)

const RPC_TIMEOUT: Duration = Duration::from_secs(30);
const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);

// Cache bounds — prevent unbounded memory growth at scale.
const MAX_STAT_CACHE_ENTRIES: usize = 50_000;
const MAX_NEGATIVE_CACHE_ENTRIES: usize = 10_000;
const MAX_INODE_MAP_ENTRIES: usize = 200_000;

// Connection pool.
const DEFAULT_POOL_SIZE: usize = 32;

// Circuit breaker — fast-fail when server is unreachable.
const CIRCUIT_BREAKER_THRESHOLD: u64 = 5; // consecutive failures to trip
const CIRCUIT_BREAKER_COOLDOWN: Duration = Duration::from_secs(10); // wait before retry

/// Parsed stat response from the server.
struct RemoteStat {
    file_type: u8,
    size: u64,
    mode: u32,
    uid: u32,
    gid: u32,
    mtime: i64,
    nlink: u32,
    rdev: u64,
    generation: Option<[u8; 40]>,
}

/// Cached metadata for a path.
struct CachedStat {
    stat: RemoteStat,
    cached_at: Instant,
}

/// Remote FUSE filesystem — fetches from nvstor server, caches locally.
pub struct RemoteFs {
    remote_addr: String,
    cache: SharedCache,
    ino_to_path: RwLock<HashMap<u64, String>>,
    path_to_ino: RwLock<HashMap<String, u64>>,
    next_ino: AtomicU64,
    next_dh: AtomicU64,
    attr_ttl: Duration,
    entry_ttl: Duration,
    pool_size: usize,
    /// Connection pool — multiple reusable TCP connections.
    pool: Mutex<Vec<TcpStream>>,
    /// In-memory metadata cache: path → stat (bounded, LRU eviction).
    stat_cache: RwLock<HashMap<String, CachedStat>>,
    /// Negative cache: paths confirmed to not exist (bounded, TTL-based).
    /// Prevents container runtimes from hammering the server probing
    /// .wh.*, security.capability, overlay xattrs, etc.
    neg_cache: RwLock<HashMap<String, Instant>>,
    /// Circuit breaker: fast-fail when server is unreachable.
    /// Prevents thundering herd of connection attempts when server is down.
    consecutive_failures: AtomicU64,
    last_failure: Mutex<Option<Instant>>,
    // Metrics.
    pub fetches: AtomicU64,
    pub cache_hits: AtomicU64,
    pub bytes_fetched: AtomicU64,
    pub rpc_errors: AtomicU64,
    pub stat_cache_hits: AtomicU64,
    pub neg_cache_hits: AtomicU64,
    pub circuit_breaker_trips: AtomicU64,
}

impl RemoteFs {
    pub fn new(remote_addr: &str, cache_dir: &Path, cache_max_bytes: u64) -> std::io::Result<Self> {
        let cache = SharedCache::new(cache_dir, cache_max_bytes)?;

        let mut ino_to_path = HashMap::new();
        let mut path_to_ino = HashMap::new();
        ino_to_path.insert(1, "/".to_string());
        path_to_ino.insert("/".to_string(), 1);

        info!(remote = %remote_addr, cache_dir = %cache_dir.display(),
              cache_max_mb = cache_max_bytes / (1024 * 1024),
              "remote FUSE filesystem initialized");

        Ok(Self {
            remote_addr: remote_addr.to_string(),
            cache,
            ino_to_path: RwLock::new(ino_to_path),
            path_to_ino: RwLock::new(path_to_ino),
            next_ino: AtomicU64::new(2),
            next_dh: AtomicU64::new(1),
            attr_ttl: DEFAULT_TTL,
            entry_ttl: DEFAULT_TTL,
            pool_size: DEFAULT_POOL_SIZE,
            pool: Mutex::new(Vec::with_capacity(DEFAULT_POOL_SIZE)),
            stat_cache: RwLock::new(HashMap::new()),
            neg_cache: RwLock::new(HashMap::new()),
            consecutive_failures: AtomicU64::new(0),
            last_failure: Mutex::new(None),
            fetches: AtomicU64::new(0),
            cache_hits: AtomicU64::new(0),
            bytes_fetched: AtomicU64::new(0),
            rpc_errors: AtomicU64::new(0),
            stat_cache_hits: AtomicU64::new(0),
            neg_cache_hits: AtomicU64::new(0),
            circuit_breaker_trips: AtomicU64::new(0),
        })
    }

    pub fn with_ttl(mut self, attr_secs: u64, entry_secs: u64) -> Self {
        self.attr_ttl = Duration::from_secs(attr_secs);
        self.entry_ttl = Duration::from_secs(entry_secs);
        self
    }

    pub fn mount(self, mountpoint: &Path, allow_other: bool) -> std::io::Result<()> {
        let mut opts = vec![
            fuser::MountOption::RO,
            fuser::MountOption::DefaultPermissions,
            fuser::MountOption::FSName(format!("nvstor-remote:{}", self.remote_addr)),
        ];
        if allow_other {
            opts.push(fuser::MountOption::AllowOther);
        }
        fuser::mount2(self, mountpoint, &opts)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::Other, e))
    }

    // -----------------------------------------------------------------------
    // Connection pool
    // -----------------------------------------------------------------------

    /// Create a fresh TCP connection to the remote server.
    fn new_conn(&self) -> std::io::Result<TcpStream> {
        let stream = TcpStream::connect_timeout(
            &self.remote_addr.parse().map_err(|e| {
                std::io::Error::new(std::io::ErrorKind::InvalidInput, format!("bad addr: {e}"))
            })?,
            CONNECT_TIMEOUT,
        )?;
        stream.set_read_timeout(Some(RPC_TIMEOUT))?;
        stream.set_write_timeout(Some(RPC_TIMEOUT))?;
        stream.set_nodelay(true)?;
        Ok(stream)
    }

    /// Check circuit breaker — fast-fail if server is known to be down.
    fn check_circuit_breaker(&self) -> std::io::Result<()> {
        let failures = self.consecutive_failures.load(Ordering::Relaxed);
        if failures >= CIRCUIT_BREAKER_THRESHOLD {
            let last = self.last_failure.lock();
            if let Some(t) = *last {
                if t.elapsed() < CIRCUIT_BREAKER_COOLDOWN {
                    self.circuit_breaker_trips.fetch_add(1, Ordering::Relaxed);
                    return Err(std::io::Error::new(
                        std::io::ErrorKind::ConnectionRefused,
                        format!(
                            "circuit breaker open: {} consecutive failures, cooldown {}s remaining",
                            failures,
                            (CIRCUIT_BREAKER_COOLDOWN - t.elapsed()).as_secs()
                        ),
                    ));
                }
            }
            // Cooldown expired — allow one attempt (half-open state).
        }
        Ok(())
    }

    /// Record a successful RPC — resets circuit breaker.
    fn record_success(&self) {
        self.consecutive_failures.store(0, Ordering::Relaxed);
    }

    /// Record a failed connection/RPC — increments circuit breaker counter.
    fn record_failure(&self) {
        self.consecutive_failures.fetch_add(1, Ordering::Relaxed);
        *self.last_failure.lock() = Some(Instant::now());
    }

    /// Get a connection from the pool, or create a new one.
    /// Validates the connection is alive with a non-blocking peek.
    /// Checks circuit breaker before attempting new connections.
    fn get_conn(&self) -> std::io::Result<TcpStream> {
        // Try pool first (doesn't need circuit breaker — connection already exists).
        if let Some(stream) = self.pool.lock().pop() {
            // Non-blocking peek to check if peer closed the connection.
            stream.set_nonblocking(true).ok();
            let mut peek_buf = [0u8; 1];
            let alive = match stream.peek(&mut peek_buf) {
                Ok(0) => false, // EOF — peer closed.
                Err(ref e) if e.kind() == std::io::ErrorKind::WouldBlock => true,
                Ok(_) => true,   // Unexpected data, but alive.
                Err(_) => false, // Error = dead.
            };
            stream.set_nonblocking(false).ok();
            if alive {
                stream.set_read_timeout(Some(RPC_TIMEOUT)).ok();
                stream.set_write_timeout(Some(RPC_TIMEOUT)).ok();
                return Ok(stream);
            }
            // Dead — fall through to create new.
        }
        // Need a new connection — check circuit breaker first.
        self.check_circuit_breaker()?;
        self.new_conn()
    }

    /// Return a healthy connection to the pool for reuse.
    fn return_conn(&self, stream: TcpStream) {
        let mut pool = self.pool.lock();
        if pool.len() < self.pool_size {
            pool.push(stream);
        }
        // If pool is full, connection is dropped (closed).
    }

    /// Discard a broken connection (do NOT return to pool).
    fn drop_conn(&self, _stream: TcpStream) {
        // Connection is dropped here — not returned to pool.
        // The drop() closes the TCP socket.
    }

    // -----------------------------------------------------------------------
    // RPC methods — each sends a request and reads a bounded response.
    // On ANY error, the connection is dropped (never left in corrupted state).
    // -----------------------------------------------------------------------

    /// Send an RPC request and read the response.
    /// On ANY error, the connection is discarded (never returned to pool).
    /// On success, the connection goes back to the pool.
    fn rpc_call(&self, req: &[u8], max_resp: usize) -> std::io::Result<Vec<u8>> {
        let mut stream = self.get_conn().map_err(|e| {
            self.record_failure();
            self.rpc_errors.fetch_add(1, Ordering::Relaxed);
            e
        })?;

        if let Err(e) = stream.write_all(req) {
            self.drop_conn(stream);
            self.record_failure();
            self.rpc_errors.fetch_add(1, Ordering::Relaxed);
            return Err(e);
        }

        let mut status = [0u8; 1];
        if let Err(e) = stream.read_exact(&mut status) {
            self.drop_conn(stream);
            self.record_failure();
            self.rpc_errors.fetch_add(1, Ordering::Relaxed);
            return Err(e);
        }

        let mut rlen_buf = [0u8; 4];
        if let Err(e) = stream.read_exact(&mut rlen_buf) {
            self.drop_conn(stream);
            self.record_failure();
            self.rpc_errors.fetch_add(1, Ordering::Relaxed);
            return Err(e);
        }
        let resp_len = u32::from_le_bytes(rlen_buf) as usize;

        if resp_len > max_resp {
            self.drop_conn(stream);
            self.record_failure();
            self.rpc_errors.fetch_add(1, Ordering::Relaxed);
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                format!("response too large: {} > {}", resp_len, max_resp),
            ));
        }

        let mut data = vec![0u8; resp_len];
        if resp_len > 0 {
            if let Err(e) = stream.read_exact(&mut data) {
                self.drop_conn(stream);
                self.record_failure();
                self.rpc_errors.fetch_add(1, Ordering::Relaxed);
                return Err(e);
            }
        }

        if status[0] != 0 {
            // Error response — connection is still healthy, return it.
            // This is an application-level error (e.g., file not found), not
            // a transport error, so we reset the circuit breaker.
            self.return_conn(stream);
            self.record_success();
            let msg = String::from_utf8_lossy(&data[..data.len().min(256)]).to_string();
            return Err(std::io::Error::new(std::io::ErrorKind::NotFound, msg));
        }

        self.return_conn(stream);
        self.record_success();
        Ok(data)
    }

    /// Build a standard RPC request: [op] [path_len:u16] [path]
    fn build_path_req(op: u8, path: &str) -> Vec<u8> {
        let path_bytes = path.as_bytes();
        let mut req = Vec::with_capacity(1 + 2 + path_bytes.len());
        req.push(op);
        req.extend_from_slice(&(path_bytes.len() as u16).to_le_bytes());
        req.extend_from_slice(path_bytes);
        req
    }

    fn fetch_stat(&self, path: &str) -> std::io::Result<RemoteStat> {
        let req = Self::build_path_req(OP_STAT, path);
        let data = self.rpc_call(&req, 128)?;
        if data.len() < 41 {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                format!("stat response too short: {} < 41", data.len()),
            ));
        }
        Ok(RemoteStat {
            file_type: data[0],
            size: u64::from_le_bytes(data[1..9].try_into().unwrap_or_default()),
            mode: u32::from_le_bytes(data[9..13].try_into().unwrap_or_default()),
            uid: u32::from_le_bytes(data[13..17].try_into().unwrap_or_default()),
            gid: u32::from_le_bytes(data[17..21].try_into().unwrap_or_default()),
            mtime: i64::from_le_bytes(data[21..29].try_into().unwrap_or_default()),
            nlink: u32::from_le_bytes(data[29..33].try_into().unwrap_or_default()),
            rdev: u64::from_le_bytes(data[33..41].try_into().unwrap_or_default()),
            generation: data.get(41..81).and_then(|bytes| bytes.try_into().ok()),
        })
    }

    /// Stat with in-memory cache + negative cache.
    /// 1. Check negative cache (ENOENT) — avoids re-probing non-existent paths
    /// 2. Check stat cache — avoids redundant RPCs for known paths
    /// 3. Fetch from remote — cache result (or cache ENOENT)
    fn cached_stat(&self, path: &str) -> std::io::Result<RemoteStat> {
        // 1. Check negative cache first — container runtimes probe many
        //    non-existent paths (.wh.*, security.capability, overlay xattrs).
        {
            let neg = self.neg_cache.read();
            if let Some(cached_at) = neg.get(path) {
                if cached_at.elapsed() < NEGATIVE_TTL {
                    self.neg_cache_hits.fetch_add(1, Ordering::Relaxed);
                    return Err(std::io::Error::new(
                        std::io::ErrorKind::NotFound,
                        "negative cache hit",
                    ));
                }
            }
        }

        // 2. Check stat cache.
        {
            let cache = self.stat_cache.read();
            if let Some(entry) = cache.get(path) {
                if entry.cached_at.elapsed() < self.attr_ttl {
                    self.stat_cache_hits.fetch_add(1, Ordering::Relaxed);
                    return Ok(RemoteStat {
                        file_type: entry.stat.file_type,
                        size: entry.stat.size,
                        mode: entry.stat.mode,
                        uid: entry.stat.uid,
                        gid: entry.stat.gid,
                        mtime: entry.stat.mtime,
                        nlink: entry.stat.nlink,
                        rdev: entry.stat.rdev,
                        generation: entry.stat.generation,
                    });
                }
            }
        }

        // 3. Cache miss — fetch from remote.
        match self.fetch_stat(path) {
            Ok(st) => {
                // Remove from negative cache if it was there.
                self.neg_cache.write().remove(path);
                // Insert into stat cache (with eviction if full).
                self.insert_stat_cache(path, &st);
                Ok(st)
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                // Cache the ENOENT so we don't re-probe.
                self.insert_neg_cache(path);
                Err(e)
            }
            Err(e) => Err(e), // Transport error — don't cache.
        }
    }

    /// Insert into stat cache with bounded eviction.
    /// When the cache exceeds MAX_STAT_CACHE_ENTRIES, evicts the oldest 10%.
    fn insert_stat_cache(&self, path: &str, st: &RemoteStat) {
        let mut cache = self.stat_cache.write();
        if cache.len() >= MAX_STAT_CACHE_ENTRIES {
            // Evict oldest 10% by cached_at timestamp.
            let evict_count = MAX_STAT_CACHE_ENTRIES / 10;
            let mut entries: Vec<(String, Instant)> = cache
                .iter()
                .map(|(k, v)| (k.clone(), v.cached_at))
                .collect();
            entries.sort_by_key(|(_, t)| *t);
            for (key, _) in entries.iter().take(evict_count) {
                cache.remove(key);
            }
        }
        cache.insert(
            path.to_string(),
            CachedStat {
                stat: RemoteStat {
                    file_type: st.file_type,
                    size: st.size,
                    mode: st.mode,
                    uid: st.uid,
                    gid: st.gid,
                    mtime: st.mtime,
                    nlink: st.nlink,
                    rdev: st.rdev,
                    generation: st.generation,
                },
                cached_at: Instant::now(),
            },
        );
    }

    /// Insert into negative cache with bounded eviction.
    fn insert_neg_cache(&self, path: &str) {
        let mut cache = self.neg_cache.write();
        if cache.len() >= MAX_NEGATIVE_CACHE_ENTRIES {
            // Evict expired entries first.
            cache.retain(|_, t| t.elapsed() < NEGATIVE_TTL);
            // If still full, drop oldest 10%.
            if cache.len() >= MAX_NEGATIVE_CACHE_ENTRIES {
                let evict_count = MAX_NEGATIVE_CACHE_ENTRIES / 10;
                let mut entries: Vec<(String, Instant)> =
                    cache.iter().map(|(k, v)| (k.clone(), *v)).collect();
                entries.sort_by_key(|(_, t)| *t);
                for (key, _) in entries.iter().take(evict_count) {
                    cache.remove(key);
                }
            }
        }
        cache.insert(path.to_string(), Instant::now());
    }

    /// Batch readdir + stat: fetches directory listing with full metadata for
    /// all children in ONE RPC.  Populates the stat cache so subsequent
    /// lookup/getattr calls hit memory instead of network.
    fn fetch_list_dir_stat(&self, path: &str) -> std::io::Result<Vec<(String, RemoteStat)>> {
        let req = Self::build_path_req(OP_LIST_DIR_STAT, path);
        let data = self.rpc_call(&req, MAX_LISTDIR_BYTES)?;

        let mut entries = Vec::new();
        let mut pos = 0;
        // Entry: [name_len:2] [name] [type:1] [size:8] [mode:4] [uid:4] [gid:4] [mtime:8] [nlink:4] [rdev:8] = 41 bytes + name
        while pos + 2 < data.len() {
            let name_len =
                u16::from_le_bytes(data[pos..pos + 2].try_into().unwrap_or_default()) as usize;
            pos += 2;
            if pos + name_len + 41 > data.len() {
                break;
            }
            let name = String::from_utf8_lossy(&data[pos..pos + name_len]).to_string();
            pos += name_len;

            let st = RemoteStat {
                file_type: data[pos],
                size: u64::from_le_bytes(data[pos + 1..pos + 9].try_into().unwrap_or_default()),
                mode: u32::from_le_bytes(data[pos + 9..pos + 13].try_into().unwrap_or_default()),
                uid: u32::from_le_bytes(data[pos + 13..pos + 17].try_into().unwrap_or_default()),
                gid: u32::from_le_bytes(data[pos + 17..pos + 21].try_into().unwrap_or_default()),
                mtime: i64::from_le_bytes(data[pos + 21..pos + 29].try_into().unwrap_or_default()),
                nlink: u32::from_le_bytes(data[pos + 29..pos + 33].try_into().unwrap_or_default()),
                rdev: u64::from_le_bytes(data[pos + 33..pos + 41].try_into().unwrap_or_default()),
                generation: None,
            };
            pos += 41;

            // Pre-populate stat cache for this child.
            let child_path = Self::child_path(path, &name);
            self.insert_stat_cache(&child_path, &st);

            entries.push((name, st));
        }
        Ok(entries)
    }

    /// Legacy directory listing (name+type only). Kept for protocol
    /// compatibility with older servers; current clients use
    /// `fetch_list_dir_stat` which returns stats inline.
    #[allow(dead_code)]
    fn fetch_list_dir(&self, path: &str) -> std::io::Result<Vec<(String, u8)>> {
        let req = Self::build_path_req(OP_LIST_DIR, path);
        let data = self.rpc_call(&req, MAX_LISTDIR_BYTES)?;

        let mut entries = Vec::new();
        let mut pos = 0;
        while pos + 2 < data.len() {
            let name_len =
                u16::from_le_bytes(data[pos..pos + 2].try_into().unwrap_or_default()) as usize;
            pos += 2;
            if pos + name_len + 1 > data.len() {
                break;
            }
            let name = String::from_utf8_lossy(&data[pos..pos + name_len]).to_string();
            pos += name_len;
            let file_type = data[pos];
            pos += 1;
            entries.push((name, file_type));
        }
        Ok(entries)
    }

    fn fetch_readlink(&self, path: &str) -> std::io::Result<Vec<u8>> {
        let req = Self::build_path_req(OP_READLINK, path);
        self.rpc_call(&req, MAX_READLINK_BYTES)
    }

    fn fetch_read_file(&self, path: &str, offset: u64, length: u32) -> std::io::Result<Vec<u8>> {
        self.fetches.fetch_add(1, Ordering::Relaxed);
        let path_bytes = path.as_bytes();
        let mut req = Vec::with_capacity(1 + 2 + path_bytes.len() + 8 + 4);
        req.push(OP_READ_FILE);
        req.extend_from_slice(&(path_bytes.len() as u16).to_le_bytes());
        req.extend_from_slice(path_bytes);
        req.extend_from_slice(&offset.to_le_bytes());
        req.extend_from_slice(&length.to_le_bytes());

        let data = self.rpc_call(&req, MAX_RESPONSE_BYTES)?;
        self.bytes_fetched
            .fetch_add(data.len() as u64, Ordering::Relaxed);
        Ok(data)
    }

    // -----------------------------------------------------------------------
    // Data access (with caching + chunked large file support)
    // -----------------------------------------------------------------------

    /// Threshold for whole-file caching vs chunked reads.
    /// Files smaller than this are fetched entirely and cached on disk.
    /// Files larger are read in chunks directly from the server — no full
    /// copy in memory. This prevents OOM on multi-GB model weight files.
    const WHOLE_FILE_CACHE_THRESHOLD: u64 = 64 * 1024 * 1024; // 64 MB

    /// Read file data for a FUSE read() request.
    /// Small files: fetch whole file, cache on disk, slice for the request.
    /// Large files: fetch only the requested chunk directly from server.
    fn read_data(&self, path: &str, offset: u64, size: u32) -> std::io::Result<Vec<u8>> {
        // Check file size from stat cache to decide strategy.
        let file_size = self.cached_stat(path).map(|st| st.size).unwrap_or(0);

        if file_size <= Self::WHOLE_FILE_CACHE_THRESHOLD {
            // Small file — fetch whole, cache, slice.
            self.get_data_cached(path, offset, size)
        } else {
            // Large file — chunked read, no full-file allocation.
            self.get_data_chunked(path, offset, size)
        }
    }

    /// Whole-file cache path: fetch entire file, store on disk, slice.
    /// Used for files ≤ 64MB (config files, shared libs, small binaries).
    fn get_data_cached(&self, path: &str, offset: u64, size: u32) -> std::io::Result<Vec<u8>> {
        let mut stat = self.cached_stat(path)?;
        if stat.generation.is_none() {
            // Directory-list metadata and legacy servers lack a generation.
            stat = self.fetch_stat(path)?;
            self.insert_stat_cache(path, &stat);
        }
        let Some(generation) = stat.generation else {
            // Legacy peers cannot safely identify persistent cached content.
            return self.fetch_read_file(path, offset, size);
        };
        let cache_key = cache_key(&self.remote_addr, path, stat.size, stat.mtime, &generation);

        if let Some(data) = self.cache.get(&cache_key) {
            self.cache_hits.fetch_add(1, Ordering::Relaxed);
            let start = (offset as usize).min(data.len());
            let end = (start + size as usize).min(data.len());
            return Ok(data[start..end].to_vec());
        }

        let data = self.fetch_read_file(path, 0, u32::MAX)?;

        // Only publish cached bytes if the source stayed on this generation.
        let after = self.fetch_stat(path)?;
        if after.generation == Some(generation)
            && after.size == data.len() as u64
            && after.mtime == stat.mtime
        {
            if let Err(e) = self.cache.put(&cache_key, &data) {
                warn!(path, error = %e, "failed to cache fetched data");
            }
        }
        self.insert_stat_cache(path, &after);

        let start = (offset as usize).min(data.len());
        let end = (start + size as usize).min(data.len());
        Ok(data[start..end].to_vec())
    }

    /// Chunked read path: fetch only the requested range from server.
    /// Used for files > 64MB (model weights, large binaries).
    /// Never allocates more than `size` bytes — safe for multi-GB files.
    fn get_data_chunked(&self, path: &str, offset: u64, size: u32) -> std::io::Result<Vec<u8>> {
        self.fetch_read_file(path, offset, size)
    }

    // -----------------------------------------------------------------------
    // Path/inode management
    // -----------------------------------------------------------------------

    fn resolve_path(&self, ino: u64) -> Option<String> {
        self.ino_to_path.read().get(&ino).cloned()
    }

    fn resolve_ino(&self, path: &str) -> u64 {
        if let Some(ino) = self.path_to_ino.read().get(path) {
            return *ino;
        }
        let ino = self.next_ino.fetch_add(1, Ordering::Relaxed);
        let mut ino_map = self.ino_to_path.write();
        let mut path_map = self.path_to_ino.write();

        // Bound inode maps — if we've accumulated too many entries,
        // log a warning. We can't evict arbitrarily since the kernel
        // holds references, but we track the growth.
        if ino_map.len() >= MAX_INODE_MAP_ENTRIES && ino_map.len() % 10_000 == 0 {
            warn!(
                entries = ino_map.len(),
                "inode map is very large — consider remounting"
            );
        }

        ino_map.insert(ino, path.to_string());
        path_map.insert(path.to_string(), ino);
        ino
    }

    fn child_path(parent: &str, name: &str) -> String {
        if parent == "/" {
            format!("/{name}")
        } else {
            format!("{parent}/{name}")
        }
    }

    fn stat_to_attr(&self, ino: u64, st: &RemoteStat) -> FileAttr {
        let kind = match st.file_type {
            1 => FileType::Directory,
            2 => FileType::Symlink,
            _ => FileType::RegularFile,
        };
        let mtime_sys = UNIX_EPOCH + Duration::from_secs(st.mtime.max(0) as u64);
        FileAttr {
            ino,
            size: st.size,
            blocks: (st.size + 511) / 512,
            atime: mtime_sys,
            mtime: mtime_sys,
            ctime: mtime_sys,
            crtime: mtime_sys,
            kind,
            perm: (st.mode & 0o7777) as u16,
            nlink: st.nlink,
            uid: st.uid,
            gid: st.gid,
            rdev: st.rdev as u32,
            blksize: 4096,
            flags: 0,
        }
    }

    fn make_dir_attr(&self, ino: u64) -> FileAttr {
        let now = SystemTime::now();
        FileAttr {
            ino,
            size: 4096,
            blocks: 8,
            atime: now,
            mtime: now,
            ctime: now,
            crtime: now,
            kind: FileType::Directory,
            perm: 0o555,
            nlink: 2,
            uid: 0,
            gid: 0,
            rdev: 0,
            blksize: 4096,
            flags: 0,
        }
    }
}

// ---------------------------------------------------------------------------
// FUSE implementation
// ---------------------------------------------------------------------------

impl Filesystem for RemoteFs {
    fn init(
        &mut self,
        _req: &Request,
        config: &mut fuser::KernelConfig,
    ) -> Result<(), libc::c_int> {
        // Enable kernel-side caching for maximum performance.
        // After first access, subsequent reads hit the kernel page cache.
        // Request kernel-level async read support.
        let _ = config.add_capabilities(fuser::consts::FUSE_ASYNC_READ);
        info!(remote = %self.remote_addr, "remote FUSE filesystem mounted");
        Ok(())
    }

    fn destroy(&mut self) {
        info!(
            fetches = self.fetches.load(Ordering::Relaxed),
            cache_hits = self.cache_hits.load(Ordering::Relaxed),
            bytes_fetched = self.bytes_fetched.load(Ordering::Relaxed),
            rpc_errors = self.rpc_errors.load(Ordering::Relaxed),
            stat_cache_hits = self.stat_cache_hits.load(Ordering::Relaxed),
            neg_cache_hits = self.neg_cache_hits.load(Ordering::Relaxed),
            circuit_breaker_trips = self.circuit_breaker_trips.load(Ordering::Relaxed),
            stat_cache_entries = self.stat_cache.read().len(),
            neg_cache_entries = self.neg_cache.read().len(),
            inode_entries = self.ino_to_path.read().len(),
            pool_size = self.pool.lock().len(),
            cache_entries = self.cache.entry_count(),
            cache_bytes = self.cache.current_bytes(),
            "remote FUSE filesystem destroyed"
        );
    }

    fn lookup(&mut self, _req: &Request, parent: u64, name: &OsStr, reply: ReplyEntry) {
        let parent_path = match self.resolve_path(parent) {
            Some(p) => p,
            None => {
                reply.error(libc::ENOENT);
                return;
            }
        };

        let child_path = Self::child_path(&parent_path, &name.to_string_lossy());
        let ino = self.resolve_ino(&child_path);

        match self.cached_stat(&child_path) {
            Ok(st) => {
                let attr = self.stat_to_attr(ino, &st);
                reply.entry(&self.entry_ttl, &attr, GENERATION);
            }
            Err(_) => {
                reply.error(libc::ENOENT);
            }
        }
    }

    fn getattr(&mut self, _req: &Request, ino: u64, _fh: Option<u64>, reply: ReplyAttr) {
        let path = match self.resolve_path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENOENT);
                return;
            }
        };

        match self.cached_stat(&path) {
            Ok(st) => {
                let attr = self.stat_to_attr(ino, &st);
                reply.attr(&self.attr_ttl, &attr);
            }
            Err(_) => {
                if path == "/" {
                    reply.attr(&self.attr_ttl, &self.make_dir_attr(ino));
                } else {
                    reply.error(libc::ENOENT);
                }
            }
        }
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
        let path = match self.resolve_path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENOENT);
                return;
            }
        };

        match self.read_data(&path, offset.max(0) as u64, size) {
            Ok(data) => {
                reply.data(&data);
            }
            Err(e) => {
                warn!(path, error = %e, "remote read failed");
                reply.error(libc::EIO);
            }
        }
    }

    fn open(&mut self, _req: &Request, _ino: u64, _flags: i32, reply: ReplyOpen) {
        reply.opened(0, fuser::consts::FOPEN_KEEP_CACHE);
    }

    fn opendir(&mut self, _req: &Request, _ino: u64, _flags: i32, reply: ReplyOpen) {
        let fh = self.next_dh.fetch_add(1, Ordering::Relaxed);
        reply.opened(fh, 0);
    }

    fn readdir(
        &mut self,
        _req: &Request,
        ino: u64,
        _fh: u64,
        offset: i64,
        mut reply: ReplyDirectory,
    ) {
        let path = match self.resolve_path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENOENT);
                return;
            }
        };

        // Build full entry list: . and .. plus remote listing.
        let mut entries: Vec<(u64, FileType, String)> = vec![
            (ino, FileType::Directory, ".".to_string()),
            (1, FileType::Directory, "..".to_string()),
        ];

        // Use batch list_dir_stat: one RPC fetches names + full metadata.
        // This pre-populates the stat cache so subsequent lookup/getattr
        // calls hit memory instead of making N more RPCs.
        match self.fetch_list_dir_stat(&path) {
            Ok(remote_entries) => {
                for (name, st) in remote_entries {
                    let child_path = Self::child_path(&path, &name);
                    let child_ino = self.resolve_ino(&child_path);
                    let kind = match st.file_type {
                        1 => FileType::Directory,
                        2 => FileType::Symlink,
                        _ => FileType::RegularFile,
                    };
                    entries.push((child_ino, kind, name));
                }
            }
            Err(e) => {
                warn!(path, error = %e, "remote readdir failed");
            }
        }

        // Emit entries starting from offset. The offset value returned by
        // reply.add is the *next* offset the kernel will use, so we use
        // 1-based absolute indices.
        for (idx, (entry_ino, kind, name)) in entries.iter().enumerate() {
            let entry_offset = (idx + 1) as i64;
            if entry_offset <= offset {
                continue;
            }
            if reply.add(*entry_ino, entry_offset, *kind, name) {
                break; // Reply buffer full.
            }
        }
        reply.ok();
    }

    fn readlink(&mut self, _req: &Request, ino: u64, reply: ReplyData) {
        let path = match self.resolve_path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENOENT);
                return;
            }
        };

        match self.fetch_readlink(&path) {
            Ok(target) => reply.data(&target),
            Err(e) => {
                warn!(path, error = %e, "remote readlink failed");
                reply.error(libc::EIO);
            }
        }
    }

    fn access(&mut self, _req: &Request, _ino: u64, _mask: i32, reply: fuser::ReplyEmpty) {
        reply.ok();
    }

    fn statfs(&mut self, _req: &Request, _ino: u64, reply: ReplyStatfs) {
        reply.statfs(
            1_000_000, 500_000, 500_000, 100_000, 50_000, 4096, 255, 4096,
        );
    }

    fn getxattr(&mut self, _req: &Request, ino: u64, name: &OsStr, size: u32, reply: ReplyXattr) {
        let path = match self.resolve_path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENODATA);
                return;
            }
        };

        // Build request: [op] [path_len:2] [path] [name_len:2] [name]
        let path_bytes = path.as_bytes();
        let name_bytes = name.as_bytes();
        let mut req = Vec::with_capacity(1 + 2 + path_bytes.len() + 2 + name_bytes.len());
        req.push(OP_GETXATTR);
        req.extend_from_slice(&(path_bytes.len() as u16).to_le_bytes());
        req.extend_from_slice(path_bytes);
        req.extend_from_slice(&(name_bytes.len() as u16).to_le_bytes());
        req.extend_from_slice(name_bytes);

        match self.rpc_call(&req, 65536) {
            Ok(data) => {
                if size == 0 {
                    reply.size(data.len() as u32);
                } else {
                    reply.data(&data);
                }
            }
            Err(_) => reply.error(libc::ENODATA),
        }
    }

    fn listxattr(&mut self, _req: &Request, ino: u64, size: u32, reply: ReplyXattr) {
        let path = match self.resolve_path(ino) {
            Some(p) => p,
            None => {
                reply.error(libc::ENODATA);
                return;
            }
        };

        let req = Self::build_path_req(OP_LISTXATTR, &path);
        match self.rpc_call(&req, 65536) {
            Ok(data) => {
                if size == 0 {
                    reply.size(data.len() as u32);
                } else {
                    reply.data(&data);
                }
            }
            Err(_) => reply.size(0), // No xattrs is fine.
        }
    }

    // Write ops — read-only filesystem.
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
    fn mkdir(&mut self, _r: &Request, _p: u64, _n: &OsStr, _m: u32, _u: u32, reply: ReplyEntry) {
        reply.error(libc::EROFS);
    }
    fn unlink(&mut self, _r: &Request, _p: u64, _n: &OsStr, reply: fuser::ReplyEmpty) {
        reply.error(libc::EROFS);
    }
    fn rmdir(&mut self, _r: &Request, _p: u64, _n: &OsStr, reply: fuser::ReplyEmpty) {
        reply.error(libc::EROFS);
    }
    fn write(
        &mut self,
        _r: &Request,
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
        _r: &Request,
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
        _r: &Request,
        _p: u64,
        _n: &OsStr,
        _np: u64,
        _nn: &OsStr,
        _f: u32,
        reply: fuser::ReplyEmpty,
    ) {
        reply.error(libc::EROFS);
    }
    fn symlink(&mut self, _r: &Request, _p: u64, _l: &OsStr, _t: &Path, reply: ReplyEntry) {
        reply.error(libc::EROFS);
    }
    fn link(&mut self, _r: &Request, _i: u64, _np: u64, _nn: &OsStr, reply: ReplyEntry) {
        reply.error(libc::EROFS);
    }
    fn mknod(
        &mut self,
        _r: &Request,
        _p: u64,
        _n: &OsStr,
        _m: u32,
        _u: u32,
        _rd: u32,
        reply: ReplyEntry,
    ) {
        reply.error(libc::EROFS);
    }
}

fn cache_key(remote: &str, path: &str, size: u64, mtime: i64, generation: &[u8; 40]) -> String {
    let mut identity = Vec::new();
    identity.extend_from_slice(&(remote.len() as u64).to_le_bytes());
    identity.extend_from_slice(remote.as_bytes());
    identity.extend_from_slice(&(path.len() as u64).to_le_bytes());
    identity.extend_from_slice(path.as_bytes());
    identity.extend_from_slice(&size.to_le_bytes());
    identity.extend_from_slice(&mtime.to_le_bytes());
    identity.extend_from_slice(generation);
    nvstor_core::types::ChunkId::from_content(&identity).to_string()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cache_key_deterministic() {
        let k1 = cache_key("host:9000", "/usr/lib/libcuda.so", 123, 456, &[0; 40]);
        let k2 = cache_key("host:9000", "/usr/lib/libcuda.so", 123, 456, &[0; 40]);
        assert_eq!(k1, k2);
        let k3 = cache_key("host:9000", "/usr/lib/libcudart.so", 123, 456, &[0; 40]);
        assert_ne!(k1, k3);
    }

    #[test]
    fn cache_identity_includes_source_and_generation() {
        let first = cache_key("first:9000", "/file", 5, 1, &[1; 40]);
        assert_ne!(first, cache_key("second:9000", "/file", 5, 1, &[1; 40]));
        assert_ne!(first, cache_key("first:9000", "/file", 5, 1, &[2; 40]));
    }

    #[test]
    fn remote_cache_refreshes_replaced_file_after_metadata_expiry() {
        let export = tempfile::tempdir().unwrap();
        let cache = tempfile::tempdir().unwrap();
        std::fs::write(export.path().join("file"), b"first").unwrap();
        let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        drop(listener);
        let server =
            crate::fs_server::FsServer::start_background(export.path().to_path_buf(), port);
        let addr = format!("127.0.0.1:{port}");
        for _ in 0..100 {
            if TcpStream::connect(&addr).is_ok() {
                break;
            }
            std::thread::sleep(Duration::from_millis(10));
        }
        let client = RemoteFs::new(&addr, cache.path(), 1024 * 1024).unwrap();
        assert_eq!(client.get_data_cached("/file", 0, 5).unwrap(), b"first");
        assert_eq!(client.get_data_cached("/file", 0, 5).unwrap(), b"first");
        assert_eq!(client.cache_hits.load(Ordering::Relaxed), 1);
        std::fs::write(export.path().join("replacement"), b"other").unwrap();
        std::fs::rename(
            export.path().join("replacement"),
            export.path().join("file"),
        )
        .unwrap();
        client.stat_cache.write().clear();
        assert_eq!(client.get_data_cached("/file", 0, 5).unwrap(), b"other");
        drop(client);
        server.shutdown();
    }

    #[test]
    fn child_path_construction() {
        assert_eq!(RemoteFs::child_path("/", "usr"), "/usr");
        assert_eq!(RemoteFs::child_path("/usr", "lib"), "/usr/lib");
        assert_eq!(
            RemoteFs::child_path("/usr/lib", "libcuda.so"),
            "/usr/lib/libcuda.so"
        );
    }

    /// Helper to create a RemoteFs for testing (no actual server connection).
    fn test_fs() -> RemoteFs {
        let dir = tempfile::tempdir().unwrap();
        RemoteFs::new("127.0.0.1:0", dir.path(), 1024 * 1024).unwrap()
    }

    #[test]
    fn negative_cache_remembers_enoent() {
        let fs = test_fs();
        // Insert a negative entry.
        fs.insert_neg_cache("/nonexistent/file.txt");

        // Should be in negative cache.
        let neg = fs.neg_cache.read();
        assert!(neg.contains_key("/nonexistent/file.txt"));
    }

    #[test]
    fn negative_cache_bounded() {
        let fs = test_fs();
        // Fill beyond MAX_NEGATIVE_CACHE_ENTRIES.
        for i in 0..MAX_NEGATIVE_CACHE_ENTRIES + 100 {
            fs.insert_neg_cache(&format!("/path/{}", i));
        }
        // Should have evicted some entries.
        let neg = fs.neg_cache.read();
        assert!(neg.len() <= MAX_NEGATIVE_CACHE_ENTRIES);
    }

    #[test]
    fn stat_cache_bounded() {
        let fs = test_fs();
        let dummy_stat = RemoteStat {
            file_type: 0,
            size: 100,
            mode: 0o644,
            uid: 0,
            gid: 0,
            mtime: 0,
            nlink: 1,
            rdev: 0,
            generation: None,
        };
        // Fill beyond MAX_STAT_CACHE_ENTRIES.
        for i in 0..MAX_STAT_CACHE_ENTRIES + 100 {
            fs.insert_stat_cache(&format!("/file/{}", i), &dummy_stat);
        }
        // Should have evicted oldest 10% at some point.
        let cache = fs.stat_cache.read();
        assert!(cache.len() <= MAX_STAT_CACHE_ENTRIES);
    }

    #[test]
    fn circuit_breaker_trips_after_threshold() {
        let fs = test_fs();
        // Simulate consecutive failures.
        for _ in 0..CIRCUIT_BREAKER_THRESHOLD {
            fs.record_failure();
        }
        // Circuit breaker should be tripped.
        let result = fs.check_circuit_breaker();
        assert!(result.is_err());
        assert_eq!(
            result.unwrap_err().kind(),
            std::io::ErrorKind::ConnectionRefused
        );
    }

    #[test]
    fn circuit_breaker_resets_on_success() {
        let fs = test_fs();
        // Trip the breaker.
        for _ in 0..CIRCUIT_BREAKER_THRESHOLD {
            fs.record_failure();
        }
        assert!(fs.check_circuit_breaker().is_err());

        // Success resets the breaker.
        fs.record_success();
        assert!(fs.check_circuit_breaker().is_ok());
    }

    #[test]
    fn circuit_breaker_allows_after_cooldown() {
        let fs = test_fs();
        // Trip the breaker with a past timestamp.
        for _ in 0..CIRCUIT_BREAKER_THRESHOLD {
            fs.record_failure();
        }
        // Manually set last_failure far in the past.
        *fs.last_failure.lock() =
            Some(Instant::now() - CIRCUIT_BREAKER_COOLDOWN - Duration::from_secs(1));
        // Should be allowed (half-open state).
        assert!(fs.check_circuit_breaker().is_ok());
    }

    #[test]
    fn pool_size_default() {
        let fs = test_fs();
        assert_eq!(fs.pool_size, DEFAULT_POOL_SIZE);
        assert_eq!(fs.pool_size, 32);
    }

    #[test]
    fn inode_maps_consistent() {
        let fs = test_fs();
        let ino1 = fs.resolve_ino("/usr/lib/libcuda.so");
        let ino2 = fs.resolve_ino("/usr/lib/libcuda.so");
        assert_eq!(ino1, ino2); // Same path → same inode.

        let ino3 = fs.resolve_ino("/usr/lib/libcudart.so");
        assert_ne!(ino1, ino3); // Different path → different inode.

        // Reverse lookup works.
        assert_eq!(
            fs.resolve_path(ino1),
            Some("/usr/lib/libcuda.so".to_string())
        );
    }
}

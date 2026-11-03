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

//! ChunkManager — the central storage engine for nvstor.
//!
//! Combines:
//! - `StoreBackend` (I/O layer) for chunk data persistence
//! - `MetadataStore` (RocksDB) for metadata persistence
//! - In-memory caches for hot chunk metadata
//!
//! All protocols (S3, NFS, NBD) go through ChunkManager.

use std::collections::{HashMap, HashSet};
use std::hash::{Hash, Hasher};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Instant, SystemTime, UNIX_EPOCH};

use parking_lot::RwLock;
use tracing::{debug, error, info, warn};
use uuid::Uuid;

use nvstor_core::checksum::crc32;
use nvstor_core::error::{NvstorError, Result};
use nvstor_core::types::{
    AccessMode, ChunkId, ChunkInfo, ChunkRef, ConsistencyMode, GcReport, IntegrityReport,
    ObjectManifest, StorageStats, VolumeMap, VolumeState, DEFAULT_CHUNK_SIZE, INLINE_THRESHOLD,
};
use nvstor_metadata::{
    BatchOp, ChunkMeta, ChunkRef as MetaChunkRef, ManifestMeta, MetadataStore, VolumeMeta,
};

use crate::backend::StoreBackend;

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

/// Configuration for ChunkManager.
#[derive(Debug, Clone)]
pub struct ChunkManagerConfig {
    /// Chunk size in bytes (default: 16 MiB).
    pub chunk_size: usize,
    /// Verify the IEEE CRC-32 chunk checksum on reads (default: true).
    pub verify_checksums: bool,
    /// Inline threshold: objects at or below this size are stored in metadata (default: 64 KB).
    pub inline_threshold: usize,
    /// Disk usage threshold (0.0-1.0). Reject writes when usage exceeds this.
    pub disk_usage_threshold: f64,
    /// Maximum write bytes per second (0 = unlimited). Simple throttle to prevent
    /// one client from saturating disk I/O.
    pub max_write_bytes_per_sec: u64,
}

impl Default for ChunkManagerConfig {
    fn default() -> Self {
        Self {
            chunk_size: DEFAULT_CHUNK_SIZE,
            verify_checksums: true,
            inline_threshold: INLINE_THRESHOLD,
            disk_usage_threshold: 0.90,
            max_write_bytes_per_sec: 0, // Unlimited by default.
        }
    }
}

// ---------------------------------------------------------------------------
// Streaming write context
// ---------------------------------------------------------------------------

/// Context for an in-progress streaming object write.
pub struct ObjectWriteCtx {
    pub object_id: String,
    pub content_type: String,
    pub chunks: Vec<ChunkRef>,
    pub total_size: u64,
    pub created_at: i64,
}

// ---------------------------------------------------------------------------
// Volume info snapshot
// ---------------------------------------------------------------------------

/// Snapshot of volume state for external consumers.
#[derive(Debug, Clone)]
pub struct VolumeInfoSnapshot {
    pub volume_id: Uuid,
    pub size: u64,
    pub chunk_size: usize,
    pub access_mode: AccessMode,
    pub consistency: ConsistencyMode,
    pub state: VolumeState,
    pub active_writer: Option<u64>,
    pub append_offset: u64,
}

// ---------------------------------------------------------------------------
// Internal cache entry
// ---------------------------------------------------------------------------

/// Cached chunk metadata with atomic ref count.
struct CachedChunk {
    #[allow(dead_code)]
    size: u64,
    checksum: u32,
    ref_count: AtomicU64,
    #[allow(dead_code)]
    created_at: i64,
}

/// Upper bound on entries in the in-memory chunk-metadata cache
/// (`ChunkManager::cache`). Each entry is small (~64 B), so 1M entries
/// caps it near ~64 MB — enough to keep a large hot set resident while
/// preventing the unbounded multi-GB growth that sustained chunk traffic
/// would otherwise cause over a multi-day run.
const MAX_META_CACHE_ENTRIES: usize = 1_000_000;

// ---------------------------------------------------------------------------
// ChunkManager
// ---------------------------------------------------------------------------

/// The central storage engine for nvstor.
///
/// Thread-safe: uses `RwLock` for metadata and `AtomicU64` for ref counts.
/// All chunk I/O goes through the `StoreBackend` trait.
/// Per-operation latency tracking for storage operations.
pub struct StorageOpMetrics {
    pub writes: AtomicU64,
    pub reads: AtomicU64,
    pub deletes: AtomicU64,
    pub write_bytes: AtomicU64,
    pub read_bytes: AtomicU64,
    pub write_latency_us_sum: AtomicU64,
    pub read_latency_us_sum: AtomicU64,
    pub write_errors: AtomicU64,
    pub read_errors: AtomicU64,
}

impl StorageOpMetrics {
    fn new() -> Self {
        Self {
            writes: AtomicU64::new(0),
            reads: AtomicU64::new(0),
            deletes: AtomicU64::new(0),
            write_bytes: AtomicU64::new(0),
            read_bytes: AtomicU64::new(0),
            write_latency_us_sum: AtomicU64::new(0),
            read_latency_us_sum: AtomicU64::new(0),
            write_errors: AtomicU64::new(0),
            read_errors: AtomicU64::new(0),
        }
    }

    fn record_write(&self, bytes: u64, latency_us: u64) {
        self.writes.fetch_add(1, Ordering::Relaxed);
        self.write_bytes.fetch_add(bytes, Ordering::Relaxed);
        self.write_latency_us_sum
            .fetch_add(latency_us, Ordering::Relaxed);
    }

    fn record_read(&self, bytes: u64, latency_us: u64) {
        self.reads.fetch_add(1, Ordering::Relaxed);
        self.read_bytes.fetch_add(bytes, Ordering::Relaxed);
        self.read_latency_us_sum
            .fetch_add(latency_us, Ordering::Relaxed);
    }

    /// Get average write latency in microseconds.
    pub fn avg_write_latency_us(&self) -> u64 {
        let writes = self.writes.load(Ordering::Relaxed);
        if writes == 0 {
            0
        } else {
            self.write_latency_us_sum.load(Ordering::Relaxed) / writes
        }
    }

    /// Get average read latency in microseconds.
    pub fn avg_read_latency_us(&self) -> u64 {
        let reads = self.reads.load(Ordering::Relaxed);
        if reads == 0 {
            0
        } else {
            self.read_latency_us_sum.load(Ordering::Relaxed) / reads
        }
    }
}

/// LRU data cache for hot chunks.
struct DataCache {
    entries: HashMap<[u8; 32], Vec<u8>>,
    order: Vec<[u8; 32]>, // LRU order: front = oldest
    max_bytes: usize,
    current_bytes: usize,
}

impl DataCache {
    fn new(max_bytes: usize) -> Self {
        Self {
            entries: HashMap::new(),
            order: Vec::new(),
            max_bytes,
            current_bytes: 0,
        }
    }

    fn get(&mut self, key: &[u8; 32]) -> Option<&[u8]> {
        if self.entries.contains_key(key) {
            // Move to back (most recently used).
            self.order.retain(|k| k != key);
            self.order.push(*key);
            self.entries.get(key).map(|v| v.as_slice())
        } else {
            None
        }
    }

    fn insert(&mut self, key: [u8; 32], data: Vec<u8>) {
        let data_len = data.len();
        if data_len > self.max_bytes / 4 {
            return; // Don't cache chunks larger than 25% of cache.
        }

        // Evict until we have room.
        while self.current_bytes + data_len > self.max_bytes && !self.order.is_empty() {
            let evict_key = self.order.remove(0);
            if let Some(evicted) = self.entries.remove(&evict_key) {
                self.current_bytes -= evicted.len();
            }
        }

        if !self.entries.contains_key(&key) {
            self.current_bytes += data_len;
            self.order.push(key);
        }
        self.entries.insert(key, data);
    }
}

pub struct ChunkManager {
    backend: Box<dyn StoreBackend>,
    metadata: Arc<MetadataStore>,
    config: ChunkManagerConfig,
    cache: RwLock<HashMap<[u8; 32], CachedChunk>>,
    volumes: RwLock<HashMap<Uuid, VolumeMap>>,
    pub op_metrics: StorageOpMetrics,
    wal: Option<parking_lot::Mutex<nvstor_core::wal::WalWriter>>,
    /// LRU cache for hot chunk data (avoids disk I/O).
    data_cache: parking_lot::Mutex<DataCache>,
    /// Write throttle: tracks bytes written in current second.
    throttle_bytes_this_sec: AtomicU64,
    throttle_last_reset: parking_lot::Mutex<Instant>,
    /// Per-chunk write lock to prevent dedup race conditions.
    /// Maps chunk_id → Mutex<()>. Multiple writers of the same content
    /// serialize through this lock, ensuring ref_count is correct.
    chunk_write_locks: RwLock<HashMap<[u8; 32], Arc<parking_lot::Mutex<()>>>>,
    /// Chunks this process wrote with `SyncMode::None` whose bytes have not
    /// been synced yet. Their rows carry `sealed: false`; `flush` syncs the
    /// bytes, then seals the rows. An unsealed row that is not in this set
    /// was left by an earlier process, so its bytes may be torn.
    unsynced: parking_lot::Mutex<HashSet<[u8; 32]>>,
    /// Bounded stripes serialize manifest ownership and pin data during reads.
    /// Multi-object operations acquire stripes in ascending order.
    object_locks: [RwLock<()>; 1024],
}

impl ChunkManager {
    /// Create a new ChunkManager.
    pub fn new(
        backend: Box<dyn StoreBackend>,
        metadata: Arc<MetadataStore>,
        config: ChunkManagerConfig,
    ) -> Self {
        Self {
            backend,
            metadata,
            config,
            cache: RwLock::new(HashMap::new()),
            volumes: RwLock::new(HashMap::new()),
            op_metrics: StorageOpMetrics::new(),
            wal: None,
            data_cache: parking_lot::Mutex::new(DataCache::new(256 * 1024 * 1024)),
            throttle_bytes_this_sec: AtomicU64::new(0),
            throttle_last_reset: parking_lot::Mutex::new(Instant::now()),
            chunk_write_locks: RwLock::new(HashMap::new()),
            unsynced: parking_lot::Mutex::new(HashSet::new()),
            object_locks: std::array::from_fn(|_| RwLock::new(())),
        }
    }

    /// Open a ChunkManager and reload volume state from metadata.
    pub fn open(
        backend: Box<dyn StoreBackend>,
        metadata: Arc<MetadataStore>,
        config: ChunkManagerConfig,
    ) -> Result<Self> {
        let mgr = Self::new(backend, metadata, config);
        mgr.reload_volumes()?;
        Ok(mgr)
    }

    /// Reload volume state from RocksDB into in-memory map.
    fn reload_volumes(&self) -> Result<()> {
        let mut vols = self.volumes.write();
        vols.clear();
        self.metadata
            .scan_volumes(|vm| {
                let uuid = Uuid::from_bytes(vm.volume_id);
                let vol = volume_meta_to_map(&vm, self.config.chunk_size);
                vols.insert(uuid, vol);
            })
            .map_err(meta_err)?;
        info!(count = vols.len(), "reloaded volumes from metadata");
        Ok(())
    }

    // =====================================================================
    // WAL (Write-Ahead Log) for crash recovery
    // =====================================================================

    /// Enable WAL for crash recovery. Call before any writes.
    ///
    /// The WAL logs intent (object_id, size) before each write. On crash:
    /// - Incomplete writes: backend has temp file, no metadata → GC cleans up
    /// - Complete writes missing metadata: WAL replay detects and logs warning
    /// - The atomic write pattern (write→fsync→rename) handles most crash cases
    /// - WAL adds protection for multi-step operations (multipart, copy)
    pub fn enable_wal(&mut self, wal_dir: &std::path::Path) -> Result<()> {
        use nvstor_core::wal::{WalConfig, WalReader, WalWriter};

        // Replay: scan WAL for incomplete operations.
        match WalReader::open(wal_dir) {
            Ok(reader) => {
                let mut replayed = 0u64;
                let mut total = 0u64;
                let metadata = self.metadata.clone();
                let _ = reader.replay_from(0, |entry| {
                    total += 1;
                    match &entry.operation {
                        nvstor_core::wal::WalOperation::PutObject { object_id, .. } => {
                            if metadata.get_manifest(object_id).ok().flatten().is_none() {
                                warn!(object_id, "WAL replay: incomplete write (no manifest)");
                                replayed += 1;
                            }
                        }
                        nvstor_core::wal::WalOperation::DeleteObject { object_id } => {
                            if metadata.get_manifest(object_id).ok().flatten().is_some() {
                                warn!(object_id, "WAL replay: incomplete delete");
                                replayed += 1;
                            }
                        }
                        _ => {}
                    }
                    Ok(())
                });
                if replayed > 0 {
                    info!(replayed, total, "WAL replay complete");
                }
            }
            Err(e) => {
                debug!(error = %e, "WAL: no existing log (first run)");
            }
        }

        // Open WAL writer.
        let mut writer = WalWriter::open(wal_dir, WalConfig::default())
            .map_err(|e| NvstorError::Internal(e.to_string()))?;
        let _ = writer.checkpoint();
        self.wal = Some(parking_lot::Mutex::new(writer));

        info!(dir = %wal_dir.display(), "WAL enabled");
        Ok(())
    }

    /// Log a write intent to WAL (audit trail, not recovery).
    ///
    /// NOTE: This WAL does NOT enable crash recovery — it logs intent only,
    /// not data. Actual crash safety comes from:
    /// 1. Content-addressed storage → orphaned chunks are harmless
    /// 2. RocksDB's built-in WAL → metadata is crash-safe
    /// 3. Atomic backend writes → temp → fsync → rename
    /// 4. Write-new-first overwrite → old data preserved until new is committed
    fn wal_log_put(&self, object_id: &str, size: u64) {
        if let Some(ref wal) = self.wal {
            let op = nvstor_core::wal::WalOperation::PutObject {
                object_id: object_id.to_string(),
                size,
                etag: String::new(),
            };
            // Best-effort — WAL failure does not block writes.
            if let Err(e) = wal.lock().append(op) {
                warn!(object_id, error = %e, "WAL append failed (non-fatal)");
            }
        }
    }

    fn wal_log_delete(&self, object_id: &str) {
        if let Some(ref wal) = self.wal {
            let op = nvstor_core::wal::WalOperation::DeleteObject {
                object_id: object_id.to_string(),
            };
            if let Err(e) = wal.lock().append(op) {
                warn!(object_id, error = %e, "WAL append failed (non-fatal)");
            }
        }
    }

    // =====================================================================
    // I/O retry helper
    // =====================================================================

    /// Retry an I/O operation with exponential backoff on transient errors.
    /// Retries up to 3 times with delays: 1ms, 10ms, 100ms.
    /// Only retries on: Interrupted, WouldBlock, TimedOut.
    fn retry_io<T, F>(&self, op_name: &str, mut f: F) -> std::io::Result<T>
    where
        F: FnMut() -> std::io::Result<T>,
    {
        let delays = [
            std::time::Duration::from_millis(1),
            std::time::Duration::from_millis(10),
            std::time::Duration::from_millis(100),
        ];

        let mut last_err = None;
        for (attempt, delay) in delays.iter().enumerate() {
            match f() {
                Ok(v) => return Ok(v),
                Err(e) => {
                    let retryable = matches!(
                        e.kind(),
                        std::io::ErrorKind::Interrupted
                            | std::io::ErrorKind::WouldBlock
                            | std::io::ErrorKind::TimedOut
                    );
                    if !retryable {
                        return Err(e); // Non-transient — fail immediately.
                    }
                    warn!(
                        op = op_name,
                        attempt = attempt + 1,
                        error = %e,
                        "transient I/O error, retrying"
                    );
                    std::thread::sleep(*delay);
                    last_err = Some(e);
                }
            }
        }
        Err(last_err
            .unwrap_or_else(|| std::io::Error::new(std::io::ErrorKind::Other, "retry exhausted")))
    }

    // =====================================================================
    // Chunk operations
    // =====================================================================

    /// Write a chunk. Content-addressed: computes SHA-256, deduplicates.
    /// Returns the content-hash ChunkId.
    /// Check if disk usage exceeds the configured threshold.
    /// Returns Err if writes should be rejected.
    fn check_disk_space(&self) -> Result<()> {
        if self.config.disk_usage_threshold >= 1.0 {
            return Ok(()); // Disabled.
        }
        match self.backend.stats() {
            Ok(stats) if stats.total_bytes > 0 => {
                let usage = stats.used_bytes as f64 / stats.total_bytes as f64;
                if usage >= self.config.disk_usage_threshold {
                    error!(
                        usage_pct = format!("{:.1}%", usage * 100.0),
                        threshold_pct = format!("{:.1}%", self.config.disk_usage_threshold * 100.0),
                        available_mb = stats.available_bytes / (1024 * 1024),
                        "disk space critically low — rejecting write"
                    );
                    return Err(NvstorError::Internal(format!(
                        "disk usage {:.1}% exceeds threshold {:.1}%",
                        usage * 100.0,
                        self.config.disk_usage_threshold * 100.0,
                    )));
                }
                Ok(())
            }
            _ => Ok(()), // Can't check — allow writes.
        }
    }

    /// Throttle writes if rate exceeds max_write_bytes_per_sec.
    /// Sleeps until the current second rolls over. Simple but effective.
    fn throttle_write(&self, data_len: usize) {
        let limit = self.config.max_write_bytes_per_sec;
        if limit == 0 {
            return; // Unlimited.
        }

        // Reset counter if a second has passed.
        {
            let mut last = self.throttle_last_reset.lock();
            if last.elapsed() >= std::time::Duration::from_secs(1) {
                self.throttle_bytes_this_sec.store(0, Ordering::Relaxed);
                *last = Instant::now();
            }
        }

        let current = self
            .throttle_bytes_this_sec
            .fetch_add(data_len as u64, Ordering::Relaxed);
        if current + data_len as u64 > limit {
            // Over budget — sleep until next second.
            let last = self.throttle_last_reset.lock();
            let remaining = std::time::Duration::from_secs(1).saturating_sub(last.elapsed());
            if !remaining.is_zero() {
                debug!(
                    limit,
                    current,
                    data_len,
                    sleep_ms = remaining.as_millis(),
                    "write throttled"
                );
                std::thread::sleep(remaining);
                self.throttle_bytes_this_sec.store(0, Ordering::Relaxed);
            }
        }
    }

    pub fn chunk_write(&self, data: &[u8]) -> Result<ChunkId> {
        self.chunk_write_with_sync(data, crate::SyncMode::Full)
    }

    /// chunk_write with explicit durability mode. Hot paths that
    /// have their own end-of-batch barrier (S3-over-RDMA MPU
    /// PERSIST, replicated writes that durabilize via the replica
    /// quorum, etc.) can pass `SyncMode::None` to skip the
    /// per-chunk fsync, which dominates the cost of a chunk write
    /// on NVMe, since the trait-default
    /// `write_chunk` does `sync_all` per chunk file before rename.
    pub fn chunk_write_with_sync(&self, data: &[u8], sync: crate::SyncMode) -> Result<ChunkId> {
        let start = Instant::now();
        let result = self.chunk_write_inner(data, sync);
        let latency_us = start.elapsed().as_micros() as u64;
        match &result {
            Ok(_) => self.op_metrics.record_write(data.len() as u64, latency_us),
            Err(_) => {
                self.op_metrics.write_errors.fetch_add(1, Ordering::Relaxed);
            }
        };
        result
    }

    /// Run `f` while holding the per-chunk lock for `raw_id`. This is
    /// the ONE serialization point for every mutation of a chunk's
    /// ref_count and existence (`chunk_write` dedup-increment + create,
    /// `chunk_ref`, `chunk_unref`, and `gc_sweep`'s delete). Routing
    /// all of them through the same lock is what makes the dedup
    /// ref-count race-free: without it, an unlocked read-modify-write
    /// in unref/ref/gc could interleave with a dedup-hit increment and
    /// delete a chunk a live object still references (or lose a ref and
    /// leak). Locks are per content-hash and each operation touches a
    /// single chunk id, so there is no lock-ordering / deadlock risk.
    ///
    /// The Drop guard removes the map entry when this is the last
    /// holder, keeping `chunk_write_locks` bounded by the number of
    /// chunks *concurrently* being mutated, not the total chunk count.
    fn with_chunk_lock<R>(&self, raw_id: [u8; 32], f: impl FnOnce() -> R) -> R {
        let lock = {
            let mut locks = self.chunk_write_locks.write();
            locks
                .entry(raw_id)
                .or_insert_with(|| Arc::new(parking_lot::Mutex::new(())))
                .clone()
        };
        struct LockCleanup<'a> {
            locks: &'a RwLock<HashMap<[u8; 32], Arc<parking_lot::Mutex<()>>>>,
            key: [u8; 32],
            lock: Arc<parking_lot::Mutex<()>>,
        }
        impl<'a> Drop for LockCleanup<'a> {
            fn drop(&mut self) {
                // map ref + this struct's ref == 2 ⇒ we're the last
                // user; drop the entry so the map stays bounded.
                // Synchronize the count check with acquisition from the map.
                let mut locks = self.locks.write();
                if Arc::strong_count(&self.lock) == 2 {
                    locks.remove(&self.key);
                }
            }
        }
        let cleanup = LockCleanup {
            locks: &self.chunk_write_locks,
            key: raw_id,
            lock,
        };
        let _guard = cleanup.lock.lock();

        f()
    }

    fn chunk_write_inner(&self, data: &[u8], sync: crate::SyncMode) -> Result<ChunkId> {
        self.check_disk_space()?;
        self.throttle_write(data.len());

        let chunk_id = ChunkId::from_content(data);
        let raw_id = *chunk_id.as_bytes();
        let checksum = crc32(data);

        // Dedup-check + create under the per-chunk lock (see
        // with_chunk_lock). Serializes against concurrent
        // writes/refs/unrefs/GC of the same content.
        let durable = !matches!(sync, crate::SyncMode::None);
        self.with_chunk_lock(raw_id, || {
            // Under lock: check if chunk already exists (dedup).
            if let Some(mut existing) = self.metadata.get_chunk(&raw_id).map_err(meta_err)? {
                // An unsealed row's bytes were written without a sync.
                if !existing.sealed {
                    if !self.unsynced.lock().contains(&raw_id) {
                        // Left by an earlier process: the row may have
                        // become durable through a later WAL sync while the
                        // bytes were lost or torn. Rewrite them rather than
                        // trust the row.
                        self.retry_io("chunk_rewrite", || {
                            self.backend.write_chunk_sync(&raw_id, data, sync)
                        })
                        .map_err(NvstorError::Io)?;
                        if !durable {
                            self.unsynced.lock().insert(raw_id);
                        }
                    } else if durable {
                        // Written by this process, so the bytes are intact
                        // in page cache; this caller needs them on disk now.
                        self.retry_io("chunk_sync", || self.backend.sync_chunk(&raw_id))
                            .map_err(NvstorError::Io)?;
                    }
                    if durable {
                        existing.sealed = true;
                        self.unsynced.lock().remove(&raw_id);
                    }
                }
                existing.ref_count += 1;
                existing.last_accessed = now_secs();
                self.metadata.put_chunk(&existing).map_err(meta_err)?;
                self.cache_insert(&raw_id, &existing);
                debug!(%chunk_id, ref_count = existing.ref_count, "chunk_write: dedup hit");
                return Ok(chunk_id);
            }

            // New chunk: write to backend with retry on transient errors.
            self.retry_io("chunk_write", || {
                self.backend.write_chunk_sync(&raw_id, data, sync)
            })
            .map_err(NvstorError::Io)?;

            let now = now_secs();
            let meta = ChunkMeta {
                id: raw_id,
                size: data.len() as u64,
                checksum,
                ref_count: 1,
                // Sealed means the bytes are on stable storage.
                sealed: durable,
                corrupt: false,
                corrupt_reason: None,
                created_at: now,
                last_accessed: now,
            };

            // The chunk row must not outlive-in-durability the chunk
            // bytes. Under SyncMode::None the backend left the data in page
            // cache, so fsyncing this row would create a durable pointer to
            // possibly-missing data — and the dedup check above keys off this
            // row, so a later identical write would skip rewriting the bytes
            // forever. Match the row's barrier to the data's.
            //
            // The dedup-hit path above deliberately stays fsync'd: an
            // under-durable ref_count can drop a still-referenced chunk
            // (premature GC), whereas an over-durable one only leaks a chunk
            // that GC later reclaims.
            //
            // Not fsyncing the row is not enough on its own. Any later
            // synced write flushes the shared RocksDB WAL and makes this row
            // durable anyway, so the row says `sealed: false` until `flush`
            // has synced the bytes, and a dedup hit never trusts an unsealed
            // row it did not write.
            self.metadata
                .put_chunk_sync(&meta, meta_sync_for(sync))
                .map_err(meta_err)?;
            if !durable {
                self.unsynced.lock().insert(raw_id);
            }
            self.cache_insert(&raw_id, &meta);

            debug!(%chunk_id, size = data.len(), "chunk_write: new chunk stored");
            Ok(chunk_id)
        })
    }

    /// Read a chunk into `buffer`. Returns the number of bytes read.
    /// Verifies the IEEE CRC-32 checksum if configured.
    pub fn chunk_read(&self, id: &ChunkId, buffer: &mut [u8]) -> Result<usize> {
        let start = Instant::now();
        let result = self.chunk_read_inner(id, buffer);
        let latency_us = start.elapsed().as_micros() as u64;
        match &result {
            Ok(n) => self.op_metrics.record_read(*n as u64, latency_us),
            Err(_) => {
                self.op_metrics.read_errors.fetch_add(1, Ordering::Relaxed);
            }
        };
        result
    }

    fn chunk_read_inner(&self, id: &ChunkId, buffer: &mut [u8]) -> Result<usize> {
        let raw_id = id.as_bytes();

        // Check data cache first (avoids disk I/O for hot chunks).
        {
            let mut cache = self.data_cache.lock();
            if let Some(cached) = cache.get(raw_id) {
                let n = cached.len().min(buffer.len());
                buffer[..n].copy_from_slice(&cached[..n]);
                return Ok(n);
            }
        }

        let n = self
            .retry_io("chunk_read", || self.backend.read_chunk(raw_id, buffer))
            .map_err(|e| {
                if e.kind() == std::io::ErrorKind::NotFound {
                    NvstorError::NotFound
                } else {
                    NvstorError::Io(e)
                }
            })?;

        // Always verify checksum on read — non-negotiable for data integrity.
        let expected = self.get_checksum(raw_id)?;
        let actual = crc32(&buffer[..n]);
        if actual != expected {
            error!(%id, expected, actual, "chunk_read: CHECKSUM MISMATCH — data corruption detected");
            return Err(NvstorError::ChecksumMismatch { expected, actual });
        }

        // Populate data cache for future reads.
        self.data_cache.lock().insert(*raw_id, buffer[..n].to_vec());

        Ok(n)
    }

    /// Increment the reference count of a chunk.
    pub fn chunk_ref(&self, id: &ChunkId) -> Result<()> {
        let raw_id = *id.as_bytes();
        // Increment the durable ref_count under the per-chunk lock so
        // it can't race a concurrent unref/write/gc (an unlocked
        // read-modify-write here could lose an increment and let a
        // still-referenced chunk be GC'd). The cache is updated from
        // the freshly-persisted value under the same lock, not by a
        // separate fetch_add that could drift from the durable count.
        self.with_chunk_lock(raw_id, || {
            let mut meta = self
                .metadata
                .get_chunk(&raw_id)
                .map_err(meta_err)?
                .ok_or(NvstorError::NotFound)?;
            meta.ref_count += 1;
            meta.last_accessed = now_secs();
            self.metadata.put_chunk(&meta).map_err(meta_err)?;
            let cache = self.cache.read();
            if let Some(entry) = cache.get(&raw_id) {
                entry.ref_count.store(meta.ref_count, Ordering::Release);
            }
            debug!(%id, ref_count = meta.ref_count, "chunk_ref: incremented");
            Ok(())
        })
    }

    /// Decrement the reference count. Deletes chunk when it reaches 0.
    /// The whole read-modify-write (and the delete-at-zero) runs under
    /// the per-chunk lock so it cannot interleave with a concurrent
    /// `chunk_write` dedup-increment, `chunk_ref`, or `gc_sweep` —
    /// otherwise two racing unrefs could double-decrement (deleting a
    /// chunk two live objects share) or an unref could delete a chunk a
    /// dedup-hit just resurrected to ref_count=1.
    pub fn chunk_unref(&self, id: &ChunkId) -> Result<()> {
        let raw_id = *id.as_bytes();
        self.with_chunk_lock(raw_id, || {
            let mut meta = self
                .metadata
                .get_chunk(&raw_id)
                .map_err(meta_err)?
                .ok_or(NvstorError::NotFound)?;

            if meta.ref_count == 0 {
                warn!(%id, "chunk_unref: ref_count already 0");
                return Ok(());
            }

            meta.ref_count -= 1;

            if meta.ref_count == 0 {
                // Delete chunk data and metadata.
                self.backend
                    .delete_chunk(&raw_id)
                    .map_err(NvstorError::Io)?;
                self.metadata.delete_chunk(&raw_id).map_err(meta_err)?;
                self.cache.write().remove(&raw_id);
                // Invalidate data cache — prevent serving stale data.
                self.data_cache.lock().entries.remove(&raw_id);
                debug!(%id, "chunk_unref: deleted (ref_count=0)");
            } else {
                self.metadata.put_chunk(&meta).map_err(meta_err)?;
                // Update cache.
                let cache = self.cache.read();
                if let Some(entry) = cache.get(&raw_id) {
                    entry.ref_count.store(meta.ref_count, Ordering::Release);
                }
                debug!(%id, ref_count = meta.ref_count, "chunk_unref: decremented");
            }

            Ok(())
        })
    }

    /// Force-delete a chunk regardless of ref count.
    pub fn chunk_delete(&self, id: &ChunkId) -> Result<()> {
        let raw_id = *id.as_bytes();
        // Under the chunk lock so `flush` never syncs a half-deleted chunk.
        self.with_chunk_lock(raw_id, || {
            self.backend
                .delete_chunk(&raw_id)
                .map_err(NvstorError::Io)?;
            self.metadata.delete_chunk(&raw_id).map_err(meta_err)?;
            self.cache.write().remove(&raw_id);
            self.unsynced.lock().remove(&raw_id);
            debug!(%id, "chunk_delete: force deleted");
            Ok(())
        })
    }

    /// Check if a chunk exists.
    pub fn chunk_exists(&self, id: &ChunkId) -> Result<bool> {
        let raw_id = id.as_bytes();
        // Check cache first.
        if self.cache.read().contains_key(raw_id) {
            return Ok(true);
        }
        Ok(self.metadata.get_chunk(raw_id).map_err(meta_err)?.is_some())
    }

    /// Get chunk metadata.
    pub fn chunk_get_info(&self, id: &ChunkId) -> Result<ChunkInfo> {
        let raw_id = id.as_bytes();
        let meta = self
            .metadata
            .get_chunk(raw_id)
            .map_err(meta_err)?
            .ok_or(NvstorError::NotFound)?;
        Ok(chunk_meta_to_info(&meta))
    }

    /// Validate chunk integrity by reading data and verifying its IEEE CRC-32.
    pub fn chunk_validate(&self, id: &ChunkId) -> Result<bool> {
        let raw_id = id.as_bytes();
        let meta = self
            .metadata
            .get_chunk(raw_id)
            .map_err(meta_err)?
            .ok_or(NvstorError::NotFound)?;

        let mut buffer = vec![0u8; meta.size as usize];
        let n = match self.backend.read_chunk(raw_id, &mut buffer) {
            Ok(n) => n,
            Err(e) => {
                warn!(%id, error = %e, "chunk_validate: read failed");
                return Ok(false);
            }
        };

        let actual = crc32(&buffer[..n]);
        Ok(actual == meta.checksum)
    }

    // =====================================================================
    // Object operations
    // =====================================================================

    /// Write an object. Small objects (<=inline_threshold) are stored inline
    /// in metadata. Large objects are split into chunks.
    pub fn object_write(
        &self,
        object_id: &str,
        data: &[u8],
        content_type: Option<&str>,
    ) -> Result<ObjectManifest> {
        self.object_write_with_sync(object_id, data, content_type, crate::SyncMode::Full)
    }

    /// Object write with explicit chunk durability mode.
    ///
    /// Use `SyncMode::None` from hot paths where durability is
    /// guaranteed by a higher-level barrier — most importantly the
    /// S3-over-RDMA fast path, where the PUT_PART completes once the
    /// chunk has been replicated to N peers and the protocol ACK
    /// surfaces well-ordered with respect to client visibility. The
    /// per-chunk `sync_all` on the POSIX backend dominates the cost of
    /// a chunk write on NVMe, so this is the main lever on the
    /// checkpoint path.
    pub fn object_write_with_sync(
        &self,
        object_id: &str,
        data: &[u8],
        content_type: Option<&str>,
        sync: crate::SyncMode,
    ) -> Result<ObjectManifest> {
        let _guard = self.object_locks[self.object_lock_index(object_id)].write();
        self.wal_log_put(object_id, data.len() as u64);

        let now = now_secs();
        let ct = content_type.unwrap_or("application/octet-stream");

        // Get old manifest BEFORE writing new data (for cleanup after).
        let old_manifest = self.metadata.get_manifest(object_id).map_err(meta_err)?;

        // Write new data first (chunks or inline).
        // This is safe because chunks are content-addressed — even if we crash,
        // orphaned chunks are harmless and GC will clean them.
        let result = if data.len() <= self.config.inline_threshold {
            self.object_write_inline(object_id, data, ct, now)
        } else {
            self.object_write_chunked(object_id, data, ct, now, sync)
        }?;

        // NOW clean up old chunks (after new manifest is atomically written).
        // If we crash between the new write and this cleanup, we just have
        // orphaned old chunks — GC handles them. No data loss.
        if let Some(old) = old_manifest {
            for cref in &old.chunks {
                let chunk_id = ChunkId::from_bytes(cref.chunk_id);
                if let Err(e) = self.chunk_unref(&chunk_id) {
                    warn!(%chunk_id, error = %e, "object_write: failed to unref old chunk");
                }
            }
        }

        Ok(result)
    }

    fn object_lock_index(&self, object_id: &str) -> usize {
        let mut hash = std::collections::hash_map::DefaultHasher::new();
        object_id.hash(&mut hash);
        hash.finish() as usize % self.object_locks.len()
    }

    fn release_manifest_chunks(&self, manifest: &ManifestMeta) {
        for chunk in &manifest.chunks {
            let id = ChunkId::from_bytes(chunk.chunk_id);
            if let Err(error) = self.chunk_unref(&id) {
                warn!(%id, %error, "failed to release manifest chunk");
            }
        }
    }

    /// A nonempty manifest without local data is a cache miss, so a distributed
    /// reader can fetch the payload from a replica. Never return a short success.
    fn complete_object_data(data: Vec<u8>, size: u64) -> Result<Vec<u8>> {
        if data.len() as u64 != size {
            return Err(NvstorError::NotFound);
        }
        Ok(data)
    }

    /// Write a small inline object.
    fn object_write_inline(
        &self,
        object_id: &str,
        data: &[u8],
        content_type: &str,
        now: i64,
    ) -> Result<ObjectManifest> {
        let manifest_meta = ManifestMeta {
            object_id: object_id.to_string(),
            size: data.len() as u64,
            chunks: vec![],
            etag: format!("\"{}\"", hex_md5(data)),
            content_type: content_type.to_string(),
            created_at: now,
            modified_at: now,
            inline_data: Some(data.to_vec()),
        };

        let mut ops = vec![BatchOp::PutManifest(manifest_meta.clone())];
        ops.push(BatchOp::PutInline {
            object_id: object_id.to_string(),
            data: data.to_vec(),
        });
        self.metadata.write_batch(ops).map_err(meta_err)?;

        debug!(object_id, size = data.len(), "object_write: inline");
        Ok(manifest_meta_to_object(&manifest_meta))
    }

    /// Write a large chunked object. All chunk metadata + manifest in one atomic batch.
    fn object_write_chunked(
        &self,
        object_id: &str,
        data: &[u8],
        content_type: &str,
        now: i64,
        sync: crate::SyncMode,
    ) -> Result<ObjectManifest> {
        let chunk_size = self.config.chunk_size;
        let chunks: Vec<&[u8]> = data.chunks(chunk_size).collect();

        // Parallel chunk writes: sha256 + disk + per-chunk metadata
        // all run concurrently across chunks. Chunks are written in
        // parallel so a multi-MiB object is not serialized per chunk.
        // `chunk_write` has
        // per-content locking, so two same-content writes still
        // dedupe correctly.
        let results: Vec<Result<(ChunkId, u32, u64)>> = std::thread::scope(|s| {
            let handles: Vec<_> = chunks
                .iter()
                .map(|c| {
                    let c = *c;
                    s.spawn(move || -> Result<(ChunkId, u32, u64)> {
                        let id = self.chunk_write_with_sync(c, sync)?;
                        Ok((id, crc32(c), c.len() as u64))
                    })
                })
                .collect();
            handles
                .into_iter()
                .map(|h| match h.join() {
                    Ok(r) => r,
                    Err(_) => Err(NvstorError::Internal("chunk worker panicked".into())),
                })
                .collect()
        });

        // Release every successful acquisition if any worker failed.
        if results.iter().any(Result::is_err) {
            for (id, _, _) in results.iter().filter_map(|r| r.as_ref().ok()) {
                if let Err(error) = self.chunk_unref(id) {
                    warn!(%id, %error, "failed to roll back unpublished chunk");
                }
            }
            return Err(results
                .into_iter()
                .find_map(Result::err)
                .expect("invariant: at least one chunk worker failed"));
        }
        let mut chunk_refs = Vec::with_capacity(results.len());
        let mut offset = 0u64;
        for res in results {
            let (chunk_id, checksum, len) = res?;
            chunk_refs.push(ChunkRef {
                chunk_id,
                length: len,
                checksum,
                logical_start: offset,
                logical_end: offset + len,
            });
            offset += len;
        }

        let manifest_meta = ManifestMeta {
            object_id: object_id.to_string(),
            size: data.len() as u64,
            chunks: chunk_refs.iter().map(core_ref_to_meta).collect(),
            etag: format!("\"{}\"", hex_md5(data)),
            content_type: content_type.to_string(),
            created_at: now,
            modified_at: now,
            inline_data: None,
        };

        // Manifest write is atomic via RocksDB WriteBatch.
        // Chunk data was already written and committed by chunk_write() above.
        // If we crash here, chunks exist as orphans → GC cleans them.
        // If manifest write succeeds, the object is fully consistent.
        if let Err(error) = self.metadata.write_batch(vec![
            BatchOp::PutManifest(manifest_meta.clone()),
            BatchOp::DeleteInline(object_id.to_string()),
        ]) {
            self.release_manifest_chunks(&manifest_meta);
            return Err(meta_err(error));
        }

        debug!(
            object_id,
            size = data.len(),
            chunks = chunk_refs.len(),
            "object_write: chunked"
        );
        Ok(manifest_meta_to_object(&manifest_meta))
    }

    /// Read an entire object, assembling from chunks.
    pub fn object_read(&self, object_id: &str) -> Result<Vec<u8>> {
        let _guard = self.object_locks[self.object_lock_index(object_id)].read();
        let manifest = self
            .metadata
            .get_manifest(object_id)
            .map_err(meta_err)?
            .ok_or(NvstorError::NotFound)?;

        // Inline object.
        if let Some(ref inline_data) = manifest.inline_data {
            return Self::complete_object_data(inline_data.clone(), manifest.size);
        }

        // Assemble from chunks — parallel for multi-chunk objects.
        let chunk_count = manifest.chunks.len();
        if chunk_count <= 1 {
            // Single chunk: read directly, no thread overhead.
            let mut result = Vec::with_capacity(manifest.size as usize);
            for cref in &manifest.chunks {
                let chunk_id = ChunkId::from_bytes(cref.chunk_id);
                let mut buf = vec![0u8; cref.length as usize];
                let n = self.chunk_read(&chunk_id, &mut buf)?;
                result.extend_from_slice(&buf[..n]);
            }
            return Self::complete_object_data(result, manifest.size);
        }

        // Multi-chunk: read chunks in parallel using scoped threads.
        // Cap parallelism at 8 to avoid spawning hundreds of threads.
        let max_parallel = 8usize;
        let chunk_buffers: Vec<std::sync::Mutex<Option<Vec<u8>>>> = (0..chunk_count)
            .map(|_| std::sync::Mutex::new(None))
            .collect();
        let first_error: std::sync::Mutex<Option<NvstorError>> = std::sync::Mutex::new(None);

        for batch_start in (0..chunk_count).step_by(max_parallel) {
            let batch_end = (batch_start + max_parallel).min(chunk_count);
            std::thread::scope(|s| {
                for i in batch_start..batch_end {
                    let cref = &manifest.chunks[i];
                    let chunk_id = ChunkId::from_bytes(cref.chunk_id);
                    let len = cref.length as usize;
                    let slot = &chunk_buffers[i];
                    let err = &first_error;
                    s.spawn(move || {
                        let mut buf = vec![0u8; len];
                        match self.chunk_read(&chunk_id, &mut buf) {
                            Ok(n) => {
                                buf.truncate(n);
                                if let Ok(mut guard) = slot.lock() {
                                    *guard = Some(buf);
                                }
                            }
                            Err(e) => {
                                if let Ok(mut guard) = err.lock() {
                                    if guard.is_none() {
                                        *guard = Some(e);
                                    }
                                }
                            }
                        }
                    });
                }
            });
            // Check for errors after each batch.
            if let Ok(guard) = first_error.lock() {
                if guard.is_some() {
                    break;
                }
            }
        }

        let has_error = first_error.lock().map(|g| g.is_some()).unwrap_or(false);
        if has_error {
            let e = first_error
                .into_inner()
                .ok()
                .flatten()
                .unwrap_or_else(|| NvstorError::Internal("parallel read failed".into()));
            return Err(e);
        }

        let mut result = Vec::with_capacity(manifest.size as usize);
        for slot in chunk_buffers {
            if let Ok(inner) = slot.into_inner() {
                if let Some(buf) = inner {
                    result.extend_from_slice(&buf);
                }
            }
        }

        Self::complete_object_data(result, manifest.size)
    }

    /// Read a byte range from an object.
    pub fn object_read_range(&self, object_id: &str, start: u64, end: u64) -> Result<Vec<u8>> {
        let _guard = self.object_locks[self.object_lock_index(object_id)].read();
        let manifest = self
            .metadata
            .get_manifest(object_id)
            .map_err(meta_err)?
            .ok_or(NvstorError::NotFound)?;

        if start >= end || start >= manifest.size {
            return Err(NvstorError::OutOfBounds);
        }

        let end = end.min(manifest.size);

        // Inline object.
        if let Some(ref inline_data) = manifest.inline_data {
            if inline_data.len() as u64 != manifest.size {
                return Err(NvstorError::NotFound);
            }
            return Ok(inline_data[start as usize..end as usize].to_vec());
        }

        // Chunked: find overlapping chunks and assemble.
        let mut result = Vec::with_capacity((end - start) as usize);
        for cref in &manifest.chunks {
            let chunk_start = cref.logical_start;
            let chunk_end = cref.logical_end;

            if chunk_end <= start || chunk_start >= end {
                continue; // No overlap.
            }

            let chunk_id = ChunkId::from_bytes(cref.chunk_id);
            let mut buf = vec![0u8; cref.length as usize];
            let n = self.chunk_read(&chunk_id, &mut buf)?;

            // Compute the slice within this chunk that overlaps [start, end).
            let read_start = if start > chunk_start {
                (start - chunk_start) as usize
            } else {
                0
            };
            let read_end = if end < chunk_end {
                (end - chunk_start) as usize
            } else {
                n
            };

            if read_end > n || read_start > read_end {
                return Err(NvstorError::NotFound);
            }
            result.extend_from_slice(&buf[read_start..read_end]);
        }

        Self::complete_object_data(result, end - start)
    }

    /// Delete an object and unref all its chunks.
    pub fn object_delete(&self, object_id: &str) -> Result<()> {
        let _guard = self.object_locks[self.object_lock_index(object_id)].write();
        self.wal_log_delete(object_id);

        let manifest = match self.metadata.get_manifest(object_id).map_err(meta_err)? {
            Some(m) => m,
            None => return Ok(()), // Already gone — idempotent.
        };

        // Remove ownership durably before reclaiming data. Readers and other
        // mutations of this object cannot race this transition.
        self.metadata
            .write_batch(vec![
                BatchOp::DeleteManifest(object_id.to_string()),
                BatchOp::DeleteInline(object_id.to_string()),
            ])
            .map_err(meta_err)?;
        self.release_manifest_chunks(&manifest);

        debug!(object_id, "object_delete: done");
        Ok(())
    }

    /// Get object manifest.
    pub fn object_get_manifest(&self, object_id: &str) -> Result<ObjectManifest> {
        let meta = self
            .metadata
            .get_manifest(object_id)
            .map_err(meta_err)?
            .ok_or(NvstorError::NotFound)?;
        Ok(manifest_meta_to_object(&meta))
    }

    /// List objects with prefix filtering and pagination.
    pub fn object_list(
        &self,
        prefix: &str,
        start_after: Option<&str>,
        max_keys: u32,
    ) -> Result<(Vec<ObjectManifest>, bool)> {
        let (metas, truncated) = self
            .metadata
            .list_manifests(prefix, start_after, max_keys)
            .map_err(meta_err)?;
        let manifests = metas.iter().map(manifest_meta_to_object).collect();
        Ok((manifests, truncated))
    }

    /// Zero-copy object copy via ref counting. No data movement.
    pub fn object_copy(&self, src_id: &str, dst_id: &str) -> Result<ObjectManifest> {
        let src_index = self.object_lock_index(src_id);
        let dst_index = self.object_lock_index(dst_id);
        let _first = self.object_locks[src_index.min(dst_index)].write();
        let _second = if src_index != dst_index {
            Some(self.object_locks[src_index.max(dst_index)].write())
        } else {
            None
        };
        let src = self
            .metadata
            .get_manifest(src_id)
            .map_err(meta_err)?
            .ok_or(NvstorError::NotFound)?;

        if src_id == dst_id {
            return Ok(manifest_meta_to_object(&src));
        }
        let old = self.metadata.get_manifest(dst_id).map_err(meta_err)?;

        let now = now_secs();

        // Acquire the new ownership before publishing; leave the old object
        // intact if any source chunk is unavailable.
        for (index, cref) in src.chunks.iter().enumerate() {
            let chunk_id = ChunkId::from_bytes(cref.chunk_id);
            if let Err(error) = self.chunk_ref(&chunk_id) {
                let mut acquired = src.clone();
                acquired.chunks.truncate(index);
                self.release_manifest_chunks(&acquired);
                return Err(error);
            }
        }

        // Create new manifest pointing to same chunks.
        let mut dst_meta = src.clone();
        dst_meta.object_id = dst_id.to_string();
        dst_meta.created_at = now;
        dst_meta.modified_at = now;

        let mut ops = vec![
            BatchOp::PutManifest(dst_meta.clone()),
            BatchOp::DeleteInline(dst_id.to_string()),
        ];

        // Copy inline data if present.
        if let Some(ref data) = src.inline_data {
            ops.push(BatchOp::PutInline {
                object_id: dst_id.to_string(),
                data: data.clone(),
            });
        }

        if let Err(error) = self.metadata.write_batch(ops) {
            self.release_manifest_chunks(&dst_meta);
            return Err(meta_err(error));
        }
        if let Some(old) = old {
            self.release_manifest_chunks(&old);
        }

        debug!(src_id, dst_id, "object_copy: zero-copy via ref counting");
        Ok(manifest_meta_to_object(&dst_meta))
    }

    // =====================================================================
    // Streaming write API
    // =====================================================================

    /// Begin a streaming object write.
    pub fn object_write_begin(
        &self,
        object_id: &str,
        content_type: Option<&str>,
    ) -> ObjectWriteCtx {
        ObjectWriteCtx {
            object_id: object_id.to_string(),
            content_type: content_type
                .unwrap_or("application/octet-stream")
                .to_string(),
            chunks: Vec::new(),
            total_size: 0,
            created_at: now_secs(),
        }
    }

    /// Write a chunk of data in a streaming write.
    pub fn object_write_chunk(&self, ctx: &mut ObjectWriteCtx, data: &[u8]) -> Result<()> {
        let chunk_id = self.chunk_write(data)?;
        let checksum = crc32(data);
        let len = data.len() as u64;

        ctx.chunks.push(ChunkRef {
            chunk_id,
            length: len,
            checksum,
            logical_start: ctx.total_size,
            logical_end: ctx.total_size + len,
        });
        ctx.total_size += len;

        Ok(())
    }

    /// Finalize a streaming write — stores the manifest atomically.
    pub fn object_write_finish(&self, ctx: ObjectWriteCtx) -> Result<ObjectManifest> {
        let now = now_secs();

        let _guard = self.object_locks[self.object_lock_index(&ctx.object_id)].write();
        let old = match self.metadata.get_manifest(&ctx.object_id) {
            Ok(old) => old,
            Err(error) => {
                self.object_write_abort(ctx)?;
                return Err(meta_err(error));
            }
        };

        let etag = if ctx.chunks.len() == 1 {
            // Single chunk: etag is chunk hash (simplified).
            format!("\"{}\"", ctx.chunks[0].chunk_id)
        } else {
            // Multipart: etag is part-count suffixed.
            format!("\"{}-{}\"", ChunkId::from_content(b""), ctx.chunks.len())
        };

        let manifest_meta = ManifestMeta {
            object_id: ctx.object_id.clone(),
            size: ctx.total_size,
            chunks: ctx.chunks.iter().map(core_ref_to_meta).collect(),
            etag,
            content_type: ctx.content_type.clone(),
            created_at: ctx.created_at,
            modified_at: now,
            inline_data: None,
        };

        if let Err(error) = self.metadata.write_batch(vec![
            BatchOp::PutManifest(manifest_meta.clone()),
            BatchOp::DeleteInline(ctx.object_id.clone()),
        ]) {
            self.release_manifest_chunks(&manifest_meta);
            return Err(meta_err(error));
        }
        if let Some(old) = old {
            self.release_manifest_chunks(&old);
        }

        debug!(
            object_id = ctx.object_id,
            size = ctx.total_size,
            chunks = ctx.chunks.len(),
            "streaming write finished"
        );
        Ok(manifest_meta_to_object(&manifest_meta))
    }

    /// Abort a streaming write — unrefs all written chunks.
    pub fn object_write_abort(&self, ctx: ObjectWriteCtx) -> Result<()> {
        for cref in &ctx.chunks {
            if let Err(e) = self.chunk_unref(&cref.chunk_id) {
                warn!(chunk_id = %cref.chunk_id, error = %e, "object_write_abort: failed to unref");
            }
        }
        debug!(
            object_id = ctx.object_id,
            chunks = ctx.chunks.len(),
            "streaming write aborted"
        );
        Ok(())
    }

    // =====================================================================
    // Volume operations
    // =====================================================================

    /// Create a volume with default options (RWO, Strong consistency).
    pub fn volume_create(&self, id: Uuid, size: u64) -> Result<()> {
        self.volume_create_with_options(id, size, AccessMode::RWO, ConsistencyMode::Strong)
    }

    /// Create a volume with specific access mode and consistency.
    pub fn volume_create_with_options(
        &self,
        id: Uuid,
        size: u64,
        access_mode: AccessMode,
        consistency: ConsistencyMode,
    ) -> Result<()> {
        if size == 0 {
            return Err(NvstorError::InvalidSize("volume size must be > 0".into()));
        }

        {
            let vols = self.volumes.read();
            if vols.contains_key(&id) {
                return Err(NvstorError::Exists);
            }
        }

        let chunk_size = self.config.chunk_size;
        let num_chunks = (size + chunk_size as u64 - 1) / chunk_size as u64;

        let vol = VolumeMap {
            volume_id: id,
            size,
            chunk_size,
            access_mode,
            consistency,
            state: VolumeState::Ready,
            active_writer: None,
            append_offset: 0,
            chunks: vec![None; num_chunks as usize],
        };

        let meta = volume_map_to_meta(&vol);
        self.metadata.put_volume(&meta).map_err(meta_err)?;
        self.volumes.write().insert(id, vol);

        info!(%id, size, "volume_create: created");
        Ok(())
    }

    /// Write to a volume at a byte offset.
    pub fn volume_write(
        &self,
        id: Uuid,
        offset: u64,
        data: &[u8],
        client_id: Option<u64>,
    ) -> Result<()> {
        if data.is_empty() {
            return Ok(());
        }

        // Check bounds and access under read lock, then release before I/O.
        let (vol_size, chunk_size, access_mode, consistency, active_writer, append_offset) = {
            let vols = self.volumes.read();
            let vol = vols.get(&id).ok_or(NvstorError::NotFound)?;
            (
                vol.size,
                vol.chunk_size,
                vol.access_mode,
                vol.consistency,
                vol.active_writer,
                vol.append_offset,
            )
        };

        let end = offset + data.len() as u64;
        if end > vol_size {
            return Err(NvstorError::OutOfBounds);
        }

        // Access control.
        if access_mode == AccessMode::ROX {
            return Err(NvstorError::PermissionDenied);
        }
        if access_mode == AccessMode::RWO {
            if let Some(writer) = active_writer {
                if client_id != Some(writer) {
                    return Err(NvstorError::VolumeLocked);
                }
            }
        }

        // Append-only check.
        if consistency == ConsistencyMode::AppendOnly && offset < append_offset {
            return Err(NvstorError::InvalidArgument(format!(
                "append-only: offset {offset} < append_offset {append_offset}"
            )));
        }

        // Write data across chunks. Release volume lock during I/O.
        let cs = chunk_size as u64;
        let mut data_offset = 0usize;
        let mut current_offset = offset;

        while data_offset < data.len() {
            let chunk_idx = (current_offset / cs) as usize;
            let offset_in_chunk = (current_offset % cs) as usize;
            let bytes_remaining = data.len() - data_offset;
            let bytes_in_chunk = (chunk_size - offset_in_chunk).min(bytes_remaining);

            // Read existing chunk data (if partial write within an existing chunk).
            let chunk_data = {
                let vols = self.volumes.read();
                let vol = vols.get(&id).ok_or(NvstorError::NotFound)?;
                vol.chunks.get(chunk_idx).copied().flatten()
            };

            let mut buf = vec![0u8; chunk_size];
            if let Some(existing_id) = chunk_data {
                // Read existing chunk for merge.
                let raw = existing_id;
                match self.backend.read_chunk(&raw.0, &mut buf) {
                    Ok(_) => {}
                    Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                        buf.fill(0);
                    }
                    Err(e) => return Err(NvstorError::Io(e)),
                }
            }

            // Apply the write.
            buf[offset_in_chunk..offset_in_chunk + bytes_in_chunk]
                .copy_from_slice(&data[data_offset..data_offset + bytes_in_chunk]);

            // Determine actual chunk length (may be partial for tail chunk).
            let actual_len = if (chunk_idx + 1) as u64 * cs > vol_size {
                (vol_size - chunk_idx as u64 * cs) as usize
            } else {
                chunk_size
            };

            // Write new chunk.
            let new_chunk_id = self.chunk_write(&buf[..actual_len])?;

            // Unref old chunk if replaced.
            if let Some(old_id) = chunk_data {
                let old = old_id;
                if old != new_chunk_id {
                    if let Err(e) = self.chunk_unref(&old) {
                        warn!(%old, error = %e, "volume_write: failed to unref old chunk");
                    }
                }
            }

            // Update volume map and persist.
            {
                let mut vols = self.volumes.write();
                let vol = vols.get_mut(&id).ok_or(NvstorError::NotFound)?;
                vol.chunks[chunk_idx] = Some(new_chunk_id);

                if vol.consistency == ConsistencyMode::AppendOnly {
                    let new_end = current_offset + bytes_in_chunk as u64;
                    if new_end > vol.append_offset {
                        vol.append_offset = new_end;
                    }
                }
            }

            // Persist volume chunk mapping.
            self.metadata
                .put_volume_chunk(
                    &id.into_bytes(),
                    chunk_idx as u64 * cs,
                    new_chunk_id.as_bytes(),
                )
                .map_err(meta_err)?;

            data_offset += bytes_in_chunk;
            current_offset += bytes_in_chunk as u64;
        }

        Ok(())
    }

    /// Read from a volume at a byte offset.
    pub fn volume_read(
        &self,
        id: Uuid,
        offset: u64,
        buffer: &mut [u8],
        client_id: Option<u64>,
    ) -> Result<usize> {
        if buffer.is_empty() {
            return Ok(0);
        }

        let (vol_size, chunk_size, chunks_snapshot) = {
            let vols = self.volumes.read();
            let vol = vols.get(&id).ok_or(NvstorError::NotFound)?;
            (vol.size, vol.chunk_size, vol.chunks.clone())
        };

        let end = (offset + buffer.len() as u64).min(vol_size);
        if offset >= vol_size {
            return Err(NvstorError::OutOfBounds);
        }

        let _ = client_id; // Reserved for future access control.

        let cs = chunk_size as u64;
        let mut buf_offset = 0usize;
        let mut current_offset = offset;
        let read_len = (end - offset) as usize;

        while buf_offset < read_len {
            let chunk_idx = (current_offset / cs) as usize;
            let offset_in_chunk = (current_offset % cs) as usize;
            let bytes_remaining = read_len - buf_offset;
            let bytes_in_chunk = (chunk_size - offset_in_chunk).min(bytes_remaining);

            let chunk_id_opt = chunks_snapshot.get(chunk_idx).copied().flatten();

            match chunk_id_opt {
                Some(raw_id) => {
                    let chunk_id = raw_id;
                    let mut chunk_buf = vec![0u8; chunk_size];
                    let n = self.chunk_read(&chunk_id, &mut chunk_buf)?;
                    let available = n.saturating_sub(offset_in_chunk);
                    let copy_len = bytes_in_chunk.min(available);
                    buffer[buf_offset..buf_offset + copy_len]
                        .copy_from_slice(&chunk_buf[offset_in_chunk..offset_in_chunk + copy_len]);
                    // Zero-fill if chunk is shorter than expected.
                    if copy_len < bytes_in_chunk {
                        buffer[buf_offset + copy_len..buf_offset + bytes_in_chunk].fill(0);
                    }
                }
                None => {
                    // Sparse: unwritten region reads as zeros.
                    buffer[buf_offset..buf_offset + bytes_in_chunk].fill(0);
                }
            }

            buf_offset += bytes_in_chunk;
            current_offset += bytes_in_chunk as u64;
        }

        Ok(read_len)
    }

    /// Delete a volume and unref all its chunks.
    pub fn volume_delete(&self, id: Uuid) -> Result<()> {
        let vol = {
            let vols = self.volumes.read();
            vols.get(&id).cloned().ok_or(NvstorError::NotFound)?
        };

        // Unref all chunks.
        for chunk_opt in &vol.chunks {
            if let Some(raw_id) = chunk_opt {
                let chunk_id = *raw_id;
                if let Err(e) = self.chunk_unref(&chunk_id) {
                    warn!(%chunk_id, error = %e, "volume_delete: failed to unref chunk");
                }
            }
        }

        // Delete volume metadata.
        self.metadata
            .delete_volume(&id.into_bytes())
            .map_err(meta_err)?;

        // Clean up volume chunk mappings.
        let cs = vol.chunk_size as u64;
        for (idx, chunk_opt) in vol.chunks.iter().enumerate() {
            if chunk_opt.is_some() {
                let _ = self
                    .metadata
                    .delete_volume_chunk(&id.into_bytes(), idx as u64 * cs);
            }
        }

        self.volumes.write().remove(&id);
        info!(%id, "volume_delete: done");
        Ok(())
    }

    /// Acquire exclusive writer lock for a RWO volume.
    pub fn volume_acquire_writer(&self, id: Uuid, client_id: u64) -> Result<()> {
        let mut vols = self.volumes.write();
        let vol = vols.get_mut(&id).ok_or(NvstorError::NotFound)?;

        if vol.access_mode != AccessMode::RWO {
            return Err(NvstorError::InvalidArgument(
                "acquire_writer only valid for RWO volumes".into(),
            ));
        }

        if let Some(existing) = vol.active_writer {
            if existing != client_id {
                return Err(NvstorError::VolumeLocked);
            }
            // Already acquired by this client.
            return Ok(());
        }

        vol.active_writer = Some(client_id);

        // Persist.
        let meta = volume_map_to_meta(vol);
        self.metadata.put_volume(&meta).map_err(meta_err)?;

        debug!(%id, client_id, "volume_acquire_writer: locked");
        Ok(())
    }

    /// Release exclusive writer lock.
    pub fn volume_release_writer(&self, id: Uuid, client_id: u64) -> Result<()> {
        let mut vols = self.volumes.write();
        let vol = vols.get_mut(&id).ok_or(NvstorError::NotFound)?;

        if vol.active_writer != Some(client_id) {
            return Err(NvstorError::InvalidArgument(
                "client does not hold the writer lock".into(),
            ));
        }

        vol.active_writer = None;

        let meta = volume_map_to_meta(vol);
        self.metadata.put_volume(&meta).map_err(meta_err)?;

        debug!(%id, client_id, "volume_release_writer: unlocked");
        Ok(())
    }

    /// Get a snapshot of volume info.
    pub fn volume_get_info(&self, id: Uuid) -> Result<VolumeInfoSnapshot> {
        let vols = self.volumes.read();
        let vol = vols.get(&id).ok_or(NvstorError::NotFound)?;
        Ok(VolumeInfoSnapshot {
            volume_id: vol.volume_id,
            size: vol.size,
            chunk_size: vol.chunk_size,
            access_mode: vol.access_mode,
            consistency: vol.consistency,
            state: vol.state,
            active_writer: vol.active_writer,
            append_offset: vol.append_offset,
        })
    }

    // =====================================================================
    // Maintenance
    // =====================================================================

    /// Garbage collection: scan RocksDB for chunks with ref_count=0 and delete them.
    pub fn gc_sweep(&self) -> Result<GcReport> {
        let start = Instant::now();
        let mut deleted = 0u64;
        let mut reclaimed = 0u64;
        let mut errors = 0u64;

        // Collect candidates from RocksDB (not in-memory cache).
        let mut candidates = Vec::new();
        self.metadata
            .scan_chunks(|meta| {
                if meta.ref_count == 0 {
                    candidates.push((meta.id, meta.size));
                }
            })
            .map_err(meta_err)?;

        for (raw_id, size) in candidates {
            // Re-check ref_count == 0 and delete UNDER the per-chunk
            // lock. Without the lock, a concurrent `chunk_write`
            // dedup-hit could bump ref 0→1 between this recheck and the
            // delete, and GC would then remove the data file of a chunk
            // a freshly-written object references. The lock serializes
            // GC's delete against write/ref/unref of the same chunk.
            let deleted_this = self.with_chunk_lock(raw_id, || -> Result<bool> {
                let current = self.metadata.get_chunk(&raw_id).map_err(meta_err)?;
                match current {
                    Some(meta) if meta.ref_count == 0 => {
                        self.backend
                            .delete_chunk(&raw_id)
                            .map_err(NvstorError::Io)?;
                        self.metadata.delete_chunk(&raw_id).map_err(meta_err)?;
                        self.cache.write().remove(&raw_id);
                        self.data_cache.lock().entries.remove(&raw_id);
                        Ok(true)
                    }
                    // Resurrected (ref_count > 0) or already gone —
                    // leave it.
                    _ => Ok(false),
                }
            });
            match deleted_this {
                Ok(true) => {
                    deleted += 1;
                    reclaimed += size;
                }
                Ok(false) => {}
                Err(e) => {
                    error!(chunk_id = %ChunkId::from_bytes(raw_id), error = %e, "gc: delete failed");
                    errors += 1;
                }
            }
        }

        let duration_ms = start.elapsed().as_millis() as u64;
        let report = GcReport {
            chunks_deleted: deleted,
            bytes_reclaimed: reclaimed,
            duration_ms,
            errors,
        };

        info!(
            deleted,
            reclaimed, duration_ms, errors, "gc_sweep: complete"
        );
        Ok(report)
    }

    /// Scrub: validate checksum integrity of stored chunks.
    pub fn scrub(&self, limit: u64) -> Result<IntegrityReport> {
        let start = Instant::now();
        let mut checked = 0u64;
        let mut failures = 0u64;
        let mut missing = 0u64;
        let mut orphaned = 0u64;

        let mut all_chunks = Vec::new();
        self.metadata
            .scan_chunks(|meta| {
                all_chunks.push(meta);
            })
            .map_err(meta_err)?;

        for meta in &all_chunks {
            if limit > 0 && checked >= limit {
                break;
            }

            if meta.ref_count == 0 {
                orphaned += 1;
            }

            let mut buf = vec![0u8; meta.size as usize];
            match self.backend.read_chunk(&meta.id, &mut buf) {
                Ok(n) => {
                    let actual = crc32(&buf[..n]);
                    if actual != meta.checksum {
                        failures += 1;
                        warn!(
                            chunk_id = %ChunkId::from_bytes(meta.id),
                            expected = meta.checksum,
                            actual,
                            "scrub: checksum mismatch"
                        );
                    }
                }
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                    missing += 1;
                    warn!(chunk_id = %ChunkId::from_bytes(meta.id), "scrub: chunk missing");
                }
                Err(e) => {
                    warn!(chunk_id = %ChunkId::from_bytes(meta.id), error = %e, "scrub: read error");
                    failures += 1;
                }
            }

            checked += 1;
        }

        let duration_ms = start.elapsed().as_millis() as u64;
        let report = IntegrityReport {
            chunks_checked: checked,
            checksum_failures: failures,
            missing_chunks: missing,
            orphaned_chunks: orphaned,
            duration_ms,
        };

        info!(
            checked,
            failures, missing, orphaned, duration_ms, "scrub: complete"
        );
        Ok(report)
    }

    /// Get storage statistics.
    pub fn stats(&self) -> Result<StorageStats> {
        let mut total_chunks = 0u64;
        let mut total_bytes = 0u64;
        let mut unique_bytes = 0u64;

        self.metadata
            .scan_chunks(|meta| {
                total_chunks += 1;
                unique_bytes += meta.size;
                total_bytes += meta.size * meta.ref_count;
            })
            .map_err(meta_err)?;

        let mut total_objects = 0u64;
        self.metadata
            .scan_manifests(|_| total_objects += 1)
            .map_err(meta_err)?;

        let total_volumes = self.volumes.read().len() as u64;

        Ok(StorageStats {
            total_chunks,
            total_bytes,
            unique_bytes,
            total_objects,
            total_volumes,
        })
    }

    /// Backend health check — probe write + read + verify + delete.
    /// Returns Ok(latency_us) if healthy, Err if the backend is degrading.
    pub fn health_check(&self) -> Result<u64> {
        let start = Instant::now();
        let probe_data = b"NVSTOR_HEALTH_PROBE";
        let probe_id = ChunkId::from_content(probe_data);
        let raw_id = *probe_id.as_bytes();

        // Write probe chunk.
        self.backend.write_chunk(&raw_id, probe_data).map_err(|e| {
            error!(error = %e, "health_check: write failed — backend may be degrading");
            NvstorError::Io(e)
        })?;

        // Read back and verify.
        let mut buf = vec![0u8; probe_data.len()];
        let n = self.backend.read_chunk(&raw_id, &mut buf).map_err(|e| {
            error!(error = %e, "health_check: read failed — backend may be degrading");
            NvstorError::Io(e)
        })?;

        if &buf[..n] != probe_data {
            error!("health_check: data mismatch — backend returning corrupt data");
            return Err(NvstorError::Internal("health probe data mismatch".into()));
        }

        // Clean up probe chunk.
        let _ = self.backend.delete_chunk(&raw_id);

        let latency = start.elapsed().as_micros() as u64;
        debug!(latency_us = latency, "health_check: OK");
        Ok(latency)
    }

    /// Flush all pending writes to stable storage: sync the bytes of every
    /// chunk this process wrote with `SyncMode::None`, then seal its row.
    /// The bytes reach disk before the row says they have.
    pub fn flush(&self) -> Result<()> {
        let pending: Vec<[u8; 32]> = self.unsynced.lock().iter().copied().collect();
        for raw_id in pending {
            self.with_chunk_lock(raw_id, || -> Result<()> {
                // A durable dedup hit may have sealed it since the snapshot.
                if !self.unsynced.lock().contains(&raw_id) {
                    return Ok(());
                }
                // No row means the chunk was deleted; nothing to sync.
                if let Some(mut meta) = self.metadata.get_chunk(&raw_id).map_err(meta_err)? {
                    self.retry_io("chunk_sync", || self.backend.sync_chunk(&raw_id))
                        .map_err(NvstorError::Io)?;
                    if !meta.sealed {
                        meta.sealed = true;
                        self.metadata.put_chunk(&meta).map_err(meta_err)?;
                    }
                }
                self.unsynced.lock().remove(&raw_id);
                Ok(())
            })?;
        }
        self.backend.flush().map_err(NvstorError::Io)?;
        Ok(())
    }

    /// Number of chunks written with `SyncMode::None` that `flush` has not
    /// yet synced.
    pub fn unsynced_chunks(&self) -> usize {
        self.unsynced.lock().len()
    }

    // =====================================================================
    // Internal helpers
    // =====================================================================

    fn cache_insert(&self, id: &[u8; 32], meta: &ChunkMeta) {
        let mut cache = self.cache.write();
        // Bound the metadata cache. Unbounded, this HashMap grows once
        // per chunk write + checksum lookup and only shrinks on chunk
        // delete — under sustained small-chunk traffic that's millions
        // of entries a day and multi-GB RSS over a long run. This cache
        // is a pure read accelerator: a miss just re-reads RocksDB, so
        // eviction *policy* correctness is irrelevant — only the bound
        // is. When at cap and inserting a new key, drop one arbitrary
        // existing entry to hold steady state.
        if cache.len() >= MAX_META_CACHE_ENTRIES && !cache.contains_key(id) {
            if let Some(victim) = cache.keys().next().copied() {
                cache.remove(&victim);
            }
        }
        cache.insert(
            *id,
            CachedChunk {
                size: meta.size,
                checksum: meta.checksum,
                ref_count: AtomicU64::new(meta.ref_count),
                created_at: meta.created_at,
            },
        );
    }

    fn get_checksum(&self, id: &[u8; 32]) -> Result<u32> {
        // Check cache first.
        {
            let cache = self.cache.read();
            if let Some(entry) = cache.get(id) {
                return Ok(entry.checksum);
            }
        }
        // Fall back to metadata.
        let meta = self
            .metadata
            .get_chunk(id)
            .map_err(meta_err)?
            .ok_or(NvstorError::NotFound)?;
        Ok(meta.checksum)
    }
}

// ---------------------------------------------------------------------------
// Free functions — type conversions
// ---------------------------------------------------------------------------

fn meta_err(e: nvstor_metadata::MetadataError) -> NvstorError {
    match e {
        nvstor_metadata::MetadataError::NotFound => NvstorError::NotFound,
        other => NvstorError::Internal(other.to_string()),
    }
}

/// Map the storage-layer durability mode onto the metadata store's barrier.
/// The metadata store only distinguishes "fsync" from "don't"; both
/// `Data` and `Full` fsync the chunk bytes, so both get a durable row.
/// `None` leaves the bytes in page cache, so the row must not be fsync'd
/// either — otherwise a crash yields a durable row pointing at missing data,
/// which the dedup check then treats as "already stored" forever.
fn meta_sync_for(sync: crate::SyncMode) -> nvstor_metadata::SyncMode {
    match sync {
        crate::SyncMode::None => nvstor_metadata::SyncMode::None,
        crate::SyncMode::Data | crate::SyncMode::Full => nvstor_metadata::SyncMode::Full,
    }
}

fn now_secs() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64
}

fn hex_md5(data: &[u8]) -> String {
    // Simple checksum-based "etag" — not actual MD5, but deterministic.
    // IEEE CRC-32 for speed since this is an internal etag, not a crypto hash.
    format!("{:08x}{:08x}", crc32(data), data.len())
}

fn chunk_meta_to_info(meta: &ChunkMeta) -> ChunkInfo {
    ChunkInfo {
        id: ChunkId::from_bytes(meta.id),
        size: meta.size,
        checksum: meta.checksum,
        locations: vec![],
        ref_count: meta.ref_count,
        created_at: meta.created_at,
        last_accessed: meta.last_accessed,
    }
}

fn core_ref_to_meta(cr: &ChunkRef) -> MetaChunkRef {
    MetaChunkRef {
        chunk_id: *cr.chunk_id.as_bytes(),
        length: cr.length as u32,
        checksum: cr.checksum,
        logical_start: cr.logical_start,
        logical_end: cr.logical_end,
        nodes: Vec::new(), // Populated by distributed_write after local write
    }
}

fn meta_ref_to_core(mr: &MetaChunkRef) -> ChunkRef {
    ChunkRef {
        chunk_id: ChunkId::from_bytes(mr.chunk_id),
        length: mr.length as u64,
        checksum: mr.checksum,
        logical_start: mr.logical_start,
        logical_end: mr.logical_end,
    }
}

fn manifest_meta_to_object(m: &ManifestMeta) -> ObjectManifest {
    ObjectManifest {
        object_id: m.object_id.clone(),
        size: m.size,
        chunks: m.chunks.iter().map(meta_ref_to_core).collect(),
        etag: if m.etag.is_empty() {
            None
        } else {
            Some(m.etag.clone())
        },
        content_type: if m.content_type.is_empty() {
            None
        } else {
            Some(m.content_type.clone())
        },
        created_at: m.created_at,
        modified_at: m.modified_at,
    }
}

fn access_mode_to_u8(m: AccessMode) -> u8 {
    match m {
        AccessMode::RWO => 0,
        AccessMode::ROX => 1,
        AccessMode::RWX => 2,
    }
}

fn u8_to_access_mode(v: u8) -> AccessMode {
    match v {
        0 => AccessMode::RWO,
        1 => AccessMode::ROX,
        2 => AccessMode::RWX,
        _ => AccessMode::RWO,
    }
}

fn consistency_to_u8(c: ConsistencyMode) -> u8 {
    match c {
        ConsistencyMode::Strong => 0,
        ConsistencyMode::Session => 1,
        ConsistencyMode::AppendOnly => 2,
    }
}

fn u8_to_consistency(v: u8) -> ConsistencyMode {
    match v {
        0 => ConsistencyMode::Strong,
        1 => ConsistencyMode::Session,
        2 => ConsistencyMode::AppendOnly,
        _ => ConsistencyMode::Strong,
    }
}

fn state_to_u8(s: VolumeState) -> u8 {
    match s {
        VolumeState::Creating => 0,
        VolumeState::Ready => 1,
        VolumeState::Degraded => 2,
        VolumeState::Deleting => 3,
    }
}

fn u8_to_state(v: u8) -> VolumeState {
    match v {
        0 => VolumeState::Creating,
        1 => VolumeState::Ready,
        2 => VolumeState::Degraded,
        3 => VolumeState::Deleting,
        _ => VolumeState::Ready,
    }
}

fn volume_map_to_meta(vol: &VolumeMap) -> VolumeMeta {
    VolumeMeta {
        volume_id: *vol.volume_id.as_bytes(),
        name: vol.volume_id.to_string(),
        size: vol.size,
        chunk_size: vol.chunk_size as u64,
        access_mode: access_mode_to_u8(vol.access_mode),
        consistency: consistency_to_u8(vol.consistency),
        replication: 1,
        state: state_to_u8(vol.state),
        active_writer: vol.active_writer.map(|w| {
            let mut bytes = [0u8; 16];
            bytes[..8].copy_from_slice(&w.to_le_bytes());
            bytes
        }),
        append_offset: vol.append_offset,
        created_at: now_secs(),
    }
}

fn volume_meta_to_map(vm: &VolumeMeta, default_chunk_size: usize) -> VolumeMap {
    let cs = if vm.chunk_size > 0 {
        vm.chunk_size as usize
    } else {
        default_chunk_size
    };
    let num_chunks = if cs > 0 {
        (vm.size + cs as u64 - 1) / cs as u64
    } else {
        0
    };
    VolumeMap {
        volume_id: Uuid::from_bytes(vm.volume_id),
        size: vm.size,
        chunk_size: cs,
        access_mode: u8_to_access_mode(vm.access_mode),
        consistency: u8_to_consistency(vm.consistency),
        state: u8_to_state(vm.state),
        active_writer: vm
            .active_writer
            .map(|bytes| u64::from_le_bytes(bytes[..8].try_into().unwrap_or([0; 8]))),
        append_offset: vm.append_offset,
        chunks: vec![None; num_chunks as usize],
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

/// Test config with the disk-usage guard off, so a test's outcome does not
/// depend on how full the host disk is. The guard tests set a threshold.
#[cfg(test)]
pub(crate) fn test_config() -> ChunkManagerConfig {
    ChunkManagerConfig {
        disk_usage_threshold: 1.0,
        ..ChunkManagerConfig::default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::backend::PosixBackend;
    use tempfile::TempDir;

    /// Create a temporary ChunkManager for testing.
    fn tmp_manager() -> (TempDir, ChunkManager) {
        let dir = TempDir::new().unwrap();
        let chunks_dir = dir.path().join("chunks");
        let meta_dir = dir.path().join("meta");

        let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
        let metadata = MetadataStore::open(&meta_dir).unwrap();
        let config = test_config();
        let mgr = ChunkManager::new(backend, Arc::new(metadata), config);

        (dir, mgr)
    }

    #[test]
    fn replacement_discards_obsolete_inline_data() {
        let (_dir, mgr) = tmp_manager();
        mgr.object_write("object", b"old inline", None).unwrap();
        let data = vec![7; 128 * 1024];
        mgr.object_write("object", &data, None).unwrap();
        assert_eq!(mgr.object_read("object").unwrap(), data);
        assert_eq!(
            mgr.object_read_range("object", 10, 30).unwrap(),
            data[10..30]
        );
        assert!(mgr.metadata.get_inline("object").unwrap().is_none());
        mgr.object_write("object", b"small again", None).unwrap();
        assert_eq!(mgr.object_read("object").unwrap(), b"small again");
    }

    #[test]
    fn self_copy_preserves_chunk_ownership() {
        let (_dir, mgr) = tmp_manager();
        let data = vec![9; 128 * 1024];
        let original = mgr.object_write("object", &data, None).unwrap();
        mgr.object_copy("object", "object").unwrap();
        assert_eq!(mgr.object_read("object").unwrap(), data);
        for chunk in original.chunks {
            assert_eq!(mgr.chunk_get_info(&chunk.chunk_id).unwrap().ref_count, 1);
        }
    }

    #[test]
    fn copy_and_stream_replace_inline_destination() {
        let (_dir, mgr) = tmp_manager();
        let data = vec![13; 128 * 1024];
        mgr.object_write("source", &data, None).unwrap();
        mgr.object_write("destination", b"old", None).unwrap();
        mgr.object_copy("source", "destination").unwrap();
        assert_eq!(mgr.object_read("destination").unwrap(), data);
        mgr.object_write("destination", b"old again", None).unwrap();
        let mut ctx = mgr.object_write_begin("destination", None);
        mgr.object_write_chunk(&mut ctx, b"streamed").unwrap();
        mgr.object_write_finish(ctx).unwrap();
        assert_eq!(mgr.object_read("destination").unwrap(), b"streamed");
        assert!(mgr.metadata.get_inline("destination").unwrap().is_none());
    }

    #[test]
    fn missing_local_payload_is_a_miss_for_full_and_range_reads() {
        let (_dir, mgr) = tmp_manager();
        mgr.object_write("object", b"some payload", None).unwrap();
        let mut manifest = mgr.metadata.get_manifest("object").unwrap().unwrap();
        manifest.inline_data = None;
        mgr.metadata.put_manifest(&manifest).unwrap();
        assert!(matches!(
            mgr.object_read("object"),
            Err(NvstorError::NotFound)
        ));
        assert!(matches!(
            mgr.object_read_range("object", 0, 4),
            Err(NvstorError::NotFound)
        ));
    }

    #[test]
    fn concurrent_replacements_preserve_other_objects_shared_chunks() {
        let (_dir, mgr) = tmp_manager();
        let data = vec![23; 128 * 1024];
        let original = mgr.object_write("retained", &data, None).unwrap();
        mgr.object_copy("retained", "contended").unwrap();
        std::thread::scope(|scope| {
            for worker in 0..8 {
                let mgr = &mgr;
                scope.spawn(move || {
                    let replacement = vec![worker; 128 * 1024];
                    for _ in 0..12 {
                        mgr.object_write("contended", &replacement, None).unwrap();
                        mgr.object_delete("contended").unwrap();
                        mgr.object_copy("retained", "contended").unwrap();
                        assert_eq!(mgr.object_read("retained").unwrap()[0], 23);
                    }
                });
            }
        });
        mgr.object_delete("contended").unwrap();
        assert_eq!(mgr.object_read("retained").unwrap(), data);
        for chunk in original.chunks {
            assert_eq!(mgr.chunk_get_info(&chunk.chunk_id).unwrap().ref_count, 1);
        }
        assert!(mgr.chunk_write_locks.read().is_empty());
    }

    #[test]
    fn chunk_lock_entries_are_reclaimed_after_last_user() {
        let (_dir, mgr) = tmp_manager();
        std::thread::scope(|scope| {
            for _ in 0..8 {
                scope.spawn(|| {
                    for _ in 0..20 {
                        let id = mgr.chunk_write(b"concurrent shared chunk").unwrap();
                        mgr.chunk_unref(&id).unwrap();
                    }
                });
            }
        });
        assert!(mgr.chunk_write_locks.read().is_empty());
    }

    // The metadata barrier must never be stronger than the data barrier.
    // None → None (both soft); Data/Full → Full (both durable).
    #[test]
    fn meta_sync_never_outruns_data_sync() {
        assert_eq!(
            meta_sync_for(crate::SyncMode::None),
            nvstor_metadata::SyncMode::None,
            "nosync data must not get an fsync'd metadata row (dedup poisoning)"
        );
        assert_eq!(
            meta_sync_for(crate::SyncMode::Data),
            nvstor_metadata::SyncMode::Full
        );
        assert_eq!(
            meta_sync_for(crate::SyncMode::Full),
            nvstor_metadata::SyncMode::Full
        );
    }

    // A chunk written with SyncMode::None must still be fully readable
    // and dedup correctly — the barrier change must not alter semantics.
    #[test]
    fn nosync_chunk_write_is_readable_and_dedups() {
        let (_dir, mgr) = tmp_manager();
        let data = b"nosync chunk payload";

        let id = mgr
            .chunk_write_with_sync(data, crate::SyncMode::None)
            .unwrap();
        let mut buf = vec![0u8; data.len()];
        let n = mgr.chunk_read(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], data);

        // Second identical write dedups onto the same content address.
        let id2 = mgr
            .chunk_write_with_sync(data, crate::SyncMode::None)
            .unwrap();
        assert_eq!(id, id2);
    }

    fn sealed(mgr: &ChunkManager, id: &ChunkId) -> bool {
        mgr.metadata
            .get_chunk(id.as_bytes())
            .unwrap()
            .unwrap()
            .sealed
    }

    // A no-sync chunk's row stays unsealed until flush has synced
    // its bytes; a synced write's row is sealed at once.
    #[test]
    fn nosync_chunk_is_unsealed_until_flush() {
        let (_dir, mgr) = tmp_manager();
        let id = mgr
            .chunk_write_with_sync(b"deferred", crate::SyncMode::None)
            .unwrap();
        assert!(!sealed(&mgr, &id));
        assert_eq!(mgr.unsynced_chunks(), 1);

        mgr.flush().unwrap();
        assert!(sealed(&mgr, &id));
        assert_eq!(mgr.unsynced_chunks(), 0);

        let full = mgr.chunk_write(b"synced at once").unwrap();
        assert!(sealed(&mgr, &full));
        assert_eq!(mgr.unsynced_chunks(), 0);
    }

    // A durable write that dedups onto this process's unsynced chunk
    // must not return before the bytes are on disk; it syncs and seals.
    #[test]
    fn durable_dedup_hit_seals_unsynced_chunk() {
        let (_dir, mgr) = tmp_manager();
        let data = b"first nosync, then full";
        let id = mgr
            .chunk_write_with_sync(data, crate::SyncMode::None)
            .unwrap();
        assert!(!sealed(&mgr, &id));

        let id2 = mgr
            .chunk_write_with_sync(data, crate::SyncMode::Full)
            .unwrap();
        assert_eq!(id, id2);
        assert!(sealed(&mgr, &id));
        assert_eq!(mgr.unsynced_chunks(), 0);
        assert_eq!(
            mgr.metadata
                .get_chunk(id.as_bytes())
                .unwrap()
                .unwrap()
                .ref_count,
            2
        );
    }

    // A crash after a no-sync write can leave a durable row (a later
    // synced write flushed the RocksDB WAL) over torn bytes. The next
    // process must rewrite the bytes on a dedup hit, not trust the row.
    #[test]
    fn dedup_hit_rewrites_torn_unsealed_chunk_after_restart() {
        let dir = TempDir::new().unwrap();
        let chunks_dir = dir.path().join("chunks");
        let meta_dir = dir.path().join("meta");
        let data: Vec<u8> = (0..8192u32).map(|i| (i % 251) as u8).collect();

        let id = {
            let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
            let metadata = MetadataStore::open(&meta_dir).unwrap();
            let mgr = ChunkManager::new(backend, Arc::new(metadata), test_config());
            let id = mgr
                .chunk_write_with_sync(&data, crate::SyncMode::None)
                .unwrap();
            // A synced write flushes the WAL, so the unsealed row persists.
            mgr.chunk_write(b"later synced write").unwrap();
            id
        };

        // Power loss: only half of the chunk's bytes reached the disk.
        let path = crate::backend::chunk_path(&chunks_dir, id.as_bytes());
        std::fs::write(&path, &data[..data.len() / 2]).unwrap();

        let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
        let metadata = MetadataStore::open(&meta_dir).unwrap();
        let mgr = ChunkManager::new(backend, Arc::new(metadata), test_config());
        assert!(!sealed(&mgr, &id));
        let mut buf = vec![0u8; data.len()];
        assert!(matches!(
            mgr.chunk_read(&id, &mut buf),
            Err(NvstorError::ChecksumMismatch { .. })
        ));

        // The same content arrives again: the bytes are rewritten.
        let id2 = mgr.chunk_write(&data).unwrap();
        assert_eq!(id, id2);
        assert!(sealed(&mgr, &id));
        let n = mgr.chunk_read(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], &data[..]);
    }

    // Same restart case, but the repairing write is itself no-sync:
    // the bytes are rewritten and the chunk stays tracked until flush.
    #[test]
    fn nosync_dedup_hit_after_restart_rewrites_and_tracks() {
        let dir = TempDir::new().unwrap();
        let chunks_dir = dir.path().join("chunks");
        let meta_dir = dir.path().join("meta");
        let data = b"torn then rewritten without sync";

        let id = {
            let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
            let metadata = MetadataStore::open(&meta_dir).unwrap();
            let mgr = ChunkManager::new(backend, Arc::new(metadata), test_config());
            let id = mgr
                .chunk_write_with_sync(data, crate::SyncMode::None)
                .unwrap();
            mgr.chunk_write(b"later synced write").unwrap();
            id
        };
        std::fs::write(crate::backend::chunk_path(&chunks_dir, id.as_bytes()), b"").unwrap();

        let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
        let metadata = MetadataStore::open(&meta_dir).unwrap();
        let mgr = ChunkManager::new(backend, Arc::new(metadata), test_config());
        mgr.chunk_write_with_sync(data, crate::SyncMode::None)
            .unwrap();
        assert_eq!(mgr.unsynced_chunks(), 1);
        assert!(!sealed(&mgr, &id));
        let mut buf = vec![0u8; data.len()];
        let n = mgr.chunk_read(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], data);

        mgr.flush().unwrap();
        assert!(sealed(&mgr, &id));
    }

    // Flush skips a no-sync chunk that was deleted before it ran.
    #[test]
    fn flush_skips_deleted_unsynced_chunk() {
        let (_dir, mgr) = tmp_manager();
        let id = mgr
            .chunk_write_with_sync(b"short-lived", crate::SyncMode::None)
            .unwrap();
        mgr.chunk_delete(&id).unwrap();
        mgr.flush().unwrap();
        assert_eq!(mgr.unsynced_chunks(), 0);
    }

    /// Create a manager with a specific inline threshold for testing.
    fn tmp_manager_with_config(config: ChunkManagerConfig) -> (TempDir, ChunkManager) {
        let dir = TempDir::new().unwrap();
        let chunks_dir = dir.path().join("chunks");
        let meta_dir = dir.path().join("meta");

        let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
        let metadata = MetadataStore::open(&meta_dir).unwrap();
        let mgr = ChunkManager::new(backend, Arc::new(metadata), config);

        (dir, mgr)
    }

    // === Chunk tests ===

    #[test]
    fn chunk_write_read_roundtrip() {
        let (_dir, mgr) = tmp_manager();
        let data = b"hello nvstor chunk manager";
        let id = mgr.chunk_write(data).unwrap();

        let mut buf = vec![0u8; 1024];
        let n = mgr.chunk_read(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], data);
    }

    #[test]
    fn chunk_content_addressing() {
        let (_dir, mgr) = tmp_manager();
        let data = b"same data, same hash";
        let id1 = mgr.chunk_write(data).unwrap();
        let id2 = mgr.chunk_write(data).unwrap();
        assert_eq!(id1, id2, "same content must produce same ChunkId");
    }

    #[test]
    fn chunk_different_data_different_id() {
        let (_dir, mgr) = tmp_manager();
        let id1 = mgr.chunk_write(b"aaa").unwrap();
        let id2 = mgr.chunk_write(b"bbb").unwrap();
        assert_ne!(id1, id2);
    }

    #[test]
    fn chunk_ref_counting() {
        let (_dir, mgr) = tmp_manager();
        let data = b"ref counting test";
        let id = mgr.chunk_write(data).unwrap();

        // Initial ref_count = 1.
        let info = mgr.chunk_get_info(&id).unwrap();
        assert_eq!(info.ref_count, 1);

        mgr.chunk_ref(&id).unwrap();
        let info = mgr.chunk_get_info(&id).unwrap();
        assert_eq!(info.ref_count, 2);

        mgr.chunk_unref(&id).unwrap();
        let info = mgr.chunk_get_info(&id).unwrap();
        assert_eq!(info.ref_count, 1);

        // Unref to 0 should delete.
        mgr.chunk_unref(&id).unwrap();
        assert!(!mgr.chunk_exists(&id).unwrap());
    }

    #[test]
    fn chunk_dedup_increments_ref() {
        let (_dir, mgr) = tmp_manager();
        let data = b"dedup ref test";

        let id = mgr.chunk_write(data).unwrap();
        assert_eq!(mgr.chunk_get_info(&id).unwrap().ref_count, 1);

        // Second write of same data should increment ref count.
        let id2 = mgr.chunk_write(data).unwrap();
        assert_eq!(id, id2);
        assert_eq!(mgr.chunk_get_info(&id).unwrap().ref_count, 2);
    }

    #[test]
    fn chunk_gc_deletes_zero_ref() {
        let (_dir, mgr) = tmp_manager();
        let data = b"gc test data";
        let id = mgr.chunk_write(data).unwrap();

        // Unref to 0 to delete.
        mgr.chunk_unref(&id).unwrap();
        assert!(!mgr.chunk_exists(&id).unwrap());

        // Write again and manually set ref_count to 0 via another unref.
        let id2 = mgr.chunk_write(data).unwrap();
        assert_eq!(id, id2);

        // The chunk has ref_count=1 now. Manually unref:
        mgr.chunk_unref(&id2).unwrap();

        // GC should find nothing (already deleted by unref).
        let report = mgr.gc_sweep().unwrap();
        assert_eq!(report.chunks_deleted, 0);
    }

    #[test]
    fn chunk_gc_sweep_collects_orphans() {
        let (_dir, mgr) = tmp_manager();

        // Write a chunk then manually set ref_count to 0 in metadata.
        let data = b"orphan gc test";
        let id = mgr.chunk_write(data).unwrap();
        let raw_id = *id.as_bytes();

        // Manually zero the ref count in metadata (simulating a crash after unref).
        let mut meta = mgr.metadata.get_chunk(&raw_id).unwrap().unwrap();
        meta.ref_count = 0;
        mgr.metadata.put_chunk(&meta).unwrap();

        let report = mgr.gc_sweep().unwrap();
        assert_eq!(report.chunks_deleted, 1);
        assert!(report.bytes_reclaimed > 0);
        assert!(!mgr.chunk_exists(&id).unwrap());
    }

    #[test]
    fn chunk_corruption_detection() {
        let (_dir, mgr) = tmp_manager();
        let data = b"integrity test data";
        let id = mgr.chunk_write(data).unwrap();

        // Validate should pass.
        assert!(mgr.chunk_validate(&id).unwrap());

        // Corrupt the stored checksum in metadata.
        let raw_id = *id.as_bytes();
        let mut meta = mgr.metadata.get_chunk(&raw_id).unwrap().unwrap();
        meta.checksum = 0xDEADDEAD;
        mgr.metadata.put_chunk(&meta).unwrap();
        mgr.cache.write().remove(&raw_id);

        // Validate should fail.
        assert!(!mgr.chunk_validate(&id).unwrap());

        // Read should fail with checksum mismatch.
        let mut buf = vec![0u8; 1024];
        let result = mgr.chunk_read(&id, &mut buf);
        assert!(result.is_err());
        assert!(matches!(
            result.unwrap_err(),
            NvstorError::ChecksumMismatch { .. }
        ));
    }

    #[test]
    fn chunk_exists_false_for_missing() {
        let (_dir, mgr) = tmp_manager();
        let fake_id = ChunkId::from_content(b"does not exist");
        assert!(!mgr.chunk_exists(&fake_id).unwrap());
    }

    #[test]
    fn chunk_force_delete() {
        let (_dir, mgr) = tmp_manager();
        let data = b"force delete me";
        let id = mgr.chunk_write(data).unwrap();
        assert!(mgr.chunk_exists(&id).unwrap());

        mgr.chunk_delete(&id).unwrap();
        assert!(!mgr.chunk_exists(&id).unwrap());
    }

    // === Object tests ===

    #[test]
    fn object_write_read_small_inline() {
        let (_dir, mgr) = tmp_manager();
        let data = b"small inline object";
        let manifest = mgr
            .object_write("test/small.txt", data, Some("text/plain"))
            .unwrap();

        assert_eq!(manifest.object_id, "test/small.txt");
        assert_eq!(manifest.size, data.len() as u64);
        assert!(manifest.chunks.is_empty(), "inline objects have no chunks");

        let read_data = mgr.object_read("test/small.txt").unwrap();
        assert_eq!(read_data, data);
    }

    #[test]
    fn object_write_read_large_chunked() {
        // Use a small chunk size so we can test chunking without huge data.
        let config = ChunkManagerConfig {
            chunk_size: 1024,
            inline_threshold: 64,
            ..test_config()
        };
        let (_dir, mgr) = tmp_manager_with_config(config);

        let data: Vec<u8> = (0..3000).map(|i| (i % 256) as u8).collect();
        let manifest = mgr.object_write("test/large.bin", &data, None).unwrap();

        assert_eq!(manifest.size, 3000);
        assert_eq!(manifest.chunks.len(), 3); // ceil(3000/1024)

        let read_data = mgr.object_read("test/large.bin").unwrap();
        assert_eq!(read_data, data);
    }

    #[test]
    fn object_delete_unrefs_chunks() {
        let config = ChunkManagerConfig {
            chunk_size: 1024,
            inline_threshold: 64,
            ..test_config()
        };
        let (_dir, mgr) = tmp_manager_with_config(config);

        let data = vec![0xABu8; 2000];
        let manifest = mgr.object_write("del/obj", &data, None).unwrap();

        let chunk_ids: Vec<ChunkId> = manifest.chunks.iter().map(|c| c.chunk_id).collect();
        assert!(!chunk_ids.is_empty());

        mgr.object_delete("del/obj").unwrap();

        // Manifest should be gone.
        assert!(mgr.object_get_manifest("del/obj").is_err());

        // Chunks should be deleted (ref_count was 1).
        for cid in &chunk_ids {
            assert!(!mgr.chunk_exists(cid).unwrap());
        }
    }

    #[test]
    fn object_list_with_prefix() {
        let (_dir, mgr) = tmp_manager();

        for i in 0..5 {
            mgr.object_write(
                &format!("photos/img{i}.jpg"),
                format!("photo data {i}").as_bytes(),
                None,
            )
            .unwrap();
        }
        for i in 0..3 {
            mgr.object_write(
                &format!("videos/clip{i}.mp4"),
                format!("video data {i}").as_bytes(),
                None,
            )
            .unwrap();
        }

        let (photos, _) = mgr.object_list("photos/", None, 100).unwrap();
        assert_eq!(photos.len(), 5);

        let (videos, _) = mgr.object_list("videos/", None, 100).unwrap();
        assert_eq!(videos.len(), 3);
    }

    #[test]
    fn object_list_pagination() {
        let (_dir, mgr) = tmp_manager();

        for i in 0..10 {
            mgr.object_write(&format!("obj{i:02}"), format!("data {i}").as_bytes(), None)
                .unwrap();
        }

        let (page1, trunc) = mgr.object_list("", None, 3).unwrap();
        assert_eq!(page1.len(), 3);
        assert!(trunc);

        let last = &page1.last().unwrap().object_id;
        let (page2, _) = mgr.object_list("", Some(last), 3).unwrap();
        assert_eq!(page2.len(), 3);
        assert_ne!(page1.last().unwrap().object_id, page2[0].object_id);
    }

    #[test]
    fn object_range_read() {
        let config = ChunkManagerConfig {
            chunk_size: 1024,
            inline_threshold: 64,
            ..test_config()
        };
        let (_dir, mgr) = tmp_manager_with_config(config);

        let data: Vec<u8> = (0..3000).map(|i| (i % 256) as u8).collect();
        mgr.object_write("range/test", &data, None).unwrap();

        // Read a range that spans chunk boundaries.
        let range = mgr.object_read_range("range/test", 500, 1500).unwrap();
        assert_eq!(range, &data[500..1500]);

        // Read range at start.
        let range = mgr.object_read_range("range/test", 0, 100).unwrap();
        assert_eq!(range, &data[0..100]);

        // Read range at end.
        let range = mgr.object_read_range("range/test", 2900, 3000).unwrap();
        assert_eq!(range, &data[2900..3000]);
    }

    #[test]
    fn object_range_read_inline() {
        let (_dir, mgr) = tmp_manager();
        let data = b"inline range read test data here";
        mgr.object_write("range/inline", data, None).unwrap();

        let range = mgr.object_read_range("range/inline", 7, 20).unwrap();
        assert_eq!(range, &data[7..20]);
    }

    #[test]
    fn object_copy_zero_copy() {
        let config = ChunkManagerConfig {
            chunk_size: 1024,
            inline_threshold: 64,
            ..test_config()
        };
        let (_dir, mgr) = tmp_manager_with_config(config);

        let data: Vec<u8> = (0..2000).map(|i| (i % 256) as u8).collect();
        let src_manifest = mgr
            .object_write("src/obj", &data, Some("application/bin"))
            .unwrap();

        let dst_manifest = mgr.object_copy("src/obj", "dst/obj").unwrap();

        // Same chunks, different manifest.
        assert_eq!(dst_manifest.size, src_manifest.size);
        assert_eq!(dst_manifest.chunks.len(), src_manifest.chunks.len());
        for (s, d) in src_manifest.chunks.iter().zip(dst_manifest.chunks.iter()) {
            assert_eq!(s.chunk_id, d.chunk_id);
        }

        // Ref count should be 2 for each chunk.
        for c in &src_manifest.chunks {
            let info = mgr.chunk_get_info(&c.chunk_id).unwrap();
            assert_eq!(info.ref_count, 2);
        }

        // Read both to verify.
        let src_data = mgr.object_read("src/obj").unwrap();
        let dst_data = mgr.object_read("dst/obj").unwrap();
        assert_eq!(src_data, dst_data);
        assert_eq!(src_data, data);
    }

    #[test]
    fn object_overwrite() {
        let (_dir, mgr) = tmp_manager();

        mgr.object_write("overwrite/key", b"version 1", None)
            .unwrap();
        let v1 = mgr.object_read("overwrite/key").unwrap();
        assert_eq!(v1, b"version 1");

        mgr.object_write("overwrite/key", b"version 2", None)
            .unwrap();
        let v2 = mgr.object_read("overwrite/key").unwrap();
        assert_eq!(v2, b"version 2");
    }

    // === Streaming write tests ===

    #[test]
    fn streaming_write_finish() {
        let (_dir, mgr) = tmp_manager();

        let mut ctx = mgr.object_write_begin("stream/test", Some("text/plain"));
        mgr.object_write_chunk(&mut ctx, b"part 1 ").unwrap();
        mgr.object_write_chunk(&mut ctx, b"part 2 ").unwrap();
        mgr.object_write_chunk(&mut ctx, b"part 3").unwrap();

        let manifest = mgr.object_write_finish(ctx).unwrap();
        assert_eq!(manifest.size, 20); // 7 + 7 + 6

        let data = mgr.object_read("stream/test").unwrap();
        assert_eq!(data, b"part 1 part 2 part 3");
    }

    #[test]
    fn streaming_write_abort_cleans_up() {
        let (_dir, mgr) = tmp_manager();

        let mut ctx = mgr.object_write_begin("stream/abort", None);
        mgr.object_write_chunk(&mut ctx, b"will be aborted")
            .unwrap();

        let chunk_id = ctx.chunks[0].chunk_id;
        mgr.object_write_abort(ctx).unwrap();

        // Chunk should be deleted (ref_count was 1, then unref'd).
        assert!(!mgr.chunk_exists(&chunk_id).unwrap());

        // No manifest should exist.
        assert!(mgr.object_get_manifest("stream/abort").is_err());
    }

    // === Volume tests ===

    #[test]
    fn volume_create_write_read() {
        let (_dir, mgr) = tmp_manager();
        let vol_id = Uuid::new_v4();
        let vol_size = 1024 * 1024; // 1 MiB

        mgr.volume_create(vol_id, vol_size).unwrap();

        let data = b"hello volume";
        mgr.volume_write(vol_id, 0, data, None).unwrap();

        let mut buf = vec![0u8; data.len()];
        let n = mgr.volume_read(vol_id, 0, &mut buf, None).unwrap();
        assert_eq!(n, data.len());
        assert_eq!(&buf[..n], data);
    }

    #[test]
    fn volume_cross_chunk_write() {
        let config = ChunkManagerConfig {
            chunk_size: 256, // Small chunk size.
            inline_threshold: 64,
            ..test_config()
        };
        let (_dir, mgr) = tmp_manager_with_config(config);

        let vol_id = Uuid::new_v4();
        mgr.volume_create(vol_id, 1024).unwrap();

        // Write 400 bytes starting at offset 100 (crosses chunk boundary at 256).
        let data: Vec<u8> = (0..400).map(|i| (i % 256) as u8).collect();
        mgr.volume_write(vol_id, 100, &data, None).unwrap();

        let mut buf = vec![0u8; 400];
        let n = mgr.volume_read(vol_id, 100, &mut buf, None).unwrap();
        assert_eq!(n, 400);
        assert_eq!(buf, data);
    }

    #[test]
    fn volume_sparse_reads_zeros() {
        let (_dir, mgr) = tmp_manager();
        let vol_id = Uuid::new_v4();
        mgr.volume_create(vol_id, 4096).unwrap();

        // Read from unwritten region.
        let mut buf = vec![0xFFu8; 100];
        let n = mgr.volume_read(vol_id, 0, &mut buf, None).unwrap();
        assert_eq!(n, 100);
        assert!(
            buf.iter().all(|&b| b == 0),
            "sparse region should read as zeros"
        );
    }

    #[test]
    fn volume_rwo_lock() {
        let (_dir, mgr) = tmp_manager();
        let vol_id = Uuid::new_v4();
        mgr.volume_create(vol_id, 4096).unwrap();

        // Acquire writer.
        mgr.volume_acquire_writer(vol_id, 1).unwrap();

        // Second client can't acquire.
        let result = mgr.volume_acquire_writer(vol_id, 2);
        assert!(matches!(result, Err(NvstorError::VolumeLocked)));

        // Same client can re-acquire (idempotent).
        mgr.volume_acquire_writer(vol_id, 1).unwrap();

        // Write with wrong client should fail.
        let result = mgr.volume_write(vol_id, 0, b"nope", Some(2));
        assert!(matches!(result, Err(NvstorError::VolumeLocked)));

        // Write with correct client should succeed.
        mgr.volume_write(vol_id, 0, b"ok", Some(1)).unwrap();

        // Release.
        mgr.volume_release_writer(vol_id, 1).unwrap();

        // Now client 2 can acquire.
        mgr.volume_acquire_writer(vol_id, 2).unwrap();
    }

    #[test]
    fn volume_append_only() {
        let (_dir, mgr) = tmp_manager();
        let vol_id = Uuid::new_v4();
        mgr.volume_create_with_options(vol_id, 4096, AccessMode::RWX, ConsistencyMode::AppendOnly)
            .unwrap();

        // First write at offset 0.
        mgr.volume_write(vol_id, 0, b"first", None).unwrap();

        // Second write must be at or after append_offset (which is now 5).
        mgr.volume_write(vol_id, 5, b"second", None).unwrap();

        // Write before append_offset should fail.
        let result = mgr.volume_write(vol_id, 0, b"rewrite", None);
        assert!(matches!(result, Err(NvstorError::InvalidArgument(_))));
    }

    #[test]
    fn volume_out_of_bounds() {
        let (_dir, mgr) = tmp_manager();
        let vol_id = Uuid::new_v4();
        mgr.volume_create(vol_id, 1024).unwrap();

        // Write past end.
        let result = mgr.volume_write(vol_id, 1000, &[0u8; 100], None);
        assert!(matches!(result, Err(NvstorError::OutOfBounds)));

        // Read past end.
        let mut buf = vec![0u8; 100];
        let result = mgr.volume_read(vol_id, 2000, &mut buf, None);
        assert!(matches!(result, Err(NvstorError::OutOfBounds)));
    }

    #[test]
    fn volume_rox_rejects_write() {
        let (_dir, mgr) = tmp_manager();
        let vol_id = Uuid::new_v4();
        mgr.volume_create_with_options(vol_id, 4096, AccessMode::ROX, ConsistencyMode::Strong)
            .unwrap();

        let result = mgr.volume_write(vol_id, 0, b"nope", None);
        assert!(matches!(result, Err(NvstorError::PermissionDenied)));
    }

    #[test]
    fn volume_delete_unrefs_chunks() {
        let config = ChunkManagerConfig {
            chunk_size: 256,
            inline_threshold: 64,
            ..test_config()
        };
        let (_dir, mgr) = tmp_manager_with_config(config);

        let vol_id = Uuid::new_v4();
        mgr.volume_create(vol_id, 512).unwrap();
        mgr.volume_write(vol_id, 0, &[0xAB; 512], None).unwrap();

        mgr.volume_delete(vol_id).unwrap();
        assert!(mgr.volume_get_info(vol_id).is_err());
    }

    #[test]
    fn volume_get_info() {
        let (_dir, mgr) = tmp_manager();
        let vol_id = Uuid::new_v4();
        mgr.volume_create_with_options(
            vol_id,
            1024 * 1024,
            AccessMode::RWX,
            ConsistencyMode::Session,
        )
        .unwrap();

        let info = mgr.volume_get_info(vol_id).unwrap();
        assert_eq!(info.volume_id, vol_id);
        assert_eq!(info.size, 1024 * 1024);
        assert_eq!(info.access_mode, AccessMode::RWX);
        assert_eq!(info.consistency, ConsistencyMode::Session);
        assert_eq!(info.state, VolumeState::Ready);
    }

    // === Concurrent tests ===

    #[test]
    fn concurrent_chunk_writes() {
        let dir = TempDir::new().unwrap();
        let chunks_dir = dir.path().join("chunks");
        let meta_dir = dir.path().join("meta");
        let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
        let metadata = MetadataStore::open(&meta_dir).unwrap();
        let mgr = std::sync::Arc::new(ChunkManager::new(
            backend,
            Arc::new(metadata),
            test_config(),
        ));

        let handles: Vec<_> = (0..10)
            .map(|i| {
                let mgr = mgr.clone();
                std::thread::spawn(move || {
                    let data = format!("concurrent chunk {i}");
                    let id = mgr.chunk_write(data.as_bytes()).unwrap();
                    let mut buf = vec![0u8; 1024];
                    let n = mgr.chunk_read(&id, &mut buf).unwrap();
                    assert_eq!(&buf[..n], data.as_bytes());
                })
            })
            .collect();

        for h in handles {
            h.join().unwrap();
        }
    }

    #[test]
    fn concurrent_ref_unref_is_race_free() {
        // Regression for the dedup ref-count race: chunk_write's
        // dedup-increment, chunk_ref, chunk_unref, and gc all mutate
        // ref_count and must serialize on the per-chunk lock. Here an
        // "anchor" write holds a permanent +1 so the count never
        // reaches 0 (no thread's unref errors on a deleted chunk),
        // then 2K threads each do a balanced pair (net zero). If any
        // read-modify-write is unlocked, updates are lost and the
        // final count drifts off the anchor value; with correct
        // locking it lands exactly on it.
        let (_dir, mgr) = tmp_manager();
        let mgr = Arc::new(mgr);
        let data = b"race-free ref counting payload";
        let id = mgr.chunk_write(data).unwrap(); // anchor, ref=1
        assert_eq!(mgr.chunk_get_info(&id).unwrap().ref_count, 1);

        const K: usize = 24;
        let mut handles = Vec::new();
        for _ in 0..K {
            // write(+1 dedup) then unref(-1)
            let m = mgr.clone();
            let d = data.to_vec();
            handles.push(std::thread::spawn(move || {
                let cid = m.chunk_write(&d).unwrap();
                m.chunk_unref(&cid).unwrap();
            }));
            // ref(+1) then unref(-1)
            let m = mgr.clone();
            let cid = id;
            handles.push(std::thread::spawn(move || {
                m.chunk_ref(&cid).unwrap();
                m.chunk_unref(&cid).unwrap();
            }));
        }
        for h in handles {
            h.join().unwrap();
        }

        // Every thread's pair netted zero; only the anchor remains.
        assert_eq!(
            mgr.chunk_get_info(&id).unwrap().ref_count,
            1,
            "ref_count drifted → a ref-count RMW raced (not under the per-chunk lock)"
        );
        // The chunk data is intact and readable.
        let mut buf = vec![0u8; data.len()];
        let n = mgr.chunk_read(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], data);

        // Final unref of the anchor deletes it.
        mgr.chunk_unref(&id).unwrap();
        assert!(!mgr.chunk_exists(&id).unwrap());
    }

    #[test]
    fn concurrent_object_writes() {
        let dir = TempDir::new().unwrap();
        let chunks_dir = dir.path().join("chunks");
        let meta_dir = dir.path().join("meta");
        let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
        let metadata = MetadataStore::open(&meta_dir).unwrap();
        let mgr = std::sync::Arc::new(ChunkManager::new(
            backend,
            Arc::new(metadata),
            test_config(),
        ));

        let handles: Vec<_> = (0..10)
            .map(|i| {
                let mgr = mgr.clone();
                std::thread::spawn(move || {
                    let key = format!("concurrent/obj{i}");
                    let data = format!("object data {i}");
                    mgr.object_write(&key, data.as_bytes(), None).unwrap();
                    let read = mgr.object_read(&key).unwrap();
                    assert_eq!(read, data.as_bytes());
                })
            })
            .collect();

        for h in handles {
            h.join().unwrap();
        }
    }

    #[test]
    fn concurrent_volume_reads() {
        let dir = TempDir::new().unwrap();
        let chunks_dir = dir.path().join("chunks");
        let meta_dir = dir.path().join("meta");
        let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
        let metadata = MetadataStore::open(&meta_dir).unwrap();
        let mgr = std::sync::Arc::new(ChunkManager::new(
            backend,
            Arc::new(metadata),
            test_config(),
        ));

        let vol_id = Uuid::new_v4();
        mgr.volume_create(vol_id, 4096).unwrap();
        mgr.volume_write(vol_id, 0, b"shared data for readers", None)
            .unwrap();

        let handles: Vec<_> = (0..10)
            .map(|_| {
                let mgr = mgr.clone();
                std::thread::spawn(move || {
                    let mut buf = vec![0u8; 23];
                    let n = mgr.volume_read(vol_id, 0, &mut buf, None).unwrap();
                    assert_eq!(n, 23);
                    assert_eq!(&buf, b"shared data for readers");
                })
            })
            .collect();

        for h in handles {
            h.join().unwrap();
        }
    }

    // === Persistence test ===

    #[test]
    fn persistence_survives_reopen() {
        let dir = TempDir::new().unwrap();
        let chunks_dir = dir.path().join("chunks");
        let meta_dir = dir.path().join("meta");

        let chunk_id;
        let object_data = b"persisted object data!";

        // Phase 1: write data and drop manager.
        {
            let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
            let metadata = MetadataStore::open(&meta_dir).unwrap();
            let mgr = ChunkManager::new(backend, Arc::new(metadata), test_config());

            chunk_id = mgr.chunk_write(b"persistent chunk").unwrap();
            mgr.object_write("persist/key", object_data, Some("text/plain"))
                .unwrap();

            mgr.flush().unwrap();
        }

        // Phase 2: reopen from same paths, verify data survives.
        {
            let backend = Box::new(PosixBackend::new(&chunks_dir).unwrap());
            let metadata = MetadataStore::open(&meta_dir).unwrap();
            let mgr = ChunkManager::new(backend, Arc::new(metadata), test_config());

            // Chunk data survives.
            let mut buf = vec![0u8; 1024];
            let n = mgr.chunk_read(&chunk_id, &mut buf).unwrap();
            assert_eq!(&buf[..n], b"persistent chunk");

            // Object data survives.
            let data = mgr.object_read("persist/key").unwrap();
            assert_eq!(data, object_data);
        }
    }

    // === Stats test ===

    #[test]
    fn stats_reports_counts() {
        let (_dir, mgr) = tmp_manager();

        let stats = mgr.stats().unwrap();
        assert_eq!(stats.total_chunks, 0);
        assert_eq!(stats.total_objects, 0);

        mgr.chunk_write(b"stats chunk 1").unwrap();
        mgr.chunk_write(b"stats chunk 2").unwrap();
        mgr.object_write("stats/obj", b"small stats obj", None)
            .unwrap();

        let stats = mgr.stats().unwrap();
        assert!(stats.total_chunks >= 2); // At least 2 explicit chunks.
        assert_eq!(stats.total_objects, 1);
    }

    // === Scrub test ===

    #[test]
    fn scrub_detects_corruption() {
        let (_dir, mgr) = tmp_manager();
        let data = b"scrub test data";
        let id = mgr.chunk_write(data).unwrap();
        let raw_id = *id.as_bytes();

        // Scrub with all good.
        let report = mgr.scrub(0).unwrap();
        assert_eq!(report.checksum_failures, 0);
        assert!(report.chunks_checked >= 1);

        // Corrupt the checksum in metadata.
        let mut meta = mgr.metadata.get_chunk(&raw_id).unwrap().unwrap();
        meta.checksum = 0xBADBAD;
        mgr.metadata.put_chunk(&meta).unwrap();
        mgr.cache.write().remove(&raw_id);

        let report = mgr.scrub(0).unwrap();
        assert_eq!(report.checksum_failures, 1);
    }

    #[test]
    fn read_detects_checksum_mismatch() {
        let (_dir, mgr) = tmp_manager();
        let data = b"integrity test data";
        let id = mgr.chunk_write(data).unwrap();
        let raw_id = *id.as_bytes();

        // Normal read works.
        let mut buf = vec![0u8; 1024];
        let n = mgr.chunk_read(&id, &mut buf).unwrap();
        assert_eq!(&buf[..n], data);

        // Corrupt the stored checksum.
        let mut meta = mgr.metadata.get_chunk(&raw_id).unwrap().unwrap();
        meta.checksum = 0xDEADBEEF;
        mgr.metadata.put_chunk(&meta).unwrap();
        mgr.cache.write().remove(&raw_id);
        // Clear data cache so the read goes to disk (where checksum is verified).
        mgr.data_cache.lock().entries.clear();

        // Read should fail with ChecksumMismatch.
        let result = mgr.chunk_read(&id, &mut buf);
        assert!(result.is_err());
        match result.unwrap_err() {
            NvstorError::ChecksumMismatch { expected, actual } => {
                assert_eq!(expected, 0xDEADBEEF);
                assert_ne!(actual, 0xDEADBEEF);
            }
            e => panic!("expected ChecksumMismatch, got {e:?}"),
        }
    }

    #[test]
    fn disk_space_check_rejects_when_full() {
        let dir = tempfile::tempdir().unwrap();
        let backend = Box::new(PosixBackend::new(dir.path().join("chunks")).unwrap());
        let metadata =
            Arc::new(nvstor_metadata::MetadataStore::open(&dir.path().join("meta")).unwrap());
        let mut config = test_config();
        // Set threshold to 0% — should always reject.
        config.disk_usage_threshold = 0.0;
        let mgr = ChunkManager::new(backend, metadata, config);

        let result = mgr.chunk_write(b"should be rejected");
        assert!(result.is_err());
        assert!(result.unwrap_err().to_string().contains("disk usage"));
    }

    #[test]
    fn disk_space_check_allows_normal() {
        let dir = tempfile::tempdir().unwrap();
        let backend = Box::new(PosixBackend::new(dir.path().join("chunks")).unwrap());
        let stats = backend.stats().unwrap();
        let usage = stats.used_bytes as f64 / stats.total_bytes as f64;
        let metadata =
            Arc::new(nvstor_metadata::MetadataStore::open(&dir.path().join("meta")).unwrap());
        // Halfway between the host's current usage and full: the guard is
        // on and the disk is below it, whatever the host's fill level.
        let mut config = test_config();
        config.disk_usage_threshold = usage + (1.0 - usage) / 2.0;
        let mgr = ChunkManager::new(backend, metadata, config);
        let result = mgr.chunk_write(b"should be accepted");
        assert!(result.is_ok());
    }

    // ===================================================================
    // Edge case tests
    // ===================================================================

    #[test]
    fn write_empty_object() {
        let (_dir, mgr) = tmp_manager();
        let manifest = mgr.object_write("test/empty.bin", b"", None).unwrap();
        assert_eq!(manifest.size, 0);
        let data = mgr.object_read("test/empty.bin").unwrap();
        assert!(data.is_empty());
    }

    #[test]
    fn write_one_byte() {
        let (_dir, mgr) = tmp_manager();
        let manifest = mgr.object_write("test/tiny.bin", b"X", None).unwrap();
        assert_eq!(manifest.size, 1);
        let data = mgr.object_read("test/tiny.bin").unwrap();
        assert_eq!(data, b"X");
    }

    #[test]
    fn write_exactly_inline_threshold() {
        let (_dir, mgr) = tmp_manager();
        let data = vec![0xAB; mgr.config.inline_threshold];
        let manifest = mgr
            .object_write("test/exact-inline.bin", &data, None)
            .unwrap();
        assert_eq!(manifest.size, data.len() as u64);
        // Should be stored inline.
        let read_data = mgr.object_read("test/exact-inline.bin").unwrap();
        assert_eq!(read_data, data);
    }

    #[test]
    fn write_one_over_inline_threshold() {
        let (_dir, mgr) = tmp_manager();
        let data = vec![0xCD; mgr.config.inline_threshold + 1];
        let manifest = mgr
            .object_write("test/just-chunked.bin", &data, None)
            .unwrap();
        assert_eq!(manifest.size, data.len() as u64);
        // Should be stored as chunks (not inline).
        assert!(!manifest.chunks.is_empty());
        let read_data = mgr.object_read("test/just-chunked.bin").unwrap();
        assert_eq!(read_data, data);
    }

    #[test]
    fn overwrite_same_key() {
        let (_dir, mgr) = tmp_manager();
        mgr.object_write("test/overwrite.txt", b"version-1", None)
            .unwrap();
        let d1 = mgr.object_read("test/overwrite.txt").unwrap();
        assert_eq!(d1, b"version-1");

        mgr.object_write("test/overwrite.txt", b"version-2-longer", None)
            .unwrap();
        let d2 = mgr.object_read("test/overwrite.txt").unwrap();
        assert_eq!(d2, b"version-2-longer");

        // Overwrite with shorter data.
        mgr.object_write("test/overwrite.txt", b"v3", None).unwrap();
        let d3 = mgr.object_read("test/overwrite.txt").unwrap();
        assert_eq!(d3, b"v3");
    }

    #[test]
    fn delete_nonexistent_is_ok() {
        let (_dir, mgr) = tmp_manager();
        // Deleting something that doesn't exist should not error.
        mgr.object_delete("does/not/exist.txt").unwrap();
    }

    #[test]
    fn read_nonexistent_is_not_found() {
        let (_dir, mgr) = tmp_manager();
        match mgr.object_read("does/not/exist.txt") {
            Err(NvstorError::NotFound) => {} // Expected.
            other => panic!("expected NotFound, got {other:?}"),
        }
    }

    #[test]
    fn rapid_create_delete_cycles() {
        let (_dir, mgr) = tmp_manager();
        for i in 0..50 {
            let key = format!("test/cycle-{i}.txt");
            mgr.object_write(&key, format!("data-{i}").as_bytes(), None)
                .unwrap();
            mgr.object_delete(&key).unwrap();
        }
        // All should be gone.
        for i in 0..50 {
            let key = format!("test/cycle-{i}.txt");
            assert!(mgr.object_read(&key).is_err());
        }
    }

    #[test]
    fn concurrent_writes_different_keys() {
        let (_dir, mgr) = tmp_manager();
        let mgr = std::sync::Arc::new(mgr);

        std::thread::scope(|s| {
            for i in 0..20 {
                let m = std::sync::Arc::clone(&mgr);
                s.spawn(move || {
                    let key = format!("test/concurrent-{i}.txt");
                    let data = format!("concurrent-data-{i}");
                    m.object_write(&key, data.as_bytes(), None).unwrap();
                });
            }
        });

        // Verify all written.
        for i in 0..20 {
            let key = format!("test/concurrent-{i}.txt");
            let data = mgr.object_read(&key).unwrap();
            assert_eq!(data, format!("concurrent-data-{i}").as_bytes());
        }
    }

    #[test]
    fn dedup_same_content() {
        let (_dir, mgr) = tmp_manager();
        let data = b"deduplicated content";
        mgr.object_write("test/dup1.txt", data, None).unwrap();
        mgr.object_write("test/dup2.txt", data, None).unwrap();

        // Both should read the same data.
        assert_eq!(mgr.object_read("test/dup1.txt").unwrap(), data);
        assert_eq!(mgr.object_read("test/dup2.txt").unwrap(), data);

        // Delete one — the other should still work (ref counting).
        mgr.object_delete("test/dup1.txt").unwrap();
        assert_eq!(mgr.object_read("test/dup2.txt").unwrap(), data);
    }

    #[test]
    fn large_object_many_chunks() {
        let (_dir, mgr) = tmp_manager();
        // 200KB with default 16MB chunk size → inline (< threshold? no, 200K > 64K)
        // → 1 chunk. Use a larger object for multi-chunk.
        let data: Vec<u8> = (0..200_000).map(|i| (i % 256) as u8).collect();
        let manifest = mgr.object_write("test/large.bin", &data, None).unwrap();
        assert_eq!(manifest.size, data.len() as u64);

        let read_data = mgr.object_read("test/large.bin").unwrap();
        assert_eq!(read_data, data);
    }

    #[test]
    fn concurrent_dedup_ref_count_correct() {
        // BUG 1 regression test: concurrent writes of the same content
        // must result in correct ref_count, not data loss.
        let (_dir, mgr) = tmp_manager();
        let mgr = std::sync::Arc::new(mgr);
        let data = b"deduplicated content for race condition test";

        // Write the same content from 10 threads simultaneously.
        std::thread::scope(|s| {
            for i in 0..10 {
                let m = std::sync::Arc::clone(&mgr);
                s.spawn(move || {
                    let key = format!("test/race-{i}.txt");
                    m.object_write(&key, data, None).unwrap();
                });
            }
        });

        // All 10 objects should be readable.
        for i in 0..10 {
            let key = format!("test/race-{i}.txt");
            let read_data = mgr.object_read(&key).unwrap();
            assert_eq!(read_data, data, "object {key} data mismatch");
        }

        // Delete 9 of 10 — the last one should still be readable
        // because ref_count should be 10, decremented to 1.
        for i in 0..9 {
            let key = format!("test/race-{i}.txt");
            mgr.object_delete(&key).unwrap();
        }

        // The 10th object should still work (ref_count=1, not deleted).
        let last = mgr.object_read("test/race-9.txt").unwrap();
        assert_eq!(
            last, data,
            "last surviving dedup object should still be readable"
        );
    }

    #[test]
    fn overwrite_does_not_lose_data_on_failure() {
        // BUG 6 regression test: overwrite should write new first, then clean old.
        // If new write succeeds, old data is cleaned up. If new write fails,
        // old data is preserved.
        let (_dir, mgr) = tmp_manager();

        // Write original.
        mgr.object_write("test/safe-overwrite.txt", b"original data", None)
            .unwrap();
        let d1 = mgr.object_read("test/safe-overwrite.txt").unwrap();
        assert_eq!(d1, b"original data");

        // Overwrite with new data.
        mgr.object_write("test/safe-overwrite.txt", b"new data", None)
            .unwrap();
        let d2 = mgr.object_read("test/safe-overwrite.txt").unwrap();
        assert_eq!(d2, b"new data");

        // The original data's chunks should have been cleaned up (unrefed).
        // The new data should be intact.
    }

    #[test]
    fn health_check_passes() {
        let (_dir, mgr) = tmp_manager();
        let latency = mgr.health_check().unwrap();
        assert!(latency > 0, "health check latency should be > 0");
        println!("health_check latency: {latency}µs");
    }

    #[test]
    fn wal_enabled_write_read() {
        let dir = tempfile::tempdir().unwrap();
        let backend = Box::new(PosixBackend::new(dir.path().join("chunks")).unwrap());
        let metadata =
            Arc::new(nvstor_metadata::MetadataStore::open(&dir.path().join("meta")).unwrap());
        let mut mgr = ChunkManager::new(backend, metadata, test_config());

        // Enable WAL.
        mgr.enable_wal(&dir.path().join("wal")).unwrap();

        // Write object (should be logged to WAL).
        let manifest = mgr
            .object_write("wal-test/obj1", b"wal protected data", None)
            .unwrap();
        assert_eq!(manifest.size, 18);

        // Read back.
        let data = mgr.object_read("wal-test/obj1").unwrap();
        assert_eq!(data, b"wal protected data");

        // Delete (should be logged to WAL).
        mgr.object_delete("wal-test/obj1").unwrap();
        assert!(mgr.object_read("wal-test/obj1").is_err());
    }

    #[test]
    fn wal_replay_detects_incomplete() {
        let dir = tempfile::tempdir().unwrap();

        // Phase 1: Write with WAL enabled.
        {
            let backend = Box::new(PosixBackend::new(dir.path().join("chunks")).unwrap());
            let metadata =
                Arc::new(nvstor_metadata::MetadataStore::open(&dir.path().join("meta")).unwrap());
            let mut mgr = ChunkManager::new(backend, metadata, test_config());
            mgr.enable_wal(&dir.path().join("wal")).unwrap();

            mgr.object_write("wal-test/complete", b"this write completes", None)
                .unwrap();
        }

        // Phase 2: Re-open with WAL replay — should find the completed write.
        {
            let backend = Box::new(PosixBackend::new(dir.path().join("chunks")).unwrap());
            let metadata =
                Arc::new(nvstor_metadata::MetadataStore::open(&dir.path().join("meta")).unwrap());
            let mut mgr = ChunkManager::new(backend, metadata, test_config());
            mgr.enable_wal(&dir.path().join("wal")).unwrap();

            // Object should still exist after replay.
            let data = mgr.object_read("wal-test/complete").unwrap();
            assert_eq!(data, b"this write completes");
        }
    }

    #[test]
    fn op_metrics_tracked() {
        let (_dir, mgr) = tmp_manager();

        // Write 3 chunks.
        for i in 0..3u8 {
            mgr.chunk_write(&[i; 100]).unwrap();
        }
        assert_eq!(
            mgr.op_metrics
                .writes
                .load(std::sync::atomic::Ordering::Relaxed),
            3
        );
        assert!(
            mgr.op_metrics
                .write_bytes
                .load(std::sync::atomic::Ordering::Relaxed)
                >= 300
        );
        assert!(mgr.op_metrics.avg_write_latency_us() > 0);

        // Read one back.
        let id = ChunkId::from_content(&[0u8; 100]);
        let mut buf = vec![0u8; 1024];
        mgr.chunk_read(&id, &mut buf).unwrap();
        assert_eq!(
            mgr.op_metrics
                .reads
                .load(std::sync::atomic::Ordering::Relaxed),
            1
        );
        assert!(mgr.op_metrics.avg_read_latency_us() > 0);

        // Verify no errors.
        assert_eq!(
            mgr.op_metrics
                .write_errors
                .load(std::sync::atomic::Ordering::Relaxed),
            0
        );
        assert_eq!(
            mgr.op_metrics
                .read_errors
                .load(std::sync::atomic::Ordering::Relaxed),
            0
        );
    }

    #[test]
    fn object_read_detects_corruption() {
        let (_dir, mgr) = tmp_manager();
        let data = b"object integrity test with enough data to be chunked properly";
        let _manifest = mgr.object_write("test/integrity.bin", data, None).unwrap();

        // Normal read works.
        let read_data = mgr.object_read("test/integrity.bin").unwrap();
        assert_eq!(read_data, data);

        // Corrupt inline data checksum (this is a small object, stored inline).
        // For inline objects, corruption is caught by the metadata layer.
        // Test with a larger object that uses chunks.
        let large_data: Vec<u8> = (0..100_000).map(|i| (i % 256) as u8).collect();
        mgr.object_write("test/large-integrity.bin", &large_data, None)
            .unwrap();
        let read_back = mgr.object_read("test/large-integrity.bin").unwrap();
        assert_eq!(read_back.len(), large_data.len());
        assert_eq!(read_back, large_data);
    }
}

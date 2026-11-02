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

use std::path::Path;
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use rocksdb::{
    ColumnFamilyDescriptor, DBWithThreadMode, IteratorMode, MultiThreaded, Options,
    WriteBatchWithTransaction, WriteOptions,
};
use serde::{Deserialize, Serialize};

// ---------------------------------------------------------------------------
// Schema version
// ---------------------------------------------------------------------------

const CURRENT_SCHEMA_VERSION: u32 = 1;
const SCHEMA_VERSION_KEY: &[u8] = b"schema_version";

// ---------------------------------------------------------------------------
// Column family names
// ---------------------------------------------------------------------------

const CF_DEFAULT: &str = "default";
const CF_CHUNKS: &str = "chunks";
const CF_MANIFESTS: &str = "manifests";
const CF_VOLUMES: &str = "volumes";
const CF_INLINE: &str = "inline";
const CF_VOLUME_CHUNKS: &str = "volume_chunks";
const CF_BUCKETS: &str = "buckets";
const CF_MULTIPART_UPLOADS: &str = "multipart_uploads";
const CF_MULTIPART_PARTS: &str = "multipart_parts";
const CF_OBJECT_VERSIONS: &str = "object_versions";

const ALL_CFS: &[&str] = &[
    CF_CHUNKS,
    CF_MANIFESTS,
    CF_VOLUMES,
    CF_INLINE,
    CF_VOLUME_CHUNKS,
    CF_BUCKETS,
    CF_MULTIPART_UPLOADS,
    CF_MULTIPART_PARTS,
    CF_OBJECT_VERSIONS,
];

// ---------------------------------------------------------------------------
// Error type
// ---------------------------------------------------------------------------

#[derive(Debug, thiserror::Error)]
pub enum MetadataError {
    #[error("RocksDB error: {0}")]
    RocksDb(String),
    #[error("serialization error: {0}")]
    Serialization(String),
    #[error("schema version {found} is newer than supported {supported}")]
    SchemaVersionTooNew { found: u32, supported: u32 },
    #[error("not found")]
    NotFound,
    #[error("internal: {0}")]
    Internal(String),
}

impl From<rocksdb::Error> for MetadataError {
    fn from(e: rocksdb::Error) -> Self {
        MetadataError::RocksDb(e.to_string())
    }
}

// ---------------------------------------------------------------------------
// Domain types (local, will be replaced with nvstor-core types later)
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChunkMeta {
    pub id: [u8; 32],
    pub size: u64,
    pub checksum: u32,
    pub ref_count: u64,
    pub sealed: bool,
    pub corrupt: bool,
    pub corrupt_reason: Option<String>,
    pub created_at: i64,
    pub last_accessed: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ChunkRef {
    pub chunk_id: [u8; 32],
    pub length: u32,
    pub checksum: u32,
    pub logical_start: u64,
    pub logical_end: u64,
    /// Node IDs where this chunk is stored.
    /// Populated during distributed_write. Empty for single-node mode.
    #[serde(default)]
    pub nodes: Vec<u64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ManifestMeta {
    pub object_id: String,
    pub size: u64,
    pub chunks: Vec<ChunkRef>,
    pub etag: String,
    pub content_type: String,
    pub created_at: i64,
    pub modified_at: i64,
    pub inline_data: Option<Vec<u8>>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct VolumeMeta {
    pub volume_id: [u8; 16],
    pub name: String,
    pub size: u64,
    pub chunk_size: u64,
    pub access_mode: u8,
    pub consistency: u8,
    pub replication: u8,
    pub state: u8,
    pub active_writer: Option<[u8; 16]>,
    pub append_offset: u64,
    pub created_at: i64,
}

/// S3 bucket metadata — persistent, survives restart.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct BucketMeta {
    /// Bucket name (globally unique within this cluster).
    pub name: String,
    /// Owner access key ID.
    pub owner: String,
    /// Creation timestamp (unix seconds).
    pub created_at: i64,
    /// AWS region string.
    pub region: String,
    /// Versioning state: 0=disabled, 1=enabled, 2=suspended.
    pub versioning: u8,
}

/// S3 multipart upload metadata — persistent, survives restart.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct MultipartUploadMeta {
    /// Upload ID (UUID string).
    pub upload_id: String,
    /// Target bucket name.
    pub bucket: String,
    /// Target object key.
    pub key: String,
    /// Content-Type for the final assembled object.
    pub content_type: String,
    /// Upload creation timestamp (unix seconds).
    pub created_at: i64,
    /// Owner access key ID.
    pub owner: String,
}

/// S3 multipart upload part metadata — persistent, survives restart.
/// Part data is stored via ChunkManager (not in heap memory).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct PartMeta {
    /// Part number (1-10000).
    pub part_number: u32,
    /// ETag of this part (CRC32 hex of part data).
    pub etag: String,
    /// Part size in bytes.
    pub size: u64,
    /// Object ID in ChunkManager where this part's data is stored.
    /// Parts are stored as temporary objects: "__multipart__/{upload_id}/part-{part_number:05}"
    pub object_id: String,
}

/// A single version of an S3 object — either real data or a delete marker.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ObjectVersion {
    /// Version ID (reverse-timestamp for natural newest-first ordering).
    pub version_id: String,
    /// Bucket name.
    pub bucket: String,
    /// Object key.
    pub key: String,
    /// True if this is a delete marker (no data).
    pub is_delete_marker: bool,
    /// True if this is the latest version.
    pub is_latest: bool,
    /// Object size (0 for delete markers).
    pub size: u64,
    /// ETag (empty for delete markers).
    pub etag: String,
    /// Content type (empty for delete markers).
    pub content_type: String,
    /// Last modified timestamp (unix seconds).
    pub last_modified: i64,
    /// Owner.
    pub owner: String,
    /// The object_id in the manifests CF (empty for delete markers).
    /// This is how we link a version to its actual chunk data.
    pub manifest_object_id: String,
}

// ---------------------------------------------------------------------------
// Batch operations
// ---------------------------------------------------------------------------

pub enum BatchOp {
    PutChunk(ChunkMeta),
    DeleteChunk([u8; 32]),
    PutManifest(ManifestMeta),
    DeleteManifest(String),
    PutVolume(VolumeMeta),
    DeleteVolume([u8; 16]),
    PutInline { object_id: String, data: Vec<u8> },
    DeleteInline(String),
    PutBucket(BucketMeta),
    DeleteBucket(String),
    PutMultipartUpload(MultipartUploadMeta),
    DeleteMultipartUpload(String),
    PutPart { upload_id: String, part: PartMeta },
    DeletePart { upload_id: String, part_number: u32 },
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn hex(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        s.push_str(&format!("{b:02x}"));
    }
    s
}

fn from_hex(s: &str) -> Result<Vec<u8>, MetadataError> {
    if s.len() % 2 != 0 {
        return Err(MetadataError::Internal("odd hex length".into()));
    }
    (0..s.len())
        .step_by(2)
        .map(|i| {
            u8::from_str_radix(&s[i..i + 2], 16)
                .map_err(|e| MetadataError::Internal(format!("hex decode: {e}")))
        })
        .collect()
}

fn ser<T: Serialize>(v: &T) -> Result<Vec<u8>, MetadataError> {
    bincode::serialize(v).map_err(|e| MetadataError::Serialization(e.to_string()))
}

fn de<'a, T: Deserialize<'a>>(bytes: &'a [u8]) -> Result<T, MetadataError> {
    bincode::deserialize(bytes).map_err(|e| MetadataError::Serialization(e.to_string()))
}

fn sync_write_opts() -> WriteOptions {
    let mut opts = WriteOptions::default();
    opts.set_sync(true);
    opts
}

fn nosync_write_opts() -> WriteOptions {
    let mut opts = WriteOptions::default();
    opts.set_sync(false);
    opts
}

/// Durability mode for batched metadata writes. Mirrors
/// `nvstor_storage::SyncMode` so the protocol layer can pass one mode
/// to both the data and the metadata store.
#[derive(Copy, Clone, Debug, PartialEq, Eq)]
pub enum SyncMode {
    /// No fsync. RocksDB writes hit the WAL in the OS page cache;
    /// kernel writeback flushes them eventually. Caller is responsible
    /// for triggering durability via an explicit flush op.
    None,
    /// fsync the WAL before returning. Equivalent to today's default.
    Full,
}

impl Default for SyncMode {
    /// Conservative default = full sync, matches today's behavior so
    /// existing call sites don't regress.
    fn default() -> Self {
        SyncMode::Full
    }
}

/// Pending metadata mutations collected during one unit of work
/// (typically one COMPOUND or one S3 request). Flushed via
/// [`MetadataStore::commit_batch`] which makes one RocksDB write
/// regardless of how many ops the batch contains, and respects the
/// caller's `SyncMode` for the durability barrier.
pub struct WriteBatch {
    /// Each op records the CF and the key/value (Some = put, None = delete).
    ops: Vec<(&'static str, Vec<u8>, Option<Vec<u8>>)>,
}

impl WriteBatch {
    pub fn new() -> Self {
        Self { ops: Vec::new() }
    }
    pub fn len(&self) -> usize {
        self.ops.len()
    }
    pub fn is_empty(&self) -> bool {
        self.ops.is_empty()
    }

    /// Record an inline put. CF: CF_INLINE.
    pub fn put_inline(&mut self, object_id: &str, data: &[u8]) {
        self.ops.push((
            CF_INLINE,
            object_id.as_bytes().to_vec(),
            Some(data.to_vec()),
        ));
    }

    /// Record an inline delete.
    pub fn delete_inline(&mut self, object_id: &str) {
        self.ops
            .push((CF_INLINE, object_id.as_bytes().to_vec(), None));
    }

    /// Record a volume_chunk put. CF: CF_VOLUME_CHUNKS. Mirrors the
    /// non-batched `MetadataStore::put_volume_chunk`: the value is the
    /// hex-encoded chunk ID (utf-8 string), not the raw bytes — that's
    /// the format the existing `get_volume_chunk` reader expects.
    pub fn put_volume_chunk(&mut self, volume_id: &[u8; 16], offset: u64, chunk_id: &[u8; 32]) {
        let key = volume_chunk_key(volume_id, offset);
        let val = hex(chunk_id).into_bytes();
        self.ops
            .push((CF_VOLUME_CHUNKS, key.into_bytes(), Some(val)));
    }
}

impl Default for WriteBatch {
    fn default() -> Self {
        Self::new()
    }
}

fn volume_chunk_key(volume_id: &[u8; 16], offset: u64) -> String {
    format!("{}/{offset:020}", hex(volume_id))
}

// ---------------------------------------------------------------------------
// MetadataStore
// ---------------------------------------------------------------------------

type DB = DBWithThreadMode<MultiThreaded>;

#[derive(Debug)]
pub struct MetadataStore {
    db: Arc<DB>,
}

impl MetadataStore {
    /// Open or create a metadata store at the given path.
    /// Performs schema version check and migration if needed.
    pub fn open(path: &Path) -> Result<Self, MetadataError> {
        let mut db_opts = Options::default();
        db_opts.create_if_missing(true);
        db_opts.create_missing_column_families(true);

        let cf_descriptors: Vec<ColumnFamilyDescriptor> = std::iter::once(CF_DEFAULT)
            .chain(ALL_CFS.iter().copied())
            .map(|name| ColumnFamilyDescriptor::new(name, Options::default()))
            .collect();

        let db = DB::open_cf_descriptors(&db_opts, path, cf_descriptors)?;
        let store = MetadataStore { db: Arc::new(db) };
        store.check_schema_version()?;
        Ok(store)
    }

    /// Close the store (flush all pending writes).
    pub fn close(self) -> Result<(), MetadataError> {
        self.db.flush().map_err(MetadataError::from)?;
        // DB is dropped when Arc reaches zero, which closes the database.
        drop(self);
        Ok(())
    }

    fn check_schema_version(&self) -> Result<(), MetadataError> {
        let cf = self
            .db
            .cf_handle(CF_DEFAULT)
            .ok_or_else(|| MetadataError::Internal("default column family missing".into()))?;

        match self.db.get_cf(&cf, SCHEMA_VERSION_KEY)? {
            Some(bytes) => {
                if bytes.len() < 4 {
                    return Err(MetadataError::Internal("corrupt schema_version".into()));
                }
                let found = u32::from_le_bytes([bytes[0], bytes[1], bytes[2], bytes[3]]);
                if found > CURRENT_SCHEMA_VERSION {
                    return Err(MetadataError::SchemaVersionTooNew {
                        found,
                        supported: CURRENT_SCHEMA_VERSION,
                    });
                }
                if found < CURRENT_SCHEMA_VERSION {
                    self.run_migrations(found)?;
                }
                Ok(())
            }
            None => {
                // New database — write current schema version.
                self.db.put_cf_opt(
                    &cf,
                    SCHEMA_VERSION_KEY,
                    CURRENT_SCHEMA_VERSION.to_le_bytes(),
                    &sync_write_opts(),
                )?;
                Ok(())
            }
        }
    }

    fn run_migrations(&self, _from: u32) -> Result<(), MetadataError> {
        // No migrations yet (version 1 is the first).
        // Future migrations go here.
        let cf = self
            .db
            .cf_handle(CF_DEFAULT)
            .ok_or_else(|| MetadataError::Internal("default column family missing".into()))?;
        self.db.put_cf_opt(
            &cf,
            SCHEMA_VERSION_KEY,
            CURRENT_SCHEMA_VERSION.to_le_bytes(),
            &sync_write_opts(),
        )?;
        Ok(())
    }

    // ---- Chunk operations ----

    pub fn put_chunk(&self, meta: &ChunkMeta) -> Result<(), MetadataError> {
        let cf = self.cf(CF_CHUNKS)?;
        let key = hex(&meta.id);
        let val = ser(meta)?;
        self.db
            .put_cf_opt(&cf, key.as_bytes(), &val, &sync_write_opts())?;
        Ok(())
    }

    /// Store chunk metadata with a caller-chosen durability barrier.
    ///
    /// The chunk row must never be MORE durable than the chunk bytes it
    /// describes. `put_chunk` always fsyncs; when the data was written
    /// with `SyncMode::None` (bytes still in page cache) that ordering
    /// inverts: after a crash the durable row points at a missing or torn
    /// file, and because the write path dedups on the presence of that row,
    /// every future write of the same content "succeeds" without ever
    /// rewriting the bytes — the object is unreadable forever while
    /// identical PUTs keep returning 200 (permanent dedup poisoning).
    ///
    /// Passing `SyncMode::None` here keeps the row exactly as durable as the
    /// data: a crash loses both together, so the next write re-creates both.
    pub fn put_chunk_sync(&self, meta: &ChunkMeta, sync: SyncMode) -> Result<(), MetadataError> {
        let cf = self.cf(CF_CHUNKS)?;
        let key = hex(&meta.id);
        let val = ser(meta)?;
        let opts = match sync {
            SyncMode::None => nosync_write_opts(),
            SyncMode::Full => sync_write_opts(),
        };
        self.db.put_cf_opt(&cf, key.as_bytes(), &val, &opts)?;
        Ok(())
    }

    pub fn get_chunk(&self, id: &[u8; 32]) -> Result<Option<ChunkMeta>, MetadataError> {
        let cf = self.cf(CF_CHUNKS)?;
        let key = hex(id);
        match self.db.get_cf(&cf, key.as_bytes())? {
            Some(bytes) => Ok(Some(de(&bytes)?)),
            None => Ok(None),
        }
    }

    pub fn delete_chunk(&self, id: &[u8; 32]) -> Result<(), MetadataError> {
        let cf = self.cf(CF_CHUNKS)?;
        let key = hex(id);
        self.db
            .delete_cf_opt(&cf, key.as_bytes(), &sync_write_opts())?;
        Ok(())
    }

    pub fn scan_chunks<F>(&self, mut f: F) -> Result<u64, MetadataError>
    where
        F: FnMut(ChunkMeta),
    {
        let cf = self.cf(CF_CHUNKS)?;
        let iter = self.db.iterator_cf(&cf, IteratorMode::Start);
        let mut count = 0u64;
        for item in iter {
            let (_key, value) = item?;
            let meta: ChunkMeta = de(&value)?;
            f(meta);
            count += 1;
        }
        Ok(count)
    }

    pub fn count_chunks(&self) -> Result<u64, MetadataError> {
        self.scan_chunks(|_| {})
    }

    // ---- Manifest operations ----

    pub fn put_manifest(&self, meta: &ManifestMeta) -> Result<(), MetadataError> {
        let cf = self.cf(CF_MANIFESTS)?;
        let val = ser(meta)?;
        self.db
            .put_cf_opt(&cf, meta.object_id.as_bytes(), &val, &sync_write_opts())?;
        Ok(())
    }

    pub fn get_manifest(&self, object_id: &str) -> Result<Option<ManifestMeta>, MetadataError> {
        let cf = self.cf(CF_MANIFESTS)?;
        match self.db.get_cf(&cf, object_id.as_bytes())? {
            Some(bytes) => Ok(Some(de(&bytes)?)),
            None => Ok(None),
        }
    }

    pub fn delete_manifest(&self, object_id: &str) -> Result<(), MetadataError> {
        let cf = self.cf(CF_MANIFESTS)?;
        self.db
            .delete_cf_opt(&cf, object_id.as_bytes(), &sync_write_opts())?;
        Ok(())
    }

    pub fn scan_manifests<F>(&self, mut f: F) -> Result<u64, MetadataError>
    where
        F: FnMut(ManifestMeta),
    {
        let cf = self.cf(CF_MANIFESTS)?;
        let iter = self.db.iterator_cf(&cf, IteratorMode::Start);
        let mut count = 0u64;
        for item in iter {
            let (_key, value) = item?;
            let meta: ManifestMeta = de(&value)?;
            f(meta);
            count += 1;
        }
        Ok(count)
    }

    /// List manifests with prefix, pagination. Returns (manifests, is_truncated).
    pub fn list_manifests(
        &self,
        prefix: &str,
        start_after: Option<&str>,
        max: u32,
    ) -> Result<(Vec<ManifestMeta>, bool), MetadataError> {
        let cf = self.cf(CF_MANIFESTS)?;

        // Determine the iteration start point.
        let mode = match start_after {
            Some(sa) => {
                // Start from the key *after* start_after. We use From mode on
                // start_after and skip the first entry if it matches exactly.
                IteratorMode::From(sa.as_bytes(), rocksdb::Direction::Forward)
            }
            None if !prefix.is_empty() => {
                IteratorMode::From(prefix.as_bytes(), rocksdb::Direction::Forward)
            }
            None => IteratorMode::Start,
        };

        let iter = self.db.iterator_cf(&cf, mode);
        let mut results = Vec::new();
        let mut is_truncated = false;

        for item in iter {
            let (key, value) = item?;
            let key_str = std::str::from_utf8(&key)
                .map_err(|e| MetadataError::Internal(format!("non-utf8 key: {e}")))?;

            // Skip the start_after key itself.
            if let Some(sa) = start_after {
                if key_str == sa {
                    continue;
                }
            }

            // Stop if key no longer matches prefix.
            if !prefix.is_empty() && !key_str.starts_with(prefix) {
                break;
            }

            if results.len() as u32 >= max {
                is_truncated = true;
                break;
            }

            let meta: ManifestMeta = de(&value)?;
            results.push(meta);
        }

        Ok((results, is_truncated))
    }

    // ---- Inline data ----

    pub fn put_inline(&self, object_id: &str, data: &[u8]) -> Result<(), MetadataError> {
        let cf = self.cf(CF_INLINE)?;
        self.db
            .put_cf_opt(&cf, object_id.as_bytes(), data, &sync_write_opts())?;
        Ok(())
    }

    /// Like `put_inline`, but skips the WAL fsync.
    ///
    /// Use on the hot WRITE path of NFS / S3 / NBD where data
    /// durability is provided by an explicit later barrier
    /// (NFS COMMIT, S3 PUT completion, NBD CMD_FLUSH). RocksDB still
    /// writes to its WAL, the OS just doesn't fsync per call —
    /// background flush + explicit barriers are what guarantee
    /// post-crash visibility. Caller is responsible for triggering
    /// that barrier; otherwise an inode size update is "soft" until
    /// then.
    pub fn put_inline_nosync(&self, object_id: &str, data: &[u8]) -> Result<(), MetadataError> {
        let cf = self.cf(CF_INLINE)?;
        self.db
            .put_cf_opt(&cf, object_id.as_bytes(), data, &nosync_write_opts())?;
        Ok(())
    }

    // ---- Batched writes ----

    /// Apply a `WriteBatch` to RocksDB as a single atomic write. The
    /// `sync` mode controls the durability barrier:
    ///   - `SyncMode::None`: WAL write goes through the OS page cache;
    ///     no fsync. Background flush / explicit later flush makes it
    ///     durable. Use this for the body of a hot-path COMPOUND
    ///     where a later flush op will sync.
    ///   - `SyncMode::Full`: fsync the WAL before returning. Use this
    ///     for explicit durability barriers (NFS COMMIT, S3 PUT
    ///     completion, S3 DELETE).
    pub fn commit_batch(&self, batch: WriteBatch, sync: SyncMode) -> Result<(), MetadataError> {
        if batch.is_empty() {
            return Ok(());
        }
        let mut db_batch = WriteBatchWithTransaction::<false>::default();
        for (cf_name, key, val) in batch.ops {
            let cf = self.cf(cf_name)?;
            match val {
                Some(v) => db_batch.put_cf(&cf, key, v),
                None => db_batch.delete_cf(&cf, key),
            }
        }
        let opts = match sync {
            SyncMode::None => nosync_write_opts(),
            SyncMode::Full => sync_write_opts(),
        };
        self.db.write_opt(db_batch, &opts)?;
        Ok(())
    }

    pub fn get_inline(&self, object_id: &str) -> Result<Option<Vec<u8>>, MetadataError> {
        let cf = self.cf(CF_INLINE)?;
        Ok(self.db.get_cf(&cf, object_id.as_bytes())?)
    }

    pub fn delete_inline(&self, object_id: &str) -> Result<(), MetadataError> {
        let cf = self.cf(CF_INLINE)?;
        self.db
            .delete_cf_opt(&cf, object_id.as_bytes(), &sync_write_opts())?;
        Ok(())
    }

    /// Scan all inline entries matching a key prefix.
    /// Returns (key, value) pairs sorted by key.
    pub fn scan_inline_prefix(
        &self,
        prefix: &str,
    ) -> Result<Vec<(String, Vec<u8>)>, MetadataError> {
        let cf = self.cf(CF_INLINE)?;
        let prefix_bytes = prefix.as_bytes();

        let mut results = Vec::new();
        let iter = self.db.iterator_cf(
            &cf,
            rocksdb::IteratorMode::From(prefix_bytes, rocksdb::Direction::Forward),
        );

        for item in iter {
            let (key, value) = item?;
            if !key.starts_with(prefix_bytes) {
                break;
            }
            let key_str = String::from_utf8(key.to_vec())
                .map_err(|e| MetadataError::Internal(format!("non-utf8 key: {e}")))?;
            results.push((key_str, value.to_vec()));
        }

        Ok(results)
    }

    /// Atomic batch write to the inline CF.
    /// All puts/deletes happen in a single RocksDB write batch.
    pub fn batch_inline(
        &self,
        puts: &[(&str, &[u8])],
        deletes: &[&str],
    ) -> Result<(), MetadataError> {
        let cf = self.cf(CF_INLINE)?;
        let mut batch = rocksdb::WriteBatchWithTransaction::<false>::default();
        for (key, value) in puts {
            batch.put_cf(&cf, key.as_bytes(), value);
        }
        for key in deletes {
            batch.delete_cf(&cf, key.as_bytes());
        }
        self.db.write_opt(batch, &sync_write_opts())?;
        Ok(())
    }

    // ---- Volume operations ----

    pub fn put_volume(&self, meta: &VolumeMeta) -> Result<(), MetadataError> {
        let cf = self.cf(CF_VOLUMES)?;
        let key = hex(&meta.volume_id);
        let val = ser(meta)?;
        self.db
            .put_cf_opt(&cf, key.as_bytes(), &val, &sync_write_opts())?;
        Ok(())
    }

    pub fn get_volume(&self, id: &[u8; 16]) -> Result<Option<VolumeMeta>, MetadataError> {
        let cf = self.cf(CF_VOLUMES)?;
        let key = hex(id);
        match self.db.get_cf(&cf, key.as_bytes())? {
            Some(bytes) => Ok(Some(de(&bytes)?)),
            None => Ok(None),
        }
    }

    pub fn delete_volume(&self, id: &[u8; 16]) -> Result<(), MetadataError> {
        let cf = self.cf(CF_VOLUMES)?;
        let key = hex(id);
        self.db
            .delete_cf_opt(&cf, key.as_bytes(), &sync_write_opts())?;
        Ok(())
    }

    pub fn scan_volumes<F>(&self, mut f: F) -> Result<u64, MetadataError>
    where
        F: FnMut(VolumeMeta),
    {
        let cf = self.cf(CF_VOLUMES)?;
        let iter = self.db.iterator_cf(&cf, IteratorMode::Start);
        let mut count = 0u64;
        for item in iter {
            let (_key, value) = item?;
            let meta: VolumeMeta = de(&value)?;
            f(meta);
            count += 1;
        }
        Ok(count)
    }

    // ---- Volume chunk mapping ----

    pub fn put_volume_chunk(
        &self,
        volume_id: &[u8; 16],
        offset: u64,
        chunk_id: &[u8; 32],
    ) -> Result<(), MetadataError> {
        let cf = self.cf(CF_VOLUME_CHUNKS)?;
        let key = volume_chunk_key(volume_id, offset);
        let val = hex(chunk_id);
        self.db
            .put_cf_opt(&cf, key.as_bytes(), val.as_bytes(), &sync_write_opts())?;
        Ok(())
    }

    pub fn get_volume_chunk(
        &self,
        volume_id: &[u8; 16],
        offset: u64,
    ) -> Result<Option<[u8; 32]>, MetadataError> {
        let cf = self.cf(CF_VOLUME_CHUNKS)?;
        let key = volume_chunk_key(volume_id, offset);
        match self.db.get_cf(&cf, key.as_bytes())? {
            Some(bytes) => {
                let hex_str = std::str::from_utf8(&bytes)
                    .map_err(|e| MetadataError::Internal(format!("non-utf8 value: {e}")))?;
                let raw = from_hex(hex_str)?;
                let mut arr = [0u8; 32];
                if raw.len() != 32 {
                    return Err(MetadataError::Internal("chunk_id not 32 bytes".into()));
                }
                arr.copy_from_slice(&raw);
                Ok(Some(arr))
            }
            None => Ok(None),
        }
    }

    pub fn delete_volume_chunk(
        &self,
        volume_id: &[u8; 16],
        offset: u64,
    ) -> Result<(), MetadataError> {
        let cf = self.cf(CF_VOLUME_CHUNKS)?;
        let key = volume_chunk_key(volume_id, offset);
        self.db
            .delete_cf_opt(&cf, key.as_bytes(), &sync_write_opts())?;
        Ok(())
    }

    pub fn scan_volume_chunks<F>(
        &self,
        volume_id: &[u8; 16],
        mut f: F,
    ) -> Result<u64, MetadataError>
    where
        F: FnMut(u64, [u8; 32]),
    {
        let cf = self.cf(CF_VOLUME_CHUNKS)?;
        let prefix = format!("{}/", hex(volume_id));
        let iter = self.db.iterator_cf(
            &cf,
            IteratorMode::From(prefix.as_bytes(), rocksdb::Direction::Forward),
        );
        let mut count = 0u64;

        for item in iter {
            let (key, value) = item?;
            let key_str = std::str::from_utf8(&key)
                .map_err(|e| MetadataError::Internal(format!("non-utf8 key: {e}")))?;
            if !key_str.starts_with(&prefix) {
                break;
            }
            let offset_str = &key_str[prefix.len()..];
            let offset: u64 = offset_str
                .parse()
                .map_err(|e| MetadataError::Internal(format!("bad offset: {e}")))?;

            let hex_str = std::str::from_utf8(&value)
                .map_err(|e| MetadataError::Internal(format!("non-utf8 value: {e}")))?;
            let raw = from_hex(hex_str)?;
            let mut chunk_id = [0u8; 32];
            if raw.len() != 32 {
                return Err(MetadataError::Internal("chunk_id not 32 bytes".into()));
            }
            chunk_id.copy_from_slice(&raw);

            f(offset, chunk_id);
            count += 1;
        }

        Ok(count)
    }

    // ---- Bucket metadata ----

    pub fn put_bucket(&self, meta: &BucketMeta) -> Result<(), MetadataError> {
        let cf = self.cf(CF_BUCKETS)?;
        let val = ser(meta)?;
        self.db
            .put_cf_opt(&cf, meta.name.as_bytes(), &val, &sync_write_opts())?;
        Ok(())
    }

    pub fn get_bucket(&self, name: &str) -> Result<Option<BucketMeta>, MetadataError> {
        let cf = self.cf(CF_BUCKETS)?;
        match self.db.get_cf(&cf, name.as_bytes())? {
            Some(bytes) => Ok(Some(de(&bytes)?)),
            None => Ok(None),
        }
    }

    pub fn delete_bucket(&self, name: &str) -> Result<(), MetadataError> {
        let cf = self.cf(CF_BUCKETS)?;
        self.db
            .delete_cf_opt(&cf, name.as_bytes(), &sync_write_opts())?;
        Ok(())
    }

    pub fn list_buckets(&self) -> Result<Vec<BucketMeta>, MetadataError> {
        let cf = self.cf(CF_BUCKETS)?;
        let mut buckets = Vec::new();
        let iter = self.db.iterator_cf(&cf, IteratorMode::Start);
        for item in iter {
            let (_, value) = item?;
            buckets.push(de(&value)?);
        }
        Ok(buckets)
    }

    // ---- Multipart upload metadata ----

    pub fn put_multipart_upload(&self, meta: &MultipartUploadMeta) -> Result<(), MetadataError> {
        let cf = self.cf(CF_MULTIPART_UPLOADS)?;
        let val = ser(meta)?;
        self.db
            .put_cf_opt(&cf, meta.upload_id.as_bytes(), &val, &sync_write_opts())?;
        Ok(())
    }

    pub fn get_multipart_upload(
        &self,
        upload_id: &str,
    ) -> Result<Option<MultipartUploadMeta>, MetadataError> {
        let cf = self.cf(CF_MULTIPART_UPLOADS)?;
        match self.db.get_cf(&cf, upload_id.as_bytes())? {
            Some(bytes) => Ok(Some(de(&bytes)?)),
            None => Ok(None),
        }
    }

    pub fn delete_multipart_upload(&self, upload_id: &str) -> Result<(), MetadataError> {
        let cf = self.cf(CF_MULTIPART_UPLOADS)?;
        self.db
            .delete_cf_opt(&cf, upload_id.as_bytes(), &sync_write_opts())?;
        Ok(())
    }

    pub fn list_multipart_uploads(
        &self,
        bucket: &str,
    ) -> Result<Vec<MultipartUploadMeta>, MetadataError> {
        let cf = self.cf(CF_MULTIPART_UPLOADS)?;
        let mut uploads = Vec::new();
        let iter = self.db.iterator_cf(&cf, IteratorMode::Start);
        for item in iter {
            let (_, value) = item?;
            let meta: MultipartUploadMeta = de(&value)?;
            if meta.bucket == bucket {
                uploads.push(meta);
            }
        }
        Ok(uploads)
    }

    // ---- Multipart part metadata ----

    /// Key format: "{upload_id}/{part_number:05}"
    fn part_key(upload_id: &str, part_number: u32) -> String {
        format!("{upload_id}/{part_number:05}")
    }

    pub fn put_part(&self, upload_id: &str, part: &PartMeta) -> Result<(), MetadataError> {
        let cf = self.cf(CF_MULTIPART_PARTS)?;
        let key = Self::part_key(upload_id, part.part_number);
        let val = ser(part)?;
        self.db
            .put_cf_opt(&cf, key.as_bytes(), &val, &sync_write_opts())?;
        Ok(())
    }

    pub fn get_part(
        &self,
        upload_id: &str,
        part_number: u32,
    ) -> Result<Option<PartMeta>, MetadataError> {
        let cf = self.cf(CF_MULTIPART_PARTS)?;
        let key = Self::part_key(upload_id, part_number);
        match self.db.get_cf(&cf, key.as_bytes())? {
            Some(bytes) => Ok(Some(de(&bytes)?)),
            None => Ok(None),
        }
    }

    pub fn delete_part(&self, upload_id: &str, part_number: u32) -> Result<(), MetadataError> {
        let cf = self.cf(CF_MULTIPART_PARTS)?;
        let key = Self::part_key(upload_id, part_number);
        self.db
            .delete_cf_opt(&cf, key.as_bytes(), &sync_write_opts())?;
        Ok(())
    }

    /// List all parts for an upload, sorted by part number.
    pub fn list_parts(&self, upload_id: &str) -> Result<Vec<PartMeta>, MetadataError> {
        let cf = self.cf(CF_MULTIPART_PARTS)?;
        let prefix = format!("{upload_id}/");
        let mut parts = Vec::new();
        let iter = self.db.iterator_cf(
            &cf,
            IteratorMode::From(prefix.as_bytes(), rocksdb::Direction::Forward),
        );
        for item in iter {
            let (key, value) = item?;
            let key_str = std::str::from_utf8(&key)
                .map_err(|e| MetadataError::Internal(format!("non-utf8 key: {e}")))?;
            if !key_str.starts_with(&prefix) {
                break;
            }
            parts.push(de(&value)?);
        }
        parts.sort_by_key(|p: &PartMeta| p.part_number);
        Ok(parts)
    }

    /// Delete all parts for an upload.
    pub fn delete_all_parts(&self, upload_id: &str) -> Result<(), MetadataError> {
        let cf = self.cf(CF_MULTIPART_PARTS)?;
        let prefix = format!("{upload_id}/");
        let mut batch = WriteBatchWithTransaction::<false>::default();
        let iter = self.db.iterator_cf(
            &cf,
            IteratorMode::From(prefix.as_bytes(), rocksdb::Direction::Forward),
        );
        for item in iter {
            let (key, _) = item?;
            let key_str = std::str::from_utf8(&key)
                .map_err(|e| MetadataError::Internal(format!("non-utf8 key: {e}")))?;
            if !key_str.starts_with(&prefix) {
                break;
            }
            batch.delete_cf(&cf, &key);
        }
        self.db.write_opt(batch, &sync_write_opts())?;
        Ok(())
    }

    // ---- Object versioning ----

    /// Generate a version ID. Uses reverse timestamp so RocksDB iteration
    /// returns newest versions first (lexicographic order = newest first).
    pub fn generate_version_id() -> String {
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos() as u64;
        // Reverse: larger timestamps produce lexicographically smaller IDs.
        format!("{:020}", u64::MAX - nanos)
    }

    /// Key format for object versions: "{bucket}/{key}\0{version_id}"
    fn version_key(bucket: &str, key: &str, version_id: &str) -> Vec<u8> {
        format!("{bucket}/{key}\0{version_id}").into_bytes()
    }

    /// Prefix for all versions of a specific object: "{bucket}/{key}\0"
    fn version_prefix(bucket: &str, key: &str) -> Vec<u8> {
        format!("{bucket}/{key}\0").into_bytes()
    }

    fn rewrite_latest_flags(
        &self,
        cf: &Arc<rocksdb::BoundColumnFamily>,
        bucket: &str,
        key: &str,
        latest_version_id: Option<&str>,
        delete_version_id: Option<&str>,
    ) -> Result<(), MetadataError> {
        let prefix = Self::version_prefix(bucket, key);
        let iter = self
            .db
            .iterator_cf(cf, IteratorMode::From(&prefix, rocksdb::Direction::Forward));

        let mut first_remaining = latest_version_id.is_none();
        let mut batch = WriteBatchWithTransaction::<false>::default();

        for item in iter {
            let (k, v) = item?;
            if !k.starts_with(&prefix) {
                break;
            }

            let mut version: ObjectVersion = de(&v)?;
            if delete_version_id == Some(version.version_id.as_str()) {
                continue;
            }

            let should_be_latest = latest_version_id
                .map(|id| id == version.version_id.as_str())
                .unwrap_or_else(|| {
                    if first_remaining {
                        first_remaining = false;
                        true
                    } else {
                        false
                    }
                });

            if version.is_latest != should_be_latest {
                version.is_latest = should_be_latest;
                batch.put_cf(cf, &k, ser(&version)?);
            }
        }

        if !batch.is_empty() {
            self.db.write_opt(batch, &sync_write_opts())?;
        }

        Ok(())
    }

    /// Store a new version of an object.
    pub fn put_object_version(&self, version: &ObjectVersion) -> Result<(), MetadataError> {
        let cf = self.cf(CF_OBJECT_VERSIONS)?;
        if version.is_latest {
            self.rewrite_latest_flags(
                &cf,
                &version.bucket,
                &version.key,
                Some(&version.version_id),
                None,
            )?;
        }
        let key = Self::version_key(&version.bucket, &version.key, &version.version_id);
        let val = ser(version)?;
        self.db.put_cf_opt(&cf, &key, &val, &sync_write_opts())?;
        Ok(())
    }

    /// Get a specific version of an object.
    pub fn get_object_version(
        &self,
        bucket: &str,
        key: &str,
        version_id: &str,
    ) -> Result<Option<ObjectVersion>, MetadataError> {
        let cf = self.cf(CF_OBJECT_VERSIONS)?;
        let k = Self::version_key(bucket, key, version_id);
        match self.db.get_cf(&cf, &k)? {
            Some(bytes) => Ok(Some(de(&bytes)?)),
            None => Ok(None),
        }
    }

    /// Get the latest version of an object (first in reverse-timestamp order).
    /// Returns None if no versions exist.
    pub fn get_latest_version(
        &self,
        bucket: &str,
        key: &str,
    ) -> Result<Option<ObjectVersion>, MetadataError> {
        let cf = self.cf(CF_OBJECT_VERSIONS)?;
        let prefix = Self::version_prefix(bucket, key);
        let iter = self.db.iterator_cf(
            &cf,
            IteratorMode::From(&prefix, rocksdb::Direction::Forward),
        );

        let mut first_version = None;
        for item in iter {
            let (k, v) = item?;
            // Check we're still within the same object's versions.
            if !k.starts_with(&prefix) {
                break;
            }
            let version: ObjectVersion = de(&v)?;
            if first_version.is_none() {
                first_version = Some(version.clone());
            }
            if version.is_latest {
                return Ok(Some(version));
            }
        }

        Ok(first_version)
    }

    /// List all versions of an object, newest first.
    pub fn list_object_versions(
        &self,
        bucket: &str,
        key: &str,
    ) -> Result<Vec<ObjectVersion>, MetadataError> {
        let cf = self.cf(CF_OBJECT_VERSIONS)?;
        let prefix = Self::version_prefix(bucket, key);
        let iter = self.db.iterator_cf(
            &cf,
            IteratorMode::From(&prefix, rocksdb::Direction::Forward),
        );

        let mut versions: Vec<ObjectVersion> = Vec::new();
        for item in iter {
            let (k, v) = item?;
            if !k.starts_with(&prefix) {
                break;
            }
            versions.push(de(&v)?);
        }

        versions.sort_by(|a, b| {
            b.is_latest
                .cmp(&a.is_latest)
                .then_with(|| b.last_modified.cmp(&a.last_modified))
                .then_with(|| a.version_id.cmp(&b.version_id))
        });

        Ok(versions)
    }

    /// List all versions across all keys in a bucket, newest first.
    /// Used for ListObjectVersions S3 API.
    pub fn list_bucket_versions(
        &self,
        bucket: &str,
        max_keys: u32,
    ) -> Result<Vec<ObjectVersion>, MetadataError> {
        let cf = self.cf(CF_OBJECT_VERSIONS)?;
        let prefix = format!("{bucket}/");
        let iter = self.db.iterator_cf(
            &cf,
            IteratorMode::From(prefix.as_bytes(), rocksdb::Direction::Forward),
        );

        let mut versions: Vec<ObjectVersion> = Vec::new();
        for item in iter {
            let (k, v) = item?;
            let key_str = std::str::from_utf8(&k)
                .map_err(|e| MetadataError::Internal(format!("non-utf8 key: {e}")))?;
            if !key_str.starts_with(&prefix) {
                break;
            }
            if versions.len() >= max_keys as usize {
                break;
            }
            versions.push(de(&v)?);
        }

        versions.sort_by(|a, b| {
            a.key
                .cmp(&b.key)
                .then_with(|| b.is_latest.cmp(&a.is_latest))
                .then_with(|| b.last_modified.cmp(&a.last_modified))
                .then_with(|| a.version_id.cmp(&b.version_id))
        });

        Ok(versions)
    }

    /// Delete a specific version permanently.
    pub fn delete_object_version(
        &self,
        bucket: &str,
        key: &str,
        version_id: &str,
    ) -> Result<(), MetadataError> {
        let cf = self.cf(CF_OBJECT_VERSIONS)?;
        let k = Self::version_key(bucket, key, version_id);
        self.db.delete_cf_opt(&cf, &k, &sync_write_opts())?;
        self.rewrite_latest_flags(&cf, bucket, key, None, Some(version_id))?;
        Ok(())
    }

    /// Delete all versions of an object (used when deleting from unversioned bucket).
    pub fn delete_all_versions(&self, bucket: &str, key: &str) -> Result<u64, MetadataError> {
        let cf = self.cf(CF_OBJECT_VERSIONS)?;
        let prefix = Self::version_prefix(bucket, key);
        let iter = self.db.iterator_cf(
            &cf,
            IteratorMode::From(&prefix, rocksdb::Direction::Forward),
        );

        let mut batch = WriteBatchWithTransaction::<false>::default();
        let mut count = 0u64;
        for item in iter {
            let (k, _) = item?;
            if !k.starts_with(&prefix) {
                break;
            }
            batch.delete_cf(&cf, &k);
            count += 1;
        }

        if count > 0 {
            self.db.write_opt(batch, &sync_write_opts())?;
        }
        Ok(count)
    }

    // ---- Batch operations ----

    pub fn write_batch(&self, ops: Vec<BatchOp>) -> Result<(), MetadataError> {
        let mut batch = WriteBatchWithTransaction::<false>::default();

        for op in &ops {
            match op {
                BatchOp::PutChunk(meta) => {
                    let cf = self.cf(CF_CHUNKS)?;
                    let key = hex(&meta.id);
                    let val = ser(meta)?;
                    batch.put_cf(&cf, key.as_bytes(), &val);
                }
                BatchOp::DeleteChunk(id) => {
                    let cf = self.cf(CF_CHUNKS)?;
                    let key = hex(id);
                    batch.delete_cf(&cf, key.as_bytes());
                }
                BatchOp::PutManifest(meta) => {
                    let cf = self.cf(CF_MANIFESTS)?;
                    let val = ser(meta)?;
                    batch.put_cf(&cf, meta.object_id.as_bytes(), &val);
                }
                BatchOp::DeleteManifest(id) => {
                    let cf = self.cf(CF_MANIFESTS)?;
                    batch.delete_cf(&cf, id.as_bytes());
                }
                BatchOp::PutVolume(meta) => {
                    let cf = self.cf(CF_VOLUMES)?;
                    let key = hex(&meta.volume_id);
                    let val = ser(meta)?;
                    batch.put_cf(&cf, key.as_bytes(), &val);
                }
                BatchOp::DeleteVolume(id) => {
                    let cf = self.cf(CF_VOLUMES)?;
                    let key = hex(id);
                    batch.delete_cf(&cf, key.as_bytes());
                }
                BatchOp::PutInline { object_id, data } => {
                    let cf = self.cf(CF_INLINE)?;
                    batch.put_cf(&cf, object_id.as_bytes(), data);
                }
                BatchOp::DeleteInline(id) => {
                    let cf = self.cf(CF_INLINE)?;
                    batch.delete_cf(&cf, id.as_bytes());
                }
                BatchOp::PutBucket(meta) => {
                    let cf = self.cf(CF_BUCKETS)?;
                    let val = ser(meta)?;
                    batch.put_cf(&cf, meta.name.as_bytes(), &val);
                }
                BatchOp::DeleteBucket(name) => {
                    let cf = self.cf(CF_BUCKETS)?;
                    batch.delete_cf(&cf, name.as_bytes());
                }
                BatchOp::PutMultipartUpload(meta) => {
                    let cf = self.cf(CF_MULTIPART_UPLOADS)?;
                    let val = ser(meta)?;
                    batch.put_cf(&cf, meta.upload_id.as_bytes(), &val);
                }
                BatchOp::DeleteMultipartUpload(id) => {
                    let cf = self.cf(CF_MULTIPART_UPLOADS)?;
                    batch.delete_cf(&cf, id.as_bytes());
                }
                BatchOp::PutPart { upload_id, part } => {
                    let cf = self.cf(CF_MULTIPART_PARTS)?;
                    let key = Self::part_key(upload_id, part.part_number);
                    let val = ser(part)?;
                    batch.put_cf(&cf, key.as_bytes(), &val);
                }
                BatchOp::DeletePart {
                    upload_id,
                    part_number,
                } => {
                    let cf = self.cf(CF_MULTIPART_PARTS)?;
                    let key = Self::part_key(upload_id, *part_number);
                    batch.delete_cf(&cf, key.as_bytes());
                }
            }
        }

        self.db.write_opt(batch, &sync_write_opts())?;
        Ok(())
    }

    // ---- Internal helpers ----

    /// Make writes issued without per-operation WAL sync durable.
    pub fn flush(&self) -> Result<(), MetadataError> {
        self.db.flush_wal(true).map_err(Into::into)
    }

    fn cf(&self, name: &str) -> Result<Arc<rocksdb::BoundColumnFamily<'_>>, MetadataError> {
        self.db
            .cf_handle(name)
            .ok_or_else(|| MetadataError::Internal(format!("column family {name} not found")))
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn tmp_store() -> (TempDir, MetadataStore) {
        let dir = TempDir::new().unwrap();
        let store = MetadataStore::open(dir.path()).unwrap();
        (dir, store)
    }

    fn make_chunk(i: u8) -> ChunkMeta {
        let mut id = [0u8; 32];
        id[0] = i;
        ChunkMeta {
            id,
            size: 4 * 1024 * 1024,
            checksum: 0xDEAD_BEEF,
            ref_count: 1,
            sealed: true,
            corrupt: false,
            corrupt_reason: None,
            created_at: 1000 + i as i64,
            last_accessed: 2000 + i as i64,
        }
    }

    // A chunk row written with SyncMode::None must still be readable
    // (the nosync path is a durability barrier choice, not a correctness
    // change), and SyncMode::Full must behave identically to put_chunk.
    #[test]
    fn put_chunk_sync_roundtrips_in_both_modes() {
        let (_dir, store) = tmp_store();

        let nosync = make_chunk(1);
        store.put_chunk_sync(&nosync, SyncMode::None).unwrap();
        let got = store
            .get_chunk(&nosync.id)
            .unwrap()
            .expect("nosync chunk readable");
        assert_eq!(got, nosync);

        let synced = make_chunk(2);
        store.put_chunk_sync(&synced, SyncMode::Full).unwrap();
        let got = store
            .get_chunk(&synced.id)
            .unwrap()
            .expect("synced chunk readable");
        assert_eq!(got, synced);

        // Matches the always-fsync sibling for the same input.
        let via_put_chunk = make_chunk(3);
        store.put_chunk(&via_put_chunk).unwrap();
        assert_eq!(
            store.get_chunk(&via_put_chunk.id).unwrap().unwrap(),
            via_put_chunk
        );
    }

    fn make_manifest(id: &str) -> ManifestMeta {
        ManifestMeta {
            object_id: id.to_string(),
            size: 1024,
            chunks: vec![],
            etag: "\"abc123\"".to_string(),
            content_type: "application/octet-stream".to_string(),
            created_at: 1000,
            modified_at: 2000,
            inline_data: None,
        }
    }

    fn make_volume(i: u8) -> VolumeMeta {
        let mut id = [0u8; 16];
        id[0] = i;
        VolumeMeta {
            volume_id: id,
            name: format!("vol-{i}"),
            size: 1024 * 1024 * 1024,
            chunk_size: 4 * 1024 * 1024,
            access_mode: 0,
            consistency: 0,
            replication: 1,
            state: 1,
            active_writer: None,
            append_offset: 0,
            created_at: 1000,
        }
    }

    // 1. Open/close roundtrip
    #[test]
    fn open_close_reopen() {
        let dir = TempDir::new().unwrap();
        {
            let store = MetadataStore::open(dir.path()).unwrap();
            assert_eq!(store.count_chunks().unwrap(), 0);
            store.close().unwrap();
        }
        {
            let store = MetadataStore::open(dir.path()).unwrap();
            assert_eq!(store.count_chunks().unwrap(), 0);
            store.close().unwrap();
        }
    }

    // 2. Schema versioning
    #[test]
    fn schema_version_set_on_new_db() {
        let dir = TempDir::new().unwrap();
        {
            let store = MetadataStore::open(dir.path()).unwrap();
            // Verify version was set by checking we can reopen without error
            drop(store);
        }
        {
            // Reopen — should succeed with matching schema version
            let store = MetadataStore::open(dir.path()).unwrap();
            drop(store);
        }
    }

    #[test]
    fn schema_version_too_new_rejected() {
        let dir = TempDir::new().unwrap();
        {
            let store = MetadataStore::open(dir.path()).unwrap();
            // Write a future version by accessing db directly
            {
                let cf = store.db.cf_handle(CF_DEFAULT).unwrap();
                store
                    .db
                    .put_cf(&cf, SCHEMA_VERSION_KEY, 999u32.to_le_bytes())
                    .unwrap();
            } // cf dropped here, releasing borrow
            store.close().unwrap();
        }
        let err = MetadataStore::open(dir.path()).unwrap_err();
        match err {
            MetadataError::SchemaVersionTooNew { found, supported } => {
                assert_eq!(found, 999);
                assert_eq!(supported, CURRENT_SCHEMA_VERSION);
            }
            other => panic!("expected SchemaVersionTooNew, got: {other}"),
        }
    }

    // 3. Chunk CRUD
    #[test]
    fn chunk_crud() {
        let (_dir, store) = tmp_store();
        let chunk = make_chunk(1);

        // Put and get.
        store.put_chunk(&chunk).unwrap();
        let got = store.get_chunk(&chunk.id).unwrap().unwrap();
        assert_eq!(got, chunk);

        // Delete.
        store.delete_chunk(&chunk.id).unwrap();
        assert!(store.get_chunk(&chunk.id).unwrap().is_none());
    }

    // 4. Chunk scan
    #[test]
    fn chunk_scan_100() {
        let (_dir, store) = tmp_store();
        for i in 0..100u8 {
            store.put_chunk(&make_chunk(i)).unwrap();
        }
        let mut collected = Vec::new();
        let count = store.scan_chunks(|c| collected.push(c)).unwrap();
        assert_eq!(count, 100);
        assert_eq!(collected.len(), 100);
    }

    // 5. Manifest CRUD
    #[test]
    fn manifest_crud() {
        let (_dir, store) = tmp_store();
        let m = make_manifest("bucket/photos/cat.jpg");

        store.put_manifest(&m).unwrap();
        let got = store
            .get_manifest("bucket/photos/cat.jpg")
            .unwrap()
            .unwrap();
        assert_eq!(got, m);

        store.delete_manifest("bucket/photos/cat.jpg").unwrap();
        assert!(store
            .get_manifest("bucket/photos/cat.jpg")
            .unwrap()
            .is_none());
    }

    // 6. Manifest listing with prefix
    #[test]
    fn manifest_list_prefix() {
        let (_dir, store) = tmp_store();
        for i in 0..5 {
            store
                .put_manifest(&make_manifest(&format!("photos/img{i}.jpg")))
                .unwrap();
        }
        for i in 0..5 {
            store
                .put_manifest(&make_manifest(&format!("videos/clip{i}.mp4")))
                .unwrap();
        }

        let (results, trunc) = store.list_manifests("photos/", None, 100).unwrap();
        assert_eq!(results.len(), 5);
        assert!(!trunc);

        let (results, _) = store.list_manifests("videos/", None, 100).unwrap();
        assert_eq!(results.len(), 5);
    }

    // 7. Inline data
    #[test]
    fn inline_data() {
        let (_dir, store) = tmp_store();
        let data = b"hello, small object!";

        store.put_inline("tiny.txt", data).unwrap();
        let got = store.get_inline("tiny.txt").unwrap().unwrap();
        assert_eq!(got, data);

        store.delete_inline("tiny.txt").unwrap();
        assert!(store.get_inline("tiny.txt").unwrap().is_none());
    }

    #[test]
    fn scan_inline_prefix() {
        let (_dir, store) = tmp_store();

        store.put_inline("iam/user/alice", b"user-alice").unwrap();
        store.put_inline("iam/user/bob", b"user-bob").unwrap();
        store.put_inline("iam/group/team-a", b"group-a").unwrap();
        store.put_inline("other/key", b"other").unwrap();

        let users = store.scan_inline_prefix("iam/user/").unwrap();
        assert_eq!(users.len(), 2);
        assert_eq!(users[0].0, "iam/user/alice");
        assert_eq!(users[1].0, "iam/user/bob");

        let groups = store.scan_inline_prefix("iam/group/").unwrap();
        assert_eq!(groups.len(), 1);

        let all_iam = store.scan_inline_prefix("iam/").unwrap();
        assert_eq!(all_iam.len(), 3);

        let empty = store.scan_inline_prefix("nonexistent/").unwrap();
        assert!(empty.is_empty());
    }

    // 8. Volume CRUD
    #[test]
    fn volume_crud() {
        let (_dir, store) = tmp_store();
        let v = make_volume(1);

        store.put_volume(&v).unwrap();
        let got = store.get_volume(&v.volume_id).unwrap().unwrap();
        assert_eq!(got, v);

        store.delete_volume(&v.volume_id).unwrap();
        assert!(store.get_volume(&v.volume_id).unwrap().is_none());
    }

    // 9. Volume chunk mapping
    #[test]
    fn volume_chunk_mapping() {
        let (_dir, store) = tmp_store();
        let vol_id = [1u8; 16];
        let chunk_a = [0xAAu8; 32];
        let chunk_b = [0xBBu8; 32];

        store.put_volume_chunk(&vol_id, 0, &chunk_a).unwrap();
        store.put_volume_chunk(&vol_id, 4194304, &chunk_b).unwrap();

        assert_eq!(
            store.get_volume_chunk(&vol_id, 0).unwrap().unwrap(),
            chunk_a
        );
        assert_eq!(
            store.get_volume_chunk(&vol_id, 4194304).unwrap().unwrap(),
            chunk_b
        );

        let mut entries = Vec::new();
        let count = store
            .scan_volume_chunks(&vol_id, |off, cid| entries.push((off, cid)))
            .unwrap();
        assert_eq!(count, 2);
        assert_eq!(entries[0].0, 0);
        assert_eq!(entries[1].0, 4194304);

        store.delete_volume_chunk(&vol_id, 0).unwrap();
        assert!(store.get_volume_chunk(&vol_id, 0).unwrap().is_none());
    }

    // 10. Batch operations
    #[test]
    fn batch_operations() {
        let (_dir, store) = tmp_store();

        let chunks: Vec<ChunkMeta> = (0..5).map(make_chunk).collect();
        let manifests: Vec<ManifestMeta> =
            (0..2).map(|i| make_manifest(&format!("obj{i}"))).collect();

        let mut ops: Vec<BatchOp> = chunks
            .iter()
            .map(|c| BatchOp::PutChunk(c.clone()))
            .collect();
        ops.extend(manifests.iter().map(|m| BatchOp::PutManifest(m.clone())));

        store.write_batch(ops).unwrap();

        for c in &chunks {
            assert!(store.get_chunk(&c.id).unwrap().is_some());
        }
        for m in &manifests {
            assert!(store.get_manifest(&m.object_id).unwrap().is_some());
        }
    }

    // 11. Concurrent reads
    #[test]
    fn concurrent_reads() {
        let (_dir, store) = tmp_store();
        let chunk = make_chunk(42);
        store.put_chunk(&chunk).unwrap();

        let db = store.db.clone();
        let mut handles = Vec::new();
        for _ in 0..10 {
            let db = db.clone();
            let id = chunk.id;
            handles.push(std::thread::spawn(move || {
                let cf = db.cf_handle(CF_CHUNKS).unwrap();
                let key = hex(&id);
                let val = db.get_cf(&cf, key.as_bytes()).unwrap().unwrap();
                let meta: ChunkMeta = de(&val).unwrap();
                assert_eq!(meta.id, id);
            }));
        }
        for h in handles {
            h.join().unwrap();
        }
    }

    // 12. Large values
    #[test]
    fn large_manifest_roundtrip() {
        let (_dir, store) = tmp_store();
        let chunks: Vec<ChunkRef> = (0..1000)
            .map(|i| ChunkRef {
                chunk_id: [i as u8; 32],
                length: 4 * 1024 * 1024,
                checksum: 0xCAFE,
                logical_start: i * 4 * 1024 * 1024,
                logical_end: (i + 1) * 4 * 1024 * 1024,
                nodes: Vec::new(),
            })
            .collect();
        let m = ManifestMeta {
            object_id: "big-object".to_string(),
            size: 1000 * 4 * 1024 * 1024,
            chunks,
            etag: "\"big\"".to_string(),
            content_type: "application/octet-stream".to_string(),
            created_at: 1000,
            modified_at: 2000,
            inline_data: None,
        };

        store.put_manifest(&m).unwrap();
        let got = store.get_manifest("big-object").unwrap().unwrap();
        assert_eq!(got.chunks.len(), 1000);
        assert_eq!(got, m);
    }

    // 13. Empty prefix list returns all
    #[test]
    fn list_empty_prefix() {
        let (_dir, store) = tmp_store();
        for i in 0..5 {
            store
                .put_manifest(&make_manifest(&format!("obj{i}")))
                .unwrap();
        }
        let (results, trunc) = store.list_manifests("", None, 100).unwrap();
        assert_eq!(results.len(), 5);
        assert!(!trunc);
    }

    // 14. Pagination
    #[test]
    fn pagination() {
        let (_dir, store) = tmp_store();
        for i in 0..10 {
            store
                .put_manifest(&make_manifest(&format!("obj{i:02}")))
                .unwrap();
        }

        // First page.
        let (page1, trunc1) = store.list_manifests("", None, 3).unwrap();
        assert_eq!(page1.len(), 3);
        assert!(trunc1);

        // Second page using start_after.
        let last_key = &page1.last().unwrap().object_id;
        let (page2, trunc2) = store.list_manifests("", Some(last_key), 3).unwrap();
        assert_eq!(page2.len(), 3);
        assert!(trunc2);

        // Ensure no overlap.
        assert_ne!(page1.last().unwrap().object_id, page2[0].object_id);

        // Collect all via pagination.
        let mut all = Vec::new();
        let mut start = None;
        loop {
            let (page, trunc) = store.list_manifests("", start.as_deref(), 4).unwrap();
            if page.is_empty() {
                break;
            }
            start = Some(page.last().unwrap().object_id.clone());
            all.extend(page);
            if !trunc {
                break;
            }
        }
        assert_eq!(all.len(), 10);
    }

    // 15. Volume scan
    #[test]
    fn volume_scan() {
        let (_dir, store) = tmp_store();
        for i in 0..5 {
            store.put_volume(&make_volume(i)).unwrap();
        }
        let mut collected = Vec::new();
        let count = store.scan_volumes(|v| collected.push(v)).unwrap();
        assert_eq!(count, 5);
        assert_eq!(collected.len(), 5);
    }

    // ---- Bucket tests ----

    fn make_bucket(name: &str) -> BucketMeta {
        BucketMeta {
            name: name.to_string(),
            owner: "testuser".to_string(),
            created_at: 1700000000,
            region: "us-east-1".to_string(),
            versioning: 0,
        }
    }

    #[test]
    fn bucket_crud() {
        let (_dir, store) = tmp_store();

        // Create
        store.put_bucket(&make_bucket("test-bucket")).unwrap();

        // Read
        let bucket = store
            .get_bucket("test-bucket")
            .unwrap()
            .expect("bucket exists");
        assert_eq!(bucket.name, "test-bucket");
        assert_eq!(bucket.owner, "testuser");
        assert_eq!(bucket.region, "us-east-1");

        // Delete
        store.delete_bucket("test-bucket").unwrap();
        assert!(store.get_bucket("test-bucket").unwrap().is_none());
    }

    #[test]
    fn bucket_list() {
        let (_dir, store) = tmp_store();

        store.put_bucket(&make_bucket("alpha")).unwrap();
        store.put_bucket(&make_bucket("bravo")).unwrap();
        store.put_bucket(&make_bucket("charlie")).unwrap();

        let buckets = store.list_buckets().unwrap();
        assert_eq!(buckets.len(), 3);
        // RocksDB iterates in sorted order
        assert_eq!(buckets[0].name, "alpha");
        assert_eq!(buckets[1].name, "bravo");
        assert_eq!(buckets[2].name, "charlie");
    }

    #[test]
    fn bucket_not_found() {
        let (_dir, store) = tmp_store();
        assert!(store.get_bucket("nonexistent").unwrap().is_none());
    }

    #[test]
    fn bucket_persists_across_restart() {
        let dir = TempDir::new().unwrap();
        {
            let store = MetadataStore::open(dir.path()).unwrap();
            store.put_bucket(&make_bucket("durable-bucket")).unwrap();
            drop(store);
        }
        {
            let store = MetadataStore::open(dir.path()).unwrap();
            let bucket = store
                .get_bucket("durable-bucket")
                .unwrap()
                .expect("survived restart");
            assert_eq!(bucket.name, "durable-bucket");
        }
    }

    // ---- Multipart upload tests ----

    fn make_upload(upload_id: &str, bucket: &str, key: &str) -> MultipartUploadMeta {
        MultipartUploadMeta {
            upload_id: upload_id.to_string(),
            bucket: bucket.to_string(),
            key: key.to_string(),
            content_type: "application/octet-stream".to_string(),
            created_at: 1700000000,
            owner: "testuser".to_string(),
        }
    }

    fn make_part(part_number: u32, size: u64) -> PartMeta {
        PartMeta {
            part_number,
            etag: format!("etag-{part_number:05}"),
            size,
            object_id: format!("__multipart__/test-upload/part-{part_number:05}"),
        }
    }

    #[test]
    fn multipart_upload_crud() {
        let (_dir, store) = tmp_store();

        let upload = make_upload("upload-1", "my-bucket", "large-file.bin");
        store.put_multipart_upload(&upload).unwrap();

        let loaded = store
            .get_multipart_upload("upload-1")
            .unwrap()
            .expect("exists");
        assert_eq!(loaded.bucket, "my-bucket");
        assert_eq!(loaded.key, "large-file.bin");

        store.delete_multipart_upload("upload-1").unwrap();
        assert!(store.get_multipart_upload("upload-1").unwrap().is_none());
    }

    #[test]
    fn multipart_upload_list_by_bucket() {
        let (_dir, store) = tmp_store();

        store
            .put_multipart_upload(&make_upload("u1", "bucket-a", "file1"))
            .unwrap();
        store
            .put_multipart_upload(&make_upload("u2", "bucket-a", "file2"))
            .unwrap();
        store
            .put_multipart_upload(&make_upload("u3", "bucket-b", "file3"))
            .unwrap();

        let uploads_a = store.list_multipart_uploads("bucket-a").unwrap();
        assert_eq!(uploads_a.len(), 2);

        let uploads_b = store.list_multipart_uploads("bucket-b").unwrap();
        assert_eq!(uploads_b.len(), 1);
    }

    #[test]
    fn part_crud() {
        let (_dir, store) = tmp_store();

        let part1 = make_part(1, 5 * 1024 * 1024);
        let part2 = make_part(2, 5 * 1024 * 1024);
        let part3 = make_part(3, 3 * 1024 * 1024);

        store.put_part("upload-1", &part1).unwrap();
        store.put_part("upload-1", &part2).unwrap();
        store.put_part("upload-1", &part3).unwrap();

        // Get individual part
        let loaded = store.get_part("upload-1", 2).unwrap().expect("exists");
        assert_eq!(loaded.part_number, 2);
        assert_eq!(loaded.size, 5 * 1024 * 1024);

        // List all parts (sorted by part number)
        let parts = store.list_parts("upload-1").unwrap();
        assert_eq!(parts.len(), 3);
        assert_eq!(parts[0].part_number, 1);
        assert_eq!(parts[1].part_number, 2);
        assert_eq!(parts[2].part_number, 3);

        // Delete individual part
        store.delete_part("upload-1", 2).unwrap();
        assert!(store.get_part("upload-1", 2).unwrap().is_none());
        assert_eq!(store.list_parts("upload-1").unwrap().len(), 2);
    }

    #[test]
    fn delete_all_parts() {
        let (_dir, store) = tmp_store();

        for i in 1..=5 {
            store.put_part("upload-x", &make_part(i, 1024)).unwrap();
        }
        assert_eq!(store.list_parts("upload-x").unwrap().len(), 5);

        store.delete_all_parts("upload-x").unwrap();
        assert_eq!(store.list_parts("upload-x").unwrap().len(), 0);
    }

    #[test]
    fn parts_isolated_per_upload() {
        let (_dir, store) = tmp_store();

        store.put_part("upload-a", &make_part(1, 100)).unwrap();
        store.put_part("upload-a", &make_part(2, 200)).unwrap();
        store.put_part("upload-b", &make_part(1, 300)).unwrap();

        assert_eq!(store.list_parts("upload-a").unwrap().len(), 2);
        assert_eq!(store.list_parts("upload-b").unwrap().len(), 1);

        store.delete_all_parts("upload-a").unwrap();
        assert_eq!(store.list_parts("upload-a").unwrap().len(), 0);
        assert_eq!(store.list_parts("upload-b").unwrap().len(), 1); // Untouched
    }

    #[test]
    fn multipart_persists_across_restart() {
        let dir = TempDir::new().unwrap();
        {
            let store = MetadataStore::open(dir.path()).unwrap();
            store
                .put_multipart_upload(&make_upload("durable-upload", "bkt", "key"))
                .unwrap();
            store
                .put_part("durable-upload", &make_part(1, 5_000_000))
                .unwrap();
            store
                .put_part("durable-upload", &make_part(2, 5_000_000))
                .unwrap();
            drop(store);
        }
        {
            let store = MetadataStore::open(dir.path()).unwrap();
            let upload = store
                .get_multipart_upload("durable-upload")
                .unwrap()
                .expect("survived");
            assert_eq!(upload.key, "key");

            let parts = store.list_parts("durable-upload").unwrap();
            assert_eq!(parts.len(), 2);
            assert_eq!(parts[0].part_number, 1);
            assert_eq!(parts[1].part_number, 2);
        }
    }

    // ---- Batch with new types ----

    #[test]
    fn batch_bucket_and_multipart() {
        let (_dir, store) = tmp_store();

        store
            .write_batch(vec![
                BatchOp::PutBucket(make_bucket("batch-bucket")),
                BatchOp::PutMultipartUpload(make_upload("batch-upload", "batch-bucket", "key")),
                BatchOp::PutPart {
                    upload_id: "batch-upload".to_string(),
                    part: make_part(1, 1024),
                },
            ])
            .unwrap();

        assert!(store.get_bucket("batch-bucket").unwrap().is_some());
        assert!(store
            .get_multipart_upload("batch-upload")
            .unwrap()
            .is_some());
        assert!(store.get_part("batch-upload", 1).unwrap().is_some());
    }

    // ---- Object versioning tests ----

    fn make_version(
        bucket: &str,
        key: &str,
        version_id: &str,
        is_delete_marker: bool,
    ) -> ObjectVersion {
        ObjectVersion {
            version_id: version_id.to_string(),
            bucket: bucket.to_string(),
            key: key.to_string(),
            is_delete_marker,
            is_latest: false, // Caller sets this
            size: if is_delete_marker { 0 } else { 1024 },
            etag: if is_delete_marker {
                String::new()
            } else {
                "abc123".to_string()
            },
            content_type: if is_delete_marker {
                String::new()
            } else {
                "text/plain".to_string()
            },
            last_modified: 1700000000,
            owner: "testuser".to_string(),
            manifest_object_id: if is_delete_marker {
                String::new()
            } else {
                format!("{bucket}/{key}")
            },
        }
    }

    #[test]
    fn version_put_get() {
        let (_dir, store) = tmp_store();

        let v = make_version("mybucket", "mykey", "v001", false);
        store.put_object_version(&v).unwrap();

        let loaded = store
            .get_object_version("mybucket", "mykey", "v001")
            .unwrap()
            .expect("exists");
        assert_eq!(loaded.version_id, "v001");
        assert_eq!(loaded.size, 1024);
        assert!(!loaded.is_delete_marker);
    }

    #[test]
    fn version_not_found() {
        let (_dir, store) = tmp_store();
        assert!(store
            .get_object_version("b", "k", "v999")
            .unwrap()
            .is_none());
    }

    #[test]
    fn version_multiple_versions_newest_first() {
        let (_dir, store) = tmp_store();

        // Reverse-timestamp IDs: smaller string = newer
        // Simulate: v1 oldest, v3 newest
        let v1 = make_version("b", "k", "00000000000000000003", false); // oldest (largest number)
        let v2 = make_version("b", "k", "00000000000000000002", false);
        let v3 = make_version("b", "k", "00000000000000000001", false); // newest (smallest number)

        store.put_object_version(&v1).unwrap();
        store.put_object_version(&v2).unwrap();
        store.put_object_version(&v3).unwrap();

        let versions = store.list_object_versions("b", "k").unwrap();
        assert_eq!(versions.len(), 3);
        // Newest first (smallest reverse-timestamp = newest)
        assert_eq!(versions[0].version_id, "00000000000000000001");
        assert_eq!(versions[1].version_id, "00000000000000000002");
        assert_eq!(versions[2].version_id, "00000000000000000003");
    }

    #[test]
    fn version_get_latest() {
        let (_dir, store) = tmp_store();

        store
            .put_object_version(&make_version("b", "k", "00000000000000000003", false))
            .unwrap();
        store
            .put_object_version(&make_version("b", "k", "00000000000000000001", false))
            .unwrap();

        let latest = store
            .get_latest_version("b", "k")
            .unwrap()
            .expect("has versions");
        assert_eq!(latest.version_id, "00000000000000000001"); // Newest
    }

    #[test]
    fn version_delete_marker() {
        let (_dir, store) = tmp_store();

        // Put a real version, then a delete marker (newer)
        store
            .put_object_version(&make_version("b", "k", "00000000000000000002", false))
            .unwrap();
        store
            .put_object_version(&make_version("b", "k", "00000000000000000001", true))
            .unwrap();

        let latest = store
            .get_latest_version("b", "k")
            .unwrap()
            .expect("has versions");
        assert!(latest.is_delete_marker);

        // But the real version is still accessible by version_id
        let v = store
            .get_object_version("b", "k", "00000000000000000002")
            .unwrap()
            .expect("still exists");
        assert!(!v.is_delete_marker);
        assert_eq!(v.size, 1024);
    }

    #[test]
    fn version_delete_specific() {
        let (_dir, store) = tmp_store();

        store
            .put_object_version(&make_version("b", "k", "v1", false))
            .unwrap();
        store
            .put_object_version(&make_version("b", "k", "v2", false))
            .unwrap();

        store.delete_object_version("b", "k", "v1").unwrap();

        assert!(store.get_object_version("b", "k", "v1").unwrap().is_none());
        assert!(store.get_object_version("b", "k", "v2").unwrap().is_some());
    }

    #[test]
    fn version_delete_all() {
        let (_dir, store) = tmp_store();

        for i in 0..5 {
            store
                .put_object_version(&make_version("b", "k", &format!("v{i:03}"), false))
                .unwrap();
        }
        assert_eq!(store.list_object_versions("b", "k").unwrap().len(), 5);

        let deleted = store.delete_all_versions("b", "k").unwrap();
        assert_eq!(deleted, 5);
        assert_eq!(store.list_object_versions("b", "k").unwrap().len(), 0);
    }

    #[test]
    fn version_isolation_per_key() {
        let (_dir, store) = tmp_store();

        store
            .put_object_version(&make_version("b", "key1", "v1", false))
            .unwrap();
        store
            .put_object_version(&make_version("b", "key1", "v2", false))
            .unwrap();
        store
            .put_object_version(&make_version("b", "key2", "v1", false))
            .unwrap();

        assert_eq!(store.list_object_versions("b", "key1").unwrap().len(), 2);
        assert_eq!(store.list_object_versions("b", "key2").unwrap().len(), 1);
    }

    #[test]
    fn version_list_bucket_versions() {
        let (_dir, store) = tmp_store();

        store
            .put_object_version(&make_version("b", "a", "v1", false))
            .unwrap();
        store
            .put_object_version(&make_version("b", "b", "v1", false))
            .unwrap();
        store
            .put_object_version(&make_version("b", "b", "v2", false))
            .unwrap();
        store
            .put_object_version(&make_version("other", "x", "v1", false))
            .unwrap();

        let versions = store.list_bucket_versions("b", 100).unwrap();
        assert_eq!(versions.len(), 3);
        // Should not include "other" bucket
    }

    #[test]
    fn version_persists_across_restart() {
        let dir = TempDir::new().unwrap();
        {
            let store = MetadataStore::open(dir.path()).unwrap();
            store
                .put_object_version(&make_version("b", "k", "v1", false))
                .unwrap();
            store
                .put_object_version(&make_version("b", "k", "v2", true))
                .unwrap();
            drop(store);
        }
        {
            let store = MetadataStore::open(dir.path()).unwrap();
            let versions = store.list_object_versions("b", "k").unwrap();
            assert_eq!(versions.len(), 2);
            // Verify both survived restart
            let v1 = store
                .get_object_version("b", "k", "v1")
                .unwrap()
                .expect("survived");
            assert!(!v1.is_delete_marker);
            let v2 = store
                .get_object_version("b", "k", "v2")
                .unwrap()
                .expect("survived");
            assert!(v2.is_delete_marker);
        }
    }

    #[test]
    fn version_id_generation() {
        // Verify IDs are monotonically decreasing (newer = smaller)
        let id1 = MetadataStore::generate_version_id();
        std::thread::sleep(std::time::Duration::from_millis(2));
        let id2 = MetadataStore::generate_version_id();

        // id2 is newer, so it should be lexicographically SMALLER (reverse timestamp)
        assert!(id2 < id1, "newer ID should be smaller: {id2} < {id1}");
    }

    // ---- WriteBatch / SyncMode ----

    #[test]
    fn write_batch_empty_is_noop() {
        let (_d, store) = tmp_store();
        store
            .commit_batch(WriteBatch::new(), SyncMode::Full)
            .unwrap();
        // No state should change.
        assert_eq!(store.get_inline("any-key").unwrap(), None);
    }

    #[test]
    fn write_batch_collects_inline_puts() {
        let (_d, store) = tmp_store();
        let mut b = WriteBatch::new();
        b.put_inline("k1", b"v1");
        b.put_inline("k2", b"v2");
        b.put_inline("k3", b"v3");
        assert_eq!(b.len(), 3);
        store.commit_batch(b, SyncMode::None).unwrap();
        assert_eq!(
            store.get_inline("k1").unwrap().as_deref(),
            Some(b"v1".as_ref())
        );
        assert_eq!(
            store.get_inline("k2").unwrap().as_deref(),
            Some(b"v2".as_ref())
        );
        assert_eq!(
            store.get_inline("k3").unwrap().as_deref(),
            Some(b"v3".as_ref())
        );
    }

    #[test]
    fn write_batch_mixes_put_and_delete_atomically() {
        let (_d, store) = tmp_store();
        // Seed.
        store.put_inline("seeded", b"old").unwrap();
        // Batch: delete the seeded key, add a new one.
        let mut b = WriteBatch::new();
        b.delete_inline("seeded");
        b.put_inline("fresh", b"new");
        store.commit_batch(b, SyncMode::Full).unwrap();
        assert_eq!(store.get_inline("seeded").unwrap(), None);
        assert_eq!(
            store.get_inline("fresh").unwrap().as_deref(),
            Some(b"new".as_ref())
        );
    }

    #[test]
    fn write_batch_volume_chunk() {
        let (_d, store) = tmp_store();
        let vid = [0xABu8; 16];
        let cid = [0xCDu8; 32];
        let mut b = WriteBatch::new();
        b.put_volume_chunk(&vid, 4096, &cid);
        store.commit_batch(b, SyncMode::Full).unwrap();
        let got = store.get_volume_chunk(&vid, 4096).unwrap();
        assert_eq!(got, Some(cid));
    }

    #[test]
    fn write_batch_no_sync_mode_persists() {
        // SyncMode::None doesn't mean "drop the write" — it means
        // "skip the WAL fsync." The data is still visible to subsequent
        // reads on the same store.
        let (_d, store) = tmp_store();
        let mut b = WriteBatch::new();
        b.put_inline("hot", b"path");
        store.commit_batch(b, SyncMode::None).unwrap();
        assert_eq!(
            store.get_inline("hot").unwrap().as_deref(),
            Some(b"path".as_ref())
        );
    }
}

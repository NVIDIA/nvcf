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

//! Core types for nvstor distributed storage.
//!
//! These types map to the C reference implementation's `types.h` and extend
//! them with Rust-specific patterns (newtypes, enums, serde).

use serde::{Deserialize, Deserializer, Serialize, Serializer};
use sha2::{Digest, Sha256};
use std::fmt;
use std::str::FromStr;
use uuid::Uuid;

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/// Default chunk size: 16 MiB.
pub const DEFAULT_CHUNK_SIZE: usize = 16 * 1024 * 1024;

/// Minimum allowed chunk size: 4 MiB.
pub const MIN_CHUNK_SIZE: usize = 4 * 1024 * 1024;

/// Maximum allowed chunk size: 64 MiB.
pub const MAX_CHUNK_SIZE: usize = 64 * 1024 * 1024;

/// Objects at or below this size are stored inline in metadata (Lustre DoM pattern).
pub const INLINE_THRESHOLD: usize = 64 * 1024;

/// OS page size used for O_DIRECT alignment.
pub const PAGE_SIZE: usize = 4096;

/// Default gRPC port.
pub const DEFAULT_GRPC_PORT: u16 = 9100;

/// Default HTTP health/metrics port.
pub const DEFAULT_HTTP_PORT: u16 = 9101;

/// Default Raft port.
pub const DEFAULT_RAFT_PORT: u16 = 9102;

/// Default NBD (Network Block Device) port.
pub const DEFAULT_NBD_PORT: u16 = 10809;

/// Default NFS v4.2 port.
pub const DEFAULT_NFS_PORT: u16 = 2049;

/// WAL file magic bytes: "BED_WAL\0".
pub const WAL_MAGIC: u64 = 0x4245445F57414C00;

/// Current WAL format version.
pub const WAL_VERSION: u32 = 1;

// ---------------------------------------------------------------------------
// Typed wrappers
// ---------------------------------------------------------------------------

/// Monotonically increasing fencing epoch for leader election.
pub type Epoch = u64;

/// Raft node identifier.
pub type NodeId = u64;

/// Unix timestamp in seconds since epoch.
pub type Timestamp = i64;

/// Manifest-level node identifier (inode tree).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub struct ManifestNodeId(pub u64);

/// Inode identifier for filesystem operations.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub struct InodeId(pub u64);

/// Opaque blob identifier.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub struct BlobId(pub Uuid);

impl BlobId {
    /// Generate a new random blob ID.
    pub fn new() -> Self {
        Self(Uuid::new_v4())
    }
}

impl Default for BlobId {
    fn default() -> Self {
        Self::new()
    }
}

// ---------------------------------------------------------------------------
// ChunkId — SHA-256 content hash
// ---------------------------------------------------------------------------

/// Content-addressed chunk identifier (SHA-256 digest).
///
/// Every chunk in nvstor is identified by the SHA-256 hash of its contents,
/// enabling deduplication and integrity verification.
#[derive(Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct ChunkId(pub [u8; 32]);

impl ChunkId {
    /// Compute a ChunkId from raw content bytes.
    pub fn from_content(data: &[u8]) -> Self {
        let hash = Sha256::digest(data);
        let mut id = [0u8; 32];
        id.copy_from_slice(&hash);
        Self(id)
    }

    /// Create a ChunkId from a raw 32-byte array.
    pub fn from_bytes(bytes: [u8; 32]) -> Self {
        Self(bytes)
    }

    /// Return the raw bytes.
    pub fn as_bytes(&self) -> &[u8; 32] {
        &self.0
    }

    /// The zero (null) chunk ID.
    pub const ZERO: Self = Self([0u8; 32]);
}

impl fmt::Display for ChunkId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        for byte in &self.0 {
            write!(f, "{byte:02x}")?;
        }
        Ok(())
    }
}

impl fmt::Debug for ChunkId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "ChunkId({self})")
    }
}

impl FromStr for ChunkId {
    type Err = ChunkIdParseError;

    fn from_str(s: &str) -> std::result::Result<Self, Self::Err> {
        if s.len() != 64 {
            return Err(ChunkIdParseError::InvalidLength(s.len()));
        }
        let mut bytes = [0u8; 32];
        for (i, chunk) in s.as_bytes().chunks(2).enumerate() {
            let hi = hex_digit(chunk[0]).ok_or(ChunkIdParseError::InvalidHex)?;
            let lo = hex_digit(chunk[1]).ok_or(ChunkIdParseError::InvalidHex)?;
            bytes[i] = (hi << 4) | lo;
        }
        Ok(Self(bytes))
    }
}

/// Error parsing a hex-encoded [`ChunkId`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ChunkIdParseError {
    /// Hex string was not exactly 64 characters.
    InvalidLength(usize),
    /// String contained non-hex characters.
    InvalidHex,
}

impl fmt::Display for ChunkIdParseError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidLength(len) => write!(f, "expected 64 hex chars, got {len}"),
            Self::InvalidHex => write!(f, "invalid hex character"),
        }
    }
}

impl std::error::Error for ChunkIdParseError {}

fn hex_digit(b: u8) -> Option<u8> {
    match b {
        b'0'..=b'9' => Some(b - b'0'),
        b'a'..=b'f' => Some(b - b'a' + 10),
        b'A'..=b'F' => Some(b - b'A' + 10),
        _ => None,
    }
}

// Serde: serialize ChunkId as a hex string for human-readable formats,
// raw bytes for binary formats (bincode).
impl Serialize for ChunkId {
    fn serialize<S: Serializer>(&self, serializer: S) -> std::result::Result<S::Ok, S::Error> {
        if serializer.is_human_readable() {
            serializer.serialize_str(&self.to_string())
        } else {
            serializer.serialize_bytes(&self.0)
        }
    }
}

impl<'de> Deserialize<'de> for ChunkId {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> std::result::Result<Self, D::Error> {
        if deserializer.is_human_readable() {
            let s = String::deserialize(deserializer)?;
            s.parse().map_err(serde::de::Error::custom)
        } else {
            struct BytesVisitor;
            impl<'de> serde::de::Visitor<'de> for BytesVisitor {
                type Value = ChunkId;
                fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                    f.write_str("32 bytes")
                }
                fn visit_bytes<E: serde::de::Error>(
                    self,
                    v: &[u8],
                ) -> std::result::Result<ChunkId, E> {
                    if v.len() != 32 {
                        return Err(E::invalid_length(v.len(), &"32 bytes"));
                    }
                    let mut arr = [0u8; 32];
                    arr.copy_from_slice(v);
                    Ok(ChunkId(arr))
                }
                fn visit_seq<A: serde::de::SeqAccess<'de>>(
                    self,
                    mut seq: A,
                ) -> std::result::Result<ChunkId, A::Error> {
                    let mut arr = [0u8; 32];
                    for (i, byte) in arr.iter_mut().enumerate() {
                        *byte = seq
                            .next_element()?
                            .ok_or_else(|| serde::de::Error::invalid_length(i, &"32 bytes"))?;
                    }
                    Ok(ChunkId(arr))
                }
            }
            deserializer.deserialize_bytes(BytesVisitor)
        }
    }
}

// ---------------------------------------------------------------------------
// Chunk metadata
// ---------------------------------------------------------------------------

/// Metadata about a single stored chunk.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ChunkInfo {
    /// Content-addressed ID (SHA-256).
    pub id: ChunkId,
    /// Size in bytes.
    pub size: u64,
    /// IEEE CRC-32 checksum of the chunk data.
    pub checksum: u32,
    /// Node IDs where replicas are stored.
    pub locations: Vec<NodeId>,
    /// Reference count (number of objects/volumes referencing this chunk).
    pub ref_count: u64,
    /// Unix timestamp when the chunk was first stored.
    pub created_at: Timestamp,
    /// Unix timestamp of last access.
    pub last_accessed: Timestamp,
}

/// A reference to a chunk within an object or volume, describing a logical byte range.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ChunkRef {
    /// The chunk being referenced.
    pub chunk_id: ChunkId,
    /// Size of data within this chunk (may be less than chunk capacity for tail chunks).
    pub length: u64,
    /// IEEE CRC-32 checksum for integrity.
    pub checksum: u32,
    /// Logical start offset within the object.
    pub logical_start: u64,
    /// Logical end offset (exclusive) within the object.
    pub logical_end: u64,
}

// ---------------------------------------------------------------------------
// Object manifest
// ---------------------------------------------------------------------------

/// Manifest describing an object as an ordered list of chunk references.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct ObjectManifest {
    /// Unique object identifier (e.g., S3 key hash or UUID).
    pub object_id: String,
    /// Total object size in bytes.
    pub size: u64,
    /// Ordered chunks composing this object.
    pub chunks: Vec<ChunkRef>,
    /// ETag (typically MD5 or multipart ETag).
    pub etag: Option<String>,
    /// MIME content type.
    pub content_type: Option<String>,
    /// Unix timestamp of creation.
    pub created_at: Timestamp,
    /// Unix timestamp of last modification.
    pub modified_at: Timestamp,
}

// ---------------------------------------------------------------------------
// Volume types
// ---------------------------------------------------------------------------

/// Volume access mode, following Kubernetes PV semantics.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum AccessMode {
    /// Read-Write-Once: single writer, single reader.
    RWO,
    /// Read-Only-Many: no writers, many readers.
    ROX,
    /// Read-Write-Many: many writers, many readers.
    RWX,
}

/// Consistency guarantees for volume I/O.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum ConsistencyMode {
    /// Linearizable reads and writes.
    Strong,
    /// Session-consistent (read-your-writes).
    Session,
    /// Append-only: writes only extend the volume.
    AppendOnly,
}

/// Lifecycle state of a volume.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum VolumeState {
    /// Volume is being provisioned.
    Creating,
    /// Volume is healthy and available.
    Ready,
    /// Volume has lost replicas but is still serving.
    Degraded,
    /// Volume is being deleted.
    Deleting,
}

/// Read consistency level for distributed reads.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum ReadConsistency {
    /// Read from leader; linearizable.
    Strong,
    /// Read from any replica; may be stale.
    Eventual,
    /// Read from any replica if freshness is within `max_staleness` seconds.
    BoundedStaleness {
        /// Maximum acceptable staleness in seconds.
        max_staleness: u64,
    },
}

/// Block-level volume with chunk mapping.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct VolumeMap {
    /// Unique volume identifier.
    pub volume_id: Uuid,
    /// Volume size in bytes.
    pub size: u64,
    /// Chunk size used for this volume.
    pub chunk_size: usize,
    /// Access mode.
    pub access_mode: AccessMode,
    /// Consistency guarantee.
    pub consistency: ConsistencyMode,
    /// Lifecycle state.
    pub state: VolumeState,
    /// Node ID of the active writer (for RWO).
    pub active_writer: Option<NodeId>,
    /// Current append offset (for AppendOnly consistency).
    pub append_offset: u64,
    /// Mapping from chunk index to ChunkId. `None` entries are unwritten (sparse).
    pub chunks: Vec<Option<ChunkId>>,
}

// ---------------------------------------------------------------------------
// Stats / Reports
// ---------------------------------------------------------------------------

/// Storage statistics snapshot.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct StorageStats {
    /// Total number of chunks.
    pub total_chunks: u64,
    /// Total bytes stored (logical, before dedup).
    pub total_bytes: u64,
    /// Unique bytes stored (physical, after dedup).
    pub unique_bytes: u64,
    /// Number of objects.
    pub total_objects: u64,
    /// Number of volumes.
    pub total_volumes: u64,
}

/// Result of a garbage collection run.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct GcReport {
    /// Number of chunks deleted.
    pub chunks_deleted: u64,
    /// Bytes reclaimed.
    pub bytes_reclaimed: u64,
    /// Duration of the GC pass in milliseconds.
    pub duration_ms: u64,
    /// Number of errors encountered during GC.
    pub errors: u64,
}

/// Result of an integrity check.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct IntegrityReport {
    /// Total chunks checked.
    pub chunks_checked: u64,
    /// Chunks with checksum mismatches.
    pub checksum_failures: u64,
    /// Chunks that are missing from expected locations.
    pub missing_chunks: u64,
    /// Chunks with orphaned data (no references).
    pub orphaned_chunks: u64,
    /// Duration of the check in milliseconds.
    pub duration_ms: u64,
}

/// Capabilities reported by a storage backend.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct BackendCaps {
    /// Supports O_DIRECT / DMA alignment.
    pub direct_io: bool,
    /// Supports io_uring async I/O.
    pub io_uring: bool,
    /// Supports RDMA zero-copy.
    pub rdma: bool,
    /// Supports server-side copy (NFS v4.2 COPY).
    pub server_side_copy: bool,
    /// Maximum concurrent I/O operations.
    pub max_concurrent_io: u32,
    /// Required alignment for direct I/O buffers.
    pub alignment: usize,
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    // -- ChunkId --

    #[test]
    fn chunk_id_from_content_deterministic() {
        let data = b"hello nvstor";
        let id1 = ChunkId::from_content(data);
        let id2 = ChunkId::from_content(data);
        assert_eq!(id1, id2);
    }

    #[test]
    fn chunk_id_different_content_different_id() {
        let id1 = ChunkId::from_content(b"aaa");
        let id2 = ChunkId::from_content(b"bbb");
        assert_ne!(id1, id2);
    }

    #[test]
    fn chunk_id_display_parse_roundtrip() {
        let id = ChunkId::from_content(b"roundtrip test");
        let hex = id.to_string();
        assert_eq!(hex.len(), 64);
        let parsed: ChunkId = hex.parse().unwrap();
        assert_eq!(id, parsed);
    }

    #[test]
    fn chunk_id_parse_uppercase() {
        let id = ChunkId::from_content(b"upper");
        let hex = id.to_string().to_uppercase();
        let parsed: ChunkId = hex.parse().unwrap();
        assert_eq!(id, parsed);
    }

    #[test]
    fn chunk_id_parse_bad_length() {
        let result = "abcd".parse::<ChunkId>();
        assert_eq!(result, Err(ChunkIdParseError::InvalidLength(4)));
    }

    #[test]
    fn chunk_id_parse_bad_hex() {
        let bad = "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz";
        assert_eq!(bad.len(), 64);
        let result = bad.parse::<ChunkId>();
        assert_eq!(result, Err(ChunkIdParseError::InvalidHex));
    }

    #[test]
    fn chunk_id_serde_json_roundtrip() {
        let id = ChunkId::from_content(b"json test");
        let json = serde_json::to_string(&id).unwrap();
        // Should be a quoted hex string.
        assert!(json.starts_with('"'));
        let deserialized: ChunkId = serde_json::from_str(&json).unwrap();
        assert_eq!(id, deserialized);
    }

    #[test]
    fn chunk_id_bincode_roundtrip() {
        let id = ChunkId::from_content(b"bincode test");
        let encoded = bincode::serialize(&id).unwrap();
        let decoded: ChunkId = bincode::deserialize(&encoded).unwrap();
        assert_eq!(id, decoded);
    }

    #[test]
    fn chunk_id_zero() {
        assert_eq!(ChunkId::ZERO.0, [0u8; 32]);
    }

    // -- ChunkInfo --

    #[test]
    fn chunk_info_serde_json_roundtrip() {
        let info = ChunkInfo {
            id: ChunkId::from_content(b"chunk"),
            size: 4096,
            checksum: 0xDEADBEEF,
            locations: vec![1, 2, 3],
            ref_count: 5,
            created_at: 1700000000,
            last_accessed: 1700000100,
        };
        let json = serde_json::to_string(&info).unwrap();
        let back: ChunkInfo = serde_json::from_str(&json).unwrap();
        assert_eq!(info, back);
    }

    #[test]
    fn chunk_info_bincode_roundtrip() {
        let info = ChunkInfo {
            id: ChunkId::from_content(b"chunk"),
            size: 1024,
            checksum: 42,
            locations: vec![1],
            ref_count: 1,
            created_at: 0,
            last_accessed: 0,
        };
        let encoded = bincode::serialize(&info).unwrap();
        let decoded: ChunkInfo = bincode::deserialize(&encoded).unwrap();
        assert_eq!(info, decoded);
    }

    // -- ChunkRef --

    #[test]
    fn chunk_ref_serde_roundtrip() {
        let cr = ChunkRef {
            chunk_id: ChunkId::from_content(b"ref"),
            length: 512,
            checksum: 0xCAFE,
            logical_start: 0,
            logical_end: 512,
        };
        let json = serde_json::to_string(&cr).unwrap();
        let back: ChunkRef = serde_json::from_str(&json).unwrap();
        assert_eq!(cr, back);

        let bin = bincode::serialize(&cr).unwrap();
        let back2: ChunkRef = bincode::deserialize(&bin).unwrap();
        assert_eq!(cr, back2);
    }

    // -- ObjectManifest --

    #[test]
    fn object_manifest_serde_roundtrip() {
        let manifest = ObjectManifest {
            object_id: "bucket/key".to_string(),
            size: 1024,
            chunks: vec![ChunkRef {
                chunk_id: ChunkId::from_content(b"data"),
                length: 1024,
                checksum: 123,
                logical_start: 0,
                logical_end: 1024,
            }],
            etag: Some("abc123".to_string()),
            content_type: Some("application/octet-stream".to_string()),
            created_at: 1700000000,
            modified_at: 1700000001,
        };
        let json = serde_json::to_string(&manifest).unwrap();
        let back: ObjectManifest = serde_json::from_str(&json).unwrap();
        assert_eq!(manifest, back);

        let bin = bincode::serialize(&manifest).unwrap();
        let back2: ObjectManifest = bincode::deserialize(&bin).unwrap();
        assert_eq!(manifest, back2);
    }

    // -- VolumeMap --

    #[test]
    fn volume_map_serde_roundtrip() {
        let vol = VolumeMap {
            volume_id: Uuid::new_v4(),
            size: 1024 * 1024 * 1024,
            chunk_size: DEFAULT_CHUNK_SIZE,
            access_mode: AccessMode::RWO,
            consistency: ConsistencyMode::Strong,
            state: VolumeState::Ready,
            active_writer: Some(1),
            append_offset: 0,
            chunks: vec![None, Some(ChunkId::from_content(b"vol-chunk")), None],
        };
        let json = serde_json::to_string(&vol).unwrap();
        let back: VolumeMap = serde_json::from_str(&json).unwrap();
        assert_eq!(vol, back);

        let bin = bincode::serialize(&vol).unwrap();
        let back2: VolumeMap = bincode::deserialize(&bin).unwrap();
        assert_eq!(vol, back2);
    }

    // -- Enums --

    #[test]
    fn access_mode_serde_roundtrip() {
        for mode in [AccessMode::RWO, AccessMode::ROX, AccessMode::RWX] {
            let json = serde_json::to_string(&mode).unwrap();
            let back: AccessMode = serde_json::from_str(&json).unwrap();
            assert_eq!(mode, back);
        }
    }

    #[test]
    fn consistency_mode_serde_roundtrip() {
        for mode in [
            ConsistencyMode::Strong,
            ConsistencyMode::Session,
            ConsistencyMode::AppendOnly,
        ] {
            let json = serde_json::to_string(&mode).unwrap();
            let back: ConsistencyMode = serde_json::from_str(&json).unwrap();
            assert_eq!(mode, back);
        }
    }

    #[test]
    fn volume_state_serde_roundtrip() {
        for state in [
            VolumeState::Creating,
            VolumeState::Ready,
            VolumeState::Degraded,
            VolumeState::Deleting,
        ] {
            let json = serde_json::to_string(&state).unwrap();
            let back: VolumeState = serde_json::from_str(&json).unwrap();
            assert_eq!(state, back);
        }
    }

    #[test]
    fn read_consistency_serde_roundtrip() {
        for rc in [
            ReadConsistency::Strong,
            ReadConsistency::Eventual,
            ReadConsistency::BoundedStaleness { max_staleness: 30 },
        ] {
            let json = serde_json::to_string(&rc).unwrap();
            let back: ReadConsistency = serde_json::from_str(&json).unwrap();
            assert_eq!(rc, back);
        }
    }

    // -- Stats / Reports --

    #[test]
    fn storage_stats_serde_roundtrip() {
        let stats = StorageStats {
            total_chunks: 100,
            total_bytes: 400_000_000,
            unique_bytes: 300_000_000,
            total_objects: 50,
            total_volumes: 2,
        };
        let json = serde_json::to_string(&stats).unwrap();
        let back: StorageStats = serde_json::from_str(&json).unwrap();
        assert_eq!(stats, back);

        let bin = bincode::serialize(&stats).unwrap();
        let back2: StorageStats = bincode::deserialize(&bin).unwrap();
        assert_eq!(stats, back2);
    }

    #[test]
    fn gc_report_serde_roundtrip() {
        let report = GcReport {
            chunks_deleted: 10,
            bytes_reclaimed: 40_000_000,
            duration_ms: 150,
            errors: 0,
        };
        let json = serde_json::to_string(&report).unwrap();
        let back: GcReport = serde_json::from_str(&json).unwrap();
        assert_eq!(report, back);
    }

    #[test]
    fn integrity_report_serde_roundtrip() {
        let report = IntegrityReport {
            chunks_checked: 1000,
            checksum_failures: 1,
            missing_chunks: 2,
            orphaned_chunks: 5,
            duration_ms: 3000,
        };
        let bin = bincode::serialize(&report).unwrap();
        let back: IntegrityReport = bincode::deserialize(&bin).unwrap();
        assert_eq!(report, back);
    }

    #[test]
    fn backend_caps_serde_roundtrip() {
        let caps = BackendCaps {
            direct_io: true,
            io_uring: true,
            rdma: false,
            server_side_copy: false,
            max_concurrent_io: 256,
            alignment: 4096,
        };
        let json = serde_json::to_string(&caps).unwrap();
        let back: BackendCaps = serde_json::from_str(&json).unwrap();
        assert_eq!(caps, back);
    }

    // -- Typed wrappers --

    #[test]
    fn manifest_node_id_serde_roundtrip() {
        let id = ManifestNodeId(42);
        let json = serde_json::to_string(&id).unwrap();
        let back: ManifestNodeId = serde_json::from_str(&json).unwrap();
        assert_eq!(id, back);
    }

    #[test]
    fn inode_id_serde_roundtrip() {
        let id = InodeId(1);
        let bin = bincode::serialize(&id).unwrap();
        let back: InodeId = bincode::deserialize(&bin).unwrap();
        assert_eq!(id, back);
    }

    #[test]
    fn blob_id_serde_roundtrip() {
        let id = BlobId::new();
        let json = serde_json::to_string(&id).unwrap();
        let back: BlobId = serde_json::from_str(&json).unwrap();
        assert_eq!(id, back);
    }

    // -- Constants --

    #[test]
    fn constants_are_powers_of_two() {
        assert!(DEFAULT_CHUNK_SIZE.is_power_of_two());
        assert!(MIN_CHUNK_SIZE.is_power_of_two());
        assert!(MAX_CHUNK_SIZE.is_power_of_two());
        assert!(INLINE_THRESHOLD.is_power_of_two());
        assert!(PAGE_SIZE.is_power_of_two());
    }

    #[test]
    fn chunk_size_ordering() {
        const {
            assert!(MIN_CHUNK_SIZE <= DEFAULT_CHUNK_SIZE);
            assert!(DEFAULT_CHUNK_SIZE <= MAX_CHUNK_SIZE);
        }
    }

    // -- Send + Sync compile-time checks --

    fn _assert_send<T: Send>() {}
    fn _assert_sync<T: Sync>() {}

    #[test]
    fn types_are_send_sync() {
        _assert_send::<ChunkId>();
        _assert_sync::<ChunkId>();
        _assert_send::<ChunkInfo>();
        _assert_sync::<ChunkInfo>();
        _assert_send::<ChunkRef>();
        _assert_sync::<ChunkRef>();
        _assert_send::<ObjectManifest>();
        _assert_sync::<ObjectManifest>();
        _assert_send::<VolumeMap>();
        _assert_sync::<VolumeMap>();
        _assert_send::<StorageStats>();
        _assert_sync::<StorageStats>();
        _assert_send::<GcReport>();
        _assert_sync::<GcReport>();
        _assert_send::<IntegrityReport>();
        _assert_sync::<IntegrityReport>();
        _assert_send::<BackendCaps>();
        _assert_sync::<BackendCaps>();
        _assert_send::<ManifestNodeId>();
        _assert_sync::<ManifestNodeId>();
        _assert_send::<InodeId>();
        _assert_sync::<InodeId>();
        _assert_send::<BlobId>();
        _assert_sync::<BlobId>();
    }
}

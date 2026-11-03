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

//! Raft types — core data structures for consensus.

use std::time::Instant;

use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// Raft node identifier (1-based, 0 = unknown/none).
pub type NodeId = u64;

/// Raft term (monotonically increasing logical clock).
pub type Term = u64;

/// Log index (1-based, 0 = no entries).
pub type LogIndex = u64;

/// Raft node role.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Role {
    Follower,
    Candidate,
    Leader,
}

impl std::fmt::Display for Role {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Role::Follower => write!(f, "Follower"),
            Role::Candidate => write!(f, "Candidate"),
            Role::Leader => write!(f, "Leader"),
        }
    }
}

/// Data payload of a Raft log entry.
///
/// These are the mutations that get replicated across the cluster.
/// Every mutation to metadata MUST go through Raft on the leader.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub enum LogEntryData {
    /// No-op entry (used for leader confirmation after election).
    Noop,

    // -- Object/manifest mutations --
    /// Store or update an object manifest.
    ///
    /// Carries the FULL chunk reference list + timestamps so a follower
    /// that applies this entry can reconstruct a complete, readable
    /// manifest. (Chunk *data* is replicated separately to placement
    /// targets; a non-replica node fetches chunks on read.) Without the
    /// chunk refs a follower's manifest would be unreadable — the reason
    /// naive metadata-through-Raft didn't work before.
    ManifestPut {
        object_id: String,
        size: u64,
        etag: String,
        content_type: String,
        #[serde(default)]
        chunks: Vec<nvstor_metadata::ChunkRef>,
        #[serde(default)]
        created_at: i64,
        #[serde(default)]
        modified_at: i64,
    },
    /// Delete an object manifest.
    ManifestDelete { object_id: String },

    // -- Object version index (S3 versioning) --
    /// Store or update an object-version record. Carries the full
    /// `ObjectVersion` so every node's version index matches — required
    /// for versioned GET-by-version-id and ListObjectVersions to return
    /// the same answer regardless of which node serves the read. The
    /// state machine calls `put_object_version`, whose is_latest flag
    /// rewrite is deterministic given the same record + prior state.
    ObjectVersionPut {
        version: nvstor_metadata::ObjectVersion,
    },
    /// Delete a specific object-version record.
    ObjectVersionDelete {
        bucket: String,
        key: String,
        version_id: String,
    },

    // -- Generic inline metadata (SSE encryption keys, aliases) --
    /// Store an inline metadata key/value on every node. Used for SSE
    /// encryption metadata (the envelope-encrypted DEK + IV) so a
    /// follower serving a read can decrypt the object. The value is
    /// opaque to the state machine — it just writes the bytes.
    InlinePut { key: String, value: Vec<u8> },
    /// Delete an inline metadata key on every node.
    InlineDelete { key: String },

    // -- Volume mutations --
    /// Create or update a volume.
    VolumePut {
        volume_id: Uuid,
        name: String,
        size: u64,
        replication: u8,
    },
    /// Delete a volume.
    VolumeDelete { volume_id: Uuid },

    // -- Bucket mutations --
    /// Create or update a bucket.
    BucketPut {
        name: String,
        owner: String,
        region: String,
        versioning: u8,
    },
    /// Delete a bucket.
    BucketDelete { name: String },

    // -- VFS mutations (for NFS) --
    /// Create a regular file.
    VfsCreateFile {
        parent_ino: u64,
        name: String,
        mode: u32,
    },
    /// Create a directory.
    VfsMkdir {
        parent_ino: u64,
        name: String,
        mode: u32,
    },
    /// Write data to a file.
    VfsWrite {
        ino: u64,
        offset: u64,
        data: Vec<u8>,
    },
    /// Delete a file.
    VfsUnlink { parent_ino: u64, name: String },
    /// Remove a directory.
    VfsRmdir { parent_ino: u64, name: String },
    /// Truncate a file.
    VfsTruncate { ino: u64, size: u64 },
    /// Rename a file or directory.
    VfsRename {
        old_parent: u64,
        old_name: String,
        new_parent: u64,
        new_name: String,
    },
    /// Create a symbolic link.
    VfsSymlink {
        parent_ino: u64,
        name: String,
        target: String,
    },
    /// Create a hard link.
    VfsLink {
        target_ino: u64,
        parent_ino: u64,
        name: String,
    },
    /// Complete manifest, including an inline payload. Appended to preserve
    /// the bincode discriminants and layouts of existing persisted log entries.
    ManifestPutV2 {
        manifest: nvstor_metadata::ManifestMeta,
    },
    /// Conditional atomic inline mutation, used for replicated root-wrapped KMS keys.
    ConditionalInlineBatch {
        check_key: String,
        expected: Option<Vec<u8>>,
        puts: Vec<(String, Vec<u8>)>,
        deletes: Vec<String>,
    },
}

/// A single entry in the Raft log.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct LogEntry {
    /// Term when entry was received by leader.
    pub term: Term,
    /// Position in the log (1-based).
    pub index: LogIndex,
    /// The replicated data.
    pub data: LogEntryData,
}

/// Persistent Raft state that must survive restarts.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PersistentState {
    /// Latest term this node has seen.
    pub current_term: Term,
    /// Candidate ID that received vote in current term (-1 = none).
    pub voted_for: i64,
}

impl Default for PersistentState {
    fn default() -> Self {
        Self {
            current_term: 0,
            voted_for: -1,
        }
    }
}

/// Volatile state on all nodes.
#[derive(Debug, Clone)]
pub struct VolatileState {
    /// Index of highest log entry known to be committed.
    pub commit_index: LogIndex,
    /// Index of highest log entry applied to state machine.
    pub last_applied: LogIndex,
}

impl Default for VolatileState {
    fn default() -> Self {
        Self {
            commit_index: 0,
            last_applied: 0,
        }
    }
}

/// Volatile state on leaders (reinitialized after election).
#[derive(Debug, Clone)]
pub struct LeaderState {
    /// For each peer: index of next log entry to send.
    pub next_index: Vec<LogIndex>,
    /// For each peer: index of highest log entry known to be replicated.
    pub match_index: Vec<LogIndex>,
    /// For each peer: when it last answered an AppendEntries in this term.
    /// Drives check-quorum: a leader that has not heard from a majority
    /// within one election timeout steps down.
    pub last_ack: Vec<Option<Instant>>,
    /// When this node became leader. Check-quorum gives a new leader one
    /// election timeout to collect acknowledgements before judging it.
    pub elected_at: Instant,
}

impl LeaderState {
    /// Initialize leader state for `peer_count` peers, starting from `last_log_index`.
    pub fn new(peer_count: usize, last_log_index: LogIndex) -> Self {
        Self {
            next_index: vec![last_log_index + 1; peer_count],
            match_index: vec![0; peer_count],
            last_ack: vec![None; peer_count],
            elected_at: Instant::now(),
        }
    }
}

/// Configuration for a peer node.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PeerConfig {
    /// Peer node ID.
    pub node_id: NodeId,
    /// Address in "host:port" format.
    pub addr: String,
}

/// Raft cluster configuration.
#[derive(Debug, Clone)]
pub struct RaftConfig {
    /// This node's ID (1-based).
    pub node_id: NodeId,
    /// TCP port for Raft RPC.
    pub raft_port: u16,
    /// Peer nodes.
    pub peers: Vec<PeerConfig>,
    /// Minimum election timeout in milliseconds.
    pub election_timeout_min_ms: u64,
    /// Maximum election timeout in milliseconds.
    pub election_timeout_max_ms: u64,
    /// Heartbeat interval in milliseconds.
    pub heartbeat_ms: u64,
    /// Maximum time to wait for a proposal to commit.
    pub propose_timeout_ms: u64,
}

impl Default for RaftConfig {
    fn default() -> Self {
        Self {
            node_id: 1,
            raft_port: 9102,
            peers: Vec::new(),
            election_timeout_min_ms: 1000,
            election_timeout_max_ms: 5000,
            heartbeat_ms: 200,
            propose_timeout_ms: 5000,
        }
    }
}

/// Result of proposing a log entry.
#[derive(Debug)]
pub enum ProposeResult {
    /// Entry committed by majority.
    Committed { index: LogIndex, term: Term },
    /// This node is not the leader.
    NotLeader { leader_id: NodeId },
    /// Proposal timed out waiting for commit.
    Timeout,
}

/// RPC message types.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
#[repr(u8)]
pub enum RpcType {
    RequestVote = 0x01,
    RequestVoteResp = 0x02,
    AppendEntries = 0x03,
    AppendEntriesResp = 0x04,
}

impl RpcType {
    pub fn from_u8(v: u8) -> Option<Self> {
        match v {
            0x01 => Some(Self::RequestVote),
            0x02 => Some(Self::RequestVoteResp),
            0x03 => Some(Self::AppendEntries),
            0x04 => Some(Self::AppendEntriesResp),
            _ => None,
        }
    }
}

/// Raft RPC header (12 bytes).
pub const RAFT_MAGIC: u32 = 0x52414654; // "RAFT"
pub const RAFT_HEADER_SIZE: usize = 12;

// ============================================================================
// RPC message payloads
// ============================================================================

/// RequestVote RPC (leader election).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RequestVote {
    pub term: Term,
    pub candidate_id: NodeId,
    pub last_log_index: LogIndex,
    pub last_log_term: Term,
}

/// RequestVote response.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RequestVoteResp {
    pub term: Term,
    pub vote_granted: bool,
}

/// AppendEntries RPC (heartbeat + log replication).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AppendEntries {
    pub term: Term,
    pub leader_id: NodeId,
    pub prev_log_index: LogIndex,
    pub prev_log_term: Term,
    pub leader_commit: LogIndex,
    pub entries: Vec<LogEntry>,
}

/// AppendEntries response.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AppendEntriesResp {
    pub term: Term,
    pub success: bool,
    pub match_index: LogIndex,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn log_entry_serialization_roundtrip() {
        let entry = LogEntry {
            term: 5,
            index: 42,
            data: LogEntryData::ManifestPut {
                object_id: "bucket/key".into(),
                size: 1024,
                etag: "abc123".into(),
                content_type: "application/octet-stream".into(),
                chunks: vec![nvstor_metadata::ChunkRef {
                    chunk_id: [7u8; 32],
                    length: 512,
                    checksum: 0xdead_beef,
                    logical_start: 0,
                    logical_end: 512,
                    nodes: vec![1, 2],
                }],
                created_at: 111,
                modified_at: 222,
            },
        };
        let encoded = bincode::serialize(&entry).expect("serialize");
        let decoded: LogEntry = bincode::deserialize(&encoded).expect("deserialize");
        assert_eq!(entry, decoded);
    }

    #[test]
    fn all_entry_types_serialize() {
        let entries = vec![
            LogEntryData::Noop,
            LogEntryData::ManifestPut {
                object_id: "o".into(),
                size: 0,
                etag: "e".into(),
                content_type: "t".into(),
                chunks: vec![],
                created_at: 0,
                modified_at: 0,
            },
            LogEntryData::ManifestDelete {
                object_id: "o".into(),
            },
            LogEntryData::VolumePut {
                volume_id: Uuid::new_v4(),
                name: "v".into(),
                size: 100,
                replication: 3,
            },
            LogEntryData::VolumeDelete {
                volume_id: Uuid::new_v4(),
            },
            LogEntryData::BucketPut {
                name: "b".into(),
                owner: "u".into(),
                region: "r".into(),
                versioning: 1,
            },
            LogEntryData::BucketDelete { name: "b".into() },
            LogEntryData::VfsCreateFile {
                parent_ino: 1,
                name: "f".into(),
                mode: 0o644,
            },
            LogEntryData::VfsMkdir {
                parent_ino: 1,
                name: "d".into(),
                mode: 0o755,
            },
            LogEntryData::VfsWrite {
                ino: 2,
                offset: 0,
                data: vec![1, 2, 3],
            },
            LogEntryData::VfsUnlink {
                parent_ino: 1,
                name: "f".into(),
            },
            LogEntryData::VfsRmdir {
                parent_ino: 1,
                name: "d".into(),
            },
            LogEntryData::VfsTruncate { ino: 2, size: 0 },
            LogEntryData::VfsRename {
                old_parent: 1,
                old_name: "a".into(),
                new_parent: 1,
                new_name: "b".into(),
            },
        ];

        for (i, data) in entries.into_iter().enumerate() {
            let entry = LogEntry {
                term: 1,
                index: i as u64 + 1,
                data,
            };
            let encoded = bincode::serialize(&entry).expect("serialize");
            let decoded: LogEntry = bincode::deserialize(&encoded).expect("deserialize");
            assert_eq!(entry, decoded, "entry type {i} roundtrip failed");
        }
    }

    #[test]
    fn request_vote_serialization() {
        let rv = RequestVote {
            term: 3,
            candidate_id: 2,
            last_log_index: 10,
            last_log_term: 2,
        };
        let encoded = bincode::serialize(&rv).expect("serialize");
        let decoded: RequestVote = bincode::deserialize(&encoded).expect("deserialize");
        assert_eq!(rv.term, decoded.term);
        assert_eq!(rv.candidate_id, decoded.candidate_id);
    }

    #[test]
    fn append_entries_serialization() {
        let ae = AppendEntries {
            term: 5,
            leader_id: 1,
            prev_log_index: 9,
            prev_log_term: 4,
            leader_commit: 8,
            entries: vec![
                LogEntry {
                    term: 5,
                    index: 10,
                    data: LogEntryData::Noop,
                },
                LogEntry {
                    term: 5,
                    index: 11,
                    data: LogEntryData::ManifestDelete {
                        object_id: "x".into(),
                    },
                },
            ],
        };
        let encoded = bincode::serialize(&ae).expect("serialize");
        let decoded: AppendEntries = bincode::deserialize(&encoded).expect("deserialize");
        assert_eq!(ae.entries.len(), decoded.entries.len());
        assert_eq!(ae.term, decoded.term);
    }

    #[test]
    fn leader_state_initialization() {
        let ls = LeaderState::new(3, 10);
        assert_eq!(ls.next_index, vec![11, 11, 11]);
        assert_eq!(ls.match_index, vec![0, 0, 0]);
        assert_eq!(ls.last_ack, vec![None, None, None]);
    }

    #[test]
    fn rpc_type_from_u8() {
        assert_eq!(RpcType::from_u8(0x01), Some(RpcType::RequestVote));
        assert_eq!(RpcType::from_u8(0x03), Some(RpcType::AppendEntries));
        assert_eq!(RpcType::from_u8(0xFF), None);
    }

    #[test]
    fn persistent_state_default() {
        let ps = PersistentState::default();
        assert_eq!(ps.current_term, 0);
        assert_eq!(ps.voted_for, -1);
    }

    #[test]
    fn config_default() {
        let cfg = RaftConfig::default();
        assert_eq!(cfg.node_id, 1);
        assert_eq!(cfg.raft_port, 9102);
        assert_eq!(cfg.heartbeat_ms, 200);
    }
}

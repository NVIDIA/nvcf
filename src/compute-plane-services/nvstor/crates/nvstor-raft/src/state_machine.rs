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

//! Raft state machine — applies committed log entries to storage.
//!
//! When a log entry is committed (majority acked), the state machine
//! applies it to the actual storage backend (MetadataStore + ChunkManager).
//! This is deterministic: the same log entries produce the same state on all nodes.

use std::sync::Arc;

use nvstor_metadata::{BucketMeta, MetadataStore};
use tracing::debug;

use crate::types::*;

/// Trait for applying committed Raft entries to storage.
///
/// Implementations are provided for different storage backends.
/// The state machine is called by RaftNode::apply_committed() after
/// entries reach the commit index.
pub trait StateMachine: Send + Sync {
    /// Apply a single committed log entry.
    /// Must be deterministic — same input produces same output on all nodes.
    fn apply(&self, entry: &LogEntry) -> Result<(), String>;
}

/// State machine backed by MetadataStore (RocksDB).
///
/// Applies metadata mutations: bucket create/delete, manifest put/delete,
/// volume create/delete. Does NOT apply VFS or chunk data mutations yet
/// (those need ChunkManager integration).
pub struct MetadataStateMachine {
    metadata: Arc<MetadataStore>,
}

impl MetadataStateMachine {
    pub fn new(metadata: Arc<MetadataStore>) -> Self {
        Self { metadata }
    }
}

impl StateMachine for MetadataStateMachine {
    fn apply(&self, entry: &LogEntry) -> Result<(), String> {
        match &entry.data {
            LogEntryData::Noop => {
                debug!(index = entry.index, "state machine: noop");
            }

            LogEntryData::BucketPut {
                name,
                owner,
                region,
                versioning,
            } => {
                let meta = BucketMeta {
                    name: name.clone(),
                    owner: owner.clone(),
                    created_at: 0, // the log entry carries no timestamp
                    region: region.clone(),
                    versioning: *versioning,
                };
                self.metadata
                    .put_bucket(&meta)
                    .map_err(|error| error.to_string())?;
                debug!(
                    index = entry.index,
                    name, "state machine: applied BucketPut"
                );
            }

            LogEntryData::BucketDelete { name } => {
                self.metadata
                    .delete_bucket(name)
                    .map_err(|error| error.to_string())?;
                debug!(
                    index = entry.index,
                    name, "state machine: applied BucketDelete"
                );
            }

            LogEntryData::ManifestPut {
                object_id,
                size,
                etag,
                content_type,
                chunks,
                created_at,
                modified_at,
            } => {
                let meta = nvstor_metadata::ManifestMeta {
                    object_id: object_id.clone(),
                    size: *size,
                    chunks: chunks.clone(), // full chunk refs so followers can read
                    etag: etag.clone(),
                    content_type: content_type.clone(),
                    created_at: *created_at,
                    modified_at: *modified_at,
                    inline_data: None,
                };
                self.metadata
                    .put_manifest(&meta)
                    .map_err(|error| error.to_string())?;
                debug!(
                    index = entry.index,
                    object_id,
                    chunks = meta.chunks.len(),
                    "state machine: applied ManifestPut"
                );
            }

            LogEntryData::ManifestPutV2 { manifest } => {
                let mut ops = vec![
                    nvstor_metadata::BatchOp::PutManifest(manifest.clone()),
                    nvstor_metadata::BatchOp::DeleteInline(manifest.object_id.clone()),
                ];
                if let Some(data) = &manifest.inline_data {
                    if data.len() as u64 != manifest.size || !manifest.chunks.is_empty() {
                        return Err("invalid inline manifest".into());
                    }
                    ops.push(nvstor_metadata::BatchOp::PutInline {
                        object_id: manifest.object_id.clone(),
                        data: data.clone(),
                    });
                }
                self.metadata
                    .write_batch(ops)
                    .map_err(|error| error.to_string())?;
            }

            LogEntryData::ConditionalInlineBatch {
                check_key,
                expected,
                puts,
                deletes,
            } => {
                // A losing compare-and-set is an applied no-op, never a stalled Raft log.
                if self
                    .metadata
                    .get_inline(check_key)
                    .map_err(|e| e.to_string())?
                    == *expected
                {
                    let put_refs: Vec<_> = puts
                        .iter()
                        .map(|(k, v)| (k.as_str(), v.as_slice()))
                        .collect();
                    let delete_refs: Vec<_> = deletes.iter().map(String::as_str).collect();
                    self.metadata
                        .batch_inline(&put_refs, &delete_refs)
                        .map_err(|e| e.to_string())?;
                }
            }

            LogEntryData::ManifestDelete { object_id } => {
                self.metadata
                    .delete_manifest(object_id)
                    .map_err(|error| error.to_string())?;
                debug!(
                    index = entry.index,
                    object_id, "state machine: applied ManifestDelete"
                );
            }

            LogEntryData::ObjectVersionPut { version } => {
                self.metadata
                    .put_object_version(version)
                    .map_err(|error| error.to_string())?;
                debug!(index = entry.index, bucket = %version.bucket, key = %version.key,
                        version_id = %version.version_id, "state machine: applied ObjectVersionPut");
            }

            LogEntryData::ObjectVersionDelete {
                bucket,
                key,
                version_id,
            } => {
                self.metadata
                    .delete_object_version(bucket, key, version_id)
                    .map_err(|error| error.to_string())?;
                debug!(
                    index = entry.index,
                    bucket, key, version_id, "state machine: applied ObjectVersionDelete"
                );
            }

            LogEntryData::InlinePut { key, value } => {
                self.metadata
                    .put_inline(key, value)
                    .map_err(|error| error.to_string())?;
                debug!(
                    index = entry.index,
                    key,
                    len = value.len(),
                    "state machine: applied InlinePut"
                );
            }

            LogEntryData::InlineDelete { key } => {
                self.metadata
                    .delete_inline(key)
                    .map_err(|error| error.to_string())?;
                debug!(
                    index = entry.index,
                    key, "state machine: applied InlineDelete"
                );
            }

            LogEntryData::VolumePut {
                volume_id,
                name,
                size,
                replication,
            } => {
                let meta = nvstor_metadata::VolumeMeta {
                    volume_id: *volume_id.as_bytes(),
                    name: name.clone(),
                    size: *size,
                    chunk_size: nvstor_core::DEFAULT_CHUNK_SIZE as u64,
                    access_mode: 0,
                    consistency: 0,
                    replication: *replication,
                    state: 1, // Ready
                    active_writer: None,
                    append_offset: 0,
                    created_at: 0,
                };
                self.metadata
                    .put_volume(&meta)
                    .map_err(|error| error.to_string())?;
                debug!(index = entry.index, "state machine: applied VolumePut");
            }

            LogEntryData::VolumeDelete { volume_id } => {
                self.metadata
                    .delete_volume(volume_id.as_bytes())
                    .map_err(|error| error.to_string())?;
                debug!(index = entry.index, "state machine: applied VolumeDelete");
            }

            // VFS mutations — applied to MetadataStore inline CF.
            // The VFS layer uses keys: nfs/i/{ino}, nfs/d/{parent}/{name}, etc.
            // On followers, these entries are applied when committed.
            // The VFS will load this state from RocksDB on startup.
            LogEntryData::VfsCreateFile {
                parent_ino,
                ref name,
                mode: _,
            } => {
                debug!(index = entry.index, parent_ino, name = %name, "state machine: VFS create file");
                // The actual file creation (inode allocation, data write) happens on the
                // leader's VFS. Followers replicate the metadata via RocksDB.
                // For now, log the mutation. Full replication requires the leader to
                // include the allocated inode number in the log entry.
            }
            LogEntryData::VfsMkdir {
                parent_ino,
                ref name,
                mode: _,
            } => {
                debug!(index = entry.index, parent_ino, name = %name, "state machine: VFS mkdir");
            }
            LogEntryData::VfsWrite {
                ino,
                offset,
                ref data,
            } => {
                debug!(
                    index = entry.index,
                    ino,
                    offset,
                    len = data.len(),
                    "state machine: VFS write"
                );
            }
            LogEntryData::VfsUnlink {
                parent_ino,
                ref name,
            } => {
                debug!(index = entry.index, parent_ino, name = %name, "state machine: VFS unlink");
            }
            LogEntryData::VfsRmdir {
                parent_ino,
                ref name,
            } => {
                debug!(index = entry.index, parent_ino, name = %name, "state machine: VFS rmdir");
            }
            LogEntryData::VfsTruncate { ino, size } => {
                debug!(
                    index = entry.index,
                    ino, size, "state machine: VFS truncate"
                );
            }
            LogEntryData::VfsRename {
                old_parent,
                ref old_name,
                new_parent: _,
                new_name: ref _new_name,
            } => {
                debug!(index = entry.index, old_parent, old_name = %old_name, "state machine: VFS rename");
            }
            LogEntryData::VfsSymlink {
                parent_ino,
                ref name,
                target: ref _target,
            } => {
                debug!(index = entry.index, parent_ino, name = %name, "state machine: VFS symlink");
            }
            LogEntryData::VfsLink {
                target_ino,
                parent_ino,
                name: ref _name,
            } => {
                debug!(
                    index = entry.index,
                    target_ino, parent_ino, "state machine: VFS link"
                );
            }
        }
        Ok(())
    }
}

/// Null state machine — does nothing. For testing.
pub struct NullStateMachine;

impl StateMachine for NullStateMachine {
    fn apply(&self, entry: &LogEntry) -> Result<(), String> {
        debug!(index = entry.index, "null state machine: entry applied");
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn conditional_inline_batch_is_atomic_and_conflicts_are_applied_noops() {
        let temp = tempfile::TempDir::new().unwrap();
        let metadata = Arc::new(MetadataStore::open(temp.path()).unwrap());
        let machine = MetadataStateMachine::new(metadata.clone());
        let entry = LogEntry {
            index: 1,
            term: 1,
            data: LogEntryData::ConditionalInlineBatch {
                check_key: "kms/key/test".into(),
                expected: None,
                puts: vec![
                    ("kms/key/test".into(), b"v1".to_vec()),
                    ("kms/material/test/v1".into(), b"wrapped".to_vec()),
                ],
                deletes: vec![],
            },
        };
        machine.apply(&entry).unwrap();
        let mut conflicting = entry.clone();
        if let LogEntryData::ConditionalInlineBatch { puts, .. } = &mut conflicting.data {
            puts[1].1 = b"different".to_vec();
        }
        machine.apply(&conflicting).unwrap();
        assert_eq!(
            metadata
                .get_inline("kms/material/test/v1")
                .unwrap()
                .unwrap(),
            b"wrapped"
        );
        machine.apply(&entry).unwrap();
        assert_eq!(metadata.get_inline("kms/key/test").unwrap().unwrap(), b"v1");
    }

    #[test]
    fn complete_manifest_replicates_inline_payload_and_survives_reopen() {
        let dir = tempfile::TempDir::new().unwrap();
        {
            let metadata = Arc::new(MetadataStore::open(dir.path()).unwrap());
            let sm = MetadataStateMachine::new(metadata.clone());
            let manifest = nvstor_metadata::ManifestMeta {
                object_id: "bucket/small".into(),
                size: 7,
                chunks: vec![],
                etag: "etag".into(),
                content_type: "text/plain".into(),
                created_at: 1,
                modified_at: 2,
                inline_data: Some(b"payload".to_vec()),
            };
            let entry = LogEntry {
                term: 1,
                index: 1,
                data: LogEntryData::ManifestPutV2 { manifest },
            };
            let bytes = bincode::serialize(&entry).unwrap();
            sm.apply(&bincode::deserialize(&bytes).unwrap()).unwrap();
        }
        let metadata = MetadataStore::open(dir.path()).unwrap();
        let manifest = metadata.get_manifest("bucket/small").unwrap().unwrap();
        assert_eq!(manifest.inline_data.unwrap(), b"payload");
        assert_eq!(
            metadata.get_inline("bucket/small").unwrap().unwrap(),
            b"payload"
        );
    }

    #[test]
    fn null_state_machine_accepts_all() {
        let sm = NullStateMachine;
        sm.apply(&LogEntry {
            term: 1,
            index: 1,
            data: LogEntryData::Noop,
        })
        .unwrap();
        sm.apply(&LogEntry {
            term: 1,
            index: 2,
            data: LogEntryData::BucketPut {
                name: "test".into(),
                owner: "user".into(),
                region: "us".into(),
                versioning: 0,
            },
        })
        .unwrap();
    }

    #[test]
    fn metadata_state_machine_applies_bucket() {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let metadata = Arc::new(MetadataStore::open(dir.path()).expect("open"));
        let sm = MetadataStateMachine::new(metadata.clone());

        // Apply BucketPut.
        sm.apply(&LogEntry {
            term: 1,
            index: 1,
            data: LogEntryData::BucketPut {
                name: "my-bucket".into(),
                owner: "admin".into(),
                region: "us-east-1".into(),
                versioning: 1,
            },
        })
        .unwrap();

        // Verify bucket exists in RocksDB.
        let bucket = metadata
            .get_bucket("my-bucket")
            .expect("ok")
            .expect("exists");
        assert_eq!(bucket.name, "my-bucket");
        assert_eq!(bucket.owner, "admin");
        assert_eq!(bucket.versioning, 1);

        // Apply BucketDelete.
        sm.apply(&LogEntry {
            term: 1,
            index: 2,
            data: LogEntryData::BucketDelete {
                name: "my-bucket".into(),
            },
        })
        .unwrap();

        assert!(metadata.get_bucket("my-bucket").expect("ok").is_none());
    }

    #[test]
    fn metadata_state_machine_applies_volume() {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let metadata = Arc::new(MetadataStore::open(dir.path()).expect("open"));
        let sm = MetadataStateMachine::new(metadata.clone());

        let vol_id = uuid::Uuid::new_v4();

        sm.apply(&LogEntry {
            term: 1,
            index: 1,
            data: LogEntryData::VolumePut {
                volume_id: vol_id,
                name: "test-vol".into(),
                size: 1024 * 1024 * 1024,
                replication: 3,
            },
        })
        .unwrap();

        let vol = metadata
            .get_volume(vol_id.as_bytes())
            .expect("ok")
            .expect("exists");
        assert_eq!(vol.name, "test-vol");
        assert_eq!(vol.replication, 3);

        sm.apply(&LogEntry {
            term: 1,
            index: 2,
            data: LogEntryData::VolumeDelete { volume_id: vol_id },
        })
        .unwrap();

        assert!(metadata
            .get_volume(vol_id.as_bytes())
            .expect("ok")
            .is_none());
    }

    #[test]
    fn metadata_state_machine_applies_manifest() {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let metadata = Arc::new(MetadataStore::open(dir.path()).expect("open"));
        let sm = MetadataStateMachine::new(metadata.clone());

        sm.apply(&LogEntry {
            term: 1,
            index: 1,
            data: LogEntryData::ManifestPut {
                object_id: "bucket/key".into(),
                size: 4096,
                etag: "abc123".into(),
                content_type: "text/plain".into(),
                chunks: vec![nvstor_metadata::ChunkRef {
                    chunk_id: [9u8; 32],
                    length: 4096,
                    checksum: 0x1234_5678,
                    logical_start: 0,
                    logical_end: 4096,
                    nodes: vec![1],
                }],
                created_at: 1000,
                modified_at: 2000,
            },
        })
        .unwrap();

        let manifest = metadata
            .get_manifest("bucket/key")
            .expect("ok")
            .expect("exists");
        assert_eq!(manifest.size, 4096);
        assert_eq!(manifest.etag, "abc123");
        // The chunk refs replicate through the log entry now.
        assert_eq!(manifest.chunks.len(), 1);
        assert_eq!(manifest.chunks[0].length, 4096);
        assert_eq!(manifest.created_at, 1000);

        sm.apply(&LogEntry {
            term: 1,
            index: 2,
            data: LogEntryData::ManifestDelete {
                object_id: "bucket/key".into(),
            },
        })
        .unwrap();

        assert!(metadata.get_manifest("bucket/key").expect("ok").is_none());
    }
}

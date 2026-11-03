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

//! Raft log — persistent storage for log entries and Raft state.
//!
//! Uses RocksDB (via nvstor-metadata's MetadataStore or a dedicated instance)
//! for crash-safe persistence of:
//! - current_term and voted_for (Raft persistent state)
//! - Log entries (indexed by log index)
//!
//! Key schema in RocksDB:
//!   "raft/term"         → u64 LE
//!   "raft/voted_for"    → i64 LE
//!   "raft/log/{index}"  → LogEntry (bincode)

use std::path::Path;
use std::sync::Arc;

use rocksdb::{ColumnFamilyDescriptor, DBWithThreadMode, MultiThreaded, Options, WriteOptions};
use tracing::{debug, warn};

use crate::types::*;

const CF_RAFT: &str = "raft";

const KEY_TERM: &[u8] = b"raft/term";
const KEY_VOTED_FOR: &[u8] = b"raft/voted_for";

type DB = DBWithThreadMode<MultiThreaded>;

/// Persistent Raft log backed by RocksDB.
pub struct RaftLog {
    db: Arc<DB>,
    /// In-memory cache of log entries for fast access.
    /// The DB is the source of truth; this is a read cache.
    entries: Vec<LogEntry>,
}

impl RaftLog {
    /// Open or create a Raft log at the given RocksDB path.
    pub fn open(path: &Path) -> Result<Self, String> {
        let mut db_opts = Options::default();
        db_opts.create_if_missing(true);
        db_opts.create_missing_column_families(true);

        let cf = ColumnFamilyDescriptor::new(CF_RAFT, Options::default());
        let cf_default = ColumnFamilyDescriptor::new("default", Options::default());

        let db = DB::open_cf_descriptors(&db_opts, path, vec![cf_default, cf])
            .map_err(|e| format!("raft log open: {e}"))?;

        let db = Arc::new(db);
        let mut log = RaftLog {
            db,
            entries: Vec::new(),
        };

        // Load existing entries into memory cache.
        log.load_entries()?;

        Ok(log)
    }

    // ---- Persistent state ----

    /// Read current_term from RocksDB.
    pub fn get_term(&self) -> Term {
        self.db
            .get(KEY_TERM)
            .ok()
            .flatten()
            .map(|v| {
                if v.len() >= 8 {
                    u64::from_le_bytes(v[..8].try_into().expect("8 bytes"))
                } else {
                    0
                }
            })
            .unwrap_or(0)
    }

    /// Persist current_term to RocksDB.
    pub fn set_term(&self, term: Term) -> Result<(), String> {
        self.db
            .put_opt(KEY_TERM, term.to_le_bytes(), &sync_opts())
            .map_err(|e| format!("set term: {e}"))
    }

    /// Read voted_for from RocksDB.
    pub fn get_voted_for(&self) -> i64 {
        self.db
            .get(KEY_VOTED_FOR)
            .ok()
            .flatten()
            .map(|v| {
                if v.len() >= 8 {
                    i64::from_le_bytes(v[..8].try_into().expect("8 bytes"))
                } else {
                    -1
                }
            })
            .unwrap_or(-1)
    }

    /// Persist voted_for to RocksDB.
    pub fn set_voted_for(&self, voted_for: i64) -> Result<(), String> {
        self.db
            .put_opt(KEY_VOTED_FOR, voted_for.to_le_bytes(), &sync_opts())
            .map_err(|e| format!("set voted_for: {e}"))
    }

    // ---- Log entries ----

    /// Number of entries in the log.
    pub fn len(&self) -> u64 {
        self.entries.len() as u64
    }

    /// Is the log empty?
    pub fn is_empty(&self) -> bool {
        self.entries.is_empty()
    }

    /// Get the last log index (0 if empty).
    pub fn last_index(&self) -> LogIndex {
        self.entries.last().map(|e| e.index).unwrap_or(0)
    }

    /// Get the last log term (0 if empty).
    pub fn last_term(&self) -> Term {
        self.entries.last().map(|e| e.term).unwrap_or(0)
    }

    /// Get a log entry by index.
    pub fn get(&self, index: LogIndex) -> Option<&LogEntry> {
        if index == 0 || self.entries.is_empty() {
            return None;
        }
        let first = self.entries[0].index;
        if index < first || index > self.last_index() {
            return None;
        }
        self.entries.get((index - first) as usize)
    }

    /// Get the term of a log entry by index.
    pub fn term_at(&self, index: LogIndex) -> Term {
        self.get(index).map(|e| e.term).unwrap_or(0)
    }

    /// Get entries from `start` (inclusive) to end of log.
    pub fn entries_from(&self, start: LogIndex) -> &[LogEntry] {
        if start == 0 || self.entries.is_empty() {
            return &[];
        }
        let first = self.entries[0].index;
        if start < first {
            return &self.entries;
        }
        let offset = (start - first) as usize;
        if offset >= self.entries.len() {
            return &[];
        }
        &self.entries[offset..]
    }

    /// Append a new entry to the log. Persists to RocksDB.
    pub fn append(&mut self, entry: LogEntry) -> Result<(), String> {
        let key = log_key(entry.index);
        let value = bincode::serialize(&entry).map_err(|e| format!("serialize entry: {e}"))?;

        self.db
            .put_opt(key.as_bytes(), &value, &sync_opts())
            .map_err(|e| format!("persist entry: {e}"))?;

        self.entries.push(entry);
        Ok(())
    }

    /// Truncate the log from `from_index` onwards (inclusive).
    /// Used when a new leader sends conflicting entries.
    pub fn truncate_from(&mut self, from_index: LogIndex) -> Result<(), String> {
        if from_index == 0 || self.entries.is_empty() {
            return Ok(());
        }

        let first = self.entries[0].index;
        if from_index < first {
            return Ok(());
        }

        // Delete from RocksDB.
        let last = self.last_index();
        for idx in from_index..=last {
            let key = log_key(idx);
            if let Err(e) = self.db.delete_opt(key.as_bytes(), &sync_opts()) {
                warn!(index = idx, error = %e, "failed to delete log entry from db");
            }
        }

        // Truncate in-memory cache.
        let offset = (from_index - first) as usize;
        self.entries.truncate(offset);

        debug!(from_index, "log truncated");
        Ok(())
    }

    /// Load all log entries from RocksDB into memory.
    fn load_entries(&mut self) -> Result<(), String> {
        let prefix = "raft/log/";

        // Scan all keys with the prefix.
        let iter = self.db.prefix_iterator(prefix.as_bytes());
        let mut entries = Vec::new();

        for item in iter {
            let (key, value) = item.map_err(|e| format!("iterate: {e}"))?;
            let key_str = std::str::from_utf8(&key).map_err(|e| format!("key decode: {e}"))?;

            if !key_str.starts_with(prefix) {
                break;
            }

            match bincode::deserialize::<LogEntry>(&value) {
                Ok(entry) => entries.push(entry),
                Err(e) => {
                    warn!(key = %key_str, error = %e, "skipping corrupt log entry");
                }
            }
        }

        // Sort by index (should already be sorted, but ensure).
        entries.sort_by_key(|e| e.index);

        debug!(count = entries.len(), "loaded raft log entries");
        self.entries = entries;
        Ok(())
    }
}

fn log_key(index: LogIndex) -> String {
    format!("raft/log/{index:020}")
}

fn sync_opts() -> WriteOptions {
    let mut opts = WriteOptions::default();
    opts.set_sync(true);
    opts
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tmp_log() -> (tempfile::TempDir, RaftLog) {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let log = RaftLog::open(dir.path()).expect("open log");
        (dir, log)
    }

    #[test]
    fn empty_log() {
        let (_dir, log) = tmp_log();
        assert!(log.is_empty());
        assert_eq!(log.len(), 0);
        assert_eq!(log.last_index(), 0);
        assert_eq!(log.last_term(), 0);
        assert!(log.get(1).is_none());
    }

    #[test]
    fn append_and_get() {
        let (_dir, mut log) = tmp_log();

        log.append(LogEntry {
            term: 1,
            index: 1,
            data: LogEntryData::Noop,
        })
        .expect("append");

        log.append(LogEntry {
            term: 1,
            index: 2,
            data: LogEntryData::BucketPut {
                name: "test".into(),
                owner: "user".into(),
                region: "us".into(),
                versioning: 0,
            },
        })
        .expect("append");

        assert_eq!(log.len(), 2);
        assert_eq!(log.last_index(), 2);
        assert_eq!(log.last_term(), 1);

        let e1 = log.get(1).expect("entry 1");
        assert_eq!(e1.term, 1);
        assert_eq!(e1.data, LogEntryData::Noop);

        let e2 = log.get(2).expect("entry 2");
        assert_eq!(e2.index, 2);
    }

    #[test]
    fn term_at() {
        let (_dir, mut log) = tmp_log();
        log.append(LogEntry {
            term: 1,
            index: 1,
            data: LogEntryData::Noop,
        })
        .expect("ok");
        log.append(LogEntry {
            term: 2,
            index: 2,
            data: LogEntryData::Noop,
        })
        .expect("ok");
        log.append(LogEntry {
            term: 2,
            index: 3,
            data: LogEntryData::Noop,
        })
        .expect("ok");

        assert_eq!(log.term_at(0), 0);
        assert_eq!(log.term_at(1), 1);
        assert_eq!(log.term_at(2), 2);
        assert_eq!(log.term_at(3), 2);
        assert_eq!(log.term_at(4), 0);
    }

    #[test]
    fn entries_from() {
        let (_dir, mut log) = tmp_log();
        for i in 1..=5 {
            log.append(LogEntry {
                term: 1,
                index: i,
                data: LogEntryData::Noop,
            })
            .expect("ok");
        }

        let from_3 = log.entries_from(3);
        assert_eq!(from_3.len(), 3);
        assert_eq!(from_3[0].index, 3);
        assert_eq!(from_3[2].index, 5);

        let from_1 = log.entries_from(1);
        assert_eq!(from_1.len(), 5);

        let from_6 = log.entries_from(6);
        assert_eq!(from_6.len(), 0);
    }

    #[test]
    fn truncate() {
        let (_dir, mut log) = tmp_log();
        for i in 1..=5 {
            log.append(LogEntry {
                term: 1,
                index: i,
                data: LogEntryData::Noop,
            })
            .expect("ok");
        }

        log.truncate_from(3).expect("truncate");
        assert_eq!(log.len(), 2);
        assert_eq!(log.last_index(), 2);
        assert!(log.get(3).is_none());
    }

    #[test]
    fn persistent_state() {
        let (_dir, log) = tmp_log();

        // Defaults.
        assert_eq!(log.get_term(), 0);
        assert_eq!(log.get_voted_for(), -1);

        // Set and read back.
        log.set_term(5).expect("set term");
        log.set_voted_for(2).expect("set voted_for");
        assert_eq!(log.get_term(), 5);
        assert_eq!(log.get_voted_for(), 2);
    }

    #[test]
    fn persistence_across_restart() {
        let dir = tempfile::TempDir::new().expect("tempdir");

        // Write data.
        {
            let mut log = RaftLog::open(dir.path()).expect("open");
            log.set_term(10).expect("set term");
            log.set_voted_for(3).expect("set voted_for");
            log.append(LogEntry {
                term: 10,
                index: 1,
                data: LogEntryData::Noop,
            })
            .expect("ok");
            log.append(LogEntry {
                term: 10,
                index: 2,
                data: LogEntryData::BucketPut {
                    name: "durable".into(),
                    owner: "test".into(),
                    region: "us".into(),
                    versioning: 1,
                },
            })
            .expect("ok");
        }

        // Reopen and verify.
        {
            let log = RaftLog::open(dir.path()).expect("reopen");
            assert_eq!(log.get_term(), 10);
            assert_eq!(log.get_voted_for(), 3);
            assert_eq!(log.len(), 2);
            assert_eq!(log.last_index(), 2);

            let e2 = log.get(2).expect("entry 2");
            assert_eq!(e2.term, 10);
            match &e2.data {
                LogEntryData::BucketPut { name, .. } => assert_eq!(name, "durable"),
                _ => panic!("wrong entry type"),
            }
        }
    }

    #[test]
    fn truncate_persists() {
        let dir = tempfile::TempDir::new().expect("tempdir");

        {
            let mut log = RaftLog::open(dir.path()).expect("open");
            for i in 1..=5 {
                log.append(LogEntry {
                    term: 1,
                    index: i,
                    data: LogEntryData::Noop,
                })
                .expect("ok");
            }
            log.truncate_from(3).expect("truncate");
        }

        {
            let log = RaftLog::open(dir.path()).expect("reopen");
            assert_eq!(log.len(), 2);
            assert_eq!(log.last_index(), 2);
        }
    }

    #[test]
    fn many_entries() {
        let (_dir, mut log) = tmp_log();
        for i in 1..=1000 {
            log.append(LogEntry {
                term: 1,
                index: i,
                data: LogEntryData::Noop,
            })
            .expect("ok");
        }
        assert_eq!(log.len(), 1000);
        assert_eq!(log.last_index(), 1000);
        assert_eq!(log.get(500).expect("exists").index, 500);
    }
}

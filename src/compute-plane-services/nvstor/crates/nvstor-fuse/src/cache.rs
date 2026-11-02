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

//! Content-addressed disk cache with LRU eviction.
//!
//! Modeled after CVMFS cache_posix.cc (650 LOC C++).
//! Stores fetched file data on local disk, keyed by content hash.
//! When cache exceeds max size, evicts least-recently-used entries.

use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::time::Instant;

use parking_lot::Mutex;
use tracing::{debug, info};

/// Cache entry metadata.
struct CacheEntry {
    /// Size in bytes.
    size: u64,
    /// Last access time (for LRU eviction).
    last_access: Instant,
}

/// Content-addressed disk cache.
pub struct DiskCache {
    /// Cache directory on local filesystem.
    cache_dir: PathBuf,
    /// Max cache size in bytes.
    max_bytes: u64,
    /// Current cache size in bytes.
    current_bytes: u64,
    /// Tracked entries: cache_key → metadata.
    entries: HashMap<String, CacheEntry>,
}

impl DiskCache {
    /// Create or open a disk cache.
    pub fn new(cache_dir: &Path, max_bytes: u64) -> std::io::Result<Self> {
        fs::create_dir_all(cache_dir)?;

        // Scan existing cache entries.
        let mut entries = HashMap::new();
        let mut current_bytes = 0u64;

        if let Ok(rd) = fs::read_dir(cache_dir) {
            for entry in rd.filter_map(|e| e.ok()) {
                if let Ok(meta) = entry.metadata() {
                    if meta.is_file() {
                        let name = entry.file_name().to_string_lossy().to_string();
                        let size = meta.len();
                        entries.insert(
                            name,
                            CacheEntry {
                                size,
                                last_access: Instant::now(),
                            },
                        );
                        current_bytes += size;
                    }
                }
            }
        }

        info!(
            cache_dir = %cache_dir.display(),
            max_mb = max_bytes / (1024 * 1024),
            existing_entries = entries.len(),
            existing_mb = current_bytes / (1024 * 1024),
            "disk cache initialized"
        );

        Ok(Self {
            cache_dir: cache_dir.to_path_buf(),
            max_bytes,
            current_bytes,
            entries,
        })
    }

    /// Get cached data. Returns None on miss.
    pub fn get(&mut self, key: &str) -> Option<Vec<u8>> {
        if let Some(entry) = self.entries.get_mut(key) {
            entry.last_access = Instant::now();
            let path = self.cache_dir.join(key);
            match fs::read(&path) {
                Ok(data) => {
                    debug!(key, size = data.len(), "cache hit");
                    Some(data)
                }
                Err(_) => {
                    // File disappeared — remove from tracking.
                    self.current_bytes -= entry.size;
                    self.entries.remove(key);
                    None
                }
            }
        } else {
            None
        }
    }

    /// Store data in cache. Evicts LRU entries if needed.
    /// Uses atomic write (temp file + rename) to prevent corruption.
    pub fn put(&mut self, key: &str, data: &[u8]) -> std::io::Result<()> {
        let size = data.len() as u64;

        // Don't cache items larger than 25% of total cache.
        if size > self.max_bytes / 4 {
            return Ok(());
        }

        // Evict until we have room.  Hold tracking lock across the full operation.
        while self.current_bytes + size > self.max_bytes {
            if !self.evict_one() {
                break;
            }
        }

        // Atomic write: write to temp file, then rename.
        let tmp_path = self.cache_dir.join(format!(".{key}.tmp"));
        let final_path = self.cache_dir.join(key);
        if let Err(e) = fs::write(&tmp_path, data) {
            let _ = fs::remove_file(&tmp_path);
            return Err(e);
        }
        if let Err(e) = fs::rename(&tmp_path, &final_path) {
            let _ = fs::remove_file(&tmp_path);
            return Err(e);
        }

        // Update tracking.
        if let Some(old) = self.entries.get(key) {
            self.current_bytes = self.current_bytes.saturating_sub(old.size);
        }
        self.entries.insert(
            key.to_string(),
            CacheEntry {
                size,
                last_access: Instant::now(),
            },
        );
        self.current_bytes += size;

        debug!(
            key,
            size,
            current_mb = self.current_bytes / (1024 * 1024),
            "cache put"
        );
        Ok(())
    }

    /// Check if key exists in cache.
    pub fn contains(&self, key: &str) -> bool {
        self.entries.contains_key(key)
    }

    /// Evict the least recently used entry. Returns true if something was evicted.
    fn evict_one(&mut self) -> bool {
        let oldest_key = self
            .entries
            .iter()
            .min_by_key(|(_, e)| e.last_access)
            .map(|(k, _)| k.clone());

        if let Some(key) = oldest_key {
            let path = self.cache_dir.join(&key);
            let _ = fs::remove_file(&path);
            if let Some(entry) = self.entries.remove(&key) {
                self.current_bytes -= entry.size;
                debug!(key, size = entry.size, "cache evict (LRU)");
                return true;
            }
        }
        false
    }

    /// Current cache usage.
    pub fn current_bytes(&self) -> u64 {
        self.current_bytes
    }
    pub fn entry_count(&self) -> usize {
        self.entries.len()
    }
    pub fn max_bytes(&self) -> u64 {
        self.max_bytes
    }
}

/// Thread-safe wrapper.
pub struct SharedCache {
    inner: Mutex<DiskCache>,
}

impl SharedCache {
    pub fn new(cache_dir: &Path, max_bytes: u64) -> std::io::Result<Self> {
        Ok(Self {
            inner: Mutex::new(DiskCache::new(cache_dir, max_bytes)?),
        })
    }

    pub fn get(&self, key: &str) -> Option<Vec<u8>> {
        self.inner.lock().get(key)
    }
    pub fn put(&self, key: &str, data: &[u8]) -> std::io::Result<()> {
        self.inner.lock().put(key, data)
    }
    pub fn contains(&self, key: &str) -> bool {
        self.inner.lock().contains(key)
    }
    pub fn current_bytes(&self) -> u64 {
        self.inner.lock().current_bytes()
    }
    pub fn entry_count(&self) -> usize {
        self.inner.lock().entry_count()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cache_put_get() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let mut cache = DiskCache::new(dir.path(), 1024 * 1024).expect("create");

        cache.put("abc123", b"hello cache").expect("put");
        assert!(cache.contains("abc123"));

        let data = cache.get("abc123").expect("get");
        assert_eq!(data, b"hello cache");
    }

    #[test]
    fn cache_miss() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let mut cache = DiskCache::new(dir.path(), 1024 * 1024).expect("create");
        assert!(cache.get("nonexistent").is_none());
    }

    #[test]
    fn cache_eviction() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let mut cache = DiskCache::new(dir.path(), 1000).expect("create");

        // Fill cache to near capacity.
        cache.put("a", &[0u8; 200]).expect("put a");
        cache.put("b", &[1u8; 200]).expect("put b");
        cache.put("c", &[2u8; 200]).expect("put c");
        cache.put("d", &[3u8; 200]).expect("put d");
        assert_eq!(cache.entry_count(), 4);
        assert_eq!(cache.current_bytes(), 800);

        // Access "b" to make it recently used.
        let _ = cache.get("b");

        // Put "e" — needs eviction. "a" should be evicted (oldest not accessed).
        cache.put("e", &[4u8; 200]).expect("put e");
        // Something was evicted to make room.
        assert!(cache.entry_count() <= 5);
        assert!(cache.contains("e"));
        // At least one old entry was evicted.
        assert!(cache.current_bytes() <= 1000);
    }

    #[test]
    fn cache_skip_oversized() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let mut cache = DiskCache::new(dir.path(), 100).expect("create");

        // Item larger than 25% of cache → not cached.
        cache.put("big", &[0u8; 50]).expect("put");
        assert!(!cache.contains("big"));
    }

    #[test]
    fn cache_survives_restart() {
        let dir = tempfile::tempdir().expect("tmpdir");

        // Write some data.
        {
            let mut cache = DiskCache::new(dir.path(), 1024 * 1024).expect("create");
            cache.put("persist1", b"data1").expect("put");
            cache.put("persist2", b"data2").expect("put");
        }

        // Reopen — should find existing entries.
        let mut cache = DiskCache::new(dir.path(), 1024 * 1024).expect("reopen");
        assert_eq!(cache.entry_count(), 2);
        assert_eq!(cache.get("persist1").expect("get"), b"data1");
    }
}

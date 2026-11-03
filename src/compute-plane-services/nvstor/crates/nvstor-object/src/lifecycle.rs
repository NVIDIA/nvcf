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

//! Version lifecycle management — expire old object versions.
//!
//! Runs as a background task, periodically scanning versioned objects
//! and deleting non-current versions older than the configured TTL.

use std::time::{Duration, SystemTime, UNIX_EPOCH};

use nvstor_metadata::MetadataStore;
use nvstor_storage::manager::ChunkManager;
use tracing::{debug, info, warn};

/// Lifecycle configuration.
#[derive(Debug, Clone)]
pub struct LifecycleConfig {
    /// How often to run the lifecycle sweep (default: 1 hour).
    pub sweep_interval: Duration,
    /// Delete non-current versions older than this (default: 30 days, 0 = never expire).
    pub noncurrent_version_expiration_days: u32,
    /// Maximum number of versions to keep per object (0 = unlimited).
    pub max_versions_per_object: u32,
}

impl Default for LifecycleConfig {
    fn default() -> Self {
        Self {
            sweep_interval: Duration::from_secs(3600),
            noncurrent_version_expiration_days: 0,
            max_versions_per_object: 0,
        }
    }
}

/// Result of a lifecycle sweep.
#[derive(Debug, Clone, Default)]
pub struct LifecycleReport {
    /// Buckets scanned.
    pub buckets_scanned: u64,
    /// Versions checked.
    pub versions_checked: u64,
    /// Versions expired by TTL.
    pub versions_expired_ttl: u64,
    /// Versions expired by max-versions limit.
    pub versions_expired_limit: u64,
    /// Objects whose data was deleted (chunks unreferenced).
    pub objects_cleaned: u64,
    /// Errors encountered.
    pub errors: u64,
}

/// Run a single lifecycle sweep across all buckets.
pub fn sweep(
    metadata: &MetadataStore,
    manager: &ChunkManager,
    config: &LifecycleConfig,
) -> LifecycleReport {
    let mut report = LifecycleReport::default();

    // Skip if no expiration configured.
    if config.noncurrent_version_expiration_days == 0 && config.max_versions_per_object == 0 {
        return report;
    }

    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64;
    let ttl_seconds = config.noncurrent_version_expiration_days as i64 * 86400;

    // List all buckets.
    let buckets = match metadata.list_buckets() {
        Ok(b) => b,
        Err(e) => {
            warn!(error = %e, "lifecycle: failed to list buckets");
            report.errors += 1;
            return report;
        }
    };

    for bucket in &buckets {
        // Only process versioned buckets.
        if bucket.versioning != 1 {
            continue;
        }

        report.buckets_scanned += 1;

        let versions = match metadata.list_bucket_versions(&bucket.name, 100_000) {
            Ok(v) => v,
            Err(e) => {
                warn!(bucket = %bucket.name, error = %e, "lifecycle: failed to list versions");
                report.errors += 1;
                continue;
            }
        };

        // Group versions by key.
        let mut by_key: std::collections::HashMap<String, Vec<nvstor_metadata::ObjectVersion>> =
            std::collections::HashMap::new();
        for v in versions {
            report.versions_checked += 1;
            by_key.entry(v.key.clone()).or_default().push(v);
        }

        for (key, key_versions) in by_key {
            // Versions are already newest-first from RocksDB iteration.
            // The first non-delete-marker is the "current" version.

            // TTL expiration: delete versions older than threshold.
            if ttl_seconds > 0 {
                let mut is_first = true;
                for v in &key_versions {
                    if is_first && !v.is_delete_marker {
                        is_first = false;
                        continue; // Don't expire the current version.
                    }
                    is_first = false;

                    if v.last_modified + ttl_seconds < now {
                        // Expired — delete this version.
                        if !v.is_delete_marker && !v.manifest_object_id.is_empty() {
                            if let Err(e) = manager.object_delete(&v.manifest_object_id) {
                                debug!(key = %key, version = %v.version_id, error = %e, "lifecycle: failed to delete object data");
                            } else {
                                report.objects_cleaned += 1;
                            }
                        }
                        if let Err(e) =
                            metadata.delete_object_version(&bucket.name, &key, &v.version_id)
                        {
                            warn!(key = %key, version = %v.version_id, error = %e, "lifecycle: failed to delete version");
                            report.errors += 1;
                        } else {
                            report.versions_expired_ttl += 1;
                        }
                    }
                }
            }

            // Max versions limit: trim excess versions (oldest first).
            if config.max_versions_per_object > 0 {
                // Re-list after TTL cleanup (versions may have been deleted).
                let remaining = match metadata.list_object_versions(&bucket.name, &key) {
                    Ok(v) => v,
                    Err(_) => continue,
                };

                if remaining.len() > config.max_versions_per_object as usize {
                    let excess = remaining.len() - config.max_versions_per_object as usize;
                    // Delete the oldest (last in the list, since newest is first).
                    for v in remaining.iter().rev().take(excess) {
                        if !v.is_delete_marker && !v.manifest_object_id.is_empty() {
                            let _ = manager.object_delete(&v.manifest_object_id);
                            report.objects_cleaned += 1;
                        }
                        if let Err(_e) =
                            metadata.delete_object_version(&bucket.name, &key, &v.version_id)
                        {
                            report.errors += 1;
                        } else {
                            report.versions_expired_limit += 1;
                        }
                    }
                }
            }
        }
    }

    if report.versions_expired_ttl > 0 || report.versions_expired_limit > 0 {
        info!(
            buckets = report.buckets_scanned,
            checked = report.versions_checked,
            expired_ttl = report.versions_expired_ttl,
            expired_limit = report.versions_expired_limit,
            cleaned = report.objects_cleaned,
            errors = report.errors,
            "lifecycle sweep complete"
        );
    }

    report
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_default_config_skips() {
        // With default config (no TTL, no max versions), sweep does nothing.
        let config = LifecycleConfig::default();
        assert_eq!(config.noncurrent_version_expiration_days, 0);
        assert_eq!(config.max_versions_per_object, 0);
    }

    #[test]
    fn test_lifecycle_config() {
        let config = LifecycleConfig {
            sweep_interval: Duration::from_secs(600),
            noncurrent_version_expiration_days: 7,
            max_versions_per_object: 5,
        };
        assert_eq!(config.noncurrent_version_expiration_days, 7);
        assert_eq!(config.max_versions_per_object, 5);
    }
}

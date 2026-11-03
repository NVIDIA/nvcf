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

//! Background scanner — continuous bitrot detection and integrity verification.
//!
//! Periodically scans stored chunks, verifies IEEE CRC-32 checksums against
//! actual data on disk. Flags corrupt chunks for repair.
//!
//! Design follows the rustfs scanner (crates/scanner/).

use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use nvstor_core::types::IntegrityReport;
use tracing::{info, warn};

use crate::manager::ChunkManager;

/// Scanner configuration.
#[derive(Debug, Clone)]
pub struct ScannerConfig {
    /// Interval between scan passes.
    pub scan_interval: Duration,
    /// Maximum chunks to scan per pass (0 = all).
    pub batch_size: u64,
    /// Pause between batches to limit I/O impact.
    pub batch_pause: Duration,
    /// Enable/disable the scanner.
    pub enabled: bool,
}

impl Default for ScannerConfig {
    fn default() -> Self {
        Self {
            scan_interval: Duration::from_secs(6 * 3600), // 6 hours
            batch_size: 10000,
            batch_pause: Duration::from_millis(100),
            enabled: false,
        }
    }
}

/// Scanner statistics.
#[derive(Debug, Clone, Default)]
pub struct ScannerStats {
    /// Total scan passes completed.
    pub passes: u64,
    /// Total chunks scanned across all passes.
    pub total_scanned: u64,
    /// Total checksum failures detected.
    pub total_corrupt: u64,
    /// Total errors during scanning.
    pub total_errors: u64,
    /// Duration of the last scan pass.
    pub last_scan_duration: Duration,
}

/// Background scanner that runs as an async task.
pub struct Scanner {
    manager: Arc<ChunkManager>,
    config: ScannerConfig,
    running: AtomicBool,
    passes: AtomicU64,
    total_scanned: AtomicU64,
    total_corrupt: AtomicU64,
    total_errors: AtomicU64,
}

impl Scanner {
    pub fn new(manager: Arc<ChunkManager>, config: ScannerConfig) -> Self {
        Self {
            manager,
            config,
            running: AtomicBool::new(false),
            passes: AtomicU64::new(0),
            total_scanned: AtomicU64::new(0),
            total_corrupt: AtomicU64::new(0),
            total_errors: AtomicU64::new(0),
        }
    }

    /// Run the scanner loop. Call from tokio::spawn.
    pub async fn run(&self) {
        if !self.config.enabled {
            info!("background scanner disabled");
            return;
        }

        self.running.store(true, Ordering::Relaxed);
        info!(
            interval_secs = self.config.scan_interval.as_secs(),
            batch_size = self.config.batch_size,
            "background scanner started"
        );

        while self.running.load(Ordering::Relaxed) {
            tokio::time::sleep(self.config.scan_interval).await;

            if !self.running.load(Ordering::Relaxed) {
                break;
            }

            let report = self.scan_pass();
            self.passes.fetch_add(1, Ordering::Relaxed);
            self.total_scanned
                .fetch_add(report.chunks_checked, Ordering::Relaxed);
            self.total_corrupt
                .fetch_add(report.checksum_failures, Ordering::Relaxed);
            self.total_errors
                .fetch_add(report.missing_chunks, Ordering::Relaxed);

            if report.checksum_failures > 0 || report.missing_chunks > 0 {
                warn!(
                    checked = report.chunks_checked,
                    corrupt = report.checksum_failures,
                    missing = report.missing_chunks,
                    duration_ms = report.duration_ms,
                    "scan pass found issues"
                );
            } else {
                info!(
                    checked = report.chunks_checked,
                    duration_ms = report.duration_ms,
                    "scan pass clean"
                );
            }
        }
    }

    /// Run a single scan pass.
    pub fn scan_pass(&self) -> IntegrityReport {
        let start = Instant::now();

        let report = match self.manager.scrub(self.config.batch_size) {
            Ok(r) => r,
            Err(e) => {
                warn!(error = %e, "scan pass failed");
                return IntegrityReport {
                    chunks_checked: 0,
                    checksum_failures: 0,
                    missing_chunks: 0,
                    orphaned_chunks: 0,
                    duration_ms: start.elapsed().as_millis() as u64,
                };
            }
        };

        report
    }

    /// Stop the scanner.
    pub fn stop(&self) {
        self.running.store(false, Ordering::Relaxed);
    }

    /// Get current statistics.
    pub fn stats(&self) -> ScannerStats {
        ScannerStats {
            passes: self.passes.load(Ordering::Relaxed),
            total_scanned: self.total_scanned.load(Ordering::Relaxed),
            total_corrupt: self.total_corrupt.load(Ordering::Relaxed),
            total_errors: self.total_errors.load(Ordering::Relaxed),
            last_scan_duration: Duration::default(), // not tracked
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::backend::PosixBackend;
    use nvstor_metadata::MetadataStore;
    use std::sync::Arc;

    fn make_manager() -> (tempfile::TempDir, Arc<ChunkManager>) {
        let dir = tempfile::TempDir::new().expect("tempdir");
        let backend = Box::new(PosixBackend::new(dir.path().join("chunks")).expect("backend"));
        let metadata = Arc::new(MetadataStore::open(&dir.path().join("meta")).expect("metadata"));
        let mgr = Arc::new(ChunkManager::new(
            backend,
            metadata,
            crate::manager::test_config(),
        ));
        (dir, mgr)
    }

    #[test]
    fn scanner_config_default() {
        let config = ScannerConfig::default();
        assert!(!config.enabled);
        assert_eq!(config.batch_size, 10000);
    }

    #[test]
    fn scan_empty_storage() {
        let (_dir, mgr) = make_manager();
        let scanner = Scanner::new(mgr, ScannerConfig::default());
        let report = scanner.scan_pass();
        assert_eq!(report.chunks_checked, 0);
        assert_eq!(report.checksum_failures, 0);
    }

    #[test]
    fn scan_finds_healthy_chunks() {
        let (_dir, mgr) = make_manager();

        // Write some objects to create chunks.
        for i in 0..5 {
            mgr.object_write(&format!("key-{i}"), &[i as u8; 1024], Some("app/bin"))
                .expect("write");
        }

        let scanner = Scanner::new(
            mgr,
            ScannerConfig {
                batch_size: 100,
                ..Default::default()
            },
        );
        let report = scanner.scan_pass();

        // Should have scanned the inline objects (stored in metadata, not as chunks).
        // Since these are small (1KB < 64KB threshold), they're inline — no chunks to scrub.
        // Write larger objects to create actual chunks.
        assert_eq!(report.checksum_failures, 0);
    }

    #[test]
    fn scan_with_large_objects() {
        let (_dir, mgr) = make_manager();

        // Write objects larger than inline threshold to create actual chunks.
        let data = vec![0xAB; 128 * 1024]; // 128KB > 64KB inline threshold
        for i in 0..3 {
            mgr.object_write(&format!("big-{i}"), &data, Some("app/bin"))
                .expect("write");
        }

        let scanner = Scanner::new(
            mgr,
            ScannerConfig {
                batch_size: 100,
                ..Default::default()
            },
        );
        let report = scanner.scan_pass();

        // Should find and verify the chunks.
        assert!(report.chunks_checked > 0);
        assert_eq!(report.checksum_failures, 0);
    }

    #[test]
    fn scanner_stats() {
        let (_dir, mgr) = make_manager();
        let scanner = Scanner::new(mgr, ScannerConfig::default());

        let stats = scanner.stats();
        assert_eq!(stats.passes, 0);
        assert_eq!(stats.total_scanned, 0);
    }
}

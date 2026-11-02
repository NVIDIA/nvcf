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

//! Prometheus metrics for all nvstor subsystems.
//!
//! Covers: S3 gateway, storage engine, NBD, transport, WAL, GC.
//! Exposed via /metrics endpoint on the health port.

use std::sync::Arc;
use std::time::Instant;

use axum::body::Body;
use axum::http::Request;
use axum::response::Response;
use prometheus::{
    Encoder, HistogramOpts, HistogramVec, IntCounter, IntCounterVec, IntGauge, IntGaugeVec, Opts,
    Registry, TextEncoder,
};

/// All nvstor metrics in one place.
#[derive(Clone)]
#[allow(dead_code)]
pub struct Metrics {
    pub registry: Registry,

    // S3 HTTP gateway
    pub s3_requests_total: IntCounterVec,
    pub s3_request_duration: HistogramVec,
    pub s3_request_size_bytes: HistogramVec,
    pub s3_response_size_bytes: HistogramVec,

    // Storage engine
    pub storage_chunks_total: IntGauge,
    pub storage_objects_total: IntGauge,
    pub storage_volumes_total: IntGauge,
    pub storage_bytes_total: IntGauge,
    pub storage_unique_bytes: IntGauge,
    pub chunk_write_duration: HistogramVec,
    pub chunk_read_duration: HistogramVec,

    // GC
    pub gc_runs_total: IntCounter,
    pub gc_chunks_deleted: IntCounter,
    pub gc_bytes_reclaimed: IntCounter,
    pub gc_errors: IntCounter,
    pub gc_duration: HistogramVec,

    // WAL
    pub wal_entries_total: IntCounter,
    pub wal_bytes_written: IntCounter,
    pub wal_syncs_total: IntCounter,
    pub wal_rotations_total: IntCounter,

    // Transport
    pub transport_connections_active: IntGaugeVec,
    pub transport_connections_total: IntCounterVec,
    pub transport_send_bytes: IntCounterVec,
    pub transport_recv_bytes: IntCounterVec,
    pub transport_errors: IntCounterVec,

    // NBD
    pub nbd_connections_active: IntGauge,
    pub nbd_reads_total: IntCounter,
    pub nbd_writes_total: IntCounter,
    pub nbd_read_bytes: IntCounter,
    pub nbd_write_bytes: IntCounter,

    // PmStore (position-mapped data path under NBD + NFS).
    // Wired in main.rs via `pm_store.stats()`; a periodic task copies
    // the atomic counters into these gauges so /metrics reflects the
    // hot-path state without instrumenting every pwrite/pread call.
    pub pm_writes_total: IntGauge,
    pub pm_write_bytes_total: IntGauge,
    pub pm_write_errors_total: IntGauge,
    pub pm_reads_total: IntGauge,
    pub pm_read_bytes_total: IntGauge,
    pub pm_read_errors_total: IntGauge,
    pub pm_volumes_created_total: IntGauge,
    pub pm_volumes_deleted_total: IntGauge,

    // NFS COMPOUND. Same refresh pattern as PmStore — nvstor-nfs
    // updates atomic counters on the dispatch path, nvstor-server
    // polls them into these gauges every few seconds.
    //
    // Why these four:
    //   - `inflight`: leading indicator of saturation; a sustained
    //     non-zero baseline means the worker pool isn't draining.
    //   - `total`/`errors`: rate() in Grafana gives ops/s and an
    //     error %; both are what you'd dashboard.
    //   - `time_us`: divided by total gives average COMPOUND latency.
    //     For p99 the per-category latency histograms in NfsMetrics
    //     are still available via the legacy /metrics endpoint on
    //     nvstor-nfs's own metrics_server.rs.
    pub nfs_compound_inflight: IntGauge,
    pub nfs_compounds_total: IntGauge,
    pub nfs_compound_errors_total: IntGauge,
    pub nfs_compound_time_us_total: IntGauge,
    pub nfs_active_connections: IntGauge,

    // Node
    pub node_uptime_seconds: IntGauge,
    pub node_ready: IntGauge,
}

impl Metrics {
    pub fn new() -> Self {
        let registry = Registry::new();

        let latency_buckets = vec![
            0.00005, 0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0, 5.0, 30.0,
        ];
        let size_buckets = vec![
            1024.0, 4096.0, 65536.0, 262144.0, 1048576.0, 4194304.0, 16777216.0, 67108864.0,
        ];

        let s3_requests_total = IntCounterVec::new(
            Opts::new("nvstor_s3_requests_total", "Total S3 HTTP requests"),
            &["method", "status"],
        )
        .expect("metric creation");

        let s3_request_duration = HistogramVec::new(
            HistogramOpts::new("nvstor_s3_request_duration_seconds", "S3 request latency")
                .buckets(latency_buckets.clone()),
            &["method"],
        )
        .expect("metric creation");

        let s3_request_size_bytes = HistogramVec::new(
            HistogramOpts::new("nvstor_s3_request_size_bytes", "S3 request body size")
                .buckets(size_buckets.clone()),
            &["method"],
        )
        .expect("metric creation");

        let s3_response_size_bytes = HistogramVec::new(
            HistogramOpts::new("nvstor_s3_response_size_bytes", "S3 response body size")
                .buckets(size_buckets),
            &["method"],
        )
        .expect("metric creation");

        let storage_chunks_total =
            IntGauge::new("nvstor_storage_chunks_total", "Total chunks").expect("metric creation");
        let storage_objects_total = IntGauge::new("nvstor_storage_objects_total", "Total objects")
            .expect("metric creation");
        let storage_volumes_total = IntGauge::new("nvstor_storage_volumes_total", "Total volumes")
            .expect("metric creation");
        let storage_bytes_total = IntGauge::new("nvstor_storage_bytes_total", "Total bytes stored")
            .expect("metric creation");
        let storage_unique_bytes =
            IntGauge::new("nvstor_storage_unique_bytes", "Unique bytes (after dedup)")
                .expect("metric creation");

        let chunk_write_duration = HistogramVec::new(
            HistogramOpts::new("nvstor_chunk_write_duration_seconds", "Chunk write latency")
                .buckets(latency_buckets.clone()),
            &["backend"],
        )
        .expect("metric creation");

        let chunk_read_duration = HistogramVec::new(
            HistogramOpts::new("nvstor_chunk_read_duration_seconds", "Chunk read latency")
                .buckets(latency_buckets.clone()),
            &["backend"],
        )
        .expect("metric creation");

        let gc_runs_total =
            IntCounter::new("nvstor_gc_runs_total", "GC sweep runs").expect("metric creation");
        let gc_chunks_deleted =
            IntCounter::new("nvstor_gc_chunks_deleted_total", "Chunks deleted by GC")
                .expect("metric creation");
        let gc_bytes_reclaimed =
            IntCounter::new("nvstor_gc_bytes_reclaimed_total", "Bytes reclaimed by GC")
                .expect("metric creation");
        let gc_errors =
            IntCounter::new("nvstor_gc_errors_total", "GC errors").expect("metric creation");
        let gc_duration = HistogramVec::new(
            HistogramOpts::new("nvstor_gc_duration_seconds", "GC sweep duration")
                .buckets(vec![0.01, 0.1, 1.0, 5.0, 30.0, 60.0, 300.0]),
            &[],
        )
        .expect("metric creation");

        let wal_entries_total = IntCounter::new("nvstor_wal_entries_total", "WAL entries written")
            .expect("metric creation");
        let wal_bytes_written =
            IntCounter::new("nvstor_wal_bytes_written_total", "WAL bytes written")
                .expect("metric creation");
        let wal_syncs_total =
            IntCounter::new("nvstor_wal_syncs_total", "WAL fsyncs").expect("metric creation");
        let wal_rotations_total =
            IntCounter::new("nvstor_wal_rotations_total", "WAL segment rotations")
                .expect("metric creation");

        let transport_connections_active = IntGaugeVec::new(
            Opts::new(
                "nvstor_transport_connections_active",
                "Active transport connections",
            ),
            &["protocol"],
        )
        .expect("metric creation");
        let transport_connections_total = IntCounterVec::new(
            Opts::new(
                "nvstor_transport_connections_total",
                "Total transport connections",
            ),
            &["protocol"],
        )
        .expect("metric creation");
        let transport_send_bytes = IntCounterVec::new(
            Opts::new("nvstor_transport_send_bytes_total", "Bytes sent"),
            &["protocol"],
        )
        .expect("metric creation");
        let transport_recv_bytes = IntCounterVec::new(
            Opts::new("nvstor_transport_recv_bytes_total", "Bytes received"),
            &["protocol"],
        )
        .expect("metric creation");
        let transport_errors = IntCounterVec::new(
            Opts::new("nvstor_transport_errors_total", "Transport errors"),
            &["protocol", "type"],
        )
        .expect("metric creation");

        let nbd_connections_active =
            IntGauge::new("nvstor_nbd_connections_active", "Active NBD connections")
                .expect("metric creation");
        let nbd_reads_total = IntCounter::new("nvstor_nbd_reads_total", "NBD read operations")
            .expect("metric creation");
        let nbd_writes_total = IntCounter::new("nvstor_nbd_writes_total", "NBD write operations")
            .expect("metric creation");
        let nbd_read_bytes = IntCounter::new("nvstor_nbd_read_bytes_total", "NBD bytes read")
            .expect("metric creation");
        let nbd_write_bytes = IntCounter::new("nvstor_nbd_write_bytes_total", "NBD bytes written")
            .expect("metric creation");

        let pm_writes_total = IntGauge::new("nvstor_pm_writes_total", "PmStore total writes")
            .expect("metric creation");
        let pm_write_bytes_total =
            IntGauge::new("nvstor_pm_write_bytes_total", "PmStore bytes written")
                .expect("metric creation");
        let pm_write_errors_total =
            IntGauge::new("nvstor_pm_write_errors_total", "PmStore write errors")
                .expect("metric creation");
        let pm_reads_total =
            IntGauge::new("nvstor_pm_reads_total", "PmStore total reads").expect("metric creation");
        let pm_read_bytes_total = IntGauge::new("nvstor_pm_read_bytes_total", "PmStore bytes read")
            .expect("metric creation");
        let pm_read_errors_total =
            IntGauge::new("nvstor_pm_read_errors_total", "PmStore read errors")
                .expect("metric creation");
        let pm_volumes_created_total =
            IntGauge::new("nvstor_pm_volumes_created_total", "PmStore volumes created")
                .expect("metric creation");
        let pm_volumes_deleted_total =
            IntGauge::new("nvstor_pm_volumes_deleted_total", "PmStore volumes deleted")
                .expect("metric creation");

        let nfs_compound_inflight = IntGauge::new(
            "nvstor_nfs_compound_inflight",
            "NFS COMPOUNDs currently in flight",
        )
        .expect("metric creation");
        let nfs_compounds_total =
            IntGauge::new("nvstor_nfs_compounds_total", "Total NFS COMPOUND requests")
                .expect("metric creation");
        let nfs_compound_errors_total =
            IntGauge::new("nvstor_nfs_compound_errors_total", "NFS COMPOUND errors")
                .expect("metric creation");
        let nfs_compound_time_us_total = IntGauge::new(
            "nvstor_nfs_compound_time_us_total",
            "Cumulative COMPOUND wall-clock time (us)",
        )
        .expect("metric creation");
        let nfs_active_connections = IntGauge::new(
            "nvstor_nfs_active_connections",
            "Active NFS TCP connections",
        )
        .expect("metric creation");

        let node_uptime_seconds =
            IntGauge::new("nvstor_node_uptime_seconds", "Node uptime").expect("metric creation");
        let node_ready = IntGauge::new("nvstor_node_ready", "Node readiness (1=ready, 0=not)")
            .expect("metric creation");

        // Register all metrics
        let collectors: Vec<Box<dyn prometheus::core::Collector>> = vec![
            Box::new(s3_requests_total.clone()),
            Box::new(s3_request_duration.clone()),
            Box::new(s3_request_size_bytes.clone()),
            Box::new(s3_response_size_bytes.clone()),
            Box::new(storage_chunks_total.clone()),
            Box::new(storage_objects_total.clone()),
            Box::new(storage_volumes_total.clone()),
            Box::new(storage_bytes_total.clone()),
            Box::new(storage_unique_bytes.clone()),
            Box::new(chunk_write_duration.clone()),
            Box::new(chunk_read_duration.clone()),
            Box::new(gc_runs_total.clone()),
            Box::new(gc_chunks_deleted.clone()),
            Box::new(gc_bytes_reclaimed.clone()),
            Box::new(gc_errors.clone()),
            Box::new(gc_duration.clone()),
            Box::new(wal_entries_total.clone()),
            Box::new(wal_bytes_written.clone()),
            Box::new(wal_syncs_total.clone()),
            Box::new(wal_rotations_total.clone()),
            Box::new(transport_connections_active.clone()),
            Box::new(transport_connections_total.clone()),
            Box::new(transport_send_bytes.clone()),
            Box::new(transport_recv_bytes.clone()),
            Box::new(transport_errors.clone()),
            Box::new(nbd_connections_active.clone()),
            Box::new(nbd_reads_total.clone()),
            Box::new(nbd_writes_total.clone()),
            Box::new(nbd_read_bytes.clone()),
            Box::new(nbd_write_bytes.clone()),
            Box::new(pm_writes_total.clone()),
            Box::new(pm_write_bytes_total.clone()),
            Box::new(pm_write_errors_total.clone()),
            Box::new(pm_reads_total.clone()),
            Box::new(pm_read_bytes_total.clone()),
            Box::new(pm_read_errors_total.clone()),
            Box::new(pm_volumes_created_total.clone()),
            Box::new(pm_volumes_deleted_total.clone()),
            Box::new(nfs_compound_inflight.clone()),
            Box::new(nfs_compounds_total.clone()),
            Box::new(nfs_compound_errors_total.clone()),
            Box::new(nfs_compound_time_us_total.clone()),
            Box::new(nfs_active_connections.clone()),
            Box::new(node_uptime_seconds.clone()),
            Box::new(node_ready.clone()),
        ];
        for c in collectors {
            registry.register(c).expect("metric registration");
        }

        Self {
            registry,
            s3_requests_total,
            s3_request_duration,
            s3_request_size_bytes,
            s3_response_size_bytes,
            storage_chunks_total,
            storage_objects_total,
            storage_volumes_total,
            storage_bytes_total,
            storage_unique_bytes,
            chunk_write_duration,
            chunk_read_duration,
            gc_runs_total,
            gc_chunks_deleted,
            gc_bytes_reclaimed,
            gc_errors,
            gc_duration,
            wal_entries_total,
            wal_bytes_written,
            wal_syncs_total,
            wal_rotations_total,
            transport_connections_active,
            transport_connections_total,
            transport_send_bytes,
            transport_recv_bytes,
            transport_errors,
            nbd_connections_active,
            nbd_reads_total,
            nbd_writes_total,
            nbd_read_bytes,
            nbd_write_bytes,
            pm_writes_total,
            pm_write_bytes_total,
            pm_write_errors_total,
            pm_reads_total,
            pm_read_bytes_total,
            pm_read_errors_total,
            pm_volumes_created_total,
            pm_volumes_deleted_total,
            nfs_compound_inflight,
            nfs_compounds_total,
            nfs_compound_errors_total,
            nfs_compound_time_us_total,
            nfs_active_connections,
            node_uptime_seconds,
            node_ready,
        }
    }

    /// Render all metrics in Prometheus text format.
    pub fn render(&self) -> String {
        let encoder = TextEncoder::new();
        let families = self.registry.gather();
        let mut buf = Vec::new();
        encoder
            .encode(&families, &mut buf)
            .expect("prometheus encode");
        String::from_utf8(buf).unwrap_or_default()
    }

    /// Update storage gauges from ChunkManager stats.
    pub fn update_storage_stats(&self, stats: &nvstor_core::StorageStats) {
        self.storage_chunks_total.set(stats.total_chunks as i64);
        self.storage_objects_total.set(stats.total_objects as i64);
        self.storage_volumes_total.set(stats.total_volumes as i64);
        self.storage_bytes_total.set(stats.total_bytes as i64);
        self.storage_unique_bytes.set(stats.unique_bytes as i64);
    }

    /// Snapshot the PmStore atomic counters into our gauges.
    ///
    /// Called from a periodic refresh task in main.rs. Using gauges
    /// for what are conceptually counters keeps the wiring simple
    /// (we own a snapshot, prometheus doesn't have to mediate); the
    /// `_total` suffix preserves the convention so a Grafana
    /// `rate()` over the series still computes ops/s correctly.
    pub fn update_pm_stats(&self, stats: &nvstor_storage::pm::PmStoreStats) {
        use std::sync::atomic::Ordering;
        self.pm_writes_total
            .set(stats.writes_total.load(Ordering::Relaxed) as i64);
        self.pm_write_bytes_total
            .set(stats.write_bytes_total.load(Ordering::Relaxed) as i64);
        self.pm_write_errors_total
            .set(stats.write_errors_total.load(Ordering::Relaxed) as i64);
        self.pm_reads_total
            .set(stats.reads_total.load(Ordering::Relaxed) as i64);
        self.pm_read_bytes_total
            .set(stats.read_bytes_total.load(Ordering::Relaxed) as i64);
        self.pm_read_errors_total
            .set(stats.read_errors_total.load(Ordering::Relaxed) as i64);
        self.pm_volumes_created_total
            .set(stats.volumes_created_total.load(Ordering::Relaxed) as i64);
        self.pm_volumes_deleted_total
            .set(stats.volumes_deleted_total.load(Ordering::Relaxed) as i64);
    }

    /// Snapshot the NFS COMPOUND atomic counters into our gauges.
    /// Called from the same periodic refresh task as `update_pm_stats`
    /// in main.rs.
    pub fn update_nfs_stats(&self, stats: &nvstor_nfs::metrics::NfsMetrics) {
        use std::sync::atomic::Ordering;
        self.nfs_compound_inflight
            .set(stats.compound_inflight.load(Ordering::Relaxed) as i64);
        self.nfs_compounds_total
            .set(stats.compound_total.load(Ordering::Relaxed) as i64);
        self.nfs_compound_errors_total
            .set(stats.compound_errors.load(Ordering::Relaxed) as i64);
        self.nfs_compound_time_us_total
            .set(stats.compound_time_us.load(Ordering::Relaxed) as i64);
        self.nfs_active_connections
            .set(stats.active_connections.load(Ordering::Relaxed) as i64);
    }

    /// Record a GC sweep result.
    pub fn record_gc(&self, report: &nvstor_core::GcReport) {
        self.gc_runs_total.inc();
        self.gc_chunks_deleted.inc_by(report.chunks_deleted);
        self.gc_bytes_reclaimed.inc_by(report.bytes_reclaimed);
        self.gc_errors.inc_by(report.errors);
        self.gc_duration
            .with_label_values(&[])
            .observe(report.duration_ms as f64 / 1000.0);
    }
}

/// Axum middleware for S3 request metrics and request ID.
pub async fn s3_metrics_layer(
    metrics: Arc<Metrics>,
    req: Request<Body>,
    next: axum::middleware::Next,
) -> Response {
    let method = req.method().to_string();
    let uri = req.uri().path().to_string();
    let request_id = uuid::Uuid::new_v4().to_string();
    let start = Instant::now();

    let span = tracing::info_span!("s3", %request_id, %method, %uri);
    let _enter = span.enter();

    let mut response = next.run(req).await;

    let duration = start.elapsed().as_secs_f64();
    let status = response.status().as_u16().to_string();

    metrics
        .s3_requests_total
        .with_label_values(&[&method, &status])
        .inc();
    metrics
        .s3_request_duration
        .with_label_values(&[&method])
        .observe(duration);

    // Add request ID and timing to response headers.
    let headers = response.headers_mut();
    headers.insert(
        "x-amz-request-id",
        request_id.parse().expect("uuid is valid header"),
    );
    headers.insert("x-amz-id-2", request_id.parse().expect("uuid"));

    response
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_metrics_creation() {
        let metrics = Metrics::new();
        assert!(!metrics.render().is_empty());
    }

    #[test]
    fn test_metrics_counters() {
        let metrics = Metrics::new();
        metrics
            .s3_requests_total
            .with_label_values(&["GET", "200"])
            .inc();
        metrics
            .s3_requests_total
            .with_label_values(&["PUT", "200"])
            .inc();
        metrics.gc_runs_total.inc();

        let output = metrics.render();
        assert!(output.contains("nvstor_s3_requests_total"));
        assert!(output.contains("nvstor_gc_runs_total"));
    }

    #[test]
    fn test_update_storage_stats() {
        let metrics = Metrics::new();
        let stats = nvstor_core::StorageStats {
            total_chunks: 100,
            total_bytes: 1_000_000,
            unique_bytes: 500_000,
            total_objects: 50,
            total_volumes: 3,
        };
        metrics.update_storage_stats(&stats);

        let output = metrics.render();
        assert!(output.contains("nvstor_storage_chunks_total 100"));
        assert!(output.contains("nvstor_storage_objects_total 50"));
    }

    #[test]
    fn test_record_gc() {
        let metrics = Metrics::new();
        let report = nvstor_core::GcReport {
            chunks_deleted: 10,
            bytes_reclaimed: 10_000,
            duration_ms: 500,
            errors: 1,
        };
        metrics.record_gc(&report);

        let output = metrics.render();
        assert!(output.contains("nvstor_gc_chunks_deleted_total 10"));
        assert!(output.contains("nvstor_gc_errors_total 1"));
    }
}

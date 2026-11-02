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

//! NFS server metrics — operation counters, latency histograms, connection
//! gauges. Exposed in Prometheus exposition format via `to_prometheus()`.
//!
//! Histograms are bucketed by category (read / write / metadata / session /
//! other) rather than per-op. Per-op buckets explode the cardinality 56× for
//! marginal operator value: an oncall sees "metadata p99 spiked" and digs in
//! from there. Adding per-op histograms later is mechanical if needed.

use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Instant;

/// Latency bucket boundaries, in microseconds. Cumulative ("le" style) per
/// the Prometheus convention. Tuned for NFS where most ops should be sub-ms
/// and anything > 100 ms is a problem worth alerting on.
pub const LATENCY_BUCKETS_US: [u64; 12] = [
    10, 50, 100, 250, 500, 1_000, 5_000, 25_000, 100_000, 500_000, 1_000_000, 5_000_000,
];

/// Histogram for one observation category.
///
/// Records each observation in the smallest bucket whose upper bound is
/// >= the observation. `+Inf` bucket is implicit via `count - sum_of_buckets`
/// at exposition time.
pub struct LatencyHistogram {
    /// Counts per bucket, NON-cumulative. Exposition converts to cumulative.
    pub buckets: [AtomicU64; 12],
    /// Counts beyond the largest finite bucket boundary (the `+Inf` bucket).
    pub overflow: AtomicU64,
    /// Sum of all observations, in microseconds.
    pub sum_us: AtomicU64,
    /// Total number of observations.
    pub count: AtomicU64,
}

impl LatencyHistogram {
    pub fn new() -> Self {
        Self {
            buckets: Default::default(),
            overflow: AtomicU64::new(0),
            sum_us: AtomicU64::new(0),
            count: AtomicU64::new(0),
        }
    }

    /// Record a duration observation.
    pub fn observe(&self, duration_us: u64) {
        let mut placed = false;
        for (i, bound) in LATENCY_BUCKETS_US.iter().enumerate() {
            if duration_us <= *bound {
                self.buckets[i].fetch_add(1, Ordering::Relaxed);
                placed = true;
                break;
            }
        }
        if !placed {
            self.overflow.fetch_add(1, Ordering::Relaxed);
        }
        self.sum_us.fetch_add(duration_us, Ordering::Relaxed);
        self.count.fetch_add(1, Ordering::Relaxed);
    }

    /// Append Prometheus exposition lines for this histogram, with the given
    /// name and category label.
    fn emit_prometheus(&self, out: &mut String, name: &str, category: &str) {
        let count = self.count.load(Ordering::Relaxed);
        let sum_us = self.sum_us.load(Ordering::Relaxed);

        // Cumulative bucket counts.
        let mut running = 0u64;
        for (i, bound_us) in LATENCY_BUCKETS_US.iter().enumerate() {
            running += self.buckets[i].load(Ordering::Relaxed);
            let bound_seconds = (*bound_us as f64) / 1_000_000.0;
            out.push_str(&format!(
                "{name}_bucket{{category=\"{category}\",le=\"{bound_seconds}\"}} {running}\n",
            ));
        }
        // +Inf bucket equals total count.
        out.push_str(&format!(
            "{name}_bucket{{category=\"{category}\",le=\"+Inf\"}} {count}\n",
        ));
        // Sum in seconds (Prometheus convention).
        let sum_seconds = (sum_us as f64) / 1_000_000.0;
        out.push_str(&format!(
            "{name}_sum{{category=\"{category}\"}} {sum_seconds}\n",
        ));
        out.push_str(&format!(
            "{name}_count{{category=\"{category}\"}} {count}\n",
        ));
    }
}

impl Default for LatencyHistogram {
    fn default() -> Self {
        Self::new()
    }
}

/// Op category — used to bucket per-op latency observations.
#[derive(Copy, Clone, Debug)]
pub enum OpCategory {
    /// READ, READLINK, READDIR — read-side data ops.
    Read,
    /// WRITE, COPY, ALLOCATE, DEALLOCATE, CLONE — write-side data ops.
    Write,
    /// LOOKUP, GETATTR, ACCESS, GETFH, PUTFH, PUTROOTFH, etc. — metadata.
    Metadata,
    /// EXCHANGE_ID, CREATE_SESSION, SEQUENCE, DESTROY_*, OPEN/CLOSE — session
    /// + state machinery.
    Session,
    /// Anything not classified above.
    Other,
}

impl OpCategory {
    pub fn label(&self) -> &'static str {
        match self {
            OpCategory::Read => "read",
            OpCategory::Write => "write",
            OpCategory::Metadata => "metadata",
            OpCategory::Session => "session",
            OpCategory::Other => "other",
        }
    }
}

/// NFS server metrics.
pub struct NfsMetrics {
    /// Total COMPOUND requests processed.
    pub compound_total: AtomicU64,
    /// Total COMPOUND errors.
    pub compound_errors: AtomicU64,
    /// In-flight COMPOUNDs right now (across all connections).
    ///
    /// Incremented on dispatch entry, decremented on dispatch
    /// completion. Useful for "is the server falling behind?" — a
    /// sustained climb means the blocking pool / storage layer
    /// isn't draining as fast as new requests arrive.
    pub compound_inflight: AtomicU64,
    /// Total bytes read via NFS READ.
    pub read_bytes: AtomicU64,
    /// Total bytes written via NFS WRITE.
    pub write_bytes: AtomicU64,
    /// Total NFS READ operations.
    pub read_ops: AtomicU64,
    /// Total NFS WRITE operations.
    pub write_ops: AtomicU64,
    /// Total OPEN operations.
    pub open_ops: AtomicU64,
    /// Total CLOSE operations.
    pub close_ops: AtomicU64,
    /// Total LOOKUP operations.
    pub lookup_ops: AtomicU64,
    /// Total GETATTR operations.
    pub getattr_ops: AtomicU64,
    /// Total CREATE operations.
    pub create_ops: AtomicU64,
    /// Total REMOVE operations.
    pub remove_ops: AtomicU64,
    /// Total COPY operations (v4.2 server-side copy).
    pub copy_ops: AtomicU64,
    /// Total bytes copied via server-side COPY.
    pub copy_bytes: AtomicU64,
    /// Total LOCK operations.
    pub lock_ops: AtomicU64,
    /// Total lock conflicts (DENIED).
    pub lock_conflicts: AtomicU64,
    /// Current active connections.
    pub active_connections: AtomicU64,
    /// 1 while the NFS/RDMA listener is accepting, 0 when RDMA is off or
    /// its listener failed and is being re-created.
    pub rdma_listener_up: AtomicU64,
    /// Fatal NFS/RDMA listener failures (device removal and the like).
    pub rdma_listener_failures: AtomicU64,
    /// Current active sessions.
    pub active_sessions: AtomicU64,
    /// Current active clients.
    pub active_clients: AtomicU64,
    /// Total lease expirations.
    pub lease_expirations: AtomicU64,
    /// Cumulative COMPOUND processing time in microseconds.
    pub compound_time_us: AtomicU64,
    /// Per-category latency histograms (read/write/metadata/session/other).
    pub op_latency_read: LatencyHistogram,
    pub op_latency_write: LatencyHistogram,
    pub op_latency_metadata: LatencyHistogram,
    pub op_latency_session: LatencyHistogram,
    pub op_latency_other: LatencyHistogram,
    /// Per-op invocation counters indexed by NFS opnum. NFSv4.2 ops top out
    /// near 75; we size to 128 to leave headroom for future protocol ops.
    /// Exposed in Prometheus as `nvstor_nfs_op_total{op="OP_NAME"}`.
    pub op_calls: [AtomicU64; 128],
}

impl NfsMetrics {
    pub fn new() -> Self {
        Self {
            compound_total: AtomicU64::new(0),
            compound_errors: AtomicU64::new(0),
            compound_inflight: AtomicU64::new(0),
            read_bytes: AtomicU64::new(0),
            write_bytes: AtomicU64::new(0),
            read_ops: AtomicU64::new(0),
            write_ops: AtomicU64::new(0),
            open_ops: AtomicU64::new(0),
            close_ops: AtomicU64::new(0),
            lookup_ops: AtomicU64::new(0),
            getattr_ops: AtomicU64::new(0),
            create_ops: AtomicU64::new(0),
            remove_ops: AtomicU64::new(0),
            copy_ops: AtomicU64::new(0),
            copy_bytes: AtomicU64::new(0),
            lock_ops: AtomicU64::new(0),
            lock_conflicts: AtomicU64::new(0),
            active_connections: AtomicU64::new(0),
            rdma_listener_up: AtomicU64::new(0),
            rdma_listener_failures: AtomicU64::new(0),
            active_sessions: AtomicU64::new(0),
            active_clients: AtomicU64::new(0),
            lease_expirations: AtomicU64::new(0),
            compound_time_us: AtomicU64::new(0),
            op_latency_read: LatencyHistogram::new(),
            op_latency_write: LatencyHistogram::new(),
            op_latency_metadata: LatencyHistogram::new(),
            op_latency_session: LatencyHistogram::new(),
            op_latency_other: LatencyHistogram::new(),
            op_calls: std::array::from_fn(|_| AtomicU64::new(0)),
        }
    }

    /// Record a per-op latency observation in the appropriate category bucket.
    pub fn record_op_latency(&self, category: OpCategory, duration_us: u64) {
        let h = match category {
            OpCategory::Read => &self.op_latency_read,
            OpCategory::Write => &self.op_latency_write,
            OpCategory::Metadata => &self.op_latency_metadata,
            OpCategory::Session => &self.op_latency_session,
            OpCategory::Other => &self.op_latency_other,
        };
        h.observe(duration_us);
    }

    pub fn record_compound(&self, start: Instant, is_error: bool) {
        self.compound_total.fetch_add(1, Ordering::Relaxed);
        if is_error {
            self.compound_errors.fetch_add(1, Ordering::Relaxed);
        }
        let elapsed_us = start.elapsed().as_micros() as u64;
        self.compound_time_us
            .fetch_add(elapsed_us, Ordering::Relaxed);
    }

    pub fn record_read(&self, bytes: u64) {
        self.read_ops.fetch_add(1, Ordering::Relaxed);
        self.read_bytes.fetch_add(bytes, Ordering::Relaxed);
    }

    pub fn record_write(&self, bytes: u64) {
        self.write_ops.fetch_add(1, Ordering::Relaxed);
        self.write_bytes.fetch_add(bytes, Ordering::Relaxed);
    }

    pub fn record_copy(&self, bytes: u64) {
        self.copy_ops.fetch_add(1, Ordering::Relaxed);
        self.copy_bytes.fetch_add(bytes, Ordering::Relaxed);
    }

    pub fn record_lookup(&self) {
        self.lookup_ops.fetch_add(1, Ordering::Relaxed);
    }

    pub fn record_getattr(&self) {
        self.getattr_ops.fetch_add(1, Ordering::Relaxed);
    }

    /// Record one invocation of the given NFS op (by opnum). Out-of-range
    /// opnums are silently ignored — they would have already been rejected
    /// by the dispatcher with NFS4ERR_OP_ILLEGAL.
    pub fn record_op(&self, opnum: u32) {
        if let Some(c) = self.op_calls.get(opnum as usize) {
            c.fetch_add(1, Ordering::Relaxed);
        }
    }

    /// Format metrics as Prometheus exposition format.
    pub fn to_prometheus(&self) -> String {
        let mut s = String::with_capacity(2048);
        let m = |name: &str, help: &str, val: u64| -> String {
            format!("# HELP {name} {help}\n# TYPE {name} counter\n{name} {val}\n")
        };
        let g = |name: &str, help: &str, val: u64| -> String {
            format!("# HELP {name} {help}\n# TYPE {name} gauge\n{name} {val}\n")
        };

        s.push_str(&m(
            "nvstor_nfs_compound_total",
            "Total COMPOUND requests",
            self.compound_total.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_compound_errors_total",
            "Total COMPOUND errors",
            self.compound_errors.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_read_bytes_total",
            "Total bytes read via NFS",
            self.read_bytes.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_write_bytes_total",
            "Total bytes written via NFS",
            self.write_bytes.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_read_ops_total",
            "Total READ operations",
            self.read_ops.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_write_ops_total",
            "Total WRITE operations",
            self.write_ops.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_open_ops_total",
            "Total OPEN operations",
            self.open_ops.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_lookup_ops_total",
            "Total LOOKUP operations",
            self.lookup_ops.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_getattr_ops_total",
            "Total GETATTR operations",
            self.getattr_ops.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_create_ops_total",
            "Total CREATE operations",
            self.create_ops.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_copy_ops_total",
            "Total server-side COPY operations",
            self.copy_ops.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_copy_bytes_total",
            "Total bytes copied via server-side COPY",
            self.copy_bytes.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_lock_ops_total",
            "Total LOCK operations",
            self.lock_ops.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_lock_conflicts_total",
            "Total lock conflicts",
            self.lock_conflicts.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_lease_expirations_total",
            "Total client lease expirations",
            self.lease_expirations.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_compound_time_us_total",
            "Total COMPOUND processing time (microseconds)",
            self.compound_time_us.load(Ordering::Relaxed),
        ));
        s.push_str(&g(
            "nvstor_nfs_active_connections",
            "Current active NFS connections",
            self.active_connections.load(Ordering::Relaxed),
        ));
        s.push_str(&g(
            "nvstor_nfs_rdma_listener_up",
            "1 while the NFS/RDMA listener is accepting connections",
            self.rdma_listener_up.load(Ordering::Relaxed),
        ));
        s.push_str(&m(
            "nvstor_nfs_rdma_listener_failures_total",
            "Fatal NFS/RDMA listener failures",
            self.rdma_listener_failures.load(Ordering::Relaxed),
        ));
        s.push_str(&g(
            "nvstor_nfs_active_sessions",
            "Current active NFS sessions",
            self.active_sessions.load(Ordering::Relaxed),
        ));
        s.push_str(&g(
            "nvstor_nfs_active_clients",
            "Current active NFS clients",
            self.active_clients.load(Ordering::Relaxed),
        ));

        // Per-category latency histograms.
        let hist_name = "nvstor_nfs_op_duration_seconds";
        s.push_str(&format!(
            "# HELP {hist_name} NFS op latency by category, in seconds.\n\
             # TYPE {hist_name} histogram\n",
        ));
        self.op_latency_read
            .emit_prometheus(&mut s, hist_name, "read");
        self.op_latency_write
            .emit_prometheus(&mut s, hist_name, "write");
        self.op_latency_metadata
            .emit_prometheus(&mut s, hist_name, "metadata");
        self.op_latency_session
            .emit_prometheus(&mut s, hist_name, "session");
        self.op_latency_other
            .emit_prometheus(&mut s, hist_name, "other");

        // Per-op invocation counters, labeled by op name. Lets the smoketest
        // assert that every individual op the kernel can send actually fired.
        let op_total = "nvstor_nfs_op_total";
        s.push_str(&format!(
            "# HELP {op_total} Per-op invocation count\n\
             # TYPE {op_total} counter\n",
        ));
        for (opnum, name) in OP_NAMES.iter().enumerate() {
            if name.is_empty() {
                continue;
            }
            let v = self.op_calls[opnum].load(Ordering::Relaxed);
            s.push_str(&format!("{op_total}{{op=\"{name}\"}} {v}\n"));
        }

        s
    }
}

/// NFS opnum → op name. Used both for metric labels and dispatcher diagnostics.
/// Empty string means "no op assigned to that opnum".
pub static OP_NAMES: [&str; 128] = {
    let mut a = [""; 128];
    a[3] = "OP_ACCESS";
    a[4] = "OP_CLOSE";
    a[5] = "OP_COMMIT";
    a[6] = "OP_CREATE";
    a[7] = "OP_DELEGPURGE";
    a[8] = "OP_DELEGRETURN";
    a[9] = "OP_GETATTR";
    a[10] = "OP_GETFH";
    a[11] = "OP_LINK";
    a[12] = "OP_LOCK";
    a[13] = "OP_LOCKT";
    a[14] = "OP_LOCKU";
    a[15] = "OP_LOOKUP";
    a[16] = "OP_LOOKUPP";
    a[17] = "OP_NVERIFY";
    a[18] = "OP_OPEN";
    a[19] = "OP_OPENATTR";
    a[20] = "OP_OPEN_CONFIRM";
    a[21] = "OP_OPEN_DOWNGRADE";
    a[22] = "OP_PUTFH";
    a[23] = "OP_PUTPUBFH";
    a[24] = "OP_PUTROOTFH";
    a[25] = "OP_READ";
    a[26] = "OP_READDIR";
    a[27] = "OP_READLINK";
    a[28] = "OP_REMOVE";
    a[29] = "OP_RENAME";
    a[30] = "OP_RENEW";
    a[31] = "OP_RESTOREFH";
    a[32] = "OP_SAVEFH";
    a[33] = "OP_SECINFO";
    a[34] = "OP_SETATTR";
    a[35] = "OP_SETCLIENTID";
    a[36] = "OP_SETCLIENTID_CONFIRM";
    a[37] = "OP_VERIFY";
    a[38] = "OP_WRITE";
    a[39] = "OP_RELEASE_LOCKOWNER";
    a[40] = "OP_BACKCHANNEL_CTL";
    a[41] = "OP_BIND_CONN_TO_SESSION";
    a[42] = "OP_EXCHANGE_ID";
    a[43] = "OP_CREATE_SESSION";
    a[44] = "OP_DESTROY_SESSION";
    a[45] = "OP_FREE_STATEID";
    a[46] = "OP_GET_DIR_DELEGATION";
    a[47] = "OP_GETDEVICEINFO";
    a[48] = "OP_GETDEVICELIST";
    a[49] = "OP_LAYOUTCOMMIT";
    a[50] = "OP_LAYOUTGET";
    a[51] = "OP_LAYOUTRETURN";
    a[52] = "OP_SECINFO_NO_NAME";
    a[53] = "OP_SEQUENCE";
    a[54] = "OP_SET_SSV";
    a[55] = "OP_TEST_STATEID";
    a[56] = "OP_WANT_DELEGATION";
    a[57] = "OP_DESTROY_CLIENTID";
    a[58] = "OP_RECLAIM_COMPLETE";
    a[59] = "OP_ALLOCATE";
    a[60] = "OP_COPY";
    a[61] = "OP_COPY_NOTIFY";
    a[62] = "OP_DEALLOCATE";
    a[63] = "OP_IO_ADVISE";
    a[64] = "OP_LAYOUTERROR";
    a[65] = "OP_LAYOUTSTATS";
    a[66] = "OP_OFFLOAD_CANCEL";
    a[67] = "OP_OFFLOAD_STATUS";
    a[68] = "OP_READ_PLUS";
    a[69] = "OP_SEEK";
    a[70] = "OP_WRITE_SAME";
    a[71] = "OP_CLONE";
    a[72] = "OP_GETXATTR";
    a[73] = "OP_SETXATTR";
    a[74] = "OP_LISTXATTRS";
    a[75] = "OP_REMOVEXATTR";
    a
};

impl Default for NfsMetrics {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn metrics_new_all_zero() {
        let m = NfsMetrics::new();
        assert_eq!(m.compound_total.load(Ordering::Relaxed), 0);
        assert_eq!(m.read_bytes.load(Ordering::Relaxed), 0);
    }

    #[test]
    fn record_compound_tracks_time() {
        let m = NfsMetrics::new();
        let start = Instant::now();
        std::thread::sleep(std::time::Duration::from_millis(1));
        m.record_compound(start, false);
        assert_eq!(m.compound_total.load(Ordering::Relaxed), 1);
        assert_eq!(m.compound_errors.load(Ordering::Relaxed), 0);
        assert!(m.compound_time_us.load(Ordering::Relaxed) > 0);
    }

    #[test]
    fn record_compound_error() {
        let m = NfsMetrics::new();
        m.record_compound(Instant::now(), true);
        assert_eq!(m.compound_errors.load(Ordering::Relaxed), 1);
    }

    #[test]
    fn prometheus_format() {
        let m = NfsMetrics::new();
        m.read_ops.fetch_add(42, Ordering::Relaxed);
        let prom = m.to_prometheus();
        assert!(prom.contains("nvstor_nfs_read_ops_total 42"));
        assert!(prom.contains("# TYPE nvstor_nfs_read_ops_total counter"));
        assert!(prom.contains("# TYPE nvstor_nfs_rdma_listener_up gauge"));
        assert!(prom.contains("nvstor_nfs_rdma_listener_failures_total 0"));
    }

    #[test]
    fn record_copy() {
        let m = NfsMetrics::new();
        m.record_copy(1024 * 1024);
        assert_eq!(m.copy_ops.load(Ordering::Relaxed), 1);
        assert_eq!(m.copy_bytes.load(Ordering::Relaxed), 1048576);
    }

    /// Histogram observations land in the smallest bucket >= duration_us, and
    /// observations exceeding the largest bucket go to overflow / +Inf.
    #[test]
    fn histogram_buckets_observation() {
        let h = LatencyHistogram::new();
        h.observe(5); // 10us bucket
        h.observe(75); // 100us bucket
        h.observe(2_000_000); // 5_000_000us bucket (5s)
        h.observe(10_000_000); // overflow

        assert_eq!(h.count.load(Ordering::Relaxed), 4);
        assert_eq!(
            h.sum_us.load(Ordering::Relaxed),
            5 + 75 + 2_000_000 + 10_000_000
        );
        assert_eq!(h.buckets[0].load(Ordering::Relaxed), 1, "5us → 10us bucket");
        assert_eq!(
            h.buckets[2].load(Ordering::Relaxed),
            1,
            "75us → 100us bucket"
        );
        assert_eq!(h.buckets[11].load(Ordering::Relaxed), 1, "2s → 5s bucket");
        assert_eq!(h.overflow.load(Ordering::Relaxed), 1, "10s → +Inf");
    }

    /// Prometheus exposition emits cumulative bucket counts + sum + count.
    #[test]
    fn histogram_prometheus_format() {
        let h = LatencyHistogram::new();
        h.observe(5); // bucket 0 (10us)
        h.observe(20); // bucket 1 (50us)
        h.observe(75); // bucket 2 (100us)

        let mut out = String::new();
        h.emit_prometheus(&mut out, "test_op_duration_seconds", "read");

        // Cumulative counts: 1, 2, 3, 3, ...
        assert!(out.contains("test_op_duration_seconds_bucket{category=\"read\",le=\"0.00001\"} 1"));
        assert!(out.contains("test_op_duration_seconds_bucket{category=\"read\",le=\"0.00005\"} 2"));
        assert!(out.contains("test_op_duration_seconds_bucket{category=\"read\",le=\"0.0001\"} 3"));
        assert!(out.contains("test_op_duration_seconds_bucket{category=\"read\",le=\"+Inf\"} 3"));
        assert!(out.contains("test_op_duration_seconds_count{category=\"read\"} 3"));
    }

    /// `record_op_latency` routes to the correct category bucket.
    #[test]
    fn nfs_metrics_record_op_latency_by_category() {
        let m = NfsMetrics::new();
        m.record_op_latency(OpCategory::Read, 5);
        m.record_op_latency(OpCategory::Write, 5);
        m.record_op_latency(OpCategory::Read, 5);

        assert_eq!(m.op_latency_read.count.load(Ordering::Relaxed), 2);
        assert_eq!(m.op_latency_write.count.load(Ordering::Relaxed), 1);
        assert_eq!(m.op_latency_metadata.count.load(Ordering::Relaxed), 0);
    }

    /// Full Prometheus output should include the histogram series.
    #[test]
    fn prometheus_output_includes_histogram() {
        let m = NfsMetrics::new();
        m.record_op_latency(OpCategory::Read, 5);
        let prom = m.to_prometheus();
        assert!(prom.contains("# TYPE nvstor_nfs_op_duration_seconds histogram"));
        assert!(prom.contains("nvstor_nfs_op_duration_seconds_count{category=\"read\"} 1"));
        assert!(
            prom.contains("nvstor_nfs_op_duration_seconds_bucket{category=\"read\",le=\"+Inf\"} 1")
        );
    }
}

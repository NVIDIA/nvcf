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

//! Client metrics.
//!
//! With the `metrics` feature, [`GdsClientMetrics`] registers the
//! `nvcf_gds_client_*` Prometheus metrics below. The names and labels are
//! the contract for the Go client (`pkg/client`) too. Without the feature
//! the crate records nothing and has no Prometheus dependency.
//!
//! | Metric | Type | Labels |
//! | :---- | :---- | :---- |
//! | `nvcf_gds_client_requests_total` | counter | `method`, `code` |
//! | `nvcf_gds_client_request_duration_seconds` | histogram | `method` |
//! | `nvcf_gds_client_retries_total` | counter | `method` |
//! | `nvcf_gds_client_connection_attempts_total` | counter | `result` (`success`, `failure`) |
//! | `nvcf_gds_client_failovers_total` | counter | `reason` (`error`, `return_to_primary`) |
//! | `nvcf_gds_client_connected` | gauge | |
//! | `nvcf_gds_client_watch_events_total` | counter | `kind`, `type` |
//! | `nvcf_gds_client_watch_resyncs_total` | counter | `kind`, `reason` (`list`, `snapshot`) |
//! | `nvcf_gds_client_cache_objects` | gauge | `kind` |
//! | `nvcf_gds_client_cache_stale` | gauge | `kind` |
//! | `nvcf_gds_client_pool_registrations_total` | counter | `result` (`success`, `failure`) |
//! | `nvcf_gds_client_pool_heartbeats_total` | counter | `result` (`success`, `failure`, `reregister`) |
//!
//! `method` is the gRPC method name (`RegisterPool`, `ListPools`, ...),
//! `code` the gRPC code in its canonical spelling (`OK`, `Unavailable`, ...),
//! `kind` the cached object kind (`pools`, `routers`) and `type` the watch
//! event type (`snapshot_start`, `upsert`, `delete`, `snapshot_end`,
//! `bookmark`). Counters are pre-initialised for every known label value.

use std::time::Duration;

use tonic::Code;

/// Prefix of every client metric.
pub const METRIC_PREFIX: &str = "nvcf_gds_client";

/// gRPC methods the client calls, in the label spelling.
pub const METHODS: &[&str] = &[
    "RegisterPool",
    "UpdatePool",
    "HeartbeatPool",
    "DeregisterPool",
    "RegisterPoolAgent",
    "HeartbeatPoolAgent",
    "DeregisterPoolAgent",
    "GetPool",
    "ListPools",
    "WatchPools",
    "ListRouters",
    "WatchRouters",
    "GetCluster",
    "ListClusters",
    "GetServiceImport",
    "ListServiceImports",
    "WatchServiceImports",
    "GetServerInfo",
];

/// Cached object kinds.
pub const CACHE_KINDS: &[&str] = &["pools", "routers"];

/// Watch event types, in the label spelling.
pub const WATCH_EVENT_TYPES: &[&str] = &[
    "snapshot_start",
    "upsert",
    "delete",
    "snapshot_end",
    "bookmark",
];

/// The canonical gRPC spelling of a code, identical to Go's `codes.Code`
/// strings so both clients emit the same label values.
pub fn code_label(code: Code) -> &'static str {
    match code {
        Code::Ok => "OK",
        Code::Cancelled => "Canceled",
        Code::Unknown => "Unknown",
        Code::InvalidArgument => "InvalidArgument",
        Code::DeadlineExceeded => "DeadlineExceeded",
        Code::NotFound => "NotFound",
        Code::AlreadyExists => "AlreadyExists",
        Code::PermissionDenied => "PermissionDenied",
        Code::ResourceExhausted => "ResourceExhausted",
        Code::FailedPrecondition => "FailedPrecondition",
        Code::Aborted => "Aborted",
        Code::OutOfRange => "OutOfRange",
        Code::Unimplemented => "Unimplemented",
        Code::Internal => "Internal",
        Code::Unavailable => "Unavailable",
        Code::DataLoss => "DataLoss",
        Code::Unauthenticated => "Unauthenticated",
    }
}

#[cfg(feature = "metrics")]
pub use imp::GdsClientMetrics;

#[cfg(feature = "metrics")]
mod imp {
    use prometheus::{Gauge, GaugeVec, HistogramOpts, HistogramVec, IntCounterVec, Opts, Registry};

    use super::{CACHE_KINDS, METHODS, METRIC_PREFIX, WATCH_EVENT_TYPES};

    /// The `nvcf_gds_client_*` metrics. Register once per registry and share
    /// the handle between clients with `GdsClient::with_metrics`.
    #[derive(Debug, Clone)]
    pub struct GdsClientMetrics {
        pub(crate) requests: IntCounterVec,
        pub(crate) request_duration: HistogramVec,
        pub(crate) retries: IntCounterVec,
        pub(crate) connection_attempts: IntCounterVec,
        pub(crate) failovers: IntCounterVec,
        pub(crate) connected: Gauge,
        pub(crate) watch_events: IntCounterVec,
        pub(crate) watch_resyncs: IntCounterVec,
        pub(crate) cache_objects: GaugeVec,
        pub(crate) cache_stale: GaugeVec,
        pub(crate) pool_registrations: IntCounterVec,
        pub(crate) pool_heartbeats: IntCounterVec,
    }

    fn name(suffix: &str) -> String {
        format!("{METRIC_PREFIX}_{suffix}")
    }

    impl GdsClientMetrics {
        /// Creates the metrics, registers them with `registry` and
        /// pre-initialises every counter.
        pub fn register(registry: &Registry) -> Result<std::sync::Arc<Self>, prometheus::Error> {
            let metrics = Self {
                requests: IntCounterVec::new(
                    Opts::new(
                        name("requests_total"),
                        "GDS RPCs completed by method and code.",
                    ),
                    &["method", "code"],
                )?,
                request_duration: HistogramVec::new(
                    HistogramOpts::new(
                        name("request_duration_seconds"),
                        "Duration of GDS unary RPC attempts.",
                    ),
                    &["method"],
                )?,
                retries: IntCounterVec::new(
                    Opts::new(
                        name("retries_total"),
                        "GDS RPC attempts retried after UNAVAILABLE or DEADLINE_EXCEEDED.",
                    ),
                    &["method"],
                )?,
                connection_attempts: IntCounterVec::new(
                    Opts::new(
                        name("connection_attempts_total"),
                        "Connection attempts to GDS endpoints.",
                    ),
                    &["result"],
                )?,
                failovers: IntCounterVec::new(
                    Opts::new(
                        name("failovers_total"),
                        "Switches of the GDS endpoint in use.",
                    ),
                    &["reason"],
                )?,
                connected: Gauge::new(
                    name("connected"),
                    "1 while the client holds a GDS connection.",
                )?,
                watch_events: IntCounterVec::new(
                    Opts::new(
                        name("watch_events_total"),
                        "GDS watch events received by kind and type.",
                    ),
                    &["kind", "type"],
                )?,
                watch_resyncs: IntCounterVec::new(
                    Opts::new(
                        name("watch_resyncs_total"),
                        "Full cache replacements from a list or a watch snapshot.",
                    ),
                    &["kind", "reason"],
                )?,
                cache_objects: GaugeVec::new(
                    Opts::new(name("cache_objects"), "Objects held by a GDS cache."),
                    &["kind"],
                )?,
                cache_stale: GaugeVec::new(
                    Opts::new(
                        name("cache_stale"),
                        "1 while a GDS cache has not heard from GDS within its stale interval.",
                    ),
                    &["kind"],
                )?,
                pool_registrations: IntCounterVec::new(
                    Opts::new(
                        name("pool_registrations_total"),
                        "RegisterPool calls made by pool registrations.",
                    ),
                    &["result"],
                )?,
                pool_heartbeats: IntCounterVec::new(
                    Opts::new(
                        name("pool_heartbeats_total"),
                        "HeartbeatPool calls made by pool registrations.",
                    ),
                    &["result"],
                )?,
            };
            registry.register(Box::new(metrics.requests.clone()))?;
            registry.register(Box::new(metrics.request_duration.clone()))?;
            registry.register(Box::new(metrics.retries.clone()))?;
            registry.register(Box::new(metrics.connection_attempts.clone()))?;
            registry.register(Box::new(metrics.failovers.clone()))?;
            registry.register(Box::new(metrics.connected.clone()))?;
            registry.register(Box::new(metrics.watch_events.clone()))?;
            registry.register(Box::new(metrics.watch_resyncs.clone()))?;
            registry.register(Box::new(metrics.cache_objects.clone()))?;
            registry.register(Box::new(metrics.cache_stale.clone()))?;
            registry.register(Box::new(metrics.pool_registrations.clone()))?;
            registry.register(Box::new(metrics.pool_heartbeats.clone()))?;
            metrics.preinitialize();
            Ok(std::sync::Arc::new(metrics))
        }

        fn preinitialize(&self) {
            for method in METHODS {
                self.requests.with_label_values(&[method, "OK"]);
                self.retries.with_label_values(&[method]);
            }
            for result in ["success", "failure"] {
                self.connection_attempts.with_label_values(&[result]);
                self.pool_registrations.with_label_values(&[result]);
            }
            for result in ["success", "failure", "reregister"] {
                self.pool_heartbeats.with_label_values(&[result]);
            }
            for reason in ["error", "return_to_primary"] {
                self.failovers.with_label_values(&[reason]);
            }
            for kind in CACHE_KINDS {
                for event_type in WATCH_EVENT_TYPES {
                    self.watch_events.with_label_values(&[kind, event_type]);
                }
                for reason in ["list", "snapshot"] {
                    self.watch_resyncs.with_label_values(&[kind, reason]);
                }
                self.cache_objects.with_label_values(&[kind]);
                self.cache_stale.with_label_values(&[kind]);
            }
        }
    }
}

/// Where the crate records metrics. Without the `metrics` feature every
/// method is a no-op.
#[derive(Clone, Default)]
pub(crate) struct MetricsHandle {
    #[cfg(feature = "metrics")]
    inner: std::sync::Arc<std::sync::OnceLock<std::sync::Arc<GdsClientMetrics>>>,
}

impl std::fmt::Debug for MetricsHandle {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("MetricsHandle").finish_non_exhaustive()
    }
}

#[cfg(feature = "metrics")]
impl MetricsHandle {
    /// Installs the metrics; later calls are ignored.
    pub(crate) fn install(&self, metrics: std::sync::Arc<GdsClientMetrics>) {
        let _ = self.inner.set(metrics);
    }

    fn get(&self) -> Option<&GdsClientMetrics> {
        self.inner.get().map(|metrics| metrics.as_ref())
    }
}

#[cfg(feature = "metrics")]
impl MetricsHandle {
    pub(crate) fn rpc(&self, method: &str, code: Code, elapsed: Option<Duration>) {
        if let Some(metrics) = self.get() {
            metrics
                .requests
                .with_label_values(&[method, code_label(code)])
                .inc();
            if let Some(elapsed) = elapsed {
                metrics
                    .request_duration
                    .with_label_values(&[method])
                    .observe(elapsed.as_secs_f64());
            }
        }
    }

    pub(crate) fn retry(&self, method: &str) {
        if let Some(metrics) = self.get() {
            metrics.retries.with_label_values(&[method]).inc();
        }
    }

    pub(crate) fn connection_attempt(&self, success: bool) {
        if let Some(metrics) = self.get() {
            let result = if success { "success" } else { "failure" };
            metrics
                .connection_attempts
                .with_label_values(&[result])
                .inc();
        }
    }

    pub(crate) fn failover(&self, reason: &str) {
        if let Some(metrics) = self.get() {
            metrics.failovers.with_label_values(&[reason]).inc();
        }
    }

    pub(crate) fn connected(&self, connected: bool) {
        if let Some(metrics) = self.get() {
            metrics.connected.set(if connected { 1.0 } else { 0.0 });
        }
    }

    pub(crate) fn watch_event(&self, kind: &str, event_type: &str) {
        if let Some(metrics) = self.get() {
            metrics
                .watch_events
                .with_label_values(&[kind, event_type])
                .inc();
        }
    }

    pub(crate) fn resync(&self, kind: &str, reason: &str) {
        if let Some(metrics) = self.get() {
            metrics
                .watch_resyncs
                .with_label_values(&[kind, reason])
                .inc();
        }
    }

    pub(crate) fn cache(&self, kind: &str, objects: usize, stale: bool) {
        if let Some(metrics) = self.get() {
            metrics
                .cache_objects
                .with_label_values(&[kind])
                .set(objects as f64);
            metrics
                .cache_stale
                .with_label_values(&[kind])
                .set(if stale { 1.0 } else { 0.0 });
        }
    }

    pub(crate) fn pool_registration(&self, success: bool) {
        if let Some(metrics) = self.get() {
            let result = if success { "success" } else { "failure" };
            metrics
                .pool_registrations
                .with_label_values(&[result])
                .inc();
        }
    }

    pub(crate) fn pool_heartbeat(&self, result: &str) {
        if let Some(metrics) = self.get() {
            metrics.pool_heartbeats.with_label_values(&[result]).inc();
        }
    }
}

#[cfg(not(feature = "metrics"))]
impl MetricsHandle {
    pub(crate) fn rpc(&self, _method: &str, _code: Code, _elapsed: Option<Duration>) {}
    pub(crate) fn retry(&self, _method: &str) {}
    pub(crate) fn connection_attempt(&self, _success: bool) {}
    pub(crate) fn failover(&self, _reason: &str) {}
    pub(crate) fn connected(&self, _connected: bool) {}
    pub(crate) fn watch_event(&self, _kind: &str, _event_type: &str) {}
    pub(crate) fn resync(&self, _kind: &str, _reason: &str) {}
    pub(crate) fn cache(&self, _kind: &str, _objects: usize, _stale: bool) {}
    pub(crate) fn pool_registration(&self, _success: bool) {}
    pub(crate) fn pool_heartbeat(&self, _result: &str) {}
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn code_labels_match_go_spelling() {
        assert_eq!(code_label(Code::Ok), "OK");
        assert_eq!(code_label(Code::Cancelled), "Canceled");
        assert_eq!(code_label(Code::Unavailable), "Unavailable");
        assert_eq!(code_label(Code::DeadlineExceeded), "DeadlineExceeded");
    }

    #[cfg(feature = "metrics")]
    #[test]
    fn register_preinitializes_counters() {
        let registry = prometheus::Registry::new();
        let metrics = GdsClientMetrics::register(&registry).expect("register");
        let handle = MetricsHandle::default();
        handle.install(metrics);
        handle.rpc(
            "ListPools",
            Code::Unavailable,
            Some(Duration::from_millis(5)),
        );
        handle.watch_event("pools", "upsert");
        let families = registry.gather();
        let names: Vec<&str> = families.iter().map(|family| family.name()).collect();
        for expected in [
            "nvcf_gds_client_requests_total",
            "nvcf_gds_client_retries_total",
            "nvcf_gds_client_connection_attempts_total",
            "nvcf_gds_client_failovers_total",
            "nvcf_gds_client_watch_events_total",
            "nvcf_gds_client_watch_resyncs_total",
            "nvcf_gds_client_pool_heartbeats_total",
            "nvcf_gds_client_pool_registrations_total",
        ] {
            assert!(
                names.contains(&expected),
                "{expected} missing from {names:?}"
            );
        }
        let requests = families
            .iter()
            .find(|family| family.name() == "nvcf_gds_client_requests_total")
            .expect("requests family");
        // One pre-initialised OK series per method plus the recorded one.
        assert_eq!(requests.get_metric().len(), METHODS.len() + 1);
        // A second registration on the same registry is rejected.
        assert!(GdsClientMetrics::register(&registry).is_err());
    }
}

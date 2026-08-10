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

use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use axum::Router;
use axum::extract::State;
use axum::http::{StatusCode, header};
use axum::response::IntoResponse;
use axum::routing::get;
use prometheus::core::{AtomicU64, GenericCounter};
use prometheus::{
    Encoder, GaugeVec, HistogramVec, IntCounterVec, IntGaugeVec, Opts, Registry, TextEncoder,
};
use tokio::net::TcpListener;
use tracing::{error, info};

use stargate_proto::pb::InferenceServerStatus;

use crate::queue_admission::{ObservedRequestState, RequestObservationTransition};
use crate::{CurrentModelStats, RequestObservation, RequestObservationState};
use stargate_runtime::OwnedTask;

const PREFIX: &str = "pylon_";

macro_rules! metric_type {
    (counter) => {
        IntCounterVec
    };
    (gauge) => {
        IntGaugeVec
    };
    (plain_gauge) => {
        IntGaugeVec
    };
    (float_gauge) => {
        GaugeVec
    };
    (histogram) => {
        HistogramVec
    };
}

macro_rules! new_metric {
    (plain_gauge, $name:expr, $help:expr, $labels:expr) => {
        IntGaugeVec::new(Opts::new($name, $help), $labels)
    };
    (gauge, $name:expr, $help:expr, $labels:expr) => {
        IntGaugeVec::new(prefixed_opts($name, $help), $labels)
    };
    (float_gauge, $name:expr, $help:expr, $labels:expr) => {
        GaugeVec::new(prefixed_opts($name, $help), $labels)
    };
    (counter, $name:expr, $help:expr, $labels:expr) => {
        IntCounterVec::new(prefixed_opts($name, $help), $labels)
    };
    (histogram, $name:expr, $help:expr, $labels:expr, $buckets:expr) => {
        HistogramVec::new(histogram_opts($name, $help, $buckets), $labels)
    };
}

macro_rules! metrics {
    (
        $($group:ident {
            $($kind:tt $field:ident(
                $metric_name:expr,
                $help:expr,
                [$($label:expr),* $(,)?]
                $(, $buckets:expr)?
            );)*
        })*
    ) => {
        /// Prometheus metrics for one pylon process.
        #[derive(Debug)]
        pub struct PylonMetrics {
            registry: Arc<Registry>,
            $($($field: metric_type!($kind),)*)*
        }

        impl PylonMetrics {
            pub fn new() -> anyhow::Result<Arc<Self>> {
                let registry = Arc::new(Registry::new());
                $($(let $field = new_metric!(
                    $kind,
                    $metric_name,
                    $help,
                    &[$($label),*]
                    $(, $buckets)?
                )?;
                registry.register(Box::new($field.clone()))?;)*)*
                Ok(Arc::new(Self { registry, $($($field,)*)* }))
            }
        }
    };
}

// Keep each descriptor on one line so its field, Prometheus name, help, labels, and buckets
// remain one auditable mapping. rustfmt would expand this table into constructor-shaped ceremony.
#[rustfmt::skip]
metrics! {
    process {
        plain_gauge target_info("target_info", "Target metadata", ["service_version", "service_name", "commit"]);
        gauge registration_stream_connected("registration_stream_connected", "Binary gauge: 1 when a stargate registration stream is connected", ["router"]);
        gauge reverse_tunnel_connected("reverse_tunnel_connected", "Binary gauge: 1 when a reverse QUIC tunnel is connected to a stargate router", ["router"]);
    }
    request {
        gauge inflight("requests_inflight", "Current number of observed requests in flight", ["model"]);
        gauge state("requests_state", "Current number of observed requests by client-side lifecycle state", ["model", "state"]);
        gauge state_input_tokens("requests_state_input_tokens", "Current input tokens for observed requests by client-side lifecycle state", ["model", "state"]);
        counter total("requests_total", "Total number of terminal requests observed by pylon", ["model", "routing_key", "status"]);
        histogram time_to_response_headers_seconds("request_time_to_response_headers_seconds", "Time from request start to upstream response headers", ["model", "routing_key"], DURATION_BUCKETS);
        histogram time_to_first_output_seconds("request_time_to_first_output_seconds", "Time from request start to first observed output message", ["model", "routing_key"], DURATION_BUCKETS);
        histogram time_to_first_token_seconds("request_time_to_first_token_seconds", "Time from request start to first observed output token", ["model", "routing_key"], DURATION_BUCKETS);
        histogram duration_seconds("request_duration_seconds", "Total observed duration for terminal requests", ["model", "routing_key", "status"], REQUEST_DURATION_BUCKETS);
        counter input_tokens("request_input_tokens_total", "Total input tokens observed on terminal requests", ["model", "routing_key", "status"]);
        counter output_tokens("request_output_tokens_total", "Total output tokens observed on terminal requests", ["model", "routing_key", "status"]);
        counter stats_sources_total("request_stats_sources_total", "Total terminal requests by stats source observed by pylon", ["model", "routing_key", "status", "source"]);
        histogram input_tokens_histogram("request_input_tokens", "Input tokens per terminal request", ["model", "routing_key", "status"], TOKEN_BUCKETS);
        histogram output_tokens_histogram("request_output_tokens", "Output tokens per terminal request", ["model", "routing_key", "status"], TOKEN_BUCKETS);
    }
    engine_stats {
        counter stream_events_total("engine_stats_stream_events_total", "Total engine stats stream events ingested by type", ["type"]);
        counter stream_invalid_events_total("engine_stats_stream_invalid_events_total", "Total invalid engine stats stream events by reason", ["reason"]);
        counter stream_reconnects_total("engine_stats_stream_reconnects_total", "Total engine stats stream reconnect attempts by reason", ["reason"]);
        gauge stream_connected("engine_stats_stream_connected", "Binary gauge: 1 when the engine stats stream is connected", ["mode"]);
        gauge live_requests("engine_stats_live_requests", "Current live request stats entries by source", ["source"]);
        gauge model_states("engine_stats_model_states", "Current engine stats aggregate model states by source", ["source"]);
        counter stale_cleanups_total("engine_stats_stale_cleanups_total", "Total stale engine stats cleanups by kind and source", ["kind", "source"]);
        counter dirty_snapshots_total("engine_stats_dirty_snapshots_total", "Total engine stats model snapshots marked dirty by source and reason", ["source", "reason"]);
        counter source_transitions_total("engine_stats_source_transitions_total", "Total engine stats source-selection transitions", ["from", "to", "reason"]);
    }
    discovery {
        counter discovery_polls_total("model_discovery_polls_total", "Total model-discovery polls by provider and outcome", ["provider", "outcome"]);
        gauge discovered_models("model_discovery_models", "Model count from the last valid discovery response", ["provider"]);
    }
    model {
        float_gauge output_tps("model_output_tps", "Current output TPS by model", ["model"]);
        float_gauge embedding_item_tps("model_embedding_item_tps", "Current embeddings item throughput by model", ["model"]);
        float_gauge last_mean_input_tps("model_last_mean_input_tps", "Last valid mean input TPS by model", ["model"]);
        float_gauge max_output_tps("model_max_output_tps", "Observed max output TPS by model", ["model"]);
        float_gauge max_embedding_item_tps("model_max_embedding_item_tps", "Observed max embeddings item throughput by model", ["model"]);
        float_gauge queue_size("model_queue_size", "Current queued request count by model", ["model"]);
        float_gauge queued_input_tokens("model_queued_input_tokens", "Current queued input tokens by model", ["model"]);
        float_gauge kv_cache_capacity_tokens("model_kv_cache_capacity_tokens", "Current KV cache capacity tokens by model", ["model"]);
        float_gauge kv_cache_used_tokens("model_kv_cache_used_tokens", "Current KV cache used tokens by model", ["model"]);
        float_gauge kv_cache_free_tokens("model_kv_cache_free_tokens", "Current KV cache free tokens by model", ["model"]);
        gauge stats_capability("model_stats_capability", "Binary gauge for observed stats capability labels by model", ["model", "capability"]);
        gauge stats_source("model_stats_source", "Binary gauge for observed stats source labels by model", ["model", "source"]);
        gauge advertised_status("model_advertised_status", "Current model status advertised to each stargate router; the active status label is 1 and other status labels are 0", ["router", "model", "status"]);
        histogram calibration_duration_ms("model_calibration_duration_ms", "Per-generation calibration traffic-ramp duration in milliseconds by model and outcome", ["model", "outcome"], CALIBRATION_BUCKETS);
    }
    retry {
        counter retryable_responses_total("retryable_responses_total", "Total number of retryable responses emitted or relayed by pylon", ["inference_server_id", "reason", "status"]);
        counter nonretryable_failures_total("nonretryable_failures_total", "Total number of upstream failures not marked retryable by pylon", ["inference_server_id", "reason"]);
    }
    queue_admission {
        counter decisions_total("queue_admission_decisions_total", "Total number of local queue mismatch admission decisions", ["inference_server_id", "model_id", "result"]);
        histogram expected_ms("queue_admission_expected_ms", "Expected queue milliseconds received from Stargate for local queue admission", ["inference_server_id", "model_id"], QUEUE_ADMISSION_BUCKETS);
        histogram actual_ms("queue_admission_actual_ms", "Actual local queue milliseconds used for queue mismatch admission", ["inference_server_id", "model_id"], QUEUE_ADMISSION_BUCKETS);
    }
    quality {
        counter checks_total("quality_checks_total", "Total number of quality checks by result", ["model", "result"]);
        counter threshold_matches_total("quality_threshold_matches_total", "Total number of requests that matched a quality threshold", ["model", "reason"]);
    }
}

macro_rules! metric_observer {
    (counter $name:ident($($arg:ident: $arg_type:ty),*) => $field:ident[$($label:expr),*]) => {
        pub fn $name(&self, $($arg: $arg_type),*) {
            self.$field.with_label_values(&[$($label),*]).inc();
        }
    };
    (bool_gauge $name:ident($($arg:ident: $arg_type:ty),*) => $field:ident[$($label:expr),*], $value:ident) => {
        pub fn $name(&self, $($arg: $arg_type),*) {
            self.$field
                .with_label_values(&[$($label),*])
                .set(i64::from($value));
        }
    };
    (count_gauge $name:ident($($arg:ident: $arg_type:ty),*) => $field:ident[$($label:expr),*], $value:ident) => {
        pub fn $name(&self, $($arg: $arg_type),*) {
            self.$field
                .with_label_values(&[$($label),*])
                .set(saturating_i64($value));
        }
    };
}

impl PylonMetrics {
    pub fn registry(&self) -> Arc<Registry> {
        self.registry.clone()
    }

    pub fn gather_text(&self) -> anyhow::Result<String> {
        let metric_families = self.registry.gather();
        let mut buffer = vec![];
        let encoder = TextEncoder::new();
        encoder.encode(&metric_families, &mut buffer)?;
        String::from_utf8(buffer).map_err(Into::into)
    }

    pub fn observe_target_info(&self, service_version: &str, service_name: &str, commit: &str) {
        self.target_info
            .with_label_values(&[service_version, service_name, commit])
            .set(1);
    }

    pub(crate) fn observe_request_transition(
        &self,
        observation: &RequestObservation,
        transition: &RequestObservationTransition,
    ) {
        if let Some(prior) = &transition.prior {
            self.adjust_observed_request(prior, -1);
        }
        if observation.is_terminal() {
            self.record_terminal_observation(observation, request_state_label(observation.state));
        }
        if let Some(current) = &transition.current {
            self.adjust_observed_request(current, 1);
        }
        for total in &transition.input_token_totals {
            self.state_input_tokens
                .with_label_values(&[
                    total.generation.model_id(),
                    request_state_label(total.state),
                ])
                .set(saturating_i64(total.input_tokens));
        }
    }

    metric_observer!(counter observe_engine_stats_stream_event(
        event_type: &'static str
    ) => stream_events_total[event_type]);
    metric_observer!(counter observe_engine_stats_invalid_event(
        reason: &'static str
    ) => stream_invalid_events_total[reason]);
    metric_observer!(counter observe_engine_stats_reconnect(
        reason: &'static str
    ) => stream_reconnects_total[reason]);
    metric_observer!(bool_gauge observe_engine_stats_stream_connected(
        mode: &'static str, connected: bool
    ) => stream_connected[mode], connected);
    metric_observer!(count_gauge observe_engine_stats_live_requests(
        source: &'static str, count: usize
    ) => live_requests[source], count);
    metric_observer!(count_gauge observe_engine_stats_model_states(
        source: &'static str, count: usize
    ) => model_states[source], count);
    metric_observer!(counter observe_engine_stats_stale_cleanup(
        kind: &'static str, source: &'static str
    ) => stale_cleanups_total[kind, source]);
    metric_observer!(counter observe_engine_stats_dirty_snapshot(
        source: &'static str, reason: &'static str
    ) => dirty_snapshots_total[source, reason]);
    metric_observer!(counter observe_engine_stats_source_transition(
        from: &'static str, to: &'static str, reason: &'static str
    ) => source_transitions_total[from, to, reason]);

    pub(crate) fn observe_model_discovery_poll(
        &self,
        provider: &str,
        outcome: &'static str,
        model_count: Option<usize>,
    ) {
        self.discovery_polls_total
            .with_label_values(&[provider, outcome])
            .inc();
        if let Some(model_count) = model_count {
            self.discovered_models
                .with_label_values(&[provider])
                .set(saturating_i64(model_count));
        }
    }

    pub fn observe_model_stats(&self, model_id: &str, stats: &CurrentModelStats) {
        for (gauge, value) in [
            (&self.output_tps, stats.output_tps),
            (&self.embedding_item_tps, stats.embedding_item_tps),
            (&self.last_mean_input_tps, stats.last_mean_input_tps),
            (&self.max_output_tps, stats.max_output_tps),
            (&self.max_embedding_item_tps, stats.max_embedding_item_tps),
            (&self.queue_size, stats.queue_size as f64),
            (&self.queued_input_tokens, stats.queued_input_size as f64),
            (
                &self.kv_cache_capacity_tokens,
                stats.kv_cache_capacity_tokens as f64,
            ),
            (
                &self.kv_cache_used_tokens,
                stats.kv_cache_used_tokens as f64,
            ),
            (
                &self.kv_cache_free_tokens,
                stats.kv_cache_free_tokens as f64,
            ),
        ] {
            gauge.with_label_values(&[model_id]).set(value);
        }
        for (gauge, values) in [
            (&self.stats_capability, &stats.stats_capabilities),
            (&self.stats_source, &stats.stats_sources),
        ] {
            for value in values {
                gauge.with_label_values(&[model_id, value]).set(1);
            }
        }
    }

    pub fn observe_model_advertised_status(
        &self,
        router_addr: &str,
        model_id: &str,
        status: InferenceServerStatus,
    ) {
        for (known_status, label) in [
            (InferenceServerStatus::Active, "active"),
            (InferenceServerStatus::Inactive, "inactive"),
            (InferenceServerStatus::Unknown, "unknown"),
        ] {
            let value = i64::from(status == known_status);
            self.advertised_status
                .with_label_values(&[router_addr, model_id, label])
                .set(value);
        }
    }

    pub(crate) fn remove_model_advertised_status(&self, router_addr: &str, model_id: &str) {
        for status in ["active", "inactive", "unknown"] {
            let _ = self
                .advertised_status
                .remove_label_values(&[router_addr, model_id, status]);
        }
    }

    pub(crate) fn remove_model_gauges(&self, model_id: &str, stats: Option<&CurrentModelStats>) {
        for gauge in [
            &self.output_tps,
            &self.embedding_item_tps,
            &self.last_mean_input_tps,
            &self.max_output_tps,
            &self.max_embedding_item_tps,
            &self.queue_size,
            &self.queued_input_tokens,
            &self.kv_cache_capacity_tokens,
            &self.kv_cache_used_tokens,
            &self.kv_cache_free_tokens,
        ] {
            let _ = gauge.remove_label_values(&[model_id]);
        }
        let _ = self.inflight.remove_label_values(&[model_id]);
        for state in [
            "queued",
            "upstream_connecting",
            "input_processing",
            "output_generation",
            "complete",
            "failed",
            "cancelled",
        ] {
            let _ = self.state.remove_label_values(&[model_id, state]);
            let _ = self
                .state_input_tokens
                .remove_label_values(&[model_id, state]);
        }
        if let Some(stats) = stats {
            for capability in &stats.stats_capabilities {
                let _ = self
                    .stats_capability
                    .remove_label_values(&[model_id, capability]);
            }
            for source in &stats.stats_sources {
                let _ = self.stats_source.remove_label_values(&[model_id, source]);
            }
        }
    }

    pub(crate) fn observe_model_calibration_duration(
        &self,
        model_id: &str,
        duration: Duration,
        outcome: CalibrationOutcome,
    ) {
        self.calibration_duration_ms
            .with_label_values(&[model_id, outcome.as_str()])
            .observe(duration.as_secs_f64() * 1_000.0);
    }

    metric_observer!(bool_gauge observe_registration_stream_connected(
        router_addr: &str, connected: bool
    ) => registration_stream_connected[router_addr], connected);
    metric_observer!(bool_gauge observe_reverse_tunnel_connected(
        router_addr: &str, connected: bool
    ) => reverse_tunnel_connected[router_addr], connected);

    #[inline]
    pub fn retryable_responses_total(
        &self,
        inference_server_id: &str,
        reason: &str,
        status: &str,
    ) -> GenericCounter<AtomicU64> {
        self.retryable_responses_total
            .with_label_values(&[inference_server_id, reason, status])
    }

    #[inline]
    pub fn nonretryable_failures_total(
        &self,
        inference_server_id: &str,
        reason: &str,
    ) -> GenericCounter<AtomicU64> {
        self.nonretryable_failures_total
            .with_label_values(&[inference_server_id, reason])
    }

    pub fn observe_queue_admission_decision(
        &self,
        inference_server_id: &str,
        model_id: &str,
        result: &str,
        expected_ms: Option<u64>,
        actual_ms: Option<u64>,
    ) {
        self.decisions_total
            .with_label_values(&[inference_server_id, model_id, result])
            .inc();
        for (histogram, milliseconds) in [
            (&self.expected_ms, expected_ms),
            (&self.actual_ms, actual_ms),
        ] {
            if let Some(milliseconds) = milliseconds {
                histogram
                    .with_label_values(&[inference_server_id, model_id])
                    .observe(milliseconds as f64);
            }
        }
    }

    metric_observer!(counter observe_quality_check_result(
        model_id: &str, result: &str
    ) => checks_total[model_id, result]);
    metric_observer!(counter observe_quality_threshold_match(
        model_id: &str, reason: &str
    ) => threshold_matches_total[model_id, reason]);

    fn adjust_observed_request(&self, request: &ObservedRequestState, delta: i64) {
        let model_id = request.generation.model_id();
        let state = request_state_label(request.state);
        self.inflight.with_label_values(&[model_id]).add(delta);
        self.state.with_label_values(&[model_id, state]).add(delta);
    }

    fn record_terminal_observation(&self, observation: &RequestObservation, state: &'static str) {
        let routing_key = observation.routing_key.as_deref().unwrap_or("");
        let labels = [observation.model_id.as_str(), routing_key, state];
        self.total.with_label_values(&labels).inc();
        for (histogram, duration) in [
            (
                &self.time_to_response_headers_seconds,
                observation.time_to_response_headers,
            ),
            (
                &self.time_to_first_output_seconds,
                observation.time_to_first_output,
            ),
            (
                &self.time_to_first_token_seconds,
                observation.time_to_first_token,
            ),
        ] {
            if let Some(duration) = duration {
                histogram
                    .with_label_values(&[&observation.model_id, routing_key])
                    .observe(duration.as_secs_f64());
            }
        }
        self.duration_seconds
            .with_label_values(&labels)
            .observe(observation.total_duration.as_secs_f64());
        self.input_tokens
            .with_label_values(&labels)
            .inc_by(observation.input_tokens);
        self.output_tokens
            .with_label_values(&labels)
            .inc_by(observation.output_tokens);
        if observation.output_tokens_from_chunk_usage {
            self.stats_sources_total
                .with_label_values(&[&observation.model_id, routing_key, state, "chunk_usage"])
                .inc();
        }
        self.input_tokens_histogram
            .with_label_values(&labels)
            .observe(observation.input_tokens as f64);
        self.output_tokens_histogram
            .with_label_values(&labels)
            .observe(observation.output_tokens as f64);
    }
}

fn prefixed_opts(metric_name: &str, help: &str) -> Opts {
    Opts::new(format!("{PREFIX}{metric_name}"), help)
}

fn histogram_opts(metric_name: &str, help: &str, buckets: &[f64]) -> prometheus::HistogramOpts {
    prometheus::HistogramOpts::new(format!("{PREFIX}{metric_name}"), help).buckets(buckets.to_vec())
}

const DURATION_BUCKETS: &[f64] = &[
    0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0,
];
const REQUEST_DURATION_BUCKETS: &[f64] = &[
    0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0, 60.0,
];
const TOKEN_BUCKETS: &[f64] = &[
    1.0, 2.5, 5.0, 7.5, 10.0, 25.0, 50.0, 75.0, 100.0, 250.0, 500.0, 750.0, 1_000.0, 2_500.0,
    5_000.0, 7_500.0, 10_000.0, 25_000.0, 50_000.0, 75_000.0, 100_000.0, 250_000.0, 500_000.0,
];
const CALIBRATION_BUCKETS: &[f64] = &[
    10.0, 25.0, 50.0, 100.0, 250.0, 500.0, 1_000.0, 2_500.0, 5_000.0, 10_000.0, 30_000.0, 60_000.0,
    120_000.0, 300_000.0, 600_000.0,
];

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum CalibrationOutcome {
    Completed,
    Error,
    Cancelled,
}

impl CalibrationOutcome {
    fn as_str(self) -> &'static str {
        match self {
            Self::Completed => "completed",
            Self::Error => "error",
            Self::Cancelled => "cancelled",
        }
    }
}
const QUEUE_ADMISSION_BUCKETS: &[f64] = &[
    0.0, 1.0, 5.0, 10.0, 25.0, 50.0, 100.0, 250.0, 500.0, 1_000.0, 2_500.0, 5_000.0, 10_000.0,
    30_000.0, 60_000.0,
];

fn request_state_label(state: RequestObservationState) -> &'static str {
    match state {
        RequestObservationState::Queued => "queued",
        RequestObservationState::UpstreamConnecting => "upstream_connecting",
        RequestObservationState::InputProcessing => "input_processing",
        RequestObservationState::OutputGeneration => "output_generation",
        RequestObservationState::Complete => "complete",
        RequestObservationState::Failed => "failed",
        RequestObservationState::Cancelled => "cancelled",
    }
}

fn saturating_i64(value: impl TryInto<i64>) -> i64 {
    // Prometheus integer gauges use i64; counts and token totals saturate instead of wrapping.
    value.try_into().unwrap_or(i64::MAX)
}

async fn get_metrics(
    State(registry): State<Arc<Registry>>,
) -> Result<impl IntoResponse, StatusCode> {
    let metric_families = registry.gather();
    let mut buffer = vec![];
    let encoder = TextEncoder::new();
    encoder.encode(&metric_families, &mut buffer).map_err(|e| {
        error!("failed to encode pylon metrics: {}", e);
        StatusCode::INTERNAL_SERVER_ERROR
    })?;
    Ok((
        StatusCode::OK,
        [(header::CONTENT_TYPE, encoder.format_type().to_string())],
        buffer,
    ))
}

owned_task_handle!(MetricsServerHandle);

pub async fn start_metrics_server(
    addr: SocketAddr,
    registry: Arc<Registry>,
) -> anyhow::Result<MetricsServerHandle> {
    let router = Router::new()
        .route("/metrics", get(get_metrics))
        .with_state(registry);

    let listener = TcpListener::bind(addr).await?;
    info!(addr = %addr, "pylon metrics server listening");

    let task = OwnedTask::spawn("metrics server", move |stop| async move {
        if let Err(error) = axum::serve(listener, router)
            .with_graceful_shutdown(stop.cancelled_owned())
            .await
        {
            error!(error = %error, "pylon metrics server failed");
        }
    });
    Ok(MetricsServerHandle { task })
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;
    use std::time::Duration;

    use stargate_proto::pb::InferenceServerStatus;

    use super::CalibrationOutcome;

    use crate::{
        CurrentModelStats, PylonMetrics, PylonRuntimeState, RequestObservation,
        RequestObservationEndpoint, RequestObservationState,
    };

    fn observation(
        request_id: &str,
        state: RequestObservationState,
        input_tokens: u64,
        output_tokens: u64,
    ) -> RequestObservation {
        RequestObservation {
            endpoint: RequestObservationEndpoint::ChatCompletions,
            request_id: request_id.to_string(),
            routing_key: Some("rk-a".to_string()),
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: Some(200),
            output_messages: u64::from(output_tokens > 0),
            output_tokens,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
            state,
            time_to_response_headers: Some(Duration::from_millis(10)),
            time_to_first_output: Some(Duration::from_millis(20)),
            time_to_first_token: Some(Duration::from_millis(25)),
            total_duration: Duration::from_millis(50),
        }
    }

    fn metrics_runtime(metrics: Arc<PylonMetrics>) -> PylonRuntimeState {
        PylonRuntimeState::observed(
            InferenceServerStatus::Unknown,
            &["model-a".to_string()],
            4,
            Some(metrics),
        )
        .0
    }

    fn assert_metrics(metrics: &PylonMetrics, expected: &[&str]) -> String {
        let body = metrics.gather_text().expect("metrics should encode");
        for expected in expected {
            assert!(body.contains(expected), "missing metric sample: {expected}");
        }
        body
    }

    #[test]
    fn target_info_and_connectivity_gauges_are_recorded() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_target_info("0.1.0", "pylon", "abc123");
        metrics.observe_registration_stream_connected("router-a", true);
        metrics.observe_reverse_tunnel_connected("router-a", true);

        assert_metrics(
            &metrics,
            &[
                r#"target_info{commit="abc123",service_name="pylon",service_version="0.1.0"} 1"#,
                r#"pylon_registration_stream_connected{router="router-a"} 1"#,
                r#"pylon_reverse_tunnel_connected{router="router-a"} 1"#,
            ],
        );
    }

    #[test]
    fn engine_stats_metrics_are_recorded_by_mode_source_and_reason() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_engine_stats_stream_event("stats");
        metrics.observe_engine_stats_invalid_event("json_parse");
        metrics.observe_engine_stats_reconnect("eof");
        metrics.observe_engine_stats_stream_connected("auto", true);
        metrics.observe_engine_stats_live_requests("engine_stats_stream", 2);
        metrics.observe_engine_stats_model_states("openai_fallback", 3);
        metrics.observe_engine_stats_stale_cleanup("request", "engine_stats_stream");
        metrics.observe_engine_stats_dirty_snapshot("openai_fallback", "missing_model");
        metrics.observe_engine_stats_source_transition(
            "openai_fallback",
            "engine_stats_stream",
            "fresh_stream",
        );

        assert_metrics(
            &metrics,
            &[
                r#"pylon_engine_stats_stream_events_total{type="stats"} 1"#,
                r#"pylon_engine_stats_stream_invalid_events_total{reason="json_parse"} 1"#,
                r#"pylon_engine_stats_stream_reconnects_total{reason="eof"} 1"#,
                r#"pylon_engine_stats_stream_connected{mode="auto"} 1"#,
                r#"pylon_engine_stats_live_requests{source="engine_stats_stream"} 2"#,
                r#"pylon_engine_stats_model_states{source="openai_fallback"} 3"#,
                r#"pylon_engine_stats_stale_cleanups_total{kind="request",source="engine_stats_stream"} 1"#,
                r#"pylon_engine_stats_dirty_snapshots_total{reason="missing_model",source="openai_fallback"} 1"#,
                r#"pylon_engine_stats_source_transitions_total{from="openai_fallback",reason="fresh_stream",to="engine_stats_stream"} 1"#,
            ],
        );
    }

    #[test]
    fn model_discovery_metrics_record_outcomes_and_last_valid_count() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_model_discovery_poll("dynamo", "success", Some(2));
        metrics.observe_model_discovery_poll("dynamo", "error", None);

        assert_metrics(
            &metrics,
            &[
                r#"pylon_model_discovery_polls_total{outcome="success",provider="dynamo"} 1"#,
                r#"pylon_model_discovery_polls_total{outcome="error",provider="dynamo"} 1"#,
                r#"pylon_model_discovery_models{provider="dynamo"} 2"#,
            ],
        );
    }

    #[test]
    fn retiring_model_removes_current_model_gauges() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let stats = CurrentModelStats {
            last_mean_input_tps: 12.0,
            stats_capabilities: vec!["input_tps".to_string()],
            stats_sources: vec!["engine_stats_stream".to_string()],
            ..CurrentModelStats::default()
        };
        metrics.observe_model_stats("model-a", &stats);

        metrics.remove_model_gauges("model-a", Some(&stats));

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(!body.contains(r#"model="model-a""#));
    }

    #[test]
    fn retry_failure_and_queue_admission_metrics_are_recorded() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics
            .retryable_responses_total("pylon-a", "upstream_status", "503")
            .inc();
        metrics
            .nonretryable_failures_total("pylon-a", "local_connect_failure")
            .inc();
        metrics.observe_queue_admission_decision(
            "pylon-a",
            "model-a",
            "rejected",
            Some(17),
            Some(23),
        );

        assert_metrics(
            &metrics,
            &[
                r#"pylon_retryable_responses_total{inference_server_id="pylon-a",reason="upstream_status",status="503"} 1"#,
                r#"pylon_nonretryable_failures_total{inference_server_id="pylon-a",reason="local_connect_failure"} 1"#,
                r#"pylon_queue_admission_decisions_total{inference_server_id="pylon-a",model_id="model-a",result="rejected"} 1"#,
                r#"pylon_queue_admission_expected_ms_count{inference_server_id="pylon-a",model_id="model-a"} 1"#,
                r#"pylon_queue_admission_expected_ms_sum{inference_server_id="pylon-a",model_id="model-a"} 17"#,
                r#"pylon_queue_admission_actual_ms_count{inference_server_id="pylon-a",model_id="model-a"} 1"#,
                r#"pylon_queue_admission_actual_ms_sum{inference_server_id="pylon-a",model_id="model-a"} 23"#,
            ],
        );
    }

    #[test]
    fn request_observations_update_state_gauges_and_terminal_counters() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let runtime_state = metrics_runtime(metrics.clone());

        runtime_state.transition_request_observation(observation(
            "req-1",
            RequestObservationState::InputProcessing,
            11,
            0,
        ));
        assert_metrics(
            &metrics,
            &[
                r#"pylon_requests_state{model="model-a",state="input_processing"} 1"#,
                r#"pylon_requests_state_input_tokens{model="model-a",state="input_processing"} 11"#,
            ],
        );

        runtime_state.transition_request_observation(observation(
            "req-1",
            RequestObservationState::OutputGeneration,
            11,
            3,
        ));
        assert_metrics(
            &metrics,
            &[
                r#"pylon_requests_state{model="model-a",state="input_processing"} 0"#,
                r#"pylon_requests_state{model="model-a",state="output_generation"} 1"#,
            ],
        );

        runtime_state.transition_request_observation(observation(
            "req-1",
            RequestObservationState::Complete,
            11,
            3,
        ));
        assert_metrics(
            &metrics,
            &[
                r#"pylon_requests_state{model="model-a",state="output_generation"} 0"#,
                r#"pylon_requests_total{model="model-a",routing_key="rk-a",status="complete"} 1"#,
                r#"pylon_request_duration_seconds_count{model="model-a",routing_key="rk-a",status="complete"} 1"#,
                r#"pylon_request_time_to_response_headers_seconds_count{model="model-a",routing_key="rk-a"} 1"#,
                r#"pylon_request_time_to_first_token_seconds_count{model="model-a",routing_key="rk-a"} 1"#,
                r#"pylon_request_input_tokens_total{model="model-a",routing_key="rk-a",status="complete"} 11"#,
                r#"pylon_request_output_tokens_total{model="model-a",routing_key="rk-a",status="complete"} 3"#,
                r#"pylon_request_input_tokens_count{model="model-a",routing_key="rk-a",status="complete"} 1"#,
                r#"pylon_request_output_tokens_count{model="model-a",routing_key="rk-a",status="complete"} 1"#,
            ],
        );
    }

    #[test]
    fn state_input_token_gauge_saturates_across_requests() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let runtime_state = metrics_runtime(metrics.clone());
        let transitions = ["req-1", "req-2"].map(|request_id| {
            let runtime_state = runtime_state.clone();
            std::thread::spawn(move || {
                runtime_state.transition_request_observation(observation(
                    request_id,
                    RequestObservationState::InputProcessing,
                    u64::MAX,
                    0,
                ));
            })
        });
        for transition in transitions {
            transition.join().unwrap();
        }
        let input_tokens = || {
            metrics
                .state_input_tokens
                .with_label_values(&["model-a", "input_processing"])
                .get()
        };
        assert_eq!(input_tokens(), i64::MAX);

        for request_id in ["req-1", "req-2"] {
            runtime_state.transition_request_observation(observation(
                request_id,
                RequestObservationState::Complete,
                u64::MAX,
                0,
            ));
            let expected = if request_id == "req-1" { i64::MAX } else { 0 };
            assert_eq!(input_tokens(), expected);
        }
    }

    #[test]
    fn terminal_observations_record_stats_sources() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let runtime_state = metrics_runtime(metrics.clone());
        let mut observation = observation("req-1", RequestObservationState::Complete, 11, 3);
        observation.output_tokens_from_chunk_usage = true;

        runtime_state.transition_request_observation(observation);

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.lines().any(|line| {
            line.starts_with("pylon_request_stats_sources_total{")
                && line.contains(r#"model="model-a""#)
                && line.contains(r#"routing_key="rk-a""#)
                && line.contains(r#"status="complete""#)
                && line.contains(r#"source="chunk_usage""#)
                && line.ends_with(" 1")
        }));
    }

    #[test]
    fn model_stats_update_capacity_and_queue_gauges() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_model_stats(
            "model-a",
            &CurrentModelStats {
                output_tps: 20.0,
                embedding_item_tps: 25.0,
                last_mean_input_tps: 30.0,
                max_output_tps: 40.0,
                max_embedding_item_tps: 45.0,
                queue_size: 2,
                queued_input_size: 17,
                kv_cache_capacity_tokens: 100,
                kv_cache_used_tokens: 30,
                kv_cache_free_tokens: 70,
                stats_capabilities: vec!["model.throughput.engine_stream".to_string()],
                stats_sources: vec!["engine_stats_stream".to_string()],
                ..CurrentModelStats::default()
            },
        );

        assert_metrics(
            &metrics,
            &[
                r#"pylon_model_output_tps{model="model-a"} 20"#,
                r#"pylon_model_embedding_item_tps{model="model-a"} 25"#,
                r#"pylon_model_last_mean_input_tps{model="model-a"} 30"#,
                r#"pylon_model_max_embedding_item_tps{model="model-a"} 45"#,
                r#"pylon_model_queue_size{model="model-a"} 2"#,
                r#"pylon_model_queued_input_tokens{model="model-a"} 17"#,
                r#"pylon_model_kv_cache_free_tokens{model="model-a"} 70"#,
                r#"pylon_model_stats_capability{capability="model.throughput.engine_stream",model="model-a"} 1"#,
                r#"pylon_model_stats_source{model="model-a",source="engine_stats_stream"} 1"#,
            ],
        );
    }

    #[test]
    fn advertised_status_updates_router_model_status_gauges() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_model_advertised_status(
            "127.0.0.1:50071",
            "model-a",
            InferenceServerStatus::Active,
        );

        assert_metrics(
            &metrics,
            &[
                r#"pylon_model_advertised_status{model="model-a",router="127.0.0.1:50071",status="active"} 1"#,
                r#"pylon_model_advertised_status{model="model-a",router="127.0.0.1:50071",status="inactive"} 0"#,
            ],
        );
    }

    #[test]
    fn calibration_duration_histogram_is_recorded_by_model_and_outcome() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_model_calibration_duration(
            "model-a",
            Duration::from_millis(42),
            CalibrationOutcome::Completed,
        );
        metrics.observe_model_calibration_duration(
            "model-a",
            Duration::from_millis(7),
            CalibrationOutcome::Error,
        );
        metrics.observe_model_calibration_duration(
            "model-a",
            Duration::from_millis(3),
            CalibrationOutcome::Cancelled,
        );

        assert_metrics(
            &metrics,
            &[
                r#"pylon_model_calibration_duration_ms_count{model="model-a",outcome="completed"} 1"#,
                r#"pylon_model_calibration_duration_ms_sum{model="model-a",outcome="completed"} 42"#,
                r#"pylon_model_calibration_duration_ms_count{model="model-a",outcome="error"} 1"#,
                r#"pylon_model_calibration_duration_ms_sum{model="model-a",outcome="error"} 7"#,
                r#"pylon_model_calibration_duration_ms_count{model="model-a",outcome="cancelled"} 1"#,
                r#"pylon_model_calibration_duration_ms_sum{model="model-a",outcome="cancelled"} 3"#,
            ],
        );
    }

    #[test]
    fn quality_check_counters_are_recorded() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_quality_check_result("model-a", "clean");
        metrics.observe_quality_check_result("model-a", "matched");
        metrics.observe_quality_check_result("model-a", "skipped");
        metrics.observe_quality_threshold_match("model-a", "repetition_1gram");

        assert_metrics(
            &metrics,
            &[
                r#"pylon_quality_checks_total{model="model-a",result="clean"} 1"#,
                r#"pylon_quality_checks_total{model="model-a",result="matched"} 1"#,
                r#"pylon_quality_checks_total{model="model-a",result="skipped"} 1"#,
                r#"pylon_quality_threshold_matches_total{model="model-a",reason="repetition_1gram"} 1"#,
            ],
        );
    }

    #[test]
    fn quality_check_metrics_are_isolated_by_model_and_reason() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_quality_check_result("model-a", "clean");
        metrics.observe_quality_check_result("model-b", "matched");
        metrics.observe_quality_threshold_match("model-a", "repetition_1gram");
        metrics.observe_quality_threshold_match("model-b", "degeneracy_score");

        let body = assert_metrics(
            &metrics,
            &[
                r#"pylon_quality_checks_total{model="model-a",result="clean"} 1"#,
                r#"pylon_quality_checks_total{model="model-b",result="matched"} 1"#,
                r#"pylon_quality_threshold_matches_total{model="model-a",reason="repetition_1gram"} 1"#,
                r#"pylon_quality_threshold_matches_total{model="model-b",reason="degeneracy_score"} 1"#,
            ],
        );
        assert!(!body.contains(
            r#"pylon_quality_threshold_matches_total{model="model-a",reason="degeneracy_score"}"#
        ));
    }
}

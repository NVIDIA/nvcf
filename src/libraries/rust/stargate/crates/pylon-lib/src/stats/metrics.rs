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

use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
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

use crate::{CurrentModelStats, RequestObservation, RequestObservationState};

const PREFIX: &str = "pylon_";

#[derive(Debug, Clone)]
struct TrackedRequest {
    model_id: String,
    state: &'static str,
    input_tokens: i64,
}

/// Prometheus metrics for one pylon process.
#[derive(Debug)]
pub struct PylonMetrics {
    registry: Arc<Registry>,
    target_info: IntGaugeVec,
    requests_inflight: IntGaugeVec,
    requests_state: IntGaugeVec,
    requests_state_input_tokens: IntGaugeVec,
    requests_total: IntCounterVec,
    request_time_to_response_headers_seconds: HistogramVec,
    request_time_to_first_output_seconds: HistogramVec,
    request_time_to_first_token_seconds: HistogramVec,
    request_duration_seconds: HistogramVec,
    request_input_tokens: IntCounterVec,
    request_output_tokens: IntCounterVec,
    request_stats_sources_total: IntCounterVec,
    engine_stats_stream_events_total: IntCounterVec,
    engine_stats_stream_invalid_events_total: IntCounterVec,
    engine_stats_stream_reconnects_total: IntCounterVec,
    engine_stats_stream_connected: IntGaugeVec,
    engine_stats_live_requests: IntGaugeVec,
    engine_stats_model_states: IntGaugeVec,
    engine_stats_stale_cleanups_total: IntCounterVec,
    engine_stats_dirty_snapshots_total: IntCounterVec,
    engine_stats_source_transitions_total: IntCounterVec,
    request_input_tokens_histogram: HistogramVec,
    request_output_tokens_histogram: HistogramVec,
    registration_stream_connected: IntGaugeVec,
    reverse_tunnel_connected: IntGaugeVec,
    model_output_tps: GaugeVec,
    model_embedding_item_tps: GaugeVec,
    model_last_mean_input_tps: GaugeVec,
    model_max_output_tps: GaugeVec,
    model_max_embedding_item_tps: GaugeVec,
    model_queue_size: GaugeVec,
    model_queued_input_tokens: GaugeVec,
    model_kv_cache_capacity_tokens: GaugeVec,
    model_kv_cache_used_tokens: GaugeVec,
    model_kv_cache_free_tokens: GaugeVec,
    model_stats_capability: IntGaugeVec,
    model_stats_source: IntGaugeVec,
    model_advertised_status: IntGaugeVec,
    model_calibration_duration_ms: HistogramVec,
    retryable_responses_total: IntCounterVec,
    nonretryable_failures_total: IntCounterVec,
    queue_admission_decisions_total: IntCounterVec,
    queue_admission_expected_ms: HistogramVec,
    queue_admission_actual_ms: HistogramVec,
    quality_checks_total: IntCounterVec,
    quality_threshold_matches_total: IntCounterVec,
    tracked_requests: Mutex<HashMap<String, TrackedRequest>>,
}

impl PylonMetrics {
    pub fn new() -> anyhow::Result<Arc<Self>> {
        let registry = Arc::new(Registry::new());

        let target_info = IntGaugeVec::new(
            Opts::new("target_info", "Target metadata"),
            &["service_version", "service_name", "commit"],
        )?;
        registry.register(Box::new(target_info.clone()))?;

        let requests_inflight = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}requests_inflight"),
                "Current number of proxied requests in flight",
            ),
            &["model"],
        )?;
        registry.register(Box::new(requests_inflight.clone()))?;

        let requests_state = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}requests_state"),
                "Current number of proxied requests by client-side lifecycle state",
            ),
            &["model", "state"],
        )?;
        registry.register(Box::new(requests_state.clone()))?;

        let requests_state_input_tokens = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}requests_state_input_tokens"),
                "Current input tokens for proxied requests by client-side lifecycle state",
            ),
            &["model", "state"],
        )?;
        registry.register(Box::new(requests_state_input_tokens.clone()))?;

        let requests_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}requests_total"),
                "Total number of terminal proxied requests observed by pylon",
            ),
            &["model", "routing_key", "status"],
        )?;
        registry.register(Box::new(requests_total.clone()))?;

        let request_time_to_response_headers_seconds = HistogramVec::new(
            duration_histogram_opts(
                "request_time_to_response_headers_seconds",
                "Time from request start to upstream response headers",
            ),
            &["model", "routing_key"],
        )?;
        registry.register(Box::new(request_time_to_response_headers_seconds.clone()))?;

        let request_time_to_first_output_seconds = HistogramVec::new(
            duration_histogram_opts(
                "request_time_to_first_output_seconds",
                "Time from request start to first observed output message",
            ),
            &["model", "routing_key"],
        )?;
        registry.register(Box::new(request_time_to_first_output_seconds.clone()))?;

        let request_time_to_first_token_seconds = HistogramVec::new(
            duration_histogram_opts(
                "request_time_to_first_token_seconds",
                "Time from request start to first observed output token",
            ),
            &["model", "routing_key"],
        )?;
        registry.register(Box::new(request_time_to_first_token_seconds.clone()))?;

        let request_duration_seconds = HistogramVec::new(
            prometheus::HistogramOpts::new(
                format!("{PREFIX}request_duration_seconds"),
                "Total observed duration for terminal proxied requests",
            )
            .buckets(vec![
                0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0, 60.0,
            ]),
            &["model", "routing_key", "status"],
        )?;
        registry.register(Box::new(request_duration_seconds.clone()))?;

        let request_input_tokens = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}request_input_tokens_total"),
                "Total input tokens observed on terminal proxied requests",
            ),
            &["model", "routing_key", "status"],
        )?;
        registry.register(Box::new(request_input_tokens.clone()))?;

        let request_output_tokens = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}request_output_tokens_total"),
                "Total output tokens observed on terminal proxied requests",
            ),
            &["model", "routing_key", "status"],
        )?;
        registry.register(Box::new(request_output_tokens.clone()))?;

        let request_stats_sources_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}request_stats_sources_total"),
                "Total terminal proxied requests by stats source observed by pylon",
            ),
            &["model", "routing_key", "status", "source"],
        )?;
        registry.register(Box::new(request_stats_sources_total.clone()))?;

        let engine_stats_stream_events_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}engine_stats_stream_events_total"),
                "Total engine stats stream events ingested by type",
            ),
            &["type"],
        )?;
        registry.register(Box::new(engine_stats_stream_events_total.clone()))?;

        let engine_stats_stream_invalid_events_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}engine_stats_stream_invalid_events_total"),
                "Total invalid engine stats stream events by reason",
            ),
            &["reason"],
        )?;
        registry.register(Box::new(engine_stats_stream_invalid_events_total.clone()))?;

        let engine_stats_stream_reconnects_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}engine_stats_stream_reconnects_total"),
                "Total engine stats stream reconnect attempts by reason",
            ),
            &["reason"],
        )?;
        registry.register(Box::new(engine_stats_stream_reconnects_total.clone()))?;

        let engine_stats_stream_connected = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}engine_stats_stream_connected"),
                "Binary gauge: 1 when the engine stats stream is connected",
            ),
            &["mode"],
        )?;
        registry.register(Box::new(engine_stats_stream_connected.clone()))?;

        let engine_stats_live_requests = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}engine_stats_live_requests"),
                "Current live request stats entries by source",
            ),
            &["source"],
        )?;
        registry.register(Box::new(engine_stats_live_requests.clone()))?;

        let engine_stats_model_states = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}engine_stats_model_states"),
                "Current engine stats aggregate model states by source",
            ),
            &["source"],
        )?;
        registry.register(Box::new(engine_stats_model_states.clone()))?;

        let engine_stats_stale_cleanups_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}engine_stats_stale_cleanups_total"),
                "Total stale engine stats cleanups by kind and source",
            ),
            &["kind", "source"],
        )?;
        registry.register(Box::new(engine_stats_stale_cleanups_total.clone()))?;

        let engine_stats_dirty_snapshots_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}engine_stats_dirty_snapshots_total"),
                "Total engine stats model snapshots marked dirty by source and reason",
            ),
            &["source", "reason"],
        )?;
        registry.register(Box::new(engine_stats_dirty_snapshots_total.clone()))?;

        let engine_stats_source_transitions_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}engine_stats_source_transitions_total"),
                "Total engine stats source-selection transitions",
            ),
            &["from", "to", "reason"],
        )?;
        registry.register(Box::new(engine_stats_source_transitions_total.clone()))?;

        let request_input_tokens_histogram = HistogramVec::new(
            token_histogram_opts(
                "request_input_tokens",
                "Input tokens per terminal proxied request",
            ),
            &["model", "routing_key", "status"],
        )?;
        registry.register(Box::new(request_input_tokens_histogram.clone()))?;

        let request_output_tokens_histogram = HistogramVec::new(
            token_histogram_opts(
                "request_output_tokens",
                "Output tokens per terminal proxied request",
            ),
            &["model", "routing_key", "status"],
        )?;
        registry.register(Box::new(request_output_tokens_histogram.clone()))?;

        let registration_stream_connected = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}registration_stream_connected"),
                "Binary gauge: 1 when a stargate registration stream is connected",
            ),
            &["router"],
        )?;
        registry.register(Box::new(registration_stream_connected.clone()))?;

        let reverse_tunnel_connected = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}reverse_tunnel_connected"),
                "Binary gauge: 1 when a reverse QUIC tunnel is connected to a stargate router",
            ),
            &["router"],
        )?;
        registry.register(Box::new(reverse_tunnel_connected.clone()))?;

        let model_output_tps = gauge_vec("model_output_tps", "Current output TPS by model")?;
        registry.register(Box::new(model_output_tps.clone()))?;
        let model_embedding_item_tps = gauge_vec(
            "model_embedding_item_tps",
            "Current embeddings item throughput by model",
        )?;
        registry.register(Box::new(model_embedding_item_tps.clone()))?;
        let model_last_mean_input_tps = gauge_vec(
            "model_last_mean_input_tps",
            "Last valid mean input TPS by model",
        )?;
        registry.register(Box::new(model_last_mean_input_tps.clone()))?;
        let model_max_output_tps =
            gauge_vec("model_max_output_tps", "Observed max output TPS by model")?;
        registry.register(Box::new(model_max_output_tps.clone()))?;
        let model_max_embedding_item_tps = gauge_vec(
            "model_max_embedding_item_tps",
            "Observed max embeddings item throughput by model",
        )?;
        registry.register(Box::new(model_max_embedding_item_tps.clone()))?;
        let model_queue_size =
            gauge_vec("model_queue_size", "Current queued request count by model")?;
        registry.register(Box::new(model_queue_size.clone()))?;
        let model_queued_input_tokens = gauge_vec(
            "model_queued_input_tokens",
            "Current queued input tokens by model",
        )?;
        registry.register(Box::new(model_queued_input_tokens.clone()))?;
        let model_kv_cache_capacity_tokens = gauge_vec(
            "model_kv_cache_capacity_tokens",
            "Current KV cache capacity tokens by model",
        )?;
        registry.register(Box::new(model_kv_cache_capacity_tokens.clone()))?;
        let model_kv_cache_used_tokens = gauge_vec(
            "model_kv_cache_used_tokens",
            "Current KV cache used tokens by model",
        )?;
        registry.register(Box::new(model_kv_cache_used_tokens.clone()))?;
        let model_kv_cache_free_tokens = gauge_vec(
            "model_kv_cache_free_tokens",
            "Current KV cache free tokens by model",
        )?;
        registry.register(Box::new(model_kv_cache_free_tokens.clone()))?;
        let model_stats_capability = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}model_stats_capability"),
                "Binary gauge for observed stats capability labels by model",
            ),
            &["model", "capability"],
        )?;
        registry.register(Box::new(model_stats_capability.clone()))?;
        let model_stats_source = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}model_stats_source"),
                "Binary gauge for observed stats source labels by model",
            ),
            &["model", "source"],
        )?;
        registry.register(Box::new(model_stats_source.clone()))?;

        let model_advertised_status = IntGaugeVec::new(
            Opts::new(
                format!("{PREFIX}model_advertised_status"),
                "Current model status advertised to each stargate router; the active status label is 1 and other status labels are 0",
            ),
            &["router", "model", "status"],
        )?;
        registry.register(Box::new(model_advertised_status.clone()))?;

        let model_calibration_duration_ms = HistogramVec::new(
            prometheus::HistogramOpts::new(
                format!("{PREFIX}model_calibration_duration_ms"),
                "Local bringup calibration duration in milliseconds by model and outcome",
            )
            .buckets(vec![
                10.0, 25.0, 50.0, 100.0, 250.0, 500.0, 1_000.0, 2_500.0, 5_000.0, 10_000.0,
                30_000.0, 60_000.0, 120_000.0, 300_000.0, 600_000.0,
            ]),
            &["model", "outcome"],
        )?;
        registry.register(Box::new(model_calibration_duration_ms.clone()))?;

        let retryable_responses_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}retryable_responses_total"),
                "Total number of retryable responses emitted or relayed by pylon",
            ),
            &["inference_server_id", "reason", "status"],
        )?;
        registry.register(Box::new(retryable_responses_total.clone()))?;

        let nonretryable_failures_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}nonretryable_failures_total"),
                "Total number of upstream failures not marked retryable by pylon",
            ),
            &["inference_server_id", "reason"],
        )?;
        registry.register(Box::new(nonretryable_failures_total.clone()))?;

        let queue_admission_decisions_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}queue_admission_decisions_total"),
                "Total number of local queue mismatch admission decisions",
            ),
            &["inference_server_id", "model_id", "result"],
        )?;
        registry.register(Box::new(queue_admission_decisions_total.clone()))?;

        let queue_admission_expected_ms = HistogramVec::new(
            prometheus::HistogramOpts::new(
                format!("{PREFIX}queue_admission_expected_ms"),
                "Expected queue milliseconds received from Stargate for local queue admission",
            )
            .buckets(vec![
                0.0, 1.0, 5.0, 10.0, 25.0, 50.0, 100.0, 250.0, 500.0, 1_000.0, 2_500.0, 5_000.0,
                10_000.0, 30_000.0, 60_000.0,
            ]),
            &["inference_server_id", "model_id"],
        )?;
        registry.register(Box::new(queue_admission_expected_ms.clone()))?;

        let queue_admission_actual_ms = HistogramVec::new(
            prometheus::HistogramOpts::new(
                format!("{PREFIX}queue_admission_actual_ms"),
                "Actual local queue milliseconds used for queue mismatch admission",
            )
            .buckets(vec![
                0.0, 1.0, 5.0, 10.0, 25.0, 50.0, 100.0, 250.0, 500.0, 1_000.0, 2_500.0, 5_000.0,
                10_000.0, 30_000.0, 60_000.0,
            ]),
            &["inference_server_id", "model_id"],
        )?;
        registry.register(Box::new(queue_admission_actual_ms.clone()))?;

        let quality_checks_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}quality_checks_total"),
                "Total number of quality checks by result",
            ),
            &["model", "result"],
        )?;
        registry.register(Box::new(quality_checks_total.clone()))?;

        let quality_threshold_matches_total = IntCounterVec::new(
            Opts::new(
                format!("{PREFIX}quality_threshold_matches_total"),
                "Total number of requests that matched a quality threshold",
            ),
            &["model", "reason"],
        )?;
        registry.register(Box::new(quality_threshold_matches_total.clone()))?;

        Ok(Arc::new(Self {
            registry,
            target_info,
            requests_inflight,
            requests_state,
            requests_state_input_tokens,
            requests_total,
            request_time_to_response_headers_seconds,
            request_time_to_first_output_seconds,
            request_time_to_first_token_seconds,
            request_duration_seconds,
            request_input_tokens,
            request_output_tokens,
            request_stats_sources_total,
            engine_stats_stream_events_total,
            engine_stats_stream_invalid_events_total,
            engine_stats_stream_reconnects_total,
            engine_stats_stream_connected,
            engine_stats_live_requests,
            engine_stats_model_states,
            engine_stats_stale_cleanups_total,
            engine_stats_dirty_snapshots_total,
            engine_stats_source_transitions_total,
            request_input_tokens_histogram,
            request_output_tokens_histogram,
            registration_stream_connected,
            reverse_tunnel_connected,
            model_output_tps,
            model_embedding_item_tps,
            model_last_mean_input_tps,
            model_max_output_tps,
            model_max_embedding_item_tps,
            model_queue_size,
            model_queued_input_tokens,
            model_kv_cache_capacity_tokens,
            model_kv_cache_used_tokens,
            model_kv_cache_free_tokens,
            model_stats_capability,
            model_stats_source,
            model_advertised_status,
            model_calibration_duration_ms,
            retryable_responses_total,
            nonretryable_failures_total,
            queue_admission_decisions_total,
            queue_admission_expected_ms,
            queue_admission_actual_ms,
            quality_checks_total,
            quality_threshold_matches_total,
            tracked_requests: Mutex::new(HashMap::new()),
        }))
    }

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

    pub fn observe_request_observation(&self, observation: &RequestObservation) {
        let state = request_state_label(observation.state);
        let input_tokens = saturating_i64(observation.input_tokens);
        let mut tracked_requests = self
            .tracked_requests
            .lock()
            .expect("pylon metrics request tracker mutex poisoned");

        if let Some(prior) = tracked_requests.remove(&observation.request_id) {
            self.decrement_tracked_request(&prior);
        }

        if observation.is_terminal() {
            self.record_terminal_observation(observation, state);
        } else {
            self.requests_inflight
                .with_label_values(&[&observation.model_id])
                .inc();
            self.requests_state
                .with_label_values(&[&observation.model_id, state])
                .inc();
            self.requests_state_input_tokens
                .with_label_values(&[&observation.model_id, state])
                .add(input_tokens);
            tracked_requests.insert(
                observation.request_id.clone(),
                TrackedRequest {
                    model_id: observation.model_id.clone(),
                    state,
                    input_tokens,
                },
            );
        }
    }

    pub fn observe_engine_stats_stream_event(&self, event_type: &'static str) {
        self.engine_stats_stream_events_total
            .with_label_values(&[event_type])
            .inc();
    }

    pub fn observe_engine_stats_invalid_event(&self, reason: &'static str) {
        self.engine_stats_stream_invalid_events_total
            .with_label_values(&[reason])
            .inc();
    }

    pub fn observe_engine_stats_reconnect(&self, reason: &'static str) {
        self.engine_stats_stream_reconnects_total
            .with_label_values(&[reason])
            .inc();
    }

    pub fn observe_engine_stats_stream_connected(&self, mode: &'static str, connected: bool) {
        self.engine_stats_stream_connected
            .with_label_values(&[mode])
            .set(i64::from(connected));
    }

    pub fn observe_engine_stats_live_requests(&self, source: &'static str, count: usize) {
        self.engine_stats_live_requests
            .with_label_values(&[source])
            .set(saturating_i64(count as u64));
    }

    pub fn observe_engine_stats_model_states(&self, source: &'static str, count: usize) {
        self.engine_stats_model_states
            .with_label_values(&[source])
            .set(saturating_i64(count as u64));
    }

    pub fn observe_engine_stats_stale_cleanup(&self, kind: &'static str, source: &'static str) {
        self.engine_stats_stale_cleanups_total
            .with_label_values(&[kind, source])
            .inc();
    }

    pub fn observe_engine_stats_dirty_snapshot(&self, source: &'static str, reason: &'static str) {
        self.engine_stats_dirty_snapshots_total
            .with_label_values(&[source, reason])
            .inc();
    }

    pub fn observe_engine_stats_source_transition(
        &self,
        from: &'static str,
        to: &'static str,
        reason: &'static str,
    ) {
        self.engine_stats_source_transitions_total
            .with_label_values(&[from, to, reason])
            .inc();
    }

    pub fn observe_model_stats(&self, model_id: &str, stats: &CurrentModelStats) {
        self.model_output_tps
            .with_label_values(&[model_id])
            .set(stats.output_tps);
        self.model_embedding_item_tps
            .with_label_values(&[model_id])
            .set(stats.embedding_item_tps);
        self.model_last_mean_input_tps
            .with_label_values(&[model_id])
            .set(stats.last_mean_input_tps);
        self.model_max_output_tps
            .with_label_values(&[model_id])
            .set(stats.max_output_tps);
        self.model_max_embedding_item_tps
            .with_label_values(&[model_id])
            .set(stats.max_embedding_item_tps);
        self.model_queue_size
            .with_label_values(&[model_id])
            .set(stats.queue_size as f64);
        self.model_queued_input_tokens
            .with_label_values(&[model_id])
            .set(stats.queued_input_size as f64);
        self.model_kv_cache_capacity_tokens
            .with_label_values(&[model_id])
            .set(stats.kv_cache_capacity_tokens as f64);
        self.model_kv_cache_used_tokens
            .with_label_values(&[model_id])
            .set(stats.kv_cache_used_tokens as f64);
        self.model_kv_cache_free_tokens
            .with_label_values(&[model_id])
            .set(stats.kv_cache_free_tokens as f64);
        for capability in &stats.stats_capabilities {
            self.model_stats_capability
                .with_label_values(&[model_id, capability])
                .set(1);
        }
        for source in &stats.stats_sources {
            self.model_stats_source
                .with_label_values(&[model_id, source])
                .set(1);
        }
    }

    pub fn observe_model_advertised_status(
        &self,
        router_addr: &str,
        model_id: &str,
        status: InferenceServerStatus,
    ) {
        for known_status in [
            InferenceServerStatus::Active,
            InferenceServerStatus::Inactive,
            InferenceServerStatus::Unknown,
        ] {
            let value = i64::from(status == known_status);
            self.model_advertised_status
                .with_label_values(&[router_addr, model_id, status_label(known_status)])
                .set(value);
        }
    }

    pub fn observe_model_calibration_duration(
        &self,
        model_id: &str,
        duration: Duration,
        success: bool,
    ) {
        let outcome = if success { "success" } else { "failure" };
        self.model_calibration_duration_ms
            .with_label_values(&[model_id, outcome])
            .observe(duration.as_secs_f64() * 1_000.0);
    }

    pub fn observe_registration_stream_connected(&self, router_addr: &str, connected: bool) {
        self.registration_stream_connected
            .with_label_values(&[router_addr])
            .set(i64::from(connected));
    }

    pub fn observe_reverse_tunnel_connected(&self, router_addr: &str, connected: bool) {
        self.reverse_tunnel_connected
            .with_label_values(&[router_addr])
            .set(i64::from(connected));
    }

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
        self.queue_admission_decisions_total
            .with_label_values(&[inference_server_id, model_id, result])
            .inc();
        if let Some(expected_ms) = expected_ms {
            self.queue_admission_expected_ms
                .with_label_values(&[inference_server_id, model_id])
                .observe(expected_ms as f64);
        }
        if let Some(actual_ms) = actual_ms {
            self.queue_admission_actual_ms
                .with_label_values(&[inference_server_id, model_id])
                .observe(actual_ms as f64);
        }
    }

    pub fn observe_quality_check_result(&self, model_id: &str, result: &str) {
        self.quality_checks_total
            .with_label_values(&[model_id, result])
            .inc();
    }

    pub fn observe_quality_threshold_match(&self, model_id: &str, reason: &str) {
        self.quality_threshold_matches_total
            .with_label_values(&[model_id, reason])
            .inc();
    }

    fn decrement_tracked_request(&self, request: &TrackedRequest) {
        self.requests_inflight
            .with_label_values(&[&request.model_id])
            .dec();
        self.requests_state
            .with_label_values(&[&request.model_id, request.state])
            .dec();
        self.requests_state_input_tokens
            .with_label_values(&[&request.model_id, request.state])
            .sub(request.input_tokens);
    }

    fn record_terminal_observation(&self, observation: &RequestObservation, state: &'static str) {
        let routing_key = observation.routing_key.as_deref().unwrap_or("");
        self.requests_total
            .with_label_values(&[&observation.model_id, routing_key, state])
            .inc();
        if let Some(time_to_response_headers) = observation.time_to_response_headers {
            self.request_time_to_response_headers_seconds
                .with_label_values(&[&observation.model_id, routing_key])
                .observe(time_to_response_headers.as_secs_f64());
        }
        if let Some(time_to_first_output) = observation.time_to_first_output {
            self.request_time_to_first_output_seconds
                .with_label_values(&[&observation.model_id, routing_key])
                .observe(time_to_first_output.as_secs_f64());
        }
        if let Some(time_to_first_token) = observation.time_to_first_token {
            self.request_time_to_first_token_seconds
                .with_label_values(&[&observation.model_id, routing_key])
                .observe(time_to_first_token.as_secs_f64());
        }
        self.request_duration_seconds
            .with_label_values(&[&observation.model_id, routing_key, state])
            .observe(observation.total_duration.as_secs_f64());
        self.request_input_tokens
            .with_label_values(&[&observation.model_id, routing_key, state])
            .inc_by(observation.input_tokens);
        self.request_output_tokens
            .with_label_values(&[&observation.model_id, routing_key, state])
            .inc_by(observation.output_tokens);
        if observation.output_tokens_from_chunk_usage {
            self.request_stats_sources_total
                .with_label_values(&[&observation.model_id, routing_key, state, "chunk_usage"])
                .inc();
        }
        self.request_input_tokens_histogram
            .with_label_values(&[&observation.model_id, routing_key, state])
            .observe(observation.input_tokens as f64);
        self.request_output_tokens_histogram
            .with_label_values(&[&observation.model_id, routing_key, state])
            .observe(observation.output_tokens as f64);
    }
}

fn duration_histogram_opts(metric_name: &str, help: &str) -> prometheus::HistogramOpts {
    prometheus::HistogramOpts::new(format!("{PREFIX}{metric_name}"), help).buckets(vec![
        0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0,
    ])
}

fn token_histogram_opts(metric_name: &str, help: &str) -> prometheus::HistogramOpts {
    prometheus::HistogramOpts::new(format!("{PREFIX}{metric_name}"), help).buckets(vec![
        1.0, 2.5, 5.0, 7.5, 10.0, 25.0, 50.0, 75.0, 100.0, 250.0, 500.0, 750.0, 1_000.0, 2_500.0,
        5_000.0, 7_500.0, 10_000.0, 25_000.0, 50_000.0, 75_000.0, 100_000.0, 250_000.0, 500_000.0,
    ])
}

fn gauge_vec(metric_name: &str, help: &str) -> prometheus::Result<GaugeVec> {
    GaugeVec::new(
        Opts::new(format!("{PREFIX}{metric_name}"), help),
        &["model"],
    )
}

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

fn status_label(status: InferenceServerStatus) -> &'static str {
    match status {
        InferenceServerStatus::Active => "active",
        InferenceServerStatus::Inactive => "inactive",
        InferenceServerStatus::Unknown => "unknown",
    }
}

fn saturating_i64(value: u64) -> i64 {
    // Prometheus integer gauges use i64; token counters saturate instead of wrapping.
    value.min(i64::MAX as u64) as i64
}

struct MetricsServerState {
    registry: Arc<Registry>,
}

async fn get_metrics(
    State(state): State<Arc<MetricsServerState>>,
) -> Result<impl IntoResponse, StatusCode> {
    let metric_families = state.registry.gather();
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

pub async fn start_metrics_server(addr: SocketAddr, registry: Arc<Registry>) -> anyhow::Result<()> {
    let router = Router::new()
        .route("/metrics", get(get_metrics))
        .with_state(Arc::new(MetricsServerState { registry }));

    let listener = TcpListener::bind(addr).await?;
    info!(addr = %addr, "pylon metrics server listening");

    axum::serve(listener, router).await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use stargate_proto::pb::InferenceServerStatus;

    use crate::{
        CurrentModelStats, PylonMetrics, RequestObservation, RequestObservationEndpoint,
        RequestObservationState,
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

    #[test]
    fn target_info_and_connectivity_gauges_are_recorded() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_target_info("0.1.0", "pylon", "abc123");
        metrics.observe_registration_stream_connected("router-a", true);
        metrics.observe_reverse_tunnel_connected("router-a", true);

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(
            r#"target_info{commit="abc123",service_name="pylon",service_version="0.1.0"} 1"#
        ));
        assert!(body.contains(r#"pylon_registration_stream_connected{router="router-a"} 1"#));
        assert!(body.contains(r#"pylon_reverse_tunnel_connected{router="router-a"} 1"#));
    }

    #[test]
    fn request_observations_update_state_gauges_and_terminal_counters() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_request_observation(&observation(
            "req-1",
            RequestObservationState::InputProcessing,
            11,
            0,
        ));
        let body = metrics.gather_text().expect("metrics should encode");
        assert!(
            body.contains(r#"pylon_requests_state{model="model-a",state="input_processing"} 1"#)
        );
        assert!(body.contains(
            r#"pylon_requests_state_input_tokens{model="model-a",state="input_processing"} 11"#
        ));

        metrics.observe_request_observation(&observation(
            "req-1",
            RequestObservationState::OutputGeneration,
            11,
            3,
        ));
        let body = metrics.gather_text().expect("metrics should encode");
        assert!(
            body.contains(r#"pylon_requests_state{model="model-a",state="input_processing"} 0"#)
        );
        assert!(
            body.contains(r#"pylon_requests_state{model="model-a",state="output_generation"} 1"#)
        );

        metrics.observe_request_observation(&observation(
            "req-1",
            RequestObservationState::Complete,
            11,
            3,
        ));
        let body = metrics.gather_text().expect("metrics should encode");
        assert!(
            body.contains(r#"pylon_requests_state{model="model-a",state="output_generation"} 0"#)
        );
        assert!(body.contains(
            r#"pylon_requests_total{model="model-a",routing_key="rk-a",status="complete"} 1"#
        ));
        assert!(body.contains(
            r#"pylon_request_duration_seconds_count{model="model-a",routing_key="rk-a",status="complete"} 1"#
        ));
        assert!(body.contains(
            r#"pylon_request_time_to_response_headers_seconds_count{model="model-a",routing_key="rk-a"} 1"#
        ));
        assert!(body.contains(
            r#"pylon_request_time_to_first_token_seconds_count{model="model-a",routing_key="rk-a"} 1"#
        ));
        assert!(body.contains(
            r#"pylon_request_input_tokens_total{model="model-a",routing_key="rk-a",status="complete"} 11"#
        ));
        assert!(body.contains(
            r#"pylon_request_output_tokens_total{model="model-a",routing_key="rk-a",status="complete"} 3"#
        ));
        assert!(body.contains(
            r#"pylon_request_input_tokens_count{model="model-a",routing_key="rk-a",status="complete"} 1"#
        ));
        assert!(body.contains(
            r#"pylon_request_output_tokens_count{model="model-a",routing_key="rk-a",status="complete"} 1"#
        ));
    }

    #[test]
    fn terminal_observations_record_stats_sources() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let mut observation = observation("req-1", RequestObservationState::Complete, 11, 3);
        observation.output_tokens_from_chunk_usage = true;

        metrics.observe_request_observation(&observation);

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

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(r#"pylon_model_output_tps{model="model-a"} 20"#));
        assert!(body.contains(r#"pylon_model_embedding_item_tps{model="model-a"} 25"#));
        assert!(body.contains(r#"pylon_model_last_mean_input_tps{model="model-a"} 30"#));
        assert!(body.contains(r#"pylon_model_max_embedding_item_tps{model="model-a"} 45"#));
        assert!(body.contains(r#"pylon_model_queue_size{model="model-a"} 2"#));
        assert!(body.contains(r#"pylon_model_queued_input_tokens{model="model-a"} 17"#));
        assert!(body.contains(r#"pylon_model_kv_cache_free_tokens{model="model-a"} 70"#));
        assert!(body.contains(
            r#"pylon_model_stats_capability{capability="model.throughput.engine_stream",model="model-a"} 1"#
        ));
        assert!(body.contains(
            r#"pylon_model_stats_source{model="model-a",source="engine_stats_stream"} 1"#
        ));
    }

    #[test]
    fn advertised_status_updates_router_model_status_gauges() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_model_advertised_status(
            "127.0.0.1:50071",
            "model-a",
            InferenceServerStatus::Active,
        );

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(
            r#"pylon_model_advertised_status{model="model-a",router="127.0.0.1:50071",status="active"} 1"#
        ));
        assert!(body.contains(
            r#"pylon_model_advertised_status{model="model-a",router="127.0.0.1:50071",status="inactive"} 0"#
        ));
    }

    #[test]
    fn calibration_duration_histogram_is_recorded_by_model_and_outcome() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_model_calibration_duration("model-a", Duration::from_millis(42), true);
        metrics.observe_model_calibration_duration("model-a", Duration::from_millis(7), false);

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(
            r#"pylon_model_calibration_duration_ms_count{model="model-a",outcome="success"} 1"#
        ));
        assert!(body.contains(
            r#"pylon_model_calibration_duration_ms_sum{model="model-a",outcome="success"} 42"#
        ));
        assert!(body.contains(
            r#"pylon_model_calibration_duration_ms_count{model="model-a",outcome="failure"} 1"#
        ));
        assert!(body.contains(
            r#"pylon_model_calibration_duration_ms_sum{model="model-a",outcome="failure"} 7"#
        ));
    }

    #[test]
    fn quality_check_counters_are_recorded() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_quality_check_result("model-a", "clean");
        metrics.observe_quality_check_result("model-a", "matched");
        metrics.observe_quality_check_result("model-a", "skipped");
        metrics.observe_quality_threshold_match("model-a", "repetition_1gram");

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(r#"pylon_quality_checks_total{model="model-a",result="clean"} 1"#));
        assert!(body.contains(r#"pylon_quality_checks_total{model="model-a",result="matched"} 1"#));
        assert!(body.contains(r#"pylon_quality_checks_total{model="model-a",result="skipped"} 1"#));
        assert!(body.contains(
            r#"pylon_quality_threshold_matches_total{model="model-a",reason="repetition_1gram"} 1"#
        ));
    }

    #[test]
    fn quality_check_metrics_are_isolated_by_model_and_reason() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");

        metrics.observe_quality_check_result("model-a", "clean");
        metrics.observe_quality_check_result("model-b", "matched");
        metrics.observe_quality_threshold_match("model-a", "repetition_1gram");
        metrics.observe_quality_threshold_match("model-b", "degeneracy_score");

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(r#"pylon_quality_checks_total{model="model-a",result="clean"} 1"#));
        assert!(body.contains(r#"pylon_quality_checks_total{model="model-b",result="matched"} 1"#));
        assert!(body.contains(
            r#"pylon_quality_threshold_matches_total{model="model-a",reason="repetition_1gram"} 1"#
        ));
        assert!(body.contains(
            r#"pylon_quality_threshold_matches_total{model="model-b",reason="degeneracy_score"} 1"#
        ));
        assert!(!body.contains(
            r#"pylon_quality_threshold_matches_total{model="model-a",reason="degeneracy_score"}"#
        ));
    }
}

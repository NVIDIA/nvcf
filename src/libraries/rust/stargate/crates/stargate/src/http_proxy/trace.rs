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

use axum::http::HeaderMap;
use tracing::{Span, field};
use tracing_opentelemetry::OpenTelemetrySpanExt;

use crate::routing_state::{RoutedClusterSnapshot, RoutedInferenceServerSnapshot};
use crate::telemetry::parent_context_from_headers;

macro_rules! record_fields {
    ($span:expr; $($name:literal = $value:expr),+ $(,)?) => {
        $($span.record($name, $value);)+
    };
}

pub(super) fn proxy_openai_request_span(headers: &HeaderMap) -> Span {
    let span = tracing::info_span!(
        "proxy_openai_request",
        request.endpoint = field::Empty,
        request.routing_key = field::Empty,
        request.model_id = field::Empty,
        request.path = field::Empty,
        request.input_tokens = field::Empty,
        request.priority = field::Empty,
        request.max_wait_ms = field::Empty,
        request.slo_ms = field::Empty,
        request.cache_affinity_key_present = field::Empty,
        routing.requested_algorithm = field::Empty,
        routing.invalid_requested_algorithm = field::Empty,
        selected_cluster.id = field::Empty,
        selected_inst.id = field::Empty,
        selected_inst.output_tps = field::Empty,
        selected_inst.last_mean_input_tps = field::Empty,
        selected_inst.max_output_tps = field::Empty,
        selected_inst.queue_size = field::Empty,
        selected_inst.queued_input_size = field::Empty,
        selected_inst.num_running_queries = field::Empty,
        selected_inst.max_engine_concurrency = field::Empty,
        selected_inst.total_query_input_size = field::Empty,
        selected_inst.kv_cache_capacity_tokens = field::Empty,
        selected_inst.kv_cache_used_tokens = field::Empty,
        selected_inst.kv_cache_free_tokens = field::Empty,
        selected_inst.rtt_ms = field::Empty,
        selected_inst.snapshot_age_ms = field::Empty,
        routing.algorithm = field::Empty,
        routing.num_candidates = field::Empty,
        routing.rank_depth = field::Empty,
        routing.selected_after_kv_free_tokens_skip = field::Empty,
        routing.retry_attempts = field::Empty,
        routing.admission_rejection_reason = field::Empty,
        proxy.upstream_status = field::Empty,
        proxy.time_to_first_byte_ms = field::Empty,
        proxy.attempt = field::Empty,
        proxy.connect_attempts = field::Empty,
        proxy.request_retries = field::Empty,
        proxy.failed_backends = field::Empty,
        proxy.queue.expected_ms = field::Empty,
        proxy.retry_reason = field::Empty,
        proxy.replay_body_bytes = field::Empty,
    );
    let _ = span.set_parent(parent_context_from_headers(headers));
    span
}

pub(super) struct RequestTraceFields<'a> {
    pub(super) routing_key: Option<&'a str>,
    pub(super) model_id: &'a str,
    pub(super) request_path: &'a str,
    pub(super) input_tokens: u64,
    pub(super) priority: u32,
    pub(super) max_wait_ms: Option<u64>,
    pub(super) request_slo_ms: Option<u64>,
    pub(super) cache_affinity_key_present: bool,
}

pub(super) fn record_request_to_span(span: &Span, fields: RequestTraceFields<'_>) {
    record_fields!(span;
        "request.routing_key" = fields.routing_key.unwrap_or(""),
        "request.model_id" = fields.model_id,
        "request.path" = fields.request_path,
        "request.input_tokens" = fields.input_tokens,
        "request.priority" = fields.priority,
        "request.max_wait_ms" = fields.max_wait_ms.unwrap_or(0),
        "request.slo_ms" = fields.request_slo_ms.unwrap_or(0),
        "request.cache_affinity_key_present" = fields.cache_affinity_key_present,
    );
}

pub(super) struct RoutingTraceFields<'a> {
    pub(super) routing_algorithm: &'a str,
    pub(super) requested_algorithm: Option<&'a str>,
    pub(super) num_candidates: usize,
    pub(super) rank_depth: usize,
    pub(super) selected_after_kv_free_tokens_skip: bool,
    pub(super) cluster: &'a RoutedClusterSnapshot,
    pub(super) chosen: &'a RoutedInferenceServerSnapshot,
}

pub(super) fn record_routing_to_span(span: &Span, routing: RoutingTraceFields<'_>) {
    let stats = &routing.cluster.stats;
    record_fields!(span;
        "routing.algorithm" = routing.routing_algorithm,
        "routing.requested_algorithm" = routing.requested_algorithm.unwrap_or(""),
        "routing.num_candidates" = routing.num_candidates,
        "routing.rank_depth" = routing.rank_depth as i64,
        "routing.selected_after_kv_free_tokens_skip" = routing.selected_after_kv_free_tokens_skip,
        "selected_cluster.id" = &routing.cluster.cluster_id,
        "selected_inst.id" = routing.chosen.inference_server_id.as_str(),
        "selected_inst.output_tps" = stats.output_tps,
        "selected_inst.last_mean_input_tps" = stats.last_mean_input_tps,
        "selected_inst.max_output_tps" = stats.max_output_tps,
        "selected_inst.queue_size" = stats.queue_size,
        "selected_inst.queued_input_size" = stats.queued_input_size,
        "selected_inst.num_running_queries" = stats.num_running_queries,
        "selected_inst.max_engine_concurrency" = stats.max_engine_concurrency,
        "selected_inst.total_query_input_size" = stats.total_query_input_size,
        "selected_inst.kv_cache_capacity_tokens" = stats.kv_cache_capacity_tokens,
        "selected_inst.kv_cache_used_tokens" = stats.kv_cache_used_tokens,
        "selected_inst.kv_cache_free_tokens" = stats.kv_cache_free_tokens,
        "selected_inst.rtt_ms" = routing.cluster.rtt.as_secs_f64() * 1000.0,
        "selected_inst.snapshot_age_ms" = routing.cluster.snapshot_updated_at.elapsed().as_secs_f64() * 1000.0,
    );
}

#[cfg(test)]
mod tests {
    use crate::telemetry::parent_context_from_headers;
    use axum::http::{HeaderMap, HeaderName, HeaderValue};
    use opentelemetry::global;
    use opentelemetry::trace::TraceContextExt;
    use opentelemetry_sdk::propagation::TraceContextPropagator;

    #[test]
    fn traceparent_header_extracts_remote_parent_context() {
        global::set_text_map_propagator(TraceContextPropagator::new());

        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static("traceparent"),
            HeaderValue::from_static("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
        );

        let parent_context = parent_context_from_headers(&headers);
        let span_context = parent_context.span().span_context().clone();

        assert!(span_context.is_valid());
        assert!(span_context.is_remote());
        assert_eq!(
            span_context.trace_id().to_string(),
            "4bf92f3577b34da6a3ce929d0e0e4736"
        );
        assert_eq!(span_context.span_id().to_string(), "00f067aa0ba902b7");
    }
}

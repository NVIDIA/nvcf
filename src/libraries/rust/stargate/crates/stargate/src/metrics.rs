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

use axum::Router;
use axum::extract::State;
use axum::http::{StatusCode, header};
use axum::response::IntoResponse;
use axum::routing::get;
use prometheus::core::{AtomicU64, GenericCounter};
use prometheus::{
    Encoder, Histogram, HistogramOpts, HistogramVec, IntCounterVec, IntGaugeVec, Opts, Registry,
    TextEncoder,
};
use tokio::net::TcpListener;
use tracing::{error, info};

pub const DEFAULT_PREFIX: &str = "stargate_";

/// Prometheus metrics for one stargate process. Uses a private [`Registry`] so
/// multiple runtimes (e.g. parallel integration tests) do not share collectors.
#[derive(Debug)]
pub struct StargateMetrics {
    registry: Arc<Registry>,
    requests_total: IntCounterVec,
    proxy_attempts_total: IntCounterVec,
    proxy_retries_total: IntCounterVec,
    routing_selections_total: IntCounterVec,
    routing_kv_free_token_fallback_selections_total: IntCounterVec,
    proxy_retry_exhausted_total: IntCounterVec,
    admission_rejections_total: IntCounterVec,
    quic_connection_evictions_total: IntCounterVec,
    quic_hot_path_reconnect_total: IntCounterVec,
    proxy_replay_buffer_bytes: HistogramVec,
    proxy_duration_seconds: HistogramVec,
    routing_duration_seconds: HistogramVec,
    active_inference_servers: IntGaugeVec,
}

fn register_int_counter_vec(
    registry: &Registry,
    name: String,
    help: &str,
    labels: &[&str],
) -> anyhow::Result<IntCounterVec> {
    let metric = IntCounterVec::new(Opts::new(name, help), labels)?;
    registry.register(Box::new(metric.clone()))?;
    Ok(metric)
}

fn register_int_gauge_vec(
    registry: &Registry,
    name: String,
    help: &str,
    labels: &[&str],
) -> anyhow::Result<IntGaugeVec> {
    let metric = IntGaugeVec::new(Opts::new(name, help), labels)?;
    registry.register(Box::new(metric.clone()))?;
    Ok(metric)
}

impl StargateMetrics {
    pub fn new() -> anyhow::Result<Arc<Self>> {
        Self::new_with_prefix(DEFAULT_PREFIX)
    }

    pub fn new_with_prefix(prefix: &str) -> anyhow::Result<Arc<Self>> {
        let registry = Arc::new(Registry::new());
        let metric_name = |suffix: &str| format!("{prefix}{suffix}");

        let requests_total = register_int_counter_vec(
            &registry,
            metric_name("requests_total"),
            "Total number of proxied requests",
            &["routing_key", "model", "inference_server_id", "status"],
        )?;

        let proxy_attempts_total = register_int_counter_vec(
            &registry,
            metric_name("proxy_attempts_total"),
            "Total number of upstream proxy attempts",
            &["routing_key", "model", "inference_server_id", "result"],
        )?;

        let proxy_retries_total = register_int_counter_vec(
            &registry,
            metric_name("proxy_retries_total"),
            "Total number of proxy retries",
            &["routing_key", "model", "reason"],
        )?;

        let routing_selections_total = register_int_counter_vec(
            &registry,
            metric_name("routing_selections_total"),
            "Total number of primary and ranked fallback cluster choices used for upstream attempts",
            &["routing_key", "model", "algorithm", "selection"],
        )?;

        let routing_kv_free_token_fallback_selections_total = register_int_counter_vec(
            &registry,
            metric_name("routing_kv_free_token_fallback_selections_total"),
            "Total number of selected routes reached after a higher-ranked candidate was skipped by KV free-token eligibility",
            &["routing_key", "model", "algorithm"],
        )?;

        let proxy_retry_exhausted_total = register_int_counter_vec(
            &registry,
            metric_name("proxy_retry_exhausted_total"),
            "Total number of proxy requests that exhausted retry options",
            &["routing_key", "model", "reason"],
        )?;

        let admission_rejections_total = register_int_counter_vec(
            &registry,
            metric_name("admission_rejections_total"),
            "Total number of requests rejected by local admission control",
            &["routing_key", "model", "reason"],
        )?;

        let quic_connection_evictions_total = register_int_counter_vec(
            &registry,
            metric_name("quic_connection_evictions_total"),
            "Total number of QUIC connection pool evictions",
            &["inference_server_id", "reason"],
        )?;

        let quic_hot_path_reconnect_total = register_int_counter_vec(
            &registry,
            metric_name("quic_hot_path_reconnect_total"),
            "Total number of direct QUIC reconnects attempted on the proxy hot path",
            &["inference_server_id", "result"],
        )?;

        let proxy_replay_buffer_bytes = HistogramVec::new(
            HistogramOpts::new(
                metric_name("proxy_replay_buffer_bytes"),
                "Replay buffer size for proxied request bodies",
            )
            .buckets(vec![
                0.0,
                1024.0,
                4096.0,
                16_384.0,
                65_536.0,
                262_144.0,
                1_048_576.0,
                4_194_304.0,
                16_777_216.0,
                67_108_864.0,
            ]),
            &["model"],
        )?;
        registry.register(Box::new(proxy_replay_buffer_bytes.clone()))?;

        let proxy_duration_seconds = HistogramVec::new(
            HistogramOpts::new(
                metric_name("proxy_duration_seconds"),
                "Time to first byte from upstream",
            )
            .buckets(vec![
                0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0,
            ]),
            &["routing_key", "model", "inference_server_id"],
        )?;
        registry.register(Box::new(proxy_duration_seconds.clone()))?;

        let routing_duration_seconds = HistogramVec::new(
            HistogramOpts::new(
                metric_name("routing_duration_seconds"),
                "Time spent selecting a inference server",
            )
            .buckets(vec![0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1]),
            &["routing_key", "model"],
        )?;
        registry.register(Box::new(routing_duration_seconds.clone()))?;

        let active_inference_servers = register_int_gauge_vec(
            &registry,
            metric_name("active_inference_servers"),
            "Active inference servers available for a routing target",
            &["routing_key", "model"],
        )?;

        Ok(Arc::new(Self {
            registry,
            requests_total,
            proxy_attempts_total,
            proxy_retries_total,
            routing_selections_total,
            routing_kv_free_token_fallback_selections_total,
            proxy_retry_exhausted_total,
            admission_rejections_total,
            quic_connection_evictions_total,
            quic_hot_path_reconnect_total,
            proxy_replay_buffer_bytes,
            proxy_duration_seconds,
            routing_duration_seconds,
            active_inference_servers,
        }))
    }

    pub fn registry(&self) -> Arc<Registry> {
        self.registry.clone()
    }

    #[cfg(test)]
    fn gather_text(&self) -> anyhow::Result<String> {
        let metric_families = self.registry.gather();
        let mut buffer = vec![];
        TextEncoder::new().encode(&metric_families, &mut buffer)?;
        String::from_utf8(buffer).map_err(Into::into)
    }

    #[inline]
    pub fn requests_total(
        &self,
        routing_key: Option<&str>,
        model: &str,
        inference_server_id: &str,
        status: &str,
    ) -> GenericCounter<AtomicU64> {
        self.requests_total.with_label_values(&[
            routing_key.unwrap_or(""),
            model,
            inference_server_id,
            status,
        ])
    }

    #[inline]
    pub fn proxy_attempts_total(
        &self,
        routing_key: Option<&str>,
        model: &str,
        inference_server_id: &str,
        result: &str,
    ) -> GenericCounter<AtomicU64> {
        self.proxy_attempts_total.with_label_values(&[
            routing_key.unwrap_or(""),
            model,
            inference_server_id,
            result,
        ])
    }

    #[inline]
    pub fn proxy_retries_total(
        &self,
        routing_key: Option<&str>,
        model: &str,
        reason: &str,
    ) -> GenericCounter<AtomicU64> {
        self.proxy_retries_total
            .with_label_values(&[routing_key.unwrap_or(""), model, reason])
    }

    #[inline]
    pub fn routing_selections_total(
        &self,
        routing_key: Option<&str>,
        model: &str,
        algorithm: &str,
        selection: &str,
    ) -> GenericCounter<AtomicU64> {
        self.routing_selections_total.with_label_values(&[
            routing_key.unwrap_or(""),
            model,
            algorithm,
            selection,
        ])
    }

    #[inline]
    pub fn routing_kv_free_token_fallback_selections_total(
        &self,
        routing_key: Option<&str>,
        model: &str,
        algorithm: &str,
    ) -> GenericCounter<AtomicU64> {
        self.routing_kv_free_token_fallback_selections_total
            .with_label_values(&[routing_key.unwrap_or(""), model, algorithm])
    }

    #[inline]
    pub fn proxy_retry_exhausted_total(
        &self,
        routing_key: Option<&str>,
        model: &str,
        reason: &str,
    ) -> GenericCounter<AtomicU64> {
        self.proxy_retry_exhausted_total.with_label_values(&[
            routing_key.unwrap_or(""),
            model,
            reason,
        ])
    }

    #[inline]
    pub fn admission_rejections_total(
        &self,
        routing_key: Option<&str>,
        model: &str,
        reason: &str,
    ) -> GenericCounter<AtomicU64> {
        self.admission_rejections_total.with_label_values(&[
            routing_key.unwrap_or(""),
            model,
            reason,
        ])
    }

    #[inline]
    pub fn quic_connection_evictions_total(
        &self,
        inference_server_id: &str,
        reason: &str,
    ) -> GenericCounter<AtomicU64> {
        self.quic_connection_evictions_total
            .with_label_values(&[inference_server_id, reason])
    }

    #[inline]
    pub fn quic_hot_path_reconnect_total(
        &self,
        inference_server_id: &str,
        result: &str,
    ) -> GenericCounter<AtomicU64> {
        self.quic_hot_path_reconnect_total
            .with_label_values(&[inference_server_id, result])
    }

    #[inline]
    pub fn proxy_replay_buffer_bytes(&self, model: &str) -> Histogram {
        self.proxy_replay_buffer_bytes.with_label_values(&[model])
    }

    #[inline]
    pub fn proxy_duration_seconds(
        &self,
        routing_key: Option<&str>,
        model: &str,
        inference_server_id: &str,
    ) -> Histogram {
        self.proxy_duration_seconds.with_label_values(&[
            routing_key.unwrap_or(""),
            model,
            inference_server_id,
        ])
    }

    #[inline]
    pub fn routing_duration_seconds(&self, routing_key: Option<&str>, model: &str) -> Histogram {
        self.routing_duration_seconds
            .with_label_values(&[routing_key.unwrap_or(""), model])
    }

    #[inline]
    pub fn set_active_inference_servers(
        &self,
        routing_key: Option<&str>,
        model: &str,
        count: usize,
    ) {
        self.active_inference_servers
            .with_label_values(&[routing_key.unwrap_or(""), model])
            .set(count.try_into().unwrap_or(i64::MAX));
    }
}

// -- Metrics HTTP server -----------------------------------------------------

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
        error!("failed to encode metrics: {}", e);
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
    info!(addr = %addr, "metrics server listening");

    axum::serve(listener, router).await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn metrics_prefix_is_applied_to_registered_collectors() {
        let metrics = StargateMetrics::new_with_prefix("llm_request_router_")
            .expect("metrics should initialize");

        metrics
            .requests_total(Some("routing-a"), "model-a", "server-a", "200")
            .inc();
        metrics
            .proxy_retries_total(Some("routing-a"), "model-a", "retryable_status")
            .inc();
        metrics
            .routing_selections_total(
                Some("routing-a"),
                "model-a",
                "pulsar-multiregion",
                "fallback",
            )
            .inc();
        metrics
            .routing_kv_free_token_fallback_selections_total(
                Some("routing-a"),
                "model-a",
                "pulsar-multiregion",
            )
            .inc();

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(
            body.contains("llm_request_router_requests_total"),
            "custom requests counter prefix missing:\n{body}"
        );
        assert!(
            body.contains("llm_request_router_proxy_retries_total"),
            "custom retry counter prefix missing:\n{body}"
        );
        assert!(
            body.contains("llm_request_router_routing_selections_total"),
            "custom routing-selection counter prefix missing:\n{body}"
        );
        assert!(
            body.contains(r#"selection="fallback""#),
            "routing-selection class label missing:\n{body}"
        );
        assert!(
            body.contains("llm_request_router_routing_kv_free_token_fallback_selections_total"),
            "custom KV-free-token fallback counter prefix missing:\n{body}"
        );
        assert!(
            !body.contains("stargate_requests_total"),
            "default stargate prefix leaked into custom metric output:\n{body}"
        );
    }

    #[test]
    fn default_metrics_prefix_keeps_stargate_metric_names() {
        let metrics = StargateMetrics::new().expect("metrics should initialize");

        metrics
            .requests_total(None, "model-a", "server-a", "200")
            .inc();

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(
            body.contains("stargate_requests_total"),
            "default stargate requests counter missing:\n{body}"
        );
    }
}

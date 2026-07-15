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

use super::core::{
    MAX_SPECULATIVE_REQUEST_BODY_PREALLOC_BYTES, TunnelServerApp, extend_body_from_buf,
    is_health_request_path, otel_parent_from_headers, pylon_upstream_parent_context,
    request_body_buffer, request_body_capacity, should_forward_header,
    should_forward_response_header,
};
use super::endpoint::{
    build_trusted_client_config, derive_sni, make_server_config, target_authority,
};
use super::reverse::{
    connect_first_reverse_quic_candidate, connect_reverse_quic_endpoint,
    reverse_quic_dial_candidates, reverse_quic_sni,
};
use super::*;
use std::collections::BTreeMap;
use std::error::Error as _;
use std::net::SocketAddr;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use anyhow::Result;
use axum::extract::Request;
use axum::http::{HeaderName, HeaderValue, StatusCode};
use axum::response::sse::Event;
use axum::response::{IntoResponse, Response};
use axum::routing::post;
use axum::{Json, Router, body::Body};
use bytes::{Buf, Bytes};
use futures::future;
use opentelemetry::trace::TraceContextExt;
use prometheus::{Encoder, TextEncoder};
use quinn::{ClientConfig, Endpoint};
use reqwest::header::HeaderMap;
use stargate_proto::pb::InferenceServerStatus;
use stargate_protocol::TunnelTransportProtocol;
use stargate_protocol::tunnel_contract::WEBTRANSPORT_TUNNEL_PATH;
use stargate_tls::ServerTlsIdentity;
use tokio::net::TcpListener;

use crate::queue_admission::QueueTrackedRequestGuard;
use crate::request_observer::{
    RequestObservationEndpoint, RequestObservationState, RequiredTunnelHeaders,
};
use crate::request_quality_monitor::RequestQualityMonitorConfig;
use crate::runtime_state::ModelGeneration;
use crate::stats::PylonMetrics;
use crate::{PylonRuntimeState, StatsCollectorConfig, start_stats_collector};

#[derive(Clone, Default)]
struct RecordingDebugSubscriber {
    events: Arc<std::sync::Mutex<Vec<BTreeMap<String, String>>>>,
}

impl RecordingDebugSubscriber {
    fn take_events(&self) -> Vec<BTreeMap<String, String>> {
        std::mem::take(
            &mut *self
                .events
                .lock()
                .expect("recorded tracing events should not be poisoned"),
        )
    }
}

fn event_by_message<'a>(
    events: &'a [BTreeMap<String, String>],
    message: &str,
) -> &'a BTreeMap<String, String> {
    events
        .iter()
        .find(|event| event.get("message").map(String::as_str) == Some(message))
        .unwrap_or_else(|| panic!("missing tracing event {message:?}"))
}

fn assert_event_field(event: &BTreeMap<String, String>, field: &str, expected: &str) {
    assert_eq!(
        event.get(field).map(String::as_str),
        Some(expected),
        "unexpected {field} field in {event:?}"
    );
}

impl tracing::Subscriber for RecordingDebugSubscriber {
    fn enabled(&self, metadata: &tracing::Metadata<'_>) -> bool {
        metadata.level() <= &tracing::Level::DEBUG
    }

    fn new_span(&self, _attrs: &tracing::span::Attributes<'_>) -> tracing::span::Id {
        tracing::span::Id::from_u64(1)
    }

    fn record(&self, _span: &tracing::span::Id, _values: &tracing::span::Record<'_>) {}

    fn record_follows_from(&self, _span: &tracing::span::Id, _follows: &tracing::span::Id) {}

    fn event(&self, event: &tracing::Event<'_>) {
        let mut visitor = RecordingFieldVisitor::default();
        event.record(&mut visitor);
        self.events
            .lock()
            .expect("recorded tracing events should not be poisoned")
            .push(visitor.fields);
    }

    fn enter(&self, _span: &tracing::span::Id) {}

    fn exit(&self, _span: &tracing::span::Id) {}
}

#[derive(Default)]
struct RecordingFieldVisitor {
    fields: BTreeMap<String, String>,
}

impl tracing::field::Visit for RecordingFieldVisitor {
    fn record_bool(&mut self, field: &tracing::field::Field, value: bool) {
        self.fields
            .insert(field.name().to_string(), value.to_string());
    }

    fn record_i64(&mut self, field: &tracing::field::Field, value: i64) {
        self.fields
            .insert(field.name().to_string(), value.to_string());
    }

    fn record_u64(&mut self, field: &tracing::field::Field, value: u64) {
        self.fields
            .insert(field.name().to_string(), value.to_string());
    }

    fn record_str(&mut self, field: &tracing::field::Field, value: &str) {
        self.fields
            .insert(field.name().to_string(), value.to_string());
    }

    fn record_debug(&mut self, field: &tracing::field::Field, value: &dyn std::fmt::Debug) {
        self.fields
            .insert(field.name().to_string(), format!("{value:?}"));
    }
}

type TestWebTransportConnectStream = h3::client::RequestStream<
    <h3_quinn::OpenStreams as h3::quic::OpenStreams<Bytes>>::BidiStream,
    Bytes,
>;

#[derive(Debug)]
struct TestSseEvent {
    event_name: Option<String>,
    data: String,
}

fn parse_test_sse_events(body: &str) -> Vec<TestSseEvent> {
    let normalized = body.replace("\r\n", "\n");
    assert!(
        normalized.is_empty() || normalized.ends_with("\n\n"),
        "SSE body ended with an incomplete event: {normalized:?}"
    );

    normalized
        .split("\n\n")
        .filter(|event| !event.is_empty())
        .filter_map(|raw_event| {
            let mut event_name = None;
            let mut data_lines = Vec::new();
            let mut saw_field = false;

            for line in raw_event.lines() {
                if line.starts_with(':') {
                    continue;
                }
                let (field, value) = line.split_once(':').unwrap_or((line, ""));
                let value = value.strip_prefix(' ').unwrap_or(value);
                match field {
                    "event" => {
                        event_name = Some(value.to_string());
                        saw_field = true;
                    }
                    "data" => {
                        data_lines.push(value);
                        saw_field = true;
                    }
                    _ => {}
                }
            }

            saw_field.then(|| TestSseEvent {
                event_name,
                data: data_lines.join("\n"),
            })
        })
        .collect()
}

fn test_sse_json_payloads(events: &[TestSseEvent]) -> Vec<serde_json::Value> {
    events
        .iter()
        .filter(|event| event.data.trim() != "[DONE]")
        .map(|event| serde_json::from_str(&event.data).unwrap())
        .collect()
}

fn observed_runtime(
    capacity: usize,
) -> (
    PylonRuntimeState,
    flume::Receiver<crate::RequestObservationEvent>,
) {
    PylonRuntimeState::observed(
        InferenceServerStatus::Unknown,
        &test_admitted_models(),
        capacity,
        None,
    )
}

async fn recv_terminal_observation(
    rx: &flume::Receiver<crate::RequestObservationEvent>,
) -> crate::RequestObservation {
    tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let observation = rx.recv_async().await.unwrap().into_observation();
            if observation.is_terminal() {
                break observation;
            }
        }
    })
    .await
    .unwrap()
}

struct DirectWebTransportSession {
    _endpoint: Endpoint,
    connection: quinn::Connection,
    _h3_connection: h3::client::Connection<h3_quinn::Connection, Bytes>,
    _connect_stream: TestWebTransportConnectStream,
    session_id: u64,
}

struct TunnelResponse {
    status: StatusCode,
    headers: HeaderMap,
    body: Vec<u8>,
}

const RETRY_CONTROL_REQUEST_HEADERS: [&str; 5] = [
    "x-stargate-upstream-retryable",
    "x-stargate-retryable",
    "x-stargate-retry-reason",
    "x-stargate-retry-after-ms",
    "x-vendor-retryable",
];
const TEST_ADMITTED_MODELS: [&str; 11] = [
    "model-a",
    "model-buffer-limit",
    "model-embed",
    "model-fallback",
    "model-h3",
    "model-progress-quality",
    "model-quality",
    "model-responses",
    "model-stream",
    "model-terminal-usage",
    "model-webtransport",
];

struct RawTunnelTest {
    tunnel: QuicHttpTunnelHandle,
    _endpoint: Endpoint,
    send: stargate_protocol::SendStream,
    recv: stargate_protocol::RecvStream,
}

impl RawTunnelTest {
    async fn start(config: QuicHttpTunnelConfig) -> Self {
        let tunnel = start_quic_http_tunnel(config).await.unwrap();
        let (_endpoint, send, recv) = open_test_tunnel_stream(tunnel.listen_addr()).await;
        Self {
            tunnel,
            _endpoint,
            send,
            recv,
        }
    }

    async fn send_json(&mut self, path: &str, model: &str, request_id: &str, body: &'static [u8]) {
        send_json_proxy_request(&mut self.send, path, model, request_id, body).await;
    }

    async fn send(&mut self, headers: HeaderMap, body: &'static [u8]) {
        send_proxy_request_with_headers(&mut self.send, headers, body).await;
    }

    async fn response_head(&mut self, expected_status: StatusCode) -> HeaderMap {
        let headers = self.recv.recv_header().await.unwrap();
        assert_eq!(
            headers.get("x-status").unwrap().to_str().unwrap(),
            expected_status.as_u16().to_string()
        );
        headers
    }

    async fn response(mut self, expected_status: StatusCode) -> TunnelResponse {
        let headers = self.response_head(expected_status).await;
        let body = read_response_bytes(&mut self.recv).await;
        self.shutdown().await;
        TunnelResponse {
            status: expected_status,
            headers,
            body,
        }
    }

    async fn drain(&mut self) {
        while self.recv.recv_body().await.unwrap().into_body().is_some() {}
    }

    async fn shutdown(self) {
        self.tunnel.shutdown().await;
    }
}

async fn observed_tunnel_for(
    app: Router,
) -> (
    RawTunnelTest,
    flume::Receiver<crate::RequestObservationEvent>,
) {
    let (runtime_state, observations) = observed_runtime(16);
    let mut config = test_tunnel_config_for(app).await;
    config.forwarding.runtime_state = runtime_state;
    (RawTunnelTest::start(config).await, observations)
}

fn assert_error_source(error: &TunnelError, expected: &str) {
    assert_eq!(
        error
            .source()
            .expect("error should expose its source")
            .to_string(),
        expected
    );
}

#[test]
fn reverse_tunnel_error_variants_preserve_source_chains() {
    let tls = TunnelError::Tls {
        source: anyhow::anyhow!("missing trusted cert"),
    };
    assert_error_source(&tls, "missing trusted cert");

    let connect = TunnelError::Connect {
        context: "resolving reverse tunnel address",
        source: std::io::Error::other("DNS lookup failed").into(),
    };
    assert_error_source(&connect, "DNS lookup failed");

    let handshake = TunnelError::Handshake {
        context: "reading reverse tunnel auth token",
        source: anyhow::anyhow!("auth token command failed"),
    };
    assert_error_source(&handshake, "auth token command failed");

    assert!(
        TunnelError::ConnectTimeout { timeout_ms: 10 }
            .source()
            .is_none(),
        "timeouts are typed terminal states, not wrapped sources"
    );
    assert!(
        TunnelError::HandshakeRejected {
            reason: "duplicate reverse tunnel".to_string()
        }
        .source()
        .is_none(),
        "peer rejection context should not invent a source error"
    );
}

#[test]
fn direct_and_reverse_configs_share_forwarding_defaults() {
    let direct = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().expect("listen addr should parse"),
        "http://upstream.local".to_string(),
    );
    let reverse = ReverseQuicTunnelConfig::new(
        "router.local:443".to_string(),
        "backend-1".to_string(),
        "http://upstream.local".to_string(),
    );

    assert_eq!(
        direct.upstream_http_base_url,
        reverse.upstream_http_base_url
    );
    assert_eq!(
        direct.forwarding.max_request_body_bytes,
        reverse.forwarding.max_request_body_bytes
    );
    assert_eq!(
        direct.forwarding.max_sse_buffer_bytes,
        reverse.forwarding.max_sse_buffer_bytes
    );
    assert_eq!(
        direct.forwarding.first_output_timeout,
        reverse.forwarding.first_output_timeout
    );
    assert_eq!(
        direct.forwarding.output_chunk_timeout,
        reverse.forwarding.output_chunk_timeout
    );
    assert!(direct.forwarding.metrics.is_none());
    assert!(reverse.forwarding.metrics.is_none());
}

#[test]
fn reverse_tunnel_app_from_config_preserves_forwarding_settings() {
    let (runtime_state, _observation_rx) = observed_runtime(16);
    let metrics = PylonMetrics::new().expect("metrics should initialize");
    let mut config = ReverseQuicTunnelConfig::new(
        "router.local:443".to_string(),
        "backend-1".to_string(),
        "http://upstream.local".to_string(),
    );
    config.forwarding.max_request_body_bytes = 1234;
    config.forwarding.max_sse_buffer_bytes = 321;
    config.forwarding.first_output_timeout = Duration::from_millis(55);
    config.forwarding.output_chunk_timeout = Duration::from_millis(77);
    config.forwarding.runtime_state = runtime_state.clone();
    config.forwarding.retry.local_connect_failures_retryable = true;
    config.forwarding.queue_mismatch_retry.enabled = false;
    config.forwarding.metrics = Some(metrics.clone());

    let app = TunnelServerApp::from_reverse_config(config);

    assert_eq!(app.inference_server_id, "backend-1");
    assert_eq!(app.upstream_http_base_url, "http://upstream.local");
    assert_eq!(app.max_request_body_bytes, 1234);
    assert_eq!(app.max_sse_buffer_bytes, 321);
    assert_eq!(app.first_output_timeout, Duration::from_millis(55));
    assert_eq!(app.output_chunk_timeout, Duration::from_millis(77));
    assert!(app.retry.local_connect_failures_retryable);
    assert!(!app.queue_mismatch_retry.enabled);
    assert!(Arc::ptr_eq(
        app.metrics.as_ref().expect("metrics should be retained"),
        &metrics
    ));
}

#[test]
fn pylon_request_header_filter_strips_tunnel_headers_case_insensitively()
-> std::result::Result<(), reqwest::header::InvalidHeaderName> {
    let retry = PylonRetryConfig {
        upstream_retry_header: HeaderName::from_static("x-vendor-retryable"),
        ..PylonRetryConfig::default()
    };
    for name in [
        "Connection",
        "Proxy-Connection",
        "Host",
        "X-Method",
        "X-Path",
        "X-Stargate-Expected-Queue-Ms",
    ]
    .into_iter()
    .chain(RETRY_CONTROL_REQUEST_HEADERS)
    {
        assert!(!should_forward_header(
            &HeaderName::from_bytes(name.as_bytes())?,
            &retry
        ));
    }
    assert!(should_forward_header(
        &HeaderName::from_bytes(b"X-Request-Id")?,
        &retry
    ));
    Ok(())
}

#[test]
fn pylon_trace_context_extracts_remote_parent() -> Result<()> {
    opentelemetry::global::set_text_map_propagator(
        opentelemetry_sdk::propagation::TraceContextPropagator::new(),
    );
    let mut headers = HeaderMap::new();
    headers.insert(
        HeaderName::from_static("traceparent"),
        HeaderValue::from_static("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
    );

    let span_context = pylon_upstream_parent_context(&headers)
        .span()
        .span_context()
        .clone();

    assert!(span_context.is_valid());
    assert!(span_context.is_remote());
    assert_eq!(
        span_context.trace_id().to_string(),
        "4bf92f3577b34da6a3ce929d0e0e4736"
    );
    assert_eq!(span_context.span_id().to_string(), "00f067aa0ba902b7");
    Ok(())
}

#[test]
fn pylon_otel_parent_attribute_uses_traceparent_header() {
    let mut headers = HeaderMap::new();
    headers.insert(
        HeaderName::from_static("traceparent"),
        HeaderValue::from_static("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
    );

    assert_eq!(
        otel_parent_from_headers(&headers),
        Some("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
    );
}

#[test]
fn pylon_response_header_filter_strips_internal_headers_case_insensitively()
-> std::result::Result<(), reqwest::header::InvalidHeaderName> {
    let retry = PylonRetryConfig::default();

    for name in [b"Connection".as_slice(), b"X-Stargate-Retryable"] {
        assert!(!should_forward_response_header(
            &HeaderName::from_bytes(name)?,
            &retry,
        ));
    }
    assert!(should_forward_response_header(
        &HeaderName::from_bytes(b"X-Kv-Cache-Hit")?,
        &retry,
    ));
    Ok(())
}

#[test]
fn request_body_buffer_uses_valid_declared_content_length() -> Result<()> {
    let headers = headers_with_content_length("4096")?;

    let body = request_body_buffer(&headers, 8192)?;

    assert_eq!(body.len(), 0);
    assert!(body.capacity() >= 4096);
    Ok(())
}

#[test]
fn request_body_buffer_caps_large_valid_declared_content_length() -> Result<()> {
    let headers = headers_with_content_length("1048576")?;

    let capacity = request_body_capacity(&headers, 2 * 1024 * 1024)?;

    assert_eq!(capacity, Some(MAX_SPECULATIVE_REQUEST_BODY_PREALLOC_BYTES));
    Ok(())
}

#[test]
fn request_body_buffer_rejects_declared_length_above_limit() -> Result<()> {
    let headers = headers_with_content_length("4097")?;

    let Err(error) = request_body_buffer(&headers, 4096) else {
        panic!("oversized content-length should fail");
    };

    assert!(error.to_string().contains("request body too large"));
    Ok(())
}

#[test]
fn request_body_buffer_ignores_invalid_content_length() -> Result<()> {
    let headers = headers_with_content_length("not-a-number")?;

    let body = request_body_buffer(&headers, 4096)?;

    assert_eq!(body.len(), 0);
    assert_eq!(body.capacity(), 0);
    Ok(())
}

fn headers_with_content_length(value: &str) -> Result<HeaderMap> {
    let mut headers = HeaderMap::new();
    headers.insert(reqwest::header::CONTENT_LENGTH, value.parse()?);
    Ok(headers)
}

#[test]
fn extend_body_from_buf_copies_and_consumes_buffer() {
    let mut body = Vec::with_capacity(5);
    let mut chunk = Bytes::from_static(b"hello");

    extend_body_from_buf(&mut body, &mut chunk);

    assert_eq!(body, b"hello");
    assert!(!chunk.has_remaining());
}

fn metrics_text(metrics: &PylonMetrics) -> String {
    let metric_families = metrics.registry().gather();
    let mut buffer = Vec::new();
    TextEncoder::new()
        .encode(&metric_families, &mut buffer)
        .expect("encode metrics");
    String::from_utf8(buffer).expect("metrics should be utf8")
}

fn assert_metric(metrics: &str, sample: &str) {
    assert!(
        metrics.contains(sample),
        "missing metric {sample:?}:\n{metrics}"
    );
}

fn assert_no_metric(metrics: &str, sample: &str) {
    assert!(
        !metrics.contains(sample),
        "unexpected metric {sample:?}:\n{metrics}"
    );
}

fn test_admitted_models() -> Vec<String> {
    TEST_ADMITTED_MODELS
        .iter()
        .map(|model| (*model).to_string())
        .collect()
}

fn admit_test_models(config: &mut QuicHttpTunnelConfig) {
    config.forwarding.runtime_state =
        PylonRuntimeState::new(InferenceServerStatus::Unknown, &test_admitted_models());
}

fn test_tunnel_config(upstream_http_base_url: impl Into<String>) -> QuicHttpTunnelConfig {
    QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        upstream_http_base_url.into(),
    )
}

fn metered_test_tunnel_config(
    upstream_http_base_url: impl Into<String>,
) -> (QuicHttpTunnelConfig, Arc<PylonMetrics>) {
    let metrics = PylonMetrics::new().unwrap();
    let mut config = test_tunnel_config(upstream_http_base_url);
    config.inference_server_id = Some("inst-a".to_string());
    config.forwarding.metrics = Some(metrics.clone());
    (config, metrics)
}

async fn spawn_test_http_server(app: Router) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });
    addr
}

async fn test_tunnel_config_for(app: Router) -> QuicHttpTunnelConfig {
    let mut config = test_tunnel_config(format!("http://{}", spawn_test_http_server(app).await));
    admit_test_models(&mut config);
    config
}

async fn metered_test_tunnel_config_for(app: Router) -> (QuicHttpTunnelConfig, Arc<PylonMetrics>) {
    let (mut config, metrics) =
        metered_test_tunnel_config(format!("http://{}", spawn_test_http_server(app).await));
    admit_test_models(&mut config);
    (config, metrics)
}

async fn read_response_bytes(recv: &mut stargate_protocol::RecvStream) -> Vec<u8> {
    let mut response_body = Vec::new();
    while let Some(chunk) = recv.recv_body().await.unwrap().into_body() {
        response_body.extend_from_slice(&chunk);
    }
    response_body
}

async fn read_response_text(recv: &mut stargate_protocol::RecvStream) -> String {
    String::from_utf8(read_response_bytes(recv).await).unwrap()
}

fn queue_mismatch_request_headers(request_id: &str) -> HeaderMap {
    let mut headers = HeaderMap::new();
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-request-id", request_id.parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("x-stargate-expected-queue-ms", "0".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    headers
}

async fn start_queue_mismatch_test_tunnel(
    tunnel_protocol: TunnelTransportProtocol,
    enabled: bool,
) -> (
    QuicHttpTunnelHandle,
    Arc<AtomicUsize>,
    PylonRuntimeState,
    QueueTrackedRequestGuard,
    Arc<PylonMetrics>,
) {
    let upstream_hits = Arc::new(AtomicUsize::new(0));
    let upstream_hits_for_app = upstream_hits.clone();

    let app = Router::new().route(
        "/v1/chat/completions",
        post(move |_req: Request| {
            let upstream_hits = upstream_hits_for_app.clone();
            async move {
                upstream_hits.fetch_add(1, Ordering::SeqCst);
                (StatusCode::OK, "forwarded")
            }
        }),
    );
    let http_addr = spawn_test_http_server(app).await;

    let metrics = PylonMetrics::new().unwrap();
    let mut config = test_tunnel_config(format!("http://{http_addr}"));
    config.tunnel_protocol = tunnel_protocol;
    config.inference_server_id = Some("inst-a".to_string());
    config.forwarding.metrics = Some(metrics.clone());
    config.forwarding.queue_mismatch_retry.enabled = enabled;
    config.forwarding.queue_mismatch_retry.retry_after_ms = Some(125);
    assert!(
        config
            .forwarding
            .runtime_state
            .begin_generation(ModelGeneration::new("model-a", 0))
    );
    assert!(
        config
            .forwarding
            .runtime_state
            .publish_generation(&ModelGeneration::new("model-a", 0))
    );
    config
        .forwarding
        .runtime_state
        .update_model_throughput("model-a", 100.0);
    let runtime_state = config.forwarding.runtime_state.clone();
    let queued_request = config
        .forwarding
        .runtime_state
        .track_request(&RequiredTunnelHeaders {
            request_id: "req-already-queued".to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens: 100,
            accepted_at: std::time::Instant::now(),
        });

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    (
        tunnel,
        upstream_hits,
        runtime_state,
        queued_request,
        metrics,
    )
}

async fn assert_http_queue_mismatch(
    protocol: TunnelTransportProtocol,
    request_id: &str,
    transport: &str,
) {
    let (tunnel, upstream_hits, runtime_state, _queued_request, metrics) =
        start_queue_mismatch_test_tunnel(protocol, true).await;
    let client = DirectTunnelClient::connect(protocol, tunnel.listen_addr()).await;
    let response = client
        .send(
            "/v1/chat/completions",
            queue_mismatch_request_headers(request_id),
            br#"{"messages":[],"stream":true}"#,
        )
        .await;
    assert_queue_mismatch_response(&response, false);
    assert_eq!(upstream_hits.load(Ordering::SeqCst), 0);
    assert_eq!(
        runtime_state.tracked_request_count(),
        1,
        "{transport} queue mismatch rejection should not leak the rejected request"
    );
    let metrics = metrics_text(&metrics);
    assert_metric(
        &metrics,
        r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="queue_estimate_mismatch",status="429"} 1"#,
    );
    client.close();
    tunnel.shutdown().await;
}

async fn send_raw_quic_quic_json_request(
    tunnel_addr: SocketAddr,
    headers: HeaderMap,
    body: &'static [u8],
) -> TunnelResponse {
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(body)).await.unwrap();
    send.finish().unwrap();

    let headers = recv.recv_header().await.unwrap();
    let status = StatusCode::from_bytes(headers["x-status"].as_bytes()).unwrap();
    let body = read_response_bytes(&mut recv).await;
    TunnelResponse {
        status,
        headers,
        body,
    }
}

fn assert_problem_response(response: &TunnelResponse, status: u16, title: &str, detail: &str) {
    assert_eq!(
        response
            .headers
            .get(reqwest::header::CONTENT_TYPE)
            .unwrap()
            .to_str()
            .unwrap(),
        "application/problem+json"
    );
    let problem: serde_json::Value = serde_json::from_slice(&response.body).unwrap();
    assert_eq!(problem["type"], "about:blank");
    assert_eq!(problem["title"], title);
    assert_eq!(problem["status"], status);
    assert_eq!(problem["detail"], detail);
}

fn assert_retry_metadata(
    headers: &HeaderMap,
    retryable: bool,
    reason: &str,
    retry_after_ms: Option<&str>,
) {
    assert_eq!(headers["x-stargate-retryable"], retryable.to_string());
    assert_eq!(headers["x-stargate-retry-reason"], reason);
    assert_eq!(
        headers
            .get("x-stargate-retry-after-ms")
            .map(|value| value.to_str().unwrap()),
        retry_after_ms
    );
}

fn assert_queue_mismatch_response(response: &TunnelResponse, raw_quic: bool) {
    assert_eq!(response.status, StatusCode::TOO_MANY_REQUESTS);
    assert_retry_metadata(
        &response.headers,
        true,
        "queue_estimate_mismatch",
        Some("125"),
    );
    assert_eq!(response.headers.contains_key("x-status"), raw_quic);
    assert!(
        std::str::from_utf8(&response.body)
            .unwrap()
            .contains("queue_estimate_mismatch")
    );
}

#[test]
fn health_request_path_accepts_query_string() {
    assert!(is_health_request_path("/health"));
    assert!(is_health_request_path("/health?probe=1"));
    assert!(!is_health_request_path("/healthz"));
}

async fn open_test_tunnel_stream(
    tunnel_addr: SocketAddr,
) -> (
    Endpoint,
    stargate_protocol::SendStream,
    stargate_protocol::RecvStream,
) {
    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();
    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    (
        endpoint,
        stargate_protocol::SendStream::new(quinn_send),
        stargate_protocol::RecvStream::new(quinn_recv),
    )
}

async fn negotiate_alpn(
    client_config: ClientConfig,
    server_config: quinn::ServerConfig,
) -> Option<Vec<u8>> {
    let server_endpoint = Endpoint::server(server_config, "127.0.0.1:0".parse().unwrap()).unwrap();
    let server_addr = server_endpoint.local_addr().unwrap();
    let server_task = tokio::spawn(async move {
        let incoming = server_endpoint.accept().await.unwrap();
        let connection = incoming.await.unwrap();
        let protocol = connection
            .handshake_data()
            .and_then(|data| data.downcast::<quinn::crypto::rustls::HandshakeData>().ok())
            .and_then(|data| data.protocol);
        connection.close(0u32.into(), b"test complete");
        protocol
    });

    let mut client_endpoint = Endpoint::client("127.0.0.1:0".parse().unwrap()).unwrap();
    client_endpoint.set_default_client_config(client_config);
    let connection = client_endpoint
        .connect(server_addr, "stargate")
        .unwrap()
        .await
        .unwrap();
    connection.close(0u32.into(), b"test complete");
    server_task.await.unwrap()
}

#[tokio::test]
async fn raw_quic_tunnel_tls_configs_do_not_negotiate_alpn() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let client_config = build_trusted_client_config(None, true, TunnelTransportProtocol::RawQuic)
        .expect("client config");
    let server_config = make_server_config(
        &ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::RawQuic,
    )
    .expect("server config");

    assert_eq!(negotiate_alpn(client_config, server_config).await, None);
}

#[tokio::test]
async fn http3_tunnel_tls_configs_negotiate_h3_alpn() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let client_config = build_trusted_client_config(None, true, TunnelTransportProtocol::Http3)
        .expect("client config");
    let server_config = make_server_config(
        &ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::Http3,
    )
    .expect("server config");

    assert_eq!(
        negotiate_alpn(client_config, server_config).await,
        Some(b"h3".to_vec())
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn http3_direct_tunnel_accepts_responses_request_to_upstream() {
    let app = Router::new().route(
        "/v1/responses",
        post(|req: Request| async move {
            let model = req
                .headers()
                .get("x-model")
                .and_then(|value| value.to_str().ok())
                .unwrap_or("missing")
                .to_string();
            let body = axum::body::to_bytes(req.into_body(), 1024 * 1024)
                .await
                .unwrap();
            (
                StatusCode::OK,
                [(reqwest::header::CONTENT_TYPE.as_str(), "application/json")],
                format!(r#"{{"model":"{model}","body_len":{}}}"#, body.len()),
            )
        }),
    );
    let mut config = test_tunnel_config_for(app).await;
    config.tunnel_protocol = TunnelTransportProtocol::Http3;
    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-h3-direct".parse().unwrap());
    headers.insert("x-model", "model-h3".parse().unwrap());
    headers.insert("x-input-tokens", "7".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    let response = send_direct_http3_json_request(
        tunnel.listen_addr(),
        "/v1/responses?source=http3",
        headers,
        br#"{"input":"hi","stream":true}"#,
    )
    .await;

    assert_eq!(response.status, StatusCode::OK);
    let payload: serde_json::Value = serde_json::from_slice(&response.body).unwrap();
    assert_eq!(
        payload.get("model").and_then(serde_json::Value::as_str),
        Some("model-h3")
    );
    assert_eq!(
        payload.get("body_len").and_then(serde_json::Value::as_u64),
        Some(28)
    );

    tunnel.shutdown().await;
}

async fn open_direct_webtransport_session(tunnel_addr: SocketAddr) -> DirectWebTransportSession {
    let mut endpoint = Endpoint::client("127.0.0.1:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(
        stargate_tls::build_insecure_quic_client_config_with_alpn(
            TunnelTransportProtocol::WebTransport.alpn_protocols(),
        )
        .unwrap(),
    );
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();
    let mut builder = h3::client::builder();
    builder.enable_extended_connect(true).enable_datagram(true);
    let (h3_connection, mut send_request): (
        h3::client::Connection<h3_quinn::Connection, Bytes>,
        h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>,
    ) = builder
        .build(h3_quinn::Connection::new(connection.clone()))
        .await
        .unwrap();
    let mut request: http::Request<()> = http::Request::builder()
        .method(http::Method::CONNECT)
        .uri(format!("https://stargate{WEBTRANSPORT_TUNNEL_PATH}"))
        .body(())
        .unwrap();
    request
        .extensions_mut()
        .insert(h3::ext::Protocol::WEB_TRANSPORT);
    let mut connect_stream = send_request.send_request(request).await.unwrap();
    let session_id = connect_stream.id().into_inner();
    connect_stream.finish().await.unwrap();
    let response = connect_stream.recv_response().await.unwrap();
    assert_eq!(response.status(), StatusCode::OK);

    DirectWebTransportSession {
        _endpoint: endpoint,
        connection,
        _h3_connection: h3_connection,
        _connect_stream: connect_stream,
        session_id,
    }
}

async fn send_direct_webtransport_json_request(
    session: &DirectWebTransportSession,
    path: &str,
    model: &str,
    request_id: &str,
    body: &'static [u8],
) -> TunnelResponse {
    let mut headers = HeaderMap::new();
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", model.parse().unwrap());
    headers.insert("x-request-id", request_id.parse().unwrap());
    headers.insert("x-input-tokens", "2".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send_direct_webtransport_request_with_headers(session, path, headers, body).await
}

async fn send_direct_webtransport_request_with_headers(
    session: &DirectWebTransportSession,
    path: &str,
    headers: HeaderMap,
    body: &'static [u8],
) -> TunnelResponse {
    let (mut quinn_send, quinn_recv) = session.connection.open_bi().await.unwrap();
    let request_head = stargate_protocol::WebTransportHttpRequestHead {
        method: reqwest::Method::POST,
        path_and_query: path.to_string(),
        headers,
    };
    let bidi_header = stargate_protocol::WebTransportBidiHeader::new(session.session_id)
        .unwrap()
        .to_bytes();
    stargate_protocol::write_webtransport_http_request_head_after_prefix(
        &mut quinn_send,
        bidi_header,
        &request_head,
    )
    .await
    .unwrap();
    write_direct_webtransport_request_body(&mut quinn_send, bytes::Bytes::from_static(body)).await;

    let mut quinn_recv = quinn_recv;
    let response_head = stargate_protocol::read_webtransport_http_response_head(&mut quinn_recv)
        .await
        .unwrap();
    let mut response_body = Vec::new();
    while let Some(chunk) = stargate_protocol::read_webtransport_http_body_chunk(&mut quinn_recv)
        .await
        .unwrap()
    {
        response_body.extend_from_slice(&chunk);
    }
    TunnelResponse {
        status: response_head.status,
        headers: response_head.headers,
        body: response_body,
    }
}

async fn write_direct_webtransport_request_body(
    quinn_send: &mut quinn::SendStream,
    body: bytes::Bytes,
) {
    match stargate_protocol::write_webtransport_http_body(quinn_send, body).await {
        Ok(()) => stargate_protocol::finish_webtransport_http_stream(quinn_send).unwrap(),
        // Head-only local rejections may stop the unread body with QUIC NO_ERROR.
        Err(error) if webtransport_request_body_stopped_normally(&error) => {}
        Err(error) => panic!("write WebTransport request body: {error}"),
    }
}

fn webtransport_request_body_stopped_normally(error: &stargate_protocol::ProtocolError) -> bool {
    let stargate_protocol::ProtocolError::Io(error) = error else {
        return false;
    };
    error
        .get_ref()
        .and_then(|source| source.downcast_ref::<quinn::WriteError>())
        .is_some_and(
            |error| matches!(error, quinn::WriteError::Stopped(code) if *code == 0u32.into()),
        )
}

#[test]
fn direct_webtransport_test_client_accepts_only_no_error_request_body_stop() {
    let no_error = stargate_protocol::ProtocolError::Io(std::io::Error::other(
        quinn::WriteError::Stopped(0u32.into()),
    ));
    let application_error = stargate_protocol::ProtocolError::Io(std::io::Error::other(
        quinn::WriteError::Stopped(1u32.into()),
    ));

    assert!(webtransport_request_body_stopped_normally(&no_error));
    assert!(!webtransport_request_body_stopped_normally(
        &application_error
    ));
}

async fn send_direct_http3_json_request(
    tunnel_addr: SocketAddr,
    path: &str,
    headers: HeaderMap,
    body: &'static [u8],
) -> TunnelResponse {
    tokio::time::timeout(Duration::from_secs(2), async move {
        let mut endpoint = Endpoint::client("127.0.0.1:0".parse().unwrap()).unwrap();
        endpoint.set_default_client_config(
            stargate_tls::build_insecure_quic_client_config_with_alpn(
                TunnelTransportProtocol::Http3.alpn_protocols(),
            )
            .unwrap(),
        );
        let connection = endpoint
            .connect(tunnel_addr, "stargate")
            .unwrap()
            .await
            .unwrap();
        let (mut driver, mut send_request) = h3::client::builder()
            .build(h3_quinn::Connection::new(connection.clone()))
            .await
            .unwrap();
        let mut driver_task =
            tokio::spawn(async move { future::poll_fn(|cx| driver.poll_close(cx)).await });

        let uri: http::Uri = format!("https://stargate:{}{path}", tunnel_addr.port())
            .parse()
            .unwrap();
        let mut request = http::Request::builder()
            .method(http::Method::POST)
            .uri(uri)
            .body(())
            .unwrap();
        *request.headers_mut() = headers;
        let mut stream = send_request.send_request(request).await.unwrap();
        stream.send_data(Bytes::from_static(body)).await.unwrap();
        stream.finish().await.unwrap();

        let response = stream.recv_response().await.unwrap();
        let status = response.status();
        let headers = response.headers().clone();
        let mut response_body = Vec::new();
        while let Some(mut chunk) = stream.recv_data().await.unwrap() {
            while chunk.has_remaining() {
                let len = chunk.remaining();
                response_body.extend_from_slice(&chunk.copy_to_bytes(len));
            }
        }

        connection.close(0u32.into(), b"test complete");
        if tokio::time::timeout(Duration::from_secs(1), &mut driver_task)
            .await
            .is_err()
        {
            driver_task.abort();
        }
        TunnelResponse {
            status,
            headers,
            body: response_body,
        }
    })
    .await
    .expect("direct HTTP/3 request timed out")
}

enum DirectTunnelClient {
    Http3(SocketAddr),
    WebTransport(Box<DirectWebTransportSession>),
}

impl DirectTunnelClient {
    async fn connect(protocol: TunnelTransportProtocol, tunnel_addr: SocketAddr) -> Self {
        match protocol {
            TunnelTransportProtocol::Http3 => Self::Http3(tunnel_addr),
            TunnelTransportProtocol::WebTransport => Self::WebTransport(Box::new(
                open_direct_webtransport_session(tunnel_addr).await,
            )),
            TunnelTransportProtocol::RawQuic => panic!("direct client requires an HTTP transport"),
        }
    }

    async fn send(&self, path: &str, headers: HeaderMap, body: &'static [u8]) -> TunnelResponse {
        match self {
            Self::Http3(tunnel_addr) => {
                send_direct_http3_json_request(*tunnel_addr, path, headers, body).await
            }
            Self::WebTransport(session) => {
                send_direct_webtransport_request_with_headers(session, path, headers, body).await
            }
        }
    }

    fn close(&self) {
        if let Self::WebTransport(session) = self {
            session.connection.close(0u32.into(), b"test complete");
        }
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_webtransport_stalled_stream_header_does_not_block_later_responses_request() {
    let app = Router::new().route(
        "/v1/responses",
        post(|req: Request| async move {
            let request_id = req
                .headers()
                .get("x-request-id")
                .and_then(|value| value.to_str().ok())
                .unwrap_or("missing")
                .to_string();
            (
                StatusCode::OK,
                [(reqwest::header::CONTENT_TYPE.as_str(), "application/json")],
                format!(r#"{{"request_id":"{request_id}"}}"#),
            )
        }),
    );
    let upstream_addr = spawn_test_http_server(app).await;

    let mut config = test_tunnel_config(format!("http://{upstream_addr}"));
    admit_test_models(&mut config);
    config.tunnel_protocol = TunnelTransportProtocol::WebTransport;
    let (header_wait_tx, header_wait_rx) = flume::bounded(1);
    config.forwarding.webtransport_stream_header_wait_tx = Some(header_wait_tx);
    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let session = open_direct_webtransport_session(tunnel.listen_addr()).await;

    let (mut stalled_send, _stalled_recv) = session.connection.open_bi().await.unwrap();
    let stalled_header = stargate_protocol::WebTransportBidiHeader::new(session.session_id)
        .unwrap()
        .to_bytes();
    stalled_send.write_all(&stalled_header[..1]).await.unwrap();
    tokio::time::timeout(Duration::from_secs(1), header_wait_rx.recv_async())
        .await
        .expect("stalled WebTransport stream did not reach header wait")
        .expect("header wait signal channel closed");
    let response = tokio::time::timeout(
        Duration::from_secs(2),
        send_direct_webtransport_json_request(
            &session,
            "/v1/responses",
            "model-webtransport",
            "req-after-stalled-direct-wt",
            br#"{"input":"hi","stream":true}"#,
        ),
    )
    .await
    .expect("direct WebTransport request after stalled stream timed out");

    assert_eq!(response.status, StatusCode::OK);
    let payload: serde_json::Value = serde_json::from_slice(&response.body).unwrap();
    assert_eq!(
        payload
            .get("request_id")
            .and_then(serde_json::Value::as_str),
        Some("req-after-stalled-direct-wt")
    );

    tunnel.shutdown().await;
}

async fn send_json_proxy_request(
    send: &mut stargate_protocol::SendStream,
    path: &str,
    model: &str,
    request_id: &str,
    body: &'static [u8],
) {
    send_proxy_request_with_headers(
        send,
        tunnel_request_headers(path, model, request_id, "11"),
        body,
    )
    .await;
}

fn tunnel_request_headers(
    path: &str,
    model: &str,
    request_id: &str,
    input_tokens: &str,
) -> HeaderMap {
    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", path.parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", model.parse().unwrap());
    headers.insert("x-request-id", request_id.parse().unwrap());
    headers.insert("x-input-tokens", input_tokens.parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    headers
}

async fn send_proxy_request_with_headers(
    send: &mut stargate_protocol::SendStream,
    headers: HeaderMap,
    body: &'static [u8],
) {
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(body)).await.unwrap();
    send.finish().unwrap();
}

fn embeddings_tunnel_headers(request_id: &str) -> HeaderMap {
    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/embeddings".parse().unwrap());
    headers.insert("x-request-id", request_id.parse().unwrap());
    headers.insert("x-model", "model-embed".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    headers
}

fn assert_quality_metrics_absent(metrics: &str) {
    assert_no_metric(metrics, "pylon_quality_checks_total");
    assert_no_metric(metrics, "pylon_quality_threshold_matches_total");
}

fn sse_app(path: &'static str, events: &'static [&'static str]) -> Router {
    Router::new().route(
        path,
        post(move || async move {
            axum::response::Sse::new(futures::stream::iter(
                events
                    .iter()
                    .map(|data| Ok::<_, std::convert::Infallible>(Event::default().data(*data))),
            ))
        }),
    )
}

fn chat_sse_app(events: &'static [&'static str]) -> Router {
    sse_app("/v1/chat/completions", events)
}

fn raw_sse_app(path: &'static str, body: &'static str) -> Router {
    Router::new().route(
        path,
        post(move || async move {
            Response::builder()
                .header("content-type", "text/event-stream")
                .body(Body::from(body))
                .unwrap()
        }),
    )
}

fn counting_app(path: &'static str, hits: Arc<AtomicUsize>) -> Router {
    Router::new().route(
        path,
        post(move || {
            let hits = hits.clone();
            async move {
                hits.fetch_add(1, Ordering::SeqCst);
                Response::new(Body::from(r#"{"unexpected":true}"#))
            }
        }),
    )
}

fn output_token_monitor(minimum: u32) -> RequestQualityMonitorConfig {
    RequestQualityMonitorConfig {
        output_tokens_threshold_min: Some(minimum),
        ..Default::default()
    }
}

async fn run_quality_request(
    app: Router,
    path: &str,
    model: &str,
    request_id: &str,
    monitor: RequestQualityMonitorConfig,
    expected_status: StatusCode,
    body: &'static [u8],
) -> (String, Vec<u8>) {
    let (mut config, metrics) = metered_test_tunnel_config_for(app).await;
    config.forwarding.request_quality_monitor = monitor;
    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel.send_json(path, model, request_id, body).await;
    tunnel.response_head(expected_status).await;
    let response_body = read_response_bytes(&mut tunnel.recv).await;
    let metrics = metrics_text(&metrics);
    tunnel.shutdown().await;
    (metrics, response_body)
}

async fn run_chat_quality_request(
    app: Router,
    request_id: &str,
    monitor: RequestQualityMonitorConfig,
) -> (String, Vec<u8>) {
    run_quality_request(
        app,
        "/v1/chat/completions",
        "model-quality",
        request_id,
        monitor,
        StatusCode::OK,
        br#"{"messages":[],"stream":true}"#,
    )
    .await
}

macro_rules! chat_quality_test {
    ($name:ident, $app:expr, $request_id:literal, {$($field:ident: $value:expr),* $(,)?}, |$metrics:ident| $assertions:block) => {
        #[tokio::test]
        async fn $name() {
            let ($metrics, _response_body) = run_chat_quality_request(
                $app,
                $request_id,
                RequestQualityMonitorConfig {
                    $($field: $value,)*
                    ..Default::default()
                },
            )
            .await;
            $assertions
        }
    };
}

fn assert_quality_result(metrics: &str, model: &str, result: &str) {
    assert_metric(
        metrics,
        &format!(r#"pylon_quality_checks_total{{model="{model}",result="{result}"}} 1"#),
    );
}

fn assert_no_quality_result(metrics: &str, model: &str, result: &str) {
    assert_no_metric(
        metrics,
        &format!(r#"pylon_quality_checks_total{{model="{model}",result="{result}"}}"#),
    );
}

fn assert_quality_threshold(metrics: &str, model: &str, reason: &str) {
    assert_metric(
        metrics,
        &format!(r#"pylon_quality_threshold_matches_total{{model="{model}",reason="{reason}"}} 1"#),
    );
}

fn assert_no_quality_threshold(metrics: &str, model: &str, reason: &str) {
    assert_no_metric(
        metrics,
        &format!(r#"pylon_quality_threshold_matches_total{{model="{model}",reason="{reason}"}}"#),
    );
}

async fn assert_local_connect_failure(
    retryable: Option<bool>,
    request_id: &str,
    expected_retryable: bool,
    metric: &str,
) {
    let (mut config, metrics) = metered_test_tunnel_config("http://127.0.0.1:0");
    admit_test_models(&mut config);
    if let Some(retryable) = retryable {
        config.forwarding.retry.local_connect_failures_retryable = retryable;
    }
    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel
        .send_json(
            "/v1/chat/completions",
            "model-a",
            request_id,
            br#"{"messages":[],"stream":true}"#,
        )
        .await;
    let response = tunnel.response(StatusCode::SERVICE_UNAVAILABLE).await;
    assert_retry_metadata(
        &response.headers,
        expected_retryable,
        "local_connect_failure",
        None,
    );
    assert_problem_response(
        &response,
        503,
        "Service Unavailable",
        "local upstream connection failed",
    );
    assert_metric(&metrics_text(&metrics), metric);
}

#[tokio::test]
async fn quic_tunnel_forwards_to_http_backend() {
    let app = Router::new().route(
            "/v1/chat/completions",
            post(|req: Request| async move {
                let model = req
                    .headers()
                    .get("x-model")
                    .and_then(|v| v.to_str().ok())
                    .unwrap_or("none");
                let saw_expected_queue_header =
                    req.headers().contains_key("x-stargate-expected-queue-ms");
                let saw_retry_control_header = RETRY_CONTROL_REQUEST_HEADERS
                .iter()
                .any(|name| req.headers().contains_key(*name));
                let mut sse = axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"ok"}}]}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
                .into_response();
                sse.headers_mut().insert(
                    HeaderName::from_static("x-echo-model"),
                    HeaderValue::from_str(model).unwrap(),
                );
                sse.headers_mut().insert(
                    HeaderName::from_static("x-saw-expected-queue"),
                    HeaderValue::from_str(&saw_expected_queue_header.to_string()).unwrap(),
                );
                sse.headers_mut().insert(
                    HeaderName::from_static("x-saw-retry-control"),
                    HeaderValue::from_str(&saw_retry_control_header.to_string()).unwrap(),
                );
                *sse.status_mut() = StatusCode::OK;
                sse
            }),
        );
    let (mut config, _metrics) = metered_test_tunnel_config_for(app).await;
    config.forwarding.retry.upstream_retry_header = HeaderName::from_static("x-vendor-retryable");
    let mut tunnel = RawTunnelTest::start(config).await;

    let mut headers =
        tunnel_request_headers("/v1/chat/completions", "model-a", "req-tunnel-1", "11");
    headers.insert("x-stargate-expected-queue-ms", "5".parse().unwrap());
    for name in RETRY_CONTROL_REQUEST_HEADERS {
        headers.insert(name, "spoofed".parse().unwrap());
    }
    tunnel
        .send(headers, br#"{"messages":[],"stream":true}"#)
        .await;

    let response_headers = tunnel.response_head(StatusCode::OK).await;
    assert_eq!(
        response_headers
            .get("x-echo-model")
            .unwrap()
            .to_str()
            .unwrap(),
        "model-a"
    );
    assert_eq!(
        response_headers
            .get("x-saw-expected-queue")
            .unwrap()
            .to_str()
            .unwrap(),
        "false"
    );
    assert_eq!(response_headers["x-saw-retry-control"], "false");

    let response_text = read_response_text(&mut tunnel.recv).await;
    let events = parse_test_sse_events(&response_text);
    assert_eq!(events.last().map(|event| event.data.trim()), Some("[DONE]"));
    let payloads = test_sse_json_payloads(&events);
    assert_eq!(payloads.len(), 1);
    assert_eq!(payloads[0]["object"], "chat.completion.chunk");
    assert_eq!(payloads[0]["choices"][0]["delta"]["content"], "ok");

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_rejects_pending_generation_before_upstream() {
    let upstream_hits = Arc::new(AtomicUsize::new(0));
    let app = counting_app("/v1/chat/completions", upstream_hits.clone());
    let (config, metrics) =
        metered_test_tunnel_config(format!("http://{}", spawn_test_http_server(app).await));
    let pending = ModelGeneration::new("model-a", 1);
    assert!(config.forwarding.runtime_state.begin_generation(pending));

    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel
        .send_json(
            "/v1/chat/completions",
            "model-a",
            "req-pending-generation",
            br#"{"messages":[],"stream":true}"#,
        )
        .await;

    let response = tunnel.response(StatusCode::SERVICE_UNAVAILABLE).await;
    assert_retry_metadata(
        &response.headers,
        true,
        "model_generation_unavailable",
        None,
    );
    assert_problem_response(
        &response,
        503,
        "Service Unavailable",
        "model generation is not admitted for request routing",
    );
    assert_eq!(
        upstream_hits.load(Ordering::SeqCst),
        0,
        "pending generations must not receive stale routed traffic"
    );
    assert_metric(
        &metrics_text(&metrics),
        r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="model_generation_unavailable",status="503"} 1"#,
    );
}

#[tokio::test]
async fn quic_tunnel_marks_explicit_retryable_upstream_rejection() {
    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move {
            let mut response = Response::new(Body::from(r#"{"error":"queue full"}"#));
            *response.status_mut() = StatusCode::TOO_MANY_REQUESTS;
            response.headers_mut().insert(
                HeaderName::from_static("x-stargate-upstream-retryable"),
                HeaderValue::from_static("true"),
            );
            response
                .headers_mut()
                .insert(reqwest::header::RETRY_AFTER, HeaderValue::from_static("2"));
            response
        }),
    );
    let (config, metrics) = metered_test_tunnel_config_for(app).await;

    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel
        .send_json(
            "/v1/chat/completions",
            "model-a",
            "req-retryable-1",
            br#"{"messages":[],"stream":true}"#,
        )
        .await;
    let response = tunnel.response(StatusCode::TOO_MANY_REQUESTS).await;
    assert_retry_metadata(
        &response.headers,
        true,
        "upstream_admission_rejected",
        Some("2000"),
    );

    let metrics = metrics_text(&metrics);
    assert_metric(
        &metrics,
        r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="upstream_admission_rejected",status="429"} 1"#,
    );
}

#[tokio::test]
async fn quic_tunnel_rejects_queue_estimate_mismatch_before_upstream() {
    let (tunnel, upstream_hits, runtime_state, _queued_request, metrics) =
        start_queue_mismatch_test_tunnel(TunnelTransportProtocol::RawQuic, true).await;
    let mut headers = queue_mismatch_request_headers("req-queue-mismatch");
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    let response = send_raw_quic_quic_json_request(
        tunnel.listen_addr(),
        headers,
        br#"{"messages":[],"stream":true}"#,
    )
    .await;
    assert_queue_mismatch_response(&response, true);
    assert_eq!(upstream_hits.load(Ordering::SeqCst), 0);
    assert_eq!(
        runtime_state.tracked_request_count(),
        1,
        "queue mismatch rejection should not leak the rejected request"
    );

    let metrics = metrics_text(&metrics);
    assert_metric(
        &metrics,
        "# HELP pylon_retryable_responses_total Total number of retryable responses emitted or relayed by pylon",
    );
    assert_metric(
        &metrics,
        r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="queue_estimate_mismatch",status="429"} 1"#,
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_queue_mismatch_retry_disabled_forwards_to_upstream() {
    let (tunnel, upstream_hits, runtime_state, _queued_request, metrics) =
        start_queue_mismatch_test_tunnel(TunnelTransportProtocol::RawQuic, false).await;

    let mut headers = queue_mismatch_request_headers("req-queue-mismatch-disabled");
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    let response = send_raw_quic_quic_json_request(
        tunnel.listen_addr(),
        headers,
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    assert_eq!(response.status, StatusCode::OK);
    assert!(response.headers.get("x-stargate-retryable").is_none());
    assert_eq!(String::from_utf8(response.body).unwrap(), "forwarded");
    assert_eq!(upstream_hits.load(Ordering::SeqCst), 1);
    assert_eq!(
        runtime_state.tracked_request_count(),
        1,
        "disabled queue mismatch admission should still finish the proxied request"
    );

    let metrics = metrics_text(&metrics);
    assert_metric(
        &metrics,
        r#"pylon_queue_admission_decisions_total{inference_server_id="inst-a",model_id="model-a",result="disabled"} 1"#,
    );
    assert_no_metric(
        &metrics,
        r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="queue_estimate_mismatch",status="429"}"#,
    );

    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn http3_tunnel_rejects_queue_estimate_mismatch_before_upstream() {
    assert_http_queue_mismatch(
        TunnelTransportProtocol::Http3,
        "req-h3-queue-mismatch",
        "HTTP/3",
    )
    .await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn webtransport_tunnel_rejects_queue_estimate_mismatch_before_upstream() {
    assert_http_queue_mismatch(
        TunnelTransportProtocol::WebTransport,
        "req-webtransport-queue-mismatch",
        "WebTransport",
    )
    .await;
}

#[tokio::test]
async fn quic_tunnel_strips_spoofed_retry_headers_without_upstream_signal() {
    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move {
            let mut response = Response::new(Body::from(r#"{"error":"too many"}"#));
            *response.status_mut() = StatusCode::TOO_MANY_REQUESTS;
            response.headers_mut().insert(
                HeaderName::from_static("x-stargate-retryable"),
                HeaderValue::from_static("true"),
            );
            response.headers_mut().insert(
                HeaderName::from_static("x-stargate-retry-reason"),
                HeaderValue::from_static("spoofed"),
            );
            response.headers_mut().insert(
                HeaderName::from_static("x-stargate-retry-after-ms"),
                HeaderValue::from_static("1"),
            );
            response
        }),
    );
    let (config, metrics) = metered_test_tunnel_config_for(app).await;

    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel
        .send_json(
            "/v1/chat/completions",
            "model-a",
            "req-bare-429",
            br#"{"messages":[],"stream":true}"#,
        )
        .await;
    let response = tunnel.response(StatusCode::TOO_MANY_REQUESTS).await;
    assert!(response.headers.get("x-stargate-retryable").is_none());
    assert!(response.headers.get("x-stargate-retry-reason").is_none());
    assert!(response.headers.get("x-stargate-retry-after-ms").is_none());

    let metrics = metrics_text(&metrics);
    assert_metric(
        &metrics,
        r#"pylon_nonretryable_failures_total{inference_server_id="inst-a",reason="missing_upstream_retry_header"} 1"#,
    );
}

#[tokio::test]
async fn quic_tunnel_marks_local_connect_failure_retryable_when_configured() {
    assert_local_connect_failure(
        Some(true),
        "req-local-connect-failure",
        true,
        r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="local_connect_failure",status="503"} 1"#,
    )
    .await;
}

#[tokio::test]
async fn quic_tunnel_marks_local_connect_failure_nonretryable_by_default() {
    assert_local_connect_failure(
        None,
        "req-local-connect-nonretry",
        false,
        r#"pylon_nonretryable_failures_total{inference_server_id="inst-a",reason="local_connect_failure"} 1"#,
    )
    .await;
}

#[tokio::test]
async fn quic_tunnel_emits_request_observation_for_streaming_response() {
    let app = sse_app(
        "/v1/chat/completions",
        &[
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}],"usage":{"completion_tokens":1}}"#,
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":" world"}}],"usage":{"completion_tokens":2}}"#,
            "[DONE]",
        ],
    );
    let (mut tunnel, rx) = observed_tunnel_for(app).await;
    let headers =
        tunnel_request_headers("/v1/chat/completions", "model-stream", "req-stream-1", "17");
    tunnel
        .send(headers, br#"{"messages":[],"stream":true}"#)
        .await;
    tunnel.response_head(StatusCode::OK).await;
    tunnel.drain().await;

    let observation = recv_terminal_observation(&rx).await;
    assert_eq!(observation.request_id, "req-stream-1");
    assert_eq!(observation.model_id, "model-stream");
    assert_eq!(observation.input_tokens, 17);
    assert_eq!(observation.output_messages, 2);
    assert_eq!(observation.output_tokens, 2);
    assert!(observation.output_tokens_explicit);
    assert!(observation.output_tokens_from_chunk_usage);
    assert_eq!(observation.state, RequestObservationState::Complete);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_emits_request_observation_for_streaming_responses() {
    let app = Router::new().route(
            "/v1/responses",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default()
                            .event("response.created")
                            .data(r#"{"type":"response.created","response":{"status":"in_progress"}}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default()
                            .event("response.output_text.delta")
                            .data(r#"{"type":"response.output_text.delta","delta":"Hello"}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default()
                            .event("response.completed")
                            .data(r#"{"type":"response.completed","response":{"usage":{"input_tokens":11,"output_tokens":2,"total_tokens":13}}}"#)
                    );
                })
            }),
        );
    let (mut tunnel, rx) = observed_tunnel_for(app).await;
    tunnel
        .send_json(
            "/v1/responses",
            "model-responses",
            "req-responses-observed",
            br#"{"input":"hello","stream":true}"#,
        )
        .await;
    tunnel.response_head(StatusCode::OK).await;
    let response_text = read_response_text(&mut tunnel.recv).await;
    let events = parse_test_sse_events(&response_text);
    let event_names: Vec<_> = events
        .iter()
        .map(|event| event.event_name.as_deref())
        .collect();
    assert_eq!(
        event_names,
        vec![
            Some("response.created"),
            Some("response.output_text.delta"),
            Some("response.completed"),
        ]
    );
    let payloads = test_sse_json_payloads(&events);
    assert_eq!(payloads[0]["type"], "response.created");
    assert_eq!(payloads[1]["type"], "response.output_text.delta");
    assert_eq!(payloads[1]["delta"], "Hello");
    assert_eq!(payloads[2]["type"], "response.completed");
    assert_eq!(
        payloads[2]["response"]["usage"]["total_tokens"],
        serde_json::json!(13)
    );

    let observation = recv_terminal_observation(&rx).await;
    assert_eq!(observation.endpoint, RequestObservationEndpoint::Responses);
    assert_eq!(observation.request_id, "req-responses-observed");
    assert_eq!(observation.model_id, "model-responses");
    assert_eq!(observation.input_tokens, 11);
    assert_eq!(observation.output_messages, 2);
    assert_eq!(observation.output_tokens, 2);
    assert!(observation.output_tokens_explicit);
    assert!(observation.output_tokens_from_chunk_usage);
    assert_eq!(observation.state, RequestObservationState::Complete);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_times_out_when_responses_stream_stalls_before_output() {
    let app = Router::new().route(
        "/v1/responses",
        post(|_req: Request| async move {
            axum::response::Sse::new(async_stream::stream! {
                yield Ok::<_, std::convert::Infallible>(
                    Event::default()
                        .event("response.created")
                        .data(r#"{"type":"response.created","response":{"status":"in_progress"}}"#)
                );
                tokio::time::sleep(Duration::from_millis(50)).await;
                yield Ok::<_, std::convert::Infallible>(
                    Event::default()
                        .event("response.completed")
                        .data(r#"{"type":"response.completed","response":{"status":"completed"}}"#)
                );
            })
        }),
    );
    let mut config = test_tunnel_config_for(app).await;
    config.forwarding.first_output_timeout = Duration::from_millis(10);
    config.forwarding.output_chunk_timeout = Duration::from_millis(100);
    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel
        .send_json(
            "/v1/responses",
            "model-responses",
            "req-responses-timeout",
            br#"{"input":"hello","stream":true}"#,
        )
        .await;
    tunnel.response_head(StatusCode::OK).await;
    let first_chunk = tunnel
        .recv
        .recv_body()
        .await
        .unwrap()
        .into_body()
        .expect("response.created event should be forwarded");
    assert!(
        String::from_utf8(first_chunk.to_vec())
            .unwrap()
            .contains("response.created")
    );
    assert!(tunnel.recv.recv_body().await.is_err());

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_feeds_chunk_usage_stats_into_stats_collector() {
    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}],"usage":{"completion_tokens":1}}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":" world"}}],"usage":{"completion_tokens":2}}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    let stats_config = StatsCollectorConfig {
        observation_channel_capacity: 16,
        ..Default::default()
    };
    let (runtime_state, observation_rx) = PylonRuntimeState::observed(
        InferenceServerStatus::Unknown,
        &["model-stream".to_string()],
        stats_config.observation_channel_capacity,
        None,
    );
    let stats_handle = start_stats_collector(stats_config, observation_rx, runtime_state.clone());

    let mut config = test_tunnel_config_for(app).await;
    config.forwarding.runtime_state = runtime_state.clone();
    let mut tunnel = RawTunnelTest::start(config).await;

    let headers = tunnel_request_headers(
        "/v1/chat/completions",
        "model-stream",
        "req-stream-stats",
        "17",
    );
    tunnel
        .send(headers, br#"{"messages":[],"stream":true}"#)
        .await;
    tunnel.response_head(StatusCode::OK).await;
    tunnel.drain().await;

    let stats = tokio::time::timeout(Duration::from_secs(1), async {
        let mut poll = tokio::time::interval(Duration::from_millis(1));
        loop {
            poll.tick().await;
            if let Some(stats) = runtime_state.model_stats("model-stream")
                && stats
                    .stats_capabilities
                    .contains(&"request.output.chunk_usage".to_string())
            {
                break stats;
            }
        }
    })
    .await
    .unwrap();
    assert!(stats.stats_observed_at_unix_ms > 0);
    assert_eq!(stats.stats_sources, vec!["chunk_usage".to_string()]);

    stats_handle.shutdown().await;
    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_counts_terminal_only_usage_tokens() {
    let app = sse_app(
        "/v1/chat/completions",
        &[
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}],"usage":null}"#,
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":" world"}}],"usage":null}"#,
            r#"{"object":"chat.completion.chunk","choices":[],"usage":{"completion_tokens":7}}"#,
            "[DONE]",
        ],
    );
    let (mut tunnel, rx) = observed_tunnel_for(app).await;
    let headers = tunnel_request_headers(
        "/v1/chat/completions",
        "model-terminal-usage",
        "req-terminal-usage",
        "13",
    );
    tunnel
        .send(headers, br#"{"messages":[],"stream":true}"#)
        .await;
    tunnel.response_head(StatusCode::OK).await;
    tunnel.drain().await;

    let observation = recv_terminal_observation(&rx).await;
    assert_eq!(observation.request_id, "req-terminal-usage");
    assert_eq!(observation.model_id, "model-terminal-usage");
    assert_eq!(observation.input_tokens, 13);
    assert_eq!(observation.output_messages, 3);
    assert_eq!(observation.output_tokens, 7);
    assert!(observation.output_tokens_explicit);
    assert!(observation.output_tokens_from_chunk_usage);
    assert_eq!(observation.state, RequestObservationState::Complete);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_uses_chunk_stats_fallback_when_progress_contract_is_absent() {
    let app = sse_app(
        "/v1/chat/completions",
        &[
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}],"usage":{"completion_tokens":9}}"#,
            "[DONE]",
        ],
    );
    let (mut tunnel, rx) = observed_tunnel_for(app).await;
    tunnel
        .send_json(
            "/v1/chat/completions",
            "model-fallback",
            "req-fallback",
            br#"{"messages":[],"stream":true}"#,
        )
        .await;
    tunnel.response_head(StatusCode::OK).await;
    tunnel.drain().await;

    let observation = recv_terminal_observation(&rx).await;
    assert_eq!(observation.output_messages, 1);
    assert_eq!(observation.output_tokens, 9);
    assert!(observation.output_tokens_explicit);
    assert!(observation.output_tokens_from_chunk_usage);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_quality_token_threshold_uses_chunk_usage_counts() {
    let (metrics, response_body) = run_quality_request(
        raw_sse_app(
            "/v1/chat/completions",
            "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"alpha beta\"}}],\"usage\":{\"completion_tokens\":12}}\n\ndata: [DONE]\n\n",
        ),
        "/v1/chat/completions",
        "model-progress-quality",
        "req-progress-quality",
        output_token_monitor(10),
        StatusCode::OK,
        br#"{"messages":[],"stream":true}"#,
    )
    .await;
    assert!(
        std::str::from_utf8(&response_body)
            .unwrap()
            .contains("completion_tokens")
    );
    assert_quality_result(&metrics, "model-progress-quality", "matched");
    assert_quality_threshold(&metrics, "model-progress-quality", "output_tokens");
}

#[tokio::test]
async fn quic_tunnel_emits_quality_metrics_for_repetitive_output() {
    let raw_model = "  model-quality  ";
    let (metrics, _response_body) = run_quality_request(
        chat_sse_app(&[
                r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"loop loop loop loop loop loop"}}],"usage":{"completion_tokens":6}}"#,
                "[DONE]",
            ]),
        "/v1/chat/completions",
        raw_model,
        "req-quality-1",
        RequestQualityMonitorConfig {
            collect_quality_metrics: true,
            collect_quality_metrics_min_tokens: 1,
            output_repetition_1gram_threshold_min: Some(0.2),
            ..RequestQualityMonitorConfig::default()
        },
        StatusCode::OK,
        br#"{"messages":[],"stream":true}"#,
    )
    .await;
    assert_quality_result(&metrics, raw_model, "matched");
    assert_quality_threshold(&metrics, raw_model, "repetition_1gram");
    assert_no_quality_result(&metrics, raw_model.trim(), "matched");
    assert_no_quality_threshold(&metrics, raw_model.trim(), "repetition_1gram");
}

#[tokio::test]
async fn quic_tunnel_scores_all_choices_in_streamed_output() {
    let (metrics, _response_body) = run_quality_request(
        sse_app(
            "/v1/chat/completions",
            &[
                r#"{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"alpha beta gamma delta"}}],"usage":{"completion_tokens":4}}"#,
                r#"{"object":"chat.completion.chunk","choices":[{"index":1,"delta":{"content":"loop loop loop loop"}}],"usage":{"completion_tokens":8}}"#,
                "[DONE]",
            ],
        ),
        "/v1/chat/completions",
        "model-quality",
        "req-quality-multi-choice",
        RequestQualityMonitorConfig {
            collect_quality_metrics: true,
            collect_quality_metrics_min_tokens: 1,
            output_repetition_1gram_threshold_min: Some(0.3),
            ..RequestQualityMonitorConfig::default()
        },
        StatusCode::OK,
        br#"{"messages":[],"stream":true,"n":2}"#,
    )
    .await;
    assert_quality_result(&metrics, "model-quality", "matched");
    assert_quality_threshold(&metrics, "model-quality", "repetition_1gram");
}

#[tokio::test]
async fn quic_tunnel_skips_quality_metrics_for_non_sse_chat_error_response() {
    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move {
            let mut response = Response::new(Body::from(r#"{"error":"backend overloaded"}"#));
            *response.status_mut() = StatusCode::INTERNAL_SERVER_ERROR;
            response
        }),
    );
    let (metrics, _response_body) = run_quality_request(
        app,
        "/v1/chat/completions",
        "model-quality",
        "req-quality-error",
        output_token_monitor(1),
        StatusCode::INTERNAL_SERVER_ERROR,
        br#"{"messages":[],"stream":true}"#,
    )
    .await;
    assert_quality_metrics_absent(&metrics);
}

chat_quality_test!(
    quic_tunnel_emits_clean_quality_metrics_once_for_clean_sse_output,
    chat_sse_app(&[
        r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"alpha beta gamma delta"}}]}"#,
        "[DONE]",
    ]),
    "req-quality-clean",
    { collect_quality_metrics: true, collect_quality_metrics_min_tokens: 1 },
    |metrics| {
        assert_quality_result(&metrics, "model-quality", "clean");
        assert_no_quality_result(&metrics, "model-quality", "matched");
        assert_no_metric(&metrics, "pylon_quality_threshold_matches_total");
    }
);

chat_quality_test!(
    quic_tunnel_emits_skipped_quality_metrics_for_unevaluated_streamed_output,
    chat_sse_app(&[
        r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"alpha beta gamma"}}]}"#,
        "[DONE]",
    ]),
    "req-quality-skipped",
    { collect_quality_metrics: true, output_repetition_1gram_threshold_min: Some(0.3) },
    |metrics| {
        assert_quality_result(&metrics, "model-quality", "skipped");
        assert_no_quality_result(&metrics, "model-quality", "clean");
        assert_no_quality_result(&metrics, "model-quality", "matched");
        assert_no_metric(&metrics, "pylon_quality_threshold_matches_total");
    }
);

chat_quality_test!(
    quic_tunnel_emits_skipped_quality_metrics_for_role_only_stream_with_token_threshold,
    chat_sse_app(&[
        r#"{"object":"chat.completion.chunk","choices":[{"delta":{"role":"assistant"}}],"usage":{"completion_tokens":3}}"#,
        "[DONE]",
    ]),
    "req-quality-role-only",
    { output_tokens_threshold_min: Some(10) },
    |metrics| {
        assert_quality_result(&metrics, "model-quality", "skipped");
        assert_no_quality_result(&metrics, "model-quality", "clean");
        assert_no_quality_result(&metrics, "model-quality", "matched");
        assert_no_metric(&metrics, "pylon_quality_threshold_matches_total");
    }
);

chat_quality_test!(
    quic_tunnel_emits_clean_quality_metrics_for_below_threshold_text_stream,
    chat_sse_app(&[
        r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"alpha beta gamma"}}],"usage":{"completion_tokens":3}}"#,
        "[DONE]",
    ]),
    "req-quality-token-clean",
    { output_tokens_threshold_min: Some(10) },
    |metrics| {
        assert_quality_result(&metrics, "model-quality", "clean");
        assert_no_quality_result(&metrics, "model-quality", "skipped");
        assert_no_quality_result(&metrics, "model-quality", "matched");
        assert_no_metric(&metrics, "pylon_quality_threshold_matches_total");
    }
);

chat_quality_test!(
    quic_tunnel_never_emits_quality_metrics_when_monitor_is_disabled,
    chat_sse_app(&[
        r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"alpha beta gamma delta"}}]}"#,
        "[DONE]",
    ]),
    "req-quality-disabled",
    {},
    |metrics| {
        assert_quality_metrics_absent(&metrics);
    }
);

#[tokio::test]
async fn quic_tunnel_never_emits_quality_metrics_for_non_chat_requests() {
    let app = Router::new().route(
        "/v1/embeddings",
        post(|_req: Request| async move { Response::new(Body::from(r#"{"embedding":[1,2,3]}"#)) }),
    );
    let (metrics, _response_body) = run_quality_request(
        app,
        "/v1/embeddings",
        "model-quality",
        "req-quality-non-chat",
        output_token_monitor(1),
        StatusCode::OK,
        br#"{"input":"hello"}"#,
    )
    .await;
    assert_quality_metrics_absent(&metrics);
}

#[tokio::test]
async fn embeddings_tunnel_forwards_json_without_stream() {
    let app = Router::new().route(
            "/v1/embeddings",
            post(|req: Request| async move {
                let body = axum::body::to_bytes(req.into_body(), 1024 * 1024)
                    .await
                    .unwrap();
                assert_eq!(
                    body,
                    Bytes::from_static(
                        br#"{"model":"model-embed","input":["alpha","beta"]}"#
                    )
                );
                Response::builder()
                    .header("content-type", "application/json")
                    .body(Body::from(
                        r#"{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0},{"object":"embedding","embedding":[0.3,0.4],"index":1}],"model":"model-embed","usage":{"prompt_tokens":11,"total_tokens":11}}"#,
                    ))
                    .unwrap()
            }),
        );
    let (mut tunnel, rx) = observed_tunnel_for(app).await;
    tunnel
        .send_json(
            "/v1/embeddings",
            "model-embed",
            "req-embed-forward",
            br#"{"model":"model-embed","input":["alpha","beta"]}"#,
        )
        .await;
    tunnel.response_head(StatusCode::OK).await;
    let payload: serde_json::Value =
        serde_json::from_slice(&read_response_bytes(&mut tunnel.recv).await).unwrap();
    assert_eq!(payload["object"], "list");
    assert_eq!(payload["data"].as_array().unwrap().len(), 2);

    let observation = recv_terminal_observation(&rx).await;
    assert_eq!(observation.endpoint, RequestObservationEndpoint::Embeddings);
    assert_eq!(observation.request_id, "req-embed-forward");
    assert_eq!(observation.model_id, "model-embed");
    assert_eq!(observation.input_tokens, 11);
    assert_eq!(observation.embedding_items, 2);
    assert!(observation.embedding_items_observed);
    assert_eq!(observation.state, RequestObservationState::Complete);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn embeddings_tunnel_rejects_malformed_json_before_upstream() {
    let hits = Arc::new(AtomicUsize::new(0));
    let app = counting_app("/v1/embeddings", hits.clone());
    let (mut tunnel, rx) = observed_tunnel_for(app).await;
    tunnel
        .send(
            embeddings_tunnel_headers("req-embed-bad-json"),
            br#"{"model":"model-embed","input":"unterminated"#,
        )
        .await;
    tunnel.response_head(StatusCode::BAD_REQUEST).await;
    let body = read_response_text(&mut tunnel.recv).await;
    assert!(
        body.contains("request body must be valid JSON"),
        "expected invalid JSON error, got {body:?}"
    );
    assert_eq!(hits.load(Ordering::SeqCst), 0);

    let observation = recv_terminal_observation(&rx).await;
    assert_eq!(observation.endpoint, RequestObservationEndpoint::Embeddings);
    assert_eq!(observation.request_id, "req-embed-bad-json");
    assert!(!observation.embedding_items_observed);
    assert_eq!(observation.state, RequestObservationState::Failed);

    tunnel.shutdown().await;
}

#[derive(Clone, Copy)]
struct DirectEmbeddingsCase {
    protocol: TunnelTransportProtocol,
    success_path: &'static str,
    request_id: &'static str,
    request_body: &'static [u8],
    response_data_json: &'static str,
    expected_first_embedding_json: &'static str,
    expected_items: usize,
}

async fn assert_direct_embeddings_case(case: DirectEmbeddingsCase) {
    let hits = Arc::new(AtomicUsize::new(0));
    let hits_for_app = hits.clone();
    let app = Router::new().route(
        "/v1/embeddings",
        post(move |req: Request| {
            let hits = hits_for_app.clone();
            async move {
                hits.fetch_add(1, Ordering::Relaxed);
                let path = req
                    .uri()
                    .path_and_query()
                    .map_or_else(|| req.uri().path().to_string(), |value| value.to_string());
                let model = req.headers()["x-model"].to_str().unwrap().to_string();
                let body = axum::body::to_bytes(req.into_body(), 1024 * 1024)
                    .await
                    .unwrap();
                Json(serde_json::json!({
                    "path": path,
                    "model": model,
                    "body": String::from_utf8(body.to_vec()).unwrap(),
                    "object": "list",
                    "data": serde_json::from_str::<serde_json::Value>(case.response_data_json)
                        .unwrap(),
                    "usage": {"prompt_tokens": 11, "total_tokens": 11}
                }))
            }
        }),
    );
    let (runtime_state, rx) = observed_runtime(16);
    let mut config = test_tunnel_config_for(app).await;
    config.tunnel_protocol = case.protocol;
    config.forwarding.runtime_state = runtime_state;
    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let client = DirectTunnelClient::connect(case.protocol, tunnel.listen_addr()).await;

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", case.request_id.parse().unwrap());
    headers.insert("x-model", "model-embed".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    let response = client
        .send(case.success_path, headers, case.request_body)
        .await;
    assert_eq!(response.status, StatusCode::OK);
    let payload: serde_json::Value = serde_json::from_slice(&response.body).unwrap();
    assert_eq!(payload["path"], case.success_path);
    assert_eq!(payload["model"], "model-embed");
    assert_eq!(
        payload["body"],
        String::from_utf8(case.request_body.to_vec()).unwrap()
    );
    assert_eq!(
        payload["data"].as_array().unwrap().len(),
        case.expected_items
    );
    assert_eq!(
        payload["data"][0]["embedding"],
        serde_json::from_str::<serde_json::Value>(case.expected_first_embedding_json).unwrap()
    );
    assert_eq!(hits.load(Ordering::Relaxed), 1);

    let observation = recv_terminal_observation(&rx).await;
    assert_eq!(observation.endpoint, RequestObservationEndpoint::Embeddings);
    assert_eq!(observation.request_id, case.request_id);
    assert_eq!(observation.embedding_items, case.expected_items as u64);
    assert!(observation.embedding_items_observed);
    assert_eq!(observation.state, RequestObservationState::Complete);

    let mut headers = HeaderMap::new();
    headers.insert(
        "x-request-id",
        format!("{}-missing", case.request_id).parse().unwrap(),
    );
    headers.insert("x-model", "model-embed".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    let response = client
        .send(
            "/v1/embeddings",
            headers,
            br#"{"model":"model-embed","input":"alpha"}"#,
        )
        .await;
    assert_eq!(response.status, StatusCode::BAD_REQUEST);
    assert!(
        String::from_utf8(response.body)
            .unwrap()
            .contains("missing required x-input-tokens header")
    );
    assert_eq!(hits.load(Ordering::Relaxed), 1);

    client.close();
    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn http3_embeddings_tunnel_forwards_json_and_validates_required_headers() {
    assert_direct_embeddings_case(DirectEmbeddingsCase {
        protocol: TunnelTransportProtocol::Http3,
        success_path: "/v1/embeddings?encoding=base64",
        request_id: "req-h3-embeddings",
        request_body: br#"{"model":"model-embed","input":"alpha","encoding_format":"base64"}"#,
        response_data_json: r#"[{"object":"embedding","embedding":"AAAA","index":0}]"#,
        expected_first_embedding_json: r#""AAAA""#,
        expected_items: 1,
    })
    .await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn webtransport_embeddings_tunnel_forwards_json_and_validates_required_headers() {
    assert_direct_embeddings_case(DirectEmbeddingsCase {
        protocol: TunnelTransportProtocol::WebTransport,
        success_path: "/v1/embeddings?source=webtransport",
        request_id: "req-wt-embeddings",
        request_body: br#"{"model":"model-embed","input":["alpha","beta"]}"#,
        response_data_json: r#"[{"object":"embedding","embedding":[0.1,0.2],"index":0},{"object":"embedding","embedding":[0.3,0.4],"index":1}]"#,
        expected_first_embedding_json: "[0.1,0.2]",
        expected_items: 2,
    })
    .await;
}

#[tokio::test]
async fn quic_tunnel_skips_quality_metrics_when_stream_times_out_before_output() {
    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move {
            axum::response::Sse::new(async_stream::stream! {
                tokio::time::sleep(Duration::from_millis(50)).await;
                yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
            })
        }),
    );
    let (mut config, metrics) = metered_test_tunnel_config_for(app).await;
    config.forwarding.first_output_timeout = Duration::from_millis(10);
    config.forwarding.request_quality_monitor = output_token_monitor(1);

    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel
        .send_json(
            "/v1/chat/completions",
            "model-quality",
            "req-quality-timeout",
            br#"{"messages":[],"stream":true}"#,
        )
        .await;
    tunnel.response_head(StatusCode::OK).await;
    assert!(tunnel.recv.recv_body().await.is_err());

    let metrics = metrics_text(&metrics);
    assert_quality_metrics_absent(&metrics);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_skips_quality_metrics_when_stream_ends_before_output() {
    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move {
            axum::response::Sse::new(async_stream::stream! {
                yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
            })
        }),
    );
    let (mut config, metrics) = metered_test_tunnel_config_for(app).await;
    config.forwarding.request_quality_monitor = output_token_monitor(1);

    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel
        .send_json(
            "/v1/chat/completions",
            "model-quality",
            "req-quality-eof",
            br#"{"messages":[],"stream":true}"#,
        )
        .await;

    if let Ok(response_headers) = tunnel.recv.recv_header().await {
        assert_eq!(response_headers["x-status"], "200");
        let _ = tunnel.recv.recv_body().await;
    }

    let metrics = metrics_text(&metrics);
    assert_quality_metrics_absent(&metrics);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_records_one_quality_check_for_multi_chunk_stream() {
    let (metrics, _response_body) = run_chat_quality_request(
        chat_sse_app(&[
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"alpha"}}]}"#,
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":" beta"}}]}"#,
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":" gamma"}}]}"#,
            "[DONE]",
        ]),
        "req-quality-multi-chunk",
        output_token_monitor(2),
    )
    .await;
    assert_quality_result(&metrics, "model-quality", "matched");
    assert_no_quality_result(&metrics, "model-quality", "clean");
}

#[tokio::test]
async fn quic_tunnel_rejects_non_streaming_request_body() {
    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move { Response::new(Body::from("{\"ok\":true}")) }),
    );
    let mut tunnel = RawTunnelTest::start(test_tunnel_config_for(app).await).await;
    let headers =
        tunnel_request_headers("/v1/chat/completions", "model-a", "req-non-stream-1", "11");
    tunnel
        .send(headers, br#"{"messages":[],"stream":false}"#)
        .await;
    let response = tunnel.response(StatusCode::BAD_REQUEST).await;
    assert_problem_response(
        &response,
        400,
        "Bad Request",
        "/v1/chat/completions requests must set stream=true",
    );
}

#[tokio::test]
async fn quic_tunnel_rejects_non_streaming_responses_request_body() {
    let upstream_hits = Arc::new(AtomicUsize::new(0));
    let app = counting_app("/v1/responses", upstream_hits.clone());
    let mut tunnel = RawTunnelTest::start(test_tunnel_config_for(app).await).await;
    let headers =
        tunnel_request_headers("/v1/responses", "model-a", "req-non-stream-responses", "11");
    tunnel
        .send(headers, br#"{"input":"hello","stream":false}"#)
        .await;
    let response = tunnel.response(StatusCode::BAD_REQUEST).await;
    assert_problem_response(
        &response,
        400,
        "Bad Request",
        "/v1/responses requests must set stream=true",
    );
    assert_eq!(
        upstream_hits.load(Ordering::SeqCst),
        0,
        "non-streaming responses requests should not reach upstream"
    );
}

#[tokio::test]
async fn quic_tunnel_rejects_missing_required_headers_for_responses() {
    let upstream_hits = Arc::new(AtomicUsize::new(0));
    let app = counting_app("/v1/responses", upstream_hits.clone());
    let tunnel = start_quic_http_tunnel(test_tunnel_config_for(app).await)
        .await
        .unwrap();

    for (missing_header, expected_body_fragment) in [
        ("x-request-id", "x-request-id"),
        ("x-model", "x-model"),
        ("x-input-tokens", "x-input-tokens"),
    ] {
        let mut headers =
            tunnel_request_headers("/v1/responses", "model-a", "req-responses-required", "11");
        headers.remove(missing_header);
        let response = send_raw_quic_quic_json_request(
            tunnel.listen_addr(),
            headers,
            br#"{"input":"hello","stream":true}"#,
        )
        .await;
        assert_eq!(
            response.status,
            StatusCode::BAD_REQUEST,
            "missing {missing_header} should be rejected"
        );
        assert_problem_response(
            &response,
            400,
            "Bad Request",
            &format!("missing required {expected_body_fragment} header"),
        );
    }

    let mut headers =
        tunnel_request_headers("/v1/responses", "model-a", "req-invalid-input-tokens", "11");
    headers.insert("x-input-tokens", "not-a-count".parse().unwrap());
    let response = send_raw_quic_quic_json_request(
        tunnel.listen_addr(),
        headers,
        br#"{"input":"hello","stream":true}"#,
    )
    .await;
    assert_eq!(response.status, StatusCode::BAD_REQUEST);
    assert_problem_response(
        &response,
        400,
        "Bad Request",
        "invalid x-input-tokens header",
    );

    assert_eq!(
        upstream_hits.load(Ordering::SeqCst),
        0,
        "requests missing required headers should not reach upstream"
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_times_out_when_no_output_event_arrives() {
    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move {
            axum::response::Sse::new(async_stream::stream! {
                tokio::time::sleep(Duration::from_millis(50)).await;
                yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
            })
        }),
    );
    let mut config = test_tunnel_config_for(app).await;
    config.forwarding.first_output_timeout = Duration::from_millis(10);
    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel
        .send_json(
            "/v1/chat/completions",
            "model-a",
            "req-timeout-1",
            br#"{"messages":[],"stream":true}"#,
        )
        .await;
    tunnel.response_head(StatusCode::OK).await;
    assert!(tunnel.recv.recv_body().await.is_err());

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_rejects_unterminated_sse_event_above_buffer_limit() {
    let (send_oversized_event, recv_oversized_event) = flume::bounded(1);

    let app = Router::new().route(
        "/v1/chat/completions",
        post(move |_req: Request| {
            let recv_oversized_event = recv_oversized_event.clone();
            async move {
                let body = Body::from_stream(async_stream::stream! {
                    recv_oversized_event
                        .recv_async()
                        .await
                        .expect("test should release the oversized SSE event");
                    yield Ok::<_, std::convert::Infallible>(Bytes::from_static(b"data: 12345678"));
                });
                let mut response = Response::new(body);
                response.headers_mut().insert(
                    axum::http::header::CONTENT_TYPE,
                    HeaderValue::from_static("text/event-stream"),
                );
                response
            }
        }),
    );
    let mut config = test_tunnel_config_for(app).await;
    config.forwarding.max_sse_buffer_bytes = 11;
    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel
        .send_json(
            "/v1/chat/completions",
            "model-buffer-limit",
            "req-buffer-limit-1",
            br#"{"messages":[],"stream":true}"#,
        )
        .await;
    tunnel.response_head(StatusCode::OK).await;

    send_oversized_event.send(()).unwrap();
    assert!(
        tunnel.recv.recv_body().await.is_err(),
        "an oversized unterminated SSE event must reset the tunnel stream while reading its body"
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_times_out_when_subsequent_output_event_arrives_too_late() {
    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"first"}}]}"#)
                    );
                    tokio::time::sleep(Duration::from_millis(50)).await;
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"second"}}]}"#)
                    );
                })
            }),
        );
    let mut config = test_tunnel_config_for(app).await;
    config.forwarding.first_output_timeout = Duration::from_millis(100);
    config.forwarding.output_chunk_timeout = Duration::from_millis(10);
    let mut tunnel = RawTunnelTest::start(config).await;
    tunnel
        .send_json(
            "/v1/chat/completions",
            "model-a",
            "req-timeout-2",
            br#"{"messages":[],"stream":true}"#,
        )
        .await;
    tunnel.response_head(StatusCode::OK).await;

    let first_chunk = tunnel
        .recv
        .recv_body()
        .await
        .unwrap()
        .into_body()
        .expect("first chunk");
    let first_text = std::str::from_utf8(&first_chunk).unwrap();
    assert!(first_text.contains("first"));

    let next_chunk = tunnel.recv.recv_body().await;
    assert!(
        next_chunk.is_err(),
        "expected stream read error after output timeout"
    );

    tunnel.shutdown().await;
}

fn trusted_client_config() -> Result<ClientConfig> {
    stargate_tls::build_insecure_quic_client_config()
}

#[test]
fn derive_sni_extracts_hostname() {
    assert_eq!(
        derive_sni("pod-a.stargate.external:50072"),
        "pod-a.stargate.external"
    );
}

#[test]
fn derive_sni_falls_back_for_ip() {
    assert_eq!(derive_sni("10.0.0.1:50072"), "stargate");
}

#[test]
fn derive_sni_falls_back_for_localhost() {
    assert_eq!(derive_sni("localhost:50072"), "stargate");
}

#[test]
fn derive_sni_falls_back_for_ipv6() {
    assert_eq!(derive_sni("::1:50072"), "stargate");
}

#[test]
fn derive_sni_falls_back_for_bracketed_ipv6() {
    assert_eq!(derive_sni("[::1]:50072"), "stargate");
}

#[test]
fn derive_sni_handles_bare_hostname() {
    assert_eq!(
        derive_sni("pod-a.stargate.external"),
        "pod-a.stargate.external"
    );
}

#[test]
fn target_authority_preserves_advertised_hostname() {
    assert_eq!(
        target_authority("pod-a.stargate.external:50072"),
        "pod-a.stargate.external:50072"
    );
}

#[test]
fn target_authority_brackets_ipv6_address() {
    assert_eq!(target_authority("::1:50072"), "[::1]:50072");
}

#[test]
fn reverse_quic_sni_prefers_override_and_derives_default() {
    let mut config = ReverseQuicTunnelConfig::new(
        "stargate-quic-lb.stargate.svc.cluster.local:50072".to_string(),
        "backend-a".to_string(),
        "http://127.0.0.1:8000".to_string(),
    );
    assert_eq!(
        reverse_quic_sni(&config),
        "stargate-quic-lb.stargate.svc.cluster.local"
    );

    config.sni_override =
        Some("stargate-0.stargate-headless.stargate.svc.cluster.local".to_string());
    assert_eq!(
        reverse_quic_sni(&config),
        "stargate-0.stargate-headless.stargate.svc.cluster.local"
    );
}

#[test]
fn reverse_quic_dial_candidates_preserve_every_resolved_address() {
    let ipv6_addr: SocketAddr = "[fd00::1]:50072".parse().unwrap();
    let ipv4_addr: SocketAddr = "10.0.0.4:50072".parse().unwrap();

    let candidates = reverse_quic_dial_candidates(&[ipv6_addr, ipv4_addr]).unwrap();

    assert_eq!(candidates, vec![ipv4_addr, ipv6_addr]);
}

#[test]
fn reverse_quic_resolved_target_keeps_ipv6_when_it_is_the_only_candidate() {
    let ipv6_addr: SocketAddr = "[fd00::1]:50072".parse().unwrap();

    let candidates = reverse_quic_dial_candidates(&[ipv6_addr]).unwrap();

    assert_eq!(candidates, vec![ipv6_addr]);
}

#[test]
fn reverse_quic_resolved_target_rejects_empty_resolution() {
    assert!(matches!(
        reverse_quic_dial_candidates(&[]),
        Err(TunnelError::NoResolvedAddress)
    ));
}

#[tokio::test]
async fn reverse_quic_connection_retries_later_resolved_candidates() {
    let first: SocketAddr = "127.0.0.1:50072".parse().unwrap();
    let second: SocketAddr = "127.0.0.1:50073".parse().unwrap();
    let attempts = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
    let attempts_for_connect = attempts.clone();

    let (connected_to, value) =
        connect_first_reverse_quic_candidate(&[first, second], move |candidate| {
            let attempts = attempts_for_connect.clone();
            async move {
                attempts
                    .lock()
                    .expect("attempts lock should not be poisoned")
                    .push(candidate);
                if candidate == first {
                    Err(anyhow::anyhow!("first candidate rejected"))
                } else {
                    Ok("connected")
                }
            }
        })
        .await
        .expect("later candidate should be attempted after the first failure");

    assert_eq!(connected_to, second);
    assert_eq!(value, "connected");
    assert_eq!(
        attempts
            .lock()
            .expect("attempts lock should not be poisoned")
            .as_slice(),
        &[first, second]
    );
}

#[tokio::test]
async fn reverse_quic_endpoint_connect_logs_attempt_resolution_and_connection_metadata() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let server_config = make_server_config(
        &ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::Http3,
    )
    .expect("server config should build");
    let server_endpoint = Endpoint::server(server_config, "127.0.0.1:0".parse().unwrap()).unwrap();
    let server_addr = server_endpoint.local_addr().unwrap();
    let server_task = tokio::spawn(async move {
        let incoming = server_endpoint.accept().await.unwrap();
        let connection = incoming.await.unwrap();
        tokio::time::timeout(Duration::from_secs(1), connection.closed())
            .await
            .expect("client close should reach server");
    });

    let mut config = ReverseQuicTunnelConfig::new(
        server_addr.to_string(),
        "backend-a".to_string(),
        "http://127.0.0.1:8000".to_string(),
    );
    config.quic_insecure = true;
    config.tunnel_protocol = TunnelTransportProtocol::Http3;
    let subscriber = RecordingDebugSubscriber::default();
    let reverse_connection = {
        let dispatch = tracing::Dispatch::new(subscriber.clone());
        let _default_guard = tracing::dispatcher::set_default(&dispatch);
        connect_reverse_quic_endpoint(&config)
            .await
            .expect("reverse QUIC endpoint should connect to local test server")
    };

    let events = subscriber.take_events();
    let server_addr = server_addr.to_string();
    let attempt_event = event_by_message(&events, "attempting Stargate reverse QUIC connection");
    assert_event_field(attempt_event, "target_addr", &server_addr);
    assert_event_field(attempt_event, "tunnel_protocol", "http3");
    assert_event_field(attempt_event, "alpn_protocols", "[\"h3\"]");
    assert_event_field(attempt_event, "quic_insecure", "true");
    let resolved_event = event_by_message(&events, "resolved Stargate reverse QUIC target");
    let expected_candidates = format!("[{server_addr}]");
    assert_event_field(resolved_event, "dial_candidates", &expected_candidates);
    assert_event_field(resolved_event, "tunnel_protocol", "http3");
    assert_event_field(resolved_event, "alpn_protocols", "[\"h3\"]");
    assert_event_field(resolved_event, "quic_insecure", "true");
    let connected_event = event_by_message(&events, "Stargate reverse QUIC connection established");
    assert_event_field(connected_event, "transport", "quic");
    for field in ["target_addr", "dial_target", "remote_addr"] {
        assert_event_field(connected_event, field, &server_addr);
    }
    assert!(
        connected_event.contains_key("stable_id"),
        "connected event should include the Quinn stable connection id"
    );
    assert!(
        connected_event.contains_key("stats"),
        "connected event should include Quinn connection stats"
    );

    reverse_connection
        .connection
        .close(0u32.into(), b"test complete");
    server_task
        .await
        .expect("server task should observe client close");
}

#[test]
fn build_trusted_client_config_insecure_succeeds_without_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let result = build_trusted_client_config(None, true, TunnelTransportProtocol::RawQuic);
    assert!(result.is_ok());
}

#[test]
fn build_trusted_client_config_secure_fails_without_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let result = build_trusted_client_config(None, false, TunnelTransportProtocol::RawQuic);
    assert!(result.is_err());
}

#[test]
fn build_trusted_client_config_secure_succeeds_with_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (cert_pem, _key_pem) = stargate_tls::generate_self_signed_cert().unwrap();
    let result =
        build_trusted_client_config(Some(&cert_pem), false, TunnelTransportProtocol::RawQuic);
    assert!(result.is_ok());
}

#[test]
fn make_server_config_self_signed_when_none() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let result = make_server_config(
        &ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::RawQuic,
    );
    assert!(result.is_ok());
}

#[test]
fn make_server_config_uses_provided_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (cert_pem, key_pem) = stargate_tls::generate_self_signed_cert().unwrap();
    let result = make_server_config(
        &ServerTlsIdentity::Provided { cert_pem, key_pem },
        TunnelTransportProtocol::RawQuic,
    );
    assert!(result.is_ok());
}

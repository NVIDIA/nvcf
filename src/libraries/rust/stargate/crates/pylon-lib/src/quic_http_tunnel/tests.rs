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
    MAX_SPECULATIVE_REQUEST_BODY_PREALLOC_BYTES, ObserverGuard, TunnelServerApp,
    extend_body_from_buf, is_health_request_path, otel_parent_from_headers,
    pylon_upstream_parent_context, request_body_buffer, request_body_capacity,
    should_forward_header, should_forward_response_header,
};
use super::endpoint::{
    build_trusted_client_config, derive_sni, make_server_config, target_authority,
};
use super::reverse::{
    connect_reverse_quic_endpoint, reverse_quic_connect_debug_target, reverse_quic_resolved_target,
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
use stargate_protocol::TunnelTransportProtocol;
use stargate_protocol::tunnel_contract::WEBTRANSPORT_TUNNEL_PATH;
use stargate_tls::ServerTlsIdentity;
use tokio::net::TcpListener;

use crate::queue_admission::{QueueAdmissionTracker, QueueTrackedRequestGuard};
use crate::request_observer::RequiredTunnelHeaders;
use crate::stats::PylonMetrics;
use crate::{StatsCollectorConfig, request_observation_channel, start_stats_collector};

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

struct DirectWebTransportSession {
    _endpoint: Endpoint,
    connection: quinn::Connection,
    _h3_connection: h3::client::Connection<h3_quinn::Connection, Bytes>,
    _connect_stream: TestWebTransportConnectStream,
    session_id: u64,
}

#[test]
fn reverse_tunnel_error_variants_preserve_source_chains() {
    let tls = TunnelError::Tls {
        source: anyhow::anyhow!("missing trusted cert"),
    };
    assert_eq!(
        tls.source()
            .expect("TLS errors should expose their source")
            .to_string(),
        "missing trusted cert"
    );

    let connect = TunnelError::Connect {
        context: "resolving reverse tunnel address",
        source: std::io::Error::other("DNS lookup failed").into(),
    };
    assert_eq!(
        connect
            .source()
            .expect("connect errors should expose their source")
            .to_string(),
        "DNS lookup failed"
    );

    let handshake = TunnelError::Handshake {
        context: "reading reverse tunnel auth token",
        source: anyhow::anyhow!("auth token command failed"),
    };
    assert_eq!(
        handshake
            .source()
            .expect("handshake errors should expose their source")
            .to_string(),
        "auth token command failed"
    );

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
        direct.max_request_body_bytes,
        reverse.max_request_body_bytes
    );
    assert_eq!(direct.first_output_timeout, reverse.first_output_timeout);
    assert_eq!(direct.output_chunk_timeout, reverse.output_chunk_timeout);
    assert!(direct.request_observation_tx.is_none());
    assert!(reverse.request_observation_tx.is_none());
    assert!(direct.metrics.is_none());
    assert!(reverse.metrics.is_none());
}

#[test]
fn reverse_tunnel_app_from_config_preserves_forwarding_settings() {
    let (observation_tx, _observation_rx) = flume::unbounded();
    let metrics = PylonMetrics::new().expect("metrics should initialize");
    let mut config = ReverseQuicTunnelConfig::new(
        "router.local:443".to_string(),
        "backend-1".to_string(),
        "http://upstream.local".to_string(),
    );
    config.max_request_body_bytes = 1234;
    config.first_output_timeout = Duration::from_millis(55);
    config.output_chunk_timeout = Duration::from_millis(77);
    config.request_observation_tx = Some(observation_tx);
    config.retry.local_connect_failures_retryable = true;
    config.queue_mismatch_retry.enabled = false;
    config.metrics = Some(metrics.clone());

    let app = TunnelServerApp::from_reverse_config(config);

    assert_eq!(app.inference_server_id, "backend-1");
    assert_eq!(app.upstream_http_base_url, "http://upstream.local");
    assert_eq!(app.max_request_body_bytes, 1234);
    assert_eq!(app.first_output_timeout, Duration::from_millis(55));
    assert_eq!(app.output_chunk_timeout, Duration::from_millis(77));
    assert!(app.request_observation_tx.is_some());
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
    assert!(!should_forward_header(&HeaderName::from_bytes(
        b"Connection"
    )?));
    assert!(!should_forward_header(&HeaderName::from_bytes(
        b"Proxy-Connection"
    )?));
    assert!(!should_forward_header(&HeaderName::from_bytes(b"Host")?));
    assert!(!should_forward_header(&HeaderName::from_bytes(
        b"X-Method"
    )?));
    assert!(!should_forward_header(&HeaderName::from_bytes(b"X-Path")?));
    assert!(!should_forward_header(&HeaderName::from_bytes(
        b"X-Stargate-Expected-Queue-Ms"
    )?));
    assert!(should_forward_header(&HeaderName::from_bytes(
        b"X-Request-Id"
    )?));
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

    assert!(!should_forward_response_header(
        &HeaderName::from_bytes(b"Connection")?,
        &retry,
    ));
    assert!(!should_forward_response_header(
        &HeaderName::from_bytes(b"X-Stargate-Retryable")?,
        &retry,
    ));
    assert!(should_forward_response_header(
        &HeaderName::from_bytes(b"X-Kv-Cache-Hit")?,
        &retry,
    ));
    Ok(())
}

#[test]
fn request_body_buffer_uses_valid_declared_content_length() -> Result<()> {
    let mut headers = HeaderMap::new();
    headers.insert(reqwest::header::CONTENT_LENGTH, "4096".parse()?);

    let body = request_body_buffer(&headers, 8192)?;

    assert_eq!(body.len(), 0);
    assert!(body.capacity() >= 4096);
    Ok(())
}

#[test]
fn request_body_buffer_caps_large_valid_declared_content_length() -> Result<()> {
    let mut headers = HeaderMap::new();
    headers.insert(reqwest::header::CONTENT_LENGTH, "1048576".parse()?);

    let capacity = request_body_capacity(&headers, 2 * 1024 * 1024)?;

    assert_eq!(capacity, Some(MAX_SPECULATIVE_REQUEST_BODY_PREALLOC_BYTES));
    Ok(())
}

#[test]
fn request_body_buffer_rejects_declared_length_above_limit() -> Result<()> {
    let mut headers = HeaderMap::new();
    headers.insert(reqwest::header::CONTENT_LENGTH, "4097".parse()?);

    let Err(error) = request_body_buffer(&headers, 4096) else {
        panic!("oversized content-length should fail");
    };

    assert!(error.to_string().contains("request body too large"));
    Ok(())
}

#[test]
fn request_body_buffer_ignores_invalid_content_length() -> Result<()> {
    let mut headers = HeaderMap::new();
    headers.insert(reqwest::header::CONTENT_LENGTH, "not-a-number".parse()?);

    let body = request_body_buffer(&headers, 4096)?;

    assert_eq!(body.len(), 0);
    assert_eq!(body.capacity(), 0);
    Ok(())
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

async fn read_response_text(recv: &mut stargate_protocol::RecvStream) -> String {
    let mut response_body = Vec::new();
    while let Some(chunk) = recv.recv_body().await.unwrap().into_body() {
        response_body.extend_from_slice(&chunk);
    }
    String::from_utf8(response_body).unwrap()
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
    QueueAdmissionTracker,
    QueueTrackedRequestGuard,
    Arc<PylonMetrics>,
) {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();
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
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.tunnel_protocol = tunnel_protocol;
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.queue_mismatch_retry.enabled = enabled;
    config.queue_mismatch_retry.retry_after_ms = Some(125);
    config
        .queue_tracker
        .update_model_throughput("model-a", 100.0);
    let queue_tracker = config.queue_tracker.clone();
    let queued_request = config.queue_tracker.track_request(&RequiredTunnelHeaders {
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
        queue_tracker,
        queued_request,
        metrics,
    )
}

async fn send_custom_quic_json_request(
    tunnel_addr: SocketAddr,
    headers: HeaderMap,
    body: &'static [u8],
) -> (HeaderMap, Vec<u8>) {
    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(body)).await.unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    let mut response_body = Vec::new();
    while let Some(chunk) = recv.recv_body().await.unwrap().into_body() {
        response_body.extend_from_slice(&chunk);
    }
    connection.close(0u32.into(), b"test complete");
    (response_headers, response_body)
}

fn assert_problem_response(
    response_headers: &HeaderMap,
    response_text: &str,
    status: u16,
    title: &str,
    detail: &str,
) {
    assert_eq!(
        response_headers
            .get(reqwest::header::CONTENT_TYPE)
            .unwrap()
            .to_str()
            .unwrap(),
        "application/problem+json"
    );
    let problem: serde_json::Value = serde_json::from_str(response_text).unwrap();
    assert_eq!(problem["type"], "about:blank");
    assert_eq!(problem["title"], title);
    assert_eq!(problem["status"], status);
    assert_eq!(problem["detail"], detail);
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
async fn custom_tunnel_tls_configs_do_not_negotiate_alpn() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let client_config = build_trusted_client_config(None, true, TunnelTransportProtocol::Custom)
        .expect("client config");
    let server_config = make_server_config(
        &ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::Custom,
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
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let upstream_addr = listener.local_addr().unwrap();
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
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{upstream_addr}"),
    );
    config.tunnel_protocol = TunnelTransportProtocol::Http3;
    let tunnel = start_quic_http_tunnel(config).await.unwrap();

    let result = tokio::time::timeout(Duration::from_secs(2), async {
        let mut endpoint = Endpoint::client("127.0.0.1:0".parse().unwrap()).unwrap();
        endpoint.set_default_client_config(
            stargate_tls::build_insecure_quic_client_config_with_alpn(
                TunnelTransportProtocol::Http3.alpn_protocols(),
            )
            .unwrap(),
        );
        let connection = endpoint
            .connect(tunnel.listen_addr(), "stargate")
            .unwrap()
            .await
            .unwrap();
        let (mut driver, mut send_request) = h3::client::builder()
            .build(h3_quinn::Connection::new(connection.clone()))
            .await
            .unwrap();
        let mut driver_task =
            tokio::spawn(async move { future::poll_fn(|cx| driver.poll_close(cx)).await });

        let uri: http::Uri = format!(
            "https://stargate:{}/v1/responses?source=http3",
            tunnel.listen_addr().port()
        )
        .parse()
        .unwrap();
        let request = http::Request::builder()
            .method(http::Method::POST)
            .uri(uri)
            .header("x-request-id", "req-h3-direct")
            .header("x-model", "model-h3")
            .header("x-input-tokens", "7")
            .header(reqwest::header::CONTENT_TYPE.as_str(), "application/json")
            .body(())
            .unwrap();
        let mut stream = send_request.send_request(request).await.unwrap();
        stream
            .send_data(Bytes::from_static(br#"{"input":"hi","stream":true}"#))
            .await
            .unwrap();
        stream.finish().await.unwrap();

        let response = stream.recv_response().await.unwrap();
        let mut body = Vec::new();
        while let Some(mut chunk) = stream.recv_data().await.unwrap() {
            while chunk.has_remaining() {
                let len = chunk.remaining();
                body.extend_from_slice(&chunk.copy_to_bytes(len));
            }
        }

        connection.close(0u32.into(), b"test complete");
        if tokio::time::timeout(Duration::from_secs(1), &mut driver_task)
            .await
            .is_err()
        {
            driver_task.abort();
        }
        (response.status(), String::from_utf8(body).unwrap())
    })
    .await
    .expect("h3 request timed out");

    assert_eq!(result.0, StatusCode::OK);
    let payload: serde_json::Value = serde_json::from_str(&result.1).unwrap();
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
) -> (StatusCode, HeaderMap, Vec<u8>) {
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
) -> (StatusCode, HeaderMap, Vec<u8>) {
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
    stargate_protocol::write_webtransport_http_body(
        &mut quinn_send,
        bytes::Bytes::from_static(body),
    )
    .await
    .unwrap();
    stargate_protocol::finish_webtransport_http_stream(&mut quinn_send).unwrap();

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
    (response_head.status, response_head.headers, response_body)
}

async fn send_direct_http3_json_request(
    tunnel_addr: SocketAddr,
    path: &str,
    headers: HeaderMap,
    body: &'static [u8],
) -> (StatusCode, HeaderMap, Vec<u8>) {
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
        (status, headers, response_body)
    })
    .await
    .expect("direct HTTP/3 request timed out")
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_webtransport_stalled_stream_header_does_not_block_later_responses_request() {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let upstream_addr = listener.local_addr().unwrap();
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
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{upstream_addr}"),
    );
    config.tunnel_protocol = TunnelTransportProtocol::WebTransport;
    let (header_wait_tx, header_wait_rx) = flume::bounded(1);
    config.webtransport_stream_header_wait_tx = Some(header_wait_tx);
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
    let (response_status, _response_headers, response_body) = tokio::time::timeout(
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

    assert_eq!(response_status, StatusCode::OK);
    let payload: serde_json::Value = serde_json::from_slice(&response_body).unwrap();
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
    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", path.parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", model.parse().unwrap());
    headers.insert("x-request-id", request_id.parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(body)).await.unwrap();
    send.finish().unwrap();
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
    assert!(
        !metrics.contains("pylon_quality_checks_total"),
        "quality checks should be absent:\n{metrics}"
    );
    assert!(
        !metrics.contains("pylon_quality_threshold_matches_total"),
        "quality threshold matches should be absent:\n{metrics}"
    );
}

#[tokio::test]
async fn quic_tunnel_forwards_to_http_backend() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

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
                *sse.status_mut() = StatusCode::OK;
                sse
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());

    let tunnel = start_quic_http_tunnel(config).await.unwrap();

    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-request-id", "req-tunnel-1".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("x-stargate-expected-queue-ms", "5".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();

    let body = b"{\"messages\":[],\"stream\":true}";
    send.send_body(Bytes::from(&body[..])).await.unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    let status = response_headers.get("x-status").unwrap().to_str().unwrap();
    assert_eq!(status, "200");
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

    let mut response_body = Vec::new();
    while let Some(chunk) = recv.recv_body().await.unwrap().into_body() {
        response_body.extend_from_slice(&chunk);
    }
    let response_text = String::from_utf8(response_body).unwrap();
    assert!(response_text.contains("chat.completion.chunk"));

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_marks_explicit_retryable_upstream_rejection() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

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
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-request-id", "req-retryable-1".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "429"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retryable")
            .unwrap()
            .to_str()
            .unwrap(),
        "true"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retry-reason")
            .unwrap()
            .to_str()
            .unwrap(),
        "upstream_admission_rejected"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retry-after-ms")
            .unwrap()
            .to_str()
            .unwrap(),
        "2000"
    );

    let metrics = metrics_text(&metrics);
    assert!(
            metrics.contains(
                r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="upstream_admission_rejected",status="429"} 1"#
            ),
            "missing retryable response metric:\n{metrics}"
        );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_rejects_queue_estimate_mismatch_before_upstream() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();
    let upstream_hits = Arc::new(AtomicUsize::new(0));
    let upstream_hits_for_app = upstream_hits.clone();

    let app = Router::new().route(
        "/v1/chat/completions",
        post(move |_req: Request| {
            let upstream_hits = upstream_hits_for_app.clone();
            async move {
                upstream_hits.fetch_add(1, Ordering::SeqCst);
                (StatusCode::OK, "unexpected")
            }
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.queue_mismatch_retry.retry_after_ms = Some(125);
    config
        .queue_tracker
        .update_model_throughput("model-a", 100.0);
    let queue_tracker = config.queue_tracker.clone();
    let _queued_request = config.queue_tracker.track_request(&RequiredTunnelHeaders {
        request_id: "req-already-queued".to_string(),
        routing_key: Some("rk-1".to_string()),
        model_id: "model-a".to_string(),
        priority: 0,
        input_tokens: 100,
        accepted_at: std::time::Instant::now(),
    });

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-request-id", "req-queue-mismatch".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("x-stargate-expected-queue-ms", "0".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "429"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retryable")
            .unwrap()
            .to_str()
            .unwrap(),
        "true"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retry-reason")
            .unwrap()
            .to_str()
            .unwrap(),
        "queue_estimate_mismatch"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retry-after-ms")
            .unwrap()
            .to_str()
            .unwrap(),
        "125"
    );

    let mut response_body = Vec::new();
    while let Some(chunk) = recv.recv_body().await.unwrap().into_body() {
        response_body.extend_from_slice(&chunk);
    }
    let response_text = String::from_utf8(response_body).unwrap();
    assert!(response_text.contains("queue_estimate_mismatch"));
    assert_eq!(upstream_hits.load(Ordering::SeqCst), 0);
    assert_eq!(
        queue_tracker.tracked_request_count(),
        1,
        "queue mismatch rejection should not leak the rejected request"
    );

    let metrics = metrics_text(&metrics);
    assert!(
            metrics.contains(
                "# HELP pylon_retryable_responses_total Total number of retryable responses emitted or relayed by pylon"
            ),
            "retryable response HELP text should cover local admission responses:\n{metrics}"
        );
    assert!(
            metrics.contains(
                r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="queue_estimate_mismatch",status="429"} 1"#
            ),
            "missing queue mismatch retry metric:\n{metrics}"
        );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_queue_mismatch_retry_disabled_forwards_to_upstream() {
    let (tunnel, upstream_hits, queue_tracker, _queued_request, metrics) =
        start_queue_mismatch_test_tunnel(TunnelTransportProtocol::Custom, false).await;

    let mut headers = queue_mismatch_request_headers("req-queue-mismatch-disabled");
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    let (response_headers, response_body) = send_custom_quic_json_request(
        tunnel.listen_addr(),
        headers,
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    assert!(response_headers.get("x-stargate-retryable").is_none());
    assert_eq!(String::from_utf8(response_body).unwrap(), "forwarded");
    assert_eq!(upstream_hits.load(Ordering::SeqCst), 1);
    assert_eq!(
        queue_tracker.tracked_request_count(),
        1,
        "disabled queue mismatch admission should still finish the proxied request"
    );

    let metrics = metrics_text(&metrics);
    assert!(
            metrics.contains(
                r#"pylon_queue_admission_decisions_total{inference_server_id="inst-a",model_id="model-a",result="disabled"} 1"#
            ),
            "missing disabled queue admission metric:\n{metrics}"
        );
    assert!(
            !metrics.contains(
                r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="queue_estimate_mismatch",status="429"}"#
            ),
            "disabled queue mismatch admission should not emit retryable rejection metrics:\n{metrics}"
        );

    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn http3_tunnel_rejects_queue_estimate_mismatch_before_upstream() {
    let (tunnel, upstream_hits, queue_tracker, _queued_request, metrics) =
        start_queue_mismatch_test_tunnel(TunnelTransportProtocol::Http3, true).await;

    let (status, response_headers, response_body) = send_direct_http3_json_request(
        tunnel.listen_addr(),
        "/v1/chat/completions",
        queue_mismatch_request_headers("req-h3-queue-mismatch"),
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    assert_eq!(status, StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(
        response_headers
            .get("x-stargate-retryable")
            .unwrap()
            .to_str()
            .unwrap(),
        "true"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retry-reason")
            .unwrap()
            .to_str()
            .unwrap(),
        "queue_estimate_mismatch"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retry-after-ms")
            .unwrap()
            .to_str()
            .unwrap(),
        "125"
    );
    assert!(response_headers.get("x-status").is_none());
    assert!(
        String::from_utf8(response_body)
            .unwrap()
            .contains("queue_estimate_mismatch")
    );
    assert_eq!(upstream_hits.load(Ordering::SeqCst), 0);
    assert_eq!(
        queue_tracker.tracked_request_count(),
        1,
        "HTTP/3 queue mismatch rejection should not leak the rejected request"
    );

    let metrics = metrics_text(&metrics);
    assert!(
            metrics.contains(
                r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="queue_estimate_mismatch",status="429"} 1"#
            ),
            "missing HTTP/3 queue mismatch retry metric:\n{metrics}"
        );

    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn webtransport_tunnel_rejects_queue_estimate_mismatch_before_upstream() {
    let (tunnel, upstream_hits, queue_tracker, _queued_request, metrics) =
        start_queue_mismatch_test_tunnel(TunnelTransportProtocol::WebTransport, true).await;
    let session = open_direct_webtransport_session(tunnel.listen_addr()).await;

    let (status, response_headers, response_body) = send_direct_webtransport_request_with_headers(
        &session,
        "/v1/chat/completions",
        queue_mismatch_request_headers("req-webtransport-queue-mismatch"),
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    assert_eq!(status, StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(
        response_headers
            .get("x-stargate-retryable")
            .unwrap()
            .to_str()
            .unwrap(),
        "true"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retry-reason")
            .unwrap()
            .to_str()
            .unwrap(),
        "queue_estimate_mismatch"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retry-after-ms")
            .unwrap()
            .to_str()
            .unwrap(),
        "125"
    );
    assert!(response_headers.get("x-status").is_none());
    assert!(
        String::from_utf8(response_body)
            .unwrap()
            .contains("queue_estimate_mismatch")
    );
    assert_eq!(upstream_hits.load(Ordering::SeqCst), 0);
    assert_eq!(
        queue_tracker.tracked_request_count(),
        1,
        "WebTransport queue mismatch rejection should not leak the rejected request"
    );

    let metrics = metrics_text(&metrics);
    assert!(
            metrics.contains(
                r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="queue_estimate_mismatch",status="429"} 1"#
            ),
            "missing WebTransport queue mismatch retry metric:\n{metrics}"
        );

    session.connection.close(0u32.into(), b"test complete");
    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_strips_spoofed_retry_headers_without_upstream_signal() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

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
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-request-id", "req-bare-429".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "429"
    );
    assert!(response_headers.get("x-stargate-retryable").is_none());
    assert!(response_headers.get("x-stargate-retry-reason").is_none());
    assert!(response_headers.get("x-stargate-retry-after-ms").is_none());

    let metrics = metrics_text(&metrics);
    assert!(
            metrics.contains(
                r#"pylon_nonretryable_failures_total{inference_server_id="inst-a",reason="missing_upstream_retry_header"} 1"#
            ),
            "missing nonretryable failure metric:\n{metrics}"
        );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_marks_local_connect_failure_retryable_when_configured() {
    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        "http://127.0.0.1:0".to_string(),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.retry.local_connect_failures_retryable = true;
    config.metrics = Some(metrics.clone());

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-request-id", "req-local-connect-failure".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "503"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retryable")
            .unwrap()
            .to_str()
            .unwrap(),
        "true"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retry-reason")
            .unwrap()
            .to_str()
            .unwrap(),
        "local_connect_failure"
    );
    let response_text = read_response_text(&mut recv).await;
    assert_problem_response(
        &response_headers,
        &response_text,
        503,
        "Service Unavailable",
        "local upstream connection failed",
    );

    let metrics = metrics_text(&metrics);
    assert!(
            metrics.contains(
                r#"pylon_retryable_responses_total{inference_server_id="inst-a",reason="local_connect_failure",status="503"} 1"#
            ),
            "missing local connect failure retryable metric:\n{metrics}"
        );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_marks_local_connect_failure_nonretryable_by_default() {
    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        "http://127.0.0.1:0".to_string(),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert(
        "x-request-id",
        "req-local-connect-nonretry".parse().unwrap(),
    );
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "503"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retryable")
            .unwrap()
            .to_str()
            .unwrap(),
        "false"
    );
    assert_eq!(
        response_headers
            .get("x-stargate-retry-reason")
            .unwrap()
            .to_str()
            .unwrap(),
        "local_connect_failure"
    );
    let response_text = read_response_text(&mut recv).await;
    assert_problem_response(
        &response_headers,
        &response_text,
        503,
        "Service Unavailable",
        "local upstream connection failed",
    );

    let metrics = metrics_text(&metrics);
    assert!(
            metrics.contains(
                r#"pylon_nonretryable_failures_total{inference_server_id="inst-a",reason="local_connect_failure"} 1"#
            ),
            "missing local connect failure nonretryable metric:\n{metrics}"
        );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_emits_request_observation_for_streaming_response() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

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
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let (tx, rx) = flume::bounded(16);
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.request_observation_tx = Some(tx);

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-stream".parse().unwrap());
    headers.insert("x-request-id", "req-stream-1".parse().unwrap());
    headers.insert("x-input-tokens", "17".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let observation = tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let observation = rx.recv_async().await.unwrap();
            if observation.is_terminal() {
                break observation;
            }
        }
    })
    .await
    .unwrap();
    assert_eq!(observation.request_id, "req-stream-1");
    assert_eq!(observation.model_id, "model-stream");
    assert_eq!(observation.input_tokens, 17);
    assert_eq!(observation.output_messages, 2);
    assert_eq!(observation.output_tokens, 2);
    assert!(observation.output_tokens_explicit);
    assert!(observation.output_tokens_from_chunk_usage);
    assert_eq!(
        observation.state,
        crate::request_observer::RequestObservationState::Complete
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_emits_request_observation_for_streaming_responses() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

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
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let (tx, rx) = flume::bounded(16);
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.request_observation_tx = Some(tx);

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/responses",
        "model-responses",
        "req-responses-observed",
        br#"{"input":"hello","stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    let mut response_body = Vec::new();
    while let Some(chunk) = recv.recv_body().await.unwrap().into_body() {
        response_body.extend_from_slice(&chunk);
    }
    let response_text = String::from_utf8(response_body).unwrap();
    assert!(response_text.contains("event: response.created"));
    assert!(response_text.contains("event: response.output_text.delta"));
    assert!(response_text.contains("event: response.completed"));

    let observation = tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let observation = rx.recv_async().await.unwrap();
            if observation.is_terminal() {
                break observation;
            }
        }
    })
    .await
    .unwrap();
    assert_eq!(
        observation.endpoint,
        crate::request_observer::RequestObservationEndpoint::Responses
    );
    assert_eq!(observation.request_id, "req-responses-observed");
    assert_eq!(observation.model_id, "model-responses");
    assert_eq!(observation.input_tokens, 11);
    assert_eq!(observation.output_messages, 2);
    assert_eq!(observation.output_tokens, 2);
    assert!(observation.output_tokens_explicit);
    assert!(observation.output_tokens_from_chunk_usage);
    assert_eq!(
        observation.state,
        crate::request_observer::RequestObservationState::Complete
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_times_out_when_responses_stream_stalls_before_output() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

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
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.first_output_timeout = Duration::from_millis(10);
    config.output_chunk_timeout = Duration::from_millis(100);
    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/responses",
        "model-responses",
        "req-responses-timeout",
        br#"{"input":"hello","stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    let first_chunk = recv
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
    assert!(recv.recv_body().await.is_err());

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_feeds_chunk_usage_stats_into_stats_collector() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

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
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let stats_config = StatsCollectorConfig {
        observation_channel_capacity: 16,
        ..Default::default()
    };
    let (observation_tx, observation_rx) = request_observation_channel(&stats_config);
    let (model_stats_tx, model_stats_rx) = flume::bounded(16);
    let (stop_tx, stop_rx) = tokio::sync::watch::channel(false);
    let stats_handle = start_stats_collector(stats_config, observation_rx, model_stats_tx, stop_rx);

    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.request_observation_tx = Some(observation_tx);

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-stream".parse().unwrap());
    headers.insert("x-request-id", "req-stream-stats".parse().unwrap());
    headers.insert("x-input-tokens", "17".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let stats = tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let (model_id, stats) = model_stats_rx.recv_async().await.unwrap();
            if model_id == "model-stream"
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

    let _ = stop_tx.send(true);
    stats_handle.shutdown().await;
    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_counts_terminal_only_usage_tokens() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}],"usage":null}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":" world"}}],"usage":null}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[],"usage":{"completion_tokens":7}}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let (tx, rx) = flume::bounded(16);
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.request_observation_tx = Some(tx);

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-terminal-usage".parse().unwrap());
    headers.insert("x-request-id", "req-terminal-usage".parse().unwrap());
    headers.insert("x-input-tokens", "13".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let observation = tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let observation = rx.recv_async().await.unwrap();
            if observation.is_terminal() {
                break observation;
            }
        }
    })
    .await
    .unwrap();
    assert_eq!(observation.request_id, "req-terminal-usage");
    assert_eq!(observation.model_id, "model-terminal-usage");
    assert_eq!(observation.input_tokens, 13);
    assert_eq!(observation.output_messages, 3);
    assert_eq!(observation.output_tokens, 7);
    assert!(observation.output_tokens_explicit);
    assert!(observation.output_tokens_from_chunk_usage);
    assert_eq!(
        observation.state,
        crate::request_observer::RequestObservationState::Complete
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_uses_chunk_stats_fallback_when_progress_contract_is_absent() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}],"usage":{"completion_tokens":9}}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let (tx, rx) = flume::bounded(16);
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.request_observation_tx = Some(tx);

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-fallback",
        "req-fallback",
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let observation = tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let observation = rx.recv_async().await.unwrap();
            if observation.is_terminal() {
                break observation;
            }
        }
    })
    .await
    .unwrap();
    assert_eq!(observation.output_messages, 1);
    assert_eq!(observation.output_tokens, 9);
    assert!(observation.output_tokens_explicit);
    assert!(observation.output_tokens_from_chunk_usage);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_quality_token_threshold_uses_chunk_usage_counts() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();
    let sse_body = "\
data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"alpha beta\"}}],\"usage\":{\"completion_tokens\":12}}\n\n\
data: [DONE]\n\n";

    let app = Router::new().route(
        "/v1/chat/completions",
        post(move |_req: Request| async move {
            Response::builder()
                .header("content-type", "text/event-stream")
                .body(Body::from(sse_body))
                .unwrap()
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        output_tokens_threshold_min: Some(10),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-progress-quality",
        "req-progress-quality",
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    let mut response_body = Vec::new();
    while let Some(chunk) = recv.recv_body().await.unwrap().into_body() {
        response_body.extend_from_slice(&chunk);
    }
    let response_body = String::from_utf8(response_body).unwrap();
    assert!(response_body.contains("completion_tokens"));

    let metrics = metrics_text(&metrics);
    assert!(metrics.contains(
        r#"pylon_quality_checks_total{model="model-progress-quality",result="matched"} 1"#
    ));
    assert!(metrics.contains(
            r#"pylon_quality_threshold_matches_total{model="model-progress-quality",reason="output_tokens"} 1"#
        ));

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_emits_quality_metrics_for_repetitive_output() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"loop loop loop loop loop loop"}}],"usage":{"completion_tokens":6}}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        collect_quality_metrics: true,
        collect_quality_metrics_min_tokens: 1,
        output_repetition_1gram_threshold_min: Some(0.2),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-quality".parse().unwrap());
    headers.insert("x-request-id", "req-quality-1".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let metrics = metrics_text(&metrics);
    assert!(
        metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="matched"} 1"#)
    );
    assert!(metrics.contains(
            r#"pylon_quality_threshold_matches_total{model="model-quality",reason="repetition_1gram"} 1"#
        ));

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_scores_all_choices_in_streamed_output() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"alpha beta gamma delta"}}],"usage":{"completion_tokens":4}}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"index":1,"delta":{"content":"loop loop loop loop"}}],"usage":{"completion_tokens":8}}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        collect_quality_metrics: true,
        collect_quality_metrics_min_tokens: 1,
        output_repetition_1gram_threshold_min: Some(0.3),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-quality",
        "req-quality-multi-choice",
        br#"{"messages":[],"stream":true,"n":2}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let metrics = metrics_text(&metrics);
    assert!(
        metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="matched"} 1"#)
    );
    assert!(metrics.contains(
            r#"pylon_quality_threshold_matches_total{model="model-quality",reason="repetition_1gram"} 1"#
        ));

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_skips_quality_metrics_for_non_sse_chat_error_response() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move {
            let mut response = Response::new(Body::from(r#"{"error":"backend overloaded"}"#));
            *response.status_mut() = StatusCode::INTERNAL_SERVER_ERROR;
            response
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        output_tokens_threshold_min: Some(1),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-quality".parse().unwrap());
    headers.insert("x-request-id", "req-quality-error".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "500"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let metrics = metrics_text(&metrics);
    assert_quality_metrics_absent(&metrics);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_emits_clean_quality_metrics_once_for_clean_sse_output() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"alpha beta gamma delta"}}]}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        collect_quality_metrics: true,
        collect_quality_metrics_min_tokens: 1,
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-quality",
        "req-quality-clean",
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let metrics = metrics_text(&metrics);
    assert!(
        metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="clean"} 1"#)
    );
    assert!(
        !metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="matched"}"#)
    );
    assert!(!metrics.contains("pylon_quality_threshold_matches_total"));

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_emits_skipped_quality_metrics_for_unevaluated_streamed_output() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"alpha beta gamma"}}]}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        collect_quality_metrics: true,
        output_repetition_1gram_threshold_min: Some(0.3),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-quality",
        "req-quality-skipped",
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let metrics = metrics_text(&metrics);
    assert!(
        metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="skipped"} 1"#)
    );
    assert!(
        !metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="clean"}"#)
    );
    assert!(
        !metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="matched"}"#)
    );
    assert!(!metrics.contains("pylon_quality_threshold_matches_total"));

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_emits_skipped_quality_metrics_for_role_only_stream_with_token_threshold() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"role":"assistant"}}],"usage":{"completion_tokens":3}}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        output_tokens_threshold_min: Some(10),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-quality",
        "req-quality-role-only",
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let metrics = metrics_text(&metrics);
    assert!(
        metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="skipped"} 1"#)
    );
    assert!(
        !metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="clean"}"#)
    );
    assert!(
        !metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="matched"}"#)
    );
    assert!(!metrics.contains("pylon_quality_threshold_matches_total"));

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_emits_clean_quality_metrics_for_below_threshold_text_stream() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"alpha beta gamma"}}],"usage":{"completion_tokens":3}}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        output_tokens_threshold_min: Some(10),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-quality",
        "req-quality-token-clean",
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let metrics = metrics_text(&metrics);
    assert!(
        metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="clean"} 1"#)
    );
    assert!(
        !metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="skipped"}"#)
    );
    assert!(
        !metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="matched"}"#)
    );
    assert!(!metrics.contains("pylon_quality_threshold_matches_total"));

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_never_emits_quality_metrics_when_monitor_is_disabled() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"alpha beta gamma delta"}}]}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-quality",
        "req-quality-disabled",
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let metrics = metrics_text(&metrics);
    assert_quality_metrics_absent(&metrics);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_never_emits_quality_metrics_for_non_chat_requests() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
        "/v1/embeddings",
        post(|_req: Request| async move { Response::new(Body::from(r#"{"embedding":[1,2,3]}"#)) }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        output_tokens_threshold_min: Some(1),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/embeddings",
        "model-quality",
        "req-quality-non-chat",
        br#"{"input":"hello"}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let metrics = metrics_text(&metrics);
    assert_quality_metrics_absent(&metrics);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn embeddings_tunnel_forwards_json_without_stream() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

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
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let (tx, rx) = flume::bounded(16);
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.request_observation_tx = Some(tx);

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/embeddings",
        "model-embed",
        "req-embed-forward",
        br#"{"model":"model-embed","input":["alpha","beta"]}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    let mut response_body = Vec::new();
    while let Some(chunk) = recv.recv_body().await.unwrap().into_body() {
        response_body.extend_from_slice(&chunk);
    }
    let payload: serde_json::Value = serde_json::from_slice(&response_body).unwrap();
    assert_eq!(payload["object"], "list");
    assert_eq!(payload["data"].as_array().unwrap().len(), 2);

    let observation = tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let observation = rx.recv_async().await.unwrap();
            if observation.is_terminal() {
                break observation;
            }
        }
    })
    .await
    .unwrap();
    assert_eq!(
        observation.endpoint,
        crate::request_observer::RequestObservationEndpoint::Embeddings
    );
    assert_eq!(observation.request_id, "req-embed-forward");
    assert_eq!(observation.model_id, "model-embed");
    assert_eq!(observation.input_tokens, 11);
    assert_eq!(observation.embedding_items, 2);
    assert!(observation.embedding_items_observed);
    assert_eq!(
        observation.state,
        crate::request_observer::RequestObservationState::Complete
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn embeddings_tunnel_rejects_missing_request_id_model_or_input_tokens() {
    for (missing_header, expected_message) in [
        ("x-request-id", "missing required x-request-id header"),
        ("x-model", "missing required x-model header"),
        ("x-input-tokens", "missing required x-input-tokens header"),
    ] {
        let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let http_addr = http_listener.local_addr().unwrap();
        let hits = Arc::new(AtomicUsize::new(0));
        let hits_for_app = hits.clone();
        let app = Router::new().route(
            "/v1/embeddings",
            post(move |_req: Request| {
                let hits = hits_for_app.clone();
                async move {
                    hits.fetch_add(1, Ordering::Relaxed);
                    Response::new(Body::from(r#"{"unexpected":true}"#))
                }
            }),
        );
        tokio::spawn(async move {
            let _ = axum::serve(http_listener, app).await;
        });

        let config = QuicHttpTunnelConfig::new(
            "127.0.0.1:0".parse().unwrap(),
            format!("http://{http_addr}"),
        );
        let tunnel = start_quic_http_tunnel(config).await.unwrap();
        let tunnel_addr = tunnel.listen_addr();
        let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

        let mut headers = embeddings_tunnel_headers("req-embed-missing");
        headers.remove(missing_header);

        send_proxy_request_with_headers(
            &mut send,
            headers,
            br#"{"model":"model-embed","input":"hello"}"#,
        )
        .await;

        let response_headers = recv.recv_header().await.unwrap();
        assert_eq!(
            response_headers.get("x-status").unwrap().to_str().unwrap(),
            "400"
        );
        let mut response_body = Vec::new();
        while let Some(chunk) = recv.recv_body().await.unwrap().into_body() {
            response_body.extend_from_slice(&chunk);
        }
        let body = String::from_utf8(response_body).unwrap();
        assert!(
            body.contains(expected_message),
            "expected body to contain {expected_message:?}, got {body:?}"
        );
        assert_eq!(
            hits.load(Ordering::Relaxed),
            0,
            "upstream must not be called when {missing_header} is missing"
        );

        tunnel.shutdown().await;
    }
}

#[tokio::test]
async fn embeddings_tunnel_rejects_malformed_json_before_upstream() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();
    let hits = Arc::new(AtomicUsize::new(0));
    let hits_for_app = hits.clone();
    let app = Router::new().route(
        "/v1/embeddings",
        post(move |_req: Request| {
            let hits = hits_for_app.clone();
            async move {
                hits.fetch_add(1, Ordering::Relaxed);
                Response::new(Body::from(r#"{"unexpected":true}"#))
            }
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let (tx, rx) = flume::bounded(16);
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.request_observation_tx = Some(tx);
    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_proxy_request_with_headers(
        &mut send,
        embeddings_tunnel_headers("req-embed-bad-json"),
        br#"{"model":"model-embed","input":"unterminated"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "400"
    );
    let mut response_body = Vec::new();
    while let Some(chunk) = recv.recv_body().await.unwrap().into_body() {
        response_body.extend_from_slice(&chunk);
    }
    let body = String::from_utf8(response_body).unwrap();
    assert!(
        body.contains("request body must be valid JSON"),
        "expected invalid JSON error, got {body:?}"
    );
    assert_eq!(hits.load(Ordering::Relaxed), 0);

    let observation = tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let observation = rx.recv_async().await.unwrap();
            if observation.is_terminal() {
                break observation;
            }
        }
    })
    .await
    .unwrap();
    assert_eq!(
        observation.endpoint,
        crate::request_observer::RequestObservationEndpoint::Embeddings
    );
    assert_eq!(observation.request_id, "req-embed-bad-json");
    assert!(!observation.embedding_items_observed);
    assert_eq!(
        observation.state,
        crate::request_observer::RequestObservationState::Failed
    );

    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn http3_embeddings_tunnel_forwards_json_and_validates_required_headers() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();
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
                    .map(|value| value.as_str().to_string())
                    .unwrap_or_else(|| req.uri().path().to_string());
                let model = req
                    .headers()
                    .get("x-model")
                    .and_then(|value| value.to_str().ok())
                    .unwrap_or("missing")
                    .to_string();
                let body = axum::body::to_bytes(req.into_body(), 1024 * 1024)
                    .await
                    .unwrap();
                Json(serde_json::json!({
                    "path": path,
                    "model": model,
                    "body": String::from_utf8(body.to_vec()).unwrap(),
                    "object": "list",
                    "data": [
                        {"object": "embedding", "embedding": "AAAA", "index": 0}
                    ],
                    "usage": {"prompt_tokens": 11, "total_tokens": 11}
                }))
            }
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let (tx, rx) = flume::bounded(16);
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.tunnel_protocol = TunnelTransportProtocol::Http3;
    config.request_observation_tx = Some(tx);
    let tunnel = start_quic_http_tunnel(config).await.unwrap();

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-h3-embeddings".parse().unwrap());
    headers.insert("x-model", "model-embed".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    let (status, _response_headers, response_body) = send_direct_http3_json_request(
        tunnel.listen_addr(),
        "/v1/embeddings?encoding=base64",
        headers,
        br#"{"model":"model-embed","input":"alpha","encoding_format":"base64"}"#,
    )
    .await;

    assert_eq!(status, StatusCode::OK);
    let payload: serde_json::Value = serde_json::from_slice(&response_body).unwrap();
    assert_eq!(payload["path"], "/v1/embeddings?encoding=base64");
    assert_eq!(payload["model"], "model-embed");
    assert_eq!(
        payload["body"],
        r#"{"model":"model-embed","input":"alpha","encoding_format":"base64"}"#
    );
    assert_eq!(payload["data"][0]["embedding"], "AAAA");
    assert_eq!(hits.load(Ordering::Relaxed), 1);

    let observation = tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let observation = rx.recv_async().await.unwrap();
            if observation.is_terminal() {
                break observation;
            }
        }
    })
    .await
    .unwrap();
    assert_eq!(
        observation.endpoint,
        crate::request_observer::RequestObservationEndpoint::Embeddings
    );
    assert_eq!(observation.request_id, "req-h3-embeddings");
    assert_eq!(observation.embedding_items, 1);
    assert!(observation.embedding_items_observed);
    assert_eq!(
        observation.state,
        crate::request_observer::RequestObservationState::Complete
    );

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-h3-embeddings-missing".parse().unwrap());
    headers.insert("x-model", "model-embed".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    let (status, _response_headers, response_body) = send_direct_http3_json_request(
        tunnel.listen_addr(),
        "/v1/embeddings",
        headers,
        br#"{"model":"model-embed","input":"alpha"}"#,
    )
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST);
    let body = String::from_utf8(response_body).unwrap();
    assert!(
        body.contains("missing required x-input-tokens header"),
        "expected missing input-token error, got {body:?}"
    );
    assert_eq!(hits.load(Ordering::Relaxed), 1);

    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn webtransport_embeddings_tunnel_forwards_json_and_validates_required_headers() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();
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
                    .map(|value| value.as_str().to_string())
                    .unwrap_or_else(|| req.uri().path().to_string());
                let model = req
                    .headers()
                    .get("x-model")
                    .and_then(|value| value.to_str().ok())
                    .unwrap_or("missing")
                    .to_string();
                let body = axum::body::to_bytes(req.into_body(), 1024 * 1024)
                    .await
                    .unwrap();
                Json(serde_json::json!({
                    "path": path,
                    "model": model,
                    "body": String::from_utf8(body.to_vec()).unwrap(),
                    "object": "list",
                    "data": [
                        {"object": "embedding", "embedding": [0.1, 0.2], "index": 0},
                        {"object": "embedding", "embedding": [0.3, 0.4], "index": 1}
                    ],
                    "usage": {"prompt_tokens": 11, "total_tokens": 11}
                }))
            }
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let (tx, rx) = flume::bounded(16);
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.tunnel_protocol = TunnelTransportProtocol::WebTransport;
    config.request_observation_tx = Some(tx);
    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let session = open_direct_webtransport_session(tunnel.listen_addr()).await;

    let (status, _response_headers, response_body) = send_direct_webtransport_json_request(
        &session,
        "/v1/embeddings?source=webtransport",
        "model-embed",
        "req-wt-embeddings",
        br#"{"model":"model-embed","input":["alpha","beta"]}"#,
    )
    .await;

    assert_eq!(status, StatusCode::OK);
    let payload: serde_json::Value = serde_json::from_slice(&response_body).unwrap();
    assert_eq!(payload["path"], "/v1/embeddings?source=webtransport");
    assert_eq!(payload["model"], "model-embed");
    assert_eq!(
        payload["body"],
        r#"{"model":"model-embed","input":["alpha","beta"]}"#
    );
    assert_eq!(payload["data"].as_array().unwrap().len(), 2);
    assert_eq!(hits.load(Ordering::Relaxed), 1);

    let observation = tokio::time::timeout(Duration::from_secs(1), async {
        loop {
            let observation = rx.recv_async().await.unwrap();
            if observation.is_terminal() {
                break observation;
            }
        }
    })
    .await
    .unwrap();
    assert_eq!(
        observation.endpoint,
        crate::request_observer::RequestObservationEndpoint::Embeddings
    );
    assert_eq!(observation.request_id, "req-wt-embeddings");
    assert_eq!(observation.embedding_items, 2);
    assert!(observation.embedding_items_observed);
    assert_eq!(
        observation.state,
        crate::request_observer::RequestObservationState::Complete
    );

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-wt-embeddings-missing".parse().unwrap());
    headers.insert("x-model", "model-embed".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    let (status, _response_headers, response_body) = send_direct_webtransport_request_with_headers(
        &session,
        "/v1/embeddings",
        headers,
        br#"{"model":"model-embed","input":"alpha"}"#,
    )
    .await;

    assert_eq!(status, StatusCode::BAD_REQUEST);
    let body = String::from_utf8(response_body).unwrap();
    assert!(
        body.contains("missing required x-input-tokens header"),
        "expected missing input-token error, got {body:?}"
    );
    assert_eq!(hits.load(Ordering::Relaxed), 1);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_skips_quality_metrics_when_stream_times_out_before_output() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move {
            axum::response::Sse::new(async_stream::stream! {
                tokio::time::sleep(Duration::from_millis(50)).await;
                yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
            })
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.first_output_timeout = Duration::from_millis(10);
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        output_tokens_threshold_min: Some(1),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-quality",
        "req-quality-timeout",
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    assert!(recv.recv_body().await.is_err());

    let metrics = metrics_text(&metrics);
    assert_quality_metrics_absent(&metrics);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_skips_quality_metrics_when_stream_ends_before_output() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move {
            axum::response::Sse::new(async_stream::stream! {
                yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
            })
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        output_tokens_threshold_min: Some(1),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-quality",
        "req-quality-eof",
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    if let Ok(response_headers) = recv.recv_header().await {
        assert_eq!(
            response_headers.get("x-status").unwrap().to_str().unwrap(),
            "200"
        );
        let _ = recv.recv_body().await;
    }

    let metrics = metrics_text(&metrics);
    assert_quality_metrics_absent(&metrics);

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_records_one_quality_check_for_multi_chunk_stream() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                axum::response::Sse::new(async_stream::stream! {
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"alpha"}}]}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":" beta"}}]}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(
                        Event::default().data(r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":" gamma"}}]}"#)
                    );
                    yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
                })
            }),
        );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let metrics = PylonMetrics::new().unwrap();
    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.inference_server_id = Some("inst-a".to_string());
    config.metrics = Some(metrics.clone());
    config.request_quality_monitor = crate::request_quality_monitor::RequestQualityMonitorConfig {
        output_tokens_threshold_min: Some(2),
        ..crate::request_quality_monitor::RequestQualityMonitorConfig::default()
    };

    let tunnel = start_quic_http_tunnel(config).await.unwrap();
    let tunnel_addr = tunnel.listen_addr();
    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;

    send_json_proxy_request(
        &mut send,
        "/v1/chat/completions",
        "model-quality",
        "req-quality-multi-chunk",
        br#"{"messages":[],"stream":true}"#,
    )
    .await;

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    while recv.recv_body().await.unwrap().into_body().is_some() {}

    let metrics = metrics_text(&metrics);
    assert!(
        metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="matched"} 1"#)
    );
    assert!(
        !metrics.contains(r#"pylon_quality_checks_total{model="model-quality",result="clean"}"#)
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_rejects_missing_request_id() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move { Response::new(Body::from("{\"ok\":true}")) }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    ))
    .await
    .unwrap();

    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[]}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "400"
    );
    let response_text = read_response_text(&mut recv).await;
    assert_problem_response(
        &response_headers,
        &response_text,
        400,
        "Bad Request",
        "missing required x-request-id header",
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_rejects_non_streaming_request_body() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move { Response::new(Body::from("{\"ok\":true}")) }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    ))
    .await
    .unwrap();

    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-request-id", "req-non-stream-1".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":false}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "400"
    );
    let response_text = read_response_text(&mut recv).await;
    assert_problem_response(
        &response_headers,
        &response_text,
        400,
        "Bad Request",
        "/v1/chat/completions requests must set stream=true",
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_rejects_non_streaming_responses_request_body() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();
    let upstream_hits = Arc::new(std::sync::atomic::AtomicUsize::new(0));

    let app_hits = Arc::clone(&upstream_hits);
    let app = Router::new().route(
        "/v1/responses",
        post(move |_req: Request| {
            let app_hits = Arc::clone(&app_hits);
            async move {
                app_hits.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
                Response::new(Body::from("{\"ok\":true}"))
            }
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    ))
    .await
    .unwrap();

    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/responses".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-request-id", "req-non-stream-responses".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"input":"hello","stream":false}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "400"
    );

    let response_text = read_response_text(&mut recv).await;
    assert_problem_response(
        &response_headers,
        &response_text,
        400,
        "Bad Request",
        "/v1/responses requests must set stream=true",
    );
    assert_eq!(
        upstream_hits.load(std::sync::atomic::Ordering::SeqCst),
        0,
        "non-streaming responses requests should not reach upstream"
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_rejects_missing_required_headers_for_responses() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();
    let upstream_hits = Arc::new(std::sync::atomic::AtomicUsize::new(0));

    let app_hits = Arc::clone(&upstream_hits);
    let app = Router::new().route(
        "/v1/responses",
        post(move |_req: Request| {
            let app_hits = Arc::clone(&app_hits);
            async move {
                app_hits.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
                Response::new(Body::from("{\"ok\":true}"))
            }
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    ))
    .await
    .unwrap();

    let tunnel_addr = tunnel.listen_addr();
    let required_headers = [
        ("x-request-id", "req-responses-required"),
        ("x-model", "model-a"),
        ("x-input-tokens", "11"),
    ];

    for (missing_header, expected_body_fragment) in [
        ("x-request-id", "x-request-id"),
        ("x-model", "x-model"),
        ("x-input-tokens", "x-input-tokens"),
    ] {
        let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;
        let mut headers = HeaderMap::new();
        headers.insert("x-method", "POST".parse().unwrap());
        headers.insert("x-path", "/v1/responses".parse().unwrap());
        headers.insert("x-routing-key", "rk-1".parse().unwrap());
        headers.insert("content-type", "application/json".parse().unwrap());
        for (name, value) in required_headers {
            if name != missing_header {
                headers.insert(name, value.parse().unwrap());
            }
        }
        send.send_header(headers).await.unwrap();
        send.send_body(Bytes::from_static(br#"{"input":"hello","stream":true}"#))
            .await
            .unwrap();
        send.finish().unwrap();

        let response_headers = recv.recv_header().await.unwrap();
        assert_eq!(
            response_headers.get("x-status").unwrap().to_str().unwrap(),
            "400",
            "missing {missing_header} should be rejected"
        );
        let response_text = read_response_text(&mut recv).await;
        assert_problem_response(
            &response_headers,
            &response_text,
            400,
            "Bad Request",
            &format!("missing required {expected_body_fragment} header"),
        );
    }

    let (_endpoint, mut send, mut recv) = open_test_tunnel_stream(tunnel_addr).await;
    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/responses".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-request-id", "req-invalid-input-tokens".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-input-tokens", "not-a-count".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"input":"hello","stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "400"
    );
    let response_text = read_response_text(&mut recv).await;
    assert_problem_response(
        &response_headers,
        &response_text,
        400,
        "Bad Request",
        "invalid x-input-tokens header",
    );

    assert_eq!(
        upstream_hits.load(std::sync::atomic::Ordering::SeqCst),
        0,
        "requests missing required headers should not reach upstream"
    );

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_times_out_when_no_output_event_arrives() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

    let app = Router::new().route(
        "/v1/chat/completions",
        post(|_req: Request| async move {
            axum::response::Sse::new(async_stream::stream! {
                tokio::time::sleep(Duration::from_millis(50)).await;
                yield Ok::<_, std::convert::Infallible>(Event::default().data("[DONE]"));
            })
        }),
    );
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.first_output_timeout = Duration::from_millis(10);
    let tunnel = start_quic_http_tunnel(config).await.unwrap();

    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-request-id", "req-timeout-1".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );
    assert!(recv.recv_body().await.is_err());

    tunnel.shutdown().await;
}

#[tokio::test]
async fn quic_tunnel_times_out_when_subsequent_output_event_arrives_too_late() {
    let http_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let http_addr = http_listener.local_addr().unwrap();

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
    tokio::spawn(async move {
        let _ = axum::serve(http_listener, app).await;
    });

    let mut config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{http_addr}"),
    );
    config.first_output_timeout = Duration::from_millis(100);
    config.output_chunk_timeout = Duration::from_millis(10);
    let tunnel = start_quic_http_tunnel(config).await.unwrap();

    let tunnel_addr = tunnel.listen_addr();

    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).unwrap();
    endpoint.set_default_client_config(trusted_client_config().unwrap());
    let connection = endpoint
        .connect(tunnel_addr, "stargate")
        .unwrap()
        .await
        .unwrap();

    let (quinn_send, quinn_recv) = connection.open_bi().await.unwrap();
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = HeaderMap::new();
    headers.insert("x-method", "POST".parse().unwrap());
    headers.insert("x-path", "/v1/chat/completions".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-request-id", "req-timeout-2".parse().unwrap());
    headers.insert("x-input-tokens", "11".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    send.send_header(headers).await.unwrap();
    send.send_body(Bytes::from_static(br#"{"messages":[],"stream":true}"#))
        .await
        .unwrap();
    send.finish().unwrap();

    let response_headers = recv.recv_header().await.unwrap();
    assert_eq!(
        response_headers.get("x-status").unwrap().to_str().unwrap(),
        "200"
    );

    let first_chunk = recv
        .recv_body()
        .await
        .unwrap()
        .into_body()
        .expect("first chunk");
    let first_text = std::str::from_utf8(&first_chunk).unwrap();
    assert!(first_text.contains("first"));

    let next_chunk = recv.recv_body().await;
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
fn reverse_quic_connect_debug_target_records_sni_alpn_and_tls_mode() {
    let mut config = ReverseQuicTunnelConfig::new(
        "stargate-quic-lb.stargate.svc.cluster.local:50072".to_string(),
        "backend-a".to_string(),
        "http://127.0.0.1:8000".to_string(),
    );
    config.sni_override =
        Some("stargate-0.stargate-headless.stargate.svc.cluster.local".to_string());
    config.quic_insecure = true;
    config.tunnel_protocol = TunnelTransportProtocol::Http3;

    let target = reverse_quic_connect_debug_target(&config);

    assert_eq!(target.target_addr, config.target_addr);
    assert_eq!(
        target.sni,
        "stargate-0.stargate-headless.stargate.svc.cluster.local"
    );
    assert_eq!(target.tunnel_protocol, TunnelTransportProtocol::Http3);
    assert_eq!(target.alpn_protocols, vec!["h3".to_string()]);
    assert!(target.quic_insecure);
}

#[test]
fn reverse_quic_resolved_target_prefers_ipv4_addr() {
    let ipv6_addr: SocketAddr = "[fd00::1]:50072".parse().unwrap();
    let ipv4_addr: SocketAddr = "10.0.0.4:50072".parse().unwrap();

    let target = reverse_quic_resolved_target(vec![ipv6_addr, ipv4_addr]).unwrap();

    assert_eq!(target.resolved_addrs, vec![ipv6_addr, ipv4_addr]);
    assert_eq!(target.resolved_target, ipv4_addr);
}

#[test]
fn reverse_quic_resolved_target_falls_back_to_first_resolved_addr() {
    let ipv6_addr: SocketAddr = "[fd00::1]:50072".parse().unwrap();

    let target = reverse_quic_resolved_target(vec![ipv6_addr]).unwrap();

    assert_eq!(target.resolved_addrs, vec![ipv6_addr]);
    assert_eq!(target.resolved_target, ipv6_addr);
}

#[tokio::test]
async fn reverse_quic_endpoint_connect_logs_attempt_resolution_and_connection_metadata() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let server_config = make_server_config(
        &ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::Custom,
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
    let attempt_event = events
        .iter()
        .find(|event| {
            event.get("message").map(String::as_str)
                == Some("attempting Stargate reverse QUIC connection")
        })
        .expect("connect attempt event should be recorded");
    assert_eq!(
        attempt_event.get("target_addr").map(String::as_str),
        Some(server_addr.as_str())
    );
    let resolved_event = events
        .iter()
        .find(|event| {
            event.get("message").map(String::as_str)
                == Some("resolved Stargate reverse QUIC target")
        })
        .expect("resolved target event should be recorded");
    assert_eq!(
        resolved_event.get("resolved_target").map(String::as_str),
        Some(server_addr.as_str())
    );
    let connected_event = events
        .iter()
        .find(|event| {
            event.get("message").map(String::as_str)
                == Some("Stargate reverse QUIC connection established")
        })
        .expect("connected event should be recorded");
    assert_eq!(
        connected_event.get("transport").map(String::as_str),
        Some("quic")
    );
    assert_eq!(
        connected_event.get("target_addr").map(String::as_str),
        Some(server_addr.as_str())
    );
    assert_eq!(
        connected_event.get("resolved_target").map(String::as_str),
        Some(server_addr.as_str())
    );
    assert_eq!(
        connected_event.get("remote_addr").map(String::as_str),
        Some(server_addr.as_str())
    );
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
fn observer_guard_calls_fail_on_drop() {
    use crate::request_observer::{RequestObservation, RequestObserver};

    let mut headers = reqwest::header::HeaderMap::new();
    headers.insert("x-request-id", "req-guard".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-input-tokens", "10".parse().unwrap());
    let (tx, rx) = flume::bounded::<RequestObservation>(8);
    let observer = RequestObserver::new(&headers, Some(tx)).unwrap();
    {
        let _guard = ObserverGuard(observer);
    }
    let mut last = None;
    while let Ok(obs) = rx.try_recv() {
        last = Some(obs);
    }
    let last = last.expect("expected at least one observation from guard drop");
    assert_eq!(
        last.state,
        crate::request_observer::RequestObservationState::Failed
    );
}

#[test]
fn observer_guard_noop_when_already_terminal() {
    use crate::request_observer::{RequestObservation, RequestObserver};

    let mut headers = reqwest::header::HeaderMap::new();
    headers.insert("x-request-id", "req-noop".parse().unwrap());
    headers.insert("x-routing-key", "rk-1".parse().unwrap());
    headers.insert("x-model", "model-a".parse().unwrap());
    headers.insert("x-input-tokens", "10".parse().unwrap());
    let (tx, rx) = flume::bounded::<RequestObservation>(16);
    let mut observer = RequestObserver::new(&headers, Some(tx)).unwrap();
    observer.on_upstream_response_headers(&reqwest::header::HeaderMap::new(), 200);
    observer.observe_output_message();
    observer.finish();
    assert!(observer.is_terminal());

    let pre_drop_count = rx.len();
    {
        let _guard = ObserverGuard(observer);
    }
    assert_eq!(
        rx.len(),
        pre_drop_count,
        "guard should not emit extra observations when already terminal"
    );
}

#[test]
fn build_trusted_client_config_insecure_succeeds_without_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let result = build_trusted_client_config(None, true, TunnelTransportProtocol::Custom);
    assert!(result.is_ok());
}

#[test]
fn build_trusted_client_config_secure_fails_without_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let result = build_trusted_client_config(None, false, TunnelTransportProtocol::Custom);
    assert!(result.is_err());
}

#[test]
fn build_trusted_client_config_secure_succeeds_with_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (cert_pem, _key_pem) = stargate_tls::generate_self_signed_cert().unwrap();
    let result =
        build_trusted_client_config(Some(&cert_pem), false, TunnelTransportProtocol::Custom);
    assert!(result.is_ok());
}

#[test]
fn make_server_config_self_signed_when_none() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let result = make_server_config(
        &ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::Custom,
    );
    assert!(result.is_ok());
}

#[test]
fn make_server_config_uses_provided_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (cert_pem, key_pem) = stargate_tls::generate_self_signed_cert().unwrap();
    let result = make_server_config(
        &ServerTlsIdentity::Provided { cert_pem, key_pem },
        TunnelTransportProtocol::Custom,
    );
    assert!(result.is_ok());
}

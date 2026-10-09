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

//! Proxy-level coverage for routing reservations created per dispatch attempt.
//!
//! Requests run through `proxy_openai_request` against a minimal raw QUIC
//! tunnel that plays the Pylon side, so tests observe the
//! `x-stargate-expected-queue-ms` header exactly as Stargate sent it and can
//! return arbitrary tunnel retry metadata.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use axum::body::Body;
use axum::extract::Request;
use axum::http::{HeaderMap, HeaderName, HeaderValue, Method, StatusCode};
use parking_lot::Mutex;
use stargate_proto::pb::{
    InferenceServerModelRegistration, InferenceServerRegistration, InferenceServerStatus,
    ModelStats,
};
use stargate_protocol::tunnel_contract::{
    HEADER_INFERENCE_SERVER_ID, HEADER_INPUT_TOKENS, HEADER_MODEL, HEADER_REQUEST_ID,
    HEADER_STARGATE_EXPECTED_QUEUE_MS, HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE,
};
use stargate_protocol::{RecvStream, SendStream, TunnelTransportProtocol};

use super::test_support::test_proxy_app_state;
use super::{OpenAiProxyEndpoint, ProxyAppState, proxy_openai_request};
use crate::auth::OpenAuthenticator;
use crate::routing_state::{RegistrationIdentity, RoutingTargetKey, RunningRegistration};
use crate::tunnel::{QuicHttpProxy, QuicTunnelConfig};

const MODEL: &str = "model-reservation";
const CLUSTER: &str = "cluster-reservation";
const INPUT_TOKENS: u64 = 37;
const BASE_QUEUE_MS: u64 = 5;
const LAST_MEAN_INPUT_TPS: f64 = 100.0;
/// Queue time one reservation adds: 37 tokens at 100 tokens/s.
const RESERVATION_QUEUE_MS: u64 = 370;
/// Long enough that no reservation can expire while a test runs.
const UNEXPIRING_TTL: Duration = Duration::from_secs(600);

#[derive(Clone, Copy)]
enum MockReply {
    Respond {
        status: u16,
        headers: &'static [(&'static str, &'static str)],
    },
    ResetStream,
}

const OK_REPLY: MockReply = MockReply::Respond {
    status: 200,
    headers: &[],
};

/// Raw QUIC tunnel endpoint standing in for Pylon. It records every request
/// header block Stargate sends and answers with a fixed reply.
struct MockTunnel {
    endpoint: quinn::Endpoint,
    received: Arc<Mutex<Vec<HeaderMap>>>,
}

impl MockTunnel {
    fn start(reply: MockReply) -> Self {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let server_config = stargate_tls::build_quic_server_config(
            &stargate_tls::ServerTlsIdentity::SelfSigned,
            TunnelTransportProtocol::RawQuic.alpn_protocols(),
        )
        .expect("mock tunnel server config should build");
        let endpoint = quinn::Endpoint::server(server_config, "127.0.0.1:0".parse().unwrap())
            .expect("mock tunnel endpoint should bind");
        let received = Arc::new(Mutex::new(Vec::new()));
        let accept_endpoint = endpoint.clone();
        let accept_received = Arc::clone(&received);
        tokio::spawn(async move {
            while let Some(incoming) = accept_endpoint.accept().await {
                let received = Arc::clone(&accept_received);
                tokio::spawn(async move {
                    let Ok(connection) = incoming.await else {
                        return;
                    };
                    while let Ok((send, recv)) = connection.accept_bi().await {
                        tokio::spawn(serve_stream(send, recv, reply, Arc::clone(&received)));
                    }
                });
            }
        });
        Self { endpoint, received }
    }

    fn url(&self) -> String {
        format!(
            "quic://{}",
            self.endpoint
                .local_addr()
                .expect("mock tunnel should have a local address")
        )
    }

    fn request_count(&self) -> usize {
        self.received.lock().len()
    }

    fn expected_queue_ms_headers(&self) -> Vec<Option<u64>> {
        self.received
            .lock()
            .iter()
            .map(|headers| {
                headers
                    .get(HEADER_STARGATE_EXPECTED_QUEUE_MS)
                    .and_then(|value| value.to_str().ok())
                    .and_then(|value| value.parse().ok())
            })
            .collect()
    }
}

impl Drop for MockTunnel {
    fn drop(&mut self) {
        self.endpoint.close(0u32.into(), b"test complete");
    }
}

async fn serve_stream(
    mut quinn_send: quinn::SendStream,
    quinn_recv: quinn::RecvStream,
    reply: MockReply,
    received: Arc<Mutex<Vec<HeaderMap>>>,
) {
    let mut recv = RecvStream::new(quinn_recv);
    let Ok(request_headers) = recv.recv_header().await else {
        return;
    };
    received.lock().push(request_headers);
    let (status, extra_headers) = match reply {
        MockReply::ResetStream => {
            let _ = quinn_send.reset(0u32.into());
            return;
        }
        MockReply::Respond { status, headers } => (status, headers),
    };
    while let Ok(frame) = recv.recv_body().await {
        if frame.into_body().is_none() {
            break;
        }
    }
    let mut response_headers = HeaderMap::new();
    response_headers.insert(
        HeaderName::from_static("x-status"),
        HeaderValue::from(status),
    );
    for (name, value) in extra_headers {
        response_headers.insert(
            HeaderName::from_static(name),
            HeaderValue::from_static(value),
        );
    }
    let mut send = SendStream::new(quinn_send);
    if send.send_header(response_headers).await.is_ok()
        && send
            .send_body(bytes::Bytes::from_static(b"{}"))
            .await
            .is_ok()
    {
        let _ = send.finish();
    }
}

fn proxy_app(ttl_min: Duration, ttl_max: Duration) -> ProxyAppState {
    let mut app = test_proxy_app_state();
    // The shared fixture uses 10 ms tunnel timeouts because it never dials a
    // backend; these tests complete real QUIC requests.
    app.quic_proxy = Arc::new(
        QuicHttpProxy::new(
            QuicTunnelConfig {
                connect_timeout: Duration::from_secs(5),
                request_timeout: Duration::from_secs(5),
                direct_quic_connections: 1,
                tls_cert_pem: None,
                server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
                server_identity_reloader: None,
                tls_reload_interval: stargate_tls::DEFAULT_TLS_RELOAD_INTERVAL,
                quic_insecure: true,
                tunnel_protocol: TunnelTransportProtocol::RawQuic,
            },
            Arc::new(OpenAuthenticator),
        )
        .expect("quic proxy should initialize"),
    );
    app.routing_reservation_ttl_min = ttl_min;
    app.routing_reservation_ttl_max = ttl_max;
    app
}

fn target() -> RoutingTargetKey {
    RoutingTargetKey::new(None, MODEL)
}

/// Registers one active direct backend in the shared test cluster.
async fn register_backend(
    app: &ProxyAppState,
    inference_server_id: &str,
    tunnel: &MockTunnel,
    rtt: Duration,
) -> RunningRegistration {
    let running = app
        .state
        .begin_registration(&RegistrationIdentity {
            inference_server_id: inference_server_id.to_string(),
            cluster_id: CLUSTER.to_string(),
            inference_server_url: tunnel.url(),
            routing_key: None,
            reverse_tunnel: false,
        })
        .expect("registration should begin");
    let update = InferenceServerRegistration {
        models: HashMap::from([(
            MODEL.to_string(),
            InferenceServerModelRegistration {
                status: InferenceServerStatus::Active.into(),
                stats: Some(ModelStats {
                    last_mean_input_tps: LAST_MEAN_INPUT_TPS,
                    queue_time_estimate_ms_by_priority: HashMap::from([(0, BASE_QUEUE_MS)]),
                    ..Default::default()
                }),
            },
        )]),
        ..Default::default()
    };
    app.state
        .apply_registration_update(&running, &update, false, Some(rtt))
        .await;
    running
}

async fn send_request(app: &ProxyAppState, request_id: &str) -> Result<HeaderMap, StatusCode> {
    let request = Request::builder()
        .method(Method::POST)
        .uri(OpenAiProxyEndpoint::CHAT_COMPLETIONS.path)
        .header(HEADER_REQUEST_ID, request_id)
        .header(HEADER_MODEL, MODEL)
        .header(HEADER_INPUT_TOKENS, INPUT_TOKENS.to_string())
        .body(Body::from("{}"))
        .expect("request should build");
    proxy_openai_request(app.clone(), request, OpenAiProxyEndpoint::CHAT_COMPLETIONS)
        .await
        .map(|response| response.headers().clone())
}

/// Reservations currently counted in the routed cluster snapshot. Base stats
/// report an empty queue, so every queued entry is a live reservation.
async fn counted_reservations(app: &ProxyAppState) -> u64 {
    let snapshot = app
        .state
        .routing_target_snapshot(&target())
        .await
        .expect("target should stay routable");
    let clusters = snapshot.clusters();
    assert_eq!(clusters.len(), 1);
    clusters[0].stats.queue_size
}

fn observed_ttl_sum_ms(app: &ProxyAppState) -> u64 {
    let sum_seconds = app
        .metrics
        .routing_reservation_ttl_seconds(None, MODEL)
        .get_sample_sum();
    (sum_seconds * 1000.0).round() as u64
}

#[tokio::test]
async fn reservation_ttl_uses_the_dispatched_backend_rtt_not_the_cluster_mean() {
    let app = proxy_app(Duration::from_millis(1), Duration::from_millis(1000));
    let fast_tunnel = MockTunnel::start(OK_REPLY);
    let slow_tunnel = MockTunnel::start(OK_REPLY);
    let rtts = HashMap::from([
        ("inst-fast", Duration::from_millis(10)),
        ("inst-slow", Duration::from_millis(100)),
    ]);
    let fast = register_backend(&app, "inst-fast", &fast_tunnel, rtts["inst-fast"]).await;
    let slow = register_backend(&app, "inst-slow", &slow_tunnel, rtts["inst-slow"]).await;
    let cluster_rtt = app
        .state
        .routing_target_snapshot(&target())
        .await
        .expect("target should be routable")
        .clusters()[0]
        .rtt;
    assert_eq!(
        cluster_rtt,
        Duration::from_millis(55),
        "the cluster mean must differ from both backend RTTs"
    );

    // Round robin dispatches one request to each backend.
    let mut dispatched = Vec::new();
    for request_id in ["req-ttl-1", "req-ttl-2"] {
        let ttl_sum_before_ms = observed_ttl_sum_ms(&app);
        let headers = send_request(&app, request_id)
            .await
            .expect("request should be proxied");
        let backend = headers[HEADER_INFERENCE_SERVER_ID]
            .to_str()
            .expect("backend id should be ASCII")
            .to_string();
        let expected_ttl = rtts[backend.as_str()];
        assert_eq!(
            observed_ttl_sum_ms(&app) - ttl_sum_before_ms,
            expected_ttl.as_millis() as u64,
            "reservation for {backend} must use its own RTT, not the {cluster_rtt:?} cluster mean"
        );
        dispatched.push(backend);
    }
    dispatched.sort();
    assert_eq!(dispatched, ["inst-fast", "inst-slow"]);
    assert_eq!(
        app.metrics
            .routing_reservation_ttl_seconds(None, MODEL)
            .get_sample_count(),
        2
    );
    assert_eq!(fast_tunnel.request_count(), 1);
    assert_eq!(slow_tunnel.request_count(), 1);

    app.state.end_registration(fast).await;
    app.state.end_registration(slow).await;
}

#[tokio::test]
async fn unexpired_reservation_raises_expected_queue_header_for_later_requests() {
    let app = proxy_app(UNEXPIRING_TTL, UNEXPIRING_TTL);
    let tunnel = MockTunnel::start(OK_REPLY);
    let running = register_backend(&app, "inst-a", &tunnel, Duration::from_millis(10)).await;

    for request_id in ["req-queue-1", "req-queue-2", "req-queue-3"] {
        send_request(&app, request_id)
            .await
            .expect("request should be proxied");
    }

    assert_eq!(
        tunnel.expected_queue_ms_headers(),
        [
            Some(BASE_QUEUE_MS),
            Some(BASE_QUEUE_MS + RESERVATION_QUEUE_MS),
            Some(BASE_QUEUE_MS + 2 * RESERVATION_QUEUE_MS),
        ],
        "each completed dispatch stays reserved until its TTL"
    );
    assert_eq!(counted_reservations(&app).await, 3);

    app.state.end_registration(running).await;
}

#[tokio::test]
async fn expired_reservation_is_excluded_from_expected_queue_header() {
    // A zero TTL sets the expiry to the dispatch instant. `Instant` is
    // monotonic, so every later snapshot read is at or past the expiry
    // boundary without sleeping or controlling the clock.
    let app = proxy_app(Duration::ZERO, Duration::ZERO);
    let tunnel = MockTunnel::start(OK_REPLY);
    let running = register_backend(&app, "inst-a", &tunnel, Duration::from_millis(10)).await;

    for request_id in ["req-expired-1", "req-expired-2"] {
        send_request(&app, request_id)
            .await
            .expect("request should be proxied");
    }

    assert_eq!(
        tunnel.expected_queue_ms_headers(),
        [Some(BASE_QUEUE_MS), Some(BASE_QUEUE_MS)],
        "an expired reservation must not inflate the next estimate"
    );
    assert_eq!(counted_reservations(&app).await, 0);
    assert_eq!(
        app.metrics
            .routing_reservation_ttl_seconds(None, MODEL)
            .get_sample_count(),
        2,
        "both dispatches must have reserved before expiring"
    );

    app.state.end_registration(running).await;
}

async fn assert_reply_keeps_reservations(reply: MockReply, case: &str) {
    let app = proxy_app(UNEXPIRING_TTL, UNEXPIRING_TTL);
    let tunnel = MockTunnel::start(reply);
    let running = register_backend(&app, "inst-a", &tunnel, Duration::from_millis(10)).await;

    let _ = send_request(&app, "req-failure").await;

    let attempts = tunnel.request_count();
    assert!(
        attempts >= 1,
        "{case}: the backend should receive a dispatch"
    );
    assert_eq!(
        counted_reservations(&app).await,
        attempts as u64,
        "{case}: every failed attempt must stay reserved until its TTL"
    );
    let _ = send_request(&app, "req-follow-up").await;
    assert_eq!(
        tunnel.expected_queue_ms_headers().get(attempts).copied(),
        Some(Some(BASE_QUEUE_MS + attempts as u64 * RESERVATION_QUEUE_MS)),
        "{case}: the next request must still see every unreleased reservation"
    );

    app.state.end_registration(running).await;
}

#[tokio::test]
async fn non_queue_mismatch_failures_keep_reservation_until_ttl() {
    for (reply, case) in [
        (
            MockReply::Respond {
                status: 503,
                headers: &[],
            },
            "503 without retry metadata",
        ),
        (
            MockReply::Respond {
                status: 503,
                headers: &[
                    (HEADER_STARGATE_RETRYABLE, "true"),
                    (HEADER_STARGATE_RETRY_REASON, "upstream_admission_rejected"),
                ],
            },
            "retryable 503",
        ),
        (
            MockReply::Respond {
                status: 429,
                headers: &[],
            },
            "429 without retry metadata",
        ),
        (
            MockReply::Respond {
                status: 429,
                headers: &[
                    (HEADER_STARGATE_RETRYABLE, "true"),
                    (HEADER_STARGATE_RETRY_REASON, "upstream_admission_rejected"),
                ],
            },
            "retryable 429 with another reason",
        ),
        (MockReply::ResetStream, "transport failure"),
    ] {
        assert_reply_keeps_reservations(reply, case).await;
    }
}

#[tokio::test]
async fn queue_mismatch_rejection_releases_reservation_before_ttl() {
    let app = proxy_app(UNEXPIRING_TTL, UNEXPIRING_TTL);
    let tunnel = MockTunnel::start(MockReply::Respond {
        status: 429,
        headers: &[
            (HEADER_STARGATE_RETRYABLE, "true"),
            (HEADER_STARGATE_RETRY_REASON, "queue_estimate_mismatch"),
        ],
    });
    let running = register_backend(&app, "inst-a", &tunnel, Duration::from_millis(10)).await;

    for request_id in ["req-mismatch-1", "req-mismatch-2"] {
        let _ = send_request(&app, request_id).await;
    }

    assert_eq!(counted_reservations(&app).await, 0);
    assert_eq!(
        tunnel.expected_queue_ms_headers(),
        [Some(BASE_QUEUE_MS), Some(BASE_QUEUE_MS)],
        "a released reservation must not inflate the next estimate"
    );

    app.state.end_registration(running).await;
}

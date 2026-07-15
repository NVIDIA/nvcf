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

use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use crate::common::sse::{assert_sse_done, json_events, parse_sse_events};
use crate::common::{
    DummyState, SelfDiscovery, TunnelTestCase, base_config, bind_ephemeral,
    direct_registration_config, dummy_chat, init_crypto, make_stargate_runtime,
    make_stargate_runtime_for_tunnel_case, make_stargate_runtime_with_lb,
    reverse_registration_config, start_dummy_inst, wait_for_routing,
    wait_for_routing_with_cache_affinity, wait_until, with_proxy_headers,
};
use axum::Router;
use axum::body::{Body, Bytes};
use axum::extract::{Request, State};
use axum::http::{HeaderName, HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use prometheus::{Encoder, TextEncoder};
use pylon_lib::{
    CurrentModelStats, InferenceServerRegistrationClient, InferenceServerRegistrationConfig,
    PylonRuntimeState, QuicHttpTunnelConfig, QuicHttpTunnelHandle, RequestObservation,
    RequestObservationEndpoint, RequestObservationState, TunnelTransportProtocol,
    start_quic_http_tunnel,
};
use stargate::proxy::ProxyRetryConfig;
use stargate::routing::RoutingTargetKey;
use stargate::runtime::{BoundStargateListeners, StargateRuntime};
use stargate_proto::pb::InferenceServerStatus;
use tokio::net::TcpListener;

#[derive(Clone, Debug)]
struct EmbeddingsBackendCapture {
    path_and_query: String,
    body: Bytes,
    model_header: Option<String>,
    request_id_header: Option<String>,
    input_tokens_header: Option<String>,
}

impl EmbeddingsBackendCapture {
    fn assert_request(&self, path: &str, body: &Bytes, model: &str, request_id: &str) {
        assert_eq!(self.path_and_query, path);
        assert_eq!(&self.body, body);
        assert_eq!(self.model_header.as_deref(), Some(model));
        assert_eq!(self.request_id_header.as_deref(), Some(request_id));
        assert_eq!(self.input_tokens_header.as_deref(), Some("1"));
    }
}

type EmbeddingsCapture = Arc<std::sync::Mutex<Option<EmbeddingsBackendCapture>>>;

fn request_header(req: &Request, name: &'static str) -> Option<String> {
    req.headers()
        .get(name)
        .and_then(|value| value.to_str().ok())
        .map(ToOwned::to_owned)
}

fn response_header<'a>(response: &'a reqwest::Response, name: &str) -> Option<&'a str> {
    response.headers().get(name)?.to_str().ok()
}

macro_rules! assert_json_pointers {
    ($value:expr, $($pointer:literal => $expected:expr),+ $(,)?) => {
        $(assert_eq!($value.pointer($pointer), Some(&serde_json::json!($expected)));)+
    };
}

async fn capture_embeddings_request(
    req: Request,
    capture: EmbeddingsCapture,
    response_body: &'static str,
) -> Response {
    let path_and_query = req
        .uri()
        .path_and_query()
        .map(|value| value.as_str().to_string())
        .unwrap_or_else(|| "/v1/embeddings".to_string());
    let model_header = request_header(&req, "x-model");
    let request_id_header = request_header(&req, "x-request-id");
    let input_tokens_header = request_header(&req, "x-input-tokens");
    let body = axum::body::to_bytes(req.into_body(), 1024 * 1024)
        .await
        .expect("embedding request body should be readable");
    *capture.lock().expect("capture mutex poisoned") = Some(EmbeddingsBackendCapture {
        path_and_query,
        body,
        model_header,
        request_id_header,
        input_tokens_header,
    });
    Response::builder()
        .header("content-type", "application/json")
        .body(Body::from(response_body))
        .expect("embedding response should build")
}

fn metrics_text(registry: Arc<prometheus::Registry>) -> String {
    let metric_families = registry.gather();
    let mut buffer = Vec::new();
    TextEncoder::new()
        .encode(&metric_families, &mut buffer)
        .expect("encode metrics");
    String::from_utf8(buffer).expect("metrics must be utf8")
}

fn metric_sum_value(metrics: &str, metric_name: &str, label_fragments: &[&str]) -> f64 {
    metrics
        .lines()
        .filter_map(|line| {
            let (sample, value) = line.rsplit_once(' ')?;
            let name = sample.split('{').next().unwrap_or(sample);
            if name == metric_name && label_fragments.iter().all(|label| sample.contains(label)) {
                value.parse::<f64>().ok()
            } else {
                None
            }
        })
        .sum()
}

fn assert_metric_delta(
    before: &str,
    after: &str,
    metric_name: &str,
    label_fragments: &[&str],
    expected: f64,
) {
    assert_eq!(
        metric_sum_value(after, metric_name, label_fragments)
            - metric_sum_value(before, metric_name, label_fragments),
        expected,
        "unexpected {metric_name} delta for {label_fragments:?}"
    );
}

macro_rules! assert_delta {
    ($before:expr, $after:expr, $metric:expr, $expected:expr; $($label:expr),+ $(,)?) => {
        assert_metric_delta($before, $after, $metric, &[$($label),+], $expected)
    };
}

fn assert_metric_sample(metrics: &str, sample: &str, expected: bool, context: &str) {
    assert_eq!(metrics.contains(sample), expected, "{context}:\n{metrics}");
}

async fn poll_until<T, F, Fut>(description: &str, timeout: Duration, mut attempt: F) -> T
where
    F: FnMut() -> Fut,
    Fut: std::future::Future<Output = Option<T>>,
{
    let deadline = tokio::time::Instant::now() + timeout;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        if let Some(value) = attempt().await {
            return value;
        }
        assert!(tokio::time::Instant::now() < deadline, "{description}");
        poll.tick().await;
    }
}

fn assert_retry_metric_deltas(
    before: &str,
    after: &str,
    model: &str,
    reject_id: &str,
    success_id: &str,
    reason: &str,
    selections: f64,
) {
    let model_label = format!(r#"model="{model}""#);
    for server_id in [reject_id, success_id] {
        assert_delta!(
            before, after, "stargate_proxy_attempts_total", 1.0;
            &format!(r#"inference_server_id="{server_id}""#), &model_label
        );
    }
    assert_delta!(
        before, after, "stargate_proxy_retries_total", 1.0;
        &model_label, &format!(r#"reason="{reason}""#)
    );
    assert_delta!(
        before, after, "stargate_routing_selections_total", selections;
        &model_label
    );
    assert_delta!(
        before, after, "stargate_requests_total", 1.0;
        &model_label
    );
    assert_delta!(
        before, after, "stargate_requests_total", 0.0;
        &format!(r#"inference_server_id="{reject_id}""#), &model_label
    );
}

fn active_runtime(model: &str) -> PylonRuntimeState {
    let runtime = PylonRuntimeState::new(InferenceServerStatus::Active, &[model.to_string()]);
    runtime.set_model_stats(
        model,
        CurrentModelStats {
            last_mean_input_tps: 1000.0,
            ..CurrentModelStats::default()
        },
    );
    runtime
}

fn set_model_queue(runtime: &PylonRuntimeState, model: &str, queued_input_size: u64) {
    runtime.set_model_stats(
        model,
        CurrentModelStats {
            last_mean_input_tps: 1000.0,
            queued_input_size,
            ..CurrentModelStats::default()
        },
    );
}

fn observe_connecting_request(
    runtime: &PylonRuntimeState,
    model: &str,
    request_id: String,
    input_tokens: u64,
) {
    runtime.observe_request(RequestObservation {
        endpoint: RequestObservationEndpoint::ChatCompletions,
        request_id,
        routing_key: None,
        model_id: model.to_string(),
        priority: 0,
        input_tokens,
        embedding_items: 0,
        embedding_items_observed: false,
        upstream_status: None,
        output_messages: 0,
        output_tokens: 0,
        output_tokens_explicit: false,
        output_tokens_from_chunk_usage: false,
        state: RequestObservationState::UpstreamConnecting,
        time_to_response_headers: None,
        time_to_first_output: None,
        time_to_first_token: None,
        total_duration: Duration::ZERO,
    });
}

fn proxy_json_request(
    client: &reqwest::Client,
    http_addr: std::net::SocketAddr,
    path: &str,
    model: &str,
    request_id: &str,
    body: &serde_json::Value,
) -> reqwest::RequestBuilder {
    proxy_request(client, http_addr, path, model, request_id).json(body)
}

fn proxy_request(
    client: &reqwest::Client,
    http_addr: std::net::SocketAddr,
    path: &str,
    model: &str,
    request_id: &str,
) -> reqwest::RequestBuilder {
    with_proxy_headers(
        client.post(format!("http://{http_addr}{path}")),
        model,
        request_id,
    )
    .header("content-type", "application/json")
}

fn streaming_chat_body(model: &str) -> serde_json::Value {
    serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    })
}

fn assert_routing_headers(
    response: &reqwest::Response,
    inference_server_id: &str,
    inference_server_url: &str,
    cluster_id: &str,
) {
    for (name, expected) in [
        ("x-inference-server-id", inference_server_id),
        ("x-inference-server-url", inference_server_url),
        ("x-stargate-cluster-id", cluster_id),
    ] {
        assert_eq!(
            response_header(response, name),
            Some(expected),
            "unexpected {name}"
        );
    }
}

fn start_registration(
    config: InferenceServerRegistrationConfig,
    failure: &'static str,
) -> InferenceServerRegistrationClient {
    let mut registration = InferenceServerRegistrationClient::default();
    registration.start(config).expect(failure);
    registration
}

async fn shutdown_stargate(handle: stargate::runtime::StargateHandle) {
    handle.begin_shutdown();
    assert!(handle.wait_for_shutdown(Duration::from_secs(5)).await);
}

async fn finish_stargate(handle: stargate::runtime::StargateHandle) {
    handle.begin_shutdown();
    let _ = handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[derive(Clone)]
struct CapturingChatBackend {
    addr: std::net::SocketAddr,
    hits: Arc<AtomicUsize>,
    bodies: Arc<std::sync::Mutex<Vec<Bytes>>>,
}

impl CapturingChatBackend {
    fn hits(&self) -> usize {
        self.hits.load(Ordering::SeqCst)
    }

    fn bodies(&self) -> Vec<Bytes> {
        self.bodies.lock().expect("body capture poisoned").clone()
    }
}

async fn start_capturing_chat_backend(retryable_rejection: bool) -> CapturingChatBackend {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let hits = Arc::new(AtomicUsize::new(0));
    let bodies = Arc::new(std::sync::Mutex::new(Vec::new()));
    let hits_for_app = hits.clone();
    let bodies_for_app = bodies.clone();
    let app = Router::new()
        .route(
            "/v1/chat/completions",
            post(move |req: Request| {
                let hits = hits_for_app.clone();
                let bodies = bodies_for_app.clone();
                async move {
                    let body = axum::body::to_bytes(req.into_body(), 1024 * 1024)
                        .await
                        .expect("capturing backend request body should be readable");
                    hits.fetch_add(1, Ordering::SeqCst);
                    bodies.lock().expect("body capture poisoned").push(body);
                    if retryable_rejection {
                        let mut response = Response::new(Body::from(r#"{"error":"queue full"}"#));
                        *response.status_mut() = StatusCode::TOO_MANY_REQUESTS;
                        response.headers_mut().insert(
                            HeaderName::from_static("x-stargate-upstream-retryable"),
                            HeaderValue::from_static("true"),
                        );
                        return response;
                    }
                    Response::builder()
                        .status(StatusCode::OK)
                        .header("content-type", "text/event-stream")
                        .body(Body::from(
                            "data: {\"object\":\"chat.completion.chunk\"}\n\ndata: [DONE]\n\n",
                        ))
                        .expect("success response should build")
                }
            }),
        )
        .route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    CapturingChatBackend { addr, hits, bodies }
}

fn make_stargate_runtime_with_retry(
    id: &str,
    retry: ProxyRetryConfig,
) -> (std::net::SocketAddr, std::net::SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (model_discovery_addr, model_discovery_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SelfDiscovery::new(id, grpc_addr, http_addr);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.model_discovery_listen_addr = model_discovery_addr;
    config.proxy_transport.retry = retry;
    let listeners = BoundStargateListeners::from_prebound(
        &config,
        grpc_listener,
        model_discovery_listener,
        http_listener,
        None,
    )
    .expect("proxy contract runtime listeners must match their configuration");
    let runtime = StargateRuntime::new(config, Box::new(discovery), listeners, None);
    (grpc_addr, http_addr, runtime)
}

async fn start_stargate(
    id: &str,
) -> (
    std::net::SocketAddr,
    std::net::SocketAddr,
    stargate::runtime::StargateHandle,
) {
    init_crypto();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime(id);
    let handle = runtime.start().await.expect("stargate failed to start");
    (grpc_addr, http_addr, handle)
}

struct ProxyFixture {
    grpc_addr: std::net::SocketAddr,
    http_addr: std::net::SocketAddr,
    handle: stargate::runtime::StargateHandle,
    registrations: Vec<InferenceServerRegistrationClient>,
    tunnels: Vec<QuicHttpTunnelHandle>,
}

impl ProxyFixture {
    async fn start(id: &str) -> Self {
        let (grpc_addr, http_addr, handle) = start_stargate(id).await;
        Self::new(grpc_addr, http_addr, handle)
    }

    async fn start_with_retry(id: &str, retry: ProxyRetryConfig) -> Self {
        init_crypto();
        let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_retry(id, retry);
        let handle = runtime.start().await.expect("stargate failed to start");
        Self::new(grpc_addr, http_addr, handle)
    }

    async fn start_for_tunnel_case(id: &str, case: TunnelTestCase) -> (Self, bool) {
        init_crypto();
        let (grpc_addr, http_addr, reverse_target, runtime) =
            make_stargate_runtime_for_tunnel_case(id, case);
        let handle = runtime.start().await.expect("stargate failed to start");
        (
            Self::new(grpc_addr, http_addr, handle),
            reverse_target.is_some(),
        )
    }

    fn new(
        grpc_addr: std::net::SocketAddr,
        http_addr: std::net::SocketAddr,
        handle: stargate::runtime::StargateHandle,
    ) -> Self {
        Self {
            grpc_addr,
            http_addr,
            handle,
            registrations: Vec::new(),
            tunnels: Vec::new(),
        }
    }

    fn register(&mut self, config: InferenceServerRegistrationConfig, failure: &'static str) {
        self.registrations.push(start_registration(config, failure));
    }

    fn own_tunnel(&mut self, tunnel: QuicHttpTunnelHandle) {
        self.tunnels.push(tunnel);
    }

    async fn add_dummy_backend(
        &mut self,
        backend_id: &str,
        cluster_id: &str,
        model: &str,
        runtime_state: PylonRuntimeState,
    ) -> String {
        let (upstream_addr, quic_url, tunnel) = start_dummy_inst(model).await;
        self.register(
            active_registration_config_with_state(
                self.grpc_addr,
                backend_id,
                cluster_id,
                quic_url.clone(),
                format!("http://{upstream_addr}"),
                runtime_state,
            ),
            "backend registration failed",
        );
        self.own_tunnel(tunnel);
        quic_url
    }

    fn chat_request(&self, model: &str, request_id: &str) -> reqwest::RequestBuilder {
        proxy_json_request(
            &reqwest::Client::new(),
            self.http_addr,
            "/v1/chat/completions",
            model,
            request_id,
            &streaming_chat_body(model),
        )
    }

    async fn add_endpoint_backend(&mut self, backend_id: &str, model: &str) -> String {
        let (upstream_addr, quic_url, tunnel) = start_responses_inst(model).await;
        self.register(
            active_registration_config(
                self.grpc_addr,
                backend_id,
                quic_url.clone(),
                format!("http://{upstream_addr}"),
                model,
            ),
            "registration failed",
        );
        self.own_tunnel(tunnel);
        wait_for_routing(self.http_addr, model, Duration::from_secs(5)).await;
        quic_url
    }

    async fn add_embeddings_backend(
        &mut self,
        backend_id: &str,
        response_body: &'static str,
    ) -> (String, EmbeddingsCapture) {
        let (upstream_addr, quic_url, tunnel, capture) = start_embeddings_inst(response_body).await;
        self.register(
            active_registration_config(
                self.grpc_addr,
                backend_id,
                quic_url.clone(),
                format!("http://{upstream_addr}"),
                "embedding-model",
            ),
            "registration failed",
        );
        self.own_tunnel(tunnel);
        wait_for_routing(self.http_addr, "embedding-model", Duration::from_secs(5)).await;
        (quic_url, capture)
    }

    async fn add_retryable_backend(&mut self, backend_id: &str, model: &str) {
        let (upstream_addr, quic_url, tunnel) = start_retryable_rejecting_inst().await;
        self.register(
            active_registration_config(
                self.grpc_addr,
                backend_id,
                quic_url,
                format!("http://{upstream_addr}"),
                model,
            ),
            "registration failed",
        );
        self.own_tunnel(tunnel);
    }

    async fn add_capturing_direct_backend(
        &mut self,
        backend_id: &str,
        cluster_id: &str,
        runtime_state: PylonRuntimeState,
        retryable_rejection: bool,
    ) -> CapturingChatBackend {
        let backend = start_capturing_chat_backend(retryable_rejection).await;
        let mut tunnel_config = QuicHttpTunnelConfig::new(
            "127.0.0.1:0".parse().unwrap(),
            format!("http://{}", backend.addr),
        );
        tunnel_config.forwarding.runtime_state = runtime_state.clone();
        let tunnel = start_quic_http_tunnel(tunnel_config)
            .await
            .expect("capturing backend tunnel failed to start");
        let quic_url = format!("quic://{}", tunnel.listen_addr());
        self.register(
            active_registration_config_with_state(
                self.grpc_addr,
                backend_id,
                cluster_id,
                quic_url,
                format!("http://{}", backend.addr),
                runtime_state,
            ),
            "capturing backend registration failed",
        );
        self.own_tunnel(tunnel);
        backend
    }

    async fn add_capturing_reverse_backend(
        &mut self,
        backend_id: &str,
        cluster_id: &str,
        protocol: TunnelTransportProtocol,
        runtime_state: PylonRuntimeState,
        retryable_rejection: bool,
    ) -> CapturingChatBackend {
        let backend = start_capturing_chat_backend(retryable_rejection).await;
        self.register(
            reverse_registration_config_for_protocol(
                self.grpc_addr,
                backend_id,
                cluster_id,
                format!("http://{}", backend.addr),
                protocol,
                runtime_state,
            ),
            "reverse backend registration failed",
        );
        backend
    }

    fn metrics(&self) -> String {
        metrics_text(self.handle.metrics().registry())
    }

    async fn shutdown(mut self) {
        for registration in &mut self.registrations {
            registration.stop();
        }
        for tunnel in self.tunnels {
            tunnel.shutdown().await;
        }
        shutdown_stargate(self.handle).await;
    }
}

async fn start_embeddings_inst(
    response_body: &'static str,
) -> (
    std::net::SocketAddr,
    String,
    QuicHttpTunnelHandle,
    EmbeddingsCapture,
) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let capture = Arc::new(std::sync::Mutex::new(None));
    let capture_for_app = capture.clone();
    let app = Router::new()
        .route(
            "/v1/embeddings",
            post(move |req: Request| {
                let capture = capture_for_app.clone();
                capture_embeddings_request(req, capture, response_body)
            }),
        )
        .route("/v1/chat/completions", post(dummy_chat))
        .route("/health", get(|| async { "ok" }))
        .with_state(DummyState {
            model: "embedding-model".to_string(),
        });
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{addr}"),
    ))
    .await
    .expect("embedding tunnel failed to start");
    let quic_url = format!("quic://{}", tunnel.listen_addr());
    (addr, quic_url, tunnel, capture)
}

async fn start_retryable_rejecting_inst() -> (std::net::SocketAddr, String, QuicHttpTunnelHandle) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let app = Router::new()
        .route(
            "/v1/chat/completions",
            post(|_req: Request| async move {
                let mut response = Response::new(Body::from(r#"{"error":"queue full"}"#));
                *response.status_mut() = StatusCode::TOO_MANY_REQUESTS;
                response.headers_mut().insert(
                    HeaderName::from_static("x-stargate-upstream-retryable"),
                    HeaderValue::from_static("true"),
                );
                response
            }),
        )
        .route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{addr}"),
    ))
    .await
    .expect("reject tunnel failed to start");
    let quic_url = format!("quic://{}", tunnel.listen_addr());
    (addr, quic_url, tunnel)
}

async fn start_responses_inst(model: &str) -> (std::net::SocketAddr, String, QuicHttpTunnelHandle) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let app = Router::new()
        .route("/v1/chat/completions", post(endpoint_contract_response))
        .route("/v1/responses", post(endpoint_contract_response))
        .route("/health", get(|| async { "ok" }))
        .with_state(EndpointContractState {
            model: model.to_string(),
        });
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{addr}"),
    ))
    .await
    .expect("responses tunnel failed to start");
    let quic_url = format!("quic://{}", tunnel.listen_addr());
    (addr, quic_url, tunnel)
}

async fn start_direct_endpoint_contract_inst(
    model: &str,
    protocol: TunnelTransportProtocol,
) -> (
    std::net::SocketAddr,
    String,
    QuicHttpTunnelHandle,
    EmbeddingsCapture,
) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let capture = Arc::new(std::sync::Mutex::new(None));
    let capture_for_app = capture.clone();
    let app = Router::new()
        .route("/v1/chat/completions", post(endpoint_contract_response))
        .route("/v1/responses", post(endpoint_contract_response))
        .route(
            "/v1/embeddings",
            post(move |req: Request| {
                let capture = capture_for_app.clone();
                capture_embeddings_request(
                    req,
                    capture,
                    r#"{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"usage":{"prompt_tokens":2,"total_tokens":2}}"#,
                )
            }),
        )
        .route("/health", get(|| async { "ok" }))
        .with_state(EndpointContractState {
            model: model.to_string(),
        });
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });

    let mut config =
        QuicHttpTunnelConfig::new("127.0.0.1:0".parse().unwrap(), format!("http://{addr}"));
    config.tunnel_protocol = protocol;
    let tunnel = start_quic_http_tunnel(config)
        .await
        .expect("direct endpoint contract tunnel failed to start");
    let quic_url = format!("quic://{}", tunnel.listen_addr());
    (addr, quic_url, tunnel, capture)
}

#[derive(Clone)]
struct EndpointContractState {
    model: String,
}

async fn endpoint_contract_response(
    State(state): State<EndpointContractState>,
    req: Request,
) -> Response {
    let model = state.model;
    let path_and_query = req
        .uri()
        .path_and_query()
        .map(|value| value.as_str().to_string())
        .unwrap_or_else(|| req.uri().path().to_string());
    if path_and_query.contains("fail=1") {
        let error = if path_and_query.starts_with("/v1/chat/completions") {
            "chat completions unavailable"
        } else {
            "responses unavailable"
        };
        let mut response = axum::Json(serde_json::json!({
            "error": error,
        }))
        .into_response();
        *response.status_mut() = StatusCode::SERVICE_UNAVAILABLE;
        return response;
    }

    let body = axum::body::to_bytes(req.into_body(), 1024 * 1024)
        .await
        .expect("request body should be readable");
    let request_json: serde_json::Value =
        serde_json::from_slice(&body).expect("request body should be json");

    let is_chat_completion = path_and_query.starts_with("/v1/chat/completions");
    if is_chat_completion
        && request_json.get("stream").and_then(|value| value.as_bool()) != Some(true)
    {
        return axum::Json(serde_json::json!({
            "id": "chatcmpl-test",
            "object": "chat.completion",
            "model": model,
            "path_and_query": path_and_query,
            "choices": [{
                "index": 0,
                "message": { "role": "assistant", "content": "contract echo" },
                "finish_reason": "stop",
            }],
            "usage": {
                "prompt_tokens": 1,
                "completion_tokens": 3,
                "total_tokens": 4,
            },
        }))
        .into_response();
    }

    let stream_body = if is_chat_completion {
        format!(
            "data: {}\n\ndata: {}\n\ndata: [DONE]\n\n",
            serde_json::json!({
                "id": "chatcmpl-test",
                "object": "chat.completion.chunk",
                "model": model.clone(),
                "path_and_query": path_and_query.clone(),
                "choices": [{
                    "index": 0,
                    "delta": { "role": "assistant" },
                    "finish_reason": null,
                }],
            }),
            serde_json::json!({
                "id": "chatcmpl-test",
                "object": "chat.completion.chunk",
                "model": model,
                "path_and_query": path_and_query,
                "request": request_json,
                "choices": [{
                    "index": 0,
                    "delta": { "content": "contract echo" },
                    "finish_reason": null,
                }],
            })
        )
    } else {
        format!(
            "event: response.created\ndata: {}\n\nevent: response.completed\ndata: {}\n\n",
            serde_json::json!({
                "type": "response.created",
                "response": {
                    "id": "resp-test",
                    "object": "response",
                    "status": "in_progress",
                    "model": model.clone(),
                    "path_and_query": path_and_query.clone(),
                },
            }),
            serde_json::json!({
                "type": "response.completed",
                "response": {
                    "id": "resp-test",
                    "object": "response",
                    "status": "completed",
                    "model": model,
                    "path_and_query": path_and_query,
                    "request": request_json,
                },
            })
        )
    };

    let mut response = Response::new(Body::from(stream_body));
    response.headers_mut().insert(
        HeaderName::from_static("content-type"),
        HeaderValue::from_static("text/event-stream"),
    );
    response
}

fn active_registration_config(
    grpc_addr: std::net::SocketAddr,
    inference_server_id: &str,
    inference_server_url: String,
    upstream_http_base_url: String,
    model_id: &str,
) -> InferenceServerRegistrationConfig {
    active_registration_config_in_cluster(
        grpc_addr,
        inference_server_id,
        "",
        inference_server_url,
        upstream_http_base_url,
        model_id,
    )
}

fn active_stale_connection_registration_config(
    grpc_addr: std::net::SocketAddr,
    inference_server_id: &str,
    inference_server_url: String,
    upstream_http_base_url: String,
    model_id: &str,
) -> InferenceServerRegistrationConfig {
    let mut config = active_registration_config(
        grpc_addr,
        inference_server_id,
        inference_server_url,
        upstream_http_base_url,
        model_id,
    );
    // Keep the admitted route stable after tunnel shutdown so stale-connection
    // tests observe the HTTP hot path instead of racing a control-plane heartbeat.
    config.min_update_interval = Duration::from_secs(60);
    config
}

fn active_registration_config_in_cluster(
    grpc_addr: std::net::SocketAddr,
    inference_server_id: &str,
    cluster_id: &str,
    inference_server_url: String,
    upstream_http_base_url: String,
    model_id: &str,
) -> InferenceServerRegistrationConfig {
    active_registration_config_with_state(
        grpc_addr,
        inference_server_id,
        cluster_id,
        inference_server_url,
        upstream_http_base_url,
        PylonRuntimeState::new(InferenceServerStatus::Active, &[model_id.to_string()]),
    )
}

fn active_registration_config_with_state(
    grpc_addr: std::net::SocketAddr,
    inference_server_id: &str,
    cluster_id: &str,
    inference_server_url: String,
    upstream_http_base_url: String,
    runtime_state: PylonRuntimeState,
) -> InferenceServerRegistrationConfig {
    InferenceServerRegistrationConfig {
        cluster_id: cluster_id.to_string(),
        ..direct_registration_config(
            vec![grpc_addr.to_string()],
            inference_server_id,
            inference_server_url,
            upstream_http_base_url,
            runtime_state,
        )
    }
}

fn reverse_registration_config_for_protocol(
    grpc_addr: std::net::SocketAddr,
    inference_server_id: &str,
    cluster_id: &str,
    upstream_http_base_url: String,
    protocol: TunnelTransportProtocol,
    runtime_state: PylonRuntimeState,
) -> InferenceServerRegistrationConfig {
    InferenceServerRegistrationConfig {
        cluster_id: cluster_id.to_string(),
        tunnel_protocol: protocol,
        ..reverse_registration_config(
            vec![grpc_addr.to_string()],
            inference_server_id,
            upstream_http_base_url,
            runtime_state,
        )
    }
}

struct PulsarHeaderFixture {
    http_addr: std::net::SocketAddr,
    handle: stargate::runtime::StargateHandle,
    registration: InferenceServerRegistrationClient,
    _tunnel: QuicHttpTunnelHandle,
}

impl PulsarHeaderFixture {
    async fn start(runtime_id: &str) -> Self {
        init_crypto();
        let mut config_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
        std::io::Write::write_all(
            &mut config_file,
            br#"{"default":"power-of-two","models":{"pulsar-model":{"algorithm":"pulsar","seed":"test-seed","require_cache_affinity_key":true,"require_input_tokens":true}}}"#,
        )
        .expect("failed to write config");
        let config_path = config_file.path().to_str().unwrap().to_string();
        let (grpc_addr, http_addr, runtime) =
            make_stargate_runtime_with_lb(runtime_id, Some(config_path));
        let handle = runtime.start().await.expect("stargate failed to start");
        let (inst_addr, quic_url, tunnel) = start_dummy_inst("pulsar-model").await;
        let registration = start_registration(
            direct_registration_config(
                vec![grpc_addr.to_string()],
                "pulsar-inst",
                quic_url,
                format!("http://{inst_addr}"),
                active_runtime("pulsar-model"),
            ),
            "registration failed",
        );
        wait_for_routing_with_cache_affinity(
            http_addr,
            "pulsar-model",
            "prefix-a",
            Duration::from_secs(5),
        )
        .await;
        Self {
            http_addr,
            handle,
            registration,
            _tunnel: tunnel,
        }
    }

    fn chat_request(&self, request_id: &str) -> reqwest::RequestBuilder {
        reqwest::Client::new()
            .post(format!("http://{}/v1/chat/completions", self.http_addr))
            .header("x-model", "pulsar-model")
            .header("x-request-id", request_id)
            .header("content-type", "application/json")
            .json(&streaming_chat_body("pulsar-model"))
    }

    async fn assert_missing_header(self, request_id: &str, present_header: (&str, &str)) {
        let response = self
            .chat_request(request_id)
            .header(present_header.0, present_header.1)
            .send()
            .await
            .expect("request failed");
        assert_eq!(response.status(), StatusCode::BAD_REQUEST);
        self.shutdown().await;
    }

    async fn shutdown(mut self) {
        self.registration.stop();
        finish_stargate(self.handle).await;
    }
}

#[tokio::test]
async fn direct_http3_and_webtransport_proxy_supported_endpoint_contracts() {
    for protocol in [
        TunnelTransportProtocol::Http3,
        TunnelTransportProtocol::WebTransport,
    ] {
        exercise_direct_protocol_endpoint_contract(protocol).await;
    }
}

async fn exercise_direct_protocol_endpoint_contract(protocol: TunnelTransportProtocol) {
    let case = TunnelTestCase::direct(protocol);
    let id = format!("test-sg-direct-{}-contracts", case.protocol_label());
    let (mut fixture, has_reverse_target) = ProxyFixture::start_for_tunnel_case(&id, case).await;
    assert!(
        !has_reverse_target,
        "direct fixture must not expose a reverse target"
    );
    let model = format!("direct-{}-contract-model", case.protocol_label());
    let backend_id = format!("direct-{}-contract-inst", case.protocol_label());
    let (backend_addr, quic_url, tunnel, embedding_capture) =
        start_direct_endpoint_contract_inst(&model, protocol).await;
    let mut registration_config = active_registration_config(
        fixture.grpc_addr,
        &backend_id,
        quic_url.clone(),
        format!("http://{backend_addr}"),
        &model,
    );
    registration_config.tunnel_protocol = protocol;
    fixture.register(registration_config, "registration failed");
    fixture.own_tunnel(tunnel);

    wait_for_routing(fixture.http_addr, &model, Duration::from_secs(10)).await;

    let http_client = reqwest::Client::new();
    let chat_body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "direct transport contract"}],
        "stream": true,
    });
    let chat_response = proxy_request(
        &http_client,
        fixture.http_addr,
        &format!("/v1/chat/completions?transport={}", case.protocol_label()),
        &model,
        &format!("req-direct-{}-chat", case.protocol_label()),
    )
    .json(&chat_body)
    .send()
    .await
    .expect("chat contract request failed");
    assert_eq!(chat_response.status(), StatusCode::OK);
    assert_eq!(
        response_header(&chat_response, "x-inference-server-id"),
        Some(backend_id.as_str())
    );
    let chat_text = chat_response
        .text()
        .await
        .expect("chat response should be readable");
    let chat_events = parse_sse_events(&chat_text).expect("chat response should be valid SSE");
    assert_sse_done(&chat_events);
    let chat_payloads = json_events(&chat_events);
    assert!(
        chat_payloads.iter().any(|payload| {
            payload["object"] == "chat.completion.chunk"
                && payload["model"] == model
                && payload
                    .pointer("/request/messages/0/content")
                    .and_then(serde_json::Value::as_str)
                    == Some("direct transport contract")
        }),
        "chat SSE payloads did not preserve the request contract: {chat_payloads:#?}"
    );

    let responses_body = serde_json::json!({
        "model": model,
        "input": "direct responses contract",
        "stream": true,
    });
    let responses_response = proxy_request(
        &http_client,
        fixture.http_addr,
        &format!("/v1/responses?transport={}", case.protocol_label()),
        &model,
        &format!("req-direct-{}-responses", case.protocol_label()),
    )
    .json(&responses_body)
    .send()
    .await
    .expect("responses contract request failed");
    assert_eq!(responses_response.status(), StatusCode::OK);
    assert_eq!(
        response_header(&responses_response, "x-inference-server-id"),
        Some(backend_id.as_str())
    );
    let responses_text = responses_response
        .text()
        .await
        .expect("responses response should be readable");
    let response_events =
        parse_sse_events(&responses_text).expect("responses response should be valid SSE");
    assert!(
        response_events
            .iter()
            .any(|event| event.event_name.as_deref() == Some("response.completed"))
    );
    let response_payloads = json_events(&response_events);
    assert!(
        response_payloads.iter().any(|payload| {
            payload["type"] == "response.completed"
                && payload
                    .pointer("/response/object")
                    .and_then(serde_json::Value::as_str)
                    == Some("response")
                && payload
                    .pointer("/response/request/input")
                    .and_then(serde_json::Value::as_str)
                    == Some("direct responses contract")
        }),
        "responses SSE payloads did not preserve the request contract: {response_payloads:#?}"
    );

    let embedding_body = Bytes::from(format!(r#"{{"model":"{model}","input":["alpha","beta"]}}"#));
    let embedding_response = proxy_request(
        &http_client,
        fixture.http_addr,
        &format!("/v1/embeddings?transport={}", case.protocol_label()),
        &model,
        &format!("req-direct-{}-embeddings", case.protocol_label()),
    )
    .body(embedding_body.clone())
    .send()
    .await
    .expect("embeddings contract request failed");
    assert_eq!(embedding_response.status(), StatusCode::OK);
    assert_eq!(
        response_header(&embedding_response, "x-inference-server-id"),
        Some(backend_id.as_str())
    );
    assert_eq!(
        embedding_response
            .bytes()
            .await
            .expect("embedding response should be readable"),
        Bytes::from_static(
            br#"{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"usage":{"prompt_tokens":2,"total_tokens":2}}"#
        )
    );
    let captured = embedding_capture
        .lock()
        .expect("capture mutex poisoned")
        .clone()
        .expect("embedding backend should be called");
    captured.assert_request(
        &format!("/v1/embeddings?transport={}", case.protocol_label()),
        &embedding_body,
        &model,
        &format!("req-direct-{}-embeddings", case.protocol_label()),
    );

    fixture.shutdown().await;
}

#[tokio::test]
async fn reverse_raw_quic_http3_webtransport_retryable_rejection_replays_body() {
    for protocol in [
        TunnelTransportProtocol::RawQuic,
        TunnelTransportProtocol::Http3,
        TunnelTransportProtocol::WebTransport,
    ] {
        exercise_reverse_retryable_rejection(protocol).await;
    }
}

async fn exercise_reverse_retryable_rejection(protocol: TunnelTransportProtocol) {
    let case = TunnelTestCase::reverse(protocol);
    let model = format!("reverse-{}-retry-model", case.protocol_label());
    let reject_id = format!("reverse-{}-a-reject", case.protocol_label());
    let success_id = format!("reverse-{}-b-success", case.protocol_label());
    let (mut fixture, has_reverse_target) = ProxyFixture::start_for_tunnel_case(
        &format!("test-sg-reverse-{}-retry", case.protocol_label()),
        case,
    )
    .await;
    assert!(has_reverse_target, "reverse fixture must expose a target");
    let reject_runtime = active_runtime(&model);
    set_model_queue(&reject_runtime, &model, 0);
    let success_runtime = active_runtime(&model);
    set_model_queue(&success_runtime, &model, 400);
    let reject_backend = fixture
        .add_capturing_reverse_backend(
            &reject_id,
            &format!("reverse-{}-reject-cluster", case.protocol_label()),
            protocol,
            reject_runtime,
            true,
        )
        .await;
    let success_backend = fixture
        .add_capturing_reverse_backend(
            &success_id,
            &format!("reverse-{}-success-cluster", case.protocol_label()),
            protocol,
            success_runtime,
            false,
        )
        .await;

    let target = RoutingTargetKey {
        routing_key: None,
        model_id: model.clone(),
    };
    wait_until(
        &format!("reverse {} retry candidates", case.protocol_label()),
        Duration::from_secs(15),
        Duration::from_millis(50),
        || {
            let state = fixture.handle.state();
            let target = target.clone();
            async move {
                let candidates = state.cluster_candidates_for_target(&target).await;
                if candidates.len() == 2
                    && candidates
                        .iter()
                        .any(|candidate| candidate.stats.queued_input_size == 0)
                    && candidates
                        .iter()
                        .any(|candidate| candidate.stats.queued_input_size == 400)
                {
                    Ok(())
                } else {
                    Err(format!("candidates={candidates:?}"))
                }
            }
        },
    )
    .await;

    let before_metrics = fixture.metrics();
    let request_body = Bytes::from(format!(
        r#"{{"model":"{model}","messages":[{{"role":"user","content":"replay {}"}}],"stream":true}}"#,
        case.protocol_label()
    ));
    let response = proxy_request(
        &reqwest::Client::new(),
        fixture.http_addr,
        "/v1/chat/completions",
        &model,
        &format!("req-reverse-{}-retry", case.protocol_label()),
    )
    .body(request_body.clone())
    .send()
    .await
    .expect("reverse retry request failed");
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response_header(&response, "x-inference-server-id"),
        Some(success_id.as_str())
    );
    let success_body = response.text().await.expect("read success body");
    assert_sse_done(
        &parse_sse_events(&success_body).expect("successful retry body should be valid SSE"),
    );
    assert_eq!(reject_backend.hits(), 1);
    assert_eq!(success_backend.hits(), 1);
    assert_eq!(reject_backend.bodies(), vec![request_body.clone()]);
    assert_eq!(success_backend.bodies(), vec![request_body]);

    let after_metrics = fixture.metrics();
    assert_retry_metric_deltas(
        &before_metrics,
        &after_metrics,
        &model,
        &reject_id,
        &success_id,
        "upstream_admission_rejected",
        2.0,
    );

    fixture.shutdown().await;
}

#[tokio::test]
async fn reverse_raw_quic_http3_webtransport_queue_mismatch_retries_sibling_before_upstream() {
    for protocol in [
        TunnelTransportProtocol::RawQuic,
        TunnelTransportProtocol::Http3,
        TunnelTransportProtocol::WebTransport,
    ] {
        exercise_reverse_queue_mismatch(protocol).await;
    }
}

async fn exercise_reverse_queue_mismatch(protocol: TunnelTransportProtocol) {
    let case = TunnelTestCase::reverse(protocol);
    let model = format!("reverse-{}-queue-mismatch-model", case.protocol_label());
    let reject_id = format!("reverse-{}-a-queue-reject", case.protocol_label());
    let success_id = format!("reverse-{}-b-queue-success", case.protocol_label());
    let cluster_id = format!("reverse-{}-queue-cluster", case.protocol_label());
    let (mut fixture, has_reverse_target) = ProxyFixture::start_for_tunnel_case(
        &format!("test-sg-reverse-{}-queue", case.protocol_label()),
        case,
    )
    .await;
    assert!(has_reverse_target, "reverse fixture must expose a target");
    let reject_runtime = active_runtime(&model);
    set_model_queue(&reject_runtime, &model, 0);
    observe_connecting_request(
        &reject_runtime,
        &model,
        format!("existing-reverse-{}-queue-work", case.protocol_label()),
        100,
    );
    let success_runtime = active_runtime(&model);
    set_model_queue(&success_runtime, &model, 0);
    let reject_backend = fixture
        .add_capturing_reverse_backend(&reject_id, &cluster_id, protocol, reject_runtime, false)
        .await;
    let success_backend = fixture
        .add_capturing_reverse_backend(&success_id, &cluster_id, protocol, success_runtime, false)
        .await;

    let target = RoutingTargetKey {
        routing_key: None,
        model_id: model.clone(),
    };
    wait_until(
        &format!("reverse {} shared queue candidate", case.protocol_label()),
        Duration::from_secs(15),
        Duration::from_millis(50),
        || {
            let state = fixture.handle.state();
            let target = target.clone();
            let cluster_id = cluster_id.clone();
            async move {
                let candidates = state.cluster_candidates_for_target(&target).await;
                if candidates.len() == 1
                    && candidates[0].cluster_id == cluster_id
                    && candidates[0].active_backend_count == 2
                    && candidates[0].stats.queued_input_size == 0
                {
                    Ok(())
                } else {
                    Err(format!("candidates={candidates:?}"))
                }
            }
        },
    )
    .await;

    let before_metrics = fixture.metrics();
    let request_body = Bytes::from(format!(
        r#"{{"model":"{model}","messages":[{{"role":"user","content":"queue {}"}}],"stream":true}}"#,
        case.protocol_label()
    ));
    let response = proxy_request(
        &reqwest::Client::new(),
        fixture.http_addr,
        "/v1/chat/completions",
        &model,
        &format!("req-reverse-{}-queue", case.protocol_label()),
    )
    .body(request_body.clone())
    .send()
    .await
    .expect("reverse queue mismatch request failed");
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response_header(&response, "x-stargate-cluster-id"),
        Some(cluster_id.as_str())
    );
    assert_eq!(
        response_header(&response, "x-inference-server-id"),
        Some(success_id.as_str())
    );
    let success_body = response.text().await.expect("read success body");
    assert_sse_done(
        &parse_sse_events(&success_body).expect("successful retry body should be valid SSE"),
    );
    assert_eq!(
        reject_backend.hits(),
        0,
        "queue mismatch must reject before upstream"
    );
    assert_eq!(success_backend.hits(), 1);
    assert_eq!(success_backend.bodies(), vec![request_body]);

    wait_until(
        &format!(
            "reverse {} queue reservation release",
            case.protocol_label()
        ),
        Duration::from_secs(5),
        Duration::from_millis(50),
        || {
            let state = fixture.handle.state();
            let target = target.clone();
            async move {
                let candidates = state.cluster_candidates_for_target(&target).await;
                if candidates.len() == 1 && candidates[0].stats.queued_input_size == 0 {
                    Ok(())
                } else {
                    Err(format!("candidates={candidates:?}"))
                }
            }
        },
    )
    .await;

    let after_metrics = fixture.metrics();
    assert_retry_metric_deltas(
        &before_metrics,
        &after_metrics,
        &model,
        &reject_id,
        &success_id,
        "queue_estimate_mismatch",
        1.0,
    );

    fixture.shutdown().await;
}

#[tokio::test]
async fn chat_completions_route_proxies_path_query_and_body_through_quic_tunnel() {
    exercise_endpoint_contract(EndpointContract::Chat).await;
}

#[tokio::test]
async fn chat_completions_route_forwards_upstream_error_through_quic_tunnel() {
    assert_upstream_endpoint_error(
        "chat",
        "/v1/chat/completions?fail=1",
        "chat-error-model",
        streaming_chat_body("chat-error-model"),
        "chat completions unavailable",
    )
    .await;
}

#[tokio::test]
async fn responses_route_proxies_path_and_query_through_quic_tunnel() {
    exercise_endpoint_contract(EndpointContract::Responses).await;
}

#[derive(Clone, Copy)]
enum EndpointContract {
    Chat,
    Responses,
}

async fn exercise_endpoint_contract(endpoint: EndpointContract) {
    let (runtime_id, backend_id, model, path, request_id, body) = match endpoint {
        EndpointContract::Chat => (
            "test-sg-chat-contract",
            "chat-contract-inst",
            "chat-contract-model",
            "/v1/chat/completions?trace=chat",
            "req-chat-contract",
            serde_json::json!({
                "model": "chat-contract-model",
                "messages": [{"role": "user", "content": "contract hello"}],
                "max_tokens": 3,
                "stream": true,
            }),
        ),
        EndpointContract::Responses => (
            "test-sg-responses",
            "responses-inst",
            "responses-model",
            "/v1/responses?trace=1",
            "req-responses",
            serde_json::json!({
                "model": "responses-model",
                "input": "hello",
                "max_output_tokens": 2,
                "stream": true,
            }),
        ),
    };
    let mut fixture = ProxyFixture::start(runtime_id).await;
    let quic_url = fixture.add_endpoint_backend(backend_id, model).await;
    let response = proxy_json_request(
        &reqwest::Client::new(),
        fixture.http_addr,
        path,
        model,
        request_id,
        &body,
    )
    .send()
    .await
    .expect("endpoint contract request failed");
    assert_eq!(response.status(), StatusCode::OK);
    assert_routing_headers(&response, backend_id, &quic_url, backend_id);
    let events = parse_sse_events(&response.text().await.expect("response should be text"))
        .expect("response should be valid SSE");
    let payloads = json_events(&events);
    match endpoint {
        EndpointContract::Chat => {
            assert_sse_done(&events);
            assert!(
                payloads.iter().any(|payload| {
                    payload["object"] == "chat.completion.chunk"
                        && payload["model"] == model
                        && payload["path_and_query"] == path
                        && payload.pointer("/choices/0/delta/content")
                            == Some(&serde_json::json!("contract echo"))
                        && payload.pointer("/request/messages/0/content")
                            == Some(&serde_json::json!("contract hello"))
                        && payload.pointer("/request/stream") == Some(&serde_json::json!(true))
                }),
                "chat SSE payload did not preserve the endpoint contract: {payloads:#?}"
            );
        }
        EndpointContract::Responses => {
            assert_eq!(
                events
                    .iter()
                    .filter_map(|event| event.event_name.as_deref())
                    .collect::<Vec<_>>(),
                vec!["response.created", "response.completed"]
            );
            let completed = payloads
                .iter()
                .find(|payload| payload["type"] == "response.completed")
                .expect("responses stream should include response.completed");
            assert_json_pointers!(
                completed,
                "/response/object" => "response",
                "/response/model" => "responses-model",
                "/response/path_and_query" => "/v1/responses?trace=1",
                "/response/request/input" => "hello",
                "/response/request/stream" => true,
            );
        }
    }
    fixture.shutdown().await;
}

#[tokio::test]
async fn responses_route_forwards_upstream_error_through_quic_tunnel() {
    assert_upstream_endpoint_error(
        "responses",
        "/v1/responses?fail=1",
        "responses-error-model",
        serde_json::json!({
            "model": "responses-error-model",
            "input": "hello",
            "stream": true,
        }),
        "responses unavailable",
    )
    .await;
}

async fn assert_upstream_endpoint_error(
    endpoint_name: &str,
    path: &str,
    model: &str,
    body: serde_json::Value,
    expected_error: &str,
) {
    let backend_id = format!("{endpoint_name}-error-inst");
    let mut fixture = ProxyFixture::start(&format!("test-sg-{endpoint_name}-error")).await;
    let quic_url = fixture.add_endpoint_backend(&backend_id, model).await;
    let response = proxy_json_request(
        &reqwest::Client::new(),
        fixture.http_addr,
        path,
        model,
        &format!("req-{endpoint_name}-error"),
        &body,
    )
    .send()
    .await
    .expect("endpoint error request failed");
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    assert_routing_headers(&response, &backend_id, &quic_url, &backend_id);
    assert_eq!(
        response
            .json::<serde_json::Value>()
            .await
            .expect("response should be json")["error"],
        expected_error
    );
    fixture.shutdown().await;
}

/// The QUIC tunnel enforces that `/v1/responses` requests must set
/// `"stream": true` in the body. Non-streaming requests are rejected with 400.
#[tokio::test]
async fn non_streaming_responses_rejected_by_quic_tunnel() {
    let mut fixture = ProxyFixture::start("test-sg-responses-nonstream").await;
    fixture
        .add_endpoint_backend("responses-ns-inst", "responses-ns-model")
        .await;
    assert_non_streaming_rejected(
        fixture,
        "/v1/responses",
        "responses-ns-model",
        "req-responses-nonstream",
        serde_json::json!({
            "model": "responses-ns-model",
            "input": "hello",
            "stream": false,
        }),
        &["/v1/responses", "stream=true"],
    )
    .await;
}

#[tokio::test]
async fn embeddings_proxy_forwards_opaque_body() {
    let mut fixture = ProxyFixture::start("test-sg-embeddings").await;
    let embedding_response = r#"{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"embedding-model","usage":{"prompt_tokens":4,"total_tokens":4}}"#;
    let (quic_url, capture) = fixture
        .add_embeddings_backend("embedding-inst", embedding_response)
        .await;

    let body = br#"{"model":"embedding-model","input":["alpha","beta"],"encoding_format":"float"}"#;
    let http_client = reqwest::Client::new();
    let resp = proxy_request(
        &http_client,
        fixture.http_addr,
        "/v1/embeddings?trace=1",
        "embedding-model",
        "req-embedding-proxy",
    )
    .body(Bytes::from_static(body))
    .send()
    .await
    .expect("embedding request failed");

    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(
        response_header(&resp, "x-inference-server-id"),
        Some("embedding-inst")
    );
    assert_eq!(
        response_header(&resp, "x-inference-server-url"),
        Some(quic_url.as_str())
    );
    let response_body = resp.bytes().await.expect("response body should read");
    assert_eq!(
        response_body,
        Bytes::from_static(embedding_response.as_bytes())
    );

    let captured = capture
        .lock()
        .expect("capture mutex poisoned")
        .clone()
        .expect("embedding backend should be called");
    captured.assert_request(
        "/v1/embeddings?trace=1",
        &Bytes::from_static(body),
        "embedding-model",
        "req-embedding-proxy",
    );

    fixture.shutdown().await;
}

#[tokio::test]
async fn embeddings_missing_model_header_returns_400() {
    assert_missing_model_rejected(
        "test-sg-embeddings-no-model",
        "/v1/embeddings",
        "req-embedding-no-model",
        serde_json::json!({"model": "embedding-model", "input": "hello"}),
    )
    .await;
}

#[tokio::test]
async fn embeddings_missing_input_tokens_returns_400_without_upstream() {
    let mut fixture = ProxyFixture::start("test-sg-embeddings-no-input-tokens").await;
    let (_, capture) = fixture
        .add_embeddings_backend("embedding-no-input-inst", r#"{"unexpected":true}"#)
        .await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{}/v1/embeddings", fixture.http_addr);
    let resp = http_client
        .post(&stargate_url)
        .header("x-model", "embedding-model")
        .header("x-request-id", "req-embedding-no-input-tokens")
        .header("content-type", "application/json")
        .body(r#"{"model":"embedding-model","input":"hello"}"#)
        .send()
        .await
        .expect("embedding request failed");

    assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
    assert!(
        capture.lock().expect("capture mutex poisoned").is_none(),
        "embeddings upstream must not run without x-input-tokens"
    );

    fixture.shutdown().await;
}

/// The QUIC tunnel enforces that `/v1/chat/completions` requests must set
/// `"stream": true` in the body. Non-streaming requests are rejected with 400.
#[tokio::test]
async fn non_streaming_chat_completions_rejected_by_quic_tunnel() {
    let mut fixture = ProxyFixture::start("test-sg-nonstream").await;
    fixture
        .add_dummy_backend("ns-inst", "", "ns-model", active_runtime("ns-model"))
        .await;

    wait_for_routing(fixture.http_addr, "ns-model", Duration::from_secs(5)).await;

    assert_non_streaming_rejected(
        fixture,
        "/v1/chat/completions",
        "ns-model",
        "req-nonstream",
        serde_json::json!({
            "model": "ns-model",
            "messages": [{"role": "user", "content": "hi"}],
        }),
        &[],
    )
    .await;
}

async fn assert_non_streaming_rejected(
    fixture: ProxyFixture,
    path: &str,
    model: &str,
    request_id: &str,
    body: serde_json::Value,
    error_fragments: &[&str],
) {
    let response = proxy_json_request(
        &reqwest::Client::new(),
        fixture.http_addr,
        path,
        model,
        request_id,
        &body,
    )
    .send()
    .await
    .expect("non-streaming request failed");
    assert_eq!(
        response.status(),
        StatusCode::BAD_REQUEST,
        "non-streaming {path} should be rejected by the QUIC tunnel"
    );
    let response_text = response.text().await.expect("response should be text");
    for fragment in error_fragments {
        assert!(response_text.contains(fragment), "missing {fragment:?}");
    }
    fixture.shutdown().await;
}

#[tokio::test]
async fn missing_model_header_returns_400() {
    assert_missing_model_rejected(
        "test-sg-noheader",
        "/v1/chat/completions",
        "req-noheader",
        serde_json::json!({
            "model": "any-model",
            "messages": [{"role": "user", "content": "hi"}],
        }),
    )
    .await;
}

async fn assert_missing_model_rejected(
    runtime_id: &str,
    path: &str,
    request_id: &str,
    body: serde_json::Value,
) {
    let (_, http_addr, handle) = start_stargate(runtime_id).await;
    let response = reqwest::Client::new()
        .post(format!("http://{http_addr}{path}"))
        .header("x-request-id", request_id)
        .header("x-input-tokens", "1")
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
    assert_eq!(
        response.status(),
        StatusCode::BAD_REQUEST,
        "missing x-model should return 400 for {path} request {request_id}"
    );
    finish_stargate(handle).await;
}

#[tokio::test]
async fn supported_endpoint_required_proxy_headers_are_enforced() {
    let mut fixture = ProxyFixture::start("test-sg-required-headers").await;
    fixture
        .add_endpoint_backend("required-header-inst", "required-header-model")
        .await;

    let endpoints = [
        (
            "/v1/chat/completions",
            streaming_chat_body("required-header-model"),
        ),
        (
            "/v1/responses",
            serde_json::json!({
                "model": "required-header-model",
                "input": "hi",
                "stream": true,
            }),
        ),
    ];
    let required_headers = ["x-model", "x-request-id", "x-input-tokens"];
    let http_client = reqwest::Client::new();

    for (endpoint, body) in endpoints {
        for missing_header in required_headers {
            let mut request = http_client
                .post(format!("http://{}{endpoint}", fixture.http_addr))
                .header("content-type", "application/json");
            if missing_header != "x-model" {
                request = request.header("x-model", "required-header-model");
            }
            if missing_header != "x-request-id" {
                request = request.header(
                    "x-request-id",
                    format!("req-required-{endpoint}-{missing_header}"),
                );
            }
            if missing_header != "x-input-tokens" {
                request = request.header("x-input-tokens", "1");
            }

            let resp = request
                .json(&body)
                .send()
                .await
                .expect("required header request failed");
            assert_eq!(
                resp.status(),
                StatusCode::BAD_REQUEST,
                "{endpoint} missing {missing_header} should return 400"
            );
        }
    }

    fixture.shutdown().await;
}

#[tokio::test]
async fn response_headers_contain_server_id_and_url() {
    let mut fixture = ProxyFixture::start("test-sg-headers").await;
    let (upstream_addr, expected_url, tunnel) = start_dummy_inst("hdr-model").await;
    fixture.register(
        InferenceServerRegistrationConfig {
            cluster_id: "hdr-cluster".to_string(),
            ..direct_registration_config(
                vec![fixture.grpc_addr.to_string()],
                "hdr-inst",
                expected_url.clone(),
                format!("http://{upstream_addr}"),
                active_runtime("hdr-model"),
            )
        },
        "registration failed",
    );
    fixture.own_tunnel(tunnel);
    wait_for_routing(fixture.http_addr, "hdr-model", Duration::from_secs(5)).await;

    let body = streaming_chat_body("hdr-model");

    let resp = proxy_json_request(
        &reqwest::Client::new(),
        fixture.http_addr,
        "/v1/chat/completions",
        "hdr-model",
        "req-headers",
        &body,
    )
    .send()
    .await
    .expect("request failed");
    assert_eq!(resp.status(), 200);

    assert_routing_headers(&resp, "hdr-inst", &expected_url, "hdr-cluster");

    fixture.shutdown().await;
}

#[tokio::test]
async fn shared_cluster_round_robins_selected_backend_header() {
    let mut fixture = ProxyFixture::start("test-sg-shared-cluster-rr").await;
    fixture
        .add_dummy_backend(
            "shared-backend-a",
            "shared-cluster",
            "shared-cluster-model",
            active_runtime("shared-cluster-model"),
        )
        .await;
    fixture
        .add_dummy_backend(
            "shared-backend-b",
            "shared-cluster",
            "shared-cluster-model",
            active_runtime("shared-cluster-model"),
        )
        .await;

    wait_for_routing(
        fixture.http_addr,
        "shared-cluster-model",
        Duration::from_secs(5),
    )
    .await;

    let mut seen = std::collections::HashSet::new();
    for i in 0..4 {
        let resp = fixture
            .chat_request("shared-cluster-model", &format!("req-shared-cluster-{i}"))
            .send()
            .await
            .expect("request failed");
        assert_eq!(resp.status(), 200);
        assert_eq!(
            response_header(&resp, "x-stargate-cluster-id"),
            Some("shared-cluster")
        );
        seen.insert(
            response_header(&resp, "x-inference-server-id")
                .expect("missing x-inference-server-id")
                .to_string(),
        );
    }

    assert_eq!(
        seen,
        std::collections::HashSet::from([
            "shared-backend-a".to_string(),
            "shared-backend-b".to_string()
        ])
    );

    fixture.shutdown().await;
}

#[tokio::test]
async fn transport_local_shared_cluster_failover_stays_within_selected_cluster() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    std::io::Write::write_all(&mut tmp_file, br#"{"default":"power-of-two"}"#)
        .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-shared-cluster-local-failover", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");
    let mut fixture = ProxyFixture::new(grpc_addr, http_addr, handle);

    let bad_runtime = PylonRuntimeState::new(
        InferenceServerStatus::Active,
        &["shared-failover-model".to_string()],
    );
    fixture.register(
        InferenceServerRegistrationConfig {
            seeds: vec![grpc_addr.to_string()],
            inference_server_id: "shared-backend-a-bad".to_string(),
            cluster_id: "shared-failover-cluster".to_string(),
            inference_server_url: "http://127.0.0.1:1".to_string(),
            min_update_interval: Duration::from_millis(100),
            reverse_tunnel: true,
            forwarding: pylon_lib::TunnelForwardingConfig {
                runtime_state: bad_runtime.clone(),
                ..Default::default()
            },
            auth_token_provider: None,
            tls_cert_pem: None,
            quic_insecure: true,
            tunnel_protocol: Default::default(),
        },
        "bad registration failed",
    );
    let good_runtime = PylonRuntimeState::new(
        InferenceServerStatus::Active,
        &["shared-failover-model".to_string()],
    );
    fixture
        .add_dummy_backend(
            "shared-backend-b-good",
            "shared-failover-cluster",
            "shared-failover-model",
            good_runtime.clone(),
        )
        .await;
    let other_runtime = PylonRuntimeState::new(
        InferenceServerStatus::Active,
        &["shared-failover-model".to_string()],
    );
    fixture
        .add_dummy_backend(
            "other-cluster-backend",
            "other-cluster",
            "shared-failover-model",
            other_runtime.clone(),
        )
        .await;

    wait_for_routing(
        fixture.http_addr,
        "shared-failover-model",
        Duration::from_secs(5),
    )
    .await;

    for runtime_state in [&bad_runtime, &good_runtime] {
        runtime_state.set_model_stats(
            "shared-failover-model".to_string(),
            CurrentModelStats {
                last_mean_input_tps: 1000.0,
                queued_input_size: 0,
                ..CurrentModelStats::default()
            },
        );
    }
    other_runtime.set_model_stats(
        "shared-failover-model".to_string(),
        CurrentModelStats {
            last_mean_input_tps: 1000.0,
            queued_input_size: 400,
            ..CurrentModelStats::default()
        },
    );

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{}/v1/chat/completions", fixture.http_addr);
    let body = streaming_chat_body("shared-failover-model");

    wait_until(
        "transport-local retry stays within chosen shared cluster",
        Duration::from_secs(15),
        Duration::from_millis(100),
        || {
            let body = body.clone();
            let http_client = http_client.clone();
            let stargate_url = stargate_url.clone();
            async move {
                let resp = http_client
                    .post(&stargate_url)
                    .header("x-model", "shared-failover-model")
                    .header("x-request-id", "req-shared-cluster-local-failover")
                    .header("x-input-tokens", "700")
                    .header("content-type", "application/json")
                    .json(&body)
                    .send()
                    .await
                    .map_err(|error| error.to_string())?;
                let status = resp.status();
                if status != StatusCode::OK {
                    return Err(format!("status {status}"));
                }
                let cluster_id = response_header(&resp, "x-stargate-cluster-id");
                let server_id = response_header(&resp, "x-inference-server-id");
                if cluster_id == Some("shared-failover-cluster")
                    && server_id == Some("shared-backend-b-good")
                {
                    Ok(())
                } else {
                    Err(format!(
                        "cluster_id={cluster_id:?}, server_id={server_id:?}"
                    ))
                }
            }
        },
    )
    .await;

    fixture.shutdown().await;
}

#[tokio::test]
async fn unknown_model_returns_404_no_eligible_candidates() {
    let (_, http_addr, handle) = start_stargate("test-sg-unknown").await;

    let http_client = reqwest::Client::new();
    let body = serde_json::json!({
        "model": "nonexistent",
        "messages": [{"role": "user", "content": "hi"}],
    });

    let resp = proxy_json_request(
        &http_client,
        http_addr,
        "/v1/chat/completions",
        "nonexistent",
        "req-unknown",
        &body,
    )
    .send()
    .await
    .expect("request failed");
    assert_eq!(
        resp.status(),
        StatusCode::NOT_FOUND,
        "unknown model with no candidates should return 404"
    );
    assert_eq!(
        response_header(&resp, "x-stargate-error-code"),
        Some("no_eligible_candidates"),
        "no-candidates proxy errors should be distinguishable from upstream errors"
    );
    let body: serde_json::Value = resp
        .json()
        .await
        .expect("no-candidates response body should be json");
    assert_eq!(body["code"], "no_eligible_candidates");

    finish_stargate(handle).await;
}

#[tokio::test]
async fn retryable_upstream_rejection_retries_alternate_backend() {
    let mut fixture = ProxyFixture::start("test-sg-retryable-rejection").await;
    let reject_runtime = active_runtime("retry-model");
    let reject_backend = fixture
        .add_capturing_direct_backend("retry-reject", "", reject_runtime.clone(), true)
        .await;

    let success_runtime = active_runtime("retry-model");
    fixture
        .add_dummy_backend("retry-success", "", "retry-model", success_runtime.clone())
        .await;

    set_model_queue(&reject_runtime, "retry-model", 0);
    set_model_queue(&success_runtime, "retry-model", 1);

    let http_client = reqwest::Client::new();
    let body = streaming_chat_body("retry-model");

    let budget_deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let budget_limited_resp = proxy_json_request(
            &http_client,
            fixture.http_addr,
            "/v1/chat/completions",
            "retry-model",
            "req-retry-budget-zero",
            &body,
        )
        .header("x-stargate-max-wait-ms", "0")
        .send()
        .await
        .expect("budget-limited request failed");

        if budget_limited_resp.status() == StatusCode::TOO_MANY_REQUESTS {
            assert!(
                budget_limited_resp
                    .headers()
                    .get("x-stargate-retryable")
                    .is_none()
            );
            let metrics = fixture.metrics();
            assert_metric_sample(
                &metrics,
                r#"stargate_proxy_retry_exhausted_total{model="retry-model",reason="retry_budget_exhausted",routing_key=""} 1"#,
                true,
                "missing retry budget exhaustion counter",
            );
            assert_metric_sample(
                &metrics,
                r#"stargate_proxy_retry_exhausted_total{model="retry-model",reason="upstream_admission_rejected",routing_key=""}"#,
                false,
                "budget exhaustion should not also count upstream reason",
            );
            break;
        }

        assert!(
            tokio::time::Instant::now() < budget_deadline,
            "zero retry budget should return the retryable rejection without retrying"
        );
        poll.tick().await;
    }

    let reject_hits_before_retry = reject_backend.hits();
    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let resp = proxy_json_request(
            &http_client,
            fixture.http_addr,
            "/v1/chat/completions",
            "retry-model",
            "req-retryable-rejection",
            &body,
        )
        .send()
        .await
        .expect("request failed");

        if resp.status().is_success()
            && response_header(&resp, "x-inference-server-id") == Some("retry-success")
            && reject_backend.hits() > reject_hits_before_retry
        {
            assert!(resp.headers().get("x-stargate-retryable").is_none());
            break;
        }

        assert!(
            tokio::time::Instant::now() < deadline,
            "retryable rejection did not retry to alternate backend"
        );
        poll.tick().await;
    }

    let metrics = fixture.metrics();
    for (sample, context) in [
        (
            r#"stargate_proxy_retries_total{model="retry-model",reason="upstream_admission_rejected",routing_key=""}"#,
            "missing retry counter",
        ),
        (
            r#"stargate_proxy_attempts_total{inference_server_id="retry-reject",model="retry-model",result="upstream_429",routing_key=""}"#,
            "missing rejecting attempt counter",
        ),
        (
            r#"stargate_proxy_attempts_total{inference_server_id="retry-success",model="retry-model",result="upstream_200",routing_key=""}"#,
            "missing success attempt counter",
        ),
        (
            r#"stargate_requests_total{inference_server_id="retry-reject",model="retry-model",routing_key="",status="429"} 1"#,
            "hidden retryable attempt should not increment request counter",
        ),
        (
            r#"stargate_requests_total{inference_server_id="retry-success",model="retry-model",routing_key="",status="200"}"#,
            "missing final success request counter",
        ),
        (
            r#"stargate_proxy_replay_buffer_bytes_count{model="retry-model"}"#,
            "missing replay buffer histogram",
        ),
    ] {
        assert_metric_sample(&metrics, sample, true, context);
    }

    fixture.shutdown().await;
}

#[tokio::test]
async fn queue_estimate_mismatch_retries_alternate_backend_before_upstream() {
    let mut fixture = ProxyFixture::start("test-sg-queue-mismatch-retry").await;
    let reject_runtime_state = active_runtime("queue-mismatch-model");
    set_model_queue(&reject_runtime_state, "queue-mismatch-model", 0);
    observe_connecting_request(
        &reject_runtime_state,
        "queue-mismatch-model",
        "req-already-queued".to_string(),
        100,
    );
    let reject_backend = fixture
        .add_capturing_direct_backend(
            "queue-mismatch-reject",
            "",
            reject_runtime_state.clone(),
            false,
        )
        .await;

    let success_runtime_state = active_runtime("queue-mismatch-model");
    fixture
        .add_dummy_backend(
            "queue-mismatch-success",
            "",
            "queue-mismatch-model",
            success_runtime_state.clone(),
        )
        .await;

    set_model_queue(&reject_runtime_state, "queue-mismatch-model", 0);
    set_model_queue(&success_runtime_state, "queue-mismatch-model", 1000);
    wait_for_routing(
        fixture.http_addr,
        "queue-mismatch-model",
        Duration::from_secs(5),
    )
    .await;

    let http_client = reqwest::Client::new();
    let body = streaming_chat_body("queue-mismatch-model");

    let before_budget_metrics = fixture.metrics();
    let budget_deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let budget_limited_resp = proxy_json_request(
            &http_client,
            fixture.http_addr,
            "/v1/chat/completions",
            "queue-mismatch-model",
            "req-queue-mismatch-budget-zero",
            &body,
        )
        .header("x-stargate-max-wait-ms", "0")
        .send()
        .await
        .expect("budget-limited queue mismatch request failed");

        let status = budget_limited_resp.status();
        let headers = budget_limited_resp.headers().clone();
        let response_text = budget_limited_resp
            .text()
            .await
            .expect("budget-limited queue mismatch body should be readable");
        let metrics = fixture.metrics();
        if status == StatusCode::TOO_MANY_REQUESTS
            && metrics.contains(
                r#"stargate_proxy_retry_exhausted_total{model="queue-mismatch-model",reason="retry_budget_exhausted",routing_key=""} 1"#,
            )
        {
            assert!(headers.get("x-stargate-retryable").is_none());
            assert!(
                response_text.contains("queue_estimate_mismatch"),
                "final queue mismatch body should preserve the upstream reason: {response_text}"
            );
            assert_eq!(
                reject_backend.hits(),
                0,
                "queue mismatch retry-budget exhaustion should still reject before upstream"
            );
            assert!(
                metrics.contains(
                    r#"stargate_proxy_attempts_total{inference_server_id="queue-mismatch-reject",model="queue-mismatch-model",result="upstream_429",routing_key=""}"#
                ),
                "missing queue mismatch attempt counter:\n{metrics}"
            );
            assert_delta!(
                &before_budget_metrics, &metrics, "stargate_proxy_retries_total", 0.0;
                r#"model="queue-mismatch-model""#,
                r#"reason="queue_estimate_mismatch""#,
                r#"routing_key="""#
            );
            assert_delta!(
                &before_budget_metrics, &metrics, "stargate_proxy_attempts_total", 0.0;
                r#"inference_server_id="queue-mismatch-success""#,
                r#"model="queue-mismatch-model""#,
                r#"result="upstream_200""#,
                r#"routing_key="""#
            );
            let rejected_snapshot = fixture
                .handle
                .state()
                .cluster_candidates_for_target(&RoutingTargetKey {
                    routing_key: None,
                    model_id: "queue-mismatch-model".to_string(),
                })
                .await
                .into_iter()
                .find(|candidate| {
                    candidate.cluster_id.as_str() == "queue-mismatch-reject"
                })
                .expect("rejected backend cluster should remain routable");
            assert_eq!(
                rejected_snapshot.stats.queued_input_size, 0,
                "pre-upstream queue mismatch rejection must release its optimistic prompt reservation"
            );
            break;
        }

        assert!(
            tokio::time::Instant::now() < budget_deadline,
            "zero retry budget should return queue mismatch rejection without retrying"
        );
        poll.tick().await;
    }

    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let resp = proxy_json_request(
            &http_client,
            fixture.http_addr,
            "/v1/chat/completions",
            "queue-mismatch-model",
            "req-queue-mismatch-retry",
            &body,
        )
        .send()
        .await
        .expect("request failed");

        let metrics = fixture.metrics();
        if resp.status().is_success()
            && response_header(&resp, "x-inference-server-id") == Some("queue-mismatch-success")
            && metrics.contains(
                r#"stargate_proxy_retries_total{model="queue-mismatch-model",reason="queue_estimate_mismatch",routing_key=""}"#,
            )
        {
            assert_eq!(reject_backend.hits(), 0);
            break;
        }

        assert!(
            tokio::time::Instant::now() < deadline,
            "queue mismatch did not retry to alternate backend"
        );
        poll.tick().await;
    }

    fixture.shutdown().await;
}

#[tokio::test]
async fn queue_estimate_mismatch_retries_sibling_in_selected_shared_cluster() {
    let mut fixture = ProxyFixture::start("test-sg-shared-cluster-queue-mismatch-retry").await;
    let reject_runtime_state = active_runtime("queue-mismatch-shared-model");
    set_model_queue(&reject_runtime_state, "queue-mismatch-shared-model", 0);
    observe_connecting_request(
        &reject_runtime_state,
        "queue-mismatch-shared-model",
        "req-shared-already-queued".to_string(),
        100,
    );
    let reject_backend = fixture
        .add_capturing_direct_backend(
            "queue-mismatch-a-reject",
            "queue-mismatch-shared-cluster",
            reject_runtime_state.clone(),
            false,
        )
        .await;
    let success_runtime_state = active_runtime("queue-mismatch-shared-model");
    fixture
        .add_dummy_backend(
            "queue-mismatch-b-success",
            "queue-mismatch-shared-cluster",
            "queue-mismatch-shared-model",
            success_runtime_state.clone(),
        )
        .await;

    for runtime_state in [&reject_runtime_state, &success_runtime_state] {
        set_model_queue(runtime_state, "queue-mismatch-shared-model", 0);
    }

    let target = RoutingTargetKey {
        routing_key: None,
        model_id: "queue-mismatch-shared-model".to_string(),
    };
    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let candidates = fixture
            .handle
            .state()
            .cluster_candidates_for_target(&target)
            .await;
        if candidates.len() == 1
            && candidates[0].cluster_id == "queue-mismatch-shared-cluster"
            && candidates[0].stats.queued_input_size == 0
        {
            break;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "shared queue-mismatch cluster did not become routable"
        );
        poll.tick().await;
    }

    let before_metrics = fixture.metrics();
    let http_client = reqwest::Client::new();
    let resp = proxy_json_request(
        &http_client,
        fixture.http_addr,
        "/v1/chat/completions",
        "queue-mismatch-shared-model",
        "req-shared-queue-mismatch-retry",
        &streaming_chat_body("queue-mismatch-shared-model"),
    )
    .send()
    .await
    .expect("request failed");

    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(
        response_header(&resp, "x-stargate-cluster-id"),
        Some("queue-mismatch-shared-cluster")
    );
    assert_eq!(
        response_header(&resp, "x-inference-server-id"),
        Some("queue-mismatch-b-success")
    );
    assert_eq!(
        reject_backend.hits(),
        0,
        "queue mismatch must reject before reaching the congested upstream"
    );
    let metrics = fixture.metrics();
    assert_delta!(
        &before_metrics, &metrics, "stargate_proxy_retries_total", 1.0;
        r#"model="queue-mismatch-shared-model""#,
        r#"reason="queue_estimate_mismatch""#,
        r#"routing_key="""#
    );
    assert_delta!(
        &before_metrics, &metrics, "stargate_proxy_attempts_total", 1.0;
        r#"inference_server_id="queue-mismatch-a-reject""#,
        r#"model="queue-mismatch-shared-model""#,
        r#"result="upstream_429""#,
        r#"routing_key="""#
    );
    assert_delta!(
        &before_metrics, &metrics, "stargate_proxy_attempts_total", 1.0;
        r#"inference_server_id="queue-mismatch-b-success""#,
        r#"model="queue-mismatch-shared-model""#,
        r#"result="upstream_200""#,
        r#"routing_key="""#
    );
    assert_delta!(
        &before_metrics, &metrics, "stargate_routing_selections_total", 1.0;
        r#"algorithm="power-of-two""#,
        r#"model="queue-mismatch-shared-model""#,
        r#"routing_key="""#,
        r#"selection="primary""#
    );

    fixture.shutdown().await;
}

#[tokio::test]
async fn closed_direct_quic_connection_recovers_on_hot_path() {
    let (grpc_addr, http_addr, handle) = start_stargate("test-sg-hotpath-reconnect").await;

    let (inst_addr, quic_url, tunnel) = start_dummy_inst("reconnect-model").await;
    let tunnel_addr = tunnel.listen_addr();

    let mut reg_client = start_registration(
        active_stale_connection_registration_config(
            grpc_addr,
            "reconnect-inst",
            quic_url,
            format!("http://{inst_addr}"),
            "reconnect-model",
        ),
        "registration failed",
    );

    wait_for_routing(http_addr, "reconnect-model", Duration::from_secs(5)).await;
    tunnel.shutdown().await;
    let replacement_tunnel = {
        let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
        let mut poll = tokio::time::interval(Duration::from_millis(50));
        loop {
            match start_quic_http_tunnel(QuicHttpTunnelConfig::new(
                tunnel_addr,
                format!("http://{inst_addr}"),
            ))
            .await
            {
                Ok(tunnel) => break tunnel,
                Err(error) if tokio::time::Instant::now() < deadline => {
                    tracing::debug!(error = %error, "replacement tunnel bind not ready");
                    poll.tick().await;
                }
                Err(error) => panic!("replacement tunnel failed to start: {error}"),
            }
        }
    };

    let http_client = reqwest::Client::new();
    let body = streaming_chat_body("reconnect-model");

    let resp = proxy_json_request(
        &http_client,
        http_addr,
        "/v1/chat/completions",
        "reconnect-model",
        "req-hotpath-reconnect",
        &body,
    )
    .send()
    .await
    .expect("request failed");

    assert_eq!(resp.status(), 200);
    assert_eq!(
        response_header(&resp, "x-inference-server-id"),
        Some("reconnect-inst")
    );

    let metrics = metrics_text(handle.metrics().registry());
    assert!(
        metrics.contains(
            r#"stargate_quic_connection_evictions_total{inference_server_id="reconnect-inst",reason="stale_connection"} 1"#
        ),
        "missing connection eviction counter in metrics:\n{metrics}"
    );
    assert!(
        metrics.contains(
            r#"stargate_quic_hot_path_reconnect_total{inference_server_id="reconnect-inst",result="success"} 1"#
        ),
        "missing hot-path reconnect counter in metrics:\n{metrics}"
    );

    reg_client.stop();
    replacement_tunnel.shutdown().await;
    finish_stargate(handle).await;
}

#[tokio::test]
async fn replay_body_over_limit_returns_413() {
    let retry = ProxyRetryConfig {
        max_replay_body_bytes: 8,
        ..ProxyRetryConfig::default()
    };
    let fixture = ProxyFixture::start_with_retry("test-sg-replay-limit", retry).await;

    let resp = proxy_request(
        &reqwest::Client::new(),
        fixture.http_addr,
        "/v1/chat/completions",
        "oversized-model",
        "req-replay-over-limit",
    )
    .body(r#"{"stream":true}"#)
    .send()
    .await
    .expect("request failed");

    assert_eq!(resp.status(), StatusCode::PAYLOAD_TOO_LARGE);

    fixture.shutdown().await;
}

#[tokio::test]
async fn chunked_replay_overflow_records_413_request_metric() {
    let retry = ProxyRetryConfig {
        max_replay_body_bytes: 8,
        ..ProxyRetryConfig::default()
    };
    let mut fixture = ProxyFixture::start_with_retry("test-sg-chunked-replay-limit", retry).await;
    fixture
        .add_retryable_backend("chunk-overflow-reject", "chunk-overflow-model")
        .await;

    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let chunked_body = reqwest::Body::wrap_stream(async_stream::stream! {
            yield Ok::<_, std::io::Error>(Bytes::from_static(br#"{"stream""#));
            yield Ok::<_, std::io::Error>(Bytes::from_static(br#":true}"#));
        });
        let resp = proxy_request(
            &reqwest::Client::new(),
            fixture.http_addr,
            "/v1/chat/completions",
            "chunk-overflow-model",
            "req-chunked-replay-overflow",
        )
        .body(chunked_body)
        .send()
        .await
        .expect("request failed");

        let metrics = fixture.metrics();
        if resp.status() == StatusCode::PAYLOAD_TOO_LARGE
            && metrics.contains(
                r#"stargate_proxy_attempts_total{inference_server_id="chunk-overflow-reject",model="chunk-overflow-model",result="upstream_429",routing_key=""}"#,
            )
        {
            assert!(
                metrics.contains(
                    r#"stargate_requests_total{inference_server_id="chunk-overflow-reject",model="chunk-overflow-model",routing_key="",status="413"} 1"#
                ),
                "missing final 413 request counter:\n{metrics}"
            );
            break;
        }

        assert!(
            tokio::time::Instant::now() < deadline,
            "chunked replay overflow should return 413 after a retryable upstream attempt"
        );
        poll.tick().await;
    }

    fixture.shutdown().await;
}

#[tokio::test]
async fn retryable_single_backend_exhausts_eligible_backends() {
    let mut fixture = ProxyFixture::start("test-sg-retry-single-exhaust").await;
    fixture
        .add_retryable_backend("single-reject", "single-exhaust-model")
        .await;

    let metrics = poll_until(
        "single retryable backend should exhaust eligible backends",
        Duration::from_secs(15),
        || {
            let request = fixture.chat_request("single-exhaust-model", "req-single-exhaust");
            let registry = fixture.handle.metrics().registry();
            async move {
                let response = request.send().await.expect("request failed");
                let metrics = metrics_text(registry);
                (response.status() == StatusCode::SERVICE_UNAVAILABLE
                    && metrics.contains(
                        r#"stargate_proxy_attempts_total{inference_server_id="single-reject",model="single-exhaust-model",result="upstream_429",routing_key=""}"#,
                    ))
                .then_some(metrics)
            }
        },
    )
    .await;
    assert_metric_sample(
        &metrics,
        r#"stargate_proxy_retry_exhausted_total{model="single-exhaust-model",reason="no_eligible_backend",routing_key=""} 1"#,
        true,
        "missing retry exhaustion metric",
    );

    fixture.shutdown().await;
}

#[tokio::test]
async fn request_retry_limit_returns_last_retryable_rejection() {
    let retry = ProxyRetryConfig {
        max_request_retries: 1,
        ..ProxyRetryConfig::default()
    };
    let mut fixture = ProxyFixture::start_with_retry("test-sg-request-retry-limit", retry).await;
    fixture
        .add_retryable_backend("retry-limit-a", "retry-limit-model")
        .await;
    fixture
        .add_retryable_backend("retry-limit-b", "retry-limit-model")
        .await;

    let response = poll_until(
        "request retry limit should return the final retryable rejection",
        Duration::from_secs(15),
        || {
            let request = fixture.chat_request("retry-limit-model", "req-retry-limit");
            async move {
                let response = request.send().await.expect("request failed");
                (response.status() == StatusCode::TOO_MANY_REQUESTS).then_some(response)
            }
        },
    )
    .await;
    assert!(response.headers().get("x-stargate-retryable").is_none());
    assert_metric_sample(
        &fixture.metrics(),
        r#"stargate_proxy_retry_exhausted_total{model="retry-limit-model",reason="upstream_admission_rejected",routing_key=""} 1"#,
        true,
        "missing request retry exhausted metric",
    );

    fixture.shutdown().await;
}

#[tokio::test]
async fn zero_connect_retries_returns_proxy_error_without_reconnect() {
    init_crypto();

    let retry = ProxyRetryConfig {
        max_connect_retries: 0,
        ..ProxyRetryConfig::default()
    };
    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_retry("test-sg-connect-retry-zero", retry);
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, tunnel) = start_dummy_inst("connect-zero-model").await;
    let mut reg_client = start_registration(
        active_stale_connection_registration_config(
            grpc_addr,
            "connect-zero-inst",
            quic_url,
            format!("http://{inst_addr}"),
            "connect-zero-model",
        ),
        "registration failed",
    );

    wait_for_routing(http_addr, "connect-zero-model", Duration::from_secs(5)).await;
    tunnel.shutdown().await;

    let http_client = reqwest::Client::new();
    let body = streaming_chat_body("connect-zero-model");

    poll_until(
        "zero connect retries should return a proxy error after tunnel closes",
        Duration::from_secs(15),
        || {
            let request = proxy_json_request(
                &http_client,
                http_addr,
                "/v1/chat/completions",
                "connect-zero-model",
                "req-connect-zero",
                &body,
            );
            async move {
                let response = request.send().await.expect("request failed");
                (response.status() == StatusCode::BAD_GATEWAY).then_some(())
            }
        },
    )
    .await;
    let metrics = metrics_text(handle.metrics().registry());
    assert_metric_sample(
        &metrics,
        r#"stargate_proxy_retry_exhausted_total{model="connect-zero-model",reason="connect_retries_exhausted",routing_key=""} 1"#,
        true,
        "missing connect retry exhausted metric",
    );
    assert_metric_sample(
        &metrics,
        r#"stargate_quic_hot_path_reconnect_total{inference_server_id="connect-zero-inst""#,
        false,
        "zero connect retries should not attempt hot-path reconnect",
    );

    reg_client.stop();
    finish_stargate(handle).await;
}

#[tokio::test]
async fn pulsar_missing_cache_affinity_header_returns_400() {
    PulsarHeaderFixture::start("test-sg-pulsar-missing-affinity")
        .await
        .assert_missing_header("req-no-affinity", ("x-input-tokens", "1"))
        .await;
}

#[tokio::test]
async fn pulsar_missing_input_tokens_header_returns_400() {
    PulsarHeaderFixture::start("test-sg-pulsar-missing-input")
        .await
        .assert_missing_header("req-no-input-tokens", ("x-cache-affinity-key", "prefix-a"))
        .await;
}

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

use crate::common::{
    DummyState, SelfDiscovery, base_config, bind_ephemeral, dummy_chat, init_crypto,
    make_stargate_runtime, make_stargate_runtime_with_lb, start_dummy_inst, wait_for_routing,
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
    BringupConfig, CurrentModelStats, InferenceServerRegistrationClient,
    InferenceServerRegistrationConfig, OutputTokenParserFactory, QueueAdmissionTracker,
    QuicHttpTunnelConfig, QuicHttpTunnelHandle, RequestObservation, RequestObservationEndpoint,
    RequestObservationState, start_quic_http_tunnel,
};
use stargate::proxy::ProxyRetryConfig;
use stargate::routing::RoutingTargetKey;
use stargate::runtime::StargateRuntime;
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

fn metrics_text(registry: Arc<prometheus::Registry>) -> String {
    let metric_families = registry.gather();
    let mut buffer = Vec::new();
    TextEncoder::new()
        .encode(&metric_families, &mut buffer)
        .expect("encode metrics");
    String::from_utf8(buffer).expect("metrics must be utf8")
}

fn metric_sample_value(metrics: &str, metric_name: &str, label_fragments: &[&str]) -> Option<f64> {
    metrics.lines().find_map(|line| {
        let (sample, value) = line.rsplit_once(' ')?;
        let name = sample.split('{').next().unwrap_or(sample);
        if name == metric_name && label_fragments.iter().all(|label| sample.contains(label)) {
            value.parse().ok()
        } else {
            None
        }
    })
}

fn make_stargate_runtime_with_retry(
    id: &str,
    retry: ProxyRetryConfig,
) -> (std::net::SocketAddr, std::net::SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SelfDiscovery::new(id, grpc_addr, http_addr);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.proxy_transport.retry = retry;
    let runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_http_listener(http_listener);
    (grpc_addr, http_addr, runtime)
}

async fn start_embeddings_inst(
    response_body: &'static str,
) -> (
    std::net::SocketAddr,
    String,
    QuicHttpTunnelHandle,
    Arc<std::sync::Mutex<Option<EmbeddingsBackendCapture>>>,
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
                async move {
                    let path_and_query = req
                        .uri()
                        .path_and_query()
                        .map(|value| value.as_str().to_string())
                        .unwrap_or_else(|| "/v1/embeddings".to_string());
                    let model_header = req
                        .headers()
                        .get("x-model")
                        .and_then(|value| value.to_str().ok())
                        .map(ToOwned::to_owned);
                    let request_id_header = req
                        .headers()
                        .get("x-request-id")
                        .and_then(|value| value.to_str().ok())
                        .map(ToOwned::to_owned);
                    let input_tokens_header = req
                        .headers()
                        .get("x-input-tokens")
                        .and_then(|value| value.to_str().ok())
                        .map(ToOwned::to_owned);
                    let body = axum::body::to_bytes(req.into_body(), 1024 * 1024)
                        .await
                        .expect("embedding request body should be readable");
                    *capture.lock().expect("capture mutex poisoned") =
                        Some(EmbeddingsBackendCapture {
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
) -> InferenceServerRegistrationConfig {
    active_registration_config_in_cluster(
        grpc_addr,
        inference_server_id,
        "",
        inference_server_url,
        upstream_http_base_url,
    )
}

fn active_registration_config_in_cluster(
    grpc_addr: std::net::SocketAddr,
    inference_server_id: &str,
    cluster_id: &str,
    inference_server_url: String,
    upstream_http_base_url: String,
) -> InferenceServerRegistrationConfig {
    InferenceServerRegistrationConfig {
        seeds: vec![grpc_addr.to_string()],
        inference_server_id: inference_server_id.to_string(),
        cluster_id: cluster_id.to_string(),
        inference_server_url,
        upstream_http_base_url: Some(upstream_http_base_url),
        min_update_interval: Duration::from_millis(100),
        status: InferenceServerStatus::Active,
        reverse_tunnel: false,
        bringup: BringupConfig {
            enabled: false,
            ..BringupConfig::default()
        },
        output_token_parser_factory: OutputTokenParserFactory::vllm(),
        request_observation_tx: None,
        request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
        metrics: None,
        retry: pylon_lib::PylonRetryConfig::default(),
        queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
        queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
        auth_token_provider: None,
        quic_insecure: true,
        tunnel_protocol: Default::default(),
    }
}

#[tokio::test]
async fn chat_completions_route_proxies_path_query_and_body_through_quic_tunnel() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-chat-contract");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_responses_inst("chat-contract-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "chat-contract-inst",
                quic_url.clone(),
                format!("http://{inst_addr}"),
            ),
            vec!["chat-contract-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "chat-contract-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions?trace=chat");
    let body = serde_json::json!({
        "model": "chat-contract-model",
        "messages": [{"role": "user", "content": "contract hello"}],
        "max_tokens": 3,
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "chat-contract-model",
        "req-chat-contract",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("chat contract request failed");
    assert_eq!(resp.status(), 200);
    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id")
            .to_str()
            .unwrap(),
        "chat-contract-inst"
    );
    assert_eq!(
        resp.headers()
            .get("x-inference-server-url")
            .expect("missing x-inference-server-url")
            .to_str()
            .unwrap(),
        quic_url
    );
    assert_eq!(
        resp.headers()
            .get("x-stargate-cluster-id")
            .expect("missing x-stargate-cluster-id")
            .to_str()
            .unwrap(),
        "chat-contract-inst"
    );
    let response_text = resp.text().await.expect("response should be text");
    assert!(response_text.contains(r#""object":"chat.completion.chunk""#));
    assert!(response_text.contains(r#""model":"chat-contract-model""#));
    assert!(response_text.contains(r#""path_and_query":"/v1/chat/completions?trace=chat""#));
    assert!(response_text.contains(r#""content":"contract echo""#));
    assert!(response_text.contains(r#""content":"contract hello""#));
    assert!(response_text.contains(r#""stream":true"#));
    assert!(response_text.contains("[DONE]"));

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn chat_completions_route_forwards_upstream_error_through_quic_tunnel() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-chat-error");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_responses_inst("chat-error-model").await;
    let expected_url = quic_url.clone();

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "chat-error-inst",
                quic_url,
                format!("http://{inst_addr}"),
            ),
            vec!["chat-error-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "chat-error-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions?fail=1");
    let body = serde_json::json!({
        "model": "chat-error-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "chat-error-model",
        "req-chat-error",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("chat error request failed");
    assert_eq!(resp.status(), StatusCode::SERVICE_UNAVAILABLE);
    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id")
            .to_str()
            .unwrap(),
        "chat-error-inst"
    );
    assert_eq!(
        resp.headers()
            .get("x-inference-server-url")
            .expect("missing x-inference-server-url")
            .to_str()
            .unwrap(),
        expected_url
    );
    assert_eq!(
        resp.headers()
            .get("x-stargate-cluster-id")
            .expect("missing x-stargate-cluster-id")
            .to_str()
            .unwrap(),
        "chat-error-inst"
    );

    let response_json: serde_json::Value = resp.json().await.expect("response should be json");
    assert_eq!(response_json["error"], "chat completions unavailable");

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn responses_route_proxies_path_and_query_through_quic_tunnel() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-responses");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_responses_inst("responses-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "responses-inst",
                quic_url.clone(),
                format!("http://{inst_addr}"),
            ),
            vec!["responses-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "responses-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/responses?trace=1");
    let body = serde_json::json!({
        "model": "responses-model",
        "input": "hello",
        "max_output_tokens": 2,
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "responses-model",
        "req-responses",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("responses request failed");
    assert_eq!(resp.status(), 200);

    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id")
            .to_str()
            .unwrap(),
        "responses-inst"
    );
    assert_eq!(
        resp.headers()
            .get("x-inference-server-url")
            .expect("missing x-inference-server-url")
            .to_str()
            .unwrap(),
        quic_url
    );
    assert_eq!(
        resp.headers()
            .get("x-stargate-cluster-id")
            .expect("missing x-stargate-cluster-id")
            .to_str()
            .unwrap(),
        "responses-inst"
    );
    let response_text = resp.text().await.expect("response should be text");
    assert!(response_text.contains("event: response.created"));
    assert!(response_text.contains("event: response.completed"));
    assert!(response_text.contains(r#""type":"response.completed""#));
    assert!(response_text.contains(r#""object":"response""#));
    assert!(response_text.contains(r#""model":"responses-model""#));
    assert!(response_text.contains(r#""path_and_query":"/v1/responses?trace=1""#));
    assert!(response_text.contains(r#""input":"hello""#));
    assert!(response_text.contains(r#""stream":true"#));

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn responses_route_forwards_upstream_error_through_quic_tunnel() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-responses-error");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_responses_inst("responses-error-model").await;
    let expected_url = quic_url.clone();

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "responses-error-inst",
                quic_url,
                format!("http://{inst_addr}"),
            ),
            vec!["responses-error-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "responses-error-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/responses?fail=1");
    let body = serde_json::json!({
        "model": "responses-error-model",
        "input": "hello",
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "responses-error-model",
        "req-responses-error",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("responses request failed");
    assert_eq!(resp.status(), StatusCode::SERVICE_UNAVAILABLE);
    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id")
            .to_str()
            .unwrap(),
        "responses-error-inst"
    );
    assert_eq!(
        resp.headers()
            .get("x-inference-server-url")
            .expect("missing x-inference-server-url")
            .to_str()
            .unwrap(),
        expected_url
    );
    assert_eq!(
        resp.headers()
            .get("x-stargate-cluster-id")
            .expect("missing x-stargate-cluster-id")
            .to_str()
            .unwrap(),
        "responses-error-inst"
    );

    let response_json: serde_json::Value = resp.json().await.expect("response should be json");
    assert_eq!(response_json["error"], "responses unavailable");

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// The QUIC tunnel enforces that `/v1/responses` requests must set
/// `"stream": true` in the body. Non-streaming requests are rejected with 400.
#[tokio::test]
async fn non_streaming_responses_rejected_by_quic_tunnel() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-responses-nonstream");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_responses_inst("responses-ns-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "responses-ns-inst",
                quic_url,
                format!("http://{inst_addr}"),
            ),
            vec!["responses-ns-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "responses-ns-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/responses");
    let body = serde_json::json!({
        "model": "responses-ns-model",
        "input": "hello",
        "stream": false,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "responses-ns-model",
        "req-responses-nonstream",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("non-streaming responses request failed");

    assert_eq!(
        resp.status(),
        StatusCode::BAD_REQUEST,
        "non-streaming responses should be rejected by the QUIC tunnel"
    );
    let response_text = resp.text().await.expect("response should be text");
    assert!(response_text.contains("/v1/responses"));
    assert!(response_text.contains("stream=true"));

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn embeddings_proxy_forwards_opaque_body() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-embeddings");
    let handle = runtime.start().await.expect("stargate failed to start");

    let embedding_response = r#"{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],"model":"embedding-model","usage":{"prompt_tokens":4,"total_tokens":4}}"#;
    let (inst_addr, quic_url, tunnel, capture) = start_embeddings_inst(embedding_response).await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "embedding-inst",
                quic_url.clone(),
                format!("http://{inst_addr}"),
            ),
            vec!["embedding-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "embedding-model", Duration::from_secs(5)).await;

    let body = br#"{"model":"embedding-model","input":["alpha","beta"],"encoding_format":"float"}"#;
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/embeddings?trace=1");

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "embedding-model",
        "req-embedding-proxy",
    )
    .header("content-type", "application/json")
    .body(Bytes::from_static(body))
    .send()
    .await
    .expect("embedding request failed");

    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .and_then(|value| value.to_str().ok()),
        Some("embedding-inst")
    );
    assert_eq!(
        resp.headers()
            .get("x-inference-server-url")
            .and_then(|value| value.to_str().ok()),
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
    assert_eq!(captured.path_and_query, "/v1/embeddings?trace=1");
    assert_eq!(captured.body, Bytes::from_static(body));
    assert_eq!(captured.model_header.as_deref(), Some("embedding-model"));
    assert_eq!(
        captured.request_id_header.as_deref(),
        Some("req-embedding-proxy")
    );
    assert_eq!(captured.input_tokens_header.as_deref(), Some("1"));

    reg_client.stop();
    tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn embeddings_missing_model_header_returns_400() {
    init_crypto();

    let (_grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-embeddings-no-model");
    let handle = runtime.start().await.expect("stargate failed to start");

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/embeddings");
    let resp = http_client
        .post(&stargate_url)
        .header("x-request-id", "req-embedding-no-model")
        .header("x-input-tokens", "1")
        .header("content-type", "application/json")
        .body(r#"{"model":"embedding-model","input":"hello"}"#)
        .send()
        .await
        .expect("embedding request failed");

    assert_eq!(resp.status(), StatusCode::BAD_REQUEST);

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn embeddings_missing_input_tokens_returns_400_without_upstream() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime("test-sg-embeddings-no-input-tokens");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, tunnel, capture) =
        start_embeddings_inst(r#"{"unexpected":true}"#).await;
    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "embedding-no-input-inst",
                quic_url,
                format!("http://{inst_addr}"),
            ),
            vec!["embedding-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "embedding-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/embeddings");
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

    reg_client.stop();
    tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// The QUIC tunnel enforces that `/v1/chat/completions` requests must set
/// `"stream": true` in the body. Non-streaming requests are rejected with 400.
#[tokio::test]
async fn non_streaming_chat_completions_rejected_by_quic_tunnel() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-nonstream");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("ns-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "ns-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: quic_url,
                upstream_http_base_url: Some(format!("http://{inst_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig::default(),
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["ns-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "ns-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "ns-model",
        "messages": [{"role": "user", "content": "hi"}],
    });

    let resp = with_proxy_headers(http_client.post(&stargate_url), "ns-model", "req-nonstream")
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("non-streaming request failed");

    assert_eq!(
        resp.status(),
        400,
        "non-streaming chat completions should be rejected by the QUIC tunnel"
    );

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn missing_model_header_returns_400() {
    init_crypto();

    let (_grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-noheader");
    let handle = runtime.start().await.expect("stargate failed to start");

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "any-model",
        "messages": [{"role": "user", "content": "hi"}],
    });

    let resp = http_client
        .post(&stargate_url)
        .header("x-request-id", "req-noheader")
        .header("x-input-tokens", "1")
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
    assert_eq!(resp.status(), 400, "missing x-model should return 400");

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn supported_endpoint_required_proxy_headers_are_enforced() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-required-headers");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_responses_inst("required-header-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "required-header-inst",
                quic_url,
                format!("http://{inst_addr}"),
            ),
            vec!["required-header-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "required-header-model", Duration::from_secs(5)).await;

    let endpoints = [
        (
            "/v1/chat/completions",
            serde_json::json!({
                "model": "required-header-model",
                "messages": [{"role": "user", "content": "hi"}],
                "stream": true,
            }),
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
                .post(format!("http://{http_addr}{endpoint}"))
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

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn response_headers_contain_server_id_and_url() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-headers");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("hdr-model").await;
    let expected_url = quic_url.clone();

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "hdr-inst".to_string(),
                cluster_id: "hdr-cluster".to_string(),
                inference_server_url: quic_url,
                upstream_http_base_url: Some(format!("http://{inst_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig::default(),
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["hdr-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "hdr-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "hdr-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let resp = with_proxy_headers(http_client.post(&stargate_url), "hdr-model", "req-headers")
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
    assert_eq!(resp.status(), 200);

    let server_id = resp
        .headers()
        .get("x-inference-server-id")
        .expect("missing x-inference-server-id")
        .to_str()
        .unwrap();
    assert_eq!(server_id, "hdr-inst");

    let server_url = resp
        .headers()
        .get("x-inference-server-url")
        .expect("missing x-inference-server-url")
        .to_str()
        .unwrap();
    assert_eq!(server_url, expected_url);

    let cluster_id = resp
        .headers()
        .get("x-stargate-cluster-id")
        .expect("missing x-stargate-cluster-id")
        .to_str()
        .unwrap();
    assert_eq!(cluster_id, "hdr-cluster");

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn shared_cluster_round_robins_selected_backend_header() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-shared-cluster-rr");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_a_addr, quic_a, tunnel_a) = start_dummy_inst("shared-cluster-model").await;
    let (inst_b_addr, quic_b, tunnel_b) = start_dummy_inst("shared-cluster-model").await;
    let mut reg_a = InferenceServerRegistrationClient::default();
    let mut reg_b = InferenceServerRegistrationClient::default();
    let _channels_a = reg_a
        .start(
            active_registration_config_in_cluster(
                grpc_addr,
                "shared-backend-a",
                "shared-cluster",
                quic_a,
                format!("http://{inst_a_addr}"),
            ),
            vec!["shared-cluster-model".to_string()],
        )
        .expect("registration a failed");
    let _channels_b = reg_b
        .start(
            active_registration_config_in_cluster(
                grpc_addr,
                "shared-backend-b",
                "shared-cluster",
                quic_b,
                format!("http://{inst_b_addr}"),
            ),
            vec!["shared-cluster-model".to_string()],
        )
        .expect("registration b failed");

    wait_for_routing(http_addr, "shared-cluster-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "shared-cluster-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let mut seen = std::collections::HashSet::new();
    for i in 0..4 {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "shared-cluster-model",
            &format!("req-shared-cluster-{i}"),
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
        assert_eq!(resp.status(), 200);
        assert_eq!(
            resp.headers()
                .get("x-stargate-cluster-id")
                .expect("missing x-stargate-cluster-id")
                .to_str()
                .unwrap(),
            "shared-cluster"
        );
        seen.insert(
            resp.headers()
                .get("x-inference-server-id")
                .expect("missing x-inference-server-id")
                .to_str()
                .unwrap()
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

    reg_a.stop();
    reg_b.stop();
    tunnel_a.shutdown().await;
    tunnel_b.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
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

    let (good_addr, good_quic_url, good_tunnel) = start_dummy_inst("shared-failover-model").await;
    let (other_addr, other_quic_url, other_tunnel) =
        start_dummy_inst("shared-failover-model").await;

    let mut bad_reg = InferenceServerRegistrationClient::default();
    let bad_channels = bad_reg
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "shared-backend-a-bad".to_string(),
                cluster_id: "shared-failover-cluster".to_string(),
                inference_server_url: "quic://127.0.0.1:1".to_string(),
                upstream_http_base_url: Some("http://127.0.0.1:1".to_string()),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["shared-failover-model".to_string()],
        )
        .expect("bad registration failed");
    let mut good_reg = InferenceServerRegistrationClient::default();
    let good_channels = good_reg
        .start(
            active_registration_config_in_cluster(
                grpc_addr,
                "shared-backend-b-good",
                "shared-failover-cluster",
                good_quic_url,
                format!("http://{good_addr}"),
            ),
            vec!["shared-failover-model".to_string()],
        )
        .expect("good registration failed");
    let mut other_reg = InferenceServerRegistrationClient::default();
    let other_channels = other_reg
        .start(
            active_registration_config_in_cluster(
                grpc_addr,
                "other-cluster-backend",
                "other-cluster",
                other_quic_url,
                format!("http://{other_addr}"),
            ),
            vec!["shared-failover-model".to_string()],
        )
        .expect("other registration failed");

    wait_for_routing(http_addr, "shared-failover-model", Duration::from_secs(5)).await;

    for channels in [&bad_channels, &good_channels] {
        channels
            .model_stats
            .send_async((
                "shared-failover-model".to_string(),
                CurrentModelStats {
                    last_mean_input_tps: 1000.0,
                    queued_input_size: 0,
                    ..CurrentModelStats::default()
                },
            ))
            .await
            .expect("send shared-cluster stats failed");
    }
    other_channels
        .model_stats
        .send_async((
            "shared-failover-model".to_string(),
            CurrentModelStats {
                last_mean_input_tps: 1000.0,
                queued_input_size: 400,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("send other-cluster stats failed");

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "shared-failover-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

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
                let cluster_id = resp
                    .headers()
                    .get("x-stargate-cluster-id")
                    .and_then(|value| value.to_str().ok())
                    .map(str::to_string);
                let server_id = resp
                    .headers()
                    .get("x-inference-server-id")
                    .and_then(|value| value.to_str().ok())
                    .map(str::to_string);
                if cluster_id.as_deref() == Some("shared-failover-cluster")
                    && server_id.as_deref() == Some("shared-backend-b-good")
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

    bad_reg.stop();
    good_reg.stop();
    other_reg.stop();
    good_tunnel.shutdown().await;
    other_tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn unknown_model_returns_404_no_eligible_candidates() {
    init_crypto();

    let (_grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-unknown");
    let handle = runtime.start().await.expect("stargate failed to start");

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "nonexistent",
        "messages": [{"role": "user", "content": "hi"}],
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "nonexistent",
        "req-unknown",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("request failed");
    assert_eq!(
        resp.status(),
        StatusCode::NOT_FOUND,
        "unknown model with no candidates should return 404"
    );
    assert_eq!(
        resp.headers()
            .get("x-stargate-error-code")
            .and_then(|value| value.to_str().ok()),
        Some("no_eligible_candidates"),
        "no-candidates proxy errors should be distinguishable from upstream errors"
    );
    let body: serde_json::Value = resp
        .json()
        .await
        .expect("no-candidates response body should be json");
    assert_eq!(body["code"], "no_eligible_candidates");

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn retryable_upstream_rejection_retries_alternate_backend() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-retryable-rejection");
    let handle = runtime.start().await.expect("stargate failed to start");

    let reject_hits = Arc::new(AtomicUsize::new(0));
    let reject_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let reject_addr = reject_listener.local_addr().unwrap();
    let reject_hits_for_app = reject_hits.clone();
    let reject_app = Router::new()
        .route(
            "/v1/chat/completions",
            post(move |_req: Request| {
                let reject_hits = reject_hits_for_app.clone();
                async move {
                    reject_hits.fetch_add(1, Ordering::Relaxed);
                    let mut response = Response::new(Body::from(r#"{"error":"queue full"}"#));
                    *response.status_mut() = StatusCode::TOO_MANY_REQUESTS;
                    response.headers_mut().insert(
                        HeaderName::from_static("x-stargate-upstream-retryable"),
                        HeaderValue::from_static("true"),
                    );
                    response
                }
            }),
        )
        .route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        axum::serve(reject_listener, reject_app).await.unwrap();
    });
    let reject_tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{reject_addr}"),
    ))
    .await
    .expect("reject tunnel failed to start");
    let reject_quic_url = format!("quic://{}", reject_tunnel.listen_addr());

    let (success_addr, success_quic_url, success_tunnel) = start_dummy_inst("retry-model").await;

    let mut reject_reg = InferenceServerRegistrationClient::default();
    let reject_channels = reject_reg
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "retry-reject".to_string(),
                cluster_id: String::new(),
                inference_server_url: reject_quic_url,
                upstream_http_base_url: Some(format!("http://{reject_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["retry-model".to_string()],
        )
        .expect("reject registration failed");

    let mut success_reg = InferenceServerRegistrationClient::default();
    let success_channels = success_reg
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "retry-success".to_string(),
                cluster_id: String::new(),
                inference_server_url: success_quic_url,
                upstream_http_base_url: Some(format!("http://{success_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["retry-model".to_string()],
        )
        .expect("success registration failed");

    reject_channels
        .model_stats
        .send_async((
            "retry-model".to_string(),
            CurrentModelStats {
                last_mean_input_tps: 1000.0,
                queued_input_size: 0,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("send reject stats failed");
    success_channels
        .model_stats
        .send_async((
            "retry-model".to_string(),
            CurrentModelStats {
                last_mean_input_tps: 1000.0,
                queued_input_size: 1,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("send success stats failed");

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "retry-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let budget_deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let budget_limited_resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "retry-model",
            "req-retry-budget-zero",
        )
        .header("content-type", "application/json")
        .header("x-stargate-max-wait-ms", "0")
        .json(&body)
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
            let metrics = metrics_text(handle.metrics().registry());
            assert!(
                metrics.contains(
                    r#"stargate_proxy_retry_exhausted_total{model="retry-model",reason="retry_budget_exhausted",routing_key=""} 1"#
                ),
                "missing retry budget exhaustion counter in metrics:\n{metrics}"
            );
            assert!(
                !metrics.contains(
                    r#"stargate_proxy_retry_exhausted_total{model="retry-model",reason="upstream_admission_rejected",routing_key=""}"#
                ),
                "budget exhaustion should not also count upstream reason:\n{metrics}"
            );
            break;
        }

        assert!(
            tokio::time::Instant::now() < budget_deadline,
            "zero retry budget should return the retryable rejection without retrying"
        );
        poll.tick().await;
    }

    let reject_hits_before_retry = reject_hits.load(Ordering::Relaxed);
    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "retry-model",
            "req-retryable-rejection",
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");

        if resp.status().is_success()
            && resp
                .headers()
                .get("x-inference-server-id")
                .and_then(|value| value.to_str().ok())
                == Some("retry-success")
            && reject_hits.load(Ordering::Relaxed) > reject_hits_before_retry
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

    let metrics = metrics_text(handle.metrics().registry());
    assert!(
        metrics.contains(
            r#"stargate_proxy_retries_total{model="retry-model",reason="upstream_admission_rejected",routing_key=""}"#
        ),
        "missing retry counter in metrics:\n{metrics}"
    );
    assert!(
        metrics.contains(
            r#"stargate_proxy_attempts_total{inference_server_id="retry-reject",model="retry-model",result="upstream_429",routing_key=""}"#
        ),
        "missing rejecting attempt counter in metrics:\n{metrics}"
    );
    assert!(
        metrics.contains(
            r#"stargate_proxy_attempts_total{inference_server_id="retry-success",model="retry-model",result="upstream_200",routing_key=""}"#
        ),
        "missing success attempt counter in metrics:\n{metrics}"
    );
    assert!(
        metrics.contains(
            r#"stargate_requests_total{inference_server_id="retry-reject",model="retry-model",routing_key="",status="429"} 1"#
        ),
        "hidden retryable attempt should not increment request counter:\n{metrics}"
    );
    assert!(
        metrics.contains(
            r#"stargate_requests_total{inference_server_id="retry-success",model="retry-model",routing_key="",status="200"}"#
        ),
        "missing final success request counter:\n{metrics}"
    );
    assert!(
        metrics.contains(r#"stargate_proxy_replay_buffer_bytes_count{model="retry-model"}"#),
        "missing replay buffer histogram in metrics:\n{metrics}"
    );

    reject_reg.stop();
    success_reg.stop();
    reject_tunnel.shutdown().await;
    success_tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn queue_estimate_mismatch_retries_alternate_backend_before_upstream() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-queue-mismatch-retry");
    let handle = runtime.start().await.expect("stargate failed to start");

    let reject_hits = Arc::new(AtomicUsize::new(0));
    let reject_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let reject_addr = reject_listener.local_addr().unwrap();
    let reject_hits_for_app = reject_hits.clone();
    let reject_app = Router::new()
        .route(
            "/v1/chat/completions",
            post(move |_req: Request| {
                let reject_hits = reject_hits_for_app.clone();
                async move {
                    reject_hits.fetch_add(1, Ordering::Relaxed);
                    (StatusCode::OK, "unexpected queue mismatch upstream hit")
                }
            }),
        )
        .route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        axum::serve(reject_listener, reject_app).await.unwrap();
    });

    let queue_tracker = QueueAdmissionTracker::default();
    queue_tracker.update_model_throughput("queue-mismatch-model", 100.0);
    queue_tracker.record_observation(&RequestObservation {
        endpoint: RequestObservationEndpoint::ChatCompletions,
        request_id: "req-already-queued".to_string(),
        routing_key: None,
        model_id: "queue-mismatch-model".to_string(),
        priority: 0,
        input_tokens: 100,
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
    let mut reject_config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{reject_addr}"),
    );
    reject_config.queue_tracker = queue_tracker;
    let reject_tunnel = start_quic_http_tunnel(reject_config)
        .await
        .expect("queue mismatch tunnel failed to start");
    let reject_quic_url = format!("quic://{}", reject_tunnel.listen_addr());

    let (success_addr, success_quic_url, success_tunnel) =
        start_dummy_inst("queue-mismatch-model").await;

    let mut reject_reg = InferenceServerRegistrationClient::default();
    let reject_channels = reject_reg
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "queue-mismatch-reject".to_string(),
                cluster_id: String::new(),
                inference_server_url: reject_quic_url,
                upstream_http_base_url: Some(format!("http://{reject_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["queue-mismatch-model".to_string()],
        )
        .expect("reject registration failed");

    let mut success_reg = InferenceServerRegistrationClient::default();
    let success_channels = success_reg
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "queue-mismatch-success".to_string(),
                cluster_id: String::new(),
                inference_server_url: success_quic_url,
                upstream_http_base_url: Some(format!("http://{success_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["queue-mismatch-model".to_string()],
        )
        .expect("success registration failed");

    reject_channels
        .model_stats
        .send_async((
            "queue-mismatch-model".to_string(),
            CurrentModelStats {
                last_mean_input_tps: 1000.0,
                queued_input_size: 0,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("send reject stats failed");
    success_channels
        .model_stats
        .send_async((
            "queue-mismatch-model".to_string(),
            CurrentModelStats {
                last_mean_input_tps: 1000.0,
                queued_input_size: 1000,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("send success stats failed");
    wait_for_routing(http_addr, "queue-mismatch-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "queue-mismatch-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let before_budget_metrics = metrics_text(handle.metrics().registry());
    let before_budget_retries = metric_sample_value(
        &before_budget_metrics,
        "stargate_proxy_retries_total",
        &[
            r#"model="queue-mismatch-model""#,
            r#"reason="queue_estimate_mismatch""#,
            r#"routing_key="""#,
        ],
    )
    .unwrap_or_default();
    let before_budget_success_attempts = metric_sample_value(
        &before_budget_metrics,
        "stargate_proxy_attempts_total",
        &[
            r#"inference_server_id="queue-mismatch-success""#,
            r#"model="queue-mismatch-model""#,
            r#"result="upstream_200""#,
            r#"routing_key="""#,
        ],
    )
    .unwrap_or_default();

    let budget_deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let budget_limited_resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "queue-mismatch-model",
            "req-queue-mismatch-budget-zero",
        )
        .header("content-type", "application/json")
        .header("x-stargate-max-wait-ms", "0")
        .json(&body)
        .send()
        .await
        .expect("budget-limited queue mismatch request failed");

        let status = budget_limited_resp.status();
        let headers = budget_limited_resp.headers().clone();
        let response_text = budget_limited_resp
            .text()
            .await
            .expect("budget-limited queue mismatch body should be readable");
        let metrics = metrics_text(handle.metrics().registry());
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
                reject_hits.load(Ordering::Relaxed),
                0,
                "queue mismatch retry-budget exhaustion should still reject before upstream"
            );
            assert!(
                metrics.contains(
                    r#"stargate_proxy_attempts_total{inference_server_id="queue-mismatch-reject",model="queue-mismatch-model",result="upstream_429",routing_key=""}"#
                ),
                "missing queue mismatch attempt counter:\n{metrics}"
            );
            assert_eq!(
                metric_sample_value(
                    &metrics,
                    "stargate_proxy_retries_total",
                    &[
                        r#"model="queue-mismatch-model""#,
                        r#"reason="queue_estimate_mismatch""#,
                        r#"routing_key="""#,
                    ],
                )
                .unwrap_or_default(),
                before_budget_retries,
                "zero retry budget should not increment queue mismatch retries:\n{metrics}"
            );
            assert_eq!(
                metric_sample_value(
                    &metrics,
                    "stargate_proxy_attempts_total",
                    &[
                        r#"inference_server_id="queue-mismatch-success""#,
                        r#"model="queue-mismatch-model""#,
                        r#"result="upstream_200""#,
                        r#"routing_key="""#,
                    ],
                )
                .unwrap_or_default(),
                before_budget_success_attempts,
                "zero retry budget should not reach the alternate backend:\n{metrics}"
            );
            let rejected_snapshot = handle
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
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "queue-mismatch-model",
            "req-queue-mismatch-retry",
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");

        let metrics = metrics_text(handle.metrics().registry());
        if resp.status().is_success()
            && resp
                .headers()
                .get("x-inference-server-id")
                .and_then(|value| value.to_str().ok())
                == Some("queue-mismatch-success")
            && metrics.contains(
                r#"stargate_proxy_retries_total{model="queue-mismatch-model",reason="queue_estimate_mismatch",routing_key=""}"#,
            )
        {
            assert_eq!(reject_hits.load(Ordering::Relaxed), 0);
            break;
        }

        assert!(
            tokio::time::Instant::now() < deadline,
            "queue mismatch did not retry to alternate backend"
        );
        poll.tick().await;
    }

    reject_reg.stop();
    success_reg.stop();
    reject_tunnel.shutdown().await;
    success_tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn queue_estimate_mismatch_retries_sibling_in_selected_shared_cluster() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime("test-sg-shared-cluster-queue-mismatch-retry");
    let handle = runtime.start().await.expect("stargate failed to start");

    let reject_hits = Arc::new(AtomicUsize::new(0));
    let reject_listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let reject_addr = reject_listener.local_addr().unwrap();
    let reject_hits_for_app = reject_hits.clone();
    let reject_app = Router::new()
        .route(
            "/v1/chat/completions",
            post(move |_req: Request| {
                let reject_hits = reject_hits_for_app.clone();
                async move {
                    reject_hits.fetch_add(1, Ordering::Relaxed);
                    (StatusCode::OK, "unexpected queue mismatch upstream hit")
                }
            }),
        )
        .route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        axum::serve(reject_listener, reject_app).await.unwrap();
    });

    let queue_tracker = QueueAdmissionTracker::default();
    queue_tracker.update_model_throughput("queue-mismatch-shared-model", 100.0);
    queue_tracker.record_observation(&RequestObservation {
        endpoint: RequestObservationEndpoint::ChatCompletions,
        request_id: "req-shared-already-queued".to_string(),
        routing_key: None,
        model_id: "queue-mismatch-shared-model".to_string(),
        priority: 0,
        input_tokens: 100,
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
    let mut reject_config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{reject_addr}"),
    );
    reject_config.queue_tracker = queue_tracker;
    let reject_tunnel = start_quic_http_tunnel(reject_config)
        .await
        .expect("queue mismatch tunnel failed to start");
    let reject_quic_url = format!("quic://{}", reject_tunnel.listen_addr());
    let (success_addr, success_quic_url, success_tunnel) =
        start_dummy_inst("queue-mismatch-shared-model").await;

    let mut reject_reg = InferenceServerRegistrationClient::default();
    let reject_channels = reject_reg
        .start(
            active_registration_config_in_cluster(
                grpc_addr,
                "queue-mismatch-a-reject",
                "queue-mismatch-shared-cluster",
                reject_quic_url,
                format!("http://{reject_addr}"),
            ),
            vec!["queue-mismatch-shared-model".to_string()],
        )
        .expect("reject registration failed");
    let mut success_reg = InferenceServerRegistrationClient::default();
    let success_channels = success_reg
        .start(
            active_registration_config_in_cluster(
                grpc_addr,
                "queue-mismatch-b-success",
                "queue-mismatch-shared-cluster",
                success_quic_url,
                format!("http://{success_addr}"),
            ),
            vec!["queue-mismatch-shared-model".to_string()],
        )
        .expect("success registration failed");

    for channels in [&reject_channels, &success_channels] {
        channels
            .model_stats
            .send_async((
                "queue-mismatch-shared-model".to_string(),
                CurrentModelStats {
                    last_mean_input_tps: 1000.0,
                    queued_input_size: 0,
                    ..CurrentModelStats::default()
                },
            ))
            .await
            .expect("send shared-cluster stats failed");
    }

    let target = RoutingTargetKey {
        routing_key: None,
        model_id: "queue-mismatch-shared-model".to_string(),
    };
    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let candidates = handle.state().cluster_candidates_for_target(&target).await;
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

    let http_client = reqwest::Client::new();
    let resp = with_proxy_headers(
        http_client.post(format!("http://{http_addr}/v1/chat/completions")),
        "queue-mismatch-shared-model",
        "req-shared-queue-mismatch-retry",
    )
    .header("content-type", "application/json")
    .json(&serde_json::json!({
        "model": "queue-mismatch-shared-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    }))
    .send()
    .await
    .expect("request failed");

    assert_eq!(resp.status(), StatusCode::OK);
    assert_eq!(
        resp.headers()
            .get("x-stargate-cluster-id")
            .and_then(|value| value.to_str().ok()),
        Some("queue-mismatch-shared-cluster")
    );
    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .and_then(|value| value.to_str().ok()),
        Some("queue-mismatch-b-success")
    );
    assert_eq!(
        reject_hits.load(Ordering::Relaxed),
        0,
        "queue mismatch must reject before reaching the congested upstream"
    );
    let metrics = metrics_text(handle.metrics().registry());
    assert!(
        metrics.contains(
            r#"stargate_proxy_retries_total{model="queue-mismatch-shared-model",reason="queue_estimate_mismatch",routing_key=""} 1"#
        ),
        "missing local mismatch retry counter:\n{metrics}"
    );

    reject_reg.stop();
    success_reg.stop();
    reject_tunnel.shutdown().await;
    success_tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn closed_direct_quic_connection_recovers_on_hot_path() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-hotpath-reconnect");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, tunnel) = start_dummy_inst("reconnect-model").await;
    let tunnel_addr = tunnel.listen_addr();

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "reconnect-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: quic_url,
                upstream_http_base_url: Some(format!("http://{inst_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["reconnect-model".to_string()],
        )
        .expect("registration failed");

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
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "reconnect-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "reconnect-model",
        "req-hotpath-reconnect",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("request failed");

    assert_eq!(resp.status(), 200);
    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .and_then(|value| value.to_str().ok()),
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
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn replay_body_over_limit_returns_413() {
    init_crypto();

    let retry = ProxyRetryConfig {
        max_replay_body_bytes: 8,
        ..ProxyRetryConfig::default()
    };
    let (_grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_retry("test-sg-replay-limit", retry);
    let handle = runtime.start().await.expect("stargate failed to start");

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "oversized-model",
        "req-replay-over-limit",
    )
    .header("content-type", "application/json")
    .body(r#"{"stream":true}"#)
    .send()
    .await
    .expect("request failed");

    assert_eq!(resp.status(), StatusCode::PAYLOAD_TOO_LARGE);

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn chunked_replay_overflow_records_413_request_metric() {
    init_crypto();

    let retry = ProxyRetryConfig {
        max_replay_body_bytes: 8,
        ..ProxyRetryConfig::default()
    };
    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_retry("test-sg-chunked-replay-limit", retry);
    let handle = runtime.start().await.expect("stargate failed to start");

    let (reject_addr, reject_quic_url, reject_tunnel) = start_retryable_rejecting_inst().await;
    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "chunk-overflow-reject",
                reject_quic_url,
                format!("http://{reject_addr}"),
            ),
            vec!["chunk-overflow-model".to_string()],
        )
        .expect("registration failed");

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let chunked_body = reqwest::Body::wrap_stream(async_stream::stream! {
            yield Ok::<_, std::io::Error>(Bytes::from_static(br#"{"stream""#));
            yield Ok::<_, std::io::Error>(Bytes::from_static(br#":true}"#));
        });
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "chunk-overflow-model",
            "req-chunked-replay-overflow",
        )
        .header("content-type", "application/json")
        .body(chunked_body)
        .send()
        .await
        .expect("request failed");

        let metrics = metrics_text(handle.metrics().registry());
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

    reg_client.stop();
    reject_tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn retryable_single_backend_exhausts_eligible_backends() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-retry-single-exhaust");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (reject_addr, reject_quic_url, reject_tunnel) = start_retryable_rejecting_inst().await;
    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "single-reject",
                reject_quic_url,
                format!("http://{reject_addr}"),
            ),
            vec!["single-exhaust-model".to_string()],
        )
        .expect("registration failed");

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "single-exhaust-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "single-exhaust-model",
            "req-single-exhaust",
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");

        let metrics = metrics_text(handle.metrics().registry());
        if resp.status() == StatusCode::SERVICE_UNAVAILABLE
            && metrics.contains(
                r#"stargate_proxy_attempts_total{inference_server_id="single-reject",model="single-exhaust-model",result="upstream_429",routing_key=""}"#,
            )
        {
            assert!(
                metrics.contains(
                    r#"stargate_proxy_retry_exhausted_total{model="single-exhaust-model",reason="no_eligible_backend",routing_key=""} 1"#
                ),
                "missing retry exhaustion metric:\n{metrics}"
            );
            break;
        }

        assert!(
            tokio::time::Instant::now() < deadline,
            "single retryable backend should exhaust eligible backends"
        );
        poll.tick().await;
    }

    reg_client.stop();
    reject_tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn request_retry_limit_returns_last_retryable_rejection() {
    init_crypto();

    let retry = ProxyRetryConfig {
        max_request_retries: 1,
        ..ProxyRetryConfig::default()
    };
    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_retry("test-sg-request-retry-limit", retry);
    let handle = runtime.start().await.expect("stargate failed to start");

    let (reject_a_addr, reject_a_quic_url, reject_a_tunnel) =
        start_retryable_rejecting_inst().await;
    let (reject_b_addr, reject_b_quic_url, reject_b_tunnel) =
        start_retryable_rejecting_inst().await;

    let mut reg_a = InferenceServerRegistrationClient::default();
    let _channels_a = reg_a
        .start(
            active_registration_config(
                grpc_addr,
                "retry-limit-a",
                reject_a_quic_url,
                format!("http://{reject_a_addr}"),
            ),
            vec!["retry-limit-model".to_string()],
        )
        .expect("registration a failed");
    let mut reg_b = InferenceServerRegistrationClient::default();
    let _channels_b = reg_b
        .start(
            active_registration_config(
                grpc_addr,
                "retry-limit-b",
                reject_b_quic_url,
                format!("http://{reject_b_addr}"),
            ),
            vec!["retry-limit-model".to_string()],
        )
        .expect("registration b failed");

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "retry-limit-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "retry-limit-model",
            "req-retry-limit",
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");

        if resp.status() == StatusCode::TOO_MANY_REQUESTS {
            assert!(resp.headers().get("x-stargate-retryable").is_none());
            let metrics = metrics_text(handle.metrics().registry());
            assert!(
                metrics.contains(
                    r#"stargate_proxy_retry_exhausted_total{model="retry-limit-model",reason="upstream_admission_rejected",routing_key=""} 1"#
                ),
                "missing request retry exhausted metric:\n{metrics}"
            );
            break;
        }

        assert!(
            tokio::time::Instant::now() < deadline,
            "request retry limit should return the final retryable rejection"
        );
        poll.tick().await;
    }

    reg_a.stop();
    reg_b.stop();
    reject_a_tunnel.shutdown().await;
    reject_b_tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
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
    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            active_registration_config(
                grpc_addr,
                "connect-zero-inst",
                quic_url,
                format!("http://{inst_addr}"),
            ),
            vec!["connect-zero-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "connect-zero-model", Duration::from_secs(5)).await;
    tunnel.shutdown().await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "connect-zero-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "connect-zero-model",
            "req-connect-zero",
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");

        if resp.status() == StatusCode::BAD_GATEWAY {
            let metrics = metrics_text(handle.metrics().registry());
            assert!(
                metrics.contains(
                    r#"stargate_proxy_retry_exhausted_total{model="connect-zero-model",reason="connect_retries_exhausted",routing_key=""} 1"#
                ),
                "missing connect retry exhausted metric:\n{metrics}"
            );
            assert!(
                !metrics.contains(
                    r#"stargate_quic_hot_path_reconnect_total{inference_server_id="connect-zero-inst""#
                ),
                "zero connect retries should not attempt hot-path reconnect:\n{metrics}"
            );
            break;
        }

        assert!(
            tokio::time::Instant::now() < deadline,
            "zero connect retries should return a proxy error after tunnel closes"
        );
        poll.tick().await;
    }

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn pulsar_missing_cache_affinity_header_returns_400() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    std::io::Write::write_all(
        &mut tmp_file,
        br#"{
            "default": "power-of-two",
            "models": {
                "pulsar-model": {
                    "algorithm": "pulsar",
                    "seed": "test-seed",
                    "require_cache_affinity_key": true,
                    "require_input_tokens": true
                }
            }
        }"#,
    )
    .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-pulsar-missing-affinity", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("pulsar-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "pulsar-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: quic_url,
                upstream_http_base_url: Some(format!("http://{inst_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig::default(),
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["pulsar-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing_with_cache_affinity(
        http_addr,
        "pulsar-model",
        "prefix-a",
        Duration::from_secs(5),
    )
    .await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "pulsar-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "pulsar-model",
        "req-no-affinity",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("request failed");
    assert_eq!(resp.status(), 400);

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn pulsar_missing_input_tokens_header_returns_400() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    std::io::Write::write_all(
        &mut tmp_file,
        br#"{
            "default": "power-of-two",
            "models": {
                "pulsar-model": {
                    "algorithm": "pulsar",
                    "seed": "test-seed",
                    "require_cache_affinity_key": true,
                    "require_input_tokens": true
                }
            }
        }"#,
    )
    .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-pulsar-missing-input", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("pulsar-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "pulsar-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: quic_url,
                upstream_http_base_url: Some(format!("http://{inst_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig::default(),
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["pulsar-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing_with_cache_affinity(
        http_addr,
        "pulsar-model",
        "prefix-a",
        Duration::from_secs(5),
    )
    .await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "pulsar-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let resp = http_client
        .post(&stargate_url)
        .header("x-model", "pulsar-model")
        .header("x-request-id", "req-no-input-tokens")
        .header("x-cache-affinity-key", "prefix-a")
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
    assert_eq!(resp.status(), 400);

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

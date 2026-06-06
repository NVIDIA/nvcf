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

use std::collections::HashSet;
use std::io::Write;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use crate::common::{
    bind_ephemeral_udp, init_crypto, make_stargate_runtime_with_reverse,
    make_stargate_runtime_with_reverse_and_lb, start_dummy_backend, wait_for_inference_server_ids,
    wait_for_routing, wait_for_unroutable, with_proxy_headers,
};
use axum::Router;
use axum::body::Body;
use axum::extract::{Request, State};
use axum::http::{HeaderName, HeaderValue, StatusCode};
use axum::response::IntoResponse;
use axum::response::Response;
use axum::routing::{get, post};
use pylon_lib::{
    BringupConfig, InferenceServerRegistrationClient, InferenceServerRegistrationConfig,
    OutputTokenParserFactory, ReverseQuicTunnelConfig, TunnelError, start_reverse_quic_tunnel,
};
use stargate_proto::pb::InferenceServerRegistration;
use stargate_proto::pb::InferenceServerStatus;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use tokio::net::TcpListener;
use tonic::transport::Channel;

async fn start_dummy_backend_with_responses(model: &str) -> std::net::SocketAddr {
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
    addr
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
            "id": "chatcmpl-reverse-test",
            "object": "chat.completion",
            "model": model,
            "path_and_query": path_and_query,
            "choices": [{
                "index": 0,
                "message": { "role": "assistant", "content": "reverse contract echo" },
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
                "id": "chatcmpl-reverse-test",
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
                "id": "chatcmpl-reverse-test",
                "object": "chat.completion.chunk",
                "model": model,
                "path_and_query": path_and_query,
                "request": request_json,
                "choices": [{
                    "index": 0,
                    "delta": { "content": "reverse contract echo" },
                    "finish_reason": null,
                }],
            })
        )
    } else {
        format!(
            "event: response.completed\ndata: {}\n\n",
            serde_json::json!({
                "type": "response.completed",
                "response": {
                    "object": "response",
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

#[tokio::test]
async fn reverse_tunnel_proxies_chat_endpoint_contract() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse(
        "test-sg-rt-chat-contract",
        reverse_addr,
        Some(reverse_socket),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr = start_dummy_backend_with_responses("rt-chat-contract-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-chat-contract-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr}"),
                upstream_http_base_url: Some(format!("http://{backend_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-chat-contract-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "rt-chat-contract-model", Duration::from_secs(10)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions?trace=reverse-chat");
    let body = serde_json::json!({
        "model": "rt-chat-contract-model",
        "messages": [{"role": "user", "content": "reverse contract"}],
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "rt-chat-contract-model",
        "req-rt-chat-contract",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("reverse chat contract request failed");
    assert_eq!(resp.status(), 200);
    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id")
            .to_str()
            .unwrap(),
        "rt-chat-contract-inst"
    );
    assert_eq!(
        resp.headers()
            .get("x-inference-server-url")
            .expect("missing x-inference-server-url")
            .to_str()
            .unwrap(),
        format!("http://{backend_addr}")
    );
    assert_eq!(
        resp.headers()
            .get("x-stargate-cluster-id")
            .expect("missing x-stargate-cluster-id")
            .to_str()
            .unwrap(),
        "rt-chat-contract-inst"
    );
    let response_text = resp.text().await.expect("response should be text");
    assert!(response_text.contains(r#""object":"chat.completion.chunk""#));
    assert!(response_text.contains(r#""model":"rt-chat-contract-model""#));
    assert!(
        response_text.contains(r#""path_and_query":"/v1/chat/completions?trace=reverse-chat""#)
    );
    assert!(response_text.contains(r#""content":"reverse contract echo""#));
    assert!(response_text.contains(r#""content":"reverse contract""#));
    assert!(response_text.contains("[DONE]"));

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn reverse_tunnel_proxies_streaming_response() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_reverse("test-sg-rt-stream", reverse_addr, Some(reverse_socket));
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr = start_dummy_backend("rt-stream-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-stream-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr}"),
                upstream_http_base_url: Some(format!("http://{backend_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-stream-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "rt-stream-model", Duration::from_secs(10)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "rt-stream-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "rt-stream-model",
        "req-rt-stream",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("streaming request failed");
    assert_eq!(resp.status(), 200);

    let sse_text = resp.text().await.expect("failed to read streaming body");
    assert!(sse_text.contains("Hello"), "SSE should contain 'Hello'");
    assert!(sse_text.contains(" world"), "SSE should contain ' world'");
    assert!(sse_text.contains("!"), "SSE should contain '!'");
    assert!(sse_text.contains("[DONE]"), "SSE should end with [DONE]");

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn reverse_tunnel_proxies_responses_response() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse(
        "test-sg-rt-responses",
        reverse_addr,
        Some(reverse_socket),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr = start_dummy_backend_with_responses("rt-responses-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-responses-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr}"),
                upstream_http_base_url: Some(format!("http://{backend_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-responses-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "rt-responses-model", Duration::from_secs(10)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/responses?trace=reverse-responses");
    let body = serde_json::json!({
        "model": "rt-responses-model",
        "input": "hi",
        "max_output_tokens": 2,
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "rt-responses-model",
        "req-rt-responses",
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
        "rt-responses-inst"
    );
    assert_eq!(
        resp.headers()
            .get("x-inference-server-url")
            .expect("missing x-inference-server-url")
            .to_str()
            .unwrap(),
        format!("http://{backend_addr}")
    );
    assert_eq!(
        resp.headers()
            .get("x-stargate-cluster-id")
            .expect("missing x-stargate-cluster-id")
            .to_str()
            .unwrap(),
        "rt-responses-inst"
    );
    let response_text = resp.text().await.expect("response should be text");
    assert!(response_text.contains("event: response.completed"));
    assert!(response_text.contains(r#""type":"response.completed""#));
    assert!(response_text.contains(r#""object":"response""#));
    assert!(response_text.contains(r#""model":"rt-responses-model""#));
    assert!(response_text.contains(r#""path_and_query":"/v1/responses?trace=reverse-responses""#));
    assert!(response_text.contains(r#""input":"hi""#));
    assert!(response_text.contains(r#""stream":true"#));

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn reverse_tunnel_forwards_endpoint_upstream_errors() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse(
        "test-sg-rt-endpoint-errors",
        reverse_addr,
        Some(reverse_socket),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr = start_dummy_backend_with_responses("rt-error-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-error-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr}"),
                upstream_http_base_url: Some(format!("http://{backend_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-error-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "rt-error-model", Duration::from_secs(10)).await;

    let http_client = reqwest::Client::new();
    let expected_url = format!("http://{backend_addr}");
    let cases = [
        (
            "/v1/chat/completions?fail=1",
            serde_json::json!({
                "model": "rt-error-model",
                "messages": [{"role": "user", "content": "hi"}],
                "stream": true,
            }),
            "chat completions unavailable",
        ),
        (
            "/v1/responses?fail=1",
            serde_json::json!({
                "model": "rt-error-model",
                "input": "hi",
                "stream": true,
            }),
            "responses unavailable",
        ),
    ];

    for (path, body, expected_error) in cases {
        let resp = with_proxy_headers(
            http_client.post(format!("http://{http_addr}{path}")),
            "rt-error-model",
            &format!("req-rt-error-{path}"),
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("reverse upstream error request failed");
        assert_eq!(resp.status(), StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(
            resp.headers()
                .get("x-inference-server-id")
                .expect("missing x-inference-server-id")
                .to_str()
                .unwrap(),
            "rt-error-inst"
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
            "rt-error-inst"
        );
        let response_json: serde_json::Value = resp.json().await.expect("response should be json");
        assert_eq!(response_json["error"], expected_error);
    }

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// The QUIC tunnel enforces streaming for chat completions. Non-streaming
/// requests through a reverse tunnel should also be rejected with 400.
#[tokio::test]
async fn reverse_tunnel_rejects_non_streaming_chat_completions() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse(
        "test-sg-rt-nonstream",
        reverse_addr,
        Some(reverse_socket),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr = start_dummy_backend("rt-ns-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-ns-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr}"),
                upstream_http_base_url: Some(format!("http://{backend_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-ns-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "rt-ns-model", Duration::from_secs(10)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "rt-ns-model",
        "messages": [{"role": "user", "content": "hi"}],
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "rt-ns-model",
        "req-rt-nonstream",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("non-streaming request failed");
    assert_eq!(
        resp.status(),
        400,
        "non-streaming chat completions should be rejected by reverse tunnel"
    );

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn reverse_tunnel_disconnection_removes_from_routing() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse(
        "test-sg-rt-disconnect",
        reverse_addr,
        Some(reverse_socket),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr = start_dummy_backend("rt-disc-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-disc-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr}"),
                upstream_http_base_url: Some(format!("http://{backend_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-disc-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "rt-disc-model", Duration::from_secs(10)).await;

    reg_client.stop();

    wait_for_unroutable(http_addr, "rt-disc-model", Duration::from_secs(10)).await;

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn reverse_tunnel_reconnection_restores_routing() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse(
        "test-sg-rt-reconnect",
        reverse_addr,
        Some(reverse_socket),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr = start_dummy_backend("rt-recon-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-recon-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr}"),
                upstream_http_base_url: Some(format!("http://{backend_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-recon-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "rt-recon-model", Duration::from_secs(10)).await;

    reg_client.stop();
    wait_for_unroutable(http_addr, "rt-recon-model", Duration::from_secs(10)).await;

    // Re-register with the same backend
    let mut reg_client2 = InferenceServerRegistrationClient::default();
    let _channels2 = reg_client2
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-recon-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr}"),
                upstream_http_base_url: Some(format!("http://{backend_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-recon-model".to_string()],
        )
        .expect("re-registration failed");

    wait_for_routing(http_addr, "rt-recon-model", Duration::from_secs(10)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "rt-recon-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "rt-recon-model",
        "req-rt-recon-verify",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("request after reconnection failed");
    assert_eq!(resp.status(), 200);
    let text = resp.text().await.unwrap();
    assert!(text.contains("[DONE]"), "SSE should end with [DONE]");

    reg_client2.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn reverse_tunnel_unregistered_id_rejected() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (_grpc_addr, _http_addr, runtime) =
        make_stargate_runtime_with_reverse("test-sg-rt-unreg", reverse_addr, Some(reverse_socket));
    let handle = runtime.start().await.expect("stargate failed to start");

    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    let mut poll = tokio::time::interval(Duration::from_millis(50));
    let result = loop {
        let mut cfg = ReverseQuicTunnelConfig::new(
            format!("localhost:{}", reverse_addr.port()),
            "totally-unknown-inst".to_string(),
            "http://127.0.0.1:9999".to_string(),
        );
        cfg.quic_insecure = true;
        let r = start_reverse_quic_tunnel(cfg).await;
        match &r {
            Err(TunnelError::HandshakeRejected { .. }) | Ok(_) => break r,
            Err(_) if tokio::time::Instant::now() < deadline => {
                poll.tick().await;
            }
            Err(_) => break r,
        }
    };

    match result {
        Err(TunnelError::HandshakeRejected { reason }) => {
            assert!(
                reason.contains("unauthorized"),
                "NACK reason should reach the client with the server's rejection message, \
                 but got: {reason}"
            );
        }
        Err(other) => panic!("expected handshake rejection, got: {other}"),
        Ok(handle) => {
            handle.shutdown().await;
            panic!("expected reverse handshake rejection for unregistered id");
        }
    }

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn reverse_tunnel_multiple_instances() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    write!(tmp_file, r#"{{"default": "round-robin", "models": {{}}}}"#)
        .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse_and_lb(
        "test-sg-rt-multi",
        reverse_addr,
        Some(reverse_socket),
        Some(config_path),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr_a = start_dummy_backend("rt-multi-model").await;
    let backend_addr_b = start_dummy_backend("rt-multi-model").await;

    let mut reg_a = InferenceServerRegistrationClient::default();
    let _channels_a = reg_a
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-multi-a".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr_a}"),
                upstream_http_base_url: Some(format!("http://{backend_addr_a}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-multi-model".to_string()],
        )
        .expect("registration failed");

    let mut reg_b = InferenceServerRegistrationClient::default();
    let _channels_b = reg_b
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-multi-b".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr_b}"),
                upstream_http_base_url: Some(format!("http://{backend_addr_b}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-multi-model".to_string()],
        )
        .expect("registration failed");

    // Wait for both instances to become routable
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "rt-multi-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let seen = wait_for_inference_server_ids(
        http_addr,
        "rt-multi-model",
        "req-rt-multi-wait",
        2,
        Duration::from_secs(20),
        Duration::from_millis(200),
    )
    .await;
    assert_eq!(
        seen.len(),
        2,
        "expected both reverse tunnel instances to register, saw: {seen:?}"
    );

    // Verify both IDs appear in subsequent requests (round-robin)
    let mut ids = HashSet::new();
    for _ in 0..6 {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "rt-multi-model",
            "req-rt-multi-run",
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
        assert_eq!(resp.status(), 200);
        let id = resp
            .headers()
            .get("x-inference-server-id")
            .unwrap()
            .to_str()
            .unwrap()
            .to_string();
        ids.insert(id);
    }
    assert_eq!(
        ids.len(),
        2,
        "round-robin over 6 requests should hit both instances, saw: {ids:?}"
    );

    reg_a.stop();
    reg_b.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// Regression: the reverse tunnel loop used to reconnect on every registration
/// heartbeat ACK because the endpoint watch receiver fires whenever the sender
/// calls `send()`, even with the same value. When the endpoint hadn't actually
/// changed, the select fell through, the tunnel handle was dropped, and the
/// outer loop created a brand-new QUIC connection -- producing a reconnect
/// storm visible as hundreds of "reverse tunnel connected" log lines per second.
///
/// This test registers a reverse-tunnel instance, waits for it to become
/// routable, then lets heartbeats flow for 3 seconds. It captures tracing
/// events and asserts that "reverse tunnel connected" fires exactly once.
#[tokio::test]
async fn reverse_tunnel_does_not_reconnect_on_duplicate_acks() {
    init_crypto();

    let connect_count = Arc::new(AtomicU64::new(0));

    struct ConnectCounter(Arc<AtomicU64>);

    impl<S: tracing::Subscriber> tracing_subscriber::layer::Layer<S> for ConnectCounter {
        fn on_event(
            &self,
            event: &tracing::Event<'_>,
            _ctx: tracing_subscriber::layer::Context<'_, S>,
        ) {
            let meta = event.metadata();
            if meta.target().contains("pylon_lib") {
                struct MessageVisitor<'a>(&'a AtomicU64);
                impl tracing::field::Visit for MessageVisitor<'_> {
                    fn record_debug(
                        &mut self,
                        field: &tracing::field::Field,
                        value: &dyn std::fmt::Debug,
                    ) {
                        if field.name() == "message" {
                            let msg = format!("{value:?}");
                            if msg.contains("reverse tunnel connected") {
                                self.0.fetch_add(1, Ordering::Relaxed);
                            }
                        }
                    }
                }
                event.record(&mut MessageVisitor(&self.0));
            }
        }
    }

    let _guard = {
        use tracing_subscriber::layer::SubscriberExt;
        use tracing_subscriber::util::SubscriberInitExt;
        tracing_subscriber::registry()
            .with(ConnectCounter(connect_count.clone()))
            .set_default()
    };

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse(
        "test-sg-rt-no-storm",
        reverse_addr,
        Some(reverse_socket),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr = start_dummy_backend("rt-storm-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "rt-storm-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: format!("http://{backend_addr}"),
                upstream_http_base_url: Some(format!("http://{backend_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: true,
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
            vec!["rt-storm-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "rt-storm-model", Duration::from_secs(10)).await;

    // Reset counter after initial connection is established
    connect_count.store(0, Ordering::SeqCst);

    // Let ~30 heartbeat ACK opportunities pass. If the bug is present, the
    // tunnel reconnects on duplicate ACKs and the counter trips during this window.
    let mut heartbeat_window = tokio::time::interval(Duration::from_millis(100));
    heartbeat_window.tick().await;
    for _ in 0..30 {
        heartbeat_window.tick().await;
        assert_eq!(
            connect_count.load(Ordering::SeqCst),
            0,
            "tunnel should not reconnect during steady-state duplicate ACK heartbeats"
        );
    }

    // Verify the tunnel is still functional
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "rt-storm-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "rt-storm-model",
        "req-rt-storm-verify",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("request after steady-state failed");
    assert_eq!(resp.status(), 200);

    let reconnects = connect_count.load(Ordering::SeqCst);
    assert_eq!(
        reconnects, 0,
        "tunnel should not reconnect during steady-state heartbeats, \
         but reconnected {reconnects} times in 3 seconds"
    );

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// Reproduces a connection leak: dropping a ReverseQuicTunnelHandle without
/// calling shutdown() leaves the QUIC connection alive on the server. A
/// subsequent connection attempt with the same inference_server_id is then
/// NACKed as "duplicate connection" indefinitely.
///
/// This simulates what happens when the registration loop calls task.abort()
/// on the reverse tunnel task without cleanly shutting down the handle.
#[tokio::test]
async fn dropped_handle_does_not_block_reconnection() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, _http_addr, runtime) = make_stargate_runtime_with_reverse(
        "test-sg-rt-drop-leak",
        reverse_addr,
        Some(reverse_socket),
    );
    let sg_handle = runtime.start().await.expect("stargate failed to start");

    // Register the inference server via raw gRPC so the server allows
    // the reverse tunnel handshake.
    let endpoint = format!("http://{grpc_addr}");
    let channel = Channel::from_shared(endpoint)
        .unwrap()
        .connect()
        .await
        .expect("grpc connect failed");
    let mut client = StargateControlPlaneClient::new(channel);
    let (tx, rx) = flume::bounded(8);
    tx.send_async(InferenceServerRegistration {
        inference_server_id: "drop-leak-inst".to_string(),
        cluster_id: String::new(),
        inference_server_url: "http://127.0.0.1:9999".to_string(),
        models: Default::default(),
        reverse_tunnel: true,
        coordinated_calibration: false,
    })
    .await
    .unwrap();
    let _reg_stream = client
        .register_inference_server(rx.into_stream())
        .await
        .expect("registration rpc failed");

    let target = format!("localhost:{}", reverse_addr.port());

    // Connect the first reverse tunnel, retrying until registration propagates.
    let handle1 = {
        let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
        let mut poll = tokio::time::interval(Duration::from_millis(50));
        loop {
            let mut cfg = ReverseQuicTunnelConfig::new(
                target.clone(),
                "drop-leak-inst".to_string(),
                "http://127.0.0.1:9999".to_string(),
            );
            cfg.quic_insecure = true;
            match start_reverse_quic_tunnel(cfg).await {
                Ok(h) => break h,
                Err(_) if tokio::time::Instant::now() < deadline => {
                    poll.tick().await;
                }
                Err(e) => panic!("first reverse tunnel failed: {e}"),
            }
        }
    };

    // Drop the handle WITHOUT calling shutdown(), simulating task.abort().
    drop(handle1);

    // Retry the second connection until the server detects the first handle's close.
    let handle2 = {
        let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
        let mut poll = tokio::time::interval(Duration::from_millis(100));
        loop {
            let mut cfg = ReverseQuicTunnelConfig::new(
                target.clone(),
                "drop-leak-inst".to_string(),
                "http://127.0.0.1:9999".to_string(),
            );
            cfg.quic_insecure = true;
            match start_reverse_quic_tunnel(cfg).await {
                Ok(h) => break h,
                Err(_) if tokio::time::Instant::now() < deadline => {
                    poll.tick().await;
                }
                Err(e) => panic!(
                    "second reverse tunnel should succeed after first handle was dropped, \
                     but got: {e}"
                ),
            }
        }
    };
    handle2.shutdown().await;

    // Drop the registration request sender so the gRPC stream ends before
    // stargate shutdown; otherwise tonic graceful shutdown can block.
    drop(tx);
    sg_handle.begin_shutdown();
    sg_handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

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
use std::net::SocketAddr;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use crate::common::sse::{
    assert_sse_done, chat_completion_contents, json_events, parse_sse_events,
};
use crate::common::{
    bind_ephemeral_udp, init_crypto, make_stargate_runtime_with_reverse_and_lb,
    reverse_registration_config, start_dummy_backend, wait_for_inference_server_ids,
    wait_for_routing, wait_for_unroutable, with_proxy_headers,
};
use axum::Router;
use axum::extract::{Request, State};
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::response::Response;
use axum::routing::{get, post};
use pylon_lib::{
    InferenceServerRegistrationClient, InferenceServerRegistrationConfig, PylonRuntimeState,
    ReverseQuicTunnelConfig, ReverseQuicTunnelHandle, TunnelError, start_reverse_quic_tunnel,
};
use stargate::runtime::StargateHandle;
use stargate_proto::pb::InferenceServerRegistration;
use stargate_proto::pb::InferenceServerStatus;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use tokio::net::TcpListener;
use tonic::transport::Channel;

const ROUTING_TIMEOUT: Duration = Duration::from_secs(10);
const SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(5);

struct ReverseStargate {
    reverse_addr: SocketAddr,
    grpc_addr: SocketAddr,
    http_addr: SocketAddr,
    handle: StargateHandle,
}

impl ReverseStargate {
    async fn start(id: &str) -> Self {
        Self::start_with_lb(id, None).await
    }

    async fn start_with_lb(id: &str, lb_config_path: Option<String>) -> Self {
        init_crypto();
        let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
        let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse_and_lb(
            id,
            reverse_addr,
            Some(reverse_socket),
            lb_config_path,
        );
        let handle = runtime.start().await.expect("stargate failed to start");
        Self {
            reverse_addr,
            grpc_addr,
            http_addr,
            handle,
        }
    }

    fn register_backend(
        &self,
        inference_server_id: &str,
        backend_addr: SocketAddr,
        model: &str,
    ) -> InferenceServerRegistrationClient {
        let mut client = InferenceServerRegistrationClient::default();
        client
            .start(reverse_backend_registration(
                self.grpc_addr,
                inference_server_id,
                backend_addr,
                model,
            ))
            .unwrap_or_else(|error| {
                panic!("failed to register reverse backend {inference_server_id}: {error}")
            });
        client
    }

    async fn wait_for_routing(&self, model: &str) {
        wait_for_routing(self.http_addr, model, ROUTING_TIMEOUT).await;
    }

    async fn post_json(
        &self,
        path: &str,
        model: &str,
        request_id: &str,
        body: &serde_json::Value,
    ) -> reqwest::Response {
        with_proxy_headers(
            reqwest::Client::new().post(format!("http://{}{path}", self.http_addr)),
            model,
            request_id,
        )
        .header("content-type", "application/json")
        .json(body)
        .send()
        .await
        .unwrap_or_else(|error| panic!("reverse tunnel request {request_id} failed: {error}"))
    }

    async fn post_streaming_chat(&self, model: &str, request_id: &str) -> reqwest::Response {
        self.post_json(
            "/v1/chat/completions",
            model,
            request_id,
            &serde_json::json!({
                "model": model,
                "messages": [{"role": "user", "content": "hi"}],
                "stream": true,
            }),
        )
        .await
    }

    async fn shutdown(self) {
        self.handle.begin_shutdown();
        assert!(
            self.handle.wait_for_shutdown(SHUTDOWN_TIMEOUT).await,
            "stargate should shut down within {SHUTDOWN_TIMEOUT:?}"
        );
    }
}

fn assert_backend_headers(response: &reqwest::Response, inference_server_id: &str, url: &str) {
    let headers = response.headers();
    for (name, expected) in [
        ("x-inference-server-id", inference_server_id),
        ("x-inference-server-url", url),
        ("x-stargate-cluster-id", inference_server_id),
    ] {
        let actual = headers
            .get(name)
            .unwrap_or_else(|| panic!("missing {name}"))
            .to_str()
            .unwrap_or_else(|error| panic!("invalid {name}: {error}"));
        assert_eq!(actual, expected, "unexpected {name}");
    }
}

async fn connect_reverse_tunnel(
    target: &str,
    inference_server_id: &str,
    retry_interval: Duration,
    failure_context: &str,
) -> ReverseQuicTunnelHandle {
    let deadline = tokio::time::Instant::now() + SHUTDOWN_TIMEOUT;
    let mut poll = tokio::time::interval(retry_interval);
    loop {
        let mut config = ReverseQuicTunnelConfig::new(
            target.to_string(),
            inference_server_id.to_string(),
            "http://127.0.0.1:9999".to_string(),
        );
        config.quic_insecure = true;
        match start_reverse_quic_tunnel(config).await {
            Ok(handle) => return handle,
            Err(_) if tokio::time::Instant::now() < deadline => {
                poll.tick().await;
            }
            Err(error) => panic!("{failure_context}: {error}"),
        }
    }
}

fn reverse_backend_registration(
    grpc_addr: SocketAddr,
    inference_server_id: &str,
    backend_addr: SocketAddr,
    model: &str,
) -> InferenceServerRegistrationConfig {
    reverse_registration_config(
        vec![grpc_addr.to_string()],
        inference_server_id,
        format!("http://{backend_addr}"),
        PylonRuntimeState::new(InferenceServerStatus::Active, &[model.to_string()]),
    )
}

async fn start_dummy_backend_with_responses(model: &str) -> std::net::SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let app = Router::new()
        .route("/v1/chat/completions", post(endpoint_contract_response))
        .route("/v1/responses", post(endpoint_contract_response))
        .route("/health", get(|| async { "ok" }))
        .with_state(model.to_string());
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    addr
}

async fn endpoint_contract_response(State(model): State<String>, req: Request) -> Response {
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
        return (
            StatusCode::SERVICE_UNAVAILABLE,
            axum::Json(serde_json::json!({
                "error": error,
            })),
        )
            .into_response();
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
            "id": "chatcmpl-reverse-test", "object": "chat.completion", "model": model,
            "path_and_query": path_and_query,
            "choices": [{"index": 0, "message": {
                "role": "assistant", "content": "reverse contract echo"
            }, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 1, "completion_tokens": 3, "total_tokens": 4},
        }))
        .into_response();
    }
    let stream_body = if is_chat_completion {
        format!(
            "data: {}\n\ndata: {}\n\ndata: [DONE]\n\n",
            serde_json::json!({
                "id": "chatcmpl-reverse-test", "object": "chat.completion.chunk",
                "model": model.clone(), "path_and_query": path_and_query.clone(),
                "choices": [{"index": 0, "delta": {"role": "assistant"},
                    "finish_reason": null}],
            }),
            serde_json::json!({
                "id": "chatcmpl-reverse-test", "object": "chat.completion.chunk",
                "model": model, "path_and_query": path_and_query, "request": request_json,
                "choices": [{"index": 0, "delta": {"content": "reverse contract echo"},
                    "finish_reason": null}],
            })
        )
    } else {
        format!(
            "event: response.completed\ndata: {}\n\n",
            serde_json::json!({
                "type": "response.completed",
                "response": {"object": "response", "model": model,
                    "path_and_query": path_and_query, "request": request_json},
            })
        )
    };
    ([("content-type", "text/event-stream")], stream_body).into_response()
}

fn assert_json_string_fields(actual: &serde_json::Value, expected: &[(&str, &str)]) {
    for (pointer, value) in expected {
        assert_eq!(
            actual.pointer(pointer).and_then(serde_json::Value::as_str),
            Some(*value),
            "unexpected {pointer}"
        );
    }
}

#[tokio::test]
async fn reverse_tunnel_proxies_chat_endpoint_contract() {
    let stargate = ReverseStargate::start("test-sg-rt-chat-contract").await;
    let backend_addr = start_dummy_backend_with_responses("rt-chat-contract-model").await;
    let mut reg_client = stargate.register_backend(
        "rt-chat-contract-inst",
        backend_addr,
        "rt-chat-contract-model",
    );
    stargate.wait_for_routing("rt-chat-contract-model").await;
    let body = serde_json::json!({
        "model": "rt-chat-contract-model",
        "messages": [{"role": "user", "content": "reverse contract"}],
        "stream": true,
    });
    let resp = stargate
        .post_json(
            "/v1/chat/completions?trace=reverse-chat",
            "rt-chat-contract-model",
            "req-rt-chat-contract",
            &body,
        )
        .await;
    assert_eq!(resp.status(), 200);
    assert_backend_headers(
        &resp,
        "rt-chat-contract-inst",
        &format!("http://{backend_addr}"),
    );
    let response_text = resp.text().await.expect("response should be text");
    let events = parse_sse_events(&response_text).expect("response should be valid SSE");
    assert_sse_done(&events);
    let payloads = json_events(&events);
    let payload = payloads
        .iter()
        .find(|payload| {
            payload.pointer("/choices/0/delta/content")
                == Some(&serde_json::json!("reverse contract echo"))
        })
        .unwrap_or_else(|| {
            panic!("reverse chat SSE payload did not preserve the endpoint contract: {payloads:#?}")
        });
    assert_json_string_fields(
        payload,
        &[
            ("/object", "chat.completion.chunk"),
            ("/model", "rt-chat-contract-model"),
            ("/path_and_query", "/v1/chat/completions?trace=reverse-chat"),
            ("/request/messages/0/content", "reverse contract"),
        ],
    );
    reg_client.stop();
    stargate.shutdown().await;
}

#[tokio::test]
async fn reverse_tunnel_proxies_streaming_response() {
    let stargate = ReverseStargate::start("test-sg-rt-stream").await;
    let backend_addr = start_dummy_backend("rt-stream-model").await;
    let mut reg_client =
        stargate.register_backend("rt-stream-inst", backend_addr, "rt-stream-model");
    stargate.wait_for_routing("rt-stream-model").await;
    let resp = stargate
        .post_streaming_chat("rt-stream-model", "req-rt-stream")
        .await;
    assert_eq!(resp.status(), 200);
    let sse_text = resp.text().await.expect("failed to read streaming body");
    let events = parse_sse_events(&sse_text).expect("streaming body should be valid SSE");
    assert_sse_done(&events);
    assert_eq!(
        chat_completion_contents(&events),
        vec!["Hello", " world", "!"]
    );
    reg_client.stop();
    stargate.shutdown().await;
}

#[tokio::test]
async fn reverse_tunnel_proxies_responses_response() {
    let stargate = ReverseStargate::start("test-sg-rt-responses").await;
    let backend_addr = start_dummy_backend_with_responses("rt-responses-model").await;
    let mut reg_client =
        stargate.register_backend("rt-responses-inst", backend_addr, "rt-responses-model");
    stargate.wait_for_routing("rt-responses-model").await;
    let body = serde_json::json!({
        "model": "rt-responses-model",
        "input": "hi",
        "max_output_tokens": 2,
        "stream": true,
    });
    let resp = stargate
        .post_json(
            "/v1/responses?trace=reverse-responses",
            "rt-responses-model",
            "req-rt-responses",
            &body,
        )
        .await;
    assert_eq!(resp.status(), 200);
    assert_backend_headers(
        &resp,
        "rt-responses-inst",
        &format!("http://{backend_addr}"),
    );
    let response_text = resp.text().await.expect("response should be text");
    let events = parse_sse_events(&response_text).expect("response should be valid SSE");
    assert_eq!(
        events
            .iter()
            .filter_map(|event| event.event_name.as_deref())
            .collect::<Vec<_>>(),
        vec!["response.completed"]
    );
    let payloads = json_events(&events);
    let completed = payloads
        .iter()
        .find(|payload| payload["type"] == "response.completed")
        .expect("responses stream should include response.completed");
    assert_json_string_fields(
        completed,
        &[
            ("/response/object", "response"),
            ("/response/model", "rt-responses-model"),
            (
                "/response/path_and_query",
                "/v1/responses?trace=reverse-responses",
            ),
            ("/response/request/input", "hi"),
        ],
    );
    assert_eq!(completed["response"]["request"]["stream"], true);
    reg_client.stop();
    stargate.shutdown().await;
}

#[tokio::test]
async fn reverse_tunnel_forwards_endpoint_upstream_errors() {
    let stargate = ReverseStargate::start("test-sg-rt-endpoint-errors").await;
    let backend_addr = start_dummy_backend_with_responses("rt-error-model").await;
    let mut reg_client = stargate.register_backend("rt-error-inst", backend_addr, "rt-error-model");
    stargate.wait_for_routing("rt-error-model").await;
    let expected_url = format!("http://{backend_addr}");
    let cases = [
        (
            "/v1/chat/completions?fail=1",
            "chat completions unavailable",
            serde_json::json!({"model": "rt-error-model", "messages": [{"role": "user",
                "content": "hi"}], "stream": true}),
        ),
        (
            "/v1/responses?fail=1",
            "responses unavailable",
            serde_json::json!({"model": "rt-error-model", "input": "hi", "stream": true}),
        ),
    ];
    for (path, expected_error, body) in cases {
        let resp = stargate
            .post_json(
                path,
                "rt-error-model",
                &format!("req-rt-error-{path}"),
                &body,
            )
            .await;
        assert_eq!(resp.status(), StatusCode::SERVICE_UNAVAILABLE);
        assert_backend_headers(&resp, "rt-error-inst", &expected_url);
        let response_json: serde_json::Value = resp.json().await.expect("response should be json");
        assert_eq!(response_json["error"], expected_error);
    }
    reg_client.stop();
    stargate.shutdown().await;
}

/// Reverse-tunnel chat completions must reject non-streaming requests with 400.
#[tokio::test]
async fn reverse_tunnel_rejects_non_streaming_chat_completions() {
    let stargate = ReverseStargate::start("test-sg-rt-nonstream").await;
    let backend_addr = start_dummy_backend("rt-ns-model").await;
    let mut reg_client = stargate.register_backend("rt-ns-inst", backend_addr, "rt-ns-model");
    stargate.wait_for_routing("rt-ns-model").await;
    let body = serde_json::json!({
        "model": "rt-ns-model",
        "messages": [{"role": "user", "content": "hi"}],
    });
    let resp = stargate
        .post_json(
            "/v1/chat/completions",
            "rt-ns-model",
            "req-rt-nonstream",
            &body,
        )
        .await;
    assert_eq!(
        resp.status(),
        400,
        "non-streaming chat completions should be rejected by reverse tunnel"
    );
    reg_client.stop();
    stargate.shutdown().await;
}

#[tokio::test]
async fn reverse_tunnel_disconnection_removes_from_routing() {
    let stargate = ReverseStargate::start("test-sg-rt-disconnect").await;
    let backend_addr = start_dummy_backend("rt-disc-model").await;
    let mut reg_client = stargate.register_backend("rt-disc-inst", backend_addr, "rt-disc-model");
    stargate.wait_for_routing("rt-disc-model").await;
    reg_client.stop();
    wait_for_unroutable(stargate.http_addr, "rt-disc-model", ROUTING_TIMEOUT).await;
    stargate.shutdown().await;
}

#[tokio::test]
async fn reverse_tunnel_reconnection_restores_routing() {
    let stargate = ReverseStargate::start("test-sg-rt-reconnect").await;
    let backend_addr = start_dummy_backend("rt-recon-model").await;
    let mut reg_client = stargate.register_backend("rt-recon-inst", backend_addr, "rt-recon-model");
    stargate.wait_for_routing("rt-recon-model").await;
    reg_client.stop();
    wait_for_unroutable(stargate.http_addr, "rt-recon-model", ROUTING_TIMEOUT).await;

    // The same backend identity must become routable again.
    let mut reg_client2 =
        stargate.register_backend("rt-recon-inst", backend_addr, "rt-recon-model");
    stargate.wait_for_routing("rt-recon-model").await;
    let resp = stargate
        .post_streaming_chat("rt-recon-model", "req-rt-recon-verify")
        .await;
    assert_eq!(resp.status(), 200);
    let text = resp.text().await.unwrap();
    assert_sse_done(&parse_sse_events(&text).expect("reconnected stream should be valid SSE"));
    reg_client2.stop();
    stargate.shutdown().await;
}

#[tokio::test]
async fn reverse_tunnel_unregistered_id_rejected() {
    let stargate = ReverseStargate::start("test-sg-rt-unreg").await;
    let deadline = tokio::time::Instant::now() + SHUTDOWN_TIMEOUT;
    let mut poll = tokio::time::interval(Duration::from_millis(50));

    let result = loop {
        let mut cfg = ReverseQuicTunnelConfig::new(
            format!("localhost:{}", stargate.reverse_addr.port()),
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
    stargate.shutdown().await;
}

#[tokio::test]
async fn reverse_tunnel_multiple_instances() {
    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    write!(tmp_file, r#"{{"default": "round-robin", "models": {{}}}}"#)
        .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();
    let stargate = ReverseStargate::start_with_lb("test-sg-rt-multi", Some(config_path)).await;
    let backend_addr_a = start_dummy_backend("rt-multi-model").await;
    let backend_addr_b = start_dummy_backend("rt-multi-model").await;
    let mut reg_a = stargate.register_backend("rt-multi-a", backend_addr_a, "rt-multi-model");
    let mut reg_b = stargate.register_backend("rt-multi-b", backend_addr_b, "rt-multi-model");

    let seen = wait_for_inference_server_ids(
        stargate.http_addr,
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

    let mut ids = HashSet::new();
    for _ in 0..6 {
        let resp = stargate
            .post_streaming_chat("rt-multi-model", "req-rt-multi-run")
            .await;
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
    stargate.shutdown().await;
}

/// Regression: duplicate registration heartbeat ACKs once caused a QUIC reconnect
/// storm. Count connection events through 30 ACK opportunities and prove the
/// established tunnel remains connected and functional.
#[tokio::test]
async fn reverse_tunnel_does_not_reconnect_on_duplicate_acks() {
    let connect_count = Arc::new(AtomicU64::new(0));
    struct ConnectCounter(Arc<AtomicU64>);
    impl<S: tracing::Subscriber> tracing_subscriber::layer::Layer<S> for ConnectCounter {
        fn on_event(
            &self,
            event: &tracing::Event<'_>,
            _ctx: tracing_subscriber::layer::Context<'_, S>,
        ) {
            if event.metadata().target().contains("pylon_lib") {
                struct MessageVisitor<'a>(&'a AtomicU64);
                impl tracing::field::Visit for MessageVisitor<'_> {
                    fn record_debug(
                        &mut self,
                        field: &tracing::field::Field,
                        value: &dyn std::fmt::Debug,
                    ) {
                        if field.name() == "message"
                            && format!("{value:?}").contains("reverse tunnel connected")
                        {
                            self.0.fetch_add(1, Ordering::Relaxed);
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
    let stargate = ReverseStargate::start("test-sg-rt-no-storm").await;
    let backend_addr = start_dummy_backend("rt-storm-model").await;
    let mut reg_client = stargate.register_backend("rt-storm-inst", backend_addr, "rt-storm-model");
    stargate.wait_for_routing("rt-storm-model").await;
    connect_count.store(0, Ordering::SeqCst);

    // Any reconnect during 30 heartbeat opportunities must fail immediately.
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

    let resp = stargate
        .post_streaming_chat("rt-storm-model", "req-rt-storm-verify")
        .await;
    assert_eq!(resp.status(), 200);
    let reconnects = connect_count.load(Ordering::SeqCst);
    assert_eq!(
        reconnects, 0,
        "tunnel should not reconnect during steady-state heartbeats, \
         but reconnected {reconnects} times in 3 seconds"
    );

    reg_client.stop();
    stargate.shutdown().await;
}

/// Regression: dropping a tunnel handle without `shutdown()` must release the
/// server connection so the same inference-server identity can reconnect.
#[tokio::test]
async fn dropped_handle_does_not_block_reconnection() {
    let stargate = ReverseStargate::start("test-sg-rt-drop-leak").await;

    // Raw gRPC registration authorizes the reverse handshake.
    let endpoint = format!("http://{}", stargate.grpc_addr);
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
    })
    .await
    .unwrap();
    let _reg_stream = client
        .register_inference_server(rx.into_stream())
        .await
        .expect("registration rpc failed");

    let target = format!("localhost:{}", stargate.reverse_addr.port());
    let handle1 = connect_reverse_tunnel(
        &target,
        "drop-leak-inst",
        Duration::from_millis(50),
        "first reverse tunnel failed",
    )
    .await;
    // Simulate task abortion rather than graceful tunnel shutdown.
    drop(handle1);
    let handle2 = connect_reverse_tunnel(
        &target,
        "drop-leak-inst",
        Duration::from_millis(100),
        "second reverse tunnel should succeed after first handle was dropped",
    )
    .await;
    handle2.shutdown().await;
    // End the gRPC stream before tonic's graceful shutdown.
    drop(tx);
    stargate.shutdown().await;
}

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
use std::time::Duration;

use crate::common::{
    ChatRequest, bind_ephemeral_udp, init_crypto, make_stargate_runtime,
    make_stargate_runtime_with_lb, make_stargate_runtime_with_reverse, start_dummy_backend,
    start_dummy_inst, wait_for_inference_server_ids, wait_for_routing, wait_for_unroutable,
    with_proxy_headers,
};
use axum::body::{Body, Bytes};
use axum::extract::State;
use axum::http::{HeaderMap, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use pylon_lib::{
    BringupConfig, EngineStatsStreamConfig, EngineStatsStreamMode,
    InferenceServerRegistrationClient, InferenceServerRegistrationConfig, OutputTokenParserFactory,
    QuicHttpTunnelConfig, QuicHttpTunnelHandle, RequestObservation, ReverseQuicTunnelConfig,
    StatsCollectorConfig, TunnelError, request_observation_channel, start_engine_stats_stream,
    start_quic_http_tunnel, start_reverse_quic_tunnel, start_stats_collector_with_engine_stats,
    stats_aggregator_update_channel,
};
use stargate::routing::RoutingTargetKey;
use stargate::test_support::StargateState;
use stargate_proto::pb::InferenceServerStatus;
use tokio::net::TcpListener;
use tokio::sync::{broadcast, watch};

#[tokio::test]
async fn end_to_end_registration_and_proxy() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-stargate");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("test-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "test-inst".to_string(),
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
            vec!["test-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "test-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "test-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "test-model",
        "req-test-stream",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("streaming request failed");
    assert_eq!(resp.status(), 200);

    let sse_text = resp.text().await.expect("failed to read streaming body");
    assert!(
        sse_text.contains("Hello"),
        "SSE should contain 'Hello' token"
    );
    assert!(
        sse_text.contains(" world"),
        "SSE should contain ' world' token"
    );
    assert!(sse_text.contains("[DONE]"), "SSE should end with [DONE]");

    reg_client.shutdown().await;
    wait_for_unroutable(http_addr, "test-model", Duration::from_secs(5)).await;

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn end_to_end_engine_stats_stream_reports_model_stats() {
    init_crypto();

    let model = "engine-stats-e2e-model";
    let request_id = "req-engine-stats-e2e";
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-stargate-engine-stats");
    let handle = runtime.start().await.expect("stargate failed to start");
    let state = handle.state();

    let stats_config = StatsCollectorConfig {
        openai_fallback_stats_enabled: false,
        ..StatsCollectorConfig::default()
    };
    let (request_observation_tx, request_observation_rx) =
        request_observation_channel(&stats_config);
    let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(&stats_config);
    let (inst_addr, quic_url, tunnel, stats_stream_connected_rx) =
        start_engine_stats_inst(model, request_observation_tx).await;
    let (engine_stats_stop_tx, engine_stats_stop_rx) = watch::channel(false);
    let engine_stats_stream = start_engine_stats_stream(
        EngineStatsStreamConfig::new(
            &format!("http://{inst_addr}"),
            "/pylon/v1/stats/stream",
            EngineStatsStreamMode::Required,
        ),
        stats_update_tx,
        engine_stats_stop_rx,
    )
    .expect("engine stats stream should start");

    let mut reg_client = InferenceServerRegistrationClient::default();
    let channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "progress-e2e-inst".to_string(),
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
            vec![model.to_string()],
        )
        .expect("registration failed");
    let (_stats_stop_tx, stats_stop_rx) = tokio::sync::watch::channel(false);
    let stats_collector = start_stats_collector_with_engine_stats(
        stats_config,
        request_observation_rx,
        Some(stats_update_rx),
        channels.model_stats.clone(),
        stats_stop_rx,
    );

    wait_for_engine_stats_stream_connection(stats_stream_connected_rx, Duration::from_secs(5))
        .await;
    wait_for_routing(http_addr, model, Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let resp = with_proxy_headers(http_client.post(&stargate_url), model, request_id)
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("streaming request failed");
    assert_eq!(resp.status(), 200);

    let sse_text = resp.text().await.expect("failed to read streaming body");
    assert!(
        sse_text.contains("Hello from engine stats"),
        "normal OpenAI data chunks should be forwarded: {sse_text}"
    );
    assert!(sse_text.contains("[DONE]"), "SSE should end with [DONE]");

    wait_for_engine_stats_stream_stats(&state, model, Duration::from_secs(5)).await;

    reg_client.stop();
    let _ = engine_stats_stop_tx.send(true);
    engine_stats_stream.shutdown().await;
    stats_collector.shutdown().await;
    tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn status_channel_controls_routing() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-stargate-status");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("status-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "test-inst-status".to_string(),
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
            vec!["status-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "status-model", Duration::from_secs(5)).await;

    channels
        .status
        .send_async(InferenceServerStatus::Inactive)
        .await
        .expect("send status failed");

    wait_for_unroutable(http_addr, "status-model", Duration::from_secs(5)).await;

    channels
        .status
        .send_async(InferenceServerStatus::Active)
        .await
        .expect("send status failed");

    wait_for_routing(http_addr, "status-model", Duration::from_secs(5)).await;

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn registration_stream_close_removes_instance() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-stargate-close");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("close-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "test-inst-close".to_string(),
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
            vec!["close-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "close-model", Duration::from_secs(5)).await;

    reg_client.stop();

    wait_for_unroutable(http_addr, "close-model", Duration::from_secs(5)).await;

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn round_robin_load_balancing() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    write!(
        tmp_file,
        r#"{{"default": "power-of-two", "models": {{"rr-model": "round-robin"}}}}"#
    )
    .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-stargate-rr", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");

    let inst_ids = ["inst-a", "inst-b", "inst-c"];
    let mut reg_clients = Vec::new();
    let mut _tunnels = Vec::new();
    for inst_id in &inst_ids {
        let (inst_addr, quic_url, tunnel) = start_dummy_inst("rr-model").await;
        _tunnels.push(tunnel);
        let mut reg_client = InferenceServerRegistrationClient::default();
        let _channels = reg_client
            .start(
                InferenceServerRegistrationConfig {
                    seeds: vec![grpc_addr.to_string()],
                    inference_server_id: inst_id.to_string(),
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
                vec!["rr-model".to_string()],
            )
            .expect("registration failed");
        reg_clients.push(reg_client);
    }

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "rr-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let seen = wait_for_inference_server_ids(
        http_addr,
        "rr-model",
        "req-rr-register",
        3,
        Duration::from_secs(10),
        Duration::from_millis(100),
    )
    .await;
    assert_eq!(
        seen.len(),
        3,
        "expected all 3 instances to register, saw: {seen:?}"
    );

    let mut chosen_ids = Vec::new();
    for _ in 0..9 {
        let resp = with_proxy_headers(http_client.post(&stargate_url), "rr-model", "req-rr-run")
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .expect("request failed");
        assert_eq!(resp.status(), 200);
        let id = resp
            .headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id header")
            .to_str()
            .unwrap()
            .to_string();
        chosen_ids.push(id);
    }

    for i in 0..6 {
        assert_eq!(
            chosen_ids[i],
            chosen_ids[i + 3],
            "round-robin pattern broken at index {i}: {:?}",
            chosen_ids
        );
    }

    let first_cycle: HashSet<_> = chosen_ids[0..3].iter().collect();
    assert_eq!(
        first_cycle.len(),
        3,
        "expected 3 distinct instances in first cycle, got: {:?}",
        &chosen_ids[0..3]
    );

    for client in &mut reg_clients {
        client.stop();
    }
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn reverse_tunnel_end_to_end() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse(
        "test-stargate-reverse",
        reverse_addr,
        Some(reverse_socket),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr = start_dummy_backend("reverse-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "reverse-inst".to_string(),
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
            vec!["reverse-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "reverse-model", Duration::from_secs(8)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "reverse-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "reverse-model",
        "req-reverse-second",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("second request failed");
    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .unwrap()
            .to_str()
            .unwrap(),
        "reverse-inst"
    );

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn reverse_tunnel_handshake_rejects_non_reverse_instance_id() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse(
        "test-stargate-reverse-reject",
        reverse_addr,
        Some(reverse_socket),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let (backend_addr, quic_url, _tunnel) = start_dummy_inst("reject-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    let _channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "reject-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: quic_url,
                upstream_http_base_url: Some(format!("http://{backend_addr}")),
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
            vec!["reject-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "reject-model", Duration::from_secs(8)).await;

    let mut reject_cfg = ReverseQuicTunnelConfig::new(
        format!("localhost:{}", reverse_addr.port()),
        "reject-inst".to_string(),
        format!("http://{backend_addr}"),
    );
    reject_cfg.quic_insecure = true;
    let reverse_result = start_reverse_quic_tunnel(reject_cfg).await;
    match reverse_result {
        Err(TunnelError::HandshakeRejected { .. }) => {}
        Err(other) => panic!("expected handshake rejection, got error: {other}"),
        Ok(handle) => {
            handle.shutdown().await;
            panic!("expected reverse handshake rejection for non-reverse instance");
        }
    }

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "reject-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "reject-model",
        "req-reject-run",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("request after rejected reverse tunnel failed");
    assert_eq!(resp.status(), 200);

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[derive(Clone)]
struct EngineStatsState {
    model: String,
    stats_tx: broadcast::Sender<String>,
    connected_tx: watch::Sender<bool>,
}

async fn start_engine_stats_inst(
    model: &str,
    request_observation_tx: flume::Sender<RequestObservation>,
) -> (
    SocketAddr,
    String,
    QuicHttpTunnelHandle,
    watch::Receiver<bool>,
) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let (stats_tx, _) = broadcast::channel(16);
    let (connected_tx, connected_rx) = watch::channel(false);
    let app = Router::new()
        .route("/v1/chat/completions", post(engine_stats_chat))
        .route("/pylon/v1/stats/stream", get(engine_stats_stream))
        .route("/health", get(|| async { "ok" }))
        .with_state(EngineStatsState {
            model: model.to_string(),
            stats_tx,
            connected_tx,
        });
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });

    let mut config =
        QuicHttpTunnelConfig::new("127.0.0.1:0".parse().unwrap(), format!("http://{addr}"));
    config.request_observation_tx = Some(request_observation_tx);
    let tunnel = start_quic_http_tunnel(config)
        .await
        .expect("tunnel failed to start");
    let tunnel_addr = tunnel.listen_addr();
    (addr, format!("quic://{tunnel_addr}"), tunnel, connected_rx)
}

async fn engine_stats_stream(State(state): State<EngineStatsState>) -> Response<Body> {
    let _ = state.connected_tx.send(true);
    let mut events = state.stats_tx.subscribe();
    let stream = async_stream::stream! {
        loop {
            match events.recv().await {
                Ok(event) => yield Ok::<Bytes, std::convert::Infallible>(Bytes::from(event)),
                Err(broadcast::error::RecvError::Lagged(_)) => continue,
                Err(broadcast::error::RecvError::Closed) => break,
            }
        }
    };

    Response::builder()
        .header("content-type", "application/x-ndjson")
        .body(Body::from_stream(stream))
        .unwrap()
}

async fn engine_stats_chat(
    headers: HeaderMap,
    State(state): State<EngineStatsState>,
    Json(req): Json<ChatRequest>,
) -> Response<Body> {
    if req.stream != Some(true) {
        return Response::builder()
            .status(400)
            .body(Body::from("streaming required"))
            .unwrap();
    }

    let request_id = headers
        .get("x-request-id")
        .and_then(|value| value.to_str().ok())
        .expect("test proxy should send x-request-id");
    let model = state.model.clone();
    send_engine_stats_event(
        &state.stats_tx,
        serde_json::json!({
            "v": 1,
            "type": "stats",
            "request_id": request_id,
            "model": model,
            "tokens_processed": 1,
            "tokens_generated": 0,
            "finished": false,
        }),
    );
    send_engine_stats_event(
        &state.stats_tx,
        serde_json::json!({
            "v": 1,
            "type": "stats",
            "request_id": request_id,
            "model": model,
            "tokens_processed": 1,
            "tokens_generated": 2,
            "finished": true,
        }),
    );

    let data_chunk = format!(
        r#"{{"object":"chat.completion.chunk","model":"{model}","choices":[{{"delta":{{"content":"Hello from engine stats"}}}}]}}"#
    );
    let sse_body = format!(
        ": keepalive\n\n\
data: {data_chunk}\n\n\
data: [DONE]\n\n"
    );

    Response::builder()
        .header("content-type", "text/event-stream")
        .body(Body::from(sse_body))
        .unwrap()
}

fn send_engine_stats_event(tx: &broadcast::Sender<String>, event: serde_json::Value) {
    let _ = tx.send(format!("{event}\n"));
}

async fn wait_for_engine_stats_stream_connection(
    mut connected_rx: watch::Receiver<bool>,
    timeout: Duration,
) {
    let deadline = tokio::time::Instant::now() + timeout;
    loop {
        if *connected_rx.borrow() {
            return;
        }
        if tokio::time::Instant::now() >= deadline {
            panic!(
                "engine stats stream did not connect within {}s",
                timeout.as_secs()
            );
        }
        let remaining = deadline.saturating_duration_since(tokio::time::Instant::now());
        tokio::time::timeout(remaining, connected_rx.changed())
            .await
            .expect("timed out waiting for engine stats stream connection")
            .expect("engine stats stream connection watch closed");
    }
}

async fn wait_for_engine_stats_stream_stats(
    state: &StargateState,
    model_id: &str,
    timeout: Duration,
) {
    let target = RoutingTargetKey {
        routing_key: None,
        model_id: model_id.to_string(),
    };
    let deadline = tokio::time::Instant::now() + timeout;
    let mut interval = tokio::time::interval(Duration::from_millis(20));
    interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);

    loop {
        let candidates = state.candidates_for_target(&target).await;
        if candidates.iter().any(|candidate| {
            candidate
                .stats
                .stats_capabilities
                .iter()
                .any(|capability| capability == "model.throughput.engine_stream")
                && candidate
                    .stats
                    .stats_sources
                    .iter()
                    .any(|source| source == "engine_stats_stream")
        }) {
            return;
        }

        if tokio::time::Instant::now() >= deadline {
            let last_seen = candidates
                .iter()
                .map(|candidate| {
                    format!(
                        "{} capabilities={:?} sources={:?}",
                        candidate.inference_server_id,
                        candidate.stats.stats_capabilities,
                        candidate.stats.stats_sources
                    )
                })
                .collect::<Vec<_>>();
            panic!(
                "model '{model_id}' did not report engine stats stream stats within {}s; last_seen={last_seen:?}",
                timeout.as_secs()
            );
        }

        interval.tick().await;
    }
}

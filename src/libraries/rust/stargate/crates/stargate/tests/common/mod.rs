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

use std::collections::{HashMap, HashSet};
use std::fmt::Debug;
use std::future::Future;
use std::net::SocketAddr;
use std::sync::atomic::{AtomicBool, AtomicU32, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use axum::Json;
use axum::Router;
use axum::extract::State;
use axum::http::{HeaderMap, StatusCode};
use axum::response::sse::{Event, KeepAlive, Sse};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use pylon_lib::{
    BringupConfig, InferenceServerRegistrationClient, InferenceServerRegistrationConfig,
    InferenceServerUpdateChannels, OutputTokenParserFactory, QuicHttpTunnelConfig,
    QuicHttpTunnelHandle, start_quic_http_tunnel,
};
use serde::{Deserialize, Serialize};
use stargate::auth::{AuthResult, WorkerAuthenticator};
use stargate::discovery::Discovery;
use stargate::proxy::ProxyTransportConfig;
use stargate::runtime::{StargateRuntime, StargateRuntimeConfig};
use stargate_forwarding::{ForwardingResolver, PeerResolution, PeerTarget};
use stargate_proto::pb::{InferenceServerStatus, StargateInfo};
use tokio::net::TcpListener;

// ---------------------------------------------------------------------------
// Test authenticator
// ---------------------------------------------------------------------------

/// Maps bearer tokens to routing keys. Tokens not in the map are rejected.
pub struct TokenMapAuthenticator {
    token_to_routing_key: HashMap<String, String>,
}

impl TokenMapAuthenticator {
    pub fn new(mappings: impl IntoIterator<Item = (&'static str, &'static str)>) -> Self {
        Self {
            token_to_routing_key: mappings
                .into_iter()
                .map(|(t, rk)| (t.to_string(), rk.to_string()))
                .collect(),
        }
    }
}

#[async_trait::async_trait]
impl WorkerAuthenticator for TokenMapAuthenticator {
    async fn authenticate(&self, token: Option<&str>) -> anyhow::Result<AuthResult> {
        let token = token.ok_or_else(|| anyhow::anyhow!("missing token"))?;
        let routing_key = self
            .token_to_routing_key
            .get(token)
            .ok_or_else(|| anyhow::anyhow!("unknown token"))?
            .clone();
        Ok(AuthResult {
            routing_key: Some(routing_key),
        })
    }
}

// ---------------------------------------------------------------------------
// Discovery implementations for tests
// ---------------------------------------------------------------------------

/// Always returns only itself. Used for single-stargate tests.
pub struct SelfDiscovery {
    self_info: StargateInfo,
}

impl SelfDiscovery {
    pub fn new(id: &str, grpc_addr: SocketAddr, http_addr: SocketAddr) -> Self {
        Self {
            self_info: StargateInfo {
                stargate_id: id.to_string(),
                advertise_addr: grpc_addr.to_string(),
                http_advertise_addr: http_addr.to_string(),
                grpc_pylon_dial_addr: String::new(),
            },
        }
    }
}

#[async_trait::async_trait]
impl Discovery for SelfDiscovery {
    fn initial_stargates(&self) -> Vec<StargateInfo> {
        vec![self.self_info.clone()]
    }

    async fn discover_stargates(&self) -> Vec<StargateInfo> {
        vec![self.self_info.clone()]
    }
}

/// Backed by a shared peer list. Each stargate registers itself on creation;
/// `discover_stargates` returns the full set. Used for multi-stargate tests.
pub struct SharedDiscovery {
    self_info: StargateInfo,
    peers: Arc<Mutex<Vec<StargateInfo>>>,
}

impl SharedDiscovery {
    pub fn new(
        id: &str,
        grpc_addr: SocketAddr,
        http_addr: SocketAddr,
        peers: Arc<Mutex<Vec<StargateInfo>>>,
    ) -> Self {
        let self_info = StargateInfo {
            stargate_id: id.to_string(),
            advertise_addr: grpc_addr.to_string(),
            http_advertise_addr: http_addr.to_string(),
            grpc_pylon_dial_addr: String::new(),
        };
        peers.lock().unwrap().push(self_info.clone());
        Self { self_info, peers }
    }
}

#[async_trait::async_trait]
impl Discovery for SharedDiscovery {
    fn initial_stargates(&self) -> Vec<StargateInfo> {
        vec![self.self_info.clone()]
    }

    async fn discover_stargates(&self) -> Vec<StargateInfo> {
        self.peers.lock().unwrap().clone()
    }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

pub fn ephemeral_addr() -> SocketAddr {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    listener.local_addr().unwrap()
}

pub fn bind_ephemeral() -> (SocketAddr, std::net::TcpListener) {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let addr = listener.local_addr().unwrap();
    (addr, listener)
}

pub fn bind_ephemeral_udp() -> (SocketAddr, std::net::UdpSocket) {
    let socket = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
    let addr = socket.local_addr().unwrap();
    (addr, socket)
}

pub fn with_proxy_headers(
    builder: reqwest::RequestBuilder,
    model: &str,
    request_id: &str,
) -> reqwest::RequestBuilder {
    with_proxy_headers_input_tokens(builder, model, request_id, 1)
}

pub fn with_proxy_headers_input_tokens(
    builder: reqwest::RequestBuilder,
    model: &str,
    request_id: &str,
    input_tokens: u64,
) -> reqwest::RequestBuilder {
    builder
        .header("x-model", model)
        .header("x-request-id", request_id)
        .header("x-input-tokens", input_tokens.to_string())
}

pub async fn wait_until<T, E, Fut, F>(
    label: &str,
    timeout: Duration,
    poll_interval: Duration,
    mut poll: F,
) -> T
where
    E: Debug,
    F: FnMut() -> Fut,
    Fut: Future<Output = Result<T, E>>,
{
    let deadline = tokio::time::Instant::now() + timeout;

    loop {
        match poll().await {
            Ok(value) => return value,
            Err(error) => {
                if tokio::time::Instant::now() >= deadline {
                    panic!(
                        "{label} did not become true within {}ms; last observed state: {error:?}",
                        timeout.as_millis()
                    );
                }

                tokio::time::sleep(poll_interval).await;
            }
        }
    }
}

pub async fn wait_for_inference_server_ids(
    http_addr: SocketAddr,
    model: &str,
    request_id_prefix: &str,
    expected_count: usize,
    timeout: Duration,
    poll_interval: Duration,
) -> HashSet<String> {
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    let seen = Arc::new(Mutex::new(HashSet::new()));
    let attempt = Arc::new(AtomicUsize::new(0));

    wait_until(
        &format!("observe {expected_count} inference server ids for model '{model}'"),
        timeout,
        poll_interval,
        || {
            let body = body.clone();
            let http_client = http_client.clone();
            let stargate_url = stargate_url.clone();
            let seen = seen.clone();
            let request_id = format!(
                "{request_id_prefix}-{}",
                attempt.fetch_add(1, Ordering::Relaxed)
            );
            async move {
                let resp = with_proxy_headers(http_client.post(stargate_url), model, &request_id)
                    .header("content-type", "application/json")
                    .json(&body)
                    .send()
                    .await;
                let resp = match resp {
                    Ok(resp) if resp.status().is_success() => resp,
                    Ok(resp) => return Err(format!("status {}", resp.status())),
                    Err(error) => return Err(error.to_string()),
                };
                let Some(server_id) = resp
                    .headers()
                    .get("x-inference-server-id")
                    .and_then(|value| value.to_str().ok())
                    .map(str::to_owned)
                else {
                    return Err("missing x-inference-server-id".to_string());
                };
                let snapshot = {
                    let mut seen = seen.lock().expect("seen server id set poisoned");
                    seen.insert(server_id);
                    seen.clone()
                };
                if snapshot.len() >= expected_count {
                    Ok(snapshot)
                } else {
                    Err(format!("seen server ids {snapshot:?}"))
                }
            }
        },
    )
    .await
}

pub fn with_proxy_headers_rk(
    builder: reqwest::RequestBuilder,
    routing_key: &str,
    model: &str,
    request_id: &str,
) -> reqwest::RequestBuilder {
    with_proxy_headers(builder, model, request_id).header("x-routing-key", routing_key)
}

pub fn with_proxy_headers_cache_affinity(
    builder: reqwest::RequestBuilder,
    model: &str,
    request_id: &str,
    cache_affinity_key: &str,
) -> reqwest::RequestBuilder {
    with_proxy_headers(builder, model, request_id)
        .header("x-cache-affinity-key", cache_affinity_key)
}

#[derive(Clone)]
pub struct DummyState {
    pub model: String,
}

#[derive(Clone)]
pub struct CountingDummyState {
    pub model: String,
    pub bringup_chat_requests: Arc<AtomicUsize>,
    pub proxy_chat_requests: Arc<AtomicUsize>,
}

pub struct CountingDummyBackend {
    pub addr: SocketAddr,
    pub bringup_chat_requests: Arc<AtomicUsize>,
    pub proxy_chat_requests: Arc<AtomicUsize>,
}

impl CountingDummyBackend {
    pub fn bringup_chat_requests(&self) -> usize {
        self.bringup_chat_requests.load(Ordering::SeqCst)
    }

    pub fn proxy_chat_requests(&self) -> usize {
        self.proxy_chat_requests.load(Ordering::SeqCst)
    }
}

#[derive(Clone)]
pub struct LifecycleDummyState {
    pub model: String,
    health_ok: Arc<AtomicBool>,
    completions_ok: Arc<AtomicBool>,
    completion_tokens: Arc<AtomicU32>,
    health_requests: Arc<AtomicUsize>,
    calibration_requests: Arc<AtomicUsize>,
    canary_requests: Arc<AtomicUsize>,
    proxy_requests: Arc<AtomicUsize>,
}

pub struct LifecycleDummyBackend {
    pub addr: SocketAddr,
    health_ok: Arc<AtomicBool>,
    completions_ok: Arc<AtomicBool>,
    completion_tokens: Arc<AtomicU32>,
    health_requests: Arc<AtomicUsize>,
    calibration_requests: Arc<AtomicUsize>,
    canary_requests: Arc<AtomicUsize>,
}

impl LifecycleDummyBackend {
    pub fn set_health_ok(&self, value: bool) {
        self.health_ok.store(value, Ordering::SeqCst);
    }

    pub fn set_completions_ok(&self, value: bool) {
        self.completions_ok.store(value, Ordering::SeqCst);
    }

    pub fn set_completion_tokens(&self, value: u32) {
        self.completion_tokens.store(value, Ordering::SeqCst);
    }

    pub fn health_requests(&self) -> usize {
        self.health_requests.load(Ordering::SeqCst)
    }

    pub fn calibration_requests(&self) -> usize {
        self.calibration_requests.load(Ordering::SeqCst)
    }

    pub fn canary_requests(&self) -> usize {
        self.canary_requests.load(Ordering::SeqCst)
    }
}

#[derive(Deserialize)]
pub struct ChatRequest {
    #[serde(default)]
    pub stream: Option<bool>,
    #[serde(default)]
    pub messages: Vec<serde_json::Value>,
}

#[derive(Serialize)]
pub struct ChunkCompletion {
    pub id: &'static str,
    pub object: &'static str,
    pub model: String,
    pub choices: Vec<ChunkChoice>,
}

#[derive(Serialize)]
pub struct ChunkChoice {
    pub index: u32,
    pub delta: Delta,
    pub finish_reason: Option<&'static str>,
}

#[derive(Serialize)]
pub struct Delta {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub role: Option<&'static str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub content: Option<&'static str>,
}

pub async fn dummy_chat(State(state): State<DummyState>, Json(req): Json<ChatRequest>) -> Response {
    if req.stream == Some(true) {
        let model = state.model.clone();
        let stream = async_stream::stream! {
            yield Ok::<_, std::convert::Infallible>(Event::default().data(
                serde_json::to_string(&ChunkCompletion {
                    id: "chunk-1",
                    object: "chat.completion.chunk",
                    model: model.clone(),
                    choices: vec![ChunkChoice {
                        index: 0,
                        delta: Delta { role: Some("assistant"), content: None },
                        finish_reason: None,
                    }],
                }).unwrap(),
            ));
            for token in &["Hello", " world", "!"] {
                yield Ok(Event::default().data(
                    serde_json::to_string(&ChunkCompletion {
                        id: "chunk-1",
                        object: "chat.completion.chunk",
                        model: model.clone(),
                        choices: vec![ChunkChoice {
                            index: 0,
                            delta: Delta { role: None, content: Some(token) },
                            finish_reason: None,
                        }],
                    }).unwrap(),
                ));
            }
            yield Ok(Event::default().data(
                serde_json::to_string(&ChunkCompletion {
                    id: "chunk-1",
                    object: "chat.completion.chunk",
                    model: model.clone(),
                    choices: vec![ChunkChoice {
                        index: 0,
                        delta: Delta { role: None, content: None },
                        finish_reason: Some("stop"),
                    }],
                }).unwrap(),
            ));
            yield Ok(Event::default().data("[DONE]"));
        };
        return Sse::new(stream)
            .keep_alive(KeepAlive::default())
            .into_response();
    }

    Json(serde_json::json!({
        "id": "test-1",
        "object": "chat.completion",
        "model": state.model,
        "choices": [{
            "index": 0,
            "message": { "role": "assistant", "content": "Hello world!" },
            "finish_reason": "stop",
        }],
        "usage": {
            "prompt_tokens": req.messages.len().max(1),
            "completion_tokens": 3,
            "total_tokens": req.messages.len().max(1) + 3,
        },
    }))
    .into_response()
}

pub async fn counting_dummy_chat(
    headers: HeaderMap,
    State(state): State<CountingDummyState>,
    Json(req): Json<ChatRequest>,
) -> Response {
    let is_bringup = headers
        .get("x-request-id")
        .and_then(|value| value.to_str().ok())
        .is_some_and(|request_id| request_id.starts_with("bringup-"));
    if is_bringup {
        state.bringup_chat_requests.fetch_add(1, Ordering::SeqCst);
    } else {
        state.proxy_chat_requests.fetch_add(1, Ordering::SeqCst);
    }
    dummy_chat(State(DummyState { model: state.model }), Json(req)).await
}

pub async fn lifecycle_dummy_health(State(state): State<LifecycleDummyState>) -> Response {
    state.health_requests.fetch_add(1, Ordering::SeqCst);
    if state.health_ok.load(Ordering::SeqCst) {
        "ok".into_response()
    } else {
        (StatusCode::SERVICE_UNAVAILABLE, "unhealthy").into_response()
    }
}

pub async fn lifecycle_dummy_chat(
    headers: HeaderMap,
    State(state): State<LifecycleDummyState>,
    Json(req): Json<ChatRequest>,
) -> Response {
    let is_bringup = headers
        .get("x-request-id")
        .and_then(|value| value.to_str().ok())
        .is_some_and(|request_id| request_id.starts_with("bringup-"));
    if is_bringup && is_canary_request(&req) {
        state.canary_requests.fetch_add(1, Ordering::SeqCst);
    } else if is_bringup {
        state.calibration_requests.fetch_add(1, Ordering::SeqCst);
    } else {
        state.proxy_requests.fetch_add(1, Ordering::SeqCst);
    }

    if !state.completions_ok.load(Ordering::SeqCst) {
        return (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(serde_json::json!({
                "error": {
                    "message": "forced lifecycle completion failure"
                }
            })),
        )
            .into_response();
    }

    if req.stream == Some(true) {
        return dummy_chat(State(DummyState { model: state.model }), Json(req)).await;
    }

    let completion_tokens = state.completion_tokens.load(Ordering::SeqCst);
    Json(serde_json::json!({
        "id": "test-1",
        "object": "chat.completion",
        "model": state.model,
        "choices": [{
            "index": 0,
            "message": { "role": "assistant", "content": "Hello world!" },
            "finish_reason": "stop",
        }],
        "usage": {
            "prompt_tokens": req.messages.len().max(1),
            "completion_tokens": completion_tokens,
            "total_tokens": req.messages.len().max(1) + completion_tokens as usize,
        },
    }))
    .into_response()
}

fn is_canary_request(req: &ChatRequest) -> bool {
    req.messages
        .iter()
        .any(|message| message.get("content").and_then(|content| content.as_str()) == Some("1+1="))
}

pub fn base_config(
    id: &str,
    grpc_addr: SocketAddr,
    http_addr: SocketAddr,
) -> StargateRuntimeConfig {
    StargateRuntimeConfig {
        stargate_id: id.to_string(),
        grpc_listen_addr: grpc_addr,
        model_discovery_listen_addr: "127.0.0.1:0".parse().unwrap(),
        http_listen_addr: http_addr,
        advertise_addr: grpc_addr,
        stargate_discovery_dns_name: "localhost".to_string(),
        remote_watch_stargate_urls: Vec::new(),
        grpc_pylon_dial_addr: None,
        advertised_hostname_template: None,
        pod_name: None,
        pod_namespace: None,
        dns_poll_interval: Duration::from_secs(60),
        watch_heartbeat_interval: Duration::from_secs(60),
        registration_update_idle_timeout:
            stargate::registration::DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT,
        registration_update_max_idle_timeout:
            stargate::registration::DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT,
        proxy_transport: ProxyTransportConfig {
            quic_connect_timeout: Duration::from_secs(5),
            quic_request_timeout: Duration::from_secs(10),
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: Default::default(),
            direct_quic_connections: 1,
            retry: Default::default(),
        },
        lb_config_path: None,
        metrics_prefix: stargate::metrics::DEFAULT_PREFIX.to_string(),
        reverse_tunnel_listen_addr: None,
        reverse_tunnel_pylon_dial_addr: None,
        reverse_tunnel_connect_timeout: Duration::from_secs(10),
    }
}

pub fn make_stargate_runtime(id: &str) -> (SocketAddr, SocketAddr, StargateRuntime) {
    make_stargate_runtime_with_lb(id, None)
}

pub fn make_stargate_runtime_with_watch_intervals(
    id: &str,
    discovery_poll_interval: Duration,
    watch_heartbeat_interval: Duration,
) -> (SocketAddr, SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SelfDiscovery::new(id, grpc_addr, http_addr);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.dns_poll_interval = discovery_poll_interval;
    config.watch_heartbeat_interval = watch_heartbeat_interval;
    let runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_http_listener(http_listener);
    (grpc_addr, http_addr, runtime)
}

pub fn make_stargate_runtime_with_auth(
    id: &str,
    authenticator: Arc<dyn stargate::auth::WorkerAuthenticator>,
) -> (SocketAddr, SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SelfDiscovery::new(id, grpc_addr, http_addr);
    let config = base_config(id, grpc_addr, http_addr);
    let runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_http_listener(http_listener)
        .with_authenticator(authenticator);
    (grpc_addr, http_addr, runtime)
}

pub fn make_stargate_runtime_with_model_discovery(
    id: &str,
) -> (SocketAddr, SocketAddr, SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (model_discovery_addr, model_discovery_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SelfDiscovery::new(id, grpc_addr, http_addr);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.model_discovery_listen_addr = model_discovery_addr;
    let runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_model_discovery_listener(model_discovery_listener)
        .with_http_listener(http_listener);
    (grpc_addr, model_discovery_addr, http_addr, runtime)
}

pub fn make_stargate_runtime_with_auth_and_model_discovery(
    id: &str,
    authenticator: Arc<dyn stargate::auth::WorkerAuthenticator>,
) -> (SocketAddr, SocketAddr, SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (model_discovery_addr, model_discovery_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SelfDiscovery::new(id, grpc_addr, http_addr);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.model_discovery_listen_addr = model_discovery_addr;
    let runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_model_discovery_listener(model_discovery_listener)
        .with_http_listener(http_listener)
        .with_authenticator(authenticator);
    (grpc_addr, model_discovery_addr, http_addr, runtime)
}

pub fn make_stargate_runtime_with_lb(
    id: &str,
    lb_config_path: Option<String>,
) -> (SocketAddr, SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SelfDiscovery::new(id, grpc_addr, http_addr);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.lb_config_path = lb_config_path;
    let runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_http_listener(http_listener);
    (grpc_addr, http_addr, runtime)
}

pub fn make_stargate_runtime_with_reverse(
    id: &str,
    reverse_addr: SocketAddr,
    reverse_socket: Option<std::net::UdpSocket>,
) -> (SocketAddr, SocketAddr, StargateRuntime) {
    make_stargate_runtime_with_reverse_and_lb(id, reverse_addr, reverse_socket, None)
}

pub fn make_stargate_runtime_with_reverse_and_lb(
    id: &str,
    reverse_addr: SocketAddr,
    reverse_socket: Option<std::net::UdpSocket>,
    lb_config_path: Option<String>,
) -> (SocketAddr, SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SelfDiscovery::new(id, grpc_addr, http_addr);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.advertised_hostname_template = Some("localhost".to_string());
    config.lb_config_path = lb_config_path;
    config.reverse_tunnel_listen_addr = Some(reverse_addr);
    let mut runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_http_listener(http_listener);
    if let Some(socket) = reverse_socket {
        runtime = runtime.with_reverse_tunnel_socket(socket);
    }
    (grpc_addr, http_addr, runtime)
}

pub fn make_stargate_runtime_with_reverse_and_auth(
    id: &str,
    reverse_addr: SocketAddr,
    reverse_socket: Option<std::net::UdpSocket>,
    authenticator: Arc<dyn stargate::auth::WorkerAuthenticator>,
) -> (SocketAddr, SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SelfDiscovery::new(id, grpc_addr, http_addr);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.advertised_hostname_template = Some("localhost".to_string());
    config.reverse_tunnel_listen_addr = Some(reverse_addr);
    let mut runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_http_listener(http_listener)
        .with_authenticator(authenticator);
    if let Some(socket) = reverse_socket {
        runtime = runtime.with_reverse_tunnel_socket(socket);
    }
    (grpc_addr, http_addr, runtime)
}

/// Creates a stargate runtime backed by a `SharedDiscovery` so that multiple
/// stargates sharing the same `peers` Arc discover each other.
pub fn make_stargate_runtime_with_shared_discovery(
    id: &str,
    peers: Arc<Mutex<Vec<StargateInfo>>>,
) -> (SocketAddr, SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SharedDiscovery::new(id, grpc_addr, http_addr, peers);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.dns_poll_interval = Duration::from_secs(1);
    let runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_http_listener(http_listener);
    (grpc_addr, http_addr, runtime)
}

/// Creates a stargate runtime backed by `SharedDiscovery` and configured with
/// additional remote region WatchStargates endpoints.
pub fn make_stargate_runtime_with_shared_discovery_and_remote_watch_urls(
    id: &str,
    peers: Arc<Mutex<Vec<StargateInfo>>>,
    remote_watch_stargate_urls: Vec<String>,
) -> (SocketAddr, SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SharedDiscovery::new(id, grpc_addr, http_addr, peers);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.dns_poll_interval = Duration::from_secs(1);
    config.remote_watch_stargate_urls = remote_watch_stargate_urls;
    let runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_http_listener(http_listener);
    (grpc_addr, http_addr, runtime)
}

/// Like `make_stargate_runtime_with_shared_discovery` but with reverse tunnel enabled.
pub fn make_stargate_runtime_with_shared_discovery_and_reverse(
    id: &str,
    peers: Arc<Mutex<Vec<StargateInfo>>>,
    reverse_addr: SocketAddr,
    reverse_socket: Option<std::net::UdpSocket>,
) -> (SocketAddr, SocketAddr, StargateRuntime) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SharedDiscovery::new(id, grpc_addr, http_addr, peers);
    let mut config = base_config(id, grpc_addr, http_addr);
    config.dns_poll_interval = Duration::from_secs(1);
    config.advertised_hostname_template = Some("localhost".to_string());
    config.reverse_tunnel_listen_addr = Some(reverse_addr);
    let mut runtime = StargateRuntime::new(config, Box::new(discovery))
        .with_grpc_listener(grpc_listener)
        .with_http_listener(http_listener);
    if let Some(socket) = reverse_socket {
        runtime = runtime.with_reverse_tunnel_socket(socket);
    }
    (grpc_addr, http_addr, runtime)
}

/// Starts a dummy HTTP backend with chat completions and health endpoints.
/// Returns the backend's `SocketAddr`.
pub async fn start_dummy_backend(model: &str) -> SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let app = Router::new()
        .route("/v1/chat/completions", post(dummy_chat))
        .route("/health", axum::routing::get(|| async { "ok" }))
        .with_state(DummyState {
            model: model.to_string(),
        });
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    addr
}

pub async fn start_counting_dummy_backend(model: &str) -> CountingDummyBackend {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let bringup_chat_requests = Arc::new(AtomicUsize::new(0));
    let proxy_chat_requests = Arc::new(AtomicUsize::new(0));
    let app = Router::new()
        .route("/v1/chat/completions", post(counting_dummy_chat))
        .route("/health", axum::routing::get(|| async { "ok" }))
        .with_state(CountingDummyState {
            model: model.to_string(),
            bringup_chat_requests: bringup_chat_requests.clone(),
            proxy_chat_requests: proxy_chat_requests.clone(),
        });
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    CountingDummyBackend {
        addr,
        bringup_chat_requests,
        proxy_chat_requests,
    }
}

pub async fn start_lifecycle_dummy_backend(model: &str) -> LifecycleDummyBackend {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let health_ok = Arc::new(AtomicBool::new(true));
    let completions_ok = Arc::new(AtomicBool::new(true));
    let completion_tokens = Arc::new(AtomicU32::new(1));
    let health_requests = Arc::new(AtomicUsize::new(0));
    let calibration_requests = Arc::new(AtomicUsize::new(0));
    let canary_requests = Arc::new(AtomicUsize::new(0));
    let proxy_requests = Arc::new(AtomicUsize::new(0));
    let app = Router::new()
        .route("/v1/chat/completions", post(lifecycle_dummy_chat))
        .route("/health", get(lifecycle_dummy_health))
        .with_state(LifecycleDummyState {
            model: model.to_string(),
            health_ok: health_ok.clone(),
            completions_ok: completions_ok.clone(),
            completion_tokens: completion_tokens.clone(),
            health_requests: health_requests.clone(),
            calibration_requests: calibration_requests.clone(),
            canary_requests: canary_requests.clone(),
            proxy_requests: proxy_requests.clone(),
        });
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    LifecycleDummyBackend {
        addr,
        health_ok,
        completions_ok,
        completion_tokens,
        health_requests,
        calibration_requests,
        canary_requests,
    }
}

/// Starts a dummy HTTP backend fronted by a QUIC HTTP tunnel.
/// Returns `(http_addr, quic_url, tunnel_handle)`. Callers must hold the
/// tunnel handle for the test's lifetime; dropping it shuts down the tunnel.
pub async fn start_dummy_inst(model: &str) -> (SocketAddr, String, QuicHttpTunnelHandle) {
    let addr = start_dummy_backend(model).await;

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{addr}"),
    ))
    .await
    .expect("tunnel failed to start");
    let tunnel_addr = tunnel.listen_addr();
    let quic_url = format!("quic://{tunnel_addr}");
    (addr, quic_url, tunnel)
}

pub async fn start_counting_dummy_inst(
    model: &str,
) -> (CountingDummyBackend, String, QuicHttpTunnelHandle) {
    let backend = start_counting_dummy_backend(model).await;

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{}", backend.addr),
    ))
    .await
    .expect("tunnel failed to start");
    let tunnel_addr = tunnel.listen_addr();
    let quic_url = format!("quic://{tunnel_addr}");
    (backend, quic_url, tunnel)
}

/// Polls the stargate `/healthz` endpoint until it responds 200.
/// Panics if not healthy within `timeout`.
pub async fn wait_for_healthy(http_addr: SocketAddr, timeout: Duration) {
    let http_client = reqwest::Client::new();
    let url = format!("http://{http_addr}/healthz");
    wait_until(
        &format!("healthz on {http_addr}"),
        timeout,
        Duration::from_millis(50),
        || {
            let http_client = http_client.clone();
            let url = url.clone();
            async move {
                match http_client.get(url).send().await {
                    Ok(r) if r.status().is_success() => Ok(()),
                    Ok(r) => Err(format!("status {}", r.status())),
                    Err(error) => Err(error.to_string()),
                }
            }
        },
    )
    .await;
}

/// Polls the stargate proxy until a request for `model` succeeds (HTTP 200).
/// Panics if routing is not established within `timeout`.
pub async fn wait_for_routing(http_addr: SocketAddr, model: &str, timeout: Duration) {
    wait_for_routing_with_rk(http_addr, None, model, timeout).await;
}

pub async fn wait_for_routing_with_cache_affinity(
    http_addr: SocketAddr,
    model: &str,
    cache_affinity_key: &str,
    timeout: Duration,
) {
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    wait_until(
        &format!("model '{model}' with cache affinity routable"),
        timeout,
        Duration::from_millis(100),
        || {
            let body = body.clone();
            let http_client = http_client.clone();
            let stargate_url = stargate_url.clone();
            async move {
                let resp = with_proxy_headers_cache_affinity(
                    http_client.post(stargate_url),
                    model,
                    "req-wait-routing-cache-affinity",
                    cache_affinity_key,
                )
                .header("content-type", "application/json")
                .json(&body)
                .send()
                .await;
                match resp {
                    Ok(r) if r.status().is_success() => Ok(()),
                    Ok(r) => Err(format!("status {}", r.status())),
                    Err(error) => Err(error.to_string()),
                }
            }
        },
    )
    .await;
}

/// Like [`wait_for_routing`] but sends the given `x-routing-key` header.
pub async fn wait_for_routing_with_rk(
    http_addr: SocketAddr,
    routing_key: Option<&str>,
    model: &str,
    timeout: Duration,
) {
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    wait_until(
        &format!("model '{model}' routable"),
        timeout,
        Duration::from_millis(100),
        || {
            let body = body.clone();
            let http_client = http_client.clone();
            let stargate_url = stargate_url.clone();
            async move {
                let mut builder =
                    with_proxy_headers(http_client.post(stargate_url), model, "req-wait-routing");
                if let Some(rk) = routing_key {
                    builder = builder.header("x-routing-key", rk);
                }
                let resp = builder
                    .header("content-type", "application/json")
                    .json(&body)
                    .send()
                    .await;
                match resp {
                    Ok(r) if r.status().is_success() => Ok(()),
                    Ok(r) => Err(format!("status {}", r.status())),
                    Err(error) => Err(error.to_string()),
                }
            }
        },
    )
    .await;
}

/// Sends `probe_count` streaming chat requests and checks that each successful
/// response has `x-inference-server-id` equal to `expected_inference_server_id`.
/// Repeats with a short poll interval until that holds or `timeout` elapses (covers async
/// stats propagation from registration clients into routing snapshots).
///
/// Each response body is fully consumed so connections do not stall when many
/// probes run in a loop (streaming SSE otherwise holds the socket open).
pub async fn wait_for_all_probes_routed_to(
    http_addr: SocketAddr,
    model: &str,
    request_id_prefix: &str,
    expected_inference_server_id: &str,
    probe_count: usize,
    timeout: Duration,
) {
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    let drain_timeout = Duration::from_secs(15);
    let deadline = tokio::time::Instant::now() + timeout;
    let mut attempt = 0u64;
    let mut poll = tokio::time::interval(Duration::from_millis(50));
    loop {
        let mut matches = 0usize;
        for i in 0..probe_count {
            let req_id = format!("{request_id_prefix}-a{attempt}-i{i}");
            // These probes wait for routing state to converge; use zero input
            // tokens so the probe traffic does not perturb LB queue pressure.
            let resp =
                with_proxy_headers_input_tokens(http_client.post(&stargate_url), model, &req_id, 0)
                    .header("content-type", "application/json")
                    .json(&body)
                    .send()
                    .await;
            let Ok(r) = resp else {
                continue;
            };
            let status = r.status();
            let server_id = r
                .headers()
                .get("x-inference-server-id")
                .and_then(|v| v.to_str().ok())
                .map(str::to_owned);
            let _ = tokio::time::timeout(drain_timeout, r.bytes()).await;
            if status.is_success() && server_id.as_deref() == Some(expected_inference_server_id) {
                matches += 1;
            }
        }
        if matches == probe_count {
            return;
        }
        if tokio::time::Instant::now() >= deadline {
            panic!(
                "within {}s, never got {probe_count} successful proxy requests for model '{model}' \
                 all routed to '{expected_inference_server_id}' (last batch matched {matches}/{probe_count})",
                timeout.as_secs()
            );
        }
        attempt += 1;
        poll.tick().await;
    }
}

/// Like [`wait_for_all_probes_routed_to`] but a single pass (no retry). Fails the
/// test if any request is unsuccessful or routed elsewhere. Drains streaming bodies.
pub async fn assert_all_probes_routed_to(
    http_addr: SocketAddr,
    model: &str,
    request_id_prefix: &str,
    expected_inference_server_id: &str,
    probe_count: usize,
) {
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    let drain_timeout = Duration::from_secs(15);
    for i in 0..probe_count {
        let req_id = format!("{request_id_prefix}-assert-{i}");
        // These probes assert routing choices; use zero input tokens so the
        // assertion traffic does not alter the LB state it is checking.
        let r = with_proxy_headers_input_tokens(http_client.post(&stargate_url), model, &req_id, 0)
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await
            .expect("request failed");
        let status = r.status();
        let server_id = r
            .headers()
            .get("x-inference-server-id")
            .and_then(|v| v.to_str().ok())
            .map(str::to_owned);
        let _ = tokio::time::timeout(drain_timeout, r.bytes()).await;
        assert!(status.is_success(), "probe {i} status {status}");
        assert_eq!(
            server_id.as_deref(),
            Some(expected_inference_server_id),
            "probe {i} x-inference-server-id"
        );
    }
}

/// Polls the stargate proxy until a request for `model` is no longer routable.
/// Panics if the model is still routable after `timeout`.
pub async fn wait_for_unroutable(http_addr: SocketAddr, model: &str, timeout: Duration) {
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    wait_until(
        &format!("model '{model}' unroutable"),
        timeout,
        Duration::from_millis(100),
        || {
            let body = body.clone();
            let http_client = http_client.clone();
            let stargate_url = stargate_url.clone();
            async move {
                let resp = with_proxy_headers(
                    http_client.post(stargate_url),
                    model,
                    "req-wait-unroutable",
                )
                .header("content-type", "application/json")
                .json(&body)
                .send()
                .await;
                match resp {
                    Ok(r) if matches!(r.status().as_u16(), 404 | 502 | 503) => Ok(()),
                    Err(_) => Ok(()),
                    Ok(r) => Err(format!("status {}", r.status())),
                }
            }
        },
    )
    .await;
}

pub fn init_crypto() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
}

pub struct BackendHandle {
    reg_client: InferenceServerRegistrationClient,
    _channels: InferenceServerUpdateChannels,
    _tunnel: Option<QuicHttpTunnelHandle>,
}

impl BackendHandle {
    pub fn stop(&mut self) {
        self.reg_client.stop();
    }
}

/// Starts a dummy backend, optionally wraps it with a QUIC tunnel (when
/// `reverse_tunnel` is false), and registers it with the given stargate seeds.
pub async fn start_and_register_backend(
    seeds: &[String],
    backend_id: &str,
    model: &str,
    reverse_tunnel: bool,
) -> BackendHandle {
    start_and_register_backend_with_bringup(
        seeds,
        backend_id,
        model,
        reverse_tunnel,
        // These tests exercise routing/discovery behavior, not calibration, so
        // skip bringup to keep registration deterministic by default.
        BringupConfig {
            enabled: false,
            ..BringupConfig::default()
        },
    )
    .await
}

pub async fn start_and_register_backend_with_bringup(
    seeds: &[String],
    backend_id: &str,
    model: &str,
    reverse_tunnel: bool,
    bringup: BringupConfig,
) -> BackendHandle {
    let backend_addr = start_dummy_backend(model).await;

    let (inference_server_url, upstream_http_base_url, tunnel) = if reverse_tunnel {
        let url = format!("http://{backend_addr}");
        (url.clone(), url, None)
    } else {
        let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
            "127.0.0.1:0".parse().unwrap(),
            format!("http://{backend_addr}"),
        ))
        .await
        .expect("tunnel failed to start");
        let quic_url = format!("quic://{}", tunnel.listen_addr());
        (quic_url, format!("http://{backend_addr}"), Some(tunnel))
    };

    let mut reg_client = InferenceServerRegistrationClient::default();
    let channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: seeds.to_vec(),
                inference_server_id: backend_id.to_string(),
                cluster_id: String::new(),
                inference_server_url,
                upstream_http_base_url: Some(upstream_http_base_url),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel,
                bringup,
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

    BackendHandle {
        reg_client,
        _channels: channels,
        _tunnel: tunnel,
    }
}

/// A test `ForwardingResolver` that maps known hostnames to actual socket
/// addresses. Peers are registered via `insert`; lookups that don't match
/// any peer return `NotPeer`, and lookups that match self return `Local`.
pub struct MapResolver {
    self_host: String,
    peers: std::collections::HashMap<String, SocketAddr>,
}

impl MapResolver {
    pub fn new(self_host: &str) -> Self {
        Self {
            self_host: self_host.to_string(),
            peers: std::collections::HashMap::new(),
        }
    }

    pub fn insert(&mut self, host: &str, addr: SocketAddr) {
        self.peers.insert(host.to_string(), addr);
    }
}

impl ForwardingResolver for MapResolver {
    fn resolve_peer(&self, host: &str, _port: u16) -> PeerResolution {
        if host == self.self_host {
            return PeerResolution::Local;
        }
        self.peers
            .get(host)
            .map(|addr| {
                PeerResolution::Peer(PeerTarget {
                    dial_addr: addr.to_string(),
                    server_name: host.to_string(),
                })
            })
            .unwrap_or(PeerResolution::NotPeer)
    }
}

/// Asserts that requests for each model route to the expected backend through
/// every stargate HTTP address. Sends `requests_per_route` requests per
/// (model, http_addr) pair and checks `x-inference-server-id`.
pub async fn assert_model_routing(
    http_addrs: &[SocketAddr],
    routes: &[(&str, &str)],
    requests_per_route: usize,
) {
    let http_client = reqwest::Client::new();
    for &http_addr in http_addrs {
        for &(model, expected_backend) in routes {
            let body = serde_json::json!({
                "model": model,
                "messages": [{"role": "user", "content": "hi"}],
                "stream": true,
            });
            for i in 0..requests_per_route {
                let resp = with_proxy_headers(
                    http_client.post(format!("http://{http_addr}/v1/chat/completions")),
                    model,
                    &format!("routing-{model}-{http_addr}-{i}"),
                )
                .header("content-type", "application/json")
                .json(&body)
                .send()
                .await
                .expect("request failed");
                assert_eq!(
                    resp.status(),
                    200,
                    "{model} request #{i} to {http_addr} failed with {}",
                    resp.status()
                );
                let server_id = resp
                    .headers()
                    .get("x-inference-server-id")
                    .expect("missing x-inference-server-id")
                    .to_str()
                    .unwrap();
                assert_eq!(
                    server_id, expected_backend,
                    "{model} request #{i} to {http_addr} routed to {server_id}, expected {expected_backend}"
                );
            }
        }
    }
}

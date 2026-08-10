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
use std::time::Duration;

use anyhow::Result;
use axum::extract::Request;
use axum::http::{HeaderMap, HeaderName, HeaderValue, Method, StatusCode};
use axum::routing::{get, post};
use axum::{Router, body::Body};
use futures::{StreamExt, future};
use quinn::{ClientConfig, Connection, Endpoint};
use tokio::net::TcpListener;

use stargate_protocol::{
    RecvStream, SendStream, TunnelTransportProtocol, WebTransportHttpResponseHead,
};

use super::body::RequestBodySendTask;
use super::connection::TunnelConnection;
use super::endpoint::{build_client_config, build_server_config};
use super::http3::{
    H3ServerConnection, H3ServerRequestStream, should_forward_h3_tunnel_request_header,
};
use super::{
    EnsureConnectedResult, QuicHttpProxy, QuicTunnelConfig, RegistrationTunnel, StreamingResponse,
};
use crate::routing_state::{
    RegistrationGeneration, RegistrationIdentity, RunningRegistration, StargateState,
    test_registration_generation,
};
use pylon_lib::{
    QuicHttpTunnelConfig, ReverseQuicTunnelConfig, start_quic_http_tunnel,
    start_reverse_quic_tunnel,
};
use stargate_runtime::CriticalTaskGroup;

const INFERENCE_SERVER_ID: &str = "test-backend";

macro_rules! tunnel_tests {
    ($($name:ident => $helper:ident($protocol:ident $(, $argument:expr)*)),+ $(,)?) => {
        $(
            #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
            async fn $name() {
                $helper(TunnelTransportProtocol::$protocol $(, $argument)*).await;
            }
        )+
    };
}

tunnel_tests! {
    direct_raw_quic_tunnel_preserves_request_head => assert_direct_tunnel_preserves_request_head(RawQuic),
    direct_http3_tunnel_preserves_request_head => assert_direct_tunnel_preserves_request_head(Http3),
    direct_webtransport_tunnel_preserves_request_head => assert_direct_tunnel_preserves_request_head(WebTransport),
    direct_http3_tunnel_proxies_request_to_upstream => assert_direct_model_proxy(Http3, "req-h3-direct", "model-h3", "/v1/models?source=http3"),
    direct_webtransport_tunnel_proxies_request_to_upstream => assert_direct_model_proxy(WebTransport, "req-wt-direct", "model-wt", "/v1/models?source=webtransport"),
    direct_http3_response_body_survives_generation_retirement => assert_response_body_survives_generation_retirement(Http3, "req-h3-evict-body", "model-h3"),
    direct_webtransport_response_body_survives_generation_retirement => assert_response_body_survives_generation_retirement(WebTransport, "req-wt-evict-body", "model-wt"),
    reverse_http3_tunnel_proxies_request_to_upstream => assert_reverse_model_proxy(Http3, "req-h3-reverse", "model-h3", "/v1/models?source=reverse-http3"),
    reverse_webtransport_tunnel_proxies_request_to_upstream => assert_reverse_model_proxy(WebTransport, "req-wt-reverse", "model-wt", "/v1/models?source=reverse-webtransport"),
    raw_quic_tunnel_tls_configs_do_not_negotiate_alpn => assert_tunnel_alpn(RawQuic, None),
    http3_tunnel_tls_configs_negotiate_h3_alpn => assert_tunnel_alpn(Http3, Some(b"h3".to_vec())),
    raw_quic_tunnel_returns_body_send_error_before_header_timeout => body_send_error_is_returned_before_header_timeout(RawQuic),
    http3_tunnel_does_not_wait_for_header_timeout_after_body_send_error => body_send_error_is_returned_before_header_timeout(Http3),
    webtransport_tunnel_returns_body_send_error_before_header_timeout => body_send_error_is_returned_before_header_timeout(WebTransport),
    raw_quic_tunnel_reports_body_send_error_at_response_eof => body_send_error_is_returned_at_response_eof(RawQuic),
    http3_tunnel_reports_body_send_error_at_response_eof => body_send_error_is_returned_at_response_eof(Http3),
    webtransport_tunnel_reports_body_send_error_at_response_eof => body_send_error_is_returned_at_response_eof(WebTransport),
    raw_quic_tunnel_does_not_wait_forever_for_stalled_request_body_at_response_eof => stalled_body_send_does_not_block_response_eof(RawQuic),
}

fn install_crypto_provider() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
}

fn server_endpoint(server_config: quinn::ServerConfig) -> Endpoint {
    Endpoint::server(server_config, "127.0.0.1:0".parse().unwrap()).unwrap()
}

fn test_server_endpoint(
    tunnel_protocol: TunnelTransportProtocol,
    configure: impl FnOnce(&mut quinn::ServerConfig),
) -> Endpoint {
    install_crypto_provider();
    let mut config = build_server_config(
        &stargate_tls::ServerTlsIdentity::SelfSigned,
        tunnel_protocol,
    )
    .expect("test server config should build");
    configure(&mut config);
    server_endpoint(config)
}

struct DropNotifier(Option<tokio::sync::oneshot::Sender<()>>);

impl Drop for DropNotifier {
    fn drop(&mut self) {
        if let Some(tx) = self.0.take() {
            let _ = tx.send(());
        }
    }
}

async fn spawn_mock_backend(app: Router) -> String {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });
    format!("http://{addr}")
}

fn health_backend(body: &'static str) -> Router {
    Router::new().route("/health", get(move || async move { body }))
}

fn model_echo_backend() -> Router {
    Router::new().route(
        "/v1/models",
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
                [(http::header::CONTENT_TYPE, "application/json")],
                format!(r#"{{"model":"{model}","body_len":{}}}"#, body.len()),
            )
        }),
    )
}

async fn response_body(mut response: StreamingResponse) -> Vec<u8> {
    let mut body = Vec::new();
    while let Some(chunk) = response.body_stream.recv_body().await.unwrap() {
        body.extend_from_slice(&chunk);
    }
    body
}

async fn response_json(response: StreamingResponse) -> serde_json::Value {
    serde_json::from_slice(&response_body(response).await).unwrap()
}

fn test_quic_proxy(tunnel_protocol: TunnelTransportProtocol) -> Arc<QuicHttpProxy> {
    test_quic_proxy_with(tunnel_protocol, |_| {})
}

fn test_quic_proxy_with(
    tunnel_protocol: TunnelTransportProtocol,
    configure: impl FnOnce(&mut QuicTunnelConfig),
) -> Arc<QuicHttpProxy> {
    let mut config = QuicTunnelConfig {
        connect_timeout: Duration::from_secs(5),
        request_timeout: Duration::from_secs(5),
        direct_quic_connections: 1,
        tls_cert_pem: None,
        server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
        quic_insecure: true,
        tunnel_protocol,
    };
    configure(&mut config);
    Arc::new(
        QuicHttpProxy::new(config, Arc::new(crate::auth::OpenAuthenticator))
            .expect("test QUIC proxy should initialize"),
    )
}

async fn start_test_quic_tunnel(
    backend_url: String,
    tunnel_protocol: TunnelTransportProtocol,
) -> pylon_lib::QuicHttpTunnelHandle {
    let mut config = QuicHttpTunnelConfig::new("127.0.0.1:0".parse().unwrap(), backend_url);
    config.tunnel_protocol = tunnel_protocol;
    start_quic_http_tunnel(config).await.unwrap()
}

fn test_request_headers(request_id: &str, model: &str, input_tokens: &str) -> HeaderMap {
    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", request_id.parse().unwrap());
    headers.insert("x-model", model.parse().unwrap());
    headers.insert("x-input-tokens", input_tokens.parse().unwrap());
    headers
}

fn register_backend(state: &StargateState, id: &str, reverse_tunnel: bool) -> RunningRegistration {
    let identity = RegistrationIdentity {
        inference_server_id: id.to_string(),
        cluster_id: id.to_string(),
        inference_server_url: "http://127.0.0.1:1".to_string(),
        routing_key: None,
        reverse_tunnel,
    };
    state.begin_registration(&identity).unwrap()
}

fn test_generation(
    id: &str,
    target_url: &str,
    reverse_tunnel: bool,
) -> Arc<RegistrationGeneration> {
    test_registration_generation(RegistrationIdentity {
        inference_server_id: id.to_string(),
        cluster_id: id.to_string(),
        inference_server_url: target_url.to_string(),
        routing_key: None,
        reverse_tunnel,
    })
}

async fn connect_direct_registration(
    proxy: &QuicHttpProxy,
    target_url: &str,
) -> Arc<RegistrationGeneration> {
    let registration = test_generation(INFERENCE_SERVER_ID, target_url, false);
    proxy
        .connect_direct_registration(&registration)
        .await
        .expect("direct registration should connect");
    registration
}

struct DirectTunnelFixture {
    proxy: Arc<QuicHttpProxy>,
    registration: Arc<RegistrationGeneration>,
    tunnel: pylon_lib::QuicHttpTunnelHandle,
}

impl DirectTunnelFixture {
    async fn start(app: Router, tunnel_protocol: TunnelTransportProtocol) -> Self {
        Self::start_with_connections(app, tunnel_protocol, 1).await
    }

    async fn start_with_connections(
        app: Router,
        tunnel_protocol: TunnelTransportProtocol,
        connection_count: usize,
    ) -> Self {
        install_crypto_provider();
        let backend_url = spawn_mock_backend(app).await;
        let tunnel = start_test_quic_tunnel(backend_url, tunnel_protocol).await;
        let proxy = test_quic_proxy_with(tunnel_protocol, |config| {
            // Keep test failures bounded below the outer harness timeout.
            config.request_timeout = Duration::from_secs(2);
            config.direct_quic_connections = connection_count;
        });
        let registration =
            connect_direct_registration(&proxy, &format!("quic://{}", tunnel.listen_addr())).await;
        Self {
            proxy,
            registration,
            tunnel,
        }
    }

    fn target_url(&self) -> String {
        format!("quic://{}", self.tunnel.listen_addr())
    }

    fn assert_connection_state(&self, expected: usize, needs_replenishment: bool) {
        let connections = self
            .registration
            .tunnel_connections()
            .connection_set()
            .expect("pooled connection");
        assert!(connections.is_healthy());
        assert_eq!(connections.len(), expected);
        assert_eq!(
            self.proxy
                .connection_set_needs_replenishment(&self.registration),
            needs_replenishment
        );
    }

    async fn shutdown(self) {
        self.tunnel.shutdown().await;
    }
}

fn raw_connection(registration: &RegistrationGeneration, index: usize) -> Connection {
    match registration
        .tunnel_connections()
        .connection_set()
        .expect("pooled connection")
        .connection(index)
    {
        TunnelConnection::RawQuic(handle) => handle.connection().clone(),
        TunnelConnection::Http3(_) | TunnelConnection::WebTransport(_) => {
            panic!("expected raw QUIC tunnel connection")
        }
    }
}

fn own_reverse_registration(
    proxy: Arc<QuicHttpProxy>,
    registration: &RunningRegistration,
) -> RegistrationTunnel {
    RegistrationTunnel::reverse(proxy, registration.generation(), Duration::from_millis(100))
}

async fn start_tunnel_server(
    state: Arc<StargateState>,
    tunnel_protocol: TunnelTransportProtocol,
) -> (Arc<QuicHttpProxy>, SocketAddr, CriticalTaskGroup) {
    install_crypto_provider();
    let proxy = test_quic_proxy(tunnel_protocol);
    let (tasks, _failures) = CriticalTaskGroup::new("stargate test");
    let addr = proxy
        .start_reverse_listener(
            state,
            tasks.clone(),
            None,
            std::net::UdpSocket::bind("127.0.0.1:0").expect("reverse listener socket should bind"),
        )
        .await
        .expect("reverse listener should start");
    (proxy, addr, tasks)
}

#[tokio::test]
async fn reverse_listener_shutdown_waits_for_stalled_dispatch_task() {
    let state = Arc::new(StargateState::new());
    let (_proxy, addr, tasks) = start_tunnel_server(state, TunnelTransportProtocol::RawQuic).await;

    let mut client_endpoint = Endpoint::client(
        "127.0.0.1:0"
            .parse()
            .expect("valid test client bind address"),
    )
    .expect("client endpoint should start");
    client_endpoint.set_default_client_config(
        build_client_config(None, true, TunnelTransportProtocol::RawQuic).expect("client config"),
    );
    let connection = client_endpoint
        .connect(addr, "stargate")
        .expect("connect")
        .await
        .expect("reverse listener accepts connection");

    tasks.begin_shutdown();
    tokio::time::timeout(Duration::from_secs(2), tasks.wait())
        .await
        .expect("tracked reverse listener tasks should exit on shutdown");
    tokio::time::timeout(Duration::from_secs(2), connection.closed())
        .await
        .expect("client connection should observe listener shutdown");
    client_endpoint.close(0u32.into(), b"test complete");
}

async fn negotiate_alpn(
    client_config: ClientConfig,
    server_config: quinn::ServerConfig,
) -> Option<Vec<u8>> {
    let server_endpoint = server_endpoint(server_config);
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

async fn assert_tunnel_alpn(tunnel_protocol: TunnelTransportProtocol, expected: Option<Vec<u8>>) {
    install_crypto_provider();
    let client_config = build_client_config(None, true, tunnel_protocol).expect("client config");
    let server_config = build_server_config(
        &stargate_tls::ServerTlsIdentity::SelfSigned,
        tunnel_protocol,
    )
    .expect("server config");
    assert_eq!(negotiate_alpn(client_config, server_config).await, expected);
}

async fn connect_reverse_tunnel(
    listener_addr: SocketAddr,
    backend_url: &str,
) -> pylon_lib::ReverseQuicTunnelHandle {
    connect_reverse_tunnel_insecure_with_protocol(
        listener_addr,
        backend_url,
        INFERENCE_SERVER_ID,
        TunnelTransportProtocol::RawQuic,
    )
    .await
}

async fn connect_reverse_tunnel_insecure_with_protocol(
    listener_addr: SocketAddr,
    backend_url: &str,
    server_id: &str,
    tunnel_protocol: TunnelTransportProtocol,
) -> pylon_lib::ReverseQuicTunnelHandle {
    let mut config = reverse_tunnel_config(listener_addr, server_id, backend_url.to_string());
    config.quic_insecure = true;
    config.tunnel_protocol = tunnel_protocol;
    start_reverse_quic_tunnel(config).await.unwrap()
}

fn reverse_tunnel_config(
    listener_addr: SocketAddr,
    server_id: &str,
    backend_url: String,
) -> ReverseQuicTunnelConfig {
    ReverseQuicTunnelConfig::new(
        format!("127.0.0.1:{}", listener_addr.port()),
        server_id.to_string(),
        backend_url,
    )
}

struct ReverseTunnelFixture {
    proxy: Arc<QuicHttpProxy>,
    generation: Arc<RegistrationGeneration>,
    addr: SocketAddr,
    handle: pylon_lib::ReverseQuicTunnelHandle,
    runtime: CriticalTaskGroup,
    _owner: RegistrationTunnel,
}

impl ReverseTunnelFixture {
    async fn start(app: Router, tunnel_protocol: TunnelTransportProtocol) -> Self {
        let backend_url = spawn_mock_backend(app).await;
        let state = Arc::new(StargateState::new());
        let registration = register_backend(&state, INFERENCE_SERVER_ID, true);
        let generation = registration.generation();
        let (proxy, addr, runtime) = start_tunnel_server(state, tunnel_protocol).await;
        let owner = own_reverse_registration(proxy.clone(), &registration);
        let handle = tokio::time::timeout(
            Duration::from_secs(3),
            connect_reverse_tunnel_insecure_with_protocol(
                addr,
                &backend_url,
                INFERENCE_SERVER_ID,
                tunnel_protocol,
            ),
        )
        .await
        .expect("reverse tunnel handshake timed out");
        assert!(
            proxy
                .await_reverse_connection(generation.clone(), Duration::from_secs(2))
                .await
        );
        Self {
            proxy,
            generation,
            addr,
            handle,
            runtime,
            _owner: owner,
        }
    }

    async fn shutdown(self) {
        self.handle.shutdown().await;
        self.runtime.begin_shutdown();
    }
}

async fn assert_model_proxy(
    proxy: &QuicHttpProxy,
    generation: &Arc<RegistrationGeneration>,
    request_id: &str,
    model: &str,
    path: &str,
) {
    let response = post_model_json(proxy, generation, request_id, model, path)
        .await
        .unwrap();
    assert_model_echo(response, model).await;
}

async fn assert_direct_model_proxy(
    tunnel_protocol: TunnelTransportProtocol,
    request_id: &str,
    model: &str,
    path: &str,
) {
    let fixture = DirectTunnelFixture::start(model_echo_backend(), tunnel_protocol).await;
    let response = tokio::time::timeout(
        Duration::from_secs(3),
        assert_model_proxy(
            &fixture.proxy,
            &fixture.registration,
            request_id,
            model,
            path,
        ),
    )
    .await;
    assert!(response.is_ok(), "direct proxy request timed out");
    fixture.shutdown().await;
}

async fn assert_reverse_model_proxy(
    tunnel_protocol: TunnelTransportProtocol,
    request_id: &str,
    model: &str,
    path: &str,
) {
    let fixture = ReverseTunnelFixture::start(model_echo_backend(), tunnel_protocol).await;
    assert_model_proxy(&fixture.proxy, &fixture.generation, request_id, model, path).await;
    fixture.shutdown().await;
}

async fn post_model_json(
    proxy: &QuicHttpProxy,
    generation: &Arc<RegistrationGeneration>,
    request_id: &str,
    model: &str,
    path: &str,
) -> anyhow::Result<StreamingResponse> {
    let mut headers = test_request_headers(request_id, model, "7");
    headers.insert("content-type", HeaderValue::from_static("application/json"));
    proxy
        .proxy_request_streaming(
            generation,
            Method::POST,
            path,
            headers,
            Body::from(r#"{"ping":true}"#),
        )
        .await
}

async fn post_chat(
    proxy: &QuicHttpProxy,
    generation: &Arc<RegistrationGeneration>,
    headers: HeaderMap,
    body: Body,
) -> anyhow::Result<StreamingResponse> {
    proxy
        .proxy_request_streaming(
            generation,
            Method::POST,
            "/v1/chat/completions",
            headers,
            body,
        )
        .await
}

async fn assert_model_echo(response: StreamingResponse, model: &str) {
    assert_eq!(response.status, StatusCode::OK);
    let payload = response_json(response).await;
    assert_eq!(
        payload.get("model").and_then(serde_json::Value::as_str),
        Some(model)
    );
    assert_eq!(
        payload.get("body_len").and_then(serde_json::Value::as_u64),
        Some(13)
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn reverse_tunnel_proxies_request_to_upstream() {
    let app = Router::new().route(
        "/v1/models",
        get(|req: Request| async move {
            let echo = req
                .headers()
                .get("x-model")
                .and_then(|v| v.to_str().ok())
                .unwrap_or("none")
                .to_string();
            (StatusCode::OK, format!(r#"{{"model":"{echo}"}}"#))
        }),
    );
    let fixture = ReverseTunnelFixture::start(app, TunnelTransportProtocol::RawQuic).await;

    let headers = test_request_headers("req-1", "test-model", "5");
    let response = fixture
        .proxy
        .proxy_request_streaming(
            &fixture.generation,
            Method::GET,
            "/v1/models",
            headers,
            Body::from("{}"),
        )
        .await
        .unwrap();

    assert_eq!(response.status, StatusCode::OK);
    let payload = response_json(response).await;
    assert_eq!(
        payload.get("model").and_then(serde_json::Value::as_str),
        Some("test-model")
    );

    fixture.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn reverse_webtransport_stalled_stream_header_does_not_block_later_requests() {
    let fixture =
        ReverseTunnelFixture::start(model_echo_backend(), TunnelTransportProtocol::WebTransport)
            .await;
    let webtransport = match fixture
        .generation
        .tunnel_connections()
        .connection_set()
        .expect("pooled connection")
        .choose_healthy()
        .expect("healthy connection")
    {
        TunnelConnection::WebTransport(handle) => handle.clone(),
        TunnelConnection::RawQuic(_) | TunnelConnection::Http3(_) => {
            panic!("expected WebTransport tunnel connection")
        }
    };
    let (_stalled_send, _stalled_recv) = webtransport.connection().open_bi().await.unwrap();

    let response = tokio::time::timeout(
        Duration::from_secs(3),
        post_model_json(
            &fixture.proxy,
            &fixture.generation,
            "req-wt-after-stalled",
            "model-wt",
            "/v1/models?source=reverse-webtransport-stalled",
        ),
    )
    .await
    .expect("request after stalled WebTransport stream timed out")
    .unwrap();
    assert_model_echo(response, "model-wt").await;
    fixture.shutdown().await;
}

async fn assert_direct_tunnel_preserves_request_head(tunnel_protocol: TunnelTransportProtocol) {
    let app = Router::new().route(
        "/v1/models",
        post(|req: Request| async move {
            let method = req.method().to_string();
            let path_and_query = req
                .uri()
                .path_and_query()
                .map(ToString::to_string)
                .unwrap_or_default();
            let repeated_headers = req
                .headers()
                .get_all("x-boundary-value")
                .iter()
                .map(|value| {
                    value
                        .to_str()
                        .expect("test header should be ascii")
                        .to_string()
                })
                .collect::<Vec<_>>();
            let body = axum::body::to_bytes(req.into_body(), 1024 * 1024)
                .await
                .expect("read test request body");
            (
                StatusCode::OK,
                [(http::header::CONTENT_TYPE, "application/json")],
                serde_json::json!({
                    "method": method,
                    "path_and_query": path_and_query,
                    "repeated_headers": repeated_headers,
                    "body": String::from_utf8(body.to_vec()).expect("test body should be utf-8"),
                })
                .to_string(),
            )
        }),
    );
    let fixture = DirectTunnelFixture::start(app, tunnel_protocol).await;

    let mut headers = test_request_headers("req-protocol-boundary", "model-boundary", "7");
    headers.insert("content-type", "application/json".parse().unwrap());
    headers.append("x-boundary-value", "one".parse().unwrap());
    headers.append("x-boundary-value", "two".parse().unwrap());
    let response = fixture
        .proxy
        .proxy_request_streaming(
            &fixture.registration,
            Method::POST,
            "/v1/models?source=protocol-boundary",
            headers,
            Body::from(r#"{"ping":true}"#),
        )
        .await
        .unwrap();
    assert_eq!(response.status, StatusCode::OK);
    let payload = response_json(response).await;
    assert_eq!(payload["method"], "POST");
    assert_eq!(
        payload["path_and_query"],
        "/v1/models?source=protocol-boundary"
    );
    assert_eq!(
        payload["repeated_headers"],
        serde_json::json!(["one", "two"])
    );
    assert_eq!(payload["body"], r#"{"ping":true}"#);
    fixture.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_connect_installs_configured_connection_set() {
    let fixture = DirectTunnelFixture::start_with_connections(
        health_backend("ok"),
        TunnelTransportProtocol::RawQuic,
        3,
    )
    .await;
    fixture.assert_connection_state(3, false);

    let headers = test_request_headers("req-direct-set", "model-direct-set", "7");
    let response = fixture
        .proxy
        .proxy_request_streaming(
            &fixture.registration,
            Method::GET,
            "/health",
            headers,
            Body::empty(),
        )
        .await
        .unwrap();

    assert_eq!(response.status, StatusCode::OK);

    raw_connection(&fixture.registration, 0).close(0u32.into(), b"test partial direct set close");
    fixture.assert_connection_state(3, true);

    fixture
        .proxy
        .connect_direct_registration(&fixture.registration)
        .await
        .unwrap();
    fixture.assert_connection_state(3, false);
    fixture.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn reverse_install_capability_commits_its_exact_registration() {
    let fixture =
        DirectTunnelFixture::start(health_backend("ok"), TunnelTransportProtocol::RawQuic).await;
    let connection = fixture
        .registration
        .tunnel_connections()
        .connection_set()
        .expect("source connection set")
        .choose_healthy()
        .expect("source healthy connection");

    let target = test_generation("reverse-install-target", "http://127.0.0.1:1", true);
    let abandoned = target
        .begin_reverse_connection_install()
        .expect("reverse install capability");
    // Dropping an uncommitted capability must restore install availability.
    drop(abandoned);
    let install = target
        .begin_reverse_connection_install()
        .expect("replacement reverse install capability");

    assert!(install.finish(connection));
    assert!(target.tunnel_connections().has_healthy_connection());
    assert!(
        target.begin_reverse_connection_install().is_none(),
        "healthy exact registration should reject another reverse install"
    );

    fixture.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn stale_generation_cannot_open_request_through_replacement_tunnel() {
    let fixture =
        DirectTunnelFixture::start(health_backend("ok"), TunnelTransportProtocol::RawQuic).await;
    let stale_generation = fixture.registration.clone();
    let mut stale_owner =
        RegistrationTunnel::direct(fixture.proxy.clone(), stale_generation.clone());
    assert!(matches!(
        stale_owner.ensure_connected().await,
        EnsureConnectedResult::Connected
    ));
    // Retire the stale generation before connecting its same-ID replacement.
    drop(stale_owner);
    let replacement_generation = test_generation(INFERENCE_SERVER_ID, &fixture.target_url(), false);
    let mut replacement_owner =
        RegistrationTunnel::direct(fixture.proxy.clone(), replacement_generation.clone());
    assert!(matches!(
        replacement_owner.ensure_connected().await,
        EnsureConnectedResult::Connected
    ));

    let headers = test_request_headers("req-exact-generation", "model-exact-generation", "0");
    let stale_result = fixture
        .proxy
        .proxy_request_streaming(
            &stale_generation,
            Method::GET,
            "/health",
            headers.clone(),
            Body::empty(),
        )
        .await;
    assert!(
        stale_result.is_err(),
        "stale registration generation must not borrow replacement tunnel"
    );

    let replacement_response = fixture
        .proxy
        .proxy_request_streaming(
            &replacement_generation,
            Method::GET,
            "/health",
            headers,
            Body::empty(),
        )
        .await
        .expect("replacement registration should own its tunnel");
    assert_eq!(replacement_response.status, StatusCode::OK);

    fixture.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn retired_registration_tunnel_cannot_be_reclaimed() {
    let fixture =
        DirectTunnelFixture::start(health_backend("ok"), TunnelTransportProtocol::RawQuic).await;
    let registration = fixture.registration.clone();
    let mut owner = RegistrationTunnel::direct(fixture.proxy.clone(), registration.clone());
    assert!(matches!(
        owner.ensure_connected().await,
        EnsureConnectedResult::Connected
    ));

    // Retire this generation so a later owner cannot reclaim its tunnel.
    drop(owner);

    let mut stale_owner = RegistrationTunnel::direct(fixture.proxy.clone(), registration);
    assert!(matches!(
        stale_owner.ensure_connected().await,
        EnsureConnectedResult::Unavailable
    ));

    fixture.shutdown().await;
}

#[tokio::test]
async fn reverse_connection_wait_finishes_when_registration_tunnel_retires() {
    install_crypto_provider();
    let proxy = test_quic_proxy_with(TunnelTransportProtocol::RawQuic, |config| {
        config.request_timeout = Duration::from_secs(2);
    });
    let registration = test_generation(INFERENCE_SERVER_ID, "http://127.0.0.1:1", true);
    let owner =
        RegistrationTunnel::reverse(proxy.clone(), registration.clone(), Duration::from_secs(30));
    let wait = tokio::spawn(async move {
        proxy
            .await_reverse_connection(registration, Duration::from_secs(30))
            .await
    });
    tokio::task::yield_now().await;

    // Retire the generation to wake its pending reverse-connection waiter.
    drop(owner);

    let connected = tokio::time::timeout(Duration::from_millis(100), wait)
        .await
        .expect("retirement should wake the exact generation's reverse waiter")
        .expect("reverse wait task should finish");
    assert!(!connected);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn registration_tunnel_replenishes_partial_direct_connection_set() {
    let fixture = DirectTunnelFixture::start_with_connections(
        health_backend("ok"),
        TunnelTransportProtocol::RawQuic,
        2,
    )
    .await;
    let mut tunnel_owner =
        RegistrationTunnel::direct(fixture.proxy.clone(), fixture.registration.clone());
    let result = tunnel_owner.ensure_connected().await;
    assert!(matches!(result, EnsureConnectedResult::Connected));
    fixture.assert_connection_state(2, false);

    raw_connection(&fixture.registration, 0).close(0u32.into(), b"test watcher direct replenish");
    fixture.assert_connection_state(2, true);

    let result = tunnel_owner.ensure_connected().await;
    assert!(matches!(result, EnsureConnectedResult::Connected));

    fixture.assert_connection_state(2, false);
    fixture.shutdown().await;
}

#[test]
fn h3_tunnel_request_filter_strips_hop_headers_case_insensitively()
-> std::result::Result<(), axum::http::header::InvalidHeaderName> {
    for name in [b"Connection".as_slice(), b"Proxy-Connection", b"Host"] {
        assert!(!should_forward_h3_tunnel_request_header(
            &HeaderName::from_bytes(name)?
        ));
    }
    assert!(should_forward_h3_tunnel_request_header(
        &HeaderName::from_bytes(b"X-Request-Id")?
    ));
    Ok(())
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_webtransport_connect_response_uses_connect_timeout() {
    let server = test_server_endpoint(TunnelTransportProtocol::WebTransport, |_| {});
    let server_addr = server.local_addr().unwrap();
    let server_task = tokio::spawn(async move {
        let incoming = server.accept().await.expect("server should accept");
        let connection = incoming.await.expect("server connection should complete");
        let mut builder = h3::server::builder();
        builder
            .enable_webtransport(true)
            .enable_extended_connect(true)
            .enable_datagram(true)
            .max_webtransport_sessions(1);
        let mut h3_connection: H3ServerConnection = builder
            .build(h3_quinn::Connection::new(connection))
            .await
            .expect("h3 server connection");
        let resolver = h3_connection
            .accept()
            .await
            .expect("accept CONNECT")
            .expect("CONNECT request");
        let (_request, _stream) = resolver.resolve_request().await.expect("resolve CONNECT");
        futures::future::pending::<()>().await;
    });

    let proxy = test_quic_proxy_with(TunnelTransportProtocol::WebTransport, |config| {
        config.connect_timeout = Duration::from_millis(50);
        config.request_timeout = Duration::from_secs(2);
    });
    let registration =
        test_generation(INFERENCE_SERVER_ID, &format!("quic://{server_addr}"), false);
    let result = tokio::time::timeout(
        Duration::from_secs(1),
        proxy.connect_direct_registration(&registration),
    )
    .await
    .expect("preconnect should return after the connect timeout");
    let error = result.expect_err("WebTransport CONNECT response should time out");
    let error_chain = format!("{error:#}");
    assert!(
        error_chain.contains("direct tunnel setup timed out"),
        "unexpected error chain: {error_chain}"
    );

    server_task.abort();
}

async fn assert_response_body_survives_generation_retirement(
    tunnel_protocol: TunnelTransportProtocol,
    request_id: &str,
    model: &str,
) {
    let release_body = Arc::new(tokio::sync::Notify::new());
    let app = Router::new().route(
        "/v1/stream",
        post({
            let release_body = release_body.clone();
            move |_req: Request| {
                let release_body = release_body.clone();
                async move {
                    let body_stream =
                        futures::stream::once(future::ready(Ok::<_, std::convert::Infallible>(
                            bytes::Bytes::from_static(b"first-"),
                        )))
                        .chain(futures::stream::once(async move {
                            release_body.notified().await;
                            Ok::<_, std::convert::Infallible>(bytes::Bytes::from_static(b"second"))
                        }));
                    http::Response::builder()
                        .status(StatusCode::OK)
                        .body(Body::from_stream(body_stream))
                        .unwrap()
                }
            }
        }),
    );
    let fixture = DirectTunnelFixture::start(app, tunnel_protocol).await;

    let mut headers = test_request_headers(request_id, model, "7");
    headers.insert("content-type", "application/json".parse().unwrap());
    let response = fixture
        .proxy
        .proxy_request_streaming(
            &fixture.registration,
            Method::POST,
            "/v1/stream",
            headers,
            Body::from(r#"{"stream":true}"#),
        )
        .await
        .unwrap();
    assert_eq!(response.status, StatusCode::OK);

    assert!(fixture.registration.tunnel_connections().retire());
    release_body.notify_waiters();
    let body = response_body(response).await;
    assert_eq!(body, b"first-second");
    fixture.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_http3_tunnel_returns_header_errors_before_request_body_finishes() {
    install_crypto_provider();

    let tunnel = start_test_quic_tunnel(
        "http://127.0.0.1:1".to_string(),
        TunnelTransportProtocol::Http3,
    )
    .await;
    let proxy = test_quic_proxy_with(TunnelTransportProtocol::Http3, |config| {
        config.request_timeout = Duration::from_secs(2);
    });
    let registration =
        connect_direct_registration(&proxy, &format!("quic://{}", tunnel.listen_addr())).await;

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-h3-early-error".parse().unwrap());
    headers.insert("x-input-tokens", "7".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());

    let (release_body_tx, release_body_rx) = tokio::sync::oneshot::channel();
    let body_stream = futures::stream::once(future::ready(Ok::<_, std::convert::Infallible>(
        bytes::Bytes::from_static(br#"{"stream":true"#),
    )))
    .chain(futures::stream::once(async move {
        let _ = release_body_rx.await;
        Ok::<_, std::convert::Infallible>(bytes::Bytes::new())
    }));

    let response = tokio::time::timeout(
        Duration::from_millis(500),
        post_chat(
            &proxy,
            &registration,
            headers,
            Body::from_stream(body_stream),
        ),
    )
    .await
    .expect("http3 proxy should return response headers before request body finishes")
    .unwrap();
    let _ = release_body_tx.send(());

    assert_eq!(response.status, StatusCode::BAD_REQUEST);
    let payload = response_json(response).await;
    assert_eq!(payload["type"], "about:blank");
    assert_eq!(payload["title"], "Bad Request");
    assert_eq!(payload["status"], 400);
    assert_eq!(payload["detail"], "missing required x-model header");

    tunnel.shutdown().await;
}

#[tokio::test]
async fn cancelled_request_body_send_finish_aborts_upload_task() {
    let (entered_tx, entered_rx) = tokio::sync::oneshot::channel();
    let (dropped_tx, dropped_rx) = tokio::sync::oneshot::channel();
    let upload_task = tokio::spawn(async move {
        let _drop_notifier = DropNotifier(Some(dropped_tx));
        let _ = entered_tx.send(());
        std::future::pending::<Result<()>>().await
    });

    entered_rx.await.expect("upload task should start");

    {
        let finish = RequestBodySendTask::new(
            "cancelled request body",
            Duration::from_secs(30),
            upload_task,
        )
        .finish();
        tokio::pin!(finish);
        tokio::select! {
            biased;
            _ = &mut finish => panic!("pending upload should not finish before cancellation"),
            _ = tokio::task::yield_now() => {}
        }
    }

    tokio::time::timeout(Duration::from_secs(1), dropped_rx)
        .await
        .expect("cancelled EOF finalization should abort the upload task")
        .expect("upload drop notifier should send");
}

struct BodySendPeer {
    addr: SocketAddr,
    release_tx: tokio::sync::oneshot::Sender<()>,
    task: tokio::task::JoinHandle<()>,
}

impl BodySendPeer {
    async fn finish(self) {
        let _ = self.release_tx.send(());
        self.task.await.expect("body-send test peer should finish");
    }
}

enum BodySendPeerMode {
    EarlySuccess,
    Inert(tokio::sync::oneshot::Sender<()>),
}

impl BodySendPeerMode {
    fn should_reply(self) -> bool {
        match self {
            Self::EarlySuccess => true,
            Self::Inert(headers_seen_tx) => {
                let _ = headers_seen_tx.send(());
                false
            }
        }
    }
}

fn start_body_send_peer(
    tunnel_protocol: TunnelTransportProtocol,
    mode: BodySendPeerMode,
) -> BodySendPeer {
    let server = test_server_endpoint(tunnel_protocol, |_| {});
    let addr = server
        .local_addr()
        .expect("test server endpoint should expose local address");
    let (release_tx, release_rx) = tokio::sync::oneshot::channel();
    let task = tokio::spawn(async move {
        let incoming = server.accept().await.expect("server should accept");
        let connection = incoming.await.expect("server connection should complete");
        match tunnel_protocol {
            TunnelTransportProtocol::RawQuic => {
                accept_body_send_raw_quic_request(connection, mode, release_rx).await
            }
            TunnelTransportProtocol::Http3 => {
                accept_body_send_h3_request(connection, mode, release_rx).await
            }
            TunnelTransportProtocol::WebTransport => {
                accept_body_send_webtransport_request(connection, mode, release_rx).await
            }
        }
    });
    BodySendPeer {
        addr,
        release_tx,
        task,
    }
}

fn start_early_success_body_send_error_peer(
    tunnel_protocol: TunnelTransportProtocol,
) -> BodySendPeer {
    install_crypto_provider();
    start_body_send_peer(tunnel_protocol, BodySendPeerMode::EarlySuccess)
}

async fn accept_body_send_raw_quic_request(
    connection: Connection,
    mode: BodySendPeerMode,
    release_rx: tokio::sync::oneshot::Receiver<()>,
) {
    let (server_send, quinn_recv) = connection.accept_bi().await.expect("request stream");
    let mut recv_stream = RecvStream::new(quinn_recv);
    recv_stream.recv_header().await.expect("request headers");
    let mut send_stream = SendStream::new(server_send);
    if mode.should_reply() {
        let mut response_headers = HeaderMap::new();
        response_headers.insert("x-status", HeaderValue::from_static("200"));
        send_stream
            .send_header(response_headers)
            .await
            .expect("send response headers");
        send_stream.finish().expect("finish response");
    }
    let _ = release_rx.await;
}

async fn accept_body_send_h3_request(
    connection: Connection,
    mode: BodySendPeerMode,
    release_rx: tokio::sync::oneshot::Receiver<()>,
) {
    let (_h3_connection, mut stream) = accept_h3_request(connection).await;
    if mode.should_reply() {
        let response = http::Response::builder()
            .status(StatusCode::OK)
            .body(())
            .expect("build h3 response");
        stream
            .send_response(response)
            .await
            .expect("send h3 response headers");
        stream.finish().await.expect("finish h3 response");
    }
    let _ = release_rx.await;
}

async fn accept_body_send_webtransport_request(
    connection: Connection,
    mode: BodySendPeerMode,
    release_rx: tokio::sync::oneshot::Receiver<()>,
) {
    let (_h3_connection, _connect_stream, mut quinn_send, _quinn_recv) =
        accept_webtransport_request(connection).await;
    if mode.should_reply() {
        let response_head = WebTransportHttpResponseHead {
            status: StatusCode::OK,
            headers: HeaderMap::new(),
        };
        stargate_protocol::write_webtransport_http_response_head(&mut quinn_send, &response_head)
            .await
            .expect("send WebTransport response head");
        stargate_protocol::finish_webtransport_http_stream(&mut quinn_send)
            .expect("finish WebTransport response");
    }
    let _ = release_rx.await;
}

async fn accept_h3_request(connection: Connection) -> (H3ServerConnection, H3ServerRequestStream) {
    let mut h3_connection: H3ServerConnection = h3::server::builder()
        .build(h3_quinn::Connection::new(connection))
        .await
        .expect("h3 server connection");
    let resolver = h3_connection
        .accept()
        .await
        .expect("accept h3 request")
        .expect("h3 request");
    let (_request, stream) = resolver
        .resolve_request()
        .await
        .expect("resolve h3 request");
    (h3_connection, stream)
}

async fn accept_webtransport_request(
    connection: Connection,
) -> (
    H3ServerConnection,
    H3ServerRequestStream,
    quinn::SendStream,
    quinn::RecvStream,
) {
    let mut builder = h3::server::builder();
    builder
        .enable_webtransport(true)
        .enable_extended_connect(true)
        .enable_datagram(true)
        .max_webtransport_sessions(1);
    let mut h3_connection: H3ServerConnection = builder
        .build(h3_quinn::Connection::new(connection.clone()))
        .await
        .expect("WebTransport h3 server connection");
    let resolver = h3_connection
        .accept()
        .await
        .expect("accept WebTransport CONNECT")
        .expect("WebTransport CONNECT request");
    let (_request, mut connect_stream) = resolver
        .resolve_request()
        .await
        .expect("resolve WebTransport CONNECT");
    let session_id = connect_stream.id().into_inner();
    connect_stream
        .send_response(
            http::Response::builder()
                .status(StatusCode::OK)
                .body(())
                .expect("build WebTransport CONNECT response"),
        )
        .await
        .expect("send WebTransport CONNECT response");

    let (quinn_send, mut quinn_recv) = connection
        .accept_bi()
        .await
        .expect("WebTransport request stream");
    let stream_session_id = stargate_protocol::read_webtransport_bidi_header(&mut quinn_recv)
        .await
        .expect("WebTransport bidi header");
    assert_eq!(stream_session_id, session_id);
    stargate_protocol::read_webtransport_http_request_head(&mut quinn_recv)
        .await
        .expect("WebTransport request head");
    (h3_connection, connect_stream, quinn_send, quinn_recv)
}

fn start_inert_body_send_error_peer(
    tunnel_protocol: TunnelTransportProtocol,
) -> (BodySendPeer, tokio::sync::oneshot::Receiver<()>) {
    install_crypto_provider();
    let (headers_seen_tx, headers_seen_rx) = tokio::sync::oneshot::channel();
    let peer = start_body_send_peer(tunnel_protocol, BodySendPeerMode::Inert(headers_seen_tx));
    (peer, headers_seen_rx)
}

async fn body_send_error_is_returned_before_header_timeout(
    tunnel_protocol: TunnelTransportProtocol,
) {
    let (peer, headers_seen_rx) = start_inert_body_send_error_peer(tunnel_protocol);
    let proxy = test_quic_proxy(tunnel_protocol);
    let registration = connect_direct_registration(&proxy, &format!("quic://{}", peer.addr)).await;

    let mut headers = test_request_headers("req-body-error", "model-body-error", "7");
    headers.insert("content-type", HeaderValue::from_static("application/json"));
    let body_stream = futures::stream::once(async move {
        let _ = headers_seen_rx.await;
        Err::<bytes::Bytes, std::io::Error>(std::io::Error::other("synthetic body failure"))
    });

    let result = tokio::time::timeout(
        Duration::from_millis(500),
        post_chat(
            &proxy,
            &registration,
            headers,
            Body::from_stream(body_stream),
        ),
    )
    .await
    .expect("body send error should be returned before the header timeout");
    let error = match result {
        Ok(_) => panic!("body send should fail before response headers arrive"),
        Err(error) => error,
    };
    let error_chain = format!("{error:#}");
    assert!(
        error_chain.contains("failed to send") && error_chain.contains("synthetic body failure"),
        "unexpected error chain: {error_chain}"
    );

    peer.finish().await;
}

async fn body_send_error_is_returned_at_response_eof(tunnel_protocol: TunnelTransportProtocol) {
    let peer = start_early_success_body_send_error_peer(tunnel_protocol);
    let proxy = test_quic_proxy(tunnel_protocol);
    let registration = connect_direct_registration(&proxy, &format!("quic://{}", peer.addr)).await;

    let mut headers = test_request_headers("req-body-error-after-headers", "model-body-error", "7");
    headers.insert("content-type", HeaderValue::from_static("application/json"));
    let (release_body_tx, release_body_rx) = tokio::sync::oneshot::channel();
    let body_stream = futures::stream::once(future::ready(Ok::<_, std::io::Error>(
        bytes::Bytes::from_static(b"partial request body"),
    )))
    .chain(futures::stream::once(async move {
        let _ = release_body_rx.await;
        Err::<bytes::Bytes, std::io::Error>(std::io::Error::other("synthetic body failure"))
    }));

    let response = tokio::time::timeout(
        Duration::from_millis(500),
        post_chat(
            &proxy,
            &registration,
            headers,
            Body::from_stream(body_stream),
        ),
    )
    .await
    .expect("early success response should arrive before request body finishes")
    .expect("proxy request should return early success response");

    let _ = release_body_tx.send(());
    let mut stream = response.body_stream;
    let error = tokio::time::timeout(Duration::from_secs(1), stream.recv_body())
        .await
        .expect("response EOF should wait for request body send result")
        .expect_err("request body failure should surface at response EOF");
    let error_chain = format!("{error:#}");
    assert!(
        error_chain.contains("failed to send") && error_chain.contains("synthetic body failure"),
        "unexpected error chain: {error_chain}"
    );

    peer.finish().await;
}

async fn stalled_body_send_does_not_block_response_eof(tunnel_protocol: TunnelTransportProtocol) {
    let peer = start_early_success_body_send_error_peer(tunnel_protocol);
    let proxy = test_quic_proxy_with(tunnel_protocol, |config| {
        config.request_timeout = Duration::from_millis(50);
    });
    let registration = connect_direct_registration(&proxy, &format!("quic://{}", peer.addr)).await;

    let mut headers =
        test_request_headers("req-body-stalled-after-headers", "model-body-stalled", "7");
    headers.insert("content-type", HeaderValue::from_static("application/json"));
    let body_stream = futures::stream::once(future::ready(Ok::<_, std::io::Error>(
        bytes::Bytes::from_static(b"partial request body"),
    )))
    .chain(futures::stream::pending::<
        std::result::Result<bytes::Bytes, std::io::Error>,
    >());

    let response = tokio::time::timeout(
        Duration::from_millis(500),
        post_chat(
            &proxy,
            &registration,
            headers,
            Body::from_stream(body_stream),
        ),
    )
    .await
    .expect("early success response should arrive before stalled body finishes")
    .expect("proxy request should return early success response");

    let mut stream = response.body_stream;
    let eof = tokio::time::timeout(Duration::from_millis(500), stream.recv_body())
        .await
        .expect("response EOF should not wait forever for a stalled request body")
        .expect("response EOF should not fail for a merely stalled request body");
    assert!(eof.is_none());

    peer.finish().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn response_header_timeout_uses_remaining_budget_after_stream_setup() {
    let server = test_server_endpoint(TunnelTransportProtocol::RawQuic, |config| {
        let mut transport = quinn::TransportConfig::default();
        // Limit the server to one open request stream so the second request
        // spends part of its timeout budget waiting for stream capacity.
        transport.max_concurrent_bidi_streams(1_u8.into());
        config.transport_config(Arc::new(transport));
    });
    let server_addr = server.local_addr().unwrap();
    let server_task = tokio::spawn(async move {
        let incoming = server.accept().await.expect("server should accept");
        let connection = incoming.await.expect("server connection should complete");
        let (_first_send, mut first_recv) = connection.accept_bi().await.expect("first stream");
        let first_task = tokio::spawn(async move {
            let _ = first_recv.read_to_end(1024).await;
        });

        let (_second_send, second_recv) = connection.accept_bi().await.expect("second stream");
        first_task.await.unwrap();
        let mut recv_stream = RecvStream::new(second_recv);
        recv_stream.recv_header().await.expect("request headers");
        future::pending::<()>().await;
    });

    let proxy = test_quic_proxy_with(TunnelTransportProtocol::RawQuic, |config| {
        config.request_timeout = Duration::from_millis(250);
    });
    let registration = connect_direct_registration(&proxy, &format!("quic://{server_addr}")).await;

    let connection = raw_connection(&registration, 0);
    let first_stream = connection.open_bi().await.expect("open first stream");

    let headers = test_request_headers("req-header-budget", "model-budget", "7");
    let request = post_chat(&proxy, &registration, headers, Body::empty());
    tokio::pin!(request);
    assert!(
        tokio::time::timeout(Duration::from_millis(180), &mut request)
            .await
            .is_err(),
        "request should wait for stream capacity"
    );
    // Free the only server-side request stream after setup has consumed
    // most of the request timeout, so the response-header phase should
    // inherit only the remaining budget.
    drop(first_stream);

    let result = tokio::time::timeout(Duration::from_millis(120), &mut request)
        .await
        .expect("remaining request budget should expire before the outer bound");
    let error = match result {
        Ok(_) => panic!("response header wait should use only remaining budget"),
        Err(error) => error,
    };
    assert!(
        format!("{error:#}").contains("quic request timed out"),
        "unexpected error: {error:#}"
    );

    server_task.abort();
}

#[tokio::test]
async fn health_check_succeeds_through_reverse_tunnel() {
    let fixture =
        ReverseTunnelFixture::start(health_backend("ok"), TunnelTransportProtocol::RawQuic).await;
    let rtt = fixture
        .proxy
        .health_check_rtt(&fixture.generation)
        .await
        .unwrap();
    assert!(rtt.as_millis() < 1000);
    fixture.shutdown().await;
}

#[tokio::test]
async fn handshake_nack_for_unregistered_server() {
    let state = Arc::new(StargateState::new());
    let (_proxy, addr, runtime) =
        start_tunnel_server(state, TunnelTransportProtocol::RawQuic).await;

    let app = Router::new().route("/health", get(|| async { "ok" }));
    let backend_url = spawn_mock_backend(app).await;

    let mut config = reverse_tunnel_config(addr, "unregistered-backend", backend_url);
    config.quic_insecure = true;
    let result = start_reverse_quic_tunnel(config).await;

    assert!(result.is_err(), "expected handshake to be rejected");

    runtime.begin_shutdown();
}

#[tokio::test]
async fn duplicate_reverse_connection_rejected() {
    let fixture =
        ReverseTunnelFixture::start(health_backend("ok"), TunnelTransportProtocol::RawQuic).await;

    let backend_url2 = spawn_mock_backend(health_backend("ok")).await;

    let mut dup_config = reverse_tunnel_config(fixture.addr, INFERENCE_SERVER_ID, backend_url2);
    dup_config.quic_insecure = true;
    let result = start_reverse_quic_tunnel(dup_config).await;
    assert!(
        result.is_err(),
        "second connection with same id should be rejected while first is active"
    );

    fixture.shutdown().await;
}

#[tokio::test]
async fn reverse_connection_cannot_cross_registration_generation() {
    let app = Router::new().route("/health", get(|| async { "old" }));
    let backend_url = spawn_mock_backend(app).await;

    let state = Arc::new(StargateState::new());
    let old_registration = register_backend(&state, INFERENCE_SERVER_ID, true);
    let old_generation = old_registration.generation();
    let (proxy, addr, runtime) =
        start_tunnel_server(state.clone(), TunnelTransportProtocol::RawQuic).await;
    let old_tunnel = RegistrationTunnel::reverse(
        proxy.clone(),
        old_generation.clone(),
        Duration::from_millis(100),
    );
    let old_handle = connect_reverse_tunnel(addr, &backend_url).await;
    assert!(
        proxy
            .await_reverse_connection(old_generation.clone(), Duration::from_secs(2))
            .await
    );

    state.end_registration(old_registration).await;
    let replacement = register_backend(&state, INFERENCE_SERVER_ID, true);
    let replacement_generation = replacement.generation();
    let replacement_tunnel = RegistrationTunnel::reverse(
        proxy.clone(),
        replacement_generation.clone(),
        Duration::from_millis(100),
    );

    let replacement_app = Router::new().route("/health", get(|| async { "replacement" }));
    let replacement_backend_url = spawn_mock_backend(replacement_app).await;
    let replacement_handle = connect_reverse_tunnel(addr, &replacement_backend_url).await;
    assert!(
        proxy
            .await_reverse_connection(replacement_generation.clone(), Duration::from_secs(2))
            .await,
        "replacement registration should own its own reverse connection"
    );

    // Delayed old-generation release must not remove the replacement generation.
    drop(old_tunnel);
    assert!(
        proxy.has_healthy_connection(&replacement_generation),
        "delayed old-generation cleanup must not remove the replacement tunnel"
    );

    state.end_registration(replacement).await;
    // Mirror registration-session ordering: retire routing before releasing its tunnel.
    drop(replacement_tunnel);
    replacement_handle.shutdown().await;
    old_handle.shutdown().await;
    runtime.begin_shutdown();
}

#[tokio::test]
async fn await_reverse_connection_ignores_closed_generation_connection() {
    install_crypto_provider();
    let app = Router::new().route("/health", get(|| async { "ok" }));
    let backend_url = spawn_mock_backend(app).await;

    let proxy = test_quic_proxy(TunnelTransportProtocol::RawQuic);
    let tunnel = start_test_quic_tunnel(backend_url, TunnelTransportProtocol::RawQuic).await;

    let registration =
        connect_direct_registration(&proxy, &format!("quic://{}", tunnel.listen_addr())).await;
    assert!(
        proxy
            .await_reverse_connection(registration.clone(), Duration::from_secs(1))
            .await
    );

    tunnel.shutdown().await;
    tokio::time::timeout(Duration::from_secs(2), async {
        while proxy.has_healthy_connection(&registration) {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("registered connection should close");

    assert!(
        !proxy
            .await_reverse_connection(registration, Duration::from_millis(50))
            .await,
        "closed generation connection must not satisfy reverse connection wait"
    );
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn reverse_tunnel_works_with_secure_client_and_provided_cert() {
    let app = Router::new().route("/health", get(|| async { (StatusCode::OK, "ok") }));
    let backend_url = spawn_mock_backend(app).await;

    install_crypto_provider();
    let (cert_pem, key_pem) = stargate_tls::generate_self_signed_cert().unwrap();

    let state = Arc::new(StargateState::new());
    let registration = register_backend(&state, INFERENCE_SERVER_ID, true);
    let generation = registration.generation();

    let proxy = test_quic_proxy_with(Default::default(), |config| {
        config.tls_cert_pem = Some(cert_pem.clone());
        config.server_tls_identity = stargate_tls::ServerTlsIdentity::Provided {
            cert_pem: cert_pem.clone(),
            key_pem,
        };
        config.quic_insecure = false;
    });
    let (runtime, _failures) = CriticalTaskGroup::new("stargate test");
    let addr = proxy
        .start_reverse_listener(
            state,
            runtime.clone(),
            None,
            std::net::UdpSocket::bind("127.0.0.1:0").unwrap(),
        )
        .await
        .unwrap();
    let _tunnel = own_reverse_registration(proxy.clone(), &registration);

    let mut config = reverse_tunnel_config(addr, INFERENCE_SERVER_ID, backend_url);
    config.tls_cert_pem = Some(cert_pem);
    config.quic_insecure = false;
    let handle = start_reverse_quic_tunnel(config).await.unwrap();

    assert!(
        proxy
            .await_reverse_connection(generation.clone(), Duration::from_secs(2))
            .await
    );

    let rtt = proxy.health_check_rtt(&generation).await.unwrap();
    assert!(rtt.as_millis() < 1000);

    handle.shutdown().await;
    runtime.begin_shutdown();
}

#[tokio::test]
async fn reverse_tunnel_secure_client_rejects_unknown_server_cert() {
    install_crypto_provider();

    let state = Arc::new(StargateState::new());
    register_backend(&state, INFERENCE_SERVER_ID, true);
    let (_proxy, addr, runtime) =
        start_tunnel_server(state, TunnelTransportProtocol::RawQuic).await;

    let app = Router::new().route("/health", get(|| async { "ok" }));
    let backend_url = spawn_mock_backend(app).await;

    let (different_cert, _) = stargate_tls::generate_self_signed_cert().unwrap();
    let mut config = reverse_tunnel_config(addr, INFERENCE_SERVER_ID, backend_url);
    config.tls_cert_pem = Some(different_cert);
    config.quic_insecure = false;
    let result = start_reverse_quic_tunnel(config).await;

    assert!(
        result.is_err(),
        "secure client with different CA should reject server cert"
    );

    runtime.begin_shutdown();
}

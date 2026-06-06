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
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use anyhow::Result;
use axum::extract::Request;
use axum::http::{HeaderMap, HeaderName, HeaderValue, Method, StatusCode};
use axum::routing::{get, post};
use axum::{Router, body::Body};
use futures::{StreamExt, future};
use quinn::{ClientConfig, Connection, Endpoint};
use tokio::net::TcpListener;
use tokio_util::sync::CancellationToken;
use tokio_util::task::TaskTracker;

use stargate_protocol::{
    RecvStream, SendStream, TunnelTransportProtocol, WebTransportHttpResponseHead,
};

use super::body::RequestBodySendTask;
use super::direct::{TunnelConnection, build_client_config, build_server_config};
use super::http3::{H3ServerConnection, should_forward_h3_tunnel_request_header};
use super::reverse::ReverseConnectionEvents;
use super::watcher::ConnState;
use super::{ConnectionWatcher, EnsureConnectedResult, QuicHttpProxy, QuicTunnelConfig};
use crate::routing_state::{RegistrationIdentity, StargateState};
use pylon_lib::{
    QuicHttpTunnelConfig, ReverseQuicTunnelConfig, start_quic_http_tunnel,
    start_reverse_quic_tunnel,
};

const INFERENCE_SERVER_ID: &str = "test-backend";

struct DropNotifier(Option<tokio::sync::oneshot::Sender<()>>);

impl Drop for DropNotifier {
    fn drop(&mut self) {
        if let Some(tx) = self.0.take() {
            let _ = tx.send(());
        }
    }
}

async fn setup_mock_backend() -> (TcpListener, String) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    (listener, format!("http://{addr}"))
}

async fn register_backend(state: &StargateState, id: &str, reverse_tunnel: bool) {
    let identity = RegistrationIdentity {
        inference_server_id: id.to_string(),
        cluster_id: id.to_string(),
        inference_server_url: "http://127.0.0.1:1".to_string(),
        routing_key: None,
        reverse_tunnel,
        coordinated_calibration: false,
    };
    state.begin_registration(&identity).await.unwrap();
}

async fn start_tunnel_server(
    state: Arc<StargateState>,
) -> (Arc<QuicHttpProxy>, SocketAddr, CancellationToken) {
    start_tunnel_server_with_insecure(state, true, TunnelTransportProtocol::Custom).await
}

async fn start_tunnel_server_with_insecure(
    state: Arc<StargateState>,
    quic_insecure: bool,
    tunnel_protocol: TunnelTransportProtocol,
) -> (Arc<QuicHttpProxy>, SocketAddr, CancellationToken) {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let proxy = Arc::new(
        QuicHttpProxy::new(
            QuicTunnelConfig {
                connect_timeout: Duration::from_secs(5),
                request_timeout: Duration::from_secs(5),
                direct_quic_connections: 1,
                tls_cert_pem: None,
                server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
                quic_insecure,
                tunnel_protocol,
            },
            Arc::new(crate::auth::OpenAuthenticator),
        )
        .expect("test QUIC proxy should initialize"),
    );
    let shutdown = CancellationToken::new();
    let addr = proxy
        .start_reverse_listener(
            "127.0.0.1:0".parse().expect("valid test listen address"),
            state,
            shutdown.clone(),
            TaskTracker::new(),
            None,
            None,
        )
        .await
        .expect("reverse listener should start");
    (proxy, addr, shutdown)
}

#[tokio::test]
async fn reverse_listener_shutdown_waits_for_stalled_dispatch_task() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let state = Arc::new(StargateState::new());
    let proxy = Arc::new(
        QuicHttpProxy::new(
            QuicTunnelConfig {
                connect_timeout: Duration::from_secs(5),
                request_timeout: Duration::from_secs(5),
                tls_cert_pem: None,
                server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
                quic_insecure: true,
                tunnel_protocol: TunnelTransportProtocol::Custom,
                direct_quic_connections: 1,
            },
            Arc::new(crate::auth::OpenAuthenticator),
        )
        .expect("test QUIC proxy should initialize"),
    );
    let shutdown = CancellationToken::new();
    let task_tracker = TaskTracker::new();
    let addr = proxy
        .start_reverse_listener(
            "127.0.0.1:0".parse().expect("valid test listen address"),
            state,
            shutdown.clone(),
            task_tracker.clone(),
            None,
            None,
        )
        .await
        .expect("reverse listener should start");

    let mut client_endpoint = Endpoint::client(
        "127.0.0.1:0"
            .parse()
            .expect("valid test client bind address"),
    )
    .expect("client endpoint should start");
    client_endpoint.set_default_client_config(
        build_client_config(None, true, TunnelTransportProtocol::Custom).expect("client config"),
    );
    let connection = client_endpoint
        .connect(addr, "stargate")
        .expect("connect")
        .await
        .expect("reverse listener accepts connection");

    shutdown.cancel();
    task_tracker.close();
    tokio::time::timeout(Duration::from_secs(2), task_tracker.wait())
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
    let client_config =
        build_client_config(None, true, TunnelTransportProtocol::Custom).expect("client config");
    let server_config = build_server_config(
        &stargate_tls::ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::Custom,
    )
    .expect("server config");

    assert_eq!(negotiate_alpn(client_config, server_config).await, None);
}

#[tokio::test]
async fn http3_tunnel_tls_configs_negotiate_h3_alpn() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let client_config =
        build_client_config(None, true, TunnelTransportProtocol::Http3).expect("client config");
    let server_config = build_server_config(
        &stargate_tls::ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::Http3,
    )
    .expect("server config");

    assert_eq!(
        negotiate_alpn(client_config, server_config).await,
        Some(b"h3".to_vec())
    );
}

async fn connect_reverse_tunnel(
    listener_addr: SocketAddr,
    backend_url: &str,
) -> pylon_lib::ReverseQuicTunnelHandle {
    connect_reverse_tunnel_insecure(listener_addr, backend_url, INFERENCE_SERVER_ID).await
}

async fn connect_reverse_tunnel_insecure(
    listener_addr: SocketAddr,
    backend_url: &str,
    server_id: &str,
) -> pylon_lib::ReverseQuicTunnelHandle {
    connect_reverse_tunnel_insecure_with_protocol(
        listener_addr,
        backend_url,
        server_id,
        TunnelTransportProtocol::Custom,
    )
    .await
}

async fn connect_reverse_tunnel_insecure_with_protocol(
    listener_addr: SocketAddr,
    backend_url: &str,
    server_id: &str,
    tunnel_protocol: TunnelTransportProtocol,
) -> pylon_lib::ReverseQuicTunnelHandle {
    let mut config = ReverseQuicTunnelConfig::new(
        format!("127.0.0.1:{}", listener_addr.port()),
        server_id.to_string(),
        backend_url.to_string(),
    );
    config.quic_insecure = true;
    config.tunnel_protocol = tunnel_protocol;
    start_reverse_quic_tunnel(config).await.unwrap()
}

#[tokio::test]
async fn reverse_connection_events_wait_until_wakes_existing_waiters() {
    let events = ReverseConnectionEvents::new();
    let connected = Arc::new(AtomicBool::new(false));

    let wait = tokio::spawn({
        let events = events.clone();
        let connected = connected.clone();
        async move {
            events
                .wait_until(Duration::from_secs(1), move || {
                    let connected = connected.clone();
                    async move { connected.load(Ordering::SeqCst) }
                })
                .await
        }
    });
    tokio::task::yield_now().await;
    connected.store(true, Ordering::SeqCst);
    events.notify_changed();

    let woke = tokio::time::timeout(Duration::from_millis(50), wait)
        .await
        .expect("reverse connection event should wake promptly")
        .expect("wait task should complete");
    assert!(woke);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn reverse_tunnel_proxies_request_to_upstream() {
    let (listener, backend_url) = setup_mock_backend().await;
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
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let state = Arc::new(StargateState::new());
    register_backend(&state, INFERENCE_SERVER_ID, true).await;
    let (proxy, addr, shutdown) = start_tunnel_server(state).await;

    let handle = connect_reverse_tunnel(addr, &backend_url).await;
    assert!(
        proxy
            .await_reverse_connection(INFERENCE_SERVER_ID, Duration::from_secs(2))
            .await
    );

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-1".parse().unwrap());
    headers.insert("x-model", "test-model".parse().unwrap());
    headers.insert("x-input-tokens", "5".parse().unwrap());

    let response = proxy
        .proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::GET,
            "/v1/models",
            headers,
            Body::from("{}"),
        )
        .await
        .unwrap();

    assert_eq!(response.status, StatusCode::OK);
    let mut body = Vec::new();
    let mut stream = response.body_stream;
    while let Some(chunk) = stream.recv_body().await.unwrap() {
        body.extend_from_slice(&chunk);
    }
    let payload: serde_json::Value = serde_json::from_slice(&body).unwrap();
    assert_eq!(
        payload.get("model").and_then(serde_json::Value::as_str),
        Some("test-model")
    );

    handle.shutdown().await;
    shutdown.cancel();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn reverse_http3_tunnel_proxies_request_to_upstream() {
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route(
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
    );
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let state = Arc::new(StargateState::new());
    register_backend(&state, INFERENCE_SERVER_ID, true).await;
    let (proxy, addr, shutdown) =
        start_tunnel_server_with_insecure(state, true, TunnelTransportProtocol::Http3).await;

    let handle = tokio::time::timeout(
        Duration::from_secs(3),
        connect_reverse_tunnel_insecure_with_protocol(
            addr,
            &backend_url,
            INFERENCE_SERVER_ID,
            TunnelTransportProtocol::Http3,
        ),
    )
    .await
    .expect("http3 reverse tunnel handshake timed out");
    assert!(
        proxy
            .await_reverse_connection(INFERENCE_SERVER_ID, Duration::from_secs(2))
            .await
    );

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-h3-reverse".parse().unwrap());
    headers.insert("x-model", "model-h3".parse().unwrap());
    headers.insert("x-input-tokens", "7".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());

    let response = proxy
        .proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::POST,
            "/v1/models?source=reverse-http3",
            headers,
            Body::from(r#"{"ping":true}"#),
        )
        .await
        .unwrap();

    assert_eq!(response.status, StatusCode::OK);
    let mut body = Vec::new();
    let mut stream = response.body_stream;
    while let Some(chunk) = stream.recv_body().await.unwrap() {
        body.extend_from_slice(&chunk);
    }
    let payload: serde_json::Value = serde_json::from_slice(&body).unwrap();
    assert_eq!(
        payload.get("model").and_then(serde_json::Value::as_str),
        Some("model-h3")
    );
    assert_eq!(
        payload.get("body_len").and_then(serde_json::Value::as_u64),
        Some(13)
    );

    handle.shutdown().await;
    shutdown.cancel();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn reverse_webtransport_tunnel_proxies_request_to_upstream() {
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route(
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
    );
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let state = Arc::new(StargateState::new());
    register_backend(&state, INFERENCE_SERVER_ID, true).await;
    let (proxy, addr, shutdown) =
        start_tunnel_server_with_insecure(state, true, TunnelTransportProtocol::WebTransport).await;

    let handle = tokio::time::timeout(
        Duration::from_secs(3),
        connect_reverse_tunnel_insecure_with_protocol(
            addr,
            &backend_url,
            INFERENCE_SERVER_ID,
            TunnelTransportProtocol::WebTransport,
        ),
    )
    .await
    .expect("webtransport reverse tunnel handshake timed out");
    assert!(
        proxy
            .await_reverse_connection(INFERENCE_SERVER_ID, Duration::from_secs(2))
            .await
    );

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-wt-reverse".parse().unwrap());
    headers.insert("x-model", "model-wt".parse().unwrap());
    headers.insert("x-input-tokens", "7".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());

    let response = proxy
        .proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::POST,
            "/v1/models?source=reverse-webtransport",
            headers,
            Body::from(r#"{"ping":true}"#),
        )
        .await
        .unwrap();

    assert_eq!(response.status, StatusCode::OK);
    let mut body = Vec::new();
    let mut stream = response.body_stream;
    while let Some(chunk) = stream.recv_body().await.unwrap() {
        body.extend_from_slice(&chunk);
    }
    let payload: serde_json::Value = serde_json::from_slice(&body).unwrap();
    assert_eq!(
        payload.get("model").and_then(serde_json::Value::as_str),
        Some("model-wt")
    );
    assert_eq!(
        payload.get("body_len").and_then(serde_json::Value::as_u64),
        Some(13)
    );

    handle.shutdown().await;
    shutdown.cancel();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn reverse_webtransport_stalled_stream_header_does_not_block_later_requests() {
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route(
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
    );
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let state = Arc::new(StargateState::new());
    register_backend(&state, INFERENCE_SERVER_ID, true).await;
    let (proxy, addr, shutdown) =
        start_tunnel_server_with_insecure(state, true, TunnelTransportProtocol::WebTransport).await;

    let handle = tokio::time::timeout(
        Duration::from_secs(3),
        connect_reverse_tunnel_insecure_with_protocol(
            addr,
            &backend_url,
            INFERENCE_SERVER_ID,
            TunnelTransportProtocol::WebTransport,
        ),
    )
    .await
    .expect("webtransport reverse tunnel handshake timed out");
    assert!(
        proxy
            .await_reverse_connection(INFERENCE_SERVER_ID, Duration::from_secs(2))
            .await
    );

    {
        let webtransport = {
            let pool = proxy.pool.read().await;
            match pool
                .get(INFERENCE_SERVER_ID)
                .expect("pooled connection")
                .choose_healthy()
                .expect("healthy connection")
            {
                TunnelConnection::WebTransport(handle) => handle.clone(),
                TunnelConnection::Custom(_) | TunnelConnection::Http3(_) => {
                    panic!("expected WebTransport tunnel connection")
                }
            }
        };
        let (_stalled_send, _stalled_recv) = webtransport.connection.open_bi().await.unwrap();

        let mut headers = HeaderMap::new();
        headers.insert("x-request-id", "req-wt-after-stalled".parse().unwrap());
        headers.insert("x-model", "model-wt".parse().unwrap());
        headers.insert("x-input-tokens", "7".parse().unwrap());
        headers.insert("content-type", "application/json".parse().unwrap());

        let response = tokio::time::timeout(
            Duration::from_secs(3),
            proxy.proxy_request_streaming(
                INFERENCE_SERVER_ID,
                Method::POST,
                "/v1/models?source=reverse-webtransport-stalled",
                headers,
                Body::from(r#"{"ping":true}"#),
            ),
        )
        .await
        .expect("request after stalled WebTransport stream timed out")
        .unwrap();

        assert_eq!(response.status, StatusCode::OK);
        let mut body = Vec::new();
        let mut stream = response.body_stream;
        while let Some(chunk) = stream.recv_body().await.unwrap() {
            body.extend_from_slice(&chunk);
        }
        let payload: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(
            payload.get("model").and_then(serde_json::Value::as_str),
            Some("model-wt")
        );
        assert_eq!(
            payload.get("body_len").and_then(serde_json::Value::as_u64),
            Some(13)
        );
    }

    handle.shutdown().await;
    shutdown.cancel();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_preconnect_installs_configured_connection_set() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        backend_url,
    ))
    .await
    .unwrap();
    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_secs(2),
            direct_quic_connections: 3,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::Custom,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .unwrap();

    proxy
        .preconnect(
            INFERENCE_SERVER_ID,
            &format!("quic://{}", tunnel.listen_addr()),
        )
        .await
        .unwrap();

    let connection_count = {
        let pool = proxy.pool.read().await;
        let connection_set = pool.get(INFERENCE_SERVER_ID).expect("pooled connection");
        assert!(connection_set.is_healthy());
        connection_set.len()
    };
    assert_eq!(connection_count, 3);

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-direct-set".parse().unwrap());
    headers.insert("x-model", "model-direct-set".parse().unwrap());
    headers.insert("x-input-tokens", "7".parse().unwrap());
    let response = proxy
        .proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::GET,
            "/health",
            headers,
            Body::empty(),
        )
        .await
        .unwrap();

    assert_eq!(response.status, StatusCode::OK);

    let first_connection = {
        let pool = proxy.pool.read().await;
        let connection_set = pool.get(INFERENCE_SERVER_ID).expect("pooled connection");
        match &connection_set.inner.connections[0] {
            TunnelConnection::Custom(connection) => connection.clone(),
            TunnelConnection::Http3(_) | TunnelConnection::WebTransport(_) => {
                panic!("expected custom tunnel connection")
            }
        }
    };
    first_connection.close(0u32.into(), b"test partial direct set close");
    assert!(
        proxy.has_healthy_connection(INFERENCE_SERVER_ID).await,
        "partial direct connection set should remain usable"
    );
    assert!(
        proxy
            .connection_set_needs_replenishment(INFERENCE_SERVER_ID)
            .await,
        "partial direct connection set should request replenishment"
    );

    proxy
        .reconnect_direct(
            INFERENCE_SERVER_ID,
            &format!("quic://{}", tunnel.listen_addr()),
        )
        .await
        .unwrap();
    assert!(proxy.has_healthy_connection(INFERENCE_SERVER_ID).await);
    assert!(
        !proxy
            .connection_set_needs_replenishment(INFERENCE_SERVER_ID)
            .await
    );
    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn connection_watcher_replenishes_partial_direct_connection_set() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        backend_url,
    ))
    .await
    .unwrap();
    let proxy = Arc::new(
        QuicHttpProxy::new(
            QuicTunnelConfig {
                connect_timeout: Duration::from_secs(5),
                request_timeout: Duration::from_secs(2),
                direct_quic_connections: 2,
                tls_cert_pem: None,
                server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
                quic_insecure: true,
                tunnel_protocol: TunnelTransportProtocol::Custom,
            },
            Arc::new(crate::auth::OpenAuthenticator),
        )
        .unwrap(),
    );

    let target_url = format!("quic://{}", tunnel.listen_addr());
    let mut watcher = ConnectionWatcher::new(proxy.clone(), Duration::from_millis(100));
    let result = watcher
        .ensure_connected(INFERENCE_SERVER_ID, &target_url, false)
        .await;
    assert!(matches!(result, EnsureConnectedResult::Connected));

    let first_connection = {
        let pool = proxy.pool.read().await;
        let connection_set = pool.get(INFERENCE_SERVER_ID).expect("pooled connection");
        assert_eq!(connection_set.len(), 2);
        match &connection_set.inner.connections[0] {
            TunnelConnection::Custom(connection) => connection.clone(),
            TunnelConnection::Http3(_) | TunnelConnection::WebTransport(_) => {
                panic!("expected custom tunnel connection")
            }
        }
    };
    first_connection.close(0u32.into(), b"test watcher direct replenish");
    assert!(
        proxy.has_healthy_connection(INFERENCE_SERVER_ID).await,
        "partial direct connection set should stay usable before replenishment"
    );
    assert!(
        proxy
            .connection_set_needs_replenishment(INFERENCE_SERVER_ID)
            .await,
        "partial direct connection set should request replenishment"
    );

    let result = watcher
        .ensure_connected(INFERENCE_SERVER_ID, &target_url, false)
        .await;
    assert!(matches!(result, EnsureConnectedResult::Connected));

    let pool = proxy.pool.read().await;
    let connection_set = pool.get(INFERENCE_SERVER_ID).expect("pooled connection");
    assert!(connection_set.is_healthy());
    assert_eq!(connection_set.len(), 2);
    // Release the pool read guard before awaiting tunnel shutdown.
    drop(pool);

    tunnel.shutdown().await;
}

#[test]
fn h3_tunnel_request_filter_strips_hop_headers_case_insensitively()
-> std::result::Result<(), axum::http::header::InvalidHeaderName> {
    assert!(!should_forward_h3_tunnel_request_header(
        &HeaderName::from_bytes(b"Connection")?
    ));
    assert!(!should_forward_h3_tunnel_request_header(
        &HeaderName::from_bytes(b"Proxy-Connection")?
    ));
    assert!(!should_forward_h3_tunnel_request_header(
        &HeaderName::from_bytes(b"Host")?
    ));
    assert!(should_forward_h3_tunnel_request_header(
        &HeaderName::from_bytes(b"X-Request-Id")?
    ));
    Ok(())
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_http3_tunnel_proxies_request_to_upstream() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route(
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
    );
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let mut tunnel_config = QuicHttpTunnelConfig::new("127.0.0.1:0".parse().unwrap(), backend_url);
    tunnel_config.tunnel_protocol = TunnelTransportProtocol::Http3;
    let tunnel = start_quic_http_tunnel(tunnel_config).await.unwrap();
    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_secs(2),
            direct_quic_connections: 1,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::Http3,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .unwrap();
    proxy
        .preconnect(
            INFERENCE_SERVER_ID,
            &format!("quic://{}", tunnel.listen_addr()),
        )
        .await
        .unwrap();

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-h3-direct".parse().unwrap());
    headers.insert("x-model", "model-h3".parse().unwrap());
    headers.insert("x-input-tokens", "7".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    let response = tokio::time::timeout(
        Duration::from_secs(3),
        proxy.proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::POST,
            "/v1/models?source=http3",
            headers,
            Body::from(r#"{"ping":true}"#),
        ),
    )
    .await
    .expect("http3 proxy request timed out")
    .unwrap();

    assert_eq!(response.status, StatusCode::OK);
    let mut body = Vec::new();
    let mut stream = response.body_stream;
    while let Some(chunk) = stream.recv_body().await.unwrap() {
        body.extend_from_slice(&chunk);
    }
    let payload: serde_json::Value = serde_json::from_slice(&body).unwrap();
    assert_eq!(
        payload.get("model").and_then(serde_json::Value::as_str),
        Some("model-h3")
    );
    assert_eq!(
        payload.get("body_len").and_then(serde_json::Value::as_u64),
        Some(13)
    );

    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_webtransport_tunnel_proxies_request_to_upstream() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route(
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
    );
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let mut tunnel_config = QuicHttpTunnelConfig::new("127.0.0.1:0".parse().unwrap(), backend_url);
    tunnel_config.tunnel_protocol = TunnelTransportProtocol::WebTransport;
    let tunnel = start_quic_http_tunnel(tunnel_config).await.unwrap();
    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_secs(2),
            direct_quic_connections: 1,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::WebTransport,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .unwrap();
    proxy
        .preconnect(
            INFERENCE_SERVER_ID,
            &format!("quic://{}", tunnel.listen_addr()),
        )
        .await
        .unwrap();

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-wt-direct".parse().unwrap());
    headers.insert("x-model", "model-wt".parse().unwrap());
    headers.insert("x-input-tokens", "7".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    let response = tokio::time::timeout(
        Duration::from_secs(3),
        proxy.proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::POST,
            "/v1/models?source=webtransport",
            headers,
            Body::from(r#"{"ping":true}"#),
        ),
    )
    .await
    .expect("webtransport proxy request timed out")
    .unwrap();

    assert_eq!(response.status, StatusCode::OK);
    let mut body = Vec::new();
    let mut stream = response.body_stream;
    while let Some(chunk) = stream.recv_body().await.unwrap() {
        body.extend_from_slice(&chunk);
    }
    let payload: serde_json::Value = serde_json::from_slice(&body).unwrap();
    assert_eq!(
        payload.get("model").and_then(serde_json::Value::as_str),
        Some("model-wt")
    );
    assert_eq!(
        payload.get("body_len").and_then(serde_json::Value::as_u64),
        Some(13)
    );

    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_webtransport_connect_response_uses_connect_timeout() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let server_config = build_server_config(
        &stargate_tls::ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::WebTransport,
    )
    .unwrap();
    let server = Endpoint::server(server_config, "127.0.0.1:0".parse().unwrap()).unwrap();
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

    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_millis(50),
            request_timeout: Duration::from_secs(2),
            direct_quic_connections: 1,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::WebTransport,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .unwrap();
    let result = tokio::time::timeout(
        Duration::from_secs(1),
        proxy.preconnect(INFERENCE_SERVER_ID, &format!("quic://{server_addr}")),
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

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_http3_response_body_survives_pool_eviction() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (listener, backend_url) = setup_mock_backend().await;
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
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let mut tunnel_config = QuicHttpTunnelConfig::new("127.0.0.1:0".parse().unwrap(), backend_url);
    tunnel_config.tunnel_protocol = TunnelTransportProtocol::Http3;
    let tunnel = start_quic_http_tunnel(tunnel_config).await.unwrap();
    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_secs(2),
            direct_quic_connections: 1,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::Http3,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .unwrap();
    proxy
        .preconnect(
            INFERENCE_SERVER_ID,
            &format!("quic://{}", tunnel.listen_addr()),
        )
        .await
        .unwrap();

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-h3-evict-body".parse().unwrap());
    headers.insert("x-model", "model-h3".parse().unwrap());
    headers.insert("x-input-tokens", "7".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    let response = proxy
        .proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::POST,
            "/v1/stream",
            headers,
            Body::from(r#"{"stream":true}"#),
        )
        .await
        .unwrap();
    assert_eq!(response.status, StatusCode::OK);

    proxy.pool.write().await.remove(INFERENCE_SERVER_ID);
    release_body.notify_waiters();

    let mut body = Vec::new();
    let mut stream = response.body_stream;
    while let Some(chunk) = stream.recv_body().await.unwrap() {
        body.extend_from_slice(&chunk);
    }
    assert_eq!(body, b"first-second");

    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_webtransport_response_body_survives_pool_eviction() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (listener, backend_url) = setup_mock_backend().await;
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
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let mut tunnel_config = QuicHttpTunnelConfig::new("127.0.0.1:0".parse().unwrap(), backend_url);
    tunnel_config.tunnel_protocol = TunnelTransportProtocol::WebTransport;
    let tunnel = start_quic_http_tunnel(tunnel_config).await.unwrap();
    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_secs(2),
            direct_quic_connections: 1,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::WebTransport,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .unwrap();
    proxy
        .preconnect(
            INFERENCE_SERVER_ID,
            &format!("quic://{}", tunnel.listen_addr()),
        )
        .await
        .unwrap();

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-wt-evict-body".parse().unwrap());
    headers.insert("x-model", "model-wt".parse().unwrap());
    headers.insert("x-input-tokens", "7".parse().unwrap());
    headers.insert("content-type", "application/json".parse().unwrap());
    let response = proxy
        .proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::POST,
            "/v1/stream",
            headers,
            Body::from(r#"{"stream":true}"#),
        )
        .await
        .unwrap();
    assert_eq!(response.status, StatusCode::OK);

    proxy.pool.write().await.remove(INFERENCE_SERVER_ID);
    release_body.notify_waiters();

    let mut body = Vec::new();
    let mut stream = response.body_stream;
    while let Some(chunk) = stream.recv_body().await.unwrap() {
        body.extend_from_slice(&chunk);
    }
    assert_eq!(body, b"first-second");

    tunnel.shutdown().await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn direct_http3_tunnel_returns_header_errors_before_request_body_finishes() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();

    let mut tunnel_config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        "http://127.0.0.1:1".to_string(),
    );
    tunnel_config.tunnel_protocol = TunnelTransportProtocol::Http3;
    let tunnel = start_quic_http_tunnel(tunnel_config).await.unwrap();
    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_secs(2),
            direct_quic_connections: 1,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::Http3,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .unwrap();
    proxy
        .preconnect(
            INFERENCE_SERVER_ID,
            &format!("quic://{}", tunnel.listen_addr()),
        )
        .await
        .unwrap();

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
        proxy.proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::POST,
            "/v1/chat/completions",
            headers,
            Body::from_stream(body_stream),
        ),
    )
    .await
    .expect("http3 proxy should return response headers before request body finishes")
    .unwrap();
    let _ = release_body_tx.send(());

    assert_eq!(response.status, StatusCode::BAD_REQUEST);
    let mut body = Vec::new();
    let mut stream = response.body_stream;
    while let Some(chunk) = stream.recv_body().await.unwrap() {
        body.extend_from_slice(&chunk);
    }
    let payload: serde_json::Value = serde_json::from_slice(&body).unwrap();
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

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn custom_tunnel_returns_body_send_error_before_header_timeout() {
    body_send_error_is_returned_before_header_timeout(TunnelTransportProtocol::Custom).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn http3_tunnel_does_not_wait_for_header_timeout_after_body_send_error() {
    body_send_error_is_returned_before_header_timeout(TunnelTransportProtocol::Http3).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn webtransport_tunnel_returns_body_send_error_before_header_timeout() {
    body_send_error_is_returned_before_header_timeout(TunnelTransportProtocol::WebTransport).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn custom_tunnel_reports_body_send_error_at_response_eof() {
    body_send_error_is_returned_at_response_eof(TunnelTransportProtocol::Custom).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn http3_tunnel_reports_body_send_error_at_response_eof() {
    body_send_error_is_returned_at_response_eof(TunnelTransportProtocol::Http3).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn webtransport_tunnel_reports_body_send_error_at_response_eof() {
    body_send_error_is_returned_at_response_eof(TunnelTransportProtocol::WebTransport).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn custom_tunnel_does_not_wait_forever_for_stalled_request_body_at_response_eof() {
    stalled_body_send_does_not_block_response_eof(TunnelTransportProtocol::Custom).await;
}

struct EarlySuccessBodySendErrorPeer {
    addr: SocketAddr,
    release_tx: tokio::sync::oneshot::Sender<()>,
    task: tokio::task::JoinHandle<()>,
}

fn start_early_success_body_send_error_peer(
    tunnel_protocol: TunnelTransportProtocol,
) -> EarlySuccessBodySendErrorPeer {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let server_config = build_server_config(
        &stargate_tls::ServerTlsIdentity::SelfSigned,
        tunnel_protocol,
    )
    .expect("test server config should build");
    let server = Endpoint::server(
        server_config,
        "127.0.0.1:0"
            .parse()
            .expect("valid test server bind address"),
    )
    .expect("test server endpoint should start");
    let addr = server
        .local_addr()
        .expect("test server endpoint should expose local address");
    let (release_tx, release_rx) = tokio::sync::oneshot::channel();
    let task = tokio::spawn(async move {
        let incoming = server.accept().await.expect("server should accept");
        let connection = incoming.await.expect("server connection should complete");
        match tunnel_protocol {
            TunnelTransportProtocol::Custom => {
                accept_early_success_custom_request(connection, release_rx).await
            }
            TunnelTransportProtocol::Http3 => {
                accept_early_success_h3_request(connection, release_rx).await
            }
            TunnelTransportProtocol::WebTransport => {
                accept_early_success_webtransport_request(connection, release_rx).await
            }
        }
    });
    EarlySuccessBodySendErrorPeer {
        addr,
        release_tx,
        task,
    }
}

async fn accept_early_success_custom_request(
    connection: Connection,
    release_rx: tokio::sync::oneshot::Receiver<()>,
) {
    let (server_send, quinn_recv) = connection.accept_bi().await.expect("request stream");
    let mut recv_stream = RecvStream::new(quinn_recv);
    recv_stream.recv_header().await.expect("request headers");
    let mut send_stream = SendStream::new(server_send);
    let mut response_headers = HeaderMap::new();
    response_headers.insert("x-status", HeaderValue::from_static("200"));
    send_stream
        .send_header(response_headers)
        .await
        .expect("send response headers");
    send_stream.finish().expect("finish response");
    let _ = release_rx.await;
}

async fn accept_early_success_h3_request(
    connection: Connection,
    release_rx: tokio::sync::oneshot::Receiver<()>,
) {
    let mut h3_connection: H3ServerConnection = h3::server::builder()
        .build(h3_quinn::Connection::new(connection))
        .await
        .expect("h3 server connection");
    let resolver = h3_connection
        .accept()
        .await
        .expect("accept h3 request")
        .expect("h3 request");
    let (_request, mut stream) = resolver
        .resolve_request()
        .await
        .expect("resolve h3 request");
    let response = http::Response::builder()
        .status(StatusCode::OK)
        .body(())
        .expect("build h3 response");
    stream
        .send_response(response)
        .await
        .expect("send h3 response headers");
    stream.finish().await.expect("finish h3 response");
    let _ = (&h3_connection, &stream);
    let _ = release_rx.await;
}

async fn accept_early_success_webtransport_request(
    connection: Connection,
    release_rx: tokio::sync::oneshot::Receiver<()>,
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
    let response = http::Response::builder()
        .status(StatusCode::OK)
        .body(())
        .expect("build WebTransport CONNECT response");
    connect_stream
        .send_response(response)
        .await
        .expect("send WebTransport CONNECT response");

    let (mut quinn_send, mut quinn_recv) = connection
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
    let response_head = WebTransportHttpResponseHead {
        status: StatusCode::OK,
        headers: HeaderMap::new(),
    };
    stargate_protocol::write_webtransport_http_response_head(&mut quinn_send, &response_head)
        .await
        .expect("send WebTransport response head");
    stargate_protocol::finish_webtransport_http_stream(&mut quinn_send)
        .expect("finish WebTransport response");
    let _ = (&h3_connection, &connect_stream, &quinn_send);
    let _ = release_rx.await;
}

struct InertBodySendErrorPeer {
    addr: SocketAddr,
    headers_seen_rx: tokio::sync::oneshot::Receiver<()>,
    release_tx: tokio::sync::oneshot::Sender<()>,
    task: tokio::task::JoinHandle<()>,
}

fn start_inert_body_send_error_peer(
    tunnel_protocol: TunnelTransportProtocol,
) -> InertBodySendErrorPeer {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let server_config = build_server_config(
        &stargate_tls::ServerTlsIdentity::SelfSigned,
        tunnel_protocol,
    )
    .expect("test server config should build");
    let server = Endpoint::server(
        server_config,
        "127.0.0.1:0"
            .parse()
            .expect("valid test server bind address"),
    )
    .expect("test server endpoint should start");
    let addr = server
        .local_addr()
        .expect("test server endpoint should expose local address");
    let (headers_seen_tx, headers_seen_rx) = tokio::sync::oneshot::channel();
    let (release_tx, release_rx) = tokio::sync::oneshot::channel();
    let task = tokio::spawn(async move {
        let incoming = server.accept().await.expect("server should accept");
        let connection = incoming.await.expect("server connection should complete");
        match tunnel_protocol {
            TunnelTransportProtocol::Custom => {
                accept_inert_custom_request(connection, headers_seen_tx, release_rx).await
            }
            TunnelTransportProtocol::Http3 => {
                accept_inert_h3_request(connection, headers_seen_tx, release_rx).await
            }
            TunnelTransportProtocol::WebTransport => {
                accept_inert_webtransport_request(connection, headers_seen_tx, release_rx).await
            }
        }
    });
    InertBodySendErrorPeer {
        addr,
        headers_seen_rx,
        release_tx,
        task,
    }
}

async fn accept_inert_custom_request(
    connection: Connection,
    headers_seen_tx: tokio::sync::oneshot::Sender<()>,
    release_rx: tokio::sync::oneshot::Receiver<()>,
) {
    let (server_send, quinn_recv) = connection.accept_bi().await.expect("request stream");
    let keep_response_send_open = server_send;
    let mut recv_stream = RecvStream::new(quinn_recv);
    recv_stream.recv_header().await.expect("request headers");
    let _ = headers_seen_tx.send(());
    let _ = release_rx.await;
    // Keep the server-side response stream in the task state until the
    // synthetic request-body failure has been observed.
    let _ = &keep_response_send_open;
}

async fn accept_inert_h3_request(
    connection: Connection,
    headers_seen_tx: tokio::sync::oneshot::Sender<()>,
    release_rx: tokio::sync::oneshot::Receiver<()>,
) {
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
    let keep_request_stream_open = stream;
    let _ = headers_seen_tx.send(());
    let _ = release_rx.await;
    // Keep H3 state alive so the peer does not synthesize a response or
    // reset before the local body producer error wins.
    let _ = (&h3_connection, &keep_request_stream_open);
}

async fn accept_inert_webtransport_request(
    connection: Connection,
    headers_seen_tx: tokio::sync::oneshot::Sender<()>,
    release_rx: tokio::sync::oneshot::Receiver<()>,
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
    let response = http::Response::builder()
        .status(StatusCode::OK)
        .body(())
        .expect("build WebTransport CONNECT response");
    connect_stream
        .send_response(response)
        .await
        .expect("send WebTransport CONNECT response");

    let (quinn_send, mut quinn_recv) = connection
        .accept_bi()
        .await
        .expect("WebTransport request stream");
    let keep_response_send_open = quinn_send;
    let stream_session_id = stargate_protocol::read_webtransport_bidi_header(&mut quinn_recv)
        .await
        .expect("WebTransport bidi header");
    assert_eq!(stream_session_id, session_id);
    stargate_protocol::read_webtransport_http_request_head(&mut quinn_recv)
        .await
        .expect("WebTransport request head");
    let _ = headers_seen_tx.send(());
    let _ = release_rx.await;
    // Keep the CONNECT session and server-side response stream open until
    // the synthetic request-body failure has been observed.
    let _ = (&h3_connection, &connect_stream, &keep_response_send_open);
}

async fn body_send_error_is_returned_before_header_timeout(
    tunnel_protocol: TunnelTransportProtocol,
) {
    let peer = start_inert_body_send_error_peer(tunnel_protocol);
    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_secs(5),
            direct_quic_connections: 1,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .expect("test QUIC proxy should initialize");
    proxy
        .preconnect(INFERENCE_SERVER_ID, &format!("quic://{}", peer.addr))
        .await
        .expect("proxy should preconnect to inert test peer");

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", HeaderValue::from_static("req-body-error"));
    headers.insert("x-model", HeaderValue::from_static("model-body-error"));
    headers.insert("x-input-tokens", HeaderValue::from_static("7"));
    headers.insert("content-type", HeaderValue::from_static("application/json"));
    let body_stream = futures::stream::once(async move {
        let _ = peer.headers_seen_rx.await;
        Err::<bytes::Bytes, std::io::Error>(std::io::Error::other("synthetic body failure"))
    });

    let result = tokio::time::timeout(
        Duration::from_millis(500),
        proxy.proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::POST,
            "/v1/chat/completions",
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

    let _ = peer.release_tx.send(());
    peer.task
        .await
        .expect("inert body-send-error peer task should finish");
}

async fn body_send_error_is_returned_at_response_eof(tunnel_protocol: TunnelTransportProtocol) {
    let peer = start_early_success_body_send_error_peer(tunnel_protocol);
    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_secs(5),
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol,
            direct_quic_connections: 1,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .expect("test QUIC proxy should initialize");
    proxy
        .preconnect(INFERENCE_SERVER_ID, &format!("quic://{}", peer.addr))
        .await
        .expect("proxy should preconnect to early-success test peer");

    let mut headers = HeaderMap::new();
    headers.insert(
        "x-request-id",
        HeaderValue::from_static("req-body-error-after-headers"),
    );
    headers.insert("x-model", HeaderValue::from_static("model-body-error"));
    headers.insert("x-input-tokens", HeaderValue::from_static("7"));
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
        proxy.proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::POST,
            "/v1/chat/completions",
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

    let _ = peer.release_tx.send(());
    peer.task
        .await
        .expect("early-success body-send-error peer task should finish");
}

async fn stalled_body_send_does_not_block_response_eof(tunnel_protocol: TunnelTransportProtocol) {
    let peer = start_early_success_body_send_error_peer(tunnel_protocol);
    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_millis(50),
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol,
            direct_quic_connections: 1,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .expect("test QUIC proxy should initialize");
    proxy
        .preconnect(INFERENCE_SERVER_ID, &format!("quic://{}", peer.addr))
        .await
        .expect("proxy should preconnect to early-success test peer");

    let mut headers = HeaderMap::new();
    headers.insert(
        "x-request-id",
        HeaderValue::from_static("req-body-stalled-after-headers"),
    );
    headers.insert("x-model", HeaderValue::from_static("model-body-stalled"));
    headers.insert("x-input-tokens", HeaderValue::from_static("7"));
    headers.insert("content-type", HeaderValue::from_static("application/json"));
    let body_stream = futures::stream::once(future::ready(Ok::<_, std::io::Error>(
        bytes::Bytes::from_static(b"partial request body"),
    )))
    .chain(futures::stream::pending::<
        std::result::Result<bytes::Bytes, std::io::Error>,
    >());

    let response = tokio::time::timeout(
        Duration::from_millis(500),
        proxy.proxy_request_streaming(
            INFERENCE_SERVER_ID,
            Method::POST,
            "/v1/chat/completions",
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

    let _ = peer.release_tx.send(());
    peer.task
        .await
        .expect("early-success stalled-body peer task should finish");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn response_header_timeout_uses_remaining_budget_after_stream_setup() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let mut server_config = build_server_config(
        &stargate_tls::ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::Custom,
    )
    .unwrap();
    let mut transport = quinn::TransportConfig::default();
    // Limit the server to one open request stream so the second request
    // spends part of its timeout budget waiting for stream capacity.
    transport.max_concurrent_bidi_streams(1_u8.into());
    server_config.transport_config(Arc::new(transport));
    let server = Endpoint::server(server_config, "127.0.0.1:0".parse().unwrap()).unwrap();
    let server_addr = server.local_addr().unwrap();
    let server_task = tokio::spawn(async move {
        let incoming = server.accept().await.expect("server should accept");
        let connection = incoming.await.expect("server connection should complete");
        let (_first_send, mut first_recv) = connection.accept_bi().await.expect("first stream");
        let first_task = tokio::spawn(async move {
            let _ = first_recv.read_to_end(1024).await;
        });

        let (second_send, second_recv) = connection.accept_bi().await.expect("second stream");
        first_task.await.unwrap();
        let mut recv_stream = RecvStream::new(second_recv);
        let mut send_stream = SendStream::new(second_send);
        recv_stream.recv_header().await.expect("request headers");
        tokio::time::sleep(Duration::from_millis(120)).await;
        let mut response_headers = HeaderMap::new();
        response_headers.insert("x-status", HeaderValue::from_static("200"));
        send_stream
            .send_header(response_headers)
            .await
            .expect("send response headers");
        send_stream.finish().expect("finish response");
    });

    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_millis(250),
            direct_quic_connections: 1,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::Custom,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .unwrap();
    proxy
        .preconnect(INFERENCE_SERVER_ID, &format!("quic://{server_addr}"))
        .await
        .unwrap();

    let connection = {
        let pool = proxy.pool.read().await;
        match pool
            .get(INFERENCE_SERVER_ID)
            .expect("pooled connection")
            .choose_healthy()
            .expect("healthy connection")
        {
            TunnelConnection::Custom(connection) => connection.clone(),
            TunnelConnection::Http3(_) | TunnelConnection::WebTransport(_) => {
                panic!("expected custom tunnel connection")
            }
        }
    };
    let first_stream = connection.open_bi().await.expect("open first stream");

    let mut headers = HeaderMap::new();
    headers.insert("x-request-id", "req-header-budget".parse().unwrap());
    headers.insert("x-model", "model-budget".parse().unwrap());
    headers.insert("x-input-tokens", "7".parse().unwrap());
    let request = proxy.proxy_request_streaming(
        INFERENCE_SERVER_ID,
        Method::POST,
        "/v1/chat/completions",
        headers,
        Body::empty(),
    );
    tokio::pin!(request);
    tokio::time::sleep(Duration::from_millis(180)).await;
    // Free the only server-side request stream after setup has consumed
    // most of the request timeout, so the response-header phase should
    // inherit only the remaining budget.
    drop(first_stream);

    let result = tokio::time::timeout(Duration::from_secs(1), &mut request)
        .await
        .expect("request should complete before outer test timeout");
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
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route("/health", get(|| async { (StatusCode::OK, "ok") }));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let state = Arc::new(StargateState::new());
    register_backend(&state, INFERENCE_SERVER_ID, true).await;
    let (proxy, addr, shutdown) = start_tunnel_server(state).await;

    let handle = connect_reverse_tunnel(addr, &backend_url).await;
    assert!(
        proxy
            .await_reverse_connection(INFERENCE_SERVER_ID, Duration::from_secs(2))
            .await
    );

    let rtt = proxy.health_check_rtt(INFERENCE_SERVER_ID).await.unwrap();
    assert!(rtt.as_millis() < 1000);

    handle.shutdown().await;
    shutdown.cancel();
}

#[tokio::test]
async fn handshake_nack_for_unregistered_server() {
    let state = Arc::new(StargateState::new());
    let (_proxy, addr, shutdown) = start_tunnel_server(state).await;

    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let mut config = ReverseQuicTunnelConfig::new(
        format!("127.0.0.1:{}", addr.port()),
        "unregistered-backend".to_string(),
        backend_url,
    );
    config.quic_insecure = true;
    let result = start_reverse_quic_tunnel(config).await;

    assert!(result.is_err(), "expected handshake to be rejected");

    shutdown.cancel();
}

#[tokio::test]
async fn url_change_evicts_stale_pool_entry() {
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let state = Arc::new(StargateState::new());
    register_backend(&state, INFERENCE_SERVER_ID, true).await;
    let (proxy, addr, shutdown) = start_tunnel_server(state).await;

    let handle = connect_reverse_tunnel(addr, &backend_url).await;
    assert!(
        proxy
            .await_reverse_connection(INFERENCE_SERVER_ID, Duration::from_secs(2))
            .await,
    );
    assert!(proxy.has_healthy_connection(INFERENCE_SERVER_ID).await);

    let mut watcher = ConnectionWatcher::new(proxy.clone(), Duration::from_millis(100));
    watcher.states.insert(
        INFERENCE_SERVER_ID.to_string(),
        ConnState::Connected {
            url: "quic://1.2.3.4:9999".to_string(),
        },
    );

    let result = watcher
        .ensure_connected(INFERENCE_SERVER_ID, "quic://5.6.7.8:1111", true)
        .await;
    assert!(matches!(result, EnsureConnectedResult::ReverseDisconnected));
    assert!(
        !proxy.has_healthy_connection(INFERENCE_SERVER_ID).await,
        "stale pool entry should have been evicted on URL change"
    );

    handle.shutdown().await;
    shutdown.cancel();
}

#[tokio::test]
async fn duplicate_reverse_connection_rejected() {
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let state = Arc::new(StargateState::new());
    register_backend(&state, INFERENCE_SERVER_ID, true).await;
    let (proxy, addr, shutdown) = start_tunnel_server(state).await;

    let handle1 = connect_reverse_tunnel(addr, &backend_url).await;
    assert!(
        proxy
            .await_reverse_connection(INFERENCE_SERVER_ID, Duration::from_secs(2))
            .await,
        "first reverse connection should be accepted"
    );

    let (listener2, backend_url2) = setup_mock_backend().await;
    let app2 = Router::new().route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        let _ = axum::serve(listener2, app2).await;
    });

    let mut dup_config = ReverseQuicTunnelConfig::new(
        format!("127.0.0.1:{}", addr.port()),
        INFERENCE_SERVER_ID.to_string(),
        backend_url2,
    );
    dup_config.quic_insecure = true;
    let result = start_reverse_quic_tunnel(dup_config).await;
    assert!(
        result.is_err(),
        "second connection with same id should be rejected while first is active"
    );

    handle1.shutdown().await;
    shutdown.cancel();
}

#[tokio::test]
async fn await_reverse_connection_ignores_closed_pool_entry() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_secs(5),
            request_timeout: Duration::from_secs(5),
            direct_quic_connections: 1,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: TunnelTransportProtocol::Custom,
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .unwrap();
    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        backend_url,
    ))
    .await
    .unwrap();

    proxy
        .preconnect(
            INFERENCE_SERVER_ID,
            &format!("quic://{}", tunnel.listen_addr()),
        )
        .await
        .unwrap();
    assert!(
        proxy
            .await_reverse_connection(INFERENCE_SERVER_ID, Duration::from_secs(1))
            .await
    );

    tunnel.shutdown().await;
    tokio::time::timeout(Duration::from_secs(2), async {
        while proxy.has_healthy_connection(INFERENCE_SERVER_ID).await {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("pooled connection should close");

    assert!(
        !proxy
            .await_reverse_connection(INFERENCE_SERVER_ID, Duration::from_millis(50))
            .await,
        "closed pool entry must not satisfy reverse connection wait"
    );
}

#[test]
fn build_client_config_insecure_succeeds_without_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let result = build_client_config(None, true, TunnelTransportProtocol::Custom);
    assert!(result.is_ok());
}

#[test]
fn build_client_config_secure_fails_without_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let result = build_client_config(None, false, TunnelTransportProtocol::Custom);
    assert!(result.is_err());
}

#[test]
fn build_client_config_secure_succeeds_with_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (cert_pem, _key_pem) = stargate_tls::generate_self_signed_cert().unwrap();
    let result = build_client_config(Some(&cert_pem), false, TunnelTransportProtocol::Custom);
    assert!(result.is_ok());
}

#[test]
fn build_server_config_self_signed_when_none() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let result = build_server_config(
        &stargate_tls::ServerTlsIdentity::SelfSigned,
        TunnelTransportProtocol::Custom,
    );
    assert!(result.is_ok());
}

#[test]
fn build_server_config_uses_provided_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (cert_pem, key_pem) = stargate_tls::generate_self_signed_cert().unwrap();
    let result = build_server_config(
        &stargate_tls::ServerTlsIdentity::Provided { cert_pem, key_pem },
        TunnelTransportProtocol::Custom,
    );
    assert!(result.is_ok());
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn reverse_tunnel_works_with_secure_client_and_provided_cert() {
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route("/health", get(|| async { (StatusCode::OK, "ok") }));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (cert_pem, key_pem) = stargate_tls::generate_self_signed_cert().unwrap();

    let state = Arc::new(StargateState::new());
    register_backend(&state, INFERENCE_SERVER_ID, true).await;

    let proxy = Arc::new(
        QuicHttpProxy::new(
            QuicTunnelConfig {
                connect_timeout: Duration::from_secs(5),
                request_timeout: Duration::from_secs(5),
                direct_quic_connections: 1,
                tls_cert_pem: Some(cert_pem.clone()),
                server_tls_identity: stargate_tls::ServerTlsIdentity::Provided {
                    cert_pem: cert_pem.clone(),
                    key_pem,
                },
                quic_insecure: false,
                tunnel_protocol: Default::default(),
            },
            Arc::new(crate::auth::OpenAuthenticator),
        )
        .unwrap(),
    );
    let shutdown = CancellationToken::new();
    let addr = proxy
        .start_reverse_listener(
            "127.0.0.1:0".parse().unwrap(),
            state,
            shutdown.clone(),
            TaskTracker::new(),
            None,
            None,
        )
        .await
        .unwrap();

    let mut config = ReverseQuicTunnelConfig::new(
        format!("127.0.0.1:{}", addr.port()),
        INFERENCE_SERVER_ID.to_string(),
        backend_url,
    );
    config.tls_cert_pem = Some(cert_pem);
    config.quic_insecure = false;
    let handle = start_reverse_quic_tunnel(config).await.unwrap();

    assert!(
        proxy
            .await_reverse_connection(INFERENCE_SERVER_ID, Duration::from_secs(2))
            .await
    );

    let rtt = proxy.health_check_rtt(INFERENCE_SERVER_ID).await.unwrap();
    assert!(rtt.as_millis() < 1000);

    handle.shutdown().await;
    shutdown.cancel();
}

#[tokio::test]
async fn reverse_tunnel_secure_client_rejects_unknown_server_cert() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();

    let state = Arc::new(StargateState::new());
    register_backend(&state, INFERENCE_SERVER_ID, true).await;
    let (_proxy, addr, shutdown) = start_tunnel_server(state).await;

    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let (different_cert, _) = stargate_tls::generate_self_signed_cert().unwrap();
    let mut config = ReverseQuicTunnelConfig::new(
        format!("127.0.0.1:{}", addr.port()),
        INFERENCE_SERVER_ID.to_string(),
        backend_url,
    );
    config.tls_cert_pem = Some(different_cert);
    config.quic_insecure = false;
    let result = start_reverse_quic_tunnel(config).await;

    assert!(
        result.is_err(),
        "secure client with different CA should reject server cert"
    );

    shutdown.cancel();
}

#[tokio::test]
async fn failed_reconnect_direct_preserves_existing_connection() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    let (listener, backend_url) = setup_mock_backend().await;
    let app = Router::new().route("/health", get(|| async { "ok" }));
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });

    let proxy = QuicHttpProxy::new(
        QuicTunnelConfig {
            connect_timeout: Duration::from_millis(50),
            request_timeout: Duration::from_secs(5),
            direct_quic_connections: 1,
            tls_cert_pem: None,
            server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
            quic_insecure: true,
            tunnel_protocol: Default::default(),
        },
        Arc::new(crate::auth::OpenAuthenticator),
    )
    .unwrap();
    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        backend_url,
    ))
    .await
    .unwrap();

    proxy
        .preconnect(
            INFERENCE_SERVER_ID,
            &format!("quic://{}", tunnel.listen_addr()),
        )
        .await
        .unwrap();
    assert!(proxy.has_healthy_connection(INFERENCE_SERVER_ID).await);

    let result = proxy
        .reconnect_direct(INFERENCE_SERVER_ID, "quic://not-an-ip:50072")
        .await;

    assert!(result.is_err());
    assert!(
        proxy.has_healthy_connection(INFERENCE_SERVER_ID).await,
        "failed reconnect must not remove the existing healthy connection"
    );

    tunnel.shutdown().await;
}

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

use std::future::Future;
use std::net::SocketAddr;
use std::sync::Arc;

use anyhow::Context;
use quinn::Endpoint;
use reqwest::header::{HeaderName, HeaderValue};
use tokio_util::{sync::CancellationToken, task::TaskTracker};

use stargate_protocol::TunnelTransportProtocol;
use stargate_protocol::tunnel_contract::{
    HEADER_INFERENCE_SERVER_ID, HEADER_REVERSE_AUTH_TOKEN, WEBTRANSPORT_TUNNEL_PATH,
};

use super::core::{TunnelForwardingConfig, TunnelServerApp, serve_bidi_streams};
use super::endpoint::{
    TunnelError, build_trusted_client_config, derive_sni, ensure_rustls_provider, target_authority,
};
use super::http3::handle_h3_established_connection;
use super::raw_quic::handle_stream;
use super::webtransport::handle_webtransport_stream;

#[derive(Clone, Debug)]
pub struct ReverseQuicTunnelConfig {
    pub target_addr: String,
    pub inference_server_id: String,
    pub upstream_http_base_url: String,
    pub forwarding: TunnelForwardingConfig,
    pub tls_cert_pem: Option<Vec<u8>>,
    pub quic_insecure: bool,
    pub tunnel_protocol: TunnelTransportProtocol,
    pub sni_override: Option<String>,
    pub auth_token_provider: Option<Arc<crate::AuthTokenProvider>>,
}

impl ReverseQuicTunnelConfig {
    pub fn new(
        target_addr: String,
        inference_server_id: String,
        upstream_http_base_url: String,
    ) -> Self {
        Self {
            target_addr,
            inference_server_id,
            upstream_http_base_url,
            forwarding: TunnelForwardingConfig::default(),
            tls_cert_pem: None,
            quic_insecure: false,
            tunnel_protocol: TunnelTransportProtocol::RawQuic,
            sni_override: None,
            auth_token_provider: None,
        }
    }
}

impl TunnelServerApp {
    pub(super) fn from_reverse_config(config: ReverseQuicTunnelConfig) -> Self {
        Self::new(
            config.inference_server_id,
            config.upstream_http_base_url,
            config.forwarding,
        )
    }
}

pub struct ReverseQuicTunnelHandle {
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
}

impl ReverseQuicTunnelHandle {
    fn spawn<Root, RootFuture>(root: Root) -> Self
    where
        Root: FnOnce(CancellationToken, TaskTracker) -> RootFuture,
        RootFuture: Future<Output = ()> + Send + 'static,
    {
        let shutdown = CancellationToken::new();
        let task_tracker = TaskTracker::new();
        task_tracker.spawn(root(shutdown.clone(), task_tracker.clone()));
        task_tracker.close();
        Self {
            shutdown,
            task_tracker,
        }
    }

    pub async fn shutdown(self) {
        self.shutdown.cancel();
        self.task_tracker.wait().await;
    }

    pub async fn closed(&self) {
        self.task_tracker.wait().await;
    }
}

impl Drop for ReverseQuicTunnelHandle {
    fn drop(&mut self) {
        self.shutdown.cancel();
    }
}

pub(super) struct ReverseQuicClientConnection {
    endpoint: Endpoint,
    pub(super) connection: quinn::Connection,
}

pub(super) fn reverse_quic_sni(config: &ReverseQuicTunnelConfig) -> String {
    config
        .sni_override
        .clone()
        .unwrap_or_else(|| derive_sni(&config.target_addr))
}

pub(super) fn reverse_quic_dial_candidates(
    resolved_addrs: &[SocketAddr],
) -> Result<Vec<SocketAddr>, TunnelError> {
    let dial_candidates = stargate_tls::ordered_dial_candidates(resolved_addrs.iter().copied());
    if dial_candidates.is_empty() {
        return Err(TunnelError::NoResolvedAddress);
    }
    Ok(dial_candidates)
}

pub(super) async fn connect_first_reverse_quic_candidate<T, Connect, ConnectFuture>(
    candidates: &[SocketAddr],
    mut connect: Connect,
) -> anyhow::Result<(SocketAddr, T)>
where
    Connect: FnMut(SocketAddr) -> ConnectFuture,
    ConnectFuture: Future<Output = anyhow::Result<T>>,
{
    let mut failures = Vec::new();
    for candidate in candidates {
        match connect(*candidate).await {
            Ok(connection) => return Ok((*candidate, connection)),
            Err(error) => failures.push(format!("{candidate}: {error:#}")),
        }
    }

    anyhow::bail!(
        "failed to connect to every resolved reverse QUIC address: {}",
        failures.join("; ")
    )
}

pub(super) async fn connect_reverse_quic_endpoint(
    config: &ReverseQuicTunnelConfig,
) -> Result<ReverseQuicClientConnection, TunnelError> {
    let client_config = build_trusted_client_config(
        config.tls_cert_pem.as_deref(),
        config.quic_insecure,
        config.tunnel_protocol,
    )
    .map_err(|source| TunnelError::Tls { source })?;
    let sni = reverse_quic_sni(config);
    let alpn_protocols: Vec<_> = config
        .tunnel_protocol
        .alpn_protocols()
        .into_iter()
        .map(|protocol| String::from_utf8_lossy(&protocol).into_owned())
        .collect();
    let resolved_addrs: Vec<_> = tokio::net::lookup_host(&config.target_addr)
        .await
        .map_err(|source| TunnelError::Connect {
            context: "resolving reverse tunnel address",
            source: source.into(),
        })?
        .collect();
    let dial_candidates = reverse_quic_dial_candidates(&resolved_addrs)?;
    tracing::debug!(
        transport = "quic",
        target_addr = %config.target_addr,
        resolved_addrs = ?resolved_addrs,
        dial_candidates = ?dial_candidates,
        sni = %sni,
        tunnel_protocol = %config.tunnel_protocol,
        alpn_protocols = ?alpn_protocols,
        quic_insecure = config.quic_insecure,
        "resolved Stargate reverse QUIC target"
    );
    let (_, connection) = connect_first_reverse_quic_candidate(&dial_candidates, |dial_target| {
        let client_config = client_config.clone();
        let sni = &sni;
        let alpn_protocols = &alpn_protocols;
        async move {
            let mut endpoint = Endpoint::client(stargate_tls::quic_client_bind_addr(dial_target))
                .context("bind reverse QUIC client")?;
            endpoint.set_default_client_config(client_config);
            let local_addr = endpoint.local_addr().ok();
            tracing::debug!(
                transport = "quic",
                target_addr = %config.target_addr,
                dial_target = %dial_target,
                sni = %sni,
                tunnel_protocol = %config.tunnel_protocol,
                alpn_protocols = ?alpn_protocols,
                quic_insecure = config.quic_insecure,
                local_addr = ?local_addr,
                "attempting Stargate reverse QUIC connection"
            );
            let connection = endpoint
                .connect(dial_target, sni)
                .context("start reverse QUIC connection")?
                .await
                .context("establish reverse QUIC connection")?;
            tracing::debug!(
                transport = "quic",
                target_addr = %config.target_addr,
                dial_target = %dial_target,
                remote_addr = %connection.remote_address(),
                stable_id = connection.stable_id(),
                sni = %sni,
                tunnel_protocol = %config.tunnel_protocol,
                alpn_protocols = ?alpn_protocols,
                quic_insecure = config.quic_insecure,
                local_addr = ?local_addr,
                stats = ?connection.stats(),
                "Stargate reverse QUIC connection established"
            );
            Ok(ReverseQuicClientConnection {
                endpoint,
                connection,
            })
        }
    })
    .await
    .map_err(|source| TunnelError::Connect {
        context: "connecting to resolved reverse tunnel addresses",
        source,
    })?;

    Ok(connection)
}

async fn resolve_reverse_auth_token(
    config: &ReverseQuicTunnelConfig,
) -> Result<Option<String>, TunnelError> {
    let Some(provider) = &config.auth_token_provider else {
        return Ok(None);
    };

    provider
        .resolve_token()
        .await
        .map(Some)
        .map_err(|source| handshake_error("reading reverse tunnel auth token", source))
}

fn handshake_error(context: &'static str, source: impl Into<anyhow::Error>) -> TunnelError {
    TunnelError::Handshake {
        context,
        source: source.into(),
    }
}

pub async fn start_reverse_quic_tunnel(
    config: ReverseQuicTunnelConfig,
) -> Result<ReverseQuicTunnelHandle, TunnelError> {
    ensure_rustls_provider();
    if config.tunnel_protocol == TunnelTransportProtocol::WebTransport {
        return start_reverse_webtransport_tunnel(config).await;
    }
    let ReverseQuicClientConnection {
        endpoint,
        connection,
    } = connect_reverse_quic_endpoint(&config).await?;

    let (mut send, mut recv) = connection
        .open_bi()
        .await
        .map_err(|source| handshake_error("opening reverse tunnel handshake stream", source))?;

    let auth_token = resolve_reverse_auth_token(&config).await?;

    let handshake_request = stargate_protocol::HandshakeRequest {
        inference_server_id: config.inference_server_id.clone(),
        auth_token,
    };
    stargate_protocol::write_handshake(&mut send, &handshake_request)
        .await
        .map_err(|source| handshake_error("writing reverse tunnel handshake", source))?;
    send.finish()
        .map_err(|source| handshake_error("finishing reverse tunnel handshake stream", source))?;

    let ack = stargate_protocol::read_handshake_ack(&mut recv)
        .await
        .map_err(|source| handshake_error("reading reverse tunnel handshake ack", source))?;
    if !ack.accepted {
        return Err(TunnelError::HandshakeRejected { reason: ack.reason });
    }

    let tunnel_protocol = config.tunnel_protocol;
    let app = TunnelServerApp::from_reverse_config(config);

    Ok(match tunnel_protocol {
        TunnelTransportProtocol::RawQuic => {
            ReverseQuicTunnelHandle::spawn(move |shutdown, stream_tracker| {
                serve_bidi_streams(
                    endpoint,
                    app,
                    connection,
                    shutdown,
                    stream_tracker,
                    handle_stream,
                    |error| tracing::warn!(%error, "reverse tunnel stream failed"),
                )
            })
        }
        TunnelTransportProtocol::Http3 => {
            ReverseQuicTunnelHandle::spawn(move |shutdown, stream_tracker| async move {
                let _endpoint = endpoint;
                if let Err(error) =
                    handle_h3_established_connection(connection, shutdown, stream_tracker, app)
                        .await
                {
                    tracing::warn!(error = %error, "reverse h3 tunnel connection failed");
                }
            })
        }
        TunnelTransportProtocol::WebTransport => {
            unreachable!("WebTransport reverse tunnels are handled before the QUIC handshake path")
        }
    })
}

async fn start_reverse_webtransport_tunnel(
    config: ReverseQuicTunnelConfig,
) -> Result<ReverseQuicTunnelHandle, TunnelError> {
    let ReverseQuicClientConnection {
        endpoint,
        connection,
    } = connect_reverse_quic_endpoint(&config).await?;
    let auth_token = resolve_reverse_auth_token(&config).await?;

    let mut builder = h3::client::builder();
    builder.enable_extended_connect(true).enable_datagram(true);
    let (h3_connection, mut send_request): (
        h3::client::Connection<h3_quinn::Connection, bytes::Bytes>,
        h3::client::SendRequest<h3_quinn::OpenStreams, bytes::Bytes>,
    ) = builder
        .build(h3_quinn::Connection::new(connection.clone()))
        .await
        .map_err(|source| handshake_error("creating h3 client", source))?;
    let mut request: http::Request<()> = http::Request::builder()
        .method(reqwest::Method::CONNECT.as_str())
        .uri(format!(
            "https://{}{WEBTRANSPORT_TUNNEL_PATH}",
            target_authority(&config.target_addr)
        ))
        .header(
            HEADER_INFERENCE_SERVER_ID,
            config.inference_server_id.as_str(),
        )
        .body(())
        .map_err(|source| handshake_error("building WebTransport CONNECT request", source))?;
    if let Some(token) = &auth_token {
        request.headers_mut().insert(
            HeaderName::from_static(HEADER_REVERSE_AUTH_TOKEN),
            HeaderValue::from_str(token).map_err(|source| {
                handshake_error("validating reverse tunnel auth token header", source)
            })?,
        );
    }
    request
        .extensions_mut()
        .insert(h3::ext::Protocol::WEB_TRANSPORT);
    let mut connect_stream = send_request
        .send_request(request)
        .await
        .map_err(|source| handshake_error("sending WebTransport CONNECT request", source))?;
    let session_id = connect_stream.id().into_inner();
    connect_stream
        .finish()
        .await
        .map_err(|source| handshake_error("finishing WebTransport CONNECT request", source))?;
    let response = connect_stream
        .recv_response()
        .await
        .map_err(|source| handshake_error("receiving WebTransport CONNECT response", source))?;
    if !response.status().is_success() {
        return Err(TunnelError::WebTransportConnectRejected {
            status: response.status(),
        });
    }

    let app = TunnelServerApp::from_reverse_config(config);

    Ok(ReverseQuicTunnelHandle::spawn(
        move |shutdown, stream_tracker| {
            serve_bidi_streams(
                (endpoint, h3_connection, connect_stream),
                app,
                connection,
                shutdown,
                stream_tracker,
                move |send, recv, app| handle_webtransport_stream(send, recv, session_id, app),
                |error| tracing::warn!(%error, "reverse WebTransport stream failed"),
            )
        },
    ))
}

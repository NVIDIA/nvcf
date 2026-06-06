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

use quinn::Endpoint;
use reqwest::header::{HeaderName, HeaderValue};
use tokio_util::sync::CancellationToken;
use tokio_util::task::TaskTracker;

use stargate_protocol::TunnelTransportProtocol;
use stargate_protocol::tunnel_contract::{
    HEADER_INFERENCE_SERVER_ID, HEADER_REVERSE_AUTH_TOKEN, WEBTRANSPORT_TUNNEL_PATH,
};

use crate::output_token_parser::OutputTokenParserFactory;
use crate::queue_admission::{PylonQueueMismatchRetryConfig, QueueAdmissionTracker};
use crate::request_observer::RequestObservation;
use crate::request_quality_monitor::RequestQualityMonitorConfig;
use crate::stats::PylonMetrics;

use super::core::{
    DEFAULT_FIRST_OUTPUT_TIMEOUT, DEFAULT_MAX_BODY_BYTES, DEFAULT_OUTPUT_CHUNK_TIMEOUT,
    PylonRetryConfig, TunnelServerApp,
};
use super::custom::handle_stream;
use super::endpoint::{
    TunnelError, build_trusted_client_config, derive_sni, ensure_rustls_provider, target_authority,
};
use super::http3::handle_h3_established_connection;
use super::webtransport::handle_webtransport_stream;

#[derive(Clone, Debug)]
pub struct ReverseQuicTunnelConfig {
    pub target_addr: String,
    pub inference_server_id: String,
    pub upstream_http_base_url: String,
    pub max_request_body_bytes: usize,
    pub first_output_timeout: Duration,
    pub output_chunk_timeout: Duration,
    pub output_token_parser_factory: OutputTokenParserFactory,
    pub tls_cert_pem: Option<Vec<u8>>,
    pub quic_insecure: bool,
    pub tunnel_protocol: TunnelTransportProtocol,
    pub request_observation_tx: Option<flume::Sender<RequestObservation>>,
    pub request_quality_monitor: RequestQualityMonitorConfig,
    pub sni_override: Option<String>,
    pub auth_token_provider: Option<std::sync::Arc<crate::AuthTokenProvider>>,
    pub retry: PylonRetryConfig,
    pub queue_mismatch_retry: PylonQueueMismatchRetryConfig,
    pub queue_tracker: QueueAdmissionTracker,
    pub metrics: Option<Arc<PylonMetrics>>,
    #[cfg(test)]
    pub webtransport_stream_header_wait_tx: Option<flume::Sender<()>>,
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
            max_request_body_bytes: DEFAULT_MAX_BODY_BYTES,
            first_output_timeout: DEFAULT_FIRST_OUTPUT_TIMEOUT,
            output_chunk_timeout: DEFAULT_OUTPUT_CHUNK_TIMEOUT,
            output_token_parser_factory: OutputTokenParserFactory,
            tls_cert_pem: None,
            quic_insecure: false,
            tunnel_protocol: TunnelTransportProtocol::Custom,
            request_observation_tx: None,
            request_quality_monitor: RequestQualityMonitorConfig::default(),
            sni_override: None,
            auth_token_provider: None,
            retry: PylonRetryConfig::default(),
            queue_mismatch_retry: PylonQueueMismatchRetryConfig::default(),
            queue_tracker: QueueAdmissionTracker::default(),
            metrics: None,
            #[cfg(test)]
            webtransport_stream_header_wait_tx: None,
        }
    }
}

impl TunnelServerApp {
    pub(super) fn from_reverse_config(config: ReverseQuicTunnelConfig) -> Self {
        Self {
            http_client: reqwest::Client::new(),
            inference_server_id: config.inference_server_id,
            upstream_http_base_url: config.upstream_http_base_url,
            max_request_body_bytes: config.max_request_body_bytes,
            first_output_timeout: config.first_output_timeout,
            output_chunk_timeout: config.output_chunk_timeout,
            output_token_parser_factory: config.output_token_parser_factory,
            request_observation_tx: config.request_observation_tx,
            request_quality_monitor: config.request_quality_monitor,
            retry: config.retry,
            queue_mismatch_retry: config.queue_mismatch_retry,
            queue_tracker: config.queue_tracker,
            metrics: config.metrics,
            #[cfg(test)]
            webtransport_stream_header_wait_tx: config.webtransport_stream_header_wait_tx,
        }
    }
}

pub struct ReverseQuicTunnelHandle {
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
}

impl ReverseQuicTunnelHandle {
    pub async fn shutdown(self) {
        self.shutdown.cancel();
        self.task_tracker.close();
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

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) struct ReverseQuicConnectDebugTarget {
    pub(super) target_addr: String,
    pub(super) sni: String,
    pub(super) tunnel_protocol: TunnelTransportProtocol,
    pub(super) alpn_protocols: Vec<String>,
    pub(super) quic_insecure: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) struct ReverseQuicResolvedTarget {
    pub(super) resolved_addrs: Vec<SocketAddr>,
    pub(super) resolved_target: SocketAddr,
}

pub(super) fn reverse_quic_connect_debug_target(
    config: &ReverseQuicTunnelConfig,
) -> ReverseQuicConnectDebugTarget {
    let sni = config
        .sni_override
        .as_deref()
        .map(String::from)
        .unwrap_or_else(|| derive_sni(&config.target_addr));

    ReverseQuicConnectDebugTarget {
        target_addr: config.target_addr.clone(),
        sni,
        tunnel_protocol: config.tunnel_protocol,
        alpn_protocols: alpn_protocols_for_debug(config.tunnel_protocol),
        quic_insecure: config.quic_insecure,
    }
}

async fn resolve_reverse_quic_target_addr(
    target_addr: &str,
) -> Result<ReverseQuicResolvedTarget, TunnelError> {
    let resolved_addrs: Vec<_> = tokio::net::lookup_host(target_addr)
        .await
        .map_err(|source| TunnelError::Connect {
            context: "resolving reverse tunnel address",
            source: source.into(),
        })?
        .collect();
    reverse_quic_resolved_target(resolved_addrs)
}

pub(super) fn reverse_quic_resolved_target(
    resolved_addrs: Vec<SocketAddr>,
) -> Result<ReverseQuicResolvedTarget, TunnelError> {
    let resolved_target = resolved_addrs
        .iter()
        .find(|addr| addr.is_ipv4())
        .copied()
        .or_else(|| resolved_addrs.first().copied())
        .ok_or(TunnelError::NoResolvedAddress)?;

    Ok(ReverseQuicResolvedTarget {
        resolved_addrs,
        resolved_target,
    })
}

fn alpn_protocols_for_debug(tunnel_protocol: TunnelTransportProtocol) -> Vec<String> {
    tunnel_protocol
        .alpn_protocols()
        .into_iter()
        .map(|protocol| String::from_utf8_lossy(&protocol).into_owned())
        .collect()
}

fn log_reverse_quic_connect_attempt(
    target: &ReverseQuicConnectDebugTarget,
    local_addr: Option<SocketAddr>,
) {
    tracing::debug!(
        transport = "quic",
        target_addr = %target.target_addr,
        sni = %target.sni,
        tunnel_protocol = %target.tunnel_protocol,
        alpn_protocols = ?target.alpn_protocols,
        quic_insecure = target.quic_insecure,
        local_addr = ?local_addr,
        "attempting Stargate reverse QUIC connection"
    );
}

fn log_reverse_quic_resolved_target(
    target: &ReverseQuicConnectDebugTarget,
    resolved: &ReverseQuicResolvedTarget,
) {
    tracing::debug!(
        transport = "quic",
        target_addr = %target.target_addr,
        resolved_target = %resolved.resolved_target,
        resolved_addrs = ?resolved.resolved_addrs,
        sni = %target.sni,
        tunnel_protocol = %target.tunnel_protocol,
        alpn_protocols = ?target.alpn_protocols,
        quic_insecure = target.quic_insecure,
        "resolved Stargate reverse QUIC target"
    );
}

fn log_reverse_quic_connected(
    target: &ReverseQuicConnectDebugTarget,
    resolved: &ReverseQuicResolvedTarget,
    local_addr: Option<SocketAddr>,
    connection: &quinn::Connection,
) {
    let remote_addr = connection.remote_address();
    let stable_id = connection.stable_id();
    let stats = connection.stats();

    tracing::debug!(
        transport = "quic",
        target_addr = %target.target_addr,
        resolved_target = %resolved.resolved_target,
        remote_addr = %remote_addr,
        stable_id,
        sni = %target.sni,
        tunnel_protocol = %target.tunnel_protocol,
        alpn_protocols = ?target.alpn_protocols,
        quic_insecure = target.quic_insecure,
        local_addr = ?local_addr,
        stats = ?stats,
        "Stargate reverse QUIC connection established"
    );
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
    let mut endpoint = Endpoint::client("0.0.0.0:0".parse().unwrap()).map_err(TunnelError::Bind)?;
    endpoint.set_default_client_config(client_config);
    let local_addr = endpoint.local_addr().ok();

    let connect_target = reverse_quic_connect_debug_target(config);
    log_reverse_quic_connect_attempt(&connect_target, local_addr);
    let resolved_target = resolve_reverse_quic_target_addr(&config.target_addr).await?;
    log_reverse_quic_resolved_target(&connect_target, &resolved_target);
    let connection = endpoint
        .connect(resolved_target.resolved_target, &connect_target.sni)
        .map_err(|source| TunnelError::Connect {
            context: "starting QUIC connection",
            source: source.into(),
        })?
        .await
        .map_err(|source| TunnelError::Connect {
            context: "establishing QUIC connection",
            source: source.into(),
        })?;
    log_reverse_quic_connected(&connect_target, &resolved_target, local_addr, &connection);

    Ok(ReverseQuicClientConnection {
        endpoint,
        connection,
    })
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
        .map_err(|source| TunnelError::Handshake {
            context: "reading reverse tunnel auth token",
            source,
        })
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

    let (mut send, mut recv) =
        connection
            .open_bi()
            .await
            .map_err(|source| TunnelError::Handshake {
                context: "opening reverse tunnel handshake stream",
                source: source.into(),
            })?;

    let auth_token = resolve_reverse_auth_token(&config).await?;

    let handshake_request = stargate_protocol::HandshakeRequest {
        inference_server_id: config.inference_server_id.clone(),
        auth_token,
    };
    stargate_protocol::write_handshake(&mut send, &handshake_request)
        .await
        .map_err(|source| TunnelError::Handshake {
            context: "writing reverse tunnel handshake",
            source: source.into(),
        })?;
    send.finish().map_err(|source| TunnelError::Handshake {
        context: "finishing reverse tunnel handshake stream",
        source: source.into(),
    })?;

    let ack = stargate_protocol::read_handshake_ack(&mut recv)
        .await
        .map_err(|source| TunnelError::Handshake {
            context: "reading reverse tunnel handshake ack",
            source: source.into(),
        })?;
    if !ack.accepted {
        return Err(TunnelError::HandshakeRejected { reason: ack.reason });
    }

    let tunnel_protocol = config.tunnel_protocol;
    let shutdown = CancellationToken::new();
    let task_tracker = TaskTracker::new();
    let app = TunnelServerApp::from_reverse_config(config);

    let shutdown_for_task = shutdown.clone();
    let stream_tracker = task_tracker.clone();
    task_tracker.spawn(async move {
        let _endpoint = endpoint;
        match tunnel_protocol {
            TunnelTransportProtocol::Custom => loop {
                tokio::select! {
                    _ = shutdown_for_task.cancelled() => break,
                    stream = connection.accept_bi() => {
                        let Ok((quinn_send, quinn_recv)) = stream else { break };
                        let app = app.clone();
                        stream_tracker.spawn(async move {
                            if let Err(error) = handle_stream(quinn_send, quinn_recv, &app).await {
                                tracing::warn!(error = %error, "reverse tunnel stream failed");
                            }
                        });
                    }
                }
            },
            TunnelTransportProtocol::Http3 => {
                if let Err(error) = handle_h3_established_connection(
                    connection,
                    shutdown_for_task,
                    stream_tracker,
                    app,
                )
                .await
                {
                    tracing::warn!(error = %error, "reverse h3 tunnel connection failed");
                }
            }
            TunnelTransportProtocol::WebTransport => unreachable!(
                "WebTransport reverse tunnels are handled before the QUIC handshake path"
            ),
        }
    });
    task_tracker.close();

    Ok(ReverseQuicTunnelHandle {
        shutdown,
        task_tracker,
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
        .map_err(|source| TunnelError::Handshake {
            context: "creating h3 client",
            source: source.into(),
        })?;
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
        .map_err(|source| TunnelError::Handshake {
            context: "building WebTransport CONNECT request",
            source: source.into(),
        })?;
    if let Some(token) = &auth_token {
        request.headers_mut().insert(
            HeaderName::from_static(HEADER_REVERSE_AUTH_TOKEN),
            HeaderValue::from_str(token).map_err(|source| TunnelError::Handshake {
                context: "validating reverse tunnel auth token header",
                source: source.into(),
            })?,
        );
    }
    request
        .extensions_mut()
        .insert(h3::ext::Protocol::WEB_TRANSPORT);
    let mut connect_stream =
        send_request
            .send_request(request)
            .await
            .map_err(|source| TunnelError::Handshake {
                context: "sending WebTransport CONNECT request",
                source: source.into(),
            })?;
    let session_id = connect_stream.id().into_inner();
    connect_stream
        .finish()
        .await
        .map_err(|source| TunnelError::Handshake {
            context: "finishing WebTransport CONNECT request",
            source: source.into(),
        })?;
    let response =
        connect_stream
            .recv_response()
            .await
            .map_err(|source| TunnelError::Handshake {
                context: "receiving WebTransport CONNECT response",
                source: source.into(),
            })?;
    if !response.status().is_success() {
        return Err(TunnelError::WebTransportConnectRejected {
            status: response.status(),
        });
    }

    let shutdown = CancellationToken::new();
    let task_tracker = TaskTracker::new();
    let app = TunnelServerApp::from_reverse_config(config);

    let shutdown_for_task = shutdown.clone();
    let stream_tracker = task_tracker.clone();
    task_tracker.spawn(async move {
        let _endpoint = endpoint;
        let _h3_connection = h3_connection;
        let _connect_stream = connect_stream;
        loop {
            tokio::select! {
                _ = shutdown_for_task.cancelled() => break,
                stream = connection.accept_bi() => {
                    let Ok((quinn_send, quinn_recv)) = stream else { break };
                    let app = app.clone();
                    stream_tracker.spawn(async move {
                        if let Err(error) =
                            handle_webtransport_stream(
                                quinn_send,
                                quinn_recv,
                                session_id,
                                app,
                            )
                            .await
                        {
                            tracing::warn!(error = %error, "reverse WebTransport stream failed");
                        }
                    });
                }
            }
        }
    });
    task_tracker.close();

    Ok(ReverseQuicTunnelHandle {
        shutdown,
        task_tracker,
    })
}

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
use std::net::SocketAddr;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use anyhow::{Context, Result, anyhow, bail, ensure};
use axum::body::Body;
use axum::http::{HeaderMap, HeaderName, HeaderValue, Method};
use quinn::{ClientConfig, Connection, Endpoint};
use rustls::RootCertStore;
use tokio::sync::RwLock;
use tracing::{info, info_span};
use url::Url;

use stargate_protocol::tunnel_contract::{HEADER_INPUT_TOKENS, HEADER_MODEL, HEADER_REQUEST_ID};
use stargate_protocol::{
    RecvStream, SendStream, TunnelTransportProtocol, WebTransportHttpRequestHead,
};
use stargate_tls::ServerTlsIdentity;

use crate::auth::WorkerAuthenticator;

use super::body::{OpenStreamingRequest, OpenStreamingRequestInner, remaining_request_timeout};
use super::http3::{Http3ConnectionHandle, h3_error, should_forward_h3_tunnel_request_header};
use super::reverse::ReverseConnectionEvents;
use super::webtransport::WebTransportConnectionHandle;
use super::{QuicTunnelConfig, StreamingResponse};

pub struct QuicHttpProxy {
    pub(super) config: QuicTunnelConfig,
    pub(super) endpoint_v4: Arc<Endpoint>,
    pub(super) endpoint_v6: Arc<Endpoint>,
    pub(super) pool: Arc<RwLock<HashMap<String, TunnelConnectionSet>>>,
    pub(super) reverse_connection_events: ReverseConnectionEvents,
    pub(super) pending_reverse_connections: Arc<Mutex<HashSet<String>>>,
    pub(super) authenticator: Arc<dyn WorkerAuthenticator>,
}

#[derive(Clone)]
pub(super) enum TunnelConnection {
    Custom(Connection),
    Http3(Http3ConnectionHandle),
    WebTransport(WebTransportConnectionHandle),
}

impl TunnelConnection {
    pub(super) fn is_healthy(&self) -> bool {
        match self {
            Self::Custom(connection) => connection.close_reason().is_none(),
            Self::Http3(handle) => {
                connection_is_healthy(&handle.connection)
                    && !handle.driver_closed.load(Ordering::Acquire)
            }
            Self::WebTransport(handle) => connection_is_healthy(&handle.connection),
        }
    }

    fn stable_id(&self) -> usize {
        match self {
            Self::Custom(connection) => connection.stable_id(),
            Self::Http3(handle) => handle.connection.stable_id(),
            Self::WebTransport(handle) => handle.connection.stable_id(),
        }
    }
}

#[derive(Clone)]
pub(super) struct TunnelConnectionSet {
    pub(super) inner: Arc<TunnelConnectionSetInner>,
}

pub(super) struct TunnelConnectionSetInner {
    pub(super) connections: Vec<TunnelConnection>,
    pub(super) cursor: AtomicUsize,
}

impl TunnelConnectionSet {
    pub(super) fn new(connections: Vec<TunnelConnection>) -> Result<Self> {
        ensure!(!connections.is_empty(), "tunnel connection set is empty");
        Ok(Self {
            inner: Arc::new(TunnelConnectionSetInner {
                connections,
                cursor: AtomicUsize::new(0),
            }),
        })
    }

    pub(super) fn single(connection: TunnelConnection) -> Self {
        Self {
            inner: Arc::new(TunnelConnectionSetInner {
                connections: vec![connection],
                cursor: AtomicUsize::new(0),
            }),
        }
    }

    #[cfg(test)]
    pub(super) fn len(&self) -> usize {
        self.inner.connections.len()
    }

    pub(super) fn is_healthy(&self) -> bool {
        self.inner
            .connections
            .iter()
            .any(TunnelConnection::is_healthy)
    }

    pub(super) fn needs_replenishment(&self) -> bool {
        !self
            .inner
            .connections
            .iter()
            .all(TunnelConnection::is_healthy)
    }

    pub(super) fn choose_healthy(&self) -> Option<TunnelConnection> {
        let len = self.inner.connections.len();
        // The cursor only spreads load across equivalent live connections, so
        // relaxed ordering is enough; the health check below owns correctness.
        let start = self.inner.cursor.fetch_add(1, Ordering::Relaxed) % len;
        for offset in 0..len {
            let index = (start + offset) % len;
            let connection = &self.inner.connections[index];
            if connection.is_healthy() {
                return Some(connection.clone());
            }
        }
        None
    }

    pub(super) fn contains_stable_id(&self, stable_id: usize) -> bool {
        self.inner
            .connections
            .iter()
            .any(|connection| connection.stable_id() == stable_id)
    }
}

fn connection_is_healthy(connection: &Connection) -> bool {
    connection.close_reason().is_none()
}

impl QuicHttpProxy {
    pub fn new(
        config: QuicTunnelConfig,
        authenticator: Arc<dyn WorkerAuthenticator>,
    ) -> Result<Self> {
        ensure!(
            config.direct_quic_connections > 0,
            "direct_quic_connections must be > 0"
        );
        let client_config = build_client_config(
            config.tls_cert_pem.as_deref(),
            config.quic_insecure,
            config.tunnel_protocol,
        )?;
        let mut endpoint_v4 = Endpoint::client("0.0.0.0:0".parse()?)?;
        let mut endpoint_v6 = Endpoint::client("[::]:0".parse()?)?;
        endpoint_v4.set_default_client_config(client_config.clone());
        endpoint_v6.set_default_client_config(client_config);

        Ok(Self {
            config,
            endpoint_v4: Arc::new(endpoint_v4),
            endpoint_v6: Arc::new(endpoint_v6),
            pool: Arc::new(RwLock::new(HashMap::new())),
            reverse_connection_events: ReverseConnectionEvents::new(),
            pending_reverse_connections: Arc::new(Mutex::new(HashSet::new())),
            authenticator,
        })
    }

    pub async fn preconnect(&self, inference_server_id: &str, target_url: &str) -> Result<()> {
        let connection = self.connect_direct_set(target_url).await?;
        self.pool
            .write()
            .await
            .insert(inference_server_id.to_string(), connection);
        Ok(())
    }

    async fn connect_direct_set(&self, target_url: &str) -> Result<TunnelConnectionSet> {
        let mut connections = Vec::with_capacity(self.config.direct_quic_connections);
        // Opening the configured set up front lets hot-path requests distribute
        // stream creation across QUIC connections instead of piling onto one.
        for _ in 0..self.config.direct_quic_connections {
            connections.push(self.connect_direct(target_url).await?);
        }
        TunnelConnectionSet::new(connections)
    }

    async fn connect_direct(&self, target_url: &str) -> Result<TunnelConnection> {
        let addr = parse_quic_addr(target_url)?;
        let endpoint = if addr.is_ipv6() {
            self.endpoint_v6.as_ref()
        } else {
            self.endpoint_v4.as_ref()
        };
        let connect = endpoint
            .connect(addr, "stargate")
            .context("initiate quic connect failed")?;
        let connection = match tokio::time::timeout(self.config.connect_timeout, connect).await {
            Ok(result) => result.context("quic connect failed")?,
            Err(_) => bail!("quic connect timed out"),
        };
        match tokio::time::timeout(
            self.config.connect_timeout,
            self.build_direct_tunnel_connection(connection),
        )
        .await
        {
            Ok(result) => result,
            Err(_) => bail!("direct tunnel setup timed out"),
        }
    }

    pub async fn reconnect_direct(
        &self,
        inference_server_id: &str,
        target_url: &str,
    ) -> Result<()> {
        let connection = self.connect_direct_set(target_url).await?;
        self.pool
            .write()
            .await
            .insert(inference_server_id.to_string(), connection);
        Ok(())
    }

    pub async fn has_healthy_connection(&self, inference_server_id: &str) -> bool {
        self.pool
            .read()
            .await
            .get(inference_server_id)
            .is_some_and(TunnelConnectionSet::is_healthy)
    }

    pub async fn connection_set_needs_replenishment(&self, inference_server_id: &str) -> bool {
        self.pool
            .read()
            .await
            .get(inference_server_id)
            .is_none_or(TunnelConnectionSet::needs_replenishment)
    }

    pub async fn health_check_rtt(&self, inference_server_id: &str) -> Result<Duration> {
        let start = std::time::Instant::now();
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(HEADER_REQUEST_ID),
            HeaderValue::from_str(&format!("stargate-health-{inference_server_id}"))
                .context("invalid health check request id")?,
        );
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("stargate-health"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_INPUT_TOKENS),
            HeaderValue::from_static("0"),
        );
        let response = self
            .proxy_request_streaming(
                inference_server_id,
                Method::GET,
                "/health",
                headers,
                Body::empty(),
            )
            .await?;
        if !response.status.is_success() {
            bail!("health check returned status {}", response.status);
        }

        let mut body_stream = response.body_stream;
        while body_stream.recv_body().await?.is_some() {}

        Ok(start.elapsed())
    }

    async fn build_direct_tunnel_connection(
        &self,
        connection: Connection,
    ) -> Result<TunnelConnection> {
        match self.config.tunnel_protocol {
            TunnelTransportProtocol::Custom => Ok(TunnelConnection::Custom(connection)),
            TunnelTransportProtocol::Http3 => self.build_h3_client_connection(connection).await,
            TunnelTransportProtocol::WebTransport => {
                self.build_webtransport_client_connection(connection).await
            }
        }
    }

    pub async fn proxy_request_streaming(
        &self,
        inference_server_id: &str,
        method: Method,
        path_and_query: &str,
        headers: HeaderMap,
        body: Body,
    ) -> Result<StreamingResponse> {
        let request = self
            .open_streaming_request(inference_server_id, method, path_and_query, headers)
            .await?;
        request.send_body_and_recv_response(body).await
    }

    pub async fn open_streaming_request(
        &self,
        inference_server_id: &str,
        method: Method,
        path_and_query: &str,
        headers: HeaderMap,
    ) -> Result<OpenStreamingRequest> {
        let _span = info_span!("quic_http_proxy");
        let started_at = Instant::now();

        let connection = {
            let pool = self.pool.read().await;
            let connection_set = pool.get(inference_server_id).ok_or_else(|| {
                anyhow!("no connection for inference server '{inference_server_id}'")
            })?;
            connection_set.choose_healthy().ok_or_else(|| {
                anyhow!("connection to inference server '{inference_server_id}' is closed")
            })?
        };

        let req = async {
            match connection {
                TunnelConnection::Custom(connection) => {
                    let (quinn_send, quinn_recv) = connection
                        .open_bi()
                        .await
                        .context("open bi stream failed")?;

                    let mut send_stream = SendStream::new(quinn_send);
                    let recv_stream = RecvStream::new(quinn_recv);

                    let mut request_headers = HeaderMap::new();
                    request_headers.insert(
                        HeaderName::from_static("x-method"),
                        HeaderValue::from_str(method.as_str()).context("invalid method")?,
                    );
                    request_headers.insert(
                        HeaderName::from_static("x-path"),
                        HeaderValue::from_str(path_and_query).context("invalid path")?,
                    );
                    for (name, value) in &headers {
                        request_headers.append(name, value.clone());
                    }

                    send_stream
                        .send_header(request_headers)
                        .await
                        .context("failed to send request headers")?;

                    let response_header_timeout =
                        remaining_request_timeout(started_at, self.config.request_timeout);
                    Ok(OpenStreamingRequest {
                        inner: OpenStreamingRequestInner::Custom {
                            send_stream,
                            recv_stream,
                        },
                        response_header_timeout,
                    })
                }
                TunnelConnection::Http3(handle) => {
                    let uri: http::Uri = format!("https://stargate{path_and_query}")
                        .parse()
                        .context("invalid h3 request uri")?;
                    let mut request = http::Request::builder()
                        .method(method.as_str())
                        .uri(uri)
                        .body(())
                        .context("build h3 request")?;
                    for (name, value) in &headers {
                        if should_forward_h3_tunnel_request_header(name) {
                            request.headers_mut().append(name, value.clone());
                        }
                    }
                    let mut send_request = handle.send_request.clone();
                    let stream = send_request
                        .send_request(request)
                        .await
                        .map_err(h3_error)
                        .context("send h3 request headers")?;
                    let response_header_timeout =
                        remaining_request_timeout(started_at, self.config.request_timeout);
                    Ok(OpenStreamingRequest {
                        inner: OpenStreamingRequestInner::Http3 {
                            stream: Box::new(stream),
                            connection_handle: handle,
                        },
                        response_header_timeout,
                    })
                }
                TunnelConnection::WebTransport(handle) => {
                    let (quinn_send, quinn_recv) = handle
                        .connection
                        .open_bi()
                        .await
                        .context("open WebTransport bi stream failed")?;

                    let mut request_headers = HeaderMap::new();
                    for (name, value) in &headers {
                        request_headers.append(name, value.clone());
                    }

                    let mut quinn_send = quinn_send;
                    let request_head = WebTransportHttpRequestHead {
                        method: method.clone(),
                        path_and_query: path_and_query.to_string(),
                        headers: request_headers,
                    };
                    stargate_protocol::write_webtransport_http_request_head_after_prefix(
                        &mut quinn_send,
                        handle.bidi_header.clone(),
                        &request_head,
                    )
                    .await
                    .context("failed to send WebTransport request head")?;

                    let response_header_timeout =
                        remaining_request_timeout(started_at, self.config.request_timeout);
                    Ok(OpenStreamingRequest {
                        inner: OpenStreamingRequestInner::WebTransport {
                            send_stream: quinn_send,
                            recv_stream: quinn_recv,
                            connection_handle: handle,
                        },
                        response_header_timeout,
                    })
                }
            }
        };

        match tokio::time::timeout(self.config.request_timeout, req).await {
            Ok(inner) => inner,
            Err(_) => bail!("quic request timed out"),
        }
    }
}

pub(super) fn build_client_config(
    cert_pem: Option<&[u8]>,
    insecure: bool,
    tunnel_protocol: TunnelTransportProtocol,
) -> Result<ClientConfig> {
    if insecure {
        return stargate_tls::build_insecure_quic_client_config_with_alpn(
            tunnel_protocol.alpn_protocols(),
        );
    }
    let cert_data = cert_pem.context("TLS cert required when --quic-insecure is not set")?;
    let mut roots = RootCertStore::empty();
    for cert in rustls_pemfile::certs(&mut &*cert_data) {
        roots
            .add(cert.context("failed to parse tunnel cert PEM")?)
            .context("failed to add tunnel cert to root store")?;
    }

    let mut tls_config = rustls::ClientConfig::builder()
        .with_root_certificates(roots)
        .with_no_client_auth();
    tls_config.alpn_protocols = tunnel_protocol.alpn_protocols();

    Ok(ClientConfig::new(Arc::new(
        quinn::crypto::rustls::QuicClientConfig::try_from(tls_config)?,
    )))
}

pub(super) fn build_server_config(
    tls_identity: &ServerTlsIdentity,
    tunnel_protocol: TunnelTransportProtocol,
) -> Result<quinn::ServerConfig> {
    if matches!(tls_identity, ServerTlsIdentity::SelfSigned) {
        info!("no TLS cert/key provided, generating self-signed certificate");
    }
    let (cert_data, key_data) = tls_identity.pem_pair()?;
    let mut cert_reader = cert_data.as_ref();
    let cert_chain: Vec<rustls::pki_types::CertificateDer<'static>> =
        rustls_pemfile::certs(&mut cert_reader)
            .collect::<std::result::Result<_, _>>()
            .context("failed to parse reverse tunnel cert PEM")?;
    let mut key_reader = key_data.as_ref();
    let key = rustls_pemfile::private_key(&mut key_reader)
        .context("failed to parse reverse tunnel key PEM")?
        .context("no private key found in reverse tunnel PEM")?;
    let mut tls_config = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(cert_chain, key)
        .context("build reverse tunnel TLS server config failed")?;
    tls_config.alpn_protocols = tunnel_protocol.alpn_protocols();
    Ok(quinn::ServerConfig::with_crypto(Arc::new(
        quinn::crypto::rustls::QuicServerConfig::try_from(tls_config)
            .context("build reverse tunnel QUIC server config failed")?,
    )))
}
pub(super) fn parse_quic_addr(target_url: &str) -> Result<SocketAddr> {
    let parsed_url = Url::parse(target_url).context("invalid quic target url")?;
    if parsed_url.scheme() != "quic" {
        bail!("target url is not quic scheme");
    }
    let port = parsed_url
        .port_or_known_default()
        .ok_or_else(|| anyhow!("missing port in quic url"))?;
    let ip = parsed_url
        .host_str()
        .and_then(|h| h.parse().ok())
        .ok_or_else(|| anyhow!("quic inference_server_url host must be an IP address"))?;
    Ok(SocketAddr::new(ip, port))
}

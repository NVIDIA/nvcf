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

use anyhow::{Context, Result, anyhow, ensure};
use bytes::Bytes;
use http::{HeaderMap, Method, Request, Response, StatusCode};
use quinn::{ClientConfig, Endpoint, ServerConfig};
use stargate_forwarding::{HostnameMatcher, RelayEndpointConfig, build_relay_transport_config};
use stargate_protocol::TunnelTransportProtocol;
use stargate_protocol::tunnel_contract::WEBTRANSPORT_TUNNEL_PATH;
use stargate_tls::ServerTlsIdentity;
use tokio::sync::watch;
use tokio_util::{sync::CancellationToken, task::TaskTracker};
use tracing::{info, warn};

use crate::endpoints::{PodTarget, SniRouteRejection, TargetSnapshot, ready_target_for_sni};
use crate::metrics::RouterMetrics;
use crate::tls::{build_router_server_config, build_upstream_client_config};
use crate::webtransport_network::{connect_first_upstream_candidate, resolve_upstream_addrs};

const WEBTRANSPORT_STREAM_HEADER_TIMEOUT: Duration = Duration::from_secs(5);

type DownstreamH3Connection = h3::server::Connection<h3_quinn::Connection, Bytes>;
type DownstreamConnectStream = h3::server::RequestStream<
    <h3_quinn::Connection as h3::quic::OpenStreams<Bytes>>::BidiStream,
    Bytes,
>;
type UpstreamH3Connection = h3::client::Connection<h3_quinn::Connection, Bytes>;
type UpstreamConnectStream = h3::client::RequestStream<
    <h3_quinn::OpenStreams as h3::quic::OpenStreams<Bytes>>::BidiStream,
    Bytes,
>;
type UpstreamSendRequest = h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>;

#[derive(Clone, Debug)]
pub struct WebTransportRouterConfig {
    pub listen_addr: SocketAddr,
    pub advertised_hostname_template: String,
    pub target_namespace: String,
    pub connect_timeout: Duration,
    pub relay_max_idle_timeout: Duration,
    pub relay_keep_alive_interval: Option<Duration>,
    pub tls_cert_pem: Option<Vec<u8>>,
    pub tls_key_pem: Option<Vec<u8>>,
    pub upstream_tls_cert_pem: Option<Vec<u8>>,
    pub quic_insecure: bool,
}

struct WebTransportRouterRuntimeConfig {
    connect_timeout: Duration,
    hostname_matcher: Option<HostnameMatcher>,
    upstream_client_config: ClientConfig,
}

struct WebTransportRouterRuntime {
    endpoint: Endpoint,
    bound_addr: SocketAddr,
    config: Arc<WebTransportRouterRuntimeConfig>,
}

impl WebTransportRouterRuntime {
    fn bind(config: WebTransportRouterConfig) -> Result<Self> {
        let relay_config = RelayEndpointConfig {
            max_idle_timeout: config.relay_max_idle_timeout,
            keep_alive_interval: config.relay_keep_alive_interval,
        };
        let identity =
            ServerTlsIdentity::from_optional_pem(config.tls_cert_pem, config.tls_key_pem)?;
        let mut upstream_client_config = client_config(
            config.upstream_tls_cert_pem.as_deref(),
            config.quic_insecure,
        )?;
        upstream_client_config.transport_config(build_relay_transport_config(relay_config)?);
        let server_config = build_webtransport_server_config(&identity, relay_config)?;
        let endpoint = Endpoint::server(server_config, config.listen_addr)
            .context("bind WebTransport router QUIC listener")?;
        let bound_addr = endpoint
            .local_addr()
            .context("read WebTransport router listener address")?;
        let config = Arc::new(WebTransportRouterRuntimeConfig {
            connect_timeout: config.connect_timeout,
            hostname_matcher: HostnameMatcher::new(
                &config.advertised_hostname_template,
                &config.target_namespace,
            ),
            upstream_client_config,
        });

        Ok(Self {
            endpoint,
            bound_addr,
            config,
        })
    }

    async fn serve(
        self,
        targets: watch::Receiver<TargetSnapshot>,
        metrics: Arc<RouterMetrics>,
        shutdown: CancellationToken,
        connection_tasks: TaskTracker,
    ) -> Result<()> {
        info!(addr = %self.bound_addr, "WebTransport router listening");
        loop {
            tokio::select! {
                _ = shutdown.cancelled() => {
                    self.endpoint.close(0u32.into(), b"shutdown");
                    return Ok(());
                }
                incoming = self.endpoint.accept() => {
                    let Some(incoming) = incoming else {
                        warn!("WebTransport router endpoint stopped accepting");
                        return Ok(());
                    };
                    let targets = targets.clone();
                    let metrics = metrics.clone();
                    let config = self.config.clone();
                    let session_shutdown = shutdown.child_token();
                    let stream_tasks = connection_tasks.clone();
                    connection_tasks.spawn(async move {
                        if let Err(error) = dispatch_incoming(
                            incoming,
                            targets,
                            metrics,
                            config,
                            session_shutdown,
                            stream_tasks,
                        ).await {
                            warn!(%error, "WebTransport router session failed");
                        }
                    });
                }
            }
        }
    }
}

pub async fn serve_webtransport_router(
    config: WebTransportRouterConfig,
    targets: watch::Receiver<TargetSnapshot>,
    metrics: Arc<RouterMetrics>,
    shutdown: CancellationToken,
    connection_tasks: TaskTracker,
) -> Result<()> {
    WebTransportRouterRuntime::bind(config)?
        .serve(targets, metrics, shutdown, connection_tasks)
        .await
}

async fn dispatch_incoming(
    incoming: quinn::Incoming,
    targets: watch::Receiver<TargetSnapshot>,
    metrics: Arc<RouterMetrics>,
    config: Arc<WebTransportRouterRuntimeConfig>,
    shutdown: CancellationToken,
    connection_tasks: TaskTracker,
) -> Result<()> {
    let connection = tokio::select! {
        _ = shutdown.cancelled() => return Ok(()),
        connection = incoming => connection.context("accept downstream QUIC connection")?,
    };
    let route = {
        let snapshot = targets.borrow();
        owned_target_for_sni(
            downstream_server_name(&connection).as_deref(),
            &snapshot,
            config.hostname_matcher.as_ref(),
        )
    };
    let (target, server_name) = match route {
        Ok((target, server_name)) => {
            metrics.observe_webtransport_session("accepted");
            (target, server_name)
        }
        Err(rejection) => {
            let (outcome, close_reason) = rejection.metric_and_reason();
            metrics.observe_webtransport_session(outcome);
            connection.close(0u32.into(), close_reason);
            return Ok(());
        }
    };
    info!(
        target_pod = %target.pod_name,
        target_quic_addr = %target.quic_addr,
        server_name = %server_name,
        "accepted downstream WebTransport session"
    );
    let downstream_connection = connection.clone();
    let downstream = match tokio::select! {
        _ = shutdown.cancelled() => {
            downstream_connection.close(0u32.into(), b"router shutdown");
            return Ok(());
        }
        downstream = DownstreamSession::from_connection(
            connection,
            config.connect_timeout,
        ) => downstream,
    } {
        Ok(DownstreamSessionOutcome::Accepted(downstream)) => downstream,
        Ok(DownstreamSessionOutcome::Rejected(rejected)) => {
            metrics.observe_webtransport_session("invalid_connect");
            rejected.wait(shutdown).await;
            return Ok(());
        }
        Ok(DownstreamSessionOutcome::Closed) => return Ok(()),
        Err(error) => {
            metrics.observe_webtransport_session("invalid_connect");
            return Err(error);
        }
    };
    let upstream = match tokio::select! {
        _ = shutdown.cancelled() => {
            downstream_connection.close(0u32.into(), b"router shutdown");
            return Ok(());
        }
        upstream = async {
            let upstream = prepare_upstream_quic(
                &target.quic_addr,
                &server_name,
                &config.upstream_client_config,
                config.connect_timeout,
                shutdown.child_token(),
            ).await?;
            open_upstream_session(
                upstream,
                &server_name,
                &downstream.request_headers,
                config.connect_timeout,
            ).await
        } => upstream,
    } {
        Ok(upstream) => upstream,
        Err(error) => {
            metrics.observe_webtransport_session("upstream_connect_error");
            let rejected = downstream
                .reject_and_hold(StatusCode::BAD_GATEWAY)
                .await
                .context("reject downstream after upstream failure")?;
            rejected.wait(shutdown).await;
            return Err(error);
        }
    };
    match tokio::select! {
        _ = shutdown.cancelled() => {
            downstream_connection.close(0u32.into(), b"router shutdown");
            return Ok(());
        }
        result = downstream.forward(upstream, shutdown.clone(), connection_tasks) => result,
    } {
        Ok(None) => {
            metrics.observe_webtransport_session("completed");
            Ok(())
        }
        Ok(Some(rejected)) => {
            metrics.observe_webtransport_session("upstream_rejected");
            rejected.wait(shutdown).await;
            Ok(())
        }
        Err(error) => {
            metrics.observe_webtransport_session("relay_error");
            Err(error)
        }
    }
}

fn owned_target_for_sni(
    sni: Option<&str>,
    targets: &TargetSnapshot,
    hostname_matcher: Option<&HostnameMatcher>,
) -> Result<(PodTarget, String), SniRouteRejection> {
    ready_target_for_sni(sni, targets, hostname_matcher)
        .map(|(target, server_name)| (target.clone(), server_name.to_owned()))
}

struct DownstreamSession {
    connection: quinn::Connection,
    request_headers: HeaderMap,
    h3_connection: DownstreamH3Connection,
    connect_stream: DownstreamConnectStream,
}

enum DownstreamSessionOutcome {
    Accepted(DownstreamSession),
    Rejected(RejectedDownstreamSession),
    Closed,
}

struct RejectedDownstreamSession(
    quinn::Connection,
    DownstreamH3Connection,
    DownstreamConnectStream,
);

impl RejectedDownstreamSession {
    async fn wait(self, shutdown: CancellationToken) {
        let Self(connection, _h3_connection, _connect_stream) = self;
        tokio::select! {
            _ = shutdown.cancelled() => connection.close(0u32.into(), b"shutdown"),
            _ = connection.closed() => {}
        }
    }
}

impl DownstreamSession {
    async fn forward(
        self,
        upstream: UpstreamSession,
        shutdown: CancellationToken,
        connection_tasks: TaskTracker,
    ) -> Result<Option<RejectedDownstreamSession>> {
        if upstream.response_status.is_success() {
            self.accept_and_bridge(upstream, shutdown, connection_tasks)
                .await
                .map(|()| None)
        } else {
            self.reject_and_hold(upstream.response_status)
                .await
                .map(Some)
        }
    }

    async fn from_connection(
        connection: quinn::Connection,
        session_timeout: Duration,
    ) -> Result<DownstreamSessionOutcome> {
        let mut h3_connection =
            tokio::time::timeout(session_timeout, build_downstream_h3(connection.clone()))
                .await
                .context("timed out creating downstream WebTransport H3 session")??;
        let Some((request, mut connect_stream)) = tokio::time::timeout(
            session_timeout,
            accept_downstream_connect(&mut h3_connection),
        )
        .await
        .context("timed out waiting for downstream WebTransport CONNECT")??
        else {
            return Ok(DownstreamSessionOutcome::Closed);
        };
        if let Err(error) = validate_webtransport_connect(&request) {
            send_downstream_response(&mut connect_stream, StatusCode::BAD_REQUEST).await?;
            warn!(%error, "rejecting invalid downstream WebTransport CONNECT");
            return Ok(DownstreamSessionOutcome::Rejected(
                RejectedDownstreamSession(connection, h3_connection, connect_stream),
            ));
        }
        Ok(DownstreamSessionOutcome::Accepted(Self {
            connection,
            request_headers: request.into_parts().0.headers,
            h3_connection,
            connect_stream,
        }))
    }

    async fn reject_and_hold(mut self, status: StatusCode) -> Result<RejectedDownstreamSession> {
        send_downstream_response(&mut self.connect_stream, status).await?;
        Ok(RejectedDownstreamSession(
            self.connection,
            self.h3_connection,
            self.connect_stream,
        ))
    }

    async fn accept_and_bridge(
        mut self,
        upstream: UpstreamSession,
        shutdown: CancellationToken,
        connection_tasks: TaskTracker,
    ) -> Result<()> {
        let downstream_session_id = self.connect_stream.id().into_inner();
        send_downstream_response(&mut self.connect_stream, StatusCode::OK).await?;
        // Keep the accepted CONNECT and H3 session handles alive while WebTransport streams bridge.
        let _downstream_h3 = self.h3_connection;
        let _downstream_connect = self.connect_stream;
        let _upstream_endpoint = upstream.quic.endpoint;
        let _upstream_h3 = upstream.h3_connection;
        let _upstream_connect = upstream.connect_stream;
        bridge_upstream_webtransport_streams(
            self.connection,
            upstream.quic.connection,
            upstream.session_id,
            downstream_session_id,
            shutdown,
            connection_tasks,
        )
        .await;
        Ok(())
    }
}

async fn build_downstream_h3(connection: quinn::Connection) -> Result<DownstreamH3Connection> {
    h3::server::builder()
        .enable_webtransport(true)
        .enable_extended_connect(true)
        .enable_datagram(true)
        .max_webtransport_sessions(1)
        .build(h3_quinn::Connection::new(connection))
        .await
        .map_err(|error| anyhow!("create downstream h3 server: {error:?}"))
}

async fn accept_downstream_connect(
    connection: &mut DownstreamH3Connection,
) -> Result<Option<(Request<()>, DownstreamConnectStream)>> {
    let Some(resolver) = connection
        .accept()
        .await
        .map_err(|error| anyhow!("accept downstream WebTransport CONNECT: {error:?}"))?
    else {
        return Ok(None);
    };
    Ok(Some(resolver.resolve_request().await.map_err(|error| {
        anyhow!("resolve downstream WebTransport CONNECT: {error:?}")
    })?))
}

async fn send_downstream_response(
    connect_stream: &mut DownstreamConnectStream,
    status: StatusCode,
) -> Result<()> {
    let outcome = if status.is_success() {
        "WebTransport"
    } else {
        "rejection"
    };
    let response = Response::builder()
        .status(status)
        .body(())
        .with_context(|| format!("build downstream {outcome} response"))?;
    connect_stream
        .send_response(response)
        .await
        .map_err(|error| anyhow!("send downstream {outcome} response: {error:?}"))?;
    if !status.is_success() {
        connect_stream
            .finish()
            .await
            .map_err(|error| anyhow!("finish downstream rejection response: {error:?}"))?;
    }
    Ok(())
}

struct UpstreamSession {
    quic: UpstreamQuic,
    h3_connection: UpstreamH3Connection,
    connect_stream: UpstreamConnectStream,
    response_status: StatusCode,
    session_id: u64,
}

struct UpstreamQuic {
    endpoint: Endpoint,
    connection: quinn::Connection,
}

async fn prepare_upstream_quic(
    upstream_addr: &str,
    server_name: &str,
    client_config: &ClientConfig,
    connect_timeout: Duration,
    shutdown: CancellationToken,
) -> Result<UpstreamQuic> {
    let candidates = tokio::select! {
        _ = shutdown.cancelled() => return Err(anyhow!("WebTransport upstream resolution cancelled")),
        candidates = resolve_upstream_addrs(upstream_addr) => candidates?,
    };
    connect_first_upstream_candidate(&candidates, |dial_target| {
        let client_config = client_config.clone();
        let shutdown = shutdown.child_token();
        async move {
            let mut endpoint = Endpoint::client(stargate_tls::quic_client_bind_addr(dial_target))
                .context("bind upstream QUIC client")?;
            endpoint.set_default_client_config(client_config);
            let connecting = endpoint
                .connect(dial_target, server_name)
                .context("start upstream QUIC connection")?;
            let connection = tokio::select! {
                _ = shutdown.cancelled() => Err(anyhow!("WebTransport upstream connection cancelled")),
                result = tokio::time::timeout(connect_timeout, connecting) => result
                    .context("WebTransport upstream QUIC connection timed out")?
                    .context("establish upstream QUIC connection"),
            }?;
            Ok(UpstreamQuic {
                endpoint,
                connection,
            })
        }
    })
    .await
    .map(|(_, upstream)| upstream)
    .with_context(|| format!("connect upstream QUIC to {upstream_addr}"))
}

async fn open_upstream_session(
    upstream: UpstreamQuic,
    server_name: &str,
    headers: &HeaderMap,
    connect_timeout: Duration,
) -> Result<UpstreamSession> {
    let (h3_connection, mut send_request) =
        build_upstream_h3(&upstream.connection, connect_timeout).await?;
    let (connect_stream, response_status, session_id) =
        tokio::time::timeout(connect_timeout, async {
            let mut connect_stream = send_request
                .send_request(build_upstream_webtransport_request(server_name, headers)?)
                .await
                .map_err(|error| anyhow!("send upstream WebTransport CONNECT: {error:?}"))?;
            let session_id = connect_stream.id().into_inner();
            connect_stream
                .finish()
                .await
                .map_err(|error| anyhow!("finish upstream WebTransport CONNECT: {error:?}"))?;
            let response_status = connect_stream
                .recv_response()
                .await
                .map_err(|error| anyhow!("read upstream WebTransport CONNECT response: {error:?}"))?
                .status();
            Result::<_, anyhow::Error>::Ok((connect_stream, response_status, session_id))
        })
        .await
        .context("timed out opening upstream WebTransport CONNECT")??;

    Ok(UpstreamSession {
        quic: upstream,
        h3_connection,
        connect_stream,
        response_status,
        session_id,
    })
}

async fn build_upstream_h3(
    connection: &quinn::Connection,
    connect_timeout: Duration,
) -> Result<(UpstreamH3Connection, UpstreamSendRequest)> {
    tokio::time::timeout(connect_timeout, async {
        h3::client::builder()
            .enable_extended_connect(true)
            .enable_datagram(true)
            .build(h3_quinn::Connection::new(connection.clone()))
            .await
            .map_err(|error| anyhow!("create upstream h3 client: {error:?}"))
    })
    .await
    .context("timed out creating upstream WebTransport H3 session")?
}

fn build_upstream_webtransport_request(
    server_name: &str,
    headers: &HeaderMap,
) -> Result<Request<()>> {
    let mut request = Request::builder()
        .method(Method::CONNECT)
        .uri(format!("https://{server_name}{WEBTRANSPORT_TUNNEL_PATH}"))
        .body(())
        .context("build upstream WebTransport CONNECT")?;
    *request.headers_mut() = headers.clone();
    request
        .extensions_mut()
        .insert(h3::ext::Protocol::WEB_TRANSPORT);
    Ok(request)
}

async fn bridge_upstream_webtransport_streams(
    downstream_connection: quinn::Connection,
    upstream_connection: quinn::Connection,
    upstream_session_id: u64,
    downstream_session_id: u64,
    shutdown: CancellationToken,
    connection_tasks: TaskTracker,
) {
    loop {
        tokio::select! {
            _ = shutdown.cancelled() => {
                downstream_connection.close(0u32.into(), b"shutdown");
                upstream_connection.close(0u32.into(), b"shutdown");
                break;
            }
            _ = downstream_connection.closed() => {
                upstream_connection.close(0u32.into(), b"downstream webtransport closed");
                break;
            }
            stream = upstream_connection.accept_bi() => {
                let Ok((upstream_send, upstream_recv)) = stream else {
                    downstream_connection.close(0u32.into(), b"upstream webtransport closed");
                    break;
                };
                let downstream_connection = downstream_connection.clone();
                let stream_shutdown = shutdown.child_token();
                connection_tasks.spawn(async move {
                    if let Err(error) = bridge_upstream_webtransport_stream(
                        downstream_connection,
                        upstream_send,
                        upstream_recv,
                        upstream_session_id,
                        downstream_session_id,
                        stream_shutdown,
                    )
                    .await
                    {
                        warn!(error = %error, "WebTransport router stream bridge failed");
                    }
                });
            }
        }
    }
}

async fn bridge_upstream_webtransport_stream(
    downstream_connection: quinn::Connection,
    mut upstream_send: quinn::SendStream,
    mut upstream_recv: quinn::RecvStream,
    upstream_session_id: u64,
    downstream_session_id: u64,
    shutdown: CancellationToken,
) -> Result<()> {
    tokio::select! {
        _ = shutdown.cancelled() => return Err(anyhow!("WebTransport stream bridge cancelled")),
        result = tokio::time::timeout(
            WEBTRANSPORT_STREAM_HEADER_TIMEOUT,
            stargate_protocol::read_webtransport_bidi_header(&mut upstream_recv),
        ) => result
            .map_err(|_| anyhow!("timed out waiting for upstream WebTransport stream header"))
            .and_then(|result| result.context("read upstream WebTransport stream header"))
            .and_then(|stream_session_id| {
                ensure!(
                    stream_session_id == upstream_session_id,
                    "upstream WebTransport session id mismatch: got {stream_session_id}, expected {upstream_session_id}"
                );
                Ok(())
            }),
    }
    .inspect_err(|_| {
        reset_webtransport_stream(&mut upstream_send, &mut upstream_recv);
    })?;

    let (mut downstream_send, downstream_recv) = tokio::select! {
        _ = shutdown.cancelled() => return Err(anyhow!("WebTransport stream bridge cancelled")),
        stream = downstream_connection.open_bi() => stream.context("open downstream WebTransport stream")?,
    };
    stargate_protocol::write_webtransport_bidi_header(&mut downstream_send, downstream_session_id)
        .await
        .context("write downstream WebTransport stream header")?;

    tokio::select! {
        _ = shutdown.cancelled() => Err(anyhow!("WebTransport stream bridge cancelled")),
        result = async {
            let (downstream_result, upstream_result) = tokio::join!(
                copy_stream(upstream_recv, downstream_send),
                copy_stream(downstream_recv, upstream_send),
            );
            downstream_result.context("copy upstream to downstream")?;
            upstream_result.context("copy downstream to upstream")?;
            Ok(())
        } => result,
    }
}

fn reset_webtransport_stream(
    quinn_send: &mut quinn::SendStream,
    quinn_recv: &mut quinn::RecvStream,
) {
    let _ = quinn_send.reset(0u32.into());
    let _ = quinn_recv.stop(0u32.into());
}

async fn copy_stream(mut recv: quinn::RecvStream, mut send: quinn::SendStream) -> Result<()> {
    while let Some(chunk) = recv
        .read_chunk(usize::MAX, true)
        .await
        .context("read QUIC stream chunk")?
    {
        send.write_all(&chunk.bytes)
            .await
            .context("write QUIC stream chunk")?;
    }
    send.finish().context("finish QUIC send stream")?;
    Ok(())
}

fn validate_webtransport_connect<B>(request: &Request<B>) -> Result<()> {
    ensure!(
        request.method() == Method::CONNECT
            && request.uri().path() == WEBTRANSPORT_TUNNEL_PATH
            && request.extensions().get::<h3::ext::Protocol>()
                == Some(&h3::ext::Protocol::WEB_TRANSPORT),
        "invalid downstream WebTransport CONNECT"
    );
    Ok(())
}

fn downstream_server_name(connection: &quinn::Connection) -> Option<String> {
    connection
        .handshake_data()
        .and_then(|data| data.downcast::<quinn::crypto::rustls::HandshakeData>().ok())
        .and_then(|data| data.server_name)
}

fn build_webtransport_server_config(
    identity: &ServerTlsIdentity,
    relay_config: RelayEndpointConfig,
) -> Result<ServerConfig> {
    build_router_server_config(
        identity,
        TunnelTransportProtocol::WebTransport.alpn_protocols(),
        relay_config,
    )
}

fn client_config(cert_pem: Option<&[u8]>, quic_insecure: bool) -> Result<ClientConfig> {
    build_upstream_client_config(
        cert_pem,
        quic_insecure,
        TunnelTransportProtocol::WebTransport.alpn_protocols(),
        "upstream TLS cert required when verification is enabled",
    )
}

#[cfg(test)]
mod tests {
    use http::HeaderValue;

    use super::*;

    const TEST_TIMEOUT: Duration = Duration::from_secs(1);
    const UPSTREAM_SESSION_ID: u64 = 41;
    const DOWNSTREAM_SESSION_ID: u64 = 77;

    fn server_config(identity: &ServerTlsIdentity) -> Result<ServerConfig> {
        build_webtransport_server_config(identity, RelayEndpointConfig::default())
    }

    fn test_router_config() -> WebTransportRouterConfig {
        WebTransportRouterConfig {
            listen_addr: "127.0.0.1:0".parse().expect("valid listener address"),
            advertised_hostname_template: "{pod_name}.stargate.external".to_string(),
            target_namespace: String::new(),
            connect_timeout: TEST_TIMEOUT,
            relay_max_idle_timeout: Duration::from_secs(30),
            relay_keep_alive_interval: Some(Duration::from_secs(5)),
            tls_cert_pem: None,
            tls_key_pem: None,
            upstream_tls_cert_pem: None,
            quic_insecure: true,
        }
    }

    fn snapshot_with_target(pod_name: &str, quic_addr: &str) -> TargetSnapshot {
        TargetSnapshot::initialized([PodTarget {
            pod_name: pod_name.to_string(),
            grpc_addr: "127.0.0.1:50071".to_string(),
            quic_addr: quic_addr.to_string(),
        }])
    }

    async fn accept_connect(
        connection: quinn::Connection,
    ) -> Result<(DownstreamH3Connection, Request<()>, DownstreamConnectStream)> {
        let mut h3_connection = build_downstream_h3(connection).await?;
        let (request, stream) = accept_downstream_connect(&mut h3_connection)
            .await?
            .expect("test upstream should receive CONNECT");
        Ok((h3_connection, request, stream))
    }

    async fn response_status(stream: &mut UpstreamConnectStream) -> StatusCode {
        stream
            .recv_response()
            .await
            .expect("WebTransport response should arrive")
            .status()
    }

    fn accepted_session(outcome: DownstreamSessionOutcome) -> DownstreamSession {
        let DownstreamSessionOutcome::Accepted(session) = outcome else {
            panic!("valid CONNECT should produce a session");
        };
        session
    }

    fn assert_error_contains(error: &anyhow::Error, expected: &str) {
        assert!(
            error.to_string().contains(expected),
            "unexpected error: {error:#}"
        );
    }

    async fn send_connect(
        mut send_request: UpstreamSendRequest,
        request: Request<()>,
    ) -> Result<UpstreamConnectStream> {
        let mut stream = send_request
            .send_request(request)
            .await
            .map_err(|error| anyhow!("send test WebTransport CONNECT: {error:?}"))?;
        stream
            .finish()
            .await
            .map_err(|error| anyhow!("finish test WebTransport CONNECT: {error:?}"))?;
        Ok(stream)
    }

    struct ConnectedDownstream {
        outcome: DownstreamSessionOutcome,
        client_stream: UpstreamConnectStream,
        _owners: ([Endpoint; 2], UpstreamH3Connection),
    }

    struct QuicPair {
        server_endpoint: Endpoint,
        client_endpoint: Endpoint,
        server_connection: quinn::Connection,
        client_connection: quinn::Connection,
    }

    async fn connect_downstream(request: Request<()>) -> Result<ConnectedDownstream> {
        let pair = connect_quic_pair().await?;
        let server_task = tokio::spawn(DownstreamSession::from_connection(
            pair.server_connection,
            TEST_TIMEOUT,
        ));
        let (client_h3, send_request) =
            build_upstream_h3(&pair.client_connection, TEST_TIMEOUT).await?;
        let client_stream = send_connect(send_request, request).await?;
        let outcome = server_task
            .await
            .expect("downstream session task should not panic")?;
        Ok(ConnectedDownstream {
            outcome,
            client_stream,
            _owners: ([pair.server_endpoint, pair.client_endpoint], client_h3),
        })
    }

    async fn open_webtransport_stream(
        connection: &quinn::Connection,
        session_id: u64,
    ) -> Result<(quinn::SendStream, quinn::RecvStream)> {
        let (mut send, recv) = connection.open_bi().await.context("open upstream stream")?;
        stargate_protocol::write_webtransport_bidi_header(&mut send, session_id)
            .await
            .context("write upstream WebTransport header")?;
        Ok((send, recv))
    }

    #[test]
    fn route_for_sni_snapshots_the_exact_ready_target() {
        let matcher = HostnameMatcher::new("{pod_name}.stargate.external", "");
        let snapshot = snapshot_with_target("stargate-1", "10.0.0.1:50072");
        let (target, server_name) = owned_target_for_sni(
            Some("stargate-1.stargate.external"),
            &snapshot,
            matcher.as_ref(),
        )
        .expect("matching SNI should select its ready target");
        assert_eq!(target.pod_name, "stargate-1");
        assert_eq!(server_name, "stargate-1.stargate.external");
        assert!(matches!(
            owned_target_for_sni(
                Some("stargate-1.stargate.external"),
                &TargetSnapshot::initialized([]),
                matcher.as_ref(),
            ),
            Err(SniRouteRejection::TargetUnavailable)
        ));
        assert_eq!(
            target.quic_addr, "10.0.0.1:50072",
            "the session-owned target remains stable after later snapshot churn"
        );
    }

    #[tokio::test]
    async fn webtransport_server_config_negotiates_h3_alpn() -> Result<()> {
        let pair = connect_quic_pair().await?;
        pair.client_connection.close(0u32.into(), b"test complete");
        pair.server_connection.closed().await;
        Ok(())
    }

    #[tokio::test]
    async fn webtransport_router_stops_and_joins_tracked_sessions_on_shutdown() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (_targets_tx, targets_rx) = watch::channel(TargetSnapshot::default());
        let metrics = Arc::new(RouterMetrics::new().expect("metrics should initialize"));
        let shutdown = CancellationToken::new();
        let connection_tasks = TaskTracker::new();
        shutdown.cancel();

        serve_webtransport_router(
            test_router_config(),
            targets_rx,
            metrics,
            shutdown,
            connection_tasks.clone(),
        )
        .await
        .expect("router should observe shutdown");

        connection_tasks.close();
        connection_tasks.wait().await;
    }

    #[tokio::test]
    async fn webtransport_router_fails_before_serving_when_udp_listener_is_occupied() -> Result<()>
    {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let occupied = std::net::UdpSocket::bind("127.0.0.1:0")?;
        let mut config = test_router_config();
        config.listen_addr = occupied.local_addr()?;
        let (_targets_tx, targets_rx) = watch::channel(TargetSnapshot::default());
        let error = serve_webtransport_router(
            config,
            targets_rx,
            Arc::new(RouterMetrics::new()?),
            CancellationToken::new(),
            TaskTracker::new(),
        )
        .await
        .expect_err("an occupied UDP listener must stop WebTransport startup");

        assert_error_contains(&error, "bind WebTransport router QUIC listener");
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn webtransport_router_shutdown_cancels_admitted_session_and_drains_tracker() -> Result<()>
    {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let upstream_endpoint = Endpoint::server(
            server_config(&ServerTlsIdentity::SelfSigned)?,
            "127.0.0.1:0".parse()?,
        )?;
        let upstream_addr = upstream_endpoint.local_addr()?;
        let (upstream_connect_tx, upstream_connect_rx) = tokio::sync::oneshot::channel();
        let (release_upstream_tx, release_upstream_rx) = tokio::sync::oneshot::channel();
        let upstream_task = tokio::spawn(async move {
            let incoming = upstream_endpoint
                .accept()
                .await
                .expect("upstream fixture should accept the router connection");
            let connection = incoming
                .await
                .expect("router should complete the upstream QUIC handshake");
            let (h3_connection, request, stream) = accept_connect(connection).await?;
            assert_eq!(request.method(), Method::CONNECT);
            assert_eq!(request.uri().path(), WEBTRANSPORT_TUNNEL_PATH);
            upstream_connect_tx
                .send(())
                .map_err(|_| anyhow!("test stopped waiting for the upstream CONNECT"))?;
            let _keep_upstream_session_open = (&h3_connection, &stream);
            let _ = release_upstream_rx.await;
            Result::<()>::Ok(())
        });

        let mut config = test_router_config();
        config.connect_timeout = Duration::from_secs(30);
        let upstream_target = upstream_addr.to_string();
        let (_targets_tx, targets_rx) =
            watch::channel(snapshot_with_target("stargate-1", &upstream_target));
        let metrics = Arc::new(RouterMetrics::new()?);
        let shutdown = CancellationToken::new();
        let connection_tasks = TaskTracker::new();
        let runtime = WebTransportRouterRuntime::bind(config)?;
        let router_addr = runtime.bound_addr;
        let router_task = tokio::spawn(runtime.serve(
            targets_rx,
            metrics.clone(),
            shutdown.clone(),
            connection_tasks.clone(),
        ));

        let mut client = Endpoint::client("127.0.0.1:0".parse()?)?;
        client.set_default_client_config(client_config(None, true)?);
        let client_connection = client
            .connect(router_addr, "stargate-1.stargate.external")?
            .await?;
        let (_client_h3, client_request) =
            build_upstream_h3(&client_connection, TEST_TIMEOUT).await?;
        let _client_stream = send_connect(
            client_request,
            build_upstream_webtransport_request("stargate-1.stargate.external", &HeaderMap::new())?,
        )
        .await?;

        tokio::time::timeout(TEST_TIMEOUT, upstream_connect_rx)
            .await
            .context("router should open the upstream CONNECT before shutdown")?
            .map_err(|_| anyhow!("upstream fixture stopped before receiving the CONNECT"))?;
        assert!(
            metrics.gather()?.contains(
                r#"stargate_k8s_router_webtransport_sessions_total{outcome="accepted"} 1"#,
            )
        );

        shutdown.cancel();
        tokio::time::timeout(TEST_TIMEOUT, router_task)
            .await
            .context("router root should stop after cancellation")???;
        connection_tasks.close();
        tokio::time::timeout(TEST_TIMEOUT, connection_tasks.wait())
            .await
            .context("admitted session task should drain after cancellation")?;
        client_connection.close(0u32.into(), b"test complete");
        let _ = release_upstream_tx.send(());
        upstream_task
            .await
            .expect("upstream fixture should not panic")?;
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn removed_target_rejects_a_new_webtransport_session_before_upstream_dial() -> Result<()>
    {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (targets_tx, targets_rx) =
            watch::channel(snapshot_with_target("stargate-1", "127.0.0.1:50072"));
        targets_tx.send_replace(TargetSnapshot::initialized([]));
        let metrics = Arc::new(RouterMetrics::new()?);
        let shutdown = CancellationToken::new();
        let connection_tasks = TaskTracker::new();
        let runtime = WebTransportRouterRuntime::bind(test_router_config())?;
        let router_addr = runtime.bound_addr;
        let router_task = tokio::spawn(runtime.serve(
            targets_rx,
            metrics.clone(),
            shutdown.clone(),
            connection_tasks.clone(),
        ));

        let mut client = Endpoint::client("127.0.0.1:0".parse()?)?;
        client.set_default_client_config(client_config(None, true)?);
        let client_connection = client
            .connect(router_addr, "stargate-1.stargate.external")?
            .await?;
        tokio::time::timeout(TEST_TIMEOUT, client_connection.closed())
            .await
            .context("a new session for a removed target should be closed")?;

        assert!(metrics.gather()?.contains(
            r#"stargate_k8s_router_webtransport_sessions_total{outcome="target_unavailable"} 1"#
        ));
        shutdown.cancel();
        tokio::time::timeout(TEST_TIMEOUT, router_task)
            .await
            .context("router should stop after cancellation")???;
        connection_tasks.close();
        connection_tasks.wait().await;
        Ok(())
    }

    #[test]
    fn upstream_connect_request_preserves_downstream_headers() {
        let mut headers = HeaderMap::new();
        headers.insert("x-request-id", HeaderValue::from_static("request-1"));
        headers.append("x-routing-key", HeaderValue::from_static("alpha"));
        headers.append("x-routing-key", HeaderValue::from_static("beta"));

        let request = build_upstream_webtransport_request("stargate-1.example", &headers).unwrap();

        assert_eq!(request.method(), Method::CONNECT);
        assert_eq!(
            request.uri().to_string(),
            format!("https://stargate-1.example{WEBTRANSPORT_TUNNEL_PATH}")
        );
        assert!(
            request
                .extensions()
                .get::<h3::ext::Protocol>()
                .is_some_and(|protocol| *protocol == h3::ext::Protocol::WEB_TRANSPORT)
        );
        assert_eq!(
            request.headers().get("x-request-id").unwrap(),
            &HeaderValue::from_static("request-1")
        );
        let routing_values: Vec<_> = request
            .headers()
            .get_all("x-routing-key")
            .iter()
            .map(|value| value.to_str().unwrap())
            .collect();
        assert_eq!(routing_values, ["alpha", "beta"]);
    }

    #[test]
    fn upstream_client_config_requires_trust_when_verification_is_enabled() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        assert!(client_config(None, false).is_err());

        let (cert_pem, _) = stargate_tls::generate_self_signed_cert().unwrap();
        assert!(client_config(Some(&cert_pem), false).is_ok());
        assert!(client_config(None, true).is_ok());
    }

    #[tokio::test]
    async fn prepare_upstream_quic_resolves_and_connects_to_a_local_server() -> Result<()> {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let server_endpoint = Endpoint::server(
            server_config(&stargate_tls::ServerTlsIdentity::SelfSigned)?,
            "127.0.0.1:0".parse()?,
        )?;
        let server_addr = server_endpoint.local_addr()?;
        let server_task = tokio::spawn(async move {
            let incoming = server_endpoint
                .accept()
                .await
                .expect("server should accept the proxy's QUIC connection");
            let connection = incoming.await?;
            tokio::time::timeout(TEST_TIMEOUT, connection.closed())
                .await
                .context("closing the proxy connection should reach the server")?;
            Result::<()>::Ok(())
        });

        let upstream = prepare_upstream_quic(
            &server_addr.to_string(),
            "stargate",
            &client_config(None, true)?,
            TEST_TIMEOUT,
            CancellationToken::new(),
        )
        .await?;

        assert_eq!(upstream.connection.remote_address(), server_addr);
        upstream.connection.close(0u32.into(), b"test complete");
        server_task
            .await
            .expect("local QUIC server task should not panic")?;
        Ok(())
    }

    #[test]
    fn downstream_connect_validation_rejects_non_webtransport_requests() {
        let request = Request::builder()
            .method(Method::GET)
            .uri(WEBTRANSPORT_TUNNEL_PATH)
            .body(())
            .unwrap();
        assert!(validate_webtransport_connect(&request).is_err());

        let mut request = Request::builder()
            .method(Method::CONNECT)
            .uri("/not-webtransport")
            .body(())
            .unwrap();
        request
            .extensions_mut()
            .insert(h3::ext::Protocol::WEB_TRANSPORT);
        assert!(validate_webtransport_connect(&request).is_err());
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn invalid_downstream_connect_receives_bad_request() -> Result<()> {
        let mut request = Request::builder()
            .method(Method::CONNECT)
            .uri("https://stargate-1.example/not-webtransport")
            .body(())?;
        request
            .extensions_mut()
            .insert(h3::ext::Protocol::WEB_TRANSPORT);
        let mut connected = connect_downstream(request).await?;
        {
            let rejected = match connected.outcome {
                DownstreamSessionOutcome::Rejected(rejected) => rejected,
                DownstreamSessionOutcome::Accepted(_) | DownstreamSessionOutcome::Closed => {
                    panic!("invalid request should be rejected")
                }
            };
            assert_eq!(
                response_status(&mut connected.client_stream).await,
                StatusCode::BAD_REQUEST
            );
            let _keep_rejection_alive_until_response = &rejected;
        }
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn downstream_session_accepts_h3_connect_and_can_reject() -> Result<()> {
        let mut headers = HeaderMap::new();
        headers.insert("x-request-id", HeaderValue::from_static("request-1"));
        let request = build_upstream_webtransport_request("stargate-1.example", &headers)?;

        let mut connected = connect_downstream(request).await?;
        let mut session = accepted_session(connected.outcome);

        assert_eq!(
            session.request_headers.get("x-request-id").unwrap(),
            &HeaderValue::from_static("request-1")
        );

        send_downstream_response(&mut session.connect_stream, StatusCode::BAD_GATEWAY).await?;
        assert_eq!(
            response_status(&mut connected.client_stream).await,
            StatusCode::BAD_GATEWAY
        );
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn upstream_session_open_sends_webtransport_connect_and_reads_status() -> Result<()> {
        let pair = connect_quic_pair().await?;
        let (release_server_tx, release_server_rx) = tokio::sync::oneshot::channel();
        let server_task = tokio::spawn(async move {
            let (_server_h3, request, mut stream) = accept_connect(pair.server_connection).await?;
            assert_eq!(request.method(), Method::CONNECT);
            assert_eq!(
                request.uri().path(),
                WEBTRANSPORT_TUNNEL_PATH,
                "upstream CONNECT should target the tunnel path"
            );
            assert_eq!(
                request.headers().get("x-routing-key").unwrap(),
                &HeaderValue::from_static("tenant-a")
            );
            send_downstream_response(&mut stream, StatusCode::BAD_GATEWAY).await?;
            let _ = release_server_rx.await;
            Result::<()>::Ok(())
        });
        let mut headers = HeaderMap::new();
        headers.insert("x-routing-key", HeaderValue::from_static("tenant-a"));

        let session = open_upstream_session(
            UpstreamQuic {
                endpoint: pair.client_endpoint,
                connection: pair.client_connection,
            },
            "stargate-1.example",
            &headers,
            TEST_TIMEOUT,
        )
        .await?;

        assert_eq!(session.response_status, StatusCode::BAD_GATEWAY);
        let _ = release_server_tx.send(());
        server_task
            .await
            .expect("upstream server task should not panic")?;
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn upstream_non_success_status_is_propagated_without_a_data_bridge() -> Result<()> {
        let mut downstream = connect_downstream(build_upstream_webtransport_request(
            "stargate-1.example",
            &HeaderMap::new(),
        )?)
        .await?;
        let downstream_session = accepted_session(downstream.outcome);

        let pair = connect_quic_pair().await?;
        let (release_upstream_tx, release_upstream_rx) = tokio::sync::oneshot::channel();
        let upstream_server = tokio::spawn(async move {
            let (h3_connection, _request, mut stream) =
                accept_connect(pair.server_connection).await?;
            send_downstream_response(&mut stream, StatusCode::TOO_MANY_REQUESTS).await?;
            let _ = release_upstream_rx.await;
            let _ = (&h3_connection, &stream);
            Result::<()>::Ok(())
        });
        let upstream = open_upstream_session(
            UpstreamQuic {
                endpoint: pair.client_endpoint,
                connection: pair.client_connection,
            },
            "stargate-1.example",
            &HeaderMap::new(),
            TEST_TIMEOUT,
        )
        .await?;

        {
            let rejected = downstream_session
                .forward(upstream, CancellationToken::new(), TaskTracker::new())
                .await?
                .expect("a non-success upstream CONNECT must not create a data bridge");
            assert_eq!(
                response_status(&mut downstream.client_stream).await,
                StatusCode::TOO_MANY_REQUESTS
            );
            let _keep_rejection_alive_until_response = &rejected;
        }
        let _ = release_upstream_tx.send(());
        upstream_server.await??;
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn stalled_upstream_stream_header_does_not_block_later_streams() -> Result<()> {
        let fixture = connect_bridge_fixture().await?;

        let bridge_task = fixture.spawn_bridge(CancellationToken::new(), TaskTracker::new());
        let (_stalled_send, _stalled_recv) = fixture.upstream_server_connection.open_bi().await?;

        let (mut upstream_send, _upstream_recv) =
            open_webtransport_stream(&fixture.upstream_server_connection, UPSTREAM_SESSION_ID)
                .await?;
        upstream_send.write_all(b"later stream").await?;
        upstream_send.finish()?;

        let (mut downstream_send, mut downstream_recv) = fixture.accept_downstream_stream().await?;
        downstream_send.finish()?;
        let payload = downstream_recv.read_to_end(1024).await?;
        assert_eq!(payload, b"later stream");

        bridge_task.abort();
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn shutdown_closes_active_webtransport_stream_tasks_and_drains_tracker() -> Result<()> {
        let fixture = connect_bridge_fixture().await?;
        let shutdown = CancellationToken::new();
        let connection_tasks = TaskTracker::new();
        let bridge_task = fixture.spawn_bridge(shutdown.clone(), connection_tasks.clone());

        let (_upstream_send, _upstream_recv) =
            open_webtransport_stream(&fixture.upstream_server_connection, UPSTREAM_SESSION_ID)
                .await?;
        let (_downstream_send, _downstream_recv) = fixture.accept_downstream_stream().await?;

        shutdown.cancel();
        tokio::time::timeout(TEST_TIMEOUT, fixture.upstream_server_connection.closed())
            .await
            .context("router shutdown should close the upstream WebTransport connection")?;
        tokio::time::timeout(TEST_TIMEOUT, bridge_task)
            .await
            .context("stream accept loop should stop after shutdown")??;
        connection_tasks.close();
        tokio::time::timeout(TEST_TIMEOUT, connection_tasks.wait())
            .await
            .context("active stream bridge task should drain after shutdown")?;
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn upstream_stream_header_error_resets_stream() -> Result<()> {
        let error = bridge_header_error(None).await?;

        assert_error_contains(&error, "read upstream WebTransport stream header");
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn upstream_stream_session_mismatch_resets_stream() -> Result<()> {
        let error = bridge_header_error(Some(99)).await?;

        assert_error_contains(&error, "upstream WebTransport session id mismatch");
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn matching_upstream_stream_bridges_bytes_in_both_directions() -> Result<()> {
        let fixture = connect_bridge_fixture().await?;
        let (mut server_send, mut server_recv) =
            open_webtransport_stream(&fixture.upstream_server_connection, UPSTREAM_SESSION_ID)
                .await?;
        server_send.write_all(b"upstream payload").await?;
        server_send.finish()?;
        let (proxy_send, proxy_recv) = fixture.accept_upstream_stream().await?;

        let bridge_task = tokio::spawn(bridge_upstream_webtransport_stream(
            fixture.downstream_proxy_connection.clone(),
            proxy_send,
            proxy_recv,
            UPSTREAM_SESSION_ID,
            DOWNSTREAM_SESSION_ID,
            CancellationToken::new(),
        ));
        let (mut downstream_send, mut downstream_recv) = fixture.accept_downstream_stream().await?;
        let downstream_payload = downstream_recv.read_to_end(1024).await?;
        assert_eq!(downstream_payload, b"upstream payload");

        downstream_send.write_all(b"downstream payload").await?;
        downstream_send.finish()?;
        let upstream_payload = server_recv.read_to_end(1024).await?;
        assert_eq!(upstream_payload, b"downstream payload");
        bridge_task
            .await
            .context("bridge task should not panic")??;
        Ok(())
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn downstream_close_closes_upstream_session() -> Result<()> {
        let fixture = connect_bridge_fixture().await?;

        let bridge_task = fixture.spawn_bridge(CancellationToken::new(), TaskTracker::new());

        fixture
            .downstream_client_connection
            .close(0u32.into(), b"client restart");
        tokio::time::timeout(TEST_TIMEOUT, fixture.upstream_server_connection.closed())
            .await
            .context("upstream session should close after downstream disconnects")?;

        bridge_task.abort();
        Ok(())
    }

    struct BridgeFixture {
        _endpoints: [Endpoint; 4],
        upstream_server_connection: quinn::Connection,
        upstream_proxy_connection: quinn::Connection,
        downstream_proxy_connection: quinn::Connection,
        downstream_client_connection: quinn::Connection,
    }

    impl BridgeFixture {
        fn spawn_bridge(
            &self,
            shutdown: CancellationToken,
            connection_tasks: TaskTracker,
        ) -> tokio::task::JoinHandle<()> {
            tokio::spawn(bridge_upstream_webtransport_streams(
                self.downstream_proxy_connection.clone(),
                self.upstream_proxy_connection.clone(),
                UPSTREAM_SESSION_ID,
                DOWNSTREAM_SESSION_ID,
                shutdown,
                connection_tasks,
            ))
        }

        async fn accept_upstream_stream(&self) -> Result<(quinn::SendStream, quinn::RecvStream)> {
            self.upstream_proxy_connection
                .accept_bi()
                .await
                .context("proxy should accept upstream stream")
        }

        async fn accept_downstream_stream(&self) -> Result<(quinn::SendStream, quinn::RecvStream)> {
            let (send, mut recv) =
                tokio::time::timeout(TEST_TIMEOUT, self.downstream_client_connection.accept_bi())
                    .await
                    .context("downstream stream should open promptly")??;
            assert_eq!(
                stargate_protocol::read_webtransport_bidi_header(&mut recv).await?,
                DOWNSTREAM_SESSION_ID
            );
            Ok((send, recv))
        }
    }

    async fn bridge_header_error(session_id: Option<u64>) -> Result<anyhow::Error> {
        let fixture = connect_bridge_fixture().await?;
        let (mut server_send, _server_recv) = fixture.upstream_server_connection.open_bi().await?;
        if let Some(session_id) = session_id {
            stargate_protocol::write_webtransport_bidi_header(&mut server_send, session_id).await?;
        } else {
            server_send.finish()?;
        }
        let (proxy_send, proxy_recv) = fixture.accept_upstream_stream().await?;
        Ok(bridge_upstream_webtransport_stream(
            fixture.downstream_proxy_connection,
            proxy_send,
            proxy_recv,
            UPSTREAM_SESSION_ID,
            DOWNSTREAM_SESSION_ID,
            CancellationToken::new(),
        )
        .await
        .expect_err("invalid upstream header should fail"))
    }

    async fn connect_bridge_fixture() -> Result<BridgeFixture> {
        let upstream = connect_quic_pair().await?;
        let downstream = connect_quic_pair().await?;
        Ok(BridgeFixture {
            _endpoints: [
                upstream.server_endpoint,
                upstream.client_endpoint,
                downstream.server_endpoint,
                downstream.client_endpoint,
            ],
            upstream_server_connection: upstream.server_connection,
            upstream_proxy_connection: upstream.client_connection,
            downstream_proxy_connection: downstream.server_connection,
            downstream_client_connection: downstream.client_connection,
        })
    }

    async fn connect_quic_pair() -> Result<QuicPair> {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let server_endpoint = Endpoint::server(
            server_config(&stargate_tls::ServerTlsIdentity::SelfSigned)?,
            "127.0.0.1:0".parse()?,
        )?;
        let server_addr = server_endpoint.local_addr()?;
        let mut client_endpoint = Endpoint::client("127.0.0.1:0".parse()?)?;
        client_endpoint.set_default_client_config(client_config(None, true)?);
        let connecting = client_endpoint.connect(server_addr, "stargate")?;
        let incoming = server_endpoint
            .accept()
            .await
            .context("server should accept")?;
        let (server_connection, client_connection) = tokio::join!(incoming, connecting);

        Ok(QuicPair {
            server_endpoint,
            client_endpoint,
            server_connection: server_connection.context("server connection should complete")?,
            client_connection: client_connection.context("client connection should complete")?,
        })
    }
}

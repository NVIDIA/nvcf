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
use clap::Parser;
use http::{HeaderMap, Method, Request, Response, StatusCode};
use quinn::{ClientConfig, Endpoint, ServerConfig};
use rustls::pki_types::CertificateDer;
use stargate_protocol::TunnelTransportProtocol;
use stargate_protocol::tunnel_contract::WEBTRANSPORT_TUNNEL_PATH;
use tokio::io::{AsyncRead, AsyncWrite, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

const WEBTRANSPORT_STREAM_HEADER_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Parser, Debug)]
#[command(name = "stargate-webtransport-l7-proxy")]
struct Args {
    #[arg(long, default_value = "0.0.0.0:50072", value_name = "ADDR")]
    listen_addr: SocketAddr,
    #[arg(long, value_name = "TEMPLATE")]
    upstream_template: String,
    #[arg(long, default_value = "0.0.0.0:50071", value_name = "ADDR")]
    control_plane_listen_addr: SocketAddr,
    #[arg(long, value_name = "ADDR")]
    control_plane_upstream_addr: Option<String>,
}

#[tokio::main]
async fn main() -> Result<()> {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
    tracing_subscriber::fmt::init();

    let args = Args::parse();
    run(args).await
}

async fn run(args: Args) -> Result<()> {
    let endpoint = Endpoint::server(server_config()?, args.listen_addr)
        .context("bind l7 proxy QUIC server")?;
    let listen_addr = endpoint.local_addr().context("read l7 proxy listen addr")?;
    let shutdown = CancellationToken::new();
    let control_plane_task = if let Some(upstream_addr) = args.control_plane_upstream_addr.clone() {
        let control_plane_listener = TcpListener::bind(args.control_plane_listen_addr)
            .await
            .with_context(|| {
                format!(
                    "bind control-plane TCP proxy on {}",
                    args.control_plane_listen_addr
                )
            })?;
        let control_plane_shutdown = shutdown.clone();
        Some(tokio::spawn(run_control_plane_tcp_proxy(
            control_plane_listener,
            upstream_addr,
            control_plane_shutdown,
        )))
    } else {
        None
    };
    let shutdown_for_signal = shutdown.clone();
    tokio::spawn(async move {
        if let Err(error) = tokio::signal::ctrl_c().await {
            warn!(error = %error, "failed to wait for shutdown signal");
        }
        shutdown_for_signal.cancel();
    });
    info!(%listen_addr, "WebTransport L7 proxy listening");

    loop {
        tokio::select! {
            _ = shutdown.cancelled() => break,
            incoming = endpoint.accept() => {
                let Some(incoming) = incoming else { break };
                let upstream_template = args.upstream_template.clone();
                tokio::spawn(async move {
                    if let Err(error) = handle_connection(incoming, upstream_template).await {
                        warn!(error = %error, "WebTransport L7 proxy connection failed");
                    }
                });
            }
        }
    }

    endpoint.close(0u32.into(), b"shutdown");
    endpoint.wait_idle().await;
    shutdown.cancel();
    if let Some(task) = control_plane_task {
        task.await
            .context("join control-plane TCP proxy task")?
            .context("run control-plane TCP proxy")?;
    }
    Ok(())
}

async fn run_control_plane_tcp_proxy(
    listener: TcpListener,
    upstream_addr: String,
    shutdown: CancellationToken,
) -> Result<()> {
    let listen_addr = listener
        .local_addr()
        .context("read control-plane TCP proxy listen addr")?;
    info!(%listen_addr, %upstream_addr, "control-plane TCP proxy listening");

    loop {
        tokio::select! {
            _ = shutdown.cancelled() => break,
            accepted = listener.accept() => {
                let (downstream, peer_addr) = accepted.context("accept control-plane TCP connection")?;
                let upstream_addr = upstream_addr.clone();
                tokio::spawn(async move {
                    if let Err(error) = handle_control_plane_tcp_connection(downstream, &upstream_addr).await {
                        warn!(%peer_addr, error = %error, "control-plane TCP proxy connection failed");
                    }
                });
            }
        }
    }

    Ok(())
}

async fn handle_control_plane_tcp_connection(
    downstream: TcpStream,
    upstream_addr: &str,
) -> Result<()> {
    let resolved_target = resolve_upstream_addr(upstream_addr).await?;
    let upstream = TcpStream::connect(resolved_target)
        .await
        .with_context(|| format!("connect control-plane upstream {upstream_addr}"))?;
    let (downstream_recv, downstream_send) = downstream.into_split();
    let (upstream_recv, upstream_send) = upstream.into_split();
    let downstream_to_upstream = copy_tcp_half(downstream_recv, upstream_send);
    let upstream_to_downstream = copy_tcp_half(upstream_recv, downstream_send);
    let (downstream_result, upstream_result) =
        tokio::join!(downstream_to_upstream, upstream_to_downstream);
    downstream_result.context("copy downstream control-plane bytes upstream")?;
    upstream_result.context("copy upstream control-plane bytes downstream")?;
    Ok(())
}

async fn copy_tcp_half<R, W>(mut recv: R, mut send: W) -> Result<()>
where
    R: AsyncRead + Unpin,
    W: AsyncWrite + Unpin,
{
    tokio::io::copy(&mut recv, &mut send)
        .await
        .context("copy TCP stream")?;
    send.shutdown().await.context("shutdown TCP stream")?;
    Ok(())
}

async fn handle_connection(incoming: quinn::Incoming, upstream_template: String) -> Result<()> {
    let connection = incoming
        .await
        .context("accept downstream QUIC connection")?;
    let server_name = downstream_server_name(&connection).unwrap_or_else(|| "stargate".to_string());
    let upstream_addr = upstream_addr_for_sni(&upstream_template, &server_name)?;
    info!(%server_name, %upstream_addr, "accepted downstream WebTransport connection");

    let mut builder = h3::server::builder();
    builder
        .enable_webtransport(true)
        .enable_extended_connect(true)
        .enable_datagram(true)
        .max_webtransport_sessions(1);
    let mut downstream_h3: h3::server::Connection<h3_quinn::Connection, Bytes> = builder
        .build(h3_quinn::Connection::new(connection.clone()))
        .await
        .map_err(|error| anyhow!("create downstream h3 server: {error:?}"))?;
    let Some(resolver) = downstream_h3
        .accept()
        .await
        .map_err(|error| anyhow!("accept downstream WebTransport CONNECT: {error:?}"))?
    else {
        return Ok(());
    };
    let (downstream_request, mut downstream_connect) = resolver
        .resolve_request()
        .await
        .map_err(|error| anyhow!("resolve downstream WebTransport CONNECT: {error:?}"))?;
    validate_webtransport_connect(&downstream_request)?;

    let upstream =
        connect_upstream(&upstream_addr, &server_name, downstream_request.headers()).await?;
    if !upstream.response_status.is_success() {
        let response = Response::builder()
            .status(upstream.response_status)
            .body(())
            .context("build downstream rejection response")?;
        downstream_connect
            .send_response(response)
            .await
            .map_err(|error| anyhow!("send downstream rejection response: {error:?}"))?;
        downstream_connect
            .finish()
            .await
            .map_err(|error| anyhow!("finish downstream rejection response: {error:?}"))?;
        return Ok(());
    }

    let downstream_session_id = downstream_connect.id().into_inner();
    let response = Response::builder()
        .status(StatusCode::OK)
        .body(())
        .context("build downstream WebTransport response")?;
    downstream_connect
        .send_response(response)
        .await
        .map_err(|error| anyhow!("send downstream WebTransport response: {error:?}"))?;

    let _downstream_h3 = downstream_h3;
    let _downstream_connect = downstream_connect;
    let _upstream_endpoint = upstream.endpoint;
    let _upstream_h3 = upstream.h3_connection;
    let _upstream_connect = upstream.connect_stream;
    let upstream_connection = upstream.connection;
    let upstream_session_id = upstream.session_id;

    bridge_upstream_webtransport_streams(
        connection,
        upstream_connection,
        upstream_session_id,
        downstream_session_id,
    )
    .await;

    Ok(())
}

async fn bridge_upstream_webtransport_streams(
    downstream_connection: quinn::Connection,
    upstream_connection: quinn::Connection,
    upstream_session_id: u64,
    downstream_session_id: u64,
) {
    loop {
        tokio::select! {
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
                tokio::spawn(async move {
                    if let Err(error) = bridge_upstream_webtransport_stream(
                        downstream_connection,
                        upstream_send,
                        upstream_recv,
                        upstream_session_id,
                        downstream_session_id,
                    )
                    .await
                    {
                        warn!(error = %error, "WebTransport L7 stream bridge failed");
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
) -> Result<()> {
    let stream_session_id = match tokio::time::timeout(
        WEBTRANSPORT_STREAM_HEADER_TIMEOUT,
        stargate_protocol::read_webtransport_bidi_header(&mut upstream_recv),
    )
    .await
    {
        Ok(Ok(session_id)) => session_id,
        Ok(Err(error)) => {
            reset_webtransport_stream(&mut upstream_send, &mut upstream_recv);
            return Err(error).context("read upstream WebTransport stream header");
        }
        Err(_) => {
            reset_webtransport_stream(&mut upstream_send, &mut upstream_recv);
            return Err(anyhow!(
                "timed out waiting for upstream WebTransport stream header"
            ));
        }
    };
    if stream_session_id != upstream_session_id {
        reset_webtransport_stream(&mut upstream_send, &mut upstream_recv);
        ensure!(
            stream_session_id == upstream_session_id,
            "upstream WebTransport session id mismatch: got {stream_session_id}, expected {upstream_session_id}"
        );
    }

    let (mut downstream_send, downstream_recv) = downstream_connection
        .open_bi()
        .await
        .context("open downstream WebTransport stream")?;
    stargate_protocol::write_webtransport_bidi_header(&mut downstream_send, downstream_session_id)
        .await
        .context("write downstream WebTransport stream header")?;

    bridge_bidirectional(
        upstream_send,
        upstream_recv,
        downstream_send,
        downstream_recv,
    )
    .await
}

fn reset_webtransport_stream(
    quinn_send: &mut quinn::SendStream,
    quinn_recv: &mut quinn::RecvStream,
) {
    let _ = quinn_send.reset(0u32.into());
    let _ = quinn_recv.stop(0u32.into());
}

struct UpstreamSession {
    endpoint: Endpoint,
    connection: quinn::Connection,
    h3_connection: h3::client::Connection<h3_quinn::Connection, Bytes>,
    connect_stream: h3::client::RequestStream<
        <h3_quinn::OpenStreams as h3::quic::OpenStreams<Bytes>>::BidiStream,
        Bytes,
    >,
    response_status: StatusCode,
    session_id: u64,
}

async fn connect_upstream(
    upstream_addr: &str,
    server_name: &str,
    headers: &HeaderMap,
) -> Result<UpstreamSession> {
    let resolved_target = resolve_upstream_addr(upstream_addr).await?;
    let mut endpoint =
        Endpoint::client("0.0.0.0:0".parse().unwrap()).context("bind upstream QUIC client")?;
    endpoint.set_default_client_config(client_config()?);
    let connection = endpoint
        .connect(resolved_target, server_name)
        .with_context(|| format!("start upstream QUIC connect to {upstream_addr}"))?
        .await
        .with_context(|| format!("connect upstream QUIC to {upstream_addr}"))?;

    let mut builder = h3::client::builder();
    builder.enable_extended_connect(true).enable_datagram(true);
    let (h3_connection, mut send_request): (
        h3::client::Connection<h3_quinn::Connection, Bytes>,
        h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>,
    ) = builder
        .build(h3_quinn::Connection::new(connection.clone()))
        .await
        .map_err(|error| anyhow!("create upstream h3 client: {error:?}"))?;

    let mut request = Request::builder()
        .method(Method::CONNECT)
        .uri(format!("https://{server_name}{WEBTRANSPORT_TUNNEL_PATH}"))
        .body(())
        .context("build upstream WebTransport CONNECT")?;
    for (name, value) in headers {
        request.headers_mut().append(name, value.clone());
    }
    request
        .extensions_mut()
        .insert(h3::ext::Protocol::WEB_TRANSPORT);

    let mut connect_stream = send_request
        .send_request(request)
        .await
        .map_err(|error| anyhow!("send upstream WebTransport CONNECT: {error:?}"))?;
    let session_id = connect_stream.id().into_inner();
    connect_stream
        .finish()
        .await
        .map_err(|error| anyhow!("finish upstream WebTransport CONNECT: {error:?}"))?;
    let response = connect_stream
        .recv_response()
        .await
        .map_err(|error| anyhow!("read upstream WebTransport CONNECT response: {error:?}"))?;
    let response_status = response.status();

    Ok(UpstreamSession {
        endpoint,
        connection,
        h3_connection,
        connect_stream,
        response_status,
        session_id,
    })
}

async fn bridge_bidirectional(
    upstream_send: quinn::SendStream,
    upstream_recv: quinn::RecvStream,
    downstream_send: quinn::SendStream,
    downstream_recv: quinn::RecvStream,
) -> Result<()> {
    let downstream = copy_stream(upstream_recv, downstream_send);
    let upstream = copy_stream(downstream_recv, upstream_send);
    let (downstream_result, upstream_result) = tokio::join!(downstream, upstream);
    downstream_result.context("copy upstream to downstream")?;
    upstream_result.context("copy downstream to upstream")?;
    Ok(())
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
    let is_webtransport = request
        .extensions()
        .get::<h3::ext::Protocol>()
        .is_some_and(|protocol| *protocol == h3::ext::Protocol::WEB_TRANSPORT);
    ensure!(
        request.method() == Method::CONNECT
            && request.uri().path() == WEBTRANSPORT_TUNNEL_PATH
            && is_webtransport,
        "invalid downstream WebTransport CONNECT"
    );
    Ok(())
}

async fn resolve_upstream_addr(upstream_addr: &str) -> Result<SocketAddr> {
    let resolved_addrs: Vec<_> = tokio::net::lookup_host(upstream_addr)
        .await
        .with_context(|| format!("resolve upstream address {upstream_addr}"))?
        .collect();
    resolved_addrs
        .iter()
        .find(|addr| addr.is_ipv4())
        .copied()
        .or_else(|| resolved_addrs.first().copied())
        .ok_or_else(|| anyhow!("no resolved upstream address for {upstream_addr}"))
}

fn downstream_server_name(connection: &quinn::Connection) -> Option<String> {
    connection
        .handshake_data()
        .and_then(|data| data.downcast::<quinn::crypto::rustls::HandshakeData>().ok())
        .and_then(|data| data.server_name)
}

fn upstream_addr_for_sni(template: &str, server_name: &str) -> Result<String> {
    let pod_name = server_name
        .split('.')
        .next()
        .filter(|value| !value.is_empty())
        .context("server name does not include a pod hostname")?;
    Ok(template
        .replace("{pod_name}", pod_name)
        .replace("{server_name}", server_name))
}

fn client_config() -> Result<ClientConfig> {
    stargate_tls::build_insecure_quic_client_config_with_alpn(
        TunnelTransportProtocol::WebTransport.alpn_protocols(),
    )
}

fn server_config() -> Result<ServerConfig> {
    let (cert_pem, key_pem) = stargate_tls::generate_self_signed_cert()?;
    let cert_chain: Vec<CertificateDer<'static>> = rustls_pemfile::certs(&mut &*cert_pem)
        .collect::<std::result::Result<_, _>>()
        .context("parse l7 proxy cert")?;
    let key = rustls_pemfile::private_key(&mut &*key_pem)
        .context("parse l7 proxy key")?
        .context("missing l7 proxy key")?;
    let mut tls_config = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(cert_chain, key)
        .context("build l7 proxy TLS config")?;
    tls_config.alpn_protocols = TunnelTransportProtocol::WebTransport.alpn_protocols();
    Ok(ServerConfig::with_crypto(Arc::new(
        quinn::crypto::rustls::QuicServerConfig::try_from(tls_config)?,
    )))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn upstream_addr_template_uses_pod_name_from_sni() {
        let addr = upstream_addr_for_sni(
            "{pod_name}.stargate-headless.ns.svc.cluster.local:50072",
            "stargate-1.stargate.external",
        )
        .unwrap();

        assert_eq!(
            addr,
            "stargate-1.stargate-headless.ns.svc.cluster.local:50072"
        );
    }

    #[test]
    fn upstream_addr_template_can_use_full_server_name() {
        let addr =
            upstream_addr_for_sni("{server_name}:50072", "stargate-1.stargate.example").unwrap();

        assert_eq!(addr, "stargate-1.stargate.example:50072");
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn stalled_upstream_stream_header_does_not_block_later_streams() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (
            _upstream_server_endpoint,
            _upstream_proxy_endpoint,
            upstream_server_connection,
            upstream_proxy_connection,
        ) = connect_quic_pair().await;
        let (
            _downstream_proxy_endpoint,
            _downstream_client_endpoint,
            downstream_proxy_connection,
            downstream_client_connection,
        ) = connect_quic_pair().await;

        let bridge_task = tokio::spawn(bridge_upstream_webtransport_streams(
            downstream_proxy_connection,
            upstream_proxy_connection,
            41,
            77,
        ));
        let (_stalled_send, _stalled_recv) = upstream_server_connection
            .open_bi()
            .await
            .expect("stalled stream");

        let (mut upstream_send, _upstream_recv) = upstream_server_connection
            .open_bi()
            .await
            .expect("second upstream stream");
        stargate_protocol::write_webtransport_bidi_header(&mut upstream_send, 41)
            .await
            .expect("write upstream WebTransport header");
        upstream_send
            .write_all(b"later stream")
            .await
            .expect("write upstream payload");
        upstream_send.finish().expect("finish upstream stream");

        let (mut downstream_send, mut downstream_recv) = tokio::time::timeout(
            Duration::from_secs(1),
            downstream_client_connection.accept_bi(),
        )
        .await
        .expect("later downstream stream should not be blocked")
        .expect("downstream stream");
        let downstream_session_id =
            stargate_protocol::read_webtransport_bidi_header(&mut downstream_recv)
                .await
                .expect("read downstream WebTransport header");
        assert_eq!(downstream_session_id, 77);
        downstream_send.finish().expect("finish downstream stream");
        let payload = downstream_recv
            .read_to_end(1024)
            .await
            .expect("read payload");
        assert_eq!(payload, b"later stream");

        bridge_task.abort();
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn downstream_close_closes_upstream_session() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (
            _upstream_server_endpoint,
            _upstream_proxy_endpoint,
            upstream_server_connection,
            upstream_proxy_connection,
        ) = connect_quic_pair().await;
        let (
            _downstream_proxy_endpoint,
            _downstream_client_endpoint,
            downstream_proxy_connection,
            downstream_client_connection,
        ) = connect_quic_pair().await;

        let bridge_task = tokio::spawn(bridge_upstream_webtransport_streams(
            downstream_proxy_connection,
            upstream_proxy_connection,
            41,
            77,
        ));

        downstream_client_connection.close(0u32.into(), b"client restart");
        tokio::time::timeout(Duration::from_secs(1), upstream_server_connection.closed())
            .await
            .expect("upstream session should close after downstream disconnects");

        bridge_task.abort();
    }

    async fn connect_quic_pair() -> (Endpoint, Endpoint, quinn::Connection, quinn::Connection) {
        let server_endpoint =
            Endpoint::server(server_config().unwrap(), "127.0.0.1:0".parse().unwrap()).unwrap();
        let server_addr = server_endpoint.local_addr().unwrap();
        let server_task = tokio::spawn(async move {
            let incoming = server_endpoint
                .accept()
                .await
                .expect("server should accept");
            let server_connection = incoming.await.expect("server connection should complete");
            (server_endpoint, server_connection)
        });

        let mut client_endpoint = Endpoint::client("127.0.0.1:0".parse().unwrap()).unwrap();
        client_endpoint.set_default_client_config(client_config().unwrap());
        let client_connection = client_endpoint
            .connect(server_addr, "stargate")
            .unwrap()
            .await
            .unwrap();
        let (server_endpoint, server_connection) = server_task.await.unwrap();

        (
            server_endpoint,
            client_endpoint,
            server_connection,
            client_connection,
        )
    }
}

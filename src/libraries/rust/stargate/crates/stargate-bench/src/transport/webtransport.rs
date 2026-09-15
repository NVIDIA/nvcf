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
use std::time::Instant;

use anyhow::{Context, Result, anyhow, ensure};
use bytes::Bytes;
use futures::future;
use http::{HeaderMap, HeaderValue, Method, Request, Response, StatusCode};
use quinn::Endpoint;
use stargate_protocol::TunnelTransportProtocol;

use super::tls::{SERVER_NAME, client_endpoint, connect_quic};
use super::trials::{ResponseMeasurement, benchmark_requests, duration_us, request_headers};
use super::{
    PayloadShape, SERVER_SHUTDOWN_TIMEOUT, TransportBenchConfig, TransportKind,
    TransportRunOutcome, start_quic_server,
};

const WEBTRANSPORT_TUNNEL_PATH: &str = "/_stargate/webtransport";

type H3ClientBidiStream = <h3_quinn::OpenStreams as h3::quic::OpenStreams<Bytes>>::BidiStream;
type H3ClientRequestStream = h3::client::RequestStream<H3ClientBidiStream, Bytes>;
#[derive(Clone)]
struct WebTransportSession {
    connection: quinn::Connection,
    bidi_header: Bytes,
}

struct WebTransportBenchmarkClient {
    connection: quinn::Connection,
    send_request: h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>,
    connect_stream: H3ClientRequestStream,
    bidi_header: Bytes,
    driver_task: tokio::task::JoinHandle<Result<()>>,
}
pub(super) async fn run_webtransport_h3_quinn(
    config: TransportBenchConfig,
    shape: PayloadShape,
    trial_index: usize,
) -> Result<TransportRunOutcome> {
    let server = start_quic_server(
        config,
        TunnelTransportProtocol::WebTransport,
        shape.response_chunks.clone(),
        "bind WebTransport QUIC server",
        "read WebTransport server addr",
        handle_webtransport_connection,
    )?;
    let (endpoint, clients) =
        connect_webtransport_clients(config, server.addr, &server.cert_pem).await?;
    let sessions = Arc::new(
        clients
            .iter()
            .map(|client| WebTransportSession {
                connection: client.connection.clone(),
                bidi_header: client.bidi_header.clone(),
            })
            .collect::<Vec<_>>(),
    );
    let outcome = benchmark_requests(
        config,
        TransportKind::WebTransportH3Quinn,
        trial_index,
        shape,
        move |request_index, connection_index, shape| {
            let session = sessions[connection_index].clone();
            move |started_at| {
                execute_webtransport_request(session, shape, request_index, started_at)
            }
        },
    )
    .await?;

    close_webtransport_clients(endpoint, clients).await;
    server.shutdown().await?;
    Ok(outcome)
}

async fn handle_webtransport_connection(
    connection: quinn::Connection,
    config: TransportBenchConfig,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<()> {
    let mut h3_connection: h3::server::Connection<h3_quinn::Connection, Bytes> =
        h3::server::builder()
            .send_grease(config.http3_send_grease)
            .enable_webtransport(true)
            .enable_extended_connect(true)
            .enable_datagram(true)
            .max_webtransport_sessions(1)
            .build(h3_quinn::Connection::new(connection.clone()))
            .await
            .map_err(|error| anyhow!("create WebTransport h3 server connection: {error:?}"))?;
    let Some(resolver) = h3_connection
        .accept()
        .await
        .map_err(|error| anyhow!("accept WebTransport CONNECT: {error:?}"))?
    else {
        return Ok(());
    };
    let (request, mut connect_stream) = resolver
        .resolve_request()
        .await
        .map_err(|error| anyhow!("resolve WebTransport CONNECT: {error:?}"))?;
    ensure!(
        request.method() == Method::CONNECT
            && request.uri().path() == WEBTRANSPORT_TUNNEL_PATH
            && request.extensions().get::<h3::ext::Protocol>()
                == Some(&h3::ext::Protocol::WEB_TRANSPORT),
        "invalid WebTransport CONNECT request"
    );
    let session_id = connect_stream.id().into_inner();
    let response = Response::builder()
        .status(StatusCode::OK)
        .body(())
        .context("build WebTransport CONNECT response")?;
    connect_stream
        .send_response(response)
        .await
        .map_err(|error| anyhow!("send WebTransport CONNECT response: {error:?}"))?;

    while let Ok((quinn_send, quinn_recv)) = connection.accept_bi().await {
        let response_chunks = response_chunks.clone();
        tokio::spawn(async move {
            let _ = handle_webtransport_benchmark_stream(
                quinn_send,
                quinn_recv,
                session_id,
                response_chunks,
            )
            .await;
        });
    }
    Ok(())
}

async fn handle_webtransport_benchmark_stream(
    quinn_send: quinn::SendStream,
    mut quinn_recv: quinn::RecvStream,
    session_id: u64,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<()> {
    let stream_session_id = stargate_protocol::read_webtransport_bidi_header(&mut quinn_recv)
        .await
        .context("read WebTransport stream header")?;
    ensure!(
        stream_session_id == session_id,
        "WebTransport stream session id mismatch: got {stream_session_id}, expected {session_id}"
    );
    handle_webtransport_http_benchmark_stream(quinn_send, quinn_recv, response_chunks).await
}

async fn handle_webtransport_http_benchmark_stream(
    mut quinn_send: quinn::SendStream,
    mut quinn_recv: quinn::RecvStream,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<()> {
    let _request_head = stargate_protocol::read_webtransport_http_request_head(&mut quinn_recv)
        .await
        .context("read WebTransport benchmark request head")?;
    while stargate_protocol::read_webtransport_http_body_chunk(&mut quinn_recv)
        .await
        .context("read WebTransport benchmark request body")?
        .is_some()
    {}

    let response_head = stargate_protocol::WebTransportHttpResponseHead {
        status: StatusCode::OK,
        headers: HeaderMap::from_iter([(
            http::header::CONTENT_TYPE,
            HeaderValue::from_static("application/octet-stream"),
        )]),
    };
    stargate_protocol::write_webtransport_http_response_head(&mut quinn_send, &response_head)
        .await
        .context("send WebTransport benchmark response head")?;
    for chunk in response_chunks.iter() {
        stargate_protocol::write_webtransport_http_body(&mut quinn_send, chunk.clone())
            .await
            .context("send WebTransport benchmark response body")?;
    }
    stargate_protocol::finish_webtransport_http_stream(&mut quinn_send)
        .context("finish WebTransport benchmark response")?;
    Ok(())
}

async fn connect_webtransport_clients(
    config: TransportBenchConfig,
    addr: SocketAddr,
    server_cert_pem: &[u8],
) -> Result<(Endpoint, Vec<WebTransportBenchmarkClient>)> {
    let endpoint = client_endpoint(
        config,
        TunnelTransportProtocol::WebTransport.alpn_protocols(),
        server_cert_pem,
    )?;
    let mut clients = Vec::with_capacity(config.quic_connections);
    for _ in 0..config.quic_connections {
        let connection = connect_quic(&endpoint, addr).await?;
        let (mut driver, mut send_request): (
            h3::client::Connection<h3_quinn::Connection, Bytes>,
            h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>,
        ) = h3::client::builder()
            .send_grease(config.http3_send_grease)
            .enable_extended_connect(true)
            .enable_datagram(true)
            .build(h3_quinn::Connection::new(connection.clone()))
            .await
            .map_err(|error| anyhow!("create WebTransport h3 client: {error:?}"))?;
        let driver_task = tokio::spawn(async move {
            let error = future::poll_fn(|cx| driver.poll_close(cx)).await;
            ensure!(
                error.is_h3_no_error(),
                "WebTransport h3 client connection closed: {error:?}"
            );
            Ok(())
        });

        let mut request = Request::builder()
            .method(Method::CONNECT)
            .uri(format!("https://{SERVER_NAME}{WEBTRANSPORT_TUNNEL_PATH}"))
            .body(())
            .context("build WebTransport CONNECT request")?;
        request
            .extensions_mut()
            .insert(h3::ext::Protocol::WEB_TRANSPORT);
        let mut connect_stream = send_request
            .send_request(request)
            .await
            .map_err(|error| anyhow!("send WebTransport CONNECT request: {error:?}"))?;
        let session_id = connect_stream.id().into_inner();
        connect_stream
            .finish()
            .await
            .map_err(|error| anyhow!("finish WebTransport CONNECT request: {error:?}"))?;
        let response = connect_stream
            .recv_response()
            .await
            .map_err(|error| anyhow!("read WebTransport CONNECT response: {error:?}"))?;
        ensure!(
            response.status().is_success(),
            "WebTransport CONNECT rejected with status {}",
            response.status()
        );
        let bidi_header = stargate_protocol::WebTransportBidiHeader::new(session_id)
            .context("precompute WebTransport benchmark stream header")?
            .to_bytes();

        clients.push(WebTransportBenchmarkClient {
            connection,
            send_request,
            connect_stream,
            bidi_header,
            driver_task,
        });
    }
    Ok((endpoint, clients))
}

async fn close_webtransport_clients(endpoint: Endpoint, clients: Vec<WebTransportBenchmarkClient>) {
    for mut client in clients {
        // Drop the CONNECT stream and final request sender before closing QUIC so the H3 driver can drain shutdown.
        drop(client.connect_stream);
        drop(client.send_request);
        client.connection.close(0_u32.into(), b"benchmark complete");
        if tokio::time::timeout(SERVER_SHUTDOWN_TIMEOUT, &mut client.driver_task)
            .await
            .is_err()
        {
            client.driver_task.abort();
        }
    }
    endpoint.wait_idle().await;
}

async fn execute_webtransport_request(
    session: WebTransportSession,
    shape: PayloadShape,
    request_index: usize,
    started_at: Instant,
) -> Result<ResponseMeasurement> {
    let (mut quinn_send, mut quinn_recv) = session
        .connection
        .open_bi()
        .await
        .context("open WebTransport request stream")?;
    let request_head = stargate_protocol::WebTransportHttpRequestHead {
        method: Method::POST,
        path_and_query: "/v1/chat/completions".to_string(),
        headers: request_headers(request_index)?,
    };
    stargate_protocol::write_webtransport_http_request_head_after_prefix(
        &mut quinn_send,
        session.bidi_header.clone(),
        &request_head,
    )
    .await
    .context("send WebTransport request head")?;
    for chunk in shape.request_chunks.iter() {
        stargate_protocol::write_webtransport_http_body(&mut quinn_send, chunk.clone())
            .await
            .context("send WebTransport request body")?;
    }
    stargate_protocol::finish_webtransport_http_stream(&mut quinn_send)
        .context("finish WebTransport request")?;

    let response_head = stargate_protocol::read_webtransport_http_response_head(&mut quinn_recv)
        .await
        .context("read WebTransport response head")?;
    let mut response = ResponseMeasurement::new(
        Some(response_head.status.as_u16()),
        duration_us(started_at.elapsed()),
    );
    while let Some(chunk) = stargate_protocol::read_webtransport_http_body_chunk(&mut quinn_recv)
        .await
        .context("read WebTransport response body")?
    {
        response.record_body(started_at, chunk.len());
    }
    Ok(response)
}

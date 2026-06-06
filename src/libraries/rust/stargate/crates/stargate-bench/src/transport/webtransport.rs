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
use http::{HeaderMap, HeaderName, HeaderValue, Method, Request, Response, StatusCode};
use quinn::Endpoint;
use stargate_protocol::TunnelTransportProtocol;
use tokio::sync::{Semaphore, oneshot};

use super::summary::summarize_samples;
use super::tls::{SERVER_NAME, client_endpoint, connect_quic, server_config};
use super::trials::{collect_samples, duration_us};
use super::{
    PayloadShape, RequestSample, RunningServer, SERVER_SHUTDOWN_TIMEOUT, TransportBenchConfig,
    TransportKind, TransportRunOutcome,
};

const WEBTRANSPORT_TUNNEL_PATH: &str = "/_stargate/webtransport";

type H3ClientBidiStream = <h3_quinn::OpenStreams as h3::quic::OpenStreams<Bytes>>::BidiStream;
type H3ClientRequestStream = h3::client::RequestStream<H3ClientBidiStream, Bytes>;
#[derive(Clone)]
struct WebTransportRequestConnection {
    connection_index: usize,
    connection: quinn::Connection,
    bidi_header: Bytes,
}

struct WebTransportBenchmarkClient {
    endpoint: Endpoint,
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
    let server = start_webtransport_server(config, shape.response_chunks.clone()).await?;
    let clients = connect_webtransport_clients(config, server.addr, &server.cert_pem).await?;
    let request_connections = Arc::new(
        clients
            .iter()
            .enumerate()
            .map(|(connection_index, client)| WebTransportRequestConnection {
                connection_index,
                connection: client.connection.clone(),
                bidi_header: client.bidi_header.clone(),
            })
            .collect::<Vec<_>>(),
    );

    if config.warmup_requests > 0 {
        let _ = drive_webtransport_requests(
            request_connections.clone(),
            shape.clone(),
            config.warmup_requests,
            config.concurrency,
        )
        .await?;
    }

    let started_at = Instant::now();
    let samples = drive_webtransport_requests(
        request_connections,
        shape.clone(),
        config.request_count,
        config.concurrency,
    )
    .await?;
    let measured_duration = started_at.elapsed();

    close_webtransport_clients(clients).await;
    server.shutdown().await?;

    let summary = summarize_samples(
        TransportKind::WebTransportH3Quinn,
        &samples,
        measured_duration,
    );
    Ok(TransportRunOutcome {
        transport: TransportKind::WebTransportH3Quinn,
        trial_index,
        summary,
        samples,
    })
}

async fn start_webtransport_server(
    config: TransportBenchConfig,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<RunningServer> {
    let generated = server_config(
        config,
        TunnelTransportProtocol::WebTransport.alpn_protocols(),
    )?;
    let endpoint = Endpoint::server(generated.server_config, "127.0.0.1:0".parse()?)
        .context("bind WebTransport QUIC server")?;
    let addr = endpoint
        .local_addr()
        .context("read WebTransport server addr")?;
    let (shutdown_tx, mut shutdown_rx) = oneshot::channel();
    let task = tokio::spawn(async move {
        loop {
            tokio::select! {
                _ = &mut shutdown_rx => break,
                incoming = endpoint.accept() => {
                    let Some(incoming) = incoming else { break };
                    let response_chunks = response_chunks.clone();
                    let config = config;
                    tokio::spawn(async move {
                        if let Ok(connection) = incoming.await {
                            let _ = handle_webtransport_connection(connection, config, response_chunks).await;
                        }
                    });
                }
            }
        }
        endpoint.close(0_u32.into(), b"benchmark shutdown");
        endpoint.wait_idle().await;
        Ok(())
    });

    Ok(RunningServer {
        addr,
        cert_pem: generated.cert_pem,
        shutdown_tx,
        task,
    })
}

async fn handle_webtransport_connection(
    connection: quinn::Connection,
    config: TransportBenchConfig,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<()> {
    let mut builder = h3::server::builder();
    builder
        .send_grease(config.http3_send_grease)
        .enable_webtransport(true)
        .enable_extended_connect(true)
        .enable_datagram(true)
        .max_webtransport_sessions(1);
    let mut h3_connection: h3::server::Connection<h3_quinn::Connection, Bytes> = builder
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
    let is_webtransport = request
        .extensions()
        .get::<h3::ext::Protocol>()
        .is_some_and(|protocol| *protocol == h3::ext::Protocol::WEB_TRANSPORT);
    ensure!(
        request.method() == Method::CONNECT
            && request.uri().path() == WEBTRANSPORT_TUNNEL_PATH
            && is_webtransport,
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

    let mut response_headers = HeaderMap::new();
    response_headers.insert(
        http::header::CONTENT_TYPE,
        HeaderValue::from_static("application/octet-stream"),
    );
    let response_head = stargate_protocol::WebTransportHttpResponseHead {
        status: StatusCode::OK,
        headers: response_headers,
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
) -> Result<Vec<WebTransportBenchmarkClient>> {
    let endpoint = client_endpoint(
        config,
        TunnelTransportProtocol::WebTransport.alpn_protocols(),
        server_cert_pem,
    )?;
    let mut clients = Vec::with_capacity(config.quic_connections);
    for _ in 0..config.quic_connections {
        let connection = connect_quic(&endpoint, addr).await?;
        let mut builder = h3::client::builder();
        builder
            .send_grease(config.http3_send_grease)
            .enable_extended_connect(true)
            .enable_datagram(true);
        let (mut driver, mut send_request): (
            h3::client::Connection<h3_quinn::Connection, Bytes>,
            h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>,
        ) = builder
            .build(h3_quinn::Connection::new(connection.clone()))
            .await
            .map_err(|error| anyhow!("create WebTransport h3 client: {error:?}"))?;
        let driver_task = tokio::spawn(async move {
            let error = future::poll_fn(|cx| driver.poll_close(cx)).await;
            if error.is_h3_no_error() {
                Ok(())
            } else {
                Err(anyhow!(
                    "WebTransport h3 client connection closed: {error:?}"
                ))
            }
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
            endpoint: endpoint.clone(),
            connection,
            send_request,
            connect_stream,
            bidi_header,
            driver_task,
        });
    }
    Ok(clients)
}

async fn close_webtransport_clients(clients: Vec<WebTransportBenchmarkClient>) {
    let endpoint = clients.first().map(|client| client.endpoint.clone());
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
    if let Some(endpoint) = endpoint {
        endpoint.wait_idle().await;
    }
}

async fn drive_webtransport_requests(
    connections: Arc<Vec<WebTransportRequestConnection>>,
    shape: PayloadShape,
    request_count: usize,
    concurrency: usize,
) -> Result<Vec<RequestSample>> {
    let semaphore = Arc::new(Semaphore::new(concurrency));
    let mut tasks = Vec::with_capacity(request_count);
    for request_index in 0..request_count {
        let request_connection = connections[request_index % connections.len()].clone();
        let shape = shape.clone();
        let semaphore = semaphore.clone();
        tasks.push(tokio::spawn(async move {
            let _permit = semaphore
                .acquire_owned()
                .await
                .expect("semaphore should remain open");
            execute_webtransport_request(request_connection, shape, request_index).await
        }));
    }
    collect_samples(tasks).await
}

async fn execute_webtransport_request(
    request_connection: WebTransportRequestConnection,
    shape: PayloadShape,
    request_index: usize,
) -> RequestSample {
    let started_at = Instant::now();
    let result = async {
        let (quinn_send, quinn_recv) = request_connection
            .connection
            .open_bi()
            .await
            .context("open WebTransport request stream")?;
        let mut quinn_send = quinn_send;
        let mut quinn_recv = quinn_recv;

        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static("x-request-id"),
            HeaderValue::from_str(&format!("transport-bench-{request_index}"))
                .context("build request id")?,
        );
        headers.insert(
            HeaderName::from_static("x-model"),
            HeaderValue::from_static("transport-bench-model"),
        );
        headers.insert(
            HeaderName::from_static("x-input-tokens"),
            HeaderValue::from_static("1"),
        );
        let request_head = stargate_protocol::WebTransportHttpRequestHead {
            method: Method::POST,
            path_and_query: "/v1/chat/completions".to_string(),
            headers,
        };
        stargate_protocol::write_webtransport_http_request_head_after_prefix(
            &mut quinn_send,
            request_connection.bidi_header.clone(),
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

        let response_head =
            stargate_protocol::read_webtransport_http_response_head(&mut quinn_recv)
                .await
                .context("read WebTransport response head")?;
        let response_headers_us = duration_us(started_at.elapsed());
        let response_status = Some(response_head.status.as_u16());
        let mut first_body_us = None;
        let mut response_bytes = 0usize;
        while let Some(chunk) =
            stargate_protocol::read_webtransport_http_body_chunk(&mut quinn_recv)
                .await
                .context("read WebTransport response body")?
        {
            if first_body_us.is_none() {
                first_body_us = Some(duration_us(started_at.elapsed()));
            }
            response_bytes += chunk.len();
        }
        Ok::<_, anyhow::Error>((
            response_status,
            response_headers_us,
            first_body_us,
            response_bytes,
        ))
    }
    .await;

    match result {
        Ok((response_status, response_headers_us, first_body_us, response_bytes)) => {
            RequestSample {
                request_index,
                connection_index: request_connection.connection_index,
                ok: response_status == Some(200) && response_bytes == shape.response_bytes,
                response_status,
                request_bytes: shape.request_bytes,
                response_bytes,
                response_headers_us: Some(response_headers_us),
                first_body_us,
                completion_us: duration_us(started_at.elapsed()),
                error: None,
            }
        }
        Err(error) => RequestSample {
            request_index,
            connection_index: request_connection.connection_index,
            ok: false,
            response_status: None,
            request_bytes: shape.request_bytes,
            response_bytes: 0,
            response_headers_us: None,
            first_body_us: None,
            completion_us: duration_us(started_at.elapsed()),
            error: Some(error.to_string()),
        },
    }
}

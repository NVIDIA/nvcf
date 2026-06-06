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

use anyhow::{Context, Result};
use bytes::Bytes;
use http::{HeaderMap, HeaderName, HeaderValue};
use quinn::Endpoint;
use stargate_protocol::TunnelTransportProtocol;
use tokio::sync::{Semaphore, oneshot};

use super::summary::summarize_samples;
use super::tls::{client_endpoint, connect_quic, server_config};
use super::trials::{collect_samples, duration_us};
use super::{
    PayloadShape, RequestSample, RunningServer, TransportBenchConfig, TransportKind,
    TransportRunOutcome,
};

#[derive(Clone)]
struct QuicRequestConnection {
    connection_index: usize,
    connection: quinn::Connection,
}

pub(super) async fn run_custom_protocol(
    config: TransportBenchConfig,
    shape: PayloadShape,
    trial_index: usize,
) -> Result<TransportRunOutcome> {
    let server = start_custom_server(config, shape.response_chunks.clone()).await?;
    let clients = connect_quic_set(
        config,
        server.addr,
        TunnelTransportProtocol::Custom.alpn_protocols(),
        &server.cert_pem,
    )
    .await?;
    let request_connections = Arc::new(
        clients
            .iter()
            .enumerate()
            .map(
                |(connection_index, (_endpoint, connection))| QuicRequestConnection {
                    connection_index,
                    connection: connection.clone(),
                },
            )
            .collect::<Vec<_>>(),
    );

    if config.warmup_requests > 0 {
        let _ = drive_custom_requests(
            request_connections.clone(),
            shape.clone(),
            config.warmup_requests,
            config.concurrency,
        )
        .await?;
    }

    let started_at = Instant::now();
    let samples = drive_custom_requests(
        request_connections,
        shape.clone(),
        config.request_count,
        config.concurrency,
    )
    .await?;
    let measured_duration = started_at.elapsed();

    close_quic_clients(clients).await;
    server.shutdown().await?;

    let summary = summarize_samples(TransportKind::CustomProtocol, &samples, measured_duration);
    Ok(TransportRunOutcome {
        transport: TransportKind::CustomProtocol,
        trial_index,
        summary,
        samples,
    })
}

pub(super) async fn start_custom_server(
    config: TransportBenchConfig,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<RunningServer> {
    let generated = server_config(config, TunnelTransportProtocol::Custom.alpn_protocols())?;
    let endpoint = Endpoint::server(generated.server_config, "127.0.0.1:0".parse()?)
        .context("bind custom protocol QUIC server")?;
    let addr = endpoint.local_addr().context("read custom server addr")?;
    let (shutdown_tx, mut shutdown_rx) = oneshot::channel();
    let task = tokio::spawn(async move {
        loop {
            tokio::select! {
                _ = &mut shutdown_rx => break,
                incoming = endpoint.accept() => {
                    let Some(incoming) = incoming else { break };
                    let response_chunks = response_chunks.clone();
                    tokio::spawn(async move {
                        if let Ok(connection) = incoming.await {
                            let _ = handle_custom_connection(connection, response_chunks).await;
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

async fn handle_custom_connection(
    connection: quinn::Connection,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<()> {
    while let Ok((quinn_send, quinn_recv)) = connection.accept_bi().await {
        let response_chunks = response_chunks.clone();
        tokio::spawn(async move {
            let _ = handle_custom_stream(quinn_send, quinn_recv, response_chunks).await;
        });
    }
    Ok(())
}

async fn handle_custom_stream(
    quinn_send: quinn::SendStream,
    quinn_recv: quinn::RecvStream,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<()> {
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let _request_headers = recv
        .recv_header()
        .await
        .context("read custom request headers")?;
    while recv.recv_body().await?.into_body().is_some() {}

    let mut response_headers = HeaderMap::new();
    response_headers.insert(
        HeaderName::from_static("x-status"),
        HeaderValue::from_static("200"),
    );
    response_headers.insert(
        http::header::CONTENT_TYPE,
        HeaderValue::from_static("application/octet-stream"),
    );
    send.send_header(response_headers)
        .await
        .context("send custom response headers")?;
    for chunk in response_chunks.iter() {
        send.send_body(chunk.clone())
            .await
            .context("send custom response body")?;
    }
    send.finish().context("finish custom response")?;
    Ok(())
}

pub(super) async fn connect_quic_set(
    config: TransportBenchConfig,
    addr: SocketAddr,
    alpn_protocols: Vec<Vec<u8>>,
    server_cert_pem: &[u8],
) -> Result<Vec<(Endpoint, quinn::Connection)>> {
    let endpoint = client_endpoint(config, alpn_protocols, server_cert_pem)?;
    let mut clients = Vec::with_capacity(config.quic_connections);
    for _ in 0..config.quic_connections {
        clients.push((endpoint.clone(), connect_quic(&endpoint, addr).await?));
    }
    Ok(clients)
}

pub(super) async fn close_quic_clients(clients: Vec<(Endpoint, quinn::Connection)>) {
    let endpoint = clients.first().map(|(endpoint, _)| endpoint.clone());
    for (_, connection) in clients {
        connection.close(0_u32.into(), b"benchmark complete");
    }
    if let Some(endpoint) = endpoint {
        endpoint.wait_idle().await;
    }
}

async fn drive_custom_requests(
    connections: Arc<Vec<QuicRequestConnection>>,
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
            execute_custom_request(request_connection, shape, request_index).await
        }));
    }
    collect_samples(tasks).await
}

async fn execute_custom_request(
    request_connection: QuicRequestConnection,
    shape: PayloadShape,
    request_index: usize,
) -> RequestSample {
    let started_at = Instant::now();
    let result = async {
        let (quinn_send, quinn_recv) = request_connection
            .connection
            .open_bi()
            .await
            .context("open custom request stream")?;
        let mut send = stargate_protocol::SendStream::new(quinn_send);
        let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static("x-method"),
            HeaderValue::from_static("POST"),
        );
        headers.insert(
            HeaderName::from_static("x-path"),
            HeaderValue::from_static("/v1/chat/completions"),
        );
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
        send.send_header(headers)
            .await
            .context("send custom request headers")?;
        for chunk in shape.request_chunks.iter() {
            send.send_body(chunk.clone())
                .await
                .context("send custom request body")?;
        }
        send.finish().context("finish custom request")?;

        let response_headers = recv
            .recv_header()
            .await
            .context("read custom response headers")?;
        let response_headers_us = duration_us(started_at.elapsed());
        let response_status = response_headers
            .get("x-status")
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.parse::<u16>().ok());
        let mut first_body_us = None;
        let mut response_bytes = 0usize;
        while let Some(chunk) = recv
            .recv_body()
            .await
            .context("read custom response body")?
            .into_body()
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

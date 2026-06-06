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

use anyhow::{Context, Result, anyhow};
use bytes::{Buf, Bytes};
use futures::future;
use http::{Method, Request, Response, StatusCode, Uri};
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

struct H3BenchmarkClient {
    endpoint: Endpoint,
    connection: quinn::Connection,
    send_request: h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>,
    driver_task: tokio::task::JoinHandle<Result<()>>,
}
pub(super) async fn run_http3_h3_quinn(
    config: TransportBenchConfig,
    shape: PayloadShape,
    trial_index: usize,
) -> Result<TransportRunOutcome> {
    let server = start_h3_server(config, shape.response_chunks.clone()).await?;
    let clients = connect_h3_clients(config, server.addr, &server.cert_pem).await?;
    let send_requests = Arc::new(
        clients
            .iter()
            .enumerate()
            .map(|(connection_index, client)| (connection_index, client.send_request.clone()))
            .collect::<Vec<_>>(),
    );

    if config.warmup_requests > 0 {
        let _ = drive_h3_requests(
            send_requests.clone(),
            server.addr,
            shape.clone(),
            config.warmup_requests,
            config.concurrency,
        )
        .await?;
    }

    let started_at = Instant::now();
    let samples = drive_h3_requests(
        send_requests,
        server.addr,
        shape.clone(),
        config.request_count,
        config.concurrency,
    )
    .await?;
    let measured_duration = started_at.elapsed();

    close_h3_clients(clients).await;
    server.shutdown().await?;

    let summary = summarize_samples(TransportKind::Http3H3Quinn, &samples, measured_duration);
    Ok(TransportRunOutcome {
        transport: TransportKind::Http3H3Quinn,
        trial_index,
        summary,
        samples,
    })
}

async fn start_h3_server(
    config: TransportBenchConfig,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<RunningServer> {
    let generated = server_config(config, TunnelTransportProtocol::Http3.alpn_protocols())?;
    let endpoint = Endpoint::server(generated.server_config, "127.0.0.1:0".parse()?)
        .context("bind h3 QUIC server")?;
    let addr = endpoint.local_addr().context("read h3 server addr")?;
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
                            let _ = handle_h3_connection(connection, config, response_chunks).await;
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

async fn handle_h3_connection(
    connection: quinn::Connection,
    config: TransportBenchConfig,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<()> {
    let mut h3_connection = h3::server::builder()
        .send_grease(config.http3_send_grease)
        .build(h3_quinn::Connection::new(connection))
        .await
        .map_err(|error| anyhow!("create h3 server connection: {error:?}"))?;
    loop {
        match h3_connection.accept().await {
            Ok(Some(resolver)) => {
                let response_chunks = response_chunks.clone();
                tokio::spawn(async move {
                    let _ = handle_h3_request(resolver, response_chunks).await;
                });
            }
            Ok(None) => break,
            Err(error) if error.is_h3_no_error() => break,
            Err(error) => return Err(anyhow!("h3 accept failed: {error:?}")),
        }
    }
    Ok(())
}

async fn handle_h3_request(
    resolver: h3::server::RequestResolver<h3_quinn::Connection, Bytes>,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<()> {
    let (_request, mut stream) = resolver
        .resolve_request()
        .await
        .map_err(|error| anyhow!("resolve h3 request: {error:?}"))?;
    while let Some(chunk) = stream
        .recv_data()
        .await
        .map_err(|error| anyhow!("read h3 request body: {error:?}"))?
    {
        let _ = chunk.remaining();
    }

    let response = Response::builder()
        .status(StatusCode::OK)
        .header(http::header::CONTENT_TYPE, "application/octet-stream")
        .body(())
        .context("build h3 response")?;
    stream
        .send_response(response)
        .await
        .map_err(|error| anyhow!("send h3 response headers: {error:?}"))?;
    for chunk in response_chunks.iter() {
        stream
            .send_data(chunk.clone())
            .await
            .map_err(|error| anyhow!("send h3 response body: {error:?}"))?;
    }
    stream
        .finish()
        .await
        .map_err(|error| anyhow!("finish h3 response: {error:?}"))?;
    Ok(())
}

async fn connect_h3_clients(
    config: TransportBenchConfig,
    addr: SocketAddr,
    server_cert_pem: &[u8],
) -> Result<Vec<H3BenchmarkClient>> {
    let endpoint = client_endpoint(
        config,
        TunnelTransportProtocol::Http3.alpn_protocols(),
        server_cert_pem,
    )?;
    let mut clients = Vec::with_capacity(config.quic_connections);
    for _ in 0..config.quic_connections {
        let connection = connect_quic(&endpoint, addr).await?;
        let quinn_connection = h3_quinn::Connection::new(connection.clone());
        let (mut driver, send_request) = h3::client::builder()
            .send_grease(config.http3_send_grease)
            .build(quinn_connection)
            .await
            .map_err(|error| anyhow!("create h3 client: {error:?}"))?;
        let driver_task = tokio::spawn(async move {
            let error = future::poll_fn(|cx| driver.poll_close(cx)).await;
            if error.is_h3_no_error() {
                Ok(())
            } else {
                Err(anyhow!("h3 client connection closed: {error:?}"))
            }
        });
        clients.push(H3BenchmarkClient {
            endpoint: endpoint.clone(),
            connection,
            send_request,
            driver_task,
        });
    }
    Ok(clients)
}

async fn close_h3_clients(clients: Vec<H3BenchmarkClient>) {
    let endpoint = clients.first().map(|client| client.endpoint.clone());
    for mut client in clients {
        // Drop the final request sender before closing QUIC so the H3 driver can drain shutdown.
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

async fn drive_h3_requests(
    send_requests: Arc<Vec<(usize, h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>)>>,
    addr: SocketAddr,
    shape: PayloadShape,
    request_count: usize,
    concurrency: usize,
) -> Result<Vec<RequestSample>> {
    let semaphore = Arc::new(Semaphore::new(concurrency));
    let mut tasks = Vec::with_capacity(request_count);
    for request_index in 0..request_count {
        let (connection_index, send_request) =
            send_requests[request_index % send_requests.len()].clone();
        let shape = shape.clone();
        let semaphore = semaphore.clone();
        tasks.push(tokio::spawn(async move {
            let _permit = semaphore
                .acquire_owned()
                .await
                .expect("semaphore should remain open");
            execute_h3_request(send_request, addr, shape, request_index, connection_index).await
        }));
    }
    collect_samples(tasks).await
}

async fn execute_h3_request(
    mut send_request: h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>,
    addr: SocketAddr,
    shape: PayloadShape,
    request_index: usize,
    connection_index: usize,
) -> RequestSample {
    let started_at = Instant::now();
    let result = async {
        let uri: Uri = format!("https://{SERVER_NAME}:{}/v1/chat/completions", addr.port())
            .parse()
            .context("build h3 request URI")?;
        let request = Request::builder()
            .method(Method::POST)
            .uri(uri)
            .header("x-request-id", format!("transport-bench-{request_index}"))
            .header("x-model", "transport-bench-model")
            .header("x-input-tokens", "1")
            .header(http::header::CONTENT_TYPE, "application/octet-stream")
            .body(())
            .context("build h3 request")?;
        let mut stream = send_request
            .send_request(request)
            .await
            .map_err(|error| anyhow!("send h3 request headers: {error:?}"))?;
        for chunk in shape.request_chunks.iter() {
            stream
                .send_data(chunk.clone())
                .await
                .map_err(|error| anyhow!("send h3 request body: {error:?}"))?;
        }
        stream
            .finish()
            .await
            .map_err(|error| anyhow!("finish h3 request: {error:?}"))?;

        let response = stream
            .recv_response()
            .await
            .map_err(|error| anyhow!("read h3 response headers: {error:?}"))?;
        let response_headers_us = duration_us(started_at.elapsed());
        let response_status = Some(response.status().as_u16());
        let mut first_body_us = None;
        let mut response_bytes = 0usize;
        while let Some(chunk) = stream
            .recv_data()
            .await
            .map_err(|error| anyhow!("read h3 response body: {error:?}"))?
        {
            if first_body_us.is_none() {
                first_body_us = Some(duration_us(started_at.elapsed()));
            }
            response_bytes += chunk.remaining();
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
                connection_index,
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
            connection_index,
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

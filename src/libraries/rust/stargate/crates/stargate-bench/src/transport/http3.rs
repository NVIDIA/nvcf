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
use bytes::{Buf, Bytes};
use futures::future;
use http::{Request, Response, StatusCode, Uri};
use quinn::Endpoint;
use stargate_protocol::TunnelTransportProtocol;

use super::tls::{SERVER_NAME, client_endpoint, connect_quic};
use super::trials::{ResponseMeasurement, benchmark_requests, duration_us, request_headers};
use super::{
    PayloadShape, SERVER_SHUTDOWN_TIMEOUT, TransportBenchConfig, TransportKind,
    TransportRunOutcome, start_quic_server,
};

struct H3BenchmarkClient {
    connection: quinn::Connection,
    send_request: h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>,
    driver_task: tokio::task::JoinHandle<Result<()>>,
}
pub(super) async fn run_http3_h3_quinn(
    config: TransportBenchConfig,
    shape: PayloadShape,
    trial_index: usize,
) -> Result<TransportRunOutcome> {
    let server = start_quic_server(
        config,
        TunnelTransportProtocol::Http3,
        shape.response_chunks.clone(),
        "bind h3 QUIC server",
        "read h3 server addr",
        handle_h3_connection,
    )?;
    let (endpoint, clients) = connect_h3_clients(config, server.addr, &server.cert_pem).await?;
    let send_requests = Arc::new(
        clients
            .iter()
            .map(|client| client.send_request.clone())
            .collect::<Vec<_>>(),
    );
    let addr = server.addr;
    let outcome = benchmark_requests(
        config,
        TransportKind::Http3H3Quinn,
        trial_index,
        shape,
        move |request_index, connection_index, shape| {
            let send_request = send_requests[connection_index].clone();
            move |started_at| {
                execute_h3_request(send_request, addr, shape, request_index, started_at)
            }
        },
    )
    .await?;

    close_h3_clients(endpoint, clients).await;
    server.shutdown().await?;
    Ok(outcome)
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
                tokio::spawn(handle_h3_request(resolver, response_chunks));
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
    while stream
        .recv_data()
        .await
        .map_err(|error| anyhow!("read h3 request body: {error:?}"))?
        .is_some()
    {}

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
) -> Result<(Endpoint, Vec<H3BenchmarkClient>)> {
    let endpoint = client_endpoint(
        config,
        TunnelTransportProtocol::Http3.alpn_protocols(),
        server_cert_pem,
    )?;
    let mut clients = Vec::with_capacity(config.quic_connections);
    for _ in 0..config.quic_connections {
        let connection = connect_quic(&endpoint, addr).await?;
        let (mut driver, send_request) = h3::client::builder()
            .send_grease(config.http3_send_grease)
            .build(h3_quinn::Connection::new(connection.clone()))
            .await
            .map_err(|error| anyhow!("create h3 client: {error:?}"))?;
        let driver_task = tokio::spawn(async move {
            let error = future::poll_fn(|cx| driver.poll_close(cx)).await;
            ensure!(
                error.is_h3_no_error(),
                "h3 client connection closed: {error:?}"
            );
            Ok(())
        });
        clients.push(H3BenchmarkClient {
            connection,
            send_request,
            driver_task,
        });
    }
    Ok((endpoint, clients))
}

async fn close_h3_clients(endpoint: Endpoint, clients: Vec<H3BenchmarkClient>) {
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
    endpoint.wait_idle().await;
}

async fn execute_h3_request(
    mut send_request: h3::client::SendRequest<h3_quinn::OpenStreams, Bytes>,
    addr: SocketAddr,
    shape: PayloadShape,
    request_index: usize,
    started_at: Instant,
) -> Result<ResponseMeasurement> {
    let uri: Uri = format!("https://{SERVER_NAME}:{}/v1/chat/completions", addr.port())
        .parse()
        .context("build h3 request URI")?;
    let mut request = Request::post(uri)
        .header(http::header::CONTENT_TYPE, "application/octet-stream")
        .body(())
        .context("build h3 request")?;
    let headers = request_headers(request_index)?;
    request.headers_mut().extend(headers);
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
    let mut response = ResponseMeasurement::new(
        Some(response.status().as_u16()),
        duration_us(started_at.elapsed()),
    );
    while let Some(chunk) = stream
        .recv_data()
        .await
        .map_err(|error| anyhow!("read h3 response body: {error:?}"))?
    {
        response.record_body(started_at, chunk.remaining());
    }
    Ok(response)
}

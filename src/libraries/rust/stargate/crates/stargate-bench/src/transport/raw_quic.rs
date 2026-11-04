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

use super::tls::{client_endpoint, connect_quic};
use super::trials::{ResponseMeasurement, benchmark_requests, duration_us, request_headers};
use super::{
    PayloadShape, RunningServer, TransportBenchConfig, TransportKind, TransportRunOutcome,
    start_quic_server,
};

pub(super) async fn run_raw_quic(
    config: TransportBenchConfig,
    shape: PayloadShape,
    trial_index: usize,
) -> Result<TransportRunOutcome> {
    let server = start_raw_quic_server(config, shape.response_chunks.clone()).await?;
    let clients = connect_quic_set(
        config,
        server.addr,
        TunnelTransportProtocol::RawQuic.alpn_protocols(),
        &server.cert_pem,
    )
    .await?;
    let connections: Arc<[_]> = clients.iter().map(|client| client.1.clone()).collect();
    let outcome = benchmark_requests(
        config,
        TransportKind::RawQuic,
        trial_index,
        shape,
        move |request_index, connection_index, shape| {
            let connection = connections[connection_index].clone();
            move |started_at| execute_raw_quic_request(connection, shape, request_index, started_at)
        },
    )
    .await?;

    close_quic_clients(clients).await;
    server.shutdown().await?;
    Ok(outcome)
}

pub(super) async fn start_raw_quic_server(
    config: TransportBenchConfig,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<RunningServer> {
    start_quic_server(
        config,
        TunnelTransportProtocol::RawQuic,
        response_chunks,
        "bind Raw QUIC server",
        "read Raw QUIC server address",
        handle_raw_quic_connection,
    )
}

async fn handle_raw_quic_connection(
    connection: quinn::Connection,
    _config: TransportBenchConfig,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<()> {
    while let Ok((quinn_send, quinn_recv)) = connection.accept_bi().await {
        tokio::spawn(handle_raw_quic_stream(
            quinn_send,
            quinn_recv,
            response_chunks.clone(),
        ));
    }
    Ok(())
}

async fn handle_raw_quic_stream(
    quinn_send: quinn::SendStream,
    quinn_recv: quinn::RecvStream,
    response_chunks: Arc<Vec<Bytes>>,
) -> Result<()> {
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    recv.recv_header()
        .await
        .context("read raw QUIC request headers")?;
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
        .context("send raw QUIC response headers")?;
    for chunk in response_chunks.iter() {
        send.send_body(chunk.clone())
            .await
            .context("send raw QUIC response body")?;
    }
    send.finish().context("finish raw QUIC response")?;
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
    let Some(endpoint) = clients.first().map(|client| client.0.clone()) else {
        return;
    };
    for (_, connection) in clients {
        connection.close(0_u32.into(), b"benchmark complete");
    }
    endpoint.wait_idle().await;
}

async fn execute_raw_quic_request(
    connection: quinn::Connection,
    shape: PayloadShape,
    request_index: usize,
    started_at: Instant,
) -> Result<ResponseMeasurement> {
    let (quinn_send, quinn_recv) = connection
        .open_bi()
        .await
        .context("open raw QUIC request stream")?;
    let mut send = stargate_protocol::SendStream::new(quinn_send);
    let mut recv = stargate_protocol::RecvStream::new(quinn_recv);

    let mut headers = request_headers(request_index)?;
    headers.insert(
        HeaderName::from_static("x-method"),
        HeaderValue::from_static("POST"),
    );
    headers.insert(
        HeaderName::from_static("x-path"),
        HeaderValue::from_static("/v1/chat/completions"),
    );
    send.send_header(headers)
        .await
        .context("send raw QUIC request headers")?;
    for chunk in shape.request_chunks.iter() {
        send.send_body(chunk.clone())
            .await
            .context("send raw QUIC request body")?;
    }
    send.finish().context("finish raw QUIC request")?;

    let response_headers = recv
        .recv_header()
        .await
        .context("read raw QUIC response headers")?;
    let response_status = response_headers
        .get("x-status")
        .and_then(|value| value.to_str().ok()?.parse::<u16>().ok());
    let mut response = ResponseMeasurement::new(response_status, duration_us(started_at.elapsed()));
    while let Some(chunk) = recv
        .recv_body()
        .await
        .context("read raw QUIC response body")?
        .into_body()
    {
        response.record_body(started_at, chunk.len());
    }
    Ok(response)
}

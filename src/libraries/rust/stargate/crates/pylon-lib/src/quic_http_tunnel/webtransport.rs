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

use anyhow::{Context, Result, bail};
use reqwest::header::HeaderMap;
use stargate_protocol::tunnel_contract::WEBTRANSPORT_TUNNEL_PATH;
use tokio_util::{sync::CancellationToken, task::TaskTracker};

use super::core::{
    ResponseBodyEventSink, TunnelRequestParts, TunnelRequestTransport, TunnelServerApp,
    WEBTRANSPORT_STREAM_HEADER_TIMEOUT, forward_tunnel_request, next_body_len, request_body_buffer,
    serve_bidi_streams,
};
use super::http3::h3_error;

pub(super) async fn handle_webtransport_connection(
    incoming: quinn::Incoming,
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
    app: TunnelServerApp,
) -> Result<()> {
    let connection = incoming.await.context("accept quic connection failed")?;
    handle_webtransport_established_connection(connection, shutdown, task_tracker, app).await
}

async fn handle_webtransport_established_connection(
    connection: quinn::Connection,
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
    app: TunnelServerApp,
) -> Result<()> {
    let mut builder = h3::server::builder();
    builder
        .enable_webtransport(true)
        .enable_extended_connect(true)
        .enable_datagram(true)
        .max_webtransport_sessions(1);
    let mut h3_connection: h3::server::Connection<h3_quinn::Connection, bytes::Bytes> = builder
        .build(h3_quinn::Connection::new(connection.clone()))
        .await
        .map_err(h3_error)
        .context("create WebTransport h3 server")?;
    let Some(resolver) = h3_connection
        .accept()
        .await
        .map_err(h3_error)
        .context("accept WebTransport CONNECT")?
    else {
        return Ok(());
    };
    let (request, mut connect_stream) = resolver
        .resolve_request()
        .await
        .map_err(h3_error)
        .context("resolve WebTransport CONNECT")?;
    let is_webtransport = request
        .extensions()
        .get::<h3::ext::Protocol>()
        .is_some_and(|protocol| *protocol == h3::ext::Protocol::WEB_TRANSPORT);
    if request.method() != reqwest::Method::CONNECT
        || request.uri().path() != WEBTRANSPORT_TUNNEL_PATH
        || !is_webtransport
    {
        let response = http::Response::builder()
            .status(reqwest::StatusCode::BAD_REQUEST.as_u16())
            .body(())
            .context("build WebTransport rejection")?;
        connect_stream
            .send_response(response)
            .await
            .map_err(h3_error)
            .context("send WebTransport rejection")?;
        connect_stream
            .finish()
            .await
            .map_err(h3_error)
            .context("finish WebTransport rejection")?;
        bail!("invalid WebTransport CONNECT request");
    }
    let session_id = connect_stream.id().into_inner();
    let response = http::Response::builder()
        .status(reqwest::StatusCode::OK.as_u16())
        .body(())
        .context("build WebTransport CONNECT response")?;
    connect_stream
        .send_response(response)
        .await
        .map_err(h3_error)
        .context("send WebTransport CONNECT response")?;

    serve_bidi_streams(
        (),
        app,
        connection,
        shutdown,
        task_tracker,
        move |send, recv, app| handle_webtransport_stream(send, recv, session_id, app),
        |error| tracing::warn!(%error, "WebTransport tunnel stream failed"),
    )
    .await;
    // Keep the CONNECT stream alive for the duration of the WebTransport loop.
    drop(connect_stream);
    Ok(())
}

struct WebTransportTunnelTransport<'a> {
    recv_stream: &'a mut quinn::RecvStream,
    send_stream: &'a mut quinn::SendStream,
}

impl ResponseBodyEventSink for WebTransportTunnelTransport<'_> {
    async fn send_body_event(&mut self, event: bytes::Bytes) -> Result<()> {
        stargate_protocol::write_webtransport_http_body(self.send_stream, event)
            .await
            .context("failed to send WebTransport response body event")
    }
}

impl TunnelRequestTransport for WebTransportTunnelTransport<'_> {
    async fn read_request_body(
        &mut self,
        request_headers: &HeaderMap,
        max_request_body_bytes: usize,
    ) -> Result<Vec<u8>> {
        read_webtransport_request_body(self.recv_stream, request_headers, max_request_body_bytes)
            .await
    }

    async fn send_response_head(
        &mut self,
        status: reqwest::StatusCode,
        headers: HeaderMap,
    ) -> Result<()> {
        stargate_protocol::write_webtransport_http_response_head(
            self.send_stream,
            &webtransport_response_head(status, headers),
        )
        .await
        .context("failed to send WebTransport response head")
    }

    async fn finish_response(&mut self) -> Result<()> {
        stargate_protocol::finish_webtransport_http_stream(self.send_stream)
            .context("failed to finish WebTransport response stream")
    }
}

async fn handle_webtransport_http_stream(
    mut quinn_send: quinn::SendStream,
    mut quinn_recv: quinn::RecvStream,
    app: &TunnelServerApp,
) -> Result<()> {
    let request_head = stargate_protocol::read_webtransport_http_request_head(&mut quinn_recv)
        .await
        .context("failed to read WebTransport request head")?;
    let method: reqwest::Method = request_head
        .method
        .as_str()
        .parse()
        .context("invalid WebTransport request method")?;
    let path_and_query = request_head.path_and_query;
    let request_headers = request_head.headers;
    let mut transport = WebTransportTunnelTransport {
        recv_stream: &mut quinn_recv,
        send_stream: &mut quinn_send,
    };
    forward_tunnel_request(
        app,
        TunnelRequestParts {
            method,
            path_and_query,
            headers: request_headers,
        },
        &mut transport,
    )
    .await
}

pub(super) async fn handle_webtransport_stream(
    mut quinn_send: quinn::SendStream,
    mut quinn_recv: quinn::RecvStream,
    expected_session_id: u64,
    app: TunnelServerApp,
) -> Result<()> {
    #[cfg(test)]
    if let Some(tx) = &app.webtransport_stream_header_wait_tx {
        let _ = tx.try_send(());
    }
    let stream_session_id = match tokio::time::timeout(
        WEBTRANSPORT_STREAM_HEADER_TIMEOUT,
        stargate_protocol::read_webtransport_bidi_header(&mut quinn_recv),
    )
    .await
    {
        Ok(Ok(session_id)) => session_id,
        Ok(Err(error)) => {
            reset_webtransport_stream(&mut quinn_send, &mut quinn_recv);
            return Err(error).context("invalid WebTransport stream header");
        }
        Err(_) => {
            reset_webtransport_stream(&mut quinn_send, &mut quinn_recv);
            bail!("timed out waiting for WebTransport stream header");
        }
    };
    if stream_session_id != expected_session_id {
        reset_webtransport_stream(&mut quinn_send, &mut quinn_recv);
        bail!(
            "WebTransport stream session id mismatch: got {stream_session_id}, expected {expected_session_id}"
        );
    }

    handle_webtransport_http_stream(quinn_send, quinn_recv, &app).await
}

fn reset_webtransport_stream(
    quinn_send: &mut quinn::SendStream,
    quinn_recv: &mut quinn::RecvStream,
) {
    let _ = quinn_send.reset(0u32.into());
    let _ = quinn_recv.stop(0u32.into());
}

async fn read_webtransport_request_body(
    recv_stream: &mut quinn::RecvStream,
    request_headers: &HeaderMap,
    max_request_body_bytes: usize,
) -> Result<Vec<u8>> {
    let mut body_bytes = request_body_buffer(request_headers, max_request_body_bytes)?;
    let mut total_body = 0usize;
    while let Some(chunk) = stargate_protocol::read_webtransport_http_body_chunk(recv_stream)
        .await
        .context("failed to read WebTransport request body")?
    {
        total_body = next_body_len(total_body, chunk.len(), max_request_body_bytes)?;
        body_bytes.extend_from_slice(&chunk);
    }
    Ok(body_bytes)
}

fn webtransport_response_head(
    status: reqwest::StatusCode,
    headers: HeaderMap,
) -> stargate_protocol::WebTransportHttpResponseHead {
    stargate_protocol::WebTransportHttpResponseHead { status, headers }
}

#[cfg(test)]
mod tests {
    use super::*;
    use reqwest::header::HeaderValue;
    use stargate_protocol::tunnel_contract::{
        HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE,
    };

    use super::super::core::{PylonRetryConfig, build_response_headers};

    fn header_value<'a>(headers: &'a HeaderMap, name: &str) -> &'a str {
        headers
            .get(name)
            .and_then(|value| value.to_str().ok())
            .expect("header should be present")
    }

    #[test]
    fn webtransport_success_head_keeps_retry_metadata() {
        let retry = PylonRetryConfig::default();
        let mut upstream_headers = HeaderMap::new();
        upstream_headers.insert(
            retry.upstream_retry_header.clone(),
            HeaderValue::from_static("true"),
        );
        upstream_headers.insert(reqwest::header::RETRY_AFTER, HeaderValue::from_static("2"));

        let status = reqwest::StatusCode::TOO_MANY_REQUESTS;
        let headers = build_response_headers(status, &upstream_headers, &retry, None, "inst-a")
            .expect("WebTransport success headers should build");
        let head = webtransport_response_head(status, headers);

        assert_eq!(head.status, reqwest::StatusCode::TOO_MANY_REQUESTS);
        assert_eq!(
            header_value(&head.headers, HEADER_STARGATE_RETRYABLE),
            "true"
        );
        assert_eq!(
            header_value(&head.headers, HEADER_STARGATE_RETRY_REASON),
            "upstream_admission_rejected"
        );
        assert_eq!(
            header_value(&head.headers, "x-stargate-retry-after-ms"),
            "2000"
        );
    }
}

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

use std::sync::Arc;

use anyhow::{Context, Result, bail};
use axum::http::{Method, StatusCode};
use bytes::Bytes;
use quinn::Connection;
use stargate_protocol::{WebTransportHttpRequestHead, tunnel_contract::WEBTRANSPORT_TUNNEL_PATH};

use super::body::{OpenStreamingRequest, OpenStreamingRequestInner};
use super::http3::{
    H3ClientConnection, H3ClientRequestStream, H3ServerConnection, H3ServerRequestStream, h3_error,
};
use super::request::OpenTunnelRequest;

#[derive(Clone)]
pub(super) struct WebTransportConnectionHandle {
    bidi_header: Bytes,
    lifetime: Arc<WebTransportConnectionLifetime>,
}

enum WebTransportH3Lifetime {
    Client {
        _connection: Box<H3ClientConnection>,
        _stream: Box<H3ClientRequestStream>,
    },
    Server {
        _connection: Box<H3ServerConnection>,
        _stream: Box<H3ServerRequestStream>,
    },
}

struct WebTransportConnectionLifetime {
    connection: Connection,
    _h3: tokio::sync::Mutex<WebTransportH3Lifetime>,
}

impl Drop for WebTransportConnectionLifetime {
    fn drop(&mut self) {
        // The WebTransport session is valid only while its CONNECT stream is
        // alive. Closing QUIC here makes the lifetime boundary explicit when
        // the last pooled handle drops.
        self.connection.close(0u32.into(), b"webtransport dropped");
    }
}

impl WebTransportConnectionHandle {
    fn new(connection: Connection, session_id: u64, h3: WebTransportH3Lifetime) -> Result<Self> {
        Ok(Self {
            bidi_header: stargate_protocol::WebTransportBidiHeader::new(session_id)
                .context("precompute WebTransport bidi stream header")?
                .to_bytes(),
            lifetime: Arc::new(WebTransportConnectionLifetime {
                connection,
                _h3: tokio::sync::Mutex::new(h3),
            }),
        })
    }

    pub(super) fn is_healthy(&self) -> bool {
        self.lifetime.connection.close_reason().is_none()
    }

    pub(super) fn stable_id(&self) -> usize {
        self.lifetime.connection.stable_id()
    }

    #[cfg(test)]
    pub(super) fn connection(&self) -> &Connection {
        &self.lifetime.connection
    }

    pub(super) async fn open_streaming_request(
        self,
        request: OpenTunnelRequest<'_>,
    ) -> Result<OpenStreamingRequest> {
        let (mut send_stream, recv_stream) = self
            .lifetime
            .connection
            .open_bi()
            .await
            .context("open WebTransport bi stream failed")?;

        let request_head = WebTransportHttpRequestHead {
            method: request.method.clone(),
            path_and_query: request.path_and_query.to_string(),
            headers: request.headers.clone(),
        };
        stargate_protocol::write_webtransport_http_request_head_after_prefix(
            &mut send_stream,
            self.bidi_header.clone(),
            &request_head,
        )
        .await
        .context("failed to send WebTransport request head")?;

        let response_header_timeout = request.response_header_timeout();
        Ok(OpenStreamingRequest {
            inner: OpenStreamingRequestInner::WebTransport {
                send_stream,
                recv_stream,
                connection_handle: self,
            },
            response_header_timeout,
        })
    }
}

pub(super) async fn build_webtransport_client_connection(
    connection: Connection,
) -> Result<WebTransportConnectionHandle> {
    let mut builder = h3::client::builder();
    builder.enable_extended_connect(true).enable_datagram(true);
    let (h3_connection, mut send_request): (
        H3ClientConnection,
        h3::client::SendRequest<h3_quinn::OpenStreams, bytes::Bytes>,
    ) = builder
        .build(h3_quinn::Connection::new(connection.clone()))
        .await
        .map_err(h3_error)
        .context("create WebTransport h3 client connection")?;

    let mut request: http::Request<()> = http::Request::builder()
        .method(Method::CONNECT)
        .uri(format!("https://stargate{WEBTRANSPORT_TUNNEL_PATH}"))
        .body(())
        .context("build WebTransport CONNECT request")?;
    request
        .extensions_mut()
        .insert(h3::ext::Protocol::WEB_TRANSPORT);

    let mut connect_stream = send_request
        .send_request(request)
        .await
        .map_err(h3_error)
        .context("send WebTransport CONNECT request")?;
    let session_id = connect_stream.id().into_inner();
    connect_stream
        .finish()
        .await
        .map_err(h3_error)
        .context("finish WebTransport CONNECT request")?;
    let response = connect_stream
        .recv_response()
        .await
        .map_err(h3_error)
        .context("receive WebTransport CONNECT response")?;
    if !response.status().is_success() {
        bail!(
            "WebTransport CONNECT rejected with status {}",
            response.status()
        );
    }

    WebTransportConnectionHandle::new(
        connection,
        session_id,
        WebTransportH3Lifetime::Client {
            _connection: Box::new(h3_connection),
            _stream: Box::new(connect_stream),
        },
    )
}

pub(super) async fn build_webtransport_server_connection(
    connection: Connection,
    h3_connection: H3ServerConnection,
    mut connect_stream: H3ServerRequestStream,
) -> Result<WebTransportConnectionHandle> {
    let session_id = connect_stream.id().into_inner();
    let response = http::Response::builder()
        .status(StatusCode::OK)
        .body(())
        .context("build WebTransport CONNECT response")?;
    connect_stream
        .send_response(response)
        .await
        .map_err(h3_error)
        .context("send WebTransport CONNECT response")?;

    WebTransportConnectionHandle::new(
        connection,
        session_id,
        WebTransportH3Lifetime::Server {
            _connection: Box::new(h3_connection),
            _stream: Box::new(connect_stream),
        },
    )
}

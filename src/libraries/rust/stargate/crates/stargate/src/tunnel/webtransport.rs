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
use stargate_protocol::tunnel_contract::WEBTRANSPORT_TUNNEL_PATH;

use super::direct::QuicHttpProxy;
use super::direct::TunnelConnection;
use super::http3::{
    H3ClientConnection, H3ClientRequestStream, H3ServerConnection, H3ServerRequestStream, h3_error,
};

#[derive(Clone)]
pub(super) struct WebTransportConnectionHandle {
    pub(super) connection: Connection,
    pub(super) bidi_header: Bytes,
    _lifetime: Arc<WebTransportConnectionLifetime>,
}

enum WebTransportH3Connection {
    Client {
        _connection: Box<H3ClientConnection>,
    },
    Server {
        _connection: Box<H3ServerConnection>,
    },
}

enum WebTransportConnectStream {
    Client { _stream: Box<H3ClientRequestStream> },
    Server { _stream: Box<H3ServerRequestStream> },
}

struct WebTransportConnectionLifetime {
    connection: Connection,
    _h3_connection: tokio::sync::Mutex<Option<WebTransportH3Connection>>,
    _connect_stream: tokio::sync::Mutex<Option<WebTransportConnectStream>>,
}

impl Drop for WebTransportConnectionLifetime {
    fn drop(&mut self) {
        // The WebTransport session is valid only while its CONNECT stream is
        // alive. Closing QUIC here makes the lifetime boundary explicit when
        // the last pooled handle drops.
        self.connection.close(0u32.into(), b"webtransport dropped");
    }
}

impl QuicHttpProxy {
    pub(super) async fn build_webtransport_client_connection(
        &self,
        connection: Connection,
    ) -> Result<TunnelConnection> {
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

        Ok(TunnelConnection::WebTransport(
            WebTransportConnectionHandle {
                connection: connection.clone(),
                bidi_header: stargate_protocol::WebTransportBidiHeader::new(session_id)
                    .context("precompute WebTransport bidi stream header")?
                    .to_bytes(),
                _lifetime: Arc::new(WebTransportConnectionLifetime {
                    connection,
                    _h3_connection: tokio::sync::Mutex::new(Some(
                        WebTransportH3Connection::Client {
                            _connection: Box::new(h3_connection),
                        },
                    )),
                    _connect_stream: tokio::sync::Mutex::new(Some(
                        WebTransportConnectStream::Client {
                            _stream: Box::new(connect_stream),
                        },
                    )),
                }),
            },
        ))
    }

    pub(super) async fn build_webtransport_server_connection(
        &self,
        connection: Connection,
        h3_connection: H3ServerConnection,
        mut connect_stream: H3ServerRequestStream,
    ) -> Result<TunnelConnection> {
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

        Ok(TunnelConnection::WebTransport(
            WebTransportConnectionHandle {
                connection: connection.clone(),
                bidi_header: stargate_protocol::WebTransportBidiHeader::new(session_id)
                    .context("precompute WebTransport bidi stream header")?
                    .to_bytes(),
                _lifetime: Arc::new(WebTransportConnectionLifetime {
                    connection,
                    _h3_connection: tokio::sync::Mutex::new(Some(
                        WebTransportH3Connection::Server {
                            _connection: Box::new(h3_connection),
                        },
                    )),
                    _connect_stream: tokio::sync::Mutex::new(Some(
                        WebTransportConnectStream::Server {
                            _stream: Box::new(connect_stream),
                        },
                    )),
                }),
            },
        ))
    }
}

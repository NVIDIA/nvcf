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
use axum::http::{Method, StatusCode};
use quinn::Connection;
use tokio_util::task::TaskTracker;
use tracing::{info, warn};

use stargate_protocol::tunnel_contract::{
    HEADER_INFERENCE_SERVER_ID, HEADER_REVERSE_AUTH_TOKEN, WEBTRANSPORT_TUNNEL_PATH,
};

use crate::routing_state::StargateState;
use crate::tunnel::QuicHttpProxy;

use super::super::connection::TunnelConnection;
use super::super::http3::{H3ServerRequestStream, h3_error};
use super::super::webtransport::build_webtransport_server_connection;
use super::{ReverseConnectionCleanupKind, spawn_reverse_connection_cleanup};

pub(super) async fn handle_reverse_webtransport_connect(
    connection: Connection,
    proxy: &QuicHttpProxy,
    state: &StargateState,
    task_tracker: &TaskTracker,
) -> Result<()> {
    let mut h3_connection = h3::server::builder()
        .enable_webtransport(true)
        .enable_extended_connect(true)
        .enable_datagram(true)
        .max_webtransport_sessions(1)
        .build(h3_quinn::Connection::new(connection.clone()))
        .await
        .map_err(h3_error)
        .context("create reverse WebTransport h3 server")?;
    let Some(resolver) = h3_connection
        .accept()
        .await
        .map_err(h3_error)
        .context("accept reverse WebTransport CONNECT")?
    else {
        bail!("reverse WebTransport connection closed before CONNECT");
    };
    let (request, mut stream) = resolver
        .resolve_request()
        .await
        .map_err(h3_error)
        .context("resolve reverse WebTransport CONNECT")?;

    let is_webtransport = request
        .extensions()
        .get::<h3::ext::Protocol>()
        .is_some_and(|protocol| *protocol == h3::ext::Protocol::WEB_TRANSPORT);
    if request.method() != Method::CONNECT
        || request.uri().path() != WEBTRANSPORT_TUNNEL_PATH
        || !is_webtransport
    {
        send_webtransport_connect_response(&mut stream, StatusCode::BAD_REQUEST).await?;
        bail!("invalid reverse WebTransport CONNECT request");
    }

    let Some(inference_server_id) = request
        .headers()
        .get(HEADER_INFERENCE_SERVER_ID)
        .and_then(|value| value.to_str().ok())
        .filter(|value| !value.is_empty())
        .map(ToOwned::to_owned)
    else {
        send_webtransport_connect_response(&mut stream, StatusCode::BAD_REQUEST).await?;
        bail!("reverse WebTransport CONNECT missing {HEADER_INFERENCE_SERVER_ID}");
    };
    let auth_token = request
        .headers()
        .get(HEADER_REVERSE_AUTH_TOKEN)
        .and_then(|value| value.to_str().ok())
        .map(ToOwned::to_owned);

    let result = match proxy
        .authenticator
        .authenticate(auth_token.as_deref())
        .await
    {
        Ok(result) => result,
        Err(error) => {
            warn!(
                inference_server_id = %inference_server_id,
                error = %error,
                "reverse WebTransport authentication failed"
            );
            send_webtransport_connect_response(&mut stream, StatusCode::UNAUTHORIZED).await?;
            bail!("authentication failed for reverse WebTransport: {inference_server_id}");
        }
    };

    let Some(registration) = state.reverse_tunnel_registration(&inference_server_id) else {
        send_webtransport_connect_response(&mut stream, StatusCode::NOT_FOUND).await?;
        bail!("unauthorized inference_server_id in reverse WebTransport: {inference_server_id}");
    };

    if result.routing_key.as_deref() != registration.routing_key().as_deref() {
        send_webtransport_connect_response(&mut stream, StatusCode::FORBIDDEN).await?;
        bail!("QUIC routing_key does not match gRPC registration: {inference_server_id}");
    }

    let Some(install) = registration.begin_reverse_connection_install() else {
        send_webtransport_connect_response(&mut stream, StatusCode::CONFLICT).await?;
        bail!("pending duplicate reverse WebTransport connection for: {inference_server_id}");
    };

    let tunnel_connection = TunnelConnection::WebTransport(
        build_webtransport_server_connection(connection.clone(), h3_connection, stream).await?,
    );
    if !install.finish(tunnel_connection) {
        bail!("duplicate reverse WebTransport connection for: {inference_server_id}");
    }

    info!(inference_server_id = %inference_server_id, "reverse WebTransport tunnel established");
    spawn_reverse_connection_cleanup(
        task_tracker,
        registration,
        connection,
        ReverseConnectionCleanupKind::WebTransport,
    );

    Ok(())
}

async fn send_webtransport_connect_response(
    stream: &mut H3ServerRequestStream,
    status: StatusCode,
) -> Result<()> {
    let response = http::Response::builder()
        .status(status)
        .body(())
        .context("build WebTransport CONNECT rejection")?;
    stream
        .send_response(response)
        .await
        .map_err(h3_error)
        .context("send WebTransport CONNECT response")?;
    stream
        .finish()
        .await
        .map_err(h3_error)
        .context("finish WebTransport CONNECT response")
}

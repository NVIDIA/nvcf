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

use std::time::Duration;

use anyhow::{Context, Result, bail};
use quinn::{Connection, SendStream};
use tokio_util::task::TaskTracker;
use tracing::{info, warn};

use crate::routing_state::StargateState;
use crate::tunnel::QuicHttpProxy;

use super::{ReverseConnectionCleanupKind, spawn_reverse_connection_cleanup};

pub(super) async fn handle_reverse_handshake(
    connection: Connection,
    proxy: &QuicHttpProxy,
    state: &StargateState,
    task_tracker: &TaskTracker,
) -> Result<()> {
    let (mut quinn_send, mut quinn_recv) = connection
        .accept_bi()
        .await
        .context("accept handshake stream")?;

    let handshake = stargate_protocol::read_handshake(&mut quinn_recv)
        .await
        .context("read handshake message")?;
    let inference_server_id = handshake.inference_server_id;

    if inference_server_id.is_empty() {
        send_handshake_nack(
            &mut quinn_send,
            "empty inference_server_id",
            proxy.config.connect_timeout,
        )
        .await?;
        bail!("empty inference_server_id in reverse handshake");
    }

    let result = match proxy
        .authenticator
        .authenticate(handshake.auth_token.as_deref())
        .await
    {
        Ok(result) => result,
        Err(e) => {
            warn!(
                inference_server_id = %inference_server_id,
                error = %e,
                "reverse handshake authentication failed"
            );
            send_handshake_nack(
                &mut quinn_send,
                "authentication failed",
                proxy.config.connect_timeout,
            )
            .await?;
            bail!("authentication failed for reverse handshake: {inference_server_id}");
        }
    };

    info!(
        inference_server_id = %inference_server_id,
        routing_key = ?result.routing_key,
        "reverse handshake authenticated"
    );

    let Some(registration) = state.reverse_tunnel_registration(&inference_server_id) else {
        warn!(
            inference_server_id = %inference_server_id,
            "reverse handshake NACK: unauthorized inference_server_id"
        );
        send_handshake_nack(
            &mut quinn_send,
            "unauthorized inference_server_id",
            proxy.config.connect_timeout,
        )
        .await?;
        bail!("unauthorized inference_server_id in reverse handshake: {inference_server_id}");
    };

    if result.routing_key.as_deref() != registration.routing_key().as_deref() {
        warn!(
            inference_server_id = %inference_server_id,
            quic_routing_key = ?result.routing_key,
            stored_routing_key = ?registration.routing_key(),
            "reverse handshake NACK: routing key mismatch"
        );
        send_handshake_nack(
            &mut quinn_send,
            "routing key mismatch",
            proxy.config.connect_timeout,
        )
        .await?;
        bail!("QUIC routing_key does not match gRPC registration: {inference_server_id}");
    }

    if !proxy
        .store_reverse_connection(registration.clone(), connection.clone())
        .await
    {
        warn!(
            inference_server_id = %inference_server_id,
            "reverse handshake NACK: duplicate connection"
        );
        send_handshake_nack(
            &mut quinn_send,
            "duplicate connection",
            proxy.config.connect_timeout,
        )
        .await?;
        bail!("duplicate reverse tunnel connection for: {inference_server_id}");
    }
    let ack = stargate_protocol::HandshakeAck {
        accepted: true,
        reason: String::new(),
    };
    stargate_protocol::write_handshake_ack(&mut quinn_send, &ack)
        .await
        .context("send ACK")?;
    quinn_send.finish().context("finish ACK stream")?;

    info!(inference_server_id = %inference_server_id, "reverse tunnel connection established");
    spawn_reverse_connection_cleanup(
        task_tracker,
        registration,
        connection,
        ReverseConnectionCleanupKind::Tunnel,
    );

    Ok(())
}

async fn send_handshake_nack(
    send: &mut SendStream,
    reason: &str,
    delivery_timeout: Duration,
) -> Result<()> {
    let ack = stargate_protocol::HandshakeAck {
        accepted: false,
        reason: reason.to_string(),
    };
    stargate_protocol::write_handshake_ack(send, &ack)
        .await
        .context("send handshake NACK")?;
    send.finish().context("finish NACK stream")?;
    // finish() marks the stream as done but does not wait for QUIC to
    // deliver the bytes. The caller bail!s after this function returns,
    // which drops the Connection and tears down the transport before the
    // NACK reaches the client. stopped() blocks until the peer has
    // consumed the stream, ensuring the rejection reason is delivered.
    match tokio::time::timeout(delivery_timeout, send.stopped()).await {
        Ok(result) => {
            result?;
        }
        Err(_) => {
            warn!(
                timeout_ms = delivery_timeout.as_millis(),
                "timed out waiting for reverse handshake NACK delivery"
            );
        }
    }
    Ok(())
}

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

use std::future::Future;
use std::net::IpAddr;
use std::sync::Arc;
use std::time::Duration;

use tokio::sync::watch;
use tokio_util::sync::CancellationToken;

use stargate_proto::pb::InferenceServerAck;

use crate::quic_http_tunnel::{
    ReverseQuicTunnelConfig, ReverseQuicTunnelHandle, TunnelError, start_reverse_quic_tunnel,
};

use super::REVERSE_TUNNEL_CONNECT_TIMEOUT;
use super::types::RegistrationSessionConfig;

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) struct ReverseTunnelEndpoint {
    pub(super) routing_target_addr: String,
    pub(super) pylon_dial_addr: String,
    pub(super) sni_override: Option<String>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(super) enum ReverseTunnelState {
    #[default]
    AwaitingEndpoint,
    Disconnected(ReverseTunnelEndpoint),
    Connected(ReverseTunnelEndpoint),
}

impl ReverseTunnelState {
    pub(super) fn endpoint(&self) -> Option<&ReverseTunnelEndpoint> {
        match self {
            Self::AwaitingEndpoint => None,
            Self::Disconnected(endpoint) | Self::Connected(endpoint) => Some(endpoint),
        }
    }

    pub(super) fn is_connected(&self) -> bool {
        matches!(self, Self::Connected(_))
    }

    pub(super) fn replace_endpoint(&mut self, endpoint: Option<ReverseTunnelEndpoint>) -> bool {
        if self.endpoint() == endpoint.as_ref() {
            return false;
        }
        *self = endpoint.map_or(Self::AwaitingEndpoint, Self::Disconnected);
        true
    }

    pub(super) fn mark_connected(&mut self, endpoint: &ReverseTunnelEndpoint) -> bool {
        if matches!(self, Self::Disconnected(current) if current == endpoint) {
            *self = Self::Connected(endpoint.clone());
            true
        } else {
            false
        }
    }

    fn mark_disconnected(&mut self, endpoint: &ReverseTunnelEndpoint) -> bool {
        if matches!(self, Self::Connected(current) if current == endpoint) {
            *self = Self::Disconnected(endpoint.clone());
            true
        } else {
            false
        }
    }
}

pub(super) fn reverse_tunnel_endpoint_from_ack(
    ack: &InferenceServerAck,
) -> Option<ReverseTunnelEndpoint> {
    let routing_target_addr = ack.reverse_tunnel_target.trim();
    let pylon_dial_addr = ack.reverse_tunnel_pylon_dial_addr.trim();
    if routing_target_addr.is_empty() || pylon_dial_addr.is_empty() {
        return None;
    }

    Some(ReverseTunnelEndpoint {
        routing_target_addr: routing_target_addr.to_string(),
        pylon_dial_addr: pylon_dial_addr.to_string(),
        sni_override: (pylon_dial_addr != routing_target_addr)
            .then(|| reverse_tunnel_sni_from_routing_target(routing_target_addr)),
    })
}

pub(super) fn reverse_tunnel_sni_from_routing_target(routing_target_addr: &str) -> String {
    let host = routing_target_addr
        .strip_prefix('[')
        .and_then(|rest| rest.split_once(']').map(|(host, _)| host))
        .or_else(|| routing_target_addr.rsplit_once(':').map(|(host, _)| host))
        .unwrap_or(routing_target_addr);
    if host == "localhost" || host.parse::<IpAddr>().is_ok() {
        "stargate".to_string()
    } else {
        host.to_string()
    }
}

pub(super) async fn reverse_tunnel_connect_with_timeout(
    connect_timeout: Duration,
    connect_attempt: impl Future<Output = Result<ReverseQuicTunnelHandle, TunnelError>>,
) -> Result<ReverseQuicTunnelHandle, TunnelError> {
    tokio::time::timeout(connect_timeout, connect_attempt)
        .await
        .map_err(|_| TunnelError::ConnectTimeout {
            timeout_ms: connect_timeout.as_millis(),
        })?
}

async fn wait_for_reverse_tunnel_retry(
    state_rx: &mut watch::Receiver<ReverseTunnelState>,
    stop: &CancellationToken,
    backoff: &mut Duration,
) -> bool {
    tokio::select! {
        _ = stop.cancelled() => false,
        changed = state_rx.changed() => {
            if changed.is_ok() {
                *backoff = Duration::from_secs(1);
                true
            } else {
                false
            }
        }
        _ = tokio::time::sleep(*backoff) => {
            *backoff = (*backoff * 2).min(Duration::from_secs(30));
            true
        }
    }
}

pub(super) fn reverse_quic_tunnel_config(
    endpoint: &ReverseTunnelEndpoint,
    config: &RegistrationSessionConfig,
) -> ReverseQuicTunnelConfig {
    ReverseQuicTunnelConfig {
        target_addr: endpoint.pylon_dial_addr.clone(),
        inference_server_id: config.inference_server_id.clone(),
        upstream_http_base_url: config.inference_server_url.clone(),
        forwarding: config.forwarding.clone(),
        tls_cert_pem: config.tls_cert_pem.clone(),
        quic_insecure: config.quic_insecure,
        tunnel_protocol: config.tunnel_protocol,
        sni_override: endpoint.sni_override.clone(),
        auth_token_provider: config.auth_token_provider.clone(),
    }
}

/// Maintains a single reverse QUIC tunnel connection to a stargate router.
pub(super) async fn run_reverse_tunnel_loop(
    router_addr: String,
    config: Arc<RegistrationSessionConfig>,
    state_tx: watch::Sender<ReverseTunnelState>,
    stop: CancellationToken,
) {
    let mut state_rx = state_tx.subscribe();
    let mut backoff = Duration::from_secs(1);

    while !stop.is_cancelled() {
        let endpoint = state_rx.borrow_and_update().endpoint().cloned();
        let Some(endpoint) = endpoint else {
            tokio::select! {
                _ = stop.cancelled() => return,
                changed = state_rx.changed() => {
                    if changed.is_err() {
                        return;
                    }
                }
            }
            continue;
        };

        let tunnel_config = reverse_quic_tunnel_config(&endpoint, &config);
        let connect_result = tokio::select! {
            _ = stop.cancelled() => return,
            changed = state_rx.changed() => {
                if changed.is_err() {
                    return;
                }
                backoff = Duration::from_secs(1);
                continue;
            }
            result = reverse_tunnel_connect_with_timeout(
                REVERSE_TUNNEL_CONNECT_TIMEOUT,
                start_reverse_quic_tunnel(tunnel_config),
            ) => result,
        };
        match connect_result {
            Ok(handle) => {
                if !state_tx.send_if_modified(|state| state.mark_connected(&endpoint))
                    || !matches!(
                        &*state_rx.borrow_and_update(),
                        ReverseTunnelState::Connected(current) if current == &endpoint
                    )
                {
                    handle.shutdown().await;
                    backoff = Duration::from_secs(1);
                    continue;
                }
                tracing::info!(
                    router_addr = %router_addr,
                    dial_addr = %endpoint.pylon_dial_addr,
                    routing_target_addr = %endpoint.routing_target_addr,
                    inference_server_id = %config.inference_server_id,
                    "reverse tunnel connected"
                );
                let connected_at = tokio::time::Instant::now();

                tokio::select! {
                    _ = stop.cancelled() => {
                        handle.shutdown().await;
                        state_tx.send_if_modified(|state| state.mark_disconnected(&endpoint));
                        return;
                    }
                    changed = state_rx.changed() => {
                        handle.shutdown().await;
                        if changed.is_err() {
                            return;
                        }
                        state_tx.send_if_modified(|state| state.mark_disconnected(&endpoint));
                        backoff = Duration::from_secs(1);
                    }
                    _ = handle.closed() => {
                        tracing::warn!(
                            router_addr = %router_addr,
                            dial_addr = %endpoint.pylon_dial_addr,
                            routing_target_addr = %endpoint.routing_target_addr,
                            inference_server_id = %config.inference_server_id,
                            backoff_ms = backoff.as_millis(),
                            "reverse tunnel connection dropped, reconnecting"
                        );
                        let disconnected = state_tx
                            .send_if_modified(|state| state.mark_disconnected(&endpoint));
                        if disconnected
                            && state_rx.borrow_and_update().endpoint() != Some(&endpoint)
                        {
                            backoff = Duration::from_secs(1);
                            continue;
                        }
                        if connected_at.elapsed() > Duration::from_secs(60) {
                            backoff = Duration::from_secs(1);
                        }
                        if !wait_for_reverse_tunnel_retry(&mut state_rx, &stop, &mut backoff).await {
                            return;
                        }
                    }
                }
            }
            Err(error) => {
                tracing::warn!(
                    router_addr = %router_addr,
                    dial_addr = %endpoint.pylon_dial_addr,
                    routing_target_addr = %endpoint.routing_target_addr,
                    inference_server_id = %config.inference_server_id,
                    error = %error,
                    backoff_ms = backoff.as_millis(),
                    "reverse tunnel connect failed, retrying"
                );
                if !wait_for_reverse_tunnel_retry(&mut state_rx, &stop, &mut backoff).await {
                    return;
                }
            }
        }
    }
}

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
use tokio::task::JoinHandle;
use tokio_util::sync::CancellationToken;

use stargate_auth::AuthTokenProvider;
use stargate_proto::pb::InferenceServerAck;
use stargate_protocol::TunnelTransportProtocol;

use crate::quic_http_tunnel::{
    ReverseQuicTunnelConfig, ReverseQuicTunnelHandle, TunnelError, TunnelForwardingConfig,
    start_reverse_quic_tunnel,
};

use super::{
    NamedJoinHandle, REGISTRATION_TASK_SHUTDOWN_TIMEOUT, REVERSE_TUNNEL_CONNECT_TIMEOUT,
    await_named_join_handle, registration_should_stop, sleep_until_registration_stop,
};

#[derive(Debug, Clone)]
pub(super) struct ReverseTunnelLoopConfig {
    pub(super) router_addr: String,
    pub(super) inference_server_id: String,
    pub(super) quic_insecure: bool,
    pub(super) tunnel_protocol: TunnelTransportProtocol,
    pub(super) forwarding: TunnelForwardingConfig,
    pub(super) auth_token_provider: Option<Arc<AuthTokenProvider>>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) struct ReverseTunnelEndpoint {
    pub(super) routing_target_addr: String,
    pub(super) pylon_dial_addr: String,
    pub(super) sni_override: Option<String>,
}

pub(super) struct ReverseQuicTunnelConfigParams {
    pub(super) dial_addr: String,
    pub(super) sni_override: Option<String>,
    pub(super) inference_server_id: String,
    pub(super) quic_insecure: bool,
    pub(super) tunnel_protocol: TunnelTransportProtocol,
    pub(super) forwarding: TunnelForwardingConfig,
    pub(super) auth_token_provider: Option<Arc<AuthTokenProvider>>,
}

pub(super) fn build_reverse_quic_tunnel_config(
    params: ReverseQuicTunnelConfigParams,
) -> ReverseQuicTunnelConfig {
    let forwarding = params.forwarding;
    let mut tunnel_config = ReverseQuicTunnelConfig::new(
        params.dial_addr,
        params.inference_server_id,
        forwarding.upstream_http_base_url.clone(),
    );
    tunnel_config.quic_insecure = params.quic_insecure;
    tunnel_config.tunnel_protocol = params.tunnel_protocol;
    tunnel_config.sni_override = params.sni_override;
    tunnel_config.max_request_body_bytes = forwarding.max_request_body_bytes;
    tunnel_config.first_output_timeout = forwarding.first_output_timeout;
    tunnel_config.output_chunk_timeout = forwarding.output_chunk_timeout;
    tunnel_config.output_token_parser_factory = forwarding.output_token_parser_factory;
    tunnel_config.request_observation_tx = forwarding.request_observation_tx;
    tunnel_config.request_quality_monitor = forwarding.request_quality_monitor;
    tunnel_config.retry = forwarding.retry;
    tunnel_config.queue_mismatch_retry = forwarding.queue_mismatch_retry;
    tunnel_config.queue_tracker = forwarding.queue_tracker;
    tunnel_config.metrics = forwarding.metrics;
    #[cfg(test)]
    {
        tunnel_config.webtransport_stream_header_wait_tx =
            forwarding.webtransport_stream_header_wait_tx;
    }
    tunnel_config.auth_token_provider = params.auth_token_provider;
    tunnel_config
}

pub(super) async fn stop_reverse_tunnel_task(
    cancel_token: CancellationToken,
    task: JoinHandle<()>,
) {
    cancel_token.cancel();
    await_named_join_handle(
        NamedJoinHandle::new("reverse tunnel registration worker", task),
        REGISTRATION_TASK_SHUTDOWN_TIMEOUT,
    )
    .await;
}

pub(super) fn reverse_tunnel_endpoint_from_ack(
    ack: &InferenceServerAck,
) -> Option<ReverseTunnelEndpoint> {
    let routing_target_addr = ack.reverse_tunnel_target.trim();
    if routing_target_addr.is_empty() {
        return None;
    }
    let pylon_dial_addr = ack.reverse_tunnel_pylon_dial_addr.trim();
    if pylon_dial_addr.is_empty() {
        return None;
    }
    if pylon_dial_addr == routing_target_addr {
        return Some(ReverseTunnelEndpoint {
            routing_target_addr: routing_target_addr.to_string(),
            pylon_dial_addr: routing_target_addr.to_string(),
            sni_override: None,
        });
    }

    Some(ReverseTunnelEndpoint {
        routing_target_addr: routing_target_addr.to_string(),
        pylon_dial_addr: pylon_dial_addr.to_string(),
        sni_override: Some(reverse_tunnel_sni_from_routing_target(routing_target_addr)),
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

pub(super) async fn reverse_tunnel_connect_with_timeout<F>(
    connect_timeout: Duration,
    connect_attempt: F,
) -> Result<ReverseQuicTunnelHandle, TunnelError>
where
    F: Future<Output = Result<ReverseQuicTunnelHandle, TunnelError>>,
{
    tokio::time::timeout(connect_timeout, connect_attempt)
        .await
        .map_err(|_| TunnelError::ConnectTimeout {
            timeout_ms: connect_timeout.as_millis(),
        })?
}

/// Maintains a single reverse QUIC tunnel connection to a stargate router.
pub(super) async fn run_reverse_tunnel_loop(
    config: ReverseTunnelLoopConfig,
    endpoint_rx: watch::Receiver<Option<ReverseTunnelEndpoint>>,
    connected_tx: watch::Sender<bool>,
    parent_stop_rx: watch::Receiver<bool>,
    local_stop_rx: watch::Receiver<bool>,
    cancel_token: CancellationToken,
) {
    let ReverseTunnelLoopConfig {
        router_addr,
        inference_server_id,
        quic_insecure,
        tunnel_protocol,
        forwarding,
        auth_token_provider,
    } = config;
    let reverse_tunnel_connect_timeout = REVERSE_TUNNEL_CONNECT_TIMEOUT;
    let mut endpoint_rx = endpoint_rx;
    let mut parent_stop_rx = parent_stop_rx;
    let mut local_stop_rx = local_stop_rx;
    let mut backoff = Duration::from_secs(1);
    const BACKOFF_MAX: Duration = Duration::from_secs(30);

    loop {
        if registration_should_stop(&parent_stop_rx, &local_stop_rx, &cancel_token) {
            let _ = connected_tx.send(false);
            return;
        }

        let endpoint = endpoint_rx.borrow().clone();
        let Some(endpoint) = endpoint else {
            let _ = connected_tx.send(false);
            tokio::select! {
                _ = cancel_token.cancelled() => return,
                changed = parent_stop_rx.changed() => {
                    if changed.is_err()
                        || registration_should_stop(&parent_stop_rx, &local_stop_rx, &cancel_token)
                    {
                        return;
                    }
                }
                changed = local_stop_rx.changed() => {
                    if changed.is_err()
                        || registration_should_stop(&parent_stop_rx, &local_stop_rx, &cancel_token)
                    {
                        return;
                    }
                }
                changed = endpoint_rx.changed() => {
                    if changed.is_err() {
                        return;
                    }
                }
                _ = tokio::time::sleep(Duration::from_millis(500)) => {}
            }
            continue;
        };

        let tunnel_config = build_reverse_quic_tunnel_config(ReverseQuicTunnelConfigParams {
            dial_addr: endpoint.pylon_dial_addr.clone(),
            sni_override: endpoint.sni_override.clone(),
            inference_server_id: inference_server_id.clone(),
            quic_insecure,
            tunnel_protocol,
            forwarding: forwarding.clone(),
            auth_token_provider: auth_token_provider.clone(),
        });
        let connect_result = tokio::select! {
            _ = cancel_token.cancelled() => {
                let _ = connected_tx.send(false);
                return;
            }
            changed = parent_stop_rx.changed() => {
                if changed.is_err()
                    || registration_should_stop(&parent_stop_rx, &local_stop_rx, &cancel_token)
                {
                    let _ = connected_tx.send(false);
                    return;
                }
                continue;
            }
            changed = local_stop_rx.changed() => {
                if changed.is_err()
                    || registration_should_stop(&parent_stop_rx, &local_stop_rx, &cancel_token)
                {
                    let _ = connected_tx.send(false);
                    return;
                }
                continue;
            }
            result = reverse_tunnel_connect_with_timeout(
                reverse_tunnel_connect_timeout,
                start_reverse_quic_tunnel(tunnel_config),
            ) => result,
        };
        match connect_result {
            Ok(handle) => {
                tracing::info!(
                    router_addr = %router_addr,
                    dial_addr = %endpoint.pylon_dial_addr,
                    routing_target_addr = %endpoint.routing_target_addr,
                    inference_server_id = %inference_server_id,
                    "reverse tunnel connected"
                );
                let _ = connected_tx.send(true);
                let connected_at = tokio::time::Instant::now();

                tokio::select! {
                    _ = cancel_token.cancelled() => {
                        handle.shutdown().await;
                        let _ = connected_tx.send(false);
                        return;
                    }
                    _ = parent_stop_rx.changed() => {
                        handle.shutdown().await;
                        let _ = connected_tx.send(false);
                        return;
                    }
                    _ = local_stop_rx.changed() => {
                        handle.shutdown().await;
                        let _ = connected_tx.send(false);
                        return;
                    }
                    _ = endpoint_rx.changed() => {
                        handle.shutdown().await;
                        let _ = connected_tx.send(false);
                        backoff = Duration::from_secs(1);
                    }
                    _ = handle.closed() => {
                        tracing::warn!(
                            router_addr = %router_addr,
                            dial_addr = %endpoint.pylon_dial_addr,
                            routing_target_addr = %endpoint.routing_target_addr,
                            inference_server_id = %inference_server_id,
                            backoff_ms = backoff.as_millis(),
                            "reverse tunnel connection dropped, reconnecting"
                        );
                        let _ = connected_tx.send(false);
                        if connected_at.elapsed() > Duration::from_secs(60) {
                            backoff = Duration::from_secs(1);
                        }
                        if sleep_until_registration_stop(
                            &mut parent_stop_rx,
                            &mut local_stop_rx,
                            &cancel_token,
                            backoff,
                        )
                        .await
                        {
                            return;
                        }
                        backoff = (backoff * 2).min(BACKOFF_MAX);
                    }
                }
            }
            Err(error) => {
                tracing::warn!(
                    router_addr = %router_addr,
                    dial_addr = %endpoint.pylon_dial_addr,
                    routing_target_addr = %endpoint.routing_target_addr,
                    inference_server_id = %inference_server_id,
                    error = %error,
                    backoff_ms = backoff.as_millis(),
                    "reverse tunnel connect failed, retrying"
                );
                let _ = connected_tx.send(false);
                if sleep_until_registration_stop(
                    &mut parent_stop_rx,
                    &mut local_stop_rx,
                    &cancel_token,
                    backoff,
                )
                .await
                {
                    return;
                }
                backoff = (backoff * 2).min(BACKOFF_MAX);
            }
        }
    }
}

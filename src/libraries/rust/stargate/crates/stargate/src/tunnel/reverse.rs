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
use std::time::Duration;

use anyhow::{Context, Result, anyhow};
use quinn::{Connection, Endpoint, EndpointConfig};
use tokio_util::task::TaskTracker;
use tracing::{Instrument, info, info_span, warn};

use stargate_forwarding::{self as forwarding, ForwardingResolver, PeerResolution};
use stargate_protocol::TunnelTransportProtocol;

use crate::routing_state::{RegistrationGeneration, StargateState};
use stargate_runtime::CriticalTaskGroup;

use super::QuicHttpProxy;
use super::connection::TunnelConnection;
use super::endpoint::{build_client_config, build_server_config};
use super::http3::build_h3_client_connection;
use super::raw_quic::RawQuicConnectionHandle;

mod handshake;
mod webtransport;

use self::handshake::handle_reverse_handshake;
use self::webtransport::handle_reverse_webtransport_connect;

#[derive(Clone, Copy)]
pub(super) enum ReverseConnectionCleanupKind {
    Tunnel,
    WebTransport,
}

pub(super) fn spawn_reverse_connection_cleanup(
    task_tracker: &TaskTracker,
    registration: Arc<RegistrationGeneration>,
    connection: Connection,
    kind: ReverseConnectionCleanupKind,
) {
    let inference_server_id = registration.inference_server_id().to_string();
    let closed_id = connection.stable_id();
    let (cleanup_span, closed_message) = match kind {
        ReverseConnectionCleanupKind::Tunnel => (
            info_span!(
                "reverse_tunnel_connection_cleanup",
                inference_server_id = %inference_server_id,
                stable_id = closed_id,
            ),
            "reverse tunnel connection closed, removed from pool",
        ),
        ReverseConnectionCleanupKind::WebTransport => (
            info_span!(
                "reverse_webtransport_connection_cleanup",
                inference_server_id = %inference_server_id,
                stable_id = closed_id,
            ),
            "reverse WebTransport connection closed, removed from pool",
        ),
    };
    task_tracker.spawn(
        async move {
            connection.closed().await;
            if registration
                .tunnel_connections()
                .remove_connection(closed_id)
            {
                warn!(inference_server_id = %inference_server_id, "{closed_message}");
            }
        }
        .instrument(cleanup_span),
    );
}

impl QuicHttpProxy {
    pub async fn await_reverse_connection(
        &self,
        registration: Arc<RegistrationGeneration>,
        timeout: Duration,
    ) -> bool {
        registration
            .tunnel_connections()
            .wait_for_healthy(timeout)
            .await
    }

    pub async fn store_reverse_connection(
        &self,
        registration: Arc<RegistrationGeneration>,
        connection: Connection,
    ) -> bool {
        let Some(install) = registration.begin_reverse_connection_install() else {
            return false;
        };

        let initialized = match self.config.tunnel_protocol {
            TunnelTransportProtocol::RawQuic => Ok(TunnelConnection::RawQuic(
                RawQuicConnectionHandle::new(connection),
            )),
            TunnelTransportProtocol::Http3 => build_h3_client_connection(connection)
                .await
                .map(TunnelConnection::Http3),
            TunnelTransportProtocol::WebTransport => Err(anyhow!(
                "reverse WebTransport connections are established by CONNECT handshake"
            )),
        };
        initialized
            .inspect_err(|error| {
                warn!(
                    inference_server_id = %registration.inference_server_id(),
                    error = %error,
                    "failed to initialize tunnel connection"
                );
            })
            .is_ok_and(|connection| install.finish(connection))
    }

    pub async fn start_reverse_listener(
        self: &Arc<Self>,
        state: Arc<StargateState>,
        tasks: CriticalTaskGroup,
        forwarding: Option<Arc<dyn ForwardingResolver>>,
        socket: std::net::UdpSocket,
    ) -> Result<SocketAddr> {
        let server_config = build_server_config(
            &self.config.server_tls_identity,
            self.config.tunnel_protocol,
        )?;
        socket
            .set_nonblocking(true)
            .context("set reverse listener socket to non-blocking")?;
        let endpoint = Endpoint::new(
            EndpointConfig::default(),
            Some(server_config),
            socket,
            quinn::default_runtime().context("no async runtime for quinn endpoint")?,
        )
        .context("create reverse listener from pre-bound socket")?;
        let bound_addr = endpoint
            .local_addr()
            .context("reverse listener local addr")?;

        let relay_endpoints = Arc::new(
            forwarding::build_relay_endpoints(
                forwarding::RelayEndpointConfig::default(),
                build_client_config(
                    self.config.tls_cert_pem.as_deref(),
                    self.config.quic_insecure,
                    self.config.tunnel_protocol,
                )?,
            )
            .context("build relay endpoints")?,
        );

        let dispatch = ReverseDispatchContext {
            proxy: self.clone(),
            state,
            forwarding,
            relay_endpoints,
            listen_port: bound_addr.port(),
            task_tracker: tasks.task_tracker(),
        };
        let listener_tasks = dispatch.task_tracker.clone();
        let listener_span = info_span!("reverse_tunnel_listener", addr = %bound_addr);
        tasks.spawn_critical("reverse tunnel listener", move |stop| {
            async move {
                loop {
                    tokio::select! {
                        _ = stop.cancelled() => break,
                        incoming = endpoint.accept() => {
                            let Some(incoming) = incoming else { break };
                            let dispatch = dispatch.clone();
                            let connection_span = info_span!("reverse_tunnel_connection", port = dispatch.listen_port);
                            listener_tasks.spawn(async move {
                                if let Err(e) = dispatch_incoming(incoming, dispatch).await {
                                    warn!(error = %e, "reverse tunnel connection failed");
                                }
                            }.instrument(connection_span));
                        }
                    }
                }
                endpoint.close(0u32.into(), b"shutdown");
                Ok(())
            }
            .instrument(listener_span)
        });

        info!(addr = %bound_addr, "reverse tunnel listener started");
        Ok(bound_addr)
    }
}

#[derive(Clone)]
struct ReverseDispatchContext {
    proxy: Arc<QuicHttpProxy>,
    state: Arc<StargateState>,
    forwarding: Option<Arc<dyn ForwardingResolver>>,
    relay_endpoints: Arc<forwarding::RelayEndpoints>,
    listen_port: u16,
    task_tracker: TaskTracker,
}

async fn dispatch_incoming(
    incoming: quinn::Incoming,
    dispatch: ReverseDispatchContext,
) -> Result<()> {
    let connection = incoming.await.context("accept reverse connection")?;

    if let Some(fwd) = dispatch.forwarding.as_deref()
        && let Some(sni) = connection
            .handshake_data()
            .and_then(|data| data.downcast::<quinn::crypto::rustls::HandshakeData>().ok())
            .and_then(|hd| hd.server_name)
    {
        match fwd.resolve_peer(&sni, dispatch.listen_port) {
            PeerResolution::Peer(peer) => {
                info!(
                    peer = %peer.dial_addr,
                    server_name = %peer.server_name,
                    sni = %sni,
                    "relaying QUIC connection to peer"
                );
                return forwarding::forward_quic_connection(
                    connection,
                    &peer,
                    &dispatch.relay_endpoints,
                    dispatch.proxy.config.connect_timeout,
                )
                .await;
            }
            PeerResolution::Local | PeerResolution::NotPeer => {}
        }
    }

    match dispatch.proxy.config.tunnel_protocol {
        TunnelTransportProtocol::WebTransport => {
            handle_reverse_webtransport_connect(
                connection,
                &dispatch.proxy,
                &dispatch.state,
                &dispatch.task_tracker,
            )
            .await
        }
        TunnelTransportProtocol::RawQuic | TunnelTransportProtocol::Http3 => {
            handle_reverse_handshake(
                connection,
                &dispatch.proxy,
                &dispatch.state,
                &dispatch.task_tracker,
            )
            .await
        }
    }
}

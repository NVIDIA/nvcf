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

use quinn::Endpoint;
use tokio_util::task::TaskTracker;

use stargate_protocol::TunnelTransportProtocol;
use stargate_runtime::{OwnedTask, TASK_SHUTDOWN_TIMEOUT};
use stargate_tls::ServerTlsIdentity;

use super::core::{TunnelForwardingConfig, TunnelServerApp};
use super::endpoint::{TunnelError, ensure_rustls_provider, make_server_config};
use super::http3::handle_h3_connection;
use super::raw_quic::handle_raw_quic_connection;
use super::webtransport::handle_webtransport_connection;

#[derive(Clone, Debug)]
pub struct QuicHttpTunnelConfig {
    pub listen_addr: SocketAddr,
    pub inference_server_id: Option<String>,
    pub upstream_http_base_url: String,
    pub forwarding: TunnelForwardingConfig,
    pub tls_cert_pem: Option<Vec<u8>>,
    pub tls_key_pem: Option<Vec<u8>>,
    pub tunnel_protocol: TunnelTransportProtocol,
}

impl QuicHttpTunnelConfig {
    pub fn new(listen_addr: SocketAddr, upstream_http_base_url: String) -> Self {
        Self {
            listen_addr,
            inference_server_id: None,
            upstream_http_base_url,
            forwarding: TunnelForwardingConfig::default(),
            tls_cert_pem: None,
            tls_key_pem: None,
            tunnel_protocol: TunnelTransportProtocol::RawQuic,
        }
    }
}

/// Owns the accept task and cancellation; dropping aborts and signals tasks without awaiting `shutdown()`.
#[derive(Debug)]
pub struct QuicHttpTunnelHandle {
    listen_addr: SocketAddr,
    endpoint: Endpoint,
    accept_task: OwnedTask,
    task_tracker: TaskTracker,
}

impl QuicHttpTunnelHandle {
    pub fn listen_addr(&self) -> SocketAddr {
        self.listen_addr
    }

    pub async fn wait_for_exit(&mut self) -> std::result::Result<(), tokio::task::JoinError> {
        self.accept_task.wait_for_exit().await
    }

    pub async fn shutdown(self) {
        self.accept_task.shutdown(TASK_SHUTDOWN_TIMEOUT).await;
        self.endpoint.close(0u32.into(), b"shutdown");
        self.task_tracker.close();
        self.task_tracker.wait().await;
    }
}

pub async fn start_quic_http_tunnel(
    config: QuicHttpTunnelConfig,
) -> Result<QuicHttpTunnelHandle, TunnelError> {
    ensure_rustls_provider();
    let QuicHttpTunnelConfig {
        listen_addr,
        inference_server_id,
        upstream_http_base_url,
        forwarding,
        tls_cert_pem,
        tls_key_pem,
        tunnel_protocol,
    } = config;
    let tls_identity = ServerTlsIdentity::from_optional_pem(tls_cert_pem, tls_key_pem)
        .map_err(|source| TunnelError::Tls { source })?;
    let server_config = make_server_config(&tls_identity, tunnel_protocol)
        .map_err(|source| TunnelError::Tls { source })?;
    let endpoint = Endpoint::server(server_config, listen_addr).map_err(TunnelError::Bind)?;
    let listen_addr = endpoint
        .local_addr()
        .map_err(|e| TunnelError::Bind(std::io::Error::other(e)))?;

    let task_tracker = TaskTracker::new();

    let endpoint_for_task = endpoint.clone();
    let app = TunnelServerApp::new(
        inference_server_id.unwrap_or_default(),
        upstream_http_base_url,
        forwarding,
    );
    let task_tracker_for_accept = task_tracker.clone();

    let accept_task = OwnedTask::spawn("direct tunnel accept loop", move |shutdown| async move {
        loop {
            tokio::select! {
                _ = shutdown.cancelled() => break,
                incoming = endpoint_for_task.accept() => {
                    let Some(incoming) = incoming else {
                        break;
                    };
                    let shutdown_for_conn = shutdown.clone();
                    let app = app.clone();
                    let tracker = task_tracker_for_accept.clone();
                    task_tracker_for_accept.spawn(async move {
                        let result = match tunnel_protocol {
                            TunnelTransportProtocol::RawQuic => {
                                handle_raw_quic_connection(incoming, shutdown_for_conn, tracker, app)
                                    .await
                            }
                            TunnelTransportProtocol::Http3 => {
                                handle_h3_connection(incoming, shutdown_for_conn, tracker, app).await
                            }
                            TunnelTransportProtocol::WebTransport => {
                                handle_webtransport_connection(
                                    incoming,
                                    shutdown_for_conn,
                                    tracker,
                                    app,
                                )
                                .await
                            }
                        };
                        if let Err(error) = result {
                            tracing::warn!(error = %error, "quic tunnel connection failed");
                        }
                    });
                }
            }
        }
    });

    Ok(QuicHttpTunnelHandle {
        listen_addr,
        endpoint,
        accept_task,
        task_tracker,
    })
}

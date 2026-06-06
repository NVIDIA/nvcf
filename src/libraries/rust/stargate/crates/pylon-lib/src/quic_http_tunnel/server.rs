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

use anyhow::Result;
use quinn::Endpoint;
use tokio_util::sync::CancellationToken;
use tokio_util::task::TaskTracker;

use stargate_protocol::TunnelTransportProtocol;
use stargate_tls::ServerTlsIdentity;

use crate::output_token_parser::OutputTokenParserFactory;
use crate::queue_admission::{PylonQueueMismatchRetryConfig, QueueAdmissionTracker};
use crate::request_observer::RequestObservation;
use crate::request_quality_monitor::RequestQualityMonitorConfig;
use crate::stats::PylonMetrics;

use super::core::{
    DEFAULT_FIRST_OUTPUT_TIMEOUT, DEFAULT_MAX_BODY_BYTES, DEFAULT_OUTPUT_CHUNK_TIMEOUT,
    PylonRetryConfig, TunnelServerApp,
};
use super::custom::handle_custom_connection;
use super::endpoint::{TunnelError, ensure_rustls_provider, make_server_config};
use super::http3::handle_h3_connection;
use super::webtransport::handle_webtransport_connection;

#[derive(Clone, Debug)]
pub struct QuicHttpTunnelConfig {
    pub listen_addr: SocketAddr,
    pub inference_server_id: Option<String>,
    pub upstream_http_base_url: String,
    pub max_request_body_bytes: usize,
    pub first_output_timeout: Duration,
    pub output_chunk_timeout: Duration,
    pub output_token_parser_factory: OutputTokenParserFactory,
    pub tls_cert_pem: Option<Vec<u8>>,
    pub tls_key_pem: Option<Vec<u8>>,
    pub quic_insecure: bool,
    pub tunnel_protocol: TunnelTransportProtocol,
    pub request_observation_tx: Option<flume::Sender<RequestObservation>>,
    pub request_quality_monitor: RequestQualityMonitorConfig,
    pub retry: PylonRetryConfig,
    pub queue_mismatch_retry: PylonQueueMismatchRetryConfig,
    pub queue_tracker: QueueAdmissionTracker,
    pub metrics: Option<Arc<PylonMetrics>>,
    #[cfg(test)]
    pub webtransport_stream_header_wait_tx: Option<flume::Sender<()>>,
}

impl QuicHttpTunnelConfig {
    pub fn new(listen_addr: SocketAddr, upstream_http_base_url: String) -> Self {
        Self {
            listen_addr,
            inference_server_id: None,
            upstream_http_base_url,
            max_request_body_bytes: DEFAULT_MAX_BODY_BYTES,
            first_output_timeout: DEFAULT_FIRST_OUTPUT_TIMEOUT,
            output_chunk_timeout: DEFAULT_OUTPUT_CHUNK_TIMEOUT,
            output_token_parser_factory: OutputTokenParserFactory,
            tls_cert_pem: None,
            tls_key_pem: None,
            quic_insecure: false,
            tunnel_protocol: TunnelTransportProtocol::Custom,
            request_observation_tx: None,
            request_quality_monitor: RequestQualityMonitorConfig::default(),
            retry: PylonRetryConfig::default(),
            queue_mismatch_retry: PylonQueueMismatchRetryConfig::default(),
            queue_tracker: QueueAdmissionTracker::default(),
            metrics: None,
            #[cfg(test)]
            webtransport_stream_header_wait_tx: None,
        }
    }
}

/// Both tunnel handles (`QuicHttpTunnelHandle`, `ReverseQuicTunnelHandle`)
/// cancel their `CancellationToken` on drop so the spawned accept loop exits
/// and the QUIC connection/endpoint closes even if the caller never awaits
/// `shutdown()` (e.g. the handle is dropped by `JoinHandle::abort()`).
/// Without this, leaked connections cause the server to NACK reconnections
/// as duplicates.
#[derive(Debug)]
pub struct QuicHttpTunnelHandle {
    listen_addr: SocketAddr,
    endpoint: Endpoint,
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
}

impl QuicHttpTunnelHandle {
    pub fn listen_addr(&self) -> SocketAddr {
        self.listen_addr
    }

    pub async fn shutdown(self) {
        self.shutdown.cancel();
        self.endpoint.close(0u32.into(), b"shutdown");
        self.task_tracker.close();
        self.task_tracker.wait().await;
    }
}

impl Drop for QuicHttpTunnelHandle {
    fn drop(&mut self) {
        self.shutdown.cancel();
    }
}

pub async fn start_quic_http_tunnel(
    config: QuicHttpTunnelConfig,
) -> Result<QuicHttpTunnelHandle, TunnelError> {
    ensure_rustls_provider();
    let tls_identity = ServerTlsIdentity::from_optional_pem(
        config.tls_cert_pem.clone(),
        config.tls_key_pem.clone(),
    )
    .map_err(|source| TunnelError::Tls { source })?;
    let server_config = make_server_config(&tls_identity, config.tunnel_protocol)
        .map_err(|source| TunnelError::Tls { source })?;
    let endpoint =
        Endpoint::server(server_config, config.listen_addr).map_err(TunnelError::Bind)?;
    let listen_addr = endpoint
        .local_addr()
        .map_err(|e| TunnelError::Bind(std::io::Error::other(e)))?;

    let shutdown = CancellationToken::new();
    let task_tracker = TaskTracker::new();

    let endpoint_for_task = endpoint.clone();
    let shutdown_for_task = shutdown.clone();
    let tunnel_protocol = config.tunnel_protocol;
    let app = TunnelServerApp {
        http_client: reqwest::Client::new(),
        inference_server_id: config.inference_server_id.unwrap_or_default(),
        upstream_http_base_url: config.upstream_http_base_url.clone(),
        max_request_body_bytes: config.max_request_body_bytes,
        first_output_timeout: config.first_output_timeout,
        output_chunk_timeout: config.output_chunk_timeout,
        output_token_parser_factory: config.output_token_parser_factory.clone(),
        request_observation_tx: config.request_observation_tx.clone(),
        request_quality_monitor: config.request_quality_monitor.clone(),
        retry: config.retry.clone(),
        queue_mismatch_retry: config.queue_mismatch_retry.clone(),
        queue_tracker: config.queue_tracker.clone(),
        metrics: config.metrics.clone(),
        #[cfg(test)]
        webtransport_stream_header_wait_tx: config.webtransport_stream_header_wait_tx.clone(),
    };
    let task_tracker_for_accept = task_tracker.clone();
    let task_tracker_for_streams = task_tracker.clone();

    task_tracker.spawn(async move {
        loop {
            tokio::select! {
                _ = shutdown_for_task.cancelled() => break,
                incoming = endpoint_for_task.accept() => {
                    let Some(incoming) = incoming else {
                        break;
                    };
                    let shutdown_for_conn = shutdown_for_task.clone();
                    let app = app.clone();
                    let tracker = task_tracker_for_streams.clone();
                    task_tracker_for_accept.spawn(async move {
                        if let Err(error) = handle_connection(
                            incoming,
                            shutdown_for_conn,
                            tracker,
                            app,
                            tunnel_protocol,
                        ).await {
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
        shutdown,
        task_tracker,
    })
}

async fn handle_connection(
    incoming: quinn::Incoming,
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
    app: TunnelServerApp,
    tunnel_protocol: TunnelTransportProtocol,
) -> Result<()> {
    match tunnel_protocol {
        TunnelTransportProtocol::Custom => {
            handle_custom_connection(incoming, shutdown, task_tracker, app).await
        }
        TunnelTransportProtocol::Http3 => {
            handle_h3_connection(incoming, shutdown, task_tracker, app).await
        }
        TunnelTransportProtocol::WebTransport => {
            handle_webtransport_connection(incoming, shutdown, task_tracker, app).await
        }
    }
}

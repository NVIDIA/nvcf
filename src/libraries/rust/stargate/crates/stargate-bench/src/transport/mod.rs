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

mod artifacts;
mod command;
mod config;
mod http3;
mod raw_quic;
mod report;
mod summary;
#[cfg(test)]
mod tests;
mod tls;
mod trials;
mod webtransport;

use std::future::Future;
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, anyhow};
use bytes::Bytes;
use serde::{Deserialize, Serialize};
use stargate_protocol::TunnelTransportProtocol;
use tokio::sync::oneshot;

use crate::statistics::{DistributionStats, NoiseClassification};

pub use artifacts::write_transport_benchmark_artifacts;
pub use command::run_transport_benchmark_command;
pub use config::TransportBenchConfig;
pub use report::render_transport_benchmark_report;
pub use trials::run_transport_benchmark;

pub(super) const SERVER_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct TransportBenchmarkOutcome {
    pub config: TransportBenchConfig,
    pub runs: Vec<TransportRunOutcome>,
    pub warmup_runs: Vec<TransportRunOutcome>,
    pub aggregates: Vec<TransportAggregateSummary>,
    pub comparisons: Vec<TransportComparisonSummary>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct TransportRunOutcome {
    pub transport: TransportKind,
    pub trial_index: usize,
    pub summary: TransportRunSummary,
    pub samples: Vec<RequestSample>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct TransportAggregateSummary {
    pub transport: TransportKind,
    pub trial_count: usize,
    pub classification: NoiseClassification,
    pub throughput_rps: DistributionStats,
    pub goodput_mib_s: DistributionStats,
    pub latency_p95_us: DistributionStats,
    pub response_headers_p95_us: DistributionStats,
    pub first_body_p95_us: DistributionStats,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct TransportComparisonSummary {
    pub baseline: TransportKind,
    pub candidate: TransportKind,
    pub throughput_delta_percent: Option<f64>,
    pub min_effect_size_percent: f64,
    pub confidence_intervals_overlap: Option<bool>,
    pub meaningful_difference: bool,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq, Eq, PartialOrd, Ord)]
#[serde(deny_unknown_fields)]
#[serde(rename_all = "kebab-case")]
pub enum TransportKind {
    RawQuic,
    Http3H3Quinn,
    WebTransportH3Quinn,
}

impl TransportKind {
    pub fn label(self) -> &'static str {
        match self {
            Self::RawQuic => "raw-quic",
            Self::Http3H3Quinn => "http3-h3-quinn",
            Self::WebTransportH3Quinn => "webtransport-h3-quinn",
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct TransportRunSummary {
    pub transport: TransportKind,
    pub request_count: usize,
    pub success_count: usize,
    pub failure_count: usize,
    pub measured_duration_ms: u64,
    pub throughput_rps: f64,
    pub goodput_mib_s: f64,
    pub latency_us: LatencySummary,
    pub response_headers_us: LatencySummary,
    pub first_body_us: LatencySummary,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct LatencySummary {
    pub min: Option<u64>,
    pub p50: Option<u64>,
    pub p90: Option<u64>,
    pub p95: Option<u64>,
    pub p99: Option<u64>,
    pub max: Option<u64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct RequestSample {
    pub request_index: usize,
    pub connection_index: usize,
    pub ok: bool,
    pub response_status: Option<u16>,
    pub request_bytes: usize,
    pub response_bytes: usize,
    pub response_headers_us: Option<u64>,
    pub first_body_us: Option<u64>,
    pub completion_us: u64,
    pub error: Option<String>,
}

#[derive(Clone)]
pub(super) struct PayloadShape {
    pub(super) request_chunks: Arc<Vec<Bytes>>,
    pub(super) response_chunks: Arc<Vec<Bytes>>,
    pub(super) request_bytes: usize,
    pub(super) response_bytes: usize,
}

pub(super) struct RunningServer {
    pub(super) addr: SocketAddr,
    pub(super) cert_pem: Vec<u8>,
    pub(super) shutdown_tx: oneshot::Sender<()>,
    pub(super) task: tokio::task::JoinHandle<Result<()>>,
}

pub(super) fn start_quic_server<H, F>(
    config: TransportBenchConfig,
    protocol: TunnelTransportProtocol,
    response_chunks: Arc<Vec<Bytes>>,
    bind_context: &'static str,
    address_context: &'static str,
    handle_connection: H,
) -> Result<RunningServer>
where
    H: Fn(quinn::Connection, TransportBenchConfig, Arc<Vec<Bytes>>) -> F + Copy + Send + 'static,
    F: Future<Output = Result<()>> + Send + 'static,
{
    let generated = tls::server_config(config, protocol.alpn_protocols())?;
    let endpoint = quinn::Endpoint::server(generated.server_config, "127.0.0.1:0".parse()?)
        .context(bind_context)?;
    let addr = endpoint.local_addr().context(address_context)?;
    let (shutdown_tx, mut shutdown_rx) = oneshot::channel();
    let task = tokio::spawn(async move {
        loop {
            let Some(incoming) = (tokio::select! {
                _ = &mut shutdown_rx => None,
                incoming = endpoint.accept() => incoming,
            }) else {
                break;
            };
            let response_chunks = response_chunks.clone();
            tokio::spawn(async move {
                if let Ok(connection) = incoming.await {
                    let _ = handle_connection(connection, config, response_chunks).await;
                }
            });
        }
        endpoint.close(0_u32.into(), b"benchmark shutdown");
        endpoint.wait_idle().await;
        Ok(())
    });
    Ok(RunningServer {
        addr,
        cert_pem: generated.cert_pem,
        shutdown_tx,
        task,
    })
}

impl RunningServer {
    pub(super) async fn shutdown(self) -> Result<()> {
        let _ = self.shutdown_tx.send(());
        let mut task = self.task;
        match tokio::time::timeout(SERVER_SHUTDOWN_TIMEOUT, &mut task).await {
            Ok(join_result) => join_result.context("transport benchmark server task panicked")?,
            Err(_) => {
                task.abort();
                Err(anyhow!("transport benchmark server did not stop"))
            }
        }
    }
}

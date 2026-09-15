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

use anyhow::{Context, Result};
use quinn::{ClientConfig, Endpoint, ServerConfig, TransportConfig, VarInt};

use super::TransportBenchConfig;

pub(super) const SERVER_NAME: &str = "localhost";

pub(super) async fn connect_quic(
    endpoint: &Endpoint,
    addr: SocketAddr,
) -> Result<quinn::Connection> {
    endpoint
        .connect(addr, SERVER_NAME)
        .context("start QUIC connection")?
        .await
        .context("complete QUIC connection")
}

pub(super) fn client_endpoint(
    config: TransportBenchConfig,
    alpn_protocols: Vec<Vec<u8>>,
    server_cert_pem: &[u8],
) -> Result<Endpoint> {
    let mut endpoint =
        Endpoint::client("127.0.0.1:0".parse()?).context("bind QUIC client endpoint")?;
    endpoint.set_default_client_config(client_config(config, alpn_protocols, server_cert_pem)?);
    Ok(endpoint)
}

pub(super) struct GeneratedServerConfig {
    pub(super) server_config: ServerConfig,
    pub(super) cert_pem: Vec<u8>,
}

pub(super) fn server_config(
    config: TransportBenchConfig,
    alpn_protocols: Vec<Vec<u8>>,
) -> Result<GeneratedServerConfig> {
    let (cert_pem, key_pem) = stargate_tls::generate_self_signed_cert()?;
    let mut server_config =
        stargate_tls::build_quic_server_config_from_pem(&cert_pem, &key_pem, alpn_protocols)?;
    server_config.transport_config(tuned_transport_config(config));
    Ok(GeneratedServerConfig {
        server_config,
        cert_pem,
    })
}

fn client_config(
    config: TransportBenchConfig,
    alpn_protocols: Vec<Vec<u8>>,
    server_cert_pem: &[u8],
) -> Result<ClientConfig> {
    let mut client_config =
        stargate_tls::build_trusted_quic_client_config_with_alpn(server_cert_pem, alpn_protocols)?;
    client_config.transport_config(tuned_transport_config(config));
    Ok(client_config)
}

fn tuned_transport_config(config: TransportBenchConfig) -> Arc<TransportConfig> {
    let mut transport = TransportConfig::default();
    // The benchmark opens many request streams on one QUIC connection, so the default limit of 100
    // would cap high-concurrency runs before either wire protocol is saturated.
    transport.max_concurrent_bidi_streams(VarInt::from_u32(16_384));
    // Expose Quinn's stream scheduler as a benchmark knob. Its documentation calls out lower
    // fragmentation and overhead for workloads with many small streams when fairness is disabled.
    transport.send_fairness(config.quic_send_fairness);
    // Use larger windows so local flow control is not the first bottleneck for payload-heavy runs.
    transport.stream_receive_window(VarInt::from_u32(16 * 1024 * 1024));
    // Use a larger connection window for aggregate throughput across concurrent request streams.
    transport.receive_window(VarInt::from_u32(64 * 1024 * 1024));
    // Match the receive window so either side can fill the local loopback path during throughput tests.
    transport.send_window(64 * 1024 * 1024);
    Arc::new(transport)
}

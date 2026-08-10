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

use anyhow::Result;
use quinn::{ClientConfig, ServerConfig};
use stargate_forwarding::{RelayEndpointConfig, build_relay_transport_config};
use stargate_tls::ServerTlsIdentity;

pub(crate) fn build_router_server_config(
    identity: &ServerTlsIdentity,
    alpn_protocols: Vec<Vec<u8>>,
    relay_config: RelayEndpointConfig,
) -> Result<ServerConfig> {
    let mut server_config = stargate_tls::build_quic_server_config(identity, alpn_protocols)?;
    server_config.transport_config(build_relay_transport_config(relay_config)?);
    Ok(server_config)
}

pub(crate) fn build_upstream_client_config(
    cert_pem: Option<&[u8]>,
    quic_insecure: bool,
    alpn_protocols: Vec<Vec<u8>>,
    missing_trust_error: &'static str,
) -> Result<ClientConfig> {
    stargate_tls::build_quic_client_config(
        cert_pem,
        quic_insecure,
        alpn_protocols,
        missing_trust_error,
    )
}

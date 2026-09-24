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

/// Writes a Kubernetes-style projected TLS generation and swaps `..data` to it.
///
/// Mirrors the atomic symlink swap kubelet performs, which is what the reload
/// watcher keys on.
#[cfg(all(test, unix))]
pub(crate) fn install_projected_identity(
    root: &std::path::Path,
    generation: &str,
    cert: &[u8],
    key: &[u8],
) {
    use std::os::unix::fs::symlink;

    let generation_dir = root.join(generation);
    std::fs::create_dir(&generation_dir).expect("create projected generation");
    std::fs::write(generation_dir.join("tls.crt"), cert).expect("write projected cert");
    std::fs::write(generation_dir.join("tls.key"), key).expect("write projected key");

    let next_data = root.join("..data-next");
    let _ = std::fs::remove_file(&next_data);
    symlink(generation, &next_data).expect("create projected data symlink");
    std::fs::rename(next_data, root.join("..data")).expect("swap projected data symlink");

    if !root.join("tls.crt").exists() {
        symlink("..data/tls.crt", root.join("tls.crt")).expect("create projected cert symlink");
        symlink("..data/tls.key", root.join("tls.key")).expect("create projected key symlink");
    }
}

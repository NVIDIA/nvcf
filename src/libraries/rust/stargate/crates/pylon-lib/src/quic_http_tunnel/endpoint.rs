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

use stargate_protocol::TunnelTransportProtocol;
use stargate_tls::ServerTlsIdentity;

#[derive(Debug, thiserror::Error)]
pub enum TunnelError {
    #[error("TLS configuration failed: {source}")]
    Tls {
        #[source]
        source: anyhow::Error,
    },
    #[error("failed to bind QUIC endpoint: {0}")]
    Bind(#[source] std::io::Error),
    #[error("reverse tunnel connection failed while {context}: {source}")]
    Connect {
        context: &'static str,
        #[source]
        source: anyhow::Error,
    },
    #[error("reverse tunnel connection timed out after {timeout_ms}ms")]
    ConnectTimeout { timeout_ms: u128 },
    #[error("reverse tunnel connection failed: no resolved reverse tunnel address")]
    NoResolvedAddress,
    #[error("reverse tunnel handshake failed while {context}: {source}")]
    Handshake {
        context: &'static str,
        #[source]
        source: anyhow::Error,
    },
    #[error("reverse tunnel handshake rejected by stargate: {reason}")]
    HandshakeRejected { reason: String },
    #[error("reverse tunnel WebTransport CONNECT rejected by stargate with status {status}")]
    WebTransportConnectRejected { status: http::StatusCode },
}

pub(super) fn ensure_rustls_provider() {
    let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
}

/// Extracts TLS SNI from `host:port`, falling back to `"stargate"` for IPs or localhost.
pub(super) fn derive_sni(target_addr: &str) -> String {
    let host = target_addr
        .strip_prefix('[')
        .and_then(|rest| rest.split_once(']').map(|(host, _)| host))
        .or_else(|| target_addr.rsplit_once(':').map(|(host, _)| host))
        .unwrap_or(target_addr);
    if host.parse::<std::net::IpAddr>().is_ok() || host == "localhost" {
        "stargate".to_string()
    } else {
        host.to_string()
    }
}

pub(super) fn target_authority(target_addr: &str) -> String {
    match target_addr.rsplit_once(':') {
        Some((host, port)) if !target_addr.starts_with('[') && host.contains(':') => {
            format!("[{host}]:{port}")
        }
        _ => target_addr.to_string(),
    }
}

pub(super) fn make_server_config(
    tls_identity: &ServerTlsIdentity,
    tunnel_protocol: TunnelTransportProtocol,
) -> Result<quinn::ServerConfig> {
    if matches!(tls_identity, ServerTlsIdentity::SelfSigned) {
        tracing::info!("no TLS cert/key provided, generating self-signed certificate");
    }
    stargate_tls::build_quic_server_config(tls_identity, tunnel_protocol.alpn_protocols())
}

pub(super) fn build_trusted_client_config(
    cert_pem: Option<&[u8]>,
    insecure: bool,
    tunnel_protocol: TunnelTransportProtocol,
) -> Result<quinn::ClientConfig> {
    stargate_tls::build_quic_client_config(
        cert_pem,
        insecure,
        tunnel_protocol.alpn_protocols(),
        "TLS cert required when --quic-insecure is not set",
    )
}

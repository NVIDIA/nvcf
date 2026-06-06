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

use anyhow::{Context, Result};

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

/// Extracts the hostname from a `host:port` target address for use as TLS SNI.
/// Falls back to `"stargate"` if the host is an IP address or localhost.
pub(super) fn derive_sni(target_addr: &str) -> String {
    let host = target_addr
        .rsplit_once(':')
        .map(|(h, _)| h)
        .unwrap_or(target_addr);
    if host.parse::<std::net::IpAddr>().is_ok() || host == "localhost" {
        "stargate".to_string()
    } else {
        host.to_string()
    }
}

pub(super) fn target_authority(target_addr: &str) -> String {
    if target_addr.starts_with('[') {
        return target_addr.to_string();
    }
    let Some((host, port)) = target_addr.rsplit_once(':') else {
        return target_addr.to_string();
    };
    if host.contains(':') {
        format!("[{host}]:{port}")
    } else {
        target_addr.to_string()
    }
}

pub(super) fn make_server_config(
    tls_identity: &ServerTlsIdentity,
    tunnel_protocol: TunnelTransportProtocol,
) -> Result<quinn::ServerConfig> {
    if matches!(tls_identity, ServerTlsIdentity::SelfSigned) {
        tracing::info!("no TLS cert/key provided, generating self-signed certificate");
    }
    let (cert_data, key_data) = tls_identity.pem_pair()?;
    let mut cert_reader = cert_data.as_ref();
    let cert_chain: Vec<rustls::pki_types::CertificateDer<'static>> =
        rustls_pemfile::certs(&mut cert_reader)
            .collect::<std::result::Result<_, _>>()
            .context("failed to parse cert PEM")?;
    let mut key_reader = key_data.as_ref();
    let key = rustls_pemfile::private_key(&mut key_reader)
        .context("failed to parse key PEM")?
        .context("no private key found in PEM")?;
    let mut tls_config = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(cert_chain, key)
        .context("build quic TLS server config failed")?;
    tls_config.alpn_protocols = tunnel_protocol.alpn_protocols();
    Ok(quinn::ServerConfig::with_crypto(std::sync::Arc::new(
        quinn::crypto::rustls::QuicServerConfig::try_from(tls_config)
            .context("build quic server config failed")?,
    )))
}

pub(super) fn build_trusted_client_config(
    cert_pem: Option<&[u8]>,
    insecure: bool,
    tunnel_protocol: TunnelTransportProtocol,
) -> Result<quinn::ClientConfig> {
    if insecure {
        return stargate_tls::build_insecure_quic_client_config_with_alpn(
            tunnel_protocol.alpn_protocols(),
        );
    }
    let cert_data = cert_pem.context("TLS cert required when --quic-insecure is not set")?;
    let mut roots = rustls::RootCertStore::empty();
    for cert in rustls_pemfile::certs(&mut &*cert_data) {
        roots
            .add(cert.context("failed to parse cert PEM")?)
            .context("failed to add cert to root store")?;
    }
    let mut tls_config = rustls::ClientConfig::builder()
        .with_root_certificates(roots)
        .with_no_client_auth();
    tls_config.alpn_protocols = tunnel_protocol.alpn_protocols();
    Ok(quinn::ClientConfig::new(std::sync::Arc::new(
        quinn::crypto::rustls::QuicClientConfig::try_from(tls_config)?,
    )))
}

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

use std::borrow::Cow;
use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr};
use std::sync::Arc;

use anyhow::{Context, Result, bail, ensure};
use quinn::{ClientConfig, ServerConfig};
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{DigitallySignedStruct, Error, SignatureScheme};

/// Generates a PEM self-signed certificate and key with SANs for `localhost` and `stargate`.
pub fn generate_self_signed_cert() -> Result<(Vec<u8>, Vec<u8>)> {
    generate_self_signed_cert_for_names(vec!["localhost".to_string(), "stargate".to_string()])
}

/// Generates a self-signed certificate and private key for the supplied DNS names.
pub fn generate_self_signed_cert_for_names(names: Vec<String>) -> Result<(Vec<u8>, Vec<u8>)> {
    let cert = rcgen::generate_simple_self_signed(names)
        .context("failed to generate self-signed certificate")?;
    let cert_pem = cert.cert.pem().into_bytes();
    let key_pem = cert.key_pair.serialize_pem().into_bytes();
    Ok((cert_pem, key_pem))
}

pub type ServerTlsPemPair<'a> = (Cow<'a, [u8]>, Cow<'a, [u8]>);

/// Prefers IPv4 dial addresses, preserving each family's resolver order and IPv6 fallback.
pub fn ordered_dial_candidates(
    resolved_addrs: impl IntoIterator<Item = SocketAddr>,
) -> Vec<SocketAddr> {
    let (mut ipv4, ipv6): (Vec<_>, Vec<_>) =
        resolved_addrs.into_iter().partition(SocketAddr::is_ipv4);
    ipv4.extend(ipv6);
    ipv4
}

/// Returns an ephemeral unspecified local address compatible with `remote_addr`.
pub fn quic_client_bind_addr(remote_addr: SocketAddr) -> SocketAddr {
    match remote_addr {
        SocketAddr::V4(_) => SocketAddr::new(Ipv4Addr::UNSPECIFIED.into(), 0),
        SocketAddr::V6(_) => SocketAddr::new(Ipv6Addr::UNSPECIFIED.into(), 0),
    }
}

/// TLS identity used by QUIC tunnel servers.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum ServerTlsIdentity {
    #[default]
    SelfSigned,
    Provided {
        cert_pem: Vec<u8>,
        key_pem: Vec<u8>,
    },
}

impl ServerTlsIdentity {
    /// Builds a server identity from optional certificate/key PEM inputs.
    pub fn from_optional_pem(cert_pem: Option<Vec<u8>>, key_pem: Option<Vec<u8>>) -> Result<Self> {
        match (cert_pem, key_pem) {
            (Some(cert_pem), Some(key_pem)) => Ok(Self::Provided { cert_pem, key_pem }),
            (None, None) => Ok(Self::SelfSigned),
            (Some(_), None) => bail!("TLS key PEM is required when TLS cert PEM is provided"),
            (None, Some(_)) => bail!("TLS cert PEM is required when TLS key PEM is provided"),
        }
    }

    /// Returns the PEM pair to parse when building a server config.
    pub fn pem_pair(&self) -> Result<ServerTlsPemPair<'_>> {
        match self {
            Self::SelfSigned => generate_self_signed_cert()
                .map(|(cert_pem, key_pem)| (Cow::Owned(cert_pem), Cow::Owned(key_pem))),
            Self::Provided { cert_pem, key_pem } => Ok((cert_pem.into(), key_pem.into())),
        }
    }
}

/// Builds a QUIC client config that skips server certificate verification.
pub fn build_insecure_quic_client_config() -> Result<ClientConfig> {
    build_insecure_quic_client_config_with_alpn(Vec::new())
}

/// Builds a QUIC client config that skips verification and advertises the supplied ALPN list.
pub fn build_insecure_quic_client_config_with_alpn(
    alpn_protocols: Vec<Vec<u8>>,
) -> Result<ClientConfig> {
    let mut tls_config = rustls::ClientConfig::builder()
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(InsecureServerCertVerifier))
        .with_no_client_auth();
    tls_config.alpn_protocols = alpn_protocols;
    quic_client_config(tls_config)
}

/// Selects verified or insecure QUIC client TLS from the supplied trust policy.
pub fn build_quic_client_config(
    cert_pem: Option<&[u8]>,
    insecure: bool,
    alpn_protocols: Vec<Vec<u8>>,
    missing_trust_error: &'static str,
) -> Result<ClientConfig> {
    if insecure {
        return build_insecure_quic_client_config_with_alpn(alpn_protocols);
    }
    build_trusted_quic_client_config_with_alpn(
        cert_pem.context(missing_trust_error)?,
        alpn_protocols,
    )
}

/// Builds a QUIC client config using the supplied PEM trust anchor and ALPN list.
pub fn build_trusted_quic_client_config_with_alpn(
    cert_pem: &[u8],
    alpn_protocols: Vec<Vec<u8>>,
) -> Result<ClientConfig> {
    let mut roots = rustls::RootCertStore::empty();
    for cert in rustls_pemfile::certs(&mut &*cert_pem) {
        roots
            .add(cert.context("failed to parse cert PEM")?)
            .context("failed to add cert to root store")?;
    }
    let mut tls_config = rustls::ClientConfig::builder()
        .with_root_certificates(roots)
        .with_no_client_auth();
    tls_config.alpn_protocols = alpn_protocols;
    quic_client_config(tls_config)
}

/// Builds a QUIC server config from one identity and ALPN policy.
pub fn build_quic_server_config(
    identity: &ServerTlsIdentity,
    alpn_protocols: Vec<Vec<u8>>,
) -> Result<ServerConfig> {
    let (cert_pem, key_pem) = identity.pem_pair()?;
    build_quic_server_config_from_pem(&cert_pem, &key_pem, alpn_protocols)
}

/// Builds a QUIC server config from PEM-encoded identity material and ALPN policy.
pub fn build_quic_server_config_from_pem(
    cert_pem: &[u8],
    key_pem: &[u8],
    alpn_protocols: Vec<Vec<u8>>,
) -> Result<ServerConfig> {
    let cert_chain = rustls_pemfile::certs(&mut &*cert_pem)
        .collect::<std::result::Result<Vec<_>, _>>()
        .context("failed to parse TLS certificate PEM")?;
    ensure!(!cert_chain.is_empty(), "no certificate found in TLS PEM");
    let key = rustls_pemfile::private_key(&mut &*key_pem)
        .context("failed to parse TLS private key PEM")?
        .context("no private key found in TLS PEM")?;
    let mut tls = rustls::ServerConfig::builder()
        .with_no_client_auth()
        .with_single_cert(cert_chain, key)
        .context("failed to build TLS server config")?;
    tls.alpn_protocols = alpn_protocols;
    Ok(ServerConfig::with_crypto(Arc::new(
        quinn::crypto::rustls::QuicServerConfig::try_from(tls)
            .context("failed to build QUIC server config")?,
    )))
}

fn quic_client_config(tls: rustls::ClientConfig) -> Result<ClientConfig> {
    Ok(ClientConfig::new(Arc::new(
        quinn::crypto::rustls::QuicClientConfig::try_from(tls)?,
    )))
}

#[derive(Debug)]
struct InsecureServerCertVerifier;

impl ServerCertVerifier for InsecureServerCertVerifier {
    fn verify_server_cert(
        &self,
        _end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp_response: &[u8],
        _now: UnixTime,
    ) -> std::result::Result<ServerCertVerified, Error> {
        Ok(ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        _message: &[u8],
        _cert: &CertificateDer<'_>,
        _dss: &DigitallySignedStruct,
    ) -> std::result::Result<HandshakeSignatureValid, Error> {
        Ok(HandshakeSignatureValid::assertion())
    }

    fn verify_tls13_signature(
        &self,
        _message: &[u8],
        _cert: &CertificateDer<'_>,
        _dss: &DigitallySignedStruct,
    ) -> std::result::Result<HandshakeSignatureValid, Error> {
        Ok(HandshakeSignatureValid::assertion())
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        rustls::crypto::aws_lc_rs::default_provider()
            .signature_verification_algorithms
            .supported_schemes()
    }
}

#[cfg(test)]
mod tests {
    use std::net::SocketAddr;

    use super::*;

    #[test]
    fn self_signed_cert_produces_nonempty_pem() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (cert_pem, key_pem) = generate_self_signed_cert().unwrap();
        assert!(!cert_pem.is_empty());
        assert!(!key_pem.is_empty());
    }

    #[test]
    fn self_signed_cert_for_names_produces_nonempty_pem() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (cert_pem, key_pem) =
            generate_self_signed_cert_for_names(vec!["sg-b.stargate.external".to_string()])
                .unwrap();
        assert!(!cert_pem.is_empty());
        assert!(!key_pem.is_empty());
    }

    #[test]
    fn insecure_client_config_succeeds() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let _config = build_insecure_quic_client_config().unwrap();
    }

    #[test]
    fn insecure_client_config_with_alpn_succeeds() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let _config = build_insecure_quic_client_config_with_alpn(vec![b"h3".to_vec()]).unwrap();
    }

    #[test]
    fn trusted_client_config_with_alpn_accepts_pem_root() {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let (cert_pem, _) = generate_self_signed_cert().unwrap();
        let _config =
            build_trusted_quic_client_config_with_alpn(&cert_pem, vec![b"h3".to_vec()]).unwrap();
    }

    #[test]
    fn server_tls_identity_requires_complete_pem_pair() {
        let cert_pem = b"cert".to_vec();
        let key_pem = b"key".to_vec();

        assert!(matches!(
            ServerTlsIdentity::from_optional_pem(None, None).unwrap(),
            ServerTlsIdentity::SelfSigned
        ));
        assert_eq!(
            ServerTlsIdentity::from_optional_pem(Some(cert_pem.clone()), Some(key_pem.clone()))
                .unwrap(),
            ServerTlsIdentity::Provided {
                cert_pem: cert_pem.clone(),
                key_pem: key_pem.clone(),
            }
        );
        assert!(ServerTlsIdentity::from_optional_pem(Some(cert_pem), None).is_err());
        assert!(ServerTlsIdentity::from_optional_pem(None, Some(key_pem)).is_err());
    }

    #[test]
    fn ordered_dial_candidates_prioritize_ipv4_without_discarding_ipv6() {
        let ipv6: SocketAddr = "[fd00::1]:50072"
            .parse()
            .expect("IPv6 address should parse");
        let ipv4: SocketAddr = "10.0.0.4:50072".parse().expect("IPv4 address should parse");

        assert_eq!(ordered_dial_candidates([ipv6, ipv4]), vec![ipv4, ipv6]);
        assert_eq!(quic_client_bind_addr(ipv4), "0.0.0.0:0".parse().unwrap());
        assert_eq!(quic_client_bind_addr(ipv6), "[::]:0".parse().unwrap());
    }
}

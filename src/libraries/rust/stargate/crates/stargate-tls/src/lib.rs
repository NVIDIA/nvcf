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
use std::sync::Arc;

use anyhow::{Context, Result, bail};
use quinn::ClientConfig;
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{DigitallySignedStruct, Error, SignatureScheme};

/// Generates a self-signed certificate and private key in PEM format.
///
/// The certificate has SANs for `localhost` and `stargate`.
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
            Self::SelfSigned => {
                let (cert_pem, key_pem) = generate_self_signed_cert()?;
                Ok((Cow::Owned(cert_pem), Cow::Owned(key_pem)))
            }
            Self::Provided { cert_pem, key_pem } => {
                Ok((Cow::Borrowed(cert_pem), Cow::Borrowed(key_pem)))
            }
        }
    }
}

/// Builds a QUIC client config that skips server certificate verification.
pub fn build_insecure_quic_client_config() -> Result<ClientConfig> {
    build_insecure_quic_client_config_with_alpn(Vec::new())
}

/// Builds a QUIC client config that skips server certificate verification and
/// advertises the supplied ALPN protocol list.
pub fn build_insecure_quic_client_config_with_alpn(
    alpn_protocols: Vec<Vec<u8>>,
) -> Result<ClientConfig> {
    let mut tls_config = rustls::ClientConfig::builder()
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(InsecureServerCertVerifier))
        .with_no_client_auth();
    tls_config.alpn_protocols = alpn_protocols;
    Ok(ClientConfig::new(Arc::new(
        quinn::crypto::rustls::QuicClientConfig::try_from(tls_config)?,
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
}

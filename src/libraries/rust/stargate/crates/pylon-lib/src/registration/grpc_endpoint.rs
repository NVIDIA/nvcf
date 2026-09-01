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

use std::{error::Error, fmt};

use anyhow::Context;
use rustls::{CertificateError as RustlsCertificateError, Error as RustlsError};
use stargate_protocol::parse_explicit_http_uri;
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Endpoint};

use super::normalize_addr;

#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub(super) struct StargateGrpcEndpoint {
    authority_addr: String,
    dial_addr: String,
}

impl StargateGrpcEndpoint {
    pub(super) fn new(
        authority_addr: impl Into<String>,
        dial_addr: impl Into<String>,
    ) -> Option<Self> {
        let authority_addr = authority_addr.into().trim().to_string();
        if authority_addr.is_empty() {
            return None;
        }
        let dial_addr = dial_addr.into().trim().to_string();
        let dial_addr = if dial_addr.is_empty() {
            authority_addr.clone()
        } else {
            parse_explicit_http_uri(&dial_addr).ok()?
        };
        Some(Self {
            authority_addr,
            dial_addr,
        })
    }

    pub(super) fn authority_addr(&self) -> &str {
        &self.authority_addr
    }

    pub(super) fn dial_endpoint(&self) -> String {
        normalize_addr(&self.dial_addr)
    }

    pub(super) fn authority_endpoint(&self) -> String {
        let dial_endpoint = self.dial_endpoint();
        let default_scheme = endpoint_scheme(&dial_endpoint).unwrap_or("http");
        normalize_addr_with_default_scheme(&self.authority_addr, default_scheme)
    }

    pub(super) fn uses_authority_override(&self) -> bool {
        self.dial_endpoint() != self.authority_endpoint()
    }

    pub(super) fn channel_endpoint(
        &self,
        grpc_tls_ca_cert_pem: Option<&[u8]>,
    ) -> anyhow::Result<Endpoint> {
        let dial_endpoint = self.dial_endpoint();
        let authority_endpoint = self.authority_endpoint();
        let dial_uri: http::Uri = dial_endpoint
            .parse()
            .context("invalid stargate gRPC dial endpoint")?;
        let origin = (dial_endpoint != authority_endpoint)
            .then(|| grpc_origin_uri(&dial_uri, &authority_endpoint))
            .transpose()?;
        let mut endpoint = match (dial_uri.scheme_str(), grpc_tls_ca_cert_pem) {
            (Some("https"), Some(ca_cert_pem)) => Endpoint::from(dial_uri)
                .tls_config(
                    ClientTlsConfig::new()
                        .with_enabled_roots()
                        .ca_certificate(Certificate::from_pem(ca_cert_pem)),
                )
                .context("configure custom CA for stargate gRPC endpoint")?,
            (Some("http"), Some(_)) => {
                anyhow::bail!("custom CA for stargate gRPC requires an HTTPS dial endpoint")
            }
            _ => Endpoint::new(dial_uri).context("configure stargate gRPC endpoint")?,
        };
        if let Some(origin) = origin {
            endpoint = endpoint.origin(origin);
        }
        Ok(endpoint)
    }
}

pub(super) fn grpc_origin_uri(
    dial_uri: &http::Uri,
    authority_endpoint: &str,
) -> anyhow::Result<http::Uri> {
    let authority_uri: http::Uri = authority_endpoint
        .parse()
        .context("invalid stargate gRPC authority endpoint")?;
    let scheme = dial_uri
        .scheme()
        .cloned()
        .unwrap_or(http::uri::Scheme::HTTP);
    let authority = authority_uri
        .authority()
        .cloned()
        .context("stargate gRPC authority endpoint is missing an authority")?;
    http::Uri::builder()
        .scheme(scheme)
        .authority(authority)
        .path_and_query("/")
        .build()
        .context("build stargate gRPC request origin")
}

impl fmt::Display for StargateGrpcEndpoint {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.uses_authority_override() {
            write!(f, "{} via {}", self.authority_addr, self.dial_addr)
        } else {
            write!(f, "{}", self.authority_addr)
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) struct StargateGrpcDebugTarget {
    pub(super) scheme: String,
    pub(super) host: String,
    pub(super) port: u16,
}

pub(super) fn stargate_grpc_debug_target(
    endpoint: &str,
) -> anyhow::Result<StargateGrpcDebugTarget> {
    let uri: http::Uri = endpoint.parse().context("parse stargate gRPC endpoint")?;
    let scheme = uri.scheme_str().unwrap_or("http").to_string();
    let authority = uri
        .authority()
        .context("stargate gRPC endpoint is missing an authority")?;
    let port = authority.port_u16().unwrap_or(match scheme.as_str() {
        "https" => 443,
        _ => 80,
    });

    Ok(StargateGrpcDebugTarget {
        scheme,
        host: authority.host().to_string(),
        port,
    })
}

pub(super) async fn connect_stargate_grpc_channel(
    router_endpoint: &StargateGrpcEndpoint,
    grpc_tls_ca_cert_pem: Option<&[u8]>,
    operation: &'static str,
) -> anyhow::Result<Channel> {
    log_stargate_grpc_connect_attempt(router_endpoint, operation, "eager");
    let channel = router_endpoint
        .channel_endpoint(grpc_tls_ca_cert_pem)?
        .connect()
        .await?;
    log_stargate_grpc_channel_connected(router_endpoint, operation);
    Ok(channel)
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, thiserror::Error)]
pub(super) enum StargateGrpcCertificateFailure {
    #[error("server certificate has an unknown issuer")]
    UnknownIssuer,
    #[error("server certificate SAN does not match the dial hostname")]
    HostnameMismatch,
    #[error("server certificate chain validation failed")]
    ChainValidation,
}

impl StargateGrpcCertificateFailure {
    fn kind(&self) -> &'static str {
        match self {
            Self::UnknownIssuer => "tls_unknown_issuer",
            Self::HostnameMismatch => "tls_hostname_mismatch",
            Self::ChainValidation => "tls_chain_validation",
        }
    }

    fn corrective_action(&self) -> &'static str {
        match self {
            Self::UnknownIssuer => {
                "verify the configured gRPC CA signs the Stargate server certificate and the server presents the required certificate chain"
            }
            Self::HostnameMismatch => {
                "configure a dial hostname present in the server certificate SAN or issue a certificate containing the configured hostname"
            }
            Self::ChainValidation => {
                "verify the configured gRPC CA signs the Stargate server certificate, the server presents the required chain, and the certificate is valid"
            }
        }
    }
}

fn rustls_certificate_error<'a>(
    mut error: &'a (dyn Error + 'static),
) -> Option<&'a RustlsCertificateError> {
    loop {
        if let Some(RustlsError::InvalidCertificate(certificate_error)) =
            error.downcast_ref::<RustlsError>()
        {
            return Some(certificate_error);
        }
        // `io::Error::source` skips the custom inner error itself, so inspect it directly.
        if let Some(RustlsError::InvalidCertificate(certificate_error)) = error
            .downcast_ref::<std::io::Error>()
            .and_then(std::io::Error::get_ref)
            .and_then(|error| error.downcast_ref::<RustlsError>())
        {
            return Some(certificate_error);
        }
        error = error.source()?;
    }
}

fn classify_stargate_grpc_certificate_failure(
    error: &(dyn Error + 'static),
) -> Option<StargateGrpcCertificateFailure> {
    match rustls_certificate_error(error)? {
        RustlsCertificateError::UnknownIssuer => {
            Some(StargateGrpcCertificateFailure::UnknownIssuer)
        }
        RustlsCertificateError::NotValidForName
        | RustlsCertificateError::NotValidForNameContext { .. } => {
            Some(StargateGrpcCertificateFailure::HostnameMismatch)
        }
        _ => Some(StargateGrpcCertificateFailure::ChainValidation),
    }
}

pub(super) fn log_stargate_grpc_certificate_failure(
    target: &StargateGrpcEndpoint,
    operation: &'static str,
    error: &(dyn Error + 'static),
    previous: Option<StargateGrpcCertificateFailure>,
) -> Option<StargateGrpcCertificateFailure> {
    let Some(failure) = classify_stargate_grpc_certificate_failure(error) else {
        return previous;
    };
    if previous == Some(failure) {
        return previous;
    }
    let dial_endpoint = target.dial_endpoint();
    let authority_endpoint = target.authority_endpoint();
    match (
        stargate_grpc_debug_target(&dial_endpoint),
        stargate_grpc_debug_target(&authority_endpoint),
    ) {
        (Ok(dial), Ok(authority)) => tracing::error!(
            transport = "grpc",
            operation,
            failure_kind = failure.kind(),
            failure_reason = %failure,
            corrective_action = failure.corrective_action(),
            tls = dial.scheme == "https",
            dial_host = %dial.host,
            dial_port = dial.port,
            authority_host = %authority.host,
            authority_port = authority.port,
            override_authority = dial_endpoint != authority_endpoint,
            "Stargate gRPC connection failed"
        ),
        _ => tracing::error!(
            transport = "grpc",
            operation,
            failure_kind = failure.kind(),
            failure_reason = %failure,
            corrective_action = failure.corrective_action(),
            "Stargate gRPC connection failed"
        ),
    }
    Some(failure)
}

macro_rules! log_stargate_grpc_target {
    ($target:expr, $operation:expr, [$($extra:tt)*], $message:literal, $error_message:literal) => {{
        if !tracing::enabled!(tracing::Level::DEBUG) {
            return;
        }
        let dial_endpoint = $target.dial_endpoint();
        let authority_endpoint = $target.authority_endpoint();
        let override_authority = dial_endpoint != authority_endpoint;
        match (
            stargate_grpc_debug_target(&dial_endpoint),
            stargate_grpc_debug_target(&authority_endpoint),
        ) {
            (Ok(dial), Ok(authority)) => tracing::debug!(
                transport = "grpc",
                operation = $operation,
                http_version = "h2",
                dial_scheme = %dial.scheme,
                tls = dial.scheme == "https",
                dial_host = %dial.host,
                dial_port = dial.port,
                authority_host = %authority.host,
                authority_port = authority.port,
                override_authority,
                $($extra)*
                $message
            ),
            (Err(_), _) | (_, Err(_)) => tracing::debug!(
                transport = "grpc",
                operation = $operation,
                override_authority,
                $($extra)*
                $error_message
            ),
        }
    }};
}

pub(super) fn log_stargate_grpc_connect_attempt(
    target: &StargateGrpcEndpoint,
    operation: &'static str,
    connect_mode: &'static str,
) {
    log_stargate_grpc_target!(
        target,
        operation,
        [connect_mode,],
        "attempting Stargate gRPC connection",
        "could not parse Stargate gRPC endpoint for connection debug logging"
    );
}

fn log_stargate_grpc_channel_connected(target: &StargateGrpcEndpoint, operation: &'static str) {
    log_stargate_grpc_target!(
        target,
        operation,
        [],
        "Stargate gRPC channel connected",
        "Stargate gRPC channel connected but endpoint metadata could not be parsed"
    );
}

fn normalize_addr_with_default_scheme(addr: &str, default_scheme: &str) -> String {
    if addr.starts_with("http://") || addr.starts_with("https://") {
        addr.to_string()
    } else {
        format!("{default_scheme}://{addr}")
    }
}

fn endpoint_scheme(endpoint: &str) -> Option<&str> {
    endpoint.split_once("://").map(|(scheme, _)| scheme)
}

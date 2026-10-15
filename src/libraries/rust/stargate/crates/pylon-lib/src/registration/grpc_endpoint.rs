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

use std::fmt;

use anyhow::Context;
use tonic::transport::{Channel, Endpoint};

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
            dial_addr
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

    pub(super) fn channel_endpoint(&self) -> anyhow::Result<Endpoint> {
        let dial_endpoint = self.dial_endpoint();
        let authority_endpoint = self.authority_endpoint();
        let mut endpoint = Channel::from_shared(dial_endpoint.clone())
            .context("invalid stargate gRPC dial endpoint")?;
        if dial_endpoint != authority_endpoint {
            let origin: http::Uri = authority_endpoint
                .parse()
                .context("invalid stargate gRPC authority endpoint")?;
            endpoint = endpoint.origin(origin);
        }
        Ok(endpoint)
    }
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
    pub(super) endpoint: String,
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
        endpoint: endpoint.to_string(),
        scheme,
        host: authority.host().to_string(),
        port,
    })
}

pub(super) async fn connect_stargate_grpc_channel(
    router_endpoint: &StargateGrpcEndpoint,
    operation: &'static str,
) -> anyhow::Result<Channel> {
    log_stargate_grpc_connect_attempt(router_endpoint, operation, "eager");
    let channel = router_endpoint.channel_endpoint()?.connect().await?;
    log_stargate_grpc_channel_connected(router_endpoint, operation);
    Ok(channel)
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
                endpoint = %dial.endpoint,
                dial_endpoint = %dial.endpoint,
                dial_scheme = %dial.scheme,
                tls = dial.scheme == "https",
                dial_host = %dial.host,
                dial_port = dial.port,
                authority_endpoint = %authority.endpoint,
                authority_host = %authority.host,
                authority_port = authority.port,
                override_authority,
                $($extra)*
                $message
            ),
            (Err(error), _) | (_, Err(error)) => tracing::debug!(
                transport = "grpc",
                operation = $operation,
                dial_endpoint = %dial_endpoint,
                authority_endpoint = %authority_endpoint,
                override_authority,
                error = %error,
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

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

    fn dial_endpoint(&self) -> String {
        normalize_addr(&self.dial_addr)
    }

    fn authority_endpoint(&self) -> String {
        let dial_endpoint = self.dial_endpoint();
        let default_scheme = endpoint_scheme(&dial_endpoint).unwrap_or("http");
        normalize_addr_with_default_scheme(&self.authority_addr, default_scheme)
    }

    fn uses_authority_override(&self) -> bool {
        self.dial_endpoint() != self.authority_endpoint()
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

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) struct StargateGrpcConnectTarget {
    pub(super) dial_endpoint: String,
    pub(super) authority_endpoint: String,
    pub(super) override_authority: bool,
}

impl StargateGrpcConnectTarget {
    pub(super) fn direct(endpoint: String) -> Self {
        Self {
            dial_endpoint: endpoint.clone(),
            authority_endpoint: endpoint,
            override_authority: false,
        }
    }
}

pub(super) fn stargate_grpc_connect_target(
    router_endpoint: &StargateGrpcEndpoint,
) -> StargateGrpcConnectTarget {
    StargateGrpcConnectTarget {
        dial_endpoint: router_endpoint.dial_endpoint(),
        authority_endpoint: router_endpoint.authority_endpoint(),
        override_authority: router_endpoint.uses_authority_override(),
    }
}

pub(super) fn stargate_grpc_channel_endpoint(
    target: &StargateGrpcConnectTarget,
) -> anyhow::Result<Endpoint> {
    let mut endpoint = Channel::from_shared(target.dial_endpoint.clone())
        .context("invalid stargate gRPC dial endpoint")?;
    if target.override_authority {
        let origin: http::Uri = target
            .authority_endpoint
            .parse()
            .context("invalid stargate gRPC authority endpoint")?;
        endpoint = endpoint.origin(origin);
    }
    Ok(endpoint)
}

pub(super) async fn connect_stargate_grpc_channel(
    router_endpoint: &StargateGrpcEndpoint,
    operation: &'static str,
) -> anyhow::Result<Channel> {
    let target = stargate_grpc_connect_target(router_endpoint);
    log_stargate_grpc_connect_attempt(&target, operation, "eager");
    let channel = stargate_grpc_channel_endpoint(&target)?.connect().await?;
    log_stargate_grpc_channel_connected(&target, operation);
    Ok(channel)
}

pub(super) fn log_stargate_grpc_connect_attempt(
    target: &StargateGrpcConnectTarget,
    operation: &'static str,
    connect_mode: &'static str,
) {
    if !tracing::enabled!(tracing::Level::DEBUG) {
        return;
    }

    match (
        stargate_grpc_debug_target(&target.dial_endpoint),
        stargate_grpc_debug_target(&target.authority_endpoint),
    ) {
        (Ok(dial), Ok(authority)) => {
            tracing::debug!(
                transport = "grpc",
                operation,
                connect_mode,
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
                override_authority = target.override_authority,
                "attempting Stargate gRPC connection"
            );
        }
        (Err(error), _) | (_, Err(error)) => {
            tracing::debug!(
                transport = "grpc",
                operation,
                connect_mode,
                dial_endpoint = %target.dial_endpoint,
                authority_endpoint = %target.authority_endpoint,
                override_authority = target.override_authority,
                error = %error,
                "could not parse Stargate gRPC endpoint for connection debug logging"
            );
        }
    }
}

fn log_stargate_grpc_channel_connected(
    target: &StargateGrpcConnectTarget,
    operation: &'static str,
) {
    if !tracing::enabled!(tracing::Level::DEBUG) {
        return;
    }

    match (
        stargate_grpc_debug_target(&target.dial_endpoint),
        stargate_grpc_debug_target(&target.authority_endpoint),
    ) {
        (Ok(dial), Ok(authority)) => {
            tracing::debug!(
                transport = "grpc",
                operation,
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
                override_authority = target.override_authority,
                "Stargate gRPC channel connected"
            );
        }
        (Err(error), _) | (_, Err(error)) => {
            tracing::debug!(
                transport = "grpc",
                operation,
                dial_endpoint = %target.dial_endpoint,
                authority_endpoint = %target.authority_endpoint,
                override_authority = target.override_authority,
                error = %error,
                "Stargate gRPC channel connected but endpoint metadata could not be parsed"
            );
        }
    }
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

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

use std::sync::Arc;
use std::time::Duration;

use stargate_auth::AuthTokenProvider;
use stargate_protocol::TunnelTransportProtocol;

use crate::quic_http_tunnel::{TunnelError, TunnelForwardingConfig};

use super::urls::{infer_upstream_http_base_url, is_direct_inference_server_url};

#[derive(Debug, thiserror::Error)]
pub enum ClientError {
    #[error("configuration error: {0}")]
    Config(String),
    #[error("QUIC tunnel failed: {0}")]
    Tunnel(#[from] TunnelError),
}

#[derive(Debug, Clone)]
pub struct InferenceServerRegistrationConfig {
    pub seeds: Vec<String>,
    pub inference_server_id: String,
    pub cluster_id: String,
    pub inference_server_url: String,
    pub forwarding: TunnelForwardingConfig,
    pub min_update_interval: Duration,
    pub reverse_tunnel: bool,
    pub tls_cert_pem: Option<Vec<u8>>,
    pub quic_insecure: bool,
    pub tunnel_protocol: TunnelTransportProtocol,
    pub auth_token_provider: Option<Arc<AuthTokenProvider>>,
}

#[derive(Debug)]
pub(super) struct RegistrationSessionConfig {
    pub(super) watch_seeds: Vec<String>,
    pub(super) inference_server_id: String,
    pub(super) cluster_id: String,
    pub(super) inference_server_url: String,
    pub(super) forwarding: TunnelForwardingConfig,
    pub(super) min_update_interval: Duration,
    pub(super) reverse_tunnel: bool,
    pub(super) tls_cert_pem: Option<Vec<u8>>,
    pub(super) quic_insecure: bool,
    pub(super) tunnel_protocol: TunnelTransportProtocol,
    pub(super) auth_token_provider: Option<Arc<AuthTokenProvider>>,
}

impl TryFrom<InferenceServerRegistrationConfig> for RegistrationSessionConfig {
    type Error = ClientError;

    fn try_from(mut config: InferenceServerRegistrationConfig) -> Result<Self, Self::Error> {
        if config.seeds.is_empty() {
            return Err(ClientError::Config("stargate seeds are empty".to_string()));
        }
        if !config.reverse_tunnel && !is_direct_inference_server_url(&config.inference_server_url) {
            return Err(ClientError::Config(
                "direct registration inference_server_url must be quic://".to_string(),
            ));
        }

        let cluster_id = if config.cluster_id.is_empty() {
            config.inference_server_id.clone()
        } else {
            std::mem::take(&mut config.cluster_id)
        };
        let inference_server_url = if config.reverse_tunnel {
            infer_upstream_http_base_url(&config.inference_server_url).ok_or_else(|| {
                ClientError::Config(
                    "reverse registration inference_server_url must be http(s)".to_string(),
                )
            })?
        } else {
            config.inference_server_url
        };
        Ok(Self {
            watch_seeds: config.seeds,
            inference_server_id: config.inference_server_id,
            cluster_id,
            inference_server_url,
            forwarding: config.forwarding,
            min_update_interval: config.min_update_interval,
            reverse_tunnel: config.reverse_tunnel,
            tls_cert_pem: config.tls_cert_pem,
            quic_insecure: config.quic_insecure,
            tunnel_protocol: config.tunnel_protocol,
            auth_token_provider: config.auth_token_provider,
        })
    }
}

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
use stargate_proto::pb::InferenceServerStatus;
use stargate_protocol::TunnelTransportProtocol;

use crate::bringup::BringupConfig;
use crate::output_token_parser::OutputTokenParserFactory;
use crate::queue_admission::{PylonQueueMismatchRetryConfig, QueueAdmissionTracker};
use crate::quic_http_tunnel::{PylonRetryConfig, TunnelError};
use crate::request_observer::RequestObservation;
use crate::request_quality_monitor::RequestQualityMonitorConfig;
use crate::stats::PylonMetrics;

use super::state::CurrentModelStats;
use super::urls::{
    effective_cluster_id, infer_upstream_http_base_url, is_direct_inference_server_url,
};

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
    pub upstream_http_base_url: Option<String>,
    pub min_update_interval: Duration,
    pub status: InferenceServerStatus,
    pub reverse_tunnel: bool,
    pub quic_insecure: bool,
    pub tunnel_protocol: TunnelTransportProtocol,
    pub bringup: BringupConfig,
    pub output_token_parser_factory: OutputTokenParserFactory,
    pub request_observation_tx: Option<flume::Sender<RequestObservation>>,
    pub request_quality_monitor: RequestQualityMonitorConfig,
    pub metrics: Option<Arc<PylonMetrics>>,
    pub retry: PylonRetryConfig,
    pub queue_mismatch_retry: PylonQueueMismatchRetryConfig,
    pub queue_tracker: QueueAdmissionTracker,
    pub auth_token_provider: Option<Arc<AuthTokenProvider>>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(super) struct RegistrationStartPlan {
    pub(super) watch_seeds: Vec<String>,
    pub(super) cluster_id: String,
    pub(super) upstream_http_base_url: String,
}

impl RegistrationStartPlan {
    pub(super) fn from_config(
        config: &InferenceServerRegistrationConfig,
    ) -> Result<Self, ClientError> {
        if config.seeds.is_empty() {
            return Err(ClientError::Config("stargate seeds are empty".to_string()));
        }
        if !config.reverse_tunnel && !is_direct_inference_server_url(&config.inference_server_url) {
            return Err(ClientError::Config(
                "direct registration inference_server_url must be quic://".to_string(),
            ));
        }

        let upstream_http_base_url = config
            .upstream_http_base_url
            .clone()
            .or_else(|| {
                config
                    .reverse_tunnel
                    .then(|| infer_upstream_http_base_url(&config.inference_server_url))
                    .flatten()
            })
            .ok_or_else(|| {
                ClientError::Config(
                    "upstream_http_base_url is required when inference_server_url is not http(s)"
                        .to_string(),
                )
            })?;

        Ok(Self {
            watch_seeds: config.seeds.clone(),
            cluster_id: effective_cluster_id(&config.cluster_id, &config.inference_server_id),
            upstream_http_base_url,
        })
    }
}

pub struct InferenceServerUpdateChannels {
    pub status: flume::Sender<InferenceServerStatus>,
    pub model_stats: flume::Sender<(String, CurrentModelStats)>,
}

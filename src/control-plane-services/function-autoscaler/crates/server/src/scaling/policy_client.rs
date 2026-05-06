/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

use super::{CustomScalingConfig, ScalingFactors, ScalingThresholds, Stickiness};
use crate::nvcf_api::oauth2_client::OAuth2Client;
use anyhow::{bail, Context, Result};
use chrono::Utc;
use std::sync::Arc;
use tonic::metadata::MetadataValue;
use tonic::transport::{Channel, ClientTlsConfig};
use uuid::Uuid;

// Include the generated proto code from nvcf.proto
pub mod nvcf_proto {
    tonic::include_proto!("nvcf");
}

use nvcf_proto::{autoscaler_client::AutoscalerClient, DeploymentConfigurationRequest};

/// gRPC client for fetching custom scaling policies.
/// Maintains a persistent channel to avoid reconnecting on every request.
/// Uses OAuth2 client credentials to get a fresh JWT token per request.
pub struct PolicyClient {
    channel: Channel,
    oauth2_client: Arc<OAuth2Client>,
}

impl PolicyClient {
    /// Create a PolicyClient with lazy connection (connects on first use).
    pub fn new_lazy(endpoint: String, oauth2_client: Arc<OAuth2Client>) -> Result<Self> {
        let channel = if endpoint.starts_with("https") {
            let tls = ClientTlsConfig::new().with_native_roots();
            Channel::from_shared(endpoint)
                .context("Invalid gRPC endpoint URL")?
                .tls_config(tls)
                .context("Failed to configure TLS")?
                .connect_lazy()
        } else {
            Channel::from_shared(endpoint)
                .context("Invalid gRPC endpoint URL")?
                .connect_lazy()
        };

        Ok(Self {
            channel,
            oauth2_client,
        })
    }

    /// Fetch custom scaling policy for a function version from the NVCF service
    pub async fn fetch_policy(&self, function_version_id: Uuid) -> Result<CustomScalingConfig> {
        // Get a fresh JWT token via OAuth2 client credentials
        let jwt_token = self
            .oauth2_client
            .get_jwt_token()
            .await
            .map_err(|e| anyhow::anyhow!("Failed to get OAuth2 JWT token: {}", e))?;

        let token: MetadataValue<_> = format!("Bearer {}", jwt_token)
            .parse()
            .context("Failed to parse bearer token as metadata value")?;
        let mut client = AutoscalerClient::with_interceptor(
            self.channel.clone(),
            move |mut req: tonic::Request<()>| {
                req.metadata_mut().insert("authorization", token.clone());
                Ok(req)
            },
        );

        // Create request
        let request = tonic::Request::new(DeploymentConfigurationRequest {
            function_id: function_version_id.to_string(),
            function_version_id: function_version_id.to_string(),
        });

        // Make gRPC call
        let response = client
            .request_deployment_configuration(request)
            .await
            .context("gRPC call to NVCF RequestDeploymentConfiguration failed")?
            .into_inner();

        // Get the first GPU config from the map
        let gpu_config = response
            .configs
            .values()
            .next()
            .context("No GPU configs returned in response")?;

        // Extract scale-up details
        let scale_up = gpu_config
            .scale_up_details
            .as_ref()
            .context("No scale_up_details in GPU config")?;

        // Extract scale-down details
        let scale_down = gpu_config
            .scale_down_details
            .as_ref()
            .context("No scale_down_details in GPU config")?;

        let scale_up_threshold = scale_up.threshold as f32;
        let scale_down_threshold = scale_down.threshold as f32;
        let scale_up_factor = scale_up.factor;
        let scale_down_factor = scale_down.factor;

        // Validate thresholds: up must be greater than down
        if scale_up_threshold <= scale_down_threshold {
            bail!(
                "Invalid thresholds from gRPC for {}: scale_up_threshold ({}) must be > scale_down_threshold ({})",
                function_version_id,
                scale_up_threshold,
                scale_down_threshold,
            );
        }

        // Validate factors: up must increase, down must decrease
        if scale_up_factor <= 1.0 {
            bail!(
                "Invalid scale_up_factor from gRPC for {}: {} (must be > 1.0)",
                function_version_id,
                scale_up_factor,
            );
        }
        if scale_down_factor <= 0.0 || scale_down_factor >= 1.0 {
            bail!(
                "Invalid scale_down_factor from gRPC for {}: {} (must be in (0, 1))",
                function_version_id,
                scale_down_factor,
            );
        }

        let thresholds = ScalingThresholds {
            scale_up_threshold,
            scale_down_threshold,
        };

        let factors = ScalingFactors {
            scale_up_factor,
            scale_down_factor,
        };

        // Extract stickiness windows (optional)
        let scale_up_stickiness = scale_up.stickiness.as_ref().and_then(|s| {
            let window = s.size.as_ref()?.seconds as u32 / 60;
            let required = s.threshold.as_ref()?.seconds as u32 / 60;
            if window > 0 && required > 0 {
                Some(Stickiness {
                    window_minutes: window,
                    required_minutes: required,
                })
            } else {
                None
            }
        });

        let scale_down_stickiness = scale_down.stickiness.as_ref().and_then(|s| {
            let window = s.size.as_ref()?.seconds as u32 / 60;
            let required = s.threshold.as_ref()?.seconds as u32 / 60;
            if window > 0 && required > 0 {
                Some(Stickiness {
                    window_minutes: window,
                    required_minutes: required,
                })
            } else {
                None
            }
        });

        tracing::info!(
            "Fetched NVCF policy for function_version_id {}: thresholds=[up:{}, down:{}], factors=[up:{}, down:{}], stickiness_up={:?}, stickiness_down={:?}",
            function_version_id,
            thresholds.scale_up_threshold,
            thresholds.scale_down_threshold,
            factors.scale_up_factor,
            factors.scale_down_factor,
            scale_up_stickiness,
            scale_down_stickiness,
        );

        Ok(CustomScalingConfig {
            function_version_id,
            scaling_thresholds: thresholds,
            scaling_factors: factors,
            scale_up_stickiness,
            scale_down_stickiness,
            fetched_at: Utc::now(),
        })
    }
}

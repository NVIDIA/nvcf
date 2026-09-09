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
use crate::nvcf_api::lazy_grpc_channel;
use crate::nvcf_api::oauth2_client::OAuth2Client;
use anyhow::{bail, Context, Result};
use chrono::Utc;
use std::collections::HashMap;
use std::sync::Arc;
use tonic::metadata::MetadataValue;
use tonic::transport::Channel;
use uuid::Uuid;

// Include the generated proto code from nvcf.proto
pub mod nvcf_proto {
    tonic::include_proto!("nvcf");
}

use nvcf_proto::{
    autoscaler_client::AutoscalerClient, AutoscalingConfiguration, DeploymentConfigurationRequest,
};

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
        let channel = lazy_grpc_channel(&endpoint)?;
        Ok(Self {
            channel,
            oauth2_client,
        })
    }

    /// Fetch custom scaling policy for a function version from the NVCF service
    pub async fn fetch_policy(
        &self,
        function_version_id: Uuid,
    ) -> Result<Option<CustomScalingConfig>> {
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

        resolve_policy(function_version_id, response.configs)
    }
}

fn resolve_policy(
    function_version_id: Uuid,
    configs: HashMap<String, AutoscalingConfiguration>,
) -> Result<Option<CustomScalingConfig>> {
    let mut configs = configs.into_iter();
    let Some((first_gpu_spec_id, first_gpu_config)) = configs.next() else {
        return Ok(None);
    };

    let policy = parse_policy(function_version_id, &first_gpu_config)?;
    let mut gpu_spec_ids = vec![first_gpu_spec_id];
    let mut has_conflict = false;

    for (gpu_spec_id, gpu_config) in configs {
        let candidate = parse_policy(function_version_id, &gpu_config)?;
        gpu_spec_ids.push(gpu_spec_id);
        if !same_policy(&policy, &candidate) {
            has_conflict = true;
        }
    }

    if has_conflict {
        gpu_spec_ids.sort();
        tracing::warn!(
            function_version_id = %function_version_id,
            gpu_spec_ids = ?gpu_spec_ids,
            "Conflicting custom autoscaling policies; using platform defaults"
        );
        return Ok(None);
    }

    Ok(Some(policy))
}

fn same_policy(left: &CustomScalingConfig, right: &CustomScalingConfig) -> bool {
    left.scaling_thresholds == right.scaling_thresholds
        && left.scaling_factors == right.scaling_factors
        && left.scale_up_stickiness == right.scale_up_stickiness
        && left.scale_down_stickiness == right.scale_down_stickiness
}

fn parse_policy(
    function_version_id: Uuid,
    gpu_config: &AutoscalingConfiguration,
) -> Result<CustomScalingConfig> {
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

#[cfg(test)]
mod tests {
    use super::*;
    use nvcf_proto::ScalingDetails;

    fn config(scale_up_threshold: i32, scale_down_threshold: i32) -> AutoscalingConfiguration {
        AutoscalingConfiguration {
            scale_up_details: Some(ScalingDetails {
                metric: "worker_utilization".to_string(),
                factor: 1.5,
                threshold: scale_up_threshold,
                stickiness: None,
            }),
            scale_down_details: Some(ScalingDetails {
                metric: "worker_utilization".to_string(),
                factor: 0.5,
                threshold: scale_down_threshold,
                stickiness: None,
            }),
        }
    }

    #[test]
    fn empty_configs_use_platform_defaults() {
        let result = resolve_policy(Uuid::new_v4(), HashMap::new()).unwrap();

        assert!(result.is_none());
    }

    #[test]
    fn single_config_uses_custom_policy() {
        let function_version_id = Uuid::new_v4();
        let configs = HashMap::from([("gpu-spec-1".to_string(), config(75, 25))]);

        let result = resolve_policy(function_version_id, configs)
            .unwrap()
            .unwrap();

        assert_eq!(result.function_version_id, function_version_id);
        assert_eq!(result.scaling_thresholds.scale_up_threshold, 75.0);
        assert_eq!(result.scaling_thresholds.scale_down_threshold, 25.0);
    }

    #[test]
    fn identical_configs_use_custom_policy() {
        let configs = HashMap::from([
            ("gpu-spec-1".to_string(), config(75, 25)),
            ("gpu-spec-2".to_string(), config(75, 25)),
        ]);

        let result = resolve_policy(Uuid::new_v4(), configs).unwrap();

        assert!(result.is_some());
    }

    #[test]
    fn conflicting_configs_use_platform_defaults() {
        let configs = HashMap::from([
            ("gpu-spec-1".to_string(), config(75, 25)),
            ("gpu-spec-2".to_string(), config(80, 20)),
        ]);

        let result = resolve_policy(Uuid::new_v4(), configs).unwrap();

        assert!(result.is_none());
    }
}

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
use std::future::Future;
use std::sync::Arc;
use std::time::Duration;
use tonic::metadata::MetadataValue;
use tonic::transport::Channel;
use uuid::Uuid;

// Include the generated proto code from nvcf.proto
pub mod nvcf_proto {
    tonic::include_proto!("nvcf");
}

use nvcf_proto::{
    autoscaler_client::AutoscalerClient, AutoscalingConfiguration, DeploymentConfigurationRequest,
    DeploymentConfigurationResponse,
};

/// gRPC client for fetching custom scaling policies.
/// Maintains a persistent channel to avoid reconnecting on every request.
/// Uses OAuth2 client credentials to get a fresh JWT token per request.
pub struct PolicyClient {
    channel: Channel,
    oauth2_client: Arc<OAuth2Client>,
    request_timeout: Duration,
    default_thresholds: ScalingThresholds,
    default_factors: ScalingFactors,
}

impl PolicyClient {
    /// Create a PolicyClient with lazy connection (connects on first use).
    pub fn new_lazy(
        endpoint: String,
        oauth2_client: Arc<OAuth2Client>,
        request_timeout: Duration,
        default_thresholds: ScalingThresholds,
        default_factors: ScalingFactors,
    ) -> Result<Self> {
        let channel = lazy_grpc_channel(&endpoint)?;
        Ok(Self {
            channel,
            oauth2_client,
            request_timeout,
            default_thresholds,
            default_factors,
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
        let response = await_policy_response(
            client.request_deployment_configuration(request),
            self.request_timeout,
        )
        .await?
        .into_inner();

        resolve_policy(
            function_version_id,
            response.configs,
            &self.default_thresholds,
            &self.default_factors,
        )
    }
}

async fn await_policy_response<F>(
    response: F,
    request_timeout: Duration,
) -> Result<tonic::Response<DeploymentConfigurationResponse>>
where
    F: Future<
        Output = std::result::Result<
            tonic::Response<DeploymentConfigurationResponse>,
            tonic::Status,
        >,
    >,
{
    tokio::time::timeout(request_timeout, response)
        .await
        .with_context(|| {
            format!("gRPC RequestDeploymentConfiguration timed out after {request_timeout:?}")
        })?
        .context("gRPC call to NVCF RequestDeploymentConfiguration failed")
}

fn resolve_policy(
    function_version_id: Uuid,
    configs: HashMap<String, AutoscalingConfiguration>,
    default_thresholds: &ScalingThresholds,
    default_factors: &ScalingFactors,
) -> Result<Option<CustomScalingConfig>> {
    let mut configs = configs.into_iter();
    let Some((first_gpu_spec_id, first_gpu_config)) = configs.next() else {
        return Ok(None);
    };

    let policy = parse_policy(
        function_version_id,
        &first_gpu_config,
        default_thresholds,
        default_factors,
    )?;
    let mut gpu_spec_ids = vec![first_gpu_spec_id];
    let mut has_conflict = false;

    for (gpu_spec_id, gpu_config) in configs {
        let candidate = parse_policy(
            function_version_id,
            &gpu_config,
            default_thresholds,
            default_factors,
        )?;
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
    default_thresholds: &ScalingThresholds,
    default_factors: &ScalingFactors,
) -> Result<CustomScalingConfig> {
    let scale_up = gpu_config.scale_up_details.as_ref();
    let scale_down = gpu_config.scale_down_details.as_ref();

    let scale_up_threshold = scale_up
        .map(|details| details.threshold as f32)
        .unwrap_or(default_thresholds.scale_up_threshold);
    let scale_down_threshold = scale_down
        .map(|details| details.threshold as f32)
        .unwrap_or(default_thresholds.scale_down_threshold);
    let scale_up_factor = scale_up
        .map(|details| details.factor)
        .unwrap_or(default_factors.scale_up_factor);
    let scale_down_factor = scale_down
        .map(|details| details.factor)
        .unwrap_or(default_factors.scale_down_factor);

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
    let scale_up_stickiness = scale_up
        .and_then(|details| details.stickiness.as_ref())
        .and_then(|s| {
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

    let scale_down_stickiness = scale_down
        .and_then(|details| details.stickiness.as_ref())
        .and_then(|s| {
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
    use std::sync::atomic::{AtomicBool, Ordering};

    struct DropProbe(Arc<AtomicBool>);

    impl Drop for DropProbe {
        fn drop(&mut self) {
            self.0.store(true, Ordering::SeqCst);
        }
    }

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

    fn resolve(
        configs: HashMap<String, AutoscalingConfiguration>,
    ) -> Result<Option<CustomScalingConfig>> {
        resolve_policy(
            Uuid::new_v4(),
            configs,
            &ScalingThresholds::default(),
            &ScalingFactors::default(),
        )
    }

    #[test]
    fn empty_configs_use_platform_defaults() {
        let result = resolve(HashMap::new()).unwrap();

        assert!(result.is_none());
    }

    #[test]
    fn single_config_uses_custom_policy() {
        let function_version_id = Uuid::new_v4();
        let configs = HashMap::from([("gpu-spec-1".to_string(), config(75, 25))]);

        let result = resolve_policy(
            function_version_id,
            configs,
            &ScalingThresholds::default(),
            &ScalingFactors::default(),
        )
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

        let result = resolve(configs).unwrap();

        assert!(result.is_some());
    }

    #[test]
    fn conflicting_configs_use_platform_defaults() {
        let configs = HashMap::from([
            ("gpu-spec-1".to_string(), config(75, 25)),
            ("gpu-spec-2".to_string(), config(80, 20)),
        ]);

        let result = resolve(configs).unwrap();

        assert!(result.is_none());
    }

    #[test]
    fn scale_up_only_uses_scale_down_defaults() {
        let mut custom = config(75, 25);
        custom.scale_down_details = None;
        let configs = HashMap::from([("gpu-spec-1".to_string(), custom)]);

        let result = resolve(configs).unwrap().unwrap();

        assert_eq!(result.scaling_thresholds.scale_up_threshold, 75.0);
        assert_eq!(result.scaling_factors.scale_up_factor, 1.5);
        assert_eq!(result.scaling_thresholds.scale_down_threshold, 30.0);
        assert_eq!(result.scaling_factors.scale_down_factor, 0.8);
    }

    #[test]
    fn scale_down_only_uses_scale_up_defaults() {
        let mut custom = config(75, 25);
        custom.scale_up_details = None;
        let configs = HashMap::from([("gpu-spec-1".to_string(), custom)]);

        let result = resolve(configs).unwrap().unwrap();

        assert_eq!(result.scaling_thresholds.scale_up_threshold, 70.0);
        assert_eq!(result.scaling_factors.scale_up_factor, 1.2);
        assert_eq!(result.scaling_thresholds.scale_down_threshold, 25.0);
        assert_eq!(result.scaling_factors.scale_down_factor, 0.5);
    }

    #[tokio::test]
    async fn stalled_request_times_out_and_is_cancelled() {
        let dropped = Arc::new(AtomicBool::new(false));
        let probe = DropProbe(Arc::clone(&dropped));
        let stalled_request = async move {
            let _probe = probe;
            std::future::pending::<
                std::result::Result<
                    tonic::Response<DeploymentConfigurationResponse>,
                    tonic::Status,
                >,
            >()
            .await
        };

        let error = await_policy_response(stalled_request, Duration::from_millis(1))
            .await
            .unwrap_err();

        assert!(error.to_string().contains("timed out"));
        assert!(dropped.load(Ordering::SeqCst));
    }
}

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

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_with::serde_as;
use std::sync::Arc;
use std::time::Duration;
use uuid::Uuid;

pub mod policy_cache;
pub mod policy_client;

fn default_scaling_loop_interval() -> Duration {
    Duration::from_secs(15)
}
fn default_discover_interval() -> Duration {
    Duration::from_secs(15)
}
fn default_lookback() -> Duration {
    Duration::from_secs(600)
}
fn default_discovery_lock_duration() -> Duration {
    Duration::from_secs(60)
}
fn default_scale_to_zero_idle_timeout() -> Duration {
    Duration::from_secs(1800)
}
fn default_utilization_window_seconds() -> u64 {
    70
}
fn default_utilization_lock_duration() -> Duration {
    Duration::from_secs(120)
}
fn default_concurrent_scaling_per_bucket() -> usize {
    10
}
fn default_cassandra_page_size() -> i32 {
    2000
}
fn default_policy_cache_max_capacity() -> u64 {
    10_000
}
fn default_discovery_recently_invoked_lookback_minutes() -> i64 {
    5
}

#[serde_as]
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ScalingSettings {
    #[serde_as(as = "serde_with::DurationSeconds<u64>")]
    #[serde(
        default = "default_scaling_loop_interval",
        rename = "scaling_loop_interval_seconds"
    )]
    pub scaling_loop_interval: Duration,
    #[serde_as(as = "serde_with::DurationSeconds<u64>")]
    #[serde(
        default = "default_discover_interval",
        rename = "discover_new_functions_interval_seconds"
    )]
    pub discover_new_functions_interval: Duration,
    #[serde_as(as = "serde_with::DurationSeconds<u64>")]
    #[serde(default = "default_lookback", rename = "lookback_seconds")]
    pub lookback: Duration,
    #[serde(default)]
    pub decay_factor: f32,
    #[serde(default, rename = "accounts_without_worker_metrics")]
    pub accounts_without_worker_metrics: String,
    pub policy: ScalingPolicy,
    #[serde_as(as = "serde_with::DurationSeconds<u64>")]
    #[serde(
        default = "default_discovery_lock_duration",
        rename = "discovery_lock_duration_seconds"
    )]
    pub discovery_lock_duration: Duration,
    #[serde_as(as = "serde_with::DurationSeconds<u64>")]
    #[serde(
        default = "default_scale_to_zero_idle_timeout",
        rename = "scale_to_zero_idle_timeout_seconds"
    )]
    pub scale_to_zero_idle_timeout: Duration,
    #[serde(default = "default_utilization_window_seconds")]
    pub utilization_window_seconds: u64,
    #[serde_as(as = "serde_with::DurationSeconds<u64>")]
    #[serde(
        default = "default_utilization_lock_duration",
        rename = "utilization_lock_duration_seconds"
    )]
    pub utilization_lock_duration: Duration,
    #[serde(default = "default_concurrent_scaling_per_bucket")]
    pub concurrent_scaling_per_bucket: usize,
    #[serde(default = "default_cassandra_page_size")]
    pub cassandra_page_size: i32,
    #[serde(default = "default_policy_cache_max_capacity")]
    pub policy_cache_max_capacity: u64,
    #[serde(default = "default_discovery_recently_invoked_lookback_minutes")]
    pub discovery_recently_invoked_lookback_minutes: i64,
    #[serde(skip)]
    pub policy_cache: Option<Arc<policy_cache::PolicyCache>>,
}

impl Default for ScalingSettings {
    fn default() -> Self {
        Self {
            scaling_loop_interval: default_scaling_loop_interval(),
            discover_new_functions_interval: default_discover_interval(),
            decay_factor: 0.0,
            lookback: default_lookback(),
            accounts_without_worker_metrics: String::new(),
            policy: ScalingPolicy::Static(StaticScalingPolicy::default()),
            discovery_lock_duration: default_discovery_lock_duration(),
            scale_to_zero_idle_timeout: default_scale_to_zero_idle_timeout(),
            utilization_window_seconds: default_utilization_window_seconds(),
            utilization_lock_duration: default_utilization_lock_duration(),
            concurrent_scaling_per_bucket: default_concurrent_scaling_per_bucket(),
            cassandra_page_size: default_cassandra_page_size(),
            policy_cache_max_capacity: default_policy_cache_max_capacity(),
            discovery_recently_invoked_lookback_minutes:
                default_discovery_recently_invoked_lookback_minutes(),
            policy_cache: None,
        }
    }
}

impl ScalingSettings {
    /// Returns true if the given NCA ID is in the accounts_without_worker_metrics list.
    /// Empty list means all accounts use worker metrics, so returns false.
    pub fn is_account_without_worker_metrics(&self, nca_id: &str) -> bool {
        self.accounts_without_worker_metrics
            .split(',')
            .any(|id| id.trim() == nca_id)
    }

    /// Returns true if NCA ID filtering is enabled (filter is not empty)
    pub fn has_accounts_without_worker_metrics(&self) -> bool {
        !self.accounts_without_worker_metrics.is_empty()
    }

    /// Get scaling policy (thresholds and factors) for a specific function version ID
    ///
    /// Behavior depends on the configured policy type:
    /// - **Static mode**: Always returns the same global config for all functions (ignores function_version_id)
    /// - **Custom mode**:
    ///   - Attempts to fetch per-function config from gRPC service (via cache)
    ///   - On success: returns the function-specific config
    ///   - On failure: falls back to the default thresholds/factors defined in Custom config
    ///
    /// The policy type is determined by the config file at startup and never changes at runtime.
    pub async fn get_policy_for_function(
        &self,
        function_version_id: &Uuid,
    ) -> ResolvedScalingPolicy {
        match &self.policy {
            ScalingPolicy::Static(static_policy) => ResolvedScalingPolicy {
                thresholds: static_policy.scaling_thresholds.clone(),
                factors: static_policy.scaling_factors.clone(),
                scale_up_stickiness: None,
                scale_down_stickiness: None,
            },
            ScalingPolicy::Custom(config) => {
                // Try to get from cache, fallback to default
                if let Some(cache) = &self.policy_cache {
                    match cache.get_or_fetch(*function_version_id).await {
                        Ok(custom_config) => ResolvedScalingPolicy {
                            thresholds: custom_config.scaling_thresholds,
                            factors: custom_config.scaling_factors,
                            scale_up_stickiness: custom_config.scale_up_stickiness,
                            scale_down_stickiness: custom_config.scale_down_stickiness,
                        },
                        Err(e) => {
                            tracing::warn!(
                                "Using default policy for {}: {:#}",
                                function_version_id,
                                e
                            );
                            ResolvedScalingPolicy {
                                thresholds: config.default_thresholds.clone(),
                                factors: config.default_factors.clone(),
                                scale_up_stickiness: None,
                                scale_down_stickiness: None,
                            }
                        }
                    }
                } else {
                    tracing::error!("Policy cache not initialized for Custom policy mode!");
                    ResolvedScalingPolicy {
                        thresholds: config.default_thresholds.clone(),
                        factors: config.default_factors.clone(),
                        scale_up_stickiness: None,
                        scale_down_stickiness: None,
                    }
                }
            }
        }
    }
}

/// Stickiness window for scaling decisions.
/// Scaling only triggers if utilization was past the threshold for at least
/// `required_minutes` out of the last `window_minutes`.
#[derive(Debug, Clone)]
pub struct Stickiness {
    pub window_minutes: u32,
    pub required_minutes: u32,
}

/// Resolved scaling policy for a function — returned by get_policy_for_function
#[derive(Debug, Clone)]
pub struct ResolvedScalingPolicy {
    pub thresholds: ScalingThresholds,
    pub factors: ScalingFactors,
    pub scale_up_stickiness: Option<Stickiness>,
    pub scale_down_stickiness: Option<Stickiness>,
}

/// Entry stored in cache per function_version_id
#[derive(Debug, Clone)]
pub struct CustomScalingConfig {
    pub function_version_id: Uuid,
    pub scaling_thresholds: ScalingThresholds,
    pub scaling_factors: ScalingFactors,
    pub scale_up_stickiness: Option<Stickiness>,
    pub scale_down_stickiness: Option<Stickiness>,
    pub fetched_at: DateTime<Utc>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(tag = "type", content = "settings")]
pub enum ScalingPolicy {
    Static(StaticScalingPolicy),
    Custom(CustomScalingPolicyConfig),
}

/// Configuration for custom (per-function) scaling policies
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct CustomScalingPolicyConfig {
    pub grpc_endpoint: String,
    #[serde(rename = "ttl_seconds")]
    pub ttl_seconds: u64,
    // Fallback defaults when custom policy fetch fails
    pub default_thresholds: ScalingThresholds,
    pub default_factors: ScalingFactors,
}

#[derive(Debug, Clone, Deserialize, Serialize, Default)]
pub struct StaticScalingPolicy {
    pub scaling_thresholds: ScalingThresholds,
    pub scaling_factors: ScalingFactors,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct ScalingThresholds {
    pub scale_up_threshold: f32,
    pub scale_down_threshold: f32,
}

impl Default for ScalingThresholds {
    fn default() -> Self {
        Self {
            scale_up_threshold: 70.0,
            scale_down_threshold: 30.0,
        }
    }
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct ScalingFactors {
    pub scale_up_factor: f32,
    pub scale_down_factor: f32,
}

impl Default for ScalingFactors {
    fn default() -> Self {
        Self {
            scale_up_factor: 1.2,
            scale_down_factor: 0.8,
        }
    }
}

pub fn calculate_average_utilization(
    historical_utilization: Vec<f64>,
    decay_factor: f32,
) -> Option<f32> {
    let mut weighted_sum = 0.0;
    let mut weight_sum = 0.0;
    let len = historical_utilization.len();

    for (i, value) in historical_utilization.iter().enumerate() {
        // Give higher weight to more recent data (reverse the index)
        let weight = (-decay_factor * (len - 1 - i) as f32).exp();
        weighted_sum += weight * *value as f32;
        weight_sum += weight;
    }
    Some(weighted_sum / weight_sum)
}

pub fn prepare_time_series(
    raw_data: Vec<(i64, String)>,
    current_timestamp: i64,
    lookback_minutes: i64,
) -> Vec<f64> {
    let current_minute = (current_timestamp / 60) * 60;
    let start_minute = current_minute - ((lookback_minutes - 1) * 60);
    let mut result = Vec::new();

    for i in 0..lookback_minutes {
        let minute = start_minute + (i * 60);
        let value = raw_data
            .iter()
            .find(|(timestamp, _)| {
                let rounded_timestamp = (*timestamp / 60) * 60;
                rounded_timestamp == minute
            })
            .and_then(|(_, utilization_str)| utilization_str.parse::<f64>().ok())
            .unwrap_or(0.0);
        result.push(value);
    }

    result
}

#[derive(Debug, Clone, PartialEq)]
pub struct ScalingDecision {
    pub desired_instances: usize,
    pub average_utilization: f32,
}

pub fn get_desired_instances(
    raw_historical_utilization: Vec<(i64, String)>,
    policy: &ResolvedScalingPolicy,
    current_instances: usize,
    decay_factor: f32,
) -> Option<ScalingDecision> {
    // Return current instances if no raw data available
    if raw_historical_utilization.is_empty() {
        tracing::debug!(
            "No historical utilization, so returning current instances and zero utilization"
        );
        return Some(ScalingDecision {
            desired_instances: current_instances.max(1),
            average_utilization: 0.0,
        });
    }

    // Parse utilization values directly from timeseries DB data points (already one per step).
    // Sort by timestamp so oldest is first — decay weighting expects chronological order.
    let mut sorted = raw_historical_utilization;
    sorted.sort_by_key(|(ts, _)| *ts);
    let historical_utilization: Vec<f64> = sorted
        .iter()
        .map(|(_, v)| v.parse::<f64>().unwrap_or(0.0))
        .collect();

    let avg_utilization =
        calculate_average_utilization(historical_utilization.clone(), decay_factor)?;
    let base_instances = current_instances.max(1);

    // Scaling decision per direction:
    // - With stickiness: count-based check over the stickiness window
    // - Without stickiness: instantaneous — use the most recent data point
    let last_utilization = *historical_utilization.last().unwrap_or(&0.0) as f32;

    let should_scale_up = match policy.scale_up_stickiness.as_ref() {
        Some(sticky) => check_stickiness(
            &historical_utilization,
            policy.thresholds.scale_up_threshold,
            true,
            sticky,
        ),
        None => last_utilization > policy.thresholds.scale_up_threshold,
    };

    let should_scale_down = match policy.scale_down_stickiness.as_ref() {
        Some(sticky) => check_stickiness(
            &historical_utilization,
            policy.thresholds.scale_down_threshold,
            false,
            sticky,
        ),
        None => last_utilization < policy.thresholds.scale_down_threshold,
    };

    let recommended_instances = if should_scale_up {
        ((base_instances as f32) * policy.factors.scale_up_factor).ceil() as usize
    } else if should_scale_down {
        // Use floor for scale down to ensure we actually reduce (ceil would get stuck)
        // Clamp to minimum 1 - scale to 0 is only allowed via explicit 30-minute no-invocation path
        ((base_instances as f32) * policy.factors.scale_down_factor)
            .floor()
            .max(1.0) as usize
    } else {
        base_instances
    };
    Some(ScalingDecision {
        desired_instances: recommended_instances,
        average_utilization: avg_utilization,
    })
}

/// Check if enough data points in the stickiness window exceed (or are below) the threshold.
/// Returns false if stickiness is None (caller should use weighted average instead).
fn check_stickiness(
    utilization_points: &[f64],
    threshold: f32,
    above: bool, // true = check for above threshold (scale up), false = below (scale down)
    sticky: &Stickiness,
) -> bool {
    let window = sticky.window_minutes as usize;
    let required = sticky.required_minutes as usize;
    let points = if utilization_points.len() > window {
        &utilization_points[utilization_points.len() - window..]
    } else {
        utilization_points
    };

    let count = points
        .iter()
        .filter(|&&v| {
            if above {
                v as f32 > threshold
            } else {
                (v as f32) < threshold
            }
        })
        .count();

    tracing::debug!(
        "Stickiness check: {} of {} points {} threshold {:.1} (need {})",
        count,
        points.len(),
        if above { "above" } else { "below" },
        threshold,
        required,
    );

    count >= required
}

#[cfg(test)]
mod tests {
    use super::*;

    fn default_policy() -> ResolvedScalingPolicy {
        ResolvedScalingPolicy {
            thresholds: ScalingThresholds::default(),
            factors: ScalingFactors::default(),
            scale_up_stickiness: None,
            scale_down_stickiness: None,
        }
    }

    fn policy_with(
        thresholds: ScalingThresholds,
        factors: ScalingFactors,
    ) -> ResolvedScalingPolicy {
        ResolvedScalingPolicy {
            thresholds,
            factors,
            scale_up_stickiness: None,
            scale_down_stickiness: None,
        }
    }

    fn default_thresholds() -> ScalingThresholds {
        ScalingThresholds::default()
    }

    fn default_factors() -> ScalingFactors {
        ScalingFactors::default()
    }

    #[test]
    fn test_prepare_time_series_complete_data() {
        let current_timestamp = 1200; // 20 minutes
        let raw_data = vec![
            (1080, "0.50".to_string()), // 18 minutes (50%)
            (1140, "0.60".to_string()), // 19 minutes (60%)
            (1200, "0.70".to_string()), // 20 minutes (70%)
        ];

        let result = prepare_time_series(raw_data, current_timestamp, 3);
        assert_eq!(result, vec![0.5, 0.6, 0.7]);
    }

    #[test]
    fn test_prepare_time_series_missing_data() {
        let current_timestamp = 1200; // 20 minutes
        let raw_data = vec![
            (1080, "0.50".to_string()), // 18 minutes (50%)
            // 19 minutes missing
            (1200, "0.70".to_string()), // 20 minutes (70%)
        ];

        let result = prepare_time_series(raw_data, current_timestamp, 3);
        assert_eq!(result, vec![0.5, 0.0, 0.7]); // Missing minute filled with 0
    }

    #[test]
    fn test_prepare_time_series_invalid_strings() {
        let current_timestamp = 1200;
        let raw_data = vec![
            (1080, "invalid".to_string()),
            (1140, "0.60".to_string()),
            (1200, "".to_string()),
        ];

        let result = prepare_time_series(raw_data, current_timestamp, 3);
        assert_eq!(result, vec![0.0, 0.6, 0.0]); // Invalid strings become 0
    }

    #[test]
    fn test_scale_up_high_utilization() {
        // High utilization (>70%) triggers scale up
        let raw_data = vec![
            (1080, "95".to_string()),
            (1140, "98".to_string()),
            (1200, "92".to_string()),
        ];

        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.1);

        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(12)); // 10 * 1.2 = 12
    }

    #[test]
    fn test_scale_up_moderate_utilization() {
        // Moderate-high utilization (>70%) still triggers scale up
        let raw_data = vec![
            (1080, "75".to_string()),
            (1140, "80".to_string()),
            (1200, "78".to_string()),
        ];

        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.1);

        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(12)); // 10 * 1.2 = 12
    }

    #[test]
    fn test_scale_down_low_utilization() {
        // Low utilization (<30%) triggers scale down
        let raw_data = vec![
            (1080, "25".to_string()),
            (1140, "20".to_string()),
            (1200, "28".to_string()),
        ];

        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.1);

        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8)); // 10 * 0.8 = 8
    }

    #[test]
    fn test_scale_down_very_low_utilization() {
        // Very low utilization (<30%) still triggers same scale down
        let raw_data = vec![
            (1080, "5".to_string()),
            (1140, "8".to_string()),
            (1200, "6".to_string()),
        ];

        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.1);

        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8)); // 10 * 0.8 = 8
    }

    /// Scale-to-zero: utilization-based scale-down must never return 0.
    /// Scale to 0 is only allowed via the explicit 30-minute no-invocation path in work/mod.rs.
    #[test]
    fn test_extreme_scale_down_from_one_instance_never_returns_zero() {
        let raw_data = vec![
            (1080, "5".to_string()),
            (1140, "8".to_string()),
            (1200, "6".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 1, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1));
    }

    /// Scale-to-zero: moderate scale-down from 1 instance must never return 0.
    #[test]
    fn test_moderate_scale_down_from_one_instance_never_returns_zero() {
        let raw_data = vec![
            (1080, "25".to_string()),
            (1140, "20".to_string()),
            (1200, "28".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 1, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1));
    }

    /// Scale-down from 2 instances with extreme low util: 2*0.8=1.6 floor=1, stays 1 (not 0).
    #[test]
    fn test_extreme_scale_down_from_two_instances_clamps_to_one() {
        let raw_data = vec![
            (1080, "5".to_string()),
            (1140, "8".to_string()),
            (1200, "6".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 2, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1));
    }

    #[test]
    fn test_no_scaling_optimal_range() {
        // Utilization between 30-70% maintains current instances
        let raw_data = vec![
            (1080, "45".to_string()),
            (1140, "50".to_string()),
            (1200, "55".to_string()),
        ];

        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.1);

        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10)); // No change
    }

    #[test]
    fn test_instantaneous_uses_last_point() {
        // Without stickiness, scaling uses the most recent data point only.
        // Last point is 5 → below scale_down_threshold (30) → scale down
        let raw_data = vec![
            (1080, "90".to_string()), // oldest - high
            (1140, "90".to_string()), // middle - high
            (1200, "5".to_string()),  // newest - low (this is what matters)
        ];

        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.8);

        // Last point is 5 < 30 → scale down: floor(10 * 0.8) = 8
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8));
    }

    #[test]
    fn test_empty_data() {
        let result = get_desired_instances(vec![], &default_policy(), 5, 0.1);

        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(5)); // Returns current instances
        assert_eq!(result.as_ref().map(|d| d.average_utilization), Some(0.0)); // No utilization for empty data
    }

    /// With no utilization data and 0 current instances, we return 1 (never 0) to avoid scaling to 0 from empty metrics.
    #[test]
    fn test_empty_data_with_zero_current_returns_at_least_one() {
        let result = get_desired_instances(vec![], &default_policy(), 0, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1));
        assert_eq!(result.as_ref().map(|d| d.average_utilization), Some(0.0));
    }

    #[test]
    fn test_scaling_decision_structure() {
        let raw_data = vec![
            (1080, "80".to_string()),
            (1140, "85".to_string()),
            (1200, "90".to_string()),
        ];

        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.1);

        assert!(result.is_some());
        let decision = result.unwrap();
        assert_eq!(decision.desired_instances, 12); // 10 * 1.2 = 12 (moderate scale up)
        assert!(decision.average_utilization > 80.0); // Should be around 85
        assert!(decision.average_utilization < 90.0); // But less than 90
    }

    // ---- Boundary condition tests ----

    #[test]
    fn test_boundary_exact_scale_up_threshold_does_not_scale() {
        // Utilization exactly at 70.0 should NOT trigger scale up (strict >)
        let raw_data = vec![
            (1080, "70".to_string()),
            (1140, "70".to_string()),
            (1200, "70".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_boundary_exact_scale_down_threshold_does_not_scale() {
        // Utilization exactly at 30.0 should NOT trigger scale down (strict <)
        let raw_data = vec![
            (1080, "30".to_string()),
            (1140, "30".to_string()),
            (1200, "30".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_boundary_just_above_scale_up_threshold() {
        let raw_data = vec![
            (1080, "70.1".to_string()),
            (1140, "70.1".to_string()),
            (1200, "70.1".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(12)); // 10 * 1.2
    }

    #[test]
    fn test_boundary_just_below_scale_down_threshold() {
        let raw_data = vec![
            (1080, "29.9".to_string()),
            (1140, "29.9".to_string()),
            (1200, "29.9".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8)); // 10 * 0.8
    }

    // ---- Custom threshold/factor tests ----

    #[test]
    fn test_custom_thresholds_narrow_band() {
        // Tight band: scale up > 60, scale down < 40
        let thresholds = ScalingThresholds {
            scale_up_threshold: 60.0,
            scale_down_threshold: 40.0,
        };
        let raw_data = vec![
            (1080, "55".to_string()),
            (1140, "55".to_string()),
            (1200, "55".to_string()),
        ];
        let result = get_desired_instances(
            raw_data,
            &policy_with(thresholds, default_factors()),
            10,
            0.0,
        );
        // 55 is in (40, 60) optimal zone
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_custom_aggressive_scale_up_factor() {
        let factors = ScalingFactors {
            scale_up_factor: 2.0,
            scale_down_factor: 0.5,
        };
        let raw_data = vec![
            (1080, "90".to_string()),
            (1140, "90".to_string()),
            (1200, "90".to_string()),
        ];
        let result = get_desired_instances(
            raw_data,
            &policy_with(default_thresholds(), factors),
            10,
            0.0,
        );
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(20)); // 10 * 2.0
    }

    #[test]
    fn test_custom_aggressive_scale_down_factor() {
        let factors = ScalingFactors {
            scale_up_factor: 1.5,
            scale_down_factor: 0.5,
        };
        let raw_data = vec![
            (1080, "5".to_string()),
            (1140, "5".to_string()),
            (1200, "5".to_string()),
        ];
        let result = get_desired_instances(
            raw_data,
            &policy_with(default_thresholds(), factors),
            10,
            0.0,
        );
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(5)); // 10 * 0.5
    }

    #[test]
    fn test_scale_up_from_single_instance() {
        // ceil(1 * 1.2) = ceil(1.2) = 2
        let raw_data = vec![
            (1080, "90".to_string()),
            (1140, "90".to_string()),
            (1200, "90".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 1, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(2));
    }

    #[test]
    fn test_scale_down_factor_zero_point_five_from_three_clamps_to_one() {
        // floor(3 * 0.5) = floor(1.5) = 1
        let factors = ScalingFactors {
            scale_up_factor: 1.5,
            scale_down_factor: 0.5,
        };
        let raw_data = vec![
            (1080, "5".to_string()),
            (1140, "5".to_string()),
            (1200, "5".to_string()),
        ];
        let result = get_desired_instances(
            raw_data,
            &policy_with(default_thresholds(), factors),
            3,
            0.0,
        );
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1)); // floor(1.5) = 1
    }

    // ---- Large instance count tests ----

    #[test]
    fn test_large_instance_count_scale_up() {
        let raw_data = vec![
            (1080, "90".to_string()),
            (1140, "90".to_string()),
            (1200, "90".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 1000, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1200)); // 1000 * 1.2
    }

    #[test]
    fn test_large_instance_count_scale_down() {
        let raw_data = vec![
            (1080, "5".to_string()),
            (1140, "5".to_string()),
            (1200, "5".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 1000, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(800)); // 1000 * 0.8
    }

    // ---- Decay factor edge cases ----

    #[test]
    fn test_zero_decay_factor_gives_uniform_average() {
        // With decay_factor=0, all values weighted equally: (90+10+50)/3 = 50
        let raw_data = vec![
            (1080, "90".to_string()),
            (1140, "10".to_string()),
            (1200, "50".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.0);
        // 50.0 is in optimal range (30-70), no scaling
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_high_decay_factor_ignores_old_data() {
        // Very high decay: only the newest value matters
        // Oldest (90) gets weight ≈ 0, newest (10) gets weight = 1
        let raw_data = vec![
            (1080, "90".to_string()),
            (1140, "90".to_string()),
            (1200, "10".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 10, 5.0);
        // Weighted avg ≈ 10 which is < 30, scale down
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8)); // 10 * 0.8
    }

    // ---- Utilization value edge cases ----

    #[test]
    fn test_utilization_above_100_still_scales_up() {
        let raw_data = vec![
            (1080, "150".to_string()),
            (1140, "200".to_string()),
            (1200, "180".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(12)); // still scales up
    }

    #[test]
    fn test_all_zero_utilization_scales_down() {
        let raw_data = vec![
            (1080, "0".to_string()),
            (1140, "0".to_string()),
            (1200, "0".to_string()),
        ];
        let result = get_desired_instances(raw_data, &default_policy(), 10, 0.0);
        // 0 < 30, scale down: floor(10 * 0.8) = 8
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8));
    }

    // ---- Stickiness tests ----

    #[test]
    fn test_stickiness_scale_up_met() {
        // 5 data points, all above threshold 20 → stickiness (window=5, required=3) met
        let raw_data = vec![
            (1080, "25".to_string()),
            (1140, "30".to_string()),
            (1200, "28".to_string()),
            (1260, "35".to_string()),
            (1320, "22".to_string()),
        ];
        let policy = ResolvedScalingPolicy {
            thresholds: ScalingThresholds {
                scale_up_threshold: 20.0,
                scale_down_threshold: 10.0,
            },
            factors: ScalingFactors {
                scale_up_factor: 2.0,
                scale_down_factor: 0.8,
            },
            scale_up_stickiness: Some(Stickiness {
                window_minutes: 5,
                required_minutes: 3,
            }),
            scale_down_stickiness: None,
        };
        let result = get_desired_instances(raw_data, &policy, 10, 0.0);
        // All 5 points > 20, need 3 → scale up: 10 * 2.0 = 20
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(20));
    }

    #[test]
    fn test_stickiness_scale_up_not_met() {
        // Only 2 of 5 points above threshold → stickiness (required=3) NOT met
        let raw_data = vec![
            (1080, "25".to_string()),
            (1140, "15".to_string()),
            (1200, "12".to_string()),
            (1260, "18".to_string()),
            (1320, "22".to_string()),
        ];
        let policy = ResolvedScalingPolicy {
            thresholds: ScalingThresholds {
                scale_up_threshold: 20.0,
                scale_down_threshold: 10.0,
            },
            factors: ScalingFactors {
                scale_up_factor: 2.0,
                scale_down_factor: 0.8,
            },
            scale_up_stickiness: Some(Stickiness {
                window_minutes: 5,
                required_minutes: 3,
            }),
            scale_down_stickiness: None,
        };
        let result = get_desired_instances(raw_data, &policy, 10, 0.0);
        // Only 2 points > 20, need 3 → no scale up, stays at 10
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_stickiness_scale_down_met() {
        // 4 of 5 points below threshold 30 → stickiness (required=3) met
        let raw_data = vec![
            (1080, "10".to_string()),
            (1140, "15".to_string()),
            (1200, "50".to_string()), // above threshold
            (1260, "8".to_string()),
            (1320, "12".to_string()),
        ];
        let policy = ResolvedScalingPolicy {
            thresholds: ScalingThresholds {
                scale_up_threshold: 70.0,
                scale_down_threshold: 30.0,
            },
            factors: ScalingFactors {
                scale_up_factor: 1.2,
                scale_down_factor: 0.5,
            },
            scale_up_stickiness: None,
            scale_down_stickiness: Some(Stickiness {
                window_minutes: 5,
                required_minutes: 3,
            }),
        };
        let result = get_desired_instances(raw_data, &policy, 10, 0.0);
        // 4 points < 30, need 3 → scale down: floor(10 * 0.5) = 5
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(5));
    }

    #[test]
    fn test_stickiness_window_smaller_than_data() {
        // 10 data points but stickiness window is 3 → only last 3 considered
        let raw_data = vec![
            (1080, "90".to_string()),
            (1140, "90".to_string()),
            (1200, "90".to_string()),
            (1260, "90".to_string()),
            (1320, "90".to_string()),
            (1380, "90".to_string()),
            (1440, "90".to_string()),
            (1500, "15".to_string()), // last 3 are low
            (1560, "10".to_string()),
            (1620, "12".to_string()),
        ];
        let policy = ResolvedScalingPolicy {
            thresholds: ScalingThresholds {
                scale_up_threshold: 70.0,
                scale_down_threshold: 30.0,
            },
            factors: ScalingFactors {
                scale_up_factor: 1.5,
                scale_down_factor: 0.8,
            },
            scale_up_stickiness: Some(Stickiness {
                window_minutes: 3,
                required_minutes: 2,
            }),
            scale_down_stickiness: Some(Stickiness {
                window_minutes: 3,
                required_minutes: 2,
            }),
        };
        let result = get_desired_instances(raw_data, &policy, 10, 0.0);
        // Last 3 points: [15, 10, 12] — all < 30, 3 >= 2 required → scale down
        // floor(10 * 0.8) = 8
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8));
    }
}

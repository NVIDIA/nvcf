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

/// Configuration for custom (per-function) scaling policies.
/// The gRPC endpoint used to fetch per-function policies is the same NVCF
/// API Autoscaler service the scaling loop reports back to, so it is
/// configured once via `nvcf_api.nvcf_api_grpc_address` rather than here.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct CustomScalingPolicyConfig {
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
    historical_utilization: &[f64],
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

/// Which Prometheus metric family drives a function's scaling decision.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum MetricSource {
    /// Worker-thread busy ratio. The default; emitted by managed worker pods.
    WorkerThreads,
    /// Control-plane request-latency / instance metrics. Used for BYOC and any
    /// other function that never emits worker series.
    ControlPlane,
}

impl MetricSource {
    /// Whether this source uses the control-plane utilization query.
    pub fn uses_control_plane_metrics(self) -> bool {
        matches!(self, MetricSource::ControlPlane)
    }
}

/// Everything a scaling decision needs, gathered once from the timeseries DB.
///
/// All NaN/parse sanitization happens while building this (see
/// [`sanitize_utilization`]), so every downstream path operates on the same
/// shape regardless of which [`MetricSource`] produced it. That is what keeps
/// scale-to-zero behaving identically for worker-metric and control-plane
/// functions.
#[derive(Debug, Clone)]
pub struct ScalingInputs {
    pub metric_source: MetricSource,
    pub current_instances: usize,
    /// Chronologically ordered, finite utilization percentages. Non-finite or
    /// unparsable samples have already been coerced to 0.0.
    pub utilization_samples: Vec<f64>,
    /// True when the function was invoked within the scale-to-zero idle window.
    pub recently_invoked: bool,
}

/// Parse raw timeseries `(timestamp, value)` rows into chronologically ordered,
/// finite utilization samples. This is the single place utilization strings are
/// turned into numbers.
///
/// Prometheus encodes 0/0 (e.g. the control-plane utilization query at zero
/// instances) as the string "NaN". Left as `f64::NAN` it poisons the weighted
/// average and silently disables the `< scale_down_threshold` scale-to-zero
/// guard, because every NaN comparison is false. Coerce every non-finite or
/// unparsable sample to 0.0 so all paths see a finite series.
pub fn sanitize_utilization(raw: Vec<(i64, String)>) -> Vec<f64> {
    let mut rows = raw;
    // Sort by timestamp so oldest is first — decay weighting and the
    // "most recent point" logic both expect chronological order.
    rows.sort_by_key(|(ts, _)| *ts);
    rows.into_iter()
        .map(|(_, v)| {
            v.parse::<f64>()
                .ok()
                .filter(|x| x.is_finite())
                .unwrap_or(0.0)
        })
        .collect()
}

#[derive(Debug, Clone, PartialEq)]
pub struct ScalingDecision {
    pub desired_instances: usize,
    pub average_utilization: f32,
}

pub fn get_desired_instances(
    utilization_samples: &[f64],
    policy: &ResolvedScalingPolicy,
    current_instances: usize,
    decay_factor: f32,
) -> Option<ScalingDecision> {
    // Return current instances if no data is available.
    if utilization_samples.is_empty() {
        tracing::debug!(
            "No historical utilization, so returning current instances and zero utilization"
        );
        return Some(ScalingDecision {
            desired_instances: current_instances.max(1),
            average_utilization: 0.0,
        });
    }

    let avg_utilization = calculate_average_utilization(utilization_samples, decay_factor)?;
    let base_instances = current_instances.max(1);

    let up = policy.thresholds.scale_up_threshold;
    let down = policy.thresholds.scale_down_threshold;
    let scale_up = breached_enough(
        utilization_samples,
        policy.scale_up_stickiness.as_ref(),
        "scale-up",
        |v| v > up,
    );
    let scale_down = breached_enough(
        utilization_samples,
        policy.scale_down_stickiness.as_ref(),
        "scale-down",
        |v| v < down,
    );

    let desired_instances = if scale_up {
        ((base_instances as f32) * policy.factors.scale_up_factor).ceil() as usize
    } else if scale_down {
        // Floor so we actually reduce (ceil would get stuck), clamped to 1.
        // Reaching 0 is only allowed via the scale-to-zero path in decide_scaling.
        ((base_instances as f32) * policy.factors.scale_down_factor)
            .floor()
            .max(1.0) as usize
    } else {
        base_instances
    };
    Some(ScalingDecision {
        desired_instances,
        average_utilization: avg_utilization,
    })
}

/// Whether enough of the recent utilization samples satisfy `breaches`.
///
/// With stickiness, at least `required_minutes` of the last `window_minutes`
/// samples must breach. Without it, the check is instantaneous, which is just
/// the same rule over a window of one (only the most recent sample counts).
/// `label` is used only for the trace line so the gate is debuggable in prod.
fn breached_enough(
    samples: &[f64],
    stickiness: Option<&Stickiness>,
    label: &str,
    breaches: impl Fn(f32) -> bool,
) -> bool {
    let (window, required) = match stickiness {
        Some(s) => (s.window_minutes as usize, s.required_minutes as usize),
        None => (1, 1),
    };
    let recent = &samples[samples.len().saturating_sub(window)..];
    let count = recent.iter().filter(|&&v| breaches(v as f32)).count();
    tracing::debug!(
        "{} gate: {} of {} recent sample(s) breached (need {}, window {})",
        label,
        count,
        recent.len(),
        required,
        window
    );
    count >= required
}

/// Decide the final desired instance count for a function from its gathered
/// inputs. This is the single decision path shared by every metric source.
///
/// Returns `None` to mean "make no scaling request this cycle." That happens
/// when the function reports 0 active instances but we asked for `>0` last
/// cycle (`last_predicted_instance_count`): the new workers simply have not
/// reported into the metrics yet, so the 0 is stale and acting on it would
/// fight an in-flight scale-up.
///
/// Otherwise returns `Some(decision)`:
///   1. utilization-driven up/down via [`get_desired_instances`], then
///   2. scale-to-zero: override to 0 only when the function is past its idle
///      window (`recently_invoked` is false) and average utilization is below
///      the scale-down threshold. This is the only path that reaches 0.
pub fn decide_scaling(
    inputs: &ScalingInputs,
    policy: &ResolvedScalingPolicy,
    decay_factor: f32,
    last_predicted_instance_count: usize,
) -> Option<ScalingDecision> {
    if inputs.current_instances == 0 && last_predicted_instance_count > 0 {
        return None;
    }

    // get_desired_instances floors a 0 reading to a base of 1, so the scale
    // factors never multiply by zero.
    let mut decision = get_desired_instances(
        &inputs.utilization_samples,
        policy,
        inputs.current_instances,
        decay_factor,
    )?;

    let idle_and_underutilized = !inputs.recently_invoked
        && decision.average_utilization < policy.thresholds.scale_down_threshold;
    if idle_and_underutilized {
        decision.desired_instances = 0;
    }

    Some(decision)
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

    fn inputs_with(
        current_instances: usize,
        utilization_samples: Vec<f64>,
        recently_invoked: bool,
    ) -> ScalingInputs {
        ScalingInputs {
            metric_source: MetricSource::WorkerThreads,
            current_instances,
            utilization_samples,
            recently_invoked,
        }
    }

    // ---- sanitize_utilization: the single NaN/parse/sort point ----

    #[test]
    fn test_sanitize_parses_and_sorts_chronologically() {
        // Out-of-order rows are sorted oldest-first so decay weighting is correct.
        let raw = vec![
            (1200, "0.70".to_string()),
            (1080, "0.50".to_string()),
            (1140, "0.60".to_string()),
        ];
        assert_eq!(sanitize_utilization(raw), vec![0.5, 0.6, 0.7]);
    }

    #[test]
    fn test_sanitize_invalid_strings_become_zero() {
        let raw = vec![
            (1080, "invalid".to_string()),
            (1140, "0.60".to_string()),
            (1200, "".to_string()),
        ];
        assert_eq!(sanitize_utilization(raw), vec![0.0, 0.6, 0.0]);
    }

    /// Prometheus encodes 0/0 (e.g. the CP utilization query at zero instances)
    /// as "NaN", and infinities can appear too. Both must become 0.0 so the
    /// average stays finite and the scale-to-zero guard keeps working.
    #[test]
    fn test_sanitize_non_finite_become_zero() {
        let raw = vec![
            (1080, "NaN".to_string()),
            (1140, "+Inf".to_string()),
            (1200, "-Inf".to_string()),
        ];
        let samples = sanitize_utilization(raw);
        assert!(samples.iter().all(|x| x.is_finite()));
        assert_eq!(samples, vec![0.0, 0.0, 0.0]);
    }

    #[test]
    fn test_sanitize_empty() {
        assert!(sanitize_utilization(vec![]).is_empty());
    }

    // ---- get_desired_instances (operates on sanitized f64 samples) ----

    #[test]
    fn test_scale_up_high_utilization() {
        let result = get_desired_instances(&[95.0, 98.0, 92.0], &default_policy(), 10, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(12)); // 10 * 1.2
    }

    #[test]
    fn test_scale_up_moderate_utilization() {
        let result = get_desired_instances(&[75.0, 80.0, 78.0], &default_policy(), 10, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(12)); // 10 * 1.2
    }

    #[test]
    fn test_scale_down_low_utilization() {
        let result = get_desired_instances(&[25.0, 20.0, 28.0], &default_policy(), 10, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8)); // 10 * 0.8
    }

    #[test]
    fn test_scale_down_very_low_utilization() {
        let result = get_desired_instances(&[5.0, 8.0, 6.0], &default_policy(), 10, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8)); // 10 * 0.8
    }

    /// Utilization-based scale-down must never return 0; scale to 0 is only
    /// reachable via decide_scaling's idle path.
    #[test]
    fn test_extreme_scale_down_from_one_instance_never_returns_zero() {
        let result = get_desired_instances(&[5.0, 8.0, 6.0], &default_policy(), 1, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1));
    }

    #[test]
    fn test_moderate_scale_down_from_one_instance_never_returns_zero() {
        let result = get_desired_instances(&[25.0, 20.0, 28.0], &default_policy(), 1, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1));
    }

    #[test]
    fn test_extreme_scale_down_from_two_instances_clamps_to_one() {
        let result = get_desired_instances(&[5.0, 8.0, 6.0], &default_policy(), 2, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1));
    }

    #[test]
    fn test_no_scaling_optimal_range() {
        let result = get_desired_instances(&[45.0, 50.0, 55.0], &default_policy(), 10, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_instantaneous_uses_last_point() {
        // Without stickiness, scaling uses the most recent (last) data point only.
        let result = get_desired_instances(&[90.0, 90.0, 5.0], &default_policy(), 10, 0.8);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8)); // 5 < 30 -> floor(10*0.8)
    }

    #[test]
    fn test_empty_data() {
        let result = get_desired_instances(&[], &default_policy(), 5, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(5));
        assert_eq!(result.as_ref().map(|d| d.average_utilization), Some(0.0));
    }

    #[test]
    fn test_empty_data_with_zero_current_returns_at_least_one() {
        let result = get_desired_instances(&[], &default_policy(), 0, 0.1);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1));
        assert_eq!(result.as_ref().map(|d| d.average_utilization), Some(0.0));
    }

    #[test]
    fn test_scaling_decision_structure() {
        let result = get_desired_instances(&[80.0, 85.0, 90.0], &default_policy(), 10, 0.1);
        let decision = result.expect("decision");
        assert_eq!(decision.desired_instances, 12); // 10 * 1.2
        assert!(decision.average_utilization > 80.0);
        assert!(decision.average_utilization < 90.0);
    }

    // ---- Boundary condition tests ----

    #[test]
    fn test_boundary_exact_scale_up_threshold_does_not_scale() {
        let result = get_desired_instances(&[70.0, 70.0, 70.0], &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_boundary_exact_scale_down_threshold_does_not_scale() {
        let result = get_desired_instances(&[30.0, 30.0, 30.0], &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_boundary_just_above_scale_up_threshold() {
        let result = get_desired_instances(&[70.1, 70.1, 70.1], &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(12));
    }

    #[test]
    fn test_boundary_just_below_scale_down_threshold() {
        let result = get_desired_instances(&[29.9, 29.9, 29.9], &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8));
    }

    // ---- Custom threshold/factor tests ----

    #[test]
    fn test_custom_thresholds_narrow_band() {
        let thresholds = ScalingThresholds {
            scale_up_threshold: 60.0,
            scale_down_threshold: 40.0,
        };
        let result = get_desired_instances(
            &[55.0, 55.0, 55.0],
            &policy_with(thresholds, default_factors()),
            10,
            0.0,
        );
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_custom_aggressive_scale_up_factor() {
        let factors = ScalingFactors {
            scale_up_factor: 2.0,
            scale_down_factor: 0.5,
        };
        let result = get_desired_instances(
            &[90.0, 90.0, 90.0],
            &policy_with(default_thresholds(), factors),
            10,
            0.0,
        );
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(20));
    }

    #[test]
    fn test_custom_aggressive_scale_down_factor() {
        let factors = ScalingFactors {
            scale_up_factor: 1.5,
            scale_down_factor: 0.5,
        };
        let result = get_desired_instances(
            &[5.0, 5.0, 5.0],
            &policy_with(default_thresholds(), factors),
            10,
            0.0,
        );
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(5));
    }

    #[test]
    fn test_scale_up_from_single_instance() {
        let result = get_desired_instances(&[90.0, 90.0, 90.0], &default_policy(), 1, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(2)); // ceil(1*1.2)
    }

    #[test]
    fn test_scale_down_factor_zero_point_five_from_three_clamps_to_one() {
        let factors = ScalingFactors {
            scale_up_factor: 1.5,
            scale_down_factor: 0.5,
        };
        let result = get_desired_instances(
            &[5.0, 5.0, 5.0],
            &policy_with(default_thresholds(), factors),
            3,
            0.0,
        );
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1)); // floor(1.5)
    }

    // ---- Large instance count tests ----

    #[test]
    fn test_large_instance_count_scale_up() {
        let result = get_desired_instances(&[90.0, 90.0, 90.0], &default_policy(), 1000, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(1200));
    }

    #[test]
    fn test_large_instance_count_scale_down() {
        let result = get_desired_instances(&[5.0, 5.0, 5.0], &default_policy(), 1000, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(800));
    }

    // ---- Decay factor edge cases ----

    #[test]
    fn test_zero_decay_factor_gives_uniform_average() {
        // (90+10+50)/3 = 50, in optimal range -> no scaling
        let result = get_desired_instances(&[90.0, 10.0, 50.0], &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_high_decay_factor_ignores_old_data() {
        // Very high decay: only the newest value (10) matters -> scale down
        let result = get_desired_instances(&[90.0, 90.0, 10.0], &default_policy(), 10, 5.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8));
    }

    // ---- Utilization value edge cases ----

    #[test]
    fn test_utilization_above_100_still_scales_up() {
        let result = get_desired_instances(&[150.0, 200.0, 180.0], &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(12));
    }

    #[test]
    fn test_all_zero_utilization_scales_down() {
        let result = get_desired_instances(&[0.0, 0.0, 0.0], &default_policy(), 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8));
    }

    // ---- Stickiness tests ----

    #[test]
    fn test_stickiness_scale_up_met() {
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
        let result = get_desired_instances(&[25.0, 30.0, 28.0, 35.0, 22.0], &policy, 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(20)); // 10 * 2.0
    }

    #[test]
    fn test_stickiness_scale_up_not_met() {
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
        // Only 2 of 5 points above 20 -> required 3 not met
        let result = get_desired_instances(&[25.0, 15.0, 12.0, 18.0, 22.0], &policy, 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(10));
    }

    #[test]
    fn test_stickiness_scale_down_met() {
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
        // 4 of 5 points below 30 -> required 3 met
        let result = get_desired_instances(&[10.0, 15.0, 50.0, 8.0, 12.0], &policy, 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(5)); // floor(10*0.5)
    }

    #[test]
    fn test_stickiness_window_smaller_than_data() {
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
        // Last 3 points [15,10,12] all < 30 -> scale down floor(10*0.8)
        let samples = [90.0, 90.0, 90.0, 90.0, 90.0, 90.0, 90.0, 15.0, 10.0, 12.0];
        let result = get_desired_instances(&samples, &policy, 10, 0.0);
        assert_eq!(result.as_ref().map(|d| d.desired_instances), Some(8));
    }

    // ---- decide_scaling: the single decision path shared by all metric sources ----

    fn scaled(desired_instances: usize, average_utilization: f32) -> Option<ScalingDecision> {
        Some(ScalingDecision {
            desired_instances,
            average_utilization,
        })
    }

    /// Idle (not recently invoked) + low utilization -> scale to zero, for any
    /// metric source.
    #[test]
    fn test_decide_scale_to_zero_when_idle_and_low_util() {
        let inputs = inputs_with(2, vec![5.0, 6.0, 4.0], false);
        assert_eq!(
            decide_scaling(&inputs, &default_policy(), 0.0, 2),
            scaled(0, 5.0)
        );
    }

    /// Recently invoked must never scale to zero, even at low utilization.
    #[test]
    fn test_decide_no_scale_to_zero_when_recently_invoked() {
        let inputs = inputs_with(2, vec![5.0, 6.0, 4.0], true);
        // Low util but invoked -> utilization-based scale down, clamped to >=1.
        assert_eq!(
            decide_scaling(&inputs, &default_policy(), 0.0, 2),
            scaled(1, 5.0)
        );
    }

    /// Idle but utilization still high -> honor the scale decision, do not zero.
    #[test]
    fn test_decide_no_scale_to_zero_when_idle_but_high_util() {
        let inputs = inputs_with(10, vec![90.0, 90.0, 90.0], false);
        assert_eq!(
            decide_scaling(&inputs, &default_policy(), 0.0, 10),
            scaled(12, 90.0)
        );
    }

    /// 0 active but instances were requested last cycle -> stale reading, skip.
    #[test]
    fn test_decide_skips_when_requested_but_none_active() {
        let inputs = inputs_with(0, vec![], false);
        assert_eq!(decide_scaling(&inputs, &default_policy(), 0.0, 5), None);
    }

    /// No active instances and nothing requested: base of 1, and an idle low-util
    /// function still scales to zero (not skipped).
    #[test]
    fn test_decide_zero_current_zero_requested_idle_scales_to_zero() {
        let inputs = inputs_with(0, vec![0.0, 0.0, 0.0], false);
        assert_eq!(
            decide_scaling(&inputs, &default_policy(), 0.0, 0),
            scaled(0, 0.0)
        );
    }

    /// Non-finite samples sanitized upstream keep average finite, so scale-to-zero
    /// still fires (regression guard for the CP "NaN" encoding).
    #[test]
    fn test_decide_with_sanitized_non_finite_scales_to_zero() {
        let samples = sanitize_utilization(vec![
            (1080, "NaN".to_string()),
            (1140, "NaN".to_string()),
            (1200, "NaN".to_string()),
        ]);
        let inputs = inputs_with(1, samples, false);
        assert_eq!(
            decide_scaling(&inputs, &default_policy(), 0.0, 1),
            scaled(0, 0.0)
        );
    }
}

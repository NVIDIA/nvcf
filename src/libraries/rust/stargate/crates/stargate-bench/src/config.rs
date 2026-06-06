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

use std::path::Path;

use anyhow::{Context, ensure};
use serde::{Deserialize, Serialize};
use stargate_protocol::TunnelTransportProtocol;

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct BenchmarkConfig {
    pub name: String,
    #[serde(default)]
    pub metadata: ScenarioMetadata,
    pub model: String,
    #[serde(default)]
    pub seed: Option<u64>,
    pub request_count: usize,
    pub max_concurrency: usize,
    #[serde(default)]
    pub tunnel_protocol: TunnelTransportProtocol,
    #[serde(default)]
    pub stargates: StargateConfig,
    pub backends: BackendConfig,
    pub traffic_pattern: TrafficPatternConfig,
    #[serde(default)]
    pub degradation: DegradationConfig,
    #[serde(default)]
    pub algorithms: Vec<AlgorithmConfig>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct ScenarioMetadata {
    #[serde(default)]
    pub description: Option<String>,
    #[serde(default)]
    pub tags: Vec<String>,
    #[serde(default)]
    pub expected_runtime: Option<String>,
    #[serde(default)]
    pub expected_signal: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct DegradationConfig {
    #[serde(default)]
    pub actions: Vec<DegradationActionConfig>,
}

#[derive(Debug, Clone, Serialize, PartialEq)]
pub struct DegradationActionConfig {
    pub at_request: usize,
    pub backend_index: usize,
    #[serde(flatten)]
    pub action: DegradationActionKind,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
#[serde(tag = "action", rename_all = "snake_case")]
pub enum DegradationActionKind {
    DeleteBackendPod,
    ScaleBackend { replicas: u32 },
}

impl<'de> Deserialize<'de> for DegradationActionConfig {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        deserializer.deserialize_map(DegradationActionConfigVisitor)
    }
}

struct DegradationActionConfigVisitor;

impl<'de> serde::de::Visitor<'de> for DegradationActionConfigVisitor {
    type Value = DegradationActionConfig;

    fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("a degradation action")
    }

    fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
    where
        M: serde::de::MapAccess<'de>,
    {
        let mut at_request = None;
        let mut backend_index = None;
        let mut action = None;
        let mut replicas = None;

        while let Some(field) = map.next_key::<DegradationActionField>()? {
            match field {
                DegradationActionField::AtRequest => {
                    if at_request.replace(map.next_value()?).is_some() {
                        return Err(serde::de::Error::duplicate_field("at_request"));
                    }
                }
                DegradationActionField::BackendIndex => {
                    if backend_index.replace(map.next_value()?).is_some() {
                        return Err(serde::de::Error::duplicate_field("backend_index"));
                    }
                }
                DegradationActionField::Action => {
                    if action.replace(map.next_value::<String>()?).is_some() {
                        return Err(serde::de::Error::duplicate_field("action"));
                    }
                }
                DegradationActionField::Replicas => {
                    if replicas.replace(map.next_value()?).is_some() {
                        return Err(serde::de::Error::duplicate_field("replicas"));
                    }
                }
            }
        }

        let at_request = at_request.ok_or_else(|| serde::de::Error::missing_field("at_request"))?;
        let backend_index =
            backend_index.ok_or_else(|| serde::de::Error::missing_field("backend_index"))?;
        let action_name = action.ok_or_else(|| serde::de::Error::missing_field("action"))?;
        let action = match action_name.as_str() {
            "delete_backend_pod" => {
                if replicas.is_some() {
                    return Err(serde::de::Error::unknown_field(
                        "replicas",
                        DEGRADATION_ACTION_FIELDS,
                    ));
                }
                DegradationActionKind::DeleteBackendPod
            }
            "scale_backend" => DegradationActionKind::ScaleBackend {
                replicas: replicas.ok_or_else(|| serde::de::Error::missing_field("replicas"))?,
            },
            other => {
                return Err(serde::de::Error::unknown_variant(
                    other,
                    &["delete_backend_pod", "scale_backend"],
                ));
            }
        };

        Ok(DegradationActionConfig {
            at_request,
            backend_index,
            action,
        })
    }
}

const DEGRADATION_ACTION_FIELDS: &[&str] = &["at_request", "backend_index", "action", "replicas"];

enum DegradationActionField {
    AtRequest,
    BackendIndex,
    Action,
    Replicas,
}

impl<'de> Deserialize<'de> for DegradationActionField {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        struct FieldVisitor;

        impl<'de> serde::de::Visitor<'de> for FieldVisitor {
            type Value = DegradationActionField;

            fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                formatter.write_str("a degradation action field")
            }

            fn visit_str<E>(self, value: &str) -> Result<Self::Value, E>
            where
                E: serde::de::Error,
            {
                match value {
                    "at_request" => Ok(DegradationActionField::AtRequest),
                    "backend_index" => Ok(DegradationActionField::BackendIndex),
                    "action" => Ok(DegradationActionField::Action),
                    "replicas" => Ok(DegradationActionField::Replicas),
                    other => Err(serde::de::Error::unknown_field(
                        other,
                        DEGRADATION_ACTION_FIELDS,
                    )),
                }
            }
        }

        deserializer.deserialize_identifier(FieldVisitor)
    }
}

impl DegradationConfig {
    pub fn validate(&self, request_count: usize, backend_count: usize) -> anyhow::Result<()> {
        for action in &self.actions {
            ensure!(
                action.at_request < request_count,
                "degradation action at_request must be less than request_count"
            );
            ensure!(
                action.backend_index < backend_count,
                "degradation action backend_index must be less than backends.count"
            );
        }
        Ok(())
    }
}

impl BenchmarkConfig {
    pub fn load(path: &Path) -> anyhow::Result<Self> {
        let bytes = std::fs::read(path)
            .with_context(|| format!("failed to read benchmark config {}", path.display()))?;
        let ext = path
            .extension()
            .and_then(|ext| ext.to_str())
            .unwrap_or_default();
        match ext {
            "yaml" | "yml" => serde_yaml_ng::from_slice(&bytes).with_context(|| {
                format!("failed to parse YAML benchmark config {}", path.display())
            }),
            _ => serde_json::from_slice(&bytes).with_context(|| {
                format!("failed to parse JSON benchmark config {}", path.display())
            }),
        }
    }

    pub fn validate(&self) -> anyhow::Result<()> {
        self.backends.validate()?;
        self.degradation
            .validate(self.request_count, self.backends.count)?;
        validate_traffic_pattern(&self.traffic_pattern)?;
        for algorithm in &self.algorithms {
            if let Some(pylon_queue_admission) = &algorithm.pylon_queue_admission {
                pylon_queue_admission.validate()?;
            }
        }
        Ok(())
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct StargateConfig {
    #[serde(default = "default_stargate_count")]
    pub count: usize,
}

impl Default for StargateConfig {
    fn default() -> Self {
        Self {
            count: default_stargate_count(),
        }
    }
}

fn default_stargate_count() -> usize {
    1
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct BackendConfig {
    pub count: usize,
    #[serde(default)]
    pub cluster_id_template: Option<String>,
    #[serde(default = "default_pylons_per_cluster")]
    pub pylons_per_cluster: usize,
    pub profile: BackendProfile,
    #[serde(default)]
    pub profiles: Vec<BackendProfileGroup>,
}

fn default_pylons_per_cluster() -> usize {
    1
}

impl BackendConfig {
    pub fn validate(&self) -> anyhow::Result<()> {
        ensure!(self.count > 0, "backends.count must be > 0");
        ensure!(
            self.pylons_per_cluster > 0,
            "backends.pylons_per_cluster must be > 0"
        );
        if let Some(template) = &self.cluster_id_template {
            ensure!(
                !template.trim().is_empty(),
                "backends.cluster_id_template must not be empty when set"
            );
            if template.contains("{cluster_index}") {
                ensure!(
                    self.count.is_multiple_of(self.pylons_per_cluster),
                    "backends.count must be divisible by backends.pylons_per_cluster when using {{cluster_index}}"
                );
            } else {
                ensure!(
                    self.pylons_per_cluster == 1,
                    "backends.pylons_per_cluster requires cluster_id_template to contain {{cluster_index}}"
                );
            }
        } else {
            ensure!(
                self.pylons_per_cluster == 1,
                "backends.pylons_per_cluster requires backends.cluster_id_template"
            );
        }
        validate_profile(&self.profile)?;
        if !self.profiles.is_empty() {
            let mut total = 0usize;
            for group in &self.profiles {
                ensure!(group.count > 0, "backend profile counts must be > 0");
                validate_profile(&group.profile)?;
                total = total
                    .checked_add(group.count)
                    .context("sum of backend profile counts overflowed usize")?;
            }
            ensure!(
                total == self.count,
                "sum of backends.profiles counts must equal backends.count"
            );
        }

        let mut first_index_by_cluster = std::collections::BTreeMap::new();
        for index in 0..self.count {
            let cluster_id = self.effective_cluster_id_for_index(index);
            if let Some(first_index) = first_index_by_cluster.get(&cluster_id) {
                ensure!(
                    self.profile_for_index(*first_index) == self.profile_for_index(index),
                    "shared routing cluster must use identical backend profiles: {cluster_id}"
                );
            } else {
                first_index_by_cluster.insert(cluster_id, index);
            }
        }
        for first_index in first_index_by_cluster.into_values() {
            let profile = self.profile_for_index(first_index);
            let pylon_count = self.pylon_count_for_upstream(first_index);
            if let Some(max_concurrent_requests) = profile.max_concurrent_requests {
                max_concurrent_requests
                    .checked_mul(pylon_count)
                    .context("shared routing cluster max_concurrent_requests overflowed usize")?;
            }
            profile
                .kv_cache_capacity_tokens
                .checked_mul(pylon_count as u64)
                .context("shared routing cluster kv_cache_capacity_tokens overflowed u64")?;
        }
        Ok(())
    }

    pub fn profile_for_index(&self, index: usize) -> &BackendProfile {
        assert!(
            index < self.count,
            "backend index must be less than backend count"
        );
        if self.profiles.is_empty() {
            return &self.profile;
        }

        let mut start = 0usize;
        for group in &self.profiles {
            let end = start + group.count;
            if index < end {
                return &group.profile;
            }
            start = end;
        }

        &self.profile
    }

    pub fn cluster_id_for_index(&self, index: usize) -> Option<String> {
        assert!(
            index < self.count,
            "backend index must be less than backend count"
        );
        self.cluster_id_template.as_ref().map(|template| {
            template
                .replace(
                    "{cluster_index}",
                    &(index / self.pylons_per_cluster).to_string(),
                )
                .replace("{backend_index}", &index.to_string())
        })
    }

    pub fn effective_cluster_id_for_index(&self, index: usize) -> String {
        self.cluster_id_for_index(index)
            .unwrap_or_else(|| format!("backend-{index}"))
    }

    pub fn upstream_index_for_index(&self, index: usize) -> usize {
        let cluster_id = self.effective_cluster_id_for_index(index);
        (0..=index)
            .find(|candidate| self.effective_cluster_id_for_index(*candidate) == cluster_id)
            .expect("the current backend must belong to its own routing cluster")
    }

    pub fn upstream_indices(&self) -> Vec<usize> {
        (0..self.count)
            .filter(|index| self.upstream_index_for_index(*index) == *index)
            .collect()
    }

    pub fn pylon_count_for_upstream(&self, upstream_index: usize) -> usize {
        assert!(
            self.upstream_index_for_index(upstream_index) == upstream_index,
            "backend index must identify a shared upstream"
        );
        let cluster_id = self.effective_cluster_id_for_index(upstream_index);
        (0..self.count)
            .filter(|index| self.effective_cluster_id_for_index(*index) == cluster_id)
            .count()
    }

    pub fn cluster_count(&self) -> usize {
        (0..self.count)
            .map(|index| self.effective_cluster_id_for_index(index))
            .collect::<std::collections::BTreeSet<_>>()
            .len()
    }
}

fn validate_profile(profile: &BackendProfile) -> anyhow::Result<()> {
    ensure!(
        profile.service_time_ms.decode_tokens_per_s > 0,
        "backend decode_tokens_per_s must be > 0"
    );
    if let Some(prefill_tokens_per_s) = profile.service_time_ms.prefill_tokens_per_s {
        ensure!(
            prefill_tokens_per_s > 0.0 && prefill_tokens_per_s.is_finite(),
            "backend prefill_tokens_per_s must be finite and > 0 when set"
        );
    }
    ensure!(
        profile.registration.last_mean_input_tps > 0.0
            && profile.registration.last_mean_input_tps.is_finite(),
        "backend registration.last_mean_input_tps must be finite and > 0"
    );
    Ok(())
}

fn validate_traffic_pattern(pattern: &TrafficPatternConfig) -> anyhow::Result<()> {
    match pattern {
        TrafficPatternConfig::Bursty(config) => ensure!(
            config.burst_period_requests > 0,
            "burst_period_requests must be > 0"
        ),
        TrafficPatternConfig::StairStep(config) => {
            ensure!(config.step_requests > 0, "step_requests must be > 0");
        }
        TrafficPatternConfig::PrefixReuse(config) => ensure!(
            config.cache_affinity_keys > 0,
            "prefix_reuse cache_affinity_keys must be > 0"
        ),
        TrafficPatternConfig::Uniform(_)
        | TrafficPatternConfig::ZipfHotset(_)
        | TrafficPatternConfig::MixedSize(_) => {}
    }
    Ok(())
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct BackendProfileGroup {
    pub count: usize,
    pub profile: BackendProfile,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct BackendProfile {
    #[serde(default = "default_backend_name")]
    pub name: String,
    #[serde(default = "default_backend_weight")]
    pub weight: f64,
    #[serde(default)]
    pub max_concurrent_requests: Option<usize>,
    #[serde(default)]
    pub kv_cache_capacity_tokens: u64,
    pub service_time_ms: ServiceTimeConfig,
    pub registration: RegistrationConfig,
}

fn default_backend_name() -> String {
    "default".to_string()
}

fn default_backend_weight() -> f64 {
    1.0
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct ServiceTimeConfig {
    pub ttft_mean: u64,
    #[serde(default)]
    pub ttft_jitter_ms: u64,
    pub decode_tokens_per_s: u64,
    #[serde(default)]
    pub decode_jitter_ms: u64,
    #[serde(default)]
    pub prefill_tokens_per_s: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct RegistrationConfig {
    pub last_mean_input_tps: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum TrafficPatternConfig {
    Uniform(UniformTrafficConfig),
    ZipfHotset(HotsetTrafficConfig),
    Bursty(BurstyTrafficConfig),
    StairStep(StairStepTrafficConfig),
    MixedSize(MixedSizeTrafficConfig),
    PrefixReuse(PrefixReuseTrafficConfig),
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct UniformTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
    pub arrival: ArrivalPatternConfig,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct HotsetTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub hotset_fraction: f64,
    pub hotset_share: f64,
    pub input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
    pub arrival: ArrivalPatternConfig,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct BurstyTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
    pub quiet_rps: f64,
    pub burst_rps: f64,
    pub burst_period_requests: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct StairStepTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
    pub start_rps: f64,
    pub step_rps: f64,
    pub step_requests: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct MixedSizeTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub arrival: ArrivalPatternConfig,
    pub small: MixedSizeClassConfig,
    pub large: MixedSizeClassConfig,
    pub small_share: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct MixedSizeClassConfig {
    pub input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct PrefixReuseTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub initial_input_tokens: TokenDistributionConfig,
    pub incremental_input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
    pub arrival: ArrivalPatternConfig,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
#[serde(tag = "distribution", rename_all = "snake_case")]
pub enum TokenDistributionConfig {
    Constant {
        value: u64,
    },
    Uniform {
        min: u64,
        max: u64,
    },
    Lognormal {
        mean: f64,
        sigma: f64,
        #[serde(default)]
        min: Option<u64>,
        #[serde(default)]
        p99_cap: Option<u64>,
    },
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
#[serde(tag = "distribution", rename_all = "snake_case")]
pub enum ArrivalPatternConfig {
    Constant { interval_ms: u64 },
    Poisson { target_rps: f64 },
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct AlgorithmConfig {
    pub name: String,
    pub config: serde_json::Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pylon_queue_admission: Option<PylonQueueAdmissionConfig>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct PylonQueueAdmissionConfig {
    pub enabled: bool,
    #[serde(default)]
    pub min_delta_ms: Option<u64>,
    #[serde(default)]
    pub tolerance_factor: Option<f64>,
    #[serde(default)]
    pub retry_after_ms: Option<u64>,
}

impl PylonQueueAdmissionConfig {
    fn validate(&self) -> anyhow::Result<()> {
        if let Some(tolerance_factor) = self.tolerance_factor {
            ensure!(
                tolerance_factor.is_finite() && tolerance_factor > 0.0,
                "pylon queue admission tolerance_factor must be finite and > 0 when set"
            );
        }
        Ok(())
    }

    pub fn pylon_args(&self) -> Vec<String> {
        let mut args = vec![format!(
            "--pylon-queue-mismatch-retry-enabled={}",
            self.enabled
        )];
        if let Some(min_delta_ms) = self.min_delta_ms {
            args.push(format!(
                "--pylon-queue-mismatch-min-delta-ms={min_delta_ms}"
            ));
        }
        if let Some(tolerance_factor) = self.tolerance_factor {
            args.push(format!(
                "--pylon-queue-mismatch-tolerance-factor={tolerance_factor}"
            ));
        }
        if let Some(retry_after_ms) = self.retry_after_ms {
            args.push(format!(
                "--pylon-queue-mismatch-retry-after-ms={retry_after_ms}"
            ));
        }
        args
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_webtransport_tunnel_protocol_from_yaml() {
        let config: BenchmarkConfig = serde_yaml_ng::from_str(
            r#"
name: webtransport
model: dummy-model
request_count: 1
max_concurrency: 1
tunnel_protocol: webtransport
backends:
  count: 1
  profile:
    service_time_ms:
      ttft_mean: 10
      decode_tokens_per_s: 100
    registration:
      last_mean_input_tps: 100.0
traffic_pattern:
  kind: uniform
  routing_keys: 1
  cache_affinity_keys: 1
  input_tokens:
    distribution: constant
    value: 10
  output_tokens:
    distribution: constant
    value: 5
  arrival:
    distribution: constant
    interval_ms: 1
"#,
        )
        .expect("config should parse");

        assert_eq!(
            config.tunnel_protocol,
            TunnelTransportProtocol::WebTransport
        );
        assert_eq!(config.tunnel_protocol.to_string(), "webtransport");
    }

    #[test]
    fn parses_degradation_actions_from_yaml() {
        let config: BenchmarkConfig = serde_yaml_ng::from_str(
            r#"
name: degradation
model: dummy-model
request_count: 10
max_concurrency: 2
backends:
  count: 1
  profile:
    service_time_ms:
      ttft_mean: 10
      decode_tokens_per_s: 100
    registration:
      last_mean_input_tps: 100.0
traffic_pattern:
  kind: uniform
  routing_keys: 1
  cache_affinity_keys: 1
  input_tokens:
    distribution: constant
    value: 10
  output_tokens:
    distribution: constant
    value: 5
  arrival:
    distribution: constant
    interval_ms: 1
degradation:
  actions:
    - at_request: 3
      backend_index: 0
      action: delete_backend_pod
    - at_request: 5
      backend_index: 0
      action: scale_backend
      replicas: 2
"#,
        )
        .expect("config should parse");

        assert_eq!(
            config.degradation.actions,
            vec![
                DegradationActionConfig {
                    at_request: 3,
                    backend_index: 0,
                    action: DegradationActionKind::DeleteBackendPod,
                },
                DegradationActionConfig {
                    at_request: 5,
                    backend_index: 0,
                    action: DegradationActionKind::ScaleBackend { replicas: 2 },
                },
            ]
        );
    }

    #[test]
    fn parses_per_algorithm_pylon_queue_admission_variants_from_yaml() {
        let config: BenchmarkConfig = serde_yaml_ng::from_str(
            r#"
name: queue-admission-ab
model: dummy-model
request_count: 1
max_concurrency: 1
backends:
  count: 1
  profile:
    service_time_ms:
      ttft_mean: 10
      decode_tokens_per_s: 100
    registration:
      last_mean_input_tps: 100.0
traffic_pattern:
  kind: uniform
  routing_keys: 0
  cache_affinity_keys: 0
  input_tokens:
    distribution: constant
    value: 10
  output_tokens:
    distribution: constant
    value: 5
  arrival:
    distribution: constant
    interval_ms: 1
algorithms:
  - name: queue-admission-enabled
    config:
      default: groq-multiregion
    pylon_queue_admission:
      enabled: true
      min_delta_ms: 0
      tolerance_factor: 1.0
      retry_after_ms: 5
  - name: queue-admission-disabled
    config:
      default: groq-multiregion
    pylon_queue_admission:
      enabled: false
      min_delta_ms: 0
      tolerance_factor: 1.0
      retry_after_ms: 5
"#,
        )
        .expect("A/B admission configuration should parse");

        let enabled = config.algorithms[0]
            .pylon_queue_admission
            .as_ref()
            .expect("enabled variant should configure pylon admission");
        assert!(enabled.enabled);
        assert_eq!(enabled.min_delta_ms, Some(0));
        assert_eq!(enabled.tolerance_factor, Some(1.0));
        assert_eq!(enabled.retry_after_ms, Some(5));
        assert_eq!(
            config.algorithms[0].config, config.algorithms[1].config,
            "A/B variants should be able to retain an identical routing configuration"
        );
        assert!(
            !config.algorithms[1]
                .pylon_queue_admission
                .as_ref()
                .expect("disabled variant should configure pylon admission")
                .enabled
        );
    }

    #[test]
    fn rejects_degradation_actions_outside_run_bounds() {
        let config = DegradationConfig {
            actions: vec![DegradationActionConfig {
                at_request: 10,
                backend_index: 0,
                action: DegradationActionKind::DeleteBackendPod,
            }],
        };
        assert!(config.validate(10, 1).is_err());

        let config = DegradationConfig {
            actions: vec![DegradationActionConfig {
                at_request: 9,
                backend_index: 1,
                action: DegradationActionKind::DeleteBackendPod,
            }],
        };
        assert!(config.validate(10, 1).is_err());
    }

    #[test]
    fn rejects_invalid_registered_input_throughput() {
        let profile = BackendProfile {
            name: "invalid-throughput".to_string(),
            weight: 1.0,
            max_concurrent_requests: None,
            kv_cache_capacity_tokens: 0,
            service_time_ms: ServiceTimeConfig {
                ttft_mean: 10,
                ttft_jitter_ms: 0,
                decode_tokens_per_s: 100,
                decode_jitter_ms: 0,
                prefill_tokens_per_s: Some(100.0),
            },
            registration: RegistrationConfig {
                last_mean_input_tps: 0.0,
            },
        };

        let error = validate_profile(&profile)
            .expect_err("zero registered input throughput should be rejected");
        assert!(
            error
                .to_string()
                .contains("registration.last_mean_input_tps")
        );
    }

    #[test]
    fn rejects_unknown_top_level_config_fields() {
        let err = serde_yaml_ng::from_str::<BenchmarkConfig>(
            r#"
name: unknown-top-level
model: dummy-model
request_count: 1
max_concurrency: 1
extra: true
backends:
  count: 1
  profile:
    service_time_ms:
      ttft_mean: 10
      decode_tokens_per_s: 100
    registration:
      last_mean_input_tps: 100.0
traffic_pattern:
  kind: uniform
  routing_keys: 1
  cache_affinity_keys: 1
  input_tokens:
    distribution: constant
    value: 10
  output_tokens:
    distribution: constant
    value: 5
  arrival:
    distribution: constant
    interval_ms: 1
"#,
        )
        .expect_err("unknown top-level benchmark config field should fail");

        assert!(
            err.to_string().contains("unknown field `extra`"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn rejects_unknown_nested_config_fields() {
        let err = serde_yaml_ng::from_str::<BenchmarkConfig>(
            r#"
name: unknown-nested
model: dummy-model
request_count: 1
max_concurrency: 1
backends:
  count: 1
  unexpected: true
  profile:
    service_time_ms:
      ttft_mean: 10
      decode_tokens_per_s: 100
    registration:
      last_mean_input_tps: 100.0
traffic_pattern:
  kind: uniform
  routing_keys: 1
  cache_affinity_keys: 1
  input_tokens:
    distribution: constant
    value: 10
  output_tokens:
    distribution: constant
    value: 5
  arrival:
    distribution: constant
    interval_ms: 1
"#,
        )
        .expect_err("unknown nested benchmark config field should fail");

        assert!(
            err.to_string().contains("unknown field `unexpected`"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn validates_bursty_period_is_nonzero() {
        let config: BenchmarkConfig = serde_yaml_ng::from_str(
            r#"
name: bursty-zero-period
model: dummy-model
request_count: 2
max_concurrency: 1
backends:
  count: 1
  profile:
    service_time_ms:
      ttft_mean: 10
      decode_tokens_per_s: 100
    registration:
      last_mean_input_tps: 100.0
traffic_pattern:
  kind: bursty
  routing_keys: 1
  cache_affinity_keys: 1
  input_tokens:
    distribution: constant
    value: 10
  output_tokens:
    distribution: constant
    value: 5
  quiet_rps: 1.0
  burst_rps: 2.0
  burst_period_requests: 0
"#,
        )
        .expect("config should parse");

        let err = config
            .validate()
            .expect_err("zero burst_period_requests should fail validation");
        assert!(
            err.to_string()
                .contains("burst_period_requests must be > 0"),
            "unexpected error: {err:#}"
        );
    }

    #[test]
    fn validates_stair_step_requests_is_nonzero() {
        let config: BenchmarkConfig = serde_yaml_ng::from_str(
            r#"
name: stair-step-zero-period
model: dummy-model
request_count: 2
max_concurrency: 1
backends:
  count: 1
  profile:
    service_time_ms:
      ttft_mean: 10
      decode_tokens_per_s: 100
    registration:
      last_mean_input_tps: 100.0
traffic_pattern:
  kind: stair_step
  routing_keys: 1
  cache_affinity_keys: 1
  input_tokens:
    distribution: constant
    value: 10
  output_tokens:
    distribution: constant
    value: 5
  start_rps: 1.0
  step_rps: 1.0
  step_requests: 0
"#,
        )
        .expect("config should parse");

        let err = config
            .validate()
            .expect_err("zero step_requests should fail validation");
        assert!(
            err.to_string().contains("step_requests must be > 0"),
            "unexpected error: {err:#}"
        );
    }

    #[test]
    fn expands_grouped_pylon_cluster_id_template() {
        let config: BenchmarkConfig = serde_yaml_ng::from_str(
            r#"
name: grouped-pylons
model: dummy-model
request_count: 1
max_concurrency: 1
backends:
  count: 4
  pylons_per_cluster: 2
  cluster_id_template: cluster-{cluster_index}
  profile:
    service_time_ms:
      ttft_mean: 10
      decode_tokens_per_s: 100
    registration:
      last_mean_input_tps: 100.0
traffic_pattern:
  kind: uniform
  routing_keys: 0
  cache_affinity_keys: 0
  input_tokens:
    distribution: constant
    value: 10
  output_tokens:
    distribution: constant
    value: 5
  arrival:
    distribution: constant
    interval_ms: 1
"#,
        )
        .expect("grouped pylon configuration should parse");

        config
            .validate()
            .expect("complete grouped topology should validate");
        assert_eq!(
            (0..4)
                .map(|index| config.backends.cluster_id_for_index(index).unwrap())
                .collect::<Vec<_>>(),
            vec!["cluster-0", "cluster-0", "cluster-1", "cluster-1"]
        );
    }

    #[test]
    fn rejects_partial_grouped_pylon_cluster_topology() {
        let config: BenchmarkConfig = serde_yaml_ng::from_str(
            r#"
name: partial-grouped-pylons
model: dummy-model
request_count: 1
max_concurrency: 1
backends:
  count: 5
  pylons_per_cluster: 2
  cluster_id_template: cluster-{cluster_index}
  profile:
    service_time_ms:
      ttft_mean: 10
      decode_tokens_per_s: 100
    registration:
      last_mean_input_tps: 100.0
traffic_pattern:
  kind: uniform
  routing_keys: 0
  cache_affinity_keys: 0
  input_tokens:
    distribution: constant
    value: 10
  output_tokens:
    distribution: constant
    value: 5
  arrival:
    distribution: constant
    interval_ms: 1
"#,
        )
        .expect("grouped pylon configuration should parse");

        let error = config
            .validate()
            .expect_err("partial grouped topology should be rejected");
        assert!(
            error
                .to_string()
                .contains("divisible by backends.pylons_per_cluster"),
            "unexpected error: {error:#}"
        );
    }

    #[test]
    fn rejects_different_profiles_within_shared_cluster() {
        let config: BenchmarkConfig = serde_yaml_ng::from_str(
            r#"
name: mixed-shared-cluster
model: dummy-model
request_count: 1
max_concurrency: 1
backends:
  count: 2
  pylons_per_cluster: 2
  cluster_id_template: cluster-{cluster_index}
  profile:
    service_time_ms:
      ttft_mean: 10
      decode_tokens_per_s: 100
    registration:
      last_mean_input_tps: 100.0
  profiles:
    - count: 1
      profile:
        name: first
        service_time_ms:
          ttft_mean: 10
          decode_tokens_per_s: 100
        registration:
          last_mean_input_tps: 100.0
    - count: 1
      profile:
        name: second
        service_time_ms:
          ttft_mean: 20
          decode_tokens_per_s: 100
        registration:
          last_mean_input_tps: 100.0
traffic_pattern:
  kind: uniform
  routing_keys: 0
  cache_affinity_keys: 0
  input_tokens:
    distribution: constant
    value: 10
  output_tokens:
    distribution: constant
    value: 5
  arrival:
    distribution: constant
    interval_ms: 1
"#,
        )
        .expect("shared-cluster configuration should parse");

        let error = config
            .validate()
            .expect_err("a shared cache cluster cannot have mixed mock profiles");
        assert!(
            error
                .to_string()
                .contains("shared routing cluster must use identical backend profiles"),
            "unexpected error: {error:#}"
        );
    }
}

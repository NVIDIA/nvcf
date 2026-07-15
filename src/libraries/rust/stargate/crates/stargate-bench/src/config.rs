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

macro_rules! config_struct {
    ($name:ident $(, $derive:ident)* { $($fields:tt)* }) => {
        #[derive(Debug, Clone, Serialize, Deserialize, PartialEq $(, $derive)*)]
        #[serde(deny_unknown_fields)]
        pub struct $name { $($fields)* }
    };
}

config_struct!(BenchmarkConfig {
    pub name: String,
    #[serde(default)]
    pub metadata: ScenarioMetadata,
    pub model: String,
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
});

config_struct!(ScenarioMetadata, Default {
    pub description: Option<String>,
    #[serde(default)]
    pub tags: Vec<String>,
    pub expected_runtime: Option<String>,
    pub expected_signal: Option<String>,
});

config_struct!(DegradationConfig, Default {
    #[serde(default)]
    pub actions: Vec<DegradationActionConfig>,
});

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(try_from = "RawDegradationActionConfig")]
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

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RawDegradationActionConfig {
    at_request: usize,
    backend_index: usize,
    action: RawDegradationActionName,
    replicas: Option<u32>,
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum RawDegradationActionName {
    DeleteBackendPod,
    ScaleBackend,
}

impl TryFrom<RawDegradationActionConfig> for DegradationActionConfig {
    type Error = &'static str;

    fn try_from(raw: RawDegradationActionConfig) -> Result<Self, Self::Error> {
        use DegradationActionKind::{DeleteBackendPod, ScaleBackend};
        use RawDegradationActionName as RawAction;

        let action = match (raw.action, raw.replicas) {
            (RawAction::DeleteBackendPod, None) => DeleteBackendPod,
            (RawAction::DeleteBackendPod, Some(_)) => {
                return Err("replicas is only valid for scale_backend degradation actions");
            }
            (RawAction::ScaleBackend, Some(replicas)) => ScaleBackend { replicas },
            (RawAction::ScaleBackend, None) => {
                return Err("scale_backend degradation actions require replicas");
            }
        };
        Ok(Self {
            at_request: raw.at_request,
            backend_index: raw.backend_index,
            action,
        })
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
        self.algorithms
            .iter()
            .filter_map(|algorithm| algorithm.pylon_queue_admission.as_ref())
            .try_for_each(PylonQueueAdmissionConfig::validate)?;
        Ok(())
    }
}

config_struct!(StargateConfig {
    #[serde(default = "default_count")]
    pub count: usize,
});

impl Default for StargateConfig {
    fn default() -> Self {
        Self {
            count: default_count(),
        }
    }
}

fn default_count() -> usize {
    1
}

config_struct!(BackendConfig {
    pub count: usize,
    pub cluster_id_template: Option<String>,
    #[serde(default = "default_count")]
    pub pylons_per_cluster: usize,
    pub profile: BackendProfile,
    #[serde(default)]
    pub profiles: Vec<BackendProfileGroup>,
});

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
            let grouped = template.contains("{cluster_index}");
            ensure!(
                grouped || self.pylons_per_cluster == 1,
                "backends.pylons_per_cluster requires cluster_id_template to contain {{cluster_index}}"
            );
            ensure!(
                !grouped || self.count.is_multiple_of(self.pylons_per_cluster),
                "backends.count must be divisible by backends.pylons_per_cluster when using {{cluster_index}}"
            );
        } else {
            ensure!(
                self.pylons_per_cluster == 1,
                "backends.pylons_per_cluster requires backends.cluster_id_template"
            );
        }
        validate_profile(&self.profile)?;
        let profile_count = self
            .profiles
            .iter()
            .try_fold(0usize, |profile_count, group| {
                ensure!(group.count > 0, "backend profile counts must be > 0");
                validate_profile(&group.profile)?;
                profile_count
                    .checked_add(group.count)
                    .context("sum of backend profile counts overflowed usize")
            })?;
        ensure!(
            self.profiles.is_empty() || profile_count == self.count,
            "sum of backends.profiles counts must equal backends.count"
        );

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
            let cluster_index = (index / self.pylons_per_cluster).to_string();
            template
                .replace("{cluster_index}", &cluster_index)
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
        let mut seen = std::collections::BTreeSet::new();
        (0..self.count)
            .filter(|index| seen.insert(self.effective_cluster_id_for_index(*index)))
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
        self.upstream_indices().len()
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
        TrafficPatternConfig::MixedSize(config) => ensure!(
            (0.0..=1.0).contains(&config.small_share),
            "small_share must be in [0, 1]"
        ),
        TrafficPatternConfig::Uniform(_) | TrafficPatternConfig::ZipfHotset(_) => {}
    }
    Ok(())
}

config_struct!(BackendProfileGroup {
    pub count: usize,
    pub profile: BackendProfile,
});

config_struct!(BackendProfile {
    #[serde(default = "default_backend_name")]
    pub name: String,
    #[serde(default = "default_backend_weight")]
    pub weight: f64,
    pub max_concurrent_requests: Option<usize>,
    #[serde(default)]
    pub kv_cache_capacity_tokens: u64,
    pub service_time_ms: ServiceTimeConfig,
    pub registration: RegistrationConfig,
});

fn default_backend_name() -> String {
    "default".to_string()
}

fn default_backend_weight() -> f64 {
    1.0
}

config_struct!(ServiceTimeConfig {
    pub ttft_mean: u64,
    #[serde(default)]
    pub ttft_jitter_ms: u64,
    pub decode_tokens_per_s: u64,
    #[serde(default)]
    pub decode_jitter_ms: u64,
    pub prefill_tokens_per_s: Option<f64>,
});

config_struct!(RegistrationConfig {
    pub last_mean_input_tps: f64,
});

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

config_struct!(UniformTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
    pub arrival: ArrivalPatternConfig,
});

config_struct!(HotsetTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub hotset_fraction: f64,
    pub hotset_share: f64,
    pub input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
    pub arrival: ArrivalPatternConfig,
});

config_struct!(BurstyTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
    pub quiet_rps: f64,
    pub burst_rps: f64,
    pub burst_period_requests: usize,
});

config_struct!(StairStepTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
    pub start_rps: f64,
    pub step_rps: f64,
    pub step_requests: usize,
});

config_struct!(MixedSizeTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub arrival: ArrivalPatternConfig,
    pub small: MixedSizeClassConfig,
    pub large: MixedSizeClassConfig,
    pub small_share: f64,
});

config_struct!(MixedSizeClassConfig {
    pub input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
});

config_struct!(PrefixReuseTrafficConfig {
    pub routing_keys: usize,
    pub cache_affinity_keys: usize,
    pub initial_input_tokens: TokenDistributionConfig,
    pub incremental_input_tokens: TokenDistributionConfig,
    pub output_tokens: TokenDistributionConfig,
    pub arrival: ArrivalPatternConfig,
});

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
        min: Option<u64>,
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

config_struct!(AlgorithmConfig {
    pub name: String,
    pub config: serde_json::Value,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pylon_queue_admission: Option<PylonQueueAdmissionConfig>,
});

config_struct!(PylonQueueAdmissionConfig {
    pub enabled: bool,
    pub min_delta_ms: Option<u64>,
    pub tolerance_factor: Option<f64>,
    pub retry_after_ms: Option<u64>,
});

impl PylonQueueAdmissionConfig {
    fn validate(&self) -> anyhow::Result<()> {
        ensure!(
            self.tolerance_factor
                .is_none_or(|factor| factor.is_finite() && factor > 0.0),
            "pylon queue admission tolerance_factor must be finite and > 0 when set"
        );
        Ok(())
    }

    pub fn pylon_args(&self) -> Vec<String> {
        let mut args = vec![format!(
            "--pylon-queue-mismatch-retry-enabled={}",
            self.enabled
        )];
        if let Some(value) = self.min_delta_ms {
            args.push(format!("--pylon-queue-mismatch-min-delta-ms={value}"));
        }
        if let Some(value) = self.tolerance_factor {
            args.push(format!("--pylon-queue-mismatch-tolerance-factor={value}"));
        }
        if let Some(value) = self.retry_after_ms {
            args.push(format!("--pylon-queue-mismatch-retry-after-ms={value}"));
        }
        args
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn profile(name: &str) -> BackendProfile {
        BackendProfile {
            name: name.to_string(),
            weight: 1.0,
            max_concurrent_requests: None,
            kv_cache_capacity_tokens: 0,
            service_time_ms: ServiceTimeConfig {
                ttft_mean: 10,
                ttft_jitter_ms: 0,
                decode_tokens_per_s: 100,
                decode_jitter_ms: 0,
                prefill_tokens_per_s: None,
            },
            registration: RegistrationConfig {
                last_mean_input_tps: 100.0,
            },
        }
    }

    fn uniform_traffic() -> TrafficPatternConfig {
        TrafficPatternConfig::Uniform(UniformTrafficConfig {
            routing_keys: 1,
            cache_affinity_keys: 1,
            input_tokens: TokenDistributionConfig::Constant { value: 10 },
            output_tokens: TokenDistributionConfig::Constant { value: 5 },
            arrival: ArrivalPatternConfig::Constant { interval_ms: 1 },
        })
    }

    fn benchmark_config() -> BenchmarkConfig {
        BenchmarkConfig {
            name: "test".to_string(),
            metadata: ScenarioMetadata::default(),
            model: "dummy-model".to_string(),
            seed: None,
            request_count: 10,
            max_concurrency: 2,
            tunnel_protocol: TunnelTransportProtocol::default(),
            stargates: StargateConfig::default(),
            backends: BackendConfig {
                count: 1,
                cluster_id_template: None,
                pylons_per_cluster: 1,
                profile: profile("default"),
                profiles: Vec::new(),
            },
            traffic_pattern: uniform_traffic(),
            degradation: DegradationConfig::default(),
            algorithms: Vec::new(),
        }
    }

    fn yaml_round_trip(config: &BenchmarkConfig) -> BenchmarkConfig {
        let yaml = serde_yaml_ng::to_string(config).expect("config should serialize");
        serde_yaml_ng::from_str(&yaml).expect("serialized config should parse")
    }

    fn parse_yaml_value(value: serde_yaml_ng::Value) -> BenchmarkConfig {
        let yaml = serde_yaml_ng::to_string(&value).expect("fixture should serialize");
        serde_yaml_ng::from_str(&yaml).expect("fixture should parse")
    }

    fn insert_yaml_field(value: &mut serde_yaml_ng::Value, key: &str) {
        value
            .as_mapping_mut()
            .expect("fixture should be a mapping")
            .insert(
                serde_yaml_ng::Value::String(key.to_string()),
                serde_yaml_ng::Value::Bool(true),
            );
    }

    fn remove_yaml_field(value: &mut serde_yaml_ng::Value, key: &str) {
        let removed = value
            .as_mapping_mut()
            .expect("fixture should be a mapping")
            .remove(serde_yaml_ng::Value::String(key.to_string()));
        assert!(removed.is_some(), "fixture should contain {key}");
    }

    fn assert_validation_error(result: anyhow::Result<()>, expected: &str) {
        let error = result.expect_err("configuration should be rejected");
        assert!(
            error.to_string().contains(expected),
            "unexpected error: {error:#}"
        );
    }

    #[test]
    fn parses_webtransport_tunnel_protocol_from_yaml() {
        let mut expected = benchmark_config();
        expected.tunnel_protocol = TunnelTransportProtocol::WebTransport;
        let config = yaml_round_trip(&expected);

        assert_eq!(
            config.tunnel_protocol,
            TunnelTransportProtocol::WebTransport
        );
        assert_eq!(config.tunnel_protocol.to_string(), "webtransport");
    }

    #[test]
    fn omitted_yaml_fields_retain_public_defaults() {
        let mut yaml = serde_yaml_ng::to_value(benchmark_config()).unwrap();
        for key in [
            "metadata",
            "seed",
            "tunnel_protocol",
            "stargates",
            "degradation",
            "algorithms",
        ] {
            remove_yaml_field(&mut yaml, key);
        }
        for key in ["cluster_id_template", "pylons_per_cluster", "profiles"] {
            remove_yaml_field(&mut yaml["backends"], key);
        }
        for key in [
            "name",
            "weight",
            "max_concurrent_requests",
            "kv_cache_capacity_tokens",
        ] {
            remove_yaml_field(&mut yaml["backends"]["profile"], key);
        }
        for key in ["ttft_jitter_ms", "decode_jitter_ms", "prefill_tokens_per_s"] {
            remove_yaml_field(&mut yaml["backends"]["profile"]["service_time_ms"], key);
        }

        let config = parse_yaml_value(yaml);

        assert_eq!(config.metadata, ScenarioMetadata::default());
        assert_eq!(config.seed, None);
        assert_eq!(config.tunnel_protocol, TunnelTransportProtocol::default());
        assert_eq!(config.stargates, StargateConfig::default());
        assert_eq!(config.degradation, DegradationConfig::default());
        assert!(config.algorithms.is_empty());
        assert_eq!(config.backends.cluster_id_template, None);
        assert_eq!(config.backends.pylons_per_cluster, 1);
        assert!(config.backends.profiles.is_empty());
        assert_eq!(config.backends.profile.name, "default");
        assert_eq!(config.backends.profile.weight, 1.0);
        assert_eq!(config.backends.profile.max_concurrent_requests, None);
        assert_eq!(config.backends.profile.kv_cache_capacity_tokens, 0);
        assert_eq!(config.backends.profile.service_time_ms.ttft_jitter_ms, 0);
        assert_eq!(config.backends.profile.service_time_ms.decode_jitter_ms, 0);
        assert_eq!(
            config.backends.profile.service_time_ms.prefill_tokens_per_s,
            None
        );
    }

    #[test]
    fn parses_degradation_actions_from_yaml() {
        let mut yaml = serde_yaml_ng::to_value(benchmark_config()).unwrap();
        yaml["degradation"] = serde_yaml_ng::from_str(
            r#"
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
        .unwrap();
        let config = parse_yaml_value(yaml);

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
    fn raw_degradation_action_conversion_preserves_and_rejects_variant_shape() {
        let action: DegradationActionConfig = serde_yaml_ng::from_str(
            r#"
at_request: 5
backend_index: 1
action: scale_backend
replicas: 2
"#,
        )
        .expect("scale action should parse");

        assert_eq!(
            action,
            DegradationActionConfig {
                at_request: 5,
                backend_index: 1,
                action: DegradationActionKind::ScaleBackend { replicas: 2 },
            }
        );

        let missing_replicas = serde_yaml_ng::from_str::<DegradationActionConfig>(
            r#"
at_request: 5
backend_index: 1
action: scale_backend
"#,
        )
        .expect_err("scale action should require replicas");
        assert!(missing_replicas.to_string().contains("replicas"));

        let delete_with_replicas = serde_yaml_ng::from_str::<DegradationActionConfig>(
            r#"
at_request: 5
backend_index: 1
action: delete_backend_pod
replicas: 2
"#,
        )
        .expect_err("delete action should reject replicas");
        assert!(delete_with_replicas.to_string().contains("replicas"));

        let unknown_action = serde_yaml_ng::from_str::<DegradationActionConfig>(
            r#"
at_request: 5
backend_index: 1
action: pause_backend
"#,
        )
        .expect_err("unknown action should fail");
        assert!(unknown_action.to_string().contains("pause_backend"));
    }

    #[test]
    fn parses_per_algorithm_pylon_queue_admission_variants_from_yaml() {
        let mut yaml = serde_yaml_ng::to_value(benchmark_config()).unwrap();
        yaml["algorithms"] = serde_yaml_ng::from_str(
            r#"
- name: queue-admission-enabled
  config: { default: groq-multiregion }
  pylon_queue_admission:
    enabled: true
    min_delta_ms: 0
    tolerance_factor: 1.0
    retry_after_ms: 5
- name: queue-admission-disabled
  config: { default: groq-multiregion }
  pylon_queue_admission:
    enabled: false
    min_delta_ms: 0
    tolerance_factor: 1.0
    retry_after_ms: 5
"#,
        )
        .unwrap();
        let config = parse_yaml_value(yaml);

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
        let mut profile = profile("invalid-throughput");
        profile.registration.last_mean_input_tps = 0.0;
        assert_validation_error(
            validate_profile(&profile),
            "registration.last_mean_input_tps",
        );
    }

    #[test]
    fn rejects_unknown_top_level_config_fields() {
        let mut yaml = serde_yaml_ng::to_value(benchmark_config()).unwrap();
        insert_yaml_field(&mut yaml, "extra");
        let yaml = serde_yaml_ng::to_string(&yaml).unwrap();
        let err = serde_yaml_ng::from_str::<BenchmarkConfig>(&yaml)
            .expect_err("unknown top-level benchmark config field should fail");

        assert!(
            err.to_string().contains("unknown field `extra`"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn rejects_unknown_nested_config_fields() {
        let mut yaml = serde_yaml_ng::to_value(benchmark_config()).unwrap();
        insert_yaml_field(&mut yaml["backends"], "unexpected");
        let yaml = serde_yaml_ng::to_string(&yaml).unwrap();
        let err = serde_yaml_ng::from_str::<BenchmarkConfig>(&yaml)
            .expect_err("unknown nested benchmark config field should fail");

        assert!(
            err.to_string().contains("unknown field `unexpected`"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn validates_bursty_period_is_nonzero() {
        let mut config = benchmark_config();
        config.traffic_pattern = TrafficPatternConfig::Bursty(BurstyTrafficConfig {
            routing_keys: 1,
            cache_affinity_keys: 1,
            input_tokens: TokenDistributionConfig::Constant { value: 10 },
            output_tokens: TokenDistributionConfig::Constant { value: 5 },
            quiet_rps: 1.0,
            burst_rps: 2.0,
            burst_period_requests: 0,
        });

        assert_validation_error(config.validate(), "burst_period_requests must be > 0");
    }

    #[test]
    fn validates_stair_step_requests_is_nonzero() {
        let mut config = benchmark_config();
        config.traffic_pattern = TrafficPatternConfig::StairStep(StairStepTrafficConfig {
            routing_keys: 1,
            cache_affinity_keys: 1,
            input_tokens: TokenDistributionConfig::Constant { value: 10 },
            output_tokens: TokenDistributionConfig::Constant { value: 5 },
            start_rps: 1.0,
            step_rps: 1.0,
            step_requests: 0,
        });

        assert_validation_error(config.validate(), "step_requests must be > 0");
    }

    #[test]
    fn expands_grouped_pylon_cluster_id_template() {
        let mut config = benchmark_config();
        config.backends.count = 4;
        config.backends.pylons_per_cluster = 2;
        config.backends.cluster_id_template = Some("cluster-{cluster_index}".to_string());

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
        let mut config = benchmark_config();
        config.backends.count = 5;
        config.backends.pylons_per_cluster = 2;
        config.backends.cluster_id_template = Some("cluster-{cluster_index}".to_string());

        assert_validation_error(
            config.validate(),
            "divisible by backends.pylons_per_cluster",
        );
    }

    #[test]
    fn rejects_different_profiles_within_shared_cluster() {
        let mut config = benchmark_config();
        config.backends.count = 2;
        config.backends.pylons_per_cluster = 2;
        config.backends.cluster_id_template = Some("cluster-{cluster_index}".to_string());
        let first = profile("first");
        let mut second = profile("second");
        second.service_time_ms.ttft_mean = 20;
        config.backends.profiles = vec![
            BackendProfileGroup {
                count: 1,
                profile: first,
            },
            BackendProfileGroup {
                count: 1,
                profile: second,
            },
        ];

        assert_validation_error(
            config.validate(),
            "shared routing cluster must use identical backend profiles",
        );
    }
}

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

use std::collections::HashMap;

use anyhow::{Context, ensure};
use rand::rngs::StdRng;
use rand::{Rng, SeedableRng};
use serde::{Deserialize, Serialize};

use crate::config::{
    ArrivalPatternConfig, BenchmarkConfig, HotsetTrafficConfig, PrefixReuseTrafficConfig,
    ScenarioMetadata, TokenDistributionConfig, TrafficPatternConfig,
};
use crate::runtime::slugify;

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Manifest {
    pub manifest_version: u32,
    pub benchmark_name: String,
    pub metadata: ScenarioMetadata,
    pub model: String,
    pub seed: u64,
    pub request_count: usize,
    pub max_concurrency: usize,
    pub stargate_count: usize,
    pub backend_count: usize,
    #[serde(default)]
    pub cluster_count: usize,
    #[serde(default = "default_pylons_per_cluster")]
    pub pylons_per_cluster: usize,
    pub requests: Vec<ManifestRequest>,
}

fn default_pylons_per_cluster() -> usize {
    1
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ManifestRequest {
    pub request_index: usize,
    pub request_id: String,
    pub scheduled_offset_ms: u64,
    pub routing_key: Option<String>,
    pub cache_affinity_key: Option<String>,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub backend_behavior_class: String,
}

pub fn generate_manifest(
    config: &BenchmarkConfig,
    cli_seed: Option<u64>,
) -> anyhow::Result<Manifest> {
    ensure!(config.request_count > 0, "request_count must be > 0");
    ensure!(config.max_concurrency > 0, "max_concurrency must be > 0");
    ensure!(config.stargates.count > 0, "stargates.count must be > 0");
    config.validate()?;
    let seed = cli_seed.or(config.seed).unwrap_or(0);
    let mut rng = StdRng::seed_from_u64(seed);
    let mut scheduled_offset_ms = 0u64;
    let mut requests = Vec::with_capacity(config.request_count);
    let mut prior_prefix_tokens = HashMap::new();
    let request_id_prefix = format!("{}-{seed}", slugify(&config.name));

    for request_index in 0..config.request_count {
        if request_index > 0 {
            scheduled_offset_ms = scheduled_offset_ms
                .checked_add(next_arrival_ms(
                    &config.traffic_pattern,
                    request_index,
                    &mut rng,
                )?)
                .context("scheduled benchmark offsets overflowed u64 milliseconds")?;
        }

        let request_shape =
            sample_request_shape(&config.traffic_pattern, &mut prior_prefix_tokens, &mut rng)?;
        requests.push(ManifestRequest {
            request_index,
            request_id: format!("{request_id_prefix}-{request_index:06}"),
            scheduled_offset_ms,
            routing_key: make_optional_key("rk", request_shape.routing_key_index),
            cache_affinity_key: make_optional_key("cak", request_shape.cache_affinity_key_index),
            input_tokens: request_shape.input_tokens,
            output_tokens: request_shape.output_tokens,
            backend_behavior_class: request_shape.backend_behavior_class.to_string(),
        });
    }

    Ok(Manifest {
        manifest_version: 1,
        benchmark_name: config.name.clone(),
        metadata: config.metadata.clone(),
        model: config.model.clone(),
        seed,
        request_count: config.request_count,
        max_concurrency: config.max_concurrency,
        stargate_count: config.stargates.count,
        backend_count: config.backends.count,
        cluster_count: config.backends.cluster_count(),
        pylons_per_cluster: config.backends.pylons_per_cluster,
        requests,
    })
}

struct RequestShape {
    routing_key_index: Option<usize>,
    cache_affinity_key_index: Option<usize>,
    input_tokens: u64,
    output_tokens: u64,
    backend_behavior_class: &'static str,
}

macro_rules! sample_common_request {
    ($routing:expr, $tokens:expr, $behavior:expr, $rng:expr) => {
        Ok(RequestShape {
            routing_key_index: sample_optional_index($routing.routing_keys, $rng),
            cache_affinity_key_index: sample_optional_index($routing.cache_affinity_keys, $rng),
            input_tokens: sample_tokens(&$tokens.input_tokens, $rng)?,
            output_tokens: sample_tokens(&$tokens.output_tokens, $rng)?,
            backend_behavior_class: $behavior,
        })
    };
}

fn sample_request_shape(
    pattern: &TrafficPatternConfig,
    prior_prefix_tokens: &mut HashMap<usize, u64>,
    rng: &mut StdRng,
) -> anyhow::Result<RequestShape> {
    match pattern {
        TrafficPatternConfig::Uniform(config) => {
            sample_common_request!(config, config, "uniform", rng)
        }
        TrafficPatternConfig::Bursty(config) => {
            sample_common_request!(config, config, "bursty", rng)
        }
        TrafficPatternConfig::StairStep(config) => {
            sample_common_request!(config, config, "stair_step", rng)
        }
        TrafficPatternConfig::ZipfHotset(config) => sample_hotset_request(config, rng),
        TrafficPatternConfig::MixedSize(config) => {
            let (class, behavior) = if rng.random::<f64>() < config.small_share {
                (&config.small, "small")
            } else {
                (&config.large, "large")
            };
            sample_common_request!(config, class, behavior, rng)
        }
        TrafficPatternConfig::PrefixReuse(config) => {
            sample_prefix_reuse_request(config, prior_prefix_tokens, rng)
        }
    }
}

fn sample_hotset_request(
    config: &HotsetTrafficConfig,
    rng: &mut StdRng,
) -> anyhow::Result<RequestShape> {
    ensure!(
        (0.0..=1.0).contains(&config.hotset_fraction),
        "hotset_fraction must be in [0, 1]"
    );
    ensure!(
        (0.0..=1.0).contains(&config.hotset_share),
        "hotset_share must be in [0, 1]"
    );
    let hotset_size = if config.cache_affinity_keys == 0 || config.hotset_fraction == 0.0 {
        0
    } else {
        (((config.cache_affinity_keys as f64) * config.hotset_fraction).round() as usize)
            .max(1)
            .min(config.cache_affinity_keys)
    };
    let use_hotset = hotset_size > 0 && rng.random::<f64>() < config.hotset_share;
    let cache_affinity_key_index = sample_optional_index(
        if use_hotset {
            hotset_size
        } else {
            config.cache_affinity_keys
        },
        rng,
    );
    let behavior = if use_hotset { "hot" } else { "cold" };
    Ok(RequestShape {
        routing_key_index: sample_optional_index(config.routing_keys, rng),
        cache_affinity_key_index,
        input_tokens: sample_tokens(&config.input_tokens, rng)?,
        output_tokens: sample_tokens(&config.output_tokens, rng)?,
        backend_behavior_class: behavior,
    })
}

fn sample_prefix_reuse_request(
    config: &PrefixReuseTrafficConfig,
    prior_prefix_tokens: &mut HashMap<usize, u64>,
    rng: &mut StdRng,
) -> anyhow::Result<RequestShape> {
    let cache_affinity_key_index = rng.random_range(0..config.cache_affinity_keys);
    let input_tokens = match prior_prefix_tokens.get(&cache_affinity_key_index).copied() {
        Some(previous) => previous
            .checked_add(sample_tokens(&config.incremental_input_tokens, rng)?)
            .context("prefix_reuse input token count overflowed u64")?,
        None => sample_tokens(&config.initial_input_tokens, rng)?,
    };
    prior_prefix_tokens.insert(cache_affinity_key_index, input_tokens);
    Ok(RequestShape {
        routing_key_index: sample_optional_index(config.routing_keys, rng),
        cache_affinity_key_index: Some(cache_affinity_key_index),
        input_tokens,
        output_tokens: sample_tokens(&config.output_tokens, rng)?,
        backend_behavior_class: "prefix_reuse",
    })
}

fn next_arrival_ms(
    pattern: &TrafficPatternConfig,
    request_index: usize,
    rng: &mut StdRng,
) -> anyhow::Result<u64> {
    let arrival = match pattern {
        TrafficPatternConfig::Uniform(config) => &config.arrival,
        TrafficPatternConfig::ZipfHotset(config) => &config.arrival,
        TrafficPatternConfig::MixedSize(config) => &config.arrival,
        TrafficPatternConfig::PrefixReuse(config) => &config.arrival,
        TrafficPatternConfig::Bursty(config) => {
            let in_burst = (request_index / config.burst_period_requests).is_multiple_of(2);
            let target_rps = if in_burst {
                config.burst_rps
            } else {
                config.quiet_rps
            };
            return sample_arrival_ms(&ArrivalPatternConfig::Poisson { target_rps }, rng);
        }
        TrafficPatternConfig::StairStep(config) => {
            let step = request_index / config.step_requests;
            let target_rps = config.start_rps + (step as f64 * config.step_rps);
            return sample_arrival_ms(&ArrivalPatternConfig::Poisson { target_rps }, rng);
        }
    };
    sample_arrival_ms(arrival, rng)
}

fn sample_arrival_ms(arrival: &ArrivalPatternConfig, rng: &mut StdRng) -> anyhow::Result<u64> {
    match arrival {
        ArrivalPatternConfig::Constant { interval_ms } => Ok(*interval_ms),
        ArrivalPatternConfig::Poisson { target_rps } => {
            ensure!(*target_rps > 0.0, "poisson target_rps must be > 0");
            let u = unit_open_interval(rng);
            let interval_secs = -u.ln() / target_rps;
            Ok((interval_secs * 1000.0).round() as u64)
        }
    }
}

fn sample_tokens(distribution: &TokenDistributionConfig, rng: &mut StdRng) -> anyhow::Result<u64> {
    match distribution {
        TokenDistributionConfig::Constant { value } => Ok(*value),
        TokenDistributionConfig::Uniform { min, max } => {
            ensure!(min <= max, "uniform token distribution requires min <= max");
            Ok(rng.random_range(*min..=*max))
        }
        TokenDistributionConfig::Lognormal {
            mean,
            sigma,
            min,
            p99_cap,
        } => {
            ensure!(*mean > 0.0, "lognormal mean must be > 0");
            ensure!(*sigma > 0.0, "lognormal sigma must be > 0");
            let u1 = unit_open_interval(rng);
            let u2 = unit_open_interval(rng);
            let z = (-2.0 * u1.ln()).sqrt() * (2.0 * std::f64::consts::PI * u2).cos();
            let value = ((*mean).ln() + (*sigma * z)).exp();
            // Token counts are discrete request work units; unclamped lognormal tails can round to 0.
            let mut sampled = value.round().max(1.0) as u64;
            if let Some(minimum) = min {
                sampled = sampled.max(*minimum);
            }
            if let Some(cap) = p99_cap {
                sampled = sampled.min(*cap);
            }
            Ok(sampled)
        }
    }
}

fn sample_optional_index(cardinality: usize, rng: &mut StdRng) -> Option<usize> {
    (cardinality > 0).then(|| rng.random_range(0..cardinality))
}

fn make_optional_key(prefix: &str, index: Option<usize>) -> Option<String> {
    index.map(|index| format!("{prefix}-{index:04}"))
}

fn unit_open_interval(rng: &mut StdRng) -> f64 {
    loop {
        let value = rng.random::<f64>();
        if value > 0.0 && value < 1.0 {
            return value;
        }
    }
}

pub fn write_manifest_json(path: &std::path::Path, manifest: &Manifest) -> anyhow::Result<()> {
    let bytes =
        serde_json::to_vec_pretty(manifest).context("failed to serialize benchmark manifest")?;
    std::fs::write(path, bytes)
        .with_context(|| format!("failed to write manifest {}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{
        AlgorithmConfig, ArrivalPatternConfig, BackendConfig, BackendProfile, HotsetTrafficConfig,
        MixedSizeClassConfig, MixedSizeTrafficConfig, PrefixReuseTrafficConfig, RegistrationConfig,
        ServiceTimeConfig, StargateConfig, TokenDistributionConfig, UniformTrafficConfig,
    };

    fn base_config() -> BenchmarkConfig {
        BenchmarkConfig {
            name: "determinism".to_string(),
            metadata: ScenarioMetadata::default(),
            model: "dummy-model".to_string(),
            seed: Some(7),
            request_count: 10,
            max_concurrency: 2,
            tunnel_protocol: stargate_protocol::TunnelTransportProtocol::RawQuic,
            stargates: StargateConfig { count: 1 },
            backends: BackendConfig {
                count: 3,
                cluster_id_template: None,
                pylons_per_cluster: 1,
                profiles: Vec::new(),
                profile: BackendProfile {
                    name: "default".to_string(),
                    weight: 1.0,
                    max_concurrent_requests: None,
                    kv_cache_capacity_tokens: 0,
                    service_time_ms: ServiceTimeConfig {
                        ttft_mean: 120,
                        ttft_jitter_ms: 20,
                        decode_tokens_per_s: 80,
                        decode_jitter_ms: 0,
                        prefill_tokens_per_s: None,
                    },
                    registration: RegistrationConfig {
                        last_mean_input_tps: 100.0,
                    },
                },
            },
            traffic_pattern: TrafficPatternConfig::Uniform(UniformTrafficConfig {
                routing_keys: 2,
                cache_affinity_keys: 4,
                input_tokens: TokenDistributionConfig::Uniform { min: 100, max: 200 },
                output_tokens: TokenDistributionConfig::Uniform { min: 20, max: 40 },
                arrival: ArrivalPatternConfig::Poisson { target_rps: 10.0 },
            }),
            degradation: crate::config::DegradationConfig::default(),
            algorithms: vec![AlgorithmConfig {
                name: "power-of-two".to_string(),
                config: serde_json::json!({ "default": "power-of-two" }),
                pylon_queue_admission: None,
            }],
        }
    }

    #[test]
    fn same_seed_produces_identical_manifest() {
        let config = base_config();
        let manifest_a = generate_manifest(&config, None).expect("manifest should generate");
        let manifest_b = generate_manifest(&config, None).expect("manifest should generate");
        let json_a = serde_json::to_string(&manifest_a).expect("json should serialize");
        let json_b = serde_json::to_string(&manifest_b).expect("json should serialize");
        assert_eq!(json_a, json_b);
        assert_eq!(
            manifest_a
                .requests
                .iter()
                .take(3)
                .map(|request| (
                    request.scheduled_offset_ms,
                    request.routing_key.as_deref(),
                    request.cache_affinity_key.as_deref(),
                    request.input_tokens,
                    request.output_tokens,
                ))
                .collect::<Vec<_>>(),
            vec![
                (0, Some("rk-0000"), Some("cak-0000"), 131, 22),
                (61, Some("rk-0001"), Some("cak-0001"), 196, 23),
                (197, Some("rk-0000"), Some("cak-0000"), 173, 33),
            ]
        );
    }

    #[test]
    fn different_seed_changes_manifest() {
        let config = base_config();
        let manifest_a = generate_manifest(&config, Some(7)).expect("manifest should generate");
        let manifest_b = generate_manifest(&config, Some(8)).expect("manifest should generate");
        let json_a = serde_json::to_string(&manifest_a).expect("json should serialize");
        let json_b = serde_json::to_string(&manifest_b).expect("json should serialize");
        assert_ne!(json_a, json_b);
    }

    #[test]
    fn prefix_reuse_manifest_grows_each_session_by_only_its_new_suffix() {
        let mut config = base_config();
        config.request_count = 3;
        config.traffic_pattern = TrafficPatternConfig::PrefixReuse(PrefixReuseTrafficConfig {
            routing_keys: 0,
            cache_affinity_keys: 1,
            initial_input_tokens: TokenDistributionConfig::Constant { value: 100_000 },
            incremental_input_tokens: TokenDistributionConfig::Constant { value: 2_000 },
            output_tokens: TokenDistributionConfig::Constant { value: 64 },
            arrival: ArrivalPatternConfig::Constant { interval_ms: 1 },
        });

        let manifest = generate_manifest(&config, None).expect("manifest should generate");

        assert_eq!(
            manifest
                .requests
                .iter()
                .map(|request| request.input_tokens)
                .collect::<Vec<_>>(),
            vec![100_000, 102_000, 104_000]
        );
        assert!(
            manifest
                .requests
                .iter()
                .all(|request| request.cache_affinity_key.as_deref() == Some("cak-0000"))
        );
    }

    #[test]
    fn zero_hotset_fraction_generates_only_cold_requests() {
        let mut config = base_config();
        config.request_count = 8;
        config.traffic_pattern = hotset_pattern(4, 0.0);

        let manifest = generate_manifest(&config, None).expect("manifest should generate");

        assert!(
            manifest
                .requests
                .iter()
                .all(|request| request.backend_behavior_class == "cold")
        );
    }

    #[test]
    fn zero_hotset_keys_generate_only_cold_requests_without_cache_keys() {
        let mut config = base_config();
        config.request_count = 8;
        config.traffic_pattern = hotset_pattern(0, 1.0);

        let manifest = generate_manifest(&config, None).expect("manifest should generate");

        assert!(
            manifest
                .requests
                .iter()
                .all(|request| request.backend_behavior_class == "cold")
        );
        assert!(
            manifest
                .requests
                .iter()
                .all(|request| request.cache_affinity_key.is_none())
        );
    }

    #[test]
    fn tiny_positive_hotset_fraction_keeps_one_hot_key() {
        let mut config = base_config();
        config.request_count = 8;
        config.traffic_pattern = hotset_pattern(8, 0.01);

        let manifest = generate_manifest(&config, None).expect("manifest should generate");

        assert!(
            manifest
                .requests
                .iter()
                .all(|request| request.backend_behavior_class == "hot")
        );
        assert!(
            manifest
                .requests
                .iter()
                .all(|request| request.cache_affinity_key.as_deref() == Some("cak-0000"))
        );
    }

    #[test]
    fn mixed_size_extreme_shares_choose_small_and_large_classes() {
        let mut config = base_config();
        config.request_count = 3;
        config.traffic_pattern = mixed_size_pattern(1.0);

        let small_manifest = generate_manifest(&config, None).expect("manifest should generate");

        assert!(small_manifest.requests.iter().all(|request| {
            request.backend_behavior_class == "small"
                && request.input_tokens == 100
                && request.output_tokens == 10
        }));

        config.traffic_pattern = mixed_size_pattern(0.0);

        let large_manifest = generate_manifest(&config, None).expect("manifest should generate");

        assert!(large_manifest.requests.iter().all(|request| {
            request.backend_behavior_class == "large"
                && request.input_tokens == 1_000
                && request.output_tokens == 100
        }));
    }

    #[test]
    fn mixed_size_config_rejects_invalid_small_share_before_generation() {
        let mut config = base_config();
        config.traffic_pattern = mixed_size_pattern(1.5);

        let error = config
            .validate()
            .expect_err("invalid small_share should fail config validation");

        assert!(error.to_string().contains("small_share must be in [0, 1]"));
    }

    fn mixed_size_pattern(small_share: f64) -> TrafficPatternConfig {
        TrafficPatternConfig::MixedSize(MixedSizeTrafficConfig {
            routing_keys: 0,
            cache_affinity_keys: 1,
            arrival: ArrivalPatternConfig::Constant { interval_ms: 1 },
            small: MixedSizeClassConfig {
                input_tokens: TokenDistributionConfig::Constant { value: 100 },
                output_tokens: TokenDistributionConfig::Constant { value: 10 },
            },
            large: MixedSizeClassConfig {
                input_tokens: TokenDistributionConfig::Constant { value: 1_000 },
                output_tokens: TokenDistributionConfig::Constant { value: 100 },
            },
            small_share,
        })
    }

    fn hotset_pattern(cache_affinity_keys: usize, hotset_fraction: f64) -> TrafficPatternConfig {
        TrafficPatternConfig::ZipfHotset(HotsetTrafficConfig {
            routing_keys: 0,
            cache_affinity_keys,
            hotset_fraction,
            hotset_share: 1.0,
            input_tokens: TokenDistributionConfig::Constant { value: 128 },
            output_tokens: TokenDistributionConfig::Constant { value: 32 },
            arrival: ArrivalPatternConfig::Constant { interval_ms: 1 },
        })
    }
}

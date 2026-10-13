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

use serde::Serialize;

use super::power_of_n::PowerOfNLoadBalancer;
use super::pulsar::PulsarLoadBalancer;
use super::pulsar_wait_and_widen::{PulsarWaitAndWidenConfig, PulsarWaitAndWidenLoadBalancer};
use super::random::RandomLoadBalancer;
use super::round_robin::RoundRobinLoadBalancer;
use super::wait_and_widen::{WaitAndWidenConfig, WaitAndWidenLoadBalancer};
use super::{
    LoadBalancer, LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig, PowerOfNAlgorithmConfig,
    PulsarRendezvousWeight, WaitAndWidenAlgorithmConfig,
};

pub fn create_load_balancer_with_config(
    config: &LoadBalancerAlgorithmConfig,
) -> anyhow::Result<Arc<dyn LoadBalancer>> {
    if config.algorithm() == LoadBalancerAlgorithm::PulsarWaitAndWiden
        && config
            .wait_and_widen_settings()
            .is_some_and(|settings| settings.comparator.is_some())
    {
        anyhow::bail!("comparator is not supported for pulsar-wait-and-widen");
    }

    if config.algorithm() == LoadBalancerAlgorithm::WaitAndWiden
        && config.wait_and_widen_settings().is_some_and(|settings| {
            settings.band_widen_interval_ms.is_some() || settings.fallback_max_queued.is_some()
        })
    {
        anyhow::bail!(
            "band_widen_interval_ms and fallback_max_queued are supported only for pulsar-wait-and-widen"
        );
    }

    if config.considers_kv_free_tokens()
        && !matches!(
            config.algorithm(),
            LoadBalancerAlgorithm::Pulsar | LoadBalancerAlgorithm::PulsarWaitAndWiden
        )
    {
        anyhow::bail!(
            "consider_kv_free_tokens is supported only for pulsar and pulsar-wait-and-widen"
        );
    }

    match config.algorithm() {
        LoadBalancerAlgorithm::PowerOfN => Ok(Arc::new(
            PowerOfNLoadBalancer::from_algorithm_config(config)?,
        )),
        LoadBalancerAlgorithm::WaitAndWiden => Ok(Arc::new(WaitAndWidenLoadBalancer::new(
            WaitAndWidenConfig::from_algorithm_config(config)?,
        ))),
        LoadBalancerAlgorithm::RoundRobin => Ok(Arc::new(RoundRobinLoadBalancer::new())),
        LoadBalancerAlgorithm::Random => Ok(Arc::new(RandomLoadBalancer)),
        LoadBalancerAlgorithm::Pulsar => Ok(Arc::new(PulsarLoadBalancer::new(config.clone()))),
        LoadBalancerAlgorithm::PulsarWaitAndWiden => Ok(Arc::new(
            PulsarWaitAndWidenLoadBalancer::new(config.clone())?,
        )),
    }
}

/// Every value the algorithm routes with, as static load-balancer config JSON. Defaults come
/// from the constructors' default-filling functions.
pub(super) fn effective_config(config: &LoadBalancerAlgorithmConfig) -> anyhow::Result<String> {
    let algorithm = config.algorithm();
    let mut effective = EffectiveConfig {
        algorithm,
        power_of_n: None,
        wait_and_widen: None,
        seed: None,
        consider_kv_free_tokens: None,
        rendezvous_weight: None,
        require_cache_affinity_key: config.requires_cache_affinity_key(),
        require_input_tokens: config.requires_input_tokens(),
        max_input_work_seconds: config.max_input_work_seconds,
    };
    match algorithm {
        LoadBalancerAlgorithm::PowerOfN => {
            let settings = PowerOfNLoadBalancer::from_algorithm_config(config)?;
            effective.power_of_n = Some(settings.to_algorithm_config());
        }
        LoadBalancerAlgorithm::WaitAndWiden => {
            let settings = WaitAndWidenConfig::from_algorithm_config(config)?;
            effective.wait_and_widen = Some(settings.to_algorithm_config());
        }
        LoadBalancerAlgorithm::RoundRobin | LoadBalancerAlgorithm::Random => {}
        LoadBalancerAlgorithm::Pulsar => {
            effective.seed = config.seed().map(ToOwned::to_owned);
            effective.consider_kv_free_tokens = Some(config.considers_kv_free_tokens());
            effective.rendezvous_weight = Some(config.rendezvous_weight);
        }
        LoadBalancerAlgorithm::PulsarWaitAndWiden => {
            let settings = PulsarWaitAndWidenConfig::from_algorithm_config(config)?;
            effective.wait_and_widen = Some(settings.to_algorithm_config());
            effective.consider_kv_free_tokens = Some(config.considers_kv_free_tokens());
            effective.rendezvous_weight = Some(config.rendezvous_weight);
        }
    }
    Ok(serde_json::to_string(&effective)?)
}

#[derive(Serialize)]
struct EffectiveConfig {
    algorithm: LoadBalancerAlgorithm,
    #[serde(flatten)]
    power_of_n: Option<PowerOfNAlgorithmConfig>,
    #[serde(flatten)]
    wait_and_widen: Option<WaitAndWidenAlgorithmConfig>,
    #[serde(skip_serializing_if = "Option::is_none")]
    seed: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    consider_kv_free_tokens: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    rendezvous_weight: Option<PulsarRendezvousWeight>,
    require_cache_affinity_key: bool,
    require_input_tokens: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    max_input_work_seconds: Option<f64>,
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use serde_json::{Value, json};

    use super::*;
    use crate::load_balancer::expression::RoutingExpression;
    use crate::load_balancer::target_state::LoadBalancerDefinition;
    use crate::load_balancer::{LoadBalancerConfig, LoadBalancerModelConfig, LoadBalancerRouter};

    fn effective(router: &LoadBalancerRouter, raw: &str) -> String {
        RoutingExpression::parse(raw)
            .and_then(|expression| expression.compile(router, "model"))
            .unwrap_or_else(|error| panic!("{raw}: {error}"))
            .effective_config()
    }

    #[test]
    fn test_effective_config_lists_routed_values() {
        let permissive =
            LoadBalancerRouter::from_config(&LoadBalancerConfig::permissive_default()).unwrap();
        let mut static_base =
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::WaitAndWiden);
        static_base.max_input_work_seconds = Some(9.0);
        let settings = static_base.wait_and_widen_settings_mut().unwrap();
        settings.n = Some(4);
        settings.ttft_bucket_size_ms = Some(50);
        let mut pulsar_base = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
        pulsar_base.rendezvous_weight = PulsarRendezvousWeight::LastMeanInputTps;
        let configured = LoadBalancerRouter::from_config(&LoadBalancerConfig {
            default: LoadBalancerAlgorithm::Random,
            request_algorithms: HashMap::from([
                (
                    LoadBalancerAlgorithm::WaitAndWiden,
                    LoadBalancerModelConfig::Detailed(Box::new(static_base)),
                ),
                (
                    LoadBalancerAlgorithm::Pulsar,
                    LoadBalancerModelConfig::Detailed(Box::new(pulsar_base)),
                ),
            ]),
            models: HashMap::new(),
        })
        .unwrap();
        // Static config can set values the constructor corrects; expressions reject them.
        let mut corrected_base =
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::WaitAndWiden);
        let settings = corrected_base.wait_and_widen_settings_mut().unwrap();
        settings.n = Some(0);
        settings.cache_affinity_virtual_nodes = Some(0);
        settings.cache_affinity_backend_selection_count = Some(0);
        let corrected = LoadBalancerRouter::from_config(&LoadBalancerConfig {
            default: LoadBalancerAlgorithm::Random,
            request_algorithms: HashMap::from([(
                LoadBalancerAlgorithm::WaitAndWiden,
                LoadBalancerModelConfig::Detailed(Box::new(corrected_base)),
            )]),
            models: HashMap::new(),
        })
        .unwrap();
        // last_cluster_affinity is static config only; expressions cannot set it.
        let mut last_cluster_base =
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PulsarWaitAndWiden);
        last_cluster_base
            .wait_and_widen_settings_mut()
            .unwrap()
            .last_cluster_affinity = Some(true);
        let last_cluster = LoadBalancerRouter::from_config(&LoadBalancerConfig {
            default: LoadBalancerAlgorithm::Random,
            request_algorithms: HashMap::from([(
                LoadBalancerAlgorithm::PulsarWaitAndWiden,
                LoadBalancerModelConfig::Detailed(Box::new(last_cluster_base)),
            )]),
            models: HashMap::new(),
        })
        .unwrap();
        let wait_and_widen_defaults = json!({
            "algorithm": "wait-and-widen", "n": 2, "max_queued": 0, "comparator": "ttft",
            "ttft_bucket_size_ms": 20, "next_bucket_unlock_factor": 0.25,
            "cache_affinity_virtual_nodes": 150, "cache_affinity_input_tokens_scale": 1.0,
            "cache_affinity_wait_ms": 0, "ignore_queue_time": false,
            "ignore_input_processing_time": false, "last_cluster_affinity": false,
            "require_cache_affinity_key": false, "require_input_tokens": false,
        });
        let with = |changes: Value| {
            let mut value = wait_and_widen_defaults.clone();
            for (key, change) in changes.as_object().unwrap() {
                value[key] = change.clone();
            }
            value
        };

        let cases = [
            (
                "power-of-n defaults",
                &permissive,
                "power-of-n;sample_count=3",
                json!({"algorithm": "power-of-n", "sample_count": 3, "comparator": "ttft",
                    "require_cache_affinity_key": false, "require_input_tokens": false}),
            ),
            (
                "round-robin with optional work limit",
                &permissive,
                "round-robin;max_input_work_seconds=2.5",
                json!({"algorithm": "round-robin", "require_cache_affinity_key": false,
                    "require_input_tokens": false, "max_input_work_seconds": 2.5}),
            ),
            (
                "random",
                &permissive,
                "random;require_input_tokens=true",
                json!({"algorithm": "random", "require_cache_affinity_key": false,
                    "require_input_tokens": true}),
            ),
            (
                "pulsar seed",
                &permissive,
                r#"pulsar;seed="a b""#,
                json!({"algorithm": "pulsar", "seed": "a b", "consider_kv_free_tokens": false,
                    "rendezvous_weight": "max-input-tps",
                    "require_cache_affinity_key": false, "require_input_tokens": false}),
            ),
            (
                "pulsar KV check requires input tokens",
                &permissive,
                "pulsar;consider_kv_free_tokens=true",
                json!({"algorithm": "pulsar", "consider_kv_free_tokens": true,
                    "rendezvous_weight": "max-input-tps",
                    "require_cache_affinity_key": false, "require_input_tokens": true}),
            ),
            (
                "pulsar inherits the static rendezvous weight",
                &configured,
                "pulsar;seed=x",
                json!({"algorithm": "pulsar", "seed": "x", "consider_kv_free_tokens": false,
                    "rendezvous_weight": "last-mean-input-tps",
                    "require_cache_affinity_key": false, "require_input_tokens": false}),
            ),
            (
                "wait-and-widen defaults",
                &permissive,
                "wait-and-widen;n=3",
                with(json!({"n": 3})),
            ),
            (
                "wait-and-widen optional fields",
                &permissive,
                r#"wait-and-widen;seed=s;max_queue_time_floor_ms=100;max_queue_time_ceil_ms=500;next_bucket_unlock_factor="0.0625";cache_affinity_backend_selection_count=2"#,
                with(json!({"seed": "s", "max_queue_time_floor_ms": 100,
                    "max_queue_time_ceil_ms": 500, "next_bucket_unlock_factor": 0.0625,
                    "cache_affinity_backend_selection_count": 2})),
            ),
            (
                "wait-and-widen inherits the static base",
                &configured,
                "wait-and-widen;max_queued=1",
                with(json!({"n": 4, "max_queued": 1, "ttft_bucket_size_ms": 50,
                    "max_input_work_seconds": 9.0})),
            ),
            (
                "wait-and-widen logs the corrected static values, not the written ones",
                &corrected,
                "wait-and-widen;seed=s",
                // n and virtual nodes are raised to 1; a backend count of 0 turns affinity off.
                with(json!({"seed": "s", "n": 1, "cache_affinity_virtual_nodes": 1})),
            ),
            (
                "pulsar-wait-and-widen widen interval follows the affinity wait",
                &permissive,
                "pulsar-wait-and-widen;cache_affinity_wait_ms=40",
                json!({"algorithm": "pulsar-wait-and-widen", "n": 2, "max_queued": 0,
                    "ttft_bucket_size_ms": 20, "next_bucket_unlock_factor": 0.25,
                    "cache_affinity_backend_selection_count": 1,
                    "cache_affinity_input_tokens_scale": 1.0, "cache_affinity_wait_ms": 40,
                    "band_widen_interval_ms": 40, "fallback_max_queued": 0,
                    "ignore_queue_time": false, "ignore_input_processing_time": false,
                    "last_cluster_affinity": false,
                    "consider_kv_free_tokens": false, "rendezvous_weight": "max-input-tps",
                    "require_cache_affinity_key": false, "require_input_tokens": false}),
            ),
            (
                "pulsar-wait-and-widen with last-cluster affinity omits an unset widen interval",
                &last_cluster,
                "pulsar-wait-and-widen;cache_affinity_wait_ms=40",
                json!({"algorithm": "pulsar-wait-and-widen", "n": 2, "max_queued": 0,
                    "ttft_bucket_size_ms": 20, "next_bucket_unlock_factor": 0.25,
                    "cache_affinity_backend_selection_count": 1,
                    "cache_affinity_input_tokens_scale": 1.0, "cache_affinity_wait_ms": 40,
                    "fallback_max_queued": 0,
                    "ignore_queue_time": false, "ignore_input_processing_time": false,
                    "last_cluster_affinity": true,
                    "consider_kv_free_tokens": false, "rendezvous_weight": "max-input-tps",
                    "require_cache_affinity_key": false, "require_input_tokens": false}),
            ),
        ];
        for (label, router, raw, expected) in cases {
            let actual = effective(router, raw);
            let value: Value = serde_json::from_str(&actual).unwrap();
            assert_eq!(value, expected, "{label}");
            let parsed: LoadBalancerAlgorithmConfig = serde_json::from_str(&actual)
                .unwrap_or_else(|error| panic!("{label}: not static config: {error}"));
            assert_eq!(
                LoadBalancerDefinition::new(parsed)
                    .unwrap()
                    .effective_config(),
                actual,
                "{label}: parse back"
            );
        }
    }
}

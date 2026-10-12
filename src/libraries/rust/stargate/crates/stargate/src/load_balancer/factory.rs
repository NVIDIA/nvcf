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

use sfv::TokenRef;

use super::power_of_n::PowerOfNLoadBalancer;
use super::pulsar::PulsarLoadBalancer;
use super::pulsar_wait_and_widen::{PulsarWaitAndWidenConfig, PulsarWaitAndWidenLoadBalancer};
use super::random::RandomLoadBalancer;
use super::round_robin::RoundRobinLoadBalancer;
use super::wait_and_widen::{WaitAndWidenConfig, WaitAndWidenLoadBalancer};
use super::{LoadBalancer, LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig};

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

/// Every routing-expression parameter the algorithm uses, with the value it routes with,
/// in the expression grammar. Defaults come from the constructors' default-filling functions.
pub(super) fn effective_routing_expression(
    config: &LoadBalancerAlgorithmConfig,
) -> anyhow::Result<String> {
    let algorithm = config.algorithm();
    let mut expression = EffectiveExpression(algorithm.to_string());
    match algorithm {
        LoadBalancerAlgorithm::PowerOfN => {
            let settings = PowerOfNLoadBalancer::from_algorithm_config(config)?;
            expression.integer("sample_count", settings.sample_count);
            expression.push("comparator", settings.comparator.as_str());
        }
        LoadBalancerAlgorithm::WaitAndWiden => {
            let settings = WaitAndWidenConfig::from_algorithm_config(config)?;
            expression.wait_and_widen(&settings, None);
        }
        LoadBalancerAlgorithm::RoundRobin | LoadBalancerAlgorithm::Random => {}
        LoadBalancerAlgorithm::Pulsar => {
            if let Some(seed) = config.seed() {
                expression.text("seed", seed);
            }
        }
        LoadBalancerAlgorithm::PulsarWaitAndWiden => {
            let settings = PulsarWaitAndWidenConfig::from_algorithm_config(config)?;
            expression.wait_and_widen(&settings.wait_and_widen, Some(&settings));
        }
    }
    if matches!(
        algorithm,
        LoadBalancerAlgorithm::Pulsar | LoadBalancerAlgorithm::PulsarWaitAndWiden
    ) {
        expression.boolean("consider_kv_free_tokens", config.considers_kv_free_tokens());
    }
    expression.boolean(
        "require_cache_affinity_key",
        config.requires_cache_affinity_key(),
    );
    expression.boolean("require_input_tokens", config.requires_input_tokens());
    if let Some(seconds) = config.max_input_work_seconds {
        expression.decimal("max_input_work_seconds", seconds);
    }
    Ok(expression.0)
}

struct EffectiveExpression(String);

impl EffectiveExpression {
    fn wait_and_widen(
        &mut self,
        settings: &WaitAndWidenConfig,
        pulsar: Option<&PulsarWaitAndWidenConfig>,
    ) {
        self.integer("n", settings.sample_count);
        self.integer("max_queued", settings.max_queued);
        if let Some(queue_slo) = &settings.queue_slo {
            self.integer("max_queue_time_floor_ms", queue_slo.start().as_millis());
            self.integer("max_queue_time_ceil_ms", queue_slo.end().as_millis());
        }
        if let Some(comparator) = settings.comparator {
            self.push("comparator", comparator.as_str());
        }
        self.integer("ttft_bucket_size_ms", settings.ttft_bucket_size.as_millis());
        self.decimal(
            "next_bucket_unlock_factor",
            settings.next_bucket_unlock_factor,
        );
        if let Some(seed) = &settings.seed {
            self.text("seed", seed);
        }
        match pulsar {
            None => {
                self.integer(
                    "cache_affinity_virtual_nodes",
                    settings.cache_affinity_virtual_nodes,
                );
                if let Some(count) = settings.cache_affinity_backend_selection_count {
                    self.integer("cache_affinity_backend_selection_count", count);
                }
            }
            Some(pulsar) => self.integer(
                "cache_affinity_backend_selection_count",
                pulsar.affinity_group_size,
            ),
        }
        self.decimal(
            "cache_affinity_input_tokens_scale",
            settings.cache_affinity_input_tokens_scale,
        );
        self.integer(
            "cache_affinity_wait_ms",
            settings.cache_affinity_wait.as_millis(),
        );
        if let Some(pulsar) = pulsar {
            self.integer(
                "band_widen_interval_ms",
                pulsar.band_widen_interval.as_millis(),
            );
            self.integer("fallback_max_queued", pulsar.fallback_max_queued);
        }
        self.boolean("ignore_queue_time", settings.ignore_queue_time);
        self.boolean(
            "ignore_input_processing_time",
            settings.ignore_input_processing_time,
        );
    }

    fn push(&mut self, key: &str, value: &str) {
        self.0.push(';');
        self.0.push_str(key);
        self.0.push('=');
        self.0.push_str(value);
    }

    fn boolean(&mut self, key: &str, value: bool) {
        self.push(key, if value { "true" } else { "false" });
    }

    fn text(&mut self, key: &str, value: &str) {
        if TokenRef::from_str(value).is_ok() {
            self.push(key, value);
        } else {
            self.quoted(key, value);
        }
    }

    // RFC 8941 integers have at most 15 digits; the parser reads quoted plain numbers.
    fn integer(&mut self, key: &str, value: impl ToString) {
        let value = value.to_string();
        if value.len() <= 15 {
            self.push(key, &value);
        } else {
            self.quoted(key, &value);
        }
    }

    // RFC 8941 decimals have at most 12 integer and 3 fractional digits.
    fn decimal(&mut self, key: &str, value: f64) {
        let mut value = value.to_string();
        if !value.contains('.') {
            value.push_str(".0");
        }
        let (whole, fraction) = value
            .trim_start_matches('-')
            .split_once('.')
            .unwrap_or_default();
        if whole.len() <= 12 && fraction.len() <= 3 {
            self.push(key, &value);
        } else {
            self.quoted(key, &value);
        }
    }

    fn quoted(&mut self, key: &str, value: &str) {
        let escaped = value.replace('\\', "\\\\").replace('"', "\\\"");
        self.push(key, &format!("\"{escaped}\""));
    }
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use super::*;
    use crate::load_balancer::expression::RoutingExpression;
    use crate::load_balancer::{LoadBalancerConfig, LoadBalancerModelConfig, LoadBalancerRouter};

    fn effective(router: &LoadBalancerRouter, raw: &str) -> String {
        RoutingExpression::parse(raw)
            .and_then(|expression| expression.compile(router, "model"))
            .unwrap_or_else(|error| panic!("{raw}: {error}"))
            .effective_expression()
    }

    #[test]
    fn test_effective_routing_expression_lists_routed_values() {
        let permissive =
            LoadBalancerRouter::from_config(&LoadBalancerConfig::permissive_default()).unwrap();
        let mut static_base =
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::WaitAndWiden);
        static_base.max_input_work_seconds = Some(9.0);
        let settings = static_base.wait_and_widen_settings_mut().unwrap();
        settings.n = Some(4);
        settings.ttft_bucket_size_ms = Some(50);
        let configured = LoadBalancerRouter::from_config(&LoadBalancerConfig {
            default: LoadBalancerAlgorithm::Random,
            request_algorithms: HashMap::from([(
                LoadBalancerAlgorithm::WaitAndWiden,
                LoadBalancerModelConfig::Detailed(Box::new(static_base)),
            )]),
            models: HashMap::new(),
        })
        .unwrap();

        let cases = [
            (
                "power-of-n defaults",
                &permissive,
                "power-of-n;sample_count=3",
                "power-of-n;sample_count=3;comparator=ttft;require_cache_affinity_key=false;require_input_tokens=false",
            ),
            (
                "round-robin with optional work limit",
                &permissive,
                "round-robin;max_input_work_seconds=2.5",
                "round-robin;require_cache_affinity_key=false;require_input_tokens=false;max_input_work_seconds=2.5",
            ),
            (
                "random",
                &permissive,
                "random;require_input_tokens=true",
                "random;require_cache_affinity_key=false;require_input_tokens=true",
            ),
            (
                "pulsar token seed",
                &permissive,
                "pulsar;seed=x",
                "pulsar;seed=x;consider_kv_free_tokens=false;require_cache_affinity_key=false;require_input_tokens=false",
            ),
            (
                "pulsar string seed; KV check requires input tokens",
                &permissive,
                r#"pulsar;seed="a b";consider_kv_free_tokens=true"#,
                r#"pulsar;seed="a b";consider_kv_free_tokens=true;require_cache_affinity_key=false;require_input_tokens=true"#,
            ),
            (
                "wait-and-widen defaults",
                &permissive,
                "wait-and-widen;n=3",
                "wait-and-widen;n=3;max_queued=0;comparator=ttft;ttft_bucket_size_ms=20;next_bucket_unlock_factor=0.25;cache_affinity_virtual_nodes=150;cache_affinity_input_tokens_scale=1.0;cache_affinity_wait_ms=0;ignore_queue_time=false;ignore_input_processing_time=false;require_cache_affinity_key=false;require_input_tokens=false",
            ),
            (
                "wait-and-widen optional fields and fine decimal",
                &permissive,
                r#"wait-and-widen;seed=s;max_queue_time_floor_ms=100;max_queue_time_ceil_ms=500;next_bucket_unlock_factor="0.0625";cache_affinity_backend_selection_count=2"#,
                r#"wait-and-widen;n=2;max_queued=0;max_queue_time_floor_ms=100;max_queue_time_ceil_ms=500;comparator=ttft;ttft_bucket_size_ms=20;next_bucket_unlock_factor="0.0625";seed=s;cache_affinity_virtual_nodes=150;cache_affinity_backend_selection_count=2;cache_affinity_input_tokens_scale=1.0;cache_affinity_wait_ms=0;ignore_queue_time=false;ignore_input_processing_time=false;require_cache_affinity_key=false;require_input_tokens=false"#,
            ),
            (
                "wait-and-widen inherits the static base",
                &configured,
                "wait-and-widen;max_queued=1",
                "wait-and-widen;n=4;max_queued=1;comparator=ttft;ttft_bucket_size_ms=50;next_bucket_unlock_factor=0.25;cache_affinity_virtual_nodes=150;cache_affinity_input_tokens_scale=1.0;cache_affinity_wait_ms=0;ignore_queue_time=false;ignore_input_processing_time=false;require_cache_affinity_key=false;require_input_tokens=false;max_input_work_seconds=9.0",
            ),
            (
                "pulsar-wait-and-widen widen interval follows the affinity wait",
                &permissive,
                "pulsar-wait-and-widen;cache_affinity_wait_ms=40",
                "pulsar-wait-and-widen;n=2;max_queued=0;ttft_bucket_size_ms=20;next_bucket_unlock_factor=0.25;cache_affinity_backend_selection_count=1;cache_affinity_input_tokens_scale=1.0;cache_affinity_wait_ms=40;band_widen_interval_ms=40;fallback_max_queued=0;ignore_queue_time=false;ignore_input_processing_time=false;consider_kv_free_tokens=false;require_cache_affinity_key=false;require_input_tokens=false",
            ),
        ];
        for (label, router, raw, expected) in cases {
            let actual = effective(router, raw);
            assert_eq!(actual, expected, "{label}");
            assert_eq!(effective(router, &actual), actual, "{label}: round trip");
        }
    }
}

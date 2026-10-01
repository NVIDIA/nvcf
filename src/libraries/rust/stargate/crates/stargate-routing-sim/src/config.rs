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

use anyhow::{Context, bail};
use serde::Deserialize;
use stargate::load_balancer::LoadBalancerAlgorithmConfig;

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SimConfig {
    pub name: String,
    pub seeds: Vec<u64>,
    pub topology: TopologyConfig,
    pub engine: EngineConfig,
    pub pylon: PylonConfig,
    pub stargate: StargateConfig,
    pub client: ClientConfig,
    pub workload: WorkloadConfig,
    pub policies: Vec<PolicyConfig>,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TopologyConfig {
    pub regions: Vec<RegionConfig>,
    /// Symmetric inter-region round-trip times in milliseconds, indexed by region.
    pub rtt_ms: Vec<Vec<f64>>,
    pub intra_region_rtt_ms: f64,
    /// Extra one-way delay for Pylon stats to reach each Stargate through relays.
    #[serde(default)]
    pub stats_relay_delay_ms: f64,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RegionConfig {
    pub name: String,
    pub stargates: usize,
    pub backends: usize,
    /// Multiplies prefill and decode speed for every backend in the region.
    #[serde(default = "one")]
    pub backend_speed: f64,
    /// Relative share of client traffic that enters through this region.
    #[serde(default = "one")]
    pub traffic_weight: f64,
}

/// Mirrors the MockDynamo engine model so simulated results can be checked
/// against cluster runs that use MockDynamo backends.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct EngineConfig {
    pub slots: usize,
    pub prefill_tokens_per_s: f64,
    pub ttft_base_ms: f64,
    pub ttft_jitter_ms: f64,
    pub decode_tokens_per_s_min: f64,
    pub decode_tokens_per_s_max: f64,
    pub kv_cache_capacity_tokens: u64,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PylonConfig {
    pub heartbeat_ms: f64,
    pub max_engine_concurrency: u64,
    pub input_tps: InputTpsModel,
    pub queue_mismatch: QueueMismatchConfig,
}

/// How Pylon derives `last_mean_input_tps` and `max_input_tps`.
#[derive(Clone, Debug, Deserialize)]
#[serde(tag = "model", rename_all = "kebab-case", deny_unknown_fields)]
pub enum InputTpsModel {
    /// Fixed values per unit of backend speed, as if seeded and never observed.
    Constant { mean: f64, max: f64 },
    /// OpenAI fallback stats: the rate over the most recent requests' input
    /// tokens divided by the union of their submit-to-first-output intervals
    /// (`pylon-lib/src/stats/aggregator.rs`). The maximum is a high-water mark.
    FallbackWindow {
        /// Value left by startup calibration, per unit of backend speed.
        initial: f64,
        window: usize,
        duration_floor_ms: f64,
    },
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct QueueMismatchConfig {
    pub enabled: bool,
    pub min_delta_ms: u64,
    pub tolerance_factor: f64,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct StargateConfig {
    pub max_request_retries: u32,
    pub routing_key: String,
    pub model_id: String,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ClientConfig {
    /// Value of the request SLO header; drives queue-SLO widening.
    pub request_slo_ms: Option<u64>,
    /// Value of the routing max-wait header.
    pub max_wait_ms: Option<u64>,
    pub timeout_ms: u64,
    /// Success criterion for goodput: first token within this budget.
    pub ttft_slo_ms: u64,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WorkloadConfig {
    pub sessions: usize,
    pub input_tokens_min: u64,
    pub input_tokens_max: u64,
    pub output_tokens: u64,
    /// Zipf exponent for session popularity. Zero selects sessions uniformly.
    #[serde(default)]
    pub session_zipf_s: f64,
    /// Each rate is an independent steady-state run with Poisson arrivals.
    pub rates_rps: Vec<f64>,
    pub warmup_s: f64,
    pub measure_s: f64,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PolicyConfig {
    pub name: String,
    /// Same shape as a Stargate per-model load-balancer config.
    pub load_balancer: serde_json::Value,
}

impl PolicyConfig {
    pub fn algorithm_config(&self) -> anyhow::Result<LoadBalancerAlgorithmConfig> {
        serde_json::from_value(self.load_balancer.clone())
            .with_context(|| format!("invalid load_balancer config for policy {}", self.name))
    }
}

impl SimConfig {
    pub fn validate(&self) -> anyhow::Result<()> {
        let regions = self.topology.regions.len();
        if regions == 0 {
            bail!("topology needs at least one region");
        }
        if self.topology.rtt_ms.len() != regions
            || self.topology.rtt_ms.iter().any(|row| row.len() != regions)
        {
            bail!("rtt_ms must be a {regions}x{regions} matrix");
        }
        if self
            .topology
            .regions
            .iter()
            .all(|region| region.stargates == 0)
        {
            bail!("topology needs at least one Stargate");
        }
        if self
            .topology
            .regions
            .iter()
            .all(|region| region.backends == 0)
        {
            bail!("topology needs at least one backend");
        }
        if self.workload.sessions == 0 {
            bail!("workload needs at least one session");
        }
        if self.workload.input_tokens_min == 0
            || self.workload.input_tokens_min > self.workload.input_tokens_max
        {
            bail!("workload input token range is invalid");
        }
        if self.seeds.is_empty() || self.workload.rates_rps.is_empty() || self.policies.is_empty() {
            bail!("seeds, rates_rps and policies must be non-empty");
        }
        for policy in &self.policies {
            let config = policy.algorithm_config()?;
            if config.max_input_work_seconds.is_some() {
                bail!(
                    "policy {}: max_input_work_seconds is not modeled yet",
                    policy.name
                );
            }
            stargate::load_balancer::create_load_balancer_with_config(&config)
                .with_context(|| format!("policy {} is not a valid load balancer", policy.name))?;
        }
        Ok(())
    }
}

fn one() -> f64 {
    1.0
}

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
use mock_engine::EngineConfig;
use serde::Deserialize;
use stargate::load_balancer::LoadBalancerAlgorithmConfig;

use crate::time::micros_from_ms;

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SimConfig {
    pub name: String,
    pub seeds: Vec<u64>,
    pub topology: TopologyConfig,
    /// The MockDynamo deployment behind each Pylon, shared with MockDynamo.
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
    /// Symmetric inter-region round-trip times in milliseconds, indexed by
    /// region. The diagonal is unused; see `intra_region_rtt_ms`.
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
    /// Divides every engine step cost for backends in the region.
    #[serde(default = "one")]
    pub backend_speed: f64,
    /// Overrides `engine.num_gpu_workers` for backends in the region.
    #[serde(default)]
    pub gpu_workers: Option<usize>,
    /// Per-backend `num_gpu_workers`, one entry per backend. Takes precedence
    /// over `gpu_workers`.
    #[serde(default)]
    pub backend_gpu_workers: Option<Vec<usize>>,
    /// Relative share of client traffic that enters through this region.
    #[serde(default = "one")]
    pub traffic_weight: f64,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PylonConfig {
    pub heartbeat_ms: f64,
    /// When set, Pylon also publishes after request state changes, at most
    /// once per this many milliseconds. When absent, only heartbeats publish.
    #[serde(default)]
    pub stats_update_coalesce_ms: Option<f64>,
    pub input_tps: InputTpsModel,
    pub queue_mismatch: QueueMismatchConfig,
}

/// How Pylon derives `last_mean_input_tps` and `max_input_tps`.
#[derive(Clone, Debug, Deserialize)]
#[serde(tag = "model", rename_all = "kebab-case", deny_unknown_fields)]
pub enum InputTpsModel {
    /// Fixed values per GPU worker and unit of backend speed, as if seeded and
    /// never observed.
    Constant { mean: f64, max: f64 },
    /// OpenAI fallback stats: the rate over the most recent requests' input
    /// tokens divided by the union of their submit-to-first-output intervals
    /// (`pylon-lib/src/stats/aggregator.rs`). The maximum is a high-water mark.
    FallbackWindow {
        /// Value left by startup calibration, per GPU worker and unit of
        /// backend speed.
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
    /// Offered request rates. Each rate is an independent steady-state run.
    pub rates_rps: Vec<f64>,
    pub warmup_s: f64,
    pub measure_s: f64,
    /// Exactly one of `fixed` or `growing` describes the sessions.
    #[serde(default)]
    pub fixed: Option<FixedSessionsConfig>,
    #[serde(default)]
    pub growing: Option<GrowingSessionsConfig>,
}

/// A fixed population of sessions. Each request picks a session and replays
/// its prompt, with Poisson arrivals.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct FixedSessionsConfig {
    pub sessions: usize,
    pub input_tokens_min: u64,
    pub input_tokens_max: u64,
    pub output_tokens: u64,
    /// Zipf exponent for session popularity. Zero selects sessions uniformly.
    #[serde(default)]
    pub session_zipf_s: f64,
}

/// Conversations that grow each turn. Sessions start with Poisson arrivals;
/// each later turn starts a think time after the previous response finishes,
/// and its prompt is the previous prompt, the previous output, and a new user
/// message. A session ends after its sampled turn count, at the context limit,
/// or when a turn fails every attempt.
#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct GrowingSessionsConfig {
    pub system_prompt_tokens: u64,
    pub user_tokens_min: u64,
    pub user_tokens_max: u64,
    pub output_tokens_min: u64,
    pub output_tokens_max: u64,
    pub turns_min: u32,
    pub turns_max: u32,
    /// Mean of the exponential delay between a response and the next turn.
    pub think_time_mean_s: f64,
    /// Prompt plus output limit; a turn that would exceed it ends the session.
    pub max_context_tokens: u64,
    /// Attempts per turn, including the first. A turn that fails every
    /// attempt ends the session.
    #[serde(default = "default_turn_attempts")]
    pub max_turn_attempts: u32,
    /// A failed attempt is retried after this many seconds times its attempt
    /// number.
    #[serde(default = "default_retry_backoff_s")]
    pub retry_backoff_s: f64,
}

fn default_turn_attempts() -> u32 {
    3
}

fn default_retry_backoff_s() -> f64 {
    1.0
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
        let rtt = &self.topology.rtt_ms;
        if rtt.len() != regions || rtt.iter().any(|row| row.len() != regions) {
            bail!("rtt_ms must be a {regions}x{regions} matrix");
        }
        if (0..regions).any(|a| (0..a).any(|b| rtt[a][b] != rtt[b][a])) {
            bail!("rtt_ms must be symmetric");
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
        match (&self.workload.fixed, &self.workload.growing) {
            (Some(fixed), None) => {
                if fixed.sessions == 0 {
                    bail!("fixed workload needs at least one session");
                }
                if !fixed.session_zipf_s.is_finite() {
                    bail!("fixed workload session_zipf_s must be finite");
                }
                if fixed.input_tokens_min == 0 || fixed.input_tokens_min > fixed.input_tokens_max {
                    bail!("fixed workload input token range is invalid");
                }
            }
            (None, Some(growing)) => {
                if growing.user_tokens_min == 0
                    || growing.user_tokens_min > growing.user_tokens_max
                    || growing.output_tokens_min == 0
                    || growing.output_tokens_min > growing.output_tokens_max
                    || growing.turns_min == 0
                    || growing.turns_min > growing.turns_max
                {
                    bail!("growing workload ranges are invalid");
                }
                if growing
                    .system_prompt_tokens
                    .saturating_add(growing.user_tokens_max)
                    .saturating_add(growing.output_tokens_max)
                    > growing.max_context_tokens
                {
                    bail!("growing workload first turn can exceed max_context_tokens");
                }
                if !(growing.think_time_mean_s.is_finite() && growing.think_time_mean_s >= 0.0) {
                    bail!("growing workload think_time_mean_s must be non-negative");
                }
                if growing.max_turn_attempts == 0
                    || !(growing.retry_backoff_s.is_finite() && growing.retry_backoff_s >= 0.0)
                {
                    bail!("growing workload retry settings are invalid");
                }
            }
            _ => bail!("workload needs exactly one of fixed or growing"),
        }
        if self.seeds.is_empty() || self.workload.rates_rps.is_empty() || self.policies.is_empty() {
            bail!("seeds, rates_rps and policies must be non-empty");
        }
        self.engine.validate().map_err(anyhow::Error::msg)?;
        // A heartbeat that rounds to zero microseconds reschedules itself forever.
        if !(self.pylon.heartbeat_ms.is_finite() && micros_from_ms(self.pylon.heartbeat_ms) > 0) {
            bail!("pylon.heartbeat_ms must be at least one microsecond");
        }
        // A non-positive rate turns every queue estimate into the unknown
        // sentinel, which production Pylon would not reject on.
        let input_tps_valid = match self.pylon.input_tps {
            InputTpsModel::Constant { mean, max } => {
                mean.is_finite() && mean > 0.0 && max.is_finite() && max >= mean
            }
            InputTpsModel::FallbackWindow {
                initial, window, ..
            } => initial.is_finite() && initial > 0.0 && window > 0,
        };
        if !input_tps_valid {
            bail!("pylon.input_tps needs positive rates, max >= mean, and a positive window");
        }
        if self.client.timeout_ms == 0 {
            bail!("client.timeout_ms must be positive");
        }
        if !(self.workload.measure_s.is_finite() && self.workload.measure_s > 0.0) {
            bail!("workload.measure_s must be positive");
        }
        // Virtual-time conversion would clamp negative or NaN delays to zero.
        let mut delays = vec![
            ("workload.warmup_s", self.workload.warmup_s),
            (
                "topology.intra_region_rtt_ms",
                self.topology.intra_region_rtt_ms,
            ),
            (
                "topology.stats_relay_delay_ms",
                self.topology.stats_relay_delay_ms,
            ),
        ];
        delays.extend(
            self.topology
                .rtt_ms
                .iter()
                .flatten()
                .map(|rtt| ("topology.rtt_ms", *rtt)),
        );
        if let Some(coalesce_ms) = self.pylon.stats_update_coalesce_ms {
            delays.push(("pylon.stats_update_coalesce_ms", coalesce_ms));
        }
        if let InputTpsModel::FallbackWindow {
            duration_floor_ms, ..
        } = self.pylon.input_tps
        {
            delays.push(("pylon.input_tps.duration_floor_ms", duration_floor_ms));
        }
        for (name, value) in delays {
            if !(value.is_finite() && value >= 0.0) {
                bail!("{name} must be finite and non-negative");
            }
        }
        if self
            .workload
            .rates_rps
            .iter()
            .any(|rate| !(rate.is_finite() && *rate > 0.0))
        {
            bail!("rates_rps must be positive");
        }
        if self
            .topology
            .regions
            .iter()
            .any(|region| !(region.traffic_weight.is_finite() && region.traffic_weight >= 0.0))
        {
            bail!("traffic_weight must be non-negative");
        }
        if self
            .topology
            .regions
            .iter()
            .filter(|region| region.stargates > 0)
            .all(|region| region.traffic_weight == 0.0)
        {
            bail!("regions with Stargates need a positive total traffic_weight");
        }
        for region in &self.topology.regions {
            if region.gpu_workers == Some(0) {
                bail!("region {}: gpu_workers must be positive", region.name);
            }
            if !(region.backend_speed.is_finite() && region.backend_speed > 0.0) {
                bail!("region {}: backend_speed must be positive", region.name);
            }
            if let Some(workers) = &region.backend_gpu_workers {
                if workers.len() != region.backends {
                    bail!(
                        "region {}: backend_gpu_workers needs {} entries",
                        region.name,
                        region.backends
                    );
                }
                if workers.contains(&0) {
                    bail!(
                        "region {}: backend_gpu_workers must be positive",
                        region.name
                    );
                }
            }
        }
        for policy in &self.policies {
            let config = policy.algorithm_config()?;
            if config.max_input_work_seconds.is_some() {
                bail!(
                    "policy {}: max_input_work_seconds is not modeled yet",
                    policy.name
                );
            }
            // Simulated Pylons publish no KV-cache stats, so every candidate
            // would be skipped.
            if config.considers_kv_free_tokens() {
                bail!(
                    "policy {}: consider_kv_free_tokens is not modeled yet",
                    policy.name
                );
            }
            stargate::load_balancer::create_load_balancer_with_config(&config)
                .with_context(|| format!("policy {} is not a valid load balancer", policy.name))?;
        }
        Ok(())
    }

    /// Every Stargate in topology order, as its region index and its share of
    /// client traffic. The simulator and the fleet driver both use this order.
    pub fn stargates(&self) -> Vec<(usize, f64)> {
        self.topology
            .regions
            .iter()
            .enumerate()
            .flat_map(|(index, region)| {
                std::iter::repeat_n(
                    (index, region.traffic_weight / region.stargates as f64),
                    region.stargates,
                )
            })
            .collect()
    }

    /// Engine configuration for backend `backend` in `region`.
    pub fn backend_engine(&self, region: &RegionConfig, backend: usize) -> EngineConfig {
        let speed = region.backend_speed;
        EngineConfig {
            num_gpu_workers: region
                .backend_gpu_workers
                .as_ref()
                .map(|workers| workers[backend])
                .or(region.gpu_workers)
                .unwrap_or(self.engine.num_gpu_workers),
            step_fixed_ms: self.engine.step_fixed_ms / speed,
            step_decode_ms_per_seq: self.engine.step_decode_ms_per_seq / speed,
            step_prefill_ms_per_token: self.engine.step_prefill_ms_per_token / speed,
            ..self.engine.clone()
        }
    }
}

fn one() -> f64 {
    1.0
}

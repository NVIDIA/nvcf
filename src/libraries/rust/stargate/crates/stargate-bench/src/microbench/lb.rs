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

use std::collections::{HashMap, HashSet};
use std::hint::black_box;
use std::io::Write;
use std::sync::{Arc, Barrier};
use std::thread;
use std::time::{Duration, Instant};

use anyhow::Context;
use clap::ValueEnum;
use stargate::load_balancer::{
    LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig, LoadBalancerConfig,
    LoadBalancerModelConfig, LoadBalancerRequest, LoadBalancerRouter,
};
use stargate::routing::{RoutedClusterSnapshot, RoutingTargetKey};
use stargate_proto::pb::{InferenceServerStatus, ModelStats};

const MULTIREGION_MODEL_ID: &str = "lb-bench-multiregion";
const MULTIREGION_ONE_EXCLUDED_MODEL_ID: &str = "lb-bench-multiregion-one-excluded";
const MULTIREGION_IGNORE_QUEUE_MODEL_ID: &str = "lb-bench-multiregion-ignore-queue";
const MULTIREGION_IGNORE_QUEUE_ONE_EXCLUDED_MODEL_ID: &str =
    "lb-bench-multiregion-ignore-queue-one-excluded";
const MULTIREGION_IGNORE_QUEUE_MULTI_EXCLUDED_MODEL_ID: &str =
    "lb-bench-multiregion-ignore-queue-multi-excluded";
const MULTIREGION_RTT_ONLY_MODEL_ID: &str = "lb-bench-multiregion-rtt-only";
const MULTIREGION_RTT_ONLY_ONE_EXCLUDED_MODEL_ID: &str =
    "lb-bench-multiregion-rtt-only-one-excluded";
const MULTIREGION_RTT_ONLY_MULTI_EXCLUDED_MODEL_ID: &str =
    "lb-bench-multiregion-rtt-only-multi-excluded";
const MULTIREGION_AFFINITY_MODEL_ID: &str = "lb-bench-multiregion-affinity";
const MULTIREGION_AFFINITY_ONE_EXCLUDED_MODEL_ID: &str =
    "lb-bench-multiregion-affinity-one-excluded";
const MULTIREGION_AFFINITY_MULTI_EXCLUDED_MODEL_ID: &str =
    "lb-bench-multiregion-affinity-multi-excluded";
const POWER_OF_TWO_MODEL_ID: &str = "lb-bench-power-of-two";
const POWER_OF_TWO_ONE_EXCLUDED_MODEL_ID: &str = "lb-bench-power-of-two-one-excluded";
const PULSAR_MODEL_ID: &str = "lb-bench-pulsar";
const PULSAR_ONE_EXCLUDED_MODEL_ID: &str = "lb-bench-pulsar-one-excluded";
const RANDOM_MODEL_ID: &str = "lb-bench-random";
const RANDOM_ONE_EXCLUDED_MODEL_ID: &str = "lb-bench-random-one-excluded";
const ROUND_ROBIN_ONE_EXCLUDED_MODEL_ID: &str = "lb-bench-round-robin-one-excluded";
const ROUND_ROBIN_MULTI_EXCLUDED_MODEL_ID: &str = "lb-bench-round-robin-multi-excluded";

#[derive(Clone, Copy, Debug, Eq, PartialEq, ValueEnum)]
#[value(rename_all = "kebab-case")]
pub enum LbMicrobenchScenario {
    PowerOfTwo,
    PowerOfTwoOneExcluded,
    GroqMultiregion,
    GroqMultiregionOneExcluded,
    GroqMultiregionIgnoreQueue,
    GroqMultiregionIgnoreQueueOneExcluded,
    GroqMultiregionIgnoreQueueMultiExcluded,
    GroqMultiregionRttOnly,
    GroqMultiregionRttOnlyOneExcluded,
    GroqMultiregionRttOnlyMultiExcluded,
    GroqMultiregionAffinity,
    GroqMultiregionAffinityOneExcluded,
    GroqMultiregionAffinityMultiExcluded,
    Pulsar,
    PulsarOneExcluded,
    Random,
    RandomOneExcluded,
    RoundRobinOneExcluded,
    RoundRobinMultiExcluded,
}

impl LbMicrobenchScenario {
    fn label(self) -> &'static str {
        match self {
            Self::PowerOfTwo => "power-of-two",
            Self::PowerOfTwoOneExcluded => "power-of-two-one-excluded",
            Self::GroqMultiregion => "groq-multiregion",
            Self::GroqMultiregionOneExcluded => "groq-multiregion-one-excluded",
            Self::GroqMultiregionIgnoreQueue => "groq-multiregion-ignore-queue",
            Self::GroqMultiregionIgnoreQueueOneExcluded => {
                "groq-multiregion-ignore-queue-one-excluded"
            }
            Self::GroqMultiregionIgnoreQueueMultiExcluded => {
                "groq-multiregion-ignore-queue-multi-excluded"
            }
            Self::GroqMultiregionRttOnly => "groq-multiregion-rtt-only",
            Self::GroqMultiregionRttOnlyOneExcluded => "groq-multiregion-rtt-only-one-excluded",
            Self::GroqMultiregionRttOnlyMultiExcluded => "groq-multiregion-rtt-only-multi-excluded",
            Self::GroqMultiregionAffinity => "groq-multiregion-affinity",
            Self::GroqMultiregionAffinityOneExcluded => "groq-multiregion-affinity-one-excluded",
            Self::GroqMultiregionAffinityMultiExcluded => {
                "groq-multiregion-affinity-multi-excluded"
            }
            Self::Pulsar => "pulsar",
            Self::PulsarOneExcluded => "pulsar-one-excluded",
            Self::Random => "random",
            Self::RandomOneExcluded => "random-one-excluded",
            Self::RoundRobinOneExcluded => "round-robin-one-excluded",
            Self::RoundRobinMultiExcluded => "round-robin-multi-excluded",
        }
    }

    fn model_id(self) -> &'static str {
        match self {
            Self::PowerOfTwo => POWER_OF_TWO_MODEL_ID,
            Self::PowerOfTwoOneExcluded => POWER_OF_TWO_ONE_EXCLUDED_MODEL_ID,
            Self::GroqMultiregion => MULTIREGION_MODEL_ID,
            Self::GroqMultiregionOneExcluded => MULTIREGION_ONE_EXCLUDED_MODEL_ID,
            Self::GroqMultiregionIgnoreQueue => MULTIREGION_IGNORE_QUEUE_MODEL_ID,
            Self::GroqMultiregionIgnoreQueueOneExcluded => {
                MULTIREGION_IGNORE_QUEUE_ONE_EXCLUDED_MODEL_ID
            }
            Self::GroqMultiregionIgnoreQueueMultiExcluded => {
                MULTIREGION_IGNORE_QUEUE_MULTI_EXCLUDED_MODEL_ID
            }
            Self::GroqMultiregionRttOnly => MULTIREGION_RTT_ONLY_MODEL_ID,
            Self::GroqMultiregionRttOnlyOneExcluded => MULTIREGION_RTT_ONLY_ONE_EXCLUDED_MODEL_ID,
            Self::GroqMultiregionRttOnlyMultiExcluded => {
                MULTIREGION_RTT_ONLY_MULTI_EXCLUDED_MODEL_ID
            }
            Self::GroqMultiregionAffinity => MULTIREGION_AFFINITY_MODEL_ID,
            Self::GroqMultiregionAffinityOneExcluded => MULTIREGION_AFFINITY_ONE_EXCLUDED_MODEL_ID,
            Self::GroqMultiregionAffinityMultiExcluded => {
                MULTIREGION_AFFINITY_MULTI_EXCLUDED_MODEL_ID
            }
            Self::Pulsar => PULSAR_MODEL_ID,
            Self::PulsarOneExcluded => PULSAR_ONE_EXCLUDED_MODEL_ID,
            Self::Random => RANDOM_MODEL_ID,
            Self::RandomOneExcluded => RANDOM_ONE_EXCLUDED_MODEL_ID,
            Self::RoundRobinOneExcluded => ROUND_ROBIN_ONE_EXCLUDED_MODEL_ID,
            Self::RoundRobinMultiExcluded => ROUND_ROBIN_MULTI_EXCLUDED_MODEL_ID,
        }
    }
}

#[derive(Clone, Debug)]
pub struct LbMicrobenchConfig {
    pub iterations: usize,
    pub warmup_iterations: usize,
    pub concurrency: usize,
    pub candidates: usize,
    pub cache_key_count: usize,
    pub scenarios: Vec<LbMicrobenchScenario>,
}

#[derive(Clone, Debug)]
pub struct LbMicrobenchRow {
    pub scenario: LbMicrobenchScenario,
    pub candidates: usize,
    pub iterations: usize,
    pub warmup_iterations: usize,
    pub concurrency: usize,
    pub total_ns: u128,
    pub ns_per_choose: f64,
    pub choices: usize,
    pub avg_rank_depth: f64,
    pub selected_backend_count: usize,
    pub top_backend: String,
    pub top_backend_choices: usize,
    pub backend_counts: Vec<(String, usize)>,
    pub checksum: u64,
}

pub fn default_lb_microbench_scenarios() -> Vec<LbMicrobenchScenario> {
    vec![
        LbMicrobenchScenario::PowerOfTwo,
        LbMicrobenchScenario::PowerOfTwoOneExcluded,
        LbMicrobenchScenario::GroqMultiregion,
        LbMicrobenchScenario::GroqMultiregionOneExcluded,
        LbMicrobenchScenario::GroqMultiregionIgnoreQueue,
        LbMicrobenchScenario::GroqMultiregionIgnoreQueueOneExcluded,
        LbMicrobenchScenario::GroqMultiregionIgnoreQueueMultiExcluded,
        LbMicrobenchScenario::GroqMultiregionRttOnly,
        LbMicrobenchScenario::GroqMultiregionRttOnlyOneExcluded,
        LbMicrobenchScenario::GroqMultiregionRttOnlyMultiExcluded,
        LbMicrobenchScenario::GroqMultiregionAffinity,
        LbMicrobenchScenario::GroqMultiregionAffinityOneExcluded,
        LbMicrobenchScenario::GroqMultiregionAffinityMultiExcluded,
        LbMicrobenchScenario::Pulsar,
        LbMicrobenchScenario::PulsarOneExcluded,
        LbMicrobenchScenario::Random,
        LbMicrobenchScenario::RandomOneExcluded,
        LbMicrobenchScenario::RoundRobinOneExcluded,
        LbMicrobenchScenario::RoundRobinMultiExcluded,
    ]
}

pub fn run_lb_microbench(config: &LbMicrobenchConfig) -> anyhow::Result<Vec<LbMicrobenchRow>> {
    validate_config(config)?;
    let scenarios = if config.scenarios.is_empty() {
        default_lb_microbench_scenarios()
    } else {
        config.scenarios.clone()
    };
    let candidates = build_candidates(config.candidates);
    let cache_keys = build_cache_keys(config.cache_key_count);
    let mut rows = Vec::with_capacity(scenarios.len());
    for scenario in scenarios {
        rows.push(run_scenario(config, scenario, &candidates, &cache_keys)?);
    }
    Ok(rows)
}

pub fn write_lb_microbench_csv<W: Write>(
    mut writer: W,
    rows: &[LbMicrobenchRow],
) -> std::io::Result<()> {
    writeln!(
        writer,
        "scenario,candidates,iterations,warmup_iterations,concurrency,total_ns,ns_per_choose,choices,avg_rank_depth,selected_backend_count,top_backend,top_backend_choices,backend_counts,checksum"
    )?;
    for row in rows {
        writeln!(
            writer,
            "{},{},{},{},{},{},{:.1},{},{:.3},{},{},{},{},{}",
            row.scenario.label(),
            row.candidates,
            row.iterations,
            row.warmup_iterations,
            row.concurrency,
            row.total_ns,
            row.ns_per_choose,
            row.choices,
            row.avg_rank_depth,
            row.selected_backend_count,
            row.top_backend,
            row.top_backend_choices,
            format_backend_counts(&row.backend_counts),
            row.checksum
        )?;
    }
    Ok(())
}

fn validate_config(config: &LbMicrobenchConfig) -> anyhow::Result<()> {
    if config.iterations == 0 {
        anyhow::bail!("--iterations must be greater than 0");
    }
    if config.concurrency == 0 {
        anyhow::bail!("--concurrency must be greater than 0");
    }
    if config.candidates == 0 {
        anyhow::bail!("--candidates must be greater than 0");
    }
    if config.cache_key_count == 0 {
        anyhow::bail!("--cache-key-count must be greater than 0");
    }
    Ok(())
}

fn run_scenario(
    config: &LbMicrobenchConfig,
    scenario: LbMicrobenchScenario,
    candidates: &[RoutedClusterSnapshot],
    cache_keys: &[String],
) -> anyhow::Result<LbMicrobenchRow> {
    let router = Arc::new(router_for_scenario(scenario)?);
    let excluded_cluster_ids = excluded_cluster_ids_for_scenario(scenario);
    let target = RoutingTargetKey {
        routing_key: Some("tenant-a".to_string()),
        model_id: scenario.model_id().to_string(),
    };
    let received_at = Instant::now();
    for iteration in 0..config.warmup_iterations {
        let request = request_for_iteration(
            &target,
            cache_keys,
            iteration,
            received_at,
            excluded_cluster_ids.as_ref(),
        );
        let _ = black_box(router.choose_candidate(&request, candidates));
    }

    let worker_count = config.concurrency.min(config.iterations);
    let (stats, total_ns) = run_concurrent_measured_iterations(
        router,
        LbMicrobenchRun {
            target: &target,
            candidates,
            cache_keys,
            config,
            worker_count,
            received_at,
            excluded_cluster_ids: excluded_cluster_ids.as_ref(),
        },
    );
    let avg_rank_depth = if stats.choices == 0 {
        0.0
    } else {
        stats.rank_depth_sum as f64 / stats.choices as f64
    };
    let mut backend_counts = stats.backend_counts.into_iter().collect::<Vec<_>>();
    backend_counts.sort_unstable_by(|(id_a, _), (id_b, _)| id_a.cmp(id_b));
    let (top_backend, top_backend_choices) = top_backend(&backend_counts);
    Ok(LbMicrobenchRow {
        scenario,
        candidates: config.candidates,
        iterations: config.iterations,
        warmup_iterations: config.warmup_iterations,
        concurrency: worker_count,
        total_ns,
        ns_per_choose: total_ns as f64 / config.iterations as f64,
        choices: stats.choices,
        avg_rank_depth,
        selected_backend_count: backend_counts.len(),
        top_backend,
        top_backend_choices,
        backend_counts,
        checksum: stats.checksum,
    })
}

#[derive(Default)]
struct LbMicrobenchStats {
    choices: usize,
    rank_depth_sum: usize,
    backend_counts: HashMap<String, usize>,
    checksum: u64,
}

impl LbMicrobenchStats {
    fn record(
        &mut self,
        choice: stargate::load_balancer::LoadBalancerCandidateChoice,
        candidates: &[RoutedClusterSnapshot],
    ) {
        self.choices += 1;
        self.rank_depth_sum += choice.rank_depth;
        let cluster_id = candidates[choice.candidate_index].cluster_id.clone();
        self.checksum = update_checksum(self.checksum, &cluster_id);
        *self.backend_counts.entry(cluster_id).or_insert(0) += 1;
    }

    fn merge(&mut self, other: Self) {
        self.choices += other.choices;
        self.rank_depth_sum += other.rank_depth_sum;
        self.checksum = self.checksum.wrapping_add(other.checksum);
        for (cluster_id, count) in other.backend_counts {
            *self.backend_counts.entry(cluster_id).or_insert(0) += count;
        }
    }
}

struct LbMicrobenchRun<'a> {
    target: &'a RoutingTargetKey,
    candidates: &'a [RoutedClusterSnapshot],
    cache_keys: &'a [String],
    config: &'a LbMicrobenchConfig,
    worker_count: usize,
    received_at: Instant,
    excluded_cluster_ids: Option<&'a HashSet<String>>,
}

fn run_concurrent_measured_iterations(
    router: Arc<LoadBalancerRouter>,
    run: LbMicrobenchRun<'_>,
) -> (LbMicrobenchStats, u128) {
    thread::scope(|scope| {
        let ready_barrier = Arc::new(Barrier::new(run.worker_count + 1));
        let release_barrier = Arc::new(Barrier::new(run.worker_count + 1));
        let mut handles = Vec::with_capacity(run.worker_count);
        for worker_index in 0..run.worker_count {
            let router = Arc::clone(&router);
            let ready_barrier = Arc::clone(&ready_barrier);
            let release_barrier = Arc::clone(&release_barrier);
            let (start_iteration, iteration_count) =
                worker_iteration_range(run.config.iterations, run.worker_count, worker_index);
            let target_ref = run.target;
            let candidates_ref = run.candidates;
            let cache_keys_ref = run.cache_keys;
            let warmup_iterations = run.config.warmup_iterations;
            let received_at = run.received_at;
            let excluded_cluster_ids = run.excluded_cluster_ids;
            handles.push(scope.spawn(move || {
                ready_barrier.wait();
                release_barrier.wait();
                let stats = run_worker_measured_iterations(LbMicrobenchWorker {
                    router: router.as_ref(),
                    target: target_ref,
                    candidates: candidates_ref,
                    cache_keys: cache_keys_ref,
                    warmup_iterations,
                    start_iteration,
                    iteration_count,
                    received_at,
                    excluded_cluster_ids,
                });
                LbMicrobenchWorkerResult {
                    stats,
                    finished_at: Instant::now(),
                }
            }));
        }

        let start = release_workers_after_ready(&ready_barrier, &release_barrier);
        merge_worker_results(
            start,
            handles
                .into_iter()
                .map(|handle| handle.join().expect("lb microbench worker panicked")),
        )
    })
}

struct LbMicrobenchWorkerResult {
    stats: LbMicrobenchStats,
    finished_at: Instant,
}

fn merge_worker_results(
    start: Instant,
    results: impl IntoIterator<Item = LbMicrobenchWorkerResult>,
) -> (LbMicrobenchStats, u128) {
    let mut stats = LbMicrobenchStats::default();
    let mut total_ns = 0;
    for result in results {
        total_ns = total_ns.max(result.finished_at.duration_since(start).as_nanos());
        stats.merge(result.stats);
    }
    (stats, total_ns)
}

fn release_workers_after_ready(ready_barrier: &Barrier, release_barrier: &Barrier) -> Instant {
    release_workers_after_ready_with_hook(ready_barrier, release_barrier, || {})
}

fn release_workers_after_ready_with_hook<F>(
    ready_barrier: &Barrier,
    release_barrier: &Barrier,
    before_ready_wait: F,
) -> Instant
where
    F: FnOnce(),
{
    before_ready_wait();
    ready_barrier.wait();
    let start = Instant::now();
    release_barrier.wait();
    start
}

struct LbMicrobenchWorker<'a> {
    router: &'a LoadBalancerRouter,
    target: &'a RoutingTargetKey,
    candidates: &'a [RoutedClusterSnapshot],
    cache_keys: &'a [String],
    warmup_iterations: usize,
    start_iteration: usize,
    iteration_count: usize,
    received_at: Instant,
    excluded_cluster_ids: Option<&'a HashSet<String>>,
}

fn run_worker_measured_iterations(worker: LbMicrobenchWorker<'_>) -> LbMicrobenchStats {
    let mut stats = LbMicrobenchStats::default();
    for local_iteration in 0..worker.iteration_count {
        let request = request_for_measured_iteration(
            worker.target,
            worker.cache_keys,
            worker.warmup_iterations,
            worker.start_iteration + local_iteration,
            worker.received_at,
            worker.excluded_cluster_ids,
        );
        if let Some(choice) = black_box(worker.router.choose_candidate(&request, worker.candidates))
        {
            stats.record(choice, worker.candidates);
        }
    }
    stats
}

fn worker_iteration_range(
    total_iterations: usize,
    worker_count: usize,
    worker_index: usize,
) -> (usize, usize) {
    let base = total_iterations / worker_count;
    let remainder = total_iterations % worker_count;
    let iteration_count = base + usize::from(worker_index < remainder);
    let start_iteration = worker_index * base + worker_index.min(remainder);
    (start_iteration, iteration_count)
}

fn request_for_measured_iteration<'a>(
    target: &'a RoutingTargetKey,
    cache_keys: &'a [String],
    warmup_iterations: usize,
    measured_iteration: usize,
    received_at: Instant,
    excluded_cluster_ids: Option<&'a HashSet<String>>,
) -> LoadBalancerRequest<'a> {
    request_for_iteration(
        target,
        cache_keys,
        warmup_iterations + measured_iteration,
        received_at,
        excluded_cluster_ids,
    )
}

fn request_for_iteration<'a>(
    target: &'a RoutingTargetKey,
    cache_keys: &'a [String],
    iteration: usize,
    received_at: Instant,
    excluded_cluster_ids: Option<&'a HashSet<String>>,
) -> LoadBalancerRequest<'a> {
    LoadBalancerRequest {
        routing_target: target,
        cache_affinity_key: Some(cache_keys[iteration % cache_keys.len()].as_str()),
        input_tokens: Some(512 + (iteration % 8) as u64 * 64),
        priority: (iteration % 4) as u32,
        received_at,
        request_slo: Some(Duration::from_millis(250)),
        excluded_cluster_ids,
    }
}

fn update_checksum(mut checksum: u64, cluster_id: &str) -> u64 {
    for byte in cluster_id.as_bytes() {
        checksum = checksum.wrapping_mul(16_777_619) ^ u64::from(*byte);
    }
    checksum
}

fn top_backend(backend_counts: &[(String, usize)]) -> (String, usize) {
    backend_counts
        .iter()
        .max_by(|(id_a, count_a), (id_b, count_b)| {
            count_a.cmp(count_b).then_with(|| id_b.cmp(id_a))
        })
        .map(|(id, count)| (id.clone(), *count))
        .unwrap_or_else(|| (String::new(), 0))
}

fn format_backend_counts(backend_counts: &[(String, usize)]) -> String {
    backend_counts
        .iter()
        .map(|(cluster_id, count)| format!("{cluster_id}:{count}"))
        .collect::<Vec<_>>()
        .join(";")
}

fn router_for_scenario(scenario: LbMicrobenchScenario) -> anyhow::Result<LoadBalancerRouter> {
    let mut models = HashMap::new();
    models.insert(
        scenario.model_id().to_string(),
        LoadBalancerModelConfig::Detailed(Box::new(config_for_scenario(scenario))),
    );
    LoadBalancerRouter::from_config(&LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: HashMap::new(),
        models,
    })
    .with_context(|| format!("failed to build {} load balancer", scenario.label()))
}

fn config_for_scenario(scenario: LbMicrobenchScenario) -> LoadBalancerAlgorithmConfig {
    let mut config = match scenario {
        LbMicrobenchScenario::PowerOfTwo | LbMicrobenchScenario::PowerOfTwoOneExcluded => {
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PowerOfTwo)
        }
        LbMicrobenchScenario::Pulsar | LbMicrobenchScenario::PulsarOneExcluded => {
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar)
        }
        LbMicrobenchScenario::Random | LbMicrobenchScenario::RandomOneExcluded => {
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Random)
        }
        LbMicrobenchScenario::RoundRobinOneExcluded
        | LbMicrobenchScenario::RoundRobinMultiExcluded => {
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::RoundRobin)
        }
        LbMicrobenchScenario::GroqMultiregion
        | LbMicrobenchScenario::GroqMultiregionOneExcluded
        | LbMicrobenchScenario::GroqMultiregionIgnoreQueue
        | LbMicrobenchScenario::GroqMultiregionIgnoreQueueOneExcluded
        | LbMicrobenchScenario::GroqMultiregionIgnoreQueueMultiExcluded
        | LbMicrobenchScenario::GroqMultiregionRttOnly
        | LbMicrobenchScenario::GroqMultiregionRttOnlyOneExcluded
        | LbMicrobenchScenario::GroqMultiregionRttOnlyMultiExcluded
        | LbMicrobenchScenario::GroqMultiregionAffinity
        | LbMicrobenchScenario::GroqMultiregionAffinityOneExcluded
        | LbMicrobenchScenario::GroqMultiregionAffinityMultiExcluded => {
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::GroqMultiregion)
        }
    };
    let is_pulsar = matches!(
        scenario,
        LbMicrobenchScenario::Pulsar | LbMicrobenchScenario::PulsarOneExcluded
    );
    config.request_policy_mut().require_cache_affinity_key = is_pulsar;
    config.request_policy_mut().require_input_tokens = is_pulsar;
    if is_pulsar {
        config
            .set_seed(Some("lb-microbench-seed".to_string()))
            .expect("pulsar supports deterministic seeding");
    }
    if let Some(multiregion) = config.multiregion_settings_mut() {
        multiregion.seed = Some("lb-microbench-seed".to_string());
        multiregion.ttft_bucket_size_ms = Some(1_000_000);
        multiregion.n = Some(2);
        multiregion.max_queued = Some(64);
        if matches!(
            scenario,
            LbMicrobenchScenario::GroqMultiregionAffinity
                | LbMicrobenchScenario::GroqMultiregionAffinityOneExcluded
                | LbMicrobenchScenario::GroqMultiregionAffinityMultiExcluded
        ) {
            multiregion.cache_affinity_virtual_nodes = Some(150);
            multiregion.cache_affinity_backend_selection_count = Some(2);
        }
        if matches!(
            scenario,
            LbMicrobenchScenario::GroqMultiregionIgnoreQueue
                | LbMicrobenchScenario::GroqMultiregionIgnoreQueueOneExcluded
                | LbMicrobenchScenario::GroqMultiregionIgnoreQueueMultiExcluded
                | LbMicrobenchScenario::GroqMultiregionRttOnly
                | LbMicrobenchScenario::GroqMultiregionRttOnlyOneExcluded
                | LbMicrobenchScenario::GroqMultiregionRttOnlyMultiExcluded
        ) {
            multiregion.ignore_queue_time = Some(true);
        }
        if matches!(
            scenario,
            LbMicrobenchScenario::GroqMultiregionRttOnly
                | LbMicrobenchScenario::GroqMultiregionRttOnlyOneExcluded
                | LbMicrobenchScenario::GroqMultiregionRttOnlyMultiExcluded
        ) {
            multiregion.ignore_input_processing_time = Some(true);
        }
    }
    config
}

fn excluded_cluster_ids_for_scenario(scenario: LbMicrobenchScenario) -> Option<HashSet<String>> {
    if matches!(
        scenario,
        LbMicrobenchScenario::GroqMultiregionIgnoreQueueMultiExcluded
            | LbMicrobenchScenario::GroqMultiregionAffinityMultiExcluded
            | LbMicrobenchScenario::GroqMultiregionRttOnlyMultiExcluded
            | LbMicrobenchScenario::RoundRobinMultiExcluded
    ) {
        return Some(HashSet::from([
            "cluster-0000".to_string(),
            "cluster-0001".to_string(),
        ]));
    }

    matches!(
        scenario,
        LbMicrobenchScenario::PowerOfTwoOneExcluded
            | LbMicrobenchScenario::GroqMultiregionOneExcluded
            | LbMicrobenchScenario::GroqMultiregionIgnoreQueueOneExcluded
            | LbMicrobenchScenario::GroqMultiregionRttOnlyOneExcluded
            | LbMicrobenchScenario::GroqMultiregionAffinityOneExcluded
            | LbMicrobenchScenario::PulsarOneExcluded
            | LbMicrobenchScenario::RandomOneExcluded
            | LbMicrobenchScenario::RoundRobinOneExcluded
    )
    .then(|| HashSet::from(["cluster-0000".to_string()]))
}

fn build_candidates(count: usize) -> Vec<RoutedClusterSnapshot> {
    (0..count)
        .map(|index| {
            let running = (index % 9) as u64;
            let max_concurrency = 16;
            RoutedClusterSnapshot {
                cluster_id: format!("cluster-{index:04}"),
                stats: ModelStats {
                    output_tps: 120.0 + (index % 11) as f64,
                    last_mean_input_tps: 1_200.0 + (index % 13) as f64 * 31.0,
                    max_output_tps: 500.0,
                    queue_size: (index % 7) as u64,
                    queued_input_size: (index % 5) as u64 * 256,
                    kv_cache_capacity_tokens: 131_072,
                    kv_cache_used_tokens: 8_192 + (index % 23) as u64 * 64,
                    kv_cache_free_tokens: 122_880 - (index % 23) as u64 * 64,
                    num_running_queries: running,
                    max_engine_concurrency: max_concurrency,
                    total_query_input_size: (index % 6) as u64 * 384,
                    queue_time_estimate_ms_by_priority: queue_time_estimates(index),
                    ..ModelStats::default()
                },
                rtt: Duration::from_micros(500 + (index % 19) as u64 * 75),
                snapshot_updated_at: Instant::now(),
                status: InferenceServerStatus::Active,
                active_backend_count: 1,
            }
        })
        .collect()
}

fn queue_time_estimates(index: usize) -> HashMap<u32, u64> {
    let mut estimates = HashMap::new();
    estimates.insert(0, (index % 5) as u64);
    estimates.insert(2, 2 + (index % 11) as u64);
    estimates.insert(4, 4 + (index % 17) as u64);
    estimates
}

fn build_cache_keys(count: usize) -> Vec<String> {
    (0..count)
        .map(|index| format!("cache-prefix-{index:06}"))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lb_microbench_runs_default_scenarios() {
        let rows = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 8,
            warmup_iterations: 2,
            concurrency: 2,
            candidates: 4,
            cache_key_count: 4,
            scenarios: Vec::new(),
        })
        .expect("microbench should run");

        assert_eq!(rows.len(), 19);
        assert!(
            rows.iter()
                .any(|row| row.scenario.label() == "power-of-two")
        );
        assert!(rows.iter().all(|row| row.choices == row.iterations));
        assert!(rows.iter().all(|row| row.concurrency == 2));
        assert!(rows.iter().all(|row| row.ns_per_choose > 0.0));
        assert!(rows.iter().all(|row| row.selected_backend_count > 0));
        assert!(rows.iter().all(|row| {
            row.backend_counts
                .iter()
                .map(|(_, count)| count)
                .sum::<usize>()
                == row.choices
        }));
    }

    #[test]
    fn lb_microbench_rejects_zero_iterations() {
        let error = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 0,
            warmup_iterations: 0,
            concurrency: 1,
            candidates: 4,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::Pulsar],
        })
        .expect_err("zero iterations should fail");

        assert!(error.to_string().contains("--iterations"));
    }

    #[test]
    fn lb_microbench_rejects_zero_concurrency() {
        let error = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 1,
            warmup_iterations: 0,
            concurrency: 0,
            candidates: 4,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::Pulsar],
        })
        .expect_err("zero concurrency should fail");

        assert!(error.to_string().contains("--concurrency"));
    }

    #[test]
    fn random_one_excluded_scenario_never_selects_excluded_backend() {
        let rows = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 32,
            warmup_iterations: 4,
            concurrency: 2,
            candidates: 8,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::RandomOneExcluded],
        })
        .expect("microbench should run");

        let row = rows
            .first()
            .expect("random-one-excluded scenario should emit a row");
        assert_eq!(row.choices, row.iterations);
        assert!(
            row.backend_counts
                .iter()
                .all(|(cluster_id, _)| cluster_id != "cluster-0000")
        );
    }

    #[test]
    fn power_of_two_one_excluded_scenario_never_selects_excluded_backend() {
        let rows = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 32,
            warmup_iterations: 4,
            concurrency: 2,
            candidates: 8,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::PowerOfTwoOneExcluded],
        })
        .expect("microbench should run");

        let row = rows
            .first()
            .expect("power-of-two-one-excluded scenario should emit a row");
        assert_eq!(row.choices, row.iterations);
        assert!(
            row.backend_counts
                .iter()
                .all(|(cluster_id, _)| cluster_id != "cluster-0000")
        );
    }

    #[test]
    fn groq_multiregion_one_excluded_scenario_never_selects_excluded_backend() {
        let rows = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 32,
            warmup_iterations: 4,
            concurrency: 2,
            candidates: 8,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::GroqMultiregionOneExcluded],
        })
        .expect("microbench should run");

        let row = rows
            .first()
            .expect("groq-multiregion-one-excluded scenario should emit a row");
        assert_eq!(row.choices, row.iterations);
        assert!(
            row.backend_counts
                .iter()
                .all(|(cluster_id, _)| cluster_id != "cluster-0000")
        );
    }

    #[test]
    fn groq_multiregion_affinity_one_excluded_scenario_never_selects_excluded_backend() {
        let rows = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 32,
            warmup_iterations: 4,
            concurrency: 2,
            candidates: 8,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::GroqMultiregionAffinityOneExcluded],
        })
        .expect("microbench should run");

        let row = rows
            .first()
            .expect("groq-multiregion-affinity-one-excluded scenario should emit a row");
        assert_eq!(row.choices, row.iterations);
        assert!(
            row.backend_counts
                .iter()
                .all(|(cluster_id, _)| cluster_id != "cluster-0000")
        );
    }

    #[test]
    fn groq_multiregion_affinity_multi_excluded_scenario_never_selects_excluded_backend() {
        let rows = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 32,
            warmup_iterations: 4,
            concurrency: 2,
            candidates: 8,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::GroqMultiregionAffinityMultiExcluded],
        })
        .expect("microbench should run");

        let row = rows
            .first()
            .expect("groq-multiregion-affinity-multi-excluded scenario should emit a row");
        assert_eq!(row.choices, row.iterations);
        assert!(row.backend_counts.iter().all(|(cluster_id, _)| {
            cluster_id != "cluster-0000" && cluster_id != "cluster-0001"
        }));
    }

    #[test]
    fn groq_multiregion_ignore_queue_multi_excluded_scenario_never_selects_excluded_backend() {
        let rows = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 32,
            warmup_iterations: 4,
            concurrency: 2,
            candidates: 8,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::GroqMultiregionIgnoreQueueMultiExcluded],
        })
        .expect("microbench should run");

        let row = rows
            .first()
            .expect("groq-multiregion-ignore-queue-multi-excluded scenario should emit a row");
        assert_eq!(row.choices, row.iterations);
        assert!(row.backend_counts.iter().all(|(cluster_id, _)| {
            cluster_id != "cluster-0000" && cluster_id != "cluster-0001"
        }));
    }

    #[test]
    fn pulsar_one_excluded_scenario_never_selects_excluded_backend() {
        let rows = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 32,
            warmup_iterations: 4,
            concurrency: 2,
            candidates: 8,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::PulsarOneExcluded],
        })
        .expect("microbench should run");

        let row = rows
            .first()
            .expect("pulsar-one-excluded scenario should emit a row");
        assert_eq!(row.choices, row.iterations);
        assert!(
            row.backend_counts
                .iter()
                .all(|(cluster_id, _)| cluster_id != "cluster-0000")
        );
    }

    #[test]
    fn round_robin_one_excluded_scenario_never_selects_excluded_backend() {
        let rows = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 32,
            warmup_iterations: 4,
            concurrency: 2,
            candidates: 8,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::RoundRobinOneExcluded],
        })
        .expect("microbench should run");

        let row = rows
            .first()
            .expect("round-robin-one-excluded scenario should emit a row");
        assert_eq!(row.choices, row.iterations);
        assert!(
            row.backend_counts
                .iter()
                .all(|(cluster_id, _)| cluster_id != "cluster-0000")
        );
    }

    #[test]
    fn round_robin_multi_excluded_scenario_never_selects_excluded_backend() {
        let rows = run_lb_microbench(&LbMicrobenchConfig {
            iterations: 32,
            warmup_iterations: 4,
            concurrency: 2,
            candidates: 8,
            cache_key_count: 4,
            scenarios: vec![LbMicrobenchScenario::RoundRobinMultiExcluded],
        })
        .expect("microbench should run");

        let row = rows
            .first()
            .expect("round-robin-multi-excluded scenario should emit a row");
        assert_eq!(row.choices, row.iterations);
        assert!(row.backend_counts.iter().all(|(cluster_id, _)| {
            cluster_id != "cluster-0000" && cluster_id != "cluster-0001"
        }));
    }

    #[test]
    fn lb_microbench_csv_is_parseable() {
        let rows = vec![LbMicrobenchRow {
            scenario: LbMicrobenchScenario::Pulsar,
            candidates: 8,
            iterations: 10,
            warmup_iterations: 2,
            concurrency: 4,
            total_ns: 1234,
            ns_per_choose: 123.4,
            choices: 10,
            avg_rank_depth: 1.2,
            selected_backend_count: 2,
            top_backend: "cluster-0001".to_string(),
            top_backend_choices: 7,
            backend_counts: vec![
                ("cluster-0000".to_string(), 3),
                ("cluster-0001".to_string(), 7),
            ],
            checksum: 42,
        }];
        let mut output = Vec::new();

        write_lb_microbench_csv(&mut output, &rows).expect("csv should render");
        let rendered = String::from_utf8(output).expect("csv should be utf8");

        assert!(rendered.starts_with("scenario,candidates,iterations"));
        assert!(rendered.contains("selected_backend_count,top_backend,top_backend_choices"));
        assert!(rendered.contains(
            "pulsar,8,10,2,4,1234,123.4,10,1.200,2,cluster-0001,7,cluster-0000:3;cluster-0001:7,42"
        ));
    }

    #[test]
    fn measured_requests_start_after_warmup_key_range() {
        let target = RoutingTargetKey {
            routing_key: Some("tenant-a".to_string()),
            model_id: PULSAR_MODEL_ID.to_string(),
        };
        let cache_keys = build_cache_keys(4);
        let received_at = Instant::now();

        let first_measured =
            request_for_measured_iteration(&target, &cache_keys, 2, 0, received_at, None);
        let second_measured =
            request_for_measured_iteration(&target, &cache_keys, 2, 1, received_at, None);

        assert_eq!(
            first_measured.cache_affinity_key,
            Some(cache_keys[2].as_str())
        );
        assert_eq!(
            second_measured.cache_affinity_key,
            Some(cache_keys[3].as_str())
        );
    }

    #[test]
    fn measured_timer_starts_after_workers_are_ready() {
        let ready_barrier = Arc::new(Barrier::new(2));
        let release_barrier = Arc::new(Barrier::new(2));
        let helper_ready_barrier = Arc::clone(&ready_barrier);
        let helper_release_barrier = Arc::clone(&release_barrier);
        let (entered_tx, entered_rx) = std::sync::mpsc::channel();
        let helper = thread::spawn(move || {
            release_workers_after_ready_with_hook(
                &helper_ready_barrier,
                &helper_release_barrier,
                || {
                    entered_tx
                        .send(())
                        .expect("test should receive readiness hook");
                },
            )
        });

        entered_rx
            .recv()
            .expect("helper should enter readiness wait");
        let ready_at = Instant::now();
        ready_barrier.wait();
        release_barrier.wait();
        let start = helper
            .join()
            .expect("timer release helper should not panic");

        assert!(start >= ready_at);
    }

    #[test]
    fn merged_worker_results_use_worker_finish_time_for_elapsed_ns() {
        let start = Instant::now();
        let first_stats = LbMicrobenchStats {
            choices: 1,
            ..LbMicrobenchStats::default()
        };
        let second_stats = LbMicrobenchStats {
            choices: 2,
            ..LbMicrobenchStats::default()
        };

        let (stats, total_ns) = merge_worker_results(
            start,
            [
                LbMicrobenchWorkerResult {
                    stats: first_stats,
                    finished_at: start + Duration::from_nanos(7),
                },
                LbMicrobenchWorkerResult {
                    stats: second_stats,
                    finished_at: start + Duration::from_nanos(11),
                },
            ],
        );

        assert_eq!(stats.choices, 3);
        assert_eq!(total_ns, 11);
    }
}

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
use std::fmt;
use std::hint::black_box;
use std::io::Write;
use std::sync::Barrier;
use std::thread;
use std::time::{Duration, Instant};

use anyhow::Context;
use clap::ValueEnum;
use stargate::load_balancer::{
    LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig, LoadBalancerConfig,
    LoadBalancerModelConfig, LoadBalancerRequest, LoadBalancerRouter, LoadBalancerTargetState,
};
use stargate::routing::{RoutedClusterSnapshot, RoutingTargetKey};
use stargate_proto::pb::{InferenceServerStatus, ModelStats};

#[derive(Clone, Copy, PartialEq)]
enum MultiregionTuning {
    IgnoreQueue,
    RttOnly,
    Affinity,
}

struct LbMicrobenchScenarioMetadata {
    model_id: &'static str,
    algorithm: LoadBalancerAlgorithm,
    tuning: Option<MultiregionTuning>,
    excluded_clusters: usize,
}

use MultiregionTuning::{Affinity, IgnoreQueue, RttOnly};

macro_rules! scenarios {
    ($($scenario:ident, $model_id:literal, $algorithm:ident, $tuning:expr, $excluded:literal;)+) => {
        #[repr(usize)]
        #[derive(Clone, Copy, Debug, Eq, PartialEq, ValueEnum)]
        #[value(rename_all = "kebab-case")]
        pub enum LbMicrobenchScenario { $($scenario,)+ }

        const LB_MICROBENCH_SCENARIOS: [LbMicrobenchScenarioMetadata; 19] = [$(
            LbMicrobenchScenarioMetadata {
                model_id: $model_id,
                algorithm: LoadBalancerAlgorithm::$algorithm,
                tuning: $tuning,
                excluded_clusters: $excluded,
            },
        )+];
    };
}

// Keep the scenario matrix row-oriented so differences remain directly comparable.
#[rustfmt::skip]
scenarios! {
    PowerOfTwo, "lb-bench-power-of-two", PowerOfTwo, None, 0;
    PowerOfTwoOneExcluded, "lb-bench-power-of-two-one-excluded", PowerOfTwo, None, 1;
    GroqMultiregion, "lb-bench-multiregion", GroqMultiregion, None, 0;
    GroqMultiregionOneExcluded, "lb-bench-multiregion-one-excluded", GroqMultiregion, None, 1;
    GroqMultiregionIgnoreQueue, "lb-bench-multiregion-ignore-queue", GroqMultiregion, Some(IgnoreQueue), 0;
    GroqMultiregionIgnoreQueueOneExcluded, "lb-bench-multiregion-ignore-queue-one-excluded", GroqMultiregion, Some(IgnoreQueue), 1;
    GroqMultiregionIgnoreQueueMultiExcluded, "lb-bench-multiregion-ignore-queue-multi-excluded", GroqMultiregion, Some(IgnoreQueue), 2;
    GroqMultiregionRttOnly, "lb-bench-multiregion-rtt-only", GroqMultiregion, Some(RttOnly), 0;
    GroqMultiregionRttOnlyOneExcluded, "lb-bench-multiregion-rtt-only-one-excluded", GroqMultiregion, Some(RttOnly), 1;
    GroqMultiregionRttOnlyMultiExcluded, "lb-bench-multiregion-rtt-only-multi-excluded", GroqMultiregion, Some(RttOnly), 2;
    GroqMultiregionAffinity, "lb-bench-multiregion-affinity", GroqMultiregion, Some(Affinity), 0;
    GroqMultiregionAffinityOneExcluded, "lb-bench-multiregion-affinity-one-excluded", GroqMultiregion, Some(Affinity), 1;
    GroqMultiregionAffinityMultiExcluded, "lb-bench-multiregion-affinity-multi-excluded", GroqMultiregion, Some(Affinity), 2;
    Pulsar, "lb-bench-pulsar", Pulsar, None, 0;
    PulsarOneExcluded, "lb-bench-pulsar-one-excluded", Pulsar, None, 1;
    Random, "lb-bench-random", Random, None, 0;
    RandomOneExcluded, "lb-bench-random-one-excluded", Random, None, 1;
    RoundRobinOneExcluded, "lb-bench-round-robin-one-excluded", RoundRobin, None, 1;
    RoundRobinMultiExcluded, "lb-bench-round-robin-multi-excluded", RoundRobin, None, 2;
}

impl LbMicrobenchScenario {
    fn metadata(self) -> &'static LbMicrobenchScenarioMetadata {
        &LB_MICROBENCH_SCENARIOS[self as usize]
    }
}

impl fmt::Display for LbMicrobenchScenario {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(
            self.to_possible_value()
                .expect("scenario should have a CLI value")
                .get_name(),
        )
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

pub fn run_lb_microbench(config: &LbMicrobenchConfig) -> anyhow::Result<Vec<LbMicrobenchRow>> {
    validate_config(config)?;
    let scenarios = if config.scenarios.is_empty() {
        LbMicrobenchScenario::value_variants()
    } else {
        &config.scenarios
    };
    let candidates = build_candidates(config.candidates);
    let cache_keys = (0..config.cache_key_count)
        .map(|index| format!("cache-prefix-{index:06}"))
        .collect::<Vec<_>>();
    let mut rows = Vec::with_capacity(scenarios.len());
    for &scenario in scenarios {
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
        "methodology,scenario,candidates,iterations,warmup_iterations,concurrency,total_ns,ns_per_choose,choices,avg_rank_depth,selected_backend_count,top_backend,top_backend_choices,backend_counts,checksum"
    )?;
    for row in rows {
        writeln!(
            writer,
            "choose-only-v2,{},{},{},{},{},{},{:.1},{},{:.3},{},{},{},{},{}",
            row.scenario,
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
    for (flag, value) in [
        ("--iterations", config.iterations),
        ("--concurrency", config.concurrency),
        ("--candidates", config.candidates),
        ("--cache-key-count", config.cache_key_count),
    ] {
        if value == 0 {
            anyhow::bail!("{flag} must be greater than 0");
        }
    }
    Ok(())
}

fn run_scenario(
    config: &LbMicrobenchConfig,
    scenario: LbMicrobenchScenario,
    candidates: &[RoutedClusterSnapshot],
    cache_keys: &[String],
) -> anyhow::Result<LbMicrobenchRow> {
    let router = LoadBalancerRouter::from_config(&LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: HashMap::new(),
        models: HashMap::from([(
            scenario.metadata().model_id.to_string(),
            LoadBalancerModelConfig::Detailed(Box::new(config_for_scenario(scenario))),
        )]),
    })
    .with_context(|| format!("failed to build {scenario} load balancer"))?;
    let target_state = LoadBalancerTargetState::default();
    let excluded_cluster_ids = excluded_cluster_ids_for_scenario(scenario);
    let target = RoutingTargetKey {
        routing_key: Some("tenant-a".to_string()),
        model_id: scenario.metadata().model_id.to_string(),
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
        let _ = black_box(router.choose_candidate(&target_state, &request, candidates));
    }

    let worker_count = config.concurrency.min(config.iterations);
    let (stats, total_ns) = run_concurrent_measured_iterations(
        &router,
        LbMicrobenchRun {
            target: &target,
            target_state: &target_state,
            candidates,
            cache_keys,
            config,
            received_at,
            excluded_cluster_ids: excluded_cluster_ids.as_ref(),
        },
    );
    let choices = stats.candidate_counts.iter().sum::<usize>();
    let avg_rank_depth = if choices == 0 {
        0.0
    } else {
        stats.rank_depth_sum as f64 / choices as f64
    };
    let mut backend_counts = stats
        .candidate_counts
        .into_iter()
        .enumerate()
        .filter(|(_, count)| *count > 0)
        .map(|(index, count)| (candidates[index].cluster_id.clone(), count))
        .collect::<Vec<_>>();
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
        choices,
        avg_rank_depth,
        selected_backend_count: backend_counts.len(),
        top_backend,
        top_backend_choices,
        backend_counts,
        checksum: stats.checksum,
    })
}

struct LbMicrobenchStats {
    rank_depth_sum: usize,
    candidate_counts: Vec<usize>,
    checksum: u64,
}

struct LbMicrobenchMeasurements {
    rank_depth_sum: usize,
    candidate_counts: Vec<usize>,
    selected_candidate_indices: Vec<usize>,
}

impl LbMicrobenchMeasurements {
    fn new(candidate_count: usize, max_choices: usize) -> Self {
        Self {
            rank_depth_sum: 0,
            candidate_counts: vec![0; candidate_count],
            selected_candidate_indices: Vec::with_capacity(max_choices),
        }
    }

    fn record(&mut self, choice: stargate::load_balancer::LoadBalancerCandidateChoice) {
        self.rank_depth_sum += choice.rank_depth;
        self.candidate_counts[choice.candidate_index] += 1;
        self.selected_candidate_indices.push(choice.candidate_index);
    }
}

#[derive(Clone, Copy)]
struct LbMicrobenchRun<'a> {
    target: &'a RoutingTargetKey,
    target_state: &'a LoadBalancerTargetState,
    candidates: &'a [RoutedClusterSnapshot],
    cache_keys: &'a [String],
    config: &'a LbMicrobenchConfig,
    received_at: Instant,
    excluded_cluster_ids: Option<&'a HashSet<String>>,
}

fn run_concurrent_measured_iterations(
    router: &LoadBalancerRouter,
    run: LbMicrobenchRun<'_>,
) -> (LbMicrobenchStats, u128) {
    let worker_count = run.config.concurrency.min(run.config.iterations);
    let ready_barrier = Barrier::new(worker_count + 1);
    let release_barrier = Barrier::new(worker_count + 1);
    thread::scope(|scope| {
        let mut handles = Vec::with_capacity(worker_count);
        let mut worker_results = Vec::with_capacity(worker_count);
        for worker_index in 0..worker_count {
            let ready_barrier = &ready_barrier;
            let release_barrier = &release_barrier;
            let iterations = worker_iterations(run.config.iterations, worker_count, worker_index);
            handles.push(scope.spawn(move || {
                let measurements =
                    LbMicrobenchMeasurements::new(run.candidates.len(), iterations.len());
                ready_barrier.wait();
                release_barrier.wait();
                let mut measurements = measurements;
                for measured_iteration in iterations {
                    let request = request_for_iteration(
                        run.target,
                        run.cache_keys,
                        run.config.warmup_iterations + measured_iteration,
                        run.received_at,
                        run.excluded_cluster_ids,
                    );
                    if let Some(choice) = black_box(router.choose_candidate(
                        run.target_state,
                        &request,
                        run.candidates,
                    )) {
                        measurements.record(choice);
                    }
                }
                (measurements, Instant::now())
            }));
        }

        let start = release_workers_after_ready(&ready_barrier, &release_barrier);
        for handle in handles {
            worker_results.push(handle.join().expect("lb microbench worker panicked"));
        }
        merge_worker_results(start, run.candidates, worker_results)
    })
}

fn merge_worker_results(
    start: Instant,
    candidates: &[RoutedClusterSnapshot],
    results: impl IntoIterator<Item = (LbMicrobenchMeasurements, Instant)>,
) -> (LbMicrobenchStats, u128) {
    let mut stats = LbMicrobenchStats {
        rank_depth_sum: 0,
        candidate_counts: vec![0; candidates.len()],
        checksum: 0,
    };
    let mut total_ns = 0;
    for (measurements, finished_at) in results {
        total_ns = total_ns.max(finished_at.duration_since(start).as_nanos());
        stats.rank_depth_sum += measurements.rank_depth_sum;
        stats.checksum = stats.checksum.wrapping_add(
            measurements
                .selected_candidate_indices
                .iter()
                .fold(0, |checksum, &index| {
                    update_checksum(checksum, &candidates[index].cluster_id)
                }),
        );
        for (count, worker_count) in stats
            .candidate_counts
            .iter_mut()
            .zip(measurements.candidate_counts)
        {
            *count += worker_count;
        }
    }
    (stats, total_ns)
}

fn release_workers_after_ready(ready_barrier: &Barrier, release_barrier: &Barrier) -> Instant {
    ready_barrier.wait();
    let start = Instant::now();
    release_barrier.wait();
    start
}

fn worker_iterations(
    total_iterations: usize,
    worker_count: usize,
    worker_index: usize,
) -> std::ops::Range<usize> {
    let base = total_iterations / worker_count;
    let remainder = total_iterations % worker_count;
    let iteration_count = base + usize::from(worker_index < remainder);
    let start_iteration = worker_index * base + worker_index.min(remainder);
    start_iteration..start_iteration + iteration_count
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

fn config_for_scenario(scenario: LbMicrobenchScenario) -> LoadBalancerAlgorithmConfig {
    let metadata = scenario.metadata();
    let mut config = LoadBalancerAlgorithmConfig::from(metadata.algorithm);
    let is_pulsar = metadata.algorithm == LoadBalancerAlgorithm::Pulsar;
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
        if metadata.tuning == Some(Affinity) {
            multiregion.cache_affinity_virtual_nodes = Some(150);
            multiregion.cache_affinity_backend_selection_count = Some(2);
        }
        if matches!(metadata.tuning, Some(IgnoreQueue | RttOnly)) {
            multiregion.ignore_queue_time = Some(true);
        }
        if metadata.tuning == Some(RttOnly) {
            multiregion.ignore_input_processing_time = Some(true);
        }
    }
    config
}

fn excluded_cluster_ids_for_scenario(scenario: LbMicrobenchScenario) -> Option<HashSet<String>> {
    let count = scenario.metadata().excluded_clusters;
    (count > 0).then(|| {
        (0..count)
            .map(|index| format!("cluster-{index:04}"))
            .collect()
    })
}

fn build_candidates(count: usize) -> Vec<RoutedClusterSnapshot> {
    (0..count)
        .map(|index| RoutedClusterSnapshot {
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
                num_running_queries: (index % 9) as u64,
                max_engine_concurrency: 16,
                total_query_input_size: (index % 6) as u64 * 384,
                queue_time_estimate_ms_by_priority: HashMap::from([
                    (0, (index % 5) as u64),
                    (2, 2 + (index % 11) as u64),
                    (4, 4 + (index % 17) as u64),
                ]),
                ..ModelStats::default()
            },
            rtt: Duration::from_micros(500 + (index % 19) as u64 * 75),
            snapshot_updated_at: Instant::now(),
            status: InferenceServerStatus::Active,
            active_backend_count: 1,
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config(scenario: LbMicrobenchScenario) -> LbMicrobenchConfig {
        LbMicrobenchConfig {
            iterations: 32,
            warmup_iterations: 4,
            concurrency: 2,
            candidates: 8,
            cache_key_count: 4,
            scenarios: vec![scenario],
        }
    }

    fn assert_scenario_excludes(scenario: LbMicrobenchScenario, excluded: &[&str]) {
        let rows = run_lb_microbench(&config(scenario)).expect("microbench should run");
        let [row] = rows.as_slice() else {
            panic!("one scenario should emit one row");
        };
        assert_eq!(row.choices, row.iterations);
        assert!(
            row.backend_counts
                .iter()
                .all(|(cluster_id, _)| !excluded.contains(&cluster_id.as_str())),
            "{scenario} selected an excluded backend: {:?}",
            row.backend_counts
        );
    }

    #[test]
    fn lb_microbench_runs_default_scenarios() {
        let mut config = config(LbMicrobenchScenario::PowerOfTwo);
        config.iterations = 8;
        config.warmup_iterations = 2;
        config.candidates = 4;
        config.scenarios.clear();
        let rows = run_lb_microbench(&config).expect("microbench should run");

        assert_eq!(rows.len(), 19);
        assert_eq!(rows[0].scenario, LbMicrobenchScenario::PowerOfTwo);
        for row in rows {
            assert_eq!(row.choices, row.iterations);
            assert_eq!(row.concurrency, 2);
            assert!(row.ns_per_choose > 0.0);
            assert!(row.selected_backend_count > 0);
            assert_eq!(
                row.backend_counts
                    .iter()
                    .map(|(_, count)| count)
                    .sum::<usize>(),
                row.choices
            );
        }
    }

    #[test]
    fn lb_microbench_scenario_display_matches_cli_value_names() {
        for scenario in LbMicrobenchScenario::value_variants() {
            assert_eq!(
                scenario.to_string(),
                scenario
                    .to_possible_value()
                    .expect("scenario should have a CLI value name")
                    .get_name()
            );
        }
    }

    #[test]
    fn lb_microbench_scenario_metadata_covers_cli_inventory() {
        let cli_scenarios = LbMicrobenchScenario::value_variants();
        assert_eq!(LB_MICROBENCH_SCENARIOS.len(), cli_scenarios.len());

        let mut model_ids = HashSet::new();
        for scenario in cli_scenarios {
            assert!(
                model_ids.insert(scenario.metadata().model_id),
                "duplicate model ID for {scenario:?}"
            );
        }
    }

    #[test]
    fn lb_microbench_rejects_zero_iterations() {
        let mut config = config(LbMicrobenchScenario::Pulsar);
        config.iterations = 0;
        let error = run_lb_microbench(&config).expect_err("zero iterations should fail");

        assert!(error.to_string().contains("--iterations"));
    }

    #[test]
    fn lb_microbench_rejects_zero_concurrency() {
        let mut config = config(LbMicrobenchScenario::Pulsar);
        config.concurrency = 0;
        let error = run_lb_microbench(&config).expect_err("zero concurrency should fail");

        assert!(error.to_string().contains("--concurrency"));
    }

    macro_rules! exclusion_tests {
        ($($name:ident: $scenario:ident => [$($excluded:literal),+];)+) => {$(
            #[test]
            fn $name() {
                assert_scenario_excludes(LbMicrobenchScenario::$scenario, &[$($excluded),+]);
            }
        )+};
    }

    #[rustfmt::skip]
    exclusion_tests! {
        random_one_excluded_scenario_never_selects_excluded_backend: RandomOneExcluded => ["cluster-0000"];
        power_of_two_one_excluded_scenario_never_selects_excluded_backend: PowerOfTwoOneExcluded => ["cluster-0000"];
        groq_multiregion_one_excluded_scenario_never_selects_excluded_backend: GroqMultiregionOneExcluded => ["cluster-0000"];
        groq_multiregion_affinity_one_excluded_scenario_never_selects_excluded_backend: GroqMultiregionAffinityOneExcluded => ["cluster-0000"];
        groq_multiregion_affinity_multi_excluded_scenario_never_selects_excluded_backend: GroqMultiregionAffinityMultiExcluded => ["cluster-0000", "cluster-0001"];
        groq_multiregion_ignore_queue_multi_excluded_scenario_never_selects_excluded_backend: GroqMultiregionIgnoreQueueMultiExcluded => ["cluster-0000", "cluster-0001"];
        pulsar_one_excluded_scenario_never_selects_excluded_backend: PulsarOneExcluded => ["cluster-0000"];
        round_robin_one_excluded_scenario_never_selects_excluded_backend: RoundRobinOneExcluded => ["cluster-0000"];
        round_robin_multi_excluded_scenario_never_selects_excluded_backend: RoundRobinMultiExcluded => ["cluster-0000", "cluster-0001"];
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

        assert!(rendered.starts_with("methodology,scenario,candidates,iterations"));
        assert!(rendered.contains("selected_backend_count,top_backend,top_backend_choices"));
        assert!(rendered.contains(
            "choose-only-v2,pulsar,8,10,2,4,1234,123.4,10,1.200,2,cluster-0001,7,cluster-0000:3;cluster-0001:7,42"
        ));
    }

    #[test]
    fn measured_requests_start_after_warmup_key_range() {
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: String::new(),
        };
        let cache_keys = ["key-0", "key-1", "key-2", "key-3"].map(String::from);
        let received_at = Instant::now();

        for measured_iteration in 0..2 {
            let iteration = 2 + measured_iteration;
            assert_eq!(
                request_for_iteration(&target, &cache_keys, iteration, received_at, None)
                    .cache_affinity_key,
                Some(cache_keys[2 + measured_iteration].as_str())
            );
        }
    }

    #[test]
    fn measured_timer_starts_after_workers_are_ready() {
        let ready_barrier = Barrier::new(2);
        let release_barrier = Barrier::new(2);
        let (entered_tx, entered_rx) = std::sync::mpsc::channel();
        thread::scope(|scope| {
            let helper = scope.spawn(|| {
                entered_tx
                    .send(())
                    .expect("test should receive readiness hook");
                release_workers_after_ready(&ready_barrier, &release_barrier)
            });
            entered_rx
                .recv()
                .expect("helper should enter readiness wait");
            let ready_at = Instant::now();
            ready_barrier.wait();
            release_barrier.wait();
            assert!(helper.join().expect("timer helper should not panic") >= ready_at);
        });
    }

    #[test]
    fn merged_worker_results_use_worker_finish_time_for_elapsed_ns() {
        let start = Instant::now();
        let candidates = build_candidates(4);
        let choice = |candidate_index| stargate::load_balancer::LoadBalancerCandidateChoice {
            candidate_index,
            rank_depth: 1,
            selected_after_kv_free_tokens_skip: false,
        };

        let mut allocation_probe = LbMicrobenchMeasurements::new(candidates.len(), 32);
        let counts_storage = allocation_probe.candidate_counts.as_ptr();
        let choices_storage = allocation_probe.selected_candidate_indices.as_ptr();
        for iteration in 0..32 {
            allocation_probe.record(choice(iteration % candidates.len()));
        }
        assert_eq!(allocation_probe.candidate_counts.as_ptr(), counts_storage);
        assert_eq!(
            allocation_probe.selected_candidate_indices.as_ptr(),
            choices_storage,
            "measured aggregation buffers must not move while recording choices"
        );

        let mut first_measurements = LbMicrobenchMeasurements::new(candidates.len(), 1);
        first_measurements.record(choice(0));
        let mut second_measurements = LbMicrobenchMeasurements::new(candidates.len(), 2);
        second_measurements.record(choice(1));
        second_measurements.record(choice(1));
        let mut worker_results = Vec::with_capacity(2);
        let result_storage = worker_results.as_ptr();
        worker_results.push((first_measurements, start + Duration::from_nanos(7)));
        worker_results.push((second_measurements, start + Duration::from_nanos(11)));
        assert_eq!(
            worker_results.as_ptr(),
            result_storage,
            "raw worker-result storage must not grow while workers are timed"
        );

        let (stats, total_ns) = merge_worker_results(start, &candidates, worker_results);

        assert_eq!(stats.candidate_counts, [1, 2, 0, 0]);
        let expected_checksum =
            update_checksum(0, &candidates[0].cluster_id).wrapping_add(update_checksum(
                update_checksum(0, &candidates[1].cluster_id),
                &candidates[1].cluster_id,
            ));
        assert_eq!(stats.checksum, expected_checksum);
        assert_eq!(total_ns, 11);
    }
}

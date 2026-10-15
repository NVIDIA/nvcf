// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//! Routing invariants under load, checked for every algorithm in
//! `LoadBalancerAlgorithm::ALL` (the stargate crate keeps that list in sync
//! with `routing_methods.json`) at its default settings. Every scenario runs
//! several Stargates that learn about each other's work only through stale
//! Pylon stats, and leaves Pylon queue-mismatch thresholds at the production
//! defaults so threshold changes are covered.

use stargate::load_balancer::LoadBalancerAlgorithm;

use crate::config::SimConfig;
use crate::metrics::RunSummary;
use crate::sim::{RunSpec, run};

const SEEDS: [u64; 3] = [1, 2, 3];
/// Allowed absolute gap between a backend's share of successful requests and
/// its expected share.
const SHARE_TOLERANCE: f64 = 0.10;
/// At most one request in this many may fail with no route while a free
/// engine slot was visible. One such failure showed up once in a few hundred
/// local runs and did not reproduce; reverting PR #2133 fails several percent.
const FREE_SLOT_FAILURE_BUDGET: usize = 100;
/// Extra stats delay, so each Stargate routes on a view that misses recent
/// work from the other Stargates.
const STALE_STATS_MS: f64 = 200.0;
/// Above what the fast versions serve alone, below the whole fleet.
const VERSIONS_RATE_RPS: f64 = 28.0;
/// Well above what one backend serves, for a burst it drains within the
/// client's wait budget.
const BURST_RATE_RPS: f64 = 25.0;

fn config(
    regions: serde_json::Value,
    rtt_ms: serde_json::Value,
    max_wait_ms: Option<u64>,
) -> SimConfig {
    let policies: Vec<_> = LoadBalancerAlgorithm::ALL
        .iter()
        .map(|algorithm| {
            let name = serde_json::to_value(algorithm).expect("algorithm serializes");
            serde_json::json!({"name": name, "load_balancer": {"algorithm": name}})
        })
        .collect();
    let config: SimConfig = serde_json::from_value(serde_json::json!({
        "name": "routing invariants",
        "seeds": SEEDS,
        "topology": {
            "regions": regions, "rtt_ms": rtt_ms, "intra_region_rtt_ms": 1.0,
            "stats_relay_delay_ms": STALE_STATS_MS
        },
        "engine": {
            "num_gpu_workers": 1, "max_num_seqs": 4, "max_batched_tokens": 2048,
            "step_fixed_ms": 4.0, "step_decode_ms_per_seq": 0.1,
            "step_prefill_ms_per_token": 0.05, "kv_cache_capacity_tokens": 100000
        },
        "pylon": {
            "heartbeat_ms": 1000.0,
            "stats_update_coalesce_ms": 10.0,
            "input_tps": {"model": "fallback-window", "initial": 20000.0, "window": 8, "duration_floor_ms": 10.0}
        },
        "stargate": {"max_request_retries": 2, "routing_key": "rk", "model_id": "m"},
        "client": {
            "request_slo_ms": 60000, "max_wait_ms": max_wait_ms, "timeout_ms": 120000,
            "ttft_slo_ms": 60000
        },
        "workload": {
            "rates_rps": [1.0], "warmup_s": 2.0, "measure_s": 3.0,
            "fixed": {
                "sessions": 200, "input_tokens_min": 500, "input_tokens_max": 1500,
                "output_tokens": 64
            }
        },
        "policies": policies
    }))
    .expect("invariant config parses");
    config.validate().expect("invariant config is valid");
    config
}

/// Two fast versions take all client traffic; a half-speed version of the
/// same model has no Stargates of its own. Clients send no wait budget, the
/// case PR #2133 fixed.
fn versions_config() -> SimConfig {
    let mut config = config(
        serde_json::json!([
            {"name": "fast", "stargates": 4, "backends": 2},
            {"name": "slow", "stargates": 0, "backends": 1, "backend_speed": 0.5, "traffic_weight": 0.0}
        ]),
        serde_json::json!([[0, 2], [2, 0]]),
        None,
    );
    config.workload.measure_s = 10.0;
    config
}

/// Runs every algorithm for every seed and checks that each measured request
/// has exactly one outcome.
fn run_every_algorithm(
    config: &SimConfig,
    rate_rps: f64,
) -> Vec<(LoadBalancerAlgorithm, RunSummary)> {
    assert_eq!(config.policies.len(), LoadBalancerAlgorithm::ALL.len());
    let mut runs = Vec::new();
    for (algorithm, policy) in LoadBalancerAlgorithm::ALL.into_iter().zip(&config.policies) {
        for &seed in &config.seeds {
            let summary = run(&RunSpec {
                config,
                policy,
                rate_rps,
                seed,
            })
            .expect("run completes");
            assert!(summary.offered > 0, "{}", label(&summary));
            assert_eq!(
                summary.succeeded
                    + summary.failed_no_route
                    + summary.failed_retries_exhausted
                    + summary.failed_timeout,
                summary.offered,
                "{}: every measured request has one outcome",
                label(&summary)
            );
            runs.push((algorithm, summary));
        }
    }
    runs
}

fn label(summary: &RunSummary) -> String {
    format!("{} seed {}", summary.policy, summary.seed)
}

/// Serving capacity share per backend, in summary order: GPU workers times
/// region speed.
fn capacity_shares(config: &SimConfig, summary: &RunSummary) -> Vec<f64> {
    let speeds: Vec<f64> = config
        .topology
        .regions
        .iter()
        .flat_map(|region| std::iter::repeat_n(region.backend_speed, region.backends))
        .collect();
    assert_eq!(speeds.len(), summary.backends.len());
    let capacity: Vec<f64> = summary
        .backends
        .iter()
        .zip(speeds)
        .map(|(backend, speed)| backend.gpu_workers as f64 * speed)
        .collect();
    let total: f64 = capacity.iter().sum();
    capacity.into_iter().map(|c| c / total).collect()
}

fn success_shares(summary: &RunSummary) -> Vec<f64> {
    let total: usize = summary.backends.iter().map(|b| b.succeeded).sum();
    assert!(total > 0, "{}: no successes", label(summary));
    summary
        .backends
        .iter()
        .map(|backend| backend.succeeded as f64 / total as f64)
        .collect()
}

/// Round-robin and random ignore load by design, so their expected share is
/// an even split; every other algorithm should follow capacity.
fn expected_shares(algorithm: LoadBalancerAlgorithm, capacity: &[f64]) -> Vec<f64> {
    match algorithm {
        LoadBalancerAlgorithm::RoundRobin | LoadBalancerAlgorithm::Random => {
            vec![1.0 / capacity.len() as f64; capacity.len()]
        }
        _ => capacity.to_vec(),
    }
}

/// Wait-and-widen rejects only when every backend it sees is busy, so its
/// rejections show the fast versions alone cannot carry the versions rate.
#[test]
fn fast_versions_alone_saturate_at_the_versions_rate() {
    let mut config = versions_config();
    config.topology.regions[1].backends = 0;
    for (algorithm, summary) in run_every_algorithm(&config, VERSIONS_RATE_RPS) {
        if algorithm != LoadBalancerAlgorithm::WaitAndWiden {
            continue;
        }
        let run = label(&summary);
        assert!(
            summary.failed_no_route > 0,
            "{run}: fast versions were not saturated"
        );
        assert!(
            summary.failed_no_route_with_free_slot <= summary.offered / FREE_SLOT_FAILURE_BUDGET,
            "{run}: {} rejections while a backend had a free engine slot",
            summary.failed_no_route_with_free_slot
        );
    }
}

#[test]
fn fast_and_slow_versions_split_saturated_load_by_capacity() {
    let config = versions_config();
    for (algorithm, summary) in run_every_algorithm(&config, VERSIONS_RATE_RPS) {
        let run = label(&summary);
        assert!(
            summary.failed_no_route_with_free_slot <= summary.offered / FREE_SLOT_FAILURE_BUDGET,
            "{run}: {} of {} requests got no_eligible_backend while a backend had a free engine slot",
            summary.failed_no_route_with_free_slot,
            summary.offered
        );
        assert_eq!(
            summary.failed_retries_exhausted, 0,
            "{run}: 503 after Pylon queue-mismatch rejections"
        );
        assert_eq!(summary.failed_timeout, 0, "{run}: client timeouts");
        let capacity = capacity_shares(&config, &summary);
        let shares = success_shares(&summary);
        let slow = shares.len() - 1;
        assert!(
            shares[slow] >= capacity[slow] / 2.0,
            "{run}: overflow did not reach the slow version: share {:.2}, capacity share {:.2}",
            shares[slow],
            capacity[slow]
        );
        let expected = expected_shares(algorithm, &capacity);
        for ((backend, share), expected) in summary.backends.iter().zip(&shares).zip(&expected) {
            assert!(
                (share - expected).abs() <= SHARE_TOLERANCE,
                "{run}: {} served {share:.2} of requests, expected {expected:.2} +/- {SHARE_TOLERANCE}",
                backend.id
            );
        }
    }
}

/// One backend takes a burst from several Stargates. Each Stargate's queue
/// estimate misses the work the others sent until stats catch up, which is
/// the case PR #2147 fixed.
#[test]
fn burst_to_one_backend_queues_instead_of_failing() {
    let config = config(
        serde_json::json!([{"name": "a", "stargates": 4, "backends": 1}]),
        serde_json::json!([[0]]),
        Some(60_000),
    );
    for (algorithm, summary) in run_every_algorithm(&config, BURST_RATE_RPS) {
        // With one backend, a single queue-mismatch rejection fails a
        // wait-and-widen request even with wait budget left (issue #2567).
        if matches!(
            algorithm,
            LoadBalancerAlgorithm::WaitAndWiden | LoadBalancerAlgorithm::PulsarWaitAndWiden
        ) {
            continue;
        }
        let run = label(&summary);
        assert_eq!(
            summary.succeeded,
            summary.offered,
            "{run}: burst requests failed while the backend could drain them: \
             {} no route, {} retries exhausted after {} queue-mismatch rejections, {} timed out",
            summary.failed_no_route,
            summary.failed_retries_exhausted,
            summary.mismatch_rejections,
            summary.failed_timeout
        );
        assert!(
            summary.ttft_ms.p99 > 1000.0,
            "{run}: the burst should queue on the backend, TTFT p99 {:.0} ms",
            summary.ttft_ms.p99
        );
    }
}

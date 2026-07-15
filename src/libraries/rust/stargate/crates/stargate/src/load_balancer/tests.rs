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
use std::time::{Duration, Instant};

use stargate_proto::pb::{InferenceServerStatus, ModelStats};

use super::*;
use crate::load_balancer::algorithm::{MAX_CACHE_AFFINITY_CACHE_KEY_BYTES, input_work_seconds};
use crate::load_balancer::groq_multiregion::{
    GroqMultiregionConfig, GroqMultiregionLoadBalancer, cache_affinity_candidate_indices,
    cache_affinity_candidates, cache_affinity_virtual_node_hash, groq_multiregion_ttft_components,
};
use crate::load_balancer::pulsar::{PulsarLoadBalancer, pulsar_hash64, pulsar_ranked_indices};
use crate::routing::{RoutedClusterSnapshot, RoutingTargetKey};
use xxhash_rust::xxh3::xxh3_64;

pub(super) struct SelectedCandidateForTest {
    pub(super) candidate: SelectedClusterForTest,
    pub(super) rank_depth: usize,
    pub(super) selected_after_kv_free_tokens_skip: bool,
}

pub(super) struct SelectedClusterForTest {
    pub(super) cluster_id: String,
}

pub(super) trait LoadBalancerTestChoiceExt: LoadBalancer {
    fn choose_for_test(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<SelectedCandidateForTest> {
        self.choose_candidate(request, candidates)
            .map(|choice| SelectedCandidateForTest {
                candidate: SelectedClusterForTest {
                    cluster_id: candidates[choice.candidate_index].cluster_id.clone(),
                },
                rank_depth: choice.rank_depth,
                selected_after_kv_free_tokens_skip: choice.selected_after_kv_free_tokens_skip,
            })
    }
}

impl<T> LoadBalancerTestChoiceExt for T where T: LoadBalancer + ?Sized {}

fn target_with_model(model_id: &str) -> RoutingTargetKey {
    target_with_routing_key("rk-1", model_id)
}

fn target_with_routing_key(routing_key: &str, model_id: &str) -> RoutingTargetKey {
    RoutingTargetKey::new(Some(routing_key.to_string()), model_id)
}

fn target() -> RoutingTargetKey {
    target_with_model("model-a")
}

fn request<'a>(
    target: &'a RoutingTargetKey,
    cache_affinity_key: Option<&'a str>,
    input_tokens: Option<u64>,
) -> LoadBalancerRequest<'a> {
    request_with_priority(target, cache_affinity_key, input_tokens, 0)
}

fn request_with_priority<'a>(
    target: &'a RoutingTargetKey,
    cache_affinity_key: Option<&'a str>,
    input_tokens: Option<u64>,
    priority: u32,
) -> LoadBalancerRequest<'a> {
    LoadBalancerRequest {
        routing_target: target,
        cache_affinity_key,
        input_tokens,
        priority,
        received_at: Instant::now(),
        request_slo: None,
        excluded_cluster_ids: None,
    }
}

fn groq_multiregion_algorithm_config(
    configure: impl FnOnce(&mut GroqMultiregionAlgorithmConfig),
) -> LoadBalancerAlgorithmConfig {
    let mut config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::GroqMultiregion);
    configure(
        config
            .multiregion_settings_mut()
            .expect("groq-multiregion config should expose multiregion settings"),
    );
    config
}

fn groq_affinity_algorithm_config(
    virtual_nodes: usize,
    selection_count: usize,
    sample_count: Option<usize>,
) -> LoadBalancerAlgorithmConfig {
    groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(virtual_nodes);
        settings.cache_affinity_backend_selection_count = Some(selection_count);
        settings.n = sample_count;
    })
}

fn groq_affinity_config(
    virtual_nodes: usize,
    selection_count: usize,
    sample_count: Option<usize>,
) -> GroqMultiregionConfig {
    GroqMultiregionConfig::from_algorithm_config(&groq_affinity_algorithm_config(
        virtual_nodes,
        selection_count,
        sample_count,
    ))
}

fn seeded_pulsar_algorithm_config(seed: &str) -> LoadBalancerAlgorithmConfig {
    let mut config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
    config
        .set_seed(Some(seed.to_string()))
        .expect("pulsar supports deterministic seeding");
    config.request_policy_mut().require_cache_affinity_key = true;
    config.request_policy_mut().require_input_tokens = true;
    config
}

#[test]
fn set_seed_reports_unsupported_algorithms_without_panicking() {
    for algorithm in [
        LoadBalancerAlgorithm::PowerOfTwo,
        LoadBalancerAlgorithm::RoundRobin,
        LoadBalancerAlgorithm::Random,
    ] {
        let mut config = LoadBalancerAlgorithmConfig::from(algorithm);
        assert_eq!(
            config.set_seed(Some("seed-1".to_string())),
            Err(LoadBalancerSeedError::Unsupported { algorithm })
        );
        assert_eq!(config.seed(), None);
    }
}

#[test]
fn pulsar_multiregion_seed_has_one_authoritative_owner() {
    let mut config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PulsarMultiregion);
    config
        .multiregion_settings_mut()
        .expect("pulsar-multiregion should expose multiregion settings")
        .seed = Some("shared-seed".to_string());

    assert_eq!(config.seed(), Some("shared-seed"));
}

fn kv_aware_pulsar_algorithm_config(seed: &str) -> LoadBalancerAlgorithmConfig {
    let mut config = seeded_pulsar_algorithm_config(seed);
    config.request_policy_mut().consider_kv_free_tokens = true;
    config
}

fn candidate(id: &str, kv_cache_free_tokens: u64) -> RoutedClusterSnapshot {
    RoutedClusterSnapshot {
        cluster_id: id.to_string(),
        stats: ModelStats {
            last_mean_input_tps: 100.0,
            max_output_tps: 100.0,
            kv_cache_capacity_tokens: 1024,
            kv_cache_used_tokens: 1024 - kv_cache_free_tokens,
            kv_cache_free_tokens,
            ..ModelStats::default()
        },
        rtt: Duration::from_millis(5),
        snapshot_updated_at: Instant::now(),
        status: InferenceServerStatus::Active,
        active_backend_count: 1,
    }
}

fn candidates(ids: &[&str]) -> Vec<RoutedClusterSnapshot> {
    ids.iter().map(|id| candidate(id, 1024)).collect()
}

fn excluded(ids: &[&str]) -> HashSet<String> {
    ids.iter().map(|id| (*id).to_string()).collect()
}

trait CandidateFixture: Sized {
    fn with_rtt_ms(self, rtt_ms: u64) -> Self;
    fn with_stats(self, configure: impl FnOnce(&mut ModelStats)) -> Self;
}

impl CandidateFixture for RoutedClusterSnapshot {
    fn with_rtt_ms(mut self, rtt_ms: u64) -> Self {
        self.rtt = Duration::from_millis(rtt_ms);
        self
    }

    fn with_stats(mut self, configure: impl FnOnce(&mut ModelStats)) -> Self {
        configure(&mut self.stats);
        self
    }
}

fn work_candidate(
    id: &str,
    rtt_ms: u64,
    input_tps: f64,
    queued_input: u64,
) -> RoutedClusterSnapshot {
    work_candidate_with_kv(id, 1024, rtt_ms, input_tps, queued_input)
}

fn work_candidate_with_kv(
    id: &str,
    kv_cache_free_tokens: u64,
    rtt_ms: u64,
    input_tps: f64,
    queued_input: u64,
) -> RoutedClusterSnapshot {
    candidate(id, kv_cache_free_tokens)
        .with_rtt_ms(rtt_ms)
        .with_stats(|stats| {
            stats.last_mean_input_tps = input_tps;
            stats.queued_input_size = queued_input;
        })
}

fn concurrency_candidate(
    id: &str,
    rtt_ms: u64,
    maximum: u64,
    running: u64,
) -> RoutedClusterSnapshot {
    candidate(id, 1024).with_rtt_ms(rtt_ms).with_stats(|stats| {
        stats.max_engine_concurrency = maximum;
        stats.num_running_queries = running;
    })
}

fn priority_candidate(id: &str, priority: u32, queue_time_ms: u64) -> RoutedClusterSnapshot {
    candidate(id, 1024).with_stats(|stats| {
        stats
            .queue_time_estimate_ms_by_priority
            .insert(priority, queue_time_ms);
    })
}

fn max_queue_time(
    floor_ms: u64,
    ceil_ms: u64,
    elapsed: Duration,
    request_slo: Option<Duration>,
) -> Duration {
    let config = groq_multiregion_algorithm_config(|settings| {
        settings.max_queue_time_floor_ms = Some(floor_ms);
        settings.max_queue_time_ceil_ms = Some(ceil_ms);
    });
    let target = target();
    let request = LoadBalancerRequest {
        received_at: Instant::now() - elapsed,
        request_slo,
        ..request(&target, None, Some(0))
    };
    GroqMultiregionConfig::from_algorithm_config(&config)
        .max_queue_time(&request)
        .expect("floor and ceil should enable max queue time")
}

fn groq_load_balancer(
    configure: impl FnOnce(&mut GroqMultiregionAlgorithmConfig),
) -> std::sync::Arc<dyn LoadBalancer> {
    create_load_balancer_with_config(&groq_multiregion_algorithm_config(configure))
        .expect("factory should accept groq-multiregion")
}

fn assert_repeated_choice(
    load_balancer: &dyn LoadBalancer,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    samples: usize,
    expected_cluster_id: &str,
) {
    for _ in 0..samples {
        let chosen = choose(load_balancer, request, candidates);
        assert_eq!(chosen.candidate.cluster_id, expected_cluster_id);
    }
}

fn choose(
    load_balancer: &dyn LoadBalancer,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> SelectedCandidateForTest {
    load_balancer
        .choose_for_test(request, candidates)
        .expect("candidate should be selected")
}

fn choose_from_router(
    router: &LoadBalancerRouter,
    target_state: &LoadBalancerTargetState,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> SelectedCandidateForTest {
    router
        .choose_for_test(target_state, request, candidates)
        .expect("candidate should be selected")
}

fn assert_excluded_queue_choice(rtt_only: bool, excluded_ids: &[&str]) {
    let load_balancer = groq_load_balancer(|settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
        settings.ignore_input_processing_time = rtt_only.then_some(true);
    });
    let target = target();
    let excluded = excluded(excluded_ids);
    let mut request = request(&target, None, Some(512));
    request.excluded_cluster_ids = Some(&excluded);
    let mut candidates = excluded_ids
        .iter()
        .map(|id| candidate(id, 1024).with_rtt_ms(5))
        .collect::<Vec<_>>();
    candidates.extend([
        work_candidate("higher-queue", 50, 100.0, 2),
        work_candidate("lower-queue", 50, 100.0, 1),
    ]);
    assert_repeated_choice(
        load_balancer.as_ref(),
        &request,
        &candidates,
        16,
        "lower-queue",
    );
}

type AffinityRetryFixture = (
    GroqMultiregionConfig,
    RoutingTargetKey,
    Vec<RoutedClusterSnapshot>,
    HashSet<String>,
    String,
);

fn affinity_retry_fixture(excluded_ids: &[&str]) -> AffinityRetryFixture {
    let config = groq_affinity_config(32, 1, None);
    let target = target();
    let mut candidates = excluded_ids
        .iter()
        .enumerate()
        .map(|(index, id)| candidate(id, 1024).with_rtt_ms((index as u64 + 1) * 5))
        .collect::<Vec<_>>();
    candidates.extend([
        candidate("affinity-successor", 1024).with_rtt_ms(100),
        candidate("global-fast", 1024).with_rtt_ms(1),
    ]);
    let excluded = excluded(excluded_ids);

    for index in 0..8192 {
        let key = format!("retry-prefix-{index}");
        let base_request = request(&target, Some(&key), Some(1));
        let primary = cache_affinity_candidate_indices(&config, &base_request, &candidates)
            .expect("cache affinity should select a backend");
        if candidates[primary[0]].cluster_id != excluded_ids[0] {
            continue;
        }
        let retry_request = LoadBalancerRequest {
            excluded_cluster_ids: Some(&excluded),
            ..base_request
        };
        let retry = cache_affinity_candidate_indices(&config, &retry_request, &candidates)
            .expect("retry should select an affinity successor");
        if candidates[retry[0]].cluster_id != "affinity-successor" {
            continue;
        }
        return (config, target, candidates, excluded, key);
    }

    panic!("expected an affinity key with excluded primaries and a distinct successor");
}

fn assert_cache_affinity_retry(excluded_ids: &[&str]) {
    let (config, target, candidates, excluded, key) = affinity_retry_fixture(excluded_ids);
    let load_balancer = GroqMultiregionLoadBalancer::new(config);
    let request = request(&target, Some(&key), Some(1));
    choose(&load_balancer, &request, &candidates);
    let retry_request = LoadBalancerRequest {
        excluded_cluster_ids: Some(&excluded),
        ..request
    };
    let chosen = choose(&load_balancer, &retry_request, &candidates);
    assert_eq!(chosen.candidate.cluster_id, "affinity-successor");
    let cached_key_bytes = load_balancer.cached_affinity_key_bytes();
    assert!(cached_key_bytes >= key.len() * 2 + excluded.iter().map(String::len).sum::<usize>());

    let chosen_again = choose(&load_balancer, &retry_request, &candidates);
    assert_eq!(chosen_again.candidate.cluster_id, "affinity-successor");
    assert_eq!(load_balancer.cached_affinity_key_bytes(), cached_key_bytes);
}

macro_rules! groq_choice_tests {
    ($(
        $name:ident:
        $configure:expr;
        $request:expr;
        [$($candidate:expr),+ $(,)?];
        $samples:expr => $expected:expr;
    )+) => {
        $(
            #[test]
            fn $name() {
                let load_balancer = groq_load_balancer($configure);
                let target = target();
                let request = ($request)(&target);
                let candidates = [$($candidate),+];
                assert_repeated_choice(
                    load_balancer.as_ref(),
                    &request,
                    &candidates,
                    $samples,
                    $expected,
                );
            }
        )+
    };
}

fn selected_cluster_id(
    choice: LoadBalancerCandidateChoice,
    candidates: &[RoutedClusterSnapshot],
) -> &str {
    &candidates[choice.candidate_index].cluster_id
}

fn choose_with_override(
    router: &LoadBalancerRouter,
    target_state: &LoadBalancerTargetState,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    algorithm: &str,
) -> LoadBalancerCandidateSelection {
    let algorithm = LoadBalancerAlgorithmOverride::parse(algorithm)
        .expect("routing algorithm override should parse");
    router
        .choose_candidate_with_algorithm_override(
            target_state,
            request,
            candidates,
            Some(&algorithm),
        )
        .expect("routing method should be available")
        .expect("candidate should be selected")
}

fn round_robin_override_choice(
    router: &LoadBalancerRouter,
    target_state: &LoadBalancerTargetState,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> LoadBalancerCandidateChoice {
    choose_with_override(router, target_state, request, candidates, "round-robin").choice
}

fn router_with_default(default: LoadBalancerAlgorithm) -> LoadBalancerRouter {
    router_with_options(default, &[], None)
}

fn router_with_model(
    default: LoadBalancerAlgorithm,
    model_id: &str,
    model: LoadBalancerModelConfig,
) -> LoadBalancerRouter {
    router_with_options(default, &[], Some((model_id, model)))
}

fn router_from_json(raw: &str) -> LoadBalancerRouter {
    let config = parse_json(raw);
    LoadBalancerRouter::from_config(&config).expect("router should build")
}

fn parse_json<T: serde::de::DeserializeOwned>(raw: &str) -> T {
    serde_json::from_str(raw).expect("config should parse")
}

fn router_with_options(
    default: LoadBalancerAlgorithm,
    request_algorithms: &[LoadBalancerAlgorithm],
    model: Option<(&str, LoadBalancerModelConfig)>,
) -> LoadBalancerRouter {
    LoadBalancerRouter::from_config(&LoadBalancerConfig {
        default,
        request_algorithms: request_algorithms
            .iter()
            .copied()
            .map(|algorithm| (algorithm, LoadBalancerModelConfig::Name(algorithm)))
            .collect(),
        models: model
            .map(|(id, config)| (id.to_string(), config))
            .into_iter()
            .collect(),
    })
    .expect("router config should parse")
}

fn assert_independent_round_robin_sequences(
    router: &LoadBalancerRouter,
    targets: [RoutingTargetKey; 2],
    candidate_ids: [&[&str]; 2],
    expected: [&[&str]; 2],
) {
    assert_eq!(expected[0].len(), expected[1].len());
    let requests = [
        request(&targets[0], None, None),
        request(&targets[1], None, None),
    ];
    let candidates =
        candidate_ids.map(|ids| ids.iter().map(|id| candidate(id, 1024)).collect::<Vec<_>>());
    let states = [
        LoadBalancerTargetState::default(),
        LoadBalancerTargetState::default(),
    ];

    for (expected_a, expected_b) in expected[0].iter().zip(expected[1]) {
        for (index, expected_cluster_id) in [expected_a, expected_b].into_iter().enumerate() {
            assert_eq!(
                choose_from_router(router, &states[index], &requests[index], &candidates[index])
                    .candidate
                    .cluster_id,
                *expected_cluster_id,
            );
        }
    }
}

fn append_test_tagged_bytes(bytes: &mut Vec<u8>, tag: &[u8], value: &[u8]) {
    bytes.extend_from_slice(tag);
    bytes.push(0xff);
    bytes.extend_from_slice(&(value.len() as u64).to_le_bytes());
    bytes.extend_from_slice(value);
}

fn assert_json_rejected<T>(raw: &str, expected: &str)
where
    T: serde::de::DeserializeOwned,
{
    let error = serde_json::from_str::<T>(raw)
        .err()
        .expect("invalid config should be rejected");
    assert!(
        error.to_string().contains(expected),
        "expected {expected} in parse error, got: {error}"
    );
}

fn assert_algorithm_overrides(raw: impl Fn(LoadBalancerAlgorithm) -> String) {
    for algorithm in [
        LoadBalancerAlgorithm::GroqMultiregion,
        LoadBalancerAlgorithm::PowerOfTwo,
        LoadBalancerAlgorithm::Pulsar,
        LoadBalancerAlgorithm::PulsarMultiregion,
        LoadBalancerAlgorithm::Random,
        LoadBalancerAlgorithm::RoundRobin,
    ] {
        let raw = raw(algorithm);
        let parsed = LoadBalancerAlgorithmOverride::parse(&raw).unwrap();
        assert_eq!(
            (parsed.requested_algorithm(), parsed.algorithm()),
            (raw.as_str(), algorithm)
        );
    }
}

#[test]
fn simple_model_config_parses_to_algorithm_enum() {
    let config: LoadBalancerConfig =
        parse_json(r#"{"default":"groq-multiregion","models":{"model-a":"round-robin"}}"#);

    assert_eq!(config.default, LoadBalancerAlgorithm::GroqMultiregion);
    assert!(matches!(
        config.models.get("model-a"),
        Some(LoadBalancerModelConfig::Name(
            LoadBalancerAlgorithm::RoundRobin
        ))
    ));
}

#[test]
fn detailed_model_config_parses_input_work_admission_limit() {
    let config: LoadBalancerConfig = parse_json(
        r#"{"models":{"model-a":{"algorithm":"power-of-two","max_input_work_seconds":2.5}}}"#,
    );

    let detailed = config
        .models
        .get("model-a")
        .cloned()
        .expect("model config should exist")
        .into_algorithm_config();
    assert_eq!(detailed.max_input_work_seconds, Some(2.5));
}

#[test]
fn input_work_seconds_uses_pool_work_and_service_rate() {
    let cluster_a = work_candidate("cluster-a", 5, 100.0, 300);
    let cluster_b = work_candidate("cluster-b", 5, 50.0, 120);
    let seconds = input_work_seconds(&[cluster_a, cluster_b], 30, None);
    assert_eq!(seconds, Some(3.0));
}

#[test]
fn input_work_seconds_excludes_failed_clusters_and_requires_valid_capacity() {
    let excluded = work_candidate("excluded", 5, 100.0, 300);
    let invalid = work_candidate("invalid", 5, f64::NAN, 100);
    let excluded_ids = HashSet::from(["excluded".to_string()]);

    let seconds = input_work_seconds(&[excluded, invalid], 30, Some(&excluded_ids));
    assert_eq!(seconds, None);
}

#[test]
fn input_work_seconds_ignores_decode_only_total_query_input_size() {
    let decode_only =
        candidate("decode-only", 1024).with_stats(|stats| stats.total_query_input_size = 10_000);

    assert_eq!(input_work_seconds(&[decode_only], 50, None), Some(0.5));
}

#[test]
fn input_work_seconds_for_pulsar_includes_capacity_despite_low_free_kv() {
    let config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
    let target = target();
    let request = request(&target, Some("prefix-a"), Some(100));
    let free_kv = work_candidate_with_kv("free-kv", 256, 5, 100.0, 50);
    let likely_warm = work_candidate_with_kv("likely-warm", 50, 5, 1000.0, 900);

    let seconds = input_work_seconds_for_request(&config, &request, &[free_kv, likely_warm])
        .expect("valid PULSAR capacity should participate in admission");
    assert!((seconds - (1050.0 / 1100.0)).abs() < f64::EPSILON);
}

#[test]
fn input_work_seconds_for_pulsar_excludes_low_free_kv_when_considered() {
    let mut config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
    config.request_policy_mut().consider_kv_free_tokens = true;
    let target = target();
    let request = request(&target, Some("prefix-a"), Some(100));
    let free_kv = work_candidate_with_kv("free-kv", 256, 5, 100.0, 50);
    let low_free_kv = work_candidate_with_kv("low-free-kv", 50, 5, 1000.0, 900);

    let seconds = input_work_seconds_for_request(&config, &request, &[free_kv, low_free_kv])
        .expect("the candidate with sufficient KV tokens should provide capacity");
    assert!((seconds - 1.5).abs() < f64::EPSILON);
}

#[test]
fn invalid_algorithm_name_fails_during_parse() {
    assert_json_rejected::<LoadBalancerConfig>(r#"{"default":"not-a-real-lb"}"#, "not-a-real-lb");
}

#[test]
fn routing_algorithm_override_parses_canonical_algorithm_names_from_algorithm_parser() {
    assert_algorithm_overrides(|algorithm| {
        let raw = algorithm.to_string();
        assert_eq!(raw.parse::<LoadBalancerAlgorithm>(), Ok(algorithm));
        raw
    });
}

#[test]
fn routing_algorithm_override_parses_header_aliases_for_kebab_case_algorithm_names() {
    assert_algorithm_overrides(|algorithm| algorithm.to_string().replace('-', "_"));
}

#[test]
fn routing_algorithm_override_rejects_empty_and_unknown_names() {
    assert_eq!(
        LoadBalancerAlgorithmOverride::parse(""),
        Err(LoadBalancerRoutingAlgorithmError::Unknown { raw: String::new() })
    );
    assert_eq!(
        LoadBalancerAlgorithmOverride::parse("sticky"),
        Err(LoadBalancerRoutingAlgorithmError::Unknown {
            raw: "sticky".to_string()
        })
    );
}

#[test]
fn algorithm_specific_load_balancer_fields_are_rejected_for_other_algorithms() {
    for (raw, expected_field) in [
        (r#"{"algorithm":"round-robin","seed":"unused"}"#, "seed"),
        (
            r#"{"algorithm":"pulsar","max_queue_time_floor_ms":100}"#,
            "max_queue_time_floor_ms",
        ),
        (
            r#"{"algorithm":"groq-multiregion","consider_kv_free_tokens":true}"#,
            "consider_kv_free_tokens",
        ),
    ] {
        assert_json_rejected::<LoadBalancerAlgorithmConfig>(raw, expected_field);
    }
}

#[test]
fn detailed_algorithm_configs_preserve_all_variant_identities() {
    use LoadBalancerAlgorithm::*;

    for (raw, expected, expected_seed, considers_kv_free_tokens) in [
        (r#"{"algorithm":"power-of-two"}"#, PowerOfTwo, None, false),
        (
            r#"{"algorithm":"groq-multiregion","seed":"groq-seed"}"#,
            GroqMultiregion,
            Some("groq-seed"),
            false,
        ),
        (r#"{"algorithm":"round-robin"}"#, RoundRobin, None, false),
        (r#"{"algorithm":"random"}"#, Random, None, false),
        (
            r#"{"algorithm":"pulsar","seed":"pulsar-seed","consider_kv_free_tokens":true}"#,
            Pulsar,
            Some("pulsar-seed"),
            true,
        ),
        (
            r#"{"algorithm":"pulsar-multiregion","seed":"hybrid-seed","consider_kv_free_tokens":true}"#,
            PulsarMultiregion,
            Some("hybrid-seed"),
            true,
        ),
    ] {
        let config: LoadBalancerAlgorithmConfig = parse_json(raw);
        assert_eq!(config.algorithm(), expected);
        assert_eq!(config.seed(), expected_seed);
        assert_eq!(config.considers_kv_free_tokens(), considers_kv_free_tokens);
    }
}

#[test]
fn unknown_load_balancer_config_fields_are_rejected() {
    assert_json_rejected::<LoadBalancerConfig>(
        r#"{"default":"power-of-two","unused_top_level_field":true,"models":{"model-a":{"algorithm":"pulsar","unused_model_field":123}}}"#,
        "unused_top_level_field",
    );
}

#[test]
fn bundled_benchmark_lb_configs_parse() {
    #[derive(serde::Deserialize)]
    struct BenchmarkManifest {
        algorithms: Vec<BenchmarkAlgorithm>,
    }

    #[derive(serde::Deserialize)]
    struct BenchmarkAlgorithm {
        name: String,
        config: serde_json::Value,
    }

    let benches_dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../benches");
    let entries = std::fs::read_dir(&benches_dir)
        .unwrap_or_else(|err| panic!("failed to read {}: {err}", benches_dir.display()));
    let mut checked = 0usize;

    for entry in entries {
        let entry = entry.expect("failed to read benchmark manifest directory entry");
        let path = entry.path();
        let extension = path.extension().and_then(|extension| extension.to_str());
        if !matches!(extension, Some("yaml" | "yml")) {
            continue;
        }

        let manifest_bytes = std::fs::read(&path)
            .unwrap_or_else(|err| panic!("failed to read {}: {err}", path.display()));
        let manifest = serde_yaml_ng::from_slice::<BenchmarkManifest>(&manifest_bytes)
            .unwrap_or_else(|err| panic!("failed to parse {}: {err}", path.display()));

        for algorithm in manifest.algorithms {
            serde_json::from_value::<LoadBalancerConfig>(algorithm.config).unwrap_or_else(|err| {
                panic!(
                    "{} algorithm {} has invalid load-balancer config: {err}",
                    path.display(),
                    algorithm.name
                )
            });
            checked += 1;
        }
    }

    assert!(checked > 0, "expected bundled benchmark LB configs");
}

#[test]
fn detailed_model_config_parses_for_pulsar() {
    let router = router_from_json(
        r#"{"default":"power-of-two","models":{"model-a":{"algorithm":"pulsar","seed":"seed-1","require_cache_affinity_key":true,"consider_kv_free_tokens":true}}}"#,
    );
    let model_config = router.algorithm_config("model-a");
    assert_eq!(model_config.algorithm(), LoadBalancerAlgorithm::Pulsar);
    assert_eq!(model_config.seed(), Some("seed-1"));
    assert!(model_config.requires_cache_affinity_key());
    assert!(model_config.requires_input_tokens());
    assert!(model_config.considers_kv_free_tokens());
}

#[test]
fn kv_free_token_consideration_is_rejected_for_non_pulsar_algorithms() {
    assert_json_rejected::<LoadBalancerConfig>(
        r#"{"models":{"model-a":{"algorithm":"round-robin","consider_kv_free_tokens":true}}}"#,
        "consider_kv_free_tokens",
    );
}

#[test]
fn detailed_model_config_parses_for_pulsar_multiregion() {
    let router = router_from_json(
        r#"{"default":"power-of-two","models":{"model-a":{"algorithm":"pulsar-multiregion","seed":"seed-1","require_cache_affinity_key":true,"require_input_tokens":true,"max_queue_time_floor_ms":100,"max_queue_time_ceil_ms":100,"ttft_bucket_size_ms":50,"n":2}}}"#,
    );
    let model_config = router.algorithm_config("model-a");
    assert_eq!(
        model_config.algorithm(),
        LoadBalancerAlgorithm::PulsarMultiregion
    );
    assert_eq!(model_config.seed(), Some("seed-1"));
    assert!(model_config.requires_cache_affinity_key());
    assert!(model_config.requires_input_tokens());
    let multiregion_settings = model_config
        .multiregion_settings()
        .expect("hybrid config should include multiregion settings");
    assert_eq!(multiregion_settings.max_queue_time_floor_ms, Some(100));
    assert_eq!(multiregion_settings.max_queue_time_ceil_ms, Some(100));
    assert_eq!(multiregion_settings.ttft_bucket_size_ms, Some(50));
    assert_eq!(multiregion_settings.n, Some(2));
}

#[test]
fn detailed_model_config_parses_groq_multiregion_cache_affinity() {
    let router = router_from_json(
        r#"{"default":"power-of-two","models":{"model-a":{"algorithm":"groq-multiregion","seed":"seed-1","require_cache_affinity_key":true,"cache_affinity_virtual_nodes":64,"cache_affinity_backend_selection_count":2}}}"#,
    );
    let model_config = router.algorithm_config("model-a");
    assert_eq!(
        model_config.algorithm(),
        LoadBalancerAlgorithm::GroqMultiregion
    );
    assert_eq!(model_config.seed(), Some("seed-1"));
    assert!(model_config.requires_cache_affinity_key());
    let multiregion_config = GroqMultiregionConfig::from_algorithm_config(model_config);
    assert_eq!(multiregion_config.cache_affinity_virtual_nodes, 64);
    assert_eq!(
        multiregion_config.cache_affinity_backend_selection_count,
        Some(2)
    );
}

#[test]
fn request_algorithms_parse_and_override_default_selection() {
    let router = router_from_json(
        r#"{"default":"power-of-two","request_algorithms":{"round-robin":"round-robin"}}"#,
    );
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();
    let first =
        choose_with_override(&router, &target_state, &request, &candidates, "round_robin").choice;
    let second =
        choose_with_override(&router, &target_state, &request, &candidates, "round_robin").choice;

    assert_eq!(selected_cluster_id(first, &candidates), "cluster-0");
    assert_eq!(selected_cluster_id(second, &candidates), "cluster-1");
}

#[test]
fn choose_candidate_returns_slice_index_for_selected_cluster() {
    let router = router_from_json(r#"{"default":"round-robin"}"#);
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();

    let first = router
        .choose_candidate(&target_state, &request, &candidates)
        .expect("candidate should be selected");
    let second = router
        .choose_candidate(&target_state, &request, &candidates)
        .expect("candidate should be selected");

    assert_eq!(first.candidate_index, 0);
    assert_eq!(first.rank_depth, 1);
    assert_eq!(candidates[first.candidate_index].cluster_id, "cluster-0");
    assert_eq!(second.candidate_index, 1);
    assert_eq!(candidates[second.candidate_index].cluster_id, "cluster-1");
}

#[test]
fn choose_candidate_with_resolution_preserves_algorithm_metadata() {
    let router = router_from_json(
        r#"{"default":"power-of-two","request_algorithms":{"round-robin":"round-robin"}}"#,
    );
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("round_robin")
        .expect("routing algorithm override should parse");
    let resolution = router
        .resolve_algorithm_override(&target.model_id, Some(&algorithm_override))
        .expect("routing method should be available");

    let selection = router
        .choose_candidate_with_algorithm_resolution(
            &target_state,
            &request,
            &candidates,
            &resolution,
        )
        .expect("candidate should be selected");

    assert_eq!(selection.choice.candidate_index, 0);
    assert_eq!(
        selection.effective_algorithm,
        LoadBalancerAlgorithm::RoundRobin
    );
    assert_eq!(
        selection.requested_algorithm.as_deref(),
        Some("round_robin")
    );
}

#[test]
fn model_request_algorithms_override_top_level_request_algorithms() {
    let router = router_from_json(
        r#"{"default":"power-of-two","request_algorithms":{"round-robin":"round-robin"},"models":{"model-a":{"algorithm":"power-of-two","request_algorithms":{"round-robin":{"algorithm":"round-robin","require_input_tokens":true}}}}}"#,
    );
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("round-robin")
        .expect("routing algorithm override should parse");

    let model_config = router
        .resolve_algorithm_override("model-a", Some(&algorithm_override))
        .expect("model routing method should be available");
    let default_config = router
        .resolve_algorithm_override("model-b", Some(&algorithm_override))
        .expect("default routing method should be available");

    assert!(model_config.config().requires_input_tokens());
    assert!(!default_config.config().requires_input_tokens());
}

#[test]
fn request_algorithm_key_must_match_configured_algorithm() {
    let config: LoadBalancerConfig =
        parse_json(r#"{"default":"power-of-two","request_algorithms":{"random":"round-robin"}}"#);

    let err = match LoadBalancerRouter::from_config(&config) {
        Ok(_) => panic!("mismatched request algorithm should fail"),
        Err(err) => err,
    };
    assert!(
        err.to_string().contains("request_algorithms key random"),
        "unexpected error: {err}"
    );
}

#[test]
fn groq_multiregion_config_resolves_internal_defaults() {
    let mut algorithm_config =
        LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::GroqMultiregion);
    let settings = algorithm_config
        .multiregion_settings_mut()
        .expect("Groq config should include multiregion settings");
    settings.cache_affinity_virtual_nodes = Some(0);
    settings.cache_affinity_backend_selection_count = Some(0);
    settings.max_queue_time_floor_ms = Some(100);
    settings.max_queue_time_ceil_ms = Some(300);
    settings.n = Some(0);
    settings.ignore_queue_time = Some(true);
    settings.ignore_input_processing_time = Some(true);
    let config = GroqMultiregionConfig::from_algorithm_config(&algorithm_config);

    assert_eq!(config.cache_affinity_virtual_nodes, 1);
    assert_eq!(config.cache_affinity_backend_selection_count, None);
    assert_eq!(config.ttft_bucket_size, Duration::from_millis(20));
    assert_eq!(config.next_bucket_unlock_factor, 0.25);
    assert_eq!(config.sample_count, 1);
    assert_eq!(config.max_queued, 0);
    assert!(config.ignore_queue_time);
    assert!(config.ignore_input_processing_time);

    let target = target();
    let request = request(&target, None, Some(0));
    assert_eq!(
        config
            .max_queue_time(&request)
            .expect("floor and ceil should enable queue SLO"),
        Duration::from_millis(300)
    );
}

#[test]
fn router_reports_groq_multiregion_algorithm_name() {
    let router = router_with_model(
        LoadBalancerAlgorithm::PowerOfTwo,
        "model-a",
        LoadBalancerModelConfig::Name(LoadBalancerAlgorithm::GroqMultiregion),
    );
    assert_eq!(router.algorithm_name("model-a"), "groq-multiregion");
}

#[test]
fn default_round_robin_uses_independent_sequences_per_model() {
    assert_independent_round_robin_sequences(
        &router_with_default(LoadBalancerAlgorithm::RoundRobin),
        [target_with_model("model-a"), target_with_model("model-b")],
        [
            &["model-a-0", "model-a-1"],
            &["model-b-0", "model-b-1", "model-b-2"],
        ],
        [
            &["model-a-0", "model-a-1", "model-a-0"],
            &["model-b-0", "model-b-1", "model-b-2"],
        ],
    );
}

#[test]
fn default_round_robin_uses_independent_sequences_per_routing_target() {
    assert_independent_round_robin_sequences(
        &router_with_default(LoadBalancerAlgorithm::RoundRobin),
        [
            target_with_routing_key("tenant-a", "shared-model"),
            target_with_routing_key("tenant-b", "shared-model"),
        ],
        [&["cluster-0", "cluster-1"], &["cluster-0", "cluster-1"]],
        [&["cluster-0", "cluster-1"], &["cluster-0", "cluster-1"]],
    );
}

#[test]
fn replacing_target_state_starts_fresh_round_robin_sequence() {
    let router = router_with_default(LoadBalancerAlgorithm::RoundRobin);
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();

    let first = choose_from_router(&router, &target_state, &request, &candidates);
    let second = choose_from_router(&router, &target_state, &request, &candidates);
    let replacement_target_state = LoadBalancerTargetState::default();
    let replacement_first =
        choose_from_router(&router, &replacement_target_state, &request, &candidates);

    assert_eq!(first.candidate.cluster_id, "cluster-0");
    assert_eq!(second.candidate.cluster_id, "cluster-1");
    assert_eq!(replacement_first.candidate.cluster_id, "cluster-0");
}

#[test]
fn target_state_distinguishes_independent_router_definitions() {
    let first_router = router_with_default(LoadBalancerAlgorithm::RoundRobin);
    let second_router = router_with_default(LoadBalancerAlgorithm::RoundRobin);
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();

    let first_router_first =
        choose_from_router(&first_router, &target_state, &request, &candidates);
    let second_router_first =
        choose_from_router(&second_router, &target_state, &request, &candidates);
    let first_router_second =
        choose_from_router(&first_router, &target_state, &request, &candidates);

    assert_eq!(first_router_first.candidate.cluster_id, "cluster-0");
    assert_eq!(second_router_first.candidate.cluster_id, "cluster-0");
    assert_eq!(first_router_second.candidate.cluster_id, "cluster-1");
}

#[test]
fn configured_round_robin_uses_independent_sequences_per_routing_target() {
    let router = router_with_model(
        LoadBalancerAlgorithm::PowerOfTwo,
        "shared-model",
        LoadBalancerModelConfig::Name(LoadBalancerAlgorithm::RoundRobin),
    );
    assert_independent_round_robin_sequences(
        &router,
        [
            target_with_routing_key("tenant-a", "shared-model"),
            target_with_routing_key("tenant-b", "shared-model"),
        ],
        [&["cluster-0", "cluster-1"], &["cluster-0", "cluster-1"]],
        [&["cluster-0", "cluster-1"], &["cluster-0", "cluster-1"]],
    );
}

#[test]
fn choose_with_no_candidates_does_not_cache_default_lb_for_target() {
    let router = router_with_default(LoadBalancerAlgorithm::RoundRobin);
    let target = target_with_model("unknown-model");
    let request = request(&target, None, None);
    let target_state = LoadBalancerTargetState::default();

    assert!(
        router
            .choose_for_test(&target_state, &request, &[])
            .is_none()
    );
    assert_eq!(target_state.instance_count(), 0);
}

#[test]
fn request_round_robin_override_uses_stable_per_target_sequence() {
    let router = router_with_options(
        LoadBalancerAlgorithm::PowerOfTwo,
        &[LoadBalancerAlgorithm::RoundRobin],
        None,
    );
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();
    let selected = (0..3)
        .map(|_| {
            selected_cluster_id(
                round_robin_override_choice(&router, &target_state, &request, &candidates),
                &candidates,
            )
            .to_string()
        })
        .collect::<Vec<_>>();

    assert_eq!(
        selected,
        ["cluster-0", "cluster-1", "cluster-0"].map(str::to_string)
    );
    assert_eq!(target_state.instance_count(), 1);
}

#[test]
fn configured_request_override_creates_target_local_balancer() {
    let router = router_with_options(
        LoadBalancerAlgorithm::RoundRobin,
        &[LoadBalancerAlgorithm::PowerOfTwo],
        None,
    );
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();
    let selection = choose_with_override(
        &router,
        &target_state,
        &request,
        &candidates,
        "power-of-two",
    );

    assert_eq!(
        selection.effective_algorithm,
        LoadBalancerAlgorithm::PowerOfTwo
    );
    assert_eq!(target_state.instance_count(), 1);
}

#[test]
fn matching_round_robin_override_reuses_configured_target_sequence() {
    let router = router_with_default(LoadBalancerAlgorithm::RoundRobin);
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();
    let without_header = choose_from_router(&router, &target_state, &request, &candidates)
        .candidate
        .cluster_id;
    let with_header = round_robin_override_choice(&router, &target_state, &request, &candidates);
    let without_header_again = choose_from_router(&router, &target_state, &request, &candidates)
        .candidate
        .cluster_id;

    assert_eq!(without_header, "cluster-0");
    assert_eq!(selected_cluster_id(with_header, &candidates), "cluster-1");
    assert_eq!(without_header_again, "cluster-0");
}

#[test]
fn request_round_robin_override_keeps_routing_targets_isolated() {
    let router = router_with_options(
        LoadBalancerAlgorithm::PowerOfTwo,
        &[LoadBalancerAlgorithm::RoundRobin],
        None,
    );
    let target_a = target_with_routing_key("rk-a", "shared-model");
    let target_b = target_with_routing_key("rk-b", "shared-model");
    let request_a = request(&target_a, None, None);
    let request_b = request(&target_b, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_a_state = LoadBalancerTargetState::default();
    let target_b_state = LoadBalancerTargetState::default();
    let first_a = round_robin_override_choice(&router, &target_a_state, &request_a, &candidates);
    let first_b = round_robin_override_choice(&router, &target_b_state, &request_b, &candidates);
    let second_a = round_robin_override_choice(&router, &target_a_state, &request_a, &candidates);
    let second_b = round_robin_override_choice(&router, &target_b_state, &request_b, &candidates);

    assert_eq!(selected_cluster_id(first_a, &candidates), "cluster-0");
    assert_eq!(selected_cluster_id(first_b, &candidates), "cluster-0");
    assert_eq!(selected_cluster_id(second_a, &candidates), "cluster-1");
    assert_eq!(selected_cluster_id(second_b, &candidates), "cluster-1");
}

#[test]
fn request_override_beats_configured_model_algorithm() {
    let router = router_with_options(
        LoadBalancerAlgorithm::PowerOfTwo,
        &[LoadBalancerAlgorithm::PowerOfTwo],
        Some((
            "shared-model",
            LoadBalancerModelConfig::Name(LoadBalancerAlgorithm::RoundRobin),
        )),
    );
    let target = target_with_model("shared-model");
    let request = request(&target, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();
    let selection = choose_with_override(
        &router,
        &target_state,
        &request,
        &candidates,
        "power_of_two",
    );

    assert_eq!(
        selection.effective_algorithm,
        LoadBalancerAlgorithm::PowerOfTwo
    );
}

#[test]
fn matching_request_override_reuses_configured_algorithm_config() {
    let mut round_robin_config =
        LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::RoundRobin);
    round_robin_config.request_policy_mut().require_input_tokens = true;
    let router = router_with_model(
        LoadBalancerAlgorithm::PowerOfTwo,
        "shared-model",
        LoadBalancerModelConfig::Detailed(Box::new(round_robin_config)),
    );
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("round-robin")
        .expect("routing algorithm override should parse");

    let config = router
        .resolve_algorithm_override("shared-model", Some(&algorithm_override))
        .expect("routing method should be available");

    assert_eq!(
        config.config().algorithm(),
        LoadBalancerAlgorithm::RoundRobin
    );
    assert!(config.config().requires_input_tokens());
}

#[test]
fn matching_model_algorithm_beats_top_level_request_config() {
    let mut pulsar_config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
    pulsar_config.request_policy_mut().require_input_tokens = true;
    let router = router_with_options(
        LoadBalancerAlgorithm::PowerOfTwo,
        &[LoadBalancerAlgorithm::Pulsar],
        Some((
            "shared-model",
            LoadBalancerModelConfig::Detailed(Box::new(pulsar_config)),
        )),
    );
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("pulsar")
        .expect("routing algorithm override should parse");

    let config = router
        .resolve_algorithm_override("shared-model", Some(&algorithm_override))
        .expect("routing algorithm should resolve");

    assert_eq!(config.config().algorithm(), LoadBalancerAlgorithm::Pulsar);
    assert!(config.config().requires_input_tokens());
}

#[test]
fn known_unavailable_request_override_returns_error() {
    let router = router_with_default(LoadBalancerAlgorithm::PowerOfTwo);
    let target = target_with_model("shared-model");
    let request = request(&target, None, None);
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("pulsar")
        .expect("routing algorithm override should parse");

    let error = router
        .choose_candidate_with_algorithm_override(
            &target_state,
            &request,
            &candidates,
            Some(&algorithm_override),
        )
        .expect_err("unconfigured routing method should fail");
    assert_eq!(
        error,
        LoadBalancerRoutingAlgorithmError::Unavailable {
            raw: "pulsar".to_string(),
            algorithm: LoadBalancerAlgorithm::Pulsar,
        }
    );
    assert!(
        router
            .resolve_algorithm_override("shared-model", Some(&algorithm_override))
            .is_err()
    );
}

#[test]
fn unknown_request_override_returns_error() {
    assert_eq!(
        LoadBalancerAlgorithmOverride::parse("sticky")
            .expect_err("unknown routing algorithm should fail"),
        LoadBalancerRoutingAlgorithmError::Unknown {
            raw: "sticky".to_string()
        }
    );
}

#[test]
fn request_excluded_clusters_are_not_selected() {
    let router = router_with_default(LoadBalancerAlgorithm::RoundRobin);
    let target = target_with_model("model-exclusions");
    let failed_clusters = HashSet::from(["cluster-0".to_string()]);
    let request = LoadBalancerRequest {
        excluded_cluster_ids: Some(&failed_clusters),
        ..request(&target, None, None)
    };
    let candidates = candidates(&["cluster-0", "cluster-1"]);
    let target_state = LoadBalancerTargetState::default();

    let chosen = choose_from_router(&router, &target_state, &request, &candidates);

    assert_eq!(chosen.candidate.cluster_id, "cluster-1");
}

groq_choice_tests! {
    groq_multiregion_prefers_lower_estimated_ttft:
    |_| {};
    |target| request(target, None, Some(10));
    [
        candidate("fast", 1024).with_rtt_ms(5),
        candidate("slow", 1024).with_rtt_ms(50),
    ];
    1 => "fast";
}

#[test]
fn groq_multiregion_single_excluded_cluster_is_not_selected() {
    let lb = groq_load_balancer(|_| {});
    let target = target();
    let excluded = HashSet::from(["fast-but-excluded".to_string()]);
    let request = LoadBalancerRequest {
        excluded_cluster_ids: Some(&excluded),
        ..request(&target, None, Some(10))
    };
    let fast = candidate("fast-but-excluded", 1024).with_rtt_ms(5);
    let slow = candidate("slow-but-eligible", 1024).with_rtt_ms(50);

    let chosen = choose(lb.as_ref(), &request, &[fast, slow]);
    assert_eq!(chosen.candidate.cluster_id, "slow-but-eligible");
}

#[test]
fn groq_multiregion_cache_affinity_key_selects_stable_primary() {
    let lb = create_load_balancer_with_config(&groq_affinity_algorithm_config(8, 1, None))
        .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, Some("prefix-a"), Some(1));
    let candidates = candidates(&["affinity-a", "affinity-b", "affinity-c"]);

    let first = choose(lb.as_ref(), &request, &candidates)
        .candidate
        .cluster_id;
    assert_repeated_choice(lb.as_ref(), &request, &candidates, 16, &first);
}

#[test]
fn groq_multiregion_cache_affinity_retry_skips_excluded_primary() {
    assert_cache_affinity_retry(&["excluded-primary"]);
}

#[test]
fn groq_multiregion_cache_affinity_retry_returns_candidate_slice_indices() {
    let (config, target, candidates, excluded, key) = affinity_retry_fixture(&["excluded-primary"]);
    let request = request(&target, Some(&key), Some(1));
    let primary = cache_affinity_candidate_indices(&config, &request, &candidates)
        .expect("cache affinity should select a backend");
    assert_eq!(candidates[primary[0]].cluster_id, "excluded-primary");
    assert_eq!(
        cache_affinity_candidate_indices(
            &config,
            &LoadBalancerRequest {
                excluded_cluster_ids: Some(&excluded),
                ..request
            },
            &candidates,
        ),
        Some(vec![1]),
    );
}

#[test]
fn groq_multiregion_cache_affinity_retry_skips_multiple_excluded_primaries() {
    assert_cache_affinity_retry(&["excluded-a", "excluded-b"]);
}

#[test]
fn groq_multiregion_affinity_cache_invalidates_when_candidates_change() {
    let config = groq_affinity_config(8, 1, None);
    let lb = GroqMultiregionLoadBalancer::new(config.clone());
    let target = target();
    let first_candidates = candidates(&["old-a", "old-b", "old-c"]);

    for idx in 0..512 {
        let key = format!("prefix-{idx}");
        let request = request(&target, Some(&key), Some(1));
        let selected = cache_affinity_candidates(&config, &request, &first_candidates)
            .expect("cache affinity should select a backend");
        if selected[0].cluster_id == "old-a" {
            continue;
        }

        choose(&lb, &request, &first_candidates);
        let replacement = candidates(&["replacement"]);
        let chosen = choose(&lb, &request, &replacement);
        assert_eq!(chosen.candidate.cluster_id, "replacement");
        return;
    }

    panic!("expected to find an affinity key that selects a non-zero candidate index");
}

#[test]
fn groq_multiregion_does_not_cache_oversized_affinity_key() {
    let config = groq_affinity_config(8, 1, None);
    let lb = GroqMultiregionLoadBalancer::new(config);
    let target = target();
    let oversized_key = "x".repeat(MAX_CACHE_AFFINITY_CACHE_KEY_BYTES + 1);
    let request = request(&target, Some(&oversized_key), Some(1));
    let candidates = candidates(&["large-key-a", "large-key-b"]);

    let choice = choose(&lb, &request, &candidates);

    assert!(choice.candidate.cluster_id.starts_with("large-key-"));
    assert_eq!(lb.cached_affinity_key_bytes(), 0);
}

#[test]
fn groq_multiregion_cache_affinity_hash_uses_cluster_identity() {
    let config = groq_affinity_config(8, 1, None);
    let target = target();
    let request = request(&target, Some("prefix-a"), Some(1));
    let candidate = candidate("inst-a", 1024);

    let hash = cache_affinity_virtual_node_hash(&config, &request, &candidate, 7);

    let mut bytes = Vec::new();
    bytes.push(1);
    append_test_tagged_bytes(&mut bytes, b"seed", b"seed-1");
    append_test_tagged_bytes(&mut bytes, b"routing_key", b"rk-1");
    append_test_tagged_bytes(&mut bytes, b"model_id", b"model-a");
    append_test_tagged_bytes(&mut bytes, b"cluster_id", b"inst-a");
    append_test_tagged_bytes(&mut bytes, b"virtual_node", &7usize.to_le_bytes());

    assert_eq!(hash, xxh3_64(&bytes));
}

#[test]
fn groq_multiregion_cache_affinity_hash_changes_with_routing_key() {
    let config = groq_affinity_config(8, 1, None);
    let target_a = target_with_routing_key("tenant-a", "shared-model");
    let target_b = target_with_routing_key("tenant-b", "shared-model");
    let request_a = request(&target_a, Some("same-prefix"), Some(1));
    let request_b = request(&target_b, Some("same-prefix"), Some(1));
    let candidate = candidate("inst-a", 1024);

    let hash_a = cache_affinity_virtual_node_hash(&config, &request_a, &candidate, 7);
    let hash_b = cache_affinity_virtual_node_hash(&config, &request_b, &candidate, 7);

    assert_ne!(
        hash_a, hash_b,
        "routing_key must be part of the affinity hash namespace"
    );
}

#[test]
fn groq_multiregion_cache_affinity_falls_back_when_primary_is_full() {
    let lb = create_load_balancer_with_config(&groq_affinity_algorithm_config(1, 1, Some(3)))
        .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, Some("prefix-a"), Some(1));
    let mut candidates = candidates(&["fallback-a", "fallback-b", "fallback-c"]);
    let primary_config = groq_affinity_config(1, 1, None);
    let primary = cache_affinity_candidates(&primary_config, &request, &candidates)
        .expect("cache affinity should select a primary")[0]
        .cluster_id
        .clone();
    for candidate in &mut candidates {
        if candidate.cluster_id == primary {
            candidate.stats.max_engine_concurrency = 1;
            candidate.stats.num_running_queries = 1;
        }
    }

    let chosen = choose(lb.as_ref(), &request, &candidates);
    assert_ne!(chosen.candidate.cluster_id, primary);
}

#[test]
fn groq_multiregion_two_affinity_candidates_still_filter_capacity() {
    let config = groq_affinity_config(8, 2, Some(2));
    let lb = GroqMultiregionLoadBalancer::new(config.clone());
    let target = target();
    let request = request(&target, Some("prefix-a"), Some(1));
    let mut candidates = candidates(&["two-affinity-a", "two-affinity-b", "two-affinity-c"]);
    let selected = cache_affinity_candidates(&config, &request, &candidates)
        .expect("cache affinity should select candidates");
    assert_eq!(selected.len(), 2);
    let full_primary_id = selected[0].cluster_id.clone();
    let available_selected_id = selected[1].cluster_id.clone();
    for candidate in &mut candidates {
        if candidate.cluster_id == full_primary_id {
            candidate.stats.max_engine_concurrency = 1;
            candidate.stats.num_running_queries = 1;
        }
    }

    let chosen = choose(&lb, &request, &candidates);

    assert_eq!(chosen.candidate.cluster_id, available_selected_id);
}

#[test]
fn groq_multiregion_cache_affinity_keys_distribute_across_backends() {
    let config = groq_affinity_config(32, 1, None);
    let target = target();
    let candidates = candidates(&["dist-a", "dist-b", "dist-c"]);
    let mut seen = HashSet::new();

    for idx in 0..128 {
        let key = format!("prefix-{idx}");
        let request = request(&target, Some(&key), Some(1));
        let selected = cache_affinity_candidates(&config, &request, &candidates)
            .expect("cache affinity should select a backend");
        seen.insert(selected[0].cluster_id.clone());
        if seen.len() >= 2 {
            break;
        }
    }

    assert!(
        seen.len() >= 2,
        "expected different cache-affinity keys to reach multiple primaries, saw {seen:?}"
    );
}

#[test]
fn groq_multiregion_cache_affinity_is_skipped_without_header() {
    let lb = create_load_balancer_with_config(&groq_affinity_algorithm_config(1, 1, Some(3)))
        .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(1));
    let candidates = [
        candidate("affinity-primary", 1024).with_rtt_ms(50),
        candidate("fastest-without-affinity", 1024).with_rtt_ms(5),
    ];
    assert_repeated_choice(
        lb.as_ref(),
        &request,
        &candidates,
        16,
        "fastest-without-affinity",
    );
}

groq_choice_tests! {
    groq_multiregion_uses_input_tokens_in_ttft_estimate:
    |_| {};
    |target| request(target, None, Some(100));
    [
        work_candidate("higher-rtt-higher-cap", 10, 200.0, 0),
        work_candidate("lower-rtt-lower-cap", 1, 10.0, 0),
    ];
    1 => "higher-rtt-higher-cap";

    groq_multiregion_can_ignore_input_processing_time_in_ttft_estimate:
    |settings| settings.ignore_input_processing_time = Some(true);
    |target| request(target, None, Some(100));
    [
        work_candidate("lower-rtt-lower-cap", 1, 10.0, 0),
        work_candidate("higher-rtt-higher-cap", 50, 200.0, 0),
    ];
    1 => "lower-rtt-lower-cap";
}

#[test]
fn groq_multiregion_limits_selection_to_first_ttft_bucket() {
    let lb = groq_load_balancer(|_| {});
    let target = target();
    let request = request(&target, None, Some(1));
    let candidates = [
        work_candidate("bucket-a", 5, 10.0, 0),
        work_candidate("bucket-b", 20, 10.0, 0),
        work_candidate("second-bucket", 50, 10.0, 0),
    ];

    for _ in 0..32 {
        let chosen = choose(lb.as_ref(), &request, &candidates);
        assert_ne!(chosen.candidate.cluster_id, "second-bucket");
    }
}

groq_choice_tests! {
    groq_multiregion_can_ignore_queue_time_in_ttft_estimate:
    |settings| settings.ignore_queue_time = Some(true);
    |target| request(target, None, Some(0));
    [
        work_candidate("lower-rtt-higher-queue", 5, 100.0, 100),
        work_candidate("higher-rtt-lower-queue", 50, 100.0, 0),
    ];
    1 => "lower-rtt-higher-queue";

    groq_multiregion_ignore_queue_still_compares_sampled_candidates_by_queue_time:
    |settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
    };
    |target| request(target, None, Some(512));
    [
        work_candidate("higher-queue", 5, 100.0, 2),
        work_candidate("lower-queue", 5, 100.0, 1),
    ];
    16 => "lower-queue";

    groq_multiregion_ignore_queue_keeps_later_prefill_buckets_locked:
    |settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
    };
    |target| request(target, None, Some(512));
    [
        work_candidate("first-bucket", 5, 10_000.0, 0),
        work_candidate("later-bucket", 50, 10_000.0, 0),
    ];
    16 => "first-bucket";
}

#[test]
fn groq_multiregion_ignore_queue_skips_single_excluded_backend() {
    assert_excluded_queue_choice(false, &["excluded"]);
}

#[test]
fn groq_multiregion_ignore_queue_skips_multiple_excluded_backends() {
    assert_excluded_queue_choice(false, &["excluded-a", "excluded-b"]);
}

groq_choice_tests! {
    groq_multiregion_deprioritizes_non_finite_ttft_candidates:
    |_| {};
    |target| request(target, None, Some(10));
    [
        candidate("finite", 1024),
        work_candidate("non-finite", 1, 0.0, 5),
    ];
    1 => "finite";

    groq_multiregion_uses_last_mean_input_tps_for_prefill_estimates:
    |_| {};
    |target| request(target, None, Some(100));
    [
        work_candidate("high-capacity", 10, 200.0, 0),
        work_candidate("low-capacity", 1, 10.0, 0),
    ];
    1 => "high-capacity";
}

#[test]
fn groq_multiregion_unlocks_later_ttft_bucket_after_waiting() {
    let lb = groq_load_balancer(|settings| {
        settings.n = Some(1);
    });
    let target = target();
    let request = LoadBalancerRequest {
        received_at: Instant::now() - Duration::from_millis(20),
        ..request(&target, None, Some(1))
    };
    let fast_full = concurrency_candidate("fast-full", 20, 1, 1);
    let slower_available = concurrency_candidate("slower-available", 60, 1, 0);

    let chosen = choose(lb.as_ref(), &request, &[fast_full, slower_available]);
    assert_eq!(chosen.candidate.cluster_id, "slower-available");
}

groq_choice_tests! {
    groq_multiregion_filters_full_backends:
    |_| {};
    |target| request(target, None, Some(1));
    [
        concurrency_candidate("full", 5, 1, 1),
        concurrency_candidate("available", 6, 1, 0),
    ];
    1 => "available";

    groq_multiregion_filters_backends_over_queue_slo:
    |settings| {
        settings.max_queue_time_floor_ms = Some(5);
        settings.max_queue_time_ceil_ms = Some(5);
        settings.n = Some(2);
    };
    |target| request(target, None, Some(0));
    [
        work_candidate("over-slo", 5, 100.0, 1),
        work_candidate("under-slo", 5, 100.0, 0),
    ];
    1 => "under-slo";

    groq_multiregion_filters_queue_slo_before_ttft_bucket_locking:
    |settings| {
        settings.max_queue_time_floor_ms = Some(5);
        settings.max_queue_time_ceil_ms = Some(5);
        settings.n = Some(2);
    };
    |target| request(target, None, Some(0));
    [
        work_candidate("over-slo-first-bucket", 1, 100.0, 1),
        work_candidate("under-slo-later-bucket", 40, 100.0, 0),
    ];
    1 => "under-slo-later-bucket";

    groq_multiregion_queue_slo_still_applies_when_queue_time_is_ignored_for_ttft:
    |settings| {
        settings.ignore_queue_time = Some(true);
        settings.max_queue_time_floor_ms = Some(5);
        settings.max_queue_time_ceil_ms = Some(5);
        settings.n = Some(2);
    };
    |target| request(target, None, Some(0));
    [
        work_candidate("over-slo-lower-rtt", 5, 100.0, 1),
        work_candidate("under-slo-higher-rtt", 6, 100.0, 0),
    ];
    1 => "under-slo-higher-rtt";
}

#[test]
fn groq_multiregion_returns_none_when_only_candidate_exceeds_queue_slo() {
    let lb = groq_load_balancer(|settings| {
        settings.max_queue_time_floor_ms = Some(5);
        settings.max_queue_time_ceil_ms = Some(5);
    });
    let target = target();
    let request = request(&target, None, Some(0));
    let over_slo = candidate("over-slo", 1024).with_stats(|stats| stats.queued_input_size = 1);

    let choice = lb.choose_for_test(&request, &[over_slo]);
    assert!(choice.is_none());
}

#[test]
fn max_queue_time_interpolates_between_floor_and_ceil() {
    let max_queue_time = max_queue_time(
        100,
        300,
        Duration::from_millis(500),
        Some(Duration::from_millis(1000)),
    );
    assert!(
        (200..=205).contains(&max_queue_time.as_millis()),
        "expected roughly 200ms max queue time, got {max_queue_time:?}"
    );
}

#[test]
fn max_queue_time_uses_ceil_when_request_slo_is_missing() {
    assert_eq!(
        max_queue_time(100, 300, Duration::ZERO, None),
        Duration::from_millis(300)
    );
}

#[test]
fn max_queue_time_uses_ceil_when_request_slo_is_zero() {
    assert_eq!(
        max_queue_time(100, 300, Duration::ZERO, Some(Duration::ZERO)),
        Duration::from_millis(300)
    );
}

#[test]
fn equal_floor_and_ceil_configures_fixed_max_queue_time() {
    assert_eq!(
        max_queue_time(75, 75, Duration::ZERO, None),
        Duration::from_millis(75)
    );
}

groq_choice_tests! {
    groq_multiregion_compares_by_queue_time_within_unlocked_bucket:
    |settings| settings.n = Some(2);
    |target| request(target, None, Some(1));
    [
        work_candidate("higher-queue", 5, 100.0, 2),
        work_candidate("lower-queue", 5, 100.0, 1),
    ];
    16 => "lower-queue";

    groq_multiregion_rtt_only_still_compares_sampled_candidates_by_queue_time:
    |settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
        settings.ignore_input_processing_time = Some(true);
    };
    |target| request(target, None, Some(512));
    [
        work_candidate("higher-queue", 5, 100.0, 2),
        work_candidate("lower-queue", 5, 100.0, 1),
    ];
    16 => "lower-queue";

    groq_multiregion_rtt_only_filters_full_backends_before_sampling:
    |settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
        settings.ignore_input_processing_time = Some(true);
    };
    |target| request(target, None, Some(512));
    [
        concurrency_candidate("full", 5, 1, 1),
        concurrency_candidate("available", 5, 1, 0),
    ];
    1 => "available";

    groq_multiregion_rtt_only_keeps_later_rtt_buckets_locked:
    |settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
        settings.ignore_input_processing_time = Some(true);
    };
    |target| request(target, None, Some(512));
    [
        candidate("first-bucket", 1024).with_rtt_ms(5),
        candidate("later-bucket", 1024).with_rtt_ms(50),
    ];
    16 => "first-bucket";
}

#[test]
fn groq_multiregion_rtt_only_skips_single_excluded_backend() {
    assert_excluded_queue_choice(true, &["excluded"]);
}

#[test]
fn groq_multiregion_rtt_only_skips_multiple_excluded_backends() {
    assert_excluded_queue_choice(true, &["excluded-a", "excluded-b"]);
}

groq_choice_tests! {
    groq_multiregion_uses_priority_queue_time_estimate:
    |settings| settings.n = Some(2);
    |target| request_with_priority(target, None, Some(0), 4);
    [
        priority_candidate("aggregate-lower-priority-higher", 4, 50),
        priority_candidate("aggregate-higher-priority-lower", 4, 5)
            .with_stats(|stats| stats.queued_input_size = 100),
    ];
    16 => "aggregate-higher-priority-lower";
}

#[test]
fn groq_multiregion_ttft_estimator_uses_priority_queue_and_ignore_flags() {
    let mut candidate = work_candidate("estimated", 7, 100.0, 999);
    candidate.stats.queue_time_estimate_ms_by_priority = HashMap::from([(4, 25)]);

    let full = groq_multiregion_ttft_components(&candidate, Some(200), 4, false, false);
    assert_eq!(full.queue_ms, 25.0);
    assert_eq!(full.ttft_ms, 2032.0);

    let ignore_queue = groq_multiregion_ttft_components(&candidate, Some(200), 4, true, false);
    assert_eq!(ignore_queue.queue_ms, 25.0);
    assert_eq!(ignore_queue.ttft_ms, 2007.0);

    let ignore_prefill = groq_multiregion_ttft_components(&candidate, Some(200), 4, false, true);
    assert_eq!(ignore_prefill.queue_ms, 25.0);
    assert_eq!(ignore_prefill.ttft_ms, 32.0);
}

groq_choice_tests! {
    groq_multiregion_clamps_priority_to_max_known_queue_time_priority:
    |settings| settings.n = Some(2);
    |target| request_with_priority(target, None, Some(0), 10);
    [
        priority_candidate("lower-clamped-queue", 2, 5),
        priority_candidate("higher-clamped-queue", 2, 50),
    ];
    16 => "lower-clamped-queue";

    groq_multiregion_uses_next_highest_priority_queue_time_estimate:
    |settings| settings.n = Some(2);
    |target| request_with_priority(target, None, Some(0), 3);
    [
        priority_candidate("higher-queue", 2, 50),
        priority_candidate("lower-queue", 2, 5),
    ];
    16 => "lower-queue";

    groq_multiregion_treats_lower_priority_only_queue_as_zero_for_higher_priority_request:
    |settings| settings.n = Some(2);
    |target| request_with_priority(target, None, Some(0), 0);
    [
        priority_candidate("sparse-lower-priority-only", 4, 0)
            .with_stats(|stats| stats.queued_input_size = 100),
        candidate("aggregate-lower-queue", 1024).with_stats(|stats| stats.queued_input_size = 1),
    ];
    16 => "sparse-lower-priority-only";
}

#[test]
fn pulsar_different_affinity_keys_reach_multiple_backends() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));
    let target = target();
    let candidates = candidates(&["inst-a", "inst-b", "inst-c"]);

    let mut seen = std::collections::HashSet::new();
    for idx in 0..128 {
        let key = format!("affinity-{idx}");
        let choice = choose(
            &pulsar,
            &request(&target, Some(&key), Some(128)),
            &candidates,
        );
        seen.insert(choice.candidate.cluster_id);
        if seen.len() >= 2 {
            break;
        }
    }

    assert!(
        seen.len() >= 2,
        "expected at least two different backends across affinity keys, saw {seen:?}"
    );
}

#[test]
fn pulsar_ranking_returns_candidate_slice_indices() {
    let config = seeded_pulsar_algorithm_config("seed-1");
    let target = target();
    let request = request(&target, Some("ranked-prefix"), Some(128));
    let candidates = vec![
        work_candidate("invalid", 5, 0.0, 0),
        work_candidate("slow", 5, 1.0, 0),
        work_candidate("fast", 5, 10.0, 0),
    ];

    let ranking = pulsar_ranked_indices(config.seed(), &request, &candidates);

    assert_eq!(ranking.len(), 2);
    assert_eq!(
        ranking.iter().copied().collect::<HashSet<_>>(),
        HashSet::from([1, 2])
    );
}

#[test]
fn pulsar_keeps_ranked_primary_despite_low_free_kv() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));
    let target = target();

    for idx in 0..512 {
        let key = format!("affinity-{idx}");
        let request = request(&target, Some(&key), Some(128));
        let feasible_candidates = candidates(&["inst-a", "inst-b"]);
        let baseline = choose(&pulsar, &request, &feasible_candidates);
        if baseline.candidate.cluster_id != "inst-a" {
            continue;
        }

        let constrained_candidates = vec![candidate("inst-a", 64), candidate("inst-b", 1024)];
        let constrained = choose(&pulsar, &request, &constrained_candidates);
        assert_eq!(constrained.candidate.cluster_id, "inst-a");
        assert_eq!(constrained.rank_depth, 1);
        return;
    }

    panic!("expected to find an affinity key that ranks inst-a first");
}

#[test]
fn pulsar_considering_kv_free_tokens_skips_ranked_primary_and_missing_metrics() {
    let pulsar = PulsarLoadBalancer::new(kv_aware_pulsar_algorithm_config("seed-1"));
    let target = target();
    let request = request(&target, Some("kv-aware-prefix"), Some(128));
    let full = vec![candidate("inst-a", 1024), candidate("inst-b", 1024)];
    let ranking = pulsar.compute_ranking(&request, &full);
    let primary_index = ranking[0];
    let fallback_index = ranking[1];

    let mut low_free = full.clone();
    low_free[primary_index] = candidate(&low_free[primary_index].cluster_id, 64);
    let low_free_choice = choose(&pulsar, &request, &low_free);
    assert_eq!(
        low_free_choice.candidate.cluster_id,
        full[fallback_index].cluster_id
    );
    assert_eq!(low_free_choice.rank_depth, 2);
    assert!(low_free_choice.selected_after_kv_free_tokens_skip);

    let mut missing_metrics = full;
    missing_metrics[primary_index]
        .stats
        .kv_cache_capacity_tokens = 0;
    missing_metrics[primary_index].stats.kv_cache_used_tokens = 0;
    missing_metrics[primary_index].stats.kv_cache_free_tokens = 0;
    let missing_metrics_choice = choose(&pulsar, &request, &missing_metrics);
    assert_eq!(
        missing_metrics_choice.candidate.cluster_id,
        missing_metrics[fallback_index].cluster_id
    );
    assert!(missing_metrics_choice.selected_after_kv_free_tokens_skip);
}

#[test]
fn pulsar_cached_ranking_rechecks_live_kv_free_tokens() {
    let pulsar = PulsarLoadBalancer::new(kv_aware_pulsar_algorithm_config("seed-1"));
    let target = target();
    let request = request(&target, Some("cached-kv-prefix"), Some(128));
    let candidates = candidates(&["inst-a", "inst-b"]);
    let primary = choose(&pulsar, &request, &candidates);

    let mut changed = candidates;
    let primary_candidate = changed
        .iter_mut()
        .find(|candidate| candidate.cluster_id == primary.candidate.cluster_id)
        .expect("selected primary should be in candidate list");
    primary_candidate.stats.kv_cache_used_tokens = 960;
    primary_candidate.stats.kv_cache_free_tokens = 64;

    let fallback = choose(&pulsar, &request, &changed);
    assert_ne!(fallback.candidate.cluster_id, primary.candidate.cluster_id);
    assert!(fallback.selected_after_kv_free_tokens_skip);
}

#[test]
fn pulsar_cached_retry_skips_single_excluded_primary() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));
    let target = target();
    let candidates = candidates(&["retry-primary", "retry-fallback", "retry-second-fallback"]);
    let base_request = request(&target, Some("retry-prefix"), Some(128));
    let primary = choose(&pulsar, &base_request, &candidates)
        .candidate
        .cluster_id;
    let excluded = HashSet::from([primary.clone()]);
    let retry_request = LoadBalancerRequest {
        excluded_cluster_ids: Some(&excluded),
        ..base_request
    };

    let retry = choose(&pulsar, &retry_request, &candidates);

    assert_ne!(retry.candidate.cluster_id, primary);
    assert!(retry.rank_depth > 1);
}

#[test]
fn pulsar_returns_none_when_all_candidates_are_excluded() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));
    let target = target();
    let candidates = candidates(&["all-excluded-a", "all-excluded-b"]);
    let excluded = candidates
        .iter()
        .map(|candidate| candidate.cluster_id.clone())
        .collect::<HashSet<_>>();
    let request = LoadBalancerRequest {
        excluded_cluster_ids: Some(&excluded),
        ..request(&target, Some("retry-prefix"), Some(128))
    };

    assert!(pulsar.choose_for_test(&request, &candidates).is_none());
}

#[test]
fn pulsar_ranking_cache_invalidates_when_capacity_weight_changes() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));
    let target = target();

    for idx in 0..1024 {
        let key = format!("affinity-{idx}");
        let request = request(&target, Some(&key), Some(128));
        let initial = vec![
            work_candidate("inst-a", 5, 10_000.0, 0),
            work_candidate("inst-b", 5, 1.0, 0),
        ];
        let changed = vec![
            work_candidate("inst-a", 5, 1.0, 0),
            work_candidate("inst-b", 5, 10_000.0, 0),
        ];

        let first = choose(&pulsar, &request, &initial).candidate.cluster_id;
        let second = choose(&pulsar, &request, &changed).candidate.cluster_id;
        if first != second {
            assert_eq!(first, "inst-a");
            assert_eq!(second, "inst-b");
            return;
        }
    }

    panic!("expected to find an affinity key whose ranking changes after capacity changes");
}

#[test]
fn pulsar_does_not_cache_oversized_affinity_key() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));
    let target = target();
    let oversized_key = "x".repeat(MAX_CACHE_AFFINITY_CACHE_KEY_BYTES + 1);
    let request = request(&target, Some(&oversized_key), Some(128));
    let candidates = candidates(&["large-key-a", "large-key-b"]);

    let choice = choose(&pulsar, &request, &candidates);

    assert!(choice.candidate.cluster_id.starts_with("large-key-"));
    assert_eq!(pulsar.cached_affinity_key_bytes(), 0);
}

#[test]
fn pulsar_hash_is_pinned_to_a_fixed_algorithm_and_version() {
    let hash = pulsar_hash64(
        Some("seed-1"),
        &Some("rk-1".to_string()),
        "model-a",
        Some("prefix-123"),
        "inst-a",
    );

    assert_eq!(hash, 0x1944_3c44_ec9f_8abf);
}

#[test]
fn pulsar_uses_last_mean_input_tps_as_weight() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));

    let candidate = work_candidate("inst-a", 5, 123.0, 0);

    assert_eq!(pulsar.weight(&candidate), Some(123.0));
}

#[test]
fn pulsar_excludes_candidate_with_invalid_last_mean_input_tps() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));

    let target = target();
    let invalid = work_candidate("inst-a", 5, 0.0, 0);
    let valid = candidate("inst-b", 1024);

    let choice = choose(
        &pulsar,
        &request(&target, Some("prefix-1"), Some(128)),
        &[invalid, valid],
    );
    assert_eq!(choice.candidate.cluster_id, "inst-b");
}

#[test]
fn pulsar_returns_none_when_all_candidates_lack_valid_last_mean_input_tps() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));

    let target = target();
    let invalid_a = work_candidate("inst-a", 5, 0.0, 0);
    let invalid_b = work_candidate("inst-b", 5, f64::NAN, 0);

    let choice = pulsar.choose_for_test(
        &request(&target, Some("prefix-1"), Some(128)),
        &[invalid_a, invalid_b],
    );
    assert!(choice.is_none());
}

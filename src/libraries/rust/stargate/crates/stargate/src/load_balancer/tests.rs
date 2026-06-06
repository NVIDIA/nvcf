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
use crate::load_balancer::algorithm::MAX_CACHE_AFFINITY_CACHE_KEY_BYTES;
use crate::load_balancer::groq_multiregion::{
    GroqMultiregionConfig, GroqMultiregionLoadBalancer, cache_affinity_candidates,
    cache_affinity_virtual_node_hash,
};
use crate::load_balancer::pulsar::{PulsarLoadBalancer, pulsar_hash64};
use crate::routing::{RoutedClusterSnapshot, RoutingTargetKey};
use xxhash_rust::xxh3::xxh3_64;

fn target_with_model(model_id: &str) -> RoutingTargetKey {
    RoutingTargetKey {
        routing_key: Some("rk-1".to_string()),
        model_id: model_id.to_string(),
    }
}

fn target_with_routing_key(routing_key: &str, model_id: &str) -> RoutingTargetKey {
    RoutingTargetKey {
        routing_key: Some(routing_key.to_string()),
        model_id: model_id.to_string(),
    }
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

fn multiregion_runtime_config(config: LoadBalancerAlgorithmConfig) -> GroqMultiregionConfig {
    GroqMultiregionConfig::from_algorithm_config(&config)
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

fn kv_aware_pulsar_algorithm_config(seed: &str) -> LoadBalancerAlgorithmConfig {
    let mut config = seeded_pulsar_algorithm_config(seed);
    config.request_policy_mut().consider_kv_free_tokens = true;
    config
}

fn candidate(id: &str, kv_cache_free_tokens: u64) -> RoutedClusterSnapshot {
    RoutedClusterSnapshot {
        cluster_id: id.to_string(),
        stats: ModelStats {
            output_tps: 0.0,
            last_mean_input_tps: 100.0,
            max_output_tps: 100.0,
            queue_size: 0,
            queued_input_size: 0,
            kv_cache_capacity_tokens: 1024,
            kv_cache_used_tokens: 1024 - kv_cache_free_tokens,
            kv_cache_free_tokens,
            num_running_queries: 0,
            max_engine_concurrency: 0,
            total_query_input_size: 0,
            queue_time_estimate_ms_by_priority: HashMap::new(),
            ..ModelStats::default()
        },
        rtt: Duration::from_millis(5),
        snapshot_updated_at: Instant::now(),
        status: InferenceServerStatus::Active,
        active_backend_count: 1,
    }
}

fn selected_cluster_id(
    choice: LoadBalancerCandidateChoice,
    candidates: &[RoutedClusterSnapshot],
) -> &str {
    &candidates[choice.candidate_index].cluster_id
}

fn request_algorithm_map(
    algorithms: &[LoadBalancerAlgorithm],
) -> HashMap<LoadBalancerAlgorithm, LoadBalancerModelConfig> {
    algorithms
        .iter()
        .copied()
        .map(|algorithm| (algorithm, LoadBalancerModelConfig::Name(algorithm)))
        .collect()
}

fn append_test_tagged_bytes(bytes: &mut Vec<u8>, tag: &[u8], value: &[u8]) {
    bytes.extend_from_slice(tag);
    bytes.push(0xff);
    bytes.extend_from_slice(&(value.len() as u64).to_le_bytes());
    bytes.extend_from_slice(value);
}

#[test]
fn simple_model_config_parses_to_algorithm_enum() {
    let config = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "groq-multiregion",
                "models": {
                    "model-a": "round-robin"
                }
            }"#,
    )
    .expect("config should parse");

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
    let config = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "models": {
                    "model-a": {
                        "algorithm": "power-of-two",
                        "max_input_work_seconds": 2.5
                    }
                }
            }"#,
    )
    .expect("config should parse");

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
    let mut cluster_a = candidate("cluster-a", 1024);
    cluster_a.stats.queued_input_size = 300;
    cluster_a.stats.last_mean_input_tps = 100.0;
    let mut cluster_b = candidate("cluster-b", 1024);
    cluster_b.stats.queued_input_size = 120;
    cluster_b.stats.last_mean_input_tps = 50.0;

    assert_eq!(
        input_work_seconds(&[cluster_a, cluster_b], 30, None),
        Some(3.0)
    );
}

#[test]
fn input_work_seconds_excludes_failed_clusters_and_requires_valid_capacity() {
    let mut excluded = candidate("excluded", 1024);
    excluded.stats.queued_input_size = 300;
    excluded.stats.last_mean_input_tps = 100.0;
    let mut invalid = candidate("invalid", 1024);
    invalid.stats.queued_input_size = 100;
    invalid.stats.last_mean_input_tps = f64::NAN;
    let excluded_ids = HashSet::from(["excluded".to_string()]);

    assert_eq!(
        input_work_seconds(&[excluded, invalid], 30, Some(&excluded_ids)),
        None
    );
}

#[test]
fn input_work_seconds_ignores_decode_only_total_query_input_size() {
    let mut decode_only = candidate("decode-only", 1024);
    decode_only.stats.total_query_input_size = 10_000;
    decode_only.stats.queued_input_size = 0;
    decode_only.stats.last_mean_input_tps = 100.0;

    assert_eq!(input_work_seconds(&[decode_only], 50, None), Some(0.5));
}

#[test]
fn input_work_seconds_for_pulsar_includes_capacity_despite_low_free_kv() {
    let config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
    let target = target();
    let request = request(&target, Some("prefix-a"), Some(100));
    let mut free_kv = candidate("free-kv", 256);
    free_kv.stats.queued_input_size = 50;
    free_kv.stats.last_mean_input_tps = 100.0;
    let mut likely_warm = candidate("likely-warm", 50);
    likely_warm.stats.queued_input_size = 900;
    likely_warm.stats.last_mean_input_tps = 1000.0;

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
    let mut free_kv = candidate("free-kv", 256);
    free_kv.stats.queued_input_size = 50;
    free_kv.stats.last_mean_input_tps = 100.0;
    let mut low_free_kv = candidate("low-free-kv", 50);
    low_free_kv.stats.queued_input_size = 900;
    low_free_kv.stats.last_mean_input_tps = 1000.0;

    let seconds = input_work_seconds_for_request(&config, &request, &[free_kv, low_free_kv])
        .expect("the candidate with sufficient KV tokens should provide capacity");
    assert!((seconds - 1.5).abs() < f64::EPSILON);
}

#[test]
fn invalid_algorithm_name_fails_during_parse() {
    let err = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "not-a-real-lb"
            }"#,
    )
    .expect_err("config parse should fail for invalid algorithm");

    assert!(
        err.to_string().contains("not-a-real-lb"),
        "unexpected parse error: {err}"
    );
}

#[test]
fn routing_algorithm_override_parses_canonical_algorithm_names_from_algorithm_parser() {
    for algorithm in [
        LoadBalancerAlgorithm::GroqMultiregion,
        LoadBalancerAlgorithm::PowerOfTwo,
        LoadBalancerAlgorithm::Pulsar,
        LoadBalancerAlgorithm::PulsarMultiregion,
        LoadBalancerAlgorithm::Random,
        LoadBalancerAlgorithm::RoundRobin,
    ] {
        let raw = algorithm.to_string();

        assert_eq!(raw.parse::<LoadBalancerAlgorithm>(), Ok(algorithm));
        assert_eq!(
            LoadBalancerAlgorithmOverride::parse(&raw),
            Ok(LoadBalancerAlgorithmOverride::for_test(raw, algorithm))
        );
    }
}

#[test]
fn routing_algorithm_override_parses_header_aliases_for_kebab_case_algorithm_names() {
    for algorithm in [
        LoadBalancerAlgorithm::GroqMultiregion,
        LoadBalancerAlgorithm::PowerOfTwo,
        LoadBalancerAlgorithm::Pulsar,
        LoadBalancerAlgorithm::PulsarMultiregion,
        LoadBalancerAlgorithm::Random,
        LoadBalancerAlgorithm::RoundRobin,
    ] {
        let raw = algorithm.to_string().replace('-', "_");
        assert_eq!(
            LoadBalancerAlgorithmOverride::parse(&raw),
            Ok(LoadBalancerAlgorithmOverride::for_test(raw, algorithm))
        );
    }
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
fn max_metric_age_config_is_rejected_after_staleness_cleanup() {
    let err = serde_json::from_str::<LoadBalancerAlgorithmConfig>(
        r#"{
                "algorithm": "pulsar",
                "max_metric_age_ms": 10000
            }"#,
    )
    .expect_err("removed metric-age config should fail startup");

    assert!(
        err.to_string().contains("max_metric_age_ms"),
        "unexpected parse error: {err}"
    );
}

#[test]
fn require_kv_metrics_config_is_rejected_after_kv_consideration_replaces_it() {
    let err = serde_json::from_str::<LoadBalancerAlgorithmConfig>(
        r#"{
                "algorithm": "pulsar",
                "require_kv_metrics": true
            }"#,
    )
    .expect_err("removed KV metrics config should fail startup");

    assert!(
        err.to_string().contains("require_kv_metrics"),
        "unexpected parse error: {err}"
    );
}

#[test]
fn removed_queue_slo_config_is_rejected_after_max_queue_time_migration() {
    let err = serde_json::from_str::<LoadBalancerAlgorithmConfig>(
        r#"{
                "algorithm": "groq-multiregion",
                "queue_slo_ms": 100
            }"#,
    )
    .expect_err("removed queue SLO alias should fail startup");

    assert!(
        err.to_string().contains("queue_slo_ms"),
        "unexpected parse error: {err}"
    );
}

#[test]
fn algorithm_specific_load_balancer_fields_are_rejected_for_other_algorithms() {
    for (raw, expected_field) in [
        (
            r#"{
                    "algorithm": "round-robin",
                    "seed": "unused"
                }"#,
            "seed",
        ),
        (
            r#"{
                    "algorithm": "pulsar",
                    "max_queue_time_floor_ms": 100
                }"#,
            "max_queue_time_floor_ms",
        ),
        (
            r#"{
                    "algorithm": "groq-multiregion",
                    "consider_kv_free_tokens": true
                }"#,
            "consider_kv_free_tokens",
        ),
    ] {
        let err = serde_json::from_str::<LoadBalancerAlgorithmConfig>(raw)
            .expect_err("algorithm-specific fields should be rejected on other algorithms");
        assert!(
            err.to_string().contains(expected_field),
            "expected {expected_field} in parse error, got: {err}"
        );
    }
}

#[test]
fn unused_load_balancer_fields_are_rejected() {
    for field in [
        "max_queue_tokens_factor",
        "hard_token_cap_factor",
        "reentry_hysteresis",
    ] {
        let raw = format!(r#"{{"algorithm": "pulsar", "{field}": 1}}"#);
        let err = serde_json::from_str::<LoadBalancerAlgorithmConfig>(&raw)
            .expect_err("unused config fields should fail startup");
        assert!(
            err.to_string().contains(field),
            "expected {field} in parse error, got: {err}"
        );
    }
}

#[test]
fn unknown_load_balancer_config_fields_are_rejected() {
    let err = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "power-of-two",
                "unused_top_level_field": true,
                "models": {
                    "model-a": {
                        "algorithm": "pulsar",
                        "unused_model_field": 123
                    }
                }
            }"#,
    )
    .expect_err("unknown config fields should fail startup");

    assert!(
        err.to_string().contains("unused_top_level_field"),
        "unexpected parse error: {err}"
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
    let config = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "power-of-two",
                "models": {
                    "model-a": {
                        "algorithm": "pulsar",
                        "seed": "seed-1",
                        "require_cache_affinity_key": true,
                        "consider_kv_free_tokens": true
                    }
                }
            }"#,
    )
    .expect("config should parse");

    let router = LoadBalancerRouter::from_config(&config).expect("router should build");
    let model_config = router.algorithm_config("model-a");
    assert_eq!(model_config.algorithm(), LoadBalancerAlgorithm::Pulsar);
    assert_eq!(model_config.seed(), Some("seed-1"));
    assert!(model_config.requires_cache_affinity_key());
    assert!(model_config.requires_input_tokens());
    assert!(model_config.considers_kv_free_tokens());
}

#[test]
fn kv_free_token_consideration_is_rejected_for_non_pulsar_algorithms() {
    let err = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "models": {
                    "model-a": {
                        "algorithm": "round-robin",
                        "consider_kv_free_tokens": true
                    }
                }
            }"#,
    )
    .expect_err("KV free-token consideration should be PULSAR-only");
    assert!(
        err.to_string().contains("consider_kv_free_tokens"),
        "unexpected validation error: {err}"
    );
}

#[test]
fn detailed_model_config_parses_for_pulsar_multiregion() {
    let config = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "power-of-two",
                "models": {
                    "model-a": {
                        "algorithm": "pulsar-multiregion",
                        "seed": "seed-1",
                        "require_cache_affinity_key": true,
                        "require_input_tokens": true,
                        "max_queue_time_floor_ms": 100,
                        "max_queue_time_ceil_ms": 100,
                        "ttft_bucket_size_ms": 50,
                        "n": 2
                    }
                }
            }"#,
    )
    .expect("config should parse");

    let router = LoadBalancerRouter::from_config(&config).expect("router should build");
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
    let config = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "power-of-two",
                "models": {
                    "model-a": {
                        "algorithm": "groq-multiregion",
                        "seed": "seed-1",
                        "require_cache_affinity_key": true,
                        "cache_affinity_virtual_nodes": 64,
                        "cache_affinity_backend_selection_count": 2
                    }
                }
            }"#,
    )
    .expect("config should parse");

    let router = LoadBalancerRouter::from_config(&config).expect("router should build");
    let model_config = router.algorithm_config("model-a");
    assert_eq!(
        model_config.algorithm(),
        LoadBalancerAlgorithm::GroqMultiregion
    );
    assert_eq!(model_config.seed(), Some("seed-1"));
    assert!(model_config.requires_cache_affinity_key());
    let multiregion_config = GroqMultiregionConfig::from_algorithm_config(model_config);
    assert_eq!(multiregion_config.cache_affinity_virtual_nodes(), 64);
    assert_eq!(
        multiregion_config.cache_affinity_backend_selection_count(),
        Some(2)
    );
}

#[test]
fn request_algorithms_parse_and_override_default_selection() {
    let config = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "power-of-two",
                "request_algorithms": {
                    "round-robin": "round-robin"
                }
            }"#,
    )
    .expect("config should parse");
    let router = LoadBalancerRouter::from_config(&config).expect("router should build");
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("round_robin")
        .expect("routing algorithm override should parse");

    let first = router
        .choose_candidate_with_algorithm_override(&request, &candidates, Some(&algorithm_override))
        .expect("routing method should be available")
        .expect("candidate should be selected")
        .choice;
    let second = router
        .choose_candidate_with_algorithm_override(&request, &candidates, Some(&algorithm_override))
        .expect("routing method should be available")
        .expect("candidate should be selected")
        .choice;

    assert_eq!(selected_cluster_id(first, &candidates), "cluster-0");
    assert_eq!(selected_cluster_id(second, &candidates), "cluster-1");
}

#[test]
fn choose_candidate_returns_slice_index_for_selected_cluster() {
    let config = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "round-robin"
            }"#,
    )
    .expect("config should parse");
    let router = LoadBalancerRouter::from_config(&config).expect("router should build");
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];

    let first = router
        .choose_candidate(&request, &candidates)
        .expect("candidate should be selected");
    let second = router
        .choose_candidate(&request, &candidates)
        .expect("candidate should be selected");

    assert_eq!(first.candidate_index, 0);
    assert_eq!(first.rank_depth, 1);
    assert_eq!(candidates[first.candidate_index].cluster_id, "cluster-0");
    assert_eq!(second.candidate_index, 1);
    assert_eq!(candidates[second.candidate_index].cluster_id, "cluster-1");
}

#[test]
fn choose_candidate_with_resolution_preserves_algorithm_metadata() {
    let config = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "power-of-two",
                "request_algorithms": {
                    "round-robin": "round-robin"
                }
            }"#,
    )
    .expect("config should parse");
    let router = LoadBalancerRouter::from_config(&config).expect("router should build");
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("round_robin")
        .expect("routing algorithm override should parse");
    let resolution = router
        .resolve_algorithm_override(&target.model_id, Some(&algorithm_override))
        .expect("routing method should be available");

    let selection = router
        .choose_candidate_with_algorithm_resolution(&request, &candidates, &resolution)
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
    let config = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "power-of-two",
                "request_algorithms": {
                    "round-robin": "round-robin"
                },
                "models": {
                    "model-a": {
                        "algorithm": "power-of-two",
                        "request_algorithms": {
                            "round-robin": {
                                "algorithm": "round-robin",
                                "require_input_tokens": true
                            }
                        }
                    }
                }
            }"#,
    )
    .expect("config should parse");
    let router = LoadBalancerRouter::from_config(&config).expect("router should build");
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
    let config = serde_json::from_str::<LoadBalancerConfig>(
        r#"{
                "default": "power-of-two",
                "request_algorithms": {
                    "random": "round-robin"
                }
            }"#,
    )
    .expect("config should parse");

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

    assert_eq!(config.cache_affinity_virtual_nodes(), 1);
    assert_eq!(config.cache_affinity_backend_selection_count(), None);
    assert_eq!(config.ttft_bucket_size(), Duration::from_millis(20));
    assert_eq!(config.next_bucket_unlock_factor(), 0.25);
    assert_eq!(config.sample_count(), 1);
    assert_eq!(config.max_queued(), 0);
    assert!(config.ignore_queue_time());
    assert!(config.ignore_input_processing_time());

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
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: HashMap::new(),
        models: [(
            "model-a".to_string(),
            LoadBalancerModelConfig::Name(LoadBalancerAlgorithm::GroqMultiregion),
        )]
        .into_iter()
        .collect(),
    };

    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    assert_eq!(router.algorithm_name("model-a"), "groq-multiregion");
}

#[test]
fn default_round_robin_uses_independent_sequences_per_model() {
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::RoundRobin,
        request_algorithms: HashMap::new(),
        models: HashMap::new(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let model_a_target = target_with_model("model-a");
    let model_b_target = target_with_model("model-b");
    let model_a_request = request(&model_a_target, None, None);
    let model_b_request = request(&model_b_target, None, None);
    let model_a_candidates = vec![candidate("model-a-0", 1024), candidate("model-a-1", 1024)];
    let model_b_candidates = vec![
        candidate("model-b-0", 1024),
        candidate("model-b-1", 1024),
        candidate("model-b-2", 1024),
    ];
    let mut model_a_selected = Vec::new();
    let mut model_b_selected = Vec::new();

    for _ in 0..3 {
        model_a_selected.push(
            router
                .choose_for_test(&model_a_request, &model_a_candidates)
                .expect("model-a candidate should be selected")
                .candidate
                .cluster_id,
        );
        model_b_selected.push(
            router
                .choose_for_test(&model_b_request, &model_b_candidates)
                .expect("model-b candidate should be selected")
                .candidate
                .cluster_id,
        );
    }

    assert_eq!(
        model_a_selected,
        vec![
            "model-a-0".to_string(),
            "model-a-1".to_string(),
            "model-a-0".to_string()
        ]
    );
    assert_eq!(
        model_b_selected,
        vec![
            "model-b-0".to_string(),
            "model-b-1".to_string(),
            "model-b-2".to_string()
        ]
    );
}

#[test]
fn default_round_robin_uses_independent_sequences_per_routing_target() {
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::RoundRobin,
        request_algorithms: HashMap::new(),
        models: HashMap::new(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let tenant_a_target = target_with_routing_key("tenant-a", "shared-model");
    let tenant_b_target = target_with_routing_key("tenant-b", "shared-model");
    let tenant_a_request = request(&tenant_a_target, None, None);
    let tenant_b_request = request(&tenant_b_target, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];
    let mut tenant_a_selected = Vec::new();
    let mut tenant_b_selected = Vec::new();

    for _ in 0..2 {
        tenant_a_selected.push(
            router
                .choose_for_test(&tenant_a_request, &candidates)
                .expect("tenant-a candidate should be selected")
                .candidate
                .cluster_id,
        );
        tenant_b_selected.push(
            router
                .choose_for_test(&tenant_b_request, &candidates)
                .expect("tenant-b candidate should be selected")
                .candidate
                .cluster_id,
        );
    }

    assert_eq!(
        tenant_a_selected,
        vec!["cluster-0".to_string(), "cluster-1".to_string()]
    );
    assert_eq!(
        tenant_b_selected,
        vec!["cluster-0".to_string(), "cluster-1".to_string()]
    );
}

#[test]
fn configured_round_robin_uses_independent_sequences_per_routing_target() {
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: HashMap::new(),
        models: [(
            "shared-model".to_string(),
            LoadBalancerModelConfig::Name(LoadBalancerAlgorithm::RoundRobin),
        )]
        .into_iter()
        .collect(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let tenant_a_target = target_with_routing_key("tenant-a", "shared-model");
    let tenant_b_target = target_with_routing_key("tenant-b", "shared-model");
    let tenant_a_request = request(&tenant_a_target, None, None);
    let tenant_b_request = request(&tenant_b_target, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];
    let mut tenant_a_selected = Vec::new();
    let mut tenant_b_selected = Vec::new();

    for _ in 0..2 {
        tenant_a_selected.push(
            router
                .choose_for_test(&tenant_a_request, &candidates)
                .expect("tenant-a candidate should be selected")
                .candidate
                .cluster_id,
        );
        tenant_b_selected.push(
            router
                .choose_for_test(&tenant_b_request, &candidates)
                .expect("tenant-b candidate should be selected")
                .candidate
                .cluster_id,
        );
    }

    assert_eq!(
        tenant_a_selected,
        vec!["cluster-0".to_string(), "cluster-1".to_string()]
    );
    assert_eq!(
        tenant_b_selected,
        vec!["cluster-0".to_string(), "cluster-1".to_string()]
    );
}

#[test]
fn choose_with_no_candidates_does_not_cache_default_lb_for_target() {
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::RoundRobin,
        request_algorithms: HashMap::new(),
        models: HashMap::new(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let target = target_with_model("unknown-model");
    let request = request(&target, None, None);
    let empty_candidates: Vec<RoutedClusterSnapshot> = Vec::new();

    assert!(
        router
            .choose_for_test(&request, &empty_candidates)
            .is_none()
    );
    assert_eq!(router.default_per_target_count(), 0);
}

#[test]
fn request_round_robin_override_uses_stable_per_target_sequence() {
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: request_algorithm_map(&[LoadBalancerAlgorithm::RoundRobin]),
        models: HashMap::new(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("round-robin")
        .expect("routing algorithm override should parse");
    let mut selected = Vec::new();

    for _ in 0..3 {
        selected.push(
            router
                .choose_candidate_with_algorithm_override(
                    &request,
                    &candidates,
                    Some(&algorithm_override),
                )
                .expect("routing method should be available")
                .expect("candidate should be selected")
                .choice,
        );
    }

    assert_eq!(
        selected
            .iter()
            .map(|choice| selected_cluster_id(*choice, &candidates).to_string())
            .collect::<Vec<_>>(),
        vec![
            "cluster-0".to_string(),
            "cluster-1".to_string(),
            "cluster-0".to_string()
        ]
    );
    assert_eq!(router.request_per_target_count(), 1);
}

#[test]
fn configured_request_override_caches_per_target_balancer() {
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::RoundRobin,
        request_algorithms: request_algorithm_map(&[LoadBalancerAlgorithm::PowerOfTwo]),
        models: HashMap::new(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("power-of-two")
        .expect("routing algorithm override should parse");

    let selection = router
        .choose_candidate_with_algorithm_override(&request, &candidates, Some(&algorithm_override))
        .expect("routing method should be available")
        .expect("candidate should be selected");

    assert_eq!(
        selection.effective_algorithm,
        LoadBalancerAlgorithm::PowerOfTwo
    );
    assert_eq!(router.request_per_target_count(), 1);
}

#[test]
fn matching_round_robin_override_reuses_configured_target_sequence() {
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::RoundRobin,
        request_algorithms: HashMap::new(),
        models: HashMap::new(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let target = target_with_model("model-a");
    let request = request(&target, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("round-robin")
        .expect("routing algorithm override should parse");

    let without_header = router
        .choose_for_test(&request, &candidates)
        .expect("candidate should be selected")
        .candidate
        .cluster_id;
    let with_header = router
        .choose_candidate_with_algorithm_override(&request, &candidates, Some(&algorithm_override))
        .expect("routing method should be available")
        .expect("candidate should be selected")
        .choice;
    let without_header_again = router
        .choose_for_test(&request, &candidates)
        .expect("candidate should be selected")
        .candidate
        .cluster_id;

    assert_eq!(without_header, "cluster-0");
    assert_eq!(selected_cluster_id(with_header, &candidates), "cluster-1");
    assert_eq!(without_header_again, "cluster-0");
}

#[test]
fn request_round_robin_override_keeps_routing_targets_isolated() {
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: request_algorithm_map(&[LoadBalancerAlgorithm::RoundRobin]),
        models: HashMap::new(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let target_a = target_with_routing_key("rk-a", "shared-model");
    let target_b = target_with_routing_key("rk-b", "shared-model");
    let request_a = request(&target_a, None, None);
    let request_b = request(&target_b, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("round-robin")
        .expect("routing algorithm override should parse");

    let first_a = router
        .choose_candidate_with_algorithm_override(
            &request_a,
            &candidates,
            Some(&algorithm_override),
        )
        .expect("routing method should be available")
        .expect("candidate should be selected")
        .choice;
    let first_b = router
        .choose_candidate_with_algorithm_override(
            &request_b,
            &candidates,
            Some(&algorithm_override),
        )
        .expect("routing method should be available")
        .expect("candidate should be selected")
        .choice;
    let second_a = router
        .choose_candidate_with_algorithm_override(
            &request_a,
            &candidates,
            Some(&algorithm_override),
        )
        .expect("routing method should be available")
        .expect("candidate should be selected")
        .choice;
    let second_b = router
        .choose_candidate_with_algorithm_override(
            &request_b,
            &candidates,
            Some(&algorithm_override),
        )
        .expect("routing method should be available")
        .expect("candidate should be selected")
        .choice;

    assert_eq!(selected_cluster_id(first_a, &candidates), "cluster-0");
    assert_eq!(selected_cluster_id(first_b, &candidates), "cluster-0");
    assert_eq!(selected_cluster_id(second_a, &candidates), "cluster-1");
    assert_eq!(selected_cluster_id(second_b, &candidates), "cluster-1");
}

#[test]
fn request_override_beats_configured_model_algorithm() {
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: request_algorithm_map(&[LoadBalancerAlgorithm::PowerOfTwo]),
        models: [(
            "shared-model".to_string(),
            LoadBalancerModelConfig::Name(LoadBalancerAlgorithm::RoundRobin),
        )]
        .into_iter()
        .collect(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let target = target_with_model("shared-model");
    let request = request(&target, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("power_of_two")
        .expect("routing algorithm override should parse");

    let selection = router
        .choose_candidate_with_algorithm_override(&request, &candidates, Some(&algorithm_override))
        .expect("routing method should be available")
        .expect("candidate should be selected");

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
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: HashMap::new(),
        models: [(
            "shared-model".to_string(),
            LoadBalancerModelConfig::Detailed(Box::new(round_robin_config)),
        )]
        .into_iter()
        .collect(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
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
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: request_algorithm_map(&[LoadBalancerAlgorithm::Pulsar]),
        models: [(
            "shared-model".to_string(),
            LoadBalancerModelConfig::Detailed(Box::new(pulsar_config)),
        )]
        .into_iter()
        .collect(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
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
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: HashMap::new(),
        models: HashMap::new(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let target = target_with_model("shared-model");
    let request = request(&target, None, None);
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];
    let algorithm_override = LoadBalancerAlgorithmOverride::parse("pulsar")
        .expect("routing algorithm override should parse");

    let error = router
        .choose_candidate_with_algorithm_override(&request, &candidates, Some(&algorithm_override))
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
    let config = LoadBalancerConfig {
        default: LoadBalancerAlgorithm::RoundRobin,
        request_algorithms: HashMap::new(),
        models: HashMap::new(),
    };
    let router = LoadBalancerRouter::from_config(&config).expect("router config should parse");
    let target = target_with_model("model-exclusions");
    let failed_clusters = HashSet::from(["cluster-0".to_string()]);
    let request = LoadBalancerRequest {
        excluded_cluster_ids: Some(&failed_clusters),
        ..request(&target, None, None)
    };
    let candidates = vec![candidate("cluster-0", 1024), candidate("cluster-1", 1024)];

    let chosen = router
        .choose_for_test(&request, &candidates)
        .expect("non-excluded candidate should be selected");

    assert_eq!(chosen.candidate.cluster_id, "cluster-1");
}

#[test]
fn groq_multiregion_prefers_lower_estimated_ttft() {
    let lb = create_load_balancer_with_config(&LoadBalancerAlgorithmConfig::from(
        LoadBalancerAlgorithm::GroqMultiregion,
    ))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(10));
    let mut fast = candidate("fast", 1024);
    fast.rtt = Duration::from_millis(5);
    let mut slow = candidate("slow", 1024);
    slow.rtt = Duration::from_millis(50);

    let chosen = lb
        .choose_for_test(&request, &[fast, slow])
        .expect("candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "fast");
}

#[test]
fn groq_multiregion_single_excluded_cluster_is_not_selected() {
    let lb = create_load_balancer_with_config(&LoadBalancerAlgorithmConfig::from(
        LoadBalancerAlgorithm::GroqMultiregion,
    ))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let excluded = HashSet::from(["fast-but-excluded".to_string()]);
    let request = LoadBalancerRequest {
        excluded_cluster_ids: Some(&excluded),
        ..request(&target, None, Some(10))
    };
    let mut fast = candidate("fast-but-excluded", 1024);
    fast.rtt = Duration::from_millis(5);
    let mut slow = candidate("slow-but-eligible", 1024);
    slow.rtt = Duration::from_millis(50);

    let chosen = lb
        .choose_for_test(&request, &[fast, slow])
        .expect("eligible candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "slow-but-eligible");
}

#[test]
fn groq_multiregion_cache_affinity_key_selects_stable_primary() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(8);
        settings.cache_affinity_backend_selection_count = Some(1);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, Some("prefix-a"), Some(1));
    let mut candidates = vec![
        candidate("affinity-a", 1024),
        candidate("affinity-b", 1024),
        candidate("affinity-c", 1024),
    ];
    for candidate in &mut candidates {
        candidate.rtt = Duration::from_millis(5);
    }

    let first = lb
        .choose_for_test(&request, &candidates)
        .expect("candidate should be selected")
        .candidate
        .cluster_id;
    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(&request, &candidates)
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, first);
    }
}

#[test]
fn groq_multiregion_cache_affinity_retry_skips_excluded_primary() {
    let config = multiregion_runtime_config(groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(32);
        settings.cache_affinity_backend_selection_count = Some(1);
    }));
    let lb = GroqMultiregionLoadBalancer::new(config.clone());
    let target = target();
    let mut excluded_primary = candidate("excluded-primary", 1024);
    excluded_primary.rtt = Duration::from_millis(5);
    let mut affinity_successor = candidate("affinity-successor", 1024);
    affinity_successor.rtt = Duration::from_millis(100);
    let mut global_fast = candidate("global-fast", 1024);
    global_fast.rtt = Duration::from_millis(1);
    let candidates = vec![excluded_primary, affinity_successor, global_fast];
    let excluded = HashSet::from(["excluded-primary".to_string()]);

    for idx in 0..8192 {
        let key = format!("retry-prefix-{idx}");
        let base_request = request(&target, Some(&key), Some(1));
        let selected = cache_affinity_candidates(&config, &base_request, &candidates)
            .expect("cache affinity should select a backend");
        if selected[0].cluster_id != "excluded-primary" {
            continue;
        }
        let retry_request = LoadBalancerRequest {
            excluded_cluster_ids: Some(&excluded),
            ..base_request
        };
        let selected_after_exclusion =
            cache_affinity_candidates(&config, &retry_request, &candidates)
                .expect("retry should select an affinity successor");
        if selected_after_exclusion[0].cluster_id != "affinity-successor" {
            continue;
        }

        let _ = lb
            .choose_for_test(&request(&target, Some(&key), Some(1)), &candidates)
            .expect("initial affinity primary should be selected");
        let chosen = lb
            .choose_for_test(&retry_request, &candidates)
            .expect("retry should select a non-excluded candidate");

        assert_eq!(chosen.candidate.cluster_id, "affinity-successor");
        let cached_key_bytes = lb.cached_affinity_key_bytes();
        assert!(cached_key_bytes >= key.len() * 2 + "excluded-primary".len());

        let chosen_again = lb
            .choose_for_test(&retry_request, &candidates)
            .expect("cached retry should select a non-excluded candidate");
        assert_eq!(chosen_again.candidate.cluster_id, "affinity-successor");
        assert_eq!(lb.cached_affinity_key_bytes(), cached_key_bytes);
        return;
    }

    panic!("expected to find an affinity key with a distinct excluded primary and successor");
}

#[test]
fn groq_multiregion_cache_affinity_retry_skips_multiple_excluded_primaries() {
    let config = multiregion_runtime_config(groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(32);
        settings.cache_affinity_backend_selection_count = Some(1);
    }));
    let lb = GroqMultiregionLoadBalancer::new(config.clone());
    let target = target();
    let mut excluded_a = candidate("excluded-a", 1024);
    excluded_a.rtt = Duration::from_millis(5);
    let mut excluded_b = candidate("excluded-b", 1024);
    excluded_b.rtt = Duration::from_millis(10);
    let mut affinity_successor = candidate("affinity-successor", 1024);
    affinity_successor.rtt = Duration::from_millis(100);
    let mut global_fast = candidate("global-fast", 1024);
    global_fast.rtt = Duration::from_millis(1);
    let candidates = vec![excluded_a, excluded_b, affinity_successor, global_fast];
    let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);

    for idx in 0..8192 {
        let key = format!("retry-prefix-{idx}");
        let base_request = request(&target, Some(&key), Some(1));
        let selected = cache_affinity_candidates(&config, &base_request, &candidates)
            .expect("cache affinity should select a backend");
        if selected[0].cluster_id != "excluded-a" {
            continue;
        }
        let retry_request = LoadBalancerRequest {
            excluded_cluster_ids: Some(&excluded),
            ..base_request
        };
        let selected_after_exclusion =
            cache_affinity_candidates(&config, &retry_request, &candidates)
                .expect("retry should select an affinity successor");
        if selected_after_exclusion[0].cluster_id != "affinity-successor" {
            continue;
        }

        let _ = lb
            .choose_for_test(&request(&target, Some(&key), Some(1)), &candidates)
            .expect("initial affinity primary should be selected");
        let chosen = lb
            .choose_for_test(&retry_request, &candidates)
            .expect("retry should select a non-excluded candidate");

        assert_eq!(chosen.candidate.cluster_id, "affinity-successor");
        let cached_key_bytes = lb.cached_affinity_key_bytes();
        assert!(cached_key_bytes >= key.len() * 2 + "excluded-a".len() + "excluded-b".len());

        let chosen_again = lb
            .choose_for_test(&retry_request, &candidates)
            .expect("cached retry should select a non-excluded candidate");
        assert_eq!(chosen_again.candidate.cluster_id, "affinity-successor");
        assert_eq!(lb.cached_affinity_key_bytes(), cached_key_bytes);
        return;
    }

    panic!("expected to find an affinity key with two excluded primaries and a distinct successor");
}

#[test]
fn groq_multiregion_affinity_cache_invalidates_when_candidates_change() {
    let config = multiregion_runtime_config(groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(8);
        settings.cache_affinity_backend_selection_count = Some(1);
    }));
    let lb = GroqMultiregionLoadBalancer::new(config.clone());
    let target = target();
    let first_candidates = vec![
        candidate("old-a", 1024),
        candidate("old-b", 1024),
        candidate("old-c", 1024),
    ];

    for idx in 0..512 {
        let key = format!("prefix-{idx}");
        let request = request(&target, Some(&key), Some(1));
        let selected = cache_affinity_candidates(&config, &request, &first_candidates)
            .expect("cache affinity should select a backend");
        if selected[0].cluster_id == "old-a" {
            continue;
        }

        let _ = lb
            .choose_for_test(&request, &first_candidates)
            .expect("initial candidate should be selected");
        let replacement = vec![candidate("replacement", 1024)];
        let chosen = lb
            .choose_for_test(&request, &replacement)
            .expect("replacement candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "replacement");
        return;
    }

    panic!("expected to find an affinity key that selects a non-zero candidate index");
}

#[test]
fn groq_multiregion_does_not_cache_oversized_affinity_key() {
    let config = multiregion_runtime_config(groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(8);
        settings.cache_affinity_backend_selection_count = Some(1);
    }));
    let lb = GroqMultiregionLoadBalancer::new(config);
    let target = target();
    let oversized_key = "x".repeat(MAX_CACHE_AFFINITY_CACHE_KEY_BYTES + 1);
    let request = request(&target, Some(&oversized_key), Some(1));
    let candidates = vec![
        candidate("large-key-a", 1024),
        candidate("large-key-b", 1024),
    ];

    let choice = lb
        .choose_for_test(&request, &candidates)
        .expect("oversized affinity key should still route");

    assert!(choice.candidate.cluster_id.starts_with("large-key-"));
    assert_eq!(lb.cached_affinity_key_bytes(), 0);
}

#[test]
fn groq_multiregion_cache_affinity_hash_uses_cluster_identity() {
    let config = multiregion_runtime_config(groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(8);
        settings.cache_affinity_backend_selection_count = Some(1);
    }));
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
    let config = multiregion_runtime_config(groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(8);
        settings.cache_affinity_backend_selection_count = Some(1);
    }));
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
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(1);
        settings.cache_affinity_backend_selection_count = Some(1);
        settings.n = Some(3);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, Some("prefix-a"), Some(1));
    let mut candidates = vec![
        candidate("fallback-a", 1024),
        candidate("fallback-b", 1024),
        candidate("fallback-c", 1024),
    ];
    let primary_config =
        multiregion_runtime_config(groq_multiregion_algorithm_config(|settings| {
            settings.seed = Some("seed-1".to_string());
            settings.cache_affinity_virtual_nodes = Some(1);
            settings.cache_affinity_backend_selection_count = Some(1);
        }));
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

    let chosen = lb
        .choose_for_test(&request, &candidates)
        .expect("fallback candidate should be selected");
    assert_ne!(chosen.candidate.cluster_id, primary);
}

#[test]
fn groq_multiregion_two_affinity_candidates_still_filter_capacity() {
    let config = multiregion_runtime_config(groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(8);
        settings.cache_affinity_backend_selection_count = Some(2);
        settings.n = Some(2);
    }));
    let lb = GroqMultiregionLoadBalancer::new(config.clone());
    let target = target();
    let request = request(&target, Some("prefix-a"), Some(1));
    let mut candidates = vec![
        candidate("two-affinity-a", 1024),
        candidate("two-affinity-b", 1024),
        candidate("two-affinity-c", 1024),
    ];
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

    let chosen = lb
        .choose_for_test(&request, &candidates)
        .expect("available affinity candidate should be selected");

    assert_eq!(chosen.candidate.cluster_id, available_selected_id);
}

#[test]
fn groq_multiregion_cache_affinity_keys_distribute_across_backends() {
    let config = multiregion_runtime_config(groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(32);
        settings.cache_affinity_backend_selection_count = Some(1);
    }));
    let target = target();
    let candidates = vec![
        candidate("dist-a", 1024),
        candidate("dist-b", 1024),
        candidate("dist-c", 1024),
    ];
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
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.seed = Some("seed-1".to_string());
        settings.cache_affinity_virtual_nodes = Some(1);
        settings.cache_affinity_backend_selection_count = Some(1);
        settings.n = Some(3);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(1));
    let mut affinity_primary = candidate("affinity-primary", 1024);
    affinity_primary.rtt = Duration::from_millis(50);
    let mut fastest = candidate("fastest-without-affinity", 1024);
    fastest.rtt = Duration::from_millis(5);

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(&request, &[affinity_primary.clone(), fastest.clone()])
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "fastest-without-affinity");
    }
}

#[test]
fn groq_multiregion_uses_input_tokens_in_ttft_estimate() {
    let lb = create_load_balancer_with_config(&LoadBalancerAlgorithmConfig::from(
        LoadBalancerAlgorithm::GroqMultiregion,
    ))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(100));
    let mut higher_rtt_higher_cap = candidate("higher-rtt-higher-cap", 1024);
    higher_rtt_higher_cap.rtt = Duration::from_millis(10);
    higher_rtt_higher_cap.stats.last_mean_input_tps = 200.0;
    let mut lower_rtt_lower_cap = candidate("lower-rtt-lower-cap", 1024);
    lower_rtt_lower_cap.rtt = Duration::from_millis(1);
    lower_rtt_lower_cap.stats.last_mean_input_tps = 10.0;

    let chosen = lb
        .choose_for_test(&request, &[higher_rtt_higher_cap, lower_rtt_lower_cap])
        .expect("candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "higher-rtt-higher-cap");
}

#[test]
fn groq_multiregion_can_ignore_input_processing_time_in_ttft_estimate() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.ignore_input_processing_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(100));
    let mut lower_rtt_lower_cap = candidate("lower-rtt-lower-cap", 1024);
    lower_rtt_lower_cap.rtt = Duration::from_millis(1);
    lower_rtt_lower_cap.stats.last_mean_input_tps = 10.0;
    let mut higher_rtt_higher_cap = candidate("higher-rtt-higher-cap", 1024);
    higher_rtt_higher_cap.rtt = Duration::from_millis(50);
    higher_rtt_higher_cap.stats.last_mean_input_tps = 200.0;

    let chosen = lb
        .choose_for_test(&request, &[lower_rtt_lower_cap, higher_rtt_higher_cap])
        .expect("candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "lower-rtt-lower-cap");
}

#[test]
fn groq_multiregion_limits_selection_to_first_ttft_bucket() {
    let lb = create_load_balancer_with_config(&LoadBalancerAlgorithmConfig::from(
        LoadBalancerAlgorithm::GroqMultiregion,
    ))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(1));
    let mut bucket_a = candidate("bucket-a", 1024);
    bucket_a.rtt = Duration::from_millis(5);
    bucket_a.stats.last_mean_input_tps = 10.0;
    let mut bucket_b = candidate("bucket-b", 1024);
    bucket_b.rtt = Duration::from_millis(20);
    bucket_b.stats.last_mean_input_tps = 10.0;
    let mut second_bucket = candidate("second-bucket", 1024);
    second_bucket.rtt = Duration::from_millis(50);
    second_bucket.stats.last_mean_input_tps = 10.0;

    for _ in 0..32 {
        let chosen = lb
            .choose_for_test(
                &request,
                &[bucket_a.clone(), bucket_b.clone(), second_bucket.clone()],
            )
            .expect("candidate should be selected");
        assert_ne!(chosen.candidate.cluster_id, "second-bucket");
    }
}

#[test]
fn groq_multiregion_can_ignore_queue_time_in_ttft_estimate() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.ignore_queue_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(0));
    let mut lower_rtt_higher_queue = candidate("lower-rtt-higher-queue", 1024);
    lower_rtt_higher_queue.rtt = Duration::from_millis(5);
    lower_rtt_higher_queue.stats.last_mean_input_tps = 100.0;
    lower_rtt_higher_queue.stats.queued_input_size = 100;
    let mut higher_rtt_lower_queue = candidate("higher-rtt-lower-queue", 1024);
    higher_rtt_lower_queue.rtt = Duration::from_millis(50);
    higher_rtt_lower_queue.stats.last_mean_input_tps = 100.0;

    let chosen = lb
        .choose_for_test(&request, &[lower_rtt_higher_queue, higher_rtt_lower_queue])
        .expect("candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "lower-rtt-higher-queue");
}

#[test]
fn groq_multiregion_ignore_queue_still_compares_sampled_candidates_by_queue_time() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(512));
    let mut higher_queue = candidate("higher-queue", 1024);
    higher_queue.rtt = Duration::from_millis(5);
    higher_queue.stats.last_mean_input_tps = 100.0;
    higher_queue.stats.queued_input_size = 2;
    let mut lower_queue = candidate("lower-queue", 1024);
    lower_queue.rtt = Duration::from_millis(5);
    lower_queue.stats.last_mean_input_tps = 100.0;
    lower_queue.stats.queued_input_size = 1;

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(&request, &[higher_queue.clone(), lower_queue.clone()])
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "lower-queue");
    }
}

#[test]
fn groq_multiregion_ignore_queue_keeps_later_prefill_buckets_locked() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(512));
    let mut first_bucket = candidate("first-bucket", 1024);
    first_bucket.rtt = Duration::from_millis(5);
    first_bucket.stats.last_mean_input_tps = 10_000.0;
    let mut later_bucket = candidate("later-bucket", 1024);
    later_bucket.rtt = Duration::from_millis(50);
    later_bucket.stats.last_mean_input_tps = 10_000.0;

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(&request, &[first_bucket.clone(), later_bucket.clone()])
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "first-bucket");
    }
}

#[test]
fn groq_multiregion_ignore_queue_skips_single_excluded_backend() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let excluded = HashSet::from(["excluded".to_string()]);
    let mut request = request(&target, None, Some(512));
    request.excluded_cluster_ids = Some(&excluded);
    let mut excluded_backend = candidate("excluded", 1024);
    excluded_backend.rtt = Duration::from_millis(5);
    let mut higher_queue = candidate("higher-queue", 1024);
    higher_queue.rtt = Duration::from_millis(50);
    higher_queue.stats.last_mean_input_tps = 100.0;
    higher_queue.stats.queued_input_size = 2;
    let mut lower_queue = candidate("lower-queue", 1024);
    lower_queue.rtt = Duration::from_millis(50);
    lower_queue.stats.last_mean_input_tps = 100.0;
    lower_queue.stats.queued_input_size = 1;

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(
                &request,
                &[
                    excluded_backend.clone(),
                    higher_queue.clone(),
                    lower_queue.clone(),
                ],
            )
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "lower-queue");
    }
}

#[test]
fn groq_multiregion_ignore_queue_skips_multiple_excluded_backends() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);
    let mut request = request(&target, None, Some(512));
    request.excluded_cluster_ids = Some(&excluded);
    let mut excluded_a = candidate("excluded-a", 1024);
    excluded_a.rtt = Duration::from_millis(5);
    let mut excluded_b = candidate("excluded-b", 1024);
    excluded_b.rtt = Duration::from_millis(5);
    let mut higher_queue = candidate("higher-queue", 1024);
    higher_queue.rtt = Duration::from_millis(50);
    higher_queue.stats.last_mean_input_tps = 100.0;
    higher_queue.stats.queued_input_size = 2;
    let mut lower_queue = candidate("lower-queue", 1024);
    lower_queue.rtt = Duration::from_millis(50);
    lower_queue.stats.last_mean_input_tps = 100.0;
    lower_queue.stats.queued_input_size = 1;

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(
                &request,
                &[
                    excluded_a.clone(),
                    excluded_b.clone(),
                    higher_queue.clone(),
                    lower_queue.clone(),
                ],
            )
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "lower-queue");
    }
}

#[test]
fn groq_multiregion_deprioritizes_non_finite_ttft_candidates() {
    let lb = create_load_balancer_with_config(&LoadBalancerAlgorithmConfig::from(
        LoadBalancerAlgorithm::GroqMultiregion,
    ))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(10));
    let finite = candidate("finite", 1024);
    let mut non_finite = candidate("non-finite", 1024);
    non_finite.rtt = Duration::from_millis(1);
    non_finite.stats.last_mean_input_tps = 0.0;
    non_finite.stats.queued_input_size = 5;

    let chosen = lb
        .choose_for_test(&request, &[finite, non_finite])
        .expect("candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "finite");
}

#[test]
fn groq_multiregion_uses_last_mean_input_tps_for_prefill_estimates() {
    let lb = create_load_balancer_with_config(&LoadBalancerAlgorithmConfig::from(
        LoadBalancerAlgorithm::GroqMultiregion,
    ))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(100));
    let mut high_capacity = candidate("high-capacity", 1024);
    high_capacity.rtt = Duration::from_millis(10);
    high_capacity.stats.last_mean_input_tps = 200.0;
    let mut low_capacity = candidate("low-capacity", 1024);
    low_capacity.rtt = Duration::from_millis(1);
    low_capacity.stats.last_mean_input_tps = 10.0;

    let chosen = lb
        .choose_for_test(&request, &[high_capacity, low_capacity])
        .expect("candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "high-capacity");
}

#[test]
fn groq_multiregion_unlocks_later_ttft_bucket_after_waiting() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(1);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = LoadBalancerRequest {
        routing_target: &target,
        cache_affinity_key: None,
        input_tokens: Some(1),
        priority: 0,
        received_at: Instant::now() - Duration::from_millis(20),
        request_slo: None,
        excluded_cluster_ids: None,
    };
    let mut fast_full = candidate("fast-full", 1024);
    fast_full.rtt = Duration::from_millis(20);
    fast_full.stats.max_engine_concurrency = 1;
    fast_full.stats.num_running_queries = 1;
    let mut slower_available = candidate("slower-available", 1024);
    slower_available.rtt = Duration::from_millis(60);
    slower_available.stats.max_engine_concurrency = 1;

    let chosen = lb
        .choose_for_test(&request, &[fast_full, slower_available])
        .expect("later bucket should unlock and provide a candidate");
    assert_eq!(chosen.candidate.cluster_id, "slower-available");
}

#[test]
fn groq_multiregion_filters_full_backends() {
    let lb = create_load_balancer_with_config(&LoadBalancerAlgorithmConfig::from(
        LoadBalancerAlgorithm::GroqMultiregion,
    ))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(1));
    let mut full = candidate("full", 1024);
    full.rtt = Duration::from_millis(5);
    full.stats.max_engine_concurrency = 1;
    full.stats.num_running_queries = 1;
    let mut available = candidate("available", 1024);
    available.rtt = Duration::from_millis(6);
    available.stats.max_engine_concurrency = 1;

    let chosen = lb
        .choose_for_test(&request, &[full, available])
        .expect("available candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "available");
}

#[test]
fn groq_multiregion_filters_backends_over_queue_slo() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.max_queue_time_floor_ms = Some(5);
        settings.max_queue_time_ceil_ms = Some(5);
        settings.n = Some(2);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(0));
    let mut over_slo = candidate("over-slo", 1024);
    over_slo.stats.last_mean_input_tps = 100.0;
    over_slo.stats.queued_input_size = 1;
    let mut under_slo = candidate("under-slo", 1024);
    under_slo.stats.last_mean_input_tps = 100.0;
    under_slo.stats.queued_input_size = 0;

    let chosen = lb
        .choose_for_test(&request, &[over_slo, under_slo])
        .expect("under-SLO candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "under-slo");
}

#[test]
fn groq_multiregion_filters_queue_slo_before_ttft_bucket_locking() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.max_queue_time_floor_ms = Some(5);
        settings.max_queue_time_ceil_ms = Some(5);
        settings.n = Some(2);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(0));
    let mut over_slo_first_bucket = candidate("over-slo-first-bucket", 1024);
    over_slo_first_bucket.rtt = Duration::from_millis(1);
    over_slo_first_bucket.stats.last_mean_input_tps = 100.0;
    over_slo_first_bucket.stats.queued_input_size = 1;
    let mut under_slo_later_bucket = candidate("under-slo-later-bucket", 1024);
    under_slo_later_bucket.rtt = Duration::from_millis(40);
    under_slo_later_bucket.stats.last_mean_input_tps = 100.0;

    let chosen = lb
        .choose_for_test(&request, &[over_slo_first_bucket, under_slo_later_bucket])
        .expect("under-SLO candidate in later TTFT bucket should be selected");
    assert_eq!(chosen.candidate.cluster_id, "under-slo-later-bucket");
}

#[test]
fn groq_multiregion_queue_slo_still_applies_when_queue_time_is_ignored_for_ttft() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.ignore_queue_time = Some(true);
        settings.max_queue_time_floor_ms = Some(5);
        settings.max_queue_time_ceil_ms = Some(5);
        settings.n = Some(2);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(0));
    let mut over_slo_lower_rtt = candidate("over-slo-lower-rtt", 1024);
    over_slo_lower_rtt.rtt = Duration::from_millis(5);
    over_slo_lower_rtt.stats.last_mean_input_tps = 100.0;
    over_slo_lower_rtt.stats.queued_input_size = 1;
    let mut under_slo_higher_rtt = candidate("under-slo-higher-rtt", 1024);
    under_slo_higher_rtt.rtt = Duration::from_millis(6);
    under_slo_higher_rtt.stats.last_mean_input_tps = 100.0;

    let chosen = lb
        .choose_for_test(&request, &[over_slo_lower_rtt, under_slo_higher_rtt])
        .expect("under-SLO candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "under-slo-higher-rtt");
}

#[test]
fn groq_multiregion_returns_none_when_only_candidate_exceeds_queue_slo() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.max_queue_time_floor_ms = Some(5);
        settings.max_queue_time_ceil_ms = Some(5);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(0));
    let mut over_slo = candidate("over-slo", 1024);
    over_slo.stats.last_mean_input_tps = 100.0;
    over_slo.stats.queued_input_size = 1;

    let choice = lb.choose_for_test(&request, &[over_slo]);
    assert!(choice.is_none());
}

#[test]
fn max_queue_time_interpolates_between_floor_and_ceil() {
    let config = groq_multiregion_algorithm_config(|settings| {
        settings.max_queue_time_floor_ms = Some(100);
        settings.max_queue_time_ceil_ms = Some(300);
    });
    let target = target();
    let request = LoadBalancerRequest {
        routing_target: &target,
        cache_affinity_key: None,
        input_tokens: Some(0),
        priority: 0,
        received_at: Instant::now() - Duration::from_millis(500),
        request_slo: Some(Duration::from_millis(1000)),
        excluded_cluster_ids: None,
    };

    let config = GroqMultiregionConfig::from_algorithm_config(&config);
    let max_queue_time = config
        .max_queue_time(&request)
        .expect("floor and ceil should enable max queue time");
    assert!(
        (200..=205).contains(&max_queue_time.as_millis()),
        "expected roughly 200ms max queue time, got {max_queue_time:?}"
    );
}

#[test]
fn max_queue_time_uses_ceil_when_request_slo_is_missing() {
    let config = groq_multiregion_algorithm_config(|settings| {
        settings.max_queue_time_floor_ms = Some(100);
        settings.max_queue_time_ceil_ms = Some(300);
    });
    let target = target();
    let request = request(&target, None, Some(0));

    let config = GroqMultiregionConfig::from_algorithm_config(&config);
    assert_eq!(
        config
            .max_queue_time(&request)
            .expect("floor and ceil should enable max queue time"),
        Duration::from_millis(300)
    );
}

#[test]
fn max_queue_time_uses_ceil_when_request_slo_is_zero() {
    let config = groq_multiregion_algorithm_config(|settings| {
        settings.max_queue_time_floor_ms = Some(100);
        settings.max_queue_time_ceil_ms = Some(300);
    });
    let target = target();
    let request = LoadBalancerRequest {
        routing_target: &target,
        cache_affinity_key: None,
        input_tokens: Some(0),
        priority: 0,
        received_at: Instant::now(),
        request_slo: Some(Duration::ZERO),
        excluded_cluster_ids: None,
    };

    let config = GroqMultiregionConfig::from_algorithm_config(&config);
    assert_eq!(
        config
            .max_queue_time(&request)
            .expect("floor and ceil should enable max queue time"),
        Duration::from_millis(300)
    );
}

#[test]
fn equal_floor_and_ceil_configures_fixed_max_queue_time() {
    let config = groq_multiregion_algorithm_config(|settings| {
        settings.max_queue_time_floor_ms = Some(75);
        settings.max_queue_time_ceil_ms = Some(75);
    });
    let target = target();
    let request = request(&target, None, Some(0));

    let config = GroqMultiregionConfig::from_algorithm_config(&config);
    assert_eq!(
        config
            .max_queue_time(&request)
            .expect("equal floor and ceil should enable fixed max queue time"),
        Duration::from_millis(75)
    );
}

#[test]
fn groq_multiregion_compares_by_queue_time_within_unlocked_bucket() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(1));
    let mut higher_queue = candidate("higher-queue", 1024);
    higher_queue.rtt = Duration::from_millis(5);
    higher_queue.stats.last_mean_input_tps = 100.0;
    higher_queue.stats.queued_input_size = 2;
    let mut lower_queue = candidate("lower-queue", 1024);
    lower_queue.rtt = Duration::from_millis(5);
    lower_queue.stats.last_mean_input_tps = 100.0;
    lower_queue.stats.queued_input_size = 1;

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(&request, &[higher_queue.clone(), lower_queue.clone()])
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "lower-queue");
    }
}

#[test]
fn groq_multiregion_rtt_only_still_compares_sampled_candidates_by_queue_time() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
        settings.ignore_input_processing_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(512));
    let mut higher_queue = candidate("higher-queue", 1024);
    higher_queue.rtt = Duration::from_millis(5);
    higher_queue.stats.last_mean_input_tps = 100.0;
    higher_queue.stats.queued_input_size = 2;
    let mut lower_queue = candidate("lower-queue", 1024);
    lower_queue.rtt = Duration::from_millis(5);
    lower_queue.stats.last_mean_input_tps = 100.0;
    lower_queue.stats.queued_input_size = 1;

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(&request, &[higher_queue.clone(), lower_queue.clone()])
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "lower-queue");
    }
}

#[test]
fn groq_multiregion_rtt_only_filters_full_backends_before_sampling() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
        settings.ignore_input_processing_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(512));
    let mut full = candidate("full", 1024);
    full.rtt = Duration::from_millis(5);
    full.stats.max_engine_concurrency = 1;
    full.stats.num_running_queries = 1;
    let mut available = candidate("available", 1024);
    available.rtt = Duration::from_millis(5);
    available.stats.max_engine_concurrency = 1;

    let chosen = lb
        .choose_for_test(&request, &[full, available])
        .expect("available candidate should be selected");
    assert_eq!(chosen.candidate.cluster_id, "available");
}

#[test]
fn groq_multiregion_rtt_only_keeps_later_rtt_buckets_locked() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
        settings.ignore_input_processing_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request(&target, None, Some(512));
    let mut first_bucket = candidate("first-bucket", 1024);
    first_bucket.rtt = Duration::from_millis(5);
    let mut later_bucket = candidate("later-bucket", 1024);
    later_bucket.rtt = Duration::from_millis(50);

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(&request, &[first_bucket.clone(), later_bucket.clone()])
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "first-bucket");
    }
}

#[test]
fn groq_multiregion_rtt_only_skips_single_excluded_backend() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
        settings.ignore_input_processing_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let excluded = HashSet::from(["excluded".to_string()]);
    let mut request = request(&target, None, Some(512));
    request.excluded_cluster_ids = Some(&excluded);
    let mut excluded_backend = candidate("excluded", 1024);
    excluded_backend.rtt = Duration::from_millis(5);
    let mut higher_queue = candidate("higher-queue", 1024);
    higher_queue.rtt = Duration::from_millis(50);
    higher_queue.stats.last_mean_input_tps = 100.0;
    higher_queue.stats.queued_input_size = 2;
    let mut lower_queue = candidate("lower-queue", 1024);
    lower_queue.rtt = Duration::from_millis(50);
    lower_queue.stats.last_mean_input_tps = 100.0;
    lower_queue.stats.queued_input_size = 1;

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(
                &request,
                &[
                    excluded_backend.clone(),
                    higher_queue.clone(),
                    lower_queue.clone(),
                ],
            )
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "lower-queue");
    }
}

#[test]
fn groq_multiregion_rtt_only_skips_multiple_excluded_backends() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
        settings.ignore_queue_time = Some(true);
        settings.ignore_input_processing_time = Some(true);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);
    let mut request = request(&target, None, Some(512));
    request.excluded_cluster_ids = Some(&excluded);
    let mut excluded_a = candidate("excluded-a", 1024);
    excluded_a.rtt = Duration::from_millis(5);
    let mut excluded_b = candidate("excluded-b", 1024);
    excluded_b.rtt = Duration::from_millis(5);
    let mut higher_queue = candidate("higher-queue", 1024);
    higher_queue.rtt = Duration::from_millis(50);
    higher_queue.stats.last_mean_input_tps = 100.0;
    higher_queue.stats.queued_input_size = 2;
    let mut lower_queue = candidate("lower-queue", 1024);
    lower_queue.rtt = Duration::from_millis(50);
    lower_queue.stats.last_mean_input_tps = 100.0;
    lower_queue.stats.queued_input_size = 1;

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(
                &request,
                &[
                    excluded_a.clone(),
                    excluded_b.clone(),
                    higher_queue.clone(),
                    lower_queue.clone(),
                ],
            )
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "lower-queue");
    }
}

#[test]
fn groq_multiregion_uses_priority_queue_time_estimate() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request_with_priority(&target, None, Some(0), 4);
    let mut aggregate_lower_priority_higher = candidate("aggregate-lower-priority-higher", 1024);
    aggregate_lower_priority_higher.stats.last_mean_input_tps = 100.0;
    aggregate_lower_priority_higher.stats.queued_input_size = 0;
    aggregate_lower_priority_higher
        .stats
        .queue_time_estimate_ms_by_priority = HashMap::from([(4, 50)]);
    let mut aggregate_higher_priority_lower = candidate("aggregate-higher-priority-lower", 1024);
    aggregate_higher_priority_lower.stats.last_mean_input_tps = 100.0;
    aggregate_higher_priority_lower.stats.queued_input_size = 100;
    aggregate_higher_priority_lower
        .stats
        .queue_time_estimate_ms_by_priority = HashMap::from([(4, 5)]);

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(
                &request,
                &[
                    aggregate_lower_priority_higher.clone(),
                    aggregate_higher_priority_lower.clone(),
                ],
            )
            .expect("candidate should be selected");
        assert_eq!(
            chosen.candidate.cluster_id,
            "aggregate-higher-priority-lower"
        );
    }
}

#[test]
fn groq_multiregion_clamps_priority_to_max_known_queue_time_priority() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request_with_priority(&target, None, Some(0), 10);
    let mut lower_clamped_queue = candidate("lower-clamped-queue", 1024);
    lower_clamped_queue.stats.queue_time_estimate_ms_by_priority = HashMap::from([(2, 5)]);
    let mut higher_clamped_queue = candidate("higher-clamped-queue", 1024);
    higher_clamped_queue
        .stats
        .queue_time_estimate_ms_by_priority = HashMap::from([(2, 50)]);

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(
                &request,
                &[lower_clamped_queue.clone(), higher_clamped_queue.clone()],
            )
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "lower-clamped-queue");
    }
}

#[test]
fn groq_multiregion_uses_next_highest_priority_queue_time_estimate() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request_with_priority(&target, None, Some(0), 3);
    let mut higher_queue = candidate("higher-queue", 1024);
    higher_queue.stats.queue_time_estimate_ms_by_priority = HashMap::from([(2, 50)]);
    let mut lower_queue = candidate("lower-queue", 1024);
    lower_queue.stats.queue_time_estimate_ms_by_priority = HashMap::from([(2, 5)]);

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(&request, &[higher_queue.clone(), lower_queue.clone()])
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "lower-queue");
    }
}

#[test]
fn groq_multiregion_treats_lower_priority_only_queue_as_zero_for_higher_priority_request() {
    let lb = create_load_balancer_with_config(&groq_multiregion_algorithm_config(|settings| {
        settings.n = Some(2);
    }))
    .expect("factory should accept groq-multiregion");
    let target = target();
    let request = request_with_priority(&target, None, Some(0), 0);
    let mut sparse_lower_priority_only = candidate("sparse-lower-priority-only", 1024);
    sparse_lower_priority_only.stats.last_mean_input_tps = 100.0;
    sparse_lower_priority_only.stats.queued_input_size = 100;
    sparse_lower_priority_only
        .stats
        .queue_time_estimate_ms_by_priority = HashMap::from([(4, 0)]);
    let mut aggregate_lower_queue = candidate("aggregate-lower-queue", 1024);
    aggregate_lower_queue.stats.last_mean_input_tps = 100.0;
    aggregate_lower_queue.stats.queued_input_size = 1;

    for _ in 0..16 {
        let chosen = lb
            .choose_for_test(
                &request,
                &[
                    sparse_lower_priority_only.clone(),
                    aggregate_lower_queue.clone(),
                ],
            )
            .expect("candidate should be selected");
        assert_eq!(chosen.candidate.cluster_id, "sparse-lower-priority-only");
    }
}

#[test]
fn pulsar_different_affinity_keys_reach_multiple_backends() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));
    let target = target();
    let candidates = vec![
        candidate("inst-a", 1024),
        candidate("inst-b", 1024),
        candidate("inst-c", 1024),
    ];

    let mut seen = std::collections::HashSet::new();
    for idx in 0..128 {
        let key = format!("affinity-{idx}");
        let choice = pulsar
            .choose_for_test(&request(&target, Some(&key), Some(128)), &candidates)
            .expect("choice should exist");
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
fn pulsar_keeps_ranked_primary_despite_low_free_kv() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));
    let target = target();

    let mut found = false;
    for idx in 0..512 {
        let key = format!("affinity-{idx}");
        let request = request(&target, Some(&key), Some(128));
        let feasible_candidates = vec![candidate("inst-a", 1024), candidate("inst-b", 1024)];
        let baseline = pulsar
            .choose_for_test(&request, &feasible_candidates)
            .expect("baseline choice should exist");
        if baseline.candidate.cluster_id != "inst-a" {
            continue;
        }

        let constrained_candidates = vec![candidate("inst-a", 64), candidate("inst-b", 1024)];
        let constrained = pulsar
            .choose_for_test(&request, &constrained_candidates)
            .expect("primary choice should exist");
        assert_eq!(constrained.candidate.cluster_id, "inst-a");
        assert_eq!(constrained.rank_depth, 1);
        found = true;
        break;
    }

    assert!(
        found,
        "expected to find an affinity key that ranks inst-a first"
    );
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
    let low_free_choice = pulsar
        .choose_for_test(&request, &low_free)
        .expect("sufficient-free fallback should be selected");
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
    let missing_metrics_choice = pulsar
        .choose_for_test(&request, &missing_metrics)
        .expect("candidate reporting KV metrics should be selected");
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
    let candidates = vec![candidate("inst-a", 1024), candidate("inst-b", 1024)];
    let primary = pulsar
        .choose_for_test(&request, &candidates)
        .expect("initial request should choose a primary");

    let mut changed = candidates;
    let primary_candidate = changed
        .iter_mut()
        .find(|candidate| candidate.cluster_id == primary.candidate.cluster_id)
        .expect("selected primary should be in candidate list");
    primary_candidate.stats.kv_cache_used_tokens = 960;
    primary_candidate.stats.kv_cache_free_tokens = 64;

    let fallback = pulsar
        .choose_for_test(&request, &changed)
        .expect("cached ranking should find the newly eligible fallback");
    assert_ne!(fallback.candidate.cluster_id, primary.candidate.cluster_id);
    assert!(fallback.selected_after_kv_free_tokens_skip);
}

#[test]
fn pulsar_cached_retry_skips_single_excluded_primary() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));
    let target = target();
    let candidates = vec![
        candidate("retry-primary", 1024),
        candidate("retry-fallback", 1024),
        candidate("retry-second-fallback", 1024),
    ];
    let base_request = request(&target, Some("retry-prefix"), Some(128));
    let primary = pulsar
        .choose_for_test(&base_request, &candidates)
        .expect("initial request should select a primary")
        .candidate
        .cluster_id;
    let excluded = HashSet::from([primary.clone()]);
    let retry_request = LoadBalancerRequest {
        excluded_cluster_ids: Some(&excluded),
        ..base_request
    };

    let retry = pulsar
        .choose_for_test(&retry_request, &candidates)
        .expect("retry should select a non-excluded candidate");

    assert_ne!(retry.candidate.cluster_id, primary);
    assert!(retry.rank_depth > 1);
}

#[test]
fn pulsar_returns_none_when_all_candidates_are_excluded() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));
    let target = target();
    let candidates = vec![
        candidate("all-excluded-a", 1024),
        candidate("all-excluded-b", 1024),
    ];
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
        let mut initial_a = candidate("inst-a", 1024);
        initial_a.stats.last_mean_input_tps = 10_000.0;
        let mut initial_b = candidate("inst-b", 1024);
        initial_b.stats.last_mean_input_tps = 1.0;
        let initial = vec![initial_a, initial_b];

        let mut changed_a = candidate("inst-a", 1024);
        changed_a.stats.last_mean_input_tps = 1.0;
        let mut changed_b = candidate("inst-b", 1024);
        changed_b.stats.last_mean_input_tps = 10_000.0;
        let changed = vec![changed_a, changed_b];

        let first = pulsar
            .choose_for_test(&request, &initial)
            .expect("initial choice should exist")
            .candidate
            .cluster_id;
        let second = pulsar
            .choose_for_test(&request, &changed)
            .expect("changed choice should exist")
            .candidate
            .cluster_id;
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
    let candidates = vec![
        candidate("large-key-a", 1024),
        candidate("large-key-b", 1024),
    ];

    let choice = pulsar
        .choose_for_test(&request, &candidates)
        .expect("oversized affinity key should still route");

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

    let mut candidate = candidate("inst-a", 1024);
    candidate.stats.last_mean_input_tps = 123.0;

    assert_eq!(pulsar.weight(&candidate), Some(123.0));
}

#[test]
fn pulsar_excludes_candidate_with_invalid_last_mean_input_tps() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));

    let target = target();
    let mut invalid = candidate("inst-a", 1024);
    invalid.stats.last_mean_input_tps = 0.0;
    let valid = candidate("inst-b", 1024);

    let choice = pulsar
        .choose_for_test(
            &request(&target, Some("prefix-1"), Some(128)),
            &[invalid, valid],
        )
        .expect("valid candidate should still be chosen");
    assert_eq!(choice.candidate.cluster_id, "inst-b");
}

#[test]
fn pulsar_returns_none_when_all_candidates_lack_valid_last_mean_input_tps() {
    let pulsar = PulsarLoadBalancer::new(seeded_pulsar_algorithm_config("seed-1"));

    let target = target();
    let mut invalid_a = candidate("inst-a", 1024);
    invalid_a.stats.last_mean_input_tps = 0.0;
    let mut invalid_b = candidate("inst-b", 1024);
    invalid_b.stats.last_mean_input_tps = f64::NAN;

    let choice = pulsar.choose_for_test(
        &request(&target, Some("prefix-1"), Some(128)),
        &[invalid_a, invalid_b],
    );
    assert!(choice.is_none());
}

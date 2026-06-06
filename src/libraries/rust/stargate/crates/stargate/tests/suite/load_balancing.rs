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
use std::io::Write;
use std::time::Duration;

use crate::common::{
    assert_all_probes_routed_to, init_crypto, make_stargate_runtime_with_lb, start_dummy_inst,
    wait_for_inference_server_ids, wait_for_routing, wait_for_routing_with_cache_affinity,
    with_proxy_headers, with_proxy_headers_cache_affinity, with_proxy_headers_input_tokens,
};
use pylon_lib::{
    BringupConfig, CurrentModelStats, InferenceServerRegistrationClient,
    InferenceServerRegistrationConfig, OutputTokenParserFactory,
};
use stargate::routing::{RoutedClusterSnapshot, RoutedInferenceServerSnapshot, RoutingTargetKey};
use stargate::test_support::StargateState;
use stargate_proto::pb::InferenceServerStatus;

#[derive(Debug)]
struct ExpectedCandidateStats<'a> {
    inference_server_id: &'a str,
    last_mean_input_tps: f64,
}

#[derive(Debug)]
struct ExpectedClusterStats<'a> {
    cluster_id: &'a str,
    last_mean_input_tps: f64,
    queued_input_size: u64,
    active_backend_count: usize,
}

#[derive(Debug)]
struct ExpectedPriorityClusterStats<'a> {
    cluster_id: &'a str,
    priority: u32,
    queue_time_estimate_ms: u64,
}

async fn wait_for_candidate_stats(
    state: &StargateState,
    model_id: &str,
    expected: &[ExpectedCandidateStats<'_>],
    timeout: Duration,
) {
    let target = RoutingTargetKey {
        routing_key: None,
        model_id: model_id.to_string(),
    };
    let deadline = tokio::time::Instant::now() + timeout;
    let mut interval = tokio::time::interval(Duration::from_millis(10));

    loop {
        let candidates = state.candidates_for_target(&target).await;
        if candidates.len() == expected.len()
            && expected.iter().all(|expected| {
                candidates
                    .iter()
                    .any(|actual| candidate_stats_match(actual, expected))
            })
        {
            return;
        }

        if tokio::time::Instant::now() >= deadline {
            let last_seen = candidates
                .iter()
                .map(|candidate| {
                    format!(
                        "{} last_mean_input_tps={}",
                        candidate.inference_server_id, candidate.stats.last_mean_input_tps
                    )
                })
                .collect::<Vec<_>>();
            panic!(
                "model '{model_id}' candidates did not reach expected stats within {}s; expected {expected:?}, last_seen {last_seen:?}",
                timeout.as_secs()
            );
        }

        interval.tick().await;
    }
}

async fn wait_for_cluster_stats(
    state: &StargateState,
    model_id: &str,
    expected: &[ExpectedClusterStats<'_>],
    timeout: Duration,
) {
    let target = RoutingTargetKey {
        routing_key: None,
        model_id: model_id.to_string(),
    };
    let deadline = tokio::time::Instant::now() + timeout;
    let mut interval = tokio::time::interval(Duration::from_millis(10));

    loop {
        let clusters = state.cluster_candidates_for_target(&target).await;
        if clusters.len() == expected.len()
            && expected.iter().all(|expected| {
                clusters
                    .iter()
                    .any(|actual| cluster_stats_match(actual, expected))
            })
        {
            return;
        }

        if tokio::time::Instant::now() >= deadline {
            let last_seen = clusters
                .iter()
                .map(|cluster| {
                    format!(
                        "{} last_mean_input_tps={} queued_input_size={} active_backend_count={}",
                        cluster.cluster_id,
                        cluster.stats.last_mean_input_tps,
                        cluster.stats.queued_input_size,
                        cluster.active_backend_count
                    )
                })
                .collect::<Vec<_>>();
            panic!(
                "model '{model_id}' clusters did not reach expected stats within {}s; expected {expected:?}, last_seen {last_seen:?}",
                timeout.as_secs()
            );
        }

        interval.tick().await;
    }
}

async fn wait_for_priority_cluster_stats(
    state: &StargateState,
    model_id: &str,
    expected: &[ExpectedPriorityClusterStats<'_>],
    timeout: Duration,
) {
    let target = RoutingTargetKey {
        routing_key: None,
        model_id: model_id.to_string(),
    };
    let deadline = tokio::time::Instant::now() + timeout;
    let mut interval = tokio::time::interval(Duration::from_millis(10));

    loop {
        let clusters = state.cluster_candidates_for_target(&target).await;
        if clusters.len() == expected.len()
            && expected.iter().all(|expected| {
                clusters
                    .iter()
                    .any(|actual| priority_cluster_stats_match(actual, expected))
            })
        {
            return;
        }

        if tokio::time::Instant::now() >= deadline {
            let last_seen = clusters
                .iter()
                .map(|cluster| {
                    format!(
                        "{} queue_time_estimate_ms_by_priority={:?}",
                        cluster.cluster_id, cluster.stats.queue_time_estimate_ms_by_priority,
                    )
                })
                .collect::<Vec<_>>();
            panic!(
                "model '{model_id}' clusters did not reach expected priority stats within {}s; expected {expected:?}, last_seen {last_seen:?}",
                timeout.as_secs()
            );
        }

        interval.tick().await;
    }
}

fn candidate_stats_match(
    actual: &RoutedInferenceServerSnapshot,
    expected: &ExpectedCandidateStats<'_>,
) -> bool {
    actual.inference_server_id == expected.inference_server_id
        && float_eq(
            actual.stats.last_mean_input_tps,
            expected.last_mean_input_tps,
        )
}

fn cluster_stats_match(
    actual: &RoutedClusterSnapshot,
    expected: &ExpectedClusterStats<'_>,
) -> bool {
    actual.cluster_id == expected.cluster_id
        && float_eq(
            actual.stats.last_mean_input_tps,
            expected.last_mean_input_tps,
        )
        && actual.stats.queued_input_size == expected.queued_input_size
        && actual.active_backend_count == expected.active_backend_count
}

fn priority_cluster_stats_match(
    actual: &RoutedClusterSnapshot,
    expected: &ExpectedPriorityClusterStats<'_>,
) -> bool {
    actual.cluster_id == expected.cluster_id
        && actual
            .stats
            .queue_time_estimate_ms_by_priority
            .get(&expected.priority)
            == Some(&expected.queue_time_estimate_ms)
}

fn float_eq(actual: f64, expected: f64) -> bool {
    (actual - expected).abs() < 1e-9
}

#[tokio::test]
async fn power_of_two_prefers_less_input_work() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_lb("test-sg-p2c", None);
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr_low, quic_url_low, _tunnel_low) = start_dummy_inst("p2c-model").await;
    let (inst_addr_high, quic_url_high, _tunnel_high) = start_dummy_inst("p2c-model").await;

    let mut reg_low = InferenceServerRegistrationClient::default();
    let channels_low = reg_low
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "inst-low-headroom".to_string(),
                cluster_id: String::new(),
                inference_server_url: quic_url_low,
                upstream_http_base_url: Some(format!("http://{inst_addr_low}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                // Skip calibration so both backends advertise Active immediately;
                // this test only exercises LB selection, not bringup.
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["p2c-model".to_string()],
        )
        .expect("registration failed");

    let mut reg_high = InferenceServerRegistrationClient::default();
    let channels_high = reg_high
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "inst-high-headroom".to_string(),
                cluster_id: String::new(),
                inference_server_url: quic_url_high,
                upstream_http_base_url: Some(format!("http://{inst_addr_high}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                // Skip calibration so both backends advertise Active immediately;
                // this test only exercises LB selection, not bringup.
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["p2c-model".to_string()],
        )
        .expect("registration failed");

    wait_for_routing(http_addr, "p2c-model", Duration::from_secs(5)).await;

    // More pending prompt work should lose when both clusters report the same service rate.
    channels_low
        .model_stats
        .send_async((
            "p2c-model".to_string(),
            CurrentModelStats {
                output_tps: 50.0,
                last_mean_input_tps: 1000.0,
                max_output_tps: 100.0,
                queue_size: 0,
                queued_input_size: 1_000,
                kv_cache_capacity_tokens: 0,
                kv_cache_used_tokens: 0,
                kv_cache_free_tokens: 0,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("send stats failed");

    // Less pending prompt work should win.
    channels_high
        .model_stats
        .send_async((
            "p2c-model".to_string(),
            CurrentModelStats {
                output_tps: 50.0,
                last_mean_input_tps: 1000.0,
                max_output_tps: 100.0,
                queue_size: 0,
                queued_input_size: 0,
                kv_cache_capacity_tokens: 0,
                kv_cache_used_tokens: 0,
                kv_cache_free_tokens: 0,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("send stats failed");

    let state = handle.state();
    wait_for_candidate_stats(
        &state,
        "p2c-model",
        &[
            ExpectedCandidateStats {
                inference_server_id: "inst-low-headroom",
                last_mean_input_tps: 1000.0,
            },
            ExpectedCandidateStats {
                inference_server_id: "inst-high-headroom",
                last_mean_input_tps: 1000.0,
            },
        ],
        Duration::from_secs(15),
    )
    .await;

    // With exactly 2 candidates, p2c samples both and picks the lower work-time candidate.
    assert_all_probes_routed_to(http_addr, "p2c-model", "req-p2c", "inst-high-headroom", 20).await;

    reg_low.stop();
    reg_high.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn input_work_admission_rejects_overloaded_pool_and_registered_unavailable_model() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    write!(
        tmp_file,
        r#"{{"models": {{"admission-model": {{"algorithm": "power-of-two", "max_input_work_seconds": 0.5}}}}}}"#
    )
    .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-input-work-admission", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");
    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("admission-model").await;

    let mut reg = InferenceServerRegistrationClient::default();
    let channels = reg
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "admission-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: quic_url,
                upstream_http_base_url: Some(format!("http://{inst_addr}")),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["admission-model".to_string()],
        )
        .expect("registration failed");
    channels
        .model_stats
        .send_async((
            "admission-model".to_string(),
            CurrentModelStats {
                last_mean_input_tps: 100.0,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("send stats failed");

    wait_for_routing(http_addr, "admission-model", Duration::from_secs(5)).await;
    channels
        .model_stats
        .send_async((
            "admission-model".to_string(),
            CurrentModelStats {
                last_mean_input_tps: 100.0,
                queued_input_size: 100,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("send overloaded stats failed");

    let client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "admission-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    let rejected = loop {
        let rejected = with_proxy_headers_input_tokens(
            client.post(&stargate_url),
            "admission-model",
            "req-input-work-admission",
            50,
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("admission request failed");
        if rejected.status() == reqwest::StatusCode::SERVICE_UNAVAILABLE {
            break rejected;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "input-work admission did not reject after overloaded stats arrived"
        );
        tokio::task::yield_now().await;
    };
    assert_eq!(rejected.status(), reqwest::StatusCode::SERVICE_UNAVAILABLE);
    assert_eq!(
        rejected
            .headers()
            .get("x-stargate-error-code")
            .and_then(|value| value.to_str().ok()),
        Some("input_work_limit_exceeded")
    );

    let missing = with_proxy_headers(
        client.post(&stargate_url),
        "missing-admission-model",
        "req-input-work-missing",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("missing-model request failed");
    assert_eq!(missing.status(), reqwest::StatusCode::NOT_FOUND);
    assert_eq!(
        missing
            .headers()
            .get("x-stargate-error-code")
            .and_then(|value| value.to_str().ok()),
        Some("no_eligible_candidates")
    );

    channels
        .status
        .send_async(InferenceServerStatus::Inactive)
        .await
        .expect("send inactive status failed");

    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    loop {
        let registered_but_unavailable = with_proxy_headers_input_tokens(
            client.post(&stargate_url),
            "admission-model",
            "req-input-work-registered-unavailable",
            50,
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("registered unavailable request failed");

        if registered_but_unavailable.status() == reqwest::StatusCode::SERVICE_UNAVAILABLE {
            assert_ne!(
                registered_but_unavailable
                    .headers()
                    .get("x-stargate-error-code")
                    .and_then(|value| value.to_str().ok()),
                Some("no_eligible_candidates")
            );
            break;
        }

        assert!(
            tokio::time::Instant::now() < deadline,
            "registered model with zero eligible candidates did not return 503"
        );
        tokio::task::yield_now().await;
    }

    reg.stop();

    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    loop {
        let unregistered_after_stop = with_proxy_headers_input_tokens(
            client.post(&stargate_url),
            "admission-model",
            "req-input-work-unregistered-after-stop",
            50,
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("unregistered missing-model request failed");

        if unregistered_after_stop.status() == reqwest::StatusCode::NOT_FOUND {
            assert_eq!(
                unregistered_after_stop
                    .headers()
                    .get("x-stargate-error-code")
                    .and_then(|value| value.to_str().ok()),
                Some("no_eligible_candidates")
            );
            break;
        }

        assert!(
            tokio::time::Instant::now() < deadline,
            "stopped registration did not return to 404 no_eligible_candidates"
        );
        tokio::task::yield_now().await;
    }

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn random_load_balancing_uses_all_instances() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    write!(tmp_file, r#"{{"default": "random", "models": {{}}}}"#).expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-random", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");

    let inst_ids = ["rand-a", "rand-b", "rand-c"];
    let mut reg_clients = Vec::new();
    let mut _tunnels = Vec::new();
    for inst_id in &inst_ids {
        let (inst_addr, quic_url, tunnel) = start_dummy_inst("rand-model").await;
        _tunnels.push(tunnel);
        let mut reg_client = InferenceServerRegistrationClient::default();
        let _channels = reg_client
            .start(
                InferenceServerRegistrationConfig {
                    seeds: vec![grpc_addr.to_string()],
                    inference_server_id: inst_id.to_string(),
                    cluster_id: String::new(),
                    inference_server_url: quic_url,
                    upstream_http_base_url: Some(format!("http://{inst_addr}")),
                    min_update_interval: Duration::from_millis(100),
                    status: InferenceServerStatus::Active,
                    reverse_tunnel: false,
                    bringup: BringupConfig::default(),
                    output_token_parser_factory: OutputTokenParserFactory::vllm(),
                    request_observation_tx: None,
                    request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                    metrics: None,
                    retry: pylon_lib::PylonRetryConfig::default(),
                    queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                    queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                    auth_token_provider: None,
                    quic_insecure: true,
                    tunnel_protocol: Default::default(),
                },
                vec!["rand-model".to_string()],
            )
            .expect("registration failed");
        reg_clients.push(reg_client);
    }

    // Wait for all 3 to register
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "rand-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    wait_for_inference_server_ids(
        http_addr,
        "rand-model",
        "req-rand-wait",
        3,
        Duration::from_secs(10),
        Duration::from_millis(100),
    )
    .await;

    // Send 30 requests and verify all 3 instances appear
    let mut seen_in_run = HashSet::new();
    for _ in 0..30 {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "rand-model",
            "req-rand-run",
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
        assert_eq!(resp.status(), 200);
        let id = resp
            .headers()
            .get("x-inference-server-id")
            .unwrap()
            .to_str()
            .unwrap()
            .to_string();
        seen_in_run.insert(id);
    }
    assert_eq!(
        seen_in_run.len(),
        3,
        "random LB should hit all 3 instances over 30 requests, saw: {seen_in_run:?}"
    );

    for client in &mut reg_clients {
        client.stop();
    }
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn power_of_two_uses_cluster_aggregated_metrics_and_backend_round_robin() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-p2c-clusters", None);
    let handle = runtime.start().await.expect("stargate failed to start");
    let state = handle.state();

    let (shared_a_addr, shared_a_quic_url, shared_a_tunnel) =
        start_dummy_inst("p2c-cluster-model").await;
    let (shared_b_addr, shared_b_quic_url, shared_b_tunnel) =
        start_dummy_inst("p2c-cluster-model").await;
    let (single_addr, single_quic_url, single_tunnel) = start_dummy_inst("p2c-cluster-model").await;
    let _tunnels = [shared_a_tunnel, shared_b_tunnel, single_tunnel];

    let make_registration_config =
        |inference_server_id: &str,
         cluster_id: &str,
         inference_server_url: String,
         upstream_http_base_url: String| {
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: inference_server_id.to_string(),
                cluster_id: cluster_id.to_string(),
                inference_server_url,
                upstream_http_base_url: Some(upstream_http_base_url),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            }
        };

    let mut reg_shared_a = InferenceServerRegistrationClient::default();
    let shared_a_channels = reg_shared_a
        .start(
            make_registration_config(
                "shared-backend-a",
                "shared-cluster",
                shared_a_quic_url,
                format!("http://{shared_a_addr}"),
            ),
            vec!["p2c-cluster-model".to_string()],
        )
        .expect("shared backend a registration failed");

    let mut reg_shared_b = InferenceServerRegistrationClient::default();
    let shared_b_channels = reg_shared_b
        .start(
            make_registration_config(
                "shared-backend-b",
                "shared-cluster",
                shared_b_quic_url,
                format!("http://{shared_b_addr}"),
            ),
            vec!["p2c-cluster-model".to_string()],
        )
        .expect("shared backend b registration failed");

    let mut reg_single = InferenceServerRegistrationClient::default();
    let single_channels = reg_single
        .start(
            make_registration_config(
                "single-backend",
                "single-cluster",
                single_quic_url,
                format!("http://{single_addr}"),
            ),
            vec!["p2c-cluster-model".to_string()],
        )
        .expect("single backend registration failed");

    wait_for_routing(http_addr, "p2c-cluster-model", Duration::from_secs(5)).await;

    let stats = |last_mean_input_tps: f64, queued_input_size: u64| CurrentModelStats {
        last_mean_input_tps,
        output_tps: 0.0,
        max_output_tps: 100.0,
        queue_size: u64::from(queued_input_size > 0),
        queued_input_size,
        kv_cache_capacity_tokens: 0,
        kv_cache_used_tokens: 0,
        kv_cache_free_tokens: 0,
        ..CurrentModelStats::default()
    };

    // Phase 1: shared-cluster backend capacity and pending prompt work both add up.
    // - shared cluster: 260 queued tokens / 200 last_mean_input_tps => 1.3s
    // - single cluster: 120 queued tokens / 100 last_mean_input_tps => 1.2s
    shared_a_channels
        .model_stats
        .send_async(("p2c-cluster-model".to_string(), stats(100.0, 130)))
        .await
        .expect("send shared-a stats failed");
    shared_b_channels
        .model_stats
        .send_async(("p2c-cluster-model".to_string(), stats(100.0, 130)))
        .await
        .expect("send shared-b stats failed");
    single_channels
        .model_stats
        .send_async(("p2c-cluster-model".to_string(), stats(100.0, 120)))
        .await
        .expect("send single stats failed");

    wait_for_cluster_stats(
        &state,
        "p2c-cluster-model",
        &[
            ExpectedClusterStats {
                cluster_id: "shared-cluster",
                last_mean_input_tps: 200.0,
                queued_input_size: 260,
                active_backend_count: 2,
            },
            ExpectedClusterStats {
                cluster_id: "single-cluster",
                last_mean_input_tps: 100.0,
                queued_input_size: 120,
                active_backend_count: 1,
            },
        ],
        Duration::from_secs(15),
    )
    .await;
    assert_all_probes_routed_to(
        http_addr,
        "p2c-cluster-model",
        "req-p2c-cluster-phase1-assert",
        "single-backend",
        8,
    )
    .await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "p2c-cluster-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    for i in 0..5 {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "p2c-cluster-model",
            &format!("req-p2c-cluster-phase1-cluster-{i}"),
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("phase1 request failed");
        assert_eq!(resp.status(), 200);
        assert_eq!(
            resp.headers()
                .get("x-stargate-cluster-id")
                .expect("missing x-stargate-cluster-id")
                .to_str()
                .unwrap(),
            "single-cluster"
        );
        assert_eq!(
            resp.headers()
                .get("x-inference-server-id")
                .expect("missing x-inference-server-id")
                .to_str()
                .unwrap(),
            "single-backend"
        );
        let _ = tokio::time::timeout(Duration::from_secs(15), resp.bytes()).await;
    }

    // Phase 2: move load to single backend so cluster selection flips to shared-cluster.
    shared_a_channels
        .model_stats
        .send_async(("p2c-cluster-model".to_string(), stats(100.0, 0)))
        .await
        .expect("send shared-a phase2 stats failed");
    shared_b_channels
        .model_stats
        .send_async(("p2c-cluster-model".to_string(), stats(100.0, 0)))
        .await
        .expect("send shared-b phase2 stats failed");
    single_channels
        .model_stats
        .send_async(("p2c-cluster-model".to_string(), stats(100.0, 120)))
        .await
        .expect("send single phase2 stats failed");

    wait_for_cluster_stats(
        &state,
        "p2c-cluster-model",
        &[
            ExpectedClusterStats {
                cluster_id: "shared-cluster",
                last_mean_input_tps: 200.0,
                queued_input_size: 0,
                active_backend_count: 2,
            },
            ExpectedClusterStats {
                cluster_id: "single-cluster",
                last_mean_input_tps: 100.0,
                queued_input_size: 120,
                active_backend_count: 1,
            },
        ],
        Duration::from_secs(15),
    )
    .await;

    let deadline = tokio::time::Instant::now() + Duration::from_secs(15);
    let mut batch = 0usize;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        batch += 1;
        let mut seen_backends = HashSet::new();
        let mut all_success = true;
        let mut all_shared_cluster = true;

        for i in 0..8 {
            let resp = with_proxy_headers(
                http_client.post(&stargate_url),
                "p2c-cluster-model",
                &format!("req-p2c-cluster-phase2-b{batch}-i{i}"),
            )
            .header("content-type", "application/json")
            .json(&body)
            .send()
            .await;
            let Ok(resp) = resp else {
                all_success = false;
                continue;
            };

            let status = resp.status();
            let cluster_id = resp
                .headers()
                .get("x-stargate-cluster-id")
                .and_then(|v| v.to_str().ok())
                .map(str::to_owned);
            let backend_id = resp
                .headers()
                .get("x-inference-server-id")
                .and_then(|v| v.to_str().ok())
                .map(str::to_owned);
            let _ = tokio::time::timeout(Duration::from_secs(15), resp.bytes()).await;

            if !status.is_success() {
                all_success = false;
                continue;
            }
            if cluster_id.as_deref() != Some("shared-cluster") {
                all_shared_cluster = false;
            }
            if let Some(backend_id) = backend_id {
                seen_backends.insert(backend_id);
            }
        }

        if all_success
            && all_shared_cluster
            && seen_backends.contains("shared-backend-a")
            && seen_backends.contains("shared-backend-b")
        {
            break;
        }

        if tokio::time::Instant::now() >= deadline {
            panic!(
                "expected shared-cluster routing with both backends seen; got all_success={all_success}, all_shared_cluster={all_shared_cluster}, seen_backends={seen_backends:?}"
            );
        }
        poll.tick().await;
    }

    reg_shared_a.stop();
    reg_shared_b.stop();
    reg_single.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn groq_multiregion_load_balancing_prefers_lower_estimated_ttft() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    write!(
        tmp_file,
        r#"{{"default": "power-of-two", "models": {{"multiregion-model": "groq-multiregion"}}}}"#
    )
    .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-multiregion", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");

    let insts = [
        ("multiregion-fast", 200.0_f64, 0_u64),
        ("multiregion-slow", 10.0_f64, 0_u64),
    ];
    let mut reg_clients = Vec::new();
    let mut _tunnels = Vec::new();
    for (inst_id, last_mean_input_tps, queued_input_size) in insts {
        let (inst_addr, quic_url, tunnel) = start_dummy_inst("multiregion-model").await;
        _tunnels.push(tunnel);
        let mut reg_client = InferenceServerRegistrationClient::default();
        let update_channels = reg_client
            .start(
                InferenceServerRegistrationConfig {
                    seeds: vec![grpc_addr.to_string()],
                    inference_server_id: inst_id.to_string(),
                    cluster_id: String::new(),
                    inference_server_url: quic_url,
                    upstream_http_base_url: Some(format!("http://{inst_addr}")),
                    min_update_interval: Duration::from_millis(100),
                    status: InferenceServerStatus::Active,
                    reverse_tunnel: false,
                    // Skip calibration so the test controls the registered
                    // stats directly instead of racing client-side calibration
                    // updates.
                    bringup: BringupConfig {
                        enabled: false,
                        ..BringupConfig::default()
                    },
                    output_token_parser_factory: OutputTokenParserFactory::vllm(),
                    request_observation_tx: None,
                    request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                    metrics: None,
                    retry: pylon_lib::PylonRetryConfig::default(),
                    queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                    queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                    auth_token_provider: None,
                    quic_insecure: true,
                    tunnel_protocol: Default::default(),
                },
                vec!["multiregion-model".to_string()],
            )
            .expect("registration failed");
        update_channels
            .model_stats
            .send_async((
                "multiregion-model".to_string(),
                CurrentModelStats {
                    output_tps: 0.0,
                    last_mean_input_tps,
                    max_output_tps: 1000.0,
                    queue_size: u64::from(queued_input_size > 0),
                    queued_input_size,
                    kv_cache_capacity_tokens: 0,
                    kv_cache_used_tokens: 0,
                    kv_cache_free_tokens: 0,
                    total_query_input_size: queued_input_size,
                    ..CurrentModelStats::default()
                },
            ))
            .await
            .expect("failed to send groq-multiregion stats");
        reg_clients.push(reg_client);
    }

    wait_for_routing(http_addr, "multiregion-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "multiregion-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let mut stable_fast = false;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    for attempt in 0..60 {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "multiregion-model",
            &format!("req-multiregion-{attempt}"),
        )
        .header("content-type", "application/json")
        .header("x-input-tokens", "10")
        .json(&body)
        .send()
        .await
        .expect("request failed");

        if resp.status() == 200 {
            let chosen = resp
                .headers()
                .get("x-inference-server-id")
                .expect("missing x-inference-server-id")
                .to_str()
                .unwrap()
                .to_string();
            if chosen == "multiregion-fast" {
                stable_fast = true;
                break;
            }
        }

        poll.tick().await;
    }

    assert!(
        stable_fast,
        "expected groq-multiregion to prefer lower estimated TTFT"
    );

    for client in &mut reg_clients {
        client.stop();
    }
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn groq_multiregion_waits_for_later_bucket_when_fastest_is_full() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    write!(
        tmp_file,
        r#"{{"default": "power-of-two", "models": {{"multiregion-wait-model": "groq-multiregion"}}}}"#
    )
    .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-multiregion-wait", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");

    let insts = [
        ("multiregion-fast-full", 0_u64, 1_u64, 1_u64),
        ("multiregion-slower-available", 10_u64, 0_u64, 1_u64),
    ];
    let mut reg_clients = Vec::new();
    let mut _tunnels = Vec::new();
    let mut update_channels = Vec::new();
    for (inst_id, _total_query_input_size, _num_running_queries, _max_engine_concurrency) in insts {
        let (inst_addr, quic_url, tunnel) = start_dummy_inst("multiregion-wait-model").await;
        _tunnels.push(tunnel);
        let mut reg_client = InferenceServerRegistrationClient::default();
        let channels = reg_client
            .start(
                InferenceServerRegistrationConfig {
                    seeds: vec![grpc_addr.to_string()],
                    inference_server_id: inst_id.to_string(),
                    cluster_id: String::new(),
                    inference_server_url: quic_url,
                    upstream_http_base_url: Some(format!("http://{inst_addr}")),
                    min_update_interval: Duration::from_millis(50),
                    status: InferenceServerStatus::Active,
                    reverse_tunnel: false,
                    bringup: BringupConfig {
                        enabled: false,
                        ..BringupConfig::default()
                    },
                    output_token_parser_factory: OutputTokenParserFactory::vllm(),
                    request_observation_tx: None,
                    request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                    metrics: None,
                    auth_token_provider: None,
                    quic_insecure: true,
                    tunnel_protocol: Default::default(),
                    retry: pylon_lib::PylonRetryConfig::default(),
                    queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                    queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                },
                vec!["multiregion-wait-model".to_string()],
            )
            .expect("registration failed");
        update_channels.push((inst_id, channels));
        reg_clients.push(reg_client);
    }

    for (inst_id, channels) in update_channels {
        let (total_query_input_size, num_running_queries, max_engine_concurrency) = insts
            .iter()
            .find_map(|(id, total, running, max_engine_concurrency)| {
                (*id == inst_id).then_some((*total, *running, *max_engine_concurrency))
            })
            .unwrap();
        channels
            .model_stats
            .send_async((
                "multiregion-wait-model".to_string(),
                CurrentModelStats {
                    output_tps: 0.0,
                    last_mean_input_tps: 100.0,
                    max_output_tps: 1000.0,
                    queue_size: u64::from(num_running_queries > 0),
                    queued_input_size: total_query_input_size,
                    num_running_queries,
                    max_engine_concurrency: Some(max_engine_concurrency),
                    total_query_input_size,
                    ..CurrentModelStats::default()
                },
            ))
            .await
            .expect("failed to send groq-multiregion wait stats");
    }

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "multiregion-wait-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let mut chose_slower = false;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    for attempt in 0..30 {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            "multiregion-wait-model",
            &format!("req-multiregion-wait-{attempt}"),
        )
        .header("content-type", "application/json")
        .header("x-max-wait-ms", "250")
        .json(&body)
        .send()
        .await
        .expect("request failed");

        if resp.status() == 200 {
            let chosen = resp
                .headers()
                .get("x-inference-server-id")
                .expect("missing x-inference-server-id")
                .to_str()
                .unwrap();
            assert_eq!(chosen, "multiregion-slower-available");
            chose_slower = true;
            break;
        }

        poll.tick().await;
    }

    assert!(
        chose_slower,
        "expected groq-multiregion to wait for a later TTFT bucket when the fastest backend is full"
    );

    for client in &mut reg_clients {
        client.stop();
    }
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn groq_multiregion_cache_affinity_prefers_stable_subset_then_falls_back() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    std::io::Write::write_all(
        &mut tmp_file,
        br#"{
            "default": "power-of-two",
            "models": {
                "multiregion-affinity-model": {
                    "algorithm": "groq-multiregion",
                    "seed": "test-seed",
                    "require_cache_affinity_key": true,
                    "cache_affinity_virtual_nodes": 8,
                    "cache_affinity_backend_selection_count": 1,
                    "n": 3
                }
            }
        }"#,
    )
    .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-multiregion-affinity", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");

    let insts = [
        "multiregion-affinity-a",
        "multiregion-affinity-b",
        "multiregion-affinity-c",
    ];
    let mut reg_clients = Vec::new();
    let mut _tunnels = Vec::new();
    let mut update_channels = Vec::new();
    for inst_id in insts {
        let (inst_addr, quic_url, tunnel) = start_dummy_inst("multiregion-affinity-model").await;
        _tunnels.push(tunnel);
        let mut reg_client = InferenceServerRegistrationClient::default();
        let channels = reg_client
            .start(
                InferenceServerRegistrationConfig {
                    seeds: vec![grpc_addr.to_string()],
                    inference_server_id: inst_id.to_string(),
                    cluster_id: String::new(),
                    inference_server_url: quic_url,
                    upstream_http_base_url: Some(format!("http://{inst_addr}")),
                    min_update_interval: Duration::from_millis(50),
                    status: InferenceServerStatus::Active,
                    reverse_tunnel: false,
                    bringup: BringupConfig {
                        enabled: false,
                        ..BringupConfig::default()
                    },
                    output_token_parser_factory: OutputTokenParserFactory::vllm(),
                    request_observation_tx: None,
                    request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                    metrics: None,
                    auth_token_provider: None,
                    quic_insecure: true,
                    tunnel_protocol: Default::default(),
                    retry: pylon_lib::PylonRetryConfig::default(),
                    queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                    queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                },
                vec!["multiregion-affinity-model".to_string()],
            )
            .expect("registration failed");
        channels
            .model_stats
            .send_async((
                "multiregion-affinity-model".to_string(),
                CurrentModelStats {
                    output_tps: 0.0,
                    last_mean_input_tps: 100.0,
                    max_output_tps: 1000.0,
                    queue_size: 0,
                    queued_input_size: 0,
                    num_running_queries: 0,
                    max_engine_concurrency: Some(100),
                    total_query_input_size: 0,
                    ..CurrentModelStats::default()
                },
            ))
            .await
            .expect("failed to send multiregion affinity stats");
        update_channels.push((inst_id.to_string(), channels));
        reg_clients.push(reg_client);
    }

    let affinity_key = "stable-prefix";
    wait_for_routing_with_cache_affinity(
        http_addr,
        "multiregion-affinity-model",
        affinity_key,
        Duration::from_secs(5),
    )
    .await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "multiregion-affinity-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let mut stable_choice = None;
    for attempt in 0..20 {
        let resp = with_proxy_headers_cache_affinity(
            http_client.post(&stargate_url),
            "multiregion-affinity-model",
            &format!("req-multiregion-affinity-stable-{attempt}"),
            affinity_key,
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
        assert_eq!(resp.status(), 200);
        let chosen = resp
            .headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id")
            .to_str()
            .unwrap()
            .to_string();
        let _ = tokio::time::timeout(Duration::from_secs(15), resp.bytes()).await;
        if let Some(stable_choice) = stable_choice.as_deref() {
            assert_eq!(chosen, stable_choice);
        } else {
            stable_choice = Some(chosen);
        }
    }

    let primary = stable_choice.expect("stable primary should be observed");
    let primary_channels = update_channels
        .iter()
        .find_map(|(inst_id, channels)| (inst_id == &primary).then_some(channels))
        .expect("primary backend should have update channel");
    primary_channels
        .model_stats
        .send_async((
            "multiregion-affinity-model".to_string(),
            CurrentModelStats {
                output_tps: 0.0,
                last_mean_input_tps: 100.0,
                max_output_tps: 1000.0,
                queue_size: 1,
                queued_input_size: 1,
                num_running_queries: 1,
                max_engine_concurrency: Some(1),
                total_query_input_size: 1,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("failed to mark primary full");

    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    let mut fallback_attempt = 0usize;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        fallback_attempt += 1;
        let resp = with_proxy_headers_cache_affinity(
            http_client.post(&stargate_url),
            "multiregion-affinity-model",
            &format!("req-multiregion-affinity-fallback-{fallback_attempt}"),
            affinity_key,
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
        if resp.status() == 200 {
            let chosen = resp
                .headers()
                .get("x-inference-server-id")
                .expect("missing x-inference-server-id")
                .to_str()
                .unwrap()
                .to_string();
            let _ = tokio::time::timeout(Duration::from_secs(15), resp.bytes()).await;
            if chosen != primary {
                break;
            }
        }
        if tokio::time::Instant::now() >= deadline {
            panic!("expected groq-multiregion affinity subset to fall back after {primary} filled");
        }
        poll.tick().await;
    }

    for client in &mut reg_clients {
        client.stop();
    }
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn groq_multiregion_requires_cache_affinity_key_when_configured() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    std::io::Write::write_all(
        &mut tmp_file,
        br#"{
            "default": "power-of-two",
            "models": {
                "multiregion-affinity-required-model": {
                    "algorithm": "groq-multiregion",
                    "seed": "test-seed",
                    "require_cache_affinity_key": true,
                    "cache_affinity_virtual_nodes": 8,
                    "cache_affinity_backend_selection_count": 1,
                    "n": 1
                }
            }
        }"#,
    )
    .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-multiregion-affinity-required", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) =
        start_dummy_inst("multiregion-affinity-required-model").await;
    let mut reg_client = InferenceServerRegistrationClient::default();
    let update_channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_addr.to_string()],
                inference_server_id: "multiregion-affinity-required-inst".to_string(),
                cluster_id: String::new(),
                inference_server_url: quic_url,
                upstream_http_base_url: Some(format!("http://{inst_addr}")),
                min_update_interval: Duration::from_millis(50),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
            },
            vec!["multiregion-affinity-required-model".to_string()],
        )
        .expect("registration failed");
    update_channels
        .model_stats
        .send_async((
            "multiregion-affinity-required-model".to_string(),
            CurrentModelStats {
                output_tps: 0.0,
                last_mean_input_tps: 100.0,
                max_output_tps: 1000.0,
                queue_size: 0,
                queued_input_size: 0,
                num_running_queries: 0,
                max_engine_concurrency: Some(100),
                total_query_input_size: 0,
                ..CurrentModelStats::default()
            },
        ))
        .await
        .expect("failed to send multiregion affinity-required stats");

    wait_for_routing_with_cache_affinity(
        http_addr,
        "multiregion-affinity-required-model",
        "affinity-required-key",
        Duration::from_secs(5),
    )
    .await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "multiregion-affinity-required-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let missing_resp = with_proxy_headers(
        http_client.post(&stargate_url),
        "multiregion-affinity-required-model",
        "req-multiregion-affinity-required-missing",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("missing affinity request failed");
    assert_eq!(
        missing_resp.status(),
        400,
        "configured model should reject missing x-cache-affinity-key"
    );
    let _ = missing_resp.bytes().await;

    let present_resp = with_proxy_headers_cache_affinity(
        http_client.post(&stargate_url),
        "multiregion-affinity-required-model",
        "req-multiregion-affinity-required-present",
        "affinity-required-key",
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("present affinity request failed");
    assert_eq!(present_resp.status(), 200);
    assert_eq!(
        present_resp
            .headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id")
            .to_str()
            .unwrap(),
        "multiregion-affinity-required-inst"
    );
    let _ = present_resp.bytes().await;

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn groq_multiregion_priority_header_uses_matching_queue_estimate() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    std::io::Write::write_all(
        &mut tmp_file,
        br#"{
            "default": "power-of-two",
            "models": {
                "multiregion-priority-model": {
                    "algorithm": "groq-multiregion",
                    "n": 2
                }
            }
        }"#,
    )
    .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-multiregion-priority", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");

    let insts = [
        (
            "multiregion-priority-specific-low",
            100_u64,
            HashMap::from([(4_u32, 5_u64)]),
        ),
        (
            "multiregion-priority-specific-high",
            0_u64,
            HashMap::from([(4_u32, 500_u64)]),
        ),
    ];
    let mut reg_clients = Vec::new();
    let mut _tunnels = Vec::new();
    for (inst_id, total_query_input_size, priority_queue_estimates) in insts {
        let (inst_addr, quic_url, tunnel) = start_dummy_inst("multiregion-priority-model").await;
        _tunnels.push(tunnel);
        let mut reg_client = InferenceServerRegistrationClient::default();
        let update_channels = reg_client
            .start(
                InferenceServerRegistrationConfig {
                    seeds: vec![grpc_addr.to_string()],
                    inference_server_id: inst_id.to_string(),
                    cluster_id: String::new(),
                    inference_server_url: quic_url,
                    upstream_http_base_url: Some(format!("http://{inst_addr}")),
                    min_update_interval: Duration::from_millis(50),
                    status: InferenceServerStatus::Active,
                    reverse_tunnel: false,
                    bringup: BringupConfig {
                        enabled: false,
                        ..BringupConfig::default()
                    },
                    output_token_parser_factory: OutputTokenParserFactory::vllm(),
                    request_observation_tx: None,
                    request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                    metrics: None,
                    retry: pylon_lib::PylonRetryConfig::default(),
                    queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                    queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                    auth_token_provider: None,
                    quic_insecure: true,
                    tunnel_protocol: Default::default(),
                },
                vec!["multiregion-priority-model".to_string()],
            )
            .expect("registration failed");
        update_channels
            .model_stats
            .send_async((
                "multiregion-priority-model".to_string(),
                CurrentModelStats {
                    output_tps: 0.0,
                    last_mean_input_tps: 100.0,
                    max_output_tps: 1000.0,
                    queue_size: u64::from(total_query_input_size > 0),
                    queued_input_size: total_query_input_size,
                    num_running_queries: u64::from(total_query_input_size > 0),
                    max_engine_concurrency: Some(100),
                    total_query_input_size,
                    queue_time_estimate_ms_by_priority: Some(priority_queue_estimates),
                    ..CurrentModelStats::default()
                },
            ))
            .await
            .expect("failed to send multiregion priority stats");
        reg_clients.push(reg_client);
    }

    let state = handle.state();
    wait_for_priority_cluster_stats(
        &state,
        "multiregion-priority-model",
        &[
            ExpectedPriorityClusterStats {
                cluster_id: "multiregion-priority-specific-low",
                priority: 4,
                queue_time_estimate_ms: 5,
            },
            ExpectedPriorityClusterStats {
                cluster_id: "multiregion-priority-specific-high",
                priority: 4,
                queue_time_estimate_ms: 500,
            },
        ],
        Duration::from_secs(5),
    )
    .await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "multiregion-priority-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    for attempt in 0..4 {
        let resp = with_proxy_headers_input_tokens(
            http_client.post(&stargate_url),
            "multiregion-priority-model",
            &format!("req-multiregion-priority-assert-{attempt}"),
            0,
        )
        .header("content-type", "application/json")
        .header("x-priority", "4")
        .json(&body)
        .send()
        .await
        .expect("priority request failed");
        assert_eq!(resp.status(), 200);
        assert_eq!(
            resp.headers()
                .get("x-inference-server-id")
                .expect("missing x-inference-server-id")
                .to_str()
                .unwrap(),
            "multiregion-priority-specific-low",
            "x-priority=4 should choose the backend with the lower priority-specific queue estimate"
        );
        let _ = resp.bytes().await;
    }

    let resp = with_proxy_headers_input_tokens(
        http_client.post(&stargate_url),
        "multiregion-priority-model",
        "req-multiregion-priority-no-header",
        0,
    )
    .header("content-type", "application/json")
    .json(&body)
    .send()
    .await
    .expect("non-priority request failed");
    assert_eq!(resp.status(), 200);
    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id")
            .to_str()
            .unwrap(),
        "multiregion-priority-specific-high",
        "without x-priority the aggregate queue estimate should still choose the lower aggregate queue"
    );
    let _ = resp.bytes().await;

    for client in &mut reg_clients {
        client.stop();
    }
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn pulsar_routes_same_affinity_key_consistently() {
    init_crypto();

    let mut tmp_file = tempfile::NamedTempFile::new().expect("failed to create temp file");
    std::io::Write::write_all(
        &mut tmp_file,
        br#"{
            "default": "power-of-two",
            "models": {
                "pulsar-model": {
                    "algorithm": "pulsar",
                    "seed": "test-seed",
                    "require_cache_affinity_key": true,
                    "require_input_tokens": true
                }
            }
        }"#,
    )
    .expect("failed to write config");
    let config_path = tmp_file.path().to_str().unwrap().to_string();

    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_lb("test-sg-pulsar", Some(config_path));
    let handle = runtime.start().await.expect("stargate failed to start");

    let insts = [
        ("pulsar-a", 1000.0),
        ("pulsar-b", 700.0),
        ("pulsar-c", 400.0),
    ];
    let mut reg_clients = Vec::new();
    let mut _tunnels = Vec::new();
    for (inst_id, last_mean_input_tps) in &insts {
        let (inst_addr, quic_url, tunnel) = start_dummy_inst("pulsar-model").await;
        _tunnels.push(tunnel);
        let mut reg_client = InferenceServerRegistrationClient::default();
        let update_channels = reg_client
            .start(
                InferenceServerRegistrationConfig {
                    seeds: vec![grpc_addr.to_string()],
                    inference_server_id: inst_id.to_string(),
                    cluster_id: String::new(),
                    inference_server_url: quic_url,
                    upstream_http_base_url: Some(format!("http://{inst_addr}")),
                    min_update_interval: Duration::from_millis(100),
                    status: InferenceServerStatus::Active,
                    reverse_tunnel: false,
                    bringup: BringupConfig::default(),
                    output_token_parser_factory: OutputTokenParserFactory::vllm(),
                    request_observation_tx: None,
                    request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                    metrics: None,
                    retry: pylon_lib::PylonRetryConfig::default(),
                    queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                    queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                    auth_token_provider: None,
                    quic_insecure: true,
                    tunnel_protocol: Default::default(),
                },
                vec!["pulsar-model".to_string()],
            )
            .expect("registration failed");
        update_channels
            .model_stats
            .send_async((
                "pulsar-model".to_string(),
                CurrentModelStats {
                    output_tps: 0.0,
                    last_mean_input_tps: *last_mean_input_tps,
                    max_output_tps: 1000.0,
                    queue_size: 0,
                    queued_input_size: 0,
                    kv_cache_capacity_tokens: 4096,
                    kv_cache_used_tokens: 0,
                    kv_cache_free_tokens: 4096,
                    ..CurrentModelStats::default()
                },
            ))
            .await
            .expect("failed to send pulsar stats");
        reg_clients.push(reg_client);
    }

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": "pulsar-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });

    let affinity_key = "affinity-stable";
    let mut stable_choice: Option<String> = None;
    let mut stable_run = 0usize;
    let mut poll = tokio::time::interval(Duration::from_millis(50));
    for attempt in 0..100 {
        let resp = with_proxy_headers_cache_affinity(
            http_client.post(&stargate_url),
            "pulsar-model",
            &format!("req-pulsar-stabilize-{attempt}"),
            affinity_key,
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
        if resp.status() != 200 {
            poll.tick().await;
            continue;
        }
        let chosen = resp
            .headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id")
            .to_str()
            .unwrap()
            .to_string();
        if stable_choice.as_deref() == Some(chosen.as_str()) {
            stable_run += 1;
        } else {
            stable_choice = Some(chosen);
            stable_run = 1;
        }
        if stable_run >= 5 {
            break;
        }
        poll.tick().await;
    }
    assert!(
        stable_run >= 5,
        "same pulsar affinity key never stabilized within the polling window"
    );

    let mut chosen_ids = HashSet::new();
    for attempt in 0..20 {
        let resp = with_proxy_headers_cache_affinity(
            http_client.post(&stargate_url),
            "pulsar-model",
            &format!("req-pulsar-stable-{attempt}"),
            affinity_key,
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
        assert_eq!(resp.status(), 200);
        let chosen = resp
            .headers()
            .get("x-inference-server-id")
            .expect("missing x-inference-server-id")
            .to_str()
            .unwrap()
            .to_string();
        chosen_ids.insert(chosen);
    }

    assert_eq!(
        chosen_ids.len(),
        1,
        "same pulsar affinity key should route consistently, saw: {chosen_ids:?}"
    );

    for client in &mut reg_clients {
        client.stop();
    }
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

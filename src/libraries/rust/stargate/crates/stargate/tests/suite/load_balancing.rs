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
use std::fmt::Debug;
use std::future::Future;
use std::io::Write;
use std::net::SocketAddr;
use std::time::Duration;

use crate::common::{
    assert_all_probes_routed_to, direct_registration_config, init_crypto,
    make_stargate_runtime_with_lb, start_dummy_inst, wait_for_inference_server_ids,
    wait_for_routing, wait_for_routing_with_cache_affinity, with_proxy_headers,
    with_proxy_headers_cache_affinity, with_proxy_headers_input_tokens,
};
use pylon_lib::{
    CurrentModelStats, InferenceServerRegistrationClient, InferenceServerRegistrationConfig,
    PylonRuntimeState, QuicHttpTunnelHandle,
};
use stargate::routing::{RoutedClusterSnapshot, RoutedInferenceServerSnapshot, RoutingTargetKey};
use stargate::runtime::StargateHandle;
use stargate::test_support::StargateState;
use stargate_proto::pb::InferenceServerStatus;

struct RunningStargate {
    grpc_addr: SocketAddr,
    http_addr: SocketAddr,
    handle: StargateHandle,
    _config: Option<tempfile::NamedTempFile>,
}

impl RunningStargate {
    async fn start(id: &str, config: Option<&str>) -> Self {
        init_crypto();
        let config = config.map(|contents| {
            let mut file = tempfile::NamedTempFile::new().expect("failed to create temp file");
            file.write_all(contents.as_bytes())
                .expect("failed to write config");
            file
        });
        let config_path = config
            .as_ref()
            .map(|file| file.path().to_string_lossy().into_owned());
        let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_lb(id, config_path);
        let handle = runtime.start().await.expect("stargate failed to start");
        Self {
            grpc_addr,
            http_addr,
            handle,
            _config: config,
        }
    }

    async fn shutdown(self) {
        self.handle.begin_shutdown();
        assert!(
            self.handle.wait_for_shutdown(Duration::from_secs(5)).await,
            "stargate runtime did not shut down"
        );
    }
}

struct RegisteredBackend {
    registration: InferenceServerRegistrationClient,
    runtime: PylonRuntimeState,
    model: String,
    _tunnel: QuicHttpTunnelHandle,
}

impl RegisteredBackend {
    async fn start(
        grpc_addr: SocketAddr,
        model: &str,
        inference_server_id: &str,
        configure: impl FnOnce(&mut InferenceServerRegistrationConfig),
    ) -> Self {
        let (upstream_addr, inference_server_url, tunnel) = start_dummy_inst(model).await;
        let runtime = PylonRuntimeState::new(InferenceServerStatus::Active, &[model.to_string()]);
        let mut config = direct_registration_config(
            vec![grpc_addr.to_string()],
            inference_server_id,
            inference_server_url,
            format!("http://{upstream_addr}"),
            runtime.clone(),
        );
        configure(&mut config);
        let mut registration = InferenceServerRegistrationClient::default();
        registration.start(config).expect("registration failed");
        Self {
            registration,
            runtime,
            model: model.to_string(),
            _tunnel: tunnel,
        }
    }

    async fn active(grpc_addr: SocketAddr, model: &str, inference_server_id: &str) -> Self {
        Self::start(grpc_addr, model, inference_server_id, |_| {}).await
    }

    async fn active_with_fast_updates(
        grpc_addr: SocketAddr,
        model: &str,
        inference_server_id: &str,
    ) -> Self {
        Self::start(grpc_addr, model, inference_server_id, |config| {
            config.min_update_interval = Duration::from_millis(50);
        })
        .await
    }

    async fn active_in_cluster(
        grpc_addr: SocketAddr,
        model: &str,
        inference_server_id: &str,
        cluster_id: &str,
    ) -> Self {
        Self::start(grpc_addr, model, inference_server_id, |config| {
            config.cluster_id = cluster_id.to_string();
        })
        .await
    }

    fn set_stats(&self, stats: CurrentModelStats) {
        self.runtime.set_model_stats(self.model.clone(), stats);
    }

    fn stop(&mut self) {
        self.registration.stop();
    }
}

fn stop_backends(backends: &mut [RegisteredBackend]) {
    for backend in backends {
        backend.stop();
    }
}

fn chat_body(model: &str) -> serde_json::Value {
    serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    })
}

fn chat_url(http_addr: SocketAddr) -> String {
    format!("http://{http_addr}/v1/chat/completions")
}

struct ChatRequests {
    client: reqwest::Client,
    url: String,
    model: String,
    body: serde_json::Value,
}

impl ChatRequests {
    fn new(http_addr: SocketAddr, model: &str) -> Self {
        Self {
            client: reqwest::Client::new(),
            url: chat_url(http_addr),
            model: model.to_string(),
            body: chat_body(model),
        }
    }

    fn request(&self, request_id: &str) -> reqwest::RequestBuilder {
        self.finish(with_proxy_headers(
            self.client.post(&self.url),
            &self.model,
            request_id,
        ))
    }

    fn with_input_tokens(&self, request_id: &str, input_tokens: u64) -> reqwest::RequestBuilder {
        self.finish(with_proxy_headers_input_tokens(
            self.client.post(&self.url),
            &self.model,
            request_id,
            input_tokens,
        ))
    }

    fn with_affinity(&self, request_id: &str, affinity_key: &str) -> reqwest::RequestBuilder {
        self.finish(with_proxy_headers_cache_affinity(
            self.client.post(&self.url),
            &self.model,
            request_id,
            affinity_key,
        ))
    }

    fn finish(&self, request: reqwest::RequestBuilder) -> reqwest::RequestBuilder {
        request
            .header("content-type", "application/json")
            .json(&self.body)
    }
}

fn response_header<'a>(response: &'a reqwest::Response, name: &str) -> &'a str {
    response
        .headers()
        .get(name)
        .unwrap_or_else(|| panic!("missing {name}"))
        .to_str()
        .unwrap_or_else(|_| panic!("invalid {name}"))
}

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
    wait_for_stats(
        model_id,
        "candidates",
        expected,
        timeout,
        || state.candidates_for_target(&target),
        candidate_stats_match,
        |candidate| {
            format!(
                "{} last_mean_input_tps={}",
                candidate.inference_server_id, candidate.stats.last_mean_input_tps
            )
        },
    )
    .await;
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
    wait_for_stats(
        model_id,
        "clusters",
        expected,
        timeout,
        || state.cluster_candidates_for_target(&target),
        cluster_stats_match,
        |cluster| {
            format!(
                "{} last_mean_input_tps={} queued_input_size={} active_backend_count={}",
                cluster.cluster_id,
                cluster.stats.last_mean_input_tps,
                cluster.stats.queued_input_size,
                cluster.active_backend_count
            )
        },
    )
    .await;
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
    wait_for_stats(
        model_id,
        "priority clusters",
        expected,
        timeout,
        || state.cluster_candidates_for_target(&target),
        priority_cluster_stats_match,
        |cluster| {
            format!(
                "{} queue_time_estimate_ms_by_priority={:?}",
                cluster.cluster_id, cluster.stats.queue_time_estimate_ms_by_priority,
            )
        },
    )
    .await;
}

async fn wait_for_stats<E, A, Load, Loaded, Matches, Describe>(
    model_id: &str,
    subject: &str,
    expected: &[E],
    timeout: Duration,
    mut load: Load,
    matches: Matches,
    describe: Describe,
) where
    E: Debug,
    Load: FnMut() -> Loaded,
    Loaded: Future<Output = Vec<A>>,
    Matches: Fn(&A, &E) -> bool,
    Describe: Fn(&A) -> String,
{
    let deadline = tokio::time::Instant::now() + timeout;
    let mut interval = tokio::time::interval(Duration::from_millis(10));
    loop {
        let actual = load().await;
        if actual.len() == expected.len()
            && expected
                .iter()
                .all(|expected| actual.iter().any(|actual| matches(actual, expected)))
        {
            return;
        }
        if tokio::time::Instant::now() >= deadline {
            let last_seen = actual.iter().map(&describe).collect::<Vec<_>>();
            panic!(
                "model '{model_id}' {subject} did not reach expected stats within {}s; expected {expected:?}, last_seen {last_seen:?}",
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
    let stargate = RunningStargate::start("test-sg-p2c", None).await;
    let mut low =
        RegisteredBackend::active(stargate.grpc_addr, "p2c-model", "inst-low-headroom").await;
    let mut high =
        RegisteredBackend::active(stargate.grpc_addr, "p2c-model", "inst-high-headroom").await;

    wait_for_routing(stargate.http_addr, "p2c-model", Duration::from_secs(5)).await;

    // More pending prompt work should lose when both clusters report the same service rate.
    low.set_stats(CurrentModelStats {
        output_tps: 50.0,
        last_mean_input_tps: 1000.0,
        max_output_tps: 100.0,
        queue_size: 0,
        queued_input_size: 1_000,
        kv_cache_capacity_tokens: 0,
        kv_cache_used_tokens: 0,
        kv_cache_free_tokens: 0,
        ..CurrentModelStats::default()
    });

    // Less pending prompt work should win.
    high.set_stats(CurrentModelStats {
        output_tps: 50.0,
        last_mean_input_tps: 1000.0,
        max_output_tps: 100.0,
        queue_size: 0,
        queued_input_size: 0,
        kv_cache_capacity_tokens: 0,
        kv_cache_used_tokens: 0,
        kv_cache_free_tokens: 0,
        ..CurrentModelStats::default()
    });

    let state = stargate.handle.state();
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
    assert_all_probes_routed_to(
        stargate.http_addr,
        "p2c-model",
        "req-p2c",
        "inst-high-headroom",
        20,
    )
    .await;

    low.stop();
    high.stop();
    stargate.shutdown().await;
}

#[tokio::test]
async fn input_work_admission_rejects_overloaded_pool_and_registered_unavailable_model() {
    let stargate = RunningStargate::start(
        "test-sg-input-work-admission",
        Some(
            r#"{"models": {"admission-model": {"algorithm": "power-of-two", "max_input_work_seconds": 0.5}}}"#,
        ),
    )
    .await;
    let mut backend =
        RegisteredBackend::active(stargate.grpc_addr, "admission-model", "admission-inst").await;
    backend.set_stats(CurrentModelStats {
        last_mean_input_tps: 100.0,
        ..CurrentModelStats::default()
    });

    wait_for_routing(
        stargate.http_addr,
        "admission-model",
        Duration::from_secs(5),
    )
    .await;
    backend.set_stats(CurrentModelStats {
        last_mean_input_tps: 100.0,
        queued_input_size: 100,
        ..CurrentModelStats::default()
    });

    let chat = ChatRequests::new(stargate.http_addr, "admission-model");
    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    let rejected = loop {
        let rejected = chat
            .with_input_tokens("req-input-work-admission", 50)
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
        chat.client.post(&chat.url),
        "missing-admission-model",
        "req-input-work-missing",
    )
    .header("content-type", "application/json")
    .json(&chat.body)
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

    backend.runtime.set_status(InferenceServerStatus::Inactive);

    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    loop {
        let registered_but_unavailable = chat
            .with_input_tokens("req-input-work-registered-unavailable", 50)
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

    backend.stop();

    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    loop {
        let unregistered_after_stop = chat
            .with_input_tokens("req-input-work-unregistered-after-stop", 50)
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

    stargate.shutdown().await;
}

#[tokio::test]
async fn random_load_balancing_uses_all_instances() {
    let stargate = RunningStargate::start(
        "test-sg-random",
        Some(r#"{"default": "random", "models": {}}"#),
    )
    .await;

    let inst_ids = ["rand-a", "rand-b", "rand-c"];
    let mut backends = Vec::new();
    for inst_id in &inst_ids {
        backends.push(
            RegisteredBackend::start(stargate.grpc_addr, "rand-model", inst_id, |_| {}).await,
        );
    }

    // Wait for all 3 to register
    let chat = ChatRequests::new(stargate.http_addr, "rand-model");

    wait_for_inference_server_ids(
        stargate.http_addr,
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
        let resp = chat
            .request("req-rand-run")
            .send()
            .await
            .expect("request failed");
        assert_eq!(resp.status(), 200);
        let id = response_header(&resp, "x-inference-server-id").to_string();
        seen_in_run.insert(id);
    }
    assert_eq!(
        seen_in_run.len(),
        3,
        "random LB should hit all 3 instances over 30 requests, saw: {seen_in_run:?}"
    );

    stop_backends(&mut backends);
    stargate.shutdown().await;
}

#[tokio::test]
async fn power_of_two_uses_cluster_aggregated_metrics_and_backend_round_robin() {
    let stargate = RunningStargate::start("test-sg-p2c-clusters", None).await;
    let state = stargate.handle.state();
    let mut shared_a = RegisteredBackend::active_in_cluster(
        stargate.grpc_addr,
        "p2c-cluster-model",
        "shared-backend-a",
        "shared-cluster",
    )
    .await;
    let mut shared_b = RegisteredBackend::active_in_cluster(
        stargate.grpc_addr,
        "p2c-cluster-model",
        "shared-backend-b",
        "shared-cluster",
    )
    .await;
    let mut single = RegisteredBackend::active_in_cluster(
        stargate.grpc_addr,
        "p2c-cluster-model",
        "single-backend",
        "single-cluster",
    )
    .await;

    wait_for_routing(
        stargate.http_addr,
        "p2c-cluster-model",
        Duration::from_secs(5),
    )
    .await;

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
    shared_a.set_stats(stats(100.0, 130));
    shared_b.set_stats(stats(100.0, 130));
    single.set_stats(stats(100.0, 120));

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
        stargate.http_addr,
        "p2c-cluster-model",
        "req-p2c-cluster-phase1-assert",
        "single-backend",
        8,
    )
    .await;

    let chat = ChatRequests::new(stargate.http_addr, "p2c-cluster-model");
    for i in 0..5 {
        let resp = chat
            .request(&format!("req-p2c-cluster-phase1-cluster-{i}"))
            .send()
            .await
            .expect("phase1 request failed");
        assert_eq!(resp.status(), 200);
        assert_eq!(
            response_header(&resp, "x-stargate-cluster-id"),
            "single-cluster"
        );
        assert_eq!(
            response_header(&resp, "x-inference-server-id"),
            "single-backend"
        );
        let _ = tokio::time::timeout(Duration::from_secs(15), resp.bytes()).await;
    }

    // Phase 2: move load to single backend so cluster selection flips to shared-cluster.
    shared_a.set_stats(stats(100.0, 0));
    shared_b.set_stats(stats(100.0, 0));
    single.set_stats(stats(100.0, 120));

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
            let resp = chat
                .request(&format!("req-p2c-cluster-phase2-b{batch}-i{i}"))
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

    shared_a.stop();
    shared_b.stop();
    single.stop();
    stargate.shutdown().await;
}

#[tokio::test]
async fn groq_multiregion_load_balancing_prefers_lower_estimated_ttft() {
    let stargate = RunningStargate::start(
        "test-sg-multiregion",
        Some(r#"{"default": "power-of-two", "models": {"multiregion-model": "groq-multiregion"}}"#),
    )
    .await;

    let insts = [
        ("multiregion-fast", 200.0_f64, 0_u64),
        ("multiregion-slow", 10.0_f64, 0_u64),
    ];
    let mut backends = Vec::new();
    for (inst_id, last_mean_input_tps, queued_input_size) in insts {
        let backend =
            RegisteredBackend::active(stargate.grpc_addr, "multiregion-model", inst_id).await;
        backend.set_stats(CurrentModelStats {
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
        });
        backends.push(backend);
    }

    wait_for_routing(
        stargate.http_addr,
        "multiregion-model",
        Duration::from_secs(5),
    )
    .await;

    let chat = ChatRequests::new(stargate.http_addr, "multiregion-model");

    let mut stable_fast = false;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    for attempt in 0..60 {
        let resp = chat
            .request(&format!("req-multiregion-{attempt}"))
            .header("x-input-tokens", "10")
            .send()
            .await
            .expect("request failed");

        if resp.status() == 200 {
            let chosen = response_header(&resp, "x-inference-server-id");
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

    stop_backends(&mut backends);
    stargate.shutdown().await;
}

#[tokio::test]
async fn groq_multiregion_waits_for_later_bucket_when_fastest_is_full() {
    let stargate = RunningStargate::start(
        "test-sg-multiregion-wait",
        Some(
            r#"{"default": "power-of-two", "models": {"multiregion-wait-model": "groq-multiregion"}}"#,
        ),
    )
    .await;

    let insts = [
        ("multiregion-fast-full", 0_u64, 1_u64, 1_u64),
        ("multiregion-slower-available", 10_u64, 0_u64, 1_u64),
    ];
    let mut backends = Vec::new();
    for (inst_id, total_query_input_size, num_running_queries, max_engine_concurrency) in insts {
        let backend = RegisteredBackend::active_with_fast_updates(
            stargate.grpc_addr,
            "multiregion-wait-model",
            inst_id,
        )
        .await;
        backend.set_stats(CurrentModelStats {
            output_tps: 0.0,
            last_mean_input_tps: 100.0,
            max_output_tps: 1000.0,
            queue_size: u64::from(num_running_queries > 0),
            queued_input_size: total_query_input_size,
            num_running_queries,
            max_engine_concurrency: Some(max_engine_concurrency),
            total_query_input_size,
            ..CurrentModelStats::default()
        });
        backends.push(backend);
    }

    let chat = ChatRequests::new(stargate.http_addr, "multiregion-wait-model");

    let mut chose_slower = false;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    for attempt in 0..30 {
        let resp = chat
            .request(&format!("req-multiregion-wait-{attempt}"))
            .header("x-max-wait-ms", "250")
            .send()
            .await
            .expect("request failed");

        if resp.status() == 200 {
            let chosen = response_header(&resp, "x-inference-server-id");
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

    stop_backends(&mut backends);
    stargate.shutdown().await;
}

#[tokio::test]
async fn groq_multiregion_cache_affinity_prefers_stable_subset_then_falls_back() {
    let stargate = RunningStargate::start(
        "test-sg-multiregion-affinity",
        Some(
            r#"{
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
        ),
    )
    .await;

    let insts = [
        "multiregion-affinity-a",
        "multiregion-affinity-b",
        "multiregion-affinity-c",
    ];
    let mut backends = Vec::new();
    for inst_id in insts {
        let backend = RegisteredBackend::active_with_fast_updates(
            stargate.grpc_addr,
            "multiregion-affinity-model",
            inst_id,
        )
        .await;
        backend.set_stats(CurrentModelStats {
            output_tps: 0.0,
            last_mean_input_tps: 100.0,
            max_output_tps: 1000.0,
            queue_size: 0,
            queued_input_size: 0,
            num_running_queries: 0,
            max_engine_concurrency: Some(100),
            total_query_input_size: 0,
            ..CurrentModelStats::default()
        });
        backends.push((inst_id, backend));
    }

    let affinity_key = "stable-prefix";
    wait_for_routing_with_cache_affinity(
        stargate.http_addr,
        "multiregion-affinity-model",
        affinity_key,
        Duration::from_secs(5),
    )
    .await;

    let chat = ChatRequests::new(stargate.http_addr, "multiregion-affinity-model");

    let mut stable_choice = None;
    for attempt in 0..20 {
        let resp = chat
            .with_affinity(
                &format!("req-multiregion-affinity-stable-{attempt}"),
                affinity_key,
            )
            .send()
            .await
            .expect("request failed");
        assert_eq!(resp.status(), 200);
        let chosen = response_header(&resp, "x-inference-server-id").to_string();
        let _ = tokio::time::timeout(Duration::from_secs(15), resp.bytes()).await;
        if let Some(stable_choice) = stable_choice.as_deref() {
            assert_eq!(chosen, stable_choice);
        } else {
            stable_choice = Some(chosen);
        }
    }

    let primary = stable_choice.expect("stable primary should be observed");
    let primary_runtime = backends
        .iter()
        .find_map(|(inst_id, backend)| (*inst_id == primary).then_some(&backend.runtime))
        .expect("primary backend should have runtime state");
    primary_runtime.set_model_stats(
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
    );

    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    let mut fallback_attempt = 0usize;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        fallback_attempt += 1;
        let resp = chat
            .with_affinity(
                &format!("req-multiregion-affinity-fallback-{fallback_attempt}"),
                affinity_key,
            )
            .send()
            .await
            .expect("request failed");
        if resp.status() == 200 {
            let chosen = response_header(&resp, "x-inference-server-id").to_string();
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

    for (_, backend) in &mut backends {
        backend.stop();
    }
    stargate.shutdown().await;
}

#[tokio::test]
async fn groq_multiregion_requires_cache_affinity_key_when_configured() {
    let stargate = RunningStargate::start(
        "test-sg-multiregion-affinity-required",
        Some(
            r#"{
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
        ),
    )
    .await;

    let mut backend = RegisteredBackend::active_with_fast_updates(
        stargate.grpc_addr,
        "multiregion-affinity-required-model",
        "multiregion-affinity-required-inst",
    )
    .await;
    backend.set_stats(CurrentModelStats {
        output_tps: 0.0,
        last_mean_input_tps: 100.0,
        max_output_tps: 1000.0,
        queue_size: 0,
        queued_input_size: 0,
        num_running_queries: 0,
        max_engine_concurrency: Some(100),
        total_query_input_size: 0,
        ..CurrentModelStats::default()
    });

    wait_for_routing_with_cache_affinity(
        stargate.http_addr,
        "multiregion-affinity-required-model",
        "affinity-required-key",
        Duration::from_secs(5),
    )
    .await;

    let chat = ChatRequests::new(stargate.http_addr, "multiregion-affinity-required-model");

    let missing_resp = chat
        .request("req-multiregion-affinity-required-missing")
        .send()
        .await
        .expect("missing affinity request failed");
    assert_eq!(
        missing_resp.status(),
        400,
        "configured model should reject missing x-cache-affinity-key"
    );
    let _ = missing_resp.bytes().await;

    let present_resp = chat
        .with_affinity(
            "req-multiregion-affinity-required-present",
            "affinity-required-key",
        )
        .send()
        .await
        .expect("present affinity request failed");
    assert_eq!(present_resp.status(), 200);
    assert_eq!(
        response_header(&present_resp, "x-inference-server-id"),
        "multiregion-affinity-required-inst"
    );
    let _ = present_resp.bytes().await;

    backend.stop();
    stargate.shutdown().await;
}

#[tokio::test]
async fn groq_multiregion_priority_header_uses_matching_queue_estimate() {
    let stargate = RunningStargate::start(
        "test-sg-multiregion-priority",
        Some(
            r#"{
            "default": "power-of-two",
            "models": {
                "multiregion-priority-model": {
                    "algorithm": "groq-multiregion",
                    "n": 2
                }
            }
        }"#,
        ),
    )
    .await;

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
    let mut backends = Vec::new();
    for (inst_id, total_query_input_size, priority_queue_estimates) in insts {
        let backend = RegisteredBackend::active_with_fast_updates(
            stargate.grpc_addr,
            "multiregion-priority-model",
            inst_id,
        )
        .await;
        backend.set_stats(CurrentModelStats {
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
        });
        backends.push(backend);
    }

    let state = stargate.handle.state();
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

    let chat = ChatRequests::new(stargate.http_addr, "multiregion-priority-model");
    for attempt in 0..4 {
        let resp = chat
            .with_input_tokens(&format!("req-multiregion-priority-assert-{attempt}"), 0)
            .header("x-priority", "4")
            .send()
            .await
            .expect("priority request failed");
        assert_eq!(resp.status(), 200);
        assert_eq!(
            response_header(&resp, "x-inference-server-id"),
            "multiregion-priority-specific-low",
            "x-priority=4 should choose the backend with the lower priority-specific queue estimate"
        );
        let _ = resp.bytes().await;
    }

    let resp = chat
        .with_input_tokens("req-multiregion-priority-no-header", 0)
        .send()
        .await
        .expect("non-priority request failed");
    assert_eq!(resp.status(), 200);
    assert_eq!(
        response_header(&resp, "x-inference-server-id"),
        "multiregion-priority-specific-high",
        "without x-priority the aggregate queue estimate should still choose the lower aggregate queue"
    );
    let _ = resp.bytes().await;

    stop_backends(&mut backends);
    stargate.shutdown().await;
}

#[tokio::test]
async fn pulsar_routes_same_affinity_key_consistently() {
    let stargate = RunningStargate::start(
        "test-sg-pulsar",
        Some(
            r#"{
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
        ),
    )
    .await;

    let insts = [
        ("pulsar-a", 1000.0),
        ("pulsar-b", 700.0),
        ("pulsar-c", 400.0),
    ];
    let mut backends = Vec::new();
    for (inst_id, last_mean_input_tps) in &insts {
        let backend =
            RegisteredBackend::start(stargate.grpc_addr, "pulsar-model", inst_id, |_| {}).await;
        backend.set_stats(CurrentModelStats {
            output_tps: 0.0,
            last_mean_input_tps: *last_mean_input_tps,
            max_output_tps: 1000.0,
            queue_size: 0,
            queued_input_size: 0,
            kv_cache_capacity_tokens: 4096,
            kv_cache_used_tokens: 0,
            kv_cache_free_tokens: 4096,
            ..CurrentModelStats::default()
        });
        backends.push(backend);
    }

    let chat = ChatRequests::new(stargate.http_addr, "pulsar-model");

    let affinity_key = "affinity-stable";
    let mut stable_choice: Option<String> = None;
    let mut stable_run = 0usize;
    let mut poll = tokio::time::interval(Duration::from_millis(50));
    for attempt in 0..100 {
        let resp = chat
            .with_affinity(&format!("req-pulsar-stabilize-{attempt}"), affinity_key)
            .send()
            .await
            .expect("request failed");
        if resp.status() != 200 {
            poll.tick().await;
            continue;
        }
        let chosen = response_header(&resp, "x-inference-server-id").to_string();
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
        let resp = chat
            .with_affinity(&format!("req-pulsar-stable-{attempt}"), affinity_key)
            .send()
            .await
            .expect("request failed");
        assert_eq!(resp.status(), 200);
        let chosen = response_header(&resp, "x-inference-server-id").to_string();
        chosen_ids.insert(chosen);
    }

    assert_eq!(
        chosen_ids.len(),
        1,
        "same pulsar affinity key should route consistently, saw: {chosen_ids:?}"
    );

    stop_backends(&mut backends);
    stargate.shutdown().await;
}

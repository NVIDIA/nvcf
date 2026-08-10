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

use super::registration::{
    RegistrationClusterGeneration, test_registration_generation,
    test_registration_generation_in_cluster,
};
use super::reservations::update_reserved_priority_queue_time;
use super::snapshots::{ClusterBackendUpsert, RoutedClusterState, RoutingTargetGeneration};
use super::*;
use crate::load_balancer::{
    GroqMultiregionAlgorithmConfig, LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig,
    LoadBalancerAlgorithmSettings, LoadBalancerConfig, LoadBalancerModelConfig,
    LoadBalancerRequest, LoadBalancerRouter,
};
use InferenceServerStatus::{Active, Inactive};
use stargate_proto::pb::InferenceServerModelRegistration;
use std::collections::HashMap;
use std::sync::Arc;

fn make_target(routing_key: Option<&str>, model_id: &str) -> RoutingTargetKey {
    RoutingTargetKey::new(routing_key.map(ToOwned::to_owned), model_id)
}

fn model_set<const N: usize>(models: [&str; N]) -> BTreeSet<String> {
    models.into_iter().map(str::to_string).collect()
}

struct RegistrationScenario {
    state: StargateState,
    routing_key: Option<String>,
}

impl RegistrationScenario {
    fn new(routing_key: Option<&str>) -> Self {
        Self {
            state: StargateState::default(),
            routing_key: routing_key.map(ToOwned::to_owned),
        }
    }

    fn target(&self, model_id: &str) -> RoutingTargetKey {
        make_target(self.routing_key.as_deref(), model_id)
    }

    fn assert_registered_models(&self, present: &[&str], absent: &[&str]) {
        for model_id in present {
            assert!(
                self.state
                    .has_registered_model_for_target(&self.target(model_id))
            );
        }
        for model_id in absent {
            assert!(
                !self
                    .state
                    .has_registered_model_for_target(&self.target(model_id))
            );
        }
    }

    fn start(&self, id: &str, port: u16) -> RunningRegistration {
        self.start_in(id, id, port)
    }

    fn start_in(&self, id: &str, cluster_id: &str, port: u16) -> RunningRegistration {
        self.start_at(
            id,
            cluster_id,
            &format!("quic://127.0.0.1:{port}"),
            self.routing_key.clone(),
        )
    }

    fn start_keyed(&self, id: &str, port: u16, routing_key: &str) -> RunningRegistration {
        self.start_at(
            id,
            id,
            &format!("quic://127.0.0.1:{port}"),
            Some(routing_key.to_string()),
        )
    }

    fn start_at(
        &self,
        id: &str,
        cluster_id: &str,
        url: &str,
        routing_key: Option<String>,
    ) -> RunningRegistration {
        self.state
            .begin_registration(&RegistrationIdentity {
                inference_server_id: id.to_string(),
                cluster_id: cluster_id.to_string(),
                inference_server_url: url.to_string(),
                routing_key,
                reverse_tunnel: false,
            })
            .unwrap()
    }

    fn update(
        &self,
        running: &RunningRegistration,
        model_id: &str,
        status: InferenceServerStatus,
        stats: ModelStats,
    ) -> InferenceServerRegistration {
        let identity = running.identity();
        InferenceServerRegistration {
            inference_server_id: identity.inference_server_id.clone(),
            cluster_id: identity.cluster_id.clone(),
            inference_server_url: identity.inference_server_url.clone(),
            models: HashMap::from([(
                model_id.to_string(),
                InferenceServerModelRegistration {
                    stats: Some(stats),
                    status: status as i32,
                },
            )]),
            reverse_tunnel: identity.reverse_tunnel,
        }
    }

    fn update_default_stats(
        &self,
        running: &RunningRegistration,
        model_id: &str,
        status: InferenceServerStatus,
    ) -> InferenceServerRegistration {
        self.update(running, model_id, status, ModelStats::default())
    }

    fn empty_update(&self, running: &RunningRegistration) -> InferenceServerRegistration {
        let mut update = self.update(running, "unused", Inactive, ModelStats::default());
        update.models.clear();
        update
    }

    async fn publish(
        &self,
        running: &RunningRegistration,
        model_id: &str,
        status: InferenceServerStatus,
        stats: ModelStats,
        rtt_ms: Option<u64>,
    ) {
        let update = self.update(running, model_id, status, stats);
        self.publish_update(running, &update, rtt_ms).await
    }

    async fn publish_default_stats(
        &self,
        running: &RunningRegistration,
        model_id: &str,
        status: InferenceServerStatus,
        rtt_ms: Option<u64>,
    ) {
        self.publish(running, model_id, status, ModelStats::default(), rtt_ms)
            .await
    }

    async fn activate(&self, running: &RunningRegistration, model_id: &str) {
        self.publish_default_stats(running, model_id, Active, Some(5))
            .await
    }

    async fn publish_update(
        &self,
        running: &RunningRegistration,
        update: &InferenceServerRegistration,
        rtt_ms: Option<u64>,
    ) {
        self.state
            .apply_registration_update(running, update, true, rtt_ms.map(Duration::from_millis))
            .await
    }

    async fn publish_connected(
        &self,
        running: &RunningRegistration,
        update: &InferenceServerRegistration,
    ) {
        self.publish_update(running, update, Some(5)).await
    }

    async fn candidates(&self, model_id: &str) -> Vec<RoutedInferenceServerSnapshot> {
        self.state
            .candidates_for_target(&self.target(model_id))
            .await
    }

    async fn only_candidate(&self, model_id: &str) -> RoutedInferenceServerSnapshot {
        let candidates = self.candidates(model_id).await;
        assert_eq!(candidates.len(), 1);
        candidates.into_iter().next().unwrap()
    }

    async fn clusters(&self, model_id: &str) -> Vec<RoutedClusterSnapshot> {
        self.state
            .cluster_candidates_for_target(&self.target(model_id))
            .await
    }

    async fn only_cluster(&self, model_id: &str) -> RoutedClusterSnapshot {
        let clusters = self.clusters(model_id).await;
        assert_eq!(clusters.len(), 1);
        clusters.into_iter().next().unwrap()
    }

    async fn selected_cluster(&self, model_id: &str) -> SelectedRoutedCluster {
        self.state
            .routing_target_snapshot(&self.target(model_id))
            .await
            .expect("target should be routable")
            .into_selected_cluster(0)
    }
}

fn assert_queue_stats(
    stats: &ModelStats,
    running: u64,
    queued: u64,
    total_input: u64,
    queued_input: u64,
    priority: u32,
    estimate_ms: u64,
) {
    assert_eq!(stats.num_running_queries, running);
    assert_eq!(stats.queue_size, queued);
    assert_eq!(stats.total_query_input_size, total_input);
    assert_eq!(stats.queued_input_size, queued_input);
    assert_eq!(
        stats.queue_time_estimate_ms_by_priority.get(&priority),
        Some(&estimate_ms)
    );
}

fn priority_stats<const N: usize>(
    last_mean_input_tps: f64,
    estimates: [(u32, u64); N],
) -> ModelStats {
    ModelStats {
        last_mean_input_tps,
        queue_time_estimate_ms_by_priority: HashMap::from(estimates),
        ..ModelStats::default()
    }
}

fn queued_stats<const N: usize>(
    last_mean_input_tps: f64,
    estimates: [(u32, u64); N],
) -> ModelStats {
    ModelStats {
        last_mean_input_tps,
        queued_input_size: 25,
        queue_time_estimate_ms_by_priority: HashMap::from(estimates),
        ..ModelStats::default()
    }
}

async fn assert_reserved_priority_map<const N: usize, const M: usize>(
    routing_key: &str,
    inference_server_id: &str,
    model_id: &str,
    initial: [(u32, u64); N],
    request_priority: u32,
    expected: [(u32, u64); M],
) {
    let scenario = RegistrationScenario::new(Some(routing_key));
    let running = scenario.start(inference_server_id, 8888);
    scenario
        .publish(
            &running,
            model_id,
            Active,
            priority_stats(100.0, initial),
            Some(5),
        )
        .await;
    let cluster = scenario.selected_cluster(model_id).await;
    let _reservation = cluster.reserve_backend(&running.generation(), 10, request_priority);

    assert_eq!(
        scenario
            .only_cluster(model_id)
            .await
            .stats
            .queue_time_estimate_ms_by_priority,
        HashMap::from(expected)
    );
}

fn shared_backend_a_stats() -> ModelStats {
    ModelStats {
        output_tps: 2.0,
        last_mean_input_tps: 100.0,
        max_output_tps: 50.0,
        queue_size: 1,
        queued_input_size: 100,
        input_processing_queries: 1,
        output_generation_queries: 2,
        stats_observed_at_unix_ms: 1000,
        stats_capabilities: vec!["request.output.chunk_usage".to_string()],
        stats_sources: vec!["chunk_usage".to_string()],
        kv_cache_capacity_tokens: 1000,
        kv_cache_used_tokens: 100,
        kv_cache_free_tokens: 900,
        num_running_queries: 11,
        max_engine_concurrency: 111,
        total_query_input_size: 1111,
        queue_time_estimate_ms_by_priority: HashMap::from([(1, 111)]),
    }
}

fn shared_backend_b_stats() -> ModelStats {
    ModelStats {
        output_tps: 5.0,
        last_mean_input_tps: 120.0,
        max_output_tps: 60.0,
        queue_size: 2,
        queued_input_size: 200,
        input_processing_queries: 3,
        output_generation_queries: 4,
        stats_observed_at_unix_ms: 2000,
        stats_capabilities: vec![
            "request.output.chunk_usage".to_string(),
            "machine.kv_cache.http".to_string(),
        ],
        stats_sources: vec!["chunk_usage".to_string(), "kv_cache_stats".to_string()],
        kv_cache_capacity_tokens: 2000,
        kv_cache_used_tokens: 500,
        kv_cache_free_tokens: 1500,
        num_running_queries: 7,
        max_engine_concurrency: 77,
        total_query_input_size: 777,
        queue_time_estimate_ms_by_priority: HashMap::from([(1, 222), (2, 333)]),
    }
}

async fn published_shared_cluster(
    stats_a: ModelStats,
    stats_b: ModelStats,
    rtt_a_ms: u64,
) -> (
    RegistrationScenario,
    RunningRegistration,
    RunningRegistration,
) {
    let scenario = RegistrationScenario::new(Some("rk-a"));
    let running_a = scenario.start_in("inst-a", "cluster-shared", 1111);
    let running_b = scenario.start_in("inst-b", "cluster-shared", 2222);
    let update_a = scenario.update(&running_a, "shared-model", Active, stats_a);
    let update_b = scenario.update(&running_b, "shared-model", Active, stats_b);
    scenario
        .publish_update(&running_a, &update_a, Some(rtt_a_ms))
        .await;
    scenario.publish_connected(&running_b, &update_b).await;
    (scenario, running_a, running_b)
}

macro_rules! assert_stats {
    ($stats:expr, $($field:ident: $expected:expr),+ $(,)?) => {{
        let stats = &$stats;
        $(assert_eq!(stats.$field, $expected);)+
    }};
}

macro_rules! backend {
    ($cluster:expr, $id:expr, $output_tps:expr, $input_tps:expr, $rtt_ms:expr) => {
        routed_backend(
            None,
            $cluster,
            $id,
            $output_tps,
            $input_tps,
            Duration::from_millis($rtt_ms),
        )
    };
    ($generation:expr => $cluster:expr, $id:expr, $output_tps:expr, $input_tps:expr, $rtt_ms:expr) => {
        routed_backend(
            Some($generation),
            $cluster,
            $id,
            $output_tps,
            $input_tps,
            Duration::from_millis($rtt_ms),
        )
    };
}

fn routed_backend(
    cluster_generation: Option<Arc<RegistrationClusterGeneration>>,
    cluster_id: &str,
    inference_server_id: &str,
    output_tps: f64,
    last_mean_input_tps: f64,
    rtt: Duration,
) -> RoutedInferenceServerSnapshot {
    let identity = RegistrationIdentity {
        inference_server_id: inference_server_id.to_string(),
        cluster_id: cluster_id.to_string(),
        inference_server_url: format!("quic://{inference_server_id}"),
        routing_key: None,
        reverse_tunnel: false,
    };
    let registration = match cluster_generation {
        Some(generation) => test_registration_generation_in_cluster(identity, generation),
        None => test_registration_generation(identity),
    };
    RoutedInferenceServerSnapshot::new(
        registration,
        ModelStats {
            output_tps,
            last_mean_input_tps,
            ..ModelStats::default()
        },
        rtt,
        Instant::now(),
        Active,
    )
}

#[derive(Clone, Copy)]
enum ReplacementClusterLifetime {
    Retired,
    Overlapping,
}

async fn assert_stale_cleanup_preserves_replacement(
    routing_key: &str,
    model_id: &str,
    inference_server_id: &str,
    cluster_id: &str,
    lifetime: ReplacementClusterLifetime,
) {
    let scenario = RegistrationScenario::new(Some(routing_key));
    let target = scenario.target(model_id);
    let old = scenario.start_in(inference_server_id, cluster_id, 1111);
    let peer = match lifetime {
        ReplacementClusterLifetime::Retired => None,
        ReplacementClusterLifetime::Overlapping => {
            Some(scenario.start_in(&format!("{inference_server_id}-peer"), cluster_id, 3333))
        }
    };
    scenario.activate(&old, model_id).await;

    let ended = scenario
        .state
        .registrations
        .end_registration(old)
        .expect("exact old registration should be removed");
    assert_eq!(
        ended.registration.cluster_generation.is_retired(),
        matches!(lifetime, ReplacementClusterLifetime::Retired)
    );

    let replacement = scenario.start_in(inference_server_id, cluster_id, 2222);
    if matches!(lifetime, ReplacementClusterLifetime::Overlapping) {
        assert!(Arc::ptr_eq(
            &ended.registration.cluster_generation,
            replacement.cluster_generation()
        ));
    }
    scenario
        .publish_default_stats(&replacement, model_id, Active, Some(6))
        .await;
    assert_eq!(
        scenario.only_candidate(model_id).await.inference_server_url,
        "quic://127.0.0.1:2222"
    );
    scenario
        .state
        .routing
        .remove_inference_server_targets(&ended.registration, &HashSet::from([target]))
        .await;

    assert_eq!(
        scenario.only_candidate(model_id).await.inference_server_url,
        "quic://127.0.0.1:2222"
    );
    if let Some(peer) = peer {
        scenario.state.end_registration(peer).await;
    }
    scenario.state.end_registration(replacement).await;
}

#[tokio::test]
async fn registration_cluster_generation_tracks_overlap_and_final_retirement() {
    let scenario = RegistrationScenario::new(Some("rk-cluster-generation"));
    let (running_a, running_b) = std::thread::scope(|scope| {
        let registration_a = scope
            .spawn(|| scenario.start_in("inst-cluster-generation-a", "cluster-generation", 1111));
        let registration_b = scope
            .spawn(|| scenario.start_in("inst-cluster-generation-b", "cluster-generation", 2222));
        (
            registration_a
                .join()
                .expect("registration A thread should not panic"),
            registration_b
                .join()
                .expect("registration B thread should not panic"),
        )
    });
    let first_generation = running_a.cluster_generation().clone();

    assert!(Arc::ptr_eq(
        &first_generation,
        running_b.cluster_generation()
    ));
    scenario.state.end_registration(running_a).await;
    assert!(!first_generation.is_retired());

    let running_c = scenario.start_in("inst-cluster-generation-c", "cluster-generation", 3333);
    assert!(Arc::ptr_eq(
        &first_generation,
        running_c.cluster_generation()
    ));

    scenario.state.end_registration(running_b).await;
    scenario.state.end_registration(running_c).await;
    assert!(first_generation.is_retired());

    let replacement = scenario.start_in(
        "inst-cluster-generation-replacement",
        "cluster-generation",
        4444,
    );
    assert!(!Arc::ptr_eq(
        &first_generation,
        replacement.cluster_generation()
    ));
    assert!(!replacement.cluster_generation().is_retired());
    scenario.state.end_registration(replacement).await;
}

#[tokio::test]
async fn stale_registration_cleanup_cannot_remove_replacement_route() {
    assert_stale_cleanup_preserves_replacement(
        "rk-reused-generation",
        "model-reused-generation",
        "inst-reused-generation",
        "cluster-reused-generation",
        ReplacementClusterLifetime::Retired,
    )
    .await;
}

#[tokio::test]
async fn stale_cleanup_cannot_remove_same_cluster_generation_replacement_route() {
    assert_stale_cleanup_preserves_replacement(
        "rk-reused-overlap",
        "model-reused-overlap",
        "inst-reused-overlap",
        "cluster-reused-overlap",
        ReplacementClusterLifetime::Overlapping,
    )
    .await;
}

#[tokio::test]
async fn stale_selected_registration_cannot_reserve_same_id_replacement() {
    let scenario = RegistrationScenario::new(Some("rk-stale-reservation"));
    let old = scenario.start_in("inst-stale-reservation", "cluster-stale-reservation", 1111);
    let stats = ModelStats {
        last_mean_input_tps: 100.0,
        queue_time_estimate_ms_by_priority: HashMap::from([(4, 5)]),
        ..ModelStats::default()
    };
    scenario
        .publish(
            &old,
            "model-stale-reservation",
            Active,
            stats.clone(),
            Some(5),
        )
        .await;
    let stale_selected = scenario
        .candidates("model-stale-reservation")
        .await
        .into_iter()
        .next()
        .expect("old registration should be routable");
    let selected_cluster = scenario.selected_cluster("model-stale-reservation").await;

    scenario.state.end_registration(old).await;
    let replacement =
        scenario.start_in("inst-stale-reservation", "cluster-stale-reservation", 2222);
    scenario
        .publish(
            &replacement,
            "model-stale-reservation",
            Active,
            stats,
            Some(6),
        )
        .await;

    let reservation = selected_cluster.reserve_backend(&stale_selected.registration, 37, 4);

    assert!(
        reservation.is_none(),
        "a stale selected registration must not reserve its same-ID replacement"
    );
    let clusters = scenario.clusters("model-stale-reservation").await;
    assert_eq!(clusters[0].stats.num_running_queries, 0);
    assert_eq!(clusters[0].stats.total_query_input_size, 0);
}

#[tokio::test]
async fn stale_selected_cluster_cannot_choose_same_id_replacement() {
    let scenario = RegistrationScenario::new(Some("rk-stale-cluster"));
    let old = scenario.start_in("inst-stale-cluster", "cluster-stale-cluster", 1111);
    scenario.activate(&old, "model-stale-cluster").await;
    let selected_cluster = scenario.selected_cluster("model-stale-cluster").await;

    scenario.state.end_registration(old).await;
    let replacement = scenario.start_in("inst-stale-cluster", "cluster-stale-cluster", 2222);
    scenario
        .publish_default_stats(&replacement, "model-stale-cluster", Active, Some(6))
        .await;

    assert!(
        selected_cluster.select_backend(&HashSet::new()).is_none(),
        "a stale selected cluster must not choose from its same-ID replacement"
    );
}

#[tokio::test]
async fn inactive_fresh_cluster_generation_clears_retired_routing_state() {
    let scenario = RegistrationScenario::new(Some("rk-retired-cluster"));
    let old = scenario.start_in("inst-retired-cluster", "cluster-retired-cluster", 1111);
    scenario.activate(&old, "model-retired-cluster").await;
    assert_eq!(scenario.candidates("model-retired-cluster").await.len(), 1);

    let ended = scenario
        .state
        .registrations
        .end_registration(old)
        .expect("exact old registration should be removed");
    assert!(ended.registration.cluster_generation.is_retired());

    let replacement = scenario.start_in("inst-retired-cluster", "cluster-retired-cluster", 2222);
    scenario
        .publish_default_stats(&replacement, "model-retired-cluster", Inactive, Some(6))
        .await;

    assert!(
        scenario
            .candidates("model-retired-cluster")
            .await
            .is_empty()
    );
    scenario.state.end_registration(replacement).await;
}

#[tokio::test]
async fn apply_registration_update_removes_models_no_longer_advertised() {
    let scenario = RegistrationScenario::new(Some("rk-1"));
    let running = scenario.start("inst-1", 1234);
    scenario
        .publish_default_stats(&running, "model-a", Active, Some(10))
        .await;

    scenario
        .publish_default_stats(&running, "model-b", Active, Some(10))
        .await;

    assert!(scenario.candidates("model-a").await.is_empty());
    assert_eq!(scenario.candidates("model-b").await.len(), 1);
    assert!(
        scenario
            .state
            .routing
            .target_state(&scenario.target("model-a"))
            .await
            .is_none()
    );
}

#[tokio::test]
async fn registered_inactive_model_is_known_without_routable_candidates() {
    let scenario = RegistrationScenario::new(Some("rk-known"));
    let running = scenario.start("inst-known-inactive", 1234);
    let target = scenario.target("model-known");
    scenario
        .publish_default_stats(&running, "model-known", Inactive, Some(10))
        .await;

    assert!(scenario.state.has_registered_model_for_target(&target));
    assert!(scenario.candidates("model-known").await.is_empty());
    assert!(
        !scenario
            .state
            .has_registered_model_for_target(&make_target(Some("wrong-rk"), "model-known"))
    );

    scenario.state.end_registration(running).await;
    assert!(!scenario.state.has_registered_model_for_target(&target));
}

#[tokio::test]
async fn registered_target_membership_survives_until_final_advertiser_leaves() {
    let scenario = RegistrationScenario::new(Some("rk-shared-target"));
    let running_a = scenario.start("inst-shared-target-a", 1234);
    let running_b = scenario.start("inst-shared-target-b", 1235);
    let target = scenario.target("model-shared-target");

    for running in [&running_a, &running_b] {
        scenario
            .publish_default_stats(running, "model-shared-target", Inactive, Some(10))
            .await;
    }

    assert!(scenario.state.has_registered_model_for_target(&target));
    assert!(
        !scenario
            .state
            .has_registered_model_for_target(&make_target(Some("rk-other"), "model-shared-target"))
    );

    scenario.state.end_registration(running_a).await;
    assert!(scenario.state.has_registered_model_for_target(&target));

    let empty_update = scenario.empty_update(&running_b);
    scenario
        .publish_update(&running_b, &empty_update, Some(10))
        .await;
    assert!(!scenario.state.has_registered_model_for_target(&target));

    scenario.state.end_registration(running_b).await;
}

#[tokio::test]
async fn registration_model_update_publishes_advertised_generation_and_retains_cleanup_union() {
    let scenario = RegistrationScenario::new(Some("rk-generation"));
    let running = scenario.start("inst-generation", 1234);

    let first = model_set(["model-a", "model-b"]);
    assert!(
        scenario
            .state
            .registrations
            .begin_advertised_model_update(&running, first.clone())
            .is_empty()
    );
    scenario.assert_registered_models(&["model-a", "model-b"], &[]);
    assert_eq!(
        scenario.state.registrations.cleanup_model_ids(&running),
        first
    );
    scenario
        .state
        .registrations
        .finish_advertised_model_update(&running);

    let second = model_set(["model-b", "model-c"]);
    assert_eq!(
        scenario
            .state
            .registrations
            .begin_advertised_model_update(&running, second.clone()),
        model_set(["model-a"])
    );
    scenario.assert_registered_models(&["model-b", "model-c"], &["model-a"]);
    assert_eq!(
        scenario.state.registrations.cleanup_model_ids(&running),
        model_set(["model-a", "model-b", "model-c"])
    );
    scenario
        .state
        .registrations
        .finish_advertised_model_update(&running);
    assert_eq!(
        scenario.state.registrations.cleanup_model_ids(&running),
        second
    );
}

#[tokio::test]
#[should_panic(expected = "registration model update started while another update is applying")]
async fn registration_model_update_rejects_overlapping_apply() {
    let scenario = RegistrationScenario::new(Some("rk-overlap"));
    let running = scenario.start("inst-overlap", 1234);

    scenario
        .state
        .registrations
        .begin_advertised_model_update(&running, BTreeSet::new());
    scenario
        .state
        .registrations
        .begin_advertised_model_update(&running, BTreeSet::new());
}

#[tokio::test]
async fn registration_cleanup_during_applying_update_removes_previous_and_advertised_routes() {
    let scenario = RegistrationScenario::new(Some("rk-cleanup-union"));
    let running = scenario.start("inst-cleanup-union", 1234);
    scenario.activate(&running, "model-a").await;

    let model_a = scenario.target("model-a");
    let model_b = scenario.target("model-b");
    scenario
        .state
        .registrations
        .begin_advertised_model_update(&running, model_set(["model-b"]));
    scenario
        .state
        .routing
        .upsert_inference_server_target(
            &model_b,
            RoutedInferenceServerSnapshot::new(
                running.generation(),
                ModelStats::default(),
                Duration::from_millis(5),
                Instant::now(),
                Active,
            ),
        )
        .await;

    scenario.state.end_registration(running).await;

    assert!(
        scenario
            .state
            .candidates_for_target(&model_a)
            .await
            .is_empty()
    );
    assert!(
        scenario
            .state
            .candidates_for_target(&model_b)
            .await
            .is_empty()
    );
}

#[tokio::test]
async fn active_registration_keeps_connection_rtt_in_snapshot() {
    let scenario = RegistrationScenario::new(Some("rk-rtt"));
    let running = scenario.start("inst-rtt", 7777);
    let expected_rtt = Duration::from_millis(42);
    scenario
        .publish_default_stats(&running, "model-rtt", Active, Some(42))
        .await;

    let candidate = scenario.only_candidate("model-rtt").await;
    assert_eq!(candidate.rtt, expected_rtt);
    assert!(Arc::ptr_eq(&candidate.registration, &running.generation()));
}

#[tokio::test]
async fn reservation_updates_local_snapshot_until_next_registration_update() {
    let scenario = RegistrationScenario::new(Some("rk-res"));
    let running = scenario.start("inst-res", 8888);
    let mut stats = priority_stats(100.0, [(4, 5)]);
    stats.max_engine_concurrency = 8;
    let update = scenario.update(&running, "model-res", Active, stats);

    scenario.publish_connected(&running, &update).await;
    let selected_cluster = scenario.selected_cluster("model-res").await;
    {
        let _successful_attempt_reservation = selected_cluster
            .reserve_backend(&running.generation(), 37, 4)
            .expect("active backend should accept reservation");
    }

    let candidates = scenario.clusters("model-res").await;
    assert_queue_stats(&candidates[0].stats, 1, 1, 37, 37, 4, 375);

    scenario.publish_connected(&running, &update).await;

    let candidates = scenario.clusters("model-res").await;
    assert_queue_stats(&candidates[0].stats, 0, 0, 0, 0, 4, 5);

    let clusters = scenario.clusters("model-res").await;
    assert_queue_stats(&clusters[0].stats, 0, 0, 0, 0, 4, 5);
}

#[tokio::test]
async fn released_reservation_restores_local_snapshot_before_registration_update() {
    let scenario = RegistrationScenario::new(Some("rk-release"));
    let running = scenario.start("inst-release", 8888);
    let update = scenario.update(
        &running,
        "model-release",
        Active,
        priority_stats(100.0, [(4, 5)]),
    );

    scenario.publish_connected(&running, &update).await;
    let selected_cluster = scenario.selected_cluster("model-release").await;
    let reservation = selected_cluster
        .reserve_backend(&running.generation(), 37, 4)
        .expect("active backend should accept reservation");

    reservation.release();

    let candidates = scenario.clusters("model-release").await;
    assert_queue_stats(&candidates[0].stats, 0, 0, 0, 0, 4, 5);

    let consumed_by_heartbeat = selected_cluster
        .reserve_backend(&running.generation(), 10, 4)
        .expect("active backend should accept reservation");
    scenario.publish_connected(&running, &update).await;
    let still_pending = selected_cluster
        .reserve_backend(&running.generation(), 20, 4)
        .expect("active backend should accept reservation");

    consumed_by_heartbeat.release();
    let candidates = scenario.clusters("model-release").await;
    assert_queue_stats(&candidates[0].stats, 1, 1, 20, 20, 4, 205);

    still_pending.release();
    let candidates = scenario.clusters("model-release").await;
    assert_queue_stats(&candidates[0].stats, 0, 0, 0, 0, 4, 5);
}

#[tokio::test]
async fn shared_cluster_reservation_updates_cluster_snapshot_even_when_other_backend_is_latest() {
    let scenario = RegistrationScenario::new(Some("rk-reserved"));
    let running_a = scenario.start_in("inst-a", "cluster-reserved", 1111);
    let running_b = scenario.start_in("inst-b", "cluster-reserved", 2222);

    let update_a = scenario.update(
        &running_a,
        "model-reserved",
        Active,
        ModelStats {
            output_tps: 0.0,
            last_mean_input_tps: 100.0,
            max_output_tps: 50.0,
            queue_size: 0,
            queued_input_size: 0,
            kv_cache_capacity_tokens: 1000,
            kv_cache_used_tokens: 100,
            kv_cache_free_tokens: 900,
            num_running_queries: 3,
            max_engine_concurrency: 8,
            total_query_input_size: 30,
            queue_time_estimate_ms_by_priority: HashMap::from([(4, 10)]),
            ..ModelStats::default()
        },
    );
    let update_b = scenario.update(
        &running_b,
        "model-reserved",
        Active,
        ModelStats {
            output_tps: 0.0,
            last_mean_input_tps: 100.0,
            max_output_tps: 60.0,
            queue_size: 0,
            queued_input_size: 0,
            kv_cache_capacity_tokens: 2000,
            kv_cache_used_tokens: 500,
            kv_cache_free_tokens: 1500,
            num_running_queries: 7,
            max_engine_concurrency: 9,
            total_query_input_size: 70,
            queue_time_estimate_ms_by_priority: HashMap::from([(4, 5)]),
            ..ModelStats::default()
        },
    );

    scenario.publish_connected(&running_a, &update_a).await;
    scenario.publish_connected(&running_b, &update_b).await;

    let selected_cluster = scenario.selected_cluster("model-reserved").await;
    let _reservation = selected_cluster.reserve_backend(&running_a.generation(), 37, 4);

    scenario.publish_connected(&running_b, &update_b).await;

    let cluster = scenario.only_cluster("model-reserved").await;
    // Reservation delta uses summed backend input capacity:
    // existing 5ms + ceil(37 tokens / 200 input TPS * 1000) = 190ms.
    assert_queue_stats(&cluster.stats, 8, 1, 107, 37, 4, 190);
}

#[tokio::test]
async fn reservation_inserts_request_priority_and_preserves_more_urgent_bucket() {
    assert_reserved_priority_map(
        "rk-priority-res",
        "inst-priority-res",
        "model-priority-res",
        [(2, 5)],
        3,
        [(2, 5), (3, 105)],
    )
    .await;
}

#[tokio::test]
async fn reservation_updates_lower_urgency_cumulative_priority_buckets() {
    assert_reserved_priority_map(
        "rk-priority-cumulative",
        "inst-priority-cumulative",
        "model-priority-cumulative",
        [(1, 10), (4, 40)],
        2,
        [(1, 10), (2, 110), (4, 140)],
    )
    .await;
}

#[test]
fn reservation_updates_existing_request_priority_and_lower_urgency_buckets() {
    let mut stats = priority_stats(100.0, [(1, 10), (2, 100), (4, 400)]);

    update_reserved_priority_queue_time(&mut stats, 10, 2);

    assert_eq!(
        stats.queue_time_estimate_ms_by_priority,
        HashMap::from([(1, 10), (2, 200), (4, 500)])
    );
}

#[test]
fn reservation_saturates_priority_queue_estimates() {
    let mut stats = priority_stats(1.0, [(0, u64::MAX - 1), (2, u64::MAX - 2)]);

    update_reserved_priority_queue_time(&mut stats, 10, 0);

    assert_eq!(
        stats.queue_time_estimate_ms_by_priority,
        HashMap::from([(0, u64::MAX), (2, u64::MAX)])
    );
}

#[tokio::test]
async fn reservation_clears_priority_map_when_delta_cannot_be_computed() {
    let mut stats = priority_stats(0.0, [(1, 10), (4, 40)]);

    update_reserved_priority_queue_time(&mut stats, 10, 2);

    assert!(stats.queue_time_estimate_ms_by_priority.is_empty());
}

#[test]
fn queue_time_estimate_helper_uses_sparse_priority_and_aggregate_fallback() {
    for (stats, expected) in [
        (queued_stats(100.0, [(1, 10), (4, 40)]), Some(10)),
        (queued_stats(100.0, []), Some(250)),
        (queued_stats(0.0, []), None),
    ] {
        assert_eq!(
            crate::queue_estimate::queue_time_estimate_ms_for_priority(&stats, 3),
            expected
        );
    }
}

#[test]
fn queue_time_estimate_helper_treats_lower_priority_only_work_as_known_zero() {
    let stats = queued_stats(100.0, [(4, 250)]);
    assert_eq!(
        crate::queue_estimate::queue_time_estimate_ms_for_priority(&stats, 0),
        Some(0)
    );
}

#[tokio::test]
async fn reservation_inserts_high_priority_estimate_when_only_lower_priority_work_exists() {
    assert_reserved_priority_map(
        "rk-priority-clear",
        "inst-priority-clear",
        "model-priority-clear",
        [(4, 5)],
        0,
        [(0, 100), (4, 105)],
    )
    .await;
}

#[tokio::test]
async fn inactive_registration_is_not_routable() {
    let scenario = RegistrationScenario::new(Some("rk-in"));
    let running = scenario.start("inst-inactive", 9999);
    scenario
        .publish_default_stats(&running, "model-r", Inactive, Some(7))
        .await;
    assert!(scenario.candidates("model-r").await.is_empty());
}

#[tokio::test]
async fn list_active_models_reports_only_routable_models() {
    let scenario = RegistrationScenario::new(Some("rk-list"));
    let active = scenario.start("inst-active", 1111);
    let active_without_rtt = scenario.start("inst-no-rtt", 2222);
    let inactive = scenario.start("inst-inactive-list", 3333);

    scenario
        .publish_default_stats(&active, "model-listed", Active, Some(7))
        .await;
    scenario
        .publish_default_stats(&active_without_rtt, "model-not-ready", Active, None)
        .await;
    scenario
        .publish_default_stats(&inactive, "model-inactive", Inactive, Some(7))
        .await;

    let models = scenario
        .state
        .list_active_models(Some("rk-list"), &[])
        .await;
    assert_eq!(models.len(), 1);
    assert_eq!(models[0], "model-listed");

    let filtered = scenario
        .state
        .list_active_models(Some("rk-list"), &["model-not-ready".to_string()])
        .await;
    assert!(filtered.is_empty(), "got: {filtered:?}");
}

#[tokio::test]
async fn list_active_models_ignores_empty_target_generations() {
    let state = StargateState::default();
    let target = make_target(Some("rk-intermediate"), "model-intermediate");
    let _target_state = state.routing.target_state_or_insert(&target).await;

    assert!(
        state
            .cluster_candidates_for_target(&target)
            .await
            .is_empty(),
        "proxy routing source of truth should not consider an uninitialized generation routable"
    );

    let listed = state.list_active_models(Some("rk-intermediate"), &[]).await;
    assert!(
        listed.is_empty(),
        "ListModels must not advertise targets without routable cluster generations: {listed:?}"
    );
}

#[test]
#[should_panic(expected = "routed snapshot inference-server ID must match exact registration")]
fn routed_cluster_rejects_snapshot_identity_mismatch() {
    let mut backend = backend!(
        "cluster-snapshot-identity",
        "backend-snapshot-identity",
        1.0,
        10.0,
        10
    );
    let cluster_state = RoutedClusterState::new(backend.registration.cluster_generation.clone());
    backend.inference_server_id = "different-exported-id".to_string();

    cluster_state.upsert_backend(Arc::new(backend));
}

#[test]
fn cluster_generation_publishes_matching_sorted_backend_and_aggregate_views() {
    let backend_b = backend!("cluster-generation", "backend-b", 2.0, 20.0, 20);
    let backend_a = backend!(
        backend_b.registration.cluster_generation.clone() =>
        "cluster-generation", "backend-a", 1.0, 10.0, 10
    );
    let cluster_state = RoutedClusterState::new(backend_b.registration.cluster_generation.clone());

    cluster_state.upsert_backend(Arc::new(backend_b.clone()));
    cluster_state.upsert_backend(Arc::new(backend_a.clone()));

    let backends = cluster_state.backend_snapshot_values();
    assert_eq!(
        backends
            .iter()
            .map(|backend| backend.inference_server_id.as_str())
            .collect::<Vec<_>>(),
        vec!["backend-a", "backend-b"]
    );
    let snapshot = cluster_state
        .routing_snapshot()
        .expect("published backends should have one matching aggregate");
    assert_eq!(snapshot.active_backend_count, backends.len());
    assert_eq!(snapshot.stats.output_tps, 3.0);
    assert_eq!(snapshot.stats.last_mean_input_tps, 30.0);
    assert_eq!(snapshot.rtt, Duration::from_millis(15));

    cluster_state.remove_backend(&backend_a.registration);
    let backends = cluster_state.backend_snapshot_values();
    let snapshot = cluster_state
        .routing_snapshot()
        .expect("remaining backend should keep the cluster routable");
    assert_eq!(backends.len(), 1);
    assert_eq!(backends[0].inference_server_id, "backend-b");
    assert_eq!(snapshot.active_backend_count, backends.len());
    assert_eq!(snapshot.stats.output_tps, 2.0);
    assert_eq!(snapshot.rtt, Duration::from_millis(20));

    cluster_state.remove_backend(&backend_b.registration);
    assert!(cluster_state.routing_snapshot().is_none());
    assert!(cluster_state.select_backend(&HashSet::new()).is_none());
}

#[test]
fn cluster_snapshot_rtt_tracks_backend_heartbeat_and_removal() {
    let backend_a = backend!("cluster-rtt-lifecycle", "backend-a", 1.0, 10.0, 8);
    let cluster_state = RoutedClusterState::new(backend_a.registration.cluster_generation.clone());
    let backend_a_registration = backend_a.registration.clone();
    assert_eq!(
        cluster_state.upsert_backend(Arc::new(backend_a)),
        ClusterBackendUpsert::Inserted
    );
    assert_eq!(
        cluster_state
            .routing_snapshot()
            .expect("one active backend should publish a cluster snapshot")
            .rtt,
        Duration::from_millis(8)
    );

    let backend_b = backend!(
        backend_a_registration.cluster_generation.clone() =>
        "cluster-rtt-lifecycle", "backend-b", 2.0, 20.0, 5
    );
    let backend_b_registration = backend_b.registration.clone();
    assert_eq!(
        cluster_state.upsert_backend(Arc::new(backend_b)),
        ClusterBackendUpsert::Inserted
    );
    assert_eq!(
        cluster_state
            .routing_snapshot()
            .expect("two active backends should publish a cluster snapshot")
            .rtt,
        Duration::from_micros(6_500)
    );

    let backend_b_heartbeat = RoutedInferenceServerSnapshot::new(
        backend_b_registration.clone(),
        ModelStats::default(),
        Duration::from_millis(20),
        Instant::now(),
        Active,
    );
    assert_eq!(
        cluster_state.upsert_backend(Arc::new(backend_b_heartbeat)),
        ClusterBackendUpsert::Replaced
    );
    let snapshot = cluster_state
        .routing_snapshot()
        .expect("a heartbeat replacement should refresh the cluster snapshot");
    assert_eq!(snapshot.active_backend_count, 2);
    assert_eq!(snapshot.rtt, Duration::from_millis(14));

    cluster_state.remove_backend(&backend_b_registration);
    let snapshot = cluster_state
        .routing_snapshot()
        .expect("the remaining active backend should keep the cluster routable");
    assert_eq!(snapshot.active_backend_count, 1);
    assert_eq!(snapshot.rtt, Duration::from_millis(8));
}

#[test]
fn cluster_snapshot_rtt_preserves_single_backend_zero() {
    let backend = routed_backend(
        None,
        "cluster-rtt-zero",
        "backend-zero",
        1.0,
        10.0,
        Duration::ZERO,
    );
    let cluster_state = RoutedClusterState::new(backend.registration.cluster_generation.clone());

    cluster_state.upsert_backend(Arc::new(backend));

    let snapshot = cluster_state
        .routing_snapshot()
        .expect("a zero-RTT backend should remain a valid singleton cluster");
    assert_eq!(snapshot.active_backend_count, 1);
    assert_eq!(snapshot.rtt, Duration::ZERO);
}

#[test]
fn cluster_snapshot_rtt_truncates_fractional_nanoseconds() {
    let backend_a = routed_backend(
        None,
        "cluster-rtt-fractional",
        "backend-a",
        1.0,
        10.0,
        Duration::from_nanos(1),
    );
    let backend_b = routed_backend(
        Some(backend_a.registration.cluster_generation.clone()),
        "cluster-rtt-fractional",
        "backend-b",
        1.0,
        10.0,
        Duration::from_nanos(4),
    );
    let cluster_state = RoutedClusterState::new(backend_a.registration.cluster_generation.clone());

    cluster_state.upsert_backend(Arc::new(backend_a));
    cluster_state.upsert_backend(Arc::new(backend_b));

    assert_eq!(
        cluster_state
            .routing_snapshot()
            .expect("two active backends should publish a truncated mean")
            .rtt,
        Duration::from_nanos(2)
    );
}

#[test]
fn cluster_snapshot_rtt_averages_three_active_backends() {
    let backend_a = backend!("cluster-rtt-three", "backend-a", 1.0, 10.0, 2);
    let cluster_generation = backend_a.registration.cluster_generation.clone();
    let backend_b = backend!(
        cluster_generation.clone() =>
        "cluster-rtt-three", "backend-b", 1.0, 10.0, 5
    );
    let backend_c = backend!(
        cluster_generation.clone() =>
        "cluster-rtt-three", "backend-c", 1.0, 10.0, 11
    );
    let cluster_state = RoutedClusterState::new(cluster_generation);

    cluster_state.upsert_backend(Arc::new(backend_c));
    cluster_state.upsert_backend(Arc::new(backend_a));
    cluster_state.upsert_backend(Arc::new(backend_b));

    let snapshot = cluster_state
        .routing_snapshot()
        .expect("three active backends should publish one cluster mean");
    assert_eq!(snapshot.active_backend_count, 3);
    assert_eq!(snapshot.rtt, Duration::from_millis(6));
}

#[test]
fn cluster_snapshot_rtt_mean_avoids_overflow_and_truncates_fractional_nanoseconds() {
    let backend_a = routed_backend(
        None,
        "cluster-rtt-overflow",
        "backend-a",
        1.0,
        10.0,
        Duration::MAX,
    );
    let backend_b = routed_backend(
        Some(backend_a.registration.cluster_generation.clone()),
        "cluster-rtt-overflow",
        "backend-b",
        1.0,
        10.0,
        Duration::MAX - Duration::from_nanos(3),
    );
    let cluster_state = RoutedClusterState::new(backend_a.registration.cluster_generation.clone());

    cluster_state.upsert_backend(Arc::new(backend_a));
    cluster_state.upsert_backend(Arc::new(backend_b));

    assert_eq!(
        cluster_state
            .routing_snapshot()
            .expect("maximum-duration backends should still aggregate")
            .rtt,
        Duration::MAX - Duration::from_nanos(2)
    );
}

#[test]
fn cluster_snapshot_rtt_mean_handles_three_near_max_durations() {
    let backend_a = routed_backend(
        None,
        "cluster-rtt-near-max",
        "backend-a",
        1.0,
        10.0,
        Duration::MAX,
    );
    let cluster_generation = backend_a.registration.cluster_generation.clone();
    let backend_b = routed_backend(
        Some(cluster_generation.clone()),
        "cluster-rtt-near-max",
        "backend-b",
        1.0,
        10.0,
        Duration::MAX - Duration::from_nanos(1),
    );
    let backend_c = routed_backend(
        Some(cluster_generation.clone()),
        "cluster-rtt-near-max",
        "backend-c",
        1.0,
        10.0,
        Duration::MAX - Duration::from_nanos(2),
    );
    let cluster_state = RoutedClusterState::new(cluster_generation);

    cluster_state.upsert_backend(Arc::new(backend_a));
    cluster_state.upsert_backend(Arc::new(backend_b));
    cluster_state.upsert_backend(Arc::new(backend_c));

    assert_eq!(
        cluster_state
            .routing_snapshot()
            .expect("near-maximum RTTs should aggregate without overflow")
            .rtt,
        Duration::MAX - Duration::from_nanos(1)
    );
}

#[test]
fn cluster_generation_derives_cluster_source_from_stored_backends() {
    let observed_at = Instant::now();
    let mut backend_a = backend!("cluster-derived-source", "backend-a", 1.0, 100.0, 10);
    backend_a.stats.max_output_tps = 10.0;
    backend_a.snapshot_updated_at = observed_at;
    let cluster_state = Arc::new(RoutedClusterState::new(
        backend_a.registration.cluster_generation.clone(),
    ));
    let backend_a_registration = backend_a.registration.clone();
    let mut backend_c = backend!(
        backend_a.registration.cluster_generation.clone() =>
        "cluster-derived-source", "backend-c", 3.0, 100.0, 30
    );
    backend_c.stats.max_output_tps = 30.0;
    backend_c.snapshot_updated_at = observed_at + Duration::from_millis(1);
    let mut backend_b = backend!(
        backend_a.registration.cluster_generation.clone() =>
        "cluster-derived-source", "backend-b", 2.0, 100.0, 20
    );
    backend_b.stats.max_output_tps = 20.0;
    backend_b.snapshot_updated_at = observed_at + Duration::from_millis(2);
    let backend_b_registration = backend_b.registration.clone();

    cluster_state.upsert_backend(Arc::new(backend_a));
    cluster_state.upsert_backend(Arc::new(backend_c));
    cluster_state.upsert_backend(Arc::new(backend_b.clone()));
    let _reservation = cluster_state
        .reserve_backend(&backend_a_registration, 25, 0)
        .expect("stored backend should accept reservation");

    let snapshot = cluster_state
        .routing_snapshot()
        .expect("latest backend should publish the cluster-scoped source");
    assert_eq!(snapshot.stats.max_output_tps, 20.0);
    assert_eq!(snapshot.stats.queue_size, 1);

    backend_b.stats.max_output_tps = 25.0;
    cluster_state.upsert_backend(Arc::new(backend_b));
    let snapshot = cluster_state
        .routing_snapshot()
        .expect("source heartbeat should retain another backend's reservation");
    assert_eq!(snapshot.stats.max_output_tps, 25.0);
    assert_eq!(snapshot.stats.queue_size, 1);

    cluster_state.remove_backend(&backend_b_registration);
    let snapshot = cluster_state
        .routing_snapshot()
        .expect("source removal should derive a surviving source");
    assert_eq!(snapshot.active_backend_count, 2);
    assert_eq!(snapshot.stats.max_output_tps, 30.0);
    assert_eq!(snapshot.stats.queue_size, 1);
}

#[test]
fn replaced_backend_registration_cannot_reserve_same_id_successor() {
    let old = backend!(
        "cluster-exact-registration",
        "backend-same-id",
        1.0,
        100.0,
        10
    );
    let cluster_generation = old.registration.cluster_generation.clone();
    let stale_registration = old.registration.clone();
    let cluster_state = Arc::new(RoutedClusterState::new(cluster_generation.clone()));
    assert_eq!(
        cluster_state.upsert_backend(Arc::new(old)),
        ClusterBackendUpsert::Inserted
    );

    let replacement = backend!(
        cluster_generation => "cluster-exact-registration", "backend-same-id", 2.0, 100.0, 5
    );
    let replacement_registration = replacement.registration.clone();
    assert_eq!(
        cluster_state.upsert_backend(Arc::new(replacement)),
        ClusterBackendUpsert::Replaced
    );

    assert!(
        cluster_state
            .reserve_backend(&stale_registration, 25, 0)
            .is_none(),
        "a replaced registration must not reserve its same-ID successor"
    );
    assert!(
        cluster_state
            .reserve_backend(&replacement_registration, 25, 0)
            .is_some(),
        "the exact stored replacement should accept a reservation"
    );
}

#[test]
fn reservation_release_only_cancels_exact_pending_reservation() {
    let old_backend = backend!("cluster-exact-release", "backend-same-id", 1.0, 100.0, 10);
    let old_registration = old_backend.registration.clone();
    let old_cluster = Arc::new(RoutedClusterState::new(
        old_registration.cluster_generation.clone(),
    ));
    old_cluster.upsert_backend(Arc::new(old_backend));
    let old_reservation = old_cluster
        .reserve_backend(&old_registration, 10, 0)
        .expect("old cluster should accept reservation");

    let replacement_backend = backend!("cluster-exact-release", "backend-same-id", 2.0, 100.0, 5);
    let replacement_registration = replacement_backend.registration.clone();
    let replacement_cluster = Arc::new(RoutedClusterState::new(
        replacement_registration.cluster_generation.clone(),
    ));
    replacement_cluster.upsert_backend(Arc::new(replacement_backend));
    let _replacement_reservation = replacement_cluster
        .reserve_backend(&replacement_registration, 20, 0)
        .expect("replacement cluster should accept its independent reservation");

    old_reservation.release();

    assert_eq!(
        old_cluster
            .routing_snapshot()
            .expect("old cluster should retain its backend")
            .stats
            .queue_size,
        0
    );
    assert_eq!(
        replacement_cluster
            .routing_snapshot()
            .expect("replacement cluster should retain its backend")
            .stats
            .queue_size,
        1,
        "releasing the old token must not cancel the replacement reservation"
    );
}

#[test]
fn reservation_release_does_not_wait_for_cluster_generation_lock() {
    let backend = backend!(
        "cluster-lock-independent-release",
        "backend-lock-independent-release",
        1.0,
        100.0,
        10
    );
    let registration = backend.registration.clone();
    let cluster = Arc::new(RoutedClusterState::new(
        registration.cluster_generation.clone(),
    ));
    cluster.upsert_backend(Arc::new(backend));
    let reservation = cluster
        .reserve_backend(&registration, 10, 0)
        .expect("active backend should accept reservation");

    let (release_finished, release_thread, _finished_receiver) = {
        let _generation = cluster.generation.lock();
        let (started_sender, started_receiver) = std::sync::mpsc::sync_channel(1);
        let (finished_sender, finished_receiver) = std::sync::mpsc::sync_channel(1);
        let release_thread = std::thread::spawn(move || {
            started_sender
                .send(())
                .expect("release start receiver should remain alive");
            reservation.release();
            finished_sender
                .send(())
                .expect("release completion receiver should remain alive");
        });
        started_receiver
            .recv()
            .expect("reservation release thread should start");
        let release_finished = finished_receiver
            .recv_timeout(Duration::from_secs(1))
            .is_ok();
        (release_finished, release_thread, finished_receiver)
    };

    release_thread
        .join()
        .expect("reservation release thread should finish");
    assert!(
        release_finished,
        "reservation release should not wait for the cluster-generation lock"
    );
    assert_eq!(
        cluster
            .routing_snapshot()
            .expect("cluster should retain its backend")
            .stats
            .queue_size,
        0
    );
}

#[test]
fn cluster_generation_round_robin_uses_stored_order_and_filtered_sequence() {
    let backend_b = backend!("cluster-round-robin-generation", "backend-b", 1.0, 10.0, 10);
    let cluster_generation = backend_b.registration.cluster_generation.clone();
    let cluster_state = RoutedClusterState::new(cluster_generation.clone());
    cluster_state.upsert_backend(Arc::new(backend_b));
    for inference_server_id in ["backend-a", "backend-c"] {
        let backend = backend!(
            cluster_generation.clone() =>
            "cluster-round-robin-generation", inference_server_id, 1.0, 10.0, 10
        );
        cluster_state.upsert_backend(Arc::new(backend.clone()));
    }

    let mut selected = Vec::new();
    for _ in 0..3 {
        selected.push(
            cluster_state
                .select_backend(&HashSet::new())
                .expect("stored backend should be selected")
                .inference_server_id
                .clone(),
        );
    }
    assert_eq!(selected, vec!["backend-a", "backend-b", "backend-c"]);

    let failed = HashSet::from(["backend-a".to_string()]);
    assert_eq!(
        cluster_state
            .select_backend(&failed)
            .expect("filtered backend should be selected")
            .inference_server_id,
        "backend-c"
    );
    assert_eq!(
        cluster_state
            .select_backend(&failed)
            .expect("filtered sequence should advance")
            .inference_server_id,
        "backend-b"
    );
}

#[test]
fn backend_selection_shares_published_snapshot_until_republication() {
    let backend = backend!(
        "cluster-shared-backend-snapshot",
        "backend-shared-snapshot",
        1.0,
        10.0,
        10
    );
    let registration = backend.registration.clone();
    let cluster_state = RoutedClusterState::new(registration.cluster_generation.clone());
    let publication = Arc::new(backend);
    cluster_state.upsert_backend(publication.clone());

    let first = cluster_state
        .select_backend(&HashSet::new())
        .expect("published backend should be selected");
    let second = cluster_state
        .select_backend(&HashSet::new())
        .expect("same published backend should be selected again");
    assert!(
        Arc::ptr_eq(&publication, &first),
        "cluster storage should retain the exact immutable publication"
    );
    assert!(
        Arc::ptr_eq(&first, &second),
        "repeated selection should share the immutable published snapshot"
    );

    let republished_publication = Arc::new(RoutedInferenceServerSnapshot::new(
        registration,
        ModelStats {
            output_tps: 99.0,
            last_mean_input_tps: 10.0,
            ..ModelStats::default()
        },
        Duration::from_millis(10),
        Instant::now(),
        Active,
    ));
    cluster_state.upsert_backend(republished_publication.clone());
    let republished = cluster_state
        .select_backend(&HashSet::new())
        .expect("republished backend should be selected");

    assert!(Arc::ptr_eq(&republished_publication, &republished));
    assert!(
        !Arc::ptr_eq(&first, &republished),
        "heartbeat republication should replace the immutable published snapshot"
    );
    assert_eq!(first.stats.output_tps, 1.0);
    assert_eq!(republished.stats.output_tps, 99.0);
}

#[test]
fn cluster_backend_aggregate_dedupes_sources_and_averages_rtt() {
    let observed_at = Instant::now();

    let mut backend_a = backend!("cluster-aggregate", "backend-a", 1.5, 10.0, 20);
    backend_a.stats = ModelStats {
        output_tps: 1.5,
        last_mean_input_tps: 10.0,
        queue_size: 2,
        queued_input_size: 20,
        input_processing_queries: 1,
        output_generation_queries: 3,
        stats_observed_at_unix_ms: 10,
        stats_capabilities: vec!["cap-a".to_string(), "shared".to_string()],
        stats_sources: vec!["source-a".to_string(), "shared".to_string()],
        ..ModelStats::default()
    };
    backend_a.snapshot_updated_at = observed_at;
    let cluster_state = RoutedClusterState::new(backend_a.registration.cluster_generation.clone());
    cluster_state.upsert_backend(Arc::new(backend_a.clone()));
    let mut backend_b = backend!(
        backend_a.registration.cluster_generation.clone() =>
        "cluster-aggregate", "backend-b", 2.5, 30.0, 5
    );
    backend_b.stats = ModelStats {
        output_tps: 2.5,
        last_mean_input_tps: 30.0,
        queue_size: 4,
        queued_input_size: 40,
        input_processing_queries: 5,
        output_generation_queries: 7,
        stats_observed_at_unix_ms: 25,
        stats_capabilities: vec!["shared".to_string(), "cap-b".to_string()],
        stats_sources: vec!["shared".to_string(), "source-b".to_string()],
        ..ModelStats::default()
    };
    backend_b.snapshot_updated_at = observed_at;
    cluster_state.upsert_backend(Arc::new(backend_b.clone()));

    let (stats, rtt, active_backend_count) = cluster_state
        .backend_aggregate()
        .expect("two live backends should aggregate");

    assert_eq!(active_backend_count, 2);
    assert_eq!(rtt, Duration::from_micros(12_500));
    assert_stats!(stats,
        output_tps: 4.0,
        last_mean_input_tps: 40.0,
        queue_size: 6,
        queued_input_size: 60,
        input_processing_queries: 6,
        output_generation_queries: 10,
        stats_observed_at_unix_ms: 25,
    );
    let mut capabilities = stats.stats_capabilities.clone();
    capabilities.sort();
    assert_eq!(capabilities, vec!["cap-a", "cap-b", "shared"]);
    assert_eq!(stats.stats_capabilities.len(), 3);

    let mut sources = stats.stats_sources.clone();
    sources.sort();
    assert_eq!(sources, vec!["shared", "source-a", "source-b"]);
    assert_eq!(stats.stats_sources.len(), 3);
}

#[tokio::test]
async fn list_active_models_filters_by_routing_key() {
    let scenario = RegistrationScenario::new(None);
    let running_a = scenario.start_keyed("inst-list-rk-a", 1111, "rk-a");
    let running_b = scenario.start_keyed("inst-list-rk-b", 2222, "rk-b");

    let mut update_a = scenario.update_default_stats(&running_a, "z-list-model", Active);
    for model in ["shared-list-model", "a-list-model"] {
        update_a.models.insert(
            model.to_string(),
            InferenceServerModelRegistration {
                stats: Some(ModelStats::default()),
                status: Active as i32,
            },
        );
    }
    let update_b = scenario.update_default_stats(&running_b, "shared-list-model", Active);

    scenario.publish_connected(&running_a, &update_a).await;
    scenario.publish_connected(&running_b, &update_b).await;
    let unscoped = scenario.state.list_active_models(None, &[]).await;
    assert!(
        unscoped.is_empty(),
        "unscoped ListModels must not include keyed registrations: {unscoped:?}"
    );

    let all_scoped = scenario.state.list_active_models_for_debug().await;
    assert_eq!(
        all_scoped,
        vec!["a-list-model", "shared-list-model", "z-list-model"]
    );

    let models_a = scenario.state.list_active_models(Some("rk-a"), &[]).await;
    assert_eq!(
        models_a,
        vec!["a-list-model", "shared-list-model", "z-list-model"]
    );

    let models_b = scenario.state.list_active_models(Some("rk-b"), &[]).await;
    assert_eq!(models_b, vec!["shared-list-model"]);

    let wrong_key = scenario.state.list_active_models(Some("rk-c"), &[]).await;
    assert!(
        wrong_key.is_empty(),
        "ListModels must not leak models across routing keys: {wrong_key:?}"
    );

    let filtered = scenario
        .state
        .list_active_models(
            Some("rk-a"),
            &[
                "z-list-model".to_string(),
                "shared-list-model".to_string(),
                "z-list-model".to_string(),
            ],
        )
        .await;
    assert_eq!(filtered, vec!["shared-list-model", "z-list-model"]);
}

#[tokio::test]
async fn list_active_models_reads_authoritative_routing_generations() {
    let scenario = RegistrationScenario::new(None);
    let target = scenario.target("model-list-authoritative");
    let running = scenario.start_at(
        "backend-list-authoritative",
        "cluster-list-authoritative",
        "quic://127.0.0.1:2222",
        None,
    );
    let registration = running.generation();
    let snapshot = RoutedInferenceServerSnapshot::new(
        registration.clone(),
        ModelStats::default(),
        Duration::from_millis(5),
        Instant::now(),
        Active,
    );

    scenario
        .state
        .routing
        .upsert_inference_server_target(&target, snapshot)
        .await;

    let candidate = scenario
        .state
        .candidates_for_target(&target)
        .await
        .into_iter()
        .next()
        .expect("directly published snapshot should be routable");
    assert!(Arc::ptr_eq(&candidate.registration, &registration));
    assert_eq!(
        (
            &candidate.cluster_id,
            &candidate.inference_server_id,
            &candidate.inference_server_url,
            candidate.reverse_tunnel,
        ),
        (
            &registration.identity.cluster_id,
            &registration.identity.inference_server_id,
            &registration.identity.inference_server_url,
            registration.identity.reverse_tunnel,
        )
    );

    let listed = scenario.state.list_active_models(None, &[]).await;
    assert_eq!(listed.len(), 1, "got: {listed:?}");
    assert_eq!(listed[0], "model-list-authoritative");

    scenario
        .state
        .routing
        .remove_inference_server_from_target(&registration, &target)
        .await;
    let after_removal = scenario.state.list_active_models(None, &[]).await;
    assert!(
        after_removal.is_empty(),
        "removed model should disappear from authoritative discovery immediately: {after_removal:?}"
    );
    scenario.state.end_registration(running).await;
}

#[tokio::test]
async fn different_routing_keys_isolate_candidates() {
    let scenario = RegistrationScenario::new(None);
    let running_a = scenario.start_keyed("inst-a", 1111, "rk-a");
    let running_b = scenario.start_keyed("inst-b", 2222, "rk-b");

    scenario.activate(&running_a, "shared-model").await;
    scenario.activate(&running_b, "shared-model").await;

    let candidates_a = scenario
        .state
        .candidates_for_target(&make_target(Some("rk-a"), "shared-model"))
        .await;
    let candidates_b = scenario
        .state
        .candidates_for_target(&make_target(Some("rk-b"), "shared-model"))
        .await;
    assert_eq!(candidates_a.len(), 1);
    assert_eq!(candidates_b.len(), 1);
    assert_eq!(candidates_a[0].inference_server_id, "inst-a");
    assert_eq!(candidates_b[0].inference_server_id, "inst-b");
}

#[test]
fn routing_target_membership_follows_selected_snapshot_registration() {
    let target_state = RoutingTargetState::default();
    let selected_snapshot = backend!("cluster-selected", "backend-selected", 1.0, 10.0, 10);
    target_state
        .upsert_backend(Arc::new(selected_snapshot.clone()))
        .expect("active routing target should accept backend");

    let selected_cluster = {
        let generation = target_state.generation.lock();
        let RoutingTargetGeneration::Active { clusters, .. } = &*generation else {
            panic!("new routing target should remain active");
        };
        assert_eq!(clusters.len(), 1);
        clusters
            .get(&selected_snapshot.registration.identity.cluster_id)
            .cloned()
            .expect(
                "target membership must follow the registration exposed to selected backend work",
            )
    };
    let stored = selected_cluster
        .select_backend(&HashSet::new())
        .expect("stored backend should be selectable");
    assert!(Arc::ptr_eq(
        &stored.registration,
        &selected_snapshot.registration
    ));
}

#[test]
fn routing_target_generation_tracks_backend_membership_without_heartbeat_double_count() {
    let target_state = RoutingTargetState::default();
    let backend_a = backend!("cluster-a", "backend-a", 1.0, 10.0, 10);
    let backend_b = backend!("cluster-b", "backend-b", 2.0, 20.0, 20);
    let backend_a_registration = backend_a.registration.clone();
    let backend_b_registration = backend_b.registration.clone();

    assert_eq!(target_state.active_backend_count(), 0);
    target_state
        .upsert_backend(Arc::new(backend_a.clone()))
        .unwrap();
    assert_eq!(target_state.active_backend_count(), 1);

    let mut heartbeat = backend_a;
    heartbeat.stats.output_tps = 3.0;
    target_state.upsert_backend(Arc::new(heartbeat)).unwrap();
    assert_eq!(
        target_state.active_backend_count(),
        1,
        "replacing one backend heartbeat must not duplicate membership"
    );

    target_state.upsert_backend(Arc::new(backend_b)).unwrap();
    assert_eq!(target_state.active_backend_count(), 2);

    target_state.remove_backend(&backend_a_registration);
    assert_eq!(target_state.active_backend_count(), 1);
    target_state.remove_backend(&backend_a_registration);
    assert_eq!(
        target_state.active_backend_count(),
        1,
        "removing absent membership must not decrement the target summary"
    );

    target_state.remove_backend(&backend_b_registration);
    assert_eq!(target_state.active_backend_count(), 0);
}

#[tokio::test]
async fn routing_target_generation_recreates_reachable_state_after_retirement() {
    let scenario = RegistrationScenario::new(Some("rk-retired"));
    let target = scenario.target("model-retired");
    let retired = scenario.state.routing.target_state_or_insert(&target).await;

    assert!(
        scenario
            .state
            .routing
            .remove_if_empty(&target, retired.clone())
            .await
    );
    let rejected_publication = Arc::new(backend!(
        "cluster-retired",
        "backend-retired",
        1.0,
        10.0,
        10
    ));
    let rejected = retired
        .upsert_backend(rejected_publication.clone())
        .expect_err("a retained stale owner must not accept unreachable backend state");
    assert!(
        Arc::ptr_eq(&rejected, &rejected_publication),
        "retired-target retry should retain the exact immutable publication"
    );

    let running = scenario.start_at(
        "backend-current",
        "cluster-current",
        "quic://backend-current",
        scenario.routing_key.clone(),
    );
    scenario
        .state
        .routing
        .upsert_inference_server_target(
            &target,
            RoutedInferenceServerSnapshot::new(
                running.generation(),
                ModelStats::default(),
                Duration::from_millis(5),
                Instant::now(),
                Active,
            ),
        )
        .await;

    let current = scenario
        .state
        .routing
        .target_state(&target)
        .await
        .expect("normal upsert should publish a replacement target generation");
    assert!(!Arc::ptr_eq(&retired, &current));
    assert_eq!(scenario.candidates("model-retired").await.len(), 1);
    scenario.state.end_registration(running).await;
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn routing_target_recreation_starts_fresh_load_balancer_state() {
    let state = StargateState::default();
    let target = make_target(Some("rk-lifecycle"), "model-lifecycle");
    let router = LoadBalancerRouter::from_config(&LoadBalancerConfig {
        default: LoadBalancerAlgorithm::RoundRobin,
        request_algorithms: HashMap::new(),
        models: HashMap::new(),
    })
    .expect("round-robin router should initialize");
    let request = LoadBalancerRequest {
        routing_target: &target,
        cache_affinity_key: None,
        input_tokens: None,
        priority: 0,
        received_at: Instant::now(),
        request_slo: None,
        excluded_cluster_ids: None,
    };
    let candidates = [
        RoutedClusterSnapshot {
            cluster_id: "cluster-0".to_string(),
            stats: ModelStats::default(),
            rtt: Duration::from_millis(1),
            snapshot_updated_at: Instant::now(),
            status: Active,
            active_backend_count: 1,
        },
        RoutedClusterSnapshot {
            cluster_id: "cluster-1".to_string(),
            stats: ModelStats::default(),
            rtt: Duration::from_millis(1),
            snapshot_updated_at: Instant::now(),
            status: Active,
            active_backend_count: 1,
        },
    ];
    let first_generation = state.routing.target_state_or_insert(&target).await;

    let first = router
        .choose_candidate(&first_generation.load_balancers, &request, &candidates)
        .expect("first generation should select a candidate");
    let second = router
        .choose_candidate(&first_generation.load_balancers, &request, &candidates)
        .expect("first generation should advance its sequence");
    state
        .routing
        .remove_if_empty(&target, first_generation.clone())
        .await;
    let replacement = state.routing.target_state_or_insert(&target).await;
    let replacement_first = router
        .choose_candidate(&replacement.load_balancers, &request, &candidates)
        .expect("replacement generation should select a candidate");

    assert_eq!(first.candidate_index, 0);
    assert_eq!(second.candidate_index, 1);
    assert!(!Arc::ptr_eq(&first_generation, &replacement));
    assert_eq!(replacement_first.candidate_index, 0);
}

#[tokio::test]
async fn routing_target_snapshot_retains_removed_generation_until_selection_finishes() {
    let state = StargateState::default();
    let target = make_target(Some("rk-snapshot"), "model-snapshot");
    let target_state = state.routing.target_state_or_insert(&target).await;
    let weak_target_state = Arc::downgrade(&target_state);
    let snapshot = state
        .routing_target_snapshot(&target)
        .await
        .expect("existing target should produce a routed snapshot");

    state
        .routing
        .remove_if_empty(&target, target_state.clone())
        .await;
    assert!(state.routing.target_state(&target).await.is_none());
    // Release the test's direct owner so only the in-flight snapshot keeps the old generation alive.
    drop(target_state);
    assert!(weak_target_state.upgrade().is_some());

    // Finishing selection releases the final owner of the removed target generation.
    drop(snapshot);
    assert!(weak_target_state.upgrade().is_none());
}

#[tokio::test]
async fn shared_cluster_registration_exposes_one_aggregated_cluster_candidate() {
    let (scenario, _running_a, _running_b) =
        published_shared_cluster(shared_backend_a_stats(), shared_backend_b_stats(), 10).await;

    let backend_candidates = scenario.candidates("shared-model").await;
    assert_eq!(backend_candidates.len(), 2);

    let cluster = scenario.only_cluster("shared-model").await;
    assert_eq!(cluster.cluster_id, "cluster-shared");
    assert_eq!(cluster.active_backend_count, 2);
    assert_stats!(cluster.stats,
        last_mean_input_tps: 220.0,
        output_tps: 7.0,
        queue_size: 3,
        queued_input_size: 300,
        input_processing_queries: 4,
        output_generation_queries: 6,
        stats_observed_at_unix_ms: 2000,
        stats_capabilities: vec![
            "request.output.chunk_usage".to_string(),
            "machine.kv_cache.http".to_string(),
        ],
        stats_sources: vec!["chunk_usage".to_string(), "kv_cache_stats".to_string()],
        max_output_tps: 60.0,
        kv_cache_capacity_tokens: 2000,
        kv_cache_used_tokens: 500,
        kv_cache_free_tokens: 1500,
        num_running_queries: 7,
        max_engine_concurrency: 77,
        total_query_input_size: 777,
        queue_time_estimate_ms_by_priority: HashMap::from([(1, 222), (2, 333)]),
    );
    assert_eq!(cluster.rtt, Duration::from_micros(7_500));
}

#[tokio::test]
async fn shared_cluster_recomputes_cluster_stats_when_source_backend_is_removed() {
    let (scenario, _running_a, running_b) =
        published_shared_cluster(shared_backend_a_stats(), shared_backend_b_stats(), 10).await;

    let before_removal = scenario.only_cluster("shared-model").await;
    assert_eq!(before_removal.active_backend_count, 2);
    assert_eq!(before_removal.rtt, Duration::from_micros(7_500));

    scenario.state.end_registration(running_b).await;

    let cluster = scenario.only_cluster("shared-model").await;
    assert_eq!(cluster.active_backend_count, 1);
    assert_stats!(cluster.stats,
        last_mean_input_tps: 100.0,
        max_output_tps: 50.0,
        kv_cache_capacity_tokens: 1000,
        kv_cache_used_tokens: 100,
        kv_cache_free_tokens: 900,
        num_running_queries: 11,
        max_engine_concurrency: 111,
        total_query_input_size: 1111,
        queue_time_estimate_ms_by_priority: HashMap::from([(1, 111)]),
    );
    assert_eq!(cluster.rtt, Duration::from_millis(10));
}

#[tokio::test]
async fn registered_backend_rtt_means_drive_cluster_load_balancer_selection() {
    let scenario = RegistrationScenario::new(Some("rk-rtt-routing"));
    let model_id = "model-rtt-routing";
    let outlier_fast = scenario.start_in("outlier-fast", "cluster-outlier", 1111);
    let outlier_slow = scenario.start_in("outlier-slow", "cluster-outlier", 2222);
    let steady_a = scenario.start_in("steady-a", "cluster-steady", 3333);
    let steady_b = scenario.start_in("steady-b", "cluster-steady", 4444);

    scenario
        .publish_default_stats(&outlier_fast, model_id, Active, Some(1))
        .await;
    scenario
        .publish_default_stats(&outlier_slow, model_id, Active, Some(101))
        .await;
    scenario
        .publish_default_stats(&steady_a, model_id, Active, Some(20))
        .await;
    scenario
        .publish_default_stats(&steady_b, model_id, Active, Some(20))
        .await;

    let target = scenario.target(model_id);
    let target_snapshot = scenario
        .state
        .routing_target_snapshot(&target)
        .await
        .expect("registered backends should publish a routable target snapshot");
    let outlier = target_snapshot
        .clusters()
        .iter()
        .find(|cluster| cluster.cluster_id == "cluster-outlier")
        .expect("outlier cluster should be published");
    let steady = target_snapshot
        .clusters()
        .iter()
        .find(|cluster| cluster.cluster_id == "cluster-steady")
        .expect("steady cluster should be published");
    assert_eq!(outlier.rtt, Duration::from_millis(51));
    assert_eq!(steady.rtt, Duration::from_millis(20));

    let algorithm_config = LoadBalancerAlgorithmConfig {
        settings: LoadBalancerAlgorithmSettings::GroqMultiregion(GroqMultiregionAlgorithmConfig {
            // Keep the slower TTFT bucket locked regardless of scheduler delay in the test.
            ttft_bucket_size_ms: Some(0),
            next_bucket_unlock_factor: Some(1_000_000.0),
            n: Some(2),
            ..GroqMultiregionAlgorithmConfig::default()
        }),
        ..LoadBalancerAlgorithmConfig::default()
    };
    let router = LoadBalancerRouter::from_config(&LoadBalancerConfig {
        default: LoadBalancerAlgorithm::PowerOfTwo,
        request_algorithms: HashMap::new(),
        models: HashMap::from([(
            model_id.to_string(),
            LoadBalancerModelConfig::Detailed(Box::new(algorithm_config)),
        )]),
    })
    .expect("Groq multiregion router should initialize");
    let request = LoadBalancerRequest {
        routing_target: &target,
        cache_affinity_key: None,
        input_tokens: None,
        priority: 0,
        received_at: Instant::now(),
        request_slo: None,
        excluded_cluster_ids: None,
    };
    let choice = router
        .choose_candidate(
            target_snapshot.load_balancers(),
            &request,
            target_snapshot.clusters(),
        )
        .expect("one registered cluster should be selected");

    assert_eq!(
        target_snapshot.clusters()[choice.candidate_index].cluster_id,
        "cluster-steady"
    );
}

#[tokio::test]
async fn shared_cluster_selects_active_backends_round_robin() {
    let (scenario, _running_a, _running_b) =
        published_shared_cluster(ModelStats::default(), ModelStats::default(), 5).await;

    let selected_cluster = scenario.selected_cluster("shared-model").await;
    let first = selected_cluster
        .select_backend(&HashSet::new())
        .expect("first backend should be selected");
    let second = selected_cluster
        .select_backend(&HashSet::new())
        .expect("second backend should be selected");
    let third = selected_cluster
        .select_backend(&HashSet::new())
        .expect("third backend should be selected");

    assert_eq!(first.inference_server_id, "inst-a");
    assert_eq!(second.inference_server_id, "inst-b");
    assert_eq!(third.inference_server_id, "inst-a");

    let selected = selected_cluster
        .select_backend(&HashSet::from(["inst-a".to_string()]))
        .expect("remaining backend should be selected");
    assert_eq!(selected.inference_server_id, "inst-b");
}

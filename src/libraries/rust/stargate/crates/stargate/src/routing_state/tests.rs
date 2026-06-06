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

use super::reservations::update_reserved_priority_queue_time;
use super::snapshots::RoutedInferenceServerSnapshotInput;
use super::*;
use stargate_proto::pb::InferenceServerModelRegistration;
use std::collections::HashMap;
use std::sync::Arc;

fn model_registration(status: i32) -> InferenceServerModelRegistration {
    InferenceServerModelRegistration {
        stats: Some(ModelStats::default()),
        status,
    }
}

fn model_registration_with_stats(
    status: i32,
    stats: ModelStats,
) -> InferenceServerModelRegistration {
    InferenceServerModelRegistration {
        stats: Some(stats),
        status,
    }
}

async fn running_registration(
    state: &StargateState,
    id: &str,
    url: &str,
    routing_key: Option<&str>,
) -> RunningRegistration {
    running_registration_in_cluster(state, id, id, url, routing_key).await
}

async fn running_registration_in_cluster(
    state: &StargateState,
    id: &str,
    cluster_id: &str,
    url: &str,
    routing_key: Option<&str>,
) -> RunningRegistration {
    let identity = RegistrationIdentity {
        inference_server_id: id.to_string(),
        cluster_id: cluster_id.to_string(),
        inference_server_url: url.to_string(),
        routing_key: routing_key.map(ToOwned::to_owned),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };
    state.begin_registration(&identity).await.unwrap()
}

async fn running_coordinated_registration_in_cluster(
    state: &StargateState,
    id: &str,
    cluster_id: &str,
    url: &str,
    routing_key: Option<&str>,
) -> RunningRegistration {
    let identity = RegistrationIdentity {
        inference_server_id: id.to_string(),
        cluster_id: cluster_id.to_string(),
        inference_server_url: url.to_string(),
        routing_key: routing_key.map(ToOwned::to_owned),
        reverse_tunnel: false,
        coordinated_calibration: true,
    };
    state.begin_registration(&identity).await.unwrap()
}

fn make_target(routing_key: Option<&str>, model_id: &str) -> RoutingTargetKey {
    RoutingTargetKey {
        routing_key: routing_key.map(ToOwned::to_owned),
        model_id: model_id.to_string(),
    }
}

fn registration_update(
    inference_server_id: &str,
    cluster_id: &str,
    url: &str,
    model_id: &str,
    status: i32,
    stats: ModelStats,
    coordinated_calibration: bool,
) -> InferenceServerRegistration {
    InferenceServerRegistration {
        inference_server_id: inference_server_id.to_string(),
        cluster_id: cluster_id.to_string(),
        inference_server_url: url.to_string(),
        models: HashMap::from([(
            model_id.to_string(),
            model_registration_with_stats(status, stats),
        )]),
        reverse_tunnel: false,
        coordinated_calibration,
    }
}

async fn submit_assigned_calibration(
    state: &StargateState,
    routing_key: Option<&str>,
    inference_server_id: &str,
    cluster_id: &str,
    model_id: &str,
    assignment_token: &str,
    last_mean_input_tps: f64,
) {
    state
        .submit_cluster_calibration(
            routing_key.map(ToOwned::to_owned),
            &SubmitClusterCalibrationRequest {
                inference_server_id: inference_server_id.to_string(),
                cluster_id: cluster_id.to_string(),
                model_id: model_id.to_string(),
                assignment_token: assignment_token.to_string(),
                measured_last_mean_input_tps: last_mean_input_tps,
            },
        )
        .await
        .expect("assigned local calibration result should be accepted");
}

#[tokio::test]
async fn apply_registration_update_removes_models_no_longer_advertised() {
    let state = StargateState::default();
    let mut running =
        running_registration(&state, "inst-1", "quic://127.0.0.1:1234", Some("rk-1")).await;
    let initial_update = InferenceServerRegistration {
        inference_server_id: "inst-1".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:1234".to_string(),
        models: HashMap::from([(
            "model-a".to_string(),
            model_registration(InferenceServerStatus::Active as i32),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };
    state
        .apply_registration_update(
            &mut running,
            &initial_update,
            true,
            Some(Duration::from_millis(10)),
        )
        .await;

    let update = InferenceServerRegistration {
        inference_server_id: "inst-1".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:1234".to_string(),
        models: HashMap::from([(
            "model-b".to_string(),
            model_registration(InferenceServerStatus::Active as i32),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(10)))
        .await;

    assert!(
        state
            .candidates_for_target(&make_target(Some("rk-1"), "model-a"))
            .await
            .is_empty()
    );
    assert_eq!(
        state
            .candidates_for_target(&make_target(Some("rk-1"), "model-b"))
            .await
            .len(),
        1
    );
    assert!(
        state
            .routing
            .target_state(&make_target(Some("rk-1"), "model-a"))
            .await
            .is_none()
    );
}

#[tokio::test]
async fn registered_inactive_model_is_known_without_routable_candidates() {
    let state = StargateState::default();
    let mut running = running_registration(
        &state,
        "inst-known-inactive",
        "quic://127.0.0.1:1234",
        Some("rk-known"),
    )
    .await;
    let target = make_target(Some("rk-known"), "model-known");
    let update = registration_update(
        "inst-known-inactive",
        "inst-known-inactive",
        "quic://127.0.0.1:1234",
        "model-known",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        false,
    );

    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(10)))
        .await;

    assert!(state.has_registered_model_for_target(&target).await);
    assert!(state.candidates_for_target(&target).await.is_empty());
    assert!(
        !state
            .has_registered_model_for_target(&make_target(Some("wrong-rk"), "model-known"))
            .await
    );

    state.end_registration("inst-known-inactive").await;
    assert!(!state.has_registered_model_for_target(&target).await);
}

#[tokio::test]
async fn active_registration_keeps_connection_rtt_in_snapshot() {
    let state = StargateState::default();
    let mut running =
        running_registration(&state, "inst-rtt", "quic://127.0.0.1:7777", Some("rk-rtt")).await;
    let update = InferenceServerRegistration {
        inference_server_id: "inst-rtt".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:7777".to_string(),
        models: HashMap::from([(
            "model-rtt".to_string(),
            model_registration(InferenceServerStatus::Active as i32),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    let expected_rtt = Duration::from_millis(42);
    state
        .apply_registration_update(&mut running, &update, true, Some(expected_rtt))
        .await;

    let candidates = state
        .candidates_for_target(&make_target(Some("rk-rtt"), "model-rtt"))
        .await;
    assert_eq!(candidates.len(), 1);
    assert_eq!(candidates[0].rtt, expected_rtt);
    assert!(matches!(
        candidates[0].delivery_target,
        DeliveryTarget::Local { .. }
    ));
}

#[tokio::test]
async fn coordinated_calibration_assigns_one_owner_and_gates_siblings_until_complete() {
    let state = StargateState::default();
    let mut running_a = running_coordinated_registration_in_cluster(
        &state,
        "inst-a",
        "cluster-cal",
        "quic://127.0.0.1:1111",
        Some("rk-cal"),
    )
    .await;
    let mut running_b = running_coordinated_registration_in_cluster(
        &state,
        "inst-b",
        "cluster-cal",
        "quic://127.0.0.1:2222",
        Some("rk-cal"),
    )
    .await;
    let target = make_target(Some("rk-cal"), "model-cal");

    let update_a = registration_update(
        "inst-a",
        "cluster-cal",
        "quic://127.0.0.1:1111",
        "model-cal",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_a,
            &update_a,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].model_id, "model-cal");
    assert_eq!(directives[0].state, CalibrationState::Run as i32);
    assert!(!directives[0].assignment_token.is_empty());
    let assignment_token = directives[0].assignment_token.clone();

    let update_b_active_without_calibration = registration_update(
        "inst-b",
        "cluster-cal",
        "quic://127.0.0.1:2222",
        "model-cal",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_b,
            &update_b_active_without_calibration,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Waiting as i32);
    assert!(state.candidates_for_target(&target).await.is_empty());

    submit_assigned_calibration(
        &state,
        Some("rk-cal"),
        "inst-a",
        "cluster-cal",
        "model-cal",
        &assignment_token,
        150.0,
    )
    .await;
    let update_a_complete = registration_update(
        "inst-a",
        "cluster-cal",
        "quic://127.0.0.1:1111",
        "model-cal",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_a,
            &update_a_complete,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Complete as i32);

    let update_b_complete = registration_update(
        "inst-b",
        "cluster-cal",
        "quic://127.0.0.1:2222",
        "model-cal",
        InferenceServerStatus::Active as i32,
        ModelStats {
            last_mean_input_tps: 120.0,
            ..ModelStats::default()
        },
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_b,
            &update_b_complete,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Complete as i32);

    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters.len(), 1);
    assert_eq!(clusters[0].active_backend_count, 2);
    assert_eq!(clusters[0].stats.last_mean_input_tps, 150.0);
}

#[tokio::test]
async fn coordinated_calibration_is_not_summed_with_runtime_reports() {
    let state = StargateState::default();
    let mut running_seed = running_coordinated_registration_in_cluster(
        &state,
        "inst-seed",
        "cluster-capacity-source",
        "quic://127.0.0.1:1111",
        Some("rk-capacity-source"),
    )
    .await;
    let mut running_runtime = running_coordinated_registration_in_cluster(
        &state,
        "inst-runtime",
        "cluster-capacity-source",
        "quic://127.0.0.1:2222",
        Some("rk-capacity-source"),
    )
    .await;
    let target = make_target(Some("rk-capacity-source"), "model-capacity-source");

    let seed_update = registration_update(
        "inst-seed",
        "cluster-capacity-source",
        "quic://127.0.0.1:1111",
        "model-capacity-source",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let assignment = state
        .apply_registration_update(
            &mut running_seed,
            &seed_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    submit_assigned_calibration(
        &state,
        Some("rk-capacity-source"),
        "inst-seed",
        "cluster-capacity-source",
        "model-capacity-source",
        &assignment[0].assignment_token,
        150.0,
    )
    .await;
    state
        .apply_registration_update(
            &mut running_seed,
            &seed_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    let runtime_update = registration_update(
        "inst-runtime",
        "cluster-capacity-source",
        "quic://127.0.0.1:2222",
        "model-capacity-source",
        InferenceServerStatus::Active as i32,
        ModelStats {
            last_mean_input_tps: 120.0,
            ..ModelStats::default()
        },
        true,
    );
    state
        .apply_registration_update(
            &mut running_runtime,
            &runtime_update,
            true,
            Some(Duration::from_millis(6)),
        )
        .await;

    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters.len(), 1);
    assert_eq!(
        clusters[0].stats.last_mean_input_tps, 150.0,
        "cluster calibration must remain independent from backend-local runtime capacity"
    );
}

#[tokio::test]
async fn coordinated_calibration_remains_cluster_capacity_floor_after_backends_report_runtime() {
    let state = StargateState::default();
    let mut running_owner = running_coordinated_registration_in_cluster(
        &state,
        "inst-owner",
        "cluster-capacity-floor",
        "quic://127.0.0.1:1111",
        Some("rk-capacity-floor"),
    )
    .await;
    let mut running_peer = running_coordinated_registration_in_cluster(
        &state,
        "inst-peer",
        "cluster-capacity-floor",
        "quic://127.0.0.1:2222",
        Some("rk-capacity-floor"),
    )
    .await;
    let target = make_target(Some("rk-capacity-floor"), "model-capacity-floor");

    let calibration_update = registration_update(
        "inst-owner",
        "cluster-capacity-floor",
        "quic://127.0.0.1:1111",
        "model-capacity-floor",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let assignment = state
        .apply_registration_update(
            &mut running_owner,
            &calibration_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    submit_assigned_calibration(
        &state,
        Some("rk-capacity-floor"),
        "inst-owner",
        "cluster-capacity-floor",
        "model-capacity-floor",
        &assignment[0].assignment_token,
        150.0,
    )
    .await;
    state
        .apply_registration_update(
            &mut running_owner,
            &calibration_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    let owner_runtime_update = registration_update(
        "inst-owner",
        "cluster-capacity-floor",
        "quic://127.0.0.1:1111",
        "model-capacity-floor",
        InferenceServerStatus::Active as i32,
        ModelStats {
            last_mean_input_tps: 50.0,
            ..ModelStats::default()
        },
        true,
    );
    state
        .apply_registration_update(
            &mut running_owner,
            &owner_runtime_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    let peer_runtime_update = registration_update(
        "inst-peer",
        "cluster-capacity-floor",
        "quic://127.0.0.1:2222",
        "model-capacity-floor",
        InferenceServerStatus::Active as i32,
        ModelStats {
            last_mean_input_tps: 40.0,
            ..ModelStats::default()
        },
        true,
    );
    state
        .apply_registration_update(
            &mut running_peer,
            &peer_runtime_update,
            true,
            Some(Duration::from_millis(6)),
        )
        .await;

    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters.len(), 1);
    assert_eq!(
        clusters[0].stats.last_mean_input_tps, 150.0,
        "a completed cluster calibration remains the floor after only smaller runtime observations are available"
    );
}

#[tokio::test]
async fn coordinated_calibration_reassigns_when_owner_disconnects_before_completion() {
    let state = StargateState::default();
    let mut running_a = running_coordinated_registration_in_cluster(
        &state,
        "inst-owner",
        "cluster-reassign",
        "quic://127.0.0.1:1111",
        Some("rk-reassign"),
    )
    .await;
    let mut running_b = running_coordinated_registration_in_cluster(
        &state,
        "inst-next",
        "cluster-reassign",
        "quic://127.0.0.1:2222",
        Some("rk-reassign"),
    )
    .await;

    let update_a = registration_update(
        "inst-owner",
        "cluster-reassign",
        "quic://127.0.0.1:1111",
        "model-reassign",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_a,
            &update_a,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Run as i32);

    state.end_registration("inst-owner").await;

    let update_b = registration_update(
        "inst-next",
        "cluster-reassign",
        "quic://127.0.0.1:2222",
        "model-reassign",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Run as i32);
}

#[tokio::test]
async fn coordinated_calibration_accepts_only_owner_submission() {
    let state = StargateState::default();
    let mut running = running_coordinated_registration_in_cluster(
        &state,
        "inst-precalibrated",
        "cluster-precalibrated",
        "quic://127.0.0.1:1111",
        Some("rk-precalibrated"),
    )
    .await;
    let target = make_target(Some("rk-precalibrated"), "model-precalibrated");

    let update = registration_update(
        "inst-precalibrated",
        "cluster-precalibrated",
        "quic://127.0.0.1:1111",
        "model-precalibrated",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let assignment = state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
        .await;
    assert_eq!(assignment[0].state, CalibrationState::Run as i32);
    submit_assigned_calibration(
        &state,
        Some("rk-precalibrated"),
        "inst-precalibrated",
        "cluster-precalibrated",
        "model-precalibrated",
        &assignment[0].assignment_token,
        123.0,
    )
    .await;
    assert!(
        state
            .cluster_candidates_for_target(&target)
            .await
            .is_empty(),
        "capacity submission alone must not assert backend activity or connectivity"
    );
    let directives = state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
        .await;

    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Complete as i32);
    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters.len(), 1);
    assert_eq!(clusters[0].stats.last_mean_input_tps, 123.0);
}

#[tokio::test]
async fn normal_registration_cannot_complete_another_pylons_assignment() {
    let state = StargateState::default();
    let mut running_sibling = running_coordinated_registration_in_cluster(
        &state,
        "inst-sibling",
        "cluster-fanout",
        "quic://127.0.0.1:2222",
        Some("rk-fanout"),
    )
    .await;
    let mut running_owner = running_coordinated_registration_in_cluster(
        &state,
        "inst-owner",
        "cluster-fanout",
        "quic://127.0.0.1:1111",
        Some("rk-fanout"),
    )
    .await;
    let target = make_target(Some("rk-fanout"), "model-fanout");

    let sibling_update = registration_update(
        "inst-sibling",
        "cluster-fanout",
        "quic://127.0.0.1:2222",
        "model-fanout",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_sibling,
            &sibling_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Run as i32);
    let assignment_token = directives[0].assignment_token.clone();
    assert!(
        state
            .cluster_candidates_for_target(&target)
            .await
            .is_empty(),
        "a RUN assignment must not trigger routing before the result RPC"
    );

    let unassigned_update = registration_update(
        "inst-owner",
        "cluster-fanout",
        "quic://127.0.0.1:1111",
        "model-fanout",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_owner,
            &unassigned_update,
            true,
            Some(Duration::from_millis(6)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Waiting as i32);
    assert!(
        state
            .cluster_candidates_for_target(&target)
            .await
            .is_empty(),
        "normal registration stats from a non-owner must not complete cluster calibration"
    );

    submit_assigned_calibration(
        &state,
        Some("rk-fanout"),
        "inst-sibling",
        "cluster-fanout",
        "model-fanout",
        &assignment_token,
        150.0,
    )
    .await;
    let directives = state
        .apply_registration_update(
            &mut running_owner,
            &unassigned_update,
            true,
            Some(Duration::from_millis(6)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Complete as i32);
    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters.len(), 1);
    assert_eq!(clusters[0].stats.last_mean_input_tps, 150.0);
}

#[tokio::test]
async fn runtime_last_mean_input_tps_without_complete_state_does_not_complete_calibration() {
    let state = StargateState::default();
    let mut running = running_coordinated_registration_in_cluster(
        &state,
        "inst-runtime-only",
        "cluster-runtime-only",
        "quic://127.0.0.1:1111",
        Some("rk-runtime-only"),
    )
    .await;
    let target = make_target(Some("rk-runtime-only"), "model-runtime-only");

    let update_initial = registration_update(
        "inst-runtime-only",
        "cluster-runtime-only",
        "quic://127.0.0.1:1111",
        "model-runtime-only",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running,
            &update_initial,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Run as i32);

    let update_runtime_only = registration_update(
        "inst-runtime-only",
        "cluster-runtime-only",
        "quic://127.0.0.1:1111",
        "model-runtime-only",
        InferenceServerStatus::Active as i32,
        ModelStats {
            last_mean_input_tps: 999.0,
            ..ModelStats::default()
        },
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running,
            &update_runtime_only,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Run as i32);
    assert!(
        state
            .cluster_candidates_for_target(&target)
            .await
            .is_empty()
    );
}

#[tokio::test]
async fn coordinated_calibration_reassigns_when_owner_removes_model_before_completion() {
    let state = StargateState::default();
    let mut running_a = running_coordinated_registration_in_cluster(
        &state,
        "inst-owner",
        "cluster-remove-model",
        "quic://127.0.0.1:1111",
        Some("rk-remove-model"),
    )
    .await;
    let mut running_b = running_coordinated_registration_in_cluster(
        &state,
        "inst-next",
        "cluster-remove-model",
        "quic://127.0.0.1:2222",
        Some("rk-remove-model"),
    )
    .await;

    let update_a = registration_update(
        "inst-owner",
        "cluster-remove-model",
        "quic://127.0.0.1:1111",
        "model-remove",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_a,
            &update_a,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Run as i32);

    let update_b = registration_update(
        "inst-next",
        "cluster-remove-model",
        "quic://127.0.0.1:2222",
        "model-remove",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Waiting as i32);

    let remove_model_update = InferenceServerRegistration {
        inference_server_id: "inst-owner".to_string(),
        cluster_id: "cluster-remove-model".to_string(),
        inference_server_url: "quic://127.0.0.1:1111".to_string(),
        models: HashMap::new(),
        reverse_tunnel: false,
        coordinated_calibration: true,
    };
    let directives = state
        .apply_registration_update(
            &mut running_a,
            &remove_model_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert!(directives.is_empty());

    let directives = state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Run as i32);
}

#[tokio::test]
async fn coordinated_calibration_does_not_complete_from_waiting_backend_stats() {
    let state = StargateState::default();
    let mut running_a = running_coordinated_registration_in_cluster(
        &state,
        "inst-owner",
        "cluster-waiting-stats",
        "quic://127.0.0.1:1111",
        Some("rk-waiting-stats"),
    )
    .await;
    let mut running_b = running_coordinated_registration_in_cluster(
        &state,
        "inst-waiting",
        "cluster-waiting-stats",
        "quic://127.0.0.1:2222",
        Some("rk-waiting-stats"),
    )
    .await;

    let update_a = registration_update(
        "inst-owner",
        "cluster-waiting-stats",
        "quic://127.0.0.1:1111",
        "model-waiting-stats",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_a,
            &update_a,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Run as i32);

    let update_b_active_with_capacity = registration_update(
        "inst-waiting",
        "cluster-waiting-stats",
        "quic://127.0.0.1:2222",
        "model-waiting-stats",
        InferenceServerStatus::Active as i32,
        ModelStats {
            last_mean_input_tps: 999.0,
            ..ModelStats::default()
        },
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_b,
            &update_b_active_with_capacity,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Waiting as i32);

    let remove_model_update = InferenceServerRegistration {
        inference_server_id: "inst-owner".to_string(),
        cluster_id: "cluster-waiting-stats".to_string(),
        inference_server_url: "quic://127.0.0.1:1111".to_string(),
        models: HashMap::new(),
        reverse_tunnel: false,
        coordinated_calibration: true,
    };
    state
        .apply_registration_update(
            &mut running_a,
            &remove_model_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    let directives = state
        .apply_registration_update(
            &mut running_b,
            &update_b_active_with_capacity,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Run as i32);
}

#[tokio::test]
async fn completed_calibration_survives_model_removal_while_cluster_is_registered() {
    let state = StargateState::default();
    let mut running = running_coordinated_registration_in_cluster(
        &state,
        "inst-owner",
        "cluster-readd-model",
        "quic://127.0.0.1:1111",
        Some("rk-readd-model"),
    )
    .await;

    let update_initial = registration_update(
        "inst-owner",
        "cluster-readd-model",
        "quic://127.0.0.1:1111",
        "model-readd",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running,
            &update_initial,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Run as i32);

    let update_complete = registration_update(
        "inst-owner",
        "cluster-readd-model",
        "quic://127.0.0.1:1111",
        "model-readd",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    submit_assigned_calibration(
        &state,
        Some("rk-readd-model"),
        "inst-owner",
        "cluster-readd-model",
        "model-readd",
        &directives[0].assignment_token,
        321.0,
    )
    .await;
    let directives = state
        .apply_registration_update(
            &mut running,
            &update_complete,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Complete as i32);

    let remove_model_update = InferenceServerRegistration {
        inference_server_id: "inst-owner".to_string(),
        cluster_id: "cluster-readd-model".to_string(),
        inference_server_url: "quic://127.0.0.1:1111".to_string(),
        models: HashMap::new(),
        reverse_tunnel: false,
        coordinated_calibration: true,
    };
    let directives = state
        .apply_registration_update(
            &mut running,
            &remove_model_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert!(directives.is_empty());

    let directives = state
        .apply_registration_update(
            &mut running,
            &update_initial,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Complete as i32);
}

#[tokio::test]
async fn coordinated_calibration_keeps_completed_model_while_peer_still_registered() {
    let state = StargateState::default();
    let mut running_a = running_coordinated_registration_in_cluster(
        &state,
        "inst-owner",
        "cluster-keep-model",
        "quic://127.0.0.1:1111",
        Some("rk-keep-model"),
    )
    .await;
    let mut running_b = running_coordinated_registration_in_cluster(
        &state,
        "inst-peer",
        "cluster-keep-model",
        "quic://127.0.0.1:2222",
        Some("rk-keep-model"),
    )
    .await;

    let update_a_initial = registration_update(
        "inst-owner",
        "cluster-keep-model",
        "quic://127.0.0.1:1111",
        "model-keep",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_a,
            &update_a_initial,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Run as i32);

    let update_a_complete = registration_update(
        "inst-owner",
        "cluster-keep-model",
        "quic://127.0.0.1:1111",
        "model-keep",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    submit_assigned_calibration(
        &state,
        Some("rk-keep-model"),
        "inst-owner",
        "cluster-keep-model",
        "model-keep",
        &directives[0].assignment_token,
        654.0,
    )
    .await;
    let directives = state
        .apply_registration_update(
            &mut running_a,
            &update_a_complete,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Complete as i32);

    let update_b = registration_update(
        "inst-peer",
        "cluster-keep-model",
        "quic://127.0.0.1:2222",
        "model-keep",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Complete as i32);

    let remove_model_update = InferenceServerRegistration {
        inference_server_id: "inst-owner".to_string(),
        cluster_id: "cluster-keep-model".to_string(),
        inference_server_url: "quic://127.0.0.1:1111".to_string(),
        models: HashMap::new(),
        reverse_tunnel: false,
        coordinated_calibration: true,
    };
    state
        .apply_registration_update(
            &mut running_a,
            &remove_model_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    let directives = state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Complete as i32);
}

#[tokio::test]
async fn final_cluster_disconnect_drops_local_snapshot_and_completed_calibration() {
    let state = StargateState::default();
    let mut owner = running_coordinated_registration_in_cluster(
        &state,
        "inst-old",
        "cluster-return",
        "quic://127.0.0.1:1111",
        Some("rk-return"),
    )
    .await;
    let target = make_target(Some("rk-return"), "model-return");
    let active_update = registration_update(
        "inst-old",
        "cluster-return",
        "quic://127.0.0.1:1111",
        "model-return",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let assignment = state
        .apply_registration_update(
            &mut owner,
            &active_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(assignment[0].state, CalibrationState::Run as i32);
    let first_token = assignment[0].assignment_token.clone();
    submit_assigned_calibration(
        &state,
        Some("rk-return"),
        "inst-old",
        "cluster-return",
        "model-return",
        &first_token,
        321.0,
    )
    .await;
    state
        .apply_registration_update(
            &mut owner,
            &active_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(
        state.cluster_candidates_for_target(&target).await[0]
            .stats
            .last_mean_input_tps,
        321.0
    );

    state.end_registration("inst-old").await;
    assert!(
        state
            .cluster_candidates_for_target(&target)
            .await
            .is_empty()
    );

    let mut replacement = running_coordinated_registration_in_cluster(
        &state,
        "inst-new",
        "cluster-return",
        "quic://127.0.0.1:2222",
        Some("rk-return"),
    )
    .await;
    let replacement_update = registration_update(
        "inst-new",
        "cluster-return",
        "quic://127.0.0.1:2222",
        "model-return",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut replacement,
            &replacement_update,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Run as i32);
    assert_ne!(directives[0].assignment_token, first_token);
    assert!(
        state
            .cluster_candidates_for_target(&target)
            .await
            .is_empty()
    );
}

#[tokio::test]
async fn completed_calibration_floor_remains_until_final_local_registration_drops() {
    let state = StargateState::default();
    let mut running_coordinated = running_coordinated_registration_in_cluster(
        &state,
        "inst-coordinated",
        "cluster-clear-capacity",
        "quic://127.0.0.1:1111",
        Some("rk-clear-capacity"),
    )
    .await;
    let mut running_local = running_registration_in_cluster(
        &state,
        "inst-local",
        "cluster-clear-capacity",
        "quic://127.0.0.1:2222",
        Some("rk-clear-capacity"),
    )
    .await;
    let target = make_target(Some("rk-clear-capacity"), "model-clear-capacity");

    let update_initial = registration_update(
        "inst-coordinated",
        "cluster-clear-capacity",
        "quic://127.0.0.1:1111",
        "model-clear-capacity",
        InferenceServerStatus::Inactive as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut running_coordinated,
            &update_initial,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    assert_eq!(directives[0].state, CalibrationState::Run as i32);
    let first_token = directives[0].assignment_token.clone();

    let update_complete = registration_update(
        "inst-coordinated",
        "cluster-clear-capacity",
        "quic://127.0.0.1:1111",
        "model-clear-capacity",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    submit_assigned_calibration(
        &state,
        Some("rk-clear-capacity"),
        "inst-coordinated",
        "cluster-clear-capacity",
        "model-clear-capacity",
        &first_token,
        777.0,
    )
    .await;
    state
        .apply_registration_update(
            &mut running_coordinated,
            &update_complete,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    let update_local = registration_update(
        "inst-local",
        "cluster-clear-capacity",
        "quic://127.0.0.1:2222",
        "model-clear-capacity",
        InferenceServerStatus::Active as i32,
        ModelStats {
            last_mean_input_tps: 100.0,
            ..ModelStats::default()
        },
        false,
    );
    state
        .apply_registration_update(
            &mut running_local,
            &update_local,
            true,
            Some(Duration::from_millis(6)),
        )
        .await;

    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters.len(), 1);
    assert_eq!(clusters[0].stats.last_mean_input_tps, 777.0);

    let remove_model_update = InferenceServerRegistration {
        inference_server_id: "inst-coordinated".to_string(),
        cluster_id: "cluster-clear-capacity".to_string(),
        inference_server_url: "quic://127.0.0.1:1111".to_string(),
        models: HashMap::new(),
        reverse_tunnel: false,
        coordinated_calibration: true,
    };
    state
        .apply_registration_update(
            &mut running_coordinated,
            &remove_model_update,
            true,
            Some(Duration::from_millis(7)),
        )
        .await;

    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters.len(), 1);
    assert_eq!(clusters[0].active_backend_count, 1);
    assert_eq!(clusters[0].stats.last_mean_input_tps, 777.0);

    state.end_registration("inst-coordinated").await;
    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters.len(), 1);
    assert_eq!(clusters[0].stats.last_mean_input_tps, 777.0);

    state.end_registration("inst-local").await;
    assert!(
        state
            .cluster_candidates_for_target(&target)
            .await
            .is_empty()
    );

    let mut replacement = running_coordinated_registration_in_cluster(
        &state,
        "inst-replacement",
        "cluster-clear-capacity",
        "quic://127.0.0.1:3333",
        Some("rk-clear-capacity"),
    )
    .await;
    let replacement_update = registration_update(
        "inst-replacement",
        "cluster-clear-capacity",
        "quic://127.0.0.1:3333",
        "model-clear-capacity",
        InferenceServerStatus::Active as i32,
        ModelStats::default(),
        true,
    );
    let directives = state
        .apply_registration_update(
            &mut replacement,
            &replacement_update,
            true,
            Some(Duration::from_millis(8)),
        )
        .await;
    assert_eq!(directives.len(), 1);
    assert_eq!(directives[0].state, CalibrationState::Run as i32);
    assert_ne!(directives[0].assignment_token, first_token);
}

#[tokio::test]
async fn reservation_updates_local_snapshot_until_next_registration_update() {
    let state = StargateState::default();
    let mut running =
        running_registration(&state, "inst-res", "quic://127.0.0.1:8888", Some("rk-res")).await;
    let target = make_target(Some("rk-res"), "model-res");
    let update = InferenceServerRegistration {
        inference_server_id: "inst-res".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:8888".to_string(),
        models: HashMap::from([(
            "model-res".to_string(),
            InferenceServerModelRegistration {
                stats: Some(ModelStats {
                    last_mean_input_tps: 100.0,
                    max_engine_concurrency: 8,
                    queue_time_estimate_ms_by_priority: HashMap::from([(4, 5)]),
                    ..ModelStats::default()
                }),
                status: InferenceServerStatus::Active.into(),
            },
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
        .await;
    let _reservation = state
        .reserve_inference_server_for_target(&target, "inst-res", Some(37), 4)
        .await;

    let candidates = state.cluster_candidates_for_target(&target).await;
    assert_eq!(candidates[0].stats.num_running_queries, 1);
    assert_eq!(candidates[0].stats.queue_size, 1);
    assert_eq!(candidates[0].stats.total_query_input_size, 37);
    assert_eq!(candidates[0].stats.queued_input_size, 37);
    assert_eq!(
        candidates[0]
            .stats
            .queue_time_estimate_ms_by_priority
            .get(&4),
        Some(&375)
    );

    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
        .await;

    let candidates = state.cluster_candidates_for_target(&target).await;
    assert_eq!(candidates[0].stats.num_running_queries, 0);
    assert_eq!(candidates[0].stats.queue_size, 0);
    assert_eq!(candidates[0].stats.total_query_input_size, 0);
    assert_eq!(candidates[0].stats.queued_input_size, 0);
    assert_eq!(
        candidates[0]
            .stats
            .queue_time_estimate_ms_by_priority
            .get(&4),
        Some(&5)
    );

    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters[0].stats.num_running_queries, 0);
    assert_eq!(clusters[0].stats.queue_size, 0);
    assert_eq!(clusters[0].stats.total_query_input_size, 0);
    assert_eq!(clusters[0].stats.queued_input_size, 0);
    assert_eq!(
        clusters[0].stats.queue_time_estimate_ms_by_priority.get(&4),
        Some(&5)
    );
}

#[tokio::test]
async fn released_reservation_restores_local_snapshot_before_registration_update() {
    let state = StargateState::default();
    let mut running = running_registration(
        &state,
        "inst-release",
        "quic://127.0.0.1:8888",
        Some("rk-release"),
    )
    .await;
    let target = make_target(Some("rk-release"), "model-release");
    let update = InferenceServerRegistration {
        inference_server_id: "inst-release".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:8888".to_string(),
        models: HashMap::from([(
            "model-release".to_string(),
            InferenceServerModelRegistration {
                stats: Some(ModelStats {
                    last_mean_input_tps: 100.0,
                    queue_time_estimate_ms_by_priority: HashMap::from([(4, 5)]),
                    ..ModelStats::default()
                }),
                status: InferenceServerStatus::Active.into(),
            },
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
        .await;
    let reservation = state
        .reserve_inference_server_for_target(&target, "inst-release", Some(37), 4)
        .await
        .expect("active backend should accept reservation");

    state
        .release_inference_server_reservation_for_target(&target, reservation)
        .await;

    let candidates = state.cluster_candidates_for_target(&target).await;
    assert_eq!(candidates[0].stats.num_running_queries, 0);
    assert_eq!(candidates[0].stats.total_query_input_size, 0);
    assert_eq!(
        candidates[0]
            .stats
            .queue_time_estimate_ms_by_priority
            .get(&4),
        Some(&5)
    );

    let consumed_by_heartbeat = state
        .reserve_inference_server_for_target(&target, "inst-release", Some(10), 4)
        .await
        .expect("active backend should accept reservation");
    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
        .await;
    let still_pending = state
        .reserve_inference_server_for_target(&target, "inst-release", Some(20), 4)
        .await
        .expect("active backend should accept reservation");

    state
        .release_inference_server_reservation_for_target(&target, consumed_by_heartbeat)
        .await;
    let candidates = state.cluster_candidates_for_target(&target).await;
    assert_eq!(candidates[0].stats.num_running_queries, 1);
    assert_eq!(candidates[0].stats.total_query_input_size, 20);
    assert_eq!(
        candidates[0]
            .stats
            .queue_time_estimate_ms_by_priority
            .get(&4),
        Some(&205)
    );

    state
        .release_inference_server_reservation_for_target(&target, still_pending)
        .await;
    let candidates = state.cluster_candidates_for_target(&target).await;
    assert_eq!(candidates[0].stats.num_running_queries, 0);
    assert_eq!(candidates[0].stats.total_query_input_size, 0);
    assert_eq!(
        candidates[0]
            .stats
            .queue_time_estimate_ms_by_priority
            .get(&4),
        Some(&5)
    );
}

#[tokio::test]
async fn shared_cluster_reservation_updates_cluster_snapshot_even_when_other_backend_is_latest() {
    let state = StargateState::default();
    let mut running_a = running_registration_in_cluster(
        &state,
        "inst-a",
        "cluster-reserved",
        "quic://127.0.0.1:1111",
        Some("rk-reserved"),
    )
    .await;
    let mut running_b = running_registration_in_cluster(
        &state,
        "inst-b",
        "cluster-reserved",
        "quic://127.0.0.1:2222",
        Some("rk-reserved"),
    )
    .await;
    let target = make_target(Some("rk-reserved"), "model-reserved");

    let update_a = InferenceServerRegistration {
        inference_server_id: "inst-a".to_string(),
        cluster_id: "cluster-reserved".to_string(),
        inference_server_url: "quic://127.0.0.1:1111".to_string(),
        models: HashMap::from([(
            "model-reserved".to_string(),
            model_registration_with_stats(
                InferenceServerStatus::Active as i32,
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
            ),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };
    let update_b = InferenceServerRegistration {
        inference_server_id: "inst-b".to_string(),
        cluster_id: "cluster-reserved".to_string(),
        inference_server_url: "quic://127.0.0.1:2222".to_string(),
        models: HashMap::from([(
            "model-reserved".to_string(),
            model_registration_with_stats(
                InferenceServerStatus::Active as i32,
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
            ),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(
            &mut running_a,
            &update_a,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    let _reservation = state
        .reserve_inference_server_for_target(&target, "inst-a", Some(37), 4)
        .await;

    state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters.len(), 1);
    let cluster = &clusters[0];
    assert_eq!(cluster.stats.queue_size, 1);
    assert_eq!(cluster.stats.queued_input_size, 37);
    assert_eq!(cluster.stats.num_running_queries, 8);
    assert_eq!(cluster.stats.total_query_input_size, 107);
    // Reservation delta uses summed backend input capacity:
    // existing 5ms + ceil(37 tokens / 200 input TPS * 1000) = 190ms.
    assert_eq!(
        cluster.stats.queue_time_estimate_ms_by_priority.get(&4),
        Some(&190)
    );
}

#[tokio::test]
async fn reservation_inserts_request_priority_and_preserves_more_urgent_bucket() {
    let state = StargateState::default();
    let mut running = running_registration(
        &state,
        "inst-priority-res",
        "quic://127.0.0.1:8888",
        Some("rk-priority-res"),
    )
    .await;
    let target = make_target(Some("rk-priority-res"), "model-priority-res");
    let update = InferenceServerRegistration {
        inference_server_id: "inst-priority-res".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:8888".to_string(),
        models: HashMap::from([(
            "model-priority-res".to_string(),
            InferenceServerModelRegistration {
                stats: Some(ModelStats {
                    last_mean_input_tps: 100.0,
                    queue_time_estimate_ms_by_priority: HashMap::from([(2, 5)]),
                    ..ModelStats::default()
                }),
                status: InferenceServerStatus::Active.into(),
            },
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
        .await;
    let _reservation = state
        .reserve_inference_server_for_target(&target, "inst-priority-res", Some(10), 3)
        .await;

    let candidates = state.cluster_candidates_for_target(&target).await;
    assert_eq!(
        candidates[0]
            .stats
            .queue_time_estimate_ms_by_priority
            .get(&2),
        Some(&5)
    );
    assert_eq!(
        candidates[0]
            .stats
            .queue_time_estimate_ms_by_priority
            .get(&3),
        Some(&105)
    );
}

#[tokio::test]
async fn reservation_updates_lower_urgency_cumulative_priority_buckets() {
    let state = StargateState::default();
    let mut running = running_registration(
        &state,
        "inst-priority-cumulative",
        "quic://127.0.0.1:8888",
        Some("rk-priority-cumulative"),
    )
    .await;
    let target = make_target(Some("rk-priority-cumulative"), "model-priority-cumulative");
    let update = InferenceServerRegistration {
        inference_server_id: "inst-priority-cumulative".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:8888".to_string(),
        models: HashMap::from([(
            "model-priority-cumulative".to_string(),
            InferenceServerModelRegistration {
                stats: Some(ModelStats {
                    last_mean_input_tps: 100.0,
                    queue_time_estimate_ms_by_priority: HashMap::from([(1, 10), (4, 40)]),
                    ..ModelStats::default()
                }),
                status: InferenceServerStatus::Active.into(),
            },
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
        .await;
    let _reservation = state
        .reserve_inference_server_for_target(&target, "inst-priority-cumulative", Some(10), 2)
        .await;

    let candidates = state.cluster_candidates_for_target(&target).await;
    let priority_estimates = &candidates[0].stats.queue_time_estimate_ms_by_priority;
    assert_eq!(priority_estimates.get(&1), Some(&10));
    assert_eq!(priority_estimates.get(&2), Some(&110));
    assert_eq!(priority_estimates.get(&4), Some(&140));
}

#[test]
fn reservation_updates_existing_request_priority_and_lower_urgency_buckets() {
    let mut stats = ModelStats {
        last_mean_input_tps: 100.0,
        queue_time_estimate_ms_by_priority: HashMap::from([(1, 10), (2, 100), (4, 400)]),
        ..ModelStats::default()
    };

    update_reserved_priority_queue_time(&mut stats, 10, 2);

    let priority_estimates = &stats.queue_time_estimate_ms_by_priority;
    assert_eq!(priority_estimates.get(&1), Some(&10));
    assert_eq!(priority_estimates.get(&2), Some(&200));
    assert_eq!(priority_estimates.get(&4), Some(&500));
}

#[test]
fn reservation_saturates_priority_queue_estimates() {
    let mut stats = ModelStats {
        last_mean_input_tps: 1.0,
        queue_time_estimate_ms_by_priority: HashMap::from([(0, u64::MAX - 1), (2, u64::MAX - 2)]),
        ..ModelStats::default()
    };

    update_reserved_priority_queue_time(&mut stats, 10, 0);

    let priority_estimates = &stats.queue_time_estimate_ms_by_priority;
    assert_eq!(priority_estimates.get(&0), Some(&u64::MAX));
    assert_eq!(priority_estimates.get(&2), Some(&u64::MAX));
}

#[tokio::test]
async fn reservation_clears_priority_map_when_delta_cannot_be_computed() {
    let mut stats = ModelStats {
        last_mean_input_tps: 0.0,
        queue_time_estimate_ms_by_priority: HashMap::from([(1, 10), (4, 40)]),
        ..ModelStats::default()
    };

    update_reserved_priority_queue_time(&mut stats, 10, 2);

    assert!(stats.queue_time_estimate_ms_by_priority.is_empty());
}

#[test]
fn queue_time_estimate_helper_uses_sparse_priority_and_aggregate_fallback() {
    let priority_stats = ModelStats {
        last_mean_input_tps: 100.0,
        queued_input_size: 25,
        queue_time_estimate_ms_by_priority: HashMap::from([(1, 10), (4, 40)]),
        ..ModelStats::default()
    };
    assert_eq!(
        crate::queue_estimate::queue_time_estimate_ms_for_priority(&priority_stats, 3),
        Some(10)
    );

    let aggregate_stats = ModelStats {
        last_mean_input_tps: 100.0,
        queued_input_size: 25,
        ..ModelStats::default()
    };
    assert_eq!(
        crate::queue_estimate::queue_time_estimate_ms_for_priority(&aggregate_stats, 3),
        Some(250)
    );

    let invalid_capacity_stats = ModelStats {
        last_mean_input_tps: 0.0,
        queued_input_size: 25,
        ..ModelStats::default()
    };
    assert_eq!(
        crate::queue_estimate::queue_time_estimate_ms_for_priority(&invalid_capacity_stats, 3),
        None
    );
}

#[test]
fn queue_time_estimate_helper_treats_lower_priority_only_work_as_known_zero() {
    let stats = ModelStats {
        last_mean_input_tps: 100.0,
        queued_input_size: 25,
        queue_time_estimate_ms_by_priority: HashMap::from([(4, 250)]),
        ..ModelStats::default()
    };

    assert_eq!(
        crate::queue_estimate::queue_time_estimate_ms_for_priority(&stats, 0),
        Some(0)
    );
}

#[tokio::test]
async fn reservation_inserts_high_priority_estimate_when_only_lower_priority_work_exists() {
    let state = StargateState::default();
    let mut running = running_registration(
        &state,
        "inst-priority-clear",
        "quic://127.0.0.1:8888",
        Some("rk-priority-clear"),
    )
    .await;
    let target = make_target(Some("rk-priority-clear"), "model-priority-clear");
    let update = InferenceServerRegistration {
        inference_server_id: "inst-priority-clear".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:8888".to_string(),
        models: HashMap::from([(
            "model-priority-clear".to_string(),
            InferenceServerModelRegistration {
                stats: Some(ModelStats {
                    last_mean_input_tps: 100.0,
                    queue_time_estimate_ms_by_priority: HashMap::from([(4, 5)]),
                    ..ModelStats::default()
                }),
                status: InferenceServerStatus::Active as i32,
            },
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(5)))
        .await;
    let _reservation = state
        .reserve_inference_server_for_target(&target, "inst-priority-clear", Some(10), 0)
        .await;

    let candidates = state.cluster_candidates_for_target(&target).await;
    assert_eq!(
        candidates[0].stats.queue_time_estimate_ms_by_priority,
        HashMap::from([(0, 100), (4, 105)])
    );
}

#[tokio::test]
async fn inactive_registration_is_not_routable() {
    let state = StargateState::default();
    let mut running = running_registration(
        &state,
        "inst-inactive",
        "quic://127.0.0.1:9999",
        Some("rk-in"),
    )
    .await;
    let update = InferenceServerRegistration {
        inference_server_id: "inst-inactive".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:9999".to_string(),
        models: HashMap::from([(
            "model-r".to_string(),
            model_registration(InferenceServerStatus::Inactive as i32),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(7)))
        .await;
    assert!(
        state
            .candidates_for_target(&make_target(Some("rk-in"), "model-r"))
            .await
            .is_empty()
    );
}

#[tokio::test]
async fn active_models_snapshot_refresh_reports_routable_models() {
    let state = StargateState::default();
    let mut active = running_registration(
        &state,
        "inst-active",
        "quic://127.0.0.1:1111",
        Some("rk-list"),
    )
    .await;
    let mut active_without_rtt = running_registration(
        &state,
        "inst-no-rtt",
        "quic://127.0.0.1:2222",
        Some("rk-list"),
    )
    .await;
    let mut inactive = running_registration(
        &state,
        "inst-inactive-list",
        "quic://127.0.0.1:3333",
        Some("rk-list"),
    )
    .await;

    state
        .apply_registration_update(
            &mut active,
            &InferenceServerRegistration {
                inference_server_id: "inst-active".to_string(),
                cluster_id: String::new(),
                inference_server_url: "quic://127.0.0.1:1111".to_string(),
                models: HashMap::from([(
                    "model-listed".to_string(),
                    model_registration(InferenceServerStatus::Active as i32),
                )]),
                reverse_tunnel: false,
                coordinated_calibration: false,
            },
            true,
            Some(Duration::from_millis(7)),
        )
        .await;
    state
        .apply_registration_update(
            &mut active_without_rtt,
            &InferenceServerRegistration {
                inference_server_id: "inst-no-rtt".to_string(),
                cluster_id: String::new(),
                inference_server_url: "quic://127.0.0.1:2222".to_string(),
                models: HashMap::from([(
                    "model-not-ready".to_string(),
                    model_registration(InferenceServerStatus::Active as i32),
                )]),
                reverse_tunnel: false,
                coordinated_calibration: false,
            },
            true,
            None,
        )
        .await;
    state
        .apply_registration_update(
            &mut inactive,
            &InferenceServerRegistration {
                inference_server_id: "inst-inactive-list".to_string(),
                cluster_id: String::new(),
                inference_server_url: "quic://127.0.0.1:3333".to_string(),
                models: HashMap::from([(
                    "model-inactive".to_string(),
                    model_registration(InferenceServerStatus::Inactive as i32),
                )]),
                reverse_tunnel: false,
                coordinated_calibration: false,
            },
            true,
            Some(Duration::from_millis(7)),
        )
        .await;

    let before_refresh = state.list_active_models(Some("rk-list"), &[]);
    assert!(
        before_refresh.is_empty(),
        "registration updates must not maintain the ListModels snapshot: {before_refresh:?}"
    );

    state.refresh_active_models_snapshot().await;

    let models = state.list_active_models(Some("rk-list"), &[]);
    assert_eq!(models.len(), 1);
    assert_eq!(models[0], "model-listed");

    let filtered = state.list_active_models(Some("rk-list"), &["model-not-ready".to_string()]);
    assert!(filtered.is_empty(), "got: {filtered:?}");
}

#[tokio::test]
async fn active_models_snapshot_uses_routable_cluster_snapshots() {
    let state = StargateState::default();
    let target = make_target(Some("rk-intermediate"), "model-intermediate");
    let target_state = state.routing.target_state_or_insert(&target).await;
    let cluster_state =
        RoutingLifecycle::cluster_state_or_insert(&target_state, "cluster-intermediate").await;

    let _ = cluster_state
        .inference_servers
        .upsert_async(
            "backend-intermediate".to_string(),
            RoutedInferenceServerSnapshot {
                cluster_id: "cluster-intermediate".to_string(),
                inference_server_id: "backend-intermediate".to_string(),
                inference_server_url: "quic://127.0.0.1:4444".to_string(),
                stats: ModelStats::default(),
                rtt: Duration::from_millis(5),
                snapshot_updated_at: Instant::now(),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                delivery_target: DeliveryTarget::Local {
                    inference_server_id: "backend-intermediate".to_string(),
                },
            },
        )
        .await;

    assert!(
        state
            .cluster_candidates_for_target(&target)
            .await
            .is_empty(),
        "proxy routing source of truth should not consider this intermediate target routable"
    );

    state.refresh_active_models_snapshot().await;
    let listed = state.list_active_models(Some("rk-intermediate"), &[]);
    assert!(
        listed.is_empty(),
        "ListModels snapshot must not advertise targets without routable cluster snapshots: {listed:?}"
    );
}

#[tokio::test]
async fn active_models_snapshot_filters_by_routing_key() {
    let state = StargateState::default();
    let mut running_a = running_registration(
        &state,
        "inst-list-rk-a",
        "quic://127.0.0.1:1111",
        Some("rk-a"),
    )
    .await;
    let mut running_b = running_registration(
        &state,
        "inst-list-rk-b",
        "quic://127.0.0.1:2222",
        Some("rk-b"),
    )
    .await;

    let update_a = InferenceServerRegistration {
        inference_server_id: "inst-list-rk-a".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:1111".to_string(),
        models: HashMap::from([(
            "shared-list-model".to_string(),
            model_registration(InferenceServerStatus::Active as i32),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };
    let update_b = InferenceServerRegistration {
        inference_server_id: "inst-list-rk-b".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:2222".to_string(),
        models: HashMap::from([(
            "shared-list-model".to_string(),
            model_registration(InferenceServerStatus::Active as i32),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(
            &mut running_a,
            &update_a,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    state.refresh_active_models_snapshot().await;

    let unscoped = state.list_active_models(None, &[]);
    assert!(
        unscoped.is_empty(),
        "unscoped ListModels must not include keyed registrations: {unscoped:?}"
    );

    let models_a = state.list_active_models(Some("rk-a"), &[]);
    assert_eq!(models_a, vec!["shared-list-model"]);

    let models_b = state.list_active_models(Some("rk-b"), &[]);
    assert_eq!(models_b, vec!["shared-list-model"]);

    let wrong_key = state.list_active_models(Some("rk-c"), &[]);
    assert!(
        wrong_key.is_empty(),
        "ListModels must not leak models across routing keys: {wrong_key:?}"
    );

    let filtered = state.list_active_models(Some("rk-a"), &["shared-list-model".to_string()]);
    assert_eq!(filtered, vec!["shared-list-model"]);
}

#[tokio::test]
async fn active_models_snapshot_is_eventually_consistent() {
    let state = StargateState::default();
    let target = make_target(None, "model-list-eventual");

    state
        .routing
        .upsert_inference_server_target(
            &target,
            RoutedInferenceServerSnapshotInput {
                cluster_id: "cluster-list-eventual",
                inference_server_id: "backend-list-eventual",
                inference_server_url: "quic://127.0.0.1:2222",
                stats: ModelStats::default(),
                rtt: Duration::from_millis(5),
                snapshot_updated_at: Instant::now(),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                delivery_target: DeliveryTarget::Local {
                    inference_server_id: "backend-list-eventual".to_string(),
                },
            },
            &state.calibration_assignments,
        )
        .await;

    assert!(
        state.list_active_models(None, &[]).is_empty(),
        "registration updates should not synchronously refresh the discovery snapshot"
    );

    state.refresh_active_models_snapshot().await;
    let listed = state.list_active_models(None, &[]);
    assert_eq!(listed.len(), 1, "got: {listed:?}");
    assert_eq!(listed[0], "model-list-eventual");

    state
        .routing
        .remove_inference_server_from_target(
            "backend-list-eventual",
            "cluster-list-eventual",
            &target,
            &state.calibration_assignments,
        )
        .await;
    let before_refresh = state.list_active_models(None, &[]);
    assert_eq!(
        before_refresh.len(),
        1,
        "snapshot should remain stale until the next refresh tick: {before_refresh:?}"
    );

    state.refresh_active_models_snapshot().await;
    let after_refresh = state.list_active_models(None, &[]);
    assert!(
        after_refresh.is_empty(),
        "removed model should disappear after snapshot refresh: {after_refresh:?}"
    );
}

#[tokio::test]
async fn different_routing_keys_isolate_candidates() {
    let state = StargateState::default();
    let mut running_a =
        running_registration(&state, "inst-a", "quic://127.0.0.1:1111", Some("rk-a")).await;
    let mut running_b =
        running_registration(&state, "inst-b", "quic://127.0.0.1:2222", Some("rk-b")).await;

    let update_a = InferenceServerRegistration {
        inference_server_id: "inst-a".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:1111".to_string(),
        models: HashMap::from([(
            "shared-model".to_string(),
            model_registration(InferenceServerStatus::Active as i32),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };
    let update_b = InferenceServerRegistration {
        inference_server_id: "inst-b".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:2222".to_string(),
        models: HashMap::from([(
            "shared-model".to_string(),
            model_registration(InferenceServerStatus::Active as i32),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(
            &mut running_a,
            &update_a,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;
    state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    let candidates_a = state
        .candidates_for_target(&make_target(Some("rk-a"), "shared-model"))
        .await;
    let candidates_b = state
        .candidates_for_target(&make_target(Some("rk-b"), "shared-model"))
        .await;
    assert_eq!(candidates_a.len(), 1);
    assert_eq!(candidates_b.len(), 1);
    assert_eq!(candidates_a[0].inference_server_id, "inst-a");
    assert_eq!(candidates_b[0].inference_server_id, "inst-b");
}

#[tokio::test]
async fn apply_registration_update_survives_poisoned_last_rtt_lock() {
    let state = StargateState::default();
    let mut running =
        running_registration(&state, "inst-poison", "quic://127.0.0.1:3333", Some("rk-p")).await;
    let update = InferenceServerRegistration {
        inference_server_id: "inst-poison".to_string(),
        cluster_id: String::new(),
        inference_server_url: "quic://127.0.0.1:3333".to_string(),
        models: HashMap::from([(
            "model-p".to_string(),
            model_registration(InferenceServerStatus::Active as i32),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    let poison = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let _guard = running.registration_state.last_rtt.lock();
        panic!("poison last_rtt lock");
    }));
    assert!(poison.is_err());

    state
        .apply_registration_update(&mut running, &update, true, Some(Duration::from_millis(9)))
        .await;

    let candidates = state
        .candidates_for_target(&make_target(Some("rk-p"), "model-p"))
        .await;
    assert_eq!(candidates.len(), 1);
    assert_eq!(candidates[0].rtt, Duration::from_millis(9));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn target_state_or_insert_does_not_panic_during_concurrent_remove() {
    let state = Arc::new(StargateState::default());
    let target = make_target(Some("rk-race"), "model-race");

    let mut writers = Vec::new();
    for _ in 0..8 {
        let state = Arc::clone(&state);
        let target = target.clone();
        writers.push(tokio::spawn(async move {
            for _ in 0..20_000 {
                let _ = state.routing.target_state_or_insert(&target).await;
                tokio::task::yield_now().await;
            }
        }));
    }

    let remover = {
        let state = Arc::clone(&state);
        let target = target.clone();
        tokio::spawn(async move {
            for _ in 0..20_000 {
                let _ = state.routing.targets.targets.remove_async(&target).await;
                tokio::task::yield_now().await;
            }
        })
    };

    for writer in writers {
        writer.await.unwrap();
    }
    remover.await.unwrap();
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn cluster_state_or_insert_does_not_panic_during_concurrent_remove() {
    let target_state = Arc::new(RoutingTargetState::default());
    let cluster_id = "cluster-race";

    let mut writers = Vec::new();
    for _ in 0..8 {
        let target_state = Arc::clone(&target_state);
        writers.push(tokio::spawn(async move {
            for _ in 0..20_000 {
                let _ = RoutingLifecycle::cluster_state_or_insert(&target_state, cluster_id).await;
                tokio::task::yield_now().await;
            }
        }));
    }

    let remover = {
        let target_state = Arc::clone(&target_state);
        tokio::spawn(async move {
            for _ in 0..20_000 {
                let _ = target_state.clusters.remove_async(cluster_id).await;
                tokio::task::yield_now().await;
            }
        })
    };

    for writer in writers {
        writer.await.unwrap();
    }
    remover.await.unwrap();
}

#[tokio::test]
async fn shared_cluster_registration_exposes_one_aggregated_cluster_candidate() {
    let state = StargateState::default();
    let mut running_a = running_registration_in_cluster(
        &state,
        "inst-a",
        "cluster-shared",
        "quic://127.0.0.1:1111",
        Some("rk-a"),
    )
    .await;
    let mut running_b = running_registration_in_cluster(
        &state,
        "inst-b",
        "cluster-shared",
        "quic://127.0.0.1:2222",
        Some("rk-a"),
    )
    .await;

    let update_a = InferenceServerRegistration {
        inference_server_id: "inst-a".to_string(),
        cluster_id: "cluster-shared".to_string(),
        inference_server_url: "quic://127.0.0.1:1111".to_string(),
        models: HashMap::from([(
            "shared-model".to_string(),
            model_registration_with_stats(
                InferenceServerStatus::Active as i32,
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
                },
            ),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };
    let update_b = InferenceServerRegistration {
        inference_server_id: "inst-b".to_string(),
        cluster_id: "cluster-shared".to_string(),
        inference_server_url: "quic://127.0.0.1:2222".to_string(),
        models: HashMap::from([(
            "shared-model".to_string(),
            model_registration_with_stats(
                InferenceServerStatus::Active as i32,
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
                },
            ),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(
            &mut running_a,
            &update_a,
            true,
            Some(Duration::from_millis(10)),
        )
        .await;
    state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    let target = make_target(Some("rk-a"), "shared-model");
    let backend_candidates = state.candidates_for_target(&target).await;
    assert_eq!(backend_candidates.len(), 2);

    let clusters = state.cluster_candidates_for_target(&target).await;
    assert_eq!(clusters.len(), 1);
    let cluster = &clusters[0];
    assert_eq!(cluster.cluster_id, "cluster-shared");
    assert_eq!(cluster.active_backend_count, 2);
    assert_eq!(cluster.stats.last_mean_input_tps, 220.0);
    assert_eq!(cluster.stats.output_tps, 7.0);
    assert_eq!(cluster.stats.queue_size, 3);
    assert_eq!(cluster.stats.queued_input_size, 300);
    assert_eq!(cluster.stats.input_processing_queries, 4);
    assert_eq!(cluster.stats.output_generation_queries, 6);
    assert_eq!(cluster.stats.stats_observed_at_unix_ms, 2000);
    assert_eq!(
        cluster.stats.stats_capabilities,
        vec![
            "request.output.chunk_usage".to_string(),
            "machine.kv_cache.http".to_string(),
        ]
    );
    assert_eq!(
        cluster.stats.stats_sources,
        vec!["chunk_usage".to_string(), "kv_cache_stats".to_string()]
    );
    assert_eq!(cluster.stats.max_output_tps, 60.0);
    assert_eq!(cluster.stats.kv_cache_capacity_tokens, 2000);
    assert_eq!(cluster.stats.kv_cache_used_tokens, 500);
    assert_eq!(cluster.stats.kv_cache_free_tokens, 1500);
    assert_eq!(cluster.stats.num_running_queries, 7);
    assert_eq!(cluster.stats.max_engine_concurrency, 77);
    assert_eq!(cluster.stats.total_query_input_size, 777);
    assert_eq!(
        cluster.stats.queue_time_estimate_ms_by_priority,
        HashMap::from([(1, 222), (2, 333)])
    );
    assert_eq!(cluster.rtt, Duration::from_millis(5));
}

#[tokio::test]
async fn shared_cluster_recomputes_cluster_stats_when_source_backend_is_removed() {
    let state = StargateState::default();
    let mut running_a = running_registration_in_cluster(
        &state,
        "inst-a",
        "cluster-shared",
        "quic://127.0.0.1:1111",
        Some("rk-a"),
    )
    .await;
    let mut running_b = running_registration_in_cluster(
        &state,
        "inst-b",
        "cluster-shared",
        "quic://127.0.0.1:2222",
        Some("rk-a"),
    )
    .await;

    let update_a = InferenceServerRegistration {
        inference_server_id: "inst-a".to_string(),
        cluster_id: "cluster-shared".to_string(),
        inference_server_url: "quic://127.0.0.1:1111".to_string(),
        models: HashMap::from([(
            "shared-model".to_string(),
            model_registration_with_stats(
                InferenceServerStatus::Active as i32,
                ModelStats {
                    last_mean_input_tps: 100.0,
                    max_output_tps: 50.0,
                    kv_cache_capacity_tokens: 1000,
                    kv_cache_used_tokens: 100,
                    kv_cache_free_tokens: 900,
                    num_running_queries: 11,
                    max_engine_concurrency: 111,
                    total_query_input_size: 1111,
                    queue_time_estimate_ms_by_priority: HashMap::from([(1, 111)]),
                    ..ModelStats::default()
                },
            ),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };
    let update_b = InferenceServerRegistration {
        inference_server_id: "inst-b".to_string(),
        cluster_id: "cluster-shared".to_string(),
        inference_server_url: "quic://127.0.0.1:2222".to_string(),
        models: HashMap::from([(
            "shared-model".to_string(),
            model_registration_with_stats(
                InferenceServerStatus::Active as i32,
                ModelStats {
                    last_mean_input_tps: 120.0,
                    max_output_tps: 60.0,
                    kv_cache_capacity_tokens: 2000,
                    kv_cache_used_tokens: 500,
                    kv_cache_free_tokens: 1500,
                    num_running_queries: 7,
                    max_engine_concurrency: 77,
                    total_query_input_size: 777,
                    queue_time_estimate_ms_by_priority: HashMap::from([(1, 222), (2, 333)]),
                    ..ModelStats::default()
                },
            ),
        )]),
        reverse_tunnel: false,
        coordinated_calibration: false,
    };

    state
        .apply_registration_update(
            &mut running_a,
            &update_a,
            true,
            Some(Duration::from_millis(10)),
        )
        .await;
    state
        .apply_registration_update(
            &mut running_b,
            &update_b,
            true,
            Some(Duration::from_millis(5)),
        )
        .await;

    state.end_registration("inst-b").await;

    let clusters = state
        .cluster_candidates_for_target(&make_target(Some("rk-a"), "shared-model"))
        .await;
    assert_eq!(clusters.len(), 1);
    let cluster = &clusters[0];
    assert_eq!(cluster.active_backend_count, 1);
    assert_eq!(cluster.stats.last_mean_input_tps, 100.0);
    assert_eq!(cluster.stats.max_output_tps, 50.0);
    assert_eq!(cluster.stats.kv_cache_capacity_tokens, 1000);
    assert_eq!(cluster.stats.kv_cache_used_tokens, 100);
    assert_eq!(cluster.stats.kv_cache_free_tokens, 900);
    assert_eq!(cluster.stats.num_running_queries, 11);
    assert_eq!(cluster.stats.max_engine_concurrency, 111);
    assert_eq!(cluster.stats.total_query_input_size, 1111);
    assert_eq!(
        cluster.stats.queue_time_estimate_ms_by_priority,
        HashMap::from([(1, 111)])
    );
}

#[tokio::test]
async fn shared_cluster_selects_active_backends_round_robin() {
    let state = StargateState::default();
    let mut running_a = running_registration_in_cluster(
        &state,
        "inst-a",
        "cluster-shared",
        "quic://127.0.0.1:1111",
        Some("rk-a"),
    )
    .await;
    let mut running_b = running_registration_in_cluster(
        &state,
        "inst-b",
        "cluster-shared",
        "quic://127.0.0.1:2222",
        Some("rk-a"),
    )
    .await;
    for (running, inst, url) in [
        (&mut running_a, "inst-a", "quic://127.0.0.1:1111"),
        (&mut running_b, "inst-b", "quic://127.0.0.1:2222"),
    ] {
        let update = InferenceServerRegistration {
            inference_server_id: inst.to_string(),
            cluster_id: "cluster-shared".to_string(),
            inference_server_url: url.to_string(),
            models: HashMap::from([(
                "shared-model".to_string(),
                model_registration(InferenceServerStatus::Active as i32),
            )]),
            reverse_tunnel: false,
            coordinated_calibration: false,
        };
        state
            .apply_registration_update(running, &update, true, Some(Duration::from_millis(5)))
            .await;
    }

    let target = make_target(Some("rk-a"), "shared-model");
    let first = state
        .select_backend_for_cluster(&target, "cluster-shared", &HashSet::new())
        .await
        .expect("first backend should be selected");
    let second = state
        .select_backend_for_cluster(&target, "cluster-shared", &HashSet::new())
        .await
        .expect("second backend should be selected");
    let third = state
        .select_backend_for_cluster(&target, "cluster-shared", &HashSet::new())
        .await
        .expect("third backend should be selected");

    assert_eq!(first.inference_server_id, "inst-a");
    assert_eq!(second.inference_server_id, "inst-b");
    assert_eq!(third.inference_server_id, "inst-a");

    let selected = state
        .select_backend_for_cluster(
            &target,
            "cluster-shared",
            &HashSet::from(["inst-a".to_string()]),
        )
        .await
        .expect("remaining backend should be selected");
    assert_eq!(selected.inference_server_id, "inst-b");
}

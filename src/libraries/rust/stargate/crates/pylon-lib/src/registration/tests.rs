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

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::sync::Arc;
use std::time::Duration;

use stargate_proto::pb::{
    InferenceServerAck, InferenceServerModelRegistration, InferenceServerRegistration,
    InferenceServerStatus, ModelStats, StargateInfo, WatchStargatesResponse,
};
use stargate_protocol::TunnelTransportProtocol;
use stargate_runtime::OwnedTask;
use tokio::sync::{mpsc, watch};
use tokio_util::sync::CancellationToken;

use crate::quic_http_tunnel::{TunnelError, TunnelForwardingConfig};
use crate::request_quality_monitor::RequestQualityMonitorConfig;
use crate::runtime_state::{CurrentModelStats, PylonRuntimeState, gated_model_status};
use crate::stats::PylonMetrics;

use super::discovery::*;
use super::grpc_endpoint::*;
use super::reverse_tunnel::*;
use super::router_stream::*;
use super::state::*;
use super::topology::*;
use super::types::RegistrationSessionConfig;
use super::urls::infer_upstream_http_base_url;
use super::*;

const TEST_WAIT: Duration = Duration::from_secs(1);

fn grpc_endpoint(authority_addr: &str) -> StargateGrpcEndpoint {
    StargateGrpcEndpoint::new(authority_addr.to_string(), authority_addr.to_string())
        .expect("test endpoint authority should be non-empty")
}

fn grpc_endpoint_with_dial(authority_addr: &str, dial_addr: &str) -> StargateGrpcEndpoint {
    StargateGrpcEndpoint::new(authority_addr.to_string(), dial_addr.to_string())
        .expect("test endpoint authority should be non-empty")
}

fn stargate_info(
    stargate_id: &str,
    advertise_addr: &str,
    grpc_pylon_dial_addr: &str,
) -> StargateInfo {
    StargateInfo {
        stargate_id: stargate_id.to_string(),
        advertise_addr: advertise_addr.to_string(),
        http_advertise_addr: String::new(),
        grpc_pylon_dial_addr: grpc_pylon_dial_addr.to_string(),
    }
}

fn watch_snapshot(routers: &[&str], watch_urls: &[&str]) -> WatchEndpointSnapshot {
    WatchEndpointSnapshot {
        registration_routers: routers
            .iter()
            .map(|router| ((*router).to_string(), grpc_endpoint(router)))
            .collect(),
        watch_urls: watch_urls.iter().map(|url| (*url).to_string()).collect(),
    }
}

fn test_registration_config() -> InferenceServerRegistrationConfig {
    InferenceServerRegistrationConfig {
        seeds: vec!["router-a".to_string()],
        inference_server_id: "inst-a".to_string(),
        cluster_id: "cluster-a".to_string(),
        inference_server_url: "quic://127.0.0.1:8443".to_string(),
        forwarding: TunnelForwardingConfig {
            runtime_state: PylonRuntimeState::new(
                InferenceServerStatus::Active,
                &["model-a".to_string()],
            ),
            ..Default::default()
        },
        min_update_interval: Duration::from_secs(2),
        reverse_tunnel: false,
        tls_cert_pem: None,
        quic_insecure: true,
        tunnel_protocol: TunnelTransportProtocol::RawQuic,
        auth_token_provider: None,
    }
}

fn registration_with_active_model(
    reverse_tunnel: bool,
    reverse_connected: bool,
) -> InferenceServerRegistration {
    let models = HashMap::from([(
        "model-a".to_string(),
        InferenceServerModelRegistration {
            stats: Some(ModelStats {
                last_mean_input_tps: 30.0,
                ..ModelStats::default()
            }),
            status: InferenceServerStatus::Active.into(),
        },
    )]);
    build_inference_server_registration(
        "client-a",
        "cluster-a",
        "quic://127.0.0.1:9000",
        &models,
        reverse_tunnel,
        reverse_connected,
    )
}

fn assert_metrics(metrics: &PylonMetrics, samples: &[&str]) {
    let body = metrics.gather_text().expect("metrics should encode");
    for sample in samples {
        assert!(body.contains(sample), "missing metric sample: {sample}");
    }
}

fn assert_invalid_registration_config(
    expected: &str,
    mutate: impl FnOnce(&mut InferenceServerRegistrationConfig),
) {
    let mut config = test_registration_config();
    mutate(&mut config);
    assert!(
        matches!(RegistrationSessionConfig::try_from(config), Err(ClientError::Config(message)) if message == expected),
        "expected registration config error: {expected}"
    );
}

async fn cancel_blocked_task<T>(
    stop: CancellationToken,
    task: tokio::task::JoinHandle<T>,
    context: &str,
) -> T {
    tokio::task::yield_now().await;
    stop.cancel();
    tokio::time::timeout(TEST_WAIT, task)
        .await
        .expect(context)
        .expect("blocked send task should not panic")
}

#[test]
fn reverse_tunnel_connectivity_only_overrides_router_local_advertisement() {
    assert_eq!(
        router_advertised_status(InferenceServerStatus::Active, true, false),
        InferenceServerStatus::Inactive
    );
    assert_eq!(
        router_advertised_status(InferenceServerStatus::Active, true, true),
        InferenceServerStatus::Active
    );
    assert_eq!(
        router_advertised_status(InferenceServerStatus::Inactive, true, false),
        InferenceServerStatus::Inactive
    );
}

#[test]
fn bringup_gates_active_status_until_model_is_advertising() {
    for (bringup_ready, expected) in [
        (false, InferenceServerStatus::Inactive),
        (true, InferenceServerStatus::Active),
    ] {
        assert_eq!(
            gated_model_status(InferenceServerStatus::Active, bringup_ready),
            expected
        );
    }
}

#[test]
fn registration_payload_keeps_every_runtime_model_and_gates_reverse_connectivity() {
    let update = registration_with_active_model(true, false);

    assert_eq!(update.cluster_id, "cluster-a");
    assert_eq!(update.models.len(), 1);
    assert_eq!(
        update.models["model-a"].status,
        InferenceServerStatus::Inactive as i32
    );
}

#[test]
fn router_advertisement_metrics_are_cleared_when_tracker_drops() {
    let metrics = PylonMetrics::new().expect("metrics should initialize");
    let update = registration_with_active_model(false, false);

    {
        let mut tracker = RouterAdvertisedStatusTracker::new(Some(metrics.as_ref()), "router-a");
        tracker.record_successful_advertisement(advertised_model_statuses(&update));
        tracker.record_reverse_tunnel_connected(true);
        assert_metrics(
            &metrics,
            &[
                r#"pylon_model_advertised_status{model="model-a",router="router-a",status="active"} 1"#,
                r#"pylon_registration_stream_connected{router="router-a"} 1"#,
                r#"pylon_reverse_tunnel_connected{router="router-a"} 1"#,
            ],
        );
    }

    assert_metrics(
        &metrics,
        &[
            r#"pylon_model_advertised_status{model="model-a",router="router-a",status="active"} 0"#,
            r#"pylon_registration_stream_connected{router="router-a"} 0"#,
            r#"pylon_reverse_tunnel_connected{router="router-a"} 0"#,
        ],
    );
}

#[test]
fn router_advertisement_metrics_remove_models_omitted_from_next_snapshot() {
    let metrics = PylonMetrics::new().expect("metrics should initialize");
    let update = registration_with_active_model(false, false);
    let mut tracker = RouterAdvertisedStatusTracker::new(Some(metrics.as_ref()), "router-a");

    tracker.record_successful_advertisement(advertised_model_statuses(&update));
    tracker.record_successful_advertisement(Vec::new());

    let body = metrics.gather_text().expect("metrics should encode");
    assert!(!body.contains(r#"pylon_model_advertised_status{model="model-a""#));
}

#[test]
fn registration_session_config_normalizes_reverse_url_and_cluster_id() {
    let mut config = test_registration_config();
    config.cluster_id.clear();
    config.inference_server_url = "http://127.0.0.1:8090/".to_string();
    config.reverse_tunnel = true;

    let session = RegistrationSessionConfig::try_from(config).expect("session should build");

    assert_eq!(session.watch_seeds, ["router-a"]);
    assert_eq!(session.cluster_id, "inst-a");
    assert_eq!(session.inference_server_url, "http://127.0.0.1:8090");
}

#[test]
fn registration_session_config_rejects_invalid_public_config() {
    assert_invalid_registration_config("stargate seeds are empty", |config| config.seeds.clear());
    assert_invalid_registration_config(
        "direct registration inference_server_url must be quic://",
        |config| config.inference_server_url = "http://127.0.0.1:8090".to_string(),
    );
    assert_invalid_registration_config(
        "reverse registration inference_server_url must be http(s)",
        |config| {
            config.reverse_tunnel = true;
            config.inference_server_url = "quic://127.0.0.1:8090".to_string();
        },
    );
}

#[test]
fn registration_session_config_accepts_empty_runtime_membership() {
    let mut config = test_registration_config();
    config.forwarding.runtime_state = PylonRuntimeState::default();

    let session = RegistrationSessionConfig::try_from(config)
        .expect("an authoritative empty model snapshot should register");

    assert!(
        session
            .forwarding
            .runtime_state
            .advertised_model_ids()
            .is_empty()
    );
}

#[test]
fn reverse_tunnel_config_uses_registration_upstream_and_preserves_forwarding() {
    let metrics = PylonMetrics::new().expect("metrics should initialize");
    let mut config = test_registration_config();
    config.reverse_tunnel = true;
    config.inference_server_url = "http://127.0.0.1:8090/".to_string();
    config.forwarding.metrics = Some(metrics.clone());
    let session = RegistrationSessionConfig::try_from(config).expect("session should build");
    let endpoint = ReverseTunnelEndpoint {
        routing_target_addr: "router-a:50072".to_string(),
        pylon_dial_addr: "dial-a:50072".to_string(),
        sni_override: Some("router-a".to_string()),
    };

    let tunnel = reverse_quic_tunnel_config(&endpoint, &session);

    assert_eq!(tunnel.upstream_http_base_url, "http://127.0.0.1:8090");
    assert!(Arc::ptr_eq(
        tunnel.forwarding.metrics.as_ref().unwrap(),
        &metrics
    ));
}

#[test]
fn stargate_grpc_endpoint_rejects_empty_authority_and_formats_dial_overrides() {
    assert!(StargateGrpcEndpoint::new(" ", "stargate-grpc-lb:443").is_none());
    assert_eq!(
        grpc_endpoint_with_dial("router-a:50071", "stargate-grpc-lb:443").to_string(),
        "router-a:50071 via stargate-grpc-lb:443"
    );
}

#[test]
fn watch_response_separates_registration_routers_from_recursive_seeds() {
    let snapshot = watch_endpoint_snapshot_from_response(
        "seed-a",
        WatchStargatesResponse {
            stargates: vec![stargate_info(
                "stargate-0",
                "stargate-0.region-a:50071",
                "lb.region-a:443",
            )],
            watch_stargate_urls: vec!["stargate.region-b:50071".to_string()],
        },
    );

    assert_eq!(
        snapshot.registration_routers,
        BTreeMap::from([(
            "stargate-0".to_string(),
            grpc_endpoint_with_dial("stargate-0.region-a:50071", "lb.region-a:443")
        )])
    );
    assert_eq!(
        snapshot.watch_urls,
        BTreeSet::from(["stargate.region-b:50071".to_string()])
    );
}

#[test]
fn recursive_discovery_publishes_the_union_after_all_snapshots_arrive() {
    let seeds = BTreeSet::from(["stargate.region-a:50071".to_string()]);
    let mut snapshots = HashMap::from([(
        "stargate.region-a:50071".to_string(),
        watch_snapshot(&["stargate-0.region-a:50071"], &["stargate.region-b:50071"]),
    )]);
    let desired = desired_watch_urls_from_snapshots(&seeds, &snapshots);
    assert!(!all_desired_watch_urls_have_snapshots(&desired, |url| {
        snapshots.contains_key(url)
    }));

    snapshots.insert(
        "stargate.region-b:50071".to_string(),
        watch_snapshot(&["stargate-0.region-b:50071"], &[]),
    );
    let desired = desired_watch_urls_from_snapshots(&seeds, &snapshots);

    assert!(all_desired_watch_urls_have_snapshots(&desired, |url| {
        snapshots.contains_key(url)
    }));
    assert_eq!(
        active_registration_routers(snapshots.values()),
        BTreeSet::from([
            grpc_endpoint("stargate-0.region-a:50071"),
            grpc_endpoint("stargate-0.region-b:50071"),
        ])
    );
}

#[tokio::test]
async fn registration_router_topology_publishes_every_discovered_router() {
    let routers = BTreeSet::from([
        grpc_endpoint("stargate-0.region-a:50071"),
        grpc_endpoint("stargate-0.region-b:50071"),
    ]);
    let (topology_tx, mut topology_rx) = watch::channel(RegistrationRouterTopology::default());

    assert!(publish_registration_router_topology(
        &topology_tx,
        &routers,
        true
    ));
    topology_rx
        .changed()
        .await
        .expect("topology should publish");

    assert_eq!(topology_rx.borrow().published_routers(), Some(&routers));
}

#[tokio::test]
async fn watch_endpoint_and_registration_sends_wake_on_cancellation() {
    let stop = CancellationToken::new();
    let task_stop = stop.clone();
    let (updates_tx, _updates_rx) = mpsc::channel(1);
    updates_tx
        .send(InferenceServerRegistration::default())
        .await
        .expect("seed update should fill channel");
    let task = tokio::spawn(async move {
        send_registration_update(
            &updates_tx,
            InferenceServerRegistration::default(),
            &task_stop,
        )
        .await
    });
    assert!(!cancel_blocked_task(stop, task, "send should stop").await);

    let stop = CancellationToken::new();
    let task_stop = stop.clone();
    let (updates_tx, _updates_rx) = mpsc::channel(1);
    let update = WatchEndpointUpdate {
        watch_url: "seed-a".to_string(),
        generation: 1,
        snapshot: None,
    };
    updates_tx
        .send(update)
        .await
        .expect("seed update should fill channel");
    let task = tokio::spawn(async move {
        send_watch_endpoint_update(
            &updates_tx,
            WatchEndpointUpdate {
                watch_url: "seed-a".to_string(),
                generation: 2,
                snapshot: None,
            },
            &task_stop,
        )
        .await
    });
    assert!(!cancel_blocked_task(stop, task, "watch send should stop").await);
}

#[test]
fn runtime_snapshot_forwards_bootstrap_and_collected_stats_exactly() {
    let runtime_state =
        PylonRuntimeState::new(InferenceServerStatus::Active, &["model-a".to_string()]);
    runtime_state.set_model_bringup_ready("model-a", true);
    let queue_time_estimate_ms_by_priority = HashMap::from([(0, 11), (2, 7)]);
    runtime_state.set_model_stats(
        "model-a",
        CurrentModelStats {
            last_mean_input_tps: 3.5,
            output_tps: 2.5,
            queue_size: 4,
            queued_input_size: 5,
            max_output_tps: 6.5,
            kv_cache_capacity_tokens: 7,
            kv_cache_used_tokens: 8,
            kv_cache_free_tokens: 9,
            num_running_queries: 10,
            max_engine_concurrency: Some(11),
            total_query_input_size: 12,
            input_processing_queries: 13,
            output_generation_queries: 14,
            stats_observed_at_unix_ms: 15,
            stats_capabilities: vec!["request.output.chunk_usage".to_string()],
            stats_sources: vec!["chunk_usage".to_string()],
            queue_time_estimate_ms_by_priority: Some(queue_time_estimate_ms_by_priority.clone()),
            ..CurrentModelStats::default()
        },
    );

    let snapshot = runtime_state.advertised_models();
    let model = &snapshot["model-a"];
    assert_eq!(model.status, InferenceServerStatus::Active as i32);
    let stats = model.stats.as_ref().expect("stats should be present");
    assert_eq!(stats.last_mean_input_tps, 3.5);
    assert_eq!(stats.output_tps, 2.5);
    assert_eq!(
        stats.queue_time_estimate_ms_by_priority,
        queue_time_estimate_ms_by_priority
    );
}

#[test]
fn reverse_tunnel_endpoint_uses_dial_address_and_preserves_routing_sni() {
    let endpoint = reverse_tunnel_endpoint_from_ack(&InferenceServerAck {
        reverse_tunnel_target: "stargate-0.stargate-headless:50072".to_string(),
        reverse_tunnel_pylon_dial_addr: "stargate-quic-lb:50072".to_string(),
    })
    .expect("ack should contain reverse tunnel endpoint");

    assert_eq!(endpoint.pylon_dial_addr, "stargate-quic-lb:50072");
    assert_eq!(
        endpoint.routing_target_addr,
        "stargate-0.stargate-headless:50072"
    );
    assert_eq!(
        endpoint.sni_override.as_deref(),
        Some("stargate-0.stargate-headless")
    );
}

#[tokio::test]
async fn reverse_tunnel_connect_attempt_times_out() {
    let result = reverse_tunnel_connect_with_timeout(
        Duration::from_millis(1),
        std::future::pending::<Result<crate::ReverseQuicTunnelHandle, TunnelError>>(),
    )
    .await;

    assert!(matches!(
        result,
        Err(TunnelError::ConnectTimeout { timeout_ms: 1 })
    ));
}

#[test]
fn registration_session_preserves_request_quality_configuration() {
    let quality = RequestQualityMonitorConfig {
        collect_quality_metrics: true,
        collect_quality_metrics_min_tokens: 7,
        output_tokens_threshold_min: Some(9),
        ..RequestQualityMonitorConfig::default()
    };
    let mut config = test_registration_config();
    config.forwarding.request_quality_monitor = quality;

    let session = RegistrationSessionConfig::try_from(config).expect("session should build");

    assert!(
        session
            .forwarding
            .request_quality_monitor
            .collect_quality_metrics
    );
    assert_eq!(
        session
            .forwarding
            .request_quality_monitor
            .collect_quality_metrics_min_tokens,
        7
    );
}

#[test]
fn infers_only_http_upstream_registration_urls() {
    assert_eq!(
        infer_upstream_http_base_url("http://127.0.0.1:8000/"),
        Some("http://127.0.0.1:8000".to_string())
    );
    assert_eq!(infer_upstream_http_base_url("http://"), None);
    assert_eq!(infer_upstream_http_base_url("quic://127.0.0.1:8000"), None);
}

#[tokio::test]
async fn stop_watched_endpoint_signals_and_awaits_task() {
    let (exited_tx, exited_rx) = tokio::sync::oneshot::channel();
    let task = OwnedTask::spawn("watch stargate endpoint", move |stop| async move {
        stop.cancelled().await;
        let _ = exited_tx.send(());
    });
    let endpoint = WatchedEndpoint {
        generation: 0,
        task,
        snapshot: None,
    };

    stop_watched_endpoint(endpoint).await;

    exited_rx.await.expect("watched endpoint task should exit");
}

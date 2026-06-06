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

use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::common::{
    LifecycleDummyBackend, SelfDiscovery, SharedDiscovery, TokenMapAuthenticator, base_config,
    bind_ephemeral, bind_ephemeral_udp, init_crypto, start_lifecycle_dummy_backend,
    wait_for_routing, wait_for_routing_with_rk, wait_for_unroutable, with_proxy_headers,
};
use prometheus::{Encoder, TextEncoder};
use pylon_lib::{
    AuthTokenProvider, BringupConfig, InferenceServerRegistrationClient,
    InferenceServerRegistrationConfig, InferenceServerUpdateChannels, OutputTokenParserFactory,
    QuicHttpTunnelConfig, QuicHttpTunnelHandle, ReverseQuicTunnelConfig, ReverseQuicTunnelHandle,
    start_quic_http_tunnel, start_reverse_quic_tunnel,
};
use reqwest::StatusCode;
use stargate::auth::WorkerAuthenticator;
use stargate::runtime::{StargateHandle, StargateRuntime};
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::stargate_model_discovery_client::StargateModelDiscoveryClient;
use stargate_proto::pb::{
    InferenceServerModelRegistration, InferenceServerRegistration, InferenceServerStatus,
    ListModelsRequest, ModelStats, StargateInfo,
};
use tokio::task::JoinHandle;
use tonic::transport::Channel;

const STATUS_TIMEOUT: Duration = Duration::from_secs(10);
const BRINGUP_TIMEOUT: Duration = Duration::from_secs(15);
const CANARY_FAILURE_TOKEN_THRESHOLD: u32 = 7;

#[derive(Debug, Clone, Copy)]
enum TransportMode {
    Direct,
    Reverse,
}

impl TransportMode {
    fn reverse_tunnel(self) -> bool {
        matches!(self, Self::Reverse)
    }

    fn label(self) -> &'static str {
        match self {
            Self::Direct => "direct",
            Self::Reverse => "reverse",
        }
    }
}

#[derive(Debug, Clone, Copy)]
enum StargateTopology {
    Single,
    Pair,
}

impl StargateTopology {
    fn label(self) -> &'static str {
        match self {
            Self::Single => "single",
            Self::Pair => "pair",
        }
    }
}

struct LifecycleTopology {
    grpc_seed: SocketAddr,
    grpc_addrs: Vec<SocketAddr>,
    http_addrs: Vec<SocketAddr>,
    model_discovery_addrs: Vec<SocketAddr>,
    reverse_tunnel_targets: Vec<String>,
    handles: Vec<StargateHandle>,
}

impl LifecycleTopology {
    async fn shutdown(self) {
        for handle in &self.handles {
            handle.begin_shutdown();
        }
        for handle in &self.handles {
            assert!(
                handle.wait_for_shutdown(Duration::from_secs(5)).await,
                "stargate did not shut down cleanly"
            );
        }
    }
}

struct LifecycleRegistration {
    backend: LifecycleDummyBackend,
    reg_client: InferenceServerRegistrationClient,
    channels: InferenceServerUpdateChannels,
    tunnel: Option<QuicHttpTunnelHandle>,
}

impl LifecycleRegistration {
    fn stop(&mut self) {
        self.reg_client.stop();
    }

    async fn shutdown_tunnel(&mut self) {
        if let Some(tunnel) = self.tunnel.take() {
            tunnel.shutdown().await;
        }
    }
}

struct RawRegistration {
    tx: flume::Sender<InferenceServerRegistration>,
    ack_task: JoinHandle<Result<(), tonic::Status>>,
}

struct LifecycleRuntimeParts<'a> {
    id: &'a str,
    grpc_addr: SocketAddr,
    grpc_listener: std::net::TcpListener,
    model_discovery_addr: SocketAddr,
    model_discovery_listener: std::net::TcpListener,
    http_addr: SocketAddr,
    http_listener: std::net::TcpListener,
    discovery: Box<dyn stargate::discovery::Discovery>,
    reverse: Option<(SocketAddr, std::net::UdpSocket)>,
    authenticator: Option<Arc<dyn WorkerAuthenticator>>,
}

struct LifecycleBackendOptions<'a> {
    backend_id: &'a str,
    cluster_id: &'a str,
    model: &'a str,
    backend: LifecycleDummyBackend,
    bringup: BringupConfig,
    auth_token: Option<String>,
}

impl RawRegistration {
    async fn send(&self, update: InferenceServerRegistration) {
        self.tx
            .send_async(update)
            .await
            .expect("raw registration stream closed");
    }

    async fn close(self) {
        self.close_with_expected_error(None).await;
    }

    async fn close_expecting_error(self, code: tonic::Code, message: &str) {
        self.close_with_expected_error(Some((code, message))).await;
    }

    async fn close_with_expected_error(self, expected_error: Option<(tonic::Code, &str)>) {
        // Close the sending half so the server observes end-of-stream before shutdown.
        drop(self.tx);
        let mut ack_task = self.ack_task;
        match tokio::time::timeout(Duration::from_secs(5), &mut ack_task).await {
            Ok(Ok(Ok(()))) => {
                assert!(
                    expected_error.is_none(),
                    "expected raw registration stream error {expected_error:?}, but stream closed cleanly"
                );
            }
            Ok(Ok(Err(status))) => {
                if let Some((code, message)) = expected_error {
                    assert_eq!(status.code(), code, "raw registration stream error code");
                    assert!(
                        status.message().contains(message),
                        "raw registration stream error message {:?} did not contain {:?}",
                        status.message(),
                        message
                    );
                } else {
                    panic!("raw registration stream returned error: {status}");
                }
            }
            Ok(Err(error)) => panic!("raw registration ack task failed: {error}"),
            Err(_) => {
                ack_task.abort();
                panic!("raw registration ack stream did not close within 5s after sender dropped");
            }
        }
    }
}

#[tokio::test]
async fn direct_single_stargate_status_lifecycle() {
    exercise_status_lifecycle(TransportMode::Direct, StargateTopology::Single).await;
}

#[tokio::test]
async fn direct_multi_stargate_status_lifecycle() {
    exercise_status_lifecycle(TransportMode::Direct, StargateTopology::Pair).await;
}

#[tokio::test]
async fn reverse_single_stargate_status_lifecycle() {
    exercise_status_lifecycle(TransportMode::Reverse, StargateTopology::Single).await;
}

#[tokio::test]
async fn reverse_multi_stargate_status_lifecycle() {
    exercise_status_lifecycle(TransportMode::Reverse, StargateTopology::Pair).await;
}

#[tokio::test]
async fn bringup_waits_for_health_before_advertising_active() {
    exercise_bringup_health(TransportMode::Direct, StargateTopology::Single).await;
}

#[tokio::test]
async fn bringup_calibration_error_keeps_backend_unroutable_until_success() {
    exercise_bringup_calibration_error(TransportMode::Direct, StargateTopology::Single).await;
}

#[tokio::test]
async fn active_canary_failure_demotes_backend_until_recovery_canary_succeeds() {
    exercise_active_canary_failure(TransportMode::Direct, StargateTopology::Single).await;
}

#[tokio::test]
async fn bringup_health_gates_reverse_and_fanout_topologies() {
    exercise_bringup_health(TransportMode::Reverse, StargateTopology::Single).await;
    exercise_bringup_health(TransportMode::Direct, StargateTopology::Pair).await;
    exercise_bringup_health(TransportMode::Reverse, StargateTopology::Pair).await;
}

#[tokio::test]
async fn bringup_calibration_error_gates_reverse_and_fanout_topologies() {
    exercise_bringup_calibration_error(TransportMode::Reverse, StargateTopology::Single).await;
    exercise_bringup_calibration_error(TransportMode::Direct, StargateTopology::Pair).await;
}

#[tokio::test]
async fn active_canary_failure_gates_reverse_and_fanout_topologies() {
    exercise_active_canary_failure(TransportMode::Direct, StargateTopology::Pair).await;
    exercise_active_canary_failure(TransportMode::Reverse, StargateTopology::Single).await;
    exercise_active_canary_failure(TransportMode::Reverse, StargateTopology::Pair).await;
}

#[tokio::test]
async fn active_canary_failure_checks_health_before_recovery() {
    init_crypto();

    let topology = start_lifecycle_topology(TransportMode::Direct, StargateTopology::Single).await;
    let model = "lifecycle-canary-health-model";
    let backend = start_lifecycle_dummy_backend(model).await;
    let mut registration = register_lifecycle_backend(
        &topology,
        TransportMode::Direct,
        "lifecycle-canary-health-backend",
        model,
        backend,
        active_canary_bringup(),
    )
    .await;

    wait_for_routing_on_all(&topology, model, BRINGUP_TIMEOUT).await;
    let successful_canaries = registration.backend.canary_requests();
    let healthy_checks = registration.backend.health_requests();

    registration.backend.set_health_ok(false);
    registration.backend.set_completions_ok(false);
    wait_for_counter_at_least(
        "failing active canary requests",
        successful_canaries + 1,
        Duration::from_secs(5),
        || registration.backend.canary_requests(),
    )
    .await;
    wait_for_counter_at_least(
        "failing health checks",
        healthy_checks + 1,
        Duration::from_secs(5),
        || registration.backend.health_requests(),
    )
    .await;
    wait_for_unroutable_on_all(&topology, model, STATUS_TIMEOUT).await;

    let unhealthy_checks = registration.backend.health_requests();
    registration.backend.set_completions_ok(true);
    wait_for_counter_at_least(
        "unhealthy recovery health checks",
        unhealthy_checks + 1,
        Duration::from_secs(5),
        || registration.backend.health_requests(),
    )
    .await;
    wait_for_unroutable_on_all(&topology, model, STATUS_TIMEOUT).await;

    registration.backend.set_health_ok(true);
    wait_for_routing_on_all(&topology, model, BRINGUP_TIMEOUT).await;
    assert_backend_serves_all(
        &topology,
        model,
        "lifecycle-canary-health-backend",
        "canary-health-recovered",
    )
    .await;

    registration.stop();
    topology.shutdown().await;
}

#[tokio::test]
async fn server_health_rtt_gates_initial_and_post_active_updates() {
    for (transport, topology) in [
        (TransportMode::Direct, StargateTopology::Single),
        (TransportMode::Direct, StargateTopology::Pair),
        (TransportMode::Reverse, StargateTopology::Single),
        (TransportMode::Reverse, StargateTopology::Pair),
    ] {
        exercise_server_health_rtt_lifecycle(transport, topology).await;
    }
}

#[tokio::test]
async fn direct_registration_routes_when_tunnel_starts_after_active_update() {
    exercise_direct_late_tunnel(StargateTopology::Single).await;
    exercise_direct_late_tunnel(StargateTopology::Pair).await;
}

#[tokio::test]
async fn reverse_registration_active_update_waits_for_tunnel_connection() {
    exercise_reverse_tunnel_gate(StargateTopology::Single).await;
    exercise_reverse_tunnel_gate(StargateTopology::Pair).await;
}

#[tokio::test]
async fn identity_mutation_closes_registration_and_removes_prior_route() {
    exercise_identity_mutation_closes_route(TransportMode::Direct).await;
    exercise_identity_mutation_closes_route(TransportMode::Reverse).await;
}

#[tokio::test]
async fn list_models_tracks_backend_and_cluster_lifecycle() {
    exercise_model_discovery_cluster_lifecycle(TransportMode::Direct).await;
    exercise_model_discovery_cluster_lifecycle(TransportMode::Reverse).await;
}

#[tokio::test]
async fn lifecycle_metrics_report_active_inference_server_count() {
    for (transport, stargates) in [
        (TransportMode::Direct, StargateTopology::Single),
        (TransportMode::Direct, StargateTopology::Pair),
        (TransportMode::Reverse, StargateTopology::Single),
        (TransportMode::Reverse, StargateTopology::Pair),
    ] {
        exercise_lifecycle_metrics(transport, stargates).await;
    }
}

async fn exercise_lifecycle_metrics(transport: TransportMode, stargates: StargateTopology) {
    init_crypto();

    let topology = start_lifecycle_topology(transport, stargates).await;
    let model = format!(
        "lifecycle-metrics-{}-{}-model",
        transport.label(),
        stargates.label()
    );
    let backend_id = format!(
        "lifecycle-metrics-{}-{}-backend",
        transport.label(),
        stargates.label()
    );
    let backend = start_lifecycle_dummy_backend(&model).await;
    let mut registration = register_lifecycle_backend(
        &topology,
        transport,
        &backend_id,
        &model,
        backend,
        disabled_bringup(),
    )
    .await;

    wait_for_routing_on_all(&topology, &model, STATUS_TIMEOUT).await;
    wait_for_active_inference_server_count(&topology, &model, 1, Duration::from_secs(5)).await;

    registration
        .channels
        .status
        .send_async(InferenceServerStatus::Inactive)
        .await
        .expect("send inactive status");
    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;
    wait_for_active_inference_server_count(&topology, &model, 0, Duration::from_secs(5)).await;

    registration.stop();
    topology.shutdown().await;
}

#[tokio::test]
async fn routing_key_lifecycle_demotes_only_matching_tenant() {
    exercise_routing_key_lifecycle(TransportMode::Direct).await;
    exercise_routing_key_lifecycle(TransportMode::Reverse).await;
}

#[tokio::test]
async fn multi_model_registration_demotes_only_matching_model() {
    exercise_multi_model_raw_lifecycle(TransportMode::Direct).await;
    exercise_multi_model_raw_lifecycle(TransportMode::Reverse).await;
}

#[tokio::test]
async fn direct_lost_quic_connection_stays_unroutable_without_recovery() {
    init_crypto();

    let topology = start_lifecycle_topology(TransportMode::Direct, StargateTopology::Single).await;
    let model = "lifecycle-direct-lost-quic-model";
    let backend = start_lifecycle_dummy_backend(model).await;
    let mut registration = register_lifecycle_backend(
        &topology,
        TransportMode::Direct,
        "lifecycle-direct-lost-quic-backend",
        model,
        backend,
        disabled_bringup(),
    )
    .await;

    wait_for_routing_on_all(&topology, model, STATUS_TIMEOUT).await;
    registration.shutdown_tunnel().await;
    wait_for_unroutable_on_all(&topology, model, STATUS_TIMEOUT).await;
    wait_for_list_models_on_all(&topology, &[], &[], Duration::from_secs(15)).await;

    registration.stop();
    topology.shutdown().await;
}

#[tokio::test]
async fn coordinated_calibration_reassigns_after_owner_stops_mid_calibration() {
    init_crypto();

    let topology = start_lifecycle_topology(TransportMode::Direct, StargateTopology::Single).await;
    let model = "lifecycle-coordinated-reassign-model";
    let backend_a = start_lifecycle_dummy_backend(model).await;
    backend_a.set_completions_ok(false);
    let backend_b = start_lifecycle_dummy_backend(model).await;
    let mut owner = register_lifecycle_backend_with_options(
        &topology,
        TransportMode::Direct,
        LifecycleBackendOptions {
            backend_id: "lifecycle-coordinated-owner",
            cluster_id: "lifecycle-coordinated-cluster",
            model,
            backend: backend_a,
            bringup: coordinated_calibration_bringup(),
            auth_token: None,
        },
    )
    .await;
    let mut waiting = register_lifecycle_backend_with_options(
        &topology,
        TransportMode::Direct,
        LifecycleBackendOptions {
            backend_id: "lifecycle-coordinated-waiting",
            cluster_id: "lifecycle-coordinated-cluster",
            model,
            backend: backend_b,
            bringup: coordinated_calibration_bringup(),
            auth_token: None,
        },
    )
    .await;

    wait_for_counter_at_least(
        "owner calibration requests",
        1,
        Duration::from_secs(5),
        || owner.backend.calibration_requests(),
    )
    .await;
    assert_eq!(
        waiting.backend.calibration_requests(),
        0,
        "waiting backend must not calibrate before owner is removed"
    );

    owner.stop();
    wait_for_counter_at_least(
        "reassigned calibration requests",
        1,
        Duration::from_secs(10),
        || waiting.backend.calibration_requests(),
    )
    .await;
    wait_for_routing_on_all(&topology, model, BRINGUP_TIMEOUT).await;
    assert_backend_serves_all(
        &topology,
        model,
        "lifecycle-coordinated-waiting",
        "coordinated-reassigned",
    )
    .await;

    waiting.stop();
    topology.shutdown().await;
}

async fn exercise_bringup_health(transport: TransportMode, stargates: StargateTopology) {
    init_crypto();

    let topology = start_lifecycle_topology(transport, stargates).await;
    let model = format!(
        "lifecycle-bringup-health-{}-{}-model",
        transport.label(),
        stargates.label()
    );
    let backend_id = format!(
        "lifecycle-bringup-health-{}-{}-backend",
        transport.label(),
        stargates.label()
    );
    let backend = start_lifecycle_dummy_backend(&model).await;
    backend.set_health_ok(false);
    let mut registration = register_lifecycle_backend(
        &topology,
        transport,
        &backend_id,
        &model,
        backend,
        calibration_bringup_without_active_canary(),
    )
    .await;

    wait_for_counter_at_least("health checks", 1, Duration::from_secs(5), || {
        registration.backend.health_requests()
    })
    .await;
    assert_eq!(
        registration.backend.calibration_requests(),
        0,
        "calibration must not run while health is failing"
    );
    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;

    registration.backend.set_health_ok(true);
    wait_for_routing_on_all(&topology, &model, BRINGUP_TIMEOUT).await;
    assert_backend_serves_all(&topology, &model, &backend_id, "bringup-health").await;
    assert!(
        registration.backend.calibration_requests() >= 1,
        "healthy bringup should run calibration before advertising"
    );

    registration.stop();
    topology.shutdown().await;
}

async fn exercise_bringup_calibration_error(transport: TransportMode, stargates: StargateTopology) {
    init_crypto();

    let topology = start_lifecycle_topology(transport, stargates).await;
    let model = format!(
        "lifecycle-bringup-error-{}-{}-model",
        transport.label(),
        stargates.label()
    );
    let backend_id = format!(
        "lifecycle-bringup-error-{}-{}-backend",
        transport.label(),
        stargates.label()
    );
    let backend = start_lifecycle_dummy_backend(&model).await;
    backend.set_completions_ok(false);
    let mut registration = register_lifecycle_backend(
        &topology,
        transport,
        &backend_id,
        &model,
        backend,
        calibration_bringup_without_active_canary(),
    )
    .await;

    wait_for_counter_at_least(
        "failed calibration requests",
        1,
        Duration::from_secs(5),
        || registration.backend.calibration_requests(),
    )
    .await;
    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;

    let failed_calibrations = registration.backend.calibration_requests();
    registration.backend.set_completions_ok(true);
    wait_for_routing_on_all(&topology, &model, BRINGUP_TIMEOUT).await;
    assert_backend_serves_all(&topology, &model, &backend_id, "bringup-error-recovered").await;
    assert!(
        registration.backend.calibration_requests() > failed_calibrations,
        "recovery should require a successful later calibration"
    );

    registration.stop();
    topology.shutdown().await;
}

async fn exercise_active_canary_failure(transport: TransportMode, stargates: StargateTopology) {
    init_crypto();

    let topology = start_lifecycle_topology(transport, stargates).await;
    let model = format!(
        "lifecycle-canary-{}-{}-model",
        transport.label(),
        stargates.label()
    );
    let backend_id = format!(
        "lifecycle-canary-{}-{}-backend",
        transport.label(),
        stargates.label()
    );
    let backend = start_lifecycle_dummy_backend(&model).await;
    backend.set_completion_tokens(1);
    let mut registration = register_lifecycle_backend(
        &topology,
        transport,
        &backend_id,
        &model,
        backend,
        active_canary_bringup(),
    )
    .await;

    wait_for_routing_on_all(&topology, &model, BRINGUP_TIMEOUT).await;
    assert_backend_serves_all(&topology, &model, &backend_id, "canary-active").await;

    let successful_canaries = registration.backend.canary_requests();
    registration
        .backend
        .set_completion_tokens(CANARY_FAILURE_TOKEN_THRESHOLD);
    wait_for_counter_at_least(
        "failing active canary requests",
        successful_canaries + 1,
        Duration::from_secs(5),
        || registration.backend.canary_requests(),
    )
    .await;
    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;

    let failed_canaries = registration.backend.canary_requests();
    registration.backend.set_completion_tokens(1);
    wait_for_routing_on_all(&topology, &model, BRINGUP_TIMEOUT).await;
    assert_backend_serves_all(&topology, &model, &backend_id, "canary-recovered").await;
    assert!(
        registration.backend.canary_requests() > failed_canaries,
        "recovery should require a later successful canary"
    );

    registration.stop();
    topology.shutdown().await;
}

async fn exercise_server_health_rtt_lifecycle(
    transport: TransportMode,
    stargates: StargateTopology,
) {
    init_crypto();

    let topology = start_lifecycle_topology(transport, stargates).await;
    let model = format!(
        "lifecycle-server-health-{}-{}-model",
        transport.label(),
        stargates.label()
    );
    let backend_id = format!(
        "lifecycle-server-health-{}-{}-backend",
        transport.label(),
        stargates.label()
    );
    let backend = start_lifecycle_dummy_backend(&model).await;
    backend.set_health_ok(false);
    let mut registration = register_lifecycle_backend(
        &topology,
        transport,
        &backend_id,
        &model,
        backend,
        disabled_bringup(),
    )
    .await;

    wait_for_counter_at_least("server health checks", 1, Duration::from_secs(5), || {
        registration.backend.health_requests()
    })
    .await;
    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;

    registration.backend.set_health_ok(true);
    wait_for_routing_on_all(&topology, &model, STATUS_TIMEOUT).await;
    assert_backend_serves_all(&topology, &model, &backend_id, "server-health-initial").await;

    let healthy_checks = registration.backend.health_requests();
    registration.backend.set_health_ok(false);
    wait_for_counter_at_least(
        "post-active health checks",
        healthy_checks + 1,
        Duration::from_secs(5),
        || registration.backend.health_requests(),
    )
    .await;
    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;

    registration.backend.set_health_ok(true);
    wait_for_routing_on_all(&topology, &model, STATUS_TIMEOUT).await;
    assert_backend_serves_all(&topology, &model, &backend_id, "server-health-recovered").await;

    registration.stop();
    topology.shutdown().await;
}

async fn exercise_direct_late_tunnel(stargates: StargateTopology) {
    init_crypto();

    let topology = start_lifecycle_topology(TransportMode::Direct, stargates).await;
    let model = format!("lifecycle-direct-late-{}-model", stargates.label());
    let backend_id = format!("lifecycle-direct-late-{}-backend", stargates.label());
    let backend = start_lifecycle_dummy_backend(&model).await;
    let (quic_addr, reserved_socket) = bind_ephemeral_udp();
    // Release the reserved address before starting the direct QUIC tunnel on it.
    drop(reserved_socket);
    let quic_url = format!("quic://{quic_addr}");
    let update = raw_registration(
        &backend_id,
        &format!("lifecycle-direct-late-{}-cluster", stargates.label()),
        &quic_url,
        &[(model.as_str(), InferenceServerStatus::Active)],
        false,
        false,
    );
    let raw_registrations = start_raw_registrations(&topology, update.clone()).await;

    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        quic_addr,
        format!("http://{}", backend.addr),
    ))
    .await
    .expect("late direct QUIC tunnel failed to start");
    send_raw_update_to_all(&raw_registrations, update).await;
    wait_for_routing_on_all(&topology, &model, STATUS_TIMEOUT).await;
    assert_backend_serves_all(&topology, &model, &backend_id, "direct-late").await;

    close_raw_registrations(raw_registrations).await;
    tunnel.shutdown().await;
    topology.shutdown().await;
}

async fn exercise_reverse_tunnel_gate(stargates: StargateTopology) {
    init_crypto();

    let topology = start_lifecycle_topology(TransportMode::Reverse, stargates).await;
    let model = format!("lifecycle-reverse-gate-{}-model", stargates.label());
    let backend_id = format!("lifecycle-reverse-gate-{}-backend", stargates.label());
    let backend = start_lifecycle_dummy_backend(&model).await;
    let upstream_http_base_url = format!("http://{}", backend.addr);
    let update = raw_registration(
        &backend_id,
        &format!("lifecycle-reverse-gate-{}-cluster", stargates.label()),
        &upstream_http_base_url,
        &[(model.as_str(), InferenceServerStatus::Active)],
        true,
        false,
    );
    let raw_registrations = start_raw_registrations(&topology, update.clone()).await;

    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;

    let tunnels = start_reverse_tunnels(&topology, &backend_id, &upstream_http_base_url).await;
    send_raw_update_to_all(&raw_registrations, update).await;
    wait_for_routing_on_all(&topology, &model, STATUS_TIMEOUT).await;
    assert_backend_serves_all(&topology, &model, &backend_id, "reverse-gate").await;

    close_raw_registrations(raw_registrations).await;
    for tunnel in tunnels {
        tunnel.shutdown().await;
    }
    topology.shutdown().await;
}

async fn exercise_identity_mutation_closes_route(transport: TransportMode) {
    init_crypto();

    let topology = start_lifecycle_topology(transport, StargateTopology::Single).await;
    let model = format!("lifecycle-identity-{}-model", transport.label());
    let backend_id = format!("lifecycle-identity-{}-backend", transport.label());
    let backend = start_lifecycle_dummy_backend(&model).await;
    let upstream_http_base_url = format!("http://{}", backend.addr);
    let (inference_server_url, direct_tunnel) = match transport {
        TransportMode::Direct => {
            let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
                "127.0.0.1:0".parse().unwrap(),
                upstream_http_base_url.clone(),
            ))
            .await
            .expect("direct QUIC tunnel failed to start");
            (format!("quic://{}", tunnel.listen_addr()), Some(tunnel))
        }
        TransportMode::Reverse => (upstream_http_base_url.clone(), None),
    };
    let update = raw_registration(
        &backend_id,
        "lifecycle-identity-cluster",
        &inference_server_url,
        &[(model.as_str(), InferenceServerStatus::Active)],
        transport.reverse_tunnel(),
        false,
    );
    let raw_registrations = start_raw_registrations(&topology, update.clone()).await;
    let reverse_tunnels = if transport.reverse_tunnel() {
        start_reverse_tunnels(&topology, &backend_id, &upstream_http_base_url).await
    } else {
        Vec::new()
    };
    send_raw_update_to_all(&raw_registrations, update).await;
    wait_for_routing_on_all(&topology, &model, STATUS_TIMEOUT).await;

    let mutated = raw_registration(
        &backend_id,
        "lifecycle-identity-mutated-cluster",
        &inference_server_url,
        &[(model.as_str(), InferenceServerStatus::Active)],
        transport.reverse_tunnel(),
        false,
    );
    send_raw_update_to_all(&raw_registrations, mutated).await;
    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;

    close_raw_registrations_expect_error(
        raw_registrations,
        tonic::Code::InvalidArgument,
        "cluster_id changed",
    )
    .await;
    if let Some(tunnel) = direct_tunnel {
        tunnel.shutdown().await;
    }
    for tunnel in reverse_tunnels {
        tunnel.shutdown().await;
    }
    topology.shutdown().await;
}

async fn exercise_model_discovery_cluster_lifecycle(transport: TransportMode) {
    init_crypto();

    let topology = start_lifecycle_topology(transport, StargateTopology::Single).await;
    let one_cluster_model = format!("lifecycle-list-one-cluster-{}-model", transport.label());
    let mut backend_a = register_lifecycle_backend_with_options(
        &topology,
        transport,
        LifecycleBackendOptions {
            backend_id: &format!("lifecycle-list-one-cluster-{}-a", transport.label()),
            cluster_id: "lifecycle-list-one-cluster",
            model: &one_cluster_model,
            backend: start_lifecycle_dummy_backend(&one_cluster_model).await,
            bringup: disabled_bringup(),
            auth_token: None,
        },
    )
    .await;
    let mut backend_b = register_lifecycle_backend_with_options(
        &topology,
        transport,
        LifecycleBackendOptions {
            backend_id: &format!("lifecycle-list-one-cluster-{}-b", transport.label()),
            cluster_id: "lifecycle-list-one-cluster",
            model: &one_cluster_model,
            backend: start_lifecycle_dummy_backend(&one_cluster_model).await,
            bringup: disabled_bringup(),
            auth_token: None,
        },
    )
    .await;

    wait_for_routing_on_all(&topology, &one_cluster_model, STATUS_TIMEOUT).await;
    wait_for_active_inference_server_count(
        &topology,
        &one_cluster_model,
        2,
        Duration::from_secs(5),
    )
    .await;
    wait_for_list_models_on_all(
        &topology,
        &[&one_cluster_model],
        &[&one_cluster_model],
        Duration::from_secs(5),
    )
    .await;

    backend_a
        .channels
        .status
        .send_async(InferenceServerStatus::Inactive)
        .await
        .expect("send inactive status");
    wait_for_active_inference_server_count(
        &topology,
        &one_cluster_model,
        1,
        Duration::from_secs(5),
    )
    .await;
    wait_for_backend_serves_all(
        &topology,
        &one_cluster_model,
        &format!("lifecycle-list-one-cluster-{}-b", transport.label()),
        "one-cluster-backend-b",
        STATUS_TIMEOUT,
    )
    .await;
    wait_for_list_models_on_all(
        &topology,
        &[&one_cluster_model],
        &[&one_cluster_model],
        Duration::from_secs(5),
    )
    .await;

    backend_b
        .channels
        .status
        .send_async(InferenceServerStatus::Inactive)
        .await
        .expect("send inactive status");
    wait_for_unroutable_on_all(&topology, &one_cluster_model, STATUS_TIMEOUT).await;
    wait_for_active_inference_server_count(
        &topology,
        &one_cluster_model,
        0,
        Duration::from_secs(5),
    )
    .await;
    wait_for_list_models_on_all(
        &topology,
        &[&one_cluster_model],
        &[],
        Duration::from_secs(5),
    )
    .await;

    let two_cluster_model = format!("lifecycle-list-two-cluster-{}-model", transport.label());
    let mut cluster_a = register_lifecycle_backend_with_options(
        &topology,
        transport,
        LifecycleBackendOptions {
            backend_id: &format!("lifecycle-list-two-cluster-{}-a", transport.label()),
            cluster_id: "lifecycle-list-two-cluster-a",
            model: &two_cluster_model,
            backend: start_lifecycle_dummy_backend(&two_cluster_model).await,
            bringup: disabled_bringup(),
            auth_token: None,
        },
    )
    .await;
    let mut cluster_b = register_lifecycle_backend_with_options(
        &topology,
        transport,
        LifecycleBackendOptions {
            backend_id: &format!("lifecycle-list-two-cluster-{}-b", transport.label()),
            cluster_id: "lifecycle-list-two-cluster-b",
            model: &two_cluster_model,
            backend: start_lifecycle_dummy_backend(&two_cluster_model).await,
            bringup: disabled_bringup(),
            auth_token: None,
        },
    )
    .await;

    wait_for_routing_on_all(&topology, &two_cluster_model, STATUS_TIMEOUT).await;
    wait_for_active_inference_server_count(
        &topology,
        &two_cluster_model,
        2,
        Duration::from_secs(5),
    )
    .await;
    wait_for_list_models_on_all(
        &topology,
        &[&two_cluster_model],
        &[&two_cluster_model],
        Duration::from_secs(5),
    )
    .await;

    cluster_a
        .channels
        .status
        .send_async(InferenceServerStatus::Inactive)
        .await
        .expect("send inactive status");
    wait_for_active_inference_server_count(
        &topology,
        &two_cluster_model,
        1,
        Duration::from_secs(5),
    )
    .await;
    wait_for_backend_serves_all(
        &topology,
        &two_cluster_model,
        &format!("lifecycle-list-two-cluster-{}-b", transport.label()),
        "two-cluster-backend-b",
        STATUS_TIMEOUT,
    )
    .await;
    wait_for_list_models_on_all(
        &topology,
        &[&two_cluster_model],
        &[&two_cluster_model],
        Duration::from_secs(5),
    )
    .await;

    cluster_b
        .channels
        .status
        .send_async(InferenceServerStatus::Inactive)
        .await
        .expect("send inactive status");
    wait_for_unroutable_on_all(&topology, &two_cluster_model, STATUS_TIMEOUT).await;
    wait_for_active_inference_server_count(
        &topology,
        &two_cluster_model,
        0,
        Duration::from_secs(5),
    )
    .await;
    wait_for_list_models_on_all(
        &topology,
        &[&two_cluster_model],
        &[],
        Duration::from_secs(5),
    )
    .await;

    backend_a.stop();
    backend_b.stop();
    cluster_a.stop();
    cluster_b.stop();
    topology.shutdown().await;
}

async fn exercise_routing_key_lifecycle(transport: TransportMode) {
    init_crypto();

    let auth = Arc::new(TokenMapAuthenticator::new([
        ("lifecycle-rk-token-a", "lifecycle-rk-a"),
        ("lifecycle-rk-token-b", "lifecycle-rk-b"),
    ]));
    let topology =
        start_lifecycle_topology_with_auth(transport, StargateTopology::Single, Some(auth)).await;
    let model = format!("lifecycle-rk-{}-model", transport.label());
    let mut tenant_a = register_lifecycle_backend_with_options(
        &topology,
        transport,
        LifecycleBackendOptions {
            backend_id: &format!("lifecycle-rk-{}-backend-a", transport.label()),
            cluster_id: "",
            model: &model,
            backend: start_lifecycle_dummy_backend(&model).await,
            bringup: disabled_bringup(),
            auth_token: Some("lifecycle-rk-token-a".to_string()),
        },
    )
    .await;
    let mut tenant_b = register_lifecycle_backend_with_options(
        &topology,
        transport,
        LifecycleBackendOptions {
            backend_id: &format!("lifecycle-rk-{}-backend-b", transport.label()),
            cluster_id: "",
            model: &model,
            backend: start_lifecycle_dummy_backend(&model).await,
            bringup: disabled_bringup(),
            auth_token: Some("lifecycle-rk-token-b".to_string()),
        },
    )
    .await;

    wait_for_routing_with_rk(
        topology.http_addrs[0],
        Some("lifecycle-rk-a"),
        &model,
        STATUS_TIMEOUT,
    )
    .await;
    wait_for_routing_with_rk(
        topology.http_addrs[0],
        Some("lifecycle-rk-b"),
        &model,
        STATUS_TIMEOUT,
    )
    .await;

    tenant_a
        .channels
        .status
        .send_async(InferenceServerStatus::Inactive)
        .await
        .expect("send inactive status");
    wait_for_unroutable_with_rk(
        topology.http_addrs[0],
        "lifecycle-rk-a",
        &model,
        STATUS_TIMEOUT,
    )
    .await;
    wait_for_routing_with_rk(
        topology.http_addrs[0],
        Some("lifecycle-rk-b"),
        &model,
        STATUS_TIMEOUT,
    )
    .await;

    tenant_a.stop();
    tenant_b.stop();
    topology.shutdown().await;
}

async fn exercise_multi_model_raw_lifecycle(transport: TransportMode) {
    init_crypto();

    let topology = start_lifecycle_topology(transport, StargateTopology::Single).await;
    let model_a = format!("lifecycle-multi-{}-a", transport.label());
    let model_b = format!("lifecycle-multi-{}-b", transport.label());
    let backend_id = format!("lifecycle-multi-{}-backend", transport.label());
    let backend = start_lifecycle_dummy_backend(&model_a).await;
    let upstream_http_base_url = format!("http://{}", backend.addr);
    let (inference_server_url, direct_tunnel) = match transport {
        TransportMode::Direct => {
            let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
                "127.0.0.1:0".parse().unwrap(),
                upstream_http_base_url.clone(),
            ))
            .await
            .expect("direct QUIC tunnel failed to start");
            (format!("quic://{}", tunnel.listen_addr()), Some(tunnel))
        }
        TransportMode::Reverse => (upstream_http_base_url.clone(), None),
    };
    let active_update = raw_registration(
        &backend_id,
        "lifecycle-multi-cluster",
        &inference_server_url,
        &[
            (model_a.as_str(), InferenceServerStatus::Active),
            (model_b.as_str(), InferenceServerStatus::Active),
        ],
        transport.reverse_tunnel(),
        false,
    );
    let raw_registrations = start_raw_registrations(&topology, active_update.clone()).await;
    let reverse_tunnels = if transport.reverse_tunnel() {
        start_reverse_tunnels(&topology, &backend_id, &upstream_http_base_url).await
    } else {
        Vec::new()
    };
    send_raw_update_to_all(&raw_registrations, active_update).await;
    wait_for_routing_on_all(&topology, &model_a, STATUS_TIMEOUT).await;
    wait_for_routing_on_all(&topology, &model_b, STATUS_TIMEOUT).await;

    send_raw_update_to_all(
        &raw_registrations,
        raw_registration(
            &backend_id,
            "lifecycle-multi-cluster",
            &inference_server_url,
            &[
                (model_a.as_str(), InferenceServerStatus::Inactive),
                (model_b.as_str(), InferenceServerStatus::Active),
            ],
            transport.reverse_tunnel(),
            false,
        ),
    )
    .await;
    wait_for_unroutable_on_all(&topology, &model_a, STATUS_TIMEOUT).await;
    wait_for_routing_on_all(&topology, &model_b, STATUS_TIMEOUT).await;

    close_raw_registrations(raw_registrations).await;
    if let Some(tunnel) = direct_tunnel {
        tunnel.shutdown().await;
    }
    for tunnel in reverse_tunnels {
        tunnel.shutdown().await;
    }
    topology.shutdown().await;
}

async fn exercise_status_lifecycle(transport: TransportMode, stargates: StargateTopology) {
    init_crypto();

    let topology = start_lifecycle_topology(transport, stargates).await;
    let model = format!(
        "lifecycle-{}-{}-model",
        transport.label(),
        stargates.label()
    );
    let backend_id = format!(
        "lifecycle-{}-{}-backend",
        transport.label(),
        stargates.label()
    );
    let backend = start_lifecycle_dummy_backend(&model).await;
    let mut registration = register_lifecycle_backend(
        &topology,
        transport,
        &backend_id,
        &model,
        backend,
        disabled_bringup(),
    )
    .await;

    wait_for_routing_on_all(&topology, &model, STATUS_TIMEOUT).await;
    assert_backend_serves_all(&topology, &model, &backend_id, "status-initial").await;

    registration
        .channels
        .status
        .send_async(InferenceServerStatus::Inactive)
        .await
        .expect("send inactive status");
    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;

    registration
        .channels
        .status
        .send_async(InferenceServerStatus::Active)
        .await
        .expect("send active status");
    wait_for_routing_on_all(&topology, &model, STATUS_TIMEOUT).await;
    assert_backend_serves_all(&topology, &model, &backend_id, "status-recovered").await;

    registration.stop();
    wait_for_unroutable_on_all(&topology, &model, STATUS_TIMEOUT).await;

    topology.shutdown().await;
}

async fn start_lifecycle_topology(
    transport: TransportMode,
    stargates: StargateTopology,
) -> LifecycleTopology {
    start_lifecycle_topology_with_auth(transport, stargates, None).await
}

async fn start_lifecycle_topology_with_auth(
    transport: TransportMode,
    stargates: StargateTopology,
    authenticator: Option<Arc<dyn WorkerAuthenticator>>,
) -> LifecycleTopology {
    match (transport, stargates) {
        (TransportMode::Direct, StargateTopology::Single) => {
            let (grpc_addr, model_discovery_addr, http_addr, _reverse_target, runtime) =
                make_lifecycle_single_runtime("lifecycle-direct-single", None, authenticator);
            let handle = runtime.start().await.expect("stargate failed to start");
            LifecycleTopology {
                grpc_seed: grpc_addr,
                grpc_addrs: vec![grpc_addr],
                http_addrs: vec![http_addr],
                model_discovery_addrs: vec![model_discovery_addr],
                reverse_tunnel_targets: Vec::new(),
                handles: vec![handle],
            }
        }
        (TransportMode::Direct, StargateTopology::Pair) => {
            let peers = Arc::new(Mutex::new(Vec::new()));
            let (grpc_addr_1, model_discovery_addr_1, http_addr_1, _reverse_target_1, runtime_1) =
                make_lifecycle_shared_runtime(
                    "lifecycle-direct-pair-1",
                    peers.clone(),
                    None,
                    authenticator.clone(),
                );
            let (grpc_addr_2, model_discovery_addr_2, http_addr_2, _reverse_target_2, runtime_2) =
                make_lifecycle_shared_runtime(
                    "lifecycle-direct-pair-2",
                    peers,
                    None,
                    authenticator,
                );
            let handle_1 = runtime_1.start().await.expect("stargate 1 failed");
            let handle_2 = runtime_2.start().await.expect("stargate 2 failed");
            LifecycleTopology {
                grpc_seed: grpc_addr_1,
                grpc_addrs: vec![grpc_addr_1, grpc_addr_2],
                http_addrs: vec![http_addr_1, http_addr_2],
                model_discovery_addrs: vec![model_discovery_addr_1, model_discovery_addr_2],
                reverse_tunnel_targets: Vec::new(),
                handles: vec![handle_1, handle_2],
            }
        }
        (TransportMode::Reverse, StargateTopology::Single) => {
            let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
            let (grpc_addr, model_discovery_addr, http_addr, reverse_target, runtime) =
                make_lifecycle_single_runtime(
                    "lifecycle-reverse-single",
                    Some((reverse_addr, reverse_socket)),
                    authenticator,
                );
            let handle = runtime.start().await.expect("stargate failed to start");
            LifecycleTopology {
                grpc_seed: grpc_addr,
                grpc_addrs: vec![grpc_addr],
                http_addrs: vec![http_addr],
                model_discovery_addrs: vec![model_discovery_addr],
                reverse_tunnel_targets: vec![reverse_target.expect("reverse target")],
                handles: vec![handle],
            }
        }
        (TransportMode::Reverse, StargateTopology::Pair) => {
            let peers = Arc::new(Mutex::new(Vec::new()));
            let (reverse_addr_1, reverse_socket_1) = bind_ephemeral_udp();
            let (reverse_addr_2, reverse_socket_2) = bind_ephemeral_udp();
            let (grpc_addr_1, model_discovery_addr_1, http_addr_1, reverse_target_1, runtime_1) =
                make_lifecycle_shared_runtime(
                    "lifecycle-reverse-pair-1",
                    peers.clone(),
                    Some((reverse_addr_1, reverse_socket_1)),
                    authenticator.clone(),
                );
            let (grpc_addr_2, model_discovery_addr_2, http_addr_2, reverse_target_2, runtime_2) =
                make_lifecycle_shared_runtime(
                    "lifecycle-reverse-pair-2",
                    peers,
                    Some((reverse_addr_2, reverse_socket_2)),
                    authenticator,
                );
            let handle_1 = runtime_1.start().await.expect("stargate 1 failed");
            let handle_2 = runtime_2.start().await.expect("stargate 2 failed");
            LifecycleTopology {
                grpc_seed: grpc_addr_1,
                grpc_addrs: vec![grpc_addr_1, grpc_addr_2],
                http_addrs: vec![http_addr_1, http_addr_2],
                model_discovery_addrs: vec![model_discovery_addr_1, model_discovery_addr_2],
                reverse_tunnel_targets: vec![
                    reverse_target_1.expect("reverse target 1"),
                    reverse_target_2.expect("reverse target 2"),
                ],
                handles: vec![handle_1, handle_2],
            }
        }
    }
}

fn make_lifecycle_single_runtime(
    id: &str,
    reverse: Option<(SocketAddr, std::net::UdpSocket)>,
    authenticator: Option<Arc<dyn WorkerAuthenticator>>,
) -> (
    SocketAddr,
    SocketAddr,
    SocketAddr,
    Option<String>,
    StargateRuntime,
) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (model_discovery_addr, model_discovery_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SelfDiscovery::new(id, grpc_addr, http_addr);
    make_lifecycle_runtime(LifecycleRuntimeParts {
        id,
        grpc_addr,
        grpc_listener,
        model_discovery_addr,
        model_discovery_listener,
        http_addr,
        http_listener,
        discovery: Box::new(discovery),
        reverse,
        authenticator,
    })
}

fn make_lifecycle_shared_runtime(
    id: &str,
    peers: Arc<Mutex<Vec<StargateInfo>>>,
    reverse: Option<(SocketAddr, std::net::UdpSocket)>,
    authenticator: Option<Arc<dyn WorkerAuthenticator>>,
) -> (
    SocketAddr,
    SocketAddr,
    SocketAddr,
    Option<String>,
    StargateRuntime,
) {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (model_discovery_addr, model_discovery_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let discovery = SharedDiscovery::new(id, grpc_addr, http_addr, peers);
    make_lifecycle_runtime(LifecycleRuntimeParts {
        id,
        grpc_addr,
        grpc_listener,
        model_discovery_addr,
        model_discovery_listener,
        http_addr,
        http_listener,
        discovery: Box::new(discovery),
        reverse,
        authenticator,
    })
}

fn make_lifecycle_runtime(
    parts: LifecycleRuntimeParts<'_>,
) -> (
    SocketAddr,
    SocketAddr,
    SocketAddr,
    Option<String>,
    StargateRuntime,
) {
    let LifecycleRuntimeParts {
        id,
        grpc_addr,
        grpc_listener,
        model_discovery_addr,
        model_discovery_listener,
        http_addr,
        http_listener,
        discovery,
        reverse,
        authenticator,
    } = parts;

    let mut config = base_config(id, grpc_addr, http_addr);
    config.model_discovery_listen_addr = model_discovery_addr;
    config.dns_poll_interval = Duration::from_secs(1);

    let mut reverse_target = None;
    let reverse_socket = reverse.map(|(reverse_addr, reverse_socket)| {
        config.advertised_hostname_template = Some("localhost".to_string());
        config.reverse_tunnel_listen_addr = Some(reverse_addr);
        config.reverse_tunnel_connect_timeout = Duration::from_millis(250);
        reverse_target = Some(format!("localhost:{}", reverse_addr.port()));
        reverse_socket
    });

    let mut runtime = StargateRuntime::new(config, discovery)
        .with_grpc_listener(grpc_listener)
        .with_model_discovery_listener(model_discovery_listener)
        .with_http_listener(http_listener);
    if let Some(reverse_socket) = reverse_socket {
        runtime = runtime.with_reverse_tunnel_socket(reverse_socket);
    }
    if let Some(authenticator) = authenticator {
        runtime = runtime.with_authenticator(authenticator);
    }

    (
        grpc_addr,
        model_discovery_addr,
        http_addr,
        reverse_target,
        runtime,
    )
}

async fn register_lifecycle_backend(
    topology: &LifecycleTopology,
    transport: TransportMode,
    backend_id: &str,
    model: &str,
    backend: LifecycleDummyBackend,
    bringup: BringupConfig,
) -> LifecycleRegistration {
    register_lifecycle_backend_with_options(
        topology,
        transport,
        LifecycleBackendOptions {
            backend_id,
            cluster_id: "",
            model,
            backend,
            bringup,
            auth_token: None,
        },
    )
    .await
}

async fn register_lifecycle_backend_with_options(
    topology: &LifecycleTopology,
    transport: TransportMode,
    options: LifecycleBackendOptions<'_>,
) -> LifecycleRegistration {
    let LifecycleBackendOptions {
        backend_id,
        cluster_id,
        model,
        backend,
        bringup,
        auth_token,
    } = options;

    let upstream_http_base_url = format!("http://{}", backend.addr);
    let (inference_server_url, tunnel) = if transport.reverse_tunnel() {
        (upstream_http_base_url.clone(), None)
    } else {
        let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
            "127.0.0.1:0".parse().unwrap(),
            upstream_http_base_url.clone(),
        ))
        .await
        .expect("direct QUIC tunnel failed to start");
        (format!("quic://{}", tunnel.listen_addr()), Some(tunnel))
    };

    let mut reg_client = InferenceServerRegistrationClient::default();
    let channels = reg_client
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![topology.grpc_seed.to_string()],
                inference_server_id: backend_id.to_string(),
                cluster_id: cluster_id.to_string(),
                inference_server_url,
                upstream_http_base_url: Some(upstream_http_base_url),
                // Short heartbeats keep lifecycle transition tests fast while
                // still exercising the registration update path.
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: transport.reverse_tunnel(),
                bringup,
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: auth_token.map(AuthTokenProvider::Static).map(Arc::new),
                quic_insecure: true,
                tunnel_protocol: pylon_lib::TunnelTransportProtocol::Custom,
            },
            vec![model.to_string()],
        )
        .expect("registration failed");

    LifecycleRegistration {
        backend,
        reg_client,
        channels,
        tunnel,
    }
}

fn disabled_bringup() -> BringupConfig {
    // The status matrix drives lifecycle explicitly through registration
    // status, so calibration is disabled to keep the transport/topology signal
    // isolated.
    BringupConfig {
        enabled: false,
        ..BringupConfig::default()
    }
}

fn calibration_bringup_without_active_canary() -> BringupConfig {
    // These tests isolate initial bringup gating; active canaries would add an
    // unrelated repeating lifecycle transition after the model advertises.
    BringupConfig {
        enabled: true,
        active_canary_interval: Duration::ZERO,
        canary_timeout: Duration::from_millis(250),
        calibration_requests: 5,
        calibration_prompt_units: 256,
        calibration_max_concurrency: 1,
        calibration_timeout: Duration::from_secs(2),
        ..BringupConfig::default()
    }
}

fn active_canary_bringup() -> BringupConfig {
    // Calibration is skipped here so the test focuses on active canary
    // demotion and recovery after the backend is already routable.
    BringupConfig {
        enabled: true,
        active_canary_interval: Duration::from_millis(50),
        canary_timeout: Duration::from_millis(250),
        canary_max_generation_threshold: CANARY_FAILURE_TOKEN_THRESHOLD,
        calibration_requests: 0,
        ..BringupConfig::default()
    }
}

fn coordinated_calibration_bringup() -> BringupConfig {
    BringupConfig {
        enabled: true,
        active_canary_interval: Duration::ZERO,
        canary_timeout: Duration::from_millis(250),
        calibration_requests: 5,
        calibration_prompt_units: 256,
        calibration_max_concurrency: 1,
        calibration_timeout: Duration::from_secs(2),
        ..BringupConfig::default()
    }
}

async fn start_raw_registration(
    grpc_addr: SocketAddr,
    initial: InferenceServerRegistration,
) -> RawRegistration {
    let endpoint = format!("http://{grpc_addr}");
    let channel = Channel::from_shared(endpoint)
        .expect("invalid endpoint")
        .connect()
        .await
        .expect("connect failed");
    let mut client = StargateControlPlaneClient::new(channel);
    let (tx, rx) = flume::bounded(16);
    tx.send_async(initial)
        .await
        .expect("send initial raw registration");
    let response = client
        .register_inference_server(rx.into_stream())
        .await
        .expect("raw registration failed");
    let mut stream = response.into_inner();
    let ack_task = tokio::spawn(async move {
        loop {
            match stream.message().await {
                Ok(Some(_)) => {}
                Ok(None) => return Ok(()),
                Err(status) => return Err(status),
            }
        }
    });
    RawRegistration { tx, ack_task }
}

async fn start_raw_registrations(
    topology: &LifecycleTopology,
    initial: InferenceServerRegistration,
) -> Vec<RawRegistration> {
    let mut registrations = Vec::new();
    for &grpc_addr in &topology.grpc_addrs {
        registrations.push(start_raw_registration(grpc_addr, initial.clone()).await);
    }
    registrations
}

async fn send_raw_update_to_all(
    registrations: &[RawRegistration],
    update: InferenceServerRegistration,
) {
    for registration in registrations {
        registration.send(update.clone()).await;
    }
}

async fn close_raw_registrations(registrations: Vec<RawRegistration>) {
    for registration in registrations {
        registration.close().await;
    }
}

async fn close_raw_registrations_expect_error(
    registrations: Vec<RawRegistration>,
    code: tonic::Code,
    message: &str,
) {
    for registration in registrations {
        registration.close_expecting_error(code, message).await;
    }
}

fn raw_registration(
    backend_id: &str,
    cluster_id: &str,
    inference_server_url: &str,
    models: &[(&str, InferenceServerStatus)],
    reverse_tunnel: bool,
    coordinated_calibration: bool,
) -> InferenceServerRegistration {
    let models = models
        .iter()
        .map(|(model, status)| {
            (
                (*model).to_string(),
                InferenceServerModelRegistration {
                    stats: Some(ModelStats {
                        output_tps: 0.0,
                        last_mean_input_tps: 100.0,
                        max_output_tps: 100.0,
                        queue_size: 0,
                        queued_input_size: 0,
                        kv_cache_capacity_tokens: 0,
                        kv_cache_used_tokens: 0,
                        kv_cache_free_tokens: 0,
                        num_running_queries: 0,
                        max_engine_concurrency: 0,
                        total_query_input_size: 0,
                        queue_time_estimate_ms_by_priority: HashMap::new(),
                        ..ModelStats::default()
                    }),
                    status: (*status).into(),
                },
            )
        })
        .collect();

    InferenceServerRegistration {
        inference_server_id: backend_id.to_string(),
        cluster_id: cluster_id.to_string(),
        inference_server_url: inference_server_url.to_string(),
        models,
        reverse_tunnel,
        coordinated_calibration,
    }
}

async fn start_reverse_tunnels(
    topology: &LifecycleTopology,
    backend_id: &str,
    upstream_http_base_url: &str,
) -> Vec<ReverseQuicTunnelHandle> {
    let mut tunnels = Vec::new();
    for target in &topology.reverse_tunnel_targets {
        let mut config = ReverseQuicTunnelConfig::new(
            target.clone(),
            backend_id.to_string(),
            upstream_http_base_url.to_string(),
        );
        config.quic_insecure = true;
        config.output_token_parser_factory = OutputTokenParserFactory::vllm();
        tunnels.push(
            start_reverse_quic_tunnel(config)
                .await
                .expect("reverse QUIC tunnel failed to start"),
        );
    }
    tunnels
}

async fn wait_for_routing_on_all(topology: &LifecycleTopology, model: &str, timeout: Duration) {
    for &http_addr in &topology.http_addrs {
        wait_for_routing(http_addr, model, timeout).await;
    }
}

async fn wait_for_unroutable_on_all(topology: &LifecycleTopology, model: &str, timeout: Duration) {
    for &http_addr in &topology.http_addrs {
        wait_for_unroutable(http_addr, model, timeout).await;
    }
}

async fn wait_for_unroutable_with_rk(
    http_addr: SocketAddr,
    routing_key: &str,
    model: &str,
    timeout: Duration,
) {
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    let deadline = tokio::time::Instant::now() + timeout;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            model,
            "req-wait-unroutable-rk",
        )
        .header("x-routing-key", routing_key)
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await;
        match resp {
            Ok(r) if matches!(r.status().as_u16(), 404 | 502 | 503) => return,
            Err(_) => return,
            _ => {
                if tokio::time::Instant::now() >= deadline {
                    panic!(
                        "model '{model}' with routing key '{routing_key}' still routable after {}s",
                        timeout.as_secs()
                    );
                }
                poll.tick().await;
            }
        }
    }
}

async fn wait_for_list_models_on_all(
    topology: &LifecycleTopology,
    model_ids: &[&str],
    expected_ids: &[&str],
    timeout: Duration,
) {
    for &model_discovery_addr in &topology.model_discovery_addrs {
        wait_for_list_models(model_discovery_addr, None, model_ids, expected_ids, timeout).await;
    }
}

async fn wait_for_list_models(
    model_discovery_addr: SocketAddr,
    routing_key: Option<&str>,
    model_ids: &[&str],
    expected_ids: &[&str],
    timeout: Duration,
) -> Vec<String> {
    let mut expected_ids = expected_ids.to_vec();
    expected_ids.sort_unstable();
    let mut client = connect_model_discovery(model_discovery_addr).await;
    let deadline = tokio::time::Instant::now() + timeout;
    let mut poll = tokio::time::interval(Duration::from_millis(200));
    loop {
        let response = client
            .list_models(ListModelsRequest {
                routing_key: routing_key.map(ToOwned::to_owned),
                model_ids: model_ids.iter().map(|model| (*model).to_string()).collect(),
            })
            .await
            .expect("ListModels RPC failed")
            .into_inner();
        let mut actual_ids = response
            .model_ids
            .iter()
            .map(String::as_str)
            .collect::<Vec<_>>();
        actual_ids.sort_unstable();
        if actual_ids == expected_ids {
            return response.model_ids;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "ListModels returned ids {actual_ids:?}; expected {expected_ids:?} within {}s",
            timeout.as_secs()
        );
        poll.tick().await;
    }
}

async fn connect_model_discovery(
    model_discovery_addr: SocketAddr,
) -> StargateModelDiscoveryClient<Channel> {
    let endpoint = format!("http://{model_discovery_addr}");
    StargateModelDiscoveryClient::connect(endpoint)
        .await
        .expect("connect to model discovery")
}

async fn wait_for_backend_serves_all(
    topology: &LifecycleTopology,
    model: &str,
    expected_backend: &str,
    request_id_prefix: &str,
    timeout: Duration,
) {
    let deadline = tokio::time::Instant::now() + timeout;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        if backend_serves_all(topology, model, expected_backend, request_id_prefix).await {
            return;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "model '{model}' did not route only to backend '{expected_backend}' within {}s",
            timeout.as_secs()
        );
        poll.tick().await;
    }
}

async fn backend_serves_all(
    topology: &LifecycleTopology,
    model: &str,
    expected_backend: &str,
    request_id_prefix: &str,
) -> bool {
    let http_client = reqwest::Client::new();
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    for (index, http_addr) in topology.http_addrs.iter().enumerate() {
        let resp = with_proxy_headers(
            http_client.post(format!("http://{http_addr}/v1/chat/completions")),
            model,
            &format!("{request_id_prefix}-{index}"),
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await;
        let Ok(resp) = resp else {
            return false;
        };
        if resp.status() != StatusCode::OK {
            return false;
        }
        let backend = resp
            .headers()
            .get("x-inference-server-id")
            .and_then(|value| value.to_str().ok());
        if backend != Some(expected_backend) {
            return false;
        }
        if !resp.text().await.is_ok_and(|text| text.contains("[DONE]")) {
            return false;
        }
    }
    true
}

async fn wait_for_metrics_contains(
    topology: &LifecycleTopology,
    expected: &str,
    timeout: Duration,
) {
    for handle in &topology.handles {
        wait_for_metrics_contains_in_registry(handle.metrics().registry(), expected, timeout).await;
    }
}

async fn wait_for_metrics_contains_in_registry(
    registry: Arc<prometheus::Registry>,
    expected: &str,
    timeout: Duration,
) {
    let deadline = tokio::time::Instant::now() + timeout;
    let mut poll = tokio::time::interval(Duration::from_millis(100));
    loop {
        let metrics = metrics_text(registry.clone());
        if metrics.lines().any(|line| line == expected) {
            return;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "metrics did not contain {expected:?} within {}s:\n{metrics}",
            timeout.as_secs()
        );
        poll.tick().await;
    }
}

async fn wait_for_active_inference_server_count(
    topology: &LifecycleTopology,
    model: &str,
    count: usize,
    timeout: Duration,
) {
    wait_for_metrics_contains(
        topology,
        &format!(r#"stargate_active_inference_servers{{model="{model}",routing_key=""}} {count}"#),
        timeout,
    )
    .await;
}

fn metrics_text(registry: Arc<prometheus::Registry>) -> String {
    let encoder = TextEncoder::new();
    let families = registry.gather();
    let mut buffer = Vec::new();
    encoder
        .encode(&families, &mut buffer)
        .expect("encode metrics");
    String::from_utf8(buffer).expect("metrics must be utf8")
}

async fn assert_backend_serves_all(
    topology: &LifecycleTopology,
    model: &str,
    expected_backend: &str,
    request_id_prefix: &str,
) {
    let http_client = reqwest::Client::new();
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    for (index, http_addr) in topology.http_addrs.iter().enumerate() {
        let resp = with_proxy_headers(
            http_client.post(format!("http://{http_addr}/v1/chat/completions")),
            model,
            &format!("{request_id_prefix}-{index}"),
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("proxy request failed");
        let status = resp.status();
        let backend = resp
            .headers()
            .get("x-inference-server-id")
            .and_then(|value| value.to_str().ok())
            .map(str::to_string);
        let text = resp.text().await.expect("read proxy body");
        assert_eq!(status, StatusCode::OK, "body={text}");
        assert_eq!(backend.as_deref(), Some(expected_backend));
        assert!(text.contains("[DONE]"), "stream should complete: {text}");
    }
}

async fn wait_for_counter_at_least<F>(label: &str, expected: usize, timeout: Duration, observed: F)
where
    F: Fn() -> usize,
{
    let deadline = tokio::time::Instant::now() + timeout;
    let mut poll = tokio::time::interval(Duration::from_millis(50));
    loop {
        let current = observed();
        if current >= expected {
            return;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "{label} did not reach {expected} within {}s; observed {current}",
            timeout.as_secs()
        );
        poll.tick().await;
    }
}

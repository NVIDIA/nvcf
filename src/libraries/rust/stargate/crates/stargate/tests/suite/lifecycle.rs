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

use std::collections::{BTreeSet, HashSet};
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::common::sse::parse_sse_events;
use crate::common::{
    LifecycleDummyBackend, SelfDiscovery, SharedDiscovery, TokenMapAuthenticator, TunnelTestCase,
    base_config, bind_ephemeral, bind_ephemeral_udp, direct_registration_config, init_crypto,
    reverse_registration_config, start_lifecycle_dummy_backend, wait_for_inference_server_ids,
    wait_for_routing, wait_for_routing_with_rk, wait_for_unroutable, wait_until,
    with_proxy_headers,
};
use prometheus::{Encoder, TextEncoder};
use pylon_lib::{
    AuthTokenProvider, BringupConfig, CurrentModelStats, InferenceServerRegistrationClient,
    ModelInitialization, ModelLifecycleConfig, ModelLifecycleHandle, ModelSource,
    PylonRuntimeState, QuicHttpTunnelConfig, QuicHttpTunnelHandle, ReverseQuicTunnelConfig,
    ReverseQuicTunnelHandle, StatsCollectorConfig, TunnelTransportProtocol, start_model_lifecycle,
    start_quic_http_tunnel, start_reverse_quic_tunnel,
};
use reqwest::StatusCode;
use stargate::auth::WorkerAuthenticator;
use stargate::routing::RoutingTargetKey;
use stargate::runtime::{
    BoundStargateListeners, ReverseTunnelConfig, StargateHandle, StargateRuntime,
};
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
const DIRECT: TunnelTestCase = TunnelTestCase::direct(TunnelTransportProtocol::RawQuic);
const REVERSE: TunnelTestCase = TunnelTestCase::reverse(TunnelTransportProtocol::RawQuic);
const ACTIVE: InferenceServerStatus = InferenceServerStatus::Active;
const INACTIVE: InferenceServerStatus = InferenceServerStatus::Inactive;

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
    grpc_addrs: Vec<SocketAddr>,
    http_addrs: Vec<SocketAddr>,
    model_discovery_addrs: Vec<SocketAddr>,
    reverse_tunnel_targets: Vec<String>,
    tunnel_protocol: TunnelTransportProtocol,
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

    async fn assert_serving(&self, model: &str, expected_backend: &str, request_id_prefix: &str) {
        check_serving(self, model, expected_backend, request_id_prefix)
            .await
            .unwrap_or_else(|error| panic!("backend response assertion failed: {error}"));
    }

    async fn wait_routing(&self, model: &str) {
        for &http_addr in &self.http_addrs {
            wait_for_routing(http_addr, model, STATUS_TIMEOUT).await;
        }
    }

    async fn wait_unroutable(&self, model: &str) {
        for &http_addr in &self.http_addrs {
            wait_for_unroutable(http_addr, model, STATUS_TIMEOUT).await;
        }
    }
}

struct LifecycleRegistration {
    backend: LifecycleDummyBackend,
    reg_client: InferenceServerRegistrationClient,
    runtime_state: PylonRuntimeState,
    _model_lifecycle: ModelLifecycleHandle,
    tunnel: Option<QuicHttpTunnelHandle>,
}

struct RawBackendPath {
    backend_id: String,
    upstream_http_base_url: String,
    inference_server_url: String,
    direct_tunnel: Option<QuicHttpTunnelHandle>,
    reverse_tunnels: Vec<ReverseQuicTunnelHandle>,
}

impl RawBackendPath {
    async fn prepare(case: TunnelTestCase, backend_id: &str, backend_addr: SocketAddr) -> Self {
        let upstream_http_base_url = format!("http://{backend_addr}");
        let (inference_server_url, direct_tunnel) = if case.reverse_tunnel() {
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
        Self {
            backend_id: backend_id.to_string(),
            upstream_http_base_url,
            inference_server_url,
            direct_tunnel,
            reverse_tunnels: Vec::new(),
        }
    }

    async fn connect_reverse(&mut self, topology: &LifecycleTopology) {
        if self.direct_tunnel.is_none() {
            self.reverse_tunnels =
                start_reverse_tunnels(topology, &self.backend_id, &self.upstream_http_base_url)
                    .await;
        }
    }

    fn registration(
        &self,
        cluster_id: &str,
        models: &[(&str, InferenceServerStatus)],
    ) -> InferenceServerRegistration {
        raw_registration(
            &self.backend_id,
            cluster_id,
            &self.inference_server_url,
            models,
            self.direct_tunnel.is_none(),
        )
    }

    async fn register(
        &mut self,
        topology: &LifecycleTopology,
        cluster_id: &str,
        models: &[(&str, InferenceServerStatus)],
    ) -> RawRegistrations {
        let update = self.registration(cluster_id, models);
        let registrations = RawRegistrations::start(topology, update.clone()).await;
        self.connect_reverse(topology).await;
        registrations.send(update).await;
        registrations
    }

    async fn shutdown(self) {
        if let Some(tunnel) = self.direct_tunnel {
            tunnel.shutdown().await;
        }
        for tunnel in self.reverse_tunnels {
            tunnel.shutdown().await;
        }
    }
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

    fn set_status(&self, status: InferenceServerStatus) {
        self.runtime_state.set_status(status);
    }

    fn set_model_stats(&self, model: &str, last_mean_input_tps: f64, queued_input_size: u64) {
        self.runtime_state.set_model_stats(
            model.to_string(),
            CurrentModelStats {
                last_mean_input_tps,
                queued_input_size,
                ..CurrentModelStats::default()
            },
        );
    }
}

struct RawRegistration {
    tx: flume::Sender<InferenceServerRegistration>,
    ack_task: JoinHandle<Result<(), tonic::Status>>,
}

struct RawRegistrations(Vec<RawRegistration>);

struct LifecycleBackendOptions<'a> {
    backend_id: &'a str,
    cluster_id: &'a str,
    models: &'a [&'a str],
    backend: LifecycleDummyBackend,
    bringup: BringupConfig,
    auth_token: Option<String>,
}

impl<'a> LifecycleBackendOptions<'a> {
    fn new(backend_id: &'a str, models: &'a [&'a str], backend: LifecycleDummyBackend) -> Self {
        Self {
            backend_id,
            cluster_id: "",
            models,
            backend,
            bringup: disabled_bringup(),
            auth_token: None,
        }
    }

    async fn serving(backend_id: &'a str, models: &'a [&'a str], served_model: &str) -> Self {
        Self::new(
            backend_id,
            models,
            start_lifecycle_dummy_backend(served_model).await,
        )
    }

    fn in_cluster(mut self, cluster_id: &'a str) -> Self {
        self.cluster_id = cluster_id;
        self
    }

    fn with_bringup(mut self, bringup: BringupConfig) -> Self {
        self.bringup = bringup;
        self
    }

    fn authenticated(mut self, token: impl Into<String>) -> Self {
        self.auth_token = Some(token.into());
        self
    }
}

struct LifecycleCase {
    topology: LifecycleTopology,
    model: String,
    backend_id: String,
    registration: LifecycleRegistration,
}

impl LifecycleCase {
    async fn start(
        case: TunnelTestCase,
        stargates: StargateTopology,
        name: Option<&str>,
        bringup: BringupConfig,
        prepare: impl FnOnce(&LifecycleDummyBackend),
    ) -> Self {
        let stem = match name {
            Some(name) => format!(
                "lifecycle-{name}-{}-{}",
                case.direction_label(),
                stargates.label()
            ),
            None => format!("lifecycle-{}-{}", case.direction_label(), stargates.label()),
        };
        Self::start_exact(case, stargates, &stem, bringup, prepare).await
    }

    async fn start_exact(
        case: TunnelTestCase,
        stargates: StargateTopology,
        stem: &str,
        bringup: BringupConfig,
        prepare: impl FnOnce(&LifecycleDummyBackend),
    ) -> Self {
        init_crypto();
        let topology = start_lifecycle_topology(case, stargates).await;
        let model = format!("{stem}-model");
        let backend_id = format!("{stem}-backend");
        let backend = start_lifecycle_dummy_backend(&model).await;
        prepare(&backend);
        let registration = LifecycleBackendOptions::new(&backend_id, &[&model], backend)
            .with_bringup(bringup)
            .register(&topology, case)
            .await;
        Self {
            topology,
            model,
            backend_id,
            registration,
        }
    }

    async fn shutdown(mut self) {
        self.registration.stop();
        self.topology.shutdown().await;
    }

    async fn wait_for_routing(&self, timeout: Duration) {
        for &http_addr in &self.topology.http_addrs {
            wait_for_routing(http_addr, &self.model, timeout).await;
        }
    }

    async fn wait_for_unroutable(&self, timeout: Duration) {
        for &http_addr in &self.topology.http_addrs {
            wait_for_unroutable(http_addr, &self.model, timeout).await;
        }
    }

    async fn assert_serves(&self, request_id_prefix: &str) {
        self.topology
            .assert_serving(&self.model, &self.backend_id, request_id_prefix)
            .await;
    }
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
        let result = match tokio::time::timeout(Duration::from_secs(5), &mut ack_task).await {
            Ok(result) => result.expect("raw registration ack task failed"),
            Err(_) => {
                ack_task.abort();
                panic!("raw registration ack stream did not close within 5s after sender dropped");
            }
        };
        match (result, expected_error) {
            (Ok(()), None) => {}
            (Ok(()), Some(error)) => {
                panic!(
                    "expected raw registration stream error {error:?}, but stream closed cleanly"
                )
            }
            (Err(status), None) => panic!("raw registration stream returned error: {status}"),
            (Err(status), Some((code, message))) => {
                assert_eq!(status.code(), code, "raw registration stream error code");
                assert!(
                    status.message().contains(message),
                    "raw registration stream error message {:?} did not contain {:?}",
                    status.message(),
                    message
                );
            }
        }
    }
}

impl RawRegistrations {
    async fn start(topology: &LifecycleTopology, initial: InferenceServerRegistration) -> Self {
        let mut registrations = Vec::with_capacity(topology.grpc_addrs.len());
        for &grpc_addr in &topology.grpc_addrs {
            registrations.push(start_raw_registration(grpc_addr, initial.clone()).await);
        }
        Self(registrations)
    }

    async fn send(&self, update: InferenceServerRegistration) {
        for registration in &self.0 {
            registration.send(update.clone()).await;
        }
    }

    async fn close(self) {
        for registration in self.0 {
            registration.close().await;
        }
    }

    async fn close_expecting_error(self, code: tonic::Code, message: &str) {
        for registration in self.0 {
            registration.close_expecting_error(code, message).await;
        }
    }
}

macro_rules! lifecycle_tests {
    ($($name:ident: $($exercise:expr),+;)+) => {$(
        #[tokio::test]
        async fn $name() {
            $($exercise.await;)+
        }
    )+};
}

macro_rules! wait_requests {
    ($observed:expr; reaches $expected:expr, $label:expr) => {
        wait_requests!($observed; reaches $expected, $label, Duration::from_secs(5))
    };
    ($observed:expr; reaches $expected:expr, $label:expr, $timeout:expr) => {
        wait_until($label, $timeout, Duration::from_millis(50), || async {
            let current = $observed;
            (current >= $expected)
                .then_some(())
                .ok_or_else(|| format!("observed {current}; expected at least {}", $expected))
        })
        .await
    };
}

lifecycle_tests! {
    direct_single_stargate_status_lifecycle: exercise_status_lifecycle(DIRECT, StargateTopology::Single);
    direct_multi_stargate_status_lifecycle: exercise_status_lifecycle(DIRECT, StargateTopology::Pair);
    reverse_single_stargate_status_lifecycle: exercise_status_lifecycle(REVERSE, StargateTopology::Single);
    reverse_multi_stargate_status_lifecycle: exercise_status_lifecycle(REVERSE, StargateTopology::Pair);
    active_canary_failure_demotes_backend_until_recovery_canary_succeeds: exercise_active_canary_failure(DIRECT, StargateTopology::Single);
    active_canary_failure_gates_reverse_and_fanout_topologies: exercise_active_canary_failure(DIRECT, StargateTopology::Pair),
        exercise_active_canary_failure(REVERSE, StargateTopology::Single),
        exercise_active_canary_failure(REVERSE, StargateTopology::Pair);
}

#[tokio::test]
async fn active_canary_failure_checks_health_before_recovery() {
    let case = LifecycleCase::start_exact(
        DIRECT,
        StargateTopology::Single,
        "lifecycle-canary-health",
        active_canary_bringup(),
        |_| {},
    )
    .await;
    let backend = &case.registration.backend;

    case.wait_for_routing(BRINGUP_TIMEOUT).await;
    let successful_canaries = backend.canary_requests();
    let healthy_checks = backend.health_requests();
    let next_canary = successful_canaries + 1;
    let next_health_check = healthy_checks + 1;

    backend.set_health_ok(false);
    backend.set_completions_ok(false);
    wait_requests!(backend.canary_requests(); reaches next_canary, "failing active canary requests");
    wait_requests!(backend.health_requests(); reaches next_health_check, "failing health checks");
    case.wait_for_unroutable(STATUS_TIMEOUT).await;

    let unhealthy_checks = backend.health_requests();
    let next_health_check = unhealthy_checks + 1;
    backend.set_completions_ok(true);
    wait_requests!(backend.health_requests(); reaches next_health_check, "unhealthy recovery health checks");
    case.wait_for_unroutable(STATUS_TIMEOUT).await;

    backend.set_health_ok(true);
    case.wait_for_routing(BRINGUP_TIMEOUT).await;
    case.assert_serves("canary-health-recovered").await;
    case.shutdown().await;
}

#[tokio::test]
async fn server_health_rtt_gates_initial_and_post_active_updates() {
    for (transport, topology) in [
        (DIRECT, StargateTopology::Single),
        (DIRECT, StargateTopology::Pair),
        (REVERSE, StargateTopology::Single),
        (REVERSE, StargateTopology::Pair),
    ] {
        exercise_server_health_rtt_lifecycle(transport, topology).await;
    }
}

lifecycle_tests! {
    direct_registration_routes_when_tunnel_starts_after_active_update: exercise_direct_late_tunnel(StargateTopology::Single),
        exercise_direct_late_tunnel(StargateTopology::Pair);
    reverse_registration_active_update_waits_for_tunnel_connection: exercise_reverse_tunnel_gate(StargateTopology::Single),
        exercise_reverse_tunnel_gate(StargateTopology::Pair);
    identity_mutation_closes_registration_and_removes_prior_route: exercise_identity_mutation_closes_route(DIRECT),
        exercise_identity_mutation_closes_route(REVERSE);
    list_models_tracks_backend_and_cluster_lifecycle: exercise_model_discovery_cluster_lifecycle(DIRECT),
        exercise_model_discovery_cluster_lifecycle(REVERSE);
}

#[tokio::test]
async fn lifecycle_metrics_report_active_inference_server_count() {
    for (transport, stargates) in [
        (DIRECT, StargateTopology::Single),
        (DIRECT, StargateTopology::Pair),
        (REVERSE, StargateTopology::Single),
        (REVERSE, StargateTopology::Pair),
    ] {
        exercise_lifecycle_metrics(transport, stargates).await;
    }
}

async fn exercise_lifecycle_metrics(case: TunnelTestCase, stargates: StargateTopology) {
    let case =
        LifecycleCase::start(case, stargates, Some("metrics"), disabled_bringup(), |_| {}).await;
    case.wait_for_routing(STATUS_TIMEOUT).await;
    wait_active_count(&case.topology, &case.model, 1).await;
    case.registration.set_status(INACTIVE);
    case.wait_for_unroutable(STATUS_TIMEOUT).await;
    wait_active_count(&case.topology, &case.model, 0).await;
    case.shutdown().await;
}

lifecycle_tests! {
    routing_key_lifecycle_demotes_only_matching_tenant: exercise_routing_key_lifecycle(DIRECT),
        exercise_routing_key_lifecycle(REVERSE);
    multi_model_registration_demotes_only_matching_model: exercise_multi_model_raw_lifecycle(DIRECT), exercise_multi_model_raw_lifecycle(REVERSE);
    direct_shared_cluster_asymmetric_models_preserve_sibling_routes: exercise_shared_cluster_asymmetric_models(DIRECT);
    reverse_shared_cluster_asymmetric_models_preserve_sibling_routes: exercise_shared_cluster_asymmetric_models(REVERSE);
    raw_shared_cluster_asymmetric_model_updates_preserve_sibling_routes: exercise_raw_shared_cluster_asymmetric_models(DIRECT),
        exercise_raw_shared_cluster_asymmetric_models(REVERSE);
}

#[tokio::test]
async fn direct_lost_quic_connection_stays_unroutable_without_recovery() {
    let mut case = LifecycleCase::start_exact(
        DIRECT,
        StargateTopology::Single,
        "lifecycle-direct-lost-quic",
        disabled_bringup(),
        |_| {},
    )
    .await;

    case.wait_for_routing(STATUS_TIMEOUT).await;
    case.registration.shutdown_tunnel().await;
    case.wait_for_unroutable(STATUS_TIMEOUT).await;
    wait_models(&case.topology, &[], &[], Duration::from_secs(15)).await;
    case.shutdown().await;
}

#[tokio::test]
async fn direct_http3_and_webtransport_connection_loss_retires_old_generation() {
    for protocol in [
        TunnelTransportProtocol::Http3,
        TunnelTransportProtocol::WebTransport,
    ] {
        exercise_direct_protocol_connection_lifecycle(protocol).await;
    }
}

async fn exercise_active_canary_failure(case: TunnelTestCase, stargates: StargateTopology) {
    let case = LifecycleCase::start(
        case,
        stargates,
        Some("canary"),
        active_canary_bringup(),
        |backend| backend.set_completion_tokens(1),
    )
    .await;
    let backend = &case.registration.backend;

    case.wait_for_routing(BRINGUP_TIMEOUT).await;
    case.assert_serves("canary-active").await;

    let successful_canaries = backend.canary_requests();
    let next_canary = successful_canaries + 1;
    backend.set_completion_tokens(CANARY_FAILURE_TOKEN_THRESHOLD);
    wait_requests!(backend.canary_requests(); reaches next_canary, "failing active canary requests");
    case.wait_for_unroutable(STATUS_TIMEOUT).await;

    let failed_canaries = backend.canary_requests();
    backend.set_completion_tokens(1);
    case.wait_for_routing(BRINGUP_TIMEOUT).await;
    case.assert_serves("canary-recovered").await;
    assert!(
        backend.canary_requests() > failed_canaries,
        "recovery should require a later successful canary"
    );
    case.shutdown().await;
}

async fn exercise_server_health_rtt_lifecycle(case: TunnelTestCase, stargates: StargateTopology) {
    let case = LifecycleCase::start(
        case,
        stargates,
        Some("server-health"),
        disabled_bringup(),
        |backend| backend.set_health_ok(false),
    )
    .await;
    let backend = &case.registration.backend;

    wait_requests!(backend.health_requests(); reaches 1, "server health checks");
    case.wait_for_unroutable(STATUS_TIMEOUT).await;

    backend.set_health_ok(true);
    case.wait_for_routing(STATUS_TIMEOUT).await;
    case.assert_serves("server-health-initial").await;

    let healthy_checks = backend.health_requests();
    let next_health_check = healthy_checks + 1;
    backend.set_health_ok(false);
    wait_requests!(backend.health_requests(); reaches next_health_check, "post-active health checks");
    case.wait_for_unroutable(STATUS_TIMEOUT).await;

    backend.set_health_ok(true);
    case.wait_for_routing(STATUS_TIMEOUT).await;
    case.assert_serves("server-health-recovered").await;
    case.shutdown().await;
}

async fn exercise_direct_late_tunnel(stargates: StargateTopology) {
    init_crypto();

    let topology = start_lifecycle_topology(DIRECT, stargates).await;
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
        &[(model.as_str(), ACTIVE)],
        false,
    );
    let raw_registrations = RawRegistrations::start(&topology, update.clone()).await;

    topology.wait_unroutable(&model).await;

    let tunnel = start_quic_http_tunnel(QuicHttpTunnelConfig::new(
        quic_addr,
        format!("http://{}", backend.addr),
    ))
    .await
    .expect("late direct QUIC tunnel failed to start");
    raw_registrations.send(update).await;
    topology.wait_routing(&model).await;
    topology
        .assert_serving(&model, &backend_id, "direct-late")
        .await;

    raw_registrations.close().await;
    tunnel.shutdown().await;
    topology.shutdown().await;
}

async fn exercise_reverse_tunnel_gate(stargates: StargateTopology) {
    init_crypto();

    let topology = start_lifecycle_topology(REVERSE, stargates).await;
    let model = format!("lifecycle-reverse-gate-{}-model", stargates.label());
    let backend_id = format!("lifecycle-reverse-gate-{}-backend", stargates.label());
    let backend = start_lifecycle_dummy_backend(&model).await;
    let upstream_http_base_url = format!("http://{}", backend.addr);
    let update = raw_registration(
        &backend_id,
        &format!("lifecycle-reverse-gate-{}-cluster", stargates.label()),
        &upstream_http_base_url,
        &[(model.as_str(), ACTIVE)],
        true,
    );
    let raw_registrations = RawRegistrations::start(&topology, update.clone()).await;

    topology.wait_unroutable(&model).await;

    let tunnels = start_reverse_tunnels(&topology, &backend_id, &upstream_http_base_url).await;
    raw_registrations.send(update).await;
    topology.wait_routing(&model).await;
    topology
        .assert_serving(&model, &backend_id, "reverse-gate")
        .await;

    raw_registrations.close().await;
    for tunnel in tunnels {
        tunnel.shutdown().await;
    }
    topology.shutdown().await;
}

async fn exercise_identity_mutation_closes_route(case: TunnelTestCase) {
    init_crypto();

    let topology = start_lifecycle_topology(case, StargateTopology::Single).await;
    let model = format!("lifecycle-identity-{}-model", case.direction_label());
    let backend_id = format!("lifecycle-identity-{}-backend", case.direction_label());
    let backend = start_lifecycle_dummy_backend(&model).await;
    let mut path = RawBackendPath::prepare(case, &backend_id, backend.addr).await;
    let raw_registrations = path
        .register(
            &topology,
            "lifecycle-identity-cluster",
            &[(model.as_str(), ACTIVE)],
        )
        .await;
    topology.wait_routing(&model).await;

    let mutated = path.registration(
        "lifecycle-identity-mutated-cluster",
        &[(model.as_str(), ACTIVE)],
    );
    raw_registrations.send(mutated).await;
    topology.wait_unroutable(&model).await;

    raw_registrations
        .close_expecting_error(tonic::Code::InvalidArgument, "cluster_id changed")
        .await;
    path.shutdown().await;
    topology.shutdown().await;
}

async fn exercise_model_discovery_cluster_lifecycle(case: TunnelTestCase) {
    init_crypto();

    let topology = start_lifecycle_topology(case, StargateTopology::Single).await;
    let mut registrations = Vec::new();
    for (name, clusters) in [
        ("one-cluster", ["lifecycle-list-one-cluster"; 2]),
        (
            "two-cluster",
            [
                "lifecycle-list-two-cluster-a",
                "lifecycle-list-two-cluster-b",
            ],
        ),
    ] {
        registrations
            .extend(exercise_model_discovery_scenario(&topology, case, name, clusters).await);
    }
    for registration in &mut registrations {
        registration.stop();
    }
    topology.shutdown().await;
}

async fn exercise_model_discovery_scenario(
    topology: &LifecycleTopology,
    case: TunnelTestCase,
    name: &str,
    clusters: [&str; 2],
) -> [LifecycleRegistration; 2] {
    let stem = format!("lifecycle-list-{name}-{}", case.direction_label());
    let model = format!("{stem}-model");
    let backend_ids = [format!("{stem}-a"), format!("{stem}-b")];
    let mut registrations = Vec::with_capacity(2);
    for (backend_id, cluster_id) in backend_ids.iter().zip(clusters) {
        registrations.push(
            LifecycleBackendOptions::serving(backend_id, &[&model], &model)
                .await
                .in_cluster(cluster_id)
                .register(topology, case)
                .await,
        );
    }

    topology.wait_routing(&model).await;
    wait_active_count(topology, &model, 2).await;
    wait_models(topology, &[&model], &[&model], Duration::from_secs(5)).await;

    registrations[0].set_status(INACTIVE);
    wait_active_count(topology, &model, 1).await;
    wait_serving(
        topology,
        &model,
        &backend_ids[1],
        &format!("{name}-backend-b"),
    )
    .await;
    wait_models(topology, &[&model], &[&model], Duration::from_secs(5)).await;

    registrations[1].set_status(INACTIVE);
    topology.wait_unroutable(&model).await;
    wait_active_count(topology, &model, 0).await;
    wait_models(topology, &[&model], &[], Duration::from_secs(5)).await;

    let backend_b = registrations.pop().expect("backend B registration");
    let backend_a = registrations.pop().expect("backend A registration");
    [backend_a, backend_b]
}

async fn exercise_shared_cluster_asymmetric_models(case: TunnelTestCase) {
    init_crypto();

    let topology = start_lifecycle_topology(case, StargateTopology::Single).await;
    let alpha = format!("lifecycle-asymmetric-{}-alpha", case.direction_label());
    let shared = format!("lifecycle-asymmetric-{}-shared", case.direction_label());
    let beta = format!("lifecycle-asymmetric-{}-beta", case.direction_label());
    let backend_a_id = format!("lifecycle-asymmetric-{}-backend-a", case.direction_label());
    let backend_b_id = format!("lifecycle-asymmetric-{}-backend-b", case.direction_label());
    let mut backend_a =
        LifecycleBackendOptions::serving(&backend_a_id, &[alpha.as_str(), shared.as_str()], &alpha)
            .await
            .in_cluster("lifecycle-asymmetric-cluster")
            .register(&topology, case)
            .await;
    let mut backend_b =
        LifecycleBackendOptions::serving(&backend_b_id, &[shared.as_str(), beta.as_str()], &beta)
            .await
            .in_cluster("lifecycle-asymmetric-cluster")
            .register(&topology, case)
            .await;

    backend_a.set_model_stats(&alpha, 11.0, 3);
    backend_a.set_model_stats(&shared, 17.0, 5);
    backend_b.set_model_stats(&shared, 23.0, 7);
    backend_b.set_model_stats(&beta, 31.0, 11);

    for model in [&alpha, &shared, &beta] {
        topology.wait_routing(model).await;
    }
    wait_models(
        &topology,
        &[],
        &[alpha.as_str(), beta.as_str(), shared.as_str()],
        STATUS_TIMEOUT,
    )
    .await;
    wait_cluster_stats(&topology, &alpha, 1, 11.0, 3, STATUS_TIMEOUT).await;
    wait_cluster_stats(&topology, &shared, 2, 40.0, 12, STATUS_TIMEOUT).await;
    wait_cluster_stats(&topology, &beta, 1, 31.0, 11, STATUS_TIMEOUT).await;

    topology
        .assert_serving(&alpha, &backend_a_id, "asymmetric-alpha")
        .await;
    topology
        .assert_serving(&beta, &backend_b_id, "asymmetric-beta")
        .await;
    let shared_backends = wait_for_inference_server_ids(
        topology.http_addrs[0],
        &shared,
        "asymmetric-shared",
        2,
        STATUS_TIMEOUT,
        Duration::from_millis(50),
    )
    .await;
    assert_eq!(
        shared_backends,
        HashSet::from([backend_a_id.clone(), backend_b_id.clone()])
    );

    backend_a.stop();
    topology.wait_unroutable(&alpha).await;
    wait_serving(
        &topology,
        &shared,
        &backend_b_id,
        "asymmetric-shared-after-a-stop",
    )
    .await;
    wait_serving(
        &topology,
        &beta,
        &backend_b_id,
        "asymmetric-beta-after-a-stop",
    )
    .await;
    wait_models(
        &topology,
        &[],
        &[beta.as_str(), shared.as_str()],
        STATUS_TIMEOUT,
    )
    .await;
    wait_cluster_stats(&topology, &shared, 1, 23.0, 7, STATUS_TIMEOUT).await;
    wait_cluster_stats(&topology, &beta, 1, 31.0, 11, STATUS_TIMEOUT).await;

    backend_b.stop();
    topology.shutdown().await;
}

async fn exercise_direct_protocol_connection_lifecycle(protocol: TunnelTransportProtocol) {
    init_crypto();

    let case = TunnelTestCase::direct(protocol);
    let topology = start_lifecycle_topology_for_case(case, StargateTopology::Single, None).await;
    let model = format!("lifecycle-direct-{}-generation", case.protocol_label());
    let old_backend_id = format!("lifecycle-direct-{}-old", case.protocol_label());
    let new_backend_id = format!("lifecycle-direct-{}-new", case.protocol_label());
    let mut old_registration = LifecycleBackendOptions::serving(&old_backend_id, &[&model], &model)
        .await
        .register(&topology, DIRECT)
        .await;

    topology.wait_routing(&model).await;
    wait_models(&topology, &[], &[&model], STATUS_TIMEOUT).await;
    old_registration.shutdown_tunnel().await;
    topology.wait_unroutable(&model).await;
    wait_models(&topology, &[], &[], STATUS_TIMEOUT).await;
    old_registration.stop();

    let mut new_registration = LifecycleBackendOptions::serving(&new_backend_id, &[&model], &model)
        .await
        .register(&topology, DIRECT)
        .await;
    topology.wait_routing(&model).await;
    wait_models(&topology, &[], &[&model], STATUS_TIMEOUT).await;
    wait_backends(&topology, &model, &[&new_backend_id]).await;

    new_registration.stop();
    topology.shutdown().await;
}

async fn exercise_raw_shared_cluster_asymmetric_models(case: TunnelTestCase) {
    init_crypto();

    let topology = start_lifecycle_topology(case, StargateTopology::Single).await;
    let alpha = format!("lifecycle-raw-asymmetric-{}-alpha", case.direction_label());
    let shared = format!("lifecycle-raw-asymmetric-{}-shared", case.direction_label());
    let beta = format!("lifecycle-raw-asymmetric-{}-beta", case.direction_label());
    let backend_a_id = format!(
        "lifecycle-raw-asymmetric-{}-backend-a",
        case.direction_label()
    );
    let backend_b_id = format!(
        "lifecycle-raw-asymmetric-{}-backend-b",
        case.direction_label()
    );
    let backend_a = start_lifecycle_dummy_backend(&alpha).await;
    let backend_b = start_lifecycle_dummy_backend(&beta).await;
    let mut path_a = RawBackendPath::prepare(case, &backend_a_id, backend_a.addr).await;
    let mut path_b = RawBackendPath::prepare(case, &backend_b_id, backend_b.addr).await;
    let registrations_a = path_a
        .register(
            &topology,
            "lifecycle-raw-asymmetric-cluster",
            &[(alpha.as_str(), ACTIVE), (shared.as_str(), ACTIVE)],
        )
        .await;
    let registrations_b = path_b
        .register(
            &topology,
            "lifecycle-raw-asymmetric-cluster",
            &[(shared.as_str(), ACTIVE), (beta.as_str(), ACTIVE)],
        )
        .await;

    for model in [&alpha, &shared, &beta] {
        topology.wait_routing(model).await;
    }
    wait_backends(&topology, &alpha, &[&backend_a_id]).await;
    wait_backends(&topology, &shared, &[&backend_a_id, &backend_b_id]).await;
    wait_backends(&topology, &beta, &[&backend_b_id]).await;

    registrations_a
        .send(path_a.registration(
            "lifecycle-raw-asymmetric-cluster",
            &[(alpha.as_str(), INACTIVE), (shared.as_str(), ACTIVE)],
        ))
        .await;
    topology.wait_unroutable(&alpha).await;
    wait_backends(&topology, &shared, &[&backend_a_id, &backend_b_id]).await;
    wait_backends(&topology, &beta, &[&backend_b_id]).await;
    wait_models(
        &topology,
        &[],
        &[beta.as_str(), shared.as_str()],
        STATUS_TIMEOUT,
    )
    .await;

    registrations_a
        .send(path_a.registration(
            "lifecycle-raw-asymmetric-cluster",
            &[(shared.as_str(), ACTIVE)],
        ))
        .await;
    topology.wait_unroutable(&alpha).await;
    wait_backends(&topology, &shared, &[&backend_a_id, &backend_b_id]).await;
    wait_backends(&topology, &beta, &[&backend_b_id]).await;

    registrations_a.close().await;
    wait_backends(&topology, &shared, &[&backend_b_id]).await;
    wait_backends(&topology, &beta, &[&backend_b_id]).await;

    registrations_b.close().await;
    path_a.shutdown().await;
    path_b.shutdown().await;
    topology.shutdown().await;
}

async fn exercise_routing_key_lifecycle(case: TunnelTestCase) {
    init_crypto();

    let auth = Arc::new(TokenMapAuthenticator::new([
        ("lifecycle-rk-token-a", "lifecycle-rk-a"),
        ("lifecycle-rk-token-b", "lifecycle-rk-b"),
    ]));
    let topology =
        start_lifecycle_topology_for_case(case, StargateTopology::Single, Some(auth)).await;
    let model = format!("lifecycle-rk-{}-model", case.direction_label());
    let mut tenants = Vec::new();
    for suffix in ["a", "b"] {
        let backend_id = format!("lifecycle-rk-{}-backend-{suffix}", case.direction_label());
        tenants.push(
            LifecycleBackendOptions::serving(&backend_id, &[&model], &model)
                .await
                .authenticated(format!("lifecycle-rk-token-{suffix}"))
                .register(&topology, case)
                .await,
        );
    }

    wait_for_routing_key(&topology, "lifecycle-rk-a", &model).await;
    wait_for_routing_key(&topology, "lifecycle-rk-b", &model).await;
    tenants[0].set_status(INACTIVE);
    wait_for_unroutable_with_rk(&topology, "lifecycle-rk-a", &model).await;
    wait_for_routing_key(&topology, "lifecycle-rk-b", &model).await;

    for tenant in &mut tenants {
        tenant.stop();
    }
    topology.shutdown().await;
}

async fn exercise_multi_model_raw_lifecycle(case: TunnelTestCase) {
    init_crypto();

    let topology = start_lifecycle_topology(case, StargateTopology::Single).await;
    let model_a = format!("lifecycle-multi-{}-a", case.direction_label());
    let model_b = format!("lifecycle-multi-{}-b", case.direction_label());
    let backend_id = format!("lifecycle-multi-{}-backend", case.direction_label());
    let backend = start_lifecycle_dummy_backend(&model_a).await;
    let mut path = RawBackendPath::prepare(case, &backend_id, backend.addr).await;
    let raw_registrations = path
        .register(
            &topology,
            "lifecycle-multi-cluster",
            &[(model_a.as_str(), ACTIVE), (model_b.as_str(), ACTIVE)],
        )
        .await;
    topology.wait_routing(&model_a).await;
    topology.wait_routing(&model_b).await;

    raw_registrations
        .send(path.registration(
            "lifecycle-multi-cluster",
            &[(model_a.as_str(), INACTIVE), (model_b.as_str(), ACTIVE)],
        ))
        .await;
    topology.wait_unroutable(&model_a).await;
    topology.wait_routing(&model_b).await;

    raw_registrations.close().await;
    path.shutdown().await;
    topology.shutdown().await;
}

async fn exercise_status_lifecycle(case: TunnelTestCase, stargates: StargateTopology) {
    let mut case = LifecycleCase::start(case, stargates, None, disabled_bringup(), |_| {}).await;
    case.wait_for_routing(STATUS_TIMEOUT).await;
    case.assert_serves("status-initial").await;
    case.registration.set_status(INACTIVE);
    case.wait_for_unroutable(STATUS_TIMEOUT).await;
    case.registration.set_status(ACTIVE);
    case.wait_for_routing(STATUS_TIMEOUT).await;
    case.assert_serves("status-recovered").await;
    case.registration.stop();
    case.wait_for_unroutable(STATUS_TIMEOUT).await;
    case.topology.shutdown().await;
}

async fn start_lifecycle_topology(
    case: TunnelTestCase,
    stargates: StargateTopology,
) -> LifecycleTopology {
    start_lifecycle_topology_for_case(case, stargates, None).await
}

async fn start_lifecycle_topology_for_case(
    case: TunnelTestCase,
    stargates: StargateTopology,
    authenticator: Option<Arc<dyn WorkerAuthenticator>>,
) -> LifecycleTopology {
    let node_count = match stargates {
        StargateTopology::Single => 1,
        StargateTopology::Pair => 2,
    };
    let peers =
        matches!(stargates, StargateTopology::Pair).then(|| Arc::new(Mutex::new(Vec::new())));
    let mut nodes = Vec::with_capacity(node_count);
    for index in 0..node_count {
        let id = match stargates {
            StargateTopology::Single => format!("lifecycle-{}-single", case.direction_label()),
            StargateTopology::Pair => {
                format!("lifecycle-{}-pair-{}", case.direction_label(), index + 1)
            }
        };
        nodes.push(make_lifecycle_node(
            &id,
            peers.clone(),
            case.reverse_tunnel(),
            authenticator.clone(),
            case.protocol,
        ));
    }

    let grpc_addrs = nodes.iter().map(|node| node.grpc_addr).collect();
    let http_addrs = nodes.iter().map(|node| node.http_addr).collect();
    let model_discovery_addrs = nodes.iter().map(|node| node.model_discovery_addr).collect();
    let reverse_tunnel_targets = nodes
        .iter_mut()
        .filter_map(|node| node.reverse_tunnel_target.take())
        .collect();
    let mut handles = Vec::with_capacity(nodes.len());
    for node in nodes {
        handles.push(
            node.runtime
                .start()
                .await
                .expect("lifecycle stargate failed"),
        );
    }
    LifecycleTopology {
        grpc_addrs,
        http_addrs,
        model_discovery_addrs,
        reverse_tunnel_targets,
        tunnel_protocol: case.protocol,
        handles,
    }
}

struct LifecycleNode {
    grpc_addr: SocketAddr,
    model_discovery_addr: SocketAddr,
    http_addr: SocketAddr,
    reverse_tunnel_target: Option<String>,
    runtime: StargateRuntime,
}

fn make_lifecycle_node(
    id: &str,
    peers: Option<Arc<Mutex<Vec<StargateInfo>>>>,
    reverse: bool,
    authenticator: Option<Arc<dyn WorkerAuthenticator>>,
    tunnel_protocol: TunnelTransportProtocol,
) -> LifecycleNode {
    let (grpc_addr, grpc_listener) = bind_ephemeral();
    let (model_discovery_addr, model_discovery_listener) = bind_ephemeral();
    let (http_addr, http_listener) = bind_ephemeral();
    let mut config = base_config(id, grpc_addr, http_addr);
    config.model_discovery_listen_addr = model_discovery_addr;
    config.dns_poll_interval = Duration::from_secs(1);
    config.proxy_transport.quic.tunnel_protocol = tunnel_protocol;
    if let Some(authenticator) = authenticator {
        config.authenticator = authenticator;
    }
    let discovery: Box<dyn stargate::discovery::Discovery> = match peers {
        Some(peers) => Box::new(SharedDiscovery::new(id, grpc_addr, http_addr, peers)),
        None => Box::new(SelfDiscovery::new(id, grpc_addr, http_addr)),
    };
    let mut reverse_tunnel_target = None;
    let reverse_tunnel = reverse.then(|| {
        let (_, reverse_socket) = bind_ephemeral_udp();
        let mut reverse_tunnel = ReverseTunnelConfig::from_bound_socket(
            reverse_socket,
            "localhost".to_string(),
            None,
            Duration::from_secs(10),
        );
        reverse_tunnel.connect_timeout = Duration::from_millis(250);
        reverse_tunnel_target = Some(format!("localhost:{}", reverse_tunnel.listen_addr().port()));
        reverse_tunnel
    });
    let listeners = BoundStargateListeners::from_prebound(
        &config,
        grpc_listener,
        model_discovery_listener,
        http_listener,
        None,
    )
    .expect("lifecycle runtime listeners must match the configured addresses");
    LifecycleNode {
        grpc_addr,
        model_discovery_addr,
        http_addr,
        reverse_tunnel_target,
        runtime: StargateRuntime::new(config, discovery, listeners, reverse_tunnel),
    }
}

impl LifecycleBackendOptions<'_> {
    async fn register(
        self,
        topology: &LifecycleTopology,
        case: TunnelTestCase,
    ) -> LifecycleRegistration {
        let LifecycleBackendOptions {
            backend_id,
            cluster_id,
            models,
            backend,
            bringup,
            auth_token,
        } = self;

        let upstream_http_base_url = format!("http://{}", backend.addr);
        let (inference_server_url, tunnel) = if case.reverse_tunnel() {
            (upstream_http_base_url.clone(), None)
        } else {
            let mut tunnel_config = QuicHttpTunnelConfig::new(
                "127.0.0.1:0".parse().unwrap(),
                upstream_http_base_url.clone(),
            );
            tunnel_config.tunnel_protocol = topology.tunnel_protocol;
            let tunnel = start_quic_http_tunnel(tunnel_config)
                .await
                .expect("direct QUIC tunnel failed to start");
            (format!("quic://{}", tunnel.listen_addr()), Some(tunnel))
        };

        let model_ids = models
            .iter()
            .map(|model| (*model).to_string())
            .collect::<Vec<_>>();
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            ACTIVE,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats_collector =
            pylon_lib::start_stats_collector(stats_config, observations, runtime_state.clone());
        let model_lifecycle = start_model_lifecycle(
            ModelLifecycleConfig {
                upstream_http_base_url: upstream_http_base_url.clone(),
                source: ModelSource::Static(model_ids.into_iter().collect::<BTreeSet<_>>()),
                initialization: ModelInitialization::ConfiguredInputTps {
                    input_tps: 1.0,
                    pin: true,
                },
                bringup,
            },
            runtime_state.clone(),
            &stats_collector,
            None,
        )
        .await
        .expect("initial model lifecycle failed");
        let mut reg_client = InferenceServerRegistrationClient::default();
        let seeds = vec![topology.grpc_addrs[0].to_string()];
        let mut config = if case.reverse_tunnel() {
            reverse_registration_config(
                seeds,
                backend_id,
                upstream_http_base_url,
                runtime_state.clone(),
            )
        } else {
            direct_registration_config(
                seeds,
                backend_id,
                inference_server_url,
                upstream_http_base_url,
                runtime_state.clone(),
            )
        };
        config.cluster_id = cluster_id.to_string();
        config.auth_token_provider = auth_token.map(AuthTokenProvider::Static).map(Arc::new);
        config.tunnel_protocol = topology.tunnel_protocol;
        reg_client.start(config).expect("registration failed");

        LifecycleRegistration {
            backend,
            reg_client,
            runtime_state,
            _model_lifecycle: model_lifecycle,
            tunnel,
        }
    }
}

fn disabled_bringup() -> BringupConfig {
    // The status matrix drives lifecycle explicitly through registration, so
    // ongoing bringup is disabled to isolate the transport/topology signal.
    BringupConfig {
        enabled: false,
        ..BringupConfig::default()
    }
}

fn active_canary_bringup() -> BringupConfig {
    // This test focuses on active canary demotion and recovery after startup.
    BringupConfig {
        enabled: true,
        active_canary_interval: Duration::from_millis(50),
        canary_timeout: Duration::from_millis(250),
        canary_max_generation_threshold: CANARY_FAILURE_TOKEN_THRESHOLD,
    }
}

async fn start_raw_registration(
    grpc_addr: SocketAddr,
    initial: InferenceServerRegistration,
) -> RawRegistration {
    let channel = Channel::from_shared(format!("http://{grpc_addr}"))
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

fn raw_registration(
    backend_id: &str,
    cluster_id: &str,
    inference_server_url: &str,
    models: &[(&str, InferenceServerStatus)],
    reverse_tunnel: bool,
) -> InferenceServerRegistration {
    let models = models
        .iter()
        .map(|(model, status)| {
            (
                (*model).to_string(),
                InferenceServerModelRegistration {
                    stats: Some(ModelStats {
                        last_mean_input_tps: 100.0,
                        max_output_tps: 100.0,
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
        config.tunnel_protocol = topology.tunnel_protocol;
        tunnels.push(
            start_reverse_quic_tunnel(config)
                .await
                .expect("reverse QUIC tunnel failed to start"),
        );
    }
    tunnels
}

async fn wait_for_unroutable_with_rk(topology: &LifecycleTopology, routing_key: &str, model: &str) {
    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{}/v1/chat/completions", topology.http_addrs[0]);
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    wait_until(
        &format!("model '{model}' with routing key '{routing_key}' unroutable"),
        STATUS_TIMEOUT,
        Duration::from_millis(100),
        || {
            let http_client = http_client.clone();
            let stargate_url = stargate_url.clone();
            let body = body.clone();
            async move {
                match with_proxy_headers(
                    http_client.post(stargate_url),
                    model,
                    "req-wait-unroutable-rk",
                )
                .header("x-routing-key", routing_key)
                .header("content-type", "application/json")
                .json(&body)
                .send()
                .await
                {
                    Ok(response) if matches!(response.status().as_u16(), 404 | 502 | 503) => Ok(()),
                    Err(_) => Ok(()),
                    Ok(response) => Err(format!("status {}", response.status())),
                }
            }
        },
    )
    .await;
}

async fn wait_for_routing_key(topology: &LifecycleTopology, routing_key: &str, model: &str) {
    wait_for_routing_with_rk(
        topology.http_addrs[0],
        Some(routing_key),
        model,
        STATUS_TIMEOUT,
    )
    .await;
}

async fn wait_models(
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
) {
    let mut expected_ids = expected_ids.to_vec();
    expected_ids.sort_unstable();
    let client = StargateModelDiscoveryClient::connect(format!("http://{model_discovery_addr}"))
        .await
        .expect("connect to model discovery");
    let model_ids = model_ids
        .iter()
        .map(|model| (*model).to_string())
        .collect::<Vec<_>>();
    wait_until(
        "ListModels expected ids",
        timeout,
        Duration::from_millis(200),
        || {
            let mut client = client.clone();
            let model_ids = model_ids.clone();
            let expected_ids = expected_ids.clone();
            async move {
                let mut actual_ids = client
                    .list_models(ListModelsRequest {
                        routing_key: routing_key.map(ToOwned::to_owned),
                        model_ids,
                    })
                    .await
                    .expect("ListModels RPC failed")
                    .into_inner()
                    .model_ids;
                actual_ids.sort_unstable();
                (actual_ids == expected_ids).then_some(()).ok_or_else(|| {
                    format!("returned ids {actual_ids:?}; expected {expected_ids:?}")
                })
            }
        },
    )
    .await
}

async fn wait_serving(
    topology: &LifecycleTopology,
    model: &str,
    expected_backend: &str,
    request_id_prefix: &str,
) {
    wait_until(
        &format!("model '{model}' routed only to backend '{expected_backend}'"),
        STATUS_TIMEOUT,
        Duration::from_millis(100),
        || check_serving(topology, model, expected_backend, request_id_prefix),
    )
    .await;
}

async fn check_serving(
    topology: &LifecycleTopology,
    model: &str,
    expected_backend: &str,
    request_id_prefix: &str,
) -> Result<(), String> {
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
        .map_err(|error| format!("proxy request failed: {error}"))?;
        let status = resp.status();
        let backend = resp
            .headers()
            .get("x-inference-server-id")
            .and_then(|value| value.to_str().ok())
            .map(str::to_string);
        let text = resp
            .text()
            .await
            .map_err(|error| format!("read proxy body: {error}"))?;
        if status != StatusCode::OK {
            return Err(format!("proxy returned {status}: {text}"));
        }
        if backend.as_deref() != Some(expected_backend) {
            return Err(format!("proxy selected backend {backend:?}"));
        }
        let events = parse_sse_events(&text).map_err(|error| format!("invalid SSE: {error}"))?;
        if events
            .last()
            .is_none_or(|event| event.data.trim() != "[DONE]")
        {
            return Err(format!(
                "SSE stream did not terminate with [DONE]: {events:#?}"
            ));
        }
    }
    Ok(())
}

async fn wait_active_count(topology: &LifecycleTopology, model: &str, count: usize) {
    let expected =
        format!(r#"stargate_active_inference_servers{{model="{model}",routing_key=""}} {count}"#);
    for handle in &topology.handles {
        let registry = handle.metrics().registry();
        wait_until(
            &format!("metrics containing {expected:?}"),
            Duration::from_secs(5),
            Duration::from_millis(100),
            || async {
                let encoder = TextEncoder::new();
                let mut buffer = Vec::new();
                encoder
                    .encode(&registry.gather(), &mut buffer)
                    .expect("encode metrics");
                let metrics = String::from_utf8(buffer).expect("metrics must be utf8");
                metrics
                    .lines()
                    .any(|line| line == expected)
                    .then_some(())
                    .ok_or(metrics)
            },
        )
        .await;
    }
}

async fn wait_cluster_stats(
    topology: &LifecycleTopology,
    model: &str,
    active_backend_count: usize,
    last_mean_input_tps: f64,
    queued_input_size: u64,
    timeout: Duration,
) {
    for handle in &topology.handles {
        let state = handle.state();
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: model.to_string(),
        };
        wait_until(
            &format!("cluster stats for model '{model}'"),
            timeout,
            Duration::from_millis(50),
            || {
                let state = state.clone();
                let target = target.clone();
                async move {
                    let candidates = state.cluster_candidates_for_target(&target).await;
                    let Some(candidate) = candidates.first() else {
                        return Err("no cluster candidate".to_string());
                    };
                    if candidates.len() != 1
                        || candidate.active_backend_count != active_backend_count
                        || candidate.stats.last_mean_input_tps != last_mean_input_tps
                        || candidate.stats.queued_input_size != queued_input_size
                    {
                        return Err(format!("unexpected cluster candidates: {candidates:?}"));
                    }
                    Ok(())
                }
            },
        )
        .await;
    }
}

async fn wait_backends(topology: &LifecycleTopology, model: &str, expected_backend_ids: &[&str]) {
    let expected_backend_ids = expected_backend_ids
        .iter()
        .map(|id| (*id).to_string())
        .collect::<HashSet<_>>();
    for handle in &topology.handles {
        let state = handle.state();
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: model.to_string(),
        };
        wait_until(
            &format!("backend ids for model '{model}'"),
            STATUS_TIMEOUT,
            Duration::from_millis(50),
            || {
                let state = state.clone();
                let target = target.clone();
                let expected_backend_ids = expected_backend_ids.clone();
                async move {
                    let actual = state
                        .candidates_for_target(&target)
                        .await
                        .into_iter()
                        .map(|candidate| candidate.inference_server_id)
                        .collect::<HashSet<_>>();
                    if actual == expected_backend_ids {
                        Ok(())
                    } else {
                        Err(format!(
                            "backend ids {actual:?}; expected {expected_backend_ids:?}"
                        ))
                    }
                }
            },
        )
        .await;
    }
}

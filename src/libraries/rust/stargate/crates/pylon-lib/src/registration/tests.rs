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
use std::pin::Pin;
use std::process::Command;
use std::sync::Arc;
use std::time::Duration;

use futures::Stream;
use rcgen::{BasicConstraints, CertificateParams, DnType, IsCa, KeyPair};
use stargate_proto::pb::stargate_control_plane_server::{
    StargateControlPlane, StargateControlPlaneServer,
};
use stargate_proto::pb::{
    InferenceServerAck, InferenceServerModelRegistration, InferenceServerRegistration,
    InferenceServerStatus, ModelStats, StargateInfo, WatchStargatesRequest, WatchStargatesResponse,
};
use stargate_protocol::TunnelTransportProtocol;
use stargate_runtime::OwnedTask;
use tokio::net::TcpListener;
use tokio::sync::{mpsc, watch};
use tokio_stream::StreamExt;
use tokio_util::sync::CancellationToken;
use tonic::transport::{Identity, Server, ServerTlsConfig};
use tonic::{Request, Response, Status};
use tower::util::MapRequestLayer;

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
const TEST_ROUTER_AUTHORITY: &str = "router-0.router-headless.example.invalid:50071";
const DEFAULT_ROOT_TEST_DIAL_URL_ENV: &str = "PYLON_DEFAULT_ROOT_TEST_DIAL_URL";
const CUSTOM_ROOT_TEST_CA_PATH_ENV: &str = "PYLON_CUSTOM_ROOT_TEST_CA_PATH";

type TestWatchStream =
    Pin<Box<dyn Stream<Item = Result<WatchStargatesResponse, Status>> + Send + 'static>>;
type TestRegistrationStream =
    Pin<Box<dyn Stream<Item = Result<InferenceServerAck, Status>> + Send + 'static>>;

struct TestCertificateAuthority {
    cert: rcgen::Certificate,
    key: KeyPair,
}

impl TestCertificateAuthority {
    fn new(common_name: &str) -> Self {
        let mut params = CertificateParams::default();
        params.is_ca = IsCa::Ca(BasicConstraints::Unconstrained);
        params
            .distinguished_name
            .push(DnType::CommonName, common_name);
        let key = KeyPair::generate().expect("test CA key should generate");
        let cert = params
            .self_signed(&key)
            .expect("test CA certificate should generate");
        Self { cert, key }
    }

    fn pem(&self) -> Vec<u8> {
        self.cert.pem().into_bytes()
    }

    fn issue_server_identity(&self, dns_name: &str) -> Identity {
        let params = CertificateParams::new(vec![dns_name.to_string()])
            .expect("test server certificate params should build");
        let key = KeyPair::generate().expect("test server key should generate");
        let cert = params
            .signed_by(&key, &self.cert, &self.key)
            .expect("test server certificate should generate");
        Identity::from_pem(cert.pem(), key.serialize_pem())
    }
}

#[derive(Clone)]
struct TestTlsControlPlaneService {
    dial_url: String,
    watch_authorities: mpsc::UnboundedSender<String>,
    registration_authorities: mpsc::UnboundedSender<String>,
    registrations: mpsc::UnboundedSender<InferenceServerRegistration>,
}

#[tonic::async_trait]
impl StargateControlPlane for TestTlsControlPlaneService {
    type WatchStargatesStream = TestWatchStream;
    type RegisterInferenceServerStream = TestRegistrationStream;

    async fn watch_stargates(
        &self,
        request: Request<WatchStargatesRequest>,
    ) -> Result<Response<Self::WatchStargatesStream>, Status> {
        let _ = self.watch_authorities.send(
            request
                .extensions()
                .get::<http::uri::Authority>()
                .map(ToString::to_string)
                .unwrap_or_default(),
        );
        let response = WatchStargatesResponse {
            stargates: vec![stargate_info(
                "stargate-0",
                TEST_ROUTER_AUTHORITY,
                &self.dial_url,
            )],
            watch_stargate_urls: Vec::new(),
        };
        Ok(Response::new(Box::pin(
            tokio_stream::once(Ok(response)).chain(tokio_stream::pending()),
        )))
    }

    async fn register_inference_server(
        &self,
        request: Request<tonic::Streaming<InferenceServerRegistration>>,
    ) -> Result<Response<Self::RegisterInferenceServerStream>, Status> {
        let _ = self.registration_authorities.send(
            request
                .extensions()
                .get::<http::uri::Authority>()
                .map(ToString::to_string)
                .unwrap_or_default(),
        );
        let mut stream = request.into_inner();
        let registrations = self.registrations.clone();
        tokio::spawn(async move {
            if let Ok(Some(registration)) = stream.message().await {
                let _ = registrations.send(registration);
            }
        });
        Ok(Response::new(Box::pin(
            tokio_stream::once(Ok(InferenceServerAck::default())).chain(tokio_stream::pending()),
        )))
    }
}

struct TestTlsControlPlane {
    dial_url: String,
    watch_authorities: mpsc::UnboundedReceiver<String>,
    registration_authorities: mpsc::UnboundedReceiver<String>,
    registrations: mpsc::UnboundedReceiver<InferenceServerRegistration>,
    task: tokio::task::JoinHandle<()>,
}

impl TestTlsControlPlane {
    async fn spawn(ca: &TestCertificateAuthority, dns_name: &str) -> Self {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("test TLS server should bind");
        let addr = listener
            .local_addr()
            .expect("test TLS server address should resolve");
        let dial_url = format!("https://localhost:{}", addr.port());
        let (watch_authorities, watch_authorities_rx) = mpsc::unbounded_channel();
        let (registration_authorities, registration_authorities_rx) = mpsc::unbounded_channel();
        let (registrations, registrations_rx) = mpsc::unbounded_channel();
        let service = TestTlsControlPlaneService {
            dial_url: dial_url.clone(),
            watch_authorities,
            registration_authorities,
            registrations,
        };
        let identity = ca.issue_server_identity(dns_name);
        let incoming = async_stream::stream! {
            loop {
                yield listener.accept().await.map(|(stream, _)| stream);
            }
        };
        let task = tokio::spawn(async move {
            Server::builder()
                .tls_config(ServerTlsConfig::new().identity(identity))
                .expect("test TLS server config should build")
                .layer(MapRequestLayer::new(|mut request: http::Request<_>| {
                    if let Some(authority) = request.uri().authority().cloned() {
                        request.extensions_mut().insert(authority);
                    }
                    request
                }))
                .add_service(StargateControlPlaneServer::new(service))
                .serve_with_incoming(incoming)
                .await
                .expect("test TLS server should serve");
        });
        Self {
            dial_url,
            watch_authorities: watch_authorities_rx,
            registration_authorities: registration_authorities_rx,
            registrations: registrations_rx,
            task,
        }
    }

    async fn first_registration(&mut self) -> InferenceServerRegistration {
        tokio::time::timeout(TEST_WAIT, self.registrations.recv())
            .await
            .expect("worker registration should not time out")
            .expect("worker registration channel should remain open")
    }

    async fn shutdown(self) {
        self.task.abort();
        let _ = self.task.await;
    }
}

async fn tls_connect_error(server: &TestTlsControlPlane, ca_cert_pem: Option<&[u8]>) -> String {
    let endpoint =
        StargateGrpcEndpoint::new(server.dial_url.clone(), "").expect("test endpoint should build");
    let error = endpoint
        .channel_endpoint(ca_cert_pem)
        .expect("test channel endpoint should configure")
        .connect()
        .await
        .expect_err("TLS connection should fail");
    format!("{error:?}").to_lowercase()
}

fn grpc_endpoint(authority_addr: &str) -> StargateGrpcEndpoint {
    StargateGrpcEndpoint::new(authority_addr.to_string(), "")
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
        grpc_tls_ca_cert_pem: None,
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
fn registration_session_keeps_grpc_and_quic_trust_independent() {
    let mut config = test_registration_config();
    config.tls_cert_pem = Some(b"quic trust".to_vec());
    config.grpc_tls_ca_cert_pem = Some(b"grpc trust".to_vec());

    let session = RegistrationSessionConfig::try_from(config).expect("session should build");

    assert_eq!(session.tls_cert_pem.as_deref(), Some(&b"quic trust"[..]));
    assert_eq!(
        session.grpc_tls_ca_cert_pem.as_deref(),
        Some(&b"grpc trust"[..])
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
    assert!(StargateGrpcEndpoint::new(" ", "https://stargate-grpc-lb:443").is_none());
    assert!(StargateGrpcEndpoint::new("router-a:50071", "stargate-grpc-lb:443").is_none());
    assert_eq!(
        grpc_endpoint_with_dial("router-a:50071", "https://stargate-grpc-lb:443").to_string(),
        "router-a:50071 via https://stargate-grpc-lb:443"
    );
}

#[test]
fn stargate_grpc_endpoint_rejects_custom_ca_for_plaintext_http() {
    let endpoint = grpc_endpoint_with_dial("router-a:50071", "http://stargate-grpc-lb:50071");

    let error = endpoint
        .channel_endpoint(Some(b"private CA contents must not be logged"))
        .err()
        .expect("custom CA with plaintext HTTP should be rejected");

    assert!(
        error
            .to_string()
            .contains("custom CA for stargate gRPC requires an HTTPS dial endpoint"),
        "unexpected error: {error:#}"
    );
}

#[test]
fn stargate_grpc_origin_keeps_dial_scheme_when_authority_scheme_differs() {
    for (dial, authority, expected) in [
        (
            "https://public.example:443",
            "http://router.internal:50071",
            "https://router.internal:50071/",
        ),
        (
            "http://public.example:80",
            "https://router.internal:50071",
            "http://router.internal:50071/",
        ),
    ] {
        let dial_uri = dial.parse().expect("dial URI should parse");
        let origin = grpc_origin_uri(&dial_uri, authority).expect("origin should build");

        assert_eq!(origin.to_string(), expected);
        assert_eq!(origin.scheme_str(), dial_uri.scheme_str());
        assert_eq!(
            origin.authority().unwrap().as_str(),
            "router.internal:50071"
        );
    }
}

#[tokio::test]
async fn custom_grpc_ca_completes_watch_and_registration_with_separate_authority() {
    let ca = TestCertificateAuthority::new("registration-test-ca");
    let mut server = TestTlsControlPlane::spawn(&ca, "localhost").await;
    let mut config = test_registration_config();
    config.seeds = vec![server.dial_url.clone()];
    config.grpc_tls_ca_cert_pem = Some(ca.pem());
    config.min_update_interval = Duration::from_millis(10);
    let mut client = InferenceServerRegistrationClient::default();

    client.start(config).expect("registration should start");
    let registration = server.first_registration().await;
    let watch_authority = tokio::time::timeout(TEST_WAIT, server.watch_authorities.recv())
        .await
        .expect("watch authority should not time out")
        .expect("watch authority channel should remain open");
    let registration_authority =
        tokio::time::timeout(TEST_WAIT, server.registration_authorities.recv())
            .await
            .expect("registration authority should not time out")
            .expect("registration authority channel should remain open");

    assert_eq!(registration.inference_server_id, "inst-a");
    assert_eq!(
        watch_authority,
        server
            .dial_url
            .strip_prefix("https://")
            .expect("test dial URL should use HTTPS")
    );
    assert_eq!(registration_authority, TEST_ROUTER_AUTHORITY);

    client.shutdown().await;
    server.shutdown().await;
}

#[tokio::test]
async fn https_without_custom_ca_uses_configured_native_roots() {
    if let Ok(dial_url) = std::env::var(DEFAULT_ROOT_TEST_DIAL_URL_ENV) {
        let endpoint = StargateGrpcEndpoint::new(dial_url, "")
            .expect("default-root test endpoint should build");
        endpoint
            .channel_endpoint(None)
            .expect("default-root test endpoint should configure")
            .connect()
            .await
            .expect("native root should verify the test server");
        return;
    }

    let ca = TestCertificateAuthority::new("default-roots-test-ca");
    let server = TestTlsControlPlane::spawn(&ca, "localhost").await;
    let ca_file = tempfile::NamedTempFile::new().expect("CA file should be created");
    std::fs::write(ca_file.path(), ca.pem()).expect("CA file should be written");
    let test_binary = std::env::current_exe().expect("test binary path should resolve");
    let dial_url = server.dial_url.clone();
    let ca_path = ca_file.path().to_path_buf();

    let output = tokio::task::spawn_blocking(move || {
        Command::new(test_binary)
            .args([
                "--exact",
                "registration::tests::https_without_custom_ca_uses_configured_native_roots",
                "--nocapture",
            ])
            .env(DEFAULT_ROOT_TEST_DIAL_URL_ENV, dial_url)
            .env("SSL_CERT_FILE", ca_path)
            .env_remove("SSL_CERT_DIR")
            .output()
            .expect("default-root child test should run")
    })
    .await
    .expect("default-root child test should join");

    assert!(
        output.status.success(),
        "default-root child test failed:\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        String::from_utf8_lossy(&output.stdout).contains("1 passed"),
        "default-root child test did not run exactly one test:\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    server.shutdown().await;
}

#[tokio::test]
async fn custom_grpc_ca_augments_configured_native_roots() {
    if let (Ok(dial_url), Ok(custom_ca_path)) = (
        std::env::var(DEFAULT_ROOT_TEST_DIAL_URL_ENV),
        std::env::var(CUSTOM_ROOT_TEST_CA_PATH_ENV),
    ) {
        let custom_ca = std::fs::read(custom_ca_path).expect("custom CA file should be readable");
        let endpoint = StargateGrpcEndpoint::new(dial_url, "")
            .expect("default-root test endpoint should build");
        endpoint
            .channel_endpoint(Some(&custom_ca))
            .expect("augmented-root test endpoint should configure")
            .connect()
            .await
            .expect("native root should remain enabled beside the custom CA");
        return;
    }

    let server_ca = TestCertificateAuthority::new("default-roots-test-ca");
    let custom_ca = TestCertificateAuthority::new("custom-roots-test-ca");
    let server = TestTlsControlPlane::spawn(&server_ca, "localhost").await;
    let server_ca_file = tempfile::NamedTempFile::new().expect("CA file should be created");
    std::fs::write(server_ca_file.path(), server_ca.pem()).expect("CA file should be written");
    let custom_ca_file = tempfile::NamedTempFile::new().expect("CA file should be created");
    std::fs::write(custom_ca_file.path(), custom_ca.pem()).expect("CA file should be written");
    let test_binary = std::env::current_exe().expect("test binary path should resolve");
    let dial_url = server.dial_url.clone();
    let server_ca_path = server_ca_file.path().to_path_buf();
    let custom_ca_path = custom_ca_file.path().to_path_buf();

    let output = tokio::task::spawn_blocking(move || {
        Command::new(test_binary)
            .args([
                "--exact",
                "registration::tests::custom_grpc_ca_augments_configured_native_roots",
                "--nocapture",
            ])
            .env(DEFAULT_ROOT_TEST_DIAL_URL_ENV, dial_url)
            .env(CUSTOM_ROOT_TEST_CA_PATH_ENV, custom_ca_path)
            .env("SSL_CERT_FILE", server_ca_path)
            .env_remove("SSL_CERT_DIR")
            .output()
            .expect("augmented-root child test should run")
    })
    .await
    .expect("augmented-root child test should join");

    assert!(
        output.status.success(),
        "augmented-root child test failed:\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        String::from_utf8_lossy(&output.stdout).contains("1 passed"),
        "augmented-root child test did not run exactly one test:\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    server.shutdown().await;
}

#[tokio::test]
async fn grpc_endpoint_rejects_ca_signed_by_untrusted_issuer() {
    let server_ca = TestCertificateAuthority::new("server-ca");
    let wrong_ca = TestCertificateAuthority::new("wrong-ca");
    let server = TestTlsControlPlane::spawn(&server_ca, "localhost").await;
    let wrong_ca_pem = wrong_ca.pem();

    let error = tls_connect_error(&server, Some(&wrong_ca_pem)).await;

    assert!(
        error.contains("unknownissuer") || error.contains("unknown issuer"),
        "unexpected TLS failure: {error}"
    );
    server.shutdown().await;
}

#[tokio::test]
async fn grpc_endpoint_rejects_leaf_without_external_dial_hostname() {
    let ca = TestCertificateAuthority::new("hostname-test-ca");
    let server = TestTlsControlPlane::spawn(&ca, "not-localhost.invalid").await;
    let ca_pem = ca.pem();

    let error = tls_connect_error(&server, Some(&ca_pem)).await;

    assert!(
        error.contains("notvalidforname") || error.contains("not valid for name"),
        "unexpected TLS failure: {error}"
    );
    server.shutdown().await;
}

#[test]
fn watch_response_separates_registration_routers_from_recursive_seeds() {
    let snapshot = watch_endpoint_snapshot_from_response(
        "seed-a",
        WatchStargatesResponse {
            stargates: vec![stargate_info(
                "stargate-0",
                "stargate-0.region-a:50071",
                "https://lb.region-a:443",
            )],
            watch_stargate_urls: vec!["https://stargate.region-b:50071".to_string()],
        },
    );

    assert_eq!(
        snapshot.registration_routers,
        BTreeMap::from([(
            "stargate-0".to_string(),
            grpc_endpoint_with_dial("stargate-0.region-a:50071", "https://lb.region-a:443")
        )])
    );
    assert_eq!(
        snapshot.watch_urls,
        BTreeSet::from(["https://stargate.region-b:50071".to_string()])
    );
}

#[test]
fn watch_response_rejects_non_uri_recursive_seeds() {
    let snapshot = watch_endpoint_snapshot_from_response(
        "seed-a",
        WatchStargatesResponse {
            stargates: vec![],
            watch_stargate_urls: vec![
                "https://stargate.region-b:50071".to_string(),
                " http://127.0.0.1:50071 ".to_string(),
                "stargate.region-c:50071".to_string(),
                "ftp://stargate.region-d:50071".to_string(),
                "https://".to_string(),
            ],
        },
    );

    assert_eq!(
        snapshot.watch_urls,
        BTreeSet::from([
            "http://127.0.0.1:50071".to_string(),
            "https://stargate.region-b:50071".to_string(),
        ])
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

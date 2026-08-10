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

use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::common::{
    BackendHandle, TokenMapAuthenticator, direct_registration_config, init_crypto,
    make_stargate_runtime, make_stargate_runtime_with_auth_and_model_discovery,
    make_stargate_runtime_with_model_discovery, make_stargate_runtime_with_shared_discovery,
    make_stargate_runtime_with_shared_discovery_and_remote_watch_urls,
    make_stargate_runtime_with_watch_intervals, start_dummy_inst, wait_for_all_probes_routed_to,
    wait_for_routing, wait_for_routing_with_rk, wait_until, with_proxy_headers,
};
use pylon_lib::{
    AuthTokenProvider, CurrentModelStats, InferenceServerRegistrationClient,
    InferenceServerRegistrationConfig, PylonRuntimeState, QuicHttpTunnelHandle,
};
use stargate::runtime::{StargateHandle, StargateRuntime};
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::stargate_model_discovery_client::StargateModelDiscoveryClient;
use stargate_proto::pb::{
    InferenceServerStatus, ListModelsRequest, WatchStargatesRequest, WatchStargatesResponse,
};
use tonic::transport::Channel;

const TEST_TIMEOUT: Duration = Duration::from_secs(5);
type DiscoveryRuntime = (SocketAddr, SocketAddr, SocketAddr, StargateRuntime);

macro_rules! assert_models {
    ($fixture:expr, $routing_key:expr, $models:expr, $expected:expr) => {
        $fixture
            .assert_models($routing_key, $models, $expected)
            .await
    };
}

struct DirectBackend {
    registration: InferenceServerRegistrationClient,
    runtime: PylonRuntimeState,
    _tunnel: QuicHttpTunnelHandle,
}

async fn shutdown(handles: impl IntoIterator<Item = StargateHandle>) {
    let handles = handles.into_iter().collect::<Vec<_>>();
    for handle in &handles {
        handle.begin_shutdown();
    }
    for handle in handles {
        handle.wait_for_shutdown(TEST_TIMEOUT).await;
    }
}

impl DirectBackend {
    async fn start(grpc_addr: SocketAddr, id: &str, model: &str, auth_token: Option<&str>) -> Self {
        let (upstream_addr, quic_url, tunnel) = start_dummy_inst(model).await;
        let runtime = PylonRuntimeState::new(InferenceServerStatus::Active, &[model.to_string()]);
        let mut registration = InferenceServerRegistrationClient::default();
        registration
            .start(InferenceServerRegistrationConfig {
                auth_token_provider: auth_token
                    .map(|token| Arc::new(AuthTokenProvider::Static(token.to_string()))),
                ..direct_registration_config(
                    vec![grpc_addr.to_string()],
                    id,
                    quic_url,
                    format!("http://{upstream_addr}"),
                    runtime.clone(),
                )
            })
            .expect("registration failed");
        Self {
            registration,
            runtime,
            _tunnel: tunnel,
        }
    }

    fn set_queued_input_size(&self, queued_input_size: u64) {
        self.runtime.set_model_stats(
            "stats-model".to_string(),
            CurrentModelStats {
                last_mean_input_tps: 1000.0,
                queued_input_size,
                ..CurrentModelStats::default()
            },
        );
    }

    fn stop(&mut self) {
        self.registration.stop();
    }
}

#[tokio::test]
async fn stats_update_via_runtime_state_propagates() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-stats");
    let handle = runtime.start().await.expect("stargate failed to start");

    let mut backend_a = DirectBackend::start(grpc_addr, "stats-inst-a", "stats-model", None).await;
    let mut backend_b = DirectBackend::start(grpc_addr, "stats-inst-b", "stats-model", None).await;
    wait_for_routing(http_addr, "stats-model", Duration::from_secs(5)).await;

    // Give instance A more pending prompt work so p2c prefers B.
    backend_a.set_queued_input_size(1_000);
    backend_b.set_queued_input_size(0);

    wait_for_all_probes_routed_to(
        http_addr,
        "stats-model",
        "req-stats-wait",
        "stats-inst-b",
        20,
        Duration::from_secs(15),
    )
    .await;

    backend_a.stop();
    backend_b.stop();
    shutdown([handle]).await;
}

#[tokio::test]
async fn watch_stargates_returns_self() {
    init_crypto();

    let (grpc_addr, _http_addr, runtime) = make_stargate_runtime("test-sg-watch");
    let handle = runtime.start().await.expect("stargate failed to start");

    let msg = first_stargate_snapshot(grpc_addr).await;

    let expected = grpc_addr.to_string();
    assert!(
        msg.stargates
            .iter()
            .any(|stargate| stargate.advertise_addr == expected),
        "WatchStargates should contain its own advertise_addr ({expected}), got: {:?}",
        msg.stargates
    );

    shutdown([handle]).await;
}

#[tokio::test]
async fn watch_stargates_returns_remote_watch_urls_without_remote_registration_targets() {
    init_crypto();

    let peers = Arc::new(Mutex::new(Vec::new()));
    let (grpc_addr, _http_addr, runtime) =
        make_stargate_runtime_with_shared_discovery_and_remote_watch_urls(
            "test-sg-watch-remote",
            peers,
            vec![
                " remote-b:50071 ".to_string(),
                "remote-a:50071".to_string(),
                "remote-b:50071".to_string(),
                String::new(),
            ],
        );
    let handle = runtime.start().await.expect("stargate failed to start");

    let msg = first_stargate_snapshot(grpc_addr).await;

    assert_eq!(msg.stargates.len(), 1);
    assert_eq!(msg.stargates[0].stargate_id, "test-sg-watch-remote");
    assert_eq!(
        msg.watch_stargate_urls,
        vec!["remote-a:50071", "remote-b:50071"]
    );

    shutdown([handle]).await;
}

#[tokio::test]
async fn watch_stargates_first_message_uses_discovery_snapshot_not_self_only_initial() {
    init_crypto();

    let peers = Arc::new(Mutex::new(Vec::new()));
    let (grpc_addr_1, _http_addr_1, runtime_1) =
        make_stargate_runtime_with_shared_discovery("test-sg-watch-full-1", peers.clone());
    let (_grpc_addr_2, _http_addr_2, runtime_2) =
        make_stargate_runtime_with_shared_discovery("test-sg-watch-full-2", peers);
    let handle_1 = runtime_1.start().await.expect("stargate 1 failed");
    let handle_2 = runtime_2.start().await.expect("stargate 2 failed");

    let msg = first_stargate_snapshot(grpc_addr_1).await;

    let mut ids: Vec<&str> = msg
        .stargates
        .iter()
        .map(|s| s.stargate_id.as_str())
        .collect();
    ids.sort();
    assert_eq!(
        ids,
        vec!["test-sg-watch-full-1", "test-sg-watch-full-2"],
        "first WatchStargates message should come from discovery, not self-only initial state"
    );

    shutdown([handle_1, handle_2]).await;
}

#[tokio::test]
async fn watch_stargates_emits_heartbeat_snapshots_without_membership_change() {
    init_crypto();

    let (grpc_addr, _http_addr, runtime) = make_stargate_runtime_with_watch_intervals(
        "test-sg-watch-heartbeat",
        Duration::from_millis(50),
        Duration::from_millis(100),
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let mut stream = watch_stargates(grpc_addr).await;

    let first = next_stargate_snapshot(&mut stream, "initial").await;
    let second = next_stargate_snapshot(&mut stream, "heartbeat").await;

    assert_eq!(
        first, second,
        "heartbeat should republish the same snapshot when discovery is unchanged"
    );

    drop(stream);
    shutdown([handle]).await;
}

async fn connect_channel(addr: SocketAddr, label: &str) -> Channel {
    let endpoint = Channel::from_shared(format!("http://{addr}")).expect("invalid endpoint");
    wait_until(
        &format!("connect to {label}"),
        TEST_TIMEOUT,
        Duration::from_millis(100),
        || {
            let endpoint = endpoint.clone();
            async move { endpoint.connect().await.map_err(|error| error.to_string()) }
        },
    )
    .await
}

async fn watch_stargates(grpc_addr: SocketAddr) -> tonic::Streaming<WatchStargatesResponse> {
    StargateControlPlaneClient::new(connect_channel(grpc_addr, "stargate gRPC").await)
        .watch_stargates(WatchStargatesRequest {})
        .await
        .expect("WatchStargates RPC failed")
        .into_inner()
}

async fn first_stargate_snapshot(grpc_addr: std::net::SocketAddr) -> WatchStargatesResponse {
    let mut stream = watch_stargates(grpc_addr).await;
    next_stargate_snapshot(&mut stream, "first").await
}

async fn next_stargate_snapshot(
    stream: &mut tonic::Streaming<WatchStargatesResponse>,
    label: &str,
) -> WatchStargatesResponse {
    tokio::time::timeout(TEST_TIMEOUT, stream.message())
        .await
        .unwrap_or_else(|_| panic!("timed out waiting for {label} WatchStargates"))
        .expect("stream error")
        .expect("stream ended without a message")
}

async fn get_json(http_addr: SocketAddr, path: &str) -> serde_json::Value {
    let response = reqwest::Client::new()
        .get(format!("http://{http_addr}{path}"))
        .send()
        .await
        .expect("HTTP inspection request failed");
    assert_eq!(response.status(), reqwest::StatusCode::OK);
    response
        .json()
        .await
        .expect("HTTP inspection response should be JSON")
}

fn list_models_request(routing_key: Option<&str>, model_ids: &[&str]) -> ListModelsRequest {
    ListModelsRequest {
        routing_key: routing_key.map(str::to_owned),
        model_ids: model_ids.iter().map(|id| (*id).to_owned()).collect(),
    }
}

struct ModelDiscoveryFixture {
    grpc_addr: SocketAddr,
    http_addr: SocketAddr,
    client: StargateModelDiscoveryClient<Channel>,
    handle: StargateHandle,
}

impl ModelDiscoveryFixture {
    async fn start(id: &str) -> Self {
        Self::from_runtime(make_stargate_runtime_with_model_discovery(id)).await
    }

    async fn start_with_auth(id: &str, auth: Arc<TokenMapAuthenticator>) -> Self {
        Self::from_runtime(make_stargate_runtime_with_auth_and_model_discovery(
            id, auth,
        ))
        .await
    }

    async fn from_runtime(
        (grpc_addr, model_discovery_addr, http_addr, runtime): DiscoveryRuntime,
    ) -> Self {
        init_crypto();
        let handle = runtime.start().await.expect("stargate failed to start");
        let client = StargateModelDiscoveryClient::new(
            connect_channel(model_discovery_addr, "model discovery gRPC").await,
        );
        Self {
            grpc_addr,
            http_addr,
            client,
            handle,
        }
    }

    async fn register(&self, backend_id: &str, model: &str) -> BackendHandle {
        let backend = crate::common::start_and_register_backend(
            &[self.grpc_addr.to_string()],
            backend_id,
            model,
            false,
        )
        .await;
        wait_for_routing(self.http_addr, model, TEST_TIMEOUT).await;
        backend
    }

    async fn direct_backend(
        &self,
        backend_id: &str,
        model: &str,
        auth_token: &str,
    ) -> DirectBackend {
        DirectBackend::start(self.grpc_addr, backend_id, model, Some(auth_token)).await
    }

    async fn assert_models<const N: usize, const M: usize>(
        &mut self,
        routing_key: Option<&str>,
        model_ids: [&str; N],
        expected_ids: [&str; M],
    ) {
        let client = self.client.clone();
        let request = list_models_request(routing_key, &model_ids);
        let mut expected_ids = expected_ids.to_vec();
        expected_ids.sort_unstable();
        wait_until(
            &format!("ListModels {model_ids:?} returns {expected_ids:?}"),
            TEST_TIMEOUT,
            Duration::from_millis(200),
            || {
                let mut client = client.clone();
                let request = request.clone();
                let expected_ids = expected_ids.clone();
                async move {
                    let mut actual_ids = client
                        .list_models(request)
                        .await
                        .map_err(|error| error.to_string())?
                        .into_inner()
                        .model_ids;
                    actual_ids.sort_unstable();
                    (actual_ids == expected_ids)
                        .then_some(())
                        .ok_or_else(|| format!("returned {actual_ids:?}"))
                }
            },
        )
        .await;
    }

    async fn shutdown(self) {
        drop(self.client);
        shutdown([self.handle]).await;
    }
}

#[tokio::test]
async fn list_models_empty_when_no_models_are_routable() {
    let mut fixture = ModelDiscoveryFixture::start("test-sg-list-empty").await;
    let mut wrong_port_client = StargateModelDiscoveryClient::new(
        connect_channel(fixture.grpc_addr, "control-plane gRPC").await,
    );

    let control_plane_list_models = wrong_port_client
        .list_models(ListModelsRequest::default())
        .await
        .expect_err("control-plane port should not serve ListModels");
    assert_eq!(
        control_plane_list_models.code(),
        tonic::Code::Unimplemented,
        "ListModels must only be served on the model-discovery port"
    );

    let response = fixture
        .client
        .list_models(ListModelsRequest::default())
        .await
        .expect("ListModels RPC failed")
        .into_inner();

    assert!(
        response.model_ids.is_empty(),
        "got: {:?}",
        response.model_ids
    );

    drop(wrong_port_client);
    fixture.shutdown().await;
}

#[tokio::test]
async fn list_models_returns_filtered_active_models() {
    let mut fixture = ModelDiscoveryFixture::start("test-sg-list-active").await;
    let mut alpha = fixture.register("list-backend-alpha", "list-alpha").await;
    let mut beta = fixture.register("list-backend-beta", "list-beta").await;

    assert_models!(fixture, None, ["list-alpha"], ["list-alpha"]);
    assert_models!(fixture, None, [], ["list-alpha", "list-beta"]);

    alpha.stop();
    assert_models!(fixture, None, [], ["list-beta"]);

    beta.stop();
    fixture.shutdown().await;
}

#[tokio::test]
async fn http_model_discovery_and_debug_state_follow_live_registration() {
    let mut fixture = ModelDiscoveryFixture::start("test-sg-http-inspection").await;
    let mut alpha = fixture
        .register("http-inspection-backend-alpha", "http-inspection-alpha")
        .await;
    let mut beta = fixture
        .register("http-inspection-backend-beta", "http-inspection-beta")
        .await;

    let grpc_models = fixture
        .client
        .list_models(list_models_request(
            None,
            &[" http-inspection-alpha ", "http-inspection-beta"],
        ))
        .await
        .expect("ListModels RPC failed")
        .into_inner()
        .model_ids;

    let model_body = get_json(
        fixture.http_addr,
        "/v1/models?model_ids=%20http-inspection-alpha%20&model_ids=http-inspection-beta",
    )
    .await;
    assert_eq!(model_body["model_ids"], serde_json::json!(grpc_models));

    let invalid_filter_response = reqwest::Client::new()
        .get(format!(
            "http://{}/v1/models?model_ids=%20",
            fixture.http_addr
        ))
        .send()
        .await
        .expect("HTTP model discovery invalid-filter request failed");
    assert_eq!(
        invalid_filter_response.status(),
        reqwest::StatusCode::BAD_REQUEST
    );

    let debug_body = get_json(fixture.http_addr, "/debug/state").await;
    let debug_config = &debug_body["config"];
    assert_eq!(debug_config["stargate_id"], "test-sg-http-inspection");
    assert_eq!(debug_config["backend_connectivity"], "direct");
    assert_eq!(
        debug_config["http_listen_addr"],
        fixture.http_addr.to_string()
    );
    assert!(
        debug_config.get("tls_cert_pem").is_none(),
        "debug config must contain only safe, explicit fields"
    );
    assert_eq!(
        debug_body["active_model_ids"],
        serde_json::json!(["http-inspection-alpha", "http-inspection-beta"])
    );

    alpha.stop();
    beta.stop();
    fixture.shutdown().await;
}

#[tokio::test]
async fn recent_list_models_hit_can_be_followed_by_no_candidates_404() {
    let mut fixture = ModelDiscoveryFixture::start("test-sg-list-404-race").await;
    let mut backend = fixture.register("list-404-backend", "list-404-model").await;
    assert_models!(fixture, None, ["list-404-model"], ["list-404-model"]);

    backend.stop();

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{}/v1/chat/completions", fixture.http_addr);
    let body = serde_json::json!({
        "model": "list-404-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    });
    let response = wait_until(
        "proxy returns no-candidates 404 after recent ListModels hit",
        TEST_TIMEOUT,
        Duration::from_millis(50),
        || {
            let request = with_proxy_headers(
                http_client.post(&stargate_url),
                "list-404-model",
                "req-list-404-race",
            )
            .header("content-type", "application/json")
            .json(&body);
            async move {
                let response = request.send().await.map_err(|error| error.to_string())?;
                (response.status() == 404)
                    .then_some(response)
                    .ok_or_else(|| "response was not 404".to_string())
            }
        },
    )
    .await;
    assert_eq!(
        response
            .headers()
            .get("x-stargate-error-code")
            .and_then(|value| value.to_str().ok()),
        Some("no_eligible_candidates"),
        "recent ListModels hit followed by local model disappearance should return the no-candidates contract"
    );
    let body: serde_json::Value = response
        .json()
        .await
        .expect("no-candidates response body should be json");
    assert_eq!(body["code"], "no_eligible_candidates");

    fixture.shutdown().await;
}

#[tokio::test]
async fn list_models_filters_by_routing_key() {
    let auth = Arc::new(TokenMapAuthenticator::new([
        ("list-token-a", "rk-list-a"),
        ("list-token-b", "rk-list-b"),
    ]));
    let mut fixture = ModelDiscoveryFixture::start_with_auth("test-sg-list-rk", auth).await;
    let mut backend_a = fixture
        .direct_backend("list-rk-backend-a", "list-rk-model-a", "list-token-a")
        .await;
    let mut backend_b = fixture
        .direct_backend("list-rk-backend-b", "list-rk-model-b", "list-token-b")
        .await;

    for (routing_key, model) in [
        ("rk-list-a", "list-rk-model-a"),
        ("rk-list-b", "list-rk-model-b"),
    ] {
        wait_for_routing_with_rk(fixture.http_addr, Some(routing_key), model, TEST_TIMEOUT).await;
    }

    let debug_body = get_json(fixture.http_addr, "/debug/state").await;
    assert_eq!(
        debug_body["active_model_ids"],
        serde_json::json!(["list-rk-model-a", "list-rk-model-b"])
    );

    assert_models!(fixture, None, [], []);
    assert_models!(fixture, Some("rk-list-a"), [], ["list-rk-model-a"]);
    assert_models!(fixture, Some("rk-list-b"), [], ["list-rk-model-b"]);
    assert_models!(fixture, Some("rk-list-c"), [], []);
    assert_models!(
        fixture,
        Some(" rk-list-a "),
        [" list-rk-model-a "],
        ["list-rk-model-a"]
    );

    let blank_model_filter = fixture
        .client
        .list_models(list_models_request(Some("rk-list-a"), &[" "]))
        .await
        .expect_err("blank model filters should be rejected");
    assert_eq!(
        blank_model_filter.code(),
        tonic::Code::InvalidArgument,
        "blank model filters should be caller errors"
    );

    backend_a.stop();
    backend_b.stop();
    fixture.shutdown().await;
}

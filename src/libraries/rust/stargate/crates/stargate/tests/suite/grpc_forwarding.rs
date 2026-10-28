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

use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::common::{
    MapResolver, base_config, init_crypto, start_dummy_inst, wait_for_routing, wait_for_unroutable,
    wait_until, with_proxy_headers,
};
use futures::StreamExt;
use stargate::runtime::BoundStargateListeners;
use stargate_forwarding::ForwardingResolver;
use stargate_proto::REGISTRATION_HEARTBEAT_MS_METADATA;
use stargate_proto::pb::StargateInfo;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::stargate_model_discovery_client::StargateModelDiscoveryClient;
use stargate_proto::pb::{
    InferenceServerAck, InferenceServerModelRegistration, InferenceServerRegistration,
    InferenceServerStatus, ListModelsRequest, ModelStats, WatchStargatesRequest,
};

type ControlClient = StargateControlPlaneClient<tonic::transport::Channel>;
type ModelDiscoveryClient = StargateModelDiscoveryClient<tonic::transport::Channel>;

fn channel_with_authority(
    actual_addr: std::net::SocketAddr,
    authority_host: &str,
    authority_port: u16,
) -> tonic::transport::Channel {
    let connector = tower::service_fn(move |_uri: http::Uri| async move {
        let stream = tokio::net::TcpStream::connect(actual_addr).await?;
        Ok::<_, std::io::Error>(hyper_util::rt::TokioIo::new(stream))
    });
    tonic::transport::Endpoint::from_shared(format!("http://{authority_host}:{authority_port}"))
        .unwrap()
        .connect_with_connector_lazy(connector)
}

fn make_registration(
    id: &str,
    url: &str,
    model: &str,
    status: InferenceServerStatus,
) -> InferenceServerRegistration {
    InferenceServerRegistration {
        inference_server_id: id.to_string(),
        cluster_id: String::new(),
        inference_server_url: url.to_string(),
        models: std::collections::HashMap::from([(
            model.to_string(),
            InferenceServerModelRegistration {
                stats: Some(ModelStats {
                    last_mean_input_tps: 100.0,
                    max_output_tps: 100.0,
                    ..ModelStats::default()
                }),
                status: status.into(),
            },
        )]),
        reverse_tunnel: false,
    }
}

struct TwoStargates {
    grpc_a: std::net::SocketAddr,
    model_discovery_a: std::net::SocketAddr,
    http_a: std::net::SocketAddr,
    grpc_b: std::net::SocketAddr,
    model_discovery_b: std::net::SocketAddr,
    http_b: std::net::SocketAddr,
    handle_a: stargate::runtime::StargateHandle,
    handle_b: stargate::runtime::StargateHandle,
}

impl TwoStargates {
    fn forwarded_client(&self) -> ControlClient {
        ControlClient::new(channel_with_authority(
            self.grpc_a,
            "sg-b",
            self.grpc_a.port(),
        ))
    }

    fn model_discovery_client(
        &self,
        actual_addr: std::net::SocketAddr,
        authority_port: u16,
    ) -> ModelDiscoveryClient {
        ModelDiscoveryClient::new(channel_with_authority(actual_addr, "sg-b", authority_port))
    }

    async fn wait_routable_on_b(&self, model: &str) {
        wait_for_routing(self.http_b, model, Duration::from_secs(15)).await;
    }

    async fn wait_unroutable_on_b(&self, model: &str) {
        wait_for_unroutable(self.http_b, model, Duration::from_secs(15)).await;
    }

    async fn shutdown(self) {
        self.handle_a.begin_shutdown();
        self.handle_b.begin_shutdown();
        self.handle_a
            .wait_for_shutdown(Duration::from_secs(5))
            .await;
        self.handle_b
            .wait_for_shutdown(Duration::from_secs(5))
            .await;
    }
}

struct ForwardedRegistration {
    updates: flume::Sender<InferenceServerRegistration>,
    id: String,
    url: String,
    model: String,
    _response: tonic::Streaming<InferenceServerAck>,
    _client: ControlClient,
}

impl ForwardedRegistration {
    async fn active(sg: &TwoStargates, id: &str, url: &str, model: &str) -> Self {
        Self::active_with_heartbeat(sg, id, url, model, None).await
    }

    async fn active_with_heartbeat(
        sg: &TwoStargates,
        id: &str,
        url: &str,
        model: &str,
        heartbeat_ms: Option<&str>,
    ) -> Self {
        let mut client = sg.forwarded_client();
        let (updates, registrations) = flume::bounded(16);
        updates
            .send_async(make_registration(
                id,
                url,
                model,
                InferenceServerStatus::Active,
            ))
            .await
            .unwrap();
        let mut request = tonic::Request::new(registrations.into_stream());
        if let Some(heartbeat_ms) = heartbeat_ms {
            request.metadata_mut().insert(
                REGISTRATION_HEARTBEAT_MS_METADATA,
                heartbeat_ms.parse().unwrap(),
            );
        }
        let response = client
            .register_inference_server(request)
            .await
            .expect("register failed")
            .into_inner();
        Self {
            updates,
            id: id.to_string(),
            url: url.to_string(),
            model: model.to_string(),
            _response: response,
            _client: client,
        }
    }

    async fn set_status(&self, status: InferenceServerStatus) {
        self.updates
            .send_async(make_registration(&self.id, &self.url, &self.model, status))
            .await
            .unwrap();
    }
}

async fn assert_forwarded_registration_error(
    sg: &TwoStargates,
    registration: InferenceServerRegistration,
    expected: tonic::Code,
) {
    let mut client = sg.forwarded_client();
    let (updates, registrations) = flume::bounded(16);
    updates.send_async(registration).await.unwrap();
    match client
        .register_inference_server(registrations.into_stream())
        .await
    {
        Err(status) => assert_eq!(status.code(), expected, "got: {status}"),
        Ok(response) => match response.into_inner().next().await {
            Some(Err(status)) => assert_eq!(status.code(), expected, "got: {status}"),
            other => panic!("expected {expected:?}, got: {other:?}"),
        },
    }
}

async fn proxy_chat(
    addr: std::net::SocketAddr,
    model: &str,
    request_id: &str,
) -> reqwest::Response {
    with_proxy_headers(
        reqwest::Client::new().post(format!("http://{addr}/v1/chat/completions")),
        model,
        request_id,
    )
    .header("content-type", "application/json")
    .json(&serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    }))
    .send()
    .await
    .expect("request failed")
}

async fn start_two_stargates() -> TwoStargates {
    start_two_stargates_with_registration_idle_timeout(
        stargate::registration::DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT,
    )
    .await
}

async fn start_two_stargates_with_registration_idle_timeout(
    registration_update_idle_timeout: Duration,
) -> TwoStargates {
    init_crypto();
    let peers = Arc::new(Mutex::new(Vec::<StargateInfo>::new()));
    let bind = |name| {
        let any_addr = "127.0.0.1:0".parse().unwrap();
        let mut config = base_config(name, any_addr, any_addr);
        config.model_discovery_listen_addr = any_addr;
        config.dns_poll_interval = Duration::from_secs(1);
        config.registration_update_idle_timeout = registration_update_idle_timeout;
        let listeners = BoundStargateListeners::bind(&mut config).unwrap();
        (config, listeners)
    };
    let (mut config_a, listeners_a) = bind("sg-a");
    let grpc_a = config_a.grpc_listen_addr;
    let model_discovery_a = config_a.model_discovery_listen_addr;
    let http_a = config_a.http_listen_addr;
    let (mut config_b, listeners_b) = bind("sg-b");
    let grpc_b = config_b.grpc_listen_addr;
    let model_discovery_b = config_b.model_discovery_listen_addr;
    let http_b = config_b.http_listen_addr;

    let mut resolver_a = MapResolver::new("sg-a");
    resolver_a.insert("sg-b", grpc_b);
    let resolver_a: Arc<dyn ForwardingResolver> = Arc::new(resolver_a);

    let mut resolver_b = MapResolver::new("sg-b");
    resolver_b.insert("sg-a", grpc_a);
    let resolver_b: Arc<dyn ForwardingResolver> = Arc::new(resolver_b);

    config_a.forwarding = Some(resolver_a);
    let discovery_a = crate::common::SharedDiscovery::new("sg-a", grpc_a, http_a, peers.clone());
    let runtime_a =
        stargate::runtime::StargateRuntime::new(config_a, Box::new(discovery_a), listeners_a, None);

    config_b.forwarding = Some(resolver_b);
    let discovery_b = crate::common::SharedDiscovery::new("sg-b", grpc_b, http_b, peers.clone());
    let runtime_b =
        stargate::runtime::StargateRuntime::new(config_b, Box::new(discovery_b), listeners_b, None);

    let handle_a = runtime_a.start().await.expect("stargate A failed");
    let handle_b = runtime_b.start().await.expect("stargate B failed");

    crate::common::wait_for_healthy(http_a, Duration::from_secs(5)).await;
    crate::common::wait_for_healthy(http_b, Duration::from_secs(5)).await;

    TwoStargates {
        grpc_a,
        model_discovery_a,
        http_a,
        grpc_b,
        model_discovery_b,
        http_b,
        handle_a,
        handle_b,
    }
}

async fn wait_for_list_models(
    client: &mut ModelDiscoveryClient,
    expected_ids: &[&str],
) -> Vec<String> {
    let mut expected_ids = expected_ids
        .iter()
        .map(|id| (*id).to_string())
        .collect::<Vec<_>>();
    expected_ids.sort_unstable();
    wait_until(
        &format!("ListModels to return {expected_ids:?}"),
        Duration::from_secs(5),
        Duration::from_millis(200),
        || {
            let mut client = client.clone();
            let expected_ids = expected_ids.clone();
            async move {
                let response = client
                    .list_models(ListModelsRequest {
                        routing_key: None,
                        model_ids: vec![],
                    })
                    .await
                    .map_err(|error| format!("RPC failed: {error:?}"))?
                    .into_inner();
                let mut actual_ids = response.model_ids.clone();
                actual_ids.sort_unstable();
                (actual_ids == expected_ids)
                    .then_some(response.model_ids)
                    .ok_or_else(|| format!("returned {actual_ids:?}"))
            }
        },
    )
    .await
}

#[tokio::test]
async fn register_forwarded_to_correct_peer() {
    let sg = start_two_stargates().await;
    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("fwd-model").await;
    {
        let _registration =
            ForwardedRegistration::active(&sg, "fwd-backend", &quic_url, "fwd-model").await;
        sg.wait_routable_on_b("fwd-model").await;

        let resp = proxy_chat(sg.http_a, "fwd-model", "req-not-on-a").await;
        assert_ne!(
            resp.status(),
            200,
            "fwd-model should NOT be routable on stargate A"
        );
    }
    sg.shutdown().await;
}

#[tokio::test]
async fn list_models_with_peer_authority_serves_local_routing_state() {
    let sg = start_two_stargates().await;
    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("fwd-list-model").await;
    {
        let _registration =
            ForwardedRegistration::active(&sg, "fwd-list-backend", &quic_url, "fwd-list-model")
                .await;
        sg.wait_routable_on_b("fwd-list-model").await;

        let mut list_client_b =
            sg.model_discovery_client(sg.model_discovery_b, sg.model_discovery_b.port());
        let models_b = wait_for_list_models(&mut list_client_b, &["fwd-list-model"]).await;
        assert_eq!(models_b, vec!["fwd-list-model"]);

        let mut list_client_a_with_b_authority =
            sg.model_discovery_client(sg.model_discovery_a, sg.model_discovery_b.port());
        let response = list_client_a_with_b_authority
            .list_models(ListModelsRequest {
                routing_key: None,
                model_ids: vec![],
            })
            .await
            .expect("ListModels RPC failed")
            .into_inner();
        assert!(
            response.model_ids.is_empty(),
            "ListModels must serve the receiving pod's local routing state, got {:?}",
            response.model_ids
        );
    }
    sg.shutdown().await;
}

#[tokio::test]
async fn watch_stargates_forwarded_to_correct_peer() {
    let sg = start_two_stargates().await;
    {
        let mut client = sg.forwarded_client();
        let mut stream = client
            .watch_stargates(WatchStargatesRequest {})
            .await
            .expect("watch_stargates failed")
            .into_inner();
        let first = stream
            .next()
            .await
            .expect("no response")
            .expect("stream error");
        let addrs: Vec<&str> = first
            .stargates
            .iter()
            .map(|s| s.advertise_addr.as_str())
            .collect();
        let b_addr = sg.grpc_b.to_string();
        assert!(
            addrs.contains(&b_addr.as_str()),
            "forwarded WatchStargates should contain B's addr {b_addr}, got: {addrs:?}"
        );
    }
    sg.shutdown().await;
}

#[tokio::test]
async fn forwarded_registration_preserves_heartbeat_metadata_for_idle_cleanup() {
    let sg = start_two_stargates_with_registration_idle_timeout(Duration::from_millis(50)).await;
    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("heartbeat-fwd-model").await;
    {
        let _registration = ForwardedRegistration::active_with_heartbeat(
            &sg,
            "heartbeat-fwd-backend",
            &quic_url,
            "heartbeat-fwd-model",
            Some("1000"),
        )
        .await;
        sg.wait_routable_on_b("heartbeat-fwd-model").await;
        wait_for_unroutable(sg.http_b, "heartbeat-fwd-model", Duration::from_secs(5)).await;
    }
    sg.shutdown().await;
}

#[tokio::test]
async fn forwarded_registration_cleanup_on_disconnect() {
    let sg = start_two_stargates().await;
    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("cleanup-model").await;
    let registration =
        ForwardedRegistration::active(&sg, "cleanup-backend", &quic_url, "cleanup-model").await;
    sg.wait_routable_on_b("cleanup-model").await;
    // Ending the fixture closes the forwarded stream and deregisters the backend.
    drop(registration);
    sg.wait_unroutable_on_b("cleanup-model").await;
    sg.shutdown().await;
}

#[tokio::test]
async fn forwarded_registration_propagates_status_updates() {
    let sg = start_two_stargates().await;
    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("status-model").await;
    {
        let registration =
            ForwardedRegistration::active(&sg, "status-backend", &quic_url, "status-model").await;
        sg.wait_routable_on_b("status-model").await;
        registration
            .set_status(InferenceServerStatus::Inactive)
            .await;
        sg.wait_unroutable_on_b("status-model").await;
        registration.set_status(InferenceServerStatus::Active).await;
        sg.wait_routable_on_b("status-model").await;
    }
    sg.shutdown().await;
}

#[tokio::test]
async fn forwarded_registration_status_updates_list_models_on_target_peer() {
    let sg = start_two_stargates().await;
    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("status-list-model").await;
    {
        let registration = ForwardedRegistration::active(
            &sg,
            "status-list-backend",
            &quic_url,
            "status-list-model",
        )
        .await;
        sg.wait_routable_on_b("status-list-model").await;
        let mut list_client_b =
            sg.model_discovery_client(sg.model_discovery_b, sg.model_discovery_b.port());
        wait_for_list_models(&mut list_client_b, &["status-list-model"]).await;

        registration
            .set_status(InferenceServerStatus::Inactive)
            .await;
        sg.wait_unroutable_on_b("status-list-model").await;
        wait_for_list_models(&mut list_client_b, &[]).await;

        registration.set_status(InferenceServerStatus::Active).await;
        sg.wait_routable_on_b("status-list-model").await;
        wait_for_list_models(&mut list_client_b, &["status-list-model"]).await;
    }
    sg.shutdown().await;
}

#[tokio::test]
async fn register_with_empty_id_rejected() {
    let sg = start_two_stargates().await;
    assert_forwarded_registration_error(
        &sg,
        InferenceServerRegistration {
            inference_server_id: String::new(),
            cluster_id: String::new(),
            inference_server_url: "quic://10.0.0.1:8080".to_string(),
            models: Default::default(),
            reverse_tunnel: false,
        },
        tonic::Code::InvalidArgument,
    )
    .await;
    sg.shutdown().await;
}

#[tokio::test]
async fn register_with_invalid_url_rejected() {
    let sg = start_two_stargates().await;
    assert_forwarded_registration_error(
        &sg,
        make_registration(
            "bad-url-backend",
            "http://not-a-quic-url",
            "bad-url-model",
            InferenceServerStatus::Active,
        ),
        tonic::Code::InvalidArgument,
    )
    .await;
    sg.shutdown().await;
}

#[tokio::test]
async fn forwarded_request_for_unknown_model_returns_404() {
    let sg = start_two_stargates().await;
    for addr in [sg.http_a, sg.http_b] {
        let resp = proxy_chat(addr, "nonexistent-model", "req-unknown").await;
        assert_eq!(
            resp.status(),
            404,
            "unknown model should return 404 on {addr}"
        );
        assert_eq!(
            resp.headers()
                .get("x-stargate-error-code")
                .and_then(|value| value.to_str().ok()),
            Some("no_eligible_candidates"),
            "no-candidates proxy errors should be distinguishable from upstream errors"
        );
    }

    sg.shutdown().await;
}

#[tokio::test]
async fn forwarded_duplicate_registration_rejected() {
    let sg = start_two_stargates().await;
    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("dup-fwd-model").await;
    {
        let _first =
            ForwardedRegistration::active(&sg, "dup-fwd-backend", &quic_url, "dup-fwd-model").await;
        sg.wait_routable_on_b("dup-fwd-model").await;
        assert_forwarded_registration_error(
            &sg,
            make_registration(
                "dup-fwd-backend",
                &quic_url,
                "dup-fwd-model",
                InferenceServerStatus::Active,
            ),
            tonic::Code::AlreadyExists,
        )
        .await;
    }
    sg.shutdown().await;
}

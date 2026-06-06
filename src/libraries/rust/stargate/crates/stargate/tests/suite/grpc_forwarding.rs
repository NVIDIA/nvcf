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
    MapResolver, base_config, bind_ephemeral, init_crypto, start_dummy_inst, wait_for_routing,
    wait_for_unroutable, with_proxy_headers,
};
use futures::StreamExt;
use stargate_forwarding::ForwardingResolver;
use stargate_proto::REGISTRATION_HEARTBEAT_MS_METADATA;
use stargate_proto::pb::StargateInfo;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::stargate_model_discovery_client::StargateModelDiscoveryClient;
use stargate_proto::pb::{
    InferenceServerModelRegistration, InferenceServerRegistration, InferenceServerStatus,
    ListModelsRequest, ModelStats, WatchStargatesRequest,
};

/// Builds a tonic channel whose TCP connection goes to `actual_addr` but
/// whose HTTP/2 `:authority` is `authority_host:authority_port`.
/// This simulates the K8s pattern where ClusterIP routes to any pod.
async fn connect_with_authority(
    actual_addr: std::net::SocketAddr,
    authority_host: &str,
    authority_port: u16,
) -> StargateControlPlaneClient<tonic::transport::Channel> {
    let connector = tower::service_fn(move |_uri: http::Uri| async move {
        let stream = tokio::net::TcpStream::connect(actual_addr).await?;
        Ok::<_, std::io::Error>(hyper_util::rt::TokioIo::new(stream))
    });
    let channel = tonic::transport::Endpoint::from_shared(format!(
        "http://{authority_host}:{authority_port}"
    ))
    .unwrap()
    .connect_with_connector_lazy(connector);
    StargateControlPlaneClient::new(channel)
}

async fn connect_model_discovery_with_authority(
    actual_addr: std::net::SocketAddr,
    authority_host: &str,
    authority_port: u16,
) -> StargateModelDiscoveryClient<tonic::transport::Channel> {
    let connector = tower::service_fn(move |_uri: http::Uri| async move {
        let stream = tokio::net::TcpStream::connect(actual_addr).await?;
        Ok::<_, std::io::Error>(hyper_util::rt::TokioIo::new(stream))
    });
    let channel = tonic::transport::Endpoint::from_shared(format!(
        "http://{authority_host}:{authority_port}"
    ))
    .unwrap()
    .connect_with_connector_lazy(connector);
    StargateModelDiscoveryClient::new(channel)
}

fn make_registration(
    id: &str,
    url: &str,
    model: &str,
    status: InferenceServerStatus,
) -> InferenceServerRegistration {
    let mut models = std::collections::HashMap::new();
    models.insert(
        model.to_string(),
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
                queue_time_estimate_ms_by_priority: std::collections::HashMap::new(),
                ..ModelStats::default()
            }),
            status: status.into(),
        },
    );
    InferenceServerRegistration {
        inference_server_id: id.to_string(),
        cluster_id: String::new(),
        inference_server_url: url.to_string(),
        models,
        reverse_tunnel: false,
        coordinated_calibration: false,
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

/// Spins up two stargates (A and B) with SharedDiscovery and gRPC forwarding.
///
/// Each stargate has a MapResolver that recognises incoming `:authority`
/// headers belonging to the other stargate and resolves them to that
/// peer's gRPC listen address:
///
///   A's resolver:  self_host="sg-a", "sg-b" -> B's gRPC addr
///   B's resolver:  self_host="sg-b", "sg-a" -> A's gRPC addr
///
/// When a gRPC request arrives at A with `:authority` == "sg-b", A's
/// resolver returns B's address and A proxies the request to B (and
/// vice versa). Requests whose `:authority` matches self are handled
/// locally.
async fn start_two_stargates() -> TwoStargates {
    start_two_stargates_with_registration_idle_timeout(
        stargate::registration::DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT,
    )
    .await
}

async fn start_two_stargates_with_registration_idle_timeout(
    registration_update_idle_timeout: Duration,
) -> TwoStargates {
    let peers = Arc::new(Mutex::new(Vec::<StargateInfo>::new()));

    let (grpc_a, grpc_listener_a) = bind_ephemeral();
    let (grpc_b, grpc_listener_b) = bind_ephemeral();
    let (model_discovery_a, model_discovery_listener_a) = bind_ephemeral();
    let (model_discovery_b, model_discovery_listener_b) = bind_ephemeral();
    let (http_a, http_listener_a) = bind_ephemeral();
    let (http_b, http_listener_b) = bind_ephemeral();

    let mut resolver_a = MapResolver::new("sg-a");
    resolver_a.insert("sg-b", grpc_b);
    let resolver_a: Arc<dyn ForwardingResolver> = Arc::new(resolver_a);

    let mut resolver_b = MapResolver::new("sg-b");
    resolver_b.insert("sg-a", grpc_a);
    let resolver_b: Arc<dyn ForwardingResolver> = Arc::new(resolver_b);

    let discovery_a = crate::common::SharedDiscovery::new("sg-a", grpc_a, http_a, peers.clone());
    let mut config_a = base_config("sg-a", grpc_a, http_a);
    config_a.model_discovery_listen_addr = model_discovery_a;
    config_a.dns_poll_interval = Duration::from_secs(1);
    config_a.registration_update_idle_timeout = registration_update_idle_timeout;
    let runtime_a = stargate::runtime::StargateRuntime::new(config_a, Box::new(discovery_a))
        .with_forwarding(resolver_a)
        .with_grpc_listener(grpc_listener_a)
        .with_model_discovery_listener(model_discovery_listener_a)
        .with_http_listener(http_listener_a);

    let discovery_b = crate::common::SharedDiscovery::new("sg-b", grpc_b, http_b, peers.clone());
    let mut config_b = base_config("sg-b", grpc_b, http_b);
    config_b.model_discovery_listen_addr = model_discovery_b;
    config_b.dns_poll_interval = Duration::from_secs(1);
    config_b.registration_update_idle_timeout = registration_update_idle_timeout;
    let runtime_b = stargate::runtime::StargateRuntime::new(config_b, Box::new(discovery_b))
        .with_forwarding(resolver_b)
        .with_grpc_listener(grpc_listener_b)
        .with_model_discovery_listener(model_discovery_listener_b)
        .with_http_listener(http_listener_b);

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
    client: &mut StargateModelDiscoveryClient<tonic::transport::Channel>,
    expected_ids: &[&str],
    timeout: Duration,
) -> Vec<String> {
    let mut expected_ids = expected_ids.to_vec();
    expected_ids.sort_unstable();
    let deadline = tokio::time::Instant::now() + timeout;
    let mut interval = tokio::time::interval(Duration::from_millis(200));
    interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    loop {
        let response = match client
            .list_models(ListModelsRequest {
                routing_key: None,
                model_ids: vec![],
            })
            .await
        {
            Ok(response) => response.into_inner(),
            Err(error) => {
                if tokio::time::Instant::now() >= deadline {
                    panic!(
                        "ListModels failed before returning {expected_ids:?} within {}s: {error:?}",
                        timeout.as_secs(),
                    );
                }
                interval.tick().await;
                continue;
            }
        };

        let mut actual_ids = response
            .model_ids
            .iter()
            .map(String::as_str)
            .collect::<Vec<_>>();
        actual_ids.sort_unstable();
        if actual_ids == expected_ids {
            return response.model_ids;
        }

        if tokio::time::Instant::now() >= deadline {
            panic!(
                "ListModels returned ids {actual_ids:?}; expected {expected_ids:?} within {}s",
                timeout.as_secs(),
            );
        }
        interval.tick().await;
    }
}

/// Backend registers via stargate A with authority matching B.
/// Stargate A forwards the registration to B.
/// The backend becomes routable on B but not on A.
#[tokio::test]
async fn register_forwarded_to_correct_peer() {
    init_crypto();
    let sg = start_two_stargates().await;

    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("fwd-model").await;

    // Connect to stargate A's TCP address, but with authority "sg-b"
    // so the tower layer captures authority "sg-b:PORT" and A forwards to B.
    let mut client = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;

    let (tx, rx) = flume::bounded(16);
    tx.send_async(make_registration(
        "fwd-backend",
        &quic_url,
        "fwd-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    let _resp = client
        .register_inference_server(rx.into_stream())
        .await
        .expect("register failed");

    wait_for_routing(sg.http_b, "fwd-model", Duration::from_secs(15)).await;

    // Should NOT be routable on A
    let http_client = reqwest::Client::new();
    let resp = with_proxy_headers(
        http_client.post(format!("http://{}/v1/chat/completions", sg.http_a)),
        "fwd-model",
        "req-not-on-a",
    )
    .header("content-type", "application/json")
    .json(&serde_json::json!({
        "model": "fwd-model",
        "messages": [{"role": "user", "content": "hi"}],
        "stream": true,
    }))
    .send()
    .await
    .expect("request failed");
    assert_ne!(
        resp.status(),
        200,
        "fwd-model should NOT be routable on stargate A"
    );

    // Close gRPC streams before shutdown so tonic's graceful shutdown
    // doesn't block waiting for in-flight RPCs to finish.
    drop(tx);
    drop(client);
    sg.shutdown().await;
}

/// ListModels is local-only; a peer authority on A must not return B's snapshot.
#[tokio::test]
async fn list_models_with_peer_authority_serves_local_snapshot() {
    init_crypto();
    let sg = start_two_stargates().await;

    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("fwd-list-model").await;
    let mut register_client = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;
    let (tx, rx) = flume::bounded(16);
    tx.send_async(make_registration(
        "fwd-list-backend",
        &quic_url,
        "fwd-list-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    let _resp = register_client
        .register_inference_server(rx.into_stream())
        .await
        .expect("register failed");
    wait_for_routing(sg.http_b, "fwd-list-model", Duration::from_secs(15)).await;

    let mut list_client_b = connect_model_discovery_with_authority(
        sg.model_discovery_b,
        "sg-b",
        sg.model_discovery_b.port(),
    )
    .await;
    let models_b = wait_for_list_models(
        &mut list_client_b,
        &["fwd-list-model"],
        Duration::from_secs(5),
    )
    .await;
    assert_eq!(models_b, vec!["fwd-list-model"]);

    let mut list_client_a_with_b_authority = connect_model_discovery_with_authority(
        sg.model_discovery_a,
        "sg-b",
        sg.model_discovery_b.port(),
    )
    .await;
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
        "ListModels must serve the receiving pod's local snapshot, got {:?}",
        response.model_ids
    );

    // Close the registration sender so the streaming RPC can finish before graceful shutdown.
    drop(tx);
    // Close the registration client before shutting down the gRPC server.
    drop(register_client);
    // Close model-discovery clients before shutting down the model-discovery server.
    drop(list_client_b);
    // Close model-discovery clients before shutting down the model-discovery server.
    drop(list_client_a_with_b_authority);
    sg.shutdown().await;
}

/// WatchStargates called on A with authority matching B returns B's data.
#[tokio::test]
async fn watch_stargates_forwarded_to_correct_peer() {
    init_crypto();
    let sg = start_two_stargates().await;

    // Connect to A with B's authority
    let mut client = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;

    let resp = client
        .watch_stargates(WatchStargatesRequest {})
        .await
        .expect("watch_stargates failed");
    let mut stream = resp.into_inner();

    let first = stream
        .next()
        .await
        .expect("no response")
        .expect("stream error");

    // The forwarded response should contain B's discovery data
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

    // Close gRPC streams before shutdown so tonic's graceful shutdown
    // doesn't block waiting for in-flight RPCs to finish.
    drop(stream);
    drop(client);
    sg.shutdown().await;
}

/// Forwarded registration preserves the advertised heartbeat metadata so the
/// target peer still applies idle cleanup.
#[tokio::test]
async fn forwarded_registration_preserves_heartbeat_metadata_for_idle_cleanup() {
    init_crypto();
    let sg = start_two_stargates_with_registration_idle_timeout(Duration::from_millis(50)).await;

    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("heartbeat-fwd-model").await;

    let mut client = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;

    let (tx, rx) = flume::bounded(16);
    tx.send_async(make_registration(
        "heartbeat-fwd-backend",
        &quic_url,
        "heartbeat-fwd-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    let mut request = tonic::Request::new(rx.into_stream());
    request
        .metadata_mut()
        .insert(REGISTRATION_HEARTBEAT_MS_METADATA, "1000".parse().unwrap());

    let _resp = client
        .register_inference_server(request)
        .await
        .expect("register failed");

    wait_for_routing(sg.http_b, "heartbeat-fwd-model", Duration::from_secs(15)).await;
    wait_for_unroutable(sg.http_b, "heartbeat-fwd-model", Duration::from_secs(5)).await;

    // Close gRPC streams before shutdown so tonic's graceful shutdown
    // doesn't block waiting for in-flight RPCs to finish.
    drop(tx);
    drop(client);
    sg.shutdown().await;
}

/// Backend registers via forwarding A -> B, then the stream drops.
/// B should remove the backend from routing.
#[tokio::test]
async fn forwarded_registration_cleanup_on_disconnect() {
    init_crypto();
    let sg = start_two_stargates().await;

    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("cleanup-model").await;

    let mut client = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;

    let (tx, rx) = flume::bounded(16);
    tx.send_async(make_registration(
        "cleanup-backend",
        &quic_url,
        "cleanup-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    let _resp = client
        .register_inference_server(rx.into_stream())
        .await
        .expect("register failed");

    wait_for_routing(sg.http_b, "cleanup-model", Duration::from_secs(15)).await;

    // Close the registration stream so stargate deregisters the backend;
    // this also lets tonic's graceful shutdown complete without blocking.
    drop(tx);

    wait_for_unroutable(sg.http_b, "cleanup-model", Duration::from_secs(15)).await;

    sg.shutdown().await;
}

/// Backend registers Active via forwarding, then sends Inactive update.
/// The status change propagates through the forwarded stream.
#[tokio::test]
async fn forwarded_registration_propagates_status_updates() {
    init_crypto();
    let sg = start_two_stargates().await;

    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("status-model").await;

    let mut client = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;

    let (tx, rx) = flume::bounded(16);
    tx.send_async(make_registration(
        "status-backend",
        &quic_url,
        "status-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    let _resp = client
        .register_inference_server(rx.into_stream())
        .await
        .expect("register failed");

    wait_for_routing(sg.http_b, "status-model", Duration::from_secs(15)).await;

    // Send Inactive status
    tx.send_async(make_registration(
        "status-backend",
        &quic_url,
        "status-model",
        InferenceServerStatus::Inactive,
    ))
    .await
    .unwrap();

    wait_for_unroutable(sg.http_b, "status-model", Duration::from_secs(15)).await;

    // Send Active again
    tx.send_async(make_registration(
        "status-backend",
        &quic_url,
        "status-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    wait_for_routing(sg.http_b, "status-model", Duration::from_secs(15)).await;

    // Close gRPC streams before shutdown so tonic's graceful shutdown
    // doesn't block waiting for in-flight RPCs to finish.
    drop(tx);
    drop(client);
    sg.shutdown().await;
}

/// Forwarded lifecycle status updates must refresh the target peer's local
/// ListModels snapshot as well as proxy routing.
#[tokio::test]
async fn forwarded_registration_status_updates_refresh_list_models_on_target_peer() {
    init_crypto();
    let sg = start_two_stargates().await;

    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("status-list-model").await;

    let mut register_client = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;

    let (tx, rx) = flume::bounded(16);
    tx.send_async(make_registration(
        "status-list-backend",
        &quic_url,
        "status-list-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    let _resp = register_client
        .register_inference_server(rx.into_stream())
        .await
        .expect("register failed");

    wait_for_routing(sg.http_b, "status-list-model", Duration::from_secs(15)).await;
    let mut list_client_b = connect_model_discovery_with_authority(
        sg.model_discovery_b,
        "sg-b",
        sg.model_discovery_b.port(),
    )
    .await;
    wait_for_list_models(
        &mut list_client_b,
        &["status-list-model"],
        Duration::from_secs(5),
    )
    .await;

    tx.send_async(make_registration(
        "status-list-backend",
        &quic_url,
        "status-list-model",
        InferenceServerStatus::Inactive,
    ))
    .await
    .unwrap();

    wait_for_unroutable(sg.http_b, "status-list-model", Duration::from_secs(15)).await;
    wait_for_list_models(&mut list_client_b, &[], Duration::from_secs(5)).await;

    tx.send_async(make_registration(
        "status-list-backend",
        &quic_url,
        "status-list-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    wait_for_routing(sg.http_b, "status-list-model", Duration::from_secs(15)).await;
    wait_for_list_models(
        &mut list_client_b,
        &["status-list-model"],
        Duration::from_secs(5),
    )
    .await;

    // Close gRPC streams before shutdown so tonic's graceful shutdown
    // doesn't block waiting for in-flight RPCs to finish.
    drop(tx);
    drop(register_client);
    drop(list_client_b);
    sg.shutdown().await;
}

/// Registration with an empty inference_server_id through the forwarding
/// path is rejected with InvalidArgument on the target peer.
#[tokio::test]
async fn register_with_empty_id_rejected() {
    init_crypto();
    let sg = start_two_stargates().await;

    let mut client = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;

    let (tx, rx) = flume::bounded(16);
    tx.send_async(InferenceServerRegistration {
        inference_server_id: String::new(),
        cluster_id: String::new(),
        inference_server_url: "quic://10.0.0.1:8080".to_string(),
        models: Default::default(),
        reverse_tunnel: false,
        coordinated_calibration: false,
    })
    .await
    .unwrap();

    let result = client.register_inference_server(rx.into_stream()).await;

    match result {
        Err(status) => {
            assert_eq!(status.code(), tonic::Code::InvalidArgument, "got: {status}");
        }
        Ok(resp) => {
            let msg = resp.into_inner().next().await;
            match msg {
                Some(Err(status)) => {
                    assert_eq!(status.code(), tonic::Code::InvalidArgument, "got: {status}");
                }
                other => panic!("expected InvalidArgument, got: {other:?}"),
            }
        }
    }

    sg.shutdown().await;
}

/// Registration with an invalid (non-quic) URL through the forwarding
/// path is rejected with InvalidArgument.
#[tokio::test]
async fn register_with_invalid_url_rejected() {
    init_crypto();
    let sg = start_two_stargates().await;

    let mut client = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;

    let (tx, rx) = flume::bounded(16);
    tx.send_async(make_registration(
        "bad-url-backend",
        "http://not-a-quic-url",
        "bad-url-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    let result = client.register_inference_server(rx.into_stream()).await;

    match result {
        Err(status) => {
            assert_eq!(status.code(), tonic::Code::InvalidArgument, "got: {status}");
        }
        Ok(resp) => {
            let msg = resp.into_inner().next().await;
            match msg {
                Some(Err(status)) => {
                    assert_eq!(status.code(), tonic::Code::InvalidArgument, "got: {status}");
                }
                other => panic!("expected InvalidArgument, got: {other:?}"),
            }
        }
    }

    sg.shutdown().await;
}

/// Requesting an unknown model through the forwarding path returns 404.
#[tokio::test]
async fn forwarded_request_for_unknown_model_returns_404() {
    init_crypto();
    let sg = start_two_stargates().await;

    let http_client = reqwest::Client::new();
    for addr in [sg.http_a, sg.http_b] {
        let resp = with_proxy_headers(
            http_client.post(format!("http://{addr}/v1/chat/completions")),
            "nonexistent-model",
            "req-unknown",
        )
        .header("content-type", "application/json")
        .json(&serde_json::json!({
            "model": "nonexistent-model",
            "messages": [{"role": "user", "content": "hi"}],
            "stream": true,
        }))
        .send()
        .await
        .expect("request failed");
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

/// Registering the same inference_server_id twice through the forwarding
/// path should be rejected with AlreadyExists.
#[tokio::test]
async fn forwarded_duplicate_registration_rejected() {
    init_crypto();
    let sg = start_two_stargates().await;

    let (_backend_http, quic_url, _tunnel) = start_dummy_inst("dup-fwd-model").await;

    // First registration (forwarded A -> B)
    let mut client1 = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;
    let (tx1, rx1) = flume::bounded(16);
    tx1.send_async(make_registration(
        "dup-fwd-backend",
        &quic_url,
        "dup-fwd-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    let _resp1 = client1
        .register_inference_server(rx1.into_stream())
        .await
        .expect("first registration should succeed");
    let _keep_tx1 = tx1;

    wait_for_routing(sg.http_b, "dup-fwd-model", Duration::from_secs(15)).await;

    // Second registration with same id (also forwarded A -> B)
    let mut client2 = connect_with_authority(sg.grpc_a, "sg-b", sg.grpc_a.port()).await;
    let (tx2, rx2) = flume::bounded(16);
    tx2.send_async(make_registration(
        "dup-fwd-backend",
        &quic_url,
        "dup-fwd-model",
        InferenceServerStatus::Active,
    ))
    .await
    .unwrap();

    let result = client2.register_inference_server(rx2.into_stream()).await;

    match result {
        Err(status) => {
            assert_eq!(status.code(), tonic::Code::AlreadyExists, "got: {status}");
        }
        Ok(resp) => {
            let msg = resp.into_inner().next().await;
            match msg {
                Some(Err(status)) => {
                    assert_eq!(status.code(), tonic::Code::AlreadyExists, "got: {status}");
                }
                other => panic!("expected AlreadyExists, got: {other:?}"),
            }
        }
    }

    // Close gRPC streams before shutdown so tonic's graceful shutdown
    // doesn't block waiting for in-flight RPCs to finish.
    drop(_keep_tx1);
    drop(client1);
    drop(client2);
    sg.shutdown().await;
}

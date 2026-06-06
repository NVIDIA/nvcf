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
    assert_model_routing, bind_ephemeral_udp, init_crypto, make_stargate_runtime,
    make_stargate_runtime_with_reverse, make_stargate_runtime_with_shared_discovery,
    make_stargate_runtime_with_shared_discovery_and_remote_watch_urls,
    make_stargate_runtime_with_shared_discovery_and_reverse, start_and_register_backend,
    start_and_register_backend_with_bringup, start_counting_dummy_inst, wait_for_routing,
};
use pylon_lib::{
    BringupConfig, InferenceServerRegistrationClient, InferenceServerRegistrationConfig,
    OutputTokenParserFactory,
};
use stargate_proto::pb::InferenceServerStatus;

const ROUTES: &[(&str, &str)] = &[
    ("model-alpha", "backend-alpha"),
    ("model-beta", "backend-beta"),
];

#[tokio::test]
async fn two_models_forward_quic() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-fwd-2m");
    let handle = runtime.start().await.expect("stargate failed to start");

    let seeds = vec![grpc_addr.to_string()];
    let mut alpha = start_and_register_backend(&seeds, "backend-alpha", "model-alpha", false).await;
    let mut beta = start_and_register_backend(&seeds, "backend-beta", "model-beta", false).await;

    wait_for_routing(http_addr, "model-alpha", Duration::from_secs(5)).await;
    wait_for_routing(http_addr, "model-beta", Duration::from_secs(5)).await;

    assert_model_routing(&[http_addr], ROUTES, 3).await;

    alpha.stop();
    beta.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn two_models_reverse_tunnel() {
    init_crypto();

    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_reverse("test-sg-rev-2m", reverse_addr, Some(reverse_socket));
    let handle = runtime.start().await.expect("stargate failed to start");

    let seeds = vec![grpc_addr.to_string()];
    let mut alpha = start_and_register_backend(&seeds, "backend-alpha", "model-alpha", true).await;
    let mut beta = start_and_register_backend(&seeds, "backend-beta", "model-beta", true).await;

    wait_for_routing(http_addr, "model-alpha", Duration::from_secs(10)).await;
    wait_for_routing(http_addr, "model-beta", Duration::from_secs(10)).await;

    assert_model_routing(&[http_addr], ROUTES, 3).await;

    alpha.stop();
    beta.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// Two stargates with SharedDiscovery. Each backend seeds only stargate 1;
/// the client discovers stargate 2 via WatchStargates and registers with it.
#[tokio::test]
async fn two_models_multi_stargate_forward_quic() {
    init_crypto();

    let peers = Arc::new(Mutex::new(Vec::new()));
    let (grpc_addr_1, http_addr_1, runtime_1) =
        make_stargate_runtime_with_shared_discovery("test-sg-mfwd-1", peers.clone());
    let (_, http_addr_2, runtime_2) =
        make_stargate_runtime_with_shared_discovery("test-sg-mfwd-2", peers.clone());
    let handle_1 = runtime_1.start().await.expect("stargate 1 failed to start");
    let handle_2 = runtime_2.start().await.expect("stargate 2 failed to start");

    let seeds = vec![grpc_addr_1.to_string()];
    let mut alpha = start_and_register_backend(&seeds, "backend-alpha", "model-alpha", false).await;
    let mut beta = start_and_register_backend(&seeds, "backend-beta", "model-beta", false).await;

    for &http_addr in &[http_addr_1, http_addr_2] {
        wait_for_routing(http_addr, "model-alpha", Duration::from_secs(10)).await;
        wait_for_routing(http_addr, "model-beta", Duration::from_secs(10)).await;
    }

    assert_model_routing(&[http_addr_1, http_addr_2], ROUTES, 3).await;

    alpha.stop();
    beta.stop();
    handle_1.begin_shutdown();
    handle_2.begin_shutdown();
    handle_1.wait_for_shutdown(Duration::from_secs(5)).await;
    handle_2.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// Two stargates with SharedDiscovery and reverse tunnel. Each backend seeds
/// only stargate 1; the client discovers stargate 2 via WatchStargates.
#[tokio::test]
async fn two_models_multi_stargate_reverse_tunnel() {
    init_crypto();

    let peers = Arc::new(Mutex::new(Vec::new()));
    let (reverse_addr_1, reverse_socket_1) = bind_ephemeral_udp();
    let (reverse_addr_2, reverse_socket_2) = bind_ephemeral_udp();
    let (grpc_addr_1, http_addr_1, runtime_1) =
        make_stargate_runtime_with_shared_discovery_and_reverse(
            "test-sg-mrev-1",
            peers.clone(),
            reverse_addr_1,
            Some(reverse_socket_1),
        );
    let (_, http_addr_2, runtime_2) = make_stargate_runtime_with_shared_discovery_and_reverse(
        "test-sg-mrev-2",
        peers.clone(),
        reverse_addr_2,
        Some(reverse_socket_2),
    );
    let handle_1 = runtime_1.start().await.expect("stargate 1 failed to start");
    let handle_2 = runtime_2.start().await.expect("stargate 2 failed to start");

    let seeds = vec![grpc_addr_1.to_string()];
    let mut alpha = start_and_register_backend(&seeds, "backend-alpha", "model-alpha", true).await;
    let mut beta = start_and_register_backend(&seeds, "backend-beta", "model-beta", true).await;

    for &http_addr in &[http_addr_1, http_addr_2] {
        wait_for_routing(http_addr, "model-alpha", Duration::from_secs(15)).await;
        wait_for_routing(http_addr, "model-beta", Duration::from_secs(15)).await;
    }

    assert_model_routing(&[http_addr_1, http_addr_2], ROUTES, 3).await;

    alpha.stop();
    beta.stop();
    handle_1.begin_shutdown();
    handle_2.begin_shutdown();
    handle_1.wait_for_shutdown(Duration::from_secs(5)).await;
    handle_2.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// Region A advertises Region B's WatchStargates endpoint as a remote watch URL.
/// A backend seeded only with Region A must recursively watch Region B and
/// register with every discovered stargate pod in both regions.
#[tokio::test]
async fn backend_discovers_remote_region_watch_url_and_registers_globally() {
    init_crypto();

    let region_a = Arc::new(Mutex::new(Vec::new()));
    let region_b = Arc::new(Mutex::new(Vec::new()));
    let (_grpc_a0, http_a0, runtime_a0) =
        make_stargate_runtime_with_shared_discovery("test-global-a-0", region_a.clone());
    let (_, http_a1, runtime_a1) =
        make_stargate_runtime_with_shared_discovery("test-global-a-1", region_a.clone());
    let (grpc_b0, http_b0, runtime_b0) =
        make_stargate_runtime_with_shared_discovery_and_remote_watch_urls(
            "test-global-b-0",
            region_b.clone(),
            Vec::new(),
        );
    let (_, http_b1, runtime_b1) =
        make_stargate_runtime_with_shared_discovery_and_remote_watch_urls(
            "test-global-b-1",
            region_b.clone(),
            Vec::new(),
        );

    let handle_a0 = runtime_a0
        .start()
        .await
        .expect("region A stargate 0 failed");
    let handle_a1 = runtime_a1
        .start()
        .await
        .expect("region A stargate 1 failed");
    let handle_b0 = runtime_b0
        .start()
        .await
        .expect("region B stargate 0 failed");
    let handle_b1 = runtime_b1
        .start()
        .await
        .expect("region B stargate 1 failed");

    let (grpc_a_remote, http_a_remote, runtime_a_remote) =
        make_stargate_runtime_with_shared_discovery_and_remote_watch_urls(
            "test-global-a-remote",
            region_a.clone(),
            vec![grpc_b0.to_string()],
        );
    let handle_a_remote = runtime_a_remote
        .start()
        .await
        .expect("region A remote-advertising stargate failed");

    let seeds = vec![grpc_a_remote.to_string()];
    let mut backend = start_and_register_backend_with_bringup(
        &seeds,
        "backend-global",
        "model-global",
        false,
        // Disable calibration so this test isolates recursive WatchStargates
        // discovery and registration fanout.
        BringupConfig {
            enabled: false,
            ..BringupConfig::default()
        },
    )
    .await;

    for http_addr in [http_a0, http_a1, http_a_remote, http_b0, http_b1] {
        wait_for_routing(http_addr, "model-global", Duration::from_secs(15)).await;
    }

    backend.stop();
    handle_a0.begin_shutdown();
    handle_a1.begin_shutdown();
    handle_a_remote.begin_shutdown();
    handle_b0.begin_shutdown();
    handle_b1.begin_shutdown();
    handle_a0.wait_for_shutdown(Duration::from_secs(5)).await;
    handle_a1.wait_for_shutdown(Duration::from_secs(5)).await;
    handle_a_remote
        .wait_for_shutdown(Duration::from_secs(5))
        .await;
    handle_b0.wait_for_shutdown(Duration::from_secs(5)).await;
    handle_b1.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn global_watch_coordinated_calibration_completes_independently_on_each_stargate() {
    init_crypto();

    let region_a = Arc::new(Mutex::new(Vec::new()));
    let region_b = Arc::new(Mutex::new(Vec::new()));

    let (grpc_b0, http_b0, runtime_b0) =
        make_stargate_runtime_with_shared_discovery_and_remote_watch_urls(
            "test-global-cal-b-0",
            region_b.clone(),
            Vec::new(),
        );
    let handle_b0 = runtime_b0
        .start()
        .await
        .expect("region B stargate 0 failed");

    let (grpc_a0, http_a0, runtime_a0) =
        make_stargate_runtime_with_shared_discovery_and_remote_watch_urls(
            "test-global-cal-a-0",
            region_a.clone(),
            vec![grpc_b0.to_string()],
        );
    let handle_a0 = runtime_a0.start().await.expect("region A stargate failed");

    let (grpc_b1, http_b1, runtime_b1) =
        make_stargate_runtime_with_shared_discovery_and_remote_watch_urls(
            "test-global-cal-b-1",
            region_b.clone(),
            vec![grpc_a0.to_string()],
        );
    let handle_b1 = runtime_b1
        .start()
        .await
        .expect("region B stargate 1 failed");

    let (backend_a, quic_url_a, _tunnel_a) = start_counting_dummy_inst("model-global-cal").await;
    let (backend_b, quic_url_b, _tunnel_b) = start_counting_dummy_inst("model-global-cal").await;

    let bringup = BringupConfig {
        enabled: true,
        active_canary_interval: Duration::ZERO,
        calibration_requests: 5,
        calibration_prompt_units: 256,
        calibration_max_concurrency: 1,
        calibration_timeout: Duration::from_secs(5),
        ..BringupConfig::default()
    };

    let mut reg_a = InferenceServerRegistrationClient::default();
    let _channels_a = reg_a
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_a0.to_string()],
                inference_server_id: "global-cal-backend-a".to_string(),
                cluster_id: "global-cal-cluster".to_string(),
                inference_server_url: quic_url_a,
                upstream_http_base_url: Some(format!("http://{}", backend_a.addr)),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
                bringup: bringup.clone(),
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
            },
            vec!["model-global-cal".to_string()],
        )
        .expect("registration A failed");

    let mut reg_b = InferenceServerRegistrationClient::default();
    let _channels_b = reg_b
        .start(
            InferenceServerRegistrationConfig {
                seeds: vec![grpc_b1.to_string()],
                inference_server_id: "global-cal-backend-b".to_string(),
                cluster_id: "global-cal-cluster".to_string(),
                inference_server_url: quic_url_b,
                upstream_http_base_url: Some(format!("http://{}", backend_b.addr)),
                min_update_interval: Duration::from_millis(100),
                status: InferenceServerStatus::Active,
                reverse_tunnel: false,
                quic_insecure: true,
                tunnel_protocol: Default::default(),
                bringup,
                output_token_parser_factory: OutputTokenParserFactory::vllm(),
                request_observation_tx: None,
                request_quality_monitor: pylon_lib::RequestQualityMonitorConfig::default(),
                metrics: None,
                retry: pylon_lib::PylonRetryConfig::default(),
                queue_mismatch_retry: pylon_lib::PylonQueueMismatchRetryConfig::default(),
                queue_tracker: pylon_lib::QueueAdmissionTracker::default(),
                auth_token_provider: None,
            },
            vec!["model-global-cal".to_string()],
        )
        .expect("registration B failed");

    for http_addr in [http_a0, http_b0, http_b1] {
        wait_for_routing(http_addr, "model-global-cal", Duration::from_secs(20)).await;
    }

    let calibration_requests =
        backend_a.bringup_chat_requests() + backend_b.bringup_chat_requests();
    assert_eq!(
        calibration_requests, 15,
        "each of the three Stargates must obtain its own five-request local calibration result"
    );
    assert!(
        backend_a.proxy_chat_requests() + backend_b.proxy_chat_requests() > 0,
        "routing probes should have reached at least one backend"
    );

    reg_a.stop();
    reg_b.stop();
    handle_a0.begin_shutdown();
    handle_b0.begin_shutdown();
    handle_b1.begin_shutdown();
    handle_a0.wait_for_shutdown(Duration::from_secs(5)).await;
    handle_b0.wait_for_shutdown(Duration::from_secs(5)).await;
    handle_b1.wait_for_shutdown(Duration::from_secs(5)).await;
}

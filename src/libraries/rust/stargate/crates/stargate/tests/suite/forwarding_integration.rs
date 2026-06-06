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

// Integration tests for backend-facing forwarding across two stargates.
// Registration fanout gives each stargate local routing state; HTTP proxy
// requests themselves remain local and are never forwarded between peers.
// Matrix: {1,2} backends x {direct-quic, reverse-tunnel} x 2 stargates.

use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::common::{
    BackendHandle, MapResolver, assert_model_routing, base_config, bind_ephemeral,
    bind_ephemeral_udp, init_crypto, start_and_register_backend, wait_for_routing,
    wait_for_unroutable, with_proxy_headers,
};
use stargate::runtime::{StargateHandle, StargateRuntime};
use stargate_forwarding::ForwardingResolver;
use stargate_proto::pb::StargateInfo;

// ---------------------------------------------------------------------------
// Shared test scaffolding
// ---------------------------------------------------------------------------

struct TwoStargates {
    grpc_a: SocketAddr,
    http_a: SocketAddr,
    grpc_b: SocketAddr,
    http_b: SocketAddr,
    handle_a: StargateHandle,
    handle_b: StargateHandle,
}

impl TwoStargates {
    fn seeds_a(&self) -> Vec<String> {
        vec![self.grpc_a.to_string()]
    }

    fn seeds_b(&self) -> Vec<String> {
        vec![self.grpc_b.to_string()]
    }

    fn http_addrs(&self) -> Vec<SocketAddr> {
        vec![self.http_a, self.http_b]
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

/// Two stargates with backend-facing gRPC forwarding (no reverse tunnel).
async fn two_stargates_direct_quic() -> TwoStargates {
    let (grpc_a, grpc_listener_a) = bind_ephemeral();
    let (grpc_b, grpc_listener_b) = bind_ephemeral();
    let (http_a, http_listener_a) = bind_ephemeral();
    let (http_b, http_listener_b) = bind_ephemeral();

    let peers = Arc::new(Mutex::new(Vec::<StargateInfo>::new()));

    let mut resolver_a = MapResolver::new("sg-a");
    resolver_a.insert("sg-b", grpc_b);
    let resolver_a: Arc<dyn ForwardingResolver> = Arc::new(resolver_a);

    let mut resolver_b = MapResolver::new("sg-b");
    resolver_b.insert("sg-a", grpc_a);
    let resolver_b: Arc<dyn ForwardingResolver> = Arc::new(resolver_b);

    let discovery_a = crate::common::SharedDiscovery::new("sg-a", grpc_a, http_a, peers.clone());
    let mut config_a = base_config("sg-a", grpc_a, http_a);
    config_a.dns_poll_interval = Duration::from_secs(1);
    let runtime_a = StargateRuntime::new(config_a, Box::new(discovery_a))
        .with_forwarding(resolver_a)
        .with_grpc_listener(grpc_listener_a)
        .with_http_listener(http_listener_a);

    let discovery_b = crate::common::SharedDiscovery::new("sg-b", grpc_b, http_b, peers.clone());
    let mut config_b = base_config("sg-b", grpc_b, http_b);
    config_b.dns_poll_interval = Duration::from_secs(1);
    let runtime_b = StargateRuntime::new(config_b, Box::new(discovery_b))
        .with_forwarding(resolver_b)
        .with_grpc_listener(grpc_listener_b)
        .with_http_listener(http_listener_b);

    let handle_a = runtime_a.start().await.expect("stargate A failed");
    let handle_b = runtime_b.start().await.expect("stargate B failed");

    crate::common::wait_for_healthy(http_a, Duration::from_secs(5)).await;
    crate::common::wait_for_healthy(http_b, Duration::from_secs(5)).await;

    TwoStargates {
        grpc_a,
        http_a,
        grpc_b,
        http_b,
        handle_a,
        handle_b,
    }
}

/// Two stargates with backend-facing gRPC and QUIC forwarding (reverse tunnel enabled).
async fn two_stargates_reverse_tunnel() -> TwoStargates {
    let (grpc_a, grpc_listener_a) = bind_ephemeral();
    let (grpc_b, grpc_listener_b) = bind_ephemeral();
    let (http_a, http_listener_a) = bind_ephemeral();
    let (http_b, http_listener_b) = bind_ephemeral();
    let (reverse_a, reverse_socket_a) = bind_ephemeral_udp();
    let (reverse_b, reverse_socket_b) = bind_ephemeral_udp();

    let peers = Arc::new(Mutex::new(Vec::<StargateInfo>::new()));

    let mut resolver_a = MapResolver::new("sg-a.stargate.external");
    resolver_a.insert("sg-b", grpc_b);
    resolver_a.insert("sg-b.stargate.external", reverse_b);
    let resolver_a: Arc<dyn ForwardingResolver> = Arc::new(resolver_a);

    let mut resolver_b = MapResolver::new("sg-b.stargate.external");
    resolver_b.insert("sg-a", grpc_a);
    resolver_b.insert("sg-a.stargate.external", reverse_a);
    let resolver_b: Arc<dyn ForwardingResolver> = Arc::new(resolver_b);

    let discovery_a = crate::common::SharedDiscovery::new("sg-a", grpc_a, http_a, peers.clone());
    let mut config_a = base_config("sg-a", grpc_a, http_a);
    config_a.dns_poll_interval = Duration::from_secs(1);
    config_a.advertised_hostname_template = Some("localhost".to_string());
    config_a.reverse_tunnel_listen_addr = Some(reverse_a);
    let runtime_a = StargateRuntime::new(config_a, Box::new(discovery_a))
        .with_forwarding(resolver_a)
        .with_grpc_listener(grpc_listener_a)
        .with_http_listener(http_listener_a)
        .with_reverse_tunnel_socket(reverse_socket_a);

    let discovery_b = crate::common::SharedDiscovery::new("sg-b", grpc_b, http_b, peers.clone());
    let mut config_b = base_config("sg-b", grpc_b, http_b);
    config_b.dns_poll_interval = Duration::from_secs(1);
    config_b.advertised_hostname_template = Some("localhost".to_string());
    config_b.reverse_tunnel_listen_addr = Some(reverse_b);
    let runtime_b = StargateRuntime::new(config_b, Box::new(discovery_b))
        .with_forwarding(resolver_b)
        .with_grpc_listener(grpc_listener_b)
        .with_http_listener(http_listener_b)
        .with_reverse_tunnel_socket(reverse_socket_b);

    let handle_a = runtime_a.start().await.expect("stargate A failed");
    let handle_b = runtime_b.start().await.expect("stargate B failed");

    crate::common::wait_for_healthy(http_a, Duration::from_secs(5)).await;
    crate::common::wait_for_healthy(http_b, Duration::from_secs(5)).await;

    TwoStargates {
        grpc_a,
        http_a,
        grpc_b,
        http_b,
        handle_a,
        handle_b,
    }
}

async fn stop_backends(mut backends: Vec<BackendHandle>) {
    for b in &mut backends {
        b.stop();
    }
}

/// 1 direct-QUIC backend seeded through one stargate; registration fanout makes
/// it locally routable on both stargates after gRPC discovery/forwarding.
#[tokio::test]
async fn direct_quic_1_backend_2_stargates() {
    init_crypto();
    let sg = two_stargates_direct_quic().await;

    let b = start_and_register_backend(&sg.seeds_a(), "backend-1", "model-1", false).await;

    for addr in &sg.http_addrs() {
        wait_for_routing(*addr, "model-1", Duration::from_secs(15)).await;
    }
    assert_model_routing(&sg.http_addrs(), &[("model-1", "backend-1")], 3).await;

    stop_backends(vec![b]).await;
    sg.shutdown().await;
}

/// 2 direct-QUIC backends seeded through different stargates; registration
/// fanout makes each locally routable on both stargates.
#[tokio::test]
async fn direct_quic_2_backends_2_stargates() {
    init_crypto();
    let sg = two_stargates_direct_quic().await;

    let b1 = start_and_register_backend(&sg.seeds_a(), "backend-1", "model-1", false).await;
    let b2 = start_and_register_backend(&sg.seeds_b(), "backend-2", "model-2", false).await;

    for addr in &sg.http_addrs() {
        wait_for_routing(*addr, "model-1", Duration::from_secs(15)).await;
        wait_for_routing(*addr, "model-2", Duration::from_secs(15)).await;
    }
    assert_model_routing(
        &sg.http_addrs(),
        &[("model-1", "backend-1"), ("model-2", "backend-2")],
        3,
    )
    .await;

    stop_backends(vec![b1, b2]).await;
    sg.shutdown().await;
}

/// 1 reverse-tunnel backend seeded through one stargate; registration and
/// reverse-tunnel fanout make it locally routable on both stargates.
#[tokio::test]
async fn reverse_tunnel_1_backend_2_stargates() {
    init_crypto();
    let sg = two_stargates_reverse_tunnel().await;

    let b = start_and_register_backend(&sg.seeds_a(), "backend-1", "model-1", true).await;

    for addr in &sg.http_addrs() {
        wait_for_routing(*addr, "model-1", Duration::from_secs(15)).await;
    }
    assert_model_routing(&sg.http_addrs(), &[("model-1", "backend-1")], 3).await;

    stop_backends(vec![b]).await;
    sg.shutdown().await;
}

/// 2 reverse-tunnel backends seeded through different stargates; fanout makes
/// each locally routable on both stargates.
#[tokio::test]
async fn reverse_tunnel_2_backends_2_stargates() {
    init_crypto();
    let sg = two_stargates_reverse_tunnel().await;

    let b1 = start_and_register_backend(&sg.seeds_a(), "backend-1", "model-1", true).await;
    let b2 = start_and_register_backend(&sg.seeds_b(), "backend-2", "model-2", true).await;

    for addr in &sg.http_addrs() {
        wait_for_routing(*addr, "model-1", Duration::from_secs(15)).await;
        wait_for_routing(*addr, "model-2", Duration::from_secs(15)).await;
    }
    assert_model_routing(
        &sg.http_addrs(),
        &[("model-1", "backend-1"), ("model-2", "backend-2")],
        3,
    )
    .await;

    stop_backends(vec![b1, b2]).await;
    sg.shutdown().await;
}

/// Unknown model returns 404 on both stargates (no backends registered).
#[tokio::test]
async fn unknown_model_returns_404_through_forwarding() {
    init_crypto();
    let sg = two_stargates_direct_quic().await;

    let http_client = reqwest::Client::new();
    for addr in sg.http_addrs() {
        let resp = with_proxy_headers(
            http_client.post(format!("http://{addr}/v1/chat/completions")),
            "nonexistent",
            "req-unknown-integ",
        )
        .header("content-type", "application/json")
        .json(&serde_json::json!({
            "model": "nonexistent",
            "messages": [{"role": "user", "content": "hi"}],
            "stream": true,
        }))
        .send()
        .await
        .expect("request failed");
        assert_eq!(resp.status(), 404, "unknown model should 404 on {addr}");
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

/// Missing x-model header returns 400 on both stargates.
#[tokio::test]
async fn missing_headers_returns_400_through_forwarding() {
    init_crypto();
    let sg = two_stargates_direct_quic().await;

    let http_client = reqwest::Client::new();
    for addr in sg.http_addrs() {
        let resp = http_client
            .post(format!("http://{addr}/v1/chat/completions"))
            .header("x-request-id", "req-noheader-integ")
            .header("x-input-tokens", "1")
            .header("content-type", "application/json")
            .json(&serde_json::json!({
                "model": "any",
                "messages": [{"role": "user", "content": "hi"}],
                "stream": true,
            }))
            .send()
            .await
            .expect("request failed");
        assert_eq!(resp.status(), 400, "missing x-model should 400 on {addr}");
    }

    sg.shutdown().await;
}

/// Backend disconnect removes routing on both stargates.
#[tokio::test]
async fn backend_disconnect_removes_routing_on_both_stargates() {
    init_crypto();
    let sg = two_stargates_direct_quic().await;

    let b = start_and_register_backend(&sg.seeds_a(), "dc-backend", "dc-model", false).await;

    for addr in &sg.http_addrs() {
        wait_for_routing(*addr, "dc-model", Duration::from_secs(15)).await;
    }
    assert_model_routing(&sg.http_addrs(), &[("dc-model", "dc-backend")], 2).await;

    stop_backends(vec![b]).await;

    for addr in &sg.http_addrs() {
        wait_for_unroutable(*addr, "dc-model", Duration::from_secs(15)).await;
    }

    sg.shutdown().await;
}

/// Non-streaming request through direct-QUIC forwarding returns 400.
#[tokio::test]
async fn non_streaming_rejected_through_direct_quic_forwarding() {
    init_crypto();
    let sg = two_stargates_direct_quic().await;

    let b = start_and_register_backend(&sg.seeds_a(), "ns-backend", "ns-model", false).await;

    for addr in &sg.http_addrs() {
        wait_for_routing(*addr, "ns-model", Duration::from_secs(15)).await;
    }

    let http_client = reqwest::Client::new();
    for addr in sg.http_addrs() {
        let resp = with_proxy_headers(
            http_client.post(format!("http://{addr}/v1/chat/completions")),
            "ns-model",
            "req-nonstream-integ",
        )
        .header("content-type", "application/json")
        .json(&serde_json::json!({
            "model": "ns-model",
            "messages": [{"role": "user", "content": "hi"}],
        }))
        .send()
        .await
        .expect("request failed");
        assert_eq!(
            resp.status(),
            400,
            "non-streaming should be rejected on {addr}"
        );
    }

    stop_backends(vec![b]).await;
    sg.shutdown().await;
}

/// Non-streaming request through reverse-tunnel forwarding returns 400.
#[tokio::test]
async fn non_streaming_rejected_through_reverse_tunnel_forwarding() {
    init_crypto();
    let sg = two_stargates_reverse_tunnel().await;

    let b = start_and_register_backend(&sg.seeds_a(), "ns-rt-backend", "ns-rt-model", true).await;

    for addr in &sg.http_addrs() {
        wait_for_routing(*addr, "ns-rt-model", Duration::from_secs(15)).await;
    }

    let http_client = reqwest::Client::new();
    for addr in sg.http_addrs() {
        let resp = with_proxy_headers(
            http_client.post(format!("http://{addr}/v1/chat/completions")),
            "ns-rt-model",
            "req-nonstream-rt-integ",
        )
        .header("content-type", "application/json")
        .json(&serde_json::json!({
            "model": "ns-rt-model",
            "messages": [{"role": "user", "content": "hi"}],
        }))
        .send()
        .await
        .expect("request failed");
        assert_eq!(
            resp.status(),
            400,
            "non-streaming through reverse tunnel should be rejected on {addr}"
        );
    }

    stop_backends(vec![b]).await;
    sg.shutdown().await;
}

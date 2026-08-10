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
use std::sync::Arc;
use std::time::Duration;

use crate::common::{
    TokenMapAuthenticator, bind_ephemeral_udp,
    direct_registration_config as base_direct_registration_config, init_crypto,
    make_stargate_runtime, make_stargate_runtime_with_auth,
    make_stargate_runtime_with_reverse_and_auth,
    reverse_registration_config as base_reverse_registration_config, start_dummy_backend,
    start_dummy_inst, wait_for_routing, wait_for_routing_with_rk, with_proxy_headers,
    with_proxy_headers_rk,
};
use pylon_lib::{
    AuthTokenProvider, InferenceServerRegistrationClient, InferenceServerRegistrationConfig,
};
use stargate_proto::pb::InferenceServerStatus;

fn reverse_registration_config(
    grpc_addr: SocketAddr,
    inference_server_id: &str,
    backend_addr: SocketAddr,
    auth_token: &str,
    model_id: &str,
) -> InferenceServerRegistrationConfig {
    InferenceServerRegistrationConfig {
        auth_token_provider: Some(Arc::new(AuthTokenProvider::Static(auth_token.to_string()))),
        ..base_reverse_registration_config(
            vec![grpc_addr.to_string()],
            inference_server_id,
            format!("http://{backend_addr}"),
            pylon_lib::PylonRuntimeState::new(
                InferenceServerStatus::Active,
                &[model_id.to_string()],
            ),
        )
    }
}

fn direct_registration_config(
    grpc_addr: SocketAddr,
    inference_server_id: &str,
    inference_server_url: String,
    upstream_addr: SocketAddr,
    model_id: &str,
    auth_token: Option<&str>,
) -> InferenceServerRegistrationConfig {
    InferenceServerRegistrationConfig {
        auth_token_provider: auth_token
            .map(|token| Arc::new(AuthTokenProvider::Static(token.to_string()))),
        ..base_direct_registration_config(
            vec![grpc_addr.to_string()],
            inference_server_id,
            inference_server_url,
            format!("http://{upstream_addr}"),
            pylon_lib::PylonRuntimeState::new(
                InferenceServerStatus::Active,
                &[model_id.to_string()],
            ),
        )
    }
}

async fn assert_no_eligible_candidates(resp: reqwest::Response, context: &str) {
    assert_eq!(
        resp.status(),
        404,
        "{context} should get 404 because no routing target matches"
    );
    assert_eq!(
        resp.headers()
            .get("x-stargate-error-code")
            .and_then(|value| value.to_str().ok()),
        Some("no_eligible_candidates"),
        "{context} should return the no-candidates error code"
    );
    let _ = resp.bytes().await;
}

struct RoutingClient {
    client: reqwest::Client,
    url: String,
    model: &'static str,
}

impl RoutingClient {
    fn new(http_addr: SocketAddr, model: &'static str) -> Self {
        Self {
            client: reqwest::Client::new(),
            url: format!("http://{http_addr}/v1/chat/completions"),
            model,
        }
    }

    async fn send(&self, routing_key: Option<&str>, request_id: &str) -> reqwest::Response {
        let request = self.client.post(&self.url);
        let request = match routing_key {
            Some(routing_key) => {
                with_proxy_headers_rk(request, routing_key, self.model, request_id)
            }
            None => with_proxy_headers(request, self.model, request_id),
        };
        request
            .header("content-type", "application/json")
            .json(&serde_json::json!({
                "model": self.model,
                "messages": [{"role": "user", "content": "hi"}],
                "stream": true,
            }))
            .send()
            .await
            .expect("request failed")
    }
}

async fn assert_routes_to(resp: reqwest::Response, expected_backend: &str, context: &str) {
    assert_eq!(resp.status(), 200, "{context}");
    assert_eq!(
        resp.headers()
            .get("x-inference-server-id")
            .and_then(|value| value.to_str().ok()),
        Some(expected_backend),
        "{context}"
    );
    let _ = resp.bytes().await;
}

/// With OpenAuthenticator (default), requests without `x-routing-key` route
/// successfully through the None routing key path.
#[tokio::test]
async fn no_routing_key_header_routes_via_open_authenticator() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-no-rk");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("no-rk-model").await;

    let mut reg = InferenceServerRegistrationClient::default();
    reg.start(direct_registration_config(
        grpc_addr,
        "no-rk-inst",
        quic_url,
        inst_addr,
        "no-rk-model",
        None,
    ))
    .expect("registration failed");

    wait_for_routing(http_addr, "no-rk-model", Duration::from_secs(5)).await;

    assert_routes_to(
        RoutingClient::new(http_addr, "no-rk-model")
            .send(None, "req-no-rk")
            .await,
        "no-rk-inst",
        "request without a routing key should use the open-authenticator backend",
    )
    .await;

    reg.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// Reverse-tunnel registrations are scoped by the authenticated routing key,
/// just like direct tunnel registrations.
#[tokio::test]
async fn reverse_tunnel_routing_keys_isolate_same_model_backends() {
    init_crypto();

    let auth = Arc::new(TokenMapAuthenticator::new([
        ("token-rt-a", "rk-rt-a"),
        ("token-rt-b", "rk-rt-b"),
    ]));
    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse_and_auth(
        "test-sg-rt-rk-iso",
        reverse_addr,
        Some(reverse_socket),
        auth,
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr_a = start_dummy_backend("rt-rk-iso-model").await;
    let backend_addr_b = start_dummy_backend("rt-rk-iso-model").await;

    let mut reg_a = InferenceServerRegistrationClient::default();
    reg_a
        .start(reverse_registration_config(
            grpc_addr,
            "rt-rk-iso-a",
            backend_addr_a,
            "token-rt-a",
            "rt-rk-iso-model",
        ))
        .expect("registration a failed");

    let mut reg_b = InferenceServerRegistrationClient::default();
    reg_b
        .start(reverse_registration_config(
            grpc_addr,
            "rt-rk-iso-b",
            backend_addr_b,
            "token-rt-b",
            "rt-rk-iso-model",
        ))
        .expect("registration b failed");

    wait_for_routing_with_rk(
        http_addr,
        Some("rk-rt-a"),
        "rt-rk-iso-model",
        Duration::from_secs(10),
    )
    .await;
    wait_for_routing_with_rk(
        http_addr,
        Some("rk-rt-b"),
        "rt-rk-iso-model",
        Duration::from_secs(10),
    )
    .await;

    let client = RoutingClient::new(http_addr, "rt-rk-iso-model");

    for (routing_key, expected_inst, request_prefix) in [
        ("rk-rt-a", "rt-rk-iso-a", "req-rt-rk-a"),
        ("rk-rt-b", "rt-rk-iso-b", "req-rt-rk-b"),
    ] {
        for i in 0..4 {
            assert_routes_to(
                client
                    .send(Some(routing_key), &format!("{request_prefix}-{i}"))
                    .await,
                expected_inst,
                &format!(
                    "{routing_key} should only route to its authenticated reverse-tunnel backend"
                ),
            )
            .await;
        }
    }

    reg_a.stop();
    reg_b.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// Wrong and omitted routing keys must fail before they can reach a reverse
/// tunnel backend registered under another routing identity.
#[tokio::test]
async fn reverse_tunnel_wrong_or_omitted_routing_key_returns_404_no_eligible_candidates() {
    init_crypto();

    let auth = Arc::new(TokenMapAuthenticator::new([("token-rt-x", "rk-rt-x")]));
    let (reverse_addr, reverse_socket) = bind_ephemeral_udp();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_reverse_and_auth(
        "test-sg-rt-rk-miss",
        reverse_addr,
        Some(reverse_socket),
        auth,
    );
    let handle = runtime.start().await.expect("stargate failed to start");

    let backend_addr = start_dummy_backend("rt-rk-miss-model").await;

    let mut reg = InferenceServerRegistrationClient::default();
    reg.start(reverse_registration_config(
        grpc_addr,
        "rt-rk-miss-inst",
        backend_addr,
        "token-rt-x",
        "rt-rk-miss-model",
    ))
    .expect("registration failed");

    wait_for_routing_with_rk(
        http_addr,
        Some("rk-rt-x"),
        "rt-rk-miss-model",
        Duration::from_secs(10),
    )
    .await;

    let client = RoutingClient::new(http_addr, "rt-rk-miss-model");
    assert_no_eligible_candidates(
        client.send(Some("rk-rt-wrong"), "req-rt-rk-wrong").await,
        "wrong routing key",
    )
    .await;
    assert_no_eligible_candidates(
        client.send(None, "req-rt-rk-omitted").await,
        "omitted routing key",
    )
    .await;

    reg.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// With a TokenMapAuthenticator, a request with the correct `x-routing-key`
/// header reaches the backend registered under that routing key.
#[tokio::test]
async fn routing_key_header_routes_to_correct_backend() {
    init_crypto();

    let auth = Arc::new(TokenMapAuthenticator::new([("token-alpha", "rk-alpha")]));
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_auth("test-sg-rk-match", auth);
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("rk-model").await;

    let mut reg = InferenceServerRegistrationClient::default();
    reg.start(direct_registration_config(
        grpc_addr,
        "rk-match-inst",
        quic_url,
        inst_addr,
        "rk-model",
        Some("token-alpha"),
    ))
    .expect("registration failed");

    wait_for_routing_with_rk(
        http_addr,
        Some("rk-alpha"),
        "rk-model",
        Duration::from_secs(5),
    )
    .await;

    assert_routes_to(
        RoutingClient::new(http_addr, "rk-model")
            .send(Some("rk-alpha"), "req-rk-match")
            .await,
        "rk-match-inst",
        "matching routing key should reach its backend",
    )
    .await;

    reg.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// A request with a routing key that has no registered backends returns 404.
#[tokio::test]
async fn wrong_routing_key_returns_404_no_eligible_candidates() {
    init_crypto();

    let auth = Arc::new(TokenMapAuthenticator::new([("token-a", "rk-a")]));
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_auth("test-sg-wrong-rk", auth);
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("rk404-model").await;

    let mut reg = InferenceServerRegistrationClient::default();
    reg.start(direct_registration_config(
        grpc_addr,
        "rk404-inst",
        quic_url,
        inst_addr,
        "rk404-model",
        Some("token-a"),
    ))
    .expect("registration failed");

    wait_for_routing_with_rk(
        http_addr,
        Some("rk-a"),
        "rk404-model",
        Duration::from_secs(5),
    )
    .await;

    assert_no_eligible_candidates(
        RoutingClient::new(http_addr, "rk404-model")
            .send(Some("rk-wrong"), "req-wrong-rk")
            .await,
        "wrong routing key",
    )
    .await;

    reg.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// Two backends registered under different routing keys for the same model are
/// isolated: each routing key only reaches its own backend.
#[tokio::test]
async fn different_routing_keys_isolate_backends() {
    init_crypto();

    let auth = Arc::new(TokenMapAuthenticator::new([
        ("token-a", "rk-a"),
        ("token-b", "rk-b"),
    ]));
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_auth("test-sg-rk-iso", auth);
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr_a, quic_url_a, _tunnel_a) = start_dummy_inst("iso-model").await;
    let (inst_addr_b, quic_url_b, _tunnel_b) = start_dummy_inst("iso-model").await;

    let mut reg_a = InferenceServerRegistrationClient::default();
    reg_a
        .start(direct_registration_config(
            grpc_addr,
            "iso-inst-a",
            quic_url_a,
            inst_addr_a,
            "iso-model",
            Some("token-a"),
        ))
        .expect("registration failed");

    let mut reg_b = InferenceServerRegistrationClient::default();
    reg_b
        .start(direct_registration_config(
            grpc_addr,
            "iso-inst-b",
            quic_url_b,
            inst_addr_b,
            "iso-model",
            Some("token-b"),
        ))
        .expect("registration failed");

    wait_for_routing_with_rk(http_addr, Some("rk-a"), "iso-model", Duration::from_secs(5)).await;
    wait_for_routing_with_rk(http_addr, Some("rk-b"), "iso-model", Duration::from_secs(5)).await;

    let client = RoutingClient::new(http_addr, "iso-model");
    for (routing_key, expected_backend, request_prefix) in [
        ("rk-a", "iso-inst-a", "req-iso-a"),
        ("rk-b", "iso-inst-b", "req-iso-b"),
    ] {
        for i in 0..5 {
            assert_routes_to(
                client
                    .send(Some(routing_key), &format!("{request_prefix}-{i}"))
                    .await,
                expected_backend,
                &format!("{routing_key} should always route to {expected_backend}"),
            )
            .await;
        }
    }

    reg_a.stop();
    reg_b.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// A request without `x-routing-key` does NOT match backends registered under
/// a non-None routing key, returning 404.
#[tokio::test]
async fn omitted_routing_key_does_not_match_keyed_backend() {
    init_crypto();

    let auth = Arc::new(TokenMapAuthenticator::new([("token-x", "rk-x")]));
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_auth("test-sg-rk-omit", auth);
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("omit-model").await;

    let mut reg = InferenceServerRegistrationClient::default();
    reg.start(direct_registration_config(
        grpc_addr,
        "omit-inst",
        quic_url,
        inst_addr,
        "omit-model",
        Some("token-x"),
    ))
    .expect("registration failed");

    wait_for_routing_with_rk(
        http_addr,
        Some("rk-x"),
        "omit-model",
        Duration::from_secs(5),
    )
    .await;

    assert_no_eligible_candidates(
        RoutingClient::new(http_addr, "omit-model")
            .send(None, "req-omit-rk")
            .await,
        "request without a routing key",
    )
    .await;

    reg.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

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

use std::time::Duration;

use crate::common::{
    init_crypto, make_stargate_runtime, make_stargate_runtime_with_readiness_warmup,
    start_and_register_backend, wait_for_routing,
};

#[tokio::test]
async fn healthz_returns_200() {
    init_crypto();

    let (_grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-healthz");
    let handle = runtime.start().await.expect("stargate failed to start");

    let http_client = reqwest::Client::new();
    let resp = http_client
        .get(format!("http://{http_addr}/healthz"))
        .send()
        .await
        .expect("healthz request failed");
    assert_eq!(resp.status(), 200);

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn readyz_returns_200_when_warmup_is_disabled() {
    init_crypto();

    let (_grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_readiness_warmup("test-sg-readyz-no-warmup", Duration::ZERO);
    let handle = runtime.start().await.expect("stargate failed to start");

    let response = reqwest::Client::new()
        .get(format!("http://{http_addr}/readyz"))
        .send()
        .await
        .expect("readyz request failed");
    assert_eq!(response.status(), 200);

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn readyz_returns_503_during_warmup() {
    init_crypto();

    let (_grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-readyz");
    let handle = runtime.start().await.expect("stargate failed to start");

    let http_client = reqwest::Client::new();
    let resp = http_client
        .get(format!("http://{http_addr}/readyz"))
        .send()
        .await
        .expect("readyz request failed");
    assert_eq!(resp.status(), 503);

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn pylon_registration_succeeds_during_readiness_warmup() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-warmup-registration");
    let handle = runtime.start().await.expect("stargate failed to start");

    let mut backend = start_and_register_backend(
        &[grpc_addr.to_string()],
        "warmup-backend",
        "warmup-model",
        false,
    )
    .await;
    wait_for_routing(http_addr, "warmup-model", Duration::from_secs(5)).await;

    let response = reqwest::Client::new()
        .get(format!("http://{http_addr}/readyz"))
        .send()
        .await
        .expect("readyz request failed");
    assert_eq!(response.status(), 503);

    backend.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

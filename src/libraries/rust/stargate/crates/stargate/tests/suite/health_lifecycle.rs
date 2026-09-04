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
use std::time::Duration;

use stargate::runtime::{BoundStargateListeners, StargateRuntime, StargateRuntimeConfig, WarmupConfig};

use crate::common::{SelfDiscovery, base_config, init_crypto, make_stargate_runtime};

fn make_stargate_runtime_with_warmup(
    id: &str,
    warmup: WarmupConfig,
) -> (SocketAddr, SocketAddr, StargateRuntime) {
    let ephemeral: SocketAddr = "127.0.0.1:0".parse().unwrap();
    let mut config: StargateRuntimeConfig = base_config(id, ephemeral, ephemeral);
    config.warmup = warmup;
    let listeners =
        BoundStargateListeners::bind(&mut config).expect("test listeners should bind");
    let grpc_addr = config.grpc_listen_addr;
    let http_addr = config.http_listen_addr;
    let discovery = Box::new(SelfDiscovery::new(id, grpc_addr, http_addr));
    (
        grpc_addr,
        http_addr,
        StargateRuntime::new(config, discovery, listeners, None),
    )
}

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

    let warmup = WarmupConfig {
        warmup_duration: Duration::ZERO,
        sample_interval: Duration::from_millis(50),
        stabilization_window: 5,
    };
    let (_grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_warmup("test-sg-readyz-no-warmup", warmup);
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

/// Readiness warmup keeps `/readyz` unavailable until the fixed window elapses.
#[tokio::test]
async fn readyz_returns_503_during_warmup_window() {
    init_crypto();

    let warmup = WarmupConfig {
        warmup_duration: Duration::from_millis(300),
        sample_interval: Duration::from_millis(50),
        stabilization_window: 100, // very high: stabilization cannot fire, only timeout
    };
    let (_grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_warmup("test-sg-readyz-warmup-503", warmup);
    let handle = runtime.start().await.expect("stargate failed to start");

    let http_client = reqwest::Client::new();

    // During the warmup window the replica should be not-ready.
    let resp = http_client
        .get(format!("http://{http_addr}/readyz"))
        .send()
        .await
        .expect("readyz request during warmup failed");
    assert_eq!(
        resp.status(),
        503,
        "readyz should return 503 during warmup window"
    );

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

/// Readiness warmup fixed window promotes the replica to ready once it elapses,
/// even when no backends register during that window.
#[tokio::test]
async fn readyz_promotes_to_ready_after_warmup_window_elapses() {
    init_crypto();

    let warmup = WarmupConfig {
        warmup_duration: Duration::from_millis(200),
        sample_interval: Duration::from_millis(50),
        stabilization_window: 100, // very high: stabilization cannot fire, only timeout
    };
    let (_grpc_addr, http_addr, runtime) =
        make_stargate_runtime_with_warmup("test-sg-readyz-warmup-promote", warmup);
    let handle = runtime.start().await.expect("stargate failed to start");

    let http_client = reqwest::Client::new();

    // Wait for the warmup window to elapse with margin.
    tokio::time::sleep(Duration::from_millis(400)).await;

    let resp = http_client
        .get(format!("http://{http_addr}/readyz"))
        .send()
        .await
        .expect("readyz request after warmup failed");
    assert_eq!(
        resp.status(),
        200,
        "readyz should return 200 after warmup window elapses"
    );

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

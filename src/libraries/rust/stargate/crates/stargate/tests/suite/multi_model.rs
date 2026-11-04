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
    direct_registration_config, init_crypto, make_stargate_runtime, start_dummy_inst,
    start_dummy_inst_with_models, wait_for_routing, with_proxy_headers,
};
use pylon_lib::{InferenceServerRegistrationClient, PylonRuntimeState};
use stargate_proto::pb::InferenceServerStatus;

#[tokio::test]
async fn multi_model_single_instance() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-multimodel");
    let handle = runtime.start().await.expect("stargate failed to start");

    let model_ids = ["model-alpha".to_string(), "model-beta".to_string()];
    let (inst_addr, quic_url, _tunnel) =
        start_dummy_inst_with_models("model-alpha", &model_ids).await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    reg_client
        .start(direct_registration_config(
            vec![grpc_addr.to_string()],
            "mm-inst",
            quic_url,
            format!("http://{inst_addr}"),
            PylonRuntimeState::new(InferenceServerStatus::Active, &model_ids),
        ))
        .expect("registration failed");

    wait_for_routing(http_addr, "model-alpha", Duration::from_secs(5)).await;
    wait_for_routing(http_addr, "model-beta", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");

    for model in &["model-alpha", "model-beta"] {
        let body = serde_json::json!({
            "model": model,
            "messages": [{"role": "user", "content": "hi"}],
            "stream": true,
        });
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            model,
            &format!("req-mm-{model}"),
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
        assert_eq!(resp.status(), 200, "model {model} should be routable");
        assert_eq!(
            resp.headers()
                .get("x-inference-server-id")
                .unwrap()
                .to_str()
                .unwrap(),
            "mm-inst",
            "both models should route to the same instance"
        );
    }

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn multiple_instances_different_models() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-diffmodels");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr_x, quic_url_x, _tunnel_x) = start_dummy_inst("model-x").await;
    let (inst_addr_y, quic_url_y, _tunnel_y) = start_dummy_inst("model-y").await;

    let mut reg_client_x = InferenceServerRegistrationClient::default();
    reg_client_x
        .start(direct_registration_config(
            vec![grpc_addr.to_string()],
            "inst-x",
            quic_url_x,
            format!("http://{inst_addr_x}"),
            PylonRuntimeState::new(InferenceServerStatus::Active, &["model-x".to_string()]),
        ))
        .expect("registration failed");

    let mut reg_client_y = InferenceServerRegistrationClient::default();
    reg_client_y
        .start(direct_registration_config(
            vec![grpc_addr.to_string()],
            "inst-y",
            quic_url_y,
            format!("http://{inst_addr_y}"),
            PylonRuntimeState::new(InferenceServerStatus::Active, &["model-y".to_string()]),
        ))
        .expect("registration failed");

    wait_for_routing(http_addr, "model-x", Duration::from_secs(5)).await;
    wait_for_routing(http_addr, "model-y", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");

    for (model, expected_inst) in &[("model-x", "inst-x"), ("model-y", "inst-y")] {
        let body = serde_json::json!({
            "model": model,
            "messages": [{"role": "user", "content": "hi"}],
            "stream": true,
        });
        let resp = with_proxy_headers(
            http_client.post(&stargate_url),
            model,
            &format!("req-diff-{model}"),
        )
        .header("content-type", "application/json")
        .json(&body)
        .send()
        .await
        .expect("request failed");
        assert_eq!(resp.status(), 200);
        assert_eq!(
            resp.headers()
                .get("x-inference-server-id")
                .unwrap()
                .to_str()
                .unwrap(),
            *expected_inst,
            "model {model} should route to {expected_inst}"
        );
    }

    reg_client_x.stop();
    reg_client_y.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

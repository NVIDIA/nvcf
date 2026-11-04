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

use crate::common::sse::{assert_sse_done, parse_sse_events};
use crate::common::{
    direct_registration_config, init_crypto, make_stargate_runtime, start_dummy_inst,
    wait_for_routing, with_proxy_headers,
};
use pylon_lib::{InferenceServerRegistrationClient, PylonRuntimeState};
use stargate_proto::pb::InferenceServerStatus;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::{InferenceServerAck, InferenceServerRegistration};
use tonic::Response;
use tonic::transport::Channel;

#[tokio::test]
async fn duplicate_inference_server_id_rejected() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-dup");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("dup-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    reg_client
        .start(direct_registration_config(
            vec![grpc_addr.to_string()],
            "dup-inst",
            quic_url.clone(),
            format!("http://{inst_addr}"),
            PylonRuntimeState::new(InferenceServerStatus::Active, &["dup-model".to_string()]),
        ))
        .expect("registration failed");

    wait_for_routing(http_addr, "dup-model", Duration::from_secs(5)).await;

    // Open a second raw gRPC registration stream with the same inference_server_id
    let endpoint = format!("http://{grpc_addr}");
    let channel = Channel::from_shared(endpoint)
        .expect("invalid endpoint")
        .connect()
        .await
        .expect("connect failed");
    let mut client = StargateControlPlaneClient::new(channel);

    let (tx, rx) = flume::bounded(8);
    tx.send_async(InferenceServerRegistration {
        inference_server_id: "dup-inst".to_string(),
        cluster_id: String::new(),
        inference_server_url: quic_url,
        models: Default::default(),
        reverse_tunnel: false,
    })
    .await
    .expect("send failed");

    let result: Result<Response<tonic::Streaming<InferenceServerAck>>, tonic::Status> =
        client.register_inference_server(rx.into_stream()).await;

    match result {
        Err(status) => {
            assert_eq!(
                status.code(),
                tonic::Code::AlreadyExists,
                "expected ALREADY_EXISTS, got: {status}"
            );
        }
        Ok(resp) => {
            let mut stream = resp.into_inner();
            let msg: Result<Option<InferenceServerAck>, tonic::Status> = stream.message().await;
            match msg {
                Err(status) => {
                    assert_eq!(
                        status.code(),
                        tonic::Code::AlreadyExists,
                        "expected ALREADY_EXISTS on stream, got: {status}"
                    );
                }
                Ok(_) => {
                    panic!("expected duplicate registration to be rejected");
                }
            }
        }
    }

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

#[tokio::test]
async fn concurrent_proxy_requests_all_succeed() {
    init_crypto();

    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-sg-concurrent");
    let handle = runtime.start().await.expect("stargate failed to start");

    let (inst_addr, quic_url, _tunnel) = start_dummy_inst("conc-model").await;

    let mut reg_client = InferenceServerRegistrationClient::default();
    reg_client
        .start(direct_registration_config(
            vec![grpc_addr.to_string()],
            "conc-inst",
            quic_url,
            format!("http://{inst_addr}"),
            PylonRuntimeState::new(InferenceServerStatus::Active, &["conc-model".to_string()]),
        ))
        .expect("registration failed");

    wait_for_routing(http_addr, "conc-model", Duration::from_secs(5)).await;

    let http_client = reqwest::Client::new();
    let stargate_url = format!("http://{http_addr}/v1/chat/completions");

    let mut handles = Vec::new();
    for i in 0..20 {
        let client = http_client.clone();
        let url = stargate_url.clone();
        handles.push(tokio::spawn(async move {
            let body = serde_json::json!({
                "model": "conc-model",
                "messages": [{"role": "user", "content": "hi"}],
                "stream": true,
            });
            let resp =
                with_proxy_headers(client.post(&url), "conc-model", &format!("req-conc-{i}"))
                    .header("content-type", "application/json")
                    .json(&body)
                    .send()
                    .await
                    .expect("concurrent request failed");
            let status = resp.status().as_u16();
            let text = resp.text().await.expect("failed to read body");
            (status, text)
        }));
    }

    for handle in handles {
        let (status, text) = handle.await.expect("task panicked");
        assert_eq!(status, 200, "concurrent request should succeed");
        assert_sse_done(&parse_sse_events(&text).expect("concurrent stream should be valid SSE"));
    }

    reg_client.stop();
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

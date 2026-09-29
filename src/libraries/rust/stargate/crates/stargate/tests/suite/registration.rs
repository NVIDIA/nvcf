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

use crate::common::sse::{assert_sse_done, parse_sse_events};
use crate::common::{
    direct_registration_config, init_crypto, make_stargate_runtime,
    make_stargate_runtime_with_auth, start_dummy_inst, wait_for_routing, with_proxy_headers,
};
use pylon_lib::{InferenceServerRegistrationClient, PylonRuntimeState};
use sha2::Digest;
use stargate::auth::{
    RegistrationAuthFailure, StaticClusterAuthenticator, WorkerAuthReloadOutcome,
};
use stargate_proto::pb::InferenceServerStatus;
use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::{InferenceServerAck, InferenceServerRegistration};
use tonic::Response;
use tonic::transport::Channel;

/// Opens a raw registration stream and returns the router's rejection.
async fn registration_rejection(
    grpc_addr: SocketAddr,
    token: &str,
    cluster_id: &str,
) -> tonic::Status {
    let channel = Channel::from_shared(format!("http://{grpc_addr}"))
        .expect("invalid endpoint")
        .connect()
        .await
        .expect("connect failed");
    let mut client = StargateControlPlaneClient::new(channel);
    let (tx, rx) = flume::bounded(8);
    tx.send_async(InferenceServerRegistration {
        inference_server_id: "static-auth-inst".to_string(),
        cluster_id: cluster_id.to_string(),
        inference_server_url: "quic://127.0.0.1:1".to_string(),
        ..Default::default()
    })
    .await
    .expect("send failed");
    let mut request = tonic::Request::new(rx.into_stream());
    request.metadata_mut().insert(
        "authorization",
        format!("Bearer {token}")
            .parse()
            .expect("bearer header should be ASCII"),
    );

    let status = match client.register_inference_server(request).await {
        Err(status) => status,
        Ok(response) => response
            .into_inner()
            .message()
            .await
            .expect_err("registration should be rejected"),
    };
    drop(tx);
    status
}

fn worker_auth_file(cluster_id: &str, tokens: &[&str]) -> String {
    let mut contents = format!("clusters:\n  {cluster_id}:\n");
    for token in tokens {
        let digest: String = sha2::Sha256::digest(token.as_bytes())
            .iter()
            .map(|byte| format!("{byte:02x}"))
            .collect();
        contents.push_str(&format!("    - sha256:{digest}\n"));
    }
    contents
}

#[tokio::test]
async fn static_cluster_credentials_gate_registration_and_count_failures() {
    init_crypto();
    let dir = tempfile::tempdir().expect("temp dir should be creatable");
    let path = dir.path().join("credentials.yaml");
    std::fs::write(&path, worker_auth_file("cluster-a", &["cluster-a-token"]))
        .expect("worker auth file should be writable");
    let authenticator = StaticClusterAuthenticator::load(&path)
        .await
        .expect("worker auth file should load")
        .with_reload_interval(Duration::from_millis(20));
    let (grpc_addr, _http_addr, runtime) =
        make_stargate_runtime_with_auth("test-sg-static-auth", Arc::new(authenticator));
    let handle = runtime.start().await.expect("stargate failed to start");
    let metrics = handle.metrics();
    let failures = |reason| metrics.registration_auth_failures_total(reason).get();
    let reloads = |outcome| metrics.worker_auth_reloads_total(outcome).get();

    let status = registration_rejection(grpc_addr, "wrong-token", "cluster-a").await;
    assert_eq!(status.code(), tonic::Code::Unauthenticated, "{status}");
    assert_eq!(failures(RegistrationAuthFailure::UnknownCredential), 1);

    let status = registration_rejection(grpc_addr, "", "cluster-a").await;
    assert_eq!(status.code(), tonic::Code::Unauthenticated, "{status}");
    assert_eq!(failures(RegistrationAuthFailure::MissingToken), 1);

    let status = registration_rejection(grpc_addr, "cluster-a-token", "cluster-b").await;
    assert_eq!(status.code(), tonic::Code::InvalidArgument, "{status}");
    assert_eq!(
        status.message(),
        "cluster_id does not match the authenticated credential"
    );
    assert_eq!(failures(RegistrationAuthFailure::ClusterMismatch), 1);

    // The runtime's reload task activates a rotated credential without a
    // restart and keeps the last good set when the file breaks.
    let staged = dir.path().join("staged.yaml");
    std::fs::write(
        &staged,
        worker_auth_file("cluster-a", &["cluster-a-token", "cluster-a-next-token"]),
    )
    .expect("staged file should be writable");
    std::fs::rename(&staged, &path).expect("staged file should replace the live file");
    wait_for(|| reloads(WorkerAuthReloadOutcome::Success) == 1).await;
    let status = registration_rejection(grpc_addr, "cluster-a-next-token", "cluster-b").await;
    assert_eq!(
        status.code(),
        tonic::Code::InvalidArgument,
        "the rotated token must authenticate as cluster-a: {status}"
    );

    std::fs::remove_file(&path).expect("worker auth file should be removable");
    wait_for(|| reloads(WorkerAuthReloadOutcome::Rejected) == 1).await;
    let status = registration_rejection(grpc_addr, "cluster-a-next-token", "cluster-b").await;
    assert_eq!(
        status.code(),
        tonic::Code::InvalidArgument,
        "the last good set must stay active: {status}"
    );
    assert_eq!(failures(RegistrationAuthFailure::UnknownCredential), 1);
    assert_eq!(failures(RegistrationAuthFailure::MissingToken), 1);

    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

async fn wait_for(mut condition: impl FnMut() -> bool) {
    tokio::time::timeout(Duration::from_secs(5), async {
        while !condition() {
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await
    .expect("condition should hold within five seconds");
}

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

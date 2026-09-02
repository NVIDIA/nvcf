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

use pylon_lib::{
    ClientError, CurrentModelStats, DEFAULT_MAX_SSE_BUFFER_BYTES,
    InferenceServerRegistrationClient, InferenceServerRegistrationConfig, ModelInitialization,
    PylonRuntimeState, QuicHttpTunnelConfig, RequestCounterUpdate, RequestCounterUpdateInput,
    RequestObservation, RequestObservationEndpoint, RequestObservationState,
    ReverseQuicTunnelConfig, StatsCollectorConfig, StatsUpdateSource, TunnelTransportProtocol,
    start_stats_collector,
};
use stargate_proto::pb::InferenceServerStatus;

#[test]
fn crate_root_exports_registration_public_api() {
    let mut client = InferenceServerRegistrationClient::default();
    client.stop();

    let stats = CurrentModelStats::default();
    assert_eq!(stats.queue_size, 0);

    let config = InferenceServerRegistrationConfig {
        seeds: vec!["router-a".to_string()],
        inference_server_id: "pylon-a".to_string(),
        cluster_id: "cluster-a".to_string(),
        inference_server_url: "quic://127.0.0.1:8443".to_string(),
        min_update_interval: Duration::from_secs(1),
        reverse_tunnel: false,
        tls_cert_pem: None,
        grpc_tls_ca_cert_pem: None,
        quic_insecure: true,
        tunnel_protocol: TunnelTransportProtocol::RawQuic,
        forwarding: pylon_lib::TunnelForwardingConfig::default(),
        auth_token_provider: None,
    };

    assert_eq!(config.inference_server_id, "pylon-a");
    assert_eq!(
        ClientError::Config("bad".to_string()).to_string(),
        "configuration error: bad"
    );

    let _counter_update = RequestCounterUpdate::new(RequestCounterUpdateInput {
        source: StatsUpdateSource::OpenAiFallback,
        request_id: "req-a".to_string(),
        model_id: "model-a".to_string(),
        tokens_processed: Some(12),
        tokens_generated: Some(4),
        finished: true,
        observed_at: tokio::time::Instant::now(),
    });
}

#[test]
fn configured_input_tps_retains_legacy_pin_field() {
    let initialization = ModelInitialization::ConfiguredInputTps {
        input_tps: 100.0,
        pin: true,
    };

    match initialization {
        ModelInitialization::ConfiguredInputTps { input_tps, pin } => {
            assert_eq!(input_tps, 100.0);
            assert!(pin);
        }
        _ => panic!("configured input TPS variant should round-trip"),
    }
}

#[tokio::test]
async fn public_observation_api_preserves_duration_based_throughput() {
    let model_id = "model-a".to_string();
    let (runtime_state, observation_rx) = PylonRuntimeState::observed(
        InferenceServerStatus::Active,
        std::slice::from_ref(&model_id),
        16,
        None,
    );
    let collector = start_stats_collector(
        StatsCollectorConfig {
            openai_fallback_stats_enabled: true,
            ..Default::default()
        },
        observation_rx,
        runtime_state.clone(),
    );

    for request_index in 0..5 {
        runtime_state.observe_request(RequestObservation {
            endpoint: RequestObservationEndpoint::ChatCompletions,
            request_id: format!("request-{request_index}"),
            routing_key: None,
            model_id: model_id.clone(),
            priority: 0,
            input_tokens: 100,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: Some(200),
            output_messages: 1,
            output_tokens: 20,
            output_tokens_explicit: true,
            output_tokens_from_chunk_usage: false,
            state: RequestObservationState::Complete,
            time_to_response_headers: Some(Duration::from_millis(100)),
            time_to_first_output: Some(Duration::from_secs(1)),
            time_to_first_token: Some(Duration::from_secs(2)),
            total_duration: Duration::from_secs(4),
        });
    }

    tokio::time::timeout(Duration::from_secs(2), async {
        loop {
            let stats = runtime_state
                .model_stats(&model_id)
                .expect("configured model should retain public stats");
            if stats.last_mean_input_tps == 100.0 && stats.output_tps == 10.0 {
                break;
            }
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("public observations should publish input and output throughput");

    collector.shutdown().await;
}

#[test]
fn crate_root_exports_tunnel_config_public_api() {
    let direct_config = QuicHttpTunnelConfig {
        listen_addr: SocketAddr::from(([127, 0, 0, 1], 0)),
        inference_server_id: Some("direct-a".to_string()),
        upstream_http_base_url: "http://127.0.0.1:8000".to_string(),
        forwarding: pylon_lib::TunnelForwardingConfig {
            max_request_body_bytes: 1024,
            max_sse_buffer_bytes: 512,
            first_output_timeout: Duration::from_secs(1),
            output_chunk_timeout: Duration::from_secs(2),
            ..Default::default()
        },
        tls_cert_pem: None,
        tls_key_pem: None,
        server_identity_reloader: None,
        tls_reload_interval: stargate_tls::DEFAULT_TLS_RELOAD_INTERVAL,
        tunnel_protocol: TunnelTransportProtocol::RawQuic,
    };
    assert_eq!(
        direct_config.upstream_http_base_url,
        "http://127.0.0.1:8000"
    );

    let reverse_config = ReverseQuicTunnelConfig {
        target_addr: "stargate.example:50072".to_string(),
        inference_server_id: "reverse-a".to_string(),
        upstream_http_base_url: "http://127.0.0.1:8001".to_string(),
        forwarding: pylon_lib::TunnelForwardingConfig {
            max_request_body_bytes: 2048,
            max_sse_buffer_bytes: 1024,
            first_output_timeout: Duration::from_secs(3),
            output_chunk_timeout: Duration::from_secs(4),
            ..Default::default()
        },
        tls_cert_pem: None,
        quic_insecure: true,
        tunnel_protocol: TunnelTransportProtocol::Http3,
        sni_override: None,
        auth_token_provider: None,
    };
    assert_eq!(
        reverse_config.upstream_http_base_url,
        "http://127.0.0.1:8001"
    );

    let default_direct = QuicHttpTunnelConfig::new(
        SocketAddr::from(([127, 0, 0, 1], 0)),
        "http://upstream".into(),
    );
    let default_reverse = ReverseQuicTunnelConfig::new(
        "stargate.example:50072".into(),
        "reverse-a".into(),
        "http://upstream".into(),
    );
    assert_eq!(
        default_direct.forwarding.max_sse_buffer_bytes,
        DEFAULT_MAX_SSE_BUFFER_BYTES
    );
    assert_eq!(
        default_reverse.forwarding.max_sse_buffer_bytes,
        DEFAULT_MAX_SSE_BUFFER_BYTES
    );
}

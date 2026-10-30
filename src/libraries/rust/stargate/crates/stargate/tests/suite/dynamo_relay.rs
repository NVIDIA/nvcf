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

//! Pylon in Dynamo Relay mode against a fixture Relay: model stats reach
//! stargate from the Relay, and losing the Relay takes the model out of
//! routing.

use std::net::SocketAddr;
use std::pin::Pin;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::{Stream, StreamExt};
use pylon_lib::{
    DynamoRelayConfig, InferenceServerRegistrationClient, PylonRuntimeState, QuicHttpTunnelConfig,
    StatsCollectorConfig, start_dynamo_relay, start_quic_http_tunnel,
    start_stats_collector_with_engine_stats, stats_aggregator_update_channel,
};
use stargate::routing::RoutingTargetKey;
use stargate::test_support::StargateState;
use stargate_proto::dynamo_kvrelay as proto;
use stargate_proto::pb::InferenceServerStatus;
use tokio::net::TcpListener;

use crate::common::{
    direct_registration_config, init_crypto, make_stargate_runtime, start_dummy_backend,
    wait_for_routing, wait_for_unroutable,
};

const WINDOW_MS: u64 = 100;

#[tokio::test]
async fn dynamo_relay_stats_reach_stargate_and_relay_loss_fails_closed() {
    init_crypto();

    let model = "dynamo-relay-e2e-model";
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime("test-stargate-dynamo-relay");
    let handle = runtime.start().await.expect("stargate failed to start");
    let state = handle.state();

    let stats_config = StatsCollectorConfig {
        relay_enabled: true,
        ..StatsCollectorConfig::default()
    };
    let (runtime_state, request_observation_rx) = PylonRuntimeState::observed(
        InferenceServerStatus::Active,
        &[model.to_string()],
        stats_config.observation_channel_capacity,
        None,
    );
    let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(&stats_config);
    let relay = FixtureRelay::start(model).await;
    let dynamo_relay = start_dynamo_relay(
        DynamoRelayConfig::new(format!("http://{}", relay.addr), "pylon-relay-e2e"),
        stats_update_tx,
    );
    let stats_collector = start_stats_collector_with_engine_stats(
        stats_config,
        request_observation_rx,
        Some(stats_update_rx),
        runtime_state.clone(),
    );

    let backend_addr = start_dummy_backend(model).await;
    let mut tunnel_config = QuicHttpTunnelConfig::new(
        "127.0.0.1:0".parse().unwrap(),
        format!("http://{backend_addr}"),
    );
    tunnel_config.forwarding.runtime_state = runtime_state.clone();
    let tunnel = start_quic_http_tunnel(tunnel_config)
        .await
        .expect("tunnel failed to start");
    let mut reg_client = InferenceServerRegistrationClient::default();
    reg_client
        .start(direct_registration_config(
            vec![grpc_addr.to_string()],
            "dynamo-relay-e2e-inst",
            format!("quic://{}", tunnel.listen_addr()),
            format!("http://{backend_addr}"),
            runtime_state,
        ))
        .expect("registration failed");

    wait_for_relay_stats(&state, model, Duration::from_secs(10)).await;
    wait_for_routing(http_addr, model, Duration::from_secs(5)).await;

    relay.enabled.store(false, Ordering::Relaxed);
    // Silence timeout is max(3 s, 3 x window_ms), plus a registration update.
    wait_for_unroutable(http_addr, model, Duration::from_secs(10)).await;

    reg_client.stop();
    dynamo_relay.shutdown().await;
    stats_collector.shutdown().await;
    tunnel.shutdown().await;
    handle.begin_shutdown();
    handle.wait_for_shutdown(Duration::from_secs(5)).await;
}

async fn wait_for_relay_stats(state: &StargateState, model_id: &str, timeout: Duration) {
    let target = RoutingTargetKey {
        routing_key: None,
        model_id: model_id.to_string(),
    };
    let deadline = tokio::time::Instant::now() + timeout;
    let mut interval = tokio::time::interval(Duration::from_millis(20));
    interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    loop {
        let candidates = state.candidates_for_target(&target).await;
        if candidates.iter().any(|candidate| {
            let stats = &candidate.stats;
            stats.output_tps == 20.0
                && stats.queue_size == 2
                && stats.queued_input_size == 17
                && stats.num_running_queries == 4
                && stats.max_engine_concurrency == 8
                && stats.total_query_input_size == 31
                && stats.input_processing_queries == 2
                && stats.output_generation_queries == 2
                && stats.kv_cache_capacity_tokens == 1_000
                && stats.kv_cache_used_tokens == 400
                && stats.kv_cache_free_tokens == 600
        }) {
            return;
        }
        if tokio::time::Instant::now() >= deadline {
            let last_seen = candidates
                .iter()
                .map(|candidate| {
                    format!("{}: {:?}", candidate.inference_server_id, candidate.stats)
                })
                .collect::<Vec<_>>();
            panic!(
                "model '{model_id}' did not report Relay stats within {}s; last_seen={last_seen:?}",
                timeout.as_secs()
            );
        }
        interval.tick().await;
    }
}

/// A Relay serving one aggregated pool for one model, with fixed load and KV
/// usage. Disabling it ends open streams and refuses new subscriptions.
#[derive(Clone)]
struct FixtureRelay {
    addr: SocketAddr,
    model: String,
    enabled: Arc<AtomicBool>,
}

impl FixtureRelay {
    async fn start(model: &str) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let relay = Self {
            addr: listener.local_addr().unwrap(),
            model: model.to_string(),
            enabled: Arc::new(AtomicBool::new(true)),
        };
        let app = tonic::service::Routes::new(
            proto::kv_event_relay_server::KvEventRelayServer::new(relay.clone()),
        )
        .into_axum_router();
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        relay
    }

    #[allow(clippy::result_large_err)]
    fn admit(&self, contract_marker: u32, subscriber_id: &str) -> Result<(), tonic::Status> {
        if !self.enabled.load(Ordering::Relaxed) {
            return Err(tonic::Status::unavailable("fixture Relay disabled"));
        }
        // Reject what the real Relay rejects, so the fixture checks Pylon's
        // request envelope.
        if contract_marker != proto::CONTRACT_MARKER || subscriber_id.is_empty() {
            return Err(tonic::Status::failed_precondition(
                "request lacks the Relay v1 contract marker or a subscriber_id",
            ));
        }
        Ok(())
    }

    /// Windows every `WINDOW_MS` while enabled, numbered from 1, with the
    /// Relay clock advancing one window per window.
    fn windows<T: Send + 'static>(
        &self,
        window: impl Fn(u64, u64) -> T + Send + 'static,
    ) -> RelayStream<T> {
        let enabled = self.enabled.clone();
        let start_ms = unix_millis();
        Box::pin(async_stream::stream! {
            for window_sequence in 1_u64.. {
                tokio::time::sleep(Duration::from_millis(WINDOW_MS)).await;
                if !enabled.load(Ordering::Relaxed) {
                    break;
                }
                yield Ok(window(window_sequence, start_ms + window_sequence * WINDOW_MS));
            }
        })
    }
}

type RelayStream<T> = Pin<Box<dyn Stream<Item = Result<T, tonic::Status>> + Send>>;

#[tonic::async_trait]
impl proto::kv_event_relay_server::KvEventRelay for FixtureRelay {
    type WatchKvPoolCatalogStream = RelayStream<proto::KvPoolCatalogUpdate>;
    type SubscribeKvPoolStream = RelayStream<proto::FilterUpdate>;
    type SubscribeServingReadinessStream = RelayStream<proto::ServingReadinessUpdate>;
    type SubscribeKvPoolLoadStream = RelayStream<proto::KvPoolLoadUpdate>;
    type SubscribeServingLoadStream = RelayStream<proto::ServingLoadUpdate>;

    async fn get_relay_info(
        &self,
        request: tonic::Request<proto::RelayInfoRequest>,
    ) -> Result<tonic::Response<proto::RelayInfo>, tonic::Status> {
        self.admit(request.get_ref().contract_marker, "relay-info")?;
        Ok(tonic::Response::new(proto::RelayInfo {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(RELAY),
            contract_marker: proto::CONTRACT_MARKER,
        }))
    }

    async fn watch_kv_pool_catalog(
        &self,
        request: tonic::Request<proto::WatchKvPoolCatalogRequest>,
    ) -> Result<tonic::Response<Self::WatchKvPoolCatalogStream>, tonic::Status> {
        let request = request.into_inner();
        self.admit(request.contract_marker, &request.subscriber_id)?;
        let update = proto::KvPoolCatalogUpdate {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(RELAY),
            revision: 1,
            snapshot: Some(proto::KvPoolCatalogSnapshot {
                pools: vec![proto::KvPoolDescriptor {
                    producer: Some(producer()),
                    serving_endpoint: None,
                    registrations: vec![proto::ModelRegistration {
                        canonical_model_id: self.model.clone(),
                        target: None,
                        aliases: Vec::new(),
                    }],
                    query_semantics: Some(proto::KvQuerySemantics {
                        kv_block_size: 1,
                        hash_format: proto::KvQueryHashFormat::DynamoStandardV1 as i32,
                    }),
                    pool_roles: vec![proto::WorkerRole::Aggregated as i32],
                }],
            }),
            contract_marker: proto::CONTRACT_MARKER,
        };
        let enabled = self.enabled.clone();
        let stream = futures::stream::once(async { Ok(update) }).chain(async_stream::stream! {
            while enabled.load(Ordering::Relaxed) {
                tokio::time::sleep(Duration::from_millis(WINDOW_MS)).await;
            }
            if false {
                yield Ok(proto::KvPoolCatalogUpdate::default());
            }
        });
        Ok(tonic::Response::new(Box::pin(stream)))
    }

    async fn subscribe_kv_pool(
        &self,
        _request: tonic::Request<proto::SubscribeKvPoolRequest>,
    ) -> Result<tonic::Response<Self::SubscribeKvPoolStream>, tonic::Status> {
        Err(tonic::Status::unimplemented(
            "the fixture Relay does not model KV pool filters",
        ))
    }

    async fn subscribe_serving_readiness(
        &self,
        _request: tonic::Request<proto::SubscribeServingReadinessRequest>,
    ) -> Result<tonic::Response<Self::SubscribeServingReadinessStream>, tonic::Status> {
        Err(tonic::Status::unimplemented(
            "the fixture Relay does not model serving readiness",
        ))
    }

    async fn subscribe_kv_pool_load(
        &self,
        request: tonic::Request<proto::SubscribeKvPoolLoadRequest>,
    ) -> Result<tonic::Response<Self::SubscribeKvPoolLoadStream>, tonic::Status> {
        let request = request.into_inner();
        self.admit(request.contract_marker, &request.subscriber_id)?;
        Ok(tonic::Response::new(self.windows(
            |window_sequence, observed_ms| proto::KvPoolLoadUpdate {
                protocol_version: proto::PROTOCOL_VERSION,
                relay: Some(RELAY),
                window_sequence,
                observed_ms,
                window_ms: WINDOW_MS,
                pools: vec![proto::KvPoolLoadEntry {
                    producer: Some(producer()),
                    kv_used_blocks: 400,
                    total_kv_blocks: 1_000,
                    kv_observed_ranks: 1,
                    kv_expected_ranks: 1,
                }],
                contract_marker: proto::CONTRACT_MARKER,
            },
        )))
    }

    async fn subscribe_serving_load(
        &self,
        request: tonic::Request<proto::SubscribeServingLoadRequest>,
    ) -> Result<tonic::Response<Self::SubscribeServingLoadStream>, tonic::Status> {
        let request = request.into_inner();
        self.admit(request.contract_marker, &request.subscriber_id)?;
        let model = self.model.clone();
        Ok(tonic::Response::new(self.windows(
            move |window_sequence, observed_ms| proto::ServingLoadUpdate {
                protocol_version: proto::PROTOCOL_VERSION,
                relay: Some(RELAY),
                window_sequence,
                observed_ms,
                window_ms: WINDOW_MS,
                pools: vec![proto::PoolServingLoad {
                    producer: Some(producer()),
                    load: Some(proto::LoadView {
                        status: proto::DataStatus::Complete as i32,
                        source_observed_ms: observed_ms,
                        ..Default::default()
                    }),
                    deployment: Some(proto::PoolDeploymentStatus {
                        live_workers: Some(1),
                        max_concurrency: Some(8),
                    }),
                }],
                models: vec![proto::ModelServingLoad {
                    namespace: "dynamo".to_string(),
                    canonical_model_id: model.clone(),
                    load: Some(proto::LoadView {
                        requests: Some(proto::RequestLifecycleStats {
                            requests_started_total: window_sequence * 4,
                            requests_completed_total: window_sequence,
                            requests_failed_total: 0,
                            requests_cancelled_total: 0,
                            requests_awaiting_first_token: Some(2),
                            requests_generating: Some(2),
                        }),
                        tokens: Some(proto::TokenLoadStats {
                            awaiting_first_token_input_tokens: Some(17),
                            inflight_input_tokens: Some(31),
                            input_tokens_total: Some(window_sequence * 4),
                            // Two tokens per 100 ms window: 20 tokens/s.
                            output_tokens_total: Some(window_sequence * 2),
                            ..Default::default()
                        }),
                        status: proto::DataStatus::Complete as i32,
                        source_observed_ms: observed_ms,
                    }),
                    deployment: Some(proto::ModelDeploymentStatus {
                        expected_frontends: 1,
                        observed_frontends: 1,
                        ready_frontends: 1,
                        serving_pools: vec![pool_id()],
                    }),
                }],
                contract_marker: proto::CONTRACT_MARKER,
            },
        )))
    }
}

const RELAY: proto::RelayIdentity = proto::RelayIdentity {
    drt_instance_id: 1,
    relay_incarnation: 1,
};

fn producer() -> proto::ProducerIdentity {
    proto::ProducerIdentity {
        pool_id: Some(pool_id()),
        producer_incarnation: 1,
        layout_generation: 1,
        ckf_format: None,
    }
}

fn pool_id() -> proto::KvPoolId {
    let digest = |byte| proto::DigestIdentity {
        digest: vec![byte; 16],
        source: proto::IdentitySource::DefaultDerived as i32,
    };
    proto::KvPoolId {
        identity_version: 1,
        indexer_domain: Some(proto::IndexerDomainId {
            cache_semantics: Some(digest(1)),
            routing_scope: Some(digest(2)),
        }),
        dc_id: 1,
    }
}

fn unix_millis() -> u64 {
    let elapsed = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    u64::try_from(elapsed.as_millis()).unwrap_or(u64::MAX)
}

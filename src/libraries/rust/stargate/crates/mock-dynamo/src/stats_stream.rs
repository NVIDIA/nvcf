// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::pin::Pin;
use std::sync::atomic::Ordering;
use std::time::Duration;

use futures::Stream;
use stargate_proto::dynamo_kvrelay as proto;
use tokio::sync::broadcast;
use tonic::{Request, Response, Status};

use crate::AppState;

const SNAPSHOT_INTERVAL: Duration = Duration::from_secs(1);
const NAMESPACE: &str = "dynamo";
const RELAY: proto::RelayIdentity = proto::RelayIdentity {
    drt_instance_id: 1,
    relay_incarnation: 1,
};

#[derive(Debug, Clone)]
pub(crate) struct StatsStreamEvent {
    pub(crate) request_id: String,
    pub(crate) model: String,
    pub(crate) input_tokens: u64,
    pub(crate) output_tokens: u64,
    pub(crate) finished: bool,
}

pub(crate) fn grpc_router(state: AppState) -> axum::Router {
    let service = proto::kv_event_relay_server::KvEventRelayServer::new(MockRelay { state });
    tonic::service::Routes::new(service).into_axum_router()
}

#[derive(Clone)]
struct MockRelay {
    state: AppState,
}

type ResponseStream<T> = Pin<Box<dyn Stream<Item = Result<T, Status>> + Send + 'static>>;

#[tonic::async_trait]
impl proto::kv_event_relay_server::KvEventRelay for MockRelay {
    type WatchKvPoolCatalogStream = ResponseStream<proto::KvPoolCatalogUpdate>;
    type SubscribeKvPoolStream = ResponseStream<proto::FilterUpdate>;
    type SubscribeServingReadinessStream = ResponseStream<proto::ServingReadinessUpdate>;
    type SubscribeKvPoolLoadStream = ResponseStream<proto::KvPoolLoadUpdate>;
    type SubscribeServingLoadStream = ResponseStream<proto::ServingLoadUpdate>;

    async fn get_relay_info(
        &self,
        _request: Request<proto::RelayInfoRequest>,
    ) -> Result<Response<proto::RelayInfo>, Status> {
        Ok(Response::new(proto::RelayInfo {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(RELAY),
            contract_marker: proto::CONTRACT_MARKER,
        }))
    }

    async fn watch_kv_pool_catalog(
        &self,
        _request: Request<proto::WatchKvPoolCatalogRequest>,
    ) -> Result<Response<Self::WatchKvPoolCatalogStream>, Status> {
        ensure_enabled(&self.state)?;
        let state = self.state.clone();
        let stream = async_stream::stream! {
            yield Ok(catalog(&state.model_name));
            // The catalog never changes; hold the stream open until disabled.
            let mut interval = snapshot_interval();
            while state.stats_stream_enabled.load(Ordering::Relaxed) {
                interval.tick().await;
            }
        };
        Ok(Response::new(Box::pin(stream)))
    }

    async fn subscribe_kv_pool(
        &self,
        _request: Request<proto::SubscribeKvPoolRequest>,
    ) -> Result<Response<Self::SubscribeKvPoolStream>, Status> {
        Err(Status::unimplemented(
            "mock Dynamo does not model KV pool filters",
        ))
    }

    async fn subscribe_serving_readiness(
        &self,
        _request: Request<proto::SubscribeServingReadinessRequest>,
    ) -> Result<Response<Self::SubscribeServingReadinessStream>, Status> {
        Err(Status::unimplemented(
            "mock Dynamo does not model serving readiness",
        ))
    }

    async fn subscribe_kv_pool_load(
        &self,
        _request: Request<proto::SubscribeKvPoolLoadRequest>,
    ) -> Result<Response<Self::SubscribeKvPoolLoadStream>, Status> {
        ensure_enabled(&self.state)?;
        let state = self.state.clone();
        let stream = async_stream::stream! {
            let mut interval = snapshot_interval();
            for window_sequence in 1.. {
                interval.tick().await;
                if !state.stats_stream_enabled.load(Ordering::Relaxed) {
                    break;
                }
                yield Ok(kv_pool_load(&state, window_sequence).await);
            }
        };
        Ok(Response::new(Box::pin(stream)))
    }

    async fn subscribe_serving_load(
        &self,
        _request: Request<proto::SubscribeServingLoadRequest>,
    ) -> Result<Response<Self::SubscribeServingLoadStream>, Status> {
        ensure_enabled(&self.state)?;
        let state = self.state.clone();
        let stream = async_stream::stream! {
            let mut events = state.stats_events.subscribe();
            let mut accumulator = LoadAccumulator::default();
            let mut interval = snapshot_interval();
            let mut window_sequence = 0;
            loop {
                tokio::select! {
                    event = events.recv() => match event {
                        Ok(event) => accumulator.observe(event),
                        Err(broadcast::error::RecvError::Lagged(_)) => {
                            yield Err(Status::resource_exhausted("mock load event stream lagged"));
                            break;
                        }
                        Err(broadcast::error::RecvError::Closed) => break,
                    },
                    _ = interval.tick() => {
                        if !state.stats_stream_enabled.load(Ordering::Relaxed) {
                            break;
                        }
                        window_sequence += 1;
                        yield Ok(accumulator.window(&state.model_name, window_sequence));
                    }
                }
            }
        };
        Ok(Response::new(Box::pin(stream)))
    }
}

fn ensure_enabled(state: &AppState) -> Result<(), Status> {
    if state.stats_stream_enabled.load(Ordering::Relaxed) {
        Ok(())
    } else {
        Err(Status::unavailable("mock Relay stats streams disabled"))
    }
}

fn snapshot_interval() -> tokio::time::Interval {
    let mut interval = tokio::time::interval(SNAPSHOT_INTERVAL);
    interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    interval
}

#[derive(Default)]
struct LoadAccumulator {
    live: HashMap<String, LiveRequest>,
    totals: BTreeMap<String, TrafficCounters>,
}

struct LiveRequest {
    model: String,
    input_tokens: u64,
    output_tokens: u64,
}

#[derive(Default)]
struct TrafficCounters {
    requests_started: u64,
    requests_completed: u64,
    input_tokens: u64,
    output_tokens: u64,
}

impl LoadAccumulator {
    fn observe(&mut self, event: StatsStreamEvent) {
        let totals = self.totals.entry(event.model.clone()).or_default();
        if let Some(request) = self.live.get_mut(&event.request_id) {
            totals.input_tokens = totals
                .input_tokens
                .saturating_add(event.input_tokens.saturating_sub(request.input_tokens));
            totals.output_tokens = totals
                .output_tokens
                .saturating_add(event.output_tokens.saturating_sub(request.output_tokens));
            request.input_tokens = request.input_tokens.max(event.input_tokens);
            request.output_tokens = request.output_tokens.max(event.output_tokens);
        } else {
            totals.requests_started = totals.requests_started.saturating_add(1);
            totals.input_tokens = totals.input_tokens.saturating_add(event.input_tokens);
            totals.output_tokens = totals.output_tokens.saturating_add(event.output_tokens);
            self.live.insert(
                event.request_id.clone(),
                LiveRequest {
                    model: event.model.clone(),
                    input_tokens: event.input_tokens,
                    output_tokens: event.output_tokens,
                },
            );
        }
        if event.finished {
            self.live.remove(&event.request_id);
            totals.requests_completed = totals.requests_completed.saturating_add(1);
        }
    }

    fn window(&self, configured_model: &str, window_sequence: u64) -> proto::ServingLoadUpdate {
        let mut model_ids = BTreeSet::from([configured_model.to_string()]);
        model_ids.extend(self.live.values().map(|request| request.model.clone()));
        model_ids.extend(self.totals.keys().cloned());
        let models = model_ids
            .into_iter()
            .map(|model| {
                let totals = self.totals.get(&model);
                let live = self
                    .live
                    .values()
                    .filter(|request| request.model == model)
                    .collect::<Vec<_>>();
                let awaiting_first_token = live
                    .iter()
                    .filter(|request| request.output_tokens == 0)
                    .count() as u64;
                let awaiting_input_tokens = live
                    .iter()
                    .filter(|request| request.output_tokens == 0)
                    .map(|request| request.input_tokens)
                    .sum();
                let inflight_input_tokens = live.iter().map(|request| request.input_tokens).sum();
                proto::ModelServingLoad {
                    namespace: NAMESPACE.to_string(),
                    canonical_model_id: model,
                    load: Some(proto::LoadView {
                        requests: Some(proto::RequestLifecycleStats {
                            requests_started_total: totals
                                .map_or(0, |totals| totals.requests_started),
                            requests_completed_total: totals
                                .map_or(0, |totals| totals.requests_completed),
                            requests_failed_total: 0,
                            requests_cancelled_total: 0,
                            requests_awaiting_first_token: Some(awaiting_first_token),
                            requests_generating: Some(live.len() as u64 - awaiting_first_token),
                        }),
                        tokens: Some(proto::TokenLoadStats {
                            awaiting_first_token_input_tokens: Some(awaiting_input_tokens),
                            inflight_input_tokens: Some(inflight_input_tokens),
                            input_tokens_total: Some(
                                totals.map_or(0, |totals| totals.input_tokens),
                            ),
                            output_tokens_total: Some(
                                totals.map_or(0, |totals| totals.output_tokens),
                            ),
                            ..Default::default()
                        }),
                        status: proto::DataStatus::Complete as i32,
                        source_observed_ms: crate::openai::unix_millis(),
                    }),
                    deployment: Some(proto::ModelDeploymentStatus {
                        expected_frontends: 1,
                        observed_frontends: 1,
                        ready_frontends: 1,
                        serving_pools: vec![pool_id()],
                    }),
                }
            })
            .collect();
        proto::ServingLoadUpdate {
            protocol_version: proto::PROTOCOL_VERSION,
            relay: Some(RELAY),
            window_sequence,
            observed_ms: crate::openai::unix_millis(),
            window_ms: SNAPSHOT_INTERVAL.as_millis() as u64,
            pools: vec![proto::PoolServingLoad {
                producer: Some(producer()),
                load: Some(proto::LoadView {
                    status: proto::DataStatus::Complete as i32,
                    source_observed_ms: crate::openai::unix_millis(),
                    ..Default::default()
                }),
                deployment: Some(proto::PoolDeploymentStatus {
                    live_workers: Some(1),
                    max_concurrency: Some(1),
                }),
            }],
            models,
            contract_marker: proto::CONTRACT_MARKER,
        }
    }
}

async fn kv_pool_load(state: &AppState, window_sequence: u64) -> proto::KvPoolLoadUpdate {
    let stats = state.kv_cache.lock().await.stats(&state.model_name);
    proto::KvPoolLoadUpdate {
        protocol_version: proto::PROTOCOL_VERSION,
        relay: Some(RELAY),
        window_sequence,
        observed_ms: crate::openai::unix_millis(),
        window_ms: SNAPSHOT_INTERVAL.as_millis() as u64,
        pools: vec![proto::KvPoolLoadEntry {
            producer: Some(producer()),
            kv_used_blocks: stats.kv_cache_used_tokens,
            total_kv_blocks: stats.kv_cache_capacity_tokens,
            kv_observed_ranks: 1,
            kv_expected_ranks: 1,
        }],
        contract_marker: proto::CONTRACT_MARKER,
    }
}

/// One aggregated pool serving the configured model with one-token KV blocks.
fn catalog(model: &str) -> proto::KvPoolCatalogUpdate {
    proto::KvPoolCatalogUpdate {
        protocol_version: proto::PROTOCOL_VERSION,
        relay: Some(RELAY),
        revision: 1,
        snapshot: Some(proto::KvPoolCatalogSnapshot {
            pools: vec![proto::KvPoolDescriptor {
                producer: Some(producer()),
                serving_endpoint: Some(proto::DynamoEndpointId {
                    namespace: NAMESPACE.to_string(),
                    component: "backend".to_string(),
                    endpoint: "generate".to_string(),
                }),
                registrations: vec![proto::ModelRegistration {
                    canonical_model_id: model.to_string(),
                    target: Some(proto::ModelTarget {
                        target: Some(proto::model_target::Target::Base(proto::BaseModelTarget {
                            base_model: model.to_string(),
                        })),
                    }),
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
    }
}

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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn load_windows_keep_cumulative_counters_and_live_gauges() {
        let mut accumulator = LoadAccumulator::default();
        accumulator.observe(StatsStreamEvent {
            request_id: "req-1".to_string(),
            model: "model-a".to_string(),
            input_tokens: 10,
            output_tokens: 2,
            finished: false,
        });

        for _ in 0..2 {
            let snapshot = accumulator.window("model-a", 1);
            let load = snapshot.models[0].load.as_ref().unwrap();
            let requests = load.requests.as_ref().unwrap();
            let tokens = load.tokens.as_ref().unwrap();
            assert_eq!(requests.requests_started_total, 1);
            assert_eq!(requests.requests_generating, Some(1));
            assert_eq!(tokens.input_tokens_total, Some(10));
            assert_eq!(tokens.output_tokens_total, Some(2));
        }
    }
}

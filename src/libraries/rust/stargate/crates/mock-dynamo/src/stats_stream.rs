// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::pin::Pin;
use std::sync::atomic::Ordering;
use std::time::Duration;

use futures::Stream;
use stargate_proto::dynamo_pool_relay as proto;
use tokio::sync::broadcast;
use tonic::{Request, Response, Status};

use crate::AppState;

const SNAPSHOT_INTERVAL: Duration = Duration::from_secs(1);

#[derive(Debug, Clone)]
pub(crate) struct StatsStreamEvent {
    pub(crate) request_id: String,
    pub(crate) model: String,
    pub(crate) input_tokens: u64,
    pub(crate) output_tokens: u64,
    pub(crate) finished: bool,
}

pub(crate) fn grpc_router(state: AppState) -> axum::Router {
    let service = proto::pool_relay_server::PoolRelayServer::new(MockPoolRelay { state });
    tonic::service::Routes::new(service).into_axum_router()
}

#[derive(Clone)]
struct MockPoolRelay {
    state: AppState,
}

type ResponseStream<T> = Pin<Box<dyn Stream<Item = Result<T, Status>> + Send + 'static>>;

#[tonic::async_trait]
impl proto::pool_relay_server::PoolRelay for MockPoolRelay {
    type WatchKvBlockIndexStream = ResponseStream<proto::KvBlockIndexUpdate>;
    type WatchKvUsageStream = ResponseStream<proto::KvUsageSnapshot>;
    type WatchLoadStream = ResponseStream<proto::LoadSnapshot>;

    async fn watch_kv_block_index(
        &self,
        _request: Request<()>,
    ) -> Result<Response<Self::WatchKvBlockIndexStream>, Status> {
        Err(Status::unimplemented(
            "mock Dynamo does not model the KV block index stream",
        ))
    }

    async fn watch_kv_usage(
        &self,
        _request: Request<()>,
    ) -> Result<Response<Self::WatchKvUsageStream>, Status> {
        ensure_enabled(&self.state)?;
        let state = self.state.clone();
        let stream = async_stream::stream! {
            let mut interval = snapshot_interval();
            loop {
                interval.tick().await;
                if !state.stats_stream_enabled.load(Ordering::Relaxed) {
                    break;
                }
                yield Ok(usage_snapshot(&state).await);
            }
        };
        Ok(Response::new(Box::pin(stream)))
    }

    async fn watch_load(
        &self,
        _request: Request<()>,
    ) -> Result<Response<Self::WatchLoadStream>, Status> {
        ensure_enabled(&self.state)?;
        let state = self.state.clone();
        let stream = async_stream::stream! {
            let mut events = state.stats_events.subscribe();
            let mut accumulator = LoadAccumulator::default();
            let mut interval = snapshot_interval();
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
                        yield Ok(accumulator.snapshot(&state.model_name));
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

    fn snapshot(&self, configured_model: &str) -> proto::LoadSnapshot {
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
                proto::ModelView {
                    model: Some(model_registration(&model)),
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
                        source_observed_at_unix_ms: crate::openai::unix_millis(),
                    }),
                    deployment: Some(proto::ModelDeploymentStatus {
                        expected_frontends: 1,
                        observed_frontends: 1,
                        ready_frontends: 1,
                        serving_pools: vec![pool_identity()],
                    }),
                }
            })
            .collect();
        proto::LoadSnapshot {
            metadata: Some(metadata()),
            pools: vec![proto::PoolView {
                pool: Some(pool_identity()),
                load: Some(proto::LoadView {
                    status: proto::DataStatus::Complete as i32,
                    source_observed_at_unix_ms: crate::openai::unix_millis(),
                    ..Default::default()
                }),
                deployment: Some(proto::PoolDeploymentStatus {
                    roles: vec![proto::WorkerRole::Aggregated as i32],
                    live_workers: Some(1),
                    max_concurrency: Some(1),
                }),
            }],
            models,
        }
    }
}

async fn usage_snapshot(state: &AppState) -> proto::KvUsageSnapshot {
    let stats = state.kv_cache.lock().await.stats(&state.model_name);
    proto::KvUsageSnapshot {
        metadata: Some(metadata()),
        pools: vec![proto::PoolKvUsage {
            pool: Some(pool_identity()),
            models: vec![model_registration(&state.model_name)],
            roles: vec![proto::WorkerRole::Aggregated as i32],
            block_size_tokens: 1,
            expected_ranks: 1,
            observed_ranks: 1,
            capacity_blocks: Some(stats.kv_cache_capacity_tokens),
            used_blocks: Some(stats.kv_cache_used_tokens),
            status: proto::DataStatus::Complete as i32,
            source_observed_at_unix_ms: crate::openai::unix_millis(),
        }],
    }
}

fn metadata() -> proto::RelayMessageMetadata {
    proto::RelayMessageMetadata {
        relay_incarnation: 1,
        emitted_at_unix_ms: crate::openai::unix_millis(),
    }
}

fn model_registration(model: &str) -> proto::ModelRegistration {
    proto::ModelRegistration {
        model: model.to_string(),
        base_model: model.to_string(),
        adapter: None,
        aliases: Vec::new(),
    }
}

fn pool_identity() -> proto::PoolIdentity {
    proto::PoolIdentity {
        cache_semantics_digest: vec![1; 16],
        cache_semantics_source: proto::IdentitySource::DefaultDerived as i32,
        routing_scope_digest: vec![2; 16],
        routing_scope_source: proto::IdentitySource::DefaultDerived as i32,
        locality_id: 1,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn load_snapshots_keep_cumulative_counters_and_live_gauges() {
        let mut accumulator = LoadAccumulator::default();
        accumulator.observe(StatsStreamEvent {
            request_id: "req-1".to_string(),
            model: "model-a".to_string(),
            input_tokens: 10,
            output_tokens: 2,
            finished: false,
        });

        for _ in 0..2 {
            let snapshot = accumulator.snapshot("model-a");
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

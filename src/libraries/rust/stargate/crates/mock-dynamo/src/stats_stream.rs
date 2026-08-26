// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::pin::Pin;
use std::sync::atomic::Ordering;
use std::time::Duration;

use futures::Stream;
use stargate_proto::dynamo_kv_dc_relay as proto;
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
    let service = proto::kv_dc_relay_server::KvDcRelayServer::new(MockKvDcRelay { state });
    tonic::service::Routes::new(service).into_axum_router()
}

#[derive(Clone)]
struct MockKvDcRelay {
    state: AppState,
}

type ResponseStream<T> = Pin<Box<dyn Stream<Item = Result<T, Status>> + Send + 'static>>;

#[tonic::async_trait]
impl proto::kv_dc_relay_server::KvDcRelay for MockKvDcRelay {
    type WatchKvCuckooFilterStream = ResponseStream<proto::KvCuckooFilterUpdate>;
    type WatchKvUsageStream = ResponseStream<proto::KvUsageSnapshot>;
    type WatchLoadStream = ResponseStream<proto::LoadSnapshot>;

    async fn watch_kv_cuckoo_filter(
        &self,
        _request: Request<()>,
    ) -> Result<Response<Self::WatchKvCuckooFilterStream>, Status> {
        Err(Status::unimplemented(
            "mock Dynamo does not model the CKF stream",
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
    windows: BTreeMap<String, WindowCounters>,
}

struct LiveRequest {
    model: String,
    input_tokens: u64,
    output_tokens: u64,
}

#[derive(Default)]
struct WindowCounters {
    requests_started: u64,
    requests_completed: u64,
    input_tokens: u64,
    output_tokens: u64,
}

impl LoadAccumulator {
    fn observe(&mut self, event: StatsStreamEvent) {
        let window = self.windows.entry(event.model.clone()).or_default();
        if let Some(request) = self.live.get_mut(&event.request_id) {
            window.input_tokens = window
                .input_tokens
                .saturating_add(event.input_tokens.saturating_sub(request.input_tokens));
            window.output_tokens = window
                .output_tokens
                .saturating_add(event.output_tokens.saturating_sub(request.output_tokens));
            request.input_tokens = request.input_tokens.max(event.input_tokens);
            request.output_tokens = request.output_tokens.max(event.output_tokens);
        } else {
            window.requests_started = window.requests_started.saturating_add(1);
            window.input_tokens = window.input_tokens.saturating_add(event.input_tokens);
            window.output_tokens = window.output_tokens.saturating_add(event.output_tokens);
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
            window.requests_completed = window.requests_completed.saturating_add(1);
        }
    }

    fn snapshot(&mut self, configured_model: &str) -> proto::LoadSnapshot {
        let mut model_ids = BTreeSet::from([configured_model.to_string()]);
        model_ids.extend(self.live.values().map(|request| request.model.clone()));
        model_ids.extend(self.windows.keys().cloned());
        let windows = std::mem::take(&mut self.windows);
        let models = model_ids
            .into_iter()
            .map(|model| {
                let window = windows.get(&model);
                let live = self
                    .live
                    .values()
                    .filter(|request| request.model == model)
                    .collect::<Vec<_>>();
                let input_processing = live
                    .iter()
                    .filter(|request| request.output_tokens == 0)
                    .count() as u64;
                let output_generation = live.len() as u64 - input_processing;
                let pending_input_tokens = live
                    .iter()
                    .filter(|request| request.output_tokens == 0)
                    .map(|request| request.input_tokens)
                    .sum();
                let live_input_tokens = live.iter().map(|request| request.input_tokens).sum();
                proto::ModelLoad {
                    model: Some(model_registration(&model)),
                    ready_frontends: Some(1),
                    pending_first_output_requests: Some(input_processing),
                    pending_first_output_input_tokens: Some(pending_input_tokens),
                    live_input_tokens: Some(live_input_tokens),
                    input_processing_requests: Some(input_processing),
                    output_generation_requests: Some(output_generation),
                    serving_pools: vec![pool_identity()],
                    requests_started: window.map_or(0, |window| window.requests_started),
                    requests_completed: window.map_or(0, |window| window.requests_completed),
                    requests_failed: 0,
                    requests_cancelled: 0,
                    input_tokens: Some(window.map_or(0, |window| window.input_tokens)),
                    output_tokens: window.map_or(0, |window| window.output_tokens),
                    status: proto::DataStatus::Complete as i32,
                    expected_frontends: 1,
                    observed_frontends: 1,
                    source_observed_at_unix_ms: crate::openai::unix_millis(),
                }
            })
            .collect();
        proto::LoadSnapshot {
            metadata: Some(metadata()),
            window_ms: 1_000,
            pools: vec![proto::PoolLoad {
                pool: Some(pool_identity()),
                role: proto::WorkerRole::Aggregated as i32,
                live_workers: Some(1),
                active_prefill_tokens: None,
                active_decode_blocks: None,
                max_concurrency: Some(1),
                scheduler_status: proto::DataStatus::Complete as i32,
                scheduler_observed_at_unix_ms: crate::openai::unix_millis(),
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
            role: proto::WorkerRole::Aggregated as i32,
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
        drt_instance_id: 1,
        relay_incarnation: 1,
        observed_at_unix_ms: crate::openai::unix_millis(),
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
        dc_id: 1,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn load_snapshots_replace_window_counters_and_keep_live_gauges() {
        let mut accumulator = LoadAccumulator::default();
        accumulator.observe(StatsStreamEvent {
            request_id: "req-1".to_string(),
            model: "model-a".to_string(),
            input_tokens: 10,
            output_tokens: 2,
            finished: false,
        });

        let first = accumulator.snapshot("model-a");
        assert_eq!(first.models[0].requests_started, 1);
        assert_eq!(first.models[0].input_tokens, Some(10));
        assert_eq!(first.models[0].output_tokens, 2);
        assert_eq!(first.models[0].output_generation_requests, Some(1));

        let second = accumulator.snapshot("model-a");
        assert_eq!(second.models[0].requests_started, 0);
        assert_eq!(second.models[0].input_tokens, Some(0));
        assert_eq!(second.models[0].output_tokens, 0);
        assert_eq!(second.models[0].output_generation_requests, Some(1));
    }
}

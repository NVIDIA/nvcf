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

use indexmap::IndexMap;
use tokio::sync::oneshot;
use tokio::time::Instant as TokioInstant;
use tokio_util::sync::CancellationToken;

use crate::runtime_state::ModelGeneration;
use crate::{CurrentModelStats, PylonRuntimeState, RequestObservationEvent};
use stargate_runtime::OwnedTask;

use super::aggregator::{ENGINE_STATS_SOURCE, KvCacheStatsSnapshot, StatsAggregator};

const DEFAULT_OBSERVATION_CHANNEL_CAPACITY: usize = 1024;
const DEFAULT_SMOOTHING_WINDOW_SIZE: usize = 8;
const DEFAULT_MIN_INPUT_TOKENS: u64 = 1;
const DEFAULT_MIN_OUTPUT_TOKENS: u64 = 1;
const DEFAULT_DURATION_FLOOR: Duration = Duration::from_millis(10);
const DEFAULT_KV_CACHE_POLL_INTERVAL: Duration = Duration::from_secs(1);
const DEFAULT_KV_CACHE_REQUEST_TIMEOUT: Duration = Duration::from_secs(1);
const DEFAULT_ENGINE_STATS_REQUEST_TTL: Duration = Duration::from_secs(300);
const DEFAULT_ENGINE_STATS_MODEL_TTL: Duration = Duration::from_secs(30);
const DEFAULT_ENGINE_STATS_SWEEP_INTERVAL: Duration = Duration::from_secs(1);

#[derive(Debug, Clone)]
pub struct StatsCollectorConfig {
    pub observation_channel_capacity: usize,
    pub smoothing_window_size: usize,
    pub min_input_tokens: u64,
    pub min_output_tokens: u64,
    pub duration_floor: Duration,
    pub kv_cache_stats_url: Option<String>,
    pub kv_cache_poll_interval: Duration,
    pub kv_cache_request_timeout: Duration,
    pub engine_stats_request_ttl: Duration,
    pub engine_stats_model_ttl: Duration,
    pub engine_stats_sweep_interval: Duration,
    pub openai_fallback_stats_enabled: bool,
}

impl Default for StatsCollectorConfig {
    fn default() -> Self {
        Self {
            observation_channel_capacity: DEFAULT_OBSERVATION_CHANNEL_CAPACITY,
            smoothing_window_size: DEFAULT_SMOOTHING_WINDOW_SIZE,
            min_input_tokens: DEFAULT_MIN_INPUT_TOKENS,
            min_output_tokens: DEFAULT_MIN_OUTPUT_TOKENS,
            duration_floor: DEFAULT_DURATION_FLOOR,
            kv_cache_stats_url: None,
            kv_cache_poll_interval: DEFAULT_KV_CACHE_POLL_INTERVAL,
            kv_cache_request_timeout: DEFAULT_KV_CACHE_REQUEST_TIMEOUT,
            engine_stats_request_ttl: DEFAULT_ENGINE_STATS_REQUEST_TTL,
            engine_stats_model_ttl: DEFAULT_ENGINE_STATS_MODEL_TTL,
            engine_stats_sweep_interval: DEFAULT_ENGINE_STATS_SWEEP_INTERVAL,
            openai_fallback_stats_enabled: true,
        }
    }
}

pub fn stats_aggregator_update_channel(
    config: &StatsCollectorConfig,
) -> (
    flume::Sender<StatsAggregatorUpdate>,
    flume::Receiver<StatsAggregatorUpdate>,
) {
    flume::bounded(config.observation_channel_capacity)
}

#[derive(Clone)]
pub(crate) struct StatsCollectorControl {
    tx: flume::Sender<StatsCollectorCommand>,
}

pub struct StatsCollectorHandle {
    task: OwnedTask,
    control: StatsCollectorControl,
}

impl StatsCollectorHandle {
    pub(crate) fn control(&self) -> StatsCollectorControl {
        self.control.clone()
    }

    pub async fn wait_for_exit(&mut self) -> Result<(), tokio::task::JoinError> {
        self.task.wait_for_exit().await
    }

    pub async fn shutdown(self) {
        self.task
            .shutdown(stargate_runtime::TASK_SHUTDOWN_TIMEOUT)
            .await;
    }
}

#[derive(Debug, Clone, Copy)]
pub(crate) enum ModelStatsInitialization {
    Empty,
    ConfiguredInputTps { input_tps: f64, pin: bool },
}

enum StatsCollectorCommand {
    Begin {
        generation: ModelGeneration,
        initialization: ModelStatsInitialization,
        reply: oneshot::Sender<bool>,
    },
    Snapshot {
        generation: ModelGeneration,
        reply: oneshot::Sender<Option<CurrentModelStats>>,
    },
    Retire {
        generation: ModelGeneration,
        reply: oneshot::Sender<bool>,
    },
}

#[derive(Debug, thiserror::Error)]
#[error("stats collector stopped before acknowledging model generation command")]
pub(crate) struct StatsCollectorStopped;

impl StatsCollectorControl {
    pub(crate) async fn begin_generation(
        &self,
        generation: ModelGeneration,
        initialization: ModelStatsInitialization,
    ) -> Result<bool, StatsCollectorStopped> {
        let (reply, acknowledged) = oneshot::channel();
        self.send(StatsCollectorCommand::Begin {
            generation,
            initialization,
            reply,
        })
        .await?;
        acknowledged.await.map_err(|_| StatsCollectorStopped)
    }

    /// Flushes queued observation and engine-stats events into the aggregator,
    /// publishes resulting runtime-state updates, then snapshots this exact
    /// generation. Returns `None` if the generation is no longer owned.
    pub(crate) async fn flush_and_snapshot(
        &self,
        generation: &ModelGeneration,
    ) -> Result<Option<CurrentModelStats>, StatsCollectorStopped> {
        let (reply, acknowledged) = oneshot::channel();
        self.send(StatsCollectorCommand::Snapshot {
            generation: generation.clone(),
            reply,
        })
        .await?;
        acknowledged.await.map_err(|_| StatsCollectorStopped)
    }

    pub(crate) async fn retire_generation(
        &self,
        generation: &ModelGeneration,
    ) -> Result<bool, StatsCollectorStopped> {
        let (reply, acknowledged) = oneshot::channel();
        self.send(StatsCollectorCommand::Retire {
            generation: generation.clone(),
            reply,
        })
        .await?;
        acknowledged.await.map_err(|_| StatsCollectorStopped)
    }

    async fn send(&self, command: StatsCollectorCommand) -> Result<(), StatsCollectorStopped> {
        self.tx
            .send_async(command)
            .await
            .map_err(|_| StatsCollectorStopped)
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum StatsUpdateSource {
    EngineStatsStream,
    OpenAiFallback,
}

#[derive(Debug, Clone)]
pub enum StatsAggregatorUpdate {
    RequestCounters(RequestCounterUpdate),
    FinalizeRequest(FinalizeRequestUpdate),
    EnableOpenAiFallback,
}

#[derive(Debug, Clone)]
pub struct RequestCounterUpdate {
    pub(crate) source: StatsUpdateSource,
    pub(crate) request_id: String,
    pub(crate) model_id: String,
    pub(crate) generation: Option<ModelGeneration>,
    pub(crate) tokens_processed: Option<u64>,
    pub(crate) tokens_generated: Option<u64>,
    pub(crate) finished: bool,
    pub(crate) observed_at: TokioInstant,
}

#[derive(Debug, Clone)]
pub struct RequestCounterUpdateInput {
    pub source: StatsUpdateSource,
    pub request_id: String,
    pub model_id: String,
    pub tokens_processed: Option<u64>,
    pub tokens_generated: Option<u64>,
    pub finished: bool,
    pub observed_at: tokio::time::Instant,
}

impl RequestCounterUpdate {
    pub fn new(input: RequestCounterUpdateInput) -> Self {
        Self {
            source: input.source,
            request_id: input.request_id,
            model_id: input.model_id,
            generation: None,
            tokens_processed: input.tokens_processed,
            tokens_generated: input.tokens_generated,
            finished: input.finished,
            observed_at: input.observed_at,
        }
    }
}

#[derive(Debug, Clone)]
pub struct FinalizeRequestUpdate {
    pub(crate) source: StatsUpdateSource,
    pub(crate) request_id: String,
    pub(crate) generation: Option<ModelGeneration>,
    pub(crate) observed_at: TokioInstant,
}

impl FinalizeRequestUpdate {
    pub fn new(
        source: StatsUpdateSource,
        request_id: impl Into<String>,
        observed_at: TokioInstant,
    ) -> Self {
        Self {
            source,
            request_id: request_id.into(),
            generation: None,
            observed_at,
        }
    }
}

pub fn start_stats_collector(
    config: StatsCollectorConfig,
    observation_rx: flume::Receiver<RequestObservationEvent>,
    runtime_state: PylonRuntimeState,
) -> StatsCollectorHandle {
    start_stats_collector_with_engine_stats(config, observation_rx, None, runtime_state)
}

pub fn start_stats_collector_with_engine_stats(
    mut config: StatsCollectorConfig,
    observation_rx: flume::Receiver<RequestObservationEvent>,
    stats_update_rx: Option<flume::Receiver<StatsAggregatorUpdate>>,
    runtime_state: PylonRuntimeState,
) -> StatsCollectorHandle {
    // A wired engine stats stream is the throughput source of truth. Auto mode
    // falls back only after the stream task sends EnableOpenAiFallback.
    config.openai_fallback_stats_enabled &= stats_update_rx.is_none();
    let mut aggregator = StatsAggregator::new(config, runtime_state.clone());
    for model_id in runtime_state.model_ids() {
        let generation = runtime_state
            .current_generation(&model_id)
            .expect("runtime model generation should remain present during startup");
        aggregator
            .begin_generation(generation, ModelStatsInitialization::Empty)
            .expect("distinct runtime models should initialize once");
    }
    let (control_tx, control_rx) = flume::bounded(aggregator.config.observation_channel_capacity);
    let task = OwnedTask::spawn("stats collector", move |stop| async move {
        run_stats_collector(
            aggregator,
            observation_rx,
            stats_update_rx,
            control_rx,
            stop,
        )
        .await;
    });
    StatsCollectorHandle {
        task,
        control: StatsCollectorControl { tx: control_tx },
    }
}

fn publish_model_stats_update(
    runtime_state: &PylonRuntimeState,
    generation: ModelGeneration,
    stats: CurrentModelStats,
) {
    runtime_state.set_generation_stats(&generation, stats);
}

fn publish_model_stats_updates(
    runtime_state: &PylonRuntimeState,
    updates: Vec<(ModelGeneration, CurrentModelStats)>,
) {
    for (generation, stats) in updates {
        publish_model_stats_update(runtime_state, generation, stats);
    }
}

async fn run_stats_collector(
    mut aggregator: StatsAggregator,
    observation_rx: flume::Receiver<RequestObservationEvent>,
    mut stats_update_rx: Option<flume::Receiver<StatsAggregatorUpdate>>,
    control_rx: flume::Receiver<StatsCollectorCommand>,
    stop: CancellationToken,
) {
    let config = aggregator.config.clone();
    let runtime_state = aggregator.runtime_state.clone();
    let http_client = reqwest::Client::new();
    let mut kv_cache_poll = tokio::time::interval(config.kv_cache_poll_interval);
    kv_cache_poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    let mut engine_stats_sweep = tokio::time::interval(config.engine_stats_sweep_interval);
    engine_stats_sweep.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    let mut stats_aggregator_updated_models = Vec::with_capacity(2);
    let mut stats_aggregator_latest_models = IndexMap::with_capacity(2);

    'collector: loop {
        tokio::select! {
            biased;
            _ = stop.cancelled() => break 'collector,
            command = control_rx.recv_async() => {
                let Ok(command) = command else {
                    break 'collector;
                };
                if matches!(command, StatsCollectorCommand::Snapshot { .. }) {
                    if let Some(rx) = &stats_update_rx {
                        drain_stats_updates(
                            &mut aggregator,
                            rx,
                            &mut stats_aggregator_updated_models,
                            &mut stats_aggregator_latest_models,
                        );
                        publish_model_stats_updates(
                            &runtime_state,
                            std::mem::take(&mut stats_aggregator_updated_models),
                        );
                    }
                    drain_ready(&observation_rx, |event| {
                        publish_observation_event(&mut aggregator, &runtime_state, event);
                    });
                }
                apply_collector_command(&mut aggregator, &runtime_state, command);
            }
            event = observation_rx.recv_async() => {
                let Ok(event) = event else {
                    break 'collector;
                };
                publish_observation_event(&mut aggregator, &runtime_state, event);
            }
            update = async {
                match &stats_update_rx {
                    Some(rx) => rx.recv_async().await.ok(),
                    None => std::future::pending().await,
                }
            } => {
                let Some(update) = update else {
                    stats_update_rx = None;
                    continue;
                };
                if aggregator.apply_control_update(&update) {
                    continue;
                }
                stats_aggregator_updated_models.clear();
                aggregator.apply_update_into(update, &mut stats_aggregator_updated_models);
                if let Some(rx) = &stats_update_rx {
                    drain_stats_updates(
                        &mut aggregator,
                        rx,
                        &mut stats_aggregator_updated_models,
                        &mut stats_aggregator_latest_models,
                    );
                }
                if let Some(metrics) = runtime_state.metrics() {
                    metrics.observe_engine_stats_live_requests(
                        "engine_stats_stream",
                        aggregator.live_request_count(),
                    );
                    metrics.observe_engine_stats_model_states(
                        "engine_stats_stream",
                        aggregator.model_state_count(),
                    );
                }
                publish_model_stats_updates(
                    &runtime_state,
                    std::mem::take(&mut stats_aggregator_updated_models),
                );
            }
            _ = engine_stats_sweep.tick() => {
                let updated_models = aggregator.sweep_stale(TokioInstant::now());
                if let Some(metrics) = runtime_state.metrics() {
                    metrics.observe_engine_stats_live_requests(
                        "engine_stats_stream",
                        aggregator.live_request_count(),
                    );
                }
                publish_model_stats_updates(&runtime_state, updated_models);
            }
            _ = kv_cache_poll.tick(), if config.kv_cache_stats_url.is_some() => {
                let Some(kv_cache) = stop
                    .run_until_cancelled(poll_kv_cache_stats(&config, &http_client))
                    .await
                else {
                    break 'collector;
                };
                let Some(kv_cache) = kv_cache else {
                    continue;
                };
                if kv_cache.model.is_empty() {
                    tracing::warn!("dropping KV-cache stats without model id");
                    continue;
                }
                let model_id = kv_cache.model.clone();
                let Some((model_id, updated_stats)) = aggregator.apply_kv_cache_stats(kv_cache)
                else {
                    tracing::warn!(
                        model_id,
                        configured_models = ?aggregator.per_model.keys(),
                        "dropping KV-cache stats for a model with no live generation"
                    );
                    continue;
                };
                publish_model_stats_update(&runtime_state, model_id, updated_stats);
            }
        }
    }
}

fn publish_observation_event(
    aggregator: &mut StatsAggregator,
    runtime_state: &PylonRuntimeState,
    event: RequestObservationEvent,
) {
    let updated_models = if aggregator.openai_fallback_stats_enabled() {
        aggregator.apply_fallback_observation(&event)
    } else {
        aggregator.apply_stream_observation(&event)
    };
    publish_model_stats_updates(runtime_state, updated_models);
}

fn drain_stats_updates(
    aggregator: &mut StatsAggregator,
    updates: &flume::Receiver<StatsAggregatorUpdate>,
    updated_models: &mut Vec<(ModelGeneration, CurrentModelStats)>,
    latest_by_model: &mut IndexMap<ModelGeneration, CurrentModelStats>,
) {
    drain_ready(updates, |update| {
        if !aggregator.apply_control_update(&update) {
            aggregator.apply_update_into(update, updated_models);
        }
    });
    retain_latest_model_updates(updated_models, latest_by_model);
}

fn apply_collector_command(
    aggregator: &mut StatsAggregator,
    runtime_state: &PylonRuntimeState,
    command: StatsCollectorCommand,
) {
    match command {
        StatsCollectorCommand::Begin {
            generation,
            initialization,
            reply,
        } => {
            let update = aggregator.begin_generation(generation, initialization);
            let applied = update.is_some();
            if let Some((generation, stats)) = update {
                publish_model_stats_update(runtime_state, generation, stats);
            }
            observe_aggregate_counts(aggregator, runtime_state);
            let _ = reply.send(applied);
        }
        StatsCollectorCommand::Snapshot { generation, reply } => {
            let _ = reply.send(aggregator.snapshot_generation(&generation));
        }
        StatsCollectorCommand::Retire { generation, reply } => {
            let retired = aggregator.retire_generation(&generation);
            observe_aggregate_counts(aggregator, runtime_state);
            let _ = reply.send(retired);
        }
    }
}

fn observe_aggregate_counts(aggregator: &StatsAggregator, runtime_state: &PylonRuntimeState) {
    if let Some(metrics) = runtime_state.metrics() {
        metrics.observe_engine_stats_live_requests(
            ENGINE_STATS_SOURCE,
            aggregator.live_request_count(),
        );
        metrics
            .observe_engine_stats_model_states(ENGINE_STATS_SOURCE, aggregator.model_state_count());
    }
}

async fn poll_kv_cache_stats(
    config: &StatsCollectorConfig,
    http_client: &reqwest::Client,
) -> Option<KvCacheStatsSnapshot> {
    let url = config.kv_cache_stats_url.as_ref()?;
    let response = http_client
        .get(url)
        .timeout(config.kv_cache_request_timeout)
        .send()
        .await
        .inspect_err(|error| {
            tracing::warn!(url, error = %error, "failed to poll KV-cache stats");
        })
        .ok()?;
    if !response.status().is_success() {
        tracing::warn!(url, status = %response.status(), "KV-cache stats endpoint returned non-success status");
        return None;
    }
    response
        .json()
        .await
        .inspect_err(|error| {
            tracing::warn!(url, error = %error, "failed to parse KV-cache stats");
        })
        .ok()
}

fn drain_ready<T>(rx: &flume::Receiver<T>, mut consume: impl FnMut(T)) {
    for _ in 0..rx.len() {
        let Ok(value) = rx.try_recv() else { break };
        consume(value);
    }
}

fn retain_latest_model_updates(
    updates: &mut Vec<(ModelGeneration, CurrentModelStats)>,
    latest_by_model: &mut IndexMap<ModelGeneration, CurrentModelStats>,
) {
    latest_by_model.clear();
    for (generation, stats) in updates.drain(..).rev() {
        latest_by_model.entry(generation).or_insert(stats);
    }
    updates.extend(latest_by_model.drain(..).rev());
}
#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::sync::Arc;

    use super::super::aggregator::{KvCacheStatsSnapshot, StatsAggregator};
    use super::super::metrics::PylonMetrics;
    use super::super::projection::fallback_update_from_observation;
    use super::*;
    use crate::RequestObservation;
    use crate::generated_request_id::{GeneratedRequestKind, next_generated_request_id};
    use crate::request_observer::RequestObservationEndpoint;
    use crate::request_observer::RequestObservationState;
    use axum::{Json, Router, routing::get};
    use tokio::net::TcpListener;

    const MODEL_STATS_TEST_TIMEOUT: Duration = milliseconds(500);

    struct RunningCollector {
        runtime_state: PylonRuntimeState,
        stats_update_tx: Option<flume::Sender<StatsAggregatorUpdate>>,
        handle: StatsCollectorHandle,
        started_at: TokioInstant,
    }

    impl RunningCollector {
        fn spawn(
            config: StatsCollectorConfig,
            metrics: Option<Arc<PylonMetrics>>,
            with_stats_updates: bool,
        ) -> Self {
            Self::spawn_with_models(
                config,
                metrics,
                with_stats_updates,
                &["model-a".to_string()],
            )
        }

        fn spawn_empty(
            config: StatsCollectorConfig,
            metrics: Option<Arc<PylonMetrics>>,
            with_stats_updates: bool,
        ) -> Self {
            Self::spawn_with_models(config, metrics, with_stats_updates, &[])
        }

        fn spawn_with_models(
            config: StatsCollectorConfig,
            metrics: Option<Arc<PylonMetrics>>,
            with_stats_updates: bool,
            model_ids: &[String],
        ) -> Self {
            let (runtime_state, observation_rx) = PylonRuntimeState::observed(
                stargate_proto::pb::InferenceServerStatus::Unknown,
                model_ids,
                config.observation_channel_capacity,
                metrics,
            );
            let (stats_update_tx, stats_update_rx) = with_stats_updates
                .then(|| stats_aggregator_update_channel(&config))
                .unzip();
            let started_at = TokioInstant::now();
            let handle = start_stats_collector_with_engine_stats(
                config,
                observation_rx,
                stats_update_rx,
                runtime_state.clone(),
            );
            Self {
                runtime_state,
                stats_update_tx,
                handle,
                started_at,
            }
        }

        async fn begin_configured_model(&self, model_id: &str, input_tps: f64, pin: bool) {
            let generation = ModelGeneration::new(model_id, 0);
            assert!(self.runtime_state.begin_generation(generation.clone()));
            assert!(
                self.handle
                    .control()
                    .begin_generation(
                        generation,
                        ModelStatsInitialization::ConfiguredInputTps { input_tps, pin }
                    )
                    .await
                    .expect("collector should acknowledge configured generation")
            );
        }

        async fn send_update(&self, update: StatsAggregatorUpdate) {
            self.stats_update_tx
                .as_ref()
                .expect("collector should have a stats update channel")
                .send_async(update)
                .await
                .expect("collector should receive stats update");
        }
        async fn send_stream(
            &self,
            request_id: &str,
            tokens_processed: u64,
            tokens_generated: u64,
            finished: bool,
            elapsed: Duration,
        ) {
            self.send_update(stream_counter_update(
                request_id,
                self.runtime_state.current_generation("model-a"),
                tokens_processed,
                tokens_generated,
                finished,
                self.started_at + elapsed,
            ))
            .await;
        }
        async fn wait_for_stats(
            &self,
            context: &str,
            predicate: impl Fn(&CurrentModelStats) -> bool,
        ) -> CurrentModelStats {
            wait_for_model_stats(&self.runtime_state, "model-a", context, predicate).await
        }
        async fn observe_until(
            &self,
            observation: RequestObservation,
            context: &str,
            predicate: impl Fn(&CurrentModelStats) -> bool,
        ) -> CurrentModelStats {
            self.runtime_state.observe_request(observation);
            self.wait_for_stats(context, predicate).await
        }
        async fn seed_stream_output(&self, request_id: &str) {
            self.send_stream(request_id, 0, 0, false, Duration::ZERO)
                .await;
            self.send_stream(request_id, 0, 10, true, seconds(1)).await;
            self.wait_for_stats("stream finish should publish stats", |stats| {
                stats.output_tps == 10.0 && stats.max_output_tps == 10.0
            })
            .await;
        }
    }

    macro_rules! config {
        ($($field:ident: $value:expr),+ $(,)?) => {
            StatsCollectorConfig {
                $($field: $value,)+
                ..Default::default()
            }
        };
        (collector; $($field:ident: $value:expr),+ $(,)?) => {
            StatsCollectorConfig {
                $($field: $value,)+
                ..config!(observation_channel_capacity: 16)
            }
        };
    }

    const fn seconds(seconds: u64) -> Duration {
        Duration::from_secs(seconds)
    }

    const fn milliseconds(milliseconds: u64) -> Duration {
        Duration::from_millis(milliseconds)
    }

    fn apply_fallback_observation(
        aggregator: &mut StatsAggregator,
        observation: &RequestObservation,
    ) -> Vec<super::super::aggregator::ModelStatsUpdate> {
        let event = aggregator
            .runtime_state
            .transition_request_observation(observation.clone());
        aggregator.apply_fallback_observation(&event)
    }

    fn apply_stream_observation(
        aggregator: &mut StatsAggregator,
        observation: &RequestObservation,
    ) -> Vec<super::super::aggregator::ModelStatsUpdate> {
        let event = aggregator
            .runtime_state
            .transition_request_observation(observation.clone());
        aggregator.apply_stream_observation(&event)
    }

    fn observation(
        endpoint: RequestObservationEndpoint,
        request_id: &str,
        state: RequestObservationState,
    ) -> RequestObservation {
        RequestObservation {
            endpoint,
            request_id: request_id.to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens: 0,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: Some(200),
            output_messages: 0,
            output_tokens: 0,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
            state,
            time_to_response_headers: None,
            time_to_first_output: None,
            time_to_first_token: None,
            total_duration: Duration::ZERO,
        }
    }

    fn completed_observation(
        input_tokens: u64,
        output_messages: u64,
        output_tokens: u64,
        time_to_first_output: Duration,
        total_duration: Duration,
    ) -> RequestObservation {
        RequestObservation {
            input_tokens,
            output_messages,
            output_tokens,
            time_to_response_headers: Some(milliseconds(20)),
            time_to_first_output: Some(time_to_first_output),
            time_to_first_token: Some(time_to_first_output),
            total_duration,
            ..observation(
                RequestObservationEndpoint::ChatCompletions,
                "req-1",
                RequestObservationState::Complete,
            )
        }
    }

    fn completed_embeddings_observation(
        input_tokens: u64,
        embedding_items: u64,
        time_to_response_headers: Duration,
        total_duration: Duration,
    ) -> RequestObservation {
        RequestObservation {
            input_tokens,
            embedding_items,
            embedding_items_observed: true,
            time_to_response_headers: Some(time_to_response_headers),
            total_duration,
            ..observation(
                RequestObservationEndpoint::Embeddings,
                "req-embedding",
                RequestObservationState::Complete,
            )
        }
    }

    fn active_chat_observation(
        request_id: &str,
        state: RequestObservationState,
    ) -> RequestObservation {
        let time_to_first_output =
            (state == RequestObservationState::OutputGeneration).then_some(milliseconds(50));
        RequestObservation {
            input_tokens: 32,
            output_messages: 1,
            output_tokens: 2,
            output_tokens_explicit: true,
            output_tokens_from_chunk_usage: true,
            time_to_response_headers: Some(milliseconds(10)),
            time_to_first_output,
            time_to_first_token: time_to_first_output,
            total_duration: milliseconds(100),
            ..observation(
                RequestObservationEndpoint::ChatCompletions,
                request_id,
                state,
            )
        }
    }

    fn trusted_completed_observation(request_id: &str) -> RequestObservation {
        RequestObservation {
            request_id: request_id.to_string(),
            output_tokens_explicit: true,
            output_tokens_from_chunk_usage: true,
            ..completed_observation(20, 1, 10, seconds(1), seconds(3))
        }
    }

    fn identified(
        mut observation: RequestObservation,
        request_id: impl Into<String>,
    ) -> RequestObservation {
        observation.request_id = request_id.into();
        observation
    }

    struct TestAggregator {
        inner: StatsAggregator,
        start: TokioInstant,
    }

    macro_rules! counter_method {
        ($name:ident, $source:ident, $token:ty, $wrap:expr) => {
            fn $name(
                &mut self,
                request_id: &str,
                tokens: ($token, $token),
                finished: bool,
                elapsed: Duration,
            ) -> Vec<super::super::aggregator::ModelStatsUpdate> {
                let wrap = $wrap;
                self.counter(
                    StatsUpdateSource::$source,
                    request_id,
                    "model-a",
                    (wrap(tokens.0), wrap(tokens.1)),
                    finished,
                    elapsed,
                )
            }
        };
    }

    impl TestAggregator {
        fn counter(
            &mut self,
            source: StatsUpdateSource,
            request_id: &str,
            model_id: &str,
            tokens: (Option<u64>, Option<u64>),
            finished: bool,
            elapsed: Duration,
        ) -> Vec<super::super::aggregator::ModelStatsUpdate> {
            self.inner
                .apply_update(StatsAggregatorUpdate::RequestCounters(
                    RequestCounterUpdate {
                        source,
                        request_id: request_id.to_string(),
                        model_id: model_id.to_string(),
                        generation: self.inner.current_generation(model_id).cloned(),
                        tokens_processed: tokens.0,
                        tokens_generated: tokens.1,
                        finished,
                        observed_at: self.start + elapsed,
                    },
                ))
        }
        fn sweep(&mut self, elapsed: Duration) -> Vec<super::super::aggregator::ModelStatsUpdate> {
            self.inner.sweep_stale(self.start + elapsed)
        }
        fn finalize(&mut self, request_id: &str, elapsed: Duration) {
            let mut update = FinalizeRequestUpdate::new(
                StatsUpdateSource::OpenAiFallback,
                request_id,
                self.start + elapsed,
            );
            update.generation = self.inner.current_generation("model-a").cloned();
            self.inner.finalize_request(update);
        }
        counter_method!(stream, EngineStatsStream, u64, Some);
        counter_method!(fallback, OpenAiFallback, u64, Some);
        counter_method!(
            partial_stream,
            EngineStatsStream,
            Option<u64>,
            std::convert::identity
        );
        fn stream_stats(
            &mut self,
            request_id: &str,
            tokens: (u64, u64),
            finished: bool,
            elapsed: Duration,
        ) -> CurrentModelStats {
            self.stream(request_id, tokens, finished, elapsed)
                .pop()
                .expect("stream update should publish stats")
                .1
        }
        fn sample_first_stream_counters(
            &mut self,
            request_prefix: &str,
            count: u64,
            tokens: (u64, u64),
        ) -> CurrentModelStats {
            (0..count)
                .filter_map(|index| {
                    self.stream(
                        &format!("{request_prefix}-{index}"),
                        tokens,
                        true,
                        seconds(index + 1),
                    )
                    .pop()
                    .map(|(_, stats)| stats)
                })
                .last()
                .expect("stream counter samples should publish stats")
        }
        fn model_counter(
            &mut self,
            source: StatsUpdateSource,
            request_id: &str,
            model_id: &str,
            tokens: (u64, u64),
            finished: bool,
            elapsed: Duration,
        ) -> Vec<super::super::aggregator::ModelStatsUpdate> {
            self.counter(
                source,
                request_id,
                model_id,
                (Some(tokens.0), Some(tokens.1)),
                finished,
                elapsed,
            )
        }
    }

    impl std::ops::Deref for TestAggregator {
        type Target = StatsAggregator;
        fn deref(&self) -> &Self::Target {
            &self.inner
        }
    }

    impl std::ops::DerefMut for TestAggregator {
        fn deref_mut(&mut self) -> &mut Self::Target {
            &mut self.inner
        }
    }

    fn test_aggregator_with_initialization(
        config: StatsCollectorConfig,
        initialization: ModelStatsInitialization,
    ) -> TestAggregator {
        let model_ids = vec!["model-a".to_string()];
        let runtime_state = PylonRuntimeState::new(
            stargate_proto::pb::InferenceServerStatus::Unknown,
            &model_ids,
        );
        let mut inner = StatsAggregator::new(config, runtime_state.clone());
        let generation = runtime_state
            .current_generation("model-a")
            .expect("test model generation should exist");
        inner
            .begin_generation(generation, initialization)
            .expect("test model generation should initialize");
        TestAggregator {
            inner,
            start: TokioInstant::now(),
        }
    }

    fn test_aggregator(config: StatsCollectorConfig) -> TestAggregator {
        test_aggregator_with_initialization(config, ModelStatsInitialization::Empty)
    }

    fn kv_cache_stats(model: &str) -> KvCacheStatsSnapshot {
        KvCacheStatsSnapshot {
            model: model.to_string(),
            kv_cache_capacity_tokens: 1_000,
            kv_cache_used_tokens: 400,
            kv_cache_free_tokens: 600,
        }
    }

    fn published_stats(
        updates: Vec<super::super::aggregator::ModelStatsUpdate>,
    ) -> CurrentModelStats {
        updates
            .into_iter()
            .find(|(generation, _)| generation.model_id() == "model-a")
            .expect("model-a stats should publish")
            .1
    }

    fn single_fallback_stats(
        aggregator: &mut StatsAggregator,
        observation: &RequestObservation,
    ) -> CurrentModelStats {
        let updates = apply_fallback_observation(aggregator, observation);
        assert_eq!(updates.len(), 1);
        updates.into_iter().next().expect("one model update").1
    }

    fn sample_observations(
        aggregator: &mut StatsAggregator,
        template: &RequestObservation,
        request_prefix: &str,
        count: usize,
        apply: fn(
            &mut StatsAggregator,
            &RequestObservation,
        ) -> Vec<super::super::aggregator::ModelStatsUpdate>,
    ) -> CurrentModelStats {
        let mut latest = None;
        for index in 0..count {
            latest = apply(
                aggregator,
                &identified(template.clone(), format!("{request_prefix}-{index}")),
            )
            .pop()
            .map(|(_, stats)| stats);
        }
        latest.expect("final observation sample should publish stats")
    }

    macro_rules! assert_stats {
        ($stats:expr; $($field:ident: $expected:expr),+ $(,)?) => {{
            let stats = &$stats;
            $(assert_eq!(stats.$field, $expected, stringify!($field));)+
        }};
    }

    fn assert_unlabeled(stats: &CurrentModelStats) {
        assert!(stats.stats_capabilities.is_empty());
        assert!(stats.stats_sources.is_empty());
    }

    macro_rules! fallback_distribution_test {
        ($name:ident, $observation:expr, $prefix:literal, $expected:expr) => {
            #[test]
            fn $name() {
                let mut aggregator = test_aggregator(StatsCollectorConfig::default());
                sample_observations(
                    &mut aggregator,
                    &$observation,
                    $prefix,
                    5,
                    apply_fallback_observation,
                );
                assert_eq!(
                    aggregator.snapshot("model-a").last_mean_input_tps,
                    $expected
                );
                let distribution = &aggregator.per_model["model-a"]
                    .metrics
                    .input_tps_distribution;
                assert_eq!(distribution.count, 5);
                assert_eq!(distribution.mean, $expected);
            }
        };
    }

    macro_rules! fallback_snapshot_test {
        ($name:ident, $config:expr, $observation:expr; $($field:ident: $expected:expr),+ $(,)?) => {
            #[test]
            fn $name() {
                let mut aggregator = test_aggregator($config);
                let stats = single_fallback_stats(&mut aggregator, &$observation);
                assert_stats!(stats; $($field: $expected),+);
            }
        };
    }

    #[test]
    fn latest_model_update_retention_keeps_last_snapshot_per_model() {
        let mut updates = [
            ("model-a", 1.0),
            ("model-b", 2.0),
            ("model-a", 3.0),
            ("model-c", 4.0),
            ("model-b", 5.0),
        ]
        .into_iter()
        .map(|(model, output_tps)| {
            (
                ModelGeneration::new(model, 1),
                CurrentModelStats {
                    output_tps,
                    ..Default::default()
                },
            )
        })
        .collect();
        let mut latest_by_model = indexmap::IndexMap::new();
        retain_latest_model_updates(&mut updates, &mut latest_by_model);
        assert_eq!(
            updates
                .iter()
                .map(|(generation, stats)| (generation.model_id(), stats.output_tps))
                .collect::<Vec<_>>(),
            [("model-a", 3.0), ("model-c", 4.0), ("model-b", 5.0)]
        );
        assert!(latest_by_model.is_empty());
    }

    #[test]
    fn ready_update_drain_uses_a_fixed_snapshot_budget() {
        let (tx, rx) = flume::bounded(3);
        (1..=3).for_each(|value| tx.try_send(value).unwrap());

        let mut drained = Vec::new();
        drain_ready(&rx, |value| {
            drained.push(value);
            tx.try_send(value + 3).unwrap();
        });
        assert_eq!(drained, [1, 2, 3]);
        assert_eq!(rx.try_iter().collect::<Vec<_>>(), [4, 5, 6]);
    }

    fn stream_counter_update(
        request_id: &str,
        generation: Option<ModelGeneration>,
        tokens_processed: u64,
        tokens_generated: u64,
        finished: bool,
        observed_at: TokioInstant,
    ) -> StatsAggregatorUpdate {
        StatsAggregatorUpdate::RequestCounters(RequestCounterUpdate {
            source: StatsUpdateSource::EngineStatsStream,
            request_id: request_id.to_string(),
            model_id: "model-a".to_string(),
            generation,
            tokens_processed: Some(tokens_processed),
            tokens_generated: Some(tokens_generated),
            finished,
            observed_at,
        })
    }

    async fn wait_for_model_stats(
        runtime_state: &PylonRuntimeState,
        model_id: &str,
        context: &str,
        predicate: impl Fn(&CurrentModelStats) -> bool,
    ) -> CurrentModelStats {
        tokio::time::timeout(MODEL_STATS_TEST_TIMEOUT, async {
            let mut poll = tokio::time::interval(milliseconds(1));
            loop {
                poll.tick().await;
                if let Some(stats) = runtime_state.model_stats(model_id)
                    && predicate(&stats)
                {
                    return stats;
                }
            }
        })
        .await
        .unwrap_or_else(|_| panic!("{context}"))
    }

    async fn wait_for_metric(metrics: &PylonMetrics, expected: &str, context: &str) {
        for _ in 0..50 {
            let body = metrics.gather_text().expect("metrics should encode");
            if body.contains(expected) {
                return;
            }
            tokio::task::yield_now().await;
        }
        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(expected), "{context}");
    }

    async fn spawn_kv_cache_server(
        app: Router,
    ) -> (std::net::SocketAddr, tokio::task::JoinHandle<()>) {
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("listener should bind");
        let address = listener.local_addr().expect("listener should have address");
        let server = tokio::spawn(async move {
            axum::serve(listener, app)
                .await
                .expect("KV-cache test server should run");
        });
        (address, server)
    }

    #[test]
    fn stats_stream_cumulative_request_counters_drive_stats_aggregator() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        aggregator.stream("req-a", (0, 0), false, Duration::ZERO);
        let updates = aggregator.stream("req-a", (10, 4), false, milliseconds(100));
        let stats = published_stats(updates);
        assert_stats!(stats; output_tps: 40.0, max_output_tps: 40.0, stats_sources: ["engine_stats_stream"]);
        for tick in 2..=5 {
            let updates =
                aggregator.stream("req-a", (tick * 10, 4), false, milliseconds(tick * 100));
            if tick < 5 {
                continue;
            }
            let stats = published_stats(updates);
            assert_eq!(stats.last_mean_input_tps, 100.0);
        }
    }

    #[test]
    fn pinned_configured_input_tps_is_preserved_across_engine_stats_updates() {
        let mut aggregator = test_aggregator_with_initialization(
            StatsCollectorConfig::default(),
            ModelStatsInitialization::ConfiguredInputTps {
                input_tps: 2_200.0,
                pin: true,
            },
        );
        let stats = aggregator.stream_stats("req-a", (0, 0), false, Duration::ZERO);
        assert_eq!(stats.last_mean_input_tps, 2_200.0);
        for tick in 1..=5 {
            aggregator.stream("req-a", (tick * 10, 0), false, milliseconds(tick * 100));
        }
        assert_eq!(aggregator.snapshot("model-a").last_mean_input_tps, 2_200.0);
    }

    #[test]
    fn unpinned_configured_input_tps_moves_with_the_first_real_sample() {
        let mut aggregator = test_aggregator_with_initialization(
            StatsCollectorConfig::default(),
            ModelStatsInitialization::ConfiguredInputTps {
                input_tps: 100.0,
                pin: false,
            },
        );
        assert_eq!(aggregator.snapshot("model-a").last_mean_input_tps, 100.0);
        aggregator.stream("req-a", (0, 0), false, Duration::ZERO);

        let stats = published_stats(aggregator.stream("req-a", (20, 0), false, milliseconds(100)));

        assert!((stats.last_mean_input_tps - (700.0 / 6.0)).abs() < f64::EPSILON);
    }

    #[test]
    fn first_engine_stream_counter_without_zero_baseline_contributes_tps() {
        let mut aggregator = test_aggregator(config!(duration_floor: milliseconds(100)));
        let stats = aggregator.stream_stats("req-first-output", (0, 10), true, Duration::ZERO);
        assert_stats!(stats; output_tps: 100.0, max_output_tps: 100.0);
        let stats = aggregator.sample_first_stream_counters("req-first-input", 5, (10, 0));
        assert_eq!(stats.last_mean_input_tps, 100.0);
    }

    #[test]
    fn first_post_baseline_engine_stream_delta_under_floor_contributes_tps() {
        let mut aggregator = test_aggregator(config!(duration_floor: milliseconds(100)));
        let label_stats = aggregator.stream_stats("req-fast", (0, 0), false, Duration::ZERO);
        assert_stats!(label_stats; stats_sources: ["engine_stats_stream"], output_tps: 0.0);
        let stats = aggregator.stream_stats("req-fast", (0, 10), true, milliseconds(1));
        assert_stats!(stats; output_tps: 100.0, max_output_tps: 100.0);
    }

    #[test]
    fn engine_stream_sub_floor_deltas_accumulate_after_fast_first_sample() {
        let mut aggregator = test_aggregator(config!(duration_floor: milliseconds(10)));
        aggregator.stream("req-live", (0, 0), false, Duration::ZERO);
        let first_stats = aggregator.stream_stats("req-live", (0, 1), false, milliseconds(1));
        assert_stats!(first_stats; output_tps: 100.0, max_output_tps: 100.0);
        for tick in 2..10 {
            let updates = aggregator.stream("req-live", (0, tick), false, milliseconds(tick));
            assert!(
                updates.is_empty(),
                "sub-floor deltas should accumulate without publishing noisy snapshots"
            );
        }
        let stats = aggregator.stream_stats("req-live", (0, 11), false, milliseconds(11));
        assert_stats!(stats; max_output_tps: 1_000.0, output_tps: 550.0);
    }

    #[test]
    fn engine_stream_missing_counter_fields_do_not_sample_stale_dimensions() {
        let mut aggregator = test_aggregator(config!(duration_floor: milliseconds(10)));
        aggregator.partial_stream("req-partial", (None, Some(0)), false, Duration::ZERO);
        let first_stats = aggregator
            .partial_stream("req-partial", (None, Some(1)), false, milliseconds(1))
            .pop()
            .expect("first output counter should publish with the duration floor")
            .1;
        assert_eq!(first_stats.output_tps, 100.0);
        assert!(
            aggregator
                .partial_stream("req-partial", (None, Some(2)), false, milliseconds(2),)
                .is_empty(),
            "second output counter is still below the duration floor"
        );
        let input_only_updates =
            aggregator.partial_stream("req-partial", (Some(1), None), false, milliseconds(11));
        assert!(
            input_only_updates.is_empty(),
            "input-only updates must not publish a stale output TPS sample"
        );
    }

    #[test]
    fn engine_stream_sub_minimum_deltas_accumulate_until_publishable() {
        let config = config!(duration_floor: milliseconds(10), min_output_tokens: 5);
        let mut aggregator = test_aggregator(config);
        aggregator.stream("req-min", (0, 0), false, Duration::ZERO);
        for tick in 1..10 {
            let updates = aggregator.stream("req-min", (0, tick), false, milliseconds(tick));
            assert!(
                updates.is_empty(),
                "tokens below the minimum or duration floor should remain accumulated"
            );
        }
        let stats = aggregator.stream_stats("req-min", (0, 10), false, milliseconds(10));
        assert_stats!(stats; output_tps: 1_000.0, max_output_tps: 1_000.0);
    }

    #[test]
    fn fallback_and_stream_cumulative_counters_share_stats_math() {
        let config = StatsCollectorConfig::default();
        let mut stream_aggregator = test_aggregator(config.clone());
        let mut fallback_aggregator = test_aggregator(config);
        for tick in 0..=5 {
            let elapsed = milliseconds(tick * 100);
            let tokens_processed = tick * 10;
            let tokens_generated = tick * 2;
            let stream_updates = stream_aggregator.stream(
                "req-shared",
                (tokens_processed, tokens_generated),
                tick == 5,
                elapsed,
            );
            let fallback_updates = fallback_aggregator.fallback(
                "req-shared",
                (tokens_processed, tokens_generated),
                tick == 5,
                elapsed,
            );
            if tick == 0 {
                assert_eq!(stream_updates.len(), 1);
                assert!(fallback_updates.is_empty());
                continue;
            }
            assert_eq!(stream_updates.len(), fallback_updates.len());
            for ((_, stream_stats), (_, fallback_stats)) in
                stream_updates.iter().zip(fallback_updates.iter())
            {
                assert_stats!(stream_stats;
                    last_mean_input_tps: fallback_stats.last_mean_input_tps,
                    output_tps: fallback_stats.output_tps,
                    max_output_tps: fallback_stats.max_output_tps
                );
            }
        }
    }

    #[test]
    fn request_counter_model_reset_finalizes_without_late_replay() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        aggregator
            .begin_generation(
                ModelGeneration::new("model-b", 2),
                ModelStatsInitialization::Empty,
            )
            .expect("second test model should initialize");
        let original_model = aggregator
            .model_counter(
                StatsUpdateSource::EngineStatsStream,
                "req-reused",
                "model-a",
                (0, 0),
                false,
                Duration::ZERO,
            )
            .pop()
            .expect("first stream event should publish model-a source labels");
        assert_eq!(
            (original_model.0.model_id(), original_model.1.output_tps),
            ("model-a", 0.0)
        );
        assert_eq!(aggregator.live_request_count(), 1);
        let replacement_model = aggregator
            .model_counter(
                StatsUpdateSource::EngineStatsStream,
                "req-reused",
                "model-b",
                (0, 0),
                false,
                milliseconds(50),
            )
            .pop()
            .expect("model change should reset request state and publish model-b source labels");
        assert_eq!(
            (
                replacement_model.0.model_id(),
                replacement_model.1.output_tps
            ),
            ("model-b", 0.0)
        );
        assert_eq!(aggregator.live_request_count(), 1);
        let finalized = aggregator
            .model_counter(
                StatsUpdateSource::OpenAiFallback,
                "req-reused",
                "model-b",
                (10, 4),
                true,
                milliseconds(150),
            )
            .pop()
            .expect("fallback finalization should publish the replacement model snapshot");
        assert_eq!(finalized.0.model_id(), "model-b");
        assert_stats!(finalized.1; output_tps: 40.0, max_output_tps: 40.0, stats_sources: ["engine_stats_stream"]);
        assert_eq!(aggregator.live_request_count(), 0);
        let late_replay = aggregator.model_counter(
            StatsUpdateSource::EngineStatsStream,
            "req-reused",
            "model-b",
            (10, 8),
            true,
            milliseconds(200),
        );
        assert!(
            late_replay.is_empty(),
            "late stream replay after fallback finalization must not double-count"
        );
        assert_eq!(aggregator.live_request_count(), 0);
    }

    #[test]
    fn dirty_fallback_counter_snapshots_preserve_lifecycle_load() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let observation = active_chat_observation(
            "req-fallback-live-load",
            RequestObservationState::OutputGeneration,
        );
        apply_stream_observation(&mut aggregator, &observation);
        assert!(
            aggregator
                .fallback("req-fallback-live-load", (0, 2), false, Duration::ZERO,)
                .is_empty(),
            "first fallback counter is a baseline"
        );
        let stats = aggregator
            .fallback("req-fallback-live-load", (0, 4), false, milliseconds(100))
            .pop()
            .expect("second fallback counter should publish output TPS")
            .1;
        assert_stats!(stats; output_tps: 20.0, num_running_queries: 1, total_query_input_size: 32, input_processing_queries: 0, output_generation_queries: 1);
    }

    #[test]
    fn engine_stream_snapshots_preserve_local_kv_cache_stats() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        aggregator.apply_kv_cache_stats(kv_cache_stats("model-a"));
        let stats = aggregator.stream_stats("req-stream-kv", (0, 10), true, Duration::ZERO);
        assert_stats!(stats; kv_cache_capacity_tokens: 1_000, kv_cache_used_tokens: 400, kv_cache_free_tokens: 600, stats_capabilities: ["model.throughput.engine_stream", "machine.kv_cache.http"], stats_sources: ["engine_stats_stream", "kv_cache_stats"]);
    }

    #[test]
    fn stats_aggregator_owns_lifecycle_engine_and_kv_state() {
        let config = config!(openai_fallback_stats_enabled: false);
        let mut aggregator = test_aggregator(config);
        let observation = active_chat_observation(
            "req-single-owner-lifecycle",
            RequestObservationState::OutputGeneration,
        );
        let lifecycle_stats = apply_stream_observation(&mut aggregator, &observation)
            .pop()
            .expect("lifecycle observation should publish a snapshot")
            .1;
        assert_stats!(lifecycle_stats; num_running_queries: 1, output_generation_queries: 1);
        let kv_stats = aggregator
            .apply_kv_cache_stats(kv_cache_stats("model-a"))
            .expect("KV stats should publish a snapshot")
            .1;
        assert_stats!(kv_stats; num_running_queries: 1, kv_cache_capacity_tokens: 1_000);
        aggregator.stream("req-single-owner-stream", (0, 0), false, Duration::ZERO);
        let stats = aggregator
            .stream("req-single-owner-stream", (0, 10), true, seconds(1))
            .pop()
            .expect("engine counters should publish the complete owned snapshot")
            .1;
        assert_stats!(stats; output_tps: 10.0, num_running_queries: 1, output_generation_queries: 1, kv_cache_capacity_tokens: 1_000, stats_sources: ["engine_stats_stream", "kv_cache_stats"]);
    }

    #[test]
    fn engine_stats_model_state_count_excludes_lifecycle_and_kv_only_models() {
        let config = config!(openai_fallback_stats_enabled: false);
        let mut aggregator = test_aggregator(config);
        apply_stream_observation(
            &mut aggregator,
            &active_chat_observation(
                "req-lifecycle-only",
                RequestObservationState::OutputGeneration,
            ),
        );
        aggregator.apply_kv_cache_stats(kv_cache_stats("model-b"));
        assert_eq!(aggregator.model_state_count(), 0);
        aggregator.stream("req-engine-state", (0, 0), false, Duration::ZERO);
        assert_eq!(aggregator.model_state_count(), 1);
    }

    #[test]
    fn stats_aggregator_keeps_embeddings_observation_with_stream_output_stats() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        aggregator.stream("req-stream", (0, 0), false, Duration::ZERO);
        let stats = aggregator.stream_stats("req-stream", (0, 10), true, seconds(1));
        assert_stats!(stats; output_tps: 10.0, max_output_tps: 10.0);
        let stats = sample_observations(
            &mut aggregator,
            &completed_embeddings_observation(20, 2, seconds(1), seconds(2)),
            "req-embedding",
            5,
            apply_stream_observation,
        );
        assert_stats!(stats; output_tps: 10.0, max_output_tps: 10.0, last_mean_input_tps: 0.0, embedding_item_tps: 2.0, max_embedding_item_tps: 2.0, stats_sources: ["engine_stats_stream"]);
        assert!(
            !stats
                .stats_capabilities
                .contains(&"request.embeddings_item_throughput".to_string())
        );
    }

    #[test]
    fn stream_mode_embeddings_do_not_double_count_stream_input_tps() {
        let config = config!(duration_floor: milliseconds(100));
        let mut aggregator = test_aggregator(config);
        let stats = aggregator.sample_first_stream_counters("req-stream-input", 5, (10, 0));
        assert_eq!(stats.last_mean_input_tps, 100.0);
        let stats = sample_observations(
            &mut aggregator,
            &completed_embeddings_observation(20, 2, seconds(1), seconds(2)),
            "req-embedding",
            5,
            apply_stream_observation,
        );
        assert_stats!(stats; last_mean_input_tps: 100.0, embedding_item_tps: 2.0, max_embedding_item_tps: 2.0);
    }

    #[tokio::test]
    async fn stats_collector_enables_openai_fallback_only_after_control_update() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let config = config!(collector; openai_fallback_stats_enabled: false);
        let collector = RunningCollector::spawn(config, Some(metrics.clone()), true);
        let stats = collector
            .observe_until(
                trusted_completed_observation("req-fallback-disabled"),
                "fallback-disabled observation should publish lifecycle-only stats",
                |_| true,
            )
            .await;
        assert_eq!(stats.output_tps, 0.0);
        assert!(!stats.stats_sources.contains(&"chunk_usage".to_string()));
        collector
            .send_update(StatsAggregatorUpdate::EnableOpenAiFallback)
            .await;
        wait_for_metric(
            &metrics,
            r#"pylon_engine_stats_source_transitions_total{from="engine_stats_stream",reason="unsupported",to="openai_fallback"} 1"#,
            "collector should process fallback control update before fallback observations are accepted",
        )
        .await;
        let stats = collector
            .observe_until(
                trusted_completed_observation("req-fallback-enabled"),
                "fallback-enabled observation should publish model stats",
                |stats| stats.output_tps == 5.0,
            )
            .await;
        assert_eq!(stats.output_tps, 5.0);
        assert!(stats.stats_sources.contains(&"chunk_usage".to_string()));
        collector.handle.shutdown().await;
    }

    #[tokio::test]
    async fn stats_collector_keeps_lifecycle_load_when_fallback_stats_disabled() {
        let config = config!(collector; openai_fallback_stats_enabled: false);
        let collector = RunningCollector::spawn(config, None, true);
        collector.seed_stream_output("req-prior-stream").await;
        let stats = collector
            .observe_until(
                active_chat_observation(
                    "req-stream-lifecycle",
                    RequestObservationState::InputProcessing,
                ),
                "stream mode lifecycle observation should publish stats",
                |stats| stats.input_processing_queries == 1,
            )
            .await;
        assert_stats!(stats; num_running_queries: 1, queue_size: 1, queued_input_size: 32, total_query_input_size: 32, input_processing_queries: 1, output_generation_queries: 0, output_tps: 10.0);
        collector.handle.shutdown().await;
    }

    #[tokio::test]
    async fn stats_collector_accepts_late_stream_finish_after_terminal_observation() {
        let config = config!(collector; openai_fallback_stats_enabled: false);
        let collector = RunningCollector::spawn(config, None, true);
        collector
            .send_stream("req-stream-race", 0, 0, false, Duration::ZERO)
            .await;
        collector
            .observe_until(
                identified(
                    completed_observation(32, 1, 10, milliseconds(50), seconds(1)),
                    "req-stream-race",
                ),
                "terminal observation should publish lifecycle stats",
                |_| true,
            )
            .await;
        collector
            .send_stream("req-stream-race", 0, 10, true, seconds(1))
            .await;
        collector
            .wait_for_stats("late stream finish should publish stats", |stats| {
                stats.output_tps == 10.0 && stats.max_output_tps == 10.0
            })
            .await;
        collector.handle.shutdown().await;
    }

    #[tokio::test]
    async fn request_observation_owns_a_first_late_engine_event() {
        let config = config!(collector; openai_fallback_stats_enabled: false);
        let collector = RunningCollector::spawn(config, None, true);
        collector
            .observe_until(
                identified(
                    completed_observation(32, 1, 10, milliseconds(50), seconds(1)),
                    "req-first-late-stream-event",
                ),
                "terminal observation should record exact request ownership",
                |stats| stats.stats_observed_at_unix_ms > 0,
            )
            .await;

        collector
            .send_update(stream_counter_update(
                "req-first-late-stream-event",
                None,
                32,
                10,
                true,
                collector.started_at + seconds(1),
            ))
            .await;
        collector
            .wait_for_stats(
                "late first engine event should claim its generation",
                |stats| stats.stats_sources == ["engine_stats_stream"],
            )
            .await;

        collector.handle.shutdown().await;
    }

    #[tokio::test]
    async fn stats_collector_helper_defaults_stats_stream_to_authoritative() {
        let collector =
            RunningCollector::spawn(config!(observation_channel_capacity: 16), None, true);
        collector
            .send_stream("req-helper-stream", 0, 0, false, Duration::ZERO)
            .await;
        collector.runtime_state.observe_request(identified(
            completed_observation(32, 0, 0, milliseconds(50), seconds(1)),
            "req-helper-stream",
        ));
        collector
            .send_stream("req-helper-stream", 0, 10, true, seconds(1))
            .await;
        collector
            .wait_for_stats("delayed stream finish should publish stats", |stats| {
                stats.output_tps == 10.0 && stats.max_output_tps == 10.0
            })
            .await;
        collector.handle.shutdown().await
    }

    #[tokio::test(start_paused = true)]
    async fn stats_collector_sweeps_stream_state_after_stats_receiver_closes() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let config = config!(collector;
            engine_stats_request_ttl: seconds(1),
            engine_stats_model_ttl: seconds(60),
            engine_stats_sweep_interval: seconds(1),
            openai_fallback_stats_enabled: false,
        );
        let mut collector = RunningCollector::spawn(config, Some(metrics.clone()), true);
        collector
            .send_stream("req-stream-stale", 0, 0, false, Duration::ZERO)
            .await;
        let stats_update_tx = collector
            .stats_update_tx
            .take()
            .expect("collector should have a stats update channel");
        drop(stats_update_tx);
        let label_stats = collector
            .wait_for_stats("initial stream label snapshot should publish", |stats| {
                stats.stats_sources == ["engine_stats_stream"]
            })
            .await;
        assert_eq!(label_stats.stats_sources, ["engine_stats_stream"]);
        tokio::time::advance(seconds(2)).await;
        wait_for_metric(
            &metrics,
            r#"pylon_engine_stats_live_requests{source="engine_stats_stream"} 0"#,
            "stale stream request should be swept after the receiver closes",
        )
        .await;
        collector.handle.shutdown().await;
    }

    #[tokio::test]
    async fn fallback_counter_snapshots_preserve_lifecycle_load() {
        let config = config!(observation_channel_capacity: 16);
        let collector = RunningCollector::spawn(config, None, false);
        let stats = collector
            .observe_until(
                active_chat_observation(
                    "req-fallback-live-load",
                    RequestObservationState::OutputGeneration,
                ),
                "fallback observation should publish stats",
                |stats| stats.output_generation_queries == 1,
            )
            .await;
        assert_stats!(stats; num_running_queries: 1, total_query_input_size: 32, input_processing_queries: 0, output_generation_queries: 1);
        collector.handle.shutdown().await;
    }

    #[tokio::test]
    async fn terminal_only_fallback_counter_does_not_clear_observed_output_tps() {
        let config = config!(observation_channel_capacity: 16);
        let collector = RunningCollector::spawn(config, None, false);
        collector
            .observe_until(
                trusted_completed_observation("req-terminal-only-fallback"),
                "terminal observation should publish stats",
                |stats| stats.output_tps == 5.0,
            )
            .await;
        for _ in 0..20 {
            tokio::task::yield_now().await;
        }
        let stats = collector
            .runtime_state
            .model_stats("model-a")
            .expect("terminal observation should leave model stats");
        assert_eq!(stats.output_tps, 5.0);
        collector.handle.shutdown().await;
    }

    #[tokio::test]
    async fn stats_collector_keeps_embeddings_observation_when_fallback_stats_disabled() {
        let config = config!(
            observation_channel_capacity: 32,
            openai_fallback_stats_enabled: false,
        );
        let collector = RunningCollector::spawn(config, None, true);
        collector.seed_stream_output("req-stream").await;
        for index in 0..5 {
            collector.runtime_state.observe_request(identified(
                completed_embeddings_observation(20, 2, seconds(1), seconds(2)),
                format!("req-embedding-{index}"),
            ));
        }
        let stats = collector
            .wait_for_stats(
                "embeddings observations should publish stream-mode stats",
                |stats| stats.embedding_item_tps > 0.0,
            )
            .await;
        assert_stats!(stats; output_tps: 10.0, max_output_tps: 10.0, last_mean_input_tps: 0.0, embedding_item_tps: 2.0, stats_sources: ["engine_stats_stream"]);
        collector.handle.shutdown().await;
    }

    #[test]
    fn stats_aggregator_ignores_regressions_and_post_finalize_events() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        aggregator.stream("req-final", (10, 2), false, Duration::ZERO);
        aggregator.stream("req-final", (20, 4), true, milliseconds(100));
        assert_eq!(aggregator.live_request_count(), 0);
        let updates = aggregator.stream("req-final", (30, 8), false, milliseconds(200));
        assert!(updates.is_empty());
        aggregator.stream("req-live", (20, 4), false, Duration::ZERO);
        let updates = aggregator.stream("req-live", (19, 5), false, milliseconds(100));
        assert!(updates.is_empty());
    }

    #[test]
    fn stats_aggregator_rejects_unconfigured_counter_models() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let updates = aggregator.model_counter(
            StatsUpdateSource::EngineStatsStream,
            "req-unconfigured",
            "model-b",
            (10, 4),
            false,
            Duration::ZERO,
        );
        assert!(updates.is_empty());
        assert_eq!(aggregator.live_request_count(), 0);
        assert_eq!(
            aggregator.snapshot("model-b").stats_sources,
            Vec::<String>::new()
        );
    }

    #[test]
    fn fallback_terminal_observation_without_trusted_counters_finalizes_stream_request() {
        let mut observation =
            completed_observation(11, 12, 10, milliseconds(100), milliseconds(1_000));
        observation.request_id = "req-stream-race".to_string();
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        aggregator.stream("req-stream-race", (5, 3), false, Duration::ZERO);
        assert_eq!(aggregator.live_request_count(), 1);
        let fallback_update = fallback_update_from_observation(&observation, None)
            .expect("terminal update should exist");
        let stats = aggregator
            .apply_update(fallback_update)
            .pop()
            .expect("terminal request observation should publish the finalized stream snapshot")
            .1;
        assert_eq!(stats.stats_sources, ["engine_stats_stream"]);
        assert_eq!(aggregator.live_request_count(), 0);
        let updates = aggregator.stream("req-stream-race", (11, 10), true, milliseconds(100));
        assert!(
            updates.is_empty(),
            "post-finalization stream stats must not double-count"
        );
    }

    #[test]
    fn stats_aggregator_sweeps_stale_request_and_model_state() {
        let config = config!(
            engine_stats_request_ttl: seconds(1),
            engine_stats_model_ttl: seconds(1),
        );
        let mut aggregator = test_aggregator(config);
        for tick in 0..=5 {
            aggregator.stream(
                "req-stale",
                (tick * 10, tick * 2),
                false,
                milliseconds(tick * 100),
            );
        }
        assert_eq!(aggregator.live_request_count(), 1);
        let updates = aggregator.sweep(seconds(2));
        assert_eq!(aggregator.live_request_count(), 0);
        let stats = published_stats(updates);
        assert_stats!(stats; last_mean_input_tps: 100.0, output_tps: 0.0, queue_size: 0, queued_input_size: 0, num_running_queries: 0, input_processing_queries: 0, output_generation_queries: 0, stats_sources: ["engine_stats_stream"]);
    }

    #[test]
    fn stats_aggregator_tombstones_stale_request_before_late_finish() {
        let config = config!(
            engine_stats_request_ttl: seconds(1),
            engine_stats_model_ttl: seconds(60),
        );
        let mut aggregator = test_aggregator(config);
        aggregator.stream("req-stale-late", (0, 0), false, Duration::ZERO);
        aggregator.stream("req-stale-late", (100, 10), false, milliseconds(100));
        assert_eq!(aggregator.live_request_count(), 1);
        let stale_updates = aggregator.sweep(seconds(2));
        assert_eq!(aggregator.live_request_count(), 0);
        assert!(
            stale_updates
                .iter()
                .any(|(generation, _)| generation.model_id() == "model-a"),
            "stale cleanup should publish a dirty model snapshot"
        );
        let late_updates =
            aggregator.stream("req-stale-late", (100, 20), true, milliseconds(2_100));
        assert!(
            late_updates.is_empty(),
            "late cumulative finish after stale cleanup must not be replayed from zero"
        );
    }

    #[test]
    fn stats_aggregator_request_counter_identity_has_one_lifecycle_entry() {
        let config = config!(engine_stats_request_ttl: seconds(1));
        let mut aggregator = test_aggregator(config);
        aggregator.stream("req-lifecycle", (0, 0), false, Duration::ZERO);
        assert_eq!(aggregator.live_request_count(), 1);
        assert_eq!(aggregator.request_counter_identity_count(), 1);
        aggregator.stream("req-lifecycle", (10, 2), true, milliseconds(100));
        assert_eq!(aggregator.live_request_count(), 0);
        assert_eq!(aggregator.request_counter_identity_count(), 1);
        let late_updates = aggregator.stream("req-lifecycle", (20, 4), true, milliseconds(200));
        assert!(late_updates.is_empty());
        assert_eq!(aggregator.request_counter_identity_count(), 1);
        aggregator.sweep(seconds(2));
        assert_eq!(aggregator.request_counter_identity_count(), 0);
    }

    #[test]
    fn repeated_request_finalization_refreshes_tombstone_expiry() {
        let config = config!(engine_stats_request_ttl: seconds(1));
        let mut aggregator = test_aggregator(config);
        aggregator.finalize("req-finalized-twice", Duration::ZERO);
        aggregator.finalize("req-finalized-twice", milliseconds(800));
        aggregator.sweep(milliseconds(1_500));
        let late_updates =
            aggregator.stream("req-finalized-twice", (10, 2), true, milliseconds(1_600));
        assert!(late_updates.is_empty());
        assert_eq!(aggregator.request_counter_identity_count(), 1);
        aggregator.sweep(seconds(2));
        assert_eq!(aggregator.request_counter_identity_count(), 0);
    }

    #[test]
    fn ownerless_request_finalization_creates_no_tombstone() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());

        aggregator
            .inner
            .finalize_request(FinalizeRequestUpdate::new(
                StatsUpdateSource::OpenAiFallback,
                "ownerless",
                aggregator.start,
            ));

        assert_eq!(aggregator.request_counter_identity_count(), 0);
    }

    #[test]
    fn stats_aggregator_keeps_bounded_request_state_for_many_cumulative_updates() {
        const REQUESTS: usize = 256;
        const EVENTS: usize = 10_000;
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let mut latest = vec![(0u64, 0u64); REQUESTS];
        for index in 0..EVENTS {
            let request_index = index % REQUESTS;
            let step = (index / REQUESTS + 1) as u64;
            let tokens_processed = step * 8;
            let tokens_generated = step;
            latest[request_index] = (tokens_processed, tokens_generated);
            aggregator.stream(
                &format!("req-{request_index}"),
                (tokens_processed, tokens_generated),
                false,
                milliseconds(index as u64),
            );
        }
        assert_eq!(aggregator.live_request_count(), REQUESTS);
        for (request_index, (tokens_processed, tokens_generated)) in latest.into_iter().enumerate()
        {
            aggregator.stream(
                &format!("req-{request_index}"),
                (tokens_processed, tokens_generated),
                true,
                seconds(60) + milliseconds(request_index as u64),
            );
        }
        assert_eq!(aggregator.live_request_count(), 0);
    }

    #[test]
    fn last_mean_input_tps_stays_sticky_without_new_samples() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        sample_observations(
            &mut aggregator,
            &completed_observation(20, 1, 1, seconds(2), seconds(2)),
            "req-sticky",
            5,
            apply_fallback_observation,
        );
        let stats = aggregator.snapshot("model-a");
        assert_eq!(stats.last_mean_input_tps, 10.0);
    }

    #[test]
    fn fallback_input_throughput_is_owned_by_stats_aggregator() {
        let config = StatsCollectorConfig::default();
        let mut aggregator = test_aggregator(config);
        let runtime_state = aggregator.runtime_state.clone();
        for request_index in 0..5 {
            let updates = apply_fallback_observation(
                &mut aggregator,
                &identified(
                    completed_observation(50, 1, 8, milliseconds(500), seconds(1)),
                    format!("req-openai-stream-{request_index}"),
                ),
            );
            assert_eq!(updates.len(), 1, "each observation should publish once");
            publish_model_stats_updates(&runtime_state, updates.clone());
            let stats = published_stats(updates);
            assert_eq!(
                stats.last_mean_input_tps,
                if request_index < 4 { 0.0 } else { 100.0 }
            );
        }
        let distribution = &aggregator.per_model["model-a"]
            .metrics
            .input_tps_distribution;
        assert_eq!(distribution.count, 5);
        assert_eq!(distribution.mean, 100.0);
        let _queued =
            runtime_state.track_request(&crate::request_observer::RequiredTunnelHeaders {
                request_id: "req-queued-after-fallback-samples".to_string(),
                routing_key: None,
                model_id: "model-a".to_string(),
                priority: 0,
                input_tokens: 50,
                accepted_at: std::time::Instant::now(),
            });
        runtime_state.transition_request_observation(active_chat_observation(
            "req-queued-after-fallback-samples",
            RequestObservationState::Queued,
        ));
        assert_eq!(
            runtime_state
                .snapshot_live_model("model-a")
                .queue_time_estimate_ms_by_priority,
            Some(HashMap::from([(0, 320)]))
        );
    }

    #[test]
    fn fallback_input_throughput_ignores_non_terminal_observations() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        for state in [
            RequestObservationState::InputProcessing,
            RequestObservationState::OutputGeneration,
        ] {
            let updates = apply_fallback_observation(
                &mut aggregator,
                &identified(
                    RequestObservation {
                        state,
                        ..completed_observation(50, 1, 8, milliseconds(500), seconds(1))
                    },
                    format!("req-live-{state:?}"),
                ),
            );
            assert_eq!(updates[0].1.last_mean_input_tps, 0.0);
        }
        assert_eq!(
            aggregator.per_model["model-a"]
                .metrics
                .input_tps_distribution
                .count,
            0
        );
    }

    fallback_distribution_test!(
        terminal_only_samples_use_request_duration_instead_of_tick_window,
        completed_observation(100, 1, 1, seconds(2), seconds(2)),
        "req-final-only",
        50.0
    );

    fallback_distribution_test!(
        terminal_only_samples_do_not_sum_same_tick_request_rates,
        completed_observation(100, 1, 1, milliseconds(10), milliseconds(10)),
        "req-final-only-sequential",
        10_000.0
    );

    fallback_snapshot_test!(
        completed_request_stats_keep_exact_output_rate_formula,
        StatsCollectorConfig::default(),
        completed_observation(120, 6, 30, seconds(3), seconds(9));
        last_mean_input_tps: 0.0, output_tps: 5.0, max_output_tps: 5.0
    );

    fallback_snapshot_test!(
        ignores_observations_below_duration_floor,
        config!(duration_floor: milliseconds(50)),
        completed_observation(20, 4, 8, milliseconds(10), milliseconds(20));
        last_mean_input_tps: 0.0, output_tps: 0.0
    );

    fallback_snapshot_test!(
        terminal_usage_chunks_use_first_output_for_output_tps,
        StatsCollectorConfig::default(),
        RequestObservation {
            time_to_first_token: Some(milliseconds(5_995)),
            ..completed_observation(20, 4, 8, seconds(2), seconds(6))
        };
        output_tps: 2.0, max_output_tps: 2.0
    );

    #[test]
    fn embeddings_stats_update_last_mean_input_tps_without_claiming_output_tps() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let observation = completed_embeddings_observation(20, 4, seconds(2), seconds(4));
        for request_index in 0..4 {
            let stats = single_fallback_stats(
                &mut aggregator,
                &identified(
                    observation.clone(),
                    format!("req-embedding-{request_index}"),
                ),
            );
            assert_eq!(stats.last_mean_input_tps, 0.0);
        }
        let stats =
            single_fallback_stats(&mut aggregator, &identified(observation, "req-embedding-4"));
        assert_stats!(stats; last_mean_input_tps: 10.0, output_tps: 0.0, max_output_tps: 0.0, embedding_item_tps: 2.0, max_embedding_item_tps: 2.0);
        assert_unlabeled(&stats);
        let live_chat = RequestObservation {
            request_id: "req-live-chat".to_string(),
            state: RequestObservationState::OutputGeneration,
            output_tokens: 20,
            time_to_first_output: Some(seconds(1)),
            time_to_first_token: Some(seconds(1)),
            total_duration: seconds(3),
            ..completed_observation(10, 1, 20, seconds(1), seconds(3))
        };
        let stats = single_fallback_stats(&mut aggregator, &live_chat);
        assert_eq!(stats.output_tps, 10.0);
    }

    fallback_distribution_test!(
        fast_embeddings_input_samples_clamp_to_duration_floor,
        completed_embeddings_observation(20, 4, milliseconds(1), milliseconds(4)),
        "req-fast-embedding",
        2000.0
    );

    fallback_snapshot_test!(
        embeddings_item_tps_clamps_fast_response_relay_duration,
        StatsCollectorConfig::default(),
        completed_embeddings_observation(20, 2, milliseconds(2), milliseconds(5));
        output_tps: 0.0,
        max_output_tps: 0.0,
        embedding_item_tps: 200.0,
        max_embedding_item_tps: 200.0
    );

    #[test]
    fn embeddings_stats_do_not_replace_chat_output_tps() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let chat = completed_observation(20, 1, 10, seconds(1), seconds(3));
        let stats = single_fallback_stats(&mut aggregator, &chat);
        assert_eq!(stats.output_tps, 5.0);
        let embeddings = completed_embeddings_observation(20, 2, seconds(1), seconds(2));
        let stats = single_fallback_stats(&mut aggregator, &embeddings);
        assert_stats!(stats; output_tps: 5.0, max_output_tps: 5.0, embedding_item_tps: 2.0, max_embedding_item_tps: 2.0);
        assert_unlabeled(&stats);
    }

    #[test]
    fn embeddings_observations_do_not_add_output_throughput_labels() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let chat = completed_observation(20, 1, 10, seconds(1), seconds(3));
        let stats = single_fallback_stats(&mut aggregator, &chat);
        assert_eq!(stats.output_tps, 5.0);
        let failed_embeddings = RequestObservation {
            state: RequestObservationState::Failed,
            ..completed_embeddings_observation(20, 2, seconds(1), seconds(2))
        };
        let stats = single_fallback_stats(&mut aggregator, &failed_embeddings);
        assert_eq!(
            stats.output_tps, 5.0,
            "failed embeddings requests must not replace the last completed output sample"
        );
        assert_stats!(stats; embedding_item_tps: 0.0, max_embedding_item_tps: 0.0);
        assert_unlabeled(&stats);
        let live_embeddings = RequestObservation {
            state: RequestObservationState::UpstreamConnecting,
            total_duration: Duration::ZERO,
            ..completed_embeddings_observation(20, 2, seconds(1), seconds(2))
        };
        let stats = single_fallback_stats(&mut aggregator, &live_embeddings);
        assert_stats!(stats; output_tps: 5.0, embedding_item_tps: 0.0);
        assert_unlabeled(&stats);
    }

    fallback_snapshot_test!(
        ignores_non_complete_observations,
        StatsCollectorConfig::default(),
        RequestObservation {
            state: RequestObservationState::Failed,
            ..completed_observation(20, 4, 8, seconds(2), seconds(6))
        };
        last_mean_input_tps: 0.0, output_tps: 0.0
    );

    #[test]
    fn publishes_live_queue_and_active_stats() {
        let config = StatsCollectorConfig::default();
        let mut aggregator = test_aggregator(config);
        let runtime_state = aggregator.runtime_state.clone();
        runtime_state.update_model_throughput("model-a", 100.0);
        let queued = RequestObservation {
            priority: 2,
            input_tokens: 24,
            time_to_response_headers: Some(milliseconds(5)),
            total_duration: milliseconds(5),
            ..observation(
                RequestObservationEndpoint::ChatCompletions,
                "req-live",
                RequestObservationState::InputProcessing,
            )
        };
        let queued_stats = single_fallback_stats(&mut aggregator, &queued);
        assert_stats!(queued_stats; queue_size: 1, queued_input_size: 24, num_running_queries: 1, total_query_input_size: 24, input_processing_queries: 1, output_generation_queries: 0, last_mean_input_tps: 0.0);
        assert_eq!(
            queued_stats.queue_time_estimate_ms_by_priority,
            Some(HashMap::from([(0, 240)]))
        );
        let generating = RequestObservation {
            output_messages: 2,
            output_tokens: 8,
            state: RequestObservationState::OutputGeneration,
            time_to_first_output: Some(seconds(2)),
            time_to_first_token: Some(seconds(2)),
            total_duration: seconds(3),
            ..queued
        };
        let active_stats = single_fallback_stats(&mut aggregator, &generating);
        assert_stats!(active_stats; queue_size: 0, queued_input_size: 0, num_running_queries: 1, total_query_input_size: 24, input_processing_queries: 0, output_generation_queries: 1, last_mean_input_tps: 0.0, output_tps: 8.0);
    }

    #[test]
    fn live_stats_math_is_exact_for_simultaneous_queued_and_generating_requests() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let queued = RequestObservation {
            input_tokens: 30,
            time_to_response_headers: Some(milliseconds(5)),
            total_duration: milliseconds(5),
            ..observation(
                RequestObservationEndpoint::ChatCompletions,
                "req-queued",
                RequestObservationState::InputProcessing,
            )
        };
        let generating = RequestObservation {
            input_tokens: 20,
            output_messages: 3,
            output_tokens: 6,
            time_to_response_headers: Some(milliseconds(5)),
            time_to_first_output: Some(seconds(2)),
            time_to_first_token: Some(seconds(2)),
            total_duration: seconds(5),
            ..observation(
                RequestObservationEndpoint::ChatCompletions,
                "req-generating",
                RequestObservationState::OutputGeneration,
            )
        };
        apply_fallback_observation(&mut aggregator, &queued);
        let stats = published_stats(apply_fallback_observation(&mut aggregator, &generating));
        assert_stats!(stats; queue_size: 1, queued_input_size: 30, num_running_queries: 2, total_query_input_size: 50, last_mean_input_tps: 0.0, output_tps: 2.0);
    }

    #[test]
    fn live_input_processing_keeps_full_requested_input_without_retired_progress() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let observation = RequestObservation {
            input_tokens: 100,
            time_to_response_headers: Some(seconds(2)),
            total_duration: seconds(30),
            ..observation(
                RequestObservationEndpoint::ChatCompletions,
                "req-input-processing",
                RequestObservationState::InputProcessing,
            )
        };
        let stats = single_fallback_stats(&mut aggregator, &observation);
        assert_stats!(stats; last_mean_input_tps: 0.0, queued_input_size: 100);
        assert_unlabeled(&stats);
        assert!(stats.stats_observed_at_unix_ms > 0);
    }

    fallback_snapshot_test!(
        chunk_usage_observations_claim_only_chunk_usage_stats,
        StatsCollectorConfig::default(),
        RequestObservation {
            output_tokens_explicit: true,
            output_tokens_from_chunk_usage: true,
            ..completed_observation(12, 1, 7, milliseconds(100), milliseconds(500))
        };
        stats_capabilities: ["request.output.chunk_usage"], stats_sources: ["chunk_usage"]
    );

    #[test]
    fn snapshot_includes_polled_kv_cache_stats() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let stats = aggregator
            .apply_kv_cache_stats(kv_cache_stats("model-a"))
            .expect("KV-cache stats should publish")
            .1;
        assert_stats!(stats; kv_cache_capacity_tokens: 1_000, kv_cache_used_tokens: 400, kv_cache_free_tokens: 600);
    }

    #[tokio::test]
    async fn kv_cache_poll_updates_model_metrics() {
        async fn kv_cache_stats() -> Json<serde_json::Value> {
            Json(serde_json::json!({
                "model": "model-a",
                "kv_cache_capacity_tokens": 1000,
                "kv_cache_used_tokens": 400,
                "kv_cache_free_tokens": 600
            }))
        }
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let app = Router::new().route("/kv-cache", get(kv_cache_stats));
        let (addr, server) = spawn_kv_cache_server(app).await;
        let config = config!(
            kv_cache_stats_url: Some(format!("http://{addr}/kv-cache")),
            kv_cache_poll_interval: milliseconds(10),
            kv_cache_request_timeout: seconds(1),
        );
        let collector = RunningCollector::spawn(config, Some(metrics.clone()), false);
        let stats = collector
            .wait_for_stats("KV-cache stats should be published", |stats| {
                stats.kv_cache_capacity_tokens == 1000
            })
            .await;
        assert_stats!(stats; kv_cache_capacity_tokens: 1000, kv_cache_used_tokens: 400, kv_cache_free_tokens: 600);
        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(r#"pylon_model_kv_cache_capacity_tokens{model="model-a"} 1000"#));
        assert!(body.contains(r#"pylon_model_kv_cache_used_tokens{model="model-a"} 400"#));
        assert!(body.contains(r#"pylon_model_kv_cache_free_tokens{model="model-a"} 600"#));
        tokio::time::timeout(seconds(2), collector.handle.shutdown())
            .await
            .expect("collector should stop");
        server.abort();
    }

    #[tokio::test]
    async fn stats_collector_shutdown_interrupts_blocked_kv_cache_poll() {
        let poll_entered = Arc::new(tokio::sync::Barrier::new(2));
        let server_poll_entered = poll_entered.clone();
        let app = Router::new().route(
            "/kv-cache",
            get(move || {
                let poll_entered = server_poll_entered.clone();
                async move {
                    poll_entered.wait().await;
                    std::future::pending::<Json<serde_json::Value>>().await
                }
            }),
        );
        let (addr, server) = spawn_kv_cache_server(app).await;
        let config = config!(
            kv_cache_stats_url: Some(format!("http://{addr}/kv-cache")),
            kv_cache_poll_interval: milliseconds(1),
            kv_cache_request_timeout: seconds(60),
        );
        let collector = RunningCollector::spawn(config, None, false);
        poll_entered.wait().await;
        let stopped = tokio::time::timeout(seconds(1), collector.handle.shutdown()).await;
        server.abort();
        stopped.expect("collector shutdown should interrupt blocked KV-cache poll");
    }

    #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
    async fn stats_collector_shutdown_progresses_under_sustained_stats_updates() {
        let config = config!(observation_channel_capacity: 1);
        let collector = RunningCollector::spawn(config, None, true);
        let tx = collector
            .stats_update_tx
            .as_ref()
            .expect("collector should have a stats update channel")
            .clone();
        let started_at = collector.started_at;
        let generation = collector.runtime_state.current_generation("model-a");
        let producer = tokio::spawn(async move {
            for sequence in 1.. {
                if tx
                    .send_async(stream_counter_update(
                        "continuous",
                        generation.clone(),
                        sequence,
                        sequence,
                        false,
                        started_at + milliseconds(sequence),
                    ))
                    .await
                    .is_err()
                {
                    return;
                }
            }
        });
        tokio::task::yield_now().await;
        tokio::time::timeout(seconds(1), collector.handle.shutdown())
            .await
            .expect("collector should observe shutdown despite a continuously ready receiver");
        tokio::time::timeout(seconds(1), producer)
            .await
            .expect("producer should stop")
            .expect("producer should not panic");
    }

    #[tokio::test]
    async fn stats_collector_publishes_mean_input_tps_from_completed_observations() {
        let config = config!(observation_channel_capacity: 16);
        let collector = RunningCollector::spawn(config, None, false);
        for request_index in 0..5 {
            collector.runtime_state.observe_request(identified(
                RequestObservation {
                    output_messages: 1,
                    output_tokens: 2,
                    time_to_first_output: Some(milliseconds(500)),
                    time_to_first_token: Some(milliseconds(600)),
                    total_duration: seconds(1),
                    ..completed_observation(50, 1, 2, milliseconds(500), seconds(1))
                },
                format!("req-stats-openai-{request_index}"),
            ));
        }
        tokio::task::yield_now().await;
        let stats = collector
            .wait_for_stats("mean input TPS should be published", |stats| {
                stats.last_mean_input_tps == 100.0
            })
            .await;
        assert_stats!(stats; last_mean_input_tps: 100.0, output_tps: 5.0);
        collector.handle.shutdown().await;
    }
    #[tokio::test]
    async fn stats_collector_bootstraps_input_tps_for_queue_admission() {
        let collector = RunningCollector::spawn_empty(StatsCollectorConfig::default(), None, false);
        collector
            .begin_configured_model("model-a", 2_200.0, false)
            .await;
        let stats = collector
            .wait_for_stats("bootstrap TPS stats should be published", |stats| {
                stats.last_mean_input_tps == 2_200.0
            })
            .await;
        assert_eq!(stats.last_mean_input_tps, 2_200.0);
        let _queued = collector.runtime_state.track_request(
            &crate::request_observer::RequiredTunnelHeaders {
                request_id: "req-queued".to_string(),
                routing_key: None,
                model_id: "model-a".to_string(),
                priority: 0,
                input_tokens: 32,
                accepted_at: std::time::Instant::now(),
            },
        );
        collector
            .runtime_state
            .transition_request_observation(active_chat_observation(
                "req-queued",
                RequestObservationState::Queued,
            ));
        assert_eq!(
            collector
                .runtime_state
                .snapshot_live_model("model-a")
                .queue_time_estimate_ms_by_priority,
            Some(HashMap::from([(0, 15)]))
        );
        collector.handle.shutdown().await;
    }

    #[test]
    fn records_metrics_when_configured() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let config = StatsCollectorConfig::default();
        let (runtime_state, _observation_rx) = PylonRuntimeState::observed(
            stargate_proto::pb::InferenceServerStatus::Unknown,
            &["model-a".to_string()],
            config.observation_channel_capacity,
            Some(metrics.clone()),
        );
        let mut aggregator = StatsAggregator::new(config, runtime_state.clone());
        aggregator
            .begin_generation(
                runtime_state
                    .current_generation("model-a")
                    .expect("test model should exist"),
                ModelStatsInitialization::Empty,
            )
            .expect("test model stats should initialize");
        let observation = completed_observation(20, 2, 10, seconds(2), seconds(4));
        for _ in 0..5 {
            let updated_stats = apply_fallback_observation(&mut aggregator, &observation);
            for (model_id, stats) in updated_stats {
                publish_model_stats_update(&runtime_state, model_id, stats);
            }
        }
        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(
            r#"pylon_requests_total{model="model-a",routing_key="rk-1",status="complete"} 5"#
        ));
        assert!(body.contains(r#"pylon_model_last_mean_input_tps{model="model-a"} 10"#));
        assert!(body.contains(r#"pylon_model_output_tps{model="model-a"} 5"#));
    }

    #[test]
    fn rejects_kv_cache_stats_for_unconfigured_models() {
        let runtime_state = PylonRuntimeState::new(
            stargate_proto::pb::InferenceServerStatus::Unknown,
            &["model-a".to_string()],
        );
        assert!(
            StatsAggregator::new(StatsCollectorConfig::default(), runtime_state)
                .apply_kv_cache_stats(kv_cache_stats("model-b"))
                .is_none()
        );
    }

    #[tokio::test]
    async fn stats_collector_owns_exact_generation_initialization_and_retirement() {
        let config = config!(observation_channel_capacity: 16);
        let (runtime_state, observation_rx) = PylonRuntimeState::observed(
            stargate_proto::pb::InferenceServerStatus::Active,
            &[],
            config.observation_channel_capacity,
            None,
        );
        let collector = start_stats_collector(config, observation_rx, runtime_state.clone());
        let control = collector.control();
        let first = crate::runtime_state::ModelGeneration::new("model-a", 1);
        let replacement = crate::runtime_state::ModelGeneration::new("model-a", 2);

        assert!(runtime_state.begin_generation(first.clone()));
        assert!(
            control
                .begin_generation(
                    first.clone(),
                    ModelStatsInitialization::ConfiguredInputTps {
                        input_tps: 100.0,
                        pin: false,
                    },
                )
                .await
                .expect("stats collector should acknowledge initialization")
        );
        assert_eq!(
            control
                .flush_and_snapshot(&first)
                .await
                .expect("snapshot request should complete")
                .expect("current generation should have stats")
                .last_mean_input_tps,
            100.0
        );
        assert!(
            control
                .retire_generation(&first)
                .await
                .expect("retirement should be acknowledged")
        );
        assert!(runtime_state.retire_generation(&first).is_some());
        assert!(runtime_state.begin_generation(replacement.clone()));
        assert!(
            control
                .begin_generation(replacement.clone(), ModelStatsInitialization::Empty)
                .await
                .expect("replacement initialization should be acknowledged")
        );

        assert!(
            control
                .flush_and_snapshot(&first)
                .await
                .expect("stale snapshot request should complete")
                .is_none()
        );
        assert_eq!(
            control
                .flush_and_snapshot(&replacement)
                .await
                .expect("replacement snapshot request should complete"),
            Some(CurrentModelStats::default())
        );
        collector.shutdown().await;
    }

    #[tokio::test]
    async fn exact_generation_snapshot_drains_already_observed_calibration_traffic() {
        let config = StatsCollectorConfig {
            duration_floor: Duration::ZERO,
            openai_fallback_stats_enabled: true,
            ..StatsCollectorConfig::default()
        };
        let (runtime_state, observation_rx) = PylonRuntimeState::observed(
            stargate_proto::pb::InferenceServerStatus::Active,
            &["model-a".to_string()],
            config.observation_channel_capacity,
            None,
        );
        let generation = runtime_state
            .current_generation("model-a")
            .expect("test generation should exist");
        for index in 0..5 {
            runtime_state.observe_request(identified(
                completed_observation(100, 1, 1, seconds(1), seconds(2)),
                format!("calibration-{index}"),
            ));
        }
        let collector = start_stats_collector(config, observation_rx, runtime_state);

        let snapshot = collector
            .control()
            .flush_and_snapshot(&generation)
            .await
            .expect("snapshot request should complete")
            .expect("current generation should have stats");

        assert!(snapshot.last_mean_input_tps > 0.0);
        collector.shutdown().await;
    }

    #[test]
    fn exact_generation_engine_events_seed_once_and_never_cross_retirement() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let retired = aggregator
            .current_generation("model-a")
            .cloned()
            .expect("initial generation should exist");
        let calibration_request =
            next_generated_request_id(GeneratedRequestKind::Calibration, &retired);

        apply_stream_observation(
            &mut aggregator,
            &identified(
                completed_observation(100, 1, 1, seconds(1), seconds(2)),
                calibration_request.clone(),
            ),
        );
        assert_eq!(
            aggregator.per_model["model-a"]
                .metrics
                .input_tps_distribution
                .count,
            1,
            "the calibration observer must record the request before duplicate engine events arrive"
        );

        aggregator.stream(&calibration_request, (0, 0), false, Duration::ZERO);
        aggregator.stream(&calibration_request, (100, 0), false, seconds(1));
        aggregator.stream(&calibration_request, (100, 0), false, seconds(1));

        assert_eq!(
            aggregator.per_model["model-a"]
                .metrics
                .input_tps_distribution
                .count,
            1,
            "repeated cumulative engine events must not double-count calibration traffic"
        );

        aggregator.stream("finished-calibration", (0, 0), false, Duration::ZERO);
        aggregator.stream("finished-calibration", (100, 0), true, seconds(1));
        assert_eq!(aggregator.inner.request_counter_identity_count(), 2);
        assert_eq!(aggregator.inner.live_request_count(), 0);

        assert!(
            aggregator
                .runtime_state
                .retire_generation(&retired)
                .is_some()
        );
        aggregator.sample_first_stream_counters("retirement-race", 5, (100, 0));
        assert!(
            aggregator
                .runtime_state
                .current_generation("model-a")
                .is_none(),
            "an old stats sample must not recreate a model after runtime retirement"
        );
        assert!(aggregator.retire_generation(&retired));
        assert_eq!(
            aggregator.inner.request_counter_identity_count(),
            0,
            "retirement must purge live and finalized exact-generation request identities"
        );
        assert_eq!(aggregator.inner.live_request_count(), 0);
        let replacement = crate::runtime_state::ModelGeneration::new("model-a", 99);
        assert!(
            aggregator
                .runtime_state
                .begin_generation(replacement.clone())
        );
        aggregator
            .begin_generation(replacement.clone(), ModelStatsInitialization::Empty)
            .expect("replacement stats generation should initialize");
        let late_observed_at = aggregator.start + seconds(2);
        aggregator.apply_update(StatsAggregatorUpdate::RequestCounters(
            RequestCounterUpdate {
                source: StatsUpdateSource::EngineStatsStream,
                request_id: "late-calibration-request".to_string(),
                model_id: "model-a".to_string(),
                generation: Some(retired),
                tokens_processed: Some(1_000),
                tokens_generated: Some(0),
                finished: true,
                observed_at: late_observed_at,
            },
        ));

        assert_eq!(
            aggregator.per_model["model-a"]
                .metrics
                .input_tps_distribution
                .count,
            0,
            "late retired-generation events must not seed a replacement"
        );
        assert_eq!(aggregator.current_generation("model-a"), Some(&replacement));
    }

    #[test]
    fn foreign_scope_calibration_like_engine_id_is_not_deduplicated() {
        let mut aggregator = test_aggregator(StatsCollectorConfig::default());
        let request_id = format!(
            "calibration-{}-g1-1",
            uuid::Uuid::from_u128(0x12345678123456781234567812345678)
        );

        aggregator.stream(&request_id, (0, 0), false, Duration::ZERO);
        aggregator.stream(&request_id, (100, 0), true, seconds(1));

        assert_eq!(
            aggregator.per_model["model-a"]
                .metrics
                .input_tps_distribution
                .count,
            1,
            "only calibration IDs owned by this process may bypass engine counters"
        );
    }

    #[test]
    fn stale_generation_publish_cannot_recreate_replacement_metrics() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let runtime_state = PylonRuntimeState::observed(
            stargate_proto::pb::InferenceServerStatus::Active,
            &[],
            4,
            Some(metrics.clone()),
        )
        .0;
        let retired = crate::runtime_state::ModelGeneration::new("model-a", 1);
        let replacement = crate::runtime_state::ModelGeneration::new("model-a", 2);
        assert!(runtime_state.begin_generation(retired.clone()));
        assert!(runtime_state.retire_generation(&retired).is_some());
        assert!(runtime_state.begin_generation(replacement.clone()));
        publish_model_stats_update(
            &runtime_state,
            replacement,
            CurrentModelStats {
                last_mean_input_tps: 10.0,
                ..CurrentModelStats::default()
            },
        );

        publish_model_stats_update(
            &runtime_state,
            retired,
            CurrentModelStats {
                last_mean_input_tps: 999.0,
                ..CurrentModelStats::default()
            },
        );

        assert_eq!(
            runtime_state
                .model_stats("model-a")
                .expect("replacement stats should remain present")
                .last_mean_input_tps,
            10.0
        );
        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(r#"pylon_model_last_mean_input_tps{model="model-a"} 10"#));
        assert!(!body.contains(r#"pylon_model_last_mean_input_tps{model="model-a"} 999"#));
    }
}

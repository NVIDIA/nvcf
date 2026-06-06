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

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use tokio::sync::watch;
use tokio::task::JoinHandle;
use tokio::time::Instant as TokioInstant;

use crate::queue_admission::QueueAdmissionTracker;
use crate::{CurrentModelStats, RequestObservation};

use super::aggregator::{
    InFlightRequestState, KvCacheStatsSnapshot, ModelMetricsState, SharedStatsAggregator,
    current_unix_millis, effective_last_mean_input_tps, fixed_last_mean_input_tps,
    valid_last_mean_input_tps,
};
use super::mean_input_tps::{
    MeanInputTpsAggregatorConfig, MeanInputTpsObservation, run_mean_input_tps_aggregator,
};
use super::metrics::PylonMetrics;
use super::projection::{
    attach_lifecycle_load, fallback_updates_from_observation, record_lifecycle_observation,
    record_observation, shared_snapshots_with_lifecycle_load, snapshot_model_stats,
    stream_mode_observation_updates_from_observation,
};

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
    pub configured_model_ids: Vec<String>,
    /// Pins input throughput for deterministic benchmarks instead of publishing learned samples.
    pub fixed_last_mean_input_tps: Option<f64>,
    pub kv_cache_stats_url: Option<String>,
    pub kv_cache_poll_interval: Duration,
    pub kv_cache_request_timeout: Duration,
    pub engine_stats_request_ttl: Duration,
    pub engine_stats_model_ttl: Duration,
    pub engine_stats_sweep_interval: Duration,
    pub openai_fallback_stats_enabled: bool,
    pub queue_tracker: QueueAdmissionTracker,
    pub metrics: Option<Arc<PylonMetrics>>,
}

impl Default for StatsCollectorConfig {
    fn default() -> Self {
        Self {
            observation_channel_capacity: DEFAULT_OBSERVATION_CHANNEL_CAPACITY,
            smoothing_window_size: DEFAULT_SMOOTHING_WINDOW_SIZE,
            min_input_tokens: DEFAULT_MIN_INPUT_TOKENS,
            min_output_tokens: DEFAULT_MIN_OUTPUT_TOKENS,
            duration_floor: DEFAULT_DURATION_FLOOR,
            configured_model_ids: Vec::new(),
            fixed_last_mean_input_tps: None,
            kv_cache_stats_url: None,
            kv_cache_poll_interval: DEFAULT_KV_CACHE_POLL_INTERVAL,
            kv_cache_request_timeout: DEFAULT_KV_CACHE_REQUEST_TIMEOUT,
            engine_stats_request_ttl: DEFAULT_ENGINE_STATS_REQUEST_TTL,
            engine_stats_model_ttl: DEFAULT_ENGINE_STATS_MODEL_TTL,
            engine_stats_sweep_interval: DEFAULT_ENGINE_STATS_SWEEP_INTERVAL,
            openai_fallback_stats_enabled: true,
            queue_tracker: QueueAdmissionTracker::default(),
            metrics: None,
        }
    }
}

pub fn request_observation_channel(
    config: &StatsCollectorConfig,
) -> (
    flume::Sender<RequestObservation>,
    flume::Receiver<RequestObservation>,
) {
    flume::bounded(config.observation_channel_capacity)
}

pub fn stats_aggregator_update_channel(
    config: &StatsCollectorConfig,
) -> (
    flume::Sender<StatsAggregatorUpdate>,
    flume::Receiver<StatsAggregatorUpdate>,
) {
    flume::bounded(config.observation_channel_capacity)
}

pub struct StatsCollectorHandle {
    task: JoinHandle<()>,
}

impl StatsCollectorHandle {
    pub async fn shutdown(self) {
        self.task.abort();
        let _ = self.task.await;
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
    RequestObservation(RequestObservationStatsUpdate),
    FinalizeRequest(FinalizeRequestUpdate),
    EnableOpenAiFallback,
}

#[derive(Debug, Clone)]
pub struct RequestCounterUpdate {
    pub(crate) source: StatsUpdateSource,
    pub(crate) request_id: String,
    pub(crate) model_id: String,
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
        let RequestCounterUpdateInput {
            source,
            request_id,
            model_id,
            tokens_processed,
            tokens_generated,
            finished,
            observed_at,
        } = input;
        Self {
            source,
            request_id,
            model_id,
            tokens_processed,
            tokens_generated,
            finished,
            observed_at,
        }
    }
}

#[derive(Debug, Clone)]
pub struct RequestObservationStatsUpdate {
    pub(crate) model_id: String,
    pub(crate) input_tokens: Option<u64>,
    pub(crate) input_duration: Option<Duration>,
    pub(crate) clamp_input_duration_to_floor: bool,
    pub(crate) embedding_items: Option<u64>,
    pub(crate) embedding_duration: Option<Duration>,
}

#[derive(Debug, Clone)]
pub struct FinalizeRequestUpdate {
    pub(crate) source: StatsUpdateSource,
    pub(crate) request_id: String,
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
            observed_at,
        }
    }
}

pub fn start_stats_collector(
    config: StatsCollectorConfig,
    observation_rx: flume::Receiver<RequestObservation>,
    model_stats_tx: flume::Sender<(String, CurrentModelStats)>,
    stop_rx: watch::Receiver<bool>,
) -> StatsCollectorHandle {
    start_stats_collector_with_engine_stats(config, observation_rx, None, model_stats_tx, stop_rx)
}

pub fn start_stats_collector_with_engine_stats(
    mut config: StatsCollectorConfig,
    observation_rx: flume::Receiver<RequestObservation>,
    stats_update_rx: Option<flume::Receiver<StatsAggregatorUpdate>>,
    model_stats_tx: flume::Sender<(String, CurrentModelStats)>,
    stop_rx: watch::Receiver<bool>,
) -> StatsCollectorHandle {
    if stats_update_rx.is_some() {
        // A wired engine stats stream is the throughput source of truth. Auto
        // mode falls back only after the stream task sends EnableOpenAiFallback.
        config.openai_fallback_stats_enabled = false;
    }
    let task = tokio::spawn(async move {
        run_stats_collector(
            config,
            observation_rx,
            stats_update_rx,
            model_stats_tx,
            stop_rx,
        )
        .await;
    });
    StatsCollectorHandle { task }
}

#[derive(Clone, Copy)]
enum ModelStatsSendMode {
    Await,
    TryFirst,
}

async fn send_model_stats_update(
    config: &StatsCollectorConfig,
    model_stats_tx: &flume::Sender<(String, CurrentModelStats)>,
    model_id: String,
    stats: CurrentModelStats,
    reason: &'static str,
    mode: ModelStatsSendMode,
) -> bool {
    observe_model_metric(config, &model_id, &stats);
    match mode {
        ModelStatsSendMode::Await => {
            let log_model_id = model_id.clone();
            if let Err(error) = model_stats_tx.send_async((model_id, stats)).await {
                tracing::warn!(
                    model_id = %log_model_id,
                    reason,
                    error = %error,
                    "dropping model stats update"
                );
                return false;
            }
        }
        ModelStatsSendMode::TryFirst => match model_stats_tx.try_send((model_id, stats)) {
            Ok(()) => {}
            Err(flume::TrySendError::Full(update)) => {
                let log_model_id = update.0.clone();
                if let Err(error) = model_stats_tx.send_async(update).await {
                    tracing::warn!(
                        model_id = %log_model_id,
                        reason,
                        error = %error,
                        "dropping model stats update"
                    );
                    return false;
                }
            }
            Err(flume::TrySendError::Disconnected((model_id, _))) => {
                tracing::warn!(
                    model_id = %model_id,
                    reason,
                    "dropping model stats update after receiver closed"
                );
                return false;
            }
        },
    }
    true
}

async fn send_model_stats_updates(
    config: &StatsCollectorConfig,
    model_stats_tx: &flume::Sender<(String, CurrentModelStats)>,
    updates: Vec<(String, CurrentModelStats)>,
    reason: &'static str,
    mode: ModelStatsSendMode,
) -> bool {
    for (model_id, stats) in updates {
        if !send_model_stats_update(config, model_stats_tx, model_id, stats, reason, mode).await {
            return false;
        }
    }
    true
}

async fn run_stats_collector(
    config: StatsCollectorConfig,
    observation_rx: flume::Receiver<RequestObservation>,
    mut stats_update_rx: Option<flume::Receiver<StatsAggregatorUpdate>>,
    model_stats_tx: flume::Sender<(String, CurrentModelStats)>,
    mut stop_rx: watch::Receiver<bool>,
) {
    let mut per_model = HashMap::<String, ModelMetricsState>::new();
    let mut in_flight = HashMap::<String, InFlightRequestState>::new();
    if let Some(last_mean_input_tps) = fixed_last_mean_input_tps(&config) {
        for model_id in &config.configured_model_ids {
            config
                .queue_tracker
                .update_model_throughput(model_id, last_mean_input_tps);
            let stats = snapshot_model_stats(&config, &mut per_model, &in_flight, model_id);
            if !send_model_stats_update(
                &config,
                &model_stats_tx,
                model_id.clone(),
                stats,
                "configured fixed input TPS stats",
                ModelStatsSendMode::Await,
            )
            .await
            {
                return;
            }
        }
    }
    let mut shared_aggregator = SharedStatsAggregator::new(config.clone());
    let (mean_input_tps_tx, mean_input_tps_rx) =
        flume::bounded(config.observation_channel_capacity);
    // This carries thresholded per-model mean updates, not raw request observations. Keep it
    // non-blocking so bounded input backpressure cannot deadlock the collector and aggregator.
    let (mean_input_tps_update_tx, mean_input_tps_update_rx) = flume::unbounded();
    let mean_input_tps_config = MeanInputTpsAggregatorConfig::from(&config);
    let mean_input_tps_task = tokio::spawn(run_mean_input_tps_aggregator(
        mean_input_tps_config,
        mean_input_tps_rx,
        mean_input_tps_update_tx,
    ));
    let http_client = reqwest::Client::new();
    let mut kv_cache_poll = tokio::time::interval(config.kv_cache_poll_interval);
    kv_cache_poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    let mut engine_stats_sweep = tokio::time::interval(config.engine_stats_sweep_interval);
    engine_stats_sweep.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
    let mut openai_fallback_stats_enabled = config.openai_fallback_stats_enabled;
    let mut stats_aggregator_updated_models = Vec::with_capacity(2);
    let mut stats_aggregator_latest_models = Vec::with_capacity(2);

    'collector: loop {
        tokio::select! {
            _ = stop_rx.changed() => {
                if *stop_rx.borrow() {
                    break 'collector;
                }
            }
            observation = observation_rx.recv_async() => {
                let Ok(observation) = observation else {
                    break 'collector;
                };
                if openai_fallback_stats_enabled {
                    let mean_input_observation =
                        MeanInputTpsObservation::from_request_observation(&observation);
                    if let Err(error) = mean_input_tps_tx.send_async(mean_input_observation).await {
                        tracing::warn!(error = %error, "stopping stats collector after mean input TPS aggregator closed");
                        break 'collector;
                    }
                    let updated_models = record_observation(
                        &config,
                        &mut per_model,
                        &mut in_flight,
                        &observation,
                    );
                    observe_request_metric(&config, &observation);
                    if !send_model_stats_updates(
                        &config,
                        &model_stats_tx,
                        updated_models,
                        "collected stats",
                        ModelStatsSendMode::Await,
                    )
                    .await
                    {
                        break 'collector;
                    }
                } else {
                    observe_request_metric(&config, &observation);
                    let updated_model_ids = record_lifecycle_observation(
                        &config,
                        &mut per_model,
                        &mut in_flight,
                        &observation,
                    );
                    let updated_models = shared_snapshots_with_lifecycle_load(
                        &config,
                        &mut per_model,
                        &in_flight,
                        &shared_aggregator,
                        updated_model_ids,
                    );
                    if !send_model_stats_updates(
                        &config,
                        &model_stats_tx,
                        updated_models,
                        "stream-mode request lifecycle stats",
                        ModelStatsSendMode::Await,
                    )
                    .await
                    {
                        break 'collector;
                    }
                    for update in stream_mode_observation_updates_from_observation(&observation) {
                        let mut updated_models = shared_aggregator.apply_update(update);
                        attach_lifecycle_load(&config, &mut per_model, &in_flight, &mut updated_models);
                        if !send_model_stats_updates(
                            &config,
                            &model_stats_tx,
                            updated_models,
                            "stream-mode request observation stats aggregator update",
                            ModelStatsSendMode::Await,
                        )
                        .await
                        {
                            break 'collector;
                        }
                    }
                }
                if openai_fallback_stats_enabled {
                    for update in fallback_updates_from_observation(&observation) {
                        let mut updated_models = shared_aggregator.apply_update(update);
                        attach_lifecycle_load(&config, &mut per_model, &in_flight, &mut updated_models);
                        if !send_model_stats_updates(
                            &config,
                            &model_stats_tx,
                            updated_models,
                            "fallback stats aggregator update",
                            ModelStatsSendMode::Await,
                        )
                        .await
                        {
                            break 'collector;
                        }
                    }
                }
            }
            update = mean_input_tps_update_rx.recv_async() => {
                let Ok(update) = update else {
                    break 'collector;
                };
                if !valid_last_mean_input_tps(update.last_mean_input_tps) {
                    continue;
                }
                let last_mean_input_tps =
                    effective_last_mean_input_tps(&config, update.last_mean_input_tps);
                let model_state = per_model.entry(update.model_id.clone()).or_default();
                model_state.last_mean_input_tps = last_mean_input_tps;
                config
                    .queue_tracker
                    .update_model_throughput(&update.model_id, last_mean_input_tps);
                let updated_stats = snapshot_model_stats(&config, &mut per_model, &in_flight, &update.model_id);
                if !send_model_stats_update(
                    &config,
                    &model_stats_tx,
                    update.model_id,
                    updated_stats,
                    "collected mean input TPS stats",
                    ModelStatsSendMode::Await,
                )
                .await
                {
                    break 'collector;
                }
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
                if apply_engine_stats_control_update(
                    &config,
                    &mut openai_fallback_stats_enabled,
                    &update,
                ) {
                    continue;
                }
                stats_aggregator_updated_models.clear();
                shared_aggregator.apply_update_into(update, &mut stats_aggregator_updated_models);
                if let Some(rx) = &stats_update_rx {
                    while let Ok(update) = rx.try_recv() {
                        if apply_engine_stats_control_update(
                            &config,
                            &mut openai_fallback_stats_enabled,
                            &update,
                        ) {
                            continue;
                        }
                        shared_aggregator.apply_update_into(
                            update,
                            &mut stats_aggregator_updated_models,
                        );
                    }
                }
                retain_latest_model_updates(
                    &mut stats_aggregator_updated_models,
                    &mut stats_aggregator_latest_models,
                );
                attach_lifecycle_load(
                    &config,
                    &mut per_model,
                    &in_flight,
                    &mut stats_aggregator_updated_models,
                );
                if let Some(metrics) = &config.metrics {
                    metrics.observe_engine_stats_live_requests(
                        "engine_stats_stream",
                        shared_aggregator.live_request_count(),
                    );
                    metrics.observe_engine_stats_model_states(
                        "engine_stats_stream",
                        shared_aggregator.model_state_count(),
                    );
                }
                if !send_model_stats_updates(
                    &config,
                    &model_stats_tx,
                    std::mem::take(&mut stats_aggregator_updated_models),
                    "collected engine stats stream stats",
                    ModelStatsSendMode::TryFirst,
                )
                .await
                {
                    break 'collector;
                }
            }
            _ = engine_stats_sweep.tick() => {
                let mut updated_models = shared_aggregator.sweep_stale(TokioInstant::now());
                attach_lifecycle_load(&config, &mut per_model, &in_flight, &mut updated_models);
                if let Some(metrics) = &config.metrics {
                    metrics.observe_engine_stats_live_requests(
                        "engine_stats_stream",
                        shared_aggregator.live_request_count(),
                    );
                }
                if !send_model_stats_updates(
                    &config,
                    &model_stats_tx,
                    updated_models,
                    "stale engine stats cleanup update",
                    ModelStatsSendMode::Await,
                )
                .await
                {
                    break 'collector;
                }
            }
            _ = kv_cache_poll.tick(), if config.kv_cache_stats_url.is_some() => {
                let Some(kv_cache) = poll_kv_cache_stats(&config, &http_client).await else {
                    continue;
                };
                if kv_cache.model.is_empty() {
                    tracing::warn!("dropping KV-cache stats without model id");
                    continue;
                }
                if !kv_cache_stats_model_allowed(&config, &kv_cache) {
                    tracing::warn!(
                        model_id = %kv_cache.model,
                        configured_models = ?config.configured_model_ids,
                        "dropping KV-cache stats for unconfigured model"
                    );
                    continue;
                }
                let model_id = kv_cache.model.clone();
                let model_state = per_model.entry(model_id.clone()).or_default();
                model_state.kv_cache = kv_cache;
                model_state.kv_cache_stats_observed = true;
                model_state.stats_observed_at_unix_ms = current_unix_millis();
                let updated_stats =
                    snapshot_model_stats(&config, &mut per_model, &in_flight, &model_id);
                if !send_model_stats_update(
                    &config,
                    &model_stats_tx,
                    model_id,
                    updated_stats,
                    "collected KV-cache stats",
                    ModelStatsSendMode::Await,
                )
                .await
                {
                    break 'collector;
                }
            }
        }
    }

    mean_input_tps_task.abort();
    let _ = mean_input_tps_task.await;
}

fn kv_cache_stats_model_allowed(
    config: &StatsCollectorConfig,
    kv_cache: &KvCacheStatsSnapshot,
) -> bool {
    config.configured_model_ids.is_empty()
        || config
            .configured_model_ids
            .iter()
            .any(|model_id| model_id == &kv_cache.model)
}

async fn poll_kv_cache_stats(
    config: &StatsCollectorConfig,
    http_client: &reqwest::Client,
) -> Option<KvCacheStatsSnapshot> {
    let url = config.kv_cache_stats_url.as_ref()?;
    let response = match http_client
        .get(url)
        .timeout(config.kv_cache_request_timeout)
        .send()
        .await
    {
        Ok(response) => response,
        Err(error) => {
            tracing::warn!(url, error = %error, "failed to poll KV-cache stats");
            return None;
        }
    };
    if !response.status().is_success() {
        tracing::warn!(url, status = %response.status(), "KV-cache stats endpoint returned non-success status");
        return None;
    }
    match response.json().await {
        Ok(stats) => Some(stats),
        Err(error) => {
            tracing::warn!(url, error = %error, "failed to parse KV-cache stats");
            None
        }
    }
}

fn observe_request_metric(config: &StatsCollectorConfig, observation: &RequestObservation) {
    let Some(metrics) = &config.metrics else {
        return;
    };

    metrics.observe_request_observation(observation);
}

fn observe_model_metric(config: &StatsCollectorConfig, model_id: &str, stats: &CurrentModelStats) {
    let Some(metrics) = &config.metrics else {
        return;
    };

    metrics.observe_model_stats(model_id, stats);
}

fn apply_engine_stats_control_update(
    config: &StatsCollectorConfig,
    openai_fallback_stats_enabled: &mut bool,
    update: &StatsAggregatorUpdate,
) -> bool {
    if !matches!(update, StatsAggregatorUpdate::EnableOpenAiFallback) {
        return false;
    }
    if !*openai_fallback_stats_enabled {
        *openai_fallback_stats_enabled = true;
        tracing::warn!("OpenAI fallback stats enabled after engine stats stream was unsupported");
        if let Some(metrics) = &config.metrics {
            metrics.observe_engine_stats_source_transition(
                "engine_stats_stream",
                "openai_fallback",
                "unsupported",
            );
        }
    }
    true
}

fn retain_latest_model_updates(
    updates: &mut Vec<(String, CurrentModelStats)>,
    scratch: &mut Vec<(String, CurrentModelStats)>,
) {
    scratch.clear();
    while let Some(update) = updates.pop() {
        if !scratch.iter().any(|(model_id, _)| model_id == &update.0) {
            scratch.push(update);
        }
    }
    while let Some(update) = scratch.pop() {
        updates.push(update);
    }
}
#[cfg(test)]
mod tests {
    use super::super::aggregator::{
        KvCacheStatsSnapshot, ModelMetricsState, SharedStatsAggregator,
    };
    use super::super::mean_input_tps::{
        MeanInputTpsAggregator, MeanInputTpsAggregatorConfig, MeanInputTpsObservation,
        MeanInputTpsUpdate,
    };
    use super::super::projection::{
        fallback_updates_from_observation, record_lifecycle_observation, record_observation,
        snapshot_model_stats, stream_mode_observation_updates_from_observation,
    };
    use super::*;
    use crate::request_observer::RequestObservationEndpoint;
    use crate::request_observer::RequestObservationState;
    use axum::{Json, Router, routing::get};
    use tokio::net::TcpListener;

    fn completed_observation(
        input_tokens: u64,
        output_messages: u64,
        output_tokens: u64,
        time_to_first_output: Duration,
        total_duration: Duration,
    ) -> RequestObservation {
        RequestObservation {
            endpoint: crate::request_observer::RequestObservationEndpoint::ChatCompletions,
            request_id: "req-1".to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: Some(200),
            output_messages,
            output_tokens,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
            state: RequestObservationState::Complete,
            time_to_response_headers: Some(Duration::from_millis(20)),
            time_to_first_output: Some(time_to_first_output),
            time_to_first_token: Some(time_to_first_output),
            total_duration,
        }
    }

    fn completed_embeddings_observation(
        input_tokens: u64,
        embedding_items: u64,
        time_to_response_headers: Duration,
        total_duration: Duration,
    ) -> RequestObservation {
        RequestObservation {
            endpoint: crate::request_observer::RequestObservationEndpoint::Embeddings,
            request_id: "req-embedding".to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens,
            embedding_items,
            embedding_items_observed: true,
            upstream_status: Some(200),
            output_messages: 0,
            output_tokens: 0,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
            state: RequestObservationState::Complete,
            time_to_response_headers: Some(time_to_response_headers),
            time_to_first_output: None,
            time_to_first_token: None,
            total_duration,
        }
    }

    fn active_chat_observation(
        request_id: &str,
        state: RequestObservationState,
    ) -> RequestObservation {
        let time_to_first_output = (state == RequestObservationState::OutputGeneration)
            .then_some(Duration::from_millis(50));
        RequestObservation {
            endpoint: crate::request_observer::RequestObservationEndpoint::ChatCompletions,
            request_id: request_id.to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens: 32,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: Some(200),
            output_messages: 1,
            output_tokens: 2,
            output_tokens_explicit: true,
            output_tokens_from_chunk_usage: true,
            state,
            time_to_response_headers: Some(Duration::from_millis(10)),
            time_to_first_output,
            time_to_first_token: time_to_first_output,
            total_duration: Duration::from_millis(100),
        }
    }

    async fn receive_mean_input_update(
        update_rx: &flume::Receiver<MeanInputTpsUpdate>,
    ) -> MeanInputTpsUpdate {
        for _ in 0..20 {
            if let Ok(update) = update_rx.try_recv() {
                return update;
            }
            tokio::task::yield_now().await;
        }
        panic!("mean input TPS update was not published");
    }

    fn mean_input_observation(observation: &RequestObservation) -> MeanInputTpsObservation {
        MeanInputTpsObservation::from_request_observation(observation)
    }

    #[test]
    fn latest_model_update_retention_keeps_last_snapshot_per_model() {
        let mut updates = vec![
            (
                "model-a".to_string(),
                CurrentModelStats {
                    output_tps: 1.0,
                    ..Default::default()
                },
            ),
            (
                "model-b".to_string(),
                CurrentModelStats {
                    output_tps: 2.0,
                    ..Default::default()
                },
            ),
            (
                "model-a".to_string(),
                CurrentModelStats {
                    output_tps: 3.0,
                    ..Default::default()
                },
            ),
        ];
        let mut scratch = Vec::new();

        retain_latest_model_updates(&mut updates, &mut scratch);

        assert_eq!(updates.len(), 2);
        assert_eq!(updates[0].0, "model-b");
        assert_eq!(updates[0].1.output_tps, 2.0);
        assert_eq!(updates[1].0, "model-a");
        assert_eq!(updates[1].1.output_tps, 3.0);
        assert!(scratch.is_empty());
    }

    fn stream_counter_update(
        request_id: &str,
        tokens_processed: u64,
        tokens_generated: u64,
        finished: bool,
        observed_at: TokioInstant,
    ) -> StatsAggregatorUpdate {
        StatsAggregatorUpdate::RequestCounters(RequestCounterUpdate {
            source: StatsUpdateSource::EngineStatsStream,
            request_id: request_id.to_string(),
            model_id: "model-a".to_string(),
            tokens_processed: Some(tokens_processed),
            tokens_generated: Some(tokens_generated),
            finished,
            observed_at,
        })
    }

    fn stream_counter_partial_update(
        request_id: &str,
        tokens_processed: Option<u64>,
        tokens_generated: Option<u64>,
        finished: bool,
        observed_at: TokioInstant,
    ) -> StatsAggregatorUpdate {
        StatsAggregatorUpdate::RequestCounters(RequestCounterUpdate {
            source: StatsUpdateSource::EngineStatsStream,
            request_id: request_id.to_string(),
            model_id: "model-a".to_string(),
            tokens_processed,
            tokens_generated,
            finished,
            observed_at,
        })
    }

    fn fallback_counter_update(
        request_id: &str,
        tokens_processed: u64,
        tokens_generated: u64,
        finished: bool,
        observed_at: TokioInstant,
    ) -> StatsAggregatorUpdate {
        StatsAggregatorUpdate::RequestCounters(RequestCounterUpdate {
            source: StatsUpdateSource::OpenAiFallback,
            request_id: request_id.to_string(),
            model_id: "model-a".to_string(),
            tokens_processed: Some(tokens_processed),
            tokens_generated: Some(tokens_generated),
            finished,
            observed_at,
        })
    }

    async fn receive_model_stats_with_last_mean_input_tps(
        model_stats_rx: &flume::Receiver<(String, CurrentModelStats)>,
        expected_last_mean_input_tps: f64,
    ) -> CurrentModelStats {
        tokio::time::timeout(Duration::from_secs(2), async {
            loop {
                let (model_id, stats) = model_stats_rx
                    .recv_async()
                    .await
                    .expect("model stats channel should stay open");
                if model_id == "model-a"
                    && stats.last_mean_input_tps == expected_last_mean_input_tps
                {
                    return stats;
                }
            }
        })
        .await
        .expect("model stats with expected last_mean_input_tps were not published")
    }

    #[test]
    fn stats_stream_cumulative_request_counters_drive_shared_aggregator() {
        let config = StatsCollectorConfig::default();
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        aggregator.apply_update(stream_counter_update("req-a", 0, 0, false, start));
        let updates = aggregator.apply_update(stream_counter_update(
            "req-a",
            10,
            4,
            false,
            start + Duration::from_millis(100),
        ));
        let stats = updates
            .into_iter()
            .find(|(model_id, _)| model_id == "model-a")
            .expect("model stats should update")
            .1;

        assert_eq!(stats.output_tps, 40.0);
        assert_eq!(stats.max_output_tps, 40.0);
        assert_eq!(stats.stats_sources, vec!["engine_stats_stream".to_string()]);

        for tick in 2..=5 {
            let updates = aggregator.apply_update(stream_counter_update(
                "req-a",
                tick * 10,
                4,
                false,
                start + Duration::from_millis(tick * 100),
            ));
            if tick < 5 {
                continue;
            }
            let stats = updates
                .into_iter()
                .find(|(model_id, _)| model_id == "model-a")
                .expect("fifth input sample should publish sticky mean")
                .1;
            assert_eq!(stats.last_mean_input_tps, 100.0);
        }
    }

    #[test]
    fn fixed_input_tps_is_preserved_across_engine_stats_updates() {
        let config = StatsCollectorConfig {
            fixed_last_mean_input_tps: Some(2_200.0),
            ..Default::default()
        };
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        let stats = aggregator
            .apply_update(stream_counter_update("req-a", 0, 0, false, start))
            .pop()
            .expect("first stream update should publish source labels")
            .1;
        assert_eq!(stats.last_mean_input_tps, 2_200.0);

        let mut published = None;
        for tick in 1..=5 {
            published = aggregator
                .apply_update(stream_counter_update(
                    "req-a",
                    tick * 10,
                    0,
                    false,
                    start + Duration::from_millis(tick * 100),
                ))
                .pop()
                .map(|(_, stats)| stats)
                .or(published);
        }
        assert_eq!(
            published
                .expect("sufficient input samples should publish stats")
                .last_mean_input_tps,
            2_200.0
        );
    }

    #[test]
    fn first_engine_stream_counter_without_zero_baseline_contributes_tps() {
        let config = StatsCollectorConfig {
            duration_floor: Duration::from_millis(100),
            ..Default::default()
        };
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        let stats = aggregator
            .apply_update(stream_counter_update(
                "req-first-output",
                0,
                10,
                true,
                start,
            ))
            .pop()
            .expect("first output counter should publish stats")
            .1;
        assert_eq!(stats.output_tps, 100.0);
        assert_eq!(stats.max_output_tps, 100.0);

        let mut latest = None;
        for index in 0..5 {
            latest = aggregator
                .apply_update(stream_counter_update(
                    &format!("req-first-input-{index}"),
                    10,
                    0,
                    true,
                    start + Duration::from_secs(index + 1),
                ))
                .pop()
                .map(|(_, stats)| stats);
        }
        let stats = latest.expect("fifth first input counter should publish mean input stats");
        assert_eq!(stats.last_mean_input_tps, 100.0);
    }

    #[test]
    fn first_post_baseline_engine_stream_delta_under_floor_contributes_tps() {
        let config = StatsCollectorConfig {
            duration_floor: Duration::from_millis(100),
            ..Default::default()
        };
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        let label_stats = aggregator
            .apply_update(stream_counter_update("req-fast", 0, 0, false, start))
            .pop()
            .expect("first engine stream event should publish source labels")
            .1;
        assert_eq!(
            label_stats.stats_sources,
            vec!["engine_stats_stream".to_string()]
        );
        assert_eq!(label_stats.output_tps, 0.0);

        let stats = aggregator
            .apply_update(stream_counter_update(
                "req-fast",
                0,
                10,
                true,
                start + Duration::from_millis(1),
            ))
            .pop()
            .expect("first real counter delta should publish stats");
        assert_eq!(stats.1.output_tps, 100.0);
        assert_eq!(stats.1.max_output_tps, 100.0);
    }

    #[test]
    fn engine_stream_sub_floor_deltas_accumulate_after_fast_first_sample() {
        let config = StatsCollectorConfig {
            duration_floor: Duration::from_millis(10),
            ..Default::default()
        };
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        aggregator.apply_update(stream_counter_update("req-live", 0, 0, false, start));
        let first_stats = aggregator
            .apply_update(stream_counter_update(
                "req-live",
                0,
                1,
                false,
                start + Duration::from_millis(1),
            ))
            .pop()
            .expect("first fast counter delta should publish with the duration floor")
            .1;
        assert_eq!(first_stats.output_tps, 100.0);
        assert_eq!(first_stats.max_output_tps, 100.0);

        for tick in 2..10 {
            let updates = aggregator.apply_update(stream_counter_update(
                "req-live",
                0,
                tick,
                false,
                start + Duration::from_millis(tick),
            ));
            assert!(
                updates.is_empty(),
                "sub-floor deltas should accumulate without publishing noisy snapshots"
            );
        }

        let stats = aggregator
            .apply_update(stream_counter_update(
                "req-live",
                0,
                11,
                false,
                start + Duration::from_millis(11),
            ))
            .pop()
            .expect("accumulated sub-floor deltas should publish once the sample window is valid")
            .1;
        assert_eq!(stats.max_output_tps, 1_000.0);
        assert_eq!(stats.output_tps, 550.0);
    }

    #[test]
    fn engine_stream_missing_counter_fields_do_not_sample_stale_dimensions() {
        let config = StatsCollectorConfig {
            duration_floor: Duration::from_millis(10),
            ..Default::default()
        };
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        aggregator.apply_update(stream_counter_partial_update(
            "req-partial",
            None,
            Some(0),
            false,
            start,
        ));
        let first_stats = aggregator
            .apply_update(stream_counter_partial_update(
                "req-partial",
                None,
                Some(1),
                false,
                start + Duration::from_millis(1),
            ))
            .pop()
            .expect("first output counter should publish with the duration floor")
            .1;
        assert_eq!(first_stats.output_tps, 100.0);

        assert!(
            aggregator
                .apply_update(stream_counter_partial_update(
                    "req-partial",
                    None,
                    Some(2),
                    false,
                    start + Duration::from_millis(2),
                ))
                .is_empty(),
            "second output counter is still below the duration floor"
        );

        let input_only_updates = aggregator.apply_update(stream_counter_partial_update(
            "req-partial",
            Some(1),
            None,
            false,
            start + Duration::from_millis(11),
        ));
        assert!(
            input_only_updates.is_empty(),
            "input-only updates must not publish a stale output TPS sample"
        );
    }

    #[test]
    fn engine_stream_sub_minimum_deltas_accumulate_until_publishable() {
        let config = StatsCollectorConfig {
            duration_floor: Duration::from_millis(10),
            min_output_tokens: 5,
            ..Default::default()
        };
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        aggregator.apply_update(stream_counter_update("req-min", 0, 0, false, start));
        for tick in 1..10 {
            let updates = aggregator.apply_update(stream_counter_update(
                "req-min",
                0,
                tick,
                false,
                start + Duration::from_millis(tick),
            ));
            assert!(
                updates.is_empty(),
                "tokens below the minimum or duration floor should remain accumulated"
            );
        }

        let stats = aggregator
            .apply_update(stream_counter_update(
                "req-min",
                0,
                10,
                false,
                start + Duration::from_millis(10),
            ))
            .pop()
            .expect("accumulated tokens should publish after reaching the floor")
            .1;
        assert_eq!(stats.output_tps, 1_000.0);
        assert_eq!(stats.max_output_tps, 1_000.0);
    }

    #[test]
    fn fallback_and_stream_cumulative_counters_share_stats_math() {
        let config = StatsCollectorConfig::default();
        let start = TokioInstant::now();
        let mut stream_aggregator = SharedStatsAggregator::new(config.clone());
        let mut fallback_aggregator = SharedStatsAggregator::new(config);

        for tick in 0..=5 {
            let observed_at = start + Duration::from_millis(tick * 100);
            let tokens_processed = tick * 10;
            let tokens_generated = tick * 2;
            let stream_updates = stream_aggregator.apply_update(stream_counter_update(
                "req-shared",
                tokens_processed,
                tokens_generated,
                tick == 5,
                observed_at,
            ));
            let fallback_updates = fallback_aggregator.apply_update(fallback_counter_update(
                "req-shared",
                tokens_processed,
                tokens_generated,
                tick == 5,
                observed_at,
            ));
            if tick == 0 {
                assert_eq!(stream_updates.len(), 1);
                assert!(fallback_updates.is_empty());
                continue;
            }
            assert_eq!(stream_updates.len(), fallback_updates.len());
            for ((_, stream_stats), (_, fallback_stats)) in
                stream_updates.iter().zip(fallback_updates.iter())
            {
                assert_eq!(
                    stream_stats.last_mean_input_tps,
                    fallback_stats.last_mean_input_tps
                );
                assert_eq!(stream_stats.output_tps, fallback_stats.output_tps);
                assert_eq!(stream_stats.max_output_tps, fallback_stats.max_output_tps);
            }
        }
    }

    #[test]
    fn dirty_fallback_counter_snapshots_preserve_lifecycle_load() {
        let config = StatsCollectorConfig::default();
        let start = TokioInstant::now();
        let mut aggregator = SharedStatsAggregator::new(config.clone());
        let mut per_model: HashMap<String, ModelMetricsState> = HashMap::new();
        let mut in_flight = HashMap::new();
        let observation = active_chat_observation(
            "req-fallback-live-load",
            RequestObservationState::OutputGeneration,
        );

        record_lifecycle_observation(&config, &mut per_model, &mut in_flight, &observation);
        assert!(
            aggregator
                .apply_update(fallback_counter_update(
                    "req-fallback-live-load",
                    0,
                    2,
                    false,
                    start
                ))
                .is_empty(),
            "first fallback counter is a baseline"
        );

        let mut updates = aggregator.apply_update(fallback_counter_update(
            "req-fallback-live-load",
            0,
            4,
            false,
            start + Duration::from_millis(100),
        ));
        attach_lifecycle_load(&config, &mut per_model, &in_flight, &mut updates);
        let stats = updates
            .pop()
            .expect("second fallback counter should publish output TPS")
            .1;

        assert_eq!(stats.output_tps, 20.0);
        assert_eq!(stats.num_running_queries, 1);
        assert_eq!(stats.total_query_input_size, 32);
        assert_eq!(stats.input_processing_queries, 0);
        assert_eq!(stats.output_generation_queries, 1);
    }

    #[test]
    fn engine_stream_snapshots_preserve_local_kv_cache_stats() {
        let config = StatsCollectorConfig::default();
        let start = TokioInstant::now();
        let mut aggregator = SharedStatsAggregator::new(config.clone());
        let mut updates =
            aggregator.apply_update(stream_counter_update("req-stream-kv", 0, 10, true, start));
        let mut per_model: HashMap<String, ModelMetricsState> = HashMap::new();
        let model_state = per_model.entry("model-a".to_string()).or_default();
        model_state.kv_cache = KvCacheStatsSnapshot {
            model: "model-a".to_string(),
            kv_cache_capacity_tokens: 1_000,
            kv_cache_used_tokens: 400,
            kv_cache_free_tokens: 600,
        };
        model_state.kv_cache_stats_observed = true;
        let in_flight = HashMap::new();

        attach_lifecycle_load(&config, &mut per_model, &in_flight, &mut updates);
        let stats = updates
            .pop()
            .expect("stream counter should publish stats with local KV overlay")
            .1;

        assert_eq!(stats.kv_cache_capacity_tokens, 1_000);
        assert_eq!(stats.kv_cache_used_tokens, 400);
        assert_eq!(stats.kv_cache_free_tokens, 600);
        assert_eq!(
            stats.stats_capabilities,
            vec![
                "model.throughput.engine_stream".to_string(),
                "machine.kv_cache.http".to_string(),
            ]
        );
        assert_eq!(
            stats.stats_sources,
            vec![
                "engine_stats_stream".to_string(),
                "kv_cache_stats".to_string(),
            ]
        );
    }

    #[test]
    fn shared_aggregator_keeps_embeddings_observation_with_stream_output_stats() {
        let config = StatsCollectorConfig::default();
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        aggregator.apply_update(stream_counter_update("req-stream", 0, 0, false, start));
        let stats = aggregator
            .apply_update(stream_counter_update(
                "req-stream",
                0,
                10,
                true,
                start + Duration::from_secs(1),
            ))
            .pop()
            .expect("stream output counters should publish stats")
            .1;
        assert_eq!(stats.output_tps, 10.0);
        assert_eq!(stats.max_output_tps, 10.0);

        let mut latest = None;
        for index in 0..5 {
            let observation = RequestObservation {
                request_id: format!("req-embedding-{index}"),
                ..completed_embeddings_observation(
                    20,
                    2,
                    Duration::from_secs(1),
                    Duration::from_secs(2),
                )
            };
            for update in stream_mode_observation_updates_from_observation(&observation) {
                latest = aggregator
                    .apply_update(update)
                    .pop()
                    .map(|(_, stats)| stats);
            }
        }

        let stats = latest.expect("fifth embeddings observation should publish stats");
        assert_eq!(stats.output_tps, 10.0);
        assert_eq!(stats.max_output_tps, 10.0);
        assert_eq!(stats.last_mean_input_tps, 0.0);
        assert_eq!(stats.embedding_item_tps, 2.0);
        assert_eq!(stats.max_embedding_item_tps, 2.0);
        assert_eq!(stats.stats_sources, vec!["engine_stats_stream".to_string()]);
        assert!(
            !stats
                .stats_capabilities
                .contains(&"request.embeddings_item_throughput".to_string())
        );
    }

    #[test]
    fn stream_mode_embeddings_do_not_double_count_stream_input_tps() {
        let config = StatsCollectorConfig {
            duration_floor: Duration::from_millis(100),
            ..Default::default()
        };
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        let mut latest = None;
        for index in 0..5 {
            latest = aggregator
                .apply_update(stream_counter_update(
                    &format!("req-stream-input-{index}"),
                    10,
                    0,
                    true,
                    start + Duration::from_secs(index + 1),
                ))
                .pop()
                .map(|(_, stats)| stats);
        }
        let stats = latest.expect("stream input counters should publish mean input stats");
        assert_eq!(stats.last_mean_input_tps, 100.0);

        let mut latest = None;
        for index in 0..5 {
            let observation = RequestObservation {
                request_id: format!("req-embedding-{index}"),
                ..completed_embeddings_observation(
                    20,
                    2,
                    Duration::from_secs(1),
                    Duration::from_secs(2),
                )
            };
            for update in stream_mode_observation_updates_from_observation(&observation) {
                latest = aggregator
                    .apply_update(update)
                    .pop()
                    .map(|(_, stats)| stats);
            }
        }

        let stats = latest.expect("embeddings observations should publish item throughput");
        assert_eq!(stats.last_mean_input_tps, 100.0);
        assert_eq!(stats.embedding_item_tps, 2.0);
        assert_eq!(stats.max_embedding_item_tps, 2.0);
    }

    #[tokio::test]
    async fn stats_collector_enables_openai_fallback_only_after_control_update() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let config = StatsCollectorConfig {
            observation_channel_capacity: 16,
            openai_fallback_stats_enabled: false,
            metrics: Some(metrics.clone()),
            ..Default::default()
        };
        let (observation_tx, observation_rx) = request_observation_channel(&config);
        let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(16);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = tokio::spawn(run_stats_collector(
            config,
            observation_rx,
            Some(stats_update_rx),
            model_stats_tx,
            stop_rx,
        ));

        let fallback_observation = RequestObservation {
            request_id: "req-fallback-disabled".to_string(),
            output_tokens_explicit: true,
            output_tokens_from_chunk_usage: true,
            ..completed_observation(20, 1, 10, Duration::from_secs(1), Duration::from_secs(3))
        };
        observation_tx
            .send_async(fallback_observation)
            .await
            .expect("collector should receive fallback-disabled observation");
        let (_model_id, stats) =
            tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
                .await
                .expect("fallback-disabled observation should publish lifecycle-only stats")
                .expect("collector should publish model stats");
        assert_eq!(stats.output_tps, 0.0);
        assert!(!stats.stats_sources.contains(&"chunk_usage".to_string()));

        stats_update_tx
            .send_async(StatsAggregatorUpdate::EnableOpenAiFallback)
            .await
            .expect("collector should receive fallback control update");
        for _ in 0..50 {
            let body = metrics.gather_text().expect("metrics should encode");
            if body.contains(
                r#"pylon_engine_stats_source_transitions_total{from="engine_stats_stream",reason="unsupported",to="openai_fallback"} 1"#,
            ) {
                break;
            }
            tokio::task::yield_now().await;
        }
        let body = metrics.gather_text().expect("metrics should encode");
        assert!(
            body.contains(
                r#"pylon_engine_stats_source_transitions_total{from="engine_stats_stream",reason="unsupported",to="openai_fallback"} 1"#
            ),
            "collector should process fallback control update before fallback observations are accepted"
        );
        observation_tx
            .send_async(RequestObservation {
                request_id: "req-fallback-enabled".to_string(),
                output_tokens_explicit: true,
                output_tokens_from_chunk_usage: true,
                ..completed_observation(20, 1, 10, Duration::from_secs(1), Duration::from_secs(3))
            })
            .await
            .expect("collector should receive fallback-enabled observation");

        let (_model_id, stats) =
            tokio::time::timeout(Duration::from_secs(2), model_stats_rx.recv_async())
                .await
                .expect("fallback-enabled observation should publish model stats")
                .expect("collector should publish model stats");
        assert_eq!(stats.output_tps, 5.0);
        assert!(stats.stats_sources.contains(&"chunk_usage".to_string()));

        stop_tx.send(true).expect("collector should receive stop");
        collector.await.expect("collector task should join");
    }

    #[tokio::test]
    async fn stats_collector_keeps_lifecycle_load_when_fallback_stats_disabled() {
        let config = StatsCollectorConfig {
            observation_channel_capacity: 16,
            openai_fallback_stats_enabled: false,
            ..Default::default()
        };
        let (observation_tx, observation_rx) = request_observation_channel(&config);
        let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(16);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = tokio::spawn(run_stats_collector(
            config,
            observation_rx,
            Some(stats_update_rx),
            model_stats_tx,
            stop_rx,
        ));

        let start = TokioInstant::now();
        stats_update_tx
            .send_async(stream_counter_update(
                "req-prior-stream",
                0,
                0,
                false,
                start,
            ))
            .await
            .expect("collector should receive stream start");
        stats_update_tx
            .send_async(stream_counter_update(
                "req-prior-stream",
                0,
                10,
                true,
                start + Duration::from_secs(1),
            ))
            .await
            .expect("collector should receive stream finish");
        let mut saw_stream_output = false;
        for _ in 0..10 {
            let (_model_id, stats) =
                tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
                    .await
                    .expect("stream finish should publish stats")
                    .expect("collector should keep model stats channel open");
            if stats.output_tps == 10.0 {
                saw_stream_output = true;
                break;
            }
        }
        assert!(saw_stream_output);

        observation_tx
            .send_async(active_chat_observation(
                "req-stream-lifecycle",
                RequestObservationState::InputProcessing,
            ))
            .await
            .expect("collector should receive stream-mode lifecycle observation");

        let (model_id, stats) =
            tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
                .await
                .expect("stream mode lifecycle observation should publish stats")
                .expect("collector should keep model stats channel open");
        assert_eq!(model_id, "model-a");
        assert_eq!(stats.num_running_queries, 1);
        assert_eq!(stats.queue_size, 1);
        assert_eq!(stats.queued_input_size, 32);
        assert_eq!(stats.total_query_input_size, 32);
        assert_eq!(stats.input_processing_queries, 1);
        assert_eq!(stats.output_generation_queries, 0);
        assert_eq!(stats.output_tps, 10.0);

        stop_tx.send(true).expect("collector should receive stop");
        collector.await.expect("collector task should join");
    }

    #[tokio::test]
    async fn stats_collector_accepts_late_stream_finish_after_terminal_observation() {
        let config = StatsCollectorConfig {
            observation_channel_capacity: 16,
            openai_fallback_stats_enabled: false,
            ..Default::default()
        };
        let (observation_tx, observation_rx) = request_observation_channel(&config);
        let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(16);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = tokio::spawn(run_stats_collector(
            config,
            observation_rx,
            Some(stats_update_rx),
            model_stats_tx,
            stop_rx,
        ));

        let start = TokioInstant::now();
        stats_update_tx
            .send_async(stream_counter_update("req-stream-race", 0, 0, false, start))
            .await
            .expect("collector should receive stream start");

        let mut terminal_observation =
            completed_observation(32, 1, 10, Duration::from_millis(50), Duration::from_secs(1));
        terminal_observation.request_id = "req-stream-race".to_string();
        observation_tx
            .send_async(terminal_observation)
            .await
            .expect("collector should receive terminal request observation");
        tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
            .await
            .expect("terminal observation should publish lifecycle stats")
            .expect("collector should keep model stats channel open");

        stats_update_tx
            .send_async(stream_counter_update(
                "req-stream-race",
                0,
                10,
                true,
                start + Duration::from_secs(1),
            ))
            .await
            .expect("collector should receive late stream finish");

        let mut saw_final_stream_stats = false;
        for _ in 0..10 {
            let (_model_id, stats) =
                tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
                    .await
                    .expect("late stream finish should publish stats")
                    .expect("collector should keep model stats channel open");
            if stats.output_tps == 10.0 && stats.max_output_tps == 10.0 {
                saw_final_stream_stats = true;
                break;
            }
        }
        assert!(saw_final_stream_stats);

        stop_tx.send(true).expect("collector should receive stop");
        collector.await.expect("collector task should join");
    }

    #[tokio::test]
    async fn stats_collector_helper_defaults_stats_stream_to_authoritative() {
        let config = StatsCollectorConfig {
            observation_channel_capacity: 16,
            ..Default::default()
        };
        let (observation_tx, observation_rx) = request_observation_channel(&config);
        let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(16);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = start_stats_collector_with_engine_stats(
            config,
            observation_rx,
            Some(stats_update_rx),
            model_stats_tx,
            stop_rx,
        );

        let start = TokioInstant::now();
        stats_update_tx
            .send_async(stream_counter_update(
                "req-helper-stream",
                0,
                0,
                false,
                start,
            ))
            .await
            .expect("collector should receive stream start");

        let mut terminal_observation =
            completed_observation(32, 0, 0, Duration::from_millis(50), Duration::from_secs(1));
        terminal_observation.request_id = "req-helper-stream".to_string();
        observation_tx
            .send_async(terminal_observation)
            .await
            .expect("collector should receive terminal request observation");

        stats_update_tx
            .send_async(stream_counter_update(
                "req-helper-stream",
                0,
                10,
                true,
                start + Duration::from_secs(1),
            ))
            .await
            .expect("collector should receive delayed stream finish");

        let mut saw_final_stream_stats = false;
        for _ in 0..10 {
            let (_model_id, stats) =
                tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
                    .await
                    .expect("delayed stream finish should publish stats")
                    .expect("collector should keep model stats channel open");
            if stats.output_tps == 10.0 && stats.max_output_tps == 10.0 {
                saw_final_stream_stats = true;
                break;
            }
        }
        assert!(saw_final_stream_stats);

        stop_tx.send(true).expect("collector should receive stop");
        collector.shutdown().await;
    }

    #[tokio::test(start_paused = true)]
    async fn stats_collector_sweeps_stream_state_after_stats_receiver_closes() {
        let config = StatsCollectorConfig {
            observation_channel_capacity: 16,
            engine_stats_request_ttl: Duration::from_secs(1),
            engine_stats_model_ttl: Duration::from_secs(60),
            engine_stats_sweep_interval: Duration::from_secs(1),
            openai_fallback_stats_enabled: false,
            ..Default::default()
        };
        let (_observation_tx, observation_rx) = request_observation_channel(&config);
        let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(16);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = tokio::spawn(run_stats_collector(
            config,
            observation_rx,
            Some(stats_update_rx),
            model_stats_tx,
            stop_rx,
        ));

        let start = TokioInstant::now();
        stats_update_tx
            .send_async(stream_counter_update(
                "req-stream-stale",
                0,
                0,
                false,
                start,
            ))
            .await
            .expect("collector should receive stream start");
        drop(stats_update_tx);

        let (_model_id, label_stats) = model_stats_rx
            .recv_async()
            .await
            .expect("initial stream label snapshot should publish");
        assert_eq!(
            label_stats.stats_sources,
            vec!["engine_stats_stream".to_string()]
        );

        tokio::time::advance(Duration::from_secs(2)).await;
        tokio::task::yield_now().await;

        let mut stale_snapshot = None;
        for _ in 0..50 {
            if let Ok((model_id, stats)) = model_stats_rx.try_recv()
                && model_id == "model-a"
            {
                stale_snapshot = Some(stats);
                break;
            }
            tokio::task::yield_now().await;
        }
        let stats = stale_snapshot.expect("stale stream request should be swept after close");
        assert_eq!(stats.stats_sources, vec!["engine_stats_stream".to_string()]);
        assert_eq!(stats.num_running_queries, 0);

        stop_tx.send(true).expect("collector should receive stop");
        collector.await.expect("collector task should join");
    }

    #[tokio::test]
    async fn fallback_counter_snapshots_preserve_lifecycle_load() {
        let config = StatsCollectorConfig {
            observation_channel_capacity: 16,
            ..Default::default()
        };
        let (observation_tx, observation_rx) = request_observation_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(16);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = tokio::spawn(run_stats_collector(
            config,
            observation_rx,
            None,
            model_stats_tx,
            stop_rx,
        ));

        observation_tx
            .send_async(active_chat_observation(
                "req-fallback-live-load",
                RequestObservationState::OutputGeneration,
            ))
            .await
            .expect("collector should receive fallback observation");

        let mut snapshots = Vec::new();
        let (model_id, stats) =
            tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
                .await
                .expect("fallback observation should publish stats")
                .expect("collector should keep model stats channel open");
        assert_eq!(model_id, "model-a");
        snapshots.push(stats);

        for _ in 0..20 {
            while let Ok((model_id, stats)) = model_stats_rx.try_recv() {
                if model_id == "model-a" {
                    snapshots.push(stats);
                }
            }
            tokio::task::yield_now().await;
        }

        for stats in snapshots {
            assert_eq!(stats.num_running_queries, 1);
            assert_eq!(stats.total_query_input_size, 32);
            assert_eq!(stats.input_processing_queries, 0);
            assert_eq!(stats.output_generation_queries, 1);
        }

        stop_tx.send(true).expect("collector should receive stop");
        collector.await.expect("collector task should join");
    }

    #[tokio::test]
    async fn terminal_only_fallback_counter_does_not_clear_observed_output_tps() {
        let config = StatsCollectorConfig {
            observation_channel_capacity: 16,
            ..Default::default()
        };
        let (observation_tx, observation_rx) = request_observation_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(16);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = tokio::spawn(run_stats_collector(
            config,
            observation_rx,
            None,
            model_stats_tx,
            stop_rx,
        ));

        observation_tx
            .send_async(RequestObservation {
                request_id: "req-terminal-only-fallback".to_string(),
                output_tokens_explicit: true,
                output_tokens_from_chunk_usage: true,
                ..completed_observation(20, 1, 10, Duration::from_secs(1), Duration::from_secs(3))
            })
            .await
            .expect("collector should receive terminal-only fallback observation");

        let mut output_tps_values = Vec::new();
        let (model_id, stats) =
            tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
                .await
                .expect("terminal observation should publish stats")
                .expect("collector should keep model stats channel open");
        assert_eq!(model_id, "model-a");
        output_tps_values.push(stats.output_tps);

        for _ in 0..20 {
            while let Ok((model_id, stats)) = model_stats_rx.try_recv() {
                if model_id == "model-a" {
                    output_tps_values.push(stats.output_tps);
                }
            }
            tokio::task::yield_now().await;
        }

        assert!(!output_tps_values.is_empty());
        assert!(
            output_tps_values
                .iter()
                .all(|output_tps| *output_tps == 5.0),
            "terminal-only fallback stats must not publish a later zero output TPS snapshot: {output_tps_values:?}"
        );

        stop_tx.send(true).expect("collector should receive stop");
        collector.await.expect("collector task should join");
    }

    #[tokio::test]
    async fn stats_collector_keeps_embeddings_observation_when_fallback_stats_disabled() {
        let config = StatsCollectorConfig {
            observation_channel_capacity: 32,
            openai_fallback_stats_enabled: false,
            ..Default::default()
        };
        let (observation_tx, observation_rx) = request_observation_channel(&config);
        let (stats_update_tx, stats_update_rx) = stats_aggregator_update_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(32);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = tokio::spawn(run_stats_collector(
            config,
            observation_rx,
            Some(stats_update_rx),
            model_stats_tx,
            stop_rx,
        ));

        let start = TokioInstant::now();
        stats_update_tx
            .send_async(stream_counter_update("req-stream", 0, 0, false, start))
            .await
            .expect("collector should receive stream start");
        stats_update_tx
            .send_async(stream_counter_update(
                "req-stream",
                0,
                10,
                true,
                start + Duration::from_secs(1),
            ))
            .await
            .expect("collector should receive stream finish");

        let mut saw_stream_output = false;
        for _ in 0..10 {
            let (_model_id, stats) =
                tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
                    .await
                    .expect("collector should publish stream stats")
                    .expect("collector should keep model stats channel open");
            if stats.output_tps == 10.0 && stats.max_output_tps == 10.0 {
                saw_stream_output = true;
                break;
            }
        }
        assert!(saw_stream_output);

        for index in 0..5 {
            observation_tx
                .send_async(RequestObservation {
                    request_id: format!("req-embedding-{index}"),
                    ..completed_embeddings_observation(
                        20,
                        2,
                        Duration::from_secs(1),
                        Duration::from_secs(2),
                    )
                })
                .await
                .expect("collector should receive embeddings observation");
        }

        let mut latest = None;
        for _ in 0..20 {
            let (_model_id, stats) =
                tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
                    .await
                    .expect("collector should publish embeddings stats")
                    .expect("collector should keep model stats channel open");
            if stats.embedding_item_tps > 0.0 {
                latest = Some(stats);
                break;
            }
        }

        let stats = latest.expect("embeddings observations should publish stream-mode stats");
        assert_eq!(stats.output_tps, 10.0);
        assert_eq!(stats.max_output_tps, 10.0);
        assert_eq!(stats.last_mean_input_tps, 0.0);
        assert_eq!(stats.embedding_item_tps, 2.0);
        assert_eq!(stats.stats_sources, vec!["engine_stats_stream".to_string()]);

        stop_tx.send(true).expect("collector should receive stop");
        collector.await.expect("collector task should join");
    }

    #[test]
    fn shared_aggregator_ignores_regressions_and_post_finalize_events() {
        let config = StatsCollectorConfig::default();
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        aggregator.apply_update(stream_counter_update("req-final", 10, 2, false, start));
        aggregator.apply_update(stream_counter_update(
            "req-final",
            20,
            4,
            true,
            start + Duration::from_millis(100),
        ));
        assert_eq!(aggregator.live_request_count(), 0);

        let updates = aggregator.apply_update(stream_counter_update(
            "req-final",
            30,
            8,
            false,
            start + Duration::from_millis(200),
        ));
        assert!(updates.is_empty());

        aggregator.apply_update(stream_counter_update("req-live", 20, 4, false, start));
        let updates = aggregator.apply_update(stream_counter_update(
            "req-live",
            19,
            5,
            false,
            start + Duration::from_millis(100),
        ));
        assert!(updates.is_empty());
    }

    #[test]
    fn fallback_terminal_observation_without_trusted_counters_finalizes_stream_request() {
        let mut observation = completed_observation(
            11,
            12,
            10,
            Duration::from_millis(100),
            Duration::from_millis(1_000),
        );
        observation.request_id = "req-stream-race".to_string();

        let config = StatsCollectorConfig::default();
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();
        aggregator.apply_update(stream_counter_update("req-stream-race", 5, 3, false, start));
        assert_eq!(aggregator.live_request_count(), 1);

        let fallback_updates = fallback_updates_from_observation(&observation);
        assert_eq!(fallback_updates.len(), 1);
        let stats = aggregator
            .apply_update(fallback_updates.into_iter().next().unwrap())
            .pop()
            .expect("terminal request observation should publish the finalized stream snapshot")
            .1;

        assert_eq!(stats.stats_sources, vec!["engine_stats_stream".to_string()]);
        assert_eq!(aggregator.live_request_count(), 0);

        let updates = aggregator.apply_update(stream_counter_update(
            "req-stream-race",
            11,
            10,
            true,
            start + Duration::from_millis(100),
        ));
        assert!(
            updates.is_empty(),
            "post-finalization stream stats must not double-count"
        );
    }

    #[test]
    fn shared_aggregator_sweeps_stale_request_and_model_state() {
        let config = StatsCollectorConfig {
            engine_stats_request_ttl: Duration::from_secs(1),
            engine_stats_model_ttl: Duration::from_secs(1),
            ..Default::default()
        };
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        for tick in 0..=5 {
            aggregator.apply_update(stream_counter_update(
                "req-stale",
                tick * 10,
                tick * 2,
                false,
                start + Duration::from_millis(tick * 100),
            ));
        }
        assert_eq!(aggregator.live_request_count(), 1);

        let updates = aggregator.sweep_stale(start + Duration::from_secs(2));
        assert_eq!(aggregator.live_request_count(), 0);
        let stats = updates
            .into_iter()
            .find(|(model_id, _)| model_id == "model-a")
            .expect("stale cleanup should publish a dirty model snapshot")
            .1;

        assert_eq!(stats.last_mean_input_tps, 100.0);
        assert_eq!(stats.output_tps, 0.0);
        assert_eq!(stats.queue_size, 0);
        assert_eq!(stats.queued_input_size, 0);
        assert_eq!(stats.num_running_queries, 0);
        assert_eq!(stats.input_processing_queries, 0);
        assert_eq!(stats.output_generation_queries, 0);
        assert_eq!(stats.stats_sources, vec!["engine_stats_stream".to_string()]);
    }

    #[test]
    fn shared_aggregator_tombstones_stale_request_before_late_finish() {
        let config = StatsCollectorConfig {
            engine_stats_request_ttl: Duration::from_secs(1),
            engine_stats_model_ttl: Duration::from_secs(60),
            ..Default::default()
        };
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();

        aggregator.apply_update(stream_counter_update("req-stale-late", 0, 0, false, start));
        aggregator.apply_update(stream_counter_update(
            "req-stale-late",
            100,
            10,
            false,
            start + Duration::from_millis(100),
        ));
        assert_eq!(aggregator.live_request_count(), 1);

        let stale_updates = aggregator.sweep_stale(start + Duration::from_secs(2));
        assert_eq!(aggregator.live_request_count(), 0);
        assert!(
            stale_updates
                .iter()
                .any(|(model_id, _)| model_id == "model-a"),
            "stale cleanup should publish a dirty model snapshot"
        );

        let late_updates = aggregator.apply_update(stream_counter_update(
            "req-stale-late",
            100,
            20,
            true,
            start + Duration::from_millis(2_100),
        ));
        assert!(
            late_updates.is_empty(),
            "late cumulative finish after stale cleanup must not be replayed from zero"
        );
    }

    #[test]
    fn shared_aggregator_keeps_bounded_request_state_for_many_cumulative_updates() {
        const REQUESTS: usize = 256;
        const EVENTS: usize = 10_000;

        let config = StatsCollectorConfig::default();
        let mut aggregator = SharedStatsAggregator::new(config);
        let start = TokioInstant::now();
        let mut latest = vec![(0u64, 0u64); REQUESTS];

        for index in 0..EVENTS {
            let request_index = index % REQUESTS;
            let step = (index / REQUESTS + 1) as u64;
            let tokens_processed = step * 8;
            let tokens_generated = step;
            latest[request_index] = (tokens_processed, tokens_generated);
            aggregator.apply_update(stream_counter_update(
                &format!("req-{request_index}"),
                tokens_processed,
                tokens_generated,
                false,
                start + Duration::from_millis(index as u64),
            ));
        }

        assert_eq!(aggregator.live_request_count(), REQUESTS);

        for (request_index, (tokens_processed, tokens_generated)) in latest.into_iter().enumerate()
        {
            aggregator.apply_update(stream_counter_update(
                &format!("req-{request_index}"),
                tokens_processed,
                tokens_generated,
                true,
                start + Duration::from_secs(60) + Duration::from_millis(request_index as u64),
            ));
        }

        assert_eq!(aggregator.live_request_count(), 0);
    }

    #[test]
    fn last_mean_input_tps_stays_sticky_without_new_samples() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::new();
        per_model
            .entry("model-a".to_string())
            .or_insert_with(ModelMetricsState::default)
            .last_mean_input_tps = 10.0;
        let in_flight = HashMap::new();

        let stats = snapshot_model_stats(&config, &mut per_model, &in_flight, "model-a");
        assert_eq!(stats.last_mean_input_tps, 10.0);

        let stats = snapshot_model_stats(&config, &mut per_model, &in_flight, "model-a");
        assert_eq!(stats.last_mean_input_tps, 10.0);
    }

    #[test]
    fn mean_input_tps_aggregator_samples_completed_openai_observations() {
        let config = StatsCollectorConfig::default();
        let mut aggregator =
            MeanInputTpsAggregator::new(MeanInputTpsAggregatorConfig::from(&config));
        let mut update = None;

        for request_index in 0..5 {
            let updates = aggregator.record_request_observation(&RequestObservation {
                request_id: format!("req-openai-stream-{request_index}"),
                ..completed_observation(
                    50,
                    1,
                    8,
                    Duration::from_millis(500),
                    Duration::from_secs(1),
                )
            });
            if request_index < 4 {
                assert!(updates.is_empty());
            } else {
                update = updates.into_iter().next();
            }
        }

        let update = update.expect("fifth completed request should publish mean input TPS");
        assert_eq!(update.model_id, "model-a");
        assert_eq!(update.last_mean_input_tps, 100.0);
        let distribution = &aggregator.per_model["model-a"].distribution;
        assert_eq!(distribution.count, 5);
        assert_eq!(distribution.mean, 100.0);
    }

    #[test]
    fn mean_input_tps_aggregator_ignores_non_terminal_observations() {
        let config = StatsCollectorConfig::default();
        let mut aggregator =
            MeanInputTpsAggregator::new(MeanInputTpsAggregatorConfig::from(&config));

        for state in [
            RequestObservationState::InputProcessing,
            RequestObservationState::OutputGeneration,
        ] {
            let updates = aggregator.record_request_observation(&RequestObservation {
                request_id: format!("req-live-{state:?}"),
                state,
                ..completed_observation(
                    50,
                    1,
                    8,
                    Duration::from_millis(500),
                    Duration::from_secs(1),
                )
            });
            assert!(updates.is_empty());
        }

        assert!(aggregator.per_model.is_empty());
    }

    #[tokio::test]
    async fn mean_input_tps_aggregator_publishes_completed_openai_samples() {
        let config = StatsCollectorConfig::default();
        let (observation_tx, observation_rx) = flume::unbounded();
        let (update_tx, update_rx) = flume::unbounded();
        let task = tokio::spawn(run_mean_input_tps_aggregator(
            MeanInputTpsAggregatorConfig::from(&config),
            observation_rx,
            update_tx,
        ));

        for request_index in 0..5 {
            observation_tx
                .send(mean_input_observation(&RequestObservation {
                    request_id: format!("req-async-openai-stream-{request_index}"),
                    ..completed_observation(
                        50,
                        1,
                        8,
                        Duration::from_millis(500),
                        Duration::from_secs(1),
                    )
                }))
                .expect("aggregator should receive completed observation");
        }

        let update = receive_mean_input_update(&update_rx).await;
        assert_eq!(update.model_id, "model-a");
        assert_eq!(update.last_mean_input_tps, 100.0);

        task.abort();
        let _ = task.await;
    }
    #[test]
    fn terminal_only_samples_use_request_duration_instead_of_tick_window() {
        let config = StatsCollectorConfig::default();
        let mut aggregator =
            MeanInputTpsAggregator::new(MeanInputTpsAggregatorConfig::from(&config));
        let mut update = None;

        for request_index in 0..5 {
            let updates = aggregator.record_request_observation(&RequestObservation {
                request_id: format!("req-final-only-{request_index}"),
                ..completed_observation(100, 1, 1, Duration::from_secs(2), Duration::from_secs(2))
            });
            if request_index < 4 {
                assert!(updates.is_empty());
            } else {
                update = updates.into_iter().next();
            }
        }
        let update = update.expect("fifth direct sample should publish the sticky mean");
        assert_eq!(update.model_id, "model-a");
        assert_eq!(update.last_mean_input_tps, 50.0);
        let distribution = &aggregator.per_model["model-a"].distribution;
        assert_eq!(distribution.count, 5);
        assert_eq!(distribution.mean, 50.0);
    }

    #[test]
    fn terminal_only_samples_do_not_sum_same_tick_request_rates() {
        let config = StatsCollectorConfig::default();
        let mut aggregator =
            MeanInputTpsAggregator::new(MeanInputTpsAggregatorConfig::from(&config));
        let mut update = None;

        for request_index in 0..5 {
            let updates = aggregator.record_request_observation(&RequestObservation {
                request_id: format!("req-final-only-sequential-{request_index}"),
                ..completed_observation(
                    100,
                    1,
                    1,
                    Duration::from_millis(10),
                    Duration::from_millis(10),
                )
            });
            if request_index < 4 {
                assert!(updates.is_empty());
            } else {
                update = updates.into_iter().next();
            }
        }
        let update = update.expect("fifth direct sample should publish the sticky mean");
        assert_eq!(update.last_mean_input_tps, 10_000.0);
        let distribution = &aggregator.per_model["model-a"].distribution;
        assert_eq!(distribution.count, 5);
        assert_eq!(distribution.mean, 10_000.0);
    }

    #[test]
    fn completed_request_stats_keep_exact_output_rate_formula() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();
        let observation =
            completed_observation(120, 6, 30, Duration::from_secs(3), Duration::from_secs(9));

        let stats = record_observation(&config, &mut per_model, &mut in_flight, &observation)
            .into_iter()
            .find(|(model_id, _)| model_id == "model-a")
            .unwrap()
            .1;

        assert_eq!(stats.last_mean_input_tps, 0.0);
        assert_eq!(stats.output_tps, 5.0);
        assert_eq!(stats.max_output_tps, 5.0);
    }

    #[test]
    fn ignores_observations_below_duration_floor() {
        let config = StatsCollectorConfig {
            duration_floor: Duration::from_millis(50),
            ..Default::default()
        };
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();
        let observation = completed_observation(
            20,
            4,
            8,
            Duration::from_millis(10),
            Duration::from_millis(20),
        );

        let stats = record_observation(&config, &mut per_model, &mut in_flight, &observation);
        assert_eq!(stats.len(), 1);
        assert_eq!(stats[0].1.last_mean_input_tps, 0.0);
        assert_eq!(stats[0].1.output_tps, 0.0);
    }

    #[test]
    fn terminal_usage_chunks_use_first_output_for_output_tps() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();
        let observation = RequestObservation {
            time_to_first_token: Some(Duration::from_millis(5_995)),
            ..completed_observation(20, 4, 8, Duration::from_secs(2), Duration::from_secs(6))
        };

        let stats = record_observation(&config, &mut per_model, &mut in_flight, &observation);

        assert_eq!(stats.len(), 1);
        assert_eq!(stats[0].1.output_tps, 2.0);
        assert_eq!(stats[0].1.max_output_tps, 2.0);
    }

    #[test]
    fn embeddings_stats_update_last_mean_input_tps_without_claiming_output_tps() {
        let config = StatsCollectorConfig::default();
        let mut aggregator =
            MeanInputTpsAggregator::new(MeanInputTpsAggregatorConfig::from(&config));
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();
        let observation =
            completed_embeddings_observation(20, 4, Duration::from_secs(2), Duration::from_secs(4));

        for _ in 0..4 {
            assert!(
                aggregator
                    .record_request_observation(&observation)
                    .is_empty()
            );
            let stats = record_observation(&config, &mut per_model, &mut in_flight, &observation);
            assert_eq!(stats[0].1.last_mean_input_tps, 0.0);
        }
        let updates = aggregator.record_request_observation(&observation);
        let stats = record_observation(&config, &mut per_model, &mut in_flight, &observation);

        assert_eq!(updates[0].last_mean_input_tps, 10.0);
        assert_eq!(stats.len(), 1);
        assert_eq!(stats[0].1.last_mean_input_tps, 0.0);
        assert_eq!(stats[0].1.output_tps, 0.0);
        assert_eq!(stats[0].1.max_output_tps, 0.0);
        assert_eq!(stats[0].1.embedding_item_tps, 2.0);
        assert_eq!(stats[0].1.max_embedding_item_tps, 2.0);
        assert!(stats[0].1.stats_capabilities.is_empty());
        assert!(stats[0].1.stats_sources.is_empty());

        let live_chat = RequestObservation {
            request_id: "req-live-chat".to_string(),
            state: RequestObservationState::OutputGeneration,
            output_tokens: 20,
            time_to_first_output: Some(Duration::from_secs(1)),
            time_to_first_token: Some(Duration::from_secs(1)),
            total_duration: Duration::from_secs(3),
            ..completed_observation(10, 1, 20, Duration::from_secs(1), Duration::from_secs(3))
        };
        let stats = record_observation(&config, &mut per_model, &mut in_flight, &live_chat);

        assert_eq!(stats[0].1.output_tps, 10.0);
    }

    #[test]
    fn fast_embeddings_input_samples_clamp_to_duration_floor() {
        let config = StatsCollectorConfig::default();
        let mut aggregator =
            MeanInputTpsAggregator::new(MeanInputTpsAggregatorConfig::from(&config));
        let observation = completed_embeddings_observation(
            20,
            4,
            Duration::from_millis(1),
            Duration::from_millis(4),
        );
        let mut update = None;

        for sample_index in 0..5 {
            let updates = aggregator.record_request_observation(&RequestObservation {
                request_id: format!("req-fast-embedding-{sample_index}"),
                ..observation.clone()
            });
            if sample_index < 4 {
                assert!(updates.is_empty());
            } else {
                update = updates.into_iter().next();
            }
        }

        let update = update.expect("fifth embeddings input sample should publish mean input TPS");
        assert_eq!(update.last_mean_input_tps, 2000.0);
        let distribution = &aggregator.per_model["model-a"].distribution;
        assert_eq!(distribution.count, 5);
        assert_eq!(distribution.mean, 2000.0);
    }

    #[test]
    fn embeddings_item_tps_clamps_fast_response_relay_duration() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();
        let observation = completed_embeddings_observation(
            20,
            2,
            Duration::from_millis(2),
            Duration::from_millis(5),
        );

        let stats = record_observation(&config, &mut per_model, &mut in_flight, &observation);

        assert_eq!(stats.len(), 1);
        assert_eq!(stats[0].1.output_tps, 0.0);
        assert_eq!(stats[0].1.max_output_tps, 0.0);
        assert_eq!(stats[0].1.embedding_item_tps, 200.0);
        assert_eq!(stats[0].1.max_embedding_item_tps, 200.0);
    }

    #[test]
    fn embeddings_stats_do_not_replace_chat_output_tps() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();

        let chat = completed_observation(20, 1, 10, Duration::from_secs(1), Duration::from_secs(3));
        let stats = record_observation(&config, &mut per_model, &mut in_flight, &chat);
        assert_eq!(stats[0].1.output_tps, 5.0);

        let embeddings =
            completed_embeddings_observation(20, 2, Duration::from_secs(1), Duration::from_secs(2));
        let stats = record_observation(&config, &mut per_model, &mut in_flight, &embeddings);

        assert_eq!(stats[0].1.output_tps, 5.0);
        assert_eq!(stats[0].1.max_output_tps, 5.0);
        assert_eq!(stats[0].1.embedding_item_tps, 2.0);
        assert_eq!(stats[0].1.max_embedding_item_tps, 2.0);
        assert!(stats[0].1.stats_capabilities.is_empty());
        assert!(stats[0].1.stats_sources.is_empty());
    }

    #[test]
    fn embeddings_observations_do_not_add_output_throughput_labels() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();

        let chat = completed_observation(20, 1, 10, Duration::from_secs(1), Duration::from_secs(3));
        let stats = record_observation(&config, &mut per_model, &mut in_flight, &chat);
        assert_eq!(stats[0].1.output_tps, 5.0);

        let failed_embeddings = RequestObservation {
            state: RequestObservationState::Failed,
            ..completed_embeddings_observation(
                20,
                2,
                Duration::from_secs(1),
                Duration::from_secs(2),
            )
        };
        let stats = record_observation(&config, &mut per_model, &mut in_flight, &failed_embeddings);

        assert_eq!(
            stats[0].1.output_tps, 5.0,
            "failed embeddings requests must not replace the last completed output sample"
        );
        assert_eq!(stats[0].1.embedding_item_tps, 0.0);
        assert_eq!(stats[0].1.max_embedding_item_tps, 0.0);
        assert!(stats[0].1.stats_capabilities.is_empty());
        assert!(stats[0].1.stats_sources.is_empty());

        let live_embeddings = RequestObservation {
            state: RequestObservationState::UpstreamConnecting,
            total_duration: Duration::ZERO,
            ..completed_embeddings_observation(
                20,
                2,
                Duration::from_secs(1),
                Duration::from_secs(2),
            )
        };
        let stats = record_observation(&config, &mut per_model, &mut in_flight, &live_embeddings);

        assert_eq!(stats[0].1.output_tps, 5.0);
        assert_eq!(stats[0].1.embedding_item_tps, 0.0);
        assert!(stats[0].1.stats_capabilities.is_empty());
        assert!(stats[0].1.stats_sources.is_empty());
    }

    #[test]
    fn ignores_non_complete_observations() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();
        let observation = RequestObservation {
            state: RequestObservationState::Failed,
            ..completed_observation(20, 4, 8, Duration::from_secs(2), Duration::from_secs(6))
        };

        let stats = record_observation(&config, &mut per_model, &mut in_flight, &observation);
        assert_eq!(stats.len(), 1);
        assert_eq!(stats[0].1.last_mean_input_tps, 0.0);
        assert_eq!(stats[0].1.output_tps, 0.0);
    }

    #[test]
    fn publishes_live_queue_and_active_stats() {
        let config = StatsCollectorConfig::default();
        config
            .queue_tracker
            .update_model_throughput("model-a", 100.0);
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();

        let queued = RequestObservation {
            endpoint: RequestObservationEndpoint::ChatCompletions,
            request_id: "req-live".to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
            priority: 2,
            input_tokens: 24,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: Some(200),
            output_messages: 0,
            output_tokens: 0,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
            state: RequestObservationState::InputProcessing,
            time_to_response_headers: Some(Duration::from_millis(5)),
            time_to_first_output: None,
            time_to_first_token: None,
            total_duration: Duration::from_millis(5),
        };
        let queued_stats = record_observation(&config, &mut per_model, &mut in_flight, &queued);
        assert_eq!(queued_stats[0].1.queue_size, 1);
        assert_eq!(queued_stats[0].1.queued_input_size, 24);
        assert_eq!(
            queued_stats[0].1.queue_time_estimate_ms_by_priority,
            Some(HashMap::from([(0, 240)]))
        );
        assert_eq!(queued_stats[0].1.num_running_queries, 1);
        assert_eq!(queued_stats[0].1.total_query_input_size, 24);
        assert_eq!(queued_stats[0].1.input_processing_queries, 1);
        assert_eq!(queued_stats[0].1.output_generation_queries, 0);
        assert_eq!(queued_stats[0].1.last_mean_input_tps, 0.0);

        let generating = RequestObservation {
            output_messages: 2,
            output_tokens: 8,
            state: RequestObservationState::OutputGeneration,
            time_to_first_output: Some(Duration::from_secs(2)),
            time_to_first_token: Some(Duration::from_secs(2)),
            total_duration: Duration::from_secs(3),
            ..queued
        };
        let active_stats = record_observation(&config, &mut per_model, &mut in_flight, &generating);
        assert_eq!(active_stats[0].1.queue_size, 0);
        assert_eq!(active_stats[0].1.queued_input_size, 0);
        assert_eq!(active_stats[0].1.num_running_queries, 1);
        assert_eq!(active_stats[0].1.total_query_input_size, 24);
        assert_eq!(active_stats[0].1.input_processing_queries, 0);
        assert_eq!(active_stats[0].1.output_generation_queries, 1);
        assert_eq!(active_stats[0].1.last_mean_input_tps, 0.0);
        assert_eq!(active_stats[0].1.output_tps, 8.0);
    }

    #[test]
    fn live_stats_math_is_exact_for_simultaneous_queued_and_generating_requests() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();

        let queued = RequestObservation {
            endpoint: RequestObservationEndpoint::ChatCompletions,
            request_id: "req-queued".to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens: 30,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: Some(200),
            output_messages: 0,
            output_tokens: 0,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
            state: RequestObservationState::InputProcessing,
            time_to_response_headers: Some(Duration::from_millis(5)),
            time_to_first_output: None,
            time_to_first_token: None,
            total_duration: Duration::from_millis(5),
        };
        let generating = RequestObservation {
            endpoint: RequestObservationEndpoint::ChatCompletions,
            request_id: "req-generating".to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens: 20,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: Some(200),
            output_messages: 3,
            output_tokens: 6,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
            state: RequestObservationState::OutputGeneration,
            time_to_response_headers: Some(Duration::from_millis(5)),
            time_to_first_output: Some(Duration::from_secs(2)),
            time_to_first_token: Some(Duration::from_secs(2)),
            total_duration: Duration::from_secs(5),
        };

        record_observation(&config, &mut per_model, &mut in_flight, &queued);
        let stats = record_observation(&config, &mut per_model, &mut in_flight, &generating)
            .into_iter()
            .find(|(model_id, _)| model_id == "model-a")
            .unwrap()
            .1;

        assert_eq!(stats.queue_size, 1);
        assert_eq!(stats.queued_input_size, 30);
        assert_eq!(stats.num_running_queries, 2);
        assert_eq!(stats.total_query_input_size, 50);
        assert_eq!(stats.last_mean_input_tps, 0.0);
        assert_eq!(stats.output_tps, 2.0);
    }

    #[test]
    fn live_input_processing_keeps_full_requested_input_without_retired_progress() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();

        let observation = RequestObservation {
            endpoint: RequestObservationEndpoint::ChatCompletions,
            request_id: "req-input-processing".to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens: 100,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: Some(200),
            output_messages: 0,
            output_tokens: 0,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
            state: RequestObservationState::InputProcessing,
            time_to_response_headers: Some(Duration::from_secs(2)),
            time_to_first_output: None,
            time_to_first_token: None,
            total_duration: Duration::from_secs(30),
        };

        let stats = record_observation(&config, &mut per_model, &mut in_flight, &observation);

        assert_eq!(stats[0].1.last_mean_input_tps, 0.0);
        assert_eq!(stats[0].1.queued_input_size, 100);
        assert!(stats[0].1.stats_capabilities.is_empty());
        assert!(stats[0].1.stats_sources.is_empty());
        assert!(stats[0].1.stats_observed_at_unix_ms > 0);
    }

    #[test]
    fn chunk_usage_observations_claim_only_chunk_usage_stats() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();

        let observation = RequestObservation {
            output_tokens_explicit: true,
            output_tokens_from_chunk_usage: true,
            ..completed_observation(
                12,
                1,
                7,
                Duration::from_millis(100),
                Duration::from_millis(500),
            )
        };

        let stats = record_observation(&config, &mut per_model, &mut in_flight, &observation);

        assert_eq!(
            stats[0].1.stats_capabilities,
            vec!["request.output.chunk_usage".to_string()]
        );
        assert_eq!(stats[0].1.stats_sources, vec!["chunk_usage".to_string()]);
    }
    #[test]
    fn snapshot_includes_polled_kv_cache_stats() {
        let config = StatsCollectorConfig::default();
        let mut per_model = HashMap::<String, ModelMetricsState>::new();
        per_model.entry("model-a".to_string()).or_default().kv_cache = KvCacheStatsSnapshot {
            model: "model-a".to_string(),
            kv_cache_capacity_tokens: 1_000,
            kv_cache_used_tokens: 400,
            kv_cache_free_tokens: 600,
        };

        let stats = snapshot_model_stats(&config, &mut per_model, &HashMap::new(), "model-a");

        assert_eq!(stats.kv_cache_capacity_tokens, 1_000);
        assert_eq!(stats.kv_cache_used_tokens, 400);
        assert_eq!(stats.kv_cache_free_tokens, 600);
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
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("listener should bind");
        let addr = listener.local_addr().expect("listener should have address");
        let server = tokio::spawn(async move {
            let app = Router::new().route("/kv-cache", get(kv_cache_stats));
            axum::serve(listener, app)
                .await
                .expect("KV-cache test server should run");
        });

        let config = StatsCollectorConfig {
            kv_cache_stats_url: Some(format!("http://{addr}/kv-cache")),
            kv_cache_poll_interval: Duration::from_millis(10),
            kv_cache_request_timeout: Duration::from_secs(1),
            metrics: Some(metrics.clone()),
            ..Default::default()
        };
        let (_observation_tx, observation_rx) = request_observation_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(4);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = tokio::spawn(run_stats_collector(
            config,
            observation_rx,
            None,
            model_stats_tx,
            stop_rx,
        ));

        let (model_id, stats) =
            tokio::time::timeout(Duration::from_secs(2), model_stats_rx.recv_async())
                .await
                .expect("KV-cache stats should be published")
                .expect("collector should publish stats");
        assert_eq!(model_id, "model-a");
        assert_eq!(stats.kv_cache_capacity_tokens, 1000);
        assert_eq!(stats.kv_cache_used_tokens, 400);
        assert_eq!(stats.kv_cache_free_tokens, 600);

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(r#"pylon_model_kv_cache_capacity_tokens{model="model-a"} 1000"#));
        assert!(body.contains(r#"pylon_model_kv_cache_used_tokens{model="model-a"} 400"#));
        assert!(body.contains(r#"pylon_model_kv_cache_free_tokens{model="model-a"} 600"#));

        stop_tx.send(true).expect("collector should receive stop");
        tokio::time::timeout(Duration::from_secs(2), collector)
            .await
            .expect("collector should stop")
            .expect("collector task should join");
        server.abort();
    }

    #[tokio::test]
    async fn stats_collector_publishes_mean_input_tps_from_completed_observations() {
        let config = StatsCollectorConfig {
            observation_channel_capacity: 16,
            ..Default::default()
        };
        let (observation_tx, observation_rx) = request_observation_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(16);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = tokio::spawn(run_stats_collector(
            config,
            observation_rx,
            None,
            model_stats_tx,
            stop_rx,
        ));

        for request_index in 0..5 {
            observation_tx
                .send_async(RequestObservation {
                    request_id: format!("req-stats-openai-{request_index}"),
                    output_messages: 1,
                    output_tokens: 2,
                    time_to_first_output: Some(Duration::from_millis(500)),
                    time_to_first_token: Some(Duration::from_millis(600)),
                    total_duration: Duration::from_secs(1),
                    ..completed_observation(
                        50,
                        1,
                        2,
                        Duration::from_millis(500),
                        Duration::from_secs(1),
                    )
                })
                .await
                .expect("collector should receive completed observations");
        }
        tokio::task::yield_now().await;

        let stats = receive_model_stats_with_last_mean_input_tps(&model_stats_rx, 100.0).await;
        assert_eq!(stats.last_mean_input_tps, 100.0);
        assert_eq!(stats.output_tps, 5.0);

        stop_tx.send(true).expect("collector should receive stop");
        collector.await.expect("collector task should join");
    }
    #[tokio::test]
    async fn stats_collector_seeds_fixed_input_tps_for_queue_admission() {
        let queue_tracker = QueueAdmissionTracker::default();
        let config = StatsCollectorConfig {
            configured_model_ids: vec!["model-a".to_string()],
            fixed_last_mean_input_tps: Some(2_200.0),
            queue_tracker: queue_tracker.clone(),
            ..Default::default()
        };
        let (_observation_tx, observation_rx) = request_observation_channel(&config);
        let (model_stats_tx, model_stats_rx) = flume::bounded(4);
        let (stop_tx, stop_rx) = watch::channel(false);
        let collector = tokio::spawn(run_stats_collector(
            config,
            observation_rx,
            None,
            model_stats_tx,
            stop_rx,
        ));

        let (model_id, stats) =
            tokio::time::timeout(Duration::from_secs(1), model_stats_rx.recv_async())
                .await
                .expect("fixed TPS stats should be published")
                .expect("collector should stay connected");
        assert_eq!(model_id, "model-a");
        assert_eq!(stats.last_mean_input_tps, 2_200.0);

        let _queued =
            queue_tracker.track_request(&crate::request_observer::RequiredTunnelHeaders {
                request_id: "req-queued".to_string(),
                routing_key: None,
                model_id: "model-a".to_string(),
                priority: 0,
                input_tokens: 32,
                accepted_at: std::time::Instant::now(),
            });
        queue_tracker.record_observation(&active_chat_observation(
            "req-queued",
            RequestObservationState::Queued,
        ));
        assert_eq!(
            queue_tracker
                .snapshot_model("model-a")
                .queue_time_estimate_ms_by_priority,
            Some(HashMap::from([(0, 15)]))
        );

        stop_tx.send(true).expect("collector should receive stop");
        collector.await.expect("collector task should join");
    }

    #[test]
    fn records_metrics_when_configured() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let config = StatsCollectorConfig {
            metrics: Some(metrics.clone()),
            ..Default::default()
        };
        let mut per_model = HashMap::new();
        let mut in_flight = HashMap::new();
        let observation =
            completed_observation(20, 2, 10, Duration::from_secs(2), Duration::from_secs(4));

        for _ in 0..5 {
            let updated_stats =
                record_observation(&config, &mut per_model, &mut in_flight, &observation);
            observe_request_metric(&config, &observation);
            for (model_id, stats) in updated_stats {
                observe_model_metric(&config, &model_id, &stats);
            }
        }
        let mut mean_input_stats =
            snapshot_model_stats(&config, &mut per_model, &in_flight, "model-a");
        mean_input_stats.last_mean_input_tps = 10.0;
        observe_model_metric(&config, "model-a", &mean_input_stats);

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(
            r#"pylon_requests_total{model="model-a",routing_key="rk-1",status="complete"} 5"#
        ));
        assert!(body.contains(r#"pylon_model_last_mean_input_tps{model="model-a"} 10"#));
        assert!(body.contains(r#"pylon_model_output_tps{model="model-a"} 5"#));
    }

    #[test]
    fn rejects_kv_cache_stats_for_unconfigured_models() {
        let config = StatsCollectorConfig {
            configured_model_ids: vec!["model-a".to_string()],
            ..Default::default()
        };
        let kv_cache = KvCacheStatsSnapshot {
            model: "model-b".to_string(),
            kv_cache_capacity_tokens: 1_000,
            kv_cache_used_tokens: 400,
            kv_cache_free_tokens: 600,
        };

        assert!(!kv_cache_stats_model_allowed(&config, &kv_cache));
    }
}

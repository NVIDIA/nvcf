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

use std::collections::{HashMap, VecDeque};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use serde::Deserialize;
use tokio::time::Instant as TokioInstant;

use crate::CurrentModelStats;
use crate::request_observer::RequestObservationEndpoint;

use super::collector::{
    FinalizeRequestUpdate, RequestCounterUpdate, RequestObservationStatsUpdate,
    StatsAggregatorUpdate, StatsCollectorConfig, StatsUpdateSource,
};
use super::token_metrics::TpsDistribution;

#[derive(Debug, Default)]
pub(super) struct ModelMetricsState {
    pub(super) last_mean_input_tps: f64,
    pub(super) chat_output_tps_samples: VecDeque<f64>,
    pub(super) chat_output_tps_sum: f64,
    pub(super) embedding_item_tps_samples: VecDeque<f64>,
    pub(super) embedding_item_tps_sum: f64,
    pub(super) max_chat_output_tps: f64,
    pub(super) max_embedding_item_tps: f64,
    pub(super) kv_cache: KvCacheStatsSnapshot,
    pub(super) stream_input_tps_distribution: TpsDistribution,
    pub(super) chunk_usage_stats_observed: bool,
    pub(super) kv_cache_stats_observed: bool,
    pub(super) engine_stream_stats_observed: bool,
    pub(super) last_stats_event_at: Option<TokioInstant>,
    pub(super) stats_observed_at_unix_ms: u64,
}

#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub(super) struct KvCacheStatsSnapshot {
    pub(super) model: String,
    pub(super) kv_cache_capacity_tokens: u64,
    pub(super) kv_cache_used_tokens: u64,
    pub(super) kv_cache_free_tokens: u64,
}

pub(super) struct InFlightRequestState {
    pub(super) endpoint: RequestObservationEndpoint,
    pub(super) model_id: String,
    pub(super) output_tokens: u64,
    pub(super) time_to_first_output: Option<Duration>,
    pub(super) time_to_first_token: Option<Duration>,
    pub(super) total_duration: Duration,
}

#[derive(Debug)]
struct RequestCounterState {
    model_id: String,
    input: CounterSampleState,
    output: CounterSampleState,
    last_seen_at: TokioInstant,
}

impl RequestCounterState {
    fn from_observed(
        model_id: String,
        tokens_processed: u64,
        tokens_generated: u64,
        observed_at: TokioInstant,
    ) -> Self {
        Self {
            model_id,
            input: CounterSampleState::from_observed(tokens_processed, observed_at),
            output: CounterSampleState::from_observed(tokens_generated, observed_at),
            last_seen_at: observed_at,
        }
    }

    fn from_zero_baseline(model_id: String, observed_at: TokioInstant) -> Self {
        Self {
            model_id,
            input: CounterSampleState::from_observed(0, observed_at),
            output: CounterSampleState::from_observed(0, observed_at),
            last_seen_at: observed_at,
        }
    }
}

#[derive(Debug)]
struct CounterSampleState {
    observed: u64,
    sampled: u64,
    sampled_at: TokioInstant,
}

impl CounterSampleState {
    fn from_observed(observed: u64, observed_at: TokioInstant) -> Self {
        Self {
            observed,
            sampled: observed,
            sampled_at: observed_at,
        }
    }

    fn is_regression(&self, next: u64) -> bool {
        next < self.observed
    }

    fn observe(
        &mut self,
        next: u64,
        observed_at: TokioInstant,
        min_units: u64,
        duration_floor: Duration,
    ) -> Option<CounterSample> {
        let prior_observed = self.observed;
        self.observed = next;

        let units = next.saturating_sub(self.sampled);
        if units == 0 || units < min_units {
            return None;
        }

        let mut duration = observed_at
            .checked_duration_since(self.sampled_at)
            .unwrap_or(Duration::ZERO);
        if duration < duration_floor && self.sampled == 0 && prior_observed == 0 {
            duration = duration_floor;
        }
        if duration < duration_floor {
            return None;
        }

        self.sampled = next;
        self.sampled_at = observed_at;
        Some(CounterSample { units, duration })
    }
}

#[derive(Debug)]
struct CounterSample {
    units: u64,
    duration: Duration,
}

pub(super) struct SharedStatsAggregator {
    config: StatsCollectorConfig,
    per_model: HashMap<String, ModelMetricsState>,
    per_request: HashMap<String, RequestCounterState>,
    finalized_requests: HashMap<String, TokioInstant>,
    unix_ms_anchor: u64,
    instant_anchor: TokioInstant,
}

impl SharedStatsAggregator {
    pub(super) fn new(config: StatsCollectorConfig) -> Self {
        Self {
            config,
            per_model: HashMap::new(),
            per_request: HashMap::new(),
            finalized_requests: HashMap::new(),
            unix_ms_anchor: current_unix_millis(),
            instant_anchor: TokioInstant::now(),
        }
    }

    pub(super) fn apply_update(
        &mut self,
        update: StatsAggregatorUpdate,
    ) -> Vec<(String, CurrentModelStats)> {
        let mut updated_models = Vec::new();
        self.apply_update_into(update, &mut updated_models);
        updated_models
    }

    pub(super) fn apply_update_into(
        &mut self,
        update: StatsAggregatorUpdate,
        updated_models: &mut Vec<(String, CurrentModelStats)>,
    ) {
        match update {
            StatsAggregatorUpdate::RequestCounters(update) => {
                self.apply_request_counters_into(update, updated_models);
            }
            StatsAggregatorUpdate::RequestObservation(update) => {
                updated_models.extend(self.apply_request_observation(update));
            }
            StatsAggregatorUpdate::FinalizeRequest(update) => {
                updated_models.extend(self.finalize_request(update));
            }
            StatsAggregatorUpdate::EnableOpenAiFallback => {}
        }
    }

    pub(super) fn live_request_count(&self) -> usize {
        self.per_request.len()
    }

    pub(super) fn model_state_count(&self) -> usize {
        self.per_model.len()
    }

    pub(super) fn unix_millis_at(&self, observed_at: TokioInstant) -> u64 {
        if let Some(elapsed) = observed_at.checked_duration_since(self.instant_anchor) {
            return self
                .unix_ms_anchor
                .saturating_add(duration_millis_u64(elapsed));
        }
        let elapsed = self
            .instant_anchor
            .checked_duration_since(observed_at)
            .unwrap_or(Duration::ZERO);
        self.unix_ms_anchor
            .saturating_sub(duration_millis_u64(elapsed))
    }

    pub(super) fn sweep_stale(&mut self, now: TokioInstant) -> Vec<(String, CurrentModelStats)> {
        let mut dirty_models = Vec::new();
        let request_ttl = self.config.engine_stats_request_ttl;
        if !request_ttl.is_zero() {
            let mut stale_requests = Vec::new();
            for (request_id, state) in &self.per_request {
                if elapsed_since(now, state.last_seen_at) >= request_ttl {
                    stale_requests.push((request_id.clone(), state.model_id.clone()));
                }
            }
            for (request_id, model_id) in stale_requests {
                if self.per_request.remove(&request_id).is_some() {
                    self.finalized_requests.insert(request_id.clone(), now);
                    tracing::warn!(
                        request_id,
                        model_id,
                        ttl_ms = request_ttl.as_millis(),
                        "removing stale engine stats request entry"
                    );
                    if let Some(metrics) = &self.config.metrics {
                        metrics
                            .observe_engine_stats_stale_cleanup("request", "engine_stats_stream");
                    }
                    push_dirty_model(&mut dirty_models, model_id);
                }
            }

            self.finalized_requests
                .retain(|_, finalized_at| elapsed_since(now, *finalized_at) < request_ttl);
        }

        let model_ttl = self.config.engine_stats_model_ttl;
        if !model_ttl.is_zero() {
            for (model_id, state) in &mut self.per_model {
                if state
                    .last_stats_event_at
                    .is_some_and(|observed_at| elapsed_since(now, observed_at) >= model_ttl)
                    && state.clear_live_output_tps()
                {
                    state.stats_observed_at_unix_ms = current_unix_millis();
                    tracing::warn!(
                        model_id,
                        ttl_ms = model_ttl.as_millis(),
                        "clearing stale engine stats output TPS"
                    );
                    if let Some(metrics) = &self.config.metrics {
                        metrics.observe_engine_stats_stale_cleanup("stats", "engine_stats_stream");
                    }
                    push_dirty_model(&mut dirty_models, model_id.clone());
                }
            }
        }

        if let Some(metrics) = &self.config.metrics {
            metrics
                .observe_engine_stats_model_states("engine_stats_stream", self.model_state_count());
            for _ in &dirty_models {
                metrics.observe_engine_stats_dirty_snapshot("engine_stats_stream", "stale");
            }
        }

        dirty_models
            .into_iter()
            .map(|model_id| {
                let stats = self.snapshot(&model_id);
                (model_id, stats)
            })
            .collect()
    }

    pub(super) fn apply_request_counters_into(
        &mut self,
        update: RequestCounterUpdate,
        updated_models: &mut Vec<(String, CurrentModelStats)>,
    ) {
        let RequestCounterUpdate {
            source,
            request_id,
            model_id,
            tokens_processed,
            tokens_generated,
            finished,
            observed_at,
        } = update;

        if self.finalized_requests.contains_key(&request_id) {
            tracing::warn!(
                request_id,
                source = ?source,
                "ignoring stats event after request finalization"
            );
            if let Some(metrics) = &self.config.metrics {
                metrics.observe_engine_stats_invalid_event("post_finalize");
            }
            return;
        }

        if !self.configured_model_allowed(&model_id) {
            tracing::warn!(
                model_id,
                configured_models = ?self.config.configured_model_ids,
                "dropping stats event for unconfigured model"
            );
            if let Some(metrics) = &self.config.metrics {
                metrics.observe_engine_stats_invalid_event("unconfigured_model");
            }
            return;
        }

        if self
            .per_request
            .get(&request_id)
            .is_some_and(|state| state.model_id != model_id)
        {
            let prior_model = self
                .per_request
                .remove(&request_id)
                .map(|state| state.model_id)
                .unwrap_or_default();
            tracing::warn!(
                request_id,
                prior_model,
                model_id,
                "resetting request stats after model changed"
            );
        }

        let duration_floor = self.config.duration_floor;
        let min_input_tokens = self.config.min_input_tokens;
        let min_output_tokens = self.config.min_output_tokens;
        let mut new_request_state = None;
        let mut remove_finished_request = false;
        let mut input_sample = None;
        let mut output_sample = None;

        if let Some(state) = self.per_request.get_mut(&request_id) {
            if tokens_processed.is_some_and(|next| state.input.is_regression(next))
                || tokens_generated.is_some_and(|next| state.output.is_regression(next))
            {
                tracing::warn!(
                    request_id,
                    model_id,
                    prior_tokens_processed = state.input.observed,
                    tokens_processed = tokens_processed.unwrap_or(state.input.observed),
                    prior_tokens_generated = state.output.observed,
                    tokens_generated = tokens_generated.unwrap_or(state.output.observed),
                    source = ?source,
                    "ignoring regressing request stats counters"
                );
                if let Some(metrics) = &self.config.metrics {
                    metrics.observe_engine_stats_invalid_event("regressing_counters");
                }
                return;
            }

            if let Some(next_tokens_processed) = tokens_processed {
                input_sample = state.input.observe(
                    next_tokens_processed,
                    observed_at,
                    min_input_tokens,
                    duration_floor,
                );
            }
            if let Some(next_tokens_generated) = tokens_generated {
                output_sample = state.output.observe(
                    next_tokens_generated,
                    observed_at,
                    min_output_tokens,
                    duration_floor,
                );
            }
            state.last_seen_at = observed_at;
            remove_finished_request = finished;
        } else {
            let next_tokens_processed = tokens_processed.unwrap_or(0);
            let next_tokens_generated = tokens_generated.unwrap_or(0);
            let mut state = if source == StatsUpdateSource::EngineStatsStream {
                RequestCounterState::from_zero_baseline(model_id.clone(), observed_at)
            } else {
                RequestCounterState::from_observed(
                    model_id.clone(),
                    next_tokens_processed,
                    next_tokens_generated,
                    observed_at,
                )
            };

            if source == StatsUpdateSource::EngineStatsStream {
                if let Some(next_tokens_processed) = tokens_processed {
                    input_sample = state.input.observe(
                        next_tokens_processed,
                        observed_at,
                        min_input_tokens,
                        duration_floor,
                    );
                }
                if let Some(next_tokens_generated) = tokens_generated {
                    output_sample = state.output.observe(
                        next_tokens_generated,
                        observed_at,
                        min_output_tokens,
                        duration_floor,
                    );
                }
            }

            if !finished {
                new_request_state = Some(state);
            }
        }
        let stats_observed_at_unix_ms = self.unix_millis_at(observed_at);

        if finished {
            if remove_finished_request {
                self.per_request.remove(&request_id);
            }
            self.finalized_requests.insert(request_id, observed_at);
        } else if let Some(state) = new_request_state {
            self.per_request.insert(request_id, state);
        }

        let smoothing_window_size = self.config.smoothing_window_size;
        let (dirty, mut stats) = {
            let mut dirty = false;
            let model_state = self.per_model.entry(model_id.clone()).or_default();
            if source == StatsUpdateSource::EngineStatsStream
                && !model_state.engine_stream_stats_observed
            {
                model_state.engine_stream_stats_observed = true;
                dirty = true;
            }
            model_state.last_stats_event_at = Some(observed_at);
            model_state.stats_observed_at_unix_ms = stats_observed_at_unix_ms;

            if let Some(sample) = input_sample
                && let Some(input_tps) =
                    tps_for_units(sample.units, sample.duration, duration_floor)
            {
                model_state.stream_input_tps_distribution.update(input_tps);
                if model_state
                    .stream_input_tps_distribution
                    .has_sufficient_data()
                    && valid_last_mean_input_tps(model_state.stream_input_tps_distribution.mean)
                {
                    let last_mean_input_tps = effective_last_mean_input_tps(
                        &self.config,
                        model_state.stream_input_tps_distribution.mean,
                    );
                    if model_state.last_mean_input_tps != last_mean_input_tps {
                        model_state.last_mean_input_tps = last_mean_input_tps;
                        self.config
                            .queue_tracker
                            .update_model_throughput(&model_id, last_mean_input_tps);
                        dirty = true;
                    }
                }
            }
            if let Some(sample) = output_sample
                && let Some(output_tps) =
                    tps_for_units(sample.units, sample.duration, duration_floor)
            {
                if output_tps > model_state.max_chat_output_tps {
                    model_state.max_chat_output_tps = output_tps;
                }
                dirty = true;
                push_sample(
                    &mut model_state.chat_output_tps_samples,
                    &mut model_state.chat_output_tps_sum,
                    output_tps,
                    smoothing_window_size,
                );
            }

            (
                dirty,
                model_state.current_stats(ModelStatsSnapshotInputs::default()),
            )
        };
        apply_fixed_last_mean_input_tps(&self.config, &mut stats);
        if dirty {
            updated_models.push((model_id, stats));
        }
    }

    pub(super) fn apply_request_observation(
        &mut self,
        update: RequestObservationStatsUpdate,
    ) -> Vec<(String, CurrentModelStats)> {
        if !self.configured_model_allowed(&update.model_id) {
            return Vec::new();
        }
        let min_input_tokens = self.config.min_input_tokens;
        let duration_floor = self.config.duration_floor;
        let smoothing_window_size = self.config.smoothing_window_size;
        let model_state = self.per_model.entry(update.model_id.clone()).or_default();
        model_state.stats_observed_at_unix_ms = current_unix_millis();
        let mut dirty = false;

        if let (Some(input_tokens), Some(duration)) = (update.input_tokens, update.input_duration)
            && input_tokens >= min_input_tokens
        {
            let duration = if update.clamp_input_duration_to_floor {
                duration.max(duration_floor)
            } else {
                duration
            };
            if let Some(input_tps) = tps_for_units(input_tokens, duration, duration_floor) {
                model_state.stream_input_tps_distribution.update(input_tps);
                if model_state
                    .stream_input_tps_distribution
                    .has_sufficient_data()
                    && valid_last_mean_input_tps(model_state.stream_input_tps_distribution.mean)
                {
                    let last_mean_input_tps = effective_last_mean_input_tps(
                        &self.config,
                        model_state.stream_input_tps_distribution.mean,
                    );
                    model_state.last_mean_input_tps = last_mean_input_tps;
                    self.config
                        .queue_tracker
                        .update_model_throughput(&update.model_id, last_mean_input_tps);
                }
                dirty = true;
            }
        }

        if let (Some(embedding_items), Some(duration)) =
            (update.embedding_items, update.embedding_duration)
        {
            let duration = duration.max(duration_floor);
            if let Some(embedding_item_tps) =
                tps_for_units(embedding_items, duration, duration_floor)
            {
                if embedding_item_tps > model_state.max_embedding_item_tps {
                    model_state.max_embedding_item_tps = embedding_item_tps;
                }
                push_sample(
                    &mut model_state.embedding_item_tps_samples,
                    &mut model_state.embedding_item_tps_sum,
                    embedding_item_tps,
                    smoothing_window_size,
                );
                dirty = true;
            }
        }

        dirty
            .then(|| (update.model_id.clone(), self.snapshot(&update.model_id)))
            .into_iter()
            .collect()
    }

    pub(super) fn finalize_request(
        &mut self,
        update: FinalizeRequestUpdate,
    ) -> Vec<(String, CurrentModelStats)> {
        self.finalized_requests
            .insert(update.request_id.clone(), update.observed_at);
        let Some(state) = self.per_request.remove(&update.request_id) else {
            return Vec::new();
        };
        let stats_observed_at_unix_ms = self.unix_millis_at(update.observed_at);
        if let Some(model_state) = self.per_model.get_mut(&state.model_id) {
            model_state.stats_observed_at_unix_ms = stats_observed_at_unix_ms;
        }
        tracing::debug!(
            request_id = update.request_id,
            source = ?update.source,
            "finalized request stats"
        );
        vec![(state.model_id.clone(), self.snapshot(&state.model_id))]
    }

    pub(super) fn snapshot(&self, model_id: &str) -> CurrentModelStats {
        let mut stats = self
            .per_model
            .get(model_id)
            .map(|state| {
                state.current_stats(ModelStatsSnapshotInputs {
                    active_chat_output_tps: 0.0,
                    queue_size: 0,
                    queued_input_size: 0,
                    num_running_queries: 0,
                    total_query_input_size: 0,
                    input_processing_queries: 0,
                    output_generation_queries: 0,
                })
            })
            .unwrap_or_default();
        apply_fixed_last_mean_input_tps(&self.config, &mut stats);
        stats
    }

    pub(super) fn configured_model_allowed(&self, model_id: &str) -> bool {
        self.config.configured_model_ids.is_empty()
            || self
                .config
                .configured_model_ids
                .iter()
                .any(|configured| configured == model_id)
    }
}

fn elapsed_since(now: TokioInstant, then: TokioInstant) -> Duration {
    now.checked_duration_since(then).unwrap_or(Duration::ZERO)
}

fn push_dirty_model(models: &mut Vec<String>, model_id: String) {
    if !models.iter().any(|existing| existing == &model_id) {
        models.push(model_id);
    }
}

#[derive(Debug, Clone, Copy, Default)]
pub(super) struct ModelStatsSnapshotInputs {
    pub(super) active_chat_output_tps: f64,
    pub(super) queue_size: u64,
    pub(super) queued_input_size: u64,
    pub(super) num_running_queries: u64,
    pub(super) total_query_input_size: u64,
    pub(super) input_processing_queries: u64,
    pub(super) output_generation_queries: u64,
}

impl ModelMetricsState {
    pub(super) fn clear_live_output_tps(&mut self) -> bool {
        self.last_stats_event_at = None;
        if self.chat_output_tps_samples.is_empty() {
            return false;
        }
        self.chat_output_tps_samples.clear();
        self.chat_output_tps_sum = 0.0;
        true
    }

    pub(super) fn current_stats(&self, inputs: ModelStatsSnapshotInputs) -> CurrentModelStats {
        let (stats_capabilities, stats_sources) = self.stats_labels();
        CurrentModelStats {
            last_mean_input_tps: self.last_mean_input_tps,
            output_tps: inputs.active_chat_output_tps.max(average_with_sum(
                &self.chat_output_tps_samples,
                self.chat_output_tps_sum,
            )),
            embedding_item_tps: average_with_sum(
                &self.embedding_item_tps_samples,
                self.embedding_item_tps_sum,
            ),
            max_output_tps: self.max_chat_output_tps,
            max_embedding_item_tps: self.max_embedding_item_tps,
            queue_size: inputs.queue_size,
            queued_input_size: inputs.queued_input_size,
            kv_cache_capacity_tokens: self.kv_cache.kv_cache_capacity_tokens,
            kv_cache_used_tokens: self.kv_cache.kv_cache_used_tokens,
            kv_cache_free_tokens: self.kv_cache.kv_cache_free_tokens,
            num_running_queries: inputs.num_running_queries,
            max_engine_concurrency: None,
            total_query_input_size: inputs.total_query_input_size,
            queue_time_estimate_ms_by_priority: None,
            input_processing_queries: inputs.input_processing_queries,
            output_generation_queries: inputs.output_generation_queries,
            stats_observed_at_unix_ms: self.stats_observed_at_unix_ms,
            stats_capabilities,
            stats_sources,
        }
    }

    pub(super) fn stats_labels(&self) -> (Vec<String>, Vec<String>) {
        // These labels are sticky per model metrics state. They describe
        // contract surfaces pylon has observed from this backend, not just the
        // surfaces exercised by the most recent request.
        let mut capabilities = Vec::new();
        let mut sources = Vec::new();
        if self.chunk_usage_stats_observed {
            capabilities.push("request.output.chunk_usage".to_string());
            sources.push("chunk_usage".to_string());
        }
        if self.kv_cache_stats_observed {
            capabilities.push("machine.kv_cache.http".to_string());
            sources.push("kv_cache_stats".to_string());
        }
        if self.engine_stream_stats_observed {
            capabilities.push("model.throughput.engine_stream".to_string());
            sources.push("engine_stats_stream".to_string());
        }
        (capabilities, sources)
    }
}

pub(super) fn current_unix_millis() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .ok()
        .and_then(|duration| u64::try_from(duration.as_millis()).ok())
        .unwrap_or_default()
}

pub(super) fn duration_millis_u64(duration: Duration) -> u64 {
    u64::try_from(duration.as_millis()).unwrap_or(u64::MAX)
}

pub(super) fn output_decode_duration(
    total_duration: Duration,
    time_to_first_output: Option<Duration>,
    time_to_first_token: Option<Duration>,
    duration_floor: Duration,
) -> Option<Duration> {
    if let Some(time_to_first_token) = time_to_first_token {
        // Observation timestamps can arrive with the same coarse clock tick; never underflow decode time.
        let token_duration = total_duration.saturating_sub(time_to_first_token);
        if token_duration >= duration_floor {
            return Some(token_duration);
        }
    }

    time_to_first_output
        // Observation timestamps can arrive with the same coarse clock tick; never underflow decode time.
        .map(|time_to_first_output| total_duration.saturating_sub(time_to_first_output))
}

pub(super) fn tps_for_units(
    units: u64,
    duration: Duration,
    duration_floor: Duration,
) -> Option<f64> {
    if units == 0 || duration < duration_floor {
        return None;
    }
    Some(units as f64 / duration.as_secs_f64())
}

pub(super) fn valid_last_mean_input_tps(last_mean_input_tps: f64) -> bool {
    last_mean_input_tps > 0.0 && last_mean_input_tps.is_finite()
}

pub(super) fn fixed_last_mean_input_tps(config: &StatsCollectorConfig) -> Option<f64> {
    config
        .fixed_last_mean_input_tps
        .filter(|value| valid_last_mean_input_tps(*value))
}

pub(super) fn effective_last_mean_input_tps(config: &StatsCollectorConfig, observed: f64) -> f64 {
    fixed_last_mean_input_tps(config).unwrap_or(observed)
}

pub(super) fn apply_fixed_last_mean_input_tps(
    config: &StatsCollectorConfig,
    stats: &mut CurrentModelStats,
) {
    if let Some(last_mean_input_tps) = fixed_last_mean_input_tps(config) {
        stats.last_mean_input_tps = last_mean_input_tps;
    }
}

pub(super) fn push_sample(
    samples: &mut VecDeque<f64>,
    sum: &mut f64,
    sample: f64,
    window_size: usize,
) {
    if window_size == 0 {
        return;
    }
    samples.push_back(sample);
    *sum += sample;
    while samples.len() > window_size {
        if let Some(removed) = samples.pop_front() {
            *sum -= removed;
        }
    }
}

pub(super) fn average_with_sum(samples: &VecDeque<f64>, sum: f64) -> f64 {
    if samples.is_empty() {
        0.0
    } else {
        sum / samples.len() as f64
    }
}

pub(super) fn average_slice(samples: &[f64]) -> f64 {
    if samples.is_empty() {
        0.0
    } else {
        samples.iter().sum::<f64>() / samples.len() as f64
    }
}

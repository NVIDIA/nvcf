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
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use serde::Deserialize;
use stargate_protocol::common::valid_last_mean_input_tps;
use tokio::time::Instant as TokioInstant;

use crate::generated_request_id::{GeneratedRequestKind, generated_request_kind};
use crate::runtime_state::{ModelGeneration, RequestInputInterval};
use crate::{CurrentModelStats, PylonRuntimeState};

use super::collector::{
    FinalizeRequestUpdate, RequestCounterUpdate, StatsAggregatorUpdate, StatsCollectorConfig,
    StatsUpdateSource,
};
use super::token_metrics::TpsDistribution;
pub(super) const ENGINE_STATS_SOURCE: &str = "engine_stats_stream";

#[derive(Debug, Default)]
pub(super) struct ModelMetricsState {
    pub(super) last_mean_input_tps: f64,
    pub(super) max_input_tps: Option<f64>,
    pub(super) max_engine_concurrency: Option<u64>,
    pub(super) chat_output_tps_samples: VecDeque<f64>,
    pub(super) chat_output_tps_sum: f64,
    pub(super) embedding_item_tps_samples: VecDeque<f64>,
    pub(super) embedding_item_tps_sum: f64,
    pub(super) max_chat_output_tps: f64,
    pub(super) max_embedding_item_tps: f64,
    pub(super) kv_cache: KvCacheStatsSnapshot,
    pub(super) input_tps_distribution: TpsDistribution,
    pub(super) request_input_intervals: RequestInputIntervalWindow,
    pub(super) completed_request_keys: VecDeque<RequestIntervalKey>,
    pub(super) output_token_calibration: OutputTokenCalibration,
    aggregate_state_counted: bool,
    pub(super) counter_output_tps_authoritative: bool,
    pub(super) chunk_usage_stats_observed: bool,
    pub(super) kv_cache_stats_observed: bool,
    pub(super) engine_stream_stats_observed: bool,
    pub(super) last_stats_event_at: Option<TokioInstant>,
    pub(super) stats_observed_at_unix_ms: u64,
}

#[derive(Debug)]
pub(super) struct GenerationMetricsState {
    pub(super) generation: ModelGeneration,
    pub(super) metrics: ModelMetricsState,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub(super) struct RequestIntervalKey {
    request_id: String,
    submitted_at: Instant,
}

impl RequestIntervalKey {
    pub(super) fn new(request_id: &str, submitted_at: Instant) -> Self {
        Self {
            request_id: request_id.to_string(),
            submitted_at,
        }
    }
}

const MIN_OUTPUT_TOKEN_CALIBRATION_SAMPLES: usize = 3;

#[derive(Debug, Default)]
pub(super) struct OutputTokenCalibration {
    ratios: VecDeque<f64>,
}

impl OutputTokenCalibration {
    pub(super) fn observe(
        &mut self,
        exact_output_tokens: u64,
        raw_output_units: u64,
        window_size: usize,
    ) {
        if window_size == 0 || raw_output_units == 0 {
            return;
        }
        let ratio = exact_output_tokens as f64 / raw_output_units as f64;
        if ratio <= 0.0 || !ratio.is_finite() {
            return;
        }
        self.ratios.push_back(ratio);
        while self.ratios.len() > window_size {
            self.ratios.pop_front();
        }
    }

    pub(super) fn scale(&self, raw_output_units: u64) -> u64 {
        if raw_output_units == 0 {
            return 0;
        }
        let Some(factor) = calibration_factor(&self.ratios) else {
            return raw_output_units;
        };
        let scaled = raw_output_units as f64 * factor;
        if !scaled.is_finite() || scaled >= u64::MAX as f64 {
            u64::MAX
        } else {
            (scaled.round() as u64).max(1)
        }
    }

    #[cfg(test)]
    pub(super) fn len(&self) -> usize {
        self.ratios.len()
    }
}

fn calibration_factor(ratios: &VecDeque<f64>) -> Option<f64> {
    if ratios.len() < MIN_OUTPUT_TOKEN_CALIBRATION_SAMPLES {
        return None;
    }
    let mut ratios = ratios.iter().copied().collect::<Vec<_>>();
    ratios.sort_unstable_by(f64::total_cmp);
    let middle = ratios.len() / 2;
    if ratios.len().is_multiple_of(2) {
        Some((ratios[middle - 1] + ratios[middle]) / 2.0)
    } else {
        Some(ratios[middle])
    }
}

// Entries newer than a pending request wait for its usage. Past either bound
// the oldest pending request is dropped from the rate rather than resolved with
// total tokens that may include cached prompt tokens.
// At roughly 100 bytes per entry, 1024 entries cost about 100 KiB per model.
const MAX_RETAINED_INPUT_INTERVALS: usize = 1024;
// Large smoothing windows still keep room for held-back entries.
const MIN_RETAINED_INPUT_INTERVAL_WINDOWS: usize = 8;
// Measured against the newest observed first output, not a wall clock, so it
// bounds staleness relative to newer requests. Decodes longer than this after
// first output are dropped from the rate.
const MAX_PENDING_INPUT_INTERVAL_LAG: Duration = Duration::from_secs(120);

#[derive(Debug)]
struct RetainedInputInterval {
    request_id: String,
    interval: RequestInputInterval,
    input_tokens: u64,
    input_tokens_explicit: bool,
    max_input_tps_eligible: bool,
    pending: bool,
}

pub(super) struct InputIntervalSample<'a> {
    pub(super) request_id: &'a str,
    pub(super) interval: RequestInputInterval,
    pub(super) input_tokens: u64,
    pub(super) input_tokens_explicit: bool,
    // True when `input_tokens` is known to count prefill work: usage split out
    // cached prompt tokens, or the request is a Pylon calibration probe.
    pub(super) max_input_tps_eligible: bool,
    // True while the request still expects usage that has not arrived. The entry
    // keeps its first-output position but is excluded from the rate until resolved.
    pub(super) pending: bool,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub(super) struct InputIntervalRate {
    pub(super) input_tps: f64,
    pub(super) max_input_tps_eligible: bool,
}

// Entries are ordered by first output. The rate covers the resolved entries
// before the oldest pending entry, so it always describes a contiguous run of
// requests; newer resolved entries are held back until that request resolves.
#[derive(Debug, Default)]
pub(super) struct RequestInputIntervalWindow {
    intervals: VecDeque<RetainedInputInterval>,
    evicted_through: Option<Instant>,
    newest_first_output: Option<Instant>,
    published: Option<InputIntervalRate>,
    has_observed_rate: bool,
}

impl RequestInputIntervalWindow {
    #[cfg(test)]
    pub(super) fn len(&self) -> usize {
        self.intervals.len()
    }

    pub(super) fn has_observed_rate(&self) -> bool {
        self.has_observed_rate
    }

    pub(super) fn observe(
        &mut self,
        sample: InputIntervalSample<'_>,
        config: &StatsCollectorConfig,
    ) -> Option<InputIntervalRate> {
        let InputIntervalSample {
            request_id,
            interval,
            input_tokens,
            input_tokens_explicit,
            max_input_tps_eligible,
            pending,
        } = sample;
        if config.smoothing_window_size == 0
            || interval.first_generated_output_at <= interval.submitted_at
        {
            return None;
        }
        self.newest_first_output = Some(
            self.newest_first_output
                .map_or(interval.first_generated_output_at, |newest| {
                    newest.max(interval.first_generated_output_at)
                }),
        );
        let existing = self.intervals.iter().position(|entry| {
            entry.request_id == request_id && entry.interval.submitted_at == interval.submitted_at
        });
        let min_input_tokens = config.min_input_tokens.max(1);
        if input_tokens < min_input_tokens {
            let resolves_pending =
                !pending && existing.is_some_and(|index| self.intervals[index].pending);
            if let Some(index) = existing
                && (input_tokens_explicit || resolves_pending)
            {
                self.intervals.remove(index);
                return self.refresh(config);
            }
            return None;
        }

        if let Some(index) = existing {
            let entry = self
                .intervals
                .get_mut(index)
                .expect("located input interval should remain retained");
            if pending && !entry.pending {
                return None;
            }
            let keep_values = (entry.input_tokens_explicit && !input_tokens_explicit)
                || (entry.max_input_tps_eligible && !max_input_tps_eligible);
            let resolves = entry.pending && !pending;
            if keep_values && !resolves {
                return None;
            }
            let changed = resolves
                || (!keep_values
                    && (entry.interval != interval
                        || entry.input_tokens != input_tokens
                        || entry.max_input_tps_eligible != max_input_tps_eligible));
            entry.pending = pending;
            if !keep_values {
                entry.interval = interval;
                entry.input_tokens = input_tokens;
                entry.input_tokens_explicit |= input_tokens_explicit;
                entry.max_input_tps_eligible = max_input_tps_eligible;
            }
            if !changed {
                return None;
            }
        } else {
            if self
                .evicted_through
                .is_some_and(|evicted| interval.first_generated_output_at <= evicted)
            {
                return None;
            }
            let insertion_index = self
                .intervals
                .iter()
                .position(|entry| {
                    entry.interval.first_generated_output_at > interval.first_generated_output_at
                })
                .unwrap_or(self.intervals.len());
            self.intervals.insert(
                insertion_index,
                RetainedInputInterval {
                    request_id: request_id.to_string(),
                    interval,
                    input_tokens,
                    input_tokens_explicit,
                    max_input_tps_eligible,
                    pending,
                },
            );
        }
        self.refresh(config)
    }

    fn refresh(&mut self, config: &StatsCollectorConfig) -> Option<InputIntervalRate> {
        self.enforce_bounds(config.smoothing_window_size);
        let rate = self.rate(config.duration_floor);
        self.has_observed_rate |= rate.is_some();
        if rate.is_none() || rate == self.published {
            return None;
        }
        self.published = rate;
        rate
    }

    fn enforce_bounds(&mut self, window_size: usize) {
        let retained_limit = MAX_RETAINED_INPUT_INTERVALS
            .max(window_size.saturating_mul(MIN_RETAINED_INPUT_INTERVAL_WINDOWS));
        loop {
            let resolved_prefix = self
                .intervals
                .iter()
                .position(|entry| entry.pending)
                .unwrap_or(self.intervals.len());
            for _ in window_size..resolved_prefix {
                let evicted = self
                    .intervals
                    .pop_front()
                    .expect("resolved input interval prefix should not be empty");
                self.advance_evicted_through(evicted.interval.first_generated_output_at);
            }
            let oldest_pending_index = resolved_prefix.min(window_size);
            let Some(oldest_pending) = self.intervals.get(oldest_pending_index) else {
                return;
            };
            let first_output = oldest_pending.interval.first_generated_output_at;
            let lagging = self.newest_first_output.is_some_and(|newest| {
                newest.saturating_duration_since(first_output) > MAX_PENDING_INPUT_INTERVAL_LAG
            });
            if self.intervals.len() <= retained_limit && !lagging {
                return;
            }
            // Accept a one-request gap instead of counting cached prompt tokens.
            // Advancing the guard makes the dropped request's late usage ignored.
            self.intervals.remove(oldest_pending_index);
            self.advance_evicted_through(first_output);
        }
    }

    fn advance_evicted_through(&mut self, first_output: Instant) {
        self.evicted_through = Some(
            self.evicted_through
                .map_or(first_output, |prior| prior.max(first_output)),
        );
    }

    fn rate(&self, duration_floor: Duration) -> Option<InputIntervalRate> {
        let rate_window = || self.intervals.iter().take_while(|entry| !entry.pending);
        let input_tokens = rate_window().fold(0_u64, |total, entry| {
            total.saturating_add(entry.input_tokens)
        });
        if input_tokens == 0 {
            return None;
        }
        let max_input_tps_eligible = rate_window().all(|entry| entry.max_input_tps_eligible);
        let mut intervals = rate_window()
            .map(|entry| entry.interval)
            .collect::<Vec<_>>();
        intervals.sort_unstable_by_key(|interval| interval.submitted_at);
        let mut intervals = intervals.into_iter();
        let first = intervals.next()?;
        let mut start = first.submitted_at;
        let mut end = first.first_generated_output_at;
        let mut union_duration = Duration::ZERO;
        for interval in intervals {
            if interval.submitted_at <= end {
                end = end.max(interval.first_generated_output_at);
            } else {
                union_duration =
                    union_duration.saturating_add(end.saturating_duration_since(start));
                start = interval.submitted_at;
                end = interval.first_generated_output_at;
            }
        }
        union_duration = union_duration.saturating_add(end.saturating_duration_since(start));
        let duration = union_duration.max(duration_floor);
        let input_tps = input_tokens as f64 / duration.as_secs_f64();
        valid_last_mean_input_tps(input_tps).then_some(InputIntervalRate {
            input_tps,
            max_input_tps_eligible,
        })
    }
}
#[derive(Debug, Clone, Default, Deserialize, PartialEq, Eq)]
pub(super) struct KvCacheStatsSnapshot {
    pub(super) model: String,
    pub(super) kv_cache_capacity_tokens: u64,
    pub(super) kv_cache_used_tokens: u64,
    pub(super) kv_cache_free_tokens: u64,
}
struct RequestCounterState {
    generation: ModelGeneration,
    input: CounterSampleState,
    output: CounterSampleState,
    last_seen_at: TokioInstant,
}
impl RequestCounterState {
    fn new(
        update: &RequestCounterUpdate,
        config: &StatsCollectorConfig,
    ) -> (Self, RequestCounterSamples) {
        let engine_stream = update.source == StatsUpdateSource::EngineStatsStream;
        let baseline = |counter: Option<u64>| counter.filter(|_| !engine_stream).unwrap_or(0);
        let mut state = Self {
            generation: update
                .generation
                .clone()
                .expect("prepared request counter update must have a generation"),
            input: CounterSampleState::new(baseline(update.tokens_processed), update.observed_at),
            output: CounterSampleState::new(baseline(update.tokens_generated), update.observed_at),
            last_seen_at: update.observed_at,
        };
        let samples = state.observe(update, config);
        (state, samples)
    }

    fn regressed(&self, update: &RequestCounterUpdate) -> bool {
        [
            (update.tokens_processed, self.input.observed),
            (update.tokens_generated, self.output.observed),
        ]
        .into_iter()
        .any(|(next, observed)| next.is_some_and(|next| next < observed))
    }

    fn observe(
        &mut self,
        update: &RequestCounterUpdate,
        config: &StatsCollectorConfig,
    ) -> RequestCounterSamples {
        self.last_seen_at = update.observed_at;
        (
            self.input.observe(
                update.tokens_processed,
                update.observed_at,
                config.min_input_tokens,
                config.duration_floor,
            ),
            self.output.observe(
                update.tokens_generated,
                update.observed_at,
                config.min_output_tokens,
                config.duration_floor,
            ),
        )
    }
}
enum RequestCounterLifecycle {
    Observed {
        generation: ModelGeneration,
        last_seen_at: TokioInstant,
    },
    Live(RequestCounterState),
    Finalized {
        generation: ModelGeneration,
        observed_at: TokioInstant,
    },
}

impl RequestCounterLifecycle {
    fn generation(&self) -> &ModelGeneration {
        match self {
            Self::Observed { generation, .. } => generation,
            Self::Live(state) => &state.generation,
            Self::Finalized { generation, .. } => generation,
        }
    }

    fn is_live(&self) -> bool {
        matches!(self, Self::Live(_))
    }
}

struct CounterSampleState {
    observed: u64,
    sampled: u64,
    sampled_at: TokioInstant,
}
type CounterSample = (u64, Duration);
type RequestCounterSamples = (Option<CounterSample>, Option<CounterSample>);
pub(super) type ModelStatsUpdate = (ModelGeneration, CurrentModelStats);

impl CounterSampleState {
    fn new(observed: u64, observed_at: TokioInstant) -> Self {
        Self {
            observed,
            sampled: observed,
            sampled_at: observed_at,
        }
    }

    fn observe(
        &mut self,
        next: Option<u64>,
        observed_at: TokioInstant,
        min_units: u64,
        duration_floor: Duration,
    ) -> Option<CounterSample> {
        let next = next?;
        let prior_observed = self.observed;
        self.observed = next;
        let units = next.saturating_sub(self.sampled);
        if units < min_units.max(1) {
            return None;
        }
        let duration = observed_at.saturating_duration_since(self.sampled_at);
        let duration = if self.sampled == 0 && prior_observed == 0 {
            duration.max(duration_floor)
        } else {
            duration
        };
        if duration < duration_floor {
            return None;
        }
        (self.sampled, self.sampled_at) = (next, observed_at);
        Some((units, duration))
    }
}

pub(super) struct InputThroughputSample {
    pub(super) units: u64,
    pub(super) duration: Duration,
    pub(super) clamp_duration_to_floor: bool,
}

pub(super) struct EmbeddingThroughputSample {
    pub(super) items: u64,
    pub(super) duration: Duration,
}

pub(super) struct StatsAggregator {
    pub(super) config: StatsCollectorConfig,
    pub(super) runtime_state: PylonRuntimeState,
    pub(super) per_model: HashMap<String, GenerationMetricsState>,
    request_counters: HashMap<String, RequestCounterLifecycle>,
    live_request_count: usize,
    pub(super) aggregate_model_state_count: usize,
    unix_ms_anchor: u64,
    instant_anchor: TokioInstant,
}

impl StatsAggregator {
    pub(super) fn new(config: StatsCollectorConfig, runtime_state: PylonRuntimeState) -> Self {
        Self {
            config,
            runtime_state,
            per_model: HashMap::new(),
            request_counters: HashMap::new(),
            live_request_count: 0,
            aggregate_model_state_count: 0,
            unix_ms_anchor: current_unix_millis(),
            instant_anchor: TokioInstant::now(),
        }
    }

    pub(super) fn begin_generation(
        &mut self,
        generation: ModelGeneration,
        initialization: super::collector::ModelStatsInitialization,
    ) -> Option<ModelStatsUpdate> {
        if self.per_model.contains_key(generation.model_id()) {
            return None;
        }
        let metrics = match initialization {
            super::collector::ModelStatsInitialization::Empty => ModelMetricsState::default(),
            super::collector::ModelStatsInitialization::ConfiguredInputTps { input_tps } => {
                let input_tps_distribution = TpsDistribution::bootstrap(input_tps)
                    .expect("configured input TPS must be positive and finite");
                ModelMetricsState {
                    last_mean_input_tps: input_tps,
                    max_input_tps: Some(input_tps),
                    input_tps_distribution,
                    aggregate_state_counted: true,
                    ..ModelMetricsState::default()
                }
            }
        };
        self.aggregate_model_state_count += usize::from(metrics.aggregate_state_counted);
        self.per_model.insert(
            generation.model_id().to_string(),
            GenerationMetricsState {
                generation: generation.clone(),
                metrics,
            },
        );
        let stats = self.snapshot(generation.model_id());
        Some((generation, stats))
    }

    pub(super) fn retire_generation(&mut self, generation: &ModelGeneration) -> bool {
        if self
            .per_model
            .get(generation.model_id())
            .is_none_or(|state| state.generation != *generation)
        {
            return false;
        }
        let retired = self
            .per_model
            .remove(generation.model_id())
            .expect("validated generation should exist");
        self.aggregate_model_state_count -= usize::from(retired.metrics.aggregate_state_counted);
        let live_request_count = &mut self.live_request_count;
        self.request_counters.retain(|_, lifecycle| {
            let owned = lifecycle.generation() == generation;
            let live = lifecycle.is_live();
            if owned && live {
                adjust_live_count(live_request_count, -1);
            }
            !owned
        });
        true
    }

    pub(super) fn snapshot_generation(
        &self,
        generation: &ModelGeneration,
    ) -> Option<CurrentModelStats> {
        self.per_model
            .get(generation.model_id())
            .filter(|state| state.generation == *generation)
            .map(|_| self.snapshot(generation.model_id()))
    }

    pub(super) fn current_generation(&self, model_id: &str) -> Option<&ModelGeneration> {
        self.per_model.get(model_id).map(|state| &state.generation)
    }

    #[cfg(test)]
    pub(super) fn apply_update(&mut self, update: StatsAggregatorUpdate) -> Vec<ModelStatsUpdate> {
        let mut updated_models = Vec::new();
        self.apply_update_into(update, &mut updated_models);
        updated_models
    }

    pub(super) fn apply_update_into(
        &mut self,
        update: StatsAggregatorUpdate,
        updated_models: &mut Vec<ModelStatsUpdate>,
    ) {
        match update {
            StatsAggregatorUpdate::EngineConcurrency(update) => {
                let Some(state) = self.per_model.get_mut(&update.model_id) else {
                    return;
                };
                if update
                    .generation
                    .as_ref()
                    .is_some_and(|generation| generation != &state.generation)
                    || state.metrics.max_engine_concurrency == update.max_engine_concurrency
                {
                    return;
                }
                state.metrics.max_engine_concurrency = update.max_engine_concurrency;
                state.metrics.stats_observed_at_unix_ms = current_unix_millis();
                let generation = state.generation.clone();
                updated_models.push((generation, self.snapshot(&update.model_id)));
            }
            StatsAggregatorUpdate::RequestCounters(update) => {
                self.apply_request_counters_into(update, updated_models)
            }
            StatsAggregatorUpdate::FinalizeRequest(update) => {
                updated_models.extend(self.finalize_request(update))
            }
            StatsAggregatorUpdate::EnableOpenAiFallback => self.enable_openai_fallback(),
        }
    }

    pub(super) fn apply_control_update(&mut self, update: &StatsAggregatorUpdate) -> bool {
        if !matches!(update, StatsAggregatorUpdate::EnableOpenAiFallback) {
            return false;
        }
        self.enable_openai_fallback();
        true
    }

    pub(super) fn openai_fallback_stats_enabled(&self) -> bool {
        self.config.openai_fallback_stats_enabled
    }
    pub(super) fn live_request_count(&self) -> usize {
        self.live_request_count
    }
    #[cfg(test)]
    pub(super) fn request_counter_identity_count(&self) -> usize {
        self.request_counters.len()
    }
    pub(super) fn model_state_count(&self) -> usize {
        self.aggregate_model_state_count
    }
    pub(super) fn remember_request_owner(
        &mut self,
        request_id: &str,
        generation: &ModelGeneration,
    ) {
        if self.current_generation(generation.model_id()) == Some(generation) {
            let observed = || RequestCounterLifecycle::Observed {
                generation: generation.clone(),
                last_seen_at: TokioInstant::now(),
            };
            match self.request_counters.get_mut(request_id) {
                Some(RequestCounterLifecycle::Observed {
                    generation: owner,
                    last_seen_at,
                }) if owner == generation => *last_seen_at = TokioInstant::now(),
                Some(RequestCounterLifecycle::Live(state)) if state.generation == *generation => {}
                Some(RequestCounterLifecycle::Finalized {
                    generation: owner, ..
                }) if owner == generation => {}
                Some(lifecycle) => *lifecycle = observed(),
                None => {
                    self.request_counters
                        .insert(request_id.to_string(), observed());
                }
            }
        }
    }
    pub(super) fn unix_millis_at(&self, observed_at: TokioInstant) -> u64 {
        match observed_at.checked_duration_since(self.instant_anchor) {
            Some(elapsed) => self
                .unix_ms_anchor
                .saturating_add(duration_millis_u64(elapsed)),
            None => self.unix_ms_anchor.saturating_sub(duration_millis_u64(
                self.instant_anchor.saturating_duration_since(observed_at),
            )),
        }
    }

    pub(super) fn sweep_stale(&mut self, now: TokioInstant) -> Vec<ModelStatsUpdate> {
        let mut dirty_models = Vec::new();
        let request_ttl = self.config.engine_stats_request_ttl;
        if !request_ttl.is_zero() {
            let metrics = self.runtime_state.metrics();
            let live_request_count = &mut self.live_request_count;
            self.request_counters
                .retain(|request_id, lifecycle| match lifecycle {
                    RequestCounterLifecycle::Observed { last_seen_at, .. } => {
                        now.saturating_duration_since(*last_seen_at) < request_ttl
                    }
                    RequestCounterLifecycle::Live(state) => {
                        if now.saturating_duration_since(state.last_seen_at) < request_ttl {
                            return true;
                        }
                        let model_id = state.generation.model_id().to_string();
                        *lifecycle = RequestCounterLifecycle::Finalized {
                            generation: state.generation.clone(),
                            observed_at: now,
                        };
                        adjust_live_count(live_request_count, -1);
                        tracing::warn!(
                            request_id,
                            model_id,
                            ttl_ms = request_ttl.as_millis(),
                            "removing stale engine stats request entry"
                        );
                        if let Some(metrics) = metrics {
                            metrics
                                .observe_engine_stats_stale_cleanup("request", ENGINE_STATS_SOURCE);
                        }
                        push_dirty_model(&mut dirty_models, model_id);
                        true
                    }
                    RequestCounterLifecycle::Finalized { observed_at, .. } => {
                        now.saturating_duration_since(*observed_at) < request_ttl
                    }
                });
        }

        let model_ttl = self.config.engine_stats_model_ttl;
        if !model_ttl.is_zero() {
            for (model_id, generation_state) in &mut self.per_model {
                let state = &mut generation_state.metrics;
                if state.last_stats_event_at.is_some_and(|observed_at| {
                    now.saturating_duration_since(observed_at) >= model_ttl
                }) && state.clear_live_output_tps()
                {
                    state.stats_observed_at_unix_ms = current_unix_millis();
                    tracing::warn!(
                        model_id,
                        ttl_ms = model_ttl.as_millis(),
                        "clearing stale engine stats output TPS"
                    );
                    if let Some(metrics) = self.runtime_state.metrics() {
                        metrics.observe_engine_stats_stale_cleanup("stats", ENGINE_STATS_SOURCE);
                    }
                    push_dirty_model(&mut dirty_models, model_id.clone());
                }
            }
        }

        if let Some(metrics) = self.runtime_state.metrics() {
            metrics
                .observe_engine_stats_model_states(ENGINE_STATS_SOURCE, self.model_state_count());
            for _ in &dirty_models {
                metrics.observe_engine_stats_dirty_snapshot(ENGINE_STATS_SOURCE, "stale");
            }
        }

        dirty_models
            .into_iter()
            .map(|model_id| self.snapshot_update(model_id))
            .collect()
    }

    pub(super) fn apply_request_counters_into(
        &mut self,
        mut update: RequestCounterUpdate,
        updated_models: &mut Vec<ModelStatsUpdate>,
    ) {
        if is_duplicate_calibration_update(&update) {
            return;
        }
        if !self.prepare_request_counter_update(&mut update) {
            return;
        }
        if !self.request_counter_update_allowed(&update) {
            return;
        }
        let request_id = std::mem::take(&mut update.request_id);
        let samples = self.apply_request_counter_transition(request_id, &update);
        self.publish_request_counter_samples(update, samples, updated_models);
    }

    fn prepare_request_counter_update(&mut self, update: &mut RequestCounterUpdate) -> bool {
        let Some(generation) =
            self.resolve_request_generation(&update.request_id, update.generation.as_ref())
        else {
            return false;
        };
        update.generation = Some(generation.clone());
        if self.current_generation(&update.model_id) == Some(&generation) {
            return true;
        }
        self.finalize_retired_request(update, generation);
        false
    }

    fn resolve_request_generation(
        &self,
        request_id: &str,
        explicit: Option<&ModelGeneration>,
    ) -> Option<ModelGeneration> {
        explicit.cloned().or_else(|| {
            self.request_counters
                .get(request_id)
                .map(|lifecycle| lifecycle.generation().clone())
        })
    }

    fn finalize_retired_request(
        &mut self,
        update: &RequestCounterUpdate,
        generation: ModelGeneration,
    ) {
        if update.finished
            && matches!(
                self.request_counters.get(&update.request_id),
                Some(RequestCounterLifecycle::Live(state)) if state.generation == generation
            )
        {
            self.request_counters.insert(
                update.request_id.clone(),
                RequestCounterLifecycle::Finalized {
                    generation,
                    observed_at: update.observed_at,
                },
            );
            adjust_live_count(&mut self.live_request_count, -1);
        }
    }
    fn request_counter_update_allowed(&self, update: &RequestCounterUpdate) -> bool {
        let current_lifecycle = self.request_counters.get(&update.request_id);
        if matches!(
            current_lifecycle,
            Some(RequestCounterLifecycle::Finalized { generation, .. })
                if Some(generation) == update.generation.as_ref()
        ) {
            tracing::warn!(
                request_id = %update.request_id,
                source = ?update.source,
                "ignoring stats event after request finalization"
            );
            if let Some(metrics) = self.runtime_state.metrics() {
                metrics.observe_engine_stats_invalid_event("post_finalize");
            }
            return false;
        }

        if let Some(RequestCounterLifecycle::Live(state)) = current_lifecycle
            && state.generation.model_id() == update.model_id.as_str()
            && state.regressed(update)
        {
            tracing::warn!(
                request_id = %update.request_id,
                model_id = %update.model_id,
                prior_tokens_processed = state.input.observed,
                tokens_processed = update.tokens_processed.unwrap_or(state.input.observed),
                prior_tokens_generated = state.output.observed,
                tokens_generated = update.tokens_generated.unwrap_or(state.output.observed),
                source = ?update.source,
                "ignoring regressing request stats counters"
            );
            if let Some(metrics) = self.runtime_state.metrics() {
                metrics.observe_engine_stats_invalid_event("regressing_counters");
            }
            return false;
        }
        true
    }
    fn apply_request_counter_transition(
        &mut self,
        request_id: String,
        update: &RequestCounterUpdate,
    ) -> RequestCounterSamples {
        let previous = self.request_counters.remove(&request_id);
        let previous_was_live = matches!(&previous, Some(RequestCounterLifecycle::Live(_)));
        let (state, samples) = match previous {
            Some(RequestCounterLifecycle::Live(mut state))
                if state.generation.model_id() == update.model_id.as_str()
                    && update.generation.as_ref() == Some(&state.generation) =>
            {
                let samples = state.observe(update, &self.config);
                (state, samples)
            }
            Some(RequestCounterLifecycle::Live(state)) => {
                tracing::warn!(
                    request_id = %request_id,
                    prior_model = %state.generation.model_id(),
                    model_id = %update.model_id,
                    "resetting request stats after model changed"
                );
                RequestCounterState::new(update, &self.config)
            }
            Some(RequestCounterLifecycle::Observed { .. })
            | Some(RequestCounterLifecycle::Finalized { .. })
            | None => RequestCounterState::new(update, &self.config),
        };
        let live_count_delta = (!update.finished as isize) - (previous_was_live as isize);
        adjust_live_count(&mut self.live_request_count, live_count_delta);
        let lifecycle = if update.finished {
            RequestCounterLifecycle::Finalized {
                generation: update
                    .generation
                    .clone()
                    .expect("allowed update must have a generation"),
                observed_at: update.observed_at,
            }
        } else {
            RequestCounterLifecycle::Live(state)
        };
        assert!(
            self.request_counters
                .insert(request_id, lifecycle)
                .is_none()
        );
        samples
    }
    fn publish_request_counter_samples(
        &mut self,
        update: RequestCounterUpdate,
        (input_sample, output_sample): RequestCounterSamples,
        updated_models: &mut Vec<ModelStatsUpdate>,
    ) {
        let stats_observed_at_unix_ms = self.unix_millis_at(update.observed_at);
        let dirty = {
            let config = &self.config;
            let generation_state = aggregate_model_state(
                &mut self.per_model,
                &mut self.aggregate_model_state_count,
                &update.model_id,
            )
            .expect("allowed model generation should exist");
            let model_state = &mut generation_state.metrics;
            model_state.last_stats_event_at = Some(update.observed_at);
            model_state.stats_observed_at_unix_ms = stats_observed_at_unix_ms;

            let mut dirty = update.source == StatsUpdateSource::EngineStatsStream
                && !std::mem::replace(&mut model_state.engine_stream_stats_observed, true);
            if let Some((units, duration)) = input_sample {
                dirty |= apply_input_throughput_sample(
                    config,
                    model_state,
                    InputThroughputSample {
                        units,
                        duration,
                        clamp_duration_to_floor: false,
                    },
                );
            }
            if let Some((units, duration)) = output_sample
                && let Some(output_tps) = tps_for_units(units, duration, config.duration_floor)
            {
                model_state.max_chat_output_tps = model_state.max_chat_output_tps.max(output_tps);
                model_state.counter_output_tps_authoritative = true;
                push_sample(
                    &mut model_state.chat_output_tps_samples,
                    &mut model_state.chat_output_tps_sum,
                    output_tps,
                    config.smoothing_window_size,
                );
                dirty = true;
            }
            dirty
        };
        if dirty {
            updated_models.push(self.snapshot_update(update.model_id));
        }
    }

    pub(super) fn finalize_request(
        &mut self,
        update: FinalizeRequestUpdate,
    ) -> Option<ModelStatsUpdate> {
        let state = self.transition_request_to_finalized(&update)?;
        adjust_live_count(&mut self.live_request_count, -1);
        let model_id = state.generation.model_id().to_string();
        if self.current_generation(&model_id) != Some(&state.generation) {
            return None;
        }
        let stats_observed_at_unix_ms = self.unix_millis_at(update.observed_at);
        if let Some(model_state) = self.per_model.get_mut(&model_id) {
            model_state.metrics.stats_observed_at_unix_ms = stats_observed_at_unix_ms;
        }
        tracing::debug!(
            request_id = update.request_id,
            source = ?update.source,
            "finalized request stats"
        );
        Some(self.snapshot_update(model_id))
    }

    fn transition_request_to_finalized(
        &mut self,
        update: &FinalizeRequestUpdate,
    ) -> Option<RequestCounterState> {
        let generation =
            self.resolve_request_generation(&update.request_id, update.generation.as_ref())?;
        if matches!(
            self.request_counters.get(&update.request_id),
            Some(RequestCounterLifecycle::Live(state)) if state.generation != generation
        ) {
            return None;
        }
        let previous = self.request_counters.insert(
            update.request_id.clone(),
            RequestCounterLifecycle::Finalized {
                generation,
                observed_at: update.observed_at,
            },
        );
        match previous {
            Some(RequestCounterLifecycle::Live(state)) => Some(state),
            Some(RequestCounterLifecycle::Observed { .. })
            | Some(RequestCounterLifecycle::Finalized { .. })
            | None => None,
        }
    }

    pub(super) fn record_engine_embedding_sample(
        &mut self,
        model_id: &str,
        sample: EmbeddingThroughputSample,
    ) -> bool {
        let duration_floor = self.config.duration_floor;
        let Some(embedding_item_tps) = tps_for_units(
            sample.items,
            sample.duration.max(duration_floor),
            duration_floor,
        ) else {
            return false;
        };
        let Some(model_state) = aggregate_model_state(
            &mut self.per_model,
            &mut self.aggregate_model_state_count,
            model_id,
        ) else {
            return false;
        };
        let model_state = &mut model_state.metrics;
        model_state.stats_observed_at_unix_ms = current_unix_millis();
        model_state.max_embedding_item_tps =
            model_state.max_embedding_item_tps.max(embedding_item_tps);
        push_sample(
            &mut model_state.embedding_item_tps_samples,
            &mut model_state.embedding_item_tps_sum,
            embedding_item_tps,
            self.config.smoothing_window_size,
        );
        true
    }

    fn enable_openai_fallback(&mut self) {
        if self.config.openai_fallback_stats_enabled {
            return;
        }
        self.config.openai_fallback_stats_enabled = true;
        tracing::warn!("OpenAI fallback stats enabled after engine stats stream was unsupported");
        if let Some(metrics) = self.runtime_state.metrics() {
            metrics.observe_engine_stats_source_transition(
                ENGINE_STATS_SOURCE,
                "openai_fallback",
                "unsupported",
            );
        }
    }

    fn snapshot_update(&self, model_id: String) -> ModelStatsUpdate {
        let generation = self
            .current_generation(&model_id)
            .cloned()
            .expect("snapshot generation should still be current");
        let stats = self.snapshot(&model_id);
        (generation, stats)
    }
}

pub(super) fn aggregate_model_state<'a>(
    per_model: &'a mut HashMap<String, GenerationMetricsState>,
    aggregate_model_state_count: &mut usize,
    model_id: &str,
) -> Option<&'a mut GenerationMetricsState> {
    let model_state = per_model.get_mut(model_id)?;
    if !std::mem::replace(&mut model_state.metrics.aggregate_state_counted, true) {
        *aggregate_model_state_count += 1;
    }
    Some(model_state)
}

fn adjust_live_count(count: &mut usize, delta: isize) {
    *count = count
        .checked_add_signed(delta)
        .expect("live request count overflowed");
}

pub(super) fn apply_input_throughput_sample(
    config: &StatsCollectorConfig,
    model_state: &mut ModelMetricsState,
    sample: InputThroughputSample,
) -> bool {
    if sample.units < config.min_input_tokens {
        return false;
    }
    let duration = if sample.clamp_duration_to_floor {
        sample.duration.max(config.duration_floor)
    } else {
        sample.duration
    };
    let Some(input_tps) = tps_for_units(sample.units, duration, config.duration_floor) else {
        return false;
    };
    model_state.input_tps_distribution.update(input_tps);
    let mean_input_tps = model_state.input_tps_distribution.mean;
    if !model_state.input_tps_distribution.has_sufficient_data()
        || !valid_last_mean_input_tps(mean_input_tps)
    {
        return false;
    }
    if model_state.last_mean_input_tps == mean_input_tps {
        return false;
    }
    model_state.publish_mean_input_tps(mean_input_tps, true);
    true
}

fn is_duplicate_calibration_update(update: &RequestCounterUpdate) -> bool {
    update.source == StatsUpdateSource::EngineStatsStream
        && generated_request_kind(&update.request_id) == Some(GeneratedRequestKind::Calibration)
}

fn push_dirty_model(models: &mut Vec<String>, model_id: String) {
    if !models.contains(&model_id) {
        models.push(model_id);
    }
}

#[derive(Clone, Copy)]
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
    // The maximum follows the published smoothed mean rather than raw samples,
    // so the mean dilutes a single outlier sample before it can raise the weight.
    // Callers pass update_max = false for a fallback window that may count cached
    // prompt tokens as prefill; the mean still moves, the maximum does not.
    pub(super) fn publish_mean_input_tps(&mut self, input_tps: f64, update_max: bool) -> bool {
        let mean_changed = self.last_mean_input_tps != input_tps;
        self.last_mean_input_tps = input_tps;
        if !update_max {
            return mean_changed;
        }
        let next_max = self
            .max_input_tps
            .map_or(input_tps, |max| max.max(input_tps));
        let max_changed = self.max_input_tps != Some(next_max);
        self.max_input_tps = Some(next_max);
        mean_changed || max_changed
    }

    pub(super) fn clear_live_output_tps(&mut self) -> bool {
        self.last_stats_event_at = None;
        if self.chat_output_tps_samples.is_empty() {
            return false;
        }
        self.chat_output_tps_samples.clear();
        self.chat_output_tps_sum = 0.0;
        self.counter_output_tps_authoritative = false;
        true
    }

    pub(super) fn current_stats(&self, inputs: ModelStatsSnapshotInputs) -> CurrentModelStats {
        let (stats_capabilities, stats_sources) = self.stats_labels();
        let active_chat_output_tps = if self.counter_output_tps_authoritative {
            0.0
        } else {
            inputs.active_chat_output_tps
        };
        CurrentModelStats {
            last_mean_input_tps: self.last_mean_input_tps,
            max_input_tps: self.max_input_tps,
            output_tps: active_chat_output_tps.max(average_with_sum(
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
            max_engine_concurrency: self.max_engine_concurrency,
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
        // Labels are sticky capabilities observed over the model state's lifetime.
        let mut capabilities = Vec::new();
        let mut sources = Vec::new();
        for (observed, capability, source) in [
            (
                self.chunk_usage_stats_observed,
                "request.output.chunk_usage",
                "chunk_usage",
            ),
            (
                self.engine_stream_stats_observed,
                "model.throughput.engine_stream",
                ENGINE_STATS_SOURCE,
            ),
            (
                self.kv_cache_stats_observed,
                "machine.kv_cache.http",
                "kv_cache_stats",
            ),
        ] {
            if observed {
                capabilities.push(capability.to_string());
                sources.push(source.to_string());
            }
        }
        (capabilities, sources)
    }
}

pub(super) fn current_unix_millis() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| u64::try_from(d.as_millis()).unwrap_or_default())
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
    // Observation timestamps can arrive with the same coarse clock tick; never underflow decode time.
    time_to_first_token
        .map(|first_token| total_duration.saturating_sub(first_token))
        .filter(|duration| *duration >= duration_floor)
        .or_else(|| time_to_first_output.map(|first| total_duration.saturating_sub(first)))
}

pub(super) fn tps_for_units(
    units: u64,
    duration: Duration,
    duration_floor: Duration,
) -> Option<f64> {
    (units > 0 && duration >= duration_floor).then(|| units as f64 / duration.as_secs_f64())
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
        let removed = samples
            .pop_front()
            .expect("non-empty smoothing window lost its oldest sample");
        *sum -= removed;
    }
}

pub(super) fn average_with_sum(samples: &VecDeque<f64>, sum: f64) -> f64 {
    match samples.len() {
        0 => 0.0,
        count => sum / count as f64,
    }
}

#[cfg(test)]
mod output_token_calibration_tests {
    use super::*;

    #[test]
    fn bounded_median_activates_after_three_samples() {
        let mut calibration = OutputTokenCalibration::default();

        calibration.observe(2, 1, 3);
        calibration.observe(8, 2, 3);
        assert_eq!(calibration.scale(5), 5);
        calibration.observe(30, 3, 3);
        assert_eq!(calibration.scale(5), 20);

        calibration.observe(40, 2, 3);
        assert_eq!(calibration.len(), 3);
        assert_eq!(calibration.scale(5), 50);
    }

    #[test]
    fn invalid_samples_are_rejected_and_scaling_saturates() {
        let mut calibration = OutputTokenCalibration::default();

        calibration.observe(5, 0, 3);
        calibration.observe(0, 5, 3);
        calibration.observe(5, 1, 0);
        assert_eq!(calibration.len(), 0);

        for _ in 0..3 {
            calibration.observe(u64::MAX, 1, 3);
        }
        assert_eq!(calibration.scale(u64::MAX), u64::MAX);
    }

    #[test]
    fn even_sample_count_uses_mean_of_middle_ratios() {
        let mut calibration = OutputTokenCalibration::default();

        calibration.observe(2, 1, 4);
        calibration.observe(4, 1, 4);
        calibration.observe(10, 1, 4);
        calibration.observe(20, 1, 4);

        assert_eq!(calibration.scale(2), 14);
    }

    #[test]
    fn positive_raw_units_never_scale_to_zero() {
        let mut calibration = OutputTokenCalibration::default();

        for _ in 0..3 {
            calibration.observe(1, 4, 3);
        }

        assert_eq!(calibration.scale(0), 0);
        assert_eq!(calibration.scale(1), 1);
    }
}

#[cfg(test)]
mod request_input_interval_window_tests {
    use super::*;

    const UNCACHED: u64 = 100;
    const TOTAL: u64 = 150;

    fn config(smoothing_window_size: usize) -> StatsCollectorConfig {
        StatsCollectorConfig {
            smoothing_window_size,
            ..Default::default()
        }
    }

    fn interval(start: Instant, submitted_ms: u64) -> RequestInputInterval {
        let submitted_at = start + Duration::from_millis(submitted_ms);
        RequestInputInterval {
            submitted_at,
            first_generated_output_at: submitted_at + Duration::from_millis(500),
        }
    }

    // A deferred request before usage: header token estimate, held as pending.
    fn placeholder(request_id: &str, interval: RequestInputInterval) -> InputIntervalSample<'_> {
        InputIntervalSample {
            request_id,
            interval,
            input_tokens: TOTAL,
            input_tokens_explicit: false,
            max_input_tps_eligible: false,
            pending: true,
        }
    }

    // A terminal observation whose usage reported cached prompt tokens.
    fn usage(request_id: &str, interval: RequestInputInterval) -> InputIntervalSample<'_> {
        InputIntervalSample {
            request_id,
            interval,
            input_tokens: UNCACHED,
            input_tokens_explicit: true,
            max_input_tps_eligible: true,
            pending: false,
        }
    }

    // A request without expected usage: resolved immediately with total tokens.
    fn live(request_id: &str, interval: RequestInputInterval) -> InputIntervalSample<'_> {
        InputIntervalSample {
            pending: false,
            ..placeholder(request_id, interval)
        }
    }

    fn retained(window: &RequestInputIntervalWindow) -> Vec<(&str, bool)> {
        window
            .intervals
            .iter()
            .map(|entry| (entry.request_id.as_str(), entry.pending))
            .collect()
    }

    fn current(window: &RequestInputIntervalWindow) -> Option<InputIntervalRate> {
        window.rate(config(1).duration_floor)
    }

    fn assert_tps(rate: Option<InputIntervalRate>, expected: f64, eligible: bool) {
        let rate = rate.expect("input rate should be available");
        assert!(
            (rate.input_tps - expected).abs() < 1e-6,
            "expected {expected} TPS, got {}",
            rate.input_tps
        );
        assert_eq!(rate.max_input_tps_eligible, eligible);
    }

    #[test]
    fn long_decodes_keep_the_rate_window_contiguous() {
        let config = config(4);
        let start = Instant::now();
        let ids = (0..40)
            .map(|index| format!("req-{index}"))
            .collect::<Vec<_>>();
        // Requests arrive every 100 ms with 500 ms prefill. Even requests finish
        // 50 ms after first output; odd requests decode for about 2 s.
        let mut events = Vec::new();
        for index in 0..40_u64 {
            let first_output_ms = index * 100 + 500;
            let decode_ms = if index % 2 == 0 { 50 } else { 2_050 };
            events.push((first_output_ms, index as usize, true));
            events.push((first_output_ms + decode_ms, index as usize, false));
        }
        events.sort_by_key(|(at_ms, _, _)| *at_ms);

        let mut window = RequestInputIntervalWindow::default();
        let mut published = Vec::new();
        let mut contiguous = false;
        for (_, index, first_output) in events {
            let request_interval = interval(start, index as u64 * 100);
            let sample = if first_output {
                placeholder(&ids[index], request_interval)
            } else {
                usage(&ids[index], request_interval)
            };
            if let Some(rate) = window.observe(sample, &config) {
                published.push(rate.input_tps);
            }
            if contiguous {
                assert_tps(current(&window), 500.0, true);
            } else {
                contiguous =
                    current(&window).is_some_and(|rate| (rate.input_tps - 500.0).abs() < 1e-6);
            }
        }

        assert!(contiguous);
        assert_tps(window.published, 500.0, true);
        for rate in published {
            assert!(
                (rate - 400.0 / 1.1).abs() > 1e-3 && (rate - 400.0 / 1.4).abs() > 1e-3,
                "published a non-contiguous window rate {rate}"
            );
        }
    }

    #[test]
    fn pending_entry_holds_back_newer_entries_until_resolved() {
        let config = config(2);
        let start = Instant::now();
        let mut window = RequestInputIntervalWindow::default();

        assert_tps(
            window.observe(usage("z", interval(start, 0)), &config),
            200.0,
            true,
        );
        assert!(
            window
                .observe(placeholder("a", interval(start, 100)), &config)
                .is_none()
        );
        assert!(
            window
                .observe(usage("b", interval(start, 200)), &config)
                .is_none()
        );
        assert!(
            window
                .observe(placeholder("a", interval(start, 100)), &config)
                .is_none()
        );
        assert_eq!(retained(&window), [("z", false), ("a", true), ("b", false)]);

        assert_tps(
            window.observe(usage("a", interval(start, 100)), &config),
            200.0 / 0.6,
            true,
        );
        assert_eq!(retained(&window), [("a", false), ("b", false)]);
    }

    #[test]
    fn retained_limit_drops_the_oldest_pending_entry() {
        let config = config(1);
        let start = Instant::now();
        let mut window = RequestInputIntervalWindow::default();
        let earlier = InputIntervalSample {
            input_tokens: 400,
            ..usage("z", interval(start, 0))
        };
        assert_tps(window.observe(earlier, &config), 800.0, true);
        window.observe(placeholder("a", interval(start, 100)), &config);
        let ids = (0..MAX_RETAINED_INPUT_INTERVALS - 2)
            .map(|index| format!("n-{index}"))
            .collect::<Vec<_>>();
        for (index, request_id) in ids.iter().enumerate() {
            let request_interval = interval(start, 200 + index as u64 * 100);
            assert!(
                window
                    .observe(usage(request_id, request_interval), &config)
                    .is_none(),
                "held-back entries must not publish while under the limit"
            );
        }
        assert_eq!(window.len(), MAX_RETAINED_INPUT_INTERVALS);

        let last = format!("n-{}", ids.len());
        let last_interval = interval(start, 200 + ids.len() as u64 * 100);
        assert_tps(
            window.observe(usage(&last, last_interval), &config),
            200.0,
            true,
        );
        assert_eq!(retained(&window), [(last.as_str(), false)]);

        assert!(
            window
                .observe(usage("a", interval(start, 100)), &config)
                .is_none(),
            "usage for a dropped request must be ignored"
        );
        assert_eq!(window.len(), 1);
    }

    #[test]
    fn lagging_pending_entry_is_dropped_by_newer_first_outputs() {
        let config = config(8);
        let start = Instant::now();
        let mut window = RequestInputIntervalWindow::default();
        let ids = (0..8).map(|index| format!("r-{index}")).collect::<Vec<_>>();
        for (index, request_id) in ids.iter().enumerate() {
            window.observe(
                usage(request_id, interval(start, index as u64 * 1_000)),
                &config,
            );
        }
        assert_tps(window.published, 200.0, true);
        window.observe(placeholder("stuck", interval(start, 8_000)), &config);

        // Faster requests arrive every second after the stuck one.
        let lag_ms = MAX_PENDING_INPUT_INTERVAL_LAG.as_millis() as u64;
        let ids = (0..=lag_ms / 1_000)
            .map(|index| format!("fast-{index}"))
            .collect::<Vec<_>>();
        let mut released = None;
        for (index, request_id) in ids.iter().enumerate() {
            let submitted_ms = 9_000 + index as u64 * 1_000;
            let sample = InputIntervalSample {
                input_tokens: 400,
                ..usage(request_id, interval(start, submitted_ms))
            };
            let rate = window.observe(sample, &config);
            if submitted_ms - 8_000 <= lag_ms {
                assert!(
                    rate.is_none(),
                    "published before the lag bound at {submitted_ms} ms"
                );
            } else {
                released = rate;
            }
        }
        assert_tps(released, 800.0, true);
        assert!(retained(&window).iter().all(|(id, _)| *id != "stuck"));

        assert!(
            window
                .observe(usage("stuck", interval(start, 8_000)), &config)
                .is_none(),
            "usage for a dropped request must be ignored"
        );
        assert!(retained(&window).iter().all(|(id, _)| *id != "stuck"));
    }

    #[test]
    fn high_concurrency_long_decodes_publish_only_uncached_rates() {
        let config = config(8);
        let start = Instant::now();
        let ids = (0..600)
            .map(|index| format!("req-{index}"))
            .collect::<Vec<_>>();
        // 10 requests per second, 500 ms prefill, 10 s decode: about 100 in flight.
        let mut events = Vec::new();
        for index in 0..600_u64 {
            let first_output_ms = index * 100 + 500;
            events.push((first_output_ms, 1, index as usize));
            events.push((first_output_ms + 10_000, 0, index as usize));
        }
        events.sort_unstable();

        let mut window = RequestInputIntervalWindow::default();
        let steady = 800.0 / 1.2;
        let mut steady_state = false;
        for (_, kind, index) in events {
            let request_interval = interval(start, index as u64 * 100);
            let sample = if kind == 1 {
                placeholder(&ids[index], request_interval)
            } else {
                usage(&ids[index], request_interval)
            };
            if let Some(rate) = window.observe(sample, &config) {
                assert!(
                    rate.max_input_tps_eligible,
                    "published a rate that counts cached tokens"
                );
                assert!((rate.input_tps - 1_000.0).abs() > 1e-3);
            }
            if steady_state {
                assert_tps(current(&window), steady, true);
            } else {
                steady_state =
                    current(&window).is_some_and(|rate| (rate.input_tps - steady).abs() < 1e-6);
            }
        }
        assert!(steady_state);
        assert_tps(window.published, steady, true);
    }

    #[test]
    fn terminal_after_eviction_is_ignored() {
        let config = config(1);
        let start = Instant::now();
        let mut window = RequestInputIntervalWindow::default();
        window.observe(placeholder("a", interval(start, 0)), &config);
        window.observe(usage("a", interval(start, 0)), &config);
        window.observe(live("b", interval(start, 100)), &config);
        assert_eq!(retained(&window), [("b", false)]);

        assert!(
            window
                .observe(usage("a", interval(start, 0)), &config)
                .is_none()
        );
        assert!(
            window
                .observe(placeholder("a", interval(start, 0)), &config)
                .is_none()
        );
        assert_eq!(retained(&window), [("b", false)]);
    }

    #[test]
    fn mixed_live_and_deferred_entries_share_one_ordered_window() {
        let config = config(2);
        let start = Instant::now();
        let mut window = RequestInputIntervalWindow::default();

        assert_tps(
            window.observe(live("l0", interval(start, 0)), &config),
            300.0,
            false,
        );
        window.observe(placeholder("d1", interval(start, 100)), &config);
        assert!(
            window
                .observe(live("l2", interval(start, 200)), &config)
                .is_none(),
            "a live entry newer than a pending entry is held back"
        );

        assert_tps(
            window.observe(usage("d1", interval(start, 100)), &config),
            (UNCACHED + TOTAL) as f64 / 0.6,
            false,
        );
        assert_eq!(retained(&window), [("d1", false), ("l2", false)]);

        assert!(
            window
                .observe(live("l0", interval(start, 0)), &config)
                .is_none(),
            "an evicted live request must not re-enter"
        );
        let downgrade = InputIntervalSample {
            input_tokens: TOTAL,
            max_input_tps_eligible: false,
            ..usage("d1", interval(start, 100))
        };
        assert!(
            window.observe(downgrade, &config).is_none(),
            "usage without cached-token data must not downgrade a resolved entry"
        );
        assert!(window.intervals[0].max_input_tps_eligible);
    }
}

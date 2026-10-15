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

use tokio::time::Instant as TokioInstant;

use crate::generated_request_id::{GeneratedRequestKind, generated_request_kind};
use crate::request_observer::{RequestObservationEndpoint, RequestObservationState};
use crate::runtime_state::ModelGeneration;
use crate::{CurrentModelStats, RequestObservation, RequestObservationEvent};

use super::aggregator::{
    EmbeddingThroughputSample, InputThroughputSample, KvCacheStatsSnapshot, ModelMetricsState,
    ModelStatsSnapshotInputs, StatsAggregator, apply_input_throughput_sample, current_unix_millis,
    output_decode_duration, push_sample, tps_for_units,
};
use super::collector::{
    FinalizeRequestUpdate, RequestCounterUpdate, StatsAggregatorUpdate, StatsCollectorConfig,
    StatsUpdateSource,
};

impl StatsAggregator {
    pub(super) fn apply_fallback_observation(
        &mut self,
        event: &RequestObservationEvent,
    ) -> Vec<super::aggregator::ModelStatsUpdate> {
        let observation = &event.observation;
        let mut changed_models = self.record_fallback_observation(event);
        let mut counter_updates = Vec::new();
        if let Some(update) =
            fallback_update_from_observation(observation, event.generation.clone())
        {
            self.apply_update_into(update, &mut counter_updates);
        }
        for (generation, _) in counter_updates {
            push_changed_model(&mut changed_models, generation.model_id().to_string());
        }
        self.snapshots(changed_models)
    }

    pub(super) fn apply_stream_observation(
        &mut self,
        event: &RequestObservationEvent,
    ) -> Vec<super::aggregator::ModelStatsUpdate> {
        if generated_request_kind(&event.observation.request_id)
            == Some(GeneratedRequestKind::Calibration)
        {
            return self.apply_fallback_observation(event);
        }
        let observation = &event.observation;
        self.remember_stream_request_owner(event);
        let mut changed_models = self.record_lifecycle_event(event);
        if self.record_stream_embedding_sample(event) {
            push_changed_model(&mut changed_models, observation.model_id.clone());
        }
        self.snapshots(changed_models)
    }

    fn remember_stream_request_owner(&mut self, event: &RequestObservationEvent) {
        if let Some(generation) = event.generation.as_ref() {
            self.remember_request_owner(&event.observation.request_id, generation);
        }
    }

    fn record_stream_embedding_sample(&mut self, event: &RequestObservationEvent) -> bool {
        let observation = &event.observation;
        event.generation.as_ref() == self.current_generation(&observation.model_id)
            && observation.endpoint == RequestObservationEndpoint::Embeddings
            && observation.state == RequestObservationState::Complete
            && observation.embedding_items_observed
            && observation
                .time_to_response_headers
                .is_some_and(|response_headers| {
                    self.record_engine_embedding_sample(
                        &observation.model_id,
                        EmbeddingThroughputSample {
                            items: observation.embedding_items,
                            duration: observation.total_duration.saturating_sub(response_headers),
                        },
                    )
                })
    }

    pub(super) fn apply_kv_cache_stats(
        &mut self,
        kv_cache: KvCacheStatsSnapshot,
    ) -> Option<super::aggregator::ModelStatsUpdate> {
        let model_id = kv_cache.model.clone();
        let model_state = self.per_model.get_mut(&model_id)?;
        model_state.metrics.kv_cache = kv_cache;
        model_state.metrics.kv_cache_stats_observed = true;
        model_state.metrics.stats_observed_at_unix_ms = current_unix_millis();
        let generation = model_state.generation.clone();
        let stats = self.snapshot(&model_id);
        Some((generation, stats))
    }

    pub(super) fn snapshot(&self, model_id: &str) -> CurrentModelStats {
        let queue = self.runtime_state.snapshot_live_model(model_id);
        let inputs = ModelStatsSnapshotInputs {
            active_chat_output_tps: queue.active_chat_output_tps,
            queue_size: queue.queue_size,
            queued_input_size: queue.queued_input_size,
            num_running_queries: queue.num_running_queries,
            total_query_input_size: queue.total_query_input_size,
            input_processing_queries: queue.input_processing_queries,
            output_generation_queries: queue.output_generation_queries,
        };
        let mut stats = self.per_model.get(model_id).map_or_else(
            || ModelMetricsState::default().current_stats(inputs),
            |state| state.metrics.current_stats(inputs),
        );
        stats.queue_time_estimate_ms_by_priority = queue.queue_time_estimate_ms_by_priority;
        stats
    }

    fn record_fallback_observation(&mut self, event: &RequestObservationEvent) -> Vec<String> {
        let observation = &event.observation;
        if event.generation.as_ref() != self.current_generation(&observation.model_id) {
            return Vec::new();
        }
        let counter_already_observed =
            observation.output_tokens_explicit && self.has_request_counter(&observation.request_id);
        let mut changed_models = self.record_lifecycle_event(event);
        let input_sample = (observation.state == RequestObservationState::Complete)
            .then(|| match observation.endpoint {
                RequestObservationEndpoint::ChatCompletions
                | RequestObservationEndpoint::Responses => observation
                    .time_to_first_output
                    .map(|duration| (duration, false)),
                RequestObservationEndpoint::Embeddings => Some((
                    observation
                        .time_to_response_headers
                        .unwrap_or(observation.total_duration),
                    true,
                )),
            })
            .flatten()
            .map(
                |(duration, clamp_duration_to_floor)| InputThroughputSample {
                    units: observation.input_tokens,
                    duration,
                    clamp_duration_to_floor,
                },
            );
        let Some(generation_state) = self.per_model.get_mut(&observation.model_id) else {
            return changed_models;
        };
        let pinned_input_tps = generation_state.pinned_input_tps;
        let model_state = &mut generation_state.metrics;
        model_state.chunk_usage_stats_observed |= observation.output_tokens_from_chunk_usage;
        let record_sample = |samples, sum: &mut f64, max: &mut f64, sample| {
            *max = max.max(sample);
            push_sample(samples, sum, sample, self.config.smoothing_window_size);
        };
        if observation.state == RequestObservationState::Complete {
            match observation.endpoint {
                RequestObservationEndpoint::ChatCompletions
                | RequestObservationEndpoint::Responses => {
                    if !counter_already_observed
                        && let Some(output_tps) = observed_output_tps(&self.config, observation)
                    {
                        record_sample(
                            &mut model_state.chat_output_tps_samples,
                            &mut model_state.chat_output_tps_sum,
                            &mut model_state.max_chat_output_tps,
                            output_tps,
                        );
                    }
                }
                RequestObservationEndpoint::Embeddings => {
                    if let Some(response_headers) = observation.time_to_response_headers
                        && let Some(embedding_item_tps) = tps_for_units(
                            observation.embedding_items,
                            observation
                                .total_duration
                                .saturating_sub(response_headers)
                                .max(self.config.duration_floor),
                            self.config.duration_floor,
                        )
                    {
                        record_sample(
                            &mut model_state.embedding_item_tps_samples,
                            &mut model_state.embedding_item_tps_sum,
                            &mut model_state.max_embedding_item_tps,
                            embedding_item_tps,
                        );
                    }
                }
            }
        }
        if input_sample.is_some_and(|sample| {
            apply_input_throughput_sample(&self.config, model_state, pinned_input_tps, sample)
        }) {
            push_changed_model(&mut changed_models, observation.model_id.clone());
        }
        changed_models
    }

    fn record_lifecycle_event(&mut self, event: &RequestObservationEvent) -> Vec<String> {
        let observation = &event.observation;
        let active_chat_output_tps = self
            .config
            .openai_fallback_stats_enabled
            .then(|| observed_output_tps(&self.config, observation))
            .flatten();
        let mut changed_models = event
            .changed_generations
            .iter()
            .filter(|generation| {
                self.current_generation(generation.model_id()) == Some(*generation)
            })
            .map(|generation| generation.model_id().to_string())
            .collect::<Vec<_>>();
        if event.generation.as_ref() != self.current_generation(&observation.model_id) {
            return changed_models;
        }
        if let Some(model_id) = self
            .runtime_state
            .update_request_active_output_tps(&observation.request_id, active_chat_output_tps)
        {
            push_changed_model(&mut changed_models, model_id);
        }
        if let Some(model_state) = self.per_model.get_mut(&observation.model_id) {
            model_state.metrics.stats_observed_at_unix_ms = current_unix_millis();
        }
        changed_models
    }

    fn snapshots(&self, model_ids: Vec<String>) -> Vec<super::aggregator::ModelStatsUpdate> {
        model_ids
            .into_iter()
            .map(|model_id| {
                let stats = self.snapshot(&model_id);
                let generation = self
                    .current_generation(&model_id)
                    .cloned()
                    .expect("changed model should still have a current generation");
                (generation, stats)
            })
            .collect()
    }
}

pub(super) fn fallback_update_from_observation(
    observation: &RequestObservation,
    generation: Option<ModelGeneration>,
) -> Option<StatsAggregatorUpdate> {
    let observed_at = TokioInstant::now();
    if observation.output_tokens_explicit {
        return Some(StatsAggregatorUpdate::RequestCounters(
            RequestCounterUpdate {
                source: StatsUpdateSource::OpenAiFallback,
                request_id: observation.request_id.clone(),
                model_id: observation.model_id.clone(),
                generation,
                tokens_processed: None,
                tokens_generated: Some(observation.output_tokens),
                finished: observation.is_terminal(),
                observed_at,
            },
        ));
    }
    observation.is_terminal().then(|| {
        StatsAggregatorUpdate::FinalizeRequest(FinalizeRequestUpdate {
            source: StatsUpdateSource::OpenAiFallback,
            request_id: observation.request_id.clone(),
            generation,
            observed_at,
        })
    })
}

fn push_changed_model(models: &mut Vec<String>, model_id: String) {
    if !models.contains(&model_id) {
        models.push(model_id);
    }
}

pub(super) fn observed_output_tps(
    config: &StatsCollectorConfig,
    observation: &RequestObservation,
) -> Option<f64> {
    if observation.endpoint == RequestObservationEndpoint::Embeddings
        || observation.output_tokens < config.min_output_tokens
    {
        return None;
    }
    tps_for_units(
        observation.output_tokens,
        output_decode_duration(
            observation.total_duration,
            observation.time_to_first_output,
            observation.time_to_first_token,
            config.duration_floor,
        )?,
        config.duration_floor,
    )
}

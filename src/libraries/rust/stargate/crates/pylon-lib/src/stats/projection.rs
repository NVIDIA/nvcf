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
use std::time::Duration;

use tokio::time::Instant as TokioInstant;

use crate::request_observer::{RequestObservationEndpoint, RequestObservationState};
use crate::{CurrentModelStats, RequestObservation};

use super::aggregator::{
    InFlightRequestState, ModelMetricsState, ModelStatsSnapshotInputs, SharedStatsAggregator,
    apply_fixed_last_mean_input_tps, average_slice, current_unix_millis, output_decode_duration,
    push_sample, tps_for_units,
};
use super::collector::{
    FinalizeRequestUpdate, RequestCounterUpdate, RequestCounterUpdateInput,
    RequestObservationStatsUpdate, StatsAggregatorUpdate, StatsCollectorConfig, StatsUpdateSource,
};

#[derive(Debug, Clone, Copy)]
pub(super) struct MeanInputDirectInputSample {
    pub(super) input_tokens: u64,
    pub(super) duration: Duration,
    pub(super) clamp_duration_to_floor: bool,
}

pub(super) fn record_in_flight_observation(
    in_flight: &mut HashMap<String, InFlightRequestState>,
    observation: &RequestObservation,
) -> Vec<String> {
    let prior_state = in_flight.remove(&observation.request_id);
    let prior_model_id = prior_state.as_ref().map(|state| state.model_id.clone());

    if !observation.is_terminal() {
        in_flight.insert(
            observation.request_id.clone(),
            InFlightRequestState {
                endpoint: observation.endpoint,
                model_id: observation.model_id.clone(),
                output_tokens: observation.output_tokens,
                time_to_first_output: observation.time_to_first_output,
                time_to_first_token: observation.time_to_first_token,
                total_duration: observation.total_duration,
            },
        );
    }

    let mut changed_models = vec![observation.model_id.clone()];
    if let Some(prior_model_id) = prior_model_id
        && prior_model_id != observation.model_id
    {
        changed_models.push(prior_model_id);
    }
    changed_models.sort();
    changed_models.dedup();
    changed_models
}

pub(super) fn record_observation(
    config: &StatsCollectorConfig,
    per_model: &mut HashMap<String, ModelMetricsState>,
    in_flight: &mut HashMap<String, InFlightRequestState>,
    observation: &RequestObservation,
) -> Vec<(String, CurrentModelStats)> {
    config.queue_tracker.record_observation(observation);
    let changed_models = record_in_flight_observation(in_flight, observation);

    let model_state = per_model.entry(observation.model_id.clone()).or_default();
    model_state.stats_observed_at_unix_ms = current_unix_millis();
    if observation.output_tokens_from_chunk_usage {
        model_state.chunk_usage_stats_observed = true;
    }
    if observation.state == RequestObservationState::Complete {
        let output_tps = observed_output_tps(config, observation);
        let embedding_item_tps = observed_embeddings_item_tps(config, observation);

        match observation.endpoint {
            RequestObservationEndpoint::ChatCompletions | RequestObservationEndpoint::Responses => {
                if let Some(output_tps) = output_tps {
                    if output_tps > model_state.max_chat_output_tps {
                        model_state.max_chat_output_tps = output_tps;
                    }
                    push_sample(
                        &mut model_state.chat_output_tps_samples,
                        &mut model_state.chat_output_tps_sum,
                        output_tps,
                        config.smoothing_window_size,
                    );
                }
            }
            RequestObservationEndpoint::Embeddings => {
                if let Some(embedding_item_tps) = embedding_item_tps {
                    if embedding_item_tps > model_state.max_embedding_item_tps {
                        model_state.max_embedding_item_tps = embedding_item_tps;
                    }
                    push_sample(
                        &mut model_state.embedding_item_tps_samples,
                        &mut model_state.embedding_item_tps_sum,
                        embedding_item_tps,
                        config.smoothing_window_size,
                    );
                }
            }
        }
    }

    changed_models
        .into_iter()
        .map(|model_id| {
            let stats = snapshot_model_stats(config, per_model, in_flight, &model_id);
            (model_id, stats)
        })
        .collect()
}

pub(super) fn record_lifecycle_observation(
    config: &StatsCollectorConfig,
    per_model: &mut HashMap<String, ModelMetricsState>,
    in_flight: &mut HashMap<String, InFlightRequestState>,
    observation: &RequestObservation,
) -> Vec<String> {
    config.queue_tracker.record_observation(observation);
    let changed_models = record_in_flight_observation(in_flight, observation);
    let model_state = per_model.entry(observation.model_id.clone()).or_default();
    model_state.stats_observed_at_unix_ms = current_unix_millis();
    changed_models
}

pub(super) fn shared_snapshots_with_lifecycle_load(
    config: &StatsCollectorConfig,
    per_model: &mut HashMap<String, ModelMetricsState>,
    in_flight: &HashMap<String, InFlightRequestState>,
    shared_aggregator: &SharedStatsAggregator,
    model_ids: Vec<String>,
) -> Vec<(String, CurrentModelStats)> {
    model_ids
        .into_iter()
        .map(|model_id| {
            let mut stats = shared_aggregator.snapshot(&model_id);
            attach_model_lifecycle_load(config, per_model, in_flight, &model_id, &mut stats);
            (model_id, stats)
        })
        .collect()
}

pub(super) fn attach_lifecycle_load(
    config: &StatsCollectorConfig,
    per_model: &mut HashMap<String, ModelMetricsState>,
    in_flight: &HashMap<String, InFlightRequestState>,
    updates: &mut [(String, CurrentModelStats)],
) {
    for (model_id, stats) in updates {
        attach_model_lifecycle_load(config, per_model, in_flight, model_id, stats);
    }
}

pub(super) fn attach_model_lifecycle_load(
    config: &StatsCollectorConfig,
    per_model: &mut HashMap<String, ModelMetricsState>,
    in_flight: &HashMap<String, InFlightRequestState>,
    model_id: &str,
    stats: &mut CurrentModelStats,
) {
    let lifecycle_stats = snapshot_model_stats(config, per_model, in_flight, model_id);
    stats.queue_size = lifecycle_stats.queue_size;
    stats.queued_input_size = lifecycle_stats.queued_input_size;
    stats.num_running_queries = lifecycle_stats.num_running_queries;
    stats.total_query_input_size = lifecycle_stats.total_query_input_size;
    stats.input_processing_queries = lifecycle_stats.input_processing_queries;
    stats.output_generation_queries = lifecycle_stats.output_generation_queries;
    stats.queue_time_estimate_ms_by_priority =
        lifecycle_stats.queue_time_estimate_ms_by_priority.clone();
    if has_any_current_model_kv_metrics(&lifecycle_stats) {
        stats.kv_cache_capacity_tokens = lifecycle_stats.kv_cache_capacity_tokens;
        stats.kv_cache_used_tokens = lifecycle_stats.kv_cache_used_tokens;
        stats.kv_cache_free_tokens = lifecycle_stats.kv_cache_free_tokens;
    }
    merge_label_if_observed(
        &mut stats.stats_capabilities,
        &lifecycle_stats.stats_capabilities,
        "machine.kv_cache.http",
    );
    merge_label_if_observed(
        &mut stats.stats_sources,
        &lifecycle_stats.stats_sources,
        "kv_cache_stats",
    );
    if stats.stats_observed_at_unix_ms == 0 {
        stats.stats_observed_at_unix_ms = lifecycle_stats.stats_observed_at_unix_ms;
    }
}

fn has_any_current_model_kv_metrics(stats: &CurrentModelStats) -> bool {
    stats.kv_cache_capacity_tokens > 0
        || stats.kv_cache_used_tokens > 0
        || stats.kv_cache_free_tokens > 0
}

fn merge_label_if_observed(labels: &mut Vec<String>, observed_labels: &[String], label: &str) {
    if observed_labels.iter().any(|observed| observed == label)
        && !labels.iter().any(|existing| existing == label)
    {
        labels.push(label.to_string());
    }
}

pub(super) fn fallback_updates_from_observation(
    observation: &RequestObservation,
) -> Vec<StatsAggregatorUpdate> {
    let observed_at = TokioInstant::now();
    let tokens_generated = observation
        .output_tokens_explicit
        .then_some(observation.output_tokens);
    if tokens_generated.is_some() {
        return vec![StatsAggregatorUpdate::RequestCounters(
            RequestCounterUpdate::new(RequestCounterUpdateInput {
                source: StatsUpdateSource::OpenAiFallback,
                request_id: observation.request_id.clone(),
                model_id: observation.model_id.clone(),
                tokens_processed: None,
                tokens_generated,
                finished: observation.is_terminal(),
                observed_at,
            }),
        )];
    }
    if observation.is_terminal() {
        return vec![StatsAggregatorUpdate::FinalizeRequest(
            FinalizeRequestUpdate::new(
                StatsUpdateSource::OpenAiFallback,
                observation.request_id.clone(),
                observed_at,
            ),
        )];
    }
    Vec::new()
}

pub(super) fn stream_mode_observation_updates_from_observation(
    observation: &RequestObservation,
) -> Vec<StatsAggregatorUpdate> {
    if observation.endpoint != RequestObservationEndpoint::Embeddings
        || observation.state != RequestObservationState::Complete
    {
        return Vec::new();
    }

    let embedding_duration = observation
        .time_to_response_headers
        .map(|response_headers| observation.total_duration.saturating_sub(response_headers));

    if !observation.embedding_items_observed || embedding_duration.is_none() {
        return Vec::new();
    }

    vec![StatsAggregatorUpdate::RequestObservation(
        RequestObservationStatsUpdate {
            model_id: observation.model_id.clone(),
            input_tokens: None,
            input_duration: None,
            clamp_input_duration_to_floor: false,
            embedding_items: observation
                .embedding_items_observed
                .then_some(observation.embedding_items),
            embedding_duration,
        },
    )]
}

pub(super) fn snapshot_model_stats(
    config: &StatsCollectorConfig,
    per_model: &mut HashMap<String, ModelMetricsState>,
    in_flight: &HashMap<String, InFlightRequestState>,
    model_id: &str,
) -> CurrentModelStats {
    let queue_snapshot = config.queue_tracker.snapshot_model(model_id);
    let mut active_chat_output_tps_samples = Vec::new();

    for state in in_flight
        .values()
        .filter(|state| state.model_id == model_id)
    {
        if matches!(
            state.endpoint,
            RequestObservationEndpoint::ChatCompletions | RequestObservationEndpoint::Responses
        ) && let Some(output_duration) = output_decode_duration(
            state.total_duration,
            state.time_to_first_output,
            state.time_to_first_token,
            config.duration_floor,
        ) && let Some(output_tps) =
            tps_for_units(state.output_tokens, output_duration, config.duration_floor)
        {
            active_chat_output_tps_samples.push(output_tps);
        }
    }

    let model_state = per_model.entry(model_id.to_string()).or_default();
    let mut stats = model_state.current_stats(ModelStatsSnapshotInputs {
        active_chat_output_tps: average_slice(&active_chat_output_tps_samples),
        queue_size: queue_snapshot.queue_size,
        queued_input_size: queue_snapshot.queued_input_size,
        num_running_queries: queue_snapshot.num_running_queries,
        total_query_input_size: queue_snapshot.total_query_input_size,
        input_processing_queries: queue_snapshot.input_processing_queries,
        output_generation_queries: queue_snapshot.output_generation_queries,
    });
    stats.queue_time_estimate_ms_by_priority = queue_snapshot.queue_time_estimate_ms_by_priority;
    apply_fixed_last_mean_input_tps(config, &mut stats);
    stats
}

pub(super) fn observed_direct_mean_input_sample(
    observation: &RequestObservation,
) -> Option<MeanInputDirectInputSample> {
    if observation.state == RequestObservationState::Complete {
        let (input_tokens, duration) = match observation.endpoint {
            RequestObservationEndpoint::ChatCompletions | RequestObservationEndpoint::Responses => {
                (observation.input_tokens, observation.time_to_first_output?)
            }
            RequestObservationEndpoint::Embeddings => {
                let duration = observation
                    .time_to_response_headers
                    .unwrap_or(observation.total_duration);
                (observation.input_tokens, duration)
            }
        };
        return Some(MeanInputDirectInputSample {
            input_tokens,
            duration,
            clamp_duration_to_floor: observation.endpoint == RequestObservationEndpoint::Embeddings,
        });
    }
    None
}

pub(super) fn observed_output_tps(
    config: &StatsCollectorConfig,
    observation: &RequestObservation,
) -> Option<f64> {
    if observation.endpoint == RequestObservationEndpoint::Embeddings {
        return None;
    }
    if observation.output_tokens < config.min_output_tokens {
        return None;
    }
    let duration = output_decode_duration(
        observation.total_duration,
        observation.time_to_first_output,
        observation.time_to_first_token,
        config.duration_floor,
    )?;
    tps_for_units(observation.output_tokens, duration, config.duration_floor)
}

pub(super) fn observed_embeddings_item_tps(
    config: &StatsCollectorConfig,
    observation: &RequestObservation,
) -> Option<f64> {
    if observation.endpoint != RequestObservationEndpoint::Embeddings {
        return None;
    }
    let response_headers_duration = observation.time_to_response_headers?;
    let relay_duration = observation
        .total_duration
        .saturating_sub(response_headers_duration);
    let duration = relay_duration.max(config.duration_floor);
    tps_for_units(observation.embedding_items, duration, config.duration_floor)
}

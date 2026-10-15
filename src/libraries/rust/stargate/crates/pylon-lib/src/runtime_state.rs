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

use parking_lot::Mutex;
use stargate_proto::pb::{InferenceServerModelRegistration, InferenceServerStatus, ModelStats};

use crate::queue_admission::{
    LiveRequestState, PylonQueueMismatchRetryConfig, QueueAdmissionDecision, QueueModelSnapshot,
    QueueTrackedRequestGuard,
};
use crate::request_observer::{RequestObservation, RequiredTunnelHeaders};
use crate::stats::PylonMetrics;
use reqwest::header::HeaderMap;

#[derive(Debug, Clone, Default, PartialEq)]
pub struct CurrentModelStats {
    // Sticky runtime-observed mean input TPS for this backend.
    pub last_mean_input_tps: f64,
    // Token/sec output rate for streaming generation endpoints. Embeddings item
    // cardinality is observed separately and is not exported through this field.
    pub output_tps: f64,
    pub embedding_item_tps: f64,
    pub queue_size: u64,
    pub queued_input_size: u64,

    // Cluster-scoped shared-hardware/scheduler state. Stargate currently keeps
    // the latest active backend snapshot for these fields when multiple
    // backends share a cluster.
    // Same token/sec unit as `output_tps`; embeddings item rates are not folded in.
    pub max_output_tps: f64,
    pub max_embedding_item_tps: f64,
    pub kv_cache_capacity_tokens: u64,
    pub kv_cache_used_tokens: u64,
    pub kv_cache_free_tokens: u64,
    pub num_running_queries: u64,
    pub max_engine_concurrency: Option<u64>,
    pub total_query_input_size: u64,
    pub queue_time_estimate_ms_by_priority: Option<HashMap<u32, u64>>,
    pub input_processing_queries: u64,
    pub output_generation_queries: u64,
    pub stats_observed_at_unix_ms: u64,
    pub stats_capabilities: Vec<String>,
    pub stats_sources: Vec<String>,
}

#[derive(Clone, Debug, Eq, Hash, Ord, PartialEq, PartialOrd)]
pub(crate) struct ModelGeneration {
    model_id: String,
    sequence: u64,
}

impl ModelGeneration {
    pub(crate) fn new(model_id: impl Into<String>, sequence: u64) -> Self {
        Self {
            model_id: model_id.into(),
            sequence,
        }
    }

    pub(crate) fn model_id(&self) -> &str {
        &self.model_id
    }

    pub(crate) fn sequence(&self) -> u64 {
        self.sequence
    }
}

#[derive(Clone, Debug, Default)]
pub struct PylonRuntimeState {
    advertised: Arc<Mutex<AdvertisedRuntimeState>>,
    live_requests: LiveRequestState,
    metrics: Option<Arc<PylonMetrics>>,
    observation_tx: Option<flume::Sender<RequestObservationEvent>>,
}

#[derive(Clone, Debug)]
pub struct RequestObservationEvent {
    pub(crate) observation: RequestObservation,
    pub(crate) generation: Option<ModelGeneration>,
    pub(crate) changed_generations: Vec<ModelGeneration>,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum RequestGenerationAdmission {
    Admitted(ModelGeneration),
    Ungated,
    Unavailable,
}

#[derive(Debug, Default)]
struct AdvertisedRuntimeState {
    base_status: InferenceServerStatus,
    require_admitted_generation: bool,
    models: HashMap<String, RuntimeModelState>,
}

impl AdvertisedRuntimeState {
    fn current(&self, generation: &ModelGeneration) -> Option<&RuntimeModelState> {
        self.models
            .get(generation.model_id())
            .filter(|model| model.generation == generation.sequence)
    }

    fn current_mut(&mut self, generation: &ModelGeneration) -> Option<&mut RuntimeModelState> {
        self.models
            .get_mut(generation.model_id())
            .filter(|model| model.generation == generation.sequence)
    }
}

#[derive(Debug, Default)]
struct RuntimeModelState {
    generation: u64,
    stats: CurrentModelStats,
    publication: ModelPublication,
}

#[derive(Debug, Default)]
enum ModelPublication {
    #[default]
    Pending,
    Admitted {
        bringup_ready: bool,
    },
}

impl PylonRuntimeState {
    /// Creates a static runtime snapshot whose initial models are already
    /// admitted. Lifecycle-managed callers should start empty and use
    /// `begin_generation` so initialization controls publication.
    pub fn new(initial_status: InferenceServerStatus, model_ids: &[String]) -> Self {
        let models = model_ids
            .iter()
            .enumerate()
            .map(|(index, model_id)| {
                (
                    model_id.clone(),
                    RuntimeModelState {
                        generation: u64::try_from(index).expect("model index should fit u64"),
                        publication: ModelPublication::Admitted {
                            bringup_ready: true,
                        },
                        ..RuntimeModelState::default()
                    },
                )
            })
            .collect();
        Self {
            advertised: Arc::new(Mutex::new(AdvertisedRuntimeState {
                base_status: initial_status,
                require_admitted_generation: true,
                models,
            })),
            live_requests: LiveRequestState::default(),
            metrics: None,
            observation_tx: None,
        }
    }

    pub fn observed(
        initial_status: InferenceServerStatus,
        model_ids: &[String],
        observation_capacity: usize,
        metrics: Option<Arc<PylonMetrics>>,
    ) -> (Self, flume::Receiver<RequestObservationEvent>) {
        let (observation_tx, observation_rx) = flume::bounded(observation_capacity);
        let mut state = Self::new(initial_status, model_ids);
        state.metrics = metrics;
        state.observation_tx = Some(observation_tx);
        (state, observation_rx)
    }

    pub fn set_status(&self, status: InferenceServerStatus) {
        self.advertised.lock().base_status = status;
    }

    pub(crate) fn model_ids(&self) -> Vec<String> {
        let mut model_ids = self
            .advertised
            .lock()
            .models
            .keys()
            .cloned()
            .collect::<Vec<_>>();
        model_ids.sort_unstable();
        model_ids
    }

    pub(crate) fn begin_generation(&self, generation: ModelGeneration) -> bool {
        let mut advertised = self.advertised.lock();
        advertised.require_admitted_generation = true;
        if advertised.models.contains_key(generation.model_id()) {
            return false;
        }
        advertised.models.insert(
            generation.model_id.clone(),
            RuntimeModelState {
                generation: generation.sequence,
                ..RuntimeModelState::default()
            },
        );
        true
    }

    pub(crate) fn current_generation(&self, model_id: &str) -> Option<ModelGeneration> {
        self.advertised
            .lock()
            .models
            .get(model_id)
            .map(|model| ModelGeneration::new(model_id, model.generation))
    }

    pub(crate) fn request_generation_admission(
        &self,
        model_id: &str,
    ) -> RequestGenerationAdmission {
        let advertised = self.advertised.lock();
        match advertised.models.get(model_id) {
            Some(model) if matches!(model.publication, ModelPublication::Admitted { .. }) => {
                RequestGenerationAdmission::Admitted(ModelGeneration::new(
                    model_id,
                    model.generation,
                ))
            }
            Some(_) => RequestGenerationAdmission::Unavailable,
            None if advertised.require_admitted_generation => {
                RequestGenerationAdmission::Unavailable
            }
            None => RequestGenerationAdmission::Ungated,
        }
    }

    pub(crate) fn publish_generation(&self, generation: &ModelGeneration) -> bool {
        let mut advertised = self.advertised.lock();
        let Some(model) = advertised.current_mut(generation) else {
            return false;
        };
        model.publication = ModelPublication::Admitted {
            bringup_ready: true,
        };
        true
    }

    pub(crate) fn generation_is_published(&self, generation: &ModelGeneration) -> bool {
        self.advertised
            .lock()
            .current(generation)
            .is_some_and(|model| matches!(model.publication, ModelPublication::Admitted { .. }))
    }

    pub(crate) fn retire_generation(
        &self,
        generation: &ModelGeneration,
    ) -> Option<CurrentModelStats> {
        let mut advertised = self.advertised.lock();
        advertised.current(generation)?;
        let retired = advertised
            .models
            .remove(generation.model_id())
            .expect("validated generation should still exist");
        self.live_requests.retire_generation(generation);
        Some(retired.stats)
    }

    pub(crate) fn set_generation_stats(
        &self,
        generation: &ModelGeneration,
        stats: CurrentModelStats,
    ) -> bool {
        let observed_stats = {
            let mut advertised = self.advertised.lock();
            let Some(model) = advertised.current_mut(generation) else {
                return false;
            };
            self.live_requests
                .update_generation_throughput(generation, stats.last_mean_input_tps);
            model.stats = stats;
            model.stats.clone()
        };
        if let Some(metrics) = &self.metrics {
            metrics.observe_model_stats(generation.model_id(), &observed_stats);
        }
        true
    }

    pub fn set_model_stats(&self, model_id: impl Into<String>, stats: CurrentModelStats) {
        let model_id = model_id.into();
        let Some(generation) = self.current_generation(&model_id) else {
            return;
        };
        self.set_generation_stats(&generation, stats);
    }

    pub fn model_stats(&self, model_id: &str) -> Option<CurrentModelStats> {
        self.advertised
            .lock()
            .models
            .get(model_id)
            .map(|model| model.stats.clone())
    }

    #[cfg(test)]
    pub(crate) fn set_model_bringup_ready(&self, model_id: impl Into<String>, ready: bool) {
        let mut advertised = self.advertised.lock();
        if let Some(model) = advertised.models.get_mut(&model_id.into()) {
            model.publication = ModelPublication::Admitted {
                bringup_ready: ready,
            };
        }
    }

    pub(crate) fn set_generation_bringup_ready(
        &self,
        generation: &ModelGeneration,
        ready: bool,
    ) -> bool {
        let mut advertised = self.advertised.lock();
        let Some(model) = advertised.current_mut(generation) else {
            return false;
        };
        let ModelPublication::Admitted { bringup_ready } = &mut model.publication else {
            return false;
        };
        *bringup_ready = ready;
        true
    }

    #[cfg(test)]
    pub(crate) fn model_bringup_ready(&self, model_id: &str) -> Option<bool> {
        self.advertised
            .lock()
            .models
            .get(model_id)
            .map(|model| match model.publication {
                ModelPublication::Pending => false,
                ModelPublication::Admitted { bringup_ready } => bringup_ready,
            })
    }

    pub(crate) fn advertised_models(&self) -> HashMap<String, InferenceServerModelRegistration> {
        let advertised = self.advertised.lock();
        advertised
            .models
            .iter()
            .filter_map(|(model_id, model)| {
                let ModelPublication::Admitted { bringup_ready } = model.publication else {
                    return None;
                };
                let stats = &model.stats;
                let registration = InferenceServerModelRegistration {
                    stats: Some(ModelStats {
                        last_mean_input_tps: stats.last_mean_input_tps,
                        output_tps: stats.output_tps,
                        max_output_tps: stats.max_output_tps,
                        queue_size: stats.queue_size,
                        queued_input_size: stats.queued_input_size,
                        kv_cache_capacity_tokens: stats.kv_cache_capacity_tokens,
                        kv_cache_used_tokens: stats.kv_cache_used_tokens,
                        kv_cache_free_tokens: stats.kv_cache_free_tokens,
                        num_running_queries: stats.num_running_queries,
                        max_engine_concurrency: stats.max_engine_concurrency.unwrap_or_default(),
                        total_query_input_size: stats.total_query_input_size,
                        queue_time_estimate_ms_by_priority: stats
                            .queue_time_estimate_ms_by_priority
                            .clone()
                            .unwrap_or_default(),
                        input_processing_queries: stats.input_processing_queries,
                        output_generation_queries: stats.output_generation_queries,
                        stats_observed_at_unix_ms: stats.stats_observed_at_unix_ms,
                        stats_capabilities: stats.stats_capabilities.clone(),
                        stats_sources: stats.stats_sources.clone(),
                    }),
                    status: gated_model_status(advertised.base_status, bringup_ready).into(),
                };
                Some((model_id.clone(), registration))
            })
            .collect()
    }

    pub fn advertised_model_ids(&self) -> Vec<String> {
        let advertised = self.advertised.lock();
        let mut model_ids = advertised
            .models
            .iter()
            .filter(|(_, model)| matches!(model.publication, ModelPublication::Admitted { .. }))
            .map(|(model_id, _)| model_id.clone())
            .collect::<Vec<_>>();
        model_ids.sort_unstable();
        model_ids
    }

    pub fn observe_request(&self, observation: RequestObservation) {
        let generation = self.current_generation(&observation.model_id);
        self.observe_request_for_generation(observation, generation);
    }

    pub(crate) fn observe_request_for_generation(
        &self,
        observation: RequestObservation,
        generation: Option<ModelGeneration>,
    ) {
        let event = self.transition_request_observation_for_generation(observation, generation);
        if let Some(tx) = &self.observation_tx {
            let request_id = event.observation.request_id.clone();
            if let Err(error) = tx.try_send(event) {
                tracing::warn!(request_id, error = %error, "dropping request observation");
            }
        }
    }

    #[cfg(test)]
    pub(crate) fn update_model_throughput(&self, model_id: &str, last_mean_input_tps: f64) {
        let generation = self
            .current_generation(model_id)
            .expect("test model generation should already exist");
        self.live_requests
            .update_generation_throughput(&generation, last_mean_input_tps);
    }

    pub(crate) fn metrics(&self) -> Option<&PylonMetrics> {
        self.metrics.as_deref()
    }

    #[cfg(test)]
    pub(crate) fn transition_request_observation(
        &self,
        observation: RequestObservation,
    ) -> RequestObservationEvent {
        let generation = self
            .current_generation(&observation.model_id)
            .expect("test model generation should already exist");
        self.transition_request_observation_for_generation(observation, Some(generation))
    }

    pub(crate) fn transition_request_observation_for_generation(
        &self,
        observation: RequestObservation,
        generation: Option<ModelGeneration>,
    ) -> RequestObservationEvent {
        // Held across the queue transition below: retire_generation() purges
        // live-request state under this lock, so releasing it after the
        // currency check would let a retired generation reinsert queue state.
        let advertised = self.advertised.lock();
        if let Some(owner) = generation.as_ref() {
            let current_generation = advertised
                .models
                .get(owner.model_id())
                .map(|model| model.generation);
            if current_generation != Some(owner.sequence) {
                tracing::debug!(
                    request_id = observation.request_id,
                    model_id = owner.model_id(),
                    observed_generation = owner.sequence(),
                    current_generation = ?current_generation,
                    "dropping request observation from a retired model generation"
                );
                return RequestObservationEvent {
                    observation,
                    generation,
                    changed_generations: Vec::new(),
                };
            }
        }
        let transition = self.live_requests.transition_generation_observation_with(
            &observation,
            generation.as_ref(),
            |transition| {
                if let Some(metrics) = &self.metrics {
                    metrics.observe_request_transition(&observation, transition);
                }
            },
        );
        RequestObservationEvent {
            observation,
            generation,
            changed_generations: transition.changed_generations,
        }
    }

    pub(crate) fn update_request_active_output_tps(
        &self,
        request_id: &str,
        active_chat_output_tps: Option<f64>,
    ) -> Option<String> {
        self.live_requests
            .update_active_output_tps(request_id, active_chat_output_tps)
    }

    pub(crate) fn request_generation(&self, request_id: &str) -> Option<ModelGeneration> {
        self.live_requests.request_generation(request_id)
    }

    pub(crate) fn snapshot_live_model(&self, model_id: &str) -> QueueModelSnapshot {
        self.current_generation(model_id)
            .map_or_else(QueueModelSnapshot::default, |generation| {
                self.live_requests.snapshot_generation(&generation)
            })
    }

    pub(crate) fn evaluate_generation_queue_admission(
        &self,
        config: &PylonQueueMismatchRetryConfig,
        required: &RequiredTunnelHeaders,
        generation: Option<&ModelGeneration>,
        headers: &HeaderMap,
    ) -> QueueAdmissionDecision {
        self.live_requests
            .evaluate_generation(config, required, generation, headers)
    }

    #[cfg(test)]
    pub(crate) fn track_request(
        &self,
        required: &RequiredTunnelHeaders,
    ) -> QueueTrackedRequestGuard {
        let generation = self
            .current_generation(&required.model_id)
            .unwrap_or_else(|| ModelGeneration::new(required.model_id.clone(), u64::MAX));
        self.live_requests
            .track_generation_request(required, generation)
    }

    pub(crate) fn track_generation_request(
        &self,
        required: &RequiredTunnelHeaders,
        generation: Option<&ModelGeneration>,
    ) -> Option<QueueTrackedRequestGuard> {
        let generation = generation?;
        let advertised = self.advertised.lock();
        advertised.current(generation)?;
        Some(
            self.live_requests
                .track_generation_request(required, generation.clone()),
        )
    }

    pub(crate) fn finish_queue_request(&self, request_id: &str) {
        self.live_requests.finish_queue_request(request_id);
    }

    #[cfg(test)]
    pub(crate) fn tracked_request_count(&self) -> usize {
        self.live_requests.tracked_request_count()
    }
}

impl RequestObservationEvent {
    pub fn observation(&self) -> &RequestObservation {
        &self.observation
    }

    pub fn into_observation(self) -> RequestObservation {
        self.observation
    }
}

pub(crate) fn gated_model_status(
    base_status: InferenceServerStatus,
    bringup_ready: bool,
) -> InferenceServerStatus {
    if base_status == InferenceServerStatus::Active && !bringup_ready {
        InferenceServerStatus::Inactive
    } else {
        base_status
    }
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use stargate_proto::pb::InferenceServerStatus;

    use super::{ModelGeneration, PylonRuntimeState, RequestGenerationAdmission};
    use crate::PylonMetrics;
    use crate::request_observer::{
        RequestObservation, RequestObservationEndpoint, RequestObservationState,
    };

    fn observation(
        request_id: &str,
        model_id: &str,
        routing_key: Option<&str>,
    ) -> RequestObservation {
        RequestObservation {
            endpoint: RequestObservationEndpoint::ChatCompletions,
            request_id: request_id.into(),
            routing_key: routing_key.map(Into::into),
            model_id: model_id.into(),
            priority: 0,
            input_tokens: 42,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: None,
            output_messages: 0,
            output_tokens: 0,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
            state: RequestObservationState::UpstreamConnecting,
            time_to_response_headers: None,
            time_to_first_output: None,
            time_to_first_token: None,
            total_duration: Duration::ZERO,
        }
    }

    #[test]
    fn publishing_observation_updates_live_state_and_emits_one_event() {
        let (runtime_state, observation_rx) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &["model-runtime-owner".to_string()],
            4,
            None,
        );
        let mut observation = observation("req-runtime-owner", "model-runtime-owner", None);

        runtime_state.observe_request(observation.clone());

        let emitted = observation_rx.try_recv().expect("observation should emit");
        assert_eq!(emitted.observation().request_id, observation.request_id);
        assert_eq!(emitted.observation().model_id, observation.model_id);
        assert_eq!(emitted.observation().state, observation.state);
        let live = runtime_state.snapshot_live_model("model-runtime-owner");
        assert_eq!(live.queue_size, 1);
        assert_eq!(live.queued_input_size, 42);

        observation.state = RequestObservationState::Complete;
        runtime_state.observe_request(observation);
        assert_eq!(
            runtime_state
                .snapshot_live_model("model-runtime-owner")
                .queue_size,
            0
        );
    }

    #[test]
    fn queue_cleanup_preserves_observed_identity_until_terminal_metrics_transition() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let (runtime_state, _observation_rx) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &["model-a".to_string()],
            4,
            Some(metrics.clone()),
        );
        let mut observation = observation("req-local-rejection", "model-a", Some("rk-a"));

        runtime_state.observe_request(observation.clone());
        runtime_state.finish_queue_request(&observation.request_id);
        observation.state = RequestObservationState::Failed;
        runtime_state.observe_request(observation);

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(
            body.contains(r#"pylon_requests_state{model="model-a",state="upstream_connecting"} 0"#),
            "terminal transition should remove the prior observed state: {body}"
        );
        assert!(
            body.contains(
                r#"pylon_requests_total{model="model-a",routing_key="rk-a",status="failed"} 1"#
            ),
            "terminal transition should record the failed request: {body}"
        );
    }

    #[test]
    fn lifecycle_metrics_do_not_wait_for_stats_channel_capacity() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let (runtime_state, _observation_rx) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &["model-a".to_string()],
            1,
            Some(metrics.clone()),
        );
        let mut observation = observation("req-full-stats-channel", "model-a", Some("rk-a"));

        runtime_state.observe_request(observation.clone());
        observation.state = RequestObservationState::Failed;
        runtime_state.observe_request(observation);

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(
            body.contains(r#"pylon_requests_state{model="model-a",state="upstream_connecting"} 0"#),
            "terminal source transition should clear the prior gauge: {body}"
        );
        assert!(
            body.contains(
                r#"pylon_requests_total{model="model-a",routing_key="rk-a",status="failed"} 1"#
            ),
            "terminal source transition should record metrics before channel send: {body}"
        );
    }

    #[test]
    fn pending_generation_is_omitted_until_exact_publication() {
        let runtime_state = PylonRuntimeState::new(InferenceServerStatus::Active, &[]);
        let generation = ModelGeneration::new("model-a", 1);

        assert!(runtime_state.begin_generation(generation.clone()));
        assert_eq!(
            runtime_state.current_generation("model-a"),
            Some(generation.clone())
        );
        assert_eq!(
            runtime_state.request_generation_admission("model-a"),
            RequestGenerationAdmission::Unavailable
        );
        assert!(runtime_state.advertised_models().is_empty());

        assert!(runtime_state.publish_generation(&generation));
        assert_eq!(
            runtime_state.request_generation_admission("model-a"),
            RequestGenerationAdmission::Admitted(generation)
        );
        assert_eq!(
            runtime_state
                .advertised_models()
                .keys()
                .cloned()
                .collect::<Vec<_>>(),
            ["model-a"]
        );
    }

    #[test]
    fn retired_generation_cannot_publish_or_mutate_replacement() {
        let runtime_state = PylonRuntimeState::new(InferenceServerStatus::Active, &[]);
        let first = ModelGeneration::new("model-a", 1);
        let replacement = ModelGeneration::new("model-a", 2);

        assert!(runtime_state.begin_generation(first.clone()));
        assert!(runtime_state.retire_generation(&first).is_some());
        assert!(runtime_state.begin_generation(replacement.clone()));
        assert!(!runtime_state.publish_generation(&first));
        assert!(!runtime_state.set_generation_stats(
            &first,
            super::CurrentModelStats {
                last_mean_input_tps: 999.0,
                ..Default::default()
            }
        ));
        assert_eq!(
            runtime_state.model_stats("model-a"),
            Some(super::CurrentModelStats::default())
        );
        assert!(runtime_state.advertised_models().is_empty());
    }

    #[test]
    fn retired_request_observations_cannot_mutate_replacement_queue_state() {
        let runtime_state = PylonRuntimeState::new(InferenceServerStatus::Active, &[]);
        let first = ModelGeneration::new("model-a", 1);
        let replacement = ModelGeneration::new("model-a", 2);
        assert!(runtime_state.begin_generation(first.clone()));
        assert!(runtime_state.publish_generation(&first));

        let mut first_observation = observation("req-first", "model-a", None);
        runtime_state.transition_request_observation_for_generation(
            first_observation.clone(),
            Some(first.clone()),
        );
        assert_eq!(runtime_state.snapshot_live_model("model-a").queue_size, 1);

        assert!(runtime_state.retire_generation(&first).is_some());
        assert!(runtime_state.begin_generation(replacement.clone()));
        assert!(runtime_state.publish_generation(&replacement));
        first_observation.state = RequestObservationState::Complete;
        runtime_state.transition_request_observation_for_generation(first_observation, Some(first));

        assert_eq!(
            runtime_state.snapshot_live_model("model-a"),
            crate::queue_admission::QueueModelSnapshot::default()
        );
    }

    #[test]
    fn retired_request_observations_cannot_record_replacement_metrics() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let (runtime_state, _observation_rx) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            4,
            Some(metrics.clone()),
        );
        let first = ModelGeneration::new("model-a", 1);
        let replacement = ModelGeneration::new("model-a", 2);
        assert!(runtime_state.begin_generation(first.clone()));
        assert!(runtime_state.retire_generation(&first).is_some());
        assert!(runtime_state.begin_generation(replacement));

        let mut stale = observation("req-first", "model-a", Some("rk-a"));
        stale.state = RequestObservationState::Failed;
        runtime_state.transition_request_observation_for_generation(stale, Some(first));

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(
            !body.contains(r#"pylon_requests_total{model="model-a""#),
            "retired work must not recreate model request metrics: {body}"
        );
    }

    #[test]
    fn observing_an_unknown_model_never_creates_a_generation() {
        let runtime_state = PylonRuntimeState::new(InferenceServerStatus::Active, &[]);

        runtime_state.observe_request(observation("req-unknown", "model-a", None));

        assert_eq!(runtime_state.current_generation("model-a"), None);
        assert!(runtime_state.advertised_models().is_empty());
        assert_eq!(
            runtime_state.snapshot_live_model("model-a"),
            crate::queue_admission::QueueModelSnapshot::default()
        );
    }
}

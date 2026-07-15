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

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

use parking_lot::Mutex;
use reqwest::header::HeaderMap;
use stargate_protocol::common::{queue_time_delta_ms, valid_last_mean_input_tps};
use stargate_protocol::tunnel_contract::HEADER_STARGATE_EXPECTED_QUEUE_MS;

use crate::request_observer::{RequestObservation, RequestObservationState, RequiredTunnelHeaders};
use crate::runtime_state::ModelGeneration;

pub(crate) const RETRY_REASON_QUEUE_ESTIMATE_MISMATCH: &str = "queue_estimate_mismatch";

#[derive(Debug, Clone)]
pub struct PylonQueueMismatchRetryConfig {
    pub enabled: bool,
    pub min_delta_ms: u64,
    pub tolerance_factor: f64,
    pub retry_after_ms: Option<u64>,
}

impl Default for PylonQueueMismatchRetryConfig {
    fn default() -> Self {
        Self {
            enabled: true,
            min_delta_ms: 25,
            tolerance_factor: 1.25,
            retry_after_ms: None,
        }
    }
}

#[derive(Clone, Debug, Default)]
pub(crate) struct LiveRequestState {
    inner: Arc<Mutex<QueueAdmissionState>>,
    observation_order: Arc<Mutex<()>>,
}

#[derive(Debug, Default)]
struct QueueAdmissionState {
    requests: HashMap<String, LiveRequest>,
    models: HashMap<ModelGeneration, QueueModelState>,
}

#[derive(Debug, Default)]
struct QueueModelState {
    last_mean_input_tps: Option<f64>,
    phase_requests: [usize; 3],
    // Exact totals keep public saturation from making exclusion or removal lossy.
    total_input_tokens: u128,
    observed_input_tokens: [u128; RequestObservationState::Complete as usize],
    active_chat_output_tps_sum: f64,
    active_chat_output_requests: usize,
    prompt_work_by_priority: BTreeMap<u32, u128>,
}

#[derive(Clone, Debug)]
struct TrackedPromptRequest {
    generation: ModelGeneration,
    priority: u32,
    input_tokens: u64,
    phase: TrackedPromptPhase,
    active_chat_output_tps: Option<f64>,
}

#[derive(Clone, Debug)]
enum LiveRequest {
    Queue(TrackedPromptRequest, Option<ObservedRequestState>),
    Observed(ObservedRequestState),
}

impl LiveRequest {
    fn generation(&self) -> &ModelGeneration {
        match self {
            Self::Queue(request, _) => &request.generation,
            Self::Observed(request) => &request.generation,
        }
    }

    fn into_observed(self) -> Option<ObservedRequestState> {
        match self {
            Self::Queue(_, observed) => observed,
            Self::Observed(observed) => Some(observed),
        }
    }

    fn update_active_output_tps(&mut self, value: Option<f64>) -> Option<String> {
        let Self::Queue(queue, _) = self else {
            return None;
        };
        queue.active_chat_output_tps = value.filter(|tps| tps.is_finite() && *tps > 0.0);
        Some(queue.generation.model_id().to_string())
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub(crate) struct ObservedRequestState {
    pub(crate) generation: ModelGeneration,
    pub(crate) state: RequestObservationState,
    pub(crate) input_tokens: u64,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub(crate) struct RequestObservationTransition {
    pub(crate) changed_generations: Vec<ModelGeneration>,
    pub(crate) prior: Option<ObservedRequestState>,
    pub(crate) current: Option<ObservedRequestState>,
    pub(crate) input_token_totals: Vec<ObservedInputTokenTotal>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub(crate) struct ObservedInputTokenTotal {
    pub(crate) generation: ModelGeneration,
    pub(crate) state: RequestObservationState,
    pub(crate) input_tokens: u128,
}

#[derive(Clone, Copy, Debug, Eq, Ord, PartialEq, PartialOrd)]
enum TrackedPromptPhase {
    Pending,
    InputProcessing,
    OutputGeneration,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub(crate) struct QueueModelSnapshot {
    pub active_chat_output_tps: f64,
    pub queue_size: u64,
    pub queued_input_size: u64,
    pub num_running_queries: u64,
    pub total_query_input_size: u64,
    pub input_processing_queries: u64,
    pub output_generation_queries: u64,
    pub queue_time_estimate_ms_by_priority: Option<HashMap<u32, u64>>,
}

#[derive(Debug)]
pub(crate) struct QueueTrackedRequestGuard {
    live_requests: LiveRequestState,
    request_id: String,
    finished: bool,
}

#[derive(Clone, Debug, PartialEq)]
pub(crate) enum QueueAdmissionDecision {
    Accepted {
        expected_ms: u64,
        actual_ms: u64,
        threshold_ms: u64,
    },
    Rejected {
        expected_ms: u64,
        actual_ms: u64,
        threshold_ms: u64,
        retry_after_ms: Option<u64>,
    },
    MissingEstimate,
    UnknownLocalEstimate {
        expected_ms: u64,
    },
    Disabled,
}

impl QueueAdmissionDecision {
    pub(crate) fn result_label(&self) -> &'static str {
        match self {
            Self::Accepted { .. } => "accepted",
            Self::Disabled => "disabled",
            Self::Rejected { .. } => "rejected",
            Self::MissingEstimate => "missing_estimate",
            Self::UnknownLocalEstimate { .. } => "unknown_local_estimate",
        }
    }

    pub(crate) fn expected_ms(&self) -> Option<u64> {
        match self {
            Self::Accepted { expected_ms, .. }
            | Self::Rejected { expected_ms, .. }
            | Self::UnknownLocalEstimate { expected_ms } => Some(*expected_ms),
            _ => None,
        }
    }

    pub(crate) fn actual_ms(&self) -> Option<u64> {
        match self {
            Self::Accepted { actual_ms, .. } | Self::Rejected { actual_ms, .. } => Some(*actual_ms),
            _ => None,
        }
    }

    pub(crate) fn threshold_ms(&self) -> Option<u64> {
        match self {
            Self::Accepted { threshold_ms, .. } | Self::Rejected { threshold_ms, .. } => {
                Some(*threshold_ms)
            }
            _ => None,
        }
    }
}

impl LiveRequestState {
    pub(crate) fn request_generation(&self, request_id: &str) -> Option<ModelGeneration> {
        self.inner
            .lock()
            .requests
            .get(request_id)
            .map(|request| request.generation().clone())
    }

    pub(crate) fn update_generation_throughput(
        &self,
        generation: &ModelGeneration,
        last_mean_input_tps: f64,
    ) {
        let mut state = self.inner.lock();
        if valid_last_mean_input_tps(last_mean_input_tps) {
            state
                .models
                .entry(generation.clone())
                .or_default()
                .last_mean_input_tps = Some(last_mean_input_tps);
        } else if let Some(model) = state.models.get_mut(generation) {
            model.last_mean_input_tps = None;
            state.remove_model_if_unused(generation);
        }
    }

    pub(crate) fn retire_generation(&self, generation: &ModelGeneration) {
        let mut state = self.inner.lock();
        let request_ids = state
            .requests
            .iter()
            .filter(|(_, request)| request.generation() == generation)
            .map(|(request_id, _)| request_id.clone())
            .collect::<Vec<_>>();
        for request_id in request_ids {
            state.remove_request(&request_id);
        }
        state.models.remove(generation);
    }

    #[cfg(test)]
    pub(crate) fn update_model_throughput(&self, model_id: &str, last_mean_input_tps: f64) {
        self.update_generation_throughput(&ModelGeneration::new(model_id, 0), last_mean_input_tps);
    }

    #[cfg(test)]
    pub(crate) fn evaluate(
        &self,
        config: &PylonQueueMismatchRetryConfig,
        required: &RequiredTunnelHeaders,
        headers: &HeaderMap,
    ) -> QueueAdmissionDecision {
        self.evaluate_generation(
            config,
            required,
            Some(&ModelGeneration::new(required.model_id.clone(), 0)),
            headers,
        )
    }

    pub(crate) fn evaluate_generation(
        &self,
        config: &PylonQueueMismatchRetryConfig,
        required: &RequiredTunnelHeaders,
        generation: Option<&ModelGeneration>,
        headers: &HeaderMap,
    ) -> QueueAdmissionDecision {
        if !config.enabled {
            return QueueAdmissionDecision::Disabled;
        }
        let Some(expected_ms) = headers
            .get(HEADER_STARGATE_EXPECTED_QUEUE_MS)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.trim().parse::<u64>().ok())
        else {
            return QueueAdmissionDecision::MissingEstimate;
        };
        let actual_ms = {
            let state = self.inner.lock();
            generation.and_then(|generation| {
                state.models.get(generation).and_then(|model| {
                    let excluded_request = state
                        .requests
                        .get(&required.request_id)
                        .and_then(|request| match request {
                            LiveRequest::Queue(queue, _) => Some(queue),
                            LiveRequest::Observed(_) => None,
                        })
                        .filter(|request| request.generation == *generation);
                    model.queue_estimate_ms_for_priority_excluding(
                        required.priority,
                        excluded_request,
                    )
                })
            })
        };
        let Some(actual_ms) = actual_ms else {
            return QueueAdmissionDecision::UnknownLocalEstimate { expected_ms };
        };
        let threshold_ms = mismatch_threshold_ms(expected_ms, config);
        if actual_ms > threshold_ms {
            QueueAdmissionDecision::Rejected {
                expected_ms,
                actual_ms,
                threshold_ms,
                retry_after_ms: config.retry_after_ms,
            }
        } else {
            QueueAdmissionDecision::Accepted {
                expected_ms,
                actual_ms,
                threshold_ms,
            }
        }
    }

    #[cfg(test)]
    pub(crate) fn track_request(
        &self,
        required: &RequiredTunnelHeaders,
    ) -> QueueTrackedRequestGuard {
        self.track_generation_request(required, ModelGeneration::new(required.model_id.clone(), 0))
    }

    pub(crate) fn track_generation_request(
        &self,
        required: &RequiredTunnelHeaders,
        generation: ModelGeneration,
    ) -> QueueTrackedRequestGuard {
        let request_id = required.request_id.clone();
        let request = TrackedPromptRequest {
            generation,
            priority: required.priority,
            input_tokens: required.input_tokens,
            phase: TrackedPromptPhase::Pending,
            active_chat_output_tps: None,
        };
        {
            let mut state = self.inner.lock();
            let observed = state
                .remove_request(&request_id)
                .and_then(|(_, request)| request.into_observed());
            state.insert_request(request_id.clone(), LiveRequest::Queue(request, observed));
        }
        QueueTrackedRequestGuard {
            live_requests: self.clone(),
            request_id,
            finished: false,
        }
    }

    #[cfg(test)]
    pub(crate) fn snapshot_model(&self, model_id: &str) -> QueueModelSnapshot {
        self.snapshot_generation(&ModelGeneration::new(model_id, 0))
    }

    pub(crate) fn snapshot_generation(&self, generation: &ModelGeneration) -> QueueModelSnapshot {
        self.inner
            .lock()
            .models
            .get(generation)
            .map_or_else(QueueModelSnapshot::default, QueueModelState::snapshot)
    }

    #[cfg(test)]
    pub(crate) fn transition_observation_with(
        &self,
        observation: &RequestObservation,
        observe: impl FnOnce(&RequestObservationTransition),
    ) -> RequestObservationTransition {
        let generation = ModelGeneration::new(observation.model_id.clone(), 0);
        self.transition_generation_observation_with(observation, Some(&generation), observe)
    }

    pub(crate) fn transition_generation_observation_with(
        &self,
        observation: &RequestObservation,
        generation: Option<&ModelGeneration>,
        observe: impl FnOnce(&RequestObservationTransition),
    ) -> RequestObservationTransition {
        let _order = self.observation_order.lock();
        let transition = self
            .inner
            .lock()
            .transition_observation(observation, generation);
        observe(&transition);
        transition
    }

    pub(crate) fn update_active_output_tps(
        &self,
        request_id: &str,
        active_chat_output_tps: Option<f64>,
    ) -> Option<String> {
        self.inner
            .lock()
            .update_active_output_tps(request_id, active_chat_output_tps)
    }

    pub(crate) fn finish_queue_request(&self, request_id: &str) {
        let mut state = self.inner.lock();
        if let Some((request_id, request)) = state.remove_request(request_id)
            && let Some(observed) = request.into_observed()
        {
            state.insert_request(request_id, LiveRequest::Observed(observed));
        }
    }
}

impl QueueAdmissionState {
    fn transition_observation(
        &mut self,
        observation: &RequestObservation,
        generation: Option<&ModelGeneration>,
    ) -> RequestObservationTransition {
        let (request_id, prior_queue, prior_observed) =
            match self.remove_request(&observation.request_id) {
                Some((request_id, LiveRequest::Queue(queue, observed))) => {
                    (request_id, Some(queue), observed)
                }
                Some((request_id, LiveRequest::Observed(observed))) => {
                    (request_id, None, Some(observed))
                }
                None => (observation.request_id.clone(), None, None),
            };
        let generation = generation.cloned().or_else(|| {
            prior_queue
                .as_ref()
                .map(|request| request.generation.clone())
                .or_else(|| {
                    prior_observed
                        .as_ref()
                        .map(|request| request.generation.clone())
                })
        });
        let changed_generations = changed_generations(
            generation.as_ref(),
            [
                prior_queue.as_ref().map(|request| &request.generation),
                prior_observed.as_ref().map(|request| &request.generation),
            ],
        );
        let current = match (observation.is_terminal(), generation) {
            (false, Some(generation)) => {
                let phase = [
                    TrackedPromptPhase::Pending,
                    TrackedPromptPhase::Pending,
                    TrackedPromptPhase::InputProcessing,
                    TrackedPromptPhase::OutputGeneration,
                ][observation.state as usize];
                let phase = prior_queue
                    .as_ref()
                    .map_or(phase, |prior| phase.max(prior.phase));
                let active_chat_output_tps = prior_queue
                    .as_ref()
                    .filter(|_| phase == TrackedPromptPhase::OutputGeneration)
                    .and_then(|request| request.active_chat_output_tps);
                let current = ObservedRequestState {
                    generation: generation.clone(),
                    state: observation.state,
                    input_tokens: observation.input_tokens,
                };
                self.insert_request(
                    request_id,
                    LiveRequest::Queue(
                        TrackedPromptRequest {
                            generation,
                            priority: observation.priority,
                            input_tokens: observation.input_tokens,
                            phase,
                            active_chat_output_tps,
                        },
                        Some(current.clone()),
                    ),
                );
                Some(current)
            }
            _ => None,
        };
        let input_token_totals =
            self.input_token_totals([prior_observed.as_ref(), current.as_ref()]);
        RequestObservationTransition {
            changed_generations,
            prior: prior_observed,
            current,
            input_token_totals,
        }
    }

    fn update_active_output_tps(
        &mut self,
        request_id: &str,
        active_chat_output_tps: Option<f64>,
    ) -> Option<String> {
        let (request_id, mut request) = self.remove_request(request_id)?;
        let model_id = request.update_active_output_tps(active_chat_output_tps);
        self.insert_request(request_id, request);
        model_id
    }

    fn advance_request_phase(&mut self, request_id: &str, next_phase: TrackedPromptPhase) {
        let Some(LiveRequest::Queue(request, _)) = self.requests.get_mut(request_id) else {
            return;
        };
        if next_phase <= request.phase {
            return;
        }
        let model = self
            .models
            .get_mut(&request.generation)
            .expect("tracked request should have model queue state");
        model.adjust_phase(request.phase, request.priority, request.input_tokens, -1);
        model.adjust_phase(next_phase, request.priority, request.input_tokens, 1);
        request.phase = next_phase;
    }

    fn remove_request(&mut self, request_id: &str) -> Option<(String, LiveRequest)> {
        let (request_id, request) = self.requests.remove_entry(request_id)?;
        self.adjust_live_request(&request, -1);
        Some((request_id, request))
    }

    fn insert_request(&mut self, request_id: String, request: LiveRequest) {
        self.adjust_live_request(&request, 1);
        self.requests.insert(request_id, request);
    }

    fn adjust_live_request(&mut self, request: &LiveRequest, delta: i8) {
        let (queue, observed) = match request {
            LiveRequest::Queue(queue, observed) => (Some(queue), observed.as_ref()),
            LiveRequest::Observed(observed) => (None, Some(observed)),
        };
        if let Some(observed) = observed {
            let model = self.models.entry(observed.generation.clone()).or_default();
            let total = &mut model.observed_input_tokens[observed.state as usize];
            *total = total
                .checked_add_signed(i128::from(observed.input_tokens) * i128::from(delta))
                .expect("observed input token total overflowed or underflowed");
        }
        if let Some(queue) = queue {
            self.models
                .entry(queue.generation.clone())
                .or_default()
                .adjust_request(queue, delta);
        }
        if delta < 0 {
            let generations = [
                observed.map(|request| &request.generation),
                queue.map(|request| &request.generation),
            ];
            for generation in generations.into_iter().flatten() {
                self.remove_model_if_unused(generation);
            }
        }
    }

    fn remove_model_if_unused(&mut self, generation: &ModelGeneration) {
        if matches!(self.models.get(generation), Some(model) if model.is_unused()) {
            self.models.remove(generation);
        }
    }

    fn input_token_totals(
        &self,
        requests: [Option<&ObservedRequestState>; 2],
    ) -> Vec<ObservedInputTokenTotal> {
        let mut totals = Vec::with_capacity(2);
        for request in requests.into_iter().flatten() {
            if totals.iter().any(|total: &ObservedInputTokenTotal| {
                total.generation == request.generation && total.state == request.state
            }) {
                continue;
            }
            totals.push(ObservedInputTokenTotal {
                generation: request.generation.clone(),
                state: request.state,
                input_tokens: self.models.get(&request.generation).map_or(0, |model| {
                    model.observed_input_tokens[request.state as usize]
                }),
            });
        }
        totals
    }
}

impl QueueModelState {
    fn adjust_request(&mut self, request: &TrackedPromptRequest, request_delta: i8) {
        self.total_input_tokens = self
            .total_input_tokens
            .checked_add_signed(i128::from(request.input_tokens) * i128::from(request_delta))
            .expect("model total input tokens overflowed or underflowed");
        self.adjust_phase(
            request.phase,
            request.priority,
            request.input_tokens,
            request_delta,
        );
        self.active_chat_output_tps_sum +=
            request.active_chat_output_tps.unwrap_or_default() * f64::from(request_delta);
        self.active_chat_output_requests = self
            .active_chat_output_requests
            .checked_add_signed(if request.active_chat_output_tps.is_some() {
                isize::from(request_delta)
            } else {
                0
            })
            .expect("model active output request count overflowed or underflowed");
    }

    fn adjust_phase(
        &mut self,
        phase: TrackedPromptPhase,
        priority: u32,
        input_tokens: u64,
        request_delta: i8,
    ) {
        let count = &mut self.phase_requests[phase as usize];
        *count = count
            .checked_add_signed(isize::from(request_delta))
            .expect("model phase request count overflowed or underflowed");
        let Some((effective_priority, remaining)) = phase.prompt_work(priority, input_tokens)
        else {
            return;
        };
        let work = self
            .prompt_work_by_priority
            .entry(effective_priority)
            .or_default();
        *work = work
            .checked_add_signed(i128::from(remaining) * i128::from(request_delta))
            .expect("model prompt input tokens overflowed or underflowed");
        if *work == 0 {
            self.prompt_work_by_priority.remove(&effective_priority);
        }
    }

    fn snapshot(&self) -> QueueModelSnapshot {
        let mut cumulative_input_tokens = 0u128;
        let mut estimates = self.last_mean_input_tps.map(|_| HashMap::new());
        let [pending, input_processing, output_generation] = self.phase_requests;
        let queued = pending
            .checked_add(input_processing)
            .expect("model queued request count overflowed");
        let running = queued
            .checked_add(output_generation)
            .expect("model running request count overflowed");

        for (priority, input_tokens) in &self.prompt_work_by_priority {
            cumulative_input_tokens = cumulative_input_tokens
                .checked_add(*input_tokens)
                .expect("model cumulative input tokens overflowed");
            if let Some((last_mean_input_tps, estimates)) =
                self.last_mean_input_tps.zip(estimates.as_mut())
            {
                estimates.insert(
                    *priority,
                    saturated_queue_time_ms(cumulative_input_tokens, last_mean_input_tps),
                );
            }
        }

        QueueModelSnapshot {
            active_chat_output_tps: if self.active_chat_output_requests == 0 {
                0.0
            } else {
                self.active_chat_output_tps_sum / self.active_chat_output_requests as f64
            },
            queue_size: saturated_u64(queued),
            queued_input_size: saturated_u64(cumulative_input_tokens),
            num_running_queries: saturated_u64(running),
            total_query_input_size: saturated_u64(self.total_input_tokens),
            input_processing_queries: saturated_u64(input_processing),
            output_generation_queries: saturated_u64(output_generation),
            // Valid throughput and no prompt work is an explicit empty map:
            // downstream merges must clear previously published queues.
            queue_time_estimate_ms_by_priority: estimates,
        }
    }

    fn queue_estimate_ms_for_priority_excluding(
        &self,
        priority: u32,
        excluded_request: Option<&TrackedPromptRequest>,
    ) -> Option<u64> {
        let last_mean_input_tps = self.last_mean_input_tps?;
        let mut input_tokens = self
            .prompt_work_by_priority
            .range(..=priority)
            .try_fold(0u128, |total, (_, input_tokens)| {
                total.checked_add(*input_tokens)
            })
            .expect("model cumulative input tokens overflowed");
        if let Some((excluded_priority, excluded_input_tokens)) =
            excluded_request.and_then(|request| {
                request
                    .phase
                    .prompt_work(request.priority, request.input_tokens)
            })
            && excluded_priority <= priority
        {
            input_tokens = input_tokens
                .checked_sub(u128::from(excluded_input_tokens))
                .expect("excluded request should be present in model prompt work");
        }
        Some(saturated_queue_time_ms(input_tokens, last_mean_input_tps))
    }

    fn is_unused(&self) -> bool {
        self.phase_requests == [0; 3]
            && self.observed_input_tokens == [0; RequestObservationState::Complete as usize]
            && self.last_mean_input_tps.is_none()
    }
}

fn changed_generations(
    generation: Option<&ModelGeneration>,
    prior_generations: [Option<&ModelGeneration>; 2],
) -> Vec<ModelGeneration> {
    let mut changed: Vec<_> = prior_generations
        .into_iter()
        .flatten()
        .filter(|prior| Some(*prior) != generation)
        .chain(generation)
        .cloned()
        .collect();
    changed.sort();
    changed.dedup();
    changed
}

fn saturated_u64(value: impl TryInto<u64>) -> u64 {
    value.try_into().unwrap_or(u64::MAX)
}

fn saturated_queue_time_ms(input_tokens: u128, last_mean_input_tps: f64) -> u64 {
    debug_assert!(valid_last_mean_input_tps(last_mean_input_tps));
    u64::try_from(input_tokens)
        .ok()
        .and_then(|input_tokens| queue_time_delta_ms(input_tokens, last_mean_input_tps))
        .unwrap_or(u64::MAX)
}

impl TrackedPromptPhase {
    fn prompt_work(self, priority: u32, input_tokens: u64) -> Option<(u32, u64)> {
        match (self, input_tokens) {
            (_, 0) | (Self::OutputGeneration, _) => None,
            (Self::Pending, input_tokens) => Some((priority, input_tokens)),
            (Self::InputProcessing, input_tokens) => Some((0, input_tokens)),
        }
    }
}

impl QueueTrackedRequestGuard {
    pub(crate) fn on_upstream_response_headers(&mut self) {
        let mut state = self.live_requests.inner.lock();
        state.advance_request_phase(&self.request_id, TrackedPromptPhase::InputProcessing);
    }

    pub(crate) fn observe_output(&mut self) {
        let mut state = self.live_requests.inner.lock();
        state.advance_request_phase(&self.request_id, TrackedPromptPhase::OutputGeneration);
    }

    pub(crate) fn finish(&mut self) {
        if !self.finished {
            self.live_requests.finish_queue_request(&self.request_id);
            self.finished = true;
        }
    }
}

impl Drop for QueueTrackedRequestGuard {
    fn drop(&mut self) {
        self.finish();
    }
}

fn mismatch_threshold_ms(expected_ms: u64, config: &PylonQueueMismatchRetryConfig) -> u64 {
    let additive_threshold = expected_ms.saturating_add(config.min_delta_ms);
    let factor = if config.tolerance_factor.is_finite() && config.tolerance_factor > 0.0 {
        config.tolerance_factor
    } else {
        1.0
    };
    // Float-to-integer conversion saturates, matching the additive overflow policy.
    let multiplicative_threshold = ((expected_ms as f64) * factor).ceil() as u64;
    additive_threshold.max(multiplicative_threshold)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::request_observer::RequestObservationEndpoint;
    use reqwest::header::{HeaderName, HeaderValue};
    use std::time::{Duration, Instant};

    impl LiveRequestState {
        fn transition_observation(
            &self,
            observation: &RequestObservation,
        ) -> RequestObservationTransition {
            self.transition_observation_with(observation, |_| {})
        }

        pub(crate) fn tracked_request_count(&self) -> usize {
            self.inner
                .lock()
                .requests
                .values()
                .filter(|request| matches!(request, LiveRequest::Queue(..)))
                .count()
        }
    }

    fn required(request_id: &str, priority: u32, input_tokens: u64) -> RequiredTunnelHeaders {
        required_for_model(request_id, "model-a", priority, input_tokens)
    }

    fn required_for_model(
        request_id: &str,
        model_id: &str,
        priority: u32,
        input_tokens: u64,
    ) -> RequiredTunnelHeaders {
        RequiredTunnelHeaders {
            request_id: request_id.to_string(),
            routing_key: None,
            model_id: model_id.to_string(),
            priority,
            input_tokens,
            accepted_at: Instant::now(),
        }
    }

    fn headers_with_expected(expected_ms: &str) -> HeaderMap {
        let name = HeaderName::from_static(HEADER_STARGATE_EXPECTED_QUEUE_MS);
        [(name, HeaderValue::from_str(expected_ms).unwrap())]
            .into_iter()
            .collect()
    }

    fn estimates(live_requests: &LiveRequestState) -> Option<HashMap<u32, u64>> {
        live_requests
            .snapshot_model("model-a")
            .queue_time_estimate_ms_by_priority
    }

    fn rejected(actual_ms: u64) -> QueueAdmissionDecision {
        QueueAdmissionDecision::Rejected {
            expected_ms: 0,
            actual_ms,
            threshold_ms: 25,
            retry_after_ms: None,
        }
    }

    fn observation(request_id: &str, state: RequestObservationState) -> RequestObservation {
        RequestObservation {
            endpoint: RequestObservationEndpoint::ChatCompletions,
            request_id: request_id.to_string(),
            routing_key: None,
            model_id: "model-a".to_string(),
            priority: 0,
            input_tokens: 100,
            embedding_items: 0,
            embedding_items_observed: false,
            upstream_status: None,
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

    #[test]
    fn one_live_request_transition_updates_queue_and_active_output_load() {
        let live_requests = LiveRequestState::default();
        let _first = live_requests.track_request(&required("req-a", 0, 100));
        let _second = live_requests.track_request(&required("req-b", 0, 100));

        for (request_id, output_tps) in [("req-a", 20.0), ("req-b", 10.0)] {
            live_requests.transition_observation(&observation(
                request_id,
                RequestObservationState::OutputGeneration,
            ));
            live_requests.update_active_output_tps(request_id, Some(output_tps));
        }

        let active = live_requests.snapshot_model("model-a");
        assert_eq!(active.output_generation_queries, 2);
        assert_eq!(active.active_chat_output_tps, 15.0);

        live_requests
            .transition_observation(&observation("req-a", RequestObservationState::Complete));

        let terminal = live_requests.snapshot_model("model-a");
        assert_eq!(terminal.output_generation_queries, 1);
        assert_eq!(terminal.active_chat_output_tps, 10.0);
    }

    #[test]
    fn metric_publication_does_not_hold_live_request_state_lock() {
        let live_requests = LiveRequestState::default();
        let worker = live_requests.clone();
        let (entered_tx, entered_rx) = std::sync::mpsc::sync_channel(0);
        let (release_tx, release_rx) = std::sync::mpsc::sync_channel(0);
        let transition = std::thread::spawn(move || {
            worker.transition_observation_with(
                &observation("req-a", RequestObservationState::InputProcessing),
                |_| {
                    entered_tx.send(()).unwrap();
                    release_rx.recv().unwrap();
                },
            )
        });
        entered_rx.recv_timeout(Duration::from_secs(1)).unwrap();

        let reader = live_requests.clone();
        let (snapshot_tx, snapshot_rx) = std::sync::mpsc::sync_channel(0);
        let snapshot = std::thread::spawn(move || {
            snapshot_tx.send(reader.snapshot_model("model-a")).unwrap();
        });
        assert_eq!(
            snapshot_rx
                .recv_timeout(Duration::from_secs(1))
                .expect("metric publication must not block request-state snapshots")
                .input_processing_queries,
            1
        );

        release_tx.send(()).unwrap();
        snapshot.join().unwrap();
        transition.join().unwrap();
    }

    #[test]
    fn concurrent_observation_publication_preserves_transition_order() {
        let live_requests = LiveRequestState::default();
        let (totals_tx, totals_rx) = std::sync::mpsc::channel();
        let (first_entered_tx, first_entered_rx) = std::sync::mpsc::sync_channel(0);
        let (release_tx, release_rx) = std::sync::mpsc::sync_channel(0);

        let first_state = live_requests.clone();
        let first_totals = totals_tx.clone();
        let first = std::thread::spawn(move || {
            first_state.transition_observation_with(
                &observation("req-a", RequestObservationState::InputProcessing),
                |transition| {
                    first_totals
                        .send(transition.input_token_totals[0].input_tokens)
                        .unwrap();
                    first_entered_tx.send(()).unwrap();
                    release_rx.recv().unwrap();
                },
            );
        });
        first_entered_rx
            .recv_timeout(Duration::from_secs(1))
            .unwrap();
        assert_eq!(totals_rx.recv().unwrap(), 100);

        let second_state = live_requests.clone();
        let second = std::thread::spawn(move || {
            second_state.transition_observation_with(
                &observation("req-b", RequestObservationState::InputProcessing),
                |transition| {
                    totals_tx
                        .send(transition.input_token_totals[0].input_tokens)
                        .unwrap();
                },
            );
        });
        assert!(
            matches!(
                totals_rx.recv_timeout(Duration::from_millis(100)),
                Err(std::sync::mpsc::RecvTimeoutError::Timeout)
            ),
            "a later transition must not publish before the earlier callback completes"
        );

        release_tx.send(()).unwrap();
        assert_eq!(totals_rx.recv_timeout(Duration::from_secs(1)).unwrap(), 200);
        first.join().unwrap();
        second.join().unwrap();
    }

    #[test]
    fn threshold_helper_accepts_at_threshold_and_rejects_above() {
        let mut config = PylonQueueMismatchRetryConfig::default();
        let threshold = mismatch_threshold_ms(100, &config);
        assert_eq!(threshold, 125);
        assert!(126 > threshold);
        assert!(125 <= threshold);
        assert_eq!(mismatch_threshold_ms(0, &config), 25);
        assert_eq!(mismatch_threshold_ms(u64::MAX, &config), u64::MAX);
        config.tolerance_factor = f64::NAN;
        assert_eq!(mismatch_threshold_ms(100, &config), 125);
    }

    #[test]
    fn live_request_state_publishes_cumulative_priority_queue_estimates() {
        let live_requests = LiveRequestState::default();
        live_requests.update_model_throughput("model-a", 100.0);
        let _priority_two = live_requests.track_request(&required("req-p2", 2, 20));
        let mut priority_four = live_requests.track_request(&required("req-p4", 4, 30));
        let mut zero_input = live_requests.track_request(&required("req-zero", 1, 0));
        priority_four.on_upstream_response_headers();

        assert_eq!(live_requests.snapshot_model("model-a").queue_size, 3);
        zero_input.on_upstream_response_headers();
        let snapshot = live_requests.snapshot_model("model-a");

        assert_eq!(snapshot.queue_size, 3);
        assert_eq!(snapshot.queued_input_size, 50);
        assert_eq!(snapshot.num_running_queries, 3);
        assert_eq!(
            snapshot.queue_time_estimate_ms_by_priority,
            Some(HashMap::from([(0, 300), (2, 500)]))
        );
        drop(zero_input);
        assert_eq!(live_requests.snapshot_model("model-a").queue_size, 2);
    }

    #[test]
    fn live_request_state_excludes_output_generation_and_drains_on_drop() {
        let live_requests = LiveRequestState::default();
        live_requests.update_model_throughput("model-a", 100.0);
        let mut request = live_requests.track_request(&required("req-output", 2, 20));
        request.observe_output();

        let snapshot = live_requests.snapshot_model("model-a");
        assert_eq!(snapshot.queue_size, 0);
        assert_eq!(snapshot.queued_input_size, 0);
        assert_eq!(snapshot.output_generation_queries, 1);

        drop(request);
        assert_eq!(live_requests.tracked_request_count(), 0);
    }

    #[test]
    fn live_request_state_publishes_explicit_empty_priority_estimates_after_prompt_queue_drains() {
        let live_requests = LiveRequestState::default();
        live_requests.update_model_throughput("model-a", 100.0);
        let request = live_requests.track_request(&required("req-drained", 2, 20));

        assert_eq!(estimates(&live_requests), Some(HashMap::from([(2, 200)])));

        drop(request);
        assert_eq!(estimates(&live_requests), Some(HashMap::new()));
    }

    #[test]
    fn queue_model_state_moves_replaced_request_identity_between_models() {
        let live_requests = LiveRequestState::default();
        live_requests.update_model_throughput("model-a", 100.0);
        live_requests.update_model_throughput("model-b", 100.0);
        let _first =
            live_requests.track_request(&required_for_model("req-shared", "model-a", 4, 20));
        let _replacement =
            live_requests.track_request(&required_for_model("req-shared", "model-b", 2, 30));

        assert_eq!(
            live_requests.snapshot_model("model-a"),
            QueueModelSnapshot {
                queue_time_estimate_ms_by_priority: Some(HashMap::new()),
                ..QueueModelSnapshot::default()
            }
        );
        assert_eq!(
            live_requests.snapshot_model("model-b"),
            QueueModelSnapshot {
                queue_size: 1,
                queued_input_size: 30,
                num_running_queries: 1,
                total_query_input_size: 30,
                queue_time_estimate_ms_by_priority: Some(HashMap::from([(2, 300)])),
                ..QueueModelSnapshot::default()
            }
        );
    }

    #[test]
    fn queue_cleanup_preserves_observed_state_until_terminal_transition() {
        let live_requests = LiveRequestState::default();
        let mut request = live_requests.track_request(&required("req-finished", 0, 100));
        let live = live_requests.transition_observation(&observation(
            "req-finished",
            RequestObservationState::UpstreamConnecting,
        ));
        request.finish();
        assert_eq!(live_requests.tracked_request_count(), 0);

        let mut terminal_observation = observation("req-finished", RequestObservationState::Failed);
        terminal_observation.model_id = "model-b".to_string();
        let terminal = live_requests.transition_observation(&terminal_observation);

        assert_eq!(terminal.prior, live.current);
        assert_eq!(terminal.current, None);
        assert_eq!(
            terminal.changed_generations,
            [
                ModelGeneration::new("model-a", 0),
                ModelGeneration::new("model-b", 0),
            ]
        );
        assert_eq!(live_requests.tracked_request_count(), 0);
    }

    #[test]
    fn delayed_earlier_observation_does_not_requeue_output_generation() {
        let live_requests = LiveRequestState::default();
        let mut request = live_requests.track_request(&required("req-output", 0, 100));
        request.observe_output();

        live_requests.transition_observation(&observation(
            "req-output",
            RequestObservationState::UpstreamConnecting,
        ));

        let snapshot = live_requests.snapshot_model("model-a");
        assert_eq!(snapshot.queue_size, 0);
        assert_eq!(snapshot.output_generation_queries, 1);
    }

    #[test]
    fn queue_model_state_keeps_guard_phase_updates_monotonic() {
        let live_requests = LiveRequestState::default();
        let mut request = live_requests.track_request(&required("req-output", 0, 100));
        request.observe_output();
        request.on_upstream_response_headers();

        let snapshot = live_requests.snapshot_model("model-a");
        assert_eq!(snapshot.queue_size, 0);
        assert_eq!(snapshot.input_processing_queries, 0);
        assert_eq!(snapshot.output_generation_queries, 1);
    }

    #[test]
    fn queue_model_state_saturates_unrepresentable_valid_throughput_estimates() {
        let live_requests = LiveRequestState::default();
        live_requests.update_model_throughput("model-a", f64::MIN_POSITIVE);
        let _queued = live_requests.track_request(&required("req-huge", 0, u64::MAX));

        assert_eq!(
            estimates(&live_requests),
            Some(HashMap::from([(0, u64::MAX)]))
        );
        assert_eq!(
            live_requests.evaluate(
                &PylonQueueMismatchRetryConfig::default(),
                &required("req-new", 0, 1),
                &headers_with_expected("0"),
            ),
            rejected(u64::MAX)
        );
    }

    #[test]
    fn queue_model_state_excludes_current_request_after_public_totals_saturate() {
        let live_requests = LiveRequestState::default();
        live_requests.update_model_throughput("model-a", 1.0);
        let current = required("req-current-huge", 0, u64::MAX);
        let _current = live_requests.track_request(&current);
        let _other = live_requests.track_request(&required("req-other", 0, 1));

        let snapshot = live_requests.snapshot_model("model-a");
        assert_eq!(snapshot.queued_input_size, u64::MAX);
        assert_eq!(
            live_requests.evaluate(
                &PylonQueueMismatchRetryConfig::default(),
                &current,
                &headers_with_expected("0"),
            ),
            rejected(1000)
        );
    }

    #[test]
    fn upstream_response_headers_do_not_apply_retired_progress_contract() {
        let live_requests = LiveRequestState::default();
        let mut request = live_requests.track_request(&required("req-progress", 0, 100));

        request.on_upstream_response_headers();

        let snapshot = live_requests.snapshot_model("model-a");
        assert_eq!(snapshot.queued_input_size, 100);
        assert_eq!(snapshot.input_processing_queries, 1);
    }

    #[test]
    fn admission_transitions_from_unknown_to_rejected_after_throughput_update() {
        let live_requests = LiveRequestState::default();
        let config = PylonQueueMismatchRetryConfig::default();
        let request = required("req-inflight", 0, 100);
        let _guard = live_requests.track_request(&request);
        let incoming = required("req-new", 0, 1);
        let headers = headers_with_expected("0");

        assert_eq!(
            live_requests.evaluate_generation(&config, &incoming, None, &headers),
            QueueAdmissionDecision::UnknownLocalEstimate { expected_ms: 0 }
        );
        assert_eq!(
            live_requests.evaluate(&config, &incoming, &headers),
            QueueAdmissionDecision::UnknownLocalEstimate { expected_ms: 0 }
        );

        live_requests.update_model_throughput("model-a", 100.0);
        assert_eq!(
            live_requests.evaluate(&config, &incoming, &headers),
            rejected(1000)
        );
    }

    #[test]
    fn admission_excludes_current_request_even_when_observed_before_evaluation() {
        let live_requests = LiveRequestState::default();
        let config = PylonQueueMismatchRetryConfig::default();
        let current = required("req-current", 0, 10);
        let _guard = live_requests.track_request(&current);
        live_requests.update_model_throughput("model-a", 100.0);

        assert_eq!(
            live_requests.evaluate(&config, &current, &headers_with_expected("0")),
            QueueAdmissionDecision::Accepted {
                expected_ms: 0,
                actual_ms: 0,
                threshold_ms: 25,
            }
        );
    }

    #[test]
    fn disabled_admission_accepts_with_distinct_metric_label() {
        let live_requests = LiveRequestState::default();
        live_requests.update_model_throughput("model-a", 100.0);
        let inflight = required("req-inflight", 0, 100);
        let _guard = live_requests.track_request(&inflight);
        let config = PylonQueueMismatchRetryConfig {
            enabled: false,
            ..PylonQueueMismatchRetryConfig::default()
        };

        let decision = live_requests.evaluate(
            &config,
            &required("req-new", 0, 1),
            &headers_with_expected("0"),
        );
        assert_eq!(decision, QueueAdmissionDecision::Disabled);
        assert_eq!(decision.result_label(), "disabled");
    }

    #[test]
    fn admission_accepts_missing_or_invalid_expected_queue_header() {
        let live_requests = LiveRequestState::default();
        let config = PylonQueueMismatchRetryConfig::default();
        let incoming = required("req-new", 0, 1);
        for headers in [HeaderMap::new(), headers_with_expected("nope")] {
            assert_eq!(
                live_requests.evaluate(&config, &incoming, &headers),
                QueueAdmissionDecision::MissingEstimate
            );
        }
    }
}

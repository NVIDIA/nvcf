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

use std::collections::BTreeMap;
use std::time::{Duration, Instant};

#[cfg(test)]
use reqwest::header::HeaderMap;

use crate::generated_request_id::{GeneratedRequestKind, generated_request_kind};
use crate::runtime_state::{
    ModelGeneration, OutputCalibrationFacts, PylonRuntimeState, RequestInputInterval,
    RequestObservationMetadata,
};
use crate::sse_message_stream::{ChatChoiceCalibration, SseEventProtocol};

mod embeddings;
mod headers;
mod tunnel;

#[cfg(test)]
use headers::MissingRequiredHeaderError;
pub(crate) use headers::{RequiredTunnelHeaders, validate_required_tunnel_headers};
pub(crate) use tunnel::TunnelRequestObserver;

const MAX_TRACKED_CHAT_CHOICES: usize = 128;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RequestObservationState {
    Queued,
    UpstreamConnecting,
    InputProcessing,
    OutputGeneration,
    Complete,
    Failed,
    Cancelled,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RequestObservationEndpoint {
    ChatCompletions,
    Responses,
    Embeddings,
}

#[derive(Debug)]
enum RequestLifecycleState {
    UpstreamConnecting,
    BackendSubmitted(BackendPhaseData),
    Terminal {
        outcome: RequestTerminalOutcome,
        backend: Option<BackendPhaseData>,
    },
}

#[derive(Debug, Clone, Copy)]
enum RequestTerminalOutcome {
    Complete,
    Failed,
    Cancelled,
}

impl RequestTerminalOutcome {
    fn observation_state(self) -> RequestObservationState {
        match self {
            Self::Complete => RequestObservationState::Complete,
            Self::Failed => RequestObservationState::Failed,
            Self::Cancelled => RequestObservationState::Cancelled,
        }
    }
}

impl RequestLifecycleState {
    fn observation_state(&self) -> RequestObservationState {
        match self {
            Self::UpstreamConnecting => RequestObservationState::UpstreamConnecting,
            Self::BackendSubmitted(backend) if backend.first_generated_output_at.is_none() => {
                RequestObservationState::InputProcessing
            }
            Self::BackendSubmitted(_) => RequestObservationState::OutputGeneration,
            Self::Terminal { outcome, .. } => outcome.observation_state(),
        }
    }

    fn backend(&self) -> Option<&BackendPhaseData> {
        match self {
            Self::BackendSubmitted(backend) => Some(backend),
            Self::Terminal { backend, .. } => backend.as_ref(),
            Self::UpstreamConnecting => None,
        }
    }
}

#[derive(Debug, Clone)]
pub struct RequestObservation {
    pub endpoint: RequestObservationEndpoint,
    pub request_id: String,
    pub routing_key: Option<String>,
    pub model_id: String,
    pub priority: u32,
    pub input_tokens: u64,
    pub embedding_items: u64,
    pub embedding_items_observed: bool,
    pub upstream_status: Option<u16>,
    pub output_messages: u64,
    pub output_tokens: u64,
    pub output_tokens_explicit: bool,
    pub output_tokens_from_chunk_usage: bool,
    pub state: RequestObservationState,
    pub time_to_response_headers: Option<Duration>,
    pub time_to_first_output: Option<Duration>,
    pub time_to_first_token: Option<Duration>,
    pub total_duration: Duration,
}

impl RequestObservation {
    pub fn is_terminal(&self) -> bool {
        matches!(
            self.state,
            RequestObservationState::Complete
                | RequestObservationState::Failed
                | RequestObservationState::Cancelled
        )
    }
}

#[derive(Clone, Copy, Debug)]
struct BackendPhaseData {
    submitted_at: Instant,
    upstream_status: Option<u16>,
    response_headers_at: Option<Instant>,
    last_upstream_event_at: Option<Instant>,
    first_generated_output_at: Option<Instant>,
    first_token_at: Option<Instant>,
    output_messages: u64,
    output_tokens: u64,
    output_calibration: OutputCalibrationFacts,
    output_tokens_explicit: bool,
    output_tokens_from_chunk_usage: bool,
}

#[derive(Debug, Default)]
struct ChatCalibrationState {
    choices: BTreeMap<u64, ChatChoiceState>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ChatChoiceState {
    Streaming,
    Finished,
}

pub(crate) struct RequestObserver {
    endpoint: RequestObservationEndpoint,
    request_id: String,
    started_at: Instant,
    routing_key: Option<String>,
    model_id: String,
    priority: u32,
    request_input_tokens: u64,
    input_tokens: u64,
    input_tokens_explicit: bool,
    generation: Option<ModelGeneration>,
    embedding_items: Option<u64>,
    chat_calibration: Option<ChatCalibrationState>,
    state: RequestLifecycleState,
    runtime_state: PylonRuntimeState,
}

impl RequestObserver {
    pub(crate) fn from_required(
        endpoint: RequestObservationEndpoint,
        required: RequiredTunnelHeaders,
        generation: Option<ModelGeneration>,
        runtime_state: PylonRuntimeState,
    ) -> Self {
        let priority = required.queue_priority();
        let RequiredTunnelHeaders {
            request_id,
            routing_key,
            model_id,
            priority: _,
            input_tokens,
            accepted_at,
        } = required;
        let output_token_calibration_enabled = runtime_state.output_token_calibration_enabled();
        let mut observer = Self {
            endpoint,
            request_id,
            started_at: accepted_at,
            routing_key,
            model_id,
            priority,
            request_input_tokens: input_tokens,
            input_tokens,
            input_tokens_explicit: false,
            generation,
            embedding_items: None,
            chat_calibration: (output_token_calibration_enabled
                && endpoint == RequestObservationEndpoint::ChatCompletions)
                .then(ChatCalibrationState::default),
            state: RequestLifecycleState::UpstreamConnecting,
            runtime_state,
        };
        observer.emit();
        observer
    }

    pub(crate) fn output_token_calibration_enabled(&self) -> bool {
        self.runtime_state.output_token_calibration_enabled()
    }

    pub(super) fn update_embedding_items(&mut self, embedding_items: Option<u64>) {
        debug_assert_eq!(self.endpoint, RequestObservationEndpoint::Embeddings);
        if self.embedding_items != embedding_items {
            self.embedding_items = embedding_items;
            self.emit();
        }
    }

    pub(crate) fn on_backend_submission(&mut self, submitted_at: Instant) {
        if !matches!(self.state, RequestLifecycleState::UpstreamConnecting) {
            return;
        }
        self.state = RequestLifecycleState::BackendSubmitted(BackendPhaseData {
            submitted_at,
            upstream_status: None,
            response_headers_at: None,
            last_upstream_event_at: None,
            first_generated_output_at: None,
            first_token_at: None,
            output_messages: 0,
            output_tokens: 0,
            output_calibration: OutputCalibrationFacts::default(),
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
        });
        self.emit();
    }

    pub(crate) fn on_upstream_response_headers(&mut self, status: u16) {
        let backend = Self::backend_mut(&mut self.state, &self.request_id, "response-header");
        if backend.response_headers_at.is_some() {
            return;
        }
        backend.upstream_status = Some(status);
        backend.response_headers_at = Some(Instant::now());
        self.emit();
    }

    pub(crate) fn observe_generated_output(
        &mut self,
        observed_at: Instant,
        token_bearing: bool,
        raw_output_units: u64,
        reasoning_text: bool,
    ) {
        let backend = Self::backend_mut(&mut self.state, &self.request_id, "output observation");
        backend.output_messages = backend.output_messages.saturating_add(1);
        backend.first_generated_output_at.get_or_insert(observed_at);
        backend.output_calibration.raw_output_units = raw_output_units;
        backend.output_calibration.reasoning_text_observed |= reasoning_text;
        if token_bearing {
            backend.first_token_at.get_or_insert(observed_at);
        }
        self.emit();
    }

    pub(crate) fn observe_upstream_event(&mut self, observed_at: Instant) {
        let backend = Self::backend_mut(
            &mut self.state,
            &self.request_id,
            "upstream event observation",
        );
        backend.last_upstream_event_at = Some(
            backend
                .last_upstream_event_at
                .map_or(observed_at, |previous| previous.max(observed_at)),
        );
    }

    pub(crate) fn observe_input_tokens_total(&mut self, input_tokens: u64) {
        if self.input_tokens_explicit {
            if input_tokens < self.input_tokens {
                tracing::warn!(
                    request_id = self.request_id,
                    prior_input_tokens = self.input_tokens,
                    input_tokens,
                    "ignoring regressing explicit input token counter"
                );
                return;
            }
            if input_tokens == self.input_tokens {
                return;
            }
        }
        self.input_tokens = input_tokens;
        self.input_tokens_explicit = true;
        self.emit();
    }

    pub(crate) fn observe_estimated_output_tokens_total(&mut self, output_tokens: u64) {
        let backend = Self::backend_mut(
            &mut self.state,
            &self.request_id,
            "output token observation",
        );
        if backend.output_tokens == output_tokens && !backend.output_tokens_explicit {
            return;
        }
        backend.output_tokens = output_tokens;
        backend.output_tokens_explicit = false;
        self.emit();
    }

    pub(crate) fn observe_output_calibration_details(
        &mut self,
        reasoning_tokens: Option<u64>,
        calibration_ineligible: bool,
    ) {
        let backend = Self::backend_mut(
            &mut self.state,
            &self.request_id,
            "output usage detail observation",
        );
        backend.output_calibration.reasoning_tokens = match (
            backend.output_calibration.reasoning_tokens,
            reasoning_tokens,
        ) {
            (Some(previous), Some(current)) => Some(previous.max(current)),
            (previous, current) => previous.or(current),
        };
        backend.output_calibration.calibration_ineligible |= calibration_ineligible;
    }

    pub(crate) fn observe_sse_protocol(&mut self, protocol: SseEventProtocol) {
        let matches_endpoint = matches!(
            (self.endpoint, protocol),
            (
                RequestObservationEndpoint::ChatCompletions,
                SseEventProtocol::ChatCompletions
            ) | (
                RequestObservationEndpoint::Responses,
                SseEventProtocol::Responses
            )
        );
        if !matches_endpoint {
            self.observe_output_calibration_details(None, true);
        }
    }

    pub(crate) fn observe_chat_choice_calibration(&mut self, choice: ChatChoiceCalibration) {
        let Some(state) = self.chat_calibration.as_mut() else {
            self.observe_output_calibration_details(None, true);
            return;
        };
        let event_after_finish =
            state.choices.get(&choice.index) == Some(&ChatChoiceState::Finished);
        if event_after_finish {
            self.observe_output_calibration_details(None, true);
            return;
        }
        if !state.choices.contains_key(&choice.index)
            && state.choices.len() >= MAX_TRACKED_CHAT_CHOICES
        {
            self.chat_calibration = None;
            self.observe_output_calibration_details(None, true);
            return;
        }
        let choice_state = if choice.safely_finished {
            ChatChoiceState::Finished
        } else {
            ChatChoiceState::Streaming
        };
        state.choices.insert(choice.index, choice_state);
    }

    pub(crate) fn observe_output_tokens_generated_so_far(&mut self, output_tokens: u64) {
        self.observe_explicit_output_tokens(output_tokens, true, true);
    }

    pub(crate) fn observe_output_tokens_total(&mut self, output_tokens: u64) {
        self.observe_explicit_output_tokens(output_tokens, false, false);
    }

    fn observe_explicit_output_tokens(
        &mut self,
        output_tokens: u64,
        from_chunk_usage: bool,
        emit_live_update: bool,
    ) {
        let backend = Self::backend_mut(
            &mut self.state,
            &self.request_id,
            "output token observation",
        );
        if backend
            .output_calibration
            .exact_output_tokens_baseline
            .is_some_and(|prior| output_tokens < prior)
        {
            backend.output_calibration.calibration_ineligible = true;
            tracing::warn!(
                request_id = self.request_id,
                prior_output_tokens = backend.output_calibration.exact_output_tokens_baseline,
                output_tokens_generated_so_far = output_tokens,
                "ignoring regressing explicit output token counter"
            );
            return;
        }
        let chunk_usage_observed = backend.output_tokens_from_chunk_usage || from_chunk_usage;
        if backend.output_tokens_explicit
            && output_tokens == backend.output_tokens
            && backend.output_tokens_from_chunk_usage == chunk_usage_observed
        {
            return;
        }
        let should_emit = output_tokens > 0 || output_tokens != backend.output_tokens;
        backend.output_tokens = output_tokens;
        backend.output_calibration.exact_output_tokens_baseline = Some(output_tokens);
        backend.output_tokens_explicit = true;
        backend.output_tokens_from_chunk_usage = chunk_usage_observed;
        if emit_live_update && should_emit {
            self.emit();
        }
    }

    pub(crate) fn complete(&mut self) {
        let unfinished_chat_choices = self.chat_calibration.as_ref().is_some_and(|state| {
            state
                .choices
                .values()
                .any(|choice| *choice != ChatChoiceState::Finished)
        });
        if unfinished_chat_choices {
            self.observe_output_calibration_details(None, true);
        }
        self.terminate(RequestTerminalOutcome::Complete);
    }

    pub(crate) fn fail(&mut self) {
        self.terminate(RequestTerminalOutcome::Failed);
    }

    pub(crate) fn cancel(&mut self) {
        self.terminate(RequestTerminalOutcome::Cancelled);
    }

    fn terminate(&mut self, outcome: RequestTerminalOutcome) {
        let backend = match &self.state {
            RequestLifecycleState::BackendSubmitted(backend) => Some(*backend),
            RequestLifecycleState::UpstreamConnecting => None,
            RequestLifecycleState::Terminal { .. } => return,
        };
        self.state = RequestLifecycleState::Terminal { outcome, backend };
        self.emit();
    }

    pub(crate) fn is_terminal(&self) -> bool {
        matches!(self.state, RequestLifecycleState::Terminal { .. })
    }

    fn backend_mut<'a>(
        state: &'a mut RequestLifecycleState,
        request_id: &str,
        action: &'static str,
    ) -> &'a mut BackendPhaseData {
        let observation_state = state.observation_state();
        match state {
            RequestLifecycleState::BackendSubmitted(backend) => backend,
            _ => panic!(
                "invalid {action} transition for request_id={request_id} from state={observation_state:?}"
            ),
        }
    }
    fn emit(&mut self) {
        let backend = self.state.backend();
        let input_interval = backend.and_then(|backend| {
            backend
                .first_generated_output_at
                .map(|first_generated_output_at| RequestInputInterval {
                    submitted_at: backend.submitted_at,
                    first_generated_output_at,
                })
        });
        let observation = RequestObservation {
            endpoint: self.endpoint,
            request_id: self.request_id.clone(),
            routing_key: self.routing_key.clone(),
            model_id: self.model_id.clone(),
            priority: self.priority,
            input_tokens: self.input_tokens,
            embedding_items: self.embedding_items.unwrap_or_default(),
            embedding_items_observed: self.endpoint == RequestObservationEndpoint::Embeddings
                && self.embedding_items.is_some(),
            upstream_status: backend.and_then(|backend| backend.upstream_status),
            output_messages: backend.map_or(0, |backend| backend.output_messages),
            output_tokens: backend.map_or(0, |backend| backend.output_tokens),
            output_tokens_explicit: backend.is_some_and(|backend| backend.output_tokens_explicit),
            output_tokens_from_chunk_usage: backend
                .is_some_and(|backend| backend.output_tokens_from_chunk_usage),
            state: self.state.observation_state(),
            // Observation timestamps can be coarser than event sequencing; never underflow
            // durations when two instants collapse to the same clock tick.
            time_to_response_headers: backend
                .and_then(|backend| backend.response_headers_at)
                .map(|instant| instant.saturating_duration_since(self.started_at)),
            time_to_first_output: backend
                .and_then(|backend| backend.first_generated_output_at)
                .map(|instant| instant.saturating_duration_since(self.started_at)),
            time_to_first_token: backend
                .and_then(|backend| backend.first_token_at)
                .map(|instant| instant.saturating_duration_since(self.started_at)),
            total_duration: self.started_at.elapsed(),
        };
        log_observation(&observation);
        self.runtime_state.observe_request_for_generation(
            observation,
            self.generation.clone(),
            RequestObservationMetadata {
                input_interval,
                request_input_tokens: self.request_input_tokens,
                input_tokens_explicit: self.input_tokens_explicit,
                output_calibration: backend
                    .map_or_else(OutputCalibrationFacts::default, |backend| {
                        backend.output_calibration
                    }),
                upstream_duration: backend
                    .and_then(|backend| backend.last_upstream_event_at)
                    .map(|instant| instant.saturating_duration_since(self.started_at)),
            },
        );
    }
}

fn log_observation(observation: &RequestObservation) {
    if !observation_logs_at_info(&observation.request_id) {
        tracing::debug!(
            request_id = observation.request_id,
            model_id = observation.model_id,
            state = ?observation.state,
            "calibration request observed"
        );
    } else if observation.endpoint == RequestObservationEndpoint::Embeddings {
        tracing::info!(
            request_id = observation.request_id,
            endpoint = ?observation.endpoint,
            routing_key = observation.routing_key.as_deref().unwrap_or(""),
            model_id = observation.model_id.as_str(),
            priority = observation.priority,
            input_tokens = observation.input_tokens,
            embedding_items = ?observation
                .embedding_items_observed
                .then_some(observation.embedding_items),
            upstream_status = observation.upstream_status.unwrap_or_default(),
            state = ?observation.state,
            time_to_response_headers_ms = observation
                .time_to_response_headers
                .map_or(0.0, |duration| duration.as_secs_f64() * 1000.0),
            total_duration_ms = observation.total_duration.as_secs_f64() * 1000.0,
            "embeddings request observed"
        );
    } else {
        tracing::info!(
            request_id = observation.request_id,
            endpoint = ?observation.endpoint,
            routing_key = observation.routing_key.as_deref().unwrap_or(""),
            model_id = observation.model_id.as_str(),
            priority = observation.priority,
            input_tokens = observation.input_tokens,
            upstream_status = observation.upstream_status.unwrap_or_default(),
            output_messages = observation.output_messages,
            output_tokens = observation.output_tokens,
            output_tokens_explicit = observation.output_tokens_explicit,
            output_tokens_from_chunk_usage = observation.output_tokens_from_chunk_usage,
            state = ?observation.state,
            time_to_response_headers_ms = observation
                .time_to_response_headers
                .map_or(0.0, |duration| duration.as_secs_f64() * 1000.0),
            time_to_first_output_ms = observation
                .time_to_first_output
                .map_or(0.0, |duration| duration.as_secs_f64() * 1000.0),
            time_to_first_token_ms = observation
                .time_to_first_token
                .map_or(0.0, |duration| duration.as_secs_f64() * 1000.0),
            total_duration_ms = observation.total_duration.as_secs_f64() * 1000.0,
            "client request observed"
        );
    }
}

fn observation_logs_at_info(request_id: &str) -> bool {
    generated_request_kind(request_id) != Some(GeneratedRequestKind::Calibration)
}

impl Drop for RequestObserver {
    fn drop(&mut self) {
        if !self.is_terminal() {
            self.cancel();
        }
    }
}

#[cfg(test)]
mod tests {
    use stargate_proto::pb::InferenceServerStatus;
    use stargate_protocol::tunnel_contract::{
        HEADER_INPUT_TOKENS, HEADER_MODEL, HEADER_REQUEST_ID, HEADER_ROUTING_KEY,
    };

    use super::*;
    use super::{embeddings::embedding_items_from_request_body, headers::RequiredHeaderErrorKind};
    use crate::generated_request_id::next_generated_request_id;

    impl RequestObserver {
        fn accepted(required: RequiredTunnelHeaders, runtime_state: PylonRuntimeState) -> Self {
            Self::from_required(
                RequestObservationEndpoint::Embeddings,
                required,
                None,
                runtime_state,
            )
        }

        fn new(
            request_headers: &HeaderMap,
            runtime_state: PylonRuntimeState,
        ) -> Result<Self, MissingRequiredHeaderError> {
            Ok(Self::from_required(
                RequestObservationEndpoint::ChatCompletions,
                validate_required_tunnel_headers(request_headers)?,
                None,
                runtime_state,
            ))
        }

        fn submit_now(&mut self) {
            self.on_backend_submission(Instant::now());
        }

        fn observe_output_message(&mut self) {
            self.observe_generated_output(Instant::now(), true, 0, false);
        }

        fn finish(&mut self) {
            self.complete();
        }
    }

    fn observed_runtime(
        capacity: usize,
    ) -> (
        PylonRuntimeState,
        flume::Receiver<crate::RequestObservationEvent>,
    ) {
        PylonRuntimeState::observed(InferenceServerStatus::Unknown, &[], capacity, None)
    }

    async fn recv_observation(
        rx: &flume::Receiver<crate::RequestObservationEvent>,
        context: &'static str,
    ) -> RequestObservation {
        tokio::time::timeout(Duration::from_secs(1), rx.recv_async())
            .await
            .expect(context)
            .expect("observation channel should remain open")
            .into_observation()
    }

    fn recv_observation_blocking(
        rx: &flume::Receiver<crate::RequestObservationEvent>,
        context: &'static str,
    ) -> RequestObservation {
        rx.try_recv().expect(context).into_observation()
    }

    fn request_headers(request_id: &str, input_tokens: u64) -> HeaderMap {
        let mut headers = HeaderMap::new();
        headers.insert(HEADER_REQUEST_ID, request_id.parse().unwrap());
        headers.insert(HEADER_ROUTING_KEY, "rk-1".parse().unwrap());
        headers.insert(HEADER_MODEL, "model-a".parse().unwrap());
        headers.insert(HEADER_INPUT_TOKENS, input_tokens.into());
        headers
    }

    fn test_observer(request_id: &str, runtime_state: PylonRuntimeState) -> RequestObserver {
        RequestObserver::new(&request_headers(request_id, 42), runtime_state).unwrap()
    }

    #[test]
    fn chat_calibration_state_exists_only_when_enabled() {
        for (enabled, expected_state) in [(false, false), (true, true)] {
            let (runtime_state, _rx) = observed_runtime(4);
            let runtime_state = if enabled {
                runtime_state.with_single_pylon_output_token_calibration()
            } else {
                runtime_state
            };
            let observer = test_observer("req-calibration-state", runtime_state);

            assert_eq!(observer.output_token_calibration_enabled(), enabled);
            assert_eq!(observer.chat_calibration.is_some(), expected_state);
        }
    }

    #[test]
    fn chat_calibration_stops_tracking_after_the_choice_limit() {
        let (runtime_state, _rx) = observed_runtime(4);
        let mut observer = test_observer(
            "req-choice-limit",
            runtime_state.with_single_pylon_output_token_calibration(),
        );
        observer.submit_now();

        for index in 0..MAX_TRACKED_CHAT_CHOICES as u64 {
            observer.observe_chat_choice_calibration(ChatChoiceCalibration {
                index,
                safely_finished: false,
            });
        }
        assert_eq!(
            observer
                .chat_calibration
                .as_ref()
                .expect("choice tracking should remain active at the limit")
                .choices
                .len(),
            MAX_TRACKED_CHAT_CHOICES
        );
        observer.observe_chat_choice_calibration(ChatChoiceCalibration {
            index: 0,
            safely_finished: false,
        });
        assert!(observer.chat_calibration.is_some());

        observer.observe_chat_choice_calibration(ChatChoiceCalibration {
            index: MAX_TRACKED_CHAT_CHOICES as u64,
            safely_finished: false,
        });

        assert!(observer.chat_calibration.is_none());
        assert!(
            response(&observer)
                .output_calibration
                .calibration_ineligible
        );

        observer.observe_chat_choice_calibration(ChatChoiceCalibration {
            index: MAX_TRACKED_CHAT_CHOICES as u64 + 1,
            safely_finished: false,
        });
        assert!(observer.chat_calibration.is_none());
    }

    #[test]
    fn only_exact_process_calibration_observations_are_debug_only() {
        let generation = ModelGeneration::new("model-a", 1);
        let calibration = next_generated_request_id(GeneratedRequestKind::Calibration, &generation);
        let canary = next_generated_request_id(GeneratedRequestKind::Canary, &generation);

        assert!(!observation_logs_at_info(&calibration));
        assert!(observation_logs_at_info(&canary));
        assert!(observation_logs_at_info(
            "calibration-00000000-0000-0000-0000-000000000000-g1-1"
        ));
        assert!(observation_logs_at_info("user-request"));
    }

    async fn responding_observer(
        request_id: &str,
    ) -> (
        RequestObserver,
        flume::Receiver<crate::RequestObservationEvent>,
        RequestObservation,
    ) {
        let (runtime_state, rx) = observed_runtime(8);
        let mut observer = test_observer(request_id, runtime_state);
        recv_observation(&rx, "initial observation should be emitted").await;
        observer.submit_now();
        recv_observation(&rx, "backend-submission observation should be emitted").await;
        observer.on_upstream_response_headers(200);
        let headers = recv_observation(&rx, "response-header observation should be emitted").await;
        (observer, rx, headers)
    }

    fn response(observer: &RequestObserver) -> &BackendPhaseData {
        match &observer.state {
            RequestLifecycleState::BackendSubmitted(response)
            | RequestLifecycleState::Terminal {
                backend: Some(response),
                ..
            } => response,
            state => panic!("state has no response: {state:?}"),
        }
    }

    #[test]
    fn validate_required_tunnel_headers_accepts_required_values() {
        let mut headers = request_headers("req-1", 42);
        headers.insert("x-priority", "7".parse().unwrap());

        let required = validate_required_tunnel_headers(&headers).unwrap();

        assert_eq!(required.request_id, "req-1");
        assert_eq!(required.routing_key.as_deref(), Some("rk-1"));
        assert_eq!(required.model_id, "model-a");
        assert_eq!(required.input_tokens, 42);
        assert_eq!(required.priority, Some(7));
    }

    #[test]
    fn validate_required_tunnel_headers_keeps_missing_priority_absent() {
        let required = validate_required_tunnel_headers(&request_headers("req-1", 42)).unwrap();

        assert_eq!(required.priority, None);
    }

    #[test]
    fn validate_required_tunnel_headers_rejects_malformed_priority() {
        let mut headers = request_headers("req-1", 42);
        headers.insert("x-priority", "not-a-priority".parse().unwrap());

        let error = validate_required_tunnel_headers(&headers).unwrap_err();

        assert_eq!(error.header_name, "x-priority");
        assert_eq!(error.kind, RequiredHeaderErrorKind::Invalid);
    }

    #[test]
    fn validate_required_tunnel_headers_rejects_malformed_input_tokens() {
        for value in [
            "not-a-token-count".parse().unwrap(),
            "".parse().unwrap(),
            " ".parse().unwrap(),
            reqwest::header::HeaderValue::from_bytes(&[0xff]).unwrap(),
        ] {
            let mut headers = request_headers("req-1", 42);
            headers.insert(HEADER_INPUT_TOKENS, value);

            let error = validate_required_tunnel_headers(&headers).unwrap_err();

            assert_eq!(error.header_name, HEADER_INPUT_TOKENS);
            assert_eq!(error.kind, RequiredHeaderErrorKind::Invalid);
        }
    }

    #[test]
    fn validate_required_tunnel_headers_rejects_missing_required_values() {
        for missing in [HEADER_REQUEST_ID, HEADER_MODEL, HEADER_INPUT_TOKENS] {
            let mut headers = request_headers("req-1", 42);
            headers.remove(missing);

            let error = validate_required_tunnel_headers(&headers).unwrap_err();

            assert_eq!(error.header_name, missing);
            assert_eq!(error.kind, RequiredHeaderErrorKind::Missing);
        }
    }

    #[test]
    fn embeddings_observation_counts_input_cardinality() {
        for (body, expected) in [
            (br#"{"input":"hello"}"#.as_slice(), 1),
            (br#"{"input":["a","b"]}"#, 2),
            (br#"{"input":[1,2,3]}"#, 1),
            (br#"{"input":[[1,2],[3,4],[5]]}"#, 3),
            (br#"{"input":[]}"#, 0),
        ] {
            assert_eq!(embedding_items_from_request_body(body), Some(expected));
        }
        assert_eq!(embedding_items_from_request_body(br#"{"input":"#), None);
    }

    fn embeddings_required_headers() -> RequiredTunnelHeaders {
        RequiredTunnelHeaders {
            request_id: "req-embeddings-terminal".to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-embed".to_string(),
            priority: None,
            input_tokens: 12,
            accepted_at: Instant::now(),
        }
    }

    #[test]
    fn tunnel_observer_drop_cancels_each_observed_endpoint() {
        for endpoint in [
            RequestObservationEndpoint::ChatCompletions,
            RequestObservationEndpoint::Responses,
            RequestObservationEndpoint::Embeddings,
        ] {
            let (runtime_state, rx) = observed_runtime(4);
            let observer = TunnelRequestObserver::accepted(
                endpoint,
                embeddings_required_headers(),
                None,
                runtime_state,
            );

            // Dropping a live tunnel observer is the terminal behavior under test.
            drop(observer);

            let observations = rx
                .try_iter()
                .map(crate::RequestObservationEvent::into_observation)
                .collect::<Vec<_>>();
            assert_eq!(observations.len(), 2);
            assert_eq!(observations[0].endpoint, endpoint);
            assert_eq!(
                observations[0].state,
                RequestObservationState::UpstreamConnecting
            );
            assert_eq!(observations[1].endpoint, endpoint);
            assert_eq!(observations[1].state, RequestObservationState::Cancelled);
        }
    }

    #[test]
    fn terminal_tunnel_observer_drop_emits_nothing() {
        for endpoint in [
            RequestObservationEndpoint::ChatCompletions,
            RequestObservationEndpoint::Responses,
            RequestObservationEndpoint::Embeddings,
        ] {
            let (runtime_state, rx) = observed_runtime(8);
            let mut observer = TunnelRequestObserver::accepted(
                endpoint,
                embeddings_required_headers(),
                None,
                runtime_state,
            );
            observer.on_backend_submission(Instant::now());
            observer.on_upstream_response_headers(200);
            if let Some(generation) = observer.generation_mut() {
                generation.observe_output_message();
            }
            observer.complete();
            let observations_before_drop = rx.len();

            // Dropping a terminal observer proves the wrapper does not emit again.
            drop(observer);

            assert_eq!(rx.len(), observations_before_drop);
        }
    }

    #[test]
    fn embeddings_observer_uses_request_acceptance_time() {
        let mut required = embeddings_required_headers();
        required.accepted_at = Instant::now()
            .checked_sub(Duration::from_millis(50))
            .expect("test acceptance time should be representable");
        let (runtime_state, rx) = observed_runtime(4);

        let _observer = RequestObserver::accepted(required, runtime_state);
        let observation = recv_observation_blocking(&rx, "accepted embeddings observation");

        assert!(observation.total_duration >= Duration::from_millis(40));
    }

    #[test]
    fn embeddings_observer_can_record_cardinality_after_acceptance() {
        let (runtime_state, rx) = observed_runtime(4);
        let mut observer = RequestObserver::accepted(embeddings_required_headers(), runtime_state);

        let accepted = recv_observation_blocking(&rx, "accepted embeddings observation");
        assert_eq!(accepted.embedding_items, 0);
        assert!(!accepted.embedding_items_observed);

        observer.update_embedding_items(Some(0));
        let parsed = recv_observation_blocking(&rx, "parsed embeddings observation");
        assert_eq!(parsed.embedding_items, 0);
        assert!(parsed.embedding_items_observed);
    }

    #[test]
    fn embeddings_observer_ignores_repeated_terminal_transition() {
        let (runtime_state, rx) = observed_runtime(8);
        let mut observer = RequestObserver::accepted(embeddings_required_headers(), runtime_state);
        observer.update_embedding_items(Some(1));
        observer.submit_now();
        observer.on_upstream_response_headers(200);
        observer.finish();
        while rx.try_recv().is_ok() {}

        observer.fail();

        assert!(observer.is_terminal());
        drop(observer);
        assert!(rx.is_empty(), "repeated terminalization must not emit");
    }

    #[test]
    fn embeddings_observer_ignores_repeated_completion() {
        let mut observer =
            RequestObserver::accepted(embeddings_required_headers(), PylonRuntimeState::default());
        observer.update_embedding_items(Some(1));
        observer.fail();
        observer.finish();
        assert_eq!(
            observer.state.observation_state(),
            RequestObservationState::Failed
        );
    }

    #[tokio::test]
    async fn counts_sse_events_across_chunk_boundaries() {
        let mut observer = test_observer("req-1", PylonRuntimeState::default());
        observer.submit_now();
        observer.on_upstream_response_headers(200);
        observer.observe_output_message();
        observer.observe_output_message();
        observer.finish();

        let response = response(&observer);
        assert_eq!(response.output_messages, 2);
        assert_eq!(response.output_tokens, 0);
        assert_eq!(
            observer.state.observation_state(),
            RequestObservationState::Complete
        );
    }

    #[tokio::test]
    async fn non_terminal_updates_are_emitted() {
        let (runtime_state, rx) = observed_runtime(8);
        let mut observer = test_observer("req-live", runtime_state);

        let initial = recv_observation(&rx, "initial observation should be emitted").await;
        assert_eq!(initial.state, RequestObservationState::UpstreamConnecting);
        assert!(!initial.is_terminal());

        observer.submit_now();
        let submitted =
            recv_observation(&rx, "backend-submission observation should be emitted").await;
        assert_eq!(submitted.state, RequestObservationState::InputProcessing);
        assert_eq!(submitted.upstream_status, None);

        observer.on_upstream_response_headers(200);
        let first = recv_observation(&rx, "response-header observation should be emitted").await;
        assert_eq!(first.state, RequestObservationState::InputProcessing);
        assert!(!first.is_terminal());

        observer.observe_output_message();
        let second = recv_observation(&rx, "output observation should be emitted").await;
        assert_eq!(second.state, RequestObservationState::OutputGeneration);
        assert_eq!(second.output_messages, 1);
        assert!(!second.is_terminal());
    }

    #[test]
    fn input_interval_is_repeated_on_cumulative_output_and_terminal_events() {
        let (runtime_state, rx) = observed_runtime(8);
        let mut observer = test_observer("req-input-interval", runtime_state);
        rx.try_recv().unwrap();

        let submitted_at = Instant::now();
        observer.on_backend_submission(submitted_at);
        let submitted = rx.try_recv().unwrap();
        assert_eq!(
            submitted.observation().state,
            RequestObservationState::InputProcessing
        );
        assert_eq!(submitted.input_interval(), None);

        observer.on_upstream_response_headers(200);
        let headers = rx.try_recv().unwrap();
        assert_eq!(
            headers.observation().state,
            RequestObservationState::InputProcessing
        );
        assert_eq!(headers.input_interval(), None);

        let first_generated_output_at = submitted_at + Duration::from_millis(10);
        observer.observe_generated_output(first_generated_output_at, true, 1, false);
        let output = rx.try_recv().unwrap();
        assert_eq!(
            output.input_interval(),
            Some(RequestInputInterval {
                submitted_at,
                first_generated_output_at,
            })
        );
        assert_eq!(
            output.input_processing_duration(),
            Some(Duration::from_millis(10))
        );

        observer.observe_estimated_output_tokens_total(2);
        let tokens = rx.try_recv().unwrap();
        assert_eq!(tokens.input_interval(), output.input_interval());

        observer.complete();
        let terminal = rx.try_recv().unwrap();
        assert_eq!(terminal.input_interval(), output.input_interval());
    }

    #[test]
    fn terminal_observation_preserves_upstream_completion_time() {
        let accepted_at = Instant::now()
            .checked_sub(Duration::from_secs(10))
            .expect("test acceptance time should be representable");
        let required = RequiredTunnelHeaders {
            request_id: "req-upstream-completion".to_string(),
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
            priority: None,
            input_tokens: 42,
            accepted_at,
        };
        let (runtime_state, rx) = observed_runtime(8);
        let mut observer = RequestObserver::from_required(
            RequestObservationEndpoint::ChatCompletions,
            required,
            None,
            runtime_state,
        );
        rx.try_recv().unwrap();
        observer.on_backend_submission(accepted_at + Duration::from_millis(500));
        rx.try_recv().unwrap();

        let first_output_at = accepted_at + Duration::from_secs(1);
        observer.observe_upstream_event(first_output_at);
        observer.observe_generated_output(first_output_at, true, 1, false);
        let output = rx.try_recv().unwrap();
        assert_eq!(output.upstream_duration, Some(Duration::from_secs(1)));

        observer.observe_output_tokens_total(100);
        observer.observe_upstream_event(accepted_at + Duration::from_secs(2));
        observer.complete();
        let terminal = rx.try_recv().unwrap();

        assert_eq!(terminal.upstream_duration, Some(Duration::from_secs(2)));
        assert!(terminal.observation().total_duration >= Duration::from_secs(9));
    }

    #[tokio::test]
    async fn upstream_connecting_observation_is_emitted_when_request_starts() {
        let (runtime_state, rx) = observed_runtime(8);
        let _observer = test_observer("req-connect", runtime_state);

        let observation = recv_observation(&rx, "initial observation should be emitted").await;
        assert_eq!(observation.request_id, "req-connect");
        assert_eq!(
            observation.state,
            RequestObservationState::UpstreamConnecting
        );
        assert_eq!(observation.upstream_status, None);
        assert!(!observation.is_terminal());
    }

    #[tokio::test]
    async fn dropping_nonterminal_observer_emits_cancelled() {
        let (runtime_state, rx) = observed_runtime(8);
        let observer = test_observer("req-cancel", runtime_state);
        let initial = recv_observation(&rx, "initial observation should be emitted").await;
        assert_eq!(initial.state, RequestObservationState::UpstreamConnecting);

        drop(observer);

        let terminal = recv_observation(&rx, "drop should emit terminal observation").await;
        assert_eq!(terminal.state, RequestObservationState::Cancelled);
        assert!(terminal.is_terminal());
    }

    #[tokio::test]
    async fn tracks_cumulative_estimated_output_tokens() {
        let mut observer = test_observer("req-1", PylonRuntimeState::default());
        observer.submit_now();
        observer.on_upstream_response_headers(200);
        observer.observe_output_message();
        observer.observe_estimated_output_tokens_total(3);
        observer.observe_output_message();
        observer.observe_estimated_output_tokens_total(5);
        observer.finish();

        let response = response(&observer);
        assert_eq!(response.output_messages, 2);
        assert_eq!(response.output_tokens, 5);
    }

    #[tokio::test]
    async fn output_tokens_alone_do_not_start_real_ttft() {
        let (mut observer, rx, _) = responding_observer("req-token").await;

        observer.observe_estimated_output_tokens_total(3);
        let token_observation = recv_observation(&rx, "token observation should be emitted").await;
        assert_eq!(
            token_observation.state,
            RequestObservationState::InputProcessing
        );
        assert_eq!(token_observation.output_tokens, 3);
        assert_eq!(token_observation.time_to_first_output, None);
        assert_eq!(token_observation.time_to_first_token, None);
    }

    #[tokio::test]
    async fn modal_output_starts_first_output_without_claiming_a_first_token() {
        let (mut observer, rx, _) = responding_observer("req-modal-output").await;

        observer.observe_output_calibration_details(None, true);
        observer.observe_generated_output(Instant::now(), false, 0, false);
        let output = recv_observation(&rx, "modal output observation should be emitted").await;

        assert_eq!(output.state, RequestObservationState::OutputGeneration);
        assert!(output.time_to_first_output.is_some());
        assert_eq!(output.time_to_first_token, None);
    }

    #[tokio::test]
    async fn exact_input_usage_updates_tokens_without_starting_output() {
        let (runtime_state, rx) = PylonRuntimeState::observed(
            InferenceServerStatus::Unknown,
            &["model-a".to_string()],
            8,
            None,
        );
        let runtime_snapshot = runtime_state.clone();
        let generation = runtime_state.current_generation("model-a");
        let mut observer = RequestObserver::from_required(
            RequestObservationEndpoint::ChatCompletions,
            validate_required_tunnel_headers(&request_headers("req-input-usage", 42))
                .expect("test headers should validate"),
            generation,
            runtime_state,
        );
        let initial = rx
            .recv_async()
            .await
            .expect("initial observation should be emitted");
        assert!(!initial.input_tokens_explicit());
        observer.submit_now();
        recv_observation(&rx, "backend-submission observation should be emitted").await;
        observer.on_upstream_response_headers(200);
        recv_observation(&rx, "response-header observation should be emitted").await;

        observer.observe_input_tokens_total(7);
        let usage = rx
            .recv_async()
            .await
            .expect("input usage observation should be emitted");
        assert!(usage.input_tokens_explicit());
        let usage = usage.into_observation();
        assert_eq!(usage.state, RequestObservationState::InputProcessing);
        assert_eq!(usage.input_tokens, 7);
        assert_eq!(usage.time_to_first_output, None);
        assert_eq!(usage.time_to_first_token, None);
        assert_eq!(
            runtime_snapshot
                .snapshot_live_model("model-a")
                .total_query_input_size,
            42,
            "exact response usage must not replace request-side live queue work"
        );
    }

    #[tokio::test]
    async fn exact_input_usage_does_not_regress() {
        let (mut observer, rx, _) = responding_observer("req-input-regression").await;

        observer.observe_input_tokens_total(7);
        let first = recv_observation(&rx, "first input usage should be emitted").await;
        assert_eq!(first.input_tokens, 7);

        observer.observe_input_tokens_total(9);
        let increased = recv_observation(&rx, "increased input usage should be emitted").await;
        assert_eq!(increased.input_tokens, 9);

        observer.observe_input_tokens_total(8);
        assert!(rx.is_empty(), "regressing input usage should not emit");
        assert_eq!(observer.input_tokens, 9);
    }

    #[tokio::test]
    async fn response_headers_do_not_disable_text_fallback() {
        let (mut observer, rx, header_observation) =
            responding_observer("req-zero-header-output").await;
        assert_eq!(header_observation.output_tokens, 0);
        assert!(!header_observation.output_tokens_explicit);

        observer.observe_estimated_output_tokens_total(3);
        let estimated_observation =
            recv_observation(&rx, "fallback token observation should be emitted").await;
        assert_eq!(
            estimated_observation.state,
            RequestObservationState::InputProcessing
        );
        assert_eq!(estimated_observation.output_tokens, 3);
        assert!(!estimated_observation.output_tokens_explicit);
        assert_eq!(estimated_observation.time_to_first_token, None);
    }

    #[tokio::test]
    async fn explicit_output_counter_corrects_prior_estimated_tokens() {
        let (mut observer, rx, _) = responding_observer("req-explicit-output").await;

        observer.observe_estimated_output_tokens_total(5);
        let estimated =
            recv_observation(&rx, "estimated token observation should be emitted").await;
        assert_eq!(estimated.output_tokens, 5);
        assert!(!estimated.output_tokens_explicit);

        observer.observe_output_tokens_generated_so_far(3);
        let explicit = recv_observation(&rx, "explicit token observation should be emitted").await;
        assert_eq!(explicit.output_tokens, 3);
        assert!(explicit.output_tokens_explicit);
        assert!(explicit.output_tokens_from_chunk_usage);

        observer.observe_output_tokens_generated_so_far(3);
        assert!(
            rx.is_empty(),
            "repeated explicit counters with no value change should not emit"
        );

        observer.observe_estimated_output_tokens_total(10);
        let estimated_tail =
            recv_observation(&rx, "estimated tail observation should be emitted").await;
        assert_eq!(estimated_tail.output_tokens, 10);
        assert!(!estimated_tail.output_tokens_explicit);

        observer.observe_output_tokens_generated_so_far(4);
        let corrected = recv_observation(&rx, "later exact usage should reconcile the tail").await;
        assert_eq!(corrected.output_tokens, 4);
        assert!(corrected.output_tokens_explicit);
    }

    #[tokio::test]
    async fn output_calibration_facts_accumulate_through_terminal_observation() {
        let (mut observer, rx, _) = responding_observer("req-output-calibration-facts").await;

        observer.observe_output_calibration_details(None, true);
        observer.observe_generated_output(Instant::now(), true, 4, true);
        let generated = rx
            .recv_async()
            .await
            .expect("generated output event should be emitted");
        assert_eq!(
            generated.output_calibration(),
            OutputCalibrationFacts {
                raw_output_units: 4,
                calibration_ineligible: true,
                reasoning_text_observed: true,
                ..OutputCalibrationFacts::default()
            }
        );

        observer.observe_output_calibration_details(Some(3), false);
        observer.observe_output_tokens_generated_so_far(10);
        let exact = rx
            .recv_async()
            .await
            .expect("exact usage event should be emitted");
        assert_eq!(
            exact.output_calibration(),
            OutputCalibrationFacts {
                raw_output_units: 4,
                exact_output_tokens_baseline: Some(10),
                calibration_ineligible: true,
                reasoning_text_observed: true,
                reasoning_tokens: Some(3),
            }
        );

        observer.finish();
        let terminal = rx
            .recv_async()
            .await
            .expect("terminal event should be emitted");
        assert_eq!(terminal.output_calibration(), exact.output_calibration());
    }

    #[tokio::test]
    async fn explicit_output_total_does_not_claim_chunk_usage() {
        let (mut observer, rx, _) = responding_observer("req-top-level-usage").await;

        observer.observe_output_message();
        recv_observation(&rx, "output observation should be emitted").await;
        observer.observe_output_tokens_total(3);
        assert!(
            rx.is_empty(),
            "top-level usage should be emitted with the terminal observation"
        );

        observer.finish();
        let terminal = recv_observation(&rx, "terminal observation should be emitted").await;
        assert!(terminal.is_terminal());
        assert_eq!(terminal.output_tokens, 3);
        assert!(terminal.output_tokens_explicit);
        assert!(!terminal.output_tokens_from_chunk_usage);
    }

    #[tokio::test]
    async fn zero_explicit_counter_marks_chunk_usage_without_extra_live_emit() {
        let (mut observer, rx, _) = responding_observer("req-zero-explicit").await;

        observer.observe_output_message();
        let output_observation =
            recv_observation(&rx, "output observation should be emitted").await;
        assert_eq!(
            output_observation.state,
            RequestObservationState::OutputGeneration
        );
        assert_eq!(output_observation.output_tokens, 0);
        assert!(!output_observation.output_tokens_from_chunk_usage);

        observer.observe_output_tokens_generated_so_far(0);
        assert!(
            rx.is_empty(),
            "zero-token explicit counters should not emit a duplicate live update"
        );

        observer.finish();
        let terminal_observation =
            recv_observation(&rx, "terminal observation should be emitted").await;
        assert!(terminal_observation.is_terminal());
        assert_eq!(terminal_observation.output_tokens, 0);
        assert!(terminal_observation.output_tokens_explicit);
        assert!(terminal_observation.output_tokens_from_chunk_usage);
    }

    #[tokio::test]
    async fn chunk_usage_counter_before_output_controls_live_token_accounting() {
        let (mut observer, rx, _) = responding_observer("req-early-chunk-usage").await;

        observer.observe_output_tokens_generated_so_far(0);
        assert!(
            rx.is_empty(),
            "zero-token chunk usage before output should not emit a duplicate live update"
        );

        observer.observe_estimated_output_tokens_total(3);
        let estimated_tail =
            recv_observation(&rx, "estimated tail should continue after sparse usage").await;
        assert_eq!(estimated_tail.output_tokens, 3);
        assert!(!estimated_tail.output_tokens_explicit);

        observer.observe_output_tokens_generated_so_far(4);
        let explicit = recv_observation(&rx, "positive chunk usage should be emitted").await;
        assert_eq!(explicit.state, RequestObservationState::InputProcessing);
        assert_eq!(explicit.output_tokens, 4);
        assert!(explicit.output_tokens_explicit);
        assert!(explicit.output_tokens_from_chunk_usage);
        assert_eq!(explicit.time_to_first_output, None);
        assert_eq!(explicit.time_to_first_token, None);

        observer.observe_output_tokens_generated_so_far(4);
        assert!(
            rx.is_empty(),
            "repeated chunk usage counters with no value change should not emit"
        );

        observer.observe_output_tokens_generated_so_far(2);
        assert!(
            rx.is_empty(),
            "regressing chunk usage counters should not emit"
        );
    }

    #[test]
    fn input_processing_chunk_usage_counter_rejects_regression() {
        let mut observer = make_test_observer();
        let started_at = Instant::now() - Duration::from_secs(1);
        observer.started_at = started_at;
        observer.state = RequestLifecycleState::BackendSubmitted(BackendPhaseData {
            submitted_at: started_at,
            upstream_status: Some(200),
            response_headers_at: Some(started_at + Duration::from_millis(10)),
            last_upstream_event_at: None,
            first_generated_output_at: None,
            first_token_at: None,
            output_messages: 0,
            output_tokens: 5,
            output_calibration: OutputCalibrationFacts {
                exact_output_tokens_baseline: Some(5),
                ..OutputCalibrationFacts::default()
            },
            output_tokens_explicit: true,
            output_tokens_from_chunk_usage: true,
        });

        observer.observe_output_calibration_details(Some(2), false);
        observer.observe_output_tokens_generated_so_far(3);

        let response = response(&observer);
        assert_eq!(response.output_tokens, 5);
        assert_eq!(response.output_calibration.reasoning_tokens, Some(2));
        assert!(response.output_calibration.calibration_ineligible);
        assert!(response.output_tokens_explicit);
        assert!(response.output_tokens_from_chunk_usage);
        assert_eq!(
            observer.state.observation_state(),
            RequestObservationState::InputProcessing
        );
        assert_eq!(response.first_generated_output_at, None);
    }

    #[test]
    fn reasoning_token_evidence_does_not_regress_to_zero() {
        let mut observer = make_test_observer();
        observer.submit_now();

        observer.observe_output_calibration_details(Some(2), false);
        observer.observe_output_tokens_generated_so_far(3);
        observer.observe_output_calibration_details(Some(0), false);
        observer.observe_output_tokens_generated_so_far(3);

        let response = response(&observer);
        assert_eq!(response.output_tokens, 3);
        assert_eq!(response.output_calibration.reasoning_tokens, Some(2));
    }

    #[test]
    fn late_usage_tokens_preserve_actual_first_token_time() {
        let mut observer = make_test_observer();
        let started_at = Instant::now() - Duration::from_secs(10);
        let first_output_at = started_at + Duration::from_secs(2);
        observer.started_at = started_at;
        observer.state = RequestLifecycleState::BackendSubmitted(BackendPhaseData {
            submitted_at: started_at + Duration::from_millis(25),
            upstream_status: Some(200),
            response_headers_at: Some(started_at + Duration::from_millis(50)),
            last_upstream_event_at: Some(first_output_at),
            first_generated_output_at: Some(first_output_at),
            first_token_at: Some(first_output_at),
            output_messages: 2,
            output_tokens: 0,
            output_calibration: OutputCalibrationFacts::default(),
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
        });

        observer.observe_estimated_output_tokens_total(7);

        let response = response(&observer);
        assert_eq!(response.first_generated_output_at, Some(first_output_at));
    }

    #[test]
    fn explicit_success_can_complete_without_output() {
        let mut observer = test_observer("req-2", PylonRuntimeState::default());
        observer.submit_now();
        observer.on_upstream_response_headers(200);
        observer.finish();
        assert_eq!(
            observer.state.observation_state(),
            RequestObservationState::Complete
        );
        let response = response(&observer);
        assert_eq!(response.first_generated_output_at, None);
    }

    #[test]
    fn terminalization_is_idempotent() {
        let mut observer = make_test_observer();
        observer.submit_now();
        observer.on_upstream_response_headers(200);
        observer.observe_output_message();
        observer.finish();
        observer.fail();
        assert_eq!(
            observer.state.observation_state(),
            RequestObservationState::Complete
        );
    }

    #[test]
    fn response_headers_after_terminal_state_panics() {
        fn panic_message(panic: Box<dyn std::any::Any + Send>) -> String {
            if let Some(message) = panic.downcast_ref::<String>() {
                return message.clone();
            }
            if let Some(message) = panic.downcast_ref::<&'static str>() {
                return (*message).to_string();
            }
            panic!("unexpected non-string panic payload");
        }

        fn assert_terminal_response_header_panic(
            terminalize: impl FnOnce(&mut RequestObserver),
            expected_state: &str,
        ) {
            let mut observer = make_test_observer();
            terminalize(&mut observer);

            let panic = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
                observer.on_upstream_response_headers(200);
            }))
            .expect_err("response headers after terminal state should panic");

            let message = panic_message(panic);
            assert!(message.contains("invalid response-header transition"));
            assert!(message.contains("request_id=req-inv"));
            assert!(message.contains(expected_state));
        }

        assert_terminal_response_header_panic(
            |observer| {
                observer.submit_now();
                observer.on_upstream_response_headers(200);
                observer.observe_output_message();
                observer.finish();
            },
            "state=Complete",
        );
        assert_terminal_response_header_panic(|observer| observer.fail(), "state=Failed");
        assert_terminal_response_header_panic(|observer| observer.cancel(), "state=Cancelled");
    }

    #[tokio::test]
    async fn failed_response_stays_failed() {
        let mut observer = test_observer("req-3", PylonRuntimeState::default());
        observer.submit_now();
        observer.on_upstream_response_headers(503);
        observer.fail();

        let response = response(&observer);
        assert_eq!(response.output_messages, 0);
        assert_eq!(
            observer.state.observation_state(),
            RequestObservationState::Failed
        );
    }

    fn make_test_observer() -> RequestObserver {
        test_observer("req-inv", PylonRuntimeState::default())
    }

    #[test]
    fn is_terminal_reports_correctly() {
        let mut observer = make_test_observer();
        assert!(!observer.is_terminal());
        observer.fail();
        assert!(observer.is_terminal());
    }
}

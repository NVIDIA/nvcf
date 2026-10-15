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

use std::time::{Duration, Instant};

use reqwest::header::HeaderMap;

use crate::generated_request_id::{GeneratedRequestKind, generated_request_kind};
use crate::runtime_state::{ModelGeneration, PylonRuntimeState};

mod embeddings;
mod headers;
mod tunnel;

#[cfg(test)]
use headers::MissingRequiredHeaderError;
pub(crate) use headers::{RequiredTunnelHeaders, validate_required_tunnel_headers};
pub(crate) use tunnel::TunnelRequestObserver;

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
    Responding(ResponsePhaseData),
    Terminal {
        outcome: RequestTerminalOutcome,
        response: Option<ResponsePhaseData>,
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
            Self::Responding(response) if response.first_output_at.is_none() => {
                RequestObservationState::InputProcessing
            }
            Self::Responding(_) => RequestObservationState::OutputGeneration,
            Self::Terminal { outcome, .. } => outcome.observation_state(),
        }
    }

    fn response(&self) -> Option<&ResponsePhaseData> {
        match self {
            Self::Responding(response) => Some(response),
            Self::Terminal { response, .. } => response.as_ref(),
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
struct ResponsePhaseData {
    upstream_status: u16,
    response_headers_at: Instant,
    first_output_at: Option<Instant>,
    first_token_at: Option<Instant>,
    output_messages: u64,
    output_tokens: u64,
    output_tokens_explicit: bool,
    output_tokens_from_chunk_usage: bool,
}

pub(crate) struct RequestObserver {
    endpoint: RequestObservationEndpoint,
    request_id: String,
    started_at: Instant,
    routing_key: Option<String>,
    model_id: String,
    priority: u32,
    input_tokens: u64,
    generation: Option<ModelGeneration>,
    embedding_items: Option<u64>,
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
        let RequiredTunnelHeaders {
            request_id,
            routing_key,
            model_id,
            priority,
            input_tokens,
            accepted_at,
        } = required;
        let mut observer = Self {
            endpoint,
            request_id,
            started_at: accepted_at,
            routing_key,
            model_id,
            priority,
            input_tokens,
            generation,
            embedding_items: None,
            state: RequestLifecycleState::UpstreamConnecting,
            runtime_state,
        };
        observer.emit();
        observer
    }

    pub(super) fn update_embedding_items(&mut self, embedding_items: Option<u64>) {
        debug_assert_eq!(self.endpoint, RequestObservationEndpoint::Embeddings);
        if self.embedding_items != embedding_items {
            self.embedding_items = embedding_items;
            self.emit();
        }
    }

    pub(crate) fn on_upstream_response_headers(
        &mut self,
        _response_headers: &HeaderMap,
        status: u16,
    ) {
        match self.state {
            RequestLifecycleState::UpstreamConnecting => {}
            RequestLifecycleState::Responding(_) | RequestLifecycleState::Terminal { .. } => {
                panic!(
                    "invalid response-header transition for request_id={} from state={:?}",
                    self.request_id,
                    self.state.observation_state()
                )
            }
        }

        self.state = RequestLifecycleState::Responding(ResponsePhaseData {
            upstream_status: status,
            response_headers_at: Instant::now(),
            first_output_at: None,
            first_token_at: None,
            output_messages: 0,
            output_tokens: 0,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
        });
        self.emit();
    }

    pub(crate) fn observe_output_message(&mut self) {
        let response =
            Self::responding_mut(&mut self.state, &self.request_id, "output observation");
        response.output_messages += 1;
        response.first_output_at.get_or_insert_with(Instant::now);
        self.emit();
    }

    pub(crate) fn observe_output_tokens(&mut self, output_tokens: u64) {
        if output_tokens == 0 {
            return;
        }

        let response = Self::responding_mut(
            &mut self.state,
            &self.request_id,
            "output token observation",
        );
        if response.output_tokens_explicit {
            return;
        }
        response.output_tokens += output_tokens;
        let now = Instant::now();
        response.first_output_at.get_or_insert(now);
        response.first_token_at.get_or_insert(now);
        self.emit();
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
        let response = Self::responding_mut(
            &mut self.state,
            &self.request_id,
            "output token observation",
        );
        if response.output_tokens_explicit && output_tokens < response.output_tokens {
            tracing::warn!(
                request_id = self.request_id,
                prior_output_tokens = response.output_tokens,
                output_tokens_generated_so_far = output_tokens,
                "ignoring regressing explicit output token counter"
            );
            return;
        }
        let chunk_usage_observed = response.output_tokens_from_chunk_usage || from_chunk_usage;
        if response.output_tokens_explicit
            && output_tokens == response.output_tokens
            && response.output_tokens_from_chunk_usage == chunk_usage_observed
        {
            return;
        }
        let should_emit = output_tokens > 0 || output_tokens != response.output_tokens;
        response.output_tokens = output_tokens;
        response.output_tokens_explicit = true;
        response.output_tokens_from_chunk_usage = chunk_usage_observed;
        if output_tokens > 0 {
            let now = Instant::now();
            response.first_output_at.get_or_insert(now);
            response.first_token_at.get_or_insert(now);
        }
        if emit_live_update && should_emit {
            self.emit();
        }
    }

    pub(crate) fn finish(&mut self) {
        let (outcome, response) = match &self.state {
            RequestLifecycleState::Responding(response) => {
                let success = (200..300).contains(&response.upstream_status);
                let outcome = if response.first_output_at.is_some() {
                    if success {
                        RequestTerminalOutcome::Complete
                    } else {
                        RequestTerminalOutcome::Failed
                    }
                } else if self.endpoint == RequestObservationEndpoint::Embeddings && success {
                    RequestTerminalOutcome::Complete
                } else if success && response.output_messages == 0 {
                    panic!(
                        "invalid finish transition for request_id={} from state=InputProcessing without observed output",
                        self.request_id
                    )
                } else {
                    RequestTerminalOutcome::Failed
                };
                (outcome, Some(*response))
            }
            RequestLifecycleState::Terminal { outcome, .. } => panic!(
                "invalid finish transition for request_id={} from state={outcome:?}",
                self.request_id,
            ),
            RequestLifecycleState::UpstreamConnecting => (RequestTerminalOutcome::Failed, None),
        };
        self.state = RequestLifecycleState::Terminal { outcome, response };

        self.emit();
    }

    pub(crate) fn fail(&mut self) {
        self.terminate(RequestTerminalOutcome::Failed, "fail");
    }

    fn cancel(&mut self) {
        self.terminate(RequestTerminalOutcome::Cancelled, "cancel");
    }

    fn terminate(&mut self, outcome: RequestTerminalOutcome, action: &'static str) {
        let response = match &self.state {
            RequestLifecycleState::Responding(response) => Some(*response),
            RequestLifecycleState::UpstreamConnecting => None,
            RequestLifecycleState::Terminal { outcome: prior, .. } => panic!(
                "invalid {action} transition for request_id={} from state={prior:?}",
                self.request_id,
            ),
        };
        self.state = RequestLifecycleState::Terminal { outcome, response };
        self.emit();
    }

    pub(crate) fn is_terminal(&self) -> bool {
        matches!(self.state, RequestLifecycleState::Terminal { .. })
    }

    fn responding_mut<'a>(
        state: &'a mut RequestLifecycleState,
        request_id: &str,
        action: &'static str,
    ) -> &'a mut ResponsePhaseData {
        let observation_state = state.observation_state();
        match state {
            RequestLifecycleState::Responding(response) => response,
            _ => panic!(
                "invalid {action} transition for request_id={request_id} from state={observation_state:?}"
            ),
        }
    }
    fn emit(&mut self) {
        let response = self.state.response();
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
            upstream_status: response.map(|response| response.upstream_status),
            output_messages: response.map_or(0, |response| response.output_messages),
            output_tokens: response.map_or(0, |response| response.output_tokens),
            output_tokens_explicit: response
                .is_some_and(|response| response.output_tokens_explicit),
            output_tokens_from_chunk_usage: response
                .is_some_and(|response| response.output_tokens_from_chunk_usage),
            state: self.state.observation_state(),
            // Observation timestamps can be coarser than event sequencing; never underflow
            // durations when two instants collapse to the same clock tick.
            time_to_response_headers: response.map(|response| {
                response
                    .response_headers_at
                    .saturating_duration_since(self.started_at)
            }),
            time_to_first_output: response
                .and_then(|response| response.first_output_at)
                .map(|instant| instant.saturating_duration_since(self.started_at)),
            time_to_first_token: response
                .and_then(|response| response.first_token_at)
                .map(|instant| instant.saturating_duration_since(self.started_at)),
            total_duration: self.started_at.elapsed(),
        };
        log_observation(&observation);
        self.runtime_state
            .observe_request_for_generation(observation, self.generation.clone());
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
        observer.on_upstream_response_headers(&HeaderMap::new(), 200);
        let headers = recv_observation(&rx, "response-header observation should be emitted").await;
        (observer, rx, headers)
    }

    fn response(observer: &RequestObserver) -> &ResponsePhaseData {
        match &observer.state {
            RequestLifecycleState::Responding(response)
            | RequestLifecycleState::Terminal {
                response: Some(response),
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
        assert_eq!(required.priority, 7);
    }

    #[test]
    fn validate_required_tunnel_headers_defaults_missing_priority_to_zero() {
        let required = validate_required_tunnel_headers(&request_headers("req-1", 42)).unwrap();

        assert_eq!(required.priority, 0);
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
            priority: 0,
            input_tokens: 12,
            accepted_at: Instant::now(),
        }
    }

    #[test]
    fn tunnel_observer_drop_fails_each_observed_endpoint() {
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
            assert_eq!(observations[1].state, RequestObservationState::Failed);
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
            observer.on_upstream_response_headers(&HeaderMap::new(), 200);
            if let Some(generation) = observer.generation_mut() {
                generation.observe_output_message();
            }
            observer.finish();
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
    fn embeddings_observer_rejects_terminal_fail_transition() {
        let (runtime_state, rx) = observed_runtime(8);
        let mut observer = RequestObserver::accepted(embeddings_required_headers(), runtime_state);
        observer.update_embedding_items(Some(1));
        observer.on_upstream_response_headers(&HeaderMap::new(), 200);
        observer.finish();
        while rx.try_recv().is_ok() {}

        let panic = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| observer.fail()));

        assert!(panic.is_err());
        assert!(observer.is_terminal());
        drop(observer);
        assert!(rx.is_empty(), "invalid transition must not emit on drop");
    }

    #[test]
    #[should_panic(expected = "invalid finish transition")]
    fn embeddings_observer_rejects_terminal_finish_transition() {
        let mut observer =
            RequestObserver::accepted(embeddings_required_headers(), PylonRuntimeState::default());
        observer.update_embedding_items(Some(1));
        observer.fail();
        observer.finish();
    }

    #[tokio::test]
    async fn counts_sse_events_across_chunk_boundaries() {
        let mut observer = test_observer("req-1", PylonRuntimeState::default());
        observer.on_upstream_response_headers(&HeaderMap::new(), 200);
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

        observer.on_upstream_response_headers(&HeaderMap::new(), 200);
        let first = recv_observation(&rx, "response-header observation should be emitted").await;
        assert_eq!(first.state, RequestObservationState::InputProcessing);
        assert!(!first.is_terminal());

        observer.observe_output_message();
        let second = recv_observation(&rx, "output observation should be emitted").await;
        assert_eq!(second.state, RequestObservationState::OutputGeneration);
        assert_eq!(second.output_messages, 1);
        assert!(!second.is_terminal());
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
    async fn accumulates_output_tokens() {
        let mut observer = test_observer("req-1", PylonRuntimeState::default());
        observer.on_upstream_response_headers(&HeaderMap::new(), 200);
        observer.observe_output_message();
        observer.observe_output_tokens(3);
        observer.observe_output_message();
        observer.observe_output_tokens(2);
        observer.finish();

        let response = response(&observer);
        assert_eq!(response.output_messages, 2);
        assert_eq!(response.output_tokens, 5);
    }

    #[tokio::test]
    async fn first_positive_output_tokens_start_real_ttft() {
        let (mut observer, rx, _) = responding_observer("req-token").await;

        observer.observe_output_tokens(3);
        let token_observation = recv_observation(&rx, "token observation should be emitted").await;
        assert_eq!(
            token_observation.state,
            RequestObservationState::OutputGeneration
        );
        assert_eq!(token_observation.output_tokens, 3);
        assert!(token_observation.time_to_first_output.is_some());
        assert!(token_observation.time_to_first_token.is_some());
    }

    #[tokio::test]
    async fn response_headers_do_not_disable_text_fallback() {
        let (mut observer, rx, header_observation) =
            responding_observer("req-zero-header-output").await;
        assert_eq!(header_observation.output_tokens, 0);
        assert!(!header_observation.output_tokens_explicit);

        observer.observe_output_tokens(3);
        let estimated_observation =
            recv_observation(&rx, "fallback token observation should be emitted").await;
        assert_eq!(
            estimated_observation.state,
            RequestObservationState::OutputGeneration
        );
        assert_eq!(estimated_observation.output_tokens, 3);
        assert!(!estimated_observation.output_tokens_explicit);
        assert!(estimated_observation.time_to_first_token.is_some());
    }

    #[tokio::test]
    async fn explicit_output_counter_corrects_prior_estimated_tokens() {
        let (mut observer, rx, _) = responding_observer("req-explicit-output").await;

        observer.observe_output_tokens(5);
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

        observer.observe_output_tokens(10);
        assert!(
            rx.is_empty(),
            "fallback deltas should not emit after explicit counters"
        );
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

        observer.observe_output_tokens(3);
        assert!(
            rx.is_empty(),
            "fallback token estimates should not emit after chunk usage becomes explicit"
        );

        observer.observe_output_tokens_generated_so_far(4);
        let explicit = recv_observation(&rx, "positive chunk usage should be emitted").await;
        assert_eq!(explicit.state, RequestObservationState::OutputGeneration);
        assert_eq!(explicit.output_tokens, 4);
        assert!(explicit.output_tokens_explicit);
        assert!(explicit.output_tokens_from_chunk_usage);
        assert!(explicit.time_to_first_output.is_some());
        assert!(explicit.time_to_first_token.is_some());

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
        observer.state = RequestLifecycleState::Responding(ResponsePhaseData {
            upstream_status: 200,
            response_headers_at: started_at + Duration::from_millis(10),
            first_output_at: None,
            first_token_at: None,
            output_messages: 0,
            output_tokens: 5,
            output_tokens_explicit: true,
            output_tokens_from_chunk_usage: true,
        });

        observer.observe_output_tokens_generated_so_far(3);

        let response = response(&observer);
        assert_eq!(response.output_tokens, 5);
        assert!(response.output_tokens_explicit);
        assert!(response.output_tokens_from_chunk_usage);
        assert_eq!(
            observer.state.observation_state(),
            RequestObservationState::InputProcessing
        );
        assert_eq!(response.first_output_at, None);
        assert_eq!(response.first_token_at, None);
    }

    #[test]
    fn late_usage_tokens_preserve_actual_first_token_time() {
        let mut observer = make_test_observer();
        let started_at = Instant::now() - Duration::from_secs(10);
        let first_output_at = started_at + Duration::from_secs(2);
        observer.started_at = started_at;
        observer.state = RequestLifecycleState::Responding(ResponsePhaseData {
            upstream_status: 200,
            response_headers_at: started_at + Duration::from_millis(50),
            first_output_at: Some(first_output_at),
            first_token_at: None,
            output_messages: 2,
            output_tokens: 0,
            output_tokens_explicit: false,
            output_tokens_from_chunk_usage: false,
        });

        let before_token_observation = Instant::now();
        observer.observe_output_tokens(7);

        let response = response(&observer);
        assert_eq!(response.first_output_at, Some(first_output_at));
        let observed_first_token_at = response.first_token_at.unwrap();
        assert!(observed_first_token_at >= before_token_observation);
        assert!(observed_first_token_at > first_output_at);
    }

    #[test]
    fn finish_without_output_panics() {
        let mut observer = test_observer("req-2", PylonRuntimeState::default());
        observer.on_upstream_response_headers(&HeaderMap::new(), 200);
        let panic = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| observer.finish()));
        assert!(panic.is_err());
        assert_eq!(
            observer.state.observation_state(),
            RequestObservationState::InputProcessing
        );

        let headers_panic = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            observer.on_upstream_response_headers(&HeaderMap::new(), 200);
        }));
        assert!(headers_panic.is_err());
    }

    #[test]
    #[should_panic(expected = "invalid fail transition")]
    fn fail_after_complete_panics() {
        let mut observer = make_test_observer();
        observer.on_upstream_response_headers(&HeaderMap::new(), 200);
        observer.observe_output_message();
        observer.finish();
        observer.fail();
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
                observer.on_upstream_response_headers(&HeaderMap::new(), 200);
            }))
            .expect_err("response headers after terminal state should panic");

            let message = panic_message(panic);
            assert!(message.contains("invalid response-header transition"));
            assert!(message.contains("request_id=req-inv"));
            assert!(message.contains(expected_state));
        }

        assert_terminal_response_header_panic(
            |observer| {
                observer.on_upstream_response_headers(&HeaderMap::new(), 200);
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
        observer.on_upstream_response_headers(&HeaderMap::new(), 503);
        observer.finish();

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

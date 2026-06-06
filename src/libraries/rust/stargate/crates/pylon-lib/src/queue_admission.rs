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

use std::collections::{BTreeSet, HashMap, HashSet, VecDeque};
use std::sync::Arc;

use parking_lot::Mutex;
use reqwest::header::HeaderMap;
use stargate_protocol::common::{queue_time_delta_ms, valid_last_mean_input_tps};
use stargate_protocol::tunnel_contract::HEADER_STARGATE_EXPECTED_QUEUE_MS;

use crate::request_observer::{RequestObservation, RequestObservationState, RequiredTunnelHeaders};

pub(crate) const RETRY_REASON_QUEUE_ESTIMATE_MISMATCH: &str = "queue_estimate_mismatch";
const FINISHED_REQUEST_TOMBSTONE_CAPACITY: usize = 4_096;

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
pub struct QueueAdmissionTracker {
    inner: Arc<Mutex<QueueAdmissionState>>,
}

#[derive(Debug, Default)]
struct QueueAdmissionState {
    requests: HashMap<String, TrackedPromptRequest>,
    model_input_tps: HashMap<String, f64>,
    finished_request_ids: HashSet<String>,
    finished_request_order: VecDeque<String>,
}

#[derive(Clone, Debug)]
struct TrackedPromptRequest {
    model_id: String,
    priority: u32,
    input_tokens: u64,
    phase: TrackedPromptPhase,
}

#[derive(Clone, Copy, Debug, Eq, Ord, PartialEq, PartialOrd)]
enum TrackedPromptPhase {
    Pending,
    InputProcessing,
    OutputGeneration,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub(crate) struct QueueModelSnapshot {
    pub queue_size: u64,
    pub queued_input_size: u64,
    pub num_running_queries: u64,
    pub total_query_input_size: u64,
    pub input_processing_queries: u64,
    pub output_generation_queries: u64,
    pub queue_time_estimate_ms_by_priority: Option<HashMap<u32, u64>>,
}

#[derive(Clone, Debug)]
pub(crate) struct QueueTrackedRequestGuard {
    tracker: QueueAdmissionTracker,
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
            Self::MissingEstimate | Self::Disabled => None,
        }
    }

    pub(crate) fn actual_ms(&self) -> Option<u64> {
        match self {
            Self::Accepted { actual_ms, .. } | Self::Rejected { actual_ms, .. } => Some(*actual_ms),
            Self::MissingEstimate | Self::UnknownLocalEstimate { .. } | Self::Disabled => None,
        }
    }

    pub(crate) fn threshold_ms(&self) -> Option<u64> {
        match self {
            Self::Accepted { threshold_ms, .. } | Self::Rejected { threshold_ms, .. } => {
                Some(*threshold_ms)
            }
            Self::MissingEstimate | Self::UnknownLocalEstimate { .. } | Self::Disabled => None,
        }
    }
}

impl QueueAdmissionTracker {
    pub fn update_model_throughput(&self, model_id: &str, last_mean_input_tps: f64) {
        let mut state = self.inner.lock();
        if valid_last_mean_input_tps(last_mean_input_tps) {
            state
                .model_input_tps
                .insert(model_id.to_string(), last_mean_input_tps);
        } else {
            state.model_input_tps.remove(model_id);
        }
    }

    pub(crate) fn evaluate(
        &self,
        config: &PylonQueueMismatchRetryConfig,
        required: &RequiredTunnelHeaders,
        headers: &HeaderMap,
    ) -> QueueAdmissionDecision {
        if !config.enabled {
            return QueueAdmissionDecision::Disabled;
        }
        let Some(expected_ms) = parse_expected_queue_ms(headers) else {
            return QueueAdmissionDecision::MissingEstimate;
        };
        let Some(actual_ms) = self.queue_estimate_ms_for_priority_excluding(
            &required.model_id,
            required.priority,
            &required.request_id,
        ) else {
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

    pub(crate) fn track_request(
        &self,
        required: &RequiredTunnelHeaders,
    ) -> QueueTrackedRequestGuard {
        let request = TrackedPromptRequest {
            model_id: required.model_id.clone(),
            priority: required.priority,
            input_tokens: required.input_tokens,
            phase: TrackedPromptPhase::Pending,
        };
        self.inner
            .lock()
            .start_request(required.request_id.clone(), request);
        QueueTrackedRequestGuard {
            tracker: self.clone(),
            request_id: required.request_id.clone(),
            finished: false,
        }
    }

    pub(crate) fn snapshot_model(&self, model_id: &str) -> QueueModelSnapshot {
        let state = self.inner.lock();
        state.snapshot_model(model_id)
    }

    pub fn record_observation(&self, observation: &RequestObservation) {
        let mut state = self.inner.lock();
        if observation.is_terminal() {
            state.finish_request(&observation.request_id);
            return;
        }
        let phase = match observation.state {
            RequestObservationState::Queued | RequestObservationState::UpstreamConnecting => {
                TrackedPromptPhase::Pending
            }
            RequestObservationState::InputProcessing => TrackedPromptPhase::InputProcessing,
            RequestObservationState::OutputGeneration => TrackedPromptPhase::OutputGeneration,
            RequestObservationState::Complete
            | RequestObservationState::Failed
            | RequestObservationState::Cancelled => unreachable!("terminal observations returned"),
        };
        if state.finished_request_ids.contains(&observation.request_id) {
            // Terminalization and local rejection happen synchronously while
            // observations drain asynchronously. Never resurrect removed work.
            return;
        }
        if let Some(request) = state.requests.get_mut(&observation.request_id) {
            if phase < request.phase {
                return;
            }
            request.phase = phase;
        } else {
            state.requests.insert(
                observation.request_id.clone(),
                TrackedPromptRequest {
                    model_id: observation.model_id.clone(),
                    priority: observation.priority,
                    input_tokens: observation.input_tokens,
                    phase,
                },
            );
        }
    }

    fn queue_estimate_ms_for_priority_excluding(
        &self,
        model_id: &str,
        priority: u32,
        excluded_request_id: &str,
    ) -> Option<u64> {
        let state = self.inner.lock();
        state.queue_estimate_ms_for_priority_excluding(model_id, priority, excluded_request_id)
    }

    fn update_request(&self, request_id: &str, update: impl FnOnce(&mut TrackedPromptRequest)) {
        if let Some(request) = self.inner.lock().requests.get_mut(request_id) {
            update(request);
        }
    }

    pub(crate) fn remove_request_id(&self, request_id: &str) {
        self.inner.lock().finish_request(request_id);
    }

    #[cfg(test)]
    pub(crate) fn tracked_request_count(&self) -> usize {
        self.inner.lock().requests.len()
    }
}

impl QueueAdmissionState {
    fn start_request(&mut self, request_id: String, request: TrackedPromptRequest) {
        if self.finished_request_ids.remove(&request_id) {
            self.finished_request_order
                .retain(|finished| finished != &request_id);
        }
        self.requests.insert(request_id, request);
    }

    fn finish_request(&mut self, request_id: &str) {
        self.requests.remove(request_id);
        if !self.finished_request_ids.insert(request_id.to_string()) {
            return;
        }
        self.finished_request_order
            .push_back(request_id.to_string());
        while self.finished_request_order.len() > FINISHED_REQUEST_TOMBSTONE_CAPACITY {
            if let Some(expired) = self.finished_request_order.pop_front() {
                self.finished_request_ids.remove(&expired);
            }
        }
    }

    fn snapshot_model(&self, model_id: &str) -> QueueModelSnapshot {
        self.snapshot_model_excluding(model_id, None)
    }

    fn snapshot_model_excluding(
        &self,
        model_id: &str,
        excluded_request_id: Option<&str>,
    ) -> QueueModelSnapshot {
        let mut snapshot = QueueModelSnapshot::default();
        let mut active_chat_output_samples = Vec::new();
        let mut priorities = BTreeSet::new();
        let mut prompt_work_by_effective_priority = Vec::new();

        for (_request_id, request) in self.requests.iter().filter(|(request_id, request)| {
            request.model_id == model_id && Some(request_id.as_str()) != excluded_request_id
        }) {
            snapshot.num_running_queries = snapshot.num_running_queries.saturating_add(1);
            snapshot.total_query_input_size = snapshot
                .total_query_input_size
                .saturating_add(request.input_tokens);
            match request.phase {
                TrackedPromptPhase::InputProcessing => {
                    snapshot.input_processing_queries =
                        snapshot.input_processing_queries.saturating_add(1);
                }
                TrackedPromptPhase::OutputGeneration => {
                    snapshot.output_generation_queries =
                        snapshot.output_generation_queries.saturating_add(1);
                    active_chat_output_samples.push(0.0);
                }
                TrackedPromptPhase::Pending => {}
            }

            if let Some((effective_priority, remaining)) = request.prompt_work() {
                snapshot.queue_size = snapshot.queue_size.saturating_add(1);
                snapshot.queued_input_size = snapshot.queued_input_size.saturating_add(remaining);
                priorities.insert(effective_priority);
                prompt_work_by_effective_priority.push((effective_priority, remaining));
            }
        }

        let Some(last_mean_input_tps) = self.effective_model_input_tps(model_id) else {
            return snapshot;
        };

        let mut estimates = HashMap::new();
        for priority in priorities {
            let work = prompt_work_by_effective_priority
                .iter()
                .filter(|(request_priority, _)| *request_priority <= priority)
                .fold(0u64, |acc, (_, remaining)| acc.saturating_add(*remaining));
            if let Some(queue_ms) = queue_time_delta_ms(work, last_mean_input_tps) {
                estimates.insert(priority, queue_ms);
            }
        }
        // A valid model throughput and no queued prompt work is an explicit
        // empty map: downstream merges must clear previously published queues.
        snapshot.queue_time_estimate_ms_by_priority = Some(estimates);
        snapshot
    }

    fn queue_estimate_ms_for_priority_excluding(
        &self,
        model_id: &str,
        priority: u32,
        excluded_request_id: &str,
    ) -> Option<u64> {
        let snapshot = self.snapshot_model_excluding(model_id, Some(excluded_request_id));
        let valid_model_tps = self.effective_model_input_tps(model_id).is_some();
        let Some(stats) = snapshot.queue_time_estimate_ms_by_priority.as_ref() else {
            return valid_model_tps.then_some(0);
        };
        stats
            .iter()
            .filter(|(candidate_priority, _)| **candidate_priority <= priority)
            .max_by_key(|(candidate_priority, _)| **candidate_priority)
            .map(|(_, queue_time_ms)| *queue_time_ms)
            // If there is local queue work only at lower-urgency priorities, this
            // request has no prompt work ahead of it. Treat that as a known zero
            // rather than an unknown estimate so idle/high-priority requests do
            // not churn through retry.
            .or_else(|| valid_model_tps.then_some(0))
    }

    fn effective_model_input_tps(&self, model_id: &str) -> Option<f64> {
        self.model_input_tps
            .get(model_id)
            .copied()
            .filter(|value| valid_last_mean_input_tps(*value))
    }
}

impl TrackedPromptRequest {
    fn prompt_work(&self) -> Option<(u32, u64)> {
        match self.phase {
            TrackedPromptPhase::OutputGeneration => None,
            TrackedPromptPhase::Pending => Some((self.priority, self.input_tokens)),
            TrackedPromptPhase::InputProcessing => Some((0, self.input_tokens)),
        }
        .filter(|(_, remaining)| *remaining > 0)
    }
}

impl QueueTrackedRequestGuard {
    pub(crate) fn on_upstream_response_headers(&mut self) {
        self.tracker.update_request(&self.request_id, |request| {
            request.phase = TrackedPromptPhase::InputProcessing;
        });
    }

    pub(crate) fn observe_output(&mut self) {
        self.tracker.update_request(&self.request_id, |request| {
            request.phase = TrackedPromptPhase::OutputGeneration;
        });
    }

    pub(crate) fn finish(&mut self) {
        if !self.finished {
            self.tracker.remove_request_id(&self.request_id);
            self.finished = true;
        }
    }
}

impl Drop for QueueTrackedRequestGuard {
    fn drop(&mut self) {
        self.finish();
    }
}

fn parse_expected_queue_ms(headers: &HeaderMap) -> Option<u64> {
    headers
        .get(HEADER_STARGATE_EXPECTED_QUEUE_MS)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.trim().parse::<u64>().ok())
}

pub(crate) fn mismatch_threshold_ms(
    expected_ms: u64,
    config: &PylonQueueMismatchRetryConfig,
) -> u64 {
    let additive_threshold = expected_ms.saturating_add(config.min_delta_ms);
    let factor = if config.tolerance_factor.is_finite() && config.tolerance_factor > 0.0 {
        config.tolerance_factor
    } else {
        1.0
    };
    let multiplicative = ((expected_ms as f64) * factor).ceil();
    let multiplicative_threshold =
        if multiplicative.is_finite() && multiplicative <= u64::MAX as f64 {
            multiplicative as u64
        } else {
            u64::MAX
        };
    additive_threshold.max(multiplicative_threshold)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::request_observer::RequestObservationEndpoint;
    use reqwest::header::HeaderValue;
    use std::time::{Duration, Instant};

    fn required(request_id: &str, priority: u32, input_tokens: u64) -> RequiredTunnelHeaders {
        RequiredTunnelHeaders {
            request_id: request_id.to_string(),
            routing_key: None,
            model_id: "model-a".to_string(),
            priority,
            input_tokens,
            accepted_at: Instant::now(),
        }
    }

    fn headers_with_expected(expected_ms: &str) -> HeaderMap {
        let mut headers = HeaderMap::new();
        headers.insert(
            HEADER_STARGATE_EXPECTED_QUEUE_MS,
            HeaderValue::from_str(expected_ms).unwrap(),
        );
        headers
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
    fn threshold_helper_accepts_at_threshold_and_rejects_above() {
        let config = PylonQueueMismatchRetryConfig::default();
        assert_eq!(mismatch_threshold_ms(100, &config), 125);
        assert!(126 > mismatch_threshold_ms(100, &config));
        assert!(125 <= mismatch_threshold_ms(100, &config));
        assert_eq!(mismatch_threshold_ms(0, &config), 25);
    }

    #[test]
    fn tracker_publishes_cumulative_priority_queue_estimates() {
        let tracker = QueueAdmissionTracker::default();
        tracker.update_model_throughput("model-a", 100.0);
        let _priority_two = tracker.track_request(&required("req-p2", 2, 20));
        let mut priority_four = tracker.track_request(&required("req-p4", 4, 30));
        priority_four.on_upstream_response_headers();

        let snapshot = tracker.snapshot_model("model-a");

        assert_eq!(snapshot.queue_size, 2);
        assert_eq!(snapshot.queued_input_size, 50);
        assert_eq!(
            snapshot.queue_time_estimate_ms_by_priority,
            Some(HashMap::from([(0, 300), (2, 500)]))
        );
    }

    #[test]
    fn tracker_excludes_output_generation_and_drains_on_drop() {
        let tracker = QueueAdmissionTracker::default();
        tracker.update_model_throughput("model-a", 100.0);
        let mut request = tracker.track_request(&required("req-output", 2, 20));
        request.observe_output();

        let snapshot = tracker.snapshot_model("model-a");
        assert_eq!(snapshot.queue_size, 0);
        assert_eq!(snapshot.queued_input_size, 0);
        assert_eq!(snapshot.output_generation_queries, 1);

        drop(request);
        assert_eq!(tracker.tracked_request_count(), 0);
    }

    #[test]
    fn tracker_publishes_explicit_empty_priority_estimates_after_prompt_queue_drains() {
        let tracker = QueueAdmissionTracker::default();
        tracker.update_model_throughput("model-a", 100.0);
        let request = tracker.track_request(&required("req-drained", 2, 20));

        assert_eq!(
            tracker
                .snapshot_model("model-a")
                .queue_time_estimate_ms_by_priority,
            Some(HashMap::from([(2, 200)]))
        );

        drop(request);
        assert_eq!(
            tracker
                .snapshot_model("model-a")
                .queue_time_estimate_ms_by_priority,
            Some(HashMap::new())
        );
    }

    #[test]
    fn delayed_nonterminal_observation_does_not_resurrect_finished_request() {
        let tracker = QueueAdmissionTracker::default();
        let mut request = tracker.track_request(&required("req-finished", 0, 100));
        request.finish();

        tracker.record_observation(&observation(
            "req-finished",
            RequestObservationState::UpstreamConnecting,
        ));

        assert_eq!(tracker.tracked_request_count(), 0);
    }

    #[test]
    fn delayed_earlier_observation_does_not_requeue_output_generation() {
        let tracker = QueueAdmissionTracker::default();
        let mut request = tracker.track_request(&required("req-output", 0, 100));
        request.observe_output();

        tracker.record_observation(&observation(
            "req-output",
            RequestObservationState::UpstreamConnecting,
        ));

        let snapshot = tracker.snapshot_model("model-a");
        assert_eq!(snapshot.queue_size, 0);
        assert_eq!(snapshot.output_generation_queries, 1);
    }

    #[test]
    fn upstream_response_headers_do_not_apply_retired_progress_contract() {
        let tracker = QueueAdmissionTracker::default();
        let mut request = tracker.track_request(&required("req-progress", 0, 100));

        request.on_upstream_response_headers();

        let snapshot = tracker.snapshot_model("model-a");
        assert_eq!(snapshot.queued_input_size, 100);
        assert_eq!(snapshot.input_processing_queries, 1);
    }

    #[test]
    fn admission_transitions_from_unknown_to_rejected_after_throughput_update() {
        let tracker = QueueAdmissionTracker::default();
        let config = PylonQueueMismatchRetryConfig::default();
        let request = required("req-inflight", 0, 100);
        let _guard = tracker.track_request(&request);
        let incoming = required("req-new", 0, 1);
        let headers = headers_with_expected("0");

        assert_eq!(
            tracker.evaluate(&config, &incoming, &headers),
            QueueAdmissionDecision::UnknownLocalEstimate { expected_ms: 0 }
        );

        tracker.update_model_throughput("model-a", 100.0);
        assert_eq!(
            tracker.evaluate(&config, &incoming, &headers),
            QueueAdmissionDecision::Rejected {
                expected_ms: 0,
                actual_ms: 1000,
                threshold_ms: 25,
                retry_after_ms: None,
            }
        );
    }

    #[test]
    fn admission_excludes_current_request_even_when_observed_before_evaluation() {
        let tracker = QueueAdmissionTracker::default();
        let config = PylonQueueMismatchRetryConfig::default();
        let current = required("req-current", 0, 10);
        let _guard = tracker.track_request(&current);
        tracker.update_model_throughput("model-a", 100.0);

        assert_eq!(
            tracker.evaluate(&config, &current, &headers_with_expected("0")),
            QueueAdmissionDecision::Accepted {
                expected_ms: 0,
                actual_ms: 0,
                threshold_ms: 25,
            }
        );
    }

    #[test]
    fn disabled_admission_accepts_with_distinct_metric_label() {
        let tracker = QueueAdmissionTracker::default();
        tracker.update_model_throughput("model-a", 100.0);
        let inflight = required("req-inflight", 0, 100);
        let _guard = tracker.track_request(&inflight);
        let config = PylonQueueMismatchRetryConfig {
            enabled: false,
            ..PylonQueueMismatchRetryConfig::default()
        };

        let decision = tracker.evaluate(
            &config,
            &required("req-new", 0, 1),
            &headers_with_expected("0"),
        );
        assert_eq!(decision, QueueAdmissionDecision::Disabled);
        assert_eq!(decision.result_label(), "disabled");
    }

    #[test]
    fn admission_accepts_missing_or_invalid_expected_queue_header() {
        let tracker = QueueAdmissionTracker::default();
        let config = PylonQueueMismatchRetryConfig::default();
        let incoming = required("req-new", 0, 1);
        assert_eq!(
            tracker.evaluate(&config, &incoming, &HeaderMap::new()),
            QueueAdmissionDecision::MissingEstimate
        );
        assert_eq!(
            tracker.evaluate(&config, &incoming, &headers_with_expected("nope")),
            QueueAdmissionDecision::MissingEstimate
        );
    }
}

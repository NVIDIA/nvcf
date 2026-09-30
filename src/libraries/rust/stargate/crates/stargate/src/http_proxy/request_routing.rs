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

use std::collections::HashSet;
use std::sync::Arc;
use std::time::{Duration, Instant};

use rand::Rng;
use tracing::Span;

use crate::load_balancer::{
    LoadBalancerAlgorithmConfig, LoadBalancerAlgorithmResolution, LoadBalancerCandidateSelection,
    LoadBalancerDecision, LoadBalancerRequest, LoadBalancerRouter, input_work_seconds_for_request,
};
use crate::metrics::StargateMetrics;
use crate::routing_state::{
    RoutedClusterSnapshot, RoutedInferenceServerSnapshot, RoutingTargetKey, RoutingTargetSnapshot,
    SelectedRoutedCluster, StargateState,
};

use super::request::ProxyRequestInputs;
use super::trace::{RoutingTraceFields, record_routing_to_span};

const ADMISSION_REASON_INPUT_WORK_LIMIT_EXCEEDED: &str = "input_work_limit_exceeded";
const ADMISSION_REASON_INPUT_WORK_CAPACITY_UNAVAILABLE: &str = "input_work_capacity_unavailable";
const ROUTING_RETRY_SLEEP_MIN_MS: u64 = 1;
const ROUTING_RETRY_SLEEP_MAX_MS: u64 = 10;
const ROUTING_RETRY_MAX_WAIT_MS: u64 = 60_000;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum RoutingRejection {
    Admission(&'static str),
    NoCandidatesNotFound,
    ServiceUnavailable,
}

pub(super) struct RequestRouting<'a> {
    state: &'a StargateState,
    router: &'a LoadBalancerRouter,
    metrics: &'a StargateMetrics,
    inputs: &'a ProxyRequestInputs,
    resolution: &'a LoadBalancerAlgorithmResolution,
    request_start: Instant,
    routing_started_at: Option<Instant>,
    retry_deadline: Option<Instant>,
    wait_deadline: Instant,
    retry_attempts: u64,
    failed_backend_ids: HashSet<String>,
    failed_cluster_ids: HashSet<String>,
    selected: Option<SelectedClusterRun>,
}

impl<'a> RequestRouting<'a> {
    pub(super) fn new(
        state: &'a StargateState,
        router: &'a LoadBalancerRouter,
        metrics: &'a StargateMetrics,
        inputs: &'a ProxyRequestInputs,
        resolution: &'a LoadBalancerAlgorithmResolution,
        request_start: Instant,
        routing_started_at: Instant,
    ) -> Self {
        Self {
            state,
            router,
            metrics,
            inputs,
            resolution,
            request_start,
            routing_started_at: Some(routing_started_at),
            retry_deadline: routing_retry_deadline(request_start, inputs.max_wait_ms),
            wait_deadline: routing_wait_deadline(request_start, inputs.max_wait_ms),
            retry_attempts: 0,
            failed_backend_ids: HashSet::new(),
            failed_cluster_ids: HashSet::new(),
            selected: None,
        }
    }

    pub(super) async fn next(
        &mut self,
    ) -> Result<(&SelectedClusterRun, &Arc<RoutedInferenceServerSnapshot>), RoutingRejection> {
        self.prepare_selection().await?;
        if let Some(started_at) = self.routing_started_at.take() {
            self.metrics
                .routing_duration_seconds(
                    self.inputs.target.routing_key.as_deref(),
                    &self.inputs.target.model_id,
                )
                .observe(started_at.elapsed().as_secs_f64());
        }
        let selected = self.selected.as_ref().expect("routing selected a cluster");
        let backend = selected
            .backend
            .as_ref()
            .expect("routing selected a backend");
        let span = Span::current();
        span.record("routing.retry_attempts", self.retry_attempts);
        record_routing_to_span(
            &span,
            RoutingTraceFields {
                routing_algorithm: &selected.routing_algorithm,
                requested_algorithm: selected.selection.requested_algorithm.as_deref(),
                num_candidates: selected.num_candidates,
                rank_depth: selected.selection.choice.rank_depth,
                selected_after_kv_free_tokens_skip: selected
                    .selection
                    .choice
                    .selected_after_kv_free_tokens_skip,
                comparator: self.resolution.config().comparator(),
                cluster: selected.cluster.snapshot(),
                chosen: backend,
            },
        );
        Ok((selected, backend))
    }

    pub(super) fn exclude_backend(&mut self, inference_server_id: String) {
        self.failed_backend_ids.insert(inference_server_id);
        if let Some(selected) = &mut self.selected {
            selected.backend = None;
        }
    }

    pub(super) fn exclude_cluster(&mut self, cluster_id: String) {
        self.failed_cluster_ids.insert(cluster_id);
        self.selected = None;
    }

    pub(super) fn failed_backend_count(&self) -> usize {
        self.failed_backend_ids.len()
    }

    pub(super) fn failed_cluster_count(&self) -> usize {
        self.failed_cluster_ids.len()
    }

    pub(super) fn retry_attempts(&self) -> u64 {
        self.retry_attempts
    }

    fn load_balancer_request(&self) -> LoadBalancerRequest<'_> {
        LoadBalancerRequest {
            routing_target: &self.inputs.target,
            cache_affinity_key: self.inputs.cache_affinity_key.as_deref(),
            input_tokens: Some(self.inputs.input_tokens),
            priority: self.inputs.priority,
            received_at: self.request_start,
            request_slo: self.inputs.request_slo_ms.map(Duration::from_millis),
            excluded_cluster_ids: (!self.failed_cluster_ids.is_empty())
                .then_some(&self.failed_cluster_ids),
        }
    }

    async fn prepare_selection(&mut self) -> Result<(), RoutingRejection> {
        loop {
            if let Some(selected) = &mut self.selected {
                if selected.backend.is_none() {
                    selected.backend = selected.cluster.select_backend(&self.failed_backend_ids);
                }
                if selected.backend.is_some() {
                    return Ok(());
                }
                self.failed_cluster_ids
                    .insert(selected.cluster.snapshot().cluster_id.clone());
                self.selected = None;
            }

            let snapshot = self
                .state
                .routing_target_snapshot(&self.inputs.target)
                .await;
            let candidates = snapshot
                .as_ref()
                .map_or(&[][..], |snapshot| snapshot.clusters());
            let num_candidates = candidates.len();
            if candidates.is_empty()
                || (!self.failed_cluster_ids.is_empty()
                    && candidates
                        .iter()
                        .all(|candidate| self.failed_cluster_ids.contains(&candidate.cluster_id)))
            {
                return Err(self.no_choice(num_candidates));
            }

            let request = self.load_balancer_request();
            if let Some(limit) = self.resolution.config().max_input_work_seconds
                && let Some(reason) = input_work_admission_rejection_reason(
                    self.resolution.config(),
                    &request,
                    candidates,
                    limit,
                )
            {
                return Err(RoutingRejection::Admission(reason));
            }
            let snapshot = snapshot.expect("eligible candidates have a routing snapshot");
            let decision = self.router.decide_with_algorithm_resolution(
                snapshot.load_balancers(),
                &request,
                snapshot.clusters(),
                self.resolution,
            );
            if let LoadBalancerDecision::Selected(choice) = decision {
                let mut selected = SelectedClusterRun::new(
                    snapshot,
                    self.resolution.selection(choice),
                    self.inputs.priority,
                );
                selected.backend = selected.cluster.select_backend(&self.failed_backend_ids);
                if selected.backend.is_some() {
                    selected.record_selection_metrics(self.metrics, &self.inputs.target);
                    self.selected = Some(selected);
                    return Ok(());
                }
                self.failed_cluster_ids
                    .insert(selected.cluster.snapshot().cluster_id.clone());
                continue;
            }
            // A later evaluation must see current membership and projected reservations.
            drop(snapshot);
            if let LoadBalancerDecision::Wait(remaining) = decision
                && let Some(delay) =
                    routing_wait_delay(remaining, Some(self.wait_deadline), Instant::now())
            {
                self.record_routing_retry();
                tracing::debug!(
                    remaining_ms = remaining.as_millis() as u64,
                    "waiting for routing bucket eligibility"
                );
                tokio::time::sleep(delay).await;
            } else if should_retry_routing(self.retry_deadline) {
                self.record_routing_retry();
                sleep_before_routing_retry(self.retry_deadline).await;
            } else {
                return Err(self.no_choice(num_candidates));
            }
        }
    }

    fn no_choice(&self, num_candidates: usize) -> RoutingRejection {
        if num_candidates == 0
            && !self
                .state
                .has_registered_model_for_target(&self.inputs.target)
            && self.failed_backend_ids.is_empty()
            && self.failed_cluster_ids.is_empty()
        {
            RoutingRejection::NoCandidatesNotFound
        } else {
            RoutingRejection::ServiceUnavailable
        }
    }

    fn record_routing_retry(&mut self) {
        self.retry_attempts += 1;
        Span::current().record("routing.retry_attempts", self.retry_attempts);
    }
}

pub(super) struct SelectedClusterRun {
    backend: Option<Arc<RoutedInferenceServerSnapshot>>,
    pub(super) cluster: SelectedRoutedCluster,
    pub(super) routing_algorithm: String,
    pub(super) selection: LoadBalancerCandidateSelection,
    num_candidates: usize,
    pub(super) expected_queue_ms: Option<u64>,
}

impl SelectedClusterRun {
    fn new(
        target_snapshot: RoutingTargetSnapshot,
        selection: LoadBalancerCandidateSelection,
        priority: u32,
    ) -> Self {
        let num_candidates = target_snapshot.clusters().len();
        let cluster = target_snapshot.into_selected_cluster(selection.choice.candidate_index);
        let expected_queue_ms = crate::queue_estimate::queue_time_estimate_ms_for_priority(
            &cluster.snapshot().stats,
            priority,
        );
        Self {
            backend: None,
            cluster,
            routing_algorithm: selection.effective_algorithm.to_string(),
            selection,
            num_candidates,
            expected_queue_ms,
        }
    }

    fn record_selection_metrics(&self, metrics: &StargateMetrics, target: &RoutingTargetKey) {
        let selection_class = if self.selection.choice.rank_depth > 1 {
            "fallback"
        } else {
            "primary"
        };
        metrics
            .routing_selections_total(
                target.routing_key.as_deref(),
                &target.model_id,
                &self.routing_algorithm,
                selection_class,
            )
            .inc();
        if self.selection.choice.selected_after_kv_free_tokens_skip {
            metrics
                .routing_kv_free_token_fallback_selections_total(
                    target.routing_key.as_deref(),
                    &target.model_id,
                    &self.routing_algorithm,
                )
                .inc();
        }
    }
}

fn input_work_admission_rejection_reason(
    config: &LoadBalancerAlgorithmConfig,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    limit_seconds: f64,
) -> Option<&'static str> {
    match input_work_seconds_for_request(config, request, candidates) {
        Some(seconds) if seconds <= limit_seconds => None,
        Some(_) => Some(ADMISSION_REASON_INPUT_WORK_LIMIT_EXCEEDED),
        None => Some(ADMISSION_REASON_INPUT_WORK_CAPACITY_UNAVAILABLE),
    }
}

fn should_retry_routing(deadline: Option<Instant>) -> bool {
    deadline.is_some_and(|deadline| Instant::now() < deadline)
}

fn routing_retry_deadline(request_start: Instant, max_wait_ms: Option<u64>) -> Option<Instant> {
    max_wait_ms.and_then(|wait_ms| {
        request_start.checked_add(Duration::from_millis(
            wait_ms.min(ROUTING_RETRY_MAX_WAIT_MS),
        ))
    })
}

/// Deadline for timed load-balancer waits. Unlike generic routing retries,
/// bucket waits do not require `x-max-wait-ms`, but they stay bounded.
fn routing_wait_deadline(request_start: Instant, max_wait_ms: Option<u64>) -> Instant {
    let wait_ms = max_wait_ms.map_or(ROUTING_RETRY_MAX_WAIT_MS, |wait_ms| {
        wait_ms.min(ROUTING_RETRY_MAX_WAIT_MS)
    });
    request_start + Duration::from_millis(wait_ms)
}

fn routing_wait_delay(
    remaining: Duration,
    deadline: Option<Instant>,
    now: Instant,
) -> Option<Duration> {
    // Recheck capacity while waiting for the next routing phase or bucket.
    // Timed waits do not need the 1-10 ms generic capacity retry loop.
    let delay = remaining.min(Duration::from_millis(25));
    let delay = deadline.map_or(delay, |deadline| {
        delay.min(deadline.saturating_duration_since(now))
    });
    (!delay.is_zero()).then_some(delay)
}

async fn sleep_before_routing_retry(deadline: Option<Instant>) {
    // The retry deadline may pass between checks; clamp elapsed deadlines to no sleep.
    let remaining = deadline.map_or(Duration::ZERO, |deadline| {
        deadline.saturating_duration_since(Instant::now())
    });
    if remaining.is_zero() {
        return;
    }
    let random_sleep_ms =
        rand::rng().random_range(ROUTING_RETRY_SLEEP_MIN_MS..ROUTING_RETRY_SLEEP_MAX_MS);
    tokio::time::sleep(remaining.min(Duration::from_millis(random_sleep_ms))).await;
}

#[cfg(test)]
mod tests;

#[cfg(test)]
mod policy_tests {
    use stargate_proto::pb::{InferenceServerStatus, ModelStats};

    use super::*;
    use crate::load_balancer::LoadBalancerAlgorithm;

    #[test]
    fn affinity_wait_rechecks_capacity_and_respects_explicit_deadline() {
        let now = Instant::now();
        assert_eq!(
            routing_wait_delay(Duration::from_millis(100), None, now),
            Some(Duration::from_millis(25))
        );
        assert_eq!(
            routing_wait_delay(Duration::from_millis(5), None, now),
            Some(Duration::from_millis(5))
        );
        assert_eq!(
            routing_wait_delay(
                Duration::from_millis(100),
                Some(now + Duration::from_millis(10)),
                now
            ),
            Some(Duration::from_millis(10))
        );
        assert_eq!(
            routing_wait_delay(Duration::from_millis(100), Some(now), now),
            None
        );
        assert_eq!(routing_wait_delay(Duration::ZERO, None, now), None);
    }

    fn cluster_candidate(cluster_id: &str) -> RoutedClusterSnapshot {
        RoutedClusterSnapshot {
            cluster_id: cluster_id.to_string(),
            stats: ModelStats::default(),
            rtt: Duration::from_millis(1),
            snapshot_updated_at: Instant::now(),
            status: InferenceServerStatus::Active,
            active_backend_count: 1,
        }
    }

    fn input_work_admission_request<'a>(
        target: &'a RoutingTargetKey,
        input_tokens: u64,
    ) -> LoadBalancerRequest<'a> {
        LoadBalancerRequest {
            routing_target: target,
            cache_affinity_key: Some("cache-key-a"),
            input_tokens: Some(input_tokens),
            priority: 0,
            received_at: Instant::now(),
            request_slo: None,
            excluded_cluster_ids: None,
        }
    }

    fn routing_target() -> RoutingTargetKey {
        RoutingTargetKey::new(None, "model-a")
    }

    #[test]
    fn input_work_admission_rejects_overloaded_pool() {
        let mut candidate = cluster_candidate("cluster-a");
        candidate.stats.queued_input_size = 300;
        candidate.stats.last_mean_input_tps = 100.0;
        let config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PowerOfN);
        let target = routing_target();
        let request = input_work_admission_request(&target, 50);

        assert_eq!(
            input_work_admission_rejection_reason(&config, &request, &[candidate], 3.0),
            Some(ADMISSION_REASON_INPUT_WORK_LIMIT_EXCEEDED)
        );
    }

    #[test]
    fn input_work_admission_ignores_decode_only_total_query_input_size() {
        let mut candidate = cluster_candidate("cluster-a");
        candidate.stats.total_query_input_size = 300;
        candidate.stats.queued_input_size = 0;
        candidate.stats.last_mean_input_tps = 100.0;
        let config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PowerOfN);
        let target = routing_target();
        let request = input_work_admission_request(&target, 50);

        assert_eq!(
            input_work_admission_rejection_reason(&config, &request, &[candidate], 3.0),
            None
        );
    }

    #[test]
    fn input_work_admission_rejects_pool_without_valid_capacity() {
        let mut candidate = cluster_candidate("cluster-a");
        candidate.stats.last_mean_input_tps = 0.0;
        let config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PowerOfN);
        let target = routing_target();
        let request = input_work_admission_request(&target, 50);

        assert_eq!(
            input_work_admission_rejection_reason(&config, &request, &[candidate], 3.0),
            Some(ADMISSION_REASON_INPUT_WORK_CAPACITY_UNAVAILABLE)
        );
    }

    #[test]
    fn routing_wait_deadline_is_bounded_without_max_wait_header() {
        let request_start = Instant::now();
        let cap = request_start + Duration::from_millis(ROUTING_RETRY_MAX_WAIT_MS);
        assert_eq!(routing_wait_deadline(request_start, None), cap);
        assert_eq!(routing_wait_deadline(request_start, Some(u64::MAX)), cap);
        assert_eq!(
            routing_wait_deadline(request_start, Some(250)),
            request_start + Duration::from_millis(250)
        );
    }

    #[test]
    fn routing_wait_stops_at_default_deadline_without_max_wait_header() {
        let request_start = Instant::now();
        let deadline = routing_wait_deadline(request_start, None);
        let one_hour = Duration::from_secs(3600);
        assert_eq!(
            routing_wait_delay(one_hour, Some(deadline), request_start),
            Some(Duration::from_millis(25))
        );
        assert_eq!(
            routing_wait_delay(
                one_hour,
                Some(deadline),
                deadline - Duration::from_millis(5)
            ),
            Some(Duration::from_millis(5))
        );
        assert_eq!(routing_wait_delay(one_hour, Some(deadline), deadline), None);
        // Generic capacity retries still require an explicit header budget.
        assert!(!should_retry_routing(routing_retry_deadline(
            request_start,
            None
        )));
    }

    #[test]
    fn routing_retry_deadline_caps_max_wait_header() {
        let request_start = Instant::now();
        let deadline = routing_retry_deadline(request_start, Some(u64::MAX))
            .expect("capped deadline should be computed");
        assert!(deadline <= request_start + Duration::from_millis(ROUTING_RETRY_MAX_WAIT_MS));
    }
}

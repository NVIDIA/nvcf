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
use std::time::{Duration, Instant};

use axum::body::Body;
use axum::http::{HeaderName, HeaderValue, StatusCode, header};
use axum::response::Response;
use rand::Rng;
use tracing::{Span, warn};

use crate::load_balancer::{
    LoadBalancerAlgorithmConfig, LoadBalancerRequest, input_work_seconds_for_request,
};
use crate::metrics::StargateMetrics;
use crate::routing_state::{RoutedClusterSnapshot, RoutingTargetKey};

use super::HEADER_STARGATE_ERROR_CODE;

const ERROR_NO_ELIGIBLE_CANDIDATES: &str = "no_eligible_candidates";
const ERROR_NO_ELIGIBLE_CANDIDATES_BODY: &str =
    r#"{"error":"no eligible candidates","code":"no_eligible_candidates"}"#;
const ERROR_INPUT_WORK_LIMIT_EXCEEDED: &str = "input_work_limit_exceeded";
const ERROR_INPUT_WORK_LIMIT_EXCEEDED_BODY: &str =
    r#"{"error":"input work admission limit exceeded","code":"input_work_limit_exceeded"}"#;
const ADMISSION_REASON_INPUT_WORK_LIMIT_EXCEEDED: &str = "input_work_limit_exceeded";
const ADMISSION_REASON_INPUT_WORK_CAPACITY_UNAVAILABLE: &str = "input_work_capacity_unavailable";
const ROUTING_RETRY_SLEEP_MIN_MS: u64 = 1;
const ROUTING_RETRY_SLEEP_MAX_MS: u64 = 10;
const ROUTING_RETRY_MAX_WAIT_MS: u64 = 60_000;

pub(super) fn eligible_cluster_candidate_count(
    candidates: &[RoutedClusterSnapshot],
    excluded_cluster_ids: Option<&HashSet<String>>,
) -> usize {
    excluded_cluster_ids.map_or(candidates.len(), |excluded_cluster_ids| {
        candidates
            .iter()
            .filter(|candidate| !excluded_cluster_ids.contains(&candidate.cluster_id))
            .count()
    })
}

pub(super) fn input_work_admission_rejection_reason(
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

pub(super) fn input_work_admission_rejection_response(
    metrics: &StargateMetrics,
    target: &RoutingTargetKey,
    reason: &'static str,
) -> Response<Body> {
    let rk_ref = target.routing_key.as_deref();
    let model_id = target.model_id.as_str();
    Span::current().record("routing.admission_rejection_reason", reason);
    metrics
        .admission_rejections_total(rk_ref, model_id, reason)
        .inc();
    metrics.requests_total(rk_ref, model_id, "", "503").inc();
    warn!(
        routing_key = ?target.routing_key,
        model_id = %model_id,
        rejection_reason = reason,
        "rejecting request before routing due to input-work admission"
    );

    json_error_response(
        StatusCode::SERVICE_UNAVAILABLE,
        ERROR_INPUT_WORK_LIMIT_EXCEEDED,
        ERROR_INPUT_WORK_LIMIT_EXCEEDED_BODY,
    )
}

#[derive(Debug, Clone, Copy)]
pub(super) struct NoRoutingChoiceInputs {
    pub(super) num_candidates: usize,
    pub(super) eligible_candidate_count: usize,
    pub(super) target_registered: bool,
    pub(super) failed_backend_count: usize,
    pub(super) failed_cluster_count: usize,
    pub(super) retry_allowed: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum NoRoutingChoiceAction {
    RetryRouting,
    Finalize(NoRoutingFinalization),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(super) enum NoRoutingFinalization {
    NoCandidatesNotFound,
    ServiceUnavailable,
}

pub(super) fn classify_no_routing_choice(inputs: NoRoutingChoiceInputs) -> NoRoutingChoiceAction {
    if inputs.num_candidates > 0 && inputs.eligible_candidate_count > 0 && inputs.retry_allowed {
        NoRoutingChoiceAction::RetryRouting
    } else if inputs.num_candidates == 0
        && !inputs.target_registered
        && inputs.failed_backend_count == 0
        && inputs.failed_cluster_count == 0
    {
        NoRoutingChoiceAction::Finalize(NoRoutingFinalization::NoCandidatesNotFound)
    } else {
        NoRoutingChoiceAction::Finalize(NoRoutingFinalization::ServiceUnavailable)
    }
}

pub(super) struct NoRoutingFinalizationContext<'a> {
    pub(super) metrics: &'a StargateMetrics,
    pub(super) target: &'a RoutingTargetKey,
    pub(super) finalization: NoRoutingFinalization,
    pub(super) failed_backend_count: usize,
    pub(super) failed_cluster_count: usize,
    pub(super) routing_retry_attempts: u64,
}

pub(super) fn finalize_no_routing_choice(
    context: NoRoutingFinalizationContext<'_>,
) -> Result<Response<Body>, StatusCode> {
    let rk_ref = context.target.routing_key.as_deref();
    let model_id = context.target.model_id.as_str();
    if context.failed_backend_count > 0 || context.failed_cluster_count > 0 {
        context
            .metrics
            .proxy_retry_exhausted_total(rk_ref, model_id, "no_eligible_backend")
            .inc();
        Span::current().record("proxy.retry_reason", "no_eligible_backend");
    }
    warn!(
        routing_key = ?context.target.routing_key,
        model_id = %model_id,
        failed_backend_count = context.failed_backend_count,
        routing_retry_attempts = context.routing_retry_attempts,
        "no inference server candidates for routing target"
    );

    match context.finalization {
        NoRoutingFinalization::NoCandidatesNotFound => {
            context
                .metrics
                .requests_total(rk_ref, model_id, "", "404")
                .inc();
            Ok(no_eligible_candidates_response())
        }
        NoRoutingFinalization::ServiceUnavailable => {
            context
                .metrics
                .requests_total(rk_ref, model_id, "", "503")
                .inc();
            Err(StatusCode::SERVICE_UNAVAILABLE)
        }
    }
}

pub(super) fn should_retry_routing(deadline: Option<Instant>) -> bool {
    deadline.is_some_and(|deadline| Instant::now() < deadline)
}

pub(super) fn routing_retry_deadline(
    request_start: Instant,
    max_wait_ms: Option<u64>,
) -> Option<Instant> {
    max_wait_ms.and_then(|wait_ms| {
        request_start.checked_add(Duration::from_millis(
            wait_ms.min(ROUTING_RETRY_MAX_WAIT_MS),
        ))
    })
}

pub(super) async fn sleep_before_routing_retry(deadline: Option<Instant>) {
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

fn no_eligible_candidates_response() -> Response<Body> {
    json_error_response(
        StatusCode::NOT_FOUND,
        ERROR_NO_ELIGIBLE_CANDIDATES,
        ERROR_NO_ELIGIBLE_CANDIDATES_BODY,
    )
}

fn json_error_response(
    status: StatusCode,
    error_code: &'static str,
    body: &'static str,
) -> Response<Body> {
    let mut response = Response::new(Body::from(body));
    *response.status_mut() = status;
    response.headers_mut().insert(
        HeaderName::from_static(HEADER_STARGATE_ERROR_CODE),
        HeaderValue::from_static(error_code),
    );
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json"),
    );
    response
}

#[cfg(test)]
mod tests {
    use stargate_proto::pb::{InferenceServerStatus, ModelStats};

    use super::*;
    use crate::load_balancer::LoadBalancerAlgorithm;

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

    fn no_routing_inputs(
        num_candidates: usize,
        eligible_candidate_count: usize,
    ) -> NoRoutingChoiceInputs {
        NoRoutingChoiceInputs {
            num_candidates,
            eligible_candidate_count,
            target_registered: false,
            failed_backend_count: 0,
            failed_cluster_count: 0,
            retry_allowed: true,
        }
    }

    #[test]
    fn input_work_admission_rejects_overloaded_pool() {
        let mut candidate = cluster_candidate("cluster-a");
        candidate.stats.queued_input_size = 300;
        candidate.stats.last_mean_input_tps = 100.0;
        let config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PowerOfTwo);
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
        let config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PowerOfTwo);
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
        let config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PowerOfTwo);
        let target = routing_target();
        let request = input_work_admission_request(&target, 50);

        assert_eq!(
            input_work_admission_rejection_reason(&config, &request, &[candidate], 3.0),
            Some(ADMISSION_REASON_INPUT_WORK_CAPACITY_UNAVAILABLE)
        );
    }

    #[test]
    fn no_routing_choice_retries_only_with_eligible_candidates_and_budget() {
        assert_eq!(
            classify_no_routing_choice(no_routing_inputs(2, 1)),
            NoRoutingChoiceAction::RetryRouting
        );
        assert_eq!(
            classify_no_routing_choice(NoRoutingChoiceInputs {
                failed_backend_count: 1,
                failed_cluster_count: 1,
                ..no_routing_inputs(2, 0)
            }),
            NoRoutingChoiceAction::Finalize(NoRoutingFinalization::ServiceUnavailable)
        );
        assert_eq!(
            classify_no_routing_choice(NoRoutingChoiceInputs {
                retry_allowed: false,
                ..no_routing_inputs(2, 1)
            }),
            NoRoutingChoiceAction::Finalize(NoRoutingFinalization::ServiceUnavailable)
        );
    }

    #[test]
    fn no_routing_choice_finalizes_empty_route_as_not_found() {
        assert_eq!(
            classify_no_routing_choice(no_routing_inputs(0, 0)),
            NoRoutingChoiceAction::Finalize(NoRoutingFinalization::NoCandidatesNotFound)
        );
    }

    #[test]
    fn no_routing_choice_finalizes_registered_empty_route_as_unavailable() {
        assert_eq!(
            classify_no_routing_choice(NoRoutingChoiceInputs {
                target_registered: true,
                ..no_routing_inputs(0, 0)
            }),
            NoRoutingChoiceAction::Finalize(NoRoutingFinalization::ServiceUnavailable)
        );
    }

    #[test]
    fn no_routing_choice_finalizes_failed_empty_route_as_unavailable() {
        assert_eq!(
            classify_no_routing_choice(NoRoutingChoiceInputs {
                failed_backend_count: 1,
                ..no_routing_inputs(0, 0)
            }),
            NoRoutingChoiceAction::Finalize(NoRoutingFinalization::ServiceUnavailable)
        );
    }

    #[test]
    fn eligible_cluster_candidate_count_uses_len_without_exclusions() {
        let candidates = vec![
            cluster_candidate("cluster-a"),
            cluster_candidate("cluster-b"),
            cluster_candidate("cluster-c"),
        ];

        assert_eq!(eligible_cluster_candidate_count(&candidates, None), 3);
    }

    #[test]
    fn eligible_cluster_candidate_count_filters_excluded_clusters() {
        let candidates = vec![
            cluster_candidate("cluster-a"),
            cluster_candidate("cluster-b"),
        ];
        let excluded = HashSet::from(["cluster-a".to_string()]);

        assert_eq!(
            eligible_cluster_candidate_count(&candidates, Some(&excluded)),
            1
        );
    }

    #[test]
    fn routing_retry_deadline_caps_max_wait_header() {
        let request_start = Instant::now();
        let deadline = routing_retry_deadline(request_start, Some(u64::MAX))
            .expect("capped deadline should be computed");
        assert!(deadline <= request_start + Duration::from_millis(ROUTING_RETRY_MAX_WAIT_MS));
    }
}

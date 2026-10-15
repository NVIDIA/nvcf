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

use axum::body::Body;
use axum::http::{HeaderMap, Method, StatusCode};
use axum::response::Response;
use tracing::Span;

use crate::load_balancer::{
    LoadBalancerAlgorithmResolution, LoadBalancerCandidateSelection, LoadBalancerRequest,
};
use crate::metrics::StargateMetrics;
use crate::routing_state::{
    RoutedInferenceServerSnapshot, RoutingTargetKey, RoutingTargetSnapshot, SelectedRoutedCluster,
};

use super::ProxyAppState;
use super::attempt::{ProxyAttemptCounters, ProxyAttemptOutcome};
use super::request::ProxyRequestInputs;
use super::retry::ReplayableRequestBody;
use super::routing::{
    NoRoutingChoiceAction, NoRoutingChoiceInputs, NoRoutingFinalizationContext,
    classify_no_routing_choice, eligible_cluster_candidate_count, finalize_no_routing_choice,
    input_work_admission_rejection_reason, input_work_admission_rejection_response,
    routing_retry_deadline, should_retry_routing, sleep_before_routing_retry,
};
use super::trace::{RoutingTraceFields, record_routing_to_span};

pub(super) struct PreparedProxyRequest {
    pub(super) request_inputs: ProxyRequestInputs,
    pub(super) lb_resolution: LoadBalancerAlgorithmResolution,
    pub(super) endpoint_name: &'static str,
    pub(super) method: Method,
    pub(super) path_and_query: String,
    pub(super) forwarded_headers: HeaderMap,
    pub(super) retry_deadline: Option<Instant>,
    pub(super) request_start: Instant,
    pub(super) replay_body: ReplayableRequestBody,
}

pub(super) struct ProxyRequestRun<'a> {
    pub(super) app: &'a ProxyAppState,
    pub(super) request: PreparedProxyRequest,
    routing_started_at: Option<Instant>,
    routing_retry_deadline: Option<Instant>,
    routing_retry_attempts: u64,
    pub(super) failed_backend_ids: HashSet<String>,
    failed_cluster_ids: HashSet<String>,
    pub(super) attempt_counters: ProxyAttemptCounters,
}

impl<'a> ProxyRequestRun<'a> {
    pub(super) fn new(app: &'a ProxyAppState, request: PreparedProxyRequest) -> Self {
        let routing_retry_deadline =
            routing_retry_deadline(request.request_start, request.request_inputs.max_wait_ms);
        Self {
            app,
            request,
            routing_started_at: Some(Instant::now()),
            routing_retry_deadline,
            routing_retry_attempts: 0,
            failed_backend_ids: HashSet::new(),
            failed_cluster_ids: HashSet::new(),
            attempt_counters: ProxyAttemptCounters::default(),
        }
    }

    pub(super) async fn execute(mut self) -> Result<Response<Body>, StatusCode> {
        loop {
            if let Some(response) = self.run_routing_attempt().await {
                return response;
            }
        }
    }

    fn excluded_cluster_ids(&self) -> Option<&HashSet<String>> {
        (!self.failed_cluster_ids.is_empty()).then_some(&self.failed_cluster_ids)
    }

    fn load_balancer_request(&self) -> LoadBalancerRequest<'_> {
        let request_inputs = &self.request.request_inputs;
        LoadBalancerRequest {
            routing_target: &request_inputs.target,
            cache_affinity_key: request_inputs.cache_affinity_key.as_deref(),
            input_tokens: Some(request_inputs.input_tokens),
            priority: request_inputs.priority,
            received_at: self.request.request_start,
            request_slo: request_inputs.request_slo_ms.map(Duration::from_millis),
            excluded_cluster_ids: self.excluded_cluster_ids(),
        }
    }

    async fn run_routing_attempt(&mut self) -> Option<Result<Response<Body>, StatusCode>> {
        let target_snapshot = self
            .app
            .state
            .routing_target_snapshot(&self.request.request_inputs.target)
            .await;
        let candidates = target_snapshot
            .as_ref()
            .map(|snapshot| snapshot.clusters())
            .unwrap_or_default();
        let num_candidates = candidates.len();
        let eligible_candidate_count =
            eligible_cluster_candidate_count(candidates, self.excluded_cluster_ids());
        let selection = {
            let lb_request = self.load_balancer_request();
            let lb_config = self.request.lb_resolution.config();
            if eligible_candidate_count > 0
                && let Some(limit_seconds) = lb_config.max_input_work_seconds
                && let Some(reason) = input_work_admission_rejection_reason(
                    lb_config,
                    &lb_request,
                    candidates,
                    limit_seconds,
                )
            {
                return Some(Ok(input_work_admission_rejection_response(
                    self.app.metrics.as_ref(),
                    &self.request.request_inputs.target,
                    reason,
                )));
            }
            target_snapshot.as_ref().and_then(|snapshot| {
                self.app
                    .lb_router
                    .choose_candidate_with_algorithm_resolution(
                        snapshot.load_balancers(),
                        &lb_request,
                        candidates,
                        &self.request.lb_resolution,
                    )
            })
        };

        let Some(selection) = selection else {
            return self
                .resolve_no_routing_choice(num_candidates, eligible_candidate_count)
                .await;
        };
        let selected_cluster = SelectedClusterRun::new(
            target_snapshot.expect("a selected candidate must come from a routing target snapshot"),
            selection,
            self.request.request_inputs.priority,
        );
        self.run_selected_cluster(&selected_cluster).await
    }

    async fn run_selected_cluster(
        &mut self,
        selected_cluster: &SelectedClusterRun,
    ) -> Option<Result<Response<Body>, StatusCode>> {
        let mut chosen = self.select_unfailed_backend(selected_cluster)?;
        selected_cluster.record_selection_metrics(
            self.app.metrics.as_ref(),
            &self.request.request_inputs.target,
        );

        loop {
            self.record_routing_selection(RoutingTraceFields {
                routing_algorithm: &selected_cluster.routing_algorithm,
                requested_algorithm: selected_cluster.selection.requested_algorithm.as_deref(),
                num_candidates: selected_cluster.num_candidates,
                rank_depth: selected_cluster.selection.choice.rank_depth,
                selected_after_kv_free_tokens_skip: selected_cluster
                    .selection
                    .choice
                    .selected_after_kv_free_tokens_skip,
                cluster: selected_cluster.cluster.snapshot(),
                chosen: &chosen,
            });

            let outcome = self.run_proxy_attempt(selected_cluster, &chosen).await;
            match outcome {
                ProxyAttemptOutcome::ReturnFinal(response) => return Some(Ok(response)),
                ProxyAttemptOutcome::ProxyError(status) => return Some(Err(status)),
                ProxyAttemptOutcome::RetrySameBackend(retry_backend) => chosen = retry_backend,
                ProxyAttemptOutcome::RetryAlternateBackend(inference_server_id) => {
                    self.failed_backend_ids.insert(inference_server_id);
                    chosen = self.select_unfailed_backend(selected_cluster)?;
                }
                ProxyAttemptOutcome::RetryAlternateCluster(cluster_id) => {
                    self.failed_cluster_ids.insert(cluster_id);
                    return None;
                }
            }
        }
    }

    fn select_unfailed_backend(
        &mut self,
        selected_cluster: &SelectedClusterRun,
    ) -> Option<Arc<RoutedInferenceServerSnapshot>> {
        let chosen = selected_cluster
            .cluster
            .select_backend(&self.failed_backend_ids);
        if chosen.is_none() {
            self.failed_cluster_ids
                .insert(selected_cluster.cluster.snapshot().cluster_id.clone());
        }
        chosen
    }

    async fn resolve_no_routing_choice(
        &mut self,
        num_candidates: usize,
        eligible_candidate_count: usize,
    ) -> Option<Result<Response<Body>, StatusCode>> {
        let target_registered = num_candidates == 0
            && self
                .app
                .state
                .has_registered_model_for_target(&self.request.request_inputs.target);

        match classify_no_routing_choice(NoRoutingChoiceInputs {
            num_candidates,
            eligible_candidate_count,
            target_registered,
            failed_backend_count: self.failed_backend_ids.len(),
            failed_cluster_count: self.failed_cluster_ids.len(),
            retry_allowed: should_retry_routing(self.routing_retry_deadline),
        }) {
            NoRoutingChoiceAction::RetryRouting => {
                self.routing_retry_attempts += 1;
                Span::current().record("routing.retry_attempts", self.routing_retry_attempts);
                sleep_before_routing_retry(self.routing_retry_deadline).await;
                None
            }
            NoRoutingChoiceAction::Finalize(finalization) => {
                Some(finalize_no_routing_choice(NoRoutingFinalizationContext {
                    metrics: self.app.metrics.as_ref(),
                    target: &self.request.request_inputs.target,
                    finalization,
                    failed_backend_count: self.failed_backend_ids.len(),
                    failed_cluster_count: self.failed_cluster_ids.len(),
                    routing_retry_attempts: self.routing_retry_attempts,
                }))
            }
        }
    }

    fn record_routing_selection(&mut self, routing: RoutingTraceFields<'_>) {
        let span = Span::current();
        span.record("routing.retry_attempts", self.routing_retry_attempts);
        record_routing_to_span(&span, routing);
        let Some(routing_started_at) = self.routing_started_at.take() else {
            return;
        };

        self.app
            .metrics
            .routing_duration_seconds(
                self.request.request_inputs.target.routing_key.as_deref(),
                &self.request.request_inputs.target.model_id,
            )
            .observe(routing_started_at.elapsed().as_secs_f64());
    }
}

pub(super) struct SelectedClusterRun {
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

#[cfg(test)]
mod tests {
    use prometheus::Encoder;
    use stargate_proto::pb::{InferenceServerStatus, ModelStats};

    use super::*;
    use crate::load_balancer::{
        LoadBalancerAlgorithm, LoadBalancerCandidateChoice, LoadBalancerCandidateSelection,
    };
    use crate::routing_state::{
        RegistrationIdentity, RoutedClusterSnapshot, RoutedInferenceServerSnapshot,
        test_registration_generation,
    };

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

    fn target() -> RoutingTargetKey {
        RoutingTargetKey::new(Some("tenant-a".to_string()), "model-a")
    }

    fn request_inputs() -> ProxyRequestInputs {
        ProxyRequestInputs {
            target: target(),
            input_tokens: 128,
            priority: 0,
            max_wait_ms: None,
            request_slo_ms: None,
            cache_affinity_key: None,
            routing_algorithm_override: None,
        }
    }

    fn routed_instance_snapshot(
        cluster_id: &str,
        inference_server_id: &str,
    ) -> RoutedInferenceServerSnapshot {
        let registration = test_registration_generation(RegistrationIdentity {
            inference_server_id: inference_server_id.to_string(),
            cluster_id: cluster_id.to_string(),
            inference_server_url: "quic://127.0.0.1:5000".to_string(),
            routing_key: None,
            reverse_tunnel: false,
        });
        RoutedInferenceServerSnapshot {
            registration,
            cluster_id: cluster_id.to_string(),
            inference_server_id: inference_server_id.to_string(),
            inference_server_url: "quic://127.0.0.1:5000".to_string(),
            stats: ModelStats::default(),
            rtt: Duration::from_millis(1),
            snapshot_updated_at: Instant::now(),
            status: InferenceServerStatus::Active,
            reverse_tunnel: false,
        }
    }

    fn metrics_text(metrics: &crate::metrics::StargateMetrics) -> String {
        let metric_families = metrics.registry().gather();
        let mut buffer = Vec::new();
        prometheus::TextEncoder::new()
            .encode(&metric_families, &mut buffer)
            .expect("metrics should encode");
        String::from_utf8(buffer).expect("Prometheus text should be UTF-8")
    }

    fn prepared_request(
        app: &ProxyAppState,
        request_inputs: ProxyRequestInputs,
    ) -> PreparedProxyRequest {
        let headers = HeaderMap::new();
        let lb_resolution = app
            .lb_router
            .resolve_algorithm_override(&request_inputs.target.model_id, None)
            .expect("default load-balancer algorithm should resolve");
        PreparedProxyRequest {
            request_inputs,
            lb_resolution,
            endpoint_name: "chat_completions",
            method: Method::POST,
            path_and_query: "/v1/chat/completions".to_string(),
            forwarded_headers: HeaderMap::new(),
            retry_deadline: None,
            request_start: Instant::now(),
            replay_body: ReplayableRequestBody::new(
                &headers,
                Body::empty(),
                app.retry.max_replay_body_bytes,
            )
            .expect("empty request body should be replayable"),
        }
    }

    #[tokio::test]
    async fn routing_selection_duration_metric_preserves_routing_key_label() {
        let app = super::super::test_support::test_proxy_app_state();
        let mut proxy_run = ProxyRequestRun::new(&app, prepared_request(&app, request_inputs()));
        let cluster = cluster_candidate("cluster-a");
        let chosen = routed_instance_snapshot("cluster-a", "inst-a");

        proxy_run.record_routing_selection(RoutingTraceFields {
            routing_algorithm: "round_robin",
            requested_algorithm: None,
            num_candidates: 1,
            rank_depth: 0,
            selected_after_kv_free_tokens_skip: false,
            cluster: &cluster,
            chosen: &chosen,
        });

        let body = metrics_text(&app.metrics);
        assert!(
            body.contains(
                r#"stargate_routing_duration_seconds_count{model="model-a",routing_key="tenant-a"} 1"#
            ),
            "routing duration metric should include keyed route label, got:\n{body}"
        );
    }

    #[tokio::test]
    async fn selected_cluster_records_selection_metric_labels() {
        let app = super::super::test_support::test_proxy_app_state();
        let target = target();
        let selection = LoadBalancerCandidateSelection {
            choice: LoadBalancerCandidateChoice {
                candidate_index: 0,
                rank_depth: 1,
                selected_after_kv_free_tokens_skip: false,
            },
            effective_algorithm: LoadBalancerAlgorithm::PowerOfTwo,
            requested_algorithm: None,
        };
        let selected_cluster = SelectedClusterRun::new(
            RoutingTargetSnapshot::for_test(vec![cluster_candidate("cluster-a")]),
            selection,
            0,
        );

        selected_cluster.record_selection_metrics(app.metrics.as_ref(), &target);

        let body = metrics_text(&app.metrics);
        assert!(
            body.contains(
                r#"stargate_routing_selections_total{algorithm="power-of-two",model="model-a",routing_key="tenant-a",selection="primary"} 1"#
            ),
            "selected cluster should preserve routing selection metric labels, got:\n{body}"
        );
    }

    #[tokio::test]
    async fn load_balancer_request_excludes_failed_clusters() {
        let app = super::super::test_support::test_proxy_app_state();
        let mut proxy_run = ProxyRequestRun::new(&app, prepared_request(&app, request_inputs()));

        proxy_run.failed_cluster_ids.insert("cluster-a".to_string());
        let request = proxy_run.load_balancer_request();
        let excluded = request
            .excluded_cluster_ids
            .expect("failed cluster should be excluded from subsequent routing");

        assert_eq!(excluded.len(), 1);
        assert!(excluded.contains("cluster-a"));
    }
}

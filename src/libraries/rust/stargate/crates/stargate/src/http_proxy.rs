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
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use axum::Router;
use axum::body::Body;
use axum::extract::{Request, State};
use axum::http::{HeaderMap, HeaderName, HeaderValue, Method, StatusCode, header, request::Parts};
use axum::response::Response;
use axum::routing::{get, post};
use futures::StreamExt;
use rand::Rng;
use stargate_protocol::tunnel_contract::{
    HEADER_INFERENCE_SERVER_ID as HEADER_CHOSEN_INFERENCE_SERVER_ID, HEADER_INPUT_TOKENS,
    HEADER_MODEL, HEADER_PRIORITY, HEADER_REQUEST_ID, HEADER_ROUTING_KEY,
    HEADER_STARGATE_EXPECTED_QUEUE_MS, HEADER_STARGATE_RETRY_AFTER_MS,
    HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE,
};
use stargate_tls::ServerTlsIdentity;
use tracing::{Instrument, Span, field, info, warn};
use tracing_opentelemetry::OpenTelemetrySpanExt;

use crate::load_balancer::{
    LoadBalancerAlgorithmConfig, LoadBalancerAlgorithmOverride, LoadBalancerRequest,
    LoadBalancerRouter, LoadBalancerRoutingAlgorithmError, input_work_seconds_for_request,
};
use crate::metrics::StargateMetrics;
use crate::routing_state::{
    RoutedClusterSnapshot, RoutedInferenceServerSnapshot, RoutingReservation, RoutingTargetKey,
    StargateState,
};
use crate::telemetry::{inject_trace_context, parent_context_from_headers};
use crate::tunnel::QuicHttpProxy;

const HEADER_ROUTING_METHOD: &str = "x-routing-method";
const HEADER_MAX_WAIT_MS: &str = "x-max-wait-ms";
const HEADER_REQUEST_SLO_MS: &str = "x-request-slo-ms";
const HEADER_CACHE_AFFINITY_KEY: &str = "x-cache-affinity-key";
const HEADER_CHOSEN_INFERENCE_SERVER_URL: &str = "x-inference-server-url";
const HEADER_CHOSEN_CLUSTER_ID: &str = "x-stargate-cluster-id";
const HEADER_STARGATE_ERROR_CODE: &str = "x-stargate-error-code";
const RETRY_REASON_QUEUE_ESTIMATE_MISMATCH: &str = "queue_estimate_mismatch";
const RETRY_REASON_RETRYABLE_PROXY_ERROR: &str = "retryable_proxy_error";
const ERROR_NO_ELIGIBLE_CANDIDATES: &str = "no_eligible_candidates";
const ERROR_NO_ELIGIBLE_CANDIDATES_BODY: &str =
    r#"{"error":"no eligible candidates","code":"no_eligible_candidates"}"#;
const ERROR_INPUT_WORK_LIMIT_EXCEEDED: &str = "input_work_limit_exceeded";
const ERROR_INPUT_WORK_LIMIT_EXCEEDED_BODY: &str =
    r#"{"error":"input work admission limit exceeded","code":"input_work_limit_exceeded"}"#;
const ADMISSION_REASON_INPUT_WORK_LIMIT_EXCEEDED: &str = "input_work_limit_exceeded";
const ADMISSION_REASON_INPUT_WORK_CAPACITY_UNAVAILABLE: &str = "input_work_capacity_unavailable";
const DEFAULT_RETRY_BUDGET_MS_HEADER: &str = "x-stargate-max-wait-ms";
const DEFAULT_MAX_REPLAY_BODY_BYTES: usize = 64 * 1024 * 1024;
const ROUTING_RETRY_SLEEP_MIN_MS: u64 = 1;
const ROUTING_RETRY_SLEEP_MAX_MS: u64 = 10;
const ROUTING_RETRY_MAX_WAIT_MS: u64 = 60_000;

#[derive(Clone, Copy, Debug)]
enum OpenAiProxyEndpoint {
    ChatCompletions,
    Responses,
    Embeddings,
}

impl OpenAiProxyEndpoint {
    fn path(self) -> &'static str {
        match self {
            Self::ChatCompletions => "/v1/chat/completions",
            Self::Responses => "/v1/responses",
            Self::Embeddings => "/v1/embeddings",
        }
    }

    fn name(self) -> &'static str {
        match self {
            Self::ChatCompletions => "chat_completions",
            Self::Responses => "responses",
            Self::Embeddings => "embeddings",
        }
    }
}

#[derive(Clone, Debug)]
pub struct ProxyRetryConfig {
    pub max_connect_retries: u32,
    pub max_request_retries: u32,
    pub max_replay_body_bytes: usize,
    pub retryable_status_codes: Vec<StatusCode>,
    pub require_pylon_retry_signal: bool,
    pub request_retry_budget_ms_header: Option<HeaderName>,
}

impl Default for ProxyRetryConfig {
    fn default() -> Self {
        Self {
            max_connect_retries: 2,
            max_request_retries: 2,
            max_replay_body_bytes: DEFAULT_MAX_REPLAY_BODY_BYTES,
            retryable_status_codes: vec![
                StatusCode::TOO_MANY_REQUESTS,
                StatusCode::SERVICE_UNAVAILABLE,
            ],
            require_pylon_retry_signal: true,
            request_retry_budget_ms_header: Some(HeaderName::from_static(
                DEFAULT_RETRY_BUDGET_MS_HEADER,
            )),
        }
    }
}
#[derive(Clone, Debug)]
pub struct ProxyTransportConfig {
    pub quic_connect_timeout: Duration,
    pub quic_request_timeout: Duration,
    pub tls_cert_pem: Option<Vec<u8>>,
    pub server_tls_identity: ServerTlsIdentity,
    pub quic_insecure: bool,
    pub tunnel_protocol: stargate_protocol::TunnelTransportProtocol,
    pub direct_quic_connections: usize,
    pub retry: ProxyRetryConfig,
}

#[derive(Clone)]
pub struct ProxyTrafficState {
    pub is_draining: Arc<AtomicBool>,
}

#[derive(Clone)]
pub struct ProxyAppState {
    pub state: Arc<StargateState>,
    pub quic_proxy: Arc<QuicHttpProxy>,
    pub traffic: ProxyTrafficState,
    pub lb_router: Arc<LoadBalancerRouter>,
    pub metrics: Arc<StargateMetrics>,
    pub retry: ProxyRetryConfig,
}

pub fn make_router(app: ProxyAppState) -> Router {
    Router::new()
        .route("/healthz", get(healthz))
        .route("/readyz", get(readyz))
        .route("/v1/chat/completions", post(proxy_chat_completions))
        .route("/v1/responses", post(proxy_responses))
        .route("/v1/embeddings", post(proxy_embeddings))
        .with_state(app)
}

async fn proxy_chat_completions(
    State(app): State<ProxyAppState>,
    req: Request,
) -> Result<Response<Body>, StatusCode> {
    proxy_openai_request(app, req, OpenAiProxyEndpoint::ChatCompletions).await
}

async fn proxy_responses(
    State(app): State<ProxyAppState>,
    req: Request,
) -> Result<Response<Body>, StatusCode> {
    proxy_openai_request(app, req, OpenAiProxyEndpoint::Responses).await
}

async fn proxy_embeddings(
    State(app): State<ProxyAppState>,
    req: Request,
) -> Result<Response<Body>, StatusCode> {
    proxy_openai_request(app, req, OpenAiProxyEndpoint::Embeddings).await
}

async fn proxy_openai_request(
    app: ProxyAppState,
    req: Request,
    endpoint: OpenAiProxyEndpoint,
) -> Result<Response<Body>, StatusCode> {
    let request_start = Instant::now();
    let (parts, body) = req.into_parts();
    let span = proxy_openai_request_span(&parts.headers);

    proxy_openai_request_inner(app, parts, body, endpoint, request_start)
        .instrument(span)
        .await
}

async fn proxy_openai_request_inner(
    app: ProxyAppState,
    parts: Parts,
    body: Body,
    endpoint: OpenAiProxyEndpoint,
    request_start: Instant,
) -> Result<Response<Body>, StatusCode> {
    let request_path = parts.uri.path().to_string();
    let path_and_query = parts
        .uri
        .path_and_query()
        .map(|pq| pq.to_string())
        .unwrap_or_else(|| endpoint.path().to_string());
    let request_inputs = parse_proxy_request_inputs(&parts.headers)?;
    let target = request_inputs.target.clone();
    let rk_ref = target.routing_key.as_deref();
    let model_id = target.model_id.as_str();

    Span::current().record("request.endpoint", endpoint.name());
    record_request_to_span(
        &Span::current(),
        RequestTraceFields {
            routing_key: rk_ref,
            model_id,
            request_path: &request_path,
            input_tokens: request_inputs.input_tokens,
            priority: request_inputs.priority,
            max_wait_ms: request_inputs.max_wait_ms,
            request_slo_ms: request_inputs.request_slo_ms,
            cache_affinity_key_present: request_inputs.cache_affinity_key.is_some(),
        },
    );

    let lb_resolution = match app
        .lb_router
        .resolve_algorithm_override(model_id, request_inputs.routing_algorithm_override.as_ref())
    {
        Ok(resolution) => resolution,
        Err(error) => {
            return Err(reject_invalid_routing_algorithm(&target, &error));
        }
    };
    validate_load_balancer_request_requirements(lb_resolution.config(), &request_inputs)?;

    let method = parts.method.clone();
    let forwarded_headers = prepare_forwarded_headers(&parts.headers);
    let retry_deadline = retry_budget_deadline(&parts.headers, &app.retry, request_start)?;
    let mut replay_body =
        ReplayableRequestBody::new(&parts.headers, body, app.retry.max_replay_body_bytes)?;

    let mut proxy_run =
        ProxyRequestRun::new(&app, &target, request_start, request_inputs.max_wait_ms);
    let mut attempt_counters = ProxyAttemptCounters::default();

    'cluster: loop {
        let candidates = app.state.cluster_candidates_for_target(&target).await;
        let num_candidates = candidates.len();
        let eligible_candidate_count =
            eligible_cluster_candidate_count(&candidates, proxy_run.excluded_cluster_ids());
        let selection = {
            let lb_request = proxy_run.load_balancer_request(&request_inputs);
            if eligible_candidate_count > 0
                && let Some(limit_seconds) = lb_resolution.config().max_input_work_seconds
                && let Some(reason) = input_work_admission_rejection_reason(
                    lb_resolution.config(),
                    &lb_request,
                    &candidates,
                    limit_seconds,
                )
            {
                return Ok(input_work_admission_rejection_response(
                    app.metrics.as_ref(),
                    &target,
                    reason,
                ));
            }
            app.lb_router.choose_candidate_with_algorithm_resolution(
                &lb_request,
                &candidates,
                &lb_resolution,
            )
        };
        let selection = match selection {
            Some(selection) => selection,
            None => {
                match proxy_run
                    .resolve_no_routing_choice(num_candidates, eligible_candidate_count)
                    .await
                {
                    NoRoutingChoiceResolution::RetryRouting => {
                        continue;
                    }
                    NoRoutingChoiceResolution::Return(response) => return response,
                }
            }
        };
        let routing_algorithm = selection.effective_algorithm.to_string();
        let requested_algorithm = selection.requested_algorithm.clone();
        let rank_depth = selection.choice.rank_depth;
        let selected_after_kv_free_tokens_skip =
            selection.choice.selected_after_kv_free_tokens_skip;
        let chosen_cluster = &candidates[selection.choice.candidate_index];
        let expected_queue_ms = crate::queue_estimate::queue_time_estimate_ms_for_priority(
            &chosen_cluster.stats,
            request_inputs.priority,
        );
        let mut retry_backend = None;
        let mut recorded_selection = false;

        loop {
            let chosen = if let Some(chosen) = retry_backend.take() {
                chosen
            } else if let Some(chosen) = app
                .state
                .select_backend_for_cluster(
                    &target,
                    &chosen_cluster.cluster_id,
                    proxy_run.failed_backend_ids(),
                )
                .await
            {
                chosen
            } else {
                proxy_run.fail_cluster(chosen_cluster.cluster_id.clone());
                continue 'cluster;
            };

            if !recorded_selection {
                let selection_class = if rank_depth > 1 {
                    "fallback"
                } else {
                    "primary"
                };
                app.metrics
                    .routing_selections_total(rk_ref, model_id, &routing_algorithm, selection_class)
                    .inc();
                if selected_after_kv_free_tokens_skip {
                    app.metrics
                        .routing_kv_free_token_fallback_selections_total(
                            rk_ref,
                            model_id,
                            &routing_algorithm,
                        )
                        .inc();
                }
                recorded_selection = true;
            }
            proxy_run.record_routing_selection(RoutingTraceFields {
                routing_algorithm: &routing_algorithm,
                requested_algorithm: requested_algorithm.as_deref(),
                num_candidates,
                rank_depth,
                selected_after_kv_free_tokens_skip,
                cluster: chosen_cluster,
                chosen: &chosen,
            });

            let route = ProxyAttemptRoute {
                chosen: &chosen,
                routing_algorithm: &routing_algorithm,
                requested_algorithm: requested_algorithm.as_deref(),
                expected_queue_ms,
            };
            match run_proxy_attempt(
                ProxyAttemptContext {
                    app: &app,
                    target: &target,
                    request_inputs: &request_inputs,
                    endpoint,
                    method: &method,
                    path_and_query: &path_and_query,
                    forwarded_headers: &forwarded_headers,
                    retry_deadline,
                    request_start,
                },
                route,
                &mut attempt_counters,
                &mut replay_body,
                proxy_run.failed_backend_ids.len(),
            )
            .await
            {
                ProxyAttemptOutcome::ReturnFinal(response) => return Ok(response),
                ProxyAttemptOutcome::ProxyError(status) => return Err(status),
                ProxyAttemptOutcome::RetrySameBackend { chosen } => {
                    retry_backend = Some(*chosen);
                    continue;
                }
                ProxyAttemptOutcome::RetryAlternateBackend {
                    inference_server_id,
                } => {
                    proxy_run.fail_backend(inference_server_id);
                    continue;
                }
                ProxyAttemptOutcome::RetryAlternateCluster { cluster_id } => {
                    proxy_run.fail_cluster(cluster_id);
                    continue 'cluster;
                }
            }
        }
    }
}

fn proxy_openai_request_span(headers: &HeaderMap) -> Span {
    let span = tracing::info_span!(
        "proxy_openai_request",
        request.endpoint = field::Empty,
        request.routing_key = field::Empty,
        request.model_id = field::Empty,
        request.path = field::Empty,
        request.input_tokens = field::Empty,
        request.priority = field::Empty,
        request.max_wait_ms = field::Empty,
        request.slo_ms = field::Empty,
        request.cache_affinity_key_present = field::Empty,
        routing.requested_algorithm = field::Empty,
        routing.invalid_requested_algorithm = field::Empty,
        selected_cluster.id = field::Empty,
        selected_inst.id = field::Empty,
        selected_inst.output_tps = field::Empty,
        selected_inst.last_mean_input_tps = field::Empty,
        selected_inst.max_output_tps = field::Empty,
        selected_inst.queue_size = field::Empty,
        selected_inst.queued_input_size = field::Empty,
        selected_inst.num_running_queries = field::Empty,
        selected_inst.max_engine_concurrency = field::Empty,
        selected_inst.total_query_input_size = field::Empty,
        selected_inst.kv_cache_capacity_tokens = field::Empty,
        selected_inst.kv_cache_used_tokens = field::Empty,
        selected_inst.kv_cache_free_tokens = field::Empty,
        selected_inst.rtt_ms = field::Empty,
        selected_inst.snapshot_age_ms = field::Empty,
        routing.algorithm = field::Empty,
        routing.num_candidates = field::Empty,
        routing.rank_depth = field::Empty,
        routing.selected_after_kv_free_tokens_skip = field::Empty,
        routing.retry_attempts = field::Empty,
        routing.admission_rejection_reason = field::Empty,
        proxy.upstream_status = field::Empty,
        proxy.time_to_first_byte_ms = field::Empty,
        proxy.attempt = field::Empty,
        proxy.connect_attempts = field::Empty,
        proxy.request_retries = field::Empty,
        proxy.failed_backends = field::Empty,
        proxy.queue.expected_ms = field::Empty,
        proxy.retry_reason = field::Empty,
        proxy.replay_body_bytes = field::Empty,
    );
    let _ = span.set_parent(parent_context_from_headers(headers));
    span
}

fn eligible_cluster_candidate_count(
    candidates: &[RoutedClusterSnapshot],
    excluded_cluster_ids: Option<&HashSet<String>>,
) -> usize {
    match excluded_cluster_ids {
        Some(excluded_cluster_ids) => candidates
            .iter()
            .filter(|candidate| !excluded_cluster_ids.contains(&candidate.cluster_id))
            .count(),
        None => candidates.len(),
    }
}

fn final_upstream_response(
    metrics: &StargateMetrics,
    routing_key: Option<&str>,
    model_id: &str,
    chosen: &RoutedInferenceServerSnapshot,
    upstream: UpstreamStreamingResponse,
) -> Result<Response<Body>, StatusCode> {
    record_final_response_metrics(metrics, routing_key, model_id, chosen, upstream.status);
    build_proxy_response(upstream, chosen)
}

struct ProxyRequestRun<'a> {
    app: &'a ProxyAppState,
    target: &'a RoutingTargetKey,
    request_start: Instant,
    routing_start: Instant,
    routing_retry_deadline: Option<Instant>,
    routing_retry_attempts: u64,
    failed_backend_ids: HashSet<String>,
    failed_cluster_ids: HashSet<String>,
    recorded_routing_duration: bool,
}

impl<'a> ProxyRequestRun<'a> {
    fn new(
        app: &'a ProxyAppState,
        target: &'a RoutingTargetKey,
        request_start: Instant,
        max_wait_ms: Option<u64>,
    ) -> Self {
        Self {
            app,
            target,
            request_start,
            routing_start: Instant::now(),
            routing_retry_deadline: routing_retry_deadline(request_start, max_wait_ms),
            routing_retry_attempts: 0,
            failed_backend_ids: HashSet::new(),
            failed_cluster_ids: HashSet::new(),
            recorded_routing_duration: false,
        }
    }

    fn routing_key(&self) -> Option<&str> {
        self.target.routing_key.as_deref()
    }

    fn model_id(&self) -> &str {
        self.target.model_id.as_str()
    }

    fn excluded_cluster_ids(&self) -> Option<&HashSet<String>> {
        (!self.failed_cluster_ids.is_empty()).then_some(&self.failed_cluster_ids)
    }

    fn failed_backend_ids(&self) -> &HashSet<String> {
        &self.failed_backend_ids
    }

    fn load_balancer_request<'b>(
        &'b self,
        request_inputs: &'b ProxyRequestInputs,
    ) -> LoadBalancerRequest<'b> {
        LoadBalancerRequest {
            routing_target: self.target,
            cache_affinity_key: request_inputs.cache_affinity_key.as_deref(),
            input_tokens: Some(request_inputs.input_tokens),
            priority: request_inputs.priority,
            received_at: self.request_start,
            request_slo: request_inputs.request_slo_ms.map(Duration::from_millis),
            excluded_cluster_ids: self.excluded_cluster_ids(),
        }
    }

    async fn resolve_no_routing_choice(
        &mut self,
        num_candidates: usize,
        eligible_candidate_count: usize,
    ) -> NoRoutingChoiceResolution {
        let target_registered = if num_candidates == 0 {
            self.app
                .state
                .has_registered_model_for_target(self.target)
                .await
        } else {
            false
        };

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
                NoRoutingChoiceResolution::RetryRouting
            }
            NoRoutingChoiceAction::Finalize(finalization) => NoRoutingChoiceResolution::Return(
                finalize_no_routing_choice(NoRoutingFinalizationContext {
                    metrics: self.app.metrics.as_ref(),
                    target: self.target,
                    finalization,
                    failed_backend_count: self.failed_backend_ids.len(),
                    failed_cluster_count: self.failed_cluster_ids.len(),
                    routing_retry_attempts: self.routing_retry_attempts,
                }),
            ),
        }
    }

    fn fail_backend(&mut self, inference_server_id: String) {
        self.failed_backend_ids.insert(inference_server_id);
    }

    fn fail_cluster(&mut self, cluster_id: String) {
        self.failed_cluster_ids.insert(cluster_id);
    }

    fn record_routing_selection(&mut self, routing: RoutingTraceFields<'_>) {
        Span::current().record("routing.retry_attempts", self.routing_retry_attempts);
        record_routing_to_span(&Span::current(), routing);
        if self.recorded_routing_duration {
            return;
        }

        self.app
            .metrics
            .routing_duration_seconds(self.routing_key(), self.model_id())
            .observe(self.routing_start.elapsed().as_secs_f64());
        self.recorded_routing_duration = true;
    }
}

enum NoRoutingChoiceResolution {
    RetryRouting,
    Return(Result<Response<Body>, StatusCode>),
}

#[derive(Default)]
struct ProxyAttemptCounters {
    attempt: u32,
    connect_retries: u32,
    request_retries: u32,
}

struct ProxyAttemptContext<'a> {
    app: &'a ProxyAppState,
    target: &'a RoutingTargetKey,
    request_inputs: &'a ProxyRequestInputs,
    endpoint: OpenAiProxyEndpoint,
    method: &'a Method,
    path_and_query: &'a str,
    forwarded_headers: &'a HeaderMap,
    retry_deadline: Option<Instant>,
    request_start: Instant,
}

impl ProxyAttemptContext<'_> {
    fn routing_key(&self) -> Option<&str> {
        self.target.routing_key.as_deref()
    }

    fn model_id(&self) -> &str {
        self.target.model_id.as_str()
    }
}

struct ProxyAttemptRoute<'a> {
    chosen: &'a RoutedInferenceServerSnapshot,
    routing_algorithm: &'a str,
    requested_algorithm: Option<&'a str>,
    expected_queue_ms: Option<u64>,
}

enum ProxyAttemptOutcome {
    ReturnFinal(Response<Body>),
    ProxyError(StatusCode),
    RetrySameBackend {
        chosen: Box<RoutedInferenceServerSnapshot>,
    },
    RetryAlternateBackend {
        inference_server_id: String,
    },
    RetryAlternateCluster {
        cluster_id: String,
    },
}

async fn run_proxy_attempt(
    context: ProxyAttemptContext<'_>,
    route: ProxyAttemptRoute<'_>,
    counters: &mut ProxyAttemptCounters,
    replay_body: &mut ReplayableRequestBody,
    failed_backend_count: usize,
) -> ProxyAttemptOutcome {
    counters.attempt += 1;
    record_proxy_attempt_counters(counters, failed_backend_count);

    if let Some(outcome) = ensure_hot_path_connection(&context, route.chosen, counters).await {
        return outcome;
    }

    let reservation = context
        .app
        .state
        .reserve_inference_server_for_target(
            context.target,
            &route.chosen.inference_server_id,
            Some(context.request_inputs.input_tokens),
            context.request_inputs.priority,
        )
        .await;
    record_proxy_attempt_start(&context, &route, counters);

    let upstream_span = proxy_upstream_attempt_span(&context, &route, counters.attempt);
    Span::current().record(
        "proxy.queue.expected_ms",
        route
            .expected_queue_ms
            .map(|value| value as i64)
            .unwrap_or_default(),
    );
    Span::current().record(
        "proxy.queue.expected_present",
        route.expected_queue_ms.is_some(),
    );
    let attempt_headers = headers_for_upstream_attempt(
        context.forwarded_headers,
        &upstream_span,
        route.expected_queue_ms,
    );
    let upstream_start = Instant::now();
    let upstream = proxy_via_quic_streaming(
        context.app,
        &route.chosen.inference_server_id,
        context.method.clone(),
        context.path_and_query,
        attempt_headers,
        || replay_body.body_for_attempt(),
    )
    .instrument(upstream_span.clone())
    .await;
    record_proxy_attempt_result(
        &context,
        &route,
        replay_body,
        &upstream,
        &upstream_span,
        upstream_start,
    );

    match upstream {
        Ok(upstream) => {
            handle_upstream_response_attempt(
                &context,
                &route,
                counters,
                replay_body,
                reservation,
                upstream,
            )
            .await
        }
        Err(status) => {
            handle_proxy_error_attempt(&context, &route, counters, replay_body, status).await
        }
    }
}

fn record_proxy_attempt_counters(counters: &ProxyAttemptCounters, failed_backend_count: usize) {
    Span::current().record("proxy.attempt", counters.attempt as i64);
    Span::current().record("proxy.connect_attempts", counters.connect_retries as i64);
    Span::current().record("proxy.request_retries", counters.request_retries as i64);
    Span::current().record("proxy.failed_backends", failed_backend_count as i64);
}

async fn ensure_hot_path_connection(
    context: &ProxyAttemptContext<'_>,
    chosen: &RoutedInferenceServerSnapshot,
    counters: &mut ProxyAttemptCounters,
) -> Option<ProxyAttemptOutcome> {
    if chosen.reverse_tunnel
        || counters.connect_retries >= context.app.retry.max_connect_retries
        || context
            .app
            .quic_proxy
            .has_healthy_connection(&chosen.inference_server_id)
            .await
    {
        return None;
    }

    counters.connect_retries += 1;
    context
        .app
        .metrics
        .quic_connection_evictions_total(&chosen.inference_server_id, "stale_connection")
        .inc();
    match context
        .app
        .quic_proxy
        .reconnect_direct(&chosen.inference_server_id, &chosen.inference_server_url)
        .await
    {
        Ok(()) => {
            record_hot_path_reconnect_success(context, chosen);
            None
        }
        Err(error) => {
            record_hot_path_reconnect_error(context, chosen);
            warn!(
                inference_server_id = %chosen.inference_server_id,
                error = %error,
                connect_retries = counters.connect_retries,
                "failed to reconnect stale QUIC upstream"
            );
            Some(ProxyAttemptOutcome::RetryAlternateBackend {
                inference_server_id: chosen.inference_server_id.clone(),
            })
        }
    }
}

fn record_proxy_attempt_start(
    context: &ProxyAttemptContext<'_>,
    route: &ProxyAttemptRoute<'_>,
    counters: &ProxyAttemptCounters,
) {
    info!(
        routing_key = ?context.target.routing_key,
        model_id = %context.model_id(),
        input_tokens = context.request_inputs.input_tokens,
        requested_algorithm = route.requested_algorithm.unwrap_or(""),
        routing_algorithm = %route.routing_algorithm,
        inference_server_id = %route.chosen.inference_server_id,
        inference_server_url = %route.chosen.inference_server_url,
        connect_retries = counters.connect_retries,
        request_retries = counters.request_retries,
        "proxying request"
    );
}

fn proxy_upstream_attempt_span(
    context: &ProxyAttemptContext<'_>,
    route: &ProxyAttemptRoute<'_>,
    attempt: u32,
) -> Span {
    tracing::info_span!(
        "proxy_upstream_http_request",
        request.endpoint = context.endpoint.name(),
        http.method = %context.method,
        http.path = %context.path_and_query,
        proxy.attempt = attempt as i64,
        selected_cluster.id = %route.chosen.cluster_id,
        selected_inst.id = %route.chosen.inference_server_id,
        routing.algorithm = %route.routing_algorithm,
        proxy.queue.expected_ms = route.expected_queue_ms.map(|value| value as i64).unwrap_or_default(),
        proxy.queue.expected_present = route.expected_queue_ms.is_some(),
        proxy.upstream_status = field::Empty,
        proxy.error = field::Empty,
        proxy.time_to_first_byte_ms = field::Empty,
    )
}

fn record_proxy_attempt_result(
    context: &ProxyAttemptContext<'_>,
    route: &ProxyAttemptRoute<'_>,
    replay_body: &ReplayableRequestBody,
    upstream: &Result<UpstreamStreamingResponse, StatusCode>,
    upstream_span: &Span,
    upstream_start: Instant,
) {
    let replay_body_bytes = replay_body.buffered_len();
    Span::current().record("proxy.replay_body_bytes", replay_body_bytes as i64);
    context
        .app
        .metrics
        .proxy_replay_buffer_bytes(context.model_id())
        .observe(replay_body_bytes as f64);

    let upstream_ttfb = upstream_start.elapsed();
    let ttfb = context.request_start.elapsed();
    record_upstream_attempt_to_span(upstream_span, upstream, upstream_ttfb);
    let attempt_result = proxy_attempt_result(upstream);
    context
        .app
        .metrics
        .proxy_attempts_total(
            context.routing_key(),
            context.model_id(),
            &route.chosen.inference_server_id,
            &attempt_result,
        )
        .inc();
    record_upstream_result_to_span(
        &Span::current(),
        context.app.metrics.as_ref(),
        upstream,
        ttfb,
        context.routing_key(),
        context.model_id(),
        route.chosen,
    );
}

async fn handle_upstream_response_attempt(
    context: &ProxyAttemptContext<'_>,
    route: &ProxyAttemptRoute<'_>,
    counters: &mut ProxyAttemptCounters,
    replay_body: &mut ReplayableRequestBody,
    reservation: Option<RoutingReservation>,
    upstream: UpstreamStreamingResponse,
) -> ProxyAttemptOutcome {
    let should_release_reservation =
        should_release_queue_mismatch_reservation(upstream.status, &upstream.headers);
    release_queue_mismatch_reservation_if_needed(context, reservation, should_release_reservation)
        .await;
    match decide_upstream_response_retry(
        upstream.status,
        &upstream.headers,
        &context.app.retry,
        retry_budget_has_remaining(context.retry_deadline),
        counters.request_retries,
        replay_body.replay_readiness(),
    ) {
        UpstreamResponseRetryDecision::ReturnFinal => {
            final_upstream_response_outcome(context, route.chosen, upstream)
        }
        UpstreamResponseRetryDecision::ReturnFinalRetryBudgetExhausted => {
            record_retry_exhausted(context, "retry_budget_exhausted");
            final_upstream_response_outcome(context, route.chosen, upstream)
        }
        UpstreamResponseRetryDecision::ReturnFinalRetryExhausted { retry_reason } => {
            record_retry_exhausted(context, &retry_reason);
            final_upstream_response_outcome(context, route.chosen, upstream)
        }
        UpstreamResponseRetryDecision::ReturnFinalReplayIncomplete { retry_reason } => {
            Span::current().record("proxy.retry_reason", retry_reason.as_str());
            warn!(
                inference_server_id = %route.chosen.inference_server_id,
                status = %upstream.status,
                retry_reason = %retry_reason,
                "not retrying response because request body replay buffer is incomplete"
            );
            final_upstream_response_outcome(context, route.chosen, upstream)
        }
        UpstreamResponseRetryDecision::ReturnProxyError {
            status,
            retry_reason,
        } => {
            Span::current().record("proxy.retry_reason", retry_reason.as_str());
            final_proxy_error_outcome(context, route.chosen, status)
        }
        UpstreamResponseRetryDecision::RetryAlternateBackend { retry_reason } => {
            record_request_retry(context, counters, &retry_reason);
            warn!(
                inference_server_id = %route.chosen.inference_server_id,
                cluster_id = %route.chosen.cluster_id,
                status = %upstream.status,
                request_retries = counters.request_retries,
                retry_reason = %retry_reason,
                "retrying request on a sibling backend after local queue mismatch"
            );
            ProxyAttemptOutcome::RetryAlternateBackend {
                inference_server_id: route.chosen.inference_server_id.clone(),
            }
        }
        UpstreamResponseRetryDecision::RetryAlternateCluster { retry_reason } => {
            record_request_retry(context, counters, &retry_reason);
            warn!(
                inference_server_id = %route.chosen.inference_server_id,
                cluster_id = %route.chosen.cluster_id,
                status = %upstream.status,
                request_retries = counters.request_retries,
                retry_reason = %retry_reason,
                "retrying request after retryable upstream response"
            );
            ProxyAttemptOutcome::RetryAlternateCluster {
                cluster_id: route.chosen.cluster_id.clone(),
            }
        }
    }
}

async fn release_queue_mismatch_reservation_if_needed(
    context: &ProxyAttemptContext<'_>,
    reservation: Option<RoutingReservation>,
    should_release_reservation: bool,
) {
    if should_release_reservation && let Some(reservation) = reservation {
        context
            .app
            .state
            .release_inference_server_reservation_for_target(context.target, reservation)
            .await;
    }
}

async fn handle_proxy_error_attempt(
    context: &ProxyAttemptContext<'_>,
    route: &ProxyAttemptRoute<'_>,
    counters: &mut ProxyAttemptCounters,
    replay_body: &mut ReplayableRequestBody,
    status: StatusCode,
) -> ProxyAttemptOutcome {
    match decide_proxy_error_retry(
        status,
        &context.app.retry,
        retry_budget_has_remaining(context.retry_deadline),
        counters.connect_retries,
        replay_body.replay_readiness(),
    ) {
        ProxyErrorRetryDecision::ReturnFinal => {
            final_proxy_error_outcome(context, route.chosen, status)
        }
        ProxyErrorRetryDecision::ReturnFinalRetryBudgetExhausted => {
            record_retry_exhausted(context, "retry_budget_exhausted");
            final_proxy_error_outcome(context, route.chosen, status)
        }
        ProxyErrorRetryDecision::ReturnFinalConnectRetriesExhausted => {
            record_retry_exhausted(context, "connect_retries_exhausted");
            final_proxy_error_outcome(context, route.chosen, status)
        }
        ProxyErrorRetryDecision::ReturnPayloadTooLarge => {
            final_proxy_error_outcome(context, route.chosen, StatusCode::PAYLOAD_TOO_LARGE)
        }
        ProxyErrorRetryDecision::ReturnFinalReplayIncomplete { retry_reason } => {
            Span::current().record("proxy.retry_reason", retry_reason.as_str());
            warn!(
                inference_server_id = %route.chosen.inference_server_id,
                status = %status,
                retry_reason = %retry_reason,
                "not retrying proxy error because request body replay buffer is incomplete"
            );
            final_proxy_error_outcome(context, route.chosen, status)
        }
        ProxyErrorRetryDecision::RetryConnectionOrFailover => {
            retry_connection_or_failover(context, route.chosen, counters).await
        }
    }
}

async fn retry_connection_or_failover(
    context: &ProxyAttemptContext<'_>,
    chosen: &RoutedInferenceServerSnapshot,
    counters: &mut ProxyAttemptCounters,
) -> ProxyAttemptOutcome {
    counters.connect_retries += 1;
    if !chosen.reverse_tunnel {
        context
            .app
            .metrics
            .quic_connection_evictions_total(&chosen.inference_server_id, "proxy_error")
            .inc();
        match context
            .app
            .quic_proxy
            .reconnect_direct(&chosen.inference_server_id, &chosen.inference_server_url)
            .await
        {
            Ok(()) => {
                record_hot_path_reconnect_success(context, chosen);
                warn!(
                    inference_server_id = %chosen.inference_server_id,
                    connect_retries = counters.connect_retries,
                    "reconnected QUIC upstream after proxy failure"
                );
                return ProxyAttemptOutcome::RetrySameBackend {
                    chosen: Box::new(chosen.clone()),
                };
            }
            Err(error) => {
                record_hot_path_reconnect_error(context, chosen);
                warn!(
                    inference_server_id = %chosen.inference_server_id,
                    error = %error,
                    connect_retries = counters.connect_retries,
                    "failed to reconnect QUIC upstream"
                );
            }
        }
    }
    context
        .app
        .metrics
        .proxy_retries_total(
            context.routing_key(),
            context.model_id(),
            "connect_failover",
        )
        .inc();
    Span::current().record("proxy.retry_reason", "connect_failover");
    ProxyAttemptOutcome::RetryAlternateBackend {
        inference_server_id: chosen.inference_server_id.clone(),
    }
}

fn record_hot_path_reconnect_success(
    context: &ProxyAttemptContext<'_>,
    chosen: &RoutedInferenceServerSnapshot,
) {
    context
        .app
        .metrics
        .quic_hot_path_reconnect_total(&chosen.inference_server_id, "success")
        .inc();
    context
        .app
        .metrics
        .proxy_retries_total(
            context.routing_key(),
            context.model_id(),
            "hot_path_reconnect",
        )
        .inc();
    Span::current().record("proxy.retry_reason", "hot_path_reconnect");
}

fn record_hot_path_reconnect_error(
    context: &ProxyAttemptContext<'_>,
    chosen: &RoutedInferenceServerSnapshot,
) {
    context
        .app
        .metrics
        .quic_hot_path_reconnect_total(&chosen.inference_server_id, "error")
        .inc();
}

fn record_retry_exhausted(context: &ProxyAttemptContext<'_>, retry_reason: &str) {
    context
        .app
        .metrics
        .proxy_retry_exhausted_total(context.routing_key(), context.model_id(), retry_reason)
        .inc();
    Span::current().record("proxy.retry_reason", retry_reason);
}

fn record_request_retry(
    context: &ProxyAttemptContext<'_>,
    counters: &mut ProxyAttemptCounters,
    retry_reason: &str,
) {
    Span::current().record("proxy.retry_reason", retry_reason);
    counters.request_retries += 1;
    context
        .app
        .metrics
        .proxy_retries_total(context.routing_key(), context.model_id(), retry_reason)
        .inc();
}

fn final_upstream_response_outcome(
    context: &ProxyAttemptContext<'_>,
    chosen: &RoutedInferenceServerSnapshot,
    upstream: UpstreamStreamingResponse,
) -> ProxyAttemptOutcome {
    match final_upstream_response(
        context.app.metrics.as_ref(),
        context.routing_key(),
        context.model_id(),
        chosen,
        upstream,
    ) {
        Ok(response) => ProxyAttemptOutcome::ReturnFinal(response),
        Err(status) => ProxyAttemptOutcome::ProxyError(status),
    }
}

fn final_proxy_error_outcome(
    context: &ProxyAttemptContext<'_>,
    chosen: &RoutedInferenceServerSnapshot,
    status: StatusCode,
) -> ProxyAttemptOutcome {
    record_final_response_metrics(
        context.app.metrics.as_ref(),
        context.routing_key(),
        context.model_id(),
        chosen,
        status,
    );
    ProxyAttemptOutcome::ProxyError(status)
}

fn build_proxy_response(
    upstream: UpstreamStreamingResponse,
    chosen: &RoutedInferenceServerSnapshot,
) -> Result<Response<Body>, StatusCode> {
    let mut response = Response::builder().status(upstream.status);
    {
        let response_headers = response
            .headers_mut()
            .ok_or(StatusCode::INTERNAL_SERVER_ERROR)?;
        copy_forwardable_headers(&upstream.headers, response_headers);
        response_headers.insert(
            HeaderName::from_static(HEADER_CHOSEN_INFERENCE_SERVER_ID),
            HeaderValue::from_str(&chosen.inference_server_id)
                .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?,
        );
        response_headers.insert(
            HeaderName::from_static(HEADER_CHOSEN_INFERENCE_SERVER_URL),
            HeaderValue::from_str(&chosen.inference_server_url)
                .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?,
        );
        response_headers.insert(
            HeaderName::from_static(HEADER_CHOSEN_CLUSTER_ID),
            HeaderValue::from_str(&chosen.cluster_id)
                .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?,
        );
    }
    response
        .body(upstream.body)
        .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)
}

struct RequestTraceFields<'a> {
    routing_key: Option<&'a str>,
    model_id: &'a str,
    request_path: &'a str,
    input_tokens: u64,
    priority: u32,
    max_wait_ms: Option<u64>,
    request_slo_ms: Option<u64>,
    cache_affinity_key_present: bool,
}

#[derive(Clone, Debug, PartialEq, Eq)]
struct ProxyRequestInputs {
    target: RoutingTargetKey,
    input_tokens: u64,
    priority: u32,
    max_wait_ms: Option<u64>,
    request_slo_ms: Option<u64>,
    cache_affinity_key: Option<String>,
    routing_algorithm_override: Option<LoadBalancerAlgorithmOverride>,
}

fn parse_proxy_request_inputs(headers: &HeaderMap) -> Result<ProxyRequestInputs, StatusCode> {
    let _request_id = get_required_header(headers, HEADER_REQUEST_ID)?;
    let input_tokens =
        parse_optional_u64_header(headers, HEADER_INPUT_TOKENS)?.ok_or(StatusCode::BAD_REQUEST)?;
    let target = RoutingTargetKey {
        routing_key: get_optional_header(headers, HEADER_ROUTING_KEY),
        model_id: get_required_header(headers, HEADER_MODEL)?,
    };
    let routing_algorithm_override = parse_routing_algorithm_override(headers, &target)?;
    Ok(ProxyRequestInputs {
        target,
        input_tokens,
        priority: parse_optional_u32_header(headers, HEADER_PRIORITY)?.unwrap_or(0),
        max_wait_ms: parse_optional_u64_header(headers, HEADER_MAX_WAIT_MS)?,
        request_slo_ms: parse_optional_u64_header(headers, HEADER_REQUEST_SLO_MS)?,
        cache_affinity_key: get_optional_header(headers, HEADER_CACHE_AFFINITY_KEY),
        routing_algorithm_override,
    })
}

fn parse_routing_algorithm_override(
    headers: &HeaderMap,
    target: &RoutingTargetKey,
) -> Result<Option<LoadBalancerAlgorithmOverride>, StatusCode> {
    let Some(value) = headers.get(HEADER_ROUTING_METHOD) else {
        return Ok(None);
    };
    let raw = value.to_str().map_err(|_| {
        reject_invalid_routing_algorithm(
            target,
            &LoadBalancerRoutingAlgorithmError::Unknown {
                raw: "<invalid-utf8>".to_string(),
            },
        )
    })?;
    raw.parse::<LoadBalancerAlgorithmOverride>()
        .map(Some)
        .map_err(|error| reject_invalid_routing_algorithm(target, &error))
}

fn validate_load_balancer_request_requirements(
    lb_config: &LoadBalancerAlgorithmConfig,
    request_inputs: &ProxyRequestInputs,
) -> Result<(), StatusCode> {
    let target = &request_inputs.target;
    let model_id = target.model_id.as_str();
    if lb_config.requires_cache_affinity_key() && request_inputs.cache_affinity_key.is_none() {
        warn!(
            routing_key = ?target.routing_key,
            model_id = %model_id,
            "missing cache affinity key for load-balanced proxy request"
        );
        return Err(StatusCode::BAD_REQUEST);
    }
    Ok(())
}

fn reject_invalid_routing_algorithm(
    target: &RoutingTargetKey,
    error: &LoadBalancerRoutingAlgorithmError,
) -> StatusCode {
    let requested_algorithm = error.requested_algorithm();
    Span::current().record("routing.requested_algorithm", requested_algorithm);
    Span::current().record("routing.invalid_requested_algorithm", requested_algorithm);
    warn!(
        routing_key = ?target.routing_key,
        model_id = %target.model_id,
        requested_algorithm = %requested_algorithm,
        rejection_reason = %error.reason(),
        "invalid routing algorithm header"
    );
    StatusCode::BAD_REQUEST
}

fn record_request_to_span(span: &Span, fields: RequestTraceFields<'_>) {
    span.record("request.routing_key", fields.routing_key.unwrap_or(""));
    span.record("request.model_id", fields.model_id);
    span.record("request.path", fields.request_path);
    span.record("request.input_tokens", fields.input_tokens);
    span.record("request.priority", fields.priority);
    span.record("request.max_wait_ms", fields.max_wait_ms.unwrap_or(0));
    span.record("request.slo_ms", fields.request_slo_ms.unwrap_or(0));
    span.record(
        "request.cache_affinity_key_present",
        fields.cache_affinity_key_present,
    );
}

struct RoutingTraceFields<'a> {
    routing_algorithm: &'a str,
    requested_algorithm: Option<&'a str>,
    num_candidates: usize,
    rank_depth: usize,
    selected_after_kv_free_tokens_skip: bool,
    cluster: &'a RoutedClusterSnapshot,
    chosen: &'a RoutedInferenceServerSnapshot,
}

fn record_routing_to_span(span: &Span, routing: RoutingTraceFields<'_>) {
    let fields = SelectedInstanceTraceFields::from_route(routing.cluster, routing.chosen);
    span.record("routing.algorithm", routing.routing_algorithm);
    span.record(
        "routing.requested_algorithm",
        routing.requested_algorithm.unwrap_or(""),
    );
    span.record("routing.num_candidates", routing.num_candidates);
    span.record("routing.rank_depth", routing.rank_depth as i64);
    span.record(
        "routing.selected_after_kv_free_tokens_skip",
        routing.selected_after_kv_free_tokens_skip,
    );
    span.record("selected_cluster.id", &routing.cluster.cluster_id);
    span.record("selected_inst.id", &fields.inference_server_id);
    span.record("selected_inst.output_tps", fields.output_tps);
    span.record(
        "selected_inst.last_mean_input_tps",
        fields.last_mean_input_tps,
    );
    span.record("selected_inst.max_output_tps", fields.max_output_tps);
    span.record("selected_inst.queue_size", fields.queue_size);
    span.record("selected_inst.queued_input_size", fields.queued_input_size);
    span.record(
        "selected_inst.num_running_queries",
        fields.num_running_queries,
    );
    span.record(
        "selected_inst.max_engine_concurrency",
        fields.max_engine_concurrency,
    );
    span.record(
        "selected_inst.total_query_input_size",
        fields.total_query_input_size,
    );
    span.record(
        "selected_inst.kv_cache_capacity_tokens",
        fields.kv_cache_capacity_tokens,
    );
    span.record(
        "selected_inst.kv_cache_used_tokens",
        fields.kv_cache_used_tokens,
    );
    span.record(
        "selected_inst.kv_cache_free_tokens",
        fields.kv_cache_free_tokens,
    );
    span.record("selected_inst.rtt_ms", fields.rtt_ms);
    span.record("selected_inst.snapshot_age_ms", fields.snapshot_age_ms);
}

#[derive(Debug, Clone, Copy)]
struct NoRoutingChoiceInputs {
    num_candidates: usize,
    eligible_candidate_count: usize,
    target_registered: bool,
    failed_backend_count: usize,
    failed_cluster_count: usize,
    retry_allowed: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum NoRoutingChoiceAction {
    RetryRouting,
    Finalize(NoRoutingFinalization),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum NoRoutingFinalization {
    NoCandidatesNotFound,
    ServiceUnavailable,
}

fn classify_no_routing_choice(inputs: NoRoutingChoiceInputs) -> NoRoutingChoiceAction {
    if inputs.num_candidates > 0 && inputs.eligible_candidate_count > 0 && inputs.retry_allowed {
        return NoRoutingChoiceAction::RetryRouting;
    }

    if inputs.num_candidates == 0
        && !inputs.target_registered
        && inputs.failed_backend_count == 0
        && inputs.failed_cluster_count == 0
    {
        return NoRoutingChoiceAction::Finalize(NoRoutingFinalization::NoCandidatesNotFound);
    }

    NoRoutingChoiceAction::Finalize(NoRoutingFinalization::ServiceUnavailable)
}

struct NoRoutingFinalizationContext<'a> {
    metrics: &'a StargateMetrics,
    target: &'a RoutingTargetKey,
    finalization: NoRoutingFinalization,
    failed_backend_count: usize,
    failed_cluster_count: usize,
    routing_retry_attempts: u64,
}

fn finalize_no_routing_choice(
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

async fn sleep_before_routing_retry(deadline: Option<Instant>) {
    let Some(deadline) = deadline else {
        return;
    };
    // The retry deadline may pass between checks; clamp elapsed deadlines to no sleep.
    let remaining = deadline.saturating_duration_since(Instant::now());
    if remaining.is_zero() {
        return;
    }
    let random_sleep_ms =
        rand::rng().random_range(ROUTING_RETRY_SLEEP_MIN_MS..ROUTING_RETRY_SLEEP_MAX_MS);
    tokio::time::sleep(remaining.min(Duration::from_millis(random_sleep_ms))).await;
}

#[derive(Debug, Clone)]
struct SelectedInstanceTraceFields {
    inference_server_id: String,
    output_tps: f64,
    last_mean_input_tps: f64,
    max_output_tps: f64,
    queue_size: u64,
    queued_input_size: u64,
    kv_cache_capacity_tokens: u64,
    kv_cache_used_tokens: u64,
    kv_cache_free_tokens: u64,
    num_running_queries: u64,
    max_engine_concurrency: u64,
    total_query_input_size: u64,
    rtt_ms: f64,
    snapshot_age_ms: f64,
}

impl SelectedInstanceTraceFields {
    fn from_route(
        cluster: &RoutedClusterSnapshot,
        backend: &RoutedInferenceServerSnapshot,
    ) -> Self {
        Self {
            inference_server_id: backend.inference_server_id.clone(),
            output_tps: cluster.stats.output_tps,
            last_mean_input_tps: cluster.stats.last_mean_input_tps,
            max_output_tps: cluster.stats.max_output_tps,
            queue_size: cluster.stats.queue_size,
            queued_input_size: cluster.stats.queued_input_size,
            kv_cache_capacity_tokens: cluster.stats.kv_cache_capacity_tokens,
            kv_cache_used_tokens: cluster.stats.kv_cache_used_tokens,
            kv_cache_free_tokens: cluster.stats.kv_cache_free_tokens,
            num_running_queries: cluster.stats.num_running_queries,
            max_engine_concurrency: cluster.stats.max_engine_concurrency,
            total_query_input_size: cluster.stats.total_query_input_size,
            rtt_ms: cluster.rtt.as_secs_f64() * 1000.0,
            snapshot_age_ms: cluster.snapshot_updated_at.elapsed().as_secs_f64() * 1000.0,
        }
    }
}

fn record_upstream_result_to_span(
    span: &Span,
    metrics: &StargateMetrics,
    result: &Result<UpstreamStreamingResponse, StatusCode>,
    ttfb: Duration,
    routing_key: Option<&str>,
    model_id: &str,
    chosen: &RoutedInferenceServerSnapshot,
) {
    span.record("proxy.time_to_first_byte_ms", ttfb.as_secs_f64() * 1000.0);
    match result {
        Ok(resp) => {
            let status_code = resp.status.as_u16().to_string();
            span.record("proxy.upstream_status", &status_code);
            metrics
                .proxy_duration_seconds(routing_key, model_id, &chosen.inference_server_id)
                .observe(ttfb.as_secs_f64());
        }
        Err(status) => {
            let status_code = status.as_u16().to_string();
            span.record("proxy.upstream_status", &status_code);
        }
    }
}

fn record_upstream_attempt_to_span(
    span: &Span,
    result: &Result<UpstreamStreamingResponse, StatusCode>,
    ttfb: Duration,
) {
    span.record("proxy.time_to_first_byte_ms", ttfb.as_secs_f64() * 1000.0);
    match result {
        Ok(resp) => {
            let status_code = resp.status.as_u16().to_string();
            span.record("proxy.upstream_status", &status_code);
        }
        Err(status) => {
            let status_code = status.as_u16().to_string();
            span.record("proxy.error", &status_code);
        }
    }
}

fn record_final_response_metrics(
    metrics: &StargateMetrics,
    routing_key: Option<&str>,
    model_id: &str,
    chosen: &RoutedInferenceServerSnapshot,
    status: StatusCode,
) {
    metrics
        .requests_total(
            routing_key,
            model_id,
            &chosen.inference_server_id,
            &status.as_u16().to_string(),
        )
        .inc();
}

struct UpstreamStreamingResponse {
    status: StatusCode,
    headers: HeaderMap,
    body: Body,
}

async fn proxy_via_quic_streaming(
    app: &ProxyAppState,
    inference_server_id: &str,
    method: Method,
    path_and_query: &str,
    forwarded_headers: HeaderMap,
    request_body: impl FnOnce() -> Result<Body, StatusCode> + Send,
) -> Result<UpstreamStreamingResponse, StatusCode> {
    let streaming_resp = app
        .quic_proxy
        .open_streaming_request(inference_server_id, method, path_and_query, forwarded_headers)
        .await
        .map_err(|error| {
            warn!(inference_server_id = %inference_server_id, error = %error, "quic upstream request failed");
            StatusCode::BAD_GATEWAY
        })?
        .send_body_and_recv_response(request_body()?)
        .await
        .map_err(|error| {
            warn!(inference_server_id = %inference_server_id, error = %error, "quic upstream request failed");
            StatusCode::BAD_GATEWAY
        })?;

    let status = streaming_resp.status;
    let headers = streaming_resp.headers;
    let mut body_stream = streaming_resp.body_stream;

    let body = Body::from_stream(async_stream::stream! {
        loop {
            match body_stream.recv_body().await {
                Ok(Some(chunk)) => yield Ok::<_, std::io::Error>(chunk),
                Ok(None) => break,
                Err(e) => {
                    yield Err(std::io::Error::other(e.to_string()));
                    break;
                }
            }
        }
    });

    Ok(UpstreamStreamingResponse {
        status,
        headers,
        body,
    })
}

fn prepare_forwarded_headers(headers: &HeaderMap) -> HeaderMap {
    let mut forwarded_headers = HeaderMap::new();
    copy_forwardable_headers(headers, &mut forwarded_headers);
    forwarded_headers
}

fn headers_for_upstream_attempt(
    forwarded_headers: &HeaderMap,
    span: &Span,
    expected_queue_ms: Option<u64>,
) -> HeaderMap {
    let mut attempt_headers = forwarded_headers.clone();
    let context = span.context();
    inject_trace_context(&mut attempt_headers, &context);
    if let Some(expected_queue_ms) = expected_queue_ms {
        attempt_headers.insert(
            HeaderName::from_static(HEADER_STARGATE_EXPECTED_QUEUE_MS),
            HeaderValue::from_str(&expected_queue_ms.to_string())
                .expect("decimal queue estimate should be a valid header value"),
        );
    }
    attempt_headers
}

struct ReplayableRequestBody {
    first_body: Option<Body>,
    buffer: Arc<Mutex<Vec<u8>>>,
    // These flags summarize the body-stream task state for later retry decisions.
    // The bytes themselves stay protected by `buffer`; acquire/release ordering
    // keeps flag observations tied to the stream task's terminal updates.
    overflowed: Arc<AtomicBool>,
    completed: Arc<AtomicBool>,
    max_bytes: usize,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ReplayReadiness {
    Ready,
    Incomplete,
    PayloadTooLarge,
}

impl ReplayableRequestBody {
    fn new(headers: &HeaderMap, body: Body, max_bytes: usize) -> Result<Self, StatusCode> {
        if let Some(content_length) = headers
            .get(axum::http::header::CONTENT_LENGTH)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.parse::<usize>().ok())
            && content_length > max_bytes
        {
            return Err(StatusCode::PAYLOAD_TOO_LARGE);
        }

        Ok(Self {
            first_body: Some(body),
            buffer: Arc::new(Mutex::new(Vec::new())),
            overflowed: Arc::new(AtomicBool::new(false)),
            completed: Arc::new(AtomicBool::new(false)),
            max_bytes,
        })
    }

    fn body_for_attempt(&mut self) -> Result<Body, StatusCode> {
        if let Some(body) = self.first_body.take() {
            return Ok(self.buffering_body(body));
        }
        if self.overflowed.load(Ordering::Acquire) {
            return Err(StatusCode::PAYLOAD_TOO_LARGE);
        }
        if !self.completed.load(Ordering::Acquire) {
            return Err(StatusCode::BAD_GATEWAY);
        }
        let buffer = self
            .buffer
            .lock()
            .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?
            .clone();
        Ok(Body::from(buffer))
    }

    fn buffered_len(&self) -> usize {
        self.buffer.lock().map(|buffer| buffer.len()).unwrap_or(0)
    }

    fn replay_readiness(&self) -> ReplayReadiness {
        if self.overflowed.load(Ordering::Acquire) {
            ReplayReadiness::PayloadTooLarge
        } else if self.first_body.is_some() || self.completed.load(Ordering::Acquire) {
            ReplayReadiness::Ready
        } else {
            ReplayReadiness::Incomplete
        }
    }

    fn buffering_body(&self, body: Body) -> Body {
        let buffer = self.buffer.clone();
        let overflowed = self.overflowed.clone();
        let completed = self.completed.clone();
        let max_bytes = self.max_bytes;
        Body::from_stream(async_stream::stream! {
            let mut stream = body.into_data_stream();
            let mut read_complete = true;
            while let Some(chunk_result) = stream.next().await {
                match chunk_result {
                    Ok(chunk) => {
                        let should_buffer = {
                            let Ok(mut buffered) = buffer.lock() else {
                                read_complete = false;
                                yield Err(std::io::Error::other("replay buffer lock poisoned"));
                                break;
                            };
                            match buffered.len().checked_add(chunk.len()) {
                                Some(next_len) if next_len <= max_bytes => {
                                    buffered.extend_from_slice(&chunk);
                                    true
                                }
                                _ => false,
                            }
                        };
                        if !should_buffer {
                            overflowed.store(true, Ordering::Release);
                        }
                        yield Ok::<_, std::io::Error>(chunk);
                    }
                    Err(error) => {
                        read_complete = false;
                        yield Err(std::io::Error::other(error.to_string()));
                        break;
                    }
                }
            }
            if read_complete {
                completed.store(true, Ordering::Release);
            }
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum UpstreamResponseRetryDecision {
    ReturnFinal,
    ReturnFinalRetryBudgetExhausted,
    ReturnFinalRetryExhausted {
        retry_reason: String,
    },
    ReturnFinalReplayIncomplete {
        retry_reason: String,
    },
    ReturnProxyError {
        status: StatusCode,
        retry_reason: String,
    },
    RetryAlternateBackend {
        retry_reason: String,
    },
    RetryAlternateCluster {
        retry_reason: String,
    },
}

fn decide_upstream_response_retry(
    status: StatusCode,
    headers: &HeaderMap,
    retry: &ProxyRetryConfig,
    retry_budget_remaining: bool,
    request_retries: u32,
    replay_readiness: ReplayReadiness,
) -> UpstreamResponseRetryDecision {
    if !should_retry_upstream_response(status, headers, retry) {
        return UpstreamResponseRetryDecision::ReturnFinal;
    }

    let retry_reason = retry_reason_from_headers(headers);
    if !retry_budget_remaining {
        return UpstreamResponseRetryDecision::ReturnFinalRetryBudgetExhausted;
    }
    if request_retries >= retry.max_request_retries {
        return UpstreamResponseRetryDecision::ReturnFinalRetryExhausted { retry_reason };
    }

    match replay_readiness {
        ReplayReadiness::Ready if retry_reason == RETRY_REASON_QUEUE_ESTIMATE_MISMATCH => {
            UpstreamResponseRetryDecision::RetryAlternateBackend { retry_reason }
        }
        ReplayReadiness::Ready => {
            UpstreamResponseRetryDecision::RetryAlternateCluster { retry_reason }
        }
        ReplayReadiness::Incomplete => {
            UpstreamResponseRetryDecision::ReturnFinalReplayIncomplete { retry_reason }
        }
        ReplayReadiness::PayloadTooLarge => UpstreamResponseRetryDecision::ReturnProxyError {
            status: StatusCode::PAYLOAD_TOO_LARGE,
            retry_reason,
        },
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum ProxyErrorRetryDecision {
    ReturnFinal,
    ReturnFinalRetryBudgetExhausted,
    ReturnFinalConnectRetriesExhausted,
    ReturnPayloadTooLarge,
    ReturnFinalReplayIncomplete { retry_reason: String },
    RetryConnectionOrFailover,
}

fn decide_proxy_error_retry(
    status: StatusCode,
    retry: &ProxyRetryConfig,
    retry_budget_remaining: bool,
    connect_retries: u32,
    replay_readiness: ReplayReadiness,
) -> ProxyErrorRetryDecision {
    if !is_retryable_proxy_error(status) {
        return ProxyErrorRetryDecision::ReturnFinal;
    }
    if !retry_budget_remaining {
        return ProxyErrorRetryDecision::ReturnFinalRetryBudgetExhausted;
    }
    if connect_retries >= retry.max_connect_retries {
        return ProxyErrorRetryDecision::ReturnFinalConnectRetriesExhausted;
    }

    match replay_readiness {
        ReplayReadiness::Ready => ProxyErrorRetryDecision::RetryConnectionOrFailover,
        ReplayReadiness::Incomplete => ProxyErrorRetryDecision::ReturnFinalReplayIncomplete {
            retry_reason: RETRY_REASON_RETRYABLE_PROXY_ERROR.to_string(),
        },
        ReplayReadiness::PayloadTooLarge => ProxyErrorRetryDecision::ReturnPayloadTooLarge,
    }
}

fn should_retry_upstream_response(
    status: StatusCode,
    headers: &HeaderMap,
    retry: &ProxyRetryConfig,
) -> bool {
    if !retry.retryable_status_codes.contains(&status) {
        return false;
    }

    if let Some(retryable) = headers
        .get(HEADER_STARGATE_RETRYABLE)
        .and_then(|value| value.to_str().ok())
    {
        return retryable.eq_ignore_ascii_case("true");
    }

    !retry.require_pylon_retry_signal
}

fn should_release_queue_mismatch_reservation(status: StatusCode, headers: &HeaderMap) -> bool {
    status == StatusCode::TOO_MANY_REQUESTS
        && headers
            .get(HEADER_STARGATE_RETRYABLE)
            .and_then(|value| value.to_str().ok())
            .is_some_and(|value| value.eq_ignore_ascii_case("true"))
        && headers
            .get(HEADER_STARGATE_RETRY_REASON)
            .and_then(|value| value.to_str().ok())
            == Some(RETRY_REASON_QUEUE_ESTIMATE_MISMATCH)
}

fn retry_budget_deadline(
    headers: &HeaderMap,
    retry: &ProxyRetryConfig,
    request_start: Instant,
) -> Result<Option<Instant>, StatusCode> {
    let Some(header_name) = &retry.request_retry_budget_ms_header else {
        return Ok(None);
    };
    let Some(header_value) = headers.get(header_name) else {
        return Ok(None);
    };
    let budget_ms = header_value
        .to_str()
        .map_err(|_| StatusCode::BAD_REQUEST)?
        .trim()
        .parse::<u64>()
        .map_err(|_| StatusCode::BAD_REQUEST)?;
    Ok(Some(
        request_start
            .checked_add(Duration::from_millis(budget_ms))
            .ok_or(StatusCode::BAD_REQUEST)?,
    ))
}

fn retry_budget_has_remaining(deadline: Option<Instant>) -> bool {
    deadline.is_none_or(|deadline| Instant::now() < deadline)
}

fn retry_reason_from_headers(headers: &HeaderMap) -> String {
    headers
        .get(HEADER_STARGATE_RETRY_REASON)
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(|value| {
            if value == RETRY_REASON_QUEUE_ESTIMATE_MISMATCH {
                RETRY_REASON_QUEUE_ESTIMATE_MISMATCH.to_string()
            } else {
                value.to_string()
            }
        })
        .unwrap_or_else(|| "retryable_upstream_response".to_string())
}

fn proxy_attempt_result(result: &Result<UpstreamStreamingResponse, StatusCode>) -> String {
    match result {
        Ok(response) => format!("upstream_{}", response.status.as_u16()),
        Err(status) => format!("proxy_{}", status.as_u16()),
    }
}

fn no_eligible_candidates_response() -> Response<Body> {
    let mut response = Response::new(Body::from(ERROR_NO_ELIGIBLE_CANDIDATES_BODY));
    *response.status_mut() = StatusCode::NOT_FOUND;
    response.headers_mut().insert(
        HeaderName::from_static(HEADER_STARGATE_ERROR_CODE),
        HeaderValue::from_static(ERROR_NO_ELIGIBLE_CANDIDATES),
    );
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json"),
    );
    response
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

fn input_work_admission_rejection_response(
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

    let mut response = Response::new(Body::from(ERROR_INPUT_WORK_LIMIT_EXCEEDED_BODY));
    *response.status_mut() = StatusCode::SERVICE_UNAVAILABLE;
    response.headers_mut().insert(
        HeaderName::from_static(HEADER_STARGATE_ERROR_CODE),
        HeaderValue::from_static(ERROR_INPUT_WORK_LIMIT_EXCEEDED),
    );
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json"),
    );
    response
}

fn is_retryable_proxy_error(status: StatusCode) -> bool {
    matches!(
        status,
        StatusCode::BAD_GATEWAY | StatusCode::GATEWAY_TIMEOUT | StatusCode::SERVICE_UNAVAILABLE
    )
}

async fn healthz() -> StatusCode {
    StatusCode::OK
}

async fn readyz(State(app): State<ProxyAppState>) -> StatusCode {
    if app.traffic.is_draining.load(Ordering::Relaxed) {
        return StatusCode::SERVICE_UNAVAILABLE;
    }

    StatusCode::OK
}

fn get_optional_header(headers: &HeaderMap, name: &'static str) -> Option<String> {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .map(|value| value.trim())
        .filter(|value| !value.is_empty())
        .map(ToOwned::to_owned)
}

fn get_required_header(headers: &HeaderMap, name: &'static str) -> Result<String, StatusCode> {
    let value = headers
        .get(name)
        .ok_or(StatusCode::BAD_REQUEST)?
        .to_str()
        .map_err(|_| StatusCode::BAD_REQUEST)?
        .trim()
        .to_string();
    if value.is_empty() {
        return Err(StatusCode::BAD_REQUEST);
    }
    Ok(value)
}

fn parse_optional_u64_header(
    headers: &HeaderMap,
    name: &'static str,
) -> Result<Option<u64>, StatusCode> {
    parse_optional_numeric_header(headers, name)
}

fn parse_optional_u32_header(
    headers: &HeaderMap,
    name: &'static str,
) -> Result<Option<u32>, StatusCode> {
    parse_optional_numeric_header(headers, name)
}

fn parse_optional_numeric_header<T>(
    headers: &HeaderMap,
    name: &'static str,
) -> Result<Option<T>, StatusCode>
where
    T: std::str::FromStr,
{
    let Some(value) = headers.get(name) else {
        return Ok(None);
    };
    let value = value.to_str().map_err(|_| StatusCode::BAD_REQUEST)?.trim();
    if value.is_empty() {
        return Err(StatusCode::BAD_REQUEST);
    }
    value
        .parse::<T>()
        .map(Some)
        .map_err(|_| StatusCode::BAD_REQUEST)
}

fn should_forward_header(name: &HeaderName) -> bool {
    // `HeaderName` stores normalized lowercase names, so matching the borrowed
    // str avoids allocating a lowercase copy for every proxied header.
    !matches!(
        name.as_str(),
        "connection"
            | "proxy-connection"
            | "keep-alive"
            | "transfer-encoding"
            | "te"
            | "trailer"
            | "upgrade"
            | "host"
            | HEADER_ROUTING_METHOD
            | HEADER_STARGATE_RETRYABLE
            | HEADER_STARGATE_RETRY_REASON
            | HEADER_STARGATE_RETRY_AFTER_MS
            | HEADER_STARGATE_EXPECTED_QUEUE_MS
            | HEADER_STARGATE_ERROR_CODE
    )
}

fn copy_forwardable_headers(from: &HeaderMap, to: &mut HeaderMap) {
    for (name, value) in from {
        if should_forward_header(name) {
            to.append(name, value.clone());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use opentelemetry::global;
    use opentelemetry::trace::TraceContextExt;
    use opentelemetry_sdk::propagation::TraceContextPropagator;
    use prometheus::Encoder;
    use std::collections::HashMap;
    use std::time::Instant;

    use axum::body::Bytes;
    use stargate_proto::pb::{InferenceServerStatus, ModelStats};

    use crate::auth::OpenAuthenticator;
    use crate::load_balancer::{
        LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig, LoadBalancerConfig,
        LoadBalancerModelConfig,
    };
    use crate::routing_state::DeliveryTarget;
    use crate::tunnel::QuicTunnelConfig;

    fn test_proxy_app_state() -> ProxyAppState {
        test_proxy_app_state_with_lb_config(LoadBalancerConfig::default())
    }

    fn test_proxy_app_state_with_lb_config(lb_config: LoadBalancerConfig) -> ProxyAppState {
        let _ = rustls::crypto::aws_lc_rs::default_provider().install_default();
        let metrics = StargateMetrics::new().expect("metrics should initialize");
        ProxyAppState {
            state: Arc::new(StargateState::new()),
            quic_proxy: Arc::new(
                QuicHttpProxy::new(
                    QuicTunnelConfig {
                        connect_timeout: Duration::from_millis(10),
                        request_timeout: Duration::from_millis(10),
                        direct_quic_connections: 1,
                        tls_cert_pem: None,
                        server_tls_identity: stargate_tls::ServerTlsIdentity::SelfSigned,
                        quic_insecure: true,
                        tunnel_protocol: Default::default(),
                    },
                    Arc::new(OpenAuthenticator),
                )
                .expect("quic proxy should initialize"),
            ),
            traffic: ProxyTrafficState {
                is_draining: Arc::new(AtomicBool::new(false)),
            },
            lb_router: Arc::new(
                LoadBalancerRouter::from_config(&lb_config)
                    .expect("load balancer should initialize"),
            ),
            metrics,
            retry: ProxyRetryConfig::default(),
        }
    }

    fn insert_required_proxy_headers(headers: &mut HeaderMap) {
        headers.insert(
            HeaderName::from_static(HEADER_REQUEST_ID),
            HeaderValue::from_static("req-test"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_INPUT_TOKENS),
            HeaderValue::from_static("128"),
        );
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

    fn routed_instance_snapshot(
        cluster_id: &str,
        inference_server_id: &str,
    ) -> RoutedInferenceServerSnapshot {
        RoutedInferenceServerSnapshot {
            cluster_id: cluster_id.to_string(),
            inference_server_id: inference_server_id.to_string(),
            inference_server_url: "quic://127.0.0.1:5000".to_string(),
            stats: ModelStats::default(),
            rtt: Duration::from_millis(1),
            snapshot_updated_at: Instant::now(),
            status: InferenceServerStatus::Active,
            reverse_tunnel: false,
            delivery_target: DeliveryTarget::Local {
                inference_server_id: inference_server_id.to_string(),
            },
        }
    }

    fn metrics_text(metrics: &StargateMetrics) -> String {
        let metric_families = metrics.registry().gather();
        let mut buffer = Vec::new();
        prometheus::TextEncoder::new()
            .encode(&metric_families, &mut buffer)
            .expect("metrics should encode");
        std::str::from_utf8(&buffer)
            .expect("Prometheus text should be UTF-8")
            .to_string()
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

    #[tokio::test]
    async fn routing_selection_duration_metric_preserves_routing_key_label() {
        let app = test_proxy_app_state();
        let target = RoutingTargetKey {
            routing_key: Some("tenant-a".to_string()),
            model_id: "model-a".to_string(),
        };
        let mut proxy_run = ProxyRequestRun::new(&app, &target, Instant::now(), None);
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
    async fn load_balancer_request_excludes_failed_clusters() {
        let app = test_proxy_app_state();
        let target = RoutingTargetKey {
            routing_key: Some("tenant-a".to_string()),
            model_id: "model-a".to_string(),
        };
        let request_inputs = ProxyRequestInputs {
            target: target.clone(),
            input_tokens: 128,
            priority: 0,
            max_wait_ms: None,
            request_slo_ms: None,
            cache_affinity_key: None,
            routing_algorithm_override: None,
        };
        let mut proxy_run = ProxyRequestRun::new(&app, &target, Instant::now(), None);

        proxy_run.fail_cluster("cluster-a".to_string());
        let request = proxy_run.load_balancer_request(&request_inputs);
        let excluded = request
            .excluded_cluster_ids
            .expect("failed cluster should be excluded from subsequent routing");

        assert_eq!(excluded.len(), 1);
        assert!(excluded.contains("cluster-a"));
    }

    #[test]
    fn selected_instance_trace_fields_exclude_url_and_include_pulsar_metrics() {
        let snapshot = RoutedInferenceServerSnapshot {
            cluster_id: "cluster-a".to_string(),
            inference_server_id: "inst-a".to_string(),
            inference_server_url: "quic://127.0.0.1:5000".to_string(),
            stats: ModelStats {
                output_tps: 20.0,
                last_mean_input_tps: 30.0,
                max_output_tps: 40.0,
                queue_size: 5,
                queued_input_size: 6,
                kv_cache_capacity_tokens: 4096,
                kv_cache_used_tokens: 1024,
                kv_cache_free_tokens: 3072,
                num_running_queries: 3,
                max_engine_concurrency: 8,
                total_query_input_size: 512,
                queue_time_estimate_ms_by_priority: std::collections::HashMap::new(),
                ..ModelStats::default()
            },
            rtt: Duration::from_millis(12),
            snapshot_updated_at: Instant::now(),
            status: InferenceServerStatus::Active,
            reverse_tunnel: false,
            delivery_target: DeliveryTarget::Local {
                inference_server_id: "inst-a".to_string(),
            },
        };
        let cluster = RoutedClusterSnapshot {
            cluster_id: "cluster-a".to_string(),
            stats: ModelStats {
                output_tps: 20.0,
                last_mean_input_tps: 30.0,
                max_output_tps: 40.0,
                queue_size: 5,
                queued_input_size: 6,
                kv_cache_capacity_tokens: 4096,
                kv_cache_used_tokens: 1024,
                kv_cache_free_tokens: 3072,
                ..ModelStats::default()
            },
            rtt: Duration::from_millis(12),
            snapshot_updated_at: Instant::now(),
            status: InferenceServerStatus::Active,
            active_backend_count: 1,
        };

        let fields = SelectedInstanceTraceFields::from_route(&cluster, &snapshot);
        assert_eq!(fields.inference_server_id, "inst-a");
        assert_eq!(fields.kv_cache_capacity_tokens, 4096);
        assert_eq!(fields.kv_cache_used_tokens, 1024);
        assert_eq!(fields.kv_cache_free_tokens, 3072);
        assert_eq!(fields.rtt_ms, 12.0);
        assert!(fields.snapshot_age_ms >= 0.0);
    }

    #[test]
    fn traceparent_header_extracts_remote_parent_context() {
        global::set_text_map_propagator(TraceContextPropagator::new());

        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static("traceparent"),
            HeaderValue::from_static("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
        );

        let parent_context = parent_context_from_headers(&headers);
        let span_context = parent_context.span().span_context().clone();

        assert!(span_context.is_valid());
        assert!(span_context.is_remote());
        assert_eq!(
            span_context.trace_id().to_string(),
            "4bf92f3577b34da6a3ce929d0e0e4736"
        );
        assert_eq!(span_context.span_id().to_string(), "00f067aa0ba902b7");
    }

    #[test]
    fn traceparent_header_injects_upstream_attempt_context() {
        global::set_text_map_propagator(TraceContextPropagator::new());

        let mut source_headers = HeaderMap::new();
        source_headers.insert(
            HeaderName::from_static("traceparent"),
            HeaderValue::from_static("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
        );
        let context = parent_context_from_headers(&source_headers);

        let mut forwarded_headers = HeaderMap::new();
        forwarded_headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );
        inject_trace_context(&mut forwarded_headers, &context);

        assert_eq!(
            forwarded_headers.get("traceparent"),
            source_headers.get("traceparent")
        );
        assert_eq!(
            forwarded_headers.get(HEADER_MODEL),
            Some(&HeaderValue::from_static("model-a"))
        );
    }

    #[test]
    fn retry_requires_explicit_pylon_signal_by_default() {
        let retry = ProxyRetryConfig::default();
        let bare_headers = HeaderMap::new();
        assert!(!should_retry_upstream_response(
            StatusCode::TOO_MANY_REQUESTS,
            &bare_headers,
            &retry
        ));

        let mut retryable_headers = HeaderMap::new();
        retryable_headers.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRYABLE),
            HeaderValue::from_static("true"),
        );
        assert!(should_retry_upstream_response(
            StatusCode::TOO_MANY_REQUESTS,
            &retryable_headers,
            &retry
        ));
    }

    #[test]
    fn retry_signal_is_ignored_for_non_retryable_status() {
        let retry = ProxyRetryConfig::default();
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRYABLE),
            HeaderValue::from_static("true"),
        );

        assert!(!should_retry_upstream_response(
            StatusCode::BAD_REQUEST,
            &headers,
            &retry
        ));
    }

    #[test]
    fn only_explicit_queue_mismatch_rejection_releases_optimistic_reservation() {
        let mut headers = HeaderMap::new();
        headers.insert(HEADER_STARGATE_RETRYABLE, HeaderValue::from_static("true"));
        headers.insert(
            HEADER_STARGATE_RETRY_REASON,
            HeaderValue::from_static(RETRY_REASON_QUEUE_ESTIMATE_MISMATCH),
        );

        assert!(should_release_queue_mismatch_reservation(
            StatusCode::TOO_MANY_REQUESTS,
            &headers
        ));
        assert!(!should_release_queue_mismatch_reservation(
            StatusCode::SERVICE_UNAVAILABLE,
            &headers
        ));

        headers.insert(
            HEADER_STARGATE_RETRY_REASON,
            HeaderValue::from_static("upstream_admission_rejected"),
        );
        assert!(!should_release_queue_mismatch_reservation(
            StatusCode::TOO_MANY_REQUESTS,
            &headers
        ));
    }

    #[test]
    fn explicit_non_retryable_signal_blocks_status_only_retry() {
        let retry = ProxyRetryConfig {
            require_pylon_retry_signal: false,
            ..ProxyRetryConfig::default()
        };
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRYABLE),
            HeaderValue::from_static("false"),
        );

        assert!(!should_retry_upstream_response(
            StatusCode::SERVICE_UNAVAILABLE,
            &headers,
            &retry
        ));
    }

    #[test]
    fn upstream_response_retry_decision_retries_when_budget_limit_and_replay_allow() {
        let retry = ProxyRetryConfig::default();
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRYABLE),
            HeaderValue::from_static("true"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRY_REASON),
            HeaderValue::from_static("upstream_overloaded"),
        );

        assert_eq!(
            decide_upstream_response_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &headers,
                &retry,
                true,
                0,
                ReplayReadiness::Ready,
            ),
            UpstreamResponseRetryDecision::RetryAlternateCluster {
                retry_reason: "upstream_overloaded".to_string()
            }
        );
    }

    #[test]
    fn queue_mismatch_retry_decision_retries_a_sibling_before_excluding_the_cluster() {
        let retry = ProxyRetryConfig::default();
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRYABLE),
            HeaderValue::from_static("true"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRY_REASON),
            HeaderValue::from_static(RETRY_REASON_QUEUE_ESTIMATE_MISMATCH),
        );

        assert_eq!(
            decide_upstream_response_retry(
                StatusCode::TOO_MANY_REQUESTS,
                &headers,
                &retry,
                true,
                0,
                ReplayReadiness::Ready,
            ),
            UpstreamResponseRetryDecision::RetryAlternateBackend {
                retry_reason: RETRY_REASON_QUEUE_ESTIMATE_MISMATCH.to_string()
            }
        );
    }

    #[test]
    fn upstream_response_retry_decision_preserves_exhaustion_precedence() {
        let retry = ProxyRetryConfig::default();
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRYABLE),
            HeaderValue::from_static("true"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRY_REASON),
            HeaderValue::from_static("upstream_overloaded"),
        );

        assert_eq!(
            decide_upstream_response_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &headers,
                &retry,
                false,
                0,
                ReplayReadiness::Ready,
            ),
            UpstreamResponseRetryDecision::ReturnFinalRetryBudgetExhausted
        );
        assert_eq!(
            decide_upstream_response_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &headers,
                &retry,
                true,
                retry.max_request_retries,
                ReplayReadiness::PayloadTooLarge,
            ),
            UpstreamResponseRetryDecision::ReturnFinalRetryExhausted {
                retry_reason: "upstream_overloaded".to_string()
            }
        );
    }

    #[test]
    fn proxy_error_retry_decision_retries_when_budget_limit_and_replay_allow() {
        let retry = ProxyRetryConfig::default();

        assert_eq!(
            decide_proxy_error_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &retry,
                true,
                0,
                ReplayReadiness::Ready,
            ),
            ProxyErrorRetryDecision::RetryConnectionOrFailover
        );
    }

    #[test]
    fn proxy_error_retry_decision_preserves_exhaustion_and_status_precedence() {
        let retry = ProxyRetryConfig::default();

        assert_eq!(
            decide_proxy_error_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &retry,
                false,
                0,
                ReplayReadiness::Ready,
            ),
            ProxyErrorRetryDecision::ReturnFinalRetryBudgetExhausted
        );
        assert_eq!(
            decide_proxy_error_retry(
                StatusCode::SERVICE_UNAVAILABLE,
                &retry,
                true,
                retry.max_connect_retries,
                ReplayReadiness::PayloadTooLarge,
            ),
            ProxyErrorRetryDecision::ReturnFinalConnectRetriesExhausted
        );
        assert_eq!(
            decide_proxy_error_retry(
                StatusCode::BAD_REQUEST,
                &retry,
                true,
                0,
                ReplayReadiness::PayloadTooLarge,
            ),
            ProxyErrorRetryDecision::ReturnFinal
        );
    }

    #[test]
    fn proxy_error_retry_decision_reports_replay_incomplete_reason() {
        let retry = ProxyRetryConfig::default();

        assert_eq!(
            decide_proxy_error_retry(
                StatusCode::BAD_GATEWAY,
                &retry,
                true,
                0,
                ReplayReadiness::Incomplete,
            ),
            ProxyErrorRetryDecision::ReturnFinalReplayIncomplete {
                retry_reason: "retryable_proxy_error".to_string()
            }
        );
    }

    #[test]
    fn retry_budget_header_zero_blocks_retry() {
        let retry = ProxyRetryConfig::default();
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(DEFAULT_RETRY_BUDGET_MS_HEADER),
            HeaderValue::from_static("0"),
        );

        let deadline = retry_budget_deadline(&headers, &retry, Instant::now()).unwrap();
        assert!(!retry_budget_has_remaining(deadline));
    }

    #[test]
    fn retry_budget_header_absent_allows_retry() {
        let retry = ProxyRetryConfig::default();
        let headers = HeaderMap::new();

        let deadline = retry_budget_deadline(&headers, &retry, Instant::now()).unwrap();
        assert!(retry_budget_has_remaining(deadline));
    }

    #[test]
    fn retry_budget_header_rejects_invalid_values() {
        let retry = ProxyRetryConfig::default();
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(DEFAULT_RETRY_BUDGET_MS_HEADER),
            HeaderValue::from_static("not-a-number"),
        );

        assert_eq!(
            retry_budget_deadline(&headers, &retry, Instant::now()),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn optional_u64_proxy_headers_reject_invalid_values() {
        for header in [
            HEADER_INPUT_TOKENS,
            HEADER_MAX_WAIT_MS,
            HEADER_REQUEST_SLO_MS,
        ] {
            let mut headers = HeaderMap::new();
            headers.insert(
                HeaderName::from_static(header),
                HeaderValue::from_static("bad"),
            );

            assert_eq!(
                parse_optional_u64_header(&headers, header),
                Err(StatusCode::BAD_REQUEST)
            );
        }
    }

    #[test]
    fn optional_u32_proxy_headers_reject_invalid_values() {
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(HEADER_PRIORITY),
            HeaderValue::from_static("bad"),
        );

        assert_eq!(
            parse_optional_u32_header(&headers, HEADER_PRIORITY),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn optional_numeric_proxy_headers_parse_valid_or_absent_values() {
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(HEADER_INPUT_TOKENS),
            HeaderValue::from_static("42"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_PRIORITY),
            HeaderValue::from_static("7"),
        );

        assert_eq!(
            parse_optional_u64_header(&headers, HEADER_INPUT_TOKENS),
            Ok(Some(42))
        );
        assert_eq!(
            parse_optional_u32_header(&headers, HEADER_PRIORITY),
            Ok(Some(7))
        );
        assert_eq!(
            parse_optional_u64_header(&headers, HEADER_MAX_WAIT_MS),
            Ok(None)
        );
    }

    #[test]
    fn proxy_request_inputs_parse_routing_and_control_headers() {
        let mut headers = HeaderMap::new();
        insert_required_proxy_headers(&mut headers);
        headers.insert(
            HeaderName::from_static(HEADER_ROUTING_KEY),
            HeaderValue::from_static("tenant-a"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_INPUT_TOKENS),
            HeaderValue::from_static("128"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_PRIORITY),
            HeaderValue::from_static("4"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_MAX_WAIT_MS),
            HeaderValue::from_static("250"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_REQUEST_SLO_MS),
            HeaderValue::from_static("900"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_CACHE_AFFINITY_KEY),
            HeaderValue::from_static("cache-key-a"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_ROUTING_METHOD),
            HeaderValue::from_static("round_robin"),
        );

        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");

        assert_eq!(inputs.target.routing_key.as_deref(), Some("tenant-a"));
        assert_eq!(inputs.target.model_id, "model-a");
        assert_eq!(inputs.input_tokens, 128);
        assert_eq!(inputs.priority, 4);
        assert_eq!(inputs.max_wait_ms, Some(250));
        assert_eq!(inputs.request_slo_ms, Some(900));
        assert_eq!(inputs.cache_affinity_key.as_deref(), Some("cache-key-a"));
        assert!(inputs.routing_algorithm_override.is_some());
    }

    #[test]
    fn proxy_missing_routing_method_uses_configured_default_algorithm() {
        let lb_router = LoadBalancerRouter::from_config(&LoadBalancerConfig {
            default: LoadBalancerAlgorithm::RoundRobin,
            request_algorithms: HashMap::new(),
            models: HashMap::new(),
        })
        .expect("load balancer should initialize");
        let mut headers = HeaderMap::new();
        insert_required_proxy_headers(&mut headers);
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );
        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");

        let config = lb_router
            .resolve_algorithm_override(
                &inputs.target.model_id,
                inputs.routing_algorithm_override.as_ref(),
            )
            .expect("missing routing method should use configured default");

        assert_eq!(
            config.config().algorithm(),
            LoadBalancerAlgorithm::RoundRobin
        );
    }

    #[test]
    fn proxy_valid_configured_routing_method_uses_request_algorithm() {
        let lb_router = LoadBalancerRouter::from_config(&LoadBalancerConfig {
            default: LoadBalancerAlgorithm::PowerOfTwo,
            request_algorithms: HashMap::from([(
                LoadBalancerAlgorithm::RoundRobin,
                LoadBalancerModelConfig::Name(LoadBalancerAlgorithm::RoundRobin),
            )]),
            models: HashMap::new(),
        })
        .expect("load balancer should initialize");
        let mut headers = HeaderMap::new();
        insert_required_proxy_headers(&mut headers);
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_ROUTING_METHOD),
            HeaderValue::from_static("round_robin"),
        );
        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");

        let config = lb_router
            .resolve_algorithm_override(
                &inputs.target.model_id,
                inputs.routing_algorithm_override.as_ref(),
            )
            .expect("configured routing method should be available");

        assert_eq!(
            config.config().algorithm(),
            LoadBalancerAlgorithm::RoundRobin
        );
    }

    #[test]
    fn proxy_unknown_routing_method_returns_bad_request() {
        let mut headers = HeaderMap::new();
        insert_required_proxy_headers(&mut headers);
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_ROUTING_METHOD),
            HeaderValue::from_static("sticky"),
        );

        assert_eq!(
            parse_proxy_request_inputs(&headers),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn proxy_blank_routing_method_returns_bad_request() {
        let mut headers = HeaderMap::new();
        insert_required_proxy_headers(&mut headers);
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_ROUTING_METHOD),
            HeaderValue::from_static("   "),
        );

        assert_eq!(
            parse_proxy_request_inputs(&headers),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn proxy_known_unconfigured_routing_method_returns_bad_request() {
        let lb_router = LoadBalancerRouter::from_config(&LoadBalancerConfig::default())
            .expect("load balancer should initialize");
        let mut headers = HeaderMap::new();
        insert_required_proxy_headers(&mut headers);
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_ROUTING_METHOD),
            HeaderValue::from_static("round-robin"),
        );
        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");
        let error = lb_router
            .resolve_algorithm_override(
                &inputs.target.model_id,
                inputs.routing_algorithm_override.as_ref(),
            )
            .expect_err("unconfigured routing method should fail");

        assert_eq!(
            reject_invalid_routing_algorithm(&inputs.target, &error),
            StatusCode::BAD_REQUEST
        );
        assert_eq!(error.reason(), "unavailable");
    }

    #[test]
    fn proxy_request_inputs_reject_missing_model() {
        let mut headers = HeaderMap::new();
        insert_required_proxy_headers(&mut headers);

        assert_eq!(
            parse_proxy_request_inputs(&headers),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn proxy_request_inputs_reject_missing_request_id() {
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_INPUT_TOKENS),
            HeaderValue::from_static("128"),
        );

        assert_eq!(
            parse_proxy_request_inputs(&headers),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn proxy_request_inputs_reject_missing_input_tokens() {
        let mut headers = HeaderMap::new();
        headers.insert(
            HeaderName::from_static(HEADER_REQUEST_ID),
            HeaderValue::from_static("req-test"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );

        assert_eq!(
            parse_proxy_request_inputs(&headers),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn load_balancer_request_requirements_reject_missing_cache_affinity_key() {
        let mut headers = HeaderMap::new();
        insert_required_proxy_headers(&mut headers);
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_INPUT_TOKENS),
            HeaderValue::from_static("128"),
        );
        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");
        let mut config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
        config.request_policy_mut().require_cache_affinity_key = true;

        assert_eq!(
            validate_load_balancer_request_requirements(&config, &inputs),
            Err(StatusCode::BAD_REQUEST)
        );
    }

    #[test]
    fn input_work_admission_rejects_overloaded_pool() {
        let mut candidate = cluster_candidate("cluster-a");
        candidate.stats.queued_input_size = 300;
        candidate.stats.last_mean_input_tps = 100.0;
        let config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PowerOfTwo);
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model-a".to_string(),
        };
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
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model-a".to_string(),
        };
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
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model-a".to_string(),
        };
        let request = input_work_admission_request(&target, 50);

        assert_eq!(
            input_work_admission_rejection_reason(&config, &request, &[candidate], 3.0),
            Some(ADMISSION_REASON_INPUT_WORK_CAPACITY_UNAVAILABLE)
        );
    }

    #[test]
    fn input_work_admission_for_pulsar_includes_low_free_kv_capacity() {
        let config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model-a".to_string(),
        };
        let request = input_work_admission_request(&target, 100);
        let mut free_kv = cluster_candidate("free-kv");
        free_kv.stats.queued_input_size = 50;
        free_kv.stats.last_mean_input_tps = 100.0;
        free_kv.stats.kv_cache_capacity_tokens = 1024;
        free_kv.stats.kv_cache_used_tokens = 768;
        free_kv.stats.kv_cache_free_tokens = 256;
        let mut likely_warm = cluster_candidate("likely-warm");
        likely_warm.stats.queued_input_size = 900;
        likely_warm.stats.last_mean_input_tps = 1000.0;
        likely_warm.stats.kv_cache_capacity_tokens = 1024;
        likely_warm.stats.kv_cache_used_tokens = 974;
        likely_warm.stats.kv_cache_free_tokens = 50;

        assert_eq!(
            input_work_admission_rejection_reason(&config, &request, &[free_kv, likely_warm], 1.0,),
            None
        );
    }

    #[test]
    fn input_work_admission_for_pulsar_excludes_low_free_kv_when_considered() {
        let mut config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
        config.request_policy_mut().consider_kv_free_tokens = true;
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model-a".to_string(),
        };
        let request = input_work_admission_request(&target, 100);
        let mut low_free_kv = cluster_candidate("low-free-kv");
        low_free_kv.stats.queued_input_size = 0;
        low_free_kv.stats.last_mean_input_tps = 1000.0;
        low_free_kv.stats.kv_cache_capacity_tokens = 1024;
        low_free_kv.stats.kv_cache_used_tokens = 974;
        low_free_kv.stats.kv_cache_free_tokens = 50;

        assert_eq!(
            input_work_admission_rejection_reason(&config, &request, &[low_free_kv], 1.0),
            Some(ADMISSION_REASON_INPUT_WORK_CAPACITY_UNAVAILABLE)
        );
    }

    #[test]
    fn load_balancer_request_requirements_accept_satisfied_controls() {
        let mut headers = HeaderMap::new();
        insert_required_proxy_headers(&mut headers);
        headers.insert(
            HeaderName::from_static(HEADER_MODEL),
            HeaderValue::from_static("model-a"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_INPUT_TOKENS),
            HeaderValue::from_static("128"),
        );
        headers.insert(
            HeaderName::from_static(HEADER_CACHE_AFFINITY_KEY),
            HeaderValue::from_static("cache-key-a"),
        );
        let inputs = parse_proxy_request_inputs(&headers).expect("headers should parse");
        let mut config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
        config.request_policy_mut().require_cache_affinity_key = true;
        config.request_policy_mut().require_input_tokens = true;

        assert_eq!(
            validate_load_balancer_request_requirements(&config, &inputs),
            Ok(())
        );
    }

    #[test]
    fn no_routing_choice_retries_only_with_eligible_candidates_and_budget() {
        assert_eq!(
            classify_no_routing_choice(NoRoutingChoiceInputs {
                num_candidates: 2,
                eligible_candidate_count: 1,
                target_registered: false,
                failed_backend_count: 0,
                failed_cluster_count: 0,
                retry_allowed: true,
            }),
            NoRoutingChoiceAction::RetryRouting
        );
        assert_eq!(
            classify_no_routing_choice(NoRoutingChoiceInputs {
                num_candidates: 2,
                eligible_candidate_count: 0,
                target_registered: false,
                failed_backend_count: 1,
                failed_cluster_count: 1,
                retry_allowed: true,
            }),
            NoRoutingChoiceAction::Finalize(NoRoutingFinalization::ServiceUnavailable)
        );
        assert_eq!(
            classify_no_routing_choice(NoRoutingChoiceInputs {
                num_candidates: 2,
                eligible_candidate_count: 1,
                target_registered: false,
                failed_backend_count: 0,
                failed_cluster_count: 0,
                retry_allowed: false,
            }),
            NoRoutingChoiceAction::Finalize(NoRoutingFinalization::ServiceUnavailable)
        );
    }

    #[test]
    fn no_routing_choice_finalizes_empty_route_as_not_found() {
        assert_eq!(
            classify_no_routing_choice(NoRoutingChoiceInputs {
                num_candidates: 0,
                eligible_candidate_count: 0,
                target_registered: false,
                failed_backend_count: 0,
                failed_cluster_count: 0,
                retry_allowed: true,
            }),
            NoRoutingChoiceAction::Finalize(NoRoutingFinalization::NoCandidatesNotFound)
        );
    }

    #[test]
    fn no_routing_choice_finalizes_registered_empty_route_as_unavailable() {
        assert_eq!(
            classify_no_routing_choice(NoRoutingChoiceInputs {
                num_candidates: 0,
                eligible_candidate_count: 0,
                target_registered: true,
                failed_backend_count: 0,
                failed_cluster_count: 0,
                retry_allowed: true,
            }),
            NoRoutingChoiceAction::Finalize(NoRoutingFinalization::ServiceUnavailable)
        );
    }

    #[test]
    fn no_routing_choice_finalizes_failed_empty_route_as_unavailable() {
        assert_eq!(
            classify_no_routing_choice(NoRoutingChoiceInputs {
                num_candidates: 0,
                eligible_candidate_count: 0,
                target_registered: false,
                failed_backend_count: 1,
                failed_cluster_count: 0,
                retry_allowed: true,
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
    fn internal_stargate_headers_are_not_forwarded_to_downstream_clients()
    -> std::result::Result<(), axum::http::header::InvalidHeaderName> {
        assert!(!should_forward_header(&HeaderName::from_bytes(
            b"Connection"
        )?));
        assert!(!should_forward_header(&HeaderName::from_bytes(
            b"Proxy-Connection"
        )?));
        assert!(!should_forward_header(&HeaderName::from_bytes(b"Host")?));
        assert!(!should_forward_header(&HeaderName::from_static(
            HEADER_ROUTING_METHOD
        )));
        assert!(!should_forward_header(&HeaderName::from_static(
            HEADER_STARGATE_RETRYABLE
        )));
        assert!(!should_forward_header(&HeaderName::from_static(
            HEADER_STARGATE_RETRY_REASON
        )));
        assert!(!should_forward_header(&HeaderName::from_static(
            HEADER_STARGATE_RETRY_AFTER_MS
        )));
        assert!(!should_forward_header(&HeaderName::from_static(
            HEADER_STARGATE_ERROR_CODE
        )));
        assert!(!should_forward_header(&HeaderName::from_static(
            HEADER_STARGATE_EXPECTED_QUEUE_MS
        )));
        assert!(should_forward_header(&HeaderName::from_bytes(
            b"X-Custom-Request"
        )?));
        Ok(())
    }

    #[test]
    fn copy_forwardable_headers_strips_internal_error_code() {
        let mut upstream = HeaderMap::new();
        upstream.insert(
            HeaderName::from_static(HEADER_STARGATE_ERROR_CODE),
            HeaderValue::from_static(ERROR_NO_ELIGIBLE_CANDIDATES),
        );
        upstream.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRYABLE),
            HeaderValue::from_static("true"),
        );
        upstream.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRY_REASON),
            HeaderValue::from_static("retryable_proxy_error"),
        );
        upstream.insert(
            HeaderName::from_static(HEADER_STARGATE_RETRY_AFTER_MS),
            HeaderValue::from_static("25"),
        );
        upstream.insert(
            HeaderName::from_static(HEADER_STARGATE_EXPECTED_QUEUE_MS),
            HeaderValue::from_static("123"),
        );
        upstream.insert(
            HeaderName::from_static("x-upstream-header"),
            HeaderValue::from_static("preserved"),
        );

        let mut downstream = HeaderMap::new();
        copy_forwardable_headers(&upstream, &mut downstream);

        assert!(!downstream.contains_key(HEADER_STARGATE_ERROR_CODE));
        assert!(!downstream.contains_key(HEADER_STARGATE_RETRYABLE));
        assert!(!downstream.contains_key(HEADER_STARGATE_RETRY_REASON));
        assert!(!downstream.contains_key(HEADER_STARGATE_RETRY_AFTER_MS));
        assert!(!downstream.contains_key(HEADER_STARGATE_EXPECTED_QUEUE_MS));
        assert_eq!(
            downstream.get("x-upstream-header"),
            Some(&HeaderValue::from_static("preserved"))
        );
    }

    #[tokio::test]
    async fn replay_body_is_incomplete_until_first_body_reaches_eof() {
        let body = Body::from_stream(async_stream::stream! {
            yield Ok::<_, std::io::Error>(Bytes::from_static(b"partial"));
            futures::future::pending::<()>().await;
        });
        let mut replay_body = ReplayableRequestBody::new(&HeaderMap::new(), body, 1024).unwrap();

        let attempt_body = replay_body.body_for_attempt().unwrap();
        let mut stream = attempt_body.into_data_stream();
        let chunk = tokio::time::timeout(Duration::from_secs(1), stream.next())
            .await
            .expect("body chunk timed out")
            .expect("missing body chunk")
            .expect("body chunk failed");

        assert_eq!(chunk, Bytes::from_static(b"partial"));
        assert_eq!(replay_body.buffered_len(), 7);
        assert_eq!(replay_body.replay_readiness(), ReplayReadiness::Incomplete);
        assert_eq!(
            replay_body.body_for_attempt().err(),
            Some(StatusCode::BAD_GATEWAY)
        );
    }

    #[tokio::test]
    async fn replay_body_replays_only_after_first_body_reaches_eof() {
        let body = Body::from("complete");
        let mut replay_body = ReplayableRequestBody::new(&HeaderMap::new(), body, 1024).unwrap();

        let attempt_body = replay_body.body_for_attempt().unwrap();
        let attempt_bytes = axum::body::to_bytes(attempt_body, 1024).await.unwrap();
        assert_eq!(attempt_bytes, Bytes::from_static(b"complete"));
        assert_eq!(replay_body.replay_readiness(), ReplayReadiness::Ready);

        let replayed_body = replay_body.body_for_attempt().unwrap();
        let replayed_bytes = axum::body::to_bytes(replayed_body, 1024).await.unwrap();
        assert_eq!(replayed_bytes, Bytes::from_static(b"complete"));
    }

    #[tokio::test]
    async fn transport_setup_failure_does_not_consume_first_replay_body() {
        let app = test_proxy_app_state();
        let body = Body::from("still-available");
        let mut replay_body = ReplayableRequestBody::new(&HeaderMap::new(), body, 1024).unwrap();

        let result = proxy_via_quic_streaming(
            &app,
            "missing-connection",
            Method::POST,
            "/v1/chat/completions",
            HeaderMap::new(),
            || replay_body.body_for_attempt(),
        )
        .await;

        assert_eq!(result.err(), Some(StatusCode::BAD_GATEWAY));

        let attempt_body = replay_body.body_for_attempt().unwrap();
        let attempt_bytes = axum::body::to_bytes(attempt_body, 1024).await.unwrap();
        assert_eq!(attempt_bytes, Bytes::from_static(b"still-available"));
    }

    #[test]
    fn untouched_first_body_is_ready_for_retry() {
        let body = Body::from("not-yet-polled");
        let replay_body = ReplayableRequestBody::new(&HeaderMap::new(), body, 1024).unwrap();

        assert_eq!(replay_body.replay_readiness(), ReplayReadiness::Ready);
    }

    #[test]
    fn routing_retry_deadline_caps_max_wait_header() {
        let request_start = Instant::now();
        let deadline = routing_retry_deadline(request_start, Some(u64::MAX))
            .expect("capped deadline should be computed");
        assert!(deadline <= request_start + Duration::from_millis(ROUTING_RETRY_MAX_WAIT_MS));
    }
}

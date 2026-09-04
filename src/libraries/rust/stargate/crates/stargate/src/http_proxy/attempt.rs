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

use std::sync::Arc;
use std::time::Instant;

use axum::body::Body;
use axum::http::{HeaderName, HeaderValue, StatusCode};
use axum::response::Response;
use stargate_protocol::tunnel_contract::{
    HEADER_INFERENCE_SERVER_ID as HEADER_CHOSEN_INFERENCE_SERVER_ID, HEADER_STARGATE_RETRY_REASON,
    HEADER_STARGATE_RETRYABLE,
};
use tracing::{Instrument, Span, field, info, warn};

use crate::routing_state::{RoutedInferenceServerSnapshot, RoutingReservation};

use super::ProxyAppState;
use super::retry::{
    FinalRetryDisposition, RetryDecision, UpstreamRetry, decide_proxy_error_retry,
    decide_upstream_response_retry, header_str, retry_budget_has_remaining,
    should_release_queue_mismatch_reservation,
};
use super::run::{ProxyRequestRun, SelectedClusterRun};
use super::upstream::{
    UpstreamStreamingResponse, copy_forwardable_headers, headers_for_upstream_attempt,
    proxy_via_quic_streaming,
};

const HEADER_CHOSEN_INFERENCE_SERVER_URL: &str = "x-inference-server-url";
const HEADER_CHOSEN_CLUSTER_ID: &str = "x-stargate-cluster-id";

#[derive(Default)]
pub(super) struct ProxyAttemptCounters {
    pub(super) attempt: u32,
    connect_retries: u32,
    request_retries: u32,
}

pub(super) enum ProxyAttemptOutcome {
    ReturnFinal(Response<Body>),
    ProxyError(StatusCode),
    RetrySameBackend(Arc<RoutedInferenceServerSnapshot>),
    RetryAlternateBackend(String),
    RetryAlternateCluster(String),
}

impl ProxyRequestRun<'_> {
    fn routing_key(&self) -> Option<&str> {
        self.request.request_inputs.target.routing_key.as_deref()
    }

    fn model_id(&self) -> &str {
        self.request.request_inputs.target.model_id.as_str()
    }

    pub(super) async fn run_proxy_attempt(
        &mut self,
        selected: &SelectedClusterRun,
        chosen: &Arc<RoutedInferenceServerSnapshot>,
    ) -> ProxyAttemptOutcome {
        self.attempt_counters.attempt += 1;
        Span::current().record("proxy.attempt", self.attempt_counters.attempt as i64);
        Span::current().record(
            "proxy.connect_attempts",
            self.attempt_counters.connect_retries as i64,
        );
        Span::current().record(
            "proxy.request_retries",
            self.attempt_counters.request_retries as i64,
        );
        Span::current().record(
            "proxy.failed_backends",
            self.failed_backend_ids.len() as i64,
        );

        if !chosen.reverse_tunnel
            && self.attempt_counters.connect_retries < self.app.retry.max_connect_retries
            && !self
                .app
                .quic_proxy
                .has_healthy_connection(&chosen.registration)
        {
            self.attempt_counters.connect_retries += 1;
            match reconnect_direct(self.app, chosen, "stale_connection").await {
                Ok(()) => self.record_retry("hot_path_reconnect"),
                Err(error) => {
                    warn!(
                        inference_server_id = %chosen.inference_server_id,
                        error = %error,
                        connect_retries = self.attempt_counters.connect_retries,
                        "failed to reconnect stale QUIC upstream"
                    );
                    return ProxyAttemptOutcome::RetryAlternateBackend(
                        chosen.inference_server_id.clone(),
                    );
                }
            }
        }

        let reservation: Option<RoutingReservation> = selected.cluster.reserve_backend(
            &chosen.registration,
            self.request.request_inputs.input_tokens,
            self.request.request_inputs.priority,
        );
        record_proxy_attempt_start(self, selected, chosen);

        let upstream_span = proxy_upstream_attempt_span(self, selected, chosen);
        Span::current().record(
            "proxy.queue.expected_ms",
            selected.expected_queue_ms.unwrap_or_default() as i64,
        );
        Span::current().record(
            "proxy.queue.expected_present",
            selected.expected_queue_ms.is_some(),
        );
        let attempt_headers = headers_for_upstream_attempt(
            &self.request.forwarded_headers,
            &upstream_span,
            selected.expected_queue_ms,
        );
        let upstream_start = Instant::now();
        let upstream = proxy_via_quic_streaming(
            self.app,
            &chosen.registration,
            self.request.method.clone(),
            &self.request.path_and_query,
            attempt_headers,
            || self.request.replay_body.body_for_attempt(),
        )
        .instrument(upstream_span.clone())
        .await;
        record_proxy_attempt_result(self, chosen, &upstream, &upstream_span, upstream_start);

        let upstream = match upstream {
            Ok(upstream) => upstream,
            Err(status) => {
                match decide_proxy_error_retry(
                    status,
                    &self.app.retry,
                    retry_budget_has_remaining(self.request.retry_deadline),
                    self.attempt_counters.connect_retries,
                    self.request.replay_body.replay_readiness(),
                ) {
                    RetryDecision::Final(disposition) => {
                        return finish_attempt(self, chosen, disposition, Err(status));
                    }
                    RetryDecision::Retry(()) => {}
                }

                self.attempt_counters.connect_retries += 1;
                if !chosen.reverse_tunnel {
                    match reconnect_direct(self.app, chosen, "proxy_error").await {
                        Ok(()) => {
                            self.record_retry("hot_path_reconnect");
                            warn!(
                                inference_server_id = %chosen.inference_server_id,
                                connect_retries = self.attempt_counters.connect_retries,
                                "reconnected QUIC upstream after proxy failure"
                            );
                            return ProxyAttemptOutcome::RetrySameBackend(Arc::clone(chosen));
                        }
                        Err(error) => warn!(
                            inference_server_id = %chosen.inference_server_id,
                            error = %error,
                            connect_retries = self.attempt_counters.connect_retries,
                            "failed to reconnect QUIC upstream"
                        ),
                    }
                }
                self.record_retry("connect_failover");
                return ProxyAttemptOutcome::RetryAlternateBackend(
                    chosen.inference_server_id.clone(),
                );
            }
        };

        if should_release_queue_mismatch_reservation(upstream.status, &upstream.headers)
            && let Some(reservation) = reservation
        {
            reservation.release();
        }
        let retry = match decide_upstream_response_retry(
            upstream.status,
            &upstream.headers,
            &self.app.retry,
            retry_budget_has_remaining(self.request.retry_deadline),
            self.attempt_counters.request_retries,
            self.request.replay_body.replay_readiness(),
        ) {
            RetryDecision::Final(disposition) => {
                return finish_attempt(self, chosen, disposition, Ok(upstream));
            }
            RetryDecision::Retry(retry) => retry,
        };
        let (outcome, retry_reason, message) = match retry {
            UpstreamRetry::AlternateBackend(reason) => (
                ProxyAttemptOutcome::RetryAlternateBackend(chosen.inference_server_id.clone()),
                reason,
                "retrying request on a sibling backend after local queue mismatch",
            ),
            UpstreamRetry::AlternateCluster(reason) => (
                ProxyAttemptOutcome::RetryAlternateCluster(chosen.cluster_id.clone()),
                reason,
                "retrying request after retryable upstream response",
            ),
        };
        self.attempt_counters.request_retries += 1;
        self.record_retry(&retry_reason);
        warn!(
            inference_server_id = %chosen.inference_server_id,
            cluster_id = %chosen.cluster_id,
            status = %upstream.status,
            request_retries = self.attempt_counters.request_retries,
            retry_reason = %retry_reason,
            "{message}"
        );
        outcome
    }

    fn record_retry(&self, reason: &str) {
        Span::current().record("proxy.retry_reason", reason);
        self.app
            .metrics
            .proxy_retries_total(self.routing_key(), self.model_id(), reason)
            .inc();
    }
}

async fn reconnect_direct(
    app: &ProxyAppState,
    chosen: &RoutedInferenceServerSnapshot,
    eviction_reason: &'static str,
) -> anyhow::Result<()> {
    let metrics = &app.metrics;
    metrics
        .quic_connection_evictions_total(&chosen.inference_server_id, eviction_reason)
        .inc();
    let result = app
        .quic_proxy
        .connect_direct_registration(&chosen.registration)
        .await;
    metrics
        .quic_hot_path_reconnect_total(
            &chosen.inference_server_id,
            if result.is_ok() { "success" } else { "error" },
        )
        .inc();
    result
}

fn record_proxy_attempt_start(
    run: &ProxyRequestRun<'_>,
    selected: &SelectedClusterRun,
    chosen: &RoutedInferenceServerSnapshot,
) {
    info!(
        routing_key = ?run.request.request_inputs.target.routing_key,
        model_id = %run.model_id(),
        input_tokens = run.request.request_inputs.input_tokens,
        requested_algorithm = selected.selection.requested_algorithm.as_deref().unwrap_or(""),
        routing_algorithm = %selected.routing_algorithm,
        inference_server_id = %chosen.inference_server_id,
        inference_server_url = %chosen.inference_server_url,
        connect_retries = run.attempt_counters.connect_retries,
        request_retries = run.attempt_counters.request_retries,
        "proxying request"
    );
}

fn proxy_upstream_attempt_span(
    run: &ProxyRequestRun<'_>,
    selected: &SelectedClusterRun,
    chosen: &RoutedInferenceServerSnapshot,
) -> Span {
    tracing::info_span!(
        "proxy_upstream_http_request",
        request.endpoint = run.request.endpoint_name,
        http.method = %run.request.method,
        http.path = %run.request.path_and_query,
        proxy.attempt = run.attempt_counters.attempt as i64,
        selected_cluster.id = %chosen.cluster_id,
        selected_inst.id = %chosen.inference_server_id,
        routing.algorithm = %selected.routing_algorithm,
        proxy.queue.expected_ms = selected.expected_queue_ms.map(|value| value as i64).unwrap_or_default(),
        proxy.queue.expected_present = selected.expected_queue_ms.is_some(),
        proxy.upstream_status = field::Empty,
        proxy.error = field::Empty,
        proxy.time_to_first_byte_ms = field::Empty,
    )
}

fn record_proxy_attempt_result(
    run: &ProxyRequestRun<'_>,
    chosen: &RoutedInferenceServerSnapshot,
    upstream: &Result<UpstreamStreamingResponse, StatusCode>,
    upstream_span: &Span,
    upstream_start: Instant,
) {
    let replay_body_bytes = run.request.replay_body.buffered_len();
    Span::current().record("proxy.replay_body_bytes", replay_body_bytes as i64);
    let metrics = &run.app.metrics;
    metrics
        .proxy_replay_buffer_bytes(run.model_id())
        .observe(replay_body_bytes as f64);

    record_upstream_result_to_span(upstream_span, upstream, upstream_start.elapsed(), true);
    let attempt_result = match upstream {
        Ok(response) => format!("upstream_{}", response.status.as_u16()),
        Err(status) => format!("proxy_{}", status.as_u16()),
    };
    metrics
        .proxy_attempts_total(
            run.routing_key(),
            run.model_id(),
            &chosen.inference_server_id,
            &attempt_result,
        )
        .inc();
    let ttfb = run.request.request_start.elapsed();
    record_upstream_result_to_span(&Span::current(), upstream, ttfb, false);
    if upstream.is_ok() {
        metrics
            .proxy_duration_seconds(
                run.routing_key(),
                run.model_id(),
                &chosen.inference_server_id,
            )
            .observe(ttfb.as_secs_f64());
    }
}

fn finish_attempt(
    run: &ProxyRequestRun<'_>,
    chosen: &RoutedInferenceServerSnapshot,
    disposition: FinalRetryDisposition,
    upstream: Result<UpstreamStreamingResponse, StatusCode>,
) -> ProxyAttemptOutcome {
    let metrics = &run.app.metrics;
    let failure = RequestFailureContext::new(&disposition, &upstream);
    if let Some(retry_reason) = disposition.retry_reason() {
        Span::current().record("proxy.retry_reason", retry_reason);
    }
    let upstream = match disposition {
        FinalRetryDisposition::PassThrough | FinalRetryDisposition::ReplayIncomplete(_) => upstream,
        FinalRetryDisposition::Exhausted(retry_reason) => {
            metrics
                .proxy_retry_exhausted_total(run.routing_key(), run.model_id(), &retry_reason)
                .inc();
            upstream
        }
        FinalRetryDisposition::PayloadTooLarge(_) => Err(StatusCode::PAYLOAD_TOO_LARGE),
    };
    let status = upstream_status(&upstream);
    metrics
        .requests_total(
            run.routing_key(),
            run.model_id(),
            &chosen.inference_server_id,
            &status.as_u16().to_string(),
        )
        .inc();
    if failure.should_log(status) {
        failure.log(run, chosen, status);
    }
    match upstream.and_then(|upstream| build_proxy_response(upstream, chosen)) {
        Ok(response) => ProxyAttemptOutcome::ReturnFinal(response),
        Err(status) => ProxyAttemptOutcome::ProxyError(status),
    }
}

/// Fields captured before the upstream result is consumed, so a final failure
/// can be logged with the backend that produced it and why no retry happened.
struct RequestFailureContext {
    pass_through: bool,
    disposition: &'static str,
    retry_reason: Option<String>,
    source: &'static str,
    upstream_retryable: Option<String>,
    upstream_retry_reason: Option<String>,
}

impl RequestFailureContext {
    fn new(
        disposition: &FinalRetryDisposition,
        upstream: &Result<UpstreamStreamingResponse, StatusCode>,
    ) -> Self {
        let upstream_headers = upstream.as_ref().ok().map(|upstream| &upstream.headers);
        let upstream_header = |name| {
            upstream_headers
                .and_then(|headers| header_str(headers, name))
                .map(str::to_owned)
        };
        Self {
            pass_through: matches!(disposition, FinalRetryDisposition::PassThrough),
            disposition: disposition.label(),
            retry_reason: disposition.retry_reason().map(str::to_owned),
            source: if upstream.is_ok() {
                "upstream_response"
            } else {
                "proxy_error"
            },
            upstream_retryable: upstream_header(HEADER_STARGATE_RETRYABLE),
            upstream_retry_reason: upstream_header(HEADER_STARGATE_RETRY_REASON),
        }
    }

    /// Client errors passed through unchanged are the caller's problem; every
    /// server-side or capacity failure, and every locally decided final status,
    /// must be visible in router logs.
    fn should_log(&self, status: StatusCode) -> bool {
        !self.pass_through || status.is_server_error() || status == StatusCode::TOO_MANY_REQUESTS
    }

    fn log(
        &self,
        run: &ProxyRequestRun<'_>,
        chosen: &RoutedInferenceServerSnapshot,
        status: StatusCode,
    ) {
        warn!(
            routing_key = ?run.request.request_inputs.target.routing_key,
            model_id = %run.model_id(),
            inference_server_id = %chosen.inference_server_id,
            cluster_id = %chosen.cluster_id,
            status = status.as_u16(),
            source = %self.source,
            disposition = %self.disposition,
            retry_reason = %self.retry_reason.as_deref().unwrap_or(""),
            upstream_retryable = %self.upstream_retryable.as_deref().unwrap_or(""),
            upstream_retry_reason = %self.upstream_retry_reason.as_deref().unwrap_or(""),
            attempt = run.attempt_counters.attempt,
            connect_retries = run.attempt_counters.connect_retries,
            request_retries = run.attempt_counters.request_retries,
            failed_backends = run.failed_backend_ids.len(),
            elapsed_ms = run.request.request_start.elapsed().as_millis() as u64,
            "proxied request failed"
        );
    }
}

fn build_proxy_response(
    upstream: UpstreamStreamingResponse,
    chosen: &RoutedInferenceServerSnapshot,
) -> Result<Response<Body>, StatusCode> {
    let mut response = Response::new(upstream.body);
    *response.status_mut() = upstream.status;
    let response_headers = response.headers_mut();
    copy_forwardable_headers(&upstream.headers, response_headers);
    for (name, value) in [
        (
            HEADER_CHOSEN_INFERENCE_SERVER_ID,
            chosen.inference_server_id.as_str(),
        ),
        (
            HEADER_CHOSEN_INFERENCE_SERVER_URL,
            chosen.inference_server_url.as_str(),
        ),
        (HEADER_CHOSEN_CLUSTER_ID, chosen.cluster_id.as_str()),
    ] {
        response_headers.insert(
            HeaderName::from_static(name),
            HeaderValue::from_str(value).map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?,
        );
    }
    Ok(response)
}

fn record_upstream_result_to_span(
    span: &Span,
    result: &Result<UpstreamStreamingResponse, StatusCode>,
    ttfb: std::time::Duration,
    error_field: bool,
) {
    span.record("proxy.time_to_first_byte_ms", ttfb.as_secs_f64() * 1000.0);
    let status = upstream_status(result);
    let field = if result.is_err() && error_field {
        "proxy.error"
    } else {
        "proxy.upstream_status"
    };
    span.record(field, status.as_u16().to_string());
}

fn upstream_status(result: &Result<UpstreamStreamingResponse, StatusCode>) -> StatusCode {
    result
        .as_ref()
        .map_or_else(|status| *status, |response| response.status)
}

#[cfg(test)]
mod tests {
    use axum::http::HeaderMap;
    use tracing::Level;

    use super::super::run::tests::{prepared_request, request_inputs, routed_instance_snapshot};
    use super::super::test_support::test_proxy_app_state;
    use super::*;
    use crate::test_logs::capture_logs;

    fn upstream_response(
        status: StatusCode,
        headers: &[(&'static str, &'static str)],
    ) -> Result<UpstreamStreamingResponse, StatusCode> {
        Ok(UpstreamStreamingResponse {
            status,
            headers: headers
                .iter()
                .map(|(name, value)| {
                    (
                        HeaderName::from_static(name),
                        HeaderValue::from_static(value),
                    )
                })
                .collect::<HeaderMap>(),
            body: Body::empty(),
        })
    }

    struct FinishAttemptCase {
        name: &'static str,
        disposition: FinalRetryDisposition,
        upstream: Result<UpstreamStreamingResponse, StatusCode>,
        expected_log_fields: &'static [&'static str],
    }

    #[tokio::test]
    async fn finish_attempt_logs_final_failures_with_backend_and_disposition() {
        let cases = [
            FinishAttemptCase {
                name: "bodyless 503 passed through without a retry signal",
                disposition: FinalRetryDisposition::PassThrough,
                upstream: upstream_response(StatusCode::SERVICE_UNAVAILABLE, &[]),
                expected_log_fields: &[
                    "proxied request failed",
                    "inference_server_id=inst-a",
                    "cluster_id=cluster-a",
                    "status=503",
                    "source=upstream_response",
                    "disposition=pass_through",
                ],
            },
            FinishAttemptCase {
                name: "429 with retry signal but replay buffer incomplete",
                disposition: FinalRetryDisposition::ReplayIncomplete(
                    "upstream_overloaded".to_string(),
                ),
                upstream: upstream_response(
                    StatusCode::TOO_MANY_REQUESTS,
                    &[
                        (HEADER_STARGATE_RETRYABLE, "true"),
                        (HEADER_STARGATE_RETRY_REASON, "upstream_overloaded"),
                    ],
                ),
                expected_log_fields: &[
                    "status=429",
                    "disposition=replay_incomplete",
                    "retry_reason=upstream_overloaded",
                    "upstream_retryable=true",
                    "upstream_retry_reason=upstream_overloaded",
                ],
            },
            FinishAttemptCase {
                name: "proxy error after connect retries exhausted",
                disposition: FinalRetryDisposition::Exhausted(
                    "connect_retries_exhausted".to_string(),
                ),
                upstream: Err(StatusCode::BAD_GATEWAY),
                expected_log_fields: &[
                    "status=502",
                    "source=proxy_error",
                    "disposition=retry_exhausted",
                    "retry_reason=connect_retries_exhausted",
                ],
            },
            FinishAttemptCase {
                name: "payload too large is a locally decided final status",
                disposition: FinalRetryDisposition::PayloadTooLarge(None),
                upstream: upstream_response(StatusCode::SERVICE_UNAVAILABLE, &[]),
                expected_log_fields: &["status=413", "disposition=payload_too_large"],
            },
        ];

        for case in cases {
            let app = test_proxy_app_state();
            let run = ProxyRequestRun::new(&app, prepared_request(&app, request_inputs()));
            let chosen = routed_instance_snapshot("cluster-a", "inst-a");

            let (_outcome, logs) = capture_logs(Level::WARN, || {
                finish_attempt(&run, &chosen, case.disposition, case.upstream)
            });

            for expected in case.expected_log_fields {
                assert!(
                    logs.contains(expected),
                    "{}: expected {expected:?} in failure log, got:\n{logs}",
                    case.name
                );
            }
        }
    }

    #[tokio::test]
    async fn finish_attempt_stays_quiet_for_passed_through_success_and_client_errors() {
        for (case, status) in [
            ("success", StatusCode::OK),
            ("client error", StatusCode::BAD_REQUEST),
        ] {
            let app = test_proxy_app_state();
            let run = ProxyRequestRun::new(&app, prepared_request(&app, request_inputs()));
            let chosen = routed_instance_snapshot("cluster-a", "inst-a");

            let (_outcome, logs) = capture_logs(Level::WARN, || {
                finish_attempt(
                    &run,
                    &chosen,
                    FinalRetryDisposition::PassThrough,
                    upstream_response(status, &[]),
                )
            });

            assert!(
                logs.is_empty(),
                "{case}: expected no failure log, got:\n{logs}"
            );
        }
    }
}

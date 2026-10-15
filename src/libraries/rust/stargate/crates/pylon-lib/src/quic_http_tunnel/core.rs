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

use std::future::Future;
use std::sync::Arc;
use std::time::{Duration, Instant, SystemTime};

use anyhow::{Context, Result, anyhow, ensure};
use bytes::{Buf, BufMut};
use futures::TryStreamExt;
use reqwest::header::{
    CONTENT_LENGTH, CONTENT_TYPE, HeaderMap, HeaderName, HeaderValue, RETRY_AFTER,
};
use reqwest::{Client, Error as ReqwestError, Method, Response, StatusCode};
use sonic_rs::JsonValueTrait;
use stargate_protocol::common::is_hop_by_hop_header;
use stargate_protocol::tunnel_contract::{
    HEADER_MODEL, HEADER_STARGATE_EXPECTED_QUEUE_MS, HEADER_STARGATE_RETRY_AFTER_MS,
    HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE, HEADER_STARGATE_UPSTREAM_RETRYABLE,
};
use stargate_telemetry::{
    inject_trace_context, parent_context_from_headers, traceparent_from_headers,
};
use tokio_util::{sync::CancellationToken, task::TaskTracker};
use tracing::{Instrument, Span, field};
use tracing_opentelemetry::OpenTelemetrySpanExt;

use super::backend::{self, DEFAULT_PRIORITY_CEILING, UpstreamBackend};
use crate::output_token_parser::OutputTokenParser;
use crate::queue_admission::{
    PylonQueueMismatchRetryConfig, QueueAdmissionDecision, QueueTrackedRequestGuard,
    RETRY_REASON_QUEUE_ESTIMATE_MISMATCH,
};
use crate::request_observer::{
    RequestObservationEndpoint, RequiredTunnelHeaders, TunnelRequestObserver,
    validate_required_tunnel_headers,
};
use crate::request_quality_monitor::{
    RequestOutputTokenProgress, RequestQualityMonitorConfig, RequestQualityRecorder,
};
use crate::runtime_state::{ModelGeneration, PylonRuntimeState, RequestGenerationAdmission};
use crate::sse_message_stream::{
    ParsedSseMessage, RelayOutcome, SseReadTimeoutPhase, UpstreamSseMessageStream,
    UpstreamSseReadError, upstream_sse_message_stream,
};
use crate::stats::PylonMetrics;
use crate::upstream_health::UpstreamHealthPaths;

pub(super) const DEFAULT_MAX_BODY_BYTES: usize = 64 * 1024 * 1024;
// This bounds each upstream SSE event, including its blank-line delimiter.
// Complete events are delivered incrementally, so one input chunk cannot make
// the pylon retain an unbounded batch of parsed events.
pub const DEFAULT_MAX_SSE_BUFFER_BYTES: usize = 1024 * 1024;
pub(super) const DEFAULT_FIRST_OUTPUT_TIMEOUT: Duration = Duration::from_secs(30);
pub(super) const DEFAULT_OUTPUT_CHUNK_TIMEOUT: Duration = Duration::from_secs(30);
pub(super) const MAX_SPECULATIVE_REQUEST_BODY_PREALLOC_BYTES: usize = 64 * 1024;
pub(super) const RETRY_REASON_UPSTREAM_ADMISSION_REJECTED: &str = "upstream_admission_rejected";
pub(super) const RETRY_REASON_LOCAL_CONNECT_FAILURE: &str = "local_connect_failure";
pub(super) const RETRY_REASON_MODEL_GENERATION_UNAVAILABLE: &str = "model_generation_unavailable";
pub(super) const WEBTRANSPORT_STREAM_HEADER_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Clone, Debug)]
pub struct PylonRetryConfig {
    pub retryable_upstream_status_codes: Vec<StatusCode>,
    pub require_upstream_retry_header: bool,
    pub upstream_retry_header: HeaderName,
    pub propagate_retry_after: bool,
    pub local_connect_failures_retryable: bool,
}

impl Default for PylonRetryConfig {
    fn default() -> Self {
        Self {
            retryable_upstream_status_codes: vec![
                StatusCode::TOO_MANY_REQUESTS,
                StatusCode::SERVICE_UNAVAILABLE,
            ],
            require_upstream_retry_header: true,
            upstream_retry_header: HeaderName::from_static(HEADER_STARGATE_UPSTREAM_RETRYABLE),
            propagate_retry_after: true,
            local_connect_failures_retryable: false,
        }
    }
}

#[derive(Clone, Debug)]
pub struct TunnelForwardingConfig {
    pub max_request_body_bytes: usize,
    /// Maximum bytes in one upstream SSE event; completed events are forwarded and released independently of the request-body limit.
    pub max_sse_buffer_bytes: usize,
    pub first_output_timeout: Duration,
    pub output_chunk_timeout: Duration,
    pub runtime_state: PylonRuntimeState,
    pub request_quality_monitor: RequestQualityMonitorConfig,
    pub retry: PylonRetryConfig,
    pub queue_mismatch_retry: PylonQueueMismatchRetryConfig,
    /// Engine dialect spoken to the local upstream; see [`UpstreamBackend`].
    pub upstream_backend: UpstreamBackend,
    /// Priority band ceiling; see [`backend::dynamo::request_priority`].
    pub priority_ceiling: u32,
    /// Shared with bringup so forwarded health probes reach the upstream path
    /// that answered, not the literal path Stargate asked for.
    pub upstream_health_paths: UpstreamHealthPaths,
    pub metrics: Option<Arc<PylonMetrics>>,
    #[cfg(test)]
    pub webtransport_stream_header_wait_tx: Option<flume::Sender<()>>,
}

impl Default for TunnelForwardingConfig {
    fn default() -> Self {
        Self {
            max_request_body_bytes: DEFAULT_MAX_BODY_BYTES,
            max_sse_buffer_bytes: DEFAULT_MAX_SSE_BUFFER_BYTES,
            first_output_timeout: DEFAULT_FIRST_OUTPUT_TIMEOUT,
            output_chunk_timeout: DEFAULT_OUTPUT_CHUNK_TIMEOUT,
            runtime_state: PylonRuntimeState::default(),
            request_quality_monitor: RequestQualityMonitorConfig::default(),
            retry: PylonRetryConfig::default(),
            queue_mismatch_retry: PylonQueueMismatchRetryConfig::default(),
            upstream_backend: UpstreamBackend::default(),
            priority_ceiling: DEFAULT_PRIORITY_CEILING,
            upstream_health_paths: UpstreamHealthPaths::default(),
            metrics: None,
            #[cfg(test)]
            webtransport_stream_header_wait_tx: None,
        }
    }
}

#[derive(Clone)]
pub(super) struct TunnelServerApp {
    pub(super) http_client: Client,
    pub(super) inference_server_id: String,
    pub(super) upstream_http_base_url: String,
    pub(super) max_request_body_bytes: usize,
    pub(super) max_sse_buffer_bytes: usize,
    pub(super) first_output_timeout: Duration,
    pub(super) output_chunk_timeout: Duration,
    pub(super) runtime_state: PylonRuntimeState,
    pub(super) request_quality_monitor: RequestQualityMonitorConfig,
    pub(super) retry: PylonRetryConfig,
    pub(super) queue_mismatch_retry: PylonQueueMismatchRetryConfig,
    pub(super) upstream_backend: UpstreamBackend,
    pub(super) priority_ceiling: u32,
    pub(super) upstream_health_paths: UpstreamHealthPaths,
    pub(super) metrics: Option<Arc<PylonMetrics>>,
    #[cfg(test)]
    pub(super) webtransport_stream_header_wait_tx: Option<flume::Sender<()>>,
}

impl TunnelServerApp {
    pub(super) fn new(
        inference_server_id: String,
        upstream_http_base_url: String,
        forwarding: TunnelForwardingConfig,
    ) -> Self {
        Self {
            http_client: Client::new(),
            inference_server_id,
            upstream_http_base_url,
            max_request_body_bytes: forwarding.max_request_body_bytes,
            max_sse_buffer_bytes: forwarding.max_sse_buffer_bytes,
            first_output_timeout: forwarding.first_output_timeout,
            output_chunk_timeout: forwarding.output_chunk_timeout,
            runtime_state: forwarding.runtime_state,
            request_quality_monitor: forwarding.request_quality_monitor,
            retry: forwarding.retry,
            queue_mismatch_retry: forwarding.queue_mismatch_retry,
            upstream_backend: forwarding.upstream_backend,
            priority_ceiling: forwarding.priority_ceiling,
            upstream_health_paths: forwarding.upstream_health_paths,
            metrics: forwarding.metrics,
            #[cfg(test)]
            webtransport_stream_header_wait_tx: forwarding.webtransport_stream_header_wait_tx,
        }
    }
}

pub(super) async fn serve_bidi_streams<Retained, Handler, HandlerFuture, LogError>(
    retained: Retained,
    app: TunnelServerApp,
    connection: quinn::Connection,
    shutdown: CancellationToken,
    stream_tracker: TaskTracker,
    handler: Handler,
    log_error: LogError,
) where
    Retained: Send + 'static,
    Handler:
        Fn(quinn::SendStream, quinn::RecvStream, TunnelServerApp) -> HandlerFuture + Send + 'static,
    HandlerFuture: Future<Output = Result<()>> + Send + 'static,
    LogError: Fn(anyhow::Error) + Copy + Send + 'static,
{
    let _retained = retained;
    loop {
        tokio::select! {
            _ = shutdown.cancelled() => break,
            stream = connection.accept_bi() => {
                let Ok((send, recv)) = stream else { break };
                let future = handler(send, recv, app.clone());
                stream_tracker.spawn(async move {
                    let Err(error) = future.await else { return };
                    log_error(error);
                });
            }
        }
    }
}

macro_rules! emit_at_info_or_debug {
    ($info:expr, $($fields:tt)*) => {
        if $info {
            tracing::info!($($fields)*);
        } else {
            tracing::debug!($($fields)*);
        }
    };
}

fn queue_decision_logs_at_info(decision: &QueueAdmissionDecision) -> bool {
    matches!(decision, QueueAdmissionDecision::Rejected { .. })
}

fn upstream_response_logs_at_info(status: StatusCode, retryable: bool) -> bool {
    retryable || !status.is_success()
}

fn sse_timeout_phase_name(phase: SseReadTimeoutPhase) -> &'static str {
    match phase {
        SseReadTimeoutPhase::FirstOutput => "first",
        SseReadTimeoutPhase::SubsequentOutput => "subsequent",
    }
}

fn relay_sse_error(error: UpstreamSseReadError) -> ResponseRelayError {
    use UpstreamSseReadError::*;
    match error {
        Timeout(phase) => ResponseRelayError::Upstream(anyhow!(
            "timed out waiting for {} output event from upstream",
            sse_timeout_phase_name(phase)
        )),
        BufferLimitExceeded {
            max_buffer_bytes,
            buffered_bytes,
        } => ResponseRelayError::Upstream(anyhow!(
            "upstream SSE event exceeded the {max_buffer_bytes}-byte buffer limit (buffered {buffered_bytes} bytes)"
        )),
        DeliveryBackpressure(phase) => ResponseRelayError::Downstream(anyhow!(
            "downstream did not accept an SSE event before the {} output deadline",
            sse_timeout_phase_name(phase)
        )),
        Upstream(error) => {
            ResponseRelayError::Upstream(error.context("failed to read upstream response message"))
        }
        Producer(error) => ResponseRelayError::Upstream(
            anyhow::Error::new(error).context("upstream SSE producer task failed"),
        ),
    }
}

#[derive(Debug, thiserror::Error)]
enum ResponseRelayError {
    #[error("upstream response relay failed: {0}")]
    Upstream(#[source] anyhow::Error),
    #[error("downstream response relay failed: {0}")]
    Downstream(#[source] anyhow::Error),
}

struct TunnelRequestLifecycle {
    required: RequiredTunnelHeaders,
    generation: Option<ModelGeneration>,
    observer: Option<TunnelRequestObserver>,
    queue_request: Option<QueueTrackedRequestGuard>,
    quality_check: Option<RequestQualityCheck>,
}

struct RequestQualityCheck {
    recorder: RequestQualityRecorder,
    model_label: HeaderValue,
}

impl TunnelRequestLifecycle {
    fn new(
        app: &TunnelServerApp,
        observation_endpoint: Option<RequestObservationEndpoint>,
        request_headers: &HeaderMap,
        required: RequiredTunnelHeaders,
        generation: Option<ModelGeneration>,
    ) -> Self {
        let observer = observation_endpoint.map(|endpoint| {
            TunnelRequestObserver::accepted(
                endpoint,
                required.clone(),
                generation.clone(),
                app.runtime_state.clone(),
            )
        });
        let quality_check = (observation_endpoint
            == Some(RequestObservationEndpoint::ChatCompletions)
            && app.request_quality_monitor.enabled()
            && app.metrics.is_some())
        .then(|| RequestQualityCheck {
            recorder: RequestQualityRecorder::new(),
            // Preserve the raw label; RequiredTunnelHeaders stores the trimmed identity.
            model_label: request_headers[HEADER_MODEL].clone(),
        });

        Self {
            required,
            generation,
            observer,
            queue_request: None,
            quality_check,
        }
    }

    fn admit_queue(
        &mut self,
        app: &TunnelServerApp,
        request_headers: &HeaderMap,
    ) -> Option<QueueAdmissionDecision> {
        let required = &self.required;
        let decision = app.runtime_state.evaluate_generation_queue_admission(
            &app.queue_mismatch_retry,
            required,
            self.generation.as_ref(),
            request_headers,
        );
        if let Some(metrics) = app.metrics.as_deref() {
            metrics.observe_queue_admission_decision(
                &app.inference_server_id,
                &required.model_id,
                decision.result_label(),
                decision.expected_ms(),
                decision.actual_ms(),
            );
        }
        emit_at_info_or_debug!(
            queue_decision_logs_at_info(&decision),
            queue.expected_ms = decision.expected_ms().unwrap_or_default(),
            queue.expected_present = decision.expected_ms().is_some(),
            queue.actual_ms = decision.actual_ms().unwrap_or_default(),
            queue.actual_present = decision.actual_ms().is_some(),
            queue.admission_result = decision.result_label(),
            queue.mismatch_threshold_ms = decision.threshold_ms().unwrap_or_default(),
            queue.mismatch_threshold_present = decision.threshold_ms().is_some(),
            "evaluated local queue mismatch admission"
        );
        if !matches!(decision, QueueAdmissionDecision::Rejected { .. }) {
            self.queue_request = app
                .runtime_state
                .track_generation_request(required, self.generation.as_ref());
            return None;
        }

        // Observers are created before admission so body validation and terminal
        // accounting keep their existing order. Remove the queue projection before
        // sending the rejection; fail() clears the observed lifecycle projection.
        app.runtime_state.finish_queue_request(&required.request_id);
        self.fail();
        Some(decision)
    }

    fn fail(&mut self) {
        if let Some(observer) = self.observer.as_mut() {
            observer.fail();
        }
    }

    fn cancel(&mut self) {
        if let Some(observer) = self.observer.as_mut() {
            observer.cancel();
        }
    }

    fn on_backend_submission(&mut self, submitted_at: Instant) {
        if let Some(queue_request) = self.queue_request.as_mut() {
            queue_request.on_backend_submission();
        }
        if let Some(observer) = self.observer.as_mut() {
            observer.on_backend_submission(submitted_at);
        }
    }

    async fn relay_sse(
        &mut self,
        mut upstream_messages: UpstreamSseMessageStream,
        transport: &mut impl TunnelRequestTransport,
    ) -> Result<RelayOutcome, ResponseRelayError> {
        let mut output_token_parser = OutputTokenParser::new();
        let mut saw_output = false;
        loop {
            let parsed_message = upstream_messages
                .try_next()
                .await
                .map_err(relay_sse_error)?;
            let Some(parsed_message) = parsed_message else {
                if saw_output {
                    return Ok(RelayOutcome::Complete);
                }
                return Err(ResponseRelayError::Upstream(anyhow!(
                    "upstream SSE stream ended before generated output or a terminal event"
                )));
            };
            let terminal = self.observe_sse_message(
                &mut output_token_parser,
                &parsed_message,
                &mut saw_output,
            );
            transport
                .send_body_event(parsed_message.raw_event)
                .await
                .map_err(ResponseRelayError::Downstream)?;
            if let Some(terminal) = terminal {
                return Ok(terminal);
            }
        }
    }

    fn observe_sse_message(
        &mut self,
        parser: &mut OutputTokenParser,
        message: &ParsedSseMessage,
        saw_output: &mut bool,
    ) -> Option<RelayOutcome> {
        let obs = self
            .observer
            .as_mut()
            .and_then(TunnelRequestObserver::generation_mut)
            .expect("streaming relay requires a generation observer");
        obs.observe_upstream_event(message.received_at);
        let mut quality_progress = None;
        if let Some(generated_output) = message.facts.generated_output.as_ref() {
            if let (false, Some(queue)) = (*saw_output, self.queue_request.as_mut()) {
                queue.observe_output();
            }
            *saw_output = true;
            obs.observe_generated_output(message.received_at, generated_output.token_bearing);
            quality_progress = parser
                .observe_estimated_output_tokens(generated_output.estimated_token_units)
                .map(|delta| {
                    obs.observe_output_tokens(delta);
                    RequestOutputTokenProgress::Delta(delta)
                });
        }
        if let Some(exact_usage) = message.facts.exact_usage {
            if let Some(input_tokens) = exact_usage.input_tokens {
                obs.observe_input_tokens_total(input_tokens);
            }
            if let Some(output_tokens) = exact_usage.output_tokens {
                let tokens = parser.observe_exact_output_tokens(output_tokens);
                obs.observe_output_tokens_generated_so_far(tokens);
                quality_progress = Some(RequestOutputTokenProgress::Cumulative { tokens });
            }
        }
        if (message.facts.generated_output.is_some() || message.facts.exact_usage.is_some())
            && let Some(quality_check) = self.quality_check.as_mut()
        {
            quality_check
                .recorder
                .observe_json_chunk(message.parsed.as_ref(), quality_progress);
        }
        message.facts.terminal
    }

    fn finish(&mut self, app: &TunnelServerApp, outcome: RelayOutcome) {
        if let Some(observer) = self.observer.as_mut() {
            match outcome {
                RelayOutcome::Complete => observer.complete(),
                RelayOutcome::Failed => observer.fail(),
            }
        }
        if let Some(queue_request) = self.queue_request.as_mut() {
            queue_request.finish();
        }
        if outcome != RelayOutcome::Complete {
            return;
        }
        let Some((quality_check, metrics)) = self
            .quality_check
            .as_ref()
            .filter(|quality_check| quality_check.recorder.has_observed_stream_output())
            .zip(app.metrics.as_deref())
        else {
            return;
        };
        let (_, result) = quality_check
            .recorder
            .evaluate(&app.request_quality_monitor);
        let model_label = quality_check.model_label.to_str().unwrap_or_default();
        metrics.observe_quality_check_result(model_label, quality_check_result_label(&result));
        if let Some(reason) = result.threshold_match_reason {
            metrics.observe_quality_threshold_match(model_label, reason);
        }
    }
}

fn quality_check_result_label(
    result: &crate::request_quality_monitor::QualityCheckResult,
) -> &'static str {
    match (result.evaluated, result.threshold_match_reason) {
        (false, _) => "skipped",
        (true, Some(_)) => "matched",
        (true, None) => "clean",
    }
}

pub(super) struct TunnelRequestParts {
    pub(super) method: Method,
    pub(super) path_and_query: String,
    pub(super) headers: HeaderMap,
}

pub(super) trait TunnelRequestTransport: ResponseBodyEventSink {
    async fn read_request_body(
        &mut self,
        request_headers: &HeaderMap,
        max_request_body_bytes: usize,
    ) -> Result<Vec<u8>>;

    async fn send_response_head(&mut self, status: StatusCode, headers: HeaderMap) -> Result<()>;

    async fn finish_response(&mut self) -> Result<()>;
}

async fn send_complete_response(
    transport: &mut impl TunnelRequestTransport,
    status: StatusCode,
    headers: HeaderMap,
    body: String,
) -> Result<()> {
    transport.send_response_head(status, headers).await?;
    transport.send_body_event(body.into()).await?;
    transport.finish_response().await
}

async fn send_problem_response(
    transport: &mut impl TunnelRequestTransport,
    status: StatusCode,
    detail: impl Into<String>,
) -> Result<()> {
    send_complete_response(
        transport,
        status,
        problem_response_headers(),
        problem_details_body(status, detail),
    )
    .await
}

async fn relay_upstream_response(
    app: &TunnelServerApp,
    mut lifecycle: Option<&mut TunnelRequestLifecycle>,
    response: Response,
    transport: &mut impl TunnelRequestTransport,
) -> Result<RelayOutcome, ResponseRelayError> {
    let status = response.status();
    let response_head = build_response_headers(
        status,
        response.headers(),
        &app.retry,
        app.metrics.as_deref(),
        &app.inference_server_id,
    )
    .map_err(ResponseRelayError::Upstream)?;
    if let Some(lifecycle) = lifecycle.as_mut()
        && let Some(observer) = lifecycle.observer.as_mut()
    {
        observer.on_upstream_response_headers(status.as_u16());
    }
    let observed_streaming = lifecycle.as_ref().is_some_and(|lifecycle| {
        lifecycle
            .observer
            .as_ref()
            .is_some_and(TunnelRequestObserver::is_streaming)
    });
    let relay_as_sse = observed_streaming && is_sse_content_type(response.headers());
    if relay_as_sse {
        let upstream_messages = upstream_sse_message_stream(
            response.bytes_stream(),
            app.first_output_timeout,
            app.output_chunk_timeout,
            app.max_sse_buffer_bytes,
        );
        transport
            .send_response_head(status, response_head)
            .await
            .map_err(ResponseRelayError::Downstream)?;
        let outcome = lifecycle
            .as_mut()
            .expect("SSE relay should have an observed request lifecycle")
            .relay_sse(upstream_messages, transport)
            .await?;
        return Ok(if status.is_success() {
            outcome
        } else {
            RelayOutcome::Failed
        });
    }

    transport
        .send_response_head(status, response_head)
        .await
        .map_err(ResponseRelayError::Downstream)?;
    if status.is_success()
        && !observed_streaming
        && let Some(lifecycle) = lifecycle.as_mut()
    {
        if let Some(queue_request) = lifecycle.queue_request.as_mut() {
            queue_request.observe_output();
        }
        if let Some(observer) = lifecycle
            .observer
            .as_mut()
            .and_then(TunnelRequestObserver::generation_mut)
        {
            observer.observe_generated_output(Instant::now(), false);
        }
    }
    let mut body_stream = response.bytes_stream();
    while let Some(chunk) = body_stream
        .try_next()
        .await
        .context("failed to read upstream response body")
        .map_err(ResponseRelayError::Upstream)?
    {
        transport
            .send_body_event(chunk)
            .await
            .map_err(ResponseRelayError::Downstream)?;
    }
    Ok(if status.is_success() && !observed_streaming {
        RelayOutcome::Complete
    } else {
        RelayOutcome::Failed
    })
}

fn is_sse_content_type(headers: &HeaderMap) -> bool {
    headers
        .get(CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.split(';').next())
        .is_some_and(|media_type| media_type.trim().eq_ignore_ascii_case("text/event-stream"))
}

pub(super) async fn forward_tunnel_request(
    app: &TunnelServerApp,
    request: TunnelRequestParts,
    transport: &mut impl TunnelRequestTransport,
) -> Result<()> {
    let TunnelRequestParts {
        method,
        path_and_query,
        headers: request_headers,
    } = request;
    let health_request = is_health_request_path(&path_and_query);
    let observation_endpoint = request_observation_endpoint(&method, &path_and_query);
    let path_and_query = if health_request {
        health_probe_path_and_query(&app.upstream_health_paths, &path_and_query)
    } else {
        path_and_query
    };
    let mut lifecycle = if health_request {
        None
    } else {
        match validate_required_tunnel_headers(&request_headers) {
            Ok(required) => {
                let generation = match app
                    .runtime_state
                    .request_generation_admission(&required.model_id)
                {
                    RequestGenerationAdmission::Admitted(generation) => Some(generation),
                    RequestGenerationAdmission::Ungated => None,
                    RequestGenerationAdmission::Unavailable => {
                        return send_complete_response(
                            transport,
                            StatusCode::SERVICE_UNAVAILABLE,
                            model_generation_unavailable_headers(app),
                            problem_details_body(
                                StatusCode::SERVICE_UNAVAILABLE,
                                "model generation is not admitted for request routing",
                            ),
                        )
                        .await;
                    }
                };
                Some(TunnelRequestLifecycle::new(
                    app,
                    observation_endpoint,
                    &request_headers,
                    required,
                    generation,
                ))
            }
            Err(error) => {
                return send_problem_response(transport, StatusCode::BAD_REQUEST, error.message())
                    .await;
            }
        }
    };
    let body_bytes = transport
        .read_request_body(&request_headers, app.max_request_body_bytes)
        .await?;
    if let Some(lifecycle) = lifecycle.as_mut() {
        if let Some(observer) = lifecycle.observer.as_mut() {
            observer.observe_request_body(&body_bytes);
        }
        if let Err(error) = validate_request_body(observation_endpoint, &body_bytes) {
            lifecycle.fail();
            return send_problem_response(transport, StatusCode::BAD_REQUEST, error).await;
        }
        if let Some(decision) = lifecycle.admit_queue(app, &request_headers) {
            let QueueAdmissionDecision::Rejected {
                expected_ms,
                actual_ms,
                threshold_ms,
                ..
            } = &decision
            else {
                unreachable!("queue admission returned a non-rejection")
            };
            return send_complete_response(
                transport,
                StatusCode::TOO_MANY_REQUESTS,
                queue_mismatch_response_headers(app, &decision)?,
                serde_json::json!({
                    "type": "about:blank",
                    "title": "Too Many Requests",
                    "status": StatusCode::TOO_MANY_REQUESTS.as_u16(),
                    "detail": "local queue estimate exceeded Stargate routing estimate",
                    "reason": RETRY_REASON_QUEUE_ESTIMATE_MISMATCH,
                    "expected_queue_ms": expected_ms,
                    "actual_queue_ms": actual_ms,
                    "threshold_ms": threshold_ms,
                })
                .to_string(),
            )
            .await;
        }
    }

    let priority = lifecycle
        .as_ref()
        .and_then(|lifecycle| lifecycle.required.priority);
    let response = match send_upstream_request(
        app,
        lifecycle.as_mut(),
        UpstreamRequestParts {
            method,
            path_and_query: &path_and_query,
            headers: &request_headers,
            body: body_bytes,
            health_request,
            priority,
        },
    )
    .await
    {
        Ok(response) => response,
        Err(error) if matches!(&error, UpstreamRequestError::Send(source) if source.is_connect()) =>
        {
            if let Some(lifecycle) = lifecycle.as_mut() {
                lifecycle.fail();
            }
            let retryable = app.retry.local_connect_failures_retryable;
            let status = record_local_connect_failure(app, &error, retryable);
            return send_complete_response(
                transport,
                status,
                local_connect_failure_headers(retryable),
                problem_details_body(status, "local upstream connection failed"),
            )
            .await;
        }
        Err(error) => {
            if let Some(lifecycle) = lifecycle.as_mut() {
                lifecycle.fail();
            }
            return Err(error.into());
        }
    };

    let outcome = match relay_upstream_response(app, lifecycle.as_mut(), response, transport).await
    {
        Ok(outcome) => outcome,
        Err(ResponseRelayError::Upstream(error)) => {
            if let Some(lifecycle) = lifecycle.as_mut() {
                lifecycle.fail();
            }
            return Err(error);
        }
        Err(ResponseRelayError::Downstream(error)) => {
            if let Some(lifecycle) = lifecycle.as_mut() {
                lifecycle.cancel();
            }
            return Err(error);
        }
    };

    if let Err(error) = transport.finish_response().await {
        if let Some(lifecycle) = lifecycle.as_mut() {
            lifecycle.cancel();
        }
        return Err(error);
    }
    if let Some(lifecycle) = lifecycle.as_mut() {
        lifecycle.finish(app, outcome);
    }

    Ok(())
}

struct UpstreamRequestParts<'a> {
    method: Method,
    path_and_query: &'a str,
    headers: &'a HeaderMap,
    body: Vec<u8>,
    health_request: bool,
    priority: Option<u32>,
}

async fn send_upstream_request(
    app: &TunnelServerApp,
    lifecycle: Option<&mut TunnelRequestLifecycle>,
    request: UpstreamRequestParts<'_>,
) -> Result<Response, UpstreamRequestError> {
    let UpstreamRequestParts {
        method,
        path_and_query,
        headers: request_headers,
        body: body_bytes,
        health_request,
        priority,
    } = request;
    let span = if !health_request {
        let span = tracing::info_span!(
            "pylon_upstream_http_request",
            otel_parent = field::Empty,
            http.method = %method,
            http.path = %path_and_query,
            inference_server.id = %app.inference_server_id,
            upstream.status = field::Empty,
            upstream.error = field::Empty,
            priority = field::Empty,
            dynamo.request_priority = field::Empty,
        );
        let _ = span.set_parent(pylon_upstream_parent_context(request_headers));
        if let Some(otel_parent) = otel_parent_from_headers(request_headers) {
            span.record("otel_parent", otel_parent);
        }
        span
    } else {
        Span::none()
    };
    let mut upstream_headers = HeaderMap::with_capacity(request_headers.len());
    for (name, value) in request_headers {
        if should_forward_header(name, &app.retry) {
            upstream_headers.append(name, value.clone());
        }
    }
    if !health_request {
        if let Some(priority) = priority {
            span.record("priority", priority);
        }
        if app.upstream_backend == UpstreamBackend::Dynamo {
            let dynamo_priority = backend::dynamo::apply_priority_headers(
                priority,
                app.priority_ceiling,
                &mut upstream_headers,
            );
            span.record("dynamo.request_priority", dynamo_priority);
        }
        inject_trace_context(&mut upstream_headers, &span.context());
    }
    let send = async {
        let request_url = join_base_path(&app.upstream_http_base_url, path_and_query)
            .map_err(UpstreamRequestError::Build)?;
        let request = app
            .http_client
            .request(method, request_url)
            .headers(upstream_headers)
            .body(body_bytes)
            .build()
            .map_err(|error| UpstreamRequestError::Build(anyhow::Error::new(error)))?;
        if let Some(lifecycle) = lifecycle {
            lifecycle.on_backend_submission(Instant::now());
        }
        app.http_client
            .execute(request)
            .await
            .map_err(UpstreamRequestError::Send)
    };
    let result = send.instrument(span.clone()).await;
    match &result {
        Ok(response) => span.record("upstream.status", response.status().as_u16()),
        Err(error) => span.record("upstream.error", error.to_string()),
    };
    result
}

pub(super) fn pylon_upstream_parent_context(headers: &HeaderMap) -> opentelemetry::Context {
    parent_context_from_headers(headers)
}

pub(super) fn otel_parent_from_headers(headers: &HeaderMap) -> Option<&str> {
    traceparent_from_headers(headers)
}

#[derive(Debug, thiserror::Error)]
pub(super) enum UpstreamRequestError {
    #[error("failed to build upstream request: {0}")]
    Build(#[source] anyhow::Error),
    #[error("upstream http request failed: {0}")]
    Send(#[source] ReqwestError),
}

fn validate_request_body(
    observation_endpoint: Option<RequestObservationEndpoint>,
    body_bytes: &[u8],
) -> Result<(), &'static str> {
    let body =
        sonic_rs::get(body_bytes, &[] as &[&str]).map_err(|_| "request body must be valid JSON")?;

    let stream_error = match observation_endpoint {
        Some(RequestObservationEndpoint::ChatCompletions) => {
            "/v1/chat/completions requests must set stream=true"
        }
        Some(RequestObservationEndpoint::Responses) => {
            "/v1/responses requests must set stream=true"
        }
        _ => return Ok(()),
    };
    (body.get("stream").and_then(|value| value.as_bool()) == Some(true))
        .then_some(())
        .ok_or(stream_error)
}

pub(super) fn is_health_request_path(path_and_query: &str) -> bool {
    path_and_query.split('?').next() == Some("/health")
}

pub(super) fn health_probe_path_and_query(
    health_paths: &UpstreamHealthPaths,
    path_and_query: &str,
) -> String {
    let probe_path = health_paths.probe_path();
    match path_and_query.split_once('?') {
        Some((_, query)) => format!("{probe_path}?{query}"),
        None => probe_path.to_string(),
    }
}

fn request_observation_endpoint(
    method: &Method,
    path_and_query: &str,
) -> Option<RequestObservationEndpoint> {
    if method != Method::POST {
        return None;
    }
    match path_and_query.split('?').next() {
        Some("/v1/chat/completions") => Some(RequestObservationEndpoint::ChatCompletions),
        Some("/v1/responses") => Some(RequestObservationEndpoint::Responses),
        Some("/v1/embeddings") => Some(RequestObservationEndpoint::Embeddings),
        _ => None,
    }
}

pub(super) trait ResponseBodyEventSink {
    async fn send_body_event(&mut self, event: bytes::Bytes) -> Result<()>;
}

pub(super) fn request_body_buffer(
    request_headers: &HeaderMap,
    max_request_body_bytes: usize,
) -> Result<Vec<u8>> {
    Ok(Vec::with_capacity(
        request_body_capacity(request_headers, max_request_body_bytes)?.unwrap_or(0),
    ))
}

pub(super) fn request_body_capacity(
    request_headers: &HeaderMap,
    max_request_body_bytes: usize,
) -> Result<Option<usize>> {
    let Some(content_length) = request_headers
        .get(CONTENT_LENGTH)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.trim().parse::<usize>().ok())
    else {
        return Ok(None);
    };
    ensure!(
        content_length <= max_request_body_bytes,
        "request body too large"
    );
    // Preallocate for honest small Content-Length values, but cap speculative
    // allocation so a legal large body cannot reserve tens of MiB up front.
    let capacity = content_length.min(MAX_SPECULATIVE_REQUEST_BODY_PREALLOC_BYTES);
    Ok(Some(capacity))
}

pub(super) fn next_body_len(
    current: usize,
    chunk_len: usize,
    max_request_body_bytes: usize,
) -> Result<usize> {
    let next = current
        .checked_add(chunk_len)
        .context("request body length overflowed")?;
    ensure!(next <= max_request_body_bytes, "request body too large");
    Ok(next)
}

pub(super) fn extend_body_from_buf(body_bytes: &mut Vec<u8>, chunk: &mut impl Buf) {
    body_bytes.put(chunk);
}

pub(super) fn build_response_headers(
    status: StatusCode,
    response_headers: &HeaderMap,
    retry: &PylonRetryConfig,
    metrics: Option<&PylonMetrics>,
    inference_server_id: &str,
) -> Result<HeaderMap> {
    let mut header_frame = HeaderMap::new();
    let upstream_retry_header_present = response_headers
        .get(&retry.upstream_retry_header)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| value.eq_ignore_ascii_case("true"));
    let status_retryable = retry.retryable_upstream_status_codes.contains(&status);
    let retryable =
        status_retryable && (!retry.require_upstream_retry_header || upstream_retry_header_present);
    let reason = if retryable {
        RETRY_REASON_UPSTREAM_ADMISSION_REJECTED
    } else if status_retryable {
        "missing_upstream_retry_header"
    } else if !status.is_success() {
        "upstream_nonretryable_status"
    } else {
        ""
    };
    emit_at_info_or_debug!(
        upstream_response_logs_at_info(status, retryable),
        upstream.status = status.as_u16(),
        tunnel.retryable = retryable,
        tunnel.retry_reason = reason,
        upstream.retry_header_present = upstream_retry_header_present,
        "classified upstream response"
    );
    if !status.is_success() {
        record_failure_metric(metrics, inference_server_id, status, retryable, reason);
    }

    if retryable {
        insert_retry_metadata(
            &mut header_frame,
            true,
            RETRY_REASON_UPSTREAM_ADMISSION_REJECTED,
        );
        if retry.propagate_retry_after
            && let Some(retry_after_ms) = retry_after_millis(response_headers)
        {
            header_frame.insert(
                HEADER_STARGATE_RETRY_AFTER_MS,
                HeaderValue::from_str(&retry_after_ms.to_string())
                    .expect("a decimal u64 is always a valid HTTP header value"),
            );
        }
    }
    for (name, value) in response_headers {
        if should_forward_response_header(name, retry) {
            header_frame.append(name, value.clone());
        }
    }
    Ok(header_frame)
}

fn retry_after_millis(response_headers: &HeaderMap) -> Option<u64> {
    let value = response_headers.get(RETRY_AFTER)?.to_str().ok()?.trim();
    if let Ok(seconds) = value.parse::<u64>() {
        return seconds.checked_mul(1000);
    }
    let retry_at = httpdate::parse_http_date(value).ok()?;
    let duration = retry_at
        .duration_since(SystemTime::now())
        .unwrap_or_default();
    u64::try_from(duration.as_millis()).ok()
}

pub(super) fn queue_mismatch_response_headers(
    app: &TunnelServerApp,
    decision: &QueueAdmissionDecision,
) -> Result<HeaderMap> {
    let status = StatusCode::TOO_MANY_REQUESTS;
    record_failure_metric(
        app.metrics.as_deref(),
        &app.inference_server_id,
        status,
        true,
        RETRY_REASON_QUEUE_ESTIMATE_MISMATCH,
    );

    let mut headers = problem_response_headers();
    insert_retry_metadata(&mut headers, true, RETRY_REASON_QUEUE_ESTIMATE_MISMATCH);
    if let QueueAdmissionDecision::Rejected {
        retry_after_ms: Some(retry_after_ms),
        ..
    } = decision
    {
        headers.insert(
            HEADER_STARGATE_RETRY_AFTER_MS,
            HeaderValue::from_str(&retry_after_ms.to_string())
                .expect("a decimal u64 is always a valid HTTP header value"),
        );
    }
    Ok(headers)
}

pub(super) fn problem_response_headers() -> HeaderMap {
    HeaderMap::from_iter([(
        CONTENT_TYPE,
        HeaderValue::from_static("application/problem+json"),
    )])
}

pub(super) fn local_connect_failure_headers(retryable: bool) -> HeaderMap {
    let mut headers = problem_response_headers();
    insert_retry_metadata(&mut headers, retryable, RETRY_REASON_LOCAL_CONNECT_FAILURE);
    headers
}

pub(super) fn model_generation_unavailable_headers(app: &TunnelServerApp) -> HeaderMap {
    let status = StatusCode::SERVICE_UNAVAILABLE;
    record_failure_metric(
        app.metrics.as_deref(),
        &app.inference_server_id,
        status,
        true,
        RETRY_REASON_MODEL_GENERATION_UNAVAILABLE,
    );
    let mut headers = problem_response_headers();
    insert_retry_metadata(
        &mut headers,
        true,
        RETRY_REASON_MODEL_GENERATION_UNAVAILABLE,
    );
    headers
}

pub(super) fn record_local_connect_failure(
    app: &TunnelServerApp,
    error: &UpstreamRequestError,
    retryable: bool,
) -> StatusCode {
    tracing::warn!(
        inference_server_id = %app.inference_server_id,
        error = %error,
        retryable,
        "local upstream connection failed"
    );

    let status = StatusCode::SERVICE_UNAVAILABLE;
    record_failure_metric(
        app.metrics.as_deref(),
        &app.inference_server_id,
        status,
        retryable,
        RETRY_REASON_LOCAL_CONNECT_FAILURE,
    );

    status
}

fn insert_retry_metadata(headers: &mut HeaderMap, retryable: bool, reason: &'static str) {
    headers.insert(
        HEADER_STARGATE_RETRYABLE,
        HeaderValue::from_static(if retryable { "true" } else { "false" }),
    );
    headers.insert(
        HEADER_STARGATE_RETRY_REASON,
        HeaderValue::from_static(reason),
    );
}

fn record_failure_metric(
    metrics: Option<&PylonMetrics>,
    inference_server_id: &str,
    status: StatusCode,
    retryable: bool,
    reason: &str,
) {
    let Some(metrics) = metrics else { return };
    let counter = if retryable {
        metrics.retryable_responses_total(inference_server_id, reason, &status.as_u16().to_string())
    } else {
        metrics.nonretryable_failures_total(inference_server_id, reason)
    };
    counter.inc();
}

pub(super) fn problem_details_body(status: StatusCode, detail: impl Into<String>) -> String {
    serde_json::json!({
        "type": "about:blank",
        "title": status.canonical_reason().unwrap_or("Error"),
        "status": status.as_u16(),
        "detail": detail.into(),
    })
    .to_string()
}

pub(super) fn join_base_path(base: &str, path_and_query: &str) -> Result<url::Url> {
    let base = url::Url::parse(base).context("invalid upstream_http_base_url")?;
    if path_and_query.starts_with('/') {
        base.join(path_and_query)
    } else {
        base.join(&format!("/{path_and_query}"))
    }
    .context("join upstream path failed")
}

pub(super) fn should_forward_header(name: &HeaderName, retry: &PylonRetryConfig) -> bool {
    !is_tunnel_control_header(name, retry)
        && !backend::dynamo::is_stripped_engine_header(name)
        && !matches!(
            name.as_str(),
            "host" | "x-method" | "x-path" | HEADER_STARGATE_EXPECTED_QUEUE_MS
        )
}

pub(super) fn should_forward_response_header(name: &HeaderName, retry: &PylonRetryConfig) -> bool {
    !is_tunnel_control_header(name, retry) && name != CONTENT_LENGTH
}

fn is_tunnel_control_header(name: &HeaderName, retry: &PylonRetryConfig) -> bool {
    // HeaderName is normalized, so this policy stays allocation-free on both hot paths.
    name == retry.upstream_retry_header
        || is_hop_by_hop_header(name)
        || matches!(
            name.as_str(),
            HEADER_STARGATE_UPSTREAM_RETRYABLE
                | HEADER_STARGATE_RETRYABLE
                | HEADER_STARGATE_RETRY_REASON
                | HEADER_STARGATE_RETRY_AFTER_MS
        )
}

#[cfg(test)]
mod tests {
    use axum::Router;
    use axum::body::Body;
    use axum::response::Response as AxumResponse;
    use axum::routing::post;
    use stargate_proto::pb::InferenceServerStatus;
    use stargate_protocol::tunnel_contract::{HEADER_INPUT_TOKENS, HEADER_REQUEST_ID};

    use crate::test_support::TestHttpServer;

    use super::*;

    #[test]
    fn classifies_sse_delivery_backpressure_as_downstream_failure() {
        assert!(matches!(
            relay_sse_error(UpstreamSseReadError::DeliveryBackpressure(
                SseReadTimeoutPhase::FirstOutput
            )),
            ResponseRelayError::Downstream(_)
        ));
        assert!(matches!(
            relay_sse_error(UpstreamSseReadError::Timeout(
                SseReadTimeoutPhase::FirstOutput
            )),
            ResponseRelayError::Upstream(_)
        ));
    }

    #[derive(Clone)]
    struct ResponseHeadGate {
        entered: Arc<tokio::sync::Notify>,
        release: Arc<tokio::sync::Notify>,
    }

    #[derive(Default)]
    struct TestTransport {
        request_body: Vec<u8>,
        response_heads: Vec<StatusCode>,
        response_events: Vec<bytes::Bytes>,
        response_head_gate: Option<ResponseHeadGate>,
        fail_finish: bool,
    }

    impl ResponseBodyEventSink for TestTransport {
        async fn send_body_event(&mut self, event: bytes::Bytes) -> Result<()> {
            self.response_events.push(event);
            Ok(())
        }
    }

    impl TunnelRequestTransport for TestTransport {
        async fn read_request_body(
            &mut self,
            _request_headers: &HeaderMap,
            _max_request_body_bytes: usize,
        ) -> Result<Vec<u8>> {
            Ok(self.request_body.clone())
        }

        async fn send_response_head(
            &mut self,
            status: StatusCode,
            _headers: HeaderMap,
        ) -> Result<()> {
            self.response_heads.push(status);
            if let Some(gate) = &self.response_head_gate {
                gate.entered.notify_one();
                gate.release.notified().await;
            }
            Ok(())
        }

        async fn finish_response(&mut self) -> Result<()> {
            if self.fail_finish {
                return Err(anyhow!("client stopped before response completion"));
            }
            Ok(())
        }
    }

    fn observed_app(
        upstream_http_base_url: impl Into<String>,
    ) -> (
        TunnelServerApp,
        flume::Receiver<crate::RequestObservationEvent>,
    ) {
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Unknown,
            &["model-a".to_string()],
            16,
            None,
        );
        let app = TunnelServerApp::new(
            "test-pylon".to_string(),
            upstream_http_base_url.into(),
            TunnelForwardingConfig {
                runtime_state,
                ..TunnelForwardingConfig::default()
            },
        );
        (app, observations)
    }

    fn observed_request(path: &str) -> TunnelRequestParts {
        let mut headers = HeaderMap::new();
        headers.insert(HEADER_REQUEST_ID, "req-observed".parse().unwrap());
        headers.insert(HEADER_MODEL, "model-a".parse().unwrap());
        headers.insert(HEADER_INPUT_TOKENS, "12".parse().unwrap());
        headers.insert(CONTENT_TYPE, "application/json".parse().unwrap());
        TunnelRequestParts {
            method: Method::POST,
            path_and_query: path.to_string(),
            headers,
        }
    }

    fn observed_states(
        observations: &flume::Receiver<crate::RequestObservationEvent>,
    ) -> Vec<crate::RequestObservationState> {
        observations
            .try_iter()
            .map(|event| event.into_observation().state)
            .collect()
    }

    async fn run_observed_sse(
        path: &'static str,
        request_body: &'static [u8],
        status: StatusCode,
        response_body: impl Into<String>,
    ) -> (
        anyhow::Result<()>,
        TestTransport,
        Vec<crate::RequestObservationEvent>,
    ) {
        run_observed_response(
            path,
            request_body,
            status,
            Some("text/event-stream"),
            response_body,
        )
        .await
    }

    async fn run_observed_response(
        path: &'static str,
        request_body: &'static [u8],
        status: StatusCode,
        response_content_type: Option<&'static str>,
        response_body: impl Into<String>,
    ) -> (
        anyhow::Result<()>,
        TestTransport,
        Vec<crate::RequestObservationEvent>,
    ) {
        let response_body = response_body.into();
        let upstream = TestHttpServer::spawn(Router::new().route(
            path,
            post(move || {
                let response_body = response_body.clone();
                async move {
                    let mut response = AxumResponse::builder().status(status);
                    if let Some(content_type) = response_content_type {
                        response = response.header(CONTENT_TYPE, content_type);
                    }
                    response.body(Body::from(response_body)).unwrap()
                }
            }),
        ))
        .await;
        let (app, observations) = observed_app(upstream.as_str());
        let mut transport = TestTransport {
            request_body: request_body.to_vec(),
            ..TestTransport::default()
        };
        let result = forward_tunnel_request(&app, observed_request(path), &mut transport).await;
        let observations = observations.try_iter().collect();
        (result, transport, observations)
    }

    #[test]
    fn sse_content_type_requires_an_exact_case_insensitive_media_type() {
        for (value, expected) in [
            ("text/event-stream", true),
            ("Text/Event-Stream; charset=utf-8", true),
            (" text/event-stream ; charset=utf-8", true),
            ("text/event-streamfoo", false),
            ("application/json", false),
        ] {
            let mut headers = HeaderMap::new();
            headers.insert(CONTENT_TYPE, value.parse().unwrap());
            assert_eq!(is_sse_content_type(&headers), expected, "{value}");
        }
        assert!(!is_sse_content_type(&HeaderMap::new()));
    }

    #[test]
    fn operational_log_levels_reserve_info_for_rejections_and_failures() {
        for decision in [
            QueueAdmissionDecision::Accepted {
                expected_ms: 1,
                actual_ms: 1,
                threshold_ms: 1,
            },
            QueueAdmissionDecision::MissingEstimate,
            QueueAdmissionDecision::UnknownLocalEstimate { expected_ms: 1 },
            QueueAdmissionDecision::Disabled,
        ] {
            assert!(!queue_decision_logs_at_info(&decision));
        }
        assert!(queue_decision_logs_at_info(
            &QueueAdmissionDecision::Rejected {
                expected_ms: 1,
                actual_ms: 2,
                threshold_ms: 1,
                retry_after_ms: None,
            }
        ));

        assert!(!upstream_response_logs_at_info(StatusCode::OK, false));
        assert!(upstream_response_logs_at_info(
            StatusCode::TOO_MANY_REQUESTS,
            true
        ));
        assert!(upstream_response_logs_at_info(
            StatusCode::INTERNAL_SERVER_ERROR,
            false
        ));
    }

    #[tokio::test]
    async fn request_build_failure_is_not_backend_submitted() {
        let (app, observations) = observed_app("not a valid upstream URL");
        let mut transport = TestTransport {
            request_body: br#"{"messages":[],"stream":true}"#.to_vec(),
            ..TestTransport::default()
        };

        let error = forward_tunnel_request(
            &app,
            observed_request("/v1/chat/completions"),
            &mut transport,
        )
        .await
        .expect_err("invalid upstream URL should fail request construction");

        assert!(
            error
                .to_string()
                .contains("failed to build upstream request")
        );
        assert_eq!(
            observed_states(&observations),
            [
                crate::RequestObservationState::UpstreamConnecting,
                crate::RequestObservationState::Failed,
            ]
        );
    }

    #[tokio::test]
    async fn execute_failure_terminates_an_already_submitted_request() {
        let (app, observations) = observed_app("http://127.0.0.1:0");
        let mut transport = TestTransport {
            request_body: br#"{"messages":[],"stream":true}"#.to_vec(),
            ..TestTransport::default()
        };

        forward_tunnel_request(
            &app,
            observed_request("/v1/chat/completions"),
            &mut transport,
        )
        .await
        .expect("local connect failure should return the problem response");

        assert_eq!(transport.response_heads, [StatusCode::SERVICE_UNAVAILABLE]);
        assert_eq!(
            observed_states(&observations),
            [
                crate::RequestObservationState::UpstreamConnecting,
                crate::RequestObservationState::InputProcessing,
                crate::RequestObservationState::Failed,
            ]
        );
    }

    #[tokio::test(start_paused = true)]
    async fn first_output_timeout_runs_while_downstream_response_head_is_stalled() {
        let upstream = TestHttpServer::spawn(Router::new().route(
            "/v1/chat/completions",
            post(|| async {
                let body = Body::from_stream(futures::stream::pending::<
                    std::result::Result<bytes::Bytes, std::convert::Infallible>,
                >());
                AxumResponse::builder()
                    .header(CONTENT_TYPE, "text/event-stream")
                    .body(body)
                    .unwrap()
            }),
        ))
        .await;
        let (mut app, observations) = observed_app(upstream.as_str());
        app.first_output_timeout = Duration::from_secs(1);
        let entered = Arc::new(tokio::sync::Notify::new());
        let release = Arc::new(tokio::sync::Notify::new());
        let gate = ResponseHeadGate {
            entered: Arc::clone(&entered),
            release: Arc::clone(&release),
        };
        let mut transport = TestTransport {
            request_body: br#"{"messages":[],"stream":true}"#.to_vec(),
            response_head_gate: Some(gate),
            ..TestTransport::default()
        };

        let (completed_tx, completed_rx) = tokio::sync::oneshot::channel();
        let forward = tokio::spawn(async move {
            let result = forward_tunnel_request(
                &app,
                observed_request("/v1/chat/completions"),
                &mut transport,
            )
            .await;
            let _ = completed_tx.send(());
            (result, transport)
        });
        entered.notified().await;
        tokio::time::advance(Duration::from_millis(1001)).await;
        release.notify_one();

        tokio::time::timeout(Duration::from_millis(100), completed_rx)
            .await
            .expect("forwarding should complete promptly after releasing the response header")
            .expect("forwarding task should report completion");
        let (result, transport) = forward.await.expect("forwarding task should not panic");

        assert!(
            result
                .expect_err("missing first output should fail the upstream relay")
                .to_string()
                .contains("timed out waiting for first output event")
        );
        assert_eq!(transport.response_heads, [StatusCode::OK]);
        assert_eq!(
            observations
                .try_iter()
                .last()
                .expect("terminal observation should be emitted")
                .observation()
                .state,
            crate::RequestObservationState::Failed
        );
    }

    #[tokio::test]
    async fn downstream_finish_failure_overrides_forwarded_success_terminal_once() {
        let upstream = TestHttpServer::spawn(Router::new().route(
            "/v1/chat/completions",
            post(|| async {
                AxumResponse::builder()
                    .header(CONTENT_TYPE, "text/event-stream")
                    .body(Body::from("data: [DONE]\n\n"))
                    .unwrap()
            }),
        ))
        .await;
        let (app, observations) = observed_app(upstream.as_str());
        let mut transport = TestTransport {
            request_body: br#"{"messages":[],"stream":true}"#.to_vec(),
            fail_finish: true,
            ..TestTransport::default()
        };

        let error = forward_tunnel_request(
            &app,
            observed_request("/v1/chat/completions"),
            &mut transport,
        )
        .await
        .expect_err("downstream finish should fail");

        assert!(
            error
                .to_string()
                .contains("client stopped before response completion")
        );
        assert_eq!(
            transport.response_events,
            [bytes::Bytes::from_static(b"data: [DONE]\n\n")]
        );
        let states = observed_states(&observations);
        assert_eq!(
            states,
            [
                crate::RequestObservationState::UpstreamConnecting,
                crate::RequestObservationState::InputProcessing,
                crate::RequestObservationState::InputProcessing,
                crate::RequestObservationState::Cancelled,
            ]
        );
        assert_eq!(
            states
                .iter()
                .filter(|state| matches!(
                    state,
                    crate::RequestObservationState::Complete
                        | crate::RequestObservationState::Failed
                        | crate::RequestObservationState::Cancelled
                ))
                .count(),
            1
        );
    }

    #[tokio::test]
    async fn explicit_success_terminal_completes_zero_output_after_applying_usage() {
        let raw = "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n";
        let (result, transport, observations) = run_observed_sse(
            "/v1/responses",
            br#"{"input":"hello","stream":true}"#,
            StatusCode::OK,
            raw,
        )
        .await;
        result.expect("explicit terminal should complete the SSE response");

        assert_eq!(
            transport.response_events,
            [bytes::Bytes::from_static(raw.as_bytes())]
        );
        let terminal = observations
            .last()
            .expect("terminal observation should be emitted")
            .observation();
        assert_eq!(terminal.state, crate::RequestObservationState::Complete);
        assert_eq!(terminal.output_messages, 0);
        assert_eq!(terminal.input_tokens, 5);
        assert_eq!(terminal.output_tokens, 2);
        assert!(terminal.output_tokens_explicit);
        assert_eq!(terminal.time_to_first_output, None);
        assert_eq!(terminal.time_to_first_token, None);
    }

    #[tokio::test]
    async fn whitespace_text_records_first_token_without_inventing_an_estimate() {
        let raw = "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\" \"}}]}\n\ndata: [DONE]\n\n";
        let (result, _, observations) = run_observed_sse(
            "/v1/chat/completions",
            br#"{"messages":[],"stream":true}"#,
            StatusCode::OK,
            raw,
        )
        .await;
        result.expect("explicit terminal should complete the whitespace SSE response");

        let terminal = observations
            .last()
            .expect("terminal observation should be emitted")
            .observation();
        assert_eq!(terminal.state, crate::RequestObservationState::Complete);
        assert!(terminal.time_to_first_output.is_some());
        assert!(terminal.time_to_first_token.is_some());
        assert_eq!(terminal.output_tokens, 0);
    }

    #[tokio::test]
    async fn exact_usage_corrects_prior_stream_estimates() {
        let raw = concat!(
            "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"one two three four five\"}}]}\n\n",
            "data: {\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"completion_tokens\":3}}\n\n",
            "data: [DONE]\n\n",
        );
        let (result, _, observations) = run_observed_sse(
            "/v1/chat/completions",
            br#"{"messages":[],"stream":true}"#,
            StatusCode::OK,
            raw,
        )
        .await;
        result.expect("exact usage should complete the SSE response");

        let terminal = observations.last().unwrap().observation();
        assert_eq!(terminal.state, crate::RequestObservationState::Complete);
        assert_eq!(terminal.output_tokens, 3);
        assert!(terminal.output_tokens_explicit);
    }

    #[tokio::test]
    async fn failed_responses_terminals_override_generated_output() {
        for (event_type, request_id_suffix) in [
            ("response.failed", "failed"),
            ("response.incomplete", "incomplete"),
            ("error", "error"),
        ] {
            let body = format!(
                "data: {{\"type\":\"response.output_text.delta\",\"delta\":\"x\"}}\n\ndata: {{\"type\":\"{event_type}\"}}\n\n"
            );
            let (result, _, observations) = run_observed_sse(
                "/v1/responses",
                br#"{"input":"hello","stream":true}"#,
                StatusCode::OK,
                body,
            )
            .await;
            result.expect("explicit failure terminal should complete the SSE response");
            let terminal = observations.last().unwrap().observation();
            assert_eq!(
                terminal.state,
                crate::RequestObservationState::Failed,
                "unexpected outcome for {request_id_suffix}"
            );
            assert_eq!(terminal.output_messages, 1);
        }
    }

    #[tokio::test]
    async fn non_success_http_status_overrides_done_terminal() {
        let (result, _, observations) = run_observed_sse(
            "/v1/chat/completions",
            br#"{"messages":[],"stream":true}"#,
            StatusCode::INTERNAL_SERVER_ERROR,
            "data: [DONE]\n\n",
        )
        .await;
        result.expect("explicit terminal should complete the non-success response");

        assert_eq!(
            observations.last().unwrap().observation().state,
            crate::RequestObservationState::Failed
        );
    }

    #[tokio::test]
    async fn clean_eof_completes_only_after_generated_output() {
        let (output_result, _, output_observations) = run_observed_sse(
            "/v1/chat/completions",
            br#"{"messages":[],"stream":true}"#,
            StatusCode::OK,
            "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n",
        )
        .await;
        output_result.expect("clean EOF after generated output should complete");
        assert_eq!(
            output_observations.last().unwrap().observation().state,
            crate::RequestObservationState::Complete
        );

        let (metadata_result, _, metadata_observations) = run_observed_sse(
            "/v1/chat/completions",
            br#"{"messages":[],"stream":true}"#,
            StatusCode::OK,
            "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
        )
        .await;
        assert!(
            metadata_result
                .expect_err("truncated metadata stream should fail")
                .to_string()
                .contains("ended before generated output or a terminal event")
        );
        assert_eq!(
            metadata_observations.last().unwrap().observation().state,
            crate::RequestObservationState::Failed
        );
    }

    #[tokio::test]
    async fn observed_streaming_requires_an_sse_response_media_type() {
        for content_type in [None, Some("application/json"), Some("text/event-streamfoo")] {
            let (result, transport, observations) = run_observed_response(
                "/v1/chat/completions",
                br#"{"messages":[],"stream":true}"#,
                StatusCode::OK,
                content_type,
                r#"{"message":"wrong response framing"}"#,
            )
            .await;
            result.expect("the wire response should still be forwarded");

            assert_eq!(
                transport.response_events,
                [bytes::Bytes::from_static(
                    br#"{"message":"wrong response framing"}"#
                )]
            );
            let terminal = observations.last().unwrap().observation();
            assert_eq!(terminal.state, crate::RequestObservationState::Failed);
            assert_eq!(terminal.output_messages, 0);
            assert_eq!(terminal.time_to_first_output, None);
            assert!(!observations.iter().any(|event| {
                event.observation().state == crate::RequestObservationState::OutputGeneration
            }));
        }
    }

    #[tokio::test]
    async fn observed_streaming_accepts_case_insensitive_sse_media_type() {
        let (result, _, observations) = run_observed_response(
            "/v1/chat/completions",
            br#"{"messages":[],"stream":true}"#,
            StatusCode::OK,
            Some("Text/Event-Stream; charset=utf-8"),
            "data: [DONE]\n\n",
        )
        .await;
        result.expect("valid SSE media type should use the streaming relay");
        assert_eq!(
            observations.last().unwrap().observation().state,
            crate::RequestObservationState::Complete
        );
    }
}

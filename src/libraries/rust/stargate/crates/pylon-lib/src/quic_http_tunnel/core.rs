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
use std::time::{Duration, SystemTime};

use anyhow::{Context, Result, anyhow, bail, ensure};
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

use crate::output_token_parser::{OutputTokenParser, OutputTokenProgress};
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
    ParsedSseMessage, SseMessage, SseReadTimeoutPhase, UpstreamSseReadError,
    upstream_sse_message_stream,
};
use crate::stats::PylonMetrics;

pub(super) const DEFAULT_MAX_BODY_BYTES: usize = 64 * 1024 * 1024;
// This bounds only one upstream SSE event waiting for its blank-line delimiter,
// not the request body or completed response events. One MiB accommodates
// unusually large structured chunks while a missing delimiter cannot make the
// pylon retain unbounded upstream bytes.
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

fn relay_sse_error(error: UpstreamSseReadError) -> anyhow::Error {
    use UpstreamSseReadError::*;
    match error {
        Timeout(phase) => anyhow!(
            "timed out waiting for {} output event from upstream",
            sse_timeout_phase_name(phase)
        ),
        BufferLimitExceeded {
            max_buffer_bytes,
            buffered_bytes,
        } => anyhow!(
            "upstream SSE buffer exceeded {max_buffer_bytes} bytes while waiting for an event boundary (buffered {buffered_bytes} bytes)"
        ),
        Upstream(error) => error.context("failed to read upstream response message"),
    }
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

    async fn relay_sse(
        &mut self,
        app: &TunnelServerApp,
        response: Response,
        transport: &mut impl TunnelRequestTransport,
    ) -> Result<()> {
        let mut upstream_messages = upstream_sse_message_stream(
            response.bytes_stream(),
            app.first_output_timeout,
            app.output_chunk_timeout,
            app.max_sse_buffer_bytes,
        );
        let mut output_token_parser = OutputTokenParser::new();
        let mut saw_output = false;
        loop {
            let parsed_message = upstream_messages.try_next().await.map_err(|error| {
                self.fail();
                relay_sse_error(error)
            })?;
            let Some(raw_event) = self.observe_sse_message(
                &mut output_token_parser,
                parsed_message,
                &mut saw_output,
            )?
            else {
                return Ok(());
            };
            transport.send_body_event(raw_event).await?;
        }
    }

    fn observe_sse_message(
        &mut self,
        parser: &mut OutputTokenParser,
        message: Option<ParsedSseMessage>,
        saw_output: &mut bool,
    ) -> Result<Option<bytes::Bytes>> {
        let obs = self
            .observer
            .as_mut()
            .and_then(TunnelRequestObserver::generation_mut)
            .ok_or_else(|| anyhow!("observer missing for observed streaming request"))?;
        let Some(message) = message else {
            if !*saw_output {
                obs.fail();
                bail!("upstream stream ended before first output event");
            }
            return Ok(None);
        };
        if let SseMessage::ChatCompletionChunk { parsed, .. } = &message.message {
            if let (false, Some(queue)) = (*saw_output, self.queue_request.as_mut()) {
                queue.observe_output();
            }
            *saw_output = true;
            obs.observe_output_message();
            let quality_progress =
                parser
                    .observe_json(parsed.as_ref())
                    .map(|progress| match progress {
                        OutputTokenProgress::ExplicitCumulative { tokens, delta } => {
                            obs.observe_output_tokens_generated_so_far(tokens);
                            RequestOutputTokenProgress::Cumulative { tokens, delta }
                        }
                        OutputTokenProgress::EstimatedDelta { delta } => {
                            obs.observe_output_tokens(delta);
                            RequestOutputTokenProgress::Delta(delta)
                        }
                    });
            if let Some(quality_check) = self.quality_check.as_mut() {
                quality_check
                    .recorder
                    .observe_json_chunk(parsed.as_ref(), quality_progress);
            }
        }
        Ok(Some(message.raw_event))
    }

    fn finish(&mut self, app: &TunnelServerApp) {
        if let Some(observer) = self.observer.as_mut() {
            observer.finish();
        }
        if let Some(queue_request) = self.queue_request.as_mut() {
            queue_request.finish();
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
) -> Result<()> {
    let status = response.status();
    let response_head = build_response_headers(
        status,
        response.headers(),
        &app.retry,
        app.metrics.as_deref(),
        &app.inference_server_id,
    )?;
    transport.send_response_head(status, response_head).await?;
    if let Some(lifecycle) = lifecycle.as_mut() {
        if let Some(queue_request) = lifecycle.queue_request.as_mut() {
            queue_request.on_upstream_response_headers();
        }
        if let Some(observer) = lifecycle.observer.as_mut() {
            observer.on_upstream_response_headers(response.headers(), status.as_u16());
        }
    }
    if let Some(lifecycle) = lifecycle.as_mut()
        && lifecycle
            .observer
            .as_ref()
            .is_some_and(TunnelRequestObserver::is_streaming)
        && response
            .headers()
            .get(CONTENT_TYPE)
            .and_then(|value| value.to_str().ok())
            .is_some_and(|value| value.starts_with("text/event-stream"))
    {
        lifecycle.relay_sse(app, response, transport).await
    } else {
        if status.is_success()
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
                observer.observe_output_message();
            }
        }
        let mut body_stream = response.bytes_stream();
        while let Some(chunk) = body_stream
            .try_next()
            .await
            .context("failed to read upstream response body")?
        {
            transport.send_body_event(chunk).await?;
        }
        Ok(())
    }
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

    let response = match send_upstream_request(
        app,
        method,
        &path_and_query,
        &request_headers,
        body_bytes,
        !health_request,
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
        Err(error) => return Err(error.into()),
    };

    relay_upstream_response(app, lifecycle.as_mut(), response, transport).await?;

    transport.finish_response().await?;
    if let Some(lifecycle) = lifecycle.as_mut() {
        lifecycle.finish(app);
    }

    Ok(())
}

async fn send_upstream_request(
    app: &TunnelServerApp,
    method: Method,
    path_and_query: &str,
    request_headers: &HeaderMap,
    body_bytes: Vec<u8>,
    traced: bool,
) -> Result<Response, UpstreamRequestError> {
    let span = if traced {
        let span = tracing::info_span!(
            "pylon_upstream_http_request",
            otel_parent = field::Empty,
            http.method = %method,
            http.path = %path_and_query,
            inference_server.id = %app.inference_server_id,
            upstream.status = field::Empty,
            upstream.error = field::Empty,
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
    if traced {
        inject_trace_context(&mut upstream_headers, &span.context());
    }
    let send = async {
        let request_url = join_base_path(&app.upstream_http_base_url, path_and_query)
            .map_err(UpstreamRequestError::Build)?;
        app.http_client
            .request(method, request_url)
            .headers(upstream_headers)
            .body(body_bytes)
            .send()
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
    use super::*;

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
}

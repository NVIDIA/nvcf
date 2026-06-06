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

use anyhow::{Context, Result};
use bytes::Buf;
use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use stargate_protocol::tunnel_contract::{HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE};
use tokio_util::sync::CancellationToken;
use tokio_util::task::TaskTracker;

use crate::queue_admission::QueueAdmissionDecision;
use crate::stats::PylonMetrics;

use super::core::{
    PylonRetryConfig, RETRY_REASON_LOCAL_CONNECT_FAILURE, ResponseBodyEventSink,
    TunnelRequestParts, TunnelRequestTransport, TunnelServerApp, UpstreamRequestError,
    build_response_headers, extend_body_from_buf, forward_tunnel_request, next_body_len,
    problem_details_body, queue_mismatch_body, queue_mismatch_response_headers,
    record_local_connect_failure, request_body_buffer,
};

pub(super) async fn handle_h3_connection(
    incoming: quinn::Incoming,
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
    app: TunnelServerApp,
) -> Result<()> {
    let connection = incoming.await.context("accept quic connection failed")?;
    handle_h3_established_connection(connection, shutdown, task_tracker, app).await
}

pub(super) async fn handle_h3_established_connection(
    connection: quinn::Connection,
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
    app: TunnelServerApp,
) -> Result<()> {
    let mut h3_connection = h3::server::builder()
        .build(h3_quinn::Connection::new(connection))
        .await
        .map_err(h3_error)
        .context("create h3 server connection")?;
    loop {
        tokio::select! {
            _ = shutdown.cancelled() => break,
            accepted = h3_connection.accept() => {
                match accepted {
                    Ok(Some(resolver)) => {
                        let app = app.clone();
                        task_tracker.spawn(async move {
                            if let Err(error) = handle_h3_request(resolver, &app).await {
                                tracing::warn!(error = %error, "h3 tunnel request failed");
                            }
                        });
                    }
                    Ok(None) => break,
                    Err(error) if error.is_h3_no_error() => break,
                    Err(error) => return Err(h3_error(error).context("h3 accept failed")),
                }
            }
        }
    }
    Ok(())
}

struct H3TunnelTransport<'a, S>
where
    S: h3::quic::RecvStream + h3::quic::SendStream<bytes::Bytes> + Send,
{
    stream: &'a mut h3::server::RequestStream<S, bytes::Bytes>,
}

impl<S> ResponseBodyEventSink for H3TunnelTransport<'_, S>
where
    S: h3::quic::RecvStream + h3::quic::SendStream<bytes::Bytes> + Send,
{
    async fn send_body_event(&mut self, event: bytes::Bytes) -> Result<()> {
        self.stream
            .send_data(event)
            .await
            .map_err(h3_error)
            .context("failed to send h3 response body event")
    }
}

impl<S> TunnelRequestTransport for H3TunnelTransport<'_, S>
where
    S: h3::quic::RecvStream + h3::quic::SendStream<bytes::Bytes> + Send,
{
    async fn read_request_body(
        &mut self,
        request_headers: &HeaderMap,
        max_request_body_bytes: usize,
    ) -> Result<Vec<u8>> {
        read_h3_request_body(self.stream, request_headers, max_request_body_bytes).await
    }

    async fn send_success(
        &mut self,
        status: reqwest::StatusCode,
        response_headers: &HeaderMap,
        retry: &PylonRetryConfig,
        metrics: Option<&PylonMetrics>,
        inference_server_id: &str,
    ) -> Result<()> {
        send_h3_success_headers(
            self.stream,
            status,
            response_headers,
            retry,
            metrics,
            inference_server_id,
        )
        .await
    }

    async fn send_error(&mut self, status: reqwest::StatusCode, message: String) -> Result<()> {
        send_h3_error_response(self.stream, status, message).await
    }

    async fn send_queue_mismatch(
        &mut self,
        app: &TunnelServerApp,
        decision: &QueueAdmissionDecision,
    ) -> Result<()> {
        send_h3_queue_mismatch_response(self.stream, app, decision).await
    }

    async fn send_local_connect_failure(
        &mut self,
        app: &TunnelServerApp,
        error: &UpstreamRequestError,
        retryable: bool,
    ) -> Result<()> {
        send_h3_local_connect_failure_response(self.stream, app, error, retryable).await
    }

    async fn finish_response(&mut self) -> Result<()> {
        self.stream
            .finish()
            .await
            .map_err(h3_error)
            .context("failed to finish h3 response stream")
    }
}

async fn handle_h3_request(
    resolver: h3::server::RequestResolver<h3_quinn::Connection, bytes::Bytes>,
    app: &TunnelServerApp,
) -> Result<()> {
    let (request, mut stream) = resolver
        .resolve_request()
        .await
        .map_err(h3_error)
        .context("resolve h3 request")?;
    let method: reqwest::Method = request
        .method()
        .as_str()
        .parse()
        .context("invalid h3 method")?;
    let path_and_query = request
        .uri()
        .path_and_query()
        .map(|value| value.as_str())
        .unwrap_or("/")
        .to_string();
    let mut transport = H3TunnelTransport {
        stream: &mut stream,
    };
    forward_tunnel_request(
        app,
        TunnelRequestParts {
            method,
            path_and_query,
            headers: request.headers().clone(),
        },
        &mut transport,
    )
    .await
}

async fn read_h3_request_body<S>(
    stream: &mut h3::server::RequestStream<S, bytes::Bytes>,
    request_headers: &HeaderMap,
    max_request_body_bytes: usize,
) -> Result<Vec<u8>>
where
    S: h3::quic::RecvStream,
{
    let mut body_bytes = request_body_buffer(request_headers, max_request_body_bytes)?;
    let mut total_body = 0usize;
    while let Some(mut chunk) = stream
        .recv_data()
        .await
        .map_err(h3_error)
        .context("failed to read h3 request body")?
    {
        total_body = next_body_len(total_body, chunk.remaining(), max_request_body_bytes)?;
        extend_body_from_buf(&mut body_bytes, &mut chunk);
    }
    Ok(body_bytes)
}

async fn send_h3_success_headers<S>(
    stream: &mut h3::server::RequestStream<S, bytes::Bytes>,
    status: reqwest::StatusCode,
    response_headers: &HeaderMap,
    retry: &PylonRetryConfig,
    metrics: Option<&PylonMetrics>,
    inference_server_id: &str,
) -> Result<()>
where
    S: h3::quic::SendStream<bytes::Bytes>,
{
    let response = h3_success_response(
        status,
        response_headers,
        retry,
        metrics,
        inference_server_id,
    )?;
    stream
        .send_response(response)
        .await
        .map_err(h3_error)
        .context("failed to send h3 response headers")
}

fn h3_success_response(
    status: reqwest::StatusCode,
    response_headers: &HeaderMap,
    retry: &PylonRetryConfig,
    metrics: Option<&PylonMetrics>,
    inference_server_id: &str,
) -> Result<http::Response<()>> {
    let headers = build_response_headers(
        status,
        response_headers,
        retry,
        metrics,
        inference_server_id,
        true,
    )?;
    let mut response = http::Response::builder()
        .status(status.as_u16())
        .body(())
        .context("build h3 response")?;
    for (name, value) in &headers {
        response.headers_mut().append(name, value.clone());
    }
    Ok(response)
}

async fn send_h3_error_response<S>(
    stream: &mut h3::server::RequestStream<S, bytes::Bytes>,
    status: reqwest::StatusCode,
    message: String,
) -> Result<()>
where
    S: h3::quic::SendStream<bytes::Bytes>,
{
    let response = h3_error_response(status)?;
    stream
        .send_response(response)
        .await
        .map_err(h3_error)
        .context("failed to send h3 error response headers")?;
    let body = problem_details_body(status, message);
    stream
        .send_data(bytes::Bytes::from(body))
        .await
        .map_err(h3_error)
        .context("failed to send h3 error response body")?;
    stream
        .finish()
        .await
        .map_err(h3_error)
        .context("failed to finish h3 error response stream")
}

fn h3_error_response(status: reqwest::StatusCode) -> Result<http::Response<()>> {
    http::Response::builder()
        .status(status.as_u16())
        .header(
            reqwest::header::CONTENT_TYPE.as_str(),
            "application/problem+json",
        )
        .body(())
        .context("build h3 error response")
}

async fn send_h3_queue_mismatch_response<S>(
    stream: &mut h3::server::RequestStream<S, bytes::Bytes>,
    app: &TunnelServerApp,
    decision: &QueueAdmissionDecision,
) -> Result<()>
where
    S: h3::quic::SendStream<bytes::Bytes>,
{
    let response = h3_queue_mismatch_response(app, decision)?;
    stream
        .send_response(response)
        .await
        .map_err(h3_error)
        .context("failed to send h3 queue mismatch headers")?;
    stream
        .send_data(bytes::Bytes::from(queue_mismatch_body(decision)))
        .await
        .map_err(h3_error)
        .context("failed to send h3 queue mismatch body")?;
    stream
        .finish()
        .await
        .map_err(h3_error)
        .context("failed to finish h3 queue mismatch response")
}

fn h3_queue_mismatch_response(
    app: &TunnelServerApp,
    decision: &QueueAdmissionDecision,
) -> Result<http::Response<()>> {
    let headers = queue_mismatch_response_headers(app, decision, false)?;
    let mut response = http::Response::builder()
        .status(reqwest::StatusCode::TOO_MANY_REQUESTS.as_u16())
        .body(())
        .context("build h3 queue mismatch response")?;
    for (name, value) in &headers {
        response.headers_mut().append(name, value.clone());
    }
    Ok(response)
}

async fn send_h3_local_connect_failure_response<S>(
    stream: &mut h3::server::RequestStream<S, bytes::Bytes>,
    app: &TunnelServerApp,
    error: &UpstreamRequestError,
    retryable: bool,
) -> Result<()>
where
    S: h3::quic::SendStream<bytes::Bytes>,
{
    let (status, response) = h3_local_connect_failure_response(app, error, retryable)?;
    stream
        .send_response(response)
        .await
        .map_err(h3_error)
        .context("failed to send h3 local connect failure response headers")?;
    let body = problem_details_body(status, "local upstream connection failed");
    stream
        .send_data(bytes::Bytes::from(body))
        .await
        .map_err(h3_error)
        .context("failed to send h3 local connect failure response body")?;
    stream
        .finish()
        .await
        .map_err(h3_error)
        .context("failed to finish h3 local connect failure response stream")
}

fn h3_local_connect_failure_response(
    app: &TunnelServerApp,
    error: &UpstreamRequestError,
    retryable: bool,
) -> Result<(reqwest::StatusCode, http::Response<()>)> {
    let status = record_local_connect_failure(app, error, retryable);
    let mut response = http::Response::builder()
        .status(status.as_u16())
        .header(
            reqwest::header::CONTENT_TYPE.as_str(),
            "application/problem+json",
        )
        .body(())
        .context("build h3 local connect failure response")?;
    response.headers_mut().insert(
        HeaderName::from_static(HEADER_STARGATE_RETRYABLE),
        HeaderValue::from_static(if retryable { "true" } else { "false" }),
    );
    response.headers_mut().insert(
        HeaderName::from_static(HEADER_STARGATE_RETRY_REASON),
        HeaderValue::from_static(RETRY_REASON_LOCAL_CONNECT_FAILURE),
    );
    Ok((status, response))
}

pub(super) fn h3_error<E>(error: E) -> anyhow::Error
where
    E: std::error::Error + Send + Sync + 'static,
{
    anyhow::Error::new(error)
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use super::*;
    use crate::output_token_parser::OutputTokenParserFactory;
    use crate::queue_admission::{PylonQueueMismatchRetryConfig, QueueAdmissionTracker};
    use crate::request_quality_monitor::RequestQualityMonitorConfig;

    fn test_app() -> TunnelServerApp {
        TunnelServerApp {
            http_client: reqwest::Client::new(),
            inference_server_id: "inst-a".to_string(),
            upstream_http_base_url: "http://127.0.0.1:1".to_string(),
            max_request_body_bytes: 8,
            first_output_timeout: Duration::from_secs(1),
            output_chunk_timeout: Duration::from_secs(1),
            output_token_parser_factory: OutputTokenParserFactory,
            request_observation_tx: None,
            request_quality_monitor: RequestQualityMonitorConfig::default(),
            retry: PylonRetryConfig::default(),
            queue_mismatch_retry: PylonQueueMismatchRetryConfig::default(),
            queue_tracker: QueueAdmissionTracker::default(),
            metrics: None,
            #[cfg(test)]
            webtransport_stream_header_wait_tx: None,
        }
    }

    fn header_value<'a>(headers: &'a HeaderMap, name: &str) -> &'a str {
        headers
            .get(name)
            .and_then(|value| value.to_str().ok())
            .expect("header should be present")
    }

    #[test]
    fn h3_error_context_preserves_source_chain() {
        let error = h3_error(std::io::Error::other("inner h3 failure")).context("outer h3 context");

        assert_eq!(error.to_string(), "outer h3 context");
        assert!(
            error
                .chain()
                .any(|source| source.to_string() == "inner h3 failure"),
            "source chain should retain the original error: {error:#}"
        );
    }

    #[test]
    fn h3_adapter_rejects_request_body_chunks_over_limit() {
        let error = next_body_len(7, 2, 8).expect_err("body should exceed adapter limit");

        assert!(
            error.to_string().contains("request body too large"),
            "unexpected body limit error: {error}"
        );
    }

    #[test]
    fn h3_success_response_omits_content_length_and_keeps_retry_metadata() {
        let retry = PylonRetryConfig::default();
        let mut upstream_headers = HeaderMap::new();
        upstream_headers.insert(
            retry.upstream_retry_header.clone(),
            HeaderValue::from_static("true"),
        );
        upstream_headers.insert(reqwest::header::RETRY_AFTER, HeaderValue::from_static("2"));
        upstream_headers.insert(
            reqwest::header::CONTENT_LENGTH,
            HeaderValue::from_static("99"),
        );

        let response = h3_success_response(
            reqwest::StatusCode::TOO_MANY_REQUESTS,
            &upstream_headers,
            &retry,
            None,
            "inst-a",
        )
        .expect("h3 success response should build");

        assert_eq!(response.status(), reqwest::StatusCode::TOO_MANY_REQUESTS);
        assert_eq!(
            header_value(response.headers(), HEADER_STARGATE_RETRYABLE),
            "true"
        );
        assert_eq!(
            header_value(response.headers(), HEADER_STARGATE_RETRY_REASON),
            "upstream_admission_rejected"
        );
        assert_eq!(
            header_value(response.headers(), "x-stargate-retry-after-ms"),
            "2000"
        );
        assert!(
            response
                .headers()
                .get(reqwest::header::CONTENT_LENGTH)
                .is_none()
        );
    }

    #[test]
    fn h3_error_response_uses_problem_json_status() {
        let response =
            h3_error_response(reqwest::StatusCode::BAD_REQUEST).expect("h3 error response");

        assert_eq!(response.status(), reqwest::StatusCode::BAD_REQUEST);
        assert_eq!(
            header_value(response.headers(), reqwest::header::CONTENT_TYPE.as_str()),
            "application/problem+json"
        );
    }

    #[test]
    fn h3_queue_mismatch_response_sets_retry_metadata() {
        let app = test_app();
        let decision = QueueAdmissionDecision::Rejected {
            expected_ms: 10,
            actual_ms: 55,
            threshold_ms: 25,
            retry_after_ms: Some(7),
        };

        let response =
            h3_queue_mismatch_response(&app, &decision).expect("queue mismatch response");

        assert_eq!(response.status(), reqwest::StatusCode::TOO_MANY_REQUESTS);
        assert_eq!(
            header_value(response.headers(), HEADER_STARGATE_RETRYABLE),
            "true"
        );
        assert_eq!(
            header_value(response.headers(), HEADER_STARGATE_RETRY_REASON),
            "queue_estimate_mismatch"
        );
        assert_eq!(
            header_value(response.headers(), "x-stargate-retry-after-ms"),
            "7"
        );
    }

    #[test]
    fn h3_local_connect_failure_response_encodes_retryability() {
        let app = test_app();
        let error = UpstreamRequestError::Build(anyhow::anyhow!("cannot build"));

        let (status, response) =
            h3_local_connect_failure_response(&app, &error, true).expect("local failure response");

        assert_eq!(status, reqwest::StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(response.status(), reqwest::StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(
            header_value(response.headers(), HEADER_STARGATE_RETRYABLE),
            "true"
        );
        assert_eq!(
            header_value(response.headers(), HEADER_STARGATE_RETRY_REASON),
            RETRY_REASON_LOCAL_CONNECT_FAILURE
        );
    }
}

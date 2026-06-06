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
use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use stargate_protocol::tunnel_contract::{HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE};
use stargate_protocol::{RecvStream, SendStream};
use tokio_util::sync::CancellationToken;
use tokio_util::task::TaskTracker;

use crate::queue_admission::QueueAdmissionDecision;
use crate::stats::PylonMetrics;

use super::core::{
    PylonRetryConfig, RETRY_REASON_LOCAL_CONNECT_FAILURE, ResponseBodyEventSink,
    TunnelRequestParts, TunnelRequestTransport, TunnelServerApp, UpstreamRequestError,
    build_response_headers, forward_tunnel_request, next_body_len, problem_details_body,
    queue_mismatch_body, queue_mismatch_response_headers, record_local_connect_failure,
    request_body_buffer,
};

pub(super) async fn handle_custom_connection(
    incoming: quinn::Incoming,
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
    app: TunnelServerApp,
) -> Result<()> {
    let connection = incoming.await.context("accept quic connection failed")?;
    loop {
        tokio::select! {
            _ = shutdown.cancelled() => break,
            stream = connection.accept_bi() => {
                let Ok((quinn_send, quinn_recv)) = stream else {
                    break;
                };
                let app = app.clone();
                task_tracker.spawn(async move {
                    if let Err(error) = handle_stream(quinn_send, quinn_recv, &app).await {
                        tracing::warn!(error = %error, "quic tunnel stream failed");
                    }
                });
            }
        }
    }
    Ok(())
}

struct CustomTunnelTransport {
    recv_stream: RecvStream,
    send_stream: SendStream,
}

impl ResponseBodyEventSink for CustomTunnelTransport {
    async fn send_body_event(&mut self, event: bytes::Bytes) -> Result<()> {
        self.send_stream
            .send_body(event)
            .await
            .context("failed to send response body event")
    }
}

impl TunnelRequestTransport for CustomTunnelTransport {
    async fn read_request_body(
        &mut self,
        request_headers: &HeaderMap,
        max_request_body_bytes: usize,
    ) -> Result<Vec<u8>> {
        let mut body_bytes = request_body_buffer(request_headers, max_request_body_bytes)?;
        let mut total_body = 0usize;
        while let Some(chunk) = self.recv_stream.recv_body().await?.into_body() {
            total_body = next_body_len(total_body, chunk.len(), max_request_body_bytes)?;
            body_bytes.extend_from_slice(&chunk);
        }
        Ok(body_bytes)
    }

    async fn send_success(
        &mut self,
        status: reqwest::StatusCode,
        response_headers: &HeaderMap,
        retry: &PylonRetryConfig,
        metrics: Option<&PylonMetrics>,
        inference_server_id: &str,
    ) -> Result<()> {
        send_success_headers(
            &mut self.send_stream,
            status,
            response_headers,
            retry,
            metrics,
            inference_server_id,
        )
        .await
    }

    async fn send_error(&mut self, status: reqwest::StatusCode, message: String) -> Result<()> {
        send_error_response(&mut self.send_stream, status, message).await
    }

    async fn send_queue_mismatch(
        &mut self,
        app: &TunnelServerApp,
        decision: &QueueAdmissionDecision,
    ) -> Result<()> {
        send_queue_mismatch_response(&mut self.send_stream, app, decision).await
    }

    async fn send_local_connect_failure(
        &mut self,
        app: &TunnelServerApp,
        error: &UpstreamRequestError,
        retryable: bool,
    ) -> Result<()> {
        send_local_connect_failure_response(&mut self.send_stream, app, error, retryable).await
    }

    async fn finish_response(&mut self) -> Result<()> {
        self.send_stream
            .finish()
            .context("failed to finish response stream")
    }
}

pub(super) async fn handle_stream(
    quinn_send: quinn::SendStream,
    quinn_recv: quinn::RecvStream,
    app: &TunnelServerApp,
) -> Result<()> {
    let mut transport = CustomTunnelTransport {
        recv_stream: RecvStream::new(quinn_recv),
        send_stream: SendStream::new(quinn_send),
    };

    let request_headers = transport
        .recv_stream
        .recv_header()
        .await
        .context("failed to read request headers")?;

    let method: reqwest::Method = request_headers
        .get("x-method")
        .and_then(|v| v.to_str().ok())
        .ok_or_else(|| anyhow::anyhow!("missing required x-method header"))?
        .parse()
        .context("invalid method")?;

    let path_and_query = request_headers
        .get("x-path")
        .and_then(|v| v.to_str().ok())
        .ok_or_else(|| anyhow::anyhow!("missing required x-path header"))?
        .to_string();
    forward_tunnel_request(
        app,
        TunnelRequestParts {
            method,
            path_and_query,
            headers: request_headers,
        },
        &mut transport,
    )
    .await
}

async fn send_success_headers(
    send_stream: &mut SendStream,
    status: reqwest::StatusCode,
    response_headers: &HeaderMap,
    retry: &PylonRetryConfig,
    metrics: Option<&PylonMetrics>,
    inference_server_id: &str,
) -> Result<()> {
    let header_frame = custom_success_header_frame(
        status,
        response_headers,
        retry,
        metrics,
        inference_server_id,
    )?;
    send_stream
        .send_header(header_frame)
        .await
        .context("failed to send response headers")
}

fn custom_success_header_frame(
    status: reqwest::StatusCode,
    response_headers: &HeaderMap,
    retry: &PylonRetryConfig,
    metrics: Option<&PylonMetrics>,
    inference_server_id: &str,
) -> Result<HeaderMap> {
    let mut header_frame = build_response_headers(
        status,
        response_headers,
        retry,
        metrics,
        inference_server_id,
        false,
    )?;
    header_frame.insert(
        HeaderName::from_static("x-status"),
        HeaderValue::from_str(&status.as_u16().to_string()).context("invalid status code")?,
    );
    Ok(header_frame)
}

async fn send_error_response(
    send_stream: &mut SendStream,
    status: reqwest::StatusCode,
    message: String,
) -> Result<()> {
    let header_frame = custom_error_header_frame(status)?;
    send_stream
        .send_header(header_frame)
        .await
        .context("failed to send error response headers")?;
    let body = problem_details_body(status, message);
    send_stream
        .send_body(body.into_bytes().into())
        .await
        .context("failed to send error response body")?;
    send_stream
        .finish()
        .context("failed to finish error response stream")?;
    Ok(())
}

fn custom_error_header_frame(status: reqwest::StatusCode) -> Result<HeaderMap> {
    let mut header_frame = HeaderMap::new();
    header_frame.insert(
        HeaderName::from_static("x-status"),
        HeaderValue::from_str(&status.as_u16().to_string()).context("invalid status code")?,
    );
    header_frame.insert(
        reqwest::header::CONTENT_TYPE,
        HeaderValue::from_static("application/problem+json"),
    );
    Ok(header_frame)
}

async fn send_queue_mismatch_response(
    send_stream: &mut SendStream,
    app: &TunnelServerApp,
    decision: &QueueAdmissionDecision,
) -> Result<()> {
    let headers = queue_mismatch_response_headers(app, decision, true)?;
    send_stream
        .send_header(headers)
        .await
        .context("failed to send queue mismatch response headers")?;
    send_stream
        .send_body(queue_mismatch_body(decision).into_bytes().into())
        .await
        .context("failed to send queue mismatch response body")?;
    send_stream
        .finish()
        .context("failed to finish queue mismatch response")?;
    Ok(())
}

async fn send_local_connect_failure_response(
    send_stream: &mut SendStream,
    app: &TunnelServerApp,
    error: &UpstreamRequestError,
    retryable: bool,
) -> Result<()> {
    let (status, header_frame) = custom_local_connect_failure_header_frame(app, error, retryable);
    send_stream
        .send_header(header_frame)
        .await
        .context("failed to send local connect failure response headers")?;
    let body = problem_details_body(status, "local upstream connection failed");
    send_stream
        .send_body(body.into_bytes().into())
        .await
        .context("failed to send local connect failure response body")?;
    send_stream
        .finish()
        .context("failed to finish local connect failure response stream")?;
    Ok(())
}

fn custom_local_connect_failure_header_frame(
    app: &TunnelServerApp,
    error: &UpstreamRequestError,
    retryable: bool,
) -> (reqwest::StatusCode, HeaderMap) {
    let status = record_local_connect_failure(app, error, retryable);
    let mut header_frame = HeaderMap::new();
    header_frame.insert(
        HeaderName::from_static("x-status"),
        HeaderValue::from_static("503"),
    );
    header_frame.insert(
        reqwest::header::CONTENT_TYPE,
        HeaderValue::from_static("application/problem+json"),
    );
    header_frame.insert(
        HeaderName::from_static(HEADER_STARGATE_RETRYABLE),
        HeaderValue::from_static(if retryable { "true" } else { "false" }),
    );
    header_frame.insert(
        HeaderName::from_static(HEADER_STARGATE_RETRY_REASON),
        HeaderValue::from_static(RETRY_REASON_LOCAL_CONNECT_FAILURE),
    );
    (status, header_frame)
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
    fn custom_adapter_rejects_request_body_chunks_over_limit() {
        let error = next_body_len(7, 2, 8).expect_err("body should exceed adapter limit");

        assert!(
            error.to_string().contains("request body too large"),
            "unexpected body limit error: {error}"
        );
    }

    #[test]
    fn custom_success_headers_preserve_status_and_retry_metadata() {
        let retry = PylonRetryConfig::default();
        let mut upstream_headers = HeaderMap::new();
        upstream_headers.insert(
            retry.upstream_retry_header.clone(),
            HeaderValue::from_static("true"),
        );
        upstream_headers.insert(reqwest::header::RETRY_AFTER, HeaderValue::from_static("2"));

        let headers = custom_success_header_frame(
            reqwest::StatusCode::TOO_MANY_REQUESTS,
            &upstream_headers,
            &retry,
            None,
            "inst-a",
        )
        .expect("custom success headers should build");

        assert_eq!(header_value(&headers, "x-status"), "429");
        assert_eq!(header_value(&headers, HEADER_STARGATE_RETRYABLE), "true");
        assert_eq!(
            header_value(&headers, HEADER_STARGATE_RETRY_REASON),
            "upstream_admission_rejected"
        );
        assert_eq!(header_value(&headers, "x-stargate-retry-after-ms"), "2000");
    }

    #[test]
    fn custom_error_headers_use_problem_json_with_custom_status() {
        let headers = custom_error_header_frame(reqwest::StatusCode::BAD_REQUEST)
            .expect("custom error headers should build");

        assert_eq!(header_value(&headers, "x-status"), "400");
        assert_eq!(
            header_value(&headers, reqwest::header::CONTENT_TYPE.as_str()),
            "application/problem+json"
        );
    }

    #[test]
    fn custom_queue_mismatch_headers_include_custom_status_and_retry_reason() {
        let app = test_app();
        let decision = QueueAdmissionDecision::Rejected {
            expected_ms: 10,
            actual_ms: 55,
            threshold_ms: 25,
            retry_after_ms: Some(7),
        };

        let headers =
            queue_mismatch_response_headers(&app, &decision, true).expect("headers should build");

        assert_eq!(header_value(&headers, "x-status"), "429");
        assert_eq!(header_value(&headers, HEADER_STARGATE_RETRYABLE), "true");
        assert_eq!(
            header_value(&headers, HEADER_STARGATE_RETRY_REASON),
            "queue_estimate_mismatch"
        );
        assert_eq!(header_value(&headers, "x-stargate-retry-after-ms"), "7");
    }

    #[test]
    fn custom_local_connect_failure_headers_encode_retryability() {
        let app = test_app();
        let error = UpstreamRequestError::Build(anyhow::anyhow!("cannot build"));

        let (status, headers) = custom_local_connect_failure_header_frame(&app, &error, true);

        assert_eq!(status, reqwest::StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(header_value(&headers, "x-status"), "503");
        assert_eq!(header_value(&headers, HEADER_STARGATE_RETRYABLE), "true");
        assert_eq!(
            header_value(&headers, HEADER_STARGATE_RETRY_REASON),
            RETRY_REASON_LOCAL_CONNECT_FAILURE
        );
    }
}

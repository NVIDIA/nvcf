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
use stargate_protocol::{RecvStream, SendStream};
use tokio_util::{sync::CancellationToken, task::TaskTracker};

use super::core::{
    ResponseBodyEventSink, TunnelRequestParts, TunnelRequestTransport, TunnelServerApp,
    forward_tunnel_request, next_body_len, request_body_buffer, serve_bidi_streams,
};

pub(super) async fn handle_raw_quic_connection(
    incoming: quinn::Incoming,
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
    app: TunnelServerApp,
) -> Result<()> {
    let connection = incoming.await.context("accept quic connection failed")?;
    serve_bidi_streams(
        (),
        app,
        connection,
        shutdown,
        task_tracker,
        handle_stream,
        |error| tracing::warn!(%error, "quic tunnel stream failed"),
    )
    .await;
    Ok(())
}

struct RawQuicTunnelTransport {
    recv_stream: RecvStream,
    send_stream: SendStream,
}

impl ResponseBodyEventSink for RawQuicTunnelTransport {
    async fn send_body_event(&mut self, event: bytes::Bytes) -> Result<()> {
        self.send_stream
            .send_body(event)
            .await
            .context("failed to send response body event")
    }
}

impl TunnelRequestTransport for RawQuicTunnelTransport {
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

    async fn send_response_head(
        &mut self,
        status: reqwest::StatusCode,
        headers: HeaderMap,
    ) -> Result<()> {
        self.send_stream
            .send_header(raw_quic_response_headers(status, headers)?)
            .await
            .context("failed to send response headers")
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
    app: TunnelServerApp,
) -> Result<()> {
    let mut transport = RawQuicTunnelTransport {
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
        &app,
        TunnelRequestParts {
            method,
            path_and_query,
            headers: request_headers,
        },
        &mut transport,
    )
    .await
}

fn raw_quic_response_headers(
    status: reqwest::StatusCode,
    mut headers: HeaderMap,
) -> Result<HeaderMap> {
    headers.insert(
        HeaderName::from_static("x-status"),
        HeaderValue::from_str(&status.as_u16().to_string()).context("invalid status code")?,
    );
    Ok(headers)
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use super::super::core::{
        PylonRetryConfig, RETRY_REASON_LOCAL_CONNECT_FAILURE, UpstreamRequestError,
        build_response_headers, local_connect_failure_headers, problem_response_headers,
        queue_mismatch_response_headers, record_local_connect_failure,
    };
    use super::*;
    use crate::queue_admission::{PylonQueueMismatchRetryConfig, QueueAdmissionDecision};
    use crate::request_quality_monitor::RequestQualityMonitorConfig;
    use crate::runtime_state::PylonRuntimeState;
    use stargate_protocol::tunnel_contract::{
        HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE,
    };

    fn test_app() -> TunnelServerApp {
        TunnelServerApp {
            http_client: reqwest::Client::new(),
            inference_server_id: "inst-a".to_string(),
            upstream_http_base_url: "http://127.0.0.1:1".to_string(),
            max_request_body_bytes: 8,
            max_sse_buffer_bytes: 1024,
            first_output_timeout: Duration::from_secs(1),
            output_chunk_timeout: Duration::from_secs(1),
            runtime_state: PylonRuntimeState::default(),
            request_quality_monitor: RequestQualityMonitorConfig::default(),
            retry: PylonRetryConfig::default(),
            queue_mismatch_retry: PylonQueueMismatchRetryConfig::default(),
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
    fn raw_quic_adapter_rejects_request_body_chunks_over_limit() {
        let error = next_body_len(7, 2, 8).expect_err("body should exceed adapter limit");

        assert!(
            error.to_string().contains("request body too large"),
            "unexpected body limit error: {error}"
        );
    }

    #[test]
    fn raw_quic_success_headers_preserve_status_and_retry_metadata() {
        let retry = PylonRetryConfig::default();
        let mut upstream_headers = HeaderMap::new();
        upstream_headers.insert(
            retry.upstream_retry_header.clone(),
            HeaderValue::from_static("true"),
        );
        upstream_headers.insert(reqwest::header::RETRY_AFTER, HeaderValue::from_static("2"));

        let status = reqwest::StatusCode::TOO_MANY_REQUESTS;
        let headers = build_response_headers(status, &upstream_headers, &retry, None, "inst-a")
            .and_then(|headers| raw_quic_response_headers(status, headers))
            .expect("raw QUIC success headers should build");

        assert_eq!(header_value(&headers, "x-status"), "429");
        assert_eq!(header_value(&headers, HEADER_STARGATE_RETRYABLE), "true");
        assert_eq!(
            header_value(&headers, HEADER_STARGATE_RETRY_REASON),
            "upstream_admission_rejected"
        );
        assert_eq!(header_value(&headers, "x-stargate-retry-after-ms"), "2000");
    }

    #[test]
    fn raw_quic_error_headers_use_problem_json_with_raw_quic_status() {
        let status = reqwest::StatusCode::BAD_REQUEST;
        let headers = raw_quic_response_headers(status, problem_response_headers())
            .expect("raw QUIC error headers should build");

        assert_eq!(header_value(&headers, "x-status"), "400");
        assert_eq!(
            header_value(&headers, reqwest::header::CONTENT_TYPE.as_str()),
            "application/problem+json"
        );
    }

    #[test]
    fn raw_quic_queue_mismatch_headers_include_raw_quic_status_and_retry_reason() {
        let app = test_app();
        let decision = QueueAdmissionDecision::Rejected {
            expected_ms: 10,
            actual_ms: 55,
            threshold_ms: 25,
            retry_after_ms: Some(7),
        };

        let status = reqwest::StatusCode::TOO_MANY_REQUESTS;
        let headers = queue_mismatch_response_headers(&app, &decision)
            .and_then(|headers| raw_quic_response_headers(status, headers))
            .expect("headers should build");

        assert_eq!(header_value(&headers, "x-status"), "429");
        assert_eq!(header_value(&headers, HEADER_STARGATE_RETRYABLE), "true");
        assert_eq!(
            header_value(&headers, HEADER_STARGATE_RETRY_REASON),
            "queue_estimate_mismatch"
        );
        assert_eq!(header_value(&headers, "x-stargate-retry-after-ms"), "7");
    }

    #[test]
    fn raw_quic_local_connect_failure_headers_encode_retryability() {
        let app = test_app();
        let error = UpstreamRequestError::Build(anyhow::anyhow!("cannot build"));

        let status = record_local_connect_failure(&app, &error, true);
        let headers = raw_quic_response_headers(status, local_connect_failure_headers(true))
            .expect("local failure headers should build");

        assert_eq!(status, reqwest::StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(header_value(&headers, "x-status"), "503");
        assert_eq!(header_value(&headers, HEADER_STARGATE_RETRYABLE), "true");
        assert_eq!(
            header_value(&headers, HEADER_STARGATE_RETRY_REASON),
            RETRY_REASON_LOCAL_CONNECT_FAILURE
        );
    }
}

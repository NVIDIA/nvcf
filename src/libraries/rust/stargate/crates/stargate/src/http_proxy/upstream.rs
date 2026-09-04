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

use axum::body::Body;
use axum::http::{HeaderMap, HeaderName, HeaderValue, Method, StatusCode};
use futures::{Stream, StreamExt};
use stargate_protocol::common::is_hop_by_hop_header;
use stargate_protocol::tunnel_contract::{
    HEADER_STARGATE_EXPECTED_QUEUE_MS, HEADER_STARGATE_RETRY_AFTER_MS,
    HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE,
};
use tracing::{Span, warn};
use tracing_opentelemetry::OpenTelemetrySpanExt;

use crate::routing_state::RegistrationGeneration;
use crate::telemetry::inject_trace_context;

use super::{HEADER_ROUTING_METHOD, HEADER_STARGATE_ERROR_CODE, ProxyAppState};

pub(super) struct UpstreamStreamingResponse {
    pub(super) status: StatusCode,
    pub(super) headers: HeaderMap,
    pub(super) body: Body,
}

pub(super) async fn proxy_via_quic_streaming(
    app: &ProxyAppState,
    registration: &std::sync::Arc<RegistrationGeneration>,
    method: Method,
    path_and_query: &str,
    forwarded_headers: HeaderMap,
    request_body: impl FnOnce() -> Result<Body, StatusCode> + Send,
) -> Result<UpstreamStreamingResponse, StatusCode> {
    let inference_server_id = registration.inference_server_id();
    let streaming_resp = app
        .quic_proxy
        .open_streaming_request(registration, method, path_and_query, forwarded_headers)
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
    let chunks = futures::stream::unfold(streaming_resp.body_stream, |mut body_stream| async {
        body_stream
            .recv_body()
            .await
            .transpose()
            .map(|chunk| (chunk, body_stream))
    });

    Ok(UpstreamStreamingResponse {
        status,
        headers,
        body: upstream_body(inference_server_id.to_owned(), status, chunks),
    })
}

/// Relays upstream body chunks to the client. A mid-stream failure is the only
/// signal an in-flight request has died with its backend, so it is logged here
/// with the backend and how far the response got.
fn upstream_body(
    inference_server_id: String,
    status: StatusCode,
    chunks: impl Stream<Item = anyhow::Result<bytes::Bytes>> + Send + 'static,
) -> Body {
    Body::from_stream(async_stream::stream! {
        let mut chunks = std::pin::pin!(chunks);
        let mut streamed_bytes: u64 = 0;
        while let Some(chunk) = chunks.next().await {
            match chunk {
                Ok(chunk) => {
                    streamed_bytes += chunk.len() as u64;
                    yield Ok(chunk);
                }
                Err(error) => {
                    warn!(
                        inference_server_id = %inference_server_id,
                        status = status.as_u16(),
                        streamed_bytes,
                        error = %error,
                        "upstream response body stream failed"
                    );
                    yield Err(std::io::Error::other(error.to_string()));
                    break;
                }
            }
        }
    })
}

pub(super) fn prepare_forwarded_headers(headers: &HeaderMap) -> HeaderMap {
    let mut forwarded_headers = HeaderMap::new();
    copy_forwardable_headers(headers, &mut forwarded_headers);
    forwarded_headers
}

pub(super) fn headers_for_upstream_attempt(
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

fn should_forward_header(name: &HeaderName) -> bool {
    !is_hop_by_hop_header(name)
        && !matches!(
            name.as_str(),
            "host"
                | HEADER_ROUTING_METHOD
                | HEADER_STARGATE_RETRYABLE
                | HEADER_STARGATE_RETRY_REASON
                | HEADER_STARGATE_RETRY_AFTER_MS
                | HEADER_STARGATE_EXPECTED_QUEUE_MS
                | HEADER_STARGATE_ERROR_CODE
        )
}

pub(super) fn copy_forwardable_headers(from: &HeaderMap, to: &mut HeaderMap) {
    for (name, value) in from {
        if should_forward_header(name) {
            to.append(name, value.clone());
        }
    }
}

#[cfg(test)]
mod tests {
    use stargate_protocol::tunnel_contract::HEADER_MODEL;

    use crate::routing_state::{RegistrationIdentity, test_registration_generation};

    use super::super::retry::ReplayableRequestBody;
    use super::super::test_support::test_proxy_app_state;
    use super::*;
    use crate::test_logs::capture_logs_async;

    fn headers<const N: usize>(entries: [(&'static str, &'static str); N]) -> HeaderMap {
        entries
            .into_iter()
            .map(|(name, value)| {
                (
                    HeaderName::from_static(name),
                    HeaderValue::from_static(value),
                )
            })
            .collect()
    }

    #[test]
    fn prepare_forwarded_headers_strips_internal_proxy_headers() {
        let source = headers([
            ("connection", "close"),
            ("host", "example.test"),
            ("x-routing-method", "random"),
            (HEADER_STARGATE_ERROR_CODE, "no_eligible_candidates"),
            (HEADER_MODEL, "gpt"),
            ("x-upstream-header", "kept"),
        ]);

        let forwarded = prepare_forwarded_headers(&source);

        assert!(!forwarded.contains_key("connection"));
        assert!(!forwarded.contains_key("host"));
        assert!(!forwarded.contains_key("x-routing-method"));
        assert!(!forwarded.contains_key(HEADER_STARGATE_ERROR_CODE));
        assert_eq!(forwarded.get(HEADER_MODEL).unwrap(), "gpt");
        assert_eq!(forwarded.get("x-upstream-header").unwrap(), "kept");
    }

    #[test]
    fn headers_for_upstream_attempt_preserves_headers_and_adds_queue_estimate() {
        let span = tracing::info_span!("attempt_header_test");
        let forwarded_headers = headers([(HEADER_MODEL, "gpt")]);

        let attempt_headers = headers_for_upstream_attempt(&forwarded_headers, &span, Some(42));

        assert_eq!(attempt_headers.get(HEADER_MODEL).unwrap(), "gpt");
        assert_eq!(
            attempt_headers
                .get(HEADER_STARGATE_EXPECTED_QUEUE_MS)
                .unwrap(),
            "42"
        );
    }

    #[test]
    fn copy_forwardable_headers_strips_internal_retry_headers() {
        let upstream = headers([
            (HEADER_STARGATE_ERROR_CODE, "no_eligible_candidates"),
            (HEADER_STARGATE_RETRYABLE, "true"),
            (HEADER_STARGATE_RETRY_REASON, "retryable_proxy_error"),
            (HEADER_STARGATE_RETRY_AFTER_MS, "25"),
            (HEADER_STARGATE_EXPECTED_QUEUE_MS, "123"),
            ("x-upstream-header", "preserved"),
        ]);

        let mut downstream = HeaderMap::new();
        copy_forwardable_headers(&upstream, &mut downstream);

        assert!(!downstream.contains_key(HEADER_STARGATE_ERROR_CODE));
        assert!(!downstream.contains_key(HEADER_STARGATE_RETRYABLE));
        assert!(!downstream.contains_key(HEADER_STARGATE_RETRY_REASON));
        assert!(!downstream.contains_key(HEADER_STARGATE_RETRY_AFTER_MS));
        assert!(!downstream.contains_key(HEADER_STARGATE_EXPECTED_QUEUE_MS));
        assert_eq!(downstream.get("x-upstream-header").unwrap(), "preserved");
    }

    #[tokio::test]
    async fn transport_setup_failure_does_not_consume_first_replay_body() {
        let app = test_proxy_app_state();
        let registration = test_registration_generation(RegistrationIdentity {
            inference_server_id: "missing-connection".to_string(),
            cluster_id: "missing-connection".to_string(),
            inference_server_url: "quic://127.0.0.1:1".to_string(),
            routing_key: None,
            reverse_tunnel: false,
        });
        let body = Body::from("still-available");
        let mut replay_body = ReplayableRequestBody::new(&HeaderMap::new(), body, 1024).unwrap();

        let result = proxy_via_quic_streaming(
            &app,
            &registration,
            Method::POST,
            "/v1/chat/completions",
            HeaderMap::new(),
            || replay_body.body_for_attempt(),
        )
        .await;

        assert_eq!(result.err(), Some(StatusCode::BAD_GATEWAY));

        let attempt_body = replay_body.body_for_attempt().unwrap();
        let attempt_bytes = axum::body::to_bytes(attempt_body, 1024).await.unwrap();
        assert_eq!(attempt_bytes, "still-available");
    }

    #[tokio::test]
    async fn upstream_body_logs_mid_stream_failure_with_backend_and_progress() {
        let chunks = futures::stream::iter([
            Ok(bytes::Bytes::from_static(b"hello")),
            Err(anyhow::anyhow!("relay connection reset")),
        ]);
        let body = upstream_body("inst-a".to_string(), StatusCode::OK, chunks);

        let (result, logs) =
            capture_logs_async(tracing::Level::WARN, axum::body::to_bytes(body, 1024)).await;

        assert!(
            result.is_err(),
            "client body should surface the upstream failure"
        );
        for expected in [
            "upstream response body stream failed",
            "inference_server_id=inst-a",
            "status=200",
            "streamed_bytes=5",
            "relay connection reset",
        ] {
            assert!(
                logs.contains(expected),
                "expected {expected:?} in body failure log, got:\n{logs}"
            );
        }
    }

    #[tokio::test]
    async fn upstream_body_is_quiet_when_stream_completes() {
        let chunks = futures::stream::iter([
            Ok(bytes::Bytes::from_static(b"hel")),
            Ok(bytes::Bytes::from_static(b"lo")),
        ]);
        let body = upstream_body("inst-a".to_string(), StatusCode::OK, chunks);

        let (result, logs) =
            capture_logs_async(tracing::Level::WARN, axum::body::to_bytes(body, 1024)).await;

        assert_eq!(result.unwrap(), "hello");
        assert!(
            logs.is_empty(),
            "clean completion should not log, got:\n{logs}"
        );
    }
}

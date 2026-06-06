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

use anyhow::{Context, Result, bail};
use reqwest::header::{HeaderMap, HeaderName, HeaderValue};
use stargate_protocol::tunnel_contract::{
    HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE, WEBTRANSPORT_TUNNEL_PATH,
};
use tokio_util::sync::CancellationToken;
use tokio_util::task::TaskTracker;

use crate::queue_admission::QueueAdmissionDecision;
use crate::stats::PylonMetrics;

use super::core::{
    PylonRetryConfig, RETRY_REASON_LOCAL_CONNECT_FAILURE, ResponseBodyEventSink,
    TunnelRequestParts, TunnelRequestTransport, TunnelServerApp, UpstreamRequestError,
    WEBTRANSPORT_STREAM_HEADER_TIMEOUT, build_response_headers, forward_tunnel_request,
    next_body_len, problem_details_body, queue_mismatch_body, queue_mismatch_response_headers,
    record_local_connect_failure, request_body_buffer,
};
use super::http3::h3_error;

pub(super) async fn handle_webtransport_connection(
    incoming: quinn::Incoming,
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
    app: TunnelServerApp,
) -> Result<()> {
    let connection = incoming.await.context("accept quic connection failed")?;
    handle_webtransport_established_connection(connection, shutdown, task_tracker, app).await
}

async fn handle_webtransport_established_connection(
    connection: quinn::Connection,
    shutdown: CancellationToken,
    task_tracker: TaskTracker,
    app: TunnelServerApp,
) -> Result<()> {
    let mut builder = h3::server::builder();
    builder
        .enable_webtransport(true)
        .enable_extended_connect(true)
        .enable_datagram(true)
        .max_webtransport_sessions(1);
    let mut h3_connection: h3::server::Connection<h3_quinn::Connection, bytes::Bytes> = builder
        .build(h3_quinn::Connection::new(connection.clone()))
        .await
        .map_err(h3_error)
        .context("create WebTransport h3 server")?;
    let Some(resolver) = h3_connection
        .accept()
        .await
        .map_err(h3_error)
        .context("accept WebTransport CONNECT")?
    else {
        return Ok(());
    };
    let (request, mut connect_stream) = resolver
        .resolve_request()
        .await
        .map_err(h3_error)
        .context("resolve WebTransport CONNECT")?;
    let is_webtransport = request
        .extensions()
        .get::<h3::ext::Protocol>()
        .is_some_and(|protocol| *protocol == h3::ext::Protocol::WEB_TRANSPORT);
    if request.method() != reqwest::Method::CONNECT
        || request.uri().path() != WEBTRANSPORT_TUNNEL_PATH
        || !is_webtransport
    {
        let response = http::Response::builder()
            .status(reqwest::StatusCode::BAD_REQUEST.as_u16())
            .body(())
            .context("build WebTransport rejection")?;
        connect_stream
            .send_response(response)
            .await
            .map_err(h3_error)
            .context("send WebTransport rejection")?;
        connect_stream
            .finish()
            .await
            .map_err(h3_error)
            .context("finish WebTransport rejection")?;
        bail!("invalid WebTransport CONNECT request");
    }
    let session_id = connect_stream.id().into_inner();
    let response = http::Response::builder()
        .status(reqwest::StatusCode::OK.as_u16())
        .body(())
        .context("build WebTransport CONNECT response")?;
    connect_stream
        .send_response(response)
        .await
        .map_err(h3_error)
        .context("send WebTransport CONNECT response")?;

    loop {
        tokio::select! {
            _ = shutdown.cancelled() => break,
            stream = connection.accept_bi() => {
                let Ok((quinn_send, quinn_recv)) = stream else {
                    break;
                };
                let app = app.clone();
                task_tracker.spawn(async move {
                    if let Err(error) =
                        handle_webtransport_stream(quinn_send, quinn_recv, session_id, app).await
                    {
                        tracing::warn!(error = %error, "WebTransport tunnel stream failed");
                    }
                });
            }
        }
    }
    // Keep the CONNECT stream alive for the duration of the WebTransport loop.
    drop(connect_stream);
    Ok(())
}

struct WebTransportTunnelTransport<'a> {
    recv_stream: &'a mut quinn::RecvStream,
    send_stream: &'a mut quinn::SendStream,
}

impl ResponseBodyEventSink for WebTransportTunnelTransport<'_> {
    async fn send_body_event(&mut self, event: bytes::Bytes) -> Result<()> {
        stargate_protocol::write_webtransport_http_body(self.send_stream, event)
            .await
            .context("failed to send WebTransport response body event")
    }
}

impl TunnelRequestTransport for WebTransportTunnelTransport<'_> {
    async fn read_request_body(
        &mut self,
        request_headers: &HeaderMap,
        max_request_body_bytes: usize,
    ) -> Result<Vec<u8>> {
        read_webtransport_request_body(self.recv_stream, request_headers, max_request_body_bytes)
            .await
    }

    async fn send_success(
        &mut self,
        status: reqwest::StatusCode,
        response_headers: &HeaderMap,
        retry: &PylonRetryConfig,
        metrics: Option<&PylonMetrics>,
        inference_server_id: &str,
    ) -> Result<()> {
        send_webtransport_success_headers(
            self.send_stream,
            status,
            response_headers,
            retry,
            metrics,
            inference_server_id,
        )
        .await
    }

    async fn send_error(&mut self, status: reqwest::StatusCode, message: String) -> Result<()> {
        send_webtransport_error_response(self.send_stream, status, message).await
    }

    async fn send_queue_mismatch(
        &mut self,
        app: &TunnelServerApp,
        decision: &QueueAdmissionDecision,
    ) -> Result<()> {
        send_webtransport_queue_mismatch_response(self.send_stream, app, decision).await
    }

    async fn send_local_connect_failure(
        &mut self,
        app: &TunnelServerApp,
        error: &UpstreamRequestError,
        retryable: bool,
    ) -> Result<()> {
        send_webtransport_local_connect_failure_response(self.send_stream, app, error, retryable)
            .await
    }

    async fn finish_response(&mut self) -> Result<()> {
        stargate_protocol::finish_webtransport_http_stream(self.send_stream)
            .context("failed to finish WebTransport response stream")
    }
}

async fn handle_webtransport_http_stream(
    mut quinn_send: quinn::SendStream,
    mut quinn_recv: quinn::RecvStream,
    app: &TunnelServerApp,
) -> Result<()> {
    let request_head = stargate_protocol::read_webtransport_http_request_head(&mut quinn_recv)
        .await
        .context("failed to read WebTransport request head")?;
    let method: reqwest::Method = request_head
        .method
        .as_str()
        .parse()
        .context("invalid WebTransport request method")?;
    let path_and_query = request_head.path_and_query;
    let request_headers = request_head.headers;
    let mut transport = WebTransportTunnelTransport {
        recv_stream: &mut quinn_recv,
        send_stream: &mut quinn_send,
    };
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

pub(super) async fn handle_webtransport_stream(
    mut quinn_send: quinn::SendStream,
    mut quinn_recv: quinn::RecvStream,
    expected_session_id: u64,
    app: TunnelServerApp,
) -> Result<()> {
    #[cfg(test)]
    if let Some(tx) = &app.webtransport_stream_header_wait_tx {
        let _ = tx.try_send(());
    }
    let stream_session_id = match tokio::time::timeout(
        WEBTRANSPORT_STREAM_HEADER_TIMEOUT,
        stargate_protocol::read_webtransport_bidi_header(&mut quinn_recv),
    )
    .await
    {
        Ok(Ok(session_id)) => session_id,
        Ok(Err(error)) => {
            reset_webtransport_stream(&mut quinn_send, &mut quinn_recv);
            return Err(error).context("invalid WebTransport stream header");
        }
        Err(_) => {
            reset_webtransport_stream(&mut quinn_send, &mut quinn_recv);
            bail!("timed out waiting for WebTransport stream header");
        }
    };
    if stream_session_id != expected_session_id {
        reset_webtransport_stream(&mut quinn_send, &mut quinn_recv);
        bail!(
            "WebTransport stream session id mismatch: got {stream_session_id}, expected {expected_session_id}"
        );
    }

    handle_webtransport_http_stream(quinn_send, quinn_recv, &app).await
}

fn reset_webtransport_stream(
    quinn_send: &mut quinn::SendStream,
    quinn_recv: &mut quinn::RecvStream,
) {
    let _ = quinn_send.reset(0u32.into());
    let _ = quinn_recv.stop(0u32.into());
}

async fn read_webtransport_request_body(
    recv_stream: &mut quinn::RecvStream,
    request_headers: &HeaderMap,
    max_request_body_bytes: usize,
) -> Result<Vec<u8>> {
    let mut body_bytes = request_body_buffer(request_headers, max_request_body_bytes)?;
    let mut total_body = 0usize;
    while let Some(chunk) = stargate_protocol::read_webtransport_http_body_chunk(recv_stream)
        .await
        .context("failed to read WebTransport request body")?
    {
        total_body = next_body_len(total_body, chunk.len(), max_request_body_bytes)?;
        body_bytes.extend_from_slice(&chunk);
    }
    Ok(body_bytes)
}

async fn send_webtransport_success_headers(
    send_stream: &mut quinn::SendStream,
    status: reqwest::StatusCode,
    response_headers: &HeaderMap,
    retry: &PylonRetryConfig,
    metrics: Option<&PylonMetrics>,
    inference_server_id: &str,
) -> Result<()> {
    let head = webtransport_success_head(
        status,
        response_headers,
        retry,
        metrics,
        inference_server_id,
    )?;
    stargate_protocol::write_webtransport_http_response_head(send_stream, &head)
        .await
        .context("failed to send WebTransport response head")
}

fn webtransport_success_head(
    status: reqwest::StatusCode,
    response_headers: &HeaderMap,
    retry: &PylonRetryConfig,
    metrics: Option<&PylonMetrics>,
    inference_server_id: &str,
) -> Result<stargate_protocol::WebTransportHttpResponseHead> {
    let headers = build_response_headers(
        status,
        response_headers,
        retry,
        metrics,
        inference_server_id,
        false,
    )?;
    Ok(stargate_protocol::WebTransportHttpResponseHead { status, headers })
}

async fn send_webtransport_error_response(
    send_stream: &mut quinn::SendStream,
    status: reqwest::StatusCode,
    message: String,
) -> Result<()> {
    let head = webtransport_error_head(status);
    stargate_protocol::write_webtransport_http_response_head(send_stream, &head)
        .await
        .context("failed to send WebTransport error response head")?;
    let body = problem_details_body(status, message);
    stargate_protocol::write_webtransport_http_body(send_stream, bytes::Bytes::from(body))
        .await
        .context("failed to send WebTransport error response body")?;
    stargate_protocol::finish_webtransport_http_stream(send_stream)
        .context("failed to finish WebTransport error response stream")?;
    Ok(())
}

fn webtransport_error_head(
    status: reqwest::StatusCode,
) -> stargate_protocol::WebTransportHttpResponseHead {
    let mut headers = HeaderMap::new();
    headers.insert(
        reqwest::header::CONTENT_TYPE,
        HeaderValue::from_static("application/problem+json"),
    );
    stargate_protocol::WebTransportHttpResponseHead { status, headers }
}

async fn send_webtransport_queue_mismatch_response(
    send_stream: &mut quinn::SendStream,
    app: &TunnelServerApp,
    decision: &QueueAdmissionDecision,
) -> Result<()> {
    let head = webtransport_queue_mismatch_head(app, decision)?;
    stargate_protocol::write_webtransport_http_response_head(send_stream, &head)
        .await
        .context("failed to send WebTransport queue mismatch response head")?;
    stargate_protocol::write_webtransport_http_body(
        send_stream,
        bytes::Bytes::from(queue_mismatch_body(decision)),
    )
    .await
    .context("failed to send WebTransport queue mismatch response body")?;
    stargate_protocol::finish_webtransport_http_stream(send_stream)
        .context("failed to finish WebTransport queue mismatch response")?;
    Ok(())
}

fn webtransport_queue_mismatch_head(
    app: &TunnelServerApp,
    decision: &QueueAdmissionDecision,
) -> Result<stargate_protocol::WebTransportHttpResponseHead> {
    let headers = queue_mismatch_response_headers(app, decision, false)?;
    Ok(stargate_protocol::WebTransportHttpResponseHead {
        status: reqwest::StatusCode::TOO_MANY_REQUESTS,
        headers,
    })
}

async fn send_webtransport_local_connect_failure_response(
    send_stream: &mut quinn::SendStream,
    app: &TunnelServerApp,
    error: &UpstreamRequestError,
    retryable: bool,
) -> Result<()> {
    let (status, head) = webtransport_local_connect_failure_head(app, error, retryable);
    stargate_protocol::write_webtransport_http_response_head(send_stream, &head)
        .await
        .context("failed to send WebTransport local connect failure response head")?;
    let body = problem_details_body(status, "local upstream connection failed");
    stargate_protocol::write_webtransport_http_body(send_stream, bytes::Bytes::from(body))
        .await
        .context("failed to send WebTransport local connect failure response body")?;
    stargate_protocol::finish_webtransport_http_stream(send_stream)
        .context("failed to finish WebTransport local connect failure response stream")?;
    Ok(())
}

fn webtransport_local_connect_failure_head(
    app: &TunnelServerApp,
    error: &UpstreamRequestError,
    retryable: bool,
) -> (
    reqwest::StatusCode,
    stargate_protocol::WebTransportHttpResponseHead,
) {
    let status = record_local_connect_failure(app, error, retryable);
    let mut headers = HeaderMap::new();
    headers.insert(
        reqwest::header::CONTENT_TYPE,
        HeaderValue::from_static("application/problem+json"),
    );
    headers.insert(
        HeaderName::from_static(HEADER_STARGATE_RETRYABLE),
        HeaderValue::from_static(if retryable { "true" } else { "false" }),
    );
    headers.insert(
        HeaderName::from_static(HEADER_STARGATE_RETRY_REASON),
        HeaderValue::from_static(RETRY_REASON_LOCAL_CONNECT_FAILURE),
    );
    (
        status,
        stargate_protocol::WebTransportHttpResponseHead { status, headers },
    )
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
    fn webtransport_adapter_rejects_request_body_chunks_over_limit() {
        let error = next_body_len(7, 2, 8).expect_err("body should exceed adapter limit");

        assert!(
            error.to_string().contains("request body too large"),
            "unexpected body limit error: {error}"
        );
    }

    #[test]
    fn webtransport_success_head_keeps_retry_metadata() {
        let retry = PylonRetryConfig::default();
        let mut upstream_headers = HeaderMap::new();
        upstream_headers.insert(
            retry.upstream_retry_header.clone(),
            HeaderValue::from_static("true"),
        );
        upstream_headers.insert(reqwest::header::RETRY_AFTER, HeaderValue::from_static("2"));

        let head = webtransport_success_head(
            reqwest::StatusCode::TOO_MANY_REQUESTS,
            &upstream_headers,
            &retry,
            None,
            "inst-a",
        )
        .expect("WebTransport success head should build");

        assert_eq!(head.status, reqwest::StatusCode::TOO_MANY_REQUESTS);
        assert_eq!(
            header_value(&head.headers, HEADER_STARGATE_RETRYABLE),
            "true"
        );
        assert_eq!(
            header_value(&head.headers, HEADER_STARGATE_RETRY_REASON),
            "upstream_admission_rejected"
        );
        assert_eq!(
            header_value(&head.headers, "x-stargate-retry-after-ms"),
            "2000"
        );
    }

    #[test]
    fn webtransport_error_head_uses_problem_json_status() {
        let head = webtransport_error_head(reqwest::StatusCode::BAD_REQUEST);

        assert_eq!(head.status, reqwest::StatusCode::BAD_REQUEST);
        assert_eq!(
            header_value(&head.headers, reqwest::header::CONTENT_TYPE.as_str()),
            "application/problem+json"
        );
    }

    #[test]
    fn webtransport_queue_mismatch_head_sets_retry_metadata() {
        let app = test_app();
        let decision = QueueAdmissionDecision::Rejected {
            expected_ms: 10,
            actual_ms: 55,
            threshold_ms: 25,
            retry_after_ms: Some(7),
        };

        let head = webtransport_queue_mismatch_head(&app, &decision)
            .expect("queue mismatch head should build");

        assert_eq!(head.status, reqwest::StatusCode::TOO_MANY_REQUESTS);
        assert_eq!(
            header_value(&head.headers, HEADER_STARGATE_RETRYABLE),
            "true"
        );
        assert_eq!(
            header_value(&head.headers, HEADER_STARGATE_RETRY_REASON),
            "queue_estimate_mismatch"
        );
        assert_eq!(
            header_value(&head.headers, "x-stargate-retry-after-ms"),
            "7"
        );
    }

    #[test]
    fn webtransport_local_connect_failure_head_encodes_retryability() {
        let app = test_app();
        let error = UpstreamRequestError::Build(anyhow::anyhow!("cannot build"));

        let (status, head) = webtransport_local_connect_failure_head(&app, &error, true);

        assert_eq!(status, reqwest::StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(head.status, reqwest::StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(
            header_value(&head.headers, HEADER_STARGATE_RETRYABLE),
            "true"
        );
        assert_eq!(
            header_value(&head.headers, HEADER_STARGATE_RETRY_REASON),
            RETRY_REASON_LOCAL_CONNECT_FAILURE
        );
    }
}

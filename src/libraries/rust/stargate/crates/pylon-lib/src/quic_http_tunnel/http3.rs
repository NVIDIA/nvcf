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
use reqwest::header::HeaderMap;
use tokio_util::{sync::CancellationToken, task::TaskTracker};

use super::core::{
    ResponseBodyEventSink, TunnelRequestParts, TunnelRequestTransport, TunnelServerApp,
    extend_body_from_buf, forward_tunnel_request, next_body_len, request_body_buffer,
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

    async fn send_response_head(
        &mut self,
        status: reqwest::StatusCode,
        headers: HeaderMap,
    ) -> Result<()> {
        self.stream
            .send_response(h3_response(status, headers)?)
            .await
            .map_err(h3_error)
            .context("failed to send h3 response headers")
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

fn h3_response(status: reqwest::StatusCode, headers: HeaderMap) -> Result<http::Response<()>> {
    let mut response = http::Response::builder()
        .status(status.as_u16())
        .body(())
        .context("build h3 response")?;
    *response.headers_mut() = headers;
    Ok(response)
}

pub(super) fn h3_error<E>(error: E) -> anyhow::Error
where
    E: std::error::Error + Send + Sync + 'static,
{
    anyhow::Error::new(error)
}

#[cfg(test)]
mod tests {
    use super::*;
    use reqwest::header::HeaderValue;
    use stargate_protocol::tunnel_contract::{
        HEADER_STARGATE_RETRY_REASON, HEADER_STARGATE_RETRYABLE,
    };

    use super::super::core::{PylonRetryConfig, build_response_headers};

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

        let status = reqwest::StatusCode::TOO_MANY_REQUESTS;
        let headers = build_response_headers(status, &upstream_headers, &retry, None, "inst-a")
            .expect("h3 success headers should build");
        let response = h3_response(status, headers).expect("h3 success response should build");

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
}

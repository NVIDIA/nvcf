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
use std::time::{Duration, Instant};

use anyhow::{Context, Result, anyhow};
use axum::body::Body;
use axum::http::{HeaderMap, StatusCode};
use bytes::Buf;
use futures::StreamExt;
use tracing::{error, warn};

use stargate_protocol::{RecvStream, SendStream, WebTransportHttpResponseHead};

use super::StreamingResponse;
use super::http3::{
    H3ClientRequestRecvStream, H3ClientRequestSendStream, H3ClientRequestStream,
    Http3ConnectionHandle, h3_error,
};
use super::webtransport::WebTransportConnectionHandle;

pub struct StreamingBody {
    inner: StreamingBodyInner,
    request_body_send_task: Option<RequestBodySendTask>,
}

enum StreamingBodyInner {
    Custom {
        recv_stream: RecvStream,
    },
    Http3 {
        stream: Box<H3ClientRequestRecvStream>,
        _connection_handle: Http3ConnectionHandle,
    },
    WebTransport {
        recv_stream: quinn::RecvStream,
        _connection_handle: WebTransportConnectionHandle,
    },
}

pub(super) struct RequestBodySendTask {
    label: &'static str,
    completion_timeout: Duration,
    handle: Option<tokio::task::JoinHandle<Result<()>>>,
}

impl RequestBodySendTask {
    pub(super) fn new(
        label: &'static str,
        completion_timeout: Duration,
        handle: tokio::task::JoinHandle<Result<()>>,
    ) -> Self {
        Self {
            label,
            completion_timeout,
            handle: Some(handle),
        }
    }

    pub(super) async fn finish(mut self) -> Result<()> {
        let Some(handle) = self.handle.take() else {
            return Ok(());
        };
        let mut handle = AbortOnDropRequestBodySendHandle::new(handle);

        match tokio::time::timeout(self.completion_timeout, handle.join()).await {
            Ok(result) => {
                handle.disarm();
                finish_request_body_send_result(self.label, result)
            }
            Err(_) => {
                warn!(
                    task = self.label,
                    timeout_ms = self.completion_timeout.as_millis(),
                    "request body send task did not finish before response EOF timeout"
                );
                // The upstream response is already complete; abort the upload
                // producer so response finalization cannot stall forever.
                handle.abort();
                let result = handle.join().await;
                handle.disarm();
                finish_timed_out_request_body_send(self.label, result)
            }
        }
    }

    fn abort(&mut self) {
        if let Some(handle) = self.handle.take() {
            handle.abort();
        }
    }
}

struct AbortOnDropRequestBodySendHandle {
    handle: Option<tokio::task::JoinHandle<Result<()>>>,
}

impl AbortOnDropRequestBodySendHandle {
    fn new(handle: tokio::task::JoinHandle<Result<()>>) -> Self {
        Self {
            handle: Some(handle),
        }
    }

    fn abort(&self) {
        if let Some(handle) = &self.handle {
            handle.abort();
        }
    }

    async fn join(&mut self) -> std::result::Result<Result<()>, tokio::task::JoinError> {
        self.handle
            .as_mut()
            .expect("request body send handle should not be disarmed before join")
            .await
    }

    fn disarm(&mut self) {
        let _completed = self.handle.take();
    }
}

impl Drop for AbortOnDropRequestBodySendHandle {
    fn drop(&mut self) {
        if let Some(handle) = &self.handle {
            // Response EOF finalization can be cancelled by downstream disconnect;
            // abort before dropping the handle so the upload task is not detached.
            handle.abort();
        }
    }
}

fn finish_request_body_send_result(
    label: &'static str,
    result: std::result::Result<Result<()>, tokio::task::JoinError>,
) -> Result<()> {
    match result.with_context(|| format!("failed to join {label} send task"))? {
        Ok(()) => Ok(()),
        Err(error) => Err(error.context(format!("failed to send {label}"))),
    }
}

fn finish_timed_out_request_body_send(
    label: &'static str,
    result: std::result::Result<Result<()>, tokio::task::JoinError>,
) -> Result<()> {
    match result {
        Ok(result) => match result {
            Ok(()) => Ok(()),
            Err(error) => Err(error.context(format!("failed to send {label}"))),
        },
        Err(error) if error.is_cancelled() => Ok(()),
        Err(error) if error.is_panic() => std::panic::resume_unwind(error.into_panic()),
        Err(error) => Err(error).with_context(|| format!("failed to join {label} send task")),
    }
}

impl Drop for RequestBodySendTask {
    fn drop(&mut self) {
        // If callers drop the response before EOF, stop the producer so it
        // cannot keep reading user body bytes after the response is abandoned.
        self.abort();
    }
}

#[derive(Clone, Copy)]
enum ResponseHeadRaceBias {
    // Custom QUIC can see a peer reset and body-producer error become ready
    // together; prefer the local body error because it is more actionable.
    SendFirst,
    // HTTP/3 and WebTransport can receive an early server response while the
    // upload is still active; preserve that response before upload errors.
    ResponseFirst,
}

struct ResponseHeadRaceConfig {
    upload_label: &'static str,
    upload_panic_context: &'static str,
    upload_error_context: &'static str,
    pub(super) response_header_timeout: Duration,
    bias: ResponseHeadRaceBias,
}

struct ResponseHeadRaceOutcome<Head> {
    head: Head,
    send_done: bool,
    send_task: tokio::task::JoinHandle<Result<()>>,
    upload_label: &'static str,
    pub(super) response_header_timeout: Duration,
}

impl<Head> ResponseHeadRaceOutcome<Head> {
    fn request_body_send_task_if_success(
        self,
        status: StatusCode,
    ) -> (Head, Option<RequestBodySendTask>) {
        let Self {
            head,
            send_done,
            send_task,
            upload_label,
            response_header_timeout,
        } = self;
        let request_body_send_task = if status.is_success() && !send_done {
            Some(RequestBodySendTask::new(
                upload_label,
                response_header_timeout,
                send_task,
            ))
        } else {
            if !send_done {
                send_task.abort();
            }
            None
        };
        (head, request_body_send_task)
    }
}

async fn race_request_body_and_response_head<Head, SendBodyFuture, RecvHeadFuture>(
    config: ResponseHeadRaceConfig,
    body: Body,
    send_body: impl FnOnce(Body) -> SendBodyFuture + Send + 'static,
    recv_head: impl FnOnce(tokio::time::Instant) -> RecvHeadFuture,
) -> Result<ResponseHeadRaceOutcome<Head>>
where
    SendBodyFuture: Future<Output = Result<()>> + Send + 'static,
    RecvHeadFuture: Future<Output = Result<Head>> + Send,
{
    let response_header_deadline = tokio::time::Instant::now() + config.response_header_timeout;
    let upload_label = config.upload_label;
    let response_header_timeout = config.response_header_timeout;
    let mut send_task = tokio::spawn(async move {
        let result = send_body(body).await;
        if let Err(error) = &result {
            error!(error = %error, upload_label, "failed to send request body");
        }
        result
    });
    let mut send_done = false;
    let response_head = recv_head(response_header_deadline);
    tokio::pin!(response_head);

    let head = match config.bias {
        ResponseHeadRaceBias::SendFirst => {
            tokio::select! {
                biased;
                send_result = &mut send_task => {
                    send_done = true;
                    match send_result.context(config.upload_panic_context)? {
                        Ok(()) => response_head.await?,
                        Err(error) => return Err(error.context(config.upload_error_context)),
                    }
                },
                response_head = &mut response_head => match response_head {
                    Ok(response_head) => response_head,
                    Err(error) => {
                        send_task.abort();
                        return Err(error);
                    }
                },
            }
        }
        ResponseHeadRaceBias::ResponseFirst => {
            tokio::select! {
                biased;
                response_head = &mut response_head => match response_head {
                    Ok(response_head) => response_head,
                    Err(error) => {
                        send_task.abort();
                        return Err(error);
                    }
                },
                send_result = &mut send_task => {
                    send_done = true;
                    match send_result.context(config.upload_panic_context)? {
                        Ok(()) => response_head.await?,
                        Err(error) => return Err(error.context(config.upload_error_context)),
                    }
                },
            }
        }
    };

    Ok(ResponseHeadRaceOutcome {
        head,
        send_done,
        send_task,
        upload_label,
        response_header_timeout,
    })
}

impl StreamingBody {
    fn custom(
        recv_stream: RecvStream,
        request_body_send_task: Option<RequestBodySendTask>,
    ) -> Self {
        Self {
            inner: StreamingBodyInner::Custom { recv_stream },
            request_body_send_task,
        }
    }

    fn http3(
        stream: Box<H3ClientRequestRecvStream>,
        connection_handle: Http3ConnectionHandle,
        request_body_send_task: Option<RequestBodySendTask>,
    ) -> Self {
        Self {
            inner: StreamingBodyInner::Http3 {
                stream,
                _connection_handle: connection_handle,
            },
            request_body_send_task,
        }
    }

    fn webtransport(
        recv_stream: quinn::RecvStream,
        connection_handle: WebTransportConnectionHandle,
        request_body_send_task: Option<RequestBodySendTask>,
    ) -> Self {
        Self {
            inner: StreamingBodyInner::WebTransport {
                recv_stream,
                _connection_handle: connection_handle,
            },
            request_body_send_task,
        }
    }

    pub async fn recv_body(&mut self) -> Result<Option<bytes::Bytes>> {
        let next_chunk = match &mut self.inner {
            StreamingBodyInner::Custom { recv_stream } => recv_stream
                .recv_body()
                .await
                .context("failed to receive custom tunnel response body")
                .map(|frame| frame.into_body()),
            StreamingBodyInner::Http3 { stream, .. } => {
                match stream
                    .recv_data()
                    .await
                    .map_err(h3_error)
                    .context("failed to receive h3 response body")?
                {
                    Some(mut chunk) => {
                        let len = chunk.remaining();
                        Ok(Some(chunk.copy_to_bytes(len)))
                    }
                    None => Ok(None),
                }
            }
            StreamingBodyInner::WebTransport { recv_stream, .. } => {
                stargate_protocol::read_webtransport_http_body_chunk(recv_stream)
                    .await
                    .context("failed to receive WebTransport response body")
            }
        };

        match next_chunk {
            Ok(Some(chunk)) => Ok(Some(chunk)),
            Ok(None) => {
                self.finish_request_body_send().await?;
                Ok(None)
            }
            Err(error) => {
                self.abort_request_body_send();
                Err(error)
            }
        }
    }

    async fn finish_request_body_send(&mut self) -> Result<()> {
        if let Some(task) = self.request_body_send_task.take() {
            task.finish().await?;
        }
        Ok(())
    }

    fn abort_request_body_send(&mut self) {
        if let Some(mut task) = self.request_body_send_task.take() {
            task.abort();
        }
    }
}

pub struct OpenStreamingRequest {
    pub(super) inner: OpenStreamingRequestInner,
    pub(super) response_header_timeout: Duration,
}

pub(super) enum OpenStreamingRequestInner {
    Custom {
        send_stream: SendStream,
        recv_stream: RecvStream,
    },
    Http3 {
        stream: Box<H3ClientRequestStream>,
        connection_handle: Http3ConnectionHandle,
    },
    WebTransport {
        send_stream: quinn::SendStream,
        recv_stream: quinn::RecvStream,
        connection_handle: WebTransportConnectionHandle,
    },
}

impl OpenStreamingRequest {
    pub async fn send_body_and_recv_response(self, body: Body) -> Result<StreamingResponse> {
        let Self {
            inner,
            response_header_timeout,
        } = self;

        match inner {
            OpenStreamingRequestInner::Custom {
                send_stream,
                recv_stream,
            } => {
                Self::send_custom_body_and_recv_response(
                    send_stream,
                    recv_stream,
                    response_header_timeout,
                    body,
                )
                .await
            }
            OpenStreamingRequestInner::Http3 {
                stream,
                connection_handle,
            } => {
                Self::send_h3_body_and_recv_response(
                    stream,
                    response_header_timeout,
                    body,
                    connection_handle,
                )
                .await
            }
            OpenStreamingRequestInner::WebTransport {
                send_stream,
                recv_stream,
                connection_handle,
            } => {
                Self::send_webtransport_body_and_recv_response(
                    send_stream,
                    recv_stream,
                    response_header_timeout,
                    body,
                    connection_handle,
                )
                .await
            }
        }
    }

    async fn send_custom_body_and_recv_response(
        send_stream: SendStream,
        mut recv_stream: RecvStream,
        response_header_timeout: Duration,
        body: Body,
    ) -> Result<StreamingResponse> {
        let race = race_request_body_and_response_head(
            ResponseHeadRaceConfig {
                upload_label: "custom request body",
                upload_panic_context: "custom request body send task panicked",
                upload_error_context: "failed to send custom request body",
                response_header_timeout,
                bias: ResponseHeadRaceBias::SendFirst,
            },
            body,
            |body| send_custom_request_body(send_stream, body),
            |deadline| recv_custom_response_headers_until(deadline, &mut recv_stream),
        )
        .await?;

        let status_code = match race
            .head
            .get("x-status")
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.parse::<u16>().ok())
        {
            Some(status_code) => status_code,
            None => {
                warn!(
                    response_headers = ?race.head,
                    "custom tunnel response missing or invalid x-status header"
                );
                502
            }
        };
        let status = StatusCode::from_u16(status_code).unwrap_or(StatusCode::BAD_GATEWAY);
        let (response_headers, request_body_send_task) =
            race.request_body_send_task_if_success(status);

        let mut clean_headers = HeaderMap::new();
        for (name, value) in &response_headers {
            if name.as_str() != "x-status" {
                clean_headers.append(name, value.clone());
            }
        }

        Ok(StreamingResponse {
            status,
            headers: clean_headers,
            body_stream: StreamingBody::custom(recv_stream, request_body_send_task),
        })
    }

    async fn send_webtransport_body_and_recv_response(
        send_stream: quinn::SendStream,
        mut recv_stream: quinn::RecvStream,
        response_header_timeout: Duration,
        body: Body,
        connection_handle: WebTransportConnectionHandle,
    ) -> Result<StreamingResponse> {
        let race = race_request_body_and_response_head(
            ResponseHeadRaceConfig {
                upload_label: "WebTransport request body",
                upload_panic_context: "WebTransport request body send task panicked",
                upload_error_context: "failed to send WebTransport request body",
                response_header_timeout,
                bias: ResponseHeadRaceBias::ResponseFirst,
            },
            body,
            |body| send_webtransport_request_body(send_stream, body),
            |deadline| recv_webtransport_response_head_until(deadline, &mut recv_stream),
        )
        .await?;
        let status = race.head.status;
        let (response_head, request_body_send_task) =
            race.request_body_send_task_if_success(status);

        Ok(StreamingResponse {
            status: response_head.status,
            headers: response_head.headers,
            body_stream: StreamingBody::webtransport(
                recv_stream,
                connection_handle,
                request_body_send_task,
            ),
        })
    }

    async fn send_h3_body_and_recv_response(
        stream: Box<H3ClientRequestStream>,
        response_header_timeout: Duration,
        body: Body,
        connection_handle: Http3ConnectionHandle,
    ) -> Result<StreamingResponse> {
        let (mut send_stream, mut recv_stream) = stream.split();
        let race = race_request_body_and_response_head(
            ResponseHeadRaceConfig {
                upload_label: "h3 request body",
                upload_panic_context: "h3 request body send task panicked",
                upload_error_context: "failed to send h3 request body",
                response_header_timeout,
                bias: ResponseHeadRaceBias::ResponseFirst,
            },
            body,
            move |body| async move { send_h3_request_body(&mut send_stream, body).await },
            |deadline| recv_h3_response_until(deadline, &mut recv_stream),
        )
        .await?;
        let status =
            StatusCode::from_u16(race.head.status().as_u16()).unwrap_or(StatusCode::BAD_GATEWAY);
        let (response, request_body_send_task) = race.request_body_send_task_if_success(status);
        Ok(StreamingResponse {
            status,
            headers: response.headers().clone(),
            body_stream: StreamingBody::http3(
                Box::new(recv_stream),
                connection_handle,
                request_body_send_task,
            ),
        })
    }
}

async fn send_custom_request_body(mut send_stream: SendStream, body: Body) -> Result<()> {
    let mut body_stream = body.into_data_stream();
    while let Some(chunk_result) = body_stream.next().await {
        let chunk = chunk_result.context("failed to read request body chunk")?;
        send_stream
            .send_body(chunk)
            .await
            .context("failed to send request body chunk")?;
    }
    send_stream.finish().context("failed to finish send stream")
}

async fn send_webtransport_request_body(
    mut send_stream: quinn::SendStream,
    body: Body,
) -> Result<()> {
    let mut body_stream = body.into_data_stream();
    while let Some(chunk_result) = body_stream.next().await {
        let chunk = chunk_result.context("failed to read request body chunk")?;
        stargate_protocol::write_webtransport_http_body(&mut send_stream, chunk)
            .await
            .context("failed to send WebTransport request body chunk")?;
    }
    stargate_protocol::finish_webtransport_http_stream(&mut send_stream)
        .context("failed to finish WebTransport request stream")
}

async fn recv_custom_response_headers_until(
    deadline: tokio::time::Instant,
    recv_stream: &mut RecvStream,
) -> Result<HeaderMap> {
    tokio::time::timeout_at(deadline, recv_stream.recv_header())
        .await
        .map_err(|_| anyhow!("quic request timed out"))?
        .context("failed to receive response headers")
}

async fn recv_webtransport_response_head_until(
    deadline: tokio::time::Instant,
    recv_stream: &mut quinn::RecvStream,
) -> Result<WebTransportHttpResponseHead> {
    tokio::time::timeout_at(
        deadline,
        stargate_protocol::read_webtransport_http_response_head(recv_stream),
    )
    .await
    .map_err(|_| anyhow!("quic request timed out"))?
    .context("failed to receive WebTransport response head")
}

async fn send_h3_request_body(
    send_stream: &mut H3ClientRequestSendStream,
    body: Body,
) -> Result<()> {
    let mut body_stream = body.into_data_stream();
    while let Some(chunk_result) = body_stream.next().await {
        let chunk = chunk_result.context("failed to read request body chunk")?;
        send_stream
            .send_data(chunk)
            .await
            .map_err(h3_error)
            .context("failed to send h3 request body chunk")?;
    }
    send_stream
        .finish()
        .await
        .map_err(h3_error)
        .context("failed to finish h3 request stream")
}

async fn recv_h3_response_until(
    deadline: tokio::time::Instant,
    recv_stream: &mut H3ClientRequestRecvStream,
) -> Result<http::Response<()>> {
    tokio::time::timeout_at(deadline, recv_stream.recv_response())
        .await
        .map_err(|_| anyhow!("quic request timed out"))?
        .map_err(h3_error)
        .context("failed to receive h3 response headers")
}

pub(super) fn remaining_request_timeout(
    started_at: Instant,
    request_timeout: Duration,
) -> Duration {
    request_timeout
        .checked_sub(started_at.elapsed())
        .unwrap_or(Duration::ZERO)
}

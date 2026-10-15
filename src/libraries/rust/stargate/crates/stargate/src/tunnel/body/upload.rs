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
use std::pin::Pin;
use std::time::Duration;

use anyhow::{Context, Result};
use axum::body::Body;
use axum::http::StatusCode;
use tracing::{error, warn};

type BodySendResult = std::result::Result<Result<()>, tokio::task::JoinError>;

pub(in crate::tunnel) struct RequestBodySendTask {
    label: &'static str,
    completion_timeout: Duration,
    handle: Option<tokio::task::JoinHandle<Result<()>>>,
}

impl RequestBodySendTask {
    pub(in crate::tunnel) fn new(
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

    pub(in crate::tunnel) async fn finish(mut self) -> Result<()> {
        match tokio::time::timeout(self.completion_timeout, self.join()).await {
            Ok(result) => {
                self.disarm();
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
                self.abort();
                let result = self.join().await;
                self.disarm();
                finish_timed_out_request_body_send(self.label, result)
            }
        }
    }

    pub(super) fn abort(&self) {
        if let Some(handle) = &self.handle {
            handle.abort();
        }
    }

    async fn join(&mut self) -> BodySendResult {
        self.handle
            .as_mut()
            .expect("request body send handle should not be disarmed before join")
            .await
    }

    fn disarm(&mut self) {
        let _completed = self.handle.take();
    }
}

fn finish_request_body_send_result(label: &'static str, result: BodySendResult) -> Result<()> {
    result
        .with_context(|| format!("failed to join {label} send task"))?
        .with_context(|| format!("failed to send {label}"))
}

fn finish_timed_out_request_body_send(label: &'static str, result: BodySendResult) -> Result<()> {
    match result {
        Ok(result) => result.with_context(|| format!("failed to send {label}")),
        Err(error) if error.is_cancelled() => Ok(()),
        Err(error) if error.is_panic() => std::panic::resume_unwind(error.into_panic()),
        Err(error) => Err(error).with_context(|| format!("failed to join {label} send task")),
    }
}

impl Drop for RequestBodySendTask {
    fn drop(&mut self) {
        // This owner spans the response-head race and response-body lifetime.
        // Cancellation or abandonment must not detach the upload producer.
        self.abort();
    }
}

#[derive(Clone, Copy)]
pub(super) enum ResponseHeadRaceBias {
    // Raw QUIC can see a peer reset and body-producer error become ready
    // together; prefer the local body error because it is more actionable.
    SendFirst,
    // HTTP/3 and WebTransport can receive an early server response while the
    // upload is still active; preserve that response before upload errors.
    ResponseFirst,
}

pub(super) struct ResponseHeadRaceConfig {
    pub(super) upload_label: &'static str,
    pub(super) response_header_timeout: Duration,
    pub(super) bias: ResponseHeadRaceBias,
}

pub(super) struct ResponseHeadRaceOutcome<Head> {
    pub(super) head: Head,
    request_body_send_task: Option<RequestBodySendTask>,
}

enum ResponseHeadRaceResult<Head> {
    Head(Result<Head>),
    Send(BodySendResult),
}

impl<Head> ResponseHeadRaceOutcome<Head> {
    pub(super) fn request_body_send_task_if_success(
        self,
        status: StatusCode,
    ) -> (Head, Option<RequestBodySendTask>) {
        if status.is_success() {
            (self.head, self.request_body_send_task)
        } else {
            (self.head, None)
        }
    }
}

pub(super) async fn race_request_body_and_response_head<Head, SendBodyFuture, RecvHeadFuture>(
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
    let send_task = tokio::spawn(async move {
        let result = send_body(body).await;
        if let Err(error) = &result {
            error!(error = %error, upload_label, "failed to send request body");
        }
        result
    });
    let mut send_task = RequestBodySendTask::new(upload_label, response_header_timeout, send_task);
    let response_head = recv_head(response_header_deadline);
    tokio::pin!(response_head);

    let first = match config.bias {
        ResponseHeadRaceBias::SendFirst => tokio::select! {
            biased;
            result = send_task.join() => ResponseHeadRaceResult::Send(result),
            result = &mut response_head => ResponseHeadRaceResult::Head(result),
        },
        ResponseHeadRaceBias::ResponseFirst => tokio::select! {
            biased;
            result = &mut response_head => ResponseHeadRaceResult::Head(result),
            result = send_task.join() => ResponseHeadRaceResult::Send(result),
        },
    };
    finish_response_head_race(first, send_task, response_head.as_mut(), upload_label).await
}

async fn finish_response_head_race<Head, RecvHeadFuture>(
    first: ResponseHeadRaceResult<Head>,
    mut send_task: RequestBodySendTask,
    response_head: Pin<&mut RecvHeadFuture>,
    upload_label: &'static str,
) -> Result<ResponseHeadRaceOutcome<Head>>
where
    RecvHeadFuture: Future<Output = Result<Head>> + Send,
{
    let (head, request_body_send_task) = match first {
        ResponseHeadRaceResult::Send(send_result) => {
            let head = match send_result
                .with_context(|| format!("{upload_label} send task panicked"))?
            {
                Ok(()) => response_head.await?,
                Err(error) => return Err(error.context(format!("failed to send {upload_label}"))),
            };
            send_task.disarm();
            (head, None)
        }
        ResponseHeadRaceResult::Head(response_head) => match response_head {
            Ok(response_head) => (response_head, Some(send_task)),
            Err(error) => return Err(error),
        },
    };
    Ok(ResponseHeadRaceOutcome {
        head,
        request_body_send_task,
    })
}

#[cfg(test)]
mod tests {
    use std::future::pending;

    use tokio::sync::oneshot;

    use super::*;

    struct DropNotifier(Option<oneshot::Sender<()>>);

    impl Drop for DropNotifier {
        fn drop(&mut self) {
            if let Some(tx) = self.0.take() {
                let _ = tx.send(());
            }
        }
    }

    #[tokio::test]
    async fn dropping_response_head_race_aborts_upload() {
        let (entered_tx, mut entered_rx) = oneshot::channel();
        let (dropped_tx, dropped_rx) = oneshot::channel();
        {
            let race = race_request_body_and_response_head(
                ResponseHeadRaceConfig {
                    upload_label: "cancelled upload race",
                    response_header_timeout: Duration::from_secs(30),
                    bias: ResponseHeadRaceBias::SendFirst,
                },
                Body::empty(),
                move |_| async move {
                    let _notifier = DropNotifier(Some(dropped_tx));
                    let _ = entered_tx.send(());
                    pending::<Result<()>>().await
                },
                |_| pending::<Result<()>>(),
            );
            tokio::pin!(race);
            tokio::select! {
                _ = &mut race => panic!("pending race should not finish"),
                entered = &mut entered_rx => entered.expect("upload should start"),
            }
        }

        tokio::time::timeout(Duration::from_secs(1), dropped_rx)
            .await
            .expect("dropping the race should stop the upload")
            .expect("upload drop notifier should send");
    }
}

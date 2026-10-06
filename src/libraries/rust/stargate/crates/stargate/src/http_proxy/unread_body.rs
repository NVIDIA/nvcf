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

//! Keeps HTTP/1 connections reusable after a response sent before the request
//! body was read (admission rejections, no candidates, validation errors).
//!
//! When a handler drops an unread body, hyper has already written the response
//! head; it then tries one read and, if the body has not fully arrived, closes
//! the connection without `Connection: close`. Clients that pool connections
//! reuse the dead connection and fail their next request.

use std::pin::Pin;
use std::task::{Context, Poll, ready};
use std::time::Duration;

use axum::body::{Body, BodyDataStream, Bytes};
use axum::http::{HeaderValue, header};
use axum::response::Response;
use futures::{Stream, StreamExt};
use tokio::sync::oneshot;
use tokio::sync::oneshot::error::TryRecvError;
use tokio::time::Instant;

/// Unread body bytes to discard to keep the connection; matches Go net/http's
/// server limit. Above it the response carries `Connection: close`.
const MAX_DISCARD_BYTES: usize = 256 * 1024;
/// Bounds the wait for a slow client before giving up on the connection.
const MAX_DISCARD_WAIT: Duration = Duration::from_secs(1);

/// The part of a request body that the handler dropped without reading.
pub(super) struct UnreadRequestBody {
    reclaimed: oneshot::Receiver<BodyDataStream>,
}

/// Wraps `body` so that the unread rest returns to the caller when the handler
/// drops it before end of stream.
pub(super) fn track_unread_body(body: Body) -> (Body, UnreadRequestBody) {
    let (reclaim, reclaimed) = oneshot::channel();
    let stream = ReclaimingStream {
        inner: Some((body.into_data_stream(), reclaim)),
    };
    (Body::from_stream(stream), UnreadRequestBody { reclaimed })
}

impl UnreadRequestBody {
    /// Discards the unread body before `response` is sent, or marks `response`
    /// to close the connection when the rest is too large or too slow.
    pub(super) async fn discard_or_close(mut self, response: &mut Response<Body>) {
        let deadline = Instant::now() + MAX_DISCARD_WAIT;
        let is_connection_reusable = match self.reclaimed.try_recv() {
            Ok(stream) => discard(stream, deadline).await,
            Err(TryRecvError::Closed) => true,
            // A successful upstream response keeps the upload task, which reads the body to the end.
            Err(TryRecvError::Empty) if response.status().is_success() => true,
            // A failed upstream response aborts the upload task; its body comes back when the task drops.
            Err(TryRecvError::Empty) => {
                match tokio::time::timeout_at(deadline, self.reclaimed).await {
                    Ok(Ok(stream)) => discard(stream, deadline).await,
                    Ok(Err(_)) => true,
                    Err(_) => false,
                }
            }
        };
        if !is_connection_reusable {
            tracing::debug!("closing connection: unread request body too large or too slow");
            response
                .headers_mut()
                .insert(header::CONNECTION, HeaderValue::from_static("close"));
        }
    }
}

async fn discard(mut stream: BodyDataStream, deadline: Instant) -> bool {
    let mut discarded = 0usize;
    let read_to_end = async {
        while let Some(chunk) = stream.next().await {
            let Ok(chunk) = chunk else {
                return false;
            };
            discarded += chunk.len();
            if discarded > MAX_DISCARD_BYTES {
                return false;
            }
        }
        true
    };
    tokio::time::timeout_at(deadline, read_to_end)
        .await
        .unwrap_or(false)
}

struct ReclaimingStream {
    /// `None` once the body ended or failed: nothing is left to discard.
    inner: Option<(BodyDataStream, oneshot::Sender<BodyDataStream>)>,
}

impl Stream for ReclaimingStream {
    type Item = Result<Bytes, axum::Error>;

    fn poll_next(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        let Some((stream, _)) = self.inner.as_mut() else {
            return Poll::Ready(None);
        };
        let item = ready!(stream.poll_next_unpin(cx));
        if !matches!(item, Some(Ok(_))) {
            self.inner = None;
        }
        Poll::Ready(item)
    }
}

impl Drop for ReclaimingStream {
    fn drop(&mut self) {
        if let Some((stream, reclaim)) = self.inner.take() {
            // Fails only after the response is ready; hyper then handles the rest itself.
            let _ = reclaim.send(stream);
        }
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;
    use std::sync::atomic::{AtomicBool, Ordering};

    use axum::http::StatusCode;

    use super::*;

    #[tokio::test]
    async fn dropped_unread_body_is_discarded_or_closes_connection() {
        let stalled =
            || Body::from_stream(futures::stream::pending::<Result<Bytes, std::io::Error>>());
        let cases = [
            ("small body", Body::from("unread"), None),
            (
                "oversized body",
                Body::from(vec![0u8; MAX_DISCARD_BYTES + 1]),
                Some("close"),
            ),
            ("stalled body", stalled(), Some("close")),
        ];
        for (label, request_body, expected_connection) in cases {
            let (body, unread) = track_unread_body(request_body);
            drop(body);
            let mut response = Response::new(Body::empty());

            unread.discard_or_close(&mut response).await;

            assert_eq!(connection_header(&response), expected_connection, "{label}");
        }
    }

    #[tokio::test]
    async fn body_held_by_upload_task_is_discarded_once_released() {
        let cases = [
            // An aborted upload task drops the body after the response is ready.
            (
                "released after failed response",
                StatusCode::BAD_GATEWAY,
                Some(Duration::from_millis(50)),
                true,
                None,
            ),
            // A running upload task reads the body itself.
            (
                "kept after successful response",
                StatusCode::OK,
                None,
                false,
                None,
            ),
            (
                "never released after failed response",
                StatusCode::BAD_GATEWAY,
                None,
                false,
                Some("close"),
            ),
        ];
        for (label, status, release_after, expected_discarded, expected_connection) in cases {
            let is_read_to_end = Arc::new(AtomicBool::new(false));
            let read_to_end = Arc::clone(&is_read_to_end);
            let request_body = Body::from_stream(async_stream::stream! {
                yield Ok::<_, std::io::Error>(Bytes::from_static(b"unread"));
                read_to_end.store(true, Ordering::SeqCst);
            });
            let (body, unread) = track_unread_body(request_body);
            let upload = tokio::spawn(async move {
                match release_after {
                    Some(delay) => tokio::time::sleep(delay).await,
                    None => std::future::pending().await,
                }
                drop(body);
            });
            let mut response = Response::new(Body::empty());
            *response.status_mut() = status;

            unread.discard_or_close(&mut response).await;
            upload.abort();

            assert_eq!(
                is_read_to_end.load(Ordering::SeqCst),
                expected_discarded,
                "{label}"
            );
            assert_eq!(connection_header(&response), expected_connection, "{label}");
        }
    }

    #[tokio::test]
    async fn fully_read_body_leaves_nothing_to_discard() {
        let (body, mut unread) = track_unread_body(Body::from("read"));
        let bytes = axum::body::to_bytes(body, 1024).await.unwrap();
        assert_eq!(bytes, Bytes::from_static(b"read"));

        assert!(matches!(
            unread.reclaimed.try_recv(),
            Err(TryRecvError::Closed)
        ));
    }

    fn connection_header(response: &Response<Body>) -> Option<&str> {
        response
            .headers()
            .get(header::CONNECTION)
            .map(|value| value.to_str().unwrap())
    }
}

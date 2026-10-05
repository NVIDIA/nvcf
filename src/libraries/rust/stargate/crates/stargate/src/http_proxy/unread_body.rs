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
use std::sync::Arc;
use std::task::{Context, Poll, ready};
use std::time::Duration;

use axum::body::{Body, BodyDataStream, Bytes};
use axum::http::{HeaderValue, header};
use axum::response::Response;
use futures::{Stream, StreamExt};
use parking_lot::Mutex;

/// Unread body bytes to discard to keep the connection; matches Go net/http's
/// server limit. Above it the response carries `Connection: close`.
const MAX_DISCARD_BYTES: usize = 256 * 1024;
/// Bounds the wait for a slow client before giving up on the connection.
const MAX_DISCARD_WAIT: Duration = Duration::from_secs(1);

type UnreadSlot = Arc<Mutex<Option<BodyDataStream>>>;

/// The part of a request body that the handler dropped without reading.
pub(super) struct UnreadRequestBody {
    slot: UnreadSlot,
}

/// Wraps `body` so that the unread rest returns to the caller when the handler
/// drops it before end of stream.
pub(super) fn track_unread_body(body: Body) -> (Body, UnreadRequestBody) {
    let slot = UnreadSlot::default();
    let stream = ReclaimingStream {
        stream: Some(body.into_data_stream()),
        slot: Arc::clone(&slot),
    };
    (Body::from_stream(stream), UnreadRequestBody { slot })
}

impl UnreadRequestBody {
    /// Discards the unread body before `response` is sent, or marks `response`
    /// to close the connection when the rest is too large or too slow.
    pub(super) async fn discard_or_close(self, response: &mut Response<Body>) {
        let Some(stream) = self.slot.lock().take() else {
            return;
        };
        if !discard(stream).await {
            tracing::debug!("closing connection: unread request body too large or too slow");
            response
                .headers_mut()
                .insert(header::CONNECTION, HeaderValue::from_static("close"));
        }
    }
}

async fn discard(mut stream: BodyDataStream) -> bool {
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
    tokio::time::timeout(MAX_DISCARD_WAIT, read_to_end)
        .await
        .unwrap_or(false)
}

struct ReclaimingStream {
    /// `None` once the body ended or failed: nothing is left to discard.
    stream: Option<BodyDataStream>,
    slot: UnreadSlot,
}

impl Stream for ReclaimingStream {
    type Item = Result<Bytes, axum::Error>;

    fn poll_next(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        let Some(stream) = self.stream.as_mut() else {
            return Poll::Ready(None);
        };
        let item = ready!(stream.poll_next_unpin(cx));
        if !matches!(item, Some(Ok(_))) {
            self.stream = None;
        }
        Poll::Ready(item)
    }
}

impl Drop for ReclaimingStream {
    fn drop(&mut self) {
        if let Some(stream) = self.stream.take() {
            *self.slot.lock() = Some(stream);
        }
    }
}

#[cfg(test)]
mod tests {
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

            assert_eq!(
                response
                    .headers()
                    .get(header::CONNECTION)
                    .map(|value| value.to_str().unwrap()),
                expected_connection,
                "{label}"
            );
        }
    }

    #[tokio::test]
    async fn fully_read_body_leaves_nothing_to_discard() {
        let (body, unread) = track_unread_body(Body::from("read"));
        let bytes = axum::body::to_bytes(body, 1024).await.unwrap();
        assert_eq!(bytes, Bytes::from_static(b"read"));

        assert!(unread.slot.lock().is_none());
    }
}

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

//! Announces the close of HTTP/1 connections after a response sent before the
//! request body was read (admission rejections, no candidates, validation
//! errors, upstream rejections during the upload).
//!
//! When a handler drops an unread body, hyper has already written the response
//! head; it then tries one read and, if the body has not fully arrived, closes
//! the connection without `Connection: close`. Clients that pool connections
//! reuse the dead connection and fail their next request.

use std::pin::Pin;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::task::{Context, Poll, ready};

use axum::body::{Body, BodyDataStream, Bytes, HttpBody};
use axum::http::{HeaderValue, header};
use axum::response::Response;
use futures::{Stream, StreamExt};

/// Records whether the request body was read to its end.
pub(super) struct UnreadRequestBody {
    is_read_to_end: Arc<AtomicBool>,
}

/// Wraps `body` so that the caller can tell whether it was read to its end.
pub(super) fn track_unread_body(body: Body) -> (Body, UnreadRequestBody) {
    let is_read_to_end = Arc::new(AtomicBool::new(body.is_end_stream()));
    let stream = EndTrackingStream {
        stream: body.into_data_stream(),
        is_read_to_end: Arc::clone(&is_read_to_end),
    };
    (
        Body::from_stream(stream),
        UnreadRequestBody { is_read_to_end },
    )
}

impl UnreadRequestBody {
    /// Marks a failed `response` to close the connection when the request body
    /// was not read to its end.
    pub(super) fn close_if_unread(&self, response: &mut Response<Body>) {
        // A successful upstream response keeps the upload task, which reads the body to the end.
        if response.status().is_success() || self.is_read_to_end.load(Ordering::Acquire) {
            return;
        }
        tracing::debug!("closing connection: request body not read to its end");
        response
            .headers_mut()
            .insert(header::CONNECTION, HeaderValue::from_static("close"));
    }
}

struct EndTrackingStream {
    stream: BodyDataStream,
    is_read_to_end: Arc<AtomicBool>,
}

impl Stream for EndTrackingStream {
    type Item = Result<Bytes, axum::Error>;

    fn poll_next(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        let item = ready!(self.stream.poll_next_unpin(cx));
        if item.is_none() {
            self.is_read_to_end.store(true, Ordering::Release);
        }
        Poll::Ready(item)
    }
}

#[cfg(test)]
mod tests {
    use axum::http::StatusCode;

    use super::*;

    #[tokio::test]
    async fn failed_response_closes_connection_when_body_was_not_read() {
        let cases = [
            // The upload task or a handler consumed the body.
            (
                "read to the end",
                Body::from("read"),
                true,
                StatusCode::NOT_FOUND,
                None,
            ),
            (
                "dropped unread",
                Body::from("unread"),
                false,
                StatusCode::NOT_FOUND,
                Some("close"),
            ),
            // The upload task keeps reading the body after a successful response.
            (
                "unread on success",
                Body::from("unread"),
                false,
                StatusCode::OK,
                None,
            ),
            (
                "empty body",
                Body::empty(),
                false,
                StatusCode::NOT_FOUND,
                None,
            ),
        ];
        for (label, request_body, is_read, status, expected_connection) in cases {
            let (body, unread) = track_unread_body(request_body);
            if is_read {
                axum::body::to_bytes(body, 1024).await.unwrap();
            } else {
                drop(body);
            }
            let mut response = Response::new(Body::empty());
            *response.status_mut() = status;

            unread.close_if_unread(&mut response);

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
}

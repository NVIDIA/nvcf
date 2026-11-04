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

use std::sync::Arc;

use axum::body::{Body, Bytes};
use axum::http::{HeaderMap, StatusCode};
use futures::StreamExt;
use parking_lot::Mutex;

pub(in crate::http_proxy) struct ReplayableRequestBody {
    first_body: Option<Body>,
    buffer: Arc<ReplayBuffer>,
}

struct ReplayBuffer {
    state: Mutex<ReplayBufferState>,
    max_bytes: usize,
}

struct ReplayBufferingGuard {
    buffer: Arc<ReplayBuffer>,
}

enum ReplayBufferState {
    Buffering(Vec<u8>),
    Complete(Bytes),
    Unavailable,
    PayloadTooLarge,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(in crate::http_proxy) enum ReplayReadiness {
    Ready,
    Incomplete,
    PayloadTooLarge,
}

impl ReplayableRequestBody {
    pub(in crate::http_proxy) fn new(
        headers: &HeaderMap,
        body: Body,
        max_bytes: usize,
    ) -> Result<Self, StatusCode> {
        if let Some(content_length) = headers
            .get(axum::http::header::CONTENT_LENGTH)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.parse::<usize>().ok())
            && content_length > max_bytes
        {
            return Err(StatusCode::PAYLOAD_TOO_LARGE);
        }

        Ok(Self {
            first_body: Some(body),
            buffer: Arc::new(ReplayBuffer {
                state: Mutex::new(ReplayBufferState::Buffering(Vec::new())),
                max_bytes,
            }),
        })
    }

    pub(in crate::http_proxy) fn body_for_attempt(&mut self) -> Result<Body, StatusCode> {
        if let Some(body) = self.first_body.take() {
            return Ok(self.buffering_body(body));
        }
        self.buffer.replay_body()
    }

    pub(in crate::http_proxy) fn buffered_len(&self) -> usize {
        match &*self.buffer.state.lock() {
            ReplayBufferState::Buffering(buffer) => buffer.len(),
            ReplayBufferState::Complete(bytes) => bytes.len(),
            ReplayBufferState::Unavailable | ReplayBufferState::PayloadTooLarge => 0,
        }
    }

    pub(in crate::http_proxy) fn replay_readiness(&self) -> ReplayReadiness {
        if self.first_body.is_some() {
            ReplayReadiness::Ready
        } else {
            match &*self.buffer.state.lock() {
                ReplayBufferState::Buffering(_) | ReplayBufferState::Unavailable => {
                    ReplayReadiness::Incomplete
                }
                ReplayBufferState::Complete(_) => ReplayReadiness::Ready,
                ReplayBufferState::PayloadTooLarge => ReplayReadiness::PayloadTooLarge,
            }
        }
    }

    fn buffering_body(&self, body: Body) -> Body {
        let buffering = ReplayBufferingGuard {
            buffer: Arc::clone(&self.buffer),
        };
        Body::from_stream(async_stream::stream! {
            let mut stream = body.into_data_stream();
            while let Some(chunk_result) = stream.next().await {
                match chunk_result {
                    Ok(chunk) => {
                        buffering.buffer.append(&chunk);
                        yield Ok::<_, std::io::Error>(chunk);
                    }
                    Err(error) => {
                        buffering.buffer.abandon();
                        yield Err(std::io::Error::other(error.to_string()));
                        return;
                    }
                }
            }
            buffering.buffer.complete();
        })
    }
}

impl ReplayBuffer {
    fn append(&self, chunk: &[u8]) {
        self.state.lock().append(chunk, self.max_bytes);
    }
    fn complete(&self) {
        self.state.lock().complete();
    }

    fn abandon(&self) {
        let mut state = self.state.lock();
        if matches!(*state, ReplayBufferState::Buffering(_)) {
            *state = ReplayBufferState::Unavailable;
        }
    }

    fn replay_body(&self) -> Result<Body, StatusCode> {
        match &*self.state.lock() {
            ReplayBufferState::Buffering(_) | ReplayBufferState::Unavailable => {
                Err(StatusCode::BAD_GATEWAY)
            }
            ReplayBufferState::Complete(bytes) => Ok(Body::from(bytes.clone())),
            ReplayBufferState::PayloadTooLarge => Err(StatusCode::PAYLOAD_TOO_LARGE),
        }
    }
}

impl ReplayBufferState {
    fn append(&mut self, chunk: &[u8], max_bytes: usize) {
        match self {
            ReplayBufferState::Buffering(buffer)
                if buffer
                    .len()
                    .checked_add(chunk.len())
                    .is_some_and(|next_len| next_len <= max_bytes) =>
            {
                buffer.extend_from_slice(chunk);
            }
            ReplayBufferState::Buffering(_) => *self = ReplayBufferState::PayloadTooLarge,
            ReplayBufferState::Complete(_) => {
                panic!("completed replay buffer received another body chunk")
            }
            ReplayBufferState::Unavailable => {
                panic!("unavailable replay buffer received another body chunk")
            }
            ReplayBufferState::PayloadTooLarge => {}
        }
    }

    fn complete(&mut self) {
        match self {
            ReplayBufferState::Buffering(buffer) => {
                let bytes = Bytes::from(std::mem::take(buffer));
                *self = ReplayBufferState::Complete(bytes);
            }
            ReplayBufferState::Complete(_) => panic!("replay buffer completed more than once"),
            ReplayBufferState::Unavailable => panic!("unavailable replay buffer completed"),
            ReplayBufferState::PayloadTooLarge => {}
        }
    }
}

impl Drop for ReplayBufferingGuard {
    fn drop(&mut self) {
        self.buffer.abandon();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    use std::time::Duration;

    use axum::body::Bytes;
    use axum::http::HeaderValue;

    #[tokio::test]
    async fn replay_body_is_incomplete_until_first_body_reaches_eof() {
        let body = Body::from_stream(async_stream::stream! {
            yield Ok::<_, std::io::Error>(Bytes::from_static(b"partial"));
            futures::future::pending::<()>().await;
        });
        let mut replay_body = ReplayableRequestBody::new(&HeaderMap::new(), body, 1024).unwrap();

        let attempt_body = replay_body.body_for_attempt().unwrap();
        let mut stream = attempt_body.into_data_stream();
        let chunk = tokio::time::timeout(Duration::from_secs(1), stream.next())
            .await
            .expect("body chunk timed out")
            .expect("missing body chunk")
            .expect("body chunk failed");

        assert_eq!(chunk, Bytes::from_static(b"partial"));
        assert_eq!(replay_body.buffered_len(), 7);
        assert_eq!(replay_body.replay_readiness(), ReplayReadiness::Incomplete);
        assert_eq!(
            replay_body.body_for_attempt().err(),
            Some(StatusCode::BAD_GATEWAY)
        );

        drop(stream);
        assert_eq!(
            replay_body.buffered_len(),
            0,
            "abandoning the first-attempt body should release non-replayable bytes"
        );
    }

    #[tokio::test]
    async fn replay_body_replays_only_after_first_body_reaches_eof() {
        let body = Body::from("complete");
        let mut replay_body = ReplayableRequestBody::new(&HeaderMap::new(), body, 1024).unwrap();

        let attempt_body = replay_body.body_for_attempt().unwrap();
        let attempt_bytes = axum::body::to_bytes(attempt_body, 1024).await.unwrap();
        assert_eq!(attempt_bytes, Bytes::from_static(b"complete"));
        assert_eq!(replay_body.replay_readiness(), ReplayReadiness::Ready);

        let replayed_body = replay_body.body_for_attempt().unwrap();
        let replayed_bytes = axum::body::to_bytes(replayed_body, 1024).await.unwrap();
        assert_eq!(replayed_bytes, Bytes::from_static(b"complete"));

        let replayed_again = replay_body.body_for_attempt().unwrap();
        let replayed_again_bytes = axum::body::to_bytes(replayed_again, 1024).await.unwrap();
        assert_eq!(replayed_again_bytes, Bytes::from_static(b"complete"));
    }

    #[tokio::test]
    async fn streamed_replay_overflow_releases_buffer_and_stays_terminal() {
        let body = Body::from_stream(async_stream::stream! {
            yield Ok::<_, std::io::Error>(Bytes::from_static(b"abc"));
            yield Ok::<_, std::io::Error>(Bytes::from_static(b"def"));
            yield Ok::<_, std::io::Error>(Bytes::from_static(b"g"));
        });
        let mut replay_body = ReplayableRequestBody::new(&HeaderMap::new(), body, 4).unwrap();

        let attempt_body = replay_body.body_for_attempt().unwrap();
        let attempt_bytes = axum::body::to_bytes(attempt_body, 1024).await.unwrap();

        assert_eq!(attempt_bytes, Bytes::from_static(b"abcdefg"));
        assert_eq!(
            replay_body.replay_readiness(),
            ReplayReadiness::PayloadTooLarge
        );
        assert_eq!(
            replay_body.buffered_len(),
            0,
            "terminal overflow should release bytes that can no longer be replayed"
        );
        assert_eq!(
            replay_body.body_for_attempt().err(),
            Some(StatusCode::PAYLOAD_TOO_LARGE)
        );
    }

    #[tokio::test]
    async fn replay_body_stream_error_remains_incomplete() {
        let body = Body::from_stream(async_stream::stream! {
            yield Ok::<_, std::io::Error>(Bytes::from_static(b"partial"));
            yield Err::<Bytes, _>(std::io::Error::other("body failed"));
        });
        let mut replay_body = ReplayableRequestBody::new(&HeaderMap::new(), body, 1024).unwrap();

        let attempt_body = replay_body.body_for_attempt().unwrap();
        assert!(axum::body::to_bytes(attempt_body, 1024).await.is_err());

        assert_eq!(
            replay_body.buffered_len(),
            0,
            "body-stream errors should release non-replayable bytes"
        );
        assert_eq!(replay_body.replay_readiness(), ReplayReadiness::Incomplete);
        assert_eq!(
            replay_body.body_for_attempt().err(),
            Some(StatusCode::BAD_GATEWAY)
        );
    }

    #[test]
    fn replay_body_rejects_content_length_above_limit() {
        let mut headers = HeaderMap::new();
        headers.insert(
            axum::http::header::CONTENT_LENGTH,
            HeaderValue::from_static("1025"),
        );

        assert_eq!(
            ReplayableRequestBody::new(&headers, Body::empty(), 1024).err(),
            Some(StatusCode::PAYLOAD_TOO_LARGE)
        );
    }

    #[test]
    fn untouched_first_body_is_ready_for_retry() {
        let body = Body::from("not-yet-polled");
        let replay_body = ReplayableRequestBody::new(&HeaderMap::new(), body, 1024).unwrap();

        assert_eq!(replay_body.replay_readiness(), ReplayReadiness::Ready);
    }
}

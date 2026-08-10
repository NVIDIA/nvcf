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

use std::pin::Pin;
use std::time::Duration;

use bytes::{Bytes, BytesMut};
use futures::{Stream, StreamExt};
use sonic_rs::{JsonValueTrait, Value};

const SSE_DONE_SENTINEL: &str = "[DONE]";

#[derive(Debug, PartialEq)]
pub(crate) enum SseMessage {
    Done,
    ChatCompletionChunk { parsed: Option<Value> },
    OtherData,
}

impl SseMessage {
    pub(crate) fn counts_as_output(&self) -> bool {
        matches!(self, Self::ChatCompletionChunk { .. })
    }
}

#[derive(Debug, PartialEq)]
pub(crate) struct ParsedSseMessage {
    pub(crate) raw_event: Bytes,
    pub(crate) message: SseMessage,
}

#[derive(Debug, Default)]
struct SseMessageBuffer {
    buffer: BytesMut,
}

impl SseMessageBuffer {
    fn push_bytes(&mut self, chunk: &[u8]) {
        self.buffer.extend_from_slice(chunk);
    }

    fn push_bytes_limited(&mut self, chunk: &[u8], max_buffer_bytes: usize) -> Option<usize> {
        self.push_bytes(chunk);
        let buffered_bytes = self.unterminated_event_bytes();
        (buffered_bytes > max_buffer_bytes).then_some(buffered_bytes)
    }

    fn unterminated_event_bytes(&self) -> usize {
        let mut remaining = self.buffer.as_ref();
        while let Some(event_end) = find_sse_event_end(remaining) {
            remaining = &remaining[event_end..];
        }
        remaining.len()
    }
}

impl Iterator for SseMessageBuffer {
    type Item = ParsedSseMessage;

    fn next(&mut self) -> Option<Self::Item> {
        let event_end = find_sse_event_end(&self.buffer)?;
        let raw_event = self.buffer.split_to(event_end).freeze();
        let fields = extract_sse_fields(raw_event.as_ref());
        Some(ParsedSseMessage {
            message: classify_sse_message(fields.event_name.as_deref(), fields.data),
            raw_event,
        })
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum SseReadTimeoutPhase {
    FirstOutput,
    SubsequentOutput,
}

#[derive(Debug, thiserror::Error)]
pub(crate) enum UpstreamSseReadError {
    #[error("timed out waiting for {0:?} SSE message")]
    Timeout(SseReadTimeoutPhase),
    #[error(
        "upstream SSE buffer exceeded {max_buffer_bytes} bytes while waiting for an event boundary"
    )]
    BufferLimitExceeded {
        max_buffer_bytes: usize,
        buffered_bytes: usize,
    },
    #[error("failed to read upstream SSE bytes: {0}")]
    Upstream(#[source] anyhow::Error),
}

pub(crate) type UpstreamSseMessageStream =
    Pin<Box<dyn Stream<Item = Result<ParsedSseMessage, UpstreamSseReadError>> + Send>>;

pub(crate) fn upstream_sse_message_stream<S>(
    mut byte_stream: S,
    first_output_timeout: Duration,
    output_chunk_timeout: Duration,
    max_buffer_bytes: usize,
) -> UpstreamSseMessageStream
where
    S: Stream<Item = reqwest::Result<bytes::Bytes>> + Send + Unpin + 'static,
{
    Box::pin(async_stream::try_stream! {
        let mut sse_messages = SseMessageBuffer::default();
        let mut has_seen_output = false;

        loop {
            if let Some(parsed_message) = sse_messages.next() {
                if parsed_message.message.counts_as_output() {
                    has_seen_output = true;
                }
                yield parsed_message;
                continue;
            }

            let (timeout, timeout_phase) = if has_seen_output {
                (output_chunk_timeout, SseReadTimeoutPhase::SubsequentOutput)
            } else {
                (first_output_timeout, SseReadTimeoutPhase::FirstOutput)
            };

            match tokio::time::timeout(timeout, byte_stream.next()).await {
                Ok(Some(Ok(chunk))) => {
                    if chunk.is_empty() {
                        continue;
                    }
                    if let Some(buffered_bytes) =
                        sse_messages.push_bytes_limited(chunk.as_ref(), max_buffer_bytes)
                    {
                        Err(UpstreamSseReadError::BufferLimitExceeded {
                            max_buffer_bytes,
                            buffered_bytes,
                        })?;
                    }
                }
                Ok(Some(Err(error))) => {
                    Err(UpstreamSseReadError::Upstream(anyhow::Error::new(error)))?;
                }
                Ok(None) => {
                    for parsed_message in sse_messages.by_ref() {
                        yield parsed_message;
                    }
                    break;
                }
                Err(_) => {
                    Err(UpstreamSseReadError::Timeout(timeout_phase))?;
                }
            }
        }
    })
}

fn classify_sse_message(event_name: Option<&str>, data: String) -> SseMessage {
    let trimmed = data.trim();
    if trimmed == SSE_DONE_SENTINEL {
        return SseMessage::Done;
    }

    let parsed = (!trimmed.is_empty() && event_name != Some("response.created"))
        .then(|| sonic_rs::from_str::<Value>(trimmed).ok())
        .flatten();
    if trimmed.is_empty()
        || event_name == Some("response.created")
        || parsed
            .as_ref()
            .is_some_and(|value| value["type"].as_str() == Some("response.created"))
    {
        return SseMessage::OtherData;
    }
    SseMessage::ChatCompletionChunk { parsed }
}

fn find_sse_event_end(buffer: &[u8]) -> Option<usize> {
    if buffer.len() < 2 {
        return None;
    }

    (0..buffer.len() - 1).find_map(|idx| {
        buffer[idx..]
            .starts_with(b"\n\n")
            .then_some(idx + 2)
            .or_else(|| buffer[idx..].starts_with(b"\r\n\r\n").then_some(idx + 4))
    })
}

#[derive(Debug, Default)]
struct ExtractedSseFields {
    event_name: Option<String>,
    data: String,
}

fn extract_sse_fields(event_bytes: &[u8]) -> ExtractedSseFields {
    let text = String::from_utf8_lossy(event_bytes);
    let mut fields = ExtractedSseFields::default();
    let mut saw_data = false;
    // Classify only standard SSE data/event fields. Comments remain part of
    // the raw event forwarded to the caller.
    for line in text.lines().map(|line| line.trim_end_matches('\r')) {
        match line.split_once(':') {
            Some(("data", rest)) => {
                if saw_data {
                    fields.data.push('\n');
                }
                fields.data.push_str(rest.trim_start());
                saw_data = true;
            }
            Some(("event", rest)) if fields.event_name.is_none() => {
                fields.event_name = Some(rest.trim_start().to_string());
            }
            _ => {}
        }
    }
    fields
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::hint::black_box;

    use crate::output_token_parser::OutputTokenParser;
    use crate::request_quality_monitor::{RequestOutputTokenProgress, RequestQualityRecorder};

    #[test]
    fn yields_complete_messages() {
        let mut messages = SseMessageBuffer::default();
        messages.push_bytes(b"data: first\n\ndata: sec");
        assert_eq!(
            messages.next(),
            Some(ParsedSseMessage {
                message: SseMessage::ChatCompletionChunk { parsed: None },
                raw_event: Bytes::from_static(b"data: first\n\n"),
            })
        );
        assert_eq!(messages.next(), None);

        messages.push_bytes(b"ond\n\n");
        assert_eq!(
            messages.next(),
            Some(ParsedSseMessage {
                message: SseMessage::ChatCompletionChunk { parsed: None },
                raw_event: Bytes::from_static(b"data: second\n\n"),
            })
        );
        assert_eq!(messages.next(), None);
    }

    #[test]
    fn classifies_done_and_output_messages() {
        let mut messages = SseMessageBuffer::default();
        messages.push_bytes(
            b"data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
        );
        messages.push_bytes(b"data: [DONE]\n\n");

        assert!(matches!(
            messages.next(),
            Some(ParsedSseMessage {
                message: SseMessage::ChatCompletionChunk { .. },
                ..
            })
        ));
        assert_eq!(
            messages.next(),
            Some(ParsedSseMessage {
                message: SseMessage::Done,
                raw_event: Bytes::from_static(b"data: [DONE]\n\n"),
            })
        );
        assert_eq!(messages.next(), None);
    }

    #[test]
    fn parses_multiline_crlf_chat_events() {
        let mut messages = SseMessageBuffer::default();
        messages.push_bytes(
            b": keepalive\r\nevent: chunk\r\ndata: {\r\ndata: \"object\":\"chat.completion.chunk\",\r\ndata: \"choices\":[{\"delta\":{\"content\":\"hi\"}}]\r\ndata: }\r\n\r\n",
        );

        let parsed = messages.next().expect("complete SSE event");
        assert!(matches!(
            parsed.message,
            SseMessage::ChatCompletionChunk { .. }
        ));
        let SseMessage::ChatCompletionChunk { .. } = parsed.message else {
            unreachable!("message kind asserted above");
        };
        let fields = extract_sse_fields(parsed.raw_event.as_ref());
        assert_eq!(
            fields.data,
            "{\n\"object\":\"chat.completion.chunk\",\n\"choices\":[{\"delta\":{\"content\":\"hi\"}}]\n}"
        );
        assert_eq!(messages.next(), None);
    }

    #[test]
    fn data_events_are_not_json_classified() {
        let mut messages = SseMessageBuffer::default();
        messages.push_bytes(b"data: {\"object\":\"chat.completion\"}\n\n");
        messages.push_bytes(b"data: [DONE]\n\n");

        let data = messages.next().expect("data event");
        assert!(data.message.counts_as_output());
        let done = messages.next().expect("done event");
        assert_eq!(done.message, SseMessage::Done);
        assert!(!done.message.counts_as_output());
    }

    #[test]
    fn responses_created_does_not_count_as_output() {
        let mut messages = SseMessageBuffer::default();
        messages.push_bytes(b"event: response.created\ndata: {\"type\":\"response.created\"}\n\n");
        messages.push_bytes(
            b"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n",
        );

        let created = messages.next().expect("created event");
        assert!(!created.message.counts_as_output());
        let delta = messages.next().expect("delta event");
        assert!(delta.message.counts_as_output());
    }

    #[tokio::test]
    async fn rejects_an_unterminated_event_that_exceeds_the_buffer_limit() {
        let byte_stream = futures::stream::iter([
            Ok::<_, reqwest::Error>(Bytes::from_static(b"data: 1234")),
            Ok::<_, reqwest::Error>(Bytes::from_static(b"5678")),
        ]);
        let mut messages = upstream_sse_message_stream(
            byte_stream,
            Duration::from_secs(1),
            Duration::from_secs(1),
            11,
        );

        match messages.next().await {
            Some(Err(UpstreamSseReadError::BufferLimitExceeded {
                max_buffer_bytes,
                buffered_bytes,
            })) => {
                assert_eq!(max_buffer_bytes, 11);
                assert_eq!(buffered_bytes, 14);
            }
            unexpected => panic!(
                "an upstream peer must not keep an unterminated SSE event buffered indefinitely: {unexpected:?}"
            ),
        }
    }

    #[tokio::test]
    async fn accepts_a_large_chunk_when_it_contains_complete_small_events() {
        let byte_stream = futures::stream::iter([Ok::<_, reqwest::Error>(Bytes::from_static(
            b"data: one\n\ndata: two\n\n",
        ))]);
        let mut messages = upstream_sse_message_stream(
            byte_stream,
            Duration::from_secs(1),
            Duration::from_secs(1),
            11,
        );

        assert_eq!(
            messages
                .next()
                .await
                .expect("first complete SSE event should be emitted")
                .expect("first complete SSE event should not fail"),
            ParsedSseMessage {
                message: SseMessage::ChatCompletionChunk { parsed: None },
                raw_event: Bytes::from_static(b"data: one\n\n"),
            }
        );
        assert_eq!(
            messages
                .next()
                .await
                .expect("second complete SSE event should be emitted")
                .expect("second complete SSE event should not fail"),
            ParsedSseMessage {
                message: SseMessage::ChatCompletionChunk { parsed: None },
                raw_event: Bytes::from_static(b"data: two\n\n"),
            }
        );
        assert!(
            messages.next().await.is_none(),
            "the byte stream should be exhausted after both events"
        );
    }

    #[test]
    fn sse_peeking_helpers_preserve_token_and_forwarding_accounting() {
        let input = SseFixture::new(8, 37);

        let raw_bytes = raw_forward_only(&input.chunks, 1);
        let peeked = peek_sse_events(&input.chunks, 1);
        let fallback = fallback_sse_events(&input.chunks, 1, false);
        let quality_fallback = fallback_sse_events(&input.chunks, 1, true);

        assert_eq!(raw_bytes, input.total_bytes);
        assert_eq!(peeked.output_messages, input.output_events);
        assert_eq!(fallback.output_messages, input.output_events);
        assert_eq!(quality_fallback.output_messages, input.output_events);
        assert_eq!(peeked.output_tokens, input.output_events as u64);
        assert_eq!(fallback.output_tokens, input.output_events as u64);
        assert_eq!(quality_fallback.output_tokens, input.output_events as u64);
        assert_eq!(fallback.forwarded_bytes, input.total_bytes);
        assert_eq!(quality_fallback.forwarded_bytes, input.total_bytes);
    }

    #[derive(Debug)]
    struct SseFixture {
        chunks: Vec<Bytes>,
        output_events: usize,
        total_bytes: usize,
    }

    impl SseFixture {
        fn new(output_events: usize, fragment_size: usize) -> Self {
            let mut body = Vec::new();
            for index in 1..=output_events {
                body.extend_from_slice(
                    format!(
                        "data: {{\"object\":\"chat.completion.chunk\",\"choices\":[{{\"delta\":{{\"content\":\"x\"}}}}],\"usage\":{{\"completion_tokens\":{index}}}}}\n\n"
                    )
                    .as_bytes(),
                );
                if index % 8 == 0 {
                    body.extend_from_slice(b": keepalive\n\n");
                }
            }
            body.extend_from_slice(b"data: [DONE]\n\n");
            let total_bytes = body.len();
            let chunks = body
                .chunks(fragment_size)
                .map(Bytes::copy_from_slice)
                .collect();

            Self {
                chunks,
                output_events,
                total_bytes,
            }
        }
    }

    #[derive(Debug, Default)]
    struct PeekedEvents {
        output_messages: usize,
        output_tokens: u64,
        forwarded_bytes: usize,
    }

    fn raw_forward_only(chunks: &[Bytes], repetitions: usize) -> usize {
        let mut bytes = 0usize;
        for _ in 0..repetitions {
            for chunk in chunks {
                bytes += black_box(chunk.len());
            }
        }
        black_box(bytes)
    }

    fn peek_sse_events(chunks: &[Bytes], repetitions: usize) -> PeekedEvents {
        let mut peeked = PeekedEvents::default();
        for _ in 0..repetitions {
            let mut messages = SseMessageBuffer::default();
            let mut token_parser = OutputTokenParser::new();
            for chunk in chunks {
                messages.push_bytes(black_box(chunk.as_ref()));
                for parsed in messages.by_ref() {
                    black_box(parsed.raw_event.len());
                    if let SseMessage::ChatCompletionChunk { parsed, .. } = parsed.message {
                        peeked.output_messages += 1;
                        if let Some(progress) = token_parser.observe_json(parsed.as_ref()) {
                            peeked.output_tokens += progress.delta();
                        }
                    }
                }
            }
        }
        black_box(peeked)
    }

    fn fallback_sse_events(
        chunks: &[Bytes],
        repetitions: usize,
        quality_enabled: bool,
    ) -> PeekedEvents {
        let mut peeked = PeekedEvents::default();
        for _ in 0..repetitions {
            let mut messages = SseMessageBuffer::default();
            let mut token_parser = OutputTokenParser::new();
            let mut quality_recorder = quality_enabled.then(RequestQualityRecorder::new);
            for chunk in chunks {
                messages.push_bytes(black_box(chunk.as_ref()));
                for parsed in messages.by_ref() {
                    peeked.forwarded_bytes += parsed.raw_event.len();
                    if let SseMessage::ChatCompletionChunk { parsed, .. } = parsed.message {
                        peeked.output_messages += 1;
                        let output_progress = token_parser.observe_json(parsed.as_ref());
                        if let Some(progress) = output_progress {
                            peeked.output_tokens += progress.delta();
                        }
                        if let Some(recorder) = quality_recorder.as_mut() {
                            recorder.observe_json_chunk(
                                parsed.as_ref(),
                                output_progress.map(|progress| {
                                    RequestOutputTokenProgress::Delta(progress.delta())
                                }),
                            );
                        }
                    }
                }
            }
            if let Some(recorder) = quality_recorder.as_ref() {
                black_box(recorder.has_observed_stream_output());
            }
        }
        black_box(peeked)
    }
}

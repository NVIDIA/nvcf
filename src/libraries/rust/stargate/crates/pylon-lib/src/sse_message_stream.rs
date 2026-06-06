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
use sonic_rs::JsonValueTrait;

const SSE_DONE_SENTINEL: &str = "[DONE]";

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum SseMessage {
    Done,
    ChatCompletionChunk { raw_data: String },
    OtherData { raw_data: String },
}

impl SseMessage {
    pub(crate) fn counts_as_output(&self) -> bool {
        matches!(self, Self::ChatCompletionChunk { .. })
    }
}

#[derive(Debug, PartialEq, Eq)]
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
    #[error("failed to read upstream SSE bytes: {0}")]
    Upstream(#[source] anyhow::Error),
}

pub(crate) type UpstreamSseMessageStream =
    Pin<Box<dyn Stream<Item = Result<ParsedSseMessage, UpstreamSseReadError>> + Send>>;

pub(crate) fn upstream_sse_message_stream<S>(
    mut byte_stream: S,
    first_output_timeout: Duration,
    output_chunk_timeout: Duration,
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

            let timeout = if has_seen_output {
                output_chunk_timeout
            } else {
                first_output_timeout
            };

            match tokio::time::timeout(timeout, byte_stream.next()).await {
                Ok(Some(Ok(chunk))) => {
                    if chunk.is_empty() {
                        continue;
                    }
                    sse_messages.push_bytes(chunk.as_ref());
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
                    let phase = if has_seen_output {
                        SseReadTimeoutPhase::SubsequentOutput
                    } else {
                        SseReadTimeoutPhase::FirstOutput
                    };
                    Err(UpstreamSseReadError::Timeout(phase))?;
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

    if trimmed.is_empty() || is_responses_non_output_event(event_name, trimmed) {
        SseMessage::OtherData { raw_data: data }
    } else {
        SseMessage::ChatCompletionChunk { raw_data: data }
    }
}

fn is_responses_non_output_event(event_name: Option<&str>, data: &str) -> bool {
    event_name == Some("response.created")
        || sonic_rs::get(data.as_bytes(), &["type"])
            .ok()
            .is_some_and(|value| value.as_str() == Some("response.created"))
}

fn find_sse_event_end(buffer: &[u8]) -> Option<usize> {
    if buffer.len() < 2 {
        return None;
    }

    for idx in 0..(buffer.len() - 1) {
        if buffer[idx] == b'\n' && buffer[idx + 1] == b'\n' {
            return Some(idx + 2);
        }
        if idx + 3 < buffer.len()
            && buffer[idx] == b'\r'
            && buffer[idx + 1] == b'\n'
            && buffer[idx + 2] == b'\r'
            && buffer[idx + 3] == b'\n'
        {
            return Some(idx + 4);
        }
    }

    None
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
    for raw_line in text.lines() {
        let line = raw_line.trim_end_matches('\r');
        if let Some(rest) = line.strip_prefix("data:") {
            if saw_data {
                fields.data.push('\n');
            }
            fields.data.push_str(rest.trim_start());
            saw_data = true;
        } else if fields.event_name.is_none()
            && let Some(rest) = line.strip_prefix("event:")
        {
            fields.event_name = Some(rest.trim_start().to_string());
        }
    }
    fields
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::hint::black_box;
    use std::time::Instant;

    use crate::output_token_parser::OutputTokenParser;
    use crate::request_quality_monitor::{RequestOutputTokenProgress, RequestQualityRecorder};

    #[test]
    fn yields_complete_messages() {
        let mut messages = SseMessageBuffer::default();
        messages.push_bytes(b"data: first\n\ndata: sec");
        assert_eq!(
            messages.next(),
            Some(ParsedSseMessage {
                message: SseMessage::ChatCompletionChunk {
                    raw_data: "first".to_string()
                },
                raw_event: Bytes::from_static(b"data: first\n\n"),
            })
        );
        assert_eq!(messages.next(), None);

        messages.push_bytes(b"ond\n\n");
        assert_eq!(
            messages.next(),
            Some(ParsedSseMessage {
                message: SseMessage::ChatCompletionChunk {
                    raw_data: "second".to_string()
                },
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
        let SseMessage::ChatCompletionChunk { raw_data } = parsed.message else {
            unreachable!("message kind asserted above");
        };
        assert_eq!(
            raw_data,
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

    #[test]
    fn sse_peeking_helpers_preserve_token_and_forwarding_accounting() {
        let input = SseBenchmarkInput::new(8, 37);

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

        print_sse_measurement(
            "tiny_sse_peek_fixture",
            Duration::from_micros(1),
            input.total_events,
            input.total_bytes,
        );
        print_sse_improvement(
            "tiny_sse_peek_fixture",
            Duration::ZERO,
            Duration::from_micros(1),
        );
        print_sse_improvement(
            "tiny_sse_peek_fixture",
            Duration::from_micros(2),
            Duration::from_micros(1),
        );
    }

    #[test]
    fn sse_peeking_overhead_report_runs_on_tiny_fixture() {
        run_sse_peeking_overhead(SseBenchmarkInput::new(8, 37), 1);
    }

    #[test]
    #[ignore = "benchmark helper; run with --release -- --ignored --nocapture"]
    fn bench_sse_peeking_overhead() {
        run_sse_peeking_overhead(SseBenchmarkInput::new(256, 37), 8_192);
    }

    fn run_sse_peeking_overhead(input: SseBenchmarkInput, repetitions: usize) {
        let total_events = input.total_events * repetitions;
        let total_output_events = input.output_events * repetitions;
        let total_bytes = input.total_bytes * repetitions;

        let raw_started = Instant::now();
        let raw_bytes = raw_forward_only(&input.chunks, repetitions);
        let raw_elapsed = raw_started.elapsed();

        let peek_started = Instant::now();
        let peeked = peek_sse_events(&input.chunks, repetitions);
        let peek_elapsed = peek_started.elapsed();

        assert_eq!(raw_bytes, total_bytes);
        assert_eq!(peeked.output_messages, total_output_events);
        assert_eq!(peeked.output_tokens, total_output_events as u64);

        let fallback_baseline_started = Instant::now();
        let fallback_baseline = fallback_sse_events(&input.chunks, repetitions, false);
        let fallback_baseline_elapsed = fallback_baseline_started.elapsed();

        let fallback_optimized_started = Instant::now();
        let fallback_optimized = fallback_sse_events(&input.chunks, repetitions, false);
        let fallback_optimized_elapsed = fallback_optimized_started.elapsed();

        assert_eq!(fallback_baseline.output_messages, total_output_events);
        assert_eq!(fallback_optimized.output_messages, total_output_events);
        assert_eq!(fallback_baseline.output_tokens, total_output_events as u64);
        assert_eq!(fallback_optimized.output_tokens, total_output_events as u64);
        assert_eq!(fallback_baseline.forwarded_bytes, total_bytes);
        assert_eq!(fallback_optimized.forwarded_bytes, total_bytes);

        let quality_baseline_started = Instant::now();
        let quality_baseline = fallback_sse_events(&input.chunks, repetitions, true);
        let quality_baseline_elapsed = quality_baseline_started.elapsed();

        let quality_optimized_started = Instant::now();
        let quality_optimized = fallback_sse_events(&input.chunks, repetitions, true);
        let quality_optimized_elapsed = quality_optimized_started.elapsed();

        assert_eq!(quality_baseline.output_messages, total_output_events);
        assert_eq!(quality_optimized.output_messages, total_output_events);
        assert_eq!(quality_baseline.output_tokens, total_output_events as u64);
        assert_eq!(quality_optimized.output_tokens, total_output_events as u64);
        assert_eq!(quality_baseline.forwarded_bytes, total_bytes);
        assert_eq!(quality_optimized.forwarded_bytes, total_bytes);

        let raw_ns_per_event = raw_elapsed.as_nanos() as f64 / total_events as f64;
        let peek_ns_per_event = peek_elapsed.as_nanos() as f64 / total_events as f64;
        let overhead_ns_per_event = peek_ns_per_event - raw_ns_per_event;
        let overhead_ratio = peek_elapsed.as_secs_f64() / raw_elapsed.as_secs_f64();
        let mib = total_bytes as f64 / 1024.0 / 1024.0;

        println!(
            "sse_peek_bench repetitions={repetitions} events={total_events} bytes={total_bytes}"
        );
        println!(
            "raw_forward_only: {:.3?}, {:.1} ns/event, {:.1} MiB/s",
            raw_elapsed,
            raw_ns_per_event,
            mib / raw_elapsed.as_secs_f64()
        );
        println!(
            "peek_sse_events:  {:.3?}, {:.1} ns/event, {:.1} MiB/s",
            peek_elapsed,
            peek_ns_per_event,
            mib / peek_elapsed.as_secs_f64()
        );
        println!(
            "peek_overhead:    {:.1} ns/event, {:.2}x raw baseline",
            overhead_ns_per_event, overhead_ratio
        );
        print_sse_measurement(
            "fallback_token_parser_baseline",
            fallback_baseline_elapsed,
            total_events,
            total_bytes,
        );
        print_sse_measurement(
            "fallback_token_parser_optimized",
            fallback_optimized_elapsed,
            total_events,
            total_bytes,
        );
        print_sse_improvement(
            "fallback_token_parser",
            fallback_baseline_elapsed,
            fallback_optimized_elapsed,
        );
        print_sse_measurement(
            "quality_enabled_baseline",
            quality_baseline_elapsed,
            total_events,
            total_bytes,
        );
        print_sse_measurement(
            "quality_enabled_optimized",
            quality_optimized_elapsed,
            total_events,
            total_bytes,
        );
        print_sse_improvement(
            "quality_enabled",
            quality_baseline_elapsed,
            quality_optimized_elapsed,
        );
    }

    #[derive(Debug)]
    struct SseBenchmarkInput {
        chunks: Vec<Bytes>,
        output_events: usize,
        total_events: usize,
        total_bytes: usize,
    }

    impl SseBenchmarkInput {
        fn new(output_events: usize, fragment_size: usize) -> Self {
            let mut body = Vec::new();
            let mut total_events = 0usize;
            for index in 1..=output_events {
                body.extend_from_slice(
                    format!(
                        "data: {{\"object\":\"chat.completion.chunk\",\"choices\":[{{\"delta\":{{\"content\":\"x\"}}}}],\"usage\":{{\"completion_tokens\":{index}}}}}\n\n"
                    )
                    .as_bytes(),
                );
                total_events += 1;

                if index % 8 == 0 {
                    body.extend_from_slice(b": keepalive\n\n");
                    total_events += 1;
                }
            }
            body.extend_from_slice(b"data: [DONE]\n\n");
            total_events += 1;

            let total_bytes = body.len();
            let chunks = body
                .chunks(fragment_size)
                .map(Bytes::copy_from_slice)
                .collect();

            Self {
                chunks,
                output_events,
                total_events,
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
                    if let SseMessage::ChatCompletionChunk { raw_data } = parsed.message {
                        peeked.output_messages += 1;
                        if let Some(delta) = token_parser.parse_incremental_output_tokens(&raw_data)
                        {
                            peeked.output_tokens += delta;
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
                    if let SseMessage::ChatCompletionChunk { raw_data } = parsed.message {
                        peeked.output_messages += 1;
                        let output_token_delta =
                            token_parser.parse_incremental_output_tokens(&raw_data);
                        if let Some(delta) = output_token_delta {
                            peeked.output_tokens += delta;
                        }
                        if let Some(recorder) = quality_recorder.as_mut() {
                            recorder.observe_sse_chunk_with_token_progress(
                                &raw_data,
                                output_token_delta.map(RequestOutputTokenProgress::Delta),
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

    fn print_sse_measurement(
        label: &str,
        elapsed: std::time::Duration,
        total_events: usize,
        total_bytes: usize,
    ) {
        let ns_per_event = elapsed.as_nanos() as f64 / total_events as f64;
        let mib = total_bytes as f64 / 1024.0 / 1024.0;
        println!(
            "{label}: {:.3?}, {:.1} ns/event, {:.1} MiB/s",
            elapsed,
            ns_per_event,
            mib / elapsed.as_secs_f64()
        );
    }

    fn print_sse_improvement(
        label: &str,
        baseline: std::time::Duration,
        optimized: std::time::Duration,
    ) {
        let improvement = if baseline.is_zero() {
            0.0
        } else {
            ((baseline.as_secs_f64() - optimized.as_secs_f64()) / baseline.as_secs_f64()) * 100.0
        };
        println!("{label}_improvement: {improvement:.2}%");
    }
}

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
use std::time::{Duration, Instant};

use bytes::{Bytes, BytesMut};
use futures::{Stream, StreamExt};
use sonic_rs::{JsonContainerTrait, JsonValueTrait, Value};
use tokio_util::task::AbortOnDropHandle;

use crate::output_token_parser::estimate_token_like_units;

const SSE_DONE_SENTINEL: &str = "[DONE]";
const SSE_DELIVERY_BUFFER_EVENTS: usize = 1;

#[derive(Debug, Default, PartialEq, Eq)]
pub(crate) struct GeneratedOutput {
    pub(crate) estimated_token_units: u64,
    pub(crate) token_bearing: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct ExactUsage {
    pub(crate) input_tokens: Option<u64>,
    pub(crate) output_tokens: Option<u64>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum RelayOutcome {
    Complete,
    Failed,
}

#[derive(Debug, Default, PartialEq, Eq)]
pub(crate) struct SseEventFacts {
    pub(crate) generated_output: Option<GeneratedOutput>,
    pub(crate) exact_usage: Option<ExactUsage>,
    pub(crate) terminal: Option<RelayOutcome>,
}

#[derive(Debug, PartialEq)]
pub(crate) struct ParsedSseMessage {
    pub(crate) raw_event: Bytes,
    pub(crate) parsed: Option<Value>,
    pub(crate) facts: SseEventFacts,
    pub(crate) received_at: Instant,
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
        let (parsed, facts) = classify_sse_event(fields.event_name.as_deref(), &fields.data);
        Some(ParsedSseMessage {
            raw_event,
            parsed,
            facts,
            received_at: Instant::now(),
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
    #[error("upstream SSE producer task failed: {0}")]
    Producer(#[source] tokio::task::JoinError),
}

pub(crate) type UpstreamSseMessageStream =
    Pin<Box<dyn Stream<Item = Result<ParsedSseMessage, UpstreamSseReadError>> + Send>>;

pub(crate) fn upstream_sse_message_stream<S>(
    byte_stream: S,
    first_output_timeout: Duration,
    output_chunk_timeout: Duration,
    max_buffer_bytes: usize,
) -> UpstreamSseMessageStream
where
    S: Stream<Item = reqwest::Result<bytes::Bytes>> + Send + Unpin + 'static,
{
    let (delivery_tx, mut delivery_rx) = tokio::sync::mpsc::channel(SSE_DELIVERY_BUFFER_EVENTS);
    let first_output_deadline = tokio::time::Instant::now() + first_output_timeout;
    let producer = AbortOnDropHandle::new(tokio::spawn(produce_sse_messages(
        byte_stream,
        delivery_tx,
        first_output_deadline,
        output_chunk_timeout,
        max_buffer_bytes,
    )));
    Box::pin(async_stream::stream! {
        while let Some(message) = delivery_rx.recv().await {
            yield message;
        }
        if let Err(error) = producer.await {
            yield Err(UpstreamSseReadError::Producer(error));
        }
    })
}

async fn produce_sse_messages<S>(
    mut byte_stream: S,
    delivery_tx: tokio::sync::mpsc::Sender<Result<ParsedSseMessage, UpstreamSseReadError>>,
    first_output_deadline: tokio::time::Instant,
    output_chunk_timeout: Duration,
    max_buffer_bytes: usize,
) where
    S: Stream<Item = reqwest::Result<bytes::Bytes>> + Send + Unpin + 'static,
{
    let mut sse_messages = SseMessageBuffer::default();
    let mut timeout_phase = SseReadTimeoutPhase::FirstOutput;
    let mut deadline = first_output_deadline;

    loop {
        let deadline_sleep = tokio::time::sleep_until(deadline);
        tokio::pin!(deadline_sleep);
        tokio::select! {
            biased;
            _ = &mut deadline_sleep => {
                let _ = delivery_tx
                    .send(Err(UpstreamSseReadError::Timeout(timeout_phase)))
                    .await;
                return;
            }
            _ = delivery_tx.closed() => return,
            next = byte_stream.next() => {
                let received_at = tokio::time::Instant::now();
                if received_at >= deadline {
                    let _ = delivery_tx
                        .send(Err(UpstreamSseReadError::Timeout(timeout_phase)))
                        .await;
                    return;
                }
                match next {
                    Some(Ok(chunk)) if chunk.is_empty() => {}
                    Some(Ok(chunk)) => {
                        if let Some(buffered_bytes) =
                            sse_messages.push_bytes_limited(chunk.as_ref(), max_buffer_bytes)
                        {
                            let _ = delivery_tx
                                .send(Err(UpstreamSseReadError::BufferLimitExceeded {
                                    max_buffer_bytes,
                                    buffered_bytes,
                                }))
                                .await;
                            return;
                        }

                        let mut messages = Vec::new();
                        let mut terminal = false;
                        for mut message in sse_messages.by_ref() {
                            message.received_at = received_at.into_std();
                            if message.facts.generated_output.is_some() {
                                timeout_phase = SseReadTimeoutPhase::SubsequentOutput;
                                deadline = received_at + output_chunk_timeout;
                            }
                            terminal = message.facts.terminal.is_some();
                            messages.push(message);
                            if terminal {
                                break;
                            }
                        }

                        let mut timed_out = false;
                        for message in messages {
                            if timed_out || terminal {
                                if delivery_tx.send(Ok(message)).await.is_err() {
                                    return;
                                }
                                continue;
                            }

                            let deadline_sleep = tokio::time::sleep_until(deadline);
                            tokio::pin!(deadline_sleep);
                            let permit = tokio::select! {
                                biased;
                                _ = &mut deadline_sleep => None,
                                _ = delivery_tx.closed() => return,
                                permit = delivery_tx.reserve() => match permit {
                                    Ok(permit) => Some(permit),
                                    Err(_) => return,
                                },
                            };
                            if let Some(permit) = permit {
                                permit.send(Ok(message));
                            } else {
                                timed_out = true;
                                if delivery_tx.send(Ok(message)).await.is_err() {
                                    return;
                                }
                            }
                        }

                        if terminal {
                            return;
                        }
                        if timed_out {
                            let _ = delivery_tx
                                .send(Err(UpstreamSseReadError::Timeout(timeout_phase)))
                                .await;
                            return;
                        }
                    }
                    Some(Err(error)) => {
                        let _ = delivery_tx
                            .send(Err(UpstreamSseReadError::Upstream(anyhow::Error::new(error))))
                            .await;
                        return;
                    }
                    None => return,
                }
            }
        }
    }
}

fn classify_sse_event(event_name: Option<&str>, data: &str) -> (Option<Value>, SseEventFacts) {
    let trimmed = data.trim();
    if trimmed == SSE_DONE_SENTINEL {
        let terminal = match terminal_outcome(event_name) {
            Some(RelayOutcome::Failed) => RelayOutcome::Failed,
            _ => RelayOutcome::Complete,
        };
        return (
            None,
            SseEventFacts {
                terminal: Some(terminal),
                ..SseEventFacts::default()
            },
        );
    }

    let parsed = (!trimmed.is_empty())
        .then(|| sonic_rs::from_str::<Value>(trimmed).ok())
        .flatten();
    let Some(value) = parsed.as_ref() else {
        let terminal = if trimmed.is_empty() {
            None
        } else {
            terminal_outcome(event_name)
        };
        return (
            parsed,
            SseEventFacts {
                terminal,
                ..SseEventFacts::default()
            },
        );
    };
    let json_event_type = value["type"].as_str();
    let event_type = json_event_type.or(event_name);
    let facts = SseEventFacts {
        generated_output: generated_output(value, event_type),
        exact_usage: exact_usage(value),
        terminal: merge_terminal_outcomes(
            terminal_outcome(json_event_type),
            terminal_outcome(event_name),
        ),
    };
    (parsed, facts)
}

// Chat Completions | chat.completion.chunk | JSON path below | string
const CHAT_DELTA_TEXT_FIELDS: &[(&str, &[&str])] = &[
    ("choices[].delta.content", &["content"]),
    ("choices[].delta.refusal", &["refusal"]),
    ("choices[].delta.reasoning", &["reasoning"]),
    ("choices[].delta.reasoning_content", &["reasoning_content"]),
    (
        "choices[].delta.function_call.name",
        &["function_call", "name"],
    ),
    (
        "choices[].delta.function_call.arguments",
        &["function_call", "arguments"],
    ),
    ("choices[].delta.audio.transcript", &["audio", "transcript"]),
];

// Chat Completions | chat.completion.chunk | JSON path below | string
const CHAT_TOOL_TEXT_FIELDS: &[(&str, &str)] = &[
    ("choices[].delta.tool_calls[].function.name", "name"),
    (
        "choices[].delta.tool_calls[].function.arguments",
        "arguments",
    ),
];

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum GeneratedValueKind {
    Text,
    Modal,
}

#[derive(Debug, Clone, Copy)]
struct ResponsesOutputSpec {
    event_type: &'static str,
    field: &'static str,
    kind: GeneratedValueKind,
}

// Responses | event_type | JSON path | accepted value type: string
const RESPONSES_OUTPUT_ALLOWLIST: &[ResponsesOutputSpec] = &[
    ResponsesOutputSpec {
        event_type: "response.output_text.delta",
        field: "delta",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.refusal.delta",
        field: "delta",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.reasoning_text.delta",
        field: "delta",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.reasoning_summary_text.delta",
        field: "delta",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.function_call_arguments.delta",
        field: "delta",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.mcp_call_arguments.delta",
        field: "delta",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.custom_tool_call_input.delta",
        field: "delta",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.code_interpreter_call_code.delta",
        field: "delta",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.shell_call_command.added",
        field: "command",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.shell_call_command.delta",
        field: "delta",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.audio.transcript.delta",
        field: "delta",
        kind: GeneratedValueKind::Text,
    },
    ResponsesOutputSpec {
        event_type: "response.audio.delta",
        field: "delta",
        kind: GeneratedValueKind::Modal,
    },
    ResponsesOutputSpec {
        event_type: "response.image_generation_call.partial_image",
        field: "partial_image_b64",
        kind: GeneratedValueKind::Modal,
    },
];

fn generated_output(value: &Value, event_type: Option<&str>) -> Option<GeneratedOutput> {
    let mut output = GeneratedOutput::default();
    let mut saw_generated_output = false;

    if value["object"].as_str() == Some("chat.completion.chunk")
        && let Some(choices) = value["choices"].as_array()
    {
        for choice in choices {
            let delta = &choice["delta"];
            for (_, path) in CHAT_DELTA_TEXT_FIELDS {
                add_generated_text(
                    &mut output,
                    &mut saw_generated_output,
                    string_at_path(delta, path),
                );
            }
            if delta["audio"]["data"]
                .as_str()
                .is_some_and(|value| !value.is_empty())
            {
                saw_generated_output = true;
            }
            if let Some(tool_calls) = delta["tool_calls"].as_array() {
                for tool_call in tool_calls {
                    for (_, field) in CHAT_TOOL_TEXT_FIELDS {
                        add_generated_text(
                            &mut output,
                            &mut saw_generated_output,
                            tool_call["function"][field].as_str(),
                        );
                    }
                }
            }
        }
    }

    if let Some(spec) = RESPONSES_OUTPUT_ALLOWLIST
        .iter()
        .find(|spec| Some(spec.event_type) == event_type)
        && let Some(fragment) = value[spec.field].as_str().filter(|value| !value.is_empty())
    {
        saw_generated_output = true;
        match spec.kind {
            GeneratedValueKind::Text => {
                output.token_bearing = true;
                output.estimated_token_units = output
                    .estimated_token_units
                    .saturating_add(estimate_token_like_units(fragment));
            }
            GeneratedValueKind::Modal => {}
        }
    }

    saw_generated_output.then_some(output)
}

fn string_at_path<'a>(mut value: &'a Value, path: &[&str]) -> Option<&'a str> {
    for field in path {
        value = &value[field];
    }
    value.as_str()
}

fn add_generated_text(
    output: &mut GeneratedOutput,
    saw_generated_output: &mut bool,
    value: Option<&str>,
) {
    if let Some(value) = value.filter(|value| !value.is_empty()) {
        *saw_generated_output = true;
        output.token_bearing = true;
        output.estimated_token_units = output
            .estimated_token_units
            .saturating_add(estimate_token_like_units(value));
    }
}

fn exact_usage(value: &Value) -> Option<ExactUsage> {
    let input_tokens = value["usage"]["prompt_tokens"]
        .as_u64()
        .or_else(|| value["response"]["usage"]["input_tokens"].as_u64());
    let output_tokens = value["usage"]["completion_tokens"]
        .as_u64()
        .or_else(|| value["response"]["usage"]["output_tokens"].as_u64())
        .or_else(|| value["output_tokens_so_far"].as_u64());
    (input_tokens.is_some() || output_tokens.is_some()).then_some(ExactUsage {
        input_tokens,
        output_tokens,
    })
}

fn terminal_outcome(event_type: Option<&str>) -> Option<RelayOutcome> {
    match event_type {
        Some("response.completed") => Some(RelayOutcome::Complete),
        Some("response.failed" | "response.incomplete" | "error") => Some(RelayOutcome::Failed),
        _ => None,
    }
}

fn merge_terminal_outcomes(
    first: Option<RelayOutcome>,
    second: Option<RelayOutcome>,
) -> Option<RelayOutcome> {
    match (first, second) {
        (Some(RelayOutcome::Failed), _) | (_, Some(RelayOutcome::Failed)) => {
            Some(RelayOutcome::Failed)
        }
        (Some(RelayOutcome::Complete), _) | (_, Some(RelayOutcome::Complete)) => {
            Some(RelayOutcome::Complete)
        }
        (None, None) => None,
    }
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

    fn parse_event(raw_event: impl AsRef<[u8]>) -> ParsedSseMessage {
        let mut messages = SseMessageBuffer::default();
        messages.push_bytes(raw_event.as_ref());
        let parsed = messages
            .next()
            .expect("fixture should contain one SSE event");
        assert_eq!(messages.next(), None, "fixture should contain one event");
        parsed
    }

    fn parse_data(data: &str) -> ParsedSseMessage {
        parse_event(format!("data: {data}\n\n"))
    }

    #[test]
    fn yields_only_complete_messages_and_preserves_raw_bytes() {
        let mut messages = SseMessageBuffer::default();
        messages.push_bytes(b"data: first\n\ndata: sec");
        let first = messages.next().expect("first event should be complete");
        assert_eq!(first.raw_event, Bytes::from_static(b"data: first\n\n"));
        assert_eq!(first.parsed, None);
        assert_eq!(first.facts, SseEventFacts::default());
        assert_eq!(messages.next(), None);

        messages.push_bytes(b"ond\n\n");
        let second = messages
            .next()
            .expect("second event should now be complete");
        assert_eq!(second.raw_event, Bytes::from_static(b"data: second\n\n"));
        assert_eq!(second.facts, SseEventFacts::default());
        assert_eq!(messages.next(), None);
    }

    #[test]
    fn classifies_every_chat_generated_string_path() {
        for (json_path, relative_path) in CHAT_DELTA_TEXT_FIELDS {
            let delta = match *relative_path {
                [field] => serde_json::json!({(*field): "x"}),
                [container, field] => {
                    serde_json::json!({(*container): {(*field): "x"}})
                }
                _ => panic!("unsupported test path {json_path}"),
            };
            let data = serde_json::json!({
                "object": "chat.completion.chunk",
                "choices": [{"delta": delta}],
            });
            let parsed = parse_data(&data.to_string());
            let output = parsed
                .facts
                .generated_output
                .unwrap_or_else(|| panic!("{json_path} should count as generated output"));
            assert!(output.token_bearing, "{json_path} should be token-bearing");
            assert_eq!(
                output.estimated_token_units, 1,
                "unexpected estimate for {json_path}"
            );
        }

        for (json_path, field) in CHAT_TOOL_TEXT_FIELDS {
            let data = serde_json::json!({
                "object": "chat.completion.chunk",
                "choices": [{"delta": {"tool_calls": [{"function": {(*field): "x"}}]}}],
            });
            let parsed = parse_data(&data.to_string());
            let output = parsed
                .facts
                .generated_output
                .unwrap_or_else(|| panic!("{json_path} should count as generated output"));
            assert!(output.token_bearing, "{json_path} should be token-bearing");
            assert_eq!(
                output.estimated_token_units, 1,
                "unexpected estimate for {json_path}"
            );
        }
    }

    #[test]
    fn chat_mixed_channels_collect_all_generated_fragments() {
        let parsed = parse_data(
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"answer","refusal":"refuse","reasoning":"think","reasoning_content":"think-more","function_call":{"name":"legacy","arguments":"{}"},"tool_calls":[{"function":{"name":"tool","arguments":"[]"}}],"audio":{"transcript":"spoken"}}}]}"#,
        );
        assert_eq!(
            parsed
                .facts
                .generated_output
                .expect("mixed Chat event should count")
                .estimated_token_units,
            9
        );
    }

    #[test]
    fn chat_audio_data_is_modal_generated_output() {
        let parsed = parse_data(
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"audio":{"data":"YWJj"}}}]}"#,
        );
        let output = parsed
            .facts
            .generated_output
            .expect("audio data should count as generated output");

        assert!(!output.token_bearing);
        assert_eq!(output.estimated_token_units, 0);
    }

    #[test]
    fn chat_audio_data_with_transcript_is_token_bearing() {
        let parsed = parse_data(
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"audio":{"data":"YWJj","transcript":"spoken"}}}]}"#,
        );
        let output = parsed
            .facts
            .generated_output
            .expect("mixed audio output should count as generated output");

        assert!(output.token_bearing);
        assert_eq!(output.estimated_token_units, 1);
    }

    #[test]
    fn classifies_every_responses_generated_output_path() {
        for spec in RESPONSES_OUTPUT_ALLOWLIST {
            let mut data = serde_json::json!({"type": spec.event_type});
            data[spec.field] = serde_json::json!("x");
            let parsed = parse_data(&data.to_string());
            let output = parsed.facts.generated_output.unwrap_or_else(|| {
                panic!(
                    "Responses {} {} should count as generated output",
                    spec.event_type, spec.field
                )
            });
            match spec.kind {
                GeneratedValueKind::Text => {
                    assert!(output.token_bearing);
                    assert_eq!(output.estimated_token_units, 1);
                }
                GeneratedValueKind::Modal => {
                    assert!(!output.token_bearing);
                    assert_eq!(output.estimated_token_units, 0);
                }
            }
        }
    }

    #[test]
    fn non_empty_whitespace_text_is_token_bearing_even_when_its_estimate_is_zero() {
        let parsed = parse_data(
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":" "}}]}"#,
        );
        let output = parsed
            .facts
            .generated_output
            .expect("non-empty text delta should count as generated output");

        assert!(output.token_bearing);
        assert_eq!(output.estimated_token_units, 0);
    }

    #[test]
    fn empty_or_non_string_allowlisted_values_are_inert() {
        for value in [
            serde_json::Value::String(String::new()),
            serde_json::json!(7),
        ] {
            let chat = serde_json::json!({
                "object": "chat.completion.chunk",
                "choices": [{"delta": {"content": value.clone()}}],
            });
            assert_eq!(parse_data(&chat.to_string()).facts.generated_output, None);

            let response = serde_json::json!({
                "type": "response.output_text.delta",
                "delta": value,
            });
            assert_eq!(
                parse_data(&response.to_string()).facts.generated_output,
                None
            );
        }
    }

    #[test]
    fn metadata_progress_unknown_and_malformed_events_are_inert() {
        for data in [
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"role":"assistant"}}]}"#,
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{},"finish_reason":"stop"}]}"#,
            r#"{"object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3}}"#,
            r#"{"type":"response.created"}"#,
            r#"{"type":"response.queued"}"#,
            r#"{"type":"response.in_progress"}"#,
            r#"{"type":"response.output_item.added"}"#,
            r#"{"type":"response.content_part.added"}"#,
            r#"{"type":"response.output_text.annotation.added"}"#,
            r#"{"type":"response.web_search_call.searching"}"#,
            r#"{"type":"response.mcp_call.in_progress"}"#,
            r#"{"type":"response.code_interpreter_call.in_progress"}"#,
            r#"{"type":"response.code_interpreter_call_code.interpreting"}"#,
            r#"{"type":"response.shell_call_output.delta","delta":"stdout"}"#,
            r#"{"type":"response.apply_patch_call_output.delta","delta":"stderr"}"#,
            r#"{"type":"response.future.delta","delta":"unknown"}"#,
            r#"{"choices":[{"delta":{"content":"missing-chat-event-type"}}]}"#,
            "not-json",
            "{",
            "",
        ] {
            let parsed = parse_data(data);
            assert_eq!(
                parsed.facts.generated_output, None,
                "unexpected generated output for {data}"
            );
        }
    }

    #[test]
    fn parses_multiline_crlf_chat_events() {
        let mut messages = SseMessageBuffer::default();
        messages.push_bytes(
            b": keepalive\r\nevent: chunk\r\ndata: {\r\ndata: \"object\":\"chat.completion.chunk\",\r\ndata: \"choices\":[{\"delta\":{\"content\":\"hi\"}}]\r\ndata: }\r\n\r\n",
        );

        let parsed = messages.next().expect("complete SSE event");
        assert!(parsed.facts.generated_output.is_some());
        let fields = extract_sse_fields(parsed.raw_event.as_ref());
        assert_eq!(
            fields.data,
            "{\n\"object\":\"chat.completion.chunk\",\n\"choices\":[{\"delta\":{\"content\":\"hi\"}}]\n}"
        );
        assert_eq!(messages.next(), None);
    }

    #[test]
    fn parses_exact_usage_independently_from_generated_output() {
        for (data, expected) in [
            (
                r#"{"object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":3}}"#,
                ExactUsage {
                    input_tokens: Some(8),
                    output_tokens: Some(3),
                },
            ),
            (
                r#"{"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":2}}}"#,
                ExactUsage {
                    input_tokens: Some(5),
                    output_tokens: Some(2),
                },
            ),
            (
                r#"{"object":"chat.completion.chunk","output_tokens_so_far":7,"choices":[]}"#,
                ExactUsage {
                    input_tokens: None,
                    output_tokens: Some(7),
                },
            ),
        ] {
            let parsed = parse_data(data);
            assert_eq!(parsed.facts.exact_usage, Some(expected));
            assert_eq!(parsed.facts.generated_output, None);
        }
    }

    #[test]
    fn ignores_missing_null_and_non_integer_usage() {
        for data in [
            r#"{"object":"chat.completion.chunk"}"#,
            r#"{"object":"chat.completion.chunk","usage":null}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":null}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":"4"}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":4.5}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":-1}}"#,
            "not-json",
        ] {
            assert_eq!(parse_data(data).facts.exact_usage, None);
        }
    }

    #[test]
    fn classifies_explicit_terminal_outcomes_without_output() {
        assert_eq!(
            parse_data("[DONE]").facts.terminal,
            Some(RelayOutcome::Complete)
        );
        let completed = parse_data(r#"{"type":"response.completed"}"#);
        assert_eq!(completed.facts.terminal, Some(RelayOutcome::Complete));
        assert_eq!(completed.facts.generated_output, None);
        for event_type in ["response.failed", "response.incomplete", "error"] {
            let parsed = parse_data(&format!(r#"{{"type":"{event_type}"}}"#));
            assert_eq!(parsed.facts.terminal, Some(RelayOutcome::Failed));
            assert_eq!(parsed.facts.generated_output, None);
        }

        let named_error = parse_event("event: error\ndata: {\"message\":\"bad\"}\n\n");
        assert_eq!(named_error.facts.terminal, Some(RelayOutcome::Failed));

        let plain_text_error = parse_event("event: error\ndata: overloaded\n\n");
        assert_eq!(plain_text_error.facts.terminal, Some(RelayOutcome::Failed));

        let typed_named_error = parse_event("event: error\ndata: {\"type\":\"server_error\"}\n\n");
        assert_eq!(typed_named_error.facts.terminal, Some(RelayOutcome::Failed));
    }

    #[test]
    fn empty_named_events_are_not_terminal() {
        let empty_error = parse_event("event: error\ndata:\n\n");

        assert_eq!(empty_error.facts.terminal, None);
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

        let first = messages
            .next()
            .await
            .expect("first complete SSE event should be emitted")
            .expect("first complete SSE event should not fail");
        assert_eq!(first.raw_event, Bytes::from_static(b"data: one\n\n"));
        assert_eq!(first.facts, SseEventFacts::default());
        let second = messages
            .next()
            .await
            .expect("second complete SSE event should be emitted")
            .expect("second complete SSE event should not fail");
        assert_eq!(second.raw_event, Bytes::from_static(b"data: two\n\n"));
        assert_eq!(second.facts, SseEventFacts::default());
        assert!(
            messages.next().await.is_none(),
            "the byte stream should be exhausted after both events"
        );
    }

    #[tokio::test]
    async fn producer_panics_are_reported_to_the_consumer() {
        let mut messages = upstream_sse_message_stream(
            futures::stream::poll_fn(|_| -> std::task::Poll<Option<reqwest::Result<Bytes>>> {
                panic!("producer panic fixture");
            }),
            Duration::from_secs(1),
            Duration::from_secs(1),
            1024,
        );

        match messages.next().await {
            Some(Err(UpstreamSseReadError::Producer(error))) => assert!(error.is_panic()),
            unexpected => panic!("producer panic should surface as a stream error: {unexpected:?}"),
        }
        assert!(messages.next().await.is_none());
    }

    #[tokio::test(start_paused = true)]
    async fn metadata_and_partial_bytes_do_not_extend_first_output_deadline() {
        let (tx, rx) = tokio::sync::mpsc::channel(4);
        let mut messages = upstream_sse_message_stream(
            tokio_stream::wrappers::ReceiverStream::new(rx),
            Duration::from_secs(1),
            Duration::from_secs(5),
            1024,
        );

        tx.send(Ok::<_, reqwest::Error>(Bytes::from_static(
            b": keepalive\n\n",
        )))
        .await
        .unwrap();
        let keepalive = messages.next().await.unwrap().unwrap();
        assert_eq!(keepalive.facts, SseEventFacts::default());

        tokio::time::advance(Duration::from_millis(600)).await;
        tx.send(Ok(Bytes::from_static(
            b"data: {\"type\":\"response.in_progress\"}\n\n",
        )))
        .await
        .unwrap();
        let metadata = messages.next().await.unwrap().unwrap();
        assert_eq!(metadata.facts.generated_output, None);

        tokio::time::advance(Duration::from_millis(300)).await;
        tx.send(Ok(Bytes::from_static(b"data: {\"type\":\"response.")))
            .await
            .unwrap();
        let pending = messages.next();
        tokio::pin!(pending);
        tokio::task::yield_now().await;
        tokio::time::advance(Duration::from_millis(101)).await;
        assert!(matches!(
            pending.await,
            Some(Err(UpstreamSseReadError::Timeout(
                SseReadTimeoutPhase::FirstOutput
            )))
        ));
    }

    #[tokio::test(start_paused = true)]
    async fn first_output_deadline_starts_before_the_consumer_polls() {
        let (_tx, rx) = tokio::sync::mpsc::channel(1);
        let mut messages = upstream_sse_message_stream(
            tokio_stream::wrappers::ReceiverStream::new(rx),
            Duration::from_secs(1),
            Duration::from_secs(1),
            1024,
        );

        tokio::time::advance(Duration::from_millis(1001)).await;
        let message = tokio::time::timeout(Duration::from_millis(100), messages.next())
            .await
            .expect("the constructor-time first-output deadline should already have elapsed");

        assert!(matches!(
            message,
            Some(Err(UpstreamSseReadError::Timeout(
                SseReadTimeoutPhase::FirstOutput
            )))
        ));
    }

    #[tokio::test(start_paused = true)]
    async fn only_generated_output_resets_subsequent_output_deadline() {
        let (tx, rx) = tokio::sync::mpsc::channel(5);
        let mut messages = upstream_sse_message_stream(
            tokio_stream::wrappers::ReceiverStream::new(rx),
            Duration::from_secs(1),
            Duration::from_secs(1),
            1024,
        );
        let output = Bytes::from_static(
            b"data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n",
        );

        tx.send(Ok::<_, reqwest::Error>(output.clone()))
            .await
            .unwrap();
        assert!(
            messages
                .next()
                .await
                .unwrap()
                .unwrap()
                .facts
                .generated_output
                .is_some()
        );

        tokio::time::advance(Duration::from_millis(700)).await;
        tx.send(Ok(Bytes::from_static(b": keepalive\n\n")))
            .await
            .unwrap();
        assert_eq!(
            messages.next().await.unwrap().unwrap().facts,
            SseEventFacts::default()
        );

        tokio::time::advance(Duration::from_millis(200)).await;
        tx.send(Ok(output)).await.unwrap();
        assert!(
            messages
                .next()
                .await
                .unwrap()
                .unwrap()
                .facts
                .generated_output
                .is_some()
        );

        tokio::time::advance(Duration::from_millis(800)).await;
        tx.send(Ok(Bytes::from_static(b"data: partial")))
            .await
            .unwrap();
        let pending = messages.next();
        tokio::pin!(pending);
        tokio::task::yield_now().await;
        tokio::time::advance(Duration::from_millis(201)).await;
        assert!(matches!(
            pending.await,
            Some(Err(UpstreamSseReadError::Timeout(
                SseReadTimeoutPhase::SubsequentOutput
            )))
        ));
    }

    #[tokio::test(start_paused = true)]
    async fn buffered_output_does_not_hide_deadline_while_consumer_is_stalled() {
        let (tx, rx) = tokio::sync::mpsc::channel(1);
        let mut messages = upstream_sse_message_stream(
            tokio_stream::wrappers::ReceiverStream::new(rx),
            Duration::from_secs(1),
            Duration::from_secs(1),
            1024,
        );
        tx.send(Ok::<_, reqwest::Error>(Bytes::from_static(
            b": metadata\n\ndata: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n",
        )))
        .await
        .unwrap();

        let metadata = messages.next().await.unwrap().unwrap();
        assert_eq!(metadata.facts, SseEventFacts::default());
        tokio::task::yield_now().await;

        tokio::time::advance(Duration::from_millis(1001)).await;
        tokio::task::yield_now().await;

        let output = messages.next().await.unwrap().unwrap();
        assert!(output.facts.generated_output.is_some());
        assert!(matches!(
            messages.next().await,
            Some(Err(UpstreamSseReadError::Timeout(
                SseReadTimeoutPhase::SubsequentOutput
            )))
        ));
    }

    #[tokio::test(start_paused = true)]
    async fn bounded_delivery_backpressure_does_not_pause_the_upstream_deadline() {
        let (tx, rx) = tokio::sync::mpsc::channel(1);
        let mut messages = upstream_sse_message_stream(
            tokio_stream::wrappers::ReceiverStream::new(rx),
            Duration::from_secs(1),
            Duration::from_secs(1),
            1024,
        );
        let metadata = (0..20)
            .map(|index| format!("data: {{\"index\":{index}}}\n\n"))
            .collect::<String>();
        tx.send(Ok::<_, reqwest::Error>(Bytes::from(metadata)))
            .await
            .unwrap();

        let first = messages.next().await.unwrap().unwrap();
        let received_at = first.received_at;
        tokio::task::yield_now().await;
        tokio::time::advance(Duration::from_secs(10)).await;
        tokio::task::yield_now().await;

        for _ in 1..20 {
            let message = messages.next().await.unwrap().unwrap();
            assert_eq!(message.facts, SseEventFacts::default());
            assert_eq!(message.received_at, received_at);
        }

        assert!(matches!(
            messages.next().await,
            Some(Err(UpstreamSseReadError::Timeout(
                SseReadTimeoutPhase::FirstOutput
            )))
        ));
    }

    #[tokio::test]
    async fn terminal_event_is_yielded_before_stream_completion() {
        let stream = futures::stream::iter([Ok::<_, reqwest::Error>(Bytes::from_static(
            b"data: [DONE]\n\ndata: trailing\n\n",
        ))]);
        let mut messages = upstream_sse_message_stream(
            stream,
            Duration::from_secs(1),
            Duration::from_secs(1),
            1024,
        );

        let terminal = messages.next().await.unwrap().unwrap();
        assert_eq!(terminal.raw_event, Bytes::from_static(b"data: [DONE]\n\n"));
        assert_eq!(terminal.facts.terminal, Some(RelayOutcome::Complete));
        assert!(messages.next().await.is_none());
    }
}

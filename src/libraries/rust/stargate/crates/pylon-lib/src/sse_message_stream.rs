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

use crate::output_token_parser::OutputCharacters;

const SSE_DONE_SENTINEL: &str = "[DONE]";
const SSE_DELIVERY_BUFFER_EVENTS: usize = 1;

#[derive(Debug, Default, PartialEq, Eq)]
pub(crate) struct GeneratedOutput {
    pub(crate) characters: OutputCharacters,
    pub(crate) token_bearing: bool,
    pub(crate) reasoning_text: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct ExactUsage {
    pub(crate) input_tokens: Option<u64>,
    pub(crate) output_tokens: Option<u64>,
    pub(crate) reasoning_tokens: Option<u64>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct ChatChoiceCalibration {
    pub(crate) index: u64,
    pub(crate) safely_finished: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum SseEventProtocol {
    ChatCompletions,
    Responses,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum RelayOutcome {
    Complete,
    Failed,
}

#[derive(Debug, Default, PartialEq, Eq)]
pub(crate) struct SseEventFacts {
    pub(crate) protocol: Option<SseEventProtocol>,
    pub(crate) generated_output: Option<GeneratedOutput>,
    pub(crate) exact_usage: Option<ExactUsage>,
    pub(crate) calibration_ineligible: bool,
    pub(crate) terminal: Option<RelayOutcome>,
}

#[derive(Debug, PartialEq)]
pub(crate) struct ParsedSseMessage {
    pub(crate) raw_event: Bytes,
    pub(crate) parsed: Option<Value>,
    pub(crate) facts: SseEventFacts,
    pub(crate) received_at: Instant,
}

impl ParsedSseMessage {
    pub(crate) fn chat_choice_calibration(
        &self,
    ) -> impl Iterator<Item = ChatChoiceCalibration> + '_ {
        self.parsed
            .as_ref()
            .filter(|value| value["object"].as_str() == Some("chat.completion.chunk"))
            .and_then(|value| value["choices"].as_array())
            .into_iter()
            .flatten()
            .filter_map(|choice| {
                choice["index"].as_u64().map(|index| ChatChoiceCalibration {
                    index,
                    safely_finished: matches!(
                        choice["finish_reason"].as_str(),
                        Some("stop" | "length")
                    ),
                })
            })
    }
}

#[derive(Debug, Default)]
struct SseMessageBuffer {
    buffer: BytesMut,
}

impl SseMessageBuffer {
    fn push_bytes(&mut self, chunk: &[u8]) {
        self.buffer.extend_from_slice(chunk);
    }

    fn push_bounded_prefix(&mut self, chunk: &[u8], max_buffer_bytes: usize) -> usize {
        let buffer_limit = max_buffer_bytes.saturating_add(1);
        let bytes_to_push = chunk
            .len()
            .min(buffer_limit.saturating_sub(self.buffer.len()));
        self.push_bytes(&chunk[..bytes_to_push]);
        bytes_to_push
    }

    fn next_limited(&mut self, max_buffer_bytes: usize) -> Result<Option<ParsedSseMessage>, usize> {
        let Some(event_end) = find_sse_event_end(&self.buffer) else {
            return if self.buffer.len() > max_buffer_bytes {
                Err(self.buffer.len())
            } else {
                Ok(None)
            };
        };
        if event_end > max_buffer_bytes {
            return Err(event_end);
        }
        Ok(Some(self.pop_event(event_end)))
    }

    fn pop_event(&mut self, event_end: usize) -> ParsedSseMessage {
        let raw_event = self.buffer.split_to(event_end).freeze();
        let fields = extract_sse_fields(raw_event.as_ref());
        let (parsed, mut facts) = classify_sse_event(fields.event_name.as_deref(), &fields.data);
        facts.calibration_ineligible |= fields.conflicting_event_names;
        ParsedSseMessage {
            raw_event,
            parsed,
            facts,
            received_at: Instant::now(),
        }
    }
}

impl Iterator for SseMessageBuffer {
    type Item = ParsedSseMessage;

    fn next(&mut self) -> Option<Self::Item> {
        let event_end = find_sse_event_end(&self.buffer)?;
        Some(self.pop_event(event_end))
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
    #[error("upstream SSE event exceeded the {max_buffer_bytes}-byte buffer limit")]
    BufferLimitExceeded {
        max_buffer_bytes: usize,
        buffered_bytes: usize,
    },
    #[error("downstream SSE delivery remained backpressured through the {0:?} output deadline")]
    DeliveryBackpressure(SseReadTimeoutPhase),
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
                        let mut remaining = chunk.as_ref();
                        loop {
                            match sse_messages.next_limited(max_buffer_bytes) {
                                Ok(Some(mut message)) => {
                                    message.received_at = received_at.into_std();
                                    if message.facts.generated_output.is_some() {
                                        timeout_phase = SseReadTimeoutPhase::SubsequentOutput;
                                        deadline = received_at + output_chunk_timeout;
                                    }
                                    let terminal = message.facts.terminal.is_some();
                                    if !deliver_sse_message(
                                        &delivery_tx,
                                        message,
                                        deadline,
                                        timeout_phase,
                                    )
                                    .await
                                    {
                                        return;
                                    }
                                    if terminal {
                                        return;
                                    }
                                }
                                Ok(None) if remaining.is_empty() => break,
                                Ok(None) => {
                                    let pushed = sse_messages
                                        .push_bounded_prefix(remaining, max_buffer_bytes);
                                    debug_assert!(pushed > 0);
                                    remaining = &remaining[pushed..];
                                }
                                Err(buffered_bytes) => {
                                    let _ = delivery_tx
                                        .send(Err(UpstreamSseReadError::BufferLimitExceeded {
                                            max_buffer_bytes,
                                            buffered_bytes,
                                        }))
                                        .await;
                                    return;
                                }
                            }
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

async fn deliver_sse_message(
    delivery_tx: &tokio::sync::mpsc::Sender<Result<ParsedSseMessage, UpstreamSseReadError>>,
    message: ParsedSseMessage,
    deadline: tokio::time::Instant,
    timeout_phase: SseReadTimeoutPhase,
) -> bool {
    if let Ok(permit) = delivery_tx.try_reserve() {
        permit.send(Ok(message));
        return true;
    }
    if delivery_tx.is_closed() {
        return false;
    }

    let deadline_sleep = tokio::time::sleep_until(deadline);
    tokio::pin!(deadline_sleep);
    let permit = tokio::select! {
        biased;
        _ = &mut deadline_sleep => None,
        permit = delivery_tx.reserve() => match permit {
            Ok(permit) => Some(permit),
            Err(_) => return false,
        },
    };
    let Some(permit) = permit else {
        let _ = delivery_tx
            .send(Err(UpstreamSseReadError::DeliveryBackpressure(
                timeout_phase,
            )))
            .await;
        return false;
    };
    permit.send(Ok(message));
    true
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
                protocol: Some(SseEventProtocol::ChatCompletions),
                calibration_ineligible: event_name
                    .is_some_and(|event_name| event_name.starts_with("response.")),
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
                calibration_ineligible: !trimmed.is_empty(),
                terminal,
                ..SseEventFacts::default()
            },
        );
    };
    let json_event_type = value["type"].as_str();
    let event_type = json_event_type.or(event_name);
    let protocol = sse_event_protocol(value, event_type);
    let (generated_output, generated_output_ineligible) = generated_output(value, event_type);
    let (exact_usage, usage_ineligible) = exact_usage(value, protocol);
    let facts = SseEventFacts {
        protocol,
        generated_output,
        exact_usage,
        calibration_ineligible: generated_output_ineligible
            || usage_ineligible
            || calibration_ineligible_event(value, json_event_type, event_name),
        terminal: merge_terminal_outcomes(
            terminal_outcome(json_event_type),
            terminal_outcome(event_name),
        ),
    };
    (parsed, facts)
}

fn sse_event_protocol(value: &Value, event_type: Option<&str>) -> Option<SseEventProtocol> {
    if value["object"].as_str() == Some("chat.completion.chunk") {
        Some(SseEventProtocol::ChatCompletions)
    } else if event_type.is_some_and(|event_type| event_type.starts_with("response.")) {
        Some(SseEventProtocol::Responses)
    } else {
        None
    }
}

fn calibration_ineligible_event(
    value: &Value,
    json_event_type: Option<&str>,
    event_name: Option<&str>,
) -> bool {
    let is_chat_completion_chunk = value["object"].as_str() == Some("chat.completion.chunk");
    let identifiers_match = match (json_event_type, event_name) {
        (Some(json_event_type), Some(event_name)) => json_event_type == event_name,
        _ => true,
    };
    if !identifiers_match {
        return true;
    }

    if is_chat_completion_chunk {
        return json_event_type.is_some()
            || event_name.is_some_and(|event_name| !matches!(event_name, "chunk" | "message"))
            || value["choices"].as_array().is_none_or(|choices| {
                choices.iter().any(|choice| {
                    let finish_reason = &choice["finish_reason"];
                    let delta = &choice["delta"];
                    choice["index"].as_u64().is_none()
                        || (!finish_reason.is_null()
                            && !matches!(finish_reason.as_str(), Some("stop" | "length")))
                        || delta["function_call"].is_object()
                        || delta["tool_calls"]
                            .as_array()
                            .is_some_and(|calls| !calls.is_empty())
                        || chat_audio_is_calibration_ineligible(delta)
                })
            });
    }

    match json_event_type.or(event_name) {
        Some("response.created" | "response.queued" | "response.in_progress") => {
            !response_output_is_calibration_safe(value, true)
        }
        Some("response.output_item.added" | "response.output_item.done") => !matches!(
            value["item"]["type"].as_str(),
            Some("message" | "reasoning")
        ),
        Some("response.content_part.added" | "response.content_part.done") => !matches!(
            value["part"]["type"].as_str(),
            Some("output_text" | "refusal" | "reasoning_text")
        ),
        Some("response.output_text.delta") => value["delta"].as_str().is_none(),
        Some("response.output_text.done") => value["text"].as_str().is_none(),
        Some("response.refusal.delta") => value["delta"].as_str().is_none(),
        Some("response.refusal.done") => value["refusal"].as_str().is_none(),
        Some("response.reasoning_text.delta" | "response.audio.transcript.delta") => {
            value["delta"].as_str().is_none()
        }
        Some("response.reasoning_text.done") => value["text"].as_str().is_none(),
        Some("response.completed") => !response_output_is_calibration_safe(value, false),
        Some(_) | None => true,
    }
}

fn chat_audio_is_calibration_ineligible(delta: &Value) -> bool {
    let audio = &delta["audio"];
    if audio.is_null() {
        return false;
    }
    if audio.as_object().is_none() {
        return true;
    }

    let transcript_is_text = audio["transcript"].as_str().is_some();
    let data = &audio["data"];
    let contains_binary_data = match data.as_str() {
        Some(data) => !data.is_empty(),
        None => !data.is_null(),
    };
    !transcript_is_text || contains_binary_data
}

fn response_output_is_calibration_safe(value: &Value, allow_empty: bool) -> bool {
    value["response"]["output"].as_array().is_some_and(|items| {
        (allow_empty || !items.is_empty())
            && items
                .iter()
                .all(|item| matches!(item["type"].as_str(), Some("message" | "reasoning")))
    })
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum GeneratedValueKind {
    Text,
    ReasoningText,
    CalibrationIneligibleText,
    Modal,
}

#[derive(Debug, Clone, Copy)]
struct ChatOutputSpec {
    relative_path: &'static [&'static str],
    kind: GeneratedValueKind,
}

// Chat Completions | chat.completion.chunk | JSON path below | string
const CHAT_DELTA_TEXT_FIELDS: &[ChatOutputSpec] = &[
    ChatOutputSpec {
        relative_path: &["content"],
        kind: GeneratedValueKind::Text,
    },
    ChatOutputSpec {
        relative_path: &["refusal"],
        kind: GeneratedValueKind::Text,
    },
    ChatOutputSpec {
        relative_path: &["reasoning"],
        kind: GeneratedValueKind::ReasoningText,
    },
    ChatOutputSpec {
        relative_path: &["reasoning_content"],
        kind: GeneratedValueKind::ReasoningText,
    },
    ChatOutputSpec {
        relative_path: &["function_call", "name"],
        kind: GeneratedValueKind::CalibrationIneligibleText,
    },
    ChatOutputSpec {
        relative_path: &["function_call", "arguments"],
        kind: GeneratedValueKind::CalibrationIneligibleText,
    },
    ChatOutputSpec {
        relative_path: &["audio", "transcript"],
        kind: GeneratedValueKind::Text,
    },
];

// Chat Completions | chat.completion.chunk | JSON path below | string
const CHAT_TOOL_TEXT_FIELDS: &[&str] = &["name", "arguments"];

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
        kind: GeneratedValueKind::ReasoningText,
    },
    ResponsesOutputSpec {
        event_type: "response.reasoning_summary_text.delta",
        field: "delta",
        kind: GeneratedValueKind::CalibrationIneligibleText,
    },
    ResponsesOutputSpec {
        event_type: "response.function_call_arguments.delta",
        field: "delta",
        kind: GeneratedValueKind::CalibrationIneligibleText,
    },
    ResponsesOutputSpec {
        event_type: "response.mcp_call_arguments.delta",
        field: "delta",
        kind: GeneratedValueKind::CalibrationIneligibleText,
    },
    ResponsesOutputSpec {
        event_type: "response.custom_tool_call_input.delta",
        field: "delta",
        kind: GeneratedValueKind::CalibrationIneligibleText,
    },
    ResponsesOutputSpec {
        event_type: "response.code_interpreter_call_code.delta",
        field: "delta",
        kind: GeneratedValueKind::CalibrationIneligibleText,
    },
    ResponsesOutputSpec {
        event_type: "response.shell_call_command.added",
        field: "command",
        kind: GeneratedValueKind::CalibrationIneligibleText,
    },
    ResponsesOutputSpec {
        event_type: "response.shell_call_command.delta",
        field: "delta",
        kind: GeneratedValueKind::CalibrationIneligibleText,
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

fn generated_output(value: &Value, event_type: Option<&str>) -> (Option<GeneratedOutput>, bool) {
    let mut output = GeneratedOutput::default();
    let mut saw_generated_output = false;
    let mut calibration_ineligible = false;

    if value["object"].as_str() == Some("chat.completion.chunk")
        && let Some(choices) = value["choices"].as_array()
    {
        for choice in choices {
            let delta = &choice["delta"];
            for spec in CHAT_DELTA_TEXT_FIELDS {
                add_generated_text(
                    &mut output,
                    &mut saw_generated_output,
                    &mut calibration_ineligible,
                    string_at_path(delta, spec.relative_path),
                    spec.kind,
                );
            }
            if delta["audio"]["data"]
                .as_str()
                .is_some_and(|value| !value.is_empty())
            {
                saw_generated_output = true;
                calibration_ineligible = true;
            }
            if let Some(tool_calls) = delta["tool_calls"].as_array() {
                for tool_call in tool_calls {
                    for &field in CHAT_TOOL_TEXT_FIELDS {
                        add_generated_text(
                            &mut output,
                            &mut saw_generated_output,
                            &mut calibration_ineligible,
                            tool_call["function"][field].as_str(),
                            GeneratedValueKind::CalibrationIneligibleText,
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
            GeneratedValueKind::Text
            | GeneratedValueKind::ReasoningText
            | GeneratedValueKind::CalibrationIneligibleText => {
                output.token_bearing = true;
                output.reasoning_text |= spec.kind == GeneratedValueKind::ReasoningText;
                calibration_ineligible |=
                    spec.kind == GeneratedValueKind::CalibrationIneligibleText;
                output.characters = output
                    .characters
                    .saturating_add(OutputCharacters::from_text(fragment));
            }
            GeneratedValueKind::Modal => calibration_ineligible = true,
        }
    }

    (
        saw_generated_output.then_some(output),
        calibration_ineligible,
    )
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
    calibration_ineligible: &mut bool,
    value: Option<&str>,
    kind: GeneratedValueKind,
) {
    if let Some(value) = value.filter(|value| !value.is_empty()) {
        *saw_generated_output = true;
        output.token_bearing = true;
        output.reasoning_text |= kind == GeneratedValueKind::ReasoningText;
        *calibration_ineligible |= kind == GeneratedValueKind::CalibrationIneligibleText;
        output.characters = output
            .characters
            .saturating_add(OutputCharacters::from_text(value));
    }
}

fn exact_usage(value: &Value, protocol: Option<SseEventProtocol>) -> (Option<ExactUsage>, bool) {
    let usage = &value["usage"];
    let response_usage = &value["response"]["usage"];
    let completion_details = &usage["completion_tokens_details"];
    let output_details = &response_usage["output_tokens_details"];
    let (input_tokens, invalid_input_tokens) =
        merge_u64_fields([&usage["prompt_tokens"], &response_usage["input_tokens"]]);
    let (output_tokens, invalid_output_tokens) = merge_u64_fields([
        &usage["completion_tokens"],
        &response_usage["output_tokens"],
        &value["output_tokens_so_far"],
    ]);
    let (reasoning_tokens, invalid_reasoning_tokens) = merge_u64_fields([
        &completion_details["reasoning_tokens"],
        &output_details["reasoning_tokens"],
    ]);
    let (audio_tokens, invalid_audio_tokens) = merge_u64_fields([
        &completion_details["audio_tokens"],
        &output_details["audio_tokens"],
    ]);
    let (rejected_prediction_tokens, invalid_rejected_prediction_tokens) = merge_u64_fields([
        &completion_details["rejected_prediction_tokens"],
        &output_details["rejected_prediction_tokens"],
    ]);
    let invalid_container = [usage, response_usage, completion_details, output_details]
        .into_iter()
        .any(|value| !value.is_null() && value.as_object().is_none());
    let chat_usage_observed = !usage.is_null() || !value["output_tokens_so_far"].is_null();
    let responses_usage_observed = !response_usage.is_null();
    let usage_protocol_mismatch = match protocol {
        Some(SseEventProtocol::ChatCompletions) => responses_usage_observed,
        Some(SseEventProtocol::Responses) => chat_usage_observed,
        None => chat_usage_observed || responses_usage_observed,
    };
    let calibration_ineligible = usage_protocol_mismatch
        || invalid_container
        || invalid_input_tokens
        || invalid_output_tokens
        || invalid_reasoning_tokens
        || invalid_audio_tokens
        || invalid_rejected_prediction_tokens
        || audio_tokens.is_some_and(|tokens| tokens > 0)
        || rejected_prediction_tokens.is_some_and(|tokens| tokens > 0);
    (
        (input_tokens.is_some() || output_tokens.is_some() || reasoning_tokens.is_some())
            .then_some(ExactUsage {
                input_tokens,
                output_tokens,
                reasoning_tokens,
            }),
        calibration_ineligible,
    )
}

fn merge_u64_fields<const N: usize>(values: [&Value; N]) -> (Option<u64>, bool) {
    let mut merged = None;
    let mut invalid = false;
    for value in values {
        if value.is_null() {
            continue;
        }
        let Some(value) = value.as_u64() else {
            invalid = true;
            continue;
        };
        invalid |= merged.is_some_and(|merged| merged != value);
        merged.get_or_insert(value);
    }
    (merged, invalid)
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
    conflicting_event_names: bool,
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
            Some(("event", rest)) => {
                let event_name = rest.trim_start();
                fields.conflicting_event_names |= fields
                    .event_name
                    .as_deref()
                    .is_some_and(|previous| previous != event_name);
                fields.event_name = Some(event_name.to_string());
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
        assert!(first.facts.calibration_ineligible);
        assert_eq!(messages.next(), None);

        messages.push_bytes(b"ond\n\n");
        let second = messages
            .next()
            .expect("second event should now be complete");
        assert_eq!(second.raw_event, Bytes::from_static(b"data: second\n\n"));
        assert!(second.facts.calibration_ineligible);
        assert_eq!(messages.next(), None);
    }

    #[test]
    fn classifies_every_chat_generated_string_path() {
        for spec in CHAT_DELTA_TEXT_FIELDS {
            let json_path = format!("choices[].delta.{}", spec.relative_path.join("."));
            let delta = match spec.relative_path {
                [field] => serde_json::json!({(*field): "x"}),
                [container, field] => {
                    serde_json::json!({(*container): {(*field): "x"}})
                }
                _ => panic!("unsupported test path {json_path}"),
            };
            let data = serde_json::json!({
                "object": "chat.completion.chunk",
                "choices": [{"index": 0, "delta": delta}],
            });
            let parsed = parse_data(&data.to_string());
            let output = parsed
                .facts
                .generated_output
                .unwrap_or_else(|| panic!("{json_path} should count as generated output"));
            assert!(output.token_bearing, "{json_path} should be token-bearing");
            assert_eq!(
                output.characters,
                OutputCharacters::from_text("x"),
                "unexpected character count for {json_path}"
            );
            assert_eq!(
                output.reasoning_text,
                spec.kind == GeneratedValueKind::ReasoningText
            );
            assert_eq!(
                parsed.facts.calibration_ineligible,
                spec.kind == GeneratedValueKind::CalibrationIneligibleText
            );
        }

        for &field in CHAT_TOOL_TEXT_FIELDS {
            let json_path = format!("choices[].delta.tool_calls[].function.{field}");
            let data = serde_json::json!({
                "object": "chat.completion.chunk",
                "choices": [{"index": 0, "delta": {"tool_calls": [{"function": {(field): "x"}}]}}],
            });
            let parsed = parse_data(&data.to_string());
            let output = parsed
                .facts
                .generated_output
                .unwrap_or_else(|| panic!("{json_path} should count as generated output"));
            assert!(output.token_bearing, "{json_path} should be token-bearing");
            assert_eq!(
                output.characters,
                OutputCharacters::from_text("x"),
                "unexpected character count for {json_path}"
            );
            assert!(parsed.facts.calibration_ineligible);
            assert!(!output.reasoning_text);
        }
    }

    #[test]
    fn chat_mixed_channels_collect_all_generated_fragments() {
        let parsed = parse_data(
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"content":"answer","refusal":"refuse","reasoning":"think","reasoning_content":"think-more","function_call":{"name":"legacy","arguments":"{}"},"tool_calls":[{"function":{"name":"tool","arguments":"[]"}}],"audio":{"transcript":"spoken"}}}]}"#,
        );
        let output = parsed
            .facts
            .generated_output
            .expect("mixed Chat event should count");
        assert_eq!(
            output.characters,
            OutputCharacters {
                ascii: 47,
                non_ascii: 0,
            }
        );
        assert!(parsed.facts.calibration_ineligible);
        assert!(output.reasoning_text);
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
        assert_eq!(output.characters, OutputCharacters::default());
        assert!(parsed.facts.calibration_ineligible);
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
        assert_eq!(output.characters, OutputCharacters::from_text("spoken"));
        assert!(parsed.facts.calibration_ineligible);
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
                GeneratedValueKind::Text
                | GeneratedValueKind::ReasoningText
                | GeneratedValueKind::CalibrationIneligibleText => {
                    assert!(output.token_bearing);
                    assert_eq!(output.characters, OutputCharacters::from_text("x"));
                    assert_eq!(
                        output.reasoning_text,
                        spec.kind == GeneratedValueKind::ReasoningText
                    );
                    assert_eq!(
                        parsed.facts.calibration_ineligible,
                        !matches!(
                            spec.event_type,
                            "response.output_text.delta"
                                | "response.refusal.delta"
                                | "response.reasoning_text.delta"
                                | "response.audio.transcript.delta"
                        )
                    );
                }
                GeneratedValueKind::Modal => {
                    assert!(!output.token_bearing);
                    assert_eq!(output.characters, OutputCharacters::default());
                    assert!(parsed.facts.calibration_ineligible);
                }
            }
        }
    }

    #[test]
    fn tool_lifecycle_events_are_calibration_ineligible_without_generated_output() {
        for data in [
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function"}]}}]}"#,
            r#"{"object":"chat.completion.chunk","choices":[{"delta":{"audio":{"id":"audio-1"}}}]}"#,
            r#"{"type":"response.web_search_call.searching"}"#,
            r#"{"type":"response.function_call_arguments.done","arguments":""}"#,
            r#"{"type":"response.output_item.added","item":{"type":"function_call"}}"#,
            r#"{"type":"response.output_item.done","item":{"type":"program"}}"#,
            r#"{"type":"response.output_item.added","item":{}}"#,
            r#"{"type":"response.output_item.done","item":{"type":7}}"#,
            r#"{"type":"response.reasoning_summary_part.added"}"#,
        ] {
            let facts = parse_data(data).facts;
            assert_eq!(
                facts.generated_output, None,
                "metadata must not count as generated output: {data}"
            );
            assert!(
                facts.calibration_ineligible,
                "tool or non-visible output must reject calibration: {data}"
            );
        }

        let message = r#"{"type":"response.output_item.added","item":{"type":"message"}}"#;
        assert!(!parse_data(message).facts.calibration_ineligible);
        let reasoning = r#"{"type":"response.output_item.added","item":{"type":"reasoning"}}"#;
        assert!(!parse_data(reasoning).facts.calibration_ineligible);
        let reasoning_part =
            r#"{"type":"response.content_part.added","part":{"type":"reasoning_text"}}"#;
        assert!(!parse_data(reasoning_part).facts.calibration_ineligible);

        let matching_message = parse_event(
            "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\"}}\n\n",
        );
        assert!(!matching_message.facts.calibration_ineligible);

        for mismatched_event in [
            "event: response.output_text.delta\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\"}}\n\n",
            "event: response.output_item.added\ndata: {\"type\":\"response.output_text.delta\",\"item\":{\"type\":\"message\"}}\n\n",
        ] {
            assert!(parse_event(mismatched_event).facts.calibration_ineligible);
        }
    }

    #[test]
    fn unsafe_or_unknown_chat_finish_reasons_are_calibration_ineligible() {
        for finish_reason in [
            serde_json::json!("content_filter"),
            serde_json::json!("tool_calls"),
            serde_json::json!("function_call"),
            serde_json::json!("future_reason"),
            serde_json::json!(7),
        ] {
            let event = serde_json::json!({
                "object": "chat.completion.chunk",
                "choices": [{
                    "index": 0,
                    "delta": {"content": "visible"},
                    "finish_reason": finish_reason,
                }],
            });
            assert!(
                parse_data(&event.to_string()).facts.calibration_ineligible,
                "unsafe finish reason must fail closed: {event}"
            );
        }

        for finish_reason in [serde_json::json!("stop"), serde_json::json!("length")] {
            let event = serde_json::json!({
                "object": "chat.completion.chunk",
                "choices": [{"index": 0, "delta": {}, "finish_reason": finish_reason}],
            });
            assert!(!parse_data(&event.to_string()).facts.calibration_ineligible);
        }
    }

    #[test]
    fn terminal_response_output_items_gate_calibration() {
        for item_type in ["message", "reasoning"] {
            let event = serde_json::json!({
                "type": "response.completed",
                "response": {
                    "output": [{"type": item_type}],
                    "usage": {"output_tokens": 2},
                },
            });
            assert!(!parse_data(&event.to_string()).facts.calibration_ineligible);
        }

        for data in [
            r#"{"type":"response.completed","response":{"output":[{"type":"function_call"}],"usage":{"output_tokens":2}}}"#,
            r#"{"type":"response.completed","response":{"output":[{}],"usage":{"output_tokens":2}}}"#,
            r#"{"type":"response.completed","response":{"output":[],"usage":{"output_tokens":2}}}"#,
            r#"{"type":"response.completed","response":{"output":"invalid","usage":{"output_tokens":2}}}"#,
            r#"{"type":"response.completed","response":{"usage":{"output_tokens":2}}}"#,
        ] {
            assert!(
                parse_data(data).facts.calibration_ineligible,
                "terminal output must fail closed: {data}"
            );
        }
    }

    #[test]
    fn conflicting_event_identifiers_are_calibration_ineligible() {
        for event in [
            "event: response.content_part.done\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"text\"}\n\n",
            "event: response.output_text.delta\ndata: {\"type\":\"response.content_part.done\",\"delta\":\"text\"}\n\n",
        ] {
            assert!(parse_event(event).facts.calibration_ineligible);
        }
    }

    #[test]
    fn duplicate_event_fields_use_the_last_identity_and_fail_closed_on_conflict() {
        let raw_event = "event: response.reasoning_text.delta\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"visible\"}\n\n";
        let fields = extract_sse_fields(raw_event.as_bytes());
        assert_eq!(
            fields.event_name.as_deref(),
            Some("response.output_text.delta")
        );
        assert!(fields.conflicting_event_names);

        let parsed = parse_event(raw_event);
        assert!(parsed.facts.generated_output.is_some());
        assert!(parsed.facts.calibration_ineligible);

        let repeated = parse_event(
            "event: response.output_text.delta\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"visible\"}\n\n",
        );
        assert!(!repeated.facts.calibration_ineligible);
    }

    #[test]
    fn untyped_non_chat_and_malformed_events_are_calibration_ineligible() {
        for data in [
            r#"{"item":{"type":"reasoning"}}"#,
            r#"{"item":{"type":"function_call"}}"#,
            r#"{"audio":{"data":"YWJj"}}"#,
            r#"{"type":"response.future.delta","delta":"unknown"}"#,
            "not-json",
            "{",
        ] {
            assert!(
                parse_data(data).facts.calibration_ineligible,
                "ambiguous event must fail closed: {data}"
            );
        }

        let chat = parse_data(
            r#"{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"text"}}]}"#,
        );
        assert!(!chat.facts.calibration_ineligible);
    }

    #[test]
    fn non_empty_whitespace_text_is_token_bearing_and_counted() {
        let parsed = parse_data(
            r#"{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" "}}]}"#,
        );
        let output = parsed
            .facts
            .generated_output
            .expect("non-empty text delta should count as generated output");

        assert!(output.token_bearing);
        assert_eq!(output.characters, OutputCharacters::from_text(" "));
        assert!(!parsed.facts.calibration_ineligible);
        assert!(!output.reasoning_text);
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
        for (data, expected, calibration_ineligible) in [
            (
                r#"{"object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":3,"completion_tokens_details":{"reasoning_tokens":2}}}"#,
                ExactUsage {
                    input_tokens: Some(8),
                    output_tokens: Some(3),
                    reasoning_tokens: Some(2),
                },
                false,
            ),
            (
                r#"{"type":"response.completed","response":{"output":[{"type":"message"}],"usage":{"input_tokens":5,"output_tokens":2,"output_tokens_details":{"reasoning_tokens":1}}}}"#,
                ExactUsage {
                    input_tokens: Some(5),
                    output_tokens: Some(2),
                    reasoning_tokens: Some(1),
                },
                false,
            ),
            (
                r#"{"object":"chat.completion.chunk","output_tokens_so_far":7,"choices":[]}"#,
                ExactUsage {
                    input_tokens: None,
                    output_tokens: Some(7),
                    reasoning_tokens: None,
                },
                false,
            ),
            (
                r#"{"object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":8,"completion_tokens_details":{"reasoning_tokens":2,"rejected_prediction_tokens":1}}}"#,
                ExactUsage {
                    input_tokens: Some(8),
                    output_tokens: None,
                    reasoning_tokens: Some(2),
                },
                true,
            ),
        ] {
            let parsed = parse_data(data);
            assert_eq!(parsed.facts.exact_usage, Some(expected));
            assert_eq!(parsed.facts.generated_output, None);
            assert_eq!(parsed.facts.calibration_ineligible, calibration_ineligible);
        }
    }

    #[test]
    fn non_visible_completion_details_are_calibration_ineligible() {
        for field in ["audio_tokens", "rejected_prediction_tokens"] {
            let data = serde_json::json!({
                "object": "chat.completion.chunk",
                "choices": [],
                "usage": {
                    "completion_tokens": 5,
                    "completion_tokens_details": {(field): 1},
                },
            });
            assert!(
                parse_data(&data.to_string()).facts.calibration_ineligible,
                "{field} should make calibration ineligible"
            );
        }
    }

    #[test]
    fn ignores_missing_and_null_usage() {
        for data in [
            r#"{"object":"chat.completion.chunk"}"#,
            r#"{"object":"chat.completion.chunk","usage":null}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":null}}"#,
            "not-json",
        ] {
            assert_eq!(parse_data(data).facts.exact_usage, None);
        }
    }

    #[test]
    fn malformed_or_conflicting_usage_is_calibration_ineligible() {
        for data in [
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":"4"}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":4.5}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":-1}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":"2"}}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":5,"completion_tokens_details":{"audio_tokens":"1"}}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":5,"completion_tokens_details":"invalid"}}"#,
            r#"{"object":"chat.completion.chunk","usage":{"completion_tokens":5},"response":{"usage":{"output_tokens":6}}}"#,
            r#"{"object":"chat.completion.chunk","choices":[],"response":{"usage":{"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0}}}}"#,
            r#"{"type":"response.completed","usage":{"completion_tokens":5},"response":{"output":[{"type":"message"}]}}"#,
            r#"{"type":"response.completed","response":{"output":[{"type":"message"}],"usage":{"output_tokens":5,"output_tokens_details":"invalid"}}}"#,
        ] {
            assert!(
                parse_data(data).facts.calibration_ineligible,
                "malformed or conflicting usage must fail closed: {data}"
            );
        }
    }

    #[test]
    fn unavailable_reasoning_detail_remains_calibration_eligible() {
        for usage in [
            r#"{"output_tokens":5}"#,
            r#"{"output_tokens":5,"output_tokens_details":null}"#,
            r#"{"output_tokens":5,"output_tokens_details":{}}"#,
            r#"{"output_tokens":5,"output_tokens_details":{"reasoning_tokens":null}}"#,
        ] {
            let data = format!(
                r#"{{"type":"response.completed","response":{{"output":[{{"type":"message"}}],"usage":{usage}}}}}"#
            );
            let parsed = parse_data(&data);
            assert_eq!(
                parsed.facts.exact_usage,
                Some(ExactUsage {
                    input_tokens: None,
                    output_tokens: Some(5),
                    reasoning_tokens: None,
                })
            );
            assert!(!parsed.facts.calibration_ineligible);
        }
    }

    #[test]
    fn classifies_explicit_terminal_outcomes_without_output() {
        let done = parse_data("[DONE]");
        assert_eq!(done.facts.terminal, Some(RelayOutcome::Complete));
        assert_eq!(done.facts.protocol, Some(SseEventProtocol::ChatCompletions));
        let completed = parse_data(r#"{"type":"response.completed"}"#);
        assert_eq!(completed.facts.terminal, Some(RelayOutcome::Complete));
        assert_eq!(completed.facts.protocol, Some(SseEventProtocol::Responses));
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

        let responses_done = parse_event("event: response.completed\ndata: [DONE]\n\n");
        assert_eq!(
            responses_done.facts.terminal,
            Some(RelayOutcome::Complete)
        );
        assert!(responses_done.facts.calibration_ineligible);
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
                assert_eq!(buffered_bytes, 12);
            }
            unexpected => panic!(
                "an upstream peer must not keep an unterminated SSE event buffered indefinitely: {unexpected:?}"
            ),
        }
    }

    #[tokio::test]
    async fn rejects_a_complete_event_that_exceeds_the_buffer_limit() {
        let byte_stream = futures::stream::iter([Ok::<_, reqwest::Error>(Bytes::from_static(
            b"data: 123456\n\n",
        ))]);
        let mut messages = upstream_sse_message_stream(
            byte_stream,
            Duration::from_secs(1),
            Duration::from_secs(1),
            13,
        );

        assert!(matches!(
            messages.next().await,
            Some(Err(UpstreamSseReadError::BufferLimitExceeded {
                max_buffer_bytes: 13,
                buffered_bytes: 14,
            }))
        ));
    }

    #[tokio::test]
    async fn streams_many_small_events_from_a_chunk_larger_than_the_buffer_limit() {
        let event_count = 128;
        let chunk = (0..event_count)
            .map(|index| format!("data: {index}\n\n"))
            .collect::<String>();
        let byte_stream = futures::stream::iter([Ok::<_, reqwest::Error>(Bytes::from(chunk))]);
        let mut messages = upstream_sse_message_stream(
            byte_stream,
            Duration::from_secs(1),
            Duration::from_secs(1),
            11,
        );

        let mut received = 0;
        while let Some(message) = messages.next().await {
            let message = message.expect("complete small SSE events should not fail");
            assert!(message.raw_event.len() <= 11);
            assert!(message.facts.calibration_ineligible);
            received += 1;
        }
        assert_eq!(received, event_count);
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
    async fn delivery_backpressure_is_not_reported_as_an_upstream_timeout() {
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

        let second = messages.next().await.unwrap().unwrap();
        assert!(second.facts.calibration_ineligible);
        assert_eq!(second.received_at, received_at);
        assert!(matches!(
            messages.next().await,
            Some(Err(UpstreamSseReadError::DeliveryBackpressure(
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

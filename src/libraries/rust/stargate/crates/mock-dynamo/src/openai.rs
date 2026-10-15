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

use axum::Json;
use axum::extract::State;
use axum::http::{HeaderMap, StatusCode};
use axum::response::sse::{Event, KeepAlive, Sse};
use axum::response::{IntoResponse, Response};
use serde::{Deserialize, Serialize};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tokio::sync::OwnedSemaphorePermit;
use tracing::info;

use crate::AppState;
use crate::kv_cache::{KvCacheAccess, KvCacheStats, insert_kv_cache_headers};
use crate::stats_stream::StatsStreamEvent;
use crate::test_control::{TestEndpoint, TestRequestClass, request_class};
use crate::timing::{
    embedding_item_count, jitter_ms, non_streaming_delay, optional_header, prefill_delay,
    request_embedding_tokens, request_input_tokens, request_output_tokens, response_input_tokens,
    response_output_tokens, token_delay,
};

#[derive(Serialize)]
pub(crate) struct ModelList {
    object: &'static str,
    data: Vec<ModelListEntry>,
}

#[derive(Serialize)]
struct ModelListEntry {
    id: String,
    object: &'static str,
}

pub(crate) async fn list_models(State(state): State<AppState>) -> Json<ModelList> {
    Json(ModelList {
        object: "list",
        data: state
            .test_control
            .record_model_discovery_request()
            .await
            .into_iter()
            .map(|id| ModelListEntry {
                id,
                object: "model",
            })
            .collect(),
    })
}

#[rustfmt::skip]
const DUMMY_TOKENS: &[&str] = &[
    "Hello", ",", " how", " can", " I", " help", " you", " today", "?", " I", " am", " a",
    " helpful", " AI", " assistant", ".", " Let", " me", " know", " what", " you", " need", ".",
    " I", "'m", " here", " to", " assist", " you", "!",
];

#[derive(Deserialize)]
pub(crate) struct ChatRequest {
    pub(crate) stream: Option<bool>,
    pub(crate) model: Option<String>,
    pub(crate) max_tokens: Option<usize>,
    #[serde(default)]
    pub(crate) messages: Vec<serde_json::Value>,
}

#[derive(Deserialize)]
pub(crate) struct ResponsesRequest {
    pub(crate) stream: Option<bool>,
    pub(crate) model: Option<String>,
    pub(crate) max_output_tokens: Option<usize>,
    pub(crate) input: Option<serde_json::Value>,
}

#[derive(Deserialize)]
pub(crate) struct EmbeddingsRequest {
    input: serde_json::Value,
    model: Option<String>,
    encoding_format: Option<EmbeddingEncodingFormat>,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub(crate) enum EmbeddingEncodingFormat {
    Float,
    Base64,
}

#[derive(Serialize)]
struct ChatCompletionChunk<'a> {
    id: &'a str,
    object: &'static str,
    model: &'a str,
    choices: [ChunkChoice<'a>; 1],
}

#[derive(Serialize)]
struct ChunkChoice<'a> {
    index: u8,
    delta: Delta<'a>,
    finish_reason: Option<&'static str>,
}

#[derive(Serialize)]
struct Delta<'a> {
    #[serde(skip_serializing_if = "Option::is_none")]
    role: Option<&'static str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    content: Option<&'a str>,
}

#[derive(Serialize)]
struct ChatCompletion<'a> {
    id: &'a str,
    object: &'static str,
    model: &'a str,
    choices: [ChatCompletionChoice<'a>; 1],
    usage: ChatUsage,
}

#[derive(Serialize)]
struct ChatCompletionChoice<'a> {
    index: u8,
    message: AssistantMessage<'a>,
    finish_reason: &'static str,
}

#[derive(Serialize)]
struct AssistantMessage<'a> {
    role: &'static str,
    content: &'a str,
}

#[derive(Serialize)]
struct ChatUsage {
    prompt_tokens: usize,
    completion_tokens: usize,
    total_tokens: usize,
}

#[derive(Serialize)]
#[serde(untagged)]
pub(crate) enum EmbeddingValue {
    Float([f32; 3]),
    Base64(&'static str),
}

struct StreamResponseConfig {
    state: AppState,
    model: String,
    id: String,
    request_id: String,
    input_tokens: usize,
    first_token_delay: Duration,
    output_tokens: usize,
    kv_cache_access: KvCacheAccess,
    request_slot: Option<OwnedSemaphorePermit>,
    kind: StreamKind,
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum StreamKind {
    Chat,
    Responses { created_at: u64 },
}

pub(crate) async fn chat_completions(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(mut req): Json<ChatRequest>,
) -> Response {
    let model = req.model.take().unwrap_or_else(|| state.model_name.clone());
    state
        .record_request(&headers, TestEndpoint::ChatCompletions, &model)
        .await;
    if state.test_control.chat_failure_enabled(&model).await {
        return error_response(
            StatusCode::SERVICE_UNAVAILABLE,
            serde_json::json!({
                "message": format!("mock chat failure enabled for model {model}"),
            }),
        );
    }
    let request_slot = state.acquire_request_slot().await;
    let input_tokens = request_input_tokens(&headers, &req);
    let output_tokens = request_output_tokens(&headers, &req, state.num_tokens);
    let stream = req.stream == Some(true);
    let id = format!("chatcmpl-mock-{}", rand_id());
    info!(id = %id, model = %model, stream = stream, "received chat/completions request");
    let request_id = optional_header(&headers, "x-request-id").unwrap_or_else(|| id.clone());
    let cache_affinity_key = optional_header(&headers, "x-cache-affinity-key");
    if stream {
        state.emit_counters(&request_id, &model, 0, 0, false);
    }
    let kv_cache_access = state
        .process_input_with_cache(cache_affinity_key.as_deref(), input_tokens)
        .await;
    let first_token_delay =
        state.ttft + Duration::from_millis(jitter_ms(&request_id, "ttft", state.ttft_jitter_ms));
    info!(
        id = %id,
        cache_affinity_key = ?cache_affinity_key,
        kv_cache_hit = kv_cache_access.hit,
        kv_cache_evicted_entries = kv_cache_access.evicted_entries,
        kv_cache_evicted_tokens = kv_cache_access.evicted_tokens,
        input_tokens = input_tokens,
        "computed mock request timing"
    );

    if stream {
        info!(id = %id, status = 200, "responding with SSE stream");
        return stream_response(StreamResponseConfig {
            state,
            model,
            id,
            request_id,
            input_tokens,
            first_token_delay,
            output_tokens,
            kv_cache_access,
            request_slot,
            kind: StreamKind::Chat,
        });
    }

    tokio::time::sleep(non_streaming_delay(
        &state,
        &request_id,
        first_token_delay,
        output_tokens,
    ))
    .await;

    let content: String = DUMMY_TOKENS
        .iter()
        .cycle()
        .take(output_tokens)
        .copied()
        .collect();

    info!(id = %id, status = 200, "responding with JSON");
    state.emit_counters(&request_id, &model, input_tokens, output_tokens, true);
    let mut response = Json(ChatCompletion {
        id: &id,
        object: "chat.completion",
        model: &model,
        choices: [ChatCompletionChoice {
            index: 0,
            message: AssistantMessage {
                role: "assistant",
                content: &content,
            },
            finish_reason: "stop",
        }],
        usage: ChatUsage {
            prompt_tokens: input_tokens,
            completion_tokens: output_tokens,
            total_tokens: input_tokens + output_tokens,
        },
    })
    .into_response();
    insert_kv_cache_headers(response.headers_mut(), kv_cache_access);
    response
}

pub(crate) async fn responses(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(mut req): Json<ResponsesRequest>,
) -> Response {
    if req.stream != Some(true) {
        return error_response(
            StatusCode::BAD_REQUEST,
            "mock-dynamo /v1/responses requires stream=true",
        );
    }

    let model = req.model.take().unwrap_or_else(|| state.model_name.clone());
    state
        .record_request(&headers, TestEndpoint::Responses, &model)
        .await;
    let request_slot = state.acquire_request_slot().await;
    let input_tokens = response_input_tokens(&headers, &req);
    let output_tokens = response_output_tokens(&headers, &req, state.num_tokens);
    let id = format!("resp-mock-{}", rand_id());
    info!(id = %id, model = %model, "received responses request");
    let request_id = optional_header(&headers, "x-request-id").unwrap_or_default();
    let cache_affinity_key = optional_header(&headers, "x-cache-affinity-key");
    state.emit_counters(&request_id, &model, 0, 0, false);
    let kv_cache_access = state
        .process_input_with_cache(cache_affinity_key.as_deref(), input_tokens)
        .await;
    let first_token_delay =
        state.ttft + Duration::from_millis(jitter_ms(&request_id, "ttft", state.ttft_jitter_ms));

    stream_response(StreamResponseConfig {
        state,
        model,
        id,
        request_id,
        input_tokens,
        first_token_delay,
        output_tokens,
        kv_cache_access,
        request_slot,
        kind: StreamKind::Responses {
            created_at: current_unix_timestamp(),
        },
    })
}

fn responses_sse_event<T: Serialize>(event_name: &'static str, value: &T) -> Event {
    Event::default()
        .event(event_name)
        .data(serde_json::to_string(value).expect("response stream event should serialize"))
}

pub(crate) async fn embeddings(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(mut req): Json<EmbeddingsRequest>,
) -> Response {
    let model = req.model.take().unwrap_or_else(|| state.model_name.clone());
    state
        .record_request(&headers, TestEndpoint::Embeddings, &model)
        .await;
    let _request_slot = state.acquire_request_slot().await;
    let item_count = embedding_item_count(&req.input);
    let prompt_tokens = request_embedding_tokens(&headers, &req.input);
    let encoding_format = req
        .encoding_format
        .unwrap_or(EmbeddingEncodingFormat::Float);
    let id = format!("embd-mock-{}", rand_id());
    let request_id = optional_header(&headers, "x-request-id").unwrap_or_else(|| id.clone());
    info!(
        id = %id,
        model = %model,
        item_count = item_count,
        prompt_tokens = prompt_tokens,
        encoding_format = ?encoding_format,
        "received embeddings request"
    );

    let data: Vec<_> = (0..item_count)
        .map(|index| {
            serde_json::json!({
                "object": "embedding",
                "embedding": deterministic_embedding_value(index, encoding_format),
                "index": index,
            })
        })
        .collect();

    state.emit_counters(&request_id, &model, prompt_tokens, None, true);

    Json(serde_json::json!({
        "object": "list",
        "data": data,
        "model": model,
        "usage": {
            "prompt_tokens": prompt_tokens,
            "total_tokens": prompt_tokens,
        },
    }))
    .into_response()
}

pub(crate) async fn health(State(state): State<AppState>) -> &'static str {
    if !state.health_delay.is_zero() {
        tokio::time::sleep(state.health_delay).await;
    }
    "ok"
}

pub(crate) async fn kv_cache_stats(State(state): State<AppState>) -> Json<KvCacheStats> {
    Json(state.kv_cache.lock().await.stats(&state.model_name))
}

impl AppState {
    async fn process_input_with_cache(
        &self,
        cache_affinity_key: Option<&str>,
        input_tokens: usize,
    ) -> KvCacheAccess {
        let access = self
            .kv_cache
            .lock()
            .await
            .access(cache_affinity_key, input_tokens);
        tokio::time::sleep(prefill_delay(
            access.uncached_input_tokens as usize,
            self.prefill_tokens_per_s,
        ))
        .await;
        let commit = self
            .kv_cache
            .lock()
            .await
            .commit(cache_affinity_key, input_tokens);
        access.with_commit(commit)
    }

    async fn acquire_request_slot(&self) -> Option<OwnedSemaphorePermit> {
        self.request_slots.clone()?.acquire_owned().await.ok()
    }

    async fn record_request(&self, headers: &HeaderMap, endpoint: TestEndpoint, model: &str) {
        let request_class = request_class(headers);
        self.test_control
            .record_request(endpoint, model, request_class)
            .await;
        if request_class == TestRequestClass::PylonGenerated {
            self.test_control.wait_for_bringup_release(model).await;
        }
    }

    fn emit_counters(
        &self,
        request_id: &str,
        model: &str,
        input_tokens: usize,
        output_tokens: impl Into<Option<usize>>,
        finished: bool,
    ) {
        let _ = self.stats_events.send(StatsStreamEvent::Stats {
            v: 1,
            request_id: request_id.to_string(),
            model: model.to_string(),
            tokens_processed: Some(input_tokens as u64),
            tokens_generated: output_tokens.into().map(|tokens| tokens as u64),
            finished,
        });
    }
}

fn error_response(status: StatusCode, error: impl Serialize) -> Response {
    (status, Json(serde_json::json!({ "error": error }))).into_response()
}

pub(crate) enum ChatStreamChunk<'a> {
    Role,
    Content(&'a str),
    Stop,
}

pub(crate) fn chat_chunk_json(id: &str, model: &str, chunk: ChatStreamChunk<'_>) -> String {
    let (role, content, finish_reason) = match chunk {
        ChatStreamChunk::Role => (Some("assistant"), None, None),
        ChatStreamChunk::Content(content) => (None, Some(content), None),
        ChatStreamChunk::Stop => (None, None, Some("stop")),
    };
    serde_json::to_string(&ChatCompletionChunk {
        id,
        object: "chat.completion.chunk",
        model,
        choices: [ChunkChoice {
            index: 0,
            delta: Delta { role, content },
            finish_reason,
        }],
    })
    .expect("chat stream event should serialize")
}

fn chat_sse_event(id: &str, model: &str, chunk: ChatStreamChunk<'_>) -> Event {
    Event::default().data(chat_chunk_json(id, model, chunk))
}

fn stream_response(config: StreamResponseConfig) -> Response {
    let StreamResponseConfig {
        state,
        model,
        id,
        request_id,
        input_tokens,
        first_token_delay,
        output_tokens,
        kv_cache_access,
        request_slot,
        kind,
    } = config;
    let stream = async_stream::stream! {
        let _request_slot = request_slot;
        let mut output_text = String::new();
        if let StreamKind::Responses { created_at } = kind {
            yield Ok::<_, std::convert::Infallible>(responses_sse_event(
                "response.created",
                &serde_json::json!({
                    "type": "response.created",
                    "response": {
                        "id": id.as_str(),
                        "object": "response",
                        "created_at": created_at,
                        "status": "in_progress",
                        "model": model.as_str(),
                        "output": [],
                        "usage": null,
                    },
                }),
            ));
        }
        tokio::time::sleep(first_token_delay).await;

        state.emit_counters(&request_id, &model, input_tokens, 0, false);

        if kind == StreamKind::Chat {
            yield Ok(chat_sse_event(&id, &model, ChatStreamChunk::Role));
        }

        for i in 0..output_tokens {
            if i > 0 {
                tokio::time::sleep(token_delay(&state, &request_id, i)).await;
            }
            let token = DUMMY_TOKENS[i % DUMMY_TOKENS.len()];
            let event = match kind {
                StreamKind::Chat => chat_sse_event(&id, &model, ChatStreamChunk::Content(token)),
                StreamKind::Responses { .. } => {
                    output_text.push_str(token);
                    responses_sse_event(
                        "response.output_text.delta",
                        &serde_json::json!({
                            "type": "response.output_text.delta",
                            "response_id": id.as_str(),
                            "output_index": 0,
                            "content_index": 0,
                            "delta": token,
                        }),
                    )
                }
            };
            yield Ok(event);
            state.emit_counters(&request_id, &model, input_tokens, i + 1, false);
        }

        let completed = match kind {
            StreamKind::Chat => chat_sse_event(&id, &model, ChatStreamChunk::Stop),
            StreamKind::Responses { created_at } => responses_sse_event(
                "response.completed",
                &serde_json::json!({
                    "type": "response.completed",
                    "response": {
                        "id": id.as_str(),
                        "object": "response",
                        "created_at": created_at,
                        "status": "completed",
                        "model": model.as_str(),
                        "output": [{
                            "id": format!("msg-{id}"),
                            "type": "message",
                            "status": "completed",
                            "role": "assistant",
                            "content": [{
                                "type": "output_text",
                                "text": output_text,
                                "annotations": [],
                            }],
                        }],
                        "usage": {
                            "input_tokens": input_tokens,
                            "output_tokens": output_tokens,
                            "total_tokens": input_tokens + output_tokens,
                        },
                    },
                }),
            ),
        };
        yield Ok(completed);

        state.emit_counters(&request_id, &model, input_tokens, output_tokens, true);

        if kind == StreamKind::Chat {
            yield Ok(Event::default().data("[DONE]"));
        }
    };

    let mut response = Sse::new(stream)
        .keep_alive(KeepAlive::default())
        .into_response();
    insert_kv_cache_headers(response.headers_mut(), kv_cache_access);
    response
}

pub(crate) fn deterministic_embedding_value(
    index: usize,
    format: EmbeddingEncodingFormat,
) -> EmbeddingValue {
    match format {
        EmbeddingEncodingFormat::Float => {
            EmbeddingValue::Float([index as f32, index as f32 + 0.125, -(index as f32) - 0.25])
        }
        EmbeddingEncodingFormat::Base64 => EmbeddingValue::Base64("AAAAAAAAAAA="),
    }
}

fn time_since_epoch() -> Duration {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
}

fn rand_id() -> String {
    format!("{:x}", time_since_epoch().as_nanos())
}

fn current_unix_timestamp() -> u64 {
    time_since_epoch().as_secs()
}

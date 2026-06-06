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
use crate::stats_stream::emit_request_stats_event;
use crate::timing::{
    embedding_item_count, jitter_ms, non_streaming_delay, optional_header, prefill_delay,
    request_embedding_tokens, request_input_tokens, request_output_tokens, response_input_tokens,
    response_output_tokens, token_delay,
};

const DUMMY_TOKENS: &[&str] = &[
    "Hello",
    ",",
    " how",
    " can",
    " I",
    " help",
    " you",
    " today",
    "?",
    " I",
    " am",
    " a",
    " helpful",
    " AI",
    " assistant",
    ".",
    " Let",
    " me",
    " know",
    " what",
    " you",
    " need",
    ".",
    " I",
    "'m",
    " here",
    " to",
    " assist",
    " you",
    "!",
];

#[derive(Deserialize)]
pub(crate) struct ChatRequest {
    #[serde(default)]
    pub(crate) stream: Option<bool>,
    #[serde(default)]
    pub(crate) model: Option<String>,
    #[serde(default)]
    pub(crate) max_tokens: Option<usize>,
    #[serde(default)]
    pub(crate) messages: Vec<serde_json::Value>,
}

#[derive(Deserialize)]
pub(crate) struct ResponsesRequest {
    #[serde(default)]
    pub(crate) stream: Option<bool>,
    #[serde(default)]
    pub(crate) model: Option<String>,
    #[serde(default)]
    pub(crate) max_output_tokens: Option<usize>,
    #[serde(default)]
    pub(crate) input: Option<serde_json::Value>,
}

#[derive(Deserialize)]
pub(crate) struct EmbeddingsRequest {
    input: serde_json::Value,
    #[serde(default)]
    model: Option<String>,
    #[serde(default)]
    encoding_format: Option<EmbeddingEncodingFormat>,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub(crate) enum EmbeddingEncodingFormat {
    Float,
    Base64,
}

#[derive(Serialize)]
struct ChatCompletionChunk {
    id: String,
    object: &'static str,
    model: String,
    choices: Vec<ChunkChoice>,
    #[serde(skip_serializing_if = "Option::is_none")]
    usage: Option<Usage>,
}

#[derive(Serialize)]
struct ChunkChoice {
    index: u32,
    delta: Delta,
    finish_reason: Option<&'static str>,
}

#[derive(Serialize)]
struct Delta {
    #[serde(skip_serializing_if = "Option::is_none")]
    role: Option<&'static str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    content: Option<String>,
}

#[derive(Serialize)]
struct ChatCompletion {
    id: String,
    object: &'static str,
    model: String,
    choices: Vec<NonStreamChoice>,
    usage: Usage,
}

#[derive(Serialize)]
struct NonStreamChoice {
    index: u32,
    message: Message,
    finish_reason: &'static str,
}

#[derive(Serialize)]
struct Message {
    role: &'static str,
    content: String,
}

#[derive(Serialize)]
struct ResponsesApiResponse {
    id: String,
    object: &'static str,
    created_at: u64,
    status: &'static str,
    model: String,
    output: Vec<ResponseOutputMessage>,
    usage: ResponsesUsage,
}

#[derive(Serialize)]
struct ResponseOutputMessage {
    id: String,
    r#type: &'static str,
    status: &'static str,
    role: &'static str,
    content: Vec<ResponseOutputContent>,
}

#[derive(Serialize)]
struct ResponseOutputContent {
    r#type: &'static str,
    text: String,
    annotations: Vec<serde_json::Value>,
}

#[derive(Serialize)]
struct ResponsesUsage {
    input_tokens: usize,
    output_tokens: usize,
    total_tokens: usize,
}

#[derive(Serialize)]
struct Usage {
    prompt_tokens: usize,
    completion_tokens: usize,
    total_tokens: usize,
}

#[derive(Serialize)]
struct EmbeddingsResponse {
    object: &'static str,
    data: Vec<EmbeddingItem>,
    model: String,
    usage: EmbeddingsUsage,
}

#[derive(Serialize)]
struct EmbeddingItem {
    object: &'static str,
    embedding: EmbeddingValue,
    index: usize,
}

#[derive(Serialize)]
#[serde(untagged)]
pub(crate) enum EmbeddingValue {
    Float(Vec<f32>),
    Base64(&'static str),
}

#[derive(Serialize)]
struct EmbeddingsUsage {
    prompt_tokens: usize,
    total_tokens: usize,
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
}

struct ResponsesStreamConfig {
    state: AppState,
    model: String,
    id: String,
    request_id: String,
    input_tokens: usize,
    first_token_delay: Duration,
    output_tokens: usize,
    kv_cache_access: KvCacheAccess,
    request_slot: Option<OwnedSemaphorePermit>,
}

pub(crate) async fn chat_completions(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(req): Json<ChatRequest>,
) -> Response {
    let request_slot = acquire_request_slot(&state).await;
    let input_tokens = request_input_tokens(&headers, &req);
    let output_tokens = request_output_tokens(&headers, &req, state.num_tokens);
    let model = req.model.clone().unwrap_or(state.model_name.clone());
    let stream = req.stream == Some(true);
    let id = format!("chatcmpl-mock-{}", rand_id());
    info!(id = %id, model = %model, stream = stream, "received chat/completions request");
    let request_id = optional_header(&headers, "x-request-id").unwrap_or_else(|| id.clone());
    let cache_affinity_key = optional_header(&headers, "x-cache-affinity-key");
    if stream {
        emit_request_stats_event(&state, &request_id, &model, Some(0), Some(0), false);
    }
    let kv_cache_access =
        process_input_with_cache(&state, cache_affinity_key.as_deref(), input_tokens).await;
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
        })
        .await;
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
    emit_request_stats_event(
        &state,
        &request_id,
        &model,
        Some(input_tokens),
        Some(output_tokens),
        true,
    );
    let mut response = Json(ChatCompletion {
        id,
        object: "chat.completion",
        model,
        choices: vec![NonStreamChoice {
            index: 0,
            message: Message {
                role: "assistant",
                content,
            },
            finish_reason: "stop",
        }],
        usage: Usage {
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
    Json(req): Json<ResponsesRequest>,
) -> Response {
    if req.stream != Some(true) {
        return (
            StatusCode::BAD_REQUEST,
            Json(serde_json::json!({
                "error": "mock-dynamo /v1/responses requires stream=true",
            })),
        )
            .into_response();
    }

    let request_slot = acquire_request_slot(&state).await;
    let input_tokens = response_input_tokens(&headers, &req);
    let output_tokens = response_output_tokens(&headers, &req, state.num_tokens);
    let model = req.model.clone().unwrap_or(state.model_name.clone());
    let id = format!("resp-mock-{}", rand_id());
    info!(id = %id, model = %model, "received responses request");
    let request_id = headers
        .get("x-request-id")
        .and_then(|value| value.to_str().ok())
        .unwrap_or("");
    let cache_affinity_key = optional_header(&headers, "x-cache-affinity-key");
    emit_request_stats_event(&state, request_id, &model, Some(0), Some(0), false);
    let kv_cache_access =
        process_input_with_cache(&state, cache_affinity_key.as_deref(), input_tokens).await;
    let first_token_delay =
        state.ttft + Duration::from_millis(jitter_ms(request_id, "ttft", state.ttft_jitter_ms));

    stream_responses_response(ResponsesStreamConfig {
        state,
        model,
        id,
        request_id: request_id.to_string(),
        input_tokens,
        first_token_delay,
        output_tokens,
        kv_cache_access,
        request_slot,
    })
    .await
}

async fn stream_responses_response(config: ResponsesStreamConfig) -> Response {
    let ResponsesStreamConfig {
        state,
        model,
        id,
        request_id,
        input_tokens,
        first_token_delay,
        output_tokens,
        kv_cache_access,
        request_slot,
    } = config;
    let created_at = current_unix_timestamp();
    let stream = async_stream::stream! {
        let _request_slot = request_slot;
        let mut output_text = String::new();
        yield Ok::<_, std::convert::Infallible>(responses_sse_event(
            "response.created",
            &serde_json::json!({
                "type": "response.created",
                "response": {
                    "id": id.clone(),
                    "object": "response",
                    "created_at": created_at,
                    "status": "in_progress",
                    "model": model.clone(),
                    "output": [],
                    "usage": null,
                },
            }),
        ));

        tokio::time::sleep(first_token_delay).await;
        emit_request_stats_event(&state, &request_id, &model, Some(input_tokens), Some(0), false);

        for i in 0..output_tokens {
            if i > 0 {
                tokio::time::sleep(token_delay(&state, &request_id, i)).await;
            }
            let token = DUMMY_TOKENS[i % DUMMY_TOKENS.len()];
            output_text.push_str(token);
            yield Ok(responses_sse_event(
                "response.output_text.delta",
                &serde_json::json!({
                    "type": "response.output_text.delta",
                    "response_id": id.clone(),
                    "output_index": 0,
                    "content_index": 0,
                    "delta": token,
                }),
            ));
            emit_request_stats_event(
                &state,
                &request_id,
                &model,
                Some(input_tokens),
                Some(i + 1),
                false,
            );
        }

        yield Ok(responses_sse_event(
            "response.completed",
            &serde_json::json!({
                "type": "response.completed",
                "response": ResponsesApiResponse {
                    id: id.clone(),
                    object: "response",
                    created_at,
                    status: "completed",
                    model: model.clone(),
                    output: vec![ResponseOutputMessage {
                        id: format!("msg-{id}"),
                        r#type: "message",
                        status: "completed",
                        role: "assistant",
                        content: vec![ResponseOutputContent {
                            r#type: "output_text",
                            text: output_text,
                            annotations: Vec::new(),
                        }],
                    }],
                    usage: ResponsesUsage {
                        input_tokens,
                        output_tokens,
                        total_tokens: input_tokens + output_tokens,
                    },
                },
            }),
        ));
        emit_request_stats_event(
            &state,
            &request_id,
            &model,
            Some(input_tokens),
            Some(output_tokens),
            true,
        );
    };

    let mut response = Sse::new(stream)
        .keep_alive(KeepAlive::default())
        .into_response();
    insert_kv_cache_headers(response.headers_mut(), kv_cache_access);
    response
}

fn responses_sse_event<T: Serialize>(event_name: &'static str, value: &T) -> Event {
    Event::default()
        .event(event_name)
        .data(serde_json::to_string(value).expect("response stream event should serialize"))
}

pub(crate) async fn embeddings(
    State(state): State<AppState>,
    headers: HeaderMap,
    Json(req): Json<EmbeddingsRequest>,
) -> Response {
    let _request_slot = acquire_request_slot(&state).await;
    let item_count = embedding_item_count(&req.input);
    let prompt_tokens = request_embedding_tokens(&headers, &req.input);
    let model = req.model.clone().unwrap_or(state.model_name.clone());
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

    let data = (0..item_count)
        .map(|index| EmbeddingItem {
            object: "embedding",
            embedding: deterministic_embedding_value(index, encoding_format),
            index,
        })
        .collect();

    emit_request_stats_event(&state, &request_id, &model, Some(prompt_tokens), None, true);

    Json(EmbeddingsResponse {
        object: "list",
        data,
        model,
        usage: EmbeddingsUsage {
            prompt_tokens,
            total_tokens: prompt_tokens,
        },
    })
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

async fn process_input_with_cache(
    state: &AppState,
    cache_affinity_key: Option<&str>,
    input_tokens: usize,
) -> KvCacheAccess {
    let access = state
        .kv_cache
        .lock()
        .await
        .access(cache_affinity_key, input_tokens);
    let prefill = prefill_delay(
        access.uncached_input_tokens as usize,
        state.prefill_tokens_per_s,
    );
    tokio::time::sleep(prefill).await;
    access.with_commit(
        state
            .kv_cache
            .lock()
            .await
            .commit(cache_affinity_key, input_tokens),
    )
}

async fn stream_response(config: StreamResponseConfig) -> Response {
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
    } = config;
    let stream = async_stream::stream! {
        let _request_slot = request_slot;
        tokio::time::sleep(first_token_delay).await;

        emit_request_stats_event(&state, &request_id, &model, Some(input_tokens), Some(0), false);

        yield Ok::<_, std::convert::Infallible>(Event::default().data(
            serde_json::to_string(&ChatCompletionChunk {
                id: id.clone(),
                object: "chat.completion.chunk",
                model: model.clone(),
                choices: vec![ChunkChoice {
                    index: 0,
                    delta: Delta {
                        role: Some("assistant"),
                        content: None,
                    },
                    finish_reason: None,
                }],
                usage: None,
            })
            .unwrap(),
        ));

        for i in 0..output_tokens {
            if i > 0 {
                tokio::time::sleep(token_delay(&state, &request_id, i)).await;
            }
            let token = DUMMY_TOKENS[i % DUMMY_TOKENS.len()];
            yield Ok(Event::default().data(
                serde_json::to_string(&ChatCompletionChunk {
                    id: id.clone(),
                    object: "chat.completion.chunk",
                    model: model.clone(),
                    choices: vec![ChunkChoice {
                        index: 0,
                        delta: Delta {
                            role: None,
                            content: Some(token.to_string()),
                        },
                        finish_reason: None,
                    }],
                    usage: None,
                })
                .unwrap(),
            ));
            emit_request_stats_event(
                &state,
                &request_id,
                &model,
                Some(input_tokens),
                Some(i + 1),
                false,
            );
        }

        yield Ok(Event::default().data(
            serde_json::to_string(&ChatCompletionChunk {
                id: id.clone(),
                object: "chat.completion.chunk",
                model: model.clone(),
                choices: vec![ChunkChoice {
                    index: 0,
                    delta: Delta {
                        role: None,
                        content: None,
                    },
                    finish_reason: Some("stop"),
                }],
                usage: None,
            })
            .unwrap(),
        ));

        emit_request_stats_event(
            &state,
            &request_id,
            &model,
            Some(input_tokens),
            Some(output_tokens),
            true,
        );

        yield Ok(Event::default().data("[DONE]"));
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
        EmbeddingEncodingFormat::Float => EmbeddingValue::Float(vec![
            index as f32,
            index as f32 + 0.125,
            -(index as f32) - 0.25,
        ]),
        EmbeddingEncodingFormat::Base64 => EmbeddingValue::Base64("AAAAAAAAAAA="),
    }
}

fn rand_id() -> String {
    let t = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos();
    format!("{t:x}")
}

fn current_unix_timestamp() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
}

async fn acquire_request_slot(state: &AppState) -> Option<OwnedSemaphorePermit> {
    match &state.request_slots {
        Some(slots) => slots.clone().acquire_owned().await.ok(),
        None => None,
    }
}

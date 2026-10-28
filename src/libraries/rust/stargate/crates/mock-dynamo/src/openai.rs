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
use axum::body::Bytes;
use axum::extract::{RawQuery, State};
use axum::http::{HeaderMap, StatusCode};
use axum::response::sse::{Event, KeepAlive, Sse};
use axum::response::{IntoResponse, Response};
use serde::{Deserialize, Serialize};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tokio::sync::OwnedSemaphorePermit;
use tracing::info;

use crate::AppState;
use crate::engine_driver::EngineRequest;
use crate::kv_cache::{KvCacheAccess, KvCacheStats, insert_kv_cache_headers};
use crate::stats_stream::StatsStreamEvent;
use crate::test_control::{TestEndpoint, TestRequestClass, is_canary_request, request_class};
use crate::timing::{
    bounded_output_tokens, embedding_item_count, jitter_ms, non_streaming_delay, optional_header,
    prefill_delay, request_embedding_tokens, request_input_tokens, response_input_tokens,
    select_output_tokens, token_delay,
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
const CANARY_ANSWER: &str = "2";

#[derive(Deserialize)]
pub(crate) struct ChatRequest {
    pub(crate) stream: Option<bool>,
    stream_options: Option<ChatStreamOptions>,
    pub(crate) model: Option<String>,
    pub(crate) max_tokens: Option<usize>,
    #[serde(default)]
    pub(crate) messages: Vec<serde_json::Value>,
}

#[derive(Deserialize)]
struct ChatStreamOptions {
    #[serde(default)]
    include_usage: bool,
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
    choices: &'a [ChunkChoice<'a>],
    // Omitted unless requested, then null until the final usage chunk.
    #[serde(skip_serializing_if = "Option::is_none")]
    usage: Option<Option<ChatUsage>>,
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
    output_tokens: usize,
    kv_cache_access: KvCacheAccess,
    pacing: TokenPacing,
    kind: StreamKind,
}

/// Output timing for a request whose input has been processed.
enum TokenPacing {
    /// Legacy fixed per-request delays. The concurrency permit is held until
    /// the response ends.
    Fixed {
        first_token_delay: Duration,
        _request_slot: Option<OwnedSemaphorePermit>,
    },
    /// Token events from the shared batched engine, which has already
    /// produced the first token.
    Engine(EngineRequest),
}

impl TokenPacing {
    async fn first_token(&self) {
        if let Self::Fixed {
            first_token_delay, ..
        } = self
        {
            tokio::time::sleep(*first_token_delay).await;
        }
    }

    async fn next_token(&mut self, state: &AppState, request_id: &str, token_index: usize) {
        match self {
            Self::Fixed { .. } => {
                tokio::time::sleep(token_delay(state, request_id, token_index)).await;
            }
            Self::Engine(request) => request.next_token().await,
        }
    }

    async fn whole_response(&mut self, state: &AppState, request_id: &str, output_tokens: usize) {
        match self {
            Self::Fixed {
                first_token_delay, ..
            } => {
                tokio::time::sleep(non_streaming_delay(
                    state,
                    request_id,
                    *first_token_delay,
                    output_tokens,
                ))
                .await;
            }
            Self::Engine(request) => request.completion().await,
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum StreamKind {
    Chat { canary: bool, include_usage: bool },
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
    let input_tokens = request_input_tokens(&headers, &req);
    let canary = is_canary_request(&headers);
    let id = format!("chatcmpl-mock-{}", rand_id());
    let request_id = optional_header(&headers, "x-request-id").unwrap_or_else(|| id.clone());
    let selected_output_tokens = if canary {
        1
    } else {
        select_output_tokens(&headers, &request_id, state.output_tokens, req.max_tokens)
    };
    let Some(output_tokens) = bounded_output_tokens(
        input_tokens,
        selected_output_tokens,
        state.context_length_tokens,
    ) else {
        return error_response(
            StatusCode::BAD_REQUEST,
            format!(
                "input token count {input_tokens} leaves no output capacity within the context length of {} tokens",
                state.context_length_tokens
            ),
        );
    };
    let stream = req.stream == Some(true);
    info!(id = %id, model = %model, stream = stream, "received chat/completions request");
    let cache_affinity_key = optional_header(&headers, "x-cache-affinity-key");
    let (kv_cache_access, mut pacing) = state
        .prepare_request(
            &model,
            &request_id,
            cache_affinity_key.as_deref(),
            input_tokens,
            output_tokens,
        )
        .await;
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
            output_tokens,
            kv_cache_access,
            pacing,
            kind: StreamKind::Chat {
                canary,
                include_usage: req
                    .stream_options
                    .as_ref()
                    .is_some_and(|options| options.include_usage),
            },
        });
    }

    pacing
        .whole_response(&state, &request_id, output_tokens)
        .await;

    let content = if canary {
        CANARY_ANSWER.to_string()
    } else {
        DUMMY_TOKENS
            .iter()
            .cycle()
            .take(output_tokens)
            .copied()
            .collect()
    };

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
            total_tokens: input_tokens.saturating_add(output_tokens),
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
    let input_tokens = response_input_tokens(&headers, &req);
    let id = format!("resp-mock-{}", rand_id());
    let request_id = optional_header(&headers, "x-request-id").unwrap_or_else(|| id.clone());
    let selected_output_tokens = select_output_tokens(
        &headers,
        &request_id,
        state.output_tokens,
        req.max_output_tokens,
    );
    let Some(output_tokens) = bounded_output_tokens(
        input_tokens,
        selected_output_tokens,
        state.context_length_tokens,
    ) else {
        return error_response(
            StatusCode::BAD_REQUEST,
            format!(
                "input token count {input_tokens} leaves no output capacity within the context length of {} tokens",
                state.context_length_tokens
            ),
        );
    };
    info!(id = %id, model = %model, "received responses request");
    let cache_affinity_key = optional_header(&headers, "x-cache-affinity-key");
    let (kv_cache_access, pacing) = state
        .prepare_request(
            &model,
            &request_id,
            cache_affinity_key.as_deref(),
            input_tokens,
            output_tokens,
        )
        .await;

    stream_response(StreamResponseConfig {
        state,
        model,
        id,
        request_id,
        input_tokens,
        output_tokens,
        kv_cache_access,
        pacing,
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

const DECISION_ECHO_HEADERS: [&str; 5] = [
    "typesafe-danger-zone-images",
    "x-model",
    "x-input-tokens",
    "x-request-id",
    "x-vendor-test",
];
const DECISION_MOCK_PROBABILITY: f64 = 0.99;
const DECISION_MOCK_RETRY_AFTER_SECONDS: &str = "7";

#[derive(Clone, Copy, PartialEq, Eq)]
enum DecisionRoute {
    Decisions,
    Systemone,
}

/// Canned OpenAI Decisions response. The body is not validated, so tests can
/// send large or loosely shaped payloads.
pub(crate) async fn decisions(
    State(state): State<AppState>,
    headers: HeaderMap,
    RawQuery(query): RawQuery,
    body: Bytes,
) -> Response {
    decision_response(state, headers, query, body, DecisionRoute::Decisions).await
}

/// Canned System One response.
pub(crate) async fn systemone(
    State(state): State<AppState>,
    headers: HeaderMap,
    RawQuery(query): RawQuery,
    body: Bytes,
) -> Response {
    decision_response(state, headers, query, body, DecisionRoute::Systemone).await
}

async fn decision_response(
    state: AppState,
    headers: HeaderMap,
    query: Option<String>,
    body: Bytes,
    route: DecisionRoute,
) -> Response {
    let request: serde_json::Value = serde_json::from_slice(&body).unwrap_or_default();
    let model = request
        .get("model")
        .and_then(serde_json::Value::as_str)
        .map_or_else(|| state.model_name.clone(), str::to_string);
    let endpoint = match route {
        DecisionRoute::Decisions => TestEndpoint::Decisions,
        DecisionRoute::Systemone => TestEndpoint::Systemone,
    };
    state.record_request(&headers, endpoint, &model).await;
    let _request_slot = state.acquire_request_slot().await;
    let input_tokens = optional_header(&headers, "x-input-tokens")
        .and_then(|value| value.parse::<usize>().ok())
        .unwrap_or_else(|| (body.len() / 4).max(1));
    let output_tokens = 1;
    let request_id = optional_header(&headers, "x-request-id")
        .unwrap_or_else(|| format!("decision-mock-{}", rand_id()));
    info!(
        request_id = %request_id,
        model = %model,
        input_tokens = input_tokens,
        body_len = body.len(),
        "received decision request"
    );
    state.emit_counters(&request_id, &model, input_tokens, output_tokens, true);

    let mock_error = query
        .as_deref()
        .into_iter()
        .flat_map(|query| query.split('&'))
        .find_map(|pair| pair.strip_prefix("mock_error="));
    match mock_error {
        Some("429") => {
            let mut response = decision_error(route, StatusCode::TOO_MANY_REQUESTS);
            response.headers_mut().insert(
                axum::http::header::RETRY_AFTER,
                axum::http::HeaderValue::from_static(DECISION_MOCK_RETRY_AFTER_SECONDS),
            );
            return response;
        }
        Some("400") => return decision_error(route, StatusCode::BAD_REQUEST),
        _ => {}
    }

    let echo: serde_json::Map<String, serde_json::Value> = DECISION_ECHO_HEADERS
        .iter()
        .map(|name| ((*name).to_string(), optional_header(&headers, name).into()))
        .chain([("body_len".to_string(), body.len().into())])
        .collect();
    let payload = match route {
        DecisionRoute::Decisions => {
            let answers: Vec<_> = request
                .get("questions")
                .and_then(serde_json::Value::as_array)
                .map(|questions| {
                    questions
                        .iter()
                        .enumerate()
                        .map(|(index, question)| decisions_answer(index, question))
                        .collect()
                })
                .unwrap_or_default();
            serde_json::json!({
                "model": model,
                "answers": answers,
                "usage": {
                    "input_tokens": input_tokens,
                    "input_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 0},
                    "output_tokens": output_tokens,
                    "output_tokens_details": {"reasoning_tokens": 0},
                    "total_tokens": input_tokens + output_tokens,
                },
                "echo": echo,
            })
        }
        DecisionRoute::Systemone => {
            let answers: serde_json::Map<String, serde_json::Value> = request
                .get("questions")
                .and_then(serde_json::Value::as_object)
                .map(|questions| {
                    questions
                        .iter()
                        .map(|(name, question)| (name.clone(), systemone_answer(question)))
                        .collect()
                })
                .unwrap_or_default();
            serde_json::json!({
                "model": model,
                "answers": answers,
                "usage": {"input_tokens": input_tokens, "output_tokens": output_tokens},
                "echo": echo,
            })
        }
    };
    let mut response = Json(payload).into_response();
    if let Some(value) = headers.get("typesafe-danger-zone-images") {
        response
            .headers_mut()
            .insert("x-mock-echo-typesafe-danger-zone-images", value.clone());
    }
    response
}

fn question_type(question: &serde_json::Value) -> &str {
    question
        .get("type")
        .and_then(serde_json::Value::as_str)
        .unwrap_or_default()
}

/// Probabilities for `count` options: the first gets the mock probability and
/// the rest share the remainder.
fn option_probabilities(count: usize) -> Vec<f64> {
    let rest = 0.01 / count.saturating_sub(1).max(1) as f64;
    (0..count)
        .map(|index| {
            if index == 0 {
                DECISION_MOCK_PROBABILITY
            } else {
                rest
            }
        })
        .collect()
}

fn mock_box() -> serde_json::Value {
    serde_json::json!({"top": 0.1, "bottom": 0.9, "left": 0.1, "right": 0.9})
}

/// Canned answer for one Decisions question. Questions are read loosely so an
/// unexpected field never fails the request; unknown types get the predicate
/// answer.
fn decisions_answer(index: usize, question: &serde_json::Value) -> serde_json::Value {
    let kind = question_type(question);
    let kind = if kind.is_empty() { "predicate" } else { kind };
    let name = question
        .get("name")
        .and_then(serde_json::Value::as_str)
        .map_or_else(|| format!("q{index}"), str::to_string);
    let options = |key: &str| -> Vec<serde_json::Value> {
        question
            .get(key)
            .and_then(serde_json::Value::as_array)
            .cloned()
            .unwrap_or_default()
    };
    match kind {
        "choice" => {
            let values: Vec<_> = options("choices")
                .into_iter()
                .map(|choice| choice.get("value").cloned().unwrap_or(choice))
                .collect();
            let probabilities = option_probabilities(values.len());
            serde_json::json!({
                "type": kind,
                "name": name,
                "choice": values.first().cloned().unwrap_or(serde_json::Value::Null),
                "probabilities": values.iter().zip(&probabilities)
                    .map(|(value, probability)| serde_json::json!({"value": value, "probability": probability}))
                    .collect::<Vec<_>>(),
                "confidence": DECISION_MOCK_PROBABILITY,
            })
        }
        "score" => {
            let labels: Vec<_> = options("levels")
                .into_iter()
                .map(|level| level.get("label").cloned().unwrap_or(level))
                .collect();
            let probabilities = option_probabilities(labels.len());
            serde_json::json!({
                "type": kind,
                "name": name,
                "score": 0,
                "probabilities": labels.iter().zip(&probabilities).enumerate()
                    .map(|(value, (label, probability))| serde_json::json!({"value": value, "label": label, "probability": probability}))
                    .collect::<Vec<_>>(),
                "confidence": DECISION_MOCK_PROBABILITY,
            })
        }
        "box" => serde_json::json!({
            "type": kind,
            "name": name,
            "box": mock_box(),
            "confidence": DECISION_MOCK_PROBABILITY,
        }),
        _ => serde_json::json!({
            "type": kind,
            "name": name,
            "probability": DECISION_MOCK_PROBABILITY,
        }),
    }
}

/// Canned answer for one System One question, keyed by the caller. Unknown
/// types get the noul answer.
fn systemone_answer(question: &serde_json::Value) -> serde_json::Value {
    let criteria = question.get("criteria");
    match question_type(question) {
        "choice" => {
            let keys: Vec<String> = match criteria {
                Some(serde_json::Value::Object(map)) => map.keys().cloned().collect(),
                Some(serde_json::Value::Array(items)) => items
                    .iter()
                    .filter_map(|item| item.as_str().map(str::to_string))
                    .collect(),
                _ => Vec::new(),
            };
            let probabilities = option_probabilities(keys.len());
            serde_json::json!({
                "type": "choice",
                "choice": keys.first().cloned().unwrap_or_default(),
                "confidence": DECISION_MOCK_PROBABILITY,
                "probabilities": keys.iter().zip(&probabilities)
                    .map(|(key, probability)| (key.clone(), serde_json::json!(probability)))
                    .collect::<serde_json::Map<_, _>>(),
            })
        }
        "score" => {
            let labels: Vec<serde_json::Value> = criteria
                .and_then(serde_json::Value::as_array)
                .cloned()
                .unwrap_or_default();
            let probabilities = option_probabilities(labels.len());
            serde_json::json!({
                "type": "score",
                "score": 0,
                "confidence": DECISION_MOCK_PROBABILITY,
                "legend": labels.iter().enumerate()
                    .map(|(index, label)| (index.to_string(), label.clone()))
                    .collect::<serde_json::Map<_, _>>(),
                "probabilities": probabilities.iter().enumerate()
                    .map(|(index, probability)| (index.to_string(), serde_json::json!(probability)))
                    .collect::<serde_json::Map<_, _>>(),
            })
        }
        "box" => serde_json::json!({
            "type": "box",
            "box": mock_box(),
            "confidence": DECISION_MOCK_PROBABILITY,
        }),
        _ => serde_json::json!({"type": "noul", "noul": DECISION_MOCK_PROBABILITY}),
    }
}

/// Error bodies follow each route's native shape: OpenAI style for decisions,
/// FastAPI style for System One.
fn decision_error(route: DecisionRoute, status: StatusCode) -> Response {
    let rate_limited = status == StatusCode::TOO_MANY_REQUESTS;
    match route {
        DecisionRoute::Decisions if rate_limited => error_response(
            status,
            serde_json::json!({
                "message": "Rate limit exceeded",
                "type": "rate_limit_error",
                "param": null,
                "code": "rate_limit_exceeded",
            }),
        ),
        DecisionRoute::Decisions => error_response(
            status,
            serde_json::json!({
                "message": "Invalid question",
                "type": "invalid_request_error",
                "param": "questions",
                "code": "invalid_question",
            }),
        ),
        DecisionRoute::Systemone if rate_limited => (
            status,
            Json(serde_json::json!({"detail": "Rate limit exceeded"})),
        )
            .into_response(),
        DecisionRoute::Systemone => (
            status,
            Json(serde_json::json!({"detail": [{
                "loc": ["body", "questions"],
                "msg": "Invalid question",
                "type": "value_error",
            }]})),
        )
            .into_response(),
    }
}

pub(crate) async fn health(State(state): State<AppState>) -> &'static str {
    if !state.health_delay.is_zero() {
        tokio::time::sleep(state.health_delay).await;
    }
    "ok"
}

pub(crate) async fn kv_cache_stats(State(state): State<AppState>) -> Json<KvCacheStats> {
    if let Some(engine) = &state.engine {
        let workers = engine.worker_stats().await;
        return Json(KvCacheStats::from_engine_workers(
            &state.model_name,
            &workers,
        ));
    }
    Json(state.kv_cache.lock().await.stats(&state.model_name))
}

impl AppState {
    /// Processes the input and returns its cache access and output timing.
    /// Stats counters report zero progress before input processing and the
    /// processed input afterwards.
    async fn prepare_request(
        &self,
        model: &str,
        request_id: &str,
        cache_affinity_key: Option<&str>,
        input_tokens: usize,
        output_tokens: usize,
    ) -> (KvCacheAccess, TokenPacing) {
        if let Some(engine) = &self.engine {
            let mut request = engine.submit(cache_affinity_key, input_tokens, output_tokens);
            self.emit_counters(request_id, model, 0, 0, false);
            // The engine never reports more reuse than the prompt length.
            let reused = request.first_token().await;
            self.emit_counters(request_id, model, input_tokens, 0, false);
            let access = KvCacheAccess {
                hit: reused > 0,
                reused_input_tokens: reused,
                uncached_input_tokens: input_tokens as u64 - reused,
                ..KvCacheAccess::default()
            };
            return (access, TokenPacing::Engine(request));
        }
        let request_slot = self.acquire_request_slot().await;
        self.emit_counters(request_id, model, 0, 0, false);
        let kv_cache_access = self
            .process_input_with_cache(cache_affinity_key, input_tokens)
            .await;
        self.emit_counters(request_id, model, input_tokens, 0, false);
        let first_token_delay =
            self.ttft + Duration::from_millis(jitter_ms(request_id, "ttft", self.ttft_jitter_ms));
        (
            kv_cache_access,
            TokenPacing::Fixed {
                first_token_delay,
                _request_slot: request_slot,
            },
        )
    }

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
    Usage {
        input_tokens: usize,
        output_tokens: usize,
    },
}

pub(crate) fn chat_chunk_json(
    id: &str,
    model: &str,
    chunk: ChatStreamChunk<'_>,
    include_usage: bool,
) -> String {
    let mut usage = include_usage.then_some(None);
    let choice = match chunk {
        ChatStreamChunk::Role => Some((Some("assistant"), None, None)),
        ChatStreamChunk::Content(content) => Some((None, Some(content), None)),
        ChatStreamChunk::Stop => Some((None, None, Some("stop"))),
        ChatStreamChunk::Usage {
            input_tokens,
            output_tokens,
        } => {
            usage = Some(Some(ChatUsage {
                prompt_tokens: input_tokens,
                completion_tokens: output_tokens,
                total_tokens: input_tokens.saturating_add(output_tokens),
            }));
            None
        }
    }
    .map(|(role, content, finish_reason)| ChunkChoice {
        index: 0,
        delta: Delta { role, content },
        finish_reason,
    });
    serde_json::to_string(&ChatCompletionChunk {
        id,
        object: "chat.completion.chunk",
        model,
        choices: choice.as_slice(),
        usage,
    })
    .expect("chat stream event should serialize")
}

fn chat_sse_event(id: &str, model: &str, chunk: ChatStreamChunk<'_>, include_usage: bool) -> Event {
    Event::default().data(chat_chunk_json(id, model, chunk, include_usage))
}

fn stream_response(config: StreamResponseConfig) -> Response {
    let StreamResponseConfig {
        state,
        model,
        id,
        request_id,
        input_tokens,
        output_tokens,
        kv_cache_access,
        mut pacing,
        kind,
    } = config;
    let stream = async_stream::stream! {
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
        pacing.first_token().await;

        if let StreamKind::Chat { include_usage, .. } = kind {
            yield Ok(chat_sse_event(&id, &model, ChatStreamChunk::Role, include_usage));
        }

        for i in 0..output_tokens {
            if i > 0 {
                pacing.next_token(&state, &request_id, i).await;
            }
            let token = if matches!(kind, StreamKind::Chat { canary: true, .. }) {
                CANARY_ANSWER
            } else {
                DUMMY_TOKENS[i % DUMMY_TOKENS.len()]
            };
            let event = match kind {
                StreamKind::Chat { include_usage, .. } => {
                    chat_sse_event(&id, &model, ChatStreamChunk::Content(token), include_usage)
                }
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
            StreamKind::Chat { include_usage, .. } => chat_sse_event(&id, &model, ChatStreamChunk::Stop, include_usage),
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
                            "total_tokens": input_tokens.saturating_add(output_tokens),
                        },
                    },
                }),
            ),
        };
        yield Ok(completed);

        state.emit_counters(&request_id, &model, input_tokens, output_tokens, true);

        if let StreamKind::Chat { include_usage, .. } = kind {
            if include_usage {
                yield Ok(chat_sse_event(&id, &model, ChatStreamChunk::Usage { input_tokens, output_tokens }, true));
            }
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

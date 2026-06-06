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

use axum::http::HeaderMap;
use std::time::Duration;

use crate::AppState;
use crate::openai::{ChatRequest, ResponsesRequest};

pub(crate) fn request_input_tokens(headers: &HeaderMap, request: &ChatRequest) -> usize {
    at_least_one_token(
        header_usize(headers, "x-input-tokens")
            .unwrap_or_else(|| estimate_prompt_tokens(&request.messages)),
    )
}

pub(crate) fn request_output_tokens(
    headers: &HeaderMap,
    request: &ChatRequest,
    default_tokens: usize,
) -> usize {
    if let Some(tokens) = header_usize(headers, "x-output-tokens") {
        return at_least_one_token(tokens);
    }
    let default_tokens = at_least_one_token(default_tokens);
    request
        .max_tokens
        .map(|tokens| at_least_one_token(tokens.min(default_tokens)))
        .unwrap_or(default_tokens)
}

pub(crate) fn response_input_tokens(headers: &HeaderMap, request: &ResponsesRequest) -> usize {
    at_least_one_token(
        header_usize(headers, "x-input-tokens")
            .unwrap_or_else(|| estimate_response_input_tokens(request.input.as_ref())),
    )
}

pub(crate) fn response_output_tokens(
    headers: &HeaderMap,
    request: &ResponsesRequest,
    default_tokens: usize,
) -> usize {
    if let Some(tokens) = header_usize(headers, "x-output-tokens") {
        return at_least_one_token(tokens);
    }
    let default_tokens = at_least_one_token(default_tokens);
    request
        .max_output_tokens
        .map(|tokens| at_least_one_token(tokens.min(default_tokens)))
        .unwrap_or(default_tokens)
}

pub(crate) fn request_embedding_tokens(headers: &HeaderMap, input: &serde_json::Value) -> usize {
    at_least_one_token(
        header_usize(headers, "x-input-tokens").unwrap_or_else(|| estimate_embedding_tokens(input)),
    )
}

pub(crate) fn embedding_item_count(input: &serde_json::Value) -> usize {
    match input {
        serde_json::Value::String(_) => 1,
        serde_json::Value::Array(items) if items.is_empty() => 0,
        serde_json::Value::Array(items) if items.iter().all(serde_json::Value::is_number) => 1,
        serde_json::Value::Array(items) => items.len(),
        _ => 1,
    }
}

pub(crate) fn optional_header(headers: &HeaderMap, name: &str) -> Option<String> {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .filter(|value| !value.is_empty())
        .map(str::to_string)
}

fn header_usize(headers: &HeaderMap, name: &str) -> Option<usize> {
    headers
        .get(name)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse().ok())
}

fn estimate_prompt_tokens(messages: &[serde_json::Value]) -> usize {
    let chars: usize = messages
        .iter()
        .map(|message| {
            message
                .get("content")
                .and_then(|content| content.as_str())
                .map(str::len)
                .unwrap_or_default()
        })
        .sum();
    at_least_one_token(chars.max(messages.len()))
}

fn estimate_response_input_tokens(input: Option<&serde_json::Value>) -> usize {
    let Some(input) = input else {
        return 1;
    };
    match input {
        serde_json::Value::String(text) => text.len(),
        serde_json::Value::Array(items) => items
            .iter()
            .map(|item| match item {
                serde_json::Value::String(text) => text.len(),
                value => value.to_string().len(),
            })
            .sum(),
        value => value.to_string().len(),
    }
}

fn estimate_embedding_tokens(input: &serde_json::Value) -> usize {
    match input {
        serde_json::Value::String(value) => value.len(),
        serde_json::Value::Array(items) if items.iter().all(serde_json::Value::is_number) => {
            items.len()
        }
        serde_json::Value::Array(items) => items
            .iter()
            .map(|item| match item {
                serde_json::Value::String(value) => value.len(),
                serde_json::Value::Array(values) => values.len(),
                _ => 1,
            })
            .sum(),
        _ => 1,
    }
}

fn at_least_one_token(tokens: usize) -> usize {
    // The mock backend's timing and cache math require nonzero token work.
    tokens.max(1)
}

pub(crate) fn prefill_delay(input_tokens: usize, tokens_per_s: f64) -> Duration {
    if tokens_per_s > 0.0 && tokens_per_s.is_finite() {
        Duration::from_secs_f64(input_tokens as f64 / tokens_per_s)
    } else {
        Duration::ZERO
    }
}

pub(crate) fn non_streaming_delay(
    state: &AppState,
    request_id: &str,
    first_token_delay: Duration,
    output_tokens: usize,
) -> Duration {
    let decode_delay = (1..output_tokens)
        .map(|token_index| token_delay(state, request_id, token_index))
        .fold(Duration::ZERO, |total, delay| total + delay);
    first_token_delay + decode_delay
}

pub(crate) fn token_delay(state: &AppState, request_id: &str, token_index: usize) -> Duration {
    state.token_delay
        + Duration::from_millis(jitter_ms(
            request_id,
            &format!("decode-{token_index}"),
            state.decode_jitter_ms,
        ))
}

pub(crate) fn jitter_ms(request_id: &str, salt: &str, max_jitter_ms: u64) -> u64 {
    if max_jitter_ms == 0 {
        return 0;
    }

    let mut hash: u64 = 1469598103934665603;
    for byte in request_id.as_bytes() {
        hash ^= u64::from(*byte);
        hash = hash.wrapping_mul(1099511628211);
    }
    for byte in salt.as_bytes() {
        hash ^= u64::from(*byte);
        hash = hash.wrapping_mul(1099511628211);
    }
    hash % (max_jitter_ms + 1)
}

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

use std::time::Duration;

use crate::generated_request_id::{GeneratedRequestKind, next_generated_request_id};
use crate::request_observer::{
    RequestObservationEndpoint, RequiredTunnelHeaders, TunnelRequestObserver,
};
use crate::runtime_state::{ModelGeneration, PylonRuntimeState};
use crate::upstream_url::upstream_endpoint;
use reqwest::StatusCode;
use serde::Deserialize;
use stargate_protocol::tunnel_contract::{HEADER_INPUT_TOKENS, HEADER_MODEL, HEADER_REQUEST_ID};

pub(crate) async fn check_upstream_health(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    timeout: Duration,
) -> bool {
    let health_url = upstream_endpoint(upstream_http_base_url, "/health");
    matches!(
        http_client.get(health_url).timeout(timeout).send().await,
        Ok(response) if response.status().is_success()
    )
}

pub(super) async fn send_canary_request(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    generation: &ModelGeneration,
    timeout: Duration,
    canary_max_generation_threshold: u32,
) -> Result<(), BringupError> {
    let request = serde_json::json!({
        "model": generation.model_id(),
        "messages": [{"role": "user", "content": "1+1="}],
        "max_tokens": canary_max_generation_threshold,
        "seed": 33,
        "temperature": 0.7,
        "top_p": 1.0,
        "stream": false,
    });

    let completion = send_completion_request(
        http_client,
        upstream_http_base_url,
        Some(timeout),
        &request,
        GeneratedRequestKind::Canary,
        generation,
        None,
    )
    .await?;
    if completion.usage.completion_tokens == canary_max_generation_threshold {
        return Err(BringupError::RunawayGeneration {
            tokens: completion.usage.completion_tokens,
        });
    }
    Ok(())
}

pub(super) async fn send_completion_request(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    timeout: Option<Duration>,
    request: &serde_json::Value,
    request_kind: GeneratedRequestKind,
    generation: &ModelGeneration,
    runtime_state: Option<&PylonRuntimeState>,
) -> Result<ChatCompletionResponse, BringupError> {
    let request_id = next_generated_request_id(request_kind, generation);
    let model_id = generation.model_id();
    let input_tokens = request
        .pointer("/messages/0/content")
        .and_then(serde_json::Value::as_str)
        .map_or(1, str::len);
    let mut observer = runtime_state.map(|runtime_state| {
        TunnelRequestObserver::accepted(
            RequestObservationEndpoint::ChatCompletions,
            RequiredTunnelHeaders {
                request_id: request_id.clone(),
                routing_key: None,
                model_id: model_id.to_string(),
                priority: 0,
                input_tokens: u64::try_from(input_tokens).unwrap_or(u64::MAX),
                accepted_at: std::time::Instant::now(),
            },
            Some(generation.clone()),
            runtime_state.clone(),
        )
    });
    let request = http_client
        .post(upstream_endpoint(
            upstream_http_base_url,
            "/v1/chat/completions",
        ))
        .header(HEADER_REQUEST_ID, &request_id)
        .header(HEADER_MODEL, model_id)
        .header(HEADER_INPUT_TOKENS, input_tokens.to_string())
        .json(request);
    let response = match timeout {
        Some(timeout) => request.timeout(timeout),
        None => request,
    }
    .send()
    .await?;

    let status = response.status();
    observe_response_headers(&mut observer, &response, status);
    let body = response.bytes().await?;
    if status.is_success() {
        let completion = serde_json::from_slice::<ChatCompletionResponse>(&body)
            .map_err(|error| BringupError::InvalidResponse(error.to_string()))?;
        finish_observation(&mut observer, &completion);
        Ok(completion)
    } else {
        let message = extract_error_message(&body);
        if is_prompt_too_long(status, &message) {
            Err(BringupError::PromptTooLong)
        } else {
            Err(BringupError::Api {
                status,
                message: message.unwrap_or_else(|| String::from_utf8_lossy(&body).into_owned()),
            })
        }
    }
}

fn observe_response_headers(
    observer: &mut Option<TunnelRequestObserver>,
    response: &reqwest::Response,
    status: StatusCode,
) {
    if let Some(observer) = observer {
        observer.on_upstream_response_headers(response.headers(), status.as_u16());
    }
}

fn finish_observation(
    observer: &mut Option<TunnelRequestObserver>,
    completion: &ChatCompletionResponse,
) {
    if let Some(observer) = observer {
        let generation = observer
            .generation_mut()
            .expect("chat completion observer should expose generation progress");
        generation.observe_output_message();
        generation.observe_output_tokens_total(u64::from(completion.usage.completion_tokens));
        observer.finish();
    }
}

fn extract_error_message(body: &[u8]) -> Option<String> {
    serde_json::from_slice::<ErrorResponse>(body)
        .ok()
        .map(|error| error.error.message)
}

pub(super) fn is_prompt_too_long(status: StatusCode, message: &Option<String>) -> bool {
    status.is_client_error()
        && message.as_ref().is_some_and(|message| {
            let message = message.to_ascii_lowercase();
            ["prompt too long", "context length", "maximum context"]
                .iter()
                .any(|needle| message.contains(needle))
        })
}

#[derive(Debug, thiserror::Error)]
pub enum BringupError {
    #[error("invalid calibration configuration: {0}")]
    InvalidCalibrationConfig(&'static str),
    #[error("http request failed: {0}")]
    Http(#[from] reqwest::Error),
    #[error("upstream health check failed during pylon startup")]
    UnhealthyUpstream,
    #[error("upstream rejected request ({status}): {message}")]
    Api { status: StatusCode, message: String },
    #[error("calibration prompt too long")]
    PromptTooLong,
    #[error("runaway generation detected at completion_tokens={tokens}")]
    RunawayGeneration { tokens: u32 },
    #[error("invalid completion response: {0}")]
    InvalidResponse(String),
    #[error("stats collector stopped during model initialization")]
    StatsCollectorStopped,
    #[error("model generation retired during initialization")]
    RetiredGeneration,
}

#[derive(Debug, Deserialize)]
pub(super) struct ChatCompletionResponse {
    usage: Usage,
}

#[derive(Debug, Deserialize)]
struct Usage {
    completion_tokens: u32,
}

#[derive(Debug, Deserialize)]
struct ErrorResponse {
    error: ErrorBody,
}

#[derive(Debug, Deserialize)]
struct ErrorBody {
    message: String,
}

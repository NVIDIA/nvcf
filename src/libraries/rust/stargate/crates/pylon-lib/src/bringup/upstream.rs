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

use std::time::{Duration, Instant};

use crate::DEFAULT_MAX_SSE_BUFFER_BYTES;
use crate::generated_request_id::{GeneratedRequestKind, next_generated_request_id};
use crate::output_token_parser::OutputTokenParser;
use crate::request_observer::{
    RequestObservationEndpoint, RequiredTunnelHeaders, TunnelRequestObserver,
};
use crate::runtime_state::{ModelGeneration, PylonRuntimeState};
use crate::sse_message_stream::{RelayOutcome, upstream_sse_message_stream};
use crate::upstream_health::UpstreamHealthPaths;
use crate::upstream_url::upstream_endpoint;
use futures::StreamExt;
use reqwest::StatusCode;
use reqwest::header::CONTENT_TYPE;
use serde::Deserialize;
use stargate_protocol::tunnel_contract::{HEADER_INPUT_TOKENS, HEADER_MODEL, HEADER_REQUEST_ID};

const MAX_UPSTREAM_ERROR_BODY_BYTES: usize = 64 * 1024;

pub(crate) async fn check_upstream_health(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    timeout: Duration,
    health_paths: &UpstreamHealthPaths,
) -> bool {
    for (index, path) in health_paths.probe_order() {
        let health_url = upstream_endpoint(upstream_http_base_url, path);
        if matches!(
            http_client.get(health_url).timeout(timeout).send().await,
            Ok(response) if response.status().is_success()
        ) {
            health_paths.mark_resolved(index);
            return true;
        }
    }
    false
}

async fn ensure_success(response: reqwest::Response) -> Result<reqwest::Response, BringupError> {
    let status = response.status();
    if status.is_success() {
        return Ok(response);
    }

    let (body, truncated) = read_error_body(response).await?;
    let message = (!truncated).then(|| extract_error_message(&body)).flatten();
    if is_prompt_too_long(status, &message) {
        Err(BringupError::PromptTooLong)
    } else {
        Err(BringupError::Api {
            status,
            message: message.unwrap_or_else(|| {
                if truncated {
                    format!(
                        "upstream returned HTTP status {status} with an error body exceeding {MAX_UPSTREAM_ERROR_BODY_BYTES} bytes"
                    )
                } else {
                    format!("upstream returned HTTP status {status} without a JSON error message")
                }
            }),
        })
    }
}

async fn read_error_body(response: reqwest::Response) -> Result<(Vec<u8>, bool), reqwest::Error> {
    let mut body = Vec::with_capacity(MAX_UPSTREAM_ERROR_BODY_BYTES);
    let mut stream = response.bytes_stream();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk?;
        let remaining = MAX_UPSTREAM_ERROR_BODY_BYTES.saturating_sub(body.len());
        if chunk.len() > remaining {
            body.extend_from_slice(&chunk[..remaining]);
            return Ok((body, true));
        }
        body.extend_from_slice(&chunk);
    }
    Ok((body, false))
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
        "stream": true,
        "stream_options": {"include_usage": true},
    });

    let request_id = next_generated_request_id(GeneratedRequestKind::Canary, generation);
    let input_tokens = request
        .pointer("/messages/0/content")
        .and_then(serde_json::Value::as_str)
        .map_or(1, str::len);
    let response = ensure_success(
        http_client
            .post(upstream_endpoint(
                upstream_http_base_url,
                "/v1/chat/completions",
            ))
            .header(HEADER_REQUEST_ID, request_id)
            .header(HEADER_MODEL, generation.model_id())
            .header(HEADER_INPUT_TOKENS, input_tokens.to_string())
            .timeout(timeout)
            .json(&request)
            .send()
            .await?,
    )
    .await?;
    let is_event_stream = response
        .headers()
        .get(CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .is_some_and(|value| {
            value.split(';').next().is_some_and(|media_type| {
                media_type.trim().eq_ignore_ascii_case("text/event-stream")
            })
        });
    if !is_event_stream {
        return Err(BringupError::InvalidResponse(
            "canary response is not an SSE stream".to_string(),
        ));
    }

    let mut messages = upstream_sse_message_stream(
        response.bytes_stream(),
        timeout,
        timeout,
        DEFAULT_MAX_SSE_BUFFER_BYTES,
    );
    let mut output_tokens = OutputTokenParser::new();
    let mut observed_tokens = 0_u64;
    let mut completed = false;
    while let Some(message) = messages.next().await {
        let message = message.map_err(|error| BringupError::InvalidResponse(error.to_string()))?;
        if let Some(generated_output) = message.facts.generated_output
            && let Some(delta) = output_tokens
                .observe_estimated_output_tokens(generated_output.estimated_token_units)
        {
            observed_tokens = observed_tokens.saturating_add(delta);
        }
        if let Some(tokens) = message
            .facts
            .exact_usage
            .and_then(|usage| usage.output_tokens)
        {
            observed_tokens = output_tokens.observe_exact_output_tokens(tokens);
        }
        if observed_tokens > u64::from(canary_max_generation_threshold) {
            return Err(BringupError::RunawayGeneration {
                tokens: u32::try_from(observed_tokens).unwrap_or(u32::MAX),
            });
        }
        match message.facts.terminal {
            Some(RelayOutcome::Complete) => {
                completed = true;
                break;
            }
            Some(RelayOutcome::Failed) => break,
            None => {}
        }
    }
    if !completed || observed_tokens == 0 {
        return Err(BringupError::InvalidResponse(
            "canary stream must contain output and end with [DONE]".to_string(),
        ));
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
                priority: None,
                input_tokens: u64::try_from(input_tokens).unwrap_or(u64::MAX),
                accepted_at: std::time::Instant::now(),
            },
            Some(generation.clone()),
            runtime_state.clone(),
        )
    });
    let result = async {
        let request = http_client
            .post(upstream_endpoint(
                upstream_http_base_url,
                "/v1/chat/completions",
            ))
            .header(HEADER_REQUEST_ID, &request_id)
            .header(HEADER_MODEL, model_id)
            .header(HEADER_INPUT_TOKENS, input_tokens.to_string())
            .json(request);
        let request = match timeout {
            Some(timeout) => request.timeout(timeout),
            None => request,
        }
        .build()?;
        if let Some(observer) = observer.as_mut() {
            observer.on_backend_submission(Instant::now());
        }
        let response = http_client.execute(request).await?;

        let status = response.status();
        observe_response_headers(&mut observer, status);
        let response = ensure_success(response).await?;
        let body = response.bytes().await?;
        serde_json::from_slice::<ChatCompletionResponse>(&body)
            .map_err(|error| BringupError::InvalidResponse(error.to_string()))
    }
    .await;
    match result {
        Ok(completion) => {
            finish_observation(&mut observer, &completion);
            Ok(completion)
        }
        Err(error) => {
            if let Some(observer) = observer.as_mut() {
                observer.fail();
            }
            Err(error)
        }
    }
}

fn observe_response_headers(observer: &mut Option<TunnelRequestObserver>, status: StatusCode) {
    if let Some(observer) = observer {
        observer.on_upstream_response_headers(status.as_u16());
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
        generation.observe_generated_output(
            Instant::now(),
            completion.usage.completion_tokens > 0,
            0,
        );
        generation.observe_output_tokens_total(u64::from(completion.usage.completion_tokens));
        observer.complete();
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
    #[error("calibration saturated before measuring positive input throughput")]
    InsufficientCalibrationData,
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

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

use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};

use crate::stats::PylonMetrics;
use crate::stats::token_metrics::{SNAPSHOT_THRESHOLD, TpsDistribution};
use futures::future::join_all;
use reqwest::StatusCode;
use serde::Deserialize;
use stargate_protocol::tunnel_contract::{HEADER_INPUT_TOKENS, HEADER_MODEL, HEADER_REQUEST_ID};
use tokio::sync::watch;
use tokio::task::JoinHandle;

const CONNECT_RETRY_INTERVAL: Duration = Duration::from_secs(1);
const CALIBRATION_PROMPT_UNITS_FLOOR: usize = 256;
const DEFAULT_ACTIVE_CANARY_INTERVAL: Duration = Duration::from_secs(5);
const DEFAULT_CANARY_TIMEOUT: Duration = Duration::from_secs(5);
const DEFAULT_CANARY_MAX_GENERATION_THRESHOLD: u32 = 237;
const DEFAULT_CALIBRATION_REQUESTS: usize = 5;
const DEFAULT_CALIBRATION_PROMPT_UNITS: usize = 4096;
const DEFAULT_CALIBRATION_MAX_CONCURRENCY: usize = 4;
const DEFAULT_CALIBRATION_TIMEOUT: Duration = Duration::from_secs(30);

static BRINGUP_REQUEST_COUNTER: AtomicU64 = AtomicU64::new(1);

#[derive(Debug, Clone)]
pub struct BringupConfig {
    pub enabled: bool,
    pub active_canary_interval: Duration,
    pub canary_timeout: Duration,
    pub canary_max_generation_threshold: u32,
    pub calibration_requests: usize,
    pub calibration_prompt_units: usize,
    pub calibration_max_concurrency: usize,
    pub calibration_timeout: Duration,
}

impl Default for BringupConfig {
    fn default() -> Self {
        Self {
            enabled: true,
            active_canary_interval: DEFAULT_ACTIVE_CANARY_INTERVAL,
            canary_timeout: DEFAULT_CANARY_TIMEOUT,
            canary_max_generation_threshold: DEFAULT_CANARY_MAX_GENERATION_THRESHOLD,
            calibration_requests: DEFAULT_CALIBRATION_REQUESTS,
            calibration_prompt_units: DEFAULT_CALIBRATION_PROMPT_UNITS,
            calibration_max_concurrency: DEFAULT_CALIBRATION_MAX_CONCURRENCY,
            calibration_timeout: DEFAULT_CALIBRATION_TIMEOUT,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum ModelBringupState {
    ConnectingUnavailable,
    Recovering,
    AdvertisingActive,
}

#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
enum BringupLifecycleState {
    #[default]
    Initializing,
    Active,
    Recovering,
}

impl BringupLifecycleState {
    fn next_action(self) -> BringupLifecycleAction {
        match self {
            Self::Recovering => BringupLifecycleAction::RunRecoveryCanary,
            Self::Active => BringupLifecycleAction::AdvertiseActive,
            Self::Initializing => BringupLifecycleAction::AdvertiseInitialActive,
        }
    }

    fn complete_initial_bringup(&mut self) {
        match self {
            Self::Initializing => *self = Self::Active,
            Self::Active => panic!("initial bringup completed after model was already active"),
            Self::Recovering => panic!("initial bringup completed while recovery was pending"),
        }
    }

    fn require_recovery_canary(&mut self) {
        match self {
            Self::Active => *self = Self::Recovering,
            Self::Recovering => {}
            Self::Initializing => panic!("recovery canary requested before initial bringup"),
        }
    }

    fn complete_recovery_canary(&mut self) {
        match self {
            Self::Recovering => *self = Self::Active,
            Self::Initializing => panic!("recovery canary completed before initial bringup"),
            Self::Active => panic!("recovery canary completed while model was already active"),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum BringupLifecycleAction {
    RunRecoveryCanary,
    AdvertiseActive,
    AdvertiseInitialActive,
}

#[derive(Debug, Clone)]
pub(crate) struct BringupTaskConfig {
    pub upstream_http_base_url: String,
    pub model_id: String,
    pub config: BringupConfig,
    pub metrics: Option<Arc<PylonMetrics>>,
}

#[derive(Debug, Clone)]
pub(crate) struct BringupModelUpdate {
    pub model_id: String,
    pub state: ModelBringupState,
}

pub(crate) fn start_bringup_supervisor(
    task_configs: Vec<BringupTaskConfig>,
    lifecycle_tx: flume::Sender<BringupModelUpdate>,
    stop_rx: watch::Receiver<bool>,
) -> JoinHandle<()> {
    tokio::spawn(async move {
        let mut tasks = Vec::new();
        for task_config in task_configs {
            let task = tokio::spawn(run_bringup_task(
                task_config,
                lifecycle_tx.clone(),
                stop_rx.clone(),
            ));
            tasks.push(task);
        }

        let mut stop_rx = stop_rx;
        loop {
            if *stop_rx.borrow() {
                break;
            }
            if stop_rx.changed().await.is_err() {
                break;
            }
        }

        for task in tasks {
            task.abort();
        }
    })
}

async fn run_bringup_task(
    task_config: BringupTaskConfig,
    lifecycle_tx: flume::Sender<BringupModelUpdate>,
    stop_rx: watch::Receiver<bool>,
) {
    let BringupTaskConfig {
        upstream_http_base_url,
        model_id,
        config,
        metrics: _,
    } = task_config;
    let http_client = reqwest::Client::new();
    let mut stop_rx = stop_rx;
    let mut lifecycle = BringupLifecycleState::default();

    if !config.enabled {
        let _ = lifecycle_tx
            .send_async(BringupModelUpdate {
                model_id,
                state: ModelBringupState::AdvertisingActive,
            })
            .await;
        return;
    }

    loop {
        if *stop_rx.borrow() {
            return;
        }

        if !check_upstream_health(&http_client, &upstream_http_base_url, config.canary_timeout)
            .await
        {
            let _ = lifecycle_tx
                .send_async(BringupModelUpdate {
                    model_id: model_id.clone(),
                    state: ModelBringupState::ConnectingUnavailable,
                })
                .await;
            if wait_or_stop(&mut stop_rx, CONNECT_RETRY_INTERVAL).await {
                return;
            }
            continue;
        }

        match lifecycle.next_action() {
            BringupLifecycleAction::RunRecoveryCanary => {
                let _ = lifecycle_tx
                    .send_async(BringupModelUpdate {
                        model_id: model_id.clone(),
                        state: ModelBringupState::Recovering,
                    })
                    .await;

                match send_canary_request(
                    &http_client,
                    &upstream_http_base_url,
                    &model_id,
                    config.canary_timeout,
                    config.canary_max_generation_threshold,
                )
                .await
                {
                    Ok(()) => {
                        lifecycle.complete_recovery_canary();
                        continue;
                    }
                    Err(error) => {
                        tracing::warn!(model_id, error = %error, "bringup recovery canary failed");
                        if wait_or_stop(&mut stop_rx, CONNECT_RETRY_INTERVAL).await {
                            return;
                        }
                        continue;
                    }
                }
            }
            BringupLifecycleAction::AdvertiseActive => {
                let _ = lifecycle_tx
                    .send_async(BringupModelUpdate {
                        model_id: model_id.clone(),
                        state: ModelBringupState::AdvertisingActive,
                    })
                    .await;
            }
            BringupLifecycleAction::AdvertiseInitialActive => {
                let _ = lifecycle_tx
                    .send_async(BringupModelUpdate {
                        model_id: model_id.clone(),
                        state: ModelBringupState::AdvertisingActive,
                    })
                    .await;
                lifecycle.complete_initial_bringup();
            }
        }

        if config.active_canary_interval.is_zero() {
            loop {
                if *stop_rx.borrow() {
                    return;
                }
                if stop_rx.changed().await.is_err() {
                    return;
                }
            }
        }

        let mut canary_interval = tokio::time::interval(config.active_canary_interval);
        canary_interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
        canary_interval.tick().await;

        loop {
            tokio::select! {
                _ = stop_rx.changed() => {
                    if *stop_rx.borrow() {
                        return;
                    }
                }
                _ = canary_interval.tick() => {
                    match send_canary_request(
                        &http_client,
                        &upstream_http_base_url,
                        &model_id,
                        config.canary_timeout,
                        config.canary_max_generation_threshold,
                    ).await {
                        Ok(()) => {}
                        Err(error) => {
                            tracing::warn!(model_id, error = %error, "active canary failed");
                            lifecycle.require_recovery_canary();
                            let next_state = if check_upstream_health(
                                &http_client,
                                &upstream_http_base_url,
                                config.canary_timeout,
                            ).await {
                                ModelBringupState::Recovering
                            } else {
                                ModelBringupState::ConnectingUnavailable
                            };
                            let _ = lifecycle_tx
                                .send_async(BringupModelUpdate {
                                    model_id: model_id.clone(),
                                    state: next_state,
                                })
                                .await;
                            break;
                        }
                    }
                }
            }
        }
    }
}

async fn wait_or_stop(stop_rx: &mut watch::Receiver<bool>, duration: Duration) -> bool {
    tokio::select! {
        _ = stop_rx.changed() => *stop_rx.borrow(),
        _ = tokio::time::sleep(duration) => *stop_rx.borrow(),
    }
}

pub(crate) async fn run_assigned_cluster_calibration(
    task_config: &BringupTaskConfig,
) -> Result<f64, BringupError> {
    let client = reqwest::Client::new();
    let started_at = Instant::now();
    let result = if check_upstream_health(
        &client,
        &task_config.upstream_http_base_url,
        task_config.config.canary_timeout,
    )
    .await
    {
        run_calibration(
            &client,
            &task_config.upstream_http_base_url,
            &task_config.model_id,
            &task_config.config,
        )
        .await
    } else {
        Err(BringupError::UnhealthyUpstream)
    };
    if let Some(metrics) = task_config.metrics.as_deref() {
        metrics.observe_model_calibration_duration(
            &task_config.model_id,
            started_at.elapsed(),
            result.is_ok(),
        );
    }
    result
}

async fn run_calibration(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    model_id: &str,
    config: &BringupConfig,
) -> Result<f64, BringupError> {
    if config.calibration_requests == 0 {
        return Ok(0.0);
    }

    let mut distribution = TpsDistribution::default();
    let mut last_error = None;

    for batch in calibration_plan(config) {
        match send_calibration_batch_with_prompt_backoff(
            http_client,
            upstream_http_base_url,
            model_id,
            config.calibration_timeout,
            batch,
        )
        .await
        {
            Ok(observed_input_tps_samples) => {
                for sample in observed_input_tps_samples {
                    distribution.update(sample);
                }
            }
            Err(BringupError::PromptTooLong) => {
                last_error = Some(BringupError::PromptTooLong);
            }
            Err(error) => return Err(error),
        }
    }

    let required_samples = config.calibration_requests.min(SNAPSHOT_THRESHOLD);
    if distribution.count >= required_samples {
        Ok(distribution.mean)
    } else if let Some(error) = last_error {
        Err(error)
    } else {
        Err(BringupError::InsufficientCalibrationSamples {
            valid_samples: distribution.count,
        })
    }
}

async fn send_calibration_batch_with_prompt_backoff(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    model_id: &str,
    timeout: Duration,
    batch: CalibrationBatch,
) -> Result<Vec<f64>, BringupError> {
    let mut prompt_units = batch.prompt_units.max(CALIBRATION_PROMPT_UNITS_FLOOR);

    loop {
        match send_calibration_batch(
            http_client,
            upstream_http_base_url,
            model_id,
            timeout,
            prompt_units,
            batch.concurrency,
        )
        .await
        {
            Err(BringupError::PromptTooLong) if prompt_units > CALIBRATION_PROMPT_UNITS_FLOOR => {
                let next_prompt_units = ((prompt_units + CALIBRATION_PROMPT_UNITS_FLOOR) / 2)
                    .max(CALIBRATION_PROMPT_UNITS_FLOOR);
                if next_prompt_units >= prompt_units {
                    return Err(BringupError::PromptTooLong);
                }
                prompt_units = next_prompt_units;
            }
            result => return result,
        }
    }
}

async fn check_upstream_health(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    timeout: Duration,
) -> bool {
    let health_url = format!("{}/health", upstream_http_base_url.trim_end_matches('/'));
    matches!(
        http_client.get(health_url).timeout(timeout).send().await,
        Ok(response) if response.status().is_success()
    )
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct CalibrationBatch {
    prompt_units: usize,
    concurrency: usize,
}

fn calibration_plan(config: &BringupConfig) -> Vec<CalibrationBatch> {
    let requests = config.calibration_requests;
    if requests == 0 {
        return Vec::new();
    }

    let max_prompt_units = config
        .calibration_prompt_units
        .max(CALIBRATION_PROMPT_UNITS_FLOOR);
    // A zero calibration concurrency override degrades to serial calibration.
    let max_concurrency = config.calibration_max_concurrency.max(1).min(requests);
    if requests == 1 {
        return vec![CalibrationBatch {
            prompt_units: max_prompt_units,
            concurrency: 1,
        }];
    }

    if max_concurrency == 1 {
        return (0..requests)
            .map(|index| {
                let prompt_units = interpolate_usize(
                    CALIBRATION_PROMPT_UNITS_FLOOR,
                    max_prompt_units,
                    index,
                    requests - 1,
                );
                let concurrency = interpolate_usize(1, max_concurrency, index, requests - 1);
                CalibrationBatch {
                    prompt_units,
                    concurrency,
                }
            })
            .collect();
    }

    let final_concurrency = max_concurrency.min(requests - 1);
    let single_request_runs = requests - final_concurrency;
    let mut batches = Vec::with_capacity(single_request_runs + 1);
    for index in 0..single_request_runs {
        batches.push(CalibrationBatch {
            prompt_units: interpolate_usize(
                CALIBRATION_PROMPT_UNITS_FLOOR,
                max_prompt_units,
                index,
                single_request_runs,
            ),
            concurrency: 1,
        });
    }
    batches.push(CalibrationBatch {
        prompt_units: max_prompt_units,
        concurrency: final_concurrency,
    });

    batches
}

fn interpolate_usize(start: usize, end: usize, index: usize, last_index: usize) -> usize {
    if last_index == 0 {
        return end;
    }
    let span = end - start;
    start + (span * index / last_index)
}

async fn send_calibration_batch(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    model_id: &str,
    timeout: Duration,
    prompt_units: usize,
    concurrency: usize,
) -> Result<Vec<f64>, BringupError> {
    assert!(concurrency > 0, "calibration batch concurrency must be > 0");
    let prompt = "1".repeat(prompt_units);
    let request = serde_json::json!({
        "model": model_id,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": 1,
        "seed": 33,
        "temperature": 0.7,
        "top_p": 1.0,
        "stream": false,
    });

    let batch_started_at = Instant::now();
    let requests = (0..concurrency).map(|_| {
        let request = request.clone();
        async move {
            send_completion_request(http_client, upstream_http_base_url, timeout, request).await?;
            Ok::<_, BringupError>(())
        }
    });
    let _: Vec<()> = join_all(requests)
        .await
        .into_iter()
        .collect::<Result<_, _>>()?;
    // Sub-millisecond localhost tests should not report infinite calibrated throughput.
    let elapsed = batch_started_at.elapsed().max(Duration::from_millis(1));
    let aggregate_input_tps = (prompt_units as f64 * concurrency as f64) / elapsed.as_secs_f64();
    Ok(vec![aggregate_input_tps; concurrency])
}

async fn send_canary_request(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    model_id: &str,
    timeout: Duration,
    canary_max_generation_threshold: u32,
) -> Result<(), BringupError> {
    let request = serde_json::json!({
        "model": model_id,
        "messages": [{"role": "user", "content": "1+1="}],
        "max_tokens": canary_max_generation_threshold,
        "seed": 33,
        "temperature": 0.7,
        "top_p": 1.0,
        "stream": false,
    });

    let completion =
        send_completion_request(http_client, upstream_http_base_url, timeout, request).await?;
    if completion.usage.completion_tokens == canary_max_generation_threshold {
        return Err(BringupError::RunawayGeneration {
            tokens: completion.usage.completion_tokens,
        });
    }
    Ok(())
}

async fn send_completion_request(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    timeout: Duration,
    request: serde_json::Value,
) -> Result<ChatCompletionResponse, BringupError> {
    let request_id = BRINGUP_REQUEST_COUNTER.fetch_add(1, Ordering::Relaxed);
    let request_url = format!(
        "{}/v1/chat/completions",
        upstream_http_base_url.trim_end_matches('/')
    );
    let response = http_client
        .post(request_url)
        .timeout(timeout)
        .header(HEADER_REQUEST_ID, format!("bringup-{request_id}"))
        .header(
            HEADER_MODEL,
            request
                .get("model")
                .and_then(|value| value.as_str())
                .unwrap_or_default(),
        )
        .header(
            HEADER_INPUT_TOKENS,
            request
                .get("messages")
                .and_then(|value| value.as_array())
                .and_then(|messages| messages.first())
                .and_then(|message| message.get("content"))
                .and_then(|value| value.as_str())
                .map(|text| text.len().to_string())
                .unwrap_or_else(|| "1".to_string()),
        )
        .json(&request)
        .send()
        .await?;

    let status = response.status();
    let body = response.bytes().await?;
    if status.is_success() {
        return serde_json::from_slice(&body)
            .map_err(|error| BringupError::InvalidResponse(error.to_string()));
    }

    let message = extract_error_message(&body);
    if is_prompt_too_long(status, &message) {
        return Err(BringupError::PromptTooLong);
    }
    Err(BringupError::Api {
        status,
        message: message.unwrap_or_else(|| String::from_utf8_lossy(&body).into_owned()),
    })
}

fn extract_error_message(body: &[u8]) -> Option<String> {
    serde_json::from_slice::<ErrorResponse>(body)
        .ok()
        .map(|error| error.error.message)
}

fn is_prompt_too_long(status: StatusCode, message: &Option<String>) -> bool {
    if !status.is_client_error() {
        return false;
    }
    let Some(message) = message else {
        return false;
    };
    let message = message.to_ascii_lowercase();
    message.contains("prompt too long")
        || message.contains("context length")
        || message.contains("maximum context")
}

#[derive(Debug, thiserror::Error)]
pub(crate) enum BringupError {
    #[error("http request failed: {0}")]
    Http(#[from] reqwest::Error),
    #[error("upstream health check failed before assigned calibration")]
    UnhealthyUpstream,
    #[error("upstream rejected request ({status}): {message}")]
    Api { status: StatusCode, message: String },
    #[error("calibration prompt too long")]
    PromptTooLong,
    #[error("runaway generation detected at completion_tokens={tokens}")]
    RunawayGeneration { tokens: u32 },
    #[error("invalid completion response: {0}")]
    InvalidResponse(String),
    #[error("calibration produced only {valid_samples} valid samples")]
    InsufficientCalibrationSamples { valid_samples: usize },
}

#[derive(Debug, Deserialize)]
struct ChatCompletionResponse {
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

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};

    use axum::Json;
    use axum::Router;
    use axum::extract::State;
    use axum::response::IntoResponse;
    use axum::routing::{get, post};
    use serde_json::Value;
    use tokio::net::TcpListener;
    use tokio::sync::{Barrier, Mutex, watch};

    #[test]
    fn detects_prompt_too_long_errors() {
        assert!(is_prompt_too_long(
            StatusCode::BAD_REQUEST,
            &Some("Prompt too long for model context length".to_string())
        ));
        assert!(!is_prompt_too_long(
            StatusCode::INTERNAL_SERVER_ERROR,
            &Some("prompt too long".to_string())
        ));
    }

    #[test]
    fn bringup_lifecycle_state_classifies_actions() {
        let mut lifecycle = BringupLifecycleState::default();
        assert_eq!(
            lifecycle.next_action(),
            BringupLifecycleAction::AdvertiseInitialActive
        );

        lifecycle.complete_initial_bringup();
        assert_eq!(lifecycle, BringupLifecycleState::Active);
        assert_eq!(
            lifecycle.next_action(),
            BringupLifecycleAction::AdvertiseActive
        );

        lifecycle.require_recovery_canary();
        assert_eq!(lifecycle, BringupLifecycleState::Recovering);
        assert_eq!(
            lifecycle.next_action(),
            BringupLifecycleAction::RunRecoveryCanary
        );

        lifecycle.complete_recovery_canary();
        assert_eq!(lifecycle, BringupLifecycleState::Active);
    }

    #[tokio::test]
    async fn canary_request_detects_runaway_generation() {
        let base_url = spawn_test_server(TestServerState {
            completion_tokens: 7,
            prompt_too_long_above: None,
            calibration_barrier: None,
            completion_delay: None,
            in_flight: None,
            max_in_flight: None,
            canary_failures_remaining: None,
            health_ok: None,
        })
        .await;
        let client = reqwest::Client::new();
        let error =
            send_canary_request(&client, &base_url, "test-model", Duration::from_secs(1), 7)
                .await
                .expect_err("expected runaway generation");
        assert!(matches!(
            error,
            BringupError::RunawayGeneration { tokens: 7 }
        ));
    }

    #[tokio::test]
    async fn calibration_reduces_prompt_size_after_prompt_too_long() {
        let base_url = spawn_test_server(TestServerState {
            completion_tokens: 1,
            prompt_too_long_above: Some(700),
            calibration_barrier: None,
            completion_delay: None,
            in_flight: None,
            max_in_flight: None,
            canary_failures_remaining: None,
            health_ok: None,
        })
        .await;
        let client = reqwest::Client::new();
        let config = BringupConfig {
            calibration_requests: 5,
            calibration_prompt_units: 1536,
            calibration_timeout: Duration::from_secs(1),
            ..BringupConfig::default()
        };

        let observed = run_calibration(&client, &base_url, "test-model", &config)
            .await
            .expect("calibration should back off and succeed");
        assert!(observed.is_finite());
        assert!(observed > 0.0);
    }

    #[tokio::test]
    async fn single_request_calibration_completes_after_prompt_backoff() {
        let base_url = spawn_test_server(TestServerState {
            completion_tokens: 1,
            prompt_too_long_above: Some(700),
            calibration_barrier: None,
            completion_delay: None,
            in_flight: None,
            max_in_flight: None,
            canary_failures_remaining: None,
            health_ok: None,
        })
        .await;
        let client = reqwest::Client::new();
        let config = BringupConfig {
            calibration_requests: 1,
            calibration_prompt_units: 1536,
            calibration_timeout: Duration::from_secs(1),
            ..BringupConfig::default()
        };

        let observed = run_calibration(&client, &base_url, "test-model", &config)
            .await
            .expect("single-request calibration should complete with one valid configured sample");
        assert!(observed.is_finite());
        assert!(observed > 0.0);
    }

    #[test]
    fn calibration_plan_sweeps_tokens_at_increasing_concurrency_levels() {
        let config = BringupConfig {
            calibration_requests: 5,
            calibration_prompt_units: 1024,
            calibration_max_concurrency: 4,
            ..BringupConfig::default()
        };

        let plan = calibration_plan(&config);

        assert_eq!(calibration_plan_request_count(&plan), 5);
        assert_eq!(
            plan,
            vec![
                CalibrationBatch {
                    prompt_units: CALIBRATION_PROMPT_UNITS_FLOOR,
                    concurrency: 1,
                },
                CalibrationBatch {
                    prompt_units: 1024,
                    concurrency: 4,
                },
            ]
        );
    }

    #[test]
    fn calibration_plan_preserves_linear_ramp_when_quadrants_cannot_be_sampled() {
        let config = BringupConfig {
            calibration_requests: 3,
            calibration_prompt_units: 1024,
            calibration_max_concurrency: 4,
            ..BringupConfig::default()
        };

        let plan = calibration_plan(&config);

        assert_eq!(calibration_plan_request_count(&plan), 3);
        assert_eq!(
            plan,
            vec![
                CalibrationBatch {
                    prompt_units: CALIBRATION_PROMPT_UNITS_FLOOR,
                    concurrency: 1,
                },
                CalibrationBatch {
                    prompt_units: 1024,
                    concurrency: 2,
                },
            ]
        );
    }

    #[test]
    fn single_calibration_request_does_not_expand_to_max_concurrency() {
        let config = BringupConfig {
            calibration_requests: 1,
            calibration_prompt_units: 1024,
            calibration_max_concurrency: 4,
            ..BringupConfig::default()
        };

        let plan = calibration_plan(&config);

        assert_eq!(calibration_plan_request_count(&plan), 1);
        assert_eq!(
            plan,
            vec![CalibrationBatch {
                prompt_units: 1024,
                concurrency: 1,
            }]
        );
    }

    fn calibration_plan_request_count(plan: &[CalibrationBatch]) -> usize {
        plan.iter().map(|batch| batch.concurrency).sum()
    }

    #[tokio::test]
    async fn calibration_batch_sends_requests_concurrently() {
        let in_flight = Arc::new(AtomicUsize::new(0));
        let max_in_flight = Arc::new(AtomicUsize::new(0));
        let base_url = spawn_test_server(TestServerState {
            completion_tokens: 1,
            prompt_too_long_above: None,
            calibration_barrier: Some(Arc::new(Barrier::new(3))),
            completion_delay: None,
            in_flight: Some(in_flight),
            max_in_flight: Some(max_in_flight.clone()),
            canary_failures_remaining: None,
            health_ok: None,
        })
        .await;
        let client = reqwest::Client::new();

        let observed = send_calibration_batch(
            &client,
            &base_url,
            "test-model",
            Duration::from_secs(1),
            256,
            3,
        )
        .await
        .expect("calibration batch should succeed");

        assert_eq!(observed.len(), 3);
        assert!(observed.iter().all(|sample| *sample > 0.0));
        assert_eq!(max_in_flight.load(Ordering::SeqCst), 3);
    }

    #[tokio::test]
    async fn concurrent_calibration_batch_reports_aggregate_backend_capacity() {
        let serial_base_url = spawn_test_server(TestServerState {
            completion_tokens: 1,
            prompt_too_long_above: None,
            calibration_barrier: None,
            completion_delay: Some(Duration::from_millis(100)),
            in_flight: None,
            max_in_flight: None,
            canary_failures_remaining: None,
            health_ok: None,
        })
        .await;
        let in_flight = Arc::new(AtomicUsize::new(0));
        let max_in_flight = Arc::new(AtomicUsize::new(0));
        let concurrent_base_url = spawn_test_server(TestServerState {
            completion_tokens: 1,
            prompt_too_long_above: None,
            calibration_barrier: Some(Arc::new(Barrier::new(3))),
            completion_delay: Some(Duration::from_millis(100)),
            in_flight: Some(in_flight),
            max_in_flight: Some(max_in_flight.clone()),
            canary_failures_remaining: None,
            health_ok: None,
        })
        .await;
        let client = reqwest::Client::new();

        let serial = send_calibration_batch(
            &client,
            &serial_base_url,
            "test-model",
            Duration::from_secs(1),
            256,
            1,
        )
        .await
        .expect("serial calibration should succeed");
        let concurrent = send_calibration_batch(
            &client,
            &concurrent_base_url,
            "test-model",
            Duration::from_secs(1),
            256,
            3,
        )
        .await
        .expect("concurrent calibration should succeed");

        let serial_tps = serial[0];
        let concurrent_mean_tps = concurrent.iter().copied().sum::<f64>() / concurrent.len() as f64;
        assert_eq!(concurrent.len(), 3);
        assert_eq!(max_in_flight.load(Ordering::SeqCst), 3);
        assert!(
            concurrent_mean_tps > serial_tps * 1.8,
            "concurrent calibration should report aggregate backend capacity: serial={serial_tps}, concurrent={concurrent_mean_tps}"
        );
    }

    #[tokio::test]
    async fn assigned_cluster_calibration_records_duration_metric() {
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let base_url = spawn_test_server(TestServerState {
            completion_tokens: 1,
            prompt_too_long_above: None,
            calibration_barrier: None,
            completion_delay: None,
            in_flight: None,
            max_in_flight: None,
            canary_failures_remaining: None,
            health_ok: None,
        })
        .await;
        let last_mean_input_tps = run_assigned_cluster_calibration(&BringupTaskConfig {
            upstream_http_base_url: base_url,
            model_id: "test-model".to_string(),
            config: BringupConfig {
                calibration_requests: 5,
                active_canary_interval: Duration::ZERO,
                canary_timeout: Duration::from_secs(1),
                calibration_timeout: Duration::from_secs(1),
                ..BringupConfig::default()
            },
            metrics: Some(metrics.clone()),
        })
        .await
        .expect("assigned calibration should produce a local measurement");
        assert!(last_mean_input_tps > 0.0);

        let body = metrics.gather_text().expect("metrics should encode");
        assert!(body.contains(
            r#"pylon_model_calibration_duration_ms_count{model="test-model",outcome="success"} 1"#
        ));
        assert!(!body.contains(
            r#"pylon_model_calibration_duration_ms_count{model="test-model",outcome="failure"}"#
        ));
    }

    #[tokio::test]
    async fn assigned_cluster_calibration_does_not_measure_unhealthy_upstream() {
        let health_ok = Arc::new(AtomicBool::new(false));
        let in_flight = Arc::new(AtomicUsize::new(0));
        let max_in_flight = Arc::new(AtomicUsize::new(0));
        let base_url = spawn_test_server(TestServerState {
            completion_tokens: 1,
            prompt_too_long_above: None,
            calibration_barrier: None,
            completion_delay: None,
            in_flight: Some(in_flight),
            max_in_flight: Some(max_in_flight.clone()),
            canary_failures_remaining: None,
            health_ok: Some(health_ok),
        })
        .await;

        let error = run_assigned_cluster_calibration(&BringupTaskConfig {
            upstream_http_base_url: base_url,
            model_id: "test-model".to_string(),
            config: BringupConfig {
                calibration_requests: 1,
                canary_timeout: Duration::from_secs(1),
                calibration_timeout: Duration::from_secs(1),
                ..BringupConfig::default()
            },
            metrics: None,
        })
        .await
        .expect_err("unhealthy upstream must not be measured for an assignment");

        assert!(matches!(error, BringupError::UnhealthyUpstream));
        assert_eq!(
            max_in_flight.load(Ordering::SeqCst),
            0,
            "calibration requests must wait until upstream health succeeds"
        );
    }

    #[tokio::test]
    async fn recovery_canary_runs_after_initial_health_activation() {
        let canary_failures_remaining = Arc::new(AtomicUsize::new(1));
        let base_url = spawn_test_server(TestServerState {
            completion_tokens: 1,
            prompt_too_long_above: None,
            calibration_barrier: None,
            completion_delay: None,
            in_flight: None,
            max_in_flight: None,
            canary_failures_remaining: Some(canary_failures_remaining),
            health_ok: None,
        })
        .await;
        let (lifecycle_tx, lifecycle_rx) = flume::bounded(32);
        let (stop_tx, stop_rx) = watch::channel(false);
        let task = tokio::spawn(run_bringup_task(
            BringupTaskConfig {
                upstream_http_base_url: base_url,
                model_id: "test-model".to_string(),
                config: BringupConfig {
                    calibration_requests: 0,
                    active_canary_interval: Duration::from_millis(10),
                    canary_timeout: Duration::from_secs(1),
                    canary_max_generation_threshold: 7,
                    ..BringupConfig::default()
                },
                metrics: None,
            },
            lifecycle_tx,
            stop_rx,
        ));

        let mut states = Vec::new();
        let observed = tokio::time::timeout(Duration::from_secs(2), async {
            loop {
                let update = lifecycle_rx.recv_async().await.unwrap();
                states.push(update.state);
                let active_count = states
                    .iter()
                    .filter(|state| **state == ModelBringupState::AdvertisingActive)
                    .count();
                if active_count >= 2 && states.contains(&ModelBringupState::Recovering) {
                    break;
                }
            }
            states
        })
        .await
        .expect("bringup task should recover after one canary failure");

        assert!(observed.contains(&ModelBringupState::Recovering));
        assert!(
            observed
                .iter()
                .filter(|state| **state == ModelBringupState::AdvertisingActive)
                .count()
                >= 2
        );

        let _ = stop_tx.send(true);
        task.await.unwrap();
    }

    #[derive(Clone)]
    struct TestServerState {
        completion_tokens: u32,
        prompt_too_long_above: Option<usize>,
        calibration_barrier: Option<Arc<Barrier>>,
        completion_delay: Option<Duration>,
        in_flight: Option<Arc<AtomicUsize>>,
        max_in_flight: Option<Arc<AtomicUsize>>,
        canary_failures_remaining: Option<Arc<AtomicUsize>>,
        health_ok: Option<Arc<AtomicBool>>,
    }

    async fn spawn_test_server(state: TestServerState) -> String {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let state = Arc::new(Mutex::new(state));
        let app = Router::new()
            .route("/health", get(test_health))
            .route("/v1/chat/completions", post(test_chat_completion))
            .with_state(state);
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        format!("http://{addr}")
    }

    async fn test_health(
        State(state): State<Arc<Mutex<TestServerState>>>,
    ) -> axum::response::Response {
        let state = state.lock().await.clone();
        if state
            .health_ok
            .as_ref()
            .is_some_and(|health_ok| !health_ok.load(Ordering::SeqCst))
        {
            return StatusCode::SERVICE_UNAVAILABLE.into_response();
        }
        "ok".into_response()
    }

    async fn test_chat_completion(
        State(state): State<Arc<Mutex<TestServerState>>>,
        Json(request): Json<Value>,
    ) -> axum::response::Response {
        let state = state.lock().await.clone();
        let prompt = request
            .get("messages")
            .and_then(|value| value.as_array())
            .and_then(|messages| messages.first())
            .and_then(|message| message.get("content"))
            .and_then(|value| value.as_str())
            .unwrap_or_default();
        let prompt_len = prompt.len();
        if let Some(in_flight) = &state.in_flight {
            let current = in_flight.fetch_add(1, Ordering::SeqCst) + 1;
            if let Some(max_in_flight) = &state.max_in_flight {
                let mut observed = max_in_flight.load(Ordering::SeqCst);
                while current > observed {
                    match max_in_flight.compare_exchange(
                        observed,
                        current,
                        Ordering::SeqCst,
                        Ordering::SeqCst,
                    ) {
                        Ok(_) => break,
                        Err(next_observed) => observed = next_observed,
                    }
                }
            }
        }
        if let Some(barrier) = &state.calibration_barrier {
            barrier.wait().await;
        }
        if let Some(delay) = state.completion_delay {
            tokio::time::sleep(delay).await;
        }
        if let Some(in_flight) = &state.in_flight {
            in_flight.fetch_sub(1, Ordering::SeqCst);
        }
        if let Some(limit) = state.prompt_too_long_above
            && prompt_len > limit
        {
            return (
                StatusCode::BAD_REQUEST,
                Json(serde_json::json!({
                    "error": {"message": "Prompt too long"}
                })),
            )
                .into_response();
        }

        let mut completion_tokens = state.completion_tokens;
        if prompt == "1+1="
            && state
                .canary_failures_remaining
                .as_ref()
                .is_some_and(|remaining| {
                    remaining
                        .fetch_update(Ordering::SeqCst, Ordering::SeqCst, |value| {
                            (value > 0).then(|| value - 1)
                        })
                        .is_ok()
                })
        {
            completion_tokens = request
                .get("max_tokens")
                .and_then(|value| value.as_u64())
                .and_then(|value| u32::try_from(value).ok())
                .unwrap_or(completion_tokens);
        }

        Json(serde_json::json!({
            "usage": {"completion_tokens": completion_tokens}
        }))
        .into_response()
    }
}

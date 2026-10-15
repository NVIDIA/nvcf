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

use futures::stream::{self, StreamExt, TryStreamExt};

use crate::generated_request_id::GeneratedRequestKind;
use crate::runtime_state::{ModelGeneration, PylonRuntimeState};
use crate::stats::StatsCollectorControl;

use super::CalibrationConfig;
use super::upstream::{BringupError, send_completion_request};

pub(super) const CALIBRATION_PROMPT_UNITS_FLOOR: usize = 256;

pub(crate) async fn run_calibration(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    generation: &ModelGeneration,
    config: &CalibrationConfig,
    stats_control: &StatsCollectorControl,
    runtime_state: &PylonRuntimeState,
) -> Result<(), BringupError> {
    let model_id = generation.model_id();
    if config.calibration_requests == 0 {
        return Err(BringupError::InvalidCalibrationConfig(
            "calibration_requests must be greater than zero",
        ));
    }

    let started_at = Instant::now();
    let mut last_completed = None;
    let mut completed_prompt_units = Vec::new();

    let mut ramp = calibration_ramp(config);
    while let Some(step) = ramp.next() {
        match send_calibration_batch(
            http_client,
            upstream_http_base_url,
            generation,
            config.calibration_timeout,
            step,
            runtime_state,
        )
        .await
        {
            Ok(CalibrationStepOutcome::Completed) => {
                if completed_prompt_units.last() != Some(&step.prompt_units) {
                    completed_prompt_units.push(step.prompt_units);
                }
                last_completed = Some(step);
            }
            Ok(CalibrationStepOutcome::Saturated) => {
                let stats = stats_control
                    .flush_and_snapshot(generation)
                    .await
                    .map_err(|_| BringupError::StatsCollectorStopped)?
                    .ok_or(BringupError::RetiredGeneration)?;
                tracing::info!(
                    model_id,
                    last_completed_prompt_units = last_completed
                        .map_or(0, |step| step.prompt_units),
                    last_completed_concurrency = last_completed
                        .map_or(0, |step| step.concurrency),
                    last_completed_request_count = last_completed
                        .map_or(0, |step| step.request_count),
                    timed_out_prompt_units = step.prompt_units,
                    timed_out_request_count = step.request_count,
                    timed_out_concurrency = step.concurrency,
                    duration_ms = started_at.elapsed().as_secs_f64() * 1_000.0,
                    current_input_tps = stats.last_mean_input_tps,
                    current_output_tps = stats.output_tps,
                    current_queue_size = stats.queue_size,
                    current_queued_input_size = stats.queued_input_size,
                    current_stats = ?stats,
                    "model calibration reached its saturation timeout"
                );
                return Ok(());
            }
            Err(BringupError::PromptTooLong) => {
                let Some(prompt_units) = completed_prompt_units
                    .iter()
                    .rev()
                    .copied()
                    .find(|prompt_units| *prompt_units < step.prompt_units)
                else {
                    return Err(BringupError::PromptTooLong);
                };
                ramp.clamp_prompt_units(prompt_units);
                tracing::info!(
                    model_id,
                    rejected_prompt_units = step.prompt_units,
                    calibration_prompt_units = prompt_units,
                    "calibration prompt exceeded the model context; continuing at the last completed prompt size"
                );
            }
            Err(error) => return Err(error),
        }
    }

    unreachable!("calibration ramp is unbounded and exits only on timeout or error")
}

#[derive(Clone, Copy, Debug)]
pub(super) struct CalibrationBatch {
    pub(super) prompt_units: usize,
    pub(super) request_count: usize,
    pub(super) concurrency: usize,
}

pub(super) fn calibration_ramp(config: &CalibrationConfig) -> CalibrationRamp {
    CalibrationRamp {
        max_prompt_units: config
            .calibration_prompt_units
            .max(CALIBRATION_PROMPT_UNITS_FLOOR),
        max_concurrency: config.calibration_max_concurrency.max(1),
        next: CalibrationBatch {
            prompt_units: CALIBRATION_PROMPT_UNITS_FLOOR,
            request_count: config.calibration_requests,
            concurrency: 1,
        },
    }
}

pub(super) struct CalibrationRamp {
    max_prompt_units: usize,
    max_concurrency: usize,
    next: CalibrationBatch,
}

impl Iterator for CalibrationRamp {
    type Item = CalibrationBatch;

    fn next(&mut self) -> Option<Self::Item> {
        let current = self.next;
        self.next = CalibrationBatch {
            prompt_units: current
                .prompt_units
                .saturating_mul(2)
                .min(self.max_prompt_units),
            request_count: current.request_count.saturating_mul(2),
            concurrency: current
                .concurrency
                .saturating_add(1)
                .min(self.max_concurrency),
        };
        Some(current)
    }
}

impl CalibrationRamp {
    fn clamp_prompt_units(&mut self, prompt_units: usize) {
        self.max_prompt_units = prompt_units.max(CALIBRATION_PROMPT_UNITS_FLOOR);
        self.next.prompt_units = self.next.prompt_units.min(self.max_prompt_units);
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(super) enum CalibrationStepOutcome {
    Completed,
    Saturated,
}

pub(super) async fn send_calibration_batch(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    generation: &ModelGeneration,
    timeout: Duration,
    batch: CalibrationBatch,
    runtime_state: &PylonRuntimeState,
) -> Result<CalibrationStepOutcome, BringupError> {
    let CalibrationBatch {
        prompt_units,
        request_count,
        concurrency,
    } = batch;
    let request = serde_json::json!({
        "model": generation.model_id(),
        "messages": [{"role": "user", "content": "1".repeat(prompt_units)}],
        "max_tokens": 1,
        "seed": 33,
        "temperature": 0.7,
        "top_p": 1.0,
        "stream": false,
    });

    let step = stream::iter((0..request_count).map(|_| {
        send_completion_request(
            http_client,
            upstream_http_base_url,
            None,
            &request,
            GeneratedRequestKind::Calibration,
            generation,
            Some(runtime_state),
        )
    }))
    .buffer_unordered(concurrency.min(request_count))
    .try_for_each(|_| async { Ok(()) });
    match tokio::time::timeout(timeout, step).await {
        Ok(outcome) => outcome.map(|()| CalibrationStepOutcome::Completed),
        Err(_) => Ok(CalibrationStepOutcome::Saturated),
    }
}

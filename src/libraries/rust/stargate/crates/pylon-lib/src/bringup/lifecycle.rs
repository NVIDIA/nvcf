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

use tokio_util::sync::CancellationToken;

use crate::runtime_state::{ModelGeneration, PylonRuntimeState};
use crate::stats::{CanaryPhase, CanaryResult};

use super::upstream::{BringupError, check_upstream_health, send_canary_request};
use super::{BringupConfig, BringupTaskConfig};

const CONNECT_RETRY_INTERVAL: Duration = Duration::from_secs(1);

struct CanaryProbe<'a> {
    http_client: reqwest::Client,
    upstream_http_base_url: &'a str,
    generation: &'a ModelGeneration,
    config: &'a BringupConfig,
    runtime_state: &'a PylonRuntimeState,
}

enum CanaryOutcome {
    Passed,
    /// An active canary timed out, but the upstream made progress on other
    /// requests while it waited. A timeout under load is queueing, not proof
    /// that the model stopped serving. Only response timeouts qualify:
    /// explicit rejections, connection errors, and a canary window with no
    /// output from any request still count as failures, and recovery
    /// canaries must pass because a demoted model receives no traffic of its
    /// own to prove it recovered.
    TimedOutUpstreamBusy(BringupError),
    Failed(BringupError),
}

impl CanaryProbe<'_> {
    /// Sends one canary and records its result. Only an active-phase response
    /// timeout that overlaps upstream progress is `TimedOutUpstreamBusy`.
    async fn send(&self, phase: CanaryPhase) -> CanaryOutcome {
        let started_at = Instant::now();
        let outcome = match send_canary_request(
            &self.http_client,
            self.upstream_http_base_url,
            self.generation,
            self.config.canary_timeout,
            self.config.canary_max_generation_threshold,
        )
        .await
        {
            Ok(()) => CanaryOutcome::Passed,
            Err(error)
                if phase == CanaryPhase::Active
                    && error.is_timeout()
                    && self.runtime_state.upstream_progressed_since(started_at) =>
            {
                CanaryOutcome::TimedOutUpstreamBusy(error)
            }
            Err(error) => CanaryOutcome::Failed(error),
        };
        self.record(
            phase,
            match outcome {
                CanaryOutcome::Passed => CanaryResult::Passed,
                CanaryOutcome::TimedOutUpstreamBusy(_) => CanaryResult::TimedOutUpstreamBusy,
                CanaryOutcome::Failed(_) => CanaryResult::Failed,
            },
        );
        outcome
    }

    /// Mirrors Dynamo's idle-only canaries: recent request progress on this
    /// model already proves it is serving, so a synthetic request would only
    /// add load to a busy backend.
    fn model_recently_progressed(&self) -> bool {
        Instant::now()
            .checked_sub(self.config.active_canary_interval)
            .is_some_and(|since| {
                self.runtime_state
                    .generation_progressed_since(self.generation, since)
            })
    }

    fn record(&self, phase: CanaryPhase, result: CanaryResult) {
        if let Some(metrics) = self.runtime_state.metrics() {
            metrics.observe_model_canary_result(self.generation.model_id(), phase, result);
        }
    }
}

/// Canaries run on a fixed interval that can match an upstream's keep-alive
/// timeout (vLLM and uvicorn default to 5 s), so a pooled connection may be
/// closed by the server just as the next canary reuses it. A fresh connection
/// per probe keeps that race from failing the canary. The connect timeout is
/// shorter than the canary timeout so a connection that cannot be established
/// surfaces as a connect error instead of an inconclusive timeout.
pub(super) fn canary_http_client(canary_timeout: Duration) -> reqwest::Client {
    reqwest::Client::builder()
        .pool_max_idle_per_host(0)
        .connect_timeout(canary_timeout / 2)
        .build()
        .unwrap_or_else(|error| {
            tracing::warn!(error = %error, "failed to build canary HTTP client; using defaults");
            reqwest::Client::new()
        })
}

pub(crate) async fn run_bringup_task(
    task_config: BringupTaskConfig,
    runtime_state: PylonRuntimeState,
    stop: CancellationToken,
) {
    let BringupTaskConfig {
        upstream_http_base_url,
        generation,
        config,
        health_paths,
    } = task_config;
    if let Some(metrics) = runtime_state.metrics() {
        metrics.init_model_canary_results(generation.model_id());
    }
    let probe = CanaryProbe {
        http_client: canary_http_client(config.canary_timeout),
        upstream_http_base_url: &upstream_http_base_url,
        generation: &generation,
        config: &config,
        runtime_state: &runtime_state,
    };

    loop {
        if !wait_for_active_canary_failure(&probe, &stop).await {
            return;
        }
        if !runtime_state.set_generation_bringup_ready(&generation, false) {
            return;
        }

        loop {
            let Some(upstream_healthy) = stop
                .run_until_cancelled(check_upstream_health(
                    &probe.http_client,
                    &upstream_http_base_url,
                    config.canary_timeout,
                    &health_paths,
                ))
                .await
            else {
                return;
            };
            if !upstream_healthy {
                if wait_or_stop(&stop, CONNECT_RETRY_INTERVAL).await {
                    return;
                }
                continue;
            }

            let Some(canary_outcome) = stop
                .run_until_cancelled(probe.send(CanaryPhase::Recovery))
                .await
            else {
                return;
            };
            match canary_outcome {
                CanaryOutcome::Passed => {}
                CanaryOutcome::TimedOutUpstreamBusy(error) | CanaryOutcome::Failed(error) => {
                    tracing::warn!(
                        model_id = generation.model_id(),
                        error = %error,
                        "bringup recovery canary failed"
                    );
                    if wait_or_stop(&stop, CONNECT_RETRY_INTERVAL).await {
                        return;
                    }
                    continue;
                }
            }

            if !runtime_state.set_generation_bringup_ready(&generation, true) {
                return;
            }
            break;
        }
    }
}

/// Runs active canaries until one fails. Returns `true` on a failure that
/// should demote the model and `false` when stopped or active canaries are
/// disabled.
async fn wait_for_active_canary_failure(probe: &CanaryProbe<'_>, stop: &CancellationToken) -> bool {
    if probe.config.active_canary_interval.is_zero() {
        stop.cancelled().await;
        return false;
    }

    let mut canary_interval = tokio::time::interval(probe.config.active_canary_interval);
    canary_interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    canary_interval.tick().await;

    loop {
        tokio::select! {
            _ = stop.cancelled() => return false,
            _ = canary_interval.tick() => {
                if probe.model_recently_progressed() {
                    probe.record(CanaryPhase::Active, CanaryResult::SkippedRecentProgress);
                    continue;
                }
                let Some(canary_outcome) = stop
                    .run_until_cancelled(probe.send(CanaryPhase::Active))
                    .await
                else {
                    return false;
                };
                match canary_outcome {
                    CanaryOutcome::Passed => {}
                    CanaryOutcome::TimedOutUpstreamBusy(error) => {
                        tracing::info!(
                            model_id = probe.generation.model_id(),
                            error = %error,
                            "active canary timed out while the upstream served other requests; keeping model ready"
                        );
                    }
                    CanaryOutcome::Failed(error) => {
                        tracing::warn!(
                            model_id = probe.generation.model_id(),
                            error = %error,
                            "active canary failed"
                        );
                        return true;
                    }
                }
            }
        }
    }
}

pub(super) async fn wait_or_stop(stop: &CancellationToken, duration: Duration) -> bool {
    stop.run_until_cancelled(tokio::time::sleep(duration))
        .await
        .is_none()
}

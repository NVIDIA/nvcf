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

use tokio_util::sync::CancellationToken;

use crate::runtime_state::PylonRuntimeState;

use super::upstream::{check_upstream_health, send_canary_request};
use super::{BringupConfig, BringupTaskConfig};

const CONNECT_RETRY_INTERVAL: Duration = Duration::from_secs(1);

pub(crate) async fn run_bringup_task(
    task_config: BringupTaskConfig,
    runtime_state: PylonRuntimeState,
    stop: CancellationToken,
) {
    let BringupTaskConfig {
        upstream_http_base_url,
        generation,
        config,
    } = task_config;
    let http_client = reqwest::Client::new();

    loop {
        if !wait_for_active_canary_failure(
            &http_client,
            &upstream_http_base_url,
            &generation,
            &config,
            &stop,
        )
        .await
        {
            return;
        }
        if !runtime_state.set_generation_bringup_ready(&generation, false) {
            return;
        }

        loop {
            let Some(upstream_healthy) = stop
                .run_until_cancelled(check_upstream_health(
                    &http_client,
                    &upstream_http_base_url,
                    config.canary_timeout,
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

            let Some(canary_result) = stop
                .run_until_cancelled(send_canary_request(
                    &http_client,
                    &upstream_http_base_url,
                    &generation,
                    config.canary_timeout,
                    config.canary_max_generation_threshold,
                ))
                .await
            else {
                return;
            };
            if let Err(error) = canary_result {
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

            if !runtime_state.set_generation_bringup_ready(&generation, true) {
                return;
            }
            break;
        }
    }
}

async fn wait_for_active_canary_failure(
    http_client: &reqwest::Client,
    upstream_http_base_url: &str,
    generation: &crate::runtime_state::ModelGeneration,
    config: &BringupConfig,
    stop: &CancellationToken,
) -> bool {
    if config.active_canary_interval.is_zero() {
        stop.cancelled().await;
        return false;
    }

    let mut canary_interval = tokio::time::interval(config.active_canary_interval);
    canary_interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    canary_interval.tick().await;

    loop {
        tokio::select! {
            _ = stop.cancelled() => return false,
            _ = canary_interval.tick() => {
                let Some(canary_result) = stop
                    .run_until_cancelled(send_canary_request(
                        http_client,
                        upstream_http_base_url,
                        generation,
                        config.canary_timeout,
                        config.canary_max_generation_threshold,
                    ))
                    .await
                else {
                    return false;
                };
                if let Err(error) = canary_result {
                    tracing::warn!(
                        model_id = generation.model_id(),
                        error = %error,
                        "active canary failed"
                    );
                    return true;
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

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

macro_rules! owned_task_handle {
    ($name:ident) => {
        pub struct $name {
            task: stargate_runtime::OwnedTask,
        }

        impl $name {
            pub async fn wait_for_exit(&mut self) -> Result<(), tokio::task::JoinError> {
                self.task.wait_for_exit().await
            }

            pub async fn shutdown(self) {
                self.task
                    .shutdown(stargate_runtime::TASK_SHUTDOWN_TIMEOUT)
                    .await;
            }
        }
    };
}

mod aggregator;
mod collector;
mod engine_stats_stream;
mod metrics;
mod projection;
pub(crate) mod token_metrics;

pub(crate) use collector::{ModelStatsInitialization, StatsCollectorControl};
pub use collector::{
    RequestCounterUpdate, RequestCounterUpdateInput, StatsAggregatorUpdate, StatsCollectorConfig,
    StatsCollectorHandle, StatsUpdateSource, start_stats_collector,
    start_stats_collector_with_engine_stats, stats_aggregator_update_channel,
};
pub use engine_stats_stream::{
    EngineStatsStreamConfig, EngineStatsStreamHandle, EngineStatsStreamMode,
    parse_engine_stats_line_for_benchmark, start_engine_stats_stream,
};
pub(crate) use metrics::CalibrationOutcome;
pub use metrics::{MetricsServerHandle, PylonMetrics, start_metrics_server};

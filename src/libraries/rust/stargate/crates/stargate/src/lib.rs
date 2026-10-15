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

pub mod auth;
pub(crate) mod control_plane;
pub mod discovery;
pub(crate) mod http_proxy;
pub mod load_balancer;
pub mod metrics;
pub(crate) mod routing_state;
pub mod runtime;
pub(crate) mod tunnel;

pub(crate) mod queue_estimate {
    use stargate_proto::pb::ModelStats;
    use stargate_protocol::common::queue_time_delta_ms;

    pub(crate) fn queue_time_estimate_ms_for_priority(
        stats: &ModelStats,
        priority: u32,
    ) -> Option<u64> {
        if !stats.queue_time_estimate_ms_by_priority.is_empty() {
            return priority_map_estimate_ms_for_priority(stats, priority).or(Some(0));
        }
        aggregate_queue_time_estimate_ms(stats)
    }

    pub(crate) fn priority_map_estimate_ms_for_priority(
        stats: &ModelStats,
        priority: u32,
    ) -> Option<u64> {
        stats
            .queue_time_estimate_ms_by_priority
            .iter()
            .filter(|(candidate_priority, _)| **candidate_priority <= priority)
            .max_by_key(|(candidate_priority, _)| **candidate_priority)
            .map(|(_, queue_time_ms)| *queue_time_ms)
    }

    pub(crate) fn aggregate_queue_time_estimate_ms(stats: &ModelStats) -> Option<u64> {
        queue_time_delta_ms(stats.queued_input_size, stats.last_mean_input_tps)
    }
}

pub mod telemetry {
    pub const DEFAULT_SERVICE_NAME: &str = "stargate";
    pub use stargate_telemetry::{
        TelemetryGuard, inject_trace_context, parent_context_from_headers, traceparent_from_headers,
    };

    pub fn init_telemetry(
        endpoint: Option<&str>,
        service_name: &str,
        access_token: Option<&str>,
    ) -> anyhow::Result<TelemetryGuard> {
        stargate_telemetry::init_telemetry(
            endpoint,
            service_name,
            "proxy_openai_request",
            access_token,
        )
    }
}

pub mod proxy {
    pub use crate::http_proxy::{ProxyRetryConfig, ProxyTransportConfig};
    pub use crate::tunnel::QuicTunnelConfig;
}

pub mod registration {
    pub use crate::control_plane::{
        DEFAULT_REGISTRATION_UPDATE_IDLE_TIMEOUT, DEFAULT_REGISTRATION_UPDATE_MAX_IDLE_TIMEOUT,
    };
}

pub mod routing {
    pub use crate::routing_state::{
        RoutedClusterSnapshot, RoutedInferenceServerSnapshot, RoutingTargetKey,
    };
}

pub mod test_support {
    pub use crate::routing_state::StargateState;
}

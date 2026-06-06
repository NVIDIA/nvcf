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
pub(crate) mod queue_estimate;
pub(crate) mod routing_state;
pub mod runtime;
pub mod telemetry;
pub(crate) mod tunnel;

pub mod proxy {
    pub use crate::http_proxy::{ProxyRetryConfig, ProxyTransportConfig};
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

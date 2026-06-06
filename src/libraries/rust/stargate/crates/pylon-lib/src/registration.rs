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

mod calibration;
mod client;
mod discovery;
mod grpc_endpoint;
mod reverse_tunnel;
mod router_stream;
mod state;
mod task_lifecycle;
#[cfg(test)]
mod tests;
mod types;
mod urls;

pub use client::InferenceServerRegistrationClient;
pub use state::CurrentModelStats;
pub use types::{ClientError, InferenceServerRegistrationConfig, InferenceServerUpdateChannels};

#[cfg(test)]
use calibration::*;
#[cfg(test)]
use discovery::*;
#[cfg(test)]
use grpc_endpoint::*;
#[cfg(test)]
use reverse_tunnel::*;
#[cfg(test)]
use router_stream::*;
#[cfg(test)]
use state::*;
use task_lifecycle::{
    CLUSTER_CALIBRATION_SUBMISSION_TIMEOUT, NamedJoinHandle, REGISTRATION_TASK_SHUTDOWN_TIMEOUT,
    REVERSE_TUNNEL_CONNECT_TIMEOUT, await_named_join_handle, registration_should_stop, should_stop,
    sleep_until_registration_stop, stop_channel_changed,
};
#[cfg(test)]
use types::RegistrationStartPlan;
use urls::normalize_addr;

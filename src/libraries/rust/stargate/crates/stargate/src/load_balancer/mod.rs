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

mod algorithm;
mod config;
mod factory;
mod groq_multiregion;
mod power_of_two;
mod pulsar;
mod pulsar_multiregion;
mod random;
mod request;
mod round_robin;
mod router;
#[cfg(test)]
mod tests;

pub use algorithm::LoadBalancer;
pub(crate) use algorithm::input_work_seconds_for_request;
pub(super) use algorithm::{HashInputBuilder, cache_affinity_key_is_cacheable, input_work_units};
#[cfg(test)]
pub(super) use algorithm::{
    LoadBalancerTestChoiceExt, SelectedCandidateForTest, SelectedClusterForTest, input_work_seconds,
};
pub use config::{
    GroqMultiregionAlgorithmConfig, LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig,
    LoadBalancerAlgorithmOverride, LoadBalancerAlgorithmSettings, LoadBalancerConfig,
    LoadBalancerModelConfig, LoadBalancerRequestPolicy, LoadBalancerRoutingAlgorithmError,
    LoadBalancerSeedError, PulsarAlgorithmConfig, PulsarMultiregionAlgorithmConfig,
};
pub use factory::create_load_balancer_with_config;
pub use request::{LoadBalancerCandidateChoice, LoadBalancerRequest};
pub use router::{
    LoadBalancerAlgorithmResolution, LoadBalancerCandidateSelection, LoadBalancerRouter,
};

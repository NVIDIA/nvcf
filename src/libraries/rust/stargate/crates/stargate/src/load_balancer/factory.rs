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

use super::groq_multiregion::{GroqMultiregionConfig, GroqMultiregionLoadBalancer};
use super::power_of_two::PowerOfTwoLoadBalancer;
use super::pulsar::PulsarLoadBalancer;
use super::pulsar_multiregion::PulsarMultiregionLoadBalancer;
use super::random::RandomLoadBalancer;
use super::round_robin::RoundRobinLoadBalancer;
use super::{LoadBalancer, LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig};

pub fn create_load_balancer_with_config(
    config: &LoadBalancerAlgorithmConfig,
) -> anyhow::Result<Arc<dyn LoadBalancer>> {
    if config.considers_kv_free_tokens()
        && !matches!(
            config.algorithm(),
            LoadBalancerAlgorithm::Pulsar | LoadBalancerAlgorithm::PulsarMultiregion
        )
    {
        anyhow::bail!(
            "consider_kv_free_tokens is supported only for pulsar and pulsar-multiregion"
        );
    }

    match config.algorithm() {
        LoadBalancerAlgorithm::PowerOfTwo => Ok(Arc::new(PowerOfTwoLoadBalancer)),
        LoadBalancerAlgorithm::GroqMultiregion => Ok(Arc::new(GroqMultiregionLoadBalancer::new(
            GroqMultiregionConfig::from_algorithm_config(config),
        ))),
        LoadBalancerAlgorithm::RoundRobin => Ok(Arc::new(RoundRobinLoadBalancer::new())),
        LoadBalancerAlgorithm::Random => Ok(Arc::new(RandomLoadBalancer)),
        LoadBalancerAlgorithm::Pulsar => Ok(Arc::new(PulsarLoadBalancer::new(config.clone()))),
        LoadBalancerAlgorithm::PulsarMultiregion => {
            Ok(Arc::new(PulsarMultiregionLoadBalancer::new(config.clone())))
        }
    }
}

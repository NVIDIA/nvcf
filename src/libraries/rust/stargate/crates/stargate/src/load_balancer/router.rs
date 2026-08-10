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

use std::collections::HashMap;

use crate::routing_state::RoutedClusterSnapshot;

use super::target_state::LoadBalancerDefinition;
use super::{
    LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig, LoadBalancerAlgorithmOverride,
    LoadBalancerCandidateChoice, LoadBalancerConfig, LoadBalancerModelConfig, LoadBalancerRequest,
    LoadBalancerRoutingAlgorithmError, LoadBalancerTargetState,
};

#[cfg(test)]
use super::tests::{SelectedCandidateForTest, SelectedClusterForTest};

struct LoadBalancerAlgorithmConfigSet {
    configured: LoadBalancerDefinition,
    request_algorithms: HashMap<LoadBalancerAlgorithm, LoadBalancerDefinition>,
}

pub struct LoadBalancerRouter {
    default_config: LoadBalancerAlgorithmConfigSet,
    per_model_config: HashMap<String, LoadBalancerAlgorithmConfigSet>,
}

#[derive(Clone, Debug)]
pub struct LoadBalancerCandidateSelection {
    pub choice: LoadBalancerCandidateChoice,
    pub effective_algorithm: LoadBalancerAlgorithm,
    pub requested_algorithm: Option<String>,
}

#[derive(Clone, Debug)]
pub struct LoadBalancerAlgorithmResolution {
    definition: LoadBalancerDefinition,
    requested_algorithm: Option<String>,
}

impl LoadBalancerAlgorithmResolution {
    pub fn config(&self) -> &LoadBalancerAlgorithmConfig {
        self.definition.config()
    }
}

impl LoadBalancerRouter {
    pub fn from_config(config: &LoadBalancerConfig) -> anyhow::Result<Self> {
        let default_config = Self::build_algorithm_config_set(
            LoadBalancerAlgorithmConfig::from(config.default),
            &config.request_algorithms,
        )?;
        let mut per_model_config = HashMap::new();
        for (model_id, model_config) in &config.models {
            let mut algorithm_config = model_config.clone().into_algorithm_config();
            let request_algorithms = std::mem::take(&mut algorithm_config.request_algorithms);
            let config_set =
                Self::build_algorithm_config_set(algorithm_config, &request_algorithms)?;
            per_model_config.insert(model_id.clone(), config_set);
        }
        Ok(Self {
            default_config,
            per_model_config,
        })
    }

    fn build_algorithm_config_set(
        configured: LoadBalancerAlgorithmConfig,
        request_algorithms: &HashMap<LoadBalancerAlgorithm, LoadBalancerModelConfig>,
    ) -> anyhow::Result<LoadBalancerAlgorithmConfigSet> {
        let configured = LoadBalancerDefinition::new(configured)?;
        let request_algorithms = Self::build_request_algorithm_configs(request_algorithms)?;
        Ok(LoadBalancerAlgorithmConfigSet {
            configured,
            request_algorithms,
        })
    }

    fn build_request_algorithm_configs(
        request_algorithms: &HashMap<LoadBalancerAlgorithm, LoadBalancerModelConfig>,
    ) -> anyhow::Result<HashMap<LoadBalancerAlgorithm, LoadBalancerDefinition>> {
        let mut configs = HashMap::new();
        for (algorithm, model_config) in request_algorithms {
            let mut algorithm_config = model_config.clone().into_algorithm_config();
            if algorithm_config.algorithm() != *algorithm {
                anyhow::bail!(
                    "request_algorithms key {algorithm} does not match configured algorithm {}",
                    algorithm_config.algorithm()
                );
            }
            algorithm_config.request_algorithms.clear();
            configs.insert(*algorithm, LoadBalancerDefinition::new(algorithm_config)?);
        }
        Ok(configs)
    }

    fn algorithm_config_set(&self, model_id: &str) -> &LoadBalancerAlgorithmConfigSet {
        self.per_model_config
            .get(model_id)
            .unwrap_or(&self.default_config)
    }

    fn request_algorithm_config_for_override<'a>(
        &'a self,
        config_set: &'a LoadBalancerAlgorithmConfigSet,
        algorithm_override: &LoadBalancerAlgorithmOverride,
    ) -> Result<&'a LoadBalancerDefinition, LoadBalancerRoutingAlgorithmError> {
        let raw = algorithm_override.requested_algorithm();
        let algorithm = algorithm_override.algorithm();

        if config_set.configured.config().algorithm() == algorithm {
            return Ok(&config_set.configured);
        }

        if let Some(definition) = config_set.request_algorithms.get(&algorithm) {
            return Ok(definition);
        }

        if !std::ptr::eq(config_set, &self.default_config)
            && let Some(definition) = self.default_config.request_algorithms.get(&algorithm)
        {
            return Ok(definition);
        }

        Err(LoadBalancerRoutingAlgorithmError::Unavailable {
            raw: raw.to_string(),
            algorithm,
        })
    }

    pub fn choose_candidate(
        &self,
        target_state: &LoadBalancerTargetState,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if candidates.is_empty() {
            return None;
        }

        let definition = &self
            .algorithm_config_set(&request.routing_target.model_id)
            .configured;
        let lb = target_state.load_balancer(definition);
        lb.choose_candidate(request, candidates)
    }

    pub fn choose_candidate_with_algorithm_override(
        &self,
        target_state: &LoadBalancerTargetState,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        algorithm_override: Option<&LoadBalancerAlgorithmOverride>,
    ) -> Result<Option<LoadBalancerCandidateSelection>, LoadBalancerRoutingAlgorithmError> {
        let resolution =
            self.resolve_algorithm_override(&request.routing_target.model_id, algorithm_override)?;
        Ok(self.choose_candidate_with_algorithm_resolution(
            target_state,
            request,
            candidates,
            &resolution,
        ))
    }

    pub fn choose_candidate_with_algorithm_resolution(
        &self,
        target_state: &LoadBalancerTargetState,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        resolution: &LoadBalancerAlgorithmResolution,
    ) -> Option<LoadBalancerCandidateSelection> {
        if candidates.is_empty() {
            return None;
        }

        let lb = target_state.load_balancer(&resolution.definition);
        let effective_algorithm = resolution.config().algorithm();
        let requested_algorithm = resolution.requested_algorithm.clone();

        lb.choose_candidate(request, candidates)
            .map(|choice| LoadBalancerCandidateSelection {
                choice,
                effective_algorithm,
                requested_algorithm,
            })
    }

    pub fn algorithm_name(&self, model_id: &str) -> String {
        self.algorithm_config(model_id).algorithm().to_string()
    }

    pub fn algorithm_config(&self, model_id: &str) -> &LoadBalancerAlgorithmConfig {
        self.algorithm_config_set(model_id).configured.config()
    }

    pub fn resolve_algorithm_override(
        &self,
        model_id: &str,
        algorithm_override: Option<&LoadBalancerAlgorithmOverride>,
    ) -> Result<LoadBalancerAlgorithmResolution, LoadBalancerRoutingAlgorithmError> {
        let config_set = self.algorithm_config_set(model_id);
        let definition = if let Some(algorithm_override) = algorithm_override {
            self.request_algorithm_config_for_override(config_set, algorithm_override)?
        } else {
            &config_set.configured
        };
        Ok(LoadBalancerAlgorithmResolution {
            definition: definition.clone(),
            requested_algorithm: algorithm_override
                .map(LoadBalancerAlgorithmOverride::requested_algorithm)
                .map(ToOwned::to_owned),
        })
    }
}

#[cfg(test)]
impl LoadBalancerRouter {
    pub(super) fn choose_for_test(
        &self,
        target_state: &LoadBalancerTargetState,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<SelectedCandidateForTest> {
        self.choose_candidate(target_state, request, candidates)
            .map(|choice| SelectedCandidateForTest {
                candidate: SelectedClusterForTest {
                    cluster_id: candidates[choice.candidate_index].cluster_id.clone(),
                },
                rank_depth: choice.rank_depth,
                selected_after_kv_free_tokens_skip: choice.selected_after_kv_free_tokens_skip,
            })
    }
}

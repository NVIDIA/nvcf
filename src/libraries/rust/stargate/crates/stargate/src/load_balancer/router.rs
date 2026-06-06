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
use std::sync::Arc;

use scc::HashMap as SccHashMap;

use crate::routing_state::{RoutedClusterSnapshot, RoutingTargetKey};

use super::{
    LoadBalancer, LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig,
    LoadBalancerAlgorithmOverride, LoadBalancerCandidateChoice, LoadBalancerConfig,
    LoadBalancerModelConfig, LoadBalancerRequest, LoadBalancerRoutingAlgorithmError,
    create_load_balancer_with_config,
};

#[cfg(test)]
use super::{SelectedCandidateForTest, SelectedClusterForTest};

struct LoadBalancerAlgorithmConfigSet {
    configured: LoadBalancerAlgorithmConfig,
    request_algorithms: HashMap<LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig>,
}

pub struct LoadBalancerRouter {
    default_config: LoadBalancerAlgorithmConfigSet,
    default_per_target: SccHashMap<RoutingTargetKey, Arc<dyn LoadBalancer>>,
    configured_per_target: SccHashMap<RoutingTargetKey, Arc<dyn LoadBalancer>>,
    request_per_target: SccHashMap<LoadBalancerOverrideKey, Arc<dyn LoadBalancer>>,
    per_model_config: HashMap<String, LoadBalancerAlgorithmConfigSet>,
}

#[derive(Clone, Debug)]
pub struct LoadBalancerCandidateSelection {
    pub choice: LoadBalancerCandidateChoice,
    pub effective_algorithm: LoadBalancerAlgorithm,
    pub requested_algorithm: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
struct LoadBalancerOverrideKey {
    routing_target: RoutingTargetKey,
    algorithm: LoadBalancerAlgorithm,
}

#[derive(Clone, Debug)]
pub struct LoadBalancerAlgorithmResolution {
    config: LoadBalancerAlgorithmConfig,
    use_request_instances: bool,
    requested_algorithm: Option<String>,
}

impl LoadBalancerAlgorithmResolution {
    pub fn config(&self) -> &LoadBalancerAlgorithmConfig {
        &self.config
    }

    fn effective_algorithm(&self) -> LoadBalancerAlgorithm {
        self.config.algorithm()
    }

    fn requested_algorithm(&self) -> Option<String> {
        self.requested_algorithm.clone()
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
            default_per_target: SccHashMap::default(),
            configured_per_target: SccHashMap::default(),
            request_per_target: SccHashMap::default(),
            per_model_config,
        })
    }

    fn build_algorithm_config_set(
        configured: LoadBalancerAlgorithmConfig,
        request_algorithms: &HashMap<LoadBalancerAlgorithm, LoadBalancerModelConfig>,
    ) -> anyhow::Result<LoadBalancerAlgorithmConfigSet> {
        // Validate configured algorithms at startup. Stateful instances are
        // created lazily per routing target to preserve routing-key isolation.
        let _ = create_load_balancer_with_config(&configured)?;
        let request_algorithms = Self::build_request_algorithm_configs(request_algorithms)?;
        Ok(LoadBalancerAlgorithmConfigSet {
            configured,
            request_algorithms,
        })
    }

    fn build_request_algorithm_configs(
        request_algorithms: &HashMap<LoadBalancerAlgorithm, LoadBalancerModelConfig>,
    ) -> anyhow::Result<HashMap<LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig>> {
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
            let _ = create_load_balancer_with_config(&algorithm_config)?;
            configs.insert(*algorithm, algorithm_config);
        }
        Ok(configs)
    }

    fn load_balancer_for_target(
        instances: &SccHashMap<RoutingTargetKey, Arc<dyn LoadBalancer>>,
        target: &RoutingTargetKey,
        config: &LoadBalancerAlgorithmConfig,
    ) -> Arc<dyn LoadBalancer> {
        if let Some(lb) = instances.read_sync(target, |_target, lb| lb.clone()) {
            return lb;
        }

        let lb = create_load_balancer_with_config(config)
            .expect("load balancer config validated during router construction");
        if instances.insert_sync(target.clone(), lb.clone()).is_ok() {
            return lb;
        }

        instances
            .read_sync(target, |_target, lb| lb.clone())
            .expect("per-target load balancer should exist after insert race")
    }

    fn configured_or_default_config_source(
        &self,
        target: &RoutingTargetKey,
    ) -> (
        &SccHashMap<RoutingTargetKey, Arc<dyn LoadBalancer>>,
        &LoadBalancerAlgorithmConfigSet,
    ) {
        let (instances, config_set) =
            if let Some(config_set) = self.per_model_config.get(&target.model_id) {
                (&self.configured_per_target, config_set)
            } else {
                (&self.default_per_target, &self.default_config)
            };
        (instances, config_set)
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
    ) -> Result<(&'a LoadBalancerAlgorithmConfig, bool), LoadBalancerRoutingAlgorithmError> {
        let raw = algorithm_override.requested_algorithm();
        let algorithm = algorithm_override.algorithm();

        if config_set.configured.algorithm() == algorithm {
            return Ok((&config_set.configured, false));
        }

        if let Some(config) = config_set.request_algorithms.get(&algorithm) {
            return Ok((config, true));
        }

        if !std::ptr::eq(config_set, &self.default_config)
            && let Some(config) = self.default_config.request_algorithms.get(&algorithm)
        {
            return Ok((config, true));
        }

        Err(LoadBalancerRoutingAlgorithmError::Unavailable {
            raw: raw.to_string(),
            algorithm,
        })
    }

    fn request_load_balancer_for_target(
        &self,
        target: &RoutingTargetKey,
        config: &LoadBalancerAlgorithmConfig,
    ) -> Arc<dyn LoadBalancer> {
        let key = LoadBalancerOverrideKey {
            routing_target: target.clone(),
            algorithm: config.algorithm(),
        };
        if let Some(lb) = self
            .request_per_target
            .read_sync(&key, |_key, lb| lb.clone())
        {
            return lb;
        }

        let lb = create_load_balancer_with_config(config)
            .expect("request load balancer config validated during router construction");
        if self
            .request_per_target
            .insert_sync(key.clone(), lb.clone())
            .is_ok()
        {
            return lb;
        }

        self.request_per_target
            .read_sync(&key, |_key, lb| lb.clone())
            .expect("request per-target load balancer should exist after insert race")
    }

    fn load_balancer_for_algorithm_resolution(
        &self,
        request: &LoadBalancerRequest<'_>,
        resolution: &LoadBalancerAlgorithmResolution,
    ) -> Arc<dyn LoadBalancer> {
        let (configured_instances, _) =
            self.configured_or_default_config_source(request.routing_target);
        if resolution.use_request_instances {
            self.request_load_balancer_for_target(request.routing_target, resolution.config())
        } else {
            Self::load_balancer_for_target(
                configured_instances,
                request.routing_target,
                resolution.config(),
            )
        }
    }

    pub fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if candidates.is_empty() {
            return None;
        }

        // `choose_candidate` is the no-request-override hot path used by the
        // proxy and load-balancer microbenchmarks. Avoid constructing a cloned
        // `LoadBalancerAlgorithmResolution` just to recover the model's
        // configured algorithm immediately afterward.
        let (instances, config_set) =
            self.configured_or_default_config_source(request.routing_target);
        let lb = Self::load_balancer_for_target(
            instances,
            request.routing_target,
            &config_set.configured,
        );
        lb.choose_candidate(request, candidates)
    }

    pub fn choose_candidate_with_algorithm_override(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        algorithm_override: Option<&LoadBalancerAlgorithmOverride>,
    ) -> Result<Option<LoadBalancerCandidateSelection>, LoadBalancerRoutingAlgorithmError> {
        let resolution =
            self.resolve_algorithm_override(&request.routing_target.model_id, algorithm_override)?;
        Ok(self.choose_candidate_with_algorithm_resolution(request, candidates, &resolution))
    }

    pub fn choose_candidate_with_algorithm_resolution(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        resolution: &LoadBalancerAlgorithmResolution,
    ) -> Option<LoadBalancerCandidateSelection> {
        if candidates.is_empty() {
            return None;
        }

        let lb = self.load_balancer_for_algorithm_resolution(request, resolution);
        let effective_algorithm = resolution.effective_algorithm();
        let requested_algorithm = resolution.requested_algorithm();

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
        &self.algorithm_config_set(model_id).configured
    }

    pub fn resolve_algorithm_override(
        &self,
        model_id: &str,
        algorithm_override: Option<&LoadBalancerAlgorithmOverride>,
    ) -> Result<LoadBalancerAlgorithmResolution, LoadBalancerRoutingAlgorithmError> {
        let config_set = self.algorithm_config_set(model_id);
        let (config, use_request_instances) = if let Some(algorithm_override) = algorithm_override {
            self.request_algorithm_config_for_override(config_set, algorithm_override)?
        } else {
            (&config_set.configured, false)
        };
        Ok(LoadBalancerAlgorithmResolution {
            config: config.clone(),
            use_request_instances,
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
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<SelectedCandidateForTest> {
        self.choose_candidate(request, candidates)
            .map(|choice| SelectedCandidateForTest {
                candidate: SelectedClusterForTest {
                    cluster_id: candidates[choice.candidate_index].cluster_id.clone(),
                },
                rank_depth: choice.rank_depth,
                selected_after_kv_free_tokens_skip: choice.selected_after_kv_free_tokens_skip,
            })
    }

    pub(super) fn default_per_target_count(&self) -> usize {
        self.default_per_target.len()
    }

    pub(super) fn request_per_target_count(&self) -> usize {
        self.request_per_target.len()
    }
}

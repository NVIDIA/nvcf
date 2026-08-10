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

use std::fmt;
use std::hash::{Hash, Hasher};
use std::sync::Arc;

use scc::HashMap as SccHashMap;

use super::{LoadBalancer, LoadBalancerAlgorithmConfig, create_load_balancer_with_config};

#[derive(Clone, Debug)]
pub(super) struct LoadBalancerDefinition(Arc<LoadBalancerAlgorithmConfig>);

impl PartialEq for LoadBalancerDefinition {
    fn eq(&self, other: &Self) -> bool {
        Arc::ptr_eq(&self.0, &other.0)
    }
}

impl Eq for LoadBalancerDefinition {}

impl Hash for LoadBalancerDefinition {
    fn hash<H: Hasher>(&self, state: &mut H) {
        Arc::as_ptr(&self.0).hash(state);
    }
}

impl LoadBalancerDefinition {
    pub(super) fn new(config: LoadBalancerAlgorithmConfig) -> anyhow::Result<Self> {
        let _ = create_load_balancer_with_config(&config)?;
        Ok(Self(Arc::new(config)))
    }

    pub(super) fn config(&self) -> &LoadBalancerAlgorithmConfig {
        &self.0
    }
}

/// Load balancers owned by one routing-target generation; replacement resets target-local counters and caches.
#[derive(Default)]
pub struct LoadBalancerTargetState {
    instances: SccHashMap<LoadBalancerDefinition, Arc<dyn LoadBalancer>>,
}

impl LoadBalancerTargetState {
    pub(super) fn load_balancer(
        &self,
        definition: &LoadBalancerDefinition,
    ) -> Arc<dyn LoadBalancer> {
        if let Some(lb) = self
            .instances
            .read_sync(definition, |_definition, lb| lb.clone())
        {
            return lb;
        }

        let lb = create_load_balancer_with_config(definition.config())
            .expect("load balancer config validated during router construction");
        if self
            .instances
            .insert_sync(definition.clone(), lb.clone())
            .is_ok()
        {
            return lb;
        }

        self.instances
            .read_sync(definition, |_definition, lb| lb.clone())
            .expect("target-local load balancer should exist after insert race")
    }

    #[cfg(test)]
    pub(super) fn instance_count(&self) -> usize {
        self.instances.len()
    }
}

impl fmt::Debug for LoadBalancerTargetState {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("LoadBalancerTargetState")
            .field("instance_count", &self.instances.len())
            .finish()
    }
}

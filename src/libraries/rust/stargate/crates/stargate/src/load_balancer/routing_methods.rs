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

//! Test view of `routing_methods.json`, the expected routing method names,
//! aliases, and routing-expression parameters.
//!
//! `http_proxy/routing_method_drift_tests.rs` compares this list with the
//! parsers, load balancers, metrics, published docs, and bundled configs.

use std::collections::BTreeSet;
use std::sync::OnceLock;

use serde::Deserialize;

use super::LoadBalancerAlgorithm;

/// Printed with every drift failure: the places that spell routing methods.
pub(crate) const UPDATE_HINT: &str = "Routing method names, aliases, and parameters must \
agree everywhere. Update together, from the repository root: \
src/libraries/rust/stargate/crates/stargate/src/load_balancer/routing_methods.json; \
LoadBalancerAlgorithm (variants, serde aliases, Display, ALL) and the \
RawLoadBalancerAlgorithmConfig aliases in load_balancer/config.rs; parameter applicability \
in load_balancer/expression.rs; the algorithm and parameter tables in \
src/libraries/rust/stargate/docs/load-balancer-configuration.md and \
docs/self-managed/llm-request-router-load-balancing.md; the routingMethod examples in \
docs/overview/llm-gateway.md; src/libraries/rust/stargate/benches/*.yaml; and \
src/libraries/rust/stargate/crates/stargate-routing-sim/configs/*.json.";

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct RoutingMethods {
    /// Expression parameters every method accepts.
    pub(crate) common_parameters: Vec<String>,
    pub(crate) methods: Vec<RoutingMethod>,
}

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct RoutingMethod {
    /// Canonical name, as `LoadBalancerAlgorithm` displays it.
    pub(crate) name: String,
    /// Other names accepted in `lb-config.json` and `x-routing-method`.
    pub(crate) aliases: Vec<String>,
    /// Expression parameters this method accepts beyond `common_parameters`.
    pub(crate) parameters: Vec<String>,
}

pub(crate) fn routing_methods() -> &'static RoutingMethods {
    static ROUTING_METHODS: OnceLock<RoutingMethods> = OnceLock::new();
    ROUTING_METHODS.get_or_init(|| {
        serde_json::from_str(include_str!("routing_methods.json"))
            .unwrap_or_else(|error| panic!("routing_methods.json is invalid: {error}"))
    })
}

impl RoutingMethods {
    /// Canonical names in `LoadBalancerAlgorithm::ALL` order.
    pub(crate) fn names(&self) -> Vec<&str> {
        self.methods
            .iter()
            .map(|method| method.name.as_str())
            .collect()
    }

    pub(crate) fn get(&self, name: &str) -> Option<&RoutingMethod> {
        self.methods.iter().find(|method| method.name == name)
    }

    pub(crate) fn accepts(&self, method: &RoutingMethod, parameter: &str) -> bool {
        self.common_parameters
            .iter()
            .chain(&method.parameters)
            .any(|accepted| accepted == parameter)
    }

    /// Every parameter that at least one method accepts.
    pub(crate) fn all_parameters(&self) -> BTreeSet<&str> {
        self.common_parameters
            .iter()
            .chain(self.methods.iter().flat_map(|method| &method.parameters))
            .map(String::as_str)
            .collect()
    }
}

impl RoutingMethod {
    pub(crate) fn algorithm(&self) -> LoadBalancerAlgorithm {
        self.name.parse().unwrap_or_else(|error| {
            panic!(
                "routing_methods.json names `{}`, which LoadBalancerAlgorithm rejects: {error}.\n{UPDATE_HINT}",
                self.name
            )
        })
    }

    /// The canonical name followed by every alias.
    pub(crate) fn spellings(&self) -> impl Iterator<Item = &str> {
        std::iter::once(self.name.as_str()).chain(self.aliases.iter().map(String::as_str))
    }
}

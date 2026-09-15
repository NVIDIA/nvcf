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

use std::collections::HashSet;
use std::time::{Duration, Instant};

use crate::routing_state::RoutingTargetKey;

#[derive(Clone, Debug)]
pub struct LoadBalancerRequest<'a> {
    pub routing_target: &'a RoutingTargetKey,
    pub cache_affinity_key: Option<&'a str>,
    pub input_tokens: Option<u64>,
    pub priority: u32,
    pub received_at: Instant,
    pub request_slo: Option<Duration>,
    pub excluded_cluster_ids: Option<&'a HashSet<String>>,
}

impl LoadBalancerRequest<'_> {
    pub(crate) fn has_excluded_clusters(&self) -> bool {
        self.excluded_cluster_ids
            .is_some_and(|excluded| !excluded.is_empty())
    }

    pub(crate) fn excludes_cluster(&self, cluster_id: &str) -> bool {
        self.excluded_cluster_ids
            .is_some_and(|excluded| excluded.contains(cluster_id))
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct LoadBalancerCandidateChoice {
    // Hot-path routing returns the slice index so the proxy can borrow the
    // selected snapshot instead of cloning every load-balancer decision.
    pub candidate_index: usize,
    pub rank_depth: usize,
    pub selected_after_kv_free_tokens_skip: bool,
}

impl LoadBalancerCandidateChoice {
    pub(crate) fn with_rank_depth_1(candidate_index: usize) -> Self {
        Self {
            candidate_index,
            rank_depth: 1,
            selected_after_kv_free_tokens_skip: false,
        }
    }
}

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
use std::sync::atomic::{AtomicUsize, Ordering};

#[cfg(test)]
use super::LoadBalancerTestChoiceExt;
use super::{LoadBalancer, LoadBalancerCandidateChoice, LoadBalancerRequest};
use crate::routing_state::RoutedClusterSnapshot;

pub(super) struct RoundRobinLoadBalancer {
    counter: AtomicUsize,
}

impl fmt::Display for RoundRobinLoadBalancer {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "round-robin")
    }
}

impl RoundRobinLoadBalancer {
    pub(super) fn new() -> Self {
        Self {
            counter: AtomicUsize::new(0),
        }
    }
}

impl LoadBalancer for RoundRobinLoadBalancer {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if candidates.is_empty() {
            return None;
        }

        if !request.has_excluded_clusters() {
            let idx = self.counter.fetch_add(1, Ordering::Relaxed) % candidates.len();
            return Some(LoadBalancerCandidateChoice::with_rank_depth_1(idx));
        }

        if let Some(choice) = self.choose_with_single_excluded_cluster(request, candidates) {
            return Some(choice);
        }
        if let Some(choice) = self.choose_with_two_excluded_clusters(request, candidates) {
            return Some(choice);
        }

        self.choose_with_exclusions(request, candidates)
    }
}

impl RoundRobinLoadBalancer {
    fn choose_with_single_excluded_cluster(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let excluded = request.excluded_cluster_ids?;
        if excluded.len() != 1 || candidates.len() < 2 {
            return None;
        }

        let excluded_cluster_id = excluded.iter().next()?;
        let excluded_index = candidates
            .iter()
            .position(|candidate| candidate.cluster_id.as_str() == excluded_cluster_id.as_str())?;
        // Retries usually exclude the one backend that just failed. Once we know
        // that excluded backend's position, the eligible round-robin slot maps
        // directly onto the candidate slice by skipping over that one position.
        // All other exclusion shapes fall back to the exact scan below.
        let idx = self.counter.fetch_add(1, Ordering::Relaxed) % (candidates.len() - 1);
        let candidate_index = idx + usize::from(idx >= excluded_index);
        let candidate = &candidates[candidate_index];
        // Candidate IDs should be unique, but keep the guard so malformed input
        // cannot make the fast path return a backend the request excluded.
        if request.excludes_cluster(&candidate.cluster_id) {
            return None;
        }

        Some(LoadBalancerCandidateChoice::with_rank_depth_1(
            candidate_index,
        ))
    }

    fn choose_with_two_excluded_clusters(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let excluded = request.excluded_cluster_ids?;
        if excluded.len() != 2 || candidates.len() < 3 {
            return None;
        }

        let mut excluded_ids = excluded.iter().map(String::as_str);
        let first_excluded = excluded_ids.next()?;
        let second_excluded = excluded_ids.next()?;
        let mut first_excluded_index = None;
        let mut second_excluded_index = None;
        for (index, candidate) in candidates.iter().enumerate() {
            let cluster_id = candidate.cluster_id.as_str();
            if cluster_id == first_excluded {
                first_excluded_index = Some(index);
            } else if cluster_id == second_excluded {
                second_excluded_index = Some(index);
            }
            if first_excluded_index.is_some() && second_excluded_index.is_some() {
                break;
            }
        }

        let first_excluded_index = first_excluded_index?;
        let second_excluded_index = second_excluded_index?;
        let (lower_excluded_index, upper_excluded_index) =
            if first_excluded_index <= second_excluded_index {
                (first_excluded_index, second_excluded_index)
            } else {
                (second_excluded_index, first_excluded_index)
            };

        // The third proxy attempt commonly has two failed clusters. Once their
        // positions are known, map the eligible round-robin slot into the full
        // slice by skipping over those positions instead of counting and
        // filtering with HashSet probes on every request.
        let idx = self.counter.fetch_add(1, Ordering::Relaxed) % (candidates.len() - 2);
        let mut candidate_index = idx;
        if candidate_index >= lower_excluded_index {
            candidate_index += 1;
        }
        if candidate_index >= upper_excluded_index {
            candidate_index += 1;
        }
        let candidate = &candidates[candidate_index];
        // Candidate IDs should be unique, but keep the guard so malformed input
        // cannot make the fast path return a backend the request excluded.
        if request.excludes_cluster(&candidate.cluster_id) {
            return None;
        }

        Some(LoadBalancerCandidateChoice::with_rank_depth_1(
            candidate_index,
        ))
    }

    fn choose_with_exclusions(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let eligible_count = candidates
            .iter()
            .filter(|candidate| !request.excludes_cluster(&candidate.cluster_id))
            .count();
        if eligible_count == 0 {
            return None;
        }

        let idx = self.counter.fetch_add(1, Ordering::Relaxed) % eligible_count;
        candidates
            .iter()
            .enumerate()
            .filter(|(_, candidate)| !request.excludes_cluster(&candidate.cluster_id))
            .nth(idx)
            .map(|(candidate_index, _)| {
                LoadBalancerCandidateChoice::with_rank_depth_1(candidate_index)
            })
    }
}

#[cfg(test)]
mod tests {
    use std::collections::HashSet;
    use std::time::{Duration, Instant};

    use stargate_proto::pb::{InferenceServerStatus, ModelStats};

    use super::*;
    use crate::routing_state::RoutingTargetKey;

    fn candidate(cluster_id: &str) -> RoutedClusterSnapshot {
        RoutedClusterSnapshot {
            cluster_id: cluster_id.to_string(),
            stats: ModelStats::default(),
            rtt: Duration::from_millis(1),
            snapshot_updated_at: Instant::now(),
            status: InferenceServerStatus::Active,
            active_backend_count: 1,
        }
    }

    fn request<'a>(
        target: &'a RoutingTargetKey,
        excluded_cluster_ids: Option<&'a HashSet<String>>,
    ) -> LoadBalancerRequest<'a> {
        LoadBalancerRequest {
            routing_target: target,
            cache_affinity_key: None,
            input_tokens: None,
            priority: 0,
            received_at: Instant::now(),
            request_slo: None,
            excluded_cluster_ids,
        }
    }

    #[test]
    fn round_robin_single_exclusion_cycles_over_remaining_candidates() {
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model".to_string(),
        };
        let excluded = HashSet::from(["cluster-0002".to_string()]);
        let request = request(&target, Some(&excluded));
        let candidates = [
            candidate("cluster-0000"),
            candidate("cluster-0001"),
            candidate("cluster-0002"),
            candidate("cluster-0003"),
        ];
        let load_balancer = RoundRobinLoadBalancer::new();

        let selected = (0..6)
            .map(|_| {
                load_balancer
                    .choose_for_test(&request, &candidates)
                    .expect("eligible candidate should be selected")
                    .candidate
                    .cluster_id
            })
            .collect::<Vec<_>>();

        assert_eq!(
            selected,
            [
                "cluster-0000",
                "cluster-0001",
                "cluster-0003",
                "cluster-0000",
                "cluster-0001",
                "cluster-0003",
            ]
        );
    }

    #[test]
    fn round_robin_two_exclusions_cycle_over_remaining_candidates() {
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model".to_string(),
        };
        let excluded = HashSet::from(["cluster-0001".to_string(), "cluster-0003".to_string()]);
        let request = request(&target, Some(&excluded));
        let candidates = [
            candidate("cluster-0000"),
            candidate("cluster-0001"),
            candidate("cluster-0002"),
            candidate("cluster-0003"),
            candidate("cluster-0004"),
        ];
        let load_balancer = RoundRobinLoadBalancer::new();

        let selected = (0..7)
            .map(|_| {
                load_balancer
                    .choose_for_test(&request, &candidates)
                    .expect("eligible candidate should be selected")
                    .candidate
                    .cluster_id
            })
            .collect::<Vec<_>>();

        assert_eq!(
            selected,
            [
                "cluster-0000",
                "cluster-0002",
                "cluster-0004",
                "cluster-0000",
                "cluster-0002",
                "cluster-0004",
                "cluster-0000",
            ]
        );
    }

    #[test]
    fn round_robin_returns_none_when_all_candidates_are_excluded() {
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model".to_string(),
        };
        let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);
        let request = request(&target, Some(&excluded));
        let candidates = [candidate("excluded-a"), candidate("excluded-b")];

        assert!(
            RoundRobinLoadBalancer::new()
                .choose_for_test(&request, &candidates)
                .is_none()
        );
    }
}

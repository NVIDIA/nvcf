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

use rand::{Rng, seq::IteratorRandom};

#[cfg(test)]
use super::LoadBalancerTestChoiceExt;
use super::{LoadBalancer, LoadBalancerCandidateChoice, LoadBalancerRequest};
use crate::routing_state::RoutedClusterSnapshot;

const EXCLUSION_REJECTION_ATTEMPTS: usize = 8;
const REJECTION_SAMPLE_EXCLUSION_RATIO_DIVISOR: usize = 4;

pub(super) struct RandomLoadBalancer;

impl fmt::Display for RandomLoadBalancer {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "random")
    }
}

impl LoadBalancer for RandomLoadBalancer {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if candidates.is_empty() {
            return None;
        }

        let mut rng = rand::rng();
        if !request.has_excluded_clusters() {
            return Some(LoadBalancerCandidateChoice::with_rank_depth_1(
                sample_candidate_index(candidates.len(), &mut rng),
            ));
        }
        if let Some(candidate_index) =
            sample_candidate_index_with_rejections(request, candidates, &mut rng)
        {
            return Some(LoadBalancerCandidateChoice::with_rank_depth_1(
                candidate_index,
            ));
        }

        sample_candidate_index_with_reservoir(request, candidates, &mut rng)
            .map(LoadBalancerCandidateChoice::with_rank_depth_1)
    }
}

fn sample_candidate_index<R: Rng + ?Sized>(candidates_len: usize, rng: &mut R) -> usize {
    rng.random_range(0..candidates_len)
}

fn sample_candidate_index_with_rejections<R: Rng + ?Sized>(
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    rng: &mut R,
) -> Option<usize> {
    if !should_try_rejection_sampling(request, candidates.len()) {
        return None;
    }

    // Retry/failover requests commonly exclude exactly the failed backend from
    // the previous attempt. Try a few O(1) random probes before falling back to
    // the exact scan. The fallback remains a uniform reservoir sample, so the
    // capped retries preserve the old uniform distribution over eligible
    // candidates while avoiding an O(n) pass on the common single-exclusion path.
    for _ in 0..EXCLUSION_REJECTION_ATTEMPTS {
        let candidate_index = sample_candidate_index(candidates.len(), rng);
        let candidate = &candidates[candidate_index];
        if !request.excludes_cluster(&candidate.cluster_id) {
            return Some(candidate_index);
        }
    }

    None
}

fn should_try_rejection_sampling(request: &LoadBalancerRequest<'_>, candidates_len: usize) -> bool {
    request.excluded_cluster_ids.is_some_and(|excluded| {
        excluded.len() <= candidates_len / REJECTION_SAMPLE_EXCLUSION_RATIO_DIVISOR
    })
}

fn sample_candidate_index_with_reservoir<R: Rng + ?Sized>(
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    rng: &mut R,
) -> Option<usize> {
    candidates
        .iter()
        .enumerate()
        .filter(|(_, candidate)| !request.excludes_cluster(&candidate.cluster_id))
        .choose(rng)
        .map(|(candidate_index, _)| candidate_index)
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
    fn random_never_selects_excluded_clusters() {
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model".to_string(),
        };
        let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);
        let request = request(&target, Some(&excluded));
        let candidates = [
            candidate("excluded-a"),
            candidate("eligible"),
            candidate("excluded-b"),
        ];

        for _ in 0..128 {
            let choice = RandomLoadBalancer
                .choose_for_test(&request, &candidates)
                .expect("eligible candidate should be selected");
            assert_eq!(choice.candidate.cluster_id, "eligible");
        }
    }

    #[test]
    fn random_skips_single_excluded_cluster_in_retry_set() {
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model".to_string(),
        };
        let excluded = HashSet::from(["cluster-0000".to_string()]);
        let request = request(&target, Some(&excluded));
        let candidates = (0..64)
            .map(|index| candidate(&format!("cluster-{index:04}")))
            .collect::<Vec<_>>();

        for _ in 0..512 {
            let choice = RandomLoadBalancer
                .choose_for_test(&request, &candidates)
                .expect("eligible candidate should be selected");
            assert_ne!(choice.candidate.cluster_id, "cluster-0000");
        }
    }

    #[test]
    fn random_returns_none_when_all_candidates_are_excluded() {
        let target = RoutingTargetKey {
            routing_key: None,
            model_id: "model".to_string(),
        };
        let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);
        let request = request(&target, Some(&excluded));
        let candidates = [candidate("excluded-a"), candidate("excluded-b")];

        assert!(
            RandomLoadBalancer
                .choose_for_test(&request, &candidates)
                .is_none()
        );
    }
}

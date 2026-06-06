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

use rand::Rng;
use tracing::debug;

#[cfg(test)]
use super::LoadBalancerTestChoiceExt;
use super::{LoadBalancer, LoadBalancerCandidateChoice, LoadBalancerRequest};
use crate::routing_state::RoutedClusterSnapshot;

const EXCLUSION_REJECTION_ATTEMPTS: usize = 8;
const REJECTION_SAMPLE_EXCLUSION_RATIO_DIVISOR: usize = 4;

pub(super) struct PowerOfTwoLoadBalancer;

impl fmt::Display for PowerOfTwoLoadBalancer {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "power-of-two")
    }
}

impl LoadBalancer for PowerOfTwoLoadBalancer {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let mut rng = rand::rng();
        let sampled = if request.has_excluded_clusters() {
            sample_with_exclusions(request, candidates, &mut rng)
        } else {
            sample_without_exclusions(candidates, &mut rng)
        };

        match sampled {
            CandidateSample::None => None,
            CandidateSample::One(candidate_index) => Some(
                LoadBalancerCandidateChoice::with_rank_depth_1(candidate_index),
            ),
            CandidateSample::Two(a_index, b_index) => Some(choose_less_loaded(
                candidates,
                a_index,
                b_index,
                request.input_tokens,
                &mut rng,
            )),
        }
    }
}

enum CandidateSample {
    None,
    One(usize),
    Two(usize, usize),
}

fn sample_without_exclusions<R: Rng + ?Sized>(
    candidates: &[RoutedClusterSnapshot],
    rng: &mut R,
) -> CandidateSample {
    match candidates.len() {
        0 => CandidateSample::None,
        1 => CandidateSample::One(0),
        len => {
            // First-attempt proxy routing normally has no failed clusters. Pick two
            // distinct indices directly so the default production algorithm is O(1)
            // instead of scanning every candidate on each request.
            let a_index = rng.random_range(0..len);
            let mut b_index = rng.random_range(0..len - 1);
            if b_index >= a_index {
                b_index += 1;
            }
            CandidateSample::Two(a_index, b_index)
        }
    }
}

fn sample_with_exclusions<R: Rng + ?Sized>(
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    rng: &mut R,
) -> CandidateSample {
    if let Some(sampled) = sample_with_rejections(request, candidates, rng) {
        return sampled;
    }

    sample_with_reservoir(request, candidates, rng)
}

fn sample_with_rejections<R: Rng + ?Sized>(
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    rng: &mut R,
) -> Option<CandidateSample> {
    if !should_try_rejection_sampling(request, candidates.len()) {
        return None;
    }

    // Retry/failover usually excludes one backend. Sampling two distinct
    // candidates from the full slice and rejecting pairs that touch the excluded
    // set keeps the common path O(1). The fallback below is the same uniform
    // reservoir sample used before, so bounded retries preserve the old uniform
    // sample-without-replacement distribution over eligible candidates.
    for _ in 0..EXCLUSION_REJECTION_ATTEMPTS {
        let CandidateSample::Two(a_index, b_index) = sample_without_exclusions(candidates, rng)
        else {
            return None;
        };
        let a = &candidates[a_index];
        let b = &candidates[b_index];
        if !request.excludes_cluster(&a.cluster_id) && !request.excludes_cluster(&b.cluster_id) {
            return Some(CandidateSample::Two(a_index, b_index));
        }
    }

    None
}

fn should_try_rejection_sampling(request: &LoadBalancerRequest<'_>, candidates_len: usize) -> bool {
    candidates_len >= 2
        && request.excluded_cluster_ids.is_some_and(|excluded| {
            excluded.len() <= candidates_len / REJECTION_SAMPLE_EXCLUSION_RATIO_DIVISOR
        })
}

fn sample_with_reservoir<R: Rng + ?Sized>(
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    rng: &mut R,
) -> CandidateSample {
    let mut sampled: [Option<usize>; 2] = [None, None];
    let mut eligible_seen = 0usize;

    for (candidate_index, candidate) in candidates.iter().enumerate() {
        if request.excludes_cluster(&candidate.cluster_id) {
            continue;
        }

        eligible_seen += 1;
        match eligible_seen {
            1 => sampled[0] = Some(candidate_index),
            2 => sampled[1] = Some(candidate_index),
            _ => {
                // Retry/failover attempts may exclude clusters. Reservoir sampling
                // preserves the old uniform sample-without-replacement behavior for
                // the remaining eligible set without allocating a filtered Vec.
                let slot = rng.random_range(0..eligible_seen);
                if slot < sampled.len() {
                    sampled[slot] = Some(candidate_index);
                }
            }
        }
    }

    match (sampled[0], sampled[1]) {
        (None, _) => CandidateSample::None,
        (Some(candidate), None) => CandidateSample::One(candidate),
        (Some(a), Some(b)) => CandidateSample::Two(a, b),
    }
}

fn choose_less_loaded<R: Rng + ?Sized>(
    candidates: &[RoutedClusterSnapshot],
    a_index: usize,
    b_index: usize,
    input_tokens: Option<u64>,
    rng: &mut R,
) -> LoadBalancerCandidateChoice {
    let a = &candidates[a_index];
    let b = &candidates[b_index];
    let a_score = load_score(a, input_tokens);
    let b_score = load_score(b, input_tokens);
    debug!(
        inst_a = %a.cluster_id,
        inst_b = %b.cluster_id,
        load_score_a = a_score,
        load_score_b = b_score,
        "sampled two clusters"
    );

    if a_score < b_score {
        LoadBalancerCandidateChoice::with_rank_depth_1(a_index)
    } else if b_score < a_score {
        LoadBalancerCandidateChoice::with_rank_depth_1(b_index)
    } else if rng.random_bool(0.5) {
        LoadBalancerCandidateChoice::with_rank_depth_1(a_index)
    } else {
        LoadBalancerCandidateChoice::with_rank_depth_1(b_index)
    }
}

fn load_score(candidate: &RoutedClusterSnapshot, input_tokens: Option<u64>) -> f64 {
    let last_mean_input_tps = candidate.stats.last_mean_input_tps;
    if last_mean_input_tps <= 0.0 || !last_mean_input_tps.is_finite() {
        return f64::INFINITY;
    }

    let input_work_units =
        super::input_work_units(candidate) + input_tokens.unwrap_or_default() as f64;
    input_work_units / last_mean_input_tps
}

#[cfg(test)]
mod tests {
    use std::collections::HashSet;
    use std::time::{Duration, Instant};

    use stargate_proto::pb::{InferenceServerStatus, ModelStats};

    use super::*;

    fn candidate(
        id: &str,
        last_mean_input_tps: f64,
        queued_input_size: u64,
    ) -> RoutedClusterSnapshot {
        RoutedClusterSnapshot {
            cluster_id: id.to_string(),
            stats: ModelStats {
                output_tps: 0.0,
                last_mean_input_tps,
                max_output_tps: 0.0,
                queue_size: 0,
                queued_input_size,
                kv_cache_capacity_tokens: 0,
                kv_cache_used_tokens: 0,
                kv_cache_free_tokens: 0,
                ..ModelStats::default()
            },
            rtt: Duration::from_millis(1),
            snapshot_updated_at: Instant::now(),
            status: InferenceServerStatus::Active,
            active_backend_count: 1,
        }
    }

    fn request<'a>(
        target: &'a crate::routing_state::RoutingTargetKey,
        excluded_cluster_ids: Option<&'a HashSet<String>>,
    ) -> LoadBalancerRequest<'a> {
        LoadBalancerRequest {
            routing_target: target,
            cache_affinity_key: None,
            input_tokens: Some(1000),
            priority: 0,
            received_at: Instant::now(),
            request_slo: None,
            excluded_cluster_ids,
        }
    }

    #[test]
    fn load_score_prefers_faster_empty_backend_for_incoming_prefill() {
        let fast = candidate("fast", 200.0, 0);
        let slow = candidate("slow", 100.0, 0);

        assert!(load_score(&fast, Some(1000)) < load_score(&slow, Some(1000)));
    }

    #[test]
    fn load_score_accounts_for_queued_prefill_work() {
        let busy_fast = candidate("busy-fast", 200.0, 10_000);
        let empty_slow = candidate("empty-slow", 100.0, 0);

        assert!(load_score(&empty_slow, Some(1000)) < load_score(&busy_fast, Some(1000)));
    }

    #[test]
    fn load_score_ignores_decode_only_total_query_input_size() {
        let mut decode_only = candidate("decode-only", 100.0, 0);
        decode_only.stats.total_query_input_size = 10_000;
        let empty = candidate("empty", 100.0, 0);

        assert_eq!(
            load_score(&decode_only, Some(1000)),
            load_score(&empty, Some(1000))
        );
    }

    #[test]
    fn power_of_two_never_selects_excluded_clusters() {
        let target = crate::routing_state::RoutingTargetKey {
            routing_key: None,
            model_id: "model-a".to_string(),
        };
        let candidates = vec![
            candidate("excluded-a", 1_000.0, 0),
            candidate("eligible", 1.0, 0),
            candidate("excluded-b", 1_000.0, 0),
        ];
        let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);
        let request = request(&target, Some(&excluded));

        for _ in 0..64 {
            let choice = PowerOfTwoLoadBalancer
                .choose_for_test(&request, &candidates)
                .expect("one eligible cluster should be selected");
            assert_eq!(choice.candidate.cluster_id, "eligible");
        }
    }

    #[test]
    fn power_of_two_skips_single_excluded_cluster_in_retry_set() {
        let target = crate::routing_state::RoutingTargetKey {
            routing_key: None,
            model_id: "model-a".to_string(),
        };
        let candidates = (0..64)
            .map(|index| candidate(&format!("cluster-{index:04}"), 1_000.0, 0))
            .collect::<Vec<_>>();
        let excluded = HashSet::from(["cluster-0000".to_string()]);
        let request = request(&target, Some(&excluded));

        for _ in 0..512 {
            let choice = PowerOfTwoLoadBalancer
                .choose_for_test(&request, &candidates)
                .expect("eligible cluster should be selected");
            assert_ne!(choice.candidate.cluster_id, "cluster-0000");
        }
    }

    #[test]
    fn power_of_two_returns_none_when_all_candidates_are_excluded() {
        let target = crate::routing_state::RoutingTargetKey {
            routing_key: None,
            model_id: "model-a".to_string(),
        };
        let candidates = vec![
            candidate("excluded-a", 1_000.0, 0),
            candidate("excluded-b", 1_000.0, 0),
        ];
        let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);
        let request = request(&target, Some(&excluded));

        assert!(
            PowerOfTwoLoadBalancer
                .choose_for_test(&request, &candidates)
                .is_none()
        );
    }
}

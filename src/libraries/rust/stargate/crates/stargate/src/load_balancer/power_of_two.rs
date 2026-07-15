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

use rand::Rng;
use stargate_protocol::common::valid_last_mean_input_tps;
use tracing::debug;

#[cfg(test)]
use super::tests::LoadBalancerTestChoiceExt;
use super::{LoadBalancer, LoadBalancerCandidateChoice, LoadBalancerRequest};
use crate::routing_state::RoutedClusterSnapshot;

const EXCLUSION_REJECTION_ATTEMPTS: usize = 8;
const REJECTION_SAMPLE_EXCLUSION_RATIO_DIVISOR: usize = 4;

pub(super) struct PowerOfTwoLoadBalancer;

impl_display!(PowerOfTwoLoadBalancer, "power-of-two");

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
            let (a_index, b_index) = sample_distinct_pair(len, rng);
            CandidateSample::Two(a_index, b_index)
        }
    }
}

fn sample_distinct_pair<R: Rng + ?Sized>(len: usize, rng: &mut R) -> (usize, usize) {
    let a_index = rng.random_range(0..len);
    let mut b_index = rng.random_range(0..len - 1);
    if b_index >= a_index {
        b_index += 1;
    }
    (a_index, b_index)
}

fn sample_with_exclusions<R: Rng + ?Sized>(
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    rng: &mut R,
) -> CandidateSample {
    sample_sparse_pair(request, candidates, rng)
        .unwrap_or_else(|| sample_with_reservoir(request, candidates, rng))
}

fn sample_sparse_pair<R: Rng + ?Sized>(
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    rng: &mut R,
) -> Option<CandidateSample> {
    let excluded = request.excluded_cluster_ids?;
    if candidates.len() < 2
        || excluded.len() > candidates.len() / REJECTION_SAMPLE_EXCLUSION_RATIO_DIVISOR
    {
        return None;
    }
    // Sparse retries use bounded O(1) pair sampling before uniform reservoir fallback.
    for _ in 0..EXCLUSION_REJECTION_ATTEMPTS {
        let (a_index, b_index) = sample_distinct_pair(candidates.len(), rng);
        if !request.excludes_cluster(&candidates[a_index].cluster_id)
            && !request.excludes_cluster(&candidates[b_index].cluster_id)
        {
            return Some(CandidateSample::Two(a_index, b_index));
        }
    }
    None
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

    let selected_index = if a_score < b_score {
        a_index
    } else if b_score < a_score {
        b_index
    } else if rng.random_bool(0.5) {
        a_index
    } else {
        b_index
    };
    LoadBalancerCandidateChoice::with_rank_depth_1(selected_index)
}

fn load_score(candidate: &RoutedClusterSnapshot, input_tokens: Option<u64>) -> f64 {
    let last_mean_input_tps = candidate.stats.last_mean_input_tps;
    if valid_last_mean_input_tps(last_mean_input_tps) {
        (super::input_work_units(candidate) + input_tokens.unwrap_or_default() as f64)
            / last_mean_input_tps
    } else {
        f64::INFINITY
    }
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
                last_mean_input_tps,
                queued_input_size,
                ..ModelStats::default()
            },
            rtt: Duration::from_millis(1),
            snapshot_updated_at: Instant::now(),
            status: InferenceServerStatus::Active,
            active_backend_count: 1,
        }
    }

    fn selected_cluster_id(
        candidates: &[RoutedClusterSnapshot],
        excluded_cluster_ids: &HashSet<String>,
    ) -> Option<String> {
        let target = crate::routing_state::RoutingTargetKey::new(None, "model-a");
        let request = LoadBalancerRequest {
            routing_target: &target,
            cache_affinity_key: None,
            input_tokens: Some(1000),
            priority: 0,
            received_at: Instant::now(),
            request_slo: None,
            excluded_cluster_ids: Some(excluded_cluster_ids),
        };
        PowerOfTwoLoadBalancer
            .choose_for_test(&request, candidates)
            .map(|choice| choice.candidate.cluster_id)
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
    fn load_score_rejects_invalid_input_throughput() {
        for input_tps in [0.0, -1.0, f64::NAN, f64::INFINITY, f64::NEG_INFINITY] {
            assert!(load_score(&candidate("invalid", input_tps, 0), Some(100)).is_infinite());
        }
    }

    #[test]
    fn power_of_two_never_selects_excluded_clusters() {
        let candidates = vec![
            candidate("excluded-a", 1_000.0, 0),
            candidate("eligible", 1.0, 0),
            candidate("excluded-b", 1_000.0, 0),
        ];
        let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);

        for _ in 0..64 {
            assert_eq!(
                selected_cluster_id(&candidates, &excluded).as_deref(),
                Some("eligible")
            );
        }
    }

    #[test]
    fn power_of_two_skips_single_excluded_cluster_in_retry_set() {
        let candidates = (0..64)
            .map(|index| candidate(&format!("cluster-{index:04}"), 1_000.0, 0))
            .collect::<Vec<_>>();
        let excluded = HashSet::from(["cluster-0000".to_string()]);

        for _ in 0..512 {
            let selected = selected_cluster_id(&candidates, &excluded)
                .expect("an eligible cluster should be selected");
            assert_ne!(selected, "cluster-0000");
        }
    }

    #[test]
    fn power_of_two_returns_none_when_all_candidates_are_excluded() {
        let candidates = vec![
            candidate("excluded-a", 1_000.0, 0),
            candidate("excluded-b", 1_000.0, 0),
        ];
        let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);

        assert!(selected_cluster_id(&candidates, &excluded).is_none());
    }
}

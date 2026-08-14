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
use rand::seq::IteratorRandom;
use stargate_protocol::common::valid_last_mean_input_tps;
use tracing::{Span, debug};

#[cfg(test)]
use super::tests::LoadBalancerTestChoiceExt;
use super::{
    LoadBalancer, LoadBalancerAlgorithmConfig, LoadBalancerCandidateChoice, LoadBalancerRequest,
    MAX_POWER_OF_N_SAMPLE_COUNT,
};
use crate::routing_state::RoutedClusterSnapshot;

pub(super) struct PowerOfNLoadBalancer {
    sample_count: usize,
}

impl PowerOfNLoadBalancer {
    pub(super) fn from_algorithm_config(
        config: &LoadBalancerAlgorithmConfig,
    ) -> anyhow::Result<Self> {
        let settings = config
            .power_of_n_settings()
            .expect("power-of-n settings should match load-balancer algorithm");
        let sample_count = settings
            .validated_sample_count()
            .map_err(anyhow::Error::msg)?;
        Ok(Self { sample_count })
    }

    fn choose_candidate_with_rng<R: Rng + ?Sized>(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        rng: &mut R,
    ) -> Option<LoadBalancerCandidateChoice> {
        let sampled = sample_candidates(request, candidates, self.sample_count, rng);
        let span = Span::current();
        span.record("routing.sample_count_configured", self.sample_count);
        span.record("routing.sample_count_effective", sampled.len());

        choose_least_loaded(candidates, sampled.as_slice(), request.input_tokens, rng)
    }
}

impl_display!(PowerOfNLoadBalancer, "power-of-n");

impl LoadBalancer for PowerOfNLoadBalancer {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let mut rng = rand::rng();
        self.choose_candidate_with_rng(request, candidates, &mut rng)
    }
}

struct CandidateSample {
    indices: [usize; MAX_POWER_OF_N_SAMPLE_COUNT],
    len: usize,
}

impl CandidateSample {
    fn new() -> Self {
        Self {
            indices: [0; MAX_POWER_OF_N_SAMPLE_COUNT],
            len: 0,
        }
    }

    fn as_slice(&self) -> &[usize] {
        &self.indices[..self.len]
    }

    fn len(&self) -> usize {
        self.len
    }
}

fn sample_candidates<R: Rng + ?Sized>(
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    sample_count: usize,
    rng: &mut R,
) -> CandidateSample {
    let mut sampled = CandidateSample::new();
    if candidates.is_empty() {
        return sampled;
    }

    if !request.has_excluded_clusters() {
        sampled.len = sample_count.min(candidates.len());
        match sampled.len {
            0 => {}
            1 => sampled.indices[0] = rng.random_range(0..candidates.len()),
            2 => {
                let (first, second) = sample_distinct_pair(candidates.len(), rng);
                sampled.indices[..2].copy_from_slice(&[first, second]);
            }
            len if len == candidates.len() => {
                for (index, slot) in sampled.indices[..len].iter_mut().enumerate() {
                    *slot = index;
                }
            }
            len => {
                let filled =
                    (0..candidates.len()).choose_multiple_fill(rng, &mut sampled.indices[..len]);
                debug_assert_eq!(filled, len);
            }
        }
        return sampled;
    }

    let eligible_indices = candidates
        .iter()
        .enumerate()
        .filter(|(_, candidate)| !request.excludes_cluster(&candidate.cluster_id))
        .map(|(index, _)| index);
    sampled.len = eligible_indices.choose_multiple_fill(rng, &mut sampled.indices[..sample_count]);
    sampled
}

fn sample_distinct_pair<R: Rng + ?Sized>(len: usize, rng: &mut R) -> (usize, usize) {
    let a_index = rng.random_range(0..len);
    let mut b_index = rng.random_range(0..len - 1);
    if b_index >= a_index {
        b_index += 1;
    }
    (a_index, b_index)
}

fn choose_least_loaded<R: Rng + ?Sized>(
    candidates: &[RoutedClusterSnapshot],
    sampled_indices: &[usize],
    input_tokens: Option<u64>,
    rng: &mut R,
) -> Option<LoadBalancerCandidateChoice> {
    let (&first_index, remaining_indices) = sampled_indices.split_first()?;
    let mut selected_index = first_index;
    let mut selected_score = load_score(&candidates[first_index], input_tokens);
    let mut tied_best_count = 1u32;

    for &candidate_index in remaining_indices {
        let score = load_score(&candidates[candidate_index], input_tokens);
        if score < selected_score {
            selected_index = candidate_index;
            selected_score = score;
            tied_best_count = 1;
        } else if score == selected_score {
            tied_best_count += 1;
            if rng.random_ratio(1, tied_best_count) {
                selected_index = candidate_index;
            }
        }
    }

    debug!(
        effective_sample_count = sampled_indices.len(),
        selected_candidate_index = selected_index,
        selected_load_score = selected_score,
        "sampled clusters"
    );
    Some(LoadBalancerCandidateChoice::with_rank_depth_1(
        selected_index,
    ))
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

    use rand::SeedableRng;
    use rand::rngs::StdRng;
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
        sample_count: usize,
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
        PowerOfNLoadBalancer { sample_count }
            .choose_for_test(&request, candidates)
            .map(|choice| choice.candidate.cluster_id)
    }

    fn sampled_indices(
        sample_count: usize,
        candidates: &[RoutedClusterSnapshot],
        excluded_cluster_ids: Option<&HashSet<String>>,
        rng: &mut StdRng,
    ) -> Vec<usize> {
        let target = crate::routing_state::RoutingTargetKey::new(None, "model-a");
        let request = LoadBalancerRequest {
            routing_target: &target,
            cache_affinity_key: None,
            input_tokens: Some(1000),
            priority: 0,
            received_at: Instant::now(),
            request_slo: None,
            excluded_cluster_ids,
        };
        sample_candidates(&request, candidates, sample_count, rng)
            .as_slice()
            .to_vec()
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
    fn power_of_n_never_selects_excluded_clusters() {
        let candidates = vec![
            candidate("excluded-a", 1_000.0, 0),
            candidate("eligible", 1.0, 0),
            candidate("excluded-b", 1_000.0, 0),
        ];
        let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);

        for _ in 0..64 {
            assert_eq!(
                selected_cluster_id(2, &candidates, &excluded).as_deref(),
                Some("eligible")
            );
        }
    }

    #[test]
    fn power_of_n_skips_single_excluded_cluster_in_retry_set() {
        let candidates = (0..64)
            .map(|index| candidate(&format!("cluster-{index:04}"), 1_000.0, 0))
            .collect::<Vec<_>>();
        let excluded = HashSet::from(["cluster-0000".to_string()]);

        for _ in 0..512 {
            let selected = selected_cluster_id(2, &candidates, &excluded)
                .expect("an eligible cluster should be selected");
            assert_ne!(selected, "cluster-0000");
        }
    }

    #[test]
    fn power_of_n_returns_none_when_all_candidates_are_excluded() {
        let candidates = vec![
            candidate("excluded-a", 1_000.0, 0),
            candidate("excluded-b", 1_000.0, 0),
        ];
        let excluded = HashSet::from(["excluded-a".to_string(), "excluded-b".to_string()]);

        assert!(selected_cluster_id(2, &candidates, &excluded).is_none());
    }

    #[test]
    fn sampling_handles_empty_singleton_and_oversized_pools() {
        let one = vec![candidate("only", 1.0, 0)];
        let three = vec![
            candidate("a", 1.0, 0),
            candidate("b", 1.0, 0),
            candidate("c", 1.0, 0),
        ];
        let mut rng = StdRng::seed_from_u64(7);

        assert!(sampled_indices(4, &[], None, &mut rng).is_empty());
        assert_eq!(sampled_indices(4, &one, None, &mut rng), [0]);
        assert_eq!(sampled_indices(3, &three, None, &mut rng), [0, 1, 2]);
        assert_eq!(sampled_indices(8, &three, None, &mut rng), [0, 1, 2]);
    }

    #[test]
    fn sampling_is_distinct_and_reproducible_for_counts_one_two_and_four() {
        let candidates = (0..8)
            .map(|index| candidate(&format!("cluster-{index}"), 1.0, 0))
            .collect::<Vec<_>>();

        for sample_count in [1, 2, 4] {
            let mut first_rng = StdRng::seed_from_u64(42);
            let mut second_rng = StdRng::seed_from_u64(42);
            let first = sampled_indices(sample_count, &candidates, None, &mut first_rng);
            let second = sampled_indices(sample_count, &candidates, None, &mut second_rng);
            let distinct = first.iter().copied().collect::<HashSet<_>>();

            assert_eq!(first, second);
            assert_eq!(first.len(), sample_count);
            assert_eq!(distinct.len(), sample_count);
        }
    }

    #[test]
    fn sampling_applies_sparse_and_dense_exclusions_before_sampling() {
        let candidates = (0..8)
            .map(|index| candidate(&format!("cluster-{index}"), 1.0, 0))
            .collect::<Vec<_>>();

        for excluded in [
            HashSet::from(["cluster-0".to_string()]),
            HashSet::from([
                "cluster-0".to_string(),
                "cluster-1".to_string(),
                "cluster-2".to_string(),
                "cluster-3".to_string(),
                "cluster-4".to_string(),
                "cluster-5".to_string(),
            ]),
        ] {
            let mut rng = StdRng::seed_from_u64(91);
            let sampled = sampled_indices(4, &candidates, Some(&excluded), &mut rng);
            let expected_count = 4.min(candidates.len() - excluded.len());

            assert_eq!(sampled.len(), expected_count);
            assert!(
                sampled
                    .iter()
                    .all(|index| !excluded.contains(&candidates[*index].cluster_id))
            );
            assert_eq!(
                sampled.iter().copied().collect::<HashSet<_>>().len(),
                expected_count
            );
        }
    }

    #[test]
    fn sampling_is_uniform_without_replacement() {
        let candidates = (0..4)
            .map(|index| candidate(&format!("cluster-{index}"), 1.0, 0))
            .collect::<Vec<_>>();
        let mut rng = StdRng::seed_from_u64(73);
        let mut inclusion_counts = [0usize; 4];
        const ITERATIONS: usize = 20_000;

        for _ in 0..ITERATIONS {
            for index in sampled_indices(2, &candidates, None, &mut rng) {
                inclusion_counts[index] += 1;
            }
        }

        let expected = ITERATIONS / 2;
        let tolerance = expected / 25;
        for count in inclusion_counts {
            assert!(count.abs_diff(expected) <= tolerance, "count={count}");
        }
    }

    #[test]
    fn full_pool_selection_chooses_the_lowest_score() {
        let candidates = vec![
            candidate("slow", 100.0, 1000),
            candidate("best", 1000.0, 0),
            candidate("busy", 1000.0, 10_000),
        ];
        let excluded = HashSet::new();

        assert_eq!(
            selected_cluster_id(3, &candidates, &excluded).as_deref(),
            Some("best")
        );
    }

    #[test]
    fn equal_scores_do_not_bias_ties_to_one_candidate() {
        let candidates = vec![
            candidate("a", 100.0, 0),
            candidate("b", 100.0, 0),
            candidate("c", 100.0, 0),
        ];
        let target = crate::routing_state::RoutingTargetKey::new(None, "model-a");
        let request = LoadBalancerRequest {
            routing_target: &target,
            cache_affinity_key: None,
            input_tokens: Some(1000),
            priority: 0,
            received_at: Instant::now(),
            request_slo: None,
            excluded_cluster_ids: None,
        };
        let load_balancer = PowerOfNLoadBalancer { sample_count: 3 };
        let mut rng = StdRng::seed_from_u64(11);
        let mut selected = HashSet::new();

        for _ in 0..64 {
            selected.insert(
                load_balancer
                    .choose_candidate_with_rng(&request, &candidates, &mut rng)
                    .expect("equal-score pool should produce a candidate")
                    .candidate_index,
            );
        }

        assert_eq!(selected, HashSet::from([0, 1, 2]));
    }
}

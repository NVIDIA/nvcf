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

mod cache_affinity;
mod estimates;
mod fast_path;

use std::cmp::Ordering;
use std::ops::RangeInclusive;
use std::time::Duration;

use rand::Rng;

use super::{
    GroqMultiregionAlgorithmConfig, LoadBalancer, LoadBalancerAlgorithmConfig,
    LoadBalancerCandidateChoice, LoadBalancerRequest,
};
use crate::routing_state::RoutedClusterSnapshot;
use cache_affinity::CacheAffinitySelector;
use estimates::{
    CandidateEstimateAccumulator, TtftEstimate, compare_least_queue_time, estimate_ttft_ms,
    has_capacity,
};

#[cfg(test)]
pub(super) use cache_affinity::{
    cache_affinity_candidate_indices, cache_affinity_candidates, cache_affinity_virtual_node_hash,
};
#[cfg(test)]
pub(super) use estimates::estimate_ttft_ms as groq_multiregion_ttft_components;

// Parity notes vs lpu-router MultiRegion:
// - Stargate uses only the sticky last_mean_input_tps capacity signal for
//   queue/prefill estimates; there is no separate live input-TPS fallback.
// - Sparse priority queue maps use the nearest published priority at or below the request priority;
//   lpu-router clamps only priorities above the max and otherwise treats missing entries as zero.
// - Stargate does not currently model lpu-router batch-folding queue estimates, LoRA dynamic-model
//   penalties, backend/datacenter request filters, backend-id overrides, or utilization-max rejection.
pub(super) struct GroqMultiregionLoadBalancer {
    config: GroqMultiregionConfig,
    cache_affinity: CacheAffinitySelector,
}

#[derive(Clone, Debug)]
pub(super) struct GroqMultiregionConfig {
    pub(super) seed: Option<String>,
    pub(super) cache_affinity_virtual_nodes: usize,
    pub(super) cache_affinity_backend_selection_count: Option<usize>,
    queue_slo: Option<RangeInclusive<Duration>>,
    pub(super) ttft_bucket_size: Duration,
    pub(super) next_bucket_unlock_factor: f64,
    pub(super) sample_count: usize,
    pub(super) max_queued: u64,
    pub(super) ignore_queue_time: bool,
    pub(super) ignore_input_processing_time: bool,
}

impl GroqMultiregionConfig {
    pub(super) fn from_algorithm_config(config: &LoadBalancerAlgorithmConfig) -> Self {
        let config = config
            .multiregion_settings()
            .expect("multiregion settings should match load-balancer algorithm");
        Self::from_settings(config)
    }

    pub(super) fn from_settings(config: &GroqMultiregionAlgorithmConfig) -> Self {
        Self {
            seed: config.seed.clone(),
            // Zero virtual nodes would make affinity routing degenerate; keep the historical minimum.
            cache_affinity_virtual_nodes: config.cache_affinity_virtual_nodes.unwrap_or(150).max(1),
            cache_affinity_backend_selection_count: config
                .cache_affinity_backend_selection_count
                .filter(|count| *count > 0),
            queue_slo: config
                .max_queue_time_floor_ms
                .zip(config.max_queue_time_ceil_ms)
                .map(|(floor_ms, ceil_ms)| {
                    Duration::from_millis(floor_ms)..=Duration::from_millis(ceil_ms)
                }),
            ttft_bucket_size: Duration::from_millis(config.ttft_bucket_size_ms.unwrap_or(20)),
            next_bucket_unlock_factor: config.next_bucket_unlock_factor.unwrap_or(0.25),
            // Sampling at least one backend keeps the algorithm meaningful even when config sets n=0.
            sample_count: config.n.unwrap_or(2).max(1),
            max_queued: config.max_queued.unwrap_or(0),
            ignore_queue_time: config.ignore_queue_time.unwrap_or(false),
            ignore_input_processing_time: config.ignore_input_processing_time.unwrap_or(false),
        }
    }

    pub(super) fn max_queue_time(&self, request: &LoadBalancerRequest<'_>) -> Option<Duration> {
        let queue_slo = self.queue_slo.as_ref()?;
        let floor_ms = queue_slo.start().as_secs_f64() * 1000.0;
        let ceil_ms = queue_slo.end().as_secs_f64() * 1000.0;
        let slo_elapsed_percentage = match request.request_slo {
            Some(request_slo) if !request_slo.is_zero() => {
                (request.received_at.elapsed().as_secs_f64() / request_slo.as_secs_f64())
                    .clamp(0.0, 1.0)
            }
            _ => 1.0,
        };
        let max_queue_time_ms = floor_ms + (ceil_ms - floor_ms) * slo_elapsed_percentage;
        Some(Duration::from_secs_f64(max_queue_time_ms / 1000.0))
    }
}

impl_display!(GroqMultiregionLoadBalancer, "groq-multiregion");

impl LoadBalancer for GroqMultiregionLoadBalancer {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if let Some(affinity_indices) =
            self.cache_affinity
                .candidate_indices(&self.config, request, candidates)
            && let Some(choice) =
                self.choose_from_candidate_indices(request, candidates, affinity_indices.as_slice())
        {
            return Some(choice);
        }

        if candidates.is_empty() {
            return None;
        }
        let config = &self.config;
        fast_path::choose_from_single_bucket(config, request, candidates)
            .or_else(|| self.choose_from_candidate_iter(request, candidates.iter(), candidates))
    }
}

impl GroqMultiregionLoadBalancer {
    pub(super) fn new(config: GroqMultiregionConfig) -> Self {
        Self {
            config,
            cache_affinity: CacheAffinitySelector::default(),
        }
    }

    pub(super) fn has_queue_slo(&self, request: &LoadBalancerRequest<'_>) -> bool {
        self.config.max_queue_time(request).is_some()
    }

    #[cfg(test)]
    pub(super) fn cached_affinity_key_bytes(&self) -> usize {
        self.cache_affinity.cached_key_bytes()
    }

    pub(super) fn choose_from_candidate_indices(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        candidate_indices: &[usize],
    ) -> Option<LoadBalancerCandidateChoice> {
        if candidate_indices.is_empty() {
            return None;
        }
        if let Some(choice) =
            self.choose_from_two_ready_affinity_candidates(request, candidates, candidate_indices)
        {
            return Some(choice);
        }

        // Cache-affinity selection stores indices into the current candidate
        // slice. Routing over references avoids cloning snapshots into a
        // temporary Vec for every affinity hit.
        self.choose_from_candidate_iter(
            request,
            candidate_indices.iter().map(|index| &candidates[*index]),
            candidates,
        )
    }

    fn choose_from_two_ready_affinity_candidates(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        candidate_indices: &[usize],
    ) -> Option<LoadBalancerCandidateChoice> {
        let &[index_a, index_b] = candidate_indices else {
            return None;
        };
        if self.config.sample_count < 2 || self.config.max_queue_time(request).is_some() {
            return None;
        }

        let candidate_a = &candidates[index_a];
        let candidate_b = &candidates[index_b];
        if [candidate_a, candidate_b].into_iter().any(|candidate| {
            request.excludes_cluster(&candidate.cluster_id)
                || !has_capacity(candidate, self.config.max_queued)
        }) {
            return None;
        }

        let estimate = |candidate| {
            estimate_ttft_ms(
                candidate,
                request.input_tokens,
                request.priority,
                self.config.ignore_queue_time,
                self.config.ignore_input_processing_time,
            )
        };
        let estimate_a = estimate(candidate_a);
        let estimate_b = estimate(candidate_b);
        if !estimate_a.ttft_ms.is_finite()
            || !estimate_b.ttft_ms.is_finite()
            || (estimate_a.ttft_ms - estimate_b.ttft_ms).abs()
                > self.config.ttft_bucket_size.as_secs_f64() * 1000.0
        {
            return None;
        }

        // With exactly two affinity-selected candidates and n >= 2, the normal
        // shuffle samples both candidates. Equal candidates used to be decided
        // by the shuffled order, so keep that random tie-break explicitly while
        // avoiding the allocation and prefix-shuffle overhead on cache hits.
        let mut rng = rand::rng();
        let candidate = choose_less_queued_candidate(
            candidate_a,
            &estimate_a,
            candidate_b,
            &estimate_b,
            &mut rng,
        );
        Some(LoadBalancerCandidateChoice::with_rank_depth_1(
            if std::ptr::eq(candidate, candidate_a) {
                index_a
            } else {
                index_b
            },
        ))
    }

    fn choose_from_candidate_iter<'a>(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: impl ExactSizeIterator<Item = &'a RoutedClusterSnapshot>,
        candidate_index_source: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let mut estimates =
            CandidateEstimateAccumulator::new(&self.config, request, candidates.len());
        if let RequestExclusions::One(excluded_cluster_id) = RequestExclusions::from(request) {
            for candidate in candidates {
                if candidate.cluster_id != excluded_cluster_id {
                    estimates.push_estimate(candidate);
                }
            }
        } else if request.has_excluded_clusters() || estimates.filters_by_queue_slo() {
            for candidate in candidates {
                if !request.excludes_cluster(&candidate.cluster_id) {
                    estimates.push_estimate(candidate);
                }
            }
        } else {
            for candidate in candidates {
                estimates.push_estimate(candidate);
            }
        }

        if estimates.is_empty() || !estimates.has_finite_fastest_ttft() {
            return None;
        }

        let bucket_size_ms = self.config.ttft_bucket_size.as_secs_f64() * 1000.0;
        if estimates.all_estimates_in_first_bucket(bucket_size_ms) {
            return self.choose_from_unlocked_candidates(
                estimates.into_estimates(),
                candidate_index_source,
            );
        }

        let mut estimated = estimates.into_estimates();
        estimated.sort_unstable_by(|(candidate_a, estimate_a), (candidate_b, estimate_b)| {
            estimate_a
                .ttft_ms
                .total_cmp(&estimate_b.ttft_ms)
                .then_with(|| candidate_a.cluster_id.cmp(&candidate_b.cluster_id))
        });

        let mut slept_for_ms = request.received_at.elapsed().as_secs_f64() * 1000.0;
        let mut prev_bucket_start_ttft = None;
        let mut unlocked_count = 0;

        for (_, estimate) in &estimated {
            if !estimate.ttft_ms.is_finite() {
                break;
            }

            let bucket_start_ttft = prev_bucket_start_ttft.get_or_insert(estimate.ttft_ms);
            let gap_ms = estimate.ttft_ms - *bucket_start_ttft;
            if gap_ms > bucket_size_ms {
                let sleep_for_at_least_ms = gap_ms * self.config.next_bucket_unlock_factor;
                if slept_for_ms < sleep_for_at_least_ms {
                    break;
                }
                slept_for_ms -= sleep_for_at_least_ms;
                *bucket_start_ttft = estimate.ttft_ms;
            }

            unlocked_count += 1;
        }

        estimated.truncate(unlocked_count);
        self.choose_from_unlocked_candidates(estimated, candidate_index_source)
    }

    fn choose_from_unlocked_candidates(
        &self,
        mut unlocked_with_capacity: Vec<(&RoutedClusterSnapshot, TtftEstimate)>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        // The caller already owns this candidate buffer. Retaining in place
        // preserves the later shuffle semantics while avoiding a second Vec
        // allocation on every routing decision.
        unlocked_with_capacity
            .retain(|(candidate, _)| has_capacity(candidate, self.config.max_queued));

        if unlocked_with_capacity.is_empty() {
            return None;
        }

        let sampled_count = self.config.sample_count.min(unlocked_with_capacity.len());
        shuffle_prefix(&mut unlocked_with_capacity, sampled_count);

        unlocked_with_capacity
            .into_iter()
            .take(sampled_count)
            .min_by(|(candidate_a, estimate_a), (candidate_b, estimate_b)| {
                compare_least_queue_time(candidate_a, estimate_a, candidate_b, estimate_b)
            })
            .map(|(candidate, _)| choice_for_candidate(candidates, candidate, 1))
    }
}

fn choice_for_candidate(
    candidates: &[RoutedClusterSnapshot],
    selected: &RoutedClusterSnapshot,
    rank_depth: usize,
) -> LoadBalancerCandidateChoice {
    let base = candidates.as_ptr() as usize;
    let selected_ptr = std::ptr::from_ref(selected) as usize;
    let stride = std::mem::size_of::<RoutedClusterSnapshot>();
    let byte_offset = selected_ptr
        .checked_sub(base)
        .expect("selected candidate should come from candidate slice");
    assert_eq!(
        byte_offset % stride,
        0,
        "selected candidate should align with candidate slice"
    );
    let candidate_index = byte_offset / stride;
    assert!(
        candidate_index < candidates.len(),
        "selected candidate should come from candidate slice"
    );
    LoadBalancerCandidateChoice {
        candidate_index,
        rank_depth,
        selected_after_kv_free_tokens_skip: false,
    }
}

fn choose_less_queued_candidate<'a>(
    candidate_a: &'a RoutedClusterSnapshot,
    estimate_a: &TtftEstimate,
    candidate_b: &'a RoutedClusterSnapshot,
    estimate_b: &TtftEstimate,
    rng: &mut impl Rng,
) -> &'a RoutedClusterSnapshot {
    match compare_least_queue_time(candidate_a, estimate_a, candidate_b, estimate_b) {
        Ordering::Less => candidate_a,
        Ordering::Equal if rng.random_bool(0.5) => candidate_a,
        Ordering::Equal | Ordering::Greater => candidate_b,
    }
}

#[derive(Clone, Copy)]
enum RequestExclusions<'a> {
    None,
    One(&'a str),
    Two(&'a str, &'a str),
    Many,
}

impl<'a> From<&LoadBalancerRequest<'a>> for RequestExclusions<'a> {
    fn from(request: &LoadBalancerRequest<'a>) -> Self {
        let Some(excluded) = request.excluded_cluster_ids else {
            return Self::None;
        };
        let mut ids = excluded.iter().map(String::as_str);
        match excluded.len() {
            0 => Self::None,
            1 => Self::One(ids.next().expect("single exclusion should contain one ID")),
            2 => {
                let first = ids
                    .next()
                    .expect("two exclusions should contain a first ID");
                let second = ids
                    .next()
                    .expect("two exclusions should contain a second ID");
                Self::Two(first.min(second), first.max(second))
            }
            _ => Self::Many,
        }
    }
}

fn shuffle_prefix<T>(items: &mut [T], count: usize) {
    let mut rng = rand::rng();
    for index in 0..count {
        let swap_index = rng.random_range(index..items.len());
        items.swap(index, swap_index);
    }
}

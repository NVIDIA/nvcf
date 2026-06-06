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

use std::cmp::Ordering;
use std::collections::{HashMap, VecDeque};
use std::fmt;
use std::sync::Arc;
use std::time::Duration;

use parking_lot::RwLock;
use rand::Rng;
use xxhash_rust::xxh3::xxh3_64;

use super::{
    GroqMultiregionAlgorithmConfig, HashInputBuilder, LoadBalancer, LoadBalancerAlgorithmConfig,
    LoadBalancerCandidateChoice, LoadBalancerRequest, cache_affinity_key_is_cacheable,
};
use crate::routing_state::{RoutedClusterSnapshot, RoutingTargetKey};

const CACHE_AFFINITY_SELECTION_CACHE_LIMIT: usize = 4096;

// Parity notes vs lpu-router MultiRegion:
// - Stargate uses only the sticky last_mean_input_tps capacity signal for
//   queue/prefill estimates; there is no separate live input-TPS fallback.
// - Sparse priority queue maps use the nearest published priority at or below the request priority;
//   lpu-router clamps only priorities above the max and otherwise treats missing entries as zero.
// - Stargate does not currently model lpu-router batch-folding queue estimates, LoRA dynamic-model
//   penalties, backend/datacenter request filters, backend-id overrides, or utilization-max rejection.
pub(super) struct GroqMultiregionLoadBalancer {
    config: GroqMultiregionConfig,
    cache_affinity_ring: RwLock<CacheAffinityRingCache>,
}

#[derive(Clone, Copy, Debug)]
struct TtftEstimate {
    queue_ms: f64,
    ttft_ms: f64,
}

struct CandidateEstimateAccumulator<'a> {
    estimates: Vec<(&'a RoutedClusterSnapshot, TtftEstimate)>,
    input_tokens: Option<u64>,
    priority: u32,
    max_queue_time_ms: Option<f64>,
    ignore_queue_time: bool,
    ignore_input_processing_time: bool,
    fastest_ttft: f64,
    slowest_ttft: f64,
    all_estimates_finite: bool,
}

impl<'a> CandidateEstimateAccumulator<'a> {
    fn new(
        config: &GroqMultiregionConfig,
        request: &LoadBalancerRequest<'_>,
        candidate_capacity: usize,
    ) -> Self {
        let max_queue_time_ms = config
            .max_queue_time(request)
            .map(|duration| duration.as_secs_f64() * 1000.0);

        Self {
            estimates: Vec::with_capacity(candidate_capacity),
            input_tokens: request.input_tokens,
            priority: request.priority,
            max_queue_time_ms,
            ignore_queue_time: config.ignore_queue_time(),
            ignore_input_processing_time: config.ignore_input_processing_time(),
            fastest_ttft: f64::INFINITY,
            slowest_ttft: f64::NEG_INFINITY,
            all_estimates_finite: true,
        }
    }

    fn filters_by_queue_slo(&self) -> bool {
        self.max_queue_time_ms.is_some()
    }

    fn push_estimate(&mut self, candidate: &'a RoutedClusterSnapshot) {
        let estimate = estimate_ttft_ms(
            candidate,
            self.input_tokens,
            self.priority,
            self.ignore_queue_time,
            self.ignore_input_processing_time,
        );
        if !within_queue_slo(&estimate, self.max_queue_time_ms) {
            return;
        }

        if estimate.ttft_ms.is_finite() {
            self.fastest_ttft = self.fastest_ttft.min(estimate.ttft_ms);
            self.slowest_ttft = self.slowest_ttft.max(estimate.ttft_ms);
        } else {
            self.all_estimates_finite = false;
        }
        self.estimates.push((candidate, estimate));
    }

    fn is_empty(&self) -> bool {
        self.estimates.is_empty()
    }

    fn has_finite_fastest_ttft(&self) -> bool {
        self.fastest_ttft.is_finite()
    }

    fn all_estimates_in_first_bucket(&self, bucket_size_ms: f64) -> bool {
        self.all_estimates_finite && self.slowest_ttft - self.fastest_ttft <= bucket_size_ms
    }

    fn into_estimates(self) -> Vec<(&'a RoutedClusterSnapshot, TtftEstimate)> {
        self.estimates
    }
}

#[derive(Clone, Debug)]
pub(super) struct GroqMultiregionConfig {
    seed: Option<String>,
    cache_affinity_virtual_nodes: usize,
    cache_affinity_backend_selection_count: Option<usize>,
    queue_slo: Option<QueueSloConfig>,
    ttft_bucket_size: Duration,
    next_bucket_unlock_factor: f64,
    sample_count: usize,
    max_queued: u64,
    ignore_queue_time: bool,
    ignore_input_processing_time: bool,
}

#[derive(Clone, Debug)]
struct QueueSloConfig {
    floor: Duration,
    ceil: Duration,
}

impl GroqMultiregionLoadBalancer {
    pub(super) fn new(config: GroqMultiregionConfig) -> Self {
        Self {
            config,
            cache_affinity_ring: RwLock::new(CacheAffinityRingCache::default()),
        }
    }

    pub(super) fn has_queue_slo(&self, request: &LoadBalancerRequest<'_>) -> bool {
        self.config.max_queue_time(request).is_some()
    }

    #[cfg(test)]
    pub(super) fn cached_affinity_key_bytes(&self) -> usize {
        let cache = self.cache_affinity_ring.read();
        cache.cached_key_bytes()
    }
}

impl GroqMultiregionConfig {
    pub(super) fn from_algorithm_config(config: &LoadBalancerAlgorithmConfig) -> Self {
        let config = config
            .multiregion_settings()
            .expect("multiregion settings should match load-balancer algorithm");
        Self::from_settings(config)
    }

    pub(super) fn from_settings(config: &GroqMultiregionAlgorithmConfig) -> Self {
        let queue_slo = match (
            config.max_queue_time_floor_ms,
            config.max_queue_time_ceil_ms,
        ) {
            (Some(floor_ms), Some(ceil_ms)) => Some(QueueSloConfig {
                floor: Duration::from_millis(floor_ms),
                ceil: Duration::from_millis(ceil_ms),
            }),
            _ => None,
        };

        // Zero virtual nodes would make affinity routing degenerate; keep the historical minimum.
        let cache_affinity_virtual_nodes =
            config.cache_affinity_virtual_nodes.unwrap_or(150).max(1);
        // Sampling at least one backend keeps the algorithm meaningful even when config sets n=0.
        let sample_count = config.n.unwrap_or(2).max(1);

        Self {
            seed: config.seed.clone(),
            cache_affinity_virtual_nodes,
            cache_affinity_backend_selection_count: config
                .cache_affinity_backend_selection_count
                .filter(|count| *count > 0),
            queue_slo,
            ttft_bucket_size: Duration::from_millis(config.ttft_bucket_size_ms.unwrap_or(20)),
            next_bucket_unlock_factor: config.next_bucket_unlock_factor.unwrap_or(0.25),
            sample_count,
            max_queued: config.max_queued.unwrap_or(0),
            ignore_queue_time: config.ignore_queue_time.unwrap_or(false),
            ignore_input_processing_time: config.ignore_input_processing_time.unwrap_or(false),
        }
    }

    pub(super) fn cache_affinity_virtual_nodes(&self) -> usize {
        self.cache_affinity_virtual_nodes
    }

    pub(super) fn cache_affinity_backend_selection_count(&self) -> Option<usize> {
        self.cache_affinity_backend_selection_count
    }

    pub(super) fn max_queue_time(&self, request: &LoadBalancerRequest<'_>) -> Option<Duration> {
        let queue_slo = self.queue_slo.as_ref()?;

        let floor_ms = queue_slo.floor.as_secs_f64() * 1000.0;
        let ceil_ms = queue_slo.ceil.as_secs_f64() * 1000.0;
        let slo_elapsed_percentage = request_slo_elapsed_percentage(request);
        let max_queue_time_ms = floor_ms + (ceil_ms - floor_ms) * slo_elapsed_percentage;
        Some(Duration::from_secs_f64(max_queue_time_ms / 1000.0))
    }

    pub(super) fn ttft_bucket_size(&self) -> Duration {
        self.ttft_bucket_size
    }

    pub(super) fn next_bucket_unlock_factor(&self) -> f64 {
        self.next_bucket_unlock_factor
    }

    pub(super) fn sample_count(&self) -> usize {
        self.sample_count
    }

    pub(super) fn max_queued(&self) -> u64 {
        self.max_queued
    }

    pub(super) fn ignore_queue_time(&self) -> bool {
        self.ignore_queue_time
    }

    pub(super) fn ignore_input_processing_time(&self) -> bool {
        self.ignore_input_processing_time
    }

    fn seed(&self) -> Option<&str> {
        self.seed.as_deref()
    }
}

fn request_slo_elapsed_percentage(request: &LoadBalancerRequest<'_>) -> f64 {
    let Some(request_slo) = request.request_slo else {
        return 1.0;
    };
    if request_slo.is_zero() {
        return 1.0;
    }

    (request.received_at.elapsed().as_secs_f64() / request_slo.as_secs_f64()).clamp(0.0, 1.0)
}

impl fmt::Display for GroqMultiregionLoadBalancer {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "groq-multiregion")
    }
}

impl LoadBalancer for GroqMultiregionLoadBalancer {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if let Some(affinity_indices) = self.cache_affinity_candidate_indices(request, candidates)
            && let Some(choice) =
                self.choose_from_candidate_indices(request, candidates, &affinity_indices)
        {
            return Some(choice);
        }

        self.choose_from_candidates(request, candidates)
    }
}

impl GroqMultiregionLoadBalancer {
    fn cache_affinity_candidate_indices(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<Arc<Vec<usize>>> {
        let cache_affinity_key = request.cache_affinity_key?;
        let selection_count = self.config.cache_affinity_backend_selection_count()?;
        if candidates.is_empty() {
            return None;
        }

        let selection_count = selection_count.min(candidates.len());
        let single_excluded_cluster_id = single_excluded_cluster_id(request);
        let double_excluded_cluster_ids = double_excluded_cluster_ids(request);
        let cacheable_selection = cache_affinity_key_is_cacheable(cache_affinity_key)
            && (!request.has_excluded_clusters()
                || single_excluded_cluster_id.is_some()
                || double_excluded_cluster_ids.is_some());
        let selected_indices = match self.cached_or_computed_affinity_selection(
            request,
            candidates,
            cache_affinity_key,
            selection_count,
            cacheable_selection,
        ) {
            CacheAffinitySelectionLookup::Hit(indices) => indices,
            CacheAffinitySelectionLookup::Computed(indices) => {
                if cacheable_selection && !indices.is_empty() {
                    let mut cache = self.cache_affinity_ring.write();
                    if cache.matches(request.routing_target, candidates) {
                        cache.insert_selection(
                            cache_affinity_key,
                            single_excluded_cluster_id,
                            double_excluded_cluster_ids,
                            indices.clone(),
                        );
                    }
                }
                indices
            }
            CacheAffinitySelectionLookup::Stale => {
                let ring = build_cache_affinity_ring(&self.config, request, candidates);
                let indices = Arc::new(select_cache_affinity_candidate_indices(
                    &self.config,
                    request,
                    &ring,
                    cache_affinity_key,
                    selection_count,
                ));
                let mut cache = self.cache_affinity_ring.write();
                cache.replace(request.routing_target, candidates, ring);
                if cacheable_selection && !indices.is_empty() {
                    cache.insert_selection(
                        cache_affinity_key,
                        single_excluded_cluster_id,
                        double_excluded_cluster_ids,
                        indices.clone(),
                    );
                }
                indices
            }
        };

        (!selected_indices.is_empty()).then_some(selected_indices)
    }

    fn cached_or_computed_affinity_selection(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        cache_affinity_key: &str,
        selection_count: usize,
        cacheable_key: bool,
    ) -> CacheAffinitySelectionLookup {
        let cache = self.cache_affinity_ring.read();
        if !cache.matches(request.routing_target, candidates) {
            return CacheAffinitySelectionLookup::Stale;
        }

        if cacheable_key {
            if let Some(excluded_cluster_id) = single_excluded_cluster_id(request) {
                if let Some(indices) =
                    cache.single_excluded_selection(cache_affinity_key, excluded_cluster_id)
                {
                    return CacheAffinitySelectionLookup::Hit(indices);
                }
            } else if let Some((first_excluded, second_excluded)) =
                double_excluded_cluster_ids(request)
            {
                if let Some(indices) = cache.two_excluded_selection(
                    cache_affinity_key,
                    first_excluded,
                    second_excluded,
                ) {
                    return CacheAffinitySelectionLookup::Hit(indices);
                }
            } else if let Some(indices) = cache.selection(cache_affinity_key) {
                return CacheAffinitySelectionLookup::Hit(indices);
            }
        }

        let indices = Arc::new(select_cache_affinity_candidate_indices(
            &self.config,
            request,
            &cache.ring,
            cache_affinity_key,
            selection_count,
        ));
        CacheAffinitySelectionLookup::Computed(indices)
    }

    fn choose_from_candidates(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if candidates.is_empty() {
            return None;
        }
        if let Some(choice) = self.choose_from_rtt_only_single_bucket(request, candidates) {
            return Some(choice);
        }
        if let Some(choice) = self.choose_from_queue_ignored_single_bucket(request, candidates) {
            return Some(choice);
        }
        self.choose_from_candidate_iter(request, candidates, candidates.len(), candidates)
    }

    fn choose_from_queue_ignored_single_bucket(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if !self.config.ignore_queue_time() || self.config.ignore_input_processing_time() {
            return None;
        }
        if self.config.max_queue_time(request).is_some() {
            return None;
        }

        match request.excluded_cluster_ids {
            Some(excluded) if excluded.is_empty() => self
                .choose_from_queue_ignored_single_bucket_filtered(request, candidates, |_| false),
            None => {
                self.choose_from_queue_ignored_single_bucket_filtered(request, candidates, |_| {
                    false
                })
            }
            Some(excluded) if excluded.len() == 1 => {
                let excluded_cluster_id = single_excluded_cluster_id(request)?;
                self.choose_from_queue_ignored_single_bucket_filtered(
                    request,
                    candidates,
                    |candidate| candidate.cluster_id == excluded_cluster_id,
                )
            }
            Some(excluded) if excluded.len() == 2 => {
                let mut excluded_ids = excluded.iter().map(String::as_str);
                let first_excluded = excluded_ids.next()?;
                let second_excluded = excluded_ids.next()?;
                self.choose_from_queue_ignored_single_bucket_filtered(
                    request,
                    candidates,
                    |candidate| {
                        let cluster_id = candidate.cluster_id.as_str();
                        cluster_id == first_excluded || cluster_id == second_excluded
                    },
                )
            }
            // Larger retry exclusion sets are uncommon and need more filtering
            // work per candidate. Keep them on the general path instead of
            // making the steady-state ignore-queue fast path carry a HashSet probe.
            Some(_) => None,
        }
    }

    fn choose_from_queue_ignored_single_bucket_filtered(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        mut excludes_candidate: impl FnMut(&RoutedClusterSnapshot) -> bool,
    ) -> Option<LoadBalancerCandidateChoice> {
        let input_tokens = request.input_tokens.unwrap_or(0) as f64;
        let mut fastest_ttft = f64::INFINITY;
        let mut slowest_ttft = f64::NEG_INFINITY;
        for candidate in candidates {
            if excludes_candidate(candidate) {
                continue;
            }
            let ttft_ms = queue_ignored_ttft_ms(candidate, input_tokens);
            if !ttft_ms.is_finite() {
                return None;
            }
            fastest_ttft = fastest_ttft.min(ttft_ms);
            slowest_ttft = slowest_ttft.max(ttft_ms);
        }

        let bucket_size_ms = self.config.ttft_bucket_size().as_secs_f64() * 1000.0;
        if !fastest_ttft.is_finite() || slowest_ttft - fastest_ttft > bucket_size_ms {
            return None;
        }

        let max_queued = self.config.max_queued();
        let mut unlocked_with_capacity = Vec::with_capacity(candidates.len());
        for candidate in candidates {
            if excludes_candidate(candidate) {
                continue;
            }
            if has_capacity(candidate, max_queued) {
                unlocked_with_capacity.push(candidate);
            }
        }

        // Queue is intentionally ignored for TTFT bucket construction in this
        // config, but it is still the primary sampled-candidate comparator. Keep
        // queue estimation to the sample instead of paying it for every backend.
        self.choose_from_unlocked_candidate_refs(request, unlocked_with_capacity, candidates)
    }

    fn choose_from_rtt_only_single_bucket(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if !self.config.ignore_queue_time() || !self.config.ignore_input_processing_time() {
            return None;
        }
        if self.config.max_queue_time(request).is_some() {
            return None;
        }

        match request.excluded_cluster_ids {
            Some(excluded) if excluded.is_empty() => {
                self.choose_from_rtt_only_single_bucket_filtered(request, candidates, |_| false)
            }
            None => {
                self.choose_from_rtt_only_single_bucket_filtered(request, candidates, |_| false)
            }
            Some(excluded) if excluded.len() == 1 => {
                let excluded_cluster_id = single_excluded_cluster_id(request)?;
                self.choose_from_rtt_only_single_bucket_filtered(request, candidates, |candidate| {
                    candidate.cluster_id == excluded_cluster_id
                })
            }
            Some(excluded) if excluded.len() == 2 => {
                let mut excluded_ids = excluded.iter().map(String::as_str);
                let first_excluded = excluded_ids.next()?;
                let second_excluded = excluded_ids.next()?;
                self.choose_from_rtt_only_single_bucket_filtered(request, candidates, |candidate| {
                    let cluster_id = candidate.cluster_id.as_str();
                    cluster_id == first_excluded || cluster_id == second_excluded
                })
            }
            // Larger retry exclusion sets are uncommon and need more filtering
            // work per candidate. Keep them on the general path instead of
            // making the steady-state RTT-only fast path carry a HashSet probe.
            Some(_) => None,
        }
    }

    fn choose_from_rtt_only_single_bucket_filtered(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        mut excludes_candidate: impl FnMut(&RoutedClusterSnapshot) -> bool,
    ) -> Option<LoadBalancerCandidateChoice> {
        let mut fastest_ttft = f64::INFINITY;
        let mut slowest_ttft = f64::NEG_INFINITY;
        for candidate in candidates {
            if excludes_candidate(candidate) {
                continue;
            }
            let ttft_ms = rtt_ms(candidate);
            fastest_ttft = fastest_ttft.min(ttft_ms);
            slowest_ttft = slowest_ttft.max(ttft_ms);
        }

        let bucket_size_ms = self.config.ttft_bucket_size().as_secs_f64() * 1000.0;
        if !fastest_ttft.is_finite() || slowest_ttft - fastest_ttft > bucket_size_ms {
            return None;
        }

        let max_queued = self.config.max_queued();
        let mut unlocked_with_capacity = Vec::with_capacity(candidates.len());
        for candidate in candidates {
            if excludes_candidate(candidate) {
                continue;
            }
            if has_capacity(candidate, max_queued) {
                unlocked_with_capacity.push(candidate);
            }
        }
        // When both non-RTT TTFT components are ignored and every candidate is
        // already in the first bucket, routing only needs queue estimates for
        // the sampled candidates. Computing sparse priority queue estimates for
        // all non-excluded backends would preserve correctness but wastes work
        // on the common wide-bucket, n=2 deployment shape.
        self.choose_from_unlocked_candidate_refs(request, unlocked_with_capacity, candidates)
    }

    fn choose_from_unlocked_candidate_refs(
        &self,
        request: &LoadBalancerRequest<'_>,
        mut unlocked_with_capacity: Vec<&RoutedClusterSnapshot>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if unlocked_with_capacity.is_empty() {
            return None;
        }

        let sample_count = self.config.sample_count();
        if sample_count == 1 {
            let selected_index = rand::rng().random_range(0..unlocked_with_capacity.len());
            return Some(choice_for_candidate(
                candidates,
                unlocked_with_capacity[selected_index],
                1,
            ));
        }
        if sample_count == 2 {
            return choose_two_rtt_only_candidates(
                &unlocked_with_capacity,
                request.priority,
                candidates,
            );
        }

        let sampled_count = sample_count.min(unlocked_with_capacity.len());
        shuffle_prefix(&mut unlocked_with_capacity, sampled_count);
        unlocked_with_capacity
            .into_iter()
            .take(sampled_count)
            .map(|candidate| {
                (
                    candidate,
                    estimate_queue_comparison(candidate, request.priority),
                )
            })
            .min_by(|(candidate_a, estimate_a), (candidate_b, estimate_b)| {
                compare_least_queue_time(candidate_a, estimate_a, candidate_b, estimate_b)
            })
            .map(|(candidate, _)| choice_for_candidate(candidates, candidate, 1))
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
            candidate_indices.len(),
            candidates,
        )
    }

    fn choose_from_two_ready_affinity_candidates(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        candidate_indices: &[usize],
    ) -> Option<LoadBalancerCandidateChoice> {
        if candidate_indices.len() != 2 || self.config.sample_count() < 2 {
            return None;
        }
        if self.config.max_queue_time(request).is_some() {
            return None;
        }

        let candidate_a = &candidates[candidate_indices[0]];
        let candidate_b = &candidates[candidate_indices[1]];
        if request.excludes_cluster(&candidate_a.cluster_id)
            || request.excludes_cluster(&candidate_b.cluster_id)
        {
            return None;
        }

        let max_queued = self.config.max_queued();
        if !has_capacity(candidate_a, max_queued) || !has_capacity(candidate_b, max_queued) {
            return None;
        }

        let estimate_a = estimate_ttft_ms(
            candidate_a,
            request.input_tokens,
            request.priority,
            self.config.ignore_queue_time(),
            self.config.ignore_input_processing_time(),
        );
        let estimate_b = estimate_ttft_ms(
            candidate_b,
            request.input_tokens,
            request.priority,
            self.config.ignore_queue_time(),
            self.config.ignore_input_processing_time(),
        );
        if !estimate_a.ttft_ms.is_finite() || !estimate_b.ttft_ms.is_finite() {
            return None;
        }

        let bucket_size_ms = self.config.ttft_bucket_size().as_secs_f64() * 1000.0;
        if (estimate_a.ttft_ms - estimate_b.ttft_ms).abs() > bucket_size_ms {
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
        let candidate_index = if std::ptr::eq(candidate, candidate_a) {
            candidate_indices[0]
        } else {
            candidate_indices[1]
        };
        Some(LoadBalancerCandidateChoice::with_rank_depth_1(
            candidate_index,
        ))
    }

    fn choose_from_candidate_iter<'a>(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: impl IntoIterator<Item = &'a RoutedClusterSnapshot>,
        candidate_capacity: usize,
        candidate_index_source: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let candidates = candidates.into_iter();

        let mut estimates =
            CandidateEstimateAccumulator::new(&self.config, request, candidate_capacity);
        if !estimates.filters_by_queue_slo() && !request.has_excluded_clusters() {
            // This is the steady-state proxy path: first-attempt routing with no
            // queue SLO. Keep it separate so every candidate does not pay for
            // retry exclusion and queue-SLO option checks.
            for candidate in candidates {
                estimates.push_estimate(candidate);
            }
        } else if !estimates.filters_by_queue_slo() {
            if let Some(excluded_cluster_id) = single_excluded_cluster_id(request) {
                // Most retries exclude exactly the backend that failed the prior
                // attempt. Compare against that one borrowed id directly instead
                // of doing a HashSet lookup for every candidate. Queue-SLO work is
                // still skipped because this branch is only for configs without a
                // queue SLO.
                for candidate in candidates {
                    if candidate.cluster_id == excluded_cluster_id {
                        continue;
                    }
                    estimates.push_estimate(candidate);
                }
            } else {
                // Multi-exclusion retries are less common, but they still avoid
                // queue-SLO filtering when the algorithm is configured without a
                // queue SLO. Keep the exact exclusion semantics for this fallback.
                for candidate in candidates {
                    if request.excludes_cluster(&candidate.cluster_id) {
                        continue;
                    }
                    estimates.push_estimate(candidate);
                }
            }
        } else {
            for candidate in candidates {
                if request.excludes_cluster(&candidate.cluster_id) {
                    continue;
                }
                estimates.push_estimate(candidate);
            }
        }

        if estimates.is_empty() {
            return None;
        }
        if !estimates.has_finite_fastest_ttft() {
            return None;
        }

        let bucket_size_ms = self.config.ttft_bucket_size().as_secs_f64() * 1000.0;
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

        let unlock_factor = self.config.next_bucket_unlock_factor();
        let mut slept_for_ms = request.received_at.elapsed().as_secs_f64() * 1000.0;
        let mut prev_bucket_start_ttft = None;
        let mut unlocked = Vec::with_capacity(estimated.len());

        for (candidate, estimate) in estimated {
            if !estimate.ttft_ms.is_finite() {
                break;
            }

            if let Some(prev_ttft) = prev_bucket_start_ttft {
                let gap_ms = estimate.ttft_ms - prev_ttft;
                if gap_ms > bucket_size_ms {
                    let sleep_for_at_least_ms = gap_ms * unlock_factor;
                    if slept_for_ms < sleep_for_at_least_ms {
                        break;
                    }
                    slept_for_ms -= sleep_for_at_least_ms;
                    prev_bucket_start_ttft = Some(estimate.ttft_ms);
                }
            } else {
                prev_bucket_start_ttft = Some(estimate.ttft_ms);
            }

            unlocked.push((candidate, estimate));
        }

        self.choose_from_unlocked_candidates(unlocked, candidate_index_source)
    }

    fn choose_from_unlocked_candidates(
        &self,
        mut unlocked_with_capacity: Vec<(&RoutedClusterSnapshot, TtftEstimate)>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let max_queued = self.config.max_queued();
        // The caller already owns this candidate buffer. Retaining in place
        // preserves the later shuffle semantics while avoiding a second Vec
        // allocation on every routing decision.
        unlocked_with_capacity.retain(|(candidate, _)| has_capacity(candidate, max_queued));

        if unlocked_with_capacity.is_empty() {
            return None;
        }

        let sample_count = self.config.sample_count();
        if sample_count == 1 {
            return choose_one_unlocked_candidate(&unlocked_with_capacity, candidates);
        }
        if sample_count == 2 {
            return choose_two_unlocked_candidates(&unlocked_with_capacity, candidates);
        }

        let sampled_count = sample_count.min(unlocked_with_capacity.len());
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

fn choose_one_unlocked_candidate(
    unlocked_with_capacity: &[(&RoutedClusterSnapshot, TtftEstimate)],
    candidates: &[RoutedClusterSnapshot],
) -> Option<LoadBalancerCandidateChoice> {
    if unlocked_with_capacity.is_empty() {
        return None;
    }

    let selected_index = rand::rng().random_range(0..unlocked_with_capacity.len());
    Some(choice_for_candidate(
        candidates,
        unlocked_with_capacity[selected_index].0,
        1,
    ))
}

fn choose_two_unlocked_candidates(
    unlocked_with_capacity: &[(&RoutedClusterSnapshot, TtftEstimate)],
    candidates: &[RoutedClusterSnapshot],
) -> Option<LoadBalancerCandidateChoice> {
    if unlocked_with_capacity.len() < 2 {
        return choose_one_unlocked_candidate(unlocked_with_capacity, candidates);
    }

    let mut rng = rand::rng();
    let candidate_a_index = rng.random_range(0..unlocked_with_capacity.len());
    let mut candidate_b_index = rng.random_range(0..unlocked_with_capacity.len() - 1);
    if candidate_b_index >= candidate_a_index {
        candidate_b_index += 1;
    }

    // This is equivalent to shuffling a two-element prefix: it samples two
    // distinct unlocked candidates uniformly, then applies the same least-queue
    // comparison with an explicit random tie-break for candidates that the old
    // shuffled order would have treated as equal.
    let (candidate_a, estimate_a) = unlocked_with_capacity[candidate_a_index];
    let (candidate_b, estimate_b) = unlocked_with_capacity[candidate_b_index];
    let candidate =
        choose_less_queued_candidate(candidate_a, &estimate_a, candidate_b, &estimate_b, &mut rng);
    Some(choice_for_candidate(candidates, candidate, 1))
}

fn choose_two_rtt_only_candidates(
    unlocked_with_capacity: &[&RoutedClusterSnapshot],
    priority: u32,
    candidates: &[RoutedClusterSnapshot],
) -> Option<LoadBalancerCandidateChoice> {
    if unlocked_with_capacity.len() < 2 {
        if unlocked_with_capacity.is_empty() {
            return None;
        }
        let selected_index = rand::rng().random_range(0..unlocked_with_capacity.len());
        return Some(choice_for_candidate(
            candidates,
            unlocked_with_capacity[selected_index],
            1,
        ));
    }

    let mut rng = rand::rng();
    let candidate_a_index = rng.random_range(0..unlocked_with_capacity.len());
    let mut candidate_b_index = rng.random_range(0..unlocked_with_capacity.len() - 1);
    if candidate_b_index >= candidate_a_index {
        candidate_b_index += 1;
    }

    let candidate_a = unlocked_with_capacity[candidate_a_index];
    let candidate_b = unlocked_with_capacity[candidate_b_index];
    let estimate_a = estimate_queue_comparison(candidate_a, priority);
    let estimate_b = estimate_queue_comparison(candidate_b, priority);
    let candidate =
        choose_less_queued_candidate(candidate_a, &estimate_a, candidate_b, &estimate_b, &mut rng);
    Some(choice_for_candidate(candidates, candidate, 1))
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

fn single_excluded_cluster_id<'a>(request: &LoadBalancerRequest<'a>) -> Option<&'a str> {
    let excluded = request.excluded_cluster_ids?;
    if excluded.len() == 1 {
        excluded.iter().next().map(String::as_str)
    } else {
        None
    }
}

fn double_excluded_cluster_ids<'a>(
    request: &LoadBalancerRequest<'a>,
) -> Option<(&'a str, &'a str)> {
    let excluded = request.excluded_cluster_ids?;
    if excluded.len() != 2 {
        return None;
    }
    let mut excluded_ids = excluded.iter().map(String::as_str);
    let first = excluded_ids.next()?;
    let second = excluded_ids.next()?;
    if first <= second {
        Some((first, second))
    } else {
        Some((second, first))
    }
}

enum CacheAffinitySelectionLookup {
    Hit(Arc<Vec<usize>>),
    Computed(Arc<Vec<usize>>),
    Stale,
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct CacheAffinityRingEntry {
    hash: u64,
    cluster_id: String,
    candidate_index: usize,
}

#[derive(Clone, Debug, Eq, Hash, PartialEq)]
struct ExcludedClusterPair {
    first: String,
    second: String,
}

impl ExcludedClusterPair {
    fn new(first: &str, second: &str) -> Self {
        if first <= second {
            Self {
                first: first.to_string(),
                second: second.to_string(),
            }
        } else {
            Self {
                first: second.to_string(),
                second: first.to_string(),
            }
        }
    }

    #[cfg(test)]
    fn key_bytes(&self) -> usize {
        self.first.len() + self.second.len()
    }
}

#[derive(Debug, Default)]
struct CacheAffinityRingCache {
    target: Option<RoutingTargetKey>,
    candidate_cluster_ids: Vec<String>,
    ring: Vec<CacheAffinityRingEntry>,
    selections: HashMap<String, Arc<Vec<usize>>>,
    selection_order: VecDeque<String>,
    // Retry selections depend on both the affinity key and the backend excluded
    // by the previous attempt. Keep them separate from the no-exclusion cache so
    // the hot first-attempt lookup can still borrow just `&str` without building
    // a composite owned key on every request.
    single_excluded_selections: HashMap<String, HashMap<String, Arc<Vec<usize>>>>,
    single_excluded_selection_order: VecDeque<(String, String)>,
    single_excluded_selection_count: usize,
    // Two-exclusion retries happen after two failed attempts. Cache those
    // separately so the common no-exclusion and single-exclusion lookup shapes
    // stay simple while repeated retry keys avoid another affinity-ring walk.
    two_excluded_selections: HashMap<String, HashMap<ExcludedClusterPair, Arc<Vec<usize>>>>,
    two_excluded_selection_order: VecDeque<(String, ExcludedClusterPair)>,
    two_excluded_selection_count: usize,
}

impl CacheAffinityRingCache {
    fn replace(
        &mut self,
        target: &RoutingTargetKey,
        candidates: &[RoutedClusterSnapshot],
        ring: Vec<CacheAffinityRingEntry>,
    ) {
        self.target = Some(target.clone());
        self.candidate_cluster_ids = candidates
            .iter()
            .map(|candidate| candidate.cluster_id.clone())
            .collect();
        self.ring = ring;
        self.selections.clear();
        self.selection_order.clear();
        self.single_excluded_selections.clear();
        self.single_excluded_selection_order.clear();
        self.single_excluded_selection_count = 0;
        self.two_excluded_selections.clear();
        self.two_excluded_selection_order.clear();
        self.two_excluded_selection_count = 0;
    }

    fn matches(&self, target: &RoutingTargetKey, candidates: &[RoutedClusterSnapshot]) -> bool {
        self.target.as_ref() == Some(target)
            && self.candidate_cluster_ids.len() == candidates.len()
            && self
                .candidate_cluster_ids
                .iter()
                .zip(candidates)
                .all(|(cached, candidate)| cached == &candidate.cluster_id)
    }

    fn selection(&self, cache_affinity_key: &str) -> Option<Arc<Vec<usize>>> {
        self.selections.get(cache_affinity_key).cloned()
    }

    fn single_excluded_selection(
        &self,
        cache_affinity_key: &str,
        excluded_cluster_id: &str,
    ) -> Option<Arc<Vec<usize>>> {
        self.single_excluded_selections
            .get(cache_affinity_key)
            .and_then(|by_excluded| by_excluded.get(excluded_cluster_id))
            .cloned()
    }

    fn two_excluded_selection(
        &self,
        cache_affinity_key: &str,
        first_excluded: &str,
        second_excluded: &str,
    ) -> Option<Arc<Vec<usize>>> {
        let excluded_pair = ExcludedClusterPair::new(first_excluded, second_excluded);
        self.two_excluded_selections
            .get(cache_affinity_key)
            .and_then(|by_excluded| by_excluded.get(&excluded_pair))
            .cloned()
    }

    fn insert_selection(
        &mut self,
        cache_affinity_key: &str,
        single_excluded_cluster_id: Option<&str>,
        double_excluded_cluster_ids: Option<(&str, &str)>,
        selected_indices: Arc<Vec<usize>>,
    ) {
        if let Some(excluded_cluster_id) = single_excluded_cluster_id {
            self.insert_single_excluded_selection(
                cache_affinity_key,
                excluded_cluster_id,
                selected_indices,
            );
            return;
        }
        if let Some((first_excluded, second_excluded)) = double_excluded_cluster_ids {
            self.insert_two_excluded_selection(
                cache_affinity_key,
                first_excluded,
                second_excluded,
                selected_indices,
            );
            return;
        }

        if let Some(existing) = self.selections.get_mut(cache_affinity_key) {
            *existing = selected_indices;
            return;
        }

        self.evict_selection_entries();
        self.selection_order
            .push_back(cache_affinity_key.to_string());
        self.selections
            .insert(cache_affinity_key.to_string(), selected_indices);
    }

    fn insert_single_excluded_selection(
        &mut self,
        cache_affinity_key: &str,
        excluded_cluster_id: &str,
        selected_indices: Arc<Vec<usize>>,
    ) {
        if let Some(by_excluded) = self.single_excluded_selections.get_mut(cache_affinity_key)
            && let Some(existing) = by_excluded.get_mut(excluded_cluster_id)
        {
            *existing = selected_indices;
            return;
        }

        self.evict_selection_entries();
        self.single_excluded_selections
            .entry(cache_affinity_key.to_string())
            .or_default()
            .insert(excluded_cluster_id.to_string(), selected_indices);
        self.single_excluded_selection_order.push_back((
            cache_affinity_key.to_string(),
            excluded_cluster_id.to_string(),
        ));
        self.single_excluded_selection_count += 1;
    }

    fn insert_two_excluded_selection(
        &mut self,
        cache_affinity_key: &str,
        first_excluded: &str,
        second_excluded: &str,
        selected_indices: Arc<Vec<usize>>,
    ) {
        let excluded_pair = ExcludedClusterPair::new(first_excluded, second_excluded);
        if let Some(by_excluded) = self.two_excluded_selections.get_mut(cache_affinity_key)
            && let Some(existing) = by_excluded.get_mut(&excluded_pair)
        {
            *existing = selected_indices;
            return;
        }

        self.evict_selection_entries();
        self.two_excluded_selections
            .entry(cache_affinity_key.to_string())
            .or_default()
            .insert(excluded_pair.clone(), selected_indices);
        self.two_excluded_selection_order
            .push_back((cache_affinity_key.to_string(), excluded_pair));
        self.two_excluded_selection_count += 1;
    }

    fn evict_selection_entries(&mut self) {
        // All affinity-selection caches share the same entry budget. On
        // pressure, evict retry-specific entries first so normal affinity hits
        // keep the same behavior they had before retry caching existed.
        while self.selection_count() >= CACHE_AFFINITY_SELECTION_CACHE_LIMIT {
            if let Some((cache_affinity_key, excluded_pair)) =
                self.two_excluded_selection_order.pop_front()
            {
                self.remove_two_excluded_selection(&cache_affinity_key, &excluded_pair);
                continue;
            }

            if let Some((cache_affinity_key, excluded_cluster_id)) =
                self.single_excluded_selection_order.pop_front()
            {
                self.remove_single_excluded_selection(&cache_affinity_key, &excluded_cluster_id);
                continue;
            }

            let Some(oldest) = self.selection_order.pop_front() else {
                break;
            };
            self.selections.remove(&oldest);
        }
    }

    fn remove_single_excluded_selection(
        &mut self,
        cache_affinity_key: &str,
        excluded_cluster_id: &str,
    ) {
        let Some(by_excluded) = self.single_excluded_selections.get_mut(cache_affinity_key) else {
            return;
        };
        if by_excluded.remove(excluded_cluster_id).is_some() {
            self.single_excluded_selection_count -= 1;
        }
        if by_excluded.is_empty() {
            self.single_excluded_selections.remove(cache_affinity_key);
        }
    }

    fn remove_two_excluded_selection(
        &mut self,
        cache_affinity_key: &str,
        excluded_pair: &ExcludedClusterPair,
    ) {
        let Some(by_excluded) = self.two_excluded_selections.get_mut(cache_affinity_key) else {
            return;
        };
        if by_excluded.remove(excluded_pair).is_some() {
            self.two_excluded_selection_count -= 1;
        }
        if by_excluded.is_empty() {
            self.two_excluded_selections.remove(cache_affinity_key);
        }
    }

    fn selection_count(&self) -> usize {
        self.selections.len()
            + self.single_excluded_selection_count
            + self.two_excluded_selection_count
    }

    #[cfg(test)]
    fn cached_key_bytes(&self) -> usize {
        let plain_key_bytes = self.selections.keys().map(String::len).sum::<usize>();
        let single_excluded_key_bytes = self
            .single_excluded_selections
            .iter()
            .map(|(cache_affinity_key, by_excluded)| {
                cache_affinity_key.len() * by_excluded.len()
                    + by_excluded.keys().map(String::len).sum::<usize>()
            })
            .sum::<usize>();
        let two_excluded_key_bytes = self
            .two_excluded_selections
            .iter()
            .map(|(cache_affinity_key, by_excluded)| {
                cache_affinity_key.len() * by_excluded.len()
                    + by_excluded
                        .keys()
                        .map(ExcludedClusterPair::key_bytes)
                        .sum::<usize>()
            })
            .sum::<usize>();
        plain_key_bytes + single_excluded_key_bytes + two_excluded_key_bytes
    }
}

#[cfg(test)]
pub(super) fn cache_affinity_candidates(
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> Option<Vec<RoutedClusterSnapshot>> {
    let cache_affinity_key = request.cache_affinity_key?;
    let selection_count = config.cache_affinity_backend_selection_count()?;
    if candidates.is_empty() {
        return None;
    }

    let selection_count = selection_count.min(candidates.len());
    let ring = build_cache_affinity_ring(config, request, candidates);
    let selected_indices = select_cache_affinity_candidate_indices(
        config,
        request,
        &ring,
        cache_affinity_key,
        selection_count,
    );

    (!selected_indices.is_empty()).then(|| {
        selected_indices
            .into_iter()
            .map(|index| candidates[index].clone())
            .collect()
    })
}

fn build_cache_affinity_ring(
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> Vec<CacheAffinityRingEntry> {
    let mut ring = Vec::with_capacity(candidates.len() * config.cache_affinity_virtual_nodes());
    for (candidate_index, candidate) in candidates.iter().enumerate() {
        for virtual_node in 0..config.cache_affinity_virtual_nodes() {
            ring.push(CacheAffinityRingEntry {
                hash: cache_affinity_virtual_node_hash(config, request, candidate, virtual_node),
                cluster_id: candidate.cluster_id.clone(),
                candidate_index,
            });
        }
    }
    ring.sort_unstable_by(|a, b| {
        a.hash
            .cmp(&b.hash)
            .then_with(|| a.cluster_id.cmp(&b.cluster_id))
            .then_with(|| a.candidate_index.cmp(&b.candidate_index))
    });
    ring
}

fn select_cache_affinity_candidate_indices(
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    ring: &[CacheAffinityRingEntry],
    cache_affinity_key: &str,
    selection_count: usize,
) -> Vec<usize> {
    if ring.is_empty() {
        return Vec::new();
    }
    let key_hash = cache_affinity_key_hash(config, request, cache_affinity_key);
    let start_index = ring
        .binary_search_by(|entry| entry.hash.cmp(&key_hash))
        .unwrap_or_else(|index| index);
    if let Some(excluded_cluster_id) = single_excluded_cluster_id(request) {
        // Affinity retries normally exclude the single backend that failed the
        // prior attempt. Keep the ring walk identical, but compare against that
        // borrowed id directly instead of paying for a HashSet lookup at each
        // virtual node.
        return select_cache_affinity_candidate_indices_from_ring(
            ring,
            start_index,
            selection_count,
            |cluster_id| cluster_id == excluded_cluster_id,
        );
    }
    if let Some((first_excluded, second_excluded)) = double_excluded_cluster_ids(request) {
        // The third attempt excludes the two earlier failed clusters. The pair
        // case is common enough to avoid a HashSet probe during the affinity
        // ring walk, but still small enough to keep as direct borrowed compares.
        return select_cache_affinity_candidate_indices_from_ring(
            ring,
            start_index,
            selection_count,
            |cluster_id| cluster_id == first_excluded || cluster_id == second_excluded,
        );
    }

    select_cache_affinity_candidate_indices_from_ring(
        ring,
        start_index,
        selection_count,
        |cluster_id| request.excludes_cluster(cluster_id),
    )
}

fn select_cache_affinity_candidate_indices_from_ring(
    ring: &[CacheAffinityRingEntry],
    start_index: usize,
    selection_count: usize,
    mut excludes_cluster: impl FnMut(&str) -> bool,
) -> Vec<usize> {
    let mut selected_indices = Vec::with_capacity(selection_count);
    let mut selected_cluster_ids = Vec::with_capacity(selection_count);
    for offset in 0..ring.len() {
        let entry = &ring[(start_index + offset) % ring.len()];
        if excludes_cluster(&entry.cluster_id) {
            continue;
        }
        if selected_cluster_ids
            .iter()
            .all(|cluster_id| *cluster_id != entry.cluster_id.as_str())
        {
            selected_cluster_ids.push(entry.cluster_id.as_str());
            selected_indices.push(entry.candidate_index);
            if selected_indices.len() >= selection_count {
                break;
            }
        }
    }

    selected_indices
}

const GROQ_MULTIREGION_CACHE_AFFINITY_HASH_VERSION: u8 = 1;

fn cache_affinity_key_hash(
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    cache_affinity_key: &str,
) -> u64 {
    let mut bytes = HashInputBuilder::new();
    append_cache_affinity_hash_prefix(&mut bytes, config, request, cache_affinity_key);
    xxh3_64(bytes.as_slice())
}

pub(super) fn cache_affinity_virtual_node_hash(
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    candidate: &RoutedClusterSnapshot,
    virtual_node: usize,
) -> u64 {
    let mut bytes = HashInputBuilder::new();
    append_cache_affinity_ring_prefix(&mut bytes, config, request);
    append_tagged_bytes(&mut bytes, b"cluster_id", candidate.cluster_id.as_bytes());
    append_tagged_bytes(&mut bytes, b"virtual_node", &virtual_node.to_le_bytes());
    xxh3_64(bytes.as_slice())
}

fn append_cache_affinity_ring_prefix(
    bytes: &mut HashInputBuilder,
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
) {
    bytes.push(GROQ_MULTIREGION_CACHE_AFFINITY_HASH_VERSION);
    append_tagged_bytes(bytes, b"seed", config.seed().unwrap_or("").as_bytes());
    append_tagged_bytes(
        bytes,
        b"routing_key",
        request
            .routing_target
            .routing_key
            .as_deref()
            .unwrap_or("")
            .as_bytes(),
    );
    append_tagged_bytes(
        bytes,
        b"model_id",
        request.routing_target.model_id.as_bytes(),
    );
}

fn append_cache_affinity_hash_prefix(
    bytes: &mut HashInputBuilder,
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    cache_affinity_key: &str,
) {
    append_cache_affinity_ring_prefix(bytes, config, request);
    append_tagged_bytes(bytes, b"cache_affinity_key", cache_affinity_key.as_bytes());
}

fn append_tagged_bytes(bytes: &mut HashInputBuilder, tag: &[u8], value: &[u8]) {
    bytes.append_tagged_bytes(tag, value);
}

fn compare_least_queue_time(
    candidate_a: &RoutedClusterSnapshot,
    estimate_a: &TtftEstimate,
    candidate_b: &RoutedClusterSnapshot,
    estimate_b: &TtftEstimate,
) -> Ordering {
    match estimate_a.queue_ms.total_cmp(&estimate_b.queue_ms) {
        Ordering::Equal => compare_least_percent_used(candidate_a, candidate_b),
        other => other,
    }
}

fn compare_least_percent_used(
    candidate_a: &RoutedClusterSnapshot,
    candidate_b: &RoutedClusterSnapshot,
) -> Ordering {
    let max_engine_concurrency_a = candidate_a.stats.max_engine_concurrency;
    let max_engine_concurrency_b = candidate_b.stats.max_engine_concurrency;
    if max_engine_concurrency_a == 0 || max_engine_concurrency_b == 0 {
        return candidate_a
            .stats
            .num_running_queries
            .cmp(&candidate_b.stats.num_running_queries);
    }

    let pct_a = candidate_a.stats.num_running_queries as f64 / max_engine_concurrency_a as f64;
    let pct_b = candidate_b.stats.num_running_queries as f64 / max_engine_concurrency_b as f64;
    pct_a.total_cmp(&pct_b)
}

fn has_capacity(candidate: &RoutedClusterSnapshot, max_queued: u64) -> bool {
    if candidate.stats.max_engine_concurrency == 0 {
        return true;
    }
    candidate.stats.num_running_queries < candidate.stats.max_engine_concurrency + max_queued
}

fn within_queue_slo(estimate: &TtftEstimate, max_queue_time_ms: Option<f64>) -> bool {
    match max_queue_time_ms {
        Some(max_queue_time_ms) => estimate.queue_ms <= max_queue_time_ms,
        None => true,
    }
}

fn shuffle_prefix<T>(items: &mut [T], count: usize) {
    let mut rng = rand::rng();
    for index in 0..count {
        let swap_index = rng.random_range(index..items.len());
        items.swap(index, swap_index);
    }
}

fn estimate_ttft_ms(
    candidate: &RoutedClusterSnapshot,
    input_tokens: Option<u64>,
    priority: u32,
    ignore_queue_time: bool,
    ignore_input_processing_time: bool,
) -> TtftEstimate {
    let input_tokens = input_tokens.unwrap_or(0) as f64;
    let effective_input_tps = effective_input_tps(candidate);
    let queue_ms = estimate_queue_delay_ms(candidate, priority, effective_input_tps);
    let prefill_ms = estimate_processing_delay_ms(input_tokens, effective_input_tps);
    let rtt_ms = rtt_ms(candidate);
    let ttft_ms = rtt_ms
        + if ignore_queue_time { 0.0 } else { queue_ms }
        + if ignore_input_processing_time {
            0.0
        } else {
            prefill_ms
        };

    TtftEstimate { queue_ms, ttft_ms }
}

fn estimate_queue_comparison(candidate: &RoutedClusterSnapshot, priority: u32) -> TtftEstimate {
    let effective_input_tps = effective_input_tps(candidate);
    TtftEstimate {
        queue_ms: estimate_queue_delay_ms(candidate, priority, effective_input_tps),
        ttft_ms: rtt_ms(candidate),
    }
}

fn queue_ignored_ttft_ms(candidate: &RoutedClusterSnapshot, input_tokens: f64) -> f64 {
    rtt_ms(candidate) + estimate_processing_delay_ms(input_tokens, effective_input_tps(candidate))
}

fn rtt_ms(candidate: &RoutedClusterSnapshot) -> f64 {
    candidate.rtt.as_secs_f64() * 1000.0
}

fn estimate_queue_delay_ms(
    candidate: &RoutedClusterSnapshot,
    priority: u32,
    effective_input_tps: f64,
) -> f64 {
    if let Some(queue_time_ms) =
        crate::queue_estimate::queue_time_estimate_ms_for_priority(&candidate.stats, priority)
    {
        return queue_time_ms as f64;
    }

    estimate_processing_delay_ms(super::input_work_units(candidate), effective_input_tps)
}

fn effective_input_tps(candidate: &RoutedClusterSnapshot) -> f64 {
    if candidate.stats.last_mean_input_tps > 0.0 && candidate.stats.last_mean_input_tps.is_finite()
    {
        candidate.stats.last_mean_input_tps
    } else {
        0.0
    }
}

fn estimate_processing_delay_ms(work_units: f64, rate: f64) -> f64 {
    if work_units == 0.0 {
        return 0.0;
    }
    if rate <= 0.0 {
        return f64::INFINITY;
    }

    (work_units / rate) * 1000.0
}

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

use std::time::Duration;

use super::pulsar::PulsarLoadBalancer;
use super::wait_and_widen::{WaitAndWidenConfig, WaitAndWidenLoadBalancer};
use super::{
    LoadBalancer, LoadBalancerAlgorithmConfig, LoadBalancerCandidateChoice, LoadBalancerDecision,
    LoadBalancerRequest,
};
use crate::routing_state::RoutedClusterSnapshot;

/// WaitAndWiden with Pulsar ranking as the source of affinity.
///
/// The affinity group is the top of the capacity-weighted rendezvous ranking
/// instead of a consistent-hash ring. Selection, the affinity wait, and bucket
/// unlocks follow WaitAndWiden. After the affinity wait, the eligible set grows
/// one exponentially wider ranking band per widen interval until it covers
/// every candidate, so overflow spreads outward from a key's stable ranking
/// instead of jumping to the whole pool at once.
pub(super) struct PulsarWaitAndWidenLoadBalancer {
    ranking: PulsarLoadBalancer,
    wait_and_widen: WaitAndWidenLoadBalancer,
    /// Same selection with `fallback_max_queued` as the capacity limit.
    fallback_wait_and_widen: WaitAndWidenLoadBalancer,
    affinity_group_size: usize,
    affinity_wait: Duration,
    band_widen_interval: Duration,
    affinity_input_tokens_scale: f64,
}

/// Affinity selection discounts prefill and uses `max_queued`; fallback uses
/// full prefill cost and `fallback_max_queued`.
#[derive(Clone, Copy)]
enum SelectionPhase {
    Affinity,
    Fallback,
}

impl PulsarWaitAndWidenLoadBalancer {
    pub(super) fn new(config: LoadBalancerAlgorithmConfig) -> anyhow::Result<Self> {
        let wait_and_widen_config = WaitAndWidenConfig::from_algorithm_config(&config)?;
        let mut fallback_config = wait_and_widen_config.clone();
        fallback_config.max_queued = wait_and_widen_config.fallback_max_queued;
        Ok(Self {
            // The Pulsar primary is the affinity group unless configured wider.
            affinity_group_size: wait_and_widen_config
                .cache_affinity_backend_selection_count
                .unwrap_or(1),
            affinity_wait: wait_and_widen_config.cache_affinity_wait,
            band_widen_interval: wait_and_widen_config.band_widen_interval,
            affinity_input_tokens_scale: wait_and_widen_config.cache_affinity_input_tokens_scale,
            fallback_wait_and_widen: WaitAndWidenLoadBalancer::new(fallback_config),
            wait_and_widen: WaitAndWidenLoadBalancer::new(wait_and_widen_config),
            ranking: PulsarLoadBalancer::new(config),
        })
    }

    /// Runs WaitAndWiden selection over the first `open_end` ranked candidates.
    fn decide_from_ranking_prefix(
        &self,
        phase: SelectionPhase,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        ranked_indices: &[usize],
        open_end: usize,
        elapsed: Duration,
    ) -> LoadBalancerDecision {
        let (selector, input_tokens_scale) = match phase {
            SelectionPhase::Affinity => (&self.wait_and_widen, self.affinity_input_tokens_scale),
            SelectionPhase::Fallback => (&self.fallback_wait_and_widen, 1.0),
        };
        let eligible = ranked_indices[..open_end]
            .iter()
            .copied()
            .filter(|index| {
                self.ranking
                    .feasibility(request, &candidates[*index])
                    .is_eligible()
            })
            .collect::<Vec<_>>();
        match selector.decide_from_candidate_indices(
            request,
            candidates,
            &eligible,
            input_tokens_scale,
            elapsed,
        ) {
            LoadBalancerDecision::Selected(choice) => LoadBalancerDecision::Selected(
                self.ranked_choice(request, candidates, ranked_indices, choice.candidate_index),
            ),
            decision => decision,
        }
    }

    fn ranked_choice(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        ranked_indices: &[usize],
        candidate_index: usize,
    ) -> LoadBalancerCandidateChoice {
        let rank_depth = ranked_indices
            .iter()
            .position(|index| *index == candidate_index)
            .expect("wait_and_widen choice must come from the PULSAR ranking")
            + 1;
        LoadBalancerCandidateChoice {
            candidate_index,
            rank_depth,
            selected_after_kv_free_tokens_skip: ranked_indices[..rank_depth - 1].iter().any(
                |index| {
                    self.ranking
                        .feasibility(request, &candidates[*index])
                        .skipped_for_kv_free_tokens()
                },
            ),
        }
    }

    fn decide_at(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        elapsed: Duration,
    ) -> LoadBalancerDecision {
        let ranked_indices = self.ranking.compute_ranking(request, candidates);
        let Some(&primary_index) = ranked_indices.first() else {
            return LoadBalancerDecision::Unavailable;
        };
        if !self.wait_and_widen.has_queue_slo(request)
            && self
                .ranking
                .feasibility(request, &candidates[primary_index])
                .is_eligible()
        {
            return LoadBalancerDecision::Selected(LoadBalancerCandidateChoice {
                candidate_index: primary_index,
                rank_depth: 1,
                selected_after_kv_free_tokens_skip: false,
            });
        }

        // Every attempt checks the affinity group first, even after the wait.
        let group_end = self.affinity_group_size.min(ranked_indices.len());
        let affinity_bucket_wait = match self.decide_from_ranking_prefix(
            SelectionPhase::Affinity,
            request,
            candidates,
            &ranked_indices,
            group_end,
            elapsed,
        ) {
            LoadBalancerDecision::Selected(choice) => {
                return LoadBalancerDecision::Selected(choice);
            }
            LoadBalancerDecision::Wait(delay) => Some(delay),
            LoadBalancerDecision::Unavailable => None,
        };
        if elapsed < self.affinity_wait {
            let remaining = self.affinity_wait - elapsed;
            return LoadBalancerDecision::Wait(
                affinity_bucket_wait.map_or(remaining, |delay| delay.min(remaining)),
            );
        }

        // After X, the open set grows by one ranking band per widen interval:
        // ranks 1..=k+2, then k+6, and so on until it covers every candidate.
        // Open candidates compete at full prefill cost, as WaitAndWiden
        // global buckets do, and bucket unlocks count from X.
        let widened_for = elapsed - self.affinity_wait;
        let bands_open = if self.band_widen_interval.is_zero() {
            usize::MAX
        } else {
            usize::try_from(widened_for.as_nanos() / self.band_widen_interval.as_nanos() + 1)
                .unwrap_or(usize::MAX)
        };
        let mut open_end = group_end;
        let mut band_width = 2usize;
        for _ in 0..bands_open {
            if open_end >= ranked_indices.len() {
                break;
            }
            open_end = open_end
                .saturating_add(band_width)
                .min(ranked_indices.len());
            band_width = band_width.saturating_mul(2);
        }
        let next_band_wait = (open_end < ranked_indices.len()).then(|| {
            let next_opening = self
                .band_widen_interval
                .saturating_mul(u32::try_from(bands_open).unwrap_or(u32::MAX));
            next_opening.saturating_sub(widened_for)
        });
        let open_decision = self.decide_from_ranking_prefix(
            SelectionPhase::Fallback,
            request,
            candidates,
            &ranked_indices,
            open_end,
            widened_for,
        );
        let open_wait = match open_decision {
            LoadBalancerDecision::Selected(choice) => {
                return LoadBalancerDecision::Selected(choice);
            }
            LoadBalancerDecision::Wait(delay) => Some(delay),
            LoadBalancerDecision::Unavailable => None,
        };
        [affinity_bucket_wait, open_wait, next_band_wait]
            .into_iter()
            .flatten()
            .min()
            .map_or(
                LoadBalancerDecision::Unavailable,
                LoadBalancerDecision::Wait,
            )
    }
}

impl_display!(PulsarWaitAndWidenLoadBalancer, "pulsar-wait-and-widen");

impl LoadBalancer for PulsarWaitAndWidenLoadBalancer {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        self.decide(request, candidates).selected()
    }

    fn decide(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> LoadBalancerDecision {
        if candidates.is_empty() {
            return LoadBalancerDecision::Unavailable;
        }
        self.decide_at(request, candidates, request.received_at.elapsed())
    }
}

#[cfg(test)]
mod tests {
    use std::collections::{HashMap, HashSet};
    use std::time::{Duration, Instant};

    use stargate_proto::pb::{InferenceServerStatus, ModelStats};

    use super::super::pulsar::PulsarLoadBalancer;
    use super::super::tests::LoadBalancerTestChoiceExt;
    use super::super::{
        LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig, LoadBalancerRequest,
        WaitAndWidenAlgorithmConfig,
    };
    use super::*;
    use crate::routing_state::{RoutedClusterSnapshot, RoutingTargetKey};

    fn target() -> RoutingTargetKey {
        RoutingTargetKey::new(Some("rk-1".to_string()), "model-a")
    }

    fn request<'a>(
        target: &'a RoutingTargetKey,
        cache_affinity_key: Option<&'a str>,
        input_tokens: Option<u64>,
    ) -> LoadBalancerRequest<'a> {
        LoadBalancerRequest {
            routing_target: target,
            cache_affinity_key,
            input_tokens,
            priority: 0,
            received_at: Instant::now(),
            request_slo: None,
            excluded_cluster_ids: None,
        }
    }

    fn pulsar_algorithm_config(seed: &str) -> LoadBalancerAlgorithmConfig {
        let mut config = LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::Pulsar);
        config
            .set_seed(Some(seed.to_string()))
            .expect("pulsar-wait-and-widen supports deterministic seeding");
        config.request_policy_mut().require_cache_affinity_key = true;
        config.request_policy_mut().require_input_tokens = true;
        config
    }

    fn pulsar_wait_and_widen_algorithm_config(
        seed: &str,
        consider_kv_free_tokens: bool,
        configure: impl FnOnce(&mut WaitAndWidenAlgorithmConfig),
    ) -> LoadBalancerAlgorithmConfig {
        let mut config =
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PulsarWaitAndWiden);
        config
            .set_seed(Some(seed.to_string()))
            .expect("pulsar-wait-and-widen supports deterministic seeding");
        config.request_policy_mut().consider_kv_free_tokens = consider_kv_free_tokens;
        configure(
            config
                .wait_and_widen_settings_mut()
                .expect("pulsar-wait-and-widen config should expose wait_and_widen settings"),
        );
        config
    }

    fn candidate(id: &str, kv_cache_free_tokens: u64) -> RoutedClusterSnapshot {
        RoutedClusterSnapshot {
            cluster_id: id.to_string(),
            stats: ModelStats {
                output_tps: 0.0,
                last_mean_input_tps: 100.0,
                max_input_tps: Some(100.0),
                max_output_tps: 100.0,
                queue_size: 0,
                queued_input_size: 0,
                kv_cache_capacity_tokens: 1024,
                kv_cache_used_tokens: 1024 - kv_cache_free_tokens,
                kv_cache_free_tokens,
                num_running_queries: 0,
                max_engine_concurrency: 0,
                total_query_input_size: 0,
                queue_time_estimate_ms_by_priority: HashMap::new(),
                ..ModelStats::default()
            },
            rtt: Duration::from_millis(5),
            snapshot_updated_at: Instant::now(),
            status: InferenceServerStatus::Active,
            active_backend_count: 1,
        }
    }

    fn candidates(count: usize, kv_cache_free_tokens: u64) -> Vec<RoutedClusterSnapshot> {
        (0..count)
            .map(|index| candidate(&format!("cluster-{index}"), kv_cache_free_tokens))
            .collect()
    }

    fn pulsar_ranked_indices(
        seed: &str,
        target: &RoutingTargetKey,
        cache_affinity_key: &str,
        input_tokens: u64,
        candidates: &[RoutedClusterSnapshot],
    ) -> Vec<usize> {
        let ordinary = PulsarLoadBalancer::new(pulsar_algorithm_config(seed));
        let mut ranked_indices = Vec::new();
        let mut excluded = HashSet::new();
        for _ in 0..candidates.len() {
            let ranked_request = LoadBalancerRequest {
                excluded_cluster_ids: (!excluded.is_empty()).then_some(&excluded),
                ..request(target, Some(cache_affinity_key), Some(input_tokens))
            };
            let selected = ordinary
                .choose_for_test(&ranked_request, candidates)
                .expect("ordinary PULSAR ranking should include every candidate");
            let selected_index = candidates
                .iter()
                .position(|candidate| candidate.cluster_id == selected.candidate.cluster_id)
                .expect("selected candidate should come from input slice");
            ranked_indices.push(selected_index);
            excluded.insert(selected.candidate.cluster_id);
        }
        ranked_indices
    }

    #[test]
    fn without_queue_slo_keeps_full_primary() {
        let target = target();
        let affinity_key = "hybrid-prefix-without-slo";
        let mut candidates = candidates(3, 0);
        for candidate in &mut candidates {
            candidate.stats.max_engine_concurrency = 1;
        }
        let hybrid_request = request(&target, Some(affinity_key), Some(0));
        let primary_index =
            pulsar_ranked_indices("hybrid-seed", &target, affinity_key, 0, &candidates)[0];
        candidates[primary_index].stats.num_running_queries = 1;

        let hybrid = PulsarWaitAndWidenLoadBalancer::new(pulsar_wait_and_widen_algorithm_config(
            "hybrid-seed",
            false,
            |settings| {
                settings.n = Some(2);
            },
        ))
        .expect("valid hybrid config");

        let chosen = hybrid
            .choose_for_test(&hybrid_request, &candidates)
            .expect("no-SLO primary should remain selectable despite live capacity");
        assert_eq!(
            chosen.candidate.cluster_id,
            candidates[primary_index].cluster_id
        );
        assert_eq!(chosen.rank_depth, 1);
    }

    #[test]
    fn kv_skip_uses_ranked_fallback_band_and_composes_with_slo() {
        let target = target();
        let request = request(&target, Some("hybrid-kv-prefix"), Some(100));
        let mut candidates = candidates(4, 1024);
        for candidate in &mut candidates {
            candidate.rtt = Duration::from_millis(50);
        }
        let ranked_indices = pulsar_ranked_indices(
            "hybrid-kv-seed",
            &target,
            "hybrid-kv-prefix",
            100,
            &candidates,
        );
        let primary_index = ranked_indices[0];
        let second_index = ranked_indices[1];
        let third_index = ranked_indices[2];
        candidates[second_index].rtt = Duration::from_millis(1);
        candidates[third_index].rtt = Duration::from_millis(500);
        candidates[primary_index].stats.kv_cache_free_tokens = 50;
        candidates[primary_index].stats.kv_cache_used_tokens = 974;

        let hybrid = PulsarWaitAndWidenLoadBalancer::new(pulsar_wait_and_widen_algorithm_config(
            "hybrid-kv-seed",
            true,
            |settings| {
                settings.n = Some(2);
                settings.band_widen_interval_ms = Some(10_000);
            },
        ))
        .expect("valid hybrid config");
        let first_band_choice = hybrid
            .choose_for_test(&request, &candidates)
            .expect("first ranked fallback band should contain a usable candidate");
        assert_eq!(
            first_band_choice.candidate.cluster_id,
            candidates[second_index].cluster_id
        );
        assert_eq!(first_band_choice.rank_depth, 2);
        assert!(first_band_choice.selected_after_kv_free_tokens_skip);

        candidates[second_index].stats.kv_cache_free_tokens = 50;
        candidates[second_index].stats.kv_cache_used_tokens = 974;
        let filtered_band_choice = hybrid
            .choose_for_test(&request, &candidates)
            .expect("KV filtering should retain the next candidate in the first band");
        assert_eq!(
            filtered_band_choice.candidate.cluster_id,
            candidates[third_index].cluster_id
        );
        assert_eq!(filtered_band_choice.rank_depth, 3);
        assert!(filtered_band_choice.selected_after_kv_free_tokens_skip);

        candidates[primary_index].stats.kv_cache_free_tokens = 1024;
        candidates[primary_index].stats.kv_cache_used_tokens = 0;
        candidates[primary_index].stats.queued_input_size = 50;
        let hybrid_with_slo = PulsarWaitAndWidenLoadBalancer::new(
            pulsar_wait_and_widen_algorithm_config("hybrid-kv-seed", true, |settings| {
                settings.max_queue_time_floor_ms = Some(100);
                settings.max_queue_time_ceil_ms = Some(100);
                settings.ttft_bucket_size_ms = Some(100);
                settings.n = Some(2);
                settings.band_widen_interval_ms = Some(10_000);
            }),
        )
        .expect("valid hybrid config");
        let composed_choice = hybrid_with_slo
            .choose_for_test(&request, &candidates)
            .expect("the remaining eligible fallback should serve SLO rejection");
        assert_eq!(
            composed_choice.candidate.cluster_id,
            candidates[third_index].cluster_id
        );
        assert_eq!(composed_choice.rank_depth, 3);
        assert!(composed_choice.selected_after_kv_free_tokens_skip);
    }

    #[test]
    fn keeps_primary_until_queue_slo_requires_first_band_fallback() {
        let target = target();
        let affinity_key = "hybrid-prefix";
        let mut candidates = candidates(5, 0);
        for candidate in &mut candidates {
            candidate.rtt = Duration::from_millis(100);
        }
        let ranked_indices =
            pulsar_ranked_indices("hybrid-seed", &target, affinity_key, 0, &candidates);
        let ranked_ids = ranked_indices
            .iter()
            .map(|index| candidates[*index].cluster_id.clone())
            .collect::<Vec<_>>();

        let primary = ranked_ids[0].clone();
        let first_fallback = ranked_ids[1].clone();
        let second_fallback = ranked_ids[2].clone();
        let globally_fast_lower_rank = ranked_ids[3].clone();
        for candidate in &mut candidates {
            if candidate.cluster_id == primary {
                candidate.rtt = Duration::from_millis(500);
                candidate.stats.queued_input_size = 5;
            } else if candidate.cluster_id == first_fallback {
                candidate.stats.queued_input_size = 0;
            } else if candidate.cluster_id == second_fallback {
                candidate.stats.queued_input_size = 1;
            } else if candidate.cluster_id == globally_fast_lower_rank {
                candidate.rtt = Duration::from_millis(1);
                candidate.stats.queued_input_size = 0;
            }
        }

        let hybrid = PulsarWaitAndWidenLoadBalancer::new(pulsar_wait_and_widen_algorithm_config(
            "hybrid-seed",
            false,
            |settings| {
                settings.max_queue_time_floor_ms = Some(100);
                settings.max_queue_time_ceil_ms = Some(100);
                settings.ttft_bucket_size_ms = Some(100);
                settings.n = Some(2);
                settings.band_widen_interval_ms = Some(1_000);
            },
        ))
        .expect("valid hybrid config");
        let hybrid_request = request(&target, Some(affinity_key), Some(0));

        let warm_choice = hybrid
            .choose_for_test(&hybrid_request, &candidates)
            .expect("primary below queue SLO should remain selected");
        assert_eq!(warm_choice.candidate.cluster_id, primary);
        assert_eq!(warm_choice.rank_depth, 1);

        let primary_candidate = candidates
            .iter_mut()
            .find(|candidate| candidate.cluster_id == primary)
            .expect("primary candidate should exist");
        primary_candidate.stats.queued_input_size = 20;

        let fallback_choice = hybrid
            .choose_for_test(&hybrid_request, &candidates)
            .expect("first fallback band should contain usable candidates");
        assert_eq!(fallback_choice.candidate.cluster_id, first_fallback);
        assert_eq!(fallback_choice.rank_depth, 2);
        assert_ne!(
            fallback_choice.candidate.cluster_id,
            globally_fast_lower_rank
        );

        for candidate in &mut candidates {
            if candidate.cluster_id == first_fallback || candidate.cluster_id == second_fallback {
                candidate.stats.queued_input_size = 20;
            } else if candidate.cluster_id == ranked_ids[4] {
                candidate.stats.queued_input_size = 1;
            }
        }

        // Ranks 2 and 3 now miss the queue SLO. The next band opens only after
        // one widen interval.
        let first_band_only = hybrid.decide_at(&hybrid_request, &candidates, Duration::ZERO);
        assert!(
            matches!(first_band_only, LoadBalancerDecision::Wait(delay) if delay <= Duration::from_secs(1)),
            "expected a wait for the next band, got {first_band_only:?}"
        );
        let expanded_choice =
            hybrid.decide_at(&hybrid_request, &candidates, Duration::from_millis(1_000));
        match expanded_choice {
            LoadBalancerDecision::Selected(choice) => {
                assert_eq!(
                    candidates[choice.candidate_index].cluster_id,
                    globally_fast_lower_rank
                );
                assert_eq!(choice.rank_depth, 4);
            }
            other => panic!("second band should contain usable candidates, got {other:?}"),
        }
    }

    fn single_slot_candidates(count: usize) -> Vec<RoutedClusterSnapshot> {
        let mut candidates = candidates(count, 0);
        for candidate in &mut candidates {
            candidate.stats.max_engine_concurrency = 1;
        }
        candidates
    }

    fn affinity_wait_config(
        seed: &str,
        configure: impl FnOnce(&mut WaitAndWidenAlgorithmConfig),
    ) -> PulsarWaitAndWidenLoadBalancer {
        PulsarWaitAndWidenLoadBalancer::new(pulsar_wait_and_widen_algorithm_config(
            seed,
            false,
            |settings| {
                settings.max_queue_time_floor_ms = Some(10_000);
                settings.max_queue_time_ceil_ms = Some(10_000);
                settings.n = Some(2);
                configure(settings);
            },
        ))
        .expect("valid hybrid config")
    }

    fn selected_rank(decision: LoadBalancerDecision) -> usize {
        match decision {
            LoadBalancerDecision::Selected(choice) => choice.rank_depth,
            other => panic!("expected a selection, got {other:?}"),
        }
    }

    #[test]
    fn affinity_wait_holds_full_primary_before_widening() {
        let target = target();
        let affinity_key = "hybrid-affinity-wait";
        let mut candidates = single_slot_candidates(5);
        let ranked = pulsar_ranked_indices("wait-seed", &target, affinity_key, 0, &candidates);
        candidates[ranked[0]].stats.num_running_queries = 1;
        let hybrid = affinity_wait_config("wait-seed", |settings| {
            settings.cache_affinity_wait_ms = Some(300);
        });
        let request = request(&target, Some(affinity_key), Some(0));

        assert_eq!(
            hybrid.decide_at(&request, &candidates, Duration::from_millis(100)),
            LoadBalancerDecision::Wait(Duration::from_millis(200))
        );
        let rank =
            selected_rank(hybrid.decide_at(&request, &candidates, Duration::from_millis(300)));
        assert!(
            (2..=3).contains(&rank),
            "first fallback band, got rank {rank}"
        );

        candidates[ranked[0]].stats.num_running_queries = 0;
        assert_eq!(
            selected_rank(hybrid.decide_at(&request, &candidates, Duration::from_millis(100))),
            1
        );
    }

    #[test]
    fn affinity_group_size_widens_the_group_before_fallback() {
        let target = target();
        let affinity_key = "hybrid-affinity-group";
        let mut candidates = single_slot_candidates(5);
        let ranked = pulsar_ranked_indices("group-seed", &target, affinity_key, 0, &candidates);
        candidates[ranked[0]].stats.num_running_queries = 1;
        let hybrid = affinity_wait_config("group-seed", |settings| {
            settings.cache_affinity_wait_ms = Some(300);
            settings.cache_affinity_backend_selection_count = Some(2);
        });
        let request = request(&target, Some(affinity_key), Some(0));

        assert_eq!(
            selected_rank(hybrid.decide_at(&request, &candidates, Duration::ZERO)),
            2
        );
    }

    #[test]
    fn locked_fallback_bucket_returns_timed_wait() {
        let target = target();
        let affinity_key = "hybrid-locked-bucket";
        let mut candidates = single_slot_candidates(5);
        let ranked = pulsar_ranked_indices("bucket-seed", &target, affinity_key, 0, &candidates);
        for rank in [0, 1, 3, 4] {
            candidates[ranked[rank]].stats.num_running_queries = 1;
        }
        // Rank 3 has capacity but sits 495 ms behind rank 2's TTFT bucket.
        candidates[ranked[2]].rtt = Duration::from_millis(500);
        let hybrid = affinity_wait_config("bucket-seed", |settings| {
            settings.ttft_bucket_size_ms = Some(20);
            settings.next_bucket_unlock_factor = Some(0.25);
        });
        let request = request(&target, Some(affinity_key), Some(0));

        match hybrid.decide_at(&request, &candidates, Duration::ZERO) {
            LoadBalancerDecision::Wait(delay) => {
                assert!(
                    delay > Duration::from_millis(100) && delay < Duration::from_millis(150),
                    "unexpected bucket wait {delay:?}"
                );
            }
            other => panic!("expected a timed wait, got {other:?}"),
        }
        assert_eq!(
            selected_rank(hybrid.decide_at(&request, &candidates, Duration::from_millis(200))),
            3
        );
    }

    #[test]
    fn widen_interval_opens_bands_progressively() {
        let target = target();
        let affinity_key = "hybrid-progressive";
        let mut candidates = single_slot_candidates(8);
        let ranked = pulsar_ranked_indices("widen-seed", &target, affinity_key, 0, &candidates);
        // Only rank 8 has capacity: it opens with the third band (ranks 1-15).
        for rank in 0..7 {
            candidates[ranked[rank]].stats.num_running_queries = 1;
        }
        let request = request(&target, Some(affinity_key), Some(0));
        let gated = affinity_wait_config("widen-seed", |settings| {
            settings.cache_affinity_wait_ms = Some(100);
            settings.band_widen_interval_ms = Some(200);
        });

        assert_eq!(
            gated.decide_at(&request, &candidates, Duration::from_millis(100)),
            LoadBalancerDecision::Wait(Duration::from_millis(200))
        );
        assert_eq!(
            gated.decide_at(&request, &candidates, Duration::from_millis(350)),
            LoadBalancerDecision::Wait(Duration::from_millis(150))
        );
        assert_eq!(
            selected_rank(gated.decide_at(&request, &candidates, Duration::from_millis(500))),
            8
        );

        let immediate = affinity_wait_config("widen-seed", |settings| {
            settings.cache_affinity_wait_ms = Some(100);
            settings.band_widen_interval_ms = Some(0);
        });
        assert_eq!(
            selected_rank(immediate.decide_at(&request, &candidates, Duration::from_millis(100))),
            8
        );
    }

    #[test]
    fn fallback_max_queued_defaults_to_free_slot_overflow() {
        let target = target();
        let affinity_key = "hybrid-fallback-queue";
        let mut candidates = single_slot_candidates(5);
        let ranked = pulsar_ranked_indices("guard-seed", &target, affinity_key, 0, &candidates);
        // The primary is full including its queue. Ranks 2 and 3 have no free
        // slot but still have one queued place each.
        candidates[ranked[0]].stats.num_running_queries = 2;
        candidates[ranked[1]].stats.num_running_queries = 1;
        candidates[ranked[2]].stats.num_running_queries = 1;
        let request = request(&target, Some(affinity_key), Some(0));
        let configure = |fallback_max_queued| {
            affinity_wait_config("guard-seed", move |settings| {
                settings.max_queued = Some(1);
                settings.fallback_max_queued = fallback_max_queued;
                settings.band_widen_interval_ms = Some(1_000);
            })
        };

        let queued =
            selected_rank(configure(Some(1)).decide_at(&request, &candidates, Duration::ZERO));
        assert!((2..=3).contains(&queued), "queued overflow rank {queued}");
        // The default guards overflow.
        let guarded = configure(None);
        assert_eq!(
            guarded.decide_at(&request, &candidates, Duration::ZERO),
            LoadBalancerDecision::Wait(Duration::from_secs(1))
        );
        let widened =
            selected_rank(guarded.decide_at(&request, &candidates, Duration::from_secs(1)));
        assert!(
            (4..=5).contains(&widened),
            "free-slot overflow rank {widened}"
        );
    }
}

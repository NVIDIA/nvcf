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

use std::borrow::Cow;
use std::time::Duration;

use super::pulsar::PulsarLoadBalancer;
use super::session::{self, MissingLastCluster};
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
    /// Same selection with `fallback_max_queued` as the capacity limit, used
    /// for the open set after the affinity wait.
    fallback_wait_and_widen: WaitAndWidenLoadBalancer,
    affinity_group_size: usize,
    affinity_wait: Duration,
    /// Explicit `band_widen_interval_ms`. Unset follows the request's
    /// effective affinity wait.
    band_widen_interval: Option<Duration>,
    affinity_input_tokens_scale: f64,
    last_cluster_affinity: bool,
}

/// Affinity selection discounts prefill and uses `max_queued`; fallback uses
/// full prefill cost and `fallback_max_queued`.
#[derive(Clone, Copy)]
enum SelectionPhase {
    Affinity { input_tokens_scale: f64 },
    Fallback,
}

impl PulsarWaitAndWidenLoadBalancer {
    pub(super) fn new(config: LoadBalancerAlgorithmConfig) -> anyhow::Result<Self> {
        let settings = config
            .wait_and_widen_settings()
            .expect("pulsar-wait-and-widen config has wait_and_widen settings");
        let wait_and_widen_config = WaitAndWidenConfig::from_algorithm_config(&config)?;
        let band_widen_interval = settings.band_widen_interval_ms.map(Duration::from_millis);
        let mut fallback_config = wait_and_widen_config.clone();
        fallback_config.max_queued = settings.fallback_max_queued.unwrap_or(0);
        Ok(Self {
            // The Pulsar primary is the affinity group unless configured wider.
            affinity_group_size: wait_and_widen_config
                .cache_affinity_backend_selection_count
                .unwrap_or(1),
            affinity_wait: wait_and_widen_config.cache_affinity_wait,
            band_widen_interval,
            affinity_input_tokens_scale: wait_and_widen_config.cache_affinity_input_tokens_scale,
            last_cluster_affinity: wait_and_widen_config.last_cluster_affinity,
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
            SelectionPhase::Affinity { input_tokens_scale } => {
                (&self.wait_and_widen, input_tokens_scale)
            }
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
        let ranking = self.ranking.compute_ranking(request, candidates);
        if ranking.is_empty() {
            return LoadBalancerDecision::Unavailable;
        }
        // Session rules reorder a per-request copy of the ranking. Rank 1 has
        // no shortcut: it goes through queue admission, the affinity wait, and
        // the affinity discount like every other affinity member.
        let plan = session::plan_affinity(
            self.last_cluster_affinity,
            request,
            candidates,
            Cow::Owned(ranking),
            MissingLastCluster::Keep,
            self.affinity_wait,
            self.affinity_input_tokens_scale,
        );
        let ranked_indices = &*plan.order;
        let affinity_wait = plan.affinity_wait;
        let band_widen_interval = self.band_widen_interval.unwrap_or(affinity_wait);

        // Every attempt checks the affinity group first, even after the wait.
        let group_end = self.affinity_group_size.min(ranked_indices.len());
        let affinity_bucket_wait = match self.decide_from_ranking_prefix(
            SelectionPhase::Affinity {
                input_tokens_scale: plan.input_tokens_scale,
            },
            request,
            candidates,
            ranked_indices,
            group_end,
            elapsed,
        ) {
            LoadBalancerDecision::Selected(choice) => {
                return LoadBalancerDecision::Selected(choice);
            }
            LoadBalancerDecision::Wait(delay) => Some(delay),
            LoadBalancerDecision::Unavailable => None,
        };
        if elapsed < affinity_wait {
            let remaining = affinity_wait - elapsed;
            return LoadBalancerDecision::Wait(
                affinity_bucket_wait.map_or(remaining, |delay| delay.min(remaining)),
            );
        }

        // After the affinity wait, the open set grows by one ranking band per
        // widen interval: ranks 1..=k+2, then 1..=k+6, and so on until it
        // covers the whole ranking. Open candidates compete at full prefill
        // cost, as WaitAndWiden global buckets do, and bucket unlocks count
        // from the end of the affinity wait.
        let widened_for = elapsed - affinity_wait;
        let bands_open = if band_widen_interval.is_zero() {
            usize::MAX
        } else {
            usize::try_from(widened_for.as_nanos() / band_widen_interval.as_nanos() + 1)
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
            let next_opening =
                band_widen_interval.saturating_mul(u32::try_from(bands_open).unwrap_or(u32::MAX));
            next_opening.saturating_sub(widened_for)
        });
        let open_decision = self.decide_from_ranking_prefix(
            SelectionPhase::Fallback,
            request,
            candidates,
            ranked_indices,
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
    use std::borrow::Cow;

    use super::super::session::{self, MissingLastCluster};
    use super::super::{
        LastClusterHint, LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig, LoadBalancerRequest,
        SessionClassification, SessionState, WaitAndWidenAlgorithmConfig,
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
            last_cluster: None,
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

    /// No queue-SLO fields, rank 1 at its engine concurrency limit.
    fn no_slo_full_primary(
        seed: &str,
        affinity_key: &str,
        configure: impl FnOnce(&mut WaitAndWidenAlgorithmConfig),
    ) -> (
        PulsarWaitAndWidenLoadBalancer,
        Vec<RoutedClusterSnapshot>,
        Vec<usize>,
    ) {
        let target = target();
        let mut candidates = single_slot_candidates(3);
        let ranked = pulsar_ranked_indices(seed, &target, affinity_key, 0, &candidates);
        candidates[ranked[0]].stats.num_running_queries = 1;
        candidates[ranked[2]].stats.num_running_queries = 1;
        let hybrid = PulsarWaitAndWidenLoadBalancer::new(pulsar_wait_and_widen_algorithm_config(
            seed,
            false,
            |settings| {
                settings.n = Some(2);
                configure(settings);
            },
        ))
        .expect("valid hybrid config");
        (hybrid, candidates, ranked)
    }

    #[test]
    fn without_queue_slo_overflows_full_primary() {
        let target = target();
        let affinity_key = "hybrid-prefix-without-slo";
        let (hybrid, candidates, ranked) = no_slo_full_primary("hybrid-seed", affinity_key, |_| {});
        let request = request(&target, Some(affinity_key), Some(0));

        let choice = hybrid
            .decide_at(&request, &candidates, Duration::ZERO)
            .selected()
            .expect("a free rank-2 cluster should take the overflow");
        assert_eq!(choice.candidate_index, ranked[1]);
        assert_eq!(choice.rank_depth, 2);
    }

    #[test]
    fn without_queue_slo_queues_on_primary_within_max_queued() {
        let target = target();
        let affinity_key = "hybrid-prefix-without-slo";
        let (hybrid, mut candidates, ranked) =
            no_slo_full_primary("hybrid-seed", affinity_key, |settings| {
                settings.max_queued = Some(4);
            });
        // One running query and one queued.
        candidates[ranked[0]].stats.num_running_queries = 2;
        let request = request(&target, Some(affinity_key), Some(0));

        let choice = hybrid
            .decide_at(&request, &candidates, Duration::ZERO)
            .selected()
            .expect("rank 1 should admit the request into its queue");
        assert_eq!(choice.candidate_index, ranked[0]);
        assert_eq!(choice.rank_depth, 1);
    }

    #[test]
    fn without_queue_slo_waits_for_affinity_then_widens() {
        let target = target();
        let affinity_key = "hybrid-prefix-without-slo";
        let (hybrid, candidates, ranked) =
            no_slo_full_primary("hybrid-seed", affinity_key, |settings| {
                settings.cache_affinity_wait_ms = Some(300);
            });
        let request = request(&target, Some(affinity_key), Some(0));

        assert_eq!(
            hybrid.decide_at(&request, &candidates, Duration::ZERO),
            LoadBalancerDecision::Wait(Duration::from_millis(300))
        );
        let choice = hybrid
            .decide_at(&request, &candidates, Duration::from_millis(300))
            .selected()
            .expect("the request should widen after the affinity wait");
        assert_eq!(choice.candidate_index, ranked[1]);
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
        assert_eq!(
            hybrid.decide_at(&hybrid_request, &candidates, Duration::ZERO),
            LoadBalancerDecision::Wait(Duration::from_secs(1))
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
        // Faster lower ranks would win any open-set selection that includes them.
        for rank in [1, 2] {
            candidates[ranked[rank]].rtt = Duration::from_millis(1);
        }
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
        // After the wait, the affinity group is still checked before the open
        // set, so a free primary wins over faster free lower ranks.
        assert_eq!(
            selected_rank(hybrid.decide_at(&request, &candidates, Duration::from_millis(500))),
            1
        );
    }

    #[test]
    fn widen_interval_defaults_to_the_affinity_wait() {
        let target = target();
        let affinity_key = "hybrid-default-interval";
        let mut candidates = single_slot_candidates(5);
        let ranked = pulsar_ranked_indices("interval-seed", &target, affinity_key, 0, &candidates);
        // Only rank 4 is free. It opens with the second band, one interval
        // after the affinity wait.
        for rank in [0, 1, 2, 4] {
            candidates[ranked[rank]].stats.num_running_queries = 1;
        }
        let hybrid = affinity_wait_config("interval-seed", |settings| {
            settings.cache_affinity_wait_ms = Some(300);
        });
        let request = request(&target, Some(affinity_key), Some(0));

        assert_eq!(
            hybrid.decide_at(&request, &candidates, Duration::from_millis(300)),
            LoadBalancerDecision::Wait(Duration::from_millis(300))
        );
        assert_eq!(
            selected_rank(hybrid.decide_at(&request, &candidates, Duration::from_millis(600))),
            4
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
        // Only rank 3 has capacity, and it sits 495 ms behind the full
        // clusters that form the first TTFT bucket.
        candidates[ranked[2]].rtt = Duration::from_millis(500);
        let hybrid = affinity_wait_config("bucket-seed", |settings| {
            settings.band_widen_interval_ms = Some(0);
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

    // Session-aware pulsar-wait-and-widen (`last_cluster_affinity`).

    const SESSION_WAIT: Duration = Duration::from_millis(300);
    const SESSION_KEY: &str = "hybrid-session";

    fn session_hybrid(
        seed: &str,
        last_cluster_affinity: bool,
        configure: impl FnOnce(&mut WaitAndWidenAlgorithmConfig),
    ) -> PulsarWaitAndWidenLoadBalancer {
        PulsarWaitAndWidenLoadBalancer::new(pulsar_wait_and_widen_algorithm_config(
            seed,
            false,
            |settings| {
                settings.n = Some(2);
                settings.cache_affinity_wait_ms = Some(300);
                settings.cache_affinity_input_tokens_scale = Some(0.1);
                settings.last_cluster_affinity = Some(last_cluster_affinity);
                configure(settings);
            },
        ))
        .expect("valid hybrid config")
    }

    fn session_request<'a>(
        target: &'a RoutingTargetKey,
        input_tokens: u64,
        hint: &'a LastClusterHint,
    ) -> LoadBalancerRequest<'a> {
        LoadBalancerRequest {
            last_cluster: Some(hint),
            ..request(target, Some(SESSION_KEY), Some(input_tokens))
        }
    }

    fn last_cluster(candidate: &RoutedClusterSnapshot) -> LastClusterHint {
        LastClusterHint::new(Some(candidate.cluster_id.clone()))
    }

    fn selected_index(decision: LoadBalancerDecision) -> usize {
        match decision {
            LoadBalancerDecision::Selected(choice) => choice.candidate_index,
            other => panic!("expected a selection, got {other:?}"),
        }
    }

    fn session_ranking(seed: &str, candidates: &[RoutedClusterSnapshot]) -> Vec<usize> {
        pulsar_ranked_indices(seed, &target(), SESSION_KEY, 0, candidates)
    }

    #[test]
    fn new_session_overflows_without_waiting() {
        let target = target();
        let mut candidates = single_slot_candidates(3);
        let ranked = session_ranking("new-seed", &candidates);
        candidates[ranked[0]].stats.num_running_queries = 1;
        candidates[ranked[2]].stats.num_running_queries = 1;

        for hint in [
            LastClusterHint::default(),
            LastClusterHint::new(Some("gone".to_string())),
        ] {
            let hybrid = session_hybrid("new-seed", true, |_| {});
            let choice = hybrid
                .decide_at(
                    &session_request(&target, 0, &hint),
                    &candidates,
                    Duration::ZERO,
                )
                .selected()
                .expect("a new session should overflow at once");
            assert_eq!(choice.candidate_index, ranked[1]);
        }

        let flag_off = session_hybrid("new-seed", false, |_| {});
        let hint = LastClusterHint::default();
        assert_eq!(
            flag_off.decide_at(
                &session_request(&target, 0, &hint),
                &candidates,
                Duration::ZERO
            ),
            LoadBalancerDecision::Wait(SESSION_WAIT)
        );
    }

    #[test]
    fn new_session_matches_flag_off_without_wait_or_discount() {
        let target = target();
        let all_free = single_slot_candidates(5);
        let ranked = session_ranking("match-seed", &all_free);
        let mut all_full = all_free.clone();
        for candidate in &mut all_full {
            candidate.stats.num_running_queries = 1;
        }
        let mut only_second_free = all_full.clone();
        only_second_free[ranked[1]].stats.num_running_queries = 0;
        let mut only_last_free = all_full.clone();
        only_last_free[ranked[4]].stats.num_running_queries = 0;
        let baseline = session_hybrid("match-seed", false, |settings| {
            settings.cache_affinity_wait_ms = Some(0);
            settings.cache_affinity_input_tokens_scale = Some(1.0);
        });

        for candidates in [&all_free, &all_full, &only_second_free, &only_last_free] {
            for elapsed_ms in [0, 50, 300, 1_000] {
                let elapsed = Duration::from_millis(elapsed_ms);
                for hint in [
                    LastClusterHint::default(),
                    LastClusterHint::new(Some("gone".to_string())),
                ] {
                    let hybrid = session_hybrid("match-seed", true, |_| {});
                    let request = session_request(&target, 0, &hint);
                    assert_eq!(
                        hybrid.decide_at(&request, candidates, elapsed),
                        baseline.decide_at(&request, candidates, elapsed),
                        "at {elapsed_ms} ms"
                    );
                }
            }
        }
    }

    #[test]
    fn new_session_uses_full_prefill_in_affinity_group() {
        let target = target();
        let mut candidates = single_slot_candidates(4);
        let ranked = session_ranking("prefill-seed", &candidates);
        // Full prefill favors the faster processor (1500 vs 2001 ms).
        // Discounted prefill favors the closer backend (600 vs 201 ms).
        candidates[ranked[0]].rtt = Duration::from_millis(500);
        candidates[ranked[0]].stats.last_mean_input_tps = 1_000.0;
        candidates[ranked[1]].rtt = Duration::from_millis(1);
        candidates[ranked[1]].stats.last_mean_input_tps = 500.0;
        assert_eq!(session_ranking("prefill-seed", &candidates), ranked);
        let group_of_two = |settings: &mut WaitAndWidenAlgorithmConfig| {
            settings.cache_affinity_backend_selection_count = Some(2);
        };

        let hint = LastClusterHint::default();
        let request = session_request(&target, 1_000, &hint);
        let flag_off = session_hybrid("prefill-seed", false, group_of_two);
        assert_eq!(
            selected_index(flag_off.decide_at(&request, &candidates, Duration::ZERO)),
            ranked[1]
        );
        for hint in [
            LastClusterHint::default(),
            LastClusterHint::new(Some("gone".to_string())),
        ] {
            let hybrid = session_hybrid("prefill-seed", true, group_of_two);
            assert_eq!(
                selected_index(hybrid.decide_at(
                    &session_request(&target, 1_000, &hint),
                    &candidates,
                    Duration::ZERO
                )),
                ranked[0]
            );
        }
    }

    #[test]
    fn returning_session_waits_for_last_cluster() {
        let target = target();
        let mut candidates = single_slot_candidates(3);
        let ranked = session_ranking("returning-seed", &candidates);
        candidates[ranked[1]].stats.num_running_queries = 1;
        let hint = last_cluster(&candidates[ranked[1]]);
        let hybrid = session_hybrid("returning-seed", true, |_| {});
        let request = session_request(&target, 0, &hint);

        assert_eq!(
            hybrid.decide_at(&request, &candidates, Duration::ZERO),
            LoadBalancerDecision::Wait(SESSION_WAIT)
        );
        for elapsed_ms in (0..300).step_by(10) {
            assert!(
                matches!(
                    hybrid.decide_at(&request, &candidates, Duration::from_millis(elapsed_ms)),
                    LoadBalancerDecision::Wait(_)
                ),
                "returning session must wait for its last cluster at {elapsed_ms} ms"
            );
        }
        assert_eq!(
            hint.classification(),
            Some(SessionClassification {
                state: SessionState::Returning,
                promoted: true,
            })
        );

        candidates[ranked[1]].stats.num_running_queries = 0;
        let choice = hybrid
            .decide_at(&request, &candidates, Duration::ZERO)
            .selected()
            .unwrap();
        assert_eq!(choice.candidate_index, ranked[1]);
        assert_eq!(choice.rank_depth, 1);
    }

    #[test]
    fn returning_session_discounts_only_the_last_cluster() {
        let target = target();
        let mut candidates = single_slot_candidates(3);
        let ranked = session_ranking("discount-seed", &candidates);
        let (a, b, c) = (ranked[0], ranked[1], ranked[2]);
        candidates[b].stats.num_running_queries = 1;
        // A wins only when discounted (201 ms vs C at 1500 ms).
        candidates[a].rtt = Duration::from_millis(1);
        candidates[a].stats.last_mean_input_tps = 500.0;
        candidates[c].rtt = Duration::from_millis(500);
        candidates[c].stats.last_mean_input_tps = 1_000.0;
        assert_eq!(session_ranking("discount-seed", &candidates), ranked);
        let hint = last_cluster(&candidates[b]);
        let request = session_request(&target, 1_000, &hint);

        let hybrid = session_hybrid("discount-seed", true, |_| {});
        assert_eq!(
            selected_index(hybrid.decide_at(&request, &candidates, SESSION_WAIT)),
            c
        );
        let flag_off = session_hybrid("discount-seed", false, |_| {});
        assert_eq!(
            selected_index(flag_off.decide_at(&request, &candidates, Duration::ZERO)),
            a
        );
    }

    #[test]
    fn returning_session_outside_group_replaces_last_member() {
        let target = target();
        let mut candidates = single_slot_candidates(4);
        let ranked = session_ranking("group-seed", &candidates);
        let (a, b, c, d) = (ranked[0], ranked[1], ranked[2], ranked[3]);
        let hint = last_cluster(&candidates[c]);
        let hybrid = session_hybrid("group-seed", true, |settings| {
            settings.cache_affinity_backend_selection_count = Some(2);
        });
        let request = session_request(&target, 0, &hint);

        // The group is (C, A): B is not selectable during the wait.
        for _ in 0..32 {
            let choice = selected_index(hybrid.decide_at(&request, &candidates, Duration::ZERO));
            assert!([a, c].contains(&choice), "chose {choice}");
        }

        for index in [a, c, d] {
            candidates[index].stats.num_running_queries = 1;
        }
        assert_eq!(
            hybrid.decide_at(&request, &candidates, Duration::from_millis(100)),
            LoadBalancerDecision::Wait(Duration::from_millis(200))
        );
        let choice = hybrid
            .decide_at(&request, &candidates, SESSION_WAIT)
            .selected()
            .unwrap();
        assert_eq!(choice.candidate_index, b);
        assert_eq!(choice.rank_depth, 3);
    }

    #[test]
    fn returning_session_inside_group_keeps_members() {
        let target = target();
        let mut candidates = single_slot_candidates(4);
        let ranked = session_ranking("members-seed", &candidates);
        let hint = last_cluster(&candidates[ranked[1]]);
        let group_of_two = |settings: &mut WaitAndWidenAlgorithmConfig| {
            settings.cache_affinity_backend_selection_count = Some(2);
        };
        let hybrid = session_hybrid("members-seed", true, group_of_two);
        let flag_off = session_hybrid("members-seed", false, group_of_two);
        let request = session_request(&target, 0, &hint);

        for _ in 0..32 {
            let choice = selected_index(hybrid.decide_at(&request, &candidates, Duration::ZERO));
            assert!(ranked[..2].contains(&choice), "chose {choice}");
        }
        candidates[ranked[0]].stats.num_running_queries = 1;
        candidates[ranked[1]].stats.num_running_queries = 1;
        for elapsed_ms in [0, 100] {
            let elapsed = Duration::from_millis(elapsed_ms);
            assert_eq!(
                hybrid.decide_at(&request, &candidates, elapsed),
                flag_off.decide_at(&request, &candidates, elapsed)
            );
        }
    }

    #[test]
    fn returning_session_widens_bands_from_reordered_ranking() {
        let target = target();
        let base = single_slot_candidates(5);
        let ranked = session_ranking("band-seed", &base);
        let hint = last_cluster(&base[ranked[3]]);
        let hybrid = session_hybrid("band-seed", true, |settings| {
            settings.band_widen_interval_ms = Some(100);
        });
        let request = session_request(&target, 0, &hint);
        let only_free = |rank: usize| {
            let mut candidates = base.clone();
            for (position, index) in ranked.iter().enumerate() {
                if position != rank {
                    candidates[*index].stats.num_running_queries = 1;
                }
            }
            candidates
        };

        // After the wait the open set is D, A, B. C opens with the next band.
        let c_free = only_free(2);
        assert_eq!(
            hybrid.decide_at(&request, &c_free, SESSION_WAIT),
            LoadBalancerDecision::Wait(Duration::from_millis(100))
        );
        let choice = hybrid
            .decide_at(&request, &c_free, SESSION_WAIT + Duration::from_millis(100))
            .selected()
            .unwrap();
        assert_eq!(choice.candidate_index, ranked[2]);
        assert_eq!(choice.rank_depth, 4);

        let b_free = only_free(1);
        let choice = hybrid
            .decide_at(&request, &b_free, SESSION_WAIT)
            .selected()
            .unwrap();
        assert_eq!(choice.candidate_index, ranked[1]);
        assert_eq!(choice.rank_depth, 3);
    }

    #[test]
    fn retry_excluded_last_cluster_acts_as_ineligible_primary() {
        let target = target();
        let candidates = single_slot_candidates(3);
        let ranked = session_ranking("excluded-seed", &candidates);
        let b_id = candidates[ranked[1]].cluster_id.clone();
        let hint = last_cluster(&candidates[ranked[1]]);
        let excluded = HashSet::from([b_id.clone()]);
        let request = LoadBalancerRequest {
            excluded_cluster_ids: Some(&excluded),
            ..session_request(&target, 0, &hint)
        };
        let hybrid = session_hybrid("excluded-seed", true, |_| {});

        assert_eq!(
            hybrid.decide_at(&request, &candidates, Duration::ZERO),
            LoadBalancerDecision::Wait(SESSION_WAIT)
        );
        let choice = selected_index(hybrid.decide_at(&request, &candidates, SESSION_WAIT));
        assert_ne!(candidates[choice].cluster_id, b_id);
    }

    #[test]
    fn kv_infeasible_last_cluster_acts_as_ineligible_primary() {
        let target = target();
        let mut candidates = candidates(3, 1024);
        let ranked = session_ranking("kv-seed", &candidates);
        candidates[ranked[1]].stats.kv_cache_free_tokens = 50;
        candidates[ranked[1]].stats.kv_cache_used_tokens = 974;
        let hint = last_cluster(&candidates[ranked[1]]);
        let request = session_request(&target, 100, &hint);
        let hybrid = PulsarWaitAndWidenLoadBalancer::new(pulsar_wait_and_widen_algorithm_config(
            "kv-seed",
            true,
            |settings| {
                settings.n = Some(2);
                settings.cache_affinity_wait_ms = Some(300);
                settings.last_cluster_affinity = Some(true);
            },
        ))
        .expect("valid hybrid config");

        assert_eq!(
            hybrid.decide_at(&request, &candidates, Duration::ZERO),
            LoadBalancerDecision::Wait(SESSION_WAIT)
        );
        let choice = hybrid
            .decide_at(&request, &candidates, SESSION_WAIT)
            .selected()
            .unwrap();
        assert_ne!(choice.candidate_index, ranked[1]);
        assert!(choice.selected_after_kv_free_tokens_skip);
    }

    #[test]
    fn promotion_is_not_cached() {
        let target = target();
        let candidates = single_slot_candidates(3);
        let ranked = session_ranking("cache-seed", &candidates);
        let hybrid = session_hybrid("cache-seed", true, |_| {});
        let returning = last_cluster(&candidates[ranked[1]]);
        assert_eq!(
            selected_index(hybrid.decide_at(
                &session_request(&target, 0, &returning),
                &candidates,
                Duration::ZERO
            )),
            ranked[1]
        );

        let new_session = LastClusterHint::default();
        let choice = hybrid
            .decide_at(
                &session_request(&target, 0, &new_session),
                &candidates,
                Duration::ZERO,
            )
            .selected()
            .unwrap();
        assert_eq!(choice.candidate_index, ranked[0]);
        assert_eq!(choice.rank_depth, 1);
    }

    #[test]
    fn session_decisions_match_across_instances() {
        let target = target();
        let mut candidates = single_slot_candidates(5);
        let ranked = session_ranking("instances-seed", &candidates);
        let (a, b, c, d, e) = (ranked[0], ranked[1], ranked[2], ranked[3], ranked[4]);
        // RTTs more than one TTFT bucket apart give one winner per open
        // set; ties within a bucket break randomly. With D
        // promoted the order is D, A, B, C, E: E is fastest but opens last.
        for (rtt_ms, index) in [(150, a), (100, b), (50, c), (250, d), (0, e)] {
            candidates[index].rtt = Duration::from_millis(rtt_ms);
        }
        let mut group_full = candidates.clone();
        group_full[a].stats.num_running_queries = 1;
        group_full[d].stats.num_running_queries = 1;
        // A 1 ms band interval opens E one millisecond after C's band,
        // before any later TTFT bucket unlocks.
        let configure = |settings: &mut WaitAndWidenAlgorithmConfig| {
            settings.n = Some(5);
            settings.cache_affinity_backend_selection_count = Some(2);
            settings.band_widen_interval_ms = Some(1);
        };
        let instances: Vec<_> = (0..2)
            .map(|_| session_hybrid("instances-seed", true, configure))
            .collect();

        let plans: Vec<_> = instances
            .iter()
            .map(|hybrid| {
                let hint = last_cluster(&candidates[d]);
                let request = session_request(&target, 0, &hint);
                let plan = session::plan_affinity(
                    hybrid.last_cluster_affinity,
                    &request,
                    &candidates,
                    Cow::Owned(hybrid.ranking.compute_ranking(&request, &candidates)),
                    MissingLastCluster::Keep,
                    hybrid.affinity_wait,
                    hybrid.affinity_input_tokens_scale,
                );
                (
                    plan.order.into_owned(),
                    plan.affinity_wait,
                    plan.input_tokens_scale,
                    hint.classification(),
                )
            })
            .collect();
        assert_eq!(plans[0], plans[1]);
        assert_eq!(plans[0].0, vec![d, a, b, c, e]);
        assert_eq!((plans[0].1, plans[0].2), (SESSION_WAIT, 0.1));

        for (snapshot, elapsed_ms, expected) in [
            (&candidates, 0, Some(a)),
            (&group_full, 150, None),
            (&group_full, 300, Some(c)),
            (&group_full, 301, Some(e)),
        ] {
            let elapsed = Duration::from_millis(elapsed_ms);
            let decisions: Vec<_> = instances
                .iter()
                .map(|hybrid| {
                    let hint = last_cluster(&candidates[d]);
                    hybrid.decide_at(&session_request(&target, 0, &hint), snapshot, elapsed)
                })
                .collect();
            assert_eq!(decisions[0], decisions[1], "at {elapsed_ms} ms");
            assert_eq!(
                decisions[0].selected().map(|choice| choice.candidate_index),
                expected,
                "at {elapsed_ms} ms"
            );
        }
    }

    #[test]
    fn last_cluster_is_ignored_when_flag_is_off() {
        let target = target();
        let mut candidates = single_slot_candidates(3);
        let ranked = session_ranking("off-seed", &candidates);
        candidates[ranked[1]].stats.num_running_queries = 1;
        let hybrid = session_hybrid("off-seed", false, |_| {});

        for elapsed_ms in [0, 300] {
            let elapsed = Duration::from_millis(elapsed_ms);
            let hint = last_cluster(&candidates[ranked[1]]);
            let with_header =
                hybrid.decide_at(&session_request(&target, 0, &hint), &candidates, elapsed);
            let without = hybrid.decide_at(
                &request(&target, Some(SESSION_KEY), Some(0)),
                &candidates,
                elapsed,
            );
            assert_eq!(with_header, without);
            assert_eq!(hint.classification(), None);
        }
    }

    #[test]
    fn last_cluster_needs_an_affinity_key() {
        let target = target();
        let candidates = single_slot_candidates(3);
        let hint = last_cluster(&candidates[0]);
        let hybrid = session_hybrid("no-key-seed", true, |_| {});
        let request = LoadBalancerRequest {
            last_cluster: Some(&hint),
            ..request(&target, None, Some(0))
        };

        assert!(
            hybrid
                .decide_at(&request, &candidates, Duration::ZERO)
                .selected()
                .is_some()
        );
        assert_eq!(hint.classification(), None);
    }
}

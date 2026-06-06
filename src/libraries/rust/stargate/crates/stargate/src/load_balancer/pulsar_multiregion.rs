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

use super::groq_multiregion::{GroqMultiregionConfig, GroqMultiregionLoadBalancer};
use super::pulsar::PulsarLoadBalancer;
use super::{
    LoadBalancer, LoadBalancerAlgorithmConfig, LoadBalancerCandidateChoice, LoadBalancerRequest,
};
use crate::routing_state::RoutedClusterSnapshot;

pub(super) struct PulsarMultiregionLoadBalancer {
    ranking: PulsarLoadBalancer,
    multiregion: GroqMultiregionLoadBalancer,
}

impl PulsarMultiregionLoadBalancer {
    pub(super) fn new(config: LoadBalancerAlgorithmConfig) -> Self {
        Self {
            multiregion: GroqMultiregionLoadBalancer::new(
                GroqMultiregionConfig::from_algorithm_config(&config),
            ),
            ranking: PulsarLoadBalancer::new(config),
        }
    }

    fn choice_with_pulsar_rank(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        ranked_indices: &[usize],
        choice: LoadBalancerCandidateChoice,
    ) -> LoadBalancerCandidateChoice {
        let rank_depth = ranked_indices
            .iter()
            .position(|index| *index == choice.candidate_index)
            .map(|position| position + 1)
            .expect("multiregion choice must come from the PULSAR-ranked candidates");
        LoadBalancerCandidateChoice {
            candidate_index: choice.candidate_index,
            rank_depth,
            selected_after_kv_free_tokens_skip: ranked_indices[..rank_depth - 1].iter().any(
                |candidate_index| {
                    self.ranking
                        .feasibility(request, &candidates[*candidate_index])
                        .skipped_for_kv_free_tokens()
                },
            ),
        }
    }

    fn kv_eligible_ranked_indices(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        ranked_indices: &[usize],
    ) -> Vec<usize> {
        ranked_indices
            .iter()
            .copied()
            .filter(|candidate_index| {
                self.ranking
                    .feasibility(request, &candidates[*candidate_index])
                    .is_eligible()
            })
            .collect()
    }
}

impl fmt::Display for PulsarMultiregionLoadBalancer {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "pulsar-multiregion")
    }
}

impl LoadBalancer for PulsarMultiregionLoadBalancer {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        if candidates.is_empty() {
            return None;
        }

        let ranked_indices = self.ranking.compute_ranking(request, candidates);
        let primary_index = *ranked_indices.first()?;
        let primary = &candidates[primary_index];
        if !self.multiregion.has_queue_slo(request)
            && self.ranking.feasibility(request, primary).is_eligible()
        {
            return Some(LoadBalancerCandidateChoice {
                candidate_index: primary_index,
                rank_depth: 1,
                selected_after_kv_free_tokens_skip: false,
            });
        }

        let primary_candidates =
            self.kv_eligible_ranked_indices(request, candidates, &[primary_index]);
        if let Some(choice) =
            self.multiregion
                .choose_from_candidate_indices(request, candidates, &primary_candidates)
        {
            return Some(self.choice_with_pulsar_rank(
                request,
                candidates,
                &ranked_indices,
                choice,
            ));
        }

        let mut band_start = 1usize;
        let mut band_width = 2usize;
        while band_start < ranked_indices.len() {
            let band_end = (band_start + band_width).min(ranked_indices.len());
            let eligible_indices = self.kv_eligible_ranked_indices(
                request,
                candidates,
                &ranked_indices[band_start..band_end],
            );
            if let Some(choice) = self.multiregion.choose_from_candidate_indices(
                request,
                candidates,
                &eligible_indices,
            ) {
                return Some(self.choice_with_pulsar_rank(
                    request,
                    candidates,
                    &ranked_indices,
                    choice,
                ));
            }
            band_start = band_end;
            band_width = band_width.saturating_mul(2);
        }

        None
    }
}

#[cfg(test)]
mod tests {
    use std::collections::{HashMap, HashSet};
    use std::time::{Duration, Instant};

    use stargate_proto::pb::{InferenceServerStatus, ModelStats};

    use super::super::pulsar::PulsarLoadBalancer;
    use super::super::{
        GroqMultiregionAlgorithmConfig, LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig,
        LoadBalancerRequest, LoadBalancerTestChoiceExt,
    };
    use super::*;
    use crate::routing_state::{RoutedClusterSnapshot, RoutingTargetKey};

    fn target() -> RoutingTargetKey {
        RoutingTargetKey {
            routing_key: Some("rk-1".to_string()),
            model_id: "model-a".to_string(),
        }
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
            .expect("pulsar-multiregion supports deterministic seeding");
        config.request_policy_mut().require_cache_affinity_key = true;
        config.request_policy_mut().require_input_tokens = true;
        config
    }

    fn pulsar_multiregion_algorithm_config(
        seed: &str,
        configure: impl FnOnce(&mut GroqMultiregionAlgorithmConfig),
    ) -> LoadBalancerAlgorithmConfig {
        let mut config =
            LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PulsarMultiregion);
        config
            .set_seed(Some(seed.to_string()))
            .expect("pulsar-multiregion supports deterministic seeding");
        configure(
            config
                .multiregion_settings_mut()
                .expect("pulsar-multiregion config should expose multiregion settings"),
        );
        config
    }

    fn kv_aware_pulsar_multiregion_algorithm_config(
        seed: &str,
        configure: impl FnOnce(&mut GroqMultiregionAlgorithmConfig),
    ) -> LoadBalancerAlgorithmConfig {
        let mut config = pulsar_multiregion_algorithm_config(seed, configure);
        config.request_policy_mut().consider_kv_free_tokens = true;
        config
    }

    fn candidate(id: &str, kv_cache_free_tokens: u64) -> RoutedClusterSnapshot {
        RoutedClusterSnapshot {
            cluster_id: id.to_string(),
            stats: ModelStats {
                output_tps: 0.0,
                last_mean_input_tps: 100.0,
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
        let mut candidates = (0..3)
            .map(|index| {
                let mut candidate = candidate(&format!("cluster-{index}"), 0);
                candidate.stats.last_mean_input_tps = 100.0;
                candidate.stats.max_engine_concurrency = 1;
                candidate
            })
            .collect::<Vec<_>>();
        let hybrid_request = request(&target, Some(affinity_key), Some(0));
        let primary_index =
            pulsar_ranked_indices("hybrid-seed", &target, affinity_key, 0, &candidates)[0];
        candidates[primary_index].stats.num_running_queries = 1;

        let hybrid = PulsarMultiregionLoadBalancer::new(pulsar_multiregion_algorithm_config(
            "hybrid-seed",
            |settings| {
                settings.n = Some(2);
            },
        ));

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
        let mut candidates = (0..4)
            .map(|index| {
                let mut candidate = candidate(&format!("cluster-{index}"), 1024);
                candidate.stats.last_mean_input_tps = 100.0;
                candidate.rtt = Duration::from_millis(50);
                candidate
            })
            .collect::<Vec<_>>();
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

        let hybrid = PulsarMultiregionLoadBalancer::new(
            kv_aware_pulsar_multiregion_algorithm_config("hybrid-kv-seed", |settings| {
                settings.n = Some(2);
            }),
        );
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
        let hybrid_with_slo = PulsarMultiregionLoadBalancer::new(
            kv_aware_pulsar_multiregion_algorithm_config("hybrid-kv-seed", |settings| {
                settings.max_queue_time_floor_ms = Some(100);
                settings.max_queue_time_ceil_ms = Some(100);
                settings.ttft_bucket_size_ms = Some(100);
                settings.n = Some(2);
            }),
        );
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
        let mut candidates = (0..5)
            .map(|index| {
                let mut candidate = candidate(&format!("cluster-{index}"), 0);
                candidate.stats.last_mean_input_tps = 100.0;
                candidate.rtt = Duration::from_millis(100);
                candidate
            })
            .collect::<Vec<_>>();
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

        let hybrid = PulsarMultiregionLoadBalancer::new(pulsar_multiregion_algorithm_config(
            "hybrid-seed",
            |settings| {
                settings.max_queue_time_floor_ms = Some(100);
                settings.max_queue_time_ceil_ms = Some(100);
                settings.ttft_bucket_size_ms = Some(100);
                settings.n = Some(2);
            },
        ));
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

        let expanded_choice = hybrid
            .choose_for_test(&hybrid_request, &candidates)
            .expect("second fallback band should contain usable candidates");
        assert_eq!(
            expanded_choice.candidate.cluster_id,
            globally_fast_lower_rank
        );
        assert_eq!(expanded_choice.rank_depth, 4);
    }
}

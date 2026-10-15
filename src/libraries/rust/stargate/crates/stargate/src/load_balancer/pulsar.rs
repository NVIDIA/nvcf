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

mod ranking;

use std::sync::Arc;

use super::{
    LoadBalancer, LoadBalancerAlgorithmConfig, LoadBalancerCandidateChoice, LoadBalancerRequest,
};
use crate::routing_state::RoutedClusterSnapshot;
use ranking::{
    PulsarRankingLookup, PulsarRankingStore, PulsarScorer, ScoredCandidate,
    compare_ranked_candidate, has_valid_input_capacity,
};

#[cfg(test)]
pub(super) use ranking::{pulsar_hash64, pulsar_ranked_indices};

pub(super) struct PulsarLoadBalancer {
    config: LoadBalancerAlgorithmConfig,
    rankings: PulsarRankingStore,
}

impl PulsarLoadBalancer {
    pub(super) fn new(config: LoadBalancerAlgorithmConfig) -> Self {
        Self {
            config,
            rankings: PulsarRankingStore::default(),
        }
    }

    #[cfg(test)]
    pub(super) fn cached_affinity_key_bytes(&self) -> usize {
        self.rankings.cached_key_bytes()
    }
}

impl_display!(PulsarLoadBalancer, "pulsar");

impl LoadBalancer for PulsarLoadBalancer {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let (lookup, cached_choice) =
            self.rankings
                .lookup_choice(request, candidates, |ranking| {
                    self.choose_from_ranked_indices(request, candidates, ranking)
                })?;
        match lookup {
            PulsarRankingLookup::Hit => cached_choice,
            PulsarRankingLookup::MissCacheable => {
                let ranking = Arc::new(self.compute_ranking(request, candidates));
                if ranking.is_empty() {
                    return None;
                }
                let choice = self.choose_from_ranked_indices(request, candidates, &ranking);
                self.rankings
                    .insert_or_choose_existing(request, candidates, ranking, |cached| {
                        self.choose_from_ranked_indices(request, candidates, cached)
                    })
                    .or(choice)
            }
            PulsarRankingLookup::MissBypass => self.choose_by_score_scan(request, candidates),
        }
    }
}

impl PulsarLoadBalancer {
    // Weighted rendezvous produces a stable capacity-weighted ranking per
    // affinity key. Retry and live KV state only gate candidates while walking
    // that ranking, so overflow remains deterministic and dispersed by key.
    fn choose_from_ranked_indices(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        ranked_indices: &[usize],
    ) -> Option<LoadBalancerCandidateChoice> {
        let mut selected_after_kv_free_tokens_skip = false;
        for (index, candidate_index) in ranked_indices.iter().enumerate() {
            let candidate = &candidates[*candidate_index];
            match self.feasibility(request, candidate) {
                PulsarCandidateFeasibility::Eligible => {
                    return Some(LoadBalancerCandidateChoice {
                        candidate_index: *candidate_index,
                        rank_depth: index + 1,
                        selected_after_kv_free_tokens_skip,
                    });
                }
                reason if reason.skipped_for_kv_free_tokens() => {
                    selected_after_kv_free_tokens_skip = true;
                }
                _ => {}
            }
        }

        None
    }

    pub(super) fn compute_ranking(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Vec<usize> {
        ranking::pulsar_ranked_indices(self.config.seed(), request, candidates)
    }

    fn choose_by_score_scan(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let mut scorer = PulsarScorer::new(self.config.seed(), request);
        let mut best_overall = None;
        let mut best_feasible = None;
        for (candidate_index, candidate) in candidates.iter().enumerate() {
            let Some(score) = scorer.score(candidate) else {
                continue;
            };
            let outranks = |best: &ScoredCandidate| {
                compare_ranked_candidate(
                    score,
                    candidate,
                    best.score,
                    &candidates[best.candidate_index],
                )
                .is_lt()
            };
            if best_overall.as_ref().is_none_or(&outranks) {
                best_overall = Some(ScoredCandidate {
                    score,
                    candidate_index,
                });
            }

            if !self.feasibility(request, candidate).is_eligible() {
                continue;
            }
            if best_feasible.as_ref().is_none_or(outranks) {
                best_feasible = Some(ScoredCandidate {
                    score,
                    candidate_index,
                });
            }
        }

        let best = best_feasible?;
        let chosen = &candidates[best.candidate_index];
        let (rank_depth, selected_after_kv_free_tokens_skip) = if best_overall
            .as_ref()
            .is_some_and(|overall| overall.candidate_index == best.candidate_index)
        {
            (1, false)
        } else {
            self.fallback_metadata(request, candidates, chosen, best.score)
        };
        Some(LoadBalancerCandidateChoice {
            candidate_index: best.candidate_index,
            rank_depth,
            selected_after_kv_free_tokens_skip,
        })
    }

    fn fallback_metadata(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        chosen: &RoutedClusterSnapshot,
        chosen_score: f64,
    ) -> (usize, bool) {
        let mut scorer = PulsarScorer::new(self.config.seed(), request);
        let mut rank_depth = 1;
        let mut skipped_for_kv_free_tokens = false;
        for candidate in candidates {
            let Some(score) = scorer.score(candidate) else {
                continue;
            };
            if compare_ranked_candidate(score, candidate, chosen_score, chosen).is_lt() {
                rank_depth += 1;
                skipped_for_kv_free_tokens |= self
                    .feasibility(request, candidate)
                    .skipped_for_kv_free_tokens();
            }
        }
        (rank_depth, skipped_for_kv_free_tokens)
    }

    #[cfg(test)]
    pub(super) fn weight(&self, candidate: &RoutedClusterSnapshot) -> Option<f64> {
        ranking::pulsar_weight(candidate)
    }

    pub(super) fn feasibility(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidate: &RoutedClusterSnapshot,
    ) -> PulsarCandidateFeasibility {
        candidate_feasibility(&self.config, request, candidate)
    }
}

pub(super) fn input_work_admission_candidate(
    config: &LoadBalancerAlgorithmConfig,
    request: &LoadBalancerRequest<'_>,
    candidate: &RoutedClusterSnapshot,
) -> bool {
    has_valid_input_capacity(candidate)
        && candidate_feasibility(config, request, candidate).is_eligible()
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(super) enum PulsarCandidateFeasibility {
    Eligible,
    RetryExcluded,
    MissingRequiredInputTokens,
    MissingKvFreeTokens,
    InsufficientKvFreeTokens,
}

impl PulsarCandidateFeasibility {
    pub(super) fn is_eligible(self) -> bool {
        self == Self::Eligible
    }

    pub(super) fn skipped_for_kv_free_tokens(self) -> bool {
        matches!(
            self,
            Self::MissingKvFreeTokens | Self::InsufficientKvFreeTokens
        )
    }
}

fn candidate_feasibility(
    config: &LoadBalancerAlgorithmConfig,
    request: &LoadBalancerRequest<'_>,
    candidate: &RoutedClusterSnapshot,
) -> PulsarCandidateFeasibility {
    if request.excludes_cluster(&candidate.cluster_id) {
        return PulsarCandidateFeasibility::RetryExcluded;
    }

    if !config.considers_kv_free_tokens() {
        return PulsarCandidateFeasibility::Eligible;
    }

    let Some(input_tokens) = request.input_tokens else {
        return PulsarCandidateFeasibility::MissingRequiredInputTokens;
    };
    if !has_kv_free_token_metrics(candidate) {
        return PulsarCandidateFeasibility::MissingKvFreeTokens;
    }
    if candidate.stats.kv_cache_free_tokens < input_tokens {
        return PulsarCandidateFeasibility::InsufficientKvFreeTokens;
    }

    PulsarCandidateFeasibility::Eligible
}

fn has_kv_free_token_metrics(candidate: &RoutedClusterSnapshot) -> bool {
    candidate.stats.kv_cache_capacity_tokens > 0
        || candidate.stats.kv_cache_used_tokens > 0
        || candidate.stats.kv_cache_free_tokens > 0
}

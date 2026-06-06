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
use std::collections::{HashMap, HashSet, VecDeque};
use std::fmt;
use std::sync::Arc;

use parking_lot::RwLock;
use xxhash_rust::xxh3::xxh3_64;

use super::{
    LoadBalancer, LoadBalancerAlgorithmConfig, LoadBalancerCandidateChoice, LoadBalancerRequest,
    cache_affinity_key_is_cacheable,
};
use crate::routing_state::{RoutedClusterSnapshot, RoutingTargetKey};

const RANKING_CACHE_LIMIT: usize = 4096;
const RANKING_CACHE_PROBATION_LIMIT: usize = 4096;

pub(super) struct PulsarLoadBalancer {
    config: LoadBalancerAlgorithmConfig,
    ranking_cache: RwLock<PulsarRankingCache>,
}

impl PulsarLoadBalancer {
    pub(super) fn new(config: LoadBalancerAlgorithmConfig) -> Self {
        Self {
            config,
            ranking_cache: RwLock::new(PulsarRankingCache::default()),
        }
    }

    #[cfg(test)]
    pub(super) fn cached_affinity_key_bytes(&self) -> usize {
        let cache = self.ranking_cache.read();
        cache.rankings.keys().map(String::len).sum::<usize>()
            + cache.probation.iter().map(String::len).sum::<usize>()
    }
}

impl fmt::Display for PulsarLoadBalancer {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "pulsar")
    }
}

impl LoadBalancer for PulsarLoadBalancer {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let (lookup, cached_choice) = self.ranking_lookup(request, candidates)?;
        match lookup {
            PulsarRankingLookup::Hit => cached_choice,
            PulsarRankingLookup::MissCacheable => {
                let ranking = Arc::new(self.compute_ranking(request, candidates));
                if ranking.is_empty() {
                    return None;
                }
                let choice = self.choose_from_ranked_indices(request, candidates, &ranking);
                let cache_affinity_key = request.cache_affinity_key.unwrap_or("");
                let mut cache = self.ranking_cache.write();
                cache.refresh_if_needed(request.routing_target, candidates);
                if let Some(cached) = cache.get_ref(cache_affinity_key) {
                    return self.choose_from_ranked_indices(request, candidates, cached);
                }
                cache.insert(cache_affinity_key.to_string(), ranking);
                choice
            }
            PulsarRankingLookup::MissBypass => self.choose_by_score_scan(request, candidates),
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
struct PulsarCandidateSignature {
    cluster_id: String,
    last_mean_input_tps_bits: u64,
}

#[derive(Debug, Default)]
struct PulsarRankingCache {
    target: Option<RoutingTargetKey>,
    candidate_signature: Vec<PulsarCandidateSignature>,
    rankings: HashMap<String, Arc<Vec<usize>>>,
    ranking_order: VecDeque<String>,
    probation: HashSet<String>,
    probation_order: VecDeque<String>,
}

impl PulsarRankingCache {
    fn refresh_if_needed(
        &mut self,
        target: &RoutingTargetKey,
        candidates: &[RoutedClusterSnapshot],
    ) {
        if self.matches(target, candidates) {
            return;
        }

        self.target = Some(target.clone());
        self.candidate_signature = candidates
            .iter()
            .map(|candidate| PulsarCandidateSignature {
                cluster_id: candidate.cluster_id.clone(),
                last_mean_input_tps_bits: candidate.stats.last_mean_input_tps.to_bits(),
            })
            .collect();
        self.rankings.clear();
        self.ranking_order.clear();
        self.probation.clear();
        self.probation_order.clear();
    }

    fn matches(&self, target: &RoutingTargetKey, candidates: &[RoutedClusterSnapshot]) -> bool {
        self.target.as_ref() == Some(target)
            && self.candidate_signature.len() == candidates.len()
            && self
                .candidate_signature
                .iter()
                .zip(candidates)
                .all(|(cached, candidate)| {
                    cached.cluster_id == candidate.cluster_id
                        && cached.last_mean_input_tps_bits
                            == candidate.stats.last_mean_input_tps.to_bits()
                })
    }

    fn get_ref(&self, cache_affinity_key: &str) -> Option<&[usize]> {
        self.rankings
            .get(cache_affinity_key)
            .map(|ranking| ranking.as_slice())
    }

    fn has_room(&self) -> bool {
        self.rankings.len() < RANKING_CACHE_LIMIT
    }

    fn miss_lookup(&mut self, cache_affinity_key: &str) -> PulsarRankingLookup {
        if self.has_room() || self.remove_probation(cache_affinity_key) {
            return PulsarRankingLookup::MissCacheable;
        }

        // Avoid full-ranking work for one-off cold keys once the cache is full,
        // but admit the key if it misses again and proves it is not one-off.
        self.insert_probation(cache_affinity_key.to_string());
        PulsarRankingLookup::MissBypass
    }

    fn insert(&mut self, cache_affinity_key: String, ranking: Arc<Vec<usize>>) {
        if let Some(existing) = self.rankings.get_mut(&cache_affinity_key) {
            *existing = ranking;
            return;
        }

        while self.rankings.len() >= RANKING_CACHE_LIMIT {
            let Some(oldest) = self.ranking_order.pop_front() else {
                break;
            };
            self.rankings.remove(&oldest);
        }
        self.remove_probation(&cache_affinity_key);
        self.ranking_order.push_back(cache_affinity_key.clone());
        self.rankings.insert(cache_affinity_key, ranking);
    }

    fn insert_probation(&mut self, cache_affinity_key: String) {
        if !self.probation.insert(cache_affinity_key.clone()) {
            return;
        }
        self.probation_order.push_back(cache_affinity_key);
        while self.probation_order.len() > RANKING_CACHE_PROBATION_LIMIT {
            let Some(oldest) = self.probation_order.pop_front() else {
                break;
            };
            self.probation.remove(&oldest);
        }
    }

    fn remove_probation(&mut self, cache_affinity_key: &str) -> bool {
        if !self.probation.remove(cache_affinity_key) {
            return false;
        }
        if let Some(position) = self
            .probation_order
            .iter()
            .position(|cached| cached == cache_affinity_key)
        {
            self.probation_order.remove(position);
        }
        true
    }
}

enum PulsarRankingLookup {
    Hit,
    MissCacheable,
    MissBypass,
}

struct ScoredCandidate {
    score: f64,
    candidate_index: usize,
}

fn compare_ranked_candidate(
    score_a: f64,
    candidate_a: &RoutedClusterSnapshot,
    score_b: f64,
    candidate_b: &RoutedClusterSnapshot,
) -> Ordering {
    score_b
        .total_cmp(&score_a)
        .then_with(|| candidate_a.cluster_id.cmp(&candidate_b.cluster_id))
}

impl PulsarLoadBalancer {
    /*
    PULSAR selection model

    Treat consistent hashing as a ranking generator, not a direct destination selector.
    For one request we score every candidate, sort by descending score, then choose the
    first candidate that is currently feasible.

    Request-specific ranking
    ------------------------
    The cache-affinity key is the stable request identity for KV reuse. A different
    affinity key gets a different deterministic ranking.

        request key K1                     request key K2
        ---------------                    ---------------
        score(A) = 0.91  rank 1            score(A) = 0.77  rank 2
        score(B) = 0.63  rank 2            score(B) = 0.82  rank 1
        score(C) = 0.18  rank 3            score(C) = 0.11  rank 3

    Progressive unlocking
    ---------------------
    We do not send all overflow to "the next node on a ring". We walk each request's own
    rendezvous ranking until we find the first feasible backend.

        K1 ranking: A -> B -> C
        K2 ranking: B -> A -> C
        K3 ranking: A -> C -> B

        if A saturates:
          K1 unlocks to B
          K2 stays on B
          K3 unlocks to C

    That scattering behavior is the whole point. Shared-primary keys do not all collapse
    onto one shared successor.

    ASCII picture
    -------------

        before saturation

          K1 ---> A
          K2 ---> A
          K3 ---> A

        ring-successor overflow would do this

          K1 ---> B
          K2 ---> B
          K3 ---> B

        PULSAR does this instead

          K1 ---> B
          K2 ---> D
          K3 ---> C

    Feasibility invariants
    ----------------------
    Feasibility must depend only on pre-hash information:

      - request headers (`x-cache-affinity-key`, `x-input-tokens`)
      - backend snapshots (`last_mean_input_tps`, KV metrics)
      - router-local admission state in future extensions

    It must not depend on the scores themselves. The ranking answers "in what order should
    I try backends for this key?" Feasibility answers "is this backend safe right now?"

    Current implementation
    ----------------------
    This implementation is the first useful slice, not the full paper:

      - ranking: weighted rendezvous hashing
      - key material: routing target + cache-affinity key + cluster_id + optional seed
      - weight: `last_mean_input_tps`
      - feasibility: retry exclusions, plus an optional reported-free-KV gate
        against request input tokens

    So the control flow is:

        rank all candidates for this request
             |
             v
        candidate[0] feasible? -- yes --> choose it
             |
             no
             v
        candidate[1] feasible? -- yes --> choose it
             |
             no
             v
        ...
             |
             v
           none feasible --> proxy service-unavailable response
    */
    fn score(
        &self,
        hash_bytes: &mut Vec<u8>,
        prefix_len: usize,
        candidate: &RoutedClusterSnapshot,
    ) -> Option<f64> {
        let weight = self.weight(candidate)?;

        let u = hash_to_unit_interval(hash_bytes, prefix_len, candidate);
        let e = -u.ln();
        if e.is_finite() && e > 0.0 {
            Some(weight / e)
        } else {
            None
        }
    }

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

    fn ranking_lookup(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<(PulsarRankingLookup, Option<LoadBalancerCandidateChoice>)> {
        if candidates.is_empty() {
            return None;
        }

        let cache_affinity_key = request.cache_affinity_key.unwrap_or("");
        if !cache_affinity_key_is_cacheable(cache_affinity_key) {
            return Some((PulsarRankingLookup::MissBypass, None));
        }

        {
            let cache = self.ranking_cache.read();
            if cache.matches(request.routing_target, candidates)
                && let Some(ranking) = cache.get_ref(cache_affinity_key)
            {
                // Keep the cached ranking borrowed only while the cache guard is
                // alive, then return the already-materialized choice. This avoids
                // cloning the Arc on the PULSAR cache-hit path without letting a
                // borrowed ranking escape the lock guard's lifetime.
                let choice = self.choose_from_ranked_indices(request, candidates, ranking);
                return Some((PulsarRankingLookup::Hit, choice));
            }
        }

        let mut cache = self.ranking_cache.write();
        cache.refresh_if_needed(request.routing_target, candidates);
        if let Some(ranking) = cache.get_ref(cache_affinity_key) {
            // Another thread may have populated the ranking after the read miss.
            // Choose while the write guard owns the borrowed slice for the same
            // reason as the read-hit fast path above.
            let choice = self.choose_from_ranked_indices(request, candidates, ranking);
            return Some((PulsarRankingLookup::Hit, choice));
        }
        Some((cache.miss_lookup(cache_affinity_key), None))
    }

    pub(super) fn compute_ranking(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Vec<usize> {
        let mut hash_bytes = pulsar_hash_prefix(
            self.config.seed(),
            &request.routing_target.routing_key,
            &request.routing_target.model_id,
            request.cache_affinity_key,
        );
        let hash_prefix_len = hash_bytes.len();
        let mut scored = Vec::with_capacity(candidates.len());
        for (candidate_index, candidate) in candidates.iter().enumerate() {
            if let Some(score) = self.score(&mut hash_bytes, hash_prefix_len, candidate) {
                scored.push(ScoredCandidate {
                    score,
                    candidate_index,
                });
            }
        }

        scored.sort_unstable_by(|a, b| {
            compare_ranked_candidate(
                a.score,
                &candidates[a.candidate_index],
                b.score,
                &candidates[b.candidate_index],
            )
        });
        scored
            .into_iter()
            .map(|candidate| candidate.candidate_index)
            .collect()
    }

    fn choose_by_score_scan(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice> {
        let mut hash_bytes = pulsar_hash_prefix(
            self.config.seed(),
            &request.routing_target.routing_key,
            &request.routing_target.model_id,
            request.cache_affinity_key,
        );
        let hash_prefix_len = hash_bytes.len();
        let mut best_overall = None;
        let mut best_feasible = None;
        for (candidate_index, candidate) in candidates.iter().enumerate() {
            let Some(score) = self.score(&mut hash_bytes, hash_prefix_len, candidate) else {
                continue;
            };
            let is_best_overall = best_overall.as_ref().is_none_or(|best: &ScoredCandidate| {
                compare_ranked_candidate(
                    score,
                    candidate,
                    best.score,
                    &candidates[best.candidate_index],
                )
                .is_lt()
            });
            if is_best_overall {
                best_overall = Some(ScoredCandidate {
                    score,
                    candidate_index,
                });
            }

            if !self.feasibility(request, candidate).is_eligible() {
                continue;
            }
            let is_best_feasible = best_feasible.as_ref().is_none_or(|best: &ScoredCandidate| {
                compare_ranked_candidate(
                    score,
                    candidate,
                    best.score,
                    &candidates[best.candidate_index],
                )
                .is_lt()
            });
            if is_best_feasible {
                best_feasible = Some(ScoredCandidate {
                    score,
                    candidate_index,
                });
            }
        }

        let best = best_feasible?;
        let chosen = &candidates[best.candidate_index];
        // The common all-feasible case has rank depth 1 and does not need the
        // second score pass used to preserve rank-depth semantics after fallback.
        let rank_depth = if best_overall
            .as_ref()
            .is_some_and(|overall| overall.candidate_index == best.candidate_index)
        {
            1
        } else {
            self.rank_depth_for_score(request, candidates, chosen, best.score)
        };
        Some(LoadBalancerCandidateChoice {
            candidate_index: best.candidate_index,
            rank_depth,
            selected_after_kv_free_tokens_skip: rank_depth > 1
                && self.higher_rank_kv_free_tokens_skip(request, candidates, chosen, best.score),
        })
    }

    fn higher_rank_kv_free_tokens_skip(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        chosen: &RoutedClusterSnapshot,
        chosen_score: f64,
    ) -> bool {
        let mut hash_bytes = pulsar_hash_prefix(
            self.config.seed(),
            &request.routing_target.routing_key,
            &request.routing_target.model_id,
            request.cache_affinity_key,
        );
        let hash_prefix_len = hash_bytes.len();
        candidates.iter().any(|candidate| {
            let Some(score) = self.score(&mut hash_bytes, hash_prefix_len, candidate) else {
                return false;
            };
            compare_ranked_candidate(score, candidate, chosen_score, chosen).is_lt()
                && self
                    .feasibility(request, candidate)
                    .skipped_for_kv_free_tokens()
        })
    }

    fn rank_depth_for_score(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        chosen: &RoutedClusterSnapshot,
        chosen_score: f64,
    ) -> usize {
        let mut hash_bytes = pulsar_hash_prefix(
            self.config.seed(),
            &request.routing_target.routing_key,
            &request.routing_target.model_id,
            request.cache_affinity_key,
        );
        let hash_prefix_len = hash_bytes.len();
        let mut rank_depth = 1;
        for candidate in candidates {
            let Some(score) = self.score(&mut hash_bytes, hash_prefix_len, candidate) else {
                continue;
            };
            if compare_ranked_candidate(score, candidate, chosen_score, chosen).is_lt() {
                rank_depth += 1;
            }
        }
        rank_depth
    }

    pub(super) fn weight(&self, candidate: &RoutedClusterSnapshot) -> Option<f64> {
        // Default to a stable capacity signal rather than live load. PULSAR needs a
        // deterministic per-key ranking for cache affinity; if ranking follows transient
        // load, hot prefixes flap between backends and destroy locality. Relative load
        // belongs in feasibility gates, not in the base rendezvous weight. `last_mean_input_tps`
        // is the built-in stable capacity proxy we already have, and for PULSAR it is
        // required: a backend without valid capacity metadata does not participate.
        if has_valid_input_capacity(candidate) {
            return Some(candidate.stats.last_mean_input_tps);
        }

        None
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

fn has_valid_input_capacity(candidate: &RoutedClusterSnapshot) -> bool {
    candidate.stats.last_mean_input_tps > 0.0 && candidate.stats.last_mean_input_tps.is_finite()
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

const PULSAR_HASH_VERSION: u8 = 1;

#[cfg(test)]
pub(super) fn pulsar_hash64(
    seed: Option<&str>,
    routing_key: &Option<String>,
    model_id: &str,
    cache_affinity_key: Option<&str>,
    affinity_target_id: &str,
) -> u64 {
    let mut bytes = pulsar_hash_prefix(seed, routing_key, model_id, cache_affinity_key);
    append_tagged_bytes(&mut bytes, b"cluster_id", affinity_target_id.as_bytes());
    xxh3_64(&bytes)
}

fn pulsar_hash_prefix(
    seed: Option<&str>,
    routing_key: &Option<String>,
    model_id: &str,
    cache_affinity_key: Option<&str>,
) -> Vec<u8> {
    let mut bytes = Vec::with_capacity(256);
    bytes.push(PULSAR_HASH_VERSION);
    append_tagged_bytes(&mut bytes, b"seed", seed.unwrap_or("").as_bytes());
    append_tagged_bytes(
        &mut bytes,
        b"routing_key",
        routing_key.as_deref().unwrap_or("").as_bytes(),
    );
    append_tagged_bytes(&mut bytes, b"model_id", model_id.as_bytes());
    append_tagged_bytes(
        &mut bytes,
        b"cache_affinity_key",
        cache_affinity_key.unwrap_or("").as_bytes(),
    );
    bytes
}

fn hash_to_unit_interval(
    bytes: &mut Vec<u8>,
    prefix_len: usize,
    candidate: &RoutedClusterSnapshot,
) -> f64 {
    bytes.truncate(prefix_len);
    append_tagged_bytes(bytes, b"cluster_id", candidate.cluster_id.as_bytes());
    let hash = xxh3_64(bytes);
    let numerator = (hash as f64) + 1.0;
    let denominator = (u64::MAX as f64) + 2.0;
    numerator / denominator
}

fn append_tagged_bytes(bytes: &mut Vec<u8>, tag: &[u8], value: &[u8]) {
    bytes.extend_from_slice(tag);
    bytes.push(0xff);
    bytes.extend_from_slice(&(value.len() as u64).to_le_bytes());
    bytes.extend_from_slice(value);
}

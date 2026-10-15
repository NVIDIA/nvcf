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
use std::sync::Arc;

use parking_lot::RwLock;
use stargate_protocol::common::valid_last_mean_input_tps;
use xxhash_rust::xxh3::xxh3_64;

use crate::load_balancer::{
    LoadBalancerCandidateChoice, LoadBalancerRequest, cache_affinity_key_is_cacheable,
};
use crate::routing_state::{RoutedClusterSnapshot, RoutingTargetKey};

const RANKING_CACHE_LIMIT: usize = 4096;
const RANKING_CACHE_PROBATION_LIMIT: usize = 4096;

#[derive(Default)]
pub(super) struct PulsarRankingStore {
    cache: RwLock<PulsarRankingCache>,
}

impl PulsarRankingStore {
    pub(super) fn lookup_choice(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        choose: impl Fn(&[usize]) -> Option<LoadBalancerCandidateChoice>,
    ) -> Option<(PulsarRankingLookup, Option<LoadBalancerCandidateChoice>)> {
        if candidates.is_empty() {
            return None;
        }

        let cache_affinity_key = request.cache_affinity_key.unwrap_or("");
        if !cache_affinity_key_is_cacheable(cache_affinity_key) {
            return Some((PulsarRankingLookup::MissBypass, None));
        }

        {
            let cache = self.cache.read();
            if cache.matches(request.routing_target, candidates)
                && let Some(ranking) = cache.get_ref(cache_affinity_key)
            {
                // Choose under the guard to avoid cloning the cached Arc.
                return Some((PulsarRankingLookup::Hit, choose(ranking)));
            }
        }

        let mut cache = self.cache.write();
        cache.refresh_if_needed(request.routing_target, candidates);
        if let Some(ranking) = cache.get_ref(cache_affinity_key) {
            // Another thread populated the ranking after the read miss.
            return Some((PulsarRankingLookup::Hit, choose(ranking)));
        }
        Some((cache.miss_lookup(cache_affinity_key), None))
    }

    pub(super) fn insert_or_choose_existing(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        ranking: Arc<Vec<usize>>,
        choose: impl Fn(&[usize]) -> Option<LoadBalancerCandidateChoice>,
    ) -> Option<LoadBalancerCandidateChoice> {
        let cache_affinity_key = request.cache_affinity_key.unwrap_or("");
        let mut cache = self.cache.write();
        cache.refresh_if_needed(request.routing_target, candidates);
        if let Some(cached) = cache.get_ref(cache_affinity_key) {
            return choose(cached);
        }
        cache.insert(cache_affinity_key.to_string(), ranking);
        None
    }

    #[cfg(test)]
    pub(super) fn cached_key_bytes(&self) -> usize {
        let cache = self.cache.read();
        cache.rankings.keys().map(String::len).sum::<usize>()
            + cache.probation.iter().map(String::len).sum::<usize>()
    }
}

pub(super) enum PulsarRankingLookup {
    Hit,
    MissCacheable,
    MissBypass,
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

    fn miss_lookup(&mut self, cache_affinity_key: &str) -> PulsarRankingLookup {
        if self.rankings.len() < RANKING_CACHE_LIMIT || self.remove_probation(cache_affinity_key) {
            return PulsarRankingLookup::MissCacheable;
        }

        // Avoid full-ranking work for one-off cold keys once the cache is full,
        // but admit the key if it misses again and proves it is not one-off.
        self.insert_probation(cache_affinity_key.to_string());
        PulsarRankingLookup::MissBypass
    }

    fn insert(&mut self, cache_affinity_key: String, ranking: Arc<Vec<usize>>) {
        if self.rankings.len() == RANKING_CACHE_LIMIT {
            let oldest = self
                .ranking_order
                .pop_front()
                .expect("ranking cache order should match its entries");
            self.rankings.remove(&oldest);
        }
        self.remove_probation(&cache_affinity_key);
        self.ranking_order.push_back(cache_affinity_key.clone());
        self.rankings.insert(cache_affinity_key, ranking);
    }

    fn insert_probation(&mut self, cache_affinity_key: String) {
        assert!(
            self.probation.insert(cache_affinity_key.clone()),
            "probation key should not already be present"
        );
        self.probation_order.push_back(cache_affinity_key);
        if self.probation_order.len() > RANKING_CACHE_PROBATION_LIMIT {
            let oldest = self
                .probation_order
                .pop_front()
                .expect("probation order should contain the inserted key");
            self.probation.remove(&oldest);
        }
    }

    fn remove_probation(&mut self, cache_affinity_key: &str) -> bool {
        if !self.probation.remove(cache_affinity_key) {
            return false;
        }
        let position = self
            .probation_order
            .iter()
            .position(|cached| cached == cache_affinity_key)
            .expect("probation order should match its set");
        self.probation_order.remove(position);
        true
    }
}

pub(super) struct ScoredCandidate {
    pub(super) score: f64,
    pub(super) candidate_index: usize,
}

pub(super) fn compare_ranked_candidate(
    score_a: f64,
    candidate_a: &RoutedClusterSnapshot,
    score_b: f64,
    candidate_b: &RoutedClusterSnapshot,
) -> Ordering {
    score_b
        .total_cmp(&score_a)
        .then_with(|| candidate_a.cluster_id.cmp(&candidate_b.cluster_id))
}

pub(super) struct PulsarScorer {
    hash_bytes: Vec<u8>,
    prefix_len: usize,
}

impl PulsarScorer {
    pub(super) fn new(seed: Option<&str>, request: &LoadBalancerRequest<'_>) -> Self {
        let hash_bytes = pulsar_hash_prefix(
            seed,
            &request.routing_target.routing_key,
            &request.routing_target.model_id,
            request.cache_affinity_key,
        );
        Self {
            prefix_len: hash_bytes.len(),
            hash_bytes,
        }
    }

    pub(super) fn score(&mut self, candidate: &RoutedClusterSnapshot) -> Option<f64> {
        let weight = pulsar_weight(candidate)?;
        let u = self.hash_to_unit_interval(candidate);
        let e = -u.ln();
        (e.is_finite() && e > 0.0).then(|| weight / e)
    }

    fn hash_to_unit_interval(&mut self, candidate: &RoutedClusterSnapshot) -> f64 {
        self.hash_bytes.truncate(self.prefix_len);
        append_tagged_bytes(
            &mut self.hash_bytes,
            b"cluster_id",
            candidate.cluster_id.as_bytes(),
        );
        let hash = xxh3_64(&self.hash_bytes);
        ((hash as f64) + 1.0) / ((u64::MAX as f64) + 2.0)
    }
}

pub(in crate::load_balancer) fn pulsar_ranked_indices(
    seed: Option<&str>,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> Vec<usize> {
    let mut scorer = PulsarScorer::new(seed, request);
    let mut scored = Vec::with_capacity(candidates.len());
    for (candidate_index, candidate) in candidates.iter().enumerate() {
        if let Some(score) = scorer.score(candidate) {
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

pub(super) fn pulsar_weight(candidate: &RoutedClusterSnapshot) -> Option<f64> {
    // Default to a stable capacity signal rather than live load. PULSAR needs a
    // deterministic per-key ranking for cache affinity; if ranking follows transient
    // load, hot prefixes flap between backends and destroy locality. Relative load
    // belongs in feasibility gates, not in the base rendezvous weight. `last_mean_input_tps`
    // is the built-in stable capacity proxy we already have, and for PULSAR it is
    // required: a backend without valid capacity metadata does not participate.
    has_valid_input_capacity(candidate).then_some(candidate.stats.last_mean_input_tps)
}

pub(super) fn has_valid_input_capacity(candidate: &RoutedClusterSnapshot) -> bool {
    valid_last_mean_input_tps(candidate.stats.last_mean_input_tps)
}

const PULSAR_HASH_VERSION: u8 = 1;

#[cfg(test)]
pub(in crate::load_balancer) fn pulsar_hash64(
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

fn append_tagged_bytes(bytes: &mut Vec<u8>, tag: &[u8], value: &[u8]) {
    bytes.extend_from_slice(tag);
    bytes.push(0xff);
    bytes.extend_from_slice(&(value.len() as u64).to_le_bytes());
    bytes.extend_from_slice(value);
}

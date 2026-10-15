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

use std::collections::{HashMap, VecDeque};
use std::sync::Arc;

use parking_lot::RwLock;
use xxhash_rust::xxh3::xxh3_64;

use super::{GroqMultiregionConfig, RequestExclusions};
use crate::load_balancer::{
    HashInputBuilder, LoadBalancerRequest, cache_affinity_key_is_cacheable,
};
use crate::routing_state::{RoutedClusterSnapshot, RoutingTargetKey};

const SELECTION_CACHE_LIMIT: usize = 4096;

#[derive(Default)]
pub(super) struct CacheAffinitySelector {
    cache: RwLock<CacheAffinityRingCache>,
}

impl CacheAffinitySelector {
    pub(super) fn candidate_indices(
        &self,
        config: &GroqMultiregionConfig,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<Arc<Vec<usize>>> {
        let cache_affinity_key = request.cache_affinity_key?;
        let selection_count = config.cache_affinity_backend_selection_count?;
        if candidates.is_empty() {
            return None;
        }

        let selection_count = selection_count.min(candidates.len());
        let exclusions = RequestExclusions::from(request);
        let cacheable_selection = cache_affinity_key_is_cacheable(cache_affinity_key)
            && !matches!(exclusions, RequestExclusions::Many);
        let select = |ring: &[CacheAffinityRingEntry]| {
            Arc::new(select_candidate_indices(
                request,
                ring,
                candidates,
                cache_affinity_key,
                selection_count,
                config,
                exclusions,
            ))
        };
        let cached_selection = {
            let cache = self.cache.read();
            if cache.matches(request.routing_target, candidates) {
                if cacheable_selection
                    && let Some(indices) = cache.selection(cache_affinity_key, exclusions)
                {
                    return Some(indices);
                }
                Some(select(&cache.ring))
            } else {
                None
            }
        };
        let (selected_indices, replacement_ring) = match cached_selection {
            Some(indices) => (indices, None),
            None => {
                let ring = build_ring(config, request, candidates);
                let indices = select(&ring);
                (indices, Some(ring))
            }
        };

        if replacement_ring.is_some() || cacheable_selection && !selected_indices.is_empty() {
            let mut cache = self.cache.write();
            if let Some(ring) = replacement_ring {
                cache.replace(request.routing_target, candidates, ring);
            }
            if cacheable_selection
                && !selected_indices.is_empty()
                && cache.matches(request.routing_target, candidates)
            {
                cache.insert_selection(cache_affinity_key, exclusions, selected_indices.clone());
            }
        }

        (!selected_indices.is_empty()).then_some(selected_indices)
    }

    #[cfg(test)]
    pub(super) fn cached_key_bytes(&self) -> usize {
        self.cache.read().cached_key_bytes()
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
struct CacheAffinityRingEntry {
    hash: u64,
    candidate_index: usize,
}

#[derive(Clone, Debug, Eq, Hash, PartialEq)]
struct ExcludedClusterPair {
    first: String,
    second: String,
}

impl ExcludedClusterPair {
    fn new(first: &str, second: &str) -> Self {
        let (first, second) = if first <= second {
            (first, second)
        } else {
            (second, first)
        };
        Self {
            first: first.to_string(),
            second: second.to_string(),
        }
    }
}

#[derive(Debug, Default)]
struct CacheAffinityRingCache {
    target: Option<RoutingTargetKey>,
    candidate_cluster_ids: Vec<String>,
    ring: Vec<CacheAffinityRingEntry>,
    selections: HashMap<String, Arc<Vec<usize>>>,
    selection_order: VecDeque<String>,
    single_excluded: HashMap<String, HashMap<String, Arc<Vec<usize>>>>,
    single_order: VecDeque<(String, String)>,
    two_excluded: HashMap<String, HashMap<ExcludedClusterPair, Arc<Vec<usize>>>>,
    two_order: VecDeque<(String, ExcludedClusterPair)>,
    selection_count: usize,
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
        self.single_excluded.clear();
        self.single_order.clear();
        self.two_excluded.clear();
        self.two_order.clear();
        self.selection_count = 0;
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

    fn selection(
        &self,
        cache_affinity_key: &str,
        exclusions: RequestExclusions<'_>,
    ) -> Option<Arc<Vec<usize>>> {
        self.lookup(cache_affinity_key, exclusions).cloned()
    }

    fn lookup(&self, key: &str, exclusions: RequestExclusions<'_>) -> Option<&Arc<Vec<usize>>> {
        match exclusions {
            RequestExclusions::None => self.selections.get(key),
            RequestExclusions::One(excluded) => self.single_excluded.get(key)?.get(excluded),
            RequestExclusions::Two(first, second) => self
                .two_excluded
                .get(key)?
                .get(&ExcludedClusterPair::new(first, second)),
            RequestExclusions::Many => None,
        }
    }

    fn insert_selection(
        &mut self,
        cache_affinity_key: &str,
        exclusions: RequestExclusions<'_>,
        selected_indices: Arc<Vec<usize>>,
    ) {
        match exclusions {
            RequestExclusions::None => {
                self.insert_plain(cache_affinity_key, selected_indices);
                return;
            }
            RequestExclusions::One(excluded) => {
                if self
                    .single_excluded
                    .get_mut(cache_affinity_key)
                    .is_some_and(|selections| replace(selections, excluded, &selected_indices))
                {
                    return;
                }
                self.evict_selection_entries();
                let (key, excluded) = (cache_affinity_key.to_string(), excluded.to_string());
                self.single_order.push_back((key.clone(), excluded.clone()));
                self.single_excluded
                    .entry(key)
                    .or_default()
                    .insert(excluded, selected_indices);
            }
            RequestExclusions::Two(first, second) => {
                self.insert_pair(cache_affinity_key, first, second, selected_indices);
                return;
            }
            RequestExclusions::Many => panic!("only cacheable exclusion shapes are inserted"),
        }
        self.selection_count += 1;
    }

    fn insert_plain(&mut self, key: &str, selected: Arc<Vec<usize>>) {
        if replace(&mut self.selections, key, &selected) {
            return;
        }
        self.evict_selection_entries();
        let key = key.to_string();
        self.selection_order.push_back(key.clone());
        self.selections.insert(key, selected);
        self.selection_count += 1;
    }

    fn insert_pair(&mut self, key: &str, first: &str, second: &str, selected: Arc<Vec<usize>>) {
        let excluded = ExcludedClusterPair::new(first, second);
        if self
            .two_excluded
            .get_mut(key)
            .is_some_and(|selections| replace(selections, &excluded, &selected))
        {
            return;
        }
        self.evict_selection_entries();
        let key = key.to_string();
        self.two_order.push_back((key.clone(), excluded.clone()));
        self.two_excluded
            .entry(key)
            .or_default()
            .insert(excluded, selected);
        self.selection_count += 1;
    }

    fn evict_selection_entries(&mut self) {
        // All affinity-selection caches share the same entry budget. On
        // pressure, evict retry-specific entries first so normal affinity hits
        // keep the same behavior they had before retry caching existed.
        while self.selection_count >= SELECTION_CACHE_LIMIT {
            let removed = if let Some((key, excluded)) = self.two_order.pop_front() {
                remove_nested(&mut self.two_excluded, &key, &excluded)
            } else if let Some((key, excluded)) = self.single_order.pop_front() {
                remove_nested(&mut self.single_excluded, &key, &excluded)
            } else if let Some(key) = self.selection_order.pop_front() {
                self.selections.remove(&key).is_some()
            } else {
                break;
            };
            self.selection_count -= usize::from(removed);
        }
    }

    #[cfg(test)]
    fn cached_key_bytes(&self) -> usize {
        let plain = self.selections.keys().map(String::len).sum::<usize>();
        let single = self
            .single_excluded
            .iter()
            .map(|(key, selections)| {
                key.len() * selections.len() + selections.keys().map(String::len).sum::<usize>()
            })
            .sum::<usize>();
        let two = self
            .two_excluded
            .iter()
            .map(|(key, selections)| {
                key.len() * selections.len()
                    + selections
                        .keys()
                        .map(|pair| pair.first.len() + pair.second.len())
                        .sum::<usize>()
            })
            .sum::<usize>();
        plain + single + two
    }
}

fn replace<K, Q>(
    selections: &mut HashMap<K, Arc<Vec<usize>>>,
    key: &Q,
    selected: &Arc<Vec<usize>>,
) -> bool
where
    K: std::borrow::Borrow<Q> + std::hash::Hash + Eq,
    Q: std::hash::Hash + Eq + ?Sized,
{
    selections.get_mut(key).is_some_and(|existing| {
        existing.clone_from(selected);
        true
    })
}

fn remove_nested<K: std::hash::Hash + Eq>(
    selections: &mut HashMap<String, HashMap<K, Arc<Vec<usize>>>>,
    cache_affinity_key: &str,
    excluded: &K,
) -> bool {
    let Some(by_exclusion) = selections.get_mut(cache_affinity_key) else {
        return false;
    };
    let removed = by_exclusion.remove(excluded).is_some();
    if by_exclusion.is_empty() {
        selections.remove(cache_affinity_key);
    }
    removed
}

#[cfg(test)]
pub(in crate::load_balancer) fn cache_affinity_candidate_indices(
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> Option<Vec<usize>> {
    CacheAffinitySelector::default()
        .candidate_indices(config, request, candidates)
        .map(Arc::unwrap_or_clone)
}

#[cfg(test)]
pub(in crate::load_balancer) fn cache_affinity_candidates(
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> Option<Vec<RoutedClusterSnapshot>> {
    cache_affinity_candidate_indices(config, request, candidates).map(|selected_indices| {
        selected_indices
            .into_iter()
            .map(|index| candidates[index].clone())
            .collect()
    })
}

fn build_ring(
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> Vec<CacheAffinityRingEntry> {
    let mut ring = Vec::with_capacity(candidates.len() * config.cache_affinity_virtual_nodes);
    for (candidate_index, candidate) in candidates.iter().enumerate() {
        for virtual_node in 0..config.cache_affinity_virtual_nodes {
            ring.push(CacheAffinityRingEntry {
                hash: cache_affinity_virtual_node_hash(config, request, candidate, virtual_node),
                candidate_index,
            });
        }
    }
    ring.sort_unstable_by(|a, b| {
        a.hash
            .cmp(&b.hash)
            .then_with(|| {
                candidates[a.candidate_index]
                    .cluster_id
                    .cmp(&candidates[b.candidate_index].cluster_id)
            })
            .then_with(|| a.candidate_index.cmp(&b.candidate_index))
    });
    ring
}

fn select_candidate_indices(
    request: &LoadBalancerRequest<'_>,
    ring: &[CacheAffinityRingEntry],
    candidates: &[RoutedClusterSnapshot],
    cache_affinity_key: &str,
    selection_count: usize,
    config: &GroqMultiregionConfig,
    exclusions: RequestExclusions<'_>,
) -> Vec<usize> {
    if ring.is_empty() {
        return Vec::new();
    }
    let key_hash = cache_affinity_key_hash(config, request, cache_affinity_key);
    let start_index = ring
        .binary_search_by(|entry| entry.hash.cmp(&key_hash))
        .unwrap_or_else(|index| index);
    // Common retry shapes use direct borrowed comparisons in the ring hot path.
    match exclusions {
        RequestExclusions::One(excluded) => select_candidate_indices_from_ring(
            ring,
            candidates,
            start_index,
            selection_count,
            |cluster_id| cluster_id == excluded,
        ),
        RequestExclusions::Two(first, second) => select_candidate_indices_from_ring(
            ring,
            candidates,
            start_index,
            selection_count,
            |cluster_id| cluster_id == first || cluster_id == second,
        ),
        RequestExclusions::None | RequestExclusions::Many => select_candidate_indices_from_ring(
            ring,
            candidates,
            start_index,
            selection_count,
            |cluster_id| request.excludes_cluster(cluster_id),
        ),
    }
}

fn select_candidate_indices_from_ring(
    ring: &[CacheAffinityRingEntry],
    candidates: &[RoutedClusterSnapshot],
    start_index: usize,
    selection_count: usize,
    mut excludes_cluster: impl FnMut(&str) -> bool,
) -> Vec<usize> {
    let mut selected_indices: Vec<usize> = Vec::with_capacity(selection_count);
    for offset in 0..ring.len() {
        let entry = &ring[(start_index + offset) % ring.len()];
        let cluster_id = candidates[entry.candidate_index].cluster_id.as_str();
        if excludes_cluster(cluster_id) {
            continue;
        }
        if selected_indices
            .iter()
            .all(|&selected| candidates[selected].cluster_id != cluster_id)
        {
            selected_indices.push(entry.candidate_index);
            if selected_indices.len() >= selection_count {
                break;
            }
        }
    }

    selected_indices
}

const HASH_VERSION: u8 = 1;

fn cache_affinity_key_hash(
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    cache_affinity_key: &str,
) -> u64 {
    let mut bytes = HashInputBuilder::new();
    append_ring_prefix(&mut bytes, config, request);
    bytes.append_tagged_bytes(b"cache_affinity_key", cache_affinity_key.as_bytes());
    xxh3_64(bytes.as_slice())
}

pub(in crate::load_balancer) fn cache_affinity_virtual_node_hash(
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
    candidate: &RoutedClusterSnapshot,
    virtual_node: usize,
) -> u64 {
    let mut bytes = HashInputBuilder::new();
    append_ring_prefix(&mut bytes, config, request);
    bytes.append_tagged_bytes(b"cluster_id", candidate.cluster_id.as_bytes());
    bytes.append_tagged_bytes(b"virtual_node", &virtual_node.to_le_bytes());
    xxh3_64(bytes.as_slice())
}

fn append_ring_prefix(
    bytes: &mut HashInputBuilder,
    config: &GroqMultiregionConfig,
    request: &LoadBalancerRequest<'_>,
) {
    bytes.push(HASH_VERSION);
    bytes.append_tagged_bytes(b"seed", config.seed.as_deref().unwrap_or("").as_bytes());
    bytes.append_tagged_bytes(
        b"routing_key",
        request
            .routing_target
            .routing_key
            .as_deref()
            .unwrap_or("")
            .as_bytes(),
    );
    bytes.append_tagged_bytes(b"model_id", request.routing_target.model_id.as_bytes());
}

#[cfg(test)]
mod tests {
    use std::mem::{size_of, size_of_val};

    use super::*;

    #[test]
    fn plain_selection_entries_remain_compact() {
        let mut cache = CacheAffinityRingCache::default();
        for index in 0..SELECTION_CACHE_LIMIT {
            cache.insert_selection(
                &format!("plain-{index}"),
                RequestExclusions::None,
                Arc::new(vec![index]),
            );
        }

        assert_eq!(
            size_of_val(&cache.selections["plain-0"]),
            size_of::<Arc<Vec<usize>>>()
        );
        assert_eq!(cache.selection_count, SELECTION_CACHE_LIMIT);
        assert_eq!(cache.single_excluded.capacity(), 0);
        assert_eq!(cache.single_order.capacity(), 0);
        assert_eq!(cache.two_excluded.capacity(), 0);
        assert_eq!(cache.two_order.capacity(), 0);
    }
}

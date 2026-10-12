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

use super::WaitAndWidenConfig;
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
        config: &WaitAndWidenConfig,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<Arc<Vec<usize>>> {
        let cache_affinity_key = request.cache_affinity_key?;
        let selection_count = config.cache_affinity_backend_selection_count?;
        if candidates.is_empty() {
            return None;
        }

        let selection_count = selection_count.min(candidates.len());
        // Determine membership before applying retry exclusions. Replacing a
        // failed member would give a cold successor the affinity discount.
        let cacheable_selection = cache_affinity_key_is_cacheable(cache_affinity_key);
        let select = |ring: &[CacheAffinityRingEntry]| {
            Arc::new(select_candidate_indices(
                request,
                ring,
                candidates,
                cache_affinity_key,
                selection_count,
                config,
            ))
        };
        let cached_selection = {
            let cache = self.cache.read();
            if cache.matches(request.routing_target, candidates) {
                if cacheable_selection && let Some(indices) = cache.selection(cache_affinity_key) {
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
                cache.insert_selection(cache_affinity_key, selected_indices.clone());
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

#[derive(Debug, Default)]
struct CacheAffinityRingCache {
    target: Option<RoutingTargetKey>,
    candidate_cluster_ids: Vec<String>,
    ring: Vec<CacheAffinityRingEntry>,
    selections: HashMap<String, Arc<Vec<usize>>>,
    selection_order: VecDeque<String>,
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

    fn insert_selection(&mut self, key: &str, selected: Arc<Vec<usize>>) {
        if let Some(existing) = self.selections.get_mut(key) {
            *existing = selected;
            return;
        }
        if self.selections.len() >= SELECTION_CACHE_LIMIT {
            let oldest = self
                .selection_order
                .pop_front()
                .expect("cached selection has insertion order");
            self.selections.remove(&oldest);
        }
        self.selection_order.push_back(key.to_string());
        self.selections.insert(key.to_string(), selected);
    }

    #[cfg(test)]
    fn cached_key_bytes(&self) -> usize {
        self.selections.keys().map(String::len).sum()
    }
}

#[cfg(test)]
pub(in crate::load_balancer) fn cache_affinity_candidate_indices(
    config: &WaitAndWidenConfig,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> Option<Vec<usize>> {
    CacheAffinitySelector::default()
        .candidate_indices(config, request, candidates)
        .map(Arc::unwrap_or_clone)
}

#[cfg(test)]
pub(in crate::load_balancer) fn cache_affinity_candidates(
    config: &WaitAndWidenConfig,
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
    config: &WaitAndWidenConfig,
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
    config: &WaitAndWidenConfig,
) -> Vec<usize> {
    if ring.is_empty() {
        return Vec::new();
    }
    let key_hash = cache_affinity_key_hash(config, request, cache_affinity_key);
    let start_index = ring
        .binary_search_by(|entry| entry.hash.cmp(&key_hash))
        .unwrap_or_else(|index| index);
    let mut selected_indices: Vec<usize> = Vec::with_capacity(selection_count);
    for offset in 0..ring.len() {
        let entry = &ring[(start_index + offset) % ring.len()];
        let cluster_id = candidates[entry.candidate_index].cluster_id.as_str();
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
    config: &WaitAndWidenConfig,
    request: &LoadBalancerRequest<'_>,
    cache_affinity_key: &str,
) -> u64 {
    let mut bytes = HashInputBuilder::new();
    append_ring_prefix(&mut bytes, config, request);
    bytes.append_tagged_bytes(b"cache_affinity_key", cache_affinity_key.as_bytes());
    xxh3_64(bytes.as_slice())
}

pub(in crate::load_balancer) fn cache_affinity_virtual_node_hash(
    config: &WaitAndWidenConfig,
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
    config: &WaitAndWidenConfig,
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
            cache.insert_selection(&format!("plain-{index}"), Arc::new(vec![index]));
        }

        assert_eq!(
            size_of_val(&cache.selections["plain-0"]),
            size_of::<Arc<Vec<usize>>>()
        );
        assert_eq!(cache.selections.len(), SELECTION_CACHE_LIMIT);
        cache.insert_selection("replacement", Arc::new(vec![0]));
        assert_eq!(cache.selections.len(), SELECTION_CACHE_LIMIT);
        assert!(cache.selection("plain-0").is_none());
        assert!(cache.selection("replacement").is_some());
    }
}

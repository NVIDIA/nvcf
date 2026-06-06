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

use crate::routing_state::RoutedClusterSnapshot;

use super::{
    LoadBalancerAlgorithm, LoadBalancerAlgorithmConfig, LoadBalancerCandidateChoice,
    LoadBalancerRequest, pulsar,
};

pub trait LoadBalancer: Send + Sync + fmt::Display {
    fn choose_candidate(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<LoadBalancerCandidateChoice>;
}

const HASH_INPUT_STACK_LEN: usize = 256;
pub(crate) const MAX_CACHE_AFFINITY_CACHE_KEY_BYTES: usize = 256;

#[inline]
pub(crate) fn cache_affinity_key_is_cacheable(cache_affinity_key: &str) -> bool {
    cache_affinity_key.len() <= MAX_CACHE_AFFINITY_CACHE_KEY_BYTES
}

#[inline]
pub(crate) fn input_work_units(candidate: &RoutedClusterSnapshot) -> f64 {
    candidate.stats.queued_input_size as f64
}

#[cfg(test)]
pub(crate) fn input_work_seconds(
    candidates: &[RoutedClusterSnapshot],
    request_input_tokens: u64,
    excluded_cluster_ids: Option<&std::collections::HashSet<String>>,
) -> Option<f64> {
    input_work_seconds_from_candidates(
        candidates.iter().filter(|candidate| {
            !excluded_cluster_ids.is_some_and(|excluded| excluded.contains(&candidate.cluster_id))
        }),
        request_input_tokens,
    )
}

pub(crate) fn input_work_seconds_for_request(
    config: &LoadBalancerAlgorithmConfig,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
) -> Option<f64> {
    input_work_seconds_from_candidates(
        candidates.iter().filter(|candidate| {
            input_work_admission_includes_candidate(config, request, candidate)
        }),
        request.input_tokens.unwrap_or_default(),
    )
}

fn input_work_admission_includes_candidate(
    config: &LoadBalancerAlgorithmConfig,
    request: &LoadBalancerRequest<'_>,
    candidate: &RoutedClusterSnapshot,
) -> bool {
    match config.algorithm() {
        LoadBalancerAlgorithm::Pulsar | LoadBalancerAlgorithm::PulsarMultiregion => {
            pulsar::input_work_admission_candidate(config, request, candidate)
        }
        _ => !request.excludes_cluster(&candidate.cluster_id),
    }
}

fn input_work_seconds_from_candidates<'a>(
    candidates: impl IntoIterator<Item = &'a RoutedClusterSnapshot>,
    request_input_tokens: u64,
) -> Option<f64> {
    let mut work_units = request_input_tokens as f64;
    let mut service_rate = 0.0;
    for candidate in candidates {
        work_units += input_work_units(candidate);
        if candidate.stats.last_mean_input_tps > 0.0
            && candidate.stats.last_mean_input_tps.is_finite()
        {
            service_rate += candidate.stats.last_mean_input_tps;
        }
    }

    (service_rate > 0.0 && service_rate.is_finite())
        .then_some(work_units / service_rate)
        .filter(|seconds| seconds.is_finite())
}

pub(crate) struct HashInputBuilder {
    stack: [u8; HASH_INPUT_STACK_LEN],
    len: usize,
    heap: Option<Vec<u8>>,
}

impl HashInputBuilder {
    pub(crate) fn new() -> Self {
        Self {
            stack: [0; HASH_INPUT_STACK_LEN],
            len: 0,
            heap: None,
        }
    }

    #[inline]
    pub(crate) fn push(&mut self, byte: u8) {
        self.extend_from_slice(&[byte]);
    }

    #[inline]
    pub(crate) fn append_tagged_bytes(&mut self, tag: &[u8], value: &[u8]) {
        self.extend_from_slice(tag);
        self.push(0xff);
        self.extend_from_slice(&(value.len() as u64).to_le_bytes());
        self.extend_from_slice(value);
    }

    #[inline]
    pub(crate) fn as_slice(&self) -> &[u8] {
        match &self.heap {
            Some(heap) => heap.as_slice(),
            None => &self.stack[..self.len],
        }
    }

    #[inline]
    fn extend_from_slice(&mut self, bytes: &[u8]) {
        if let Some(heap) = &mut self.heap {
            heap.extend_from_slice(bytes);
            return;
        }

        let new_len = self.len + bytes.len();
        if new_len <= self.stack.len() {
            self.stack[self.len..new_len].copy_from_slice(bytes);
            self.len = new_len;
            return;
        }

        let mut heap = Vec::with_capacity(new_len.max(self.stack.len() * 2));
        heap.extend_from_slice(&self.stack[..self.len]);
        heap.extend_from_slice(bytes);
        self.heap = Some(heap);
    }
}

#[cfg(test)]
pub(crate) struct SelectedCandidateForTest {
    pub(super) candidate: SelectedClusterForTest,
    pub(super) rank_depth: usize,
    pub(super) selected_after_kv_free_tokens_skip: bool,
}

#[cfg(test)]
pub(crate) struct SelectedClusterForTest {
    pub(super) cluster_id: String,
}

#[cfg(test)]
pub(crate) trait LoadBalancerTestChoiceExt: LoadBalancer {
    fn choose_for_test(
        &self,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
    ) -> Option<SelectedCandidateForTest> {
        self.choose_candidate(request, candidates)
            .map(|choice| SelectedCandidateForTest {
                candidate: SelectedClusterForTest {
                    cluster_id: candidates[choice.candidate_index].cluster_id.clone(),
                },
                rank_depth: choice.rank_depth,
                selected_after_kv_free_tokens_skip: choice.selected_after_kv_free_tokens_skip,
            })
    }
}

#[cfg(test)]
impl<T> LoadBalancerTestChoiceExt for T where T: LoadBalancer + ?Sized {}

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

use crate::load_balancer::{LoadBalancerRequest, Ttft, ttft};
use crate::routing_state::RoutedClusterSnapshot;

use super::WaitAndWidenConfig;

pub(super) struct CandidateTtftAccumulator<'a> {
    ttfts: Vec<(&'a RoutedClusterSnapshot, Ttft)>,
    input_tokens: Option<u64>,
    priority: u32,
    max_queue_time_ms: Option<f64>,
    ignore_queue_time: bool,
    ignore_input_processing_time: bool,
    fastest_ttft: f64,
    slowest_ttft: f64,
    all_ttfts_finite: bool,
}

impl<'a> CandidateTtftAccumulator<'a> {
    pub(super) fn new(
        config: &WaitAndWidenConfig,
        request: &LoadBalancerRequest<'_>,
        candidate_capacity: usize,
    ) -> Self {
        let max_queue_time_ms = config
            .max_queue_time(request)
            .map(|duration| duration.as_secs_f64() * 1000.0);

        Self {
            ttfts: Vec::with_capacity(candidate_capacity),
            input_tokens: request.input_tokens,
            priority: request.priority,
            max_queue_time_ms,
            ignore_queue_time: config.ignore_queue_time,
            ignore_input_processing_time: config.ignore_input_processing_time,
            fastest_ttft: f64::INFINITY,
            slowest_ttft: f64::NEG_INFINITY,
            all_ttfts_finite: true,
        }
    }

    pub(super) fn filters_by_queue_slo(&self) -> bool {
        self.max_queue_time_ms.is_some()
    }

    pub(super) fn push_ttft(&mut self, candidate: &'a RoutedClusterSnapshot) {
        let ttft = ttft(
            candidate,
            self.input_tokens,
            self.priority,
            self.ignore_queue_time,
            self.ignore_input_processing_time,
        );
        if !within_queue_slo(&ttft, self.max_queue_time_ms) {
            return;
        }

        if ttft.ttft_ms.is_finite() {
            self.fastest_ttft = self.fastest_ttft.min(ttft.ttft_ms);
            self.slowest_ttft = self.slowest_ttft.max(ttft.ttft_ms);
        } else {
            self.all_ttfts_finite = false;
        }
        self.ttfts.push((candidate, ttft));
    }

    pub(super) fn is_empty(&self) -> bool {
        self.ttfts.is_empty()
    }

    pub(super) fn has_finite_fastest_ttft(&self) -> bool {
        self.fastest_ttft.is_finite()
    }

    pub(super) fn all_in_first_bucket(&self, bucket_size_ms: f64) -> bool {
        self.all_ttfts_finite && self.slowest_ttft - self.fastest_ttft <= bucket_size_ms
    }

    pub(super) fn into_ttfts(self) -> Vec<(&'a RoutedClusterSnapshot, Ttft)> {
        self.ttfts
    }
}

pub(super) fn compare_least_queue_time(
    candidate_a: &RoutedClusterSnapshot,
    ttft_a: &Ttft,
    candidate_b: &RoutedClusterSnapshot,
    ttft_b: &Ttft,
) -> Ordering {
    match ttft_a.queue_ms.total_cmp(&ttft_b.queue_ms) {
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

pub(super) fn has_capacity(candidate: &RoutedClusterSnapshot, max_queued: u64) -> bool {
    if candidate.stats.max_engine_concurrency == 0 {
        return true;
    }
    candidate.stats.num_running_queries < candidate.stats.max_engine_concurrency + max_queued
}

fn within_queue_slo(ttft: &Ttft, max_queue_time_ms: Option<f64>) -> bool {
    max_queue_time_ms.is_none_or(|max_queue_time_ms| ttft.queue_ms <= max_queue_time_ms)
}

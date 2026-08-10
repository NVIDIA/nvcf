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

use crate::load_balancer::{LoadBalancerRequest, input_work_units};
use crate::routing_state::RoutedClusterSnapshot;
use stargate_protocol::common::valid_last_mean_input_tps;

use super::GroqMultiregionConfig;

#[derive(Clone, Copy, Debug)]
pub(in crate::load_balancer) struct TtftEstimate {
    pub(in crate::load_balancer) queue_ms: f64,
    pub(in crate::load_balancer) ttft_ms: f64,
}

pub(super) struct CandidateEstimateAccumulator<'a> {
    estimates: Vec<(&'a RoutedClusterSnapshot, TtftEstimate)>,
    input_tokens: Option<u64>,
    priority: u32,
    max_queue_time_ms: Option<f64>,
    ignore_queue_time: bool,
    ignore_input_processing_time: bool,
    fastest_ttft: f64,
    slowest_ttft: f64,
    all_estimates_finite: bool,
}

impl<'a> CandidateEstimateAccumulator<'a> {
    pub(super) fn new(
        config: &GroqMultiregionConfig,
        request: &LoadBalancerRequest<'_>,
        candidate_capacity: usize,
    ) -> Self {
        let max_queue_time_ms = config
            .max_queue_time(request)
            .map(|duration| duration.as_secs_f64() * 1000.0);

        Self {
            estimates: Vec::with_capacity(candidate_capacity),
            input_tokens: request.input_tokens,
            priority: request.priority,
            max_queue_time_ms,
            ignore_queue_time: config.ignore_queue_time,
            ignore_input_processing_time: config.ignore_input_processing_time,
            fastest_ttft: f64::INFINITY,
            slowest_ttft: f64::NEG_INFINITY,
            all_estimates_finite: true,
        }
    }

    pub(super) fn filters_by_queue_slo(&self) -> bool {
        self.max_queue_time_ms.is_some()
    }

    pub(super) fn push_estimate(&mut self, candidate: &'a RoutedClusterSnapshot) {
        let estimate = estimate_ttft_ms(
            candidate,
            self.input_tokens,
            self.priority,
            self.ignore_queue_time,
            self.ignore_input_processing_time,
        );
        if !within_queue_slo(&estimate, self.max_queue_time_ms) {
            return;
        }

        if estimate.ttft_ms.is_finite() {
            self.fastest_ttft = self.fastest_ttft.min(estimate.ttft_ms);
            self.slowest_ttft = self.slowest_ttft.max(estimate.ttft_ms);
        } else {
            self.all_estimates_finite = false;
        }
        self.estimates.push((candidate, estimate));
    }

    pub(super) fn is_empty(&self) -> bool {
        self.estimates.is_empty()
    }

    pub(super) fn has_finite_fastest_ttft(&self) -> bool {
        self.fastest_ttft.is_finite()
    }

    pub(super) fn all_estimates_in_first_bucket(&self, bucket_size_ms: f64) -> bool {
        self.all_estimates_finite && self.slowest_ttft - self.fastest_ttft <= bucket_size_ms
    }

    pub(super) fn into_estimates(self) -> Vec<(&'a RoutedClusterSnapshot, TtftEstimate)> {
        self.estimates
    }
}

pub(super) fn compare_least_queue_time(
    candidate_a: &RoutedClusterSnapshot,
    estimate_a: &TtftEstimate,
    candidate_b: &RoutedClusterSnapshot,
    estimate_b: &TtftEstimate,
) -> Ordering {
    match estimate_a.queue_ms.total_cmp(&estimate_b.queue_ms) {
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

fn within_queue_slo(estimate: &TtftEstimate, max_queue_time_ms: Option<f64>) -> bool {
    max_queue_time_ms.is_none_or(|max_queue_time_ms| estimate.queue_ms <= max_queue_time_ms)
}

pub(in crate::load_balancer) fn estimate_ttft_ms(
    candidate: &RoutedClusterSnapshot,
    input_tokens: Option<u64>,
    priority: u32,
    ignore_queue_time: bool,
    ignore_input_processing_time: bool,
) -> TtftEstimate {
    let input_tokens = input_tokens.unwrap_or(0) as f64;
    let effective_input_tps = effective_input_tps(candidate);
    let queue_ms = estimate_queue_delay_ms(candidate, priority, effective_input_tps);
    let prefill_ms = estimate_processing_delay_ms(input_tokens, effective_input_tps);
    let rtt_ms = rtt_ms(candidate);
    let ttft_ms = rtt_ms
        + if ignore_queue_time { 0.0 } else { queue_ms }
        + if ignore_input_processing_time {
            0.0
        } else {
            prefill_ms
        };

    TtftEstimate { queue_ms, ttft_ms }
}

pub(super) fn estimate_queue_comparison(
    candidate: &RoutedClusterSnapshot,
    priority: u32,
) -> TtftEstimate {
    let effective_input_tps = effective_input_tps(candidate);
    TtftEstimate {
        queue_ms: estimate_queue_delay_ms(candidate, priority, effective_input_tps),
        ttft_ms: rtt_ms(candidate),
    }
}

pub(super) fn queue_ignored_ttft_ms(candidate: &RoutedClusterSnapshot, input_tokens: f64) -> f64 {
    rtt_ms(candidate) + estimate_processing_delay_ms(input_tokens, effective_input_tps(candidate))
}

pub(super) fn rtt_ms(candidate: &RoutedClusterSnapshot) -> f64 {
    candidate.rtt.as_secs_f64() * 1000.0
}

fn estimate_queue_delay_ms(
    candidate: &RoutedClusterSnapshot,
    priority: u32,
    effective_input_tps: f64,
) -> f64 {
    if let Some(queue_time_ms) =
        crate::queue_estimate::queue_time_estimate_ms_for_priority(&candidate.stats, priority)
    {
        return queue_time_ms as f64;
    }

    estimate_processing_delay_ms(input_work_units(candidate), effective_input_tps)
}

fn effective_input_tps(candidate: &RoutedClusterSnapshot) -> f64 {
    if valid_last_mean_input_tps(candidate.stats.last_mean_input_tps) {
        candidate.stats.last_mean_input_tps
    } else {
        0.0
    }
}

fn estimate_processing_delay_ms(work_units: f64, rate: f64) -> f64 {
    if work_units == 0.0 {
        return 0.0;
    }
    if rate <= 0.0 {
        return f64::INFINITY;
    }

    (work_units / rate) * 1000.0
}

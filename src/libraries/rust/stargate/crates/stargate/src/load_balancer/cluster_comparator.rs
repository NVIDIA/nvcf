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
use std::fmt;

use serde::Deserialize;
use stargate_protocol::common::valid_last_mean_input_tps;

use super::{LoadBalancerRequest, input_work_units};
use crate::routing_state::RoutedClusterSnapshot;

#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq)]
#[serde(rename_all = "kebab-case")]
pub enum ClusterComparator {
    #[default]
    Ttft,
    QueueTime,
    InputWorkSeconds,
    Utilization,
    NumRequestsQueued,
}

impl fmt::Display for ClusterComparator {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(self.as_str())
    }
}

impl ClusterComparator {
    pub(crate) const fn as_str(self) -> &'static str {
        match self {
            Self::Ttft => "ttft",
            Self::QueueTime => "queue-time",
            Self::InputWorkSeconds => "input-work-seconds",
            Self::Utilization => "utilization",
            Self::NumRequestsQueued => "num-requests-queued",
        }
    }

    #[inline]
    pub(crate) fn compare(
        self,
        request: &LoadBalancerRequest<'_>,
        candidate_a: &RoutedClusterSnapshot,
        candidate_b: &RoutedClusterSnapshot,
    ) -> Ordering {
        match self {
            Self::Ttft => ttft_ms(candidate_a, request).total_cmp(&ttft_ms(candidate_b, request)),
            Self::QueueTime => queue_delay_ms(candidate_a, request.priority)
                .total_cmp(&queue_delay_ms(candidate_b, request.priority)),
            Self::InputWorkSeconds => input_work_seconds(candidate_a, request.input_tokens)
                .total_cmp(&input_work_seconds(candidate_b, request.input_tokens)),
            Self::Utilization => utilization(candidate_a).total_cmp(&utilization(candidate_b)),
            Self::NumRequestsQueued => candidate_a
                .stats
                .queue_size
                .cmp(&candidate_b.stats.queue_size),
        }
    }
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct Ttft {
    pub(crate) queue_ms: f64,
    pub(crate) ttft_ms: f64,
}

#[inline]
pub(crate) fn ttft(
    candidate: &RoutedClusterSnapshot,
    input_tokens: Option<u64>,
    priority: u32,
    ignore_queue_time: bool,
    ignore_input_processing_time: bool,
) -> Ttft {
    let input_tps = input_tps(candidate);
    let queue_ms = queue_delay_ms_with_tps(candidate, priority, input_tps);
    let prefill_ms = processing_delay_ms(input_tokens.unwrap_or_default() as f64, input_tps);
    let ttft_ms = rtt_ms(candidate)
        + if ignore_queue_time { 0.0 } else { queue_ms }
        + if ignore_input_processing_time {
            0.0
        } else {
            prefill_ms
        };

    Ttft { queue_ms, ttft_ms }
}

#[inline]
pub(crate) fn queue_ignored_ttft_ms(candidate: &RoutedClusterSnapshot, input_tokens: f64) -> f64 {
    rtt_ms(candidate) + processing_delay_ms(input_tokens, input_tps(candidate))
}

#[inline]
pub(crate) fn rtt_ms(candidate: &RoutedClusterSnapshot) -> f64 {
    candidate.rtt.as_secs_f64() * 1000.0
}

fn ttft_ms(candidate: &RoutedClusterSnapshot, request: &LoadBalancerRequest<'_>) -> f64 {
    ttft(
        candidate,
        request.input_tokens,
        request.priority,
        false,
        false,
    )
    .ttft_ms
}

fn queue_delay_ms(candidate: &RoutedClusterSnapshot, priority: u32) -> f64 {
    queue_delay_ms_with_tps(candidate, priority, input_tps(candidate))
}

fn queue_delay_ms_with_tps(
    candidate: &RoutedClusterSnapshot,
    priority: u32,
    input_tps: f64,
) -> f64 {
    crate::queue_estimate::queue_time_estimate_ms_for_priority(&candidate.stats, priority)
        .map_or_else(
            || processing_delay_ms(input_work_units(candidate), input_tps),
            |queue_time_ms| queue_time_ms as f64,
        )
}

fn input_work_seconds(candidate: &RoutedClusterSnapshot, request_input_tokens: Option<u64>) -> f64 {
    processing_delay_seconds(
        input_work_units(candidate) + request_input_tokens.unwrap_or_default() as f64,
        input_tps(candidate),
    )
}

fn utilization(candidate: &RoutedClusterSnapshot) -> f64 {
    candidate.stats.num_running_queries as f64
        / candidate.stats.max_engine_concurrency.max(1) as f64
}

fn input_tps(candidate: &RoutedClusterSnapshot) -> f64 {
    if valid_last_mean_input_tps(candidate.stats.last_mean_input_tps) {
        candidate.stats.last_mean_input_tps
    } else {
        0.0
    }
}

fn processing_delay_ms(work: f64, rate: f64) -> f64 {
    processing_delay_seconds(work, rate) * 1000.0
}

fn processing_delay_seconds(work: f64, rate: f64) -> f64 {
    if work == 0.0 {
        return 0.0;
    }
    if rate <= 0.0 {
        return f64::INFINITY;
    }
    work / rate
}

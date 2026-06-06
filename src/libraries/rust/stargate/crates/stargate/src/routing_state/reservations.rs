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

use super::*;
use stargate_protocol::common::queue_time_delta_ms;

#[derive(Clone, Debug)]
pub(super) struct PendingClusterReservation {
    pub(super) inference_server_id: String,
    pub(super) input_tokens: u64,
    pub(super) priority: u32,
}

#[derive(Clone, Copy, Debug)]
pub(crate) struct RoutingReservation {
    pub(super) id: u64,
}

#[derive(Debug, Default)]
pub(super) struct RoutingReservationState {
    next_id: AtomicU64,
}

impl RoutingReservationState {
    pub(super) fn next(&self) -> RoutingReservation {
        RoutingReservation {
            id: self.next_id.fetch_add(1, Ordering::Relaxed),
        }
    }
}

pub(super) fn update_reserved_priority_queue_time(
    stats: &mut ModelStats,
    input_tokens: u64,
    priority: u32,
) {
    if stats.queue_time_estimate_ms_by_priority.is_empty() {
        return;
    }

    let Some(delta_ms) = queue_time_delta_ms(input_tokens, stats.last_mean_input_tps) else {
        stats.queue_time_estimate_ms_by_priority.clear();
        return;
    };

    let pre_reservation_estimate_ms =
        crate::queue_estimate::priority_map_estimate_ms_for_priority(stats, priority)
            .unwrap_or_default();
    let estimate = stats
        .queue_time_estimate_ms_by_priority
        .entry(priority)
        .or_insert(pre_reservation_estimate_ms);
    *estimate = estimate.saturating_add(delta_ms);
    for (candidate_priority, estimate_ms) in &mut stats.queue_time_estimate_ms_by_priority {
        if *candidate_priority <= priority {
            continue;
        }
        // Queue-time estimates are advisory routing stats; saturate rather than wrap on extreme input.
        *estimate_ms = estimate_ms.saturating_add(delta_ms);
    }
}

pub(super) fn apply_pending_cluster_reservations(
    stats: &mut ModelStats,
    pending_cluster_reservations: &BTreeMap<u64, PendingClusterReservation>,
) {
    for pending in pending_cluster_reservations.values() {
        // Pending reservations are advisory routing stats; saturate rather than wrap.
        stats.queue_size = stats.queue_size.saturating_add(1);
        stats.queued_input_size = stats.queued_input_size.saturating_add(pending.input_tokens);
        stats.num_running_queries = stats.num_running_queries.saturating_add(1);
        stats.total_query_input_size = stats
            .total_query_input_size
            .saturating_add(pending.input_tokens);
        update_reserved_priority_queue_time(stats, pending.input_tokens, pending.priority);
    }
}

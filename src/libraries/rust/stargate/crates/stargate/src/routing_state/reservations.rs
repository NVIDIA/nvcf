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
use crate::metrics::StargateMetrics;
use stargate_protocol::common::queue_time_delta_ms;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Instant;

#[derive(Debug)]
pub(super) struct PendingClusterReservation {
    pub(super) inference_server_id: String,
    pub(super) registration: Option<Arc<RegistrationGeneration>>,
    pub(super) input_tokens: u64,
    pub(super) priority: u32,
    expires_at: Instant,
    active: AtomicBool,
    metrics: Option<Arc<StargateMetrics>>,
    routing_key: Option<String>,
    model: String,
}

impl PendingClusterReservation {
    pub(super) fn new(
        registration: Arc<RegistrationGeneration>,
        input_tokens: u64,
        priority: u32,
        ttl: Duration,
        metrics: Option<Arc<StargateMetrics>>,
        target: &RoutingTargetKey,
    ) -> Arc<Self> {
        Self::new_at(
            Some(registration),
            input_tokens,
            priority,
            Instant::now(),
            ttl,
            metrics,
            target,
        )
    }

    fn new_at(
        registration: Option<Arc<RegistrationGeneration>>,
        input_tokens: u64,
        priority: u32,
        now: Instant,
        ttl: Duration,
        metrics: Option<Arc<StargateMetrics>>,
        target: &RoutingTargetKey,
    ) -> Arc<Self> {
        let inference_server_id = registration
            .as_ref()
            .map_or_else(String::new, |registration| {
                registration.inference_server_id().to_string()
            });
        if let Some(metrics) = &metrics {
            metrics
                .inc_routing_reservations_active(target.routing_key.as_deref(), &target.model_id);
            metrics
                .routing_reservation_ttl_seconds(target.routing_key.as_deref(), &target.model_id)
                .observe(ttl.as_secs_f64());
        }
        Arc::new(Self {
            inference_server_id,
            registration,
            input_tokens,
            priority,
            expires_at: now + ttl,
            active: AtomicBool::new(true),
            metrics,
            routing_key: target.routing_key.clone(),
            model: target.model_id.clone(),
        })
    }

    pub(super) fn is_active(&self) -> bool {
        self.active.load(Ordering::Acquire)
    }

    #[cfg(test)]
    pub(super) fn expires_at(&self) -> Instant {
        self.expires_at
    }

    pub(super) fn is_active_at(&self, now: Instant) -> bool {
        if now >= self.expires_at {
            self.deactivate();
        }
        self.is_active()
    }

    pub(super) fn deactivate(&self) {
        if self
            .active
            .compare_exchange(true, false, Ordering::AcqRel, Ordering::Acquire)
            .is_ok()
            && let Some(metrics) = &self.metrics
        {
            metrics.dec_routing_reservations_active(self.routing_key.as_deref(), &self.model);
        }
    }
}

impl Drop for PendingClusterReservation {
    fn drop(&mut self) {
        self.deactivate();
    }
}

// Cancellation is explicit because a successful attempt remains pending until its TTL expires.
#[derive(Debug)]
pub(crate) struct RoutingReservation(pub(super) Arc<PendingClusterReservation>);

impl RoutingReservation {
    pub(crate) fn release(self) {
        self.0.deactivate();
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
        if *candidate_priority > priority {
            // Queue-time estimates are advisory routing stats; saturate rather than wrap.
            *estimate_ms = estimate_ms.saturating_add(delta_ms);
        }
    }
}

pub(super) fn apply_pending_cluster_reservations(
    stats: &mut ModelStats,
    pending_cluster_reservations: &[Arc<PendingClusterReservation>],
) {
    for pending in pending_cluster_reservations
        .iter()
        .filter(|pending| pending.is_active())
    {
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

#[cfg(test)]
mod tests {
    use prometheus::Encoder;

    use std::collections::HashMap;

    use super::*;

    #[test]
    fn filling_last_slot_restores_queue_estimate_and_release_frees_it() {
        for priorities in [HashMap::new(), HashMap::from([(0, 10_000)])] {
            let base = ModelStats {
                last_mean_input_tps: 8_000.0,
                queued_input_size: 80_000,
                num_running_queries: 24,
                max_engine_concurrency: 25,
                queue_time_estimate_ms_by_priority: priorities,
                ..Default::default()
            };
            let estimate = |stats: &ModelStats| {
                crate::queue_estimate::queue_time_estimate_ms_for_priority(stats, 0)
            };
            assert_eq!(estimate(&base), Some(0));
            let target = RoutingTargetKey::new(None, "model");
            let pending = PendingClusterReservation::new_at(
                None,
                8_000,
                0,
                Instant::now(),
                Duration::from_millis(1),
                None,
                &target,
            );
            let reservations = [pending.clone()];
            let mut full = base.clone();
            apply_pending_cluster_reservations(&mut full, &reservations);
            assert_eq!(full.num_running_queries, 25);
            assert_eq!(full.queued_input_size, 88_000);
            assert_eq!(estimate(&full), Some(11_000));
            RoutingReservation(pending).release();
            let mut recovered = base;
            apply_pending_cluster_reservations(&mut recovered, &reservations);
            assert_eq!(estimate(&recovered), Some(0));
        }
    }

    #[test]
    fn reservation_expires_at_its_fixed_ttl_boundary() {
        let target = RoutingTargetKey::new(Some("routing".to_string()), "model");
        let dispatched_at = Instant::now();
        let pending = PendingClusterReservation::new_at(
            None,
            10,
            0,
            dispatched_at,
            Duration::from_millis(60),
            None,
            &target,
        );

        assert!(pending.is_active_at(dispatched_at + Duration::from_millis(59)));
        assert!(!pending.is_active_at(dispatched_at + Duration::from_millis(60)));
        assert!(!pending.is_active());
    }

    #[test]
    fn reservation_metrics_decrement_once_on_release() {
        let target = RoutingTargetKey::new(Some("routing".to_string()), "model");
        let metrics = StargateMetrics::new().expect("metrics should initialize");
        let pending = PendingClusterReservation::new_at(
            None,
            1,
            0,
            Instant::now(),
            Duration::from_millis(60),
            Some(metrics.clone()),
            &target,
        );
        let active_gauge = || {
            let mut encoded = Vec::new();
            prometheus::TextEncoder::new()
                .encode(&metrics.registry().gather(), &mut encoded)
                .expect("metrics should encode");
            String::from_utf8(encoded).expect("metrics should be UTF-8")
        };

        assert!(active_gauge().contains(
            "stargate_routing_reservations_active{model=\"model\",routing_key=\"routing\"} 1"
        ));
        pending.deactivate();
        pending.deactivate();
        assert!(active_gauge().contains(
            "stargate_routing_reservations_active{model=\"model\",routing_key=\"routing\"} 0"
        ));
    }
}

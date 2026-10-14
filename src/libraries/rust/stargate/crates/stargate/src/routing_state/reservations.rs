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
use std::sync::OnceLock;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Instant;

/// Long enough that no test reservation's soft expiry passes on wall-clock
/// time while a test runs. `Duration::MAX` would overflow `Instant + Duration`.
#[cfg(test)]
pub(super) const UNEXPIRING_TEST_RESERVATION_TTL: Duration = Duration::from_secs(600);

#[derive(Debug)]
pub(super) struct PendingClusterReservation {
    pub(super) inference_server_id: String,
    pub(super) registration: Option<Arc<RegistrationGeneration>>,
    pub(super) input_tokens: u64,
    pub(super) priority: u32,
    ttl: Duration,
    // Soft expiry: a registration update from the reserved backend received
    // at or after this instant ends the reservation. Time alone never does,
    // so a backend that stops reporting keeps its reservations until it is
    // removed. Unset until the request is handed to the tunnel, so no update
    // can settle a request that has not left Stargate. `Some(None)` when the
    // TTL is too large to represent as an `Instant`; such a reservation only
    // ends by release or backend removal.
    expires_at: OnceLock<Option<Instant>>,
    active: AtomicBool,
    metrics: Option<Arc<StargateMetrics>>,
    routing_key: Option<String>,
    model: String,
}

impl PendingClusterReservation {
    /// Creates a counted reservation whose soft expiry starts when it is
    /// armed.
    pub(super) fn new_unarmed(
        registration: Option<Arc<RegistrationGeneration>>,
        input_tokens: u64,
        priority: u32,
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
            ttl,
            expires_at: OnceLock::new(),
            active: AtomicBool::new(true),
            metrics,
            routing_key: target.routing_key.clone(),
            model: target.model_id.clone(),
        })
    }

    /// Creates a reservation already armed at `now`.
    #[cfg(test)]
    pub(super) fn new_at(
        registration: Option<Arc<RegistrationGeneration>>,
        input_tokens: u64,
        priority: u32,
        now: Instant,
        ttl: Duration,
        metrics: Option<Arc<StargateMetrics>>,
        target: &RoutingTargetKey,
    ) -> Arc<Self> {
        let pending = Self::new_unarmed(registration, input_tokens, priority, ttl, metrics, target);
        pending.arm_at(now);
        pending
    }

    pub(super) fn is_active(&self) -> bool {
        self.active.load(Ordering::Acquire)
    }

    /// Starts the soft expiry at `now`. Only the first call takes effect.
    pub(super) fn arm_at(&self, now: Instant) {
        let _ = self.expires_at.set(now.checked_add(self.ttl));
    }

    /// Ends the reservation when a registration update from its own backend,
    /// received at `updated_at`, arrives at or after the soft expiry. Receipt
    /// time is a settlement threshold, not proof that the update includes the
    /// request. Earlier updates, and any update before the reservation is
    /// armed, leave it counting.
    pub(super) fn settle_on_backend_update(&self, updated_at: Instant) {
        if let Some(Some(expires_at)) = self.expires_at.get()
            && updated_at >= *expires_at
        {
            self.deactivate();
        }
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

// Cancellation is explicit because a successful attempt remains pending until
// its backend reports at or after the soft expiry.
#[derive(Debug)]
pub(crate) struct RoutingReservation(pub(super) Arc<PendingClusterReservation>);

impl RoutingReservation {
    /// Starts the soft expiry once the request has been handed to the tunnel.
    /// Later calls keep the first arm time.
    pub(crate) fn arm(&self) {
        self.0.arm_at(Instant::now());
    }

    pub(crate) fn release(self) {
        self.0.deactivate();
    }
}

impl Drop for RoutingReservation {
    // An attempt that ends before sending its request, including by
    // cancellation, arms the reservation here so it still ends on the
    // backend's next update after the TTL.
    fn drop(&mut self) {
        self.arm();
    }
}

/// The soft TTL of a reservation on a backend whose health-check RTT is `rtt`.
pub fn routing_reservation_ttl(rtt: Duration, min: Duration, max: Duration) -> Duration {
    // Startup rejects min > max; `Duration::clamp` would panic on every
    // dispatch if a programmatic config bypassed that check, so max wins.
    rtt.max(min).min(max)
}

/// A routing reservation that the caller holds instead of a routed cluster,
/// for simulators that run load balancers outside `StargateState`. It follows
/// the proxy's rules: it counts from creation, its soft expiry starts when it
/// is armed, and it ends only when released or when an update from its own
/// backend is received at or after the soft expiry. The caller routes each
/// update from the reserved backend to it.
#[derive(Debug)]
pub struct DetachedRoutingReservation(Arc<PendingClusterReservation>);

impl DetachedRoutingReservation {
    pub fn new(input_tokens: u64, priority: u32, ttl: Duration, target: &RoutingTargetKey) -> Self {
        Self(PendingClusterReservation::new_unarmed(
            None,
            input_tokens,
            priority,
            ttl,
            None,
            target,
        ))
    }

    /// Starts the soft expiry once the request head is sent. Only the first
    /// call takes effect.
    pub fn arm_at(&self, sent_at: Instant) {
        self.0.arm_at(sent_at);
    }

    /// Applies an update from the reserved backend received at `received_at`.
    pub fn settle_on_backend_update(&self, received_at: Instant) {
        self.0.settle_on_backend_update(received_at);
    }

    /// Ends the reservation early, as a queue-mismatch rejection does.
    pub fn release(&self) {
        self.0.deactivate();
    }

    pub fn is_active(&self) -> bool {
        self.0.is_active()
    }

    /// Adds the request to `stats` while the reservation is active.
    pub fn apply(&self, stats: &mut ModelStats) {
        apply_pending_cluster_reservations(stats, std::slice::from_ref(&self.0));
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
        apply_reservation(stats, pending.input_tokens, pending.priority);
    }
}

/// Counts one routed request that the backend's stats do not yet include.
pub fn apply_reservation(stats: &mut ModelStats, input_tokens: u64, priority: u32) {
    // Pending reservations are advisory routing stats; saturate rather than wrap.
    stats.queue_size = stats.queue_size.saturating_add(1);
    stats.queued_input_size = stats.queued_input_size.saturating_add(input_tokens);
    stats.num_running_queries = stats.num_running_queries.saturating_add(1);
    stats.total_query_input_size = stats.total_query_input_size.saturating_add(input_tokens);
    update_reserved_priority_queue_time(stats, input_tokens, priority);
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
    fn soft_expiry_waits_for_a_backend_update_at_or_after_it() {
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
        let at = |millis| dispatched_at + Duration::from_millis(millis);

        pending.settle_on_backend_update(at(59));
        assert!(
            pending.is_active(),
            "an update received before the soft expiry cannot include the request"
        );
        pending.settle_on_backend_update(at(60));
        assert!(!pending.is_active());
    }

    #[test]
    fn detached_reservation_follows_the_proxy_lifecycle() {
        let target = RoutingTargetKey::new(None, "model");
        let ttl = routing_reservation_ttl(
            Duration::from_millis(60),
            Duration::from_millis(1),
            Duration::from_millis(1000),
        );
        let reservation = DetachedRoutingReservation::new(100, 0, ttl, &target);
        let reserved_stats = |reservation: &DetachedRoutingReservation| {
            let mut stats = ModelStats::default();
            reservation.apply(&mut stats);
            (stats.queue_size, stats.queued_input_size)
        };
        let sent_at = Instant::now();

        reservation.settle_on_backend_update(sent_at + Duration::from_secs(3600));
        assert_eq!(reserved_stats(&reservation), (1, 100), "unarmed");
        reservation.arm_at(sent_at);
        reservation.settle_on_backend_update(sent_at + Duration::from_millis(59));
        assert!(reservation.is_active());
        reservation.settle_on_backend_update(sent_at + Duration::from_millis(60));
        assert!(!reservation.is_active());
        assert_eq!(reserved_stats(&reservation), (0, 0));

        let released = DetachedRoutingReservation::new(100, 0, ttl, &target);
        released.release();
        assert_eq!(reserved_stats(&released), (0, 0));
    }

    #[test]
    fn unarmed_reservation_is_not_settled_by_any_update() {
        let target = RoutingTargetKey::new(Some("routing".to_string()), "model");
        let pending = PendingClusterReservation::new_unarmed(
            None,
            10,
            0,
            Duration::from_millis(1),
            None,
            &target,
        );

        pending.settle_on_backend_update(Instant::now() + Duration::from_secs(3600));
        assert!(
            pending.is_active(),
            "a request that has not left Stargate cannot appear in any update"
        );
    }

    #[test]
    fn soft_expiry_starts_when_the_reservation_is_armed() {
        let target = RoutingTargetKey::new(Some("routing".to_string()), "model");
        let reserved_at = Instant::now();
        let pending = PendingClusterReservation::new_unarmed(
            None,
            10,
            0,
            Duration::from_millis(60),
            None,
            &target,
        );
        let armed_at = reserved_at + Duration::from_millis(100);
        let after_arm = |millis| armed_at + Duration::from_millis(millis);

        pending.settle_on_backend_update(armed_at);
        assert!(pending.is_active());
        pending.arm_at(armed_at);
        pending.arm_at(after_arm(1000));
        pending.settle_on_backend_update(after_arm(59));
        assert!(
            pending.is_active(),
            "an update before arm time plus TTL cannot include the request"
        );
        pending.settle_on_backend_update(after_arm(60));
        assert!(!pending.is_active(), "the first arm time must win");
    }

    #[test]
    fn dropping_an_unarmed_token_arms_its_reservation() {
        let target = RoutingTargetKey::new(Some("routing".to_string()), "model");
        let metrics = StargateMetrics::new().expect("metrics should initialize");
        let ttl = Duration::from_millis(60);
        let pending =
            PendingClusterReservation::new_unarmed(None, 1, 0, ttl, Some(metrics.clone()), &target);
        let before_drop = Instant::now();

        drop(RoutingReservation(Arc::clone(&pending)));
        assert!(active_reservation_gauge(&metrics).ends_with(" 1"));
        pending.settle_on_backend_update(before_drop);
        assert!(pending.is_active());
        pending.settle_on_backend_update(Instant::now() + ttl);
        assert!(!pending.is_active());
        assert!(active_reservation_gauge(&metrics).ends_with(" 0"));
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

    fn active_reservation_gauge(metrics: &StargateMetrics) -> String {
        let mut encoded = Vec::new();
        prometheus::TextEncoder::new()
            .encode(&metrics.registry().gather(), &mut encoded)
            .expect("metrics should encode");
        String::from_utf8(encoded)
            .expect("metrics should be UTF-8")
            .lines()
            .find(|line| line.starts_with("stargate_routing_reservations_active{"))
            .expect("reservation gauge series should exist")
            .to_string()
    }

    #[test]
    fn dropping_last_reservation_owner_decrements_active_gauge() {
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
        let token = RoutingReservation(Arc::clone(&pending));
        assert!(active_reservation_gauge(&metrics).ends_with(" 1"));

        drop(pending);
        assert!(
            active_reservation_gauge(&metrics).ends_with(" 1"),
            "a remaining owner keeps the reservation active"
        );
        drop(token);
        assert!(
            active_reservation_gauge(&metrics).ends_with(" 0"),
            "dropping the last owner must settle the gauge"
        );
    }

    #[test]
    fn unrepresentable_ttl_does_not_panic_or_leak_the_active_gauge() {
        let target = RoutingTargetKey::new(Some("routing".to_string()), "model");
        let metrics = StargateMetrics::new().expect("metrics should initialize");
        let dispatched_at = Instant::now();
        let pending = PendingClusterReservation::new_at(
            None,
            1,
            0,
            dispatched_at,
            Duration::MAX,
            Some(metrics.clone()),
            &target,
        );

        assert!(active_reservation_gauge(&metrics).ends_with(" 1"));
        pending.settle_on_backend_update(dispatched_at + Duration::from_secs(365 * 24 * 60 * 60));
        assert!(
            pending.is_active(),
            "an unrepresentable soft expiry must not settle on an update"
        );
        RoutingReservation(pending).release();
        assert!(active_reservation_gauge(&metrics).ends_with(" 0"));
    }
}

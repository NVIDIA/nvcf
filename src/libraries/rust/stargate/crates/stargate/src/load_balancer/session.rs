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

//! Session-aware affinity for `last_cluster_affinity`.
//!
//! The gateway sends the cluster that served a session's last request in
//! `x-stargate-last-cluster-id`. A request without that hint is a new session:
//! it skips the affinity wait and the affinity prefill discount. A request
//! whose hint names a current candidate is a returning session: its last
//! cluster moves to the front of the affinity order, and the configured wait,
//! discount, and widening apply unchanged.

use std::borrow::Cow;
use std::sync::OnceLock;
use std::time::Duration;

use tracing::Span;

use super::{LoadBalancerAlgorithmConfig, LoadBalancerRequest};
use crate::metrics::StargateMetrics;
use crate::routing_state::{RoutedClusterSnapshot, RoutingTargetKey};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SessionState {
    /// No usable last-cluster hint.
    New,
    /// The hint names a current candidate.
    Returning,
    /// The hint names a cluster outside the candidate set. Routed as `New`.
    Stale,
}

impl SessionState {
    pub const ALL: [Self; 3] = [Self::New, Self::Returning, Self::Stale];

    pub fn as_str(self) -> &'static str {
        match self {
            Self::New => "new",
            Self::Returning => "returning",
            Self::Stale => "stale",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SessionSelection {
    Primary,
    Fallback,
}

impl SessionSelection {
    pub const ALL: [Self; 2] = [Self::Primary, Self::Fallback];

    pub fn as_str(self) -> &'static str {
        match self {
            Self::Primary => "primary",
            Self::Fallback => "fallback",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SessionClassification {
    pub state: SessionState,
    /// The last cluster moved ahead of the original first affinity position.
    pub promoted: bool,
}

/// Per-request last-cluster hint. The first load-balancer decision classifies
/// the request; retries and timed waits reuse that classification.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct LastClusterHint {
    last_cluster_id: Option<String>,
    classification: OnceLock<SessionClassification>,
}

impl LastClusterHint {
    pub fn new(last_cluster_id: Option<String>) -> Self {
        Self {
            last_cluster_id,
            classification: OnceLock::new(),
        }
    }

    pub fn last_cluster_id(&self) -> Option<&str> {
        self.last_cluster_id.as_deref()
    }

    /// `None` until a session-aware load balancer classifies the request.
    pub fn classification(&self) -> Option<SessionClassification> {
        self.classification.get().copied()
    }

    #[cfg(test)]
    pub(crate) fn classified(
        last_cluster_id: Option<&str>,
        classification: SessionClassification,
    ) -> Self {
        let hint = Self::new(last_cluster_id.map(ToOwned::to_owned));
        hint.classification
            .set(classification)
            .expect("a new hint is unclassified");
        hint
    }
}

/// Affinity inputs for one decision after the session rules apply.
#[derive(Debug)]
pub(super) struct AffinityPlan<'a> {
    pub(super) order: Cow<'a, [usize]>,
    pub(super) affinity_wait: Duration,
    pub(super) input_tokens_scale: f64,
}

/// What to do when a returning session's last cluster is a candidate but not
/// in the affinity order.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(super) enum MissingLastCluster {
    /// The order is a fixed-size affinity group: put the last cluster first
    /// and drop the group's last member.
    InsertAndTruncate,
    /// The order is a full ranking that omits the cluster: leave it unchanged.
    Keep,
}

/// Applies the session rules to one decision. The order passed in is the
/// original affinity order; per-key caches are never modified.
pub(super) fn plan_affinity<'a>(
    enabled: bool,
    request: &LoadBalancerRequest<'_>,
    candidates: &[RoutedClusterSnapshot],
    order: Cow<'a, [usize]>,
    missing: MissingLastCluster,
    configured_wait: Duration,
    configured_scale: f64,
) -> AffinityPlan<'a> {
    if !enabled || request.cache_affinity_key.is_none() {
        return AffinityPlan {
            order,
            affinity_wait: configured_wait,
            input_tokens_scale: configured_scale,
        };
    }
    let hint_id = request
        .last_cluster
        .and_then(LastClusterHint::last_cluster_id);
    let last_index = hint_id.and_then(|id| {
        candidates
            .iter()
            .position(|candidate| candidate.cluster_id == id)
    });
    let classify = || match (hint_id, last_index) {
        (None, _) => SessionClassification {
            state: SessionState::New,
            promoted: false,
        },
        (Some(_), None) => SessionClassification {
            state: SessionState::Stale,
            promoted: false,
        },
        (Some(_), Some(last_index)) => SessionClassification {
            state: SessionState::Returning,
            promoted: order.first() != Some(&last_index)
                && (order.contains(&last_index)
                    || missing == MissingLastCluster::InsertAndTruncate),
        },
    };
    let classification = request
        .last_cluster
        .map_or_else(classify, |hint| *hint.classification.get_or_init(classify));

    match classification.state {
        SessionState::New | SessionState::Stale => AffinityPlan {
            order,
            affinity_wait: Duration::ZERO,
            input_tokens_scale: 1.0,
        },
        SessionState::Returning => AffinityPlan {
            // A pinned returning session whose last cluster left the candidate
            // set on a later attempt keeps the original order.
            order: match last_index {
                Some(last_index) => promote(order, last_index, missing),
                None => order,
            },
            affinity_wait: configured_wait,
            input_tokens_scale: configured_scale,
        },
    }
}

fn promote(
    mut order: Cow<'_, [usize]>,
    last_index: usize,
    missing: MissingLastCluster,
) -> Cow<'_, [usize]> {
    match order.iter().position(|index| *index == last_index) {
        Some(0) => {}
        // Rotating the prefix keeps every other member's relative order.
        Some(position) => order.to_mut()[..=position].rotate_right(1),
        None if missing == MissingLastCluster::InsertAndTruncate && !order.is_empty() => {
            let order = order.to_mut();
            order.pop();
            order.insert(0, last_index);
        }
        None => {}
    }
    order
}

/// Records the session metric, span field, and debug log for a selection.
///
/// For a returning session, `primary` means the request went to its last
/// cluster. For new and stale sessions, `primary` means the cluster came from
/// the affinity group (`rank_depth` within the configured group size).
#[allow(clippy::too_many_arguments)]
pub(crate) fn record_session_selection(
    metrics: &StargateMetrics,
    config: &LoadBalancerAlgorithmConfig,
    algorithm: &str,
    request_id: &str,
    target: &RoutingTargetKey,
    hint: &LastClusterHint,
    selected_cluster_id: &str,
    rank_depth: usize,
) {
    let Some(classification) = hint.classification() else {
        return;
    };
    let selection = match classification.state {
        SessionState::Returning => {
            if hint.last_cluster_id() == Some(selected_cluster_id) {
                SessionSelection::Primary
            } else {
                SessionSelection::Fallback
            }
        }
        SessionState::New | SessionState::Stale => {
            let group_size = config
                .wait_and_widen_settings()
                .and_then(|settings| settings.cache_affinity_backend_selection_count)
                .filter(|count| *count > 0)
                .unwrap_or(1);
            if rank_depth <= group_size {
                SessionSelection::Primary
            } else {
                SessionSelection::Fallback
            }
        }
    };

    let routing_key = target.routing_key.as_deref();
    // Routing key and model are dynamic, so every state and selection pair
    // starts at zero the first time a target reports a session selection.
    for state in SessionState::ALL {
        for pair_selection in SessionSelection::ALL {
            metrics
                .routing_session_selections_total(
                    routing_key,
                    &target.model_id,
                    algorithm,
                    state.as_str(),
                    pair_selection.as_str(),
                )
                .inc_by(0);
        }
    }
    metrics
        .routing_session_selections_total(
            routing_key,
            &target.model_id,
            algorithm,
            classification.state.as_str(),
            selection.as_str(),
        )
        .inc();

    Span::current().record("routing.session_state", classification.state.as_str());
    tracing::debug!(
        request_id = %request_id,
        model_id = %target.model_id,
        routing_key = ?target.routing_key,
        algorithm = %algorithm,
        session_state = classification.state.as_str(),
        last_cluster_promoted = classification.promoted,
        selected_cluster_id = %selected_cluster_id,
        selection = selection.as_str(),
        "routing session classified"
    );
}

#[cfg(test)]
mod tests {
    use std::time::Instant;

    use super::*;
    use crate::load_balancer::tests::candidates;
    use crate::load_balancer::{
        LoadBalancerAlgorithm, LoadBalancerAlgorithmSettings, WaitAndWidenAlgorithmConfig,
    };

    const WAIT: Duration = Duration::from_millis(300);
    const SCALE: f64 = 0.1;

    fn target() -> RoutingTargetKey {
        RoutingTargetKey::new(Some("rk-1".to_string()), "model-a")
    }

    fn request<'a>(
        target: &'a RoutingTargetKey,
        cache_affinity_key: Option<&'a str>,
        hint: Option<&'a LastClusterHint>,
    ) -> LoadBalancerRequest<'a> {
        LoadBalancerRequest {
            routing_target: target,
            cache_affinity_key,
            input_tokens: Some(100),
            priority: 0,
            received_at: Instant::now(),
            request_slo: None,
            excluded_cluster_ids: None,
            last_cluster: hint,
        }
    }

    fn plan<'a>(
        enabled: bool,
        request: &LoadBalancerRequest<'_>,
        candidates: &[RoutedClusterSnapshot],
        order: &'a [usize],
        missing: MissingLastCluster,
    ) -> AffinityPlan<'a> {
        plan_affinity(
            enabled,
            request,
            candidates,
            Cow::Borrowed(order),
            missing,
            WAIT,
            SCALE,
        )
    }

    fn five_candidates() -> Vec<RoutedClusterSnapshot> {
        candidates(&["A", "B", "C", "D", "E"])
    }

    fn hint(id: &str) -> LastClusterHint {
        LastClusterHint::new(Some(id.to_string()))
    }

    #[test]
    fn missing_hint_classifies_new_and_drops_wait_and_discount() {
        let target = target();
        let candidates = five_candidates();
        let hint = LastClusterHint::new(None);
        let request = request(&target, Some("key"), Some(&hint));
        let plan = plan(
            true,
            &request,
            &candidates,
            &[0],
            MissingLastCluster::InsertAndTruncate,
        );

        assert_eq!(&*plan.order, &[0]);
        assert_eq!(plan.affinity_wait, Duration::ZERO);
        assert_eq!(plan.input_tokens_scale, 1.0);
        assert_eq!(
            hint.classification(),
            Some(SessionClassification {
                state: SessionState::New,
                promoted: false
            })
        );
    }

    #[test]
    fn unknown_cluster_classifies_stale_and_routes_as_new() {
        let target = target();
        let candidates = five_candidates();
        let hint = hint("gone");
        let request = request(&target, Some("key"), Some(&hint));
        let plan = plan(
            true,
            &request,
            &candidates,
            &[0, 1],
            MissingLastCluster::InsertAndTruncate,
        );

        assert_eq!(&*plan.order, &[0, 1]);
        assert_eq!(plan.affinity_wait, Duration::ZERO);
        assert_eq!(plan.input_tokens_scale, 1.0);
        assert_eq!(
            hint.classification().map(|c| c.state),
            Some(SessionState::Stale)
        );
    }

    #[test]
    fn returning_session_promotes_into_single_member_group() {
        let target = target();
        let candidates = five_candidates();
        let hint = hint("B");
        let request = request(&target, Some("key"), Some(&hint));
        let plan = plan(
            true,
            &request,
            &candidates,
            &[0],
            MissingLastCluster::InsertAndTruncate,
        );

        assert_eq!(&*plan.order, &[1]);
        assert_eq!(plan.affinity_wait, WAIT);
        assert_eq!(plan.input_tokens_scale, SCALE);
        assert_eq!(
            hint.classification(),
            Some(SessionClassification {
                state: SessionState::Returning,
                promoted: true
            })
        );
    }

    #[test]
    fn returning_session_outside_group_replaces_last_member() {
        let target = target();
        let candidates = five_candidates();
        let hint = hint("C");
        let request = request(&target, Some("key"), Some(&hint));
        let plan = plan(
            true,
            &request,
            &candidates,
            &[0, 1],
            MissingLastCluster::InsertAndTruncate,
        );

        assert_eq!(&*plan.order, &[2, 0]);
    }

    #[test]
    fn returning_session_inside_group_keeps_members() {
        let target = target();
        let candidates = five_candidates();
        let hint = hint("B");
        let request = request(&target, Some("key"), Some(&hint));
        let plan = plan(
            true,
            &request,
            &candidates,
            &[0, 1],
            MissingLastCluster::InsertAndTruncate,
        );

        assert_eq!(&*plan.order, &[1, 0]);
    }

    #[test]
    fn returning_session_reorders_full_ranking_and_keeps_relative_order() {
        let target = target();
        let candidates = five_candidates();
        let hint = hint("D");
        let request = request(&target, Some("key"), Some(&hint));
        let plan = plan(
            true,
            &request,
            &candidates,
            &[0, 1, 2, 3, 4],
            MissingLastCluster::Keep,
        );

        assert_eq!(&*plan.order, &[3, 0, 1, 2, 4]);
    }

    #[test]
    fn returning_session_already_first_is_not_promoted() {
        let target = target();
        let candidates = five_candidates();
        let hint = hint("A");
        let request = request(&target, Some("key"), Some(&hint));
        let plan = plan(
            true,
            &request,
            &candidates,
            &[0, 1],
            MissingLastCluster::Keep,
        );

        assert!(matches!(plan.order, Cow::Borrowed(_)));
        assert_eq!(&*plan.order, &[0, 1]);
        assert_eq!(
            hint.classification(),
            Some(SessionClassification {
                state: SessionState::Returning,
                promoted: false
            })
        );
    }

    #[test]
    fn ranking_without_last_cluster_is_kept() {
        let target = target();
        let candidates = five_candidates();
        let hint = hint("E");
        let request = request(&target, Some("key"), Some(&hint));
        let plan = plan(
            true,
            &request,
            &candidates,
            &[0, 1, 2, 3],
            MissingLastCluster::Keep,
        );

        assert_eq!(&*plan.order, &[0, 1, 2, 3]);
        assert_eq!(plan.affinity_wait, WAIT);
        assert_eq!(
            hint.classification(),
            Some(SessionClassification {
                state: SessionState::Returning,
                promoted: false
            })
        );
    }

    #[test]
    fn classification_is_pinned_across_attempts() {
        let target = target();
        let candidates = five_candidates();
        let hint = hint("B");
        let request = request(&target, Some("key"), Some(&hint));
        let _ = plan(
            true,
            &request,
            &candidates,
            &[0],
            MissingLastCluster::InsertAndTruncate,
        );

        // B left the candidate set before a retry: still returning, no promotion.
        let later = crate::load_balancer::tests::candidates(&["A", "C"]);
        let plan = plan(
            true,
            &request,
            &later,
            &[0],
            MissingLastCluster::InsertAndTruncate,
        );
        assert_eq!(&*plan.order, &[0]);
        assert_eq!(plan.affinity_wait, WAIT);
        assert_eq!(plan.input_tokens_scale, SCALE);
        assert_eq!(
            hint.classification().map(|c| c.state),
            Some(SessionState::Returning)
        );
    }

    #[test]
    fn flag_off_or_missing_affinity_key_is_not_classified() {
        let target = target();
        let candidates = five_candidates();
        for (enabled, affinity_key) in [(false, Some("key")), (true, None)] {
            let hint = hint("B");
            let request = request(&target, affinity_key, Some(&hint));
            let plan = plan(
                enabled,
                &request,
                &candidates,
                &[0],
                MissingLastCluster::InsertAndTruncate,
            );

            assert_eq!(&*plan.order, &[0]);
            assert_eq!(plan.affinity_wait, WAIT);
            assert_eq!(plan.input_tokens_scale, SCALE);
            assert_eq!(hint.classification(), None);
        }
    }

    #[test]
    fn request_without_hint_is_new_when_enabled() {
        let target = target();
        let candidates = five_candidates();
        let request = request(&target, Some("key"), None);
        let plan = plan(
            true,
            &request,
            &candidates,
            &[0],
            MissingLastCluster::InsertAndTruncate,
        );

        assert_eq!(plan.affinity_wait, Duration::ZERO);
        assert_eq!(plan.input_tokens_scale, 1.0);
    }

    fn classified_hint(id: Option<&str>, state: SessionState) -> LastClusterHint {
        LastClusterHint::classified(
            id,
            SessionClassification {
                state,
                promoted: false,
            },
        )
    }

    fn session_series(metrics: &StargateMetrics) -> Vec<(String, String, u64)> {
        metrics
            .registry()
            .gather()
            .into_iter()
            .filter(|family| family.name() == "stargate_routing_session_selections_total")
            .flat_map(|family| family.get_metric().to_vec())
            .map(|metric| {
                let label = |name: &str| {
                    metric
                        .get_label()
                        .iter()
                        .find(|label| label.name() == name)
                        .map(|label| label.value().to_string())
                        .unwrap_or_default()
                };
                (
                    label("session_state"),
                    label("selection"),
                    metric.get_counter().value() as u64,
                )
            })
            .collect()
    }

    fn pulsar_wait_and_widen_config(group_size: Option<usize>) -> LoadBalancerAlgorithmConfig {
        LoadBalancerAlgorithmConfig {
            settings: LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(
                WaitAndWidenAlgorithmConfig {
                    cache_affinity_backend_selection_count: group_size,
                    last_cluster_affinity: Some(true),
                    ..WaitAndWidenAlgorithmConfig::default()
                },
            ),
            ..LoadBalancerAlgorithmConfig::from(LoadBalancerAlgorithm::PulsarWaitAndWiden)
        }
    }

    #[test]
    fn session_metric_starts_every_pair_at_zero_and_counts_selection() {
        let metrics = StargateMetrics::new().unwrap();
        let config = pulsar_wait_and_widen_config(None);
        let hint = classified_hint(Some("B"), SessionState::Returning);
        record_session_selection(
            &metrics,
            &config,
            "pulsar-wait-and-widen",
            "req-1",
            &target(),
            &hint,
            "B",
            2,
        );

        let mut series = session_series(&metrics);
        series.sort();
        assert_eq!(series.len(), 6);
        assert_eq!(
            series
                .iter()
                .filter(|(_, _, value)| *value == 1)
                .collect::<Vec<_>>(),
            vec![&("returning".to_string(), "primary".to_string(), 1)]
        );
        assert_eq!(series.iter().map(|(_, _, value)| value).sum::<u64>(), 1);
    }

    #[test]
    fn returning_session_on_other_cluster_counts_fallback() {
        let metrics = StargateMetrics::new().unwrap();
        let config = pulsar_wait_and_widen_config(None);
        let hint = classified_hint(Some("B"), SessionState::Returning);
        record_session_selection(
            &metrics,
            &config,
            "pulsar-wait-and-widen",
            "req-1",
            &target(),
            &hint,
            "A",
            1,
        );

        assert!(session_series(&metrics).contains(&(
            "returning".to_string(),
            "fallback".to_string(),
            1
        )));
    }

    #[test]
    fn new_session_primary_means_affinity_group() {
        let config = pulsar_wait_and_widen_config(Some(2));
        for (state, rank_depth, expected) in [
            (SessionState::New, 1, "primary"),
            (SessionState::New, 2, "primary"),
            (SessionState::New, 3, "fallback"),
            (SessionState::Stale, 3, "fallback"),
        ] {
            let metrics = StargateMetrics::new().unwrap();
            let hint = classified_hint(None, state);
            record_session_selection(
                &metrics,
                &config,
                "pulsar-wait-and-widen",
                "req-1",
                &target(),
                &hint,
                "A",
                rank_depth,
            );
            assert!(
                session_series(&metrics).contains(&(
                    state.as_str().to_string(),
                    expected.to_string(),
                    1
                )),
                "{state:?} at rank {rank_depth}"
            );
        }
    }

    #[test]
    fn unclassified_requests_record_nothing() {
        let metrics = StargateMetrics::new().unwrap();
        let config = pulsar_wait_and_widen_config(None);
        record_session_selection(
            &metrics,
            &config,
            "pulsar-wait-and-widen",
            "req-1",
            &target(),
            &hint("B"),
            "B",
            1,
        );

        assert!(session_series(&metrics).is_empty());
    }
}

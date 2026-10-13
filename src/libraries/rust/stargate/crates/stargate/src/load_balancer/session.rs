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
enum SessionSelection {
    Primary,
    Fallback,
}

impl SessionSelection {
    const ALL: [Self; 2] = [Self::Primary, Self::Fallback];

    fn as_str(self) -> &'static str {
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

/// Records the pinned session state on the current span. A no-op until the
/// first load-balancer decision classifies the request.
pub(crate) fn record_session_state(hint: &LastClusterHint) {
    if let Some(classification) = hint.classification() {
        Span::current().record("routing.session_state", classification.state.as_str());
    }
}

/// Records the session metric and debug log for a selection.
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
    use std::collections::HashSet;
    use std::time::Instant;

    use super::*;
    use crate::load_balancer::pulsar::pulsar_ranked_indices;
    use crate::load_balancer::pulsar_wait_and_widen::PulsarWaitAndWidenLoadBalancer;
    use crate::load_balancer::tests::candidates;
    use crate::load_balancer::wait_and_widen::{
        WaitAndWidenConfig, WaitAndWidenLoadBalancer, cache_affinity_candidate_indices,
    };
    use crate::load_balancer::{
        LoadBalancerAlgorithm, LoadBalancerCandidateChoice, LoadBalancerDecision,
        PulsarRendezvousWeight, WaitAndWidenAlgorithmConfig,
    };
    use LoadBalancerAlgorithm::{PulsarWaitAndWiden, WaitAndWiden};
    use LoadBalancerDecision::{Selected, Unavailable, Wait};
    use MissingLastCluster::{InsertAndTruncate, Keep};

    const WAIT: Duration = Duration::from_millis(300);
    const SCALE: f64 = 0.1;
    const KEY: &str = "session-key";
    const BOTH: [LoadBalancerAlgorithm; 2] = [WaitAndWiden, PulsarWaitAndWiden];

    type Decide = Box<
        dyn Fn(
            &LoadBalancerRequest<'_>,
            &[RoutedClusterSnapshot],
            Duration,
        ) -> LoadBalancerDecision,
    >;

    fn ms(millis: u64) -> Duration {
        Duration::from_millis(millis)
    }

    fn target() -> RoutingTargetKey {
        RoutingTargetKey::new(Some("rk-1".to_string()), "model-a")
    }

    fn hint(id: &str) -> LastClusterHint {
        LastClusterHint::new(Some(id.to_string()))
    }

    fn request<'a>(
        target: &'a RoutingTargetKey,
        cache_affinity_key: Option<&'a str>,
        input_tokens: u64,
        hint: Option<&'a LastClusterHint>,
    ) -> LoadBalancerRequest<'a> {
        LoadBalancerRequest {
            routing_target: target,
            cache_affinity_key,
            input_tokens: Some(input_tokens),
            priority: 0,
            received_at: Instant::now(),
            request_slo: None,
            excluded_cluster_ids: None,
            last_cluster: hint,
        }
    }

    /// X = 300 ms, s = 0.1, k = 1, and sampling that covers every candidate.
    fn config(
        algorithm: LoadBalancerAlgorithm,
        last_cluster_affinity: bool,
        configure: impl FnOnce(&mut WaitAndWidenAlgorithmConfig),
    ) -> LoadBalancerAlgorithmConfig {
        let mut config = LoadBalancerAlgorithmConfig::from(algorithm);
        config.set_seed(Some("seed-1".to_string())).unwrap();
        let settings = config.wait_and_widen_settings_mut().unwrap();
        settings.n = Some(5);
        settings.cache_affinity_backend_selection_count = Some(1);
        settings.cache_affinity_wait_ms = Some(300);
        settings.cache_affinity_input_tokens_scale = Some(SCALE);
        settings.last_cluster_affinity = Some(last_cluster_affinity);
        configure(settings);
        config
    }

    fn balancer(config: LoadBalancerAlgorithmConfig) -> Decide {
        if config.algorithm() == WaitAndWiden {
            let lb = WaitAndWidenLoadBalancer::new(
                WaitAndWidenConfig::from_algorithm_config(&config).unwrap(),
            );
            Box::new(move |request, candidates, elapsed| lb.decide_at(request, candidates, elapsed))
        } else {
            let lb = PulsarWaitAndWidenLoadBalancer::new(config).unwrap();
            Box::new(move |request, candidates, elapsed| lb.decide_at(request, candidates, elapsed))
        }
    }

    /// The original affinity order for `KEY`: the ring group followed by the
    /// other candidates, or the full Pulsar ranking.
    fn affinity_order(
        config: &LoadBalancerAlgorithmConfig,
        candidates: &[RoutedClusterSnapshot],
    ) -> Vec<usize> {
        let target = target();
        let request = request(&target, Some(KEY), 0, None);
        if config.algorithm() == WaitAndWiden {
            let config = WaitAndWidenConfig::from_algorithm_config(config).unwrap();
            let mut order =
                cache_affinity_candidate_indices(&config, &request, candidates).unwrap();
            let rest: Vec<_> = (0..candidates.len())
                .filter(|index| !order.contains(index))
                .collect();
            order.extend(rest);
            order
        } else {
            pulsar_ranked_indices(
                config.seed(),
                PulsarRendezvousWeight::default(),
                &request,
                candidates,
            )
        }
    }

    fn single_slot(count: usize) -> Vec<RoutedClusterSnapshot> {
        let mut candidates = candidates(&["a", "b", "c", "d", "e"][..count]);
        for candidate in &mut candidates {
            candidate.stats.max_engine_concurrency = 1;
        }
        candidates
    }

    fn fill(candidates: &mut [RoutedClusterSnapshot], indices: &[usize]) {
        for index in indices {
            candidates[*index].stats.num_running_queries = 1;
        }
    }

    fn selected(index: usize, rank_depth: usize) -> LoadBalancerDecision {
        Selected(LoadBalancerCandidateChoice {
            candidate_index: index,
            rank_depth,
            selected_after_kv_free_tokens_skip: false,
        })
    }

    fn selected_index(decision: LoadBalancerDecision) -> usize {
        decision
            .selected()
            .unwrap_or_else(|| panic!("expected a selection, got {decision:?}"))
            .candidate_index
    }

    #[test]
    fn plan_affinity_applies_session_rules() {
        use SessionState::{New, Returning, Stale};
        let target = target();
        let candidates = candidates(&["A", "B", "C", "D", "E"]);
        let plan = |enabled, key, hint: Option<&LastClusterHint>, order: &[usize], missing| {
            let request = request(&target, key, 0, hint);
            let plan = plan_affinity(
                enabled,
                &request,
                &candidates,
                Cow::Borrowed(order),
                missing,
                WAIT,
                SCALE,
            );
            let configured = (plan.affinity_wait, plan.input_tokens_scale) == (WAIT, SCALE);
            assert!(
                configured
                    || (plan.affinity_wait, plan.input_tokens_scale) == (Duration::ZERO, 1.0)
            );
            (plan.order.into_owned(), configured)
        };

        for (last_id, order, missing, expected_order, expected) in [
            (None, vec![0], InsertAndTruncate, vec![0], (New, false)),
            (
                Some("gone"),
                vec![0, 1],
                InsertAndTruncate,
                vec![0, 1],
                (Stale, false),
            ),
            (
                Some("B"),
                vec![0],
                InsertAndTruncate,
                vec![1],
                (Returning, true),
            ),
            (
                Some("C"),
                vec![0, 1],
                InsertAndTruncate,
                vec![2, 0],
                (Returning, true),
            ),
            (
                Some("B"),
                vec![0, 1],
                InsertAndTruncate,
                vec![1, 0],
                (Returning, true),
            ),
            (
                Some("D"),
                vec![0, 1, 2, 3, 4],
                Keep,
                vec![3, 0, 1, 2, 4],
                (Returning, true),
            ),
            (Some("A"), vec![0, 1], Keep, vec![0, 1], (Returning, false)),
            (
                Some("E"),
                vec![0, 1, 2, 3],
                Keep,
                vec![0, 1, 2, 3],
                (Returning, false),
            ),
        ] {
            let hint = LastClusterHint::new(last_id.map(str::to_owned));
            let case = format!("{last_id:?} {order:?}");
            let configured = expected.0 == Returning;
            assert_eq!(
                plan(true, Some(KEY), Some(&hint), &order, missing),
                (expected_order, configured),
                "{case}"
            );
            let classification = hint.classification().map(|c| (c.state, c.promoted));
            assert_eq!(classification, Some(expected), "{case}");
        }

        // Flag off or no affinity key: unchanged and unclassified.
        let last = hint("B");
        for (enabled, key) in [(false, Some(KEY)), (true, None)] {
            let planned = plan(enabled, key, Some(&last), &[0], InsertAndTruncate);
            assert_eq!(planned, (vec![0], true));
        }
        assert_eq!(last.classification(), None);
        // A request without a hint, as outside the proxy, routes as new.
        assert_eq!(
            plan(true, Some(KEY), None, &[0], InsertAndTruncate),
            (vec![0], false)
        );
    }

    #[test]
    fn classification_is_pinned_across_attempts() {
        let target = target();
        let hint = hint("B");
        let request = request(&target, Some(KEY), 0, Some(&hint));
        let plan = |candidates: &[RoutedClusterSnapshot]| {
            let plan = plan_affinity(
                true,
                &request,
                candidates,
                Cow::Borrowed(&[0]),
                InsertAndTruncate,
                WAIT,
                SCALE,
            );
            (
                plan.order.into_owned(),
                plan.affinity_wait,
                plan.input_tokens_scale,
            )
        };
        assert_eq!(plan(&candidates(&["A", "B"])), (vec![1], WAIT, SCALE));
        // B left the candidate set before a retry: still returning, original order.
        assert_eq!(plan(&candidates(&["A", "C"])), (vec![0], WAIT, SCALE));
        assert_eq!(
            hint.classification().map(|c| c.state),
            Some(SessionState::Returning)
        );
    }

    /// k = 2. Slower TTFT buckets stay locked past every tested elapsed
    /// time, so no tie breaks at random.
    fn locked_group_of_two(settings: &mut WaitAndWidenAlgorithmConfig) {
        settings.cache_affinity_backend_selection_count = Some(2);
        settings.next_bucket_unlock_factor = Some(100.0);
    }

    #[test]
    fn new_and_stale_sessions_route_like_flag_off_with_no_wait_or_discount() {
        let target = target();
        for algorithm in BOTH {
            let session_config = config(algorithm, true, locked_group_of_two);
            let mut all_free = single_slot(4);
            let order = affinity_order(&session_config, &all_free);
            for (position, rtt_ms) in [120, 80, 40, 160].into_iter().enumerate() {
                all_free[order[position]].rtt = ms(rtt_ms);
            }
            let mut all_full = all_free.clone();
            fill(&mut all_full, &order);
            let mut group_full = all_free.clone();
            fill(&mut group_full, &order[..2]);
            // Full prefill favors order[0] (1500 vs 2001 ms); the 0.1
            // discount favors order[1] (600 vs 201 ms).
            let mut prefill = all_free.clone();
            prefill[order[0]].rtt = ms(500);
            prefill[order[0]].stats.last_mean_input_tps = 1_000.0;
            prefill[order[1]].rtt = ms(1);
            prefill[order[1]].stats.last_mean_input_tps = 500.0;

            let session = balancer(session_config);
            let baseline = balancer(config(algorithm, false, |settings| {
                locked_group_of_two(settings);
                settings.cache_affinity_wait_ms = Some(0);
                settings.cache_affinity_input_tokens_scale = Some(1.0);
            }));
            for snapshot in [&all_free, &all_full, &group_full, &prefill] {
                for elapsed in [0, 50, 300, 1_000].map(ms) {
                    for hint in [LastClusterHint::default(), hint("gone")] {
                        let request = request(&target, Some(KEY), 1_000, Some(&hint));
                        assert_eq!(
                            session(&request, snapshot, elapsed),
                            baseline(&request, snapshot, elapsed),
                            "{algorithm} at {elapsed:?}"
                        );
                    }
                }
            }

            let new = LastClusterHint::default();
            let request = request(&target, Some(KEY), 1_000, Some(&new));
            assert_eq!(
                selected_index(session(&request, &group_full, Duration::ZERO)),
                order[2],
                "{algorithm}"
            );
            assert_eq!(
                selected_index(session(&request, &prefill, Duration::ZERO)),
                order[0],
                "{algorithm}"
            );
        }
    }

    #[test]
    fn returning_session_waits_for_its_last_cluster_and_promotion_is_not_cached() {
        let target = target();
        for algorithm in BOTH {
            let config = config(algorithm, true, |_| {});
            let mut candidates = single_slot(3);
            let order = affinity_order(&config, &candidates);
            let (a, b) = (order[0], order[1]);
            fill(&mut candidates, &[b]);
            let lb = balancer(config);
            let returning = hint(&candidates[b].cluster_id);
            let held = request(&target, Some(KEY), 0, Some(&returning));

            // A is free, but the request holds for B for the whole wait.
            for elapsed_ms in [0, 150, 299] {
                assert_eq!(
                    lb(&held, &candidates, ms(elapsed_ms)),
                    Wait(ms(300 - elapsed_ms)),
                    "{algorithm}"
                );
            }
            assert_eq!(
                returning.classification(),
                Some(SessionClassification {
                    state: SessionState::Returning,
                    promoted: true
                })
            );
            candidates[b].stats.num_running_queries = 0;
            assert_eq!(
                lb(&held, &candidates, Duration::ZERO),
                selected(b, 1),
                "{algorithm}"
            );

            let new = LastClusterHint::default();
            let new_request = request(&target, Some(KEY), 0, Some(&new));
            assert_eq!(
                lb(&new_request, &candidates, Duration::ZERO),
                selected(a, 1),
                "{algorithm}"
            );
        }
    }

    #[test]
    fn returning_session_does_not_discount_the_original_rank_one() {
        let target = target();
        for algorithm in BOTH {
            let config = config(algorithm, true, |_| {});
            let mut candidates = single_slot(3);
            let order = affinity_order(&config, &candidates);
            let (a, b, c) = (order[0], order[1], order[2]);
            fill(&mut candidates, &[b]);
            // Discounted, A beats C (201 vs 1500 ms); at full prefill C wins.
            candidates[a].rtt = ms(1);
            candidates[a].stats.last_mean_input_tps = 500.0;
            candidates[c].rtt = ms(500);
            candidates[c].stats.last_mean_input_tps = 1_000.0;
            let returning = hint(&candidates[b].cluster_id);
            let request = request(&target, Some(KEY), 1_000, Some(&returning));

            assert_eq!(
                selected_index(balancer(config)(&request, &candidates, WAIT)),
                c,
                "{algorithm}"
            );
        }
    }

    #[test]
    fn returning_session_group_keeps_its_size() {
        let target = target();
        for algorithm in BOTH {
            let config = config(algorithm, true, |settings| {
                settings.cache_affinity_backend_selection_count = Some(2);
            });
            let base = single_slot(4);
            let order = affinity_order(&config, &base);
            let (a, b, c, d) = (order[0], order[1], order[2], order[3]);
            let lb = balancer(config);

            // Last cluster C outside the group (A, B): the group is (C, A).
            let last = hint(&base[c].cluster_id);
            let outside = request(&target, Some(KEY), 0, Some(&last));
            let mut candidates = base.clone();
            fill(&mut candidates, &[a, c, d]);
            assert_eq!(
                lb(&outside, &candidates, ms(100)),
                Wait(ms(200)),
                "{algorithm}"
            );
            assert_eq!(
                selected_index(lb(&outside, &candidates, WAIT)),
                b,
                "{algorithm}"
            );
            candidates[a].stats.num_running_queries = 0;
            assert_eq!(
                selected_index(lb(&outside, &candidates, Duration::ZERO)),
                a,
                "{algorithm}"
            );

            // Last cluster B inside the group: the members stay A and B.
            let last = hint(&base[b].cluster_id);
            let inside = request(&target, Some(KEY), 0, Some(&last));
            let mut candidates = base.clone();
            fill(&mut candidates, &[b]);
            assert_eq!(
                selected_index(lb(&inside, &candidates, Duration::ZERO)),
                a,
                "{algorithm}"
            );
            fill(&mut candidates, &[a]);
            assert_eq!(
                lb(&inside, &candidates, Duration::ZERO),
                Wait(WAIT),
                "{algorithm}"
            );
        }
    }

    #[test]
    fn pulsar_returning_session_widens_bands_from_the_promoted_ranking() {
        let target = target();
        let config = config(PulsarWaitAndWiden, true, |settings| {
            settings.band_widen_interval_ms = Some(100);
        });
        let base = single_slot(5);
        let order = affinity_order(&config, &base);
        let lb = balancer(config);
        let last = hint(&base[order[3]].cluster_id);
        let request = request(&target, Some(KEY), 0, Some(&last));
        let only_free = |position: usize| {
            let mut candidates = base.clone();
            for (other, index) in order.iter().enumerate() {
                if other != position {
                    candidates[*index].stats.num_running_queries = 1;
                }
            }
            candidates
        };

        // After the wait the open set is D, A, B. C opens with the next band.
        assert_eq!(lb(&request, &only_free(1), WAIT), selected(order[1], 3));
        assert_eq!(lb(&request, &only_free(2), WAIT), Wait(ms(100)));
        assert_eq!(
            lb(&request, &only_free(2), WAIT + ms(100)),
            selected(order[2], 4)
        );
    }

    #[test]
    fn ineligible_last_cluster_acts_as_an_ineligible_rank_one() {
        let target = target();
        for (algorithm, kv_check) in [
            (WaitAndWiden, false),
            (PulsarWaitAndWiden, false),
            (PulsarWaitAndWiden, true),
        ] {
            let mut config = config(algorithm, true, |_| {});
            config.request_policy_mut().consider_kv_free_tokens = kv_check;
            let mut candidates = single_slot(3);
            let b = affinity_order(&config, &candidates)[1];
            let b_id = candidates[b].cluster_id.clone();
            let excluded = HashSet::from([b_id.clone()]);
            if kv_check {
                candidates[b].stats.kv_cache_free_tokens = 50;
                candidates[b].stats.kv_cache_used_tokens = 974;
            }
            let last = hint(&b_id);
            let request = LoadBalancerRequest {
                excluded_cluster_ids: (!kv_check).then_some(&excluded),
                ..request(&target, Some(KEY), 100, Some(&last))
            };
            let lb = balancer(config);

            assert_eq!(
                lb(&request, &candidates, Duration::ZERO),
                Wait(WAIT),
                "{algorithm}"
            );
            assert_ne!(
                selected_index(lb(&request, &candidates, WAIT)),
                b,
                "{algorithm}"
            );
        }
    }

    #[test]
    fn instances_with_the_same_config_make_the_same_choice() {
        let target = target();
        for algorithm in BOTH {
            let config = config(algorithm, true, locked_group_of_two);
            let mut candidates = single_slot(5);
            let order = affinity_order(&config, &candidates);
            for (position, rtt_ms) in [150, 100, 50, 250, 0].into_iter().enumerate() {
                candidates[order[position]].rtt = ms(rtt_ms);
            }
            let mut group_full = candidates.clone();
            fill(&mut group_full, &[order[0], order[3]]);
            let instances = [balancer(config.clone()), balancer(config)];

            for (snapshot, elapsed_ms) in [
                (&candidates, 0),
                (&group_full, 150),
                (&group_full, 300),
                (&group_full, 301),
            ] {
                let [first, second] = instances.each_ref().map(|lb| {
                    let last = hint(&candidates[order[3]].cluster_id);
                    lb(
                        &request(&target, Some(KEY), 0, Some(&last)),
                        snapshot,
                        ms(elapsed_ms),
                    )
                });
                assert_eq!(first, second, "{algorithm} at {elapsed_ms} ms");
            }
        }
    }

    #[test]
    fn last_cluster_is_ignored_without_flag_affinity_key_or_group() {
        let target = target();
        for (algorithm, enabled, key, group) in [
            (WaitAndWiden, false, Some(KEY), Some(1)),
            (PulsarWaitAndWiden, false, Some(KEY), Some(1)),
            (WaitAndWiden, true, None, Some(1)),
            (PulsarWaitAndWiden, true, None, Some(1)),
            (WaitAndWiden, true, Some(KEY), None),
        ] {
            let lb = balancer(config(algorithm, enabled, |settings| {
                locked_group_of_two(settings);
                settings.cache_affinity_backend_selection_count = group;
            }));
            let mut candidates = single_slot(3);
            for (index, rtt_ms) in [40, 80, 120].into_iter().enumerate() {
                candidates[index].rtt = ms(rtt_ms);
            }
            fill(&mut candidates, &[1]);
            let last = hint(&candidates[1].cluster_id);
            for elapsed in [Duration::ZERO, WAIT] {
                assert_eq!(
                    lb(&request(&target, key, 0, Some(&last)), &candidates, elapsed),
                    lb(&request(&target, key, 0, None), &candidates, elapsed),
                    "{algorithm} enabled={enabled} key={key:?} group={group:?}"
                );
            }
            assert_eq!(last.classification(), None);
        }
    }

    #[test]
    fn pulsar_pins_the_classification_even_with_an_empty_ranking() {
        let target = target();
        let lb = balancer(config(PulsarWaitAndWiden, true, |_| {}));
        let last = hint("b");
        let request = request(&target, Some(KEY), 0, Some(&last));
        let mut unweighted = single_slot(1);
        unweighted[0].stats.max_input_tps = None;

        assert_eq!(lb(&request, &unweighted, Duration::ZERO), Unavailable);
        // B joins before the retry. The request stays stale, so it does not
        // wait for B.
        let mut candidates = single_slot(3);
        fill(&mut candidates, &[1]);
        assert!(
            lb(&request, &candidates, Duration::ZERO)
                .selected()
                .is_some()
        );
        assert_eq!(
            last.classification().map(|c| c.state),
            Some(SessionState::Stale)
        );
    }

    /// Label values in declaration order, with the counter value.
    fn session_series(metrics: &StargateMetrics) -> Vec<(Vec<String>, u64)> {
        metrics
            .registry()
            .gather()
            .into_iter()
            .filter(|family| family.name() == "stargate_routing_session_selections_total")
            .flat_map(|family| family.get_metric().to_vec())
            .map(|metric| {
                let labels = [
                    "routing_key",
                    "model",
                    "algorithm",
                    "session_state",
                    "selection",
                ]
                .map(|name| {
                    metric
                        .get_label()
                        .iter()
                        .find(|label| label.name() == name)
                        .map(|label| label.value().to_string())
                        .unwrap_or_default()
                });
                (labels.to_vec(), metric.get_counter().value() as u64)
            })
            .collect()
    }

    #[test]
    fn session_metric_starts_every_pair_at_zero_and_counts_the_selection() {
        let config = config(PulsarWaitAndWiden, true, |settings| {
            settings.cache_affinity_backend_selection_count = Some(2);
        });
        // Returning: primary means the last cluster. New and stale: primary
        // means a rank within the affinity group (k = 2).
        use SessionSelection::{Fallback, Primary};
        use SessionState::{New, Returning, Stale};
        for (state, last_id, selected_id, rank_depth, selection) in [
            (Returning, Some("B"), "B", 3, Primary),
            (Returning, Some("B"), "A", 1, Fallback),
            (New, None, "A", 2, Primary),
            (New, None, "A", 3, Fallback),
            (Stale, Some("gone"), "A", 1, Primary),
            (Stale, Some("gone"), "A", 3, Fallback),
        ] {
            let metrics = StargateMetrics::new().unwrap();
            let classification = SessionClassification {
                state,
                promoted: false,
            };
            let hint = LastClusterHint::classified(last_id, classification);
            record_session_selection(
                &metrics,
                &config,
                "pulsar-wait-and-widen",
                "req-1",
                &target(),
                &hint,
                selected_id,
                rank_depth,
            );

            let series = session_series(&metrics);
            let labels = |state: SessionState, selection: SessionSelection| {
                ["rk-1", "model-a", "pulsar-wait-and-widen"]
                    .into_iter()
                    .chain([state.as_str(), selection.as_str()])
                    .map(str::to_owned)
                    .collect::<Vec<_>>()
            };
            for state in SessionState::ALL {
                for selection in SessionSelection::ALL {
                    let labels = labels(state, selection);
                    assert!(
                        series
                            .iter()
                            .any(|(series_labels, _)| *series_labels == labels)
                    );
                }
            }
            let counted: Vec<_> = series.into_iter().filter(|(_, value)| *value > 0).collect();
            assert_eq!(
                counted,
                vec![(labels(state, selection), 1)],
                "{state:?} {selection:?}"
            );
        }
    }

    #[test]
    fn unclassified_requests_record_no_session_metric() {
        let metrics = StargateMetrics::new().unwrap();
        let config = config(PulsarWaitAndWiden, true, |_| {});
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

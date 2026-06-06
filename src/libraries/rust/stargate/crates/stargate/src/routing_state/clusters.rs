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

use super::calibration::{ClusterCalibrationAssignments, valid_last_mean_input_tps};
use super::keys::RoutingTargetKey;
use super::reservations::{
    PendingClusterReservation, RoutingReservation, RoutingReservationState,
    apply_pending_cluster_reservations, update_reserved_priority_queue_time,
};
use super::snapshots::{
    ActiveModelSnapshot, ActiveModelSnapshotState, ClusterScopedUpdate, RoutedClusterSnapshot,
    RoutedClusterState, RoutedInferenceServerSnapshot, RoutedInferenceServerSnapshotInput,
    RoutingTargetState, StoredClusterSnapshot,
};
use super::*;

#[derive(Debug, Default)]
pub(super) struct RoutingLifecycle {
    pub(super) targets: RoutingTargetStore,
    active_models: ActiveModelSnapshotState,
    reservations: RoutingReservationState,
    metrics: Option<Arc<StargateMetrics>>,
}

#[derive(Debug, Default)]
pub(super) struct RoutingTargetStore {
    pub(super) targets: SccHashMap<RoutingTargetKey, Arc<RoutingTargetState>>,
}

impl RoutingTargetStore {
    pub(super) async fn target_state(
        &self,
        target: &RoutingTargetKey,
    ) -> Option<Arc<RoutingTargetState>> {
        self.targets
            .read_async(target, |_key, state| state.clone())
            .await
    }

    pub(super) async fn target_state_or_insert(
        &self,
        target: &RoutingTargetKey,
    ) -> Arc<RoutingTargetState> {
        loop {
            if let Some(existing) = self.target_state(target).await {
                return existing;
            }

            let candidate = Arc::new(RoutingTargetState::default());
            if self
                .targets
                .insert_async(target.clone(), candidate.clone())
                .await
                .is_ok()
            {
                return candidate;
            }

            if let Some(existing) = self.target_state(target).await {
                return existing;
            }
        }
    }

    pub(super) async fn cluster_state(
        target_state: &RoutingTargetState,
        cluster_id: &str,
    ) -> Option<Arc<RoutedClusterState>> {
        target_state
            .clusters
            .read_async(cluster_id, |_key, state| state.clone())
            .await
    }

    pub(super) async fn cluster_state_or_insert(
        target_state: &RoutingTargetState,
        cluster_id: &str,
    ) -> Arc<RoutedClusterState> {
        loop {
            if let Some(existing) = Self::cluster_state(target_state, cluster_id).await {
                return existing;
            }

            let candidate = Arc::new(RoutedClusterState::default());
            if target_state
                .clusters
                .insert_async(cluster_id.to_string(), candidate.clone())
                .await
                .is_ok()
            {
                return candidate;
            }

            if let Some(existing) = Self::cluster_state(target_state, cluster_id).await {
                return existing;
            }
        }
    }

    pub(super) async fn targets(&self) -> Vec<(RoutingTargetKey, Arc<RoutingTargetState>)> {
        let mut targets = Vec::new();
        let _ = self
            .targets
            .iter_async(|target, target_state| {
                targets.push((target.clone(), target_state.clone()));
                true
            })
            .await;
        targets
    }

    pub(super) async fn remove_if_empty(
        &self,
        target: &RoutingTargetKey,
        target_state: Arc<RoutingTargetState>,
    ) {
        let _ = self
            .targets
            .remove_if_async(target, move |current| {
                Arc::ptr_eq(current, &target_state) && current.clusters.is_empty()
            })
            .await;
    }
}

impl RoutingLifecycle {
    pub(super) fn new(metrics: Option<Arc<StargateMetrics>>) -> Self {
        Self {
            targets: RoutingTargetStore::default(),
            active_models: ActiveModelSnapshotState::default(),
            reservations: RoutingReservationState::default(),
            metrics,
        }
    }

    pub(super) async fn target_state(
        &self,
        target: &RoutingTargetKey,
    ) -> Option<Arc<RoutingTargetState>> {
        self.targets.target_state(target).await
    }

    pub(super) async fn target_state_or_insert(
        &self,
        target: &RoutingTargetKey,
    ) -> Arc<RoutingTargetState> {
        self.targets.target_state_or_insert(target).await
    }

    pub(super) async fn cluster_state(
        target_state: &RoutingTargetState,
        cluster_id: &str,
    ) -> Option<Arc<RoutedClusterState>> {
        RoutingTargetStore::cluster_state(target_state, cluster_id).await
    }

    pub(super) async fn cluster_state_or_insert(
        target_state: &RoutingTargetState,
        cluster_id: &str,
    ) -> Arc<RoutedClusterState> {
        RoutingTargetStore::cluster_state_or_insert(target_state, cluster_id).await
    }

    pub(super) async fn upsert_inference_server_target(
        &self,
        target: &RoutingTargetKey,
        snapshot_input: RoutedInferenceServerSnapshotInput<'_>,
        calibration_assignments: &ClusterCalibrationAssignments,
    ) {
        let snapshot = RoutedInferenceServerSnapshot {
            cluster_id: snapshot_input.cluster_id.to_string(),
            inference_server_id: snapshot_input.inference_server_id.to_string(),
            inference_server_url: snapshot_input.inference_server_url.to_string(),
            stats: snapshot_input.stats,
            rtt: snapshot_input.rtt,
            snapshot_updated_at: snapshot_input.snapshot_updated_at,
            status: snapshot_input.status,
            reverse_tunnel: snapshot_input.reverse_tunnel,
            delivery_target: snapshot_input.delivery_target,
        };
        let cluster_scoped_update = ClusterScopedUpdate {
            source_backend_id: snapshot.inference_server_id.clone(),
            stats: snapshot.stats.clone(),
            snapshot_updated_at: snapshot.snapshot_updated_at,
            status: snapshot.status,
        };

        let target_state = self.target_state_or_insert(target).await;
        let cluster_state =
            Self::cluster_state_or_insert(&target_state, snapshot_input.cluster_id).await;
        let _ = cluster_state
            .inference_servers
            .upsert_async(snapshot_input.inference_server_id.to_string(), snapshot)
            .await;
        let calibrated_last_mean_input_tps = calibration_assignments
            .completed_last_mean_input_tps(target, snapshot_input.cluster_id)
            .await;
        refresh_cluster_snapshot(
            snapshot_input.cluster_id,
            &cluster_state,
            Some(cluster_scoped_update),
            calibrated_last_mean_input_tps,
        )
        .await;
        self.refresh_active_inference_server_count(target, &target_state)
            .await;
    }

    pub(super) async fn remove_inference_server_from_target(
        &self,
        inference_server_id: &str,
        cluster_id: &str,
        target: &RoutingTargetKey,
        calibration_assignments: &ClusterCalibrationAssignments,
    ) {
        let Some(target_state) = self.target_state(target).await else {
            return;
        };

        let Some(cluster_state) = Self::cluster_state(&target_state, cluster_id).await else {
            return;
        };

        let _ = cluster_state
            .inference_servers
            .remove_async(inference_server_id)
            .await;

        if !cluster_state.inference_servers.is_empty() {
            let calibrated_last_mean_input_tps = calibration_assignments
                .completed_last_mean_input_tps(target, cluster_id)
                .await;
            refresh_cluster_snapshot(
                cluster_id,
                &cluster_state,
                None,
                calibrated_last_mean_input_tps,
            )
            .await;
        } else {
            *cluster_state.cluster_snapshot.lock() = None;
        }

        if cluster_state.inference_servers.is_empty() {
            let cluster_state_for_remove = cluster_state.clone();
            let _ = target_state
                .clusters
                .remove_if_async(cluster_id, move |current| {
                    Arc::ptr_eq(current, &cluster_state_for_remove)
                        && current.inference_servers.is_empty()
                })
                .await;
        }

        let count = count_target_inference_servers(&target_state).await;
        if count == 0 {
            self.targets.remove_if_empty(target, target_state).await;
        }

        self.set_active_inference_server_count(target, count);
    }

    pub(super) async fn remove_inference_server_targets(
        &self,
        inference_server_id: &str,
        cluster_id: &str,
        targets: &HashSet<RoutingTargetKey>,
        calibration_assignments: &ClusterCalibrationAssignments,
    ) {
        for target in targets {
            self.remove_inference_server_from_target(
                inference_server_id,
                cluster_id,
                target,
                calibration_assignments,
            )
            .await;
        }
    }

    pub(super) async fn candidates_for_target(
        &self,
        target: &RoutingTargetKey,
    ) -> Vec<RoutedInferenceServerSnapshot> {
        let Some(target_state) = self.target_state(target).await else {
            return Vec::new();
        };

        let mut candidates = Vec::new();
        for (_cluster_id, cluster_state) in collect_target_clusters(&target_state).await {
            let _ = cluster_state
                .inference_servers
                .iter_async(|_inference_server_id, snapshot| {
                    candidates.push(snapshot.clone());
                    true
                })
                .await;
        }
        candidates
    }

    pub(super) async fn cluster_candidates_for_target(
        &self,
        target: &RoutingTargetKey,
        calibration_assignments: &ClusterCalibrationAssignments,
    ) -> Vec<RoutedClusterSnapshot> {
        let Some(target_state) = self.target_state(target).await else {
            return Vec::new();
        };

        let mut clusters = Vec::new();
        for (cluster_id, cluster_state) in collect_target_clusters(&target_state).await {
            let calibrated_last_mean_input_tps = calibration_assignments
                .completed_last_mean_input_tps(target, &cluster_id)
                .await;
            if let Some(snapshot) = cluster_snapshot_for_target(
                &cluster_id,
                &cluster_state,
                calibrated_last_mean_input_tps,
            )
            .await
            {
                clusters.push(snapshot);
            }
        }
        clusters
    }

    pub(super) async fn refresh_active_models_snapshot(
        &self,
        calibration_assignments: &ClusterCalibrationAssignments,
    ) {
        let mut models = BTreeSet::new();
        for (target, target_state) in self.targets.targets().await {
            let mut has_routable_snapshot = false;
            for (cluster_id, cluster_state) in collect_target_clusters(&target_state).await {
                let calibrated_last_mean_input_tps = calibration_assignments
                    .completed_last_mean_input_tps(&target, &cluster_id)
                    .await;
                if cluster_snapshot_for_target(
                    &cluster_id,
                    &cluster_state,
                    calibrated_last_mean_input_tps,
                )
                .await
                .is_some()
                {
                    has_routable_snapshot = true;
                    break;
                }
            }
            if !has_routable_snapshot {
                continue;
            }
            models.insert(ActiveModelSnapshot {
                routing_key: target.routing_key,
                model_id: target.model_id,
            });
        }

        self.active_models.replace(models.into_iter().collect());
    }

    pub(super) fn list_active_models(
        &self,
        routing_key: Option<&str>,
        model_ids: &[String],
    ) -> Vec<String> {
        self.active_models.list(routing_key, model_ids)
    }

    pub(super) async fn select_backend_for_cluster(
        &self,
        target: &RoutingTargetKey,
        cluster_id: &str,
        failed_backend_ids: &HashSet<String>,
    ) -> Option<RoutedInferenceServerSnapshot> {
        let target_state = self.target_state(target).await?;
        let cluster_state = Self::cluster_state(&target_state, cluster_id).await?;

        let mut candidates = Vec::new();
        let _ = cluster_state
            .inference_servers
            .iter_async(|backend_id, snapshot| {
                if !failed_backend_ids.contains(backend_id) {
                    candidates.push(snapshot.clone());
                }
                true
            })
            .await;

        if candidates.is_empty() {
            return None;
        }

        candidates.sort_by(|a, b| a.inference_server_id.cmp(&b.inference_server_id));
        let idx = cluster_state
            .round_robin_counter
            .fetch_add(1, Ordering::Relaxed)
            % candidates.len();
        Some(candidates[idx].clone())
    }

    pub(super) async fn reserve_inference_server_for_target(
        &self,
        target: &RoutingTargetKey,
        inference_server_id: &str,
        input_tokens: Option<u64>,
        priority: u32,
    ) -> Option<RoutingReservation> {
        let target_state = self.target_state(target).await?;
        let input_tokens = input_tokens.unwrap_or(0);
        let reservation = self.reservations.next();
        // Reservation counters are optimistic routing stats; saturate at u64::MAX rather than wrap.
        for (_cluster_id, cluster_state) in collect_target_clusters(&target_state).await {
            let backend_exists = cluster_state
                .inference_servers
                .read_async(inference_server_id, |_id, _snapshot| ())
                .await
                .is_some();
            if !backend_exists {
                continue;
            }
            let mut snapshot = cluster_state.cluster_snapshot.lock();
            let stored = snapshot.as_mut()?;
            // Keep optimistic work in routing-owned state: backend snapshots are
            // heartbeat-owned, so rejection can release only its own reservation.
            stored.pending_cluster_reservations.insert(
                reservation.id,
                PendingClusterReservation {
                    inference_server_id: inference_server_id.to_string(),
                    input_tokens,
                    priority,
                },
            );

            stored.snapshot.stats.queue_size = stored.snapshot.stats.queue_size.saturating_add(1);
            stored.snapshot.stats.queued_input_size = stored
                .snapshot
                .stats
                .queued_input_size
                .saturating_add(input_tokens);
            stored.snapshot.stats.num_running_queries =
                stored.snapshot.stats.num_running_queries.saturating_add(1);
            stored.snapshot.stats.total_query_input_size = stored
                .snapshot
                .stats
                .total_query_input_size
                .saturating_add(input_tokens);
            update_reserved_priority_queue_time(&mut stored.snapshot.stats, input_tokens, priority);
            return Some(reservation);
        }
        None
    }

    pub(super) async fn release_inference_server_reservation_for_target(
        &self,
        target: &RoutingTargetKey,
        reservation: RoutingReservation,
        calibration_assignments: &ClusterCalibrationAssignments,
    ) {
        let Some(target_state) = self.target_state(target).await else {
            return;
        };
        for (cluster_id, cluster_state) in collect_target_clusters(&target_state).await {
            let pending = cluster_state
                .cluster_snapshot
                .lock()
                .as_mut()
                .and_then(|stored| stored.pending_cluster_reservations.remove(&reservation.id));
            if pending.is_none() {
                continue;
            }

            let calibrated_last_mean_input_tps = calibration_assignments
                .completed_last_mean_input_tps(target, &cluster_id)
                .await;
            refresh_cluster_snapshot(
                &cluster_id,
                &cluster_state,
                None,
                calibrated_last_mean_input_tps,
            )
            .await;
            return;
        }
    }

    pub(super) async fn refresh_active_inference_server_count(
        &self,
        target: &RoutingTargetKey,
        target_state: &RoutingTargetState,
    ) {
        let count = count_target_inference_servers(target_state).await;
        self.set_active_inference_server_count(target, count);
    }

    pub(super) fn set_active_inference_server_count(
        &self,
        target: &RoutingTargetKey,
        count: usize,
    ) {
        if let Some(metrics) = &self.metrics {
            metrics.set_active_inference_servers(
                target.routing_key.as_deref(),
                &target.model_id,
                count,
            );
        }
    }
}

pub(super) async fn count_target_inference_servers(target_state: &RoutingTargetState) -> usize {
    let mut count = 0;
    let _ = target_state
        .clusters
        .iter_async(|_, cluster_state| {
            count += cluster_state.inference_servers.len();
            true
        })
        .await;
    count
}

pub(super) async fn collect_target_clusters(
    target_state: &RoutingTargetState,
) -> Vec<(String, Arc<RoutedClusterState>)> {
    let mut clusters = Vec::new();
    let _ = target_state
        .clusters
        .iter_async(|cluster_id, cluster_state| {
            clusters.push((cluster_id.clone(), cluster_state.clone()));
            true
        })
        .await;
    clusters
}

pub(super) fn set_backend_scoped_stats(stats: &mut ModelStats, src: &ModelStats) {
    stats.last_mean_input_tps = src.last_mean_input_tps;
    stats.output_tps = src.output_tps;
    stats.queue_size = src.queue_size;
    stats.queued_input_size = src.queued_input_size;
    stats.input_processing_queries = src.input_processing_queries;
    stats.output_generation_queries = src.output_generation_queries;
    stats.stats_observed_at_unix_ms = src.stats_observed_at_unix_ms;
    stats.stats_capabilities = src.stats_capabilities.clone();
    stats.stats_sources = src.stats_sources.clone();
}

pub(super) fn set_cluster_scoped_stats(stats: &mut ModelStats, src: &ModelStats) {
    stats.max_output_tps = src.max_output_tps;
    stats.kv_cache_capacity_tokens = src.kv_cache_capacity_tokens;
    stats.kv_cache_used_tokens = src.kv_cache_used_tokens;
    stats.kv_cache_free_tokens = src.kv_cache_free_tokens;
    stats.num_running_queries = src.num_running_queries;
    stats.max_engine_concurrency = src.max_engine_concurrency;
    stats.total_query_input_size = src.total_query_input_size;
    stats.queue_time_estimate_ms_by_priority = src.queue_time_estimate_ms_by_priority.clone();
}

pub(super) fn build_cluster_snapshot(
    cluster_id: &str,
    stored: &StoredClusterSnapshot,
    backend_stats: &ModelStats,
    rtt: Duration,
    active_backend_count: usize,
    calibrated_last_mean_input_tps: Option<f64>,
) -> RoutedClusterSnapshot {
    let mut snapshot = stored.snapshot.clone();
    snapshot.cluster_id = cluster_id.to_string();
    snapshot.rtt = rtt;
    snapshot.active_backend_count = active_backend_count;
    set_backend_scoped_stats(&mut snapshot.stats, backend_stats);
    if let Some(calibrated_last_mean_input_tps) = calibrated_last_mean_input_tps {
        snapshot.stats.last_mean_input_tps = snapshot
            .stats
            .last_mean_input_tps
            .max(calibrated_last_mean_input_tps);
    }
    set_cluster_scoped_stats(&mut snapshot.stats, &stored.cluster_stats_base);
    apply_pending_cluster_reservations(&mut snapshot.stats, &stored.pending_cluster_reservations);
    snapshot
}

pub(super) async fn collect_cluster_backend_aggregate(
    cluster_state: &RoutedClusterState,
) -> Option<(ModelStats, Duration, usize)> {
    let mut backend_stats = ModelStats::default();
    let mut active_backend_count = 0usize;
    let mut rtt: Option<Duration> = None;

    let _ = cluster_state
        .inference_servers
        .iter_async(|_backend_id, snapshot| {
            active_backend_count += 1;
            backend_stats.output_tps += snapshot.stats.output_tps;
            if valid_last_mean_input_tps(snapshot.stats.last_mean_input_tps) {
                backend_stats.last_mean_input_tps += snapshot.stats.last_mean_input_tps;
            }
            backend_stats.queue_size += snapshot.stats.queue_size;
            backend_stats.queued_input_size += snapshot.stats.queued_input_size;
            backend_stats.input_processing_queries += snapshot.stats.input_processing_queries;
            backend_stats.output_generation_queries += snapshot.stats.output_generation_queries;
            backend_stats.stats_observed_at_unix_ms = backend_stats
                .stats_observed_at_unix_ms
                .max(snapshot.stats.stats_observed_at_unix_ms);
            append_unique_strings(
                &mut backend_stats.stats_capabilities,
                &snapshot.stats.stats_capabilities,
            );
            append_unique_strings(
                &mut backend_stats.stats_sources,
                &snapshot.stats.stats_sources,
            );
            rtt = Some(match rtt {
                Some(current) => current.min(snapshot.rtt),
                None => snapshot.rtt,
            });
            true
        })
        .await;

    Some((backend_stats, rtt?, active_backend_count))
}

pub(super) fn append_unique_strings(target: &mut Vec<String>, values: &[String]) {
    for value in values {
        if !target.iter().any(|existing| existing == value) {
            target.push(value.clone());
        }
    }
}

pub(super) async fn cluster_snapshot_for_target(
    cluster_id: &str,
    cluster_state: &RoutedClusterState,
    calibrated_last_mean_input_tps: Option<f64>,
) -> Option<RoutedClusterSnapshot> {
    let stored = cluster_state.cluster_snapshot.lock().clone()?;

    let (backend_stats, rtt, active_backend_count) =
        collect_cluster_backend_aggregate(cluster_state).await?;
    Some(build_cluster_snapshot(
        cluster_id,
        &stored,
        &backend_stats,
        rtt,
        active_backend_count,
        calibrated_last_mean_input_tps,
    ))
}

pub(super) async fn refresh_cluster_snapshot(
    cluster_id: &str,
    cluster_state: &RoutedClusterState,
    cluster_scoped_update: Option<ClusterScopedUpdate>,
    calibrated_last_mean_input_tps: Option<f64>,
) {
    let mut present_backend_ids = HashSet::new();
    let Some((backend_stats, rtt, active_backend_count)) =
        collect_cluster_backend_aggregate(cluster_state).await
    else {
        *cluster_state.cluster_snapshot.lock() = None;
        return;
    };
    let _ = cluster_state
        .inference_servers
        .iter_async(|backend_id, _snapshot| {
            present_backend_ids.insert(backend_id.clone());
            true
        })
        .await;

    let mut stored_opt = cluster_state.cluster_snapshot.lock();
    if stored_opt.is_none() && cluster_scoped_update.is_none() {
        return;
    }

    let next_cluster_scoped = {
        let stored = stored_opt.get_or_insert_with(|| {
            let initial_update = cluster_scoped_update
                .clone()
                .expect("cluster snapshot initialization requires a cluster-scoped update");
            StoredClusterSnapshot {
                snapshot: RoutedClusterSnapshot {
                    cluster_id: cluster_id.to_string(),
                    stats: ModelStats::default(),
                    rtt,
                    snapshot_updated_at: initial_update.snapshot_updated_at,
                    status: initial_update.status,
                    active_backend_count,
                },
                cluster_stats_source_backend_id: initial_update.source_backend_id.clone(),
                cluster_stats_base: initial_update.stats.clone(),
                raw_cluster_updates: HashMap::new(),
                pending_cluster_reservations: BTreeMap::new(),
            }
        });

        if let Some(update) = cluster_scoped_update.clone() {
            stored
                .raw_cluster_updates
                .insert(update.source_backend_id.clone(), update);
        }
        stored
            .raw_cluster_updates
            .retain(|backend_id, _| present_backend_ids.contains(backend_id));
        stored
            .pending_cluster_reservations
            .retain(|_, pending| present_backend_ids.contains(&pending.inference_server_id));

        if let Some(update) = cluster_scoped_update.as_ref() {
            stored
                .pending_cluster_reservations
                .retain(|_, pending| pending.inference_server_id != update.source_backend_id);
        }

        if let Some(update) = cluster_scoped_update {
            Some(update)
        } else if let Some(update) = stored
            .raw_cluster_updates
            .get(&stored.cluster_stats_source_backend_id)
            .cloned()
        {
            Some(update)
        } else {
            stored
                .raw_cluster_updates
                .values()
                .max_by_key(|update| update.snapshot_updated_at)
                .cloned()
        }
    };

    let Some(next_cluster_scoped) = next_cluster_scoped else {
        *stored_opt = None;
        return;
    };

    let stored = stored_opt
        .as_mut()
        .expect("stored cluster snapshot should exist after initialization");

    stored.cluster_stats_source_backend_id = next_cluster_scoped.source_backend_id.clone();
    stored.cluster_stats_base = next_cluster_scoped.stats.clone();
    stored.snapshot.snapshot_updated_at = next_cluster_scoped.snapshot_updated_at;
    stored.snapshot.status = next_cluster_scoped.status;
    stored.snapshot = build_cluster_snapshot(
        cluster_id,
        stored,
        &backend_stats,
        rtt,
        active_backend_count,
        calibrated_last_mean_input_tps,
    );
}

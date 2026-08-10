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

use std::collections::HashSet;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use parking_lot::Mutex;
use stargate_proto::pb::ModelStats;
use stargate_protocol::common::valid_last_mean_input_tps;

use super::registration::{RegistrationClusterGeneration, RegistrationGeneration};
use super::reservations::{
    PendingClusterReservation, RoutingReservation, apply_pending_cluster_reservations,
};
use super::snapshots::{
    ClusterBackendRemoval, ClusterBackendUpsert, ClusterRoutingGeneration, ClusterSnapshotState,
    RoutedClusterSnapshot, RoutedClusterState, RoutedInferenceServerSnapshot,
};

fn set_cluster_scoped_stats(stats: &mut ModelStats, src: &ModelStats) {
    stats.max_output_tps = src.max_output_tps;
    stats.kv_cache_capacity_tokens = src.kv_cache_capacity_tokens;
    stats.kv_cache_used_tokens = src.kv_cache_used_tokens;
    stats.kv_cache_free_tokens = src.kv_cache_free_tokens;
    stats.num_running_queries = src.num_running_queries;
    stats.max_engine_concurrency = src.max_engine_concurrency;
    stats.total_query_input_size = src.total_query_input_size;
    stats.queue_time_estimate_ms_by_priority = src.queue_time_estimate_ms_by_priority.clone();
}

fn append_unique_strings(target: &mut Vec<String>, values: &[String]) {
    for value in values {
        if !target.contains(value) {
            target.push(value.clone());
        }
    }
}

fn backend_index(
    backends: &[Arc<RoutedInferenceServerSnapshot>],
    inference_server_id: &str,
) -> Result<usize, usize> {
    backends.binary_search_by(|backend| {
        backend
            .inference_server_id
            .as_str()
            .cmp(inference_server_id)
    })
}

impl ClusterRoutingGeneration {
    fn upsert_backend(
        &mut self,
        backend: Arc<RoutedInferenceServerSnapshot>,
    ) -> ClusterBackendUpsert {
        match backend_index(&self.backends, &backend.inference_server_id) {
            Ok(index) => {
                self.backends[index] = backend;
                ClusterBackendUpsert::Replaced
            }
            Err(index) => {
                self.backends.insert(index, backend);
                ClusterBackendUpsert::Inserted
            }
        }
    }

    fn remove_backend(&mut self, registration: &Arc<RegistrationGeneration>) -> bool {
        let inference_server_id = &registration.identity.inference_server_id;
        let Ok(index) = backend_index(&self.backends, inference_server_id) else {
            return false;
        };
        if !Arc::ptr_eq(&self.backends[index].registration, registration) {
            return false;
        }
        self.backends.remove(index);
        true
    }

    fn contains_registration(&self, registration: &Arc<RegistrationGeneration>) -> bool {
        backend_index(&self.backends, registration.inference_server_id())
            .is_ok_and(|index| Arc::ptr_eq(&self.backends[index].registration, registration))
    }

    fn backend_aggregate(&self) -> Option<(ModelStats, Duration, usize)> {
        let active_backend_count = self.backends.len();
        if active_backend_count == 0 {
            return None;
        }
        let backend_count = active_backend_count as u128;
        let mut backend_stats = ModelStats::default();
        let mut rtt_mean_nanos = 0_u128;
        let mut rtt_remainder_nanos = 0_u128;

        for backend in &self.backends {
            backend_stats.output_tps += backend.stats.output_tps;
            if valid_last_mean_input_tps(backend.stats.last_mean_input_tps) {
                backend_stats.last_mean_input_tps += backend.stats.last_mean_input_tps;
            }
            backend_stats.queue_size += backend.stats.queue_size;
            backend_stats.queued_input_size += backend.stats.queued_input_size;
            backend_stats.input_processing_queries += backend.stats.input_processing_queries;
            backend_stats.output_generation_queries += backend.stats.output_generation_queries;
            backend_stats.stats_observed_at_unix_ms = backend_stats
                .stats_observed_at_unix_ms
                .max(backend.stats.stats_observed_at_unix_ms);
            append_unique_strings(
                &mut backend_stats.stats_capabilities,
                &backend.stats.stats_capabilities,
            );
            append_unique_strings(
                &mut backend_stats.stats_sources,
                &backend.stats.stats_sources,
            );
            // Divide each sample before adding it so the mean cannot overflow.
            // Carrying the remainder after every sample also makes fractional
            // nanoseconds truncate deterministically after the final sample.
            let rtt_nanos = backend.rtt.as_nanos();
            rtt_mean_nanos += rtt_nanos / backend_count;
            rtt_remainder_nanos += rtt_nanos % backend_count;
            rtt_mean_nanos += rtt_remainder_nanos / backend_count;
            rtt_remainder_nanos %= backend_count;
        }

        // The loop maintains backend_count * mean + remainder = processed sum,
        // so the final mean is floor(total / backend_count). It cannot exceed
        // the largest input or Duration::MAX, and both casts are lossless.
        let rtt = Duration::new(
            (rtt_mean_nanos / 1_000_000_000) as u64,
            (rtt_mean_nanos % 1_000_000_000) as u32,
        );

        Some((backend_stats, rtt, active_backend_count))
    }

    fn refresh_snapshot(&mut self, updated_backend_id: Option<&str>) {
        let Some((backend_stats, rtt, active_backend_count)) = self.backend_aggregate() else {
            self.snapshot_state = None;
            return;
        };
        if self.snapshot_state.is_none() && updated_backend_id.is_none() {
            return;
        }
        let source_backend_index = updated_backend_id
            .and_then(|backend_id| backend_index(&self.backends, backend_id).ok())
            .or_else(|| {
                self.snapshot_state.as_ref().and_then(|state| {
                    backend_index(&self.backends, &state.cluster_stats_source_backend_id).ok()
                })
            })
            .or_else(|| {
                self.backends
                    .iter()
                    .enumerate()
                    .max_by_key(|(_, backend)| backend.snapshot_updated_at)
                    .map(|(index, _)| index)
            })
            .expect("non-empty cluster generation should have a source backend");
        let source_backend = &self.backends[source_backend_index];
        let source_backend_id = source_backend.inference_server_id.clone();
        let mut pending_cluster_reservations = self
            .snapshot_state
            .take()
            .map(|state| state.pending_cluster_reservations)
            .unwrap_or_default();
        pending_cluster_reservations.retain(|pending| {
            pending.is_active()
                && backend_index(&self.backends, &pending.inference_server_id).is_ok()
        });
        if let Some(updated_backend_id) = updated_backend_id {
            pending_cluster_reservations
                .retain(|pending| pending.inference_server_id != updated_backend_id);
        }
        let mut stats = backend_stats;
        set_cluster_scoped_stats(&mut stats, &source_backend.stats);
        let base_snapshot = RoutedClusterSnapshot {
            cluster_id: source_backend.cluster_id.clone(),
            stats,
            rtt,
            snapshot_updated_at: source_backend.snapshot_updated_at,
            status: source_backend.status,
            active_backend_count,
        };

        self.snapshot_state = Some(ClusterSnapshotState {
            base_snapshot,
            cluster_stats_source_backend_id: source_backend_id,
            pending_cluster_reservations,
        });
    }

    fn routing_snapshot(&mut self) -> Option<RoutedClusterSnapshot> {
        let snapshot_state = self.snapshot_state.as_mut()?;
        snapshot_state
            .pending_cluster_reservations
            .retain(|pending| pending.is_active());
        let mut snapshot = snapshot_state.base_snapshot.clone();
        apply_pending_cluster_reservations(
            &mut snapshot.stats,
            &snapshot_state.pending_cluster_reservations,
        );
        Some(snapshot)
    }

    fn reserve_backend(
        &mut self,
        registration: &Arc<RegistrationGeneration>,
        input_tokens: u64,
        priority: u32,
    ) -> Option<RoutingReservation> {
        if !self.contains_registration(registration) {
            return None;
        }
        let snapshot_state = self.snapshot_state.as_mut()?;
        let pending = PendingClusterReservation::new(
            registration.inference_server_id().to_string(),
            input_tokens,
            priority,
        );
        snapshot_state
            .pending_cluster_reservations
            .push(pending.clone());
        Some(RoutingReservation(pending))
    }
}

impl RoutedClusterState {
    pub(super) fn new(cluster_generation: Arc<RegistrationClusterGeneration>) -> Self {
        Self {
            cluster_generation,
            generation: Mutex::default(),
            round_robin_counter: AtomicUsize::default(),
        }
    }

    pub(super) fn upsert_backend(
        &self,
        backend: Arc<RoutedInferenceServerSnapshot>,
    ) -> ClusterBackendUpsert {
        backend.assert_registration_identity();
        assert!(
            Arc::ptr_eq(
                &self.cluster_generation,
                &backend.registration.cluster_generation
            ),
            "routed cluster cannot contain backends from different registration cluster generations"
        );
        let updated_backend_id = backend.inference_server_id.clone();
        let mut generation = self.generation.lock();
        let upsert = generation.upsert_backend(backend);
        generation.refresh_snapshot(Some(&updated_backend_id));
        upsert
    }

    pub(super) fn remove_backend(
        &self,
        registration: &Arc<RegistrationGeneration>,
    ) -> ClusterBackendRemoval {
        let mut generation = self.generation.lock();
        if !generation.remove_backend(registration) {
            return ClusterBackendRemoval::Missing;
        }
        generation.refresh_snapshot(None);
        if generation.backends.is_empty() {
            ClusterBackendRemoval::Emptied
        } else {
            ClusterBackendRemoval::Removed
        }
    }

    pub(super) fn routing_snapshot(&self) -> Option<RoutedClusterSnapshot> {
        self.generation.lock().routing_snapshot()
    }

    pub(super) fn backend_snapshot_values(&self) -> Vec<RoutedInferenceServerSnapshot> {
        let backends = self.generation.lock().backends.clone();
        backends
            .into_iter()
            .map(|backend| backend.as_ref().clone())
            .collect()
    }

    pub(super) fn select_backend(
        &self,
        failed_backend_ids: &HashSet<String>,
    ) -> Option<Arc<RoutedInferenceServerSnapshot>> {
        let generation = self.generation.lock();
        if generation.backends.is_empty() {
            return None;
        }
        if failed_backend_ids.is_empty() {
            let index = self.round_robin_counter.fetch_add(1, Ordering::Relaxed)
                % generation.backends.len();
            return Some(Arc::clone(&generation.backends[index]));
        }

        let mut eligible = generation
            .backends
            .iter()
            .filter(|backend| !failed_backend_ids.contains(&backend.inference_server_id));
        let eligible_count = eligible.clone().count();
        if eligible_count == 0 {
            return None;
        }
        let index = self.round_robin_counter.fetch_add(1, Ordering::Relaxed) % eligible_count;
        eligible.nth(index).map(Arc::clone)
    }

    pub(super) fn reserve_backend(
        &self,
        registration: &Arc<RegistrationGeneration>,
        input_tokens: u64,
        priority: u32,
    ) -> Option<RoutingReservation> {
        self.generation
            .lock()
            .reserve_backend(registration, input_tokens, priority)
    }

    #[cfg(test)]
    pub(super) fn backend_aggregate(&self) -> Option<(ModelStats, Duration, usize)> {
        self.generation.lock().backend_aggregate()
    }
}

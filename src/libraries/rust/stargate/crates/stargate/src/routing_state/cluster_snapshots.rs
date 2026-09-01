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

use std::collections::{BTreeSet, HashMap, HashSet};
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

const PROXY_LOCAL_REQUEST_LOAD_CAPABILITY: &str = "request.load.proxy_local";

fn set_legacy_source_stats(stats: &mut ModelStats, src: &ModelStats) {
    stats.max_output_tps = src.max_output_tps;
    stats.kv_cache_capacity_tokens = src.kv_cache_capacity_tokens;
    stats.kv_cache_used_tokens = src.kv_cache_used_tokens;
    stats.kv_cache_free_tokens = src.kv_cache_free_tokens;
    stats.num_running_queries = src.num_running_queries;
    stats.max_engine_concurrency = src.max_engine_concurrency;
    stats.total_query_input_size = src.total_query_input_size;
    stats.queue_time_estimate_ms_by_priority = src.queue_time_estimate_ms_by_priority.clone();
}

fn set_shared_engine_stats(stats: &mut ModelStats, src: &ModelStats) {
    stats.kv_cache_capacity_tokens = src.kv_cache_capacity_tokens;
    stats.kv_cache_used_tokens = src.kv_cache_used_tokens;
    stats.kv_cache_free_tokens = src.kv_cache_free_tokens;
    stats.max_engine_concurrency = src.max_engine_concurrency;
}

fn has_proxy_local_request_load(stats: &ModelStats) -> bool {
    stats
        .stats_capabilities
        .iter()
        .any(|capability| capability == PROXY_LOCAL_REQUEST_LOAD_CAPABILITY)
}

fn valid_output_tps(output_tps: f64) -> bool {
    output_tps >= 0.0 && output_tps.is_finite()
}

fn has_queued_work(stats: &ModelStats) -> bool {
    stats.queue_size > 0 || stats.queued_input_size > 0
}

fn proxy_local_wait_ms(stats: &ModelStats, priority: u32) -> u64 {
    if has_queued_work(stats) {
        crate::queue_estimate::priority_map_estimate_ms_for_priority(stats, priority)
            .unwrap_or_default()
    } else {
        0
    }
}

fn checked_ceil_u64(value: f64) -> Option<u64> {
    let rounded = value.ceil();
    // `u64::MAX as f64` rounds to 2^64, so the cast must saturate that boundary.
    (rounded.is_finite() && rounded >= 0.0 && rounded <= u64::MAX as f64).then_some(rounded as u64)
}

fn compensated_sum(values: impl IntoIterator<Item = f64>) -> f64 {
    let mut sum = 0.0_f64;
    let mut correction = 0.0_f64;
    for value in values {
        let next = sum + value;
        correction += if sum >= value {
            (sum - next) + value
        } else {
            (value - next) + sum
        };
        sum = next;
    }
    sum + correction
}

fn proxy_local_priority_map(backends: &[Arc<RoutedInferenceServerSnapshot>]) -> HashMap<u32, u64> {
    if backends.len() == 1 {
        return backends[0].stats.queue_time_estimate_ms_by_priority.clone();
    }

    if backends.iter().any(|backend| {
        has_queued_work(&backend.stats)
            && (!valid_last_mean_input_tps(backend.stats.last_mean_input_tps)
                || backend.stats.queue_time_estimate_ms_by_priority.is_empty())
    }) {
        return HashMap::new();
    }

    let priorities: BTreeSet<u32> = backends
        .iter()
        .flat_map(|backend| {
            backend
                .stats
                .queue_time_estimate_ms_by_priority
                .keys()
                .copied()
        })
        .collect();
    let input_tps_sum: f64 = backends
        .iter()
        .map(|backend| backend.stats.last_mean_input_tps)
        .filter(|input_tps| valid_last_mean_input_tps(*input_tps))
        .sum();
    if priorities.is_empty() || !valid_last_mean_input_tps(input_tps_sum) {
        return HashMap::new();
    }
    let max_input_tps = backends
        .iter()
        .map(|backend| backend.stats.last_mean_input_tps)
        .filter(|input_tps| valid_last_mean_input_tps(*input_tps))
        .fold(0.0_f64, f64::max);
    let valid_input_tps_count = backends
        .iter()
        .map(|backend| backend.stats.last_mean_input_tps)
        .filter(|input_tps| valid_last_mean_input_tps(*input_tps))
        .count();

    let mut aggregate = HashMap::with_capacity(priorities.len());
    for priority in priorities {
        let (min_wait_ms, max_wait_ms) = backends
            .iter()
            .filter(|backend| valid_last_mean_input_tps(backend.stats.last_mean_input_tps))
            .map(|backend| proxy_local_wait_ms(&backend.stats, priority))
            .fold((u64::MAX, 0), |(min_wait_ms, max_wait_ms), wait_ms| {
                (min_wait_ms.min(wait_ms), max_wait_ms.max(wait_ms))
            });
        if min_wait_ms == max_wait_ms {
            aggregate.insert(priority, min_wait_ms);
            continue;
        }

        let max_delta_ms = max_wait_ms - min_wait_ms;
        let max_unscaled_term = max_input_tps * max_delta_ms as f64;
        let unscaled_sum_is_finite = max_unscaled_term.is_finite()
            && max_unscaled_term <= f64::MAX / valid_input_tps_count as f64;
        // A common power-of-two divisor is exact for normal values. The
        // conservative exponent also bounds a worst-case usize-length sum.
        let rate_scale = if unscaled_sum_is_finite {
            1.0
        } else {
            2.0_f64.powi((u64::BITS + usize::BITS + 1) as i32)
        };
        let scaled_input_tps_sum = compensated_sum(
            backends
                .iter()
                .map(|backend| backend.stats.last_mean_input_tps)
                .filter(|input_tps| valid_last_mean_input_tps(*input_tps))
                .map(|input_tps| input_tps / rate_scale),
        );
        let weighted_delta_sum = compensated_sum(backends.iter().filter_map(|backend| {
            let input_tps = backend.stats.last_mean_input_tps;
            valid_last_mean_input_tps(input_tps).then(|| {
                let wait_ms = proxy_local_wait_ms(&backend.stats, priority);
                (input_tps / rate_scale) * wait_ms.saturating_sub(min_wait_ms) as f64
            })
        }));
        if !valid_last_mean_input_tps(scaled_input_tps_sum)
            || !weighted_delta_sum.is_finite()
            || weighted_delta_sum < 0.0
        {
            return HashMap::new();
        }
        let weighted_delta_ms = weighted_delta_sum / scaled_input_tps_sum;
        let Some(approximate_ceil_ms) =
            checked_ceil_u64(weighted_delta_ms.clamp(0.0, max_delta_ms as f64))
        else {
            return HashMap::new();
        };

        // Division can round an exact integer quotient one ULP upward or a
        // positive fraction downward. Resolve the adjacent integer boundary
        // by comparing the weighted residual on each side of it.
        let lower_delta_ms = approximate_ceil_ms.saturating_sub(1).min(max_delta_ms);
        let positive_residual = compensated_sum(backends.iter().filter_map(|backend| {
            let input_tps = backend.stats.last_mean_input_tps;
            if !valid_last_mean_input_tps(input_tps) {
                return None;
            }
            let delta_ms = proxy_local_wait_ms(&backend.stats, priority) - min_wait_ms;
            (delta_ms > lower_delta_ms)
                .then(|| (input_tps / rate_scale) * (delta_ms - lower_delta_ms) as f64)
        }));
        let negative_residual = compensated_sum(backends.iter().filter_map(|backend| {
            let input_tps = backend.stats.last_mean_input_tps;
            if !valid_last_mean_input_tps(input_tps) {
                return None;
            }
            let delta_ms = proxy_local_wait_ms(&backend.stats, priority) - min_wait_ms;
            (delta_ms < lower_delta_ms)
                .then(|| (input_tps / rate_scale) * (lower_delta_ms - delta_ms) as f64)
        }));
        if !positive_residual.is_finite() || !negative_residual.is_finite() {
            return HashMap::new();
        }
        let ceiled_delta_ms = if positive_residual > negative_residual {
            lower_delta_ms.saturating_add(1)
        } else {
            lower_delta_ms
        }
        .min(max_delta_ms);
        let weighted_wait_ms = min_wait_ms.saturating_add(ceiled_delta_ms).min(max_wait_ms);
        aggregate.insert(priority, weighted_wait_ms);
    }
    aggregate
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

    fn backend_aggregate(&self) -> Option<(ModelStats, Duration, usize, bool)> {
        let active_backend_count = self.backends.len();
        if active_backend_count == 0 {
            return None;
        }
        let proxy_local_request_load = self
            .backends
            .iter()
            .all(|backend| has_proxy_local_request_load(&backend.stats));
        let backend_count = active_backend_count as u128;
        let mut backend_stats = ModelStats::default();
        let mut valid_input_tps_components = 0_usize;
        let mut valid_output_tps_components = 0_usize;
        let mut valid_max_output_tps_components = 0_usize;
        let mut rtt_mean_nanos = 0_u128;
        let mut rtt_remainder_nanos = 0_u128;

        for backend in &self.backends {
            if proxy_local_request_load {
                if valid_last_mean_input_tps(backend.stats.last_mean_input_tps) {
                    backend_stats.last_mean_input_tps += backend.stats.last_mean_input_tps;
                    valid_input_tps_components += 1;
                }
                if valid_output_tps(backend.stats.output_tps) {
                    backend_stats.output_tps += backend.stats.output_tps;
                    valid_output_tps_components += 1;
                }
                if valid_output_tps(backend.stats.max_output_tps) {
                    backend_stats.max_output_tps = backend_stats
                        .max_output_tps
                        .max(backend.stats.max_output_tps);
                    valid_max_output_tps_components += 1;
                }
                backend_stats.queue_size = backend_stats
                    .queue_size
                    .saturating_add(backend.stats.queue_size);
                backend_stats.queued_input_size = backend_stats
                    .queued_input_size
                    .saturating_add(backend.stats.queued_input_size);
                backend_stats.num_running_queries = backend_stats
                    .num_running_queries
                    .saturating_add(backend.stats.num_running_queries);
                backend_stats.total_query_input_size = backend_stats
                    .total_query_input_size
                    .saturating_add(backend.stats.total_query_input_size);
                backend_stats.input_processing_queries = backend_stats
                    .input_processing_queries
                    .saturating_add(backend.stats.input_processing_queries);
                backend_stats.output_generation_queries = backend_stats
                    .output_generation_queries
                    .saturating_add(backend.stats.output_generation_queries);
            } else {
                backend_stats.output_tps += backend.stats.output_tps;
                if valid_last_mean_input_tps(backend.stats.last_mean_input_tps) {
                    backend_stats.last_mean_input_tps += backend.stats.last_mean_input_tps;
                }
                backend_stats.queue_size += backend.stats.queue_size;
                backend_stats.queued_input_size += backend.stats.queued_input_size;
                backend_stats.input_processing_queries += backend.stats.input_processing_queries;
                backend_stats.output_generation_queries += backend.stats.output_generation_queries;
            }
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

        if proxy_local_request_load {
            if valid_input_tps_components == 0 || !backend_stats.last_mean_input_tps.is_finite() {
                backend_stats.last_mean_input_tps = 0.0;
            }
            if valid_output_tps_components == 0 || !backend_stats.output_tps.is_finite() {
                backend_stats.output_tps = 0.0;
            }
            if valid_max_output_tps_components == 0 {
                backend_stats.max_output_tps = 0.0;
            }
            backend_stats.queue_time_estimate_ms_by_priority =
                proxy_local_priority_map(&self.backends);
        }

        // The loop maintains backend_count * mean + remainder = processed sum,
        // so the final mean is floor(total / backend_count). It cannot exceed
        // the largest input or Duration::MAX, and both casts are lossless.
        let rtt = Duration::new(
            (rtt_mean_nanos / 1_000_000_000) as u64,
            (rtt_mean_nanos % 1_000_000_000) as u32,
        );

        Some((
            backend_stats,
            rtt,
            active_backend_count,
            proxy_local_request_load,
        ))
    }

    fn refresh_snapshot(&mut self, updated_backend_id: Option<&str>) {
        let Some((backend_stats, rtt, active_backend_count, proxy_local_request_load)) =
            self.backend_aggregate()
        else {
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
        if proxy_local_request_load {
            set_shared_engine_stats(&mut stats, &source_backend.stats);
        } else {
            set_legacy_source_stats(&mut stats, &source_backend.stats);
        }
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
        self.generation
            .lock()
            .backend_aggregate()
            .map(|(stats, rtt, count, _)| (stats, rtt, count))
    }
}

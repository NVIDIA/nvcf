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

use super::keys::DeliveryTarget;
use super::reservations::PendingClusterReservation;
use super::*;

#[derive(Clone, Debug, Eq, Ord, PartialEq, PartialOrd)]
pub(super) struct ActiveModelSnapshot {
    pub(super) routing_key: Option<String>,
    pub(super) model_id: String,
}

#[derive(Debug, Default)]
pub(super) struct ActiveModelSnapshotState {
    pub(super) snapshot: RwLock<Vec<ActiveModelSnapshot>>,
}

impl ActiveModelSnapshotState {
    pub(super) fn replace(&self, models: Vec<ActiveModelSnapshot>) {
        *self.snapshot.write() = models;
    }

    pub(super) fn list(&self, routing_key: Option<&str>, model_ids: &[String]) -> Vec<String> {
        let model_filter = (!model_ids.is_empty()).then(|| {
            model_ids
                .iter()
                .map(String::as_str)
                .collect::<BTreeSet<_>>()
        });
        self.snapshot
            .read()
            .iter()
            .filter(|snapshot| snapshot.routing_key.as_deref() == routing_key)
            .filter(|snapshot| match &model_filter {
                Some(filter) => filter.contains(snapshot.model_id.as_str()),
                None => true,
            })
            .map(|snapshot| snapshot.model_id.clone())
            .collect()
    }
}

#[derive(Debug, Default)]
pub(super) struct RoutingTargetState {
    pub(super) clusters: SccHashMap<String, Arc<RoutedClusterState>>,
}

#[derive(Debug, Default)]
pub(super) struct RoutedClusterState {
    pub(super) inference_servers: SccHashMap<String, RoutedInferenceServerSnapshot>,
    pub(super) round_robin_counter: AtomicUsize,
    // The cluster owns the routable snapshot: backend-scoped load is aggregated
    // across active backends, while cluster-scoped fields are stored here and
    // refreshed from the latest registration update or local reservation logic.
    pub(super) cluster_snapshot: Mutex<Option<StoredClusterSnapshot>>,
}

#[derive(Clone, Debug)]
pub struct RoutedInferenceServerSnapshot {
    pub cluster_id: String,
    pub inference_server_id: String,
    pub inference_server_url: String,
    pub stats: ModelStats,
    pub rtt: Duration,
    pub snapshot_updated_at: Instant,
    pub status: InferenceServerStatus,
    pub reverse_tunnel: bool,
    pub delivery_target: DeliveryTarget,
}

#[derive(Clone, Debug)]
pub struct RoutedClusterSnapshot {
    pub cluster_id: String,
    pub stats: ModelStats,
    pub rtt: Duration,
    pub snapshot_updated_at: Instant,
    pub status: InferenceServerStatus,
    pub active_backend_count: usize,
}

#[derive(Clone, Debug)]
pub(super) struct StoredClusterSnapshot {
    pub(super) snapshot: RoutedClusterSnapshot,
    pub(super) cluster_stats_source_backend_id: String,
    // Raw cluster-scoped stats from registration heartbeats. Pending local
    // reservations are tracked separately so unrelated backend heartbeats do
    // not wipe optimistic load before the chosen backend reports again.
    pub(super) cluster_stats_base: ModelStats,
    pub(super) raw_cluster_updates: HashMap<String, ClusterScopedUpdate>,
    pub(super) pending_cluster_reservations: BTreeMap<u64, PendingClusterReservation>,
}

#[derive(Clone, Debug)]
pub(super) struct ClusterScopedUpdate {
    pub(super) source_backend_id: String,
    pub(super) stats: ModelStats,
    pub(super) snapshot_updated_at: Instant,
    pub(super) status: InferenceServerStatus,
}

pub(super) struct RoutedInferenceServerSnapshotInput<'a> {
    pub(super) cluster_id: &'a str,
    pub(super) inference_server_id: &'a str,
    pub(super) inference_server_url: &'a str,
    pub(super) stats: ModelStats,
    pub(super) rtt: Duration,
    pub(super) snapshot_updated_at: Instant,
    pub(super) status: InferenceServerStatus,
    pub(super) reverse_tunnel: bool,
    pub(super) delivery_target: DeliveryTarget,
}

pub struct RegisteredReverseTunnel {
    // Reverse-tunnel authentication proves the registration's routing scope,
    // while the pylon-advertised inference_server_id selects the live registration.
    // Duplicate live IDs are rejected when that registration stream starts.
    pub routing_key: Option<String>,
}

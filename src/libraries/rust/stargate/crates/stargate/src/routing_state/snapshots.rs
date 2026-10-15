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

use super::registration::{RegistrationClusterGeneration, RegistrationGeneration};
use super::reservations::{PendingClusterReservation, RoutingReservation};
use super::*;
use crate::load_balancer::LoadBalancerTargetState;
use parking_lot::Mutex;
use std::collections::{HashMap, HashSet};

#[derive(Debug, Default)]
pub(super) struct RoutingTargetState {
    pub(super) generation: Mutex<RoutingTargetGeneration>,
    pub(super) load_balancers: LoadBalancerTargetState,
}

#[derive(Debug)]
pub(super) enum RoutingTargetGeneration {
    Active {
        clusters: HashMap<String, Arc<RoutedClusterState>>,
        active_backend_count: usize,
    },
    Retired,
}

impl Default for RoutingTargetGeneration {
    fn default() -> Self {
        Self::Active {
            clusters: HashMap::new(),
            active_backend_count: 0,
        }
    }
}

#[derive(Debug)]
pub(crate) struct RoutingTargetSnapshot {
    target_state: Arc<RoutingTargetState>,
    clusters: Vec<RoutedClusterSnapshot>,
    cluster_owners: Vec<Arc<RoutedClusterState>>,
}

impl RoutingTargetSnapshot {
    pub(super) fn new(
        target_state: Arc<RoutingTargetState>,
        clusters: Vec<(RoutedClusterSnapshot, Arc<RoutedClusterState>)>,
    ) -> Self {
        let (clusters, cluster_owners) = clusters.into_iter().unzip();
        Self {
            target_state,
            clusters,
            cluster_owners,
        }
    }

    pub(crate) fn load_balancers(&self) -> &LoadBalancerTargetState {
        &self.target_state.load_balancers
    }

    pub(crate) fn clusters(&self) -> &[RoutedClusterSnapshot] {
        &self.clusters
    }

    pub(crate) fn into_clusters(self) -> Vec<RoutedClusterSnapshot> {
        self.clusters
    }

    pub(crate) fn into_selected_cluster(mut self, index: usize) -> SelectedRoutedCluster {
        assert_eq!(
            self.clusters.len(),
            self.cluster_owners.len(),
            "routing target snapshot cluster data and owners must remain aligned"
        );
        SelectedRoutedCluster {
            snapshot: self.clusters.swap_remove(index),
            owner: self.cluster_owners.swap_remove(index),
        }
    }

    #[cfg(test)]
    pub(crate) fn for_test(clusters: Vec<RoutedClusterSnapshot>) -> Self {
        let clusters = clusters
            .into_iter()
            .map(|snapshot| {
                let registration =
                    super::registration::test_registration_generation(RegistrationIdentity {
                        inference_server_id: format!("{}-test-owner", snapshot.cluster_id),
                        cluster_id: snapshot.cluster_id.clone(),
                        inference_server_url: "quic://127.0.0.1:5000".to_string(),
                        routing_key: None,
                        reverse_tunnel: false,
                    });
                let owner = Arc::new(RoutedClusterState::new(
                    registration.cluster_generation.clone(),
                ));
                (snapshot, owner)
            })
            .collect();
        Self::new(Arc::new(RoutingTargetState::default()), clusters)
    }
}

#[derive(Debug)]
pub(crate) struct SelectedRoutedCluster {
    snapshot: RoutedClusterSnapshot,
    owner: Arc<RoutedClusterState>,
}

impl SelectedRoutedCluster {
    pub(crate) fn snapshot(&self) -> &RoutedClusterSnapshot {
        &self.snapshot
    }

    pub(crate) fn select_backend(
        &self,
        failed_backend_ids: &HashSet<String>,
    ) -> Option<Arc<RoutedInferenceServerSnapshot>> {
        self.owner.select_backend(failed_backend_ids)
    }

    pub(crate) fn reserve_backend(
        &self,
        registration: &Arc<RegistrationGeneration>,
        input_tokens: u64,
        priority: u32,
    ) -> Option<RoutingReservation> {
        self.owner
            .reserve_backend(registration, input_tokens, priority)
    }
}

#[derive(Debug)]
pub(super) struct RoutedClusterState {
    pub(super) cluster_generation: Arc<RegistrationClusterGeneration>,
    pub(super) generation: Mutex<ClusterRoutingGeneration>,
    pub(super) round_robin_counter: AtomicUsize,
}

#[derive(Debug, Default)]
pub(super) struct ClusterRoutingGeneration {
    // Request routing is the hot path, so immutable backend publications are
    // shared in stable ID order and replaced by the lower-frequency
    // registration path.
    pub(super) backends: Vec<Arc<RoutedInferenceServerSnapshot>>,
    pub(super) snapshot_state: Option<ClusterSnapshotState>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(super) enum ClusterBackendUpsert {
    Inserted,
    Replaced,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(super) enum ClusterBackendRemoval {
    Missing,
    Removed,
    Emptied,
}

#[derive(Clone, Debug)]
pub struct RoutedInferenceServerSnapshot {
    pub(crate) registration: Arc<RegistrationGeneration>,
    pub cluster_id: String,
    pub inference_server_id: String,
    pub inference_server_url: String,
    pub stats: ModelStats,
    pub rtt: Duration,
    pub snapshot_updated_at: Instant,
    pub status: InferenceServerStatus,
    pub reverse_tunnel: bool,
}

impl RoutedInferenceServerSnapshot {
    pub(super) fn new(
        registration: Arc<RegistrationGeneration>,
        stats: ModelStats,
        rtt: Duration,
        snapshot_updated_at: Instant,
        status: InferenceServerStatus,
    ) -> Self {
        Self {
            cluster_id: registration.identity.cluster_id.clone(),
            inference_server_id: registration.identity.inference_server_id.clone(),
            inference_server_url: registration.identity.inference_server_url.clone(),
            stats,
            rtt,
            snapshot_updated_at,
            status,
            reverse_tunnel: registration.identity.reverse_tunnel,
            registration,
        }
    }

    pub(super) fn assert_registration_identity(&self) {
        let identity = &self.registration.identity;
        assert_eq!(
            self.cluster_id, identity.cluster_id,
            "routed snapshot cluster ID must match exact registration"
        );
        assert_eq!(
            self.inference_server_id, identity.inference_server_id,
            "routed snapshot inference-server ID must match exact registration"
        );
        assert_eq!(
            self.inference_server_url, identity.inference_server_url,
            "routed snapshot inference-server URL must match exact registration"
        );
        assert_eq!(
            self.reverse_tunnel, identity.reverse_tunnel,
            "routed snapshot tunnel direction must match exact registration"
        );
    }
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

#[derive(Debug)]
pub(super) struct ClusterSnapshotState {
    // Pending reservations are applied when a routing snapshot is read.
    // Keeping this base unprojected prevents stale derived values from becoming
    // another source of truth.
    pub(super) base_snapshot: RoutedClusterSnapshot,
    // Retain source identity so the latest processed heartbeat wins even when
    // receive timestamps tie; its stats remain owned by `backends`.
    pub(super) cluster_stats_source_backend_id: String,
    // Pending local reservations remain separate from heartbeat-owned backend
    // snapshots so unrelated backend heartbeats do not wipe optimistic load.
    pub(super) pending_cluster_reservations: Vec<Arc<PendingClusterReservation>>,
}

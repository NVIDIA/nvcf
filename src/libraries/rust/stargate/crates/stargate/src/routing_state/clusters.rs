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

use super::keys::RoutingTargetKey;
use super::registration::RegistrationGeneration;
use super::snapshots::{
    ClusterBackendRemoval, ClusterBackendUpsert, RoutedClusterSnapshot, RoutedClusterState,
    RoutedInferenceServerSnapshot, RoutingTargetGeneration, RoutingTargetSnapshot,
    RoutingTargetState,
};
use super::*;

#[derive(Debug, Default)]
pub(super) struct RoutingLifecycle {
    targets: SccHashMap<RoutingTargetKey, Arc<RoutingTargetState>>,
    metrics: Option<Arc<StargateMetrics>>,
}

impl RoutingTargetState {
    pub(super) fn upsert_backend(
        &self,
        backend: Arc<RoutedInferenceServerSnapshot>,
    ) -> Result<(), Arc<RoutedInferenceServerSnapshot>> {
        let cluster_id = backend.registration.identity.cluster_id.clone();
        let cluster_generation = backend.registration.cluster_generation.clone();
        assert!(
            !cluster_generation.is_retired(),
            "retired registration cluster generation cannot publish routing state"
        );
        let mut generation = self.generation.lock();
        let RoutingTargetGeneration::Active {
            clusters,
            active_backend_count,
        } = &mut *generation
        else {
            return Err(backend);
        };

        let Some(cluster_state) = clusters.get(&cluster_id) else {
            let cluster_state = Arc::new(RoutedClusterState::new(cluster_generation));
            cluster_state.upsert_backend(backend);
            clusters.insert(cluster_id, cluster_state);
            update_active_backend_count(active_backend_count, 0, 1);
            return Ok(());
        };

        if Arc::ptr_eq(&cluster_state.cluster_generation, &cluster_generation) {
            if cluster_state.upsert_backend(backend) == ClusterBackendUpsert::Inserted {
                update_active_backend_count(active_backend_count, 0, 1);
            }
            return Ok(());
        }

        assert!(
            cluster_state.cluster_generation.is_retired(),
            "one routing target cannot contain different active generations of the same cluster"
        );
        let stale_backend_count = cluster_state.generation.lock().backends.len();
        let replacement = Arc::new(RoutedClusterState::new(cluster_generation));
        replacement.upsert_backend(backend);
        clusters
            .insert(cluster_id, replacement)
            .expect("existing routed cluster generation should be replaced");
        update_active_backend_count(active_backend_count, stale_backend_count, 1);
        Ok(())
    }

    pub(super) fn remove_backend(&self, registration: &Arc<RegistrationGeneration>) {
        let cluster_id = &registration.identity.cluster_id;
        let mut generation = self.generation.lock();
        let RoutingTargetGeneration::Active {
            clusters,
            active_backend_count,
        } = &mut *generation
        else {
            return;
        };
        let Some(cluster_state) = clusters.get(cluster_id) else {
            return;
        };

        let removed_backend_count = if !Arc::ptr_eq(
            &cluster_state.cluster_generation,
            &registration.cluster_generation,
        ) {
            if !cluster_state.cluster_generation.is_retired() {
                assert!(
                    registration.cluster_generation.is_retired(),
                    "one routing target cannot contain different active generations of the same cluster"
                );
                return;
            }
            let backend_count = cluster_state.generation.lock().backends.len();
            clusters.remove(cluster_id).expect(
                "retired routed cluster generation should remain target-owned until removal",
            );
            backend_count
        } else {
            match cluster_state.remove_backend(registration) {
                ClusterBackendRemoval::Missing => return,
                ClusterBackendRemoval::Removed => 1,
                ClusterBackendRemoval::Emptied => {
                    clusters
                        .remove(cluster_id)
                        .expect("emptied cluster should remain target-owned until removal");
                    1
                }
            }
        };
        update_active_backend_count(active_backend_count, removed_backend_count, 0);
    }

    fn cluster_states(&self) -> Vec<Arc<RoutedClusterState>> {
        match &*self.generation.lock() {
            RoutingTargetGeneration::Active { clusters, .. } => {
                clusters.values().cloned().collect()
            }
            RoutingTargetGeneration::Retired => Vec::new(),
        }
    }

    pub(super) fn active_backend_count(&self) -> usize {
        match &*self.generation.lock() {
            RoutingTargetGeneration::Active {
                active_backend_count,
                ..
            } => *active_backend_count,
            RoutingTargetGeneration::Retired => 0,
        }
    }

    fn retire_if_empty(&self) -> bool {
        let mut generation = self.generation.lock();
        let RoutingTargetGeneration::Active {
            clusters,
            active_backend_count,
        } = &*generation
        else {
            return true;
        };
        if !clusters.is_empty() {
            return false;
        }
        assert_eq!(
            *active_backend_count, 0,
            "empty routing target generation must not retain active backends"
        );
        *generation = RoutingTargetGeneration::Retired;
        true
    }
}

impl RoutingLifecycle {
    pub(super) fn new(metrics: Option<Arc<StargateMetrics>>) -> Self {
        Self {
            metrics,
            ..Self::default()
        }
    }

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
            if let Some(state) = self.existing_or_inserted_target(target).await {
                return state;
            }
        }
    }

    async fn existing_or_inserted_target(
        &self,
        target: &RoutingTargetKey,
    ) -> Option<Arc<RoutingTargetState>> {
        if let Some(existing) = self.target_state(target).await {
            return Some(existing);
        }
        let candidate = Arc::new(RoutingTargetState::default());
        match self
            .targets
            .insert_async(target.clone(), candidate.clone())
            .await
        {
            Ok(()) => Some(candidate),
            Err(_) => None,
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

    pub(super) async fn matching_targets(
        &self,
        routing_key: Option<&str>,
        model_ids: &[String],
    ) -> Vec<(RoutingTargetKey, Arc<RoutingTargetState>)> {
        if model_ids.is_empty() {
            return self
                .targets()
                .await
                .into_iter()
                .filter(|(target, _state)| target.routing_key.as_deref() == routing_key)
                .collect();
        }

        let routing_key = routing_key.map(ToOwned::to_owned);
        let mut targets = Vec::new();
        for model_id in model_ids.iter().collect::<BTreeSet<_>>() {
            let target = RoutingTargetKey::new(routing_key.clone(), model_id);
            if let Some(target_state) = self.target_state(&target).await {
                targets.push((target, target_state));
            }
        }
        targets
    }

    async fn publish_active_backend_count(&self, target: &RoutingTargetKey) {
        let Some(metrics) = &self.metrics else {
            return;
        };
        loop {
            let before = self.target_state(target).await;
            let count = before
                .as_ref()
                .map_or(0, |target_state| target_state.active_backend_count());
            metrics.set_active_inference_servers(
                target.routing_key.as_deref(),
                &target.model_id,
                count,
            );

            let stable = match &before {
                None => self.target_state(target).await.is_none(),
                Some(before) => self.target_state(target).await.is_some_and(|after| {
                    Arc::ptr_eq(before, &after) && after.active_backend_count() == count
                }),
            };
            if stable {
                return;
            }
        }
    }

    pub(super) async fn remove_if_empty(
        &self,
        target: &RoutingTargetKey,
        target_state: Arc<RoutingTargetState>,
    ) -> bool {
        // Retire while the current map entry is write-locked so a stale owner
        // cannot publish into detached state after a replacement is inserted.
        self.targets
            .remove_if_async(target, move |current| {
                Arc::ptr_eq(current, &target_state) && current.retire_if_empty()
            })
            .await
            .is_some()
    }

    pub(super) async fn upsert_inference_server_target(
        &self,
        target: &RoutingTargetKey,
        snapshot: RoutedInferenceServerSnapshot,
    ) {
        let mut snapshot = Arc::new(snapshot);
        loop {
            let target_state = self.target_state_or_insert(target).await;
            let Err(rejected) = target_state.upsert_backend(snapshot) else {
                break;
            };
            snapshot = rejected;
            let _ = self.remove_if_empty(target, target_state).await;
        }
        self.publish_active_backend_count(target).await;
    }

    pub(super) async fn remove_inference_server_from_target(
        &self,
        registration: &Arc<RegistrationGeneration>,
        target: &RoutingTargetKey,
    ) {
        let Some(target_state) = self.target_state(target).await else {
            return;
        };

        target_state.remove_backend(registration);
        let _ = self.remove_if_empty(target, target_state).await;
        self.publish_active_backend_count(target).await;
    }

    pub(super) async fn remove_inference_server_targets(
        &self,
        registration: &Arc<RegistrationGeneration>,
        targets: &HashSet<RoutingTargetKey>,
    ) {
        for target in targets {
            self.remove_inference_server_from_target(registration, target)
                .await;
        }
    }

    pub(super) async fn candidates_for_target(
        &self,
        target: &RoutingTargetKey,
    ) -> Vec<RoutedInferenceServerSnapshot> {
        self.target_state(target)
            .await
            .into_iter()
            .flat_map(|target_state| target_state.cluster_states())
            .flat_map(|cluster| cluster.backend_snapshot_values())
            .collect()
    }

    pub(super) async fn cluster_candidates_for_target(
        &self,
        target: &RoutingTargetKey,
    ) -> Vec<RoutedClusterSnapshot> {
        self.routing_target_snapshot(target)
            .await
            .map_or_else(Vec::new, RoutingTargetSnapshot::into_clusters)
    }

    pub(super) async fn routing_target_snapshot(
        &self,
        target: &RoutingTargetKey,
    ) -> Option<RoutingTargetSnapshot> {
        let target_state = self.target_state(target).await?;

        let mut clusters = Vec::new();
        for cluster_state in target_state.cluster_states() {
            if let Some(snapshot) = cluster_state.routing_snapshot() {
                clusters.push((snapshot, cluster_state));
            }
        }
        Some(RoutingTargetSnapshot::new(target_state, clusters))
    }

    pub(super) async fn list_active_models(
        &self,
        routing_key: Option<&str>,
        model_ids: &[String],
    ) -> Vec<String> {
        active_model_ids(self.matching_targets(routing_key, model_ids).await)
    }

    pub(super) async fn list_active_models_for_debug(&self) -> Vec<String> {
        active_model_ids(self.targets().await)
    }
}

fn update_active_backend_count(count: &mut usize, removed: usize, added: usize) {
    *count = count
        .checked_sub(removed)
        .and_then(|count| count.checked_add(added))
        .expect("routing target active backend count overflow or underflow");
}

fn active_model_ids(targets: Vec<(RoutingTargetKey, Arc<RoutingTargetState>)>) -> Vec<String> {
    targets
        .into_iter()
        .filter_map(|(target, state)| (state.active_backend_count() > 0).then_some(target.model_id))
        .collect::<BTreeSet<_>>()
        .into_iter()
        .collect()
}

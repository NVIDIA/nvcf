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

use parking_lot::{Mutex, RwLock};
use scc::HashMap as SccHashMap;
use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, AtomicUsize, Ordering};
use std::time::{Duration, Instant};
use tonic::Status;
use tracing::warn;

use crate::metrics::StargateMetrics;
use stargate_proto::pb::{
    CalibrationState, InferenceServerRegistration, InferenceServerStatus,
    ModelCalibrationDirective, ModelStats, SubmitClusterCalibrationRequest,
};

mod calibration;
mod clusters;
mod keys;
mod registration;
mod reservations;
mod snapshots;

pub use keys::{DeliveryTarget, RoutingTargetKey};
pub use snapshots::{
    RegisteredReverseTunnel, RoutedClusterSnapshot, RoutedInferenceServerSnapshot,
};

pub(crate) use keys::RegistrationIdentity;
pub(crate) use registration::RunningRegistration;
pub(crate) use reservations::RoutingReservation;

#[cfg(test)]
use clusters::RoutingLifecycle;
use snapshots::RoutedInferenceServerSnapshotInput;
#[cfg(test)]
use snapshots::RoutingTargetState;

#[derive(Debug)]
pub struct StargateState {
    registrations: registration::RegistrationLifecycle,
    routing: clusters::RoutingLifecycle,
    calibration_assignments: calibration::ClusterCalibrationAssignments,
}

impl Default for StargateState {
    fn default() -> Self {
        Self::new()
    }
}

impl StargateState {
    pub fn new() -> Self {
        Self::new_inner(None)
    }

    pub fn new_with_metrics(metrics: Arc<StargateMetrics>) -> Self {
        Self::new_inner(Some(metrics))
    }

    fn new_inner(metrics: Option<Arc<StargateMetrics>>) -> Self {
        Self {
            registrations: registration::RegistrationLifecycle::default(),
            routing: clusters::RoutingLifecycle::new(metrics),
            calibration_assignments: calibration::ClusterCalibrationAssignments::default(),
        }
    }

    pub(crate) async fn begin_registration(
        &self,
        identity: &RegistrationIdentity,
    ) -> Result<RunningRegistration, Status> {
        self.registrations.begin_registration(identity).await
    }

    pub(crate) async fn end_registration(&self, inference_server_id: &str) {
        let Some(registration) = self
            .registrations
            .end_registration(inference_server_id)
            .await
        else {
            return;
        };

        let routing_key = registration.identity.routing_key.clone();
        let mut model_ids = HashSet::new();
        let _ = registration
            .registered_models
            .iter_async(|model_id, _registered| {
                model_ids.insert(model_id.clone());
                true
            })
            .await;
        let targets: HashSet<RoutingTargetKey> = model_ids
            .into_iter()
            .map(|model_id| RoutingTargetKey {
                routing_key: routing_key.clone(),
                model_id,
            })
            .collect();
        self.release_cluster_calibration(&registration.identity)
            .await;
        self.routing
            .remove_inference_server_targets(
                inference_server_id,
                &registration.identity.cluster_id,
                &targets,
                &self.calibration_assignments,
            )
            .await;
    }

    async fn calibration_directive_for_model(
        &self,
        running: &RunningRegistration,
        model_id: &str,
    ) -> Option<ModelCalibrationDirective> {
        self.calibration_assignments
            .directive_for_model(running, model_id)
            .await
    }

    pub(crate) async fn submit_cluster_calibration(
        &self,
        routing_key: Option<String>,
        request: &SubmitClusterCalibrationRequest,
    ) -> Result<(), Status> {
        self.calibration_assignments
            .submit(routing_key, request)
            .await
    }

    async fn release_cluster_calibration(&self, identity: &RegistrationIdentity) {
        // Completed floors live for the local cluster lifetime. An unfinished
        // assignment is invalidated when its owner registration disappears.
        let cluster_still_registered = self
            .registrations
            .cluster_has_local_registration(identity)
            .await;
        self.calibration_assignments
            .release_for_registration(identity, cluster_still_registered)
            .await;
    }

    async fn release_removed_model_assignment(
        &self,
        identity: &RegistrationIdentity,
        removed_models: &HashSet<String>,
    ) {
        self.calibration_assignments
            .release_removed_model_assignment(identity, removed_models)
            .await;
    }

    pub(crate) async fn apply_registration_update(
        &self,
        running: &mut RunningRegistration,
        update: &InferenceServerRegistration,
        reverse_connected: bool,
        rtt: Option<Duration>,
    ) -> Vec<ModelCalibrationDirective> {
        let routing_key = running.routing_key();
        let mut calibration_directives = Vec::new();

        running.set_last_rtt(rtt);

        let registered_models = running.registered_model_ids().await;
        let current_models: HashSet<String> = update.models.keys().cloned().collect();
        let removed_models: HashSet<String> = registered_models
            .difference(&current_models)
            .cloned()
            .collect();
        let removed_targets: HashSet<RoutingTargetKey> = removed_models
            .iter()
            .map(|model_id| RoutingTargetKey {
                routing_key: routing_key.clone(),
                model_id: model_id.clone(),
            })
            .collect();
        self.release_removed_model_assignment(&running.identity, &removed_models)
            .await;
        self.routing
            .remove_inference_server_targets(
                &running.identity.inference_server_id,
                &running.identity.cluster_id,
                &removed_targets,
                &self.calibration_assignments,
            )
            .await;
        for model_id in &removed_models {
            running.remove_registered_model(model_id).await;
        }

        for (model_id, model) in &update.models {
            // Identical stats across consecutive updates are expected because
            // heartbeat sends carry full registration snapshots.
            let target = RoutingTargetKey {
                routing_key: routing_key.clone(),
                model_id: model_id.clone(),
            };
            let calibration_directive = self
                .calibration_directive_for_model(running, model_id)
                .await;
            if let Some(directive) = calibration_directive.clone() {
                calibration_directives.push(directive);
            }
            let calibration_pending = calibration_directive.as_ref().is_some()
                && self
                    .calibration_assignments
                    .completed_last_mean_input_tps(&target, &running.identity.cluster_id)
                    .await
                    .is_none();
            let stats = model.stats.clone().unwrap_or_default();
            let model_status = InferenceServerStatus::try_from(model.status)
                .unwrap_or(InferenceServerStatus::Unknown);
            let effective_status =
                if (running.identity.reverse_tunnel && !reverse_connected) || calibration_pending {
                    InferenceServerStatus::Inactive
                } else if model.stats.is_none() {
                    warn!(
                        inference_server_id = %running.identity.inference_server_id,
                        model_id = %model_id,
                        "missing model stats in registration; setting model status to inactive"
                    );
                    InferenceServerStatus::Inactive
                } else {
                    model_status
                };

            running.mark_model_registered(model_id).await;

            if effective_status == InferenceServerStatus::Active {
                let Some(current_rtt) = rtt else {
                    warn!(
                        inference_server_id = %running.identity.inference_server_id,
                        model_id = %model_id,
                        "active model registration missing connection RTT; skipping routing update"
                    );
                    self.routing
                        .remove_inference_server_from_target(
                            &running.identity.inference_server_id,
                            &running.identity.cluster_id,
                            &target,
                            &self.calibration_assignments,
                        )
                        .await;
                    continue;
                };
                self.routing
                    .upsert_inference_server_target(
                        &target,
                        RoutedInferenceServerSnapshotInput {
                            cluster_id: &running.identity.cluster_id,
                            inference_server_id: &running.identity.inference_server_id,
                            inference_server_url: &running.identity.inference_server_url,
                            stats,
                            rtt: current_rtt,
                            snapshot_updated_at: Instant::now(),
                            status: effective_status,
                            reverse_tunnel: running.identity.reverse_tunnel,
                            delivery_target: DeliveryTarget::Local {
                                inference_server_id: running.identity.inference_server_id.clone(),
                            },
                        },
                        &self.calibration_assignments,
                    )
                    .await;
            } else {
                self.routing
                    .remove_inference_server_from_target(
                        &running.identity.inference_server_id,
                        &running.identity.cluster_id,
                        &target,
                        &self.calibration_assignments,
                    )
                    .await;
            }
        }

        calibration_directives
    }

    /// Returns all active inference server snapshots for a
    /// `(routing_key, model_id)` pair. The HTTP proxy calls this to get the
    /// candidate set that the load balancer chooses from.
    pub async fn candidates_for_target(
        &self,
        target: &RoutingTargetKey,
    ) -> Vec<RoutedInferenceServerSnapshot> {
        self.routing.candidates_for_target(target).await
    }

    pub async fn cluster_candidates_for_target(
        &self,
        target: &RoutingTargetKey,
    ) -> Vec<RoutedClusterSnapshot> {
        self.routing
            .cluster_candidates_for_target(target, &self.calibration_assignments)
            .await
    }

    pub async fn has_registered_model_for_target(&self, target: &RoutingTargetKey) -> bool {
        self.registrations
            .has_registered_model_for_target(target)
            .await
    }

    pub async fn refresh_active_models_snapshot(&self) {
        self.routing
            .refresh_active_models_snapshot(&self.calibration_assignments)
            .await;
    }

    pub fn list_active_models(
        &self,
        routing_key: Option<&str>,
        model_ids: &[String],
    ) -> Vec<String> {
        self.routing.list_active_models(routing_key, model_ids)
    }

    pub async fn select_backend_for_cluster(
        &self,
        target: &RoutingTargetKey,
        cluster_id: &str,
        failed_backend_ids: &HashSet<String>,
    ) -> Option<RoutedInferenceServerSnapshot> {
        self.routing
            .select_backend_for_cluster(target, cluster_id, failed_backend_ids)
            .await
    }

    pub(crate) async fn reserve_inference_server_for_target(
        &self,
        target: &RoutingTargetKey,
        inference_server_id: &str,
        input_tokens: Option<u64>,
        priority: u32,
    ) -> Option<RoutingReservation> {
        self.routing
            .reserve_inference_server_for_target(
                target,
                inference_server_id,
                input_tokens,
                priority,
            )
            .await
    }

    pub(crate) async fn release_inference_server_reservation_for_target(
        &self,
        target: &RoutingTargetKey,
        reservation: RoutingReservation,
    ) {
        self.routing
            .release_inference_server_reservation_for_target(
                target,
                reservation,
                &self.calibration_assignments,
            )
            .await;
    }

    /// Looks up the registration for an inference server that declared
    /// `reverse_tunnel = true` during gRPC registration. Returns `None` if
    /// the server is not registered or was registered without reverse tunnel
    /// mode.
    ///
    /// Called during the QUIC reverse-tunnel handshake to confirm the
    /// connecting server was expected and to retrieve the auth-derived
    /// routing key for comparison against the QUIC handshake's own auth
    /// result.
    pub async fn registered_reverse_tunnel(
        &self,
        inference_server_id: &str,
    ) -> Option<RegisteredReverseTunnel> {
        self.registrations
            .registered_reverse_tunnel(inference_server_id)
            .await
    }
}

#[cfg(test)]
mod tests;

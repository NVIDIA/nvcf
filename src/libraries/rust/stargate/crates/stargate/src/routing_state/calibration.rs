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

use super::keys::{RegistrationIdentity, RoutingTargetKey};
use super::registration::RunningRegistration;
use super::*;

#[derive(Debug, Default)]
pub(super) struct ClusterCalibrationAssignments {
    pub(super) assignments: SccHashMap<ClusterCalibrationKey, Arc<Mutex<ClusterCalibrationState>>>,
}

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub(super) struct ClusterCalibrationKey {
    routing_key: Option<String>,
    cluster_id: String,
    model_id: String,
}

#[derive(Clone, Debug, PartialEq)]
pub(super) enum ClusterCalibrationState {
    Assigned {
        owner_inference_server_id: String,
        assignment_token: String,
    },
    Complete {
        owner_inference_server_id: String,
        assignment_token: String,
        last_mean_input_tps: f64,
    },
}

impl ClusterCalibrationAssignments {
    pub(super) async fn directive_for_model(
        &self,
        running: &RunningRegistration,
        model_id: &str,
    ) -> Option<ModelCalibrationDirective> {
        if !running.identity.coordinated_calibration {
            return None;
        }

        let key = ClusterCalibrationKey {
            routing_key: running.identity.routing_key.clone(),
            cluster_id: running.identity.cluster_id.clone(),
            model_id: model_id.to_string(),
        };

        loop {
            if let Some(existing) = self
                .assignments
                .read_async(&key, |_key, state| state.clone())
                .await
            {
                let mut state = existing.lock();
                return Some(match &mut *state {
                    ClusterCalibrationState::Assigned {
                        owner_inference_server_id,
                        assignment_token,
                    } if owner_inference_server_id == &running.identity.inference_server_id => {
                        calibration_run_directive(model_id, assignment_token)
                    }
                    ClusterCalibrationState::Assigned { .. } => {
                        calibration_wait_directive(model_id)
                    }
                    ClusterCalibrationState::Complete { .. } => {
                        calibration_complete_directive(model_id)
                    }
                });
            }

            let assignment_token = new_cluster_calibration_assignment_token();
            let initial_state = ClusterCalibrationState::Assigned {
                owner_inference_server_id: running.identity.inference_server_id.clone(),
                assignment_token: assignment_token.clone(),
            };
            let inserted = self
                .assignments
                .insert_async(key.clone(), Arc::new(Mutex::new(initial_state)))
                .await
                .is_ok();
            if inserted {
                return Some(calibration_run_directive(model_id, &assignment_token));
            }
        }
    }

    pub(super) async fn submit(
        &self,
        routing_key: Option<String>,
        request: &SubmitClusterCalibrationRequest,
    ) -> Result<(), Status> {
        if request.inference_server_id.is_empty()
            || request.cluster_id.is_empty()
            || request.model_id.is_empty()
            || request.assignment_token.is_empty()
        {
            return Err(Status::invalid_argument(
                "cluster calibration submission identity and assignment token must be non-empty",
            ));
        }
        if !valid_last_mean_input_tps(request.measured_last_mean_input_tps) {
            return Err(Status::invalid_argument(
                "measured_last_mean_input_tps must be positive and finite",
            ));
        }

        let key = ClusterCalibrationKey {
            routing_key,
            cluster_id: request.cluster_id.clone(),
            model_id: request.model_id.clone(),
        };
        let Some(existing) = self
            .assignments
            .read_async(&key, |_key, state| state.clone())
            .await
        else {
            return Err(Status::failed_precondition(
                "cluster calibration has no active local assignment",
            ));
        };

        let mut state = existing.lock();
        match &*state {
            ClusterCalibrationState::Assigned {
                owner_inference_server_id,
                assignment_token,
            } if owner_inference_server_id == &request.inference_server_id
                && assignment_token == &request.assignment_token =>
            {
                *state = ClusterCalibrationState::Complete {
                    owner_inference_server_id: request.inference_server_id.clone(),
                    assignment_token: request.assignment_token.clone(),
                    last_mean_input_tps: request.measured_last_mean_input_tps,
                };
                Ok(())
            }
            ClusterCalibrationState::Complete {
                owner_inference_server_id,
                assignment_token,
                last_mean_input_tps,
            } if owner_inference_server_id == &request.inference_server_id
                && assignment_token == &request.assignment_token
                && last_mean_input_tps.to_bits()
                    == request.measured_last_mean_input_tps.to_bits() =>
            {
                Ok(())
            }
            ClusterCalibrationState::Assigned { .. } => Err(Status::failed_precondition(
                "cluster calibration submission does not own the local assignment",
            )),
            ClusterCalibrationState::Complete { .. } => Err(Status::failed_precondition(
                "cluster calibration was already completed by another submission",
            )),
        }
    }

    pub(super) async fn completed_last_mean_input_tps(
        &self,
        target: &RoutingTargetKey,
        cluster_id: &str,
    ) -> Option<f64> {
        let key = ClusterCalibrationKey {
            routing_key: target.routing_key.clone(),
            cluster_id: cluster_id.to_string(),
            model_id: target.model_id.clone(),
        };
        self.assignments
            .read_async(&key, |_key, state| match &*state.lock() {
                ClusterCalibrationState::Complete {
                    last_mean_input_tps,
                    ..
                } => Some(*last_mean_input_tps),
                ClusterCalibrationState::Assigned { .. } => None,
            })
            .await
            .flatten()
    }

    pub(super) async fn release_for_registration(
        &self,
        identity: &RegistrationIdentity,
        cluster_still_registered: bool,
    ) {
        let mut keys_to_check = Vec::new();
        let _ = self
            .assignments
            .iter_async(|key, _state| {
                if !same_cluster_calibration_scope(key, identity) {
                    return true;
                }
                keys_to_check.push(key.clone());
                true
            })
            .await;

        for key in keys_to_check {
            let identity = identity.clone();
            let remove_key = key.clone();
            let _ = self
                .assignments
                .remove_if_async(&key, move |state| {
                    if !same_cluster_calibration_scope(&remove_key, &identity) {
                        return false;
                    }
                    if !cluster_still_registered {
                        return true;
                    }
                    matches!(
                        &*state.lock(),
                        ClusterCalibrationState::Assigned {
                            owner_inference_server_id,
                            ..
                        } if owner_inference_server_id == &identity.inference_server_id
                    )
                })
                .await;
        }
    }

    pub(super) async fn release_removed_model_assignment(
        &self,
        identity: &RegistrationIdentity,
        removed_models: &HashSet<String>,
    ) {
        if removed_models.is_empty() {
            return;
        }

        for model_id in removed_models {
            let key = ClusterCalibrationKey {
                routing_key: identity.routing_key.clone(),
                cluster_id: identity.cluster_id.clone(),
                model_id: model_id.clone(),
            };
            let owner_inference_server_id = identity.inference_server_id.clone();
            let _ = self
                .assignments
                .remove_if_async(&key, move |state| {
                    matches!(
                        &*state.lock(),
                        ClusterCalibrationState::Assigned {
                            owner_inference_server_id: owner,
                            ..
                        } if owner == &owner_inference_server_id
                    )
                })
                .await;
        }
    }
}

pub(super) fn valid_last_mean_input_tps(last_mean_input_tps: f64) -> bool {
    last_mean_input_tps > 0.0 && last_mean_input_tps.is_finite()
}

fn new_cluster_calibration_assignment_token() -> String {
    format!("{:032x}", rand::random::<u128>())
}

fn calibration_run_directive(model_id: &str, assignment_token: &str) -> ModelCalibrationDirective {
    ModelCalibrationDirective {
        model_id: model_id.to_string(),
        state: CalibrationState::Run as i32,
        assignment_token: assignment_token.to_string(),
    }
}

fn calibration_wait_directive(model_id: &str) -> ModelCalibrationDirective {
    ModelCalibrationDirective {
        model_id: model_id.to_string(),
        state: CalibrationState::Waiting as i32,
        assignment_token: String::new(),
    }
}

fn calibration_complete_directive(model_id: &str) -> ModelCalibrationDirective {
    ModelCalibrationDirective {
        model_id: model_id.to_string(),
        state: CalibrationState::Complete as i32,
        assignment_token: String::new(),
    }
}

pub(super) fn same_cluster_calibration_scope(
    key: &ClusterCalibrationKey,
    identity: &RegistrationIdentity,
) -> bool {
    key.cluster_id == identity.cluster_id && key.routing_key == identity.routing_key
}

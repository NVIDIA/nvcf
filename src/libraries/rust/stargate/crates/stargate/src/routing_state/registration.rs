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
use super::snapshots::RegisteredReverseTunnel;
use super::*;

#[derive(Debug, Default)]
pub(super) struct RegistrationLifecycle {
    index: RegistrationIndex,
}

#[derive(Debug, Default)]
pub(super) struct RegistrationIndex {
    pub(super) registrations: SccHashMap<String, Arc<RegisteredInferenceServerState>>,
}

#[derive(Debug)]
pub(super) struct RegisteredInferenceServerState {
    pub(super) identity: RegistrationIdentity,
    pub(super) registered_models: SccHashMap<String, ()>,
    pub(super) last_rtt: Mutex<Option<Duration>>,
}

pub(crate) struct RunningRegistration {
    pub(crate) identity: RegistrationIdentity,
    pub(super) registration_state: Arc<RegisteredInferenceServerState>,
}

impl RunningRegistration {
    pub(super) async fn registered_model_ids(&self) -> HashSet<String> {
        let mut registered_models = HashSet::new();
        let _ = self
            .registration_state
            .registered_models
            .iter_async(|model_id, _registered| {
                registered_models.insert(model_id.clone());
                true
            })
            .await;
        registered_models
    }

    pub(super) async fn mark_model_registered(&self, model_id: &str) {
        let _ = self
            .registration_state
            .registered_models
            .upsert_async(model_id.to_string(), ())
            .await;
    }

    pub(super) async fn remove_registered_model(&self, model_id: &str) {
        let _ = self
            .registration_state
            .registered_models
            .remove_async(model_id)
            .await;
    }

    pub(super) fn set_last_rtt(&self, rtt: Option<Duration>) {
        *self.registration_state.last_rtt.lock() = rtt;
    }

    pub(super) fn routing_key(&self) -> &Option<String> {
        &self.registration_state.identity.routing_key
    }
}

impl RegistrationIndex {
    pub(super) async fn get(
        &self,
        inference_server_id: &str,
    ) -> Option<Arc<RegisteredInferenceServerState>> {
        self.registrations
            .read_async(inference_server_id, |_id, registration| {
                registration.clone()
            })
            .await
    }

    pub(super) async fn claim(
        &self,
        identity: &RegistrationIdentity,
    ) -> Result<Arc<RegisteredInferenceServerState>, Arc<RegisteredInferenceServerState>> {
        let registration = Arc::new(RegisteredInferenceServerState {
            identity: identity.clone(),
            registered_models: SccHashMap::default(),
            last_rtt: Mutex::new(None),
        });
        match self
            .registrations
            .insert_async(identity.inference_server_id.clone(), registration.clone())
            .await
        {
            Ok(()) => Ok(registration),
            Err(_) => match self.get(&identity.inference_server_id).await {
                Some(existing) => Err(existing),
                None => Err(registration),
            },
        }
    }

    pub(super) async fn remove(
        &self,
        inference_server_id: &str,
    ) -> Option<Arc<RegisteredInferenceServerState>> {
        self.registrations
            .remove_async(inference_server_id)
            .await
            .map(|(_id, registration)| registration)
    }

    pub(super) async fn registrations_for_routing_key(
        &self,
        routing_key: &Option<String>,
    ) -> Vec<Arc<RegisteredInferenceServerState>> {
        let mut registrations = Vec::new();
        let _ = self
            .registrations
            .iter_async(|_id, registration| {
                if registration.identity.routing_key == *routing_key {
                    registrations.push(registration.clone());
                }
                true
            })
            .await;
        registrations
    }

    pub(super) async fn has_cluster_registration(&self, identity: &RegistrationIdentity) -> bool {
        let mut cluster_still_registered = false;
        let _ = self
            .registrations
            .iter_async(|_id, registration| {
                if registration.identity.cluster_id == identity.cluster_id
                    && registration.identity.routing_key == identity.routing_key
                {
                    cluster_still_registered = true;
                    return false;
                }
                true
            })
            .await;
        cluster_still_registered
    }

    pub(super) async fn registered_reverse_tunnel(
        &self,
        inference_server_id: &str,
    ) -> Option<RegisteredReverseTunnel> {
        self.registrations
            .read_async(inference_server_id, |_id, registration| {
                if !registration.identity.reverse_tunnel {
                    return None;
                }
                Some(RegisteredReverseTunnel {
                    routing_key: registration.identity.routing_key.clone(),
                })
            })
            .await
            .flatten()
    }
}

impl RegistrationLifecycle {
    pub(super) async fn begin_registration(
        &self,
        identity: &RegistrationIdentity,
    ) -> Result<RunningRegistration, Status> {
        let registration_state = self.index.claim(identity).await.map_err(|existing| {
            duplicate_registration_status(&identity.inference_server_id, &existing)
        })?;
        Ok(RunningRegistration {
            identity: identity.clone(),
            registration_state,
        })
    }

    pub(super) async fn end_registration(
        &self,
        inference_server_id: &str,
    ) -> Option<Arc<RegisteredInferenceServerState>> {
        self.index.remove(inference_server_id).await
    }

    pub(super) async fn cluster_has_local_registration(
        &self,
        identity: &RegistrationIdentity,
    ) -> bool {
        self.index.has_cluster_registration(identity).await
    }

    pub(super) async fn has_registered_model_for_target(&self, target: &RoutingTargetKey) -> bool {
        for registration in self
            .index
            .registrations_for_routing_key(&target.routing_key)
            .await
        {
            if registration
                .registered_models
                .read_async(&target.model_id, |_model_id, _registered| ())
                .await
                .is_some()
            {
                return true;
            }
        }
        false
    }

    pub(super) async fn registered_reverse_tunnel(
        &self,
        inference_server_id: &str,
    ) -> Option<RegisteredReverseTunnel> {
        self.index
            .registered_reverse_tunnel(inference_server_id)
            .await
    }
}

pub(super) fn duplicate_registration_status(
    inference_server_id: &str,
    existing: &Arc<RegisteredInferenceServerState>,
) -> Status {
    warn!(
        inference_server_id = %inference_server_id,
        existing_url = %existing.identity.inference_server_url,
        existing_reverse_tunnel = existing.identity.reverse_tunnel,
        "duplicate inference_server_id: another stream already registered this id"
    );
    Status::already_exists(format!(
        "inference_server_id '{}' is already registered",
        inference_server_id
    ))
}

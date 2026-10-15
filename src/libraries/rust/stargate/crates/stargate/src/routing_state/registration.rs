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
use super::*;
use parking_lot::RwLock;
use std::collections::{HashMap, hash_map::Entry};
use std::sync::atomic::{AtomicBool, Ordering};

use crate::tunnel::RegistrationConnections;

#[derive(Debug, Default)]
pub(super) struct RegistrationRegistry {
    membership: RwLock<RegistrationMembership>,
}

#[derive(Debug, Default)]
struct RegistrationMembership {
    registrations_by_id: HashMap<String, RegistrationRecord>,
    active_clusters: HashMap<RegistrationClusterKey, ActiveRegistrationCluster>,
    advertised_targets: HashMap<RoutingTargetKey, usize>,
}

#[derive(Debug, Clone, PartialEq, Eq, Hash)]
struct RegistrationClusterKey(Option<String>, String);

#[derive(Debug)]
struct RegistrationRecord {
    state: Arc<RegistrationGeneration>,
    models: RegisteredModelState,
}

#[derive(Debug)]
struct ActiveRegistrationCluster {
    generation: Arc<RegistrationClusterGeneration>,
    registration_count: usize,
}

#[derive(Debug, Default)]
pub(super) struct RegistrationClusterGeneration {
    retired: AtomicBool,
}

#[derive(Debug)]
pub(crate) struct RegistrationGeneration {
    pub(super) identity: RegistrationIdentity,
    pub(super) cluster_generation: Arc<RegistrationClusterGeneration>,
    tunnel_connections: RegistrationConnections,
}

#[derive(Debug)]
enum RegisteredModelState {
    Stable(BTreeSet<String>),
    Applying {
        previous: BTreeSet<String>,
        advertised: BTreeSet<String>,
    },
}

pub(crate) struct RunningRegistration(Arc<RegistrationGeneration>);

pub(super) struct EndedRegistration {
    pub(super) registration: Arc<RegistrationGeneration>,
    pub(super) cleanup_model_ids: BTreeSet<String>,
}

impl RunningRegistration {
    pub(crate) fn identity(&self) -> &RegistrationIdentity {
        &self.0.identity
    }

    #[cfg(test)]
    pub(super) fn cluster_generation(&self) -> &Arc<RegistrationClusterGeneration> {
        &self.0.cluster_generation
    }

    pub(crate) fn generation(&self) -> Arc<RegistrationGeneration> {
        self.0.clone()
    }
}

impl RegistrationGeneration {
    pub(crate) fn inference_server_id(&self) -> &str {
        &self.identity.inference_server_id
    }

    pub(crate) fn inference_server_url(&self) -> &str {
        &self.identity.inference_server_url
    }

    pub(crate) fn routing_key(&self) -> &Option<String> {
        &self.identity.routing_key
    }

    pub(crate) fn reverse_tunnel(&self) -> bool {
        self.identity.reverse_tunnel
    }

    pub(crate) fn tunnel_connections(&self) -> &RegistrationConnections {
        &self.tunnel_connections
    }
}

impl RegistrationClusterGeneration {
    fn retire(&self) {
        assert!(
            !self.retired.swap(true, Ordering::AcqRel),
            "registration cluster generation retired more than once"
        );
    }

    pub(super) fn is_retired(&self) -> bool {
        self.retired.load(Ordering::Acquire)
    }
}

impl RegisteredModelState {
    fn begin_update(
        &mut self,
        advertised: BTreeSet<String>,
    ) -> (BTreeSet<String>, BTreeSet<String>) {
        let Self::Stable(previous) = self else {
            panic!("registration model update started while another update is applying");
        };
        let previous = std::mem::take(previous);
        let added = advertised.difference(&previous).cloned().collect();
        let removed = previous.difference(&advertised).cloned().collect();
        *self = Self::Applying {
            previous,
            advertised,
        };
        (added, removed)
    }

    fn finish_update(&mut self) {
        let Self::Applying { advertised, .. } = self else {
            panic!("registration model update finished without an applying update");
        };
        let advertised = std::mem::take(advertised);
        *self = Self::Stable(advertised);
    }

    fn cleanup_model_ids(&self) -> BTreeSet<String> {
        match self {
            Self::Stable(advertised) => advertised.clone(),
            Self::Applying {
                previous,
                advertised,
            } => previous.union(advertised).cloned().collect(),
        }
    }
}

impl RegistrationRegistry {
    pub(super) fn begin_registration(
        &self,
        identity: &RegistrationIdentity,
    ) -> Result<RunningRegistration, Status> {
        self.membership.write().begin(identity)
    }

    pub(super) fn end_registration(
        &self,
        running: RunningRegistration,
    ) -> Option<EndedRegistration> {
        self.membership.write().end(running)
    }

    pub(super) fn begin_advertised_model_update(
        &self,
        running: &RunningRegistration,
        advertised_models: BTreeSet<String>,
    ) -> BTreeSet<String> {
        let mut membership = self.membership.write();
        let registration = &running.0;
        let (added, removed) = {
            let record = membership.running_record_mut(registration);
            record.models.begin_update(advertised_models)
        };
        membership.remove_advertised_targets(&registration.identity.routing_key, &removed);
        membership.add_advertised_targets(&registration.identity.routing_key, &added);
        removed
    }

    pub(super) fn finish_advertised_model_update(&self, running: &RunningRegistration) {
        let mut membership = self.membership.write();
        let record = membership.running_record_mut(&running.0);
        record.models.finish_update();
    }

    #[cfg(test)]
    pub(super) fn cleanup_model_ids(&self, running: &RunningRegistration) -> BTreeSet<String> {
        let membership = self.membership.read();
        let record = membership
            .registrations_by_id
            .get(&running.0.identity.inference_server_id)
            .expect("running registration must remain indexed for cleanup inspection");
        assert!(
            Arc::ptr_eq(&record.state, &running.0),
            "cleanup inspection must target the exact running registration"
        );
        record.models.cleanup_model_ids()
    }

    pub(super) fn has_registered_model_for_target(&self, target: &RoutingTargetKey) -> bool {
        self.membership
            .read()
            .advertised_targets
            .contains_key(target)
    }

    pub(super) fn reverse_tunnel_registration(
        &self,
        inference_server_id: &str,
    ) -> Option<Arc<RegistrationGeneration>> {
        let membership = self.membership.read();
        membership
            .registrations_by_id
            .get(inference_server_id)
            .filter(|record| record.state.identity.reverse_tunnel)
            .map(|record| record.state.clone())
    }
}

impl RegistrationMembership {
    fn begin(&mut self, identity: &RegistrationIdentity) -> Result<RunningRegistration, Status> {
        let RegistrationMembership {
            registrations_by_id,
            active_clusters,
            ..
        } = self;
        let registration_entry =
            match registrations_by_id.entry(identity.inference_server_id.clone()) {
                Entry::Occupied(existing) => {
                    let existing = &existing.get().state;
                    warn!(
                        inference_server_id = %identity.inference_server_id,
                        existing_url = %existing.identity.inference_server_url,
                        existing_reverse_tunnel = existing.identity.reverse_tunnel,
                        "duplicate inference_server_id: another stream already registered this id"
                    );
                    return Err(Status::already_exists(format!(
                        "inference_server_id '{}' is already registered",
                        identity.inference_server_id
                    )));
                }
                Entry::Vacant(vacant) => vacant,
            };

        let cluster_key =
            RegistrationClusterKey(identity.routing_key.clone(), identity.cluster_id.clone());
        let cluster_generation = match active_clusters.entry(cluster_key) {
            Entry::Occupied(mut existing) => {
                let cluster = existing.get_mut();
                cluster.registration_count = cluster
                    .registration_count
                    .checked_add(1)
                    .expect("registration cluster generation count overflow");
                cluster.generation.clone()
            }
            Entry::Vacant(vacant) => {
                let generation = Arc::new(RegistrationClusterGeneration::default());
                vacant.insert(ActiveRegistrationCluster {
                    generation: generation.clone(),
                    registration_count: 1,
                });
                generation
            }
        };
        let state = Arc::new(RegistrationGeneration {
            identity: identity.clone(),
            cluster_generation,
            tunnel_connections: RegistrationConnections::new(),
        });
        registration_entry.insert(RegistrationRecord {
            state: state.clone(),
            models: RegisteredModelState::Stable(BTreeSet::new()),
        });
        Ok(RunningRegistration(state))
    }

    fn end(&mut self, running: RunningRegistration) -> Option<EndedRegistration> {
        let registration = running.0;
        let inference_server_id = &registration.identity.inference_server_id;
        self.registrations_by_id
            .get(inference_server_id)
            .filter(|current| Arc::ptr_eq(&current.state, &registration))?;

        let removed = self
            .registrations_by_id
            .remove(inference_server_id)
            .expect("exact registration should remain present until removal");

        self.remove_advertised_targets(
            &registration.identity.routing_key,
            match &removed.models {
                RegisteredModelState::Stable(models)
                | RegisteredModelState::Applying {
                    advertised: models, ..
                } => models,
            },
        );
        let cleanup_model_ids = removed.models.cleanup_model_ids();

        let cluster_key = RegistrationClusterKey(
            registration.identity.routing_key.clone(),
            registration.identity.cluster_id.clone(),
        );
        let Entry::Occupied(mut cluster_entry) = self.active_clusters.entry(cluster_key) else {
            panic!("live registration must retain its active cluster generation");
        };

        let cluster = cluster_entry.get_mut();
        assert!(
            Arc::ptr_eq(&cluster.generation, &registration.cluster_generation),
            "active cluster entry must match exact registration generation"
        );
        if cluster.registration_count == 1 {
            cluster.generation.retire();
            cluster_entry.remove();
        } else {
            cluster.registration_count = cluster
                .registration_count
                .checked_sub(1)
                .expect("registration cluster generation count underflow");
        }

        Some(EndedRegistration {
            registration,
            cleanup_model_ids,
        })
    }
    fn running_record_mut(
        &mut self,
        registration: &Arc<RegistrationGeneration>,
    ) -> &mut RegistrationRecord {
        let record = self
            .registrations_by_id
            .get_mut(&registration.identity.inference_server_id)
            .expect("running registration must remain indexed during model update");
        assert!(
            Arc::ptr_eq(&record.state, registration),
            "model update must target the exact running registration"
        );
        record
    }

    fn add_advertised_targets(
        &mut self,
        routing_key: &Option<String>,
        model_ids: &BTreeSet<String>,
    ) {
        for model_id in model_ids {
            let target = RoutingTargetKey::new(routing_key.clone(), model_id);
            let advertiser_count = self.advertised_targets.entry(target).or_default();
            *advertiser_count = advertiser_count
                .checked_add(1)
                .expect("registered target advertiser count overflow");
        }
    }

    fn remove_advertised_targets(
        &mut self,
        routing_key: &Option<String>,
        model_ids: &BTreeSet<String>,
    ) {
        for model_id in model_ids {
            let target = RoutingTargetKey::new(routing_key.clone(), model_id);
            let advertiser_count = self.advertised_targets.get_mut(&target).unwrap_or_else(|| {
                panic!("registered target advertiser removed without active membership")
            });
            *advertiser_count = advertiser_count
                .checked_sub(1)
                .expect("registered target advertiser count underflow");
            if *advertiser_count == 0 {
                self.advertised_targets.remove(&target);
            }
        }
    }
}

#[cfg(test)]
pub(crate) fn test_registration_generation(
    identity: RegistrationIdentity,
) -> Arc<RegistrationGeneration> {
    test_registration_generation_in_cluster(
        identity,
        Arc::new(RegistrationClusterGeneration::default()),
    )
}

#[cfg(test)]
pub(super) fn test_registration_generation_in_cluster(
    identity: RegistrationIdentity,
    cluster_generation: Arc<RegistrationClusterGeneration>,
) -> Arc<RegistrationGeneration> {
    Arc::new(RegistrationGeneration {
        identity,
        cluster_generation,
        tunnel_connections: RegistrationConnections::new(),
    })
}

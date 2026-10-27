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

//! Pool registration through the Pool API overlay.
//!
//! [`PoolRegistration`] registers a pool, renews its lease with heartbeats
//! every `ttl / 3` (jittered), re-registers when GDS lost the registration
//! (NOT_FOUND) or minted a new epoch (ABORTED `LEASE_EPOCH_MISMATCH`), and
//! shuts down gracefully: DRAINING first, then `DeregisterPool`. This is the
//! integration surface for the Dynamo PoolRelay and for pools that are not
//! Kubernetes Services.

use std::collections::HashMap;
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use tokio::sync::watch;
use tokio_util::sync::CancellationToken;
use tonic::Code;
use tracing::{info, warn};

use crate::client::GdsClient;
use crate::error::{Error, reason};
use crate::proto::v1::{
    DeregisterPoolRequest, FrontendEndpoint, HeartbeatPoolRequest, Location, ModelServed, Pool,
    PoolState as PoolPhase, RegisterPoolRequest, ServiceRef, UpdatePoolRequest,
};
use crate::task::OwnedTask;

/// Default pool lease; GDS clamps leases to [10 s, 5 m].
pub const DEFAULT_POOL_LEASE_TTL: Duration = Duration::from_secs(60);

/// Heartbeats start between this fraction of the interval and the full
/// interval, so pools registered together spread their heartbeats.
const HEARTBEAT_JITTER: f64 = 0.2;

/// The next writer generation: `max(previous + 1, unix milliseconds)`. Keeps
/// generations monotonic across restarts and region failover, so DynamoDB
/// Global Table last-writer-wins agrees with generation order.
pub fn next_generation(previous: u64) -> u64 {
    let unix_ms = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |elapsed| {
            u64::try_from(elapsed.as_millis()).unwrap_or(u64::MAX)
        });
    previous.saturating_add(1).max(unix_ms)
}

/// The routing state a pool asks for.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum DesiredState {
    /// Routers may choose the pool.
    #[default]
    Active,
    /// Routers stop choosing the pool; in-flight traffic completes.
    Draining,
}

impl DesiredState {
    fn proto(self) -> PoolPhase {
        match self {
            Self::Active => PoolPhase::Active,
            Self::Draining => PoolPhase::Draining,
        }
    }
}

/// The writer-owned fields of a pool registration.
#[derive(Debug, Clone, Default)]
pub struct PoolSpec {
    pub clusterset: String,
    /// Keep the registration's id stable; the server assigns one when empty.
    pub pool_id: String,
    /// DNS label unique within (clusterset, cluster).
    pub name: String,
    /// `location.cluster` is required.
    pub location: Location,
    pub hardware_class: String,
    pub models: Vec<ModelServed>,
    pub worker_count: i32,
    pub ready_worker_count: i32,
    /// Dispatch entries; an API-registered pool supplies the OPENAI `url`.
    pub frontends: Vec<FrontendEndpoint>,
    /// Exported Services in the pool's cluster used for the endpoint join.
    pub services: Vec<ServiceRef>,
    pub labels: HashMap<String, String>,
    pub desired_state: DesiredState,
    /// Identity of this writer, for example the PoolRelay pod name.
    pub owner_id: String,
    /// Lease duration; [`DEFAULT_POOL_LEASE_TTL`] when zero.
    pub lease_ttl: Duration,
    /// Heartbeat interval; `lease_ttl / 3` when unset.
    pub heartbeat_interval: Option<Duration>,
}

impl PoolSpec {
    /// A spec with the required fields set.
    pub fn new(
        clusterset: impl Into<String>,
        cluster: impl Into<String>,
        name: impl Into<String>,
    ) -> Self {
        Self {
            clusterset: clusterset.into(),
            name: name.into(),
            location: Location {
                cluster: cluster.into(),
                ..Location::default()
            },
            ..Self::default()
        }
    }

    fn lease_ttl(&self) -> Duration {
        if self.lease_ttl.is_zero() {
            DEFAULT_POOL_LEASE_TTL
        } else {
            self.lease_ttl
        }
    }

    fn heartbeat_interval(&self) -> Duration {
        self.heartbeat_interval
            .filter(|interval| !interval.is_zero())
            .unwrap_or_else(|| self.lease_ttl() / 3)
    }

    fn to_pool(&self, pool_id: &str) -> Pool {
        Pool {
            clusterset: self.clusterset.clone(),
            pool_id: pool_id.to_string(),
            name: self.name.clone(),
            location: Some(self.location.clone()),
            hardware_class: self.hardware_class.clone(),
            models: self.models.clone(),
            worker_count: self.worker_count,
            ready_worker_count: self.ready_worker_count,
            frontends: self.frontends.clone(),
            services: self.services.clone(),
            labels: self.labels.clone(),
            desired_state: self.desired_state.proto() as i32,
            owner_id: self.owner_id.clone(),
            ..Pool::default()
        }
    }
}

/// A change to writer-owned pool fields. Only the fields that are set are
/// sent (as the `UpdatePool` field mask) and kept for re-registration. An
/// update with no field set sends the whole spec.
#[derive(Debug, Clone, Default)]
pub struct PoolUpdate {
    pub location: Option<Location>,
    pub hardware_class: Option<String>,
    pub models: Option<Vec<ModelServed>>,
    pub worker_count: Option<i32>,
    pub ready_worker_count: Option<i32>,
    pub frontends: Option<Vec<FrontendEndpoint>>,
    pub services: Option<Vec<ServiceRef>>,
    pub labels: Option<HashMap<String, String>>,
    pub desired_state: Option<DesiredState>,
}

impl PoolUpdate {
    /// Applies the set fields to `spec` and returns their field paths.
    fn apply(self, spec: &mut PoolSpec) -> Vec<String> {
        let mut paths = Vec::new();
        let mut set = |path: &str| paths.push(path.to_string());
        if let Some(location) = self.location {
            spec.location = location;
            set("location");
        }
        if let Some(hardware_class) = self.hardware_class {
            spec.hardware_class = hardware_class;
            set("hardware_class");
        }
        if let Some(models) = self.models {
            spec.models = models;
            set("models");
        }
        if let Some(worker_count) = self.worker_count {
            spec.worker_count = worker_count;
            set("worker_count");
        }
        if let Some(ready_worker_count) = self.ready_worker_count {
            spec.ready_worker_count = ready_worker_count;
            set("ready_worker_count");
        }
        if let Some(frontends) = self.frontends {
            spec.frontends = frontends;
            set("frontends");
        }
        if let Some(services) = self.services {
            spec.services = services;
            set("services");
        }
        if let Some(labels) = self.labels {
            spec.labels = labels;
            set("labels");
        }
        if let Some(desired_state) = self.desired_state {
            spec.desired_state = desired_state;
            set("desired_state");
        }
        paths
    }
}

/// The registration state published by [`PoolRegistration::subscribe`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RegistrationState {
    /// The last register or heartbeat succeeded.
    Registered {
        pool_id: String,
        epoch: u64,
        desired_state: DesiredState,
        /// The derived state GDS reported.
        state: PoolPhase,
    },
    /// Heartbeats or re-registration fail; the task keeps retrying with
    /// backoff. The lease expires if this lasts longer than the lease TTL.
    Retrying {
        pool_id: String,
        failures: u32,
        error: String,
    },
    /// The stop token fired; the registration was left to expire.
    Stopped { pool_id: String },
    /// [`PoolRegistration::shutdown`] deregistered the pool.
    Deregistered { pool_id: String },
}

#[derive(Debug, Default)]
struct Lease {
    pool_id: String,
    epoch: u64,
    generation: u64,
}

struct Shared {
    client: GdsClient,
    spec: Mutex<PoolSpec>,
    /// Serializes register, update, heartbeat and deregister so a
    /// re-registration never races a heartbeat with a stale epoch.
    lease: tokio::sync::Mutex<Lease>,
    state_tx: watch::Sender<RegistrationState>,
}

/// A registered pool with a background heartbeat. Dropping it stops the
/// heartbeat without deregistering; the lease then expires and the pool
/// turns UNHEALTHY. Call [`PoolRegistration::shutdown`] to leave cleanly.
pub struct PoolRegistration {
    shared: Arc<Shared>,
    task: Option<OwnedTask>,
}

impl std::fmt::Debug for PoolRegistration {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("PoolRegistration")
            .field("state", &*self.shared.state_tx.borrow())
            .finish_non_exhaustive()
    }
}

impl PoolRegistration {
    /// Registers the pool and starts the heartbeat task. Fails when the
    /// first `RegisterPool` fails after the client's retries. Cancelling
    /// `stop` stops the heartbeat.
    pub async fn register(
        client: GdsClient,
        spec: PoolSpec,
        stop: CancellationToken,
    ) -> Result<Self, Error> {
        let pool_id = spec.pool_id.clone();
        let (state_tx, _) = watch::channel(RegistrationState::Retrying {
            pool_id: pool_id.clone(),
            failures: 0,
            error: "not registered yet".to_string(),
        });
        let shared = Arc::new(Shared {
            client,
            spec: Mutex::new(spec),
            lease: tokio::sync::Mutex::new(Lease {
                pool_id,
                ..Lease::default()
            }),
            state_tx,
        });
        {
            let mut lease = shared.lease.lock().await;
            shared.register_locked(&mut lease).await?;
        }
        let task = OwnedTask::spawn(&stop, {
            let shared = shared.clone();
            move |stop| heartbeat_loop(shared, stop)
        });
        Ok(Self {
            shared,
            task: Some(task),
        })
    }

    /// The pool id GDS assigned or confirmed.
    pub fn pool_id(&self) -> String {
        match &*self.shared.state_tx.borrow() {
            RegistrationState::Registered { pool_id, .. }
            | RegistrationState::Retrying { pool_id, .. }
            | RegistrationState::Stopped { pool_id }
            | RegistrationState::Deregistered { pool_id } => pool_id.clone(),
        }
    }

    /// The current registration state.
    pub fn state(&self) -> RegistrationState {
        self.shared.state_tx.borrow().clone()
    }

    /// Registration state changes.
    pub fn subscribe(&self) -> watch::Receiver<RegistrationState> {
        self.shared.state_tx.subscribe()
    }

    /// Sets ACTIVE or DRAINING with `UpdatePool`. Later heartbeats and
    /// re-registrations carry the same state.
    pub async fn set_desired_state(&self, desired_state: DesiredState) -> Result<Pool, Error> {
        self.update(PoolUpdate {
            desired_state: Some(desired_state),
            ..PoolUpdate::default()
        })
        .await
    }

    /// Changes writer-owned fields with `UpdatePool`. When GDS no longer
    /// knows the pool, the pool is registered again with the updated spec.
    pub async fn update(&self, update: PoolUpdate) -> Result<Pool, Error> {
        if let RegistrationState::Stopped { pool_id }
        | RegistrationState::Deregistered { pool_id } = self.state()
        {
            return Err(Error::RegistrationLost(format!(
                "pool {pool_id} no longer heartbeats; register it again"
            )));
        }
        let mut lease = self.shared.lease.lock().await;
        let paths = update.apply(&mut self.shared.lock_spec());
        self.shared.update_locked(&mut lease, paths).await
    }

    /// Records worker counts; the next heartbeat reports them. Cheap enough
    /// to call on every change.
    pub fn report_workers(&self, worker_count: i32, ready_worker_count: i32) {
        let mut spec = self.shared.lock_spec();
        spec.worker_count = worker_count;
        spec.ready_worker_count = ready_worker_count;
    }

    /// Leaves the clusterset. With a non-zero `drain` the pool is set
    /// DRAINING first and keeps heartbeating for `drain`, so routers stop
    /// choosing it while in-flight requests complete; then the heartbeat
    /// stops and the pool is deregistered.
    pub async fn shutdown(mut self, drain: Duration) -> Result<(), Error> {
        if !drain.is_zero() {
            match self.set_desired_state(DesiredState::Draining).await {
                Ok(_) => info!(pool_id = %self.pool_id(), ?drain, "GDS pool draining"),
                Err(error) => warn!(
                    pool_id = %self.pool_id(),
                    %error,
                    "could not set the GDS pool DRAINING; deregistering after the drain period"
                ),
            }
            tokio::time::sleep(drain).await;
        }
        if let Some(task) = self.task.take() {
            task.shutdown().await;
        }
        let lease = self.shared.lease.lock().await;
        let clusterset = self.shared.lock_spec().clusterset.clone();
        let result = self
            .shared
            .client
            .deregister_pool(DeregisterPoolRequest {
                clusterset,
                pool_id: lease.pool_id.clone(),
            })
            .await;
        self.shared
            .state_tx
            .send_replace(RegistrationState::Deregistered {
                pool_id: lease.pool_id.clone(),
            });
        match &result {
            Ok(_) => info!(pool_id = %lease.pool_id, "GDS pool deregistered"),
            Err(error) => warn!(
                pool_id = %lease.pool_id,
                %error,
                "GDS pool deregistration failed; the lease will expire"
            ),
        }
        result.map(|_| ())
    }
}

impl Shared {
    fn lock_spec(&self) -> MutexGuard<'_, PoolSpec> {
        self.spec
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }

    fn publish_registered(&self, lease: &Lease, state: i32) {
        let desired_state = self.lock_spec().desired_state;
        self.state_tx.send_replace(RegistrationState::Registered {
            pool_id: lease.pool_id.clone(),
            epoch: lease.epoch,
            desired_state,
            state: PoolPhase::try_from(state).unwrap_or(PoolPhase::Unspecified),
        });
    }

    /// `RegisterPool` with the current spec and a new generation. Mints a
    /// new lease epoch.
    async fn register_locked(&self, lease: &mut Lease) -> Result<Pool, Error> {
        let mut stale_retry = true;
        loop {
            let spec = self.lock_spec().clone();
            lease.generation = next_generation(lease.generation);
            let request = RegisterPoolRequest {
                clusterset: spec.clusterset.clone(),
                pool: Some(spec.to_pool(&lease.pool_id)),
                lease_ttl: prost_types::Duration::try_from(spec.lease_ttl()).ok(),
                generation: lease.generation,
            };
            match self.client.register_pool(request).await {
                Ok(response) => {
                    self.client.metrics().pool_registration(true);
                    let pool = response.pool.unwrap_or_default();
                    if !pool.pool_id.is_empty() {
                        lease.pool_id = pool.pool_id.clone();
                    }
                    lease.epoch = pool.lease.as_ref().map_or(0, |lease| lease.epoch);
                    if pool.generation > lease.generation {
                        lease.generation = pool.generation;
                    }
                    info!(
                        pool_id = %lease.pool_id,
                        pool = %spec.name,
                        epoch = lease.epoch,
                        "GDS pool registered"
                    );
                    self.publish_registered(lease, pool.state);
                    return Ok(pool);
                }
                Err(error) if stale_retry && raise_generation(lease, &error) => {
                    stale_retry = false;
                }
                Err(error) => {
                    self.client.metrics().pool_registration(false);
                    return Err(error);
                }
            }
        }
    }

    async fn update_locked(&self, lease: &mut Lease, paths: Vec<String>) -> Result<Pool, Error> {
        let mut stale_retry = true;
        loop {
            let spec = self.lock_spec().clone();
            lease.generation = next_generation(lease.generation);
            let request = UpdatePoolRequest {
                clusterset: spec.clusterset.clone(),
                pool_id: lease.pool_id.clone(),
                pool: Some(spec.to_pool(&lease.pool_id)),
                update_mask: Some(prost_types::FieldMask {
                    paths: paths.clone(),
                }),
                generation: lease.generation,
            };
            match self.client.update_pool(request).await {
                Ok(response) => {
                    let pool = response.pool.unwrap_or_default();
                    self.publish_registered(lease, pool.state);
                    return Ok(pool);
                }
                Err(error) if error.code() == Some(Code::NotFound) => {
                    info!(
                        pool_id = %lease.pool_id,
                        "GDS no longer knows the pool; registering it again"
                    );
                    return self.register_locked(lease).await;
                }
                Err(error) if stale_retry && raise_generation(lease, &error) => {
                    stale_retry = false;
                }
                Err(error) => return Err(error),
            }
        }
    }

    /// One heartbeat; re-registers when the lease is gone or superseded.
    async fn heartbeat(&self) -> Result<(), Error> {
        let mut lease = self.lease.lock().await;
        let spec = self.lock_spec().clone();
        let request = HeartbeatPoolRequest {
            clusterset: spec.clusterset.clone(),
            pool_id: lease.pool_id.clone(),
            epoch: lease.epoch,
            worker_count: Some(spec.worker_count),
            ready_worker_count: Some(spec.ready_worker_count),
            desired_state: spec.desired_state.proto() as i32,
        };
        match self.client.heartbeat_pool(request).await {
            Ok(response) => {
                self.client.metrics().pool_heartbeat("success");
                if let Some(epoch) = response.lease.as_ref().map(|lease| lease.epoch)
                    && epoch != 0
                {
                    lease.epoch = epoch;
                }
                self.publish_registered(&lease, response.state);
                Ok(())
            }
            Err(error) if matches!(error.code(), Some(Code::NotFound | Code::Aborted)) => {
                self.client.metrics().pool_heartbeat("reregister");
                info!(
                    pool_id = %lease.pool_id,
                    epoch = lease.epoch,
                    reason = error.reason().as_deref().unwrap_or(""),
                    "GDS pool lease lost; registering again"
                );
                self.register_locked(&mut lease).await.map(|_| ())
            }
            Err(error) => {
                self.client.metrics().pool_heartbeat("failure");
                Err(error)
            }
        }
    }
}

/// On STALE_GENERATION, moves the writer generation past the stored one
/// and reports whether the write should be retried.
fn raise_generation(lease: &mut Lease, error: &Error) -> bool {
    let Some(details) = error.details() else {
        return false;
    };
    if details.reason != reason::STALE_GENERATION {
        return false;
    }
    if let Some(current) = details.current_generation() {
        lease.generation = lease.generation.max(current);
    }
    true
}

async fn heartbeat_loop(shared: Arc<Shared>, stop: CancellationToken) {
    let mut failures: u32 = 0;
    loop {
        let interval = shared.lock_spec().heartbeat_interval();
        let delay = if failures == 0 {
            interval.mul_f64(1.0 - HEARTBEAT_JITTER * rand::random::<f64>())
        } else {
            shared
                .client
                .policy()
                .backoff
                .delay(failures - 1)
                .min(interval)
        };
        if stop
            .run_until_cancelled(tokio::time::sleep(delay))
            .await
            .is_none()
        {
            break;
        }
        let Some(result) = stop.run_until_cancelled(shared.heartbeat()).await else {
            break;
        };
        match result {
            Ok(()) => failures = 0,
            Err(error) => {
                failures = failures.saturating_add(1);
                let pool_id = shared.lease.lock().await.pool_id.clone();
                warn!(%pool_id, failures, %error, "GDS pool heartbeat failed");
                shared.state_tx.send_replace(RegistrationState::Retrying {
                    pool_id,
                    failures,
                    error: error.to_string(),
                });
            }
        }
    }
    let pool_id = shared.lease.lock().await.pool_id.clone();
    shared.state_tx.send_if_modified(|state| {
        if matches!(state, RegistrationState::Deregistered { .. }) {
            return false;
        }
        *state = RegistrationState::Stopped { pool_id };
        true
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn generation_is_monotonic_and_at_least_unix_ms() {
        let first = next_generation(0);
        assert!(first > 1_600_000_000_000, "{first}");
        let far_future = u64::MAX - 10;
        assert_eq!(next_generation(far_future), far_future + 1);
        assert!(next_generation(first) > first);
    }

    #[test]
    fn update_sets_mask_paths_and_spec() {
        let mut spec = PoolSpec::new("cs", "c1", "p");
        let paths = PoolUpdate {
            worker_count: Some(4),
            desired_state: Some(DesiredState::Draining),
            ..PoolUpdate::default()
        }
        .apply(&mut spec);
        assert_eq!(paths, ["worker_count", "desired_state"]);
        assert_eq!(spec.worker_count, 4);
        assert_eq!(spec.desired_state, DesiredState::Draining);
        assert!(PoolUpdate::default().apply(&mut spec).is_empty());
    }

    #[test]
    fn heartbeat_interval_defaults_to_a_third_of_the_lease() {
        let mut spec = PoolSpec::new("cs", "c1", "p");
        assert_eq!(spec.heartbeat_interval(), Duration::from_secs(20));
        spec.lease_ttl = Duration::from_secs(30);
        assert_eq!(spec.heartbeat_interval(), Duration::from_secs(10));
        spec.heartbeat_interval = Some(Duration::from_secs(3));
        assert_eq!(spec.heartbeat_interval(), Duration::from_secs(3));
    }

    #[test]
    fn spec_to_pool_carries_writer_fields() {
        let mut spec = PoolSpec::new("cs", "c1", "p");
        spec.desired_state = DesiredState::Draining;
        spec.owner_id = "relay-0".to_string();
        let pool = spec.to_pool("id-1");
        assert_eq!(pool.pool_id, "id-1");
        assert_eq!(pool.location.expect("location").cluster, "c1");
        assert_eq!(pool.desired_state, PoolPhase::Draining as i32);
        assert_eq!(pool.owner_id, "relay-0");
    }
}

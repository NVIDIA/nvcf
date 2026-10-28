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

//! Pool agent registration on a paused clock.

use std::collections::BTreeMap;
use std::time::Duration;

use gds_client::v1::{
    DeregisterPoolAgentRequest, FrontendRole, HeartbeatPoolAgentRequest, PoolState,
};
use gds_client::{AgentPhase, AgentRegistration, AgentSpec, GdsClient};
use tokio_util::sync::CancellationToken;
use tonic::{Code, Status};

use crate::support::fake_gds::FakeGds;
use crate::support::{fast_policy, wait_until};

const TTL: Duration = Duration::from_secs(30);
const CLUSTER: &str = "c1";
const POOL: &str = "pool-a";
const ROLE: FrontendRole = FrontendRole::Pylon;
const AGENT: &str = "pylon-0";

fn models(list: &str) -> BTreeMap<String, String> {
    BTreeMap::from([("models".to_string(), list.to_string())])
}

fn spec() -> AgentSpec {
    let mut spec = AgentSpec::new("cs", CLUSTER, POOL, ROLE, AGENT);
    spec.attributes = models("llama");
    spec.lease_ttl = Some(TTL);
    spec
}

fn client(fake: &FakeGds) -> GdsClient {
    let (channel, server) = fake.in_memory_channel();
    // The server lives as long as the test runtime.
    std::mem::forget(server);
    GdsClient::from_channel_with_policy(channel, fast_policy())
}

async fn register(fake: &FakeGds, stop: &CancellationToken) -> AgentRegistration {
    AgentRegistration::register(client(fake), spec(), stop.clone())
        .await
        .expect("register")
}

fn epoch(registration: &AgentRegistration) -> u64 {
    let state = registration.state();
    assert_eq!(state.phase, AgentPhase::Registered, "{state:?}");
    state.epoch
}

fn stored_models(fake: &FakeGds) -> Option<String> {
    fake.agent_registration(CLUSTER, POOL, ROLE, AGENT)
        .and_then(|registration| registration.attributes.get("models").cloned())
}

fn heartbeats_of(fake: &FakeGds, agent_id: &str) -> Vec<HeartbeatPoolAgentRequest> {
    fake.agent_heartbeats()
        .into_iter()
        .filter(|heartbeat| heartbeat.agent_id == agent_id)
        .collect()
}

#[tokio::test(start_paused = true)]
async fn registers_and_heartbeats_every_third_of_the_lease() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let state = registration.state();
    assert_eq!(state.phase, AgentPhase::Registered);
    assert_eq!(state.pool_id, "agent-pool-c1-pool-a");
    assert_eq!(state.pool_state, PoolState::Active, "PYLON is an ingress");
    assert!(state.epoch > 0);
    assert_eq!((state.failures, state.last_error), (0, None));

    let request = fake.agent_registrations().pop().expect("registration");
    assert_eq!(
        (
            request.clusterset.as_str(),
            request.cluster.as_str(),
            request.pool_name.as_str()
        ),
        ("cs", CLUSTER, POOL)
    );
    assert!(request.pool_id.is_empty(), "GDS resolves the pool by name");
    assert!(
        request.generation > 1_600_000_000_000,
        "generation is unix ms based"
    );
    assert_eq!(request.lease_ttl.map(|ttl| ttl.seconds), Some(30));
    assert_eq!(
        request.agent.as_ref().map(|agent| agent.agent_id.as_str()),
        Some(AGENT)
    );
    let stored = fake
        .agent_registration(CLUSTER, POOL, ROLE, AGENT)
        .expect("stored");
    assert_eq!(
        stored.lease.map(|lease| lease.epoch),
        Some(state.epoch),
        "the epoch of this process's registration"
    );
    assert_eq!(stored_models(&fake).as_deref(), Some("llama"));
    assert_eq!(fake.calls("HeartbeatPoolAgent"), 0);

    // Jitter keeps each interval within [0.8, 1.0] of ttl / 3.
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_millis(100), || fake
            .calls("HeartbeatPoolAgent")
            == 1)
        .await,
        "first heartbeat by ttl/3"
    );
    tokio::time::sleep(TTL).await;
    let heartbeats = fake.agent_heartbeats();
    assert!(
        (4..=5).contains(&heartbeats.len()),
        "{} heartbeats in 40 s",
        heartbeats.len()
    );
    let current_epoch = epoch(&registration);
    assert!(heartbeats.iter().all(|heartbeat| {
        heartbeat.epoch == current_epoch
            && heartbeat.pool_id == state.pool_id
            && heartbeat.role == ROLE as i32
            && heartbeat.agent_id == AGENT
            && heartbeat.clusterset == "cs"
    }));
    assert!(
        heartbeats
            .iter()
            .all(|heartbeat| heartbeat.attributes.is_empty()),
        "unchanged attributes are not sent again"
    );
    assert_eq!(fake.calls("RegisterPoolAgent"), 1);

    stop.cancel();
    assert!(
        wait_until(Duration::from_secs(1), || registration.state().phase
            == AgentPhase::Stopped)
        .await
    );
    let count = fake.calls("HeartbeatPoolAgent");
    tokio::time::sleep(TTL).await;
    assert_eq!(
        fake.calls("HeartbeatPoolAgent"),
        count,
        "no heartbeat after stop"
    );
    assert!(
        fake.agent_registration(CLUSTER, POOL, ROLE, AGENT)
            .is_some(),
        "left to expire"
    );
}

#[tokio::test(start_paused = true)]
async fn heartbeats_follow_the_granted_lease() {
    let fake = FakeGds::new("r");
    let mut spec = spec();
    // The fake clamps leases to [10 s, 5 m] as GDS does.
    spec.lease_ttl = Some(Duration::from_secs(3));
    let _registration = AgentRegistration::register(client(&fake), spec, CancellationToken::new())
        .await
        .expect("register");

    tokio::time::sleep(Duration::from_secs(2)).await;
    assert_eq!(
        fake.calls("HeartbeatPoolAgent"),
        0,
        "a third of the granted 10 s, not of the requested 3 s"
    );
    tokio::time::sleep(Duration::from_millis(1400)).await;
    assert!(
        wait_until(Duration::from_millis(100), || fake
            .calls("HeartbeatPoolAgent")
            == 1)
        .await
    );
}

#[tokio::test(start_paused = true)]
async fn epoch_mismatch_registers_again() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let first_epoch = epoch(&registration);

    fake.bump_agent_epoch(CLUSTER, POOL, ROLE, AGENT);
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || fake.calls("RegisterPoolAgent")
            == 2)
        .await,
        "re-registered after LEASE_EPOCH_MISMATCH"
    );
    let stored_epoch = fake
        .agent_registration(CLUSTER, POOL, ROLE, AGENT)
        .and_then(|registration| registration.lease)
        .map(|lease| lease.epoch)
        .expect("stored lease");
    assert!(
        wait_until(Duration::from_secs(1), || registration.state().epoch
            == stored_epoch)
        .await
    );
    assert_ne!(stored_epoch, first_epoch);
    let registrations = fake.agent_registrations();
    assert!(registrations[1].generation > registrations[0].generation);

    // The next heartbeat carries the new epoch.
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || {
            fake.agent_heartbeats()
                .last()
                .is_some_and(|heartbeat| heartbeat.epoch == stored_epoch)
        })
        .await
    );
    assert_eq!(registration.state().phase, AgentPhase::Registered);
}

#[tokio::test(start_paused = true)]
async fn not_found_heartbeat_registers_again() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let pool_id = registration.pool_id();

    fake.remove_agent_silently(CLUSTER, POOL, ROLE, AGENT);
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || fake.calls("RegisterPoolAgent")
            == 2)
        .await,
        "re-registered after NOT_FOUND"
    );
    assert_eq!(registration.pool_id(), pool_id, "pool id is stable");
    assert_eq!(
        stored_models(&fake).as_deref(),
        Some("llama"),
        "the re-registration carries the attributes"
    );
}

#[tokio::test(start_paused = true)]
async fn failing_heartbeats_publish_retrying_then_recover() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let mut states = registration.subscribe();

    fake.set_unavailable(true);
    states
        .wait_for(|state| state.phase == AgentPhase::Retrying && state.failures >= 2)
        .await
        .expect("retrying");
    assert!(registration.state().last_error.is_some());
    fake.set_unavailable(false);
    // Retries come after a backoff shorter than the interval.
    states
        .wait_for(|state| state.phase == AgentPhase::Registered)
        .await
        .expect("registered again");
    let state = registration.state();
    assert_eq!(state.failures, 0);
    assert!(state.last_error.is_some(), "the last error is kept");
    assert_eq!(
        fake.calls("RegisterPoolAgent"),
        1,
        "a transient failure keeps the lease"
    );
}

#[tokio::test(start_paused = true)]
async fn attributes_reach_gds_with_the_next_heartbeat() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;

    registration.set_attributes(models("llama,mixtral"));
    assert_eq!(fake.calls("HeartbeatPoolAgent"), 0, "no call of its own");
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || stored_models(&fake).as_deref()
            == Some("llama,mixtral"))
        .await
    );
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || fake.agent_heartbeats().len()
            == 2)
        .await
    );
    let heartbeats = fake.agent_heartbeats();
    assert_eq!(
        heartbeats[0].attributes,
        models("llama,mixtral").into_iter().collect()
    );
    assert!(
        heartbeats[1].attributes.is_empty(),
        "sent until acknowledged"
    );

    // A failed heartbeat keeps the change pending.
    registration.set_attributes(models("mixtral"));
    fake.fail_next("HeartbeatPoolAgent", Status::internal("boom"));
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || stored_models(&fake).as_deref()
            == Some("mixtral"))
        .await
    );
    assert_eq!(fake.calls("HeartbeatPoolAgent"), 4);

    // A heartbeat cannot clear attributes; a registration does.
    registration.set_attributes(BTreeMap::new());
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || fake.calls("RegisterPoolAgent")
            == 2)
        .await
    );
    let stored = fake
        .agent_registration(CLUSTER, POOL, ROLE, AGENT)
        .expect("stored");
    assert!(stored.attributes.is_empty());
    assert_eq!(registration.state().phase, AgentPhase::Registered);
}

#[tokio::test(start_paused = true)]
async fn stale_generation_is_retried_above_the_stored_one() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let far_future = u64::MAX / 2;
    fake.set_agent_generation(CLUSTER, POOL, ROLE, AGENT, far_future);
    fake.bump_agent_epoch(CLUSTER, POOL, ROLE, AGENT);

    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || fake.agent_registrations().len()
            == 3)
        .await
    );
    let registrations = fake.agent_registrations();
    assert!(registrations[1].generation < far_future);
    assert_eq!(registrations[2].generation, far_future + 1);
    assert!(
        wait_until(Duration::from_secs(1), || registration.state().phase
            == AgentPhase::Registered)
        .await
    );
}

#[tokio::test(start_paused = true)]
async fn a_rejected_first_registration_fails() {
    let fake = FakeGds::new("r");
    fake.fail_next(
        "RegisterPoolAgent",
        Status::invalid_argument("pool_name is not a DNS label"),
    );
    let error = AgentRegistration::register(client(&fake), spec(), CancellationToken::new())
        .await
        .expect_err("rejected");
    assert_eq!(error.code(), Some(Code::InvalidArgument));
    assert!(
        fake.agent_registration(CLUSTER, POOL, ROLE, AGENT)
            .is_none()
    );
}

#[tokio::test(start_paused = true)]
async fn agents_of_one_pool_share_its_id() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let relay = AgentRegistration::register(
        client(&fake),
        AgentSpec::new("cs", CLUSTER, POOL, FrontendRole::KvRelay, "relay-0"),
        stop.clone(),
    )
    .await
    .expect("relay");
    assert_eq!(
        relay.state().pool_state,
        PoolState::Registered,
        "a relay alone is not an ingress"
    );
    let pylon = register(&fake, &stop).await;
    assert_eq!(pylon.pool_id(), relay.pool_id());
    assert_eq!(pylon.state().pool_state, PoolState::Active);
}

#[tokio::test(start_paused = true)]
async fn replicas_of_one_role_do_not_fence_each_other() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let first = register(&fake, &stop).await;
    let mut spec = spec();
    spec.agent_id = "pylon-1".to_string();
    let second = AgentRegistration::register(client(&fake), spec, stop.clone())
        .await
        .expect("second replica");
    assert_eq!(first.pool_id(), second.pool_id());
    let (first_epoch, second_epoch) = (epoch(&first), epoch(&second));
    assert_ne!(first_epoch, second_epoch);

    tokio::time::sleep(TTL).await;
    for (agent_id, epoch) in [(AGENT, first_epoch), ("pylon-1", second_epoch)] {
        let heartbeats = heartbeats_of(&fake, agent_id);
        assert!(heartbeats.len() >= 3, "{agent_id}: {}", heartbeats.len());
        assert!(
            heartbeats.iter().all(|heartbeat| heartbeat.epoch == epoch),
            "{agent_id} keeps its own epoch"
        );
    }
    assert_eq!(
        fake.calls("RegisterPoolAgent"),
        2,
        "neither registration superseded the other"
    );
    assert_eq!(first.state().phase, AgentPhase::Registered);
    assert_eq!(second.state().phase, AgentPhase::Registered);

    // Deregistering one replica leaves the other registered.
    first.shutdown().await.expect("shutdown");
    assert!(
        fake.agent_registration(CLUSTER, POOL, ROLE, AGENT)
            .is_none()
    );
    assert!(
        fake.agent_registration(CLUSTER, POOL, ROLE, "pylon-1")
            .is_some()
    );
    let count = heartbeats_of(&fake, "pylon-1").len();
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || heartbeats_of(&fake, "pylon-1")
            .len()
            > count)
        .await
    );
    assert_eq!(epoch(&second), second_epoch);
    assert_eq!(fake.calls("RegisterPoolAgent"), 2);
}

#[tokio::test(start_paused = true)]
async fn shutdown_deregisters_at_once() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let pool_id = registration.pool_id();
    let mut states = registration.subscribe();

    registration.shutdown().await.expect("shutdown");
    assert_eq!(fake.calls("DeregisterPoolAgent"), 1);
    assert!(
        fake.agent_registration(CLUSTER, POOL, ROLE, AGENT)
            .is_none()
    );
    assert_eq!(states.borrow_and_update().phase, AgentPhase::Deregistered);
    let count = fake.calls("HeartbeatPoolAgent");
    tokio::time::sleep(TTL).await;
    assert_eq!(fake.calls("HeartbeatPoolAgent"), count, "heartbeat stopped");

    // Deregistering an unknown agent succeeds.
    client(&fake)
        .deregister_pool_agent(DeregisterPoolAgentRequest {
            clusterset: "cs".to_string(),
            pool_id,
            role: ROLE as i32,
            agent_id: AGENT.to_string(),
        })
        .await
        .expect("idempotent");
}

#[tokio::test(start_paused = true)]
async fn a_failed_deregistration_leaves_the_lease_to_expire() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let states = registration.subscribe();

    fake.fail_next("DeregisterPoolAgent", Status::internal("boom"));
    let error = registration
        .shutdown()
        .await
        .expect_err("deregistration fails");
    assert_eq!(error.code(), Some(Code::Internal));
    let state = states.borrow().clone();
    assert_eq!(state.phase, AgentPhase::Stopped);
    assert!(state.last_error.is_some());
    assert!(
        fake.agent_registration(CLUSTER, POOL, ROLE, AGENT)
            .is_some()
    );
}

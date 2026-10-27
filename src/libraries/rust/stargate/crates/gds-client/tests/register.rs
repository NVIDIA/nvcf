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

//! Pool registration on a paused clock.

use std::time::Duration;

use gds_client::v1::PoolState;
use gds_client::{
    DesiredState, Error, GdsClient, PoolRegistration, PoolSpec, PoolUpdate, RegistrationState,
};
use tokio_util::sync::CancellationToken;
use tonic::Status;

use crate::support::fake_gds::FakeGds;
use crate::support::{fast_policy, wait_until};

const TTL: Duration = Duration::from_secs(30);

fn spec() -> PoolSpec {
    let mut spec = PoolSpec::new("cs", "c1", "pool-a");
    spec.lease_ttl = TTL;
    spec.owner_id = "relay-0".to_string();
    spec
}

async fn register(fake: &FakeGds, stop: &CancellationToken) -> PoolRegistration {
    let (channel, server) = fake.in_memory_channel();
    // The server lives as long as the test runtime.
    std::mem::forget(server);
    let client = GdsClient::from_channel_with_policy(channel, fast_policy());
    PoolRegistration::register(client, spec(), stop.clone())
        .await
        .expect("register")
}

fn epoch(registration: &PoolRegistration) -> u64 {
    match registration.state() {
        RegistrationState::Registered { epoch, .. } => epoch,
        other => panic!("not registered: {other:?}"),
    }
}

#[tokio::test(start_paused = true)]
async fn heartbeats_every_third_of_the_lease() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let pool_id = registration.pool_id();
    assert_eq!(pool_id, "pool-1");
    assert_eq!(fake.calls("RegisterPool"), 1);
    assert_eq!(fake.calls("HeartbeatPool"), 0);
    let registered = fake.pool(&pool_id).expect("stored");
    assert!(
        registered.generation > 1_600_000_000_000,
        "generation is unix ms based"
    );
    assert_eq!(
        registered
            .lease
            .as_ref()
            .and_then(|lease| lease.ttl.as_ref())
            .map(|ttl| ttl.seconds),
        Some(30)
    );

    // Jitter keeps each interval within [0.8, 1.0] of ttl / 3.
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_millis(100), || fake.calls("HeartbeatPool")
            == 1)
        .await,
        "first heartbeat by ttl/3"
    );
    tokio::time::sleep(TTL).await;
    let heartbeats = fake.heartbeats();
    assert!(
        (4..=5).contains(&heartbeats.len()),
        "{} heartbeats in 40 s",
        heartbeats.len()
    );
    let current_epoch = epoch(&registration);
    assert!(
        heartbeats
            .iter()
            .all(|heartbeat| heartbeat.epoch == current_epoch)
    );
    assert!(
        heartbeats
            .iter()
            .all(|heartbeat| heartbeat.pool_id == pool_id)
    );
    assert_eq!(fake.calls("RegisterPool"), 1);
    stop.cancel();
    assert!(
        wait_until(Duration::from_secs(1), || matches!(
            registration.state(),
            RegistrationState::Stopped { .. }
        ))
        .await
    );
    let count = fake.calls("HeartbeatPool");
    tokio::time::sleep(TTL).await;
    assert_eq!(
        fake.calls("HeartbeatPool"),
        count,
        "no heartbeat after stop"
    );
    assert!(matches!(
        registration.set_desired_state(DesiredState::Draining).await,
        Err(Error::RegistrationLost(_))
    ));
}

#[tokio::test(start_paused = true)]
async fn not_found_heartbeat_registers_again() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let first_epoch = epoch(&registration);

    fake.fail_next("HeartbeatPool", Status::not_found("pool unknown"));
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || fake.calls("RegisterPool") == 2).await,
        "re-registered after NOT_FOUND"
    );
    assert!(
        wait_until(Duration::from_secs(1), || epoch(&registration)
            != first_epoch)
        .await
    );
    assert_eq!(registration.pool_id(), "pool-1", "pool id is stable");
    let registrations = fake.registrations();
    assert_eq!(
        registrations[1].pool.as_ref().expect("pool").pool_id,
        "pool-1"
    );
    assert!(registrations[1].generation > registrations[0].generation);

    // The next heartbeat carries the new epoch.
    tokio::time::sleep(TTL / 3).await;
    let new_epoch = epoch(&registration);
    assert!(
        wait_until(Duration::from_secs(1), || {
            fake.heartbeats()
                .last()
                .is_some_and(|heartbeat| heartbeat.epoch == new_epoch)
        })
        .await
    );
}

#[tokio::test(start_paused = true)]
async fn epoch_mismatch_registers_again() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;

    fake.bump_pool_epoch(&registration.pool_id());
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || fake.calls("RegisterPool") == 2).await,
        "re-registered after LEASE_EPOCH_MISMATCH"
    );
    assert!(matches!(
        registration.state(),
        RegistrationState::Registered { .. }
    ));
}

#[tokio::test(start_paused = true)]
async fn failing_heartbeats_publish_retrying_then_recover() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let mut states = registration.subscribe();

    fake.set_unavailable(true);
    states
        .wait_for(
            |state| matches!(state, RegistrationState::Retrying { failures, .. } if *failures >= 2),
        )
        .await
        .expect("retrying");
    fake.set_unavailable(false);
    // Retries come after a backoff shorter than the interval.
    states
        .wait_for(|state| matches!(state, RegistrationState::Registered { .. }))
        .await
        .expect("registered again");
    assert_eq!(
        fake.calls("RegisterPool"),
        1,
        "a transient failure keeps the lease"
    );
}

#[tokio::test(start_paused = true)]
async fn workers_and_desired_state_reach_gds() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let pool_id = registration.pool_id();

    registration.report_workers(8, 6);
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || {
            fake.pool(&pool_id)
                .is_some_and(|pool| pool.worker_count == 8 && pool.ready_worker_count == 6)
        })
        .await
    );

    let pool = registration
        .set_desired_state(DesiredState::Draining)
        .await
        .expect("drain");
    assert_eq!(pool.state(), PoolState::Draining);
    let update = fake.updates().pop().expect("update");
    assert_eq!(update.update_mask.expect("mask").paths, ["desired_state"]);
    assert!(matches!(
        registration.state(),
        RegistrationState::Registered {
            desired_state: DesiredState::Draining,
            state: PoolState::Draining,
            ..
        }
    ));
    // Heartbeats keep asserting DRAINING.
    tokio::time::sleep(TTL / 3).await;
    assert!(
        wait_until(Duration::from_secs(1), || {
            fake.heartbeats()
                .last()
                .is_some_and(|heartbeat| heartbeat.desired_state == PoolState::Draining as i32)
        })
        .await
    );

    let pool = registration
        .update(PoolUpdate {
            hardware_class: Some("b200-ib".to_string()),
            ..PoolUpdate::default()
        })
        .await
        .expect("update");
    assert_eq!(pool.hardware_class, "b200-ib");
}

#[tokio::test(start_paused = true)]
async fn stale_generation_is_retried_above_the_stored_one() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let pool_id = registration.pool_id();
    let far_future = u64::MAX / 2;
    fake.set_pool_generation(&pool_id, far_future);

    registration
        .update(PoolUpdate {
            worker_count: Some(3),
            ..PoolUpdate::default()
        })
        .await
        .expect("update after stale generation");
    let updates = fake.updates();
    assert_eq!(updates.len(), 2);
    assert!(updates[0].generation < far_future);
    assert_eq!(updates[1].generation, far_future + 1);
    assert_eq!(fake.pool(&pool_id).expect("pool").worker_count, 3);
}

#[tokio::test(start_paused = true)]
async fn update_of_a_forgotten_pool_registers_it_again() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    fake.remove_pool_silently(&registration.pool_id());

    let pool = registration
        .update(PoolUpdate {
            worker_count: Some(5),
            ..PoolUpdate::default()
        })
        .await
        .expect("update");
    assert_eq!(fake.calls("RegisterPool"), 2);
    assert_eq!(
        pool.worker_count, 5,
        "the re-registration carries the update"
    );
}

#[tokio::test(start_paused = true)]
async fn shutdown_drains_then_deregisters() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    let pool_id = registration.pool_id();
    let mut states = registration.subscribe();

    let drain = Duration::from_secs(25);
    let shutdown = tokio::spawn(registration.shutdown(drain));
    assert!(
        wait_until(Duration::from_secs(1), || {
            fake.pool(&pool_id)
                .is_some_and(|pool| pool.desired_state == PoolState::Draining as i32)
        })
        .await,
        "DRAINING before deregistration"
    );
    // The pool keeps heartbeating while it drains.
    tokio::time::sleep(drain / 2).await;
    assert!(fake.pool(&pool_id).is_some());
    assert!(fake.calls("HeartbeatPool") >= 1);
    assert_eq!(fake.calls("DeregisterPool"), 0);

    shutdown.await.expect("join").expect("shutdown");
    assert_eq!(fake.calls("DeregisterPool"), 1);
    assert!(fake.pool(&pool_id).is_none());
    assert!(matches!(
        &*states.borrow_and_update(),
        RegistrationState::Deregistered { .. }
    ));
    let count = fake.calls("HeartbeatPool");
    tokio::time::sleep(TTL).await;
    assert_eq!(fake.calls("HeartbeatPool"), count, "heartbeat stopped");
}

#[tokio::test(start_paused = true)]
async fn shutdown_without_drain_deregisters_at_once() {
    let fake = FakeGds::new("r");
    let stop = CancellationToken::new();
    let registration = register(&fake, &stop).await;
    registration
        .shutdown(Duration::ZERO)
        .await
        .expect("shutdown");
    assert_eq!(fake.calls("UpdatePool"), 0);
    assert_eq!(fake.calls("DeregisterPool"), 1);
}

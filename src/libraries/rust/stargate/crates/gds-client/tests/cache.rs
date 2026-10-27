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

//! PoolCache: list, watch deltas, TTL re-list and the stale flag, on a
//! paused clock.

use std::sync::Arc;
use std::time::Duration;

use gds_client::v1::{Location, ModelServed, Pool, PoolState as PoolPhase};
use gds_client::{Error, GdsClient, PoolCache, PoolCacheConfig, PoolState};
use tokio_util::sync::CancellationToken;

use crate::support::fake_gds::{FakeGds, RunningServer};
use crate::support::{fast_policy, wait_until};

const TTL: Duration = Duration::from_secs(30);

fn pool(id: &str, cluster: &str, model: &str) -> Pool {
    Pool {
        pool_id: id.to_string(),
        name: id.to_string(),
        location: Some(Location {
            cluster: cluster.to_string(),
            ..Location::default()
        }),
        models: vec![ModelServed {
            model_id: model.to_string(),
            ..ModelServed::default()
        }],
        state: PoolPhase::Active as i32,
        ..Pool::default()
    }
}

fn config() -> PoolCacheConfig {
    let mut config = PoolCacheConfig::new("cs");
    config.ttl = TTL;
    config.stale_after = 2 * TTL;
    config
}

fn start(fake: &FakeGds, config: PoolCacheConfig) -> (PoolCache, CancellationToken, RunningServer) {
    let (channel, server) = fake.in_memory_channel();
    let client = GdsClient::from_channel_with_policy(channel, fast_policy());
    let stop = CancellationToken::new();
    (PoolCache::spawn(client, config, stop.clone()), stop, server)
}

fn ids(state: &PoolState) -> Vec<&str> {
    state.pools.keys().map(String::as_str).collect()
}

async fn wait_for_state(
    cache: &PoolCache,
    condition: impl FnMut(&Arc<PoolState>) -> bool,
) -> Arc<PoolState> {
    let mut rx = cache.subscribe();
    let state = tokio::time::timeout(Duration::from_secs(300), rx.wait_for(condition))
        .await
        .expect("state reached in time")
        .expect("cache running");
    state.clone()
}

#[tokio::test(start_paused = true)]
async fn lists_then_follows_watch_deltas() {
    let fake = FakeGds::new("r");
    fake.upsert_pool(pool("p1", "c1", "llama"));
    fake.upsert_pool(pool("p2", "c2", "mixtral"));
    let (cache, _stop, _server) = start(&fake, config());

    let state = cache.wait_synced().await.expect("synced");
    assert_eq!(ids(&state), ["p1", "p2"]);
    assert!(state.synced && !state.stale);
    assert!(!state.revision.is_empty());
    assert!(state.fetched_at.is_some());
    assert_eq!(fake.calls("ListPools"), 1);
    assert!(wait_until(Duration::from_secs(1), || fake.calls("WatchPools") == 1).await);
    // The watch resumes from the list revision, so no snapshot is sent.
    assert_eq!(fake.snapshots_sent(), 0);
    assert_eq!(fake.resumes_served(), 1);

    fake.upsert_pool(pool("p3", "c1", "llama"));
    let state = wait_for_state(&cache, |state| state.len() == 3).await;
    assert_eq!(
        state
            .by_model("llama")
            .map(|pool| pool.pool_id.as_str())
            .collect::<Vec<_>>(),
        ["p1", "p3"]
    );
    fake.remove_pool("p1");
    let state = wait_for_state(&cache, |state| state.get("p1").is_none()).await;
    assert_eq!(ids(&state), ["p2", "p3"]);
    assert_eq!(state.by_cluster("c1").count(), 1);
    assert_eq!(fake.calls("ListPools"), 1, "deltas need no list");
}

#[tokio::test(start_paused = true)]
async fn relists_every_ttl() {
    let fake = FakeGds::new("r");
    fake.upsert_pool(pool("p1", "c1", "llama"));
    fake.upsert_pool(pool("p2", "c1", "llama"));
    let (cache, _stop, _server) = start(&fake, config());
    cache.wait_synced().await.expect("synced");

    // A change whose event was lost is repaired by the next full list.
    fake.remove_pool_silently("p2");
    tokio::time::sleep(TTL / 2).await;
    assert_eq!(cache.snapshot().len(), 2);
    tokio::time::sleep(TTL / 2).await;
    assert!(wait_until(Duration::from_secs(1), || fake.calls("ListPools") == 2).await);
    let state = wait_for_state(&cache, |state| state.len() == 1).await;
    assert_eq!(ids(&state), ["p1"]);
    tokio::time::sleep(TTL).await;
    assert!(wait_until(Duration::from_secs(1), || fake.calls("ListPools") == 3).await);
    // Each list reopens the watch from the new revision.
    assert!(wait_until(Duration::from_secs(1), || fake.calls("WatchPools") == 3).await);
}

#[tokio::test(start_paused = true)]
async fn refresh_now_lists_at_once() {
    let fake = FakeGds::new("r");
    fake.upsert_pool(pool("p1", "c1", "llama"));
    let (cache, _stop, _server) = start(&fake, config());
    cache.wait_synced().await.expect("synced");
    fake.remove_pool_silently("p1");

    cache.refresh_now();
    let state = wait_for_state(&cache, |state| state.is_empty()).await;
    assert!(state.is_empty());
    assert_eq!(fake.calls("ListPools"), 2);
}

#[tokio::test(start_paused = true)]
async fn without_watch_only_lists() {
    let fake = FakeGds::new("r");
    fake.upsert_pool(pool("p1", "c1", "llama"));
    let mut config = config();
    config.watch = false;
    let (cache, _stop, _server) = start(&fake, config);
    cache.wait_synced().await.expect("synced");

    fake.upsert_pool(pool("p2", "c1", "llama"));
    tokio::time::sleep(TTL / 2).await;
    assert_eq!(cache.snapshot().len(), 1, "no watch, no delta");
    let state = wait_for_state(&cache, |state| state.len() == 2).await;
    assert!(state.synced && !state.stale);
    assert_eq!(fake.calls("WatchPools"), 0);
    assert_eq!(fake.calls("ListPools"), 2);
}

#[tokio::test(start_paused = true)]
async fn turns_stale_without_gds_and_recovers() {
    let fake = FakeGds::new("r");
    fake.set_bookmark_interval(Some(Duration::from_secs(5)));
    fake.upsert_pool(pool("p1", "c1", "llama"));
    fake.upsert_pool(pool("p2", "c1", "llama"));
    let mut config = config();
    config.ttl = Duration::from_secs(600);
    config.stale_after = Duration::from_secs(20);
    let (cache, _stop, _server) = start(&fake, config);
    cache.wait_synced().await.expect("synced");

    // Bookmarks keep a quiet cache fresh well past stale_after.
    tokio::time::sleep(Duration::from_secs(60)).await;
    assert!(!cache.snapshot().stale);

    fake.set_unavailable(true);
    fake.disconnect_watches();
    let state = wait_for_state(&cache, |state| state.stale).await;
    assert_eq!(
        ids(&state),
        ["p1", "p2"],
        "stale state keeps the last pools"
    );
    assert!(state.synced);

    fake.set_unavailable(false);
    let state = wait_for_state(&cache, |state| !state.stale).await;
    assert_eq!(state.len(), 2);
}

#[tokio::test(start_paused = true)]
async fn new_view_epoch_snapshot_replaces_the_state() {
    let fake = FakeGds::new("r");
    fake.upsert_pool(pool("p1", "c1", "llama"));
    fake.upsert_pool(pool("p2", "c1", "llama"));
    let (cache, _stop, _server) = start(&fake, config());
    cache.wait_synced().await.expect("synced");
    assert!(wait_until(Duration::from_secs(1), || fake.calls("WatchPools") == 1).await);

    // A restarted replica cannot resume the revision and sends a snapshot,
    // which also repairs a change the client never saw.
    fake.remove_pool_silently("p2");
    fake.new_view_epoch();
    fake.disconnect_watches();
    let state = wait_for_state(&cache, |state| state.len() == 1).await;
    assert_eq!(ids(&state), ["p1"]);
    assert_eq!(fake.snapshots_sent(), 1);
    assert!(state.revision.starts_with("2:"), "{}", state.revision);
    assert_eq!(fake.calls("ListPools"), 1, "the snapshot needs no list");
}

#[tokio::test(start_paused = true)]
async fn stopping_the_cache_ends_it() {
    let fake = FakeGds::new("r");
    fake.set_unavailable(true);
    let (cache, stop, _server) = start(&fake, config());
    stop.cancel();
    assert!(matches!(cache.wait_synced().await, Err(Error::Shutdown)));

    let fake = FakeGds::new("r");
    let (cache, _stop, _server) = start(&fake, config());
    cache.wait_synced().await.expect("synced");
    drop(cache);
    tokio::time::sleep(Duration::from_millis(10)).await;
    let calls = fake.calls("ListPools");
    tokio::time::sleep(3 * TTL).await;
    assert_eq!(
        fake.calls("ListPools"),
        calls,
        "a dropped cache stops listing"
    );
}

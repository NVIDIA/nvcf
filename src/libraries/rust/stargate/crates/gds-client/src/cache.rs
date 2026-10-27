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

//! A TTL-cached, watch-driven view of the pools of a clusterset.

use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::Duration;

use tokio::sync::watch;
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

use crate::client::{GdsClient, WatchStream};
use crate::error::Error;
use crate::proto::v1::{
    EventType, ListPoolsRequest, Pool, PoolState as PoolPhase, WatchPoolsRequest,
    WatchPoolsResponse,
};
use crate::watch::{CacheHandle, CacheKind, EngineConfig, StateMeta, WatchEvent};

/// Default interval of the full re-list.
pub const DEFAULT_CACHE_TTL: Duration = Duration::from_secs(30);

/// A consistent view of the matching pools, keyed by `pool_id`.
#[derive(Debug, Clone, Default)]
pub struct PoolState {
    pub pools: BTreeMap<String, Arc<Pool>>,
    /// Revision of the last list, snapshot, delta or bookmark applied.
    /// Opaque; only GDS interprets it.
    pub revision: String,
    /// When the last full list or watch snapshot was applied.
    pub fetched_at: Option<Instant>,
    /// Set once the first list or snapshot was applied.
    pub synced: bool,
    /// Set while GDS has not been heard from within the stale interval. The
    /// pools are the last known ones.
    pub stale: bool,
}

impl PoolState {
    pub fn get(&self, pool_id: &str) -> Option<&Arc<Pool>> {
        self.pools.get(pool_id)
    }

    pub fn len(&self) -> usize {
        self.pools.len()
    }

    pub fn is_empty(&self) -> bool {
        self.pools.is_empty()
    }

    pub fn iter(&self) -> impl Iterator<Item = &Arc<Pool>> {
        self.pools.values()
    }

    /// Pools located in `cluster`.
    pub fn by_cluster<'a>(&'a self, cluster: &'a str) -> impl Iterator<Item = &'a Arc<Pool>> {
        self.iter().filter(move |pool| {
            pool.location
                .as_ref()
                .is_some_and(|location| location.cluster == cluster)
        })
    }

    /// Pools serving `model` as a model id or an alias.
    pub fn by_model<'a>(&'a self, model: &'a str) -> impl Iterator<Item = &'a Arc<Pool>> {
        self.iter().filter(move |pool| serves_model(pool, model))
    }

    /// Pools whose derived state is ACTIVE: live and not draining.
    pub fn active(&self) -> impl Iterator<Item = &Arc<Pool>> {
        self.iter()
            .filter(|pool| pool.state == PoolPhase::Active as i32)
    }
}

/// Whether `pool` serves `model` as a model id or an alias.
pub fn serves_model(pool: &Pool, model: &str) -> bool {
    pool.models
        .iter()
        .any(|served| served.model_id == model || served.aliases.iter().any(|alias| alias == model))
}

/// Configuration of a [`PoolCache`].
#[derive(Debug, Clone)]
pub struct PoolCacheConfig {
    /// The clusterset and filters; an empty filter matches every pool except
    /// GONE ones.
    pub filter: ListPoolsRequest,
    /// Interval of the full re-list. The watch is reopened after each list.
    pub ttl: Duration,
    /// Follow changes with a watch between lists. Without it the cache only
    /// lists every `ttl`.
    pub watch: bool,
    /// The cache turns stale when GDS was not heard from for this long.
    pub stale_after: Duration,
}

impl PoolCacheConfig {
    /// Watches every pool of `clusterset` with the default timing: re-list
    /// every 30 s, stale after 60 s.
    pub fn new(clusterset: impl Into<String>) -> Self {
        Self {
            filter: ListPoolsRequest {
                clusterset: clusterset.into(),
                ..ListPoolsRequest::default()
            },
            ttl: DEFAULT_CACHE_TTL,
            watch: true,
            stale_after: 2 * DEFAULT_CACHE_TTL,
        }
    }
}

/// A background task keeping a [`PoolState`] current: list, then watch
/// deltas, then a full re-list every TTL. Dropping the cache or cancelling
/// its token stops the task.
pub struct PoolCache {
    handle: CacheHandle<PoolKind>,
}

impl std::fmt::Debug for PoolCache {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("PoolCache").finish_non_exhaustive()
    }
}

impl PoolCache {
    /// Starts the cache task on the current Tokio runtime.
    pub fn spawn(client: GdsClient, config: PoolCacheConfig, stop: CancellationToken) -> Self {
        let engine = EngineConfig {
            ttl: config.ttl,
            watch: config.watch,
            stale_after: config.stale_after,
        };
        Self {
            handle: CacheHandle::spawn(client, config.filter, engine, &stop),
        }
    }

    /// State updates. The receiver always holds the latest state.
    pub fn subscribe(&self) -> watch::Receiver<Arc<PoolState>> {
        self.handle.subscribe()
    }

    /// The current state.
    pub fn snapshot(&self) -> Arc<PoolState> {
        self.handle.snapshot()
    }

    /// Waits for the first list or snapshot.
    pub async fn wait_synced(&self) -> Result<Arc<PoolState>, Error> {
        self.handle.wait_synced().await
    }

    /// Lists again now instead of at the next TTL.
    pub fn refresh_now(&self) {
        self.handle.refresh_now();
    }
}

struct PoolKind;

/// The key of a pool in [`PoolState::pools`]: its `pool_id`, or
/// `cluster/name` for a pool without one.
pub fn pool_key(pool: &Pool) -> String {
    if !pool.pool_id.is_empty() {
        return pool.pool_id.clone();
    }
    let cluster = pool
        .location
        .as_ref()
        .map_or("", |location| location.cluster.as_str());
    format!("{cluster}/{}", pool.name)
}

impl CacheKind for PoolKind {
    type Object = Pool;
    type Filter = ListPoolsRequest;
    type Event = WatchPoolsResponse;
    type State = PoolState;

    const NAME: &'static str = "pools";

    fn key(object: &Pool) -> String {
        pool_key(object)
    }

    async fn list(
        client: &GdsClient,
        filter: &ListPoolsRequest,
    ) -> Result<(Vec<Pool>, String), Error> {
        let response = client.list_pools(filter.clone()).await?;
        Ok((response.pools, response.revision))
    }

    async fn watch(
        client: &GdsClient,
        filter: &ListPoolsRequest,
        resume_revision: String,
    ) -> Result<WatchStream<WatchPoolsResponse>, Error> {
        client
            .watch_pools(WatchPoolsRequest {
                clusterset: filter.clusterset.clone(),
                region: filter.region.clone(),
                dc: filter.dc.clone(),
                cluster: filter.cluster.clone(),
                hardware_class: filter.hardware_class.clone(),
                model: filter.model.clone(),
                states: filter.states.clone(),
                selector: filter.selector.clone(),
                include_endpoints: filter.include_endpoints,
                resume_revision,
            })
            .await
    }

    fn event(event: WatchPoolsResponse) -> WatchEvent<Pool> {
        WatchEvent {
            event_type: EventType::try_from(event.r#type).ok(),
            object: event.pool,
            revision: event.revision,
        }
    }

    fn state(objects: &BTreeMap<String, Arc<Pool>>, meta: &StateMeta) -> PoolState {
        PoolState {
            pools: objects.clone(),
            revision: meta.revision.clone(),
            fetched_at: meta.fetched_at,
            synced: meta.synced,
            stale: meta.stale,
        }
    }

    fn is_synced(state: &PoolState) -> bool {
        state.synced
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::v1::{Location, ModelServed};

    fn pool(id: &str, cluster: &str, models: &[(&str, &[&str])], state: PoolPhase) -> Pool {
        Pool {
            pool_id: id.to_string(),
            name: id.to_string(),
            location: Some(Location {
                cluster: cluster.to_string(),
                ..Location::default()
            }),
            models: models
                .iter()
                .map(|(model_id, aliases)| ModelServed {
                    model_id: model_id.to_string(),
                    aliases: aliases.iter().map(ToString::to_string).collect(),
                    ..ModelServed::default()
                })
                .collect(),
            state: state as i32,
            ..Pool::default()
        }
    }

    fn state(pools: Vec<Pool>) -> PoolState {
        PoolState {
            pools: pools
                .into_iter()
                .map(|pool| (pool_key(&pool), Arc::new(pool)))
                .collect(),
            ..PoolState::default()
        }
    }

    fn ids<'a>(pools: impl Iterator<Item = &'a Arc<Pool>>) -> Vec<&'a str> {
        pools.map(|pool| pool.pool_id.as_str()).collect()
    }

    #[test]
    fn iterators_filter_pools() {
        let state = state(vec![
            pool("a", "c1", &[("llama", &["llama-3"])], PoolPhase::Active),
            pool("b", "c1", &[("mixtral", &[])], PoolPhase::Draining),
            pool("c", "c2", &[("llama", &[])], PoolPhase::Unhealthy),
        ]);
        assert_eq!(ids(state.by_cluster("c1")), ["a", "b"]);
        assert_eq!(ids(state.by_model("llama")), ["a", "c"]);
        assert_eq!(ids(state.by_model("llama-3")), ["a"]);
        assert_eq!(ids(state.active()), ["a"]);
        assert_eq!(state.len(), 3);
        assert!(state.get("b").is_some());
    }

    #[test]
    fn key_falls_back_to_cluster_and_name() {
        let mut anonymous = pool("", "c1", &[], PoolPhase::Active);
        anonymous.name = "p".to_string();
        assert_eq!(pool_key(&anonymous), "c1/p");
    }
}

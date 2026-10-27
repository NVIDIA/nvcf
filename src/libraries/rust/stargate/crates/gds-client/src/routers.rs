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

//! A TTL-cached, watch-driven view of the routers of a clusterset, and the
//! seed URLs pylons and Stargates derive from it.
//!
//! Routers are derived by GDS from Services labelled
//! `gds.nvidia.com/role=router`. A consumer never dials a router replica
//! directly: it dials the derived Service that the MCS controller of its own
//! cluster creates for the import,
//! `<scheme>://derived-<hash>.<namespace>.svc.cluster.local:<grpc_port>`,
//! and Stargate's `WatchStargates` reports the per-replica identities from
//! there.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::Arc;
use std::time::Duration;

use tokio::sync::watch;
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

use crate::cache::DEFAULT_CACHE_TTL;
use crate::client::{GdsClient, WatchStream};
use crate::derived::derived_service_host;
use crate::error::Error;
use crate::labels::{DIAL_SCHEME_HTTP, DIAL_SCHEME_HTTPS};
use crate::proto::v1::{
    EventType, LeaseState, ListRoutersRequest, Router, WatchRoutersRequest, WatchRoutersResponse,
};
use crate::watch::{CacheHandle, CacheKind, EngineConfig, StateMeta, WatchEvent};

/// Scheme used for a router whose export carries no
/// `gds.nvidia.com/dial-scheme` annotation.
pub const DEFAULT_DIAL_SCHEME: &str = DIAL_SCHEME_HTTPS;

/// A consistent view of the matching routers, keyed by
/// `cluster/namespace/name`.
#[derive(Debug, Clone, Default)]
pub struct RouterState {
    pub routers: BTreeMap<String, Arc<Router>>,
    /// Opaque revision of the last list, snapshot, delta or bookmark.
    pub revision: String,
    /// When the last full list or watch snapshot was applied.
    pub fetched_at: Option<Instant>,
    /// Set once the first list or snapshot was applied.
    pub synced: bool,
    /// Set while GDS has not been heard from within the stale interval.
    pub stale: bool,
}

impl RouterState {
    pub fn len(&self) -> usize {
        self.routers.len()
    }

    pub fn is_empty(&self) -> bool {
        self.routers.is_empty()
    }

    pub fn iter(&self) -> impl Iterator<Item = &Arc<Router>> {
        self.routers.values()
    }

    /// Routers in `cluster`.
    pub fn by_cluster<'a>(&'a self, cluster: &'a str) -> impl Iterator<Item = &'a Arc<Router>> {
        self.iter().filter(move |router| router.cluster == cluster)
    }

    /// The clusters that run at least one router.
    pub fn clusters(&self) -> BTreeSet<&str> {
        self.iter().map(|router| router.cluster.as_str()).collect()
    }
}

/// The key of a router in [`RouterState::routers`].
pub fn router_key(router: &Router) -> String {
    format!("{}/{}/{}", router.cluster, router.namespace, router.name)
}

/// The URL a consumer in any cluster of the clusterset dials to reach
/// `router`: `<scheme>://derived-<hash>.<namespace>.svc.cluster.local:<grpc_port>`.
/// The scheme is the router's dial scheme, [`DEFAULT_DIAL_SCHEME`] when
/// unset. Returns `None` when the router has no gRPC port, no namespace or
/// name, or an unknown dial scheme.
pub fn seed_url(router: &Router) -> Option<String> {
    let scheme = match router.dial_scheme.trim().to_ascii_lowercase().as_str() {
        "" => DEFAULT_DIAL_SCHEME,
        DIAL_SCHEME_HTTPS => DIAL_SCHEME_HTTPS,
        DIAL_SCHEME_HTTP => DIAL_SCHEME_HTTP,
        _ => return None,
    };
    let port = u16::try_from(router.grpc_port)
        .ok()
        .filter(|port| *port != 0)?;
    if router.namespace.is_empty() || router.name.is_empty() {
        return None;
    }
    Some(format!(
        "{scheme}://{}:{port}",
        derived_service_host(&router.namespace, &router.name)
    ))
}

/// Whether a router can be offered as a seed: its cluster lease is not
/// expired and it has a dialable seed URL.
fn usable(router: &Router) -> bool {
    router.cluster_lease_state != LeaseState::Expired as i32 && seed_url(router).is_some()
}

/// The `WatchStargates` URLs a Stargate in `own_cluster` adds to its
/// `watch_stargate_urls`: one seed URL per router Service of every other
/// cluster. Routers whose cluster lease expired are skipped.
///
/// The derived name depends only on the namespace and name, so routers
/// exported under the same namespace and name from several clusters share one
/// derived Service (and one URL) whose VIP spans all of them.
pub fn remote_watch_urls(state: &RouterState, own_cluster: &str) -> BTreeSet<String> {
    state
        .iter()
        .filter(|router| router.cluster != own_cluster && usable(router))
        .filter_map(|router| seed_url(router))
        .collect()
}

/// The seeds a pylon passes to its registration loop: one seed URL per
/// router Service of every cluster, its own included. Routers whose cluster
/// lease expired are skipped.
pub fn pylon_seed_urls(state: &RouterState) -> BTreeSet<String> {
    state
        .iter()
        .filter(|router| usable(router))
        .filter_map(|router| seed_url(router))
        .collect()
}

/// Configuration of a [`RouterCache`].
#[derive(Debug, Clone)]
pub struct RouterCacheConfig {
    /// The clusterset and optional region and cluster filters.
    pub filter: ListRoutersRequest,
    /// Interval of the full re-list.
    pub ttl: Duration,
    /// Follow changes with a watch between lists.
    pub watch: bool,
    /// The cache turns stale when GDS was not heard from for this long.
    pub stale_after: Duration,
}

impl RouterCacheConfig {
    /// Watches every router of `clusterset`: re-list every 30 s, stale after
    /// 60 s.
    pub fn new(clusterset: impl Into<String>) -> Self {
        Self {
            filter: ListRoutersRequest {
                clusterset: clusterset.into(),
                ..ListRoutersRequest::default()
            },
            ttl: DEFAULT_CACHE_TTL,
            watch: true,
            stale_after: 2 * DEFAULT_CACHE_TTL,
        }
    }
}

/// A background task keeping a [`RouterState`] current, with the same
/// list, watch and re-list cycle as [`crate::cache::PoolCache`].
pub struct RouterCache {
    handle: CacheHandle<RouterKind>,
}

impl std::fmt::Debug for RouterCache {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("RouterCache").finish_non_exhaustive()
    }
}

impl RouterCache {
    /// Starts the cache task on the current Tokio runtime.
    pub fn spawn(client: GdsClient, config: RouterCacheConfig, stop: CancellationToken) -> Self {
        let engine = EngineConfig {
            ttl: config.ttl,
            watch: config.watch,
            stale_after: config.stale_after,
        };
        Self {
            handle: CacheHandle::spawn(client, config.filter, engine, &stop),
        }
    }

    pub fn subscribe(&self) -> watch::Receiver<Arc<RouterState>> {
        self.handle.subscribe()
    }

    pub fn snapshot(&self) -> Arc<RouterState> {
        self.handle.snapshot()
    }

    pub async fn wait_synced(&self) -> Result<Arc<RouterState>, Error> {
        self.handle.wait_synced().await
    }

    pub fn refresh_now(&self) {
        self.handle.refresh_now();
    }
}

struct RouterKind;

impl CacheKind for RouterKind {
    type Object = Router;
    type Filter = ListRoutersRequest;
    type Event = WatchRoutersResponse;
    type State = RouterState;

    const NAME: &'static str = "routers";

    fn key(object: &Router) -> String {
        router_key(object)
    }

    async fn list(
        client: &GdsClient,
        filter: &ListRoutersRequest,
    ) -> Result<(Vec<Router>, String), Error> {
        let response = client.list_routers(filter.clone()).await?;
        Ok((response.routers, response.revision))
    }

    async fn watch(
        client: &GdsClient,
        filter: &ListRoutersRequest,
        resume_revision: String,
    ) -> Result<WatchStream<WatchRoutersResponse>, Error> {
        client
            .watch_routers(WatchRoutersRequest {
                clusterset: filter.clusterset.clone(),
                region: filter.region.clone(),
                cluster: filter.cluster.clone(),
                resume_revision,
            })
            .await
    }

    fn event(event: WatchRoutersResponse) -> WatchEvent<Router> {
        WatchEvent {
            event_type: EventType::try_from(event.r#type).ok(),
            object: event.router,
            revision: event.revision,
        }
    }

    fn state(objects: &BTreeMap<String, Arc<Router>>, meta: &StateMeta) -> RouterState {
        RouterState {
            routers: objects.clone(),
            revision: meta.revision.clone(),
            fetched_at: meta.fetched_at,
            synced: meta.synced,
            stale: meta.stale,
        }
    }

    fn is_synced(state: &RouterState) -> bool {
        state.synced
    }
}

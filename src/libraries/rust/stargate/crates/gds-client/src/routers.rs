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
//! `gds.nvidia.com/role=router`, one [`Router`] per cluster, namespace and
//! name. The seeds of a router are its ready replicas,
//! `<scheme>://<replica address>:<grpc_port>` ([`router_seed_urls`]), so a
//! seed always reaches the routers of one cluster, and Stargate's
//! `WatchStargates` on a seed reports the per-replica identities from there.
//!
//! The derived Service of a router's import ([`derived_service_url`]) is not
//! a seed. Every cluster deploys the same router Service, by MCS semantics
//! their exports merge into one import, and its VIP spreads over the
//! k8s-router pods of all router clusters: a pylon dialing it could reach
//! another cluster's k8s-router than the Stargate it registers with.
//!
//! Pylon and the Dynamo PoolRelay both dial routers from the same records:
//! Pylon through [`pylon_seed_urls`], the PoolRelay through
//! [`relay_seed_urls`]. Both are the gRPC replica URLs today.

use std::collections::{BTreeMap, BTreeSet};
use std::net::IpAddr;
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

/// The scheme of `router`'s URLs: its dial scheme, [`DEFAULT_DIAL_SCHEME`]
/// when unset. `None` for a scheme other than `http` and `https`.
fn dial_scheme(router: &Router) -> Option<&'static str> {
    match router.dial_scheme.trim().to_ascii_lowercase().as_str() {
        "" => Some(DEFAULT_DIAL_SCHEME),
        DIAL_SCHEME_HTTPS => Some(DIAL_SCHEME_HTTPS),
        DIAL_SCHEME_HTTP => Some(DIAL_SCHEME_HTTP),
        _ => None,
    }
}

/// The gRPC port of `router`, `None` when unset or out of range.
fn grpc_port(router: &Router) -> Option<u16> {
    u16::try_from(router.grpc_port)
        .ok()
        .filter(|port| *port != 0)
}

/// The UDP port of `router`'s reverse tunnel listener, named by the
/// `gds.nvidia.com/quic-port-name` annotation (default `quic`). `None` when
/// unset or out of range. Informational for Pylon, whose tunnel address comes
/// from the Stargate registration ack.
pub fn quic_port(router: &Router) -> Option<u16> {
    u16::try_from(router.quic_port)
        .ok()
        .filter(|port| *port != 0)
}

/// The host of a seed URL for a replica address: an IPv4 address or a host
/// name as is, an IPv6 address in brackets. `None` for an empty address, an
/// IPv6 address with a zone, or another address that contains a colon.
fn seed_host(address: &str) -> Option<String> {
    let address = address.trim();
    match address.parse::<IpAddr>() {
        Ok(IpAddr::V4(_)) => Some(address.to_string()),
        Ok(IpAddr::V6(_)) => Some(format!("[{address}]")),
        Err(_) if address.is_empty() || address.contains(':') => None,
        Err(_) => Some(address.to_string()),
    }
}

/// The seed URLs of `router`, one per ready replica, sorted and without
/// duplicates: `<scheme>://<replica address>:<grpc_port>`, with IPv6
/// addresses in brackets. The scheme is the router's dial scheme,
/// [`DEFAULT_DIAL_SCHEME`] when unset. Empty when the router has no gRPC port
/// or an unknown dial scheme. Replicas that are not ready, or whose address
/// is empty, an IPv6 address with a zone or otherwise contains a colon, are
/// skipped. The cluster lease is not checked; [`pylon_seed_urls`] and
/// [`remote_watch_urls`] check it.
///
/// The port is the router Service's gRPC port, so the Service must use the
/// same number for the port and its target port, as the llm-request-router
/// chart does.
pub fn router_seed_urls(router: &Router) -> Vec<String> {
    grpc_replica_urls(router).into_iter().collect()
}

/// The URLs a Dynamo PoolRelay dials to publish its state streams to
/// `router`, one per ready replica: today the same
/// `<scheme>://<replica address>:<grpc_port>` URLs as [`router_seed_urls`],
/// since the relay uses the routers' gRPC listener. It is a separate
/// function so that a relay-specific listener or scheme can be introduced
/// without changing the Pylon seeds. The cluster lease is not checked;
/// [`relay_seed_urls`] checks it.
pub fn router_relay_urls(router: &Router) -> BTreeSet<String> {
    grpc_replica_urls(router)
}

/// `<scheme>://<replica address>:<grpc_port>` for every ready replica of
/// `router`, as documented on [`router_seed_urls`].
fn grpc_replica_urls(router: &Router) -> BTreeSet<String> {
    let (Some(scheme), Some(port)) = (dial_scheme(router), grpc_port(router)) else {
        return BTreeSet::new();
    };
    router
        .replicas
        .iter()
        .filter(|replica| replica.ready)
        .filter_map(|replica| seed_host(&replica.address))
        .map(|host| format!("{scheme}://{host}:{port}"))
        .collect()
}

/// The URL of the derived Service of `router`'s import in the caller's
/// cluster: `<scheme>://derived-<hash>.<namespace>.svc.cluster.local:<grpc_port>`.
/// Every cluster that exports the router's namespace and name joins that one
/// import, so this VIP spreads over the router replicas of all of them. It
/// is not a seed (a pylon could reach another cluster's k8s-router than the
/// Stargate it registers with); use it only where the merged VIP is wanted.
/// Returns `None` when the router has no gRPC port, no namespace or name, or
/// an unknown dial scheme.
pub fn derived_service_url(router: &Router) -> Option<String> {
    let scheme = dial_scheme(router)?;
    let port = grpc_port(router)?;
    if router.namespace.is_empty() || router.name.is_empty() {
        return None;
    }
    Some(format!(
        "{scheme}://{}:{port}",
        derived_service_host(&router.namespace, &router.name)
    ))
}

/// Whether the cluster lease of `router` has not expired.
fn lease_live(router: &Router) -> bool {
    router.cluster_lease_state != LeaseState::Expired as i32
}

/// The `WatchStargates` URLs a Stargate in `own_cluster` adds to its
/// `watch_stargate_urls`: the [`router_seed_urls`] of every router in
/// another cluster whose cluster lease has not expired. Each remote router
/// cluster contributes one URL per ready replica.
pub fn remote_watch_urls(state: &RouterState, own_cluster: &str) -> BTreeSet<String> {
    state
        .iter()
        .filter(|router| router.cluster != own_cluster && lease_live(router))
        .flat_map(|router| router_seed_urls(router))
        .collect()
}

/// The seeds a pylon passes to its registration loop: the
/// [`router_seed_urls`] of every router whose cluster lease has not expired,
/// its own cluster included. Each router cluster contributes one URL per
/// ready replica (its stargate-k8s-router or Stargate pods); the pylon opens
/// `WatchStargates` on each and fails over between them.
pub fn pylon_seed_urls(state: &RouterState) -> BTreeSet<String> {
    state
        .iter()
        .filter(|router| lease_live(router))
        .flat_map(|router| router_seed_urls(router))
        .collect()
}

/// The URLs a Dynamo PoolRelay subscribes to: the [`router_relay_urls`] of
/// every router whose cluster lease has not expired, its own cluster
/// included. The relay opens its state streams over gRPC on each and uses no
/// QUIC. Today the result equals [`pylon_seed_urls`]; use this function in
/// the relay so a relay-specific listener can diverge later.
pub fn relay_seed_urls(state: &RouterState) -> BTreeSet<String> {
    state
        .iter()
        .filter(|router| lease_live(router))
        .flat_map(|router| router_relay_urls(router))
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

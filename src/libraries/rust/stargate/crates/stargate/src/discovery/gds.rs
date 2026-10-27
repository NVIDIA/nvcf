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

//! Remote router discovery through the Global Discovery Service (GDS).
//!
//! [`GdsDiscovery`] wraps the local peer discovery, which stays authoritative
//! for the Stargates of this cluster, and adds the router replica seeds of
//! every other cluster of the clusterset to `watch_stargate_urls`. Pylons
//! that follow those URLs reach the routers of the other clusters through
//! this Stargate's `WatchStargates`.

use std::collections::BTreeSet;
use std::sync::Arc;

use gds_client::{GdsClient, RouterCache, RouterCacheConfig, RouterState, remote_watch_urls};
use parking_lot::Mutex;
use stargate_proto::pb::StargateInfo;
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use super::Discovery;

/// What [`GdsDiscovery`] last published, so it logs only changes.
#[derive(Debug, Default, PartialEq, Eq)]
struct Published {
    urls: BTreeSet<String>,
    stale: bool,
}

/// Local peer discovery plus the routers of the other clusters from GDS.
pub struct GdsDiscovery {
    inner: Box<dyn Discovery>,
    routers: watch::Receiver<Arc<RouterState>>,
    own_cluster: String,
    published: Mutex<Published>,
    /// Keeps the router cache task running; dropping it stops the task.
    _cache: Option<RouterCache>,
}

impl GdsDiscovery {
    /// Starts a router cache for `clusterset` on the current Tokio runtime
    /// and wraps `inner`. `own_cluster` is this Stargate's cluster id in the
    /// clusterset; its routers are not remote.
    pub fn spawn(
        inner: Box<dyn Discovery>,
        client: GdsClient,
        clusterset: &str,
        own_cluster: &str,
    ) -> Self {
        let cache = RouterCache::spawn(
            client,
            RouterCacheConfig::new(clusterset),
            CancellationToken::new(),
        );
        let mut discovery = Self::from_routers(inner, cache.subscribe(), own_cluster);
        discovery._cache = Some(cache);
        discovery
    }

    /// Wraps `inner` with the routers published on `routers`, for hosts that
    /// run their own [`RouterCache`] and for tests.
    pub fn from_routers(
        inner: Box<dyn Discovery>,
        routers: watch::Receiver<Arc<RouterState>>,
        own_cluster: impl Into<String>,
    ) -> Self {
        Self {
            inner,
            routers,
            own_cluster: own_cluster.into(),
            published: Mutex::new(Published::default()),
            _cache: None,
        }
    }

    /// The seeds of the routers of the other clusters, from the latest
    /// router view. A stale view keeps its last known routers.
    fn gds_remote_watch_urls(&self) -> BTreeSet<String> {
        let (urls, stale) = {
            let state = self.routers.borrow();
            (remote_watch_urls(&state, &self.own_cluster), state.stale)
        };
        self.log_change(&urls, stale);
        urls
    }

    fn log_change(&self, urls: &BTreeSet<String>, stale: bool) {
        let mut published = self.published.lock();
        if published.stale != stale {
            if stale {
                warn!(
                    own_cluster = %self.own_cluster,
                    "GDS router view is stale; keeping the last known remote watch URLs"
                );
            } else {
                info!(own_cluster = %self.own_cluster, "GDS router view is current again");
            }
            published.stale = stale;
        }
        if published.urls != *urls {
            let added: Vec<&String> = urls.difference(&published.urls).collect();
            let removed: Vec<&String> = published.urls.difference(urls).collect();
            info!(
                own_cluster = %self.own_cluster,
                remote_watch_url_count = urls.len(),
                ?added,
                ?removed,
                "GDS remote watch URLs changed"
            );
            published.urls.clone_from(urls);
        }
    }
}

#[async_trait::async_trait]
impl Discovery for GdsDiscovery {
    fn initial_stargates(&self) -> Vec<StargateInfo> {
        self.inner.initial_stargates()
    }

    async fn discover_stargates(&self) -> Vec<StargateInfo> {
        self.inner.discover_stargates().await
    }

    async fn discover_remote_watch_urls(&self) -> Vec<String> {
        let mut urls = self.gds_remote_watch_urls();
        urls.extend(self.inner.discover_remote_watch_urls().await);
        urls.into_iter().collect()
    }
}

#[cfg(test)]
mod tests {
    use std::net::SocketAddr;

    use gds_client::v1::{LeaseState, Router, RouterReplica};

    use super::*;
    use crate::discovery::SelfOnlyDiscovery;

    fn router(cluster: &str, scheme: &str, addresses: &[&str]) -> Router {
        Router {
            cluster: cluster.to_string(),
            namespace: "nvcf".to_string(),
            name: "llm-request-router-backend-router".to_string(),
            grpc_port: 50071,
            dial_scheme: scheme.to_string(),
            cluster_lease_state: LeaseState::Active as i32,
            replicas: addresses
                .iter()
                .map(|address| RouterReplica {
                    address: address.to_string(),
                    ready: true,
                    ..RouterReplica::default()
                })
                .collect(),
            ..Router::default()
        }
    }

    fn state(routers: Vec<Router>) -> Arc<RouterState> {
        Arc::new(RouterState {
            routers: routers
                .into_iter()
                .map(|router| (gds_client::routers::router_key(&router), Arc::new(router)))
                .collect(),
            synced: true,
            ..RouterState::default()
        })
    }

    fn self_only() -> Box<dyn Discovery> {
        let addr: SocketAddr = "10.0.0.1:50071".parse().unwrap();
        Box::new(SelfOnlyDiscovery::new(addr, "stargate-0".to_string(), 8000))
    }

    #[tokio::test]
    async fn remote_watch_urls_are_the_seeds_of_other_live_clusters() {
        let mut expired = router("d", "http", &["10.4.0.1"]);
        expired.cluster_lease_state = LeaseState::Expired as i32;
        let (_tx, rx) = watch::channel(state(vec![
            router("a", "http", &["10.1.0.1", "10.1.0.2"]),
            router("b", "http", &["10.2.0.2", "10.2.0.1"]),
            router("c", "", &["fd00::1"]),
            expired,
        ]));
        let discovery = GdsDiscovery::from_routers(self_only(), rx, "a");

        assert_eq!(
            discovery.discover_remote_watch_urls().await,
            [
                "http://10.2.0.1:50071",
                "http://10.2.0.2:50071",
                "https://[fd00::1]:50071",
            ]
        );
    }

    #[tokio::test]
    async fn local_peers_stay_authoritative() {
        let (_tx, rx) = watch::channel(state(vec![router("b", "http", &["10.2.0.1"])]));
        let inner = self_only();
        let expected = inner.initial_stargates();
        let discovery = GdsDiscovery::from_routers(inner, rx, "a");

        assert_eq!(discovery.initial_stargates(), expected);
        assert_eq!(discovery.discover_stargates().await, expected);
    }

    #[tokio::test]
    async fn remote_watch_urls_follow_router_changes() {
        let (tx, rx) = watch::channel(Arc::new(RouterState::default()));
        let discovery = GdsDiscovery::from_routers(self_only(), rx, "a");
        assert!(discovery.discover_remote_watch_urls().await.is_empty());

        tx.send(state(vec![router("b", "http", &["10.2.0.1"])]))
            .unwrap();
        assert_eq!(
            discovery.discover_remote_watch_urls().await,
            ["http://10.2.0.1:50071"]
        );

        // A stale view keeps the last known routers.
        let mut stale = (*state(vec![router("b", "http", &["10.2.0.1"])])).clone();
        stale.stale = true;
        tx.send(Arc::new(stale)).unwrap();
        assert_eq!(
            discovery.discover_remote_watch_urls().await,
            ["http://10.2.0.1:50071"]
        );
        assert!(discovery.published.lock().stale);

        tx.send(state(Vec::new())).unwrap();
        assert!(discovery.discover_remote_watch_urls().await.is_empty());
        assert!(!discovery.published.lock().stale);
    }

    #[tokio::test]
    async fn spawned_cache_starts_empty_without_a_reachable_gds() {
        let client = GdsClient::new(gds_client::GdsClientConfig {
            urls: vec!["http://127.0.0.1:1".to_string()],
            policy: gds_client::ConnectPolicy {
                allow_insecure_http: true,
                ..gds_client::ConnectPolicy::default()
            },
            ..gds_client::GdsClientConfig::default()
        })
        .unwrap();
        let discovery = GdsDiscovery::spawn(self_only(), client, "prod", "a");

        assert!(discovery.discover_remote_watch_urls().await.is_empty());
        assert_eq!(
            discovery.discover_stargates().await,
            self_only().initial_stargates()
        );
    }
}

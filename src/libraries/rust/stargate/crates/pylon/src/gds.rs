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

//! The seeds Pylon registers through: the static `--stargate-address`, the
//! router replica seeds the Global Discovery Service (GDS) lists for the
//! clusterset, or both.

use std::collections::BTreeSet;
use std::sync::Arc;

use anyhow::{Context, Result};
use gds_client::{GdsClient, GdsClientConfig, RouterCache, RouterCacheConfig, RouterState};
use stargate_runtime::OwnedTask;
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

/// Where GDS router seeds come from.
#[derive(Debug, Clone)]
pub(crate) struct GdsSeedConfig {
    pub(crate) client: GdsClientConfig,
    pub(crate) clusterset: String,
}

/// The seed set handed to the registration loop, and what keeps it current.
pub(crate) struct RouterSeeds {
    seeds: watch::Receiver<BTreeSet<String>>,
    feed: Option<GdsSeedFeed>,
}

/// The router cache and the task that turns its view into seeds. Dropping
/// it stops both.
struct GdsSeedFeed {
    client: GdsClient,
    _cache: RouterCache,
    _task: OwnedTask,
}

impl RouterSeeds {
    /// The static address alone, or, with GDS, a router cache on the current
    /// Tokio runtime whose seeds are published together with the static
    /// address. Seeds published before the cache synced are the static
    /// address only.
    pub(crate) fn start(static_address: Option<&str>, gds: Option<&GdsSeedConfig>) -> Result<Self> {
        let static_seeds: BTreeSet<String> = static_address
            .map(str::trim)
            .filter(|address| !address.is_empty())
            .map(ToString::to_string)
            .into_iter()
            .collect();
        let Some(gds) = gds else {
            return Ok(Self {
                seeds: pylon_lib::static_seeds(static_seeds),
                feed: None,
            });
        };
        let client =
            GdsClient::new(gds.client.clone()).context("invalid GDS client configuration")?;
        let cache = RouterCache::spawn(
            client.clone(),
            RouterCacheConfig::new(gds.clusterset.clone()),
            CancellationToken::new(),
        );
        let (seeds_tx, seeds) = watch::channel(static_seeds.clone());
        let routers = cache.subscribe();
        let task = OwnedTask::spawn("GDS router seeds", move |stop| {
            feed_seeds(routers, static_seeds, seeds_tx, stop)
        });
        info!(
            gds_urls = ?gds.client.urls,
            clusterset = %gds.clusterset,
            "discovering Stargate seeds through GDS"
        );
        Ok(Self {
            seeds,
            feed: Some(GdsSeedFeed {
                client,
                _cache: cache,
                _task: task,
            }),
        })
    }

    pub(crate) fn receiver(&self) -> watch::Receiver<BTreeSet<String>> {
        self.seeds.clone()
    }

    /// The GDS client of the router cache, `None` without GDS. Clones share
    /// its connection.
    pub(crate) fn gds_client(&self) -> Option<&GdsClient> {
        self.feed.as_ref().map(|feed| &feed.client)
    }
}

/// Publishes the static seeds plus the [`gds_client::pylon_seed_urls`] of
/// every router view on `seeds_tx`, until `stop` is cancelled or the view
/// channel closes. A stale view keeps its last known routers.
pub(crate) async fn feed_seeds(
    mut routers: watch::Receiver<Arc<RouterState>>,
    static_seeds: BTreeSet<String>,
    seeds_tx: watch::Sender<BTreeSet<String>>,
    stop: CancellationToken,
) {
    let mut stale = false;
    let mut warned_no_routers = false;
    loop {
        let (gds_seeds, router_count, synced, now_stale) = {
            let state = routers.borrow_and_update();
            (
                gds_client::pylon_seed_urls(&state),
                state.len(),
                state.synced,
                state.stale,
            )
        };
        if now_stale != stale {
            if now_stale {
                warn!("GDS router view is stale; keeping the last known Stargate seeds");
            } else {
                info!("GDS router view is current again");
            }
            stale = now_stale;
        }
        let mut seeds = static_seeds.clone();
        seeds.extend(gds_seeds);
        if warn_no_routers(&mut warned_no_routers, synced, &seeds) {
            warn!(
                router_count,
                "GDS lists no router with a live lease and ready replicas; no Stargate to register with"
            );
        }
        seeds_tx.send_if_modified(|current| {
            if *current == seeds {
                return false;
            }
            info!(
                seed_count = seeds.len(),
                router_count,
                added = seeds.difference(current).count(),
                removed = current.difference(&seeds).count(),
                "GDS Stargate seeds changed"
            );
            *current = std::mem::take(&mut seeds);
            true
        });
        tokio::select! {
            _ = stop.cancelled() => return,
            changed = routers.changed() => {
                if changed.is_err() {
                    return;
                }
            }
        }
    }
}

/// Whether to warn that the synced router view gives no seed: once each time
/// the seed set becomes empty, including at startup.
fn warn_no_routers(warned: &mut bool, synced: bool, seeds: &BTreeSet<String>) -> bool {
    let empty = synced && seeds.is_empty();
    let warn = empty && !*warned;
    *warned = empty;
    warn
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use gds_client::v1::{LeaseState, Router, RouterReplica};

    use super::*;

    fn router(cluster: &str, addresses: &[&str]) -> Router {
        Router {
            cluster: cluster.to_string(),
            namespace: "nvcf".to_string(),
            name: "llm-request-router-backend-router".to_string(),
            grpc_port: 50071,
            dial_scheme: "http".to_string(),
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

    fn set(urls: &[&str]) -> BTreeSet<String> {
        urls.iter().map(ToString::to_string).collect()
    }

    async fn wait_for(seeds: &mut watch::Receiver<BTreeSet<String>>, expected: &[&str]) {
        let expected = set(expected);
        tokio::time::timeout(Duration::from_secs(5), async {
            loop {
                if *seeds.borrow_and_update() == expected {
                    return;
                }
                seeds.changed().await.expect("the feed should be running");
            }
        })
        .await
        .unwrap_or_else(|_| panic!("seeds never became {expected:?}"));
    }

    #[test]
    fn no_router_warning_fires_once_per_empty_period() {
        let mut warned = false;
        let none = BTreeSet::new();
        let some = set(&["http://10.1.0.1:50071"]);

        assert!(
            !warn_no_routers(&mut warned, false, &none),
            "not before the first list"
        );
        assert!(warn_no_routers(&mut warned, true, &none));
        assert!(
            !warn_no_routers(&mut warned, true, &none),
            "once while it stays empty"
        );
        assert!(!warn_no_routers(&mut warned, true, &some));
        assert!(
            warn_no_routers(&mut warned, true, &none),
            "again after seeds came and went"
        );
    }

    #[tokio::test]
    async fn static_address_alone_never_changes() {
        let seeds = RouterSeeds::start(Some(" http://stargate:50071 "), None).unwrap();
        let receiver = seeds.receiver();

        assert_eq!(*receiver.borrow(), set(&["http://stargate:50071"]));
        assert!(
            receiver.has_changed().is_err(),
            "a static seed set has no live sender"
        );
        assert!(seeds.gds_client().is_none());
    }

    #[tokio::test]
    async fn feed_unions_static_seeds_with_live_router_seeds() {
        let (routers_tx, routers_rx) = watch::channel(Arc::new(RouterState::default()));
        let (seeds_tx, mut seeds) = watch::channel(BTreeSet::new());
        let stop = CancellationToken::new();
        let task = tokio::spawn(feed_seeds(
            routers_rx,
            set(&["http://local-stargate:50071"]),
            seeds_tx,
            stop.clone(),
        ));
        wait_for(&mut seeds, &["http://local-stargate:50071"]).await;

        let mut expired = router("c", &["10.3.0.1"]);
        expired.cluster_lease_state = LeaseState::Expired as i32;
        routers_tx
            .send(state(vec![
                router("a", &["10.1.0.1"]),
                router("b", &["10.2.0.1", "10.2.0.2"]),
                expired,
            ]))
            .unwrap();
        wait_for(
            &mut seeds,
            &[
                "http://10.1.0.1:50071",
                "http://10.2.0.1:50071",
                "http://10.2.0.2:50071",
                "http://local-stargate:50071",
            ],
        )
        .await;

        routers_tx
            .send(state(vec![router("a", &["10.1.0.1"])]))
            .unwrap();
        wait_for(
            &mut seeds,
            &["http://10.1.0.1:50071", "http://local-stargate:50071"],
        )
        .await;

        stop.cancel();
        tokio::time::timeout(Duration::from_secs(5), task)
            .await
            .expect("the feed should stop")
            .expect("the feed should not panic");
    }

    #[tokio::test]
    async fn feed_stops_when_the_router_view_closes() {
        let (routers_tx, routers_rx) = watch::channel(state(vec![router("a", &["10.1.0.1"])]));
        let (seeds_tx, mut seeds) = watch::channel(BTreeSet::new());
        let task = tokio::spawn(feed_seeds(
            routers_rx,
            BTreeSet::new(),
            seeds_tx,
            CancellationToken::new(),
        ));
        wait_for(&mut seeds, &["http://10.1.0.1:50071"]).await;

        drop(routers_tx);
        tokio::time::timeout(Duration::from_secs(5), task)
            .await
            .expect("the feed should stop")
            .expect("the feed should not panic");
        assert!(seeds.has_changed().is_err(), "the seed sender is dropped");
        assert_eq!(*seeds.borrow(), set(&["http://10.1.0.1:50071"]));
    }

    #[tokio::test]
    async fn gds_seeds_start_with_the_static_address_before_gds_answers() {
        let seeds = RouterSeeds::start(
            Some("http://stargate:50071"),
            Some(&GdsSeedConfig {
                client: GdsClientConfig {
                    urls: vec!["http://127.0.0.1:1".to_string()],
                    policy: gds_client::ConnectPolicy {
                        allow_insecure_http: true,
                        ..gds_client::ConnectPolicy::default()
                    },
                    ..GdsClientConfig::default()
                },
                clusterset: "prod".to_string(),
            }),
        )
        .unwrap();
        let receiver = seeds.receiver();

        assert_eq!(*receiver.borrow(), set(&["http://stargate:50071"]));
        assert!(
            receiver.has_changed().is_ok(),
            "a GDS seed set follows the router cache"
        );
    }
}

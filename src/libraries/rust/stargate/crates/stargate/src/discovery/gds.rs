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
//! [`GdsDiscovery`] wraps the local peer discovery and adds the router replica
//! seeds of every other cluster of the clusterset to `watch_stargate_urls`.
//! Pylons that follow those URLs reach the routers of the other clusters
//! through this Stargate's `WatchStargates`.
//!
//! In direct router mode ([`is_direct`]) the exported router Service selects
//! the Stargate pods themselves, and each pod advertises its pod IP.
//! [`GdsDiscovery`] then finds the router record of its own cluster, the one
//! whose replicas include this Stargate's address, ready or not, and uses it
//! for the names and the dial addresses of the local Stargates.
//!
//! Peer names ([`Discovery::discover_stargates`]). Headless DNS discovery
//! lists Deployment pods by the dashed-IP name Kubernetes DNS gives an
//! endpoint without a hostname, while each pod lists itself by its pod name.
//! A pylon that watches every replica would see each pod under two
//! authorities and open two registrations with it, which Stargate rejects
//! as a duplicate. When the router record names this Stargate by the host it
//! advertises (the record's hostname template with `{pod_name}` replaced by
//! this Stargate's id, on the record's gRPC port), the peers are listed under
//! their replica identities instead:
//!
//! - a peer that local discovery lists by a name of a replica with an
//!   identity becomes `stargate_id` = the identity, `advertise_addr` =
//!   [`replica_authority`] and `grpc_pylon_dial_addr` =
//!   `<dial scheme>://<replica address>:<grpc port>`. The names of a replica
//!   are the hostname template with `{pod_name}` replaced by its identity,
//!   its EndpointSlice hostname, or its address with `.` and `:` turned into
//!   `-` (the SRV name of an endpoint without a hostname);
//! - a ready replica with an identity that local discovery does not list is
//!   added the same way;
//! - this Stargate keeps its own entry, and its other names are dropped;
//! - every other peer, including peers advertised by IP address and replicas
//!   without an identity, is kept as local discovery lists it.
//!
//! Otherwise the peers stay as local discovery lists them.
//!
//! Peer dial addresses ([`Discovery::peer_dial_addrs`]). A dial URL for each
//! local Stargate, `<dial scheme>://<replica address>:<grpc port>`, keyed by
//! the host local discovery advertises for it: the replica's address, for
//! peers advertised by IP address, and each of the replica's names above.
//! This Stargate itself is keyed by the host it advertises and dials its own
//! advertise address. The publisher fills the dial address of every entry
//! that has none from this map, so this Stargate and the peers kept as listed
//! get one too.

use std::collections::{BTreeSet, HashMap, HashSet};
use std::net::{IpAddr, SocketAddr};
use std::sync::Arc;

use gds_client::labels::{self, DIAL_SCHEME_HTTP, DIAL_SCHEME_HTTPS};
use gds_client::routers::{DEFAULT_DIAL_SCHEME, is_direct, replica_authority, router_key};
use gds_client::v1::{Router, RouterReplica};
use gds_client::{GdsClient, RouterCache, RouterCacheConfig, RouterState, remote_watch_urls};
use parking_lot::Mutex;
use stargate_proto::pb::StargateInfo;
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use super::{Discovery, advertise_host};

/// How [`GdsDiscovery`] last listed the local Stargates.
#[derive(Debug, Default, Clone, PartialEq, Eq)]
enum PeerNames {
    /// As local discovery lists them: no direct-mode router record of this
    /// cluster lists this Stargate.
    #[default]
    Local,
    /// As local discovery lists them, because the direct-mode router record
    /// with this key does not name this Stargate by the host it advertises.
    TemplateMismatch(String),
    /// Under the replica identities of the router record with this key.
    Identities(String),
}

/// What [`GdsDiscovery`] last published, so it logs only changes.
#[derive(Debug, Default, PartialEq, Eq)]
struct Published {
    urls: BTreeSet<String>,
    stale: bool,
    /// Key of the own direct-mode router record peer dial URLs come from.
    direct_router: Option<String>,
    peer_names: PeerNames,
}

/// Local peer discovery plus the routers of the other clusters from GDS.
pub struct GdsDiscovery {
    inner: Box<dyn Discovery>,
    routers: watch::Receiver<Arc<RouterState>>,
    own_cluster: String,
    /// This Stargate's `--advertise-addr`; its IP finds the own router
    /// record among the routers of `own_cluster`.
    advertise_addr: SocketAddr,
    published: Mutex<Published>,
    /// Keeps the router cache task running; dropping it stops the task.
    _cache: Option<RouterCache>,
}

impl GdsDiscovery {
    /// Starts a router cache for `clusterset` on the current Tokio runtime
    /// and wraps `inner`. `own_cluster` is this Stargate's cluster id in the
    /// clusterset; its routers are not remote. `advertise_addr` is this
    /// Stargate's advertised gRPC address.
    pub fn spawn(
        inner: Box<dyn Discovery>,
        client: GdsClient,
        clusterset: &str,
        own_cluster: &str,
        advertise_addr: SocketAddr,
    ) -> Self {
        let cache = RouterCache::spawn(
            client,
            RouterCacheConfig::new(clusterset),
            CancellationToken::new(),
        );
        let mut discovery =
            Self::from_routers(inner, cache.subscribe(), own_cluster, advertise_addr);
        discovery._cache = Some(cache);
        discovery
    }

    /// Wraps `inner` with the routers published on `routers`, for hosts that
    /// run their own [`RouterCache`] and for tests.
    pub fn from_routers(
        inner: Box<dyn Discovery>,
        routers: watch::Receiver<Arc<RouterState>>,
        own_cluster: impl Into<String>,
        advertise_addr: SocketAddr,
    ) -> Self {
        Self {
            inner,
            routers,
            own_cluster: own_cluster.into(),
            advertise_addr,
            published: Mutex::new(Published::default()),
            _cache: None,
        }
    }

    /// The local Stargates, under their replica identities when the own
    /// router record is in direct mode and names this Stargate (module
    /// documentation), from the latest router view. A stale view keeps its
    /// last known routers.
    fn direct_peers(&self, local: Vec<StargateInfo>) -> Vec<StargateInfo> {
        let self_stargates = self.inner.initial_stargates();
        let peers = {
            let state = self.routers.borrow();
            direct_peers(
                &state,
                &self.own_cluster,
                self.advertise_addr,
                &self_stargates,
                local,
            )
        };
        self.log_peer_names(peers.names, peers.stargates.len());
        peers.stargates
    }

    fn log_peer_names(&self, names: PeerNames, stargate_count: usize) {
        let mut published = self.published.lock();
        if published.peer_names == names {
            return;
        }
        match &names {
            PeerNames::Identities(router) => info!(
                own_cluster = %self.own_cluster,
                router,
                stargate_count,
                "listing this cluster's Stargates under their GDS replica identities"
            ),
            PeerNames::TemplateMismatch(router) => warn!(
                own_cluster = %self.own_cluster,
                router,
                advertise_addr = %self.advertise_addr,
                "the GDS router of this cluster does not name this Stargate by the host it \
                 advertises (hostname template, Stargate id or gRPC port differ); listing \
                 peers as local discovery finds them"
            ),
            PeerNames::Local if matches!(published.peer_names, PeerNames::Identities(_)) => info!(
                own_cluster = %self.own_cluster,
                "no GDS router of this cluster in direct mode lists this Stargate; \
                 listing peers as local discovery finds them"
            ),
            PeerNames::Local => {}
        }
        published.peer_names = names;
    }

    /// The dial URLs of this cluster's Stargates in direct mode, from the
    /// latest router view. A stale view keeps its last known routers.
    fn direct_peer_dial_addrs(&self) -> HashMap<String, String> {
        let self_hosts: Vec<String> = self
            .inner
            .initial_stargates()
            .iter()
            .filter_map(|stargate| advertise_host(&stargate.advertise_addr))
            .collect();
        let dial = {
            let state = self.routers.borrow();
            direct_dial_addrs(&state, &self.own_cluster, self.advertise_addr, &self_hosts)
        };
        self.log_direct_router(dial.router.as_deref(), dial.addrs.len());
        dial.addrs
    }

    fn log_direct_router(&self, router: Option<&str>, dial_addr_count: usize) {
        let mut published = self.published.lock();
        if published.direct_router.as_deref() == router {
            return;
        }
        match router {
            Some(router) => info!(
                own_cluster = %self.own_cluster,
                router,
                dial_addr_count,
                "GDS router of this cluster is in direct mode; filling peer dial addresses"
            ),
            None => info!(
                own_cluster = %self.own_cluster,
                advertise_addr = %self.advertise_addr,
                "no GDS router of this cluster in direct mode lists this Stargate; \
                 peer dial addresses are not filled"
            ),
        }
        published.direct_router = router.map(str::to_string);
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
        let local = self.inner.discover_stargates().await;
        self.direct_peers(local)
    }

    async fn discover_remote_watch_urls(&self) -> Vec<String> {
        let mut urls = self.gds_remote_watch_urls();
        urls.extend(self.inner.discover_remote_watch_urls().await);
        urls.into_iter().collect()
    }

    async fn peer_dial_addrs(&self) -> HashMap<String, String> {
        let mut addrs = self.inner.peer_dial_addrs().await;
        addrs.extend(self.direct_peer_dial_addrs());
        addrs
    }
}

/// The direct-mode router record of `own_cluster` that lists this
/// Stargate's IP among its replicas, ready or not, with the scheme and port
/// of its dial URLs.
struct OwnDirectRouter<'a> {
    router: &'a Router,
    scheme: &'static str,
    port: u16,
}

impl<'a> OwnDirectRouter<'a> {
    /// `None` without such a record, or when its dial scheme is unknown or
    /// its gRPC port unset.
    fn find(state: &'a RouterState, own_cluster: &str, own_ip: IpAddr) -> Option<Self> {
        let router = state.iter().find(|router| {
            router.cluster == own_cluster
                && is_direct(router)
                && router
                    .replicas
                    .iter()
                    .any(|replica| replica_ip(replica) == Some(own_ip))
        })?;
        Some(Self {
            router,
            scheme: dial_scheme(router)?,
            port: grpc_port(router)?,
        })
    }

    fn key(&self) -> String {
        router_key(self.router)
    }

    /// `<scheme>://<replica address>:<grpc port>`, IPv6 in brackets.
    fn dial_url(&self, replica: &RouterReplica) -> Option<String> {
        url_host(&replica.address).map(|host| format!("{}://{host}:{}", self.scheme, self.port))
    }

    /// The hosts local discovery may list `replica` by: the hostname
    /// template with `{pod_name}` replaced by its identity, its EndpointSlice
    /// hostname, or its dashed-IP DNS name. Empty when the template does not
    /// contain `{pod_name}` exactly once.
    fn replica_names(&self, replica: &RouterReplica) -> Vec<String> {
        let mut names: Vec<String> = [
            Some(replica.identity.clone()),
            Some(replica.hostname.clone()),
            dns_endpoint_name(&replica.address),
        ]
        .into_iter()
        .flatten()
        .filter_map(|pod_name| self.host_for(&pod_name))
        .collect();
        names.dedup();
        names
    }

    /// The host of the hostname template with `{pod_name}` replaced by
    /// `pod_name`, as [`advertise_host`] spells it.
    fn host_for(&self, pod_name: &str) -> Option<String> {
        labels::replica_authority(
            &self.router.hostname_template,
            pod_name,
            self.router.grpc_port,
        )
        .and_then(|authority| advertise_host(&authority))
    }

    /// Whether the record names `stargate` by its advertised authority: the
    /// hostname template with `{pod_name}` replaced by its id, on the
    /// record's gRPC port.
    fn names(&self, stargate: &StargateInfo) -> bool {
        labels::replica_authority(
            &self.router.hostname_template,
            &stargate.stargate_id,
            self.router.grpc_port,
        )
        .and_then(|authority| authority_key(&authority))
        .is_some_and(|key| Some(key) == authority_key(&stargate.advertise_addr))
    }

    /// The `WatchStargates` entry of `replica` under its identity, `None`
    /// without an identity or a usable address.
    fn identity_stargate(&self, replica: &RouterReplica) -> Option<StargateInfo> {
        Some(StargateInfo {
            stargate_id: replica.identity.trim().to_string(),
            advertise_addr: replica_authority(self.router, replica)?,
            http_advertise_addr: String::new(),
            grpc_pylon_dial_addr: self.dial_url(replica)?,
        })
    }
}

/// The local Stargates as [`direct_peers`] lists them.
#[derive(Debug, PartialEq, Eq)]
struct DirectPeers {
    names: PeerNames,
    stargates: Vec<StargateInfo>,
}

/// `local`, the Stargates local discovery lists, rewritten under the
/// replica identities of the own direct-mode router record as the module
/// documentation describes. `self_stargates` are this Stargate's own
/// entries ([`Discovery::initial_stargates`]). Without such a record, or
/// when it does not name this Stargate by the host it advertises, `local`
/// is returned unchanged.
fn direct_peers(
    state: &RouterState,
    own_cluster: &str,
    advertise_addr: SocketAddr,
    self_stargates: &[StargateInfo],
    local: Vec<StargateInfo>,
) -> DirectPeers {
    let own_ip = advertise_addr.ip();
    let Some(own) = OwnDirectRouter::find(state, own_cluster, own_ip) else {
        return DirectPeers {
            names: PeerNames::Local,
            stargates: local,
        };
    };
    if self_stargates.is_empty() || !self_stargates.iter().all(|stargate| own.names(stargate)) {
        return DirectPeers {
            names: PeerNames::TemplateMismatch(own.key()),
            stargates: local,
        };
    }
    let self_hosts: HashSet<String> = self_stargates
        .iter()
        .filter_map(|stargate| advertise_host(&stargate.advertise_addr))
        .collect();

    // A replica at this Stargate's IP, or one named like it (a pod that kept
    // its name and moved, still listed at its old address), is this
    // Stargate: its other names are dropped and it is never added.
    let mut own_names: HashSet<String> = HashSet::new();
    let mut peers_by_name: HashMap<String, &RouterReplica> = HashMap::new();
    let mut peers: Vec<&RouterReplica> = Vec::new();
    for replica in &own.router.replicas {
        let names = own.replica_names(replica);
        if replica_ip(replica) == Some(own_ip) || names.iter().any(|name| self_hosts.contains(name))
        {
            own_names.extend(names);
        } else if !replica.identity.trim().is_empty() {
            for name in names {
                peers_by_name.entry(name).or_insert(replica);
            }
            peers.push(replica);
        }
    }

    let mut listed: HashSet<String> = HashSet::new();
    let mut stargates = Vec::with_capacity(local.len());
    for stargate in local {
        let Some(host) = advertise_host(&stargate.advertise_addr) else {
            stargates.push(stargate);
            continue;
        };
        if self_hosts.contains(&host) {
            stargates.push(stargate);
        } else if own_names.contains(&host) {
            // This Stargate under another name, for example its dashed IP.
        } else if let Some(replica) = peers_by_name.get(&host) {
            if listed.insert(replica.identity.trim().to_string()) {
                stargates.extend(own.identity_stargate(replica));
            }
        } else {
            stargates.push(stargate);
        }
    }
    for replica in peers.into_iter().filter(|replica| replica.ready) {
        if listed.insert(replica.identity.trim().to_string()) {
            stargates.extend(own.identity_stargate(replica));
        }
    }
    stargates.sort_by(|left, right| {
        left.stargate_id
            .cmp(&right.stargate_id)
            .then_with(|| left.advertise_addr.cmp(&right.advertise_addr))
    });
    DirectPeers {
        names: PeerNames::Identities(own.key()),
        stargates,
    }
}

/// The peer dial URLs of the own direct-mode router record.
#[derive(Debug, Default, PartialEq, Eq)]
struct DirectDialAddrs {
    /// Key of the router record, `None` without one.
    router: Option<String>,
    /// Dial URL by advertise host.
    addrs: HashMap<String, String>,
}

/// The dial URLs of the Stargates of `own_cluster` when its router record
/// that lists `advertise_addr`'s IP among its replicas is in direct mode,
/// keyed as the module documentation describes. `self_hosts` are the hosts
/// this Stargate advertises for itself; they dial `advertise_addr`. All
/// replicas count, ready or not: local discovery decides which Stargates
/// are listed, including warming ones, and this only supplies addresses.
fn direct_dial_addrs(
    state: &RouterState,
    own_cluster: &str,
    advertise_addr: SocketAddr,
    self_hosts: &[String],
) -> DirectDialAddrs {
    let own_ip = advertise_addr.ip();
    let Some(own) = OwnDirectRouter::find(state, own_cluster, own_ip) else {
        return DirectDialAddrs::default();
    };
    let mut addrs = HashMap::new();
    for replica in &own.router.replicas {
        let Some(url) = own.dial_url(replica) else {
            continue;
        };
        let hosts = advertise_host(&replica.address)
            .into_iter()
            .chain(own.replica_names(replica));
        for host in hosts {
            addrs.insert(host, url.clone());
        }
    }
    if let Some(self_url_host) = url_host(&own_ip.to_string()) {
        let self_url = format!("{}://{self_url_host}:{}", own.scheme, advertise_addr.port());
        for host in self_hosts {
            addrs.insert(host.clone(), self_url.clone());
        }
    }
    DirectDialAddrs {
        router: Some(own.key()),
        addrs,
    }
}

/// `host:port` of an authority, the host as [`advertise_host`] spells it.
/// `None` without a host or a numeric port.
fn authority_key(authority: &str) -> Option<String> {
    let host = advertise_host(authority)?;
    let (_, port) = authority.trim().trim_end_matches('/').rsplit_once(':')?;
    let port: u16 = port.parse().ok()?;
    Some(format!("{host}:{port}"))
}

/// The DNS label Kubernetes gives an endpoint without a hostname in the SRV
/// records of a headless Service: its IP address with `.` and `:` replaced
/// by `-`.
fn dns_endpoint_name(address: &str) -> Option<String> {
    let address = address.trim();
    address
        .parse::<IpAddr>()
        .is_ok()
        .then(|| address.replace(['.', ':'], "-"))
}

fn replica_ip(replica: &RouterReplica) -> Option<IpAddr> {
    replica.address.trim().parse().ok()
}

/// The scheme of `router`'s seed URLs, as `gds-client` derives it: the dial
/// scheme, [`DEFAULT_DIAL_SCHEME`] when unset, `None` when unknown.
fn dial_scheme(router: &Router) -> Option<&'static str> {
    match router.dial_scheme.trim().to_ascii_lowercase().as_str() {
        "" => Some(DEFAULT_DIAL_SCHEME),
        DIAL_SCHEME_HTTPS => Some(DIAL_SCHEME_HTTPS),
        DIAL_SCHEME_HTTP => Some(DIAL_SCHEME_HTTP),
        _ => None,
    }
}

fn grpc_port(router: &Router) -> Option<u16> {
    u16::try_from(router.grpc_port)
        .ok()
        .filter(|port| *port != 0)
}

/// The host of a URL for a replica address, as in the seed URLs: an IPv4
/// address or a host name as is, an IPv6 address in brackets. `None` for an
/// empty address or another address with a colon.
fn url_host(address: &str) -> Option<String> {
    let address = address.trim();
    match address.parse::<IpAddr>() {
        Ok(IpAddr::V4(_)) => Some(address.to_string()),
        Ok(IpAddr::V6(_)) => Some(format!("[{address}]")),
        Err(_) if address.is_empty() || address.contains(':') => None,
        Err(_) => Some(address.to_string()),
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use gds_client::v1::{LeaseState, RouterMode};

    use hickory_resolver::TokioAsyncResolver;
    use hickory_resolver::config::{ResolverConfig, ResolverOpts};

    use super::*;
    use crate::discovery::{HeadlessDnsDiscovery, HeadlessDnsDiscoveryConfig, SelfOnlyDiscovery};

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

    fn self_addr() -> SocketAddr {
        "10.0.0.1:50071".parse().unwrap()
    }

    fn self_only() -> Box<dyn Discovery> {
        Box::new(SelfOnlyDiscovery::new(
            self_addr(),
            "stargate-0".to_string(),
            8000,
        ))
    }

    /// The advertised hostname template Stargate runs with.
    const TEMPLATE: &str = "{pod_name}.stargate-headless.{namespace}.svc.cluster.local";
    /// [`TEMPLATE`] as the chart exports it, `{namespace}` resolved.
    const EXPORTED_TEMPLATE: &str = "{pod_name}.stargate-headless.nvcf.svc.cluster.local";

    fn host(pod_name: &str) -> String {
        EXPORTED_TEMPLATE.replace("{pod_name}", pod_name)
    }

    /// A router record of Stargate pods in `mode`; each replica is
    /// `(address, identity, ready)`.
    fn stargate_router(cluster: &str, mode: RouterMode, replicas: &[(&str, &str, bool)]) -> Router {
        let mut router = router(cluster, "http", &[]);
        router.name = "llm-request-router".to_string();
        router.router_mode = mode as i32;
        router.hostname_template = EXPORTED_TEMPLATE.to_string();
        router.replicas = replicas
            .iter()
            .map(|(address, identity, ready)| RouterReplica {
                address: address.to_string(),
                identity: identity.to_string(),
                ready: *ready,
                ..RouterReplica::default()
            })
            .collect();
        router
    }

    fn direct_router(replicas: &[(&str, &str, bool)]) -> Router {
        stargate_router("a", RouterMode::Direct, replicas)
    }

    fn addrs(entries: &[(&str, &str)]) -> HashMap<String, String> {
        entries
            .iter()
            .map(|(host, url)| (host.to_string(), url.to_string()))
            .collect()
    }

    fn self_hosts() -> Vec<String> {
        vec![host("stargate-0")]
    }

    #[test]
    fn direct_router_dial_addrs_cover_addresses_dns_endpoint_names_and_identities() {
        let mut router = direct_router(&[
            ("10.0.0.1", "", true),
            ("10.0.0.2", "", true),
            // Warming peers are listed by headless discovery too.
            ("10.0.0.3", "stargate-3", false),
        ]);
        router.replicas[1].hostname = "stargate-2".to_string();
        let state = state(vec![router]);

        let dial = direct_dial_addrs(&state, "a", self_addr(), &self_hosts());

        assert_eq!(dial.router.as_deref(), Some("a/nvcf/llm-request-router"));
        assert_eq!(
            dial.addrs,
            addrs(&[
                ("10.0.0.1", "http://10.0.0.1:50071"),
                (&host("10-0-0-1"), "http://10.0.0.1:50071"),
                ("10.0.0.2", "http://10.0.0.2:50071"),
                (&host("10-0-0-2"), "http://10.0.0.2:50071"),
                // The EndpointSlice hostname names a replica too.
                (&host("stargate-2"), "http://10.0.0.2:50071"),
                ("10.0.0.3", "http://10.0.0.3:50071"),
                (&host("10-0-0-3"), "http://10.0.0.3:50071"),
                (&host("stargate-3"), "http://10.0.0.3:50071"),
                // This Stargate dials its own advertise address.
                (&host("stargate-0"), "http://10.0.0.1:50071"),
            ])
        );
    }

    #[test]
    fn backend_router_mode_fills_no_dial_addrs_and_keeps_local_peers() {
        let local = vec![
            StargateInfo {
                stargate_id: "10-0-0-2".to_string(),
                advertise_addr: format!("{}:50071", host("10-0-0-2")),
                ..StargateInfo::default()
            },
            self_stargate(),
        ];
        for mode in [RouterMode::BackendRouter, RouterMode::Unspecified] {
            let state = state(vec![stargate_router(
                "a",
                mode,
                &[
                    ("10.0.0.1", "stargate-0", true),
                    ("10.0.0.2", "stargate-1", true),
                ],
            )]);

            assert_eq!(
                direct_dial_addrs(&state, "a", self_addr(), &self_hosts()),
                DirectDialAddrs::default(),
                "mode {mode:?}"
            );
            assert_eq!(
                direct_peers(&state, "a", self_addr(), &[self_stargate()], local.clone()),
                DirectPeers {
                    names: PeerNames::Local,
                    stargates: local.clone()
                },
                "mode {mode:?}"
            );
        }
    }

    #[test]
    fn own_router_is_the_direct_router_of_this_cluster_that_lists_this_stargate() {
        // Another cluster's router with this pod IP, and a router of this
        // cluster without it: neither is this Stargate's router.
        let other_cluster = stargate_router("b", RouterMode::Direct, &[("10.0.0.1", "", true)]);
        let mut other_router = direct_router(&[("10.0.0.9", "", true)]);
        other_router.name = "another-router".to_string();
        let mut backend_router =
            stargate_router("a", RouterMode::BackendRouter, &[("10.0.0.1", "", true)]);
        backend_router.name = "a-backend-router".to_string();
        assert_eq!(
            direct_dial_addrs(
                &state(vec![
                    other_cluster.clone(),
                    other_router.clone(),
                    backend_router.clone()
                ]),
                "a",
                self_addr(),
                &self_hosts(),
            ),
            DirectDialAddrs::default()
        );

        // A not-ready replica of this Stargate still identifies its router.
        let own = direct_router(&[("10.0.0.1", "", false)]);
        let dial = direct_dial_addrs(
            &state(vec![other_cluster, other_router, backend_router, own]),
            "a",
            self_addr(),
            &self_hosts(),
        );
        assert_eq!(dial.router.as_deref(), Some("a/nvcf/llm-request-router"));
        assert_eq!(
            dial.addrs.get(&host("stargate-0")),
            Some(&"http://10.0.0.1:50071".to_string())
        );
        assert!(!dial.addrs.contains_key("10.0.0.9"));
    }

    #[test]
    fn dial_addrs_use_the_dial_scheme_grpc_port_and_bracketed_ipv6() {
        let mut router = direct_router(&[("fd00::1", "", true), ("fd00::2", "", true)]);
        router.dial_scheme = String::new();
        router.grpc_port = 50081;
        let advertise_addr: SocketAddr = "[fd00::1]:50071".parse().unwrap();

        let dial = direct_dial_addrs(&state(vec![router.clone()]), "a", advertise_addr, &[]);

        assert_eq!(
            dial.addrs,
            addrs(&[
                ("fd00::1", "https://[fd00::1]:50081"),
                (&host("fd00--1"), "https://[fd00::1]:50081"),
                ("fd00::2", "https://[fd00::2]:50081"),
                (&host("fd00--2"), "https://[fd00::2]:50081"),
            ])
        );

        // The self entry dials the advertised port with the router's scheme.
        router.dial_scheme = "HTTP".to_string();
        let dial = direct_dial_addrs(
            &state(vec![router.clone()]),
            "a",
            advertise_addr,
            &["stargate-0.example".to_string()],
        );
        assert_eq!(
            dial.addrs.get("stargate-0.example"),
            Some(&"http://[fd00::1]:50071".to_string())
        );
        assert_eq!(
            dial.addrs.get("fd00::2"),
            Some(&"http://[fd00::2]:50081".to_string())
        );

        for (scheme, port) in [("grpc", 50071), ("http", 0)] {
            router.dial_scheme = scheme.to_string();
            router.grpc_port = port;
            assert_eq!(
                direct_dial_addrs(&state(vec![router.clone()]), "a", advertise_addr, &[]),
                DirectDialAddrs::default(),
                "scheme {scheme:?} port {port}"
            );
        }
    }

    #[test]
    fn hostname_keys_need_a_template_with_one_pod_name() {
        for template in [
            "",
            "stargate.nvcf.svc.cluster.local",
            "{pod_name}.{pod_name}.example",
        ] {
            let mut router = direct_router(&[("10.0.0.2", "stargate-2", true)]);
            router.replicas.push(RouterReplica {
                address: "10.0.0.1".to_string(),
                ..RouterReplica::default()
            });
            router.hostname_template = template.to_string();

            assert_eq!(
                direct_dial_addrs(&state(vec![router]), "a", self_addr(), &[]).addrs,
                addrs(&[
                    ("10.0.0.1", "http://10.0.0.1:50071"),
                    ("10.0.0.2", "http://10.0.0.2:50071"),
                ]),
                "template {template:?}"
            );
        }
    }

    fn headless_discovery(self_pod_name: &str) -> HeadlessDnsDiscovery {
        HeadlessDnsDiscovery::new(HeadlessDnsDiscoveryConfig {
            self_pod_name: self_pod_name.to_string(),
            pod_namespace: "nvcf".to_string(),
            advertised_hostname_template: TEMPLATE.to_string(),
            discovery_dns_name: "stargate-headless.nvcf.svc.cluster.local".to_string(),
            resolver: TokioAsyncResolver::tokio(ResolverConfig::default(), ResolverOpts::default()),
            grpc_port: 50071,
        })
    }

    /// The entry headless discovery lists `stargate-0` under for itself.
    fn self_stargate() -> StargateInfo {
        headless_discovery("stargate-0")
            .initial_stargates()
            .remove(0)
    }

    /// Local discovery with a fixed peer list: what headless DNS discovery
    /// lists for the given SRV endpoint names, this pod included.
    struct ListedDiscovery {
        headless: HeadlessDnsDiscovery,
        listed: Vec<StargateInfo>,
    }

    impl ListedDiscovery {
        fn new(self_pod_name: &str, srv_names: &[&str]) -> Self {
            let headless = headless_discovery(self_pod_name);
            let listed = headless.stargates_from_srv_records(srv_names.iter().map(|name| {
                (
                    format!("{name}.stargate-headless.nvcf.svc.cluster.local."),
                    50071,
                )
            }));
            Self { headless, listed }
        }
    }

    #[async_trait::async_trait]
    impl Discovery for ListedDiscovery {
        fn initial_stargates(&self) -> Vec<StargateInfo> {
            self.headless.initial_stargates()
        }

        async fn discover_stargates(&self) -> Vec<StargateInfo> {
            self.listed.clone()
        }
    }

    /// What `WatchStargates` publishes from `discovery` without
    /// `--grpc-pylon-dial-addr`: each entry as `(stargate_id, advertise_addr,
    /// grpc_pylon_dial_addr)`, the dial address from
    /// [`Discovery::peer_dial_addrs`] when discovery leaves it empty.
    async fn published(discovery: &dyn Discovery) -> Vec<(String, String, String)> {
        let dial_addrs = discovery.peer_dial_addrs().await;
        discovery
            .discover_stargates()
            .await
            .into_iter()
            .map(|stargate| {
                let dial = if stargate.grpc_pylon_dial_addr.is_empty() {
                    advertise_host(&stargate.advertise_addr)
                        .and_then(|host| dial_addrs.get(&host).cloned())
                        .unwrap_or_default()
                } else {
                    stargate.grpc_pylon_dial_addr
                };
                (stargate.stargate_id, stargate.advertise_addr, dial)
            })
            .collect()
    }

    fn entry(id: &str, pod_name: &str, dial: &str) -> (String, String, String) {
        (
            id.to_string(),
            format!("{}:50071", host(pod_name)),
            dial.to_string(),
        )
    }

    /// A Deployment pod: its name, its pod IP, and what headless DNS lists.
    const POD_0: (&str, &str) = ("stargate-0", "10.0.0.1");
    const POD_1: (&str, &str) = ("stargate-1", "10.0.0.2");
    const SRV_NAMES: [&str; 2] = ["10-0-0-1", "10-0-0-2"];

    fn gds_discovery_of(
        (pod_name, pod_ip): (&str, &str),
        srv_names: &[&str],
        routers: watch::Receiver<Arc<RouterState>>,
    ) -> GdsDiscovery {
        GdsDiscovery::from_routers(
            Box::new(ListedDiscovery::new(pod_name, srv_names)),
            routers,
            "a",
            format!("{pod_ip}:50071").parse().unwrap(),
        )
    }

    #[tokio::test]
    async fn deployment_pods_are_listed_once_under_their_identities_by_every_replica() {
        // Kubernetes DNS names Deployment endpoints by dashed IP in the SRV
        // records of the headless Service, the listing pod included, while
        // each pod lists itself by its pod name. Without identities a pylon
        // watching both pods saw each one under two authorities.
        let (_tx, rx) = watch::channel(state(vec![direct_router(&[
            ("10.0.0.1", "stargate-0", true),
            ("10.0.0.2", "stargate-1", true),
        ])]));
        let pod_0 = gds_discovery_of(POD_0, &SRV_NAMES, rx.clone());
        let pod_1 = gds_discovery_of(POD_1, &SRV_NAMES, rx);

        let expected = vec![
            entry("stargate-0", "stargate-0", "http://10.0.0.1:50071"),
            entry("stargate-1", "stargate-1", "http://10.0.0.2:50071"),
        ];
        assert_eq!(published(&pod_0).await, expected);
        assert_eq!(published(&pod_1).await, expected);

        // A pylon opens one registration per (authority, dial address).
        let endpoints: BTreeSet<(String, String)> = published(&pod_0)
            .await
            .into_iter()
            .chain(published(&pod_1).await)
            .map(|(_, authority, dial)| (authority, dial))
            .collect();
        assert_eq!(endpoints.len(), 2, "{endpoints:?}");
        assert_eq!(
            pod_0.published.lock().peer_names,
            PeerNames::Identities("a/nvcf/llm-request-router".to_string())
        );
    }

    #[tokio::test]
    async fn ready_replicas_missing_from_dns_are_added_and_warming_ones_only_when_listed() {
        let (_tx, rx) = watch::channel(state(vec![direct_router(&[
            ("10.0.0.1", "stargate-0", true),
            // Listed by DNS while warming: renamed, not dropped.
            ("10.0.0.2", "stargate-1", false),
            // Not in DNS yet: added only when ready.
            ("10.0.0.3", "stargate-2", true),
            ("10.0.0.4", "stargate-3", false),
        ])]));
        let discovery = gds_discovery_of(POD_0, &["10-0-0-2", "10-0-0-9"], rx);

        assert_eq!(
            published(&discovery).await,
            vec![
                // A peer GDS does not know is kept as DNS lists it.
                (
                    "10-0-0-9".to_string(),
                    format!("{}:50071", host("10-0-0-9")),
                    String::new()
                ),
                entry("stargate-0", "stargate-0", "http://10.0.0.1:50071"),
                entry("stargate-1", "stargate-1", "http://10.0.0.2:50071"),
                entry("stargate-2", "stargate-2", "http://10.0.0.3:50071"),
            ]
        );
    }

    #[tokio::test]
    async fn replicas_without_identity_keep_their_dns_names_and_dial_addrs() {
        // The peer has no identity: it keeps its dashed-IP name and gets its
        // dial address by that name, as before identities existed. This
        // Stargate's dashed-IP name is still dropped next to its own entry.
        let (_tx, rx) = watch::channel(state(vec![direct_router(&[
            ("10.0.0.1", "stargate-0", true),
            ("10.0.0.2", "", true),
        ])]));
        let discovery = gds_discovery_of(POD_0, &SRV_NAMES, rx);

        assert_eq!(
            published(&discovery).await,
            vec![
                entry("10-0-0-2", "10-0-0-2", "http://10.0.0.2:50071"),
                entry("stargate-0", "stargate-0", "http://10.0.0.1:50071"),
            ]
        );
    }

    #[tokio::test]
    async fn a_router_that_names_this_stargate_differently_keeps_the_dns_names() {
        let listed = ListedDiscovery::new(POD_0.0, &SRV_NAMES).listed;
        let identities = [
            ("10.0.0.1", "stargate-0", true),
            ("10.0.0.2", "stargate-1", true),
        ];
        let mut other_template = direct_router(&identities);
        other_template.hostname_template = "{pod_name}.stargate.example.com".to_string();
        let mut other_port = direct_router(&identities);
        other_port.grpc_port = 443;
        let mut no_template = direct_router(&identities);
        no_template.hostname_template.clear();

        for router in [other_template, other_port, no_template] {
            let state = state(vec![router.clone()]);
            assert_eq!(
                direct_peers(&state, "a", self_addr(), &[self_stargate()], listed.clone()),
                DirectPeers {
                    names: PeerNames::TemplateMismatch("a/nvcf/llm-request-router".to_string()),
                    stargates: listed.clone(),
                },
                "{router:?}"
            );
        }

        // A Stargate advertised by its IP address (DNS A-record discovery)
        // is not named by the template either.
        let state = state(vec![direct_router(&identities)]);
        let self_by_ip = self_only().initial_stargates();
        assert_eq!(
            direct_peers(&state, "a", self_addr(), &self_by_ip, self_by_ip.clone()).names,
            PeerNames::TemplateMismatch("a/nvcf/llm-request-router".to_string())
        );
    }

    #[test]
    fn a_replica_named_like_this_stargate_at_another_address_is_not_added() {
        // A pod that kept its name and moved: GDS may still list its old
        // address next to the new one.
        let state = state(vec![direct_router(&[
            ("10.0.0.7", "stargate-0", true),
            ("10.0.0.1", "stargate-0", true),
            ("10.0.0.2", "stargate-1", true),
        ])]);
        let local = vec![
            StargateInfo {
                stargate_id: "10-0-0-7".to_string(),
                advertise_addr: format!("{}:50071", host("10-0-0-7")),
                ..StargateInfo::default()
            },
            self_stargate(),
        ];

        let peers = direct_peers(&state, "a", self_addr(), &[self_stargate()], local);

        assert_eq!(
            peers
                .stargates
                .iter()
                .map(|stargate| (
                    stargate.stargate_id.as_str(),
                    stargate.grpc_pylon_dial_addr.as_str()
                ))
                .collect::<BTreeMap<_, _>>(),
            BTreeMap::from([("stargate-0", ""), ("stargate-1", "http://10.0.0.2:50071")])
        );
    }

    #[tokio::test]
    async fn peers_follow_router_changes_through_the_trait() {
        let (tx, rx) = watch::channel(Arc::new(RouterState::default()));
        let discovery: Box<dyn Discovery> = Box::new(gds_discovery_of(POD_0, &SRV_NAMES, rx));
        let dns_names = vec![
            entry("10-0-0-1", "10-0-0-1", ""),
            entry("10-0-0-2", "10-0-0-2", ""),
            entry("stargate-0", "stargate-0", ""),
        ];
        assert_eq!(published(discovery.as_ref()).await, dns_names);

        tx.send(state(vec![direct_router(&[
            ("10.0.0.1", "stargate-0", true),
            ("10.0.0.2", "stargate-1", true),
        ])]))
        .unwrap();
        assert_eq!(
            published(discovery.as_ref()).await,
            vec![
                entry("stargate-0", "stargate-0", "http://10.0.0.1:50071"),
                entry("stargate-1", "stargate-1", "http://10.0.0.2:50071"),
            ]
        );

        tx.send(state(vec![stargate_router(
            "a",
            RouterMode::BackendRouter,
            &[
                ("10.0.0.1", "stargate-0", true),
                ("10.0.0.2", "stargate-1", true),
            ],
        )]))
        .unwrap();
        assert_eq!(published(discovery.as_ref()).await, dns_names);
    }

    #[tokio::test]
    async fn peer_dial_addrs_follow_router_changes_through_the_trait() {
        let inner = headless_discovery("stargate-0");
        let (tx, rx) = watch::channel(Arc::new(RouterState::default()));
        let discovery: Box<dyn Discovery> = Box::new(GdsDiscovery::from_routers(
            Box::new(inner),
            rx,
            "a",
            self_addr(),
        ));
        assert!(discovery.peer_dial_addrs().await.is_empty());

        tx.send(state(vec![direct_router(&[
            ("10.0.0.1", "", true),
            ("10.0.0.2", "", true),
        ])]))
        .unwrap();
        let dial_addrs = discovery.peer_dial_addrs().await;
        assert_eq!(
            dial_addrs.get(&host("stargate-0")),
            Some(&"http://10.0.0.1:50071".to_string())
        );
        assert_eq!(
            dial_addrs.get(&host("10-0-0-2")),
            Some(&"http://10.0.0.2:50071".to_string())
        );

        tx.send(state(vec![stargate_router(
            "a",
            RouterMode::BackendRouter,
            &[("10.0.0.1", "", true), ("10.0.0.2", "", true)],
        )]))
        .unwrap();
        assert!(discovery.peer_dial_addrs().await.is_empty());
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
        let discovery = GdsDiscovery::from_routers(self_only(), rx, "a", self_addr());

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
    async fn local_peers_stay_authoritative_without_a_direct_router() {
        let (_tx, rx) = watch::channel(state(vec![router("b", "http", &["10.2.0.1"])]));
        let inner = self_only();
        let expected = inner.initial_stargates();
        let discovery = GdsDiscovery::from_routers(inner, rx, "a", self_addr());

        assert_eq!(discovery.initial_stargates(), expected);
        assert_eq!(discovery.discover_stargates().await, expected);
    }

    #[tokio::test]
    async fn remote_watch_urls_follow_router_changes() {
        let (tx, rx) = watch::channel(Arc::new(RouterState::default()));
        let discovery = GdsDiscovery::from_routers(self_only(), rx, "a", self_addr());
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
        let discovery = GdsDiscovery::spawn(self_only(), client, "prod", "a", self_addr());

        assert!(discovery.discover_remote_watch_urls().await.is_empty());
        assert_eq!(
            discovery.discover_stargates().await,
            self_only().initial_stargates()
        );
    }
}

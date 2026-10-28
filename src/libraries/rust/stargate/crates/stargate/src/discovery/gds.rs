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
//!
//! In direct router mode the exported router Service selects the Stargate
//! pods themselves, and each pod advertises its pod IP. [`GdsDiscovery`]
//! then finds the router record of its own cluster, the one whose replicas
//! include this Stargate's address, and offers a dial URL for each local
//! Stargate through [`Discovery::peer_dial_addrs`]:
//! `<dial scheme>://<replica address>:<grpc port>`, keyed by the host the
//! local discovery advertises for it. A replica is matched by
//!
//! - its address, for peers advertised by IP address;
//! - the hostname template with `{pod_name}` replaced by the replica's
//!   EndpointSlice hostname, when set;
//! - the hostname template with `{pod_name}` replaced by the replica address
//!   with `.` and `:` turned into `-`, the name Kubernetes DNS gives an
//!   endpoint without a hostname in the SRV records of a headless Service,
//!   which is how headless DNS discovery lists Deployment pods.
//!
//! This Stargate itself is keyed by the host it advertises and dials its own
//! advertise address. A peer listed by its pod name with no EndpointSlice
//! hostname (StatefulSet pods behind the exported ClusterIP Service, or a
//! DNS server that publishes pod names) matches only once the router record
//! carries each replica's pod name.

use std::collections::{BTreeSet, HashMap};
use std::net::{IpAddr, SocketAddr};
use std::sync::Arc;

use gds_client::labels::{DIAL_SCHEME_HTTP, DIAL_SCHEME_HTTPS};
use gds_client::routers::{DEFAULT_DIAL_SCHEME, router_key};
use gds_client::v1::{Router, RouterReplica};
use gds_client::{GdsClient, RouterCache, RouterCacheConfig, RouterState, remote_watch_urls};
use parking_lot::Mutex;
use stargate_forwarding::render_hostname;
use stargate_proto::pb::StargateInfo;
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use super::{Discovery, advertise_host};

/// Export annotation that says what a router's replicas are:
/// `backend-router` (stargate-k8s-router pods) or `direct` (Stargate pods).
const ANNOTATION_ROUTER_MODE: &str = "gds.nvidia.com/router-mode";
const ROUTER_MODE_DIRECT: &str = "direct";
/// Export annotation with the advertised hostname template of a router's
/// Stargate pods, `{pod_name}` unresolved.
const ANNOTATION_HOSTNAME_TEMPLATE: &str = "gds.nvidia.com/hostname-template";
const POD_NAME_PLACEHOLDER: &str = "{pod_name}";

/// What [`GdsDiscovery`] last published, so it logs only changes.
#[derive(Debug, Default, PartialEq, Eq)]
struct Published {
    urls: BTreeSet<String>,
    stale: bool,
    /// Key of the own direct-mode router record peer dial URLs come from.
    direct_router: Option<String>,
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
        self.inner.discover_stargates().await
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
    let Some(router) = state.by_cluster(own_cluster).find(|router| {
        is_direct(router)
            && router
                .replicas
                .iter()
                .any(|replica| replica_ip(replica) == Some(own_ip))
    }) else {
        return DirectDialAddrs::default();
    };
    let (Some(scheme), Some(port)) = (dial_scheme(router), grpc_port(router)) else {
        return DirectDialAddrs::default();
    };
    let template = hostname_template(router);
    let mut addrs = HashMap::new();
    for replica in &router.replicas {
        let Some(url_host) = url_host(&replica.address) else {
            continue;
        };
        let url = format!("{scheme}://{url_host}:{port}");
        let mut hosts = vec![replica.address.clone()];
        if let Some(template) = template {
            let pod_names = replica_identity(replica)
                .map(str::to_string)
                .into_iter()
                .chain(dns_endpoint_name(&replica.address));
            hosts.extend(
                pod_names.map(|pod_name| render_hostname(template, &pod_name, &router.namespace)),
            );
        }
        for host in hosts.iter().filter_map(|host| advertise_host(host)) {
            addrs.insert(host, url.clone());
        }
    }
    if let Some(self_url_host) = url_host(&own_ip.to_string()) {
        let self_url = format!("{scheme}://{self_url_host}:{}", advertise_addr.port());
        for host in self_hosts {
            addrs.insert(host.clone(), self_url.clone());
        }
    }
    DirectDialAddrs {
        router: Some(router_key(router)),
        addrs,
    }
}

fn is_direct(router: &Router) -> bool {
    router
        .annotations
        .get(ANNOTATION_ROUTER_MODE)
        .is_some_and(|mode| mode.trim().eq_ignore_ascii_case(ROUTER_MODE_DIRECT))
}

/// The hostname template of `router`'s Stargate pods, when it names each pod
/// by exactly one `{pod_name}`.
fn hostname_template(router: &Router) -> Option<&str> {
    router
        .annotations
        .get(ANNOTATION_HOSTNAME_TEMPLATE)
        .map(|template| template.trim())
        .filter(|template| template.matches(POD_NAME_PLACEHOLDER).count() == 1)
}

/// The name a replica's pod goes by in its advertised hostname: the
/// EndpointSlice hostname when set. The router record does not carry the pod
/// name of other replicas yet.
fn replica_identity(replica: &RouterReplica) -> Option<&str> {
    Some(replica.hostname.trim()).filter(|hostname| !hostname.is_empty())
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

    use gds_client::v1::LeaseState;

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

    const TEMPLATE: &str = "{pod_name}.stargate-headless.{namespace}.svc.cluster.local";

    /// A router record of Stargate pods, annotated with `mode`.
    fn stargate_router(cluster: &str, mode: &str, replicas: &[(&str, &str, bool)]) -> Router {
        let mut router = router(cluster, "http", &[]);
        router.name = "llm-request-router".to_string();
        router.annotations = [
            (ANNOTATION_ROUTER_MODE.to_string(), mode.to_string()),
            (
                ANNOTATION_HOSTNAME_TEMPLATE.to_string(),
                TEMPLATE.to_string(),
            ),
        ]
        .into();
        router.replicas = replicas
            .iter()
            .map(|(address, hostname, ready)| RouterReplica {
                address: address.to_string(),
                hostname: hostname.to_string(),
                ready: *ready,
                ..RouterReplica::default()
            })
            .collect();
        router
    }

    fn addrs(entries: &[(&str, &str)]) -> HashMap<String, String> {
        entries
            .iter()
            .map(|(host, url)| (host.to_string(), url.to_string()))
            .collect()
    }

    fn self_hosts() -> Vec<String> {
        vec!["stargate-0.stargate-headless.nvcf.svc.cluster.local".to_string()]
    }

    #[test]
    fn direct_router_dial_addrs_cover_addresses_dns_endpoint_names_and_hostnames() {
        let state = state(vec![stargate_router(
            "a",
            "direct",
            &[
                ("10.0.0.1", "", true),
                ("10.0.0.2", "", true),
                // Warming peers are listed by headless discovery too.
                ("10.0.0.3", "stargate-3", false),
            ],
        )]);

        let dial = direct_dial_addrs(&state, "a", self_addr(), &self_hosts());

        assert_eq!(dial.router.as_deref(), Some("a/nvcf/llm-request-router"));
        assert_eq!(
            dial.addrs,
            addrs(&[
                ("10.0.0.1", "http://10.0.0.1:50071"),
                (
                    "10-0-0-1.stargate-headless.nvcf.svc.cluster.local",
                    "http://10.0.0.1:50071"
                ),
                ("10.0.0.2", "http://10.0.0.2:50071"),
                (
                    "10-0-0-2.stargate-headless.nvcf.svc.cluster.local",
                    "http://10.0.0.2:50071"
                ),
                ("10.0.0.3", "http://10.0.0.3:50071"),
                (
                    "10-0-0-3.stargate-headless.nvcf.svc.cluster.local",
                    "http://10.0.0.3:50071"
                ),
                (
                    "stargate-3.stargate-headless.nvcf.svc.cluster.local",
                    "http://10.0.0.3:50071"
                ),
                // This Stargate dials its own advertise address.
                (
                    "stargate-0.stargate-headless.nvcf.svc.cluster.local",
                    "http://10.0.0.1:50071"
                ),
            ])
        );
    }

    #[test]
    fn backend_router_mode_fills_no_dial_addrs() {
        for mode in ["backend-router", "", "relay"] {
            let state = state(vec![stargate_router(
                "a",
                mode,
                &[("10.0.0.1", "", true), ("10.0.0.2", "", true)],
            )]);

            assert_eq!(
                direct_dial_addrs(&state, "a", self_addr(), &self_hosts()),
                DirectDialAddrs::default(),
                "mode {mode:?}"
            );
        }
        let mut unannotated = stargate_router("a", "direct", &[("10.0.0.1", "", true)]);
        unannotated.annotations.clear();
        assert_eq!(
            direct_dial_addrs(&state(vec![unannotated]), "a", self_addr(), &self_hosts()),
            DirectDialAddrs::default()
        );
    }

    #[test]
    fn own_router_is_the_direct_router_of_this_cluster_that_lists_this_stargate() {
        // Another cluster's router with this pod IP, and a router of this
        // cluster without it: neither is this Stargate's router.
        let other_cluster = stargate_router("b", "direct", &[("10.0.0.1", "", true)]);
        let mut other_router = stargate_router("a", "direct", &[("10.0.0.9", "", true)]);
        other_router.name = "another-router".to_string();
        let mut backend_router = stargate_router("a", "backend-router", &[("10.0.0.1", "", true)]);
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
        let own = stargate_router("a", "direct", &[("10.0.0.1", "", false)]);
        let dial = direct_dial_addrs(
            &state(vec![other_cluster, other_router, backend_router, own]),
            "a",
            self_addr(),
            &self_hosts(),
        );
        assert_eq!(dial.router.as_deref(), Some("a/nvcf/llm-request-router"));
        assert_eq!(
            dial.addrs
                .get("stargate-0.stargate-headless.nvcf.svc.cluster.local"),
            Some(&"http://10.0.0.1:50071".to_string())
        );
        assert!(!dial.addrs.contains_key("10.0.0.9"));
    }

    #[test]
    fn dial_addrs_use_the_dial_scheme_grpc_port_and_bracketed_ipv6() {
        let mut router = stargate_router(
            "a",
            "direct",
            &[("fd00::1", "", true), ("fd00::2", "", true)],
        );
        router.dial_scheme = String::new();
        router.grpc_port = 50081;
        let advertise_addr: SocketAddr = "[fd00::1]:50071".parse().unwrap();

        let dial = direct_dial_addrs(&state(vec![router.clone()]), "a", advertise_addr, &[]);

        assert_eq!(
            dial.addrs,
            addrs(&[
                ("fd00::1", "https://[fd00::1]:50081"),
                (
                    "fd00--1.stargate-headless.nvcf.svc.cluster.local",
                    "https://[fd00::1]:50081"
                ),
                ("fd00::2", "https://[fd00::2]:50081"),
                (
                    "fd00--2.stargate-headless.nvcf.svc.cluster.local",
                    "https://[fd00::2]:50081"
                ),
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
            None,
            Some("stargate.nvcf.svc.cluster.local"),
            Some("{pod_name}.{pod_name}.example"),
        ] {
            let mut router = stargate_router("a", "direct", &[("10.0.0.2", "stargate-2", true)]);
            router.replicas.push(RouterReplica {
                address: "10.0.0.1".to_string(),
                ..RouterReplica::default()
            });
            match template {
                Some(template) => {
                    router.annotations.insert(
                        ANNOTATION_HOSTNAME_TEMPLATE.to_string(),
                        template.to_string(),
                    );
                }
                None => {
                    router.annotations.remove(ANNOTATION_HOSTNAME_TEMPLATE);
                }
            }

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

    fn headless_discovery() -> HeadlessDnsDiscovery {
        HeadlessDnsDiscovery::new(HeadlessDnsDiscoveryConfig {
            self_pod_name: "stargate-0".to_string(),
            pod_namespace: "nvcf".to_string(),
            advertised_hostname_template: TEMPLATE.to_string(),
            discovery_dns_name: "stargate-headless.nvcf.svc.cluster.local".to_string(),
            resolver: TokioAsyncResolver::tokio(ResolverConfig::default(), ResolverOpts::default()),
            grpc_port: 50071,
        })
    }

    #[test]
    fn dial_addrs_match_what_headless_discovery_lists_for_deployment_pods() {
        // Kubernetes DNS names Deployment endpoints by dashed IP in the SRV
        // records of the headless Service, this pod included.
        let local = headless_discovery().stargates_from_srv_records(vec![
            (
                "10-0-0-1.stargate-headless.nvcf.svc.cluster.local.".to_string(),
                50071,
            ),
            (
                "10-0-0-2.stargate-headless.nvcf.svc.cluster.local.".to_string(),
                50071,
            ),
        ]);
        let self_hosts: Vec<String> = headless_discovery()
            .initial_stargates()
            .iter()
            .filter_map(|stargate| advertise_host(&stargate.advertise_addr))
            .collect();
        let dial = direct_dial_addrs(
            &state(vec![stargate_router(
                "a",
                "direct",
                &[("10.0.0.1", "", true), ("10.0.0.2", "", true)],
            )]),
            "a",
            self_addr(),
            &self_hosts,
        );

        let matched: BTreeMap<&str, Option<&str>> = local
            .iter()
            .map(|stargate| {
                (
                    stargate.stargate_id.as_str(),
                    advertise_host(&stargate.advertise_addr)
                        .and_then(|host| dial.addrs.get(&host))
                        .map(String::as_str),
                )
            })
            .collect();
        assert_eq!(
            matched,
            BTreeMap::from([
                ("10-0-0-1", Some("http://10.0.0.1:50071")),
                ("10-0-0-2", Some("http://10.0.0.2:50071")),
                ("stargate-0", Some("http://10.0.0.1:50071")),
            ])
        );
    }

    #[tokio::test]
    async fn peer_dial_addrs_follow_router_changes_through_the_trait() {
        let inner = headless_discovery();
        let (tx, rx) = watch::channel(Arc::new(RouterState::default()));
        let discovery: Box<dyn Discovery> = Box::new(GdsDiscovery::from_routers(
            Box::new(inner),
            rx,
            "a",
            self_addr(),
        ));
        assert!(discovery.peer_dial_addrs().await.is_empty());

        tx.send(state(vec![stargate_router(
            "a",
            "direct",
            &[("10.0.0.1", "", true), ("10.0.0.2", "", true)],
        )]))
        .unwrap();
        let dial_addrs = discovery.peer_dial_addrs().await;
        assert_eq!(
            dial_addrs.get("stargate-0.stargate-headless.nvcf.svc.cluster.local"),
            Some(&"http://10.0.0.1:50071".to_string())
        );
        assert_eq!(
            dial_addrs.get("10-0-0-2.stargate-headless.nvcf.svc.cluster.local"),
            Some(&"http://10.0.0.2:50071".to_string())
        );

        tx.send(state(vec![stargate_router(
            "a",
            "backend-router",
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
    async fn local_peers_stay_authoritative() {
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

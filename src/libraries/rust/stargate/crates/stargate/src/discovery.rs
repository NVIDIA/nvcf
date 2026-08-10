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

use std::net::{IpAddr, SocketAddr};

use hickory_resolver::TokioAsyncResolver;
use stargate_forwarding::render_hostname;
use tracing::warn;

use stargate_proto::pb::StargateInfo;

#[async_trait::async_trait]
pub trait Discovery: Send + Sync + 'static {
    fn initial_stargates(&self) -> Vec<StargateInfo>;
    async fn discover_stargates(&self) -> Vec<StargateInfo>;
}

#[async_trait::async_trait]
impl Discovery for Box<dyn Discovery> {
    fn initial_stargates(&self) -> Vec<StargateInfo> {
        (**self).initial_stargates()
    }

    async fn discover_stargates(&self) -> Vec<StargateInfo> {
        (**self).discover_stargates().await
    }
}

pub struct HeadlessDnsDiscoveryConfig {
    pub self_pod_name: String,
    pub pod_namespace: String,
    pub advertised_hostname_template: String,
    pub discovery_dns_name: String,
    pub resolver: TokioAsyncResolver,
    pub grpc_port: u16,
}

pub struct HeadlessDnsDiscovery {
    config: HeadlessDnsDiscoveryConfig,
}

impl HeadlessDnsDiscovery {
    pub fn new(config: HeadlessDnsDiscoveryConfig) -> Self {
        Self { config }
    }

    fn stargate_info_for_endpoint(&self, endpoint_hostname: &str, grpc_port: u16) -> StargateInfo {
        let hostname = render_hostname(
            &self.config.advertised_hostname_template,
            endpoint_hostname,
            &self.config.pod_namespace,
        );
        StargateInfo {
            stargate_id: endpoint_hostname.to_string(),
            advertise_addr: format!("{hostname}:{grpc_port}"),
            // Kubernetes HTTP proxy traffic is load-balanced and local-only via
            // stargate-proxy. Per-pod HTTP addresses would either be unroutable
            // or imply peer forwarding that the proxy deliberately does not do.
            http_advertise_addr: String::new(),
            grpc_pylon_dial_addr: String::new(),
        }
    }

    fn stargates_from_srv_records<I>(&self, srv_records: I) -> Vec<StargateInfo>
    where
        I: IntoIterator<Item = (String, u16)>,
    {
        let mut stargates = Vec::new();
        for (target, port) in srv_records {
            let Some(endpoint_hostname) =
                endpoint_hostname_from_srv_target(&target, &self.config.discovery_dns_name)
            else {
                warn!(
                    dns_name = %self.config.discovery_dns_name,
                    srv_target = %target,
                    "ignoring SRV target outside headless service domain"
                );
                continue;
            };
            stargates.push(self.stargate_info_for_endpoint(endpoint_hostname, port));
        }
        let self_info =
            self.stargate_info_for_endpoint(&self.config.self_pod_name, self.config.grpc_port);
        finalize_stargates(stargates, self_info, |stargate| &stargate.stargate_id)
    }
}

#[async_trait::async_trait]
impl Discovery for HeadlessDnsDiscovery {
    fn initial_stargates(&self) -> Vec<StargateInfo> {
        vec![self.stargate_info_for_endpoint(&self.config.self_pod_name, self.config.grpc_port)]
    }

    // StatefulSet-backed headless Service SRV records carry canonical pod
    // hostnames and are backed by ready EndpointSlices.
    async fn discover_stargates(&self) -> Vec<StargateInfo> {
        let config = &self.config;
        let srv_lookup_name = format!("_grpc._tcp.{}", config.discovery_dns_name);

        match config.resolver.srv_lookup(&srv_lookup_name).await {
            Ok(lookup) => self.stargates_from_srv_records(
                lookup
                    .iter()
                    .map(|srv| (srv.target().to_utf8(), srv.port())),
            ),
            Err(e) => {
                warn!(
                    dns_name = %srv_lookup_name,
                    error = %e,
                    "headless service SRV lookup failed"
                );
                self.initial_stargates()
            }
        }
    }
}

fn finalize_stargates(
    mut stargates: Vec<StargateInfo>,
    self_info: StargateInfo,
    self_key: fn(&StargateInfo) -> &str,
) -> Vec<StargateInfo> {
    if !stargates
        .iter()
        .any(|stargate| self_key(stargate) == self_key(&self_info))
    {
        stargates.push(self_info);
    }
    stargates.sort_by(|left, right| {
        left.stargate_id
            .cmp(&right.stargate_id)
            .then_with(|| left.advertise_addr.cmp(&right.advertise_addr))
    });
    stargates
}

fn endpoint_hostname_from_srv_target<'a>(
    target: &'a str,
    headless_dns_suffix: &str,
) -> Option<&'a str> {
    let target = target.trim_end_matches('.');
    let suffix = headless_dns_suffix.trim_end_matches('.');
    let endpoint_hostname = target.strip_suffix(suffix)?.strip_suffix('.')?;
    if endpoint_hostname.is_empty() || endpoint_hostname.contains('.') {
        return None;
    }
    Some(endpoint_hostname)
}

pub struct SelfOnlyDiscovery {
    self_info: StargateInfo,
}

impl SelfOnlyDiscovery {
    pub fn new(self_addr: SocketAddr, self_stargate_id: String, http_port: u16) -> Self {
        Self {
            self_info: StargateInfo {
                stargate_id: self_stargate_id,
                advertise_addr: self_addr.to_string(),
                http_advertise_addr: SocketAddr::new(self_addr.ip(), http_port).to_string(),
                grpc_pylon_dial_addr: String::new(),
            },
        }
    }
}

#[async_trait::async_trait]
impl Discovery for SelfOnlyDiscovery {
    fn initial_stargates(&self) -> Vec<StargateInfo> {
        vec![self.self_info.clone()]
    }

    async fn discover_stargates(&self) -> Vec<StargateInfo> {
        self.initial_stargates()
    }
}

pub struct DnsDiscovery {
    self_addr: SocketAddr,
    self_stargate_id: String,
    discovery_dns_name: String,
    resolver: TokioAsyncResolver,
    http_port: u16,
}

impl DnsDiscovery {
    pub fn new(
        self_addr: SocketAddr,
        self_stargate_id: String,
        discovery_dns_name: String,
        resolver: TokioAsyncResolver,
        http_port: u16,
    ) -> Self {
        Self {
            self_addr,
            self_stargate_id,
            discovery_dns_name,
            resolver,
            http_port,
        }
    }

    fn stargate_info_for_ip(&self, ip: IpAddr) -> Option<StargateInfo> {
        let addr = SocketAddr::new(ip, self.self_addr.port());
        let is_self = addr == self.self_addr;
        if !is_self && (ip.is_loopback() || ip.is_unspecified()) {
            return None;
        }

        let addr = addr.to_string();
        Some(StargateInfo {
            stargate_id: if is_self {
                self.self_stargate_id.clone()
            } else {
                addr.clone()
            },
            advertise_addr: addr,
            http_advertise_addr: if is_self {
                SocketAddr::new(ip, self.http_port).to_string()
            } else {
                String::new()
            },
            grpc_pylon_dial_addr: String::new(),
        })
    }

    fn self_stargate_info(&self) -> StargateInfo {
        StargateInfo {
            stargate_id: self.self_stargate_id.clone(),
            advertise_addr: self.self_addr.to_string(),
            http_advertise_addr: SocketAddr::new(self.self_addr.ip(), self.http_port).to_string(),
            grpc_pylon_dial_addr: String::new(),
        }
    }

    fn stargates_from_ips<I>(&self, ips: I) -> Vec<StargateInfo>
    where
        I: IntoIterator<Item = IpAddr>,
    {
        let stargates = ips
            .into_iter()
            .filter_map(|ip| self.stargate_info_for_ip(ip))
            .collect();
        finalize_stargates(stargates, self.self_stargate_info(), |stargate| {
            &stargate.advertise_addr
        })
    }
}

#[async_trait::async_trait]
impl Discovery for DnsDiscovery {
    fn initial_stargates(&self) -> Vec<StargateInfo> {
        vec![self.self_stargate_info()]
    }

    async fn discover_stargates(&self) -> Vec<StargateInfo> {
        match self.resolver.lookup_ip(&self.discovery_dns_name).await {
            Ok(lookup) => self.stargates_from_ips(lookup.iter()),
            Err(err) => {
                warn!(
                    dns_name = %self.discovery_dns_name,
                    error = %err,
                    "dns lookup failed"
                );
                self.initial_stargates()
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use hickory_resolver::config::{ResolverConfig, ResolverOpts};

    fn stargate(id: &str, advertise_addr: &str, http_advertise_addr: &str) -> StargateInfo {
        StargateInfo {
            stargate_id: id.to_string(),
            advertise_addr: advertise_addr.to_string(),
            http_advertise_addr: http_advertise_addr.to_string(),
            grpc_pylon_dial_addr: String::new(),
        }
    }

    fn make_headless_discovery() -> HeadlessDnsDiscovery {
        let resolver =
            TokioAsyncResolver::tokio(ResolverConfig::default(), ResolverOpts::default());
        HeadlessDnsDiscovery::new(HeadlessDnsDiscoveryConfig {
            self_pod_name: "stargate-0".to_string(),
            pod_namespace: "prod".to_string(),
            advertised_hostname_template: "{pod_name}.stargate.external".to_string(),
            discovery_dns_name: "stargate-headless.prod.svc.cluster.local".to_string(),
            resolver,
            grpc_port: 50071,
        })
    }

    #[test]
    fn headless_dns_discovery_renders_self_from_pod_hostname() {
        let discovery = make_headless_discovery();

        assert_eq!(
            discovery.initial_stargates(),
            vec![stargate(
                "stargate-0",
                "stargate-0.stargate.external:50071",
                ""
            )]
        );
    }

    #[test]
    fn headless_dns_discovery_renders_peer_from_srv_target_hostname() {
        let discovery = make_headless_discovery();

        assert_eq!(
            discovery.stargate_info_for_endpoint("stargate-1", 50071),
            stargate("stargate-1", "stargate-1.stargate.external:50071", "")
        );
    }

    #[test]
    fn endpoint_hostname_from_srv_target_requires_headless_suffix() {
        assert_eq!(
            endpoint_hostname_from_srv_target(
                "stargate-1.stargate-headless.prod.svc.cluster.local.",
                "stargate-headless.prod.svc.cluster.local",
            ),
            Some("stargate-1")
        );
        assert_eq!(
            endpoint_hostname_from_srv_target(
                "stargate-1.other.prod.svc.cluster.local.",
                "stargate-headless.prod.svc.cluster.local",
            ),
            None
        );
    }

    #[test]
    fn headless_dns_discovery_finalizes_srv_records_with_self_fallback_and_sorting() {
        let discovery = make_headless_discovery();

        let stargates = discovery.stargates_from_srv_records(vec![
            (
                "stargate-2.stargate-headless.prod.svc.cluster.local.".to_string(),
                50072,
            ),
            (
                "stargate-9.other.prod.svc.cluster.local.".to_string(),
                50071,
            ),
            (
                "stargate-1.stargate-headless.prod.svc.cluster.local.".to_string(),
                50071,
            ),
        ]);

        assert_eq!(
            stargates,
            vec![
                stargate("stargate-0", "stargate-0.stargate.external:50071", ""),
                stargate("stargate-1", "stargate-1.stargate.external:50071", ""),
                stargate("stargate-2", "stargate-2.stargate.external:50072", ""),
            ]
        );
    }

    #[test]
    fn headless_dns_discovery_falls_back_to_self_when_no_srv_records_survive() {
        let discovery = make_headless_discovery();

        let stargates = discovery.stargates_from_srv_records(vec![(
            "stargate-9.other.prod.svc.cluster.local.".to_string(),
            50071,
        )]);

        assert_eq!(stargates, discovery.initial_stargates());
    }

    #[test]
    fn dns_discovery_finalizes_ips_with_self_fallback_alias_filter_and_sorting() {
        let resolver =
            TokioAsyncResolver::tokio(ResolverConfig::default(), ResolverOpts::default());
        let discovery = DnsDiscovery::new(
            "10.0.0.2:50071".parse().unwrap(),
            "local-stargate".to_string(),
            "stargate-headless".to_string(),
            resolver,
            8000,
        );

        let stargates = discovery.stargates_from_ips(vec![
            "10.0.0.3".parse().unwrap(),
            "127.0.0.1".parse().unwrap(),
            "10.0.0.1".parse().unwrap(),
        ]);

        assert_eq!(
            stargates,
            vec![
                stargate("10.0.0.1:50071", "10.0.0.1:50071", ""),
                stargate("10.0.0.3:50071", "10.0.0.3:50071", ""),
                stargate("local-stargate", "10.0.0.2:50071", "10.0.0.2:8000"),
            ]
        );
    }

    #[tokio::test]
    async fn self_only_discovery_returns_only_configured_stargate() {
        let discovery = SelfOnlyDiscovery::new(
            "127.0.0.1:50071".parse().unwrap(),
            "local-stargate".to_string(),
            8000,
        );
        let expected = vec![stargate(
            "local-stargate",
            "127.0.0.1:50071",
            "127.0.0.1:8000",
        )];

        assert_eq!(discovery.initial_stargates(), expected);
        assert_eq!(discovery.discover_stargates().await, expected);
    }

    #[test]
    fn dns_discovery_skips_loopback_aliases_that_are_not_self() {
        let resolver =
            TokioAsyncResolver::tokio(ResolverConfig::default(), ResolverOpts::default());
        let discovery = DnsDiscovery::new(
            "127.0.0.1:50071".parse().unwrap(),
            "local-stargate".to_string(),
            "localhost".to_string(),
            resolver,
            8000,
        );

        assert_eq!(
            discovery.stargate_info_for_ip("127.0.0.1".parse().unwrap()),
            Some(stargate(
                "local-stargate",
                "127.0.0.1:50071",
                "127.0.0.1:8000"
            ))
        );
        assert_eq!(discovery.stargate_info_for_ip("::1".parse().unwrap()), None);

        let scoped_self = "[fe80::1%3]:50071".parse().unwrap();
        let scoped = DnsDiscovery::new(
            scoped_self,
            "scoped-stargate".to_string(),
            "headless".to_string(),
            TokioAsyncResolver::tokio(ResolverConfig::default(), ResolverOpts::default()),
            8000,
        );
        assert_eq!(
            scoped.initial_stargates(),
            vec![stargate(
                "scoped-stargate",
                &scoped_self.to_string(),
                "[fe80::1]:8000"
            )]
        );
    }
}

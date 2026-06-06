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
    self_pod_name: String,
    pod_namespace: String,
    advertised_hostname_template: String,
    discovery_dns_name: String,
    resolver: TokioAsyncResolver,
    grpc_port: u16,
}

impl HeadlessDnsDiscovery {
    pub fn new(config: HeadlessDnsDiscoveryConfig) -> Self {
        Self {
            self_pod_name: config.self_pod_name,
            pod_namespace: config.pod_namespace,
            advertised_hostname_template: config.advertised_hostname_template,
            discovery_dns_name: config.discovery_dns_name,
            resolver: config.resolver,
            grpc_port: config.grpc_port,
        }
    }

    fn srv_lookup_name(&self) -> String {
        format!("_grpc._tcp.{}", self.discovery_dns_name)
    }

    fn stargate_info_for_endpoint(&self, endpoint_hostname: &str, grpc_port: u16) -> StargateInfo {
        let hostname = render_hostname(
            &self.advertised_hostname_template,
            endpoint_hostname,
            &self.pod_namespace,
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
}

#[async_trait::async_trait]
impl Discovery for HeadlessDnsDiscovery {
    fn initial_stargates(&self) -> Vec<StargateInfo> {
        vec![self.stargate_info_for_endpoint(&self.self_pod_name, self.grpc_port)]
    }

    // StatefulSet-backed headless Service SRV records carry canonical pod
    // hostnames and are backed by ready EndpointSlices.
    async fn discover_stargates(&self) -> Vec<StargateInfo> {
        let mut stargates = Vec::new();
        let srv_lookup_name = self.srv_lookup_name();

        match self.resolver.srv_lookup(srv_lookup_name.as_str()).await {
            Ok(lookup) => {
                for srv in lookup.iter() {
                    let target = srv.target().to_utf8();
                    let Some(endpoint_hostname) =
                        endpoint_hostname_from_srv_target(&target, &self.discovery_dns_name)
                    else {
                        warn!(
                            dns_name = %self.discovery_dns_name,
                            srv_target = %target,
                            "ignoring SRV target outside headless service domain"
                        );
                        continue;
                    };
                    stargates.push(self.stargate_info_for_endpoint(&endpoint_hostname, srv.port()));
                }
            }
            Err(e) => {
                warn!(
                    dns_name = %srv_lookup_name,
                    error = %e,
                    "headless service SRV lookup failed"
                );
            }
        };

        if stargates.is_empty() {
            return self.initial_stargates();
        }

        if !stargates
            .iter()
            .any(|s| s.stargate_id == self.self_pod_name)
        {
            stargates.push(self.stargate_info_for_endpoint(&self.self_pod_name, self.grpc_port));
        }

        sort_stargates(&mut stargates);
        stargates
    }
}

fn sort_stargates(stargates: &mut [StargateInfo]) {
    stargates.sort_by(|left, right| {
        left.stargate_id
            .cmp(&right.stargate_id)
            .then_with(|| left.advertise_addr.cmp(&right.advertise_addr))
    });
}

fn endpoint_hostname_from_srv_target(target: &str, headless_dns_suffix: &str) -> Option<String> {
    let target = target.trim_end_matches('.');
    let suffix = headless_dns_suffix.trim_end_matches('.');
    let service_suffix = format!(".{suffix}");
    let endpoint_hostname = target.strip_suffix(&service_suffix)?;
    if endpoint_hostname.is_empty() || endpoint_hostname.contains('.') {
        return None;
    }
    Some(endpoint_hostname.to_string())
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
        vec![self.self_info.clone()]
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
        if !is_self && is_non_self_local_alias(ip) {
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
}

#[async_trait::async_trait]
impl Discovery for DnsDiscovery {
    fn initial_stargates(&self) -> Vec<StargateInfo> {
        let self_addr = self.self_addr.to_string();
        vec![StargateInfo {
            stargate_id: self.self_stargate_id.clone(),
            advertise_addr: self_addr,
            http_advertise_addr: SocketAddr::new(self.self_addr.ip(), self.http_port).to_string(),
            grpc_pylon_dial_addr: String::new(),
        }]
    }

    async fn discover_stargates(&self) -> Vec<StargateInfo> {
        let self_addr_str = self.self_addr.to_string();
        let mut stargates = Vec::new();

        match self.resolver.lookup_ip(&self.discovery_dns_name).await {
            Ok(lookup) => {
                for ip in lookup.iter() {
                    if let Some(stargate) = self.stargate_info_for_ip(ip) {
                        stargates.push(stargate);
                    }
                }
            }
            Err(err) => {
                warn!(
                    dns_name = %self.discovery_dns_name,
                    error = %err,
                    "dns lookup failed"
                );
            }
        }

        if !stargates.iter().any(|s| s.advertise_addr == self_addr_str) {
            stargates.push(StargateInfo {
                stargate_id: self.self_stargate_id.clone(),
                advertise_addr: self_addr_str,
                http_advertise_addr: SocketAddr::new(self.self_addr.ip(), self.http_port)
                    .to_string(),
                grpc_pylon_dial_addr: String::new(),
            });
        }

        sort_stargates(&mut stargates);
        stargates
    }
}

fn is_non_self_local_alias(ip: IpAddr) -> bool {
    ip.is_loopback() || ip.is_unspecified()
}

#[cfg(test)]
mod tests {
    use super::*;
    use hickory_resolver::config::{ResolverConfig, ResolverOpts};

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
            vec![StargateInfo {
                stargate_id: "stargate-0".to_string(),
                advertise_addr: "stargate-0.stargate.external:50071".to_string(),
                http_advertise_addr: String::new(),
                grpc_pylon_dial_addr: String::new(),
            }]
        );
    }

    #[test]
    fn headless_dns_discovery_renders_peer_from_srv_target_hostname() {
        let discovery = make_headless_discovery();

        assert_eq!(
            discovery.stargate_info_for_endpoint("stargate-1", 50071),
            StargateInfo {
                stargate_id: "stargate-1".to_string(),
                advertise_addr: "stargate-1.stargate.external:50071".to_string(),
                http_advertise_addr: String::new(),
                grpc_pylon_dial_addr: String::new(),
            }
        );
    }

    #[test]
    fn endpoint_hostname_from_srv_target_requires_headless_suffix() {
        assert_eq!(
            endpoint_hostname_from_srv_target(
                "stargate-1.stargate-headless.prod.svc.cluster.local.",
                "stargate-headless.prod.svc.cluster.local",
            ),
            Some("stargate-1".to_string())
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
    fn sort_stargates_orders_by_id_for_stable_watch_snapshots() {
        let mut stargates = vec![
            StargateInfo {
                stargate_id: "stargate-1".to_string(),
                advertise_addr: "stargate-1.stargate.external:50071".to_string(),
                http_advertise_addr: String::new(),
                grpc_pylon_dial_addr: String::new(),
            },
            StargateInfo {
                stargate_id: "stargate-0".to_string(),
                advertise_addr: "stargate-0.stargate.external:50071".to_string(),
                http_advertise_addr: String::new(),
                grpc_pylon_dial_addr: String::new(),
            },
        ];

        sort_stargates(&mut stargates);

        assert_eq!(
            stargates
                .into_iter()
                .map(|stargate| stargate.stargate_id)
                .collect::<Vec<_>>(),
            vec!["stargate-0", "stargate-1"]
        );
    }

    #[tokio::test]
    async fn self_only_discovery_returns_only_configured_stargate() {
        let discovery = SelfOnlyDiscovery::new(
            "127.0.0.1:50071".parse().unwrap(),
            "local-stargate".to_string(),
            8000,
        );
        let expected = vec![StargateInfo {
            stargate_id: "local-stargate".to_string(),
            advertise_addr: "127.0.0.1:50071".to_string(),
            http_advertise_addr: "127.0.0.1:8000".to_string(),
            grpc_pylon_dial_addr: String::new(),
        }];

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
            Some(StargateInfo {
                stargate_id: "local-stargate".to_string(),
                advertise_addr: "127.0.0.1:50071".to_string(),
                http_advertise_addr: "127.0.0.1:8000".to_string(),
                grpc_pylon_dial_addr: String::new(),
            })
        );
        assert_eq!(discovery.stargate_info_for_ip("::1".parse().unwrap()), None);
    }
}

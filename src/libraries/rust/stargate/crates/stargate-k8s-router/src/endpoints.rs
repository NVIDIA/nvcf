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

use std::collections::BTreeMap;
use std::net::{IpAddr, SocketAddr};

use k8s_openapi::api::discovery::v1::{Endpoint, EndpointConditions, EndpointPort, EndpointSlice};
use stargate_forwarding::HostnameMatcher;

pub const ENDPOINT_SLICE_SERVICE_NAME_LABEL: &str = "kubernetes.io/service-name";

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct PodTarget {
    pub pod_name: String,
    pub grpc_addr: String,
    pub quic_addr: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TargetSnapshot {
    ready: Option<Vec<PodTarget>>,
}

impl TargetSnapshot {
    pub fn initialized(targets: impl IntoIterator<Item = PodTarget>) -> Self {
        let ready = targets
            .into_iter()
            .map(|target| (target.pod_name.clone(), target))
            .collect::<BTreeMap<_, _>>()
            .into_values()
            .collect();
        Self { ready: Some(ready) }
    }

    pub fn is_initialized(&self) -> bool {
        self.ready.is_some()
    }

    pub fn target_for_pod(&self, pod_name: &str) -> Option<PodTarget> {
        self.target_for_pod_ref(pod_name).cloned()
    }

    pub fn target_for_pod_ref(&self, pod_name: &str) -> Option<&PodTarget> {
        let targets = self.ready_targets()?;
        let index = targets
            .binary_search_by(|target| target.pod_name.as_str().cmp(pod_name))
            .ok()?;
        targets.get(index)
    }

    pub fn first_ready_ref(&self, offset: usize) -> Option<&PodTarget> {
        let targets = self.ready_targets()?;
        if targets.is_empty() {
            return None;
        }
        targets.get(offset % targets.len())
    }

    pub fn ready_count(&self) -> usize {
        self.ready_targets().map_or(0, <[PodTarget]>::len)
    }

    fn ready_targets(&self) -> Option<&[PodTarget]> {
        self.ready.as_deref()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SniRouteRejection {
    MissingSni,
    UnknownSni,
    TargetUnavailable,
}

impl SniRouteRejection {
    pub(crate) fn metric_and_reason(self) -> (&'static str, &'static [u8]) {
        match self {
            Self::MissingSni => ("missing_sni", b"missing target SNI"),
            Self::UnknownSni => ("unknown_sni", b"unknown target SNI"),
            Self::TargetUnavailable => ("target_unavailable", b"target stargate not ready"),
        }
    }
}

pub(crate) fn ready_target_for_sni<'target, 'sni>(
    sni: Option<&'sni str>,
    targets: &'target TargetSnapshot,
    hostname_matcher: Option<&HostnameMatcher>,
) -> Result<(&'target PodTarget, &'sni str), SniRouteRejection> {
    let sni = sni.ok_or(SniRouteRejection::MissingSni)?;
    let pod_name = hostname_matcher
        .and_then(|matcher| matcher.extract_pod(sni))
        .ok_or(SniRouteRejection::UnknownSni)?;
    let target = targets
        .target_for_pod_ref(pod_name)
        .ok_or(SniRouteRejection::TargetUnavailable)?;
    Ok((target, sni))
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct TargetBuildConfig {
    pub service_name: String,
    pub grpc_port_name: String,
    pub quic_port_name: String,
}

pub fn snapshot_from_slices<'a>(
    slices: impl IntoIterator<Item = &'a EndpointSlice>,
    config: &TargetBuildConfig,
) -> TargetSnapshot {
    let targets = slices
        .into_iter()
        .filter(|slice| slice_belongs_to_service(slice, &config.service_name))
        .filter_map(|slice| {
            let ports = slice.ports.as_deref()?;
            Some((
                slice,
                named_port(ports, &config.grpc_port_name, "TCP")?,
                named_port(ports, &config.quic_port_name, "UDP")?,
            ))
        })
        .flat_map(|(slice, grpc_port, quic_port)| {
            slice
                .endpoints
                .iter()
                .filter_map(move |endpoint| target_from_endpoint(endpoint, grpc_port, quic_port))
        });
    TargetSnapshot::initialized(targets)
}

fn slice_belongs_to_service(slice: &EndpointSlice, service_name: &str) -> bool {
    slice
        .metadata
        .labels
        .as_ref()
        .and_then(|labels| labels.get(ENDPOINT_SLICE_SERVICE_NAME_LABEL))
        .is_some_and(|value| value == service_name)
}

fn named_port(ports: &[EndpointPort], name: &str, protocol: &str) -> Option<u16> {
    let port = ports.iter().find(|port| {
        port.name.as_deref() == Some(name)
            && port
                .protocol
                .as_deref()
                .unwrap_or("TCP")
                .eq_ignore_ascii_case(protocol)
    })?;
    u16::try_from(port.port?).ok().filter(|port| *port > 0)
}

fn target_from_endpoint(endpoint: &Endpoint, grpc_port: u16, quic_port: u16) -> Option<PodTarget> {
    if !endpoint_is_ready(endpoint.conditions.as_ref()) {
        return None;
    }
    let pod_name = endpoint_pod_name(endpoint)?.to_owned();
    let address = endpoint.addresses.first()?;
    Some(PodTarget {
        pod_name,
        grpc_addr: socket_addr(address, grpc_port),
        quic_addr: socket_addr(address, quic_port),
    })
}

fn endpoint_is_ready(conditions: Option<&EndpointConditions>) -> bool {
    let ready = conditions.and_then(|c| c.ready).unwrap_or(true);
    let serving = conditions.and_then(|c| c.serving).unwrap_or(true);
    let terminating = conditions.and_then(|c| c.terminating).unwrap_or(false);
    ready && serving && !terminating
}

fn endpoint_pod_name(endpoint: &Endpoint) -> Option<&str> {
    endpoint
        .target_ref
        .as_ref()
        .filter(|target| target.kind.as_deref().is_none_or(|kind| kind == "Pod"))
        .and_then(|target| target.name.as_deref())
        .or(endpoint.hostname.as_deref())
}

fn socket_addr(address: &str, port: u16) -> String {
    address
        .parse::<IpAddr>()
        .map(|ip| SocketAddr::new(ip, port).to_string())
        .unwrap_or_else(|_| format!("{address}:{port}"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::perf_tests::assert_twenty_percent_faster;
    use k8s_openapi::api::core::v1::ObjectReference;
    use k8s_openapi::apimachinery::pkg::apis::meta::v1::ObjectMeta;
    use std::hint::black_box;
    use std::time::Instant;

    fn config() -> TargetBuildConfig {
        TargetBuildConfig {
            service_name: "stargate-headless".to_string(),
            grpc_port_name: "grpc".to_string(),
            quic_port_name: "quic".to_string(),
        }
    }

    fn slice(name: &str, endpoints: Vec<Endpoint>) -> EndpointSlice {
        EndpointSlice {
            address_type: "IPv4".to_string(),
            metadata: ObjectMeta {
                name: Some(name.to_string()),
                labels: Some(BTreeMap::from([(
                    ENDPOINT_SLICE_SERVICE_NAME_LABEL.to_string(),
                    "stargate-headless".to_string(),
                )])),
                ..ObjectMeta::default()
            },
            ports: Some(vec![
                EndpointPort {
                    name: Some("grpc".to_string()),
                    port: Some(50071),
                    protocol: Some("TCP".to_string()),
                    ..EndpointPort::default()
                },
                EndpointPort {
                    name: Some("quic".to_string()),
                    port: Some(50072),
                    protocol: Some("UDP".to_string()),
                    ..EndpointPort::default()
                },
            ]),
            endpoints,
        }
    }

    fn endpoint(name: &str, ip: &str, conditions: Option<EndpointConditions>) -> Endpoint {
        Endpoint {
            addresses: vec![ip.to_string()],
            conditions,
            target_ref: Some(ObjectReference {
                kind: Some("Pod".to_string()),
                name: Some(name.to_string()),
                ..ObjectReference::default()
            }),
            ..Endpoint::default()
        }
    }

    fn target(name: &str, ip: &str) -> PodTarget {
        PodTarget {
            pod_name: name.to_string(),
            grpc_addr: format!("{ip}:50071"),
            quic_addr: format!("{ip}:50072"),
        }
    }

    fn conditions(
        ready: Option<bool>,
        serving: Option<bool>,
        terminating: Option<bool>,
    ) -> Option<EndpointConditions> {
        Some(EndpointConditions {
            ready,
            serving,
            terminating,
        })
    }

    fn many_targets(count: usize) -> TargetSnapshot {
        let targets: Vec<_> = (0..count)
            .map(|index| {
                let pod_name = format!("stargate-{index}");
                target(&pod_name, &format!("10.0.0.{index}"))
            })
            .collect();
        TargetSnapshot::initialized(targets)
    }

    #[test]
    fn initialized_snapshot_owns_one_stable_ready_target_sequence() {
        let snapshot = TargetSnapshot::initialized(vec![
            target("stargate-1", "10.0.0.11"),
            target("stargate-0", "10.0.0.10"),
        ]);

        assert!(snapshot.is_initialized());
        assert_eq!(
            snapshot
                .first_ready_ref(0)
                .map(|target| target.pod_name.as_str()),
            Some("stargate-0")
        );
        assert_eq!(
            snapshot
                .first_ready_ref(1)
                .map(|target| target.pod_name.as_str()),
            Some("stargate-1")
        );
        assert_eq!(
            snapshot
                .target_for_pod_ref("stargate-1")
                .map(|target| target.grpc_addr.as_str()),
            Some("10.0.0.11:50071")
        );
        assert!(std::ptr::eq(
            snapshot
                .first_ready_ref(1)
                .expect("ordered target should exist"),
            snapshot
                .target_for_pod_ref("stargate-1")
                .expect("named target should exist")
        ));
        assert!(!TargetSnapshot::default().is_initialized());
    }

    #[test]
    fn initialized_snapshot_normalizes_duplicate_pods_by_last_observation() {
        let snapshot = TargetSnapshot::initialized([
            target("stargate-0", "10.0.0.10"),
            target("stargate-0", "10.0.0.11"),
        ]);

        assert_eq!(snapshot.ready_count(), 1);
        assert_eq!(
            snapshot
                .target_for_pod_ref("stargate-0")
                .map(|target| target.grpc_addr.as_str()),
            Some("10.0.0.11:50071")
        );
    }

    #[test]
    fn snapshot_includes_ready_pod_targets() {
        let slice = slice(
            "slice-a",
            vec![endpoint(
                "stargate-0",
                "10.0.0.10",
                conditions(Some(true), Some(true), Some(false)),
            )],
        );

        let snapshot = snapshot_from_slices([&slice], &config());

        assert!(snapshot.is_initialized());
        assert_eq!(
            snapshot.target_for_pod("stargate-0"),
            Some(target("stargate-0", "10.0.0.10"))
        );
    }

    #[test]
    fn snapshot_treats_missing_conditions_with_kubernetes_defaults() {
        let slice = slice("slice-a", vec![endpoint("stargate-0", "10.0.0.10", None)]);

        let snapshot = snapshot_from_slices([&slice], &config());

        assert_eq!(snapshot.ready_count(), 1);
    }

    #[test]
    fn snapshot_excludes_unready_serving_false_and_terminating_endpoints() {
        let slice = slice(
            "slice-a",
            vec![
                endpoint("unready", "10.0.0.10", conditions(Some(false), None, None)),
                endpoint(
                    "not-serving",
                    "10.0.0.11",
                    conditions(None, Some(false), None),
                ),
                endpoint(
                    "terminating",
                    "10.0.0.12",
                    conditions(None, None, Some(true)),
                ),
            ],
        );

        let snapshot = snapshot_from_slices([&slice], &config());

        assert_eq!(snapshot.ready_count(), 0);
    }

    #[test]
    fn snapshot_ignores_slices_without_named_tcp_and_udp_ports() {
        let mut slice = slice(
            "slice-a",
            vec![endpoint(
                "stargate-0",
                "10.0.0.10",
                conditions(Some(true), None, None),
            )],
        );
        slice.ports = Some(vec![EndpointPort {
            name: Some("grpc".to_string()),
            port: Some(50071),
            protocol: Some("TCP".to_string()),
            ..EndpointPort::default()
        }]);

        let snapshot = snapshot_from_slices([&slice], &config());

        assert_eq!(snapshot.ready_count(), 0);
    }

    #[test]
    fn snapshot_formats_ipv6_endpoint_addresses() {
        let slice = slice("slice-a", vec![endpoint("stargate-0", "fd00::10", None)]);

        let snapshot = snapshot_from_slices([&slice], &config());

        assert_eq!(
            snapshot
                .target_for_pod("stargate-0")
                .map(|target| target.grpc_addr),
            Some("[fd00::10]:50071".to_string())
        );
    }

    #[test]
    #[ignore = "performance benchmark; run with --ignored --nocapture"]
    fn bench_target_snapshot_ready_lookup() {
        const BASELINE_NS_PER_OP: f64 = 136.95;

        let snapshot = many_targets(128);
        let iterations = 1_000_000usize;
        let started = Instant::now();
        let mut checksum = 0usize;

        for index in 0..iterations {
            let target = black_box(&snapshot)
                .first_ready_ref(black_box(index))
                .expect("snapshot has targets");
            checksum = checksum.wrapping_add(target.grpc_addr.len());
        }

        let elapsed = started.elapsed();
        let ns_per_op = elapsed.as_nanos() as f64 / iterations as f64;
        eprintln!(
            "bench_target_snapshot_ready_lookup: iterations={iterations} elapsed={elapsed:?} ns_per_op={ns_per_op:.2} checksum={checksum}"
        );
        assert!(checksum > 0);
        assert_twenty_percent_faster(
            "bench_target_snapshot_ready_lookup",
            BASELINE_NS_PER_OP,
            ns_per_op,
        );
    }
}

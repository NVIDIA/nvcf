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

//! Router seed URLs and the RouterCache.

use std::collections::BTreeSet;
use std::sync::Arc;
use std::time::Duration;

use gds_client::v1::{LeaseState, Router, RouterMode, RouterReplica};
use gds_client::{
    GdsClient, RouterCache, RouterCacheConfig, RouterState, derived_service_url, is_direct,
    pylon_seed_urls, quic_port, relay_seed_urls, remote_watch_urls, replica_authority,
    router_relay_urls, router_seed_urls,
};
use tokio_util::sync::CancellationToken;

use crate::support::fake_gds::FakeGds;
use crate::support::fast_policy;

fn router(cluster: &str, namespace: &str, name: &str, scheme: &str, grpc_port: i32) -> Router {
    Router {
        cluster: cluster.to_string(),
        region: format!("region-{cluster}"),
        namespace: namespace.to_string(),
        name: name.to_string(),
        grpc_port,
        http_port: 8080,
        dial_scheme: scheme.to_string(),
        cluster_lease_state: LeaseState::Active as i32,
        ..Router::default()
    }
}

/// `router` with one ready replica per address.
fn with_replicas(mut router: Router, addresses: &[&str]) -> Router {
    router.replicas = addresses
        .iter()
        .map(|address| replica(address, true))
        .collect();
    router
}

fn replica(address: &str, ready: bool) -> RouterReplica {
    RouterReplica {
        address: address.to_string(),
        ready,
        ..RouterReplica::default()
    }
}

fn state(routers: Vec<Router>) -> RouterState {
    RouterState {
        routers: routers
            .into_iter()
            .map(|router| (gds_client::routers::router_key(&router), Arc::new(router)))
            .collect(),
        synced: true,
        ..RouterState::default()
    }
}

fn set(urls: &[&str]) -> BTreeSet<String> {
    urls.iter().map(ToString::to_string).collect()
}

#[test]
fn router_seed_urls_are_the_ready_replicas() {
    let mut r = router("a", "llm", "llm-request-router-backend", "https", 50071);
    r.replicas = vec![
        replica("10.0.0.2", true),
        replica("10.0.0.1", true),
        replica("10.0.0.1", true),
        replica("10.0.0.3", false),
        replica("2001:db8::1", true),
        replica("fe80::1%eth0", true),
        replica("router-0.example.internal", true),
        replica("bad:host", true),
        replica(" ", true),
    ];
    assert_eq!(
        router_seed_urls(&r),
        vec![
            "https://10.0.0.1:50071",
            "https://10.0.0.2:50071",
            "https://[2001:db8::1]:50071",
            "https://router-0.example.internal:50071",
        ],
        "ready replicas only, IPv6 bracketed, sorted, once each"
    );

    r.dial_scheme = "HTTP".to_string();
    assert_eq!(router_seed_urls(&r)[0], "http://10.0.0.1:50071");
    r.dial_scheme = String::new();
    assert_eq!(
        router_seed_urls(&r)[0],
        "https://10.0.0.1:50071",
        "the scheme defaults to https"
    );
    r.dial_scheme = "grpc".to_string();
    assert!(router_seed_urls(&r).is_empty(), "unknown dial scheme");
    r.dial_scheme = "https".to_string();
    for port in [0, 70000] {
        r.grpc_port = port;
        assert!(router_seed_urls(&r).is_empty(), "gRPC port {port}");
    }
    assert!(router_seed_urls(&router("a", "llm", "r", "https", 50071)).is_empty());
}

#[test]
fn derived_service_url_is_the_shared_vip() {
    assert_eq!(
        derived_service_url(&router(
            "a",
            "llm",
            "llm-request-router-backend",
            "https",
            50071
        ))
        .as_deref(),
        Some("https://derived-b756q8b74k.llm.svc.cluster.local:50071")
    );
    assert_eq!(
        derived_service_url(&router("a", "llm-stack", "backend-router", "http", 50071)).as_deref(),
        Some("http://derived-t1d4et30ia.llm-stack.svc.cluster.local:50071")
    );
    assert_eq!(
        derived_service_url(&router("a", "llm-stack", "backend-router", "", 443)).as_deref(),
        Some("https://derived-t1d4et30ia.llm-stack.svc.cluster.local:443"),
        "the scheme defaults to https"
    );
    assert_eq!(
        derived_service_url(&router("a", "llm-stack", "backend-router", "HTTPS", 443)).as_deref(),
        Some("https://derived-t1d4et30ia.llm-stack.svc.cluster.local:443")
    );
    assert!(derived_service_url(&router("a", "llm", "r", "grpc", 50071)).is_none());
    assert!(derived_service_url(&router("a", "llm", "r", "https", 0)).is_none());
    assert!(derived_service_url(&router("a", "llm", "r", "https", 70000)).is_none());
    assert!(derived_service_url(&router("a", "", "r", "https", 50071)).is_none());
}

#[test]
fn remote_and_pylon_seeds() {
    let mut expired = with_replicas(
        router("d", "llm", "router-d", "https", 50071),
        &["10.3.0.1"],
    );
    expired.cluster_lease_state = LeaseState::Expired as i32;
    let state = state(vec![
        with_replicas(
            router("a", "llm", "router-a", "https", 50071),
            &["10.0.0.1"],
        ),
        with_replicas(
            router("b", "llm", "router-b", "https", 50071),
            &["10.1.0.1", "10.1.0.2"],
        ),
        with_replicas(router("c", "llm", "router-c", "http", 50072), &["fd00::5"]),
        expired,
        with_replicas(router("e", "llm", "no-grpc", "https", 0), &["10.4.0.1"]),
        router("f", "llm", "no-replicas", "https", 50071),
    ]);
    let a = "https://10.0.0.1:50071";
    let b = ["https://10.1.0.1:50071", "https://10.1.0.2:50071"];
    let c = "http://[fd00::5]:50072";

    assert_eq!(remote_watch_urls(&state, "a"), set(&[b[0], b[1], c]));
    assert_eq!(remote_watch_urls(&state, "z"), set(&[a, b[0], b[1], c]));
    assert_eq!(pylon_seed_urls(&state), set(&[a, b[0], b[1], c]));
    assert_eq!(state.clusters().len(), 6);
}

#[test]
fn quic_port_is_set_only_for_a_valid_port() {
    let mut r = router("a", "llm", "router", "http", 50071);
    assert_eq!(quic_port(&r), None, "absent");
    r.quic_port = 8443;
    assert_eq!(quic_port(&r), Some(8443));
    for port in [-1, 70000] {
        r.quic_port = port;
        assert_eq!(quic_port(&r), None, "QUIC port {port}");
    }
}

#[test]
fn relay_urls_are_the_ready_grpc_replicas() {
    let mut r = with_replicas(
        router("a", "llm", "router-a", "http", 50071),
        &["10.0.0.2", "10.0.0.1", "fd00::1", "10.0.0.1"],
    );
    r.replicas.push(replica("10.0.0.3", false));
    r.quic_port = 8443;
    assert_eq!(
        router_relay_urls(&r),
        set(&[
            "http://10.0.0.1:50071",
            "http://10.0.0.2:50071",
            "http://[fd00::1]:50071",
        ]),
        "the gRPC port of every ready replica, never the QUIC port"
    );
    assert_eq!(
        router_relay_urls(&r),
        router_seed_urls(&r).into_iter().collect::<BTreeSet<_>>(),
        "relays and pylons dial the same listener today"
    );
    r.dial_scheme = "grpc".to_string();
    assert!(router_relay_urls(&r).is_empty(), "unknown dial scheme");
    r.dial_scheme = "http".to_string();
    r.grpc_port = 0;
    assert!(router_relay_urls(&r).is_empty(), "no gRPC port");
}

#[test]
fn relay_seeds_cover_every_cluster_with_a_live_lease() {
    let mut expired = with_replicas(
        router("c", "llm", "router-c", "https", 50071),
        &["10.2.0.1"],
    );
    expired.cluster_lease_state = LeaseState::Expired as i32;
    let state = state(vec![
        with_replicas(
            router("a", "llm", "router-a", "https", 50071),
            &["10.0.0.1"],
        ),
        with_replicas(
            router("b", "llm", "router-b", "http", 50072),
            &["10.1.0.1", "10.1.0.2"],
        ),
        expired,
    ]);
    let relay = relay_seed_urls(&state);
    assert_eq!(
        relay,
        set(&[
            "https://10.0.0.1:50071",
            "http://10.1.0.1:50072",
            "http://10.1.0.2:50072",
        ]),
        "the relay's own cluster included, expired leases skipped"
    );
    assert_eq!(relay, pylon_seed_urls(&state));
    assert!(relay_seed_urls(&RouterState::default()).is_empty());
}

#[test]
fn routers_sharing_a_name_keep_their_own_seeds() {
    // Same namespace and name in two clusters: one import and one derived
    // VIP that spans both, but each cluster's replicas stay separate seeds.
    let a = with_replicas(
        router("a", "nvcf", "llm-request-router-backend", "https", 50071),
        &["10.0.0.1", "10.0.0.2"],
    );
    let b = with_replicas(
        router("b", "nvcf", "llm-request-router-backend", "https", 50071),
        &["10.1.0.1"],
    );
    assert_eq!(derived_service_url(&a), derived_service_url(&b));
    let state = state(vec![a, b]);
    assert_eq!(
        pylon_seed_urls(&state),
        set(&[
            "https://10.0.0.1:50071",
            "https://10.0.0.2:50071",
            "https://10.1.0.1:50071",
        ])
    );
    assert_eq!(
        remote_watch_urls(&state, "a"),
        set(&["https://10.1.0.1:50071"]),
        "a Stargate in a watches only b's replicas"
    );
    let vip = derived_service_url(state.iter().next().expect("router")).expect("vip");
    assert!(
        !pylon_seed_urls(&state).contains(&vip),
        "the derived VIP is not a seed"
    );
}

/// A direct-mode router: its replicas are Stargate pods with identities.
fn direct(cluster: &str, template: &str, replicas: &[(&str, &str)]) -> Router {
    let mut r = router(cluster, "llm", "stargate", "http", 50071);
    r.router_mode = RouterMode::Direct as i32;
    r.hostname_template = template.to_string();
    r.replicas = replicas
        .iter()
        .map(|(address, identity)| RouterReplica {
            identity: identity.to_string(),
            ..replica(address, true)
        })
        .collect();
    r
}

#[test]
fn replica_authority_of_a_direct_router() {
    let r = direct(
        "a",
        "{pod_name}.stargate.example.com",
        &[
            ("10.0.0.1", "stargate-0"),
            ("10.0.0.2", "stargate-7f9c-abcde"),
            ("10.0.0.3", ""),
        ],
    );
    assert!(is_direct(&r));
    let authorities: Vec<Option<String>> = r
        .replicas
        .iter()
        .map(|replica| replica_authority(&r, replica))
        .collect();
    assert_eq!(
        authorities,
        vec![
            Some("stargate-0.stargate.example.com:50071".to_string()),
            Some("stargate-7f9c-abcde.stargate.example.com:50071".to_string()),
            None,
        ],
        "a replica without identity has no authority"
    );
    assert_eq!(
        router_seed_urls(&r),
        vec![
            "http://10.0.0.1:50071",
            "http://10.0.0.2:50071",
            "http://10.0.0.3:50071"
        ],
        "the seeds stay the replica addresses"
    );

    let mut not_ready = r.replicas[0].clone();
    not_ready.ready = false;
    assert_eq!(
        replica_authority(&r, &not_ready).as_deref(),
        Some("stargate-0.stargate.example.com:50071"),
        "readiness is not checked"
    );

    let mut no_port = r.clone();
    no_port.grpc_port = 0;
    assert_eq!(
        replica_authority(&no_port, &r.replicas[0]),
        None,
        "no gRPC port"
    );
    let mut no_template = r.clone();
    no_template.hostname_template = String::new();
    assert_eq!(
        replica_authority(&no_template, &r.replicas[0]),
        None,
        "no template"
    );
    let mut no_placeholder = r.clone();
    no_placeholder.hostname_template = "stargate.example.com".to_string();
    assert_eq!(
        replica_authority(&no_placeholder, &r.replicas[0]),
        None,
        "no placeholder"
    );
}

#[test]
fn backend_router_replicas_have_no_authority() {
    for mode in [RouterMode::Unspecified, RouterMode::BackendRouter] {
        let mut r = direct(
            "a",
            "{pod_name}.stargate.example.com",
            &[("10.0.0.1", "router-0")],
        );
        r.router_mode = mode as i32;
        assert!(!is_direct(&r), "{mode:?}");
        assert_eq!(replica_authority(&r, &r.replicas[0]), None, "{mode:?}");
    }
    let mut unknown = direct("a", "{pod_name}.x", &[("10.0.0.1", "router-0")]);
    unknown.router_mode = 42;
    assert!(!is_direct(&unknown), "an unknown mode is not direct");
}

#[tokio::test(start_paused = true)]
async fn router_cache_feeds_seed_urls() {
    let fake = FakeGds::new("r");
    fake.upsert_router(with_replicas(
        router("a", "llm", "router", "https", 50071),
        &["10.0.0.1"],
    ));
    let (channel, _server) = fake.in_memory_channel();
    let client = GdsClient::from_channel_with_policy(channel, fast_policy());
    let stop = CancellationToken::new();
    let cache = RouterCache::spawn(client, RouterCacheConfig::new("cs"), stop.clone());

    let state = cache.wait_synced().await.expect("synced");
    assert_eq!(state.len(), 1);
    assert!(remote_watch_urls(&state, "a").is_empty());
    assert_eq!(pylon_seed_urls(&state), set(&["https://10.0.0.1:50071"]));

    let remote = with_replicas(
        router("b", "llm", "router-b", "https", 50071),
        &["10.1.0.1", "10.1.0.2"],
    );
    let remote_seeds: BTreeSet<String> = router_seed_urls(&remote).into_iter().collect();
    fake.upsert_router(remote);
    let mut rx = cache.subscribe();
    let state = tokio::time::timeout(
        Duration::from_secs(60),
        rx.wait_for(|state| state.len() == 2),
    )
    .await
    .expect("delta in time")
    .expect("running")
    .clone();
    assert_eq!(remote_watch_urls(&state, "a"), remote_seeds);
    assert_eq!(pylon_seed_urls(&state).len(), 3);
    assert_eq!(state.by_cluster("b").count(), 1);

    fake.remove_router("b", "llm", "router-b");
    let state = tokio::time::timeout(
        Duration::from_secs(60),
        rx.wait_for(|state| state.len() == 1),
    )
    .await
    .expect("delete in time")
    .expect("running")
    .clone();
    assert!(remote_watch_urls(&state, "a").is_empty());
    stop.cancel();
}

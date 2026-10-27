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

use gds_client::v1::{LeaseState, Router, RouterReplica};
use gds_client::{
    GdsClient, RouterCache, RouterCacheConfig, RouterState, derived_service_url, pylon_seed_urls,
    remote_watch_urls, router_seed_urls,
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

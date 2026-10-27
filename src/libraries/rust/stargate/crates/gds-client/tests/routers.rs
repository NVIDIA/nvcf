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

use gds_client::v1::{LeaseState, Router};
use gds_client::{
    GdsClient, RouterCache, RouterCacheConfig, RouterState, pylon_seed_urls, remote_watch_urls,
    seed_url,
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
fn seed_url_is_the_derived_service() {
    assert_eq!(
        seed_url(&router(
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
        seed_url(&router("a", "llm-stack", "backend-router", "http", 50071)).as_deref(),
        Some("http://derived-t1d4et30ia.llm-stack.svc.cluster.local:50071")
    );
    assert_eq!(
        seed_url(&router("a", "llm-stack", "backend-router", "", 443)).as_deref(),
        Some("https://derived-t1d4et30ia.llm-stack.svc.cluster.local:443"),
        "the scheme defaults to https"
    );
    assert_eq!(
        seed_url(&router("a", "llm-stack", "backend-router", "HTTPS", 443)).as_deref(),
        Some("https://derived-t1d4et30ia.llm-stack.svc.cluster.local:443")
    );
    assert!(seed_url(&router("a", "llm", "r", "grpc", 50071)).is_none());
    assert!(seed_url(&router("a", "llm", "r", "https", 0)).is_none());
    assert!(seed_url(&router("a", "llm", "r", "https", 70000)).is_none());
    assert!(seed_url(&router("a", "", "r", "https", 50071)).is_none());
}

#[test]
fn remote_and_pylon_seeds() {
    let mut expired = router("d", "llm", "router-d", "https", 50071);
    expired.cluster_lease_state = LeaseState::Expired as i32;
    let state = state(vec![
        router("a", "llm", "router-a", "https", 50071),
        router("b", "llm", "router-b", "https", 50071),
        router("c", "llm", "router-c", "http", 50072),
        expired,
        router("e", "llm", "no-grpc", "https", 0),
    ]);
    let a = seed_url(&router("a", "llm", "router-a", "https", 50071)).expect("a");
    let b = seed_url(&router("b", "llm", "router-b", "https", 50071)).expect("b");
    let c = seed_url(&router("c", "llm", "router-c", "http", 50072)).expect("c");
    assert!(c.starts_with("http://derived-") && c.ends_with(".llm.svc.cluster.local:50072"));

    assert_eq!(remote_watch_urls(&state, "a"), set(&[&b, &c]));
    assert_eq!(remote_watch_urls(&state, "z"), set(&[&a, &b, &c]));
    assert_eq!(pylon_seed_urls(&state), set(&[&a, &b, &c]));
    assert_eq!(state.clusters().len(), 5);
}

#[test]
fn routers_sharing_a_name_share_one_seed() {
    // Same namespace and name in two clusters: one derived Service, one URL.
    let state = state(vec![
        router("a", "llm", "backend-router", "https", 50071),
        router("b", "llm", "backend-router", "https", 50071),
    ]);
    assert_eq!(pylon_seed_urls(&state).len(), 1);
    assert_eq!(remote_watch_urls(&state, "a").len(), 1);
}

#[tokio::test(start_paused = true)]
async fn router_cache_feeds_seed_urls() {
    let fake = FakeGds::new("r");
    fake.upsert_router(router("a", "llm", "router", "https", 50071));
    let (channel, _server) = fake.in_memory_channel();
    let client = GdsClient::from_channel_with_policy(channel, fast_policy());
    let stop = CancellationToken::new();
    let cache = RouterCache::spawn(client, RouterCacheConfig::new("cs"), stop.clone());

    let state = cache.wait_synced().await.expect("synced");
    assert_eq!(state.len(), 1);
    assert!(remote_watch_urls(&state, "a").is_empty());
    assert_eq!(pylon_seed_urls(&state).len(), 1);

    let remote = router("b", "llm", "router-b", "https", 50071);
    let remote_seed = seed_url(&remote).expect("seed");
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
    assert_eq!(remote_watch_urls(&state, "a"), set(&[&remote_seed]));
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

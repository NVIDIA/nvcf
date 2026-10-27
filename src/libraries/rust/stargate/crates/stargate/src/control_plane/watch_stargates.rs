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

use std::collections::{BTreeMap, BTreeSet};
use std::net::SocketAddr;
use std::time::{Duration, Instant};

use futures::{Stream, stream};
use tokio::sync::watch;
use tonic::Status;
use tracing::{debug, warn};
use url::Url;

use stargate_proto::pb::{StargateInfo, WatchStargatesResponse};
use stargate_protocol::parse_explicit_http_uri;

use crate::discovery::Discovery;
use stargate_runtime::CriticalTaskGroup;

pub(super) struct WatchStargatesPublisherConfig {
    pub(super) advertise_addr: SocketAddr,
    pub(super) discovery_dns_name: String,
    pub(super) discovery: Box<dyn Discovery>,
    pub(super) remote_watch_stargate_urls: Vec<String>,
    pub(super) grpc_pylon_dial_addr: Option<String>,
    pub(super) discovery_poll_interval: Duration,
    pub(super) watch_heartbeat_interval: Duration,
    pub(super) tasks: CriticalTaskGroup,
}

pub(super) fn spawn_watch_stargates_publisher(
    WatchStargatesPublisherConfig {
        advertise_addr,
        discovery_dns_name,
        discovery,
        remote_watch_stargate_urls,
        grpc_pylon_dial_addr,
        discovery_poll_interval,
        watch_heartbeat_interval,
        tasks,
    }: WatchStargatesPublisherConfig,
) -> watch::Receiver<WatchStargatesResponse> {
    let local_watch_endpoint_keys = local_watch_endpoint_keys(advertise_addr, &discovery_dns_name);
    let remote_watch_stargate_urls =
        normalize_remote_watch_urls(remote_watch_stargate_urls, &local_watch_endpoint_keys);
    let (watch_stargates_tx, watch_stargates_rx) =
        watch::channel(WatchStargatesResponse::default());

    tasks.spawn_critical("WatchStargates publisher", move |stop| async move {
        let mut known = WatchStargatesResponse::default();
        let mut last_emit: Option<Instant> = None;
        let mut poll = tokio::time::interval(discovery_poll_interval);
        poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);

        loop {
            tokio::select! {
                _ = stop.cancelled() => break,
                _ = poll.tick() => {}
            }

            let event = tokio::select! {
                _ = stop.cancelled() => break,
                (stargates, dynamic_watch_urls) = async {
                    tokio::join!(
                        discovery.discover_stargates(),
                        discovery.discover_remote_watch_urls(),
                    )
                } => {
                    let remote_watch_urls = union_remote_watch_urls(
                        &remote_watch_stargate_urls,
                        dynamic_watch_urls,
                        &local_watch_endpoint_keys,
                    );
                    build_watch_stargates_response(
                        stargates,
                        &remote_watch_urls,
                        grpc_pylon_dial_addr.as_deref(),
                    )
                },
            };

            let changed = event != known;
            let heartbeat_due = last_emit.is_none_or(|ts| ts.elapsed() >= watch_heartbeat_interval);

            if changed || heartbeat_due {
                let _ = watch_stargates_tx.send(event.clone());
                last_emit = Some(Instant::now());
                debug!(
                    stargate_count = event.stargates.len(),
                    remote_watch_url_count = event.watch_stargate_urls.len(),
                    changed,
                    heartbeat_due,
                    "published stargate snapshot"
                );
            }

            known = event;
        }
        Ok(())
    });

    watch_stargates_rx
}

fn build_watch_stargates_response(
    stargates: Vec<StargateInfo>,
    remote_watch_stargate_urls: &[String],
    grpc_pylon_dial_addr: Option<&str>,
) -> WatchStargatesResponse {
    let mut stargates = normalize_stargates(stargates);
    if let Some(grpc_pylon_dial_addr) = grpc_pylon_dial_addr
        .map(str::trim)
        .filter(|addr| !addr.is_empty())
    {
        for stargate in &mut stargates {
            stargate.grpc_pylon_dial_addr = grpc_pylon_dial_addr.to_string();
        }
    }
    WatchStargatesResponse {
        stargates,
        watch_stargate_urls: remote_watch_stargate_urls.to_vec(),
    }
}

fn normalize_stargates(stargates: Vec<StargateInfo>) -> Vec<StargateInfo> {
    let mut by_advertise_addr: BTreeMap<String, StargateInfo> = BTreeMap::new();
    for stargate in stargates {
        by_advertise_addr
            .entry(stargate.advertise_addr.clone())
            .and_modify(|known| {
                if known.stargate_id.is_empty() || !stargate.stargate_id.is_empty() {
                    known.clone_from(&stargate);
                }
            })
            .or_insert(stargate);
    }

    let mut deduped = BTreeMap::new();
    for stargate in by_advertise_addr.into_values() {
        let key = if stargate.stargate_id.is_empty() {
            stargate.advertise_addr.clone()
        } else {
            stargate.stargate_id.clone()
        };
        deduped.insert(key, stargate);
    }
    deduped.into_values().collect()
}

/// The static remote Watch URLs, already normalized, plus the URLs discovery
/// learned at runtime, normalized the same way. A dynamic URL for the same
/// endpoint as a static one is dropped, so the static spelling wins.
fn union_remote_watch_urls(
    static_urls: &[String],
    dynamic_urls: Vec<String>,
    excluded_endpoint_keys: &BTreeSet<String>,
) -> Vec<String> {
    if dynamic_urls.is_empty() {
        return static_urls.to_vec();
    }
    normalize_remote_watch_urls(
        static_urls.iter().cloned().chain(dynamic_urls),
        excluded_endpoint_keys,
    )
}

fn normalize_remote_watch_urls(
    urls: impl IntoIterator<Item = String>,
    excluded_endpoint_keys: &BTreeSet<String>,
) -> Vec<String> {
    let mut deduped: BTreeMap<String, String> = BTreeMap::new();
    for (rejected_watch_url_index, raw_url) in urls.into_iter().enumerate() {
        let url = match parse_explicit_http_uri(&raw_url) {
            Ok(url) => url,
            Err(error) => {
                warn!(
                    rejected_watch_url_index,
                    %error,
                    "ignoring invalid remote Stargate Watch URI"
                );
                continue;
            }
        };
        let key = watch_endpoint_key(&url).unwrap_or_else(|| url.clone());
        if excluded_endpoint_keys.contains(&key) {
            continue;
        }
        deduped.entry(key).or_insert(url);
    }
    deduped.into_values().collect()
}

fn local_watch_endpoint_keys(
    advertise_addr: SocketAddr,
    discovery_dns_name: &str,
) -> BTreeSet<String> {
    let discovery_dns_name = discovery_dns_name.trim();
    let cluster_service_dns_name = discovery_dns_name.replace("-headless.", ".");
    [
        Some(advertise_addr.to_string()),
        Some(format!("{discovery_dns_name}:{}", advertise_addr.port())),
        (cluster_service_dns_name != discovery_dns_name)
            .then(|| format!("{cluster_service_dns_name}:{}", advertise_addr.port())),
    ]
    .into_iter()
    .flatten()
    .filter_map(|endpoint| watch_endpoint_key(&endpoint))
    .collect()
}

fn watch_endpoint_key(endpoint: &str) -> Option<String> {
    let endpoint = endpoint.trim();
    if endpoint.is_empty() {
        return None;
    }
    let url = if endpoint.starts_with("http://") || endpoint.starts_with("https://") {
        endpoint.to_string()
    } else {
        format!("http://{endpoint}")
    };
    let parsed = Url::parse(&url).ok()?;
    let host = parsed.host_str()?;
    let port = parsed.port_or_known_default()?;
    Some(format!("{host}:{port}"))
}

pub(super) fn watch_stargates_stream_from_receiver(
    mut rx: watch::Receiver<WatchStargatesResponse>,
) -> impl Stream<Item = Result<WatchStargatesResponse, Status>> + Send + 'static {
    let initial = rx.borrow_and_update().clone();
    let pending_initial = watch_response_has_entries(&initial).then_some(initial);
    stream::unfold((rx, pending_initial), |(mut rx, pending)| async move {
        let message = if let Some(message) = pending {
            message
        } else {
            rx.changed().await.ok()?;
            rx.borrow_and_update().clone()
        };
        Some((Ok(message), (rx, None)))
    })
}

fn watch_response_has_entries(response: &WatchStargatesResponse) -> bool {
    !response.stargates.is_empty() || !response.watch_stargate_urls.is_empty()
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;

    use futures::{Stream, StreamExt};
    use parking_lot::Mutex;

    use super::*;

    fn stargate(id: &str, advertise_addr: &str, http_addr: &str) -> StargateInfo {
        StargateInfo {
            stargate_id: id.to_string(),
            advertise_addr: advertise_addr.to_string(),
            http_advertise_addr: http_addr.to_string(),
            grpc_pylon_dial_addr: String::new(),
        }
    }

    #[test]
    fn watch_stargates_response_sorts_and_dedupes_local_and_remote_entries() {
        let remote_watch_urls = normalize_remote_watch_urls(
            vec![
                "https://remote-b.stargate:50071".to_string(),
                "https://remote-a.stargate:50071".to_string(),
                "https://remote-b.stargate:50071".to_string(),
            ],
            &BTreeSet::new(),
        );
        let response = build_watch_stargates_response(
            vec![
                stargate("stargate-1", "10.0.0.2:50071", "10.0.0.2:8000"),
                stargate("stargate-0", "10.0.0.1:50071", "10.0.0.1:8000"),
                stargate("stargate-1", "10.0.0.2:50071", "10.0.0.2:8000"),
            ],
            &remote_watch_urls,
            None,
        );

        let ids: Vec<&str> = response
            .stargates
            .iter()
            .map(|info| info.stargate_id.as_str())
            .collect();
        assert_eq!(ids, vec!["stargate-0", "stargate-1"]);
        assert_eq!(
            response.watch_stargate_urls,
            vec![
                "https://remote-a.stargate:50071",
                "https://remote-b.stargate:50071"
            ]
        );
    }

    #[test]
    fn watch_stargates_response_dedupes_empty_id_by_advertise_addr() {
        let response = build_watch_stargates_response(
            vec![
                stargate("", "10.0.0.1:50071", "10.0.0.1:8000"),
                stargate("stargate-0", "10.0.0.1:50071", "10.0.0.1:8000"),
            ],
            &[],
            None,
        );

        assert_eq!(response.stargates.len(), 1);
        assert_eq!(response.stargates[0].stargate_id, "stargate-0");
        assert_eq!(response.stargates[0].advertise_addr, "10.0.0.1:50071");
    }

    #[test]
    fn watch_stargates_response_applies_configured_grpc_pylon_dial_addr() {
        let response = build_watch_stargates_response(
            vec![stargate("stargate-0", "stargate-0.region-a:50071", "")],
            &[],
            Some(" https://stargate-grpc-lb.region-a:443 "),
        );

        assert_eq!(
            response.stargates[0].grpc_pylon_dial_addr,
            "https://stargate-grpc-lb.region-a:443"
        );
    }

    #[test]
    fn remote_watch_urls_are_normalized_and_filter_self_endpoints() {
        let excluded = local_watch_endpoint_keys(
            "10.0.0.1:50071".parse().unwrap(),
            "stargate-headless.ns.svc.cluster.local",
        );
        let urls = normalize_remote_watch_urls(
            vec![
                " https://remote-b:50071 ".to_string(),
                "http://remote-a:50071".to_string(),
                "https://remote-b:50071".to_string(),
                String::new(),
                "http://10.0.0.1:50071".to_string(),
                "https://stargate-headless.ns.svc.cluster.local:50071".to_string(),
                "http://stargate.ns.svc.cluster.local:50071".to_string(),
            ],
            &excluded,
        );

        assert_eq!(
            urls,
            vec!["http://remote-a:50071", "https://remote-b:50071"]
        );
    }

    #[test]
    fn dynamic_remote_watch_urls_are_unioned_with_the_static_list() {
        let excluded = local_watch_endpoint_keys(
            "10.0.0.1:50071".parse().unwrap(),
            "stargate-headless.ns.svc.cluster.local",
        );
        let static_urls =
            normalize_remote_watch_urls(vec!["https://remote-b:50071".to_string()], &excluded);

        assert_eq!(
            union_remote_watch_urls(&static_urls, Vec::new(), &excluded),
            static_urls
        );
        assert_eq!(
            union_remote_watch_urls(
                &static_urls,
                vec![
                    "http://10.2.0.2:50071".to_string(),
                    // Same endpoint as a static URL: the static spelling wins.
                    "http://remote-b:50071".to_string(),
                    // This Stargate itself.
                    "http://10.0.0.1:50071".to_string(),
                    "not a uri".to_string(),
                    "https://[fd00::1]:50071".to_string(),
                    "http://10.2.0.2:50071".to_string(),
                ],
                &excluded,
            ),
            vec![
                "http://10.2.0.2:50071",
                "https://[fd00::1]:50071",
                "https://remote-b:50071",
            ]
        );
    }

    struct FakeDiscovery {
        stargates: Vec<StargateInfo>,
        remote_watch_urls: Arc<Mutex<Vec<String>>>,
    }

    #[async_trait::async_trait]
    impl Discovery for FakeDiscovery {
        fn initial_stargates(&self) -> Vec<StargateInfo> {
            self.stargates.clone()
        }

        async fn discover_stargates(&self) -> Vec<StargateInfo> {
            self.stargates.clone()
        }

        async fn discover_remote_watch_urls(&self) -> Vec<String> {
            self.remote_watch_urls.lock().clone()
        }
    }

    async fn next_watch_urls(
        rx: &mut watch::Receiver<WatchStargatesResponse>,
        expected: &[&str],
    ) -> WatchStargatesResponse {
        tokio::time::timeout(Duration::from_secs(10), async {
            loop {
                let current = rx.borrow_and_update().clone();
                if current.watch_stargate_urls == expected {
                    return current;
                }
                rx.changed().await.expect("publisher should be running");
            }
        })
        .await
        .unwrap_or_else(|_| panic!("watch_stargate_urls never became {expected:?}"))
    }

    #[tokio::test]
    async fn publisher_unions_dynamic_remote_watch_urls_every_poll_tick() {
        let remote_watch_urls = Arc::new(Mutex::new(vec![
            "http://10.2.0.1:50071".to_string(),
            "http://10.0.0.1:50071".to_string(),
        ]));
        let (tasks, _failures) = CriticalTaskGroup::new("watch-stargates-test");
        let mut rx = spawn_watch_stargates_publisher(WatchStargatesPublisherConfig {
            advertise_addr: "10.0.0.1:50071".parse().unwrap(),
            discovery_dns_name: "stargate-headless.ns.svc.cluster.local".to_string(),
            discovery: Box::new(FakeDiscovery {
                stargates: vec![stargate("stargate-0", "10.0.0.1:50071", "")],
                remote_watch_urls: remote_watch_urls.clone(),
            }),
            remote_watch_stargate_urls: vec!["https://remote-b.stargate:50071".to_string()],
            grpc_pylon_dial_addr: None,
            discovery_poll_interval: Duration::from_millis(10),
            watch_heartbeat_interval: Duration::from_secs(60),
            tasks: tasks.clone(),
        });

        let first = next_watch_urls(
            &mut rx,
            &["http://10.2.0.1:50071", "https://remote-b.stargate:50071"],
        )
        .await;
        assert_eq!(first.stargates.len(), 1);

        remote_watch_urls
            .lock()
            .push("http://10.3.0.1:50071".to_string());
        next_watch_urls(
            &mut rx,
            &[
                "http://10.2.0.1:50071",
                "http://10.3.0.1:50071",
                "https://remote-b.stargate:50071",
            ],
        )
        .await;

        remote_watch_urls.lock().clear();
        next_watch_urls(&mut rx, &["https://remote-b.stargate:50071"]).await;

        tasks.begin_shutdown();
        tasks.wait().await;
    }

    #[test]
    fn watch_response_has_entries_only_when_local_or_remote_targets_exist() {
        assert!(!watch_response_has_entries(
            &WatchStargatesResponse::default()
        ));
        assert!(watch_response_has_entries(&WatchStargatesResponse {
            stargates: vec![stargate("stargate-0", "10.0.0.1:50071", "10.0.0.1:8000",)],
            watch_stargate_urls: Vec::new(),
        }));
        assert!(watch_response_has_entries(&WatchStargatesResponse {
            stargates: Vec::new(),
            watch_stargate_urls: vec!["remote-a:50071".to_string()],
        }));
    }

    #[tokio::test]
    async fn watch_stargates_stream_marks_initial_snapshot_seen() {
        let (tx, rx) = watch::channel(WatchStargatesResponse::default());
        let first = WatchStargatesResponse {
            stargates: vec![stargate("stargate-0", "10.0.0.1:50071", "10.0.0.1:8000")],
            watch_stargate_urls: Vec::new(),
        };
        tx.send(first.clone()).expect("receiver should be alive");
        let mut out = Box::pin(watch_stargates_stream_from_receiver(rx));

        assert_eq!(out.next().await.unwrap().unwrap(), first);

        let waker = futures::task::noop_waker_ref();
        let mut cx = std::task::Context::from_waker(waker);
        assert!(matches!(
            out.as_mut().poll_next(&mut cx),
            std::task::Poll::Pending
        ));

        let second = WatchStargatesResponse {
            stargates: vec![stargate("stargate-1", "10.0.0.2:50071", "10.0.0.2:8000")],
            watch_stargate_urls: Vec::new(),
        };
        tx.send(second.clone()).expect("receiver should be alive");

        assert_eq!(out.next().await.unwrap().unwrap(), second);
    }
}

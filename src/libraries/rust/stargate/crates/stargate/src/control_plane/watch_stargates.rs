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
use tracing::debug;
use url::Url;

use stargate_proto::pb::{StargateInfo, WatchStargatesResponse};

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
                stargates = discovery.discover_stargates() => {
                    build_watch_stargates_response(
                        stargates,
                        &remote_watch_stargate_urls,
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

fn normalize_remote_watch_urls(
    urls: Vec<String>,
    excluded_endpoint_keys: &BTreeSet<String>,
) -> Vec<String> {
    let mut deduped: BTreeMap<String, String> = BTreeMap::new();
    for raw_url in urls {
        let url = raw_url.trim().to_string();
        if url.is_empty() {
            continue;
        }
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
    use futures::{Stream, StreamExt};

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
                "remote-b.stargate:50071".to_string(),
                "remote-a.stargate:50071".to_string(),
                "remote-b.stargate:50071".to_string(),
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
            vec!["remote-a.stargate:50071", "remote-b.stargate:50071"]
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
            Some(" stargate-grpc-lb.region-a:443 "),
        );

        assert_eq!(
            response.stargates[0].grpc_pylon_dial_addr,
            "stargate-grpc-lb.region-a:443"
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
                " remote-b:50071 ".to_string(),
                "remote-a:50071".to_string(),
                "remote-b:50071".to_string(),
                String::new(),
                "10.0.0.1:50071".to_string(),
                "http://10.0.0.1:50071".to_string(),
                "stargate-headless.ns.svc.cluster.local:50071".to_string(),
                "stargate.ns.svc.cluster.local:50071".to_string(),
            ],
            &excluded,
        );

        assert_eq!(urls, vec!["remote-a:50071", "remote-b:50071"]);
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

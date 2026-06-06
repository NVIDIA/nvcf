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

use std::collections::{BTreeSet, HashMap};
use std::time::{Duration, Instant};

use anyhow::Context;
use tokio::sync::{mpsc, watch};
use tokio::task::JoinHandle;

use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::{WatchStargatesRequest, WatchStargatesResponse};

use super::grpc_endpoint::{
    StargateGrpcConnectTarget, StargateGrpcEndpoint, log_stargate_grpc_connect_attempt,
    stargate_grpc_channel_endpoint,
};
use super::{
    NamedJoinHandle, REGISTRATION_TASK_SHUTDOWN_TIMEOUT, await_named_join_handle, normalize_addr,
    should_stop, stop_channel_changed,
};

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(super) struct WatchEndpointSnapshot {
    pub(super) registration_routers: BTreeSet<StargateGrpcEndpoint>,
    pub(super) watch_urls: BTreeSet<String>,
}

#[derive(Debug)]
pub(super) enum WatchEndpointEvent {
    Snapshot(WatchEndpointSnapshot),
    Disconnected,
}

#[derive(Debug)]
pub(super) struct WatchEndpointUpdate {
    pub(super) watch_url: String,
    pub(super) generation: u64,
    pub(super) event: WatchEndpointEvent,
}

#[derive(Debug)]
pub(super) enum WatchEndpointState {
    Connecting,
    Live(WatchEndpointSnapshot),
    Disconnected,
}

impl WatchEndpointState {
    pub(super) fn snapshot(&self) -> Option<&WatchEndpointSnapshot> {
        match self {
            WatchEndpointState::Live(snapshot) => Some(snapshot),
            WatchEndpointState::Connecting | WatchEndpointState::Disconnected => None,
        }
    }

    pub(super) fn has_snapshot(&self) -> bool {
        matches!(self, WatchEndpointState::Live(_))
    }
}

pub(super) struct WatchedEndpoint {
    pub(super) generation: u64,
    pub(super) stop_tx: watch::Sender<bool>,
    pub(super) task: JoinHandle<()>,
    pub(super) state: WatchEndpointState,
}

const INITIAL_WATCH_DISCOVERY_TIMEOUT: Duration = Duration::from_secs(5);

pub(super) async fn run_watch_stargate_discovery(
    seeds: Vec<String>,
    stargate_updates_tx: mpsc::Sender<BTreeSet<StargateGrpcEndpoint>>,
    mut stop_rx: watch::Receiver<bool>,
) {
    let seeds = normalize_string_set(seeds);
    let (endpoint_updates_tx, mut endpoint_updates_rx) = mpsc::channel::<WatchEndpointUpdate>(32);
    let mut watched: HashMap<String, WatchedEndpoint> = HashMap::new();
    let mut next_generation = 0_u64;
    let mut last_published = BTreeSet::new();
    let mut has_published = false;
    let initial_discovery_started_at = Instant::now();

    loop {
        if *stop_rx.borrow() {
            break;
        }

        let desired_watch_urls = desired_watch_urls(&seeds, &watched);
        let current_watch_urls: Vec<String> = watched.keys().cloned().collect();
        for watch_url in current_watch_urls {
            if desired_watch_urls.contains(&watch_url) {
                continue;
            }
            if let Some(endpoint) = watched.remove(&watch_url) {
                stop_watched_endpoint(endpoint).await;
            }
        }

        for watch_url in &desired_watch_urls {
            if watched.contains_key(watch_url) {
                continue;
            }
            let generation = next_generation;
            next_generation = next_generation
                .checked_add(1)
                .expect("watch endpoint generation counter overflowed");
            let (endpoint_stop_tx, endpoint_stop_rx) = watch::channel(false);
            let task = tokio::spawn(watch_stargate_endpoint(
                watch_url.clone(),
                generation,
                endpoint_updates_tx.clone(),
                stop_rx.clone(),
                endpoint_stop_rx,
            ));
            watched.insert(
                watch_url.clone(),
                WatchedEndpoint {
                    generation,
                    stop_tx: endpoint_stop_tx,
                    task,
                    state: WatchEndpointState::Connecting,
                },
            );
        }

        let active_routers = active_registration_routers(watched_endpoint_snapshots(&watched));
        let snapshots_complete =
            all_desired_watch_urls_have_snapshots(&desired_watch_urls, |watch_url| {
                watched
                    .get(watch_url)
                    .is_some_and(|endpoint| endpoint.state.has_snapshot())
            });
        if should_publish_watch_routers(
            &active_routers,
            &last_published,
            snapshots_complete,
            initial_discovery_started_at.elapsed() >= INITIAL_WATCH_DISCOVERY_TIMEOUT,
            has_published,
        ) {
            if stargate_updates_tx
                .send(active_routers.clone())
                .await
                .is_err()
            {
                break;
            }
            last_published = active_routers;
            has_published = true;
        }

        tokio::select! {
            maybe_update = endpoint_updates_rx.recv() => {
                match maybe_update {
                    Some(update) => {
                        apply_watch_endpoint_update(&mut watched, update);
                    }
                    None => break,
                }
            }
            _ = stop_rx.changed() => {
                if *stop_rx.borrow() {
                    break;
                }
            }
            _ = tokio::time::sleep(Duration::from_millis(100)) => {}
        }
    }

    for (_, endpoint) in watched {
        stop_watched_endpoint(endpoint).await;
    }
}

pub(super) async fn stop_watched_endpoint(endpoint: WatchedEndpoint) {
    let _ = endpoint.stop_tx.send(true);
    await_named_join_handle(
        NamedJoinHandle::new("watch stargate endpoint", endpoint.task),
        REGISTRATION_TASK_SHUTDOWN_TIMEOUT,
    )
    .await;
}

async fn watch_stargate_endpoint(
    watch_url: String,
    generation: u64,
    endpoint_updates_tx: mpsc::Sender<WatchEndpointUpdate>,
    mut stop_rx: watch::Receiver<bool>,
    mut endpoint_stop_rx: watch::Receiver<bool>,
) {
    loop {
        if should_stop(&stop_rx, &endpoint_stop_rx) {
            return;
        }

        let target = StargateGrpcConnectTarget::direct(normalize_addr(&watch_url));
        log_stargate_grpc_connect_attempt(&target, "watch_stargates", "lazy");
        let channel = stargate_grpc_channel_endpoint(&target)
            .context("invalid watch endpoint")
            .map(|endpoint| endpoint.connect_lazy());
        let Ok(channel) = channel else {
            if watch_endpoint_sleep_or_stop(
                &mut stop_rx,
                &mut endpoint_stop_rx,
                Duration::from_secs(1),
            )
            .await
            {
                return;
            }
            continue;
        };
        let mut client = StargateControlPlaneClient::new(channel);
        let response = tokio::select! {
            response = client.watch_stargates(WatchStargatesRequest {}) => response,
            changed = stop_rx.changed() => {
                if stop_channel_changed(changed, &stop_rx) || should_stop(&stop_rx, &endpoint_stop_rx) {
                    return;
                }
                continue;
            }
            changed = endpoint_stop_rx.changed() => {
                if stop_channel_changed(changed, &endpoint_stop_rx) || should_stop(&stop_rx, &endpoint_stop_rx) {
                    return;
                }
                continue;
            }
        };
        let Ok(response) = response else {
            if watch_endpoint_sleep_or_stop(
                &mut stop_rx,
                &mut endpoint_stop_rx,
                Duration::from_secs(1),
            )
            .await
            {
                return;
            }
            continue;
        };
        let mut stream = response.into_inner();

        loop {
            tokio::select! {
                message = stream.message() => {
                    match message {
                        Ok(Some(event)) => {
                            let update = WatchEndpointUpdate {
                                watch_url: watch_url.clone(),
                                generation,
                                event: WatchEndpointEvent::Snapshot(
                                    watch_endpoint_snapshot_from_response(&watch_url, event),
                                ),
                            };
                            if !send_watch_endpoint_update(
                                &endpoint_updates_tx,
                                update,
                                &mut stop_rx,
                                &mut endpoint_stop_rx,
                            )
                            .await
                            {
                                return;
                            }
                        }
                        Ok(None) | Err(_) => {
                            let update = WatchEndpointUpdate {
                                watch_url: watch_url.clone(),
                                generation,
                                event: WatchEndpointEvent::Disconnected,
                            };
                            if !send_watch_endpoint_update(
                                &endpoint_updates_tx,
                                update,
                                &mut stop_rx,
                                &mut endpoint_stop_rx,
                            )
                            .await
                            {
                                return;
                            }
                            break;
                        }
                    }
                }
                changed = stop_rx.changed() => {
                    if stop_channel_changed(changed, &stop_rx)
                        || should_stop(&stop_rx, &endpoint_stop_rx)
                    {
                        return;
                    }
                }
                changed = endpoint_stop_rx.changed() => {
                    if stop_channel_changed(changed, &endpoint_stop_rx)
                        || should_stop(&stop_rx, &endpoint_stop_rx)
                    {
                        return;
                    }
                }
            }
        }

        if watch_endpoint_sleep_or_stop(&mut stop_rx, &mut endpoint_stop_rx, Duration::from_secs(1))
            .await
        {
            return;
        }
    }
}

pub(super) async fn send_watch_endpoint_update(
    endpoint_updates_tx: &mpsc::Sender<WatchEndpointUpdate>,
    update: WatchEndpointUpdate,
    parent_stop_rx: &mut watch::Receiver<bool>,
    endpoint_stop_rx: &mut watch::Receiver<bool>,
) -> bool {
    loop {
        let permit = tokio::select! {
            permit = endpoint_updates_tx.reserve() => match permit {
                Ok(permit) => permit,
                Err(_) => return false,
            },
            changed = parent_stop_rx.changed() => {
                if stop_channel_changed(changed, parent_stop_rx)
                    || should_stop(parent_stop_rx, endpoint_stop_rx)
                {
                    return false;
                }
                continue;
            }
            changed = endpoint_stop_rx.changed() => {
                if stop_channel_changed(changed, endpoint_stop_rx)
                    || should_stop(parent_stop_rx, endpoint_stop_rx)
                {
                    return false;
                }
                continue;
            }
        };
        permit.send(update);
        return true;
    }
}

pub(super) fn apply_watch_endpoint_update(
    watched: &mut HashMap<String, WatchedEndpoint>,
    update: WatchEndpointUpdate,
) -> bool {
    let Some(endpoint) = watched.get_mut(&update.watch_url) else {
        return false;
    };
    if endpoint.generation != update.generation {
        return false;
    }
    endpoint.state = match update.event {
        WatchEndpointEvent::Snapshot(snapshot) => WatchEndpointState::Live(snapshot),
        WatchEndpointEvent::Disconnected => WatchEndpointState::Disconnected,
    };
    true
}

pub(super) fn should_publish_watch_routers(
    active_routers: &BTreeSet<StargateGrpcEndpoint>,
    last_published: &BTreeSet<StargateGrpcEndpoint>,
    snapshots_complete: bool,
    initial_discovery_timed_out: bool,
    has_published: bool,
) -> bool {
    // The normal initial publish waits for recursive discovery to complete, but
    // a bad redundant seed must not block registration to already discovered routers.
    let initial_publish_ready =
        snapshots_complete || (initial_discovery_timed_out && !active_routers.is_empty());
    // After the first publish, losing a watch stream is itself a router-removal update.
    (initial_publish_ready || has_published) && active_routers != last_published
}

pub(super) fn watch_endpoint_snapshot_from_response(
    _watch_url: &str,
    response: WatchStargatesResponse,
) -> WatchEndpointSnapshot {
    WatchEndpointSnapshot {
        registration_routers: response
            .stargates
            .into_iter()
            .filter_map(stargate_info_registration_endpoint)
            .collect(),
        watch_urls: normalize_string_set(response.watch_stargate_urls),
    }
}

fn stargate_info_registration_endpoint(
    info: stargate_proto::pb::StargateInfo,
) -> Option<StargateGrpcEndpoint> {
    let authority_addr = if !info.advertise_addr.trim().is_empty() {
        info.advertise_addr
    } else {
        info.stargate_id
    };
    StargateGrpcEndpoint::new(authority_addr, info.grpc_pylon_dial_addr)
}

fn desired_watch_urls(
    seeds: &BTreeSet<String>,
    watched: &HashMap<String, WatchedEndpoint>,
) -> BTreeSet<String> {
    desired_watch_urls_from_snapshot_lookup(seeds, |watch_url| {
        watched
            .get(watch_url)
            .and_then(|endpoint| endpoint.state.snapshot())
    })
}

#[cfg(test)]
pub(super) fn desired_watch_urls_from_snapshots(
    seeds: &BTreeSet<String>,
    snapshots: &HashMap<String, WatchEndpointSnapshot>,
) -> BTreeSet<String> {
    desired_watch_urls_from_snapshot_lookup(seeds, |watch_url| snapshots.get(watch_url))
}

fn desired_watch_urls_from_snapshot_lookup<'a>(
    seeds: &BTreeSet<String>,
    mut snapshot_for_watch_url: impl FnMut(&str) -> Option<&'a WatchEndpointSnapshot>,
) -> BTreeSet<String> {
    let mut desired = seeds.clone();
    let mut pending: Vec<String> = seeds.iter().cloned().collect();
    while let Some(watch_url) = pending.pop() {
        let Some(snapshot) = snapshot_for_watch_url(&watch_url) else {
            continue;
        };
        for next_watch_url in &snapshot.watch_urls {
            if desired.insert(next_watch_url.clone()) {
                pending.push(next_watch_url.clone());
            }
        }
    }
    desired
}

pub(super) fn active_registration_routers<'a>(
    snapshots: impl IntoIterator<Item = &'a WatchEndpointSnapshot>,
) -> BTreeSet<StargateGrpcEndpoint> {
    snapshots
        .into_iter()
        .flat_map(|snapshot| snapshot.registration_routers.iter().cloned())
        .collect()
}

pub(super) fn all_desired_watch_urls_have_snapshots(
    desired_watch_urls: &BTreeSet<String>,
    has_snapshot: impl Fn(&str) -> bool,
) -> bool {
    desired_watch_urls
        .iter()
        .all(|watch_url| has_snapshot(watch_url))
}

pub(super) fn watched_endpoint_snapshots(
    watched: &HashMap<String, WatchedEndpoint>,
) -> impl Iterator<Item = &WatchEndpointSnapshot> {
    watched
        .values()
        .filter_map(|endpoint| endpoint.state.snapshot())
}

fn normalize_string_set(values: Vec<String>) -> BTreeSet<String> {
    values
        .into_iter()
        .map(|value| value.trim().to_string())
        .filter(|value| !value.is_empty())
        .collect()
}

async fn watch_endpoint_sleep_or_stop(
    parent_stop_rx: &mut watch::Receiver<bool>,
    endpoint_stop_rx: &mut watch::Receiver<bool>,
    duration: Duration,
) -> bool {
    tokio::select! {
        changed = parent_stop_rx.changed() => {
            stop_channel_changed(changed, parent_stop_rx)
                || should_stop(parent_stop_rx, endpoint_stop_rx)
        }
        changed = endpoint_stop_rx.changed() => {
            stop_channel_changed(changed, endpoint_stop_rx)
                || should_stop(parent_stop_rx, endpoint_stop_rx)
        }
        _ = tokio::time::sleep(duration) => should_stop(parent_stop_rx, endpoint_stop_rx),
    }
}

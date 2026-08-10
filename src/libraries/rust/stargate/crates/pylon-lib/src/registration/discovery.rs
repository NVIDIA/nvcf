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

use std::collections::hash_map::Entry;
use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::time::Duration;

use tokio::sync::{mpsc, watch};
use tokio_util::sync::CancellationToken;

use stargate_proto::pb::stargate_control_plane_client::StargateControlPlaneClient;
use stargate_proto::pb::{WatchStargatesRequest, WatchStargatesResponse};

use stargate_runtime::{OwnedTask, TASK_SHUTDOWN_TIMEOUT};

use super::grpc_endpoint::{StargateGrpcEndpoint, log_stargate_grpc_connect_attempt};
use super::topology::{RegistrationRouterTopology, publish_registration_router_topology};

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub(super) struct WatchEndpointSnapshot {
    pub(super) registration_routers: BTreeMap<String, StargateGrpcEndpoint>,
    pub(super) watch_urls: BTreeSet<String>,
}

#[derive(Debug)]
pub(super) struct WatchEndpointUpdate {
    pub(super) watch_url: String,
    pub(super) generation: u64,
    pub(super) snapshot: Option<WatchEndpointSnapshot>,
}

pub(super) struct WatchedEndpoint {
    pub(super) generation: u64,
    pub(super) task: OwnedTask,
    pub(super) snapshot: Option<WatchEndpointSnapshot>,
}

const INITIAL_WATCH_DISCOVERY_TIMEOUT: Duration = Duration::from_secs(5);

pub(super) async fn run_watch_stargate_discovery(
    seeds: Vec<String>,
    topology_tx: watch::Sender<RegistrationRouterTopology>,
    stop: CancellationToken,
) {
    let seeds = normalize_string_set(seeds);
    let (endpoint_updates_tx, mut endpoint_updates_rx) = mpsc::channel::<WatchEndpointUpdate>(32);
    let mut watched: HashMap<String, WatchedEndpoint> = HashMap::new();
    let mut next_generation = 0_u64;
    let initial_discovery_timeout = tokio::time::sleep(INITIAL_WATCH_DISCOVERY_TIMEOUT);
    tokio::pin!(initial_discovery_timeout);
    let mut initial_discovery_timed_out = false;

    loop {
        if stop.is_cancelled() {
            break;
        }

        let desired_watch_urls = desired_watch_urls_from_snapshot_lookup(&seeds, |watch_url| {
            watched
                .get(watch_url)
                .and_then(|endpoint| endpoint.snapshot.as_ref())
        });
        let removed_endpoints = watched
            .extract_if(|watch_url, _| !desired_watch_urls.contains(watch_url))
            .map(|(_, endpoint)| endpoint)
            .collect::<Vec<_>>();
        for endpoint in removed_endpoints {
            stop_watched_endpoint(endpoint).await;
        }

        for watch_url in &desired_watch_urls {
            let Entry::Vacant(entry) = watched.entry(watch_url.clone()) else {
                continue;
            };
            let generation = next_generation;
            next_generation = next_generation
                .checked_add(1)
                .expect("watch endpoint generation counter overflowed");
            let task = OwnedTask::spawn_child("watch stargate endpoint", &stop, {
                let watch_url = watch_url.clone();
                let endpoint_updates_tx = endpoint_updates_tx.clone();
                move |endpoint_stop| {
                    watch_stargate_endpoint(
                        watch_url,
                        generation,
                        endpoint_updates_tx,
                        endpoint_stop,
                    )
                }
            });
            entry.insert(WatchedEndpoint {
                generation,
                task,
                snapshot: None,
            });
        }

        let active_routers = active_registration_routers(watched_endpoint_snapshots(&watched));
        let snapshots_complete =
            all_desired_watch_urls_have_snapshots(&desired_watch_urls, |watch_url| {
                watched
                    .get(watch_url)
                    .is_some_and(|endpoint| endpoint.snapshot.is_some())
            });
        // A bad redundant seed must not block registration to already discovered routers.
        let initial_publish_ready =
            snapshots_complete || (initial_discovery_timed_out && !active_routers.is_empty());
        publish_registration_router_topology(&topology_tx, &active_routers, initial_publish_ready);
        let awaiting_initial_timeout =
            !initial_discovery_timed_out && topology_tx.borrow().published_routers().is_none();

        tokio::select! {
            Some(update) = endpoint_updates_rx.recv() => {
                apply_watch_endpoint_update(&mut watched, update);
            }
            _ = stop.cancelled() => break,
            _ = &mut initial_discovery_timeout, if awaiting_initial_timeout => {
                initial_discovery_timed_out = true;
            }
        }
    }

    let tasks = watched
        .into_values()
        .map(|endpoint| endpoint.task)
        .collect();
    OwnedTask::shutdown_all(tasks, TASK_SHUTDOWN_TIMEOUT).await;
}

pub(super) async fn stop_watched_endpoint(endpoint: WatchedEndpoint) {
    endpoint.task.shutdown(TASK_SHUTDOWN_TIMEOUT).await;
}

async fn watch_stargate_endpoint(
    watch_url: String,
    generation: u64,
    endpoint_updates_tx: mpsc::Sender<WatchEndpointUpdate>,
    stop: CancellationToken,
) {
    let target = StargateGrpcEndpoint::new(watch_url.clone(), "")
        .expect("normalized watch URL should be non-empty");
    loop {
        if stop.is_cancelled() {
            return;
        }

        log_stargate_grpc_connect_attempt(&target, "watch_stargates", "lazy");
        let stream = match target.channel_endpoint() {
            Ok(endpoint) => {
                let mut client = StargateControlPlaneClient::new(endpoint.connect_lazy());
                tokio::select! {
                    response = client.watch_stargates(WatchStargatesRequest {}) => {
                        response.ok().map(|response| response.into_inner())
                    }
                    _ = stop.cancelled() => return,
                }
            }
            Err(_) => None,
        };
        if let Some(mut stream) = stream {
            loop {
                let Some(message) = stop.run_until_cancelled(stream.message()).await else {
                    return;
                };
                let snapshot = message
                    .ok()
                    .flatten()
                    .map(|response| watch_endpoint_snapshot_from_response(&watch_url, response));
                let disconnected = snapshot.is_none();
                if !send_watch_endpoint_update(
                    &endpoint_updates_tx,
                    WatchEndpointUpdate {
                        watch_url: watch_url.clone(),
                        generation,
                        snapshot,
                    },
                    &stop,
                )
                .await
                {
                    return;
                }
                if disconnected {
                    break;
                }
            }
        }

        if stop
            .run_until_cancelled(tokio::time::sleep(Duration::from_secs(1)))
            .await
            .is_none()
        {
            return;
        }
    }
}

pub(super) async fn send_watch_endpoint_update(
    endpoint_updates_tx: &mpsc::Sender<WatchEndpointUpdate>,
    update: WatchEndpointUpdate,
    stop: &CancellationToken,
) -> bool {
    matches!(
        stop.run_until_cancelled(endpoint_updates_tx.send(update))
            .await,
        Some(Ok(()))
    )
}

pub(super) fn apply_watch_endpoint_update(
    watched: &mut HashMap<String, WatchedEndpoint>,
    update: WatchEndpointUpdate,
) -> bool {
    let Some(endpoint) = watched
        .get_mut(&update.watch_url)
        .filter(|endpoint| endpoint.generation == update.generation)
    else {
        return false;
    };
    endpoint.snapshot = update.snapshot;
    true
}

pub(super) fn watch_endpoint_snapshot_from_response(
    _watch_url: &str,
    response: WatchStargatesResponse,
) -> WatchEndpointSnapshot {
    WatchEndpointSnapshot {
        registration_routers: response
            .stargates
            .into_iter()
            .filter_map(stargate_info_registration_router)
            .collect(),
        watch_urls: normalize_string_set(response.watch_stargate_urls),
    }
}

fn stargate_info_registration_router(
    info: stargate_proto::pb::StargateInfo,
) -> Option<(String, StargateGrpcEndpoint)> {
    let stargate_id = if info.stargate_id.trim().is_empty() {
        info.advertise_addr.trim().to_string()
    } else {
        info.stargate_id.trim().to_string()
    };
    let authority_addr = if !info.advertise_addr.trim().is_empty() {
        info.advertise_addr
    } else {
        stargate_id.clone()
    };
    StargateGrpcEndpoint::new(authority_addr, info.grpc_pylon_dial_addr)
        .map(|endpoint| (stargate_id, endpoint))
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
        .flat_map(|snapshot| snapshot.registration_routers.values().cloned())
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
        .filter_map(|endpoint| endpoint.snapshot.as_ref())
}

fn normalize_string_set(values: Vec<String>) -> BTreeSet<String> {
    values
        .into_iter()
        .map(|value| value.trim().to_string())
        .filter(|value| !value.is_empty())
        .collect()
}

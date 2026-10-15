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

use anyhow::Result;
use futures::StreamExt;
use k8s_openapi::api::discovery::v1::EndpointSlice;
use kube::runtime::watcher::{self, Event};
use kube::{Api, Client, ResourceExt};
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;
use tracing::{debug, error, info};

use crate::endpoints::{
    ENDPOINT_SLICE_SERVICE_NAME_LABEL, TargetBuildConfig, TargetSnapshot,
    snapshot_from_slices as snapshot,
};

pub async fn run_endpoint_slice_watcher(
    client: Client,
    namespace: String,
    build_config: TargetBuildConfig,
    tx: watch::Sender<TargetSnapshot>,
    shutdown: CancellationToken,
) -> Result<()> {
    let selector = format!(
        "{}={}",
        ENDPOINT_SLICE_SERVICE_NAME_LABEL, build_config.service_name
    );
    let mut events = Box::pin(watcher::watcher(
        Api::<EndpointSlice>::namespaced(client, &namespace),
        watcher::Config::default().labels(&selector),
    ));
    let mut state = WatcherState::new(build_config);

    loop {
        tokio::select! {
            _ = shutdown.cancelled() => {
                info!("endpoint slice watcher shutting down");
                return Ok(());
            }
            event = events.next() => match event {
                None => {
                    // A clean stream end means the watcher task can no longer
                    // receive updates. The parent treats this task exit as
                    // fatal so Kubernetes restarts the router instead of
                    // serving stale targets.
                    error!("endpoint slice watcher stream ended");
                    return Ok(());
                }
                Some(Ok(event)) => {
                    if let Some(snapshot) = state.apply(event) {
                        debug!(
                            ready_target_count = snapshot.ready_count(),
                            "publishing EndpointSlice target snapshot"
                        );
                        let _ = tx.send(snapshot);
                    }
                }
                Some(Err(error)) => {
                    error!(%error, "endpoint slice watcher error; kube watcher will recover on next poll");
                }
            },
        }
    }
}

struct WatcherState {
    store: BTreeMap<String, EndpointSlice>,
    init_store: BTreeMap<String, EndpointSlice>,
    build_config: TargetBuildConfig,
}

impl WatcherState {
    fn new(build_config: TargetBuildConfig) -> Self {
        Self {
            store: BTreeMap::new(),
            init_store: BTreeMap::new(),
            build_config,
        }
    }

    fn apply(&mut self, event: Event<EndpointSlice>) -> Option<TargetSnapshot> {
        match event {
            Event::Init => {
                self.init_store.clear();
                return None;
            }
            Event::InitApply(slice) => {
                self.init_store.insert(slice_key(&slice), slice);
                return None;
            }
            Event::InitDone => {
                self.store = std::mem::take(&mut self.init_store);
            }
            Event::Apply(slice) => {
                self.store.insert(slice_key(&slice), slice);
            }
            Event::Delete(slice) => {
                self.store.remove(&slice_key(&slice));
            }
        }
        Some(snapshot(self.store.values(), &self.build_config))
    }
}

fn slice_key(slice: &EndpointSlice) -> String {
    let namespace = slice.namespace().unwrap_or_default();
    format!("{namespace}/{}", slice.name_any())
}

#[cfg(test)]
mod tests {
    use super::*;
    use k8s_openapi::api::core::v1::ObjectReference;
    use k8s_openapi::api::discovery::v1::{Endpoint, EndpointPort};
    use k8s_openapi::apimachinery::pkg::apis::meta::v1::ObjectMeta;

    fn config() -> TargetBuildConfig {
        TargetBuildConfig {
            service_name: "stargate-headless".to_string(),
            grpc_port_name: "grpc".to_string(),
            quic_port_name: "quic".to_string(),
        }
    }

    fn port(name: &str, port: i32, protocol: &str) -> EndpointPort {
        EndpointPort {
            name: Some(name.to_string()),
            port: Some(port),
            protocol: Some(protocol.to_string()),
            ..EndpointPort::default()
        }
    }

    fn slice(name: &str, pod_name: &str, ip: &str) -> EndpointSlice {
        EndpointSlice {
            address_type: "IPv4".to_string(),
            metadata: ObjectMeta {
                namespace: Some("stargate-local".to_string()),
                name: Some(name.to_string()),
                labels: Some(BTreeMap::from([(
                    ENDPOINT_SLICE_SERVICE_NAME_LABEL.to_string(),
                    "stargate-headless".to_string(),
                )])),
                ..ObjectMeta::default()
            },
            ports: Some(vec![port("grpc", 50071, "TCP"), port("quic", 50072, "UDP")]),
            endpoints: vec![Endpoint {
                addresses: vec![ip.to_string()],
                target_ref: Some(ObjectReference {
                    kind: Some("Pod".to_string()),
                    name: Some(pod_name.to_string()),
                    ..ObjectReference::default()
                }),
                ..Endpoint::default()
            }],
        }
    }

    #[test]
    fn init_apply_does_not_publish_before_init_done() {
        let mut state = WatcherState::new(config());

        assert!(state.apply(Event::Init).is_none());
        let initial = slice("slice-a", "stargate-0", "10.0.0.10");
        assert!(state.apply(Event::InitApply(initial)).is_none());

        let snapshot = state
            .apply(Event::InitDone)
            .expect("InitDone should publish a snapshot");
        assert_eq!(snapshot.ready_count(), 1);
        assert!(snapshot.target_for_pod("stargate-0").is_some());
    }

    #[test]
    fn delete_unknown_slice_keeps_existing_targets() {
        let mut state = WatcherState::new(config());
        state
            .apply(Event::Apply(slice("slice-a", "stargate-0", "10.0.0.10")))
            .expect("Apply should publish a snapshot");

        let missing = slice("slice-missing", "stargate-1", "10.0.0.11");
        let snapshot = state
            .apply(Event::Delete(missing))
            .expect("Delete should publish a snapshot");

        assert_eq!(snapshot.ready_count(), 1);
        assert!(snapshot.target_for_pod("stargate-0").is_some());
        assert!(snapshot.target_for_pod("stargate-1").is_none());
    }

    #[test]
    fn reinit_replaces_previous_store() {
        let mut state = WatcherState::new(config());
        state
            .apply(Event::Apply(slice("slice-old", "stargate-0", "10.0.0.10")))
            .expect("Apply should publish a snapshot");

        assert!(state.apply(Event::Init).is_none());
        let replacement = slice("slice-new", "stargate-1", "10.0.0.11");
        assert!(state.apply(Event::InitApply(replacement)).is_none());
        let snapshot = state
            .apply(Event::InitDone)
            .expect("InitDone should publish a snapshot");

        assert_eq!(snapshot.ready_count(), 1);
        assert!(snapshot.target_for_pod("stargate-0").is_none());
        assert!(snapshot.target_for_pod("stargate-1").is_some());
    }
}

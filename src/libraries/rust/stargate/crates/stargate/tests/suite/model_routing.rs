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

use std::sync::{Arc, Mutex};
use std::time::Duration;

use crate::common::{
    TunnelDirection, assert_model_routing, bind_ephemeral_udp, init_crypto, make_stargate_runtime,
    make_stargate_runtime_with_reverse, make_stargate_runtime_with_shared_discovery,
    make_stargate_runtime_with_shared_discovery_and_remote_watch_urls,
    make_stargate_runtime_with_shared_discovery_and_reverse, start_and_register_backend,
    wait_for_routing,
};
use stargate::runtime::StargateHandle;

const ROUTES: &[(&str, &str)] = &[
    ("model-alpha", "backend-alpha"),
    ("model-beta", "backend-beta"),
];
const MULTI_FORWARD_NODES: &[&str] = &["test-sg-mfwd-1", "test-sg-mfwd-2"];
const MULTI_REVERSE_NODES: &[&str] = &["test-sg-mrev-1", "test-sg-mrev-2"];

struct GlobalWatchNode {
    grpc_addr: std::net::SocketAddr,
    http_addr: std::net::SocketAddr,
    handle: Option<StargateHandle>,
}

impl GlobalWatchNode {
    async fn start(
        id: &str,
        peers: Arc<Mutex<Vec<stargate_proto::pb::StargateInfo>>>,
        remote: Option<std::net::SocketAddr>,
    ) -> Self {
        let (grpc_addr, http_addr, runtime) =
            make_stargate_runtime_with_shared_discovery_and_remote_watch_urls(
                id,
                peers,
                remote.into_iter().map(|addr| addr.to_string()).collect(),
            );
        Self {
            grpc_addr,
            http_addr,
            handle: Some(runtime.start().await.expect("global-watch stargate failed")),
        }
    }

    fn begin_shutdown(&self) {
        let handle = self
            .handle
            .as_ref()
            .expect("global-watch node already stopped");
        handle.begin_shutdown();
    }

    async fn wait_for_shutdown(&mut self) {
        let handle = self
            .handle
            .take()
            .expect("global-watch node already stopped");
        assert!(handle.wait_for_shutdown(Duration::from_secs(5)).await);
    }
}

async fn assert_two_model_routes(
    seed_addr: std::net::SocketAddr,
    http_addrs: &[std::net::SocketAddr],
    reverse_tunnel: bool,
    timeout: Duration,
) {
    let seeds = vec![seed_addr.to_string()];
    let mut alpha =
        start_and_register_backend(&seeds, "backend-alpha", "model-alpha", reverse_tunnel).await;
    let mut beta =
        start_and_register_backend(&seeds, "backend-beta", "model-beta", reverse_tunnel).await;
    for &http_addr in http_addrs {
        for &(model, _) in ROUTES {
            wait_for_routing(http_addr, model, timeout).await;
        }
    }
    assert_model_routing(http_addrs, ROUTES, 3).await;
    alpha.stop();
    beta.stop();
}

async fn run_two_model_case(node_ids: &[&str], tunnel: TunnelDirection, timeout_secs: u64) {
    init_crypto();
    let reverse_tunnel = matches!(tunnel, TunnelDirection::Reverse);
    let peers = (node_ids.len() > 1).then(|| Arc::new(Mutex::new(Vec::new())));
    let mut nodes = Vec::with_capacity(node_ids.len());
    for id in node_ids {
        let runtime = match (&peers, reverse_tunnel) {
            (None, false) => make_stargate_runtime(id),
            (None, true) => {
                let (addr, socket) = bind_ephemeral_udp();
                make_stargate_runtime_with_reverse(id, addr, Some(socket))
            }
            (Some(peers), false) => make_stargate_runtime_with_shared_discovery(id, peers.clone()),
            (Some(peers), true) => {
                let (addr, socket) = bind_ephemeral_udp();
                make_stargate_runtime_with_shared_discovery_and_reverse(
                    id,
                    peers.clone(),
                    addr,
                    Some(socket),
                )
            }
        };
        let (grpc_addr, http_addr, runtime) = runtime;
        nodes.push(GlobalWatchNode {
            grpc_addr,
            http_addr,
            handle: Some(runtime.start().await.expect("stargate failed to start")),
        });
    }

    assert_two_model_routes(
        nodes[0].grpc_addr,
        &nodes.iter().map(|node| node.http_addr).collect::<Vec<_>>(),
        reverse_tunnel,
        Duration::from_secs(timeout_secs),
    )
    .await;
    for node in &nodes {
        node.begin_shutdown();
    }
    for node in &mut nodes {
        node.wait_for_shutdown().await;
    }
}

#[tokio::test]
async fn two_models_forward_quic() {
    run_two_model_case(&["test-sg-fwd-2m"], TunnelDirection::Direct, 5).await;
}

#[tokio::test]
async fn two_models_reverse_tunnel() {
    run_two_model_case(&["test-sg-rev-2m"], TunnelDirection::Reverse, 10).await;
}

/// Two stargates with SharedDiscovery. Each backend seeds only stargate 1;
/// the client discovers stargate 2 via WatchStargates and registers with it.
#[tokio::test]
async fn two_models_multi_stargate_forward_quic() {
    run_two_model_case(MULTI_FORWARD_NODES, TunnelDirection::Direct, 10).await;
}

/// Two stargates with SharedDiscovery and reverse tunnel. Each backend seeds
/// only stargate 1; the client discovers stargate 2 via WatchStargates.
#[tokio::test]
async fn two_models_multi_stargate_reverse_tunnel() {
    run_two_model_case(MULTI_REVERSE_NODES, TunnelDirection::Reverse, 15).await;
}

/// Region A advertises Region B's WatchStargates endpoint as a remote watch URL.
/// A backend seeded only with Region A must recursively watch Region B and
/// register with every discovered stargate pod in both regions.
#[tokio::test]
async fn backend_discovers_remote_region_watch_url_and_registers_globally() {
    init_crypto();

    let region_a = Arc::new(Mutex::new(Vec::new()));
    let region_b = Arc::new(Mutex::new(Vec::new()));
    let a0 = GlobalWatchNode::start("test-global-a-0", region_a.clone(), None).await;
    let a1 = GlobalWatchNode::start("test-global-a-1", region_a.clone(), None).await;
    let b0 = GlobalWatchNode::start("test-global-b-0", region_b.clone(), None).await;
    let b1 = GlobalWatchNode::start("test-global-b-1", region_b, None).await;
    let a_remote =
        GlobalWatchNode::start("test-global-a-remote", region_a, Some(b0.grpc_addr)).await;
    let seeds = vec![a_remote.grpc_addr.to_string()];
    let mut nodes = [a0, a1, a_remote, b0, b1];
    let mut backend =
        start_and_register_backend(&seeds, "backend-global", "model-global", false).await;

    for node in &nodes {
        wait_for_routing(node.http_addr, "model-global", Duration::from_secs(15)).await;
    }

    backend.stop();
    for node in &nodes {
        node.begin_shutdown();
    }
    for node in &mut nodes {
        node.wait_for_shutdown().await;
    }
}

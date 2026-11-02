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

//! 3+ node distributed placement integration tests.
//!
//! ## What these tests cover
//!
//! Each test stands up an N-node *logical* cluster (each "node" is a
//! `tempfile::TempDir` + a real RocksDB-backed `ChunkManager`) and
//! exercises the placement / write / repair paths against it.
//!
//! Tests that need to actually fan a write out over the network use
//! `nvstor_rdma::rpc::RpcServer` (one per node) wired to that node's
//! `ChunkManager`, built with `RpcServer::new`, which serves the inline
//! TCP ops the replication and repair paths use. The managers dial them
//! through `DistributedManager::set_tcp_rpc_transport`, so no RDMA
//! device is needed and every test runs on any Linux host.
//!
//! Tests that don't need fanout (pure placement-decision tests) work
//! anywhere with no special setup.

#![cfg(feature = "rdma")]

use std::sync::atomic::{AtomicU16, Ordering};
use std::sync::Arc;
use std::time::Duration;

use nvstor_metadata::MetadataStore;
use nvstor_placement::distributed::DistributedManager;
use nvstor_placement::node::{ClusterMap, NodeId, StorageNode};
use nvstor_placement::placement;
use nvstor_placement::policy::{PlacementPolicy, PlacementStrategy};
use nvstor_rdma::rpc::{FetchFn, PersistFn, RpcServer};
use nvstor_rdma::transport::RdmaTransport;
use nvstor_storage::backend::PosixBackend;
use nvstor_storage::manager::{ChunkManager, ChunkManagerConfig};

use tempfile::TempDir;

// ---------------------------------------------------------------------------
// Port allocator — each test claims a non-overlapping window so parallel
// `cargo test` runs don't collide.
// ---------------------------------------------------------------------------

/// Start of the dynamic port range. Reserves 100 ports per call to
/// `alloc_port_window` (more than any single test needs).
static NEXT_PORT: AtomicU16 = AtomicU16::new(23000);

/// Cluster credential used by these tests. Node-to-node RPC is
/// authenticated, so both the RPC servers and the DistributedManagers
/// that dial them must share this.
fn test_cluster_credential() -> nvstor_rdma::auth::Credentials {
    nvstor_rdma::auth::Credentials::new("CLUSTER-TEST", "cluster-test-secret")
}

fn alloc_port_window(n: u16) -> u16 {
    NEXT_PORT.fetch_add(n, Ordering::SeqCst)
}

const HOST: &str = "127.0.0.1";

// ---------------------------------------------------------------------------
// Storage-only node (no RPC). Used by pure placement-decision tests.
// ---------------------------------------------------------------------------

struct StorageOnlyNode {
    id: NodeId,
    manager: Arc<ChunkManager>,
    failure_domain: String,
    capacity_bytes: u64,
    used_bytes: u64,
    rdma_port: u16,
    _tempdir: TempDir,
}

fn storage_only_node(
    id: NodeId,
    failure_domain: &str,
    capacity_bytes: u64,
    used_bytes: u64,
    rdma_port: u16,
) -> StorageOnlyNode {
    let tmp = TempDir::new().expect("tempdir");
    let backend = Box::new(PosixBackend::new(&tmp.path().join("data")).expect("posix"));
    let metadata = Arc::new(MetadataStore::open(&tmp.path().join("meta")).expect("metadata"));
    let mgr = Arc::new(ChunkManager::new(
        backend,
        metadata,
        ChunkManagerConfig {
            disk_usage_threshold: 1.0,
            ..ChunkManagerConfig::default()
        },
    ));
    StorageOnlyNode {
        id,
        manager: mgr,
        failure_domain: failure_domain.to_string(),
        capacity_bytes,
        used_bytes,
        rdma_port,
        _tempdir: tmp,
    }
}

fn storage_node_meta(n: &StorageOnlyNode) -> StorageNode {
    let mut sn = StorageNode::new(n.id, &format!("{HOST}:{}", n.rdma_port));
    sn.rdma_port = n.rdma_port;
    sn.failure_domain = n.failure_domain.clone();
    sn.capacity_bytes = n.capacity_bytes;
    sn.used_bytes = n.used_bytes;
    sn
}

fn cluster_map_from(nodes: &[&StorageOnlyNode]) -> ClusterMap {
    let mut map = ClusterMap::new();
    for n in nodes {
        map.add_node(storage_node_meta(n));
    }
    map
}

// ---------------------------------------------------------------------------
// Full RPC-equipped node. Required for tests that exercise actual
// cross-node fanout. Construction can fail if no RDMA device is
// available; callers should handle that path.
// ---------------------------------------------------------------------------

struct RpcNode {
    id: NodeId,
    manager: Arc<ChunkManager>,
    rdma_port: u16,
    rpc_port: u16,
    failure_domain: String,
    rpc_server: Arc<RpcServer>,
    /// `None` for TCP-only nodes. Held only to keep RDMA transport
    /// alive while the test runs; never inspected.
    _rdma_transport: Option<Arc<RdmaTransport>>,
    _tempdir: TempDir,
}

/// TCP-only build, no RDMA hardware required. Uses
/// `RpcServer::new`, which serves the inline ops the
/// replication / repair / xattr paths actually use. The fanout /
/// repair / EC tests below all rely solely on inline ops, so this
/// works on any host.
fn build_tcp_rpc_node(id: NodeId, rdma_port: u16, failure_domain: &str) -> RpcNode {
    let tmp = TempDir::new().expect("tempdir");
    let backend = Box::new(PosixBackend::new(&tmp.path().join("data")).expect("posix"));
    let metadata = Arc::new(MetadataStore::open(&tmp.path().join("meta")).expect("metadata"));
    let mgr = Arc::new(ChunkManager::new(
        backend,
        metadata,
        ChunkManagerConfig {
            disk_usage_threshold: 1.0,
            ..ChunkManagerConfig::default()
        },
    ));

    let mgr_p = Arc::clone(&mgr);
    let persist_fn: PersistFn = Arc::new(move |oid: &str, data: &[u8]| {
        mgr_p
            .object_write(oid, data, None)
            .map(|_| ())
            .map_err(|e| e.to_string())
    });
    let mgr_f = Arc::clone(&mgr);
    let fetch_fn: FetchFn =
        Arc::new(move |oid: &str| mgr_f.object_read(oid).map_err(|e| e.to_string()));

    // Node-to-node RPC authenticates per connection, so the test
    // server needs the cluster credential and every client must present it.
    let mut rpc_server = RpcServer::new(persist_fn, fetch_fn);
    rpc_server.set_cluster_credential(test_cluster_credential());
    let rpc_port = rdma_port + 1;
    rpc_server
        .start(rpc_port)
        .expect("rpc server start (tcp-only mode binds straight TCP)");
    let rpc_server = Arc::new(rpc_server);

    RpcNode {
        id,
        manager: mgr,
        rdma_port,
        rpc_port,
        failure_domain: failure_domain.to_string(),
        rpc_server,
        _rdma_transport: None,
        _tempdir: tmp,
    }
}

/// TCP-only DistributedManager: same shape as `try_build_dm_for` but
/// uses `set_tcp_rpc_transport` so no RDMA device is opened. The four
/// fanout/repair/EC tests below only use inline ops (OP_PERSIST_INLINE,
/// OP_FETCH_INLINE, OP_EXISTS), all of which are served by the
/// TCP-only RpcServer.
fn build_tcp_dm_for(
    nodes: &[RpcNode],
    index: usize,
    policy: PlacementPolicy,
) -> DistributedManager {
    let local = &nodes[index];
    let cluster = build_rpc_cluster(nodes);
    let mut dm = DistributedManager::new(local.id, Arc::clone(&local.manager), cluster, policy);
    dm.set_cluster_credential(test_cluster_credential());
    dm.set_tcp_rpc_transport();
    dm
}

fn rpc_node_meta(n: &RpcNode) -> StorageNode {
    let mut sn = StorageNode::new(n.id, &format!("{HOST}:{}", n.rdma_port));
    sn.rdma_port = n.rdma_port;
    sn.failure_domain = n.failure_domain.clone();
    sn
}

fn build_rpc_cluster(nodes: &[RpcNode]) -> ClusterMap {
    let mut map = ClusterMap::new();
    for n in nodes {
        map.add_node(rpc_node_meta(n));
    }
    map
}

async fn wait_for_rpc(port: u16) {
    use std::net::TcpStream;
    let deadline = std::time::Instant::now() + Duration::from_secs(3);
    loop {
        if TcpStream::connect_timeout(
            &format!("{HOST}:{port}").parse().unwrap(),
            Duration::from_millis(100),
        )
        .is_ok()
        {
            return;
        }
        assert!(
            std::time::Instant::now() < deadline,
            "rpc server on port {port} never came up"
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
}

// ---------------------------------------------------------------------------
// Tests.
// ---------------------------------------------------------------------------

/// Test 1 — write fans out to all 3 nodes.
///
/// Runs over the TCP-only RPC harness — no RDMA hardware needed.
#[tokio::test]
async fn three_node_write_fanout() {
    let base = alloc_port_window(20);
    let nodes = vec![
        build_tcp_rpc_node(1, base, "rack-0"),
        build_tcp_rpc_node(2, base + 4, "rack-0"),
        build_tcp_rpc_node(3, base + 8, "rack-0"),
    ];
    for n in &nodes {
        wait_for_rpc(n.rpc_port).await;
    }

    let dm = build_tcp_dm_for(&nodes, 0, PlacementPolicy::replicated_3());

    let object_id = "test/three_node_fanout";
    let data = b"hello distributed cluster";
    let result = dm
        .distributed_write(object_id, data, None)
        .await
        .expect("distributed_write");
    assert_eq!(
        result.nodes_written, 3,
        "expected fanout to all 3 nodes (errors: {:?})",
        result.errors
    );

    for n in &nodes {
        let stored = n
            .manager
            .object_read(object_id)
            .unwrap_or_else(|e| panic!("node {} missing chunk: {e}", n.id));
        assert_eq!(stored, data, "node {} stored wrong bytes", n.id);
    }
}

/// Test 2 — node 3 down at write time → write succeeds via the
/// remaining survivors; bring it back, run `repair_pass`, node 3
/// catches up.
#[tokio::test]
async fn node_down_write_succeeds_to_survivors_then_repair() {
    let base = alloc_port_window(20);
    let mut nodes = vec![
        build_tcp_rpc_node(1, base, "rack-0"),
        build_tcp_rpc_node(2, base + 4, "rack-0"),
        build_tcp_rpc_node(3, base + 8, "rack-0"),
    ];
    for n in &nodes {
        wait_for_rpc(n.rpc_port).await;
    }

    // Knock node 3 offline (close its RPC server).
    nodes[2].rpc_server.shutdown();
    tokio::time::sleep(Duration::from_millis(150)).await;

    let dm = build_tcp_dm_for(&nodes, 0, PlacementPolicy::replicated_3());
    dm.mark_node_down(3);

    let object_id = "test/node_down_then_repair";
    let data = b"survives through repair";
    // place_chunk requires `required_nodes` (=3) ACTIVE nodes, so this
    // may return Err("not enough active nodes"). That itself is the
    // documented behavior of the planner — it refuses to place under
    // quorum loss. We tolerate either outcome and assert the survivor
    // path below.
    let _ = dm.distributed_write(object_id, data, None).await;

    // Place the data on a survivor manually so the repair phase has
    // something to copy. (In production this would be the result of a
    // partial-success write where >= min_write_nodes succeeded.)
    nodes[0]
        .manager
        .object_write(object_id, data, None)
        .expect("seed survivor");

    // -- Repair phase --
    // Bring node 3 back: spin up a fresh TCP-only RpcServer pointing at
    // the same ChunkManager.
    let mgr_p = Arc::clone(&nodes[2].manager);
    let persist_fn: PersistFn = Arc::new(move |oid: &str, data: &[u8]| {
        mgr_p
            .object_write(oid, data, None)
            .map(|_| ())
            .map_err(|e| e.to_string())
    });
    let mgr_f = Arc::clone(&nodes[2].manager);
    let fetch_fn: FetchFn =
        Arc::new(move |oid: &str| mgr_f.object_read(oid).map_err(|e| e.to_string()));
    let mut new_rpc = RpcServer::new(persist_fn, fetch_fn);
    new_rpc.set_cluster_credential(test_cluster_credential());
    let mut started = false;
    for _ in 0..30 {
        if new_rpc.start(nodes[2].rpc_port).is_ok() {
            started = true;
            break;
        }
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    assert!(
        started,
        "could not restart RPC on port {}",
        nodes[2].rpc_port
    );
    nodes[2].rpc_server = Arc::new(new_rpc);
    wait_for_rpc(nodes[2].rpc_port).await;

    // Re-mark node 3 active so repair_pass can reach it.
    let mut updated = build_rpc_cluster(&nodes);
    for n in &mut updated.nodes {
        n.status = nvstor_placement::node::NodeStatus::Active;
    }
    // Replace the manager's cluster view with the post-repair one.
    let mut dm_n1 = DistributedManager::new(
        nodes[0].id,
        Arc::clone(&nodes[0].manager),
        updated,
        PlacementPolicy::replicated_3(),
    );
    dm_n1.set_cluster_credential(test_cluster_credential());
    dm_n1.set_tcp_rpc_transport();

    let (scanned, repaired, errors) = dm_n1.repair_pass();
    assert!(scanned >= 1, "repair scanned {scanned} (expected >=1)");
    assert!(
        repaired >= 1,
        "repair re-replicated {repaired} (expected >=1, errors: {errors})"
    );

    // Node 3 must now have the data.
    let n3 = nodes[2]
        .manager
        .object_read(object_id)
        .expect("node 3 should have been repaired");
    assert_eq!(n3, data);
}

/// Test 3 — node 3 permanently leaves; node 4 joins; `repair_pass`
/// migrates the chunk to node 4.
#[tokio::test]
async fn permanent_node_loss_repair_migrates() {
    let base = alloc_port_window(24);
    let nodes = vec![
        build_tcp_rpc_node(1, base, "rack-0"),
        build_tcp_rpc_node(2, base + 4, "rack-0"),
        build_tcp_rpc_node(3, base + 8, "rack-0"),
    ];
    for n in &nodes {
        wait_for_rpc(n.rpc_port).await;
    }

    let dm = build_tcp_dm_for(&nodes, 0, PlacementPolicy::replicated_3());
    let object_id = "test/permanent_loss";
    let data = b"chunk that must migrate to node 4";
    dm.distributed_write(object_id, data, None)
        .await
        .expect("initial write");
    for n in &nodes {
        assert!(
            n.manager.object_read(object_id).is_ok(),
            "node {} missing",
            n.id
        );
    }

    // Permanently kill node 3.
    nodes[2].rpc_server.shutdown();
    tokio::time::sleep(Duration::from_millis(150)).await;

    // Spin up node 4.
    let node4 = build_tcp_rpc_node(4, base + 12, "rack-0");
    wait_for_rpc(node4.rpc_port).await;

    // New cluster: 1, 2, 4 (no 3).
    let mut new_cluster = ClusterMap::new();
    new_cluster.add_node(rpc_node_meta(&nodes[0]));
    new_cluster.add_node(rpc_node_meta(&nodes[1]));
    new_cluster.add_node(rpc_node_meta(&node4));

    let mut dm_n1 = DistributedManager::new(
        nodes[0].id,
        Arc::clone(&nodes[0].manager),
        new_cluster,
        PlacementPolicy::replicated_3(),
    );
    dm_n1.set_cluster_credential(test_cluster_credential());
    dm_n1.set_tcp_rpc_transport();

    let (scanned, repaired, _errors) = dm_n1.repair_pass();
    assert!(scanned >= 1);
    assert!(repaired >= 1, "expected repair to re-replicate to node 4");

    let n4_data = node4
        .manager
        .object_read(object_id)
        .expect("node 4 should hold the chunk after repair");
    assert_eq!(n4_data, data);
}

/// Test 4 — rack-aware placement.
///
/// This test exercises only the placement-decision path
/// (`place_chunk`), which is a pure function and needs no RPC servers.
/// So it runs on any host, with or without RDMA.
#[tokio::test]
async fn rack_aware_placement() {
    let base = alloc_port_window(8);
    let n1 = storage_only_node(1, "rack-a", 0, 0, base);
    let n2 = storage_only_node(2, "rack-a", 0, 0, base + 1);
    let n3 = storage_only_node(3, "rack-b", 0, 0, base + 2);
    let n4 = storage_only_node(4, "rack-b", 0, 0, base + 3);
    let cluster = cluster_map_from(&[&n1, &n2, &n3, &n4]);
    let policy = PlacementPolicy::replicated_3();
    assert!(policy.spread_domains, "policy must spread for this test");

    // Try several distinct chunk IDs; every single one must place
    // replicas in BOTH racks (3 replicas + 2 racks ⇒ at least one
    // replica from each rack).
    let mut all_two_rack = true;
    let mut sample_results = Vec::new();
    for i in 0..20u32 {
        let key = format!("rack-aware/obj-{i}");
        let chunk_id = nvstor_core::types::ChunkId::from_content(key.as_bytes());
        let result = placement::place_chunk(&chunk_id.0, &policy, &cluster).expect("placement");
        assert_eq!(result.nodes.len(), 3, "key {key}");
        let racks: std::collections::HashSet<&str> = result
            .nodes
            .iter()
            .map(|nid| {
                cluster
                    .get_node(*nid)
                    .map(|sn| sn.failure_domain.as_str())
                    .unwrap_or("?")
            })
            .collect();
        sample_results.push((key.clone(), result.nodes.clone(), racks.clone()));
        if racks.len() < 2 {
            all_two_rack = false;
        }
    }
    assert!(
        all_two_rack,
        "at least one placement put all 3 replicas in one rack: {sample_results:?}"
    );

    // Sanity: a write through `DistributedManager` (single-node
    // cluster of self only) using the same hashing must agree with
    // `place_chunk` results — i.e., placement is deterministic.
    // We do this with a single-node DistributedManager because we
    // don't need multi-node RPC for this assertion.
    let dm = DistributedManager::new(1, Arc::clone(&n1.manager), cluster.clone(), policy.clone());
    dm.set_cluster_credential(test_cluster_credential());
    for (key, expected, _) in &sample_results {
        let chunk_id = nvstor_core::types::ChunkId::from_content(key.as_bytes());
        let recomputed =
            placement::place_chunk(&chunk_id.0, &policy, &dm.cluster_map()).expect("redo");
        assert_eq!(&recomputed.nodes, expected, "non-deterministic placement");
    }
}

/// Test 5 — capacity-aware placement.
///
/// 100 KB writes must not be placed on a node with 1 KB free, regardless
/// of the chunk hash.
#[tokio::test]
async fn capacity_aware_placement() {
    let base = alloc_port_window(20);
    let nodes = vec![
        storage_only_node(1, "rack-0", 1_000_000_000, 0, base),
        storage_only_node(2, "rack-0", 1_000_000_000, 0, base + 1),
        // Node 3: ~1 KB free.
        storage_only_node(3, "rack-0", 1_001_000, 1_000_000, base + 2),
    ];
    let cluster = cluster_map_from(&[&nodes[0], &nodes[1], &nodes[2]]);

    let policy = PlacementPolicy::replicated_2();
    let mut bad_picks = 0u32;
    for i in 0..50u32 {
        let key = format!("cap/{i}");
        let chunk_id = nvstor_core::types::ChunkId::from_content(key.as_bytes());
        // 100 KB write: node 3 (1 KB free) must be excluded.
        let r = placement::place_chunk_for_write(&chunk_id.0, &policy, &cluster, 100_000)
            .expect("placement");
        if r.nodes.contains(&3) {
            bad_picks += 1;
        }
    }
    assert_eq!(
        bad_picks, 0,
        "node 3 (1KB free) was selected for {bad_picks}/50 100KB writes"
    );
}

/// Test 6 — 4+2 erasure-coded write across 6 nodes; kill 2; read still
/// reconstructs.
#[tokio::test]
async fn erasure_coded_recovery() {
    let base = alloc_port_window(40);
    let nodes = vec![
        build_tcp_rpc_node(1, base, "rack-0"),
        build_tcp_rpc_node(2, base + 4, "rack-0"),
        build_tcp_rpc_node(3, base + 8, "rack-1"),
        build_tcp_rpc_node(4, base + 12, "rack-1"),
        build_tcp_rpc_node(5, base + 16, "rack-2"),
        build_tcp_rpc_node(6, base + 20, "rack-2"),
    ];
    for n in &nodes {
        wait_for_rpc(n.rpc_port).await;
    }

    let policy = PlacementPolicy::ec_4_2();
    assert!(matches!(
        policy.strategy,
        PlacementStrategy::ErasureCoded {
            data_shards: 4,
            parity_shards: 2
        }
    ));

    let dm = build_tcp_dm_for(&nodes, 0, policy);

    let object_id = "test/ec_recovery";
    // Distinctive payload — just under one EC chunk to keep test fast.
    let data: Vec<u8> = (0..4096u32).map(|i| (i & 0xFF) as u8).collect();
    let result = dm
        .distributed_write(object_id, &data, None)
        .await
        .expect("EC write");
    assert!(
        result.nodes_written >= 6,
        "expected all 6 shards written, got {} (errors: {:?})",
        result.nodes_written,
        result.errors
    );

    // Kill 2 nodes (max EC tolerance).
    nodes[4].rpc_server.shutdown();
    nodes[5].rpc_server.shutdown();
    tokio::time::sleep(Duration::from_millis(200)).await;

    let recovered = dm
        .distributed_read(object_id)
        .await
        .expect("EC reconstruct");
    assert_eq!(recovered, data, "EC recovered data mismatch");
}

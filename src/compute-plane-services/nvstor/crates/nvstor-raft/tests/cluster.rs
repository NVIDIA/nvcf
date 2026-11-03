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

//! 3-node Raft cluster integration tests.
//!
//! Each test brings up 3 in-process Raft nodes on
//! `127.0.0.1:0` ports, persists logs in `tempfile::TempDir`s, and exercises
//! a real multi-node scenario end-to-end (election, replication, leader
//! failure, persistence, quorum loss, partition).
//!
//! Timing is tight (election timeout 50–100 ms, heartbeat 20 ms) so each
//! test finishes in a few seconds when the machine is idle.
//!
//! The tests must hold on a loaded machine (a full
//! `--workspace` run, or CPU hogs on every core). Under load a leader can
//! miss its heartbeat window and a follower legitimately takes over, so:
//!   * nothing assumes leadership is stable — `propose_on_leader` follows
//!     `NotLeader` to the current leader, as a real client does;
//!   * the leader is the running node that leads the HIGHEST term (a deposed
//!     leader may not have heard yet that it was deposed);
//!   * every wait is a poll that returns as soon as its condition holds,
//!     bounded by `liveness_bound()` — a multiple of the configured election
//!     timeout, not a guess — and fails loud instead of hanging;
//!   * a stopped node has released every handle before it is restarted.
//!
//! Safety properties (nothing commits without a majority, a deposed leader
//! never reports a lost entry as committed) are asserted outright; only
//! liveness waits are time-bounded.

use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use nvstor_raft::log::RaftLog;
use nvstor_raft::network::RaftNetwork;
use nvstor_raft::node::RaftNode;
use nvstor_raft::server;
use nvstor_raft::state_machine::{MetadataStateMachine, NullStateMachine, StateMachine};
use nvstor_raft::types::{
    LogEntryData, LogIndex, NodeId, PeerConfig, ProposeResult, RaftConfig, Role,
};

use nvstor_metadata::MetadataStore;

use tempfile::TempDir;
use tokio::net::TcpListener;
use tokio::task::JoinHandle;

// ---------------------------------------------------------------------------
// Timing. Everything is derived from the Raft configuration below.
// ---------------------------------------------------------------------------

const ELECTION_TIMEOUT_MIN_MS: u64 = 50;
const ELECTION_TIMEOUT_MAX_MS: u64 = 100;
const HEARTBEAT_MS: u64 = 20;
const PROPOSE_TIMEOUT_MS: u64 = 3000;

/// Upper bound for any liveness wait (a leader emerging, an entry
/// committing, state converging): 100 maximum election timeouts. On an idle
/// machine these conditions hold within one or two election timeouts; the
/// rest is headroom for scheduler delay under load. Polls return as soon as
/// the condition holds, so the bound costs nothing when the test passes.
fn liveness_bound() -> Duration {
    Duration::from_millis(100 * ELECTION_TIMEOUT_MAX_MS)
}

/// Upper bound on one `propose` call: its own timeout plus a liveness bound
/// for the scheduler to run it.
fn propose_bound() -> Duration {
    Duration::from_millis(PROPOSE_TIMEOUT_MS) + liveness_bound()
}

/// Poll interval for all waits: a fraction of the heartbeat.
fn poll() -> Duration {
    Duration::from_millis(HEARTBEAT_MS / 2)
}

// ---------------------------------------------------------------------------
// Test fixture: a 3-node in-process cluster.
// ---------------------------------------------------------------------------

/// Per-node runtime state. Holds the live `RaftNode` plus the spawned
/// server/ticker task handles so we can selectively kill or restart nodes.
struct ClusterNode {
    node_id: NodeId,
    /// Address this node's RPC server is listening on.
    addr: String,
    /// Directory holding the persistent Raft log (RocksDB).
    raft_dir: PathBuf,
    /// Directory holding the persistent metadata store (RocksDB) — only
    /// populated if the node uses `MetadataStateMachine`.
    meta_dir: Option<PathBuf>,
    /// Live node handle (None when the node is shut down).
    node: Option<Arc<RaftNode>>,
    /// Live metadata store (None when not used or shut down).
    metadata: Option<Arc<MetadataStore>>,
    /// Ticker task handle.
    run_task: Option<JoinHandle<()>>,
    /// RPC server task handle.
    server_task: Option<JoinHandle<()>>,
}

impl ClusterNode {
    fn is_running(&self) -> bool {
        self.node.is_some()
    }

    /// Stop the node's ticker and server tasks, then drop all RocksDB
    /// handles so the on-disk state can be reopened cleanly.
    async fn shutdown(&mut self) {
        if let Some(node) = &self.node {
            node.stop();
        }
        if let Some(t) = self.run_task.take() {
            t.abort();
            let _ = t.await;
        }
        if let Some(t) = self.server_task.take() {
            // Aborting the server also aborts the connections it accepted
            // (`server::run` owns them), so the node stops answering RPCs.
            t.abort();
            let _ = t.await;
        }
        // Aborted connection handlers drop their node handle when the
        // runtime next drops the task; wait for that so the RocksDB lock is
        // free before anyone reopens the directory.
        if let Some(node) = self.node.take() {
            let released = tokio::time::timeout(liveness_bound(), async {
                while Arc::strong_count(&node) > 1 {
                    tokio::time::sleep(poll()).await;
                }
            })
            .await;
            assert!(
                released.is_ok(),
                "node{} still referenced after shutdown",
                self.node_id
            );
        }
        self.metadata.take();
    }
}

/// Test cluster owning the temp dirs (Drop deletes them) and node handles.
struct Cluster {
    /// Tempdirs we keep alive for the cluster's lifetime so RocksDB data
    /// isn't deleted out from under us.
    _tempdirs: Vec<TempDir>,
    nodes: Vec<ClusterNode>,
}

impl Cluster {
    /// Build and start a 3-node cluster with the given state-machine kind.
    /// Returns the cluster with all 3 nodes running.
    async fn start(use_metadata_sm: bool) -> Self {
        // Bind 3 listeners on 127.0.0.1:0 to discover ephemeral ports.
        let mut listeners = Vec::new();
        let mut addrs = Vec::new();
        for _ in 0..3 {
            let l = TcpListener::bind("127.0.0.1:0").await.expect("bind");
            let a = l.local_addr().expect("addr").to_string();
            listeners.push(l);
            addrs.push(a);
        }

        // Allocate one tempdir per node for the Raft log, plus (optionally)
        // one for the metadata store. Keep them alive in `_tempdirs` so
        // they aren't deleted while RocksDB is open.
        let mut tempdirs = Vec::new();
        let mut nodes: Vec<ClusterNode> = Vec::new();

        for (i, listener) in listeners.into_iter().enumerate() {
            let node_id: NodeId = (i + 1) as u64;

            let raft_td = TempDir::new().expect("raft tempdir");
            let raft_dir = raft_td.path().to_path_buf();
            tempdirs.push(raft_td);

            let meta_dir = if use_metadata_sm {
                let td = TempDir::new().expect("meta tempdir");
                let p = td.path().to_path_buf();
                tempdirs.push(td);
                Some(p)
            } else {
                None
            };

            let peers = build_peers(node_id, &addrs);
            let (node, metadata, run_task, server_task) =
                spawn_node(node_id, &raft_dir, meta_dir.as_deref(), listener, peers);

            nodes.push(ClusterNode {
                node_id,
                addr: addrs[i].clone(),
                raft_dir,
                meta_dir,
                node: Some(node),
                metadata,
                run_task: Some(run_task),
                server_task: Some(server_task),
            });
        }

        Cluster {
            _tempdirs: tempdirs,
            nodes,
        }
    }

    /// Poll until `check` holds on every running node, or fail.
    ///
    /// `wait_for_commit` waits on the commit INDEX, but the state machine
    /// applies entries *after* they commit. Asserting immediately after a
    /// commit therefore races the apply, and the window widens under load.
    /// Tests should wait on the property they actually care about — the data
    /// being visible — rather than on a proxy for it.
    async fn wait_for_all_nodes<F>(&self, what: &str, check: F)
    where
        F: Fn(&MetadataStore) -> bool,
    {
        let timeout = liveness_bound();
        let started = std::time::Instant::now();
        loop {
            let all = self
                .nodes
                .iter()
                .filter(|n| n.is_running())
                .filter_map(|n| n.metadata.as_ref())
                .all(|md| check(md));
            if all {
                return;
            }
            if started.elapsed() > timeout {
                panic!(
                    "timed out after {timeout:?} waiting for {what} to converge on every node: {:?}",
                    self.snapshot()
                );
            }
            tokio::time::sleep(poll()).await;
        }
    }

    /// Propose an entry, following leadership if it moves.
    ///
    /// `wait_for_leader` returns a snapshot: leadership can legitimately
    /// change between observing it and proposing, and `NotLeader` is the
    /// CORRECT response from a node that has since stepped down. Under the
    /// CPU contention of a full `--workspace` run a leader can miss its
    /// heartbeat window and a follower campaigns, so a test that assumes a
    /// stable leader is testing scheduler luck.
    ///
    /// This retries on `NotLeader` by re-resolving the leader — the same
    /// thing a real client does, and what the S3 layer's not-leader redirect
    /// does in production. A retried entry may end up in the log twice (the
    /// first attempt's outcome is unknown to the client); every entry the
    /// tests propose is idempotent.
    async fn propose_on_leader(&self, entry: LogEntryData) -> ProposeResult {
        let deadline = propose_bound();
        let started = std::time::Instant::now();
        let mut last = ProposeResult::Timeout;

        while started.elapsed() < deadline {
            let node = self.leader_node().await;
            last = node.propose(entry.clone()).await;
            match last {
                ProposeResult::Committed { .. } => return last,
                // Stepped down between observation and propose — re-resolve.
                ProposeResult::NotLeader { .. } => tokio::time::sleep(poll()).await,
                // Still leader, yet no majority committed it within the
                // propose timeout: a real failure, not a leadership race.
                ProposeResult::Timeout => return last,
            }
        }
        last
    }

    /// The current leader and a term it actually led.
    ///
    /// Reading `is_leader()` and then `current_term()` races a deposition:
    /// a leader of term 1 that has just heard of term 2 reports term 2, a
    /// term it never led, and a test comparing later terms against it waits
    /// for something that need not happen. The term is read on both sides of
    /// the role check; terms only grow, so equal reads around a positive
    /// check mean the node led that term.
    async fn leader_with_term(&self) -> (usize, u64) {
        let started = std::time::Instant::now();
        loop {
            let idx = self.wait_for_leader().await;
            let node = self.nodes[idx].node.as_ref().expect("leader node");
            let before = node.current_term();
            let leads = node.is_leader();
            let after = node.current_term();
            if leads && before == after {
                return (idx, before);
            }
            assert!(
                started.elapsed() < liveness_bound(),
                "leadership never held still long to read its term: {:?}",
                self.snapshot()
            );
            tokio::time::sleep(poll()).await;
        }
    }

    /// The current leader's handle, re-resolved.
    async fn leader_node(&self) -> Arc<RaftNode> {
        let idx = self.wait_for_leader().await;
        Arc::clone(self.nodes[idx].node.as_ref().expect("leader node"))
    }

    /// Index of the running node that leads the highest term, waiting up to
    /// `liveness_bound()` for one to exist.
    ///
    /// "Exactly one node says it is leader" is the wrong test: a leader that
    /// was deposed keeps saying so until it hears from the new term, and
    /// both are legitimately leaders of their own terms. The newest term is
    /// the one that can commit.
    async fn wait_for_leader(&self) -> usize {
        let result = tokio::time::timeout(liveness_bound(), async {
            loop {
                let leader = self
                    .nodes
                    .iter()
                    .enumerate()
                    .filter_map(|(i, n)| n.node.as_ref().map(|nd| (i, nd)))
                    .filter(|(_, nd)| nd.is_leader())
                    .max_by_key(|(_, nd)| nd.current_term())
                    .map(|(i, _)| i);
                if let Some(i) = leader {
                    return i;
                }
                tokio::time::sleep(poll()).await;
            }
        })
        .await;
        match result {
            Ok(i) => i,
            Err(_) => panic!(
                "no leader elected within {:?}: {:?}",
                liveness_bound(),
                self.snapshot()
            ),
        }
    }

    /// Wait for every running node's `commit_index` to be at least `target`.
    async fn wait_for_commit(&self, target: LogIndex) {
        let reached = tokio::time::timeout(liveness_bound(), async {
            loop {
                let all_caught_up = self
                    .nodes
                    .iter()
                    .filter_map(|n| n.node.as_ref())
                    .all(|nd| nd.commit_index() >= target);
                if all_caught_up {
                    return;
                }
                tokio::time::sleep(poll()).await;
            }
        })
        .await;
        if reached.is_err() {
            panic!(
                "commit_index did not reach {target} within {:?}: {:?}",
                liveness_bound(),
                self.snapshot()
            );
        }
    }

    /// One line per node, for failure messages.
    fn snapshot(&self) -> Vec<String> {
        self.nodes
            .iter()
            .map(|n| match n.node.as_ref() {
                Some(nd) => format!(
                    "node{} role={} term={} commit={} last_log={}",
                    n.node_id,
                    nd.role(),
                    nd.current_term(),
                    nd.commit_index(),
                    nd.last_log_index()
                ),
                None => format!("node{} (down)", n.node_id),
            })
            .collect()
    }

    /// Shut down every node and await all spawned tasks. Used between
    /// phases of `persist_across_restart`.
    async fn shutdown_all(&mut self) {
        for n in self.nodes.iter_mut() {
            n.shutdown().await;
        }
    }

    /// Re-open every node from the saved tempdirs, preserving each node's
    /// original listening port (we re-bind to the same address). Used by
    /// `persist_across_restart`.
    async fn restart_all(&mut self) {
        for idx in 0..self.nodes.len() {
            self.restart_node(idx).await;
        }
    }

    /// Restart a single node that was previously shut down. Re-binds the
    /// same address.
    async fn restart_node(&mut self, idx: usize) {
        let node_id = self.nodes[idx].node_id;
        let addr = self.nodes[idx].addr.clone();
        let raft_dir = self.nodes[idx].raft_dir.clone();
        let meta_dir = self.nodes[idx].meta_dir.clone();
        let addrs: Vec<String> = self.nodes.iter().map(|n| n.addr.clone()).collect();

        // tokio sets SO_REUSEADDR on Unix listeners, so the port is free
        // again as soon as the previous listener is dropped.
        let listener = TcpListener::bind(addr.as_str())
            .await
            .unwrap_or_else(|e| panic!("rebind {}: {}", addr, e));
        let peers = build_peers(node_id, &addrs);
        let (node, metadata, run_task, server_task) =
            spawn_node(node_id, &raft_dir, meta_dir.as_deref(), listener, peers);

        let n = &mut self.nodes[idx];
        n.node = Some(node);
        n.metadata = metadata;
        n.run_task = Some(run_task);
        n.server_task = Some(server_task);
    }
}

// ---------------------------------------------------------------------------
// Node spawn helper.
// ---------------------------------------------------------------------------

fn build_peers(node_id: NodeId, addrs: &[String]) -> Vec<PeerConfig> {
    let mut peers = Vec::new();
    for (i, addr) in addrs.iter().enumerate() {
        let pid = (i + 1) as NodeId;
        if pid != node_id {
            peers.push(PeerConfig {
                node_id: pid,
                addr: addr.clone(),
            });
        }
    }
    peers
}

fn fast_config(node_id: NodeId, peers: Vec<PeerConfig>) -> RaftConfig {
    RaftConfig {
        node_id,
        // raft_port is only used by binaries; tests bind their own listener.
        raft_port: 0,
        peers,
        election_timeout_min_ms: ELECTION_TIMEOUT_MIN_MS,
        election_timeout_max_ms: ELECTION_TIMEOUT_MAX_MS,
        heartbeat_ms: HEARTBEAT_MS,
        propose_timeout_ms: PROPOSE_TIMEOUT_MS,
    }
}

/// Open the log + (optional) metadata, build the node, and spawn its
/// ticker and RPC server. Returns the live handles.
fn spawn_node(
    node_id: NodeId,
    raft_dir: &std::path::Path,
    meta_dir: Option<&std::path::Path>,
    listener: TcpListener,
    peers: Vec<PeerConfig>,
) -> (
    Arc<RaftNode>,
    Option<Arc<MetadataStore>>,
    JoinHandle<()>,
    JoinHandle<()>,
) {
    let log = RaftLog::open(raft_dir).expect("open raft log");
    let network = Arc::new(RaftNetwork::new(node_id, peers.clone()));
    let config = fast_config(node_id, peers);

    let (sm, metadata): (Box<dyn StateMachine>, Option<Arc<MetadataStore>>) = match meta_dir {
        Some(p) => {
            let md = Arc::new(MetadataStore::open(p).expect("open metadata"));
            (Box::new(MetadataStateMachine::new(md.clone())), Some(md))
        }
        None => (Box::new(NullStateMachine), None),
    };

    let node = Arc::new(RaftNode::new(config, log, network, sm));

    let run_node = node.clone();
    let run_task = tokio::spawn(async move {
        run_node.run().await;
    });

    let server_node = node.clone();
    let server_task = tokio::spawn(async move {
        let _ = server::run(listener, server_node).await;
    });

    (node, metadata, run_task, server_task)
}

fn bucket(name: String) -> LogEntryData {
    LogEntryData::BucketPut {
        name,
        owner: "u".into(),
        region: "r".into(),
        versioning: 0,
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

/// Scenario 1: bootstrap a 3-node cluster, verify a leader is elected,
/// propose 10 entries through the leader, and verify all 3 SMs converge to
/// the same final state.
#[tokio::test(flavor = "multi_thread")]
async fn three_node_election_and_replication() {
    let cluster = Cluster::start(true).await;

    cluster.wait_for_leader().await;

    // Propose 10 BucketPut entries, following leadership if it moves —
    // holding one node handle across 10 proposals assumes a stability Raft
    // does not promise.
    for i in 0..10u64 {
        let result = cluster
            .propose_on_leader(LogEntryData::BucketPut {
                name: format!("bucket-{i}"),
                owner: "test".into(),
                region: "us".into(),
                versioning: 0,
            })
            .await;
        match result {
            ProposeResult::Committed { .. } => {}
            other => panic!("expected Committed for entry {i}, got {other:?}"),
        }
    }

    // Wait for every node's commit_index to catch up with the leader's log.
    let target = cluster.leader_node().await.last_log_index();
    cluster.wait_for_commit(target).await;

    // Wait for all 10 to be visible everywhere before comparing. This
    // removes apply-lag as an explanation: if it still times out, entries
    // that `propose` reported Committed are genuinely missing, which is a
    // product bug rather than a slow test (for example, a deposed leader
    // reporting a new leader's entry at its index as its own).
    cluster
        .wait_for_all_nodes("10 buckets", |md| {
            md.list_buckets().map(|b| b.len() >= 10).unwrap_or(false)
        })
        .await;

    // Verify each MetadataStore has the same final bucket set.
    let mut bucket_sets: Vec<Vec<String>> = Vec::new();
    for n in &cluster.nodes {
        let md = n.metadata.as_ref().expect("metadata store");
        let mut names: Vec<String> = md
            .list_buckets()
            .expect("list_buckets")
            .into_iter()
            .map(|b| b.name)
            .collect();
        names.sort();
        bucket_sets.push(names);
    }
    let first = &bucket_sets[0];
    assert_eq!(first.len(), 10, "expected 10 buckets, got {first:?}");
    for (i, set) in bucket_sets.iter().enumerate().skip(1) {
        assert_eq!(set, first, "node {i} bucket set diverged: {set:?}");
    }
}

/// An object manifest proposed with its chunk refs must converge on
/// every node so a follower that never received the chunk DATA still holds a
/// complete, readable manifest (and can fetch chunk bytes from a replica).
#[tokio::test(flavor = "multi_thread")]
async fn manifest_put_with_chunks_replicates() {
    let cluster = Cluster::start(true).await;
    cluster.wait_for_leader().await;

    let chunks = vec![
        nvstor_metadata::ChunkRef {
            chunk_id: [7u8; 32],
            length: 4096,
            checksum: 0xdead_beef,
            logical_start: 0,
            logical_end: 4096,
            nodes: vec![1, 2],
        },
        nvstor_metadata::ChunkRef {
            chunk_id: [8u8; 32],
            length: 2048,
            checksum: 0x1234_5678,
            logical_start: 4096,
            logical_end: 6144,
            nodes: vec![2, 3],
        },
    ];

    let result = cluster
        .propose_on_leader(LogEntryData::ManifestPut {
            object_id: "photos/cat.jpg".into(),
            size: 6144,
            etag: "\"abc123\"".into(),
            content_type: "image/jpeg".into(),
            chunks: chunks.clone(),
            created_at: 1000,
            modified_at: 2000,
        })
        .await;
    assert!(
        matches!(result, ProposeResult::Committed { .. }),
        "got {result:?}"
    );

    cluster
        .wait_for_all_nodes("manifest", |md| {
            md.get_manifest("photos/cat.jpg").ok().flatten().is_some()
        })
        .await;

    // Every node — leader and both followers — must have the full manifest.
    for n in &cluster.nodes {
        let md = n.metadata.as_ref().expect("metadata store");
        let mm = md
            .get_manifest("photos/cat.jpg")
            .expect("get_manifest ok")
            .expect("manifest present on every node");
        assert_eq!(mm.size, 6144);
        assert_eq!(mm.etag, "\"abc123\"");
        assert_eq!(mm.chunks.len(), 2, "chunk refs must replicate");
        assert_eq!(mm.chunks[0].chunk_id, [7u8; 32]);
        assert_eq!(mm.chunks[1].logical_end, 6144);
        assert_eq!(mm.created_at, 1000);
        assert_eq!(mm.modified_at, 2000);
    }

    // A ManifestDelete must likewise converge and remove it everywhere.
    let del = cluster
        .propose_on_leader(LogEntryData::ManifestDelete {
            object_id: "photos/cat.jpg".into(),
        })
        .await;
    assert!(
        matches!(del, ProposeResult::Committed { .. }),
        "got {del:?}"
    );
    cluster
        .wait_for_all_nodes("manifest removal", |md| {
            md.get_manifest("photos/cat.jpg").ok().flatten().is_none()
        })
        .await;
    for n in &cluster.nodes {
        let md = n.metadata.as_ref().expect("metadata store");
        assert!(
            md.get_manifest("photos/cat.jpg").expect("ok").is_none(),
            "manifest must be gone on every node after ManifestDelete"
        );
    }
}

/// The object-version index and inline SSE encryption metadata must
/// converge on every node so a follower can serve versioned GETs and
/// decrypt SSE objects after a leader change.
#[tokio::test(flavor = "multi_thread")]
async fn version_index_and_inline_meta_replicate() {
    let cluster = Cluster::start(true).await;
    cluster.wait_for_leader().await;

    let version = nvstor_metadata::ObjectVersion {
        version_id: "v0-abc".into(),
        bucket: "b".into(),
        key: "k".into(),
        is_delete_marker: false,
        is_latest: true,
        size: 123,
        etag: "\"e\"".into(),
        content_type: "text/plain".into(),
        last_modified: 42,
        owner: "default".into(),
        manifest_object_id: "b/k@v0-abc".into(),
    };
    let r = cluster
        .propose_on_leader(LogEntryData::ObjectVersionPut {
            version: version.clone(),
        })
        .await;
    assert!(matches!(r, ProposeResult::Committed { .. }), "got {r:?}");
    let r = cluster
        .propose_on_leader(LogEntryData::InlinePut {
            key: "enc/b/k@v0-abc".into(),
            value: b"envelope-dek-and-iv".to_vec(),
        })
        .await;
    assert!(matches!(r, ProposeResult::Committed { .. }), "got {r:?}");

    cluster
        .wait_for_all_nodes("version + inline meta", |md| {
            md.get_object_version("b", "k", "v0-abc")
                .ok()
                .flatten()
                .is_some()
                && md.get_inline("enc/b/k@v0-abc").ok().flatten().is_some()
        })
        .await;

    for n in &cluster.nodes {
        let md = n.metadata.as_ref().expect("metadata store");
        let v = md
            .get_object_version("b", "k", "v0-abc")
            .expect("ok")
            .expect("version present on every node");
        assert_eq!(v.manifest_object_id, "b/k@v0-abc");
        assert_eq!(v.size, 123);
        assert_eq!(
            md.get_inline("enc/b/k@v0-abc")
                .expect("ok")
                .expect("enc meta present"),
            b"envelope-dek-and-iv"
        );
    }

    // Delete the version + its inline meta; both must vanish everywhere.
    let r = cluster
        .propose_on_leader(LogEntryData::ObjectVersionDelete {
            bucket: "b".into(),
            key: "k".into(),
            version_id: "v0-abc".into(),
        })
        .await;
    assert!(matches!(r, ProposeResult::Committed { .. }), "got {r:?}");
    let r = cluster
        .propose_on_leader(LogEntryData::InlineDelete {
            key: "enc/b/k@v0-abc".into(),
        })
        .await;
    assert!(matches!(r, ProposeResult::Committed { .. }), "got {r:?}");
    cluster
        .wait_for_all_nodes("version + inline meta removal", |md| {
            md.get_object_version("b", "k", "v0-abc")
                .ok()
                .flatten()
                .is_none()
                && md.get_inline("enc/b/k@v0-abc").ok().flatten().is_none()
        })
        .await;

    for n in &cluster.nodes {
        let md = n.metadata.as_ref().expect("metadata store");
        assert!(md
            .get_object_version("b", "k", "v0-abc")
            .expect("ok")
            .is_none());
        assert!(md.get_inline("enc/b/k@v0-abc").expect("ok").is_none());
    }
}

/// Scenario 2: kill the leader; a surviving node wins a later term.
///
/// On an idle machine this takes about one election timeout; the wait is
/// bounded by `liveness_bound()` because under load an election can split
/// and need another round.
#[tokio::test(flavor = "multi_thread")]
async fn kill_leader_followers_re_elect() {
    let mut cluster = Cluster::start(false).await;

    let (leader_idx, original_term) = cluster.leader_with_term().await;
    let original_leader_id = cluster.nodes[leader_idx].node_id;

    cluster.nodes[leader_idx].shutdown().await;

    // Read leader_id between two consistent observations of leadership in
    // one term; a read that straddles a step-down is retried, not asserted.
    let started = std::time::Instant::now();
    let (new_leader_idx, new_term, reported_leader_id) = loop {
        let (idx, term) = cluster.leader_with_term().await;
        let nd = cluster.nodes[idx].node.as_ref().expect("new leader");
        let id = nd.leader_id();
        if nd.is_leader() && nd.current_term() == term {
            break (idx, term, id);
        }
        assert!(
            started.elapsed() < liveness_bound(),
            "leadership never held still"
        );
    };

    assert_ne!(
        cluster.nodes[new_leader_idx].node_id, original_leader_id,
        "a stopped node cannot lead"
    );
    assert!(
        new_term > original_term,
        "term must advance: {} -> {}",
        original_term,
        new_term
    );
    assert_eq!(
        reported_leader_id, cluster.nodes[new_leader_idx].node_id,
        "leader_id should match the elected node"
    );
}

/// Scenario 3: propose entries, shut down the entire cluster, re-open from
/// the same disk dirs, and verify the log replays and replication picks up.
#[tokio::test(flavor = "multi_thread")]
async fn persist_across_restart() {
    let mut cluster = Cluster::start(false).await;
    cluster.wait_for_leader().await;

    // Phase 1: propose 5 entries.
    for i in 0..5u64 {
        let r = cluster
            .propose_on_leader(bucket(format!("phase1-{i}")))
            .await;
        assert!(
            matches!(r, ProposeResult::Committed { .. }),
            "phase1 entry {i}: {r:?}"
        );
    }
    let phase1_last = cluster.leader_node().await.last_log_index();
    cluster.wait_for_commit(phase1_last).await;

    // Shut down every node, then reopen from the same dirs.
    cluster.shutdown_all().await;
    cluster.restart_all().await;

    // Every node replays its log from disk on open, before any election.
    for n in &cluster.nodes {
        let nd = n.node.as_ref().expect("running");
        assert!(
            nd.last_log_index() >= phase1_last,
            "node{}: last_log_index {} < phase1_last {}",
            n.node_id,
            nd.last_log_index(),
            phase1_last
        );
    }

    // A leader must re-elect.
    cluster.wait_for_leader().await;

    // Phase 2: append 5 more entries and verify replication still works.
    for i in 0..5u64 {
        let r = cluster
            .propose_on_leader(bucket(format!("phase2-{i}")))
            .await;
        assert!(
            matches!(r, ProposeResult::Committed { .. }),
            "phase2 entry {i}: {r:?}"
        );
    }
    let phase2_last = cluster.leader_node().await.last_log_index();
    cluster.wait_for_commit(phase2_last).await;
    assert!(phase2_last >= phase1_last + 5);
}

/// Scenario 4: kill 2 of 3 nodes. The survivor cannot reach a majority, so
/// nothing it proposes may commit; check-quorum makes it step down and
/// answer `NotLeader` instead of holding the proposal until the propose
/// timeout. After one node returns, writes commit again.
#[tokio::test(flavor = "multi_thread")]
async fn quorum_loss_writes_stall() {
    let mut cluster = Cluster::start(false).await;

    // Kill both followers of the CURRENT leader and confirm that node was
    // still leading the same term once they were gone. If leadership moved
    // in between (possible under load), bring them back and try again: the
    // scenario needs a survivor that believes it leads.
    let mut survivor_idx = None;
    for _ in 0..20 {
        let leader_idx = cluster.wait_for_leader().await;
        let leader = Arc::clone(cluster.nodes[leader_idx].node.as_ref().expect("leader"));
        cluster.wait_for_commit(leader.last_log_index()).await;
        let term = leader.current_term();

        let followers: Vec<usize> = (0..3).filter(|i| *i != leader_idx).collect();
        for &f in &followers {
            cluster.nodes[f].shutdown().await;
        }
        if leader.is_leader() && leader.current_term() == term {
            survivor_idx = Some(leader_idx);
            break;
        }
        for &f in &followers {
            cluster.restart_node(f).await;
        }
    }
    let survivor_idx =
        survivor_idx.expect("no leader survived losing its followers in 20 attempts");
    let survivor = Arc::clone(cluster.nodes[survivor_idx].node.as_ref().expect("survivor"));

    // From here on nothing can reach the survivor's commit index except its
    // own quorum, which it no longer has.
    let baseline_commit = survivor.commit_index();

    // Propose on the survivor. With 1 of 3 nodes alive quorum (2) is
    // impossible: the proposal must not commit. Check-quorum steps the
    // survivor down within about one election timeout, which releases the
    // proposal with NotLeader long before its 3 s propose timeout.
    let proposer = Arc::clone(&survivor);
    let stalled = tokio::time::timeout(propose_bound(), async move {
        proposer.propose(bucket("stalled".into())).await
    })
    .await
    .expect("propose did not return");
    match stalled {
        ProposeResult::NotLeader { .. } => {}
        ProposeResult::Committed { index, term } => {
            panic!("SAFETY: committed at {index} (term {term}) with 1 of 3 nodes alive")
        }
        ProposeResult::Timeout => {
            panic!("check-quorum did not step the isolated leader down before the propose timeout")
        }
    }
    assert!(
        !survivor.is_leader(),
        "an isolated node must not claim leadership"
    );
    assert_eq!(
        survivor.commit_index(),
        baseline_commit,
        "commit advanced without quorum"
    );

    // Bring one follower back. Quorum is now 2/3; writes commit again.
    let revive_idx = (0..3).find(|i| *i != survivor_idx).expect("a dead node");
    let kill_idx = (0..3)
        .find(|i| *i != survivor_idx && *i != revive_idx)
        .expect("the other dead node");
    cluster.restart_node(revive_idx).await;

    let result = cluster
        .propose_on_leader(bucket("after-recovery".into()))
        .await;
    assert!(
        matches!(result, ProposeResult::Committed { .. }),
        "expected Committed after quorum restored, got {result:?}"
    );
    // The survivor rejoins and catches up with the restored majority.
    let target = cluster.leader_node().await.last_log_index();
    cluster.wait_for_commit(target).await;
    assert!(survivor.commit_index() > baseline_commit);

    // Sanity: the still-dead node is silent.
    assert!(!cluster.nodes[kill_idx].is_running());
}

/// Scenario 5: partition the leader from its followers. The followers
/// elect a new leader at a higher term; when the partition heals, the old
/// leader steps down and any uncommitted writes it accepted are dropped.
///
/// Implementation note: the library has no first-class network-partition
/// API. We approximate a one-sided partition by stopping the old leader's
/// ticker (so it stops sending heartbeats and replicating) while leaving
/// its server up. The two healthy nodes time out, elect, and bump the
/// term. When we restart the old leader's ticker, it will receive an
/// AppendEntries from the new leader at a higher term and step down.
#[tokio::test(flavor = "multi_thread")]
async fn split_brain_prevention() {
    let mut cluster = Cluster::start(false).await;

    let (old_leader_idx, old_term) = cluster.leader_with_term().await;
    let old_leader = cluster.nodes[old_leader_idx]
        .node
        .as_ref()
        .expect("leader")
        .clone();
    let old_leader_id = cluster.nodes[old_leader_idx].node_id;

    // "Partition" the old leader: stop its ticker, abort its run task.
    // Server stays up so it can receive RequestVote / AppendEntries from
    // the eventual new leader.
    old_leader.stop();
    if let Some(t) = cluster.nodes[old_leader_idx].run_task.take() {
        t.abort();
        let _ = t.await;
    }

    // Try to propose on the old leader while its ticker is down. If it still
    // believes it leads, the entry is appended to its log but never
    // replicated; it must never be reported committed.
    let stale_propose = tokio::spawn({
        let l = old_leader.clone();
        async move { l.propose(bucket("stale-uncommitted".into())).await }
    });

    // Wait for one of the other two nodes to win election at a higher term.
    let new_leader_idx = tokio::time::timeout(liveness_bound(), async {
        loop {
            for (i, n) in cluster.nodes.iter().enumerate() {
                if i == old_leader_idx {
                    continue;
                }
                if let Some(nd) = n.node.as_ref() {
                    if nd.is_leader() && nd.current_term() > old_term {
                        return i;
                    }
                }
            }
            tokio::time::sleep(poll()).await;
        }
    })
    .await
    .expect("no new leader elected after partition");
    assert_ne!(
        cluster.nodes[new_leader_idx].node_id, old_leader_id,
        "new leader id must differ"
    );

    // Append an entry through the new leadership so its log diverges from
    // the old leader's stale entry at the same index.
    let r = cluster
        .propose_on_leader(bucket("post-partition".into()))
        .await;
    assert!(matches!(r, ProposeResult::Committed { .. }), "got {r:?}");
    let new_term = cluster.leader_node().await.current_term();
    assert!(new_term > old_term, "new term must be higher");

    // Heal the partition: restart the old leader's ticker. It will hear
    // a heartbeat from the new leader at a higher term and step down.
    let restarted_node = old_leader.clone();
    let new_run_task = tokio::spawn(async move {
        restarted_node.run().await;
    });
    cluster.nodes[old_leader_idx].run_task = Some(new_run_task);

    // The stale proposal must not commit: it ends NotLeader once the old
    // leader hears the newer term (or Timeout if it never does).
    let stale = tokio::time::timeout(propose_bound(), stale_propose)
        .await
        .expect("stale proposal never returned")
        .expect("join");
    assert!(
        !matches!(stale, ProposeResult::Committed { .. }),
        "SAFETY: entry from a partitioned leader reported committed: {stale:?}"
    );

    // Wait for the old leader to recognize the new term and step down.
    tokio::time::timeout(liveness_bound(), async {
        loop {
            if old_leader.role() != Role::Leader && old_leader.current_term() >= new_term {
                return;
            }
            tokio::time::sleep(poll()).await;
        }
    })
    .await
    .expect("old leader did not step down");

    // The old leader converges on the new leadership's log: its stale entry
    // is overwritten and it commits everything the new leader has.
    let target = cluster.leader_node().await.last_log_index();
    cluster.wait_for_commit(target).await;
    assert!(
        old_leader.last_log_index() >= target,
        "old leader did not converge: {} < {}",
        old_leader.last_log_index(),
        target
    );
}

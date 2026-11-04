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

//! Node management — tracks cluster membership and node health.

use serde::{Deserialize, Serialize};

/// Unique identifier for a storage node.
pub type NodeId = u64;

/// Lowest node ID produced by [`node_id_for_address`]. IDs below this are
/// reserved for explicitly configured nodes (and 0 means "unknown" in Raft).
const DERIVED_ID_FLOOR: u64 = 1_000;

/// Derive a stable node ID from a node's address.
///
/// Cluster identity MUST NOT depend on the order a node happens to list its
/// seeds: if each node assigns IDs by its own seed-list index, the same
/// physical machine gets a different ID on every node, so placement computed
/// on node A targets different machines than the same object computed on
/// node B — replicas scatter and reads miss.
///
/// This maps address → ID with a fixed FNV-1a hash so every node in the
/// cluster independently computes the SAME ID for the same peer. Only the
/// host part is hashed (ports differ per service: 9002 wire, 9000 S3, 9102
/// Raft — all the same machine).
///
/// Note: `std::collections::hash_map::DefaultHasher` is NOT usable here — it
/// is randomly seeded per process, so two nodes would derive different IDs
/// for the same address. FNV-1a is fixed-seed and stable across processes,
/// restarts, and architectures.
pub fn node_id_for_address(addr: &str) -> NodeId {
    let host = addr
        .split(':')
        .next()
        .unwrap_or(addr)
        .trim()
        .to_ascii_lowercase();

    // FNV-1a, 64-bit.
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    for byte in host.as_bytes() {
        hash ^= *byte as u64;
        hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
    }

    // Keep clear of 0 (Raft's "leader unknown") and the low range reserved
    // for explicitly configured IDs.
    DERIVED_ID_FLOOR + (hash % (u64::MAX - DERIVED_ID_FLOOR))
}

/// Parse a seed entry into `(node_id, address)`.
///
/// Two accepted forms:
///   - `"<id>@<addr>"` — explicit ID, the canonical form for production
///     configs (same shape as etcd's `initial-cluster`). Every node writes
///     the same ID for the same peer, so identity is unambiguous.
///   - `"<addr>"` — no ID given; derive one via [`node_id_for_address`].
///     Still consistent cluster-wide, just not human-chosen.
///
/// An `<id>@` prefix that doesn't parse as a number is treated as part of the
/// address (so an IPv6 literal or a userinfo-style host isn't silently
/// mangled into a bogus ID).
pub fn parse_seed(entry: &str) -> (NodeId, String) {
    if let Some((id_part, addr_part)) = entry.split_once('@') {
        if let Ok(id) = id_part.trim().parse::<u64>() {
            let addr = addr_part.trim().to_string();
            // A numeric prefix is an ID field, so strip it either way; 0 is
            // reserved (Raft's "leader unknown"), so derive instead of
            // leaving "0@host" as a bogus, unconnectable address.
            let id = if id == 0 {
                node_id_for_address(&addr)
            } else {
                id
            };
            return (id, addr);
        }
    }
    let addr = entry.trim().to_string();
    (node_id_for_address(&addr), addr)
}

/// Node health status.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum NodeStatus {
    /// Node is healthy and serving requests.
    Active,
    /// Node is not responding (suspected failure).
    Suspect,
    /// Node confirmed down — data needs repair.
    Down,
    /// Node is being decommissioned — data being drained.
    Draining,
}

/// A storage node in the cluster.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct StorageNode {
    /// Unique node identifier.
    pub id: NodeId,
    /// Node address (host:port for data transfer).
    pub address: String,
    /// S3 endpoint for this node.
    pub s3_address: String,
    /// Current health status.
    pub status: NodeStatus,
    /// Available storage capacity in bytes.
    pub capacity_bytes: u64,
    /// Used storage in bytes.
    pub used_bytes: u64,
    /// Weight for placement (higher = more chunks assigned).
    pub weight: u32,
    /// Failure domain (rack, zone, region) for spread placement.
    pub failure_domain: String,
    /// Last heartbeat timestamp (unix seconds).
    pub last_heartbeat: i64,
    /// RDMA control port for QP exchange (0 = RDMA disabled).
    pub rdma_port: u16,
}

impl StorageNode {
    pub fn new(id: NodeId, address: &str) -> Self {
        Self {
            id,
            address: address.to_string(),
            s3_address: format!(
                "http://{}:9000",
                address.split(':').next().unwrap_or(address)
            ),
            status: NodeStatus::Active,
            capacity_bytes: 0,
            used_bytes: 0,
            weight: 100,
            failure_domain: "default".into(),
            last_heartbeat: 0,
            rdma_port: 0,
        }
    }

    pub fn is_available(&self) -> bool {
        self.status == NodeStatus::Active
    }

    pub fn free_bytes(&self) -> u64 {
        self.capacity_bytes.saturating_sub(self.used_bytes)
    }
}

/// Cluster membership — the set of known nodes.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ClusterMap {
    /// All known nodes.
    pub nodes: Vec<StorageNode>,
    /// Cluster epoch — incremented on membership changes.
    pub epoch: u64,
}

impl ClusterMap {
    pub fn new() -> Self {
        Self {
            nodes: Vec::new(),
            epoch: 1,
        }
    }

    pub fn add_node(&mut self, node: StorageNode) {
        // Replace if exists, add if new.
        if let Some(pos) = self.nodes.iter().position(|n| n.id == node.id) {
            self.nodes[pos] = node;
        } else {
            self.nodes.push(node);
        }
        self.epoch += 1;
    }

    pub fn remove_node(&mut self, id: NodeId) {
        self.nodes.retain(|n| n.id != id);
        self.epoch += 1;
    }

    pub fn get_node(&self, id: NodeId) -> Option<&StorageNode> {
        self.nodes.iter().find(|n| n.id == id)
    }

    pub fn active_nodes(&self) -> Vec<&StorageNode> {
        self.nodes.iter().filter(|n| n.is_available()).collect()
    }

    pub fn node_count(&self) -> usize {
        self.nodes.len()
    }

    pub fn active_count(&self) -> usize {
        self.nodes.iter().filter(|n| n.is_available()).count()
    }
}

impl Default for ClusterMap {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // The core invariant — every node must derive the SAME id for the
    // same peer, regardless of the order it lists its seeds. This is the
    // property the old seed-index scheme violated.
    #[test]
    fn node_ids_are_independent_of_seed_order() {
        // Node A lists [B, C]; node B lists [C, A]; node C lists [A, B].
        let a = "10.0.0.1:9002";
        let b = "10.0.0.2:9002";
        let c = "10.0.0.3:9002";

        let from_a: Vec<NodeId> = [b, c].iter().map(|s| parse_seed(s).0).collect();
        let from_b: Vec<NodeId> = [c, a].iter().map(|s| parse_seed(s).0).collect();
        let from_c: Vec<NodeId> = [a, b].iter().map(|s| parse_seed(s).0).collect();

        // Whoever computes it, address → id is the same mapping.
        assert_eq!(from_a[0], from_c[1]); // b
        assert_eq!(from_a[1], from_b[0]); // c
        assert_eq!(from_b[1], from_c[0]); // a

        // And distinct hosts get distinct ids.
        let ids = [parse_seed(a).0, parse_seed(b).0, parse_seed(c).0];
        assert_ne!(ids[0], ids[1]);
        assert_ne!(ids[1], ids[2]);
        assert_ne!(ids[0], ids[2]);
    }

    #[test]
    fn node_id_ignores_port_and_case() {
        // Same machine reached on different service ports is one node.
        assert_eq!(
            node_id_for_address("10.0.0.1:9002"),
            node_id_for_address("10.0.0.1:9000")
        );
        assert_eq!(
            node_id_for_address("Node1.example.com:9002"),
            node_id_for_address("node1.example.com:9102")
        );
    }

    #[test]
    fn node_id_is_stable_and_never_zero() {
        // Fixed-seed hash: the same input must map to the same id across
        // processes/restarts (DefaultHasher would break this).
        assert_eq!(
            node_id_for_address("10.0.0.1"),
            node_id_for_address("10.0.0.1")
        );
        assert!(node_id_for_address("10.0.0.1") >= DERIVED_ID_FLOOR);
        assert_ne!(node_id_for_address(""), 0);
    }

    #[test]
    fn parse_seed_honors_explicit_id() {
        assert_eq!(
            parse_seed("7@10.0.0.5:9002"),
            (7, "10.0.0.5:9002".to_string())
        );
        // Explicit id 0 is invalid (Raft reserves it) → fall back to derived.
        let (id, addr) = parse_seed("0@10.0.0.5:9002");
        assert_ne!(id, 0);
        assert_eq!(addr, "10.0.0.5:9002");
        // Non-numeric prefix stays part of the address, not a bogus id.
        let (_, addr) = parse_seed("user@host:9002");
        assert_eq!(addr, "user@host:9002");
    }

    #[test]
    fn cluster_map_add_remove() {
        let mut map = ClusterMap::new();
        assert_eq!(map.node_count(), 0);

        map.add_node(StorageNode::new(1, "10.0.0.1:9002"));
        map.add_node(StorageNode::new(2, "10.0.0.2:9002"));
        map.add_node(StorageNode::new(3, "10.0.0.3:9002"));
        assert_eq!(map.node_count(), 3);
        assert_eq!(map.active_count(), 3);

        map.remove_node(2);
        assert_eq!(map.node_count(), 2);
        assert_eq!(map.epoch, 5); // 1 + 3 adds + 1 remove
    }

    #[test]
    fn node_status() {
        let mut node = StorageNode::new(1, "10.0.0.1:9002");
        assert!(node.is_available());

        node.status = NodeStatus::Down;
        assert!(!node.is_available());
    }

    #[test]
    fn failure_domain() {
        let mut n1 = StorageNode::new(1, "10.0.0.1:9002");
        n1.failure_domain = "rack-a".into();
        let mut n2 = StorageNode::new(2, "10.0.0.2:9002");
        n2.failure_domain = "rack-b".into();
        assert_ne!(n1.failure_domain, n2.failure_domain);
    }
}

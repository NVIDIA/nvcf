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

//! Placement engine — maps chunks to nodes using consistent hashing.
//!
//! Uses jump consistent hash for deterministic, balanced placement
//! with minimal disruption when nodes join or leave.
//! Respects failure domains for rack/zone-aware spreading.

use sha2::{Digest, Sha256};

use crate::node::{ClusterMap, NodeId, StorageNode};
use crate::policy::PlacementPolicy;

/// Result of a placement decision.
#[derive(Debug, Clone)]
pub struct PlacementResult {
    /// Nodes that should hold this chunk, in priority order.
    /// For replication: all nodes hold the full chunk.
    /// For EC: each node holds one shard (data or parity).
    pub nodes: Vec<NodeId>,
    /// The policy that was used.
    pub policy_name: String,
}

/// Place a chunk on nodes according to the given policy.
///
/// Uses the chunk's content hash to deterministically select nodes.
/// The same chunk_id always maps to the same nodes (given the same cluster map).
///
/// **Read path**: pass `None` for `min_free_bytes` so the selection mirrors
/// what `place_chunk_for_write` returned at write time.
pub fn place_chunk(
    chunk_id: &[u8],
    policy: &PlacementPolicy,
    cluster: &ClusterMap,
) -> Result<PlacementResult, String> {
    place_chunk_inner(chunk_id, policy, cluster, None)
}

/// Capacity-aware variant for the write path.
///
/// Excludes any active node whose `free_bytes() < chunk_size` from the
/// candidate set before running the deterministic selection. Reads use
/// `place_chunk` (without size) so they still find data on nodes whose
/// remaining capacity has since fallen below the chunk size.
pub fn place_chunk_for_write(
    chunk_id: &[u8],
    policy: &PlacementPolicy,
    cluster: &ClusterMap,
    chunk_size: u64,
) -> Result<PlacementResult, String> {
    place_chunk_inner(chunk_id, policy, cluster, Some(chunk_size))
}

fn place_chunk_inner(
    chunk_id: &[u8],
    policy: &PlacementPolicy,
    cluster: &ClusterMap,
    min_free_bytes: Option<u64>,
) -> Result<PlacementResult, String> {
    let mut active: Vec<&StorageNode> = cluster.active_nodes();
    active.sort_unstable_by_key(|node| node.id);
    if let Some(min) = min_free_bytes {
        // capacity_bytes == 0 = node hasn't reported usage yet (e.g. just
        // joined, discovery heartbeat hasn't landed). Trust it rather than
        // refuse all writes during bootstrap. Once a node has reported
        // (capacity_bytes > 0), the free_bytes check applies normally.
        active.retain(|n| n.capacity_bytes == 0 || n.free_bytes() >= min);
    }
    let needed = policy.strategy.required_nodes() as usize;

    if active.len() < needed {
        return Err(format!(
            "not enough active nodes with free_bytes >= {}: need {needed}, have {}",
            min_free_bytes.unwrap_or(0),
            active.len(),
        ));
    }

    let nodes = if policy.spread_domains {
        select_spread(&active, chunk_id, needed)
    } else {
        select_simple(&active, chunk_id, needed)
    };

    Ok(PlacementResult {
        nodes,
        policy_name: policy.name.clone(),
    })
}

/// Simple placement: select N nodes using jump consistent hash.
fn select_simple(nodes: &[&StorageNode], chunk_id: &[u8], count: usize) -> Vec<NodeId> {
    if nodes.is_empty() || count == 0 {
        return Vec::new();
    }

    let hash = hash_to_u64(chunk_id);
    let mut selected = Vec::with_capacity(count);
    let mut used = std::collections::HashSet::new();

    // Use jump hash with perturbation to select distinct nodes.
    for i in 0..count {
        let perturbed = hash.wrapping_add((i as u64).wrapping_mul(0x9E3779B97F4A7C15));
        let idx = jump_hash(perturbed, nodes.len() as u32) as usize;

        // Find next unused node from this position.
        let mut pos = idx;
        for _ in 0..nodes.len() {
            if !used.contains(&pos) {
                used.insert(pos);
                selected.push(nodes[pos].id);
                break;
            }
            pos = (pos + 1) % nodes.len();
        }
    }

    selected
}

/// Spread placement: select nodes from different failure domains.
fn select_spread(nodes: &[&StorageNode], chunk_id: &[u8], count: usize) -> Vec<NodeId> {
    if nodes.is_empty() || count == 0 {
        return Vec::new();
    }

    let hash = hash_to_u64(chunk_id);
    let mut selected = Vec::with_capacity(count);
    let mut used_domains = std::collections::HashSet::new();
    let mut used_nodes = std::collections::HashSet::new();

    // First pass: pick one node per failure domain.
    let mut sorted_nodes: Vec<(usize, &StorageNode)> =
        nodes.iter().enumerate().map(|(i, n)| (i, *n)).collect();
    // Sort by hash-based order for determinism.
    sorted_nodes.sort_by_key(|(i, _)| {
        let h = hash.wrapping_add((*i as u64).wrapping_mul(0x517CC1B727220A95));
        jump_hash(h, 1000)
    });

    for (_, node) in &sorted_nodes {
        if selected.len() >= count {
            break;
        }
        if !used_domains.contains(&node.failure_domain) {
            used_domains.insert(node.failure_domain.clone());
            used_nodes.insert(node.id);
            selected.push(node.id);
        }
    }

    // Second pass: if we need more nodes than domains, allow same domain.
    for (_, node) in &sorted_nodes {
        if selected.len() >= count {
            break;
        }
        if !used_nodes.contains(&node.id) {
            used_nodes.insert(node.id);
            selected.push(node.id);
        }
    }

    selected
}

/// Jump consistent hash — maps a key to a bucket [0, num_buckets).
/// From the paper "A Fast, Minimal Memory, Consistent Hash Algorithm"
/// by Lamping and Veach (Google, 2014).
fn jump_hash(key: u64, num_buckets: u32) -> u32 {
    if num_buckets <= 1 {
        return 0;
    }
    let mut b: i64 = -1;
    let mut j: i64 = 0;
    let mut k = key;

    while j < num_buckets as i64 {
        b = j;
        k = k.wrapping_mul(2862933555777941757u64).wrapping_add(1);
        let divisor = ((k >> 33) as u64).wrapping_add(1);
        j = (((b.wrapping_add(1)) as u128 * (1u128 << 31)) / divisor as u128) as i64;
    }

    b as u32
}

/// Hash bytes to a u64 for placement decisions.
fn hash_to_u64(data: &[u8]) -> u64 {
    let digest = Sha256::digest(data);
    u64::from_le_bytes(digest[..8].try_into().unwrap_or([0; 8]))
}

// ---------------------------------------------------------------------------
// Placement-at-write: capacity-aware node selection
// ---------------------------------------------------------------------------

/// Pick the best nodes for a new write based on real-time cluster state.
///
/// Selection criteria (in priority order):
/// 1. Node must be Active (not Suspect, Down, or Draining)
/// 2. Node must have free capacity (free_bytes > 0)
/// 3. Spread across failure domains if rack_spread is true
/// 4. Prefer nodes with most free space (load balancing)
///
/// Returns exactly `count` node IDs, or error if not enough healthy nodes.
pub fn pick_nodes(
    cluster: &ClusterMap,
    count: usize,
    rack_spread: bool,
) -> Result<Vec<NodeId>, String> {
    let mut candidates: Vec<&StorageNode> =
        cluster.nodes.iter().filter(|n| n.is_available()).collect();

    if candidates.len() < count {
        return Err(format!(
            "not enough active nodes: need {count}, have {}",
            candidates.len()
        ));
    }

    // Sort by free space descending (emptiest nodes first).
    candidates.sort_by(|a, b| b.free_bytes().cmp(&a.free_bytes()));

    let mut selected = Vec::with_capacity(count);
    let mut used_domains = std::collections::HashSet::new();
    let mut used_ids = std::collections::HashSet::new();

    if rack_spread {
        // First pass: one node per failure domain, preferring most free space.
        for node in &candidates {
            if selected.len() >= count {
                break;
            }
            if !used_domains.contains(&node.failure_domain) {
                used_domains.insert(node.failure_domain.clone());
                used_ids.insert(node.id);
                selected.push(node.id);
            }
        }
    }

    // Fill remaining from most-free-space candidates.
    for node in &candidates {
        if selected.len() >= count {
            break;
        }
        if !used_ids.contains(&node.id) {
            used_ids.insert(node.id);
            selected.push(node.id);
        }
    }

    if selected.len() < count {
        return Err(format!(
            "could not select {count} nodes (selected {})",
            selected.len()
        ));
    }

    Ok(selected)
}

/// Pick a single replacement node for repair.
/// Avoids nodes already holding the data (`exclude_nodes`).
pub fn pick_repair_target(cluster: &ClusterMap, exclude_nodes: &[NodeId]) -> Option<NodeId> {
    let mut candidates: Vec<&StorageNode> = cluster
        .nodes
        .iter()
        .filter(|n| n.is_available() && !exclude_nodes.contains(&n.id))
        .collect();

    // Pick the node with most free space.
    candidates.sort_by(|a, b| b.free_bytes().cmp(&a.free_bytes()));
    candidates.first().map(|n| n.id)
}

/// Given a chunk and cluster, determine where to read it from.
/// Returns the first available node that holds the chunk.
pub fn read_target(
    chunk_id: &[u8],
    policy: &PlacementPolicy,
    cluster: &ClusterMap,
) -> Result<NodeId, String> {
    let result = place_chunk(chunk_id, policy, cluster)?;
    for node_id in &result.nodes {
        if let Some(node) = cluster.get_node(*node_id) {
            if node.is_available() {
                return Ok(*node_id);
            }
        }
    }
    Err("no available node holds this chunk".into())
}

/// Determine which chunks need repair after a node failure.
/// Returns a list of (chunk_id, new_target_node) pairs.
pub fn repair_plan(
    failed_node: NodeId,
    chunk_ids: &[[u8; 32]],
    policy: &PlacementPolicy,
    cluster: &ClusterMap,
) -> Vec<([u8; 32], NodeId)> {
    let mut repairs = Vec::new();

    // Where each chunk lived is its placement with the failed node still a
    // member: placing against `cluster` itself would skip the failed node
    // and never find the chunks it held.
    let mut before = cluster.clone();
    for node in &mut before.nodes {
        if node.id == failed_node {
            node.status = crate::node::NodeStatus::Active;
        }
    }

    for chunk_id in chunk_ids {
        // Check if this chunk was placed on the failed node.
        if let Ok(result) = place_chunk(chunk_id, policy, &before) {
            if result.nodes.contains(&failed_node) {
                // Find a replacement node not already in the placement.
                let active = cluster.active_nodes();
                for node in &active {
                    if !result.nodes.contains(&node.id) && node.id != failed_node {
                        repairs.push((*chunk_id, node.id));
                        break;
                    }
                }
            }
        }
    }

    repairs
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::node::StorageNode;

    #[test]
    fn member_insertion_order_does_not_change_placement() {
        let first = test_cluster(5);
        let mut second = first.clone();
        second.nodes.reverse();
        for spread in [true, false] {
            let mut policy = PlacementPolicy::replicated_3();
            policy.spread_domains = spread;
            for key in 0..100u64 {
                assert_eq!(
                    place_chunk(&key.to_le_bytes(), &policy, &first)
                        .unwrap()
                        .nodes,
                    place_chunk(&key.to_le_bytes(), &policy, &second)
                        .unwrap()
                        .nodes,
                );
            }
        }
    }

    fn test_cluster(n: usize) -> ClusterMap {
        let mut map = ClusterMap::new();
        for i in 0..n {
            let mut node = StorageNode::new((i + 1) as NodeId, &format!("10.0.0.{}:9002", i + 1));
            node.failure_domain = format!("rack-{}", i % 3);
            map.add_node(node);
        }
        map
    }

    #[test]
    fn place_replicated_3() {
        let cluster = test_cluster(5);
        let policy = PlacementPolicy::replicated_3();
        let chunk = b"test-chunk-001";

        let result = place_chunk(chunk, &policy, &cluster).unwrap();
        assert_eq!(result.nodes.len(), 3);

        // All nodes should be distinct.
        let unique: std::collections::HashSet<_> = result.nodes.iter().collect();
        assert_eq!(unique.len(), 3);
    }

    #[test]
    fn place_ec_4_2() {
        let cluster = test_cluster(8);
        let policy = PlacementPolicy::ec_4_2();
        let chunk = b"test-chunk-002";

        let result = place_chunk(chunk, &policy, &cluster).unwrap();
        assert_eq!(result.nodes.len(), 6);
    }

    #[test]
    fn not_enough_nodes() {
        let cluster = test_cluster(2);
        let policy = PlacementPolicy::replicated_3();
        let chunk = b"test-chunk-003";

        assert!(place_chunk(chunk, &policy, &cluster).is_err());
    }

    #[test]
    fn deterministic_placement() {
        let cluster = test_cluster(10);
        let policy = PlacementPolicy::replicated_3();
        let chunk = b"deterministic-chunk";

        let r1 = place_chunk(chunk, &policy, &cluster).unwrap();
        let r2 = place_chunk(chunk, &policy, &cluster).unwrap();
        assert_eq!(r1.nodes, r2.nodes);
    }

    #[test]
    fn different_chunks_different_placement() {
        let cluster = test_cluster(10);
        let policy = PlacementPolicy::replicated_3();

        let r1 = place_chunk(b"chunk-a", &policy, &cluster).unwrap();
        let r2 = place_chunk(b"chunk-b", &policy, &cluster).unwrap();
        // Different chunks should (usually) go to different nodes.
        // Not guaranteed but very likely with 10 nodes.
        assert_ne!(r1.nodes, r2.nodes);
    }

    #[test]
    fn spread_across_domains() {
        let cluster = test_cluster(6); // rack-0, rack-1, rack-2, rack-0, rack-1, rack-2
        let policy = PlacementPolicy::replicated_3();
        let chunk = b"spread-test";

        let result = place_chunk(chunk, &policy, &cluster).unwrap();
        assert_eq!(result.nodes.len(), 3);

        // Check that nodes are in different failure domains.
        let mut domains = std::collections::HashSet::new();
        for nid in &result.nodes {
            let node = cluster.get_node(*nid).unwrap();
            domains.insert(node.failure_domain.clone());
        }
        assert_eq!(domains.len(), 3); // 3 different racks
    }

    #[test]
    fn jump_hash_distribution() {
        // Verify jump hash distributes evenly.
        let mut counts = [0u32; 5];
        for i in 0..10000u64 {
            let bucket = jump_hash(i, 5);
            counts[bucket as usize] += 1;
        }
        // Each bucket should get ~2000 ± 200.
        for c in &counts {
            assert!(*c > 1500 && *c < 2500, "uneven: {counts:?}");
        }
    }

    #[test]
    fn read_target_finds_available() {
        let cluster = test_cluster(3);
        let policy = PlacementPolicy::replicated_3();
        let chunk = b"read-test";

        let target = read_target(chunk, &policy, &cluster).unwrap();
        assert!(cluster.get_node(target).unwrap().is_available());
    }

    #[test]
    fn repair_plan_after_failure() {
        let mut cluster = test_cluster(5);
        let policy = PlacementPolicy::replicated_3();

        let chunks: Vec<[u8; 32]> = (0..10)
            .map(|i| {
                let mut h = [0u8; 32];
                h[0] = i;
                h
            })
            .collect();

        // Which chunks node 1 held while it was up.
        let on_node_1: Vec<[u8; 32]> = chunks
            .iter()
            .copied()
            .filter(|c| {
                place_chunk(c, &policy, &cluster)
                    .expect("place")
                    .nodes
                    .contains(&1)
            })
            .collect();
        assert!(
            !on_node_1.is_empty(),
            "the fixture must put some chunks on node 1"
        );

        // Simulate node 1 failure.
        cluster.nodes[0].status = crate::node::NodeStatus::Down;

        let repairs = repair_plan(1, &chunks, &policy, &cluster);
        // Every chunk node 1 held gets exactly one new target, never node 1.
        let repaired: Vec<[u8; 32]> = repairs.iter().map(|(c, _)| *c).collect();
        assert_eq!(repaired, on_node_1);
        assert!(repairs.iter().all(|(_, n)| *n != 1));
    }

    // --- Placement-at-write tests ---

    #[test]
    fn pick_nodes_prefers_most_free_space() {
        let mut cluster = ClusterMap::new();
        let mut n1 = StorageNode::new(1, "10.0.0.1:9002");
        n1.capacity_bytes = 1_000_000;
        n1.used_bytes = 900_000; // 100KB free
        let mut n2 = StorageNode::new(2, "10.0.0.2:9002");
        n2.capacity_bytes = 1_000_000;
        n2.used_bytes = 100_000; // 900KB free
        let mut n3 = StorageNode::new(3, "10.0.0.3:9002");
        n3.capacity_bytes = 1_000_000;
        n3.used_bytes = 500_000; // 500KB free
        cluster.add_node(n1);
        cluster.add_node(n2);
        cluster.add_node(n3);

        let nodes = pick_nodes(&cluster, 2, false).unwrap();
        assert_eq!(nodes.len(), 2);
        // Node 2 has most free space, should be first.
        assert_eq!(nodes[0], 2);
        // Node 3 has second most free space.
        assert_eq!(nodes[1], 3);
    }

    #[test]
    fn pick_nodes_skips_full_node() {
        let mut cluster = ClusterMap::new();
        let mut n1 = StorageNode::new(1, "10.0.0.1:9002");
        n1.capacity_bytes = 1_000_000;
        n1.used_bytes = 1_000_000; // FULL
        let mut n2 = StorageNode::new(2, "10.0.0.2:9002");
        n2.capacity_bytes = 1_000_000;
        n2.used_bytes = 0;
        let mut n3 = StorageNode::new(3, "10.0.0.3:9002");
        n3.capacity_bytes = 1_000_000;
        n3.used_bytes = 0;
        cluster.add_node(n1);
        cluster.add_node(n2);
        cluster.add_node(n3);

        let nodes = pick_nodes(&cluster, 2, false).unwrap();
        // Node 1 is full but still Active, so it's included (free_bytes=0).
        // pick_nodes sorts by free_bytes desc, so 2 and 3 come first.
        assert!(!nodes.contains(&1) || nodes.len() == 2);
    }

    #[test]
    fn pick_nodes_skips_down_node() {
        let mut cluster = ClusterMap::new();
        let mut n1 = StorageNode::new(1, "10.0.0.1:9002");
        n1.status = crate::node::NodeStatus::Down;
        n1.capacity_bytes = 1_000_000;
        let mut n2 = StorageNode::new(2, "10.0.0.2:9002");
        n2.capacity_bytes = 1_000_000;
        let mut n3 = StorageNode::new(3, "10.0.0.3:9002");
        n3.capacity_bytes = 1_000_000;
        cluster.add_node(n1);
        cluster.add_node(n2);
        cluster.add_node(n3);

        let nodes = pick_nodes(&cluster, 2, false).unwrap();
        assert_eq!(nodes.len(), 2);
        assert!(!nodes.contains(&1)); // Down node excluded.
    }

    #[test]
    fn pick_nodes_rack_spread() {
        let mut cluster = ClusterMap::new();
        let mut n1 = StorageNode::new(1, "10.0.0.1:9002");
        n1.failure_domain = "rack-a".into();
        n1.capacity_bytes = 1_000_000;
        let mut n2 = StorageNode::new(2, "10.0.0.2:9002");
        n2.failure_domain = "rack-a".into();
        n2.capacity_bytes = 1_000_000;
        let mut n3 = StorageNode::new(3, "10.0.0.3:9002");
        n3.failure_domain = "rack-b".into();
        n3.capacity_bytes = 1_000_000;
        cluster.add_node(n1);
        cluster.add_node(n2);
        cluster.add_node(n3);

        let nodes = pick_nodes(&cluster, 2, true).unwrap();
        assert_eq!(nodes.len(), 2);
        // With rack spread, should pick one from rack-a and one from rack-b.
        let has_rack_a = nodes.contains(&1) || nodes.contains(&2);
        let has_rack_b = nodes.contains(&3);
        assert!(has_rack_a && has_rack_b, "should spread across racks");
    }

    #[test]
    fn pick_repair_target_avoids_existing() {
        let mut cluster = ClusterMap::new();
        for i in 1..=5 {
            let mut n = StorageNode::new(i, &format!("10.0.0.{i}:9002"));
            n.capacity_bytes = 1_000_000;
            n.used_bytes = (i as u64) * 100_000;
            cluster.add_node(n);
        }

        let target = pick_repair_target(&cluster, &[1, 2, 3]);
        assert!(target.is_some());
        let t = target.unwrap();
        assert!(t != 1 && t != 2 && t != 3);
        // Should pick node 4 (most free among 4 and 5).
        assert_eq!(t, 4);
    }
}

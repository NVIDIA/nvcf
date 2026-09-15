/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

use crate::cassandra::cassandra_service::CassandraServiceManager;
use anyhow::Result;
use std::collections::HashMap;
use std::sync::{Arc, RwLock};
use std::time::Duration;
use tokio::task::JoinHandle;

pub const BUCKET_COUNT: usize = 128;
// We don't expect topology to change often, so we can cache them for a while
const CACHE_REFRESH_INTERVAL: Duration = Duration::from_secs(180);

/// Cached bucket assignments for the current node
#[derive(Debug, Clone)]
struct CachedBucketAssignment {
    buckets: Vec<usize>,
    bucket_ranges: HashMap<usize, (i64, i64)>,
}

pub struct NodeBucketManager {
    cached_assignment: Arc<RwLock<Option<CachedBucketAssignment>>>,
    background_handle: JoinHandle<()>,
}

impl NodeBucketManager {
    /// Creates a new NodeBucketManager and starts the background task
    pub fn new(node_id: String, cassandra_service: Arc<CassandraServiceManager>) -> Arc<Self> {
        let cached_assignment = Arc::new(RwLock::new(None));
        let background_handle = Self::start_background_task(
            node_id.clone(),
            cassandra_service.clone(),
            cached_assignment.clone(),
        );

        Arc::new(Self {
            cached_assignment,
            background_handle,
        })
    }

    /// Starts the background task for bucket management
    fn start_background_task(
        node_id: String,
        cassandra_service: Arc<CassandraServiceManager>,
        cached_assignment: Arc<RwLock<Option<CachedBucketAssignment>>>,
    ) -> JoinHandle<()> {
        tokio::spawn(async move {
            Self::update_buckets(&cassandra_service, &node_id, &cached_assignment).await;
            let mut interval = tokio::time::interval(CACHE_REFRESH_INTERVAL);
            loop {
                // Update buckets on interval
                interval.tick().await;
                Self::update_buckets(&cassandra_service, &node_id, &cached_assignment).await;
            }
        })
    }

    pub fn get_my_buckets(&self) -> Vec<usize> {
        if let Some(cached) = self.cached_assignment.read().unwrap().as_ref() {
            cached.buckets.clone()
        } else {
            vec![]
        }
    }

    pub fn get_range_for_bucket(&self, bucket_index: usize) -> Option<(i64, i64)> {
        if let Some(cached) = self.cached_assignment.read().unwrap().as_ref() {
            cached.bucket_ranges.get(&bucket_index).copied()
        } else {
            None
        }
    }

    pub fn get_all_bucket_ranges(&self) -> HashMap<usize, (i64, i64)> {
        if let Some(cached) = self.cached_assignment.read().unwrap().as_ref() {
            cached.bucket_ranges.clone()
        } else {
            HashMap::new()
        }
    }
}

impl Drop for NodeBucketManager {
    fn drop(&mut self) {
        self.background_handle.abort();
    }
}

impl NodeBucketManager {
    /// Internal method to update bucket assignments
    async fn update_buckets(
        cassandra_service: &CassandraServiceManager,
        node_id: &str,
        cached_assignment: &Arc<RwLock<Option<CachedBucketAssignment>>>,
    ) {
        match Self::fetch_buckets_for_node(cassandra_service, node_id).await {
            Ok(buckets) => {
                if buckets.is_empty() {
                    // Means the node is not healthy, we must set the cached assignment to empty
                    *cached_assignment.write().unwrap() = None;
                } else {
                    // Create bucket ranges map for the assigned buckets
                    let bucket_ranges: HashMap<usize, (i64, i64)> = buckets
                        .iter()
                        .map(|&bucket_index| {
                            (bucket_index, get_token_range_for_bucket(bucket_index))
                        })
                        .collect();

                    *cached_assignment.write().unwrap() = Some(CachedBucketAssignment {
                        buckets,
                        bucket_ranges,
                    });
                }
            }
            Err(e) => {
                // Cassandra is not healthy, we will try to use the cached assignment if it exists until node goes down or becomes healthy again
                tracing::error!("Failed to update bucket assignments: {}", e);
            }
        }
    }

    /// Internal method to fetch bucket assignments from Cassandra
    async fn fetch_buckets_for_node(
        cassandra_service: &CassandraServiceManager,
        node_id: &str,
    ) -> Result<Vec<usize>> {
        let mut nodes = cassandra_service.get_all_nodes().await?;
        nodes.sort_by(|a, b| a.node_id.cmp(&b.node_id));

        if nodes.is_empty() {
            // Cassandra is not healthy, we must set the cached assignment to empty
            return Ok(vec![]);
        }
        // If we're not in the list (e.g. we removed ourselves because we're not ready), we get no buckets
        if !nodes.iter().any(|n| n.node_id == node_id) {
            return Ok(vec![]);
        }
        Self::fetch_buckets_for_node_internal(nodes, node_id).await
    }

    async fn fetch_buckets_for_node_internal(
        mut nodes: Vec<crate::models::NodeHealth>,
        node_id: &str,
    ) -> Result<Vec<usize>> {
        // Sort nodes lexicographically by node_id for consistent bucket assignment
        nodes.sort_by(|a, b| a.node_id.cmp(&b.node_id));

        // Find the index of our node
        let node_index = nodes
            .iter()
            .position(|node| node.node_id == node_id)
            .ok_or_else(|| anyhow::anyhow!("Node '{}' not found in healthy nodes", node_id))?;

        let total_nodes = nodes.len();
        let buckets_per_node = BUCKET_COUNT / total_nodes;
        let remaining_buckets = BUCKET_COUNT % total_nodes;

        let buckets_for_this_node = if node_index < remaining_buckets {
            buckets_per_node + 1 // First 'remaining_buckets' nodes get one extra bucket
        } else {
            buckets_per_node
        };

        // Calculate starting bucket index for this node
        let mut start_bucket = node_index * buckets_per_node;
        if node_index < remaining_buckets {
            start_bucket += node_index; // Add extra buckets for previous nodes
        } else {
            start_bucket += remaining_buckets; // Add all remaining buckets distributed to earlier nodes
        }

        let bucket_list: Vec<usize> =
            (start_bucket..start_bucket + buckets_for_this_node).collect();
        Ok(bucket_list)
    }
}

/// Get the token range (start, end) for a specific bucket index
fn get_token_range_for_bucket(bucket_index: usize) -> (i64, i64) {
    let [min_token, max_token] = crate::cassandra::CASSANDRA_TOKEN_RANGE;

    // Use i128 to avoid overflow when calculating range size
    let token_range_size = (max_token as i128) - (min_token as i128);
    let bucket_size = token_range_size / BUCKET_COUNT as i128;

    let start_token = min_token as i128 + (bucket_index as i128 * bucket_size);
    let end_token = if bucket_index == BUCKET_COUNT - 1 {
        // Last bucket gets any remaining range
        max_token as i128
    } else {
        min_token as i128 + ((bucket_index + 1) as i128 * bucket_size) - 1
    };

    (start_token as i64, end_token as i64)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Cassandra/Scylla Murmur3 token computation, copied verbatim from
    /// scylla-1.3.0/src/routing/partitioner.rs so the result is identical to what
    /// the real driver produces.
    fn cassandra_murmur3_token(data: &[u8]) -> i64 {
        use std::num::Wrapping;

        const C1: Wrapping<i64> = Wrapping(0x87c37b91114253d5_u64 as i64);
        const C2: Wrapping<i64> = Wrapping(0x4cf5ad432745937f_u64 as i64);

        let rotl64 = |v: Wrapping<i64>, n: u32| -> Wrapping<i64> {
            Wrapping((v.0 << n) | (v.0 as u64 >> (64 - n)) as i64)
        };
        let fmix = |mut k: Wrapping<i64>| -> Wrapping<i64> {
            k ^= Wrapping((k.0 as u64 >> 33) as i64);
            k *= Wrapping(0xff51afd7ed558ccd_u64 as i64);
            k ^= Wrapping((k.0 as u64 >> 33) as i64);
            k *= Wrapping(0xc4ceb9fe1a85ec53_u64 as i64);
            k ^= Wrapping((k.0 as u64 >> 33) as i64);
            k
        };

        let mut h1 = Wrapping(0_i64);
        let mut h2 = Wrapping(0_i64);
        let total_len = data.len();
        let nblocks = total_len / 16;

        for i in 0..nblocks {
            let off = i * 16;
            let k1 = Wrapping(i64::from_le_bytes(data[off..off + 8].try_into().unwrap()));
            let k2 = Wrapping(i64::from_le_bytes(
                data[off + 8..off + 16].try_into().unwrap(),
            ));

            let mut kk1 = k1 * C1;
            kk1 = rotl64(kk1, 31);
            kk1 *= C2;
            h1 ^= kk1;
            h1 = rotl64(h1, 27);
            h1 += h2;
            h1 = h1 * Wrapping(5) + Wrapping(0x52dce729);

            let mut kk2 = k2 * C2;
            kk2 = rotl64(kk2, 33);
            kk2 *= C1;
            h2 ^= kk2;
            h2 = rotl64(h2, 31);
            h2 += h1;
            h2 = h2 * Wrapping(5) + Wrapping(0x38495ab5);
        }

        let tail = &data[nblocks * 16..];
        let buf_len = tail.len();
        let mut k1 = Wrapping(0_i64);
        let mut k2 = Wrapping(0_i64);

        if buf_len > 8 {
            for i in (8..buf_len).rev() {
                k2 ^= Wrapping(tail[i] as i8 as i64) << ((i - 8) * 8);
            }
            k2 *= C2;
            k2 = rotl64(k2, 33);
            k2 *= C1;
            h2 ^= k2;
        }
        if buf_len > 0 {
            for i in (0..std::cmp::min(8, buf_len)).rev() {
                k1 ^= Wrapping(tail[i] as i8 as i64) << (i * 8);
            }
            k1 *= C1;
            k1 = rotl64(k1, 31);
            k1 *= C2;
            h1 ^= k1;
        }

        h1 ^= Wrapping(total_len as i64);
        h2 ^= Wrapping(total_len as i64);
        h1 += h2;
        h2 += h1;
        h1 = fmix(h1);
        h2 = fmix(h2);
        h1 += h2;
        h2 += h1;

        // Token = lower 64 bits of (h2 << 64 | h1) cast to i64 = h1
        (((h2.0 as i128) << 64) | h1.0 as i128) as i64
    }

    /// Serialize a composite partition key in Cassandra wire format:
    /// for each component: [2-byte big-endian length][bytes][0x00 terminator]
    fn composite_key(components: &[&[u8]]) -> Vec<u8> {
        let mut out = Vec::new();
        for comp in components {
            let len = comp.len() as u16;
            out.extend_from_slice(&len.to_be_bytes());
            out.extend_from_slice(comp);
            out.push(0x00);
        }
        out
    }

    #[test]
    fn test_bucket_for_known_function() {
        // function_id = d09f001a-a689-413b-b656-1f1a177dd582
        // function_version_id = a1f69be7-cf76-4dfb-b2d0-f2e91c19b065
        // d09f001a-a689-413b-b656-1f1a177dd582
        let fid: [u8; 16] = [
            0xd0, 0x9f, 0x00, 0x1a, 0xa6, 0x89, 0x41, 0x3b, 0xb6, 0x56, 0x1f, 0x1a, 0x17, 0x7d,
            0xd5, 0x82,
        ];
        // a1f69be7-cf76-4dfb-b2d0-f2e91c19b065
        let fvid: [u8; 16] = [
            0xa1, 0xf6, 0x9b, 0xe7, 0xcf, 0x76, 0x4d, 0xfb, 0xb2, 0xd0, 0xf2, 0xe9, 0x1c, 0x19,
            0xb0, 0x65,
        ];
        let key = composite_key(&[&fid, &fvid]);
        let token = cassandra_murmur3_token(&key);

        let [min_token, _] = crate::cassandra::CASSANDRA_TOKEN_RANGE;
        let total_range: i128 = (i64::MAX as i128) - (i64::MIN as i128);
        let bucket_size = total_range / BUCKET_COUNT as i128;
        let bucket = ((token as i128 - min_token as i128) / bucket_size)
            .clamp(0, BUCKET_COUNT as i128 - 1) as usize;

        println!("token={token}  bucket={bucket}");
        assert!(bucket < BUCKET_COUNT);
    }

    #[test]
    fn test_get_token_range_for_bucket() {
        let [min_token, max_token] = crate::cassandra::CASSANDRA_TOKEN_RANGE;

        // Test first bucket starts at min_token
        let (start, _) = get_token_range_for_bucket(0);
        assert_eq!(start, min_token);

        // Test last bucket ends at max_token
        let (_, end) = get_token_range_for_bucket(BUCKET_COUNT - 1);
        assert_eq!(end, max_token);

        // Test buckets are contiguous (no gaps)
        for i in 0..10 {
            let (_, end1) = get_token_range_for_bucket(i);
            let (start2, _) = get_token_range_for_bucket(i + 1);
            assert_eq!(end1 + 1, start2, "Gap between bucket {} and {}", i, i + 1);
        }

        // Test all ranges are valid (start <= end)
        for i in 0..10 {
            let (start, end) = get_token_range_for_bucket(i);
            assert!(
                start <= end,
                "Bucket {} has invalid range: {} > {}",
                i,
                start,
                end
            );
        }
    }

    #[test]
    fn test_complete_token_coverage() {
        // Verify all buckets together cover the complete token range exactly once
        let [min_token, max_token] = crate::cassandra::CASSANDRA_TOKEN_RANGE;

        let mut all_ranges = Vec::new();
        for i in 0..BUCKET_COUNT {
            all_ranges.push(get_token_range_for_bucket(i));
        }

        // Sort by start token
        all_ranges.sort_by_key(|&(start, _)| start);

        // Check coverage is complete and non-overlapping
        assert_eq!(
            all_ranges[0].0, min_token,
            "First bucket doesn't start at min token"
        );
        assert_eq!(
            all_ranges.last().unwrap().1,
            max_token,
            "Last bucket doesn't end at max token"
        );

        // Check no gaps or overlaps
        for i in 0..all_ranges.len() - 1 {
            let (_, end_current) = all_ranges[i];
            let (start_next, _) = all_ranges[i + 1];
            assert_eq!(
                end_current + 1,
                start_next,
                "Gap or overlap between buckets {} and {}",
                i,
                i + 1
            );
        }
    }

    #[tokio::test]
    async fn test_lexicographical_node_sorting() {
        use crate::models::NodeHealth;
        use chrono::Utc;

        // Create test nodes with different node_ids (not in alphabetical order)
        let nodes = vec![
            NodeHealth {
                node_id: "node-zebra".to_string(),
                last_updated_at: Utc::now(),
            },
            NodeHealth {
                node_id: "node-alpha".to_string(),
                last_updated_at: Utc::now(),
            },
            NodeHealth {
                node_id: "node-charlie".to_string(),
                last_updated_at: Utc::now(),
            },
            NodeHealth {
                node_id: "node-beta".to_string(),
                last_updated_at: Utc::now(),
            },
        ];

        // Test bucket assignment for each node
        let alpha_buckets =
            NodeBucketManager::fetch_buckets_for_node_internal(nodes.clone(), "node-alpha")
                .await
                .unwrap();
        let beta_buckets =
            NodeBucketManager::fetch_buckets_for_node_internal(nodes.clone(), "node-beta")
                .await
                .unwrap();
        let charlie_buckets =
            NodeBucketManager::fetch_buckets_for_node_internal(nodes.clone(), "node-charlie")
                .await
                .unwrap();
        let zebra_buckets =
            NodeBucketManager::fetch_buckets_for_node_internal(nodes.clone(), "node-zebra")
                .await
                .unwrap();

        // Verify lexicographical ordering affects bucket assignment
        // node-alpha should be index 0, node-beta index 1, node-charlie index 2, node-zebra index 3

        // With 4 nodes, each should get BUCKET_COUNT/4 buckets, distributed based on their sorted position
        let expected_buckets_per_node = BUCKET_COUNT / 4;
        assert_eq!(
            alpha_buckets.len(),
            expected_buckets_per_node,
            "node-alpha should get {} buckets",
            expected_buckets_per_node
        );
        assert_eq!(
            beta_buckets.len(),
            expected_buckets_per_node,
            "node-beta should get {} buckets",
            expected_buckets_per_node
        );
        assert_eq!(
            charlie_buckets.len(),
            expected_buckets_per_node,
            "node-charlie should get {} buckets",
            expected_buckets_per_node
        );
        assert_eq!(
            zebra_buckets.len(),
            expected_buckets_per_node,
            "node-zebra should get {} buckets",
            expected_buckets_per_node
        );

        // Verify no bucket overlap between nodes
        let mut all_assigned_buckets: Vec<usize> = Vec::new();
        all_assigned_buckets.extend(&alpha_buckets);
        all_assigned_buckets.extend(&beta_buckets);
        all_assigned_buckets.extend(&charlie_buckets);
        all_assigned_buckets.extend(&zebra_buckets);

        all_assigned_buckets.sort();
        all_assigned_buckets.dedup();
        assert_eq!(
            all_assigned_buckets.len(),
            BUCKET_COUNT,
            "All {} buckets should be assigned exactly once",
            BUCKET_COUNT
        );

        // Verify alphabetical ordering: alpha < beta < charlie < zebra
        assert!(
            alpha_buckets[0] < beta_buckets[0],
            "node-alpha should get lower-numbered buckets than node-beta"
        );
        assert!(
            beta_buckets[0] < charlie_buckets[0],
            "node-beta should get lower-numbered buckets than node-charlie"
        );
        assert!(
            charlie_buckets[0] < zebra_buckets[0],
            "node-charlie should get lower-numbered buckets than node-zebra"
        );
    }
}

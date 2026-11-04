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

//! Placement policies — replication and erasure coding configuration.

use serde::{Deserialize, Serialize};

/// How data is protected across nodes.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum PlacementStrategy {
    /// Simple replication: N identical copies.
    Replicated {
        /// Number of replicas (including the primary).
        replica_count: u32,
    },
    /// Erasure coding: k data shards + m parity shards.
    ErasureCoded {
        /// Number of data shards.
        data_shards: u32,
        /// Number of parity shards.
        parity_shards: u32,
    },
}

impl PlacementStrategy {
    /// Total number of nodes needed for this strategy.
    pub fn required_nodes(&self) -> u32 {
        match self {
            Self::Replicated { replica_count } => *replica_count,
            Self::ErasureCoded {
                data_shards,
                parity_shards,
            } => data_shards + parity_shards,
        }
    }

    /// Number of node failures that can be tolerated.
    pub fn fault_tolerance(&self) -> u32 {
        match self {
            Self::Replicated { replica_count } => replica_count.saturating_sub(1),
            Self::ErasureCoded { parity_shards, .. } => *parity_shards,
        }
    }

    /// Storage overhead factor (1.0 = no overhead).
    pub fn overhead(&self) -> f64 {
        match self {
            Self::Replicated { replica_count } => *replica_count as f64,
            Self::ErasureCoded {
                data_shards,
                parity_shards,
            } => (*data_shards + *parity_shards) as f64 / *data_shards as f64,
        }
    }
}

impl Default for PlacementStrategy {
    fn default() -> Self {
        Self::Replicated { replica_count: 3 }
    }
}

/// Placement policy for a bucket or volume.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PlacementPolicy {
    /// Name of this policy.
    pub name: String,
    /// Data protection strategy.
    pub strategy: PlacementStrategy,
    /// Spread replicas across failure domains (rack-aware placement).
    pub spread_domains: bool,
    /// Minimum number of active nodes required for writes.
    /// For replication: must write to this many nodes before ack.
    /// For EC: must write all k+m shards.
    pub min_write_nodes: u32,
}

impl PlacementPolicy {
    /// 3-way replication (default, good for small clusters).
    pub fn replicated_3() -> Self {
        Self {
            name: "replicated-3".into(),
            strategy: PlacementStrategy::Replicated { replica_count: 3 },
            spread_domains: true,
            min_write_nodes: 2, // Write to 2 of 3 before ack (quorum)
        }
    }

    /// 2-way replication (faster, less protection).
    pub fn replicated_2() -> Self {
        Self {
            name: "replicated-2".into(),
            strategy: PlacementStrategy::Replicated { replica_count: 2 },
            spread_domains: true,
            min_write_nodes: 2,
        }
    }

    /// EC 4+2 (storage efficient, good for large data).
    pub fn ec_4_2() -> Self {
        Self {
            name: "ec-4-2".into(),
            strategy: PlacementStrategy::ErasureCoded {
                data_shards: 4,
                parity_shards: 2,
            },
            spread_domains: true,
            min_write_nodes: 6,
        }
    }

    /// EC 8+3 (very efficient, for large clusters).
    pub fn ec_8_3() -> Self {
        Self {
            name: "ec-8-3".into(),
            strategy: PlacementStrategy::ErasureCoded {
                data_shards: 8,
                parity_shards: 3,
            },
            spread_domains: true,
            min_write_nodes: 11,
        }
    }

    /// Single copy (no redundancy, for scratch/temp data).
    pub fn single() -> Self {
        Self {
            name: "single".into(),
            strategy: PlacementStrategy::Replicated { replica_count: 1 },
            spread_domains: false,
            min_write_nodes: 1,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn replication_properties() {
        let s = PlacementStrategy::Replicated { replica_count: 3 };
        assert_eq!(s.required_nodes(), 3);
        assert_eq!(s.fault_tolerance(), 2);
        assert_eq!(s.overhead(), 3.0);
    }

    #[test]
    fn ec_properties() {
        let s = PlacementStrategy::ErasureCoded {
            data_shards: 4,
            parity_shards: 2,
        };
        assert_eq!(s.required_nodes(), 6);
        assert_eq!(s.fault_tolerance(), 2);
        assert_eq!(s.overhead(), 1.5);
    }

    #[test]
    fn ec_8_3_properties() {
        let s = PlacementStrategy::ErasureCoded {
            data_shards: 8,
            parity_shards: 3,
        };
        assert_eq!(s.required_nodes(), 11);
        assert_eq!(s.fault_tolerance(), 3);
        assert!((s.overhead() - 1.375).abs() < 0.01);
    }

    #[test]
    fn default_policy() {
        let p = PlacementPolicy::replicated_3();
        assert_eq!(p.strategy.required_nodes(), 3);
        assert_eq!(p.min_write_nodes, 2);
        assert!(p.spread_domains);
    }

    #[test]
    fn single_copy() {
        let p = PlacementPolicy::single();
        assert_eq!(p.strategy.fault_tolerance(), 0);
        assert_eq!(p.strategy.overhead(), 1.0);
    }
}

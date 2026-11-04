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

//! Reed-Solomon erasure coding for data durability.
//!
//! Splits data into k data shards + m parity shards.
//! Can reconstruct from any k of the k+m shards.
//! Default: 4+2 (1.5x overhead, tolerates 2 failures).

use nvstor_core::error::{NvstorError, Result};
use reed_solomon_erasure::galois_8::ReedSolomon;
use serde::{Deserialize, Serialize};

/// Erasure coding configuration.
#[derive(Debug, Clone)]
pub struct ErasureConfig {
    rs: ReedSolomon,
    k: usize,
    m: usize,
}

/// Metadata needed to reconstruct data from shards.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ShardInfo {
    pub original_len: usize,
    pub shard_size: usize,
    pub data_shards: usize,
    pub parity_shards: usize,
}

impl ErasureConfig {
    /// Create with k data shards and m parity shards.
    pub fn new(k: usize, m: usize) -> Result<Self> {
        if k == 0 || m == 0 {
            return Err(NvstorError::InvalidArgument(
                "data_shards and parity_shards must be > 0".into(),
            ));
        }
        let rs = ReedSolomon::new(k, m)
            .map_err(|e| NvstorError::Internal(format!("reed-solomon init: {e}")))?;
        Ok(Self { rs, k, m })
    }

    /// Default 4+2 configuration.
    pub fn default_4_2() -> Self {
        Self::new(4, 2).expect("4+2 is always valid")
    }

    /// Encode data into k+m shards.
    pub fn encode(&self, data: &[u8]) -> Result<(Vec<Vec<u8>>, ShardInfo)> {
        if data.is_empty() {
            // Special case: empty data produces empty shards with size 1
            let info = ShardInfo {
                original_len: 0,
                shard_size: 1,
                data_shards: self.k,
                parity_shards: self.m,
            };
            let mut shards = vec![vec![0u8; 1]; self.k + self.m];
            self.rs
                .encode(&mut shards)
                .map_err(|e| NvstorError::Internal(format!("encode empty: {e}")))?;
            return Ok((shards, info));
        }
        let shard_size = (data.len() + self.k - 1) / self.k;
        let padded_len = shard_size * self.k;

        // Create data shards (padded)
        let mut all_data = data.to_vec();
        all_data.resize(padded_len, 0);

        let mut shards: Vec<Vec<u8>> = all_data.chunks(shard_size).map(|c| c.to_vec()).collect();

        // Add empty parity shards
        for _ in 0..self.m {
            shards.push(vec![0u8; shard_size]);
        }

        // Compute parity
        self.rs
            .encode(&mut shards)
            .map_err(|e| NvstorError::Internal(format!("encode: {e}")))?;

        let info = ShardInfo {
            original_len: data.len(),
            shard_size,
            data_shards: self.k,
            parity_shards: self.m,
        };

        Ok((shards, info))
    }

    /// Reconstruct data from shards. Missing shards are None.
    pub fn decode(&self, shards: &mut [Option<Vec<u8>>], info: &ShardInfo) -> Result<Vec<u8>> {
        if shards.len() != info.data_shards + info.parity_shards {
            return Err(NvstorError::InvalidArgument(format!(
                "expected {} shards, got {}",
                info.data_shards + info.parity_shards,
                shards.len()
            )));
        }

        self.rs
            .reconstruct(shards)
            .map_err(|e| NvstorError::Internal(format!("reconstruct: {e}")))?;

        // Reassemble data shards
        let mut result = Vec::with_capacity(info.original_len);
        for shard in shards.iter().take(info.data_shards) {
            if let Some(s) = shard {
                result.extend_from_slice(s);
            }
        }
        result.truncate(info.original_len);

        Ok(result)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn roundtrip_basic() {
        let ec = ErasureConfig::default_4_2();
        let data = b"hello erasure coding for nvstor storage";
        let (shards, info) = ec.encode(data).unwrap();
        assert_eq!(shards.len(), 6); // 4+2

        let mut opts: Vec<Option<Vec<u8>>> = shards.into_iter().map(Some).collect();
        let recovered = ec.decode(&mut opts, &info).unwrap();
        assert_eq!(recovered, data);
    }

    #[test]
    fn decode_with_one_missing() {
        let ec = ErasureConfig::default_4_2();
        let data = b"can recover from one missing shard";
        let (shards, info) = ec.encode(data).unwrap();

        let mut opts: Vec<Option<Vec<u8>>> = shards.into_iter().map(Some).collect();
        opts[2] = None; // Remove one data shard
        let recovered = ec.decode(&mut opts, &info).unwrap();
        assert_eq!(recovered, data);
    }

    #[test]
    fn decode_with_max_missing() {
        let ec = ErasureConfig::default_4_2();
        let data = b"can recover from two missing shards (m=2)";
        let (shards, info) = ec.encode(data).unwrap();

        let mut opts: Vec<Option<Vec<u8>>> = shards.into_iter().map(Some).collect();
        opts[0] = None; // Remove 2 shards (max for m=2)
        opts[3] = None;
        let recovered = ec.decode(&mut opts, &info).unwrap();
        assert_eq!(recovered, data);
    }

    #[test]
    fn decode_fails_too_many_missing() {
        let ec = ErasureConfig::default_4_2();
        let data = b"cannot recover from three missing (m=2)";
        let (shards, info) = ec.encode(data).unwrap();

        let mut opts: Vec<Option<Vec<u8>>> = shards.into_iter().map(Some).collect();
        opts[0] = None;
        opts[1] = None;
        opts[2] = None; // 3 missing > m=2
        assert!(ec.decode(&mut opts, &info).is_err());
    }

    #[test]
    fn small_data() {
        let ec = ErasureConfig::default_4_2();
        let data = b"x";
        let (shards, info) = ec.encode(data).unwrap();
        let mut opts: Vec<Option<Vec<u8>>> = shards.into_iter().map(Some).collect();
        assert_eq!(ec.decode(&mut opts, &info).unwrap(), data);
    }

    #[test]
    fn exact_multiple() {
        let ec = ErasureConfig::default_4_2();
        let data = vec![0xAB; 400]; // Exactly divisible by k=4
        let (shards, info) = ec.encode(&data).unwrap();
        assert_eq!(info.shard_size, 100);
        let mut opts: Vec<Option<Vec<u8>>> = shards.into_iter().map(Some).collect();
        assert_eq!(ec.decode(&mut opts, &info).unwrap(), data);
    }

    #[test]
    fn large_data() {
        let ec = ErasureConfig::default_4_2();
        let data = vec![0xCD; 100_000];
        let (shards, info) = ec.encode(&data).unwrap();
        let mut opts: Vec<Option<Vec<u8>>> = shards.into_iter().map(Some).collect();
        opts[5] = None; // Remove one parity shard
        assert_eq!(ec.decode(&mut opts, &info).unwrap(), data);
    }

    #[test]
    fn custom_config_8_4() {
        let ec = ErasureConfig::new(8, 4).unwrap();
        let data = vec![0xEF; 1000];
        let (shards, info) = ec.encode(&data).unwrap();
        assert_eq!(shards.len(), 12);

        let mut opts: Vec<Option<Vec<u8>>> = shards.into_iter().map(Some).collect();
        opts[0] = None;
        opts[3] = None;
        opts[7] = None;
        opts[11] = None; // 4 missing = max for m=4
        assert_eq!(ec.decode(&mut opts, &info).unwrap(), data);
    }

    #[test]
    fn empty_data() {
        let ec = ErasureConfig::default_4_2();
        let data = b"";
        let (shards, info) = ec.encode(data).unwrap();
        let mut opts: Vec<Option<Vec<u8>>> = shards.into_iter().map(Some).collect();
        assert_eq!(ec.decode(&mut opts, &info).unwrap(), data);
    }

    #[test]
    fn invalid_config() {
        assert!(ErasureConfig::new(0, 2).is_err());
        assert!(ErasureConfig::new(4, 0).is_err());
    }

    #[test]
    fn wrong_shard_count() {
        let ec = ErasureConfig::default_4_2();
        let info = ShardInfo {
            original_len: 10,
            shard_size: 3,
            data_shards: 4,
            parity_shards: 2,
        };
        let mut shards: Vec<Option<Vec<u8>>> = vec![Some(vec![0; 3]); 3]; // Only 3, need 6
        assert!(ec.decode(&mut shards, &info).is_err());
    }
}

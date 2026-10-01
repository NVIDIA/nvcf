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

//! Port of the MockDynamo per-key LRU KV cache (`mock-dynamo/src/kv_cache.rs`).
//! MockDynamo is a binary crate, so its cache cannot be linked directly. Keep
//! the access and commit semantics identical so simulated reuse matches the
//! cluster mock.

use std::collections::{HashMap, VecDeque};

#[derive(Debug, Default)]
pub struct KvCache {
    capacity_tokens: u64,
    used_tokens: u64,
    entries: HashMap<u32, u64>,
    lru: VecDeque<u32>,
}

impl KvCache {
    pub fn new(capacity_tokens: u64) -> Self {
        Self {
            capacity_tokens,
            ..Default::default()
        }
    }

    /// Returns the number of reused input tokens for this request.
    pub fn access(&mut self, key: u32, input_tokens: u64) -> u64 {
        if self.capacity_tokens == 0 {
            return 0;
        }
        let Some(cached_tokens) = self.entries.get(&key).copied() else {
            return 0;
        };
        self.touch(key);
        cached_tokens.min(input_tokens)
    }

    pub fn commit(&mut self, key: u32, input_tokens: u64) {
        if self.capacity_tokens == 0 {
            return;
        }
        let cached_tokens = self.entries.remove(&key);
        self.lru.retain(|entry| *entry != key);
        self.used_tokens = self.used_tokens.saturating_sub(cached_tokens.unwrap_or(0));
        let retained_tokens = cached_tokens.unwrap_or(0).max(input_tokens);
        if retained_tokens > self.capacity_tokens {
            return;
        }
        while self.used_tokens.saturating_add(retained_tokens) > self.capacity_tokens {
            let Some(evicted_key) = self.lru.pop_front() else {
                break;
            };
            let evicted_tokens = self
                .entries
                .remove(&evicted_key)
                .expect("every LRU key must own a cache entry");
            self.used_tokens = self.used_tokens.saturating_sub(evicted_tokens);
        }
        self.entries.insert(key, retained_tokens);
        self.lru.push_back(key);
        self.used_tokens = self.used_tokens.saturating_add(retained_tokens);
    }

    fn touch(&mut self, key: u32) {
        self.lru.retain(|entry| *entry != key);
        self.lru.push_back(key);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn evicts_least_recently_used_entry() {
        let mut cache = KvCache::new(100);
        cache.commit(1, 60);
        cache.commit(2, 40);
        assert_eq!(cache.access(1, 60), 60);
        cache.commit(3, 40);
        assert_eq!(cache.access(2, 40), 0);
        assert_eq!(cache.access(1, 60), 60);
        assert_eq!(cache.access(3, 40), 40);
    }

    #[test]
    fn oversized_prompt_is_not_retained() {
        let mut cache = KvCache::new(100);
        cache.commit(1, 150);
        assert_eq!(cache.access(1, 150), 0);
    }

    #[test]
    fn reuse_is_capped_by_request_size() {
        let mut cache = KvCache::new(100);
        cache.commit(1, 80);
        assert_eq!(cache.access(1, 50), 50);
    }
}

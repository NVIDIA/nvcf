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

use axum::http::{HeaderMap, HeaderName, HeaderValue};
use serde::Serialize;
use std::collections::{HashMap, VecDeque};

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub(crate) struct KvCacheStats {
    pub(crate) model: String,
    pub(crate) kv_cache_capacity_tokens: u64,
    pub(crate) kv_cache_used_tokens: u64,
    pub(crate) kv_cache_free_tokens: u64,
    pub(crate) kv_cache_entries: usize,
    pub(crate) kv_cache_hit_count: u64,
    pub(crate) kv_cache_miss_count: u64,
    pub(crate) kv_cache_eviction_count: u64,
    pub(crate) kv_cache_evicted_tokens: u64,
}

#[derive(Debug, Clone)]
struct KvCacheEntry {
    tokens: u64,
}

#[derive(Debug, Default)]
pub(crate) struct KvCacheState {
    capacity_tokens: u64,
    used_tokens: u64,
    hit_count: u64,
    miss_count: u64,
    eviction_count: u64,
    evicted_tokens: u64,
    entries: HashMap<String, KvCacheEntry>,
    lru: VecDeque<String>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct KvCacheAccess {
    pub(crate) hit: bool,
    pub(crate) reused_input_tokens: u64,
    pub(crate) uncached_input_tokens: u64,
    pub(crate) evicted_entries: u64,
    pub(crate) evicted_tokens: u64,
}

#[derive(Debug, Default, Clone, Copy, PartialEq, Eq)]
pub(crate) struct KvCacheCommit {
    pub(crate) evicted_entries: u64,
    pub(crate) evicted_tokens: u64,
}

impl KvCacheAccess {
    pub(crate) fn with_commit(mut self, commit: KvCacheCommit) -> Self {
        self.evicted_entries = commit.evicted_entries;
        self.evicted_tokens = commit.evicted_tokens;
        self
    }
}

impl KvCacheState {
    pub(crate) fn new(capacity_tokens: u64) -> Self {
        Self {
            capacity_tokens,
            used_tokens: 0,
            hit_count: 0,
            miss_count: 0,
            eviction_count: 0,
            evicted_tokens: 0,
            entries: HashMap::new(),
            lru: VecDeque::new(),
        }
    }

    pub(crate) fn access(
        &mut self,
        cache_affinity_key: Option<&str>,
        input_tokens: usize,
    ) -> KvCacheAccess {
        // Mock cache counters saturate like telemetry so pathological tests cannot wrap them.
        let tokens = input_tokens as u64;
        let Some(cache_affinity_key) = cache_affinity_key else {
            self.miss_count = self.miss_count.saturating_add(1);
            return KvCacheAccess {
                hit: false,
                reused_input_tokens: 0,
                uncached_input_tokens: tokens,
                evicted_entries: 0,
                evicted_tokens: 0,
            };
        };
        if self.capacity_tokens == 0 {
            self.miss_count = self.miss_count.saturating_add(1);
            return KvCacheAccess {
                hit: false,
                reused_input_tokens: 0,
                uncached_input_tokens: tokens,
                evicted_entries: 0,
                evicted_tokens: 0,
            };
        }

        let cached_tokens = self
            .entries
            .get(cache_affinity_key)
            .map(|entry| entry.tokens);
        if cached_tokens.is_some() {
            self.lru.retain(|key| key != cache_affinity_key);
            self.lru.push_back(cache_affinity_key.to_string());
        }
        let reused_input_tokens = cached_tokens.unwrap_or(0).min(tokens);
        let uncached_input_tokens = tokens.saturating_sub(reused_input_tokens);
        let hit = reused_input_tokens > 0;
        if hit {
            self.hit_count = self.hit_count.saturating_add(1);
        } else {
            self.miss_count = self.miss_count.saturating_add(1);
        }

        KvCacheAccess {
            hit,
            reused_input_tokens,
            uncached_input_tokens,
            evicted_entries: 0,
            evicted_tokens: 0,
        }
    }

    pub(crate) fn commit(
        &mut self,
        cache_affinity_key: Option<&str>,
        input_tokens: usize,
    ) -> KvCacheCommit {
        let Some(cache_affinity_key) = cache_affinity_key else {
            return KvCacheCommit::default();
        };
        if self.capacity_tokens == 0 {
            return KvCacheCommit::default();
        }

        let tokens = input_tokens as u64;
        let cached_tokens = self.entries.remove(cache_affinity_key).map(|entry| {
            self.lru.retain(|key| key != cache_affinity_key);
            self.used_tokens = self.used_tokens.saturating_sub(entry.tokens);
            entry.tokens
        });
        let retained_tokens = cached_tokens.unwrap_or(0).max(tokens);
        let mut evicted_entries = 0u64;
        let mut evicted_tokens = 0u64;
        if retained_tokens <= self.capacity_tokens {
            // used_tokens is maintained as <= capacity, but saturating arithmetic avoids wrapping on bad input.
            while self.used_tokens.saturating_add(retained_tokens) > self.capacity_tokens {
                let Some(evicted_key) = self.lru.pop_front() else {
                    break;
                };
                if let Some(evicted) = self.entries.remove(&evicted_key) {
                    self.used_tokens = self.used_tokens.saturating_sub(evicted.tokens);
                    evicted_entries = evicted_entries.saturating_add(1);
                    evicted_tokens = evicted_tokens.saturating_add(evicted.tokens);
                }
            }
            self.entries.insert(
                cache_affinity_key.to_string(),
                KvCacheEntry {
                    tokens: retained_tokens,
                },
            );
            self.lru.push_back(cache_affinity_key.to_string());
            self.used_tokens = self.used_tokens.saturating_add(retained_tokens);
        } else if let Some(cached_tokens) = cached_tokens {
            // The reused prefix served this request, but the expanded prompt no longer fits for reuse.
            evicted_entries = evicted_entries.saturating_add(1);
            evicted_tokens = evicted_tokens.saturating_add(cached_tokens);
        }
        self.eviction_count = self.eviction_count.saturating_add(evicted_entries);
        self.evicted_tokens = self.evicted_tokens.saturating_add(evicted_tokens);

        KvCacheCommit {
            evicted_entries,
            evicted_tokens,
        }
    }

    pub(crate) fn stats(&self, model: &str) -> KvCacheStats {
        KvCacheStats {
            model: model.to_string(),
            kv_cache_capacity_tokens: self.capacity_tokens,
            kv_cache_used_tokens: self.used_tokens,
            // Keep exported mock stats nonnegative even if a test mutates internal counters.
            kv_cache_free_tokens: self.capacity_tokens.saturating_sub(self.used_tokens),
            kv_cache_entries: self.entries.len(),
            kv_cache_hit_count: self.hit_count,
            kv_cache_miss_count: self.miss_count,
            kv_cache_eviction_count: self.eviction_count,
            kv_cache_evicted_tokens: self.evicted_tokens,
        }
    }
}

pub(crate) fn insert_kv_cache_headers(headers: &mut HeaderMap, access: KvCacheAccess) {
    headers.insert(
        HeaderName::from_static("x-kv-cache-hit"),
        HeaderValue::from_static(if access.hit { "true" } else { "false" }),
    );
    headers.insert(
        HeaderName::from_static("x-kv-cache-evicted-entries"),
        HeaderValue::from_str(&access.evicted_entries.to_string())
            .expect("evicted entry count should be a valid header value"),
    );
    headers.insert(
        HeaderName::from_static("x-kv-cache-evicted-tokens"),
        HeaderValue::from_str(&access.evicted_tokens.to_string())
            .expect("evicted token count should be a valid header value"),
    );
    headers.insert(
        HeaderName::from_static("x-kv-cache-reused-input-tokens"),
        HeaderValue::from_str(&access.reused_input_tokens.to_string())
            .expect("reused input token count should be a valid header value"),
    );
    headers.insert(
        HeaderName::from_static("x-kv-cache-uncached-input-tokens"),
        HeaderValue::from_str(&access.uncached_input_tokens.to_string())
            .expect("uncached input token count should be a valid header value"),
    );
}

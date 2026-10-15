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

use std::collections::{HashMap, VecDeque};

use crate::{EngineConfig, EngineEvent, Micros, RequestId, RequestSpec, WorkerStats};

/// KV memory accounting:
/// - A cached prefix occupies its token count in the prefix cache. While a
///   running sequence uses it, the entry is pinned and cannot be evicted.
/// - A running sequence additionally reserves its uncached input tokens and
///   all output tokens at admission, so it never needs preemption.
/// - When prefill finishes, the uncached input moves into the key's cache
///   entry and the sequence keeps only its output reservation.
/// - When a keyed sequence completes, its output also moves into the entry,
///   which then covers input plus output. A session's next turn, whose prompt
///   extends that conversation, reuses all of it. Matching is per key, not
///   per token block.
/// - Concurrent sequences that prefill the same new key each release their
///   input reservation, but the entry grows only once, as block-level prefix
///   deduplication would store it.
pub(crate) struct Worker {
    waiting: VecDeque<RequestId>,
    running: Vec<RequestId>,
    sequences: HashMap<RequestId, Sequence>,
    cache: PrefixCache,
    reserved_tokens: u64,
    step: Option<Step>,
}

struct Sequence {
    spec: RequestSpec,
    phase: Phase,
    reused_input_tokens: u64,
    /// Tokens reserved outside the prefix cache.
    reserved_tokens: u64,
}

#[derive(Clone, Copy)]
enum Phase {
    Waiting,
    Prefilling { remaining: u64 },
    Decoding { generated: u64 },
}

struct Step {
    end: Micros,
    /// Sequences in this step and the prefill tokens each processes; zero
    /// means one decode token.
    work: Vec<(RequestId, u64)>,
}

impl Worker {
    pub(crate) fn new(kv_cache_capacity_tokens: u64) -> Self {
        Self {
            waiting: VecDeque::new(),
            running: Vec::new(),
            sequences: HashMap::new(),
            cache: PrefixCache::new(kv_cache_capacity_tokens),
            reserved_tokens: 0,
            step: None,
        }
    }

    pub(crate) fn step_end(&self) -> Option<Micros> {
        self.step.as_ref().map(|step| step.end)
    }

    pub(crate) fn cached_tokens(&self, key: u64) -> Option<u64> {
        self.cache.tokens(key)
    }

    /// Ordering key for least-loaded routing.
    pub(crate) fn load(&self) -> (usize, u64) {
        let pending_prefill = self
            .sequences
            .values()
            .map(|sequence| match sequence.phase {
                Phase::Waiting => sequence.spec.input_tokens,
                Phase::Prefilling { remaining } => remaining,
                Phase::Decoding { .. } => 0,
            })
            .sum();
        (self.sequences.len(), pending_prefill)
    }

    pub(crate) fn stats(&self) -> WorkerStats {
        WorkerStats {
            waiting: self.waiting.len(),
            running: self.running.len(),
            kv_cache_used_tokens: self.cache.used_tokens + self.reserved_tokens,
            kv_cache_capacity_tokens: self.cache.capacity_tokens,
        }
    }

    pub(crate) fn enqueue(&mut self, id: RequestId, spec: RequestSpec) {
        self.sequences.insert(
            id,
            Sequence {
                spec,
                phase: Phase::Waiting,
                reused_input_tokens: 0,
                reserved_tokens: 0,
            },
        );
        self.waiting.push_back(id);
    }

    pub(crate) fn remove(&mut self, id: RequestId) {
        let Some(sequence) = self.sequences.remove(&id) else {
            return;
        };
        if matches!(sequence.phase, Phase::Waiting) {
            self.waiting.retain(|waiting| *waiting != id);
        } else {
            self.running.retain(|running| *running != id);
            self.release(&sequence);
        }
    }

    /// Admits waiting requests and schedules the next step at `now`. Leaves
    /// the worker idle when there is no work.
    pub(crate) fn start_step(&mut self, now: Micros, config: &EngineConfig) {
        self.step = None;
        self.admit(config);

        let mut work = Vec::with_capacity(self.running.len());
        let mut decode_seqs = 0u64;
        for id in &self.running {
            if matches!(self.sequences[id].phase, Phase::Decoding { .. }) {
                work.push((*id, 0));
                decode_seqs += 1;
            }
        }
        let mut prefill_budget = config.max_batched_tokens.saturating_sub(decode_seqs);
        let mut prefill_tokens = 0u64;
        for id in &self.running {
            if prefill_budget == 0 {
                break;
            }
            if let Phase::Prefilling { remaining } = self.sequences[id].phase {
                let chunk = remaining.min(prefill_budget);
                work.push((*id, chunk));
                prefill_budget -= chunk;
                prefill_tokens += chunk;
            }
        }
        if work.is_empty() {
            return;
        }
        let step_ms = config.step_fixed_ms
            + config.step_decode_ms_per_seq * decode_seqs as f64
            + config.step_prefill_ms_per_token * prefill_tokens as f64;
        let duration = ((step_ms * 1000.0).round() as Micros).max(1);
        self.step = Some(Step {
            end: now + duration,
            work,
        });
    }

    /// Applies the current step's progress at its end time. Returns the
    /// requests that completed.
    pub(crate) fn finish_step(
        &mut self,
        at: Micros,
        emit_token_events: bool,
        events: &mut Vec<EngineEvent>,
    ) -> Vec<RequestId> {
        let step = self.step.take().expect("finished step must be scheduled");
        let mut completed = Vec::new();
        for (id, prefill_chunk) in step.work {
            let Some(sequence) = self.sequences.get_mut(&id) else {
                // Cancelled during the step.
                continue;
            };
            let generated = match sequence.phase {
                Phase::Prefilling { remaining } => {
                    let remaining = remaining - prefill_chunk;
                    if remaining > 0 {
                        sequence.phase = Phase::Prefilling { remaining };
                        continue;
                    }
                    let spec = sequence.spec;
                    let reused = sequence.reused_input_tokens;
                    let uncached = spec.input_tokens - reused;
                    sequence.phase = Phase::Decoding { generated: 1 };
                    if let Some(key) = spec.cache_key {
                        // The prompt's KV becomes the key's cache entry.
                        sequence.reserved_tokens -= uncached;
                        self.reserved_tokens -= uncached;
                        self.cache.extend_pinned(key, spec.input_tokens);
                    }
                    events.push(EngineEvent::FirstToken {
                        id,
                        at,
                        reused_input_tokens: reused,
                    });
                    1
                }
                Phase::Decoding { generated } => {
                    let generated = generated + 1;
                    sequence.phase = Phase::Decoding { generated };
                    if emit_token_events {
                        events.push(EngineEvent::Token { id, at });
                    }
                    generated
                }
                // The step's request was cancelled and its ID resubmitted.
                Phase::Waiting => continue,
            };
            if generated >= sequence.spec.output_tokens {
                completed.push(id);
            }
        }
        for id in &completed {
            events.push(EngineEvent::Completed { id: *id, at });
            let spec = self.sequences[id].spec;
            if let Some(key) = spec.cache_key {
                // The output reservation becomes cache space when the
                // sequence is removed below.
                self.cache
                    .extend_pinned(key, spec.input_tokens + spec.output_tokens);
            }
            self.remove(*id);
        }
        completed
    }

    fn admit(&mut self, config: &EngineConfig) {
        while self.running.len() < config.max_num_seqs {
            let Some(&id) = self.waiting.front() else {
                return;
            };
            let spec = self.sequences[&id].spec;
            let reused = spec
                .cache_key
                .and_then(|key| self.cache.tokens(key))
                .unwrap_or(0)
                .min(spec.input_tokens);
            let reserve = spec.input_tokens - reused + spec.output_tokens;
            if let Some(key) = spec.cache_key.filter(|_| reused > 0) {
                self.cache.pin(key);
            }
            // An oversized request still runs alone rather than blocking forever.
            if !self.cache.can_make_room(reserve, self.reserved_tokens) && !self.running.is_empty()
            {
                if let Some(key) = spec.cache_key.filter(|_| reused > 0) {
                    self.cache.unpin(key);
                }
                // Head-of-line: later requests wait for memory too.
                return;
            }
            self.cache.make_room(reserve, self.reserved_tokens);
            self.waiting.pop_front();
            self.running.push(id);
            self.reserved_tokens += reserve;
            let sequence = self
                .sequences
                .get_mut(&id)
                .expect("waiting request has a sequence");
            sequence.reused_input_tokens = reused;
            sequence.reserved_tokens = reserve;
            // A fully cached prompt still computes its last position.
            sequence.phase = Phase::Prefilling {
                remaining: (spec.input_tokens - reused).max(1),
            };
            // A new key's entry must survive until this prefill fills it.
            if reused == 0
                && let Some(key) = spec.cache_key
            {
                self.cache.pin(key);
            }
        }
    }

    fn release(&mut self, sequence: &Sequence) {
        self.reserved_tokens -= sequence.reserved_tokens;
        if let Some(key) = sequence.spec.cache_key {
            self.cache.unpin(key);
        }
    }
}

/// Per-key LRU prefix cache with pinning for entries in use.
struct PrefixCache {
    capacity_tokens: u64,
    used_tokens: u64,
    entries: HashMap<u64, CacheEntry>,
    /// Monotonic recency stamp; higher is more recent.
    clock: u64,
}

struct CacheEntry {
    tokens: u64,
    pins: u32,
    last_used: u64,
}

impl PrefixCache {
    fn new(capacity_tokens: u64) -> Self {
        Self {
            capacity_tokens,
            used_tokens: 0,
            entries: HashMap::new(),
            clock: 0,
        }
    }

    fn tokens(&self, key: u64) -> Option<u64> {
        self.entries
            .get(&key)
            .map(|entry| entry.tokens)
            .filter(|tokens| *tokens > 0)
    }

    fn touch(&mut self, key: u64) -> &mut CacheEntry {
        self.clock += 1;
        let clock = self.clock;
        let entry = self.entries.entry(key).or_insert(CacheEntry {
            tokens: 0,
            pins: 0,
            last_used: clock,
        });
        entry.last_used = clock;
        entry
    }

    fn pin(&mut self, key: u64) {
        self.touch(key).pins += 1;
    }

    fn unpin(&mut self, key: u64) {
        let Some(entry) = self.entries.get_mut(&key) else {
            return;
        };
        entry.pins = entry.pins.saturating_sub(1);
        if entry.pins == 0 && entry.tokens == 0 {
            self.entries.remove(&key);
        }
    }

    /// Grows a pinned entry to `tokens`. The caller already accounted for the
    /// memory as a reservation, so this only moves it into the cache.
    fn extend_pinned(&mut self, key: u64, tokens: u64) {
        let entry = self.touch(key);
        let grown = tokens.saturating_sub(entry.tokens);
        entry.tokens = entry.tokens.max(tokens);
        self.used_tokens += grown;
    }

    /// Whether evicting unpinned entries can fit `needed` more tokens beside
    /// the cache and `reserved` sequence tokens.
    fn can_make_room(&self, needed: u64, reserved: u64) -> bool {
        let pinned_tokens: u64 = self
            .entries
            .values()
            .filter(|entry| entry.pins > 0)
            .map(|entry| entry.tokens)
            .sum();
        pinned_tokens + reserved + needed <= self.capacity_tokens
    }

    /// Evicts least recently used unpinned entries until `needed` more tokens
    /// fit beside the cache and `reserved` sequence tokens, or until none
    /// remain.
    fn make_room(&mut self, needed: u64, reserved: u64) {
        while self.used_tokens + reserved + needed > self.capacity_tokens {
            let Some(victim) = self
                .entries
                .iter()
                .filter(|(_, entry)| entry.pins == 0 && entry.tokens > 0)
                .min_by_key(|(_, entry)| entry.last_used)
                .map(|(key, _)| *key)
            else {
                return;
            };
            let evicted = self.entries.remove(&victim).expect("victim entry exists");
            self.used_tokens -= evicted.tokens;
        }
    }
}

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

//! Time-agnostic model of a batched inference deployment.
//!
//! A deployment has `num_gpu_workers` workers. Each worker runs
//! iteration-level steps like vLLM, SGLang, or TensorRT-LLM: every step
//! decodes one token for each running sequence in its decode phase and spends
//! the remaining token budget on chunked prefill. A step takes
//! `step_fixed_ms + step_decode_ms_per_seq * decoding + step_prefill_ms_per_token * prefill_tokens`.
//! Prefill and decode therefore share each worker's compute.
//!
//! Each worker has its own prefix cache keyed by the request's cache key.
//! Requests are routed to the worker that caches the most tokens for their
//! key regardless of its load, or to the least-loaded worker when none does.
//!
//! The engine has no clock. Callers pass the current time to every method and
//! use [`Engine::next_event_time`] to know when to advance. MockDynamo drives
//! it in real time; the routing simulator drives it in virtual time.

mod worker;

use std::collections::HashMap;

use serde::Deserialize;

use worker::Worker;

/// Engine time in microseconds from an arbitrary origin.
pub type Micros = u64;

/// Caller-assigned request identifier, unique among live requests. An ID may
/// be reused after its request completes or is cancelled.
pub type RequestId = u64;

#[derive(Clone, Debug, Deserialize, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct EngineConfig {
    /// Inference workers in the deployment. Each owns one scheduler and KV cache.
    pub num_gpu_workers: usize,
    /// Maximum running sequences per worker.
    pub max_num_seqs: usize,
    /// Maximum tokens processed per step per worker, decode and prefill combined.
    pub max_batched_tokens: u64,
    pub step_fixed_ms: f64,
    pub step_decode_ms_per_seq: f64,
    pub step_prefill_ms_per_token: f64,
    /// KV capacity per worker, shared by running sequences and the prefix cache.
    pub kv_cache_capacity_tokens: u64,
}

impl EngineConfig {
    /// One H100 80GB per worker serving Llama 3.1 8B in FP8 with TP1.
    ///
    /// The step costs are estimates, not measurements: about 163 output
    /// tokens per second per sequence at 25 running sequences, and about
    /// 20,000 prefill tokens per second per worker.
    pub fn h100_llama_3_1_8b(num_gpu_workers: usize) -> Self {
        Self {
            num_gpu_workers,
            max_num_seqs: 25,
            max_batched_tokens: 2048,
            step_fixed_ms: 4.0,
            step_decode_ms_per_seq: 0.085,
            step_prefill_ms_per_token: 0.05,
            kv_cache_capacity_tokens: 400_000,
        }
    }

    /// Concurrency limit for the whole deployment, for Pylon's
    /// `--max-engine-concurrency`.
    pub fn max_concurrency(&self) -> u64 {
        (self.num_gpu_workers as u64).saturating_mul(self.max_num_seqs as u64)
    }

    pub fn validate(&self) -> Result<(), String> {
        if self.num_gpu_workers == 0 {
            return Err("num_gpu_workers must be at least 1".to_string());
        }
        if self.max_num_seqs == 0 {
            return Err("max_num_seqs must be at least 1".to_string());
        }
        // Each running sequence decodes one token per step.
        if self.max_batched_tokens < self.max_num_seqs as u64 {
            return Err("max_batched_tokens must be at least max_num_seqs".to_string());
        }
        if self.kv_cache_capacity_tokens == 0 {
            return Err("kv_cache_capacity_tokens must be at least 1".to_string());
        }
        for (name, value) in [
            ("step_fixed_ms", self.step_fixed_ms),
            ("step_decode_ms_per_seq", self.step_decode_ms_per_seq),
            ("step_prefill_ms_per_token", self.step_prefill_ms_per_token),
        ] {
            if !value.is_finite() || value < 0.0 {
                return Err(format!("{name} must be finite and non-negative"));
            }
        }
        Ok(())
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RequestSpec {
    /// Prefix-cache key. Requests without a key never reuse cached tokens.
    pub cache_key: Option<u64>,
    pub input_tokens: u64,
    /// Total output tokens including the first. Zero is treated as one,
    /// since prefill always produces a token.
    pub output_tokens: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum EngineEvent {
    /// Prefill finished and the first output token was produced.
    FirstToken {
        id: RequestId,
        at: Micros,
        reused_input_tokens: u64,
    },
    /// One more output token was produced. Emitted only when token events are
    /// enabled.
    Token { id: RequestId, at: Micros },
    /// The last output token was produced and the sequence left the worker.
    Completed { id: RequestId, at: Micros },
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WorkerStats {
    pub waiting: usize,
    pub running: usize,
    pub kv_cache_used_tokens: u64,
    pub kv_cache_capacity_tokens: u64,
}

pub struct Engine {
    config: EngineConfig,
    workers: Vec<Worker>,
    /// Worker owning each live request.
    owners: HashMap<RequestId, usize>,
    now: Micros,
    emit_token_events: bool,
    pending_events: Vec<EngineEvent>,
}

impl Engine {
    pub fn new(config: EngineConfig, emit_token_events: bool) -> Result<Self, String> {
        config.validate()?;
        let workers = (0..config.num_gpu_workers)
            .map(|_| Worker::new(config.kv_cache_capacity_tokens))
            .collect();
        Ok(Self {
            config,
            workers,
            owners: HashMap::new(),
            now: 0,
            emit_token_events,
            pending_events: Vec::new(),
        })
    }

    /// Queues a request at `now` and returns the worker chosen for it.
    pub fn submit(&mut self, now: Micros, id: RequestId, spec: RequestSpec) -> usize {
        self.run_until(now);
        debug_assert!(
            !self.owners.contains_key(&id),
            "request {id} is already live"
        );
        let spec = RequestSpec {
            output_tokens: spec.output_tokens.max(1),
            ..spec
        };
        let worker = self.route(spec.cache_key);
        self.owners.insert(id, worker);
        self.workers[worker].enqueue(id, spec);
        self.start_idle_worker(worker);
        worker
    }

    /// Removes a request, as when the client disconnects. Returns false when
    /// the request already finished or was never submitted.
    pub fn cancel(&mut self, now: Micros, id: RequestId) -> bool {
        self.run_until(now);
        let Some(worker) = self.owners.remove(&id) else {
            return false;
        };
        self.workers[worker].remove(id);
        // Freed memory or slots may let waiting requests start.
        self.start_idle_worker(worker);
        true
    }

    /// Processes every step that ends at or before `now` and returns the
    /// events produced since the previous call, in time order.
    pub fn advance_to(&mut self, now: Micros) -> Vec<EngineEvent> {
        self.run_until(now);
        std::mem::take(&mut self.pending_events)
    }

    /// The next time at which [`Engine::advance_to`] would produce progress.
    pub fn next_event_time(&self) -> Option<Micros> {
        self.workers.iter().filter_map(Worker::step_end).min()
    }

    pub fn worker_stats(&self) -> Vec<WorkerStats> {
        self.workers.iter().map(Worker::stats).collect()
    }

    fn run_until(&mut self, now: Micros) {
        debug_assert!(now >= self.now, "engine time must not go backwards");
        loop {
            let next = self
                .workers
                .iter()
                .enumerate()
                .filter_map(|(index, worker)| Some((worker.step_end()?, index)))
                .min();
            let Some((step_end, worker)) = next.filter(|(step_end, _)| *step_end <= now) else {
                break;
            };
            let finished = self.workers[worker].finish_step(
                step_end,
                self.emit_token_events,
                &mut self.pending_events,
            );
            for id in finished {
                self.owners.remove(&id);
            }
            self.workers[worker].start_step(step_end, &self.config);
        }
        self.now = self.now.max(now);
    }

    fn start_idle_worker(&mut self, worker: usize) {
        if self.workers[worker].step_end().is_none() {
            self.workers[worker].start_step(self.now, &self.config);
        }
    }

    /// Perfect KV routing: the worker caching the most tokens for the key,
    /// otherwise the least-loaded worker.
    fn route(&self, cache_key: Option<u64>) -> usize {
        if let Some(key) = cache_key {
            let cached = self
                .workers
                .iter()
                .enumerate()
                .filter_map(|(index, worker)| Some((worker.cached_tokens(key)?, index)))
                .max_by_key(|(tokens, index)| (*tokens, std::cmp::Reverse(*index)));
            if let Some((_, index)) = cached {
                return index;
            }
        }
        self.workers
            .iter()
            .enumerate()
            .min_by_key(|(index, worker)| (worker.load(), *index))
            .map(|(index, _)| index)
            .expect("engine has at least one worker")
    }
}

#[cfg(test)]
mod tests;

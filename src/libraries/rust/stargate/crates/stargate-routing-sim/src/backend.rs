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

//! One Pylon plus one MockDynamo engine.
//!
//! Engine: a fixed number of request slots with a FIFO wait queue. Each
//! admitted request prefills its uncached tokens at the full per-request rate,
//! independent of other requests, then decodes at its own sampled rate. This is
//! the MockDynamo timing model, not a model of real GPU contention.
//!
//! Pylon: tracks live requests by phase and publishes the same `ModelStats`
//! fields that `pylon-lib/src/queue_admission.rs` derives for routing.

use std::collections::{HashMap, VecDeque};

use stargate_proto::pb::ModelStats;
use stargate_protocol::common::{has_available_engine_slot, queue_time_delta_ms};

use crate::config::{EngineConfig, InputTpsModel, PylonConfig, QueueMismatchConfig};
use crate::kv_cache::KvCache;
use crate::time::{Micros, micros_from_ms};

pub struct Backend {
    pub id: String,
    pub region: usize,
    speed: f64,
    free_slots: usize,
    waiting: VecDeque<usize>,
    kv_cache: KvCache,
    input_phase_requests: u64,
    output_phase_requests: u64,
    prompt_work_tokens: u64,
    total_input_tokens: u64,
    input_tps: InputTpsEstimate,
}

/// Pylon's reported input throughput.
struct InputTpsEstimate {
    last_mean: f64,
    max: f64,
    /// (submitted, first output, input tokens), ordered by first output.
    window: VecDeque<(Micros, Micros, u64)>,
}

impl Backend {
    pub fn new(
        id: String,
        region: usize,
        speed: f64,
        engine: &EngineConfig,
        pylon: &PylonConfig,
    ) -> Self {
        let (last_mean, max) = match pylon.input_tps {
            InputTpsModel::Constant { mean, max } => (mean * speed, max * speed),
            InputTpsModel::FallbackWindow { initial, .. } => (initial * speed, initial * speed),
        };
        Self {
            id,
            region,
            speed,
            free_slots: engine.slots,
            waiting: VecDeque::new(),
            kv_cache: KvCache::new(engine.kv_cache_capacity_tokens),
            input_phase_requests: 0,
            output_phase_requests: 0,
            prompt_work_tokens: 0,
            total_input_tokens: 0,
            input_tps: InputTpsEstimate {
                last_mean,
                max,
                window: VecDeque::new(),
            },
        }
    }

    pub fn speed(&self) -> f64 {
        self.speed
    }

    fn live_requests(&self) -> u64 {
        self.input_phase_requests + self.output_phase_requests
    }

    /// Pylon queue-estimate mismatch admission. Returns true when Pylon would
    /// reject with a retryable 429 before the request reaches the engine.
    pub fn rejects(
        &self,
        expected_queue_ms: Option<u64>,
        pylon: &PylonConfig,
        mismatch: &QueueMismatchConfig,
    ) -> bool {
        if !mismatch.enabled {
            return false;
        }
        let Some(expected_ms) = expected_queue_ms else {
            return false;
        };
        let actual_ms =
            if has_available_engine_slot(self.live_requests(), pylon.max_engine_concurrency) {
                0
            } else {
                queue_time_delta_ms(self.prompt_work_tokens, self.input_tps.last_mean)
                    .unwrap_or(u64::MAX)
            };
        let additive = expected_ms.saturating_add(mismatch.min_delta_ms);
        let factor = if mismatch.tolerance_factor.is_finite() && mismatch.tolerance_factor > 0.0 {
            mismatch.tolerance_factor
        } else {
            1.0
        };
        let multiplicative = ((expected_ms as f64) * factor).ceil() as u64;
        actual_ms > additive.max(multiplicative)
    }

    /// Pylon accepted the request and forwarded it to the engine. Returns true
    /// when the engine has a free slot and the request can start now.
    pub fn accept(&mut self, request: usize, input_tokens: u64) -> bool {
        self.input_phase_requests += 1;
        self.prompt_work_tokens += input_tokens;
        self.total_input_tokens += input_tokens;
        if self.free_slots > 0 {
            self.free_slots -= 1;
            true
        } else {
            self.waiting.push_back(request);
            false
        }
    }

    /// Starts prefill and returns the number of reused input tokens.
    pub fn start_prefill(&mut self, session: u32, input_tokens: u64) -> u64 {
        self.kv_cache.access(session, input_tokens)
    }

    pub fn finish_prefill(&mut self, session: u32, input_tokens: u64) {
        self.kv_cache.commit(session, input_tokens);
    }

    /// Pylon observed first output for a request it submitted at `submitted`.
    pub fn first_token(
        &mut self,
        input_tokens: u64,
        submitted: Micros,
        now: Micros,
        pylon: &PylonConfig,
    ) {
        self.input_phase_requests -= 1;
        self.output_phase_requests += 1;
        self.prompt_work_tokens -= input_tokens;
        if let InputTpsModel::FallbackWindow {
            window,
            duration_floor_ms,
            ..
        } = pylon.input_tps
        {
            self.observe_input_interval(submitted, now, input_tokens, window, duration_floor_ms);
        }
    }

    fn observe_input_interval(
        &mut self,
        submitted: Micros,
        first_output: Micros,
        input_tokens: u64,
        window: usize,
        duration_floor_ms: f64,
    ) {
        if window == 0 || first_output <= submitted {
            return;
        }
        let estimate = &mut self.input_tps;
        // Events arrive in time order, so first-output order is append order.
        estimate
            .window
            .push_back((submitted, first_output, input_tokens));
        while estimate.window.len() > window {
            estimate.window.pop_front();
        }
        let tokens: u64 = estimate.window.iter().map(|entry| entry.2).sum();
        let mut intervals: Vec<(Micros, Micros)> = estimate
            .window
            .iter()
            .map(|entry| (entry.0, entry.1))
            .collect();
        intervals.sort_unstable();
        let mut union: Micros = 0;
        let (mut start, mut end) = intervals[0];
        for &(next_start, next_end) in &intervals[1..] {
            if next_start <= end {
                end = end.max(next_end);
            } else {
                union += end - start;
                (start, end) = (next_start, next_end);
            }
        }
        union += end - start;
        let seconds = union.max(micros_from_ms(duration_floor_ms)) as f64 / 1_000_000.0;
        let rate = tokens as f64 / seconds;
        if rate.is_finite() && rate > 0.0 {
            estimate.last_mean = rate;
            estimate.max = estimate.max.max(rate);
        }
    }

    /// Frees the slot and returns the next queued request, which now owns it.
    pub fn complete(&mut self, input_tokens: u64) -> Option<usize> {
        self.output_phase_requests -= 1;
        self.total_input_tokens -= input_tokens;
        self.release_slot()
    }

    /// The client disconnected while the request waited for a slot.
    pub fn cancel_waiting(&mut self, request: usize, input_tokens: u64) {
        let position = self
            .waiting
            .iter()
            .position(|waiting| *waiting == request)
            .expect("cancelled waiting request must be queued");
        self.waiting.remove(position);
        self.forget_input_phase(input_tokens);
    }

    /// The client disconnected during prefill. Returns the next slot owner.
    pub fn cancel_prefilling(&mut self, input_tokens: u64) -> Option<usize> {
        self.forget_input_phase(input_tokens);
        self.release_slot()
    }

    fn forget_input_phase(&mut self, input_tokens: u64) {
        self.input_phase_requests -= 1;
        self.prompt_work_tokens -= input_tokens;
        self.total_input_tokens -= input_tokens;
    }

    fn release_slot(&mut self) -> Option<usize> {
        if let Some(next) = self.waiting.pop_front() {
            Some(next)
        } else {
            self.free_slots += 1;
            None
        }
    }

    pub fn prefill_tokens_per_s(&self, engine: &EngineConfig) -> f64 {
        engine.prefill_tokens_per_s * self.speed
    }

    pub fn stats(&self, pylon: &PylonConfig) -> ModelStats {
        let mean_input_tps = self.input_tps.last_mean;
        let mut queue_time_estimate_ms_by_priority = HashMap::new();
        if self.prompt_work_tokens > 0 {
            queue_time_estimate_ms_by_priority.insert(
                0,
                queue_time_delta_ms(self.prompt_work_tokens, mean_input_tps).unwrap_or(u64::MAX),
            );
        }
        ModelStats {
            last_mean_input_tps: mean_input_tps,
            max_input_tps: Some(self.input_tps.max),
            queue_size: self.input_phase_requests,
            queued_input_size: self.prompt_work_tokens,
            num_running_queries: self.live_requests(),
            max_engine_concurrency: pylon.max_engine_concurrency,
            total_query_input_size: self.total_input_tokens,
            input_processing_queries: self.input_phase_requests,
            output_generation_queries: self.output_phase_requests,
            queue_time_estimate_ms_by_priority,
            ..Default::default()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn engine() -> EngineConfig {
        EngineConfig {
            slots: 2,
            prefill_tokens_per_s: 1000.0,
            ttft_base_ms: 0.0,
            ttft_jitter_ms: 0.0,
            decode_tokens_per_s_min: 100.0,
            decode_tokens_per_s_max: 100.0,
            kv_cache_capacity_tokens: 10_000,
        }
    }

    fn pylon() -> PylonConfig {
        PylonConfig {
            heartbeat_ms: 1000.0,
            max_engine_concurrency: 2,
            input_tps: InputTpsModel::Constant {
                mean: 1000.0,
                max: 1000.0,
            },
            queue_mismatch: QueueMismatchConfig {
                enabled: true,
                min_delta_ms: 25,
                tolerance_factor: 1.25,
            },
        }
    }

    #[test]
    fn slots_queue_fifo_and_hand_off_on_completion() {
        let pylon = pylon();
        let mut backend = Backend::new("b".into(), 0, 1.0, &engine(), &pylon);
        assert!(backend.accept(0, 100));
        assert!(backend.accept(1, 100));
        assert!(!backend.accept(2, 100));
        backend.first_token(100, 0, 1, &pylon);
        assert_eq!(backend.complete(100), Some(2));
        let stats = backend.stats(&pylon);
        assert_eq!(stats.num_running_queries, 2);
        assert_eq!(stats.queued_input_size, 200);
        assert_eq!(stats.queue_time_estimate_ms_by_priority.get(&0), Some(&200));
    }

    #[test]
    fn mismatch_rejects_only_when_full_and_far_above_expected() {
        let pylon = pylon();
        let mut backend = Backend::new("b".into(), 0, 1.0, &engine(), &pylon);
        backend.accept(0, 1000);
        assert!(!backend.rejects(Some(0), &pylon, &pylon.queue_mismatch));
        backend.accept(1, 1000);
        // Full engine with 2,000 prompt tokens at 1,000 tokens/s is 2,000 ms.
        assert!(backend.rejects(Some(0), &pylon, &pylon.queue_mismatch));
        assert!(!backend.rejects(Some(1700), &pylon, &pylon.queue_mismatch));
        assert!(!backend.rejects(None, &pylon, &pylon.queue_mismatch));
    }

    #[test]
    fn fallback_window_rate_uses_interval_union_and_keeps_peak() {
        let mut pylon = pylon();
        pylon.input_tps = InputTpsModel::FallbackWindow {
            initial: 500.0,
            window: 2,
            duration_floor_ms: 10.0,
        };
        let mut backend = Backend::new("b".into(), 0, 1.0, &engine(), &pylon);
        for request in 0..3 {
            backend.accept(request, 1000);
        }
        // Two overlapping one-second intervals: 2,000 tokens over 1.5 s.
        backend.first_token(1000, 0, 1_000_000, &pylon);
        backend.first_token(1000, 500_000, 1_500_000, &pylon);
        let stats = backend.stats(&pylon);
        assert!((stats.last_mean_input_tps - 2000.0 / 1.5).abs() < 1e-6);
        // A cache hit with a 20 ms interval replaces the oldest entry.
        backend.first_token(1000, 1_980_000, 2_000_000, &pylon);
        let stats = backend.stats(&pylon);
        assert!((stats.last_mean_input_tps - 2000.0 / 1.02).abs() < 1e-6);
        assert_eq!(stats.max_input_tps, Some(stats.last_mean_input_tps));
    }
}

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

//! One Pylon in front of one MockDynamo deployment.
//!
//! Engine: the shared `mock-engine` model that MockDynamo runs in real time.
//! It owns scheduling, batching, KV caching, and worker routing.
//!
//! Pylon: tracks live requests by phase and publishes the same `ModelStats`
//! fields that `pylon-lib/src/queue_admission.rs` derives for routing.

use std::collections::{HashMap, VecDeque};

use mock_engine::{Engine, EngineConfig, EngineEvent, RequestSpec};
use stargate_proto::pb::ModelStats;
use stargate_protocol::common::{has_available_engine_slot, queue_time_delta_ms};

use crate::config::{InputTpsModel, PylonConfig, QueueMismatchConfig};
use crate::time::{Micros, micros_from_ms};

pub struct Backend {
    pub id: String,
    pub region: usize,
    engine: Engine,
    /// Pylon's limit: the deployment's workers times sequences per worker.
    max_engine_concurrency: u64,
    /// Time of the scheduled engine wake event, if any.
    pub wake_at: Option<Micros>,
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
        engine: EngineConfig,
        pylon: &PylonConfig,
    ) -> anyhow::Result<Self> {
        let capacity = speed * engine.num_gpu_workers as f64;
        let (last_mean, max) = match pylon.input_tps {
            InputTpsModel::Constant { mean, max } => (mean * capacity, max * capacity),
            InputTpsModel::FallbackWindow { initial, .. } => {
                (initial * capacity, initial * capacity)
            }
        };
        let max_engine_concurrency = engine.max_concurrency();
        Ok(Self {
            id,
            region,
            engine: Engine::new(engine, false).map_err(anyhow::Error::msg)?,
            max_engine_concurrency,
            wake_at: None,
            input_phase_requests: 0,
            output_phase_requests: 0,
            prompt_work_tokens: 0,
            total_input_tokens: 0,
            input_tps: InputTpsEstimate {
                last_mean,
                max,
                window: VecDeque::new(),
            },
        })
    }

    fn live_requests(&self) -> u64 {
        self.input_phase_requests + self.output_phase_requests
    }

    /// Pylon queue-estimate mismatch admission. Returns true when Pylon would
    /// reject with a retryable 429 before the request reaches the engine.
    pub fn rejects(&self, expected_queue_ms: Option<u64>, mismatch: &QueueMismatchConfig) -> bool {
        if !mismatch.enabled {
            return false;
        }
        let Some(expected_ms) = expected_queue_ms else {
            return false;
        };
        let actual_ms =
            if has_available_engine_slot(self.live_requests(), self.max_engine_concurrency) {
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

    /// Pylon accepted the request and submitted it to the engine.
    pub fn accept(&mut self, now: Micros, request: usize, spec: RequestSpec) {
        self.input_phase_requests += 1;
        self.prompt_work_tokens += spec.input_tokens;
        self.total_input_tokens += spec.input_tokens;
        self.engine.submit(now, request as u64, spec);
    }

    pub fn advance_engine(&mut self, now: Micros) -> Vec<EngineEvent> {
        self.engine.advance_to(now)
    }

    pub fn next_engine_event(&self) -> Option<Micros> {
        self.engine.next_event_time()
    }

    /// Pylon observed first output for a request it submitted at `submitted`.
    pub fn first_token(
        &mut self,
        input_tokens: u64,
        submitted: Micros,
        first_output: Micros,
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
            self.observe_input_interval(
                submitted,
                first_output,
                input_tokens,
                window,
                duration_floor_ms,
            );
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

    /// Pylon saw the request finish.
    pub fn complete(&mut self, input_tokens: u64) {
        self.output_phase_requests -= 1;
        self.total_input_tokens -= input_tokens;
    }

    /// The client disconnected; the engine drops the request.
    pub fn cancel(&mut self, now: Micros, request: usize, input_tokens: u64, decoding: bool) {
        if decoding {
            self.output_phase_requests -= 1;
        } else {
            self.input_phase_requests -= 1;
            self.prompt_work_tokens -= input_tokens;
        }
        self.total_input_tokens -= input_tokens;
        self.engine.cancel(now, request as u64);
    }

    pub fn stats(&self) -> ModelStats {
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
            max_engine_concurrency: self.max_engine_concurrency,
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
            num_gpu_workers: 1,
            max_num_seqs: 2,
            max_batched_tokens: 10_000,
            step_fixed_ms: 1.0,
            step_decode_ms_per_seq: 0.0,
            step_prefill_ms_per_token: 1.0,
            kv_cache_capacity_tokens: 100_000,
        }
    }

    fn pylon() -> PylonConfig {
        PylonConfig {
            heartbeat_ms: 1000.0,
            stats_update_coalesce_ms: None,
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

    fn spec(input_tokens: u64) -> RequestSpec {
        RequestSpec {
            cache_key: None,
            input_tokens,
            output_tokens: 4,
        }
    }

    #[test]
    fn pylon_stats_track_phases_and_engine_concurrency() {
        let pylon = pylon();
        let mut backend = Backend::new("b".into(), 0, 1.0, engine(), &pylon).unwrap();
        backend.accept(0, 0, spec(100));
        backend.accept(0, 1, spec(100));
        backend.accept(0, 2, spec(100));
        let stats = backend.stats();
        assert_eq!(stats.max_engine_concurrency, 2);
        assert_eq!(stats.num_running_queries, 3);
        assert_eq!(stats.queued_input_size, 300);
        assert_eq!(stats.queue_time_estimate_ms_by_priority.get(&0), Some(&300));

        backend.first_token(100, 0, 10_000, &pylon);
        backend.cancel(20_000, 2, 100, false);
        let stats = backend.stats();
        assert_eq!(stats.num_running_queries, 2);
        assert_eq!(stats.queued_input_size, 100);
    }

    #[test]
    fn mismatch_rejects_only_when_full_and_far_above_expected() {
        let pylon = pylon();
        let mut backend = Backend::new("b".into(), 0, 1.0, engine(), &pylon).unwrap();
        backend.accept(0, 0, spec(1000));
        assert!(!backend.rejects(Some(0), &pylon.queue_mismatch));
        backend.accept(0, 1, spec(1000));
        // Full engine with 2,000 prompt tokens at 1,000 tokens/s is 2,000 ms.
        assert!(backend.rejects(Some(0), &pylon.queue_mismatch));
        assert!(!backend.rejects(Some(1700), &pylon.queue_mismatch));
        assert!(!backend.rejects(None, &pylon.queue_mismatch));
    }

    #[test]
    fn fallback_window_rate_uses_interval_union_and_keeps_peak() {
        let mut pylon = pylon();
        pylon.input_tps = InputTpsModel::FallbackWindow {
            initial: 500.0,
            window: 2,
            duration_floor_ms: 10.0,
        };
        let mut backend = Backend::new("b".into(), 0, 1.0, engine(), &pylon).unwrap();
        for request in 0..3 {
            backend.accept(0, request, spec(1000));
        }
        // Two overlapping one-second intervals: 2,000 tokens over 1.5 s.
        backend.first_token(1000, 0, 1_000_000, &pylon);
        backend.first_token(1000, 500_000, 1_500_000, &pylon);
        let stats = backend.stats();
        assert!((stats.last_mean_input_tps - 2000.0 / 1.5).abs() < 1e-6);
        // A cache hit with a 20 ms interval replaces the oldest entry.
        backend.first_token(1000, 1_980_000, 2_000_000, &pylon);
        let stats = backend.stats();
        assert!((stats.last_mean_input_tps - 2000.0 / 1.02).abs() < 1e-6);
        assert_eq!(stats.max_input_tps, Some(stats.last_mean_input_tps));
    }
}

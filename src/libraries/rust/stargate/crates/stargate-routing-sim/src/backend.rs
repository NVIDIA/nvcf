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
//! fields that `pylon-lib/src/queue_admission.rs` derives for routing. The
//! fallback input throughput runs Pylon's own window from `pylon-lib`.

use std::collections::HashMap;
use std::time::{Duration, Instant};

use mock_engine::{Engine, EngineConfig, EngineEvent, RequestSpec};
use pylon_lib::{
    InputIntervalSample, RequestInputInterval, RequestInputIntervalWindow, StatsCollectorConfig,
};
use stargate_proto::pb::ModelStats;
use stargate_protocol::common::{has_available_engine_slot, queue_time_delta_ms};

use crate::config::{InputTpsModel, PylonConfig, QueueMismatchConfig};
use crate::time::Micros;

pub struct Backend {
    pub id: String,
    pub region: usize,
    pub gpu_workers: usize,
    engine: Engine,
    /// Pylon's limit: the deployment's workers times sequences per worker.
    pub max_engine_concurrency: u64,
    /// Time of the scheduled engine wake event, if any.
    pub wake_at: Option<Micros>,
    input_phase_requests: u64,
    output_phase_requests: u64,
    prompt_work_tokens: u64,
    total_input_tokens: u64,
    input_tps: InputTpsEstimate,
}

/// Engine submit and first output times of a request, as Pylon saw them.
#[derive(Clone, Copy, Debug)]
pub struct InputInterval {
    pub submitted: Micros,
    pub first_output: Micros,
}

/// Pylon's reported input throughput.
struct InputTpsEstimate {
    last_mean: f64,
    max: f64,
    fallback: Option<FallbackWindow>,
}

/// Pylon's OpenAI fallback window, driven in virtual time.
struct FallbackWindow {
    window: RequestInputIntervalWindow,
    config: StatsCollectorConfig,
    /// Virtual time zero as a real instant. The window only compares and
    /// subtracts instants, so this mapping reproduces virtual durations.
    epoch: Instant,
}

/// What a request's usage told Pylon when its window entry was observed.
#[derive(Clone, Copy, Debug)]
enum InputUsage {
    /// First output arrived and usage is still expected.
    Pending,
    /// Final usage, with the engine's cached prompt token count.
    Reported { cached_tokens: u64 },
    /// The request ended without usage.
    Missing,
}

impl Backend {
    /// `epoch` is the real instant that stands for virtual time zero.
    pub fn new(
        id: String,
        region: usize,
        speed: f64,
        engine: EngineConfig,
        pylon: &PylonConfig,
        epoch: Instant,
    ) -> anyhow::Result<Self> {
        let capacity = speed * engine.num_gpu_workers as f64;
        let input_tps = match pylon.input_tps {
            InputTpsModel::Constant { mean, max } => InputTpsEstimate {
                last_mean: mean * capacity,
                max: max * capacity,
                fallback: None,
            },
            InputTpsModel::FallbackWindow {
                initial,
                window,
                duration_floor_ms,
            } => InputTpsEstimate {
                last_mean: initial * capacity,
                max: initial * capacity,
                fallback: Some(FallbackWindow {
                    window: RequestInputIntervalWindow::default(),
                    config: StatsCollectorConfig {
                        smoothing_window_size: window,
                        duration_floor: Duration::from_secs_f64(duration_floor_ms / 1000.0),
                        ..StatsCollectorConfig::default()
                    },
                    epoch,
                }),
            },
        };
        let max_engine_concurrency = engine.max_concurrency();
        Ok(Self {
            id,
            region,
            gpu_workers: engine.num_gpu_workers,
            engine: Engine::new(engine, false).map_err(anyhow::Error::msg)?,
            max_engine_concurrency,
            wake_at: None,
            input_phase_requests: 0,
            output_phase_requests: 0,
            prompt_work_tokens: 0,
            total_input_tokens: 0,
            input_tps,
        })
    }

    fn live_requests(&self) -> u64 {
        self.input_phase_requests + self.output_phase_requests
    }

    /// Whether Pylon tracks no live requests or prompt work and the engine
    /// holds no sequences.
    pub fn is_idle(&self) -> bool {
        self.live_requests() == 0
            && self.prompt_work_tokens == 0
            && self.total_input_tokens == 0
            && self
                .engine
                .worker_stats()
                .iter()
                .all(|worker| worker.waiting == 0 && worker.running == 0)
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

    /// Pylon observed first output. The request's usage arrives at the end
    /// of its stream.
    pub fn first_token(&mut self, request: usize, input_tokens: u64, interval: InputInterval) {
        self.input_phase_requests -= 1;
        self.output_phase_requests += 1;
        self.prompt_work_tokens -= input_tokens;
        self.observe_input_interval(request, input_tokens, interval, InputUsage::Pending);
    }

    /// Pylon saw the request finish. Its final usage reports the prompt
    /// tokens the engine served from its prefix cache.
    pub fn complete(
        &mut self,
        request: usize,
        input_tokens: u64,
        cached_tokens: u64,
        interval: InputInterval,
    ) {
        self.output_phase_requests -= 1;
        self.total_input_tokens -= input_tokens;
        self.observe_input_interval(
            request,
            input_tokens,
            interval,
            InputUsage::Reported { cached_tokens },
        );
    }

    /// The client disconnected; the engine drops the request. `interval` is
    /// set when the request had produced first output.
    pub fn cancel(
        &mut self,
        now: Micros,
        request: usize,
        input_tokens: u64,
        interval: Option<InputInterval>,
    ) {
        if let Some(interval) = interval {
            self.output_phase_requests -= 1;
            self.observe_input_interval(request, input_tokens, interval, InputUsage::Missing);
        } else {
            self.input_phase_requests -= 1;
            self.prompt_work_tokens -= input_tokens;
        }
        self.total_input_tokens -= input_tokens;
        self.engine.cancel(now, request as u64);
    }

    /// Feeds one request event to Pylon's fallback window. Builds the sample
    /// the way `record_fallback_observation` in
    /// `pylon-lib/src/stats/projection.rs` does for a streamed Chat
    /// Completions request that expects usage
    /// (`--force-chat-completions-include-usage`) from an engine whose usage
    /// always reports cached prompt tokens, as MockDynamo's does.
    fn observe_input_interval(
        &mut self,
        request: usize,
        input_tokens: u64,
        interval: InputInterval,
        usage: InputUsage,
    ) {
        let estimate = &mut self.input_tps;
        let Some(fallback) = &mut estimate.fallback else {
            return;
        };
        // Cached tokens above the prompt total are inconsistent; the request
        // then counts total tokens and is not max-eligible. Without reported
        // cached tokens, Pylon counts a Chat request as a miss only once its
        // usage is explicit, which a request without usage never is.
        let uncached_tokens = match usage {
            InputUsage::Reported { cached_tokens } => input_tokens.checked_sub(cached_tokens),
            InputUsage::Pending | InputUsage::Missing => None,
        };
        let at = |micros: Micros| fallback.epoch + Duration::from_micros(micros);
        let request_id = request.to_string();
        let sample = InputIntervalSample {
            request_id: &request_id,
            interval: RequestInputInterval {
                submitted_at: at(interval.submitted),
                first_generated_output_at: at(interval.first_output),
            },
            prompt_tokens: input_tokens,
            input_tokens: uncached_tokens.unwrap_or(input_tokens),
            input_tokens_explicit: matches!(usage, InputUsage::Reported { .. }),
            max_input_tps_eligible: uncached_tokens.is_some(),
            pending: matches!(usage, InputUsage::Pending),
        };
        let Some(rate) = fallback.window.observe(sample, &fallback.config) else {
            return;
        };
        // `ModelMetricsState::publish_mean_input_tps`: the window rate is the
        // published mean, and only a max-eligible mean raises the maximum.
        estimate.last_mean = rate.input_tps;
        if rate.max_input_tps_eligible {
            estimate.max = estimate.max.max(rate.input_tps);
        }
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

    fn interval(submitted_ms: u64, first_output_ms: u64) -> InputInterval {
        InputInterval {
            submitted: submitted_ms * 1000,
            first_output: first_output_ms * 1000,
        }
    }

    /// A backend whose fallback window starts from 500 tokens/s.
    fn fallback_backend(window: usize, requests: usize, input_tokens: u64) -> Backend {
        let mut pylon = pylon();
        pylon.input_tps = InputTpsModel::FallbackWindow {
            initial: 500.0,
            window,
            duration_floor_ms: 10.0,
        };
        let mut backend =
            Backend::new("b".into(), 0, 1.0, engine(), &pylon, Instant::now()).unwrap();
        for request in 0..requests {
            backend.accept(0, request, spec(input_tokens));
        }
        backend
    }

    fn input_tps(backend: &Backend) -> (f64, f64) {
        let stats = backend.stats();
        (
            stats.last_mean_input_tps,
            stats
                .max_input_tps
                .expect("simulated Pylons report a maximum"),
        )
    }

    fn assert_tps(actual: (f64, f64), expected: (f64, f64)) {
        assert!(
            (actual.0 - expected.0).abs() < 1e-6 && (actual.1 - expected.1).abs() < 1e-6,
            "{actual:?} != {expected:?}"
        );
    }

    #[test]
    fn pylon_stats_track_phases_and_engine_concurrency() {
        let pylon = pylon();
        let mut backend =
            Backend::new("b".into(), 0, 1.0, engine(), &pylon, Instant::now()).unwrap();
        backend.accept(0, 0, spec(100));
        backend.accept(0, 1, spec(100));
        backend.accept(0, 2, spec(100));
        let stats = backend.stats();
        assert_eq!(stats.max_engine_concurrency, 2);
        assert_eq!(stats.num_running_queries, 3);
        assert_eq!(stats.queued_input_size, 300);
        assert_eq!(stats.queue_time_estimate_ms_by_priority.get(&0), Some(&300));

        backend.first_token(0, 100, interval(0, 10));
        backend.cancel(20_000, 2, 100, None);
        let stats = backend.stats();
        assert_eq!(stats.num_running_queries, 2);
        assert_eq!(stats.queued_input_size, 100);
    }

    #[test]
    fn mismatch_rejects_only_when_full_and_far_above_expected() {
        let pylon = pylon();
        let mut backend =
            Backend::new("b".into(), 0, 1.0, engine(), &pylon, Instant::now()).unwrap();
        backend.accept(0, 0, spec(1000));
        assert!(!backend.rejects(Some(0), &pylon.queue_mismatch));
        backend.accept(0, 1, spec(1000));
        // Full engine with 2,000 prompt tokens at 1,000 tokens/s is 2,000 ms.
        assert!(backend.rejects(Some(0), &pylon.queue_mismatch));
        assert!(!backend.rejects(Some(1700), &pylon.queue_mismatch));
        assert!(!backend.rejects(None, &pylon.queue_mismatch));
    }

    #[test]
    fn fallback_rate_waits_for_usage_and_counts_uncached_tokens() {
        let mut backend = fallback_backend(8, 2, 1000);
        // Two overlapping one-second intervals over 1.5 s.
        backend.first_token(0, 1000, interval(0, 1000));
        backend.first_token(1, 1000, interval(500, 1500));
        assert_tps(input_tps(&backend), (500.0, 500.0));
        // The older request still expects usage, so it holds the newer one
        // out of the rate.
        backend.complete(1, 1000, 0, interval(500, 1500));
        assert_tps(input_tps(&backend), (500.0, 500.0));
        // 200 cached tokens leave 800 + 1,000 uncached tokens over 1.5 s.
        backend.complete(0, 1000, 200, interval(0, 1000));
        let rate = 1800.0 / 1.5;
        assert_tps(input_tps(&backend), (rate, rate));
    }

    #[test]
    fn fallback_rate_drops_cache_hits_with_a_short_uncached_remainder() {
        let mut backend = fallback_backend(8, 2, 1000);
        backend.first_token(0, 1000, interval(0, 1000));
        backend.complete(0, 1000, 0, interval(0, 1000));
        assert_tps(input_tps(&backend), (1000.0, 1000.0));
        // 255 uncached tokens over 10 ms would be 25,500 tokens/s.
        backend.first_token(1, 1000, interval(2000, 2010));
        backend.complete(1, 1000, 745, interval(2000, 2010));
        assert_tps(input_tps(&backend), (1000.0, 1000.0));
    }

    #[test]
    fn fallback_maximum_rises_only_from_windows_that_reported_cached_tokens() {
        let mut backend = fallback_backend(1, 2, 1000);
        // A request cancelled after first output never reports usage, so it
        // counts total prompt tokens and its window cannot raise the maximum.
        backend.first_token(0, 1000, interval(0, 1000));
        backend.cancel(1_100_000, 0, 1000, Some(interval(0, 1000)));
        assert_tps(input_tps(&backend), (1000.0, 500.0));
        backend.first_token(1, 1000, interval(2000, 2500));
        backend.complete(1, 1000, 0, interval(2000, 2500));
        assert_tps(input_tps(&backend), (2000.0, 2000.0));
    }
}

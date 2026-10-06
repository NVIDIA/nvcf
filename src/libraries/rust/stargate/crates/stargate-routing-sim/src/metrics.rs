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

use std::time::Duration;

use serde::Serialize;

use crate::sim::RunSpec;
use crate::time::{Micros, micros_from_ms, ms};

#[derive(Clone, Debug, Default)]
pub struct RequestRecord {
    pub arrival: Micros,
    pub input_tokens: u64,
    pub stargate_region: usize,
    /// Load-balancer calls, including timed-wait rechecks.
    pub route_attempts: u32,
    pub mismatch_rejections: u32,
    pub dispatched_at: Option<Micros>,
    pub backend_received_at: Option<Micros>,
    pub reservation: Option<u64>,
    pub backend: Option<usize>,
    /// Ranking depth of the final selection; 1 is the primary candidate.
    pub rank_depth: usize,
    /// When the engine produced the first token, before the return trip.
    pub backend_first_token_at: Option<Micros>,
    pub reused_input_tokens: u64,
    pub first_token_at: Option<Micros>,
    pub completed_at: Option<Micros>,
    pub attempt: u32,
    /// The last allowed attempt of a growing-session turn failed.
    pub abandoned_session: bool,
    pub no_route: bool,
    pub retries_exhausted: bool,
    pub timed_out: bool,
}

impl RequestRecord {
    pub fn new(arrival: Micros, input_tokens: u64, stargate_region: usize) -> Self {
        Self {
            arrival,
            input_tokens,
            stargate_region,
            ..Default::default()
        }
    }

    fn succeeded(&self) -> bool {
        self.completed_at.is_some() && !self.timed_out
    }
}

#[derive(Clone, Debug, Serialize)]
pub struct RunSummary {
    pub policy: String,
    pub rate_rps: f64,
    pub seed: u64,
    pub offered: usize,
    pub succeeded: usize,
    /// Successful requests whose first token met the TTFT SLO.
    pub good: usize,
    pub failed_no_route: usize,
    pub failed_retries_exhausted: usize,
    pub failed_timeout: usize,
    /// Measured requests that retry a failed turn.
    pub retried_requests: usize,
    pub abandoned_sessions: usize,
    pub goodput_rps: f64,
    pub throughput_rps: f64,
    pub slo_attainment: f64,
    pub ttft_ms: Percentiles,
    pub e2e_ms: Percentiles,
    /// Prompt sizes of measured requests.
    pub input_tokens: Percentiles,
    /// Arrival to final dispatch, including affinity and bucket waits.
    pub routing_delay_ms: Percentiles,
    /// Backend arrival to first token at the backend: engine queueing plus prefill.
    pub backend_ttft_ms: Percentiles,
    pub cache_hit_rate: f64,
    pub reused_input_token_fraction: f64,
    pub mismatch_rejections: u64,
    pub mean_route_attempts: f64,
    pub cross_region_fraction: f64,
    /// Successful requests not served by their first-ranked candidate.
    pub off_primary_fraction: f64,
    /// Highest per-backend share of successful requests divided by the mean share.
    pub backend_load_peak_to_mean: f64,
    /// Successful requests by the backend that served them.
    pub backends: Vec<BackendSummary>,
    pub wall_clock_ms: f64,
}

#[derive(Clone, Debug, Serialize)]
pub struct BackendSummary {
    pub id: String,
    pub gpu_workers: usize,
    pub max_engine_concurrency: u64,
    pub succeeded: usize,
    pub ttft_ms: Percentiles,
    /// Backend arrival to first token: engine queueing plus prefill.
    pub backend_ttft_ms: Percentiles,
    pub reused_input_token_fraction: f64,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct Percentiles {
    pub p50: f64,
    pub p90: f64,
    pub p99: f64,
}

impl Percentiles {
    fn from_ms(mut values: Vec<f64>) -> Self {
        if values.is_empty() {
            return Self::default();
        }
        values.sort_by(f64::total_cmp);
        let pick = |quantile: f64| {
            let rank = (quantile * values.len() as f64).ceil() as usize;
            values[rank.clamp(1, values.len()) - 1]
        };
        Self {
            p50: pick(0.50),
            p90: pick(0.90),
            p99: pick(0.99),
        }
    }
}

pub fn summarize(
    spec: &RunSpec<'_>,
    records: &[RequestRecord],
    measure_start: Micros,
    measure_end: Micros,
    backend_ids: &[String],
    backend_engines: &[mock_engine::EngineConfig],
    wall_clock: Duration,
) -> RunSummary {
    let config = spec.config;
    let backend_regions: Vec<usize> = config
        .topology
        .regions
        .iter()
        .enumerate()
        .flat_map(|(region, spec)| std::iter::repeat_n(region, spec.backends))
        .collect();
    debug_assert_eq!(backend_regions.len(), backend_ids.len());
    let measured: Vec<&RequestRecord> = records
        .iter()
        .filter(|record| (measure_start..measure_end).contains(&record.arrival))
        .collect();
    let succeeded: Vec<&RequestRecord> = measured
        .iter()
        .copied()
        .filter(|record| record.succeeded())
        .collect();
    let ttft = |record: &RequestRecord| {
        record
            .first_token_at
            .expect("successful request has a first token")
            - record.arrival
    };
    let ttft_slo = micros_from_ms(config.client.ttft_slo_ms as f64);
    let good = succeeded
        .iter()
        .filter(|record| ttft(record) <= ttft_slo)
        .count();
    let measure_seconds = (measure_end - measure_start) as f64 / 1_000_000.0;
    let reused: u64 = succeeded
        .iter()
        .map(|record| record.reused_input_tokens)
        .sum();
    let input: u64 = succeeded.iter().map(|record| record.input_tokens).sum();
    let mut per_backend = vec![0usize; backend_ids.len()];
    let mut cross_region = 0usize;
    for record in &succeeded {
        let backend = record.backend.expect("successful request has a backend");
        per_backend[backend] += 1;
        if record.stargate_region != backend_regions[backend] {
            cross_region += 1;
        }
    }
    let mean_backend = succeeded.len() as f64 / backend_ids.len().max(1) as f64;
    let ratio = |numerator: usize, denominator: usize| {
        if denominator == 0 {
            0.0
        } else {
            numerator as f64 / denominator as f64
        }
    };

    RunSummary {
        policy: spec.policy.name.clone(),
        rate_rps: spec.rate_rps,
        seed: spec.seed,
        offered: measured.len(),
        succeeded: succeeded.len(),
        good,
        failed_no_route: measured.iter().filter(|record| record.no_route).count(),
        failed_retries_exhausted: measured
            .iter()
            .filter(|record| record.retries_exhausted)
            .count(),
        failed_timeout: measured.iter().filter(|record| record.timed_out).count(),
        retried_requests: measured.iter().filter(|record| record.attempt > 0).count(),
        abandoned_sessions: measured
            .iter()
            .filter(|record| record.abandoned_session)
            .count(),
        goodput_rps: good as f64 / measure_seconds,
        throughput_rps: succeeded.len() as f64 / measure_seconds,
        slo_attainment: ratio(good, measured.len()),
        ttft_ms: Percentiles::from_ms(succeeded.iter().map(|record| ms(ttft(record))).collect()),
        input_tokens: Percentiles::from_ms(
            measured
                .iter()
                .map(|record| record.input_tokens as f64)
                .collect(),
        ),
        e2e_ms: Percentiles::from_ms(
            succeeded
                .iter()
                .map(|record| {
                    ms(record
                        .completed_at
                        .expect("successful request has a completion")
                        - record.arrival)
                })
                .collect(),
        ),
        routing_delay_ms: Percentiles::from_ms(
            succeeded
                .iter()
                .filter_map(|record| record.dispatched_at.map(|at| ms(at - record.arrival)))
                .collect(),
        ),
        backend_ttft_ms: Percentiles::from_ms(
            succeeded
                .iter()
                .filter_map(|record| {
                    Some(ms(
                        record.backend_first_token_at? - record.backend_received_at?
                    ))
                })
                .collect(),
        ),
        cache_hit_rate: ratio(
            succeeded
                .iter()
                .filter(|record| record.reused_input_tokens > 0)
                .count(),
            succeeded.len(),
        ),
        reused_input_token_fraction: if input == 0 {
            0.0
        } else {
            reused as f64 / input as f64
        },
        mismatch_rejections: measured
            .iter()
            .map(|record| u64::from(record.mismatch_rejections))
            .sum(),
        mean_route_attempts: if measured.is_empty() {
            0.0
        } else {
            measured
                .iter()
                .map(|record| f64::from(record.route_attempts))
                .sum::<f64>()
                / measured.len() as f64
        },
        cross_region_fraction: ratio(cross_region, succeeded.len()),
        off_primary_fraction: ratio(
            succeeded
                .iter()
                .filter(|record| record.rank_depth > 1)
                .count(),
            succeeded.len(),
        ),
        backends: backend_ids
            .iter()
            .zip(backend_engines)
            .enumerate()
            .map(|(index, (id, engine))| {
                let served: Vec<&RequestRecord> = succeeded
                    .iter()
                    .copied()
                    .filter(|record| record.backend == Some(index))
                    .collect();
                let input: u64 = served.iter().map(|record| record.input_tokens).sum();
                let reused: u64 = served.iter().map(|record| record.reused_input_tokens).sum();
                BackendSummary {
                    id: id.clone(),
                    gpu_workers: engine.num_gpu_workers,
                    max_engine_concurrency: engine.max_concurrency(),
                    succeeded: served.len(),
                    ttft_ms: Percentiles::from_ms(
                        served.iter().map(|record| ms(ttft(record))).collect(),
                    ),
                    backend_ttft_ms: Percentiles::from_ms(
                        served
                            .iter()
                            .filter_map(|record| {
                                Some(ms(
                                    record.backend_first_token_at? - record.backend_received_at?
                                ))
                            })
                            .collect(),
                    ),
                    reused_input_token_fraction: if input == 0 {
                        0.0
                    } else {
                        reused as f64 / input as f64
                    },
                }
            })
            .collect(),
        backend_load_peak_to_mean: if mean_backend > 0.0 {
            per_backend.iter().copied().max().unwrap_or_default() as f64 / mean_backend
        } else {
            0.0
        },
        wall_clock_ms: wall_clock.as_secs_f64() * 1000.0,
    }
}

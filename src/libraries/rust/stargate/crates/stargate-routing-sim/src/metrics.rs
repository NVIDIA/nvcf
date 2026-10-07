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

use crate::backend::Backend;
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
    fn succeeded(&self) -> bool {
        self.completed_at.is_some() && !self.timed_out
    }

    fn ttft(&self) -> Micros {
        self.first_token_at
            .expect("successful request has a first token")
            - self.arrival
    }

    /// Backend arrival to first token at the backend.
    fn backend_ttft(&self) -> Micros {
        self.backend_first_token_at
            .expect("successful request has a backend first token")
            - self
                .backend_received_at
                .expect("successful request reached its backend")
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

/// Nearest-rank percentiles; all zero when there are no samples.
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

fn ratio(numerator: f64, denominator: f64) -> f64 {
    if denominator == 0.0 {
        0.0
    } else {
        numerator / denominator
    }
}

/// Fraction of input tokens served from the prefix cache.
fn reused_fraction(records: &[&RequestRecord]) -> f64 {
    let reused: u64 = records
        .iter()
        .map(|record| record.reused_input_tokens)
        .sum();
    let input: u64 = records.iter().map(|record| record.input_tokens).sum();
    ratio(reused as f64, input as f64)
}

pub fn summarize(
    spec: &RunSpec<'_>,
    records: &[RequestRecord],
    measure_start: Micros,
    measure_end: Micros,
    backends: &[Backend],
    wall_clock: Duration,
) -> RunSummary {
    let config = spec.config;
    let measured: Vec<&RequestRecord> = records
        .iter()
        .filter(|record| (measure_start..measure_end).contains(&record.arrival))
        .collect();
    let succeeded: Vec<&RequestRecord> = measured
        .iter()
        .copied()
        .filter(|record| record.succeeded())
        .collect();
    let mut served: Vec<Vec<&RequestRecord>> = vec![Vec::new(); backends.len()];
    let mut cross_region = 0usize;
    for record in &succeeded {
        let backend = record.backend.expect("successful request has a backend");
        served[backend].push(record);
        if backends[backend].region != record.stargate_region {
            cross_region += 1;
        }
    }
    let ttft_slo = micros_from_ms(config.client.ttft_slo_ms as f64);
    let good = succeeded
        .iter()
        .filter(|record| record.ttft() <= ttft_slo)
        .count();
    let measure_seconds = (measure_end - measure_start) as f64 / 1_000_000.0;
    let count = |predicate: fn(&RequestRecord) -> bool| {
        measured.iter().filter(|record| predicate(record)).count()
    };
    let mean_backend = succeeded.len() as f64 / backends.len() as f64;

    RunSummary {
        policy: spec.policy.name.clone(),
        rate_rps: spec.rate_rps,
        seed: spec.seed,
        offered: measured.len(),
        succeeded: succeeded.len(),
        good,
        failed_no_route: count(|record| record.no_route),
        failed_retries_exhausted: count(|record| record.retries_exhausted),
        failed_timeout: count(|record| record.timed_out),
        retried_requests: count(|record| record.attempt > 0),
        abandoned_sessions: count(|record| record.abandoned_session),
        goodput_rps: good as f64 / measure_seconds,
        throughput_rps: succeeded.len() as f64 / measure_seconds,
        slo_attainment: ratio(good as f64, measured.len() as f64),
        ttft_ms: Percentiles::from_ms(succeeded.iter().map(|record| ms(record.ttft())).collect()),
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
                .map(|record| ms(record.backend_ttft()))
                .collect(),
        ),
        cache_hit_rate: ratio(
            succeeded
                .iter()
                .filter(|record| record.reused_input_tokens > 0)
                .count() as f64,
            succeeded.len() as f64,
        ),
        reused_input_token_fraction: reused_fraction(&succeeded),
        mismatch_rejections: measured
            .iter()
            .map(|record| u64::from(record.mismatch_rejections))
            .sum(),
        mean_route_attempts: ratio(
            measured
                .iter()
                .map(|record| f64::from(record.route_attempts))
                .sum(),
            measured.len() as f64,
        ),
        cross_region_fraction: ratio(cross_region as f64, succeeded.len() as f64),
        off_primary_fraction: ratio(
            succeeded
                .iter()
                .filter(|record| record.rank_depth > 1)
                .count() as f64,
            succeeded.len() as f64,
        ),
        backend_load_peak_to_mean: ratio(
            served.iter().map(Vec::len).max().unwrap_or_default() as f64,
            mean_backend,
        ),
        backends: backends
            .iter()
            .zip(&served)
            .map(|(backend, served)| BackendSummary {
                id: backend.id.clone(),
                gpu_workers: backend.gpu_workers,
                max_engine_concurrency: backend.max_engine_concurrency,
                succeeded: served.len(),
                ttft_ms: Percentiles::from_ms(
                    served.iter().map(|record| ms(record.ttft())).collect(),
                ),
                backend_ttft_ms: Percentiles::from_ms(
                    served
                        .iter()
                        .map(|record| ms(record.backend_ttft()))
                        .collect(),
                ),
                reused_input_token_fraction: reused_fraction(served),
            })
            .collect(),
        wall_clock_ms: wall_clock.as_secs_f64() * 1000.0,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn percentiles_use_nearest_rank_and_zero_for_no_samples() {
        let percentiles = Percentiles::from_ms((1..=100).rev().map(f64::from).collect());
        assert_eq!(
            (percentiles.p50, percentiles.p90, percentiles.p99),
            (50.0, 90.0, 99.0)
        );
        let single = Percentiles::from_ms(vec![7.0]);
        assert_eq!((single.p50, single.p99), (7.0, 7.0));
        let empty = Percentiles::from_ms(Vec::new());
        assert_eq!((empty.p50, empty.p99), (0.0, 0.0));
    }
}

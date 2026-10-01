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

//! Open-loop request generation. The arrival sequence and every per-request
//! random draw depend only on the seed, so each policy sees identical demand
//! (common random numbers) and paired differences have low variance.

use rand::rngs::StdRng;
use rand::{Rng, SeedableRng};

use crate::config::{EngineConfig, WorkloadConfig};
use crate::time::{Micros, micros_from_secs};

#[derive(Clone, Debug)]
pub struct PlannedRequest {
    pub arrival: Micros,
    pub session: u32,
    pub stargate: usize,
    pub input_tokens: u64,
    pub output_tokens: u64,
    /// Engine-side draws, fixed per request like MockDynamo's request-ID hashing.
    pub ttft_jitter: Micros,
    pub decode_tokens_per_s: f64,
}

pub struct WorkloadPlan {
    pub requests: Vec<PlannedRequest>,
    pub measure_start: Micros,
    pub measure_end: Micros,
}

pub fn plan(
    workload: &WorkloadConfig,
    engine: &EngineConfig,
    stargate_weights: &[f64],
    rate_rps: f64,
    seed: u64,
) -> WorkloadPlan {
    let mut rng = StdRng::seed_from_u64(seed);
    let session_input_tokens: Vec<u64> = (0..workload.sessions)
        .map(|_| rng.random_range(workload.input_tokens_min..=workload.input_tokens_max))
        .collect();
    let session_cdf = cumulative(
        &(1..=workload.sessions)
            .map(|rank| 1.0 / (rank as f64).powf(workload.session_zipf_s))
            .collect::<Vec<_>>(),
    );
    let stargate_cdf = cumulative(stargate_weights);

    let measure_start = micros_from_secs(workload.warmup_s);
    let measure_end = measure_start + micros_from_secs(workload.measure_s);
    let mut requests = Vec::new();
    let mut now = 0.0_f64;
    loop {
        let unit: f64 = rng.random_range(f64::EPSILON..1.0);
        now += -unit.ln() / rate_rps;
        let arrival = micros_from_secs(now);
        if arrival >= measure_end {
            break;
        }
        let session = sample(&session_cdf, rng.random());
        requests.push(PlannedRequest {
            arrival,
            session: u32::try_from(session).expect("session count fits in u32"),
            stargate: sample(&stargate_cdf, rng.random()),
            input_tokens: session_input_tokens[session],
            output_tokens: workload.output_tokens,
            ttft_jitter: micros_from_secs(rng.random_range(0.0..=engine.ttft_jitter_ms) / 1000.0),
            decode_tokens_per_s: rng
                .random_range(engine.decode_tokens_per_s_min..=engine.decode_tokens_per_s_max),
        });
    }
    WorkloadPlan {
        requests,
        measure_start,
        measure_end,
    }
}

fn cumulative(weights: &[f64]) -> Vec<f64> {
    let total: f64 = weights.iter().sum();
    let mut running = 0.0;
    weights
        .iter()
        .map(|weight| {
            running += weight / total;
            running
        })
        .collect()
}

fn sample(cdf: &[f64], unit: f64) -> usize {
    cdf.partition_point(|bound| *bound < unit)
        .min(cdf.len() - 1)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn workload() -> (WorkloadConfig, EngineConfig) {
        (
            WorkloadConfig {
                sessions: 10,
                input_tokens_min: 100,
                input_tokens_max: 200,
                output_tokens: 16,
                session_zipf_s: 0.0,
                rates_rps: vec![50.0],
                warmup_s: 1.0,
                measure_s: 9.0,
            },
            EngineConfig {
                slots: 4,
                prefill_tokens_per_s: 1000.0,
                ttft_base_ms: 10.0,
                ttft_jitter_ms: 20.0,
                decode_tokens_per_s_min: 100.0,
                decode_tokens_per_s_max: 200.0,
                kv_cache_capacity_tokens: 1000,
            },
        )
    }

    #[test]
    fn plan_is_deterministic_for_a_seed() {
        let (workload, engine) = workload();
        let a = plan(&workload, &engine, &[1.0, 1.0], 50.0, 7);
        let b = plan(&workload, &engine, &[1.0, 1.0], 50.0, 7);
        assert_eq!(a.requests.len(), b.requests.len());
        assert!(
            a.requests
                .iter()
                .zip(&b.requests)
                .all(|(x, y)| x.arrival == y.arrival && x.session == y.session)
        );
    }

    #[test]
    fn plan_rate_matches_poisson_mean() {
        let (workload, engine) = workload();
        let planned = plan(&workload, &engine, &[1.0], 50.0, 11);
        let count = planned.requests.len() as f64;
        assert!((count - 500.0).abs() < 80.0, "count={count}");
    }

    #[test]
    fn session_inputs_stay_fixed_and_in_range() {
        let (workload, engine) = workload();
        let planned = plan(&workload, &engine, &[1.0], 50.0, 3);
        let mut by_session = std::collections::HashMap::new();
        for request in &planned.requests {
            assert!((100..=200).contains(&request.input_tokens));
            let first = by_session
                .entry(request.session)
                .or_insert(request.input_tokens);
            assert_eq!(*first, request.input_tokens);
        }
    }
}

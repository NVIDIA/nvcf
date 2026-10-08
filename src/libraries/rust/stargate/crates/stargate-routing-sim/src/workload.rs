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

//! Request generation. Arrival times, sessions, and every per-request random
//! draw depend only on the seed, so each policy sees identical demand (common
//! random numbers) and paired differences have low variance. Growing sessions
//! fix every turn's size and think time up front; only when later turns start
//! depends on how fast the policy served earlier ones.

use rand::rngs::StdRng;
use rand::{Rng, SeedableRng};

use crate::config::{FixedSessionsConfig, GrowingSessionsConfig, WorkloadConfig};
use crate::time::{Micros, micros_from_secs};

#[derive(Clone, Debug)]
pub struct PlannedRequest {
    pub arrival: Micros,
    pub session: u32,
    /// Zero-based turn within a growing session; zero for fixed sessions.
    pub turn: u32,
    /// Zero-based attempt of this turn; retries reuse the same prompt.
    pub attempt: u32,
    pub stargate: usize,
    pub input_tokens: u64,
    pub output_tokens: u64,
}

#[derive(Clone, Debug)]
pub struct PlannedTurn {
    pub input_tokens: u64,
    pub output_tokens: u64,
    /// Delay after the previous turn's response; zero for the first turn.
    pub think_time: Micros,
}

#[derive(Clone, Debug)]
pub struct PlannedSession {
    pub stargate: usize,
    pub turns: Vec<PlannedTurn>,
}

pub struct WorkloadPlan {
    /// Requests whose arrival is known up front: every fixed-session request,
    /// or the first turn of each growing session.
    pub requests: Vec<PlannedRequest>,
    /// Growing sessions, indexed by session ID. Empty for fixed sessions.
    pub sessions: Vec<PlannedSession>,
    pub measure_start: Micros,
    pub measure_end: Micros,
}

impl WorkloadPlan {
    /// The request for `turn` of a growing session arriving at `arrival`.
    pub fn turn_request(
        &self,
        session: u32,
        turn: u32,
        attempt: u32,
        arrival: Micros,
    ) -> Option<PlannedRequest> {
        let planned = self.sessions.get(session as usize)?;
        let spec = planned.turns.get(turn as usize)?;
        Some(PlannedRequest {
            arrival,
            session,
            turn,
            attempt,
            stargate: planned.stargate,
            input_tokens: spec.input_tokens,
            output_tokens: spec.output_tokens,
        })
    }
}

pub fn plan(
    workload: &WorkloadConfig,
    stargate_weights: &[f64],
    rate_rps: f64,
    seed: u64,
) -> WorkloadPlan {
    let measure_start = micros_from_secs(workload.warmup_s);
    let measure_end = measure_start + micros_from_secs(workload.measure_s);
    let stargate_cdf = cumulative(stargate_weights);
    let mut rng = StdRng::seed_from_u64(seed);
    let (requests, sessions) = match (&workload.fixed, &workload.growing) {
        (Some(fixed), _) => (
            plan_fixed(fixed, &stargate_cdf, rate_rps, measure_end, &mut rng),
            Vec::new(),
        ),
        (None, Some(growing)) => plan_growing(
            growing,
            &stargate_cdf,
            rate_rps,
            measure_end,
            seed,
            &mut rng,
        ),
        (None, None) => unreachable!("validated workload has a session model"),
    };
    WorkloadPlan {
        requests,
        sessions,
        measure_start,
        measure_end,
    }
}

fn plan_fixed(
    fixed: &FixedSessionsConfig,
    stargate_cdf: &[f64],
    rate_rps: f64,
    measure_end: Micros,
    rng: &mut StdRng,
) -> Vec<PlannedRequest> {
    let session_input_tokens: Vec<u64> = (0..fixed.sessions)
        .map(|_| rng.random_range(fixed.input_tokens_min..=fixed.input_tokens_max))
        .collect();
    let session_cdf = cumulative(
        &(1..=fixed.sessions)
            .map(|rank| 1.0 / (rank as f64).powf(fixed.session_zipf_s))
            .collect::<Vec<_>>(),
    );
    poisson_arrivals(rate_rps, measure_end, rng)
        .into_iter()
        .map(|arrival| {
            let session = sample(&session_cdf, rng.random());
            PlannedRequest {
                arrival,
                session: u32::try_from(session).expect("session count fits in u32"),
                turn: 0,
                attempt: 0,
                stargate: sample(stargate_cdf, rng.random()),
                input_tokens: session_input_tokens[session],
                output_tokens: fixed.output_tokens,
            }
        })
        .collect()
}

fn plan_growing(
    growing: &GrowingSessionsConfig,
    stargate_cdf: &[f64],
    rate_rps: f64,
    measure_end: Micros,
    seed: u64,
    rng: &mut StdRng,
) -> (Vec<PlannedRequest>, Vec<PlannedSession>) {
    // Sessions per second that offer `rate_rps` requests per second, using
    // the mean turn count after context-limit truncation.
    let mut estimate_rng = StdRng::seed_from_u64(seed ^ 0x7e57_7a1e);
    let samples = 10_000;
    let mean_turns = (0..samples)
        .map(|_| sample_session(growing, 0, &mut estimate_rng).turns.len() as f64)
        .sum::<f64>()
        / f64::from(samples);
    let session_rate = rate_rps / mean_turns.max(1.0);

    let mut requests = Vec::new();
    let mut sessions = Vec::new();
    for arrival in poisson_arrivals(session_rate, measure_end, rng) {
        let stargate = sample(stargate_cdf, rng.random());
        let session = sample_session(growing, stargate, rng);
        let id = u32::try_from(sessions.len()).expect("session count fits in u32");
        let first = &session.turns[0];
        requests.push(PlannedRequest {
            arrival,
            session: id,
            turn: 0,
            attempt: 0,
            stargate,
            input_tokens: first.input_tokens,
            output_tokens: first.output_tokens,
        });
        sessions.push(session);
    }
    (requests, sessions)
}

fn sample_session(
    growing: &GrowingSessionsConfig,
    stargate: usize,
    rng: &mut StdRng,
) -> PlannedSession {
    let turn_count = rng.random_range(growing.turns_min..=growing.turns_max);
    let mut turns: Vec<PlannedTurn> = Vec::with_capacity(turn_count as usize);
    for turn in 0..turn_count {
        let user = rng.random_range(growing.user_tokens_min..=growing.user_tokens_max);
        let output_tokens = rng.random_range(growing.output_tokens_min..=growing.output_tokens_max);
        let think: f64 = rng.random_range(f64::EPSILON..1.0);
        let input_tokens = match turns.last() {
            None => growing.system_prompt_tokens + user,
            Some(previous) => previous.input_tokens + previous.output_tokens + user,
        };
        if input_tokens + output_tokens > growing.max_context_tokens {
            break;
        }
        turns.push(PlannedTurn {
            input_tokens,
            output_tokens,
            think_time: if turn == 0 {
                0
            } else {
                micros_from_secs(-think.ln() * growing.think_time_mean_s)
            },
        });
    }
    PlannedSession { stargate, turns }
}

fn poisson_arrivals(rate: f64, end: Micros, rng: &mut StdRng) -> Vec<Micros> {
    let mut arrivals = Vec::new();
    let mut now = 0.0_f64;
    loop {
        let unit: f64 = rng.random_range(f64::EPSILON..1.0);
        now += -unit.ln() / rate;
        let arrival = micros_from_secs(now);
        if arrival >= end {
            return arrivals;
        }
        arrivals.push(arrival);
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

    fn fixed_workload() -> WorkloadConfig {
        WorkloadConfig {
            rates_rps: vec![50.0],
            warmup_s: 1.0,
            measure_s: 9.0,
            fixed: Some(FixedSessionsConfig {
                sessions: 10,
                input_tokens_min: 100,
                input_tokens_max: 200,
                output_tokens: 16,
                session_zipf_s: 0.0,
            }),
            growing: None,
        }
    }

    fn growing_workload() -> WorkloadConfig {
        WorkloadConfig {
            fixed: None,
            growing: Some(GrowingSessionsConfig {
                system_prompt_tokens: 1000,
                user_tokens_min: 100,
                user_tokens_max: 200,
                output_tokens_min: 50,
                output_tokens_max: 100,
                turns_min: 2,
                turns_max: 30,
                think_time_mean_s: 5.0,
                max_context_tokens: 4000,
                max_turn_attempts: 3,
                retry_backoff_s: 1.0,
            }),
            ..fixed_workload()
        }
    }

    #[test]
    fn plan_is_deterministic_for_a_seed() {
        let workload = fixed_workload();
        let a = plan(&workload, &[1.0, 1.0], 50.0, 7);
        let b = plan(&workload, &[1.0, 1.0], 50.0, 7);
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
        let planned = plan(&fixed_workload(), &[1.0], 50.0, 11);
        let count = planned.requests.len() as f64;
        assert!((count - 500.0).abs() < 80.0, "count={count}");
    }

    #[test]
    fn fixed_session_inputs_stay_fixed_and_in_range() {
        let planned = plan(&fixed_workload(), &[1.0], 50.0, 3);
        let mut by_session = std::collections::HashMap::new();
        for request in &planned.requests {
            assert!((100..=200).contains(&request.input_tokens));
            let first = by_session
                .entry(request.session)
                .or_insert(request.input_tokens);
            assert_eq!(*first, request.input_tokens);
        }
    }

    #[test]
    fn growing_turns_extend_the_previous_prompt_and_output() {
        let planned = plan(&growing_workload(), &[1.0], 50.0, 5);
        assert!(!planned.sessions.is_empty());
        for session in &planned.sessions {
            assert!(!session.turns.is_empty());
            assert_eq!(session.turns[0].think_time, 0);
            for pair in session.turns.windows(2) {
                let growth = pair[1].input_tokens - pair[0].input_tokens - pair[0].output_tokens;
                assert!((100..=200).contains(&growth));
            }
            let last = session.turns.last().unwrap();
            assert!(last.input_tokens + last.output_tokens <= 4000);
        }
    }

    #[test]
    fn growing_session_rate_offers_the_target_request_rate() {
        let workload = growing_workload();
        let planned = plan(&workload, &[1.0], 50.0, 9);
        let offered: usize = planned
            .sessions
            .iter()
            .map(|session| session.turns.len())
            .sum();
        // 10 s of session arrivals at 50 requests per second.
        assert!((offered as f64 - 500.0).abs() < 150.0, "offered={offered}");
        let request = planned.turn_request(0, 1, 0, 123).unwrap();
        assert_eq!(request.arrival, 123);
        assert_eq!(
            request.input_tokens,
            planned.sessions[0].turns[1].input_tokens
        );
    }
}

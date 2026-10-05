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

use super::*;

/// 1 ms per step, 0.1 ms per decoding sequence, 10 us per prefill token.
fn config(num_gpu_workers: usize) -> EngineConfig {
    EngineConfig {
        num_gpu_workers,
        max_num_seqs: 4,
        max_batched_tokens: 1000,
        step_fixed_ms: 1.0,
        step_decode_ms_per_seq: 0.1,
        step_prefill_ms_per_token: 0.01,
        kv_cache_capacity_tokens: 100_000,
    }
}

fn spec(cache_key: Option<u64>, input_tokens: u64, output_tokens: u64) -> RequestSpec {
    RequestSpec {
        cache_key,
        input_tokens,
        output_tokens,
    }
}

/// Runs the engine until idle and returns every event.
fn drain(engine: &mut Engine) -> Vec<EngineEvent> {
    let mut events = Vec::new();
    while let Some(next) = engine.next_event_time() {
        events.extend(engine.advance_to(next));
    }
    events
}

fn first_token_at(events: &[EngineEvent], request: RequestId) -> Micros {
    events
        .iter()
        .find_map(|event| match event {
            EngineEvent::FirstToken { id, at, .. } if *id == request => Some(*at),
            _ => None,
        })
        .expect("request produced a first token")
}

fn completed_at(events: &[EngineEvent], request: RequestId) -> Micros {
    events
        .iter()
        .find_map(|event| match event {
            EngineEvent::Completed { id, at } if *id == request => Some(*at),
            _ => None,
        })
        .expect("request completed")
}

#[test]
fn single_request_prefills_in_chunks_then_decodes() {
    let mut engine = Engine::new(config(1), false).unwrap();
    engine.submit(0, 1, spec(None, 2500, 3));
    let events = drain(&mut engine);
    // Prefill steps of 1000, 1000 and 500 tokens: 11 + 11 + 6 ms.
    assert_eq!(first_token_at(&events, 1), 28_000);
    // Two decode steps of 1.1 ms each.
    assert_eq!(completed_at(&events, 1), 30_200);
}

#[test]
fn concurrent_prefills_share_the_token_budget() {
    let mut engine = Engine::new(config(1), false).unwrap();
    engine.submit(0, 1, spec(None, 1000, 1));
    let alone = first_token_at(&drain(&mut engine), 1);

    let mut engine = Engine::new(config(1), false).unwrap();
    engine.submit(0, 1, spec(None, 1000, 1));
    engine.submit(0, 2, spec(None, 1000, 1));
    let events = drain(&mut engine);
    assert_eq!(first_token_at(&events, 1), alone);
    // The second prompt waits for the first step's budget.
    assert_eq!(first_token_at(&events, 2), 2 * alone);
}

#[test]
fn prefill_slows_decode_on_the_same_worker() {
    let mut engine = Engine::new(config(1), true).unwrap();
    engine.submit(0, 1, spec(None, 10, 100));
    let events = engine.advance_to(20_000);
    let token_times: Vec<Micros> = events
        .iter()
        .filter_map(|event| match event {
            EngineEvent::Token { id: 1, at, .. } => Some(*at),
            _ => None,
        })
        .collect();
    let quiet_gap = token_times[1] - token_times[0];
    assert_eq!(quiet_gap, 1_100);

    engine.submit(20_000, 2, spec(None, 900, 1));
    let events = engine.advance_to(40_000);
    let busy_gap = events
        .iter()
        .filter_map(|event| match event {
            EngineEvent::Token { id: 1, at, .. } => Some(*at),
            _ => None,
        })
        .collect::<Vec<_>>()
        .windows(2)
        .map(|pair| pair[1] - pair[0])
        .max()
        .unwrap();
    // One step carries 899 prefill tokens beside the decode token.
    assert!(
        busy_gap >= 10_000,
        "decode gap during prefill was {busy_gap}"
    );
}

#[test]
fn cached_prefix_skips_prefill_and_routes_to_its_worker() {
    let mut engine = Engine::new(config(2), false).unwrap();
    let cold_worker = engine.submit(0, 1, spec(Some(7), 2000, 1));
    let cold = drain(&mut engine);
    let cold_ttft = first_token_at(&cold, 1);

    let warm_worker = engine.submit(100_000, 2, spec(Some(7), 2000, 1));
    let warm = drain(&mut engine);
    assert_eq!(warm_worker, cold_worker);
    assert!(first_token_at(&warm, 2) - 100_000 < cold_ttft / 10);
    assert!(warm.iter().any(|event| matches!(
        event,
        EngineEvent::FirstToken {
            id: 2,
            reused_input_tokens: 2000,
            ..
        }
    )));
}

#[test]
fn new_keys_spread_to_the_least_loaded_worker() {
    let mut engine = Engine::new(config(3), false).unwrap();
    let workers: Vec<usize> = (0..3)
        .map(|id| engine.submit(0, id, spec(Some(id), 500, 10)))
        .collect();
    assert_eq!(workers, vec![0, 1, 2]);
}

#[test]
fn max_num_seqs_queues_extra_requests() {
    let mut engine = Engine::new(config(1), false).unwrap();
    for id in 0..5 {
        engine.submit(0, id, spec(None, 10, 50));
    }
    // Admission happens at step boundaries, as in a real scheduler.
    let mut events = engine.advance_to(engine.next_event_time().unwrap());
    let stats = &engine.worker_stats()[0];
    assert_eq!((stats.running, stats.waiting), (4, 1));
    events.extend(drain(&mut engine));
    let first_done = (0..4).map(|id| completed_at(&events, id)).min().unwrap();
    assert!(first_token_at(&events, 4) > first_done);
}

#[test]
fn kv_capacity_evicts_unpinned_prefixes_lru_first() {
    let mut engine = Engine::new(
        EngineConfig {
            kv_cache_capacity_tokens: 3000,
            ..config(1)
        },
        false,
    )
    .unwrap();
    for (id, key) in [(1, 10), (2, 20)] {
        engine.submit(id * 1_000_000, id, spec(Some(key), 1000, 1));
        drain(&mut engine);
    }
    // Key 30 needs 1001 tokens; key 10 is least recently used.
    engine.submit(3_000_000, 3, spec(Some(30), 1000, 1));
    drain(&mut engine);
    engine.submit(4_000_000, 4, spec(Some(20), 1000, 1));
    engine.submit(4_000_000, 5, spec(Some(10), 1000, 1));
    let events = drain(&mut engine);
    let reused = |request| {
        events
            .iter()
            .find_map(|event| match event {
                EngineEvent::FirstToken {
                    id,
                    reused_input_tokens,
                    ..
                } if *id == request => Some(*reused_input_tokens),
                _ => None,
            })
            .unwrap()
    };
    assert_eq!(reused(4), 1000);
    assert_eq!(reused(5), 0);
}

#[test]
fn memory_pressure_delays_admission_until_space_frees() {
    let mut engine = Engine::new(
        EngineConfig {
            kv_cache_capacity_tokens: 1500,
            ..config(1)
        },
        false,
    )
    .unwrap();
    engine.submit(0, 1, spec(None, 1000, 20));
    engine.submit(0, 2, spec(None, 1000, 20));
    assert_eq!(engine.worker_stats()[0].running, 1);
    let events = drain(&mut engine);
    assert!(first_token_at(&events, 2) > completed_at(&events, 1));
}

#[test]
fn oversized_request_runs_alone_instead_of_blocking() {
    let mut engine = Engine::new(
        EngineConfig {
            kv_cache_capacity_tokens: 100,
            ..config(1)
        },
        false,
    )
    .unwrap();
    engine.submit(0, 1, spec(None, 500, 2));
    let events = drain(&mut engine);
    assert!(completed_at(&events, 1) > 0);
}

#[test]
fn cancel_frees_a_slot_for_waiting_work() {
    let mut engine = Engine::new(
        EngineConfig {
            max_num_seqs: 1,
            ..config(1)
        },
        false,
    )
    .unwrap();
    engine.submit(0, 1, spec(None, 10, 10_000));
    engine.submit(0, 2, spec(None, 10, 1));
    assert!(engine.cancel(5_000, 1));
    let events = drain(&mut engine);
    assert!(
        !events
            .iter()
            .any(|event| matches!(event, EngineEvent::Completed { id: 1, .. }))
    );
    assert!(first_token_at(&events, 2) < 10_000);
    assert!(!engine.cancel(20_000, 1));
}

#[test]
fn max_concurrency_counts_every_worker() {
    assert_eq!(config(3).max_concurrency(), 12);
    assert_eq!(EngineConfig::h100_llama_3_1_8b(8).max_concurrency(), 8 * 25);
}

#[test]
fn h100_profile_matches_its_documented_rates() {
    let config = EngineConfig::h100_llama_3_1_8b(1);
    let decode_step_ms = config.step_fixed_ms + 25.0 * config.step_decode_ms_per_seq;
    let decode_tps = 1000.0 / decode_step_ms;
    assert!((150.0..175.0).contains(&decode_tps), "decode {decode_tps}");
    let prefill_tps = 1000.0 / config.step_prefill_ms_per_token;
    assert!(
        (15_000.0..25_000.0).contains(&prefill_tps),
        "prefill {prefill_tps}"
    );
}

#[test]
fn invalid_configs_are_rejected() {
    for broken in [
        EngineConfig {
            num_gpu_workers: 0,
            ..config(1)
        },
        EngineConfig {
            max_num_seqs: 0,
            ..config(1)
        },
        EngineConfig {
            max_batched_tokens: 0,
            ..config(1)
        },
        EngineConfig {
            step_fixed_ms: f64::NAN,
            ..config(1)
        },
    ] {
        assert!(Engine::new(broken, false).is_err());
    }
}

#[test]
fn completed_output_extends_the_cache_for_the_next_turn() {
    let mut engine = Engine::new(config(1), false).unwrap();
    engine.submit(0, 1, spec(Some(9), 1000, 200));
    drain(&mut engine);
    assert_eq!(engine.worker_stats()[0].kv_cache_used_tokens, 1200);

    // The next turn's prompt is the previous prompt, its output, and 300 new tokens.
    engine.submit(1_000_000, 2, spec(Some(9), 1500, 10));
    let events = drain(&mut engine);
    assert!(events.iter().any(|event| matches!(
        event,
        EngineEvent::FirstToken {
            id: 2,
            reused_input_tokens: 1200,
            ..
        }
    )));
    assert_eq!(engine.worker_stats()[0].kv_cache_used_tokens, 1510);
}

#[test]
fn cancelled_requests_do_not_cache_partial_output() {
    let mut engine = Engine::new(config(1), false).unwrap();
    engine.submit(0, 1, spec(Some(9), 1000, 10_000));
    engine.advance_to(50_000);
    engine.cancel(50_000, 1);
    assert_eq!(engine.worker_stats()[0].kv_cache_used_tokens, 1000);
}

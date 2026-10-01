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

//! Discrete-event loop around the production load balancers.
//!
//! Each Stargate owns one real `LoadBalancer` instance and a view of backend
//! stats that is only as fresh as the last Pylon heartbeat it received, plus
//! its own optimistic reservations. The proxy routing loop
//! (`stargate/src/http_proxy/run.rs` and `routing.rs`) is mirrored here: timed
//! waits recheck at most every 25 ms until the routing wait deadline, generic
//! no-choice retries sleep 1-10 ms while the max-wait budget remains, and
//! Pylon queue-mismatch rejections release the reservation and reroute with
//! the backend excluded.

use std::cmp::Reverse;
use std::collections::{BinaryHeap, HashSet};
use std::rc::Rc;
use std::sync::Arc;
use std::time::{Duration, Instant};

use rand::rngs::StdRng;
use rand::{Rng, SeedableRng};
use stargate::load_balancer::{
    LoadBalancer, LoadBalancerDecision, LoadBalancerRequest, create_load_balancer_with_config,
};
use stargate::routing::{RoutedClusterSnapshot, RoutingTargetKey};
use stargate_proto::pb::{InferenceServerStatus, ModelStats};
use stargate_protocol::common::{has_available_engine_slot, queue_time_delta_ms};

use crate::backend::Backend;
use crate::config::{PolicyConfig, SimConfig};
use crate::metrics::{RequestRecord, RunSummary, summarize};
use crate::time::{Micros, micros_from_duration, micros_from_ms, micros_from_secs};
use crate::workload::{PlannedRequest, plan};

const ROUTING_WAIT_RECHECK: Micros = 25_000;
const ROUTING_RETRY_MAX_WAIT: Micros = 60_000_000;
const ROUTING_RETRY_SLEEP_MIN_MS: u64 = 1;
const ROUTING_RETRY_SLEEP_MAX_MS: u64 = 10;

pub struct RunSpec<'a> {
    pub config: &'a SimConfig,
    pub policy: &'a PolicyConfig,
    pub rate_rps: f64,
    pub seed: u64,
}

#[derive(Debug)]
enum Event {
    RouteAttempt(usize),
    BackendReceive {
        request: usize,
        backend: usize,
        expected_queue_ms: Option<u64>,
    },
    MismatchRejected {
        request: usize,
        backend: usize,
        reservation: u64,
    },
    PrefillDone(usize),
    FirstToken(usize),
    Complete(usize),
    ClientTimeout(usize),
    Heartbeat(usize),
    StatsArrive {
        backend: usize,
        stargate: usize,
        stats: Rc<ModelStats>,
    },
}

struct Scheduled {
    at: Micros,
    sequence: u64,
    event: Event,
}

impl PartialEq for Scheduled {
    fn eq(&self, other: &Self) -> bool {
        (self.at, self.sequence) == (other.at, other.sequence)
    }
}

impl Eq for Scheduled {}

impl PartialOrd for Scheduled {
    fn partial_cmp(&self, other: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for Scheduled {
    fn cmp(&self, other: &Self) -> std::cmp::Ordering {
        (self.at, self.sequence).cmp(&(other.at, other.sequence))
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum EngineState {
    NotAdmitted,
    Waiting,
    Prefilling,
    Decoding,
    Finished,
}

struct Reservation {
    id: u64,
    input_tokens: u64,
}

struct StargateView {
    region: usize,
    load_balancer: Arc<dyn LoadBalancer>,
    stats: Vec<Option<Rc<ModelStats>>>,
    reservations: Vec<Vec<Reservation>>,
}

struct RequestState {
    plan: PlannedRequest,
    cache_affinity_key: String,
    excluded: HashSet<String>,
    record: RequestRecord,
    engine: EngineState,
    resolved: bool,
}

struct Simulation<'a> {
    config: &'a SimConfig,
    now: Micros,
    sequence: u64,
    queue: BinaryHeap<Reverse<Scheduled>>,
    backends: Vec<Backend>,
    stargates: Vec<StargateView>,
    requests: Vec<RequestState>,
    target: RoutingTargetKey,
    rng: StdRng,
    next_reservation: u64,
    unresolved: usize,
}

pub fn run(spec: &RunSpec<'_>) -> anyhow::Result<RunSummary> {
    let config = spec.config;
    let algorithm = spec.policy.algorithm_config()?;
    let mut backends = Vec::new();
    let mut stargates = Vec::new();
    let mut stargate_weights = Vec::new();
    for (region_index, region) in config.topology.regions.iter().enumerate() {
        for backend_index in 0..region.backends {
            backends.push(Backend::new(
                format!("{}-backend-{backend_index}", region.name),
                region_index,
                region.backend_speed,
                &config.engine,
                &config.pylon,
            ));
        }
        for _ in 0..region.stargates {
            stargates.push(StargateView {
                region: region_index,
                load_balancer: create_load_balancer_with_config(&algorithm)?,
                stats: Vec::new(),
                reservations: Vec::new(),
            });
            stargate_weights.push(region.traffic_weight / region.stargates as f64);
        }
    }
    let initial_stats: Vec<Rc<ModelStats>> = backends
        .iter()
        .map(|backend| Rc::new(backend.stats(&config.pylon)))
        .collect();
    for stargate in &mut stargates {
        stargate.stats = initial_stats.iter().cloned().map(Some).collect();
        stargate.reservations = backends.iter().map(|_| Vec::new()).collect();
    }

    let workload = plan(
        &config.workload,
        &config.engine,
        &stargate_weights,
        spec.rate_rps,
        spec.seed,
    );
    let requests = workload
        .requests
        .into_iter()
        .map(|plan| RequestState {
            cache_affinity_key: format!("session-{}", plan.session),
            record: RequestRecord::new(
                plan.arrival,
                plan.input_tokens,
                stargates[plan.stargate].region,
            ),
            plan,
            excluded: HashSet::new(),
            engine: EngineState::NotAdmitted,
            resolved: false,
        })
        .collect::<Vec<_>>();

    let mut simulation = Simulation {
        config,
        now: 0,
        sequence: 0,
        queue: BinaryHeap::new(),
        unresolved: requests.len(),
        backends,
        stargates,
        requests,
        target: RoutingTargetKey {
            routing_key: Some(config.stargate.routing_key.clone()),
            model_id: config.stargate.model_id.clone(),
        },
        // Infrastructure randomness is separate from the workload stream so
        // policies that retry differently still see identical demand.
        rng: StdRng::seed_from_u64(spec.seed ^ 0x5eed_0f57_a6a7),
        next_reservation: 0,
    };
    simulation.schedule_initial_events();
    let started = Instant::now();
    simulation.run_to_completion();
    let records = simulation
        .requests
        .into_iter()
        .map(|request| request.record)
        .collect::<Vec<_>>();
    Ok(summarize(
        spec,
        &records,
        workload.measure_start,
        workload.measure_end,
        &simulation
            .backends
            .iter()
            .map(|backend| backend.id.clone())
            .collect::<Vec<_>>(),
        started.elapsed(),
    ))
}

impl Simulation<'_> {
    fn schedule(&mut self, delay: Micros, event: Event) {
        self.sequence += 1;
        self.queue.push(Reverse(Scheduled {
            at: self.now + delay,
            sequence: self.sequence,
            event,
        }));
    }

    fn schedule_initial_events(&mut self) {
        let heartbeat = micros_from_ms(self.config.pylon.heartbeat_ms);
        for backend in 0..self.backends.len() {
            let offset = self.rng.random_range(0..heartbeat.max(1));
            self.schedule(offset, Event::Heartbeat(backend));
        }
        let timeout = micros_from_ms(self.config.client.timeout_ms as f64);
        for request in 0..self.requests.len() {
            let arrival = self.requests[request].plan.arrival;
            self.schedule(arrival, Event::RouteAttempt(request));
            self.schedule(arrival + timeout, Event::ClientTimeout(request));
        }
    }

    fn run_to_completion(&mut self) {
        while self.unresolved > 0 {
            let Some(Reverse(next)) = self.queue.pop() else {
                break;
            };
            self.now = next.at;
            self.handle(next.event);
        }
    }

    fn handle(&mut self, event: Event) {
        match event {
            Event::RouteAttempt(request) => self.route_attempt(request),
            Event::BackendReceive {
                request,
                backend,
                expected_queue_ms,
            } => self.backend_receive(request, backend, expected_queue_ms),
            Event::MismatchRejected {
                request,
                backend,
                reservation,
            } => self.mismatch_rejected(request, backend, reservation),
            Event::PrefillDone(request) => self.prefill_done(request),
            Event::FirstToken(request) => self.first_token(request),
            Event::Complete(request) => self.complete(request),
            Event::ClientTimeout(request) => self.client_timeout(request),
            Event::Heartbeat(backend) => self.heartbeat(backend),
            Event::StatsArrive {
                backend,
                stargate,
                stats,
            } => {
                let view = &mut self.stargates[stargate];
                view.stats[backend] = Some(stats);
                // A processed heartbeat replaces every pending reservation
                // for that backend (`cluster_snapshots.rs`).
                view.reservations[backend].clear();
            }
        }
    }

    fn one_way(&self, region_a: usize, region_b: usize) -> Micros {
        micros_from_ms(self.rtt_ms(region_a, region_b) / 2.0)
    }

    fn rtt_ms(&self, region_a: usize, region_b: usize) -> f64 {
        if region_a == region_b {
            self.config.topology.intra_region_rtt_ms
        } else {
            self.config.topology.rtt_ms[region_a][region_b]
        }
    }

    fn candidates(&self, stargate: usize) -> (Vec<RoutedClusterSnapshot>, Vec<usize>) {
        let view = &self.stargates[stargate];
        let mut candidates = Vec::with_capacity(self.backends.len());
        let mut backend_indices = Vec::with_capacity(self.backends.len());
        let now = Instant::now();
        for (backend_index, backend) in self.backends.iter().enumerate() {
            let Some(base) = &view.stats[backend_index] else {
                continue;
            };
            let mut stats = ModelStats::clone(base);
            for reservation in &view.reservations[backend_index] {
                apply_reservation(&mut stats, reservation.input_tokens);
            }
            candidates.push(RoutedClusterSnapshot {
                cluster_id: backend.id.clone(),
                stats,
                rtt: Duration::from_secs_f64(self.rtt_ms(view.region, backend.region) / 1000.0),
                snapshot_updated_at: now,
                status: InferenceServerStatus::Active,
                active_backend_count: 1,
            });
            backend_indices.push(backend_index);
        }
        (candidates, backend_indices)
    }

    fn route_attempt(&mut self, request: usize) {
        if self.requests[request].resolved {
            return;
        }
        let stargate = self.requests[request].plan.stargate;
        let arrival = self.requests[request].plan.arrival;
        let (candidates, backend_indices) = self.candidates(stargate);
        let state = &self.requests[request];
        let excluded = &state.excluded;
        let eligible = candidates
            .iter()
            .filter(|candidate| !excluded.contains(&candidate.cluster_id))
            .count();
        let decision = if eligible == 0 {
            LoadBalancerDecision::Unavailable
        } else {
            let elapsed = Duration::from_micros(self.now - arrival);
            // Load balancers read elapsed time through `received_at.elapsed()`.
            // Backdating the real clock by the virtual elapsed time makes that
            // read return virtual time, give or take the call's own runtime.
            let received_at = Instant::now()
                .checked_sub(elapsed)
                .expect("virtual elapsed time fits within the process clock");
            let lb_request = LoadBalancerRequest {
                routing_target: &self.target,
                cache_affinity_key: Some(&state.cache_affinity_key),
                input_tokens: Some(state.plan.input_tokens),
                priority: 0,
                received_at,
                request_slo: self.config.client.request_slo_ms.map(Duration::from_millis),
                excluded_cluster_ids: (!excluded.is_empty()).then_some(excluded),
            };
            self.stargates[stargate]
                .load_balancer
                .decide(&lb_request, &candidates)
        };
        self.requests[request].record.route_attempts += 1;

        match decision {
            LoadBalancerDecision::Selected(choice) => {
                let backend = backend_indices[choice.candidate_index];
                let expected_queue_ms =
                    expected_queue_ms(&candidates[choice.candidate_index].stats);
                self.dispatch(request, backend, expected_queue_ms);
            }
            LoadBalancerDecision::Wait(remaining) => {
                let wait_deadline = arrival + self.max_wait().unwrap_or(ROUTING_RETRY_MAX_WAIT);
                let delay = micros_from_duration(remaining)
                    .min(ROUTING_WAIT_RECHECK)
                    .min(wait_deadline.saturating_sub(self.now));
                if delay > 0 {
                    self.schedule(delay, Event::RouteAttempt(request));
                } else {
                    self.no_routing_choice(request, eligible);
                }
            }
            LoadBalancerDecision::Unavailable => self.no_routing_choice(request, eligible),
        }
    }

    fn max_wait(&self) -> Option<Micros> {
        self.config
            .client
            .max_wait_ms
            .map(|wait_ms| micros_from_ms(wait_ms as f64).min(ROUTING_RETRY_MAX_WAIT))
    }

    fn no_routing_choice(&mut self, request: usize, eligible: usize) {
        let arrival = self.requests[request].plan.arrival;
        let remaining = self
            .max_wait()
            .map(|max_wait| (arrival + max_wait).saturating_sub(self.now))
            .unwrap_or_default();
        if eligible > 0 && remaining > 0 {
            let sleep_ms = self
                .rng
                .random_range(ROUTING_RETRY_SLEEP_MIN_MS..ROUTING_RETRY_SLEEP_MAX_MS);
            let delay = micros_from_ms(sleep_ms as f64).min(remaining);
            self.schedule(delay, Event::RouteAttempt(request));
        } else {
            self.resolve(request, |record| record.no_route = true);
        }
    }

    fn dispatch(&mut self, request: usize, backend: usize, expected_queue_ms: Option<u64>) {
        let stargate = self.requests[request].plan.stargate;
        self.next_reservation += 1;
        let reservation = Reservation {
            id: self.next_reservation,
            input_tokens: self.requests[request].plan.input_tokens,
        };
        self.stargates[stargate].reservations[backend].push(reservation);
        let record = &mut self.requests[request].record;
        record.dispatched_at = Some(self.now);
        record.backend = Some(backend);
        record.reservation = Some(self.next_reservation);
        let delay = self.one_way(
            self.stargates[stargate].region,
            self.backends[backend].region,
        );
        self.schedule(
            delay,
            Event::BackendReceive {
                request,
                backend,
                expected_queue_ms,
            },
        );
    }

    fn backend_receive(&mut self, request: usize, backend: usize, expected_queue_ms: Option<u64>) {
        if self.requests[request].resolved {
            return;
        }
        let stargate = self.requests[request].plan.stargate;
        let return_delay = self.one_way(
            self.backends[backend].region,
            self.stargates[stargate].region,
        );
        if self.backends[backend].rejects(
            expected_queue_ms,
            &self.config.pylon,
            &self.config.pylon.queue_mismatch,
        ) {
            let reservation = self.requests[request]
                .record
                .reservation
                .expect("dispatched request has a reservation");
            self.schedule(
                return_delay,
                Event::MismatchRejected {
                    request,
                    backend,
                    reservation,
                },
            );
            return;
        }
        let input_tokens = self.requests[request].plan.input_tokens;
        self.requests[request].record.backend_received_at = Some(self.now);
        if self.backends[backend].accept(request, input_tokens) {
            self.start_prefill(request);
        } else {
            self.requests[request].engine = EngineState::Waiting;
        }
    }

    fn mismatch_rejected(&mut self, request: usize, backend: usize, reservation: u64) {
        let stargate = self.requests[request].plan.stargate;
        self.stargates[stargate].reservations[backend].retain(|pending| pending.id != reservation);
        if self.requests[request].resolved {
            return;
        }
        let record = &mut self.requests[request].record;
        record.mismatch_rejections += 1;
        record.backend = None;
        if record.mismatch_rejections > self.config.stargate.max_request_retries {
            self.resolve(request, |record| record.retries_exhausted = true);
            return;
        }
        // One backend per cluster: the sibling-backend retry finds none and
        // the proxy excludes the cluster before rerouting.
        let cluster_id = self.backends[backend].id.clone();
        self.requests[request].excluded.insert(cluster_id);
        self.schedule(0, Event::RouteAttempt(request));
    }

    fn start_prefill(&mut self, request: usize) {
        let backend_index = self.requests[request]
            .record
            .backend
            .expect("admitted request has a backend");
        let state = &mut self.requests[request];
        let backend = &mut self.backends[backend_index];
        let reused = backend.start_prefill(state.plan.session, state.plan.input_tokens);
        state.record.reused_input_tokens = reused;
        state.record.prefill_started_at = Some(self.now);
        state.engine = EngineState::Prefilling;
        let uncached = state.plan.input_tokens - reused;
        let delay =
            micros_from_secs(uncached as f64 / backend.prefill_tokens_per_s(&self.config.engine));
        self.schedule(delay, Event::PrefillDone(request));
    }

    fn prefill_done(&mut self, request: usize) {
        let state = &mut self.requests[request];
        if state.engine != EngineState::Prefilling {
            return;
        }
        let backend = state
            .record
            .backend
            .expect("prefilling request has a backend");
        self.backends[backend].finish_prefill(state.plan.session, state.plan.input_tokens);
        let delay = micros_from_ms(self.config.engine.ttft_base_ms) + state.plan.ttft_jitter;
        self.schedule(delay, Event::FirstToken(request));
    }

    fn first_token(&mut self, request: usize) {
        let state = &mut self.requests[request];
        if state.engine != EngineState::Prefilling {
            return;
        }
        state.engine = EngineState::Decoding;
        let backend_index = state
            .record
            .backend
            .expect("decoding request has a backend");
        let submitted = state
            .record
            .backend_received_at
            .expect("decoding request reached its backend");
        self.backends[backend_index].first_token(
            state.plan.input_tokens,
            submitted,
            self.now,
            &self.config.pylon,
        );
        let return_delay = self.one_way(
            self.backends[backend_index].region,
            self.stargates[self.requests[request].plan.stargate].region,
        );
        let state = &mut self.requests[request];
        state.record.first_token_at = Some(self.now + return_delay);
        let decode_seconds = state.plan.output_tokens.saturating_sub(1) as f64
            / (state.plan.decode_tokens_per_s * self.backends[backend_index].speed());
        self.schedule(micros_from_secs(decode_seconds), Event::Complete(request));
    }

    fn complete(&mut self, request: usize) {
        let state = &mut self.requests[request];
        if state.engine != EngineState::Decoding {
            return;
        }
        state.engine = EngineState::Finished;
        let backend_index = state
            .record
            .backend
            .expect("completed request has a backend");
        let (stargate, input_tokens) = (state.plan.stargate, state.plan.input_tokens);
        let return_delay = self.one_way(
            self.backends[backend_index].region,
            self.stargates[stargate].region,
        );
        let next = self.backends[backend_index].complete(input_tokens);
        self.requests[request].record.completed_at = Some(self.now + return_delay);
        if !self.requests[request].resolved {
            self.resolve(request, |_| {});
        }
        if let Some(next) = next {
            self.start_prefill(next);
        }
    }

    fn client_timeout(&mut self, request: usize) {
        if self.requests[request].resolved {
            return;
        }
        self.resolve(request, |record| record.timed_out = true);
        // Disconnects propagate to the engine; free the slot the way a dropped
        // MockDynamo stream does. Reservations stay until the next heartbeat.
        let state = &mut self.requests[request];
        let Some(backend_index) = state.record.backend else {
            return;
        };
        let input_tokens = state.plan.input_tokens;
        let next = match state.engine {
            EngineState::NotAdmitted | EngineState::Finished => None,
            EngineState::Waiting => {
                self.backends[backend_index].cancel_waiting(request, input_tokens);
                None
            }
            EngineState::Prefilling => self.backends[backend_index].cancel_prefilling(input_tokens),
            EngineState::Decoding => self.backends[backend_index].complete(input_tokens),
        };
        self.requests[request].engine = EngineState::Finished;
        if let Some(next) = next {
            self.start_prefill(next);
        }
    }

    fn heartbeat(&mut self, backend: usize) {
        let stats = Rc::new(self.backends[backend].stats(&self.config.pylon));
        let relay = micros_from_ms(self.config.topology.stats_relay_delay_ms);
        for stargate in 0..self.stargates.len() {
            let delay = self.one_way(
                self.backends[backend].region,
                self.stargates[stargate].region,
            ) + relay;
            self.schedule(
                delay,
                Event::StatsArrive {
                    backend,
                    stargate,
                    stats: Rc::clone(&stats),
                },
            );
        }
        self.schedule(
            micros_from_ms(self.config.pylon.heartbeat_ms),
            Event::Heartbeat(backend),
        );
    }

    fn resolve(&mut self, request: usize, mark: impl FnOnce(&mut RequestRecord)) {
        let state = &mut self.requests[request];
        debug_assert!(!state.resolved);
        state.resolved = true;
        mark(&mut state.record);
        self.unresolved -= 1;
    }
}

/// Mirrors `apply_pending_cluster_reservations` for a priority-0 request.
fn apply_reservation(stats: &mut ModelStats, input_tokens: u64) {
    stats.queue_size = stats.queue_size.saturating_add(1);
    stats.queued_input_size = stats.queued_input_size.saturating_add(input_tokens);
    stats.num_running_queries = stats.num_running_queries.saturating_add(1);
    stats.total_query_input_size = stats.total_query_input_size.saturating_add(input_tokens);
    if stats.queue_time_estimate_ms_by_priority.is_empty() {
        return;
    }
    match queue_time_delta_ms(input_tokens, stats.last_mean_input_tps) {
        Some(delta_ms) => {
            for estimate in stats.queue_time_estimate_ms_by_priority.values_mut() {
                *estimate = estimate.saturating_add(delta_ms);
            }
        }
        None => stats.queue_time_estimate_ms_by_priority.clear(),
    }
}

/// Mirrors `queue_time_estimate_ms_for_priority` for priority 0.
fn expected_queue_ms(stats: &ModelStats) -> Option<u64> {
    if has_available_engine_slot(stats.num_running_queries, stats.max_engine_concurrency) {
        return Some(0);
    }
    if !stats.queue_time_estimate_ms_by_priority.is_empty() {
        return stats
            .queue_time_estimate_ms_by_priority
            .get(&0)
            .copied()
            .or(Some(0));
    }
    queue_time_delta_ms(stats.queued_input_size, stats.last_mean_input_tps)
}

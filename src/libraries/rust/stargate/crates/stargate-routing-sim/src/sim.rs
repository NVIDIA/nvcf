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
//! no-choice retries sleep 1-9 ms while the max-wait budget remains, and
//! Pylon queue-mismatch rejections release the reservation and reroute with
//! the backend excluded.

use std::cmp::Reverse;
use std::collections::{BinaryHeap, HashSet};
use std::rc::Rc;
use std::sync::Arc;
use std::time::{Duration, Instant};

use mock_engine::{EngineEvent, RequestSpec};
use rand::rngs::StdRng;
use rand::{Rng, SeedableRng};
use stargate::load_balancer::{
    LoadBalancer, LoadBalancerDecision, LoadBalancerRequest, create_load_balancer_with_config,
};
use stargate::routing::{
    RoutedClusterSnapshot, RoutingTargetKey, apply_reservation, queue_time_estimate_ms_for_priority,
};
use stargate_proto::pb::{InferenceServerStatus, ModelStats};

use crate::backend::Backend;
use crate::config::{PolicyConfig, SimConfig};
use crate::metrics::{RequestRecord, RunSummary, summarize};
use crate::time::{Micros, micros_from_duration, micros_from_ms};
use crate::workload::{PlannedRequest, WorkloadPlan, plan};

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
        reservation: u64,
        expected_queue_ms: Option<u64>,
    },
    MismatchRejected {
        request: usize,
        backend: usize,
        reservation: u64,
    },
    /// The backend's engine has progress due now.
    EngineWake(usize),
    /// A growing session's turn starts, or a failed turn is retried.
    NextTurn {
        session: u32,
        turn: u32,
        attempt: u32,
    },
    ClientTimeout(usize),
    Heartbeat(usize),
    /// Coalesced change-driven publish for a backend.
    Publish(usize),
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
    InEngine,
    Finished,
}

struct Reservation {
    id: u64,
    input_tokens: u64,
}

struct StargateView {
    region: usize,
    load_balancer: Arc<dyn LoadBalancer>,
    stats: Vec<Rc<ModelStats>>,
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
    workload: WorkloadPlan,
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
    last_publish: Vec<Micros>,
    publish_pending: Vec<bool>,
}

pub fn run(spec: &RunSpec<'_>) -> anyhow::Result<RunSummary> {
    let config = spec.config;
    let algorithm = spec.policy.algorithm_config()?;
    let mut backends = Vec::new();
    let mut stargates = Vec::new();
    for (region_index, region) in config.topology.regions.iter().enumerate() {
        for backend_index in 0..region.backends {
            backends.push(Backend::new(
                format!("{}-backend-{backend_index}", region.name),
                region_index,
                region.backend_speed,
                config.backend_engine(region, backend_index),
                &config.pylon,
            )?);
        }
        for _ in 0..region.stargates {
            stargates.push(StargateView {
                region: region_index,
                load_balancer: create_load_balancer_with_config(&algorithm)?,
                stats: Vec::new(),
                reservations: Vec::new(),
            });
        }
    }
    let initial_stats: Vec<Rc<ModelStats>> = backends
        .iter()
        .map(|backend| Rc::new(backend.stats()))
        .collect();
    for stargate in &mut stargates {
        stargate.stats = initial_stats.clone();
        stargate.reservations = backends.iter().map(|_| Vec::new()).collect();
    }

    let stargate_weights: Vec<f64> = config
        .stargates()
        .into_iter()
        .map(|(_, weight)| weight)
        .collect();
    let mut workload = plan(
        &config.workload,
        &stargate_weights,
        spec.rate_rps,
        spec.seed,
    );
    let requests = std::mem::take(&mut workload.requests)
        .into_iter()
        .map(|plan| request_state(plan, &stargates))
        .collect::<Vec<_>>();

    let (measure_start, measure_end) = (workload.measure_start, workload.measure_end);
    let backend_count = backends.len();
    let mut simulation = Simulation {
        config,
        workload,
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
        last_publish: vec![0; backend_count],
        publish_pending: vec![false; backend_count],
    };
    simulation.schedule_initial_events();
    let started = Instant::now();
    simulation.run_to_completion();
    debug_assert!(
        simulation.backends.iter().all(Backend::is_idle),
        "every resolved request left Pylon and the engine"
    );
    let records = simulation
        .requests
        .into_iter()
        .map(|request| request.record)
        .collect::<Vec<_>>();
    Ok(summarize(
        spec,
        &records,
        measure_start,
        measure_end,
        &simulation.backends,
        started.elapsed(),
    ))
}

fn request_state(plan: PlannedRequest, stargates: &[StargateView]) -> RequestState {
    RequestState {
        cache_affinity_key: format!("session-{}", plan.session),
        record: RequestRecord {
            arrival: plan.arrival,
            input_tokens: plan.input_tokens,
            stargate_region: stargates[plan.stargate].region,
            attempt: plan.attempt,
            ..RequestRecord::default()
        },
        plan,
        excluded: HashSet::new(),
        engine: EngineState::NotAdmitted,
        resolved: false,
    }
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
        for request in 0..self.requests.len() {
            self.schedule_arrival(request);
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
        debug_assert_eq!(self.unresolved, 0, "every request resolved");
    }

    fn handle(&mut self, event: Event) {
        match event {
            Event::RouteAttempt(request) => self.route_attempt(request),
            Event::BackendReceive {
                request,
                backend,
                reservation,
                expected_queue_ms,
            } => self.backend_receive(request, backend, reservation, expected_queue_ms),
            Event::MismatchRejected {
                request,
                backend,
                reservation,
            } => self.mismatch_rejected(request, backend, reservation),
            Event::EngineWake(backend) => self.engine_wake(backend),
            Event::NextTurn {
                session,
                turn,
                attempt,
            } => self.next_turn(session, turn, attempt),
            Event::ClientTimeout(request) => self.client_timeout(request),
            Event::Heartbeat(backend) => {
                self.publish(backend);
                self.schedule(
                    micros_from_ms(self.config.pylon.heartbeat_ms),
                    Event::Heartbeat(backend),
                );
            }
            Event::Publish(backend) => {
                self.publish_pending[backend] = false;
                self.publish(backend);
            }
            Event::StatsArrive {
                backend,
                stargate,
                stats,
            } => {
                let view = &mut self.stargates[stargate];
                view.stats[backend] = stats;
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

    /// One candidate per backend, in backend order, as `stargate` sees it.
    fn candidates(&self, stargate: usize) -> Vec<RoutedClusterSnapshot> {
        let view = &self.stargates[stargate];
        let now = Instant::now();
        self.backends
            .iter()
            .enumerate()
            .map(|(index, backend)| {
                let mut stats = ModelStats::clone(&view.stats[index]);
                for reservation in &view.reservations[index] {
                    apply_reservation(&mut stats, reservation.input_tokens, 0);
                }
                RoutedClusterSnapshot {
                    cluster_id: backend.id.clone(),
                    stats,
                    rtt: Duration::from_secs_f64(self.rtt_ms(view.region, backend.region) / 1000.0),
                    snapshot_updated_at: now,
                    status: InferenceServerStatus::Active,
                    active_backend_count: 1,
                }
            })
            .collect()
    }

    fn route_attempt(&mut self, request: usize) {
        if self.requests[request].resolved {
            return;
        }
        if self.requests[request].excluded.len() == self.backends.len() {
            // Queue-mismatch reroutes excluded every cluster; the proxy
            // reports this as exhausted retries.
            self.fail(request, |record| record.retries_exhausted = true);
            return;
        }
        let stargate = self.requests[request].plan.stargate;
        let arrival = self.requests[request].plan.arrival;
        let candidates = self.candidates(stargate);
        let state = &self.requests[request];
        let excluded = &state.excluded;
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
            last_cluster: None,
        };
        let decision = self.stargates[stargate]
            .load_balancer
            .decide(&lb_request, &candidates);
        self.requests[request].record.route_attempts += 1;

        match decision {
            LoadBalancerDecision::Selected(choice) => {
                let backend = choice.candidate_index;
                self.requests[request].record.rank_depth = choice.rank_depth;
                let expected_queue_ms =
                    queue_time_estimate_ms_for_priority(&candidates[backend].stats, 0);
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
                    self.no_routing_choice(request);
                }
            }
            LoadBalancerDecision::Unavailable => self.no_routing_choice(request),
        }
    }

    fn max_wait(&self) -> Option<Micros> {
        self.config
            .client
            .max_wait_ms
            .map(|wait_ms| micros_from_ms(wait_ms as f64).min(ROUTING_RETRY_MAX_WAIT))
    }

    fn no_routing_choice(&mut self, request: usize) {
        let arrival = self.requests[request].plan.arrival;
        let remaining = self
            .max_wait()
            .map(|max_wait| (arrival + max_wait).saturating_sub(self.now))
            .unwrap_or_default();
        if remaining > 0 {
            let sleep_ms = self
                .rng
                .random_range(ROUTING_RETRY_SLEEP_MIN_MS..ROUTING_RETRY_SLEEP_MAX_MS);
            let delay = micros_from_ms(sleep_ms as f64).min(remaining);
            self.schedule(delay, Event::RouteAttempt(request));
        } else {
            self.fail(request, |record| record.no_route = true);
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
        let delay = self.one_way(
            self.stargates[stargate].region,
            self.backends[backend].region,
        );
        self.schedule(
            delay,
            Event::BackendReceive {
                request,
                backend,
                reservation: self.next_reservation,
                expected_queue_ms,
            },
        );
    }

    fn backend_receive(
        &mut self,
        request: usize,
        backend: usize,
        reservation: u64,
        expected_queue_ms: Option<u64>,
    ) {
        if self.requests[request].resolved {
            return;
        }
        let stargate = self.requests[request].plan.stargate;
        let return_delay = self.one_way(
            self.backends[backend].region,
            self.stargates[stargate].region,
        );
        if self.backends[backend].rejects(expected_queue_ms, &self.config.pylon.queue_mismatch) {
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
        let plan = &self.requests[request].plan;
        let spec = RequestSpec {
            cache_key: Some(u64::from(plan.session)),
            input_tokens: plan.input_tokens,
            output_tokens: plan.output_tokens,
        };
        self.requests[request].record.backend_received_at = Some(self.now);
        self.requests[request].engine = EngineState::InEngine;
        self.backends[backend].accept(self.now, request, spec);
        self.reschedule_wake(backend);
        self.stats_changed(backend);
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
            self.fail(request, |record| record.retries_exhausted = true);
            return;
        }
        // One backend per cluster: the sibling-backend retry finds none and
        // the proxy excludes the cluster before rerouting.
        let cluster_id = self.backends[backend].id.clone();
        self.requests[request].excluded.insert(cluster_id);
        self.schedule(0, Event::RouteAttempt(request));
    }

    /// Advances a backend's engine to now and applies its events.
    fn engine_wake(&mut self, backend: usize) {
        if self.backends[backend].wake_at != Some(self.now) {
            // Superseded by an earlier wake.
            return;
        }
        self.backends[backend].wake_at = None;
        for event in self.backends[backend].advance_engine(self.now) {
            let request = match event {
                EngineEvent::FirstToken { id, .. }
                | EngineEvent::Token { id, .. }
                | EngineEvent::Completed { id, .. } => id as usize,
            };
            // Events produced before a client cancellation was applied.
            if self.requests[request].engine != EngineState::InEngine {
                continue;
            }
            match event {
                EngineEvent::FirstToken {
                    id,
                    at,
                    reused_input_tokens,
                    ..
                } => self.first_token(backend, id as usize, at, reused_input_tokens),
                EngineEvent::Completed { id, at } => self.complete(backend, id as usize, at),
                EngineEvent::Token { .. } => {}
            }
        }
        self.reschedule_wake(backend);
    }

    /// Schedules the next engine wake if it is earlier than the pending one.
    fn reschedule_wake(&mut self, backend: usize) {
        let Some(next) = self.backends[backend].next_engine_event() else {
            return;
        };
        if self.backends[backend]
            .wake_at
            .is_some_and(|scheduled| scheduled <= next)
        {
            return;
        }
        self.backends[backend].wake_at = Some(next);
        self.schedule(next.saturating_sub(self.now), Event::EngineWake(backend));
    }

    fn first_token(&mut self, backend: usize, request: usize, at: Micros, reused: u64) {
        let state = &mut self.requests[request];
        state.record.reused_input_tokens = reused;
        state.record.backend_first_token_at = Some(at);
        let submitted = state
            .record
            .backend_received_at
            .expect("engine requests reached their backend");
        let (input_tokens, stargate) = (state.plan.input_tokens, state.plan.stargate);
        self.backends[backend].first_token(input_tokens, submitted, at, &self.config.pylon);
        let return_delay = self.one_way(
            self.backends[backend].region,
            self.stargates[stargate].region,
        );
        self.requests[request].record.first_token_at = Some(at + return_delay);
        self.stats_changed(backend);
    }

    fn complete(&mut self, backend: usize, request: usize, at: Micros) {
        let state = &mut self.requests[request];
        state.engine = EngineState::Finished;
        let (input_tokens, stargate) = (state.plan.input_tokens, state.plan.stargate);
        self.backends[backend].complete(input_tokens);
        let return_delay = self.one_way(
            self.backends[backend].region,
            self.stargates[stargate].region,
        );
        self.requests[request].record.completed_at = Some(at + return_delay);
        // A client timeout cancels engine work, so a completing request is
        // still unresolved.
        self.resolve(request, |_| {});
        self.schedule_next_turn(request, at + return_delay);
        self.stats_changed(backend);
    }

    fn schedule_arrival(&mut self, request: usize) {
        let arrival = self.requests[request].plan.arrival;
        let timeout = micros_from_ms(self.config.client.timeout_ms as f64);
        self.schedule(arrival - self.now, Event::RouteAttempt(request));
        self.schedule(arrival + timeout - self.now, Event::ClientTimeout(request));
    }

    /// Queues the session's next turn after a successful response.
    fn schedule_next_turn(&mut self, request: usize, responded_at: Micros) {
        let plan = &self.requests[request].plan;
        let (session, turn) = (plan.session, plan.turn + 1);
        let Some(next) = self
            .workload
            .sessions
            .get(session as usize)
            .and_then(|planned| planned.turns.get(turn as usize))
        else {
            return;
        };
        self.schedule_turn(session, turn, 0, responded_at + next.think_time);
    }

    /// Resolves a failed request and retries its turn for growing sessions.
    fn fail(&mut self, request: usize, mark: impl FnOnce(&mut RequestRecord)) {
        self.resolve(request, mark);
        let Some(growing) = &self.config.workload.growing else {
            return;
        };
        let plan = &self.requests[request].plan;
        let attempt = plan.attempt + 1;
        if attempt >= growing.max_turn_attempts {
            self.requests[request].record.abandoned_session = true;
            return;
        }
        let backoff = micros_from_ms(growing.retry_backoff_s * 1000.0 * f64::from(attempt));
        let (session, turn) = (plan.session, plan.turn);
        self.schedule_turn(session, turn, attempt, self.now + backoff);
    }

    fn schedule_turn(&mut self, session: u32, turn: u32, attempt: u32, start: Micros) {
        if start >= self.workload.measure_end {
            return;
        }
        // A pending turn keeps the run alive; its request inherits the count.
        self.unresolved += 1;
        self.schedule(
            start.saturating_sub(self.now),
            Event::NextTurn {
                session,
                turn,
                attempt,
            },
        );
    }

    fn next_turn(&mut self, session: u32, turn: u32, attempt: u32) {
        let plan = self
            .workload
            .turn_request(session, turn, attempt, self.now)
            .expect("scheduled turns exist in the plan");
        let request = self.requests.len();
        self.requests.push(request_state(plan, &self.stargates));
        self.schedule_arrival(request);
    }

    fn client_timeout(&mut self, request: usize) {
        if self.requests[request].resolved {
            return;
        }
        self.fail(request, |record| record.timed_out = true);
        // Disconnects propagate to the engine; free the slot the way a dropped
        // MockDynamo stream does. Reservations stay until the next heartbeat.
        let state = &mut self.requests[request];
        let Some(backend_index) = state.record.backend else {
            return;
        };
        if state.engine != EngineState::InEngine {
            return;
        }
        state.engine = EngineState::Finished;
        let input_tokens = state.plan.input_tokens;
        let decoding = state.record.backend_first_token_at.is_some();
        self.backends[backend_index].cancel(self.now, request, input_tokens, decoding);
        self.reschedule_wake(backend_index);
        self.stats_changed(backend_index);
    }

    /// Pylon request state changed on `backend`.
    fn stats_changed(&mut self, backend: usize) {
        let Some(coalesce_ms) = self.config.pylon.stats_update_coalesce_ms else {
            return;
        };
        if self.publish_pending[backend] {
            return;
        }
        self.publish_pending[backend] = true;
        let ready_at = self.last_publish[backend] + micros_from_ms(coalesce_ms);
        // A zero delay still runs after the current event, so simultaneous
        // changes share one publish.
        self.schedule(ready_at.saturating_sub(self.now), Event::Publish(backend));
    }

    fn publish(&mut self, backend: usize) {
        self.last_publish[backend] = self.now;
        let stats = Rc::new(self.backends[backend].stats());
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
    }

    fn resolve(&mut self, request: usize, mark: impl FnOnce(&mut RequestRecord)) {
        let state = &mut self.requests[request];
        // A double resolve would wrap `unresolved` and spin forever on heartbeats.
        assert!(!state.resolved, "request {request} resolved twice");
        state.resolved = true;
        mark(&mut state.record);
        self.unresolved -= 1;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config(workload: serde_json::Value) -> SimConfig {
        let config: SimConfig = serde_json::from_value(serde_json::json!({
            "name": "end-to-end test",
            "seeds": [1],
            "topology": {
                "regions": [
                    {"name": "a", "stargates": 2, "backends": 2},
                    {"name": "b", "stargates": 1, "backends": 2, "traffic_weight": 0.5}
                ],
                "rtt_ms": [[0, 40], [40, 0]],
                "intra_region_rtt_ms": 1.0
            },
            "engine": {
                "num_gpu_workers": 1, "max_num_seqs": 4, "max_batched_tokens": 2048,
                "step_fixed_ms": 4.0, "step_decode_ms_per_seq": 0.1,
                "step_prefill_ms_per_token": 0.05, "kv_cache_capacity_tokens": 100000
            },
            "pylon": {
                "heartbeat_ms": 1000.0,
                "stats_update_coalesce_ms": 10.0,
                "input_tps": {"model": "fallback-window", "initial": 20000.0, "window": 8, "duration_floor_ms": 10.0},
                "queue_mismatch": {"enabled": true, "min_delta_ms": 25, "tolerance_factor": 1.25}
            },
            "stargate": {"max_request_retries": 2, "routing_key": "rk", "model_id": "m"},
            "client": {"request_slo_ms": 2000, "max_wait_ms": 2000, "timeout_ms": 3000, "ttft_slo_ms": 2000},
            "workload": workload,
            "policies": [
                {"name": "power-of-n", "load_balancer": {"algorithm": "power-of-n"}},
                {"name": "wait-and-widen", "load_balancer": {
                    "algorithm": "wait-and-widen",
                    "max_queue_time_floor_ms": 500, "max_queue_time_ceil_ms": 500
                }},
                {"name": "pulsar-wait-and-widen", "load_balancer": {
                    "algorithm": "pulsar-wait-and-widen", "seed": "s",
                    "max_queue_time_floor_ms": 500, "max_queue_time_ceil_ms": 500,
                    "cache_affinity_wait_ms": 100
                }}
            ]
        }))
        .expect("test config parses");
        config.validate().expect("test config is valid");
        config
    }

    /// Runs every policy and checks that each measured request has exactly
    /// one outcome.
    fn run_policies(config: &SimConfig, rate_rps: f64) -> Vec<RunSummary> {
        config
            .policies
            .iter()
            .map(|policy| {
                let summary = run(&RunSpec {
                    config,
                    policy,
                    rate_rps,
                    seed: 1,
                })
                .expect("run completes");
                assert!(summary.offered > 0, "{}", policy.name);
                assert_eq!(
                    summary.succeeded
                        + summary.failed_no_route
                        + summary.failed_retries_exhausted
                        + summary.failed_timeout,
                    summary.offered,
                    "{}: every measured request has one outcome",
                    policy.name
                );
                summary
            })
            .collect()
    }

    fn assert_runs_resolve_every_request(config: &SimConfig, rate_rps: f64) {
        for summary in run_policies(config, rate_rps) {
            assert!(summary.goodput_rps > 0.0, "{}", summary.policy);
        }
    }

    #[test]
    fn fixed_sessions_resolve_every_request() {
        let config = config(serde_json::json!({
            "rates_rps": [1.0], "warmup_s": 1.0, "measure_s": 4.0,
            "fixed": {
                "sessions": 20, "input_tokens_min": 500, "input_tokens_max": 4000,
                "output_tokens": 64
            }
        }));
        assert_runs_resolve_every_request(&config, 20.0);
    }

    #[test]
    fn growing_sessions_resolve_with_retries() {
        let config = config(serde_json::json!({
            "rates_rps": [1.0], "warmup_s": 1.0, "measure_s": 4.0,
            "growing": {
                "system_prompt_tokens": 500, "user_tokens_min": 50, "user_tokens_max": 200,
                "output_tokens_min": 16, "output_tokens_max": 64,
                "turns_min": 2, "turns_max": 4, "think_time_mean_s": 0.2,
                "max_context_tokens": 8000, "max_turn_attempts": 2, "retry_backoff_s": 0.1
            }
        }));
        assert_runs_resolve_every_request(&config, 60.0);
    }

    #[test]
    fn short_client_timeouts_cancel_engine_work_and_retry_turns() {
        let mut config = config(serde_json::json!({
            "rates_rps": [1.0], "warmup_s": 1.0, "measure_s": 4.0,
            "growing": {
                "system_prompt_tokens": 500, "user_tokens_min": 50, "user_tokens_max": 200,
                "output_tokens_min": 16, "output_tokens_max": 64,
                "turns_min": 2, "turns_max": 4, "think_time_mean_s": 0.2,
                "max_context_tokens": 8000, "max_turn_attempts": 2, "retry_backoff_s": 0.1
            }
        }));
        config.client.timeout_ms = 250;
        config.client.max_wait_ms = Some(100);
        let summaries = run_policies(&config, 60.0);
        // The debug assertions in `run` check that every cancelled request
        // also left Pylon and the engine.
        for summary in &summaries {
            assert!(summary.failed_timeout > 0, "{}", summary.policy);
            assert!(summary.retried_requests > 0, "{}", summary.policy);
        }
        assert!(
            summaries
                .iter()
                .any(|summary| summary.abandoned_sessions > 0)
        );
    }

    #[test]
    fn affinity_waits_without_max_wait_widen_instead_of_failing() {
        let mut config = config(serde_json::json!({
            "rates_rps": [1.0], "warmup_s": 1.0, "measure_s": 4.0,
            "fixed": {
                "sessions": 20, "input_tokens_min": 500, "input_tokens_max": 4000,
                "output_tokens": 64
            }
        }));
        config.client.max_wait_ms = None;
        config
            .policies
            .retain(|policy| policy.name == "pulsar-wait-and-widen");
        let summary = &run_policies(&config, 20.0)[0];
        assert_eq!(summary.failed_no_route, 0);
        assert!(summary.off_primary_fraction > 0.0);
    }

    #[test]
    fn mismatch_rejections_exclude_the_only_backend_and_exhaust_retries() {
        let mut config = config(serde_json::json!({
            "rates_rps": [1.0], "warmup_s": 1.0, "measure_s": 4.0,
            "fixed": {
                "sessions": 20, "input_tokens_min": 2000, "input_tokens_max": 4000,
                "output_tokens": 64
            }
        }));
        config.topology.regions[0].backends = 1;
        config.topology.regions[1].backends = 0;
        config.policies.retain(|policy| policy.name == "power-of-n");
        config.validate().expect("single-backend config is valid");
        let summary = &run_policies(&config, 60.0)[0];
        assert!(summary.mismatch_rejections > 0);
        assert!(summary.failed_retries_exhausted > 0);
        assert_eq!(summary.failed_no_route, 0);
    }

    #[test]
    fn configs_that_would_hang_or_misroute_are_rejected() {
        let valid = config(serde_json::json!({
            "rates_rps": [1.0], "warmup_s": 0.0, "measure_s": 1.0,
            "fixed": {"sessions": 1, "input_tokens_min": 1, "input_tokens_max": 1, "output_tokens": 1}
        }));
        type Breakage = (&'static str, fn(&mut SimConfig));
        let breakages: [Breakage; 6] = [
            ("heartbeat_ms", |config| config.pylon.heartbeat_ms = 0.0004),
            ("measure_s", |config| config.workload.measure_s = 0.0),
            ("rtt_ms", |config| config.topology.rtt_ms[0][1] = f64::NAN),
            ("rates_rps", |config| config.workload.rates_rps = vec![-1.0]),
            ("traffic_weight", |config| {
                for region in &mut config.topology.regions {
                    region.traffic_weight = 0.0;
                }
            }),
            ("gpu_workers", |config| {
                config.topology.regions[0].gpu_workers = Some(0);
            }),
        ];
        for (field, break_config) in breakages {
            let mut config = valid.clone();
            break_config(&mut config);
            let error = config.validate().expect_err(field);
            assert!(error.to_string().contains(field), "{field}: {error}");
        }
    }

    #[test]
    fn kv_free_token_policies_are_rejected() {
        let mut config = config(serde_json::json!({
            "rates_rps": [1.0], "warmup_s": 0.0, "measure_s": 1.0,
            "fixed": {"sessions": 1, "input_tokens_min": 1, "input_tokens_max": 1, "output_tokens": 1}
        }));
        config.policies = vec![PolicyConfig {
            name: "pulsar-kv".to_string(),
            load_balancer: serde_json::json!({"algorithm": "pulsar", "consider_kv_free_tokens": true}),
        }];
        let error = config.validate().expect_err("KV stats are not simulated");
        assert!(error.to_string().contains("consider_kv_free_tokens"));
    }
}

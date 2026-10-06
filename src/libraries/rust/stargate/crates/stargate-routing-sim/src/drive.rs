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

//! Real-time load driver for a deployed fleet.
//!
//! The driver executes the same workload plan as the simulator, from the same
//! config and seed, against a Stargate HTTP endpoint. Prompts are not sent as
//! text: each request declares its size with `x-input-tokens` and
//! `x-output-tokens`, which Stargate routes on and MockDynamo honors, and its
//! session with `x-cache-affinity-key`.
//!
//! One process drives one region. It sends only the requests whose planned
//! Stargate is in that region, so a fleet run uses one driver per region with
//! a shared start time. Each process writes one JSON line per request.

use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{Context, bail};
use futures::StreamExt;
use serde::{Deserialize, Serialize};
use tokio::io::AsyncWriteExt;
use tokio::sync::mpsc;
use tokio::time::Instant;
use tokio_util::task::TaskTracker;

use crate::config::SimConfig;
use crate::time::{Micros, micros_from_secs};
use crate::workload::{PlannedRequest, WorkloadPlan, plan};

pub struct DriveArgs {
    pub config: SimConfig,
    /// Base URL of the region's Stargate HTTP endpoint, without a path.
    pub endpoint: String,
    pub region: String,
    pub rate_rps: f64,
    pub seed: u64,
    /// Sent as `x-routing-method` to select a per-request algorithm.
    pub routing_method: Option<String>,
    /// Prefixes request IDs and cache keys so runs never share cache entries.
    pub run_label: String,
    /// Shared start time in Unix milliseconds for drivers in all regions.
    pub start_at_unix_ms: u64,
    pub records: PathBuf,
    /// Client token sent as a bearer token.
    pub api_key: Option<String>,
}

/// One request attempt as observed by the client.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct FleetRecord {
    pub run: String,
    pub region: String,
    pub session: u32,
    pub turn: u32,
    pub attempt: u32,
    /// Planned or scheduled start, relative to the run start.
    pub arrival_us: Micros,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub status: Option<u16>,
    /// `x-stargate-error-code`, a transport error, or `timeout`.
    pub error: Option<String>,
    /// Time from arrival to the first streamed chunk.
    pub ttft_us: Option<Micros>,
    /// Time from arrival to the end of the response.
    pub e2e_us: Option<Micros>,
    /// `x-stargate-cluster-id` of the backend that served the request.
    pub cluster_id: Option<String>,
    /// `x-kv-cache-reused-input-tokens` from MockDynamo.
    pub reused_input_tokens: Option<u64>,
    /// The last allowed attempt of a growing-session turn failed.
    pub abandoned_session: bool,
}

impl FleetRecord {
    pub fn succeeded(&self) -> bool {
        self.e2e_us.is_some() && self.error.is_none()
    }
}

struct Driver {
    args: DriveArgs,
    plan: WorkloadPlan,
    stargate_regions: Vec<usize>,
    region_index: usize,
    client: reqwest::Client,
    start: Instant,
    records: mpsc::UnboundedSender<FleetRecord>,
    tracker: TaskTracker,
}

pub async fn drive(args: DriveArgs) -> anyhow::Result<()> {
    let region_index = args
        .config
        .topology
        .regions
        .iter()
        .position(|region| region.name == args.region)
        .with_context(|| format!("region {} is not in the topology", args.region))?;
    let stargates = args.config.stargates();
    let weights: Vec<f64> = stargates.iter().map(|(_, weight)| *weight).collect();
    let plan = plan(&args.config.workload, &weights, args.rate_rps, args.seed);
    let client = reqwest::Client::builder()
        .pool_idle_timeout(Duration::from_secs(90))
        .build()
        .context("building HTTP client")?;

    let file = tokio::fs::File::create(&args.records)
        .await
        .with_context(|| format!("creating {}", args.records.display()))?;
    let (records, mut received) = mpsc::unbounded_channel::<FleetRecord>();
    let writer = tokio::spawn(async move {
        let mut file = tokio::io::BufWriter::new(file);
        while let Some(record) = received.recv().await {
            let mut line = serde_json::to_vec(&record).expect("records serialize");
            line.push(b'\n');
            file.write_all(&line).await?;
        }
        file.flush().await
    });

    let start = wait_for_start(args.start_at_unix_ms).await?;
    let driver = Arc::new(Driver {
        stargate_regions: stargates.iter().map(|(region, _)| *region).collect(),
        region_index,
        client,
        start,
        records,
        tracker: TaskTracker::new(),
        plan,
        args,
    });
    let initial: Vec<PlannedRequest> = driver
        .plan
        .requests
        .iter()
        .filter(|request| driver.owns(request))
        .cloned()
        .collect();
    for request in initial {
        let at = request.arrival;
        driver.spawn_request(request, at);
    }
    driver.tracker.close();
    driver.tracker.wait().await;
    drop(driver);
    writer.await.context("record writer panicked")??;
    Ok(())
}

async fn wait_for_start(start_at_unix_ms: u64) -> anyhow::Result<Instant> {
    let now_ms = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .context("system clock before Unix epoch")?
        .as_millis();
    let start_at = u128::from(start_at_unix_ms);
    if start_at + 1000 < now_ms {
        bail!("start time {start_at_unix_ms} is already in the past");
    }
    let delay = Duration::from_millis(u64::try_from(start_at.saturating_sub(now_ms))?);
    let start = Instant::now() + delay;
    tokio::time::sleep_until(start).await;
    Ok(start)
}

impl Driver {
    fn owns(&self, request: &PlannedRequest) -> bool {
        self.stargate_regions[request.stargate] == self.region_index
    }

    fn now(&self) -> Micros {
        u64::try_from(self.start.elapsed().as_micros()).unwrap_or(Micros::MAX)
    }

    /// Runs `request` at `at` and, for growing sessions, its follow-ups.
    fn spawn_request(self: &Arc<Self>, request: PlannedRequest, at: Micros) {
        if at >= self.plan.measure_end {
            return;
        }
        let driver = Arc::clone(self);
        self.tracker.spawn(async move {
            tokio::time::sleep_until(driver.start + Duration::from_micros(at)).await;
            let mut record = driver.send(&request, at).await;
            let succeeded = record.succeeded();
            let now = driver.now();
            if succeeded {
                driver.records.send(record).ok();
                driver.schedule_next_turn(&request, now);
                return;
            }
            let retry = driver.schedule_retry(&request, now);
            record.abandoned_session = !retry && driver.args.config.workload.growing.is_some();
            driver.records.send(record).ok();
        });
    }

    fn schedule_next_turn(self: &Arc<Self>, request: &PlannedRequest, responded_at: Micros) {
        let turn = request.turn + 1;
        let Some(think) = self
            .plan
            .sessions
            .get(request.session as usize)
            .and_then(|session| session.turns.get(turn as usize))
            .map(|planned| planned.think_time)
        else {
            return;
        };
        let start = responded_at + think;
        if let Some(next) = self.plan.turn_request(request.session, turn, 0, start) {
            self.spawn_request(next, start);
        }
    }

    /// Retries a failed growing-session turn with backoff. Returns false when
    /// the session is abandoned.
    fn schedule_retry(self: &Arc<Self>, request: &PlannedRequest, failed_at: Micros) -> bool {
        let Some(growing) = &self.args.config.workload.growing else {
            return false;
        };
        let attempt = request.attempt + 1;
        if attempt >= growing.max_turn_attempts {
            return false;
        }
        let start = failed_at + micros_from_secs(growing.retry_backoff_s * f64::from(attempt));
        match self
            .plan
            .turn_request(request.session, request.turn, attempt, start)
        {
            Some(retry) => {
                self.spawn_request(retry, start);
                true
            }
            None => false,
        }
    }

    async fn send(&self, request: &PlannedRequest, at: Micros) -> FleetRecord {
        let args = &self.args;
        let config = &args.config;
        let mut record = FleetRecord {
            run: args.run_label.clone(),
            region: args.region.clone(),
            session: request.session,
            turn: request.turn,
            attempt: request.attempt,
            arrival_us: at,
            input_tokens: request.input_tokens,
            output_tokens: request.output_tokens,
            status: None,
            error: None,
            ttft_us: None,
            e2e_us: None,
            cluster_id: None,
            reused_input_tokens: None,
            abandoned_session: false,
        };
        let request_id = format!(
            "{}-s{}-t{}-a{}",
            args.run_label, request.session, request.turn, request.attempt
        );
        let body = serde_json::json!({
            "model": config.stargate.model_id,
            "messages": [{"role": "user", "content": "x"}],
            "stream": true,
            "max_tokens": request.output_tokens,
        });
        let mut builder = self
            .client
            .post(format!("{}/v1/chat/completions", args.endpoint))
            .timeout(Duration::from_millis(config.client.timeout_ms))
            .header("x-request-id", &request_id)
            .header("x-model", &config.stargate.model_id)
            .header("x-routing-key", &config.stargate.routing_key)
            .header("x-input-tokens", request.input_tokens)
            .header("x-output-tokens", request.output_tokens)
            .header(
                "x-cache-affinity-key",
                format!("{}-session-{}", args.run_label, request.session),
            )
            .json(&body);
        if let Some(slo_ms) = config.client.request_slo_ms {
            builder = builder.header("x-request-slo-ms", slo_ms);
        }
        if let Some(wait_ms) = config.client.max_wait_ms {
            builder = builder.header("x-max-wait-ms", wait_ms);
        }
        if let Some(method) = &args.routing_method {
            builder = builder.header("x-routing-method", method);
        }
        if let Some(api_key) = &args.api_key {
            builder = builder.bearer_auth(api_key);
        }

        let response = match builder.send().await {
            Ok(response) => response,
            Err(error) => {
                record.error = Some(transport_error(&error));
                return record;
            }
        };
        let status = response.status();
        record.status = Some(status.as_u16());
        let header = |name: &str| {
            response
                .headers()
                .get(name)
                .and_then(|value| value.to_str().ok())
                .map(str::to_string)
        };
        record.cluster_id = header("x-stargate-cluster-id");
        record.reused_input_tokens =
            header("x-kv-cache-reused-input-tokens").and_then(|value| value.parse().ok());
        if !status.is_success() {
            record.error =
                Some(header("x-stargate-error-code").unwrap_or_else(|| format!("http-{status}")));
            return record;
        }
        let mut body = response.bytes_stream();
        while let Some(chunk) = body.next().await {
            match chunk {
                // MockDynamo sends the role chunk when the first token is ready.
                Ok(_) if record.ttft_us.is_none() => {
                    record.ttft_us = Some(self.now().saturating_sub(at));
                }
                Ok(_) => {}
                Err(error) => {
                    record.error = Some(transport_error(&error));
                    return record;
                }
            }
        }
        record.e2e_us = Some(self.now().saturating_sub(at));
        record
    }
}

fn transport_error(error: &reqwest::Error) -> String {
    if error.is_timeout() {
        "timeout".to_string()
    } else if error.is_connect() {
        "connect".to_string()
    } else {
        "transport".to_string()
    }
}

#[cfg(test)]
mod tests {
    use std::collections::HashSet;
    use std::sync::Mutex;

    use axum::Router;
    use axum::body::Body;
    use axum::http::{HeaderMap, Response, StatusCode};
    use axum::routing::post;

    use super::*;

    /// Two regions with one Stargate each; region "b" gets no traffic.
    fn config() -> SimConfig {
        serde_json::from_value(serde_json::json!({
            "name": "driver test",
            "seeds": [1],
            "topology": {
                "regions": [
                    {"name": "a", "stargates": 1, "backends": 1, "traffic_weight": 1.0},
                    {"name": "b", "stargates": 1, "backends": 1, "traffic_weight": 0.0}
                ],
                "rtt_ms": [[0, 10], [10, 0]],
                "intra_region_rtt_ms": 1.0
            },
            "engine": {
                "num_gpu_workers": 1, "max_num_seqs": 4, "max_batched_tokens": 1000,
                "step_fixed_ms": 1.0, "step_decode_ms_per_seq": 0.1,
                "step_prefill_ms_per_token": 0.01, "kv_cache_capacity_tokens": 100000
            },
            "pylon": {
                "heartbeat_ms": 1000.0,
                "input_tps": {"model": "constant", "mean": 1000.0, "max": 1000.0},
                "queue_mismatch": {"enabled": false, "min_delta_ms": 25, "tolerance_factor": 1.25}
            },
            "stargate": {"max_request_retries": 2, "routing_key": "rk", "model_id": "m"},
            "client": {"request_slo_ms": 1000, "max_wait_ms": 1000, "timeout_ms": 2000, "ttft_slo_ms": 1000},
            "workload": {
                "rates_rps": [20.0], "warmup_s": 0.0, "measure_s": 1.0,
                "growing": {
                    "system_prompt_tokens": 100, "user_tokens_min": 10, "user_tokens_max": 20,
                    "output_tokens_min": 2, "output_tokens_max": 4,
                    "turns_min": 3, "turns_max": 3, "think_time_mean_s": 0.0,
                    "max_context_tokens": 10000, "max_turn_attempts": 2, "retry_backoff_s": 0.01
                }
            },
            "policies": [{"name": "rr", "load_balancer": {"algorithm": "round-robin"}}]
        }))
        .expect("test config parses")
    }

    #[derive(Default)]
    struct Seen {
        request_ids: Mutex<Vec<String>>,
    }

    /// Fails the first attempt of every second turn with 503, otherwise
    /// streams two chunks.
    async fn stargate(seen: Arc<Seen>, headers: HeaderMap) -> Response<Body> {
        let header = |name: &str| headers[name].to_str().unwrap().to_string();
        assert!(header("x-input-tokens").parse::<u64>().unwrap() >= 110);
        assert!(header("x-output-tokens").parse::<u64>().unwrap() >= 2);
        assert!(header("x-cache-affinity-key").starts_with("run-1-session-"));
        assert_eq!(header("x-routing-method"), "round-robin");
        assert_eq!(header("authorization"), "Bearer client-token");
        let request_id = header("x-request-id");
        seen.request_ids.lock().unwrap().push(request_id.clone());
        if request_id.contains("-t1-a0") {
            return Response::builder()
                .status(StatusCode::SERVICE_UNAVAILABLE)
                .header("x-stargate-error-code", "overloaded_error")
                .body(Body::empty())
                .unwrap();
        }
        let chunks = futures::stream::iter([
            Ok::<_, std::convert::Infallible>("data: {}\n\n"),
            Ok("data: [DONE]\n\n"),
        ]);
        Response::builder()
            .header("x-stargate-cluster-id", "backend-a")
            .header("x-kv-cache-reused-input-tokens", "100")
            .body(Body::from_stream(chunks))
            .unwrap()
    }

    #[tokio::test]
    async fn driver_runs_every_owned_turn_and_retries_failures() {
        let seen = Arc::new(Seen::default());
        let app = Router::new().route(
            "/v1/chat/completions",
            post({
                let seen = Arc::clone(&seen);
                move |headers: HeaderMap| stargate(Arc::clone(&seen), headers)
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        let records =
            std::env::temp_dir().join(format!("routing-sim-driver-{}.jsonl", std::process::id()));
        let start_at_unix_ms = u64::try_from(
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_millis(),
        )
        .unwrap()
            + 100;

        drive(DriveArgs {
            config: config(),
            endpoint: format!("http://{address}"),
            region: "a".to_string(),
            rate_rps: 20.0,
            seed: 7,
            routing_method: Some("round-robin".to_string()),
            run_label: "run-1".to_string(),
            start_at_unix_ms,
            records: records.clone(),
            api_key: Some("client-token".to_string()),
        })
        .await
        .unwrap();

        let written: Vec<FleetRecord> = std::fs::read_to_string(&records)
            .unwrap()
            .lines()
            .map(|line| serde_json::from_str(line).unwrap())
            .collect();
        std::fs::remove_file(&records).ok();
        let sessions: HashSet<u32> = written.iter().map(|record| record.session).collect();
        assert!(!sessions.is_empty());
        for session in sessions {
            let mut turns: Vec<&FleetRecord> = written
                .iter()
                .filter(|record| record.session == session)
                .collect();
            turns.sort_by_key(|record| (record.turn, record.attempt));
            let outcomes: Vec<(u32, u32, bool)> = turns
                .iter()
                .map(|record| (record.turn, record.attempt, record.succeeded()))
                .collect();
            // Turns start only after the measured window if their session
            // arrived late, so only check the prefix that ran.
            let expected = [(0, 0, true), (1, 0, false), (1, 1, true), (2, 0, true)];
            assert_eq!(
                outcomes[..],
                expected[..outcomes.len()],
                "session {session}"
            );
            for record in turns.iter().filter(|record| record.succeeded()) {
                assert_eq!(record.cluster_id.as_deref(), Some("backend-a"));
                assert_eq!(record.reused_input_tokens, Some(100));
                assert!(record.ttft_us.unwrap() <= record.e2e_us.unwrap());
            }
            let failed = turns.iter().find(|record| !record.succeeded());
            if let Some(failed) = failed {
                assert_eq!(failed.error.as_deref(), Some("overloaded_error"));
                assert_eq!(failed.status, Some(503));
            }
        }
    }
}

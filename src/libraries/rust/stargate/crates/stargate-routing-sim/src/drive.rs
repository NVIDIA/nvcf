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
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use anyhow::{Context, bail};
use futures::StreamExt;
use reqwest::header::{AUTHORIZATION, HeaderMap, HeaderValue};
use serde::{Deserialize, Serialize};
use sse_core::{MessageEvent, SseDecoder, SseEvent};
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
    /// `x-request-id` sent with the request, unique within the run.
    pub request_id: String,
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
    /// Time from arrival to the first streamed `data:` event.
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
        self.ttft_us.is_some() && self.e2e_us.is_some() && self.error.is_none()
    }
}

struct Driver {
    args: DriveArgs,
    plan: WorkloadPlan,
    client: reqwest::Client,
    /// The endpoint's chat completions URL.
    url: reqwest::Url,
    start: Instant,
    records: mpsc::UnboundedSender<FleetRecord>,
    tracker: TaskTracker,
    /// Numbers requests so every `x-request-id` is unique.
    sent: AtomicU64,
}

pub async fn drive(args: DriveArgs) -> anyhow::Result<()> {
    let region_index = args
        .config
        .topology
        .regions
        .iter()
        .position(|region| region.name == args.region)
        .with_context(|| format!("region {} is not in the topology", args.region))?;
    // Fail at startup rather than recording a whole run of failures.
    if !(args.rate_rps.is_finite() && args.rate_rps > 0.0) {
        bail!("rate must be positive, got {}", args.rate_rps);
    }
    let endpoint = reqwest::Url::parse(&args.endpoint)
        .with_context(|| format!("invalid endpoint {}", args.endpoint))?;
    if !matches!(endpoint.scheme(), "http" | "https") {
        bail!("endpoint {} must use http or https", args.endpoint);
    }
    if endpoint.path() != "/" || endpoint.query().is_some() {
        bail!("endpoint {} must not have a path or query", args.endpoint);
    }
    let url = endpoint.join("v1/chat/completions")?;
    HeaderValue::from_str(&format!("{}-{}", args.run_label, args.region))
        .context("run label and region must be valid header values")?;
    let client = reqwest::Client::builder()
        .default_headers(run_headers(&args)?)
        .timeout(Duration::from_millis(args.config.client.timeout_ms))
        .build()
        .context("building HTTP client")?;
    let stargates = args.config.stargates();
    let weights: Vec<f64> = stargates.iter().map(|(_, weight)| *weight).collect();
    let plan = plan(&args.config.workload, &weights, args.rate_rps, args.seed);
    let stargate_regions: Vec<usize> = stargates.iter().map(|(region, _)| *region).collect();

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
        client,
        url,
        start,
        records,
        tracker: TaskTracker::new(),
        sent: AtomicU64::new(0),
        plan,
        args,
    });
    // Spawn each request at its arrival so pending requests hold no task.
    for request in driver
        .plan
        .requests
        .iter()
        .filter(|request| stargate_regions[request.stargate] == region_index)
    {
        tokio::time::sleep_until(start + Duration::from_micros(request.arrival)).await;
        driver.spawn_request(request.clone());
    }
    driver.tracker.close();
    driver.tracker.wait().await;
    drop(driver);
    writer.await.context("record writer panicked")??;
    Ok(())
}

/// Headers shared by every request of the run. Building them up front rejects
/// invalid values before the run starts instead of failing every request.
fn run_headers(args: &DriveArgs) -> anyhow::Result<HeaderMap> {
    let config = &args.config;
    let mut headers = HeaderMap::new();
    let mut insert = |name: &'static str, value: String| -> anyhow::Result<()> {
        let value =
            HeaderValue::try_from(value).with_context(|| format!("invalid {name} header value"))?;
        headers.insert(name, value);
        Ok(())
    };
    insert("x-model", config.stargate.model_id.clone())?;
    insert("x-routing-key", config.stargate.routing_key.clone())?;
    if let Some(slo_ms) = config.client.request_slo_ms {
        insert("x-request-slo-ms", slo_ms.to_string())?;
    }
    if let Some(wait_ms) = config.client.max_wait_ms {
        insert("x-max-wait-ms", wait_ms.to_string())?;
    }
    if let Some(method) = &args.routing_method {
        insert("x-routing-method", method.clone())?;
    }
    if let Some(api_key) = &args.api_key {
        let mut value = HeaderValue::try_from(format!("Bearer {api_key}"))
            .context("API key must be a valid header value")?;
        value.set_sensitive(true);
        headers.insert(AUTHORIZATION, value);
    }
    Ok(headers)
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
    let now = Instant::now();
    // A start up to one second late keeps the shared start instant, so this
    // region's arrivals stay aligned with the other regions.
    let start = if start_at >= now_ms {
        now + Duration::from_millis(u64::try_from(start_at - now_ms)?)
    } else {
        now.checked_sub(Duration::from_millis(u64::try_from(now_ms - start_at)?))
            .unwrap_or(now)
    };
    tokio::time::sleep_until(start).await;
    Ok(start)
}

impl Driver {
    fn now(&self) -> Micros {
        u64::try_from(self.start.elapsed().as_micros()).unwrap_or(Micros::MAX)
    }

    /// Runs `request` at its arrival and, for growing sessions, its
    /// follow-ups.
    fn spawn_request(self: &Arc<Self>, request: PlannedRequest) {
        if request.arrival >= self.plan.measure_end {
            return;
        }
        let driver = Arc::clone(self);
        self.tracker.spawn(async move {
            tokio::time::sleep_until(driver.start + Duration::from_micros(request.arrival)).await;
            let mut record = driver.send(&request).await;
            let now = driver.now();
            if record.succeeded() {
                driver.schedule_next_turn(&request, now);
            } else {
                driver.fail(&request, &mut record, now);
            }
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
        let next = self
            .plan
            .turn_request(request.session, turn, 0, responded_at + think)
            .expect("the next turn exists in the plan");
        self.spawn_request(next);
    }

    /// Retries a failed growing-session turn with backoff, or marks the
    /// session abandoned after its last allowed attempt.
    fn fail(
        self: &Arc<Self>,
        request: &PlannedRequest,
        record: &mut FleetRecord,
        failed_at: Micros,
    ) {
        let Some(growing) = &self.args.config.workload.growing else {
            return;
        };
        let attempt = request.attempt + 1;
        if attempt >= growing.max_turn_attempts {
            record.abandoned_session = true;
            return;
        }
        let start = failed_at + micros_from_secs(growing.retry_backoff_s * f64::from(attempt));
        let retry = self
            .plan
            .turn_request(request.session, request.turn, attempt, start)
            .expect("a failed turn exists in the plan");
        self.spawn_request(retry);
    }

    async fn send(&self, request: &PlannedRequest) -> FleetRecord {
        let args = &self.args;
        let at = request.arrival;
        // Fixed sessions repeat session, turn, and attempt, so a sequence
        // number keeps IDs unique; Pylon tracks live requests by ID.
        let request_id = format!(
            "{}-{}-{}-s{}-t{}-a{}",
            args.run_label,
            args.region,
            self.sent.fetch_add(1, Ordering::Relaxed),
            request.session,
            request.turn,
            request.attempt
        );
        let mut record = FleetRecord {
            run: args.run_label.clone(),
            region: args.region.clone(),
            request_id: request_id.clone(),
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
        let body = serde_json::json!({
            "model": args.config.stargate.model_id,
            "messages": [{"role": "user", "content": "x"}],
            "stream": true,
            "max_tokens": request.output_tokens,
        });
        let builder = self
            .client
            .post(self.url.clone())
            .header("x-request-id", &request_id)
            .header("x-input-tokens", request.input_tokens)
            .header("x-output-tokens", request.output_tokens)
            .header(
                "x-cache-affinity-key",
                format!("{}-session-{}", args.run_label, request.session),
            )
            .json(&body);

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
            record.error = Some(
                header("x-stargate-error-code")
                    .unwrap_or_else(|| format!("http-{}", status.as_u16())),
            );
            // Read the short error body so the connection returns to the pool.
            let _ = response.bytes().await;
            return record;
        }
        let mut body = response.bytes_stream();
        let mut events = SseDecoder::new();
        let mut done = false;
        while let Some(chunk) = body.next().await {
            let mut chunk = match chunk {
                Ok(chunk) => chunk,
                Err(error) => {
                    record.error = Some(transport_error(&error));
                    return record;
                }
            };
            while let Some(event) = events.next(&mut chunk) {
                let event = match event {
                    Ok(SseEvent::Message(event)) => event,
                    Ok(SseEvent::Retry(_)) => continue,
                    Err(_) => {
                        record.error = Some("invalid-sse".to_string());
                        return record;
                    }
                };
                // MockDynamo sends the role event when the first token is
                // ready.
                record
                    .ttft_us
                    .get_or_insert_with(|| self.now().saturating_sub(at));
                if is_error_event(&event) {
                    record.error = Some("stream-error".to_string());
                    return record;
                }
                done |= event.data.trim() == "[DONE]";
            }
        }
        if done {
            record.e2e_us = Some(self.now().saturating_sub(at));
        } else {
            record.error = Some("incomplete".to_string());
        }
        record
    }
}

/// An SSE `error` event, or a data payload carrying an OpenAI-style `error`
/// object.
fn is_error_event(event: &MessageEvent) -> bool {
    event.event == "error"
        || (event.data.contains("\"error\"")
            && serde_json::from_str::<serde_json::Value>(&event.data)
                .is_ok_and(|value| value.get("error").is_some_and(|error| !error.is_null())))
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

    /// Two regions with one Stargate each and equal traffic.
    fn config() -> SimConfig {
        serde_json::from_value(serde_json::json!({
            "name": "driver test",
            "seeds": [1],
            "topology": {
                "regions": [
                    {"name": "a", "stargates": 1, "backends": 1, "traffic_weight": 1.0},
                    {"name": "b", "stargates": 1, "backends": 1, "traffic_weight": 1.0}
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

    #[tokio::test]
    async fn drive_rejects_invalid_arguments_before_starting() {
        let args = |rate_rps: f64, endpoint: &str, routing_method: &str| DriveArgs {
            config: config(),
            endpoint: endpoint.to_string(),
            region: "a".to_string(),
            rate_rps,
            seed: 1,
            routing_method: Some(routing_method.to_string()),
            run_label: "run-1".to_string(),
            start_at_unix_ms: u64::MAX,
            records: PathBuf::from("/nonexistent/records.jsonl"),
            api_key: None,
        };
        let endpoint = "http://127.0.0.1:1";
        for (rate_rps, endpoint, routing_method, message) in [
            (-1.0, endpoint, "round-robin", "rate"),
            (f64::NAN, endpoint, "round-robin", "rate"),
            (10.0, "router:8000", "round-robin", "http"),
            (10.0, "not a url", "round-robin", "endpoint"),
            (10.0, "http://router:8000/v1", "round-robin", "path"),
            (10.0, endpoint, "round\nrobin", "x-routing-method"),
        ] {
            let error = drive(args(rate_rps, endpoint, routing_method))
                .await
                .expect_err(message);
            assert!(error.to_string().contains(message), "{error}");
        }
    }

    #[derive(Default)]
    struct Seen {
        request_ids: Mutex<Vec<String>>,
    }

    /// Fails turn 1's first attempt with 503 and both attempts of turn 2
    /// in-stream, first with an error event and then by ending before
    /// `[DONE]`. Other requests stream two events.
    async fn stargate(seen: Arc<Seen>, headers: HeaderMap) -> Response<Body> {
        let header = |name: &str| headers[name].to_str().unwrap().to_string();
        assert!(header("x-input-tokens").parse::<u64>().unwrap() >= 110);
        assert!(header("x-output-tokens").parse::<u64>().unwrap() >= 2);
        assert!(header("x-cache-affinity-key").starts_with("run-1-session-"));
        assert_eq!(header("x-routing-method"), "round-robin");
        assert_eq!(header("authorization"), "Bearer client-token");
        let request_id = header("x-request-id");
        assert!(request_id.starts_with("run-1-a-"));
        seen.request_ids.lock().unwrap().push(request_id.clone());
        if request_id.contains("-t1-a0") {
            return Response::builder()
                .status(StatusCode::SERVICE_UNAVAILABLE)
                .header("x-stargate-error-code", "overloaded_error")
                .body(Body::empty())
                .unwrap();
        }
        let last = if request_id.ends_with("-t2-a0") {
            "data: {\"error\":{\"message\":\"engine failed\"}}\n\n"
        } else if request_id.ends_with("-t2-a1") {
            ""
        } else {
            "data: [DONE]\n\n"
        };
        let chunks = futures::stream::iter([
            Ok::<_, std::convert::Infallible>(": keep-alive\n\ndata: {}\n\n"),
            Ok(last),
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
        let config = config();
        let plan = plan(&config.workload, &[1.0, 1.0], 20.0, 7);
        let owned: HashSet<u32> = plan
            .requests
            .iter()
            .filter(|request| request.stargate == 0)
            .map(|request| request.session)
            .collect();
        let sessions: HashSet<u32> = written.iter().map(|record| record.session).collect();
        assert!(!owned.is_empty());
        assert_eq!(
            sessions, owned,
            "the driver sends exactly region a's sessions"
        );
        let request_ids: HashSet<&str> = written
            .iter()
            .map(|record| record.request_id.as_str())
            .collect();
        assert_eq!(request_ids.len(), written.len(), "request IDs are unique");
        assert_eq!(seen.request_ids.lock().unwrap().len(), written.len());
        let mut complete_sessions = 0;
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
            let expected = [
                (0, 0, true),
                (1, 0, false),
                (1, 1, true),
                (2, 0, false),
                (2, 1, false),
            ];
            assert_eq!(
                outcomes[..],
                expected[..outcomes.len()],
                "session {session}"
            );
            if outcomes.len() == expected.len() {
                complete_sessions += 1;
            }
            for record in turns.iter().filter(|record| record.succeeded()) {
                assert_eq!(record.cluster_id.as_deref(), Some("backend-a"));
                assert_eq!(record.reused_input_tokens, Some(100));
                assert!(record.ttft_us.unwrap() <= record.e2e_us.unwrap());
            }
            let failures: Vec<(Option<u16>, Option<&str>, bool)> = turns
                .iter()
                .filter(|record| !record.succeeded())
                .map(|record| {
                    (
                        record.status,
                        record.error.as_deref(),
                        record.abandoned_session,
                    )
                })
                .collect();
            let expected = [
                (Some(503), Some("overloaded_error"), false),
                (Some(200), Some("stream-error"), false),
                (Some(200), Some("incomplete"), true),
            ];
            assert_eq!(
                failures[..],
                expected[..failures.len()],
                "session {session}"
            );
        }
        assert!(
            complete_sessions > 0,
            "some session ran every turn and abandoned the last one"
        );
    }
}

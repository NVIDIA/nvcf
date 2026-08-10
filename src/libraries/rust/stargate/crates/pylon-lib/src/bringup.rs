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

mod calibration;
mod lifecycle;
mod upstream;

pub(crate) use calibration::run_calibration;
pub(crate) use lifecycle::run_bringup_task;
pub use upstream::BringupError;
pub(crate) use upstream::check_upstream_health;

const DEFAULT_ACTIVE_CANARY_INTERVAL: Duration = Duration::from_secs(5);
const DEFAULT_CANARY_TIMEOUT: Duration = Duration::from_secs(5);
const DEFAULT_CANARY_MAX_GENERATION_THRESHOLD: u32 = 237;
const DEFAULT_CALIBRATION_REQUESTS: usize = 5;
const DEFAULT_CALIBRATION_PROMPT_UNITS: usize = 4096;
const DEFAULT_CALIBRATION_MAX_CONCURRENCY: usize = 4;
const DEFAULT_CALIBRATION_TIMEOUT: Duration = Duration::from_secs(30);

#[derive(Debug, Clone)]
pub struct BringupConfig {
    pub enabled: bool,
    pub active_canary_interval: Duration,
    pub canary_timeout: Duration,
    pub canary_max_generation_threshold: u32,
}

#[derive(Debug, Clone)]
pub struct CalibrationConfig {
    pub health_timeout: Duration,
    pub calibration_requests: usize,
    pub calibration_prompt_units: usize,
    pub calibration_max_concurrency: usize,
    pub calibration_timeout: Duration,
}

impl Default for BringupConfig {
    fn default() -> Self {
        Self {
            enabled: true,
            active_canary_interval: DEFAULT_ACTIVE_CANARY_INTERVAL,
            canary_timeout: DEFAULT_CANARY_TIMEOUT,
            canary_max_generation_threshold: DEFAULT_CANARY_MAX_GENERATION_THRESHOLD,
        }
    }
}

impl Default for CalibrationConfig {
    fn default() -> Self {
        Self {
            health_timeout: DEFAULT_CANARY_TIMEOUT,
            calibration_requests: DEFAULT_CALIBRATION_REQUESTS,
            calibration_prompt_units: DEFAULT_CALIBRATION_PROMPT_UNITS,
            calibration_max_concurrency: DEFAULT_CALIBRATION_MAX_CONCURRENCY,
            calibration_timeout: DEFAULT_CALIBRATION_TIMEOUT,
        }
    }
}

#[derive(Debug, Clone)]
pub(crate) struct BringupTaskConfig {
    pub upstream_http_base_url: String,
    pub generation: crate::runtime_state::ModelGeneration,
    pub config: BringupConfig,
}

#[cfg(test)]
mod tests {
    use super::*;
    use super::{calibration::*, lifecycle::*, upstream::*};
    use std::sync::Arc;
    use std::sync::atomic::{AtomicUsize, Ordering};

    use axum::Json;
    use axum::Router;
    use axum::extract::State;
    use axum::http::HeaderMap;
    use axum::response::IntoResponse;
    use axum::routing::{get, post};
    use reqwest::StatusCode;
    use serde_json::Value;
    use stargate_proto::pb::InferenceServerStatus;
    use stargate_protocol::tunnel_contract::HEADER_REQUEST_ID;
    use tokio::net::TcpListener;
    use tokio::sync::{Barrier, Mutex, Notify};
    use tokio_util::sync::CancellationToken;
    use uuid::Uuid;

    use crate::runtime_state::PylonRuntimeState;
    use crate::test_support::TestHttpServer;
    use crate::{StatsCollectorConfig, StatsCollectorHandle, start_stats_collector};

    async fn wait_for_bringup_ready(runtime_state: &PylonRuntimeState, expected: bool) {
        tokio::time::timeout(Duration::from_secs(2), async {
            let mut poll = tokio::time::interval(Duration::from_millis(1));
            loop {
                poll.tick().await;
                if runtime_state.model_bringup_ready("test-model") == Some(expected) {
                    return;
                }
            }
        })
        .await
        .expect("bringup task should publish expected state");
    }

    async fn wait_for_bringup_notification(notification: &Notify, expected_event: &str) {
        if tokio::time::timeout(Duration::from_secs(2), notification.notified())
            .await
            .is_err()
        {
            panic!("bringup task should {expected_event}");
        }
    }

    fn test_runtime_state() -> PylonRuntimeState {
        PylonRuntimeState::new(InferenceServerStatus::Active, &["test-model".into()])
    }

    fn test_generation() -> crate::runtime_state::ModelGeneration {
        crate::runtime_state::ModelGeneration::new("test-model", 0)
    }

    fn test_stats_collector() -> (PylonRuntimeState, StatsCollectorHandle) {
        let config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &["test-model".to_string()],
            config.observation_channel_capacity,
            None,
        );
        let collector = start_stats_collector(config, observations, runtime_state.clone());
        (runtime_state, collector)
    }

    fn test_task_config(
        upstream_http_base_url: String,
        config: BringupConfig,
    ) -> BringupTaskConfig {
        BringupTaskConfig {
            upstream_http_base_url,
            generation: crate::runtime_state::ModelGeneration::new("test-model", 0),
            config,
        }
    }

    #[test]
    fn detects_prompt_too_long_errors() {
        assert!(is_prompt_too_long(
            StatusCode::BAD_REQUEST,
            &Some("Prompt too long for model context length".to_string())
        ));
        assert!(!is_prompt_too_long(
            StatusCode::INTERNAL_SERVER_ERROR,
            &Some("prompt too long".to_string())
        ));
    }

    #[tokio::test]
    async fn wait_or_stop_returns_when_cancelled() {
        let stop = CancellationToken::new();
        stop.cancel();

        assert!(wait_or_stop(&stop, Duration::from_secs(60)).await);
    }

    #[tokio::test]
    async fn canary_request_detects_runaway_generation() {
        let base_url = spawn_test_server(TestServerState {
            completion_tokens: 7,
            ..TestServerState::default()
        })
        .await;
        let client = reqwest::Client::new();
        let error = send_canary_request(
            &client,
            &base_url,
            &test_generation(),
            Duration::from_secs(1),
            7,
        )
        .await
        .expect_err("expected runaway generation");
        assert!(matches!(
            error,
            BringupError::RunawayGeneration { tokens: 7 }
        ));
    }

    #[tokio::test]
    async fn pylon_generated_request_ids_include_kind_uuid_and_monotonic_counter() {
        let request_ids = Arc::new(Mutex::new(Vec::new()));
        let base_url = spawn_test_server(TestServerState {
            request_ids: Some(request_ids.clone()),
            ..TestServerState::default()
        })
        .await;
        let client = reqwest::Client::new();

        send_canary_request(
            &client,
            &base_url,
            &test_generation(),
            Duration::from_secs(1),
            7,
        )
        .await
        .expect("canary request should succeed");
        send_calibration_batch(
            &client,
            &base_url,
            &test_generation(),
            Duration::from_secs(1),
            CalibrationBatch {
                prompt_units: CALIBRATION_PROMPT_UNITS_FLOOR,
                request_count: 2,
                concurrency: 2,
            },
            &test_runtime_state(),
        )
        .await
        .expect("calibration batch should succeed");

        let request_ids = request_ids.lock().await.clone();
        assert_eq!(request_ids.len(), 3);

        let parsed = request_ids
            .iter()
            .map(|request_id| parse_pylon_generated_request_id(request_id))
            .collect::<Vec<_>>();
        assert_eq!(parsed[0].kind, "canary");
        assert!(
            parsed[1..]
                .iter()
                .all(|parsed| parsed.kind == "calibration")
        );
        let request_scope = parsed[0].uuid;
        assert!(
            parsed
                .iter()
                .all(|request_id| request_id.uuid == request_scope)
        );
        assert!(parsed.iter().all(|request_id| request_id.counter > 0));
        assert!(parsed.iter().all(|request_id| request_id.generation == 0));

        let canary_counter = parsed[0].counter;
        let mut calibration_counters = parsed[1..]
            .iter()
            .map(|parsed| parsed.counter)
            .collect::<Vec<_>>();
        calibration_counters.sort_unstable();
        calibration_counters.dedup();
        assert_eq!(calibration_counters.len(), 2);
        assert!(
            calibration_counters
                .iter()
                .all(|counter| *counter > canary_counter)
        );
    }

    #[tokio::test]
    async fn prompt_too_long_clamps_to_the_last_completed_frontier() {
        let prompt_lengths = Arc::new(Mutex::new(Vec::new()));
        let base_url = spawn_test_server(TestServerState {
            prompt_too_long_above: Some(700),
            completions_before_block: Some(Arc::new(AtomicUsize::new(4))),
            prompt_lengths: Some(prompt_lengths.clone()),
            ..TestServerState::default()
        })
        .await;
        let client = reqwest::Client::new();
        let config = CalibrationConfig {
            calibration_requests: 1,
            calibration_prompt_units: 1536,
            calibration_timeout: Duration::from_secs(1),
            ..CalibrationConfig::default()
        };
        let (runtime_state, stats) = test_stats_collector();
        let stats_control = stats.control();

        run_calibration(
            &client,
            &base_url,
            &test_generation(),
            &config,
            &stats_control,
            &runtime_state,
        )
        .await
        .expect("calibration should continue below the rejected prompt frontier");

        let prompt_lengths = prompt_lengths.lock().await;
        let rejected = prompt_lengths
            .iter()
            .rposition(|prompt_units| *prompt_units > 700)
            .expect("the ramp should probe beyond the model context");
        assert!(
            prompt_lengths[rejected + 1..]
                .iter()
                .all(|prompt_units| *prompt_units == 512),
            "after rejection the ramp must remain at the last completed prompt frontier"
        );
        stats.shutdown().await;
    }

    #[tokio::test]
    async fn repeated_prompt_rejection_steps_down_to_the_next_completed_frontier() {
        let prompt_lengths = Arc::new(Mutex::new(Vec::new()));
        let prompt_rejections = Arc::new(Mutex::new(vec![1024, 512]));
        let base_url = spawn_test_server(TestServerState {
            prompt_rejections: Some(prompt_rejections.clone()),
            completions_before_block: Some(Arc::new(AtomicUsize::new(6))),
            prompt_lengths: Some(prompt_lengths.clone()),
            ..TestServerState::default()
        })
        .await;
        let (runtime_state, stats) = test_stats_collector();

        run_calibration(
            &reqwest::Client::new(),
            &base_url,
            &test_generation(),
            &CalibrationConfig {
                calibration_requests: 1,
                calibration_prompt_units: 1024,
                calibration_max_concurrency: 1,
                calibration_timeout: Duration::from_secs(1),
                ..CalibrationConfig::default()
            },
            &stats.control(),
            &runtime_state,
        )
        .await
        .expect("calibration should step down again instead of restarting");

        assert!(prompt_rejections.lock().await.is_empty());
        let prompt_lengths = prompt_lengths.lock().await;
        let second_rejection = prompt_lengths
            .iter()
            .rposition(|prompt_units| *prompt_units == 512)
            .expect("the clamped frontier should be rejected once");
        assert!(
            prompt_lengths[second_rejection + 1..]
                .iter()
                .all(|prompt_units| *prompt_units == CALIBRATION_PROMPT_UNITS_FLOOR),
            "a repeated rejection must continue from the next lower completed prompt"
        );
        stats.shutdown().await;
    }

    #[tokio::test]
    async fn prompt_too_long_at_the_calibration_floor_is_an_error() {
        let base_url = spawn_test_server(TestServerState {
            prompt_too_long_above: Some(CALIBRATION_PROMPT_UNITS_FLOOR - 1),
            ..TestServerState::default()
        })
        .await;
        let (runtime_state, stats) = test_stats_collector();

        let error = run_calibration(
            &reqwest::Client::new(),
            &base_url,
            &test_generation(),
            &CalibrationConfig {
                calibration_requests: 1,
                calibration_prompt_units: 1024,
                calibration_timeout: Duration::from_secs(1),
                ..CalibrationConfig::default()
            },
            &stats.control(),
            &runtime_state,
        )
        .await
        .expect_err("the minimum calibration prompt rejection must fail");

        assert!(matches!(error, BringupError::PromptTooLong));
        stats.shutdown().await;
    }

    #[test]
    fn calibration_ramp_keeps_increasing_load_after_prompt_and_concurrency_caps() {
        let config = CalibrationConfig {
            calibration_requests: 2,
            calibration_prompt_units: 1024,
            calibration_max_concurrency: 3,
            ..CalibrationConfig::default()
        };
        let ramp = calibration_ramp(&config).take(5).collect::<Vec<_>>();

        assert_eq!(
            ramp.iter()
                .map(|step| (step.prompt_units, step.request_count, step.concurrency))
                .collect::<Vec<_>>(),
            vec![
                (256, 2, 1),
                (512, 4, 2),
                (1024, 8, 3),
                (1024, 16, 3),
                (1024, 32, 3),
            ]
        );
    }

    #[tokio::test]
    async fn calibration_batch_sends_requests_concurrently() {
        let in_flight = Arc::new(AtomicUsize::new(0));
        let max_in_flight = Arc::new(AtomicUsize::new(0));
        let base_url = spawn_test_server(TestServerState {
            calibration_barrier: Some(Arc::new(Barrier::new(3))),
            in_flight: Some(in_flight),
            max_in_flight: Some(max_in_flight.clone()),
            ..TestServerState::default()
        })
        .await;
        let client = reqwest::Client::new();

        let outcome = send_calibration_batch(
            &client,
            &base_url,
            &test_generation(),
            Duration::from_secs(1),
            CalibrationBatch {
                prompt_units: 256,
                request_count: 3,
                concurrency: 3,
            },
            &test_runtime_state(),
        )
        .await
        .expect("calibration batch should succeed");

        assert_eq!(outcome, CalibrationStepOutcome::Completed);
        assert_eq!(max_in_flight.load(Ordering::SeqCst), 3);
    }

    #[tokio::test]
    async fn calibration_top_level_usage_does_not_claim_chunk_usage_stats() {
        let base_url = spawn_test_server(TestServerState::default()).await;
        let stats_config = StatsCollectorConfig {
            duration_floor: Duration::ZERO,
            ..StatsCollectorConfig::default()
        };
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &["test-model".to_string()],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let generation = test_generation();

        let outcome = send_calibration_batch(
            &reqwest::Client::new(),
            &base_url,
            &generation,
            Duration::from_secs(1),
            CalibrationBatch {
                prompt_units: 256,
                request_count: 1,
                concurrency: 1,
            },
            &runtime_state,
        )
        .await
        .expect("calibration batch should succeed");

        assert_eq!(outcome, CalibrationStepOutcome::Completed);
        let stats_snapshot = stats
            .control()
            .flush_and_snapshot(&generation)
            .await
            .expect("stats collector should respond")
            .expect("generation should remain live");
        assert!(
            stats_snapshot.output_tps > 0.0,
            "terminal top-level usage should seed output throughput"
        );
        assert!(
            stats_snapshot.max_output_tps > 0.0,
            "terminal top-level usage should seed shared output throughput"
        );
        assert!(
            !stats_snapshot
                .stats_capabilities
                .contains(&"request.output.chunk_usage".to_string())
        );
        assert!(
            !stats_snapshot
                .stats_sources
                .contains(&"chunk_usage".to_string())
        );

        stats.shutdown().await;
    }

    #[tokio::test]
    async fn calibration_keeps_increasing_load_until_a_step_times_out() {
        let request_ids = Arc::new(Mutex::new(Vec::new()));
        let calibration_blocked = Arc::new(Notify::new());
        let base_url = spawn_test_server(TestServerState {
            completions_before_block: Some(Arc::new(AtomicUsize::new(3))),
            calibration_blocked: Some(calibration_blocked.clone()),
            request_ids: Some(request_ids.clone()),
            ..TestServerState::default()
        })
        .await;
        let (runtime_state, stats) = test_stats_collector();
        let stats_control = stats.control();
        let calibration = tokio::spawn(async move {
            run_calibration(
                &reqwest::Client::new(),
                &base_url,
                &test_generation(),
                &CalibrationConfig {
                    calibration_requests: 1,
                    calibration_prompt_units: 1024,
                    calibration_max_concurrency: 1,
                    calibration_timeout: Duration::from_secs(30),
                    ..CalibrationConfig::default()
                },
                &stats_control,
                &runtime_state,
            )
            .await
        });

        wait_for_bringup_notification(
            &calibration_blocked,
            "reach the first saturated calibration step",
        )
        .await;

        let request_count = request_ids.lock().await.len();
        assert!(
            request_count >= 4,
            "the ramp must continue beyond its first finite sweep; observed {request_count} requests"
        );

        tokio::time::pause();
        tokio::time::advance(Duration::from_secs(30)).await;
        calibration
            .await
            .expect("calibration task should not panic")
            .expect("the first load-step timeout should complete calibration");
        tokio::time::resume();
        stats.shutdown().await;
    }

    #[tokio::test]
    async fn recovery_canary_runs_after_initial_health_activation() {
        let canaries = CanarySequence::default();
        let health_requests = Arc::new(AtomicUsize::new(0));
        let base_url = spawn_test_server(TestServerState {
            canaries: Some(canaries.clone()),
            health_requests: Some(health_requests.clone()),
            ..TestServerState::default()
        })
        .await;
        let runtime_state = test_runtime_state();
        runtime_state.set_model_bringup_ready("test-model", true);
        let stop = CancellationToken::new();
        let task = tokio::spawn(run_bringup_task(
            test_task_config(
                base_url.to_string(),
                BringupConfig {
                    active_canary_interval: Duration::from_millis(10),
                    canary_timeout: Duration::from_secs(1),
                    canary_max_generation_threshold: 7,
                    ..BringupConfig::default()
                },
            ),
            runtime_state.clone(),
            stop.clone(),
        ));

        wait_for_bringup_notification(&canaries.started[0], "start the initial canary").await;
        assert_eq!(runtime_state.model_bringup_ready("test-model"), Some(true));
        canaries.release[0].notify_one();

        wait_for_bringup_notification(&canaries.started[1], "start the recovery canary").await;
        assert_eq!(
            health_requests.load(Ordering::SeqCst),
            1,
            "one recovery attempt should perform one health check"
        );
        assert_eq!(runtime_state.model_bringup_ready("test-model"), Some(false));
        canaries.release[1].notify_one();

        wait_for_bringup_ready(&runtime_state, true).await;

        stop.cancel();
        task.await.unwrap();
    }

    #[tokio::test]
    async fn active_bringup_stops_when_cancelled() {
        let base_url = spawn_test_server(TestServerState::default()).await;
        let runtime_state = test_runtime_state();
        runtime_state.set_model_bringup_ready("test-model", true);
        let stop = CancellationToken::new();
        let task = tokio::spawn(run_bringup_task(
            test_task_config(
                base_url.to_string(),
                BringupConfig {
                    active_canary_interval: Duration::from_secs(60),
                    canary_timeout: Duration::from_secs(1),
                    ..BringupConfig::default()
                },
            ),
            runtime_state.clone(),
            stop.clone(),
        ));

        wait_for_bringup_ready(&runtime_state, true).await;

        stop.cancel();
        tokio::time::timeout(Duration::from_secs(1), task)
            .await
            .expect("bringup task should stop when cancelled")
            .expect("bringup task should not panic");
    }

    #[tokio::test]
    async fn bringup_cancellation_interrupts_blocked_health_check() {
        let health_entered = Arc::new(Barrier::new(2));
        let server_health_entered = health_entered.clone();
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let server = tokio::spawn(async move {
            let app = Router::new().route(
                "/health",
                get(move || {
                    let health_entered = server_health_entered.clone();
                    async move {
                        health_entered.wait().await;
                        std::future::pending::<&'static str>().await
                    }
                }),
            );
            axum::serve(listener, app).await.unwrap();
        });
        let runtime_state = test_runtime_state();
        runtime_state.set_model_bringup_ready("test-model", true);
        let stop = CancellationToken::new();
        let task = tokio::spawn(run_bringup_task(
            test_task_config(
                format!("http://{addr}"),
                BringupConfig {
                    active_canary_interval: Duration::from_millis(1),
                    canary_timeout: Duration::from_secs(60),
                    ..BringupConfig::default()
                },
            ),
            runtime_state,
            stop.clone(),
        ));
        health_entered.wait().await;

        stop.cancel();
        let stopped = tokio::time::timeout(Duration::from_secs(1), task).await;
        server.abort();

        stopped
            .expect("bringup cancellation should interrupt blocked health check")
            .expect("bringup task should not panic");
    }

    #[derive(Clone)]
    struct TestServerState {
        completion_tokens: u32,
        prompt_too_long_above: Option<usize>,
        calibration_barrier: Option<Arc<Barrier>>,
        completions_before_block: Option<Arc<AtomicUsize>>,
        calibration_blocked: Option<Arc<Notify>>,
        in_flight: Option<Arc<AtomicUsize>>,
        max_in_flight: Option<Arc<AtomicUsize>>,
        canary_failures_remaining: Option<Arc<AtomicUsize>>,
        canaries: Option<CanarySequence>,
        health_requests: Option<Arc<AtomicUsize>>,
        request_ids: Option<Arc<Mutex<Vec<String>>>>,
        prompt_lengths: Option<Arc<Mutex<Vec<usize>>>>,
        prompt_rejections: Option<Arc<Mutex<Vec<usize>>>>,
    }

    impl Default for TestServerState {
        fn default() -> Self {
            Self {
                completion_tokens: 1,
                prompt_too_long_above: None,
                calibration_barrier: None,
                completions_before_block: None,
                calibration_blocked: None,
                in_flight: None,
                max_in_flight: None,
                canary_failures_remaining: None,
                canaries: None,
                health_requests: None,
                request_ids: None,
                prompt_lengths: None,
                prompt_rejections: None,
            }
        }
    }

    #[derive(Debug)]
    struct ParsedPylonGeneratedRequestId<'a> {
        kind: &'a str,
        uuid: Uuid,
        generation: u64,
        counter: u64,
    }

    fn parse_pylon_generated_request_id(request_id: &str) -> ParsedPylonGeneratedRequestId<'_> {
        let (kind, suffix) = request_id
            .split_once('-')
            .expect("request id should include kind prefix");
        let (scope_and_generation, counter) = suffix
            .rsplit_once('-')
            .expect("request id should end with a counter suffix");
        let (uuid, generation) = scope_and_generation
            .rsplit_once("-g")
            .expect("request id should include generation suffix");
        ParsedPylonGeneratedRequestId {
            kind,
            uuid: Uuid::parse_str(uuid).expect("request id should include a UUID"),
            generation: generation
                .parse()
                .expect("request id should include a decimal generation"),
            counter: counter
                .parse()
                .expect("request id should include a decimal counter"),
        }
    }

    #[derive(Clone, Default)]
    struct CanarySequence {
        requests: Arc<AtomicUsize>,
        started: [Arc<Notify>; 2],
        release: [Arc<Notify>; 2],
    }

    async fn spawn_test_server(state: TestServerState) -> TestHttpServer {
        let state = Arc::new(Mutex::new(state));
        let app = Router::new()
            .route("/health", get(test_health))
            .route("/v1/chat/completions", post(test_chat_completion))
            .with_state(state);
        TestHttpServer::spawn(app).await
    }

    async fn test_health(
        State(state): State<Arc<Mutex<TestServerState>>>,
    ) -> axum::response::Response {
        let state = state.lock().await.clone();
        if let Some(health_requests) = &state.health_requests {
            health_requests.fetch_add(1, Ordering::SeqCst);
        }
        "ok".into_response()
    }

    async fn test_chat_completion(
        State(state): State<Arc<Mutex<TestServerState>>>,
        headers: HeaderMap,
        Json(request): Json<Value>,
    ) -> axum::response::Response {
        let state = state.lock().await.clone();
        if let Some(request_ids) = &state.request_ids
            && let Some(request_id) = headers
                .get(HEADER_REQUEST_ID)
                .and_then(|value| value.to_str().ok())
        {
            request_ids.lock().await.push(request_id.to_string());
        }
        let prompt = request
            .get("messages")
            .and_then(|value| value.as_array())
            .and_then(|messages| messages.first())
            .and_then(|message| message.get("content"))
            .and_then(|value| value.as_str())
            .unwrap_or_default();
        let prompt_len = prompt.len();
        if let Some(prompt_lengths) = &state.prompt_lengths {
            prompt_lengths.lock().await.push(prompt_len);
        }
        let reject_prompt = if let Some(prompt_rejections) = &state.prompt_rejections {
            let mut prompt_rejections = prompt_rejections.lock().await;
            if prompt_rejections.first() == Some(&prompt_len) {
                prompt_rejections.remove(0);
                true
            } else {
                false
            }
        } else {
            false
        };
        if let Some(in_flight) = &state.in_flight {
            let current = in_flight.fetch_add(1, Ordering::SeqCst) + 1;
            if let Some(max_in_flight) = &state.max_in_flight {
                let mut observed = max_in_flight.load(Ordering::SeqCst);
                while current > observed {
                    match max_in_flight.compare_exchange(
                        observed,
                        current,
                        Ordering::SeqCst,
                        Ordering::SeqCst,
                    ) {
                        Ok(_) => break,
                        Err(next_observed) => observed = next_observed,
                    }
                }
            }
        }
        if let Some(barrier) = &state.calibration_barrier {
            barrier.wait().await;
        }
        let should_block = state
            .completions_before_block
            .as_ref()
            .is_some_and(|remaining| {
                remaining
                    .fetch_update(Ordering::SeqCst, Ordering::SeqCst, |value| {
                        value.checked_sub(1)
                    })
                    .is_err()
            });
        if should_block {
            if let Some(calibration_blocked) = &state.calibration_blocked {
                calibration_blocked.notify_one();
            }
            std::future::pending::<()>().await;
        }
        if let Some(in_flight) = &state.in_flight {
            in_flight.fetch_sub(1, Ordering::SeqCst);
        }
        if reject_prompt
            || state
                .prompt_too_long_above
                .is_some_and(|limit| prompt_len > limit)
        {
            return (
                StatusCode::BAD_REQUEST,
                Json(serde_json::json!({
                    "error": {"message": "Prompt too long"}
                })),
            )
                .into_response();
        }

        let mut completion_tokens = state.completion_tokens;
        if prompt == "1+1=" {
            if let Some(canaries) = &state.canaries {
                let request = canaries.requests.fetch_add(1, Ordering::SeqCst);
                if let (Some(started), Some(release)) =
                    (canaries.started.get(request), canaries.release.get(request))
                {
                    started.notify_one();
                    release.notified().await;
                }
                completion_tokens = if request == 0 { 7 } else { 1 };
            } else if state
                .canary_failures_remaining
                .as_ref()
                .is_some_and(|remaining| {
                    remaining
                        .fetch_update(Ordering::SeqCst, Ordering::SeqCst, |value| {
                            (value > 0).then(|| value - 1)
                        })
                        .is_ok()
                })
            {
                completion_tokens = request
                    .get("max_tokens")
                    .and_then(|value| value.as_u64())
                    .and_then(|value| u32::try_from(value).ok())
                    .unwrap_or(completion_tokens);
            }
        }

        Json(serde_json::json!({
            "usage": {"completion_tokens": completion_tokens}
        }))
        .into_response()
    }
}

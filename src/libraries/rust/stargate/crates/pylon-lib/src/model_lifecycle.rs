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

use std::collections::{BTreeSet, HashMap};
use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use stargate_runtime::{OwnedTask, TASK_SHUTDOWN_TIMEOUT};
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;
use url::Url;

use crate::bringup::{
    BringupConfig, BringupError, BringupTaskConfig, CalibrationConfig, check_upstream_health,
    run_bringup_task, run_calibration,
};
use crate::model_discovery::{ModelDiscoveryConfig, ModelDiscoveryError, discover_model_ids};
use crate::runtime_state::{ModelGeneration, PylonRuntimeState};
use crate::stats::{
    CalibrationOutcome, ModelStatsInitialization, PylonMetrics, StatsCollectorControl,
    StatsCollectorHandle,
};

const STATIC_RETRY_INTERVAL: Duration = Duration::from_secs(1);

#[derive(Clone, Debug)]
pub enum ModelSource {
    Static(BTreeSet<String>),
    Discovered(ModelDiscoveryConfig),
}

#[derive(Clone, Debug)]
pub enum ModelInitialization {
    Calibration(CalibrationConfig),
    ConfiguredInputTps { input_tps: f64, pin: bool },
}

#[derive(Clone, Debug)]
pub struct ModelLifecycleConfig {
    pub upstream_http_base_url: String,
    pub source: ModelSource,
    pub initialization: ModelInitialization,
    pub bringup: BringupConfig,
}

#[derive(Debug, thiserror::Error)]
pub enum ModelLifecycleError {
    #[error("model discovery failed: {0}")]
    Discovery(#[from] ModelDiscoveryError),
    #[error("model initialization failed: {0}")]
    Bringup(#[from] BringupError),
    #[error("stats collector stopped during model lifecycle transition")]
    StatsCollectorStopped,
    #[error("model generation transition lost exact ownership")]
    StaleGeneration,
    #[error("model generation counter exhausted")]
    GenerationCounterExhausted,
}

pub struct ModelLifecycleHandle {
    task: OwnedTask,
}

impl ModelLifecycleHandle {
    pub async fn wait_for_exit(&mut self) -> Result<(), tokio::task::JoinError> {
        self.task.wait_for_exit().await
    }

    pub async fn shutdown(self) {
        self.task.shutdown(TASK_SHUTDOWN_TIMEOUT).await;
    }
}

struct LiveModelGeneration {
    generation: ModelGeneration,
    canary: Option<OwnedTask>,
}

struct PendingInitialization {
    generation: ModelGeneration,
    retry_at: Instant,
}

type DiscoveryPollResult = (
    ModelDiscoveryConfig,
    Result<BTreeSet<String>, ModelDiscoveryError>,
);

enum InitializationAttemptOutcome {
    Ready,
    Retired,
    Cancelled,
    Failed(ModelLifecycleError),
}

struct ModelLifecycleSupervisor {
    config: ModelLifecycleConfig,
    runtime_state: PylonRuntimeState,
    stats: StatsCollectorControl,
    metrics: Option<Arc<PylonMetrics>>,
    http_client: reqwest::Client,
    live: HashMap<String, LiveModelGeneration>,
    pending: Vec<PendingInitialization>,
    next_generation: u64,
    next_poll_at: Option<Instant>,
}

pub async fn start_model_lifecycle(
    config: ModelLifecycleConfig,
    runtime_state: PylonRuntimeState,
    stats_collector: &StatsCollectorHandle,
    metrics: Option<Arc<PylonMetrics>>,
) -> Result<ModelLifecycleHandle, ModelLifecycleError> {
    let http_client = reqwest::Client::new();
    Url::parse(&config.upstream_http_base_url).map_err(ModelDiscoveryError::from)?;
    let desired = match &config.source {
        ModelSource::Static(model_ids) => model_ids.clone(),
        ModelSource::Discovered(discovery) => {
            let result = discover_model_ids(
                &http_client,
                &config.upstream_http_base_url,
                discovery.provider,
                discovery.request_timeout,
            )
            .await;
            observe_discovery_poll(metrics.as_deref(), discovery, &result);
            result?
        }
    };
    let next_poll_at = match &config.source {
        ModelSource::Static(_) => None,
        ModelSource::Discovered(discovery) => Some(Instant::now() + discovery.poll_interval),
    };
    let mut supervisor = ModelLifecycleSupervisor {
        config,
        runtime_state,
        stats: stats_collector.control(),
        metrics,
        http_client,
        live: HashMap::new(),
        pending: Vec::new(),
        next_generation: 1,
        next_poll_at,
    };
    supervisor.reconcile(desired).await?;
    while let Some(pending) = supervisor.pop_next_pending() {
        let generation = pending.generation;
        match supervisor.drive_initialization(&generation, None).await? {
            InitializationAttemptOutcome::Ready => {
                if let Err(error) = supervisor.admit(&generation).await {
                    supervisor.shutdown().await;
                    return Err(error);
                }
            }
            InitializationAttemptOutcome::Retired => {}
            InitializationAttemptOutcome::Failed(error) => {
                supervisor.shutdown().await;
                return Err(error);
            }
            InitializationAttemptOutcome::Cancelled => {
                unreachable!("startup initialization has no cancellation token")
            }
        }
    }

    let task = OwnedTask::spawn("model lifecycle supervisor", move |stop| async move {
        if let Err(error) = supervisor.run(stop).await {
            tracing::error!(error = %error, "model lifecycle supervisor failed");
        }
        supervisor.shutdown().await;
    });
    Ok(ModelLifecycleHandle { task })
}

fn observe_discovery_poll(
    metrics: Option<&PylonMetrics>,
    discovery: &ModelDiscoveryConfig,
    result: &Result<BTreeSet<String>, ModelDiscoveryError>,
) {
    if let Some(metrics) = metrics {
        metrics.observe_model_discovery_poll(
            discovery.provider.as_str(),
            if result.is_ok() { "success" } else { "error" },
            result.as_ref().ok().map(BTreeSet::len),
        );
    }
}

impl ModelLifecycleSupervisor {
    async fn run(&mut self, stop: CancellationToken) -> Result<(), ModelLifecycleError> {
        self.start_admitted_canaries(&stop);
        loop {
            if stop.is_cancelled() {
                return Ok(());
            }

            if let Some(pending) = self.pop_ready_pending() {
                self.run_pending(pending, &stop).await?;
                continue;
            }

            let retry_at = self.next_retry_at();
            tokio::select! {
                _ = stop.cancelled() => {
                    return Ok(());
                }
                _ = wait_until(self.next_poll_at) => {
                    let Some(result) = stop.run_until_cancelled(self.poll()).await else {
                        return Ok(());
                    };
                    result?;
                }
                _ = wait_until(retry_at) => {}
            }
        }
    }

    async fn run_pending(
        &mut self,
        pending: PendingInitialization,
        stop: &CancellationToken,
    ) -> Result<(), ModelLifecycleError> {
        let generation = pending.generation;
        match self.drive_initialization(&generation, Some(stop)).await? {
            InitializationAttemptOutcome::Ready => {
                self.admit(&generation).await?;
                self.start_canary(&generation, stop)?;
            }
            InitializationAttemptOutcome::Failed(error) => {
                tracing::warn!(
                    model_id = generation.model_id(),
                    error = %error,
                    "model initialization failed; keeping generation pending"
                );
                self.pending.push(PendingInitialization {
                    generation,
                    retry_at: Instant::now() + self.retry_interval(),
                });
            }
            InitializationAttemptOutcome::Retired | InitializationAttemptOutcome::Cancelled => {}
        }
        Ok(())
    }

    async fn poll(&mut self) -> Result<(), ModelLifecycleError> {
        let poll = self.next_discovery_poll();
        let result = poll.await;
        self.apply_discovery_poll(result).await
    }

    fn next_discovery_poll(&self) -> impl Future<Output = DiscoveryPollResult> + Send + 'static {
        let discovery = match &self.config.source {
            ModelSource::Static(_) => None,
            ModelSource::Discovered(discovery) => Some(discovery.clone()),
        };
        let next_poll_at = self.next_poll_at;
        let http_client = self.http_client.clone();
        let upstream_http_base_url = self.config.upstream_http_base_url.clone();
        async move {
            let Some(discovery) = discovery else {
                return std::future::pending::<DiscoveryPollResult>().await;
            };
            wait_until(next_poll_at).await;
            let result = discover_model_ids(
                &http_client,
                &upstream_http_base_url,
                discovery.provider,
                discovery.request_timeout,
            )
            .await;
            (discovery, result)
        }
    }

    async fn apply_discovery_poll(
        &mut self,
        result: DiscoveryPollResult,
    ) -> Result<(), ModelLifecycleError> {
        match self.record_discovery_poll(result) {
            Some(desired) => self.reconcile(desired).await,
            None => Ok(()),
        }
    }

    fn record_discovery_poll(
        &mut self,
        (discovery, result): DiscoveryPollResult,
    ) -> Option<BTreeSet<String>> {
        observe_discovery_poll(self.metrics.as_deref(), &discovery, &result);
        self.next_poll_at = Some(Instant::now() + discovery.poll_interval);
        match result {
            Ok(desired) => Some(desired),
            Err(error) => {
                tracing::warn!(error = %error, "model discovery poll failed; retaining last-known-good set");
                None
            }
        }
    }

    async fn reconcile(&mut self, desired: BTreeSet<String>) -> Result<(), ModelLifecycleError> {
        let removed = self
            .live
            .values()
            .filter(|model| !desired.contains(model.generation.model_id()))
            .map(|model| model.generation.clone())
            .collect::<BTreeSet<_>>();
        for generation in removed {
            self.retire(&generation).await;
        }

        let added = desired
            .iter()
            .filter(|model_id| !self.live.contains_key(*model_id))
            .cloned()
            .collect::<Vec<_>>();
        for model_id in added {
            let sequence = self.next_generation;
            self.next_generation = self
                .next_generation
                .checked_add(1)
                .ok_or(ModelLifecycleError::GenerationCounterExhausted)?;
            let generation = ModelGeneration::new(model_id.clone(), sequence);
            if !self.runtime_state.begin_generation(generation.clone()) {
                return Err(ModelLifecycleError::StaleGeneration);
            }
            let initialization = match &self.config.initialization {
                ModelInitialization::Calibration(_) => ModelStatsInitialization::Empty,
                ModelInitialization::ConfiguredInputTps { input_tps, pin } => {
                    ModelStatsInitialization::ConfiguredInputTps {
                        input_tps: *input_tps,
                        pin: *pin,
                    }
                }
            };
            if !self
                .stats
                .begin_generation(generation.clone(), initialization)
                .await
                .map_err(|_| ModelLifecycleError::StatsCollectorStopped)?
            {
                let _ = self.runtime_state.retire_generation(&generation);
                return Err(ModelLifecycleError::StaleGeneration);
            }
            self.live.insert(
                model_id.clone(),
                LiveModelGeneration {
                    generation: generation.clone(),
                    canary: None,
                },
            );
            self.pending.push(PendingInitialization {
                generation,
                retry_at: Instant::now(),
            });
        }
        Ok(())
    }

    async fn drive_initialization(
        &mut self,
        generation: &ModelGeneration,
        stop: Option<&CancellationToken>,
    ) -> Result<InitializationAttemptOutcome, ModelLifecycleError> {
        if !self.is_current(generation) {
            return Ok(InitializationAttemptOutcome::Retired);
        }
        let attempt = initialization_attempt(
            self.http_client.clone(),
            self.config.clone(),
            self.stats.clone(),
            self.runtime_state.clone(),
            generation.clone(),
            self.metrics.clone(),
        );
        tokio::pin!(attempt);
        let mut deferred_desired = Vec::new();
        loop {
            let poll = self.next_discovery_poll();
            tokio::pin!(poll);
            tokio::select! {
                biased;
                _ = wait_for_cancellation(stop) => {
                    return Ok(InitializationAttemptOutcome::Cancelled);
                }
                result = &mut poll => {
                    if let Some(desired) = self.record_discovery_poll(result) {
                        if desired.contains(generation.model_id()) {
                            if deferred_desired.last() != Some(&desired) {
                                deferred_desired.push(desired);
                            }
                        } else {
                            self.reconcile(desired).await?;
                            return Ok(InitializationAttemptOutcome::Retired);
                        }
                    }
                }
                result = &mut attempt => {
                    let outcome = match result {
                        Ok(()) => InitializationAttemptOutcome::Ready,
                        Err(error) => InitializationAttemptOutcome::Failed(error),
                    };
                    for desired in deferred_desired {
                        self.reconcile(desired).await?;
                        if !self.is_current(generation) {
                            return Ok(InitializationAttemptOutcome::Retired);
                        }
                    }
                    return Ok(outcome);
                }
            }
        }
    }

    async fn admit(&mut self, generation: &ModelGeneration) -> Result<(), ModelLifecycleError> {
        self.stats
            .flush_and_snapshot(generation)
            .await
            .map_err(|_| ModelLifecycleError::StatsCollectorStopped)?
            .ok_or(ModelLifecycleError::StaleGeneration)?;
        if !self.runtime_state.publish_generation(generation) {
            return Err(ModelLifecycleError::StaleGeneration);
        }
        Ok(())
    }

    fn start_admitted_canaries(&mut self, stop: &CancellationToken) {
        let generations = self
            .live
            .values()
            .filter(|model| {
                model.canary.is_none()
                    && self
                        .runtime_state
                        .generation_is_published(&model.generation)
            })
            .map(|model| model.generation.clone())
            .collect::<Vec<_>>();
        for generation in generations {
            self.start_canary(&generation, stop)
                .expect("published generation should remain live before supervisor starts");
        }
    }

    fn start_canary(
        &mut self,
        generation: &ModelGeneration,
        stop: &CancellationToken,
    ) -> Result<(), ModelLifecycleError> {
        let canary = self.spawn_canary(generation, stop);
        let model = self
            .live
            .get_mut(generation.model_id())
            .filter(|model| model.generation == *generation)
            .ok_or(ModelLifecycleError::StaleGeneration)?;
        model.canary = canary;
        Ok(())
    }

    fn spawn_canary(
        &self,
        generation: &ModelGeneration,
        stop: &CancellationToken,
    ) -> Option<OwnedTask> {
        self.config.bringup.enabled.then(|| {
            let task_config = BringupTaskConfig {
                upstream_http_base_url: self.config.upstream_http_base_url.clone(),
                generation: generation.clone(),
                config: self.config.bringup.clone(),
            };
            let runtime_state = self.runtime_state.clone();
            OwnedTask::spawn_child("model canary", stop, move |model_stop| {
                run_bringup_task(task_config, runtime_state, model_stop)
            })
        })
    }

    async fn retire(&mut self, generation: &ModelGeneration) {
        if !self.is_current(generation) {
            return;
        }
        let model = self
            .live
            .remove(generation.model_id())
            .expect("current generation should still be live");
        self.pending
            .retain(|pending| pending.generation != *generation);
        if let Some(canary) = model.canary {
            canary.shutdown(TASK_SHUTDOWN_TIMEOUT).await;
        }
        let retired_stats = self.runtime_state.retire_generation(generation);
        let _ = self.stats.retire_generation(generation).await;
        if let Some(metrics) = self.metrics.as_deref() {
            metrics.remove_model_gauges(generation.model_id(), retired_stats.as_ref());
        }
    }

    async fn shutdown(&mut self) {
        let generations = self
            .live
            .values()
            .map(|model| model.generation.clone())
            .collect::<Vec<_>>();
        for generation in generations {
            self.retire(&generation).await;
        }
    }

    fn is_current(&self, generation: &ModelGeneration) -> bool {
        self.live
            .get(generation.model_id())
            .is_some_and(|model| model.generation == *generation)
    }

    fn retry_interval(&self) -> Duration {
        match &self.config.source {
            ModelSource::Static(_) => STATIC_RETRY_INTERVAL,
            ModelSource::Discovered(discovery) => discovery.poll_interval,
        }
    }

    fn pop_next_pending(&mut self) -> Option<PendingInitialization> {
        let index = earliest_pending_index(&self.pending)?;
        Some(self.pending.remove(index))
    }

    fn pop_ready_pending(&mut self) -> Option<PendingInitialization> {
        let index = earliest_pending_index(&self.pending)?;
        (self.pending[index].retry_at <= Instant::now()).then(|| self.pending.remove(index))
    }

    fn next_retry_at(&self) -> Option<Instant> {
        self.pending.iter().map(|pending| pending.retry_at).min()
    }
}

struct CalibrationAttemptTimer {
    metrics: Option<Arc<PylonMetrics>>,
    model_id: String,
    started_at: Instant,
}

impl CalibrationAttemptTimer {
    fn start(metrics: Option<Arc<PylonMetrics>>, model_id: &str) -> Self {
        Self {
            metrics,
            model_id: model_id.to_string(),
            started_at: Instant::now(),
        }
    }

    fn finish(&mut self, result: &Result<(), BringupError>) {
        self.record(if result.is_ok() {
            CalibrationOutcome::Completed
        } else {
            CalibrationOutcome::Error
        });
    }

    fn record(&mut self, outcome: CalibrationOutcome) {
        let Some(metrics) = self.metrics.take() else {
            return;
        };
        metrics.observe_model_calibration_duration(
            &self.model_id,
            self.started_at.elapsed(),
            outcome,
        );
    }
}

impl Drop for CalibrationAttemptTimer {
    fn drop(&mut self) {
        self.record(CalibrationOutcome::Cancelled);
    }
}

async fn initialization_attempt(
    http_client: reqwest::Client,
    config: ModelLifecycleConfig,
    stats: StatsCollectorControl,
    runtime_state: PylonRuntimeState,
    generation: ModelGeneration,
    metrics: Option<Arc<PylonMetrics>>,
) -> Result<(), ModelLifecycleError> {
    let health_timeout = match &config.initialization {
        ModelInitialization::Calibration(calibration) => Some(calibration.health_timeout),
        ModelInitialization::ConfiguredInputTps { .. } if config.bringup.enabled => {
            Some(config.bringup.canary_timeout)
        }
        ModelInitialization::ConfiguredInputTps { .. } => None,
    };
    if let Some(health_timeout) = health_timeout
        && !check_upstream_health(&http_client, &config.upstream_http_base_url, health_timeout)
            .await
    {
        return Err(BringupError::UnhealthyUpstream.into());
    }
    if let ModelInitialization::Calibration(calibration) = &config.initialization {
        let mut timer = CalibrationAttemptTimer::start(metrics, generation.model_id());
        let result = run_calibration(
            &http_client,
            &config.upstream_http_base_url,
            &generation,
            calibration,
            &stats,
            &runtime_state,
        )
        .await;
        timer.finish(&result);
        result?;
    }
    Ok(())
}

async fn wait_until(deadline: Option<Instant>) {
    match deadline {
        Some(deadline) => tokio::time::sleep_until(deadline).await,
        None => std::future::pending().await,
    }
}

async fn wait_for_cancellation(stop: Option<&CancellationToken>) {
    match stop {
        Some(stop) => stop.cancelled().await,
        None => std::future::pending().await,
    }
}

fn earliest_pending_index(pending: &[PendingInitialization]) -> Option<usize> {
    pending
        .iter()
        .enumerate()
        .min_by_key(|(_, pending)| pending.retry_at)
        .map(|(index, _)| index)
}

#[cfg(test)]
mod tests {
    use std::collections::{BTreeSet, HashMap};
    use std::sync::Arc;
    use std::sync::Mutex as StdMutex;
    use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
    use std::time::Duration;

    use axum::extract::State;
    use axum::http::{HeaderMap, StatusCode};
    use axum::response::{IntoResponse, Response};
    use axum::routing::{get, post};
    use axum::{Json, Router};
    use serde_json::{Value, json};
    use stargate_proto::pb::InferenceServerStatus;
    use stargate_protocol::tunnel_contract::HEADER_REQUEST_ID;
    use stargate_runtime::OwnedTask;
    use tokio::sync::{Notify, RwLock, mpsc};
    use tokio_util::sync::CancellationToken;

    use super::{
        LiveModelGeneration, ModelInitialization, ModelLifecycleConfig, ModelLifecycleSupervisor,
        ModelSource, start_model_lifecycle,
    };
    use crate::generated_request_id::generated_request_generation;
    use crate::runtime_state::ModelGeneration;
    use crate::test_support::TestHttpServer;
    use crate::{
        BringupConfig, CalibrationConfig, ModelDiscoveryConfig, ModelDiscoveryProvider,
        PylonMetrics, PylonRuntimeState, StatsCollectorConfig, start_stats_collector,
    };

    #[derive(Clone)]
    enum CompletionBehavior {
        Pending,
        SaturateAfter {
            completed_requests_per_generation: usize,
        },
        FailModel {
            model_id: &'static str,
            failing: Arc<AtomicBool>,
        },
    }

    #[derive(Clone)]
    struct TestUpstreamState {
        models: Arc<RwLock<Vec<String>>>,
        discovery_polls: Arc<AtomicUsize>,
        discovery_polled: Arc<Notify>,
        discovery_failing: Arc<AtomicBool>,
        discovery_blocked: Arc<AtomicBool>,
        calibrations: mpsc::UnboundedSender<ModelGeneration>,
        completed_requests: Arc<StdMutex<HashMap<ModelGeneration, usize>>>,
        completion_behavior: CompletionBehavior,
    }

    struct TestUpstream {
        base_url: String,
        models: Arc<RwLock<Vec<String>>>,
        discovery_polls: Arc<AtomicUsize>,
        discovery_polled: Arc<Notify>,
        discovery_failing: Arc<AtomicBool>,
        discovery_blocked: Arc<AtomicBool>,
        calibrations: mpsc::UnboundedReceiver<ModelGeneration>,
        server: TestHttpServer,
    }

    impl TestUpstream {
        async fn spawn(initial_models: &[&str], completion_behavior: CompletionBehavior) -> Self {
            let models = Arc::new(RwLock::new(
                initial_models
                    .iter()
                    .map(|model_id| (*model_id).to_string())
                    .collect(),
            ));
            let discovery_polls = Arc::new(AtomicUsize::new(0));
            let discovery_polled = Arc::new(Notify::new());
            let discovery_failing = Arc::new(AtomicBool::new(false));
            let discovery_blocked = Arc::new(AtomicBool::new(false));
            let (calibrations, calibration_rx) = mpsc::unbounded_channel();
            let state = TestUpstreamState {
                models: models.clone(),
                discovery_polls: discovery_polls.clone(),
                discovery_polled: discovery_polled.clone(),
                discovery_failing: discovery_failing.clone(),
                discovery_blocked: discovery_blocked.clone(),
                calibrations,
                completed_requests: Arc::new(StdMutex::new(HashMap::new())),
                completion_behavior,
            };
            let server = TestHttpServer::spawn(
                Router::new()
                    .route("/v1/models", get(test_models))
                    .route("/health", get(|| async { "ok" }))
                    .route("/v1/chat/completions", post(test_completion))
                    .with_state(state),
            )
            .await;
            Self {
                base_url: server.to_string(),
                models,
                discovery_polls,
                discovery_polled,
                discovery_failing,
                discovery_blocked,
                calibrations: calibration_rx,
                server,
            }
        }

        async fn set_models(&self, model_ids: &[&str]) {
            *self.models.write().await = model_ids
                .iter()
                .map(|model_id| (*model_id).to_string())
                .collect();
        }

        fn set_discovery_failing(&self, failing: bool) {
            self.discovery_failing.store(failing, Ordering::SeqCst);
        }

        fn set_discovery_blocked(&self, blocked: bool) {
            self.discovery_blocked.store(blocked, Ordering::SeqCst);
        }

        async fn next_calibration(&mut self) -> ModelGeneration {
            tokio::time::timeout(Duration::from_secs(2), self.calibrations.recv())
                .await
                .expect("calibration request should arrive")
                .expect("calibration request channel should remain open")
        }

        async fn shutdown(self) {
            self.server.shutdown().await;
        }
    }

    #[derive(Clone)]
    struct BlockingKvCacheState {
        blocked: Arc<AtomicBool>,
        blocked_polls: Arc<AtomicUsize>,
        released: Arc<Notify>,
    }

    struct BlockingKvCacheServer {
        server: TestHttpServer,
        blocked: Arc<AtomicBool>,
        blocked_polls: Arc<AtomicUsize>,
        released: Arc<Notify>,
    }

    impl BlockingKvCacheServer {
        async fn spawn() -> Self {
            let blocked = Arc::new(AtomicBool::new(false));
            let blocked_polls = Arc::new(AtomicUsize::new(0));
            let released = Arc::new(Notify::new());
            let state = BlockingKvCacheState {
                blocked: blocked.clone(),
                blocked_polls: blocked_polls.clone(),
                released: released.clone(),
            };
            let server = TestHttpServer::spawn(
                Router::new()
                    .route("/kv-cache", get(test_kv_cache))
                    .with_state(state),
            )
            .await;
            Self {
                server,
                blocked,
                blocked_polls,
                released,
            }
        }

        fn url(&self) -> String {
            format!("{}/kv-cache", self.server.as_str())
        }

        fn block(&self) {
            self.blocked.store(true, Ordering::SeqCst);
        }

        fn unblock(&self) {
            self.blocked.store(false, Ordering::SeqCst);
            self.released.notify_waiters();
        }

        fn blocked_poll_count(&self) -> usize {
            self.blocked_polls.load(Ordering::SeqCst)
        }

        async fn wait_for_blocked_poll_after(&self, count: usize) {
            wait_for("KV-cache stats poll should block", || {
                self.blocked_polls.load(Ordering::SeqCst) > count
            })
            .await;
        }

        async fn shutdown(self) {
            self.server.shutdown().await;
        }
    }

    async fn test_models(State(state): State<TestUpstreamState>) -> Response {
        state.discovery_polls.fetch_add(1, Ordering::SeqCst);
        state.discovery_polled.notify_one();
        if state.discovery_blocked.load(Ordering::SeqCst) {
            return std::future::pending().await;
        }
        if state.discovery_failing.load(Ordering::SeqCst) {
            return StatusCode::SERVICE_UNAVAILABLE.into_response();
        }
        let data = state
            .models
            .read()
            .await
            .iter()
            .map(|model_id| json!({"id": model_id}))
            .collect::<Vec<_>>();
        Json(json!({"data": data})).into_response()
    }

    async fn test_completion(
        State(state): State<TestUpstreamState>,
        headers: HeaderMap,
        Json(request): Json<Value>,
    ) -> Response {
        let model_id = request["model"].as_str().unwrap_or_default();
        let generation = headers
            .get(HEADER_REQUEST_ID)
            .and_then(|value| value.to_str().ok())
            .and_then(|request_id| generated_request_generation(request_id, model_id))
            .expect("calibration request should identify its exact generation");
        state
            .calibrations
            .send(generation.clone())
            .expect("test should still observe calibration traffic");
        match state.completion_behavior {
            CompletionBehavior::Pending => std::future::pending().await,
            CompletionBehavior::SaturateAfter {
                completed_requests_per_generation,
            } => {
                let should_complete = {
                    let mut completed = state
                        .completed_requests
                        .lock()
                        .expect("completed request counts should not be poisoned");
                    let completed = completed.entry(generation).or_default();
                    if *completed < completed_requests_per_generation {
                        *completed += 1;
                        true
                    } else {
                        false
                    }
                };
                if should_complete {
                    Json(json!({"usage": {"completion_tokens": 1}})).into_response()
                } else {
                    std::future::pending().await
                }
            }
            CompletionBehavior::FailModel {
                model_id: failed_model,
                ref failing,
            } if model_id == failed_model && failing.load(Ordering::SeqCst) => {
                StatusCode::SERVICE_UNAVAILABLE.into_response()
            }
            CompletionBehavior::FailModel { .. } => std::future::pending().await,
        }
    }

    async fn test_kv_cache(State(state): State<BlockingKvCacheState>) -> Response {
        if state.blocked.load(Ordering::SeqCst) {
            state.blocked_polls.fetch_add(1, Ordering::SeqCst);
            loop {
                let released = state.released.notified();
                if !state.blocked.load(Ordering::SeqCst) {
                    break;
                }
                released.await;
            }
        }
        Json(json!({
            "model": "model-a",
            "kv_cache_capacity_tokens": 1000,
            "kv_cache_used_tokens": 400,
            "kv_cache_free_tokens": 600
        }))
        .into_response()
    }

    fn discovery_config(base_url: &str) -> ModelLifecycleConfig {
        ModelLifecycleConfig {
            upstream_http_base_url: base_url.to_string(),
            source: ModelSource::Discovered(ModelDiscoveryConfig {
                provider: ModelDiscoveryProvider::Dynamo,
                poll_interval: Duration::from_millis(10),
                request_timeout: Duration::from_secs(1),
            }),
            initialization: ModelInitialization::ConfiguredInputTps {
                input_tps: 123.0,
                pin: false,
            },
            bringup: BringupConfig {
                enabled: false,
                ..BringupConfig::default()
            },
        }
    }

    fn calibration_discovery_config(
        base_url: &str,
        calibration_timeout: Duration,
    ) -> ModelLifecycleConfig {
        ModelLifecycleConfig {
            initialization: ModelInitialization::Calibration(CalibrationConfig {
                health_timeout: Duration::from_secs(1),
                calibration_requests: 1,
                calibration_prompt_units: 256,
                calibration_max_concurrency: 1,
                calibration_timeout,
            }),
            ..discovery_config(base_url)
        }
    }

    async fn wait_for(description: &str, mut reached: impl FnMut() -> bool) {
        tokio::time::timeout(Duration::from_secs(2), async {
            let mut poll = tokio::time::interval(Duration::from_millis(1));
            loop {
                poll.tick().await;
                if reached() {
                    return;
                }
            }
        })
        .await
        .expect(description);
    }

    async fn wait_for_model_ids(runtime_state: &PylonRuntimeState, expected: &[&str]) {
        let expected = expected
            .iter()
            .map(|model_id| (*model_id).to_string())
            .collect::<Vec<_>>();
        wait_for("advertised model set should converge", || {
            runtime_state.advertised_model_ids() == expected
        })
        .await;
    }

    async fn wait_for_generation_retirement(runtime_state: &PylonRuntimeState, model_id: &str) {
        wait_for("model generation should retire", || {
            runtime_state.current_generation(model_id).is_none()
        })
        .await;
    }

    async fn wait_for_discovery_polls(counter: &AtomicUsize, expected: usize) {
        wait_for("model discovery should keep polling", || {
            counter.load(Ordering::SeqCst) >= expected
        })
        .await;
    }

    async fn wait_for_calibration_count(
        metrics: &PylonMetrics,
        model_id: &str,
        outcome: &str,
        minimum: u64,
    ) {
        let prefix = format!(
            "pylon_model_calibration_duration_ms_count{{model=\"{model_id}\",outcome=\"{outcome}\"}} "
        );
        wait_for("calibration metric should reach expected count", || {
            let body = metrics.gather_text().expect("metrics should encode");
            let count = body.lines().find_map(|line| {
                line.strip_prefix(&prefix)
                    .and_then(|value| value.parse::<u64>().ok())
            });
            count.is_some_and(|count| count >= minimum)
        })
        .await;
    }

    fn calibration_duration_sum_ms(metrics: &PylonMetrics, model_id: &str, outcome: &str) -> f64 {
        let prefix = format!(
            "pylon_model_calibration_duration_ms_sum{{model=\"{model_id}\",outcome=\"{outcome}\"}} "
        );
        metrics
            .gather_text()
            .expect("metrics should encode")
            .lines()
            .find_map(|line| {
                line.strip_prefix(&prefix)
                    .and_then(|value| value.parse::<f64>().ok())
            })
            .expect("calibration duration sum should exist")
    }

    #[tokio::test]
    async fn configured_models_publish_only_after_exact_stats_initialization() {
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());

        let lifecycle = start_model_lifecycle(
            ModelLifecycleConfig {
                upstream_http_base_url: "http://127.0.0.1:1".to_string(),
                source: ModelSource::Static(BTreeSet::from([
                    "model-a".to_string(),
                    "model-b".to_string(),
                ])),
                initialization: ModelInitialization::ConfiguredInputTps {
                    input_tps: 123.0,
                    pin: false,
                },
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
            },
            runtime_state.clone(),
            &stats,
            None,
        )
        .await
        .expect("static model lifecycle should initialize");

        let advertised = runtime_state.advertised_models();
        assert_eq!(
            advertised.keys().cloned().collect::<BTreeSet<_>>(),
            BTreeSet::from(["model-a".to_string(), "model-b".to_string()])
        );
        assert!(advertised.values().all(|model| {
            model
                .stats
                .as_ref()
                .is_some_and(|stats| stats.last_mean_input_tps == 123.0)
        }));

        lifecycle.shutdown().await;
        stats.shutdown().await;
    }

    #[tokio::test]
    async fn retire_cancels_canary_before_removing_runtime_generation() {
        let kv_cache = BlockingKvCacheServer::spawn().await;
        let stats_config = StatsCollectorConfig {
            kv_cache_stats_url: Some(kv_cache.url()),
            // Poll quickly and keep the request open so retirement parks on
            // stats cleanup after the canary ordering point under test.
            kv_cache_poll_interval: Duration::from_millis(1),
            kv_cache_request_timeout: Duration::from_secs(60),
            ..StatsCollectorConfig::default()
        };
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let generation = ModelGeneration::new("model-a", 1);
        assert!(runtime_state.begin_generation(generation.clone()));
        assert!(runtime_state.publish_generation(&generation));

        let parent_stop = CancellationToken::new();
        let canary_cancelled = Arc::new(AtomicBool::new(false));
        let canary_runtime = runtime_state.clone();
        let canary_model_id = generation.model_id().to_string();
        let canary_cancelled_task = canary_cancelled.clone();
        let canary =
            OwnedTask::spawn_child("test model canary", &parent_stop, move |stop| async move {
                loop {
                    if stop.is_cancelled() {
                        canary_cancelled_task.store(true, Ordering::SeqCst);
                        return;
                    }
                    if canary_runtime
                        .current_generation(&canary_model_id)
                        .is_none()
                    {
                        return;
                    }
                    tokio::task::yield_now().await;
                }
            });
        let mut supervisor = ModelLifecycleSupervisor {
            config: ModelLifecycleConfig {
                upstream_http_base_url: "http://127.0.0.1:1".to_string(),
                source: ModelSource::Static(BTreeSet::new()),
                initialization: ModelInitialization::ConfiguredInputTps {
                    input_tps: 123.0,
                    pin: false,
                },
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
            },
            runtime_state: runtime_state.clone(),
            stats: stats.control(),
            metrics: None,
            http_client: reqwest::Client::new(),
            live: HashMap::from([(
                generation.model_id().to_string(),
                LiveModelGeneration {
                    generation: generation.clone(),
                    canary: Some(canary),
                },
            )]),
            pending: Vec::new(),
            next_generation: 2,
            next_poll_at: None,
        };

        let blocked_polls = kv_cache.blocked_poll_count();
        kv_cache.block();
        kv_cache.wait_for_blocked_poll_after(blocked_polls).await;
        let retire_generation = generation.clone();
        let retire = tokio::spawn(async move {
            supervisor.retire(&retire_generation).await;
            supervisor
        });
        wait_for(
            "retirement should stop the canary before stats cleanup",
            || canary_cancelled.load(Ordering::SeqCst) || parent_stop.is_cancelled(),
        )
        .await;

        assert!(
            canary_cancelled.load(Ordering::SeqCst),
            "canary should observe cancellation before the runtime generation disappears"
        );
        assert!(
            !parent_stop.is_cancelled(),
            "normal retirement must not look like an unexpected canary exit"
        );

        kv_cache.unblock();
        let _supervisor = retire
            .await
            .expect("retirement task should not panic after stats resumes");
        stats.shutdown().await;
        kv_cache.shutdown().await;
    }

    #[tokio::test]
    async fn calibration_traffic_seeds_positive_observed_stats_before_publication() {
        let upstream = TestUpstream::spawn(
            &[],
            CompletionBehavior::SaturateAfter {
                completed_requests_per_generation: 7,
            },
        )
        .await;
        let stats_config = StatsCollectorConfig {
            duration_floor: Duration::ZERO,
            openai_fallback_stats_enabled: false,
            ..StatsCollectorConfig::default()
        };
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());

        let lifecycle = start_model_lifecycle(
            ModelLifecycleConfig {
                upstream_http_base_url: upstream.base_url.clone(),
                source: ModelSource::Static(BTreeSet::from(["model-a".to_string()])),
                initialization: ModelInitialization::Calibration(CalibrationConfig {
                    health_timeout: Duration::from_secs(1),
                    calibration_requests: 1,
                    calibration_prompt_units: 1024,
                    calibration_max_concurrency: 1,
                    calibration_timeout: Duration::from_millis(20),
                }),
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
            },
            runtime_state.clone(),
            &stats,
            None,
        )
        .await
        .expect("calibration timeout should complete startup");

        let published = runtime_state
            .advertised_models()
            .remove("model-a")
            .expect("calibrated model should be advertised");
        assert!(
            published
                .stats
                .is_some_and(|stats| stats.last_mean_input_tps > 0.0),
            "completed calibration traffic must seed the ordinary observed stats window"
        );

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn initial_calibration_error_is_fatal_and_unadvertised() {
        let failing = Arc::new(AtomicBool::new(true));
        let upstream = TestUpstream::spawn(
            &[],
            CompletionBehavior::FailModel {
                model_id: "model-a",
                failing,
            },
        )
        .await;
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());

        let result = start_model_lifecycle(
            ModelLifecycleConfig {
                source: ModelSource::Static(BTreeSet::from(["model-a".to_string()])),
                ..calibration_discovery_config(&upstream.base_url, Duration::from_secs(1))
            },
            runtime_state.clone(),
            &stats,
            None,
        )
        .await;

        assert!(matches!(
            result,
            Err(super::ModelLifecycleError::Bringup(_))
        ));
        assert!(runtime_state.advertised_model_ids().is_empty());
        assert!(runtime_state.current_generation("model-a").is_none());

        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn static_models_never_poll_the_discovery_endpoint() {
        let upstream = TestUpstream::spawn(&["ignored-model"], CompletionBehavior::Pending).await;
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let lifecycle = start_model_lifecycle(
            ModelLifecycleConfig {
                upstream_http_base_url: upstream.base_url.clone(),
                source: ModelSource::Static(BTreeSet::from(["model-a".to_string()])),
                initialization: ModelInitialization::ConfiguredInputTps {
                    input_tps: 123.0,
                    pin: false,
                },
                bringup: BringupConfig {
                    enabled: false,
                    ..BringupConfig::default()
                },
            },
            runtime_state.clone(),
            &stats,
            None,
        )
        .await
        .expect("static model lifecycle should initialize");

        tokio::task::yield_now().await;
        assert_eq!(upstream.discovery_polls.load(Ordering::SeqCst), 0);
        assert_eq!(runtime_state.advertised_model_ids(), ["model-a"]);

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn discovery_waits_the_configured_interval_after_success_and_error() {
        let upstream = TestUpstream::spawn(&["model-a"], CompletionBehavior::Pending).await;
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let mut config = discovery_config(&upstream.base_url);
        let ModelSource::Discovered(discovery) = &mut config.source else {
            unreachable!("test config must use discovery")
        };
        discovery.poll_interval = Duration::from_millis(500);
        let lifecycle = start_model_lifecycle(config, runtime_state, &stats, None)
            .await
            .expect("initial discovery should initialize");

        upstream.discovery_polled.notified().await;
        tokio::time::pause();
        assert_eq!(upstream.discovery_polls.load(Ordering::SeqCst), 1);
        tokio::time::advance(Duration::from_millis(499)).await;
        tokio::task::yield_now().await;
        assert_eq!(upstream.discovery_polls.load(Ordering::SeqCst), 1);
        let second_poll = upstream.discovery_polled.notified();
        tokio::time::advance(Duration::from_millis(1)).await;
        second_poll.await;
        assert_eq!(upstream.discovery_polls.load(Ordering::SeqCst), 2);

        upstream.set_discovery_failing(true);
        tokio::time::advance(Duration::from_millis(499)).await;
        tokio::task::yield_now().await;
        assert_eq!(upstream.discovery_polls.load(Ordering::SeqCst), 2);
        let third_poll = upstream.discovery_polled.notified();
        tokio::time::advance(Duration::from_millis(1)).await;
        third_poll.await;
        assert_eq!(upstream.discovery_polls.load(Ordering::SeqCst), 3);

        tokio::time::resume();
        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn valid_empty_initial_discovery_keeps_polling_and_admits_a_later_model() {
        let upstream = TestUpstream::spawn(&[], CompletionBehavior::Pending).await;
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let lifecycle = start_model_lifecycle(
            discovery_config(&upstream.base_url),
            runtime_state.clone(),
            &stats,
            None,
        )
        .await
        .expect("empty initial discovery should initialize");

        assert!(runtime_state.advertised_model_ids().is_empty());
        upstream.set_models(&["model-a"]).await;
        wait_for_model_ids(&runtime_state, &["model-a"]).await;

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn discovered_remove_and_readd_allocates_a_fresh_generation() {
        let upstream = TestUpstream::spawn(&["model-a"], CompletionBehavior::Pending).await;
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let lifecycle = start_model_lifecycle(
            discovery_config(&upstream.base_url),
            runtime_state.clone(),
            &stats,
            None,
        )
        .await
        .expect("initial discovery should initialize");
        let first = runtime_state
            .current_generation("model-a")
            .expect("first generation should exist");

        upstream.set_models(&[]).await;
        wait_for_model_ids(&runtime_state, &[]).await;
        upstream.set_models(&["model-a"]).await;
        wait_for_model_ids(&runtime_state, &["model-a"]).await;
        let replacement = runtime_state
            .current_generation("model-a")
            .expect("replacement generation should exist");

        assert_ne!(first, replacement);
        assert_eq!(
            runtime_state
                .model_stats("model-a")
                .expect("replacement stats should exist")
                .last_mean_input_tps,
            123.0
        );

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn discovered_remove_and_readd_completes_two_calibration_plans() {
        let upstream = TestUpstream::spawn(
            &["model-a"],
            CompletionBehavior::SaturateAfter {
                completed_requests_per_generation: 5,
            },
        )
        .await;
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let stats_config = StatsCollectorConfig {
            duration_floor: Duration::ZERO,
            ..StatsCollectorConfig::default()
        };
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let lifecycle = start_model_lifecycle(
            calibration_discovery_config(&upstream.base_url, Duration::from_millis(20)),
            runtime_state.clone(),
            &stats,
            Some(metrics.clone()),
        )
        .await
        .expect("first calibration should complete");
        let first = runtime_state
            .current_generation("model-a")
            .expect("first generation should exist");

        upstream.set_models(&[]).await;
        wait_for_model_ids(&runtime_state, &[]).await;
        upstream.set_models(&["model-a"]).await;
        wait_for_model_ids(&runtime_state, &["model-a"]).await;
        let replacement = runtime_state
            .current_generation("model-a")
            .expect("replacement generation should exist");

        assert_ne!(first, replacement);
        wait_for_calibration_count(&metrics, "model-a", "completed", 2).await;

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn discovery_errors_retain_last_known_good_until_a_valid_empty_set() {
        let upstream = TestUpstream::spawn(&["model-a"], CompletionBehavior::Pending).await;
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let lifecycle = start_model_lifecycle(
            discovery_config(&upstream.base_url),
            runtime_state.clone(),
            &stats,
            None,
        )
        .await
        .expect("initial discovery should initialize");

        upstream.set_models(&[]).await;
        upstream.set_discovery_failing(true);
        let polls = upstream.discovery_polls.load(Ordering::SeqCst);
        wait_for_discovery_polls(&upstream.discovery_polls, polls + 2).await;
        assert_eq!(runtime_state.advertised_model_ids(), ["model-a"]);

        upstream.set_discovery_failing(false);
        wait_for_model_ids(&runtime_state, &[]).await;

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn shutdown_interrupts_a_blocked_discovery_poll() {
        let upstream = TestUpstream::spawn(&["model-a"], CompletionBehavior::Pending).await;
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let lifecycle = start_model_lifecycle(
            discovery_config(&upstream.base_url),
            runtime_state,
            &stats,
            None,
        )
        .await
        .expect("initial discovery should initialize");
        upstream.set_discovery_blocked(true);
        let polls = upstream.discovery_polls.load(Ordering::SeqCst);
        wait_for_discovery_polls(&upstream.discovery_polls, polls + 1).await;

        tokio::time::timeout(Duration::from_secs(1), lifecycle.shutdown())
            .await
            .expect("shutdown should cancel the blocked discovery request");

        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn removal_cancels_blocked_calibration_without_publication() {
        let mut upstream = TestUpstream::spawn(&[], CompletionBehavior::Pending).await;
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let lifecycle = start_model_lifecycle(
            calibration_discovery_config(&upstream.base_url, Duration::from_secs(60)),
            runtime_state.clone(),
            &stats,
            Some(metrics.clone()),
        )
        .await
        .expect("empty initial discovery should start");

        upstream.set_models(&["model-a"]).await;
        let retired = upstream.next_calibration().await;
        assert!(runtime_state.advertised_model_ids().is_empty());
        upstream.set_models(&[]).await;
        wait_for_generation_retirement(&runtime_state, "model-a").await;
        let polls = upstream.discovery_polls.load(Ordering::SeqCst);
        wait_for_discovery_polls(&upstream.discovery_polls, polls + 2).await;

        assert!(runtime_state.advertised_model_ids().is_empty());
        assert_eq!(
            stats
                .control()
                .flush_and_snapshot(&retired)
                .await
                .expect("stats collector should respond"),
            None
        );
        wait_for_calibration_count(&metrics, "model-a", "cancelled", 1).await;
        let metric_body = metrics.gather_text().expect("metrics should encode");
        assert!(metric_body.contains(
            r#"pylon_model_calibration_duration_ms_count{model="model-a",outcome="cancelled"} 1"#
        ));
        assert!(!metric_body.contains(
            r#"pylon_model_calibration_duration_ms_count{model="model-a",outcome="completed"}"#
        ));

        upstream.set_models(&["model-a"]).await;
        let replacement = upstream.next_calibration().await;
        assert_ne!(retired, replacement);
        upstream.set_models(&[]).await;
        wait_for_generation_retirement(&runtime_state, "model-a").await;

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn dynamic_calibration_errors_retry_without_withdrawing_siblings() {
        let failing = Arc::new(AtomicBool::new(false));
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let mut upstream = TestUpstream::spawn(
            &[],
            CompletionBehavior::FailModel {
                model_id: "model-b",
                failing: failing.clone(),
            },
        )
        .await;
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let mut config =
            calibration_discovery_config(&upstream.base_url, Duration::from_millis(20));
        let ModelSource::Discovered(discovery) = &mut config.source else {
            unreachable!("test config must use discovery")
        };
        discovery.poll_interval = Duration::from_millis(100);
        let lifecycle =
            start_model_lifecycle(config, runtime_state.clone(), &stats, Some(metrics.clone()))
                .await
                .expect("empty initial discovery should start");

        upstream.set_models(&["model-a"]).await;
        assert_eq!(upstream.next_calibration().await.model_id(), "model-a");
        wait_for_model_ids(&runtime_state, &["model-a"]).await;

        failing.store(true, Ordering::SeqCst);
        upstream.set_models(&["model-a", "model-b"]).await;
        let first_attempt = upstream.next_calibration().await;
        assert_eq!(first_attempt.model_id(), "model-b");
        let mut error_observed = false;
        for _ in 0..100 {
            let body = metrics.gather_text().expect("metrics should encode");
            if body.contains(
                r#"pylon_model_calibration_duration_ms_count{model="model-b",outcome="error"} 1"#,
            ) {
                error_observed = true;
                break;
            }
            tokio::task::yield_now().await;
        }
        assert!(
            error_observed,
            "the first failed attempt should be recorded"
        );
        assert_eq!(runtime_state.advertised_model_ids(), ["model-a"]);
        let second_attempt = upstream.next_calibration().await;
        assert_eq!(second_attempt.model_id(), "model-b");
        assert_eq!(first_attempt, second_attempt);
        assert_eq!(runtime_state.advertised_model_ids(), ["model-a"]);
        wait_for_calibration_count(&metrics, "model-b", "error", 2).await;

        failing.store(false, Ordering::SeqCst);
        wait_for_model_ids(&runtime_state, &["model-a", "model-b"]).await;
        let completed_metrics = metrics.gather_text().expect("metrics should encode");
        assert!(completed_metrics.contains(
            r#"pylon_model_calibration_duration_ms_count{model="model-b",outcome="completed"} 1"#
        ));

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[test]
    fn pending_initializations_choose_the_earliest_retry_deadline() {
        let now = tokio::time::Instant::now();
        let pending = [
            super::PendingInitialization {
                generation: ModelGeneration::new("model-late", 1),
                retry_at: now + Duration::from_secs(2),
            },
            super::PendingInitialization {
                generation: ModelGeneration::new("model-ready", 2),
                retry_at: now,
            },
            super::PendingInitialization {
                generation: ModelGeneration::new("model-next", 3),
                retry_at: now + Duration::from_secs(1),
            },
        ];

        assert_eq!(super::earliest_pending_index(&pending), Some(1));
    }

    #[tokio::test]
    async fn shutdown_cancels_active_calibration_without_completing_it() {
        let mut upstream = TestUpstream::spawn(&[], CompletionBehavior::Pending).await;
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let lifecycle = start_model_lifecycle(
            calibration_discovery_config(&upstream.base_url, Duration::from_secs(60)),
            runtime_state.clone(),
            &stats,
            Some(metrics.clone()),
        )
        .await
        .expect("empty initial discovery should start");

        upstream.set_models(&["model-a"]).await;
        let generation = upstream.next_calibration().await;
        lifecycle.shutdown().await;

        assert!(runtime_state.advertised_model_ids().is_empty());
        assert_eq!(
            stats
                .control()
                .flush_and_snapshot(&generation)
                .await
                .expect("stats collector should respond"),
            None
        );
        wait_for_calibration_count(&metrics, "model-a", "cancelled", 1).await;
        let metric_body = metrics.gather_text().expect("metrics should encode");
        assert!(!metric_body.contains(
            r#"pylon_model_calibration_duration_ms_count{model="model-a",outcome="completed"}"#
        ));

        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn blocked_discovery_does_not_pause_or_inflate_active_calibration() {
        let mut upstream = TestUpstream::spawn(&[], CompletionBehavior::Pending).await;
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let mut config =
            calibration_discovery_config(&upstream.base_url, Duration::from_millis(100));
        let ModelSource::Discovered(discovery) = &mut config.source else {
            unreachable!("test config must use discovery")
        };
        discovery.request_timeout = Duration::from_secs(60);
        let lifecycle =
            start_model_lifecycle(config, runtime_state.clone(), &stats, Some(metrics.clone()))
                .await
                .expect("empty initial discovery should start");

        upstream.set_models(&["model-a"]).await;
        upstream.next_calibration().await;
        upstream.set_discovery_blocked(true);
        let polls = upstream.discovery_polls.load(Ordering::SeqCst);
        wait_for_discovery_polls(&upstream.discovery_polls, polls + 1).await;

        tokio::time::timeout(
            Duration::from_millis(500),
            wait_for_model_ids(&runtime_state, &["model-a"]),
        )
        .await
        .expect("a blocked discovery request must not pause calibration");
        wait_for_calibration_count(&metrics, "model-a", "completed", 1).await;
        assert!(
            calibration_duration_sum_ms(&metrics, "model-a", "completed") < 250.0,
            "discovery request latency must not inflate calibration duration"
        );

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn sibling_reconciliation_does_not_pause_or_inflate_active_calibration() {
        let mut upstream = TestUpstream::spawn(&[], CompletionBehavior::Pending).await;
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let active = ModelGeneration::new("model-a", 1);
        let sibling = ModelGeneration::new("model-b", 2);
        assert!(runtime_state.begin_generation(active.clone()));
        assert!(runtime_state.begin_generation(sibling.clone()));
        assert!(runtime_state.publish_generation(&sibling));
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let mut config =
            calibration_discovery_config(&upstream.base_url, Duration::from_millis(50));
        let ModelSource::Discovered(discovery) = &mut config.source else {
            unreachable!("test config must use discovery")
        };
        // Poll quickly so sibling reconciliation competes with the active
        // calibration attempt before its saturation timeout fires.
        discovery.poll_interval = Duration::from_millis(1);
        upstream.set_models(&["model-a"]).await;
        let release_sibling_canary = Arc::new(Notify::new());
        let canary_release = release_sibling_canary.clone();
        let sibling_canary = OwnedTask::spawn("blocked sibling canary", move |_| async move {
            canary_release.notified().await;
        });
        let mut supervisor = ModelLifecycleSupervisor {
            config,
            runtime_state: runtime_state.clone(),
            stats: stats.control(),
            metrics: Some(metrics.clone()),
            http_client: reqwest::Client::new(),
            live: HashMap::from([
                (
                    active.model_id().to_string(),
                    LiveModelGeneration {
                        generation: active.clone(),
                        canary: None,
                    },
                ),
                (
                    sibling.model_id().to_string(),
                    LiveModelGeneration {
                        generation: sibling.clone(),
                        canary: Some(sibling_canary),
                    },
                ),
            ]),
            pending: Vec::new(),
            next_generation: 3,
            next_poll_at: Some(tokio::time::Instant::now()),
        };
        let active_for_drive = active.clone();
        let drive = tokio::spawn(async move {
            let outcome = supervisor
                .drive_initialization(&active_for_drive, None)
                .await;
            (outcome, supervisor)
        });
        let generation = upstream.next_calibration().await;
        assert_eq!(generation.model_id(), "model-a");
        wait_for_discovery_polls(&upstream.discovery_polls, 1).await;
        upstream.set_models(&["model-a", "model-b"]).await;
        let polls_after_readd = upstream.discovery_polls.load(Ordering::SeqCst);
        wait_for_discovery_polls(&upstream.discovery_polls, polls_after_readd + 1).await;

        tokio::time::timeout(
            Duration::from_millis(750),
            wait_for_calibration_count(&metrics, "model-a", "completed", 1),
        )
        .await
        .expect("sibling reconciliation must not pause active calibration completion");
        assert!(
            calibration_duration_sum_ms(&metrics, "model-a", "completed") < 250.0,
            "sibling reconciliation latency must not inflate calibration duration"
        );

        release_sibling_canary.notify_waiters();
        let (outcome, mut supervisor) = tokio::time::timeout(Duration::from_secs(1), drive)
            .await
            .expect("deferred reconciliation should finish after the sibling canary stops")
            .expect("initialization task should not panic");
        assert!(matches!(
            outcome,
            Ok(super::InitializationAttemptOutcome::Ready)
        ));
        let replacement = runtime_state
            .current_generation("model-b")
            .expect("reappearing sibling should start a fresh generation");
        assert_ne!(
            replacement, sibling,
            "queued remove/readd transitions must not leave the old sibling generation live"
        );
        assert!(
            !runtime_state.generation_is_published(&replacement),
            "reappearing sibling should calibrate before publication"
        );

        supervisor.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn shutdown_cancels_blocked_discovery_during_active_calibration() {
        let mut upstream = TestUpstream::spawn(&[], CompletionBehavior::Pending).await;
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let mut config = calibration_discovery_config(&upstream.base_url, Duration::from_secs(60));
        let ModelSource::Discovered(discovery) = &mut config.source else {
            unreachable!("test config must use discovery")
        };
        discovery.request_timeout = Duration::from_secs(60);
        let lifecycle = start_model_lifecycle(config, runtime_state, &stats, Some(metrics.clone()))
            .await
            .expect("empty initial discovery should start");

        upstream.set_models(&["model-a"]).await;
        upstream.next_calibration().await;
        upstream.set_discovery_blocked(true);
        let polls = upstream.discovery_polls.load(Ordering::SeqCst);
        wait_for_discovery_polls(&upstream.discovery_polls, polls + 1).await;

        tokio::time::timeout(Duration::from_millis(500), lifecycle.shutdown())
            .await
            .expect("shutdown must cancel discovery while calibration is active");
        wait_for_calibration_count(&metrics, "model-a", "cancelled", 1).await;

        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn discovery_errors_during_calibration_retain_the_pending_generation() {
        let mut upstream = TestUpstream::spawn(&[], CompletionBehavior::Pending).await;
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let lifecycle = start_model_lifecycle(
            calibration_discovery_config(&upstream.base_url, Duration::from_millis(100)),
            runtime_state.clone(),
            &stats,
            None,
        )
        .await
        .expect("empty initial discovery should start");

        upstream.set_models(&["model-a"]).await;
        let generation = upstream.next_calibration().await;
        upstream.set_discovery_failing(true);
        let polls = upstream.discovery_polls.load(Ordering::SeqCst);
        wait_for_discovery_polls(&upstream.discovery_polls, polls + 2).await;

        assert_eq!(
            runtime_state.current_generation("model-a"),
            Some(generation.clone())
        );
        assert!(runtime_state.advertised_model_ids().is_empty());
        wait_for_model_ids(&runtime_state, &["model-a"]).await;
        assert_eq!(
            runtime_state.current_generation("model-a"),
            Some(generation),
            "poll errors must retain the last-known-good generation"
        );

        upstream.set_models(&[]).await;
        upstream.set_discovery_failing(false);
        wait_for_model_ids(&runtime_state, &[]).await;
        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn initial_discovered_calibrations_run_sequentially() {
        let mut upstream = TestUpstream::spawn(
            &["model-d", "model-b", "model-a", "model-c"],
            CompletionBehavior::Pending,
        )
        .await;
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let config = calibration_discovery_config(&upstream.base_url, Duration::from_millis(20));
        let start_runtime = runtime_state.clone();
        let start = tokio::spawn(async move {
            let result = start_model_lifecycle(config, start_runtime, &stats, None).await;
            (result, stats)
        });

        let mut order = Vec::new();
        for _ in 0..4 {
            let generation = upstream.next_calibration().await;
            assert!(
                !order.contains(&generation.model_id().to_string()),
                "one generation should reach its timeout before the next starts"
            );
            order.push(generation.model_id().to_string());
        }
        let (lifecycle, stats) = start.await.expect("lifecycle startup should not panic");
        let lifecycle = lifecycle.expect("all expected timeouts should complete calibration");

        assert_eq!(order, ["model-a", "model-b", "model-c", "model-d"]);
        assert_eq!(
            runtime_state.advertised_model_ids(),
            ["model-a", "model-b", "model-c", "model-d"]
        );

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }

    #[tokio::test]
    async fn initial_removal_cancels_blocked_calibration_before_publication() {
        let mut upstream = TestUpstream::spawn(&["model-a"], CompletionBehavior::Pending).await;
        let metrics = PylonMetrics::new().expect("metrics should initialize");
        let stats_config = StatsCollectorConfig::default();
        let (runtime_state, observations) = PylonRuntimeState::observed(
            InferenceServerStatus::Active,
            &[],
            stats_config.observation_channel_capacity,
            None,
        );
        let stats = start_stats_collector(stats_config, observations, runtime_state.clone());
        let config = calibration_discovery_config(&upstream.base_url, Duration::from_secs(60));
        let start_runtime = runtime_state.clone();
        let start_metrics = metrics.clone();
        let start = tokio::spawn(async move {
            let result =
                start_model_lifecycle(config, start_runtime, &stats, Some(start_metrics)).await;
            (result, stats)
        });

        let retired = upstream.next_calibration().await;
        upstream.set_models(&[]).await;
        let (lifecycle, stats) = tokio::time::timeout(Duration::from_secs(2), start)
            .await
            .expect("initial removal should cancel blocked calibration")
            .expect("lifecycle startup task should not panic");
        let lifecycle = lifecycle.expect("valid empty discovery should complete startup");

        assert!(runtime_state.advertised_model_ids().is_empty());
        assert_eq!(
            stats
                .control()
                .flush_and_snapshot(&retired)
                .await
                .expect("stats collector should respond"),
            None
        );
        wait_for_calibration_count(&metrics, "model-a", "cancelled", 1).await;

        lifecycle.shutdown().await;
        stats.shutdown().await;
        upstream.shutdown().await;
    }
}

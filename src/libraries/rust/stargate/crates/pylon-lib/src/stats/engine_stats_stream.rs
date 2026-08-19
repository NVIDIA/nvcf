// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::fmt;
use std::str::FromStr;
use std::sync::Arc;
use std::time::Duration;

use stargate_proto::dynamo_frontend_stats as proto;
use stargate_runtime::OwnedTask;
use tokio::time::Instant as TokioInstant;
use tokio_util::sync::CancellationToken;
use tonic::Code;

use super::collector::{RequestCounterUpdate, StatsAggregatorUpdate, StatsUpdateSource};
use super::kv_stats::kv_snapshot_from_proto;
use super::metrics::PylonMetrics;
use crate::PylonRuntimeState;
use crate::generated_request_id::generated_request_generation;

const DEFAULT_INITIAL_RECONNECT_BACKOFF: Duration = Duration::from_millis(100);
const DEFAULT_MAX_RECONNECT_BACKOFF: Duration = Duration::from_secs(5);

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EngineStatsStreamMode {
    Auto,
    Required,
    Off,
}

impl EngineStatsStreamMode {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Auto => "auto",
            Self::Required => "required",
            Self::Off => "off",
        }
    }
}

impl fmt::Display for EngineStatsStreamMode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl FromStr for EngineStatsStreamMode {
    type Err = ParseEngineStatsStreamModeError;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        match value {
            "auto" => Ok(Self::Auto),
            "required" => Ok(Self::Required),
            "off" => Ok(Self::Off),
            _ => Err(ParseEngineStatsStreamModeError),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, thiserror::Error)]
#[error("expected one of auto, required, off")]
pub struct ParseEngineStatsStreamModeError;

#[derive(Debug, Clone)]
pub struct EngineStatsStreamConfig {
    pub endpoint: String,
    pub mode: EngineStatsStreamMode,
    pub initial_reconnect_backoff: Duration,
    pub max_reconnect_backoff: Duration,
    pub metrics: Option<Arc<PylonMetrics>>,
    pub runtime_state: Option<PylonRuntimeState>,
}

impl EngineStatsStreamConfig {
    pub fn new(upstream_base_url: &str, mode: EngineStatsStreamMode) -> Self {
        Self {
            endpoint: upstream_base_url.trim_end_matches('/').to_string(),
            mode,
            initial_reconnect_backoff: DEFAULT_INITIAL_RECONNECT_BACKOFF,
            max_reconnect_backoff: DEFAULT_MAX_RECONNECT_BACKOFF,
            metrics: None,
            runtime_state: None,
        }
    }
}

impl Default for EngineStatsStreamConfig {
    fn default() -> Self {
        Self::new("http://127.0.0.1:8090", EngineStatsStreamMode::Auto)
    }
}

owned_task_handle!(EngineStatsStreamHandle);

pub fn start_engine_stats_stream(
    config: EngineStatsStreamConfig,
    stats_update_tx: flume::Sender<StatsAggregatorUpdate>,
) -> Option<EngineStatsStreamHandle> {
    if config.mode == EngineStatsStreamMode::Off {
        return None;
    }
    let task = OwnedTask::spawn("engine stats stream", move |stop| {
        run_engine_stats_stream(config, stats_update_tx, stop)
    });
    Some(EngineStatsStreamHandle { task })
}

async fn run_engine_stats_stream(
    config: EngineStatsStreamConfig,
    stats_update_tx: flume::Sender<StatsAggregatorUpdate>,
    stop: CancellationToken,
) {
    let mut backoff = config.initial_reconnect_backoff;
    let mut valid_event_seen = false;
    loop {
        if stop.is_cancelled() {
            return;
        }
        let retry_reason =
            match read_stream_once(&config, &stats_update_tx, &stop, &mut valid_event_seen).await {
                StreamReadOutcome::Stopped => return,
                StreamReadOutcome::Unsupported
                    if config.mode == EngineStatsStreamMode::Auto && !valid_event_seen =>
                {
                    tracing::warn!(
                        endpoint = config.endpoint,
                        "frontend stats gRPC service unsupported; using OpenAI fallback observation"
                    );
                    let _ = send_stats_update(
                        &stats_update_tx,
                        StatsAggregatorUpdate::EnableOpenAiFallback,
                        &stop,
                    )
                    .await;
                    return;
                }
                StreamReadOutcome::Unsupported => "unsupported",
                StreamReadOutcome::Retry(reason) => reason,
            };

        if let Some(metrics) = &config.metrics {
            metrics.observe_engine_stats_reconnect(retry_reason);
        }
        if valid_event_seen {
            backoff = config.initial_reconnect_backoff;
        }
        if stop
            .run_until_cancelled(tokio::time::sleep(backoff))
            .await
            .is_none()
        {
            return;
        }
        backoff = (backoff * 2).min(config.max_reconnect_backoff);
    }
}

#[derive(Debug)]
enum StreamReadOutcome {
    Stopped,
    Unsupported,
    Retry(&'static str),
}

async fn read_stream_once(
    config: &EngineStatsStreamConfig,
    stats_update_tx: &flume::Sender<StatsAggregatorUpdate>,
    stop: &CancellationToken,
    valid_event_seen: &mut bool,
) -> StreamReadOutcome {
    let connect =
        proto::frontend_stats_client::FrontendStatsClient::connect(config.endpoint.clone());
    let mut client = match stop.run_until_cancelled(connect).await {
        None => return StreamReadOutcome::Stopped,
        Some(Ok(client)) => client,
        Some(Err(error)) => {
            tracing::warn!(endpoint = config.endpoint, %error, "frontend stats gRPC connect failed");
            return StreamReadOutcome::Retry("connect_error");
        }
    };

    let watch = client.watch_stats(proto::WatchStatsRequest {});
    let response = match stop.run_until_cancelled(watch).await {
        None => return StreamReadOutcome::Stopped,
        Some(Ok(response)) => response,
        Some(Err(status)) => return status_outcome(config, status),
    };

    observe_connected(config, true);
    let mut stream = response.into_inner();
    let outcome = loop {
        let message = match stop.run_until_cancelled(stream.message()).await {
            None => break StreamReadOutcome::Stopped,
            Some(message) => message,
        };
        let update = match message {
            Ok(Some(update)) => update,
            Ok(None) => break StreamReadOutcome::Retry("eof"),
            Err(status) => break status_outcome(config, status),
        };
        let (event_type, update) = match translate_update(config, update, TokioInstant::now()) {
            Ok(update) => update,
            Err(error) => {
                tracing::warn!(endpoint = config.endpoint, %error, "invalid frontend stats update");
                if let Some(metrics) = &config.metrics {
                    metrics.observe_engine_stats_invalid_event("protobuf");
                }
                continue;
            }
        };
        *valid_event_seen = true;
        if let Some(metrics) = &config.metrics {
            metrics.observe_engine_stats_stream_event(event_type);
        }
        if !send_stats_update(stats_update_tx, update, stop).await {
            break StreamReadOutcome::Stopped;
        }
    };
    observe_connected(config, false);
    outcome
}

fn status_outcome(config: &EngineStatsStreamConfig, status: tonic::Status) -> StreamReadOutcome {
    if matches!(status.code(), Code::Unimplemented | Code::NotFound) {
        tracing::warn!(endpoint = config.endpoint, %status, "frontend stats gRPC service is unsupported");
        StreamReadOutcome::Unsupported
    } else {
        tracing::warn!(endpoint = config.endpoint, %status, "frontend stats gRPC stream disconnected");
        StreamReadOutcome::Retry("grpc_status")
    }
}

fn translate_update(
    config: &EngineStatsStreamConfig,
    update: proto::StatsUpdate,
    observed_at: TokioInstant,
) -> anyhow::Result<(&'static str, StatsAggregatorUpdate)> {
    match update.update {
        Some(proto::stats_update::Update::RequestStats(request)) => {
            let request_id = request.request_id.trim();
            let model_id = request.model.trim();
            anyhow::ensure!(!request_id.is_empty(), "request ID is empty");
            anyhow::ensure!(!model_id.is_empty(), "model ID is empty");
            anyhow::ensure!(
                request.tokens_processed.is_some()
                    || request.tokens_generated.is_some()
                    || request.finished,
                "request update has no counters"
            );
            let generation = generated_request_generation(request_id, model_id).or_else(|| {
                config
                    .runtime_state
                    .as_ref()
                    .and_then(|state| state.request_generation(request_id))
            });
            Ok((
                "stats",
                StatsAggregatorUpdate::RequestCounters(RequestCounterUpdate {
                    source: StatsUpdateSource::EngineStatsStream,
                    request_id: request_id.to_string(),
                    model_id: model_id.to_string(),
                    generation,
                    tokens_processed: request.tokens_processed,
                    tokens_generated: request.tokens_generated,
                    finished: request.finished,
                    observed_at,
                }),
            ))
        }
        Some(proto::stats_update::Update::KvStats(snapshot)) => Ok((
            "kv_stats_snapshot",
            StatsAggregatorUpdate::KvCache(kv_snapshot_from_proto(snapshot)?),
        )),
        None => anyhow::bail!("stats update is missing its payload"),
    }
}

async fn send_stats_update(
    stats_update_tx: &flume::Sender<StatsAggregatorUpdate>,
    update: StatsAggregatorUpdate,
    stop: &CancellationToken,
) -> bool {
    match stats_update_tx.try_send(update) {
        Ok(()) => true,
        Err(flume::TrySendError::Full(update)) => stop
            .run_until_cancelled(stats_update_tx.send_async(update))
            .await
            .is_some_and(|result| result.is_ok()),
        Err(flume::TrySendError::Disconnected(_)) => false,
    }
}

fn observe_connected(config: &EngineStatsStreamConfig, connected: bool) {
    if let Some(metrics) = &config.metrics {
        metrics.observe_engine_stats_stream_connected(config.mode.as_str(), connected);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::pin::Pin;
    use std::sync::atomic::{AtomicUsize, Ordering};

    use futures::Stream;
    use tokio::net::TcpListener;

    fn request_update(request: proto::RequestStats) -> proto::StatsUpdate {
        proto::StatsUpdate {
            update: Some(proto::stats_update::Update::RequestStats(request)),
        }
    }

    #[test]
    fn parses_stream_modes() {
        assert_eq!("auto".parse(), Ok(EngineStatsStreamMode::Auto));
        assert_eq!("required".parse(), Ok(EngineStatsStreamMode::Required));
        assert_eq!("off".parse(), Ok(EngineStatsStreamMode::Off));
        assert!("other".parse::<EngineStatsStreamMode>().is_err());
    }

    #[test]
    fn translates_request_counters() {
        let update = request_update(proto::RequestStats {
            request_id: "request-a".to_string(),
            model: "model-a".to_string(),
            tokens_processed: Some(12),
            tokens_generated: Some(3),
            finished: false,
        });
        let (_, update) = translate_update(
            &EngineStatsStreamConfig::default(),
            update,
            TokioInstant::now(),
        )
        .unwrap();
        let StatsAggregatorUpdate::RequestCounters(update) = update else {
            panic!("expected request counters");
        };
        assert_eq!(update.request_id, "request-a");
        assert_eq!(update.model_id, "model-a");
        assert_eq!(update.tokens_processed, Some(12));
        assert_eq!(update.tokens_generated, Some(3));
    }

    #[test]
    fn rejects_empty_request_updates() {
        let update = request_update(proto::RequestStats {
            request_id: "request-a".to_string(),
            model: "model-a".to_string(),
            tokens_processed: None,
            tokens_generated: None,
            finished: false,
        });
        assert!(
            translate_update(
                &EngineStatsStreamConfig::default(),
                update,
                TokioInstant::now()
            )
            .is_err()
        );
    }

    #[test]
    fn translates_kv_snapshots_on_the_same_stream() {
        let update = proto::StatsUpdate {
            update: Some(proto::stats_update::Update::KvStats(
                proto::KvStatsSnapshot {
                    snapshot_id: 1,
                    observed_at_unix_ms: 10,
                    models: Vec::new(),
                },
            )),
        };
        let (_, update) = translate_update(
            &EngineStatsStreamConfig::default(),
            update,
            TokioInstant::now(),
        )
        .unwrap();
        assert!(matches!(update, StatsAggregatorUpdate::KvCache(_)));
    }

    #[derive(Clone)]
    struct TestFrontendStats {
        calls: Arc<AtomicUsize>,
        updates: Arc<Vec<proto::StatsUpdate>>,
    }

    #[tonic::async_trait]
    impl proto::frontend_stats_server::FrontendStats for TestFrontendStats {
        type WatchStatsStream =
            Pin<Box<dyn Stream<Item = Result<proto::StatsUpdate, tonic::Status>> + Send>>;
        type WatchKvPlacementsStream =
            Pin<Box<dyn Stream<Item = Result<proto::KvPlacementUpdate, tonic::Status>> + Send>>;

        async fn watch_stats(
            &self,
            _request: tonic::Request<proto::WatchStatsRequest>,
        ) -> Result<tonic::Response<Self::WatchStatsStream>, tonic::Status> {
            self.calls.fetch_add(1, Ordering::Relaxed);
            let stream = futures::stream::iter(self.updates.as_ref().clone().into_iter().map(Ok));
            Ok(tonic::Response::new(Box::pin(stream)))
        }

        async fn watch_kv_placements(
            &self,
            _request: tonic::Request<proto::WatchKvPlacementsRequest>,
        ) -> Result<tonic::Response<Self::WatchKvPlacementsStream>, tonic::Status> {
            Err(tonic::Status::unimplemented("not used"))
        }
    }

    async fn spawn_test_server(router: axum::Router) -> (String, tokio::task::JoinHandle<()>) {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let endpoint = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move {
            axum::serve(listener, router).await.unwrap();
        });
        (endpoint, server)
    }

    #[tokio::test]
    async fn grpc_stream_delivers_both_updates_and_reconnects_after_eof() {
        let calls = Arc::new(AtomicUsize::new(0));
        let updates = Arc::new(vec![
            request_update(proto::RequestStats {
                request_id: "request-a".to_string(),
                model: "model-a".to_string(),
                tokens_processed: Some(10),
                tokens_generated: None,
                finished: false,
            }),
            proto::StatsUpdate {
                update: Some(proto::stats_update::Update::KvStats(
                    proto::KvStatsSnapshot {
                        snapshot_id: 1,
                        observed_at_unix_ms: 10,
                        models: Vec::new(),
                    },
                )),
            },
        ]);
        let service = proto::frontend_stats_server::FrontendStatsServer::new(TestFrontendStats {
            calls: calls.clone(),
            updates,
        });
        let router = tonic::service::Routes::new(service).into_axum_router();
        let (endpoint, server) = spawn_test_server(router).await;
        let config = EngineStatsStreamConfig {
            initial_reconnect_backoff: Duration::from_millis(1),
            max_reconnect_backoff: Duration::from_millis(1),
            ..EngineStatsStreamConfig::new(&endpoint, EngineStatsStreamMode::Required)
        };
        let (tx, rx) = flume::bounded(8);
        let stream = start_engine_stats_stream(config, tx).unwrap();

        let first = tokio::time::timeout(Duration::from_secs(1), rx.recv_async())
            .await
            .unwrap()
            .unwrap();
        let second = tokio::time::timeout(Duration::from_secs(1), rx.recv_async())
            .await
            .unwrap()
            .unwrap();
        let third = tokio::time::timeout(Duration::from_secs(1), rx.recv_async())
            .await
            .unwrap()
            .unwrap();
        assert!(matches!(first, StatsAggregatorUpdate::RequestCounters(_)));
        assert!(matches!(second, StatsAggregatorUpdate::KvCache(_)));
        assert!(matches!(third, StatsAggregatorUpdate::RequestCounters(_)));
        assert!(calls.load(Ordering::Relaxed) >= 2);

        stream.shutdown().await;
        server.abort();
    }

    #[tokio::test]
    async fn auto_mode_falls_back_when_grpc_service_is_absent() {
        let (endpoint, server) = spawn_test_server(axum::Router::new()).await;
        let config = EngineStatsStreamConfig::new(&endpoint, EngineStatsStreamMode::Auto);
        let (tx, rx) = flume::bounded(1);
        let stream = start_engine_stats_stream(config, tx).unwrap();

        let update = tokio::time::timeout(Duration::from_secs(1), rx.recv_async())
            .await
            .expect("auto mode should resolve unsupported gRPC")
            .unwrap();
        assert!(matches!(
            update,
            StatsAggregatorUpdate::EnableOpenAiFallback
        ));

        stream.shutdown().await;
        server.abort();
    }
}

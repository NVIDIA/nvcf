// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::convert::Infallible;
use std::pin::Pin;
use std::sync::atomic::Ordering;
use std::time::Duration;

use axum::body::{Body, Bytes};
use axum::extract::State;
use axum::http::{HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use futures::Stream;
use stargate_proto::dynamo_frontend_stats as proto;
use tokio::sync::broadcast;
use tonic::{Request, Response as GrpcResponse, Status};

use crate::AppState;

#[derive(Debug, Clone)]
pub(crate) struct StatsStreamEvent {
    pub(crate) request_id: String,
    pub(crate) model: String,
    pub(crate) tokens_processed: Option<u64>,
    pub(crate) tokens_generated: Option<u64>,
    pub(crate) finished: bool,
}

pub(crate) fn grpc_router(state: AppState) -> axum::Router {
    let service =
        proto::frontend_stats_server::FrontendStatsServer::new(MockFrontendStats { state });
    tonic::service::Routes::new(service).into_axum_router()
}

pub(crate) async fn stats_stream(State(state): State<AppState>) -> Response {
    if !state.stats_stream_enabled.load(Ordering::Relaxed) {
        return StatusCode::SERVICE_UNAVAILABLE.into_response();
    }
    let source = mixed_stats_stream(state);
    let stream = async_stream::stream! {
        futures::pin_mut!(source);
        while let Some(update) = futures::StreamExt::next(&mut source).await {
            yield Ok::<Bytes, Infallible>(ndjson_event(update));
        }
    };
    let mut response = Response::new(Body::from_stream(stream));
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/x-ndjson"),
    );
    response
}

fn mixed_stats_stream(state: AppState) -> impl Stream<Item = proto::StatsUpdate> + Send + 'static {
    let mut events = state.stats_events.subscribe();
    async_stream::stream! {
        let mut snapshots = tokio::time::interval(Duration::from_secs(1));
        snapshots.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);
        let mut snapshot_id = 1_u64;
        loop {
            if !state.stats_stream_enabled.load(Ordering::Relaxed) {
                break;
            }
            tokio::select! {
                event = events.recv() => match event {
                    Ok(event) => yield request_update(event),
                    Err(broadcast::error::RecvError::Lagged(_)) => break,
                    Err(broadcast::error::RecvError::Closed) => break,
                },
                _ = snapshots.tick() => {
                    let stats = state.kv_cache.lock().await.stats(&state.model_name);
                    yield kv_update(snapshot_id, stats);
                    snapshot_id = snapshot_id.saturating_add(1);
                }
            }
        }
    }
}

fn request_update(event: StatsStreamEvent) -> proto::StatsUpdate {
    proto::StatsUpdate {
        update: Some(proto::stats_update::Update::RequestStats(
            proto::RequestStats {
                request_id: event.request_id,
                model: event.model,
                tokens_processed: event.tokens_processed,
                tokens_generated: event.tokens_generated,
                finished: event.finished,
            },
        )),
    }
}

fn kv_update(snapshot_id: u64, stats: crate::kv_cache::KvCacheStats) -> proto::StatsUpdate {
    proto::StatsUpdate {
        update: Some(proto::stats_update::Update::KvStats(
            proto::KvStatsSnapshot {
                snapshot_id,
                observed_at_unix_ms: crate::openai::unix_millis(),
                models: vec![proto::ModelKvStats {
                    model: stats.model,
                    aliases: Vec::new(),
                    routing_cache: Some(proto::RoutingCacheStats {
                        role: proto::WorkerRole::Aggregated as i32,
                        capacity_tokens: stats.kv_cache_capacity_tokens,
                        used_tokens: stats.kv_cache_used_tokens,
                        free_tokens: stats.kv_cache_free_tokens,
                    }),
                    pools: Vec::new(),
                }],
            },
        )),
    }
}

pub(crate) fn ndjson_event(update: proto::StatsUpdate) -> Bytes {
    let value = match update
        .update
        .expect("mock stats update must have a payload")
    {
        proto::stats_update::Update::RequestStats(event) => serde_json::json!({
            "v": 1,
            "type": "stats",
            "request_id": event.request_id,
            "model": event.model,
            "tokens_processed": event.tokens_processed,
            "tokens_generated": event.tokens_generated,
            "finished": event.finished,
        }),
        proto::stats_update::Update::KvStats(snapshot) => serde_json::json!({
            "v": 1,
            "type": "kv_stats_snapshot",
            "snapshot_id": snapshot.snapshot_id,
            "observed_at_unix_ms": snapshot.observed_at_unix_ms,
            "models": snapshot.models.into_iter().map(|model| serde_json::json!({
                "model": model.model,
                "aliases": model.aliases,
                "routing_cache": model.routing_cache.map(|cache| serde_json::json!({
                    "role": "aggregated",
                    "capacity_tokens": cache.capacity_tokens,
                    "used_tokens": cache.used_tokens,
                    "free_tokens": cache.free_tokens,
                })),
                "pools": [],
            })).collect::<Vec<_>>(),
        }),
    };
    let mut line = serde_json::to_vec(&value).expect("mock stats update should serialize");
    line.push(b'\n');
    Bytes::from(line)
}

#[derive(Clone)]
struct MockFrontendStats {
    state: AppState,
}

#[tonic::async_trait]
impl proto::frontend_stats_server::FrontendStats for MockFrontendStats {
    type WatchStatsStream =
        Pin<Box<dyn Stream<Item = Result<proto::StatsUpdate, Status>> + Send + 'static>>;
    type WatchKvPlacementsStream =
        Pin<Box<dyn Stream<Item = Result<proto::KvPlacementUpdate, Status>> + Send + 'static>>;

    async fn watch_stats(
        &self,
        _request: Request<proto::WatchStatsRequest>,
    ) -> Result<GrpcResponse<Self::WatchStatsStream>, Status> {
        if !self.state.stats_stream_enabled.load(Ordering::Relaxed) {
            return Err(Status::unavailable("mock stats stream disabled"));
        }
        let stream = futures::StreamExt::map(mixed_stats_stream(self.state.clone()), Ok);
        Ok(GrpcResponse::new(Box::pin(stream)))
    }

    async fn watch_kv_placements(
        &self,
        _request: Request<proto::WatchKvPlacementsRequest>,
    ) -> Result<GrpcResponse<Self::WatchKvPlacementsStream>, Status> {
        Err(Status::unimplemented(
            "mock Dynamo does not model KV placements",
        ))
    }
}

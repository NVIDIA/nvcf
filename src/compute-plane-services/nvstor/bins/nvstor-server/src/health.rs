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

//! Health, readiness, and status endpoints.
//!
//! Per ARCHITECTURE.md:
//! - /healthz — liveness (process alive)
//! - /readyz  — readiness (ready for traffic)
//! - /status  — deep status with storage stats
//! - /metrics — Prometheus text format

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Instant;

use axum::extract::State;
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::routing::get;
use axum::{Json, Router};
use nvstor_storage::manager::ChunkManager;

/// Shared state for the health server.
pub struct HealthState {
    pub mgr: Arc<ChunkManager>,
    pub metrics: Arc<crate::metrics::Metrics>,
    pub ready: Arc<AtomicBool>,
    pub start_time: Instant,
    pub node_name: String,
    pub cluster_name: String,
    pub distributed: Option<Arc<nvstor_placement::distributed::DistributedManager>>,
    pub raft_enabled: bool,
}

/// Build the health/metrics HTTP router.
pub fn router(state: Arc<HealthState>) -> Router {
    Router::new()
        .route("/healthz", get(healthz))
        .route("/readyz", get(readyz))
        .route("/status", get(status))
        .route("/metrics", get(prometheus_metrics))
        .merge(crate::admin::router(state.clone()))
        .with_state(state)
}

/// Liveness probe — lightweight, confirms process is alive.
async fn healthz() -> &'static str {
    "ok"
}

/// Readiness probe — confirms the server can accept traffic.
async fn readyz(State(state): State<Arc<HealthState>>) -> impl IntoResponse {
    if state.ready.load(Ordering::Relaxed) {
        (StatusCode::OK, "ready")
    } else {
        (StatusCode::SERVICE_UNAVAILABLE, "not ready")
    }
}

/// Deep status endpoint — structured JSON with component health and storage stats.
async fn status(State(state): State<Arc<HealthState>>) -> impl IntoResponse {
    let uptime = state.start_time.elapsed().as_secs();

    let storage = match state.mgr.stats() {
        Ok(stats) => serde_json::json!({
            "chunks": stats.total_chunks,
            "total_bytes": stats.total_bytes,
            "unique_bytes": stats.unique_bytes,
            "objects": stats.total_objects,
            "volumes": stats.total_volumes,
        }),
        Err(_) => serde_json::json!({"error": "unavailable"}),
    };

    let cluster = if let Some(ref dm) = state.distributed {
        let h = dm.health();
        serde_json::json!({
            "total_nodes": h.total_nodes,
            "active_nodes": h.active_nodes,
            "epoch": h.epoch,
            "policy": h.policy,
        })
    } else {
        serde_json::json!({"mode": "standalone"})
    };

    let body = serde_json::json!({
        "status": if state.ready.load(Ordering::Relaxed) { "ready" } else { "starting" },
        "version": env!("CARGO_PKG_VERSION"),
        "node": state.node_name,
        "cluster": state.cluster_name,
        "uptime_seconds": uptime,
        "storage": storage,
        "cluster_health": cluster,
    });

    (StatusCode::OK, Json(body))
}

/// Prometheus metrics endpoint.
async fn prometheus_metrics(State(state): State<Arc<HealthState>>) -> impl IntoResponse {
    // Update storage gauges before rendering.
    if let Ok(stats) = state.mgr.stats() {
        state.metrics.update_storage_stats(&stats);
    }
    let body = state.metrics.render();
    (
        StatusCode::OK,
        [("content-type", "text/plain; version=0.0.4; charset=utf-8")],
        body,
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_health_state_creation() {
        let _state = HealthState {
            mgr: Arc::new(test_manager()),
            metrics: Arc::new(crate::metrics::Metrics::new()),
            ready: Arc::new(AtomicBool::new(true)),
            start_time: Instant::now(),
            node_name: "test-node".into(),
            cluster_name: "test-cluster".into(),
            distributed: None,
            raft_enabled: false,
        };
    }

    fn test_manager() -> ChunkManager {
        let dir = tempfile::tempdir().unwrap();
        let backend =
            nvstor_storage::backend::PosixBackend::new(dir.path().join("chunks")).unwrap();
        let meta = nvstor_metadata::MetadataStore::open(&dir.path().join("meta")).unwrap();
        // Leak the tempdir so it lives long enough for the test
        let _dir = Box::leak(Box::new(dir));
        ChunkManager::new(Box::new(backend), Arc::new(meta), Default::default())
    }
}

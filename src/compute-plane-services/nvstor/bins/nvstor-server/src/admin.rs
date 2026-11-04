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

//! Admin API — cluster management and diagnostics endpoints.
//!
//! Provides REST endpoints for operators on the health port (:9101):
//! - GET /admin/cluster — cluster status
//! - GET /admin/storage — storage statistics
//! - GET /admin/raft — Raft consensus state
//! - POST /admin/gc — trigger GC sweep
//! - POST /admin/scrub — trigger integrity scrub

use std::sync::Arc;

use axum::extract::State;
use axum::http::StatusCode;
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::{Json, Router};
/// Build the admin API router.
pub fn router(state: Arc<crate::health::HealthState>) -> Router<Arc<crate::health::HealthState>> {
    Router::new()
        .route("/admin/cluster", get(cluster_status))
        .route("/admin/storage", get(storage_stats))
        .route("/admin/gc", post(trigger_gc))
        .route("/admin/scrub", post(trigger_scrub))
        .with_state(state)
}

/// GET /admin/cluster — cluster overview.
async fn cluster_status(State(state): State<Arc<crate::health::HealthState>>) -> impl IntoResponse {
    let uptime = state.start_time.elapsed().as_secs();

    let body = serde_json::json!({
        "node": state.node_name,
        "cluster": state.cluster_name,
        "uptime_seconds": uptime,
        "raft_enabled": state.raft_enabled,
        "version": env!("CARGO_PKG_VERSION"),
    });

    (StatusCode::OK, Json(body))
}

/// GET /admin/storage — detailed storage statistics.
async fn storage_stats(State(state): State<Arc<crate::health::HealthState>>) -> impl IntoResponse {
    match state.mgr.stats() {
        Ok(stats) => {
            let body = serde_json::json!({
                "chunks": stats.total_chunks,
                "objects": stats.total_objects,
                "volumes": stats.total_volumes,
                "total_bytes": stats.total_bytes,
                "unique_bytes": stats.unique_bytes,
            });
            (StatusCode::OK, Json(body))
        }
        Err(e) => {
            let body = serde_json::json!({"error": e.to_string()});
            (StatusCode::INTERNAL_SERVER_ERROR, Json(body))
        }
    }
}

/// POST /admin/gc — trigger a garbage collection sweep.
async fn trigger_gc(State(state): State<Arc<crate::health::HealthState>>) -> impl IntoResponse {
    match state.mgr.gc_sweep() {
        Ok(report) => {
            let body = serde_json::json!({
                "chunks_deleted": report.chunks_deleted,
                "bytes_reclaimed": report.bytes_reclaimed,
                "duration_ms": report.duration_ms,
                "errors": report.errors,
            });
            (StatusCode::OK, Json(body))
        }
        Err(e) => {
            let body = serde_json::json!({"error": e.to_string()});
            (StatusCode::INTERNAL_SERVER_ERROR, Json(body))
        }
    }
}

/// POST /admin/scrub — trigger an integrity check.
async fn trigger_scrub(State(state): State<Arc<crate::health::HealthState>>) -> impl IntoResponse {
    match state.mgr.scrub(10000) {
        Ok(report) => {
            let body = serde_json::json!({
                "chunks_checked": report.chunks_checked,
                "checksum_failures": report.checksum_failures,
                "missing_chunks": report.missing_chunks,
                "orphaned_chunks": report.orphaned_chunks,
                "duration_ms": report.duration_ms,
            });
            (StatusCode::OK, Json(body))
        }
        Err(e) => {
            let body = serde_json::json!({"error": e.to_string()});
            (StatusCode::INTERNAL_SERVER_ERROR, Json(body))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn admin_state_creation() {
        let _state = crate::health::HealthState {
            mgr: Arc::new(test_manager()),
            metrics: Arc::new(crate::metrics::Metrics::new()),
            ready: Arc::new(std::sync::atomic::AtomicBool::new(true)),
            node_name: "test".into(),
            cluster_name: "cluster".into(),
            start_time: std::time::Instant::now(),
            distributed: None,
            raft_enabled: false,
        };
    }

    fn test_manager() -> nvstor_storage::manager::ChunkManager {
        let dir = tempfile::tempdir().expect("tempdir");
        let backend =
            nvstor_storage::backend::PosixBackend::new(dir.path().join("chunks")).expect("backend");
        let meta = nvstor_metadata::MetadataStore::open(&dir.path().join("meta")).expect("meta");
        let _dir = Box::leak(Box::new(dir));
        nvstor_storage::manager::ChunkManager::new(
            Box::new(backend),
            std::sync::Arc::new(meta),
            Default::default(),
        )
    }
}

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

use std::net::SocketAddr;
use std::sync::Arc;

use axum::Router;
use axum::extract::State;
use axum::http::StatusCode;
use axum::routing::get;
use tokio::net::TcpListener;
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;

use crate::endpoints::TargetSnapshot;
use crate::metrics::RouterMetrics;

#[derive(Clone)]
struct HealthState {
    targets: watch::Receiver<TargetSnapshot>,
    metrics: Arc<RouterMetrics>,
}

pub async fn serve_health(
    listen_addr: SocketAddr,
    targets: watch::Receiver<TargetSnapshot>,
    metrics: Arc<RouterMetrics>,
    shutdown: CancellationToken,
) -> anyhow::Result<()> {
    let listener = TcpListener::bind(listen_addr).await?;
    axum::serve(listener, health_router(targets, metrics))
        .with_graceful_shutdown(async move {
            shutdown.cancelled().await;
        })
        .await?;
    Ok(())
}

pub fn health_router(
    targets: watch::Receiver<TargetSnapshot>,
    metrics: Arc<RouterMetrics>,
) -> Router {
    Router::new()
        .route("/livez", get(livez))
        .route("/readyz", get(readyz))
        .route("/metrics", get(metrics_handler))
        .with_state(HealthState { targets, metrics })
}

async fn livez() -> StatusCode {
    StatusCode::OK
}

async fn readyz(State(state): State<HealthState>) -> StatusCode {
    let snapshot = state.targets.borrow();
    if snapshot.is_initialized() && snapshot.ready_count() > 0 {
        StatusCode::OK
    } else {
        StatusCode::SERVICE_UNAVAILABLE
    }
}

async fn metrics_handler(State(state): State<HealthState>) -> (StatusCode, String) {
    match state.metrics.gather() {
        Ok(body) => (StatusCode::OK, body),
        Err(error) => (StatusCode::INTERNAL_SERVER_ERROR, error.to_string()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::endpoints::PodTarget;
    use axum::body::Body;
    use http::{Request, StatusCode};
    use tower::ServiceExt;

    #[tokio::test]
    async fn readyz_requires_initialized_nonempty_snapshot() {
        let metrics = Arc::new(RouterMetrics::new().expect("metrics should initialize"));
        let (tx, rx) = watch::channel(TargetSnapshot::default());
        let router = health_router(rx, metrics);
        let ready_request = || {
            Request::builder()
                .uri("/readyz")
                .body(Body::empty())
                .expect("request should build")
        };

        let response = router
            .clone()
            .oneshot(ready_request())
            .await
            .expect("readiness request should succeed");
        assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);

        tx.send(TargetSnapshot::initialized([]))
            .expect("initialized empty snapshot should publish");
        let response = router
            .clone()
            .oneshot(ready_request())
            .await
            .expect("readiness request should succeed");
        assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);

        tx.send(TargetSnapshot::initialized([PodTarget {
            pod_name: "stargate-0".to_string(),
            grpc_addr: "10.0.0.10:50071".to_string(),
            quic_addr: "10.0.0.10:50072".to_string(),
        }]))
        .expect("ready snapshot should publish");
        let response = router
            .oneshot(ready_request())
            .await
            .expect("readiness request should succeed");
        assert_eq!(response.status(), StatusCode::OK);
    }

    #[tokio::test]
    async fn metrics_endpoint_exports_router_metrics() {
        let metrics = Arc::new(RouterMetrics::new().expect("metrics should initialize"));
        metrics.observe_quic_connection("accepted");
        let (_tx, rx) = watch::channel(TargetSnapshot::default());
        let response = health_router(rx, metrics)
            .oneshot(
                Request::builder()
                    .uri("/metrics")
                    .body(Body::empty())
                    .expect("request should build"),
            )
            .await
            .expect("metrics request should succeed");

        assert_eq!(response.status(), StatusCode::OK);
        let body = axum::body::to_bytes(response.into_body(), usize::MAX)
            .await
            .expect("body should be readable");
        let body = std::str::from_utf8(&body).expect("metrics should be utf8");
        assert!(
            body.contains(r#"stargate_k8s_router_quic_connections_total{outcome="accepted"} 1"#)
        );
    }
}

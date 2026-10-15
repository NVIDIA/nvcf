/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

use crate::health::{ComponentHealth, Health, HealthStatus as HealthState};
use axum::extract::State;
use axum::http::StatusCode;
use axum::response::Json;
use serde::Serialize;
use std::collections::HashMap;
use std::sync::Arc;

#[derive(Debug, Serialize)]
pub struct HealthResponse {
    status: String,
    message: Option<String>,
    last_updated: u64,
    components: HashMap<String, ComponentHealthResponse>,
}

#[derive(Debug, Serialize)]
pub struct ComponentHealthResponse {
    status: String,
    message: Option<String>,
    last_updated: u64,
}

impl From<ComponentHealth> for ComponentHealthResponse {
    fn from(component: ComponentHealth) -> Self {
        let status_str = match component.status {
            HealthState::Healthy => "healthy",
            HealthState::Unhealthy => "unhealthy",
        };

        Self {
            status: status_str.to_string(),
            message: component.message,
            last_updated: component.last_updated,
        }
    }
}

/// Liveness: process is alive. No dependency checks.
/// Used by Kubernetes liveness probe. Failure causes pod restart.
/// We intentionally do not check TimeseriesDb/Cassandra here — restarting when they're
/// unreachable does not help; the pod should stay up.
pub async fn get_liveness() -> (StatusCode, &'static str) {
    (StatusCode::OK, "ok")
}

/// Readiness: process can do useful work (dependencies healthy).
/// Used by Kubernetes readiness probe. Failure only stops traffic, no restart.
/// Includes Cassandra and TimeseriesDb; if e.g. TimeseriesDb is unreachable we report not ready.
pub async fn get_readiness(
    State(health): State<Arc<Health>>,
) -> (StatusCode, Json<HealthResponse>) {
    let health_info = health.get_health();

    let (status_code, status_str) = match health_info.overall_status {
        HealthState::Healthy => (StatusCode::OK, "healthy"),
        HealthState::Unhealthy => (StatusCode::SERVICE_UNAVAILABLE, "unhealthy"),
    };

    let components: HashMap<String, ComponentHealthResponse> = health_info
        .components
        .into_iter()
        .map(|(name, component)| (name, ComponentHealthResponse::from(component)))
        .collect();

    (
        status_code,
        Json(HealthResponse {
            status: status_str.to_string(),
            message: health_info.message,
            last_updated: health_info.last_updated,
            components,
        }),
    )
}

/// Legacy overall health (same semantics as readiness).
pub async fn get_health(state: State<Arc<Health>>) -> (StatusCode, Json<HealthResponse>) {
    get_readiness(state).await
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::health::Health;
    use axum::body::to_bytes;
    use axum::http::header::CONTENT_TYPE;
    use axum::response::IntoResponse;

    #[tokio::test]
    async fn liveness_is_always_ok_even_when_cassandra_not_yet_up() {
        // Liveness must never check dependencies — restarting the pod doesn't help
        // if Cassandra is down.
        let health = Arc::new(Health::new());
        health.register_component("cassandra_client"); // initializes as Unhealthy

        let (status, _) = get_liveness().await;
        assert_eq!(status, StatusCode::OK);
    }

    #[tokio::test]
    async fn readiness_returns_503_before_cassandra_connects() {
        // register_component initializes as Unhealthy, matching server.rs startup order.
        let health = Arc::new(Health::new());
        health.register_component("cassandra_client");

        let (status, _) = get_readiness(State(health)).await;
        assert_eq!(status, StatusCode::SERVICE_UNAVAILABLE);
    }

    #[tokio::test]
    async fn readiness_returns_200_once_all_components_healthy() {
        let health = Arc::new(Health::new());
        health.register_component("cassandra_client");
        health.register_component("timeseries_db_client");
        health.set_component_healthy("cassandra_client", Some("connected".to_string()));
        health.set_component_healthy("timeseries_db_client", Some("connected".to_string()));

        let (status, _) = get_readiness(State(health)).await;
        assert_eq!(status, StatusCode::OK);
    }

    #[tokio::test]
    async fn health_returns_503_with_unhealthy_component_details() {
        let health = Arc::new(Health::new());
        health.register_component("cassandra_client");
        health.register_component("timeseries_db_client");
        health.set_component_healthy("cassandra_client", Some("connected".to_string()));

        let response = get_health(State(health)).await.into_response();

        assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(
            response.headers().get(CONTENT_TYPE).unwrap(),
            "application/json"
        );

        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        let body: serde_json::Value = serde_json::from_slice(&body).unwrap();

        assert_eq!(body["status"], "unhealthy");
        assert_eq!(
            body["message"],
            "1 component(s) unhealthy: timeseries_db_client: initializing"
        );
        assert!(body["last_updated"].is_u64());
        assert_eq!(body["components"].as_object().unwrap().len(), 2);
        assert_eq!(
            body["components"]["timeseries_db_client"]["status"],
            "unhealthy"
        );
        assert!(body["components"]["timeseries_db_client"]["last_updated"].is_u64());
        assert_eq!(
            body["components"]["timeseries_db_client"]["message"],
            "initializing"
        );
        assert_eq!(body["components"]["cassandra_client"]["status"], "healthy");
        assert!(body["components"]["cassandra_client"]["last_updated"].is_u64());
    }
}

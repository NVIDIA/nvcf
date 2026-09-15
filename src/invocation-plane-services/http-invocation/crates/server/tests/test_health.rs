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

mod mocks;

use axum::body::Body;
use axum::response::Response;
use axum::Router;
use http::{Method, StatusCode};
use mocks::{fixtures, nvcf_api_mock::HEALTH_CACHE_TTL};
use nvcf_invocation_service::app::app;
use std::time::Duration;
use tower::{Service, ServiceExt};

#[tokio::test]
async fn test_health_cache() -> anyhow::Result<()> {
    let (_localstack, _nats, mock_nvcf_api, config) = fixtures().await;

    let mut app = app(config, None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    mock_nvcf_api.set_unhealthy().await;
    let response = call_health(app).await?;
    assert_eq!(response.status(), StatusCode::INTERNAL_SERVER_ERROR);
    mock_nvcf_api.set_healthy().await;
    let response = call_health(app).await?;
    assert_eq!(response.status(), StatusCode::OK);
    mock_nvcf_api.set_unhealthy().await;
    // Response from cache still says healthy
    let response = call_health(app).await?;
    assert_eq!(response.status(), StatusCode::OK);
    // Checking again after nvcf_api health cache expires, should return unhealthy
    tokio::time::sleep(Duration::from_secs(HEALTH_CACHE_TTL.into())).await;
    let response = call_health(app).await?;
    assert_eq!(response.status(), StatusCode::INTERNAL_SERVER_ERROR);
    Ok(())
}

async fn call_health(app: &mut Router) -> anyhow::Result<Response> {
    let request = axum::http::Request::builder()
        .method(Method::GET)
        .uri("/health")
        .body(Body::empty())?;
    let response = app.call(request).await?;
    Ok(response)
}

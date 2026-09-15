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

//! This block of tests is designed to ensure that browsers' "preflight" CORS checks - which have
//! distinct characteristics from the subsequent requests to the service itself - will always work.
//! Preflight checks do not typically include headers for Authorization, Content-type, etc.
mod mocks;

use axum::{
    body::Body,
    http::{self, Method, Request},
    Router,
};
use mocks::{
    fixtures,
    nvcf_worker_mock::{DefaultWorkHandler, PublishMode, Worker, WorkerProperties},
    FUNCTION_ID, INSTANCE_ID, VERSION_ID_1,
};
use nvcf_invocation_service::app::app;
use tower::{Service, ServiceExt};

#[tokio::test]
async fn test_pexec_cors() -> anyhow::Result<()> {
    let uri = format!("/v2/nvcf/pexec/functions/{FUNCTION_ID}");

    let mut app = hydrate_fixtures().await?;
    let request = build_request(uri, "POST".to_string());
    let response = app.call(request).await?;

    assert_eq!(response.status().as_u16(), 200);
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_ALLOW_CREDENTIALS)
            .unwrap(),
        "true"
    );
    assert_eq!(
        response.headers().get(http::header::VARY).unwrap(),
        "origin, access-control-request-method, access-control-request-headers"
    );
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_MAX_AGE)
            .unwrap(),
        "86400"
    );
    assert_eq!(response.headers().get(http::header::ALLOW).unwrap(), "POST");

    Ok(())
}

#[tokio::test]
async fn test_pexec_version_id_cors() -> anyhow::Result<()> {
    let uri = format!("/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}");

    let mut app = hydrate_fixtures().await?;
    let request = build_request(uri, "POST".to_string());
    let response = app.call(request).await?;

    assert_eq!(response.status().as_u16(), 200);
    assert_eq!(
        response
            .headers()
            .get("Access-Control-Allow-Credentials")
            .unwrap(),
        "true"
    );
    assert_eq!(
        response.headers().get(http::header::VARY).unwrap(),
        "origin, access-control-request-method, access-control-request-headers"
    );
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_MAX_AGE)
            .unwrap(),
        "86400"
    );
    assert_eq!(response.headers().get(http::header::ALLOW).unwrap(), "POST");

    Ok(())
}

#[tokio::test]
async fn test_exec_cors() -> anyhow::Result<()> {
    let uri = format!("/v2/nvcf/exec/functions/{FUNCTION_ID}");

    let mut app = hydrate_fixtures().await?;
    let request = build_request(uri, "POST".to_string());
    let response = app.call(request).await?;

    assert_eq!(response.status().as_u16(), 200);
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_ALLOW_CREDENTIALS)
            .unwrap(),
        "true"
    );
    assert_eq!(
        response.headers().get(http::header::VARY).unwrap(),
        "origin, access-control-request-method, access-control-request-headers"
    );
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_MAX_AGE)
            .unwrap(),
        "86400"
    );
    assert_eq!(response.headers().get(http::header::ALLOW).unwrap(), "POST");

    Ok(())
}

#[tokio::test]
async fn test_exec_version_id_cors() -> anyhow::Result<()> {
    let uri = format!("/v2/nvcf/exec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}");

    let mut app = hydrate_fixtures().await?;
    let request = build_request(uri, "POST".to_string());
    let response = app.call(request).await?;

    assert_eq!(response.status().as_u16(), 200);
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_ALLOW_CREDENTIALS)
            .unwrap(),
        "true"
    );
    assert_eq!(
        response.headers().get(http::header::VARY).unwrap(),
        "origin, access-control-request-method, access-control-request-headers"
    );
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_MAX_AGE)
            .unwrap(),
        "86400"
    );
    assert_eq!(response.headers().get(http::header::ALLOW).unwrap(), "POST");

    Ok(())
}

#[tokio::test]
async fn test_pexec_status_cors() -> anyhow::Result<()> {
    let uri = "/v2/nvcf/pexec/status/1234".to_string();

    let mut app = hydrate_fixtures().await?;
    let request = build_request(uri, "GET".to_string());
    let response = app.call(request).await?;

    assert_eq!(response.status().as_u16(), 200);
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_ALLOW_CREDENTIALS)
            .unwrap(),
        "true"
    );
    assert_eq!(
        response.headers().get(http::header::VARY).unwrap(),
        "origin, access-control-request-method, access-control-request-headers"
    );
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_MAX_AGE)
            .unwrap(),
        "86400"
    );
    assert_eq!(
        response.headers().get(http::header::ALLOW).unwrap(),
        "GET,HEAD"
    );

    Ok(())
}

#[tokio::test]
async fn test_exec_status_cors() -> anyhow::Result<()> {
    let uri = "/v2/nvcf/exec/status/1234".to_string();

    let mut app = hydrate_fixtures().await?;
    let request = build_request(uri, "GET".to_string());
    let response = app.call(request).await?;

    assert_eq!(response.status().as_u16(), 200);
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_ALLOW_CREDENTIALS)
            .unwrap(),
        "true"
    );
    assert_eq!(
        response.headers().get(http::header::VARY).unwrap(),
        "origin, access-control-request-method, access-control-request-headers"
    );
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_MAX_AGE)
            .unwrap(),
        "86400"
    );
    assert_eq!(
        response.headers().get(http::header::ALLOW).unwrap(),
        "GET,HEAD"
    );

    Ok(())
}

async fn hydrate_fixtures() -> anyhow::Result<Router> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;
    let mut app = app(config.clone(), None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;
    let _worker = Worker::new(
        config.nats_properties.clone(),
        WorkerProperties {
            function_id: FUNCTION_ID,
            function_version_id: VERSION_ID_1,
            instance_id: INSTANCE_ID.into(),
        },
        Box::new(DefaultWorkHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    Ok(app.to_owned())
}

fn build_request(uri: String, method: String) -> Request<Body> {
    axum::http::Request::builder()
        .method(Method::OPTIONS)
        .uri(uri)
        .header(http::header::ORIGIN, "http://localhost")
        .header(http::header::ACCESS_CONTROL_REQUEST_METHOD, method)
        .header(
            http::header::ACCESS_CONTROL_REQUEST_HEADERS,
            "authorization,content-type",
        )
        .body(Body::from(""))
        .unwrap()
}

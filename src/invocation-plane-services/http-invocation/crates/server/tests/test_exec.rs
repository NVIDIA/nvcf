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

use crate::mocks::nvcf_worker_mock::PublishMode;
use axum::body::Body;
use axum::http;
use axum::http::header::{AUTHORIZATION, CONTENT_TYPE};
use axum::http::{HeaderValue, Method, StatusCode};
use bytes::Bytes;
use futures::stream;
use http_body_util::BodyExt;
use mime::APPLICATION_JSON;
use mocks::{
    fixtures,
    nvcf_worker_mock::{EchoWorkHandler, PollAwareHandler, Worker, WorkerProperties},
    API_KEY, FUNCTION_ID, INSTANCE_ID, VERSION_ID_1,
};
use nvcf_invocation_service::app::app;
use nvcf_invocation_service::routes::{InvokeFunctionResponse, InvokeStatus};
use problem_details::ProblemDetails;
use std::time::Duration;
use tower::{Service, ServiceExt};

#[tokio::test]
async fn test_exec() -> anyhow::Result<()> {
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
        Box::new(EchoWorkHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/exec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header(CONTENT_TYPE, APPLICATION_JSON.essence_str())
        .body(Body::from(r#"{"requestBody":{"abc": 123}}"#))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_str(APPLICATION_JSON.essence_str())?)
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert!(!response_body.contains("null"));
    let response_body: InvokeFunctionResponse = serde_json::from_str(&response_body)?;
    assert_eq!(response_body.status, InvokeStatus::Fulfilled);
    assert_eq!(response_body.percent_complete, Some(100));
    assert_eq!(
        response_body.response.unwrap().to_string(),
        r#"{"abc":123}"#
    );
    Ok(())
}

#[tokio::test]
async fn test_exec_poll() -> anyhow::Result<()> {
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
        Box::new(PollAwareHandler {
            sleep_time: Duration::from_secs(2),
            enable_echo_request: true,
        }),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/exec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header(CONTENT_TYPE, APPLICATION_JSON.essence_str())
        .body(Body::from(
            r#"{"requestHeader":{"pollDurationSeconds":1},"requestBody":{"abc": 123}}"#,
        ))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::ACCEPTED);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_str(APPLICATION_JSON.essence_str())?)
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert!(!response_body.contains("null"));
    let response_body: InvokeFunctionResponse = serde_json::from_str(&response_body)?;
    assert_eq!(response_body.status, InvokeStatus::InProgress);
    let request_id = response_body.req_id;
    let request = axum::http::Request::builder()
        .method(Method::GET)
        .uri(format!("/v2/nvcf/exec/status/{request_id}"))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::empty())?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_str(APPLICATION_JSON.essence_str())?)
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert!(!response_body.contains("null"));
    let response_body: InvokeFunctionResponse = serde_json::from_str(&response_body)?;
    assert_eq!(response_body.status, InvokeStatus::Fulfilled);
    assert_eq!(response_body.percent_complete, Some(100));
    assert_eq!(
        response_body.response.unwrap().to_string(),
        r#"{"abc":123}"#
    );
    Ok(())
}

#[tokio::test]
async fn test_exec_bad_body() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;

    let mut app = app(config, None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/exec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header(CONTENT_TYPE, APPLICATION_JSON.essence_str())
        .body(Body::from("{}"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    Ok(())
}

#[tokio::test]
async fn test_exec_body_collect_error() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;

    let mut app = app(config, None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let error_body = Body::from_stream(stream::once(async {
        Err::<Bytes, std::io::Error>(std::io::Error::other("body stream failed"))
    }));

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/exec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header(CONTENT_TYPE, APPLICATION_JSON.essence_str())
        .body(error_body)?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    let response_body = response.into_body().collect().await?.to_bytes();
    let problem: ProblemDetails = serde_json::from_slice(&response_body)?;
    assert!(problem
        .detail
        .unwrap_or_default()
        .starts_with("failed to collect request body:"));
    Ok(())
}

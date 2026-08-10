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
use axum::body::{Body, HttpBody};
use axum::http;
use axum::http::header::{AUTHORIZATION, CONTENT_TYPE};
use axum::http::{HeaderValue, Method, StatusCode};
use http_body_util::BodyExt;
use mocks::{
    fixtures,
    nvcf_worker_mock::{PollAwareHandler, Worker, WorkerProperties},
    API_KEY, FUNCTION_ID, INSTANCE_ID, VERSION_ID_1,
};
use nvcf_invocation_service::app::app;
use std::collections::HashMap;
use std::time::Duration;
use tokio::time::timeout;
use tower::{Service, ServiceExt};

#[tokio::test]
async fn test_poll() -> anyhow::Result<()> {
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
            enable_echo_request: false,
        }),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header("nvcf-poll-seconds", "1")
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::ACCEPTED);
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("in-progress"))
    );
    assert_eq!(
        response.headers().get(http::header::CONTENT_LENGTH),
        Some(&HeaderValue::from_static("0"))
    );
    assert_eq!(response.body().size_hint().exact(), Some(0));
    let request_id = response
        .headers()
        .get("nvcf-reqid")
        .unwrap()
        .to_str()?
        .to_string();
    // the mock worker will wait until we consume the response body before moving onto the next request.
    drop(response);

    let request = axum::http::Request::builder()
        .method(Method::GET)
        .uri(format!("/v2/nvcf/pexec/status/{request_id}"))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::empty())?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_static("text/plain"))
    );
    assert_eq!(
        response.headers().get("nvcf-reqid"),
        Some(&HeaderValue::from_str(&request_id)?)
    );
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("fulfilled"))
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");
    Ok(())
}

#[tokio::test]
async fn test_worker_responds_while_client_is_not_polling() -> anyhow::Result<()> {
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
            enable_echo_request: false,
        }),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header("nvcf-poll-seconds", "1")
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::ACCEPTED);
    let request_id = response
        .headers()
        .get("nvcf-reqid")
        .unwrap()
        .to_str()?
        .to_string();
    drop(response);
    let request = axum::http::Request::builder()
        .method(Method::GET)
        .uri(format!("/v2/nvcf/pexec/status/{request_id}"))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::empty())?;
    tokio::time::sleep(Duration::from_secs(2)).await;
    let response = timeout(Duration::from_secs(2), app.call(request)).await??;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_static("text/plain"))
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");
    Ok(())
}

#[tokio::test]
async fn test_bad_poll_header() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;

    let mut app = app(config, None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header("nvcf-poll-seconds", "10000")
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = timeout(Duration::from_secs(1), app.call(request)).await??;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);

    Ok(())
}

#[tokio::test]
async fn test_cross_region_poll() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;
    let mut nats_properties_region_1 = config.nats_properties.clone();
    nats_properties_region_1.region = "region_1".into();
    nats_properties_region_1.other_regions = vec!["region_2".into()];
    let mut config_region_1 = config.clone();
    config_region_1.nats_properties = nats_properties_region_1.clone();
    config_region_1.worker_stream_properties.self_address = Some("server_1".into());

    let mut nats_properties_region_2 = config.nats_properties.clone();
    nats_properties_region_2.region = "region_2".into();
    // putting region 3 first since it doesn't exist so we can check parallel lookups
    nats_properties_region_2.other_regions = vec!["region_3".into(), "region_1".into()];
    let mut config_region_2 = config.clone();
    config_region_2.nats_properties = nats_properties_region_2.clone();
    config_region_2.worker_stream_properties.self_address = Some("server_2".into());

    let mut app_region_1 = app(config_region_1.clone(), None).await?;
    let app_region_1 = ServiceExt::<http::Request<Body>>::ready(&mut app_region_1).await?;
    let mut app_region_2 = app(config_region_2.clone(), None).await?;
    let app_region_2 = ServiceExt::<http::Request<Body>>::ready(&mut app_region_2).await?;

    let _worker = Worker::new(
        nats_properties_region_1.clone(),
        WorkerProperties {
            function_id: FUNCTION_ID,
            function_version_id: VERSION_ID_1,
            instance_id: INSTANCE_ID.into(),
        },
        Box::new(PollAwareHandler {
            sleep_time: Duration::from_secs(2),
            enable_echo_request: false,
        }),
        PublishMode::AttachMultiRegion(HashMap::from([
            (
                config_region_1
                    .worker_stream_properties
                    .self_address
                    .clone()
                    .unwrap(),
                app_region_1.clone(),
            ),
            (
                config_region_2
                    .worker_stream_properties
                    .self_address
                    .clone()
                    .unwrap(),
                app_region_2.clone(),
            ),
        ])),
    )
    .await?
    .into_background_task();

    // send the initial request
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header("nvcf-poll-seconds", "1")
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app_region_1.call(request).await?;
    assert_eq!(response.status(), StatusCode::ACCEPTED);
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("in-progress"))
    );
    let request_id = response
        .headers()
        .get("nvcf-reqid")
        .unwrap()
        .to_str()?
        .to_string();
    drop(response);

    // verify another region can get the request status
    let request = axum::http::Request::builder()
        .method(Method::GET)
        .uri(format!("/v2/nvcf/pexec/status/{request_id}"))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header("nvcf-poll-seconds", "3")
        .body(Body::empty())?;

    let response = app_region_2.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_static("text/plain"))
    );
    assert_eq!(
        response.headers().get("nvcf-reqid"),
        Some(&HeaderValue::from_str(&request_id)?)
    );
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("fulfilled"))
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");

    // verify the request is cleaned up
    let request = axum::http::Request::builder()
        .method(Method::GET)
        .uri(format!("/v2/nvcf/pexec/status/{request_id}"))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header("nvcf-poll-seconds", "1")
        .body(Body::empty())?;
    let response = app_region_1.call(request).await?;
    assert_eq!(response.status(), StatusCode::NOT_FOUND);

    Ok(())
}

#[tokio::test]
async fn test_cross_region_poll_response_returned_while_client_not_present() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;
    let mut nats_properties_region_1 = config.nats_properties.clone();
    nats_properties_region_1.region = "region_1".into();
    nats_properties_region_1.other_regions = vec!["region_2".into()];
    let mut config_region_1 = config.clone();
    config_region_1.nats_properties = nats_properties_region_1.clone();
    config_region_1.worker_stream_properties.self_address = Some("server_1".into());

    let mut nats_properties_region_2 = config.nats_properties.clone();
    nats_properties_region_2.region = "region_2".into();
    // putting region 3 first since it doesn't exist so we can check parallel lookups
    nats_properties_region_2.other_regions = vec!["region_3".into(), "region_1".into()];
    let mut config_region_2 = config.clone();
    config_region_2.nats_properties = nats_properties_region_2.clone();
    config_region_2.worker_stream_properties.self_address = Some("server_2".into());

    let mut app_region_1 = app(config_region_1.clone(), None).await?;
    let app_region_1 = ServiceExt::<http::Request<Body>>::ready(&mut app_region_1).await?;
    let mut app_region_2 = app(config_region_2.clone(), None).await?;
    let app_region_2 = ServiceExt::<http::Request<Body>>::ready(&mut app_region_2).await?;

    let _worker = Worker::new(
        nats_properties_region_1.clone(),
        WorkerProperties {
            function_id: FUNCTION_ID,
            function_version_id: VERSION_ID_1,
            instance_id: INSTANCE_ID.into(),
        },
        Box::new(PollAwareHandler {
            sleep_time: Duration::from_secs(2),
            enable_echo_request: false,
        }),
        PublishMode::AttachMultiRegion(HashMap::from([
            (
                config_region_1
                    .worker_stream_properties
                    .self_address
                    .clone()
                    .unwrap(),
                app_region_1.clone(),
            ),
            (
                config_region_2
                    .worker_stream_properties
                    .self_address
                    .clone()
                    .unwrap(),
                app_region_2.clone(),
            ),
        ])),
    )
    .await?
    .into_background_task();

    // send the initial request
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header("nvcf-poll-seconds", "1")
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app_region_1.call(request).await?;
    assert_eq!(response.status(), StatusCode::ACCEPTED);
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("in-progress"))
    );
    let request_id = response
        .headers()
        .get("nvcf-reqid")
        .unwrap()
        .to_str()?
        .to_string();
    drop(response);

    // sleep long enough for the response to come back while the client is not there
    tokio::time::sleep(Duration::from_secs(3)).await;

    // verify another region can get the request status
    let request = axum::http::Request::builder()
        .method(Method::GET)
        .uri(format!("/v2/nvcf/pexec/status/{request_id}"))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header("nvcf-poll-seconds", "1")
        .body(Body::empty())?;

    let response = app_region_2.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_static("text/plain"))
    );
    assert_eq!(
        response.headers().get("nvcf-reqid"),
        Some(&HeaderValue::from_str(&request_id)?)
    );
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("fulfilled"))
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");

    // verify the request is cleaned up
    let request = axum::http::Request::builder()
        .method(Method::GET)
        .uri(format!("/v2/nvcf/pexec/status/{request_id}"))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header("nvcf-poll-seconds", "1")
        .body(Body::empty())?;
    let response = app_region_1.call(request).await?;
    assert_eq!(response.status(), StatusCode::NOT_FOUND);

    Ok(())
}

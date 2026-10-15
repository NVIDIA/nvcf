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

use anyhow::Error;
use axum::{
    body::Body,
    http::{self, header, HeaderValue, Method, StatusCode},
};
use bytes::Bytes;
use futures::{AsyncBufReadExt, TryStreamExt};
use http_body_util::BodyExt;
use mocks::{
    fixtures,
    nvcf_worker_mock::{
        JsonHttpRequest, PingPongAttachHandler, PublishMode, ReturnRequestHandler, Worker,
        WorkerProperties,
    },
    API_KEY, FUNCTION_ID, INSTANCE_ID, VERSION_ID_1,
};
use nvcf_invocation_service::app::app;
use nvcf_invocation_service::settings::AppConfig;
use problem_details::ProblemDetails;
use tokio_stream::wrappers::ReceiverStream;
use tower::{Service, ServiceExt};

#[tokio::test]
async fn test_passthrough() -> anyhow::Result<()> {
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
        Box::new(ReturnRequestHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    for method in [
        Method::POST,
        Method::GET,
        Method::OPTIONS,
        Method::PUT,
        Method::try_from("CUSTOM")?,
    ] {
        for path in [
            "",
            "/",
            "?query=123",
            "/?query=123",
            "/abc/123",
            "/abc/123?query=456",
        ] {
            tracing::info!(method = %method, path = path, "creating request");
            let request = axum::http::Request::builder()
                .method(&method)
                .uri(format!(
                    "https://{VERSION_ID_1}.{FUNCTION_ID}.example.nvidia.com{path}"
                ))
                .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
                .body(Body::from("a body"))?;
            tracing::info!(method = %method, path = path, "created request");
            let response = app.call(request).await?;
            assert_eq!(response.status(), StatusCode::OK);
            assert_eq!(
                response.headers().get(header::CONTENT_TYPE),
                Some(&HeaderValue::from_static("application/json"))
            );
            assert!(response.headers().get("nvcf-reqid").is_some());
            assert_eq!(
                response.headers().get("access-control-expose-headers"),
                Some(&HeaderValue::from_static("nvcf-reqid"))
            );
            assert_eq!(
                response.headers().get("nvcf-status"),
                Some(&HeaderValue::from_static("fulfilled"))
            );
            let response_body = response.into_body().collect().await?.to_bytes();
            let parsed_request: JsonHttpRequest = serde_json::from_slice(&response_body)?;
            assert_eq!(parsed_request.method, method.as_str());
            if !path.is_empty() {
                assert_eq!(parsed_request.path, path);
            } else {
                // https://www.w3.org/Protocols/rfc2616/rfc2616-sec5.html#sec5.1.2
                // Note that the absolute path cannot be empty; if none is present in the original
                // URI, it MUST be given as "/" (the server root).
                // A transparent proxy MUST NOT rewrite the "abs_path" part of the received
                // Request-URI when forwarding it to the next inbound server, except as noted above
                // to replace a null abs_path with "/".
                assert_eq!(parsed_request.path, "/");
            }
            assert_eq!(parsed_request.body, Some("a body".to_string()));
            tracing::info!(method = %method, path = path, "finished request");
        }
    }
    Ok(())
}

#[tokio::test]
async fn test_virtual_host() -> anyhow::Result<()> {
    // Virtual hosting allows a single IP address to serve multiple domain names based on the Host name
    // passed in the request header.  This test ensures that we are correctly parsing the Host name for
    // both the tlb and non-tlb use cases.
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
        Box::new(ReturnRequestHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    // Tlb route
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header(
            header::HOST,
            format!("{VERSION_ID_1}.{FUNCTION_ID}.example.nvidia.com"),
        )
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    assert!(response.headers().get("nvcf-reqid").is_some());
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("fulfilled"))
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let parsed_request: JsonHttpRequest = serde_json::from_slice(&response_body)?;
    assert_eq!(parsed_request.method, Method::POST.as_str());
    assert_eq!(parsed_request.path, "/");
    assert_eq!(parsed_request.body, Some("a body".to_string()));

    // Non-Tlb route
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header(header::HOST, "example.nvidia.com")
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header("function-id", FUNCTION_ID.to_string())
        .header("function-version-id", VERSION_ID_1.to_string())
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    assert!(response.headers().get("nvcf-reqid").is_some());
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("fulfilled"))
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let parsed_request: JsonHttpRequest = serde_json::from_slice(&response_body)?;
    assert_eq!(parsed_request.method, Method::POST.as_str());
    assert_eq!(parsed_request.path, "/");
    assert_eq!(parsed_request.body, Some("a body".to_string()));

    Ok(())
}

#[tokio::test]
async fn test_streaming_payload() -> anyhow::Result<()> {
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
        Box::new(ReturnRequestHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let large_body = "a".repeat(20 * 1024 * 1024);
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "https://{VERSION_ID_1}.{FUNCTION_ID}.example.nvidia.com"
        ))
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from(large_body.clone()))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get(header::CONTENT_TYPE),
        Some(&HeaderValue::from_static("application/json"))
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let parsed_request: JsonHttpRequest = serde_json::from_slice(&response_body)?;
    let parsed_response_body = parsed_request.body.unwrap();
    assert_eq!(parsed_response_body.len(), large_body.len());
    assert_eq!(parsed_response_body, large_body);
    Ok(())
}

#[tokio::test]
async fn test_bidi_streaming() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;
    bidi_streaming_test(config).await
}

#[tokio::test]
async fn test_bidi_streaming_fully_streamed() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, mut config) = fixtures().await;
    // stream full request means including the request headers and entire unbuffered body. usually we buffer the first 32k.
    config.worker_stream_properties.stream_full_request = true;
    bidi_streaming_test(config).await
}

async fn bidi_streaming_test(config: AppConfig) -> Result<(), Error> {
    let mut app = app(config.clone(), None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let _worker = Worker::new(
        config.nats_properties.clone(),
        WorkerProperties {
            function_id: FUNCTION_ID,
            function_version_id: VERSION_ID_1,
            instance_id: INSTANCE_ID.into(),
        },
        Box::new(PingPongAttachHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let (tx, rx) = tokio::sync::mpsc::channel(1);
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "https://{VERSION_ID_1}.{FUNCTION_ID}.example.nvidia.com"
        ))
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from_stream(ReceiverStream::from(rx)))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    tracing::info!("got response headers");
    let mut read = TryStreamExt::map_err(
        response.into_body().into_data_stream(),
        std::io::Error::other,
    )
    .into_async_read()
    .lines();
    while let Some(line) = read.try_next().await? {
        assert_eq!(line, "ping");
        tracing::info!("client got ping");
        tracing::info!("client sending pong");
        tx.send(Ok::<_, std::io::Error>(Bytes::from_static(b"pong\n")))
            .await?;
    }
    Ok(())
}

#[tokio::test]
async fn test_failed_request_attach() -> Result<(), Error> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;
    let mut app = app(config.clone(), None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    for method in [Method::POST, Method::GET] {
        let request = axum::http::Request::builder()
            .method(method)
            .uri("/v2/nvcf/worker/request-attach")
            .header(header::AUTHORIZATION, "Bearer bad-token")
            .body(Body::empty())?;
        let response = app.call(request).await?;
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
        let response_body = response.into_body().collect().await?.to_bytes();
        let parsed_response: ProblemDetails = serde_json::from_slice(&response_body)?;
        assert_eq!(parsed_response.status, Some(StatusCode::UNAUTHORIZED));
        assert_eq!(
            parsed_response.detail,
            Some("The provided authorization token is invalid or has expired".to_string())
        );
    }

    Ok(())
}

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

use axum::{
    body::Body,
    http::{self, HeaderValue, Method, StatusCode},
};
use mocks::{
    fixtures,
    nvcf_worker_mock::{CustomHeadersWorkHandler, PublishMode, Worker, WorkerProperties},
    API_KEY, FUNCTION_ID, INSTANCE_ID, VERSION_ID_1,
};
use nvcf_invocation_service::app::app;
use tower::{Service, ServiceExt};

// Typically you'd place this in the same module or a dedicated macros module:
macro_rules! hydrate_fixtures {
    ($response_headers:expr) => {{
        // The curly braces inside the macro body let us return an expression (a block).
        // This block can contain multiple statements and a final expression.
        let (_localstack, nats, mock_nvcf_api, config) = fixtures().await;
        let mut app = app(config.clone(), None).await?;
        let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

        let worker = Worker::new(
            config.nats_properties.clone(),
            WorkerProperties {
                function_id: FUNCTION_ID,
                function_version_id: VERSION_ID_1,
                instance_id: INSTANCE_ID.into(),
            },
            Box::new(CustomHeadersWorkHandler::new($response_headers)),
            PublishMode::Attach(app.clone()),
        )
        .await?
        .into_background_task();

        // Return the items you want to use in your test
        (app.clone(), (nats, mock_nvcf_api, worker))
    }};
}

#[tokio::test]
async fn test_all_response_headers_returned() -> anyhow::Result<()> {
    let (mut app, _fixtures_guard) = hydrate_fixtures!(http::HeaderMap::from_iter(vec![
        (
            http::header::ACCESS_CONTROL_ALLOW_ORIGIN,
            HeaderValue::from_static("http://my-url.com")
        ),
        (
            http::header::ACCESS_CONTROL_EXPOSE_HEADERS,
            HeaderValue::from_static("x-header1, x-header2")
        ),
        (
            http::header::ACCESS_CONTROL_EXPOSE_HEADERS,
            HeaderValue::from_static("x-header3")
        ),
        (
            http::header::HeaderName::from_static("x-header1"),
            HeaderValue::from_static("foo")
        ),
        (
            http::header::HeaderName::from_static("x-header2"),
            HeaderValue::from_static("bar")
        ),
        (
            http::header::HeaderName::from_static("x-header3"),
            HeaderValue::from_static("baz")
        ),
    ]));

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(http::header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header(
            http::header::ACCESS_CONTROL_REQUEST_METHOD,
            "POST".to_string(),
        )
        .header(
            http::header::ACCESS_CONTROL_REQUEST_HEADERS,
            "authorization,content-type",
        )
        .body(Body::from("a body"))?;

    tracing::info!("Request = {:#?}", &request);
    let response = app.call(request).await?;
    tracing::info!("Response = {:#?}", &response);

    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("fulfilled"))
    );

    // Confirm that the response lists all exposed headers
    let mut exposed_headers = vec![];
    for value in response
        .headers()
        .get_all(http::header::ACCESS_CONTROL_EXPOSE_HEADERS)
        .iter()
    {
        exposed_headers.push(value.to_str()?.to_string());
    }
    exposed_headers.sort();
    assert_eq!(
        exposed_headers,
        vec!["nvcf-reqid", "x-header1, x-header2", "x-header3"]
    );
    assert!(response.headers().get("nvcf-reqid").is_some());
    assert_eq!(response.headers().get("x-header1").unwrap(), "foo");
    assert_eq!(response.headers().get("x-header2").unwrap(), "bar");
    assert_eq!(response.headers().get("x-header3").unwrap(), "baz");
    assert_eq!(
        response
            .headers()
            .get(http::header::ACCESS_CONTROL_ALLOW_ORIGIN)
            .unwrap(),
        "http://my-url.com"
    );

    Ok(())
}

#[tokio::test]
async fn test_reqid_returned_for_empty_response_headers() -> anyhow::Result<()> {
    let (mut app, _fixtures_guard) = hydrate_fixtures!(http::HeaderMap::new());

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(http::header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .header(
            http::header::ACCESS_CONTROL_REQUEST_METHOD,
            "POST".to_string(),
        )
        .header(
            http::header::ACCESS_CONTROL_REQUEST_HEADERS,
            "authorization,content-type",
        )
        .body(Body::from("a body"))?;

    tracing::info!("Request = {:#?}", &request);
    let response = app.call(request).await?;
    tracing::info!("Response = {:#?}", &response);

    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("fulfilled"))
    );

    // Confirm that the response lists all exposed headers
    let mut exposed_headers = vec![];
    for value in response
        .headers()
        .get_all(http::header::ACCESS_CONTROL_EXPOSE_HEADERS)
        .iter()
    {
        exposed_headers.push(value.to_str()?.to_string());
    }
    assert_eq!(exposed_headers, vec!["nvcf-reqid"]);
    assert!(response.headers().get("nvcf-reqid").is_some());

    Ok(())
}

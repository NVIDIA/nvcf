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

use crate::mocks::nvcf_worker_mock::{PublishMode, WorkerProperties};
use axum::body::Body;
use axum::http;
use axum::http::header::{AUTHORIZATION, CONTENT_TYPE};
use axum::http::{HeaderValue, Method, StatusCode};
use http_body_util::BodyExt;
use mocks::{
    fixtures, localstack_s3_client,
    nvcf_worker_mock::{DefaultWorkHandler, Worker},
    API_KEY, ASSETS_BUCKET, FUNCTION_ID, INSTANCE_ID, NCA_ID, VERSION_ID_1,
};
use nvcf_invocation_service::app::app;
use problem_details::ProblemDetails;
use tower::{Service, ServiceExt};
use uuid::uuid;

#[tokio::test]
async fn test_input_asset() -> anyhow::Result<()> {
    let (localstack, _nats, _mock_nvcf_api, config) = fixtures().await;
    tracing::info!("pushing assets to s3");
    let s3_client = localstack_s3_client(&localstack).await?;
    let asset_id_1 = uuid!("6233efee-4462-4f84-9904-fca872a2cdab");
    let asset_id_2 = uuid!("7233efee-4462-4f84-9904-fca872a2cdab");
    let asset_id_3 = uuid!("8233efee-4462-4f84-9904-fca872a2cdab");
    let asset_id_4 = uuid!("9233efee-4462-4f84-9904-fca872a2cdab");
    for asset_id in [asset_id_1, asset_id_2, asset_id_3, asset_id_4] {
        s3_client
            .put_object()
            .bucket(ASSETS_BUCKET)
            .key(format!("{NCA_ID}/{asset_id}"))
            .content_type("application/json")
            .body("{}".as_bytes().to_vec().into())
            .send()
            .await?;
    }

    tracing::info!("finished pushing assets to s3");
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

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header(
            "NVCF-INPUT-ASSET-REFERENCES",
            format!("{asset_id_1},{asset_id_2}, {asset_id_3}"),
        )
        .header("NVCF-INPUT-ASSET-REFERENCES", format!("{asset_id_4}"))
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
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
async fn test_missing_input_asset() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;
    let asset_id = uuid!("8233efee-4462-4f84-9904-fca872a2cdab");
    let mut app = app(config, None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header("NVCF-INPUT-ASSET-REFERENCES", asset_id.to_string())
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::NOT_FOUND);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_static("application/problem+json"))
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let problem: ProblemDetails = serde_json::from_slice(&response_body)?;
    assert_eq!(
        problem,
        ProblemDetails::from_status_code(StatusCode::NOT_FOUND)
            .with_title("Not Found")
            .with_detail(format!("NotFound error: failed to find asset {asset_id}"))
    );
    Ok(())
}

#[tokio::test]
async fn test_empty_input_asset() -> anyhow::Result<()> {
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

    let request = axum::http::Request::builder()
        .method(Method::POST)
        // a customer is currently sending us headers like this
        .header("NVCF-INPUT-ASSET-REFERENCES", "")
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
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

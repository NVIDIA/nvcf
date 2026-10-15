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
use axum::http::header::{AUTHORIZATION, CONTENT_TYPE, LOCATION};
use axum::http::{Method, StatusCode};
use mocks::{
    fixtures,
    nvcf_worker_mock::{LargeResponseWorkHandler, Worker, WorkerProperties},
    API_KEY, FUNCTION_ID, INSTANCE_ID, VERSION_ID_1,
};
use nvcf_invocation_service::app::app;
use tower::{Service, ServiceExt};

#[tokio::test]
async fn test_large_response() -> anyhow::Result<()> {
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
        Box::new(LargeResponseWorkHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::FOUND);
    assert_eq!(response.headers().get("nvcf-status").unwrap(), "fulfilled");
    let redirect_content = reqwest::Client::new()
        .get(response.headers().get(LOCATION).unwrap().to_str().unwrap())
        .send()
        .await?;
    assert_eq!(
        redirect_content.headers().get(CONTENT_TYPE).unwrap(),
        "application/zip"
    );
    assert_eq!(redirect_content.text().await?, "fake-zip");

    Ok(())
}

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
use axum::{
    body::Body,
    http::{self, header, HeaderValue, Method, StatusCode},
};
use futures::future;
use mocks::{
    fixtures,
    nvcf_worker_mock::{
        DefaultWorkHandler, DroppableBackgroundWorker, FailedWorkHandler, Worker, WorkerProperties,
    },
    API_KEY, FUNCTION_ID, INSTANCE_ID, VERSION_ID_1,
};
use nvcf_invocation_service::app::app;
use std::time::{Duration, Instant};
use tokio::{task::JoinHandle, time::timeout};
use tower::{Service, ServiceExt};
use uuid::{uuid, Uuid};

#[tokio::test]
async fn test_request_id_not_found() -> anyhow::Result<()> {
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
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;

    // Verify that the request was processed successfully
    assert_eq!(response.status(), StatusCode::OK);
    assert!(response.headers().get("nvcf-reqid").is_some());

    // Lookup the request id that just finished (and should've been dequeued)
    let request_id: String = response
        .headers()
        .get("nvcf-reqid")
        .map(|hv| hv.to_str().unwrap().into())
        .unwrap();

    let request = axum::http::Request::builder()
        .method(Method::GET)
        .uri(format!("/v2/nvcf/pexec/status/{request_id}"))
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    // Verify the response status is NOT_FOUND
    assert_eq!(response.status(), StatusCode::NOT_FOUND);

    Ok(())
}

#[tokio::test]
async fn test_function_not_found() -> anyhow::Result<()> {
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

    let some_nonexistent_function_id: Uuid = uuid!("88888888-8888-8888-8888-888888888888");
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/pexec/functions/{some_nonexistent_function_id}/versions/{VERSION_ID_1}"
        ))
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    // Verify the status code
    assert_eq!(response.status(), StatusCode::NOT_FOUND);

    Ok(())
}

#[tokio::test]
async fn test_deleted_function_not_found() -> anyhow::Result<()> {
    let (_localstack, _nats, mut mock_nvcf_api, config) = fixtures().await;
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

    // Invoke a function and see that it succeeds
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a function"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);

    // Delete the function
    mock_nvcf_api.delete_function(FUNCTION_ID).await;

    // Invoke the function again
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a deleted function"))?;
    let response = app.call(request).await?;
    // Verify that a 404 status is returned
    assert_eq!(response.status(), StatusCode::NOT_FOUND);

    Ok(())
}

#[tokio::test]
async fn test_queue_overflow() -> anyhow::Result<()> {
    let max_messages = 10;
    let (_localstack, _nats, _mock_nvcf_api, mut config) = fixtures().await;
    config.nats_properties.max_messages = max_messages;
    // DO NOT remove retries to make the test return TOO_MANY_REQUESTS error. the test should work with retries enabled.

    let mut app = app(config, None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    // Create the allowable number of requests + 1
    let mut tasks = Vec::new();
    for _ in 0..max_messages + 1 {
        let request = axum::http::Request::builder()
            .method(Method::POST)
            .header("nvcf-poll-seconds", "1")
            .uri(format!(
                "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
            ))
            .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
            .body(Body::empty())?;

        tasks.push(tokio::spawn(app.call(request)));
    }
    let results = future::join_all(tasks).await;

    // Execute task list. Verify that all requests were accepted
    let responses: Vec<StatusCode> = results
        .into_iter()
        .map(|res| res.unwrap().unwrap().status())
        .collect();
    let count_too_many_requests = responses
        .iter()
        .filter(|&&code| code == StatusCode::TOO_MANY_REQUESTS)
        .count();
    let count_gateway_timeouts = responses
        .iter()
        .filter(|&&code| code == StatusCode::GATEWAY_TIMEOUT)
        .count();

    // Assert we have exactly one TOO_MANY_REQUESTS and all the others are GATEWAY_TIMEOUT
    assert_eq!(count_too_many_requests, 1_usize);
    assert_eq!(count_gateway_timeouts, max_messages as usize);

    Ok(())
}

#[tokio::test]
async fn test_failed_worker_response_status() -> anyhow::Result<()> {
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
        Box::new(FailedWorkHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    // Worker error should be transformed to 502 errors from the service
    assert_eq!(response.status(), StatusCode::INTERNAL_SERVER_ERROR);

    Ok(())
}

#[tokio::test]
async fn test_timeout() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;

    let mut app = app(config, None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header("nvcf-poll-seconds", "1")
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    // now that we don't generate 202 from the invocations service, it will timeout if there is no worker.
    // give it extra 2s as we do the same for the worker's timeout duration in case it's timeout in the middle of sending a response.
    let response = timeout(Duration::from_secs(4), app.call(request)).await??;
    assert_eq!(response.status(), StatusCode::GATEWAY_TIMEOUT);
    assert!(response.headers().get("nvcf-reqid").is_some());
    assert_eq!(
        response.headers().get("nvcf-status"),
        Some(&HeaderValue::from_static("errored"))
    );
    Ok(())
}

#[tokio::test]
async fn test_delayed_worker_start() -> anyhow::Result<()> {
    // NVCF-4507: Until a worker is available to pick up requests, the service should respond with
    // 504 gateway timeouts and not 500 internal server errors.  Once the worker does become available,
    // it should successfully process the request.
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;

    // Prepare the application, but do NOT start the worker yet
    let mut app = app(config.clone(), None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;
    let worker = Worker::new(
        config.nats_properties.clone(),
        WorkerProperties {
            function_id: FUNCTION_ID,
            function_version_id: VERSION_ID_1,
            instance_id: INSTANCE_ID.into(),
        },
        Box::new(DefaultWorkHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?;

    let _delayed_background_worker: JoinHandle<DroppableBackgroundWorker> = tokio::spawn({
        let worker = worker; // move in for spawning
        async move {
            tokio::time::sleep(Duration::from_secs(8)).await;
            worker.into_background_task()
        }
    });

    // Attempt the request in a loop for up to 10 seconds.
    let start = Instant::now();
    let max_duration = Duration::from_secs(10);

    loop {
        // Return an error if max duration is exceeded.
        if start.elapsed() > max_duration {
            return Err(anyhow::anyhow!(
                "Request still timing out after 10 seconds."
            ));
        }

        // Attempt the request with a 1-second poll duration.  The service will add 2 more seconds
        // so will timeout after 3 seconds if the worker is not started.
        let request = axum::http::Request::builder()
            .method(Method::POST)
            .header("nvcf-poll-seconds", "1")
            .uri(format!(
                "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
            ))
            .header(header::AUTHORIZATION, format!("Bearer {API_KEY}"))
            .body(Body::from("a body"))?;

        let response = app.call(request).await?;

        // We expect GATEWAY_TIMEOUT's until the worker is spawned.  Keep looping until the worker
        // comes alive and picks up the request.
        if response.status() == StatusCode::GATEWAY_TIMEOUT {
            continue;
        } else {
            // Assert that the worker response is successful
            assert_eq!(
                response.status(),
                StatusCode::OK,
                "Expected 200 OK after worker is started"
            );
            assert!(response.headers().get("nvcf-reqid").is_some());
            break;
        }
    }
    Ok(())
}

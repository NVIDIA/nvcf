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
use axum::http::header::{ACCEPT, AUTHORIZATION, CONTENT_TYPE};
use axum::http::{HeaderValue, Method, StatusCode};
use bytes::Bytes;
use futures::stream;
use http_body_util::BodyExt;
use mime::TEXT_EVENT_STREAM;
use mocks::{
    fixtures,
    nvcf_worker_mock::{
        DefaultWorkHandler, FnWorkHandler, SseWorkHandler, Worker, WorkerProperties,
    },
    rate_limit_mock, API_KEY, FUNCTION_ID, FUNCTION_ID_2_RATELIMIT_SYNC,
    FUNCTION_ID_3_RATELIMIT_ASYNC, INSTANCE_ID, VERSION_ID_1, VERSION_ID_3, VERSION_ID_4,
};
use nvcf_invocation_service::{
    app::app,
    rate_limit::rate_limit_api::{RateLimitResponse, RateLimitResult},
};
use problem_details::ProblemDetails;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::time;
use tokio::time::timeout;
use tonic::Response;
use tower::{Service, ServiceExt};

#[tokio::test]
async fn test_pexec() -> anyhow::Result<()> {
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

    for i in 0..100 {
        tracing::info!("performing request {i}");
        let request = axum::http::Request::builder()
            .method(Method::POST)
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
        assert!(response.headers().get("nvcf-reqid").is_some());
        assert_eq!(
            response.headers().get("access-control-expose-headers"),
            Some(&HeaderValue::from_static("nvcf-reqid"))
        );
        assert_eq!(
            response.headers().get("nvcf-status"),
            Some(&HeaderValue::from_static("fulfilled"))
        );
        assert_eq!(
            response.headers().get(http::header::CONTENT_LENGTH),
            Some(&HeaderValue::from_str(
                "a response".len().to_string().as_str()
            )?)
        );
        assert_eq!(
            response.body().size_hint().exact(),
            Some("a response".len() as u64)
        );
        let response_body = response.into_body().collect().await?.to_bytes();
        let response_body = String::from_utf8(response_body.into())?;
        assert_eq!(response_body, "a response");
        tracing::info!("completed request {i}");
    }
    Ok(())
}

#[tokio::test]
async fn test_sse() -> anyhow::Result<()> {
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
        Box::new(SseWorkHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header(ACCEPT, TEXT_EVENT_STREAM.as_ref())
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_str(TEXT_EVENT_STREAM.as_ref())?)
    );
    assert_eq!(response.headers().get(http::header::CONTENT_LENGTH), None);
    assert!(response.headers().get("nvcf-reqid").is_some());
    assert_eq!(
        response.headers().get("access-control-expose-headers"),
        Some(&HeaderValue::from_static("nvcf-reqid"))
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(
        response_body,
        "event:an event\ndata:some data\n\n".repeat(100)
    );

    Ok(())
}

#[tokio::test]
async fn test_bad_auth() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;

    let mut app = app(config, None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, "Bearer bad-token")
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_static("application/problem+json"))
    );
    let response_body = response.into_body().collect().await?.to_bytes();
    let problem: ProblemDetails = serde_json::from_slice(&response_body)?;
    assert_eq!(
        problem,
        ProblemDetails::from_status_code(StatusCode::UNAUTHORIZED)
            .with_title("Unauthorized")
            .with_detail("client is unauthorized") // message comes from mock nvcf api
    );
    Ok(())
}

#[tokio::test]
async fn test_pexec_body_read_error() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;

    let mut app = app(config, None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let error_body = Body::from_stream(stream::once(async {
        Err::<Bytes, std::io::Error>(std::io::Error::other("body stream failed"))
    }));

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(error_body)?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    let response_body = response.into_body().collect().await?.to_bytes();
    let problem: ProblemDetails = serde_json::from_slice(&response_body)?;
    assert!(problem
        .detail
        .unwrap_or_default()
        .starts_with("failed to read request body:"));
    Ok(())
}

#[tokio::test]
async fn test_wrong_function() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;

    let mut app = app(config, None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri("/v2/nvcf/pexec/functions/bac11503-75ca-4a40-bf24-77ce49d13355")
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
            .with_detail("function not found") // message comes from mock nvcf api
    );
    Ok(())
}

#[tokio::test]
async fn test_large_payload() -> anyhow::Result<()> {
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

    // we should be able to send exactly 5MB bodies
    let large_body = "a".repeat(5 * 1024 * 1024);
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from(large_body))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get(CONTENT_TYPE),
        Some(&HeaderValue::from_static("text/plain"))
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
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");

    Ok(())
}

#[tokio::test]
async fn test_cancelled_request() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;

    let mut app = app(config.clone(), None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header(ACCEPT, TEXT_EVENT_STREAM.as_ref())
        .header("nvcf-poll-seconds", "1")
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::GATEWAY_TIMEOUT);
    assert!(response.headers().get("nvcf-reqid").is_some());

    let counter = Arc::new(AtomicU64::new(0));
    let counter_cloned = counter.clone();
    let _worker = Worker::new(
        config.nats_properties.clone(),
        WorkerProperties {
            function_id: FUNCTION_ID,
            function_version_id: VERSION_ID_1,
            instance_id: INSTANCE_ID.into(),
        },
        Box::new(FnWorkHandler(Box::new(move |_worker, _request| {
            counter_cloned.fetch_add(1, Ordering::SeqCst);
            Ok(())
        }))),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();
    // expecting the worker to not pick up any work
    time::sleep(Duration::from_secs(1)).await;
    assert_eq!(0, counter.load(Ordering::SeqCst));
    Ok(())
}

#[tokio::test]
async fn test_rate_limited() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, mut config) = fixtures().await;
    let atomic_rate_limit_status = Arc::new(Mutex::new(RateLimitResult::Allow));
    let mock_rate_limit = rate_limit_mock::RateLimitMock {
        callback: {
            let atomic_rate_limit_status = atomic_rate_limit_status.clone();
            Box::new(move |_| {
                Ok(Response::new(RateLimitResponse {
                    result: (*atomic_rate_limit_status.lock().expect("lock poisoned")).into(),
                }))
            })
        },
    }
    .into_server()
    .await;
    config.rate_limit_enabled = true;
    config.rate_limit_address = format!("http://{}", mock_rate_limit.address());
    let mut app = app(config.clone(), None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let _worker = Worker::new(
        config.nats_properties.clone(),
        WorkerProperties {
            function_id: FUNCTION_ID_3_RATELIMIT_ASYNC,
            function_version_id: VERSION_ID_4,
            instance_id: INSTANCE_ID.into(),
        },
        Box::new(DefaultWorkHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let make_request = || {
        axum::http::Request::builder()
            .method(Method::POST)
            .uri(format!(
                "/v2/nvcf/pexec/functions/{FUNCTION_ID_3_RATELIMIT_ASYNC}/versions/{VERSION_ID_4}"
            ))
            .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
            .body(Body::from("a body"))
    };

    // first call, non rate limited. should be accepted.
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::OK);
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");

    // next call should still go through because we are checking in the background
    *atomic_rate_limit_status.lock().expect("lock poisoned") = RateLimitResult::Disallow;
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::OK);
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");

    // sleep for long enough to let the async check to return
    tokio::time::sleep(Duration::from_millis(100)).await;
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);

    // on allow, requests should immediately go through again
    *atomic_rate_limit_status.lock().expect("lock poisoned") = RateLimitResult::Allow;
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::OK);
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");

    // synchronous checks will still be enabled at this point because there was a recent rate limit,
    // so setting to disallow should immediately error
    *atomic_rate_limit_status.lock().expect("lock poisoned") = RateLimitResult::Disallow;
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);

    Ok(())
}

#[tokio::test]
async fn test_no_rate_limited() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, mut config) = fixtures().await;
    let atomic_rate_limit_status = Arc::new(Mutex::new(RateLimitResult::Allow));
    let mock_rate_limit = rate_limit_mock::RateLimitMock {
        callback: {
            let atomic_rate_limit_status = atomic_rate_limit_status.clone();
            Box::new(move |_| {
                Ok(Response::new(RateLimitResponse {
                    result: (*atomic_rate_limit_status.lock().expect("lock poisoned")).into(),
                }))
            })
        },
    }
    .into_server()
    .await;

    config.rate_limit_enabled = false;
    config.rate_limit_address = format!("http://{}", mock_rate_limit.address());
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

    let make_request = || {
        axum::http::Request::builder()
            .method(Method::POST)
            .uri(format!(
                "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
            ))
            .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
            .body(Body::from("a body"))
    };

    // first call, non rate limited. should be accepted.
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::OK);
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");

    // next call should still go through because this function does not have a ratelimit policy
    *atomic_rate_limit_status.lock().expect("lock poisoned") = RateLimitResult::Disallow;
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::OK);
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");

    // sleep for long enough, and it should still go through
    tokio::time::sleep(Duration::from_millis(100)).await;
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::OK);
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::OK);
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");

    Ok(())
}

#[tokio::test]
async fn test_rate_limited_sync_check() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, mut config) = fixtures().await;
    let atomic_rate_limit_status = Arc::new(Mutex::new(RateLimitResult::Disallow));
    let mock_rate_limit = rate_limit_mock::RateLimitMock {
        callback: {
            let atomic_rate_limit_status = atomic_rate_limit_status.clone();
            Box::new(move |_| {
                Ok(Response::new(RateLimitResponse {
                    result: (*atomic_rate_limit_status.lock().expect("lock poisoned")).into(),
                }))
            })
        },
    }
    .into_server()
    .await;

    config.rate_limit_enabled = true;
    config.rate_limit_address = format!("http://{}", mock_rate_limit.address());
    let mut app = app(config.clone(), None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let _worker = Worker::new(
        config.nats_properties.clone(),
        WorkerProperties {
            function_id: FUNCTION_ID_2_RATELIMIT_SYNC,
            function_version_id: VERSION_ID_3,
            instance_id: INSTANCE_ID.into(),
        },
        Box::new(DefaultWorkHandler {}),
        PublishMode::Attach(app.clone()),
    )
    .await?
    .into_background_task();

    let make_request = || {
        axum::http::Request::builder()
            .method(Method::POST)
            .uri(format!(
                "/v2/nvcf/pexec/functions/{FUNCTION_ID_2_RATELIMIT_SYNC}/versions/{VERSION_ID_3}"
            ))
            .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
            .body(Body::from("a body"))
    };

    // first call, should be rate limited right away
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);

    // next call still can't go through
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);

    // on allow, requests should immediately go through again
    *atomic_rate_limit_status.lock().expect("lock poisoned") = RateLimitResult::Allow;
    let response = app.call(make_request()?).await?;
    assert_eq!(response.status(), StatusCode::OK);
    let response_body = response.into_body().collect().await?.to_bytes();
    let response_body = String::from_utf8(response_body.into())?;
    assert_eq!(response_body, "a response");

    Ok(())
}

#[tokio::test]
async fn test_rate_limit_request_carries_client_auth_subject() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, mut config) = fixtures().await;

    let captured_subject = Arc::new(Mutex::new(None::<String>));
    let mock_rate_limit = rate_limit_mock::RateLimitMock {
        callback: {
            let captured_subject = captured_subject.clone();
            Box::new(move |req| {
                *captured_subject.lock().expect("lock poisoned") =
                    Some(req.client_auth_subject.clone());
                Ok(Response::new(RateLimitResponse {
                    result: RateLimitResult::Allow.into(),
                }))
            })
        },
    }
    .into_server()
    .await;

    config.rate_limit_enabled = true;
    config.rate_limit_address = format!("http://{}", mock_rate_limit.address());
    let mut app = app(config.clone(), None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    let _worker = Worker::new(
        config.nats_properties.clone(),
        WorkerProperties {
            function_id: FUNCTION_ID_2_RATELIMIT_SYNC,
            function_version_id: VERSION_ID_3,
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
            "/v2/nvcf/pexec/functions/{FUNCTION_ID_2_RATELIMIT_SYNC}/versions/{VERSION_ID_3}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;
    let response = app.call(request).await?;
    assert_eq!(response.status(), StatusCode::OK);

    // The nvcf api mock returns "test-subject" as client_auth_subject; the
    // invocation service must forward that into the RateLimitRequest so the
    // ratelimiter can apply per-user (caller-tier) limits when configured.
    let captured = captured_subject
        .lock()
        .expect("lock poisoned")
        .clone()
        .expect("ratelimiter mock should have received a request");
    assert_eq!(captured, "test-subject");

    Ok(())
}

#[tokio::test]
async fn test_drop_mid_request() -> anyhow::Result<()> {
    let (_localstack, _nats, _mock_nvcf_api, config) = fixtures().await;

    let mut app = app(config.clone(), None).await?;
    let app = ServiceExt::<http::Request<Body>>::ready(&mut app).await?;

    // Create a POST request that presumably triggers NATS work, but can wait for 5 seconds before being accepted
    let request = axum::http::Request::builder()
        .method(Method::POST)
        .header("nvcf-poll-seconds", "5")
        .uri(format!(
            "/v2/nvcf/pexec/functions/{FUNCTION_ID}/versions/{VERSION_ID_1}"
        ))
        .header(AUTHORIZATION, format!("Bearer {API_KEY}"))
        .body(Body::from("a body"))?;

    // expect the the call to be accepted and dropped after timeout, which is 4 seconds
    timeout(Duration::from_secs(4), app.call(request))
        .await
        .expect_err("expect it to timeout and drop the request");

    // Set up a worker that increments `counter` if it gets a request
    let counter = Arc::new(AtomicU64::new(0));
    let _worker = {
        let counter_cloned = counter.clone();
        Worker::new(
            config.nats_properties.clone(),
            WorkerProperties {
                function_id: FUNCTION_ID,
                function_version_id: VERSION_ID_1,
                instance_id: INSTANCE_ID.into(),
            },
            Box::new(FnWorkHandler(Box::new(move |_worker, _request| {
                counter_cloned.fetch_add(1, Ordering::SeqCst);
                Ok(())
            }))),
            PublishMode::Attach(app.clone()),
        )
        .await?
        .into_background_task()
    };
    // Wait a bit to let the server notice
    tokio::time::sleep(Duration::from_millis(1000)).await;
    // The server's RAII guard should have canceled the NATS request.
    // Hence, the worker should see 0 requests processed.
    assert_eq!(0, counter.load(Ordering::SeqCst));

    Ok(())
}

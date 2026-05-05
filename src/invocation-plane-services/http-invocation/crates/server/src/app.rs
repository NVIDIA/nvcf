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

use crate::{
    middleware::{self as nvcf_mw, spans::NVCFMakeSpan},
    nats::NatsService,
    nvcf_api::NVCFService,
    rate_limit::RateLimitService,
    routes::{self, function_id_headers, split_hostname, tlb_handler},
    s3::S3Service,
    secrets::secret_provider::SecretFileWatcher,
    settings::AppConfig,
    worker_streams::WorkerStreamService,
};
use axum::{
    body::Body,
    extract::FromRef,
    middleware as axum_mw,
    response::{IntoResponse, Response},
    routing::{get, post},
    Router,
};
use axum_extra::extract::Host;
use axum_prometheus::{EndpointLabel, PrometheusMetricLayerBuilder};
use hyper::StatusCode;
use problem_details::ProblemDetails;
use std::{sync::Arc, time::Duration};
use tower::ServiceExt;
use tower_http::{
    catch_panic::CatchPanicLayer,
    cors::CorsLayer,
    timeout::TimeoutLayer,
    trace::{DefaultOnRequest, TraceLayer},
};
use tracing::{Level, Span};

#[derive(Clone, FromRef)]
pub struct AppState {
    nvcf_service: Arc<NVCFService>,
    nats_service: Arc<NatsService>,
    s3_service: Arc<S3Service>,
    rate_limit_service: Arc<RateLimitService>,
    worker_stream_service: Arc<WorkerStreamService>,
    app_config: Arc<AppConfig>,
}

pub async fn app(
    config: AppConfig,
    secrets: Option<Arc<SecretFileWatcher>>,
) -> anyhow::Result<Router> {
    let app_state = AppState {
        nvcf_service: NVCFService::new(
            &config.nvcf_api_address,
            &config.oauth2_base_url,
            secrets.clone(),
            &config.nvcf_api_properties,
            &config.grpc_client,
        )
        .await?
        .into(),
        nats_service: NatsService::new(
            &config.nats_properties,
            &config.oauth2_base_url,
            secrets.clone(),
            &config.grpc_client,
        )
        .await?
        .into(),
        s3_service: S3Service::new(&config.s3_properties).await?.into(),
        rate_limit_service: RateLimitService::new(
            config.rate_limit_enabled,
            &config.rate_limit_address,
            &config.oauth2_base_url,
            secrets,
            &config.grpc_client,
        )
        .await?
        .into(),
        worker_stream_service: WorkerStreamService::new(&config.worker_stream_properties)?.into(),
        app_config: config.into(),
    };

    let trace_layer = TraceLayer::new_for_http()
        .make_span_with(NVCFMakeSpan::new())
        .on_request(DefaultOnRequest::new().level(Level::DEBUG))
        .on_response(
            |response: &Response<Body>, _latency: Duration, span: &Span| {
                span.record(
                    "http.response.status_code",
                    tracing::field::display(response.status().as_u16()),
                );
                tracing::debug!("response generated")
            },
        );
    // 1h timeout plus 5s to let us send responses at the 1h mark.
    let timeout_layer = TimeoutLayer::with_status_code(
        StatusCode::REQUEST_TIMEOUT,
        Duration::from_secs(60 * 60 + 5),
    );

    let path_based_router = Router::new()
        .fallback(handler_404)
        .route(
            "/v2/nvcf/pexec/functions/{function_id}",
            post(routes::pexec),
        )
        .route(
            "/v2/nvcf/pexec/functions/{function_id}/versions/{function_version_id}",
            post(routes::pexec),
        )
        .route(
            "/v2/nvcf/pexec/status/{request_id}",
            get(routes::pexec_status_route),
        )
        .route("/v2/nvcf/exec/functions/{function_id}", post(routes::exec))
        .route(
            "/v2/nvcf/exec/functions/{function_id}/versions/{function_version_id}",
            post(routes::exec),
        )
        .route(
            "/v2/nvcf/exec/status/{request_id}",
            get(routes::exec_status),
        )
        .route(
            "/v2/nvcf/worker/request-attach",
            get(routes::request_attach),
        )
        .route(
            "/v2/nvcf/worker/request-attach",
            post(routes::response_attach),
        )
        .layer(axum_mw::from_fn(nvcf_mw::auth::auth_middleware)) // Everything above this layer will require auth DON'T MOVE IT
        .route("/health", get(routes::get_health)) // Needs to be ahead of other layers to avoid auth, and after metrics layer to get recorded
        .layer(CorsLayer::very_permissive().max_age(Duration::from_secs(86400))) // only on the path based router. for full path passthrough we send the OPTIONS and all other requests all the way to the worker.
        .layer(
            PrometheusMetricLayerBuilder::new()
                .enable_response_body_size(true)
                .with_endpoint_label_type(EndpointLabel::MatchedPathWithFallbackFn(|_| {
                    "UNKNOWN".to_string()
                }))
                .build(),
        )
        .layer(CatchPanicLayer::new())
        .layer(timeout_layer)
        .layer(axum_mw::from_fn(
            |request: axum::extract::Request, next: axum::middleware::Next| {
                Span::current().record("tlb", false);
                next.run(request)
            },
        ))
        .layer(trace_layer.clone())
        .with_state(app_state.clone());

    // using a full router instead of a handler function so the metrics layer can be attached
    let tlb_router = Router::new()
        .fallback(tlb_handler)
        .layer(axum_mw::from_fn(nvcf_mw::auth::auth_middleware))
        .layer(
            PrometheusMetricLayerBuilder::new()
                .enable_response_body_size(true)
                .with_endpoint_label_type(EndpointLabel::MatchedPathWithFallbackFn(|_| {
                    "TLB".to_string()
                }))
                .build(),
        )
        .layer(CatchPanicLayer::new())
        .layer(timeout_layer)
        .layer(axum_mw::from_fn(
            |request: axum::extract::Request, next: axum::middleware::Next| {
                Span::current()
                    .record("tlb", true)
                    .record("otel.name", format!("{} TLB", request.method()))
                    .record("http.route", "TLB");
                next.run(request)
            },
        ))
        .layer(trace_layer)
        .with_state(app_state);

    let host_router = Router::new()
        // docs say not to have just a fallback, but I can't figure out how to name the handler type to pass it to axum::serve.
        .fallback(
            |hostname: Option<Host>, mut request: http::Request<Body>| async move {
                // check for headers too. we don't have wildcard dns yet, so we need to use headers to test.
                if let Some((function_id, function_version_id)) =
                    function_id_headers(request.headers())
                        .or_else(|| hostname.and_then(|hostname| split_hostname(&hostname.0)))
                {
                    if let Some(function_id) = function_id {
                        request.extensions_mut().insert(function_id);
                    }
                    if let Some(function_version_id) = function_version_id {
                        request.extensions_mut().insert(function_version_id);
                    }
                    return tlb_router.oneshot(request).await;
                }
                path_based_router.oneshot(request).await
            },
        );

    Ok(host_router)
}

async fn handler_404() -> impl IntoResponse {
    ProblemDetails::from_status_code(StatusCode::NOT_FOUND)
}

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

use axum::body::Body as AxumBody;
use axum::extract::{FromRequestParts, MatchedPath, Path};
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use http::{Request, StatusCode};
use tracing_opentelemetry_instrumentation_sdk::http::{
    http_flavor, http_host, url_scheme, user_agent,
};

use axum::extract;
use http::request::Parts;
use std::collections::HashMap;
use tracing::{field::Empty, span, Level, Span};
use tracing_opentelemetry::OpenTelemetrySpanExt;
use tracing_opentelemetry_instrumentation_sdk::http as otel_http;

pub async fn _add_path_params_to_span(req: Request<AxumBody>, next: Next) -> impl IntoResponse {
    let (parts, body) = req.into_parts();
    let mut parts_clone = parts.clone();
    let params = match _parse_url_params(&mut parts_clone).await {
        Ok(value) => value,
        Err(value) => return value,
    };

    let span = Span::current();
    // Attach each path param to the span
    // NOTE: These fields must already exist in the span
    for (key, value) in params.iter() {
        span.record(key.as_str(), value.as_str());
    }

    // Recreate the request from the original parts because Request can't be cloned
    let re_req = Request::from_parts(parts, body);

    next.run(re_req).await
}

#[derive(Debug, Clone)]
pub struct NVCFMakeSpan {}

impl NVCFMakeSpan {
    pub fn new() -> Self {
        Self {}
    }
}

impl Default for NVCFMakeSpan {
    fn default() -> Self {
        Self::new()
    }
}

const TRACING_LEVEL: Level = Level::DEBUG;

impl<B> tower_http::trace::MakeSpan<B> for NVCFMakeSpan {
    fn make_span(&mut self, request: &Request<B>) -> Span {
        macro_rules! make_span {
            ($operation:expr, $route:expr, $method:expr, $req_version:expr, $headers:expr, $address:expr, $port:expr, $user_agent:expr, $path:expr, $query:expr, $scheme:expr) => {
                span!(
                    target: "otel::tracing",
                    parent: None,
                    TRACING_LEVEL,
                    "Request",
                    "span.type" = "web", // non-official open-telemetry key, only supported by Datadog
                    exception.message = Empty, // to set on response
                    http.route = %$route, // to set by router of "webframework" after
                    http.client.address = Empty, //%$request.connection_info().realip_remote_addr().unwrap_or(""),
                    http.response.status_code = Empty, // to set on response
                    otel.name = %$operation, // to set by router of "webframework" after
                    otel.kind = format!("{:?}",opentelemetry::trace::SpanKind::Server),
                    otel.status_code = Empty, // to set on response
                    http.request.method = %$method,
                    network.protocol.version = %$req_version,
                    // request.headers = $headers,
                    server.address = %$address,
                    server.port = %$port,
                    user_agent.original = %$user_agent,
                    url.path = %$path,
                    url.query = %$query,
                    url.scheme = %$scheme,
                    trace_id = Empty, // to set on response
                    request_id = Empty, // to set
                    function_id = Empty,
                    function_version_id = Empty,
                    nca_id = Empty,
                    tlb = Empty,
                    operation = Empty,
                    error = Empty,
                    app_error = Empty,
                )
            }
        }

        let route = request
            .extensions()
            .get::<MatchedPath>()
            .map_or_else(|| "", |mp| mp.as_str());
        let method = request.method().as_str();
        let operation = get_operation(request);
        let req_version = http_flavor(request.version()).to_string();
        // let headers = format!("{:?}", request.headers());
        let address = http_host(request);
        let port = match request.uri().port() {
            Some(port) => port.to_string(),
            None => "8080".to_string(),
        };
        let user_agent = user_agent(request);
        let path = request.uri().path();
        let query = request.uri().query().unwrap_or_default();
        let url_scheme = url_scheme(request.uri());

        let span = make_span!(
            operation,
            route,
            method,
            req_version,
            headers,
            address,
            port,
            user_agent,
            path,
            query,
            url_scheme
        );
        let _ = span.set_parent(otel_http::extract_context(request.headers()));
        span
    }
}

pub async fn _parse_url_params(
    parts: &mut Parts,
) -> Result<HashMap<String, String>, Response<AxumBody>> {
    let Path(params): Path<HashMap<String, String>> =
        match Path::from_request_parts(parts, &()).await {
            Ok(p) => p,
            Err(_) => {
                let error_response = axum::Json(serde_json::json!({
                    "error": "Bad Request",
                    "message": "Request contains an unparseable path"
                }));
                return Err((StatusCode::BAD_REQUEST, error_response).into_response());
            }
        };
    Ok(params)
}

pub fn get_operation<B>(req: &extract::Request<B>) -> String {
    let route = http_route(req);
    let method = req.method().as_str();
    format!("{method} {route}")
}

fn http_route<B>(req: &extract::Request<B>) -> &str {
    req.extensions()
        .get::<MatchedPath>()
        .map_or_else(|| "", |mp| mp.as_str())
}

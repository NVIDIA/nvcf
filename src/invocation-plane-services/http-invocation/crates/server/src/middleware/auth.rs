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

use axum::body::Body;
use axum::extract::Request;
use axum::middleware::Next;
use axum::response::IntoResponse;
use headers::{Authorization, HeaderMapExt};
use http::header::AUTHORIZATION;
use http::{Response, StatusCode};

/// Parse one bearer header for control-plane or request-scoped handler validation.
pub async fn auth_middleware(mut req: Request, next: Next) -> impl IntoResponse {
    if req.headers().get_all(AUTHORIZATION).iter().count() != 1 {
        tracing::warn!("expected one authorization header");
        return auth_err_response();
    }
    let Some(bearer) = req
        .headers()
        .typed_get::<Authorization<headers::authorization::Bearer>>()
    else {
        tracing::warn!("failed to parse bearer authorization header");
        return auth_err_response();
    };
    if bearer.token().is_empty() || bearer.token().bytes().any(|b| b.is_ascii_whitespace()) {
        tracing::warn!("invalid bearer authorization header");
        return auth_err_response();
    }

    // Token validation is delegated to auth_invocation; worker attach routes
    // validate their request-scoped tokens in their handlers.
    req.extensions_mut().insert(bearer);
    req.headers_mut().remove(AUTHORIZATION);
    next.run(req).await
}

/// Use the same response for every authorization-header parsing failure.
fn auth_err_response() -> Response<Body> {
    let mut err_response: Response<Body> = Response::new("Unauthorized access".into());
    *err_response.status_mut() = StatusCode::UNAUTHORIZED;
    err_response
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{extract::Extension, middleware, routing::get, Router};
    use headers::authorization::Bearer;
    use http::{HeaderMap, HeaderValue};
    use http_body_util::BodyExt;
    use tower::ServiceExt;

    fn app() -> Router {
        Router::new()
            .route("/", get(|Extension(auth): Extension<Authorization<Bearer>>, headers: HeaderMap| async move {
                assert_eq!(auth.token(), "test-token");
                assert!(!headers.contains_key(AUTHORIZATION));
                StatusCode::NO_CONTENT
            }))
            .layer(middleware::from_fn(auth_middleware))
    }

    #[tokio::test]
    async fn rejects_malformed_or_missing_authorization() {
        for header in [
            None,
            Some(""),
            Some("Basic test-token"),
            Some("Bearer"),
            Some("Bearer "),
            Some("Bearer test token"),
        ] {
            let mut request = Request::new(Body::empty());
            if let Some(header) = header {
                request
                    .headers_mut()
                    .insert(AUTHORIZATION, HeaderValue::from_str(header).unwrap());
            }
            let response = app().oneshot(request).await.unwrap();
            assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
            assert_eq!(
                response.into_body().collect().await.unwrap().to_bytes(),
                "Unauthorized access"
            );
        }
    }

    #[tokio::test]
    async fn rejects_duplicate_authorization_headers() {
        let mut request = Request::new(Body::empty());
        request
            .headers_mut()
            .append(AUTHORIZATION, HeaderValue::from_static("Bearer test-token"));
        request.headers_mut().append(
            AUTHORIZATION,
            HeaderValue::from_static("Bearer other-token"),
        );
        assert_eq!(
            app().oneshot(request).await.unwrap().status(),
            StatusCode::UNAUTHORIZED
        );
    }

    #[tokio::test]
    async fn extracts_bearer_for_handler_validation() {
        for header in [
            "Bearer test-token",
            "bearer test-token",
            "BEARER test-token",
        ] {
            let request = Request::builder()
                .header(AUTHORIZATION, header)
                .body(Body::empty())
                .unwrap();
            assert_eq!(
                app().oneshot(request).await.unwrap().status(),
                StatusCode::NO_CONTENT
            );
        }
    }
}

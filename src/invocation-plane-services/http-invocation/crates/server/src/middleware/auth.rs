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
use headers::Authorization;
use http::header::AUTHORIZATION;
use http::{Response, StatusCode};

pub async fn auth_middleware(mut req: Request, next: Next) -> impl IntoResponse {
    if let Some(auth) = req.headers().get(AUTHORIZATION) {
        match auth.to_str() {
            Ok(value) => {
                if let Some(token) = value.strip_prefix("Bearer ") {
                    let bearer = match Authorization::bearer(token) {
                        Ok(b) => b,
                        Err(_) => {
                            tracing::warn!("failed to parse authorization header");
                            return auth_err_response();
                        }
                    };
                    let extensions = req.extensions_mut();
                    extensions.insert(bearer); // <------ ADDS IT TO THE REQUEST AS AN EXPLICIT TYPE
                    req.headers_mut().remove(AUTHORIZATION); //   <------ REMOVES IT FROM THE HEADERS
                    tracing::trace!("auth middleware passed");
                }
            }
            Err(_) => {
                tracing::warn!("Could not cast authorization header to string");
                return auth_err_response();
            }
        }
    } else {
        tracing::warn!("Missing Authorization header");
        let mut missing_header = Response::new("Header of type `authorization` was missing".into());
        *missing_header.status_mut() = StatusCode::UNAUTHORIZED;
        return missing_header;
    }
    next.run(req).await
}

fn auth_err_response() -> Response<Body> {
    let mut err_response: Response<Body> = Response::new("Unauthorized access".into());
    *err_response.status_mut() = StatusCode::UNAUTHORIZED;
    err_response
}

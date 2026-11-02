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

//! Audit logging — structured request logs for compliance.
//!
//! Every S3/wire/NBD request logged with:
//! who (access key), what (operation), when (timestamp),
//! where (bucket/key), outcome (status), request ID, duration.
//!
//! CloudTrail-compatible JSON format.

use std::time::Instant;

use axum::body::Body;
use axum::http::Request;
use axum::response::Response;
use tracing::info;
use uuid::Uuid;

/// Axum middleware that logs every request in audit format.
pub async fn audit_layer(req: Request<Body>, next: axum::middleware::Next) -> Response {
    let request_id = Uuid::new_v4().to_string();
    let method = req.method().to_string();
    let path = req.uri().path().to_string();
    let query = req.uri().query().unwrap_or("").to_string();

    let client_ip = req
        .headers()
        .get("x-forwarded-for")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("direct")
        .to_string();

    let access_key = req
        .headers()
        .get("authorization")
        .and_then(|v| v.to_str().ok())
        .and_then(|s| s.split("Credential=").nth(1))
        .and_then(|s| s.split('/').next())
        .unwrap_or("anonymous")
        .to_string();

    let user_agent = req
        .headers()
        .get("user-agent")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string();

    let start = Instant::now();
    let response = next.run(req).await;
    let duration_ms = start.elapsed().as_millis() as u64;

    let status = response.status().as_u16();

    // Structured audit log — filtered by tracing target "nvstor::audit"
    info!(
        target: "nvstor::audit",
        request_id = %request_id,
        method = %method,
        path = %path,
        query = %query,
        status = status,
        duration_ms = duration_ms,
        client_ip = %client_ip,
        access_key = %access_key,
        user_agent = %user_agent,
        "audit"
    );

    response
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_uuid_generation() {
        let id = Uuid::new_v4().to_string();
        assert_eq!(id.len(), 36);
    }
}

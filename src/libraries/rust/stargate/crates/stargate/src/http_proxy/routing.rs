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
use axum::http::{HeaderName, HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use tracing::{Span, warn};

use crate::metrics::StargateMetrics;
use crate::routing_state::RoutingTargetKey;

use super::HEADER_STARGATE_ERROR_CODE;
use super::request_routing::RoutingRejection;

const ERROR_OVERLOADED: &str = "overloaded_error";
const ERROR_NO_ELIGIBLE_CANDIDATES: &str = "no_eligible_candidates";
const ERROR_NO_ELIGIBLE_CANDIDATES_BODY: &str =
    r#"{"error":"no eligible candidates","code":"no_eligible_candidates"}"#;

pub(super) fn overloaded_response() -> Response<Body> {
    (
        StatusCode::SERVICE_UNAVAILABLE,
        [(HEADER_STARGATE_ERROR_CODE, ERROR_OVERLOADED)],
        axum::Json(serde_json::json!({
            "error": {
                "code": ERROR_OVERLOADED,
                "message": "Inference capacity is temporarily unavailable.",
                "param": "",
                "type": ERROR_OVERLOADED,
            }
        })),
    )
        .into_response()
}

pub(super) fn input_work_admission_rejection_response(
    metrics: &StargateMetrics,
    target: &RoutingTargetKey,
    reason: &'static str,
) -> Response<Body> {
    let rk_ref = target.routing_key.as_deref();
    let model_id = target.model_id.as_str();
    Span::current().record("routing.admission_rejection_reason", reason);
    metrics
        .admission_rejections_total(rk_ref, model_id, reason)
        .inc();
    metrics.requests_total(rk_ref, model_id, "", "503").inc();
    warn!(
        routing_key = ?target.routing_key,
        model_id = %model_id,
        rejection_reason = reason,
        "rejecting request before routing due to input-work admission"
    );

    overloaded_response()
}

pub(super) struct RoutingRejectionContext<'a> {
    pub(super) metrics: &'a StargateMetrics,
    pub(super) target: &'a RoutingTargetKey,
    pub(super) rejection: RoutingRejection,
    pub(super) failed_backend_count: usize,
    pub(super) failed_cluster_count: usize,
    pub(super) routing_retry_attempts: u64,
    pub(super) capacity_rejected: bool,
}

pub(super) fn routing_rejection_response(
    context: RoutingRejectionContext<'_>,
) -> Result<Response<Body>, StatusCode> {
    let (status, response) = match context.rejection {
        RoutingRejection::Admission(reason) => {
            return Ok(input_work_admission_rejection_response(
                context.metrics,
                context.target,
                reason,
            ));
        }
        RoutingRejection::NoCandidatesNotFound => ("404", Ok(no_eligible_candidates_response())),
        RoutingRejection::ServiceUnavailable => (
            "503",
            if context.capacity_rejected {
                Ok(overloaded_response())
            } else {
                Err(StatusCode::SERVICE_UNAVAILABLE)
            },
        ),
    };
    let rk_ref = context.target.routing_key.as_deref();
    let model_id = context.target.model_id.as_str();
    if context.failed_backend_count > 0 || context.failed_cluster_count > 0 {
        context
            .metrics
            .proxy_retry_exhausted_total(rk_ref, model_id, "no_eligible_backend")
            .inc();
        Span::current().record("proxy.retry_reason", "no_eligible_backend");
    }
    warn!(
        routing_key = ?context.target.routing_key,
        model_id = %model_id,
        finalization = ?context.rejection,
        failed_backend_count = context.failed_backend_count,
        failed_cluster_count = context.failed_cluster_count,
        routing_retry_attempts = context.routing_retry_attempts,
        "no inference server candidates for routing target"
    );

    context
        .metrics
        .requests_total(rk_ref, model_id, "", status)
        .inc();
    response
}

fn no_eligible_candidates_response() -> Response<Body> {
    json_error_response(
        StatusCode::NOT_FOUND,
        ERROR_NO_ELIGIBLE_CANDIDATES,
        ERROR_NO_ELIGIBLE_CANDIDATES_BODY,
    )
}

fn json_error_response(
    status: StatusCode,
    error_code: &'static str,
    body: &'static str,
) -> Response<Body> {
    let mut response = Response::new(Body::from(body));
    *response.status_mut() = status;
    response.headers_mut().insert(
        HeaderName::from_static(HEADER_STARGATE_ERROR_CODE),
        HeaderValue::from_static(error_code),
    );
    response.headers_mut().insert(
        header::CONTENT_TYPE,
        HeaderValue::from_static("application/json"),
    );
    response
}

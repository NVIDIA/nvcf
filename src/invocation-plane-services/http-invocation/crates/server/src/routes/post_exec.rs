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

use crate::nats::NatsService;
use crate::nvcf_api::NVCFService;
use crate::rate_limit::RateLimitService;
use crate::request_id::RequestId;
use crate::routes::app_error::AppError;
use crate::routes::input_asset_header::InputAssetHeader;
use crate::routes::pexec;
use crate::routes::post_pexec::{FunctionRouting, NVCF_POLL_SECONDS};
use crate::s3::S3Service;
use crate::settings::AppConfig;
use crate::worker_streams::WorkerStreamService;
use anyhow::Context;
use axum::body::Body;
use axum::extract::{Extension, Path, Request, State};
use axum::response::Response;
use axum::{extract, RequestExt};
use axum_extra::headers::authorization::Bearer;
use axum_extra::headers::Authorization;
use axum_extra::TypedHeader;
use extract::Json;
use http::header::{CONTENT_TYPE, LOCATION};
use http::StatusCode;
use http_body_util::BodyExt;
use mime::APPLICATION_JSON;
use problem_details::ProblemDetails;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use serde_with::skip_serializing_none;
use std::sync::Arc;
use tracing::Level;
use uuid::Uuid;

#[skip_serializing_none]
#[derive(Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RequestHeader {
    pub input_asset_references: Option<Vec<String>>,
    pub poll_duration_seconds: Option<i64>,
}

#[skip_serializing_none]
#[derive(Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct InvokeFunctionRequest {
    pub request_header: Option<RequestHeader>,
    pub request_body: Value,
}

#[skip_serializing_none]
#[derive(Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct InvokeFunctionResponse {
    pub req_id: Uuid,
    pub status: InvokeStatus,
    pub response_reference: Option<String>,
    pub percent_complete: Option<i32>,
    pub error_code: Option<i32>,
    pub response: Option<Value>,
}

#[derive(Serialize, Deserialize, Eq, PartialEq, Debug)]
#[serde(rename_all = "kebab-case")]
pub enum InvokeStatus {
    Errored,
    InProgress,
    Fulfilled,
    PendingEvaluation,
    Rejected,
}

#[allow(clippy::too_many_arguments)]
pub async fn exec(
    Path(function_routing): Path<FunctionRouting>,
    State(nvcf_service): State<Arc<NVCFService>>,
    State(nats_service): State<Arc<NatsService>>,
    State(s3_service): State<Arc<S3Service>>,
    State(rate_limit_service): State<Arc<RateLimitService>>,
    State(worker_stream_service): State<Arc<WorkerStreamService>>,
    State(app_config): State<Arc<AppConfig>>,
    Extension(Authorization(bearer)): Extension<Authorization<Bearer>>,
    request: Request,
) -> Result<(StatusCode, Json<InvokeFunctionResponse>), AppError> {
    let function_id = function_routing.function_id;
    let function_version_id = function_routing.function_version_id;

    let span = tracing::Span::current();
    span.record("function_id", function_id.to_string());
    span.record(
        "function_version_id",
        function_version_id
            .map(|id| id.to_string())
            .unwrap_or_default(),
    );

    let (parts, body) = request.with_limited_body().into_parts();
    let body = body
        .collect()
        .await
        .map_err(|err| {
            AppError(
                ProblemDetails::from_status_code(StatusCode::BAD_REQUEST)
                    .with_detail(format!("failed to collect request body: {}", err)),
            )
        })?
        .to_bytes();
    let payload: InvokeFunctionRequest = serde_json::from_slice(&body).map_err(|err| {
        AppError(
            ProblemDetails::from_status_code(StatusCode::BAD_REQUEST).with_detail(err.to_string()),
        )
    })?;
    let mut wrapped_req =
        Request::post(parts.uri).header(CONTENT_TYPE, APPLICATION_JSON.essence_str());

    if let Some(request_header) = &payload.request_header {
        if let Some(poll_duration_seconds) = request_header.poll_duration_seconds {
            wrapped_req = wrapped_req.header(NVCF_POLL_SECONDS, poll_duration_seconds);
        }
    }
    let input_asset_references = payload
        .request_header
        .map(|request_header| request_header.input_asset_references.unwrap_or_default())
        .unwrap_or_default()
        .into_iter()
        .map(|asset_id| Uuid::try_parse(&asset_id))
        .collect::<Result<Vec<_>, _>>()?;

    let serialized_body = payload.request_body.to_string();
    let wrapped_req = wrapped_req.body(Body::from(serialized_body))?;
    let response = pexec(
        Path(function_routing),
        State(nvcf_service),
        State(nats_service),
        State(s3_service),
        State(rate_limit_service),
        State(worker_stream_service),
        State(app_config),
        Extension(Authorization(bearer)),
        TypedHeader(InputAssetHeader(input_asset_references)),
        wrapped_req,
    )
    .await?;
    map_passthrough_response_to_wrapped_response(response).await
}

#[tracing::instrument(level = Level::TRACE, fields(request_id))]
pub async fn map_passthrough_response_to_wrapped_response(
    response: Response,
) -> Result<(StatusCode, Json<InvokeFunctionResponse>), AppError> {
    let (parts, body) = response.into_parts();
    let req_id = parts
        .headers
        .get(RequestId::HEADER_NAME)
        .map_or("", |v| v.to_str().unwrap_or_default())
        .parse()
        .unwrap_or_default();
    let span = tracing::Span::current();
    span.record("request_id", format!("{}", req_id));
    Ok((
        match parts.status {
            StatusCode::FOUND => StatusCode::OK,
            _ => parts.status,
        },
        Json(InvokeFunctionResponse {
            req_id,
            status: match parts.status {
                StatusCode::OK | StatusCode::FOUND => InvokeStatus::Fulfilled,
                StatusCode::ACCEPTED => InvokeStatus::InProgress,
                _ => InvokeStatus::Errored,
            },
            response_reference: match parts.status {
                StatusCode::FOUND => Some(
                    parts
                        .headers
                        .get(LOCATION)
                        .map_or("", |v| v.to_str().unwrap_or_default())
                        .to_string(),
                ),
                _ => None,
            },
            percent_complete: match parts.status {
                StatusCode::OK | StatusCode::FOUND => Some(100),
                _ => None,
            },
            error_code: match parts.status {
                StatusCode::OK | StatusCode::ACCEPTED | StatusCode::FOUND => None,
                _ => Some(parts.status.as_u16().into()),
            },
            response: match parts.status {
                StatusCode::OK => {
                    let response_body = body.collect().await?.to_bytes();
                    let response_body: Value = serde_json::from_slice(&response_body).context(
                        format!("invalid json in response body for request_id: {:?}", req_id),
                    )?;
                    Some(response_body)
                }
                _ => None,
            },
        }),
    ))
}

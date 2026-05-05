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

use crate::metrics;
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use problem_details::ProblemDetails;
use tonic::{Code, Status};
use tracing::Span;

// all app errors should be a problem details format
pub struct AppError(pub ProblemDetails);

// Tell axum how to convert `AppError` into a response.
impl IntoResponse for AppError {
    fn into_response(self) -> Response {
        tracing::warn!(
            "app error response status: {} body: {:?}",
            self.0.status.unwrap_or_default(),
            self.0
        );
        metrics::record_nvcf_application_error(
            self.0.status.unwrap_or_default().as_str().to_string(),
        );
        let span = Span::current();
        span.record("app_error", "true");
        self.0.into_response()
    }
}

// This enables using `?` on functions that return `Result<_, anyhow::Error>` to turn them into
// `Result<_, AppError>`. That way you don't need to do that manually.
impl<E> From<E> for AppError
where
    E: Into<anyhow::Error>,
{
    fn from(err: E) -> Self {
        let err: anyhow::Error = err.into();
        let detail = err
            .chain()
            .map(|cause| cause.to_string())
            .collect::<Vec<_>>()
            .join("; cause: ");
        Self(
            ProblemDetails::from_status_code(StatusCode::INTERNAL_SERVER_ERROR).with_detail(detail),
        )
    }
}

impl AppError {
    // can't use from and into traits because of lack of specialization support and general into anyhow impl
    pub(crate) fn map_nvcf_api_err(err: crate::nvcf_api::Error) -> Self {
        match err {
            crate::nvcf_api::Error::Grpc(err) => AppError(
                ProblemDetails::from_status_code(grpc_status_to_http_status(&err))
                    .with_detail(err.message()),
            ),
            crate::nvcf_api::Error::Other(err) => err.context("failed to auth").into(),
        }
    }

    // Convert errors from the nats submodule to AppErrors.  This function is required because we can't
    // use the from and into traits as they conflict with Rust's blanket "impl<T> From<T> for T".
    pub(crate) fn map_nats_err(err: crate::nats::Error) -> Self {
        match err {
            crate::nats::Error::Subscribe(err) => AppError(
                ProblemDetails::from_status_code(StatusCode::NOT_FOUND)
                    .with_detail(err.to_string()),
            ),
            crate::nats::Error::Publish(err) => AppError(
                ProblemDetails::from_status_code(StatusCode::TOO_MANY_REQUESTS)
                    .with_detail(err.to_string()),
            ),
            crate::nats::Error::Other(err) => err.context("failed to send nats request").into(),
        }
    }
}

fn grpc_status_to_http_status(status: &Status) -> StatusCode {
    match status.code() {
        Code::Ok => StatusCode::OK,
        Code::Cancelled | Code::ResourceExhausted | Code::Aborted | Code::Unavailable => {
            StatusCode::SERVICE_UNAVAILABLE
        }
        Code::InvalidArgument | Code::OutOfRange => StatusCode::BAD_REQUEST,
        Code::DeadlineExceeded => StatusCode::GATEWAY_TIMEOUT,
        Code::NotFound => StatusCode::NOT_FOUND,
        Code::AlreadyExists => StatusCode::CONFLICT,
        Code::PermissionDenied => StatusCode::FORBIDDEN,
        Code::FailedPrecondition => StatusCode::PRECONDITION_FAILED,
        Code::Unimplemented => StatusCode::NOT_IMPLEMENTED,
        Code::Unknown if status.message().contains("Cloud credits expired") => {
            StatusCode::PAYMENT_REQUIRED
        }
        Code::Internal | Code::Unknown | Code::DataLoss => StatusCode::INTERNAL_SERVER_ERROR,
        Code::Unauthenticated => StatusCode::UNAUTHORIZED,
    }
}

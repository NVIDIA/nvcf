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

use crate::nats::{NatsService, WorkerPollingResponse};
use crate::nvcf_api::{
    nvcf::{
        worker_invoke_function_request::{
            stateless_config::{
                connection_config::{Config, Http1ProtocolConfig},
                ConnectionConfig,
            },
            StatelessConfig,
        },
        WorkerInvokeFunctionRequest,
    },
    NVCFService,
};
use crate::request_id::RequestId;
use crate::routes::app_error::AppError;
use crate::routes::http_headers::remove_hop_by_hop_headers;
use crate::settings::AppConfig;
use crate::worker_streams::WorkerStreamService;
use anyhow::Context;
use axum::extract::{Extension, Path, State};
use axum::http::{HeaderMap, StatusCode};
use axum::response::{IntoResponse, Response};
use axum_extra::headers::authorization::Bearer;
use axum_extra::headers::Authorization;
use problem_details::ProblemDetails;
use serde::Deserialize;
use std::default::Default;
use std::sync::Arc;
use std::time::SystemTime;
use tracing::Level;
use tracing::Span;
use uom::si::information;
use uom::si::usize::Information;
use uuid::Uuid;

#[derive(Deserialize)]
pub struct StatusPath {
    pub request_id: Uuid,
}

pub async fn pexec_status_route(
    Path(StatusPath { request_id }): Path<StatusPath>,
    State(nvcf_service): State<Arc<NVCFService>>,
    State(nats_service): State<Arc<NatsService>>,
    State(worker_stream_service): State<Arc<WorkerStreamService>>,
    State(app_config): State<Arc<AppConfig>>,
    Extension(Authorization(bearer)): Extension<Authorization<Bearer>>,
    mut headers: HeaderMap,
) -> Result<Response, AppError> {
    pexec_status(
        request_id,
        nvcf_service,
        nats_service,
        worker_stream_service,
        app_config,
        bearer,
        &mut headers,
    )
    .await
}

#[tracing::instrument(level = Level::DEBUG, skip(nvcf_service, nats_service, worker_stream_service, app_config, bearer))]
pub async fn pexec_status(
    request_id: Uuid,
    nvcf_service: Arc<NVCFService>,
    nats_service: Arc<NatsService>,
    worker_stream_service: Arc<WorkerStreamService>,
    app_config: Arc<AppConfig>,
    bearer: Bearer,
    headers: &mut HeaderMap,
) -> Result<Response, AppError> {
    let request_id: RequestId = request_id.into();
    let request_id_string = request_id.to_string();
    let span = Span::current();
    span.record("request_id", request_id_string.clone());
    remove_hop_by_hop_headers(headers);

    let poll_duration = super::post_pexec::poll_duration(headers, false)
        .context("failed to parse poll duration")
        .map_err(|err| {
            AppError(
                ProblemDetails::from_status_code(StatusCode::BAD_REQUEST)
                    .with_detail(err.to_string()),
            )
        })?;

    let existing_request = match nats_service.get_request_mapping(request_id).await? {
        None => {
            tracing::warn!(
                "no matching request found for request_id: {}",
                request_id_string
            );
            return Ok(ProblemDetails::from_status_code(StatusCode::NOT_FOUND).into_response());
        }
        Some(existing) => {
            tracing::trace!("request located: {:?}", existing);
            existing
        }
    };
    let existing_region = existing_request.region;
    let existing_request = existing_request.worker_result_tracking;

    let function_id: Uuid = existing_request.function_id.parse()?;
    let function_version_id: Uuid = existing_request.function_version_id.parse()?;
    span.record("function_id", existing_request.function_id);
    span.record("function_version_id", existing_request.function_version_id);

    let original_start_time = request_time_to_system_time(existing_request.request_time);
    // will error if the client is not allowed
    span.record("operation", "auth_invocation");
    let allowed_functions = match nvcf_service
        .auth_invocation(bearer, function_id, Some(function_version_id), None)
        .await
    {
        Ok(allowed) => allowed,
        Err(err) => {
            span.record("error", true);
            if let Some(nca_id) = err.nca_id() {
                span.record("nca_id", nca_id);
            }
            return Err(AppError::map_nvcf_api_err(err));
        }
    };
    span.record("nca_id", allowed_functions.authed_client_nca_id.clone());

    let (response_tx, response_rx) = tokio::sync::oneshot::channel();
    let response_authorization_token =
        worker_stream_service.generate_response_token(request_id, response_tx);

    let headers = crate::routes::post_pexec::to_nvcf_request_headers(headers)
        .context("failed to parse http request headers")?;
    let five_mb = Information::new::<information::mebibyte>(5).get::<information::byte>();
    let payload = WorkerInvokeFunctionRequest {
        request_id: request_id.to_string(),
        nca_id: allowed_functions.authed_client_nca_id.clone(),
        subject: allowed_functions.authed_client_subject,
        large_response_url: Default::default(),
        max_direct_response_size_bytes: five_mb as u32,
        input_asset_reference: Default::default(), // assets only supported for initial call
        request_time: Some(SystemTime::now().into()),
        stateful_config: None,
        request_body: None, // body not supported for GET
        direct_response_url: app_config.regional_nvcf_api_grpc_address.clone(),
        request_headers: headers,
        request_method: http::method::Method::GET.as_str().into(),
        request_path: allowed_functions
            .function_version_ids
            .into_iter()
            .find(|fv| fv.function_version_id == function_version_id)
            .ok_or_else(|| anyhow::anyhow!("missing matching function version in auth response"))?
            .default_path
            .unwrap_or_default(),
        stateless_config: Some(StatelessConfig {
            connection_configs: vec![ConnectionConfig {
                config: Some(Config::Http1Config(Http1ProtocolConfig {
                    proxy_uri: worker_stream_service.proxy_address().map(Into::into),
                    target_uri: worker_stream_service.self_address().into(),
                    request_authorization_token: None, // GET requests don't have a request body
                    response_authorization_token: response_authorization_token.token().into(),
                })),
            }],
        }),
    };

    let worker_polling_response = nats_service
        .polling_request(request_id, payload)
        .await
        .unwrap_or(WorkerPollingResponse::NoResponse);

    if worker_polling_response != WorkerPollingResponse::AckedRequest {
        return Err(AppError(
            ProblemDetails::from_status_code(StatusCode::GATEWAY_TIMEOUT)
                .with_detail("worker did not acknowledge the status request."),
        ));
    }

    let response = super::post_pexec::handle_streaming_response(
        nats_service.clone(),
        function_id,
        function_version_id,
        allowed_functions.authed_client_nca_id,
        request_id,
        poll_duration,
        original_start_time,
        Some(&existing_region),
        response_rx,
    )
    .await?
    .into_response();
    // if the status is not 202 we know that we're finished polling
    if response.status() != StatusCode::ACCEPTED {
        if let Err(err) = nats_service
            .purge_record_of_polling_request(&existing_region, request_id)
            .await
        {
            tracing::warn!("failed to purge polling request mapping {request_id} in region {existing_region}, error: {err:?}");
        }
    }
    Ok(response)
}

fn request_time_to_system_time(request_start_time: Option<prost_types::Timestamp>) -> SystemTime {
    if let Some(request_time) = request_start_time {
        match SystemTime::try_from(request_time) {
            Ok(start_time) => start_time,
            _ => SystemTime::now(),
        }
    } else {
        SystemTime::now()
    }
}

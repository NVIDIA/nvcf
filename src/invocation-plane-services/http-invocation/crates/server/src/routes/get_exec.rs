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
use crate::routes::app_error::AppError;
use crate::routes::get_pexec::pexec_status;
use crate::routes::post_exec::InvokeFunctionResponse;
use crate::settings::AppConfig;
use crate::worker_streams::WorkerStreamService;
use axum::extract::{Extension, Path, State};
use axum::Json;
use axum_extra::headers::authorization::Bearer;
use axum_extra::headers::Authorization;
use http::{HeaderMap, StatusCode};
use std::sync::Arc;
use uuid::Uuid;

pub async fn exec_status(
    Path(request_id): Path<Uuid>,
    State(nvcf_service): State<Arc<NVCFService>>,
    State(nats_service): State<Arc<NatsService>>,
    State(worker_stream_service): State<Arc<WorkerStreamService>>,
    State(app_config): State<Arc<AppConfig>>,
    Extension(Authorization(bearer)): Extension<Authorization<Bearer>>,
) -> Result<(StatusCode, Json<InvokeFunctionResponse>), AppError> {
    let response = pexec_status(
        request_id,
        nvcf_service,
        nats_service,
        worker_stream_service,
        app_config,
        bearer,
        &mut HeaderMap::new(),
    )
    .await?;
    tracing::trace!(
        request_id = request_id.to_string(),
        "response status code: {:?}",
        response.status().clone()
    );
    crate::routes::post_exec::map_passthrough_response_to_wrapped_response(response).await
}

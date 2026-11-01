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
use crate::routes::app_error::AppError;
use axum::extract::State;
use axum::response::IntoResponse;
use axum::Json;
use serde::Serialize;
use std::sync::Arc;

#[derive(Serialize)]
struct HealthResponse {
    status: String,
}

/// Readiness: this pod can serve invocations when its own NATS connection is up.
/// nvcf-api is deliberately not checked. Every replica shares it, so gating on
/// it would remove all replicas during an nvcf-api outage and drop in-flight
/// work whose results return through this Service.
pub async fn get_health(
    State(nats_service): State<Arc<NatsService>>,
) -> Result<impl IntoResponse, AppError> {
    if let Err(e) = nats_service.health() {
        tracing::error!("Health check reported NATS connection unhealthy");
        return Err(e.into());
    }

    Ok(Json(HealthResponse {
        status: "OK".into(),
    }))
}

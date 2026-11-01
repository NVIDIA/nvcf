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
use axum::extract::State;
use axum::response::IntoResponse;
use axum::Json;
use serde::Serialize;
use std::sync::Arc;
use std::time::Duration;
use tokio::time::timeout;

#[derive(Serialize)]
struct HealthResponse {
    status: String,
}

pub async fn get_health(
    State(nvcf_service): State<Arc<NVCFService>>,
    State(nats_service): State<Arc<NatsService>>,
) -> Result<impl IntoResponse, AppError> {
    if let Err(e) = nats_service.health() {
        tracing::error!("Health check reported NATS connection unhealthy");
        return Err(e.into());
    }
    // timeout for health in deployment configs is 3s
    if let Err(e) = timeout(Duration::from_secs(2), nvcf_service.health()).await? {
        tracing::error!("Health check could not connect to NVCF");
        return Err(e.into());
    }

    Ok(Json(HealthResponse {
        status: "OK".into(),
    }))
}

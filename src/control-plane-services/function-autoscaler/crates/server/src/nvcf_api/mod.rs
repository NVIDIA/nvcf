/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::fmt;
use uuid::Uuid;

pub mod errors;
pub mod nvcf_client;
pub mod oauth2_client;

pub use errors::NvcfApiError;

#[derive(Debug, Clone, Copy, Serialize, Deserialize, PartialEq)]
pub enum FunctionStatus {
    ACTIVE,
    DEPLOYING,
    ERRORED,
    INACTIVE,
    DELETED,
}

impl fmt::Display for FunctionStatus {
    fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result {
        match self {
            FunctionStatus::ACTIVE => write!(f, "ACTIVE"),
            FunctionStatus::DEPLOYING => write!(f, "DEPLOYING"),
            FunctionStatus::ERRORED => write!(f, "ERRORED"),
            FunctionStatus::INACTIVE => write!(f, "INACTIVE"),
            FunctionStatus::DELETED => write!(f, "DELETED"),
        }
    }
}

#[derive(Debug, Serialize, Deserialize, Clone)]
#[serde(rename_all = "camelCase")]
pub struct AutoscalerRequest {
    pub required_number_of_instances: i32,
    pub predicted_at: DateTime<Utc>,
}

#[derive(Debug, Serialize, Deserialize, Clone)]
#[serde(rename_all = "camelCase")]
pub struct AutoscalerResponse {
    pub active_instances: i32,
    pub pending_instances: i32,
    pub allocating_instances: i32,
    pub terminating_instances: i32,
    pub function_status: FunctionStatus,
}

#[derive(Debug, Clone)]
pub struct DeploymentInfo {
    pub function_id: Uuid,
    pub function_version_id: Uuid,
    pub nca_id: String,
    pub required_number_of_instances: i32,
    pub recently_invoked: bool,
    pub enqueued_at: std::time::Instant,
}

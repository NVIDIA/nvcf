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
use scylla::DeserializeRow;
use uuid::Uuid;

#[derive(Debug, Clone, DeserializeRow)]
pub struct ActiveFunctionDetails {
    pub function_id: Uuid,
    pub function_version_id: Uuid,
    /// Optional for backwards compatibility with existing Cassandra rows that predate this column.
    #[scylla(rename = "account_id")]
    pub nca_id: Option<String>,
    pub last_updated_at: Option<DateTime<Utc>>,
    pub num_workers: Option<i32>,
    pub last_predicted_desired_instance_count: Option<i32>,
    pub last_predicted_error_code: Option<String>,
}

impl ActiveFunctionDetails {
    pub fn new(function_id: Uuid, function_version_id: Uuid, nca_id: String) -> Self {
        Self {
            function_id,
            function_version_id,
            nca_id: Some(nca_id),
            last_updated_at: Some(Utc::now()),
            num_workers: None,
            last_predicted_desired_instance_count: None,
            last_predicted_error_code: None,
        }
    }

    /// Returns nca_id or empty string if not set (for backwards compatibility)
    pub fn nca_id_or_empty(&self) -> &str {
        self.nca_id.as_deref().unwrap_or("")
    }

    /// Alias for nca_id_or_empty for backwards compatibility
    pub fn nca_id_or_nil(&self) -> String {
        self.nca_id.clone().unwrap_or_default()
    }
}

#[derive(Debug, Clone, DeserializeRow)]
pub struct ActiveFunction {
    pub function_id: Uuid,
    pub function_version_id: Uuid,
    #[scylla(rename = "account_id")]
    pub nca_id: String,
}

#[derive(Debug, Clone, DeserializeRow)]
pub struct DistributedLock {
    pub lock_name: String,
    pub node_id: String,
    pub acquired_at: DateTime<Utc>,
}

#[derive(Debug, Clone, DeserializeRow)]
pub struct NodeHealth {
    pub node_id: String,
    pub last_updated_at: DateTime<Utc>,
}

#[derive(Debug, Clone, DeserializeRow)]
pub struct DistributedLockResult {
    #[scylla(rename = "[applied]")]
    pub applied: bool,
    // Conditional writes return the existing row when they are not applied.
    // Lock columns carry independent TTLs in Cassandra and can therefore be
    // null while a different column still keeps the row alive.
    pub lock_name: Option<String>,
    pub node_id: Option<String>,
    pub acquired_at: Option<DateTime<Utc>>,
}

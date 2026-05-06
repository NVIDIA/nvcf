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

use serde::{Deserialize, Serialize};
use serde_with::serde_as;
use std::time::Duration;

const DEFAULT_CONTACT_POINTS: &str = "127.0.0.1";
const DEFAULT_PORT: u32 = 9042;
const DEFAULT_COMPRESSION: &str = "lz4";
const DEFAULT_KEYSPACE: &str = "nvcf_autoscaler";
const DEFAULT_CONNECTION_TIMEOUT: Duration = Duration::from_millis(10000); // 10 seconds
const DEFAULT_CONSISTENCY: &str = "LOCAL_QUORUM";
const DEFAULT_SERIAL_CONSISTENCY: &str = "LOCAL_SERIAL";
const DEFAULT_REQUEST_TIMEOUT: Duration = Duration::from_millis(10000); // 10 seconds
const DEFAULT_MAX_RETRY_COUNT: u32 = 3;
const DEFAULT_RETRY_INTERVAL: Duration = Duration::from_millis(1000); // 1 second
const DEFAULT_HISTORY_PREDICTION_TTL_SECONDS: i32 = 300;

#[serde_as]
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct CassandraSettings {
    pub contact_points: String,
    pub port: u32,
    pub compression: String,
    pub keyspace: String,
    #[serde_as(as = "serde_with::DurationMilliSeconds<u64>")]
    #[serde(rename = "connection_timeout_ms")]
    pub connection_timeout: std::time::Duration,
    pub ssl: SslSettings,
    pub pool: PoolSettings,
    pub execution_profile: ExecutionProfileSettings,
    #[serde(default)]
    pub is_development: bool,
    pub history_prediction_ttl_seconds: i32,
    #[serde(default = "default_node_health_ttl")]
    pub node_health_ttl_seconds: i32,
    #[serde(default = "default_recently_invoked_ttl")]
    pub recently_invoked_ttl_seconds: i32,
    #[serde(default = "default_health_check_cache_ttl")]
    pub health_check_cache_ttl_seconds: u64,
}

fn default_node_health_ttl() -> i32 {
    180
}
fn default_recently_invoked_ttl() -> i32 {
    1800
}
fn default_health_check_cache_ttl() -> u64 {
    30
}

impl Default for CassandraSettings {
    fn default() -> Self {
        Self {
            contact_points: DEFAULT_CONTACT_POINTS.to_string(),
            port: DEFAULT_PORT,
            compression: DEFAULT_COMPRESSION.to_string(),
            keyspace: DEFAULT_KEYSPACE.to_string(),
            connection_timeout: DEFAULT_CONNECTION_TIMEOUT,
            ssl: SslSettings::default(),
            pool: PoolSettings::default(),
            execution_profile: ExecutionProfileSettings::default(),
            is_development: true,
            history_prediction_ttl_seconds: DEFAULT_HISTORY_PREDICTION_TTL_SECONDS,
            node_health_ttl_seconds: default_node_health_ttl(),
            recently_invoked_ttl_seconds: default_recently_invoked_ttl(),
            health_check_cache_ttl_seconds: default_health_check_cache_ttl(),
        }
    }
}

#[derive(Debug, Clone, Deserialize, Serialize, Default)]
pub struct SslSettings {
    pub enabled: bool,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct PoolSettings {
    pub local_size: usize,
}

impl Default for PoolSettings {
    fn default() -> Self {
        Self { local_size: 2 }
    }
}

#[serde_as]
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ExecutionProfileSettings {
    pub consistency: String,
    pub serial_consistency: String,
    #[serde_as(as = "serde_with::DurationMilliSeconds<u64>")]
    #[serde(rename = "request_timeout_ms")]
    pub request_timeout: std::time::Duration,
    pub speculative_execution_policy: SpeculativeExecutionPolicySettings,
}

impl Default for ExecutionProfileSettings {
    fn default() -> Self {
        Self {
            consistency: DEFAULT_CONSISTENCY.to_string(),
            serial_consistency: DEFAULT_SERIAL_CONSISTENCY.to_string(),
            request_timeout: DEFAULT_REQUEST_TIMEOUT,
            speculative_execution_policy: SpeculativeExecutionPolicySettings::default(),
        }
    }
}

#[serde_as]
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct SpeculativeExecutionPolicySettings {
    pub max_retry_count: u32,
    #[serde_as(as = "serde_with::DurationMilliSeconds<u64>")]
    #[serde(rename = "retry_interval_ms")]
    pub retry_interval: std::time::Duration,
}

impl Default for SpeculativeExecutionPolicySettings {
    fn default() -> Self {
        Self {
            max_retry_count: DEFAULT_MAX_RETRY_COUNT,
            retry_interval: DEFAULT_RETRY_INTERVAL,
        }
    }
}

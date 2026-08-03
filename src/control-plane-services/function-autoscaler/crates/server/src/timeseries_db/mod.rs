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

use backon::ExponentialBuilder;
use serde::{Deserialize, Serialize};
use std::path::PathBuf;

mod errors;
pub mod timeseries_db_client;

#[derive(Debug, Clone, Copy, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum TimeseriesDbAuthMode {
    /// Do not authenticate. Intended for local development only.
    #[default]
    None,
    /// Authenticate through the legacy token gateway.
    Token,
    /// Authenticate directly to the tenant endpoint with a client certificate.
    Mtls,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(default)]
pub struct TimeseriesDbSettings {
    /// URL for the authentication service
    pub authn_url: String,
    /// URL for the TimeseriesDb API
    pub timeseries_db_url: String,
    /// Environment for the TimeseriesDb metrics
    pub env: String,
    /// Ignore filter for environment in TimeseriesDb queries
    pub ignore_env: bool,
    /// Authentication mechanism. When omitted, `disable_auth` retains its legacy behavior.
    #[serde(default)]
    pub auth_mode: Option<TimeseriesDbAuthMode>,
    /// Deprecated compatibility setting. Prefer `auth_mode`.
    #[serde(default)]
    pub disable_auth: bool,
    /// PEM client certificate chain used by `mtls` mode.
    #[serde(default)]
    pub client_certificate_path: Option<PathBuf>,
    /// PEM client private key used by `mtls` mode.
    #[serde(default)]
    pub client_private_key_path: Option<PathBuf>,
    #[serde(default = "default_ts_http_timeout")]
    pub http_timeout_seconds: u64,
    #[serde(default = "default_ts_query_backoff")]
    pub query_backoff_max_delay_seconds: u64,
    #[serde(default = "default_ts_auth_backoff")]
    pub auth_backoff_max_delay_seconds: u64,
    /// Optional backoff configuration for retries
    #[serde(skip)]
    pub backoff: Option<ExponentialBuilder>,
}

fn default_ts_http_timeout() -> u64 {
    30
}
fn default_ts_query_backoff() -> u64 {
    5
}
fn default_ts_auth_backoff() -> u64 {
    180
}

impl Default for TimeseriesDbSettings {
    fn default() -> Self {
        Self {
            authn_url: "".to_string(),
            timeseries_db_url: "http://localhost:10903".to_string(),
            auth_mode: None,
            disable_auth: true,
            client_certificate_path: None,
            client_private_key_path: None,
            env: "stg".to_string(),
            ignore_env: false,
            http_timeout_seconds: default_ts_http_timeout(),
            query_backoff_max_delay_seconds: default_ts_query_backoff(),
            auth_backoff_max_delay_seconds: default_ts_auth_backoff(),
            backoff: None,
        }
    }
}

impl TimeseriesDbSettings {
    pub fn effective_auth_mode(&self) -> TimeseriesDbAuthMode {
        self.auth_mode.unwrap_or(if self.disable_auth {
            TimeseriesDbAuthMode::None
        } else {
            TimeseriesDbAuthMode::Token
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn explicit_auth_mode_takes_precedence_over_legacy_setting() {
        let settings = TimeseriesDbSettings {
            auth_mode: Some(TimeseriesDbAuthMode::Mtls),
            disable_auth: true,
            ..Default::default()
        };

        assert_eq!(settings.effective_auth_mode(), TimeseriesDbAuthMode::Mtls);
    }

    #[test]
    fn legacy_auth_setting_remains_compatible() {
        let token_settings = TimeseriesDbSettings {
            disable_auth: false,
            ..Default::default()
        };
        let local_settings = TimeseriesDbSettings {
            disable_auth: true,
            ..Default::default()
        };

        assert_eq!(
            token_settings.effective_auth_mode(),
            TimeseriesDbAuthMode::Token
        );
        assert_eq!(
            local_settings.effective_auth_mode(),
            TimeseriesDbAuthMode::None
        );
    }
}

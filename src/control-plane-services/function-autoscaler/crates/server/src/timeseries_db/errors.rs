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
use std::fmt;

/// Error types that can occur in the TimeseriesDb API client
#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
#[repr(u16)]
pub enum TimeseriesDbApiError {
    /// Authentication/authorization error (401/403)
    AuthError = 1,
    /// Server error (5xx)
    ServerError,
    /// Client error (4xx, excluding auth errors)
    ClientError,
    /// Error reading response body
    BodyReadError,
    /// Network/connection error
    NetworkError,
    /// Success status
    Success,
}

impl fmt::Display for TimeseriesDbApiError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_short_string())
    }
}

impl std::error::Error for TimeseriesDbApiError {}

impl From<TimeseriesDbApiError> for f64 {
    fn from(value: TimeseriesDbApiError) -> Self {
        value as u16 as f64
    }
}

impl From<&TimeseriesDbApiError> for f64 {
    fn from(value: &TimeseriesDbApiError) -> Self {
        *value as u16 as f64
    }
}

impl TimeseriesDbApiError {
    /// Get a short string representation for metrics
    pub fn to_short_string(self) -> String {
        match self {
            TimeseriesDbApiError::AuthError => "AUTH_ERROR".to_string(),
            TimeseriesDbApiError::ServerError => "SERVER_ERROR".to_string(),
            TimeseriesDbApiError::ClientError => "CLIENT_ERROR".to_string(),
            TimeseriesDbApiError::BodyReadError => "BODY_READ_ERROR".to_string(),
            TimeseriesDbApiError::NetworkError => "NETWORK_ERROR".to_string(),
            TimeseriesDbApiError::Success => "SUCCESS".to_string(),
        }
    }
}

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

/// Comprehensive error types that can occur in the NVCF API package
#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
#[repr(u16)]
pub enum NvcfApiError {
    /// Internal error
    InternalError = 1,

    /// Unknown error
    UnknownError,
    /// Function and Scaling Errors
    /// Function not found
    FunctionNotFound,
    /// Function is not in an active state
    FunctionNotActive,
    /// Requested number of instances exceeds maximum allowed or is below minimum required
    InstanceRequestBeyondLimits,
    /// Function is currently deploying
    FunctionDeploying,
    /// Function is in error state
    FunctionError,
    /// Function has been deleted
    FunctionDeleted,

    // Rate Limiting and Queue Errors
    /// Request queue is at full capacity
    QueueFull,
    /// Rate limit exceeded
    RateLimitExceeded,
}

impl fmt::Display for NvcfApiError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_short_string())
    }
}

impl std::error::Error for NvcfApiError {}

impl From<NvcfApiError> for f64 {
    fn from(value: NvcfApiError) -> Self {
        value as u16 as f64
    }
}

impl From<&NvcfApiError> for f64 {
    fn from(value: &NvcfApiError) -> Self {
        *value as u16 as f64
    }
}

impl NvcfApiError {
    /// Get a short string representation for database storage
    pub fn to_short_string(&self) -> String {
        match self {
            // Function errors
            NvcfApiError::FunctionNotFound => "FUNCTION_NOT_FOUND".to_string(),
            NvcfApiError::FunctionNotActive => "FUNCTION_NOT_ACTIVE".to_string(),
            NvcfApiError::FunctionDeploying => "FUNCTION_DEPLOYING".to_string(),
            NvcfApiError::FunctionError => "FUNCTION_ERROR".to_string(),
            NvcfApiError::FunctionDeleted => "FUNCTION_DELETED".to_string(),
            NvcfApiError::InstanceRequestBeyondLimits => {
                "INSTANCE_REQUEST_BEYOND_LIMITS".to_string()
            }

            // Rate limiting errors
            NvcfApiError::QueueFull => "QUEUE_FULL".to_string(),
            NvcfApiError::RateLimitExceeded => "RATE_LIMIT_EXCEEDED".to_string(),

            // Internal errors
            NvcfApiError::InternalError => "INTERNAL_ERROR".to_string(),
            NvcfApiError::UnknownError => "UNKNOWN_ERROR".to_string(),
        }
    }
}

/// Error types that can occur in the OAuth2 API client
#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
#[repr(u16)]
pub enum OAuth2ApiError {
    /// Network/connection error
    NetworkError = 100,
    /// HTTP error response
    HttpError,
    /// JSON parsing error
    ParseError,
    /// Missing access token in response
    MissingAccessToken,
    /// Missing expires_in field in response
    MissingExpiresIn,
    /// Success status
    Success,
}

impl fmt::Display for OAuth2ApiError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_short_string())
    }
}

impl std::error::Error for OAuth2ApiError {}

impl From<OAuth2ApiError> for f64 {
    fn from(value: OAuth2ApiError) -> Self {
        value as u16 as f64
    }
}

impl From<&OAuth2ApiError> for f64 {
    fn from(value: &OAuth2ApiError) -> Self {
        *value as u16 as f64
    }
}

impl OAuth2ApiError {
    /// Get a short string representation for metrics
    pub fn to_short_string(&self) -> String {
        match self {
            OAuth2ApiError::NetworkError => "NETWORK_ERROR".to_string(),
            OAuth2ApiError::HttpError => "HTTP_ERROR".to_string(),
            OAuth2ApiError::ParseError => "PARSE_ERROR".to_string(),
            OAuth2ApiError::MissingAccessToken => "MISSING_ACCESS_TOKEN".to_string(),
            OAuth2ApiError::MissingExpiresIn => "MISSING_EXPIRES_IN".to_string(),
            OAuth2ApiError::Success => "SUCCESS".to_string(),
        }
    }
}

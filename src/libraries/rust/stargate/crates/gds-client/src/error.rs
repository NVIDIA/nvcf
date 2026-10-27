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

//! The crate error type and helpers for GDS error details.

use std::collections::HashMap;

use prost::Message;
use tonic::{Code, Status};

/// `ErrorInfo.domain` of every GDS error.
pub const ERROR_DOMAIN: &str = "gds.nvidia.com";

/// `ErrorInfo.reason` values the GDS server returns. They mirror
/// `api/errors.go` of the global-discovery module.
pub mod reason {
    /// FAILED_PRECONDITION: the write carried a generation that does not
    /// exceed the stored one. Metadata `current_generation` holds the stored
    /// generation.
    pub const STALE_GENERATION: &str = "STALE_GENERATION";
    /// ABORTED: a heartbeat carried an epoch other than the current one.
    pub const LEASE_EPOCH_MISMATCH: &str = "LEASE_EPOCH_MISMATCH";
    /// FAILED_PRECONDITION or NOT_FOUND: the cluster is not registered.
    pub const CLUSTER_NOT_REGISTERED: &str = "CLUSTER_NOT_REGISTERED";
    /// ALREADY_EXISTS: another pool uses the name in (clusterset, cluster).
    pub const POOL_NAME_TAKEN: &str = "POOL_NAME_TAKEN";
    /// INVALID_ARGUMENT: an endpoint set exceeds the limit.
    pub const TOO_MANY_ENDPOINTS: &str = "TOO_MANY_ENDPOINTS";
    /// RESOURCE_EXHAUSTED: the replica serves its maximum of watchers.
    pub const TOO_MANY_WATCHERS: &str = "TOO_MANY_WATCHERS";
    /// UNAVAILABLE: the backing store did not answer.
    pub const STORE_UNAVAILABLE: &str = "STORE_UNAVAILABLE";
    /// UNAVAILABLE: the replica is draining.
    pub const SHUTTING_DOWN: &str = "SHUTTING_DOWN";
    /// `ErrorInfo.metadata` key carrying the stored generation of a
    /// STALE_GENERATION error.
    pub const METADATA_CURRENT_GENERATION: &str = "current_generation";
}

/// Errors returned by the GDS client.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// The endpoint list is empty.
    #[error("no GDS endpoint configured")]
    NoEndpoints,
    /// A configured URL cannot be used.
    #[error("invalid GDS URL {url:?}: {reason}")]
    InvalidUrl { url: String, reason: String },
    /// Every configured and advertised endpoint failed to connect.
    #[error("all {attempted} GDS endpoints failed; last error: {last_error}")]
    AllEndpointsFailed {
        attempted: usize,
        last_error: String,
    },
    /// Connecting to one endpoint failed.
    #[error("connect to GDS endpoint {url}: {source}")]
    Connect {
        url: String,
        #[source]
        source: tonic::transport::Error,
    },
    /// Connecting to one endpoint, TLS and HTTP/2 handshakes included, took
    /// longer than the connect timeout.
    #[error("connect to GDS endpoint {url} timed out after {timeout:?}")]
    ConnectTimeout {
        url: String,
        timeout: std::time::Duration,
    },
    /// The TLS options are invalid.
    #[error("GDS TLS configuration: {0}")]
    Tls(String),
    /// The server returned an error status, or the call timed out.
    #[error("GDS RPC failed: {code:?}: {message}", code = .0.code(), message = .0.message())]
    Rpc(#[from] Status),
    /// A pool registration can no longer be renewed.
    #[error("GDS pool registration lost: {0}")]
    RegistrationLost(String),
    /// The client, cache or registration was stopped.
    #[error("GDS client shut down")]
    Shutdown,
}

impl Error {
    /// The gRPC status of an [`Error::Rpc`].
    pub fn status(&self) -> Option<&Status> {
        match self {
            Self::Rpc(status) => Some(status),
            _ => None,
        }
    }

    /// The gRPC code of an [`Error::Rpc`].
    pub fn code(&self) -> Option<Code> {
        self.status().map(Status::code)
    }

    /// The GDS `ErrorInfo` of an [`Error::Rpc`], when the server sent one.
    pub fn details(&self) -> Option<ErrorDetails> {
        self.status().and_then(error_details)
    }

    /// The GDS `ErrorInfo.reason` of an [`Error::Rpc`].
    pub fn reason(&self) -> Option<String> {
        self.details().map(|details| details.reason)
    }

    /// Whether a later attempt, possibly against another replica, can
    /// succeed.
    pub fn is_retryable(&self) -> bool {
        match self {
            Self::Rpc(status) => is_retryable_code(status.code()),
            Self::AllEndpointsFailed { .. }
            | Self::Connect { .. }
            | Self::ConnectTimeout { .. } => true,
            _ => false,
        }
    }
}

/// Codes on which the client retries a unary call and fails over to the next
/// endpoint.
pub(crate) fn is_retryable_code(code: Code) -> bool {
    matches!(code, Code::Unavailable | Code::DeadlineExceeded)
}

/// The `google.rpc.ErrorInfo` of a GDS error status.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ErrorDetails {
    pub reason: String,
    pub domain: String,
    pub metadata: HashMap<String, String>,
}

impl ErrorDetails {
    /// The stored generation of a STALE_GENERATION error.
    pub fn current_generation(&self) -> Option<u64> {
        self.metadata
            .get(reason::METADATA_CURRENT_GENERATION)
            .and_then(|value| value.parse().ok())
    }
}

const ERROR_INFO_TYPE_URL: &str = "type.googleapis.com/google.rpc.ErrorInfo";

// google.rpc.Status and google.rpc.ErrorInfo, declared here so the crate
// needs no googleapis dependency to read error details.
#[derive(Clone, PartialEq, prost::Message)]
struct RpcStatus {
    #[prost(int32, tag = "1")]
    code: i32,
    #[prost(string, tag = "2")]
    message: String,
    #[prost(message, repeated, tag = "3")]
    details: Vec<prost_types::Any>,
}

#[derive(Clone, PartialEq, prost::Message)]
struct ErrorInfo {
    #[prost(string, tag = "1")]
    reason: String,
    #[prost(string, tag = "2")]
    domain: String,
    #[prost(map = "string, string", tag = "3")]
    metadata: HashMap<String, String>,
}

/// Decodes the GDS `ErrorInfo` from the `grpc-status-details-bin` of a
/// status. Returns `None` when the status carries none.
pub fn error_details(status: &Status) -> Option<ErrorDetails> {
    let details = status.details();
    if details.is_empty() {
        return None;
    }
    let rpc_status = RpcStatus::decode(details).ok()?;
    rpc_status
        .details
        .iter()
        .filter(|any| any.type_url == ERROR_INFO_TYPE_URL)
        .find_map(|any| ErrorInfo::decode(any.value.as_slice()).ok())
        .map(|info| ErrorDetails {
            reason: info.reason,
            domain: info.domain,
            metadata: info.metadata,
        })
}

/// Builds a status carrying a GDS `ErrorInfo`, as the server does. Fakes and
/// tests use it.
pub fn status_with_reason(
    code: Code,
    message: impl Into<String>,
    reason: &str,
    metadata: HashMap<String, String>,
) -> Status {
    let message = message.into();
    let info = ErrorInfo {
        reason: reason.to_string(),
        domain: ERROR_DOMAIN.to_string(),
        metadata,
    };
    let rpc_status = RpcStatus {
        code: code as i32,
        message: message.clone(),
        details: vec![prost_types::Any {
            type_url: ERROR_INFO_TYPE_URL.to_string(),
            value: info.encode_to_vec(),
        }],
    };
    Status::with_details(code, message, rpc_status.encode_to_vec().into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn error_details_round_trip() {
        let status = status_with_reason(
            Code::FailedPrecondition,
            "stale",
            reason::STALE_GENERATION,
            HashMap::from([(
                reason::METADATA_CURRENT_GENERATION.to_string(),
                "42".to_string(),
            )]),
        );
        let error = Error::from(status);
        let details = error.details().expect("details");
        assert_eq!(details.reason, reason::STALE_GENERATION);
        assert_eq!(details.domain, ERROR_DOMAIN);
        assert_eq!(details.current_generation(), Some(42));
        assert_eq!(error.reason().as_deref(), Some(reason::STALE_GENERATION));
        assert!(!error.is_retryable());
    }

    #[test]
    fn plain_status_has_no_details() {
        let error = Error::from(Status::unavailable("down"));
        assert!(error.details().is_none());
        assert!(error.is_retryable());
        assert_eq!(error.code(), Some(Code::Unavailable));
    }

    #[test]
    fn display_omits_details_bytes() {
        let error = Error::from(Status::not_found("pool p"));
        assert_eq!(error.to_string(), "GDS RPC failed: NotFound: pool p");
    }
}

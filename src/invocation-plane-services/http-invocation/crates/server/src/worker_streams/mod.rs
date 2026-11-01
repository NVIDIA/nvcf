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

use ahash::RandomState;
use dashmap::DashMap;
use http::uri::Authority;
use http::Uri;
use serde::{Deserialize, Serialize};
use std::fmt;
use std::net::Ipv4Addr;
use std::str::FromStr;
use std::sync::Arc;
use tokio::sync::oneshot;
use uuid::Uuid;

use crate::request_id::RequestId;

mod droppable_body;
mod known_size_stream_body;

pub use droppable_body::DroppableBody;
pub use known_size_stream_body::{new_from_buf_and_body_data_stream, KnownSizeStreamBody};

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct WorkerStreamProperties {
    // the address which should uniquely identify this server for worker callbacks
    // if not provided it will be built from pod_ip and service_address
    pub self_address: Option<String>,
    // the address of an http proxy server used to reach this server, if necessary
    pub proxy_address: Option<String>,
    // internal pod ip
    pub pod_ip: Option<String>,
    // k8s service name of this server's deployment group. used with pod_ip to build a unique address.
    pub service_address: Option<String>,
    // if true, do not send any http request info in the nats message. send it all in the worker callback.
    pub stream_full_request: bool,
}

// Token kind enum to differentiate between request and response tokens
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TokenKind {
    Request,
    Response,
}

#[derive(thiserror::Error, Debug)]
pub enum TokenError {
    #[error("Invalid token")]
    InvalidToken,
}

// Data stream for requests
#[derive(Debug)]
pub enum RequestDataStream {
    Raw(axum::body::Body),
    HttpRequest(Box<http::Request<axum::body::Body>>),
}

// Request entry struct
#[derive(Debug)]
pub struct RequestEntry {
    pub request_id: RequestId,
    pub data_stream: oneshot::Receiver<RequestDataStream>,
}

// Response entry struct
#[derive(Debug)]
pub struct ResponseEntry {
    pub request_id: RequestId,
    pub response_writer: oneshot::Sender<http::Response<axum::body::Body>>,
}

// Unified token type that handles both request and response tokens
pub struct StreamToken {
    token: String,
    kind: TokenKind,
    service: Arc<WorkerStreamService>,
}

impl StreamToken {
    pub fn token(&self) -> &str {
        &self.token
    }

    pub fn kind(&self) -> TokenKind {
        self.kind
    }
}

impl fmt::Display for StreamToken {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.token)
    }
}

impl Drop for StreamToken {
    fn drop(&mut self) {
        match self.kind {
            TokenKind::Request => {
                let self1 = &self.service;
                let token = &self.token;
                self1.request_tokens.remove(token);
            }
            TokenKind::Response => {
                let self1 = &self.service;
                let token = &self.token;
                self1.response_tokens.remove(token);
            }
        }
    }
}

#[derive(Debug)]
pub struct WorkerStreamService {
    properties: WorkerStreamProperties,
    self_address: String,
    request_tokens: DashMap<String, RequestEntry, RandomState>,
    response_tokens: DashMap<String, ResponseEntry, RandomState>,
}

impl WorkerStreamService {
    pub fn new(properties: &WorkerStreamProperties) -> anyhow::Result<Self> {
        let mut self_address = if let Some(self_address) = &properties.self_address {
            let uri = Uri::try_from(self_address)?;
            if uri.path() != "/" && !uri.path().is_empty() || uri.query().is_some() {
                return Err(anyhow::anyhow!("Self address should not contain a path"));
            }
            uri.to_string()
        } else if let (Some(pod_ip), Some(service_address)) =
            (&properties.pod_ip, &properties.service_address)
        {
            Ipv4Addr::from_str(pod_ip)?; // validate expected ipv4
            let pod_ip = pod_ip.replace(".", "-");
            let uri = Uri::try_from(service_address)?;

            // Disallow paths in service address
            if uri.path() != "/" && !uri.path().is_empty() || uri.query().is_some() {
                return Err(anyhow::anyhow!("Service address should not contain a path"));
            }

            let mut parts = uri.into_parts();
            parts.authority = Some(Authority::try_from(format!(
                "{}.{}",
                pod_ip,
                parts
                    .authority
                    .ok_or_else(|| anyhow::anyhow!("Missing authority"))?
                    .as_str()
            ))?);
            Uri::from_parts(parts)?.to_string()
        } else {
            return Err(anyhow::anyhow!("Missing self address"));
        };
        // Remove trailing slash that Uri::from_parts adds
        if self_address.ends_with('/') {
            self_address.pop();
        }
        Ok(Self {
            self_address,
            properties: properties.clone(),
            request_tokens: DashMap::with_hasher(RandomState::new()),
            response_tokens: DashMap::with_hasher(RandomState::new()),
        })
    }

    pub fn generate_request_token(
        self: &Arc<Self>,
        request_id: RequestId,
        data_stream: oneshot::Receiver<RequestDataStream>,
    ) -> StreamToken {
        let token = Uuid::new_v4().to_string();
        let entry = RequestEntry {
            request_id,
            data_stream,
        };
        self.request_tokens.insert(token.clone(), entry);
        StreamToken {
            token,
            kind: TokenKind::Request,
            service: self.clone(),
        }
    }

    pub fn generate_response_token(
        self: &Arc<Self>,
        request_id: RequestId,
        response_writer: oneshot::Sender<http::Response<axum::body::Body>>,
    ) -> StreamToken {
        let token = Uuid::new_v4().to_string();
        let entry = ResponseEntry {
            request_id,
            response_writer,
        };
        self.response_tokens.insert(token.clone(), entry);
        StreamToken {
            token,
            kind: TokenKind::Response,
            service: self.clone(),
        }
    }

    pub fn validate_request_token(&self, token: &str) -> Result<RequestEntry, TokenError> {
        // Remove the token and return the request ID
        if let Some((_, entry)) = self.request_tokens.remove(token) {
            Ok(entry)
        } else {
            Err(TokenError::InvalidToken)
        }
    }

    pub fn validate_response_token(&self, token: &str) -> Result<ResponseEntry, TokenError> {
        // Remove the token and return the request ID
        if let Some((_, entry)) = self.response_tokens.remove(token) {
            Ok(entry)
        } else {
            Err(TokenError::InvalidToken)
        }
    }

    pub fn self_address(&self) -> &str {
        &self.self_address
    }

    pub fn proxy_address(&self) -> Option<&str> {
        self.properties.proxy_address.as_deref()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_new_with_self_address() {
        let properties = WorkerStreamProperties {
            self_address: Some("http://example.com/".to_string()),
            ..Default::default()
        };

        let service = WorkerStreamService::new(&properties).expect("Should create service");
        assert_eq!(service.self_address(), "http://example.com");
    }

    #[test]
    fn test_new_with_self_address_and_pod_ip() {
        let properties = WorkerStreamProperties {
            self_address: Some("http://example.com".to_string()),
            pod_ip: Some("192.168.1.1".to_string()),
            service_address: Some("http://service.namespace.svc.cluster.local".to_string()),
            ..Default::default()
        };

        let service = WorkerStreamService::new(&properties).expect("Should create service");
        // self_address should take precedence over pod_ip and service_address
        assert_eq!(service.self_address(), "http://example.com");
    }

    #[test]
    fn test_new_with_pod_ip_and_service_address() {
        let properties = WorkerStreamProperties {
            pod_ip: Some("192.168.1.1".to_string()),
            service_address: Some("http://service.namespace.svc.cluster.local".to_string()),
            ..Default::default()
        };

        let service = WorkerStreamService::new(&properties).expect("Should create service");
        assert_eq!(
            service.self_address(),
            "http://192-168-1-1.service.namespace.svc.cluster.local"
        );
    }

    #[test]
    fn test_new_missing_required_fields() {
        let properties = WorkerStreamProperties::default();
        let result = WorkerStreamService::new(&properties);
        assert!(result.is_err());

        let error = result.unwrap_err();
        assert!(error.to_string().contains("Missing self address"));
    }

    #[test]
    fn test_new_invalid_pod_ip() {
        let properties = WorkerStreamProperties {
            pod_ip: Some("not-an-ip".to_string()),
            service_address: Some("http://service.namespace.svc.cluster.local".to_string()),
            ..Default::default()
        };

        let result = WorkerStreamService::new(&properties);
        assert!(result.is_err());
    }

    #[test]
    fn test_new_invalid_service_address() {
        let properties = WorkerStreamProperties {
            pod_ip: Some("192.168.1.1".to_string()),
            service_address: Some("http://[invalid-url".to_string()),
            ..Default::default()
        };

        let result = WorkerStreamService::new(&properties);
        assert!(result.is_err());
    }

    #[test]
    fn test_new_service_address_without_authority() {
        let properties = WorkerStreamProperties {
            pod_ip: Some("192.168.1.1".to_string()),
            service_address: Some("/path/without/authority".to_string()),
            ..Default::default()
        };

        let result = WorkerStreamService::new(&properties);
        assert!(result.is_err());
    }

    #[test]
    fn test_new_service_address_with_path() {
        let properties = WorkerStreamProperties {
            pod_ip: Some("192.168.1.1".to_string()),
            service_address: Some("http://service.namespace.svc.cluster.local/path".to_string()),
            ..Default::default()
        };

        let result = WorkerStreamService::new(&properties);
        assert!(result.is_err());
        let error = result.unwrap_err();
        assert!(error.to_string().contains("should not contain a path"));
    }

    #[test]
    fn test_partial_properties() {
        // Only pod_ip without service_address
        let properties = WorkerStreamProperties {
            pod_ip: Some("192.168.1.1".to_string()),
            ..Default::default()
        };
        let result = WorkerStreamService::new(&properties);
        assert!(result.is_err());

        // Only service_address without pod_ip
        let properties = WorkerStreamProperties {
            service_address: Some("http://service.namespace.svc.cluster.local".to_string()),
            ..Default::default()
        };
        let result = WorkerStreamService::new(&properties);
        assert!(result.is_err());
    }

    #[test]
    fn test_new_with_self_address_with_path() {
        let properties = WorkerStreamProperties {
            self_address: Some("http://example.com/path".to_string()),
            ..Default::default()
        };

        let result = WorkerStreamService::new(&properties);
        assert!(result.is_err());
        let error = result.unwrap_err();
        assert!(error.to_string().contains("should not contain a path"));
    }

    #[test]
    fn test_new_with_trailing_slash_in_self_address() {
        let properties = WorkerStreamProperties {
            self_address: Some("http://example.com/".to_string()),
            ..Default::default()
        };

        let service = WorkerStreamService::new(&properties).expect("Should create service");
        assert_eq!(service.self_address(), "http://example.com");
    }

    #[test]
    fn test_new_with_trailing_slash_in_service_address() {
        let properties = WorkerStreamProperties {
            pod_ip: Some("192.168.1.1".to_string()),
            service_address: Some("http://service.namespace.svc.cluster.local/".to_string()),
            ..Default::default()
        };

        let service = WorkerStreamService::new(&properties).expect("Should create service");
        assert_eq!(
            service.self_address(),
            "http://192-168-1-1.service.namespace.svc.cluster.local"
        );
    }
}

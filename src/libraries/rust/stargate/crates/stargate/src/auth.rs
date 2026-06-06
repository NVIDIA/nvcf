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

use std::sync::Arc;

use anyhow::{Context, Result};
use stargate_auth::AuthTokenProvider;
use stargate_proto::gateway_pb::AuthLlmWorkerRequest;
use stargate_proto::gateway_pb::llm_gateway_client::LlmGatewayClient;
use tonic::transport::Channel;
use tracing::debug;

pub struct AuthResult {
    pub routing_key: Option<String>,
}

#[async_trait::async_trait]
pub trait WorkerAuthenticator: Send + Sync {
    async fn authenticate(&self, token: Option<&str>) -> Result<AuthResult>;
}

pub struct GrpcWorkerAuthenticator {
    client: LlmGatewayClient<Channel>,
    token_provider: Option<Arc<AuthTokenProvider>>,
}

impl GrpcWorkerAuthenticator {
    pub async fn connect(
        endpoint: &str,
        token_provider: Option<AuthTokenProvider>,
    ) -> Result<Self> {
        let channel = Channel::from_shared(endpoint.to_string())?
            .connect()
            .await?;
        Ok(Self {
            client: LlmGatewayClient::new(channel),
            token_provider: token_provider.map(Arc::new),
        })
    }
}

#[async_trait::async_trait]
impl WorkerAuthenticator for GrpcWorkerAuthenticator {
    async fn authenticate(&self, token: Option<&str>) -> Result<AuthResult> {
        let mut request = tonic::Request::new(AuthLlmWorkerRequest {
            worker_token: token.unwrap_or("").to_string(),
        });
        if let Some(provider) = &self.token_provider {
            let auth_token = provider.resolve_token().await?;
            let header_value: tonic::metadata::MetadataValue<tonic::metadata::Ascii> =
                format!("Bearer {auth_token}")
                    .parse()
                    .context("invalid auth token")?;
            request.metadata_mut().insert("authorization", header_value);
        }
        let response = self.client.clone().auth_llm_worker(request).await?;
        let inner = response.into_inner();
        debug!(routing_key = %inner.routing_key, "worker authenticated");
        Ok(AuthResult {
            routing_key: Some(inner.routing_key),
        })
    }
}

pub struct OpenAuthenticator;

#[async_trait::async_trait]
impl WorkerAuthenticator for OpenAuthenticator {
    async fn authenticate(&self, _token: Option<&str>) -> Result<AuthResult> {
        Ok(AuthResult { routing_key: None })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn bearer_metadata_attached_with_static_provider() {
        let provider = Arc::new(AuthTokenProvider::Static("test-token".to_string()));
        let auth_token = provider.resolve_token().await.unwrap();
        let header_value: tonic::metadata::MetadataValue<tonic::metadata::Ascii> =
            format!("Bearer {auth_token}").parse().unwrap();

        let mut request = tonic::Request::new(());
        request.metadata_mut().insert("authorization", header_value);

        let got = request
            .metadata()
            .get("authorization")
            .unwrap()
            .to_str()
            .unwrap();
        assert_eq!(got, "Bearer test-token");
    }
}

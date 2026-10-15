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
use stargate_proto::gateway_pb::llm_gateway_client::LlmGatewayClient;
use stargate_proto::gateway_pb::{AuthLlmWorkerRequest, AuthLlmWorkerResponse};
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
        let request = build_gateway_auth_request(token, self.token_provider.as_deref()).await?;
        let response = self.client.clone().auth_llm_worker(request).await?;
        let result = auth_result_from_gateway_response(response.into_inner());
        debug!(routing_key = ?result.routing_key, "worker authenticated");
        Ok(result)
    }
}

async fn build_gateway_auth_request(
    token: Option<&str>,
    token_provider: Option<&AuthTokenProvider>,
) -> Result<tonic::Request<AuthLlmWorkerRequest>> {
    let mut request = tonic::Request::new(AuthLlmWorkerRequest {
        worker_token: token.unwrap_or("").to_string(),
    });
    if let Some(provider) = token_provider {
        let auth_token = provider.resolve_token().await?;
        let header_value: tonic::metadata::MetadataValue<tonic::metadata::Ascii> =
            format!("Bearer {auth_token}")
                .parse()
                .context("invalid auth token")?;
        request.metadata_mut().insert("authorization", header_value);
    }
    Ok(request)
}

fn auth_result_from_gateway_response(response: AuthLlmWorkerResponse) -> AuthResult {
    let routing_key = response.routing_key.trim();
    AuthResult {
        routing_key: if routing_key.is_empty() {
            None
        } else {
            Some(routing_key.to_string())
        },
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
    use axum::Router;
    use axum::body::{Body, Bytes};
    use axum::extract::State;
    use axum::http::{HeaderMap, StatusCode};
    use axum::response::{IntoResponse, Response};
    use axum::routing::post;
    use prost::Message;
    use std::sync::Mutex;
    use tokio::net::TcpListener;

    #[tokio::test]
    async fn grpc_authenticator_sends_worker_token_and_gateway_authorization() {
        let gateway = FakeGatewayState::new("tenant-a");
        let (endpoint, handle) = start_fake_gateway(gateway.clone()).await;
        let authenticator = GrpcWorkerAuthenticator::connect(
            &endpoint,
            Some(AuthTokenProvider::Static("gateway-token".to_string())),
        )
        .await
        .expect("authenticator should connect to fake gateway");

        let result = authenticator
            .authenticate(Some("worker-token"))
            .await
            .expect("gateway auth should succeed");

        assert_eq!(result.routing_key.as_deref(), Some("tenant-a"));
        let requests = gateway.requests();
        assert_eq!(requests.len(), 1);
        assert_eq!(requests[0].worker_token, "worker-token");
        assert_eq!(
            requests[0].authorization.as_deref(),
            Some("Bearer gateway-token")
        );

        handle.abort();
    }

    #[tokio::test]
    async fn grpc_authenticator_normalizes_gateway_routing_key() {
        let gateway = FakeGatewayState::new(" tenant-a ");
        let (endpoint, handle) = start_fake_gateway(gateway).await;
        let authenticator = GrpcWorkerAuthenticator::connect(&endpoint, None)
            .await
            .expect("authenticator should connect to fake gateway");

        let result = authenticator
            .authenticate(Some("worker-token"))
            .await
            .expect("gateway auth should succeed");

        assert_eq!(result.routing_key.as_deref(), Some("tenant-a"));

        handle.abort();
    }

    #[tokio::test]
    async fn grpc_authenticator_treats_blank_gateway_routing_key_as_unscoped() {
        let gateway = FakeGatewayState::new("   ");
        let (endpoint, handle) = start_fake_gateway(gateway.clone()).await;
        let authenticator = GrpcWorkerAuthenticator::connect(&endpoint, None)
            .await
            .expect("authenticator should connect to fake gateway");

        let result = authenticator
            .authenticate(None)
            .await
            .expect("gateway auth should succeed");

        assert_eq!(result.routing_key, None);
        let requests = gateway.requests();
        assert_eq!(requests.len(), 1);
        assert_eq!(requests[0].worker_token, "");
        assert_eq!(requests[0].authorization, None);

        handle.abort();
    }

    #[derive(Clone)]
    struct FakeGatewayState {
        routing_key: String,
        requests: Arc<Mutex<Vec<CapturedGatewayAuthRequest>>>,
    }

    impl FakeGatewayState {
        fn new(routing_key: &str) -> Self {
            Self {
                routing_key: routing_key.to_string(),
                requests: Arc::new(Mutex::new(Vec::new())),
            }
        }

        fn requests(&self) -> Vec<CapturedGatewayAuthRequest> {
            self.requests
                .lock()
                .expect("fake gateway request mutex should not be poisoned")
                .clone()
        }
    }

    #[derive(Clone, Debug, PartialEq, Eq)]
    struct CapturedGatewayAuthRequest {
        worker_token: String,
        authorization: Option<String>,
    }

    async fn start_fake_gateway(
        gateway: FakeGatewayState,
    ) -> (String, tokio::task::JoinHandle<()>) {
        let app = Router::new()
            .route(
                "/llm_gateway.LlmGateway/AuthLlmWorker",
                post(fake_gateway_auth),
            )
            .with_state(gateway);
        let listener = TcpListener::bind("127.0.0.1:0")
            .await
            .expect("fake gateway should bind");
        let addr = listener
            .local_addr()
            .expect("fake gateway should expose local address");
        let handle = tokio::spawn(async move {
            axum::serve(listener, app)
                .await
                .expect("fake gateway should serve requests");
        });

        (format!("http://{addr}"), handle)
    }

    async fn fake_gateway_auth(
        State(gateway): State<FakeGatewayState>,
        headers: HeaderMap,
        body: Bytes,
    ) -> Response {
        let request = match decode_grpc_message::<AuthLlmWorkerRequest>(&body) {
            Ok(request) => request,
            Err(status) => return status.into_response(),
        };
        let authorization = headers
            .get("authorization")
            .and_then(|value| value.to_str().ok())
            .map(ToOwned::to_owned);
        gateway
            .requests
            .lock()
            .expect("fake gateway request mutex should not be poisoned")
            .push(CapturedGatewayAuthRequest {
                worker_token: request.worker_token,
                authorization,
            });

        encode_grpc_message(AuthLlmWorkerResponse {
            routing_key: gateway.routing_key.clone(),
        })
    }

    fn decode_grpc_message<M>(body: &[u8]) -> Result<M, StatusCode>
    where
        M: Message + Default,
    {
        if body.len() < 5 || body[0] != 0 {
            return Err(StatusCode::BAD_REQUEST);
        }
        let message_len = u32::from_be_bytes([body[1], body[2], body[3], body[4]]) as usize;
        if body.len() != 5 + message_len {
            return Err(StatusCode::BAD_REQUEST);
        }
        M::decode(&body[5..]).map_err(|_| StatusCode::BAD_REQUEST)
    }

    fn encode_grpc_message<M>(message: M) -> Response
    where
        M: Message,
    {
        let mut encoded = Vec::new();
        message
            .encode(&mut encoded)
            .expect("fake gateway response should encode");
        let mut body = Vec::with_capacity(5 + encoded.len());
        body.push(0);
        body.extend_from_slice(&(encoded.len() as u32).to_be_bytes());
        body.extend_from_slice(&encoded);

        Response::builder()
            .status(StatusCode::OK)
            .header("content-type", "application/grpc")
            .header("grpc-status", "0")
            .body(Body::from(body))
            .expect("fake gateway response should build")
    }
}

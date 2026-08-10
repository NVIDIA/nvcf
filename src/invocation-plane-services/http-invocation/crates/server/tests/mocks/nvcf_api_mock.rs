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

use std::{collections::HashMap, net::SocketAddr, sync::Arc, time::Duration};
use tokio::{net::TcpListener, sync::Mutex, time};
use tonic::metadata::MetadataValue;
use tonic::transport::Server;
use tonic::{Request, Response, Status};
use tonic_health::server::{health_reporter, HealthReporter};
use tonic_health::ServingStatus;
use uuid::Uuid;

use nvcf_invocation_service::nvcf_api::NCA_ID_METADATA_KEY;

use nvcf_invocation_service::nvcf_api::{
    nvcf::{
        client_invoke_response::FunctionVersion,
        invocation_server::{Invocation, InvocationServer},
        ClientInvokeRequest, ClientInvokeResponse,
    },
    NVCFApiProperties,
};

#[derive(Debug, Default)]
pub struct ApiMock {
    pub functions: HashMap<Uuid, FunctionMetadata>,
    pub clients: HashMap<String, ApiClient>,
}

#[derive(Debug)]
pub struct FunctionMetadata {
    pub functions: Vec<Uuid>,
    pub has_rate_limit: bool,
    pub sync_check: bool,
}

#[derive(Debug)]
pub struct ApiClient {
    pub subject: String,
    pub nca_id: String,
}

pub const HEALTH_CACHE_TTL: u16 = 1;
pub const AUTH_CACHE_TTL: u16 = 1;

impl ApiMock {
    /// Creates a Status with nca_id in metadata (matching nvcf-service behavior)
    fn status_with_nca_id(code: tonic::Code, message: &str, nca_id: &str) -> Status {
        let mut status = Status::new(code, message);
        if let Ok(value) = nca_id.parse::<MetadataValue<tonic::metadata::Ascii>>() {
            status.metadata_mut().insert(NCA_ID_METADATA_KEY, value);
        }
        status
    }

    async fn auth_client_invocation(
        &self,
        request: Request<ClientInvokeRequest>,
    ) -> Result<Response<ClientInvokeResponse>, Status> {
        let request = request.into_inner();
        let client = self
            .clients
            .get(&request.client_authorization_token)
            .ok_or_else(|| Status::unauthenticated("client is unauthorized"))?;
        let function_id = request
            .function_id
            .parse()
            .map_err(|err| Status::invalid_argument(format!("{}", err)))?;
        let function_metadata = self.functions.get(&function_id).ok_or_else(|| {
            // Include nca_id in error metadata (matching nvcf-service GrpcNcaIdException behavior)
            Self::status_with_nca_id(tonic::Code::NotFound, "function not found", &client.nca_id)
        })?;
        let requested_function_version_id = request.function_version_id;
        Ok(Response::new(ClientInvokeResponse {
            function_id: function_id.into(),
            function_versions: function_metadata
                .functions
                .iter()
                .map(|fv| fv.to_string())
                .filter(|fv| {
                    requested_function_version_id
                        .as_ref()
                        .map(|requested_fv| fv == requested_fv)
                        .unwrap_or(true)
                })
                .map(|fv| FunctionVersion {
                    function_version_id: fv,
                    default_invocation_path: None,
                    has_rate_limit: function_metadata.has_rate_limit,
                    sync_check: function_metadata.sync_check,
                })
                .collect(),
            client_auth_subject: client.subject.clone(),
            client_nca_id: match request.target_nca_id {
                None => client.nca_id.clone(),
                Some(nca_id) => nca_id,
            },
        }))
    }

    pub async fn into_server(self) -> ApiMockServer {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let (reporter, health_server) = health_reporter();
        let api_mock = Arc::new(Mutex::new(self));
        let service = ApiMockService::new(Arc::clone(&api_mock));
        let serve = Server::builder()
            .add_service(InvocationServer::new(service))
            .add_service(health_server)
            .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener));
        let handle =
            tokio::spawn(async move { serve.await.expect("mock nvcf api should start up") });
        tracing::info!("nvcf api mock started");
        ApiMockServer {
            address,
            handle,
            reporter: reporter.into(),
            api_mock,
        }
    }
}

// Wrapper for a mutable ApiMock instance
#[derive(Clone)]
pub struct ApiMockService {
    api_mock: Arc<Mutex<ApiMock>>,
}

impl ApiMockService {
    pub fn new(api_mock: Arc<Mutex<ApiMock>>) -> Self {
        ApiMockService { api_mock }
    }
}

/// Implement your gRPC service trait methods, locking the Arc as needed
#[tonic::async_trait]
impl Invocation for ApiMockService {
    async fn auth_client_invocation(
        &self,
        request: Request<ClientInvokeRequest>,
    ) -> Result<Response<ClientInvokeResponse>, Status> {
        // Lock the underlying ApiMock so we can mutate or query it
        let api_mock = self.api_mock.lock().await;
        api_mock.auth_client_invocation(request).await
    }
}

pub struct ApiMockServer {
    address: SocketAddr,
    handle: tokio::task::JoinHandle<()>,
    reporter: tokio::sync::Mutex<HealthReporter>,
    api_mock: Arc<Mutex<ApiMock>>,
}

#[allow(unused)]
impl ApiMockServer {
    pub fn address(&self) -> String {
        self.address.to_string()
    }
    pub fn properties(&self) -> NVCFApiProperties {
        NVCFApiProperties {
            // For testing purpose, set lowered expiration times for health & auth caches
            health_cache_ttl: Duration::from_secs(HEALTH_CACHE_TTL.into()),
            auth_cache_ttl: Duration::from_secs(AUTH_CACHE_TTL.into()),
        }
    }
    pub async fn set_healthy(&self) {
        self.reporter
            .lock()
            .await
            .set_service_status("", ServingStatus::Serving)
            .await;
    }
    pub async fn set_unhealthy(&self) {
        self.reporter
            .lock()
            .await
            .set_service_status("", ServingStatus::NotServing)
            .await;
    }
    pub async fn delete_function(&mut self, function_id: Uuid) {
        let mut api_mock = self.api_mock.lock().await;
        if let Some(removed) = api_mock.functions.remove(&function_id) {
            tracing::warn!("Removed entry: {:?}", removed);
        } else {
            tracing::warn!("Function {:?} not found", function_id);
        }
        // Wait for auth cache to expire to ensure that the API returns
        // the expected result on next query.
        time::sleep(Duration::from_secs(AUTH_CACHE_TTL.into())).await;
    }
}

impl Drop for ApiMockServer {
    fn drop(&mut self) {
        self.handle.abort()
    }
}

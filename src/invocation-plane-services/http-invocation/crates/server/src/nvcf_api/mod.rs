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

pub mod auth_client_service;
pub mod oauth2_client;

use crate::metrics::record_grpc_client_response;
use crate::nvcf_api::auth_client_service::{AuthLayer, AuthSvc};
use crate::nvcf_api::nvcf::invocation_client::InvocationClient;
use crate::nvcf_api::nvcf::ClientInvokeRequest;
use crate::nvcf_api::oauth2_client::TokenProducer;
use crate::secrets::secret_provider::SecretFileWatcher;
use crate::settings::GrpcClientConfig;
use anyhow::Context;
use axum_extra::headers::authorization::Bearer;
use moka::future::CacheBuilder;
use oauth2::Scope;
use serde::{Deserialize, Serialize};
use std::sync::Arc;
use std::time::Duration;
use tonic::transport::Channel;
use tonic::Code;
use tonic_health::pb::health_check_response::ServingStatus;
use tonic_health::pb::health_client::HealthClient;
use tonic_health::pb::HealthCheckRequest;
use tonic_tracing_opentelemetry::middleware::client::{OtelGrpcLayer, OtelGrpcService};
use tracing::Level;
use uuid::Uuid;

pub mod nvcf {
    #![allow(clippy::all)]
    tonic::include_proto!("nvcf");
}

#[derive(Eq, PartialEq, Hash, Debug)]
struct InvocationCacheKey {
    user_auth: String,
    function_id: Uuid,
    function_version_id: Option<Uuid>,
    target_nca_id: Option<String>,
}

pub struct NVCFService {
    client: InvocationClient<OtelGrpcService<AuthSvc>>,
    auth_cache:
        moka::future::Cache<InvocationCacheKey, AllowedFunctionInvocations, ahash::RandomState>,
    health_cache: moka::future::Cache<(), (), ahash::RandomState>,
    health_client: HealthClient<OtelGrpcService<Channel>>,
    nvcf_api_address: String, // for metrics
}

#[derive(Deserialize, Serialize, Clone, Debug)]
pub struct NVCFApiProperties {
    pub health_cache_ttl: Duration,
    pub auth_cache_ttl: Duration,
}

impl Default for NVCFApiProperties {
    fn default() -> Self {
        Self {
            health_cache_ttl: Duration::from_secs(20),
            auth_cache_ttl: Duration::from_secs(60),
        }
    }
}

#[derive(Clone, Debug)]
pub struct AllowedFunctionInvocations {
    pub function_id: Uuid,
    pub function_version_ids: Vec<AllowedFunctionVersion>,
    pub authed_client_subject: String,
    pub authed_client_nca_id: String,
}

#[derive(Clone, Debug)]
pub struct AllowedFunctionVersion {
    pub function_version_id: Uuid,
    pub default_path: Option<String>,
    pub has_rate_limit: bool,
    pub sync_check: bool,
}

/// Metadata key for nca_id in gRPC error responses (matches NvcfConstants.TAG_NCA_ID)
pub const NCA_ID_METADATA_KEY: &str = "nca_id";

#[derive(thiserror::Error, Debug)]
pub enum Error {
    /// Error from the underlying tonic grpc client
    #[error("GRPC error: {0}")]
    Grpc(#[from] tonic::Status),
    /// There was an error from something else
    #[error("Other error: {0}")]
    Other(#[from] anyhow::Error),
}

impl Error {
    /// Extracts the NCA ID from gRPC error metadata, if present.
    /// The NVCF API includes the NCA ID in error responses when available.
    pub fn nca_id(&self) -> Option<String> {
        match self {
            Error::Grpc(status) => status
                .metadata()
                .get(NCA_ID_METADATA_KEY)
                .and_then(|v| v.to_str().ok())
                .map(|s| s.to_string()),
            Error::Other(_) => None,
        }
    }
}

impl NVCFService {
    pub async fn new(
        nvcf_address: &str,
        oauth2_token_endpoint: &str,
        secrets_provider: Option<Arc<SecretFileWatcher>>,
        nvcf_api_properties: &Option<NVCFApiProperties>,
        grpc_config: &GrpcClientConfig,
    ) -> anyhow::Result<Self> {
        let token_provider = TokenProducer::new(
            oauth2_token_endpoint,
            Scope::new("invocation:check_invocation".into()),
            secrets_provider,
            Some(|fixed| fixed.nvcf_api_token),
            grpc_config,
        )?;
        tracing::info!("connecting to nvcf api {}", nvcf_address);

        let channel = grpc_config.build_grpc_channel(nvcf_address).await?;
        let health_channel = tower::ServiceBuilder::new()
            .layer(OtelGrpcLayer)
            .service(channel.clone());
        let channel = tower::ServiceBuilder::new()
            .layer(OtelGrpcLayer)
            .layer(AuthLayer::new(token_provider))
            .service(channel);
        let client = InvocationClient::new(channel);
        let health_client = HealthClient::new(health_channel);
        tracing::info!("connected to nvcf api {}", nvcf_address);
        let props = nvcf_api_properties.clone().unwrap_or_default();

        Ok(Self {
            client,
            health_client,
            auth_cache: CacheBuilder::new(1024)
                .name("nvcf_auth_cache")
                .time_to_live(props.auth_cache_ttl)
                .build_with_hasher(ahash::RandomState::default()),
            health_cache: CacheBuilder::new(1)
                .name("nvcf_health_cache")
                .time_to_live(props.health_cache_ttl)
                .build_with_hasher(ahash::RandomState::default()),
            nvcf_api_address: nvcf_address.to_string(),
        })
    }

    #[tracing::instrument(level = Level::TRACE, skip(self), err)]
    pub async fn health(&self) -> anyhow::Result<()> {
        self.health_cache
            .try_get_with((), async {
                let health_check_response = self
                    .health_client
                    .clone()
                    .check(HealthCheckRequest::default())
                    .await?
                    .into_inner();
                match health_check_response.status() {
                    ServingStatus::Serving => Ok(()),
                    _ => Err(anyhow::anyhow!("nvcf api is not healthy")),
                }
            })
            .await
            .map_err(|err| anyhow::anyhow!(err.to_string()))
    }

    #[tracing::instrument(level = Level::DEBUG, skip(self, user_auth), err)]
    pub async fn auth_invocation(
        &self,
        user_auth: Bearer,
        function_id: Uuid,
        function_version_id: Option<Uuid>,
        target_nca_id: Option<String>, // used for super admins (KAS) to invoke on behalf of a given nca id
    ) -> Result<AllowedFunctionInvocations, Error> {
        let allowed_functions = self
            .auth_cache
            .try_get_with(
                InvocationCacheKey {
                    user_auth: user_auth.token().to_string(),
                    function_id,
                    function_version_id,
                    target_nca_id: target_nca_id.clone(),
                },
                self.auth_invocation_uncached(
                    user_auth,
                    function_id,
                    function_version_id,
                    target_nca_id,
                ),
            )
            .await
            .map_err(|err| match err.as_ref() {
                Error::Grpc(err) => Error::Grpc(err.clone()),
                Error::Other(err) => Error::Other(anyhow::anyhow!(err.to_string())),
            })?;
        Ok(allowed_functions)
    }

    #[tracing::instrument(level = Level::TRACE, skip(self, user_auth), err)]
    async fn auth_invocation_uncached(
        &self,
        user_auth: Bearer,
        function_id: Uuid,
        function_version_id: Option<Uuid>,
        target_nca_id: Option<String>, // used for super admins (KAS) to invoke on behalf of a given nca id
    ) -> Result<AllowedFunctionInvocations, Error> {
        let response = self
            .client
            .clone()
            .auth_client_invocation(ClientInvokeRequest {
                client_authorization_token: user_auth.token().into(),
                function_id: function_id.into(),
                function_version_id: function_version_id.map(|fv| fv.into()),
                target_nca_id,
            })
            .await;
        match &response {
            Ok(_) => {
                record_grpc_client_response(self.nvcf_api_address.clone(), Code::Ok);
            }
            Err(status) => {
                record_grpc_client_response(self.nvcf_api_address.clone(), status.code());
            }
        };
        let response = response?.into_inner();
        let allowed_function_id = response
            .function_id
            .parse()
            .context("failed to parse function id")?;
        let versions = response
            .function_versions
            .into_iter()
            .map(|fv| {
                fv.function_version_id
                    .parse()
                    .context("failed to parse function version id")
                    .map(|function_version_id| AllowedFunctionVersion {
                        function_version_id,
                        default_path: fv.default_invocation_path,
                        has_rate_limit: fv.has_rate_limit,
                        sync_check: fv.sync_check,
                    })
            })
            .collect::<Result<Vec<_>, _>>()?;
        if versions.is_empty() {
            return Err(
                anyhow::anyhow!("auth response must contain at least one version id").into(),
            );
        }
        Ok(AllowedFunctionInvocations {
            function_id: allowed_function_id,
            function_version_ids: versions,
            authed_client_subject: response.client_auth_subject,
            authed_client_nca_id: response.client_nca_id,
        })
    }
}

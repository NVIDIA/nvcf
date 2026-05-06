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

use crate::metrics::record_grpc_client_response;
use crate::nvcf_api::auth_client_service::{AuthLayer, AuthSvc};
use crate::nvcf_api::oauth2_client::TokenProducer;
use crate::rate_limit::rate_limit_api::rate_limit_service_client::RateLimitServiceClient;
use crate::rate_limit::rate_limit_api::{RateLimitRequest, RateLimitResult};
use crate::secrets::secret_provider::SecretFileWatcher;
use crate::settings::GrpcClientConfig;
use oauth2::Scope;
use std::sync::Arc;
use std::time::Duration;
use tokio::time::timeout;
use tonic::{Code, Status};
use tonic_tracing_opentelemetry::middleware::client::{OtelGrpcLayer, OtelGrpcService};
use tracing::Instrument;
use tracing::Level;
use uuid::Uuid;

pub mod rate_limit_api {
    #![allow(clippy::all)]
    tonic::include_proto!("nvcfratelimiter");
}

pub struct RateLimitService {
    client: RateLimitServiceClient<OtelGrpcService<AuthSvc>>,
    rate_limit_cache: moka::sync::Cache<RateLimitCacheKey, (), ahash::RandomState>,
    rate_limit_address: String, // for metrics
    enabled: bool,
}

#[derive(thiserror::Error, Debug)]
enum Error {
    /// Error from the underlying tonic grpc client
    #[error("GRPC error: {0}")]
    Grpc(#[from] Status),
    /// Call was explicitly disallowed
    #[error("limited")]
    Limited,
}

#[derive(Eq, PartialEq, Debug)]
pub enum LimitResult {
    Allowed,
    Limited,
}

#[derive(Eq, PartialEq, Hash, Debug)]
struct RateLimitCacheKey {
    nca_id: String,
    function_id: Uuid,
    function_version_id: Uuid,
}

impl RateLimitService {
    pub async fn new(
        rate_limit_enabled: bool,
        rate_limit_address: &str,
        oauth2_token_endpoint: &str,
        secrets_provider: Option<Arc<SecretFileWatcher>>,
        grpc_config: &GrpcClientConfig,
    ) -> anyhow::Result<Self> {
        let token_provider = TokenProducer::new(
            oauth2_token_endpoint,
            Scope::new("ratelimit:check_invocation".into()),
            secrets_provider,
            Some(|fixed| fixed.rate_limit_token),
            grpc_config,
        )?;
        tracing::info!("connecting to nvcf rate limiter {}", rate_limit_address);

        let channel = if rate_limit_enabled {
            grpc_config.build_grpc_channel(rate_limit_address).await?
        } else {
            grpc_config.build_grpc_channel_lazy(rate_limit_address)?
        };

        let channel = tower::ServiceBuilder::new()
            .layer(OtelGrpcLayer)
            .layer(AuthLayer::new(token_provider))
            .service(channel);
        let client = RateLimitServiceClient::new(channel);
        tracing::info!("connected to nvcf rate limiter {}", rate_limit_address);

        Ok(Self {
            enabled: rate_limit_enabled,
            client,
            rate_limit_cache: moka::sync::CacheBuilder::default()
                .name("rate_limit_cache")
                .time_to_live(Duration::from_secs(60))
                .build_with_hasher(ahash::RandomState::default()),
            rate_limit_address: rate_limit_address.to_string(),
        })
    }

    /// Checks rate limit with a mixed synchronous/asynchronous approach:
    /// 1. **Immediate In-Memory Check**: Instantly returns if previously not surpassing the rate limit.
    /// 2. **Background Check for Non-Limited Requests**: Fires off a background task to verify with the rate limiter service if not currently limited in memory.
    ///    - If the background check later determines the rate is limited, in-memory state is updated to block future requests until reset.
    /// 3. **Synchronous Check for Already Limited Requests**: If currently rate-limited in memory, performs synchronous check with the rate limiter service to confirm the status.
    /// 4. **Synchronous Check for function with sync_check set to true**. If function config has sync_check set to true, always performs synchronous check with the rate limiter service to confirm the status.
    pub async fn check_rate_limit(
        &self,
        nca_id: String,
        function_id: Uuid,
        function_version_id: Uuid,
        sync_check: bool,
    ) -> LimitResult {
        if !self.enabled {
            return LimitResult::Allowed;
        }
        // Is the call known to have been rate limited? Or is sync_check set to true?
        if sync_check
            || self.rate_limit_cache.contains_key(&RateLimitCacheKey {
                nca_id: nca_id.clone(),
                function_id,
                function_version_id,
            })
        {
            // Perform synchronous recheck with rate limiter.
            let result = Self::check_rate_external(
                self.client.clone(),
                self.rate_limit_address.clone(),
                nca_id.clone(),
                function_id,
                function_version_id,
            )
            .await;
            // Keep refreshing the in-memory state if limited, but let a ttl clear the future synchronous checks.
            if let Err(Error::Limited) = result {
                self.rate_limit_cache.insert(
                    RateLimitCacheKey {
                        nca_id,
                        function_id,
                        function_version_id,
                    },
                    (),
                );
                return LimitResult::Limited;
            }
            return LimitResult::Allowed;
        }

        // Not currently rate-limited in memory; initiate background check
        tokio::spawn(timeout(Duration::from_secs(10), {
            let client = self.client.clone();
            let rate_limit_cache = self.rate_limit_cache.clone();
            let rate_limit_address = self.rate_limit_address.clone();
            async move {
                // Background task to check with the rate limiter service
                if let Err(Error::Limited) = Self::check_rate_external(
                    client,
                    rate_limit_address,
                    nca_id.clone(),
                    function_id,
                    function_version_id,
                )
                .await
                {
                    // Upon rate limit, update in-memory flag to prevent future requests
                    rate_limit_cache.insert(
                        RateLimitCacheKey {
                            nca_id,
                            function_id,
                            function_version_id,
                        },
                        (),
                    );
                }
            }
            .in_current_span()
        }));

        // Immediately return without awaiting the background task's result
        LimitResult::Allowed
    }

    #[tracing::instrument(level = Level::TRACE, skip(client, rate_limit_address), err)]
    async fn check_rate_external(
        mut client: RateLimitServiceClient<OtelGrpcService<AuthSvc>>,
        rate_limit_address: String,
        nca_id: String,
        function_id: Uuid,
        function_version_id: Uuid,
    ) -> Result<(), Error> {
        let response = client
            .rate_limit(RateLimitRequest {
                nca_id,
                function_id: function_id.to_string(),
                function_version_id: function_version_id.to_string(),
            })
            .await;
        match &response {
            Ok(_) => {
                record_grpc_client_response(rate_limit_address, Code::Ok);
            }
            Err(status) => {
                record_grpc_client_response(rate_limit_address, status.code());
            }
        };
        let response = response?.into_inner();
        match response.result() {
            RateLimitResult::Allow => Ok(()),
            RateLimitResult::Disallow => Err(Error::Limited),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::settings::GrpcClientConfig;

    #[tokio::test]
    async fn test_rate_limit_service_new_disabled() {
        let grpc_config = GrpcClientConfig::default();

        // Test creating RateLimitService with rate limiting disabled
        let result = RateLimitService::new(
            false, // rate_limit_enabled = false
            "http://localhost:9090",
            "https://example.com/token",
            None,
            &grpc_config,
        )
        .await;

        assert!(
            result.is_ok(),
            "Should successfully create disabled rate limit service"
        );

        let service = result.unwrap();
        assert!(!service.enabled, "Service should be disabled");
    }

    #[tokio::test]
    async fn test_check_rate_limit_disabled_service() {
        let grpc_config = GrpcClientConfig::default();

        let service = RateLimitService::new(
            false, // disabled
            "http://localhost:9090",
            "https://example.com/token",
            None,
            &grpc_config,
        )
        .await
        .unwrap();

        // Test that disabled service always returns Allowed
        let result = service
            .check_rate_limit(
                "test-nca".to_string(),
                uuid::Uuid::new_v4(),
                uuid::Uuid::new_v4(),
                false,
            )
            .await;

        assert_eq!(result, LimitResult::Allowed);
    }
}

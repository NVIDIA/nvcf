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

use super::policy_client::PolicyClient;
use super::{CustomScalingConfig, ScalingFactors, ScalingThresholds};
use crate::nvcf_api::oauth2_client::OAuth2Client;
use anyhow::Result;
use chrono::Utc;
use moka::future::Cache;
use std::sync::Arc;
use std::time::Duration;
use uuid::Uuid;

/// Cache for per-function custom scaling policies
pub struct PolicyCache {
    /// TTL cache with function_version_id as key
    cache: Cache<Uuid, CustomScalingConfig>,
    /// gRPC client for fetching policies
    grpc_client: Arc<PolicyClient>,
    /// Default fallback policy
    default_thresholds: ScalingThresholds,
    default_factors: ScalingFactors,
}

impl std::fmt::Debug for PolicyCache {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("PolicyCache")
            .field("cache_entry_count", &self.cache.entry_count())
            .field("default_thresholds", &self.default_thresholds)
            .field("default_factors", &self.default_factors)
            .finish_non_exhaustive()
    }
}

impl PolicyCache {
    /// Create a new PolicyCache with the specified TTL and defaults
    pub fn new(
        grpc_endpoint: String,
        oauth2_client: Arc<OAuth2Client>,
        ttl_seconds: u64,
        max_capacity: u64,
        default_thresholds: ScalingThresholds,
        default_factors: ScalingFactors,
    ) -> Result<Self> {
        let cache = Cache::builder()
            .max_capacity(max_capacity)
            .time_to_live(Duration::from_secs(ttl_seconds))
            .build();

        let grpc_client = Arc::new(PolicyClient::new_lazy(grpc_endpoint, oauth2_client)?);

        Ok(Self {
            cache,
            grpc_client,
            default_thresholds,
            default_factors,
        })
    }

    /// Get policy from cache or fetch from gRPC service
    pub async fn get_or_fetch(&self, function_version_id: Uuid) -> Result<CustomScalingConfig> {
        // Try cache first
        if let Some(config) = self.cache.get(&function_version_id).await {
            tracing::debug!("Cache hit for function_version_id: {}", function_version_id);
            return Ok(config);
        }

        tracing::debug!(
            "Cache miss for function_version_id: {}, fetching from gRPC",
            function_version_id
        );

        // Cache miss - fetch from gRPC
        match self.fetch_from_grpc(function_version_id).await {
            Ok(config) => {
                // Store in cache
                self.cache.insert(function_version_id, config.clone()).await;
                tracing::info!(
                    "Successfully fetched and cached custom policy for function_version_id: {}",
                    function_version_id
                );
                Ok(config)
            }
            Err(e) => {
                tracing::warn!(
                    "Failed to fetch custom policy for {}: {:#}, will use default",
                    function_version_id,
                    e
                );
                Err(e)
            }
        }
    }

    /// Fetch policy from gRPC service
    async fn fetch_from_grpc(&self, function_version_id: Uuid) -> Result<CustomScalingConfig> {
        self.grpc_client.fetch_policy(function_version_id).await
    }

    /// Get default configuration (used as fallback)
    pub fn get_default_config(&self, function_version_id: Uuid) -> CustomScalingConfig {
        CustomScalingConfig {
            function_version_id,
            scaling_thresholds: self.default_thresholds.clone(),
            scaling_factors: self.default_factors.clone(),
            scale_up_stickiness: None,
            scale_down_stickiness: None,
            fetched_at: Utc::now(),
        }
    }

    /// Manually invalidate a function's cached policy (useful for testing or forced refresh)
    pub async fn invalidate(&self, function_version_id: &Uuid) {
        self.cache.invalidate(function_version_id).await;
        tracing::info!(
            "Invalidated cache entry for function_version_id: {}",
            function_version_id
        );
    }

    /// Get cache statistics
    pub fn get_stats(&self) -> (u64, u64) {
        (self.cache.entry_count(), self.cache.weighted_size())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::secrets::secrets_file_watcher::SecretFileWatcher;
    use tempfile::TempDir;
    use tokio::fs;

    async fn create_test_oauth2_client() -> (Arc<OAuth2Client>, TempDir) {
        let temp_dir = TempDir::new().unwrap();
        let secrets_file = temp_dir.path().join("secrets.json");
        let secrets_content = serde_json::json!({
            "kv": {
                "nvcf_api": {
                    "client_id": "test_client_id",
                    "client_secret": "test_client_secret"
                }
            }
        });
        fs::write(&secrets_file, secrets_content.to_string())
            .await
            .unwrap();
        let secrets_watcher = SecretFileWatcher::new(&secrets_file).await.unwrap();
        let oauth2_client =
            OAuth2Client::new("http://localhost:0".to_string(), Arc::new(secrets_watcher)).unwrap();
        (Arc::new(oauth2_client), temp_dir)
    }

    #[tokio::test]
    async fn test_policy_cache_creation() {
        let (oauth2_client, _tmp) = create_test_oauth2_client().await;
        let cache = PolicyCache::new(
            "http://localhost:50051".to_string(),
            oauth2_client,
            86400,
            10000,
            ScalingThresholds::default(),
            ScalingFactors::default(),
        )
        .unwrap();

        assert_eq!(cache.cache.entry_count(), 0);
    }

    #[tokio::test]
    async fn test_get_default_config() {
        let (oauth2_client, _tmp) = create_test_oauth2_client().await;
        let cache = PolicyCache::new(
            "http://localhost:50051".to_string(),
            oauth2_client,
            86400,
            10000,
            ScalingThresholds::default(),
            ScalingFactors::default(),
        )
        .unwrap();

        let fvid = Uuid::new_v4();
        let default_config = cache.get_default_config(fvid);

        assert_eq!(default_config.function_version_id, fvid);
    }

    #[tokio::test]
    async fn test_cache_invalidation() {
        let (oauth2_client, _tmp) = create_test_oauth2_client().await;
        let cache = PolicyCache::new(
            "http://localhost:50051".to_string(),
            oauth2_client,
            86400,
            10000,
            ScalingThresholds::default(),
            ScalingFactors::default(),
        )
        .unwrap();

        let fvid = Uuid::new_v4();

        // Manually insert something
        cache
            .cache
            .insert(fvid, cache.get_default_config(fvid))
            .await;

        // Verify it's there
        assert!(cache.cache.get(&fvid).await.is_some());

        // Invalidate
        cache.invalidate(&fvid).await;

        // Verify it's gone
        assert!(cache.cache.get(&fvid).await.is_none());
    }
}

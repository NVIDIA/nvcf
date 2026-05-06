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

use crate::metrics;
use crate::nvcf_api::errors::OAuth2ApiError;
use crate::secrets::secrets_file_watcher::SecretFileWatcher;
use reqwest::ClientBuilder;
use reqwest_middleware::ClientWithMiddleware;
use std::sync::Arc;
use std::time::{Duration, Instant};

const DEFAULT_HTTP_TIMEOUT: Duration = Duration::from_secs(30);
const TOKEN_REFRESH_INTERVAL: Duration = Duration::from_secs(3 * 60); // 3 minutes
const TOKEN_REFRESH_THRESHOLD_PERCENT: u64 = 20; // Refresh when 20% time remaining
use tokio::sync::RwLock;
use tracing;

#[derive(Debug, Clone)]
pub struct CachedToken {
    pub access_token: String,
    pub expires_at: Instant,
    pub expires_in: u64,
}

#[derive(Debug, Clone)]
pub struct OAuth2Client {
    pub oauth2_api_url: String,
    pub secrets_watcher: Arc<SecretFileWatcher>,
    pub http_client: reqwest_middleware::ClientWithMiddleware,
    pub token_cache: Arc<RwLock<Option<CachedToken>>>,
}

impl OAuth2Client {
    pub fn new(
        oauth2_api_url: String,
        secrets_watcher: Arc<SecretFileWatcher>,
    ) -> Result<Self, anyhow::Error> {
        Self::with_timeouts(
            oauth2_api_url,
            secrets_watcher,
            DEFAULT_HTTP_TIMEOUT,
            TOKEN_REFRESH_INTERVAL,
        )
    }

    pub fn with_timeouts(
        oauth2_api_url: String,
        secrets_watcher: Arc<SecretFileWatcher>,
        http_timeout: Duration,
        token_refresh_interval: Duration,
    ) -> Result<Self, anyhow::Error> {
        let client = Self {
            oauth2_api_url: oauth2_api_url.to_string(),
            secrets_watcher,
            http_client: reqwest_middleware::ClientBuilder::new(
                ClientBuilder::new().timeout(http_timeout).build().unwrap(),
            )
            .with(reqwest_tracing::TracingMiddleware::default())
            .build(),
            token_cache: Arc::new(RwLock::new(None)),
        };

        // Start background token refresh task
        client.start_token_refresh_task(token_refresh_interval);

        Ok(client)
    }

    fn start_token_refresh_task(&self, token_refresh_interval: Duration) {
        let token_cache = self.token_cache.clone();
        let oauth2_api_url = self.oauth2_api_url.clone();
        let secrets_watcher = self.secrets_watcher.clone();
        let http_client = self.http_client.clone();
        tracing::info!("Starting token refresh task for OAuth2 client");
        tokio::spawn(async move {
            let mut interval = tokio::time::interval(token_refresh_interval);
            interval.tick().await; // Skip first immediate tick

            loop {
                // Check if token needs refresh (refresh when 75% of lifetime has passed)
                let needs_refresh = {
                    let cache = token_cache.read().await;
                    match cache.as_ref() {
                        Some(cached_token) => {
                            let time_remaining = cached_token
                                .expires_at
                                .saturating_duration_since(Instant::now());
                            // If time remaining is less than TOKEN_REFRESH_THRESHOLD_PERCENT% of expires_in, refresh
                            time_remaining
                                <= Duration::from_secs(
                                    cached_token.expires_in * TOKEN_REFRESH_THRESHOLD_PERCENT / 100,
                                )
                        }
                        None => true, // No token cached, need to fetch
                    }
                };

                if needs_refresh {
                    if let Err(e) = Self::refresh_token_internal(
                        &token_cache,
                        &oauth2_api_url,
                        &secrets_watcher,
                        &http_client,
                    )
                    .await
                    {
                        tracing::error!("Failed to refresh token in background task: {}", e);
                    } else {
                        tracing::debug!("Successfully refreshed token in background task");
                    }
                }
                interval.tick().await;
            }
        });
    }

    pub async fn get_jwt_token(&self) -> Result<String, Box<dyn std::error::Error>> {
        // Check if we have a cached valid token
        {
            let cache = self.token_cache.read().await;
            if let Some(cached_token) = cache.as_ref() {
                if cached_token.expires_at > Instant::now() {
                    return Ok(cached_token.access_token.clone());
                }
            }
        }

        // Fetch new token if not cached or expired
        Self::refresh_token_internal(
            &self.token_cache,
            &self.oauth2_api_url,
            &self.secrets_watcher,
            &self.http_client,
        )
        .await
    }

    async fn refresh_token_internal(
        token_cache: &Arc<RwLock<Option<CachedToken>>>,
        oauth2_api_url: &str,
        secrets_watcher: &Arc<SecretFileWatcher>,
        http_client: &reqwest_middleware::ClientWithMiddleware,
    ) -> Result<String, Box<dyn std::error::Error>> {
        // Get current secrets
        let secrets = secrets_watcher.get_config();

        let nvcf_creds = secrets
            .nvcf_api
            .ok_or("No NVCF API credentials found in secrets")?;

        let (new_token, expires_in) = Self::get_token_request_internal(
            oauth2_api_url,
            &nvcf_creds.client_id,
            &nvcf_creds.client_secret,
            http_client,
        )
        .await?;

        let expires_at = Instant::now() + Duration::from_secs(expires_in);
        let cached_token = CachedToken {
            access_token: new_token.clone(),
            expires_at,
            expires_in,
        };

        {
            let mut cache = token_cache.write().await;
            *cache = Some(cached_token);
        }

        Ok(new_token)
    }

    async fn get_token_request_internal(
        oauth2_api_url: &str,
        client_id: &str,
        client_secret: &str,
        http_client: &ClientWithMiddleware,
    ) -> Result<(String, u64), Box<dyn std::error::Error>> {
        let start_time = std::time::Instant::now();
        let token_url = format!("{}/token", oauth2_api_url);
        tracing::debug!("Fetching token from {}", token_url);

        let params = [
            ("grant_type", "client_credentials"),
            ("scope", "admin:scale_function autoscaler:fetch_config"),
        ];

        let response = http_client
            .post(&token_url)
            .basic_auth(client_id, Some(client_secret))
            .form(&params)
            .send()
            .await
            .inspect_err(|_e| {
                let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                metrics::record_oauth2_api_request(
                    &OAuth2ApiError::NetworkError.to_short_string(),
                    duration,
                );
            })?;

        if !response.status().is_success() {
            let duration = start_time.elapsed().as_secs_f64() * 1000.0;
            metrics::record_oauth2_api_request(
                &OAuth2ApiError::HttpError.to_short_string(),
                duration,
            );
            tracing::error!("Request failed to fetch token: {}", response.status());
            let error_text = response
                .text()
                .await
                .unwrap_or_else(|_| "Unknown error".to_string());
            metrics::record_oauth2_client_token_refresh_failure();
            return Err(format!("Fetching token request failed: {}", error_text).into());
        }

        let token_response: serde_json::Value = response.json().await.inspect_err(|_e| {
            let duration = start_time.elapsed().as_secs_f64() * 1000.0;
            metrics::record_oauth2_api_request(
                &OAuth2ApiError::ParseError.to_short_string(),
                duration,
            );
        })?;

        let access_token = token_response
            .get("access_token")
            .and_then(|t| t.as_str())
            .ok_or_else(|| {
                let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                metrics::record_oauth2_api_request(
                    &OAuth2ApiError::MissingAccessToken.to_short_string(),
                    duration,
                );
                metrics::record_oauth2_client_token_refresh_failure();
                "No access_token in response"
            })?;
        let expires_in = token_response
            .get("expires_in")
            .and_then(|t| t.as_u64())
            .ok_or_else(|| {
                let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                metrics::record_oauth2_api_request(
                    &OAuth2ApiError::MissingExpiresIn.to_short_string(),
                    duration,
                );
                metrics::record_oauth2_client_token_refresh_failure();
                "No expires_in in response"
            })?;

        let duration = start_time.elapsed().as_secs_f64() * 1000.0;
        metrics::record_oauth2_api_request(&OAuth2ApiError::Success.to_short_string(), duration);
        Ok((access_token.to_string(), expires_in))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mockito::Server;
    use serde_json::json;
    use tempfile::TempDir;
    use tokio::fs;

    async fn create_test_secrets_file() -> (TempDir, std::path::PathBuf) {
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
        (temp_dir, secrets_file)
    }

    #[tokio::test]
    async fn test_cached_token() {
        let (_temp_dir, secrets_file) = create_test_secrets_file().await;
        let secrets_watcher = SecretFileWatcher::new(&secrets_file).await.unwrap();

        let oauth2_client =
            OAuth2Client::new("https://test.com".to_string(), Arc::new(secrets_watcher)).unwrap();

        // Test that the credentials are properly loaded
        let secrets = oauth2_client.secrets_watcher.get_config();
        assert!(secrets.nvcf_api.is_some());
        let creds = secrets.nvcf_api.unwrap();
        assert_eq!(creds.client_id, "test_client_id");
        assert_eq!(creds.client_secret, "test_client_secret");
    }

    #[tokio::test]
    async fn test_token_expiration() {
        let (_temp_dir, secrets_file) = create_test_secrets_file().await;
        let secrets_watcher = SecretFileWatcher::new(&secrets_file).await.unwrap();

        let oauth2_client =
            OAuth2Client::new("https://test.com".to_string(), Arc::new(secrets_watcher)).unwrap();

        // Test cache expiration logic
        let cached_token = CachedToken {
            access_token: "test_token".to_string(),
            expires_at: Instant::now() + Duration::from_secs(3600),
            expires_in: 3600,
        };

        {
            let mut cache = oauth2_client.token_cache.write().await;
            *cache = Some(cached_token);
        }

        // Should return cached token
        let cache = oauth2_client.token_cache.read().await;
        assert!(cache.is_some());
        assert!(cache.as_ref().unwrap().expires_at > Instant::now());
    }

    #[tokio::test]
    async fn test_oauth2_token_request_success() {
        let mut server = Server::new_async().await;

        // Mock successful OAuth2 token response
        let mock = server
            .mock("POST", "/token")
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(
                json!({
                    "access_token": "mock_access_token_12345",
                    "token_type": "Bearer",
                    "expires_in": 3600
                })
                .to_string(),
            )
            .create_async()
            .await;

        let client = reqwest_middleware::ClientBuilder::new(reqwest::Client::new()).build();
        let result = OAuth2Client::get_token_request_internal(
            &server.url(),
            "test_client_id",
            "test_client_secret",
            &client,
        )
        .await;

        mock.assert_async().await;
        assert!(result.is_ok());

        let (token, expires_in) = result.unwrap();
        assert_eq!(token, "mock_access_token_12345");
        assert_eq!(expires_in, 3600);
    }

    #[tokio::test]
    async fn test_get_token_request_failure() {
        let mut server = Server::new_async().await;

        // Mock failed token response
        let mock = server
            .mock("POST", "/token")
            .with_status(401)
            .with_header("content-type", "application/json")
            .with_body(
                json!({
                    "error": "invalid_client",
                    "error_description": "Client authentication failed"
                })
                .to_string(),
            )
            .create_async()
            .await;

        let client = reqwest_middleware::ClientBuilder::new(reqwest::Client::new()).build();
        let result = OAuth2Client::get_token_request_internal(
            &server.url(),
            "invalid_client_id",
            "invalid_client_secret",
            &client,
        )
        .await;

        mock.assert_async().await;
        assert!(result.is_err());
        assert!(result
            .unwrap_err()
            .to_string()
            .contains("Fetching token request failed"));
    }

    #[tokio::test]
    async fn test_get_jwt_token_with_mock_server() {
        let mut server = Server::new_async().await;
        let (_temp_dir, secrets_file) = create_test_secrets_file().await;
        let secrets_watcher = SecretFileWatcher::new(&secrets_file).await.unwrap();

        // Mock successful OAuth2 token response
        let mock = server
            .mock("POST", "/token")
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(
                json!({
                    "access_token": "fresh_token_67890",
                    "token_type": "Bearer",
                    "expires_in": 3600
                })
                .to_string(),
            )
            .expect_at_least(1)
            .create_async()
            .await;

        let oauth2_client = OAuth2Client::new(server.url(), Arc::new(secrets_watcher)).unwrap();

        // First call should fetch token from mock server
        let token1 = oauth2_client.get_jwt_token().await;
        assert!(token1.is_ok());
        assert_eq!(token1.unwrap(), "fresh_token_67890");

        // Second call should return cached token (no additional server call)
        let token2 = oauth2_client.get_jwt_token().await;
        assert!(token2.is_ok());
        assert_eq!(token2.unwrap(), "fresh_token_67890");

        mock.assert_async().await;
    }

    #[tokio::test]
    async fn test_token_refresh_when_expired() {
        let mut server = Server::new_async().await;
        let (_temp_dir, secrets_file) = create_test_secrets_file().await;
        let secrets_watcher = SecretFileWatcher::new(&secrets_file).await.unwrap();

        // Mock OAuth2 responses - first call returns one token, second call returns a different token
        let mock1 = server
            .mock("POST", "/token")
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(
                json!({
                    "access_token": "first_token",
                    "token_type": "Bearer",
                    "expires_in": 1 // Very short expiry for testing
                })
                .to_string(),
            )
            .expect(1)
            .create_async()
            .await;

        let mock2 = server
            .mock("POST", "/token")
            .with_status(200)
            .with_header("content-type", "application/json")
            .with_body(
                json!({
                    "access_token": "refreshed_token",
                    "token_type": "Bearer",
                    "expires_in": 3600
                })
                .to_string(),
            )
            .expect(1)
            .create_async()
            .await;

        let oauth2_client = OAuth2Client::new(server.url(), Arc::new(secrets_watcher)).unwrap();

        // Get first token
        let token1 = oauth2_client.get_jwt_token().await.unwrap();
        assert_eq!(token1, "first_token");

        // Wait for token to expire
        tokio::time::sleep(Duration::from_secs(2)).await;

        // Get token again - should fetch new one since first expired
        let token2 = oauth2_client.get_jwt_token().await.unwrap();
        assert_eq!(token2, "refreshed_token");

        mock1.assert_async().await;
        mock2.assert_async().await;
    }
}

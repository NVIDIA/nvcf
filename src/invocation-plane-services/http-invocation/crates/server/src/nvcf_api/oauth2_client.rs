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

use crate::metrics::record_http_client_response;
use crate::secrets::secret_config::FixedBearerSecrets;
use crate::secrets::secret_provider::SecretFileWatcher;
use crate::settings::GrpcClientConfig;
use futures::FutureExt;
use http::Extensions;
use oauth2::{
    basic::BasicClient, AccessToken, AsyncHttpClient, ClientId, ClientSecret, HttpClientError,
    HttpRequest, HttpResponse, Scope, TokenResponse, TokenUrl,
};
use reqwest::{Request, Response};
use reqwest_middleware::{ClientBuilder, ClientWithMiddleware, Middleware, Next};
use reqwest_tracing::TracingMiddleware;
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;
use std::time::Duration;
use std::time::Instant;
use tokio::sync::RwLock;

pub enum TokenProducer {
    OAuth2Producer(CachedTokenClient),
    FixedProducer(Box<dyn Fn() -> anyhow::Result<String> + Send + Sync + 'static>),
}

impl TokenProducer {
    pub async fn produce_token(&self) -> anyhow::Result<String> {
        Ok(match self {
            TokenProducer::OAuth2Producer(cached_token_client) => {
                cached_token_client.get_token().await?.into_secret()
            }
            TokenProducer::FixedProducer(fixed_producer) => fixed_producer()?,
        })
    }

    pub fn new(
        oauth2_token_endpoint: &str,
        scope: Scope,
        secrets_provider: Option<Arc<SecretFileWatcher>>,
        fixed_producer_extractor: Option<fn(FixedBearerSecrets) -> Option<String>>,
        grpc_config: &GrpcClientConfig,
    ) -> anyhow::Result<Option<TokenProducer>> {
        Ok(match secrets_provider {
            Some(secrets_provider) => Some({
                match fixed_producer_extractor.and_then(|extractor| {
                    extractor(secrets_provider.get_config().fixed_bearer_secrets)
                }) {
                    None => TokenProducer::OAuth2Producer(CachedTokenClient::new(
                        format!("{oauth2_token_endpoint}/token"),
                        scope,
                        move || {
                            // we just have one oauth2 client, so we don't need to pick the secret field with the extractor
                            let oauth2 = secrets_provider.get_config().oauth2;
                            (
                                ClientId::new(oauth2.client_id),
                                ClientSecret::new(oauth2.client_secret),
                            )
                        },
                        grpc_config.oauth2_client_timeout,
                    )?),
                    // for air gapped nvcf bearer tokens are provided directly from vault
                    Some(_) => TokenProducer::FixedProducer(Box::new(move || {
                        fixed_producer_extractor
                            .and_then(|extractor| {
                                // pick the correct secret field with the extractor
                                extractor(secrets_provider.get_config().fixed_bearer_secrets)
                            })
                            .ok_or_else(|| anyhow::anyhow!("fixed bearer secret missing"))
                    })),
                }
            }),
            None => None,
        })
    }
}

pub struct CachedTokenClient {
    client: TracedAsyncHttpClient,
    token_cache: Arc<RwLock<Option<(AccessToken, Instant)>>>,
    token_url: TokenUrl,
    scope: Scope,
    // boxed to erase type and make this easier to name
    secret_producer: Box<dyn Fn() -> (ClientId, ClientSecret) + Send + Sync + 'static>,
}

impl CachedTokenClient {
    pub fn new(
        token_url: String,
        scope: Scope,
        secret_producer: impl Fn() -> (ClientId, ClientSecret) + Send + Sync + 'static,
        timeout: Duration,
    ) -> anyhow::Result<Self> {
        Ok(Self {
            secret_producer: Box::new(secret_producer),
            token_cache: Arc::new(RwLock::new(None)),
            token_url: TokenUrl::new(token_url)?,
            scope,
            client: TracedAsyncHttpClient(
                ClientBuilder::new(reqwest::Client::builder().timeout(timeout).build()?)
                    .with(TracingMiddleware::default())
                    .with(MetricsMiddleware)
                    .build(),
            ),
        })
    }

    pub async fn get_token(&self) -> anyhow::Result<AccessToken> {
        // Check if we have a cached token
        let cache = self.token_cache.read().await;
        if let Some((access_token, expires_at)) = cache.as_ref() {
            if expires_at > &Instant::now() {
                return Ok(access_token.clone());
            }
        }
        // If not, fetch a new token and cache it
        drop(cache); // Release the read lock

        let mut cache = self.token_cache.write().await;
        // check if another thread already fetched a token while we dropped the lock
        if let Some((access_token, expires_at)) = cache.as_ref() {
            if expires_at > &Instant::now() {
                return Ok(access_token.clone());
            }
        }

        let (client_id, client_secret) = (self.secret_producer)();
        let client = BasicClient::new(client_id)
            .set_client_secret(client_secret)
            .set_token_uri(self.token_url.clone());
        let token_result = client
            .exchange_client_credentials()
            .add_scope(self.scope.clone())
            .request_async(&self.client)
            .await?;

        let access_token = token_result.access_token().clone();
        // expire in 90% of the total time. we aren't handling retries on 401,
        // so we need to be sure the token is valid when we send it.
        let expires_in = token_result
            .expires_in()
            .ok_or(anyhow::anyhow!(
                "missing expiry time from oauth2 token response"
            ))?
            .mul_f32(0.9);
        let expires_at = Instant::now() + expires_in;
        *cache = Some((access_token, expires_at));
        Ok(token_result.access_token().clone())
    }
}

struct TracedAsyncHttpClient(ClientWithMiddleware);

impl TracedAsyncHttpClient {
    async fn oauth_call(
        &self,
        request: HttpRequest,
    ) -> Result<HttpResponse, HttpClientError<reqwest::Error>> {
        let response = self
            .0
            .execute(request.try_into().map_err(Box::new)?)
            .await
            .map_err(|err| match err {
                reqwest_middleware::Error::Middleware(err) => {
                    HttpClientError::Other(err.to_string())
                }
                reqwest_middleware::Error::Reqwest(err) => HttpClientError::Reqwest(Box::new(err)),
            })?;

        let mut builder = http::Response::builder()
            .status(response.status())
            .version(response.version());

        for (name, value) in response.headers().iter() {
            builder = builder.header(name, value);
        }

        builder
            .body(response.bytes().await.map_err(Box::new)?.to_vec())
            .map_err(HttpClientError::Http)
    }
}

impl<'c> AsyncHttpClient<'c> for TracedAsyncHttpClient {
    type Error = HttpClientError<reqwest::Error>;
    type Future = Pin<Box<dyn Future<Output = Result<HttpResponse, Self::Error>> + Send + 'c>>;

    fn call(&'c self, request: HttpRequest) -> Self::Future {
        self.oauth_call(request).boxed()
    }
}

struct MetricsMiddleware;

#[async_trait::async_trait]
impl Middleware for MetricsMiddleware {
    async fn handle(
        &self,
        req: Request,
        extensions: &mut Extensions,
        next: Next<'_>,
    ) -> reqwest_middleware::Result<Response> {
        let domain = req.url().domain().unwrap_or_default().to_string();
        let outcome = next.run(req, extensions).await;
        match &outcome {
            Ok(response) => {
                let status = response.status();
                record_http_client_response(domain, status);
            }
            Err(err) => {
                if let Some(status) = err.status() {
                    record_http_client_response(domain, status);
                }
            }
        }
        outcome
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{routing::post, Json, Router};
    use serde::Serialize;
    use std::time::Duration;
    use tokio::net::TcpListener;
    use tokio::time::sleep;

    // Test for FixedProducer variant
    #[tokio::test]
    async fn test_fixed_producer() {
        let token_value = "test_token_value".to_string();
        let token_producer =
            TokenProducer::FixedProducer(Box::new(move || Ok(token_value.clone())));

        let result = token_producer.produce_token().await;
        assert!(result.is_ok());
        assert_eq!(result.unwrap(), "test_token_value");
    }

    // Test error handling in FixedProducer
    #[tokio::test]
    async fn test_fixed_producer_error() {
        let token_producer =
            TokenProducer::FixedProducer(Box::new(move || Err(anyhow::anyhow!("token error"))));

        let result = token_producer.produce_token().await;
        assert!(result.is_err());
        assert_eq!(result.unwrap_err().to_string(), "token error");
    }

    // Simulated OAuth2 server response format
    #[derive(Serialize)]
    struct TokenResponse {
        access_token: String,
        token_type: String,
        expires_in: u64,
    }

    // Helper to start a test server with configurable response
    async fn start_test_server(
        response_handler: impl Fn() -> TokenResponse + Clone + Send + Sync + 'static,
    ) -> String {
        // Create a handler that will return our configured response
        async fn handle_token_request<F>(response_handler: F) -> Json<TokenResponse>
        where
            F: Fn() -> TokenResponse,
        {
            Json(response_handler())
        }

        // Build the app with the token endpoint
        let app = Router::new().route(
            "/token",
            post(move || handle_token_request(response_handler.clone())),
        );

        // Find an available port
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        // Start the server in the background
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });

        // Return the URL
        format!("http://{}", addr)
    }

    // Test for OAuth2Producer with a real token client
    #[tokio::test]
    async fn test_oauth2_producer() -> anyhow::Result<()> {
        // Start a test server that returns a mock token
        let server_url = start_test_server(|| TokenResponse {
            access_token: "mock_access_token".to_string(),
            token_type: "bearer".to_string(),
            expires_in: 3600,
        })
        .await;

        // Create a real CachedTokenClient pointing to our test server
        let cached_token_client = make_test_cached_token_client(server_url)?;

        let token_producer = TokenProducer::OAuth2Producer(cached_token_client);

        // Test token production
        let result = token_producer.produce_token().await;
        assert_eq!(result?, "mock_access_token");

        Ok(())
    }

    // Test token caching behavior
    #[tokio::test]
    async fn test_cached_token_client() -> anyhow::Result<()> {
        // Set up a counter to track how many times the token endpoint is called
        let counter = Arc::new(std::sync::atomic::AtomicUsize::new(0));

        // Create a response handler that increments counter and returns different tokens each time
        let counter_clone = counter.clone();
        let server_url = start_test_server(move || {
            let count = counter_clone.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            TokenResponse {
                access_token: format!("token_{}", count),
                token_type: "bearer".to_string(),
                expires_in: 3600,
            }
        })
        .await;

        // Create a real client pointing to our test server
        let cached_token_client = make_test_cached_token_client(server_url)?;

        // First call should make a request
        let token1 = cached_token_client.get_token().await?;
        assert_eq!(token1.into_secret(), "token_0");
        assert_eq!(counter.load(std::sync::atomic::Ordering::SeqCst), 1);

        // Second call should use the cache and not make a new request
        let token2 = cached_token_client.get_token().await?;
        assert_eq!(token2.into_secret(), "token_0");
        assert_eq!(counter.load(std::sync::atomic::Ordering::SeqCst), 1); // Still 1

        // Force expiry of the token
        *cached_token_client.token_cache.write().await = None;

        // Third call should make another request and get a new token
        let token3 = cached_token_client.get_token().await?;
        assert_eq!(token3.into_secret(), "token_1");
        assert_eq!(counter.load(std::sync::atomic::Ordering::SeqCst), 2);

        Ok(())
    }

    // Test token expiration and renewal
    #[tokio::test]
    async fn test_token_expiration_and_renewal() -> anyhow::Result<()> {
        // Set up a counter to track how many times the token endpoint is called
        let counter = Arc::new(std::sync::atomic::AtomicUsize::new(0));

        // Create a response handler that returns a short-lived token first, then a longer-lived one
        let counter_clone = counter.clone();
        let server_url = start_test_server(move || {
            let count = counter_clone.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            if count == 0 {
                TokenResponse {
                    access_token: "short_lived_token".to_string(),
                    token_type: "bearer".to_string(),
                    expires_in: 1, // 1 second expiration
                }
            } else {
                TokenResponse {
                    access_token: "renewed_token".to_string(),
                    token_type: "bearer".to_string(),
                    expires_in: 3600,
                }
            }
        })
        .await;

        // Create a real client pointing to our test server
        let cached_token_client = make_test_cached_token_client(server_url)?;

        // First token request
        let token1 = cached_token_client.get_token().await?;
        assert_eq!(token1.into_secret(), "short_lived_token");

        // Second request immediately - should return the cached token
        let token2 = cached_token_client.get_token().await?;
        assert_eq!(token2.into_secret(), "short_lived_token");
        assert_eq!(counter.load(std::sync::atomic::Ordering::SeqCst), 1); // Still 1

        // Wait for token to expire (more than 1 second), accounting for the 0.9 multiplier in the code
        sleep(Duration::from_millis(1200)).await;

        // Third request after expiration - should get a new token
        let token3 = cached_token_client.get_token().await?;
        assert_eq!(token3.into_secret(), "renewed_token");
        assert_eq!(counter.load(std::sync::atomic::Ordering::SeqCst), 2);

        Ok(())
    }

    // Test error handling in OAuth2 mode
    #[tokio::test]
    async fn test_oauth2_error_handling() -> anyhow::Result<()> {
        // Start a test server that returns an error response
        let listener = TcpListener::bind("127.0.0.1:0").await?;
        let addr = listener.local_addr()?;

        // Create an app that returns a 500 error
        let app = Router::new().route(
            "/token",
            post(|| async { http::StatusCode::INTERNAL_SERVER_ERROR }),
        );

        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });

        let server_url = format!("http://{}", addr);

        // Create a client pointing to our error server
        let cached_token_client = make_test_cached_token_client(server_url)?;

        let token_producer = TokenProducer::OAuth2Producer(cached_token_client);

        // Attempt to produce a token should fail
        let result = token_producer.produce_token().await;
        assert!(result.is_err());

        Ok(())
    }

    fn make_test_cached_token_client(server_url: String) -> anyhow::Result<CachedTokenClient> {
        CachedTokenClient::new(
            format!("{}/token", server_url),
            Scope::new("test:scope".into()),
            || {
                (
                    ClientId::new("client_id".to_string()),
                    ClientSecret::new("client_secret".to_string()),
                )
            },
            Duration::from_secs(10),
        )
    }
}

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

use crate::health::{HealthCheckResult, HealthChecker};
use crate::metrics::{
    record_timeseries_db_auth_failure, record_timeseries_db_query_failure,
    record_timeseries_db_request,
};
use crate::secrets::secrets_file_watcher::SecretFileWatcher;
use crate::timeseries_db::{
    errors::TimeseriesDbApiError, TimeseriesDbAuthMode, TimeseriesDbSettings,
};
use anyhow::{Context, Result};
use async_trait::async_trait;
use backon::{ExponentialBuilder, Retryable};
use chrono::{DateTime, Duration, Utc};
use openssl::asn1::Asn1Time;
use openssl::pkey::PKey;
use openssl::x509::X509;
use reqwest::{ClientBuilder, Identity};
use reqwest_middleware::ClientWithMiddleware;
use serde::Deserialize;
use serde_json;
use std::cmp::Ordering;
use std::sync::{Arc, Mutex};
use std::time::{Duration as StdDuration, SystemTime, UNIX_EPOCH};
use tracing;
use url::Url;

const TOKEN_REFRESH_THRESHOLD: chrono::Duration = chrono::Duration::minutes(3);
const DEFAULT_MIN_BACKOFF_DELAY: StdDuration = StdDuration::from_millis(100);

fn query_range_url(mut base_url: Url, auth_mode: TimeseriesDbAuthMode) -> Result<Url> {
    {
        let mut segments = base_url
            .path_segments_mut()
            .map_err(|_| anyhow::anyhow!("Cannot modify URL"))?;
        if auth_mode != TimeseriesDbAuthMode::Token {
            segments.push("api");
        }
        segments.push("v1").push("query_range");
    }
    Ok(base_url)
}

fn mtls_identity(config: &TimeseriesDbSettings, base_url: &Url) -> Result<Identity> {
    if base_url.scheme() != "https" {
        return Err(anyhow::anyhow!(
            "TimeseriesDb mTLS authentication requires an https URL"
        ));
    }
    if base_url.path() != "/" || base_url.query().is_some() || base_url.fragment().is_some() {
        return Err(anyhow::anyhow!(
            "TimeseriesDb mTLS URL must be an origin without a path, query, or fragment"
        ));
    }

    let certificate_path = config.client_certificate_path.as_ref().ok_or_else(|| {
        anyhow::anyhow!("TimeseriesDb mTLS authentication requires client_certificate_path")
    })?;
    let private_key_path = config.client_private_key_path.as_ref().ok_or_else(|| {
        anyhow::anyhow!("TimeseriesDb mTLS authentication requires client_private_key_path")
    })?;
    let certificate_pem = std::fs::read(certificate_path).with_context(|| {
        format!(
            "Failed to read TimeseriesDb client certificate from {}",
            certificate_path.display()
        )
    })?;
    let private_key_pem = std::fs::read(private_key_path).with_context(|| {
        format!(
            "Failed to read TimeseriesDb client private key from {}",
            private_key_path.display()
        )
    })?;

    let certificates = X509::stack_from_pem(&certificate_pem)
        .context("Failed to parse TimeseriesDb client certificate chain")?;
    let leaf_certificate = certificates
        .first()
        .ok_or_else(|| anyhow::anyhow!("TimeseriesDb client certificate chain is empty"))?;
    let private_key = PKey::private_key_from_pem(&private_key_pem)
        .context("Failed to parse TimeseriesDb client private key")?;
    let leaf_public_key = leaf_certificate
        .public_key()
        .context("Failed to read TimeseriesDb client certificate public key")?;
    if !leaf_public_key.public_eq(&private_key) {
        return Err(anyhow::anyhow!(
            "TimeseriesDb client certificate does not match the private key"
        ));
    }

    let now = Asn1Time::days_from_now(0).context("Failed to get current certificate time")?;
    if leaf_certificate
        .not_before()
        .compare(&now)
        .context("Failed to compare TimeseriesDb client certificate validity")?
        == Ordering::Greater
    {
        return Err(anyhow::anyhow!(
            "TimeseriesDb client certificate is not valid yet"
        ));
    }
    if leaf_certificate
        .not_after()
        .compare(&now)
        .context("Failed to compare TimeseriesDb client certificate expiry")?
        != Ordering::Greater
    {
        return Err(anyhow::anyhow!(
            "TimeseriesDb client certificate has expired"
        ));
    }

    // Normalize either PKCS#1 or PKCS#8 input to the PKCS#8 form required by
    // reqwest's native TLS identity constructor.
    let private_key_pem = private_key
        .private_key_to_pem_pkcs8()
        .context("Failed to encode TimeseriesDb client private key as PKCS#8")?;
    Identity::from_pkcs8_pem(&certificate_pem, &private_key_pem)
        .context("Failed to build TimeseriesDb client identity")
}

#[derive(Debug, Clone, Deserialize)]
pub struct TimeseriesDbResponse {
    pub status: String,
    pub data: ResponseData,
}

#[derive(Debug, Clone, Deserialize)]
pub struct ResponseData {
    #[serde(rename = "resultType")]
    pub result_type: String,
    pub result: Vec<TimeseriesDbResult>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct TimeseriesDbResult {
    pub metric: Metric,
    /// Prometheus/TimeseriesDb query_range returns timestamps as floats (Unix seconds); we use f64 for compatibility.
    pub values: Vec<(f64, String)>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct Metric {
    #[serde(rename = "__name__")]
    pub name: Option<String>,
    pub error_code: Option<String>,
    pub function_id: Option<String>,
    pub function_version_id: Option<String>,
    pub nca_id: Option<String>,
    pub instance_id: Option<String>,
}

#[derive(Debug, Clone)]
pub struct Credentials {
    pub token: String,
    pub expiration: DateTime<Utc>,
}

#[derive(Clone)]
pub struct TimeseriesDbClient {
    base_url: Url,
    http_client: ClientWithMiddleware,
    credentials: Arc<Mutex<Option<Credentials>>>,
    credential_provider: Option<Arc<dyn CredentialProvider + Send + Sync>>,
    auth_mode: TimeseriesDbAuthMode,
    is_healthy: Arc<Mutex<bool>>,
    backoff: Option<ExponentialBuilder>,
}

#[async_trait]
pub trait CredentialProvider: Send + Sync {
    async fn get_credentials(&self) -> Result<Credentials>;
}

impl TimeseriesDbClient {
    pub fn new(
        config: &TimeseriesDbSettings,
        credential_provider: Option<Arc<dyn CredentialProvider + Send + Sync>>,
    ) -> Result<Self> {
        let base_url = Url::parse(&config.timeseries_db_url).context("Failed to parse base URL")?;
        let auth_mode = config.effective_auth_mode();

        if auth_mode == TimeseriesDbAuthMode::Token && credential_provider.is_none() {
            return Err(anyhow::anyhow!(
                "TimeseriesDb token authentication requires a credential provider"
            ));
        }

        let http_timeout = StdDuration::from_secs(config.http_timeout_seconds);
        let mut client_builder = ClientBuilder::new().timeout(http_timeout);
        if auth_mode == TimeseriesDbAuthMode::Mtls {
            client_builder = client_builder.identity(mtls_identity(config, &base_url)?);
        }

        let http_client = reqwest_middleware::ClientBuilder::new(
            client_builder
                .build()
                .context("Failed to build HTTP client")?,
        )
        .with(reqwest_tracing::TracingMiddleware::default())
        .build();

        tracing::info!(auth_mode = ?auth_mode, "Configured TimeseriesDb authentication");

        let query_backoff = config.backoff.unwrap_or_else(|| {
            ExponentialBuilder::default()
                .with_max_times(10)
                .with_max_delay(StdDuration::from_secs(
                    config.query_backoff_max_delay_seconds,
                ))
                .with_min_delay(DEFAULT_MIN_BACKOFF_DELAY)
                .with_factor(2.0)
        });

        Ok(Self {
            base_url,
            http_client,
            credentials: Arc::new(Mutex::new(None)),
            credential_provider,
            auth_mode,
            is_healthy: Arc::new(Mutex::new(true)),
            backoff: Some(query_backoff),
        })
    }

    async fn get_token(&self) -> Result<String> {
        if self.credential_provider.is_none() {
            return Err(anyhow::anyhow!("No credential provider available"));
        }

        let should_refresh = {
            let creds_guard = self.credentials.lock().unwrap();
            match &*creds_guard {
                Some(creds) => {
                    let threshold_from_now = Utc::now() + TOKEN_REFRESH_THRESHOLD;
                    creds.expiration < threshold_from_now
                }
                None => true,
            }
        };

        if should_refresh {
            tracing::info!("Refreshing TimeseriesDb credentials");
            match self
                .credential_provider
                .as_ref()
                .unwrap()
                .get_credentials()
                .await
            {
                Ok(new_creds) => {
                    let mut creds_guard = self.credentials.lock().unwrap();
                    *creds_guard = Some(new_creds.clone());
                    *self.is_healthy.lock().unwrap() = true;
                    Ok(new_creds.token)
                }
                Err(refresh_error) => {
                    tracing::error!(
                        "Failed to refresh TimeseriesDb credentials: {}",
                        refresh_error
                    );
                    record_timeseries_db_auth_failure();

                    let creds_guard = self.credentials.lock().unwrap();
                    tracing::warn!(
                            "Failed to refresh TimeseriesDb credentials, trying to use existing unexpired token");
                    if let Some(creds) = &*creds_guard {
                        let now = Utc::now();
                        if creds.expiration > now {
                            return Ok(creds.token.clone());
                        } else {
                            tracing::error!(
                                "Old token is also expired, no valid credentials available"
                            );
                        }
                    }
                    *self.is_healthy.lock().unwrap() = false;
                    Err(anyhow::anyhow!("No valid credentials available"))
                }
            }
        } else {
            let creds_guard = self.credentials.lock().unwrap();
            let token = creds_guard.as_ref().unwrap().token.clone();
            Ok(token)
        }
    }

    pub async fn query_range(
        &self,
        query: &str,
        start: DateTime<Utc>,
        end: DateTime<Utc>,
        step: StdDuration,
    ) -> Result<TimeseriesDbResponse> {
        let url = query_range_url(self.base_url.clone(), self.auth_mode)?;

        let query_params = [
            ("query", query.to_string()),
            ("start", start.timestamp().to_string()),
            ("end", end.timestamp().to_string()),
            ("step", step.as_secs().to_string()),
        ];

        if self.auth_mode != TimeseriesDbAuthMode::Token {
            self.execute_with_retry_no_auth(url, &query_params).await
        } else {
            self.execute_with_retry(move |token| {
                let url = url.clone();
                let query_params = query_params.clone();
                async move { self.execute_request(url, &query_params, Some(&token)).await }
            })
            .await
        }
    }

    // self and auth_token would add tokens or credentials to the traces, so they are excluded
    #[tracing::instrument(name = "execute_timeseries_db_request", skip(self, auth_token))]
    async fn execute_request(
        &self,
        url: Url,
        query_params: &[(&str, String)],
        auth_token: Option<&str>,
    ) -> Result<TimeseriesDbResponse> {
        let start_time = std::time::Instant::now();
        let mut request = self.http_client.get(url.clone()).query(query_params);
        if let Some(token) = auth_token {
            request = request.header("authorizationToken", token);
        }

        let response = request.send().await;
        match response {
            Ok(response) => {
                let status = response.status();
                let body = response.text().await;
                match body {
                    Ok(body_text) => {
                        if status.is_success() {
                            let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                            record_timeseries_db_request(
                                &TimeseriesDbApiError::Success.to_short_string(),
                                duration,
                            );
                            let timeseries_db_response: TimeseriesDbResponse =
                                serde_json::from_str(&body_text)
                                    .context("Failed to deserialize TimeseriesDb response")?;
                            Ok(timeseries_db_response)
                        } else {
                            let duration = start_time.elapsed().as_secs_f64() * 1000.0;

                            if status == reqwest::StatusCode::UNAUTHORIZED
                                || status == reqwest::StatusCode::FORBIDDEN
                            {
                                record_timeseries_db_request(
                                    &TimeseriesDbApiError::AuthError.to_short_string(),
                                    duration,
                                );
                                *self.is_healthy.lock().unwrap() = false;
                                tracing::error!(
                                    "TimeseriesDb authentication failed: {} - {}",
                                    status,
                                    body_text
                                );
                            } else if status.is_server_error() {
                                record_timeseries_db_request(
                                    &TimeseriesDbApiError::ServerError.to_short_string(),
                                    duration,
                                );
                                tracing::error!(
                                    "TimeseriesDb server error: {} - {}",
                                    status,
                                    body_text
                                );
                            } else {
                                record_timeseries_db_request(
                                    &TimeseriesDbApiError::ClientError.to_short_string(),
                                    duration,
                                );
                                tracing::error!(
                                    "TimeseriesDb client error: {} - {}",
                                    status,
                                    body_text
                                );
                            }
                            Err(anyhow::anyhow!(
                                "TimeseriesDb API error: {} - {}",
                                status,
                                body_text
                            ))
                        }
                    }
                    Err(e) => {
                        let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                        record_timeseries_db_request(
                            &TimeseriesDbApiError::BodyReadError.to_short_string(),
                            duration,
                        );
                        Err(anyhow::anyhow!("Failed to read response body: {}", e))
                    }
                }
            }
            Err(e) => {
                let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                record_timeseries_db_request(
                    &TimeseriesDbApiError::NetworkError.to_short_string(),
                    duration,
                );
                Err(anyhow::anyhow!("HTTP request failed: {}", e))
            }
        }
    }

    async fn execute_with_retry_no_auth(
        &self,
        url: Url,
        query_params: &[(&str, String)],
    ) -> Result<TimeseriesDbResponse> {
        let backoff = self
            .backoff
            .expect("backoff policy must be set during construction");

        let execute_fn = || async { self.execute_request(url.clone(), query_params, None).await };

        execute_fn
            .retry(&backoff)
            .when(|err| {
                let err_msg = err.to_string();
                record_timeseries_db_query_failure();
                tracing::warn!("TimeseriesDb API error: {}", err_msg);
                true
            })
            .await
    }

    async fn execute_with_retry<F, Fut, T>(&self, operation: F) -> Result<T>
    where
        F: Fn(String) -> Fut,
        Fut: std::future::Future<Output = Result<T>>,
    {
        let backoff = self
            .backoff
            .expect("backoff policy must be set during construction");

        let retry_fn = || async {
            let token = self.get_token().await?;
            operation(token).await
        };

        retry_fn
            .retry(&backoff)
            .when(|err| {
                let err_msg = err.to_string();
                if err_msg.contains("401 Unauthorized") || err_msg.contains("403 Forbidden") {
                    {
                        let mut creds_guard = self.credentials.lock().unwrap();
                        *creds_guard = None;
                    }
                    {
                        let mut health = self.is_healthy.lock().unwrap();
                        *health = false;
                    }
                    record_timeseries_db_auth_failure();
                    tracing::error!("TimeseriesDb authentication error: {}", err_msg);
                    false
                } else {
                    record_timeseries_db_query_failure();
                    tracing::warn!("TimeseriesDb API error: {}", err_msg);
                    true
                }
            })
            .await
    }
}

pub struct AuthnCredentialProvider {
    secrets_watcher: Arc<SecretFileWatcher>,
    authn_url: String,
    http_client: ClientWithMiddleware,
    auth_backoff_max_delay: StdDuration,
}

impl AuthnCredentialProvider {
    pub fn new(config: &TimeseriesDbSettings, secrets_watcher: Arc<SecretFileWatcher>) -> Self {
        let http_timeout = StdDuration::from_secs(config.http_timeout_seconds);
        let http_client = reqwest_middleware::ClientBuilder::new(
            ClientBuilder::new()
                .timeout(http_timeout)
                .build()
                .expect("Failed to build HTTP client for auth"),
        )
        .with(reqwest_tracing::TracingMiddleware::default())
        .build();
        let authn_url = config.authn_url.clone();

        Self {
            secrets_watcher,
            authn_url,
            http_client,
            auth_backoff_max_delay: StdDuration::from_secs(config.auth_backoff_max_delay_seconds),
        }
    }
}

#[async_trait]
impl CredentialProvider for AuthnCredentialProvider {
    async fn get_credentials(&self) -> Result<Credentials> {
        let backoff = ExponentialBuilder::default()
            .with_max_times(10)
            .with_max_delay(self.auth_backoff_max_delay)
            .with_min_delay(DEFAULT_MIN_BACKOFF_DELAY)
            .with_factor(2.0);

        let operation = || async {
            let secrets = self.secrets_watcher.get_config();
            let timeseries_db_credentials = secrets
                .timeseries_db
                .ok_or_else(|| anyhow::anyhow!("TimeseriesDb credentials not found in secrets"))?;

            let response = self
                .http_client
                .get(&self.authn_url)
                .basic_auth(
                    &timeseries_db_credentials.username,
                    Some(&timeseries_db_credentials.password),
                )
                .send()
                .await
                .context("Failed to send authentication request")?;

            if !response.status().is_success() {
                let status = response.status();
                let body = response
                    .text()
                    .await
                    .unwrap_or_else(|_| "Unknown error".to_string());

                return Err(anyhow::anyhow!(
                    "Authentication failed: {} - {}",
                    status,
                    body
                ));
            }

            let auth_response: serde_json::Value = response
                .json()
                .await
                .context("Failed to parse authentication response")?;

            let token = auth_response
                .get("token")
                .and_then(|t| t.as_str())
                .ok_or_else(|| anyhow::anyhow!("token not found in auth response"))?;

            let expires_in_seconds = auth_response
                .get("expires_in")
                .and_then(|e| e.as_i64())
                .ok_or_else(|| anyhow::anyhow!("expires_in not found in auth response"))?;

            let expiration = Utc::now() + Duration::seconds(expires_in_seconds);

            Ok(Credentials {
                token: token.to_string(),
                expiration,
            })
        };

        operation
            .retry(&backoff)
            .when(|err| {
                let err_msg = err.to_string();
                if err_msg.contains("401") || err_msg.contains("403") {
                    tracing::error!("Auth error, will not retry: {}", err_msg);
                    false
                } else {
                    tracing::warn!("Auth request error, will retry: {}", err_msg);
                    true
                }
            })
            .await
    }
}

#[async_trait]
impl HealthChecker for TimeseriesDbClient {
    async fn health(&self) -> HealthCheckResult {
        let is_healthy = *self.is_healthy.lock().unwrap();
        HealthCheckResult {
            is_healthy,
            message: Some(if is_healthy {
                "TimeseriesDb client is healthy".to_string()
            } else {
                "TimeseriesDb client unhealthy".to_string()
            }),
            last_updated: SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_secs(),
        }
    }

    fn component_name(&self) -> &str {
        "timeseries_db_client"
    }
}

#[cfg(test)]
mod tests {

    use super::*;
    use chrono::{TimeZone, Utc};
    use http_body_util::Full;
    use hyper::body::{Bytes, Incoming};
    use hyper::server::conn::http1;
    use hyper::service::service_fn;
    use hyper::{Request, Response, StatusCode};
    use hyper_util::rt::TokioIo;
    use openssl::bn::{BigNum, MsbOption};
    use openssl::hash::MessageDigest;
    use openssl::rsa::Rsa;
    use openssl::x509::extension::{BasicConstraints, ExtendedKeyUsage, KeyUsage};
    use openssl::x509::{X509Builder, X509NameBuilder};
    use std::convert::Infallible;
    use std::path::Path;
    use std::sync::{
        atomic::{AtomicUsize, Ordering},
        Arc,
    };
    use std::time::Duration as StdDuration;
    use std::time::Duration;
    use tokio::net::TcpListener;

    const TEST_MAX_BACKOFF_DELAY: StdDuration = StdDuration::from_millis(500);

    struct MockCredentialProvider {
        token_counter: AtomicUsize,
        should_fail: bool,
    }

    impl MockCredentialProvider {
        fn new(should_fail: bool) -> Self {
            Self {
                token_counter: AtomicUsize::new(0),
                should_fail,
            }
        }
    }

    #[async_trait]
    impl CredentialProvider for MockCredentialProvider {
        async fn get_credentials(&self) -> Result<Credentials> {
            if self.should_fail {
                return Err(anyhow::anyhow!("Credential retrieval failed"));
            }

            let count = self.token_counter.fetch_add(1, Ordering::SeqCst);
            let token = format!("test_token_{}", count);
            Ok(Credentials {
                token,
                expiration: Utc::now() + Duration::from_secs(60 * 60),
            })
        }
    }

    // Implement CredentialProvider for Arc<MockCredentialProvider> to enable proper sharing
    #[async_trait]
    impl CredentialProvider for Arc<MockCredentialProvider> {
        async fn get_credentials(&self) -> Result<Credentials> {
            self.as_ref().get_credentials().await
        }
    }

    // Mock server state
    struct MockServerState {
        request_count: AtomicUsize,
        responses: Vec<(StatusCode, String)>,
        expected_auth_tokens: Vec<String>,
    }

    impl MockServerState {
        fn new(responses: Vec<(StatusCode, String)>, expected_auth_tokens: Vec<String>) -> Self {
            Self {
                request_count: AtomicUsize::new(0),
                responses,
                expected_auth_tokens,
            }
        }

        fn get_response(&self, auth_header: Option<&str>) -> Response<Full<Bytes>> {
            let count = self.request_count.fetch_add(1, Ordering::SeqCst);

            // Verify auth token if specified
            if count < self.expected_auth_tokens.len() {
                let expected_token = &self.expected_auth_tokens[count];
                if let Some(auth) = auth_header {
                    // The TimeseriesDbClient sends the token directly in authorizationToken header, not as Bearer
                    if auth != expected_token {
                        return Response::builder()
                            .status(StatusCode::UNAUTHORIZED)
                            .body(Full::new(Bytes::from("Unauthorized: Invalid token")))
                            .unwrap();
                    }
                } else if !expected_token.is_empty() {
                    return Response::builder()
                        .status(StatusCode::UNAUTHORIZED)
                        .body(Full::new(Bytes::from("Unauthorized: Missing token")))
                        .unwrap();
                }
            }

            // Return configured response
            if count < self.responses.len() {
                let (status, body) = &self.responses[count];
                Response::builder()
                    .status(*status)
                    .body(Full::new(Bytes::from(body.clone())))
                    .unwrap()
            } else {
                // If we have configured responses, repeat the last one for additional requests
                // Otherwise, default to success
                if !self.responses.is_empty() {
                    let (status, body) = &self.responses[self.responses.len() - 1];
                    Response::builder()
                        .status(*status)
                        .body(Full::new(Bytes::from(body.clone())))
                        .unwrap()
                } else {
                    Response::builder()
                        .status(StatusCode::OK)
                        .body(Full::new(Bytes::from(get_mock_response())))
                        .unwrap()
                }
            }
        }
    }

    async fn start_mock_server(state: Arc<MockServerState>) -> String {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();

        // Clone state for the server loop
        let state_clone = state.clone();

        // Spawn the server task
        tokio::spawn(async move {
            loop {
                match listener.accept().await {
                    Ok((stream, _)) => {
                        // Clone state for this connection
                        let state = state_clone.clone();

                        // Convert TCP stream to TokioIo
                        let io = TokioIo::new(stream);

                        // Spawn a task for each connection
                        tokio::spawn(async move {
                            // Create service for handling requests with the correct type
                            let svc = service_fn(move |req: Request<Incoming>| {
                                let state = state.clone();
                                async move {
                                    // Check for the authorizationToken header that TimeseriesDbClient actually sends
                                    let auth_header = req
                                        .headers()
                                        .get("authorizationToken")
                                        .and_then(|h| h.to_str().ok());
                                    let response = state.get_response(auth_header);
                                    Ok::<_, Infallible>(response)
                                }
                            });

                            // Serve the connection
                            if let Err(err) = http1::Builder::new().serve_connection(io, svc).await
                            {
                                tracing::error!("Connection error: {}", err);
                            }
                        });
                    }
                    Err(e) => {
                        tracing::error!("Accept error: {}", e);
                        tokio::time::sleep(Duration::from_millis(100)).await;
                    }
                }
            }
        });

        // Add a small delay to ensure the server is ready
        tokio::time::sleep(Duration::from_millis(10)).await;

        // Return the server URL
        format!("http://{}", addr)
    }

    fn get_mock_response() -> String {
        r#"{
            "status": "success",
            "data": {
                "resultType": "matrix",
                "result": [
                    {
                        "metric": {
                            "__name__": "test_metric",
                            "error_code": "0",
                            "function_id": "test-id",
                            "instance_id": "test-instance-id"
                        },
                        "values": [
                            [1748551740, "6"],
                            [1748551800, "7"]
                        ]
                    }
                ]
            }
        }"#
        .to_string()
    }

    fn fast_test_backoff() -> ExponentialBuilder {
        ExponentialBuilder::default()
            .with_max_times(10)
            .with_max_delay(TEST_MAX_BACKOFF_DELAY)
            .with_min_delay(DEFAULT_MIN_BACKOFF_DELAY)
            .with_factor(1.5)
    }

    fn generate_client_identity(expired: bool) -> Result<(Vec<u8>, Vec<u8>)> {
        let intermediate_key = PKey::from_rsa(Rsa::generate(2048)?)?;
        let mut intermediate_name = X509NameBuilder::new()?;
        intermediate_name.append_entry_by_text("O", "NVIDIA")?;
        intermediate_name.append_entry_by_text("CN", "test-intermediate")?;
        let intermediate_name = intermediate_name.build();
        let mut intermediate_serial = BigNum::new()?;
        intermediate_serial.rand(128, MsbOption::MAYBE_ZERO, false)?;
        let intermediate_serial = intermediate_serial.to_asn1_integer()?;
        let mut intermediate = X509Builder::new()?;
        intermediate.set_version(2)?;
        intermediate.set_serial_number(&intermediate_serial)?;
        intermediate.set_subject_name(&intermediate_name)?;
        intermediate.set_issuer_name(&intermediate_name)?;
        intermediate.set_pubkey(&intermediate_key)?;
        let intermediate_not_before = Asn1Time::days_from_now(0)?;
        let intermediate_not_after = Asn1Time::days_from_now(365)?;
        intermediate.set_not_before(&intermediate_not_before)?;
        intermediate.set_not_after(&intermediate_not_after)?;
        intermediate.append_extension(BasicConstraints::new().critical().ca().build()?)?;
        intermediate.append_extension(KeyUsage::new().key_cert_sign().crl_sign().build()?)?;
        intermediate.sign(&intermediate_key, MessageDigest::sha256())?;
        let intermediate = intermediate.build();

        let private_key = PKey::from_rsa(Rsa::generate(2048)?)?;
        let mut name = X509NameBuilder::new()?;
        name.append_entry_by_text("O", "NVIDIA")?;
        name.append_entry_by_text("OU", "NSPECT-TEST")?;
        name.append_entry_by_text("CN", "test-tenant")?;
        let name = name.build();

        let mut serial = BigNum::new()?;
        serial.rand(128, MsbOption::MAYBE_ZERO, false)?;
        let serial = serial.to_asn1_integer()?;
        let mut certificate = X509Builder::new()?;
        certificate.set_version(2)?;
        certificate.set_serial_number(&serial)?;
        certificate.set_subject_name(&name)?;
        certificate.set_issuer_name(intermediate.subject_name())?;
        certificate.set_pubkey(&private_key)?;
        let (not_before, not_after) = if expired {
            (Asn1Time::from_unix(1)?, Asn1Time::from_unix(2)?)
        } else {
            (Asn1Time::days_from_now(0)?, Asn1Time::days_from_now(30)?)
        };
        certificate.set_not_before(&not_before)?;
        certificate.set_not_after(&not_after)?;
        certificate.append_extension(BasicConstraints::new().critical().build()?)?;
        certificate.append_extension(KeyUsage::new().digital_signature().build()?)?;
        certificate.append_extension(ExtendedKeyUsage::new().client_auth().build()?)?;
        certificate.sign(&intermediate_key, MessageDigest::sha256())?;

        let mut certificate_chain = certificate.build().to_pem()?;
        certificate_chain.extend(intermediate.to_pem()?);

        Ok((certificate_chain, private_key.private_key_to_pem_pkcs8()?))
    }

    fn write_identity(
        directory: &Path,
        certificate: &[u8],
        private_key: &[u8],
    ) -> Result<TimeseriesDbSettings> {
        let certificate_path = directory.join("client_chain.crt");
        let private_key_path = directory.join("client.key");
        std::fs::write(&certificate_path, certificate)?;
        std::fs::write(&private_key_path, private_key)?;
        Ok(TimeseriesDbSettings {
            timeseries_db_url: "https://metrics.example.test".to_string(),
            auth_mode: Some(TimeseriesDbAuthMode::Mtls),
            disable_auth: false,
            client_certificate_path: Some(certificate_path),
            client_private_key_path: Some(private_key_path),
            ..Default::default()
        })
    }

    #[test]
    fn mtls_requires_both_identity_paths() {
        let config = TimeseriesDbSettings {
            timeseries_db_url: "https://metrics.example.test".to_string(),
            auth_mode: Some(TimeseriesDbAuthMode::Mtls),
            disable_auth: false,
            ..Default::default()
        };

        let error = TimeseriesDbClient::new(&config, None)
            .err()
            .expect("mTLS configuration without certificate paths must fail");
        assert!(error.to_string().contains("client_certificate_path"));
    }

    #[test]
    fn mtls_requires_https_origin_without_a_path() {
        for invalid_url in [
            "http://metrics.example.test",
            "https://metrics.example.test/legacy-api",
            "https://metrics.example.test?tenant=test",
            "https://metrics.example.test#fragment",
        ] {
            let config = TimeseriesDbSettings {
                timeseries_db_url: invalid_url.to_string(),
                auth_mode: Some(TimeseriesDbAuthMode::Mtls),
                ..Default::default()
            };

            let error = TimeseriesDbClient::new(&config, None)
                .err()
                .expect("invalid mTLS endpoint must fail");
            assert!(
                error.to_string().contains("https URL")
                    || error.to_string().contains("must be an origin"),
                "unexpected error for {invalid_url}: {error}"
            );
        }
    }

    #[test]
    fn mtls_accepts_a_valid_guide_style_identity() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let (certificate, private_key) = generate_client_identity(false)?;
        let config = write_identity(directory.path(), &certificate, &private_key)?;

        let client = TimeseriesDbClient::new(&config, None)?;

        assert_eq!(client.auth_mode, TimeseriesDbAuthMode::Mtls);
        Ok(())
    }

    #[test]
    fn mtls_rejects_a_private_key_for_another_certificate() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let (certificate, _) = generate_client_identity(false)?;
        let (_, other_private_key) = generate_client_identity(false)?;
        let config = write_identity(directory.path(), &certificate, &other_private_key)?;

        let error = TimeseriesDbClient::new(&config, None)
            .err()
            .expect("mismatched certificate and key must fail");

        assert!(error.to_string().contains("does not match"));
        Ok(())
    }

    #[test]
    fn mtls_rejects_an_expired_certificate() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let (certificate, private_key) = generate_client_identity(true)?;
        let config = write_identity(directory.path(), &certificate, &private_key)?;

        let error = TimeseriesDbClient::new(&config, None)
            .err()
            .expect("expired certificate must fail");

        assert!(error.to_string().contains("expired"));
        Ok(())
    }

    #[test]
    fn mtls_rejects_a_malformed_private_key() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let (certificate, _) = generate_client_identity(false)?;
        let config = write_identity(directory.path(), &certificate, b"not a private key")?;

        let error = TimeseriesDbClient::new(&config, None)
            .err()
            .expect("malformed private key must fail");

        assert!(error.to_string().contains("Failed to parse"));
        Ok(())
    }

    #[test]
    fn mtls_uses_the_prometheus_query_range_path() {
        let base_url = Url::parse("https://metrics.example.test").unwrap();

        let mtls_url = query_range_url(base_url.clone(), TimeseriesDbAuthMode::Mtls).unwrap();
        let local_url = query_range_url(base_url.clone(), TimeseriesDbAuthMode::None).unwrap();
        let token_url = query_range_url(base_url, TimeseriesDbAuthMode::Token).unwrap();

        assert_eq!(mtls_url.path(), "/api/v1/query_range");
        assert_eq!(local_url.path(), "/api/v1/query_range");
        assert_eq!(token_url.path(), "/v1/query_range");
    }

    #[tokio::test]
    async fn test_successful_query() -> Result<()> {
        let state = Arc::new(MockServerState::new(
            vec![(StatusCode::OK, get_mock_response())],
            vec!["".to_string(), "".to_string()],
        ));

        let server_url = start_mock_server(state).await;

        let config = TimeseriesDbSettings {
            authn_url: "".to_string(),
            timeseries_db_url: server_url,
            disable_auth: true,
            env: "stg".to_string(),
            ignore_env: false,
            backoff: Some(fast_test_backoff()),
            ..Default::default()
        };

        let client =
            TimeseriesDbClient::new(&config, Some(Arc::new(MockCredentialProvider::new(false))))?;

        let start = Utc.timestamp_opt(1748551740, 0).unwrap();
        let end = Utc.timestamp_opt(1748551800, 0).unwrap();

        let result = client
            .query_range("test_query", start, end, StdDuration::from_secs(60))
            .await?;

        assert_eq!(result.status, "success");
        assert_eq!(result.data.result_type, "matrix");
        assert_eq!(result.data.result.len(), 1);
        assert_eq!(result.data.result[0].values.len(), 2);
        Ok(())
    }

    #[tokio::test]
    async fn test_credential_refresh() -> Result<()> {
        let state = Arc::new(MockServerState::new(
            vec![
                (StatusCode::OK, get_mock_response()),
                (StatusCode::OK, get_mock_response()),
            ],
            vec!["".to_string(), "".to_string()],
        ));

        let server_url = start_mock_server(state).await;

        let config = TimeseriesDbSettings {
            authn_url: "".to_string(),
            timeseries_db_url: server_url,
            disable_auth: true,
            env: "stg".to_string(),
            ignore_env: false,
            backoff: Some(fast_test_backoff()),
            ..Default::default()
        };

        let client =
            TimeseriesDbClient::new(&config, Some(Arc::new(MockCredentialProvider::new(false))))?;

        let start = Utc.timestamp_opt(1748551740, 0).unwrap();
        let end = Utc.timestamp_opt(1748551800, 0).unwrap();

        let result1 = client
            .query_range("test_query", start, end, StdDuration::from_secs(60))
            .await?;
        assert_eq!(result1.status, "success");

        {
            let mut creds_guard = client.credentials.lock().unwrap();
            if let Some(creds) = &mut *creds_guard {
                creds.expiration = Utc::now() - Duration::from_secs(60);
            }
        }

        let result2 = client
            .query_range("test_query", start, end, StdDuration::from_secs(60))
            .await?;
        assert_eq!(result2.status, "success");

        Ok(())
    }

    #[tokio::test]
    async fn test_retry_on_server_error() -> Result<()> {
        let state = Arc::new(MockServerState::new(
            vec![
                (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "Server error".to_string(),
                ),
                (StatusCode::OK, get_mock_response()),
            ],
            vec!["".to_string(), "".to_string()],
        ));

        let server_url = start_mock_server(state).await;

        let config = TimeseriesDbSettings {
            authn_url: "".to_string(),
            timeseries_db_url: server_url,
            disable_auth: true,
            env: "stg".to_string(),
            ignore_env: false,
            backoff: Some(fast_test_backoff()),
            ..Default::default()
        };

        let client =
            TimeseriesDbClient::new(&config, Some(Arc::new(MockCredentialProvider::new(false))))?;

        let start = Utc.timestamp_opt(1748551740, 0).unwrap();
        let end = Utc.timestamp_opt(1748551800, 0).unwrap();

        let result = client
            .query_range("test_query", start, end, StdDuration::from_secs(60))
            .await?;
        assert_eq!(result.status, "success");

        Ok(())
    }

    #[tokio::test]
    async fn test_no_retry_on_auth_error() -> Result<()> {
        // The client will:
        // 1. Try with token_0 (gets 401)
        // 2. Don't retry
        let state = Arc::new(MockServerState::new(
            vec![(StatusCode::UNAUTHORIZED, "Unauthorized".to_string())],
            vec!["test_token_0".to_string()],
        ));

        let server_url = start_mock_server(state).await;

        let config = TimeseriesDbSettings {
            authn_url: "".to_string(),
            timeseries_db_url: server_url,
            disable_auth: false,
            env: "stg".to_string(),
            ignore_env: false,
            backoff: Some(fast_test_backoff()),
            ..Default::default()
        };

        // Create the credential provider and wrap it in Arc
        let credential_provider: Arc<dyn CredentialProvider + Send + Sync> =
            Arc::new(MockCredentialProvider::new(false));

        let client = TimeseriesDbClient::new(&config, Some(credential_provider))?;

        let start = Utc.timestamp_opt(1748551740, 0).unwrap();
        let end = Utc.timestamp_opt(1748551800, 0).unwrap();

        let result = client
            .query_range("test_query", start, end, StdDuration::from_secs(60))
            .await;

        assert!(result.is_err());
        assert!(result.unwrap_err().to_string().contains("401"));

        Ok(())
    }

    #[tokio::test]
    async fn test_max_retries_exceeded() -> Result<()> {
        let state = Arc::new(MockServerState::new(
            vec![
                (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "Server error".to_string(),
                ),
                (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "Server error".to_string(),
                ),
                (
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "Server error".to_string(),
                ),
            ],
            vec![
                "test_token_0".to_string(),
                "test_token_0".to_string(),
                "test_token_0".to_string(),
            ],
        ));

        let server_url = start_mock_server(state).await;

        let backoff = ExponentialBuilder::default()
            .with_max_times(10)
            .with_max_delay(StdDuration::from_millis(500))
            .with_min_delay(StdDuration::from_millis(50))
            .with_factor(1.2);

        let config = TimeseriesDbSettings {
            authn_url: "".to_string(),
            timeseries_db_url: server_url,
            disable_auth: true,
            env: "stg".to_string(),
            ignore_env: false,
            backoff: Some(backoff),
            ..Default::default()
        };

        let client =
            TimeseriesDbClient::new(&config, Some(Arc::new(MockCredentialProvider::new(false))))?;

        let start = Utc.timestamp_opt(1748551740, 0).unwrap();
        let end = Utc.timestamp_opt(1748551800, 0).unwrap();

        let result = client
            .query_range("test_query", start, end, StdDuration::from_secs(60))
            .await;
        assert!(result.is_err());

        Ok(())
    }

    #[tokio::test]
    async fn test_credential_provider_failure() -> Result<()> {
        let config = TimeseriesDbSettings {
            authn_url: "".to_string(),
            timeseries_db_url: "http://localhost:10903".to_string(),
            disable_auth: false,
            env: "stg".to_string(),
            ignore_env: false,
            backoff: Some(fast_test_backoff()),
            ..Default::default()
        };

        let credential_provider = Arc::new(MockCredentialProvider::new(true));

        let client = TimeseriesDbClient::new(&config, Some(credential_provider))?;

        let start = Utc.timestamp_opt(1748551740, 0).unwrap();
        let end = Utc.timestamp_opt(1748551800, 0).unwrap();

        let result = client
            .query_range("test_query", start, end, StdDuration::from_secs(60))
            .await;
        assert!(result.is_err());
        let error_msg = result.unwrap_err().to_string();
        assert!(error_msg.contains("No valid credentials available"));
        Ok(())
    }

    #[tokio::test]
    async fn test_retry_on_client_error() -> Result<()> {
        let state = Arc::new(MockServerState::new(
            vec![
                (StatusCode::BAD_REQUEST, "Bad Request".to_string()),
                (StatusCode::OK, get_mock_response()),
            ],
            vec!["test_token_0".to_string(), "test_token_0".to_string()],
        ));

        let server_url = start_mock_server(state).await;

        let config = TimeseriesDbSettings {
            authn_url: "".to_string(),
            timeseries_db_url: server_url,
            disable_auth: false,
            env: "stg".to_string(),
            ignore_env: false,
            backoff: Some(fast_test_backoff()),
            ..Default::default()
        };

        let client =
            TimeseriesDbClient::new(&config, Some(Arc::new(MockCredentialProvider::new(false))))?;

        let start = Utc.timestamp_opt(1748551740, 0).unwrap();
        let end = Utc.timestamp_opt(1748551800, 0).unwrap();

        let result = client
            .query_range("test_query", start, end, StdDuration::from_secs(60))
            .await?;
        assert_eq!(result.status, "success");

        Ok(())
    }

    #[tokio::test]
    async fn test_health_status_on_auth_vs_other_errors() -> Result<()> {
        let auth_error_state = Arc::new(MockServerState::new(
            vec![(StatusCode::UNAUTHORIZED, "Unauthorized".to_string())],
            vec!["test_token_0".to_string()],
        ));

        let server_url = start_mock_server(auth_error_state).await;

        let config = TimeseriesDbSettings {
            authn_url: "".to_string(),
            timeseries_db_url: server_url,
            disable_auth: false,
            env: "stg".to_string(),
            ignore_env: false,
            backoff: Some(fast_test_backoff()),
            ..Default::default()
        };

        let client =
            TimeseriesDbClient::new(&config, Some(Arc::new(MockCredentialProvider::new(false))))?;

        let start = Utc.timestamp_opt(1748551740, 0).unwrap();
        let end = Utc.timestamp_opt(1748551800, 0).unwrap();

        let result = client
            .query_range("test_query", start, end, StdDuration::from_secs(60))
            .await;
        assert!(result.is_err());

        let health_result = client.health().await;
        assert!(!health_result.is_healthy);
        assert!(health_result.message.unwrap().contains("unhealthy"));

        Ok(())
    }

    #[tokio::test]
    async fn test_client_error_retry_behavior() -> Result<()> {
        let state = Arc::new(MockServerState::new(
            vec![
                (StatusCode::BAD_REQUEST, "Bad Request".to_string()),
                (StatusCode::OK, get_mock_response()),
            ],
            vec!["".to_string(), "".to_string()],
        ));

        let server_url = start_mock_server(state).await;

        let config = TimeseriesDbSettings {
            authn_url: "".to_string(),
            timeseries_db_url: server_url,
            disable_auth: true,
            env: "stg".to_string(),
            ignore_env: false,
            backoff: Some(fast_test_backoff()),
            ..Default::default()
        };

        let client = TimeseriesDbClient::new(&config, None)?;

        let start = Utc.timestamp_opt(1748551740, 0).unwrap();
        let end = Utc.timestamp_opt(1748551800, 0).unwrap();

        let result = client
            .query_range("test_query", start, end, StdDuration::from_secs(60))
            .await?;

        assert_eq!(result.status, "success");
        Ok(())
    }

    #[tokio::test]
    async fn test_response_deserialization() -> Result<()> {
        let json = get_mock_response();
        let response: TimeseriesDbResponse = serde_json::from_str(&json)?;

        assert_eq!(response.status, "success");
        assert_eq!(response.data.result_type, "matrix");
        assert_eq!(response.data.result.len(), 1);

        let result = &response.data.result[0];
        assert_eq!(result.metric.name, Some("test_metric".to_string()));
        assert_eq!(result.metric.error_code, Some("0".to_string()));
        assert_eq!(result.metric.function_id, Some("test-id".to_string()));
        assert_eq!(
            result.metric.instance_id,
            Some("test-instance-id".to_string())
        );

        assert_eq!(result.values.len(), 2);
        assert_eq!(result.values[0].0 as i64, 1748551740);
        assert_eq!(result.values[0].1, "6");
        assert_eq!(result.values[1].0 as i64, 1748551800);
        assert_eq!(result.values[1].1, "7");

        Ok(())
    }

    /// Integration test that connects to real auth and TimeseriesDb servers.
    /// Requires environment variables: TIMESERIESDB_URL, AUTH_URL, AUTH_USERNAME, AUTH_PASSWORD
    /// Run with: `cargo test test_real_integration -- --ignored`
    #[tokio::test]
    #[ignore = "Requires real auth and TimeseriesDb servers - set TIMESERIESDB_URL, AUTH_URL, AUTH_USERNAME, AUTH_PASSWORD"]
    async fn test_real_integration() -> Result<()> {
        let config = TimeseriesDbSettings::default();
        // Create a temporary secrets file for testing
        let temp_dir = std::env::temp_dir();
        let secrets_file = temp_dir.join("test_secrets.json");
        let secrets_content = serde_json::json!({
            "kv": {
                "timeseries_db": {
                    "username": "test_username",
                    "password": "test_password"
                }
            }
        });
        tokio::fs::write(&secrets_file, secrets_content.to_string())
            .await
            .unwrap();

        // Create a real credential provider
        let secrets_watcher = Arc::new(SecretFileWatcher::new(&secrets_file).await.unwrap());
        let credential_provider = Arc::new(AuthnCredentialProvider::new(&config, secrets_watcher));

        // Create the client with real URLs
        let client = TimeseriesDbClient::new(&config, Some(credential_provider))?;

        // Test a real query
        let end = Utc::now();
        let start = end - chrono::Duration::hours(1);

        // Try a very simple query first - just a constant
        let query = "1";

        let result = client
            .query_range(query, start, end, StdDuration::from_secs(300))
            .await?;

        // Basic assertions
        assert_eq!(result.status, "success");
        assert!(result.data.result_type == "matrix" || result.data.result_type == "vector");

        Ok(())
    }
}

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

use std::{
    fmt,
    path::PathBuf,
    time::{Duration, Instant},
};

use anyhow::{Context, anyhow};
use sonic_rs::JsonValueTrait;
use tokio::sync::Mutex;
use tracing::debug;

const DEFAULT_EXPIRES_IN: Duration = Duration::from_secs(300);
const EXPIRY_REFRESH_BUFFER: Duration = Duration::from_secs(60);
const MAX_EXPIRES_IN: Duration = Duration::from_secs(24 * 60 * 60);
const TOKEN_REQUEST_TIMEOUT: Duration = Duration::from_secs(10);

fn token_client(timeout: Duration) -> reqwest::Client {
    // Bound the single-flight refresh lock when the endpoint stalls at headers or body.
    reqwest::Client::builder()
        .timeout(timeout)
        .build()
        .expect("failed to build oauth2 token client")
}

/// Mints and caches OAuth2 tokens, re-reading credentials for every refresh.
pub struct ClientCredentialsProvider {
    token_url: String,
    credentials_path: PathBuf,
    scope: String,
    http: reqwest::Client,
    cache: Mutex<Option<CachedToken>>,
}

struct CachedToken(String, Instant);

impl ClientCredentialsProvider {
    /// Mints scoped tokens at `<provider_host>/token` using `credentials_path`.
    pub fn new(provider_host: &str, credentials_path: PathBuf, scope: impl Into<String>) -> Self {
        Self {
            token_url: format!("{}/token", provider_host.trim_end_matches('/')),
            credentials_path,
            scope: scope.into(),
            http: token_client(TOKEN_REQUEST_TIMEOUT),
            cache: Mutex::new(None),
        }
    }

    /// Returns a valid cached token, coalescing refresh callers under one lock.
    pub async fn resolve(&self) -> anyhow::Result<String> {
        let mut cache = self.cache.lock().await;
        if let Some(CachedToken(token, expires_at)) = cache.as_ref()
            && Instant::now() + EXPIRY_REFRESH_BUFFER < *expires_at
        {
            return Ok(token.clone());
        }

        let credentials = crate::read_secret_file(&self.credentials_path).await?;
        let (client_id, client_secret) = parse_client_credentials(&credentials)?;

        // Send the client credentials via HTTP Basic auth (client_secret_basic),
        // matching the SSA token endpoint and the Go services' oauth2 client. Each
        // value is form-urlencoded first per RFC 6749 §2.3.1 (reqwest only base64s
        // the raw `id:secret`), so reserved characters in a rotated secret survive.
        // The form body carries only grant_type and scope.
        let response = self
            .http
            .post(&self.token_url)
            .basic_auth(
                form_encode(client_id.as_str()),
                Some(form_encode(client_secret.as_str())),
            )
            .form(&[
                ("grant_type", "client_credentials"),
                ("scope", self.scope.as_str()),
            ])
            .send()
            .await
            .context("oauth2 token request failed")?;
        let (access_token, expires_in) = parse_token_response(response).await?;
        let expires_in_secs = expires_in.as_secs();
        debug!(scope = %self.scope, expires_in_secs, "minted worker-auth client-credentials token");
        let expires_at = Instant::now() + expires_in;
        *cache = Some(CachedToken(access_token.clone(), expires_at));
        Ok(access_token)
    }
}

/// Form-urlencodes an OAuth credential the same way Go's `url.QueryEscape` does,
/// as required for `client_secret_basic` (RFC 6749 §2.3.1). Keeps the unreserved
/// set (`A-Za-z0-9-_.~`), maps space to `+`, and percent-encodes everything else
/// with uppercase hex. Base64 secret characters (`+` `/` `=`) therefore survive.
fn form_encode(value: &str) -> String {
    const HEX: &[u8; 16] = b"0123456789ABCDEF";
    let mut out = String::with_capacity(value.len());
    for &b in value.as_bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(b as char);
            }
            b' ' => out.push('+'),
            _ => {
                out.push('%');
                out.push(HEX[(b >> 4) as usize] as char);
                out.push(HEX[(b & 0x0f) as usize] as char);
            }
        }
    }
    out
}

async fn parse_token_response(response: reqwest::Response) -> anyhow::Result<(String, Duration)> {
    let status = response.status();
    if !status.is_success() {
        return Err(anyhow!("oauth2 token endpoint returned status {status}"));
    }
    let body = response.bytes().await;
    let body = body.context("failed to read oauth2 token response")?;
    let access_token = sonic_rs::get(&body, ["access_token"])
        .context("oauth2 token response missing access_token")?
        .as_str()
        .ok_or_else(|| anyhow!("oauth2 access_token is not a string"))?
        .to_owned();
    let expires_in = sonic_rs::get(&body, ["expires_in"])
        .ok()
        .and_then(|value| value.as_u64())
        .map_or(DEFAULT_EXPIRES_IN, Duration::from_secs)
        .min(MAX_EXPIRES_IN);
    Ok((access_token, expires_in))
}

fn parse_client_credentials(bytes: &[u8]) -> anyhow::Result<(String, String)> {
    let field = |name| {
        sonic_rs::get(bytes, [name])
            .with_context(|| format!("secrets file missing {name}"))?
            .as_str()
            .with_context(|| format!("secrets file value at {name} is not a string"))
            .map(str::to_owned)
    };
    Ok((field("id")?, field("secret")?))
}

// Keep credentials and cached tokens out of logs.
impl fmt::Debug for ClientCredentialsProvider {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("ClientCredentialsProvider")
            .field("token_url", &self.token_url)
            .field("scope", &self.scope)
            .field("credentials_path", &self.credentials_path)
            .finish_non_exhaustive()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use std::sync::Arc;
    use std::sync::atomic::{AtomicUsize, Ordering};

    use axum::Router;
    use axum::extract::{Form, State};
    use axum::routing::post;
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt};

    type EndpointState = (Arc<AtomicUsize>, u64);

    async fn serve_token_endpoint(expires_in: u64) -> (String, Arc<AtomicUsize>) {
        let hits = Arc::new(AtomicUsize::new(0));
        let app = Router::new()
            .route("/token", post(handle_token))
            .with_state((hits.clone(), expires_in));

        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        (format!("http://{addr}"), hits)
    }

    async fn handle_token(
        State((hits, expires_in)): State<EndpointState>,
        headers: axum::http::HeaderMap,
        Form(form): Form<std::collections::HashMap<String, String>>,
    ) -> String {
        hits.fetch_add(1, Ordering::SeqCst);
        // Credentials must be sent via HTTP Basic auth (client_secret_basic), not
        // the form body, matching the SSA token endpoint. Compare the full header
        // without logging it so a failure cannot leak the encoded credentials.
        // Expected value is base64 of the form-urlencoded "id:secret", i.e.
        // "client+id%2B%26:client-secret+%2B%3D%26" for the fixture below.
        let auth = headers
            .get(axum::http::header::AUTHORIZATION)
            .and_then(|v| v.to_str().ok())
            .unwrap_or_default();
        assert!(
            auth == "Basic Y2xpZW50K2lkJTJCJTI2OmNsaWVudC1zZWNyZXQrJTJCJTNEJTI2",
            "unexpected or missing Basic auth credentials"
        );
        assert!(
            !form.contains_key("client_id"),
            "client_id must not be in the form body"
        );
        assert!(
            !form.contains_key("client_secret"),
            "client_secret must not be in the form body"
        );
        for (field, expected) in [
            ("grant_type", "client_credentials"),
            ("scope", "llm:check worker/+&"),
        ] {
            assert_eq!(form.get(field).map(String::as_str), Some(expected));
        }
        format!(
            r#"{{"access_token":"minted-token","token_type":"Bearer","expires_in":{}}}"#,
            expires_in
        )
    }

    #[test]
    fn form_encode_matches_go_query_escape() {
        // Unreserved set is passed through; space maps to '+'.
        assert_eq!(form_encode("nvssa-stg-AbC_1.2~3"), "nvssa-stg-AbC_1.2~3");
        assert_eq!(form_encode("a b"), "a+b");
        // Base64 secret characters must be percent-encoded (uppercase hex).
        assert_eq!(form_encode("ssap-a+b/c="), "ssap-a%2Bb%2Fc%3D");
        assert_eq!(form_encode("client id+&"), "client+id%2B%26");
        assert_eq!(form_encode("client-secret +=&"), "client-secret+%2B%3D%26");
    }

    fn credentials_file(id: &str, secret: &str) -> tempfile::NamedTempFile {
        let mut tmp = tempfile::NamedTempFile::new().unwrap();
        write!(tmp, r#"{{"id": "{id}", "secret": "{secret}"}}"#).unwrap();
        tmp
    }

    fn provider_for(host: &str) -> (ClientCredentialsProvider, tempfile::NamedTempFile) {
        let credentials = credentials_file("client id+&", "client-secret +=&");
        let provider = ClientCredentialsProvider::new(
            host,
            credentials.path().to_path_buf(),
            "llm:check worker/+&",
        );
        (provider, credentials)
    }

    async fn read_request(socket: &mut tokio::net::TcpStream) {
        let mut request_line = String::new();
        tokio::io::BufReader::new(socket)
            .read_line(&mut request_line)
            .await
            .unwrap();
        assert_eq!(request_line, "POST /token HTTP/1.1\r\n");
    }

    async fn stalled_request_error(
        response_head: Option<&'static [u8]>,
        request_timeout: Duration,
        retry_succeeds: bool,
    ) -> anyhow::Error {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let host = format!("http://{}", listener.local_addr().unwrap());
        let (stalled, request_stalled) = tokio::sync::oneshot::channel();
        let (release, request_finished) = tokio::sync::oneshot::channel();
        let server = tokio::spawn(async move {
            let (mut first, _) = listener.accept().await.unwrap();
            read_request(&mut first).await;
            if let Some(response_head) = response_head {
                first.write_all(response_head).await.unwrap();
            }
            stalled.send(()).unwrap();
            request_finished.await.unwrap();
            // The retry must establish a fresh connection after the timed-out request.
            drop(first);
            if retry_succeeds {
                let (mut retry, _) = listener.accept().await.unwrap();
                read_request(&mut retry).await;
                let body = r#"{"access_token":"minted-token","expires_in":300}"#;
                let head = format!("HTTP/1.1 200 OK\r\nContent-Length: {}\r\n\r\n", body.len());
                retry.write_all(head.as_bytes()).await.unwrap();
                retry.write_all(body.as_bytes()).await.unwrap();
            }
        });
        let (mut provider, _credentials) = provider_for(&host);
        provider.http = token_client(request_timeout);
        let (stalled, outcome) = tokio::join!(
            request_stalled,
            tokio::time::timeout(Duration::from_secs(1), provider.resolve())
        );
        stalled.unwrap();
        let error = outcome
            .expect("stalled token request did not finish within its bound")
            .unwrap_err();
        release.send(()).unwrap();
        if retry_succeeds {
            assert_eq!(provider.resolve().await.unwrap(), "minted-token");
        }
        server.await.unwrap();
        error
    }

    fn assert_timed_out(error: anyhow::Error) {
        assert!(
            error
                .chain()
                .any(|cause| cause.to_string().contains("timed out"))
        );
    }

    type Fixture = (
        ClientCredentialsProvider,
        Arc<AtomicUsize>,
        tempfile::NamedTempFile,
    );

    async fn fixture(expires_in: u64) -> Fixture {
        let (host, hits) = serve_token_endpoint(expires_in).await;
        let (provider, credentials) = provider_for(&host);
        (provider, hits, credentials)
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn mints_token_without_exposing_secrets_in_debug() {
        let (provider, _hits, _credentials) = fixture(300).await;
        assert_eq!(provider.resolve().await.unwrap(), "minted-token");
        let output = format!("{provider:?}");
        for secret in ["client id+&", "client-secret +=&", "minted-token"] {
            assert!(!output.contains(secret));
        }
        let provider = crate::AuthTokenProvider::ClientCredentials(Arc::new(provider));
        assert_eq!(format!("{provider:?}"), "ClientCredentials(<redacted>)");
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn caches_token_within_validity_window() {
        let (provider, hits, _credentials) = fixture(300).await;
        provider.resolve().await.unwrap();
        provider.resolve().await.unwrap();
        assert_eq!(hits.load(Ordering::SeqCst), 1);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn refetches_when_token_is_near_expiry() {
        let (provider, hits, _credentials) = fixture(1).await;
        provider.resolve().await.unwrap();
        provider.resolve().await.unwrap();
        assert_eq!(hits.load(Ordering::SeqCst), 2);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn clamps_oversized_expires_in() {
        let (provider, hits, _credentials) = fixture(u64::MAX).await;
        assert_eq!(provider.resolve().await.unwrap(), "minted-token");
        provider.resolve().await.unwrap();
        assert_eq!(hits.load(Ordering::SeqCst), 1);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn errors_when_credentials_file_lacks_id() {
        let mut tmp = tempfile::NamedTempFile::new().unwrap();
        write!(tmp, r#"{{"secret": "client-secret"}}"#).unwrap();
        let provider = ClientCredentialsProvider::new(
            "http://unused",
            tmp.path().to_path_buf(),
            "llm:check_worker",
        );

        let error = provider.resolve().await.unwrap_err();
        assert!(error.to_string().contains("missing id"));
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn errors_on_non_success_status() {
        let error = stalled_request_error(
            Some(b"HTTP/1.1 401 Unauthorized\r\nContent-Length: 1024\r\n\r\n"),
            TOKEN_REQUEST_TIMEOUT,
            false,
        )
        .await;
        assert!(error.to_string().contains("401"));
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn request_timeout_releases_single_flight_lock_for_retry() {
        let error = stalled_request_error(None, Duration::from_millis(250), true).await;
        assert_timed_out(error);
    }

    #[tokio::test(flavor = "multi_thread")]
    async fn request_timeout_covers_success_response_body() {
        let error = stalled_request_error(
            Some(b"HTTP/1.1 200 OK\r\nContent-Length: 1024\r\n\r\n"),
            Duration::from_millis(250),
            false,
        )
        .await;
        assert_timed_out(error);
    }
}

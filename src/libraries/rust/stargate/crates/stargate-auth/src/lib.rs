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

use std::{fmt, path::Path, path::PathBuf, str, sync::Arc};

use anyhow::Context;
use sonic_rs::JsonValueTrait;

mod client_credentials;

pub use client_credentials::ClientCredentialsProvider;

/// Provides opaque tokens from fixed, re-read file, JSON, or OAuth2 sources.
#[derive(Clone)]
pub enum AuthTokenProvider {
    /// A fixed token value supplied at startup.
    Static(String),
    /// A plain-text file whose trimmed contents are the token.
    File(PathBuf),
    /// A JSON file whose object-key path selects the token.
    JsonFile { path: PathBuf, key: Vec<String> },
    /// OAuth2 client-credentials grant. See [`ClientCredentialsProvider`].
    ClientCredentials(Arc<ClientCredentialsProvider>),
}

impl fmt::Debug for AuthTokenProvider {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Static(_) => f.write_str("Static(<redacted>)"),
            Self::File(path) => write!(f, "File({path:?})"),
            Self::JsonFile { path, key } => write!(f, "JsonFile({path:?}, {key:?})"),
            Self::ClientCredentials(_) => f.write_str("ClientCredentials(<redacted>)"),
        }
    }
}

impl AuthTokenProvider {
    /// Builds a scoped client-credentials provider using `credentials_path`.
    pub fn client_credentials(
        provider_host: &str,
        credentials_path: PathBuf,
        scope: impl Into<String>,
    ) -> Self {
        Self::ClientCredentials(
            ClientCredentialsProvider::new(provider_host, credentials_path, scope).into(),
        )
    }

    /// Resolves the current token, re-reading file-backed sources.
    pub async fn resolve_token(&self) -> anyhow::Result<String> {
        match self {
            Self::Static(token) => Ok(token.clone()),
            Self::File(path) => {
                let bytes = read_secret_file(path).await?;
                str::from_utf8(&bytes)
                    .with_context(|| format!("token file {} is not UTF-8", path.display()))
                    .map(|contents| contents.trim().to_owned())
            }
            Self::JsonFile { path, key } => {
                let bytes = read_secret_file(path).await?;
                sonic_rs::get(&*bytes, key.iter().map(String::as_str))
                    .context("failed to extract key from secrets file")?
                    .as_str()
                    .context("token value at key path is not a string")
                    .map(str::to_owned)
            }
            Self::ClientCredentials(provider) => provider.resolve().await,
        }
    }
}

async fn read_secret_file(path: &Path) -> anyhow::Result<Vec<u8>> {
    tokio::fs::read(path)
        .await
        .with_context(|| format!("failed to read {}", path.display()))
}

#[cfg(test)]
mod tests {
    use std::io::Write;

    use super::*;

    fn temp_file(contents: &[u8]) -> tempfile::NamedTempFile {
        let mut file = tempfile::NamedTempFile::new().unwrap();
        file.write_all(contents).unwrap();
        file
    }

    fn file_provider(contents: &[u8]) -> (tempfile::NamedTempFile, AuthTokenProvider) {
        let file = temp_file(contents);
        let provider = AuthTokenProvider::File(file.path().to_path_buf());
        (file, provider)
    }

    fn json_provider(contents: &str, key: &[&str]) -> (tempfile::NamedTempFile, AuthTokenProvider) {
        let file = temp_file(contents.as_bytes());
        let provider = AuthTokenProvider::JsonFile {
            path: file.path().to_path_buf(),
            key: key.iter().map(|segment| (*segment).to_string()).collect(),
        };
        (file, provider)
    }

    #[tokio::test]
    async fn static_returns_literal() {
        let provider = AuthTokenProvider::Static("my-token".to_string());
        assert_eq!(provider.resolve_token().await.unwrap(), "my-token");
        assert_eq!(format!("{provider:?}"), "Static(<redacted>)");
    }

    #[tokio::test]
    async fn file_reads_and_trims() {
        let (_file, provider) = file_provider(b"  file-token  \n");
        assert_eq!(provider.resolve_token().await.unwrap(), "file-token");
    }

    #[tokio::test]
    async fn json_file_top_level_key() {
        let (_file, provider) = json_provider(r#"{"authToken": "abc123"}"#, &["authToken"]);
        assert_eq!(provider.resolve_token().await.unwrap(), "abc123");
    }

    #[tokio::test]
    async fn json_file_nested_key() {
        let (_file, provider) =
            json_provider(r#"{"auth": {"token": "nested_val"}}"#, &["auth", "token"]);
        assert_eq!(provider.resolve_token().await.unwrap(), "nested_val");
    }

    #[tokio::test]
    async fn json_file_missing_key_errors() {
        let (_file, provider) = json_provider(r#"{"other": "val"}"#, &["authToken"]);
        assert!(provider.resolve_token().await.is_err());
    }

    #[tokio::test]
    async fn json_file_invalid_json_errors() {
        let (_file, provider) = json_provider("not-json", &["authToken"]);
        assert!(provider.resolve_token().await.is_err());
    }

    #[tokio::test]
    async fn missing_token_file_preserves_io_error_source() {
        let path = tempfile::tempdir().unwrap().path().join("missing-token");
        let provider = AuthTokenProvider::File(path);

        let error = provider.resolve_token().await.unwrap_err();

        assert!(
            error
                .chain()
                .any(|source| source.downcast_ref::<std::io::Error>().is_some())
        );
    }

    #[tokio::test]
    async fn invalid_utf8_token_file_preserves_utf8_error_source() {
        let (_file, provider) = file_provider(&[0xff]);

        let error = provider.resolve_token().await.unwrap_err();

        assert!(
            error
                .chain()
                .any(|source| source.downcast_ref::<str::Utf8Error>().is_some())
        );
    }

    #[tokio::test]
    async fn invalid_json_token_file_preserves_parse_error_source() {
        let (_file, provider) = json_provider("not-json", &["authToken"]);

        let error = provider.resolve_token().await.unwrap_err();

        assert!(error.to_string().contains("failed to extract key"));
        assert!(error.chain().nth(1).is_some());
    }
}

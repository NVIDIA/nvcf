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

use crate::secrets::secrets_config::Secrets;
use notify::{RecursiveMode, Watcher};
use serde_json::Error as SerdeError;
use std::fmt;
use std::io::Error as IoError;
use std::path::Path;
use std::sync::{Arc, RwLock};
use tokio::sync::mpsc;
use tracing;

use super::secrets_config::CredentialsData;

#[derive(Debug)]
pub enum ConfigError {
    Io(IoError),
    Json(SerdeError),
}

impl From<IoError> for ConfigError {
    fn from(err: IoError) -> Self {
        ConfigError::Io(err)
    }
}

impl From<SerdeError> for ConfigError {
    fn from(err: SerdeError) -> Self {
        ConfigError::Json(err)
    }
}

impl fmt::Display for ConfigError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            ConfigError::Io(e) => write!(f, "IO error: {}", e),
            ConfigError::Json(e) => write!(f, "JSON error: {}", e),
        }
    }
}

#[derive(Debug, Clone)]
pub struct SecretFileWatcher {
    config: Arc<RwLock<Secrets>>,
}

impl SecretFileWatcher {
    pub async fn new(path: &Path) -> anyhow::Result<Self> {
        let config = Arc::new(RwLock::new(
            Self::load_config_from_file(path).await.map_err(|e| {
                anyhow::anyhow!("Failed to load config from file {}: {}", path.display(), e)
            })?,
        ));
        let config_clone = config.clone();
        let path = path.to_path_buf();

        tokio::spawn(async move {
            Self::watch_config(path, config_clone).await;
        });

        Ok(Self { config })
    }

    async fn load_config_from_file(path: &Path) -> Result<Secrets, ConfigError> {
        let content = tokio::fs::read(path).await.map_err(ConfigError::Io)?;

        if content.is_empty() {
            return Err(ConfigError::Io(std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                "Secrets file is empty",
            )));
        }

        let config = serde_json::from_slice(&content).map_err(ConfigError::Json)?;

        Ok(config)
    }

    async fn watch_config(path: std::path::PathBuf, config: Arc<RwLock<Secrets>>) {
        let (tx, mut rx) = mpsc::channel(1);

        let mut watcher = notify::recommended_watcher(move |res| {
            if let Err(err) = tx.blocking_send(res) {
                tracing::warn!("failed to send file update event: {:?}", err);
            }
        })
        .unwrap();

        // Watch the parent directory to catch all file events
        if let Some(parent) = path.parent() {
            if let Err(e) = watcher.watch(parent, RecursiveMode::NonRecursive) {
                tracing::error!("Failed to watch directory: {:?}", e);
                return;
            }
        }

        let mut last_config_content: Option<Vec<u8>> = None;

        while let Some(event) = rx.recv().await {
            if let Ok(event) = event {
                if !matches!(
                    event.kind,
                    notify::EventKind::Create(_) | notify::EventKind::Modify(_)
                ) {
                    continue;
                }

                for event_path in event.paths {
                    // Only process events for our specific file
                    if event_path == path {
                        match tokio::fs::read(&event_path).await {
                            Ok(current_content) => {
                                // Compare with last known content to avoid processing duplicate events
                                if let Some(ref last_content) = last_config_content {
                                    if current_content == *last_content {
                                        tracing::debug!("File content unchanged, skipping reload");
                                        continue;
                                    }
                                }

                                tracing::info!("secret file content changed: {:?}", event_path);
                                match Self::load_config_from_file(&event_path).await {
                                    Ok(new_config) => {
                                        let mut w = config.write().unwrap();
                                        *w = new_config;
                                        last_config_content = Some(current_content);
                                    }
                                    Err(err) => {
                                        tracing::warn!("failed to load secret file: {:?}", err)
                                    }
                                }
                            }
                            Err(err) => tracing::warn!("failed to load secret file: {:?}", err),
                        }
                    }
                }
            }
        }
    }

    pub fn get_config(&self) -> CredentialsData {
        let guard = self.config.read().unwrap();
        guard.kv.clone()
    }

    pub fn get_nvcf_api_token(&self) -> Option<String> {
        let guard = self.config.read().unwrap();
        guard
            .nvcf_api_token
            .as_deref()
            .map(str::trim)
            .filter(|token| !token.is_empty())
            .map(str::to_owned)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::path::PathBuf;
    use tempfile::TempDir;

    struct TestSecretFile {
        path: PathBuf,
    }

    impl TestSecretFile {
        fn new() -> Self {
            let tmp_dir = TempDir::with_prefix_in("rs-autoscaler-test", "/tmp").unwrap();
            let path = tmp_dir.path().join("test_secrets.json");
            Self { path }
        }

        fn cleanup(&self) {
            if let Err(e) = std::fs::remove_dir_all(&self.path) {
                eprintln!("Failed to cleanup test directory {:?}: {}", self.path, e);
            }
        }

        fn write_valid_secrets(&self) {
            let valid_creds = r#"{
                "kv": {
                    "cassandra": {
                        "username": "test_username",
                        "password": "test_password"
                    }
                }
            }"#;
            fs::write(&self.path, valid_creds).unwrap();
        }

        fn write_invalid_json(&self) {
            let invalid_json = r#"{
                "kv": {
                    "cassandra": {
                        "username": "test_username",
                        "password": "test_password",
                        invalid_json
                    }
                }
            }"#;
            fs::write(&self.path, invalid_json).unwrap();
        }

        fn write_empty_file(&self) {
            fs::write(&self.path, "").unwrap();
        }

        fn write_malformed_secrets(&self) {
            let malformed_creds = r#"{
                "kv": {
                    "cassandra": {
                        "username": "test_username"
                    }
                }
            }"#;
            fs::write(&self.path, malformed_creds).unwrap();
        }
    }

    #[tokio::test]
    async fn test_secrets_file_watcher() {
        let test_file = TestSecretFile::new();
        std::fs::create_dir_all(test_file.path.parent().unwrap()).unwrap();
        test_file.write_valid_secrets();
        let secrets_file_watcher = SecretFileWatcher::new(&test_file.path).await.unwrap();
        let config = secrets_file_watcher.get_config();
        assert_eq!(config.cassandra.as_ref().unwrap().username, "test_username");
        test_file.cleanup();
    }

    #[tokio::test]
    async fn test_secrets_file_watcher_reads_nvcf_api_token() {
        let test_file = TestSecretFile::new();
        let creds_with_token = r#"{
            "nvcfApiToken": "  bearer-token  ",
            "kv": {
                "cassandra": {
                    "username": "test_username",
                    "password": "test_password"
                }
            }
        }"#;
        std::fs::create_dir_all(test_file.path.parent().unwrap()).unwrap();
        fs::write(&test_file.path, creds_with_token).unwrap();
        let secrets_file_watcher = SecretFileWatcher::new(&test_file.path).await.unwrap();

        assert_eq!(
            secrets_file_watcher.get_nvcf_api_token().as_deref(),
            Some("bearer-token")
        );
        test_file.cleanup();
    }

    #[tokio::test]
    async fn test_secrets_file_watcher_with_invalid_file() {
        let test_file = TestSecretFile::new();
        std::fs::create_dir_all(test_file.path.parent().unwrap()).unwrap();
        let result = SecretFileWatcher::new(&test_file.path).await;
        assert!(result.is_err());
        test_file.cleanup();
    }

    #[tokio::test]
    async fn test_secrets_file_watcher_with_empty_file() {
        let test_file = TestSecretFile::new();
        std::fs::create_dir_all(test_file.path.parent().unwrap()).unwrap();
        test_file.write_empty_file();
        let result = SecretFileWatcher::new(&test_file.path).await;
        assert!(result.is_err());
        test_file.cleanup();
    }

    #[tokio::test]
    async fn test_secrets_file_watcher_with_invalid_json() {
        let test_file = TestSecretFile::new();
        std::fs::create_dir_all(test_file.path.parent().unwrap()).unwrap();
        test_file.write_invalid_json();
        let result = SecretFileWatcher::new(&test_file.path).await;
        assert!(result.is_err());
        test_file.cleanup();
    }

    #[tokio::test]
    async fn test_secrets_file_watcher_with_malformed_secrets() {
        let test_file = TestSecretFile::new();
        std::fs::create_dir_all(test_file.path.parent().unwrap()).unwrap();
        test_file.write_malformed_secrets();
        let result = SecretFileWatcher::new(&test_file.path).await;
        assert!(result.is_err());
        test_file.cleanup();
    }

    #[tokio::test]
    async fn test_secrets_file_watcher_with_extra_fields() {
        let test_file = TestSecretFile::new();
        let creds_with_extra = r#"{
            "kv": {
                "cassandra": {
                    "username": "test_username",
                    "password": "test_password"
                },
                "extra_service": {
                    "key1": "value1",
                    "key2": 123,
                    "key3": {
                        "nested": "value"
                    }
                },
                "another_service": "some_value"
            }
        }"#;
        std::fs::create_dir_all(test_file.path.parent().unwrap()).unwrap();
        fs::write(&test_file.path, creds_with_extra).unwrap();
        let result = SecretFileWatcher::new(&test_file.path).await;
        assert!(result.is_err());
        test_file.cleanup();
    }
}

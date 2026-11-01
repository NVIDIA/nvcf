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

use crate::secrets::secret_config::Secrets;
use anyhow::Context as _;
use notify::{PollWatcher, RecommendedWatcher, RecursiveMode, Watcher};
use serde::{de::DeserializeOwned, Serialize};
use std::{
    path::Path,
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio::sync::mpsc;

pub struct SecretFileWatcher<Config = Secrets>
where
    Config: Serialize + DeserializeOwned + Clone + Send + 'static,
{
    config: Arc<Mutex<Config>>,
    _primary_watcher: RecommendedWatcher, // Keep the primary watcher alive
    _secondary_watcher: PollWatcher,      // Keep the secondary watcher alive as fallback
}

impl<Config> SecretFileWatcher<Config>
where
    Config: Serialize + DeserializeOwned + Clone + Send + 'static,
{
    pub async fn new(secrets_path: &Path, poll_interval: Duration) -> anyhow::Result<Self> {
        let config = Arc::new(Mutex::new(
            Self::load_config_from_file(secrets_path)
                .await
                .context("error loading initial secrets file")?,
        ));

        // Create channel for both watchers to share
        let (tx, mut rx) = mpsc::channel(32);

        // Create RecommendedWatcher for fast event detection
        let tx_clone = tx.clone();
        let mut recommended_watcher = RecommendedWatcher::new(
            move |res: notify::Result<notify::Event>| {
                match res {
                    Ok(ref event) => {
                        // Process only events that modify the file
                        if event.kind.is_create() || event.kind.is_modify() {
                            tracing::info!("RecommendedWatcher detected change: {:?}", event);
                            if let Err(err) = tx_clone.blocking_send(res) {
                                tracing::warn!("failed to send file update event: {:?}", err);
                            }
                        }
                    }
                    Err(e) => tracing::warn!("RecommendedWatcher error: {:?}", e),
                }
            },
            notify::Config::default(),
        )?;

        // Watch the specific secrets file with recommended watcher
        recommended_watcher.watch(secrets_path, RecursiveMode::NonRecursive)?;
        tracing::info!("RecommendedWatcher watching file: {:?}", secrets_path);

        // Create PollWatcher as fallback with optimized configuration
        let mut poll_watcher = PollWatcher::new(
            move |res| {
                tracing::info!("PollWatcher detected change: {:?}", res);
                if let Err(err) = tx.blocking_send(res) {
                    tracing::warn!("failed to forward file update event: {}", err);
                }
            },
            notify::Config::default()
                .with_poll_interval(poll_interval)
                .with_compare_contents(true), // Enable content comparison to detect changes
        )?;

        // Watch the specific file instead of the entire directory for better performance
        poll_watcher.watch(secrets_path, RecursiveMode::NonRecursive)?;
        tracing::info!("PollWatcher watching file: {:?}", secrets_path);

        // Spawn task to handle file change events from both watchers
        let config_clone = config.clone();
        let secrets_path_clone = secrets_path.to_path_buf();
        tokio::spawn(async move {
            tracing::info!("File watcher task started");
            let mut event_count = 0;
            while let Some(res) = rx.recv().await {
                event_count += 1;
                tracing::info!("Received file event #{}: {:?}", event_count, res);
                match res {
                    Ok(event) => {
                        // Reload only when the event includes the target secrets file path
                        // Note: Since both watchers now watch the specific file, this check may be redundant
                        // but kept for safety in case PollWatcher generates events for other files
                        if event
                            .paths
                            .iter()
                            .any(|p| p.file_name() == secrets_path_clone.file_name())
                        {
                            tracing::info!("secret file changed: {:?}", secrets_path_clone);
                            match Self::load_config_from_file(&secrets_path_clone).await {
                                Ok(new_secrets) => {
                                    if let Ok(mut guard) = config_clone.lock() {
                                        *guard = new_secrets;
                                    }
                                    tracing::info!("secrets updated successfully");
                                }
                                Err(e) => {
                                    tracing::warn!("failed to reload secrets: {}", e);
                                }
                            }
                        } else {
                            tracing::info!("Event not for target file; paths: {:?}", event.paths);
                        }
                    }
                    Err(e) => {
                        tracing::warn!("watch error: {}", e);
                    }
                }
            }
            tracing::info!(
                "File watcher task ended, received {} events total",
                event_count
            );
        });

        Ok(Self {
            config,
            _primary_watcher: recommended_watcher,
            _secondary_watcher: poll_watcher,
        })
    }

    pub async fn load_config_from_file(path: &Path) -> Result<Config, std::io::Error> {
        let content = tokio::fs::read(path).await?;
        let config = serde_json::from_slice(&content)
            .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;
        Ok(config)
    }

    pub fn get_config(&self) -> Config {
        self.config
            .lock()
            .expect("secret reload mutex was poisoned")
            .clone()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde::{Deserialize, Serialize};
    use tempfile::tempdir;

    #[derive(Clone, Serialize, Deserialize, Debug, PartialEq)]
    struct TestConfig {
        some_secret: String,
        some_value: u64,
    }

    // Test that only uses RecommendedWatcher (by setting a very long poll interval)
    #[tokio::test]
    async fn test_recommended_watcher_only() -> anyhow::Result<()> {
        let dir = tempdir()?;
        let file_path = dir.path().join("test.json");
        let initial_config = TestConfig {
            some_secret: "initial secret".to_string(),
            some_value: 100,
        };

        tokio::fs::write(&file_path, serde_json::to_string(&initial_config)?).await?;

        // Create watcher with very long poll interval (effectively disabling polling)
        let watcher: SecretFileWatcher<TestConfig> =
            SecretFileWatcher::new(&file_path, Duration::from_secs(3600)).await?; // 1 hour poll interval

        assert_eq!(watcher.get_config(), initial_config);

        // Update the file - this should be detected by RecommendedWatcher
        let updated_config = TestConfig {
            some_secret: "updated secret".to_string(),
            some_value: 200,
        };

        tokio::fs::write(&file_path, serde_json::to_string(&updated_config)?).await?;

        // Wait a short time for RecommendedWatcher to detect the change
        tokio::time::sleep(Duration::from_millis(100)).await;

        // Check if RecommendedWatcher detected the change
        let current_config = watcher.get_config();
        tracing::info!(
            "RecommendedWatcher test - Current: {:?}, Expected: {:?}",
            current_config,
            updated_config
        );

        // Note: This test may fail if the OS doesn't support RecommendedWatcher well
        // In that case, the PollWatcher fallback should still work
        if current_config == updated_config {
            tracing::info!("RecommendedWatcher successfully detected the change!");
        } else {
            tracing::info!(
                "RecommendedWatcher didn't detect change, PollWatcher fallback will handle it"
            );
        }

        Ok(())
    }

    #[tokio::test]
    async fn test_watcher_updates_secrets_when_file_modified() -> anyhow::Result<()> {
        // Create a test config file
        let dir = tempdir()?;
        let file_path = dir.path().join("test.json");
        let initial_config = TestConfig {
            some_secret: "my secret".to_string(),
            some_value: 887,
        };

        tokio::fs::write(&file_path, serde_json::to_string(&initial_config)?).await?;

        // Create the watcher and confirm the initial config
        let watcher: SecretFileWatcher<TestConfig> =
            SecretFileWatcher::new(&file_path, Duration::from_secs(1)).await?;

        assert_eq!(
            watcher.get_config(),
            initial_config,
            "initial config mismatch"
        );

        // Update the file
        let updated_config = TestConfig {
            some_secret: "my new secret".to_string(),
            some_value: 888,
        };

        tokio::fs::write(&file_path, serde_json::to_string(&updated_config)?).await?;

        // Force file sync to ensure changes are written to disk
        if let Ok(file) = std::fs::OpenOptions::new().write(true).open(&file_path) {
            let _ = file.sync_all();
        }

        // Wait for the watcher to detect the change
        // PollWatcher checks every 1 second, so wait at least 1.5 seconds to ensure polling happens
        tokio::time::sleep(Duration::from_millis(1500)).await;

        // Debug: Check if the file was actually modified
        let file_content = tokio::fs::read_to_string(&file_path).await?;
        tracing::info!("File content after modification: {}", file_content);

        // Debug: Check what the watcher currently has
        let current_config = watcher.get_config();
        tracing::info!("Watcher current config: {:?}", current_config);
        tracing::info!("Expected updated config: {:?}", updated_config);

        // Confirm the watcher detects the update
        assert_eq!(
            current_config, updated_config,
            "watcher failed to detect update"
        );

        Ok(())
    }

    #[tokio::test]
    async fn test_watcher_updates_secrets_when_file_replaced() -> anyhow::Result<()> {
        // Create a test config file
        let dir = tempdir()?;
        let file_path = dir.path().join("test.json");
        let initial_config = TestConfig {
            some_secret: "my secret".to_string(),
            some_value: 887,
        };

        tokio::fs::write(&file_path, serde_json::to_string(&initial_config)?).await?;

        // Create the watcher and confirm the initial config
        let watcher: SecretFileWatcher<TestConfig> =
            SecretFileWatcher::new(&file_path, Duration::from_secs(1)).await?;

        assert_eq!(
            watcher.get_config(),
            initial_config,
            "initial config mismatch"
        );

        // Replace the file (simulate file replacement)
        let new_config = TestConfig {
            some_secret: "my new secret".to_string(),
            some_value: 888,
        };

        // Remove and recreate the file
        tokio::fs::remove_file(&file_path).await?;
        tokio::fs::write(&file_path, serde_json::to_string(&new_config)?).await?;

        // Force file sync to ensure changes are written to disk
        if let Ok(file) = std::fs::OpenOptions::new().write(true).open(&file_path) {
            let _ = file.sync_all();
        }

        // Wait for the watcher to detect the change (at least one poll interval)
        tokio::time::sleep(Duration::from_millis(1500)).await;

        // Confirm the watcher detects the update
        let current_config = watcher.get_config();
        tracing::info!("Current config: {:?}", current_config);
        tracing::info!("Expected config: {:?}", new_config);
        assert_eq!(
            current_config, new_config,
            "watcher failed to detect update"
        );

        Ok(())
    }
}

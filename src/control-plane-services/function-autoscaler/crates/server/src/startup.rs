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

//! Wait for dependencies (Cassandra, TimeseriesDb) at startup with exponential backoff.

use crate::cassandra::cassandra_service::CassandraServiceManager;
use crate::cassandra::cassandra_settings::CassandraSettings;
use crate::secrets::secrets_file_watcher::SecretFileWatcher;
use crate::timeseries_db::timeseries_db_client::{CredentialProvider, TimeseriesDbClient};
use crate::timeseries_db::TimeseriesDbSettings;
use backon::{ExponentialBuilder, Retryable};
use chrono::{Duration as ChronoDuration, Utc};
use std::sync::Arc;
use std::time::Duration;

fn startup_backoff() -> ExponentialBuilder {
    ExponentialBuilder::default()
        .with_min_delay(Duration::from_secs(1))
        .with_max_delay(Duration::from_secs(60))
        .with_max_times(usize::MAX)
}

/// Waits for Cassandra to be reachable, then returns the manager. Uses exponential backoff.
pub async fn wait_for_cassandra(
    config: &CassandraSettings,
    secrets: Arc<SecretFileWatcher>,
) -> Arc<CassandraServiceManager> {
    (|| {
        let config = config.clone();
        let secrets = Arc::clone(&secrets);
        async move {
            CassandraServiceManager::new(&config, secrets)
                .await
                .map(Arc::new)
        }
    })
    .retry(startup_backoff())
    .notify(|err, delay| {
        tracing::warn!(
            dependency = "cassandra",
            error = %err,
            retry_in_secs = delay.as_secs(),
            "dependency not ready, retrying with backoff"
        );
    })
    .await
    .expect("cassandra startup retry exhausted — unreachable with usize::MAX retries")
}

/// Builds the TimeseriesDb client, then waits until a probe query succeeds. Uses exponential backoff.
pub async fn wait_for_timeseries_db(
    config: &TimeseriesDbSettings,
    credential_provider: Option<Arc<dyn CredentialProvider + Send + Sync>>,
) -> Result<Arc<TimeseriesDbClient>, anyhow::Error> {
    let client = TimeseriesDbClient::new(config, credential_provider)
        .map(Arc::new)
        .map_err(|e| anyhow::anyhow!("Failed to create TimeseriesDbClient: {}", e))?;
    (|| {
        let client = Arc::clone(&client);
        async move {
            let end = Utc::now();
            let start = end - ChronoDuration::seconds(60);
            client
                .query_range("1", start, end, Duration::from_secs(60))
                .await
                .map(|_| ())
        }
    })
    .retry(startup_backoff())
    .notify(|err, delay| {
        tracing::warn!(
            dependency = "timeseries_db",
            error = %err,
            retry_in_secs = delay.as_secs(),
            "dependency not ready, retrying with backoff"
        );
    })
    .await
    .expect("timeseries_db startup retry exhausted — unreachable with usize::MAX retries");
    Ok(client)
}

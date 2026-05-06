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
use crate::models::{
    ActiveFunction, ActiveFunctionDetails, DistributedLock, DistributedLockResult, NodeHealth,
};
use crate::secrets::secrets_config::CassandraSslCertificates;
use anyhow::Result;
use async_trait::async_trait;
use base64::Engine;
use futures::TryStreamExt as _;
use openssl::ssl::{SslContextBuilder, SslMethod, SslVerifyMode};
use scylla::client::session::Session;
use scylla::client::{
    execution_profile::ExecutionProfile, session_builder::SessionBuilder, Compression, PoolSize,
};

use scylla::policies::retry::DefaultRetryPolicy;
use scylla::policies::speculative_execution::SimpleSpeculativeExecutionPolicy;
use scylla::statement::{Consistency, SerialConsistency, Statement};
use std::num::NonZero;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tracing;
use uuid::Uuid;

use super::cassandra_settings::CassandraSettings;
use super::statements::*;
use crate::secrets::secrets_file_watcher::SecretFileWatcher;
pub const CASSANDRA_TOKEN_RANGE: [i64; 2] = [i64::MIN, i64::MAX];

/// Execute any async operation with automatic timing and tracing
async fn with_cassandra_timing<T, F, Fut>(operation_name: &str, operation: F) -> T
where
    F: FnOnce() -> Fut,
    Fut: std::future::Future<Output = T>,
{
    let start = std::time::Instant::now();
    let result = operation().await;
    let duration = start.elapsed();

    tracing::trace!(
        cassandra.operation = operation_name,
        cassandra.duration_ms = duration.as_millis(),
        "Cassandra operation completed"
    );

    result
}

/// Run `f` concurrently over `items` in batches of `chunk_size`, collecting all errors.
/// Equivalent to: for each chunk, join_all the futures, fail fast on first error.
async fn execute_chunked<T, U, F, Fut>(items: &[T], chunk_size: usize, f: F) -> Result<()>
where
    F: Fn(&T) -> Fut,
    Fut: std::future::Future<Output = Result<U>>,
{
    for chunk in items.chunks(chunk_size) {
        futures::future::join_all(chunk.iter().map(&f))
            .await
            .into_iter()
            .collect::<Result<Vec<_>>>()?;
    }
    Ok(())
}

pub struct CassandraServiceManager {
    config: CassandraSettings,
    secrets_watcher: Arc<SecretFileWatcher>,
    current_session: Arc<Mutex<Option<Arc<Session>>>>,
    health_check_cache: Mutex<Option<HealthCheckResult>>,
    health_check_cache_ttl: Duration,
}

impl CassandraServiceManager {
    pub async fn new(
        config: &CassandraSettings,
        secrets_watcher: Arc<SecretFileWatcher>,
    ) -> Result<Self> {
        let manager = Self {
            config: config.clone(),
            secrets_watcher,
            current_session: Arc::new(Mutex::new(None)),
            health_check_cache: Mutex::new(None),
            health_check_cache_ttl: Duration::from_secs(30),
        };

        // Create initial service
        manager.initialize_service().await?;

        Ok(manager)
    }

    async fn initialize_service(&self) -> Result<()> {
        let secrets = self.secrets_watcher.get_config();
        let cassandra_creds = secrets
            .cassandra
            .ok_or_else(|| anyhow::anyhow!("Cassandra credentials not found in secrets"))?;

        // Only require SSL certs if SSL is enabled
        let ssl_certs = if self.config.ssl.enabled {
            Some(secrets.cassandra_ssl.ok_or_else(|| {
                anyhow::anyhow!("Cassandra SSL certificates not found in secrets")
            })?)
        } else {
            None
        };

        let new_session = self
            .build_session(&cassandra_creds, ssl_certs.as_ref())
            .await?;

        let mut current = self.current_session.lock().unwrap();
        *current = Some(Arc::new(new_session));

        Ok(())
    }

    async fn build_session(
        &self,
        cassandra_creds: &crate::secrets::secrets_config::CassandraCredentials,
        ssl_certs: Option<&CassandraSslCertificates>,
    ) -> Result<Session> {
        let mut session_builder = SessionBuilder::new();

        // Add all contact points from the comma-separated list
        for contact_point in self.config.contact_points.split(',') {
            let contact_point = contact_point.trim();
            session_builder =
                session_builder.known_node(format!("{}:{}", contact_point, self.config.port));
        }

        let compression = match self.config.compression.as_str() {
            "lz4" => Some(Compression::Lz4),
            "snappy" => Some(Compression::Snappy),
            _ => None,
        };

        // Configure TLS only if SSL is enabled and certs are provided
        let tls_context = if self.config.ssl.enabled {
            if let Some(certs) = ssl_certs {
                Some(Self::create_ssl_context(certs)?.build())
            } else {
                return Err(anyhow::anyhow!(
                    "SSL is enabled but no certificates provided"
                ));
            }
        } else {
            None
        };

        session_builder
            .user(&cassandra_creds.username, &cassandra_creds.password)
            .fetch_schema_metadata(false)
            .compression(compression)
            .connection_timeout(self.config.connection_timeout)
            .tls_context(tls_context)
            .default_execution_profile_handle(
                ExecutionProfile::builder()
                    .consistency(if self.config.is_development {
                        Consistency::One
                    } else {
                        Consistency::LocalQuorum
                    })
                    .serial_consistency(Some(SerialConsistency::LocalSerial))
                    .request_timeout(Some(self.config.execution_profile.request_timeout))
                    .speculative_execution_policy(Some(Arc::new(
                        SimpleSpeculativeExecutionPolicy {
                            max_retry_count: self
                                .config
                                .execution_profile
                                .speculative_execution_policy
                                .max_retry_count
                                as usize,
                            retry_interval: self
                                .config
                                .execution_profile
                                .speculative_execution_policy
                                .retry_interval,
                        },
                    )))
                    .retry_policy(Arc::new(DefaultRetryPolicy::new()))
                    .build()
                    .into_handle(),
            )
            .use_keyspace(&self.config.keyspace, true)
            .pool_size(PoolSize::PerHost(
                NonZero::new(self.config.pool.local_size).unwrap(),
            ))
            .build()
            .await
            .map_err(|e| anyhow::anyhow!("Failed to build Cassandra session: {}", e))
    }

    fn create_ssl_context(ssl_certs: &CassandraSslCertificates) -> Result<SslContextBuilder> {
        use openssl::pkey::PKey;
        use openssl::x509::X509;

        tracing::info!("Creating SSL context from memory");

        // Decode certificates from base64
        let tls_cert_content =
            base64::engine::general_purpose::STANDARD.decode(&ssl_certs.tls_cert)?;
        let app_cert_content =
            base64::engine::general_purpose::STANDARD.decode(&ssl_certs.app_cert)?;
        let app_key_content =
            base64::engine::general_purpose::STANDARD.decode(&ssl_certs.app_key)?;

        // Parse certificates and key
        let ca_cert = X509::from_pem(&tls_cert_content)?;
        let client_cert = X509::from_pem(&app_cert_content)?;
        let private_key = PKey::private_key_from_pem(&app_key_content)?;

        // Create SSL context
        let mut context_builder = SslContextBuilder::new(SslMethod::tls())?;

        // Add CA certificate to the certificate store
        let cert_store = context_builder.cert_store_mut();
        cert_store.add_cert(ca_cert)?;

        context_builder.set_certificate(&client_cert)?;
        context_builder.set_private_key(&private_key)?;
        context_builder.set_verify(SslVerifyMode::NONE);

        Ok(context_builder)
    }

    #[tracing::instrument(skip(self))]
    pub async fn get_session(&self) -> Result<Arc<Session>> {
        let session_guard = self.current_session.lock().unwrap();
        session_guard
            .as_ref()
            .ok_or_else(|| anyhow::anyhow!("CassandraSession not initialized"))
            .map(Arc::clone)
    }

    /// Start background health monitoring that runs every 30 seconds
    pub fn start_health_monitoring(self: Arc<Self>) {
        let manager = Arc::clone(&self);
        tokio::spawn(async move {
            let mut interval = tokio::time::interval(Duration::from_secs(30));
            interval.tick().await; // Skip first immediate tick

            loop {
                interval.tick().await;
                let current_time = SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_secs();

                // Perform health check
                if let Ok(session) = manager.get_session().await {
                    let query_result = with_cassandra_timing("health_check", || async {
                        let mut statement = Statement::new("SELECT now() FROM system.local;");
                        statement.set_is_idempotent(true);
                        session.query_unpaged(statement, ()).await
                    })
                    .await;

                    match query_result {
                        Ok(_) => {
                            let result = HealthCheckResult {
                                is_healthy: true,
                                message: Some("Cassandra session is healthy".to_string()),
                                last_updated: current_time,
                            };
                            crate::metrics::record_cassandra_health_status(true);
                            *manager.health_check_cache.lock().unwrap() = Some(result.clone());
                        }
                        Err(e) => {
                            let result = HealthCheckResult {
                                is_healthy: false,
                                message: Some(format!("Query failed: {}", e)),
                                last_updated: current_time,
                            };
                            crate::metrics::record_cassandra_health_status(false);
                            *manager.health_check_cache.lock().unwrap() = Some(result.clone());
                        }
                    }
                } else {
                    tracing::error!("Failed to get Cassandra session for health check. Attempting to recreate session...");
                    let result = HealthCheckResult {
                        is_healthy: false,
                        message: Some(
                            "Cassandra session is not healthy because session is not initialized"
                                .to_string(),
                        ),
                        last_updated: current_time,
                    };
                    crate::metrics::record_cassandra_health_status(false);
                    *manager.health_check_cache.lock().unwrap() = Some(result.clone());
                    manager.attempt_service_recreation().await;
                }
            }
        });
    }

    async fn attempt_service_recreation(&self) {
        let secrets = self.secrets_watcher.get_config();
        let cassandra_creds = match secrets.cassandra {
            Some(creds) => creds,
            None => {
                tracing::error!(
                    "Cannot recreate Cassandra session: credentials not found in secrets"
                );
                return;
            }
        };

        // Only require SSL certs if SSL is enabled
        let ssl_certs = if self.config.ssl.enabled {
            match secrets.cassandra_ssl {
                Some(certs) => Some(certs),
                None => {
                    tracing::error!(
                        "Cannot recreate Cassandra session: SSL certificates not found in secrets"
                    );
                    return;
                }
            }
        } else {
            None
        };

        tracing::info!("Recreating Cassandra session with updated credentials");

        match self
            .build_session(&cassandra_creds, ssl_certs.as_ref())
            .await
        {
            Ok(new_session) => {
                let mut current = self.current_session.lock().unwrap();
                *current = Some(Arc::new(new_session));
                tracing::info!("Successfully recreated Cassandra session");
            }
            Err(e) => {
                tracing::error!("Failed to recreate Cassandra session: {}", e);
            }
        }
    }

    #[tracing::instrument(skip(self, function), fields(function_id = %function.function_id, function_version_id = %function.function_version_id))]
    pub async fn insert_to_active_functions(
        &self,
        function: &ActiveFunctionDetails,
        table: ActiveFunctionTable,
    ) -> Result<()> {
        let session = self.get_session().await?;

        let stmt = match table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_stmt_insert_to_recently_invoked_functions(
                    &self.config.keyspace,
                    self.config.recently_invoked_ttl_seconds,
                )
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_stmt_insert_to_running_functions_without_invocations(
                    &self.config.keyspace,
                    self.config.recently_invoked_ttl_seconds,
                )
            }
        };

        let mut prepared_statement = session.prepare(stmt).await?;
        prepared_statement.set_is_idempotent(true);
        let nca_id = function.nca_id_or_nil();
        session
            .execute_unpaged(
                &prepared_statement,
                (
                    &function.function_id,
                    &function.function_version_id,
                    &nca_id,
                    &function.last_updated_at,
                ),
            )
            .await?;
        Ok(())
    }

    // Not instrumented: return value is Vec<ActiveFunction> and would be captured in the span (large debug output).
    pub async fn get_active_functions_with_token_range(
        &self,
        token_range: &[i64],
        page_size: i32,
        table: ActiveFunctionTable,
    ) -> Result<Vec<ActiveFunction>> {
        let session = self.get_session().await?;
        let stmt = match table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_select_recently_invoked_functions_in_token_range_stmt(&self.config.keyspace)
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_select_running_functions_without_invocations_in_token_range_stmt(
                    &self.config.keyspace,
                )
            }
        };

        with_cassandra_timing("get_active_functions_with_token_range", || async {
            let mut prepared_statement = session.prepare(stmt).await?;
            prepared_statement.set_page_size(page_size);
            // Use LOCAL_QUORUM to match the QUORUM write consistency used in add_new_active_functions_batch.
            // LOCAL_ONE (execution profile default) can read from a replica that hasn't received a
            // recent QUORUM write, causing a second pod to see a function as new and overwrite the
            // history row with num_workers=-1 even after a prior pod already wrote it.
            prepared_statement.set_consistency(Consistency::LocalQuorum);
            let mut results = Vec::new();
            let token_range_min = token_range[0];
            let token_range_max = token_range[1];

            // Use execute_iter with proper page size for pagination
            // nca_id is optional (nullable) for backwards compatibility with existing rows
            let mut iter = session
                .execute_iter(prepared_statement, (token_range_min, token_range_max))
                .await?
                .rows_stream::<(Uuid, Uuid, Option<String>)>()?;

            while let Some((function_id, function_version_id, nca_id_opt)) = iter.try_next().await?
            {
                let function = ActiveFunction {
                    function_id,
                    function_version_id,
                    nca_id: nca_id_opt.unwrap_or_default(),
                };
                results.push(function);
            }

            Ok(results)
        })
        .await
    }

    #[tracing::instrument(skip(self))]
    pub async fn get_active_function_history_by_id(
        &self,
        function_id: &Uuid,
        function_version_id: &Uuid,
        table: ActiveFunctionTable,
    ) -> Result<Option<ActiveFunctionDetails>> {
        let session = self.get_session().await?;
        let stmt = match table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_select_recently_invoked_function_history_by_id_stmt(&self.config.keyspace)
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_select_running_function_without_invocations_history_by_id_stmt(
                    &self.config.keyspace,
                )
            }
        };
        with_cassandra_timing("get_active_function_history_by_id", || async {
            let mut prepared_statement = session.prepare(stmt).await?;
            prepared_statement.set_tracing(true);
            let mut iter = session
                .execute_iter(prepared_statement, (function_id, function_version_id))
                .await?
                .rows_stream::<ActiveFunctionDetails>()?;
            Ok(iter.try_next().await?)
        })
        .await
    }

    #[tracing::instrument(skip(self))]
    pub async fn add_new_active_function(
        &self,
        function: &ActiveFunctionDetails,
        table: ActiveFunctionTable,
    ) -> Result<()> {
        let session = self.get_session().await?;
        let stmt_active_function = match table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_stmt_insert_to_recently_invoked_functions(
                    &self.config.keyspace,
                    self.config.recently_invoked_ttl_seconds,
                )
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_stmt_insert_to_running_functions_without_invocations(
                    &self.config.keyspace,
                    self.config.recently_invoked_ttl_seconds,
                )
            }
        };
        let stmt_active_function_history = match table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_insert_recently_invoked_functions_history_pk_stmt(&self.config.keyspace)
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_insert_running_functions_without_invocations_history_pk_stmt(
                    &self.config.keyspace,
                )
            }
        };
        let mut batch = scylla::statement::batch::Batch::default();
        batch.set_consistency(scylla::statement::Consistency::Quorum);
        batch.append_statement(Statement::new(stmt_active_function));
        batch.append_statement(Statement::new(stmt_active_function_history));
        let nca_id = function.nca_id_or_nil();
        let values = (
            (
                &function.function_id,
                &function.function_version_id,
                &nca_id,
                function.last_updated_at,
            ),
            (
                &function.function_id,
                &function.function_version_id,
                &nca_id,
                &function.num_workers.unwrap_or(-1),
            ),
        );
        match session.batch(&batch, values).await {
            Ok(_) => {
                tracing::debug!(
                    "Successfully inserted function {}:{} to Cassandra",
                    function.function_id,
                    function.function_version_id
                );
            }
            Err(e) => {
                tracing::error!(
                    "Failed to insert function {}:{} to Cassandra: {}",
                    function.function_id,
                    function.function_version_id,
                    e
                );
                return Err(e.into());
            }
        }
        Ok(())
    }

    /// Upserts a function into recently_invoked_functions with a fresh TTL.
    /// Called by the scaling loop when desired_instance_count > 0 to keep the
    /// function alive in the active set without touching the history table.
    #[tracing::instrument(skip(self))]
    pub async fn refresh_active_function_ttl(&self, function: &ActiveFunction) -> Result<()> {
        let session = self.get_session().await?;
        let stmt = get_stmt_insert_to_recently_invoked_functions(
            &self.config.keyspace,
            self.config.recently_invoked_ttl_seconds,
        );
        with_cassandra_timing("refresh_active_function_ttl", || async {
            let mut prepared = session.prepare(stmt).await?;
            prepared.set_consistency(scylla::statement::Consistency::LocalQuorum);
            session
                .execute_unpaged(
                    &prepared,
                    (
                        &function.function_id,
                        &function.function_version_id,
                        &function.nca_id,
                        chrono::Utc::now(),
                    ),
                )
                .await?;
            Ok(())
        })
        .await
    }

    #[tracing::instrument(skip(self, functions), fields(functions_len = functions.len()))]
    pub async fn add_new_active_functions_batch(
        &self,
        functions: &[ActiveFunctionDetails],
        table: ActiveFunctionTable,
    ) -> Result<()> {
        if functions.is_empty() {
            return Ok(());
        }

        let session = self.get_session().await?;
        let stmt_active_function = match table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_stmt_insert_to_recently_invoked_functions(
                    &self.config.keyspace,
                    self.config.recently_invoked_ttl_seconds,
                )
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_stmt_insert_to_running_functions_without_invocations(
                    &self.config.keyspace,
                    self.config.recently_invoked_ttl_seconds,
                )
            }
        };
        let stmt_active_function_history = match table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_insert_recently_invoked_functions_history_pk_stmt(&self.config.keyspace)
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_insert_running_functions_without_invocations_history_pk_stmt(
                    &self.config.keyspace,
                )
            }
        };

        let prepared_active_function = session.prepare(stmt_active_function).await?;
        let prepared_active_function_history =
            session.prepare(stmt_active_function_history).await?;

        execute_chunked(functions, 200, |function| {
            let session = session.clone();
            let prepared_active_function = prepared_active_function.clone();
            let prepared_active_function_history = prepared_active_function_history.clone();
            let function_id = function.function_id;
            let function_version_id = function.function_version_id;
            let nca_id = function.nca_id_or_nil();
            let last_updated_at = function.last_updated_at;
            let num_workers = function.num_workers.unwrap_or(-1);
            async move {
                let mut batch = scylla::statement::batch::Batch::default();
                batch.set_consistency(scylla::statement::Consistency::Quorum);
                batch.append_statement(prepared_active_function);
                batch.append_statement(prepared_active_function_history);
                let values = (
                    (&function_id, &function_version_id, &nca_id, last_updated_at),
                    (&function_id, &function_version_id, &nca_id, &num_workers),
                );
                session.batch(&batch, values).await.map_err(|e| {
                    tracing::error!(
                        "Failed to insert function {}:{} to Cassandra: {}",
                        function_id,
                        function_version_id,
                        e
                    );
                    anyhow::Error::from(e)
                })
            }
        })
        .await?;

        Ok(())
    }

    #[tracing::instrument(skip(self))]
    pub async fn delete_active_function(
        &self,
        function_id: &Uuid,
        function_version_id: &Uuid,
        table: ActiveFunctionTable,
    ) -> Result<()> {
        let session = self.get_session().await?;
        let stmt_active_function = match table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_delete_recently_invoked_function_stmt(&self.config.keyspace)
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_delete_running_function_without_invocations_stmt(&self.config.keyspace)
            }
        };
        let stmt_active_function_history = match table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_delete_recently_invoked_function_history_pk_stmt(&self.config.keyspace)
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_delete_running_function_without_invocations_history_pk_stmt(
                    &self.config.keyspace,
                )
            }
        };
        let mut batch = scylla::statement::batch::Batch::default();
        batch.set_consistency(scylla::statement::Consistency::Quorum);

        batch.append_statement(Statement::new(stmt_active_function));
        batch.append_statement(Statement::new(stmt_active_function_history));
        let values: ((&Uuid, &Uuid), (&Uuid, &Uuid)) = (
            (function_id, function_version_id),
            (function_id, function_version_id),
        );
        match session.batch(&batch, values).await {
            Ok(_) => {
                tracing::debug!(
                    "Successfully deleted function {}:{} from Cassandra",
                    function_id,
                    function_version_id
                );
            }
            Err(e) => {
                tracing::error!(
                    "Failed to delete function {}:{} from Cassandra: {}",
                    function_id,
                    function_version_id,
                    e
                );
                return Err(e.into());
            }
        }
        Ok(())
    }

    #[tracing::instrument(skip(self, functions), fields(functions_len = functions.len()))]
    pub async fn transition_functions_between_tables_batch(
        &self,
        functions: &[ActiveFunctionDetails],
        from_table: ActiveFunctionTable,
        to_table: ActiveFunctionTable,
    ) -> Result<()> {
        if functions.is_empty() {
            return Ok(());
        }

        let session = self.get_session().await?;

        // Prepare delete statements for source table
        let stmt_delete_active = match from_table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_delete_recently_invoked_function_stmt(&self.config.keyspace)
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_delete_running_function_without_invocations_stmt(&self.config.keyspace)
            }
        };
        let stmt_delete_history = match from_table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_delete_recently_invoked_function_history_pk_stmt(&self.config.keyspace)
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_delete_running_function_without_invocations_history_pk_stmt(
                    &self.config.keyspace,
                )
            }
        };

        // Prepare insert statements for destination table
        let stmt_insert_active = match to_table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_stmt_insert_to_recently_invoked_functions(
                    &self.config.keyspace,
                    self.config.recently_invoked_ttl_seconds,
                )
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_stmt_insert_to_running_functions_without_invocations(
                    &self.config.keyspace,
                    self.config.recently_invoked_ttl_seconds,
                )
            }
        };
        let stmt_insert_history = match to_table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_insert_recently_invoked_functions_history_pk_stmt(&self.config.keyspace)
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_insert_running_functions_without_invocations_history_pk_stmt(
                    &self.config.keyspace,
                )
            }
        };

        let prepared_delete_active = session.prepare(stmt_delete_active).await?;
        let prepared_delete_history = session.prepare(stmt_delete_history).await?;
        let prepared_insert_active = session.prepare(stmt_insert_active).await?;
        let prepared_insert_history = session.prepare(stmt_insert_history).await?;

        execute_chunked(functions, 200, |function| {
            let session = session.clone();
            let prepared_delete_active = prepared_delete_active.clone();
            let prepared_delete_history = prepared_delete_history.clone();
            let prepared_insert_active = prepared_insert_active.clone();
            let prepared_insert_history = prepared_insert_history.clone();
            let function_id = function.function_id;
            let function_version_id = function.function_version_id;
            let nca_id = function.nca_id_or_nil();
            let last_updated_at = function.last_updated_at;
            let num_workers = function.num_workers.unwrap_or(-1);
            async move {
                let mut batch = scylla::statement::batch::Batch::default();
                batch.set_consistency(scylla::statement::Consistency::Quorum);
                batch.append_statement(prepared_delete_active);
                batch.append_statement(prepared_delete_history);
                batch.append_statement(prepared_insert_active);
                batch.append_statement(prepared_insert_history);
                let values = (
                    (&function_id, &function_version_id),
                    (&function_id, &function_version_id),
                    (&function_id, &function_version_id, &nca_id, last_updated_at),
                    (&function_id, &function_version_id, &nca_id, &num_workers),
                );
                session.batch(&batch, values).await.map_err(|e| {
                    tracing::error!(
                        "Failed to transition function {}:{}: {}",
                        function_id,
                        function_version_id,
                        e
                    );
                    anyhow::Error::from(e)
                })
            }
        })
        .await?;

        Ok(())
    }

    #[tracing::instrument(skip(self))]
    pub async fn insert_to_active_function_history_prediction_row(
        &self,
        function: &ActiveFunctionDetails,
        table: ActiveFunctionTable,
    ) -> Result<()> {
        let session = self.get_session().await?;
        let stmt = match table {
            ActiveFunctionTable::RecentlyInvokedFunctions => {
                get_stmt_str_insert_to_recently_invoked_functions_history_prediction_row(
                    &self.config.keyspace,
                    self.config.history_prediction_ttl_seconds,
                )
            }
            ActiveFunctionTable::RunningFunctionsWithoutInvocations => {
                get_stmt_str_insert_to_running_functions_without_invocations_history_prediction_row(
                    &self.config.keyspace,
                    self.config.history_prediction_ttl_seconds,
                )
            }
        };
        let error_code = function.last_predicted_error_code.clone();
        let nca_id = function.nca_id_or_nil();

        let mut prepared_statement = session.prepare(stmt).await?;
        prepared_statement.set_is_idempotent(true);
        match session
            .execute_unpaged(
                &prepared_statement,
                (
                    &function.function_id,
                    &function.function_version_id,
                    &nca_id,
                    &function.num_workers,
                    &function.last_predicted_desired_instance_count.unwrap_or(0),
                    &error_code,
                    &function.last_updated_at,
                ),
            )
            .await
        {
            Ok(_) => {
                tracing::debug!(
                    "Successfully inserted prediction row for function {}:{} to Cassandra",
                    function.function_id,
                    function.function_version_id
                );
            }
            Err(e) => {
                tracing::error!(
                    "Failed to insert prediction row for function {}:{} to Cassandra: {}",
                    function.function_id,
                    function.function_version_id,
                    e
                );
                return Err(e.into());
            }
        }
        Ok(())
    }

    // Returns true if the lock was acquired, false if it was already held by another node
    #[tracing::instrument(skip(self))]
    pub async fn put_lock(&self, lock: &DistributedLock, ttl_seconds: i32) -> Result<bool> {
        let session = self.get_session().await?;

        let result = with_cassandra_timing("put_lock", || async {
            let mut prepared = session
                .prepare(get_stmt_insert_to_locks(&self.config.keyspace))
                .await?;
            prepared.set_consistency(Consistency::One);
            prepared.set_serial_consistency(Some(SerialConsistency::Serial));
            prepared.set_is_idempotent(true);
            prepared.set_tracing(true);
            session
                .execute_unpaged(
                    &prepared,
                    (
                        &lock.lock_name,
                        &lock.node_id,
                        lock.acquired_at,
                        ttl_seconds,
                    ),
                )
                .await
        })
        .await?;

        // Log tracing_id if available
        if let Some(trace_id) = result.tracing_id() {
            tracing::info!(
                cassandra.tracing_id = trace_id.to_string(),
                "Cassandra tracing enabled"
            );
        }
        let rows = result.into_rows_result()?;

        let column_specs = rows.column_specs();
        if column_specs.get_by_name("lock_name").is_some() {
            if let Some(row) = rows.rows::<DistributedLockResult>()?.next() {
                return Ok(row.unwrap().applied);
            }
        }

        if let Some(row) = rows.rows::<(bool,)>()?.next() {
            return Ok(row.unwrap().0);
        }

        Ok(false)
    }

    #[tracing::instrument(skip(self))]
    pub async fn get_lock(&self, lock_name: &str) -> Result<Option<DistributedLock>> {
        let session = self.get_session().await?;

        with_cassandra_timing("get_lock", || async {
            let mut prepared_statement = session
                .prepare(get_select_locks_stmt(&self.config.keyspace))
                .await?;
            prepared_statement.set_is_idempotent(true);

            let mut iter = session
                .execute_iter(prepared_statement, (lock_name,))
                .await?
                .rows_stream::<DistributedLock>()?;
            Ok(iter.try_next().await?)
        })
        .await
    }

    /// LWT TTL refresh — only applies if this node still owns the lock.
    /// Returns Ok(true) if the TTL was refreshed, Ok(false) if another node now owns it.
    #[tracing::instrument(skip(self))]
    pub async fn refresh_lock_ttl(
        &self,
        lock_name: &str,
        node_id: &str,
        ttl_seconds: i32,
    ) -> Result<bool> {
        let session = self.get_session().await?;
        with_cassandra_timing("refresh_lock_ttl", || async {
            let mut prepared = session
                .prepare(get_stmt_refresh_lock(&self.config.keyspace))
                .await?;
            prepared.set_consistency(Consistency::LocalQuorum);
            prepared.set_serial_consistency(Some(SerialConsistency::Serial));
            prepared.set_is_idempotent(false);
            let result = session
                .execute_unpaged(&prepared, (ttl_seconds, node_id, lock_name, node_id))
                .await?;
            let rows = result.into_rows_result()?;
            // On a CAS miss, Scylla returns [applied]=false + the IF-clause columns (node_id).
            // Check for that shape first; if node_id is present we know applied=false.
            if rows.column_specs().get_by_name("node_id").is_some() {
                return Ok(false);
            }
            if let Some(row) = rows.rows::<(bool,)>()?.next() {
                return Ok(row?.0);
            }
            Ok(false)
        })
        .await
    }

    #[tracing::instrument(skip(self))]
    pub async fn delete_lock(&self, lock_name: &str) -> Result<()> {
        let session = self.get_session().await?;
        let mut prepared_statement = session
            .prepare(get_delete_locks_stmt(&self.config.keyspace))
            .await?;
        prepared_statement.set_is_idempotent(true);
        match session
            .execute_unpaged(&prepared_statement, (lock_name,))
            .await
        {
            Ok(_) => {
                tracing::debug!("Successfully deleted lock {} from Cassandra", lock_name);
            }
            Err(e) => {
                tracing::error!("Failed to delete lock {} from Cassandra: {}", lock_name, e);
                return Err(e.into());
            }
        }
        Ok(())
    }

    #[tracing::instrument(skip(self))]
    pub async fn insert_to_nodes(&self, node: &NodeHealth) -> Result<()> {
        let session = self.get_session().await?;
        let mut prepared_statement = session
            .prepare(get_stmt_insert_to_nodes(
                &self.config.keyspace,
                self.config.node_health_ttl_seconds,
            ))
            .await?;
        prepared_statement.set_is_idempotent(true);
        match session
            .execute_unpaged(&prepared_statement, (&node.node_id, &node.last_updated_at))
            .await
        {
            Ok(_) => {
                tracing::debug!("Successfully inserted node {} to Cassandra", node.node_id);
            }
            Err(e) => {
                tracing::error!("Failed to insert node {} to Cassandra: {}", node.node_id, e);
                return Err(e.into());
            }
        }
        Ok(())
    }

    #[tracing::instrument(skip(self))]
    pub async fn get_all_nodes(&self) -> Result<Vec<NodeHealth>> {
        let session = self.get_session().await?;

        with_cassandra_timing("get_all_nodes", || async {
            let prepared_statement = session
                .prepare(get_select_all_nodes_stmt(&self.config.keyspace))
                .await?;

            let iter = session
                .execute_iter(prepared_statement, ())
                .await?
                .rows_stream::<NodeHealth>()?;
            Ok(iter.try_collect().await?)
        })
        .await
    }

    #[tracing::instrument(skip(self))]
    pub async fn delete_node(&self, node_id: &str) -> Result<()> {
        let session = self.get_session().await?;
        let mut prepared_statement = session
            .prepare(get_delete_node_stmt(&self.config.keyspace))
            .await?;
        prepared_statement.set_is_idempotent(true);
        match session
            .execute_unpaged(&prepared_statement, (node_id,))
            .await
        {
            Ok(_) => {
                tracing::debug!("Successfully deleted node {} from Cassandra", node_id);
            }
            Err(e) => {
                tracing::error!("Failed to delete node {} from Cassandra: {}", node_id, e);
                return Err(e.into());
            }
        }
        Ok(())
    }

    #[tracing::instrument(skip(self))]
    pub async fn get_range_assigned_to_node(&self, node_id: &str) -> Result<[i64; 2]> {
        let mut nodes = self.get_all_nodes().await?;
        nodes.sort_by(|a, b| a.node_id.cmp(&b.node_id));
        // Find the index of our node
        let index = nodes
            .iter()
            .position(|node| node.node_id == node_id)
            .ok_or_else(|| anyhow::anyhow!("Node not found"))?;

        // Calculate token ranges
        let min_token: i128 = CASSANDRA_TOKEN_RANGE[0] as i128;
        let max_token: i128 = CASSANDRA_TOKEN_RANGE[1] as i128;
        let total_nodes = nodes.len() as i128;

        let total_range = max_token - min_token + 1;

        let step = total_range / total_nodes;
        let mut result = Vec::with_capacity(total_nodes as usize);

        for i in 0..total_nodes {
            let range_start = min_token + i * step;
            let range_end = if i == total_nodes - 1 {
                max_token // include any remaining values in last slice
            } else {
                range_start + step - 1
            };
            result.push((range_start, range_end));
        }

        let ans = result.get(index).unwrap();
        Ok([ans.0 as i64, ans.1 as i64])
    }
}

#[async_trait]
impl HealthChecker for CassandraServiceManager {
    async fn health(&self) -> HealthCheckResult {
        // Check if we have a cached result
        if let Some(cached_result) = &*self.health_check_cache.lock().unwrap() {
            let elapsed_time = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_secs()
                - cached_result.last_updated;

            if elapsed_time < self.health_check_cache_ttl.as_secs() {
                return cached_result.clone();
            }
        }

        // Perform actual health check
        let result = match self.get_session().await {
            Ok(session) => {
                let query_result = with_cassandra_timing("health_check_trait", || async {
                    let mut statement =
                        Statement::new(get_health_check_query_stmt(&self.config.keyspace));
                    statement.set_is_idempotent(true);
                    session.query_unpaged(statement, ()).await
                })
                .await;

                match query_result {
                    Ok(_) => HealthCheckResult {
                        is_healthy: true,
                        message: Some("Cassandra session is healthy".to_string()),
                        last_updated: SystemTime::now()
                            .duration_since(UNIX_EPOCH)
                            .unwrap()
                            .as_secs(),
                    },
                    Err(e) => HealthCheckResult {
                        is_healthy: false,
                        message: Some(format!("Query failed: {}", e)),
                        last_updated: SystemTime::now()
                            .duration_since(UNIX_EPOCH)
                            .unwrap()
                            .as_secs(),
                    },
                }
            }
            Err(e) => HealthCheckResult {
                is_healthy: false,
                message: Some(e.to_string()),
                last_updated: SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_secs(),
            },
        };

        // Cache the result
        *self.health_check_cache.lock().unwrap() = Some(result.clone());
        result
    }

    fn component_name(&self) -> &str {
        "cassandra_client"
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cassandra::cassandra_settings::{
        ExecutionProfileSettings, PoolSettings, SslSettings,
    };
    use crate::secrets::secrets_file_watcher::SecretFileWatcher;
    use chrono::Utc;
    use std::path::Path;
    use std::sync::{Once, OnceLock};
    use tempfile::TempDir;
    use uuid::Uuid;

    static INIT: Once = Once::new();
    static TEMP_DIR: OnceLock<TempDir> = OnceLock::new();

    fn init_temp_dir() {
        INIT.call_once(|| {
            let dir = tempfile::tempdir().expect("failed to create temp dir");
            TEMP_DIR.set(dir).expect("TempDir already set");
        });
    }

    fn get_test_dir() -> &'static TempDir {
        init_temp_dir();
        TEMP_DIR.get().expect("TempDir not initialized")
    }

    // Helper function to get test secrets path
    fn get_test_secrets_path() -> String {
        std::env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR not set")
            + "/../../local_env/vault/secrets.json"
    }

    // Helper function to create test settings
    async fn create_test_settings() -> CassandraSettings {
        let test_dir = get_test_dir();
        tokio::fs::create_dir_all(&test_dir)
            .await
            .expect("Failed to create test directory");

        CassandraSettings {
            contact_points: "localhost".to_string(),
            port: 9042,
            compression: "lz4".to_string(),
            keyspace: "nvcf_autoscaler".to_string(),
            connection_timeout: Duration::from_secs(5),
            ssl: SslSettings { enabled: true },
            pool: PoolSettings { local_size: 1 },
            execution_profile: ExecutionProfileSettings::default(),
            is_development: true,
            history_prediction_ttl_seconds: 300,
            ..Default::default()
        }
    }

    fn create_test_active_function_details() -> ActiveFunctionDetails {
        ActiveFunctionDetails {
            function_id: Uuid::new_v4(),
            function_version_id: Uuid::new_v4(),
            nca_id: Some("test-nca-id".to_string()),
            num_workers: Some(1),
            last_predicted_desired_instance_count: Some(1),
            last_predicted_error_code: None,
            last_updated_at: Some(Utc::now()),
        }
    }

    fn create_test_lock() -> DistributedLock {
        DistributedLock {
            lock_name: "test_lock".to_string(),
            node_id: Uuid::new_v4().to_string(),
            acquired_at: Utc::now(),
        }
    }

    fn create_test_node_health() -> NodeHealth {
        NodeHealth {
            node_id: Uuid::new_v4().to_string(),
            last_updated_at: Utc::now(),
        }
    }

    #[tokio::test]
    #[ignore = "Requires running Cassandra"]
    async fn test_cassandra_service_initialization() {
        let settings = create_test_settings().await;
        let secrets_path = get_test_secrets_path();
        let secrets_watcher = Arc::new(
            SecretFileWatcher::new(Path::new(&secrets_path))
                .await
                .unwrap(),
        );
        let result = CassandraServiceManager::new(&settings, secrets_watcher).await;
        assert!(result.is_ok());
    }

    #[tokio::test]
    #[ignore = "Requires running Cassandra"]
    async fn test_active_function_operations() {
        let settings = create_test_settings().await;
        let secrets_path = get_test_secrets_path();
        let secrets_watcher = Arc::new(
            SecretFileWatcher::new(Path::new(&secrets_path))
                .await
                .unwrap(),
        );
        let manager = CassandraServiceManager::new(&settings, secrets_watcher)
            .await
            .unwrap();

        // Test both table types
        for table_type in [
            ActiveFunctionTable::RecentlyInvokedFunctions,
            ActiveFunctionTable::RunningFunctionsWithoutInvocations,
        ] {
            let function = create_test_active_function_details();
            // Test insert operation
            let insert_result = manager.add_new_active_function(&function, table_type).await;
            assert!(
                insert_result.is_ok(),
                "Insert failed for {:?}: {:?}",
                table_type,
                insert_result.err()
            );

            // Test get operation
            let get_result = manager
                .get_active_function_history_by_id(
                    &function.function_id,
                    &function.function_version_id,
                    table_type,
                )
                .await;
            assert!(
                get_result.is_ok(),
                "Get failed for {:?}: {:?}",
                table_type,
                get_result.err()
            );
            let result = get_result.unwrap();
            assert!(result.is_some());
            let function_details = result.unwrap();
            assert_eq!(function_details.function_id, function.function_id);
            assert_eq!(
                function_details.function_version_id,
                function.function_version_id
            );
            assert_eq!(function_details.num_workers, Some(1));
            assert_eq!(function_details.last_predicted_desired_instance_count, None);
            assert_eq!(function_details.last_predicted_error_code, None);

            // Test get operation with token range
            let token_range = CASSANDRA_TOKEN_RANGE;
            let get_result = manager
                .get_active_functions_with_token_range(&token_range, 100, table_type)
                .await;
            assert!(get_result.is_ok());
            let functions = get_result.unwrap();
            assert!(!functions.is_empty());
            assert_eq!(functions.len(), 1);
            assert_eq!(functions[0].function_id, function.function_id);
            assert_eq!(
                functions[0].function_version_id,
                function.function_version_id
            );

            // Test insert details with some fields
            let insert_result = manager
                .insert_to_active_function_history_prediction_row(&function, table_type)
                .await;
            assert!(insert_result.is_ok());

            // Test get operation
            let get_result = manager
                .get_active_function_history_by_id(
                    &function_details.function_id,
                    &function_details.function_version_id,
                    table_type,
                )
                .await;
            assert!(get_result.is_ok());
            let result = get_result.unwrap();
            assert!(result.is_some());
            let mut modified_function_details = result.unwrap();
            assert_eq!(function.function_id, modified_function_details.function_id);
            assert_eq!(
                function.function_version_id,
                modified_function_details.function_version_id
            );
            let expected_num_workers = function.num_workers;
            assert_eq!(function.num_workers, modified_function_details.num_workers);
            assert_eq!(modified_function_details.num_workers, expected_num_workers);
            // Expect the value from the fixture (Some(1) for first insert)
            assert_eq!(
                modified_function_details.last_predicted_desired_instance_count,
                Some(1)
            );
            assert_eq!(modified_function_details.last_predicted_error_code, None);

            // Insert again with some fields modified
            modified_function_details.last_predicted_desired_instance_count = Some(10);
            modified_function_details.last_predicted_error_code = None;
            let insert_result = manager
                .insert_to_active_function_history_prediction_row(
                    &modified_function_details,
                    table_type,
                )
                .await;
            assert!(insert_result.is_ok());

            // Test get operation again
            let get_result = manager
                .get_active_function_history_by_id(
                    &function_details.function_id,
                    &function_details.function_version_id,
                    table_type,
                )
                .await;
            assert!(get_result.is_ok());
            let result = get_result.unwrap();
            assert!(result.is_some());
            let function_details = result.unwrap();
            assert_eq!(
                function_details.function_id,
                modified_function_details.function_id
            );
            assert_eq!(
                function_details.function_version_id,
                modified_function_details.function_version_id
            );
            assert_eq!(
                function_details.num_workers,
                modified_function_details.num_workers
            );
            assert_eq!(function_details.num_workers, expected_num_workers);
            // Expect the last value written (Some(10) for second insert)
            assert_eq!(
                function_details.last_predicted_desired_instance_count,
                Some(10)
            );
            assert_eq!(function_details.last_predicted_error_code, None);

            // Test delete operation
            let delete_result = manager
                .delete_active_function(
                    &function.function_id,
                    &function.function_version_id,
                    table_type,
                )
                .await;
            assert!(delete_result.is_ok());
            // Test get operation again- it should be empty
            let get_result = manager
                .get_active_functions_with_token_range(&token_range, 100, table_type)
                .await;
            assert!(get_result.is_ok());
            assert!(get_result.unwrap().is_empty());
        }
    }

    #[tokio::test]
    #[ignore = "Requires running Cassandra"]
    async fn test_lock_operations() {
        let settings = create_test_settings().await;
        let secrets_path = get_test_secrets_path();
        let secrets_watcher = Arc::new(
            SecretFileWatcher::new(Path::new(&secrets_path))
                .await
                .unwrap(),
        );
        let manager = CassandraServiceManager::new(&settings, secrets_watcher)
            .await
            .unwrap();

        let lock = create_test_lock();
        let ttl_seconds = 2;

        // Test put operation
        let put_result = manager.put_lock(&lock, ttl_seconds).await;
        assert!(put_result.is_ok());

        // Test get operation
        let get_result = manager.get_lock(&lock.lock_name).await;
        assert!(get_result.is_ok());
        assert!(get_result.unwrap().is_some());

        // Test insert operation again- it should fail
        let put_result = manager.put_lock(&lock, ttl_seconds).await;
        assert!(put_result.is_ok());
        assert!(!put_result.unwrap());

        tokio::time::sleep(Duration::from_secs(ttl_seconds as u64 + 1)).await;

        // Test get operation again- it should be None
        let get_result = manager.get_lock(&lock.lock_name).await;
        assert!(get_result.is_ok());
        assert!(get_result.unwrap().is_none());
        // Test insert again- it should succeed
        let put_result = manager.put_lock(&lock, ttl_seconds).await;
        assert!(put_result.is_ok());
        assert!(put_result.unwrap());

        // Test delete operation
        let delete_result = manager.delete_lock(&lock.lock_name).await;
        assert!(delete_result.is_ok());

        // Test get operation again- it should be None
        let get_result = manager.get_lock(&lock.lock_name).await;
        assert!(get_result.is_ok());
        assert!(get_result.unwrap().is_none());

        // Test delete operation again- it should fail
        let delete_result = manager.delete_lock(&lock.lock_name).await;
        assert!(delete_result.is_ok());
    }

    #[tokio::test]
    #[ignore = "Requires running Cassandra"]
    async fn test_node_operations() {
        let settings = create_test_settings().await;
        let secrets_path = get_test_secrets_path();
        let secrets_watcher = Arc::new(
            SecretFileWatcher::new(Path::new(&secrets_path))
                .await
                .unwrap(),
        );
        let manager = CassandraServiceManager::new(&settings, secrets_watcher)
            .await
            .unwrap();

        let node_health = create_test_node_health();

        // Get all nodes
        let get_result = manager.get_all_nodes().await;
        assert!(get_result.is_ok());
        assert!(get_result.unwrap().is_empty());

        // Test put operation for node health
        let put_result = manager.insert_to_nodes(&node_health).await;
        assert!(put_result.is_ok());

        // Get all nodes again
        let get_result = manager.get_all_nodes().await;
        assert!(get_result.is_ok());
        let nodes = get_result.unwrap();
        assert!(!nodes.is_empty());
        let original_node = nodes.first().unwrap();
        assert_eq!(original_node.node_id, node_health.node_id);

        // Sleep for 2 seconds
        tokio::time::sleep(Duration::from_secs(2)).await;
        // Test update operation
        let time_now = Utc::now();
        let updated_node = NodeHealth {
            node_id: node_health.node_id.clone(),
            last_updated_at: time_now,
        };
        let update_result = manager.insert_to_nodes(&updated_node).await;
        assert!(update_result.is_ok());

        let get_result = manager.get_all_nodes().await;
        assert!(get_result.is_ok());
        let nodes = get_result.unwrap();
        assert!(!nodes.is_empty());
        let stored_node = nodes.first().unwrap();
        assert_eq!(stored_node.node_id, updated_node.node_id);

        let time_diff = original_node
            .last_updated_at
            .signed_duration_since(updated_node.last_updated_at);
        assert!(time_diff.num_seconds().abs() > 1); // Allow for a small difference in seconds

        // Test delete operation
        let delete_result = manager.delete_node(&node_health.node_id).await;
        assert!(delete_result.is_ok());

        // Verify node is deleted
        let get_result = manager.get_all_nodes().await;
        assert!(get_result.is_ok());
        let nodes = get_result.unwrap();
        assert!(nodes.is_empty());

        // Create and insert multiple nodes
        let node1 = create_test_node_health();
        let node2 = create_test_node_health();
        let node3 = create_test_node_health();

        manager.insert_to_nodes(&node1).await.unwrap();
        manager.insert_to_nodes(&node2).await.unwrap();
        manager.insert_to_nodes(&node3).await.unwrap();

        // Sort nodes by node_id
        let mut nodes = vec![node1, node2, node3];
        nodes.sort_by_key(|node| node.node_id.clone());

        // Get ranges for each node
        let range1 = manager.get_range_assigned_to_node(&nodes[0].node_id).await;
        let range2 = manager.get_range_assigned_to_node(&nodes[1].node_id).await;
        let range3 = manager.get_range_assigned_to_node(&nodes[2].node_id).await;

        // Verify results
        assert!(range1.is_ok());
        assert!(range2.is_ok());
        assert!(range3.is_ok());

        let ranges = [range1.unwrap(), range2.unwrap(), range3.unwrap()];

        // Verify ranges are non-overlapping and cover the entire token space
        assert_eq!(ranges.len(), 3);

        // Check that ranges are in order
        for i in 0..ranges.len() - 1 {
            assert!(ranges[i][1] < ranges[i + 1][0]);
        }

        // Check that first range starts at min_token and last range ends at max_token
        assert_eq!(ranges[0][0], CASSANDRA_TOKEN_RANGE[0]);
        assert_eq!(ranges[2][1], CASSANDRA_TOKEN_RANGE[1]);

        // Check that ranges are roughly equal in size using i128 for calculations
        let range_sizes: Vec<i128> = ranges
            .iter()
            .map(|r| (r[1] as i128) - (r[0] as i128) + 1)
            .collect();
        let avg_size = range_sizes.iter().sum::<i128>() / range_sizes.len() as i128;
        for size in range_sizes {
            assert!((size - avg_size).abs() <= 1); // Allow for 1 token difference due to rounding
        }

        // Test non-existent node
        let non_existent_range = manager.get_range_assigned_to_node("non_existent_id").await;
        assert!(non_existent_range.is_err());

        // Test delete nodes
        for node in nodes {
            let delete_result = manager.delete_node(&node.node_id).await;
            assert!(delete_result.is_ok());
        }
    }

    #[tokio::test]
    #[ignore = "Requires running Cassandra"]
    async fn test_cassandra_service_health_check() {
        let settings = create_test_settings().await;
        let secrets_path = get_test_secrets_path();
        let secrets_watcher = Arc::new(
            SecretFileWatcher::new(Path::new(&secrets_path))
                .await
                .unwrap(),
        );
        let manager = CassandraServiceManager::new(&settings, secrets_watcher)
            .await
            .unwrap();

        // Test health check
        let health = manager.health().await;

        assert!(health.message.is_some());
        assert!(health.last_updated > 0);
    }

    #[tokio::test]
    #[ignore = "Requires running Cassandra"]
    async fn test_cassandra_service_health_check_caching() {
        let settings = create_test_settings().await;
        let secrets_path = get_test_secrets_path();
        let secrets_watcher = Arc::new(
            SecretFileWatcher::new(Path::new(&secrets_path))
                .await
                .unwrap(),
        );
        let manager = CassandraServiceManager::new(&settings, secrets_watcher)
            .await
            .unwrap();

        // Set a mocked cached result
        *manager.health_check_cache.lock().unwrap() = Some(HealthCheckResult {
            is_healthy: true,
            message: Some("Mocked Cassandra session is healthy".to_string()),
            last_updated: SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_secs(),
        });

        // First call should return cached result
        let health1 = manager.health().await;
        assert!(health1.is_healthy);
        assert_eq!(
            health1.message,
            Some("Mocked Cassandra session is healthy".to_string())
        );

        // Second call should also return cached result (within TTL)
        let health2 = manager.health().await;
        assert!(health2.is_healthy);
        assert_eq!(
            health2.message,
            Some("Mocked Cassandra session is healthy".to_string())
        );

        // Both results should have the same timestamp (proving cache was used)
        assert_eq!(health1.last_updated, health2.last_updated);
    }

    #[tokio::test]
    #[ignore = "Requires running Cassandra"]
    async fn test_cassandra_service_manager_service_recreation() {
        let settings = create_test_settings().await;
        let secrets_path = get_test_secrets_path();
        let secrets_watcher = Arc::new(
            SecretFileWatcher::new(Path::new(&secrets_path))
                .await
                .unwrap(),
        );

        let manager = Arc::new(
            CassandraServiceManager::new(&settings, secrets_watcher)
                .await
                .unwrap(),
        );

        // Test that session is initially available
        assert!(manager.get_session().await.is_ok());

        // Test session recreation
        manager.attempt_service_recreation().await;

        // Session should still be available after recreation
        assert!(manager.get_session().await.is_ok());
    }

    #[tokio::test]
    #[ignore = "Requires running Cassandra"]
    async fn test_cassandra_service_manager_operations() {
        let settings = create_test_settings().await;
        let secrets_path = get_test_secrets_path();
        let secrets_watcher = Arc::new(
            SecretFileWatcher::new(Path::new(&secrets_path))
                .await
                .unwrap(),
        );

        let manager = CassandraServiceManager::new(&settings, secrets_watcher)
            .await
            .unwrap();

        // Test get operation
        let get_result = manager.get_all_nodes().await;
        assert!(get_result.is_ok());
    }

    #[tokio::test]
    #[ignore = "Requires running Cassandra"]
    async fn test_cassandra_service_health_check_cache_expiry() {
        let settings = create_test_settings().await;
        let secrets_path = get_test_secrets_path();
        let secrets_watcher = Arc::new(
            SecretFileWatcher::new(Path::new(&secrets_path))
                .await
                .unwrap(),
        );

        // Create manager with a very short cache TTL for testing
        let manager = CassandraServiceManager {
            config: settings.clone(),
            secrets_watcher: secrets_watcher.clone(),
            current_session: Arc::new(Mutex::new(None)),
            health_check_cache: Mutex::new(None),
            health_check_cache_ttl: Duration::from_millis(100), // Very short TTL
        };

        // Initialize the session
        manager.initialize_service().await.unwrap();

        // Set an old cached result (simulate expired cache)
        *manager.health_check_cache.lock().unwrap() = Some(HealthCheckResult {
            is_healthy: true,
            message: Some("Old cached result".to_string()),
            last_updated: SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_secs()
                - 1, // 1 second old
        });

        // Sleep to ensure cache expires
        tokio::time::sleep(Duration::from_millis(200)).await;

        // This call should perform a fresh health check, not use cache
        let health = manager.health().await;

        // The message should not be the old cached one
        assert_ne!(health.message, Some("Old cached result".to_string()));
        // Should have a fresh timestamp
        let current_time = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs();
        assert!(health.last_updated >= current_time - 2); // Within last 2 seconds
    }
}

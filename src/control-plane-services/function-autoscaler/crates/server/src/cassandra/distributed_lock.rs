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

use crate::cassandra::cassandra_service::CassandraServiceManager;
use crate::metrics;
use crate::models::DistributedLock;
use anyhow::Result;
use chrono::Utc;
use std::sync::Arc;
use tracing;

pub struct DistributedLockManager {
    cassandra_service: Arc<CassandraServiceManager>,
    node_id: String,
}

impl DistributedLockManager {
    pub fn new(cassandra_service: Arc<CassandraServiceManager>, node_id: String) -> Self {
        Self {
            cassandra_service,
            node_id,
        }
    }

    pub fn node_id(&self) -> &str {
        &self.node_id
    }

    pub async fn try_acquire(
        &self,
        lock_name: String,
        lock_duration_seconds: i32,
    ) -> Result<Option<DistributedLockGuard>> {
        DistributedLockGuard::try_acquire(
            lock_name,
            self.node_id.clone(),
            self.cassandra_service.clone(),
            lock_duration_seconds,
        )
        .await
    }

    /// Try to acquire without a guard — lock persists until TTL expiry or explicit delete.
    /// Returns true if acquired, false if another node holds it.
    pub async fn try_acquire_persistent(
        &self,
        lock_name: String,
        lock_duration_seconds: i32,
    ) -> Result<bool> {
        self.cassandra_service
            .put_lock(
                &DistributedLock {
                    lock_name,
                    node_id: self.node_id.clone(),
                    acquired_at: Utc::now(),
                },
                lock_duration_seconds,
            )
            .await
    }

    pub async fn get_lock(&self, lock_name: &str) -> Result<Option<DistributedLock>> {
        self.cassandra_service.get_lock(lock_name).await
    }

    /// LWT TTL refresh — only applies if this node still owns the lock.
    /// Returns Ok(true) if refreshed, Ok(false) if another node now owns the lock.
    pub async fn refresh_lock_ttl(
        &self,
        lock_name: &str,
        lock_duration_seconds: i32,
    ) -> Result<bool> {
        self.cassandra_service
            .refresh_lock_ttl(lock_name, &self.node_id, lock_duration_seconds)
            .await
    }
}

pub struct DistributedLockGuard {
    lock_name: String,
    cassandra_service: Arc<CassandraServiceManager>,
    released: bool,
}

impl DistributedLockGuard {
    pub async fn try_acquire(
        lock_name: String,
        node_id: String,
        cassandra_service: Arc<CassandraServiceManager>,
        lock_duration_seconds: i32,
    ) -> Result<Option<Self>> {
        let lock_acquired = cassandra_service
            .put_lock(
                &DistributedLock {
                    lock_name: lock_name.clone(),
                    node_id,
                    acquired_at: Utc::now(),
                },
                lock_duration_seconds,
            )
            .await?;

        if lock_acquired {
            Ok(Some(Self {
                lock_name,
                cassandra_service,
                released: false,
            }))
        } else {
            metrics::record_distributed_lock_acquisition_failure(&lock_name);
            Ok(None)
        }
    }
}

impl Drop for DistributedLockGuard {
    fn drop(&mut self) {
        if !self.released {
            let lock_name = self.lock_name.clone();
            let cassandra_service = self.cassandra_service.clone();

            tokio::spawn(async move {
                match cassandra_service.delete_lock(&lock_name).await {
                    Ok(()) => {
                        tracing::info!("Released lock {} during drop", lock_name);
                    }
                    Err(e) => {
                        tracing::error!("Failed to release lock {} during drop: {}", lock_name, e);

                        // If deletion fails, spawn a task to retry after the lock duration
                        let retry_lock_name = lock_name.clone();
                        let retry_cassandra_service = cassandra_service.clone();
                        tokio::spawn(async move {
                            tokio::time::sleep(tokio::time::Duration::from_secs(5)).await;
                            if let Err(e) =
                                retry_cassandra_service.delete_lock(&retry_lock_name).await
                            {
                                tracing::error!(
                                    "Failed to release lock {} during retry: {}",
                                    retry_lock_name,
                                    e
                                );
                            } else {
                                tracing::info!(
                                    "Successfully released lock {} during retry",
                                    retry_lock_name
                                );
                            }
                        });
                    }
                }
            });
        }
    }
}

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
use crate::cassandra::distributed_lock::DistributedLockManager;
use crate::cassandra::statements::ActiveFunctionTable;
use crate::metrics;
use crate::models::ActiveFunctionDetails;
use crate::nvcf_api::oauth2_client;
use crate::nvcf_api::{
    AutoscalerRequest, AutoscalerResponse, DeploymentInfo, FunctionStatus, NvcfApiError,
};
use crate::secrets::secrets_file_watcher::SecretFileWatcher;
use crate::work::bucket::{NodeBucketManager, BUCKET_COUNT};
use crate::work::{FunctionCachedState, FunctionStateCache};
use chrono::Utc;
use leaky_bucket::RateLimiter;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

use std::sync::Arc;
use std::time::Duration;
use tokio::sync::mpsc;
use tokio_util::sync::{CancellationToken, DropGuard};
use tracing;
use uuid::Uuid;

const SCALE_FUNCTION_URL: &str = "/v2/nvcf/predictions/functions/{funcId}/versions/{versionId}";
const NVCF_API_BUCKET_LOCK_TTL_SECONDS: Duration = Duration::from_secs(15);
pub const NVCF_API_BUCKET_LOCK_PREFIX: &str = "nvcf_api_bucket";

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NvcfApiSettings {
    /// Base URL for the OAuth2 client API used for authentication
    pub oauth2_token_api_address: String,
    /// Base URL for the NVCF API
    pub nvcf_api_address: String,
    #[serde(default)]
    pub disable_auth: bool,
    pub dry_run: bool,
    #[serde(default = "default_auth_http_timeout")]
    pub auth_http_timeout_seconds: u64,
    #[serde(default = "default_token_refresh_interval")]
    pub token_refresh_interval_seconds: u64,
    #[serde(default)]
    pub rate_limiter: RateLimiterSettings,
}

fn default_auth_http_timeout() -> u64 {
    30
}
fn default_token_refresh_interval() -> u64 {
    180
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(default)]
pub struct RateLimiterSettings {
    pub queue_capacity: usize,
    pub bucket_capacity: usize,
    pub refill_rate: usize,
    pub refill_interval_ms: u64,
}

impl Default for RateLimiterSettings {
    fn default() -> Self {
        Self {
            queue_capacity: 5000,
            bucket_capacity: 5000,
            refill_rate: 200,
            refill_interval_ms: 100,
        }
    }
}

impl Default for NvcfApiSettings {
    fn default() -> Self {
        Self {
            oauth2_token_api_address: "".to_string(),
            nvcf_api_address: "http://localhost:8082".to_string(),
            disable_auth: true,
            dry_run: false,
            auth_http_timeout_seconds: default_auth_http_timeout(),
            token_refresh_interval_seconds: default_token_refresh_interval(),
            rate_limiter: RateLimiterSettings::default(),
        }
    }
}

pub(crate) struct NvcfApiServiceSettings {
    // How many requests can be queued to be processed per bucket
    queue_capacity: usize,
    num_queues: usize,
    // How many requests can be processed at a time globally
    bucket_capacity: usize,
    // Refill rate of the rate limiter
    refill_rate: usize,
    // Interval at which the rate limiter is refilled
    interval: Duration,
}

impl Default for NvcfApiServiceSettings {
    fn default() -> Self {
        Self::from(RateLimiterSettings::default())
    }
}

impl From<RateLimiterSettings> for NvcfApiServiceSettings {
    fn from(rl: RateLimiterSettings) -> Self {
        Self {
            queue_capacity: rl.queue_capacity,
            num_queues: BUCKET_COUNT,
            bucket_capacity: rl.bucket_capacity,
            refill_rate: rl.refill_rate,
            interval: Duration::from_millis(rl.refill_interval_ms),
        }
    }
}

struct ProcessRequestCtx<'a> {
    rate_limiter: &'a Arc<RateLimiter>,
    nvcf_api_url: &'a str,
    nvcf_api_client: &'a reqwest_middleware::ClientWithMiddleware,
    oauth2_client: Option<&'a oauth2_client::OAuth2Client>,
    cassandra_service: Option<&'a CassandraServiceManager>,
    function_state_cache: Option<&'a FunctionStateCache>,
    dry_run: bool,
}

pub struct NvcfApiService {
    pub nvcf_api_url: String,
    pub nvcf_api_client: reqwest_middleware::ClientWithMiddleware,
    cassandra_service: Option<Arc<CassandraServiceManager>>,
    function_state_cache: Option<Arc<FunctionStateCache>>,
    oauth2_client: Option<oauth2_client::OAuth2Client>,
    per_bucket_request_queue: HashMap<usize, mpsc::Sender<DeploymentInfo>>,
    rate_limiter: Arc<RateLimiter>,
    _drop_guard: DropGuard,
    queue_processor_handles: Vec<tokio::task::JoinHandle<()>>,
}

impl NvcfApiService {
    pub fn new(
        nvcf_api_settings: NvcfApiSettings,
        cassandra_service: Option<Arc<CassandraServiceManager>>,
        secrets_watcher: Arc<SecretFileWatcher>,
        bucket_manager: Arc<NodeBucketManager>,
        lock_manager: Arc<DistributedLockManager>,
        function_state_cache: Option<Arc<FunctionStateCache>>,
    ) -> Self {
        let settings = NvcfApiServiceSettings::from(nvcf_api_settings.rate_limiter.clone());
        Self::with_capacity_and_rate_limit(
            nvcf_api_settings,
            cassandra_service,
            secrets_watcher,
            settings,
            lock_manager,
            bucket_manager,
            function_state_cache,
        )
    }

    pub(crate) fn with_capacity_and_rate_limit(
        nvcf_api_settings: NvcfApiSettings,
        cassandra_service: Option<Arc<CassandraServiceManager>>,
        secrets_watcher: Arc<SecretFileWatcher>,
        settings: NvcfApiServiceSettings,
        lock_manager: Arc<DistributedLockManager>,
        bucket_manager: Arc<NodeBucketManager>,
        function_state_cache: Option<Arc<FunctionStateCache>>,
    ) -> Self {
        let oauth2_client = if !nvcf_api_settings.disable_auth {
            Some(
                oauth2_client::OAuth2Client::with_timeouts(
                    nvcf_api_settings.oauth2_token_api_address,
                    secrets_watcher,
                    Duration::from_secs(nvcf_api_settings.auth_http_timeout_seconds),
                    Duration::from_secs(nvcf_api_settings.token_refresh_interval_seconds),
                )
                .unwrap(),
            )
        } else {
            None
        };

        // Create one channel per bucket
        let (bucket_senders, bucket_receivers): (HashMap<_, _>, HashMap<_, _>) = (0..settings
            .num_queues)
            .map(|bucket_index| {
                let (sender, receiver) = mpsc::channel(settings.queue_capacity);
                ((bucket_index, sender), (bucket_index, receiver))
            })
            .unzip();

        let cancellation_token = CancellationToken::new();
        let drop_guard = cancellation_token.clone().drop_guard();

        let mut service = Self {
            nvcf_api_url: nvcf_api_settings.nvcf_api_address,
            nvcf_api_client: reqwest_middleware::ClientBuilder::new(reqwest::Client::new())
                .with(reqwest_tracing::TracingMiddleware::default())
                .build(),
            cassandra_service,
            function_state_cache,
            oauth2_client,
            per_bucket_request_queue: bucket_senders,
            rate_limiter: Arc::new(
                RateLimiter::builder()
                    .max(settings.bucket_capacity)
                    .initial(settings.bucket_capacity)
                    .refill(settings.refill_rate)
                    .interval(settings.interval)
                    .build(),
            ),
            _drop_guard: drop_guard,
            queue_processor_handles: Vec::new(),
        };

        // Start background processing for all buckets
        service.start_background_processing(
            bucket_receivers,
            lock_manager,
            nvcf_api_settings.dry_run,
            bucket_manager,
            cancellation_token,
        );
        service
    }

    pub fn start_background_processing(
        &mut self,
        bucket_receivers: HashMap<usize, mpsc::Receiver<DeploymentInfo>>,
        lock_manager: Arc<DistributedLockManager>,
        dry_run: bool,
        bucket_manager: Arc<NodeBucketManager>,
        cancellation_token: CancellationToken,
    ) {
        let rate_limiter = self.rate_limiter.clone();
        let nvcf_api_url = self.nvcf_api_url.clone();
        let nvcf_api_client = self.nvcf_api_client.clone();
        let oauth2_client = self.oauth2_client.clone();
        let cassandra_service = self.cassandra_service.clone();
        let function_state_cache = self.function_state_cache.clone();

        for (bucket_index, mut receiver) in bucket_receivers {
            let rate_limiter = rate_limiter.clone();
            let nvcf_api_url = nvcf_api_url.clone();
            let nvcf_api_client = nvcf_api_client.clone();
            let oauth2_client = oauth2_client.clone();
            let cassandra_service = cassandra_service.clone();
            let function_state_cache = function_state_cache.clone();
            let bucket_manager = bucket_manager.clone();
            let lock_manager = lock_manager.clone();
            let cancellation_token = cancellation_token.clone();

            let handle = tokio::spawn(async move {
                loop {
                    tokio::select! {
                        _ = cancellation_token.cancelled() => {
                            tracing::info!("Bucket {} processor received shutdown signal", bucket_index);
                            break;
                        }

                        // Main processing
                        _ = async {
                            // Skip if bucket not assigned to this node
                            if !bucket_manager.get_my_buckets().contains(&bucket_index) {
                                return;
                            }

                            // Skip if no work pending - check without blocking
                            if receiver.is_empty() {
                                return;
                            }

                    if let Some(cassandra_service) = &cassandra_service {
                        let lock_name = format!("{}_{}", NVCF_API_BUCKET_LOCK_PREFIX, bucket_index);
                        match lock_manager.try_acquire(
                            lock_name,
                            NVCF_API_BUCKET_LOCK_TTL_SECONDS.as_secs() as i32,
                        )
                        .await
                        {
                            Ok(Some(_lock_guard)) => {
                                tracing::debug!(
                                    "Bucket {}: Acquired lock, processing requests",
                                    bucket_index
                                );

                                let processing_start = std::time::Instant::now();

                                loop {
                                    if processing_start.elapsed() >= NVCF_API_BUCKET_LOCK_TTL_SECONDS {
                                        tracing::debug!("Bucket {}: Lock timeout expired, releasing", bucket_index);
                                        break;
                                    }

                                    match receiver.try_recv() {
                                        Ok(info) => {
                                            if let Err(e) = Self::process_single_request(
                                                info,
                                                &ProcessRequestCtx {
                                                    rate_limiter: &rate_limiter,
                                                    nvcf_api_url: &nvcf_api_url,
                                                    nvcf_api_client: &nvcf_api_client,
                                                    oauth2_client: oauth2_client.as_ref(),
                                                    cassandra_service: Some(cassandra_service.as_ref()),
                                                    function_state_cache: function_state_cache.as_deref(),
                                                    dry_run,
                                                },
                                            )
                                            .await
                                            {
                                                tracing::error!(
                                                    "Bucket {}: Failed to process request: {}",
                                                    bucket_index,
                                                    e
                                                );
                                            }
                                            metrics::record_request_processed();
                                        }
                                        Err(mpsc::error::TryRecvError::Empty) => {
                                            break;
                                        }
                                        Err(mpsc::error::TryRecvError::Disconnected) => {
                                            tracing::warn!("Bucket {}: channel disconnected", bucket_index);
                                            return;
                                        }
                                    }
                                }
                                // Lock is automatically released when _lock_guard goes out of scope
                            }
                            Ok(None) => {
                                tracing::debug!(
                                    "Bucket {}: Could not acquire lock, skipping",
                                    bucket_index
                                );
                            }
                            Err(e) => {
                                tracing::error!(
                                    "Bucket {}: Error acquiring lock: {}",
                                    bucket_index,
                                    e
                                );
                            }
                        }
                            } else {
                                tracing::debug!("Bucket {}: No Cassandra, rejecting request", bucket_index);
                            }
                        } => {
                            tokio::time::sleep(Duration::from_millis(250)).await;
                        }
                    }
                }
                tracing::info!("Bucket {} processor shutdown complete", bucket_index);
            });

            self.queue_processor_handles.push(handle);
        }
    }

    pub fn queue_scaling_request(
        &self,
        bucket_index: usize,
        deployment_info: DeploymentInfo,
    ) -> Result<(), NvcfApiError> {
        if let Some(sender) = self.per_bucket_request_queue.get(&bucket_index) {
            match sender.try_send(deployment_info) {
                Ok(()) => {
                    metrics::record_request_queued();
                    Ok(())
                }
                Err(mpsc::error::TrySendError::Full(_)) => {
                    metrics::record_request_rate_limited();
                    Err(NvcfApiError::QueueFull)
                }
                Err(mpsc::error::TrySendError::Closed(_)) => {
                    tracing::error!("Bucket {} queue is closed", bucket_index);
                    Err(NvcfApiError::InternalError)
                }
            }
        } else {
            tracing::error!("Invalid bucket index: {}", bucket_index);
            Err(NvcfApiError::InternalError)
        }
    }

    pub async fn get_function_status(
        &self,
        function_id: Uuid,
        function_version_id: Uuid,
        nca_id: String,
    ) -> Result<AutoscalerResponse, NvcfApiError> {
        // Make a dummy scaling request to get current status
        let deployment_info = DeploymentInfo {
            function_id,
            function_version_id,
            nca_id,
            required_number_of_instances: -1, // Request -1 to just get status
            recently_invoked: false,
            enqueued_at: std::time::Instant::now(),
        };

        Self::scale_function_internal_static(
            &self.nvcf_api_url,
            &self.nvcf_api_client,
            self.oauth2_client.as_ref(),
            deployment_info,
        )
        .await
    }

    #[tracing::instrument(
        name = "scale_function_nvcf_api_call",
        skip(nvcf_api_client, oauth2_client)
    )]
    async fn scale_function_internal_static(
        nvcf_api_url: &str,
        nvcf_api_client: &reqwest_middleware::ClientWithMiddleware,
        oauth2_client: Option<&oauth2_client::OAuth2Client>,
        deployment_info: DeploymentInfo,
    ) -> Result<AutoscalerResponse, NvcfApiError> {
        let start_time = std::time::Instant::now();
        let url = format!(
            "{}{}",
            nvcf_api_url,
            SCALE_FUNCTION_URL
                .replace("{funcId}", &deployment_info.function_id.to_string())
                .replace(
                    "{versionId}",
                    &deployment_info.function_version_id.to_string()
                )
        );

        let request = AutoscalerRequest {
            required_number_of_instances: deployment_info.required_number_of_instances,
            predicted_at: Utc::now(),
        };

        let mut request_builder = nvcf_api_client.put(&url);
        if let Some(oauth2_client) = oauth2_client {
            let jwt_token = oauth2_client.get_jwt_token().await.map_err(|e| {
                tracing::error!(
                    "Failed to get JWT token for function {} version {}: {}",
                    deployment_info.function_id,
                    deployment_info.function_version_id,
                    e
                );
                NvcfApiError::InternalError
            })?;
            request_builder =
                request_builder.header("Authorization", format!("Bearer {}", jwt_token));
        }
        request_builder = request_builder.header("Content-Type", "application/json");
        let body = serde_json::to_vec(&request).map_err(|e| {
            tracing::error!("Failed to serialize scale request: {}", e);
            NvcfApiError::InternalError
        })?;
        request_builder = request_builder.body(body);
        let response = request_builder.send().await.map_err(|e| {
            let duration = start_time.elapsed().as_secs_f64() * 1000.0;
            let error = NvcfApiError::InternalError;
            metrics::record_nvcf_api_request("scale_function", &error.to_short_string(), duration);
            tracing::error!(
                "HTTP request failed for function {} version {}: {}",
                deployment_info.function_id,
                deployment_info.function_version_id,
                e
            );
            error
        })?;
        if !response.status().is_success() {
            let status_code = response.status().as_u16();
            let error_body = response
                .text()
                .await
                .unwrap_or_else(|_| "Unable to read error response".to_string());
            tracing::error!(
                "Error response: {} for functionID {} and versionID {}",
                error_body,
                deployment_info.function_id,
                deployment_info.function_version_id
            );
            match status_code {
                // This means autoscaler gave a prediction that is beyond limits, utilization is high
                400 => {
                    let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                    let error = NvcfApiError::InstanceRequestBeyondLimits;
                    metrics::record_nvcf_api_request(
                        "scale_function",
                        &error.to_short_string(),
                        duration,
                    );
                    return Err(error);
                }
                404 => {
                    let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                    let error = NvcfApiError::FunctionNotFound;
                    metrics::record_nvcf_api_request(
                        "scale_function",
                        &error.to_short_string(),
                        duration,
                    );
                    return Err(error);
                }
                _ => {
                    let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                    let error = NvcfApiError::UnknownError;
                    metrics::record_nvcf_api_request(
                        "scale_function",
                        &error.to_short_string(),
                        duration,
                    );
                    return Err(error);
                }
            }
        }
        // Get response text first to avoid borrow checker issues
        let response_text = response.text().await.map_err(|e| {
            let duration = start_time.elapsed().as_secs_f64() * 1000.0;
            let error = NvcfApiError::InternalError;
            metrics::record_nvcf_api_request("scale_function", &error.to_short_string(), duration);
            tracing::error!(
                "Failed to read response text for function {} version {}: {}",
                deployment_info.function_id,
                deployment_info.function_version_id,
                e
            );
            error
        })?;

        if let Ok(json_value) = serde_json::from_str::<serde_json::Value>(&response_text) {
            let function_status = if let Some(status_str) =
                json_value.get("functionStatus").and_then(|v| v.as_str())
            {
                match status_str {
                    "DEPLOYING" => FunctionStatus::DEPLOYING,
                    "DELETED" => FunctionStatus::DELETED,
                    "ERRORED" => FunctionStatus::ERRORED,
                    "INACTIVE" => FunctionStatus::INACTIVE,
                    _ => FunctionStatus::ACTIVE,
                }
            } else {
                FunctionStatus::ACTIVE
            };

            match function_status {
                FunctionStatus::DEPLOYING => {
                    let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                    metrics::record_nvcf_api_request(
                        "scale_function",
                        "function_deploying",
                        duration,
                    );
                    Err(NvcfApiError::FunctionDeploying)
                }
                FunctionStatus::DELETED => {
                    let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                    metrics::record_nvcf_api_request(
                        "scale_function",
                        "function_deleted",
                        duration,
                    );
                    Err(NvcfApiError::FunctionDeleted)
                }
                FunctionStatus::ERRORED => {
                    let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                    metrics::record_nvcf_api_request(
                        "scale_function",
                        "function_errored",
                        duration,
                    );
                    Err(NvcfApiError::FunctionError)
                }
                FunctionStatus::INACTIVE => {
                    let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                    metrics::record_nvcf_api_request(
                        "scale_function",
                        "function_inactive",
                        duration,
                    );
                    Err(NvcfApiError::FunctionNotActive)
                }
                FunctionStatus::ACTIVE => {
                    let active_instances: i32 = json_value
                        .get("activeInstances")
                        .and_then(|v| v.as_i64())
                        .map(|v| v as i32)
                        .unwrap_or(0);

                    let pending_instances = json_value
                        .get("pendingInstances")
                        .and_then(|v| v.as_i64())
                        .map(|v| v as i32)
                        .unwrap_or(0);

                    let allocating_instances = json_value
                        .get("allocatingInstances")
                        .and_then(|v| v.as_i64())
                        .map(|v| v as i32)
                        .unwrap_or(0);

                    let terminating_instances = json_value
                        .get("terminatingInstances")
                        .and_then(|v| v.as_i64())
                        .map(|v| v as i32)
                        .unwrap_or(0);
                    let duration = start_time.elapsed().as_secs_f64() * 1000.0;
                    metrics::record_nvcf_api_request("scale_function", "SUCCESS", duration);
                    Ok(AutoscalerResponse {
                        active_instances,
                        pending_instances,
                        allocating_instances,
                        terminating_instances,
                        function_status,
                    })
                }
            }
        } else {
            let duration = start_time.elapsed().as_secs_f64() * 1000.0;
            let error = NvcfApiError::UnknownError;
            metrics::record_nvcf_api_request("scale_function", &error.to_short_string(), duration);
            Err(error)
        }
    }

    async fn process_single_request(
        info: DeploymentInfo,
        ctx: &ProcessRequestCtx<'_>,
    ) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
        let rate_limiter = ctx.rate_limiter;
        let nvcf_api_url = ctx.nvcf_api_url;
        let nvcf_api_client = ctx.nvcf_api_client;
        let oauth2_client = ctx.oauth2_client;
        let cassandra_service = ctx.cassandra_service;
        let function_state_cache = ctx.function_state_cache;
        let dry_run = ctx.dry_run;
        // Check if request is stale (older than 15 seconds)
        if info.enqueued_at.elapsed() > std::time::Duration::from_secs(15) {
            tracing::debug!(
                "Discarding stale request for function {} version {} (enqueued {} seconds ago)",
                info.function_id,
                info.function_version_id,
                info.enqueued_at.elapsed().as_secs()
            );
            return Ok(());
        }

        rate_limiter.acquire(1).await;
        let mut error_code = None;
        if dry_run {
            tracing::info!(
                "Dry run result: FunctionID: {} VersionID: {} RequiredInstances: {}",
                info.function_id,
                info.function_version_id,
                info.required_number_of_instances
            );
        } else {
            // Log exactly what we're sending to NVCF
            tracing::info!(
                "NVCF API REQUEST: function_id={}, version_id={}, required_instances={}",
                info.function_id,
                info.function_version_id,
                info.required_number_of_instances
            );
            // Process the request
            let result = Self::scale_function_internal_static(
                nvcf_api_url,
                nvcf_api_client,
                oauth2_client,
                info.clone(),
            )
            .await;

            // Log result and record metrics
            let mut num_workers_from_api: Option<i32> = None;
            match result {
                Ok(response) => {
                    // Record autoscaling status
                    metrics::record_autoscaling_status(
                        info.function_id.to_string(),
                        info.function_version_id.to_string(),
                        0_f64,
                    );

                    // Capture active_instances from API response for feedback loop
                    num_workers_from_api = Some(response.active_instances);

                    tracing::debug!(
                        "Successfully processed scaling request - Active: {}, Pending: {}, Allocating: {}, Terminating: {}, Status: {}",
                        response.active_instances,
                        response.pending_instances,
                        response.allocating_instances,
                        response.terminating_instances,
                        response.function_status
                    );
                }
                Err(e) => {
                    // Record failed processing
                    metrics::record_autoscaling_status(
                        info.function_id.to_string(),
                        info.function_version_id.to_string(),
                        e.into(),
                    );
                    error_code = Some(e.to_string());
                    tracing::error!("Failed to process scaling request: {}", e);
                    metrics::record_request_rejected();
                }
            }

            let active_function_details = ActiveFunctionDetails {
                function_id: info.function_id,
                function_version_id: info.function_version_id,
                nca_id: Some(info.nca_id),
                last_updated_at: Some(Utc::now()),
                num_workers: num_workers_from_api,
                last_predicted_desired_instance_count: Some(info.required_number_of_instances),
                last_predicted_error_code: error_code.clone(),
            };

            // Update in-memory cache with the latest prediction result
            if let Some(cache) = function_state_cache {
                cache.insert(
                    (info.function_id, info.function_version_id),
                    FunctionCachedState {
                        last_predicted_desired_instance_count: Some(
                            info.required_number_of_instances,
                        ),
                        last_predicted_error_code: error_code,
                    },
                );
            }

            // Handle Cassandra operations if available
            if let Some(cassandra_service) = &cassandra_service {
                let table = if info.recently_invoked {
                    ActiveFunctionTable::RecentlyInvokedFunctions
                } else {
                    ActiveFunctionTable::RunningFunctionsWithoutInvocations
                };

                if let Err(cassandra_error) = cassandra_service
                    .insert_to_active_function_history_prediction_row(
                        &active_function_details,
                        table,
                    )
                    .await
                {
                    tracing::error!(
                        "Failed to report error to Cassandra for function {} version {}: {}",
                        info.function_id,
                        info.function_version_id,
                        cassandra_error,
                    );
                }
            }
        }

        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use uuid::Uuid;

    use super::*;

    #[tokio::test]
    async fn test_function_status_display() {
        assert_eq!(FunctionStatus::ACTIVE.to_string(), "ACTIVE");
        assert_eq!(FunctionStatus::DEPLOYING.to_string(), "DEPLOYING");
    }

    #[tokio::test]
    async fn test_autoscaler_request_serialization() {
        let request = AutoscalerRequest {
            required_number_of_instances: 5,
            predicted_at: Utc::now(),
        };

        let json = serde_json::to_string(&request).unwrap();
        assert!(json.contains("requiredNumberOfInstances"));
    }

    #[tokio::test]
    async fn test_autoscaler_response_parsing() {
        let response_str = r#"
        {
            "activeInstances": 1,
            "pendingInstances": 0,
            "allocatingInstances": 0,
            "terminatingInstances": 0,
            "functionStatus": "ACTIVE"
        }"#;
        let response = AutoscalerResponse {
            active_instances: 1,
            pending_instances: 0,
            allocating_instances: 0,
            terminating_instances: 0,
            function_status: FunctionStatus::ACTIVE,
        };
        let parsed_response: AutoscalerResponse = serde_json::from_str(response_str).unwrap();
        assert_eq!(parsed_response.active_instances, response.active_instances);
        assert_eq!(
            parsed_response.pending_instances,
            response.pending_instances
        );
        assert_eq!(
            parsed_response.allocating_instances,
            response.allocating_instances
        );
        assert_eq!(
            parsed_response.terminating_instances,
            response.terminating_instances
        );
        assert_eq!(parsed_response.function_status, response.function_status);
    }

    #[tokio::test]
    async fn test_background_processing_functionality() {
        // Create a minimal test service
        let nvcf_settings = NvcfApiSettings::default();
        let temp_dir = tempfile::TempDir::new().unwrap();
        let secrets_file = temp_dir.path().join("secrets.json");

        // Create mock SSL certificate files
        let ca_cert_file = temp_dir.path().join("ca_cert.pem");
        let client_cert_file = temp_dir.path().join("client_cert.pem");
        let client_key_file = temp_dir.path().join("client_key.pem");

        std::fs::write(&ca_cert_file, "test_ca_cert_content").unwrap();
        std::fs::write(&client_cert_file, "test_client_cert_content").unwrap();
        std::fs::write(&client_key_file, "test_client_key_content").unwrap();

        // Create test secrets file
        let secrets_content = format!(
            r#"
        {{
            "kv": {{
                "nvcf_api": {{
                    "client_id": "test_client_id",
                    "client_secret": "test_client_secret"
                }},
                "cassandra": {{
                    "username": "test_user",
                    "password": "test_password",
                    "ca_cert": "{}",
                    "client_cert": "{}",
                    "client_key": "{}"
                }}
            }}
        }}
        "#,
            ca_cert_file.display(),
            client_cert_file.display(),
            client_key_file.display()
        );
        std::fs::write(&secrets_file, secrets_content).unwrap();

        let _secrets_watcher = Arc::new(SecretFileWatcher::new(&secrets_file).await.unwrap());
        let _node_id = "test_node_id".to_string();

        // Skip creating the bucket manager entirely for this test
        // Create service with empty bucket receivers since we're just testing basic functionality
        let (bucket_senders, _bucket_receivers): (HashMap<_, _>, HashMap<_, _>) = (0..10)
            .map(|bucket_index| {
                let (sender, receiver) = mpsc::channel(100);
                ((bucket_index, sender), (bucket_index, receiver))
            })
            .unzip();

        let cancellation_token = CancellationToken::new();
        let drop_guard = cancellation_token.clone().drop_guard();
        let service = NvcfApiService {
            nvcf_api_url: nvcf_settings.nvcf_api_address,
            nvcf_api_client: reqwest_middleware::ClientBuilder::new(reqwest::Client::new())
                .with(reqwest_tracing::TracingMiddleware::default())
                .build(),
            cassandra_service: None,
            function_state_cache: None,
            oauth2_client: None,
            per_bucket_request_queue: bucket_senders,
            rate_limiter: Arc::new(
                RateLimiter::builder()
                    .max(100)
                    .initial(100)
                    .refill(10)
                    .interval(Duration::from_millis(100))
                    .build(),
            ),
            _drop_guard: drop_guard,
            queue_processor_handles: Vec::new(),
        };

        // Test that the service was created successfully
        assert!(!service.per_bucket_request_queue.is_empty());
        assert!(service.nvcf_api_url.contains("localhost"));

        // Service will shutdown automatically when dropped
    }

    #[tokio::test]
    async fn test_staleness_check_fresh_request() {
        // Test that fresh requests (< 15s) are not considered stale
        let deployment_info = DeploymentInfo {
            function_id: Uuid::new_v4(),
            function_version_id: Uuid::new_v4(),
            nca_id: "test-nca-id".to_string(),
            required_number_of_instances: 1,
            recently_invoked: false,
            enqueued_at: std::time::Instant::now(), // Fresh timestamp
        };

        // Simulate the staleness check logic
        let is_stale = deployment_info.enqueued_at.elapsed() > std::time::Duration::from_secs(15);

        assert!(!is_stale, "Fresh request should not be considered stale");
    }

    #[tokio::test]
    async fn test_staleness_check_stale_request() {
        // Test that old requests (> 15s) are considered stale
        let old_time = std::time::Instant::now() - std::time::Duration::from_secs(20);
        let deployment_info = DeploymentInfo {
            function_id: Uuid::new_v4(),
            function_version_id: Uuid::new_v4(),
            nca_id: "test-nca-id".to_string(),
            required_number_of_instances: 1,
            recently_invoked: false,
            enqueued_at: old_time, // Old timestamp
        };

        // Simulate the staleness check logic
        let is_stale = deployment_info.enqueued_at.elapsed() > std::time::Duration::from_secs(15);

        assert!(is_stale, "Old request should be considered stale");
    }

    #[tokio::test]
    async fn test_staleness_check_edge_case() {
        // Test edge case around 15 seconds
        let almost_stale_time = std::time::Instant::now() - std::time::Duration::from_secs(14);
        let deployment_info = DeploymentInfo {
            function_id: Uuid::new_v4(),
            function_version_id: Uuid::new_v4(),
            nca_id: "test-nca-id".to_string(),
            required_number_of_instances: 1,
            recently_invoked: false,
            enqueued_at: almost_stale_time, // Just under 15s
        };

        // Simulate the staleness check logic
        let is_stale = deployment_info.enqueued_at.elapsed() > std::time::Duration::from_secs(15);

        assert!(!is_stale, "Request just under 15s should not be stale");
    }

    #[tokio::test]
    async fn test_staleness_check_boundary() {
        // Test boundary case - use 16 seconds to ensure it's clearly stale
        let stale_time = std::time::Instant::now() - std::time::Duration::from_secs(16);
        let deployment_info = DeploymentInfo {
            function_id: Uuid::new_v4(),
            function_version_id: Uuid::new_v4(),
            nca_id: "test-nca-id".to_string(),
            required_number_of_instances: 1,
            recently_invoked: false,
            enqueued_at: stale_time, // Clearly stale
        };

        // Simulate the staleness check logic
        let is_stale = deployment_info.enqueued_at.elapsed() > std::time::Duration::from_secs(15);

        // At 16 seconds, it should be considered stale
        assert!(is_stale, "Request older than 15s should be stale");
    }

    #[tokio::test]
    async fn test_deployment_info_serialization() {
        // Test that DeploymentInfo with the new timestamp field works correctly
        let deployment_info = DeploymentInfo {
            function_id: Uuid::parse_str("550e8400-e29b-41d4-a716-446655440000").unwrap(),
            function_version_id: Uuid::parse_str("550e8400-e29b-41d4-a716-446655440001").unwrap(),
            nca_id: "CMYBKSNNjtg1TQmSke-gHNGgMlFvA-dCRAI8gcHOBcw".to_string(),
            required_number_of_instances: 5,
            recently_invoked: true,
            enqueued_at: std::time::Instant::now(),
        };

        // Verify all fields are accessible
        assert_eq!(deployment_info.required_number_of_instances, 5);
        assert!(deployment_info.recently_invoked);
        // Verify timestamp is recent (less than 1 second old)
        assert!(deployment_info.enqueued_at.elapsed() < std::time::Duration::from_secs(1));
    }

    #[tokio::test]
    async fn test_rate_limiting_behavior() {
        use leaky_bucket::RateLimiter;

        // Test rate limiting behavior directly
        let rate_limiter = Arc::new(
            RateLimiter::builder()
                .max(5) // 5 token capacity
                .refill(1) // 1 token per interval
                .interval(Duration::from_millis(1000)) // 1 second interval
                .build(),
        );

        // Consume tokens rapidly to test rate limiting
        let mut successful_acquisitions = 0;
        for i in 0..10 {
            if rate_limiter.try_acquire(1) {
                successful_acquisitions += 1;
                tracing::debug!("Successfully acquired token {}", i + 1);
            } else {
                tracing::debug!("Failed to acquire token {} - rate limited", i + 1);
                break;
            }
        }

        // Should only be able to acquire up to the bucket capacity (5 tokens)
        assert!(
            successful_acquisitions <= 5,
            "Expected to acquire at most 5 tokens, but acquired {}",
            successful_acquisitions
        );

        // Check rate limiter status
        let available_tokens = rate_limiter.balance();
        let max_tokens = rate_limiter.max();
        let utilization = (1.0 - (available_tokens as f64 / max_tokens as f64)) * 100.0;

        // Should have consumed some tokens
        assert!(
            available_tokens < max_tokens,
            "Expected tokens ({}) to be less than capacity ({})",
            available_tokens,
            max_tokens
        );

        // Utilization should be greater than 0
        assert!(
            utilization > 0.0,
            "Expected utilization ({}) to be greater than 0",
            utilization
        );

        println!(
            "Rate limiter status: {}/{} tokens available, {:.1}% utilization",
            available_tokens, max_tokens, utilization
        );

        // Test that tokens refill over time - wait longer to ensure refill happens
        tokio::time::sleep(Duration::from_millis(1500)).await; // Wait for refill

        let available_after_refill = rate_limiter.balance();
        assert!(
            available_after_refill >= available_tokens,
            "Expected tokens to refill or stay same over time. Before: {}, After: {}",
            available_tokens,
            available_after_refill
        );

        println!(
            "After refill: {}/{} tokens available",
            available_after_refill, max_tokens
        );

        // The key test is that we successfully demonstrated rate limiting occurred
        assert!(
            successful_acquisitions <= 5,
            "Rate limiting test passed: acquired {} tokens out of 10 attempts (max capacity: 5)",
            successful_acquisitions
        );
    }

    #[tokio::test]
    async fn test_queue_capacity_limits() {
        // Create a test service with small queue capacity
        let nvcf_settings = NvcfApiSettings::default();
        let temp_dir = tempfile::TempDir::new().unwrap();
        let secrets_file = temp_dir.path().join("secrets.json");

        // Create mock SSL certificate files
        let ca_cert_file = temp_dir.path().join("ca_cert.pem");
        let client_cert_file = temp_dir.path().join("client_cert.pem");
        let client_key_file = temp_dir.path().join("client_key.pem");

        tokio::fs::write(&ca_cert_file, "test_ca_cert_content")
            .await
            .unwrap();
        tokio::fs::write(&client_cert_file, "test_client_cert_content")
            .await
            .unwrap();
        tokio::fs::write(&client_key_file, "test_client_key_content")
            .await
            .unwrap();

        let secrets_content = format!(
            r#"
        {{
            "kv": {{
                "nvcf_api": {{
                    "client_id": "test_client_id",
                    "client_secret": "test_client_secret"
                }},
                "cassandra": {{
                    "username": "test_user",
                    "password": "test_password",
                    "ca_cert": "{}",
                    "client_cert": "{}",
                    "client_key": "{}"
                }}
            }}
        }}
        "#,
            ca_cert_file.display(),
            client_cert_file.display(),
            client_key_file.display()
        );
        tokio::fs::write(&secrets_file, secrets_content)
            .await
            .unwrap();

        let _secrets_watcher = Arc::new(SecretFileWatcher::new(&secrets_file).await.unwrap());

        // Create test service directly without Cassandra dependencies
        let (bucket_senders, _bucket_receivers): (HashMap<_, _>, HashMap<_, _>) = (0..1024)
            .map(|bucket_index| {
                let (sender, receiver) = mpsc::channel(3);
                ((bucket_index, sender), (bucket_index, receiver))
            })
            .unzip();

        let cancellation_token = CancellationToken::new();
        let drop_guard = cancellation_token.clone().drop_guard();

        let service = NvcfApiService {
            nvcf_api_url: nvcf_settings.nvcf_api_address,
            nvcf_api_client: reqwest_middleware::ClientBuilder::new(reqwest::Client::new())
                .with(reqwest_tracing::TracingMiddleware::default())
                .build(),
            cassandra_service: None,
            function_state_cache: None,
            oauth2_client: None,
            per_bucket_request_queue: bucket_senders,
            rate_limiter: Arc::new(
                RateLimiter::builder()
                    .max(10)
                    .initial(10)
                    .refill(1)
                    .interval(Duration::from_millis(1000))
                    .build(),
            ),
            _drop_guard: drop_guard,
            queue_processor_handles: Vec::new(),
        };

        // Create test deployment
        let old_time = std::time::Instant::now() - std::time::Duration::from_secs(20);
        let deployment = DeploymentInfo {
            function_id: Uuid::new_v4(),
            function_version_id: Uuid::new_v4(),
            nca_id: "test-nca-id".to_string(),
            required_number_of_instances: 1,
            recently_invoked: false,
            enqueued_at: old_time,
        };

        // Try to queue more requests than the queue capacity
        let mut successful_queues = 0;
        let mut failed_queues = 0;
        let mut queue_full_errors = 0;

        for i in 0..6 {
            let test_deployment = deployment.clone();

            match service.queue_scaling_request(0, test_deployment) {
                Ok(()) => {
                    successful_queues += 1;
                    tracing::info!("Successfully queued request {}", i);
                }
                Err(NvcfApiError::QueueFull) => {
                    failed_queues += 1;
                    queue_full_errors += 1;
                    tracing::error!("Request {} rejected: Queue is full", i);
                }
                Err(e) => {
                    failed_queues += 1;
                    tracing::error!("Request {} failed with unexpected error: {}", i, e);
                }
            }
        }

        // Should only succeed for the first 3 requests (queue capacity)
        assert_eq!(successful_queues, 3, "Expected exactly 3 successful queues");
        assert_eq!(
            failed_queues, 3,
            "Expected exactly 3 failed queues due to capacity"
        );
        assert_eq!(queue_full_errors, 3, "Expected exactly 3 QueueFull errors");

        println!(
            "Queue test completed: {}/{} requests queued successfully",
            successful_queues,
            successful_queues + failed_queues
        );
    }
}

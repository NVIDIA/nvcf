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

use crate::cassandra::distributed_lock::DistributedLockManager;
use crate::cassandra::statements::ActiveFunctionTable;
use crate::health::HealthStatus;
use crate::metrics;
use crate::models::NodeHealth;
use crate::nvcf_api::nvcf_client::NvcfApiService;
use crate::nvcf_api::{DeploymentInfo, NvcfApiError};
use crate::scaling::{get_desired_instances, ScalingDecision, ScalingSettings};
use crate::{
    cassandra::cassandra_service::CassandraServiceManager,
    timeseries_db::timeseries_db_client::TimeseriesDbClient,
};
use anyhow::Result;
use chrono::{Duration, Utc};
use moka::sync::Cache;

use std::sync::Arc;
use std::time::Duration as StdDuration;
use tokio::sync::Semaphore;
use tokio::task::JoinSet;
use tracing;
use uuid::Uuid;

#[derive(Clone)]
pub struct FunctionCachedState {
    pub last_predicted_desired_instance_count: Option<i32>,
    pub last_predicted_error_code: Option<String>,
}

pub type FunctionStateCache = Cache<(Uuid, Uuid), FunctionCachedState>;

pub fn new_function_state_cache() -> FunctionStateCache {
    Cache::builder()
        .time_to_live(StdDuration::from_secs(5 * 60))
        .build()
}

pub mod bucket;
pub mod discovery;

use discovery::get_recently_invoked_functions;

const TIMESERIES_DB_QUERY_STEP: StdDuration = StdDuration::from_secs(60); // 1 minute step for TimeseriesDb queries
pub const CALCULATE_UTILIZATION_LOCK_PREFIX: &str = "util_lock";

/// Get historical utilization data for a specific function
#[allow(clippy::too_many_arguments)]
async fn get_function_utilization_history(
    timeseries_db_client: &TimeseriesDbClient,
    function_id: &Uuid,
    function_version_id: &Uuid,
    env: &str,
    use_control_plane_metrics: bool,
    ignore_env: bool,
    lookback_minutes: i64,
    utilization_window_seconds: u64,
) -> Result<Vec<(i64, String)>> {
    let end_time = Utc::now();
    let start_time = end_time - Duration::minutes(lookback_minutes);
    let step = TIMESERIES_DB_QUERY_STEP;

    let env_suffix = if ignore_env {
        String::new()
    } else {
        let env_val = if env == "stg" { "stage" } else { "prod" };
        format!(", environment=\"{}\"", env_val)
    };

    let query = if use_control_plane_metrics {
        format!(
            r#"100 * sum by(function_id, function_version_id, nca_id) (rate(function_request_latency_sum{{function_id="{id}", function_version_id="{v_id}"{env}}}[2m])) /
            (avg by(function_id, function_version_id, nca_id) (nvcf_function_instances_current{{function_id="{id}", function_version_id="{v_id}"{env}}}) * avg by(function_id, function_version_id, nca_id) (nvcf_function_concurrency{{function_id="{id}", function_version_id="{v_id}"{env}}})) or vector(0)"#,
            id = function_id,
            v_id = function_version_id,
            env = env_suffix
        )
    } else {
        format!(
            r#"((sum by(function_id, function_version_id, nca_id) (increase(nvcf_worker_service_worker_thread_busy_seconds_total{{function_id="{id}", function_version_id="{v_id}"{env}}}[{window}s]))) / {window} * 100) /
            (sum by(function_id, function_version_id, nca_id) (nvcf_worker_service_worker_thread_count_total{{function_id="{id}", function_version_id="{v_id}"{env}}}))"#,
            id = function_id,
            v_id = function_version_id,
            env = env_suffix,
            window = utilization_window_seconds
        )
    };
    tracing::debug!(
        "Executing utilization query for function {} version {} (ignore_env={}): {}",
        function_id,
        function_version_id,
        ignore_env,
        query
    );

    let response = timeseries_db_client
        .query_range(&query, start_time, end_time, step)
        .await?;

    // Extract the time series data
    let mut utilization_data = Vec::new();
    tracing::debug!(
        "Utilization query returned {} results for function {} version {}",
        response.data.result.len(),
        function_id,
        function_version_id
    );

    for result in &response.data.result {
        if let (Some(f_id), Some(f_v_id)) = (
            &result.metric.function_id,
            &result.metric.function_version_id,
        ) {
            // Verify this is the function we're looking for
            if f_id == &function_id.to_string() && f_v_id == &function_version_id.to_string() {
                tracing::debug!(
                    "Found matching function {}:{} with {} data points",
                    f_id,
                    f_v_id,
                    result.values.len()
                );
                utilization_data.extend(
                    result
                        .values
                        .iter()
                        .map(|(t, v)| (t.round() as i64, v.clone())),
                );
                break;
            }
        }
    }

    tracing::debug!(
        "Returning {} utilization data points for function {} version {}",
        utilization_data.len(),
        function_id,
        function_version_id
    );

    Ok(utilization_data)
}

/// Get current instance count for BYOC functions from TimeseriesDb
/// This queries the same metric used in utilization calculation for consistency
async fn get_byoc_instance_count(
    timeseries_db_client: &TimeseriesDbClient,
    function_id: &Uuid,
    function_version_id: &Uuid,
    env: &str,
    ignore_env: bool,
) -> Result<usize> {
    let env_suffix = if ignore_env {
        "".to_string()
    } else {
        format!(", env=\"{}\"", env)
    };

    let query = format!(
        r#"avg by(function_id, function_version_id, nca_id) (nvcf_function_instances_current{{function_id="{id}", function_version_id="{v_id}"{env}}})"#,
        id = function_id,
        v_id = function_version_id,
        env = env_suffix
    );

    // Use query_range with a 1-minute window to get the latest value
    let end_time = Utc::now();
    let start_time = end_time - chrono::Duration::seconds(60);
    let step = std::time::Duration::from_secs(60);

    tracing::info!(
        "BYOC instance count query for {}:{}: {}",
        function_id,
        function_version_id,
        query
    );

    let response = timeseries_db_client
        .query_range(&query, start_time, end_time, step)
        .await?;

    tracing::info!(
        "BYOC instance count response for {}:{}: {} results",
        function_id,
        function_version_id,
        response.data.result.len()
    );

    // Extract instance count from response - get the latest value
    for (idx, result) in response.data.result.iter().enumerate() {
        tracing::info!(
            "BYOC result[{}] for {}:{}: metric={:?}, values={:?}",
            idx,
            function_id,
            function_version_id,
            result.metric,
            result.values
        );

        if let (Some(f_id), Some(f_v_id)) = (
            &result.metric.function_id,
            &result.metric.function_version_id,
        ) {
            if f_id == &function_id.to_string() && f_v_id == &function_version_id.to_string() {
                // Get the last value from the time series (tuple of timestamp, value_string)
                if let Some((timestamp, value_str)) = result.values.last() {
                    if let Ok(count) = value_str.parse::<f64>() {
                        tracing::info!(
                            "BYOC instance count for {}:{} from TimeseriesDb: {} (timestamp: {}, raw: {})",
                            function_id,
                            function_version_id,
                            count.round() as usize,
                            timestamp,
                            value_str
                        );
                        return Ok(count.round() as usize);
                    }
                }
            }
        }
    }

    tracing::debug!(
        "No instance count data from TimeseriesDb for BYOC function {}:{}, returning 0",
        function_id,
        function_version_id
    );
    // No data found - return 0
    Ok(0)
}

/// Get current worker count for non-BYOC functions from TimeseriesDb.
/// We use this instead of details.num_workers because the history table is only updated when
/// discovery adds or moves a function; if a function stays in the same table, num_workers is
/// never refreshed and scaling would report stale counts (e.g. 2 when TimeseriesDb shows 3).
///
/// We do not filter or group by nca_id in the query (same as utilization and BYOC queries).
async fn get_current_worker_count_from_timeseries_db(
    timeseries_db_client: &TimeseriesDbClient,
    function_id: &Uuid,
    function_version_id: &Uuid,
    nca_id: &str,
    env: &str,
    ignore_env: bool,
) -> Result<usize> {
    let env_filter = if ignore_env {
        String::new()
    } else {
        let environment = if env == "stg" { "stage" } else { "prod" };
        format!(r#", environment="{}""#, environment)
    };
    let query = format!(
        r#"count by(function_id, function_version_id) (nvcf_worker_service_worker_thread_count_total{{function_id="{}", function_version_id="{}"{}}})"#,
        function_id, function_version_id, env_filter
    );

    const STEP_SECS: i64 = 60;
    // Align end to the last completed step so evaluation times are deterministic and the last
    // point is at a step boundary (avoids clock skew / off-minute issues with Prometheus step).
    let now_secs = Utc::now().timestamp();
    let end_secs = (now_secs / STEP_SECS) * STEP_SECS;
    let end_time = chrono::DateTime::from_timestamp(end_secs, 0).unwrap_or_else(Utc::now);
    let start_time = end_time - Duration::seconds(5 * STEP_SECS);
    let step = StdDuration::from_secs(STEP_SECS as u64);

    let response = timeseries_db_client
        .query_range(&query, start_time, end_time, step)
        .await?;

    for result in &response.data.result {
        if let Some((_, value_str)) = result.values.last() {
            if let Ok(count) = value_str.parse::<f64>() {
                let n = count.round() as usize;
                tracing::debug!(
                    "Worker count from TimeseriesDb for {}:{} (nca_id={}): {}",
                    function_id,
                    function_version_id,
                    nca_id,
                    n
                );
                return Ok(n);
            }
        }
    }

    tracing::warn!(
        "No worker count from TimeseriesDb for {}:{} (nca_id={}) — no matching series or empty values; reporting 0",
        function_id,
        function_version_id,
        nca_id
    );
    Ok(0)
}

// Function that creates or removes our node entry in Cassandra based on readiness.
// When healthy we insert (or refresh TTL); when unhealthy we delete so we're not in the
// healthy_nodes list and get no bucket assignment (no processing).
pub async fn create_new_node(
    node_id: &str,
    health: &HealthStatus,
    cassandra_service: &CassandraServiceManager,
) -> Result<()> {
    if health == &HealthStatus::Healthy {
        cassandra_service
            .insert_to_nodes(&NodeHealth {
                node_id: node_id.to_string(),
                last_updated_at: Utc::now(),
            })
            .await?;
        tracing::info!("Created new node entry for node: {}", node_id);
    } else {
        cassandra_service.delete_node(node_id).await?;
        tracing::info!(
            "Removed node {} from healthy_nodes (not ready); no processing will be assigned",
            node_id
        );
    }
    Ok(())
}

// Function that runs the P0 autoscaling logic. Called every 30 seconds.
#[allow(clippy::too_many_arguments)]
pub async fn run_autoscaling_logic_p0(
    cassandra_service: Arc<CassandraServiceManager>,
    timeseries_db_client: Arc<TimeseriesDbClient>,
    nvcf_api_service: Arc<NvcfApiService>,
    scaling_settings: Arc<ScalingSettings>,
    env: &str,
    ignore_env: bool,
    bucket_manager: &bucket::NodeBucketManager,
    lock_manager: &DistributedLockManager,
    function_state_cache: Arc<FunctionStateCache>,
) -> Result<()> {
    tracing::info!("Starting P0 autoscaling logic for env: {}", env);

    // Process recently invoked functions (P0 priority)
    make_scaling_requests_for_table(
        cassandra_service,
        timeseries_db_client,
        nvcf_api_service,
        ActiveFunctionTable::RecentlyInvokedFunctions,
        true, // recently_invoked = true
        scaling_settings,
        env,
        ignore_env,
        bucket_manager,
        lock_manager,
        function_state_cache,
    )
    .await?;

    Ok(())
}

// Function that makes scaling requests for a specific table
#[allow(clippy::too_many_arguments)]
async fn make_scaling_requests_for_table(
    cassandra_service: Arc<CassandraServiceManager>,
    timeseries_db_client: Arc<TimeseriesDbClient>,
    nvcf_api_service: Arc<NvcfApiService>,
    table: ActiveFunctionTable,
    recently_invoked: bool,
    scaling_settings: Arc<ScalingSettings>,
    env: &str,
    ignore_env: bool,
    bucket_manager: &bucket::NodeBucketManager,
    lock_manager: &DistributedLockManager,
    function_state_cache: Arc<FunctionStateCache>,
) -> Result<()> {
    let bucket_ranges = bucket_manager.get_all_bucket_ranges();

    if bucket_ranges.is_empty() {
        tracing::warn!("No buckets assigned to this node, skipping scaling logic");
        return Ok(());
    }

    let page_size = scaling_settings.cassandra_page_size;

    for (bucket_index, (start_token, end_token)) in bucket_ranges.iter() {
        let token_range = [*start_token, *end_token];
        let functions_in_bucket = cassandra_service
            .get_active_functions_with_token_range(&token_range, page_size, table)
            .await?;

        tracing::info!(
            "Processing {} functions in bucket {} for table {:?}",
            functions_in_bucket.len(),
            bucket_index,
            table
        );

        if functions_in_bucket.is_empty() {
            continue; // Skip to next bucket if no functions
        }

        // Try to acquire distributed lock for this bucket
        let lock_name = format!(
            "{}_{}_{:?}",
            CALCULATE_UTILIZATION_LOCK_PREFIX, bucket_index, table
        );
        if let Ok(Some(_lock_guard)) = lock_manager
            .try_acquire(
                lock_name,
                scaling_settings.utilization_lock_duration.as_secs() as i32,
            )
            .await
        {
            tracing::debug!(
                "Acquired lock for bucket {}, processing {} functions",
                bucket_index,
                functions_in_bucket.len()
            );

            // Use a semaphore to limit concurrent scaling requests within this bucket
            let semaphore = Arc::new(Semaphore::new(
                scaling_settings.concurrent_scaling_per_bucket,
            ));
            let mut join_set: JoinSet<Result<()>> = JoinSet::new();

            // Process all functions in this bucket
            for function in functions_in_bucket {
                let permit = semaphore.clone().acquire_owned().await.unwrap();
                let cassandra_service = cassandra_service.clone();
                let timeseries_db_client = timeseries_db_client.clone();
                let nvcf_api_service = nvcf_api_service.clone();
                let scaling_settings = scaling_settings.clone();
                let function_state_cache = function_state_cache.clone();
                let env = env.to_string();
                let bucket_index = *bucket_index;

                join_set.spawn(async move {
                    let _permit = permit; // Keep the permit alive for the duration of this task

                    // Look up cached state from the previous scaling cycle (written by nvcf_client
                    // after each NVCF API call). None means we haven't successfully called NVCF yet.
                    let cached: Option<FunctionCachedState> = function_state_cache
                        .get(&(function.function_id, function.function_version_id));

                    // Check if this is a BYOC function (NCA ID is in the filter list)
                    // BYOC functions use utilization-based scaling even with unknown worker count
                    let nca_id_str = function.nca_id.as_str();
                    let uses_cp_metrics = scaling_settings.has_accounts_without_worker_metrics()
                        && scaling_settings.is_account_without_worker_metrics(nca_id_str);

                    tracing::debug!(
                        "Scaling path for {}:{} - account: '{}', has_accounts_without_worker_metrics: {}, uses_cp_metrics: {}",
                        function.function_id,
                        function.function_version_id,
                        nca_id_str,
                        scaling_settings.has_accounts_without_worker_metrics(),
                        uses_cp_metrics,
                    );

                    // Get current instance count from TimeseriesDb
                    let current_instances = if uses_cp_metrics {
                        match get_byoc_instance_count(
                            &timeseries_db_client,
                            &function.function_id,
                            &function.function_version_id,
                            &env,
                            ignore_env,
                        )
                        .await
                        {
                            Ok(n) => n,
                            Err(e) => {
                                tracing::warn!(
                                    "TimeseriesDb BYOC instance count failed for {}:{}, using 0: {}",
                                    function.function_id,
                                    function.function_version_id,
                                    e
                                );
                                0
                            }
                        }
                    } else {
                        match get_current_worker_count_from_timeseries_db(
                            &timeseries_db_client,
                            &function.function_id,
                            &function.function_version_id,
                            nca_id_str,
                            &env,
                            ignore_env,
                        )
                        .await
                        {
                            Ok(n) => n,
                            Err(e) => {
                                tracing::warn!(
                                    "TimeseriesDb worker count failed for {}:{} (nca_id={}), using 0: {}",
                                    function.function_id,
                                    function.function_version_id,
                                    nca_id_str,
                                    e
                                );
                                0
                            }
                        }
                    };

                    tracing::info!(
                        "Current instances for {}:{} = {} (uses_cp_metrics: {}, source: {})",
                        function.function_id,
                        function.function_version_id,
                        current_instances,
                        uses_cp_metrics,
                        if uses_cp_metrics { "TimeseriesDb nvcf_function_instances_current" } else { "TimeseriesDb nvcf_worker_service_worker_thread_count_total" }
                    );

                    // Get utilization history for scaling calculation
                    let utilization_history = get_function_utilization_history(
                        &timeseries_db_client,
                        &function.function_id,
                        &function.function_version_id,
                        &env,
                        uses_cp_metrics,
                        ignore_env,
                        scaling_settings.lookback.as_secs() as i64 / 60,
                        scaling_settings.utilization_window_seconds,
                    ).await?;

                    // Check for recent invocations (30-minute timeout for scale-to-zero)
                    let recent_invocations = get_recently_invoked_functions(
                        &timeseries_db_client,
                        Some(function.function_version_id),
                        scaling_settings.scale_to_zero_idle_timeout.as_secs() as i64 / 60,
                        env.as_str(),
                        ignore_env,
                    ).await?;

                    // Always compute utilization first — needed for both scaling decisions and scale-to-zero guard
                    let last_predicted_instance_count = cached
                        .as_ref()
                        .and_then(|c| c.last_predicted_desired_instance_count)
                        .unwrap_or(0) as usize;

                    let scaling_base_instances = match current_instances {
                        0 if last_predicted_instance_count > 0 => {
                            tracing::info!(
                                "Function {}:{} has {} requested instances but 0 active - waiting for workers to come up",
                                function.function_id, function.function_version_id, last_predicted_instance_count
                            );
                            return Ok(());
                        }
                        0 => 1,
                        n => n,
                    };

                    let policy = scaling_settings
                        .get_policy_for_function(&function.function_version_id)
                        .await;
                    let scaling_decision = if let Some(decision) = get_desired_instances(
                        utilization_history.clone(),
                        &policy,
                        scaling_base_instances,
                        scaling_settings.decay_factor,
                    ) {
                        tracing::debug!(
                            "Scaling decision for function {}:{} - current_instances: {}, scaling_base: {}, desired_instances: {}, average_utilization: {:.6}%",
                            function.function_id, function.function_version_id,
                            current_instances, scaling_base_instances, decision.desired_instances, decision.average_utilization
                        );
                        decision
                    } else {
                        tracing::warn!(
                            "Failed to calculate scaling decision for function {}:{}, using fallback",
                            function.function_id,
                            function.function_version_id
                        );
                        ScalingDecision {
                            desired_instances: last_predicted_instance_count.max(1),
                            average_utilization: 0.0,
                        }
                    };

                    // Scale-to-zero: only if no recent invocations AND utilization is below scale-down threshold.
                    // This prevents killing active workers when the invocation metric has gaps.
                    let (raw_desired_instance_count, utilization) = if recent_invocations.is_empty()
                        && scaling_decision.average_utilization < policy.thresholds.scale_down_threshold
                    {
                        if current_instances >= 1 {
                            tracing::info!(
                                "Function {}:{} has no invocations in 30 minutes and low utilization ({:.1}% < {:.1}%) - scaling to 0",
                                function.function_id,
                                function.function_version_id,
                                scaling_decision.average_utilization,
                                policy.thresholds.scale_down_threshold,
                            );
                        } else {
                            tracing::info!(
                                "Function {}:{} has 0 instances with no invocations - staying at 0",
                                function.function_id,
                                function.function_version_id,
                            );
                        }
                        (0, Some(scaling_decision.average_utilization as f64))
                    } else if recent_invocations.is_empty() {
                        // No invocations but utilization is still high — don't scale to zero, use utilization-based decision
                        tracing::info!(
                            "Function {}:{} has no invocations in 30 minutes but utilization is {:.1}% (>= {:.1}%) - NOT scaling to zero",
                            function.function_id,
                            function.function_version_id,
                            scaling_decision.average_utilization,
                            policy.thresholds.scale_down_threshold,
                        );
                        (
                            scaling_decision.desired_instances as i32,
                            Some(scaling_decision.average_utilization as f64),
                        )
                    } else {
                        (
                            scaling_decision.desired_instances as i32,
                            Some(scaling_decision.average_utilization as f64),
                        )
                    };

                    let desired_instance_count = if raw_desired_instance_count < 0 {
                        tracing::warn!(
                            "Negative desired instance count ({}) for function {}:{}, clamping to 0",
                            raw_desired_instance_count,
                            function.function_id,
                            function.function_version_id
                        );
                        0
                    } else {
                        raw_desired_instance_count
                    };

                    tracing::debug!(
                        "Recording metrics for function {}:{} - current: {}, desired: {}, utilization: {}",
                        function.function_id,
                        function.function_version_id,
                        current_instances,
                        desired_instance_count,
                        utilization
                            .map(|u| format!("{:.6}%", u))
                            .unwrap_or_else(|| "unknown".to_string())
                    );

                    metrics::record_scaling_decision(
                        function.function_id.to_string(),
                        function.function_version_id.to_string(),
                        current_instances,
                        desired_instance_count as usize,
                        utilization,
                    );

                    // Renew TTL so the function stays in the active set as long as instances exist.
                    // If desired is 0 we let the row expire naturally — no explicit delete needed.
                    if desired_instance_count > 0 {
                        if let Err(e) = cassandra_service.refresh_active_function_ttl(&function).await {
                            tracing::warn!(
                                "Failed to refresh TTL for function {}:{}: {}",
                                function.function_id,
                                function.function_version_id,
                                e
                            );
                        }
                    }

                    // Check if we should skip this scaling request
                    if should_skip_scaling_request(
                        function.function_id,
                        function.function_version_id,
                        cached.as_ref(),
                        desired_instance_count,
                    ) {
                        return Ok(());
                    }

                    let deployment_info = DeploymentInfo {
                        function_id: function.function_id,
                        function_version_id: function.function_version_id,
                        nca_id: function.nca_id.clone(),
                        required_number_of_instances: desired_instance_count,
                        recently_invoked,
                        enqueued_at: std::time::Instant::now(),
                    };

                    let result = nvcf_api_service.queue_scaling_request(bucket_index, deployment_info);
                    match result {
                        Ok(_) => {
                            metrics::record_request_processed();
                            Ok(())
                        }
                        Err(e) => {
                            metrics::record_request_rejected();
                            tracing::error!("Failed to process the scaling request for function version ID {}: {}", function.function_version_id, e);
                            Ok(())
                        }
                    }
                });
            }

            // Wait for all scaling requests in this bucket to complete
            while let Some(result) = join_set.join_next().await {
                if let Err(e) = result {
                    tracing::error!("Task failed in bucket {}: {}", bucket_index, e);
                }
            }

            tracing::debug!("Completed processing bucket {}", bucket_index);
        } else {
            tracing::info!(
                "Unable to acquire lock for bucket {}, skipping",
                bucket_index
            );
        }
    }

    Ok(())
}

// Function that determines if we should skip a scaling request
fn should_skip_scaling_request(
    function_id: Uuid,
    function_version_id: Uuid,
    cached: Option<&FunctionCachedState>,
    desired_instance_count: i32,
) -> bool {
    let last_predicted_error_code = cached
        .and_then(|c| c.last_predicted_error_code.as_deref())
        .unwrap_or_default();
    let last_predicted_desired_instance_count = cached
        .and_then(|c| c.last_predicted_desired_instance_count)
        .unwrap_or(-1);

    if last_predicted_error_code == NvcfApiError::FunctionNotFound.to_string() {
        tracing::info!(
            "Skipping scaling request for function {} version {} due to function not found",
            function_id,
            function_version_id
        );
        // TODO(csaikia): Send a metric
        return true;
    }

    if last_predicted_desired_instance_count == desired_instance_count {
        tracing::info!(
            "Skipping scaling request for function {} version {} due to duplicate scaling request",
            function_id,
            function_version_id
        );
        return true;
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;
    use uuid::Uuid;

    #[tokio::test]
    async fn test_deployment_info_includes_enqueued_timestamp() {
        // Test that DeploymentInfo is created with current timestamp
        let function_id = Uuid::new_v4();
        let function_version_id = Uuid::new_v4();
        let nca_id = "test-nca-id".to_string();
        let desired_instance_count = 3;
        let recently_invoked = true;

        let deployment_info = DeploymentInfo {
            function_id,
            function_version_id,
            nca_id,
            required_number_of_instances: desired_instance_count,
            recently_invoked,
            enqueued_at: std::time::Instant::now(),
        };

        // Verify the timestamp is recent (less than 1 second old)
        assert!(deployment_info.enqueued_at.elapsed() < std::time::Duration::from_secs(1));
        assert_eq!(deployment_info.function_id, function_id);
        assert_eq!(deployment_info.function_version_id, function_version_id);
        assert_eq!(
            deployment_info.required_number_of_instances,
            desired_instance_count
        );
        assert_eq!(deployment_info.recently_invoked, recently_invoked);
    }

    /// Scale-to-zero: DeploymentInfo with required_number_of_instances = 0 is valid
    /// (used when scaling to 0 after 30 minutes with no invocations).
    #[test]
    fn test_deployment_info_scale_to_zero_allows_zero_instances() {
        let deployment_info = DeploymentInfo {
            function_id: Uuid::new_v4(),
            function_version_id: Uuid::new_v4(),
            nca_id: String::new(),
            required_number_of_instances: 0,
            recently_invoked: false,
            enqueued_at: std::time::Instant::now(),
        };
        assert_eq!(deployment_info.required_number_of_instances, 0);
    }

    /// Scale-to-zero: we should not skip a scaling request when desired is 0 and last predicted was 1
    /// (explicit scale-to-zero from 1 instance after 30 min idle).
    #[test]
    fn test_should_not_skip_scale_to_zero_request() {
        let id = Uuid::new_v4();
        let vid = Uuid::new_v4();
        let cached = FunctionCachedState {
            last_predicted_desired_instance_count: Some(1),
            last_predicted_error_code: None,
        };
        assert!(!should_skip_scaling_request(id, vid, Some(&cached), 0));
    }

    #[test]
    fn test_lock_key_format() {
        let function_version_id = Uuid::parse_str("550e8400-e29b-41d4-a716-446655440001").unwrap();
        let expected_key = "function-version-id-550e8400-e29b-41d4-a716-446655440001";
        let actual_key = format!("function-version-id-{}", function_version_id);
        assert_eq!(actual_key, expected_key);
    }

    #[test]
    fn test_should_skip_scaling_request_logic() {
        let id = Uuid::new_v4();
        let vid = Uuid::new_v4();
        let cached = FunctionCachedState {
            last_predicted_desired_instance_count: Some(5),
            last_predicted_error_code: None,
        };

        // Should skip when desired count matches last predicted
        assert!(should_skip_scaling_request(id, vid, Some(&cached), 5));

        // Should not skip when desired count differs
        assert!(!should_skip_scaling_request(id, vid, Some(&cached), 3));
    }

    #[test]
    fn test_should_skip_scaling_request_with_function_not_found() {
        let id = Uuid::new_v4();
        let vid = Uuid::new_v4();
        let cached = FunctionCachedState {
            last_predicted_desired_instance_count: Some(3),
            last_predicted_error_code: Some("FUNCTION_NOT_FOUND".to_string()),
        };

        // Should skip when last error was FunctionNotFound
        assert!(should_skip_scaling_request(id, vid, Some(&cached), 5));
    }
}

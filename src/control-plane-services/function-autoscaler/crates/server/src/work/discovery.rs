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

use crate::cassandra::{
    cassandra_service::CassandraServiceManager, statements::ActiveFunctionTable,
};
use crate::metrics;
use crate::models::ActiveFunctionDetails;
use crate::timeseries_db::timeseries_db_client::TimeseriesDbClient;
use anyhow::Result;
use chrono::{Duration, Utc};
use std::collections::{HashMap, HashSet};
use std::sync::Arc;
use std::time::Duration as StdDuration;
use tokio::time::Instant;
use tracing;
use uuid::Uuid;

pub const LOCK_NAME_FUNCTION_DISCOVERY: &str = "function_discovery";

/// Lookback window (minutes) for "recently invoked" in discovery. Functions with no invocations
/// in this window are moved from recently_invoked to running_functions. Aligns with
/// recently_invoked_functions table TTL (300s): rows expire after 5 min if not re-inserted.
pub const DISCOVERY_RECENTLY_INVOKED_LOOKBACK_MINUTES: i64 = 5;

const QUERY_INVOCATION_SERVICE: &str = r#"(
    sum by (function_id, function_version_id, nca_id) (function_request{env_filter} > 0)
    and
    sum by (function_id, function_version_id, nca_id) (function_request{env_filter} unless function_request{env_filter} offset 5m)
    )
    or
    (
    sum by (function_id, function_version_id, nca_id) (increase(function_request{env_filter}[5m]) > 0)
)"#;

const QUERY_GRPC_PROXY: &str = r#"(
    sum by (function_id, function_version_id, nca_id) (function_request_total{env_filter} > 0)
    and
    sum by (function_id, function_version_id, nca_id) (function_request_total{env_filter} unless function_request_total{env_filter} offset 5m)
    )
    or
    (
    sum by (function_id, function_version_id, nca_id) (increase(function_request_total{env_filter}[5m]) > 0)
)"#;

#[derive(Debug)]
pub enum FunctionDiscoveryError {
    LockAcquisitionFailed,
    Other(anyhow::Error),
}

impl std::fmt::Display for FunctionDiscoveryError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            FunctionDiscoveryError::LockAcquisitionFailed => {
                write!(
                    f,
                    "Failed to acquire distributed lock for function discovery"
                )
            }
            FunctionDiscoveryError::Other(e) => {
                write!(f, "Function discovery failed: {}", e)
            }
        }
    }
}

impl std::error::Error for FunctionDiscoveryError {}

impl From<anyhow::Error> for FunctionDiscoveryError {
    fn from(e: anyhow::Error) -> Self {
        FunctionDiscoveryError::Other(e)
    }
}

fn get_timeseries_db_query(
    template: &str,
    env: &str,
    ignore_env: bool,
    function_version_filter: Option<Uuid>,
) -> String {
    match (ignore_env, function_version_filter) {
        (true, None) => template.replace("{env_filter}", ""),
        (false, None) => {
            let env_filter = format!(r#"{{aws_env="{}"}}"#, env);
            template.replace("{env_filter}", &env_filter)
        }
        (true, Some(fv)) => {
            let env_filter = format!(r#"{{function_version_id="{}"}}"#, fv);
            template.replace("{env_filter}", &env_filter)
        }
        (false, Some(fv)) => {
            let env_filter = format!(r#"{{function_version_id="{}", aws_env="{}"}}"#, fv, env);
            template.replace("{env_filter}", &env_filter)
        }
    }
}

/// Current state of functions across different sources.
/// Keys use only (function_id, function_version_id) to match the Cassandra PRIMARY KEY.
struct FunctionState {
    // What's currently in the DB
    db_recently_invoked: HashSet<(Uuid, Uuid)>,
}

/// What actions need to be taken
struct FunctionActions {
    add_recently_invoked: Vec<ActiveFunctionDetails>,
}

/// Step 2: Fetch current state from Cassandra and TimeseriesDb
async fn fetch_function_state(
    cassandra_service: &CassandraServiceManager,
    timeseries_db_client: &TimeseriesDbClient,
    env: &str,
    timeseries_db_ignore_env: bool,
) -> Result<(FunctionState, Vec<ActiveFunctionDetails>)> {
    let range = [i64::MIN, i64::MAX];
    let page_size = 2000;

    let db_recently_invoked = cassandra_service
        .get_active_functions_with_token_range(
            &range,
            page_size,
            ActiveFunctionTable::RecentlyInvokedFunctions,
        )
        .await?;

    tracing::info!("Getting recently invoked functions...");
    let timeseries_db_recently_invoked = get_recently_invoked_functions(
        timeseries_db_client,
        None,
        DISCOVERY_RECENTLY_INVOKED_LOOKBACK_MINUTES,
        env,
        timeseries_db_ignore_env,
    )
    .await?;
    tracing::debug!(
        "Got {} recently invoked functions",
        timeseries_db_recently_invoked.len()
    );

    tracing::info!("Getting running functions (workers + BYOC active instances)...");
    let timeseries_db_running_functions =
        get_functions_with_workers(timeseries_db_client, env, timeseries_db_ignore_env).await?;
    tracing::info!(
        "Got {} running functions (includes BYOC)",
        timeseries_db_running_functions.len()
    );

    // Union both TimeseriesDb sources — everything active belongs in the single table
    let mut timeseries_db_active_map: HashMap<(Uuid, Uuid), ActiveFunctionDetails> =
        timeseries_db_recently_invoked
            .into_iter()
            .map(|f| ((f.function_id, f.function_version_id), f))
            .collect();
    for f in timeseries_db_running_functions {
        timeseries_db_active_map
            .entry((f.function_id, f.function_version_id))
            .or_insert(f);
    }
    let timeseries_db_active_functions: Vec<ActiveFunctionDetails> =
        timeseries_db_active_map.into_values().collect();

    let state = FunctionState {
        db_recently_invoked: db_recently_invoked
            .iter()
            .map(|f| (f.function_id, f.function_version_id))
            .collect(),
    };

    Ok((state, timeseries_db_active_functions))
}

/// Step 3: Find TimeseriesDb-active functions not yet in the DB
fn analyze_function_actions(
    state: &FunctionState,
    timeseries_db_active_functions: &[ActiveFunctionDetails],
) -> FunctionActions {
    let add_recently_invoked = timeseries_db_active_functions
        .iter()
        .filter(|f| {
            let key = (f.function_id, f.function_version_id);
            if !state.db_recently_invoked.contains(&key) {
                tracing::debug!(
                    "New function {}:{} not in DB, will add to recently_invoked",
                    f.function_id,
                    f.function_version_id
                );
                true
            } else {
                false
            }
        })
        .cloned()
        .collect();

    FunctionActions {
        add_recently_invoked,
    }
}

/// Step 4: Execute the database changes
async fn execute_function_actions(
    cassandra_service: &CassandraServiceManager,
    actions: &FunctionActions,
) -> Result<()> {
    if actions.add_recently_invoked.is_empty() {
        return Ok(());
    }

    tracing::info!(
        "Adding {} new functions",
        actions.add_recently_invoked.len()
    );

    cassandra_service
        .add_new_active_functions_batch(
            &actions.add_recently_invoked,
            ActiveFunctionTable::RecentlyInvokedFunctions,
        )
        .await?;

    for function in &actions.add_recently_invoked {
        metrics::record_function_table_state(
            function.function_id.to_string(),
            function.function_version_id.to_string(),
            metrics::FunctionTableState::RecentlyInvoked,
        );
    }

    Ok(())
}

pub async fn discover_new_functions(
    cassandra_service: Arc<CassandraServiceManager>,
    lock_manager: Arc<crate::cassandra::distributed_lock::DistributedLockManager>,
    timeseries_db_client: &TimeseriesDbClient,
    env: &str,
    timeseries_db_ignore_env: bool,
    lock_duration_seconds: i32,
) -> Result<(), FunctionDiscoveryError> {
    // Step 1: Acquire or renew discovery lock (persistent leader pattern)
    let function_discovery_start_time = Instant::now();
    let mut step_start_time = function_discovery_start_time;
    // Try to refresh atomically first (LWT: only applies if we still own the lock).
    // Falls through to acquire if the lock is gone or held by another node.
    let refreshed = lock_manager
        .refresh_lock_ttl(LOCK_NAME_FUNCTION_DISCOVERY, lock_duration_seconds)
        .await
        .map_err(FunctionDiscoveryError::from)?;
    if !refreshed {
        // We don't own it — compete for it with a LWT IF NOT EXISTS
        let won = lock_manager
            .try_acquire_persistent(
                LOCK_NAME_FUNCTION_DISCOVERY.to_string(),
                lock_duration_seconds,
            )
            .await
            .map_err(FunctionDiscoveryError::from)?;
        if !won {
            tracing::debug!(
                "Autoscaler discovering new functions skipped - lock held by another node"
            );
            return Err(FunctionDiscoveryError::LockAcquisitionFailed);
        }
    }
    tracing::info!(
        "Autoscaler discovering new functions - Step 1 (lock acquisition) took {:?} milliseconds",
        step_start_time.elapsed().as_millis()
    );

    // Step 2: Fetch current state
    step_start_time = Instant::now();
    let (state, timeseries_db_active_functions) = fetch_function_state(
        &cassandra_service,
        timeseries_db_client,
        env,
        timeseries_db_ignore_env,
    )
    .await
    .map_err(FunctionDiscoveryError::from)?;
    tracing::debug!(
        "Autoscaler discovering new functions - Step 2 (fetch state) took {:?} milliseconds",
        step_start_time.elapsed().as_millis()
    );

    // Step 3: Analyze what needs to be done
    step_start_time = Instant::now();
    let actions = analyze_function_actions(&state, &timeseries_db_active_functions);
    tracing::debug!(
        "Autoscaler discovering new functions - Step 3 (analyze actions) took {:?} milliseconds",
        step_start_time.elapsed().as_millis()
    );

    // Step 4: Execute the changes
    step_start_time = Instant::now();
    execute_function_actions(&cassandra_service, &actions)
        .await
        .map_err(FunctionDiscoveryError::from)?;
    tracing::debug!(
        "Autoscaler discovering new functions - Step 4 (execute actions) took {:?} milliseconds",
        step_start_time.elapsed().as_millis()
    );

    // Record the total duration as a metric
    tracing::info!(
        "Autoscaler discovering new functions successfully completed in {:?} milliseconds",
        function_discovery_start_time.elapsed().as_millis()
    );
    // Lock will be automatically released when _lock_guard goes out of scope
    Ok(())
}

/// Executes a PromQL query to get recently invoked functions
pub async fn get_recently_invoked_functions(
    timeseries_db_client: &TimeseriesDbClient,
    function_version_id_filter: Option<Uuid>,
    lookback_period_minutes: i64,
    env: &str,
    timeseries_db_ignore_env: bool,
) -> Result<Vec<ActiveFunctionDetails>> {
    let end_time = Utc::now();
    let start_time = end_time - Duration::minutes(lookback_period_minutes);
    let step = StdDuration::from_secs(60); // 1 minute step

    tracing::info!("Executing PromQL queries for recently invoked functions");

    let invocation_query = get_timeseries_db_query(
        QUERY_INVOCATION_SERVICE,
        env,
        timeseries_db_ignore_env,
        function_version_id_filter,
    );
    let grpc_query = get_timeseries_db_query(
        QUERY_GRPC_PROXY,
        env,
        timeseries_db_ignore_env,
        function_version_id_filter,
    );

    tracing::info!(
        "Will execute invocation service query: {}",
        invocation_query
    );
    tracing::info!("Will execute gRPC proxy query: {}", grpc_query);

    let response_invocation_service = match timeseries_db_client
        .query_range(&invocation_query, start_time, end_time, step)
        .await
    {
        Ok(response) => {
            tracing::info!("Successfully executed invocation service query");
            response
        }
        Err(e) => {
            tracing::error!("Failed to execute invocation service query: {}", e);
            return Err(e);
        }
    };

    let response_grpc_proxy = match timeseries_db_client
        .query_range(&grpc_query, start_time, end_time, step)
        .await
    {
        Ok(response) => {
            tracing::info!("Successfully executed gRPC proxy query");
            response
        }
        Err(e) => {
            tracing::error!("Failed to execute gRPC proxy query: {}", e);
            return Err(e);
        }
    };

    let mut recently_invoked_functions = Vec::new();
    let mut seen_functions: HashSet<(Uuid, Uuid, String)> = HashSet::new();

    // Process both responses to extract function details
    for response in [&response_invocation_service, &response_grpc_proxy] {
        for result in &response.data.result {
            if let Some(function_version_id_str) = &result.metric.function_version_id {
                // Parse UUIDs from strings (nca_id is kept as string, not UUID)
                if let (Ok(function_id), Ok(function_version_id)) = (
                    Uuid::parse_str(&result.metric.function_id.clone().unwrap_or_default()),
                    Uuid::parse_str(function_version_id_str),
                ) {
                    let nca_id = result.metric.nca_id.clone().unwrap_or_default();

                    // Check if we've already seen this function
                    let key = (function_id, function_version_id, nca_id.clone());
                    if seen_functions.contains(&key) {
                        tracing::debug!(
                            "Skipping duplicate function {}:{}:{}",
                            function_id,
                            function_version_id,
                            nca_id
                        );
                        continue;
                    }
                    seen_functions.insert(key);

                    // Create ActiveFunctionDetails from the query result
                    let function_details = ActiveFunctionDetails {
                        function_id,
                        function_version_id,
                        nca_id: Some(nca_id.clone()),
                        last_updated_at: Some(end_time),
                        num_workers: None, // Recently invoked functions start with unknown worker count
                        last_predicted_desired_instance_count: None,
                        last_predicted_error_code: None,
                    };

                    tracing::debug!(
                        "Parsed recently invoked function from TimeseriesDb: {}:{}:{}",
                        function_id,
                        function_version_id,
                        nca_id
                    );
                    recently_invoked_functions.push(function_details);
                } else {
                    tracing::info!("Failed to parse UUIDs for function: {:?}", result.metric);
                }
            } else {
                tracing::info!("Missing function_version_id in result: {:?}", result.metric);
            }
        }
    }

    tracing::info!(
        "Found {} unique recently invoked functions (after deduplication).",
        recently_invoked_functions.len()
    );

    Ok(recently_invoked_functions)
}

/// Executes a PromQL query to get functions with workers based on worker thread count metric
/// Get functions that are running - either with worker metrics OR with active instances (BYOC)
/// This covers both normal functions (which emit worker metrics) and BYOC functions (which only have instance counts)
pub async fn get_functions_with_workers(
    timeseries_db_client: &TimeseriesDbClient,
    env: &str,
    timeseries_db_ignore_env: bool,
) -> Result<Vec<ActiveFunctionDetails>> {
    let end_time = Utc::now();
    let start_time = end_time - Duration::minutes(5); // 5 minute window
    let step = StdDuration::from_secs(60); // 1 minute step

    // Query for functions with workers OR functions with active instances (for BYOC)
    // This ensures both normal functions and BYOC functions are discovered
    // Note: nvcf_function_instances_current query does NOT filter by state to match get_byoc_instance_count
    let query = if timeseries_db_ignore_env {
        r#"count by(function_id, function_version_id, nca_id) (nvcf_worker_service_worker_thread_count_total) > 0
or
avg by(function_id, function_version_id, nca_id) (nvcf_function_instances_current) > 0"#.to_string()
    } else {
        let environment = if env == "stg" { "stage" } else { "prod" };
        format!(
            r#"count by(function_id, function_version_id, nca_id) (nvcf_worker_service_worker_thread_count_total{{environment="{}"}}) > 0
or
avg by(function_id, function_version_id, nca_id) (nvcf_function_instances_current{{environment="{}"}}) > 0"#,
            environment, environment
        )
    };

    tracing::info!(
        "Executing PromQL query for functions with workers (ignore_env={}): {}",
        timeseries_db_ignore_env,
        query
    );

    let response = match timeseries_db_client
        .query_range(&query, start_time, end_time, step)
        .await
    {
        Ok(response) => {
            tracing::info!("Successfully executed functions with workers query");
            response
        }
        Err(e) => {
            tracing::error!("Failed to execute functions with workers query: {}", e);
            return Err(e);
        }
    };
    // OR query can return multiple series per function (worker count and/or instance count).
    // Dedupe by (function_id, function_version_id, nca_id) and keep max num_workers so
    // reported current instances match the metric we query (avoid undercount when one
    // series says 2 and the other says 3).
    let mut by_key: HashMap<(Uuid, Uuid, String), ActiveFunctionDetails> = HashMap::new();

    for result in &response.data.result {
        if let Some(function_version_id_str) = &result.metric.function_version_id {
            if let (Ok(function_id), Ok(function_version_id)) = (
                Uuid::parse_str(&result.metric.function_id.clone().unwrap_or_default()),
                Uuid::parse_str(function_version_id_str),
            ) {
                let nca_id = result.metric.nca_id.clone().unwrap_or_default();

                let num_workers = if !result.values.is_empty() {
                    let raw_value = &result.values.last().unwrap().1;
                    match raw_value.parse::<f64>() {
                        Ok(float_value) => {
                            if float_value.is_finite() && float_value >= 0.0 {
                                Some(float_value.round() as i32)
                            } else {
                                tracing::warn!(
                                    "Invalid worker count value for function {}:{}:{}: {} (not finite or negative)",
                                    function_id, function_version_id, nca_id, float_value
                                );
                                None
                            }
                        }
                        Err(e) => {
                            tracing::warn!(
                                "Failed to parse worker count for function {}:{}:{}: '{}' - {}",
                                function_id,
                                function_version_id,
                                nca_id,
                                raw_value,
                                e
                            );
                            None
                        }
                    }
                } else {
                    None
                };

                let key = (function_id, function_version_id, nca_id.clone());
                let details = ActiveFunctionDetails {
                    function_id,
                    function_version_id,
                    nca_id: Some(nca_id),
                    last_updated_at: Some(end_time),
                    num_workers,
                    last_predicted_desired_instance_count: None,
                    last_predicted_error_code: None,
                };

                let existing = by_key.get(&key).and_then(|d| d.num_workers);
                let keep = match (existing, num_workers) {
                    (None, _) => true,
                    (Some(_a), None) => false,
                    (Some(a), Some(b)) => b > a,
                };
                if keep {
                    by_key.insert(key, details);
                }
            }
        }
    }

    let functions_with_workers: Vec<_> = by_key.into_values().collect();
    tracing::info!(
        "Found {} functions with workers",
        functions_with_workers.len()
    );

    Ok(functions_with_workers)
}

/// Get functions with active instances from TimeseriesDb (for BYOC functions that don't emit worker metrics)
/// This uses the nvcf_function_instances_current metric which tracks actual running instances
pub async fn get_functions_with_active_instances(
    timeseries_db_client: &TimeseriesDbClient,
    env: &str,
    timeseries_db_ignore_env: bool,
) -> Result<Vec<ActiveFunctionDetails>> {
    let end_time = Utc::now();
    let start_time = end_time - Duration::minutes(5); // 5 minute window
    let step = StdDuration::from_secs(60); // 1 minute step

    let query = if timeseries_db_ignore_env {
        r#"nvcf_function_instances_current{state="active"} > 0"#.to_string()
    } else {
        let environment = if env == "stg" { "stage" } else { "prod" };
        format!(
            r#"nvcf_function_instances_current{{state="active", environment="{}"}} > 0"#,
            environment
        )
    };

    tracing::info!(
        "Executing PromQL query for functions with active instances (ignore_env={}): {}",
        timeseries_db_ignore_env,
        query
    );

    let response = match timeseries_db_client
        .query_range(&query, start_time, end_time, step)
        .await
    {
        Ok(response) => {
            tracing::info!("Successfully executed functions with active instances query");
            response
        }
        Err(e) => {
            tracing::error!(
                "Failed to execute functions with active instances query: {}",
                e
            );
            return Err(e);
        }
    };

    let mut functions_with_active_instances = Vec::new();

    // Process the response to extract function details
    for result in &response.data.result {
        if let Some(function_version_id_str) = &result.metric.function_version_id {
            // Parse UUIDs from strings (nca_id is kept as string, not UUID)
            if let (Ok(function_id), Ok(function_version_id)) = (
                Uuid::parse_str(&result.metric.function_id.clone().unwrap_or_default()),
                Uuid::parse_str(function_version_id_str),
            ) {
                let nca_id = result.metric.nca_id.clone().unwrap_or_default();

                // Parse the instance count from the most recent value
                let instance_count = if !result.values.is_empty() {
                    let raw_value = &result.values.last().unwrap().1;
                    match raw_value.parse::<f64>() {
                        Ok(float_value) => {
                            if float_value.is_finite() && float_value > 0.0 {
                                Some(float_value.round() as i32)
                            } else {
                                None
                            }
                        }
                        Err(_) => None,
                    }
                } else {
                    None
                };

                // Only include if there are active instances
                if let Some(count) = instance_count {
                    if count > 0 {
                        let function_details = ActiveFunctionDetails {
                            function_id,
                            function_version_id,
                            nca_id: Some(nca_id.clone()),
                            last_updated_at: Some(end_time),
                            num_workers: Some(-1), // BYOC functions have num_workers = -1
                            last_predicted_desired_instance_count: None,
                            last_predicted_error_code: None,
                        };

                        tracing::debug!(
                            "Found function {}:{} with {} active instances (nca_id: {})",
                            function_id,
                            function_version_id,
                            count,
                            nca_id
                        );

                        functions_with_active_instances.push(function_details);
                    }
                }
            }
        }
    }

    tracing::info!(
        "Found {} functions with active instances",
        functions_with_active_instances.len()
    );

    Ok(functions_with_active_instances)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::timeseries_db::timeseries_db_client::{
        Metric, ResponseData, TimeseriesDbResponse, TimeseriesDbResult,
    };

    #[test]
    fn test_get_functions_with_workers_response_parsing() {
        // Create a mock TimeseriesDb response
        let mock_response = TimeseriesDbResponse {
            status: "success".to_string(),
            data: ResponseData {
                result_type: "matrix".to_string(),
                result: vec![TimeseriesDbResult {
                    metric: Metric {
                        name: Some("nvcf_worker_service_worker_thread_count_total".to_string()),
                        error_code: None,
                        function_id: Some("550e8400-e29b-41d4-a716-446655440000".to_string()),
                        function_version_id: Some(
                            "550e8400-e29b-41d4-a716-446655440001".to_string(),
                        ),
                        nca_id: Some("CMYBKSNNjtg1TQmSke-gHNGgMlFvA-dCRAI8gcHOBcw".to_string()),
                        instance_id: None,
                    },
                    values: vec![
                        (1748551740.0, "5".to_string()), // timestamp, worker count
                    ],
                }],
            },
        };

        // Test parsing logic
        let result = &mock_response.data.result[0];
        let function_version_id_str = result.metric.function_version_id.as_ref().unwrap();

        let function_id = Uuid::parse_str(result.metric.function_id.as_ref().unwrap()).unwrap();
        let function_version_id = Uuid::parse_str(function_version_id_str).unwrap();
        let nca_id = result.metric.nca_id.clone().unwrap_or_default();

        let num_workers = if !result.values.is_empty() {
            let raw_value = &result.values.last().unwrap().1;
            match raw_value.parse::<f64>() {
                Ok(float_value) => {
                    if float_value.is_finite() && float_value >= 0.0 {
                        Some(float_value.round() as i32)
                    } else {
                        None
                    }
                }
                Err(_) => None,
            }
        } else {
            None
        };

        assert_eq!(
            function_id.to_string(),
            "550e8400-e29b-41d4-a716-446655440000"
        );
        assert_eq!(
            function_version_id.to_string(),
            "550e8400-e29b-41d4-a716-446655440001"
        );
        assert_eq!(nca_id, "CMYBKSNNjtg1TQmSke-gHNGgMlFvA-dCRAI8gcHOBcw");
        assert_eq!(num_workers, Some(5));
    }

    #[test]
    fn test_get_functions_with_workers_invalid_uuid() {
        // Create a mock TimeseriesDb response with invalid UUID
        let mock_response = TimeseriesDbResponse {
            status: "success".to_string(),
            data: ResponseData {
                result_type: "matrix".to_string(),
                result: vec![TimeseriesDbResult {
                    metric: Metric {
                        name: Some("nvcf_worker_service_worker_thread_count_total".to_string()),
                        error_code: None,
                        function_id: Some("invalid-uuid".to_string()),
                        function_version_id: Some("also-invalid".to_string()),
                        nca_id: Some("also-invalid-nca".to_string()),
                        instance_id: None,
                    },
                    values: vec![(1748551740.0, "3".to_string())],
                }],
            },
        };

        // Test parsing logic with invalid UUID
        let result = &mock_response.data.result[0];
        let function_version_id_str = result.metric.function_version_id.as_ref().unwrap();

        let function_id_result = Uuid::parse_str(result.metric.function_id.as_ref().unwrap());
        let function_version_id_result = Uuid::parse_str(function_version_id_str);

        assert!(function_id_result.is_err());
        assert!(function_version_id_result.is_err());
    }
}

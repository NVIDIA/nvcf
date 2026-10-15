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
use anyhow::{anyhow, Result};
use chrono::{Duration, Utc};
use futures::{stream, StreamExt};
use std::collections::{HashMap, HashSet};
use std::sync::Arc;
use std::time::Duration as StdDuration;
use tokio::sync::Semaphore;
use tokio::time::Instant;
use tracing;
use uuid::Uuid;

pub const LOCK_NAME_FUNCTION_DISCOVERY: &str = "function_discovery";

/// Lookback window (minutes) for "recently invoked" in discovery. Functions with no invocations
/// in this window are moved from recently_invoked to running_functions.
pub const DISCOVERY_RECENTLY_INVOKED_LOOKBACK_MINUTES: i64 = 5;
const DISCOVERY_QUERY_CONCURRENCY: usize = 4;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum DiscoveryShard {
    Hex0To3,
    Hex4To7,
    Hex8ToB,
    HexCToF,
}

impl DiscoveryShard {
    const ALL: [Self; 4] = [Self::Hex0To3, Self::Hex4To7, Self::Hex8ToB, Self::HexCToF];

    fn name(self) -> &'static str {
        match self {
            Self::Hex0To3 => "0-3",
            Self::Hex4To7 => "4-7",
            Self::Hex8ToB => "8-b",
            Self::HexCToF => "c-f",
        }
    }

    fn function_id_regex(self) -> &'static str {
        match self {
            Self::Hex0To3 => "[0-3].*",
            Self::Hex4To7 => "[4-7].*",
            Self::Hex8ToB => "[89ab].*",
            Self::HexCToF => "[c-f].*",
        }
    }

    fn order(self) -> u8 {
        match self {
            Self::Hex0To3 => 0,
            Self::Hex4To7 => 1,
            Self::Hex8ToB => 2,
            Self::HexCToF => 3,
        }
    }
}

#[derive(Clone, Copy, Debug)]
enum InvocationMetricSource {
    InvocationService,
    GrpcProxy,
}

impl InvocationMetricSource {
    fn name(self) -> &'static str {
        match self {
            Self::InvocationService => "invocation_service",
            Self::GrpcProxy => "grpc_proxy",
        }
    }

    fn order(self) -> u8 {
        match self {
            Self::InvocationService => 0,
            Self::GrpcProxy => 1,
        }
    }
}

struct RecentInvocationQuery {
    source: InvocationMetricSource,
    shard: Option<DiscoveryShard>,
    query: String,
}

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
    shard: Option<DiscoveryShard>,
) -> String {
    let mut matchers = Vec::new();
    if !ignore_env {
        matchers.push(format!(r#"aws_env="{}""#, env));
    }
    if let Some(function_version_id) = function_version_filter {
        matchers.push(format!(r#"function_version_id="{}""#, function_version_id));
    }
    if let Some(shard) = shard {
        matchers.push(format!(r#"function_id=~"{}""#, shard.function_id_regex()));
    }

    let selector = if matchers.is_empty() {
        String::new()
    } else {
        format!("{{{}}}", matchers.join(", "))
    };
    template.replace("{env_filter}", &selector)
}

fn recent_invocation_queries(
    env: &str,
    ignore_env: bool,
    function_version_filter: Option<Uuid>,
) -> Vec<RecentInvocationQuery> {
    let shards: Vec<Option<DiscoveryShard>> = if function_version_filter.is_some() {
        vec![None]
    } else {
        DiscoveryShard::ALL.into_iter().map(Some).collect()
    };

    let mut queries = Vec::with_capacity(shards.len() * 2);
    for shard in shards {
        queries.push(RecentInvocationQuery {
            source: InvocationMetricSource::InvocationService,
            shard,
            query: get_timeseries_db_query(
                QUERY_INVOCATION_SERVICE,
                env,
                ignore_env,
                function_version_filter,
                shard,
            ),
        });
        queries.push(RecentInvocationQuery {
            source: InvocationMetricSource::GrpcProxy,
            shard,
            query: get_timeseries_db_query(
                QUERY_GRPC_PROXY,
                env,
                ignore_env,
                function_version_filter,
                shard,
            ),
        });
    }
    queries
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

    let timeseries_db_active_functions =
        fetch_timeseries_db_active_functions(timeseries_db_client, env, timeseries_db_ignore_env)
            .await?;

    let state = FunctionState {
        db_recently_invoked: db_recently_invoked
            .iter()
            .map(|f| (f.function_id, f.function_version_id))
            .collect(),
    };

    Ok((state, timeseries_db_active_functions))
}

/// Fetch active functions from independent invocation and worker metric sources.
/// A failed source is unknown, not empty: keep every successful result, and fail
/// only when neither source produced a usable response.
async fn fetch_timeseries_db_active_functions(
    timeseries_db_client: &TimeseriesDbClient,
    env: &str,
    timeseries_db_ignore_env: bool,
) -> Result<Vec<ActiveFunctionDetails>> {
    tracing::info!("Getting recently invoked and running functions...");
    let query_semaphore = Arc::new(Semaphore::new(DISCOVERY_QUERY_CONCURRENCY));
    let workers_semaphore = Arc::clone(&query_semaphore);
    let (recent_result, workers_result) = tokio::join!(
        get_recently_invoked_functions_with_semaphore(
            timeseries_db_client,
            None,
            DISCOVERY_RECENTLY_INVOKED_LOOKBACK_MINUTES,
            env,
            timeseries_db_ignore_env,
            Some(query_semaphore),
        ),
        async {
            let _permit = workers_semaphore
                .acquire_owned()
                .await
                .expect("discovery query semaphore must remain open");
            get_functions_with_workers(timeseries_db_client, env, timeseries_db_ignore_env).await
        },
    );

    let mut active_map: HashMap<(Uuid, Uuid), ActiveFunctionDetails> = HashMap::new();
    let mut failed_sources = 0usize;

    match recent_result {
        Ok(functions) => {
            tracing::debug!("Got {} recently invoked functions", functions.len());
            for function in functions {
                active_map.insert(
                    (function.function_id, function.function_version_id),
                    function,
                );
            }
        }
        Err(error) => {
            tracing::error!(error = %error, "Recently invoked function discovery failed");
            failed_sources += 1;
        }
    }

    match workers_result {
        Ok(functions) => {
            tracing::info!("Got {} running functions (includes BYOC)", functions.len());
            for function in functions {
                merge_worker_details(&mut active_map, function);
            }
        }
        Err(error) => {
            tracing::error!(error = %error, "Running function discovery failed");
            failed_sources += 1;
        }
    }

    if failed_sources == 2 {
        return Err(anyhow!(
            "all TimeseriesDb function discovery sources failed"
        ));
    }

    if failed_sources > 0 {
        tracing::warn!(
            failed_sources,
            functions_found = active_map.len(),
            "Function discovery completed with partial TimeseriesDb results"
        );
    }

    Ok(active_map.into_values().collect())
}

fn merge_worker_details(
    active_map: &mut HashMap<(Uuid, Uuid), ActiveFunctionDetails>,
    function: ActiveFunctionDetails,
) {
    active_map
        .entry((function.function_id, function.function_version_id))
        .and_modify(|existing| existing.num_workers = function.num_workers)
        .or_insert(function);
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
    get_recently_invoked_functions_with_semaphore(
        timeseries_db_client,
        function_version_id_filter,
        lookback_period_minutes,
        env,
        timeseries_db_ignore_env,
        None,
    )
    .await
}

async fn get_recently_invoked_functions_with_semaphore(
    timeseries_db_client: &TimeseriesDbClient,
    function_version_id_filter: Option<Uuid>,
    lookback_period_minutes: i64,
    env: &str,
    timeseries_db_ignore_env: bool,
    query_semaphore: Option<Arc<Semaphore>>,
) -> Result<Vec<ActiveFunctionDetails>> {
    let end_time = Utc::now();
    let start_time = end_time - Duration::minutes(lookback_period_minutes);
    let step = StdDuration::from_secs(60); // 1 minute step

    let queries =
        recent_invocation_queries(env, timeseries_db_ignore_env, function_version_id_filter);
    let query_count = queries.len();
    tracing::info!(
        query_count,
        sharded = function_version_id_filter.is_none(),
        "Executing PromQL queries for recently invoked functions"
    );

    // Discovery runs eight queries (two sources across four shards) through one
    // shared concurrency bound. Per-function scaling remains two unsharded
    // queries. Every query is polled even when another source or shard fails.
    let mut query_results = stream::iter(queries.into_iter().map(|query_spec| {
        let query_semaphore = query_semaphore.clone();
        async move {
            let _permit = match query_semaphore {
                Some(semaphore) => Some(
                    semaphore
                        .acquire_owned()
                        .await
                        .expect("discovery query semaphore must remain open"),
                ),
                None => None,
            };
            let source = query_spec.source;
            let shard = query_spec.shard;
            let result = timeseries_db_client
                .query_range(&query_spec.query, start_time, end_time, step)
                .await;
            (source, shard, result)
        }
    }))
    .buffer_unordered(DISCOVERY_QUERY_CONCURRENCY.min(query_count))
    .collect::<Vec<_>>()
    .await;

    // Preserve the original source precedence even though requests complete
    // out of order. This keeps deduplication independent of query latency.
    query_results.sort_by_key(|(source, shard, _)| {
        (
            source.order(),
            shard.map(DiscoveryShard::order).unwrap_or(0),
        )
    });

    let mut recently_invoked_functions = Vec::new();
    let mut seen_functions: HashSet<(Uuid, Uuid, String)> = HashSet::new();
    let mut failed_queries = 0usize;
    let mut successful_queries = 0usize;

    for (source, shard, query_result) in query_results {
        let shard_name = shard.map(DiscoveryShard::name).unwrap_or("none");
        let response = match query_result {
            Ok(response) => {
                successful_queries += 1;
                tracing::info!(
                    source = source.name(),
                    shard = shard_name,
                    series = response.data.result.len(),
                    "Recently invoked query succeeded"
                );
                response
            }
            Err(error) => {
                tracing::error!(
                    source = source.name(),
                    shard = shard_name,
                    error = %error,
                    "Recently invoked query failed"
                );
                failed_queries += 1;
                continue;
            }
        };

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

    // Discovery can safely consume partial shards because it only inserts
    // positive observations. Per-function scaling remains fail-closed: both
    // invocation sources must succeed before an empty result can mean idle.
    if successful_queries == 0 || (function_version_id_filter.is_some() && failed_queries > 0) {
        return Err(anyhow!(
            "recently invoked TimeseriesDb queries failed: {successful_queries} succeeded, {failed_queries} failed"
        ));
    }

    if failed_queries > 0 {
        tracing::warn!(
            successful_queries,
            failed_queries,
            functions_found = recently_invoked_functions.len(),
            "Recently invoked discovery completed with partial shard results"
        );
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
    // Align end to the previous fully-settled step boundary so the trailing point is not the
    // bleeding edge — partial scrape cycles there can collapse count by(...) for healthy pods.
    const STEP_SECS: i64 = 60;
    let now_secs = Utc::now().timestamp();
    let end_secs = (now_secs / STEP_SECS) * STEP_SECS - STEP_SECS;
    let end_time = chrono::DateTime::from_timestamp(end_secs, 0).unwrap_or_else(Utc::now);
    let start_time = end_time - Duration::minutes(5); // 5 minute window
    let step = StdDuration::from_secs(STEP_SECS as u64);

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
    // Align end to the previous fully-settled step boundary so the trailing point is not the
    // bleeding edge — partial scrape cycles there can collapse aggregations for healthy pods.
    const STEP_SECS: i64 = 60;
    let now_secs = Utc::now().timestamp();
    let end_secs = (now_secs / STEP_SECS) * STEP_SECS - STEP_SECS;
    let end_time = chrono::DateTime::from_timestamp(end_secs, 0).unwrap_or_else(Utc::now);
    let start_time = end_time - Duration::minutes(5); // 5 minute window
    let step = StdDuration::from_secs(STEP_SECS as u64);

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
    use crate::timeseries_db::TimeseriesDbSettings;

    fn fast_backoff() -> backon::ExponentialBuilder {
        backon::ExponentialBuilder::default()
            .with_max_times(1)
            .with_min_delay(StdDuration::from_millis(1))
            .with_max_delay(StdDuration::from_millis(2))
    }

    fn ts_client(url: String) -> TimeseriesDbClient {
        let config = TimeseriesDbSettings {
            timeseries_db_url: url,
            disable_auth: true,
            env: "stg".to_string(),
            ignore_env: true,
            backoff: Some(fast_backoff()),
            ..Default::default()
        };
        TimeseriesDbClient::new(&config, None).expect("build test client")
    }

    fn vm_series(metric_name: &str, function_id: Uuid, function_version_id: Uuid) -> String {
        format!(
            r#"{{"status":"success","data":{{"resultType":"matrix","result":[{{"metric":{{"__name__":"{metric_name}","function_id":"{function_id}","function_version_id":"{function_version_id}","nca_id":"nca"}},"values":[[1700000000,"1"]]}}]}}}}"#
        )
    }

    #[test]
    fn discovery_queries_cover_four_fixed_shards_for_both_sources() {
        let queries = recent_invocation_queries("prd", false, None);
        assert_eq!(queries.len(), 8);

        for shard in DiscoveryShard::ALL {
            let matcher = format!(r#"function_id=~"{}""#, shard.function_id_regex());
            let shard_queries: Vec<_> = queries
                .iter()
                .filter(|query| query.shard == Some(shard))
                .collect();
            assert_eq!(shard_queries.len(), 2);
            for query in shard_queries {
                assert_eq!(query.query.matches(&matcher).count(), 4);
                assert_eq!(query.query.matches(r#"aws_env="prd""#).count(), 4);
            }
        }
    }

    #[test]
    fn per_function_recent_invocation_queries_are_not_sharded() {
        let function_version_id = Uuid::new_v4();
        let queries = recent_invocation_queries("stg", true, Some(function_version_id));

        assert_eq!(queries.len(), 2);
        for query in queries {
            assert!(query.shard.is_none());
            assert!(!query.query.contains("function_id=~"));
            assert_eq!(
                query
                    .query
                    .matches(&format!(r#"function_version_id="{function_version_id}""#))
                    .count(),
                4
            );
        }
    }

    #[tokio::test]
    async fn invocation_failure_does_not_suppress_grpc_discovery_shards() {
        let function_id = Uuid::new_v4();
        let function_version_id = Uuid::new_v4();
        let mut server = mockito::Server::new_async().await;
        let invocation = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex(
                r"function_request(?:%7B|\{)".to_string(),
            ))
            .with_status(500)
            .with_body("boom")
            .expect_at_least(4)
            .create_async()
            .await;
        let grpc = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex(
                "function_request_total".to_string(),
            ))
            .with_status(200)
            .with_body(vm_series(
                "function_request_total",
                function_id,
                function_version_id,
            ))
            .expect(4)
            .create_async()
            .await;

        let functions = get_recently_invoked_functions(
            &ts_client(server.url()),
            None,
            DISCOVERY_RECENTLY_INVOKED_LOOKBACK_MINUTES,
            "stg",
            true,
        )
        .await
        .expect("gRPC shard results should survive invocation failures");

        assert_eq!(functions.len(), 1);
        assert_eq!(functions[0].function_id, function_id);
        invocation.assert_async().await;
        grpc.assert_async().await;
    }

    #[tokio::test]
    async fn per_function_invocation_failure_still_queries_grpc_but_fails_closed() {
        let function_id = Uuid::new_v4();
        let function_version_id = Uuid::new_v4();
        let mut server = mockito::Server::new_async().await;
        let invocation = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex(
                r"function_request(?:%7B|\{)".to_string(),
            ))
            .with_status(500)
            .with_body("boom")
            .expect_at_least(1)
            .create_async()
            .await;
        let grpc = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex(
                "function_request_total".to_string(),
            ))
            .with_status(200)
            .with_body(vm_series(
                "function_request_total",
                function_id,
                function_version_id,
            ))
            .expect(1)
            .create_async()
            .await;

        let result = get_recently_invoked_functions(
            &ts_client(server.url()),
            Some(function_version_id),
            DISCOVERY_RECENTLY_INVOKED_LOOKBACK_MINUTES,
            "stg",
            true,
        )
        .await;

        let error = result.expect_err("a partial per-function result must fail closed");
        let message = format!("{error:#}");
        assert!(message.contains("1 succeeded, 1 failed"));
        assert!(!message.contains("boom"));
        invocation.assert_async().await;
        grpc.assert_async().await;
    }

    #[tokio::test]
    async fn workers_query_survives_total_invocation_discovery_failure() {
        let function_id = Uuid::new_v4();
        let function_version_id = Uuid::new_v4();
        let mut server = mockito::Server::new_async().await;
        let recent = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex("function_request".to_string()))
            .with_status(500)
            .with_body("boom")
            .expect_at_least(8)
            .create_async()
            .await;
        let workers = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex(
                "worker_thread_count_total".to_string(),
            ))
            .with_status(200)
            .with_body(vm_series(
                "nvcf_worker_service_worker_thread_count_total",
                function_id,
                function_version_id,
            ))
            .expect(1)
            .create_async()
            .await;

        let functions = fetch_timeseries_db_active_functions(&ts_client(server.url()), "stg", true)
            .await
            .expect("worker results should survive invocation discovery failure");

        assert_eq!(functions.len(), 1);
        assert_eq!(functions[0].function_id, function_id);
        recent.assert_async().await;
        workers.assert_async().await;
    }

    #[test]
    fn worker_count_is_preserved_when_discovery_sources_overlap() {
        let function_id = Uuid::new_v4();
        let function_version_id = Uuid::new_v4();
        let recent = ActiveFunctionDetails::new(function_id, function_version_id, "recent".into());
        let mut worker =
            ActiveFunctionDetails::new(function_id, function_version_id, "worker".into());
        worker.num_workers = Some(3);
        let mut active_map = HashMap::from([((function_id, function_version_id), recent)]);

        merge_worker_details(&mut active_map, worker);

        let merged = active_map.get(&(function_id, function_version_id)).unwrap();
        assert_eq!(merged.nca_id.as_deref(), Some("recent"));
        assert_eq!(merged.num_workers, Some(3));
    }

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

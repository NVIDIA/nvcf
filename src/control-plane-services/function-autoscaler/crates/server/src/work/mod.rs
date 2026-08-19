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
use crate::scaling::{
    decide_scaling, sanitize_utilization, MetricSource, ScalingInputs, ScalingSettings,
};
use crate::{
    cassandra::cassandra_service::CassandraServiceManager,
    timeseries_db::timeseries_db_client::TimeseriesDbClient,
};
use anyhow::{Context, Result};
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

#[derive(Clone)]
pub struct MetricRoutingCache {
    sources: Cache<(Uuid, Uuid), MetricSource>,
    gateway_targets: Cache<Uuid, Uuid>,
}

pub fn new_metric_routing_cache() -> MetricRoutingCache {
    let ttl = StdDuration::from_secs(60 * 60);
    MetricRoutingCache {
        sources: Cache::builder().time_to_live(ttl).build(),
        gateway_targets: Cache::new(10_000),
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct GatewayTarget {
    function_version_id: Uuid,
    nca_id: String,
    current_instances: usize,
    total_current_instances: usize,
}

struct GatheredScalingInputs {
    inputs: ScalingInputs,
    gateway_target: Option<GatewayTarget>,
}

pub mod bucket;
pub mod discovery;

use discovery::get_recently_invoked_functions;

const TIMESERIES_DB_QUERY_STEP: StdDuration = StdDuration::from_secs(60); // 1 minute step for TimeseriesDb queries
pub const CALCULATE_UTILIZATION_LOCK_PREFIX: &str = "util_lock";

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
struct MetricEnvironments {
    aws: &'static str,
    worker: &'static str,
    control_plane: &'static str,
}

impl MetricEnvironments {
    fn from_config(env: &str) -> Self {
        if env == "stg" {
            Self {
                aws: "stg",
                worker: "stage",
                control_plane: "staging",
            }
        } else {
            Self {
                aws: "prd",
                worker: "prod",
                control_plane: "production",
            }
        }
    }
}

/// Get historical utilization data for a specific function
#[allow(clippy::too_many_arguments)]
async fn get_function_utilization_history(
    timeseries_db_client: &TimeseriesDbClient,
    function_id: &Uuid,
    function_version_id: &Uuid,
    env: &str,
    metric_source: MetricSource,
    ignore_env: bool,
    lookback_minutes: i64,
    utilization_window_seconds: u64,
) -> Result<Vec<(i64, String)>> {
    let end_time = Utc::now();
    let start_time = end_time - Duration::minutes(lookback_minutes);
    let step = TIMESERIES_DB_QUERY_STEP;

    let metric_env = MetricEnvironments::from_config(env);
    let worker_env_suffix = if ignore_env {
        String::new()
    } else {
        format!(r#", environment="{}""#, metric_env.worker)
    };
    let aws_env_suffix = if ignore_env {
        String::new()
    } else {
        format!(r#", aws_env="{}""#, metric_env.aws)
    };

    let query = match metric_source {
        MetricSource::ControlPlane => {
            // Invocation latency can be split by caller NCA, while capacity belongs to the shared
            // function-version pool. Aggregate both sides by that shared identity so the query works
            // with both per-caller and NCA-free latency series.
            format!(
                r#"100 * sum by(function_id, function_version_id) (rate(function_request_latency_sum{{function_id="{id}", function_version_id="{v_id}"{env}}}[2m])) /
            (avg by(function_id, function_version_id) (nvcf_function_instances_current{{function_id="{id}", function_version_id="{v_id}"{env}}}) * avg by(function_id, function_version_id) (nvcf_function_concurrency{{function_id="{id}", function_version_id="{v_id}"{env}}})) or vector(0)"#,
                id = function_id,
                v_id = function_version_id,
                env = aws_env_suffix
            )
        }
        MetricSource::WorkerThreads => format!(
            r#"((sum by(function_id, function_version_id, nca_id) (increase(nvcf_worker_service_worker_thread_busy_seconds_total{{function_id="{id}", function_version_id="{v_id}"{env}}}[{window}s]))) / {window} * 100) /
            (sum by(function_id, function_version_id, nca_id) (nvcf_worker_service_worker_thread_count_total{{function_id="{id}", function_version_id="{v_id}"{env}}}))"#,
            id = function_id,
            v_id = function_version_id,
            env = worker_env_suffix,
            window = utilization_window_seconds
        ),
        MetricSource::LlmGateway => {
            // Little's Law: average duration * request rate collapses to the
            // per-second rate of the duration sum.
            format!(
                r#"100 * sum by(function_id) (rate(llm_api_gateway_http_request_duration_seconds_sum{{function_id="{id}"{aws_env}}}[2m])) /
                clamp_min(sum by(function_id) (
                    max by(function_id, function_version_id) (nvcf_function_instances_current{{function_id="{id}"{aws_env}}})
                    * on(function_id, function_version_id)
                    max by(function_id, function_version_id) (nvcf_function_concurrency{{function_id="{id}"{aws_env}}})
                ), 1)"#,
                id = function_id,
                aws_env = aws_env_suffix,
            )
        }
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

    let expected_function_id = function_id.to_string();
    let expected_version_id = function_version_id.to_string();
    for result in &response.data.result {
        if let Some(f_id) = &result.metric.function_id {
            let version_matches = metric_source == MetricSource::LlmGateway
                || result.metric.function_version_id.as_deref()
                    == Some(expected_version_id.as_str());
            // Verify this is the function we're looking for
            if f_id == &expected_function_id && version_matches {
                tracing::debug!(
                    "Found matching function {}:{} with {} data points",
                    f_id,
                    result
                        .metric
                        .function_version_id
                        .as_deref()
                        .unwrap_or("gateway"),
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
        String::new()
    } else {
        let metric_env = MetricEnvironments::from_config(env);
        format!(r#", environment="{}""#, metric_env.control_plane)
    };

    let query = format!(
        r#"avg by(function_id, function_version_id, nca_id) (nvcf_function_instances_current{{function_id="{id}", function_version_id="{v_id}"{env}}})"#,
        id = function_id,
        v_id = function_version_id,
        env = env_suffix
    );

    // Align end to the previous fully-settled step boundary (one step back from now).
    // Reading the bleeding edge can pick up a partial scrape cycle and report a wrong count.
    const STEP_SECS: i64 = 60;
    let now_secs = Utc::now().timestamp();
    let end_secs = (now_secs / STEP_SECS) * STEP_SECS - STEP_SECS;
    let end_time = chrono::DateTime::from_timestamp(end_secs, 0).unwrap_or_else(Utc::now);
    let start_time = end_time - chrono::Duration::seconds(STEP_SECS);
    let step = std::time::Duration::from_secs(STEP_SECS as u64);

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

async fn llm_gateway_metrics_present(
    timeseries_db_client: &TimeseriesDbClient,
    function_id: &Uuid,
    env: &str,
    ignore_env: bool,
) -> Result<bool> {
    let end_time = Utc::now();
    let env_suffix = if ignore_env {
        String::new()
    } else {
        let metric_env = MetricEnvironments::from_config(env);
        format!(r#", aws_env="{}""#, metric_env.aws)
    };
    let query = format!(
        r#"count by(function_id) (llm_api_gateway_http_requests_total{{function_id="{function_id}"{env_suffix}}})"#,
    );
    let response = timeseries_db_client
        .query_range(
            &query,
            end_time - Duration::minutes(5),
            end_time,
            TIMESERIES_DB_QUERY_STEP,
        )
        .await?;

    Ok(!response.data.result.is_empty())
}

fn select_gateway_target(
    mut versions: Vec<GatewayTarget>,
    pinned: Option<Uuid>,
) -> Option<GatewayTarget> {
    versions.sort_by_key(|version| (version.current_instances == 0, version.function_version_id));
    let has_active = versions
        .first()
        .is_some_and(|version| version.current_instances > 0);

    pinned
        .and_then(|id| {
            versions
                .iter()
                .find(|version| {
                    version.function_version_id == id
                        && (!has_active || version.current_instances > 0)
                })
                .cloned()
        })
        .or_else(|| versions.into_iter().next())
}

fn gateway_target_desired_instances(
    desired_total: usize,
    total_current: usize,
    target_current: usize,
) -> usize {
    target_current
        .saturating_add(desired_total)
        .saturating_sub(total_current)
}

async fn get_gateway_target(
    timeseries_db_client: &TimeseriesDbClient,
    function_id: &Uuid,
    env: &str,
    ignore_env: bool,
    routing_cache: &MetricRoutingCache,
) -> Result<GatewayTarget> {
    let end_time = Utc::now();
    let env_suffix = if ignore_env {
        String::new()
    } else {
        let metric_env = MetricEnvironments::from_config(env);
        format!(r#", aws_env="{}""#, metric_env.aws)
    };
    let query = format!(
        r#"((max by(function_id, function_version_id) (nvcf_function_instances_current{{function_id="{id}"{env}}}))
        * on(function_id, function_version_id) group_left(nca_id)
        max by(function_id, function_version_id, nca_id) (nvcf_function_info{{function_id="{id}"{env}}}))
        or
        (max by(function_id, function_version_id, nca_id) (nvcf_function_info{{function_id="{id}"{env}}}) * 0)"#,
        id = function_id,
        env = env_suffix,
    );
    let response = timeseries_db_client
        .query_range(
            &query,
            end_time - Duration::minutes(5),
            end_time,
            TIMESERIES_DB_QUERY_STEP,
        )
        .await?;

    let mut versions = Vec::new();
    for result in response.data.result {
        let Some(version_id) = result
            .metric
            .function_version_id
            .as_deref()
            .and_then(|value| Uuid::parse_str(value).ok())
        else {
            continue;
        };
        let current_instances = result
            .values
            .last()
            .and_then(|(_, value)| value.parse::<f64>().ok())
            .filter(|value| value.is_finite() && *value >= 0.0)
            .map(|value| value.round() as usize)
            .unwrap_or(0);
        versions.push(GatewayTarget {
            function_version_id: version_id,
            nca_id: result.metric.nca_id.unwrap_or_default(),
            current_instances,
            total_current_instances: 0,
        });
    }

    let total_current_instances = versions
        .iter()
        .map(|version| version.current_instances)
        .sum();
    let pinned = routing_cache.gateway_targets.get(function_id);
    let mut target = select_gateway_target(versions, pinned).with_context(|| {
        format!(
            "nvcf_function_info returned no versions for gateway function {}",
            function_id
        )
    })?;
    target.total_current_instances = total_current_instances;
    routing_cache
        .gateway_targets
        .insert(*function_id, target.function_version_id);
    Ok(target)
}

async fn llm_gateway_recently_invoked(
    timeseries_db_client: &TimeseriesDbClient,
    function_id: &Uuid,
    lookback_seconds: u64,
    env: &str,
    ignore_env: bool,
) -> Result<bool> {
    let end_time = Utc::now();
    let lookback_seconds = lookback_seconds.max(1);
    let env_suffix = if ignore_env {
        String::new()
    } else {
        let metric_env = MetricEnvironments::from_config(env);
        format!(r#", aws_env="{}""#, metric_env.aws)
    };
    let query = format!(
        r#"sum by(function_id) (increase(llm_api_gateway_http_requests_total{{function_id="{function_id}"{env_suffix}}}[{lookback_seconds}s])) > 0"#,
    );
    let response = timeseries_db_client
        .query_range(
            &query,
            end_time - Duration::minutes(1),
            end_time,
            TIMESERIES_DB_QUERY_STEP,
        )
        .await?;
    Ok(!response.data.result.is_empty())
}

/// Get current worker count for non-BYOC functions from TimeseriesDb.
/// We use this instead of details.num_workers because the history table is only updated when
/// discovery adds or moves a function; if a function stays in the same table, num_workers is
/// never refreshed and scaling would report stale counts (e.g. 2 when TimeseriesDb shows 3).
///
/// Returns `Ok(None)` when no worker series matched the query at all. Callers use that as the
/// signal to try gateway metrics before control-plane metrics. `Ok(Some(0))` would mean
/// "series exists but count parsed as 0," which we don't
/// expect from this counter shape but is kept distinct from the fallback signal.
///
/// We do not filter or group by nca_id in the query (same as utilization and BYOC queries).
async fn get_current_worker_count_from_timeseries_db(
    timeseries_db_client: &TimeseriesDbClient,
    function_id: &Uuid,
    function_version_id: &Uuid,
    nca_id: &str,
    env: &str,
    ignore_env: bool,
) -> Result<Option<usize>> {
    let env_filter = if ignore_env {
        String::new()
    } else {
        let metric_env = MetricEnvironments::from_config(env);
        format!(r#", environment="{}""#, metric_env.worker)
    };
    let query = format!(
        r#"count by(function_id, function_version_id) (nvcf_worker_service_worker_thread_count_total{{function_id="{}", function_version_id="{}"{}}})"#,
        function_id, function_version_id, env_filter
    );

    const STEP_SECS: i64 = 60;
    // Roll the window back by one step so the trailing point is a fully-settled step boundary.
    // Reading the bleeding edge collapses count() when scrape cycles for some pods haven't
    // landed yet (Prometheus returns data for "now" or future timestamps via staleness lookback,
    // but partial scrapes there make count by(...) drop to whatever subset is fresh).
    let now_secs = Utc::now().timestamp();
    let end_secs = (now_secs / STEP_SECS) * STEP_SECS - STEP_SECS;
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
                return Ok(Some(n));
            }
        }
    }

    tracing::debug!(
        "No worker series for {}:{} (nca_id={}); caller will try gateway metrics",
        function_id,
        function_version_id,
        nca_id
    );
    Ok(None)
}

/// Gather source-specific inputs and retain the selected gateway target for deployment.
#[allow(clippy::too_many_arguments)]
async fn gather_scaling_inputs(
    timeseries_db_client: &TimeseriesDbClient,
    function_id: &Uuid,
    function_version_id: &Uuid,
    nca_id: &str,
    env: &str,
    ignore_env: bool,
    scaling_settings: &ScalingSettings,
    routing_cache: &MetricRoutingCache,
) -> Result<GatheredScalingInputs> {
    let key = (*function_id, *function_version_id);
    let cached_source = routing_cache.sources.get(&key);
    let mut metric_source = cached_source.unwrap_or(MetricSource::WorkerThreads);
    let mut current_instances = 0;
    let mut gateway_target = None;
    let mut cache_source = cached_source.is_none();

    if metric_source == MetricSource::WorkerThreads {
        match get_current_worker_count_from_timeseries_db(
            timeseries_db_client,
            function_id,
            function_version_id,
            nca_id,
            env,
            ignore_env,
        )
        .await
        {
            Ok(Some(count)) => current_instances = count,
            Ok(None) if cached_source.is_none() => {
                metric_source = if llm_gateway_metrics_present(
                    timeseries_db_client,
                    function_id,
                    env,
                    ignore_env,
                )
                .await?
                {
                    MetricSource::LlmGateway
                } else {
                    MetricSource::ControlPlane
                };
            }
            Ok(None) => {}
            Err(error) => {
                tracing::warn!(
                    "TimeseriesDb worker count failed for {}:{} (nca_id={}), using 0: {}",
                    function_id,
                    function_version_id,
                    nca_id,
                    error
                );
                cache_source = false;
            }
        }
    }

    match metric_source {
        MetricSource::WorkerThreads => {}
        MetricSource::LlmGateway => {
            let target = get_gateway_target(
                timeseries_db_client,
                function_id,
                env,
                ignore_env,
                routing_cache,
            )
            .await?;
            current_instances = target.total_current_instances;
            gateway_target = Some(target);
        }
        MetricSource::ControlPlane => {
            current_instances = get_byoc_instance_count(
                timeseries_db_client,
                function_id,
                function_version_id,
                env,
                ignore_env,
            )
            .await
            .unwrap_or_else(|error| {
                tracing::warn!(
                    "CP instance count failed for {}:{}, using 0: {}",
                    function_id,
                    function_version_id,
                    error
                );
                0
            });
        }
    }

    if cache_source {
        routing_cache.sources.insert(key, metric_source);
    }

    let raw_utilization = get_function_utilization_history(
        timeseries_db_client,
        function_id,
        function_version_id,
        env,
        metric_source,
        ignore_env,
        scaling_settings.lookback.as_secs() as i64 / 60,
        scaling_settings.utilization_window_seconds,
    )
    .await?;
    let utilization_samples = sanitize_utilization(raw_utilization);

    let recently_invoked = if metric_source == MetricSource::LlmGateway {
        llm_gateway_recently_invoked(
            timeseries_db_client,
            function_id,
            scaling_settings.scale_to_zero_idle_timeout.as_secs(),
            env,
            ignore_env,
        )
        .await?
    } else {
        !get_recently_invoked_functions(
            timeseries_db_client,
            Some(*function_version_id),
            scaling_settings.scale_to_zero_idle_timeout.as_secs() as i64 / 60,
            env,
            ignore_env,
        )
        .await?
        .is_empty()
    };

    Ok(GatheredScalingInputs {
        inputs: ScalingInputs {
            metric_source,
            current_instances,
            utilization_samples,
            recently_invoked,
        },
        gateway_target,
    })
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
    metric_routing_cache: Arc<MetricRoutingCache>,
) -> Result<()> {
    tracing::info!("Starting P0 autoscaling logic for env: {}", env);

    // Process recently invoked functions (P0 priority)
    make_scaling_requests_for_table(
        cassandra_service,
        timeseries_db_client,
        nvcf_api_service,
        ActiveFunctionTable::RecentlyInvokedFunctions,
        scaling_settings,
        env,
        ignore_env,
        bucket_manager,
        lock_manager,
        function_state_cache,
        metric_routing_cache,
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
    scaling_settings: Arc<ScalingSettings>,
    env: &str,
    ignore_env: bool,
    bucket_manager: &bucket::NodeBucketManager,
    lock_manager: &DistributedLockManager,
    function_state_cache: Arc<FunctionStateCache>,
    metric_routing_cache: Arc<MetricRoutingCache>,
) -> Result<()> {
    let bucket_ranges = bucket_manager.get_all_bucket_ranges();
    let mut task_failures = 0usize;
    let mut first_task_error = None;

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
                let metric_routing_cache = metric_routing_cache.clone();
                let env = env.to_string();
                let bucket_index = *bucket_index;

                join_set.spawn(async move {
                    let _permit = permit; // Keep the permit alive for the duration of this task

                    // Look up cached state from the previous scaling cycle (written by nvcf_client
                    // after each NVCF API call). None means we haven't successfully called NVCF yet.
                    let cached: Option<FunctionCachedState> = function_state_cache
                        .get(&(function.function_id, function.function_version_id));
                    let last_predicted_instance_count = cached
                        .as_ref()
                        .and_then(|c| c.last_predicted_desired_instance_count)
                        .unwrap_or(0) as usize;

                    // Acquire all metrics into one sanitized struct, then run the single
                    // decision path that is shared by every metric source. NaN handling,
                    // metric-source selection, and scale-to-zero all live behind these calls.
                    let gathered = gather_scaling_inputs(
                        &timeseries_db_client,
                        &function.function_id,
                        &function.function_version_id,
                        function.nca_id.as_str(),
                        &env,
                        ignore_env,
                        &scaling_settings,
                        &metric_routing_cache,
                    )
                    .await
                    .with_context(|| {
                        format!(
                            "gathering scaling inputs for {}:{}",
                            function.function_id, function.function_version_id
                        )
                    })?;

                    if gathered.gateway_target.as_ref().is_some_and(|target| {
                        target.function_version_id != function.function_version_id
                    }) {
                        return Ok(());
                    }

                    let current_instances = gathered
                        .gateway_target
                        .as_ref()
                        .map(|target| target.current_instances)
                        .unwrap_or(gathered.inputs.current_instances);
                    let target_nca_id = gathered
                        .gateway_target
                        .as_ref()
                        .map(|target| target.nca_id.clone())
                        .unwrap_or_else(|| function.nca_id.clone());
                    let inputs = gathered.inputs;

                    tracing::info!(
                        "Scaling inputs for {}:{} - current_instances: {}, source: {:?}, samples: {}, recently_invoked: {}",
                        function.function_id,
                        function.function_version_id,
                        inputs.current_instances,
                        inputs.metric_source,
                        inputs.utilization_samples.len(),
                        inputs.recently_invoked,
                    );

                    let policy = scaling_settings
                        .get_policy_for_function(&function.function_version_id)
                        .await;

                    let Some(decision) = decide_scaling(
                        &inputs,
                        &policy,
                        scaling_settings.decay_factor,
                        last_predicted_instance_count,
                    ) else {
                        tracing::info!(
                            "Function {}:{} reports 0 active instances but {} were requested last cycle - skipping until the new workers report in",
                            function.function_id,
                            function.function_version_id,
                            last_predicted_instance_count
                        );
                        return Ok(());
                    };

                    let desired_instance_count = if let Some(target) = &gathered.gateway_target {
                        gateway_target_desired_instances(
                            decision.desired_instances,
                            target.total_current_instances,
                            target.current_instances,
                        ) as i32
                    } else {
                        decision.desired_instances as i32
                    };

                    tracing::info!(
                        "Scaling decision for {}:{} - total current: {}, total desired: {}, target current: {}, target desired: {}, avg_utilization: {:.6}% (recently_invoked: {})",
                        function.function_id,
                        function.function_version_id,
                        inputs.current_instances,
                        decision.desired_instances,
                        current_instances,
                        desired_instance_count,
                        decision.average_utilization,
                        inputs.recently_invoked,
                    );
                    // desired == 0 is only reachable via decide_scaling's scale-to-zero
                    // override; log the reason explicitly so it is greppable in prod.
                    if decision.desired_instances == 0 {
                        tracing::info!(
                            "Function {}:{} scaling to 0 - idle (no invocations in window) and utilization {:.1}% < scale-down threshold {:.1}%",
                            function.function_id,
                            function.function_version_id,
                            decision.average_utilization,
                            policy.thresholds.scale_down_threshold,
                        );
                    }
                    let utilization = Some(decision.average_utilization as f64);

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
                        nca_id: target_nca_id,
                        required_number_of_instances: desired_instance_count,
                        recently_invoked: inputs.recently_invoked,
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

            // Wait for all scaling requests in this bucket to complete. JoinSet
            // returns two result layers: the task's Result and Tokio's JoinError.
            // Count both while continuing to drain independent work.
            let (failure_count, error) = drain_scaling_tasks(&mut join_set).await;
            task_failures += failure_count;
            if first_task_error.is_none() {
                first_task_error = error.map(|error| (*bucket_index, error));
            }

            tracing::debug!("Completed processing bucket {}", bucket_index);
        } else {
            tracing::info!(
                "Unable to acquire lock for bucket {}, skipping",
                bucket_index
            );
        }
    }

    if let Some((bucket_index, error)) = first_task_error {
        Err(error.context(format!(
            "{task_failures} per-function scaling task(s) failed for table {table:?}; first failure was in bucket {bucket_index}"
        )))
    } else {
        Ok(())
    }
}

async fn drain_scaling_tasks(join_set: &mut JoinSet<Result<()>>) -> (usize, Option<anyhow::Error>) {
    let mut failures = 0usize;
    let mut first_task_error = None;
    let mut first_join_error = None;
    while let Some(join_result) = join_set.join_next().await {
        match join_result {
            Ok(Ok(())) => {}
            Ok(Err(error)) => {
                failures += 1;
                if first_task_error.is_none() {
                    first_task_error = Some(error);
                }
            }
            Err(join_error) => {
                failures += 1;
                if first_join_error.is_none() {
                    first_join_error = Some(join_error.into());
                }
            }
        }
    }
    (failures, first_task_error.or(first_join_error))
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
    use crate::timeseries_db::TimeseriesDbSettings;
    use uuid::Uuid;

    // ---- Helpers for the metric-acquisition tests ----

    /// Tiny retry budget so error paths resolve in milliseconds, not seconds.
    fn fast_backoff() -> backon::ExponentialBuilder {
        backon::ExponentialBuilder::default()
            .with_max_times(1)
            .with_min_delay(StdDuration::from_millis(1))
            .with_max_delay(StdDuration::from_millis(2))
    }

    /// A TimeseriesDb client (auth disabled) pointed at a mockito server.
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

    /// A VictoriaMetrics matrix response with a single series and one sample.
    /// `metric_fields` is the raw JSON body of the `metric` object.
    fn vm_series(metric_fields: &str, value: &str) -> String {
        format!(
            r#"{{"status":"success","data":{{"resultType":"matrix","result":[{{"metric":{{{metric_fields}}},"values":[[1700000000,"{value}"]]}}]}}}}"#
        )
    }

    /// A VictoriaMetrics matrix response with no series (empty result).
    fn vm_empty() -> String {
        r#"{"status":"success","data":{"resultType":"matrix","result":[]}}"#.to_string()
    }

    async fn gather_for_test(
        client: &TimeseriesDbClient,
        function_id: &Uuid,
        function_version_id: &Uuid,
    ) -> GatheredScalingInputs {
        gather_scaling_inputs(
            client,
            function_id,
            function_version_id,
            "nca",
            "stg",
            true,
            &ScalingSettings::default(),
            &new_metric_routing_cache(),
        )
        .await
        .expect("gather scaling inputs")
    }

    fn gateway_version(function_version_id: Uuid, current_instances: usize) -> GatewayTarget {
        GatewayTarget {
            function_version_id,
            nca_id: "nca".to_string(),
            current_instances,
            total_current_instances: 0,
        }
    }

    #[test]
    fn gateway_target_prefers_and_sticks_to_an_active_version() {
        let idle = Uuid::new_v4();
        let active = Uuid::new_v4();
        let other_active = Uuid::new_v4();

        let selected = select_gateway_target(
            vec![gateway_version(idle, 0), gateway_version(active, 2)],
            Some(idle),
        )
        .expect("active target");
        assert_eq!(selected.function_version_id, active);

        let selected = select_gateway_target(
            vec![gateway_version(active, 2), gateway_version(other_active, 3)],
            Some(active),
        )
        .expect("pinned active target");
        assert_eq!(selected.function_version_id, active);

        let selected = select_gateway_target(
            vec![gateway_version(idle, 0), gateway_version(active, 0)],
            Some(idle),
        )
        .expect("pinned idle target");
        assert_eq!(selected.function_version_id, idle);
    }

    #[test]
    fn gateway_delta_is_applied_only_to_the_selected_version() {
        assert_eq!(gateway_target_desired_instances(12, 10, 4), 6);
        assert_eq!(gateway_target_desired_instances(10, 10, 4), 4);
        assert_eq!(gateway_target_desired_instances(6, 10, 4), 0);
    }

    #[test]
    fn metric_environments_use_metric_family_label_values() {
        assert_eq!(
            MetricEnvironments::from_config("stg"),
            MetricEnvironments {
                aws: "stg",
                worker: "stage",
                control_plane: "staging",
            }
        );
        assert_eq!(
            MetricEnvironments::from_config("prd"),
            MetricEnvironments {
                aws: "prd",
                worker: "prod",
                control_plane: "production",
            }
        );
        assert_eq!(
            MetricEnvironments::from_config("prod"),
            MetricEnvironments::from_config("prd")
        );
    }

    #[tokio::test]
    async fn test_control_plane_utilization_query_aggregates_shared_function_pool() {
        let fid = Uuid::new_v4();
        let fvid = Uuid::new_v4();
        let expected_query = format!(
            r#"100 * sum by(function_id, function_version_id) (rate(function_request_latency_sum{{function_id="{fid}", function_version_id="{fvid}"}}[2m])) /
            (avg by(function_id, function_version_id) (nvcf_function_instances_current{{function_id="{fid}", function_version_id="{fvid}"}}) * avg by(function_id, function_version_id) (nvcf_function_concurrency{{function_id="{fid}", function_version_id="{fvid}"}})) or vector(0)"#
        );
        let mut server = mockito::Server::new_async().await;
        let _utilization = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::UrlEncoded(
                "query".to_string(),
                expected_query,
            ))
            .with_status(200)
            .with_body(vm_series(
                &format!(r#""function_id":"{fid}","function_version_id":"{fvid}""#),
                "42",
            ))
            .create_async()
            .await;

        let utilization = get_function_utilization_history(
            &ts_client(server.url()),
            &fid,
            &fvid,
            "stg",
            MetricSource::ControlPlane,
            true,
            5,
            60,
        )
        .await
        .expect("control-plane utilization");

        assert_eq!(utilization, vec![(1_700_000_000, "42".to_string())]);
    }

    #[tokio::test]
    async fn test_control_plane_utilization_query_uses_aws_environment_labels() {
        let fid = Uuid::new_v4();
        let fvid = Uuid::new_v4();
        let mut server = mockito::Server::new_async().await;

        for (configured_env, aws_env) in [("prd", "prd"), ("stg", "stg")] {
            let expected_query = format!(
                r#"100 * sum by(function_id, function_version_id) (rate(function_request_latency_sum{{function_id="{fid}", function_version_id="{fvid}", aws_env="{aws_env}"}}[2m])) /
            (avg by(function_id, function_version_id) (nvcf_function_instances_current{{function_id="{fid}", function_version_id="{fvid}", aws_env="{aws_env}"}}) * avg by(function_id, function_version_id) (nvcf_function_concurrency{{function_id="{fid}", function_version_id="{fvid}", aws_env="{aws_env}"}})) or vector(0)"#
            );
            let query_mock = server
                .mock("GET", "/api/v1/query_range")
                .match_query(mockito::Matcher::UrlEncoded(
                    "query".to_string(),
                    expected_query,
                ))
                .with_status(200)
                .with_body(vm_series(
                    &format!(r#""function_id":"{fid}","function_version_id":"{fvid}""#),
                    "42",
                ))
                .expect(1)
                .create_async()
                .await;

            let utilization = get_function_utilization_history(
                &ts_client(server.url()),
                &fid,
                &fvid,
                configured_env,
                MetricSource::ControlPlane,
                false,
                5,
                60,
            )
            .await
            .expect("control-plane utilization");

            assert_eq!(utilization, vec![(1_700_000_000, "42".to_string())]);
            query_mock.assert_async().await;
        }
    }

    #[tokio::test]
    async fn drain_scaling_tasks_preserves_inner_and_counts_join_errors() {
        let mut join_set: JoinSet<Result<()>> = JoinSet::new();
        join_set.spawn(async { Ok(()) });
        join_set.spawn(async { Err(anyhow::anyhow!("sentinel task error")) });
        join_set.spawn(async {
            panic!("sentinel task panic");
            #[allow(unreachable_code)]
            Ok(())
        });

        let (failures, error) = drain_scaling_tasks(&mut join_set).await;
        assert_eq!(failures, 2);
        assert!(format!("{:#}", error.expect("first task error")).contains("sentinel task error"));
    }

    #[tokio::test]
    async fn test_gather_uses_gateway_and_selects_active_version() {
        let fid = Uuid::new_v4();
        let idle_version = Uuid::new_v4();
        let active_version = Uuid::new_v4();
        let mut server = mockito::Server::new_async().await;

        let _wc = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex("worker_thread_count_total".into()))
            .with_status(200)
            .with_body(vm_empty())
            .create_async()
            .await;
        let _gateway = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex("http_requests_total".into()))
            .with_status(200)
            .with_body(vm_series(&format!(r#""function_id":"{fid}""#), "1"))
            .expect_at_least(2)
            .create_async()
            .await;
        let versions = format!(
            r#"{{"status":"success","data":{{"resultType":"matrix","result":[
                {{"metric":{{"function_id":"{fid}","function_version_id":"{idle_version}","nca_id":"nca"}},"values":[[1700000000,"0"]]}},
                {{"metric":{{"function_id":"{fid}","function_version_id":"{active_version}","nca_id":"nca"}},"values":[[1700000000,"2"]]}}
            ]}}}}"#
        );
        let _info = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex("nvcf_function_info".into()))
            .with_status(200)
            .with_body(versions)
            .create_async()
            .await;
        let _samples = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex(
                "http_request_duration_seconds_sum".into(),
            ))
            .with_status(200)
            .with_body(vm_series(&format!(r#""function_id":"{fid}""#), "25"))
            .create_async()
            .await;

        let gathered = gather_for_test(&ts_client(server.url()), &fid, &idle_version).await;

        assert_eq!(gathered.inputs.metric_source, MetricSource::LlmGateway);
        assert_eq!(gathered.inputs.current_instances, 2);
        assert_eq!(gathered.inputs.utilization_samples, vec![25.0]);
        assert!(gathered.inputs.recently_invoked);
        let target = gathered.gateway_target.expect("gateway target");
        assert_eq!(target.function_version_id, active_version);
        assert_eq!(target.nca_id, "nca");
    }

    #[tokio::test]
    async fn gateway_queries_use_normalized_aws_env_concurrency_and_request_counter() {
        let fid = Uuid::new_v4();
        let fvid = Uuid::new_v4();
        let mut server = mockito::Server::new_async().await;
        let function_series = vm_series(&format!(r#""function_id":"{fid}""#), "1");

        let _present = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::AllOf(vec![
                mockito::Matcher::Regex("count.*http_requests_total".into()),
                mockito::Matcher::Regex("aws_env.*prd".into()),
            ]))
            .with_status(200)
            .with_body(function_series.clone())
            .create_async()
            .await;
        assert!(
            llm_gateway_metrics_present(&ts_client(server.url()), &fid, "prod", false)
                .await
                .expect("gateway presence query")
        );

        let _target = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::AllOf(vec![
                mockito::Matcher::Regex("nvcf_function_info".into()),
                mockito::Matcher::Regex("aws_env.*prd".into()),
            ]))
            .with_status(200)
            .with_body(vm_series(
                &format!(r#""function_id":"{fid}","function_version_id":"{fvid}","nca_id":"nca""#),
                "1",
            ))
            .create_async()
            .await;
        let target = get_gateway_target(
            &ts_client(server.url()),
            &fid,
            "prod",
            false,
            &new_metric_routing_cache(),
        )
        .await
        .expect("gateway target query");
        assert_eq!(target.function_version_id, fvid);

        let _utilization = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::AllOf(vec![
                mockito::Matcher::Regex("http_request_duration_seconds_sum".into()),
                mockito::Matcher::Regex("nvcf_function_concurrency".into()),
                mockito::Matcher::Regex("aws_env.*prd".into()),
            ]))
            .with_status(200)
            .with_body(function_series.clone())
            .create_async()
            .await;
        assert_eq!(
            get_function_utilization_history(
                &ts_client(server.url()),
                &fid,
                &fvid,
                "prod",
                MetricSource::LlmGateway,
                false,
                5,
                70,
            )
            .await
            .expect("gateway utilization query"),
            vec![(1700000000, "1".to_string())]
        );

        let _recent = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::AllOf(vec![
                mockito::Matcher::Regex("increase.*http_requests_total".into()),
                mockito::Matcher::Regex("aws_env.*prd".into()),
                mockito::Matcher::Regex("1s".into()),
            ]))
            .with_status(200)
            .with_body(function_series)
            .create_async()
            .await;
        assert!(
            llm_gateway_recently_invoked(&ts_client(server.url()), &fid, 0, "prod", false)
                .await
                .expect("gateway recent invocation query")
        );
    }

    /// No worker or gateway series -> fall back to control-plane metrics.
    #[tokio::test]
    async fn test_gather_falls_back_to_control_plane_when_other_sources_are_absent() {
        let fid = Uuid::new_v4();
        let fvid = Uuid::new_v4();
        let mut server = mockito::Server::new_async().await;
        let _wc = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex("worker_thread_count_total".into()))
            .with_status(200)
            .with_body(vm_empty())
            .create_async()
            .await;
        let _gateway = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex("http_requests_total".into()))
            .with_status(200)
            .with_body(vm_empty())
            .create_async()
            .await;
        let _byoc = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex(
                "nvcf_function_instances_current".into(),
            ))
            .with_status(200)
            .with_body(vm_series(
                &format!(r#""function_id":"{fid}","function_version_id":"{fvid}""#),
                "7",
            ))
            .expect_at_least(2)
            .create_async()
            .await;
        let _invocation = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex("function_request%7B".into()))
            .with_status(200)
            .with_body(vm_empty())
            .create_async()
            .await;
        let _grpc = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex("function_request_total".into()))
            .with_status(200)
            .with_body(vm_empty())
            .create_async()
            .await;

        let gathered = gather_for_test(&ts_client(server.url()), &fid, &fvid).await;

        assert_eq!(gathered.inputs.current_instances, 7);
        assert_eq!(gathered.inputs.metric_source, MetricSource::ControlPlane);
    }

    #[tokio::test]
    async fn test_byoc_instance_count_uses_control_plane_environment_labels() {
        let fid = Uuid::new_v4();
        let fvid = Uuid::new_v4();
        let mut server = mockito::Server::new_async().await;
        let body = vm_series(
            &format!(r#""function_id":"{fid}","function_version_id":"{fvid}""#),
            "3",
        );

        let _prd = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex("production".into()))
            .with_status(200)
            .with_body(body.clone())
            .create_async()
            .await;
        let _stg = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Regex("staging".into()))
            .with_status(200)
            .with_body(body)
            .create_async()
            .await;

        let client = ts_client(server.url());

        assert_eq!(
            get_byoc_instance_count(&client, &fid, &fvid, "prd", false)
                .await
                .expect("prod cp instance count"),
            3
        );
        assert_eq!(
            get_byoc_instance_count(&client, &fid, &fvid, "stg", false)
                .await
                .expect("stage cp instance count"),
            3
        );
    }

    /// Happy path: worker count, utilization, and recent invocations are gathered
    /// and sanitized into one ScalingInputs. A single response satisfies every
    /// query (count, utilization, invocation), so we assert the assembled shape.
    #[tokio::test]
    async fn test_gather_scaling_inputs_assembles_worker_path() {
        let fid = Uuid::new_v4();
        let fvid = Uuid::new_v4();
        let mut server = mockito::Server::new_async().await;
        let body = vm_series(
            &format!(r#""function_id":"{fid}","function_version_id":"{fvid}","nca_id":"nca""#),
            "5",
        );
        let _all = server
            .mock("GET", "/api/v1/query_range")
            .match_query(mockito::Matcher::Any)
            .with_status(200)
            .with_body(body)
            .expect_at_least(1)
            .create_async()
            .await;

        let client = ts_client(server.url());
        let settings = ScalingSettings::default();
        let routing_cache = new_metric_routing_cache();
        let gathered = gather_scaling_inputs(
            &client,
            &fid,
            &fvid,
            "nca",
            "stg",
            true,
            &settings,
            &routing_cache,
        )
        .await
        .expect("gather inputs");
        let inputs = gathered.inputs;

        assert_eq!(inputs.current_instances, 5);
        assert_eq!(inputs.metric_source, MetricSource::WorkerThreads);
        assert_eq!(inputs.utilization_samples, vec![5.0]);
        assert!(inputs.recently_invoked);
    }

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

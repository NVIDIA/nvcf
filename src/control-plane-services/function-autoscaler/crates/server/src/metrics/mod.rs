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

use std::{net::SocketAddr, time::Duration};

use crate::nvcf_api::nvcf_client::NVCF_API_BUCKET_LOCK_PREFIX;
use crate::settings::MetricsSettings;
use crate::work::discovery::LOCK_NAME_FUNCTION_DISCOVERY;
use crate::work::CALCULATE_UTILIZATION_LOCK_PREFIX;
use metrics::{counter, gauge, histogram};
use metrics_exporter_prometheus::{Matcher, PrometheusBuilder};
use metrics_util::MetricKindMask;

struct Metric {
    name: &'static str,
    description: &'static str,
}
pub enum AutoscalingStatus {
    Ok,
    Error,
}

const AUTOSCALING_STATUS: Metric = Metric {
    name: "nvcf_autoscaler.autoscaling.status",
    description: "Autoscaling status for function ID and version ID",
};

const AUTOSCALING_QUEUE_SIZE: Metric = Metric {
    name: "nvcf_autoscaler.queue.size",
    description: "Current number of requests in the NVCF API queue",
};

const AUTOSCALING_QUEUE_CAPACITY: Metric = Metric {
    name: "nvcf_autoscaler.queue.capacity",
    description: "Maximum capacity of the NVCF API queue",
};

const AUTOSCALING_REQUESTS_QUEUED_TOTAL: Metric = Metric {
    name: "nvcf_autoscaler.requests.queued_total",
    description: "Total number of requests queued",
};

const AUTOSCALING_REQUESTS_REJECTED_TOTAL: Metric = Metric {
    name: "nvcf_autoscaler.requests.rejected_total",
    description: "Total number of requests rejected due to full queue",
};

const AUTOSCALING_REQUESTS_RATE_LIMITED_TOTAL: Metric = Metric {
    name: "nvcf_autoscaler.requests.rate_limited_total",
    description: "Total number of requests rate limited",
};

const AUTOSCALING_REQUESTS_PROCESSED_TOTAL: Metric = Metric {
    name: "nvcf_autoscaler.requests.processed_total",
    description: "Total number of requests successfully processed",
};

const AUTOSCALING_CURRENT_INSTANCES: Metric = Metric {
    name: "nvcf_autoscaler.scaling.current_instances",
    description: "Current number of instances for a function",
};

const AUTOSCALING_DESIRED_INSTANCES: Metric = Metric {
    name: "nvcf_autoscaler.scaling.desired_instances",
    description: "Desired number of instances calculated by scaling logic",
};

const AUTOSCALING_UTILIZATION: Metric = Metric {
    name: "nvcf_autoscaler.scaling.utilization",
    description: "Current utilization percentage for a function",
};

// Metric to capture TimeseriesDb authN failures (actionable on Autoscaler team)
const AUTOSCALING_TSDB_AUTH_FAILURE_TOTAL: Metric = Metric {
    name: "nvcf_autoscaler.timeseries_db.auth_failure_total",
    description: "Total number of TimeseriesDb authentication failures",
};

// Metric to capture server-side TimeseriesDb query failures (actionable on DPE team)
const AUTOSCALING_TSDB_SERVER_SIDE_FAILURE_TOTAL: Metric = Metric {
    name: "nvcf_autoscaler.timeseries_db.server_side_failure_total",
    description: "Total number of TimeseriesDb query failures",
};

const AUTOSCALING_TSDB_REQUESTS_TOTAL: Metric = Metric {
    name: "nvcf_autoscaler.timeseries_db.requests_total",
    description: "Total number of TimeseriesDb API requests by status",
};

const AUTOSCALING_TSDB_REQUEST_DURATION: Metric = Metric {
    name: "nvcf_autoscaler.timeseries_db.request_duration_milliseconds",
    description:
        "Duration of TimeseriesDb API requests in milliseconds (includes count via _count)",
};

// Metric to track which table a function is currently in
const AUTOSCALING_FUNCTION_TABLE_STATE: Metric = Metric {
    name: "nvcf_autoscaler.function_table_state",
    description: "Current table state for a function (1=recently_invoked, 2=running_without_invocations, 0=not_tracked)",
};

// Duration of function discovery thread
const AUTOSCALING_FUNCTION_DISCOVERY_DURATION: Metric = Metric {
    name: "nvcf_autoscaler.function_discovery_duration_seconds",
    description: "Duration of function discovery thread",
};

const AUTOSCALING_CASSANDRA_HEALTH_STATUS: Metric = Metric {
    name: "nvcf_autoscaler.cassandra.health_status",
    description: "Cassandra health check status (1 if healthy, 0 if not)",
};

const AUTOSCALING_HEALTH_OVERALL_STATUS: Metric = Metric {
    name: "nvcf_autoscaler.health.overall_status",
    description: "Overall service health status (1 if healthy, 0 if not)",
};

const AUTOSCALING_HEALTH_COMPONENT_STATUS: Metric = Metric {
    name: "nvcf_autoscaler.health.component_status",
    description: "Per-component health status (1 if healthy, 0 if not)",
};

const AUTOSCALING_OAUTH2_CLIENT_TOKEN_REFRESH_FAILURE_TOTAL: Metric = Metric {
    name: "nvcf_autoscaler.oauth2_client.token_refresh_failure_total",
    description: "Total number of OAuth2 client token refresh failures",
};

const AUTOSCALING_DISTRIBUTED_LOCK: Metric = Metric {
    name: "nvcf_autoscaler.distributed_lock",
    description: "Distributed lock status (1=acquired, 0=released)",
};

const AUTOSCALING_DISTRIBUTED_LOCK_ACQUISITION_FAILURES_TOTAL: Metric = Metric {
    name: "nvcf_autoscaler.distributed_lock.acquisition_failures_total",
    description:
        "Total number of distributed lock acquisition failures (lock already held by another node)",
};

const AUTOSCALING_UTILIZATION_DATA_AGE: Metric = Metric {
    name: "nvcf_autoscaler.processing.utilization_data_age_milliseconds",
    description:
        "Age in milliseconds of utilization data when successfully processing scaling requests",
};

const NVCF_API_REQUEST_DURATION: Metric = Metric {
    name: "nvcf_autoscaler.nvcf_api.request_duration_milliseconds",
    description: "Duration of NVCF API requests in milliseconds (includes count via _count)",
};

const OAUTH2_API_REQUEST_DURATION: Metric = Metric {
    name: "nvcf_autoscaler.oauth2_api.request_duration_milliseconds",
    description: "Duration of OAuth2 API requests in milliseconds (includes count via _count)",
};

const GAUGES: [Metric; 11] = [
    AUTOSCALING_STATUS,
    AUTOSCALING_QUEUE_SIZE,
    AUTOSCALING_QUEUE_CAPACITY,
    AUTOSCALING_REQUESTS_RATE_LIMITED_TOTAL,
    AUTOSCALING_CURRENT_INSTANCES,
    AUTOSCALING_DESIRED_INSTANCES,
    AUTOSCALING_UTILIZATION,
    AUTOSCALING_FUNCTION_TABLE_STATE,
    AUTOSCALING_CASSANDRA_HEALTH_STATUS,
    AUTOSCALING_HEALTH_OVERALL_STATUS,
    AUTOSCALING_HEALTH_COMPONENT_STATUS,
];

const COUNTERS: [Metric; 9] = [
    AUTOSCALING_REQUESTS_QUEUED_TOTAL,
    AUTOSCALING_REQUESTS_REJECTED_TOTAL,
    AUTOSCALING_REQUESTS_PROCESSED_TOTAL,
    AUTOSCALING_TSDB_AUTH_FAILURE_TOTAL,
    AUTOSCALING_OAUTH2_CLIENT_TOKEN_REFRESH_FAILURE_TOTAL,
    AUTOSCALING_TSDB_SERVER_SIDE_FAILURE_TOTAL,
    AUTOSCALING_DISTRIBUTED_LOCK,
    AUTOSCALING_DISTRIBUTED_LOCK_ACQUISITION_FAILURES_TOTAL,
    AUTOSCALING_TSDB_REQUESTS_TOTAL,
];

const HISTOGRAMS: [Metric; 5] = [
    AUTOSCALING_UTILIZATION_DATA_AGE,
    NVCF_API_REQUEST_DURATION,
    OAUTH2_API_REQUEST_DURATION,
    AUTOSCALING_TSDB_REQUEST_DURATION,
    AUTOSCALING_FUNCTION_DISCOVERY_DURATION,
];

pub fn record_request_queued() {
    counter!(AUTOSCALING_REQUESTS_QUEUED_TOTAL.name).increment(1);
}

pub fn record_request_rejected() {
    counter!(AUTOSCALING_REQUESTS_REJECTED_TOTAL.name).increment(1);
}

pub fn record_request_processed() {
    counter!(AUTOSCALING_REQUESTS_PROCESSED_TOTAL.name).increment(1);
}

pub fn record_request_rate_limited() {
    counter!(AUTOSCALING_REQUESTS_RATE_LIMITED_TOTAL.name).increment(1);
}

pub fn update_queue_metrics(current_size: usize, capacity: usize) {
    gauge!(AUTOSCALING_QUEUE_SIZE.name).set(current_size as f64);
    gauge!(AUTOSCALING_QUEUE_CAPACITY.name).set(capacity as f64);
}

pub fn record_autoscaling_status(function_id: String, function_version_id: String, reason: f64) {
    let labels = [
        ("function_id", function_id),
        ("function_version_id", function_version_id),
    ];

    gauge!(AUTOSCALING_STATUS.name, &labels).set(reason);
}

pub fn record_timeseries_db_auth_failure() {
    counter!(AUTOSCALING_TSDB_AUTH_FAILURE_TOTAL.name).increment(1);
}

pub fn record_timeseries_db_query_failure() {
    counter!(AUTOSCALING_TSDB_SERVER_SIDE_FAILURE_TOTAL.name).increment(1);
}

pub fn record_timeseries_db_request(status: &str, duration_milliseconds: f64) {
    // Counter for request status
    let status_labels = [("status", status.to_string())];
    counter!(AUTOSCALING_TSDB_REQUESTS_TOTAL.name, &status_labels).increment(1);

    let duration_labels = [("status", status.to_string())];
    histogram!(AUTOSCALING_TSDB_REQUEST_DURATION.name, &duration_labels)
        .record(duration_milliseconds);
}

// Test function to verify the metric is working
pub fn test_function_table_state_metric() {
    tracing::info!("Testing function table state metric");
    record_function_table_state(
        "test-function-id".to_string(),
        "test-version-id".to_string(),
        FunctionTableState::RecentlyInvoked,
    );
}

pub fn record_cassandra_health_status(is_healthy: bool) {
    let value = if is_healthy { 1.0 } else { 0.0 };
    gauge!(AUTOSCALING_CASSANDRA_HEALTH_STATUS.name).set(value);
}

pub fn record_scaling_decision(
    function_id: String,
    function_version_id: String,
    current_instances: usize,
    desired_instances: usize,
    utilization: Option<f64>,
) {
    let labels = [
        ("function_id", function_id),
        ("function_version_id", function_version_id),
    ];

    gauge!(AUTOSCALING_CURRENT_INSTANCES.name, &labels).set(current_instances as f64);
    gauge!(AUTOSCALING_DESIRED_INSTANCES.name, &labels).set(desired_instances as f64);

    // Only record utilization if we have the data
    if let Some(util) = utilization {
        gauge!(AUTOSCALING_UTILIZATION.name, &labels).set(util);
    }
}

pub fn record_function_table_state(
    function_id: String,
    function_version_id: String,
    table_state: FunctionTableState,
) {
    let labels = [
        ("function_id", function_id.clone()),
        ("function_version_id", function_version_id.clone()),
    ];

    let state_value = match table_state {
        FunctionTableState::RecentlyInvoked => 1.0,
        FunctionTableState::RunningWithoutInvocations => 2.0,
        FunctionTableState::NotTracked => 0.0,
    };

    tracing::debug!(
        "Recording function table state: {}:{} = {} ({:?})",
        function_id,
        function_version_id,
        state_value,
        table_state
    );

    gauge!(AUTOSCALING_FUNCTION_TABLE_STATE.name, &labels).set(state_value);
}

pub fn record_function_discovery_duration(duration: Duration) {
    histogram!(AUTOSCALING_FUNCTION_DISCOVERY_DURATION.name).record(duration.as_secs_f64());
}

pub fn record_distributed_lock_acquisition_failure(lock_name: &str) {
    // Determine lock type based on lock name pattern
    let lock_type = match lock_name {
        name if name == LOCK_NAME_FUNCTION_DISCOVERY => LOCK_NAME_FUNCTION_DISCOVERY,
        name if name.starts_with(NVCF_API_BUCKET_LOCK_PREFIX) => NVCF_API_BUCKET_LOCK_PREFIX,
        name if name.starts_with(CALCULATE_UTILIZATION_LOCK_PREFIX) => {
            CALCULATE_UTILIZATION_LOCK_PREFIX
        }
        _ => "other",
    };

    let labels = [
        ("lock_name", lock_name.to_string()),
        ("lock_type", lock_type.to_string()),
    ];
    counter!(
        AUTOSCALING_DISTRIBUTED_LOCK_ACQUISITION_FAILURES_TOTAL.name,
        &labels
    )
    .increment(1);
}

#[derive(Debug, Clone, Copy)]
pub enum FunctionTableState {
    RecentlyInvoked,
    RunningWithoutInvocations,
    NotTracked,
}

pub fn record_oauth2_client_token_refresh_failure() {
    counter!(AUTOSCALING_OAUTH2_CLIENT_TOKEN_REFRESH_FAILURE_TOTAL.name).increment(1);
}

pub fn record_utilization_data_age(delta_milliseconds: i64) {
    histogram!(AUTOSCALING_UTILIZATION_DATA_AGE.name).record(delta_milliseconds as f64);
}

pub fn record_nvcf_api_request(endpoint: &str, status: &str, duration_milliseconds: f64) {
    let labels = [
        ("endpoint", endpoint.to_string()),
        ("status", status.to_string()),
    ];

    histogram!(NVCF_API_REQUEST_DURATION.name, &labels).record(duration_milliseconds);
}

pub fn record_oauth2_api_request(status: &str, duration_milliseconds: f64) {
    let labels = [("status", status.to_string())];

    histogram!(OAUTH2_API_REQUEST_DURATION.name, &labels).record(duration_milliseconds);
}

pub fn record_health_overall_status(is_healthy: bool) {
    let value = if is_healthy { 1.0 } else { 0.0 };
    gauge!(AUTOSCALING_HEALTH_OVERALL_STATUS.name).set(value);
}

pub fn record_health_component_status(component_name: &str, is_healthy: bool) {
    let value = if is_healthy { 1.0 } else { 0.0 };
    let labels = [("component", component_name.to_string())];
    gauge!(AUTOSCALING_HEALTH_COMPONENT_STATUS.name, &labels).set(value);
}

pub fn init_metrics(settings: &MetricsSettings) -> anyhow::Result<()> {
    let mut metrics_endpoint = "127.0.0.1:41337";
    // Find the prometheus exporter and retrieve the endpoint
    if let Some(prometheus_exporter) = settings
        .exporters
        .iter()
        .find(|exp| exp.exporter == "prometheus")
    {
        metrics_endpoint = prometheus_exporter
            .endpoint
            .trim_start_matches("http://")
            .trim_start_matches("https://");
    }
    let metrics_endpoint: SocketAddr = metrics_endpoint.parse()?;

    PrometheusBuilder::new()
        .idle_timeout(
            MetricKindMask::COUNTER | MetricKindMask::GAUGE | MetricKindMask::HISTOGRAM,
            Some(Duration::from_secs(settings.idle_timeout_seconds)),
        )
        .with_http_listener(metrics_endpoint)
        .set_buckets_for_metric(
            Matcher::Full(AUTOSCALING_FUNCTION_DISCOVERY_DURATION.name.to_string()),
            &[1.0, 5.0, 10.0, 15.0, 30.0, 60.0, 120.0],
        )?
        .set_buckets_for_metric(
            Matcher::Full(AUTOSCALING_TSDB_REQUEST_DURATION.name.to_string()),
            &[30.0, 60.0, 120.0, 300.0, 600.0, 900.0, 1200.0],
        )?
        .set_buckets_for_metric(
            Matcher::Full(NVCF_API_REQUEST_DURATION.name.to_string()),
            &[30.0, 60.0, 120.0, 300.0, 600.0, 900.0, 1200.0],
        )?
        .set_buckets_for_metric(
            Matcher::Full(OAUTH2_API_REQUEST_DURATION.name.to_string()),
            &[30.0, 60.0, 120.0, 300.0, 600.0, 900.0, 1200.0],
        )?
        .install()?;

    for name in GAUGES {
        register_gauge(name);
    }

    for name in COUNTERS {
        register_counter(name);
    }

    for name in HISTOGRAMS {
        register_histogram(name);
    }

    let collector = metrics_process::Collector::default();
    collector.describe();
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(Duration::from_secs(5));
        loop {
            interval.tick().await;
            collector.collect();
        }
    });
    Ok(())
}

fn register_gauge(metric: Metric) {
    metrics::describe_gauge!(metric.name, metric.description);
}

fn register_counter(metric: Metric) {
    metrics::describe_counter!(metric.name, metric.description);
}

fn register_histogram(metric: Metric) {
    metrics::describe_histogram!(metric.name, metric.description);
}

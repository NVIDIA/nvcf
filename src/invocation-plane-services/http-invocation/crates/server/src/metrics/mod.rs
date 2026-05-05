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

use crate::telemetry::settings::MetricsSettings;
use async_nats::{Event, Statistics};
use axum_prometheus::AXUM_HTTP_REQUESTS_DURATION_SECONDS;
use metrics::{counter, histogram};
use metrics_exporter_prometheus::{Matcher, PrometheusBuilder};
use metrics_util::MetricKindMask;
use std::net::SocketAddr;
use std::sync::atomic::Ordering;
use std::sync::{Arc, OnceLock};
use std::time::{Duration, SystemTime};

struct Metric {
    name: &'static str,
    description: &'static str,
}

const COUNTERS: [Metric; 19] = [
    FUNCTION_INVOCATION_ERROR,
    FUNCTION_REQUEST,
    NATS_IN_BYTES,
    NATS_OUT_BYTES,
    NATS_IN_MESSAGES,
    NATS_OUT_MESSAGES,
    NATS_CONNECTS,
    TOTAL_NATS_ERRORS,
    NATS_EVENT_CONNECTED,
    NATS_EVENT_DISCONNECTED,
    NATS_EVENT_LAMEDUCK_MODE,
    NATS_EVENT_SLOW_CONSUMER,
    NATS_EVENT_SERVER_ERROR,
    NATS_EVENT_CLIENT_ERROR,
    NATS_SLOW_CONSUMER_DROPPED_MESSAGES,
    GRPC_CLIENT_RESPONSES,
    HTTP_CLIENT_RESPONSES,
    NATS_STREAM_PUBLISHES,
    AWS_REQUESTS_STATUS,
];
const HISTOGRAMS: [Metric; 1] = [FUNCTION_REQUEST_LATENCY];

const SECONDS_DURATION_BUCKETS: &[f64; 15] = &[
    0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 25.0, 50.0, 100.0, 1000.0,
];

// define time bucket for FUNCTION_REQUEST_LATENCY in the same way we're doing in the Java code
const FUNCTION_REQUEST_LATENCY_BUCKETS_IN_SECONDS: &[f64; 15] = &[
    0.1, 0.25, 0.5, 1.0, 1.5, 2.0, 3.0, 6.0, 20.0, 60.0, 120.0, 300.0, 600.0, 1200.0, 1800.0,
];

const FUNCTION_REQUEST_LATENCY: Metric = Metric {
    name: "function.request.latency",
    description: "Function request latency.",
};

const FUNCTION_INVOCATION_ERROR: Metric = Metric {
    name: "app.invocation.error",
    description: "Function invocation errors.",
};

const FUNCTION_REQUEST: Metric = Metric {
    name: "function.request",
    description: "Function requests, including those not finished.",
};

const NATS_IN_BYTES: Metric = Metric {
    name: "nats.in.bytes",
    description: "Inbound bytes from NATS.",
};

const NATS_OUT_BYTES: Metric = Metric {
    name: "nats.out.bytes",
    description: "Outbound bytes to NATS.",
};

const NATS_IN_MESSAGES: Metric = Metric {
    name: "nats.in.messages",
    description: "Inbound messages from NATS.",
};

const NATS_OUT_MESSAGES: Metric = Metric {
    name: "nats.out.messages",
    description: "Outbound messages to NATS.",
};

const NATS_STREAM_PUBLISHES: Metric = Metric {
    name: "nats.jetstream.publish",
    description: "Number of messages published to NATS streams.",
};

const NATS_CONNECTS: Metric = Metric {
    name: "nats.connects",
    description: "Number of NATS connections.",
};

const TOTAL_NATS_ERRORS: Metric = Metric {
    name: "total.nats.errors",
    description: "Total errors of NATS.",
};

const NATS_EVENT_CONNECTED: Metric = Metric {
    name: "nats.event.connected",
    description: "Number of NATS connected events.",
};

const NATS_EVENT_DISCONNECTED: Metric = Metric {
    name: "nats.event.disconnected",
    description: "Number of NATS disconnected events.",
};

const NATS_EVENT_LAMEDUCK_MODE: Metric = Metric {
    name: "nats.event.lameduck.mode",
    description: "Number of NATS lameduck mode events.",
};

const NATS_EVENT_SLOW_CONSUMER: Metric = Metric {
    name: "nats.event.slow.consumer",
    description: "Number of NATS slow consumer events.",
};

const NATS_EVENT_SERVER_ERROR: Metric = Metric {
    name: "nats.event.server.error",
    description: "Number of NATS server error events.",
};

const NATS_EVENT_CLIENT_ERROR: Metric = Metric {
    name: "nats.event.client.error",
    description: "Number of NATS client error events.",
};

const NATS_SLOW_CONSUMER_DROPPED_MESSAGES: Metric = Metric {
    name: "nats.slow.consumer.dropped.messages",
    description: "Number of dropped messages of NATS.",
};

const GRPC_CLIENT_RESPONSES: Metric = Metric {
    name: "grpc.client.responses",
    description: "status of responses from gRPC clients.",
};

const HTTP_CLIENT_RESPONSES: Metric = Metric {
    name: "http.client.responses",
    description: "status of responses from HTTP clients.",
};

const AWS_REQUESTS_STATUS: Metric = Metric {
    name: "aws.requests.status",
    description: "status of requests to aws.",
};

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

    // metrics will reset after 45 minutes of no use
    PrometheusBuilder::new()
        .idle_timeout(
            MetricKindMask::COUNTER | MetricKindMask::HISTOGRAM,
            Some(Duration::from_secs(45 * 60)),
        )
        .with_http_listener(metrics_endpoint)
        .set_buckets_for_metric(
            Matcher::Full(FUNCTION_REQUEST_LATENCY.name.to_string()),
            FUNCTION_REQUEST_LATENCY_BUCKETS_IN_SECONDS,
        )?
        .set_buckets_for_metric(
            Matcher::Full(AXUM_HTTP_REQUESTS_DURATION_SECONDS.to_string()),
            SECONDS_DURATION_BUCKETS,
        )?
        .install()?;

    for name in COUNTERS {
        register_counter(name);
    }

    for name in HISTOGRAMS {
        register_histogram(name);
    }
    let collector = metrics_process::Collector::default();
    collector.describe();
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(Duration::from_secs(1));
        loop {
            interval.tick().await;
            collector.collect();
        }
    });
    Ok(())
}

pub fn record_invocation_end(
    function_id: String,
    function_version_id: String,
    nca_id: String,
    start_time: SystemTime,
) {
    let labels = [
        ("function_id", function_id),
        ("function_version_id", function_version_id),
        ("nca_id", nca_id),
    ];
    if let Ok(latency) = start_time.elapsed() {
        histogram!(FUNCTION_REQUEST_LATENCY.name, &labels).record(latency);
    }
}

pub fn record_invocation_start(function_id: String, function_version_id: String, nca_id: String) {
    let labels = [
        ("function_id", function_id),
        ("function_version_id", function_version_id),
        ("nca_id", nca_id),
    ];
    counter!(FUNCTION_REQUEST.name, &labels).increment(1);
}

// errors that happened in the nvcf system, as opposed to inference errors
pub fn record_nvcf_application_error(status_code: String) {
    let labels = [("http_status_code", status_code)];
    counter!(FUNCTION_INVOCATION_ERROR.name, &labels).increment(1);
}

pub fn record_http_client_response(host: String, status_code: http::status::StatusCode) {
    let labels = [
        ("host", host),
        ("http_status_code", status_code.as_str().to_string()),
    ];
    counter!(HTTP_CLIENT_RESPONSES.name, &labels).increment(1);
}

pub fn record_grpc_client_response(host: String, status_code: tonic::Code) {
    // TODO move to layer. maybe https://github.com/blkmlk/tonic-prometheus-layer
    let code: i32 = status_code.into();
    let labels = [("host", host), ("grpc_status_code", code.to_string())];
    counter!(GRPC_CLIENT_RESPONSES.name, &labels).increment(1);
}

pub fn record_nats_jetstream_publish(stream_name: String, status: String) {
    let labels = [("stream", stream_name), ("status", status)];
    counter!(NATS_STREAM_PUBLISHES.name, &labels).increment(1);
}

pub fn record_aws_request_status(host: String, status_code: http::status::StatusCode) {
    let labels = [
        ("host", host),
        ("aws_status_code", status_code.as_str().to_string()),
    ];
    counter!(AWS_REQUESTS_STATUS.name, &labels).increment(1);
}

pub fn record_nats_error_total() {
    counter!(TOTAL_NATS_ERRORS.name).increment(1);
}

pub fn record_nats_event(event: &Event) {
    match event {
        Event::Connected => {
            counter!(NATS_EVENT_CONNECTED.name).increment(1);
        }
        Event::Disconnected => {
            counter!(NATS_EVENT_DISCONNECTED.name).increment(1);
        }
        Event::LameDuckMode => {
            counter!(NATS_EVENT_LAMEDUCK_MODE.name).increment(1);
        }
        Event::SlowConsumer(count) => {
            counter!(NATS_EVENT_SLOW_CONSUMER.name).increment(1);
            counter!(NATS_SLOW_CONSUMER_DROPPED_MESSAGES.name).increment(*count);
        }
        Event::ServerError(_err) => {
            counter!(NATS_EVENT_SERVER_ERROR.name).increment(1);
        }
        Event::ClientError(_err) => {
            counter!(NATS_EVENT_CLIENT_ERROR.name).increment(1);
        }
        _ => {
            // Ignore all other events
        }
    }
}

// it is only valid to call this function once. multiple nats connections would overwrite each other.
pub fn register_nats_statistics(statistics: Arc<Statistics>) {
    static ONE_NATS_CONNECTION: OnceLock<()> = OnceLock::new();
    ONE_NATS_CONNECTION.get_or_init(|| {
        tokio::spawn(async move {
            let mut interval = tokio::time::interval(Duration::from_secs(1));
            loop {
                interval.tick().await;
                counter!(NATS_IN_BYTES.name).absolute(statistics.in_bytes.load(Ordering::Relaxed));
                counter!(NATS_OUT_BYTES.name)
                    .absolute(statistics.out_bytes.load(Ordering::Relaxed));
                counter!(NATS_IN_MESSAGES.name)
                    .absolute(statistics.in_messages.load(Ordering::Relaxed));
                counter!(NATS_OUT_MESSAGES.name)
                    .absolute(statistics.out_messages.load(Ordering::Relaxed));
                counter!(NATS_CONNECTS.name).absolute(statistics.connects.load(Ordering::Relaxed));
            }
        });
    });
}

fn register_counter(metric: Metric) {
    metrics::describe_counter!(metric.name, metric.description);
}

fn register_histogram(metric: Metric) {
    metrics::describe_histogram!(metric.name, metric.description);
}

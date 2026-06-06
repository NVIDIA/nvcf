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

use std::hint::black_box;
use std::time::{Duration, Instant};

use anyhow::{Result, ensure};
use http::HeaderName;

#[derive(Debug, Clone)]
pub(crate) struct HeaderFilterMicrobenchConfig {
    pub iterations: usize,
    pub warmup_iterations: usize,
    pub header_count: usize,
}

pub(crate) fn run_header_filter_microbench(
    config: HeaderFilterMicrobenchConfig,
) -> Result<HeaderFilterMicrobenchOutcome> {
    config.validate()?;

    let mut rows = Vec::new();
    for scenario in HeaderFilterScenario::ALL {
        let headers = scenario.headers(config.header_count);
        warm_up(
            &headers,
            config.warmup_iterations,
            scenario.baseline_filter(),
        );
        warm_up(
            &headers,
            config.warmup_iterations,
            scenario.optimized_filter(),
        );
        let baseline = measure_filter(&headers, config.iterations, scenario.baseline_filter())?;
        let optimized = measure_filter(&headers, config.iterations, scenario.optimized_filter())?;
        rows.push(HeaderFilterMicrobenchRow {
            scenario,
            baseline,
            optimized,
        });
    }

    Ok(HeaderFilterMicrobenchOutcome { rows })
}

pub(crate) fn render_header_filter_microbench_report(
    outcome: &HeaderFilterMicrobenchOutcome,
) -> String {
    let mut report = String::new();
    report.push_str("# Header Filter Microbench\n\n");
    report.push_str(
        "| Scenario | Headers | Accepted | Baseline ns/header | Optimized ns/header | Improvement |\n",
    );
    report.push_str("| --- | ---: | ---: | ---: | ---: | ---: |\n");
    for row in &outcome.rows {
        report.push_str(&format!(
            "| {} | {} | {} | {:.2} | {:.2} | {:.2}% |\n",
            row.scenario.label(),
            row.baseline.header_count,
            row.optimized.accepted,
            row.baseline.ns_per_header,
            row.optimized.ns_per_header,
            row.improvement_percent()
        ));
    }
    report
}

#[derive(Debug)]
pub(crate) struct HeaderFilterMicrobenchOutcome {
    rows: Vec<HeaderFilterMicrobenchRow>,
}

#[derive(Debug)]
struct HeaderFilterMicrobenchRow {
    scenario: HeaderFilterScenario,
    baseline: HeaderFilterMeasurement,
    optimized: HeaderFilterMeasurement,
}

impl HeaderFilterMicrobenchRow {
    fn improvement_percent(&self) -> f64 {
        if self.baseline.ns_per_header == 0.0 {
            return 0.0;
        }
        ((self.baseline.ns_per_header - self.optimized.ns_per_header) / self.baseline.ns_per_header)
            * 100.0
    }
}

#[derive(Debug)]
struct HeaderFilterMeasurement {
    header_count: usize,
    accepted: usize,
    ns_per_header: f64,
}

type HeaderFilter = fn(&HeaderName) -> bool;

impl HeaderFilterMicrobenchConfig {
    fn validate(&self) -> Result<()> {
        ensure!(self.iterations > 0, "iterations must be > 0");
        ensure!(self.header_count > 0, "header_count must be > 0");
        self.iterations
            .checked_mul(self.header_count)
            .ok_or_else(|| anyhow::anyhow!("iterations * header_count is too large"))?;
        Ok(())
    }
}

#[derive(Clone, Copy, Debug)]
enum HeaderFilterScenario {
    StargateProxy,
    StargateHttp3Tunnel,
    PylonUpstreamRequest,
    PylonTunnelResponse,
}

impl HeaderFilterScenario {
    const ALL: [Self; 4] = [
        Self::StargateProxy,
        Self::StargateHttp3Tunnel,
        Self::PylonUpstreamRequest,
        Self::PylonTunnelResponse,
    ];

    fn label(self) -> &'static str {
        match self {
            Self::StargateProxy => "stargate-proxy",
            Self::StargateHttp3Tunnel => "stargate-http3-tunnel",
            Self::PylonUpstreamRequest => "pylon-upstream-request",
            Self::PylonTunnelResponse => "pylon-tunnel-response",
        }
    }

    fn headers(self, header_count: usize) -> Vec<HeaderName> {
        let seed = match self {
            Self::StargateProxy => STARGATE_PROXY_HEADERS,
            Self::StargateHttp3Tunnel => HOP_BY_HOP_MIXED_HEADERS,
            Self::PylonUpstreamRequest => PYLON_REQUEST_HEADERS,
            Self::PylonTunnelResponse => PYLON_RESPONSE_HEADERS,
        };
        (0..header_count)
            .map(|index| HeaderName::from_static(seed[index % seed.len()]))
            .collect()
    }

    fn baseline_filter(self) -> HeaderFilter {
        match self {
            Self::StargateProxy => stargate_proxy_baseline,
            Self::StargateHttp3Tunnel => stargate_h3_tunnel_baseline,
            Self::PylonUpstreamRequest => pylon_request_baseline,
            Self::PylonTunnelResponse => pylon_response_baseline,
        }
    }

    fn optimized_filter(self) -> HeaderFilter {
        match self {
            Self::StargateProxy => stargate_proxy_optimized,
            Self::StargateHttp3Tunnel => stargate_h3_tunnel_optimized,
            Self::PylonUpstreamRequest => pylon_request_optimized,
            Self::PylonTunnelResponse => pylon_response_optimized,
        }
    }
}

const HOP_BY_HOP_MIXED_HEADERS: &[&str] = &[
    "authorization",
    "content-type",
    "x-request-id",
    "connection",
    "x-model",
    "x-input-tokens",
    "proxy-connection",
    "x-cache-affinity-key",
    "user-agent",
    "keep-alive",
    "traceparent",
    "transfer-encoding",
    "tracestate",
    "te",
    "x-forwarded-for",
    "trailer",
    "accept",
    "upgrade",
    "content-length",
    "host",
    "x-custom-a",
    "x-custom-b",
];

const STARGATE_PROXY_HEADERS: &[&str] = &[
    "authorization",
    "content-type",
    "x-request-id",
    "connection",
    "x-model",
    "x-routing-method",
    "x-input-tokens",
    "proxy-connection",
    "x-stargate-retryable",
    "x-cache-affinity-key",
    "x-stargate-retry-reason",
    "user-agent",
    "x-stargate-retry-after-ms",
    "keep-alive",
    "traceparent",
    "x-stargate-error-code",
    "transfer-encoding",
    "tracestate",
    "te",
    "x-forwarded-for",
    "trailer",
    "accept",
    "upgrade",
    "content-length",
    "host",
    "x-custom-a",
    "x-custom-b",
];

const PYLON_REQUEST_HEADERS: &[&str] = &[
    "authorization",
    "content-type",
    "x-request-id",
    "connection",
    "x-model",
    "x-method",
    "x-input-tokens",
    "proxy-connection",
    "x-path",
    "x-cache-affinity-key",
    "user-agent",
    "keep-alive",
    "traceparent",
    "transfer-encoding",
    "tracestate",
    "te",
    "x-forwarded-for",
    "trailer",
    "accept",
    "upgrade",
    "content-length",
    "host",
    "x-custom-a",
    "x-custom-b",
];

const PYLON_RESPONSE_HEADERS: &[&str] = &[
    "content-type",
    "x-status",
    "x-kv-cache-hit",
    "connection",
    "proxy-connection",
    "x-stargate-upstream-retryable",
    "x-stargate-retryable",
    "x-stargate-retry-reason",
    "keep-alive",
    "x-stargate-retry-after-ms",
    "transfer-encoding",
    "te",
    "x-custom-response",
    "trailer",
    "accept",
    "upgrade",
    "content-length",
];

fn warm_up(headers: &[HeaderName], iterations: usize, filter: HeaderFilter) {
    let accepted = scan_headers(headers, iterations, filter);
    black_box(accepted);
}

fn measure_filter(
    headers: &[HeaderName],
    iterations: usize,
    filter: HeaderFilter,
) -> Result<HeaderFilterMeasurement> {
    let total_headers = iterations
        .checked_mul(headers.len())
        .ok_or_else(|| anyhow::anyhow!("iterations * header count overflowed"))?;
    let start = Instant::now();
    let accepted = scan_headers(headers, iterations, filter);
    let elapsed = start.elapsed();
    Ok(HeaderFilterMeasurement {
        header_count: headers.len(),
        accepted,
        ns_per_header: ns_per_header(elapsed, total_headers),
    })
}

fn scan_headers(headers: &[HeaderName], iterations: usize, filter: HeaderFilter) -> usize {
    let mut accepted = 0usize;
    for _ in 0..iterations {
        for name in headers {
            if filter(black_box(name)) {
                accepted += 1;
            }
        }
    }
    accepted
}

fn ns_per_header(elapsed: Duration, total_headers: usize) -> f64 {
    elapsed.as_nanos() as f64 / total_headers as f64
}

fn stargate_proxy_baseline(name: &HeaderName) -> bool {
    let key = name.as_str().to_ascii_lowercase();
    !matches!(
        key.as_str(),
        "connection"
            | "proxy-connection"
            | "keep-alive"
            | "transfer-encoding"
            | "te"
            | "trailer"
            | "upgrade"
            | "host"
            | "x-routing-method"
            | "x-stargate-retryable"
            | "x-stargate-retry-reason"
            | "x-stargate-retry-after-ms"
            | "x-stargate-error-code"
    )
}

fn stargate_proxy_optimized(name: &HeaderName) -> bool {
    !matches!(
        name.as_str(),
        "connection"
            | "proxy-connection"
            | "keep-alive"
            | "transfer-encoding"
            | "te"
            | "trailer"
            | "upgrade"
            | "host"
            | "x-routing-method"
            | "x-stargate-retryable"
            | "x-stargate-retry-reason"
            | "x-stargate-retry-after-ms"
            | "x-stargate-error-code"
    )
}

fn stargate_h3_tunnel_baseline(name: &HeaderName) -> bool {
    let key = name.as_str().to_ascii_lowercase();
    !matches!(
        key.as_str(),
        "connection"
            | "proxy-connection"
            | "keep-alive"
            | "transfer-encoding"
            | "te"
            | "trailer"
            | "upgrade"
            | "host"
    )
}

fn stargate_h3_tunnel_optimized(name: &HeaderName) -> bool {
    !matches!(
        name.as_str(),
        "connection"
            | "proxy-connection"
            | "keep-alive"
            | "transfer-encoding"
            | "te"
            | "trailer"
            | "upgrade"
            | "host"
    )
}

fn pylon_request_baseline(name: &HeaderName) -> bool {
    let key = name.as_str().to_ascii_lowercase();
    !matches!(
        key.as_str(),
        "connection"
            | "proxy-connection"
            | "keep-alive"
            | "transfer-encoding"
            | "te"
            | "trailer"
            | "upgrade"
            | "host"
            | "x-method"
            | "x-path"
    )
}

fn pylon_request_optimized(name: &HeaderName) -> bool {
    !matches!(
        name.as_str(),
        "connection"
            | "proxy-connection"
            | "keep-alive"
            | "transfer-encoding"
            | "te"
            | "trailer"
            | "upgrade"
            | "host"
            | "x-method"
            | "x-path"
    )
}

fn pylon_response_baseline(name: &HeaderName) -> bool {
    let key = name.as_str().to_ascii_lowercase();
    !matches!(
        key.as_str(),
        "connection"
            | "proxy-connection"
            | "keep-alive"
            | "transfer-encoding"
            | "te"
            | "trailer"
            | "upgrade"
            | "content-length"
            | "x-stargate-upstream-retryable"
            | "x-stargate-retryable"
            | "x-stargate-retry-reason"
            | "x-stargate-retry-after-ms"
    )
}

fn pylon_response_optimized(name: &HeaderName) -> bool {
    let name = name.as_str();
    !matches!(
        name,
        "connection"
            | "proxy-connection"
            | "keep-alive"
            | "transfer-encoding"
            | "te"
            | "trailer"
            | "upgrade"
            | "content-length"
            | "x-stargate-upstream-retryable"
            | "x-stargate-retryable"
            | "x-stargate-retry-reason"
            | "x-stargate-retry-after-ms"
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn optimized_filters_match_baseline_filters() {
        for scenario in HeaderFilterScenario::ALL {
            for name in scenario.headers(128) {
                assert_eq!(
                    (scenario.baseline_filter())(&name),
                    (scenario.optimized_filter())(&name),
                    "scenario={} header={}",
                    scenario.label(),
                    name
                );
            }
        }
    }

    #[test]
    fn report_includes_every_header_filter_scenario() -> Result<()> {
        let outcome = run_header_filter_microbench(HeaderFilterMicrobenchConfig {
            iterations: 1,
            warmup_iterations: 0,
            header_count: 16,
        })?;

        let report = render_header_filter_microbench_report(&outcome);

        for scenario in HeaderFilterScenario::ALL {
            assert!(report.contains(scenario.label()));
        }
        Ok(())
    }

    #[test]
    fn header_filter_microbench_rejects_zero_work() {
        let Err(error) = run_header_filter_microbench(HeaderFilterMicrobenchConfig {
            iterations: 0,
            warmup_iterations: 0,
            header_count: 16,
        }) else {
            panic!("zero iterations should fail");
        };

        assert!(error.to_string().contains("iterations must be > 0"));
    }
}

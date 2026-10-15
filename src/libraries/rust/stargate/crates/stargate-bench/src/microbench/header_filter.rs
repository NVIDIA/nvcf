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
use std::time::Instant;

use anyhow::{Result, ensure};
use clap::Args;
use http::HeaderName;

#[derive(Args, Debug, Clone)]
pub(crate) struct HeaderFilterMicrobenchConfig {
    #[arg(long, default_value_t = 1_000_000, value_name = "N")]
    pub iterations: usize,
    #[arg(long, default_value_t = 100_000, value_name = "N")]
    pub warmup_iterations: usize,
    #[arg(long, default_value_t = 128, value_name = "N")]
    pub header_count: usize,
}

pub(crate) fn run_header_filter_microbench(
    config: HeaderFilterMicrobenchConfig,
) -> Result<HeaderFilterMicrobenchOutcome> {
    config.validate()?;

    let mut rows = Vec::with_capacity(HEADER_FILTER_SCENARIOS.len());
    for scenario in HEADER_FILTER_SCENARIOS {
        let headers = scenario.headers(config.header_count);
        black_box(scan_headers(
            &headers,
            config.warmup_iterations,
            scenario.baseline,
        ));
        black_box(scan_headers(
            &headers,
            config.warmup_iterations,
            scenario.optimized,
        ));
        let baseline = measure_filter(&headers, config.iterations, scenario.baseline);
        let optimized = measure_filter(&headers, config.iterations, scenario.optimized);
        rows.push(HeaderFilterMicrobenchRow {
            scenario: scenario.label,
            header_count: headers.len(),
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
            row.scenario,
            row.header_count,
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
    scenario: &'static str,
    header_count: usize,
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
    accepted: usize,
    ns_per_header: f64,
}

type HeaderFilter = fn(&HeaderName) -> bool;

impl HeaderFilterMicrobenchConfig {
    fn validate(&self) -> Result<()> {
        ensure!(self.iterations > 0, "iterations must be > 0");
        ensure!(self.header_count > 0, "header_count must be > 0");
        ensure!(
            self.iterations.checked_mul(self.header_count).is_some(),
            "iterations * header_count is too large"
        );
        Ok(())
    }
}

#[derive(Clone, Copy)]
struct HeaderFilterScenario {
    label: &'static str,
    seed: &'static [&'static str],
    baseline: HeaderFilter,
    optimized: HeaderFilter,
}

impl HeaderFilterScenario {
    fn headers(self, header_count: usize) -> Vec<HeaderName> {
        (0..header_count)
            .map(|index| HeaderName::from_static(self.seed[index % self.seed.len()]))
            .collect()
    }
}

#[rustfmt::skip]
const HOP_BY_HOP_MIXED_HEADERS: &[&str] = &[
    "authorization", "content-type", "x-request-id", "connection", "x-model",
    "x-input-tokens", "proxy-connection", "x-cache-affinity-key", "user-agent", "keep-alive",
    "traceparent", "transfer-encoding", "tracestate", "te", "x-forwarded-for",
    "trailer", "accept", "upgrade", "content-length", "host", "x-custom-a", "x-custom-b",
];

#[rustfmt::skip]
const STARGATE_PROXY_HEADERS: &[&str] = &[
    "authorization", "content-type", "x-request-id", "connection", "x-model",
    "x-routing-method", "x-input-tokens", "proxy-connection", "x-stargate-retryable",
    "x-cache-affinity-key", "x-stargate-expected-queue-ms", "x-stargate-retry-reason",
    "user-agent", "x-stargate-retry-after-ms", "keep-alive", "traceparent",
    "x-stargate-error-code", "transfer-encoding", "tracestate", "te",
    "x-forwarded-for", "trailer", "accept", "upgrade", "content-length", "host",
    "x-custom-a", "x-custom-b",
];

#[rustfmt::skip]
const PYLON_REQUEST_HEADERS: &[&str] = &[
    "authorization", "content-type", "x-request-id", "connection", "x-model", "x-method",
    "x-input-tokens", "proxy-connection", "x-path", "x-cache-affinity-key",
    "x-stargate-expected-queue-ms", "user-agent", "keep-alive", "traceparent",
    "transfer-encoding", "tracestate", "te", "x-forwarded-for", "trailer", "accept",
    "upgrade", "content-length", "host", "x-custom-a", "x-custom-b",
];

#[rustfmt::skip]
const PYLON_RESPONSE_HEADERS: &[&str] = &[
    "content-type", "x-status", "x-kv-cache-hit", "connection", "proxy-connection",
    "x-stargate-upstream-retryable", "x-stargate-retryable", "x-stargate-retry-reason",
    "keep-alive", "x-stargate-retry-after-ms", "transfer-encoding", "te",
    "x-custom-response", "trailer", "accept", "upgrade", "content-length",
];

fn measure_filter(
    headers: &[HeaderName],
    iterations: usize,
    filter: HeaderFilter,
) -> HeaderFilterMeasurement {
    let start = Instant::now();
    let accepted = scan_headers(headers, iterations, filter);
    let elapsed = start.elapsed();
    HeaderFilterMeasurement {
        accepted,
        ns_per_header: elapsed.as_nanos() as f64 / (iterations * headers.len()) as f64,
    }
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

macro_rules! define_filter_pair {
    ($baseline:ident, $optimized:ident; $($blocked:literal)|+) => {
        fn $baseline(name: &HeaderName) -> bool {
            let key = name.as_str().to_ascii_lowercase();
            !matches!(key.as_str(), $($blocked)|+)
        }

        fn $optimized(name: &HeaderName) -> bool {
            !matches!(name.as_str(), $($blocked)|+)
        }
    };
}

define_filter_pair!(
    stargate_proxy_baseline,
    stargate_proxy_optimized;
    "connection" | "proxy-connection" | "keep-alive" | "transfer-encoding" | "te" | "trailer"
        | "upgrade" | "host" | "x-routing-method" | "x-stargate-retryable"
        | "x-stargate-retry-reason" | "x-stargate-retry-after-ms" | "x-stargate-error-code"
        | "x-stargate-expected-queue-ms"
);

define_filter_pair!(
    stargate_h3_tunnel_baseline,
    stargate_h3_tunnel_optimized;
    "connection" | "proxy-connection" | "keep-alive" | "transfer-encoding" | "te" | "trailer"
        | "upgrade" | "host"
);
define_filter_pair!(
    pylon_request_baseline,
    pylon_request_optimized;
    "connection" | "proxy-connection" | "keep-alive" | "transfer-encoding" | "te" | "trailer"
        | "upgrade" | "host" | "x-method" | "x-path" | "x-stargate-expected-queue-ms"
);

define_filter_pair!(
    pylon_response_baseline,
    pylon_response_optimized;
    "connection" | "proxy-connection" | "keep-alive" | "transfer-encoding" | "te" | "trailer"
        | "upgrade" | "content-length" | "x-stargate-upstream-retryable" | "x-stargate-retryable"
        | "x-stargate-retry-reason" | "x-stargate-retry-after-ms"
);

#[rustfmt::skip]
const HEADER_FILTER_SCENARIOS: [HeaderFilterScenario; 4] = [
    HeaderFilterScenario { label: "stargate-proxy", seed: STARGATE_PROXY_HEADERS, baseline: stargate_proxy_baseline, optimized: stargate_proxy_optimized },
    HeaderFilterScenario { label: "stargate-http3-tunnel", seed: HOP_BY_HOP_MIXED_HEADERS, baseline: stargate_h3_tunnel_baseline, optimized: stargate_h3_tunnel_optimized },
    HeaderFilterScenario { label: "pylon-upstream-request", seed: PYLON_REQUEST_HEADERS, baseline: pylon_request_baseline, optimized: pylon_request_optimized },
    HeaderFilterScenario { label: "pylon-tunnel-response", seed: PYLON_RESPONSE_HEADERS, baseline: pylon_response_baseline, optimized: pylon_response_optimized },
];

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn optimized_filters_match_baseline_filters() {
        for scenario in HEADER_FILTER_SCENARIOS {
            for name in scenario.headers(128) {
                assert_eq!(
                    (scenario.baseline)(&name),
                    (scenario.optimized)(&name),
                    "scenario={} header={}",
                    scenario.label,
                    name
                );
            }
        }

        let expected_queue_ms = HeaderName::from_static("x-stargate-expected-queue-ms");
        for scenario in [HEADER_FILTER_SCENARIOS[0], HEADER_FILTER_SCENARIOS[2]] {
            assert!(!(scenario.baseline)(&expected_queue_ms));
            assert!(!(scenario.optimized)(&expected_queue_ms));
            assert!(scenario.seed.contains(&expected_queue_ms.as_str()));
            let headers = scenario.headers(scenario.seed.len());
            assert_eq!(scan_headers(&headers, 1, scenario.baseline), 14);
            assert_eq!(scan_headers(&headers, 1, scenario.optimized), 14);
        }
    }

    #[test]
    fn report_includes_every_header_filter_scenario() {
        let outcome = run_header_filter_microbench(HeaderFilterMicrobenchConfig {
            iterations: 1,
            warmup_iterations: 0,
            header_count: 16,
        })
        .expect("valid microbenchmark config should run");
        assert_eq!(outcome.rows.len(), HEADER_FILTER_SCENARIOS.len());
        for row in &outcome.rows {
            assert_eq!(row.header_count, 16);
            assert_eq!(row.baseline.accepted, row.optimized.accepted);
        }

        let report = render_header_filter_microbench_report(&outcome);

        for scenario in HEADER_FILTER_SCENARIOS {
            assert!(report.contains(scenario.label));
        }
    }

    #[test]
    fn header_filter_microbench_rejects_zero_work() {
        let error = run_header_filter_microbench(HeaderFilterMicrobenchConfig {
            iterations: 0,
            warmup_iterations: 0,
            header_count: 16,
        })
        .expect_err("zero iterations should fail");

        assert!(error.to_string().contains("iterations must be > 0"));
    }
}

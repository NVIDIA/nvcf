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

use super::TransportBenchmarkOutcome;

pub fn render_transport_benchmark_report(outcome: &TransportBenchmarkOutcome) -> String {
    let mut out = String::new();
    out.push_str("# Transport Benchmark\n\n");
    out.push_str(&format!(
        "- Requests: `{}`\n- Concurrency: `{}`\n- QUIC connections: `{}`\n- Warmup requests: `{}`\n- Trials: `{}`\n- Warmup trials: `{}`\n- Cooldown: `{} ms`\n- Randomized order: `{}`\n- Noise threshold CV: `{:.4}`\n- Min effect size: `{:.2}%`\n- Request bytes: `{}`\n- Response bytes: `{}`\n- QUIC send fairness: `{}`\n- HTTP/3 grease: `{}`\n\n",
        outcome.config.request_count,
        outcome.config.concurrency,
        outcome.config.quic_connections,
        outcome.config.warmup_requests,
        outcome.config.trials,
        outcome.config.warmup_trials,
        outcome.config.cooldown_ms,
        outcome.config.randomize_order,
        outcome.config.noise_threshold_cv,
        outcome.config.min_effect_size_percent,
        outcome.config.request_body_bytes,
        outcome.config.response_body_bytes,
        outcome.config.quic_send_fairness,
        outcome.config.http3_send_grease,
    ));

    if !outcome.aggregates.is_empty() {
        out.push_str("## Aggregate\n\n");
        out.push_str("| Transport | Trials | Classification | Throughput Mean | Throughput 95% CI | Throughput CV | P95 Latency Median | Headers P95 Median | First Body P95 Median |\n");
        out.push_str("|---|---:|---|---:|---:|---:|---:|---:|---:|\n");
        for aggregate in &outcome.aggregates {
            out.push_str(&format!(
                "| {} | {} | {:?} | {} | {} | {} | {} | {} | {} |\n",
                aggregate.transport.label(),
                aggregate.trial_count,
                aggregate.classification,
                optional_float(aggregate.throughput_rps.mean, " req/s"),
                optional_ci(&aggregate.throughput_rps.mean_ci_95, " req/s"),
                optional_cv(aggregate.throughput_rps.coefficient_of_variation),
                optional_float(aggregate.latency_p95_us.median, " us"),
                optional_float(aggregate.response_headers_p95_us.median, " us"),
                optional_float(aggregate.first_body_p95_us.median, " us"),
            ));
        }
        out.push('\n');
    }

    if !outcome.comparisons.is_empty() {
        out.push_str("## Comparisons\n\n");
        out.push_str(
            "| Baseline | Candidate | Throughput Delta | CI Overlap | Meaningful Difference |\n",
        );
        out.push_str("|---|---|---:|---|---|\n");
        for comparison in &outcome.comparisons {
            out.push_str(&format!(
                "| {} | {} | {} | {} | {} |\n",
                comparison.baseline.label(),
                comparison.candidate.label(),
                optional_percent(comparison.throughput_delta_percent),
                optional_bool(comparison.confidence_intervals_overlap),
                comparison.meaningful_difference,
            ));
        }
        out.push('\n');
    }

    out.push_str("## Measured Trials\n\n");
    out.push_str("| Trial | Transport | Success | Throughput | Goodput | P50 | P95 | P99 | Max | Headers P50 | Headers P95 | Headers P99 | First Body P50 | First Body P95 | First Body P99 |\n");
    out.push_str("|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n");
    for run in &outcome.runs {
        let summary = &run.summary;
        out.push_str(&format!(
            "| {} | {} | {}/{} | {:.1} req/s | {:.2} MiB/s | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} |\n",
            run.trial_index,
            summary.transport.label(),
            summary.success_count,
            summary.request_count,
            summary.throughput_rps,
            summary.goodput_mib_s,
            optional_us(summary.latency_us.p50),
            optional_us(summary.latency_us.p95),
            optional_us(summary.latency_us.p99),
            optional_us(summary.latency_us.max),
            optional_us(summary.response_headers_us.p50),
            optional_us(summary.response_headers_us.p95),
            optional_us(summary.response_headers_us.p99),
            optional_us(summary.first_body_us.p50),
            optional_us(summary.first_body_us.p95),
            optional_us(summary.first_body_us.p99),
        ));
    }
    out
}

fn optional_us(value: Option<u64>) -> String {
    value
        .map(|value| format!("{value} us"))
        .unwrap_or_else(|| "-".to_string())
}

fn optional_float(value: Option<f64>, unit: &str) -> String {
    value
        .map(|value| format!("{value:.2}{unit}"))
        .unwrap_or_else(|| "-".to_string())
}

fn optional_ci(value: &Option<crate::statistics::ConfidenceInterval>, unit: &str) -> String {
    value
        .as_ref()
        .map(|interval| format!("[{:.2}, {:.2}]{unit}", interval.lower, interval.upper))
        .unwrap_or_else(|| "-".to_string())
}

fn optional_cv(value: Option<f64>) -> String {
    value
        .map(|value| format!("{value:.4}"))
        .unwrap_or_else(|| "-".to_string())
}

fn optional_percent(value: Option<f64>) -> String {
    value
        .map(|value| format!("{value:.2}%"))
        .unwrap_or_else(|| "-".to_string())
}

fn optional_bool(value: Option<bool>) -> String {
    value
        .map(|value| value.to_string())
        .unwrap_or_else(|| "-".to_string())
}

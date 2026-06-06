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

use std::time::Duration;

use crate::statistics::{
    NoiseClassification, classify_noise, summarize_distribution, upper_nearest_rank_index,
};

use super::{
    LatencySummary, RequestSample, TransportAggregateSummary, TransportComparisonSummary,
    TransportKind, TransportRunOutcome, TransportRunSummary,
};

pub(super) fn summarize_aggregates(
    runs: &[TransportRunOutcome],
    noise_threshold_cv: f64,
) -> Vec<TransportAggregateSummary> {
    [
        TransportKind::CustomProtocol,
        TransportKind::Http3H3Quinn,
        TransportKind::WebTransportH3Quinn,
    ]
    .into_iter()
    .filter_map(|transport| {
        let transport_runs = runs
            .iter()
            .filter(|run| run.transport == transport)
            .collect::<Vec<_>>();
        if transport_runs.is_empty() {
            return None;
        }
        let seed = match transport {
            TransportKind::CustomProtocol => 11,
            TransportKind::Http3H3Quinn => 17,
            TransportKind::WebTransportH3Quinn => 23,
        };
        let throughput_rps = summarize_distribution(
            &transport_runs
                .iter()
                .map(|run| run.summary.throughput_rps)
                .collect::<Vec<_>>(),
            seed,
        );
        let classification = classify_noise(&throughput_rps, noise_threshold_cv);
        Some(TransportAggregateSummary {
            transport,
            trial_count: transport_runs.len(),
            classification,
            throughput_rps,
            goodput_mib_s: summarize_distribution(
                &transport_runs
                    .iter()
                    .map(|run| run.summary.goodput_mib_s)
                    .collect::<Vec<_>>(),
                seed + 1,
            ),
            latency_p95_us: summarize_distribution(
                &transport_runs
                    .iter()
                    .filter_map(|run| run.summary.latency_us.p95.map(|value| value as f64))
                    .collect::<Vec<_>>(),
                seed + 2,
            ),
            response_headers_p95_us: summarize_distribution(
                &transport_runs
                    .iter()
                    .filter_map(|run| {
                        run.summary
                            .response_headers_us
                            .p95
                            .map(|value| value as f64)
                    })
                    .collect::<Vec<_>>(),
                seed + 3,
            ),
            first_body_p95_us: summarize_distribution(
                &transport_runs
                    .iter()
                    .filter_map(|run| run.summary.first_body_us.p95.map(|value| value as f64))
                    .collect::<Vec<_>>(),
                seed + 4,
            ),
        })
    })
    .collect()
}

pub(super) fn summarize_comparisons(
    aggregates: &[TransportAggregateSummary],
    min_effect_size_percent: f64,
) -> Vec<TransportComparisonSummary> {
    let baseline = aggregates
        .iter()
        .find(|aggregate| aggregate.transport == TransportKind::CustomProtocol);
    let Some(baseline) = baseline else {
        return Vec::new();
    };

    aggregates
        .iter()
        .filter(|candidate| candidate.transport != TransportKind::CustomProtocol)
        .map(|candidate| {
            let throughput_delta_percent =
                match (baseline.throughput_rps.mean, candidate.throughput_rps.mean) {
                    (Some(baseline), Some(candidate)) if baseline.abs() > f64::EPSILON => {
                        Some((candidate - baseline) / baseline * 100.0)
                    }
                    _ => None,
                };
            let confidence_intervals_overlap = match (
                &baseline.throughput_rps.mean_ci_95,
                &candidate.throughput_rps.mean_ci_95,
            ) {
                (Some(left), Some(right)) => {
                    Some(left.lower <= right.upper && right.lower <= left.upper)
                }
                _ => None,
            };
            let classifications_support_comparison = baseline.classification
                == NoiseClassification::Reliable
                && candidate.classification == NoiseClassification::Reliable;
            let meaningful_difference = throughput_delta_percent.is_some_and(|delta| {
                classifications_support_comparison
                    && baseline.trial_count >= 2
                    && candidate.trial_count >= 2
                    && confidence_intervals_overlap == Some(false)
                    && delta.abs() >= min_effect_size_percent
            });

            TransportComparisonSummary {
                baseline: TransportKind::CustomProtocol,
                candidate: candidate.transport,
                throughput_delta_percent,
                min_effect_size_percent,
                confidence_intervals_overlap,
                meaningful_difference,
            }
        })
        .collect()
}

pub(super) fn summarize_samples(
    transport: TransportKind,
    samples: &[RequestSample],
    measured_duration: Duration,
) -> TransportRunSummary {
    let success_count = samples.iter().filter(|sample| sample.ok).count();
    let failure_count = samples.len() - success_count;
    let measured_duration_secs = measured_duration.as_secs_f64();
    let throughput_rps = if measured_duration_secs > 0.0 {
        success_count as f64 / measured_duration_secs
    } else {
        0.0
    };
    let transferred_bytes = samples
        .iter()
        .filter(|sample| sample.ok)
        .map(|sample| sample.request_bytes + sample.response_bytes)
        .sum::<usize>();
    let goodput_mib_s = if measured_duration_secs > 0.0 {
        transferred_bytes as f64 / measured_duration_secs / 1024.0 / 1024.0
    } else {
        0.0
    };

    TransportRunSummary {
        transport,
        request_count: samples.len(),
        success_count,
        failure_count,
        measured_duration_ms: duration_ms(measured_duration),
        throughput_rps,
        goodput_mib_s,
        latency_us: summarize_values(
            samples
                .iter()
                .filter(|sample| sample.ok)
                .map(|sample| sample.completion_us),
        ),
        response_headers_us: summarize_values(
            samples
                .iter()
                .filter(|sample| sample.ok)
                .filter_map(|sample| sample.response_headers_us),
        ),
        first_body_us: summarize_values(
            samples
                .iter()
                .filter(|sample| sample.ok)
                .filter_map(|sample| sample.first_body_us),
        ),
    }
}

fn summarize_values(values: impl Iterator<Item = u64>) -> LatencySummary {
    let mut values = values.collect::<Vec<_>>();
    if values.is_empty() {
        return LatencySummary::default();
    }
    values.sort_unstable();
    LatencySummary {
        min: values.first().copied(),
        p50: percentile(&values, 0.50),
        p90: percentile(&values, 0.90),
        p95: percentile(&values, 0.95),
        p99: percentile(&values, 0.99),
        max: values.last().copied(),
    }
}

fn percentile(sorted_values: &[u64], percentile: f64) -> Option<u64> {
    if sorted_values.is_empty() {
        return None;
    }
    let index = upper_nearest_rank_index(sorted_values.len(), percentile)?;
    sorted_values.get(index).copied()
}

fn duration_ms(duration: Duration) -> u64 {
    duration.as_millis().try_into().unwrap_or(u64::MAX)
}

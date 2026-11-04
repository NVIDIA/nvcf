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
    DistributionStats, NoiseClassification, classify_noise, summarize_distribution,
    upper_nearest_rank_index,
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
        (TransportKind::RawQuic, 11),
        (TransportKind::Http3H3Quinn, 17),
        (TransportKind::WebTransportH3Quinn, 23),
    ]
    .into_iter()
    .filter_map(|(transport, seed)| {
        let transport_runs = runs
            .iter()
            .filter(|run| run.transport == transport)
            .collect::<Vec<_>>();
        if transport_runs.is_empty() {
            return None;
        }
        let throughput_rps = summarize_runs(&transport_runs, seed, |run| {
            Some(run.summary.throughput_rps)
        });
        let classification = classify_noise(&throughput_rps, noise_threshold_cv);
        Some(TransportAggregateSummary {
            transport,
            trial_count: transport_runs.len(),
            classification,
            throughput_rps,
            goodput_mib_s: summarize_runs(&transport_runs, seed + 1, |run| {
                Some(run.summary.goodput_mib_s)
            }),
            latency_p95_us: summarize_runs(&transport_runs, seed + 2, |run| {
                run.summary.latency_us.p95.map(|value| value as f64)
            }),
            response_headers_p95_us: summarize_runs(&transport_runs, seed + 3, |run| {
                run.summary
                    .response_headers_us
                    .p95
                    .map(|value| value as f64)
            }),
            first_body_p95_us: summarize_runs(&transport_runs, seed + 4, |run| {
                run.summary.first_body_us.p95.map(|value| value as f64)
            }),
        })
    })
    .collect()
}

fn summarize_runs(
    runs: &[&TransportRunOutcome],
    seed: u64,
    value: impl Fn(&TransportRunOutcome) -> Option<f64>,
) -> DistributionStats {
    summarize_distribution(
        &runs.iter().filter_map(|run| value(run)).collect::<Vec<_>>(),
        seed,
    )
}

pub(super) fn summarize_comparisons(
    aggregates: &[TransportAggregateSummary],
    min_effect_size_percent: f64,
) -> Vec<TransportComparisonSummary> {
    let Some(baseline) = aggregates
        .iter()
        .find(|aggregate| aggregate.transport == TransportKind::RawQuic)
    else {
        return Vec::new();
    };

    aggregates
        .iter()
        .filter(|candidate| candidate.transport != TransportKind::RawQuic)
        .map(|candidate| {
            let throughput_delta_percent = baseline
                .throughput_rps
                .mean
                .zip(candidate.throughput_rps.mean)
                .filter(|(baseline, _)| baseline.abs() > f64::EPSILON)
                .map(|(baseline, candidate)| (candidate - baseline) / baseline * 100.0);
            let confidence_intervals_overlap = baseline
                .throughput_rps
                .mean_ci_95
                .as_ref()
                .zip(candidate.throughput_rps.mean_ci_95.as_ref())
                .map(|(left, right)| left.lower <= right.upper && right.lower <= left.upper);
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
                baseline: TransportKind::RawQuic,
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
    let successful = || samples.iter().filter(|sample| sample.ok);
    let success_count = samples.iter().filter(|sample| sample.ok).count();
    let failure_count = samples.len() - success_count;
    let measured_duration_secs = measured_duration.as_secs_f64();
    let per_second = |count: usize| {
        if measured_duration.is_zero() {
            0.0
        } else {
            count as f64 / measured_duration_secs
        }
    };
    let transferred_bytes = successful()
        .map(|sample| sample.request_bytes + sample.response_bytes)
        .sum::<usize>();

    TransportRunSummary {
        transport,
        request_count: samples.len(),
        success_count,
        failure_count,
        measured_duration_ms: measured_duration.as_millis().try_into().unwrap_or(u64::MAX),
        throughput_rps: per_second(success_count),
        goodput_mib_s: per_second(transferred_bytes) / 1024.0 / 1024.0,
        latency_us: summarize_values(successful().map(|sample| sample.completion_us)),
        response_headers_us: summarize_values(
            successful().filter_map(|sample| sample.response_headers_us),
        ),
        first_body_us: summarize_values(successful().filter_map(|sample| sample.first_body_us)),
    }
}

fn summarize_values(values: impl Iterator<Item = u64>) -> LatencySummary {
    let mut values = values.collect::<Vec<_>>();
    values.sort_unstable();
    let percentile = |rank| {
        upper_nearest_rank_index(values.len(), rank).and_then(|index| values.get(index).copied())
    };
    LatencySummary {
        min: values.first().copied(),
        p50: percentile(0.50),
        p90: percentile(0.90),
        p95: percentile(0.95),
        p99: percentile(0.99),
        max: values.last().copied(),
    }
}

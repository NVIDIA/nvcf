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

//! Summaries of fleet driver records, measured the same way as simulator runs.

use std::collections::{BTreeMap, HashMap};

use serde::Serialize;

use crate::drive::FleetRecord;
use crate::metrics::Percentiles;
use crate::time::{Micros, micros_from_ms, micros_from_secs, ms};

#[derive(Clone, Debug, Serialize)]
pub struct FleetSummary {
    pub run: String,
    pub offered: usize,
    pub succeeded: usize,
    /// Successful requests whose first chunk met the TTFT SLO.
    pub good: usize,
    pub goodput_rps: f64,
    pub throughput_rps: f64,
    pub slo_attainment: f64,
    /// Failed requests by error code, transport error, or `timeout`.
    pub failures: BTreeMap<String, usize>,
    pub retried_requests: usize,
    pub abandoned_sessions: usize,
    pub ttft_ms: Percentiles,
    pub e2e_ms: Percentiles,
    pub input_tokens: Percentiles,
    pub reused_input_token_fraction: f64,
    pub backends: Vec<FleetBackendSummary>,
}

#[derive(Clone, Debug, Serialize)]
pub struct FleetBackendSummary {
    pub cluster_id: String,
    pub gpu_workers: Option<usize>,
    pub succeeded: usize,
    pub ttft_ms: Percentiles,
    pub reused_input_token_fraction: f64,
}

pub struct Window {
    pub start: Micros,
    pub end: Micros,
    pub ttft_slo: Micros,
}

impl Window {
    pub fn new(warmup_s: f64, measure_s: f64, ttft_slo_ms: u64) -> Self {
        let start = micros_from_secs(warmup_s);
        Self {
            start,
            end: start + micros_from_secs(measure_s),
            ttft_slo: micros_from_ms(ttft_slo_ms as f64),
        }
    }
}

/// Summarizes each run in `records`, counting requests that arrived inside
/// the measured window.
pub fn summarize(
    records: &[FleetRecord],
    window: &Window,
    backend_gpus: &HashMap<String, usize>,
) -> Vec<FleetSummary> {
    let mut runs: BTreeMap<&str, Vec<&FleetRecord>> = BTreeMap::new();
    for record in records {
        if (window.start..window.end).contains(&record.arrival_us) {
            runs.entry(&record.run).or_default().push(record);
        }
    }
    let seconds = (window.end - window.start) as f64 / 1_000_000.0;
    runs.into_iter()
        .map(|(run, measured)| summarize_run(run, &measured, window, seconds, backend_gpus))
        .collect()
}

fn summarize_run(
    run: &str,
    measured: &[&FleetRecord],
    window: &Window,
    seconds: f64,
    backend_gpus: &HashMap<String, usize>,
) -> FleetSummary {
    let succeeded: Vec<&FleetRecord> = measured
        .iter()
        .copied()
        .filter(|record| record.succeeded())
        .collect();
    let ttft = |record: &FleetRecord| record.ttft_us.unwrap_or(Micros::MAX);
    let good = succeeded
        .iter()
        .filter(|record| ttft(record) <= window.ttft_slo)
        .count();
    let mut failures = BTreeMap::new();
    for record in measured.iter().filter(|record| !record.succeeded()) {
        let kind = record
            .error
            .clone()
            .unwrap_or_else(|| "incomplete".to_string());
        *failures.entry(kind).or_default() += 1;
    }
    let mut by_backend: BTreeMap<&str, Vec<&FleetRecord>> = BTreeMap::new();
    for record in &succeeded {
        if let Some(cluster_id) = &record.cluster_id {
            by_backend.entry(cluster_id).or_default().push(record);
        }
    }
    let ratio = |numerator: usize, denominator: usize| {
        if denominator == 0 {
            0.0
        } else {
            numerator as f64 / denominator as f64
        }
    };
    FleetSummary {
        run: run.to_string(),
        offered: measured.len(),
        succeeded: succeeded.len(),
        good,
        goodput_rps: good as f64 / seconds,
        throughput_rps: succeeded.len() as f64 / seconds,
        slo_attainment: ratio(good, measured.len()),
        failures,
        retried_requests: measured.iter().filter(|record| record.attempt > 0).count(),
        abandoned_sessions: measured
            .iter()
            .filter(|record| record.abandoned_session)
            .count(),
        ttft_ms: Percentiles::from_ms(succeeded.iter().map(|record| ms(ttft(record))).collect()),
        e2e_ms: Percentiles::from_ms(
            succeeded
                .iter()
                .filter_map(|record| record.e2e_us.map(ms))
                .collect(),
        ),
        input_tokens: Percentiles::from_ms(
            measured
                .iter()
                .map(|record| record.input_tokens as f64)
                .collect(),
        ),
        reused_input_token_fraction: reuse_fraction(&succeeded),
        backends: by_backend
            .into_iter()
            .map(|(cluster_id, served)| FleetBackendSummary {
                cluster_id: cluster_id.to_string(),
                gpu_workers: backend_gpus.get(cluster_id).copied(),
                succeeded: served.len(),
                ttft_ms: Percentiles::from_ms(
                    served.iter().map(|record| ms(ttft(record))).collect(),
                ),
                reused_input_token_fraction: reuse_fraction(&served),
            })
            .collect(),
    }
}

/// Reused share of input tokens among requests that reported reuse.
fn reuse_fraction(records: &[&FleetRecord]) -> f64 {
    let (reused, input) = records
        .iter()
        .filter_map(|record| Some((record.reused_input_tokens?, record.input_tokens)))
        .fold((0u64, 0u64), |(reused, input), (r, i)| {
            (reused + r, input + i)
        });
    if input == 0 {
        0.0
    } else {
        reused as f64 / input as f64
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn record(arrival_s: f64, ttft_ms: Option<u64>, error: Option<&str>) -> FleetRecord {
        FleetRecord {
            run: "run-a".to_string(),
            region: "usw2".to_string(),
            request_id: format!("run-a-{arrival_s}"),
            session: 1,
            turn: 0,
            attempt: 0,
            arrival_us: micros_from_secs(arrival_s),
            input_tokens: 1000,
            output_tokens: 10,
            status: Some(if error.is_some() { 503 } else { 200 }),
            error: error.map(str::to_string),
            ttft_us: ttft_ms.map(|value| value * 1000),
            e2e_us: ttft_ms.map(|value| value * 1000 + 500_000),
            cluster_id: Some("backend-a".to_string()),
            reused_input_tokens: Some(800),
            abandoned_session: false,
        }
    }

    #[test]
    fn summary_counts_only_the_measured_window_and_classifies_outcomes() {
        let records = vec![
            record(1.0, Some(100), None),
            record(15.0, Some(100), None),
            record(16.0, Some(20_000), None),
            record(17.0, None, Some("overloaded_error")),
            record(40.0, Some(100), None),
        ];
        let window = Window::new(10.0, 20.0, 10_000);
        let gpus = HashMap::from([("backend-a".to_string(), 4)]);
        let summaries = summarize(&records, &window, &gpus);

        assert_eq!(summaries.len(), 1);
        let summary = &summaries[0];
        assert_eq!(summary.offered, 3);
        assert_eq!(summary.succeeded, 2);
        assert_eq!(summary.good, 1);
        assert_eq!(summary.failures.get("overloaded_error"), Some(&1));
        assert!((summary.reused_input_token_fraction - 0.8).abs() < 1e-9);
        assert_eq!(summary.backends[0].gpu_workers, Some(4));
        assert_eq!(summary.backends[0].succeeded, 2);
    }
}

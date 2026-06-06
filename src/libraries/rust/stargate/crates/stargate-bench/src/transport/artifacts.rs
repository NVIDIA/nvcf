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

use std::path::Path;

use anyhow::{Context, Result};
use serde::Serialize;

use super::report::render_transport_benchmark_report;
use super::{RequestSample, TransportBenchmarkOutcome, TransportKind};

#[derive(Serialize)]
struct RequestSampleRecord<'a> {
    transport: TransportKind,
    trial_index: usize,
    #[serde(flatten)]
    sample: &'a RequestSample,
}

pub fn write_transport_benchmark_artifacts(
    output_dir: &Path,
    outcome: &TransportBenchmarkOutcome,
) -> Result<()> {
    std::fs::create_dir_all(output_dir)
        .with_context(|| format!("failed to create {}", output_dir.display()))?;
    let summaries = outcome
        .runs
        .iter()
        .map(|run| run.summary.clone())
        .collect::<Vec<_>>();
    let summary_json = serde_json::json!({
        "config": outcome.config,
        "summaries": summaries,
        "aggregates": outcome.aggregates,
        "comparisons": outcome.comparisons,
        "warmup_run_count": outcome.warmup_runs.len(),
    });
    let summary_path = output_dir.join("transport-summary.json");
    std::fs::write(
        &summary_path,
        serde_json::to_vec_pretty(&summary_json).context("serialize transport summary")?,
    )
    .with_context(|| format!("failed to write {}", summary_path.display()))?;

    let report_path = output_dir.join("transport-report.md");
    std::fs::write(&report_path, render_transport_benchmark_report(outcome))
        .with_context(|| format!("failed to write {}", report_path.display()))?;

    let multiple_trials = [
        TransportKind::CustomProtocol,
        TransportKind::Http3H3Quinn,
        TransportKind::WebTransportH3Quinn,
    ]
    .iter()
    .any(|transport| {
        outcome
            .runs
            .iter()
            .filter(|run| run.transport == *transport)
            .count()
            > 1
    });
    for run in &outcome.runs {
        let samples_path = if multiple_trials {
            output_dir.join(format!(
                "transport-samples-{}-trial-{}.jsonl",
                run.transport.label(),
                run.trial_index
            ))
        } else {
            output_dir.join(format!("transport-samples-{}.jsonl", run.transport.label()))
        };
        let mut out = String::new();
        for sample in &run.samples {
            let record = RequestSampleRecord {
                transport: run.transport,
                trial_index: run.trial_index,
                sample,
            };
            out.push_str(&serde_json::to_string(&record).context("serialize transport sample")?);
            out.push('\n');
        }
        std::fs::write(&samples_path, out)
            .with_context(|| format!("failed to write {}", samples_path.display()))?;
    }

    Ok(())
}

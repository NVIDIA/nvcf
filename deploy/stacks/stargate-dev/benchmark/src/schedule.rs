// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::BTreeMap;
use std::fs::File;
use std::io::{BufRead, BufReader};
use std::path::Path;

use anyhow::{Context, Result, ensure};
use serde::Deserialize;

use crate::report::Metrics;
use crate::suite::{RunLimit, Stream};

#[derive(Deserialize)]
struct Timing {
    admitted_offset_ms: f64,
    completed_offset_ms: f64,
}

#[derive(Deserialize)]
struct Request {
    id: u64,
    timing: Timing,
}

#[derive(Debug, Deserialize, PartialEq)]
struct ReportedStep {
    rate_limit: f64,
    duration_ms: u64,
}

#[derive(Deserialize)]
struct Header {
    kind: String,
    workers: usize,
    rate_steps: Vec<ReportedStep>,
}

/// Checks that a rate-schedule run used the planned steps, admitted every
/// request inside the schedule and kept each session worker serial.
pub fn verify(path: &Path, stream: &Stream, metrics: &Metrics) -> Result<()> {
    if stream.rate_steps.is_empty() {
        return Ok(());
    }
    let RunLimit::DurationSeconds(seconds) = stream.limit else {
        anyhow::bail!("rate schedule requires a duration limit");
    };
    let end_ms = seconds as f64 * 1000.0;
    ensure!(
        metrics.duration_ms >= end_ms,
        "scheduled report ended before the final step"
    );
    let expected: Vec<_> = stream
        .rate_steps
        .iter()
        .map(|step| ReportedStep {
            rate_limit: step.rate.get() as f64,
            duration_ms: step.duration_seconds.get() * 1000,
        })
        .collect();

    let log = path.with_extension("requests.jsonl");
    let mut lines = BufReader::new(
        File::open(&log).with_context(|| format!("open request timeline {}", log.display()))?,
    )
    .lines();
    let header: Header = serde_json::from_str(&lines.next().context("request timeline is empty")??)
        .context("parse request timeline header")?;
    ensure!(
        header.kind == "schedule"
            && header.rate_steps == expected
            && header.workers == stream.workers,
        "request timeline configuration differs from its plan"
    );
    let workers = u64::try_from(stream.workers)?;
    let mut last_by_worker = BTreeMap::<u64, (u64, f64)>::new();
    let mut count = 0;
    for (index, line) in lines.enumerate() {
        count += 1;
        let request: Request = serde_json::from_str(&line?)
            .with_context(|| format!("parse request timeline line {}", index + 2))?;
        let admitted = request.timing.admitted_offset_ms;
        let completed = request.timing.completed_offset_ms;
        ensure!(
            request.id > 0
                && admitted.is_finite()
                && admitted >= 0.0
                && admitted < end_ms
                && completed >= admitted
                && completed <= metrics.duration_ms + 1.0,
            "invalid identity or timing in request {}",
            request.id
        );
        let worker = (request.id - 1) % workers;
        if let Some((previous_id, previous_end)) =
            last_by_worker.insert(worker, (request.id, completed))
        {
            ensure!(
                previous_id.checked_add(workers) == Some(request.id) && admitted >= previous_end,
                "session worker overlapped, skipped or repeated requests"
            );
        } else {
            ensure!(
                request.id == worker + 1,
                "session worker did not start at its assigned prompt"
            );
        }
    }
    ensure!(count == metrics.requests, "request timeline is incomplete");
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::suite::{RateStep, RequestLimits};
    use serde_json::{Value, json};

    #[test]
    fn timeline_requires_serial_sessions_inside_the_planned_schedule() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let path = directory.path().join("spark.json");
        let stream = Stream {
            name: None,
            workload: "sessions.yaml".into(),
            rate: 2,
            workers: 2,
            rate_steps: vec![RateStep {
                rate: 2.try_into()?,
                duration_seconds: 1.try_into()?,
            }],
            limit: RunLimit::DurationSeconds(1),
            limits: RequestLimits {
                max_tokens: 256,
                timeout_seconds: 30,
                request_slo_ms: 10000,
                max_wait_ms: 10000,
            },
        };
        let metrics = Metrics {
            requests: 3,
            successful: 3,
            failed: 0,
            retries: 0,
            retried_requests: 0,
            duration_ms: 1500.0,
            cache_hit_requests: 0,
            cache_observed_requests: 0,
            cached_tokens: 0,
            ttft_ms: None,
            latency_ms: None,
            itl_ms: None,
        };
        let steps = json!([{"rate_limit":2.0,"duration_ms":1000}]);
        let header = json!({"kind":"schedule","workers":2,"rate_steps":steps});
        let records = vec![
            json!({"id":1,"timing":{"admitted_offset_ms":0.0,"completed_offset_ms":500.0}}),
            json!({"id":2,"timing":{"admitted_offset_ms":250.0,"completed_offset_ms":750.0}}),
            json!({"id":3,"timing":{"admitted_offset_ms":800.0,"completed_offset_ms":1500.0}}),
        ];
        let write = |header: &Value, records: &[Value]| -> Result<()> {
            let mut text = serde_json::to_string(header)? + "\n";
            for record in records {
                text.push_str(&serde_json::to_string(record)?);
                text.push('\n');
            }
            std::fs::write(path.with_extension("requests.jsonl"), text)?;
            Ok(())
        };
        write(&header, &records)?;
        verify(&path, &stream, &metrics)?;
        for (pointer, value) in [
            ("/timing/admitted_offset_ms", json!(1000.0)),
            ("/timing/admitted_offset_ms", json!(499.0)),
            ("/timing/completed_offset_ms", json!(1502.0)),
            ("/id", json!(5)),
        ] {
            let mut invalid = records.clone();
            *invalid[2].pointer_mut(pointer).unwrap() = value;
            write(&header, &invalid)?;
            assert!(verify(&path, &stream, &metrics).is_err(), "{pointer}");
        }
        write(&header, &records[..2])?;
        assert!(verify(&path, &stream, &metrics).is_err());
        let mut invalid = header.clone();
        invalid["workers"] = json!(1);
        write(&invalid, &records)?;
        assert!(verify(&path, &stream, &metrics).is_err());
        let mut invalid = header.clone();
        invalid["rate_steps"][0]["rate_limit"] = json!(3.0);
        write(&invalid, &records)?;
        assert!(verify(&path, &stream, &metrics).is_err());
        write(&header, &records)?;
        let mut early = metrics.clone();
        early.duration_ms = 999.0;
        assert!(verify(&path, &stream, &early).is_err());
        Ok(())
    }
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::BTreeMap;
use std::fs::File;
use std::io::{BufRead, BufReader};
use std::path::Path;

use anyhow::{Context, Result, ensure};
use serde::Deserialize;

use crate::artifact::read_json;
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
    success: bool,
    http_error_status: Option<u16>,
    retries: u64,
    ttft_ms: f64,
    latency_ms: f64,
    cached_tokens: u64,
    cache_observations: u64,
    cache_hits: u64,
}

#[derive(Debug, Deserialize, PartialEq)]
struct ReportedStep {
    rate_limit: f64,
    duration_ms: u64,
}

#[derive(Deserialize)]
struct ReportConfig {
    workers: usize,
    rate_steps: Vec<ReportedStep>,
    duration_ms: u64,
}

#[derive(Deserialize)]
struct Report {
    config: ReportConfig,
}

#[derive(Deserialize)]
struct Header {
    kind: String,
    workers: usize,
    rate_steps: Vec<ReportedStep>,
}

#[derive(Default, PartialEq, Eq)]
struct Totals {
    requests: u64,
    successful: u64,
    failed: u64,
    retries: u64,
    retried_requests: u64,
    cache_hits: u64,
    cache_observations: u64,
    cached_tokens: u64,
}

impl Totals {
    fn add(&mut self, request: &Request) -> Result<()> {
        for (total, value) in [
            (&mut self.requests, 1),
            (&mut self.successful, u64::from(request.success)),
            (&mut self.failed, u64::from(!request.success)),
            (&mut self.retries, request.retries),
            (&mut self.retried_requests, u64::from(request.retries > 0)),
            (&mut self.cache_hits, request.cache_hits),
            (&mut self.cache_observations, request.cache_observations),
            (
                &mut self.cached_tokens,
                if request.success {
                    request.cached_tokens
                } else {
                    0
                },
            ),
        ] {
            *total = total
                .checked_add(value)
                .context("request timeline counter overflow")?;
        }
        Ok(())
    }
}

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
    let report: Report = read_json(path)?;
    ensure!(
        report.config.rate_steps == expected
            && report.config.workers == stream.workers
            && report.config.duration_ms == seconds * 1000,
        "Spark report configuration differs from the planned rate schedule"
    );

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
    let mut totals = Totals::default();
    for (index, line) in lines.enumerate() {
        let request: Request = serde_json::from_str(&line?)
            .with_context(|| format!("parse request timeline line {}", index + 2))?;
        let admitted = request.timing.admitted_offset_ms;
        let completed = request.timing.completed_offset_ms;
        ensure!(
            [admitted, completed, request.ttft_ms, request.latency_ms]
                .iter()
                .all(|value| value.is_finite() && *value >= 0.0)
                && admitted < end_ms
                && completed >= admitted
                && completed <= metrics.duration_ms + 1.0,
            "invalid timing in request {}",
            request.id
        );
        ensure!(
            request.id > 0 && request.cache_hits <= request.cache_observations,
            "invalid identity or cache counters in request {}",
            request.id
        );
        ensure!(
            request
                .http_error_status
                .is_none_or(|status| !request.success
                    && status != 200
                    && (100..1000).contains(&status)),
            "invalid HTTP error status in request {}",
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
        totals.add(&request)?;
    }
    ensure!(
        totals
            == Totals {
                requests: metrics.requests,
                successful: metrics.successful,
                failed: metrics.failed,
                retries: metrics.retries,
                retried_requests: metrics.retried_requests,
                cache_hits: metrics.cache_hit_requests,
                cache_observations: metrics.cache_observed_requests,
                cached_tokens: metrics.cached_tokens,
            },
        "request timeline counters differ from the Spark summary"
    );
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::suite::{RateStep, RequestLimits};
    use serde_json::{Value, json};

    #[test]
    fn timeline_requires_complete_accounting_serial_sessions_and_matching_schedule() -> Result<()> {
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
            successful: 2,
            failed: 1,
            retries: 1,
            retried_requests: 1,
            duration_ms: 1500.0,
            cache_hit_requests: 2,
            cache_observed_requests: 2,
            cached_tokens: 6,
            ttft_ms: None,
            latency_ms: None,
            itl_ms: None,
        };
        let steps = json!([{"rate_limit":2.0,"duration_ms":1000}]);
        let report = json!({"config":{"workers":2,"rate_steps":steps,"duration_ms":1000}});
        std::fs::write(&path, serde_json::to_vec(&report)?)?;
        let header = json!({"kind":"schedule","workers":2,"rate_steps":steps});
        let records = vec![
            json!({"id":1,"timing":{"admitted_offset_ms":0.0,"completed_offset_ms":500.0},"success":true,"http_error_status":null,"retries":0,"ttft_ms":100.0,"latency_ms":500.0,"cached_tokens":3,"cache_observations":1,"cache_hits":1}),
            json!({"id":2,"timing":{"admitted_offset_ms":250.0,"completed_offset_ms":750.0},"success":false,"http_error_status":429,"retries":1,"ttft_ms":0.0,"latency_ms":500.0,"cached_tokens":0,"cache_observations":0,"cache_hits":0}),
            json!({"id":3,"timing":{"admitted_offset_ms":800.0,"completed_offset_ms":1500.0},"success":true,"http_error_status":null,"retries":0,"ttft_ms":100.0,"latency_ms":700.0,"cached_tokens":3,"cache_observations":1,"cache_hits":1}),
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
        let mut non_ok = records.clone();
        non_ok[1]["http_error_status"] = json!(201);
        write(&header, &non_ok)?;
        verify(&path, &stream, &metrics)?;
        for (pointer, value) in [
            ("/timing/admitted_offset_ms", json!(1000.0)),
            ("/timing/admitted_offset_ms", json!(499.0)),
            ("/timing/completed_offset_ms", json!(1502.0)),
            ("/id", json!(5)),
            ("/http_error_status", json!(200)),
            ("/cached_tokens", json!(4)),
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
        write(&header, &records)?;
        let mut invalid = report.clone();
        invalid["config"]["rate_steps"][0]["rate_limit"] = json!(3.0);
        std::fs::write(&path, serde_json::to_vec(&invalid)?)?;
        assert!(verify(&path, &stream, &metrics).is_err());
        std::fs::write(&path, serde_json::to_vec(&report)?)?;
        let mut early = metrics.clone();
        early.duration_ms = 999.0;
        assert!(verify(&path, &stream, &early).is_err());
        Ok(())
    }
}

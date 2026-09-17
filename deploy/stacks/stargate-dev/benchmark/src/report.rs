// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, BTreeSet};
use std::fs::{self, File};
use std::io::{BufReader, Read, Write};
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use tempfile::NamedTempFile;

use crate::suite::{Algorithm, RequestLimits, RunLimit};
use crate::workload::Workload;

const DEFINITIONS: &str = concat!(
    "Successful throughput is successful completed requests divided by reported wall time. ",
    "Fixed-duration runs exclude window-end in-flight cancellations from recorded request/failure counts and latency quantiles. ",
    "Latency quantiles describe successful requests, include client retry time, and are histogram bucket lower bounds. ",
    "Cache percentages use reported KV observations, which can include failed requests and multiple responses per logical request; zero observations mean unknown. ",
    "Ratios compare matched algorithm pairs. Repeat ranges are observed minima and maxima, not confidence intervals. ",
    "These measurements do not establish offered-load SLO attainment or production capacity. ",
    "Production configuration recommendations remain pending real-engine QA."
);

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct VerifiedReport {
    pub sha256: String,
    pub metrics: Metrics,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Metrics {
    pub requests: u64,
    pub successful: u64,
    pub failed: u64,
    pub retries: u64,
    pub retried_requests: u64,
    pub duration_ms: f64,
    pub cache_hit_requests: u64,
    pub cache_observed_requests: u64,
    pub cached_tokens: u64,
    pub ttft_ms: Option<Distribution>,
    pub latency_ms: Option<Distribution>,
    pub itl_ms: Option<Distribution>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Distribution {
    pub min: f64,
    pub max: f64,
    pub mean: f64,
    pub p50: f64,
    pub p90: f64,
    pub p95: f64,
    pub p99: f64,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Row {
    pub case: String,
    pub pair_id: String,
    pub algorithm: Algorithm,
    pub rate: usize,
    pub workers: usize,
    pub limit: RunLimit,
    pub limits: RequestLimits,
    pub workload: Workload,
    pub source: PathBuf,
    pub metrics: Metrics,
}

#[derive(Deserialize)]
struct SparkReport {
    summary: SparkSummary,
    throughput: SparkThroughput,
    ttft_ms: Option<Distribution>,
    latency_ms: Option<Distribution>,
    itl_ms: Option<Distribution>,
}

#[derive(Deserialize)]
struct SparkSummary {
    total_requests: u64,
    successful: u64,
    failed: u64,
    total_retries: u64,
    retried_requests: u64,
    total_duration_ms: u64,
}

#[derive(Deserialize)]
struct SparkThroughput {
    kv_cache_hit_requests: u64,
    kv_cache_observed_requests: u64,
    total_cached_tokens: u64,
}

impl Distribution {
    fn validate(&self) -> Result<()> {
        ensure!(
            [
                self.min, self.max, self.mean, self.p50, self.p90, self.p95, self.p99
            ]
            .iter()
            .all(|value| value.is_finite() && *value >= 0.0),
            "latency values must be finite and nonnegative"
        );
        ensure!(self.min <= self.max, "latency minimum exceeds its maximum");
        ensure!(
            self.p50 <= self.p90 && self.p90 <= self.p95 && self.p95 <= self.p99,
            "latency quantiles must not decrease"
        );
        // Bucket lower bounds may be below the exact observed minimum.
        ensure!(self.p99 <= self.max, "latency quantile exceeds its maximum");
        Ok(())
    }
}

impl Metrics {
    pub fn successful_rps(&self) -> Option<f64> {
        divide(
            Some(self.successful as f64 * 1000.0),
            Some(self.duration_ms),
        )
    }

    pub fn cache_hit_pct(&self) -> Option<f64> {
        divide(
            Some(self.cache_hit_requests as f64 * 100.0),
            Some(self.cache_observed_requests as f64),
        )
    }

    fn validate(&self) -> Result<()> {
        ensure!(
            self.successful.checked_add(self.failed) == Some(self.requests),
            "successful and failed counts do not equal total requests"
        );
        ensure!(
            self.retried_requests <= self.requests,
            "retried request count exceeds total requests"
        );
        ensure!(
            self.retries >= self.retried_requests
                && (self.retries == 0) == (self.retried_requests == 0),
            "retry count is inconsistent with retried requests"
        );
        ensure!(
            self.cache_hit_requests <= self.cache_observed_requests,
            "cache hits exceed cache observations"
        );
        ensure!(
            self.duration_ms.is_finite() && self.duration_ms >= 0.0,
            "reported duration must be finite and nonnegative"
        );
        for (name, distribution) in [
            ("TTFT", &self.ttft_ms),
            ("end-to-end latency", &self.latency_ms),
            ("inter-token latency", &self.itl_ms),
        ] {
            if let Some(distribution) = distribution {
                distribution
                    .validate()
                    .with_context(|| format!("invalid {name}"))?;
            } else {
                ensure!(self.successful == 0, "successful report is missing {name}");
            }
        }
        Ok(())
    }
}

pub fn inspect(path: &Path, limit: RunLimit, cold_capacity: bool) -> Result<VerifiedReport> {
    let file = File::open(path).with_context(|| format!("open Spark report {}", path.display()))?;
    let mut reader = BufReader::with_capacity(
        64 * 1024,
        HashedReader {
            file,
            hasher: Sha256::new(),
        },
    );
    let report: SparkReport = serde_json::from_reader(&mut reader)
        .with_context(|| format!("parse Spark report {}", path.display()))?;
    let sha256 = format!("{:x}", reader.into_inner().hasher.finalize());
    let mut metrics = Metrics {
        requests: report.summary.total_requests,
        successful: report.summary.successful,
        failed: report.summary.failed,
        retries: report.summary.total_retries,
        retried_requests: report.summary.retried_requests,
        duration_ms: report.summary.total_duration_ms as f64,
        cache_hit_requests: report.throughput.kv_cache_hit_requests,
        cache_observed_requests: report.throughput.kv_cache_observed_requests,
        cached_tokens: report.throughput.total_cached_tokens,
        ttft_ms: report.ttft_ms,
        latency_ms: report.latency_ms,
        itl_ms: report.itl_ms,
    };
    metrics
        .validate()
        .with_context(|| format!("invalid Spark report {}", path.display()))?;
    match limit {
        RunLimit::Requests(requests) => {
            ensure!(requests > 0, "fixed-count request limit must be positive");
            ensure!(
                metrics.requests == u64::try_from(requests)?,
                "incomplete fixed-count report {}: recorded {}, expected {requests}",
                path.display(),
                metrics.requests
            );
        }
        RunLimit::DurationSeconds(seconds) => {
            ensure!(seconds > 0, "fixed-duration limit must be positive");
        }
    }
    if cold_capacity {
        ensure!(
            metrics.cache_hit_requests == 0,
            "cold-capacity report contains cache hits: {}",
            path.display()
        );
        ensure!(
            metrics.successful == 0 || metrics.cache_observed_requests > 0,
            "cold-capacity report lacks cache observations: {}",
            path.display()
        );
    }
    if metrics.successful == 0 {
        metrics.ttft_ms = None;
        metrics.latency_ms = None;
        metrics.itl_ms = None;
    }
    Ok(VerifiedReport { sha256, metrics })
}

pub fn verify_accepted(
    path: &Path,
    expected_sha256: &str,
    limit: RunLimit,
    cold_capacity: bool,
) -> Result<VerifiedReport> {
    let report = inspect(path, limit, cold_capacity)?;
    ensure!(
        report.sha256 == expected_sha256,
        "accepted Spark report checksum changed: {}",
        path.display()
    );
    Ok(report)
}

struct HashedReader {
    file: File,
    hasher: Sha256,
}

impl Read for HashedReader {
    fn read(&mut self, buffer: &mut [u8]) -> std::io::Result<usize> {
        let read = self.file.read(buffer)?;
        self.hasher.update(&buffer[..read]);
        Ok(read)
    }
}

fn divide(numerator: Option<f64>, denominator: Option<f64>) -> Option<f64> {
    let numerator = numerator?;
    let denominator = denominator?;
    (denominator > 0.0)
        .then(|| numerator / denominator)
        .filter(|value| value.is_finite())
}

fn matching_workloads(left: &Workload, right: &Workload) -> bool {
    match (left, right) {
        (
            Workload::Unique {
                count: left_count,
                bytes: left_bytes,
                ..
            },
            Workload::Unique {
                count: right_count,
                bytes: right_bytes,
                ..
            },
        ) => left_count == right_count && left_bytes == right_bytes,
        _ => left == right,
    }
}

fn matching_conditions(left: &Row, right: &Row) -> bool {
    left.case == right.case
        && left.rate == right.rate
        && left.workers == right.workers
        && left.limit == right.limit
        && left.limits == right.limits
        && matching_workloads(&left.workload, &right.workload)
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct Comparison<'a> {
    pair_id: &'a str,
    case: &'a str,
    rate: usize,
    workers: usize,
    wait_and_widen_source: &'a Path,
    power_of_n_source: &'a Path,
    goodput_ratio: Option<f64>,
    ttft_p99_ratio: Option<f64>,
    latency_p99_ratio: Option<f64>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct RatioSummary {
    observed_pairs: usize,
    median: Option<f64>,
    observed_range: Option<[f64; 2]>,
}

impl RatioSummary {
    fn new(values: impl Iterator<Item = Option<f64>>) -> Self {
        let mut values: Vec<_> = values.flatten().collect();
        values.sort_by(f64::total_cmp);
        let median = if values.is_empty() {
            None
        } else {
            let middle = values.len() / 2;
            Some(if values.len().is_multiple_of(2) {
                values[middle - 1] / 2.0 + values[middle] / 2.0
            } else {
                values[middle]
            })
        };
        Self {
            observed_pairs: values.len(),
            median,
            observed_range: values
                .first()
                .zip(values.last())
                .map(|(low, high)| [*low, *high]),
        }
    }
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct Aggregate<'a> {
    case: &'a str,
    rate: usize,
    workers: usize,
    pairs: usize,
    goodput_ratio: RatioSummary,
    ttft_p99_ratio: RatioSummary,
    latency_p99_ratio: RatioSummary,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct RenderedRow<'a> {
    #[serde(flatten)]
    row: &'a Row,
    successful_rps: Option<f64>,
    cache_hit_pct: Option<f64>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct Summary<'a> {
    definitions: &'static str,
    rows: Vec<RenderedRow<'a>>,
    comparisons: Vec<Comparison<'a>>,
    aggregates: Vec<Aggregate<'a>>,
    unpaired: Vec<&'a str>,
}

fn summarize(rows: &[Row]) -> Result<Summary<'_>> {
    let mut pairs: BTreeMap<&str, BTreeMap<Algorithm, &Row>> = BTreeMap::new();
    let mut sources = BTreeSet::new();
    for row in rows {
        ensure!(
            !row.case.is_empty() && !row.pair_id.is_empty(),
            "report rows require case and pair IDs"
        );
        ensure!(
            row.rate > 0 && row.workers > 0,
            "report rows require positive rate and workers"
        );
        row.metrics
            .validate()
            .with_context(|| format!("invalid metrics for {}", row.source.display()))?;
        row.workload
            .validate()
            .with_context(|| format!("invalid workload for {}", row.source.display()))?;
        ensure!(
            sources.insert(&row.source),
            "report source is repeated: {}",
            row.source.display()
        );
        ensure!(
            pairs
                .entry(&row.pair_id)
                .or_default()
                .insert(row.algorithm, row)
                .is_none(),
            "pair {} repeats algorithm {}",
            row.pair_id,
            row.algorithm
        );
    }
    let mut comparisons = Vec::new();
    let mut unpaired = Vec::new();
    let mut groups: BTreeMap<(&str, usize, usize), (&Row, Vec<usize>)> = BTreeMap::new();
    for (pair_id, members) in pairs {
        let (Some(waw), Some(power)) = (
            members.get(&Algorithm::WaitAndWiden),
            members.get(&Algorithm::PowerOfN),
        ) else {
            unpaired.push(pair_id);
            continue;
        };
        ensure!(
            matching_conditions(waw, power),
            "pair {pair_id} has different workload or execution settings"
        );
        let group = groups
            .entry((&waw.case, waw.rate, waw.workers))
            .or_insert_with(|| (waw, Vec::new()));
        ensure!(
            matching_conditions(group.0, waw),
            "repeat settings changed for case {} at {} RPS",
            waw.case,
            waw.rate
        );
        group.1.push(comparisons.len());
        comparisons.push(Comparison {
            pair_id,
            case: &waw.case,
            rate: waw.rate,
            workers: waw.workers,
            wait_and_widen_source: &waw.source,
            power_of_n_source: &power.source,
            goodput_ratio: divide(waw.metrics.successful_rps(), power.metrics.successful_rps()),
            ttft_p99_ratio: divide(
                waw.metrics.ttft_ms.as_ref().map(|value| value.p99),
                power.metrics.ttft_ms.as_ref().map(|value| value.p99),
            ),
            latency_p99_ratio: divide(
                waw.metrics.latency_ms.as_ref().map(|value| value.p99),
                power.metrics.latency_ms.as_ref().map(|value| value.p99),
            ),
        });
    }
    let aggregates = groups
        .into_iter()
        .map(|((case, rate, workers), (_, indices))| Aggregate {
            case,
            rate,
            workers,
            pairs: indices.len(),
            goodput_ratio: RatioSummary::new(
                indices
                    .iter()
                    .map(|index| comparisons[*index].goodput_ratio),
            ),
            ttft_p99_ratio: RatioSummary::new(
                indices
                    .iter()
                    .map(|index| comparisons[*index].ttft_p99_ratio),
            ),
            latency_p99_ratio: RatioSummary::new(
                indices
                    .iter()
                    .map(|index| comparisons[*index].latency_p99_ratio),
            ),
        })
        .collect();
    Ok(Summary {
        definitions: DEFINITIONS,
        rows: rows
            .iter()
            .map(|row| RenderedRow {
                row,
                successful_rps: row.metrics.successful_rps(),
                cache_hit_pct: row.metrics.cache_hit_pct(),
            })
            .collect(),
        comparisons,
        aggregates,
        unpaired,
    })
}

pub fn render(output: &Path, rows: &[Row]) -> Result<()> {
    let summary = summarize(rows)?;
    fs::create_dir_all(output)
        .with_context(|| format!("create report directory {}", output.display()))?;
    let json = stage(output, |file| {
        serde_json::to_writer_pretty(&mut *file, &summary)?;
        file.write_all(b"\n")?;
        Ok(())
    })?;
    let csv = stage(output, |file| write_csv(file, rows))?;
    let text = stage(output, |file| write_text(file, &summary))?;
    for (file, name) in [
        (json, "summary.json"),
        (csv, "summary.csv"),
        (text, "summary.txt"),
    ] {
        let path = output.join(name);
        file.persist(&path)
            .with_context(|| format!("replace derived report {}", path.display()))?;
    }
    Ok(())
}

fn stage(directory: &Path, render: impl FnOnce(&mut File) -> Result<()>) -> Result<NamedTempFile> {
    let mut file = NamedTempFile::new_in(directory).context("stage derived report")?;
    render(file.as_file_mut())?;
    file.flush().context("flush derived report")?;
    Ok(file)
}

fn optional_number(value: Option<f64>) -> String {
    value.map(|value| value.to_string()).unwrap_or_default()
}

fn write_csv(file: &mut File, rows: &[Row]) -> Result<()> {
    let mut writer = csv::Writer::from_writer(file);
    writer.write_record([
        "case",
        "pair_id",
        "algorithm",
        "rate",
        "workers",
        "requested_count",
        "duration_seconds",
        "max_tokens",
        "timeout_seconds",
        "request_slo_ms",
        "max_wait_ms",
        "workload",
        "source",
        "requests",
        "successful",
        "failed",
        "retries",
        "retried_requests",
        "reported_duration_ms",
        "successful_rps",
        "cache_hits",
        "cache_observations",
        "cache_hit_pct",
        "cached_tokens",
        "ttft_p99_ms",
        "latency_p99_ms",
        "itl_p99_ms",
    ])?;
    for row in rows {
        let (count, duration) = match row.limit {
            RunLimit::Requests(count) => (count.to_string(), String::new()),
            RunLimit::DurationSeconds(duration) => (String::new(), duration.to_string()),
        };
        writer.write_record([
            row.case.clone(),
            row.pair_id.clone(),
            row.algorithm.to_string(),
            row.rate.to_string(),
            row.workers.to_string(),
            count,
            duration,
            row.limits.max_tokens.to_string(),
            row.limits.timeout_seconds.to_string(),
            row.limits.request_slo_ms.to_string(),
            row.limits.max_wait_ms.to_string(),
            serde_json::to_string(&row.workload)?,
            row.source.to_string_lossy().into_owned(),
            row.metrics.requests.to_string(),
            row.metrics.successful.to_string(),
            row.metrics.failed.to_string(),
            row.metrics.retries.to_string(),
            row.metrics.retried_requests.to_string(),
            row.metrics.duration_ms.to_string(),
            optional_number(row.metrics.successful_rps()),
            row.metrics.cache_hit_requests.to_string(),
            row.metrics.cache_observed_requests.to_string(),
            optional_number(row.metrics.cache_hit_pct()),
            row.metrics.cached_tokens.to_string(),
            optional_number(row.metrics.ttft_ms.as_ref().map(|value| value.p99)),
            optional_number(row.metrics.latency_ms.as_ref().map(|value| value.p99)),
            optional_number(row.metrics.itl_ms.as_ref().map(|value| value.p99)),
        ])?;
    }
    writer.flush()?;
    Ok(())
}

fn value_text(value: Option<f64>) -> String {
    value
        .map(|value| format!("{value:.3}"))
        .unwrap_or_else(|| "unknown".into())
}

fn percentage_change(ratio: f64) -> Result<f64> {
    let change = (ratio - 1.0) * 100.0;
    ensure!(
        change.is_finite(),
        "paired percentage change exceeds the finite numeric range"
    );
    Ok(change)
}

fn write_text(file: &mut File, summary: &Summary<'_>) -> Result<()> {
    writeln!(file, "Spark benchmark results\n")?;
    for rendered in &summary.rows {
        let row = rendered.row;
        writeln!(
            file,
            "{} / {} / {}: {} successful requests/s; {} recorded failures / {} requests; {} client retries; KV hits {} / {} observations ({}%); TTFT p99 {} ms; end-to-end p99 {} ms.",
            row.case,
            row.pair_id,
            row.algorithm,
            value_text(rendered.successful_rps),
            row.metrics.failed,
            row.metrics.requests,
            row.metrics.retries,
            row.metrics.cache_hit_requests,
            row.metrics.cache_observed_requests,
            value_text(rendered.cache_hit_pct),
            value_text(row.metrics.ttft_ms.as_ref().map(|value| value.p99)),
            value_text(row.metrics.latency_ms.as_ref().map(|value| value.p99))
        )?;
    }
    for aggregate in &summary.aggregates {
        writeln!(
            file,
            "\n{} at {} RPS / {} workers: {} matched pairs",
            aggregate.case, aggregate.rate, aggregate.workers, aggregate.pairs
        )?;
        for (name, ratio) in [
            ("Successful throughput", &aggregate.goodput_ratio),
            ("TTFT p99", &aggregate.ttft_p99_ratio),
            ("End-to-end p99", &aggregate.latency_p99_ratio),
        ] {
            match (ratio.median, ratio.observed_range) {
                (Some(median), Some([low, high])) => writeln!(
                    file,
                    "  {name}: median paired change {:+.2}%; observed range {:+.2}% to {:+.2}% ({} defined ratios).",
                    percentage_change(median)?,
                    percentage_change(low)?,
                    percentage_change(high)?,
                    ratio.observed_pairs
                )?,
                _ => writeln!(file, "  {name}: unknown; no defined paired ratios.")?,
            }
        }
    }
    if !summary.unpaired.is_empty() {
        writeln!(
            file,
            "\nNo paired comparison for: {}",
            summary.unpaired.join(", ")
        )?;
    }
    writeln!(file, "\n{}", summary.definitions)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::{Value, json};

    fn raw() -> Value {
        let distribution =
            json!({"min": 1.1,"max": 10.0,"mean": 5.0,"p50": 1.0,"p90": 5.0,"p95": 6.0,"p99": 8.0});
        json!({
            "summary": {"total_requests":10,"successful":8,"failed":2,"total_retries":3,"retried_requests":2,"total_duration_ms":2000},
            "throughput": {"kv_cache_hit_requests":3,"kv_cache_observed_requests":12,"total_cached_tokens":17},
            "ttft_ms":distribution,"latency_ms":distribution,"itl_ms":distribution,
            "ignored_request_details":[{"large_body":"ignored"}]
        })
    }

    fn inspect_value(value: &Value, limit: RunLimit, cold: bool) -> Result<VerifiedReport> {
        let directory = tempfile::tempdir()?;
        let path = directory.path().join("spark.json");
        fs::write(&path, serde_json::to_vec(value)?)?;
        inspect(&path, limit, cold)
    }

    fn row(pair: &str, algorithm: Algorithm, successful: u64, duration_ms: f64) -> Row {
        let mut metrics = inspect_value(&raw(), RunLimit::Requests(10), false)
            .unwrap()
            .metrics;
        metrics.requests = successful;
        metrics.successful = successful;
        metrics.failed = 0;
        metrics.retries = 0;
        metrics.retried_requests = 0;
        metrics.duration_ms = duration_ms;
        if successful == 0 {
            metrics.cache_hit_requests = 0;
            metrics.cache_observed_requests = 0;
            metrics.cached_tokens = 0;
            metrics.ttft_ms = None;
            metrics.latency_ms = None;
            metrics.itl_ms = None;
        }
        Row {
            case: "capacity".into(),
            pair_id: pair.into(),
            algorithm,
            rate: 8,
            workers: 16,
            limit: RunLimit::DurationSeconds(10),
            limits: RequestLimits {
                max_tokens: 256,
                timeout_seconds: 30,
                request_slo_ms: 10000,
                max_wait_ms: 10000,
            },
            workload: Workload::Unique {
                count: 84,
                bytes: 512,
                start: if algorithm == Algorithm::WaitAndWiden {
                    32
                } else {
                    116
                },
            },
            source: PathBuf::from(format!("{pair}/{algorithm}/spark.json")),
            metrics,
        }
    }

    #[test]
    fn verifies_full_file_hash_and_ignores_unneeded_payloads() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let path = directory.path().join("spark.json");
        let mut report = raw();
        report["ignored_request_details"] = json!(["x".repeat(200_000)]);
        let bytes = format!("{}\n \t", serde_json::to_string(&report)?);
        fs::write(&path, &bytes)?;
        let expected = format!("{:x}", Sha256::digest(bytes.as_bytes()));
        let verified = verify_accepted(&path, &expected, RunLimit::Requests(10), false)?;
        assert_eq!(verified.sha256, expected);
        assert_eq!(verified.metrics.successful_rps(), Some(4.0));
        assert_eq!(verified.metrics.cache_hit_pct(), Some(25.0));
        assert_eq!(verified.metrics.cached_tokens, 17);
        fs::write(&path, format!("{bytes}\n"))?;
        assert!(verify_accepted(&path, &expected, RunLimit::Requests(10), false).is_err());
        Ok(())
    }

    #[test]
    fn rejects_inconsistent_counts_incomplete_runs_and_invalid_latencies() {
        for (pointer, replacement) in [
            ("/summary/failed", json!(1)),
            ("/summary/successful", json!(u64::MAX)),
            ("/summary/retried_requests", json!(11)),
            ("/summary/total_retries", json!(1)),
            ("/summary/retried_requests", json!(0)),
            ("/summary/total_duration_ms", json!(-1)),
            ("/throughput/kv_cache_hit_requests", json!(13)),
            ("/ttft_ms/p99", json!(-1)),
            ("/ttft_ms/p99", json!(11)),
            ("/ttft_ms/p95", json!(9)),
            ("/latency_ms", Value::Null),
            ("/itl_ms/mean", Value::Null),
        ] {
            let mut report = raw();
            *report.pointer_mut(pointer).unwrap() = replacement;
            assert!(
                inspect_value(&report, RunLimit::Requests(10), false).is_err(),
                "accepted {pointer}"
            );
        }
        assert!(inspect_value(&raw(), RunLimit::Requests(11), false).is_err());
    }

    #[test]
    fn zero_success_and_timed_empty_reports_keep_undefined_metrics_unknown() -> Result<()> {
        for failed in [0, 10] {
            let mut report = raw();
            report["summary"] = json!({"total_requests":failed,"successful":0,"failed":failed,"total_retries":0,"retried_requests":0,"total_duration_ms":0});
            report["throughput"] = json!({"kv_cache_hit_requests":0,"kv_cache_observed_requests":0,"total_cached_tokens":0});
            let metrics = inspect_value(&report, RunLimit::DurationSeconds(1), true)?.metrics;
            assert_eq!(metrics.successful_rps(), None);
            assert_eq!(metrics.cache_hit_pct(), None);
            assert!(
                metrics.ttft_ms.is_none()
                    && metrics.latency_ms.is_none()
                    && metrics.itl_ms.is_none()
            );
        }
        let mut report = raw();
        report["throughput"]["kv_cache_hit_requests"] = json!(0);
        report["throughput"]["kv_cache_observed_requests"] = json!(0);
        assert_eq!(
            inspect_value(&report, RunLimit::Requests(10), false)?
                .metrics
                .cache_hit_pct(),
            None
        );
        assert!(inspect_value(&report, RunLimit::Requests(10), true).is_err());
        report["throughput"]["kv_cache_observed_requests"] = json!(1);
        inspect_value(&report, RunLimit::Requests(10), true)?;
        for field in ["ttft_ms", "latency_ms", "itl_ms"] {
            report[field] = json!({"min":0,"max":0,"mean":0,"p50":0,"p90":0,"p95":0,"p99":0});
        }
        assert_eq!(
            inspect_value(&report, RunLimit::Requests(10), true)?
                .metrics
                .ttft_ms
                .unwrap()
                .p99,
            0.0
        );
        assert!(inspect_value(&raw(), RunLimit::Requests(10), true).is_err());
        Ok(())
    }

    #[test]
    fn pairing_requires_explicit_matching_conditions_but_allows_disjoint_ids() -> Result<()> {
        let waw = row("pair", Algorithm::WaitAndWiden, 10, 1000.0);
        let power = row("pair", Algorithm::PowerOfN, 10, 1000.0);
        assert_eq!(
            summarize(&[waw.clone(), power.clone()])?.comparisons.len(),
            1
        );
        for changed in ["workload", "rate", "workers", "limit", "slo"] {
            let mut other = power.clone();
            match changed {
                "workload" => {
                    other.workload = Workload::Unique {
                        count: 84,
                        bytes: 1024,
                        start: 116,
                    }
                }
                "rate" => other.rate += 1,
                "workers" => other.workers += 1,
                "limit" => other.limit = RunLimit::DurationSeconds(11),
                "slo" => other.limits.request_slo_ms += 1,
                _ => unreachable!(),
            }
            assert!(
                summarize(&[waw.clone(), other]).is_err(),
                "accepted unmatched {changed}"
            );
        }
        let single = [waw];
        let summary = summarize(&single)?;
        assert!(summary.comparisons.is_empty());
        assert_eq!(summary.unpaired, ["pair"]);
        Ok(())
    }

    #[test]
    fn paired_ratios_use_observed_repeat_ranges_and_undefined_baselines() -> Result<()> {
        let rows = [
            row("one", Algorithm::WaitAndWiden, 10, 1000.0),
            row("one", Algorithm::PowerOfN, 10, 2000.0),
            row("two", Algorithm::WaitAndWiden, 10, 2000.0),
            row("two", Algorithm::PowerOfN, 10, 1000.0),
            row("zero", Algorithm::WaitAndWiden, 10, 1000.0),
            row("zero", Algorithm::PowerOfN, 0, 1000.0),
        ];
        let summary = summarize(&rows)?;
        assert_eq!(summary.aggregates.len(), 1);
        let ratios = &summary.aggregates[0].goodput_ratio;
        assert_eq!(summary.aggregates[0].pairs, 3);
        assert_eq!(ratios.observed_pairs, 2);
        assert_eq!(ratios.median, Some(1.25));
        assert_eq!(ratios.observed_range, Some([0.5, 2.0]));
        let zero = summary
            .comparisons
            .iter()
            .find(|pair| pair.pair_id == "zero")
            .unwrap();
        assert_eq!(zero.goodput_ratio, None);
        assert_eq!(zero.ttft_p99_ratio, None);
        Ok(())
    }

    #[test]
    fn renderer_escapes_csv_derives_prose_and_preserves_outputs_on_invalid_input() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let mut waw = row("one", Algorithm::WaitAndWiden, 10, 1000.0);
        waw.case = "case, \"quoted\"\nsecond line".into();
        let mut power = row("one", Algorithm::PowerOfN, 10, 2000.0);
        power.case.clone_from(&waw.case);
        let rows = [waw.clone(), power];
        render(directory.path(), &rows)?;
        let original = fs::read(directory.path().join("summary.json"))?;
        let json: Value = serde_json::from_slice(&original)?;
        assert_eq!(json["comparisons"][0]["goodputRatio"], 2.0);
        let mut csv = csv::Reader::from_path(directory.path().join("summary.csv"))?;
        let record = csv.records().next().unwrap()?;
        assert_eq!(&record[0], waw.case);
        let text = fs::read_to_string(directory.path().join("summary.txt"))?;
        assert!(text.contains("+100.00%"));
        assert!(text.contains("not confidence intervals"));
        assert!(text.contains("window-end in-flight cancellations"));
        waw.metrics.duration_ms = f64::NAN;
        assert!(render(directory.path(), &[waw]).is_err());
        assert_eq!(fs::read(directory.path().join("summary.json"))?, original);
        let mut overflowing = rows.clone();
        overflowing[1].metrics.ttft_ms = Some(Distribution {
            min: 8e-308,
            max: 8e-308,
            mean: 8e-308,
            p50: 8e-308,
            p90: 8e-308,
            p95: 8e-308,
            p99: 8e-308,
        });
        assert!(render(directory.path(), &overflowing).is_err());
        assert_eq!(fs::read(directory.path().join("summary.json"))?, original);
        Ok(())
    }
}

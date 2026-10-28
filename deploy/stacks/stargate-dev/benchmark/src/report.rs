// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::artifact::atomic_json;
use crate::suite::{Algorithm, RateStep, RequestLimits, RunLimit};
use crate::workload::Workload;

const BASELINE: Algorithm = Algorithm::WaitAndWiden;

const DEFINITIONS: &str = concat!(
    "Successful throughput is successful completed requests divided by reported wall time. ",
    "Fixed-duration runs exclude window-end in-flight cancellations from recorded request/failure counts and latency quantiles. ",
    "Rate schedules instead stop admissions at the final step and drain all admitted requests; their throughput divides by the scheduled window and excludes the drain. ",
    "Latency quantiles describe successful requests, include client retry time, and are histogram bucket lower bounds. ",
    "Cache percentages use reported KV observations, which can include failed requests and multiple responses per logical request; zero observations mean unknown. ",
    "Ratios divide wait-and-widen by each other algorithm in the same pair; a ratio is undefined when either arm has no successful requests. ",
    "Repeat ranges are observed minima and maxima, not confidence intervals. ",
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
    #[serde(rename = "initialRate")]
    pub rate: usize,
    pub rate_steps: Vec<RateStep>,
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

impl Row {
    /// Rate schedules measure the scheduled window; other runs use reported wall time.
    fn successful_rps(&self) -> Option<f64> {
        let window_ms = match self.limit {
            RunLimit::DurationSeconds(seconds) if !self.rate_steps.is_empty() => {
                seconds as f64 * 1000.0
            }
            _ => self.metrics.duration_ms,
        };
        divide(
            Some(self.metrics.successful as f64 * 1000.0),
            Some(window_ms),
        )
    }
}

impl Metrics {
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
    let bytes = fs::read(path).with_context(|| format!("read Spark report {}", path.display()))?;
    let sha256 = format!("{:x}", Sha256::digest(&bytes));
    let report: SparkReport = serde_json::from_slice(&bytes)
        .with_context(|| format!("parse Spark report {}", path.display()))?;
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
    if let RunLimit::Requests(requests) = limit {
        ensure!(
            metrics.requests == u64::try_from(requests)?,
            "incomplete fixed-count report {}: recorded {}, expected {requests}",
            path.display(),
            metrics.requests
        );
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

fn divide(numerator: Option<f64>, denominator: Option<f64>) -> Option<f64> {
    let numerator = numerator?;
    let denominator = denominator?;
    (denominator > 0.0)
        .then(|| numerator / denominator)
        .filter(|value| value.is_finite())
}

fn throughput_ratio(baseline: &Row, other: &Row) -> Option<f64> {
    if baseline.metrics.successful == 0 || other.metrics.successful == 0 {
        return None;
    }
    divide(baseline.successful_rps(), other.successful_rps())
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct Comparison<'a> {
    pair_id: &'a str,
    case: &'a str,
    #[serde(rename = "initialRate")]
    rate: usize,
    rate_steps: &'a [RateStep],
    workers: usize,
    baseline_source: &'a Path,
    other_algorithm: Algorithm,
    other_source: &'a Path,
    successful_throughput_ratio: Option<f64>,
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
    #[serde(rename = "initialRate")]
    rate: usize,
    rate_steps: &'a [RateStep],
    workers: usize,
    other_algorithm: Algorithm,
    pairs: usize,
    successful_throughput_ratio: RatioSummary,
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
    baseline: Algorithm,
    rows: Vec<RenderedRow<'a>>,
    comparisons: Vec<Comparison<'a>>,
    aggregates: Vec<Aggregate<'a>>,
    unpaired: Vec<&'a str>,
}

fn summarize(rows: &[Row]) -> Summary<'_> {
    let mut pairs: BTreeMap<&str, BTreeMap<Algorithm, &Row>> = BTreeMap::new();
    for row in rows {
        pairs
            .entry(&row.pair_id)
            .or_default()
            .insert(row.algorithm, row);
    }
    let mut comparisons = Vec::new();
    let mut unpaired = Vec::new();
    for (pair_id, members) in pairs {
        let baseline = match members.get(&BASELINE) {
            Some(baseline) if members.len() > 1 => *baseline,
            _ => {
                unpaired.push(pair_id);
                continue;
            }
        };
        for (algorithm, other) in members
            .iter()
            .filter(|(algorithm, _)| **algorithm != BASELINE)
        {
            comparisons.push(Comparison {
                pair_id,
                case: &baseline.case,
                rate: baseline.rate,
                rate_steps: &baseline.rate_steps,
                workers: baseline.workers,
                baseline_source: &baseline.source,
                other_algorithm: *algorithm,
                other_source: &other.source,
                successful_throughput_ratio: throughput_ratio(baseline, other),
                ttft_p99_ratio: divide(
                    baseline.metrics.ttft_ms.as_ref().map(|value| value.p99),
                    other.metrics.ttft_ms.as_ref().map(|value| value.p99),
                ),
                latency_p99_ratio: divide(
                    baseline.metrics.latency_ms.as_ref().map(|value| value.p99),
                    other.metrics.latency_ms.as_ref().map(|value| value.p99),
                ),
            });
        }
    }
    let mut groups = BTreeMap::<_, Vec<&Comparison>>::new();
    for comparison in &comparisons {
        groups
            .entry((
                comparison.case,
                comparison.rate,
                comparison.workers,
                comparison.other_algorithm,
            ))
            .or_default()
            .push(comparison);
    }
    let aggregates = groups
        .into_values()
        .map(|members| Aggregate {
            case: members[0].case,
            rate: members[0].rate,
            rate_steps: members[0].rate_steps,
            workers: members[0].workers,
            other_algorithm: members[0].other_algorithm,
            pairs: members.len(),
            successful_throughput_ratio: RatioSummary::new(
                members.iter().map(|pair| pair.successful_throughput_ratio),
            ),
            ttft_p99_ratio: RatioSummary::new(members.iter().map(|pair| pair.ttft_p99_ratio)),
            latency_p99_ratio: RatioSummary::new(members.iter().map(|pair| pair.latency_p99_ratio)),
        })
        .collect();
    Summary {
        definitions: DEFINITIONS,
        baseline: BASELINE,
        rows: rows
            .iter()
            .map(|row| RenderedRow {
                row,
                successful_rps: row.successful_rps(),
                cache_hit_pct: row.metrics.cache_hit_pct(),
            })
            .collect(),
        comparisons,
        aggregates,
        unpaired,
    }
}

pub fn render(output: &Path, rows: &[Row]) -> Result<()> {
    let summary = summarize(rows);
    let mut text = Vec::new();
    write_text(&mut text, &summary)?;
    atomic_json(&output.join("summary.json"), &summary)?;
    let path = output.join("summary.txt");
    fs::write(&path, text).with_context(|| format!("write derived report {}", path.display()))
}

fn value_text(value: Option<f64>) -> String {
    value
        .map(|value| format!("{value:.3}"))
        .unwrap_or_else(|| "unknown".into())
}

fn write_text(out: &mut impl Write, summary: &Summary<'_>) -> Result<()> {
    writeln!(out, "Spark benchmark results\n")?;
    for rendered in &summary.rows {
        let row = rendered.row;
        writeln!(
            out,
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
        let load = if aggregate.rate_steps.is_empty() {
            format!("{} RPS", aggregate.rate)
        } else {
            aggregate
                .rate_steps
                .iter()
                .map(|step| format!("{} RPS for {} s", step.rate, step.duration_seconds))
                .collect::<Vec<_>>()
                .join(" -> ")
        };
        writeln!(
            out,
            "\n{} at {} / {} workers, {} vs {}: {} matched pairs",
            aggregate.case,
            load,
            aggregate.workers,
            summary.baseline,
            aggregate.other_algorithm,
            aggregate.pairs
        )?;
        for (name, ratio) in [
            (
                "Successful throughput",
                &aggregate.successful_throughput_ratio,
            ),
            ("TTFT p99", &aggregate.ttft_p99_ratio),
            ("End-to-end p99", &aggregate.latency_p99_ratio),
        ] {
            match (ratio.median, ratio.observed_range) {
                (Some(median), Some([low, high])) => writeln!(
                    out,
                    "  {name}: median paired change {:+.2}%; observed range {:+.2}% to {:+.2}% ({} defined ratios).",
                    (median - 1.0) * 100.0,
                    (low - 1.0) * 100.0,
                    (high - 1.0) * 100.0,
                    ratio.observed_pairs
                )?,
                _ => writeln!(out, "  {name}: unknown; no defined paired ratios.")?,
            }
        }
    }
    if !summary.unpaired.is_empty() {
        writeln!(
            out,
            "\nNo {} comparison for: {}",
            summary.baseline,
            summary.unpaired.join(", ")
        )?;
    }
    writeln!(out, "\n{}", summary.definitions)?;
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
            rate_steps: Vec::new(),
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
        let verified = inspect(&path, RunLimit::Requests(10), false)?;
        assert_eq!(verified.sha256, expected);
        assert_eq!(verified.metrics.cache_hit_pct(), Some(25.0));
        assert_eq!(verified.metrics.cached_tokens, 17);
        fs::write(&path, format!("{bytes}\n"))?;
        assert_ne!(
            inspect(&path, RunLimit::Requests(10), false)?.sha256,
            expected
        );
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
            assert_eq!(metrics.successful, 0);
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
    fn paired_ratios_use_observed_repeat_ranges_and_undefined_arms() {
        let rows = [
            row("one", Algorithm::WaitAndWiden, 10, 1000.0),
            row("one", Algorithm::PowerOfN, 10, 2000.0),
            row("two", Algorithm::WaitAndWiden, 10, 2000.0),
            row("two", Algorithm::PowerOfN, 10, 1000.0),
            row("other-zero", Algorithm::WaitAndWiden, 10, 1000.0),
            row("other-zero", Algorithm::PowerOfN, 0, 1000.0),
            row("baseline-zero", Algorithm::WaitAndWiden, 0, 1000.0),
            row("baseline-zero", Algorithm::PowerOfN, 10, 1000.0),
        ];
        let summary = summarize(&rows);
        assert_eq!(summary.aggregates.len(), 1);
        let ratios = &summary.aggregates[0].successful_throughput_ratio;
        assert_eq!(summary.aggregates[0].pairs, 4);
        assert_eq!(ratios.observed_pairs, 2);
        assert_eq!(ratios.median, Some(1.25));
        assert_eq!(ratios.observed_range, Some([0.5, 2.0]));
        for pair in ["other-zero", "baseline-zero"] {
            let comparison = summary
                .comparisons
                .iter()
                .find(|comparison| comparison.pair_id == pair)
                .unwrap();
            assert_eq!(comparison.successful_throughput_ratio, None, "{pair}");
            assert_eq!(comparison.ttft_p99_ratio, None, "{pair}");
        }
    }

    #[test]
    fn every_other_algorithm_is_compared_with_the_baseline() {
        let rows = [
            row("all", Algorithm::WaitAndWiden, 10, 1000.0),
            row("all", Algorithm::PowerOfN, 10, 2000.0),
            row("all", Algorithm::PulsarWaitAndWiden, 10, 4000.0),
            row("no-baseline", Algorithm::PowerOfN, 10, 1000.0),
            row("no-baseline", Algorithm::PulsarWaitAndWiden, 10, 1000.0),
            row("baseline-only", Algorithm::WaitAndWiden, 10, 1000.0),
        ];
        let summary = summarize(&rows);
        let compared: Vec<_> = summary
            .comparisons
            .iter()
            .map(|comparison| {
                (
                    comparison.other_algorithm,
                    comparison.successful_throughput_ratio,
                )
            })
            .collect();
        assert_eq!(
            compared,
            [
                (Algorithm::PowerOfN, Some(2.0)),
                (Algorithm::PulsarWaitAndWiden, Some(4.0))
            ]
        );
        assert_eq!(summary.aggregates.len(), 2);
        assert_eq!(summary.unpaired, ["baseline-only", "no-baseline"]);
    }

    #[test]
    fn rate_schedule_throughput_excludes_the_drain() -> Result<()> {
        let mut scheduled = row("ramp", Algorithm::WaitAndWiden, 10, 15000.0);
        scheduled.rate_steps = vec![RateStep {
            rate: 8.try_into()?,
            duration_seconds: 10.try_into()?,
        }];
        assert_eq!(scheduled.successful_rps(), Some(1.0));
        let fixed = row("fixed", Algorithm::WaitAndWiden, 10, 15000.0);
        assert_eq!(fixed.successful_rps(), Some(10.0 / 15.0));
        Ok(())
    }

    #[test]
    fn renderer_writes_json_and_prose_summaries() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let rows = [
            row("one", Algorithm::WaitAndWiden, 10, 1000.0),
            row("one", Algorithm::PowerOfN, 10, 2000.0),
        ];
        render(directory.path(), &rows)?;
        render(directory.path(), &rows)?;
        let json: Value =
            serde_json::from_slice(&fs::read(directory.path().join("summary.json"))?)?;
        assert_eq!(json["baseline"], "wait-and-widen");
        assert_eq!(json["comparisons"][0]["otherAlgorithm"], "power-of-n");
        assert_eq!(json["comparisons"][0]["successfulThroughputRatio"], 2.0);
        let text = fs::read_to_string(directory.path().join("summary.txt"))?;
        assert!(text.contains("wait-and-widen vs power-of-n: 1 matched pairs"));
        assert!(text.contains("+100.00%"));
        assert!(text.contains("not confidence intervals"));
        assert!(text.contains("window-end in-flight cancellations"));
        assert_eq!(fs::read_dir(directory.path())?.count(), 2);
        Ok(())
    }
}

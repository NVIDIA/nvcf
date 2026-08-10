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

use std::collections::BTreeSet;
use std::fmt::Write as _;
use std::path::{Path, PathBuf};

use anyhow::Context;

use crate::config::{BenchmarkConfig, PylonQueueAdmissionConfig, ScenarioMetadata};
use crate::manifest::Manifest;
use crate::score::{QueueAdmissionSummary, RunSummary};

#[derive(Debug, Clone)]
pub struct ReportContext {
    pub name: String,
    pub metadata: ScenarioMetadata,
    pub model: String,
    pub request_count: usize,
    pub max_concurrency: usize,
    pub stargate_count: usize,
    pub backend_count: usize,
    pub cluster_count: usize,
    pub pylons_per_cluster: usize,
}

impl ReportContext {
    pub fn from_config(config: &BenchmarkConfig) -> Self {
        Self {
            name: config.name.clone(),
            metadata: config.metadata.clone(),
            model: config.model.clone(),
            request_count: config.request_count,
            max_concurrency: config.max_concurrency,
            stargate_count: config.stargates.count,
            backend_count: config.backends.count,
            cluster_count: config.backends.cluster_count(),
            pylons_per_cluster: config.backends.pylons_per_cluster,
        }
    }

    pub fn from_manifest(manifest: &Manifest) -> Self {
        Self {
            name: manifest.benchmark_name.clone(),
            metadata: manifest.metadata.clone(),
            model: manifest.model.clone(),
            request_count: manifest.request_count,
            max_concurrency: manifest.max_concurrency,
            stargate_count: manifest.stargate_count,
            backend_count: manifest.backend_count,
            cluster_count: match manifest.cluster_count {
                0 => manifest.backend_count,
                count => count,
            },
            pylons_per_cluster: manifest.pylons_per_cluster,
        }
    }
}

#[derive(Debug, Clone)]
pub struct ReportEntry {
    pub algorithm_name: String,
    pub pylon_queue_admission: Option<PylonQueueAdmissionConfig>,
    pub summary: RunSummary,
}

#[derive(Debug, Clone)]
pub struct BenchmarkReportArtifacts {
    pub comparison_path: PathBuf,
    pub report_path: PathBuf,
}

pub fn write_benchmark_report_artifacts(
    output_dir: &Path,
    context: &ReportContext,
    entries: &[ReportEntry],
) -> anyhow::Result<BenchmarkReportArtifacts> {
    let artifacts = BenchmarkReportArtifacts {
        comparison_path: output_dir.join("comparison.json"),
        report_path: output_dir.join("report.md"),
    };
    let comparison = entries.iter().map(comparison_entry).collect::<Vec<_>>();
    let comparison_bytes = serde_json::to_vec_pretty(&comparison)
        .context("failed to serialize benchmark comparison")?;
    std::fs::write(&artifacts.comparison_path, comparison_bytes)
        .with_context(|| format!("failed to write {}", artifacts.comparison_path.display()))?;
    write_markdown_report_artifact(&artifacts.report_path, context, entries)?;
    Ok(artifacts)
}

pub fn write_markdown_report_artifact(
    report_path: &Path,
    context: &ReportContext,
    entries: &[ReportEntry],
) -> anyhow::Result<()> {
    std::fs::write(report_path, render_markdown_report(context, entries))
        .with_context(|| format!("failed to write {}", report_path.display()))
}

pub(crate) fn comparison_entry(entry: &ReportEntry) -> serde_json::Value {
    let summary = &entry.summary;
    serde_json::json!({
        "algorithm_name": entry.algorithm_name,
        "pylon_queue_admission": entry.pylon_queue_admission,
        "success_rate": summary.success_rate,
        "avg_ttft_ms": summary.avg_ttft_ms,
        "p95_ttft_ms": summary.p95_ttft_ms,
        "avg_ttlt_ms": summary.avg_ttlt_ms,
        "max_ttlt_ms": summary.max_ttlt_ms,
        "total_length_ms": summary.total_length_ms,
        "successful_requests_per_second": summary.successful_requests_per_second,
        "successful_output_tokens_per_second": summary.successful_output_tokens_per_second,
        "balance_score": summary.balance_score,
        "capacity_balance_score": summary.capacity_balance_score,
        "cluster_balance_score": summary.cluster_balance_score,
        "cluster_capacity_balance_score": summary.cluster_capacity_balance_score,
        "cache_observed_request_count": summary.cache_summary.observed_request_count,
        "cache_hit_count": summary.cache_summary.hit_count,
        "cache_miss_count": summary.cache_summary.miss_count,
        "cache_hit_rate": summary.cache_summary.hit_rate,
        "cache_eviction_count": summary.cache_summary.eviction_count,
        "cache_evicted_tokens": summary.cache_summary.evicted_tokens,
        "cache_reused_input_tokens": summary.cache_summary.reused_input_tokens,
        "cache_uncached_input_tokens": summary.cache_summary.uncached_input_tokens,
        "cache_input_reuse_rate": summary.cache_summary.input_reuse_rate,
        "cache_key_movement_rate": summary.stickiness_summary.movement_rate,
        "moved_cache_key_count": summary.stickiness_summary.moved_cache_key_count,
        "failure_group_count": summary.failure_summary.len(),
        "queue_admission": summary.queue_admission_summary,
        "routing_selection": summary.routing_selection_summary,
    })
}

pub fn render_markdown_report(context: &ReportContext, entries: &[ReportEntry]) -> String {
    let mut out = String::new();
    render_report_header(&mut out, context);
    render_warnings(&mut out, context, entries);
    render_overview_table(&mut out, entries);
    render_share_table(&mut out, ShareGroupKind::Cluster, entries);
    render_share_table(&mut out, ShareGroupKind::Backend, entries);
    render_failure_table(&mut out, entries);
    out
}

fn render_report_header(out: &mut String, context: &ReportContext) {
    write!(out, "# Benchmark Report: {}\n\n", context.name).unwrap();
    if let Some(description) = &context.metadata.description {
        write!(out, "{description}\n\n").unwrap();
    }
    write!(
        out,
        "- Model: `{}`\n- Requests: `{}`\n- Max concurrency: `{}`\n- Stargates: `{}`\n- Pylons/backends: `{}`\n- Routing clusters: `{}`\n- Pylons per generated cluster: `{}`\n\n",
        context.model,
        context.request_count,
        context.max_concurrency,
        context.stargate_count,
        context.backend_count,
        context.cluster_count,
        context.pylons_per_cluster
    )
    .unwrap();
    let metadata_start = out.len();
    if !context.metadata.tags.is_empty() {
        writeln!(out, "- Tags: `{}`", context.metadata.tags.join("`, `")).unwrap();
    }
    if let Some(expected_runtime) = &context.metadata.expected_runtime {
        writeln!(out, "- Expected runtime: `{expected_runtime}`").unwrap();
    }
    if let Some(expected_signal) = &context.metadata.expected_signal {
        writeln!(out, "- Expected signal: {expected_signal}").unwrap();
    }
    if out.len() != metadata_start {
        out.push('\n');
    }
}

fn render_overview_table(out: &mut String, entries: &[ReportEntry]) {
    out.push_str("| Algorithm | Admission Mode | Success | Successful RPS | Output Goodput | Avg TTFT | P95 TTFT | Avg TTLT | P95 TTLT | Max TTLT | Total Length | Cluster Equal Balance | Cluster Input-Capacity Balance | Pylon Equal Balance | Pylon Capacity Balance | Cache Hits | Cache Hit Rate | Input Reuse Rate | Reused Input | Prefilled Input | Cache Movement | Cache Evictions | Evicted Tokens | Failure Groups | Fallback Route Choices | KV-Free Fallback Choices | Pylon Rejected | Pylon Disabled | Queue Mismatch Retries | Retry Exhausted |\n");
    out.push_str("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|\n");
    for entry in entries {
        let summary = &entry.summary;
        let cache = &summary.cache_summary;
        let queue = &summary.queue_admission_summary;
        let routing = &summary.routing_selection_summary;
        let stickiness = &summary.stickiness_summary;
        out.push_str(&format!(
            "| {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} |\n",
            entry.algorithm_name,
            admission_mode(entry.pylon_queue_admission.as_ref()),
            percent(summary.success_rate),
            optional_rate(summary.successful_requests_per_second, "req/s"),
            optional_rate(summary.successful_output_tokens_per_second, "tok/s"),
            optional_ms(summary.avg_ttft_ms),
            optional_integer_ms(summary.p95_ttft_ms),
            ms_float(summary.avg_ttlt_ms),
            ms(summary.p95_ttlt_ms),
            ms(summary.max_ttlt_ms),
            ms(summary.total_length_ms),
            optional_score(summary.cluster_balance_score),
            optional_score(summary.cluster_capacity_balance_score),
            optional_score(summary.balance_score),
            optional_score(summary.capacity_balance_score),
            cache_hits(cache.hit_count, cache.miss_count),
            optional_percent(cache.hit_rate),
            optional_percent(cache.input_reuse_rate),
            cache.reused_input_tokens,
            cache.uncached_input_tokens,
            optional_percent(stickiness.movement_rate),
            cache.eviction_count,
            cache.evicted_tokens,
            summary.failure_summary.len(),
            counter(routing.fallback_count),
            counter(routing.kv_free_token_fallback_count),
            counter(queue.pylon_rejected_count),
            counter(queue.pylon_disabled_count),
            counter(queue.stargate_queue_mismatch_retry_count),
            retry_exhaustion(queue),
        ));
    }
}

#[derive(Clone, Copy)]
enum ShareGroupKind {
    Cluster = 0,
    Backend = 1,
}

fn render_share_table(out: &mut String, group_kind: ShareGroupKind, entries: &[ReportEntry]) {
    let (section_title, id_header) =
        [("Cluster Shares", "Cluster"), ("Backend Shares", "Backend")][group_kind as usize];
    out.push_str(&format!("\n## {section_title}\n\n"));
    for entry in entries {
        let summary = &entry.summary;
        let (request_shares, input_shares, output_shares, capacity_shares, summaries) =
            match group_kind {
                ShareGroupKind::Cluster => (
                    &summary.cluster_request_shares,
                    &summary.cluster_input_token_shares,
                    &summary.cluster_output_token_shares,
                    &summary.cluster_capacity_shares,
                    &summary.cluster_summaries,
                ),
                ShareGroupKind::Backend => (
                    &summary.backend_request_shares,
                    &summary.backend_input_token_shares,
                    &summary.backend_output_token_shares,
                    &summary.backend_capacity_shares,
                    &summary.backend_summaries,
                ),
            };
        out.push_str(&format!("### {}\n\n", entry.algorithm_name));
        out.push_str(&format!(
            "| {} | Requests | Success | Request Share | Input Share | Output Share | Capacity Share | Avg TTLT | P95 TTLT | Cache Hit Rate | Evictions |\n",
            id_header
        ));
        out.push_str("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n");
        let member_ids = request_shares
            .keys()
            .chain(capacity_shares.keys())
            .chain(summaries.keys())
            .map(String::as_str)
            .collect::<BTreeSet<_>>();
        for member_id in member_ids {
            let member_summary = summaries.get(member_id);
            out.push_str(&format!(
                "| {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} |\n",
                member_id,
                optional_display(member_summary.map(|summary| summary.request_count)),
                optional_display(member_summary.map(|summary| summary.success_count)),
                optional_percent(request_shares.get(member_id).copied()),
                optional_percent(input_shares.get(member_id).copied()),
                optional_percent(output_shares.get(member_id).copied()),
                optional_percent(capacity_shares.get(member_id).copied()),
                optional_ms(member_summary.and_then(|summary| summary.avg_ttlt_ms)),
                optional_integer_ms(member_summary.and_then(|summary| summary.p95_ttlt_ms)),
                optional_percent(member_summary.and_then(|summary| summary.cache_hit_rate)),
                optional_display(member_summary.map(|summary| summary.cache_eviction_count)),
            ));
        }
        out.push('\n');
    }
}

fn render_failure_table(out: &mut String, entries: &[ReportEntry]) {
    if entries
        .iter()
        .all(|entry| entry.summary.failure_summary.is_empty())
    {
        return;
    }
    out.push_str("## Failures\n\n");
    out.push_str("| Algorithm | Status | Backend | Count | Error |\n");
    out.push_str("|---|---:|---|---:|---|\n");
    for entry in entries {
        for failure in &entry.summary.failure_summary {
            writeln!(
                out,
                "| {} | {} | {} | {} | {} |",
                entry.algorithm_name,
                failure.status_code,
                failure.selected_backend_id.as_deref().unwrap_or("-"),
                failure.count,
                failure.error.as_deref().unwrap_or("-"),
            )
            .unwrap();
        }
    }
    out.push('\n');
}

fn ms(value: u64) -> String {
    format!("{value} ms")
}

fn ms_float(value: f64) -> String {
    format!("{value:.1} ms")
}

fn optional_ms(value: Option<f64>) -> String {
    optional(value, ms_float)
}

fn optional_integer_ms(value: Option<u64>) -> String {
    optional(value, ms)
}

fn percent(value: f64) -> String {
    format!("{:.1}%", value * 100.0)
}

fn optional_percent(value: Option<f64>) -> String {
    optional(value, percent)
}

fn optional_score(value: Option<f64>) -> String {
    optional(value, |value| format!("{value:.3}"))
}

fn optional_rate(value: Option<f64>, unit: &str) -> String {
    optional(value, |value| format!("{value:.1} {unit}"))
}

fn optional<T>(value: Option<T>, render: impl FnOnce(T) -> String) -> String {
    value.map(render).unwrap_or_else(|| "-".to_string())
}

fn optional_display(value: Option<impl std::fmt::Display>) -> String {
    optional(value, |value| value.to_string())
}

fn cache_hits(hit_count: usize, miss_count: usize) -> String {
    if hit_count == 0 && miss_count == 0 {
        "-".to_string()
    } else {
        format!("{hit_count}/{miss_count}")
    }
}

fn admission_mode(config: Option<&PylonQueueAdmissionConfig>) -> String {
    let Some(config) = config else {
        return "runtime default".to_string();
    };
    let mode = if config.enabled {
        "enabled"
    } else {
        "disabled"
    };
    let mut details = Vec::new();
    if let Some(min_delta_ms) = config.min_delta_ms {
        details.push(format!("min={min_delta_ms}ms"));
    }
    if let Some(tolerance_factor) = config.tolerance_factor {
        details.push(format!("factor={}", counter(tolerance_factor)));
    }
    if let Some(retry_after_ms) = config.retry_after_ms {
        details.push(format!("retry-after={retry_after_ms}ms"));
    }
    if details.is_empty() {
        mode.to_string()
    } else {
        format!("{mode} ({})", details.join(", "))
    }
}

fn counter(value: f64) -> String {
    if value.fract() == 0.0 {
        format!("{value:.0}")
    } else {
        format!("{value:.3}")
    }
}

fn retry_exhaustion(summary: &QueueAdmissionSummary) -> String {
    let total = counter(summary.stargate_retry_exhausted_count);
    if summary.stargate_retry_exhausted_by_reason.is_empty() {
        return total;
    }
    let reasons = summary
        .stargate_retry_exhausted_by_reason
        .iter()
        .map(|(reason, count)| format!("{reason}={}", counter(*count)))
        .collect::<Vec<_>>()
        .join(", ");
    format!("{total} ({reasons})")
}

fn render_warnings(out: &mut String, context: &ReportContext, entries: &[ReportEntry]) {
    let start = out.len();
    out.push_str("## Warnings\n\n");
    if entries.is_empty() {
        out.push_str("- No algorithm summaries were found.\n\n");
        return;
    }
    let has_tag = |targets: &[&str]| {
        context
            .metadata
            .tags
            .iter()
            .any(|tag| targets.contains(&tag.as_str()))
    };
    let cache_focused = has_tag(&["cache", "pulsar", "kv-cache"]);
    let queue_admission_focused = has_tag(&["queue-admission", "queue-mismatch"]);
    let pmr_fallback_focused = has_tag(&["pmr-fallback"]);
    for entry in entries {
        let summary = &entry.summary;
        if summary.success_rate < 1.0 {
            writeln!(
                out,
                "- {} success rate was {:.1}%.",
                entry.algorithm_name,
                summary.success_rate * 100.0
            )
            .unwrap();
        }
        if let Some(score) = summary.capacity_balance_score
            && score < 0.5
        {
            writeln!(
                out,
                "- {} capacity balance score was low ({score:.3}).",
                entry.algorithm_name
            )
            .unwrap();
        }
        if cache_focused {
            if summary.cache_summary.observed_request_count == 0 {
                writeln!(
                    out,
                    "- {} did not report per-request KV-cache headers.",
                    entry.algorithm_name
                )
                .unwrap();
            } else if summary.cache_summary.hit_count == 0 {
                writeln!(
                    out,
                    "- {} reported KV-cache headers but no cache hits.",
                    entry.algorithm_name
                )
                .unwrap();
            }
        }
        if pmr_fallback_focused
            && entry.algorithm_name == "pulsar-multiregion"
            && summary.routing_selection_summary.fallback_count == 0.0
        {
            out.push_str("- pulsar-multiregion did not observe a ranked fallback route choice in the PMR fallback scenario.\n");
        }
    }
    if queue_admission_focused
        && entries.iter().all(|entry| {
            let queue = &entry.summary.queue_admission_summary;
            queue.pylon_rejected_count == 0.0 && queue.stargate_queue_mismatch_retry_count == 0.0
        })
    {
        out.push_str("- No pylon queue-mismatch rejections or Stargate queue-mismatch retries were observed.\n");
    }
    if out.len() == start + "## Warnings\n\n".len() {
        out.truncate(start);
    } else {
        out.push('\n');
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{
        ArrivalPatternConfig, BackendConfig, BackendProfile, RegistrationConfig, ServiceTimeConfig,
        StargateConfig, TokenDistributionConfig, TrafficPatternConfig, UniformTrafficConfig,
    };
    use crate::score::{
        BackendSummary, CacheSummary, FailureSummary, QueueAdmissionSummary,
        RoutingSelectionSummary, summarize_with_capacity,
    };
    use std::collections::BTreeMap;

    fn config() -> BenchmarkConfig {
        BenchmarkConfig {
            name: "report-test".to_string(),
            metadata: ScenarioMetadata::default(),
            model: "dummy-model".to_string(),
            seed: Some(1),
            request_count: 1,
            max_concurrency: 1,
            tunnel_protocol: stargate_protocol::TunnelTransportProtocol::RawQuic,
            stargates: StargateConfig { count: 1 },
            backends: BackendConfig {
                count: 1,
                cluster_id_template: None,
                pylons_per_cluster: 1,
                profiles: Vec::new(),
                profile: BackendProfile {
                    name: "default".to_string(),
                    weight: 1.0,
                    max_concurrent_requests: None,
                    kv_cache_capacity_tokens: 0,
                    service_time_ms: ServiceTimeConfig {
                        ttft_mean: 1,
                        ttft_jitter_ms: 0,
                        decode_tokens_per_s: 1,
                        decode_jitter_ms: 0,
                        prefill_tokens_per_s: None,
                    },
                    registration: RegistrationConfig {
                        last_mean_input_tps: 1.0,
                    },
                },
            },
            traffic_pattern: TrafficPatternConfig::Uniform(UniformTrafficConfig {
                routing_keys: 0,
                cache_affinity_keys: 0,
                input_tokens: TokenDistributionConfig::Constant { value: 1 },
                output_tokens: TokenDistributionConfig::Constant { value: 1 },
                arrival: ArrivalPatternConfig::Constant { interval_ms: 1 },
            }),
            degradation: Default::default(),
            algorithms: Vec::new(),
        }
    }

    fn render(
        config: &BenchmarkConfig,
        algorithm_name: &str,
        pylon_queue_admission: Option<PylonQueueAdmissionConfig>,
        summary: RunSummary,
    ) -> String {
        render_markdown_report(
            &ReportContext::from_config(config),
            &[ReportEntry {
                algorithm_name: algorithm_name.to_string(),
                pylon_queue_admission,
                summary,
            }],
        )
    }

    fn queue_admission() -> PylonQueueAdmissionConfig {
        PylonQueueAdmissionConfig {
            enabled: true,
            min_delta_ms: Some(0),
            tolerance_factor: Some(1.0),
            retry_after_ms: Some(5),
        }
    }

    #[test]
    fn report_artifacts_write_comparison_and_markdown_from_same_entries() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let mut summary = summarize_with_capacity(&[], BTreeMap::new());
        summary.p95_ttft_ms = Some(17);
        summary.queue_admission_summary = QueueAdmissionSummary {
            pylon_rejected_count: 4.0,
            stargate_queue_mismatch_retry_count: 3.0,
            ..QueueAdmissionSummary::default()
        };
        summary.routing_selection_summary = RoutingSelectionSummary {
            primary_count: 5.0,
            fallback_count: 2.0,
            kv_free_token_fallback_count: 1.0,
        };
        let entry = ReportEntry {
            algorithm_name: "groq-admission-enabled".to_string(),
            pylon_queue_admission: Some(queue_admission()),
            summary,
        };

        let artifacts = write_benchmark_report_artifacts(
            tempdir.path(),
            &ReportContext::from_config(&config()),
            &[entry],
        )
        .expect("report artifacts should write");

        let comparison: serde_json::Value = serde_json::from_slice(
            &std::fs::read(&artifacts.comparison_path).expect("comparison should read"),
        )
        .expect("comparison should parse");
        assert_eq!(comparison[0]["algorithm_name"], "groq-admission-enabled");
        assert_eq!(comparison[0]["pylon_queue_admission"]["enabled"], true);
        assert_eq!(comparison[0]["p95_ttft_ms"], 17);
        assert_eq!(
            comparison[0]["queue_admission"]["pylon_rejected_count"],
            4.0
        );
        assert_eq!(
            comparison[0]["queue_admission"]["stargate_queue_mismatch_retry_count"],
            3.0
        );
        assert_eq!(comparison[0]["routing_selection"]["fallback_count"], 2.0);
        assert_eq!(
            comparison[0]["routing_selection"]["kv_free_token_fallback_count"],
            1.0
        );

        let report = std::fs::read_to_string(&artifacts.report_path).expect("report should read");
        assert!(report.contains("| groq-admission-enabled | enabled"));
        assert!(report.contains("Pylon Rejected"));
        assert!(report.contains("Fallback Route Choices"));
    }

    #[test]
    fn markdown_report_includes_key_columns() {
        let mut summary = summarize_with_capacity(&[], BTreeMap::new());
        summary.request_count = 1;
        summary.success_rate = 1.0;
        summary.successful_requests_per_second = Some(40.0);
        summary.successful_output_tokens_per_second = Some(400.0);
        summary.avg_ttft_ms = Some(10.0);
        summary.p95_ttft_ms = Some(10);
        summary.avg_ttlt_ms = 20.0;
        summary.p95_ttlt_ms = 20;
        summary.max_ttlt_ms = 20;
        summary.total_length_ms = 25;
        summary.balance_score = Some(1.0);
        summary.capacity_balance_score = Some(1.0);
        summary.cluster_balance_score = Some(1.0);
        summary.cluster_capacity_balance_score = Some(1.0);
        summary.backend_request_shares = BTreeMap::from([("backend-0".to_string(), 1.0)]);
        summary.backend_capacity_shares = summary.backend_request_shares.clone();
        summary.cluster_request_shares = BTreeMap::from([("cluster-a".to_string(), 1.0)]);
        summary.cluster_capacity_shares = summary.cluster_request_shares.clone();
        summary.cluster_summaries = BTreeMap::from([(
            "cluster-a".to_string(),
            BackendSummary {
                request_count: 1,
                success_count: 1,
                input_tokens: 1,
                output_tokens: 10,
                avg_ttlt_ms: Some(20.0),
                p95_ttlt_ms: Some(20),
                cache_hit_rate: Some(1.0),
                cache_eviction_count: 0,
                cache_evicted_tokens: 0,
            },
        )]);
        summary.cache_summary = CacheSummary {
            observed_request_count: 1,
            hit_count: 1,
            hit_rate: Some(1.0),
            reused_input_tokens: 10,
            uncached_input_tokens: 1,
            input_reuse_rate: Some(10.0 / 11.0),
            ..CacheSummary::default()
        };

        let report = render(&config(), "power-of-two", None, summary);

        assert!(report.contains("| Algorithm | Admission Mode | Success |"));
        assert!(report.contains("Successful RPS"));
        assert!(report.contains("Output Goodput"));
        assert!(report.contains("P95 TTFT"));
        assert!(report.contains("| 400.0 tok/s | 10.0 ms | 10 ms | 20.0 ms |"));
        assert!(report.contains("Cluster Input-Capacity Balance"));
        assert!(report.contains("Cache Hit Rate"));
        assert!(report.contains("Input Reuse Rate"));
        assert!(report.contains("## Cluster Shares"));
        assert!(report.contains("cluster-a"));
        assert!(report.contains("backend-0"));
    }

    #[test]
    fn markdown_report_warns_for_cache_scenarios_without_cache_headers() {
        let mut config = config();
        config.metadata.tags = vec!["cache".to_string()];
        let mut summary = summarize_with_capacity(&[], BTreeMap::new());
        summary.success_rate = 1.0;

        let report = render(&config, "pulsar", None, summary);

        assert!(report.contains("## Warnings"));
        assert!(report.contains("did not report per-request KV-cache headers"));
    }

    #[test]
    fn markdown_report_labels_admission_variant_and_proof_counters() {
        let mut summary = summarize_with_capacity(&[], BTreeMap::new());
        summary.queue_admission_summary = QueueAdmissionSummary {
            pylon_rejected_count: 3.0,
            pylon_disabled_count: 0.0,
            stargate_queue_mismatch_retry_count: 2.0,
            stargate_retry_exhausted_count: 1.0,
            stargate_retry_exhausted_by_reason: BTreeMap::from([(
                "retry_budget_exhausted".to_string(),
                1.0,
            )]),
            ..QueueAdmissionSummary::default()
        };
        summary.routing_selection_summary = RoutingSelectionSummary {
            primary_count: 6.0,
            fallback_count: 2.0,
            kv_free_token_fallback_count: 1.0,
        };

        let report = render(
            &config(),
            "groq-admission-enabled",
            Some(queue_admission()),
            summary,
        );

        assert!(report.contains("Admission Mode"));
        assert!(report.contains("enabled (min=0ms, factor=1, retry-after=5ms)"));
        assert!(report.contains("Pylon Rejected"));
        assert!(report.contains("Fallback Route Choices"));
        assert!(report.contains("| groq-admission-enabled | enabled"));
        assert!(report.contains("| 2 | 1 | 3 | 0 | 2 | 1 (retry_budget_exhausted=1) |"));
    }

    #[test]
    fn markdown_report_renders_failure_rows_only_when_failures_exist() {
        let successful_summary = summarize_with_capacity(&[], BTreeMap::new());
        let mut failed_summary = summarize_with_capacity(&[], BTreeMap::new());
        failed_summary.failure_summary = vec![FailureSummary {
            status_code: 503,
            selected_backend_id: Some("backend-a".to_string()),
            error: Some("upstream unavailable".to_string()),
            count: 2,
        }];

        let successful_report = render(&config(), "round-robin", None, successful_summary);
        let failed_report = render(&config(), "power-of-two", None, failed_summary);

        assert!(!successful_report.contains("## Failures"));
        assert!(failed_report.contains("## Failures"));
        assert!(failed_report.contains("| Algorithm | Status | Backend | Count | Error |"));
        assert!(
            failed_report.contains("| power-of-two | 503 | backend-a | 2 | upstream unavailable |")
        );
    }

    #[test]
    fn markdown_report_warns_when_a_pmr_fallback_scenario_never_uses_fallback() {
        let mut config = config();
        config.metadata.tags = vec!["pmr-fallback".to_string()];

        let report = render(
            &config,
            "pulsar-multiregion",
            None,
            summarize_with_capacity(&[], BTreeMap::new()),
        );

        assert!(
            report.contains("pulsar-multiregion did not observe a ranked fallback route choice")
        );
    }
}

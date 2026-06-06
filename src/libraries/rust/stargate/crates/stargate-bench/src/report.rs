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

use crate::config::{BenchmarkConfig, PylonQueueAdmissionConfig, ScenarioMetadata};
use crate::manifest::Manifest;
use crate::score::{BackendSummary, QueueAdmissionSummary, RunSummary};
use std::collections::BTreeSet;

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
            cluster_count: manifest_cluster_count(manifest),
            pylons_per_cluster: manifest.pylons_per_cluster,
        }
    }
}

fn manifest_cluster_count(manifest: &Manifest) -> usize {
    if manifest.cluster_count == 0 {
        manifest.backend_count
    } else {
        manifest.cluster_count
    }
}

#[derive(Debug, Clone)]
pub struct ReportEntry {
    pub algorithm_name: String,
    pub pylon_queue_admission: Option<PylonQueueAdmissionConfig>,
    pub summary: RunSummary,
}

pub fn render_markdown_report(context: &ReportContext, entries: &[ReportEntry]) -> String {
    let mut out = String::new();
    render_report_header(&mut out, context);
    render_warning_section(&mut out, &report_warnings(context, entries));
    render_overview_table(&mut out, entries);
    render_share_table(&mut out, ShareGroupKind::Cluster, entries);
    render_share_table(&mut out, ShareGroupKind::Backend, entries);
    render_failure_table(&mut out, entries);
    out
}

fn render_report_header(out: &mut String, context: &ReportContext) {
    out.push_str(&format!("# Benchmark Report: {}\n\n", context.name));
    if let Some(description) = &context.metadata.description {
        out.push_str(description);
        out.push_str("\n\n");
    }
    out.push_str(&format!("- Model: `{}`\n", context.model));
    out.push_str(&format!("- Requests: `{}`\n", context.request_count));
    out.push_str(&format!(
        "- Max concurrency: `{}`\n",
        context.max_concurrency
    ));
    out.push_str(&format!("- Stargates: `{}`\n", context.stargate_count));
    out.push_str(&format!("- Pylons/backends: `{}`\n", context.backend_count));
    out.push_str(&format!(
        "- Routing clusters: `{}`\n",
        context.cluster_count
    ));
    out.push_str(&format!(
        "- Pylons per generated cluster: `{}`\n\n",
        context.pylons_per_cluster
    ));
    if !context.metadata.tags.is_empty() {
        out.push_str(&format!(
            "- Tags: `{}`\n",
            context.metadata.tags.join("`, `")
        ));
    }
    if let Some(expected_runtime) = &context.metadata.expected_runtime {
        out.push_str(&format!("- Expected runtime: `{expected_runtime}`\n"));
    }
    if let Some(expected_signal) = &context.metadata.expected_signal {
        out.push_str(&format!("- Expected signal: {expected_signal}\n"));
    }
    if !context.metadata.tags.is_empty()
        || context.metadata.expected_runtime.is_some()
        || context.metadata.expected_signal.is_some()
    {
        out.push('\n');
    }
}

fn render_warning_section(out: &mut String, warnings: &[String]) {
    if !warnings.is_empty() {
        out.push_str("## Warnings\n\n");
        for warning in warnings {
            out.push_str(&format!("- {warning}\n"));
        }
        out.push('\n');
    }
}

fn render_overview_table(out: &mut String, entries: &[ReportEntry]) {
    out.push_str("| Algorithm | Admission Mode | Success | Successful RPS | Output Goodput | Avg TTFT | P95 TTFT | Avg TTLT | P95 TTLT | Max TTLT | Total Length | Cluster Equal Balance | Cluster Input-Capacity Balance | Pylon Equal Balance | Pylon Capacity Balance | Cache Hits | Cache Hit Rate | Input Reuse Rate | Reused Input | Prefilled Input | Cache Movement | Cache Evictions | Evicted Tokens | Failure Groups | Fallback Route Choices | KV-Free Fallback Choices | Pylon Rejected | Pylon Disabled | Queue Mismatch Retries | Retry Exhausted |\n");
    out.push_str("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|\n");
    for entry in entries {
        let summary = &entry.summary;
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
            cache_hits(
                summary.cache_summary.hit_count,
                summary.cache_summary.miss_count
            ),
            optional_percent(summary.cache_summary.hit_rate),
            optional_percent(summary.cache_summary.input_reuse_rate),
            summary.cache_summary.reused_input_tokens,
            summary.cache_summary.uncached_input_tokens,
            optional_percent(summary.stickiness_summary.movement_rate),
            summary.cache_summary.eviction_count,
            summary.cache_summary.evicted_tokens,
            summary.failure_summary.len(),
            counter(summary.routing_selection_summary.fallback_count),
            counter(
                summary
                    .routing_selection_summary
                    .kv_free_token_fallback_count
            ),
            counter(summary.queue_admission_summary.pylon_rejected_count),
            counter(summary.queue_admission_summary.pylon_disabled_count),
            counter(
                summary
                    .queue_admission_summary
                    .stargate_queue_mismatch_retry_count
            ),
            retry_exhaustion(&summary.queue_admission_summary),
        ));
    }
}

#[derive(Clone, Copy)]
enum ShareGroupKind {
    Cluster,
    Backend,
}

impl ShareGroupKind {
    fn section_title(self) -> &'static str {
        match self {
            Self::Cluster => "Cluster Shares",
            Self::Backend => "Backend Shares",
        }
    }

    fn id_header(self) -> &'static str {
        match self {
            Self::Cluster => "Cluster",
            Self::Backend => "Backend",
        }
    }

    fn ids(self, summary: &RunSummary) -> BTreeSet<&str> {
        let mut ids = BTreeSet::new();
        match self {
            Self::Cluster => {
                ids.extend(summary.cluster_request_shares.keys().map(String::as_str));
                ids.extend(summary.cluster_capacity_shares.keys().map(String::as_str));
                ids.extend(summary.cluster_summaries.keys().map(String::as_str));
            }
            Self::Backend => {
                ids.extend(summary.backend_request_shares.keys().map(String::as_str));
                ids.extend(summary.backend_capacity_shares.keys().map(String::as_str));
                ids.extend(summary.backend_summaries.keys().map(String::as_str));
            }
        }
        ids
    }

    fn member_summary<'a>(self, summary: &'a RunSummary, id: &str) -> Option<&'a BackendSummary> {
        match self {
            Self::Cluster => summary.cluster_summaries.get(id),
            Self::Backend => summary.backend_summaries.get(id),
        }
    }

    fn request_share(self, summary: &RunSummary, id: &str) -> Option<f64> {
        match self {
            Self::Cluster => summary.cluster_request_shares.get(id).copied(),
            Self::Backend => summary.backend_request_shares.get(id).copied(),
        }
    }

    fn input_share(self, summary: &RunSummary, id: &str) -> Option<f64> {
        match self {
            Self::Cluster => summary.cluster_input_token_shares.get(id).copied(),
            Self::Backend => summary.backend_input_token_shares.get(id).copied(),
        }
    }

    fn output_share(self, summary: &RunSummary, id: &str) -> Option<f64> {
        match self {
            Self::Cluster => summary.cluster_output_token_shares.get(id).copied(),
            Self::Backend => summary.backend_output_token_shares.get(id).copied(),
        }
    }

    fn capacity_share(self, summary: &RunSummary, id: &str) -> Option<f64> {
        match self {
            Self::Cluster => summary.cluster_capacity_shares.get(id).copied(),
            Self::Backend => summary.backend_capacity_shares.get(id).copied(),
        }
    }
}

fn render_share_table(out: &mut String, group_kind: ShareGroupKind, entries: &[ReportEntry]) {
    out.push_str(&format!("\n## {}\n\n", group_kind.section_title()));
    for entry in entries {
        out.push_str(&format!("### {}\n\n", entry.algorithm_name));
        out.push_str(&format!(
            "| {} | Requests | Success | Request Share | Input Share | Output Share | Capacity Share | Avg TTLT | P95 TTLT | Cache Hit Rate | Evictions |\n",
            group_kind.id_header()
        ));
        out.push_str("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n");
        for member_id in group_kind.ids(&entry.summary) {
            let member_summary = group_kind.member_summary(&entry.summary, member_id);
            out.push_str(&format!(
                "| {} | {} | {} | {} | {} | {} | {} | {} | {} | {} | {} |\n",
                member_id,
                member_summary
                    .map(|summary| summary.request_count.to_string())
                    .unwrap_or_else(|| "-".to_string()),
                member_summary
                    .map(|summary| summary.success_count.to_string())
                    .unwrap_or_else(|| "-".to_string()),
                optional_percent(group_kind.request_share(&entry.summary, member_id)),
                optional_percent(group_kind.input_share(&entry.summary, member_id)),
                optional_percent(group_kind.output_share(&entry.summary, member_id)),
                optional_percent(group_kind.capacity_share(&entry.summary, member_id)),
                member_summary
                    .and_then(|summary| summary.avg_ttlt_ms)
                    .map(ms_float)
                    .unwrap_or_else(|| "-".to_string()),
                member_summary
                    .and_then(|summary| summary.p95_ttlt_ms)
                    .map(ms)
                    .unwrap_or_else(|| "-".to_string()),
                optional_percent(member_summary.and_then(|summary| summary.cache_hit_rate)),
                member_summary
                    .map(|summary| summary.cache_eviction_count.to_string())
                    .unwrap_or_else(|| "-".to_string()),
            ));
        }
        out.push('\n');
    }
}

fn render_failure_table(out: &mut String, entries: &[ReportEntry]) {
    let has_failures = entries
        .iter()
        .any(|entry| !entry.summary.failure_summary.is_empty());
    if has_failures {
        out.push_str("## Failures\n\n");
        out.push_str("| Algorithm | Status | Backend | Count | Error |\n");
        out.push_str("|---|---:|---|---:|---|\n");
        for entry in entries {
            for failure in &entry.summary.failure_summary {
                out.push_str(&format!(
                    "| {} | {} | {} | {} | {} |\n",
                    entry.algorithm_name,
                    failure.status_code,
                    failure.selected_backend_id.as_deref().unwrap_or("-"),
                    failure.count,
                    failure.error.as_deref().unwrap_or("-"),
                ));
            }
        }
        out.push('\n');
    }
}

fn ms(value: u64) -> String {
    format!("{value} ms")
}

fn ms_float(value: f64) -> String {
    format!("{value:.1} ms")
}

fn optional_ms(value: Option<f64>) -> String {
    value.map(ms_float).unwrap_or_else(|| "-".to_string())
}

fn optional_integer_ms(value: Option<u64>) -> String {
    value.map(ms).unwrap_or_else(|| "-".to_string())
}

fn percent(value: f64) -> String {
    format!("{:.1}%", value * 100.0)
}

fn optional_percent(value: Option<f64>) -> String {
    value.map(percent).unwrap_or_else(|| "-".to_string())
}

fn optional_score(value: Option<f64>) -> String {
    value
        .map(|value| format!("{value:.3}"))
        .unwrap_or_else(|| "-".to_string())
}

fn optional_rate(value: Option<f64>, unit: &str) -> String {
    value
        .map(|value| format!("{value:.1} {unit}"))
        .unwrap_or_else(|| "-".to_string())
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

fn report_warnings(context: &ReportContext, entries: &[ReportEntry]) -> Vec<String> {
    let mut warnings = Vec::new();
    if entries.is_empty() {
        warnings.push("No algorithm summaries were found.".to_string());
        return warnings;
    }
    let cache_focused = context
        .metadata
        .tags
        .iter()
        .any(|tag| matches!(tag.as_str(), "cache" | "pulsar" | "kv-cache"));
    let queue_admission_focused = context
        .metadata
        .tags
        .iter()
        .any(|tag| matches!(tag.as_str(), "queue-admission" | "queue-mismatch"));
    let pmr_fallback_focused = context
        .metadata
        .tags
        .iter()
        .any(|tag| tag == "pmr-fallback");
    for entry in entries {
        if entry.summary.success_rate < 1.0 {
            warnings.push(format!(
                "{} success rate was {:.1}%.",
                entry.algorithm_name,
                entry.summary.success_rate * 100.0
            ));
        }
        if let Some(score) = entry.summary.capacity_balance_score
            && score < 0.5
        {
            warnings.push(format!(
                "{} capacity balance score was low ({score:.3}).",
                entry.algorithm_name
            ));
        }
        if cache_focused && entry.summary.cache_summary.observed_request_count == 0 {
            warnings.push(format!(
                "{} did not report per-request KV-cache headers.",
                entry.algorithm_name
            ));
        }
        if cache_focused
            && entry.summary.cache_summary.observed_request_count > 0
            && entry.summary.cache_summary.hit_count == 0
        {
            warnings.push(format!(
                "{} reported KV-cache headers but no cache hits.",
                entry.algorithm_name
            ));
        }
        if pmr_fallback_focused
            && entry.algorithm_name == "pulsar-multiregion"
            && entry.summary.routing_selection_summary.fallback_count == 0.0
        {
            warnings.push(
                "pulsar-multiregion did not observe a ranked fallback route choice in the PMR fallback scenario."
                    .to_string(),
            );
        }
    }
    if queue_admission_focused
        && entries.iter().all(|entry| {
            entry.summary.queue_admission_summary.pylon_rejected_count == 0.0
                && entry
                    .summary
                    .queue_admission_summary
                    .stargate_queue_mismatch_retry_count
                    == 0.0
        })
    {
        warnings.push(
            "No pylon queue-mismatch rejections or Stargate queue-mismatch retries were observed."
                .to_string(),
        );
    }
    warnings
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{
        ArrivalPatternConfig, BackendConfig, BackendProfile, RegistrationConfig, ServiceTimeConfig,
        StargateConfig, TokenDistributionConfig, TrafficPatternConfig, UniformTrafficConfig,
    };
    use crate::score::{
        CacheSummary, FailureSummary, QueueAdmissionSummary, RoutingSelectionSummary,
        summarize_with_capacity,
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
            tunnel_protocol: stargate_protocol::TunnelTransportProtocol::Custom,
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

    #[test]
    fn markdown_report_includes_key_columns() {
        let mut request_shares = BTreeMap::new();
        request_shares.insert("backend-0".to_string(), 1.0);
        let summary = RunSummary {
            request_count: 1,
            success_rate: 1.0,
            successful_requests_per_second: Some(40.0),
            successful_output_tokens_per_second: Some(400.0),
            avg_ttft_ms: Some(10.0),
            p50_ttft_ms: Some(10),
            p95_ttft_ms: Some(10),
            p99_ttft_ms: Some(10),
            avg_ttlt_ms: 20.0,
            p50_ttlt_ms: 20,
            p95_ttlt_ms: 20,
            p99_ttlt_ms: 20,
            max_ttlt_ms: 20,
            total_length_ms: 25,
            balance_score: Some(1.0),
            capacity_balance_score: Some(1.0),
            cluster_balance_score: Some(1.0),
            cluster_capacity_balance_score: Some(1.0),
            backend_request_shares: request_shares.clone(),
            backend_capacity_shares: request_shares,
            backend_input_token_shares: BTreeMap::new(),
            backend_output_token_shares: BTreeMap::new(),
            backend_summaries: BTreeMap::new(),
            cluster_request_shares: BTreeMap::from([("cluster-a".to_string(), 1.0)]),
            cluster_capacity_shares: BTreeMap::from([("cluster-a".to_string(), 1.0)]),
            cluster_input_token_shares: BTreeMap::new(),
            cluster_output_token_shares: BTreeMap::new(),
            cluster_summaries: BTreeMap::from([(
                "cluster-a".to_string(),
                crate::score::BackendSummary {
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
            )]),
            cache_summary: CacheSummary {
                observed_request_count: 1,
                hit_count: 1,
                miss_count: 0,
                hit_rate: Some(1.0),
                eviction_count: 0,
                evicted_tokens: 0,
                reused_input_tokens: 10,
                uncached_input_tokens: 1,
                input_reuse_rate: Some(10.0 / 11.0),
            },
            stickiness_summary: Default::default(),
            failure_summary: Vec::new(),
            queue_admission_summary: Default::default(),
            routing_selection_summary: Default::default(),
        };

        let report = render_markdown_report(
            &ReportContext::from_config(&config()),
            &[ReportEntry {
                algorithm_name: "power-of-two".to_string(),
                pylon_queue_admission: None,
                summary,
            }],
        );

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
        let summary = RunSummary {
            request_count: 1,
            success_rate: 1.0,
            successful_requests_per_second: None,
            successful_output_tokens_per_second: None,
            avg_ttft_ms: Some(10.0),
            p50_ttft_ms: Some(10),
            p95_ttft_ms: Some(10),
            p99_ttft_ms: Some(10),
            avg_ttlt_ms: 20.0,
            p50_ttlt_ms: 20,
            p95_ttlt_ms: 20,
            p99_ttlt_ms: 20,
            max_ttlt_ms: 20,
            total_length_ms: 25,
            balance_score: Some(1.0),
            capacity_balance_score: Some(1.0),
            cluster_balance_score: None,
            cluster_capacity_balance_score: None,
            backend_request_shares: BTreeMap::new(),
            backend_capacity_shares: BTreeMap::new(),
            backend_input_token_shares: BTreeMap::new(),
            backend_output_token_shares: BTreeMap::new(),
            backend_summaries: BTreeMap::new(),
            cluster_request_shares: BTreeMap::new(),
            cluster_capacity_shares: BTreeMap::new(),
            cluster_input_token_shares: BTreeMap::new(),
            cluster_output_token_shares: BTreeMap::new(),
            cluster_summaries: BTreeMap::new(),
            cache_summary: CacheSummary::default(),
            stickiness_summary: Default::default(),
            failure_summary: Vec::new(),
            queue_admission_summary: Default::default(),
            routing_selection_summary: Default::default(),
        };

        let report = render_markdown_report(
            &ReportContext::from_config(&config),
            &[ReportEntry {
                algorithm_name: "pulsar".to_string(),
                pylon_queue_admission: None,
                summary,
            }],
        );

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

        let report = render_markdown_report(
            &ReportContext::from_config(&config()),
            &[ReportEntry {
                algorithm_name: "groq-admission-enabled".to_string(),
                pylon_queue_admission: Some(crate::config::PylonQueueAdmissionConfig {
                    enabled: true,
                    min_delta_ms: Some(0),
                    tolerance_factor: Some(1.0),
                    retry_after_ms: Some(5),
                }),
                summary,
            }],
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
        let mut successful_summary = summarize_with_capacity(&[], BTreeMap::new());
        successful_summary.failure_summary = Vec::new();
        let mut failed_summary = summarize_with_capacity(&[], BTreeMap::new());
        failed_summary.failure_summary = vec![FailureSummary {
            status_code: 503,
            selected_backend_id: Some("backend-a".to_string()),
            error: Some("upstream unavailable".to_string()),
            count: 2,
        }];

        let successful_report = render_markdown_report(
            &ReportContext::from_config(&config()),
            &[ReportEntry {
                algorithm_name: "round-robin".to_string(),
                pylon_queue_admission: None,
                summary: successful_summary,
            }],
        );
        let failed_report = render_markdown_report(
            &ReportContext::from_config(&config()),
            &[ReportEntry {
                algorithm_name: "power-of-two".to_string(),
                pylon_queue_admission: None,
                summary: failed_summary,
            }],
        );

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

        let report = render_markdown_report(
            &ReportContext::from_config(&config),
            &[ReportEntry {
                algorithm_name: "pulsar-multiregion".to_string(),
                pylon_queue_admission: None,
                summary: summarize_with_capacity(&[], BTreeMap::new()),
            }],
        );

        assert!(
            report.contains("pulsar-multiregion did not observe a ranked fallback route choice")
        );
    }
}

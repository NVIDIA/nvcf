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

use std::collections::{BTreeMap, BTreeSet};

use serde::{Deserialize, Serialize};

use crate::config::BackendConfig;
use crate::driver::RequestResult;

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RunSummary {
    pub request_count: usize,
    pub success_rate: f64,
    #[serde(default)]
    pub successful_requests_per_second: Option<f64>,
    #[serde(default)]
    pub successful_output_tokens_per_second: Option<f64>,
    pub avg_ttft_ms: Option<f64>,
    pub p50_ttft_ms: Option<u64>,
    pub p95_ttft_ms: Option<u64>,
    pub p99_ttft_ms: Option<u64>,
    pub avg_ttlt_ms: f64,
    pub p50_ttlt_ms: u64,
    pub p95_ttlt_ms: u64,
    pub p99_ttlt_ms: u64,
    pub max_ttlt_ms: u64,
    #[serde(default)]
    pub total_length_ms: u64,
    pub balance_score: Option<f64>,
    #[serde(default)]
    pub capacity_balance_score: Option<f64>,
    #[serde(default)]
    pub cluster_balance_score: Option<f64>,
    #[serde(default)]
    pub cluster_capacity_balance_score: Option<f64>,
    pub backend_request_shares: BTreeMap<String, f64>,
    #[serde(default)]
    pub backend_capacity_shares: BTreeMap<String, f64>,
    #[serde(default)]
    pub backend_input_token_shares: BTreeMap<String, f64>,
    #[serde(default)]
    pub backend_output_token_shares: BTreeMap<String, f64>,
    #[serde(default)]
    pub backend_summaries: BTreeMap<String, BackendSummary>,
    #[serde(default)]
    pub cluster_request_shares: BTreeMap<String, f64>,
    #[serde(default)]
    pub cluster_capacity_shares: BTreeMap<String, f64>,
    #[serde(default)]
    pub cluster_input_token_shares: BTreeMap<String, f64>,
    #[serde(default)]
    pub cluster_output_token_shares: BTreeMap<String, f64>,
    #[serde(default)]
    pub cluster_summaries: BTreeMap<String, BackendSummary>,
    #[serde(default)]
    pub cache_summary: CacheSummary,
    #[serde(default)]
    pub stickiness_summary: StickinessSummary,
    #[serde(default)]
    pub failure_summary: Vec<FailureSummary>,
    pub queue_admission_summary: QueueAdmissionSummary,
    pub routing_selection_summary: RoutingSelectionSummary,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct CacheSummary {
    pub observed_request_count: usize,
    pub hit_count: usize,
    pub miss_count: usize,
    pub hit_rate: Option<f64>,
    pub eviction_count: u64,
    pub evicted_tokens: u64,
    #[serde(default)]
    pub reused_input_tokens: u64,
    #[serde(default)]
    pub uncached_input_tokens: u64,
    #[serde(default)]
    pub input_reuse_rate: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct QueueAdmissionSummary {
    pub pylon_accepted_count: f64,
    pub pylon_rejected_count: f64,
    pub pylon_disabled_count: f64,
    pub pylon_missing_estimate_count: f64,
    pub pylon_unknown_local_estimate_count: f64,
    pub stargate_queue_mismatch_retry_count: f64,
    pub stargate_retry_exhausted_count: f64,
    #[serde(default)]
    pub stargate_retry_exhausted_by_reason: BTreeMap<String, f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct RoutingSelectionSummary {
    pub primary_count: f64,
    pub fallback_count: f64,
    #[serde(default)]
    pub kv_free_token_fallback_count: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct BackendSummary {
    pub request_count: usize,
    pub success_count: usize,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub avg_ttlt_ms: Option<f64>,
    pub p95_ttlt_ms: Option<u64>,
    pub cache_hit_rate: Option<f64>,
    pub cache_eviction_count: u64,
    pub cache_evicted_tokens: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, PartialEq)]
#[serde(deny_unknown_fields)]
pub struct StickinessSummary {
    pub observed_cache_key_count: usize,
    pub sticky_cache_key_count: usize,
    pub moved_cache_key_count: usize,
    pub movement_rate: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(deny_unknown_fields)]
pub struct FailureSummary {
    pub status_code: u16,
    pub selected_backend_id: Option<String>,
    pub error: Option<String>,
    pub count: usize,
}

#[derive(Debug, Clone, Default)]
pub struct RoutingTopology {
    pub backend_capacity_shares: BTreeMap<String, f64>,
    pub cluster_capacity_shares: BTreeMap<String, f64>,
    pub backend_cluster_ids: BTreeMap<String, String>,
}

#[cfg(test)]
pub fn summarize_with_capacity(
    results: &[RequestResult],
    backend_capacity_shares: BTreeMap<String, f64>,
) -> RunSummary {
    let topology = RoutingTopology {
        cluster_capacity_shares: backend_capacity_shares.clone(),
        backend_cluster_ids: backend_capacity_shares
            .keys()
            .map(|backend_id| (backend_id.clone(), backend_id.clone()))
            .collect(),
        backend_capacity_shares,
    };
    summarize_with_topology(results, &topology)
}

pub fn summarize_with_topology(
    results: &[RequestResult],
    topology: &RoutingTopology,
) -> RunSummary {
    let request_count = results.len();
    let successes = results.iter().filter(|result| result.ok).count();
    let success_rate = if request_count == 0 {
        0.0
    } else {
        successes as f64 / request_count as f64
    };

    let mut ttft: Vec<u64> = results
        .iter()
        .filter_map(|result| result.first_output_ms)
        .collect();
    let mut ttlt: Vec<u64> = results.iter().map(|result| result.completion_ms).collect();
    ttft.sort_unstable();
    ttlt.sort_unstable();

    let max_ttlt_ms = results
        .iter()
        .map(|result| result.completion_ms)
        .max()
        .unwrap_or(0);
    let total_length_ms = total_length_ms(results);
    let backend_request_shares = backend_request_shares(results);
    let backend_input_token_shares = backend_token_shares(results, TokenKind::Input);
    let backend_output_token_shares = backend_token_shares(results, TokenKind::Output);
    let cluster_request_shares = cluster_request_shares(results, &topology.backend_cluster_ids);
    let cluster_input_token_shares =
        cluster_token_shares(results, TokenKind::Input, &topology.backend_cluster_ids);
    let cluster_output_token_shares =
        cluster_token_shares(results, TokenKind::Output, &topology.backend_cluster_ids);
    let balance_score = if backend_request_shares.is_empty() {
        None
    } else {
        Some(equal_share_balance_score(&backend_request_shares))
    };
    let capacity_balance_score =
        if backend_request_shares.is_empty() || topology.backend_capacity_shares.is_empty() {
            None
        } else {
            Some(expected_share_balance_score(
                &backend_request_shares,
                &topology.backend_capacity_shares,
            ))
        };
    let cluster_balance_score = (!cluster_request_shares.is_empty()
        && !topology.cluster_capacity_shares.is_empty())
    .then(|| {
        equal_expected_share_balance_score(
            &cluster_request_shares,
            topology.cluster_capacity_shares.keys(),
        )
    });
    let cluster_capacity_balance_score = (!cluster_input_token_shares.is_empty()
        && !topology.cluster_capacity_shares.is_empty())
    .then(|| {
        expected_share_balance_score(
            &cluster_input_token_shares,
            &topology.cluster_capacity_shares,
        )
    });
    let cache_summary = cache_summary(results);
    let backend_summaries = backend_summaries(results);
    let cluster_summaries = cluster_summaries(results, &topology.backend_cluster_ids);
    let stickiness_summary = stickiness_summary(results, &topology.backend_cluster_ids);
    let failure_summary = failure_summary(results);

    RunSummary {
        request_count,
        success_rate,
        successful_requests_per_second: per_second(successes as u64, total_length_ms),
        successful_output_tokens_per_second: per_second(
            results
                .iter()
                .filter(|result| result.ok)
                .map(|result| result.output_tokens)
                .sum(),
            total_length_ms,
        ),
        avg_ttft_ms: average(&ttft),
        p50_ttft_ms: percentile(&ttft, 0.50),
        p95_ttft_ms: percentile(&ttft, 0.95),
        p99_ttft_ms: percentile(&ttft, 0.99),
        avg_ttlt_ms: average(&ttlt).unwrap_or(0.0),
        p50_ttlt_ms: percentile(&ttlt, 0.50).unwrap_or(0),
        p95_ttlt_ms: percentile(&ttlt, 0.95).unwrap_or(0),
        p99_ttlt_ms: percentile(&ttlt, 0.99).unwrap_or(0),
        max_ttlt_ms,
        total_length_ms,
        balance_score,
        capacity_balance_score,
        cluster_balance_score,
        cluster_capacity_balance_score,
        backend_request_shares,
        backend_capacity_shares: topology.backend_capacity_shares.clone(),
        backend_input_token_shares,
        backend_output_token_shares,
        backend_summaries,
        cluster_request_shares,
        cluster_capacity_shares: topology.cluster_capacity_shares.clone(),
        cluster_input_token_shares,
        cluster_output_token_shares,
        cluster_summaries,
        cache_summary,
        stickiness_summary,
        failure_summary,
        queue_admission_summary: QueueAdmissionSummary::default(),
        routing_selection_summary: RoutingSelectionSummary::default(),
    }
}

pub fn queue_admission_summary_from_prometheus(metrics: &str) -> QueueAdmissionSummary {
    let mut summary = QueueAdmissionSummary::default();
    for line in metrics.lines() {
        let Some((series, value)) = prometheus_counter_sample(line) else {
            continue;
        };
        let name = series.split_once('{').map_or(series, |(name, _)| name);
        match name {
            "pylon_queue_admission_decisions_total"
            | "pylon_queue_admission_decisions_total_total" => {
                match prometheus_label_value(series, "result") {
                    Some("accepted") => summary.pylon_accepted_count += value,
                    Some("rejected") => summary.pylon_rejected_count += value,
                    Some("disabled") => summary.pylon_disabled_count += value,
                    Some("missing_estimate") => summary.pylon_missing_estimate_count += value,
                    Some("unknown_local_estimate") => {
                        summary.pylon_unknown_local_estimate_count += value;
                    }
                    _ => {}
                }
            }
            "stargate_proxy_retries_total" | "stargate_proxy_retries_total_total"
                if prometheus_label_value(series, "reason") == Some("queue_estimate_mismatch") =>
            {
                summary.stargate_queue_mismatch_retry_count += value;
            }
            "stargate_proxy_retry_exhausted_total"
            | "stargate_proxy_retry_exhausted_total_total" => {
                summary.stargate_retry_exhausted_count += value;
                let reason = prometheus_label_value(series, "reason")
                    .unwrap_or("unlabeled")
                    .to_string();
                *summary
                    .stargate_retry_exhausted_by_reason
                    .entry(reason)
                    .or_default() += value;
            }
            _ => {}
        }
    }
    summary
}

pub fn queue_admission_summary_delta_from_prometheus(
    baseline_metrics: &str,
    post_replay_metrics: &str,
) -> QueueAdmissionSummary {
    let baseline = queue_admission_summary_from_prometheus(baseline_metrics);
    let post_replay = queue_admission_summary_from_prometheus(post_replay_metrics);
    QueueAdmissionSummary {
        pylon_accepted_count: counter_delta(
            post_replay.pylon_accepted_count,
            baseline.pylon_accepted_count,
        ),
        pylon_rejected_count: counter_delta(
            post_replay.pylon_rejected_count,
            baseline.pylon_rejected_count,
        ),
        pylon_disabled_count: counter_delta(
            post_replay.pylon_disabled_count,
            baseline.pylon_disabled_count,
        ),
        pylon_missing_estimate_count: counter_delta(
            post_replay.pylon_missing_estimate_count,
            baseline.pylon_missing_estimate_count,
        ),
        pylon_unknown_local_estimate_count: counter_delta(
            post_replay.pylon_unknown_local_estimate_count,
            baseline.pylon_unknown_local_estimate_count,
        ),
        stargate_queue_mismatch_retry_count: counter_delta(
            post_replay.stargate_queue_mismatch_retry_count,
            baseline.stargate_queue_mismatch_retry_count,
        ),
        stargate_retry_exhausted_count: counter_delta(
            post_replay.stargate_retry_exhausted_count,
            baseline.stargate_retry_exhausted_count,
        ),
        stargate_retry_exhausted_by_reason: post_replay
            .stargate_retry_exhausted_by_reason
            .iter()
            .filter_map(|(reason, post_replay_count)| {
                let count = counter_delta(
                    *post_replay_count,
                    baseline
                        .stargate_retry_exhausted_by_reason
                        .get(reason)
                        .copied()
                        .unwrap_or_default(),
                );
                (count > 0.0).then(|| (reason.clone(), count))
            })
            .collect(),
    }
}

pub fn routing_selection_summary_from_prometheus(metrics: &str) -> RoutingSelectionSummary {
    let mut summary = RoutingSelectionSummary::default();
    for line in metrics.lines() {
        let Some((series, value)) = prometheus_counter_sample(line) else {
            continue;
        };
        let name = series.split_once('{').map_or(series, |(name, _)| name);
        match name {
            "stargate_routing_selections_total" | "stargate_routing_selections_total_total" => {
                match prometheus_label_value(series, "selection") {
                    Some("primary") => summary.primary_count += value,
                    Some("fallback") => summary.fallback_count += value,
                    _ => {}
                }
            }
            "stargate_routing_kv_free_token_fallback_selections_total"
            | "stargate_routing_kv_free_token_fallback_selections_total_total" => {
                summary.kv_free_token_fallback_count += value;
            }
            _ => {}
        }
    }
    summary
}

pub fn routing_selection_summary_delta_from_prometheus(
    baseline_metrics: &str,
    post_replay_metrics: &str,
) -> RoutingSelectionSummary {
    let baseline = routing_selection_summary_from_prometheus(baseline_metrics);
    let post_replay = routing_selection_summary_from_prometheus(post_replay_metrics);
    RoutingSelectionSummary {
        primary_count: counter_delta(post_replay.primary_count, baseline.primary_count),
        fallback_count: counter_delta(post_replay.fallback_count, baseline.fallback_count),
        kv_free_token_fallback_count: counter_delta(
            post_replay.kv_free_token_fallback_count,
            baseline.kv_free_token_fallback_count,
        ),
    }
}

fn counter_delta(post_replay: f64, baseline: f64) -> f64 {
    (post_replay - baseline).max(0.0)
}

fn prometheus_counter_sample(line: &str) -> Option<(&str, f64)> {
    let mut fields = line.split_whitespace();
    let series = fields.next()?;
    if series.starts_with('#') {
        return None;
    }
    let value = fields.next()?.parse::<f64>().ok()?;
    Some((series, value))
}

fn prometheus_label_value<'a>(series: &'a str, label: &str) -> Option<&'a str> {
    let needle = format!(r#"{label}=""#);
    let start = series.find(&needle)? + needle.len();
    let rest = &series[start..];
    let end = rest.find('"')?;
    Some(&rest[..end])
}

pub fn topology_for(backends: &BackendConfig) -> RoutingTopology {
    let mut backend_capacities = BTreeMap::new();
    let mut cluster_capacities = BTreeMap::new();
    let mut backend_cluster_ids = BTreeMap::new();
    let mut total_capacity = 0.0f64;
    for index in 0..backends.count {
        let capacity = backends
            .profile_for_index(index)
            .registration
            .last_mean_input_tps;
        if capacity > 0.0 && capacity.is_finite() {
            let backend_id = format!("backend-{index}");
            let cluster_id = backends.effective_cluster_id_for_index(index);
            backend_capacities.insert(backend_id.clone(), capacity);
            *cluster_capacities.entry(cluster_id.clone()).or_default() += capacity;
            backend_cluster_ids.insert(backend_id, cluster_id);
            total_capacity += capacity;
        }
    }
    if total_capacity <= 0.0 {
        return RoutingTopology::default();
    }
    RoutingTopology {
        backend_capacity_shares: normalized_shares(backend_capacities, total_capacity),
        cluster_capacity_shares: normalized_shares(cluster_capacities, total_capacity),
        backend_cluster_ids,
    }
}

fn normalized_shares(
    capacities: BTreeMap<String, f64>,
    total_capacity: f64,
) -> BTreeMap<String, f64> {
    capacities
        .into_iter()
        .map(|(id, capacity)| (id, capacity / total_capacity))
        .collect()
}

fn total_length_ms(results: &[RequestResult]) -> u64 {
    let Some(first_dispatch_ms) = results.iter().map(|result| result.dispatch_offset_ms).min()
    else {
        return 0;
    };
    let last_completion_ms = results
        .iter()
        .map(|result| {
            // Broken or synthetic benchmark inputs should not wrap the report window.
            result
                .dispatch_offset_ms
                .saturating_add(result.completion_ms)
        })
        .max()
        .unwrap_or(first_dispatch_ms);
    // If input rows are out of order or malformed, report a zero-length window instead of wrapping.
    last_completion_ms.saturating_sub(first_dispatch_ms)
}

fn backend_request_shares(results: &[RequestResult]) -> BTreeMap<String, f64> {
    let mut counts: BTreeMap<String, usize> = BTreeMap::new();
    let mut total = 0usize;
    for result in results {
        if !result.ok {
            continue;
        }
        if let Some(backend_id) = &result.selected_backend_id {
            *counts.entry(backend_id.clone()).or_default() += 1;
            total += 1;
        }
    }

    if total == 0 {
        return BTreeMap::new();
    }

    counts
        .into_iter()
        .map(|(backend_id, count)| (backend_id, count as f64 / total as f64))
        .collect()
}

fn cluster_request_shares(
    results: &[RequestResult],
    backend_cluster_ids: &BTreeMap<String, String>,
) -> BTreeMap<String, f64> {
    request_shares_by_group(results, |backend_id| {
        backend_cluster_ids
            .get(backend_id)
            .map(String::as_str)
            .unwrap_or(backend_id)
    })
}

fn request_shares_by_group<'a>(
    results: &'a [RequestResult],
    group_for_backend: impl Fn(&'a str) -> &'a str,
) -> BTreeMap<String, f64> {
    let mut counts: BTreeMap<String, usize> = BTreeMap::new();
    let mut total = 0usize;
    for result in results {
        if !result.ok {
            continue;
        }
        if let Some(backend_id) = &result.selected_backend_id {
            *counts
                .entry(group_for_backend(backend_id).to_string())
                .or_default() += 1;
            total += 1;
        }
    }
    if total == 0 {
        return BTreeMap::new();
    }
    counts
        .into_iter()
        .map(|(id, count)| (id, count as f64 / total as f64))
        .collect()
}

#[derive(Debug, Clone, Copy)]
enum TokenKind {
    Input,
    Output,
}

impl TokenKind {
    fn includes_failed_routed_work(self) -> bool {
        matches!(self, Self::Input)
    }
}

fn backend_token_shares(results: &[RequestResult], token_kind: TokenKind) -> BTreeMap<String, f64> {
    let mut totals: BTreeMap<String, u64> = BTreeMap::new();
    let mut total = 0u64;
    for result in results {
        if !result.ok && !token_kind.includes_failed_routed_work() {
            continue;
        }
        let Some(backend_id) = &result.selected_backend_id else {
            continue;
        };
        let tokens = match token_kind {
            TokenKind::Input => result.input_tokens,
            TokenKind::Output => result.output_tokens,
        };
        *totals.entry(backend_id.clone()).or_default() += tokens;
        // Benchmark token totals are report counters; saturate instead of wrapping on bad input.
        total = total.saturating_add(tokens);
    }

    if total == 0 {
        return BTreeMap::new();
    }

    totals
        .into_iter()
        .map(|(backend_id, tokens)| (backend_id, tokens as f64 / total as f64))
        .collect()
}

fn cluster_token_shares(
    results: &[RequestResult],
    token_kind: TokenKind,
    backend_cluster_ids: &BTreeMap<String, String>,
) -> BTreeMap<String, f64> {
    token_shares_by_group(results, token_kind, |backend_id| {
        backend_cluster_ids
            .get(backend_id)
            .map(String::as_str)
            .unwrap_or(backend_id)
    })
}

fn token_shares_by_group<'a>(
    results: &'a [RequestResult],
    token_kind: TokenKind,
    group_for_backend: impl Fn(&'a str) -> &'a str,
) -> BTreeMap<String, f64> {
    let mut totals: BTreeMap<String, u64> = BTreeMap::new();
    let mut total = 0u64;
    for result in results {
        if !result.ok && !token_kind.includes_failed_routed_work() {
            continue;
        }
        let Some(backend_id) = &result.selected_backend_id else {
            continue;
        };
        let tokens = match token_kind {
            TokenKind::Input => result.input_tokens,
            TokenKind::Output => result.output_tokens,
        };
        *totals
            .entry(group_for_backend(backend_id).to_string())
            .or_default() += tokens;
        total = total.saturating_add(tokens);
    }
    if total == 0 {
        return BTreeMap::new();
    }
    totals
        .into_iter()
        .map(|(id, tokens)| (id, tokens as f64 / total as f64))
        .collect()
}

fn backend_summaries(results: &[RequestResult]) -> BTreeMap<String, BackendSummary> {
    summaries_by_group(results, |backend_id| backend_id)
}

fn cluster_summaries(
    results: &[RequestResult],
    backend_cluster_ids: &BTreeMap<String, String>,
) -> BTreeMap<String, BackendSummary> {
    summaries_by_group(results, |backend_id| {
        backend_cluster_ids
            .get(backend_id)
            .map(String::as_str)
            .unwrap_or(backend_id)
    })
}

fn summaries_by_group<'a>(
    results: &'a [RequestResult],
    group_for_backend: impl Fn(&'a str) -> &'a str,
) -> BTreeMap<String, BackendSummary> {
    let mut grouped: BTreeMap<String, Vec<&RequestResult>> = BTreeMap::new();
    for result in results {
        let Some(backend_id) = &result.selected_backend_id else {
            continue;
        };
        grouped
            .entry(group_for_backend(backend_id).to_string())
            .or_default()
            .push(result);
    }

    grouped
        .into_iter()
        .map(|(backend_id, results)| {
            let request_count = results.len();
            let success_count = results.iter().filter(|result| result.ok).count();
            let input_tokens = results
                .iter()
                .map(|result| result.input_tokens)
                .sum::<u64>();
            let output_tokens = results
                .iter()
                .map(|result| result.output_tokens)
                .sum::<u64>();
            let mut ttlt = results
                .iter()
                .map(|result| result.completion_ms)
                .collect::<Vec<_>>();
            ttlt.sort_unstable();
            let observed_cache = results
                .iter()
                .filter(|result| result.kv_cache_hit.is_some())
                .count();
            let cache_hits = results
                .iter()
                .filter(|result| result.kv_cache_hit == Some(true))
                .count();
            let cache_hit_rate =
                (observed_cache > 0).then_some(cache_hits as f64 / observed_cache as f64);
            let cache_eviction_count = results
                .iter()
                .filter_map(|result| result.kv_cache_evicted_entries)
                .sum();
            let cache_evicted_tokens = results
                .iter()
                .filter_map(|result| result.kv_cache_evicted_tokens)
                .sum();
            (
                backend_id,
                BackendSummary {
                    request_count,
                    success_count,
                    input_tokens,
                    output_tokens,
                    avg_ttlt_ms: average(&ttlt),
                    p95_ttlt_ms: percentile(&ttlt, 0.95),
                    cache_hit_rate,
                    cache_eviction_count,
                    cache_evicted_tokens,
                },
            )
        })
        .collect()
}

fn stickiness_summary(
    results: &[RequestResult],
    backend_cluster_ids: &BTreeMap<String, String>,
) -> StickinessSummary {
    let mut clusters_by_cache_key = BTreeMap::<String, BTreeSet<String>>::new();
    for result in results {
        if !result.ok {
            continue;
        }
        let (Some(cache_affinity_key), Some(backend_id)) =
            (&result.cache_affinity_key, &result.selected_backend_id)
        else {
            continue;
        };
        clusters_by_cache_key
            .entry(cache_affinity_key.clone())
            .or_default()
            .insert(
                backend_cluster_ids
                    .get(backend_id)
                    .unwrap_or(backend_id)
                    .clone(),
            );
    }
    let observed_cache_key_count = clusters_by_cache_key.len();
    let moved_cache_key_count = clusters_by_cache_key
        .values()
        .filter(|clusters| clusters.len() > 1)
        .count();
    let sticky_cache_key_count = observed_cache_key_count - moved_cache_key_count;
    let movement_rate = (observed_cache_key_count > 0)
        .then_some(moved_cache_key_count as f64 / observed_cache_key_count as f64);
    StickinessSummary {
        observed_cache_key_count,
        sticky_cache_key_count,
        moved_cache_key_count,
        movement_rate,
    }
}

fn failure_summary(results: &[RequestResult]) -> Vec<FailureSummary> {
    let mut counts = BTreeMap::<(u16, Option<String>, Option<String>), usize>::new();
    for result in results {
        if result.ok {
            continue;
        }
        let key = (
            result.status_code,
            result.selected_backend_id.clone(),
            result.error.clone(),
        );
        *counts.entry(key).or_default() += 1;
    }
    counts
        .into_iter()
        .map(
            |((status_code, selected_backend_id, error), count)| FailureSummary {
                status_code,
                selected_backend_id,
                error,
                count,
            },
        )
        .collect()
}

fn equal_share_balance_score(shares: &BTreeMap<String, f64>) -> f64 {
    let expected = 1.0 / shares.len() as f64;
    let mean_abs_error = shares
        .values()
        .map(|observed| (observed - expected).abs())
        .sum::<f64>()
        / shares.len() as f64;
    (1.0 - mean_abs_error / expected).clamp(0.0, 1.0)
}

fn equal_expected_share_balance_score<'a>(
    observed: &BTreeMap<String, f64>,
    ids: impl Iterator<Item = &'a String>,
) -> f64 {
    let ids = ids.collect::<Vec<_>>();
    let expected_share = 1.0 / ids.len() as f64;
    let expected = ids
        .into_iter()
        .map(|id| (id.clone(), expected_share))
        .collect();
    expected_share_balance_score(observed, &expected)
}

fn expected_share_balance_score(
    observed: &BTreeMap<String, f64>,
    expected: &BTreeMap<String, f64>,
) -> f64 {
    let backend_ids = expected
        .keys()
        .chain(observed.keys())
        .collect::<BTreeSet<_>>();
    let total_abs_error = backend_ids
        .into_iter()
        .map(|backend_id| {
            let observed = observed.get(backend_id).copied().unwrap_or(0.0);
            let expected = expected.get(backend_id).copied().unwrap_or(0.0);
            (observed - expected).abs()
        })
        .sum::<f64>();
    (1.0 - total_abs_error / 2.0).clamp(0.0, 1.0)
}

fn cache_summary(results: &[RequestResult]) -> CacheSummary {
    let observed_request_count = results
        .iter()
        .filter(|result| result.kv_cache_hit.is_some())
        .count();
    let hit_count = results
        .iter()
        .filter(|result| result.kv_cache_hit == Some(true))
        .count();
    let miss_count = results
        .iter()
        .filter(|result| result.kv_cache_hit == Some(false))
        .count();
    let hit_rate =
        (observed_request_count > 0).then_some(hit_count as f64 / observed_request_count as f64);
    let eviction_count = results
        .iter()
        .filter_map(|result| result.kv_cache_evicted_entries)
        .sum();
    let evicted_tokens = results
        .iter()
        .filter_map(|result| result.kv_cache_evicted_tokens)
        .sum();
    let reused_input_tokens = results
        .iter()
        .filter_map(|result| result.kv_cache_reused_input_tokens)
        .sum();
    let uncached_input_tokens = results
        .iter()
        .filter_map(|result| result.kv_cache_uncached_input_tokens)
        .sum();
    let observed_input_tokens = reused_input_tokens + uncached_input_tokens;
    let input_reuse_rate = (observed_input_tokens > 0)
        .then_some(reused_input_tokens as f64 / observed_input_tokens as f64);
    CacheSummary {
        observed_request_count,
        hit_count,
        miss_count,
        hit_rate,
        eviction_count,
        evicted_tokens,
        reused_input_tokens,
        uncached_input_tokens,
        input_reuse_rate,
    }
}

fn per_second(value: u64, total_length_ms: u64) -> Option<f64> {
    (total_length_ms > 0).then_some(value as f64 * 1000.0 / total_length_ms as f64)
}

fn average(values: &[u64]) -> Option<f64> {
    if values.is_empty() {
        None
    } else {
        Some(values.iter().map(|value| *value as f64).sum::<f64>() / values.len() as f64)
    }
}

fn percentile(values: &[u64], q: f64) -> Option<u64> {
    if values.is_empty() {
        return None;
    }
    let index = ((values.len() - 1) as f64 * q).round() as usize;
    values.get(index).copied()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn result(id: usize, backend: &str, ttft: u64, ttlt: u64) -> RequestResult {
        RequestResult {
            request_index: id,
            request_id: format!("req-{id}"),
            routing_key: None,
            cache_affinity_key: None,
            input_tokens: 1,
            output_tokens: 1,
            scheduled_offset_ms: 0,
            status_code: 200,
            selected_backend_id: Some(backend.to_string()),
            dispatch_offset_ms: 0,
            response_headers_ms: Some(1),
            first_output_ms: Some(ttft),
            completion_ms: ttlt,
            kv_cache_hit: None,
            kv_cache_reused_input_tokens: None,
            kv_cache_uncached_input_tokens: None,
            kv_cache_evicted_entries: None,
            kv_cache_evicted_tokens: None,
            ok: true,
            error: None,
        }
    }

    #[test]
    fn summary_computes_basic_metrics() {
        let summary = summarize_with_capacity(
            &[
                result(0, "a", 10, 20),
                result(1, "b", 30, 40),
                result(2, "a", 20, 50),
                result(3, "b", 40, 60),
            ],
            BTreeMap::new(),
        );
        assert_eq!(summary.request_count, 4);
        assert_eq!(summary.p50_ttft_ms, Some(30));
        assert_eq!(summary.p95_ttlt_ms, 60);
        assert_eq!(summary.max_ttlt_ms, 60);
        assert_eq!(summary.total_length_ms, 60);
        assert_eq!(summary.balance_score, Some(1.0));
        assert_eq!(summary.capacity_balance_score, None);
    }

    #[test]
    fn total_length_accounts_for_dispatch_offsets() {
        let mut first = result(0, "a", 10, 20);
        first.dispatch_offset_ms = 100;
        let mut second = result(1, "a", 10, 40);
        second.dispatch_offset_ms = 250;

        let summary = summarize_with_capacity(&[first, second], BTreeMap::new());

        assert_eq!(summary.total_length_ms, 190);
        assert_eq!(summary.max_ttlt_ms, 40);
    }

    #[test]
    fn summary_computes_capacity_balance_score() {
        let mut expected = BTreeMap::new();
        expected.insert("a".to_string(), 0.75);
        expected.insert("b".to_string(), 0.25);

        let summary = summarize_with_capacity(
            &[
                result(0, "a", 10, 20),
                result(1, "a", 10, 20),
                result(2, "a", 10, 20),
                result(3, "b", 10, 20),
            ],
            expected,
        );

        assert_eq!(summary.capacity_balance_score, Some(1.0));
    }

    #[test]
    fn summary_computes_grouped_cluster_balance_and_goodput() {
        let backends = BackendConfig {
            count: 4,
            cluster_id_template: Some("cluster-{cluster_index}".to_string()),
            pylons_per_cluster: 2,
            profile: crate::config::BackendProfile {
                name: "unused".to_string(),
                weight: 1.0,
                max_concurrent_requests: None,
                kv_cache_capacity_tokens: 0,
                service_time_ms: crate::config::ServiceTimeConfig {
                    ttft_mean: 1,
                    ttft_jitter_ms: 0,
                    decode_tokens_per_s: 1,
                    decode_jitter_ms: 0,
                    prefill_tokens_per_s: None,
                },
                registration: crate::config::RegistrationConfig {
                    last_mean_input_tps: 1.0,
                },
            },
            profiles: vec![
                crate::config::BackendProfileGroup {
                    count: 2,
                    profile: crate::config::BackendProfile {
                        name: "fast".to_string(),
                        weight: 1.0,
                        max_concurrent_requests: None,
                        kv_cache_capacity_tokens: 0,
                        service_time_ms: crate::config::ServiceTimeConfig {
                            ttft_mean: 1,
                            ttft_jitter_ms: 0,
                            decode_tokens_per_s: 1,
                            decode_jitter_ms: 0,
                            prefill_tokens_per_s: None,
                        },
                        registration: crate::config::RegistrationConfig {
                            last_mean_input_tps: 100.0,
                        },
                    },
                },
                crate::config::BackendProfileGroup {
                    count: 2,
                    profile: crate::config::BackendProfile {
                        name: "slow".to_string(),
                        weight: 1.0,
                        max_concurrent_requests: None,
                        kv_cache_capacity_tokens: 0,
                        service_time_ms: crate::config::ServiceTimeConfig {
                            ttft_mean: 1,
                            ttft_jitter_ms: 0,
                            decode_tokens_per_s: 1,
                            decode_jitter_ms: 0,
                            prefill_tokens_per_s: None,
                        },
                        registration: crate::config::RegistrationConfig {
                            last_mean_input_tps: 50.0,
                        },
                    },
                },
            ],
        };
        let mut first = result(0, "backend-0", 10, 1000);
        first.input_tokens = 100;
        first.output_tokens = 10;
        first.cache_affinity_key = Some("shared-prefix".to_string());
        let mut second = result(1, "backend-1", 10, 1000);
        second.input_tokens = 100;
        second.output_tokens = 10;
        second.cache_affinity_key = Some("shared-prefix".to_string());
        let mut third = result(2, "backend-2", 10, 1000);
        third.input_tokens = 50;
        third.output_tokens = 10;
        let mut fourth = result(3, "backend-3", 10, 1000);
        fourth.input_tokens = 50;
        fourth.output_tokens = 10;

        let summary =
            summarize_with_topology(&[first, second, third, fourth], &topology_for(&backends));

        assert_eq!(summary.cluster_request_shares["cluster-0"], 0.5);
        assert_eq!(summary.cluster_request_shares["cluster-1"], 0.5);
        assert_eq!(summary.cluster_input_token_shares["cluster-0"], 2.0 / 3.0);
        assert_eq!(summary.cluster_input_token_shares["cluster-1"], 1.0 / 3.0);
        assert_eq!(summary.cluster_capacity_balance_score, Some(1.0));
        assert_eq!(summary.cluster_summaries["cluster-0"].request_count, 2);
        assert_eq!(summary.successful_requests_per_second, Some(4.0));
        assert_eq!(summary.successful_output_tokens_per_second, Some(40.0));
        assert_eq!(summary.stickiness_summary.moved_cache_key_count, 0);
        assert_eq!(summary.stickiness_summary.movement_rate, Some(0.0));
    }

    #[test]
    fn input_capacity_balance_includes_failed_work_routed_to_a_cluster() {
        let backends = BackendConfig {
            count: 2,
            cluster_id_template: Some("cluster-{cluster_index}".to_string()),
            pylons_per_cluster: 1,
            profile: crate::config::BackendProfile {
                name: "equal".to_string(),
                weight: 1.0,
                max_concurrent_requests: None,
                kv_cache_capacity_tokens: 0,
                service_time_ms: crate::config::ServiceTimeConfig {
                    ttft_mean: 1,
                    ttft_jitter_ms: 0,
                    decode_tokens_per_s: 1,
                    decode_jitter_ms: 0,
                    prefill_tokens_per_s: None,
                },
                registration: crate::config::RegistrationConfig {
                    last_mean_input_tps: 100.0,
                },
            },
            profiles: Vec::new(),
        };
        let mut served = result(0, "backend-0", 10, 1000);
        served.input_tokens = 100;
        served.output_tokens = 10;
        let mut rejected = result(1, "backend-1", 10, 1000);
        rejected.input_tokens = 100;
        rejected.output_tokens = 0;
        rejected.status_code = 429;
        rejected.ok = false;

        let summary = summarize_with_topology(&[served, rejected], &topology_for(&backends));

        assert_eq!(summary.backend_input_token_shares["backend-0"], 0.5);
        assert_eq!(summary.backend_input_token_shares["backend-1"], 0.5);
        assert_eq!(summary.cluster_input_token_shares["cluster-0"], 0.5);
        assert_eq!(summary.cluster_input_token_shares["cluster-1"], 0.5);
        assert_eq!(summary.cluster_capacity_balance_score, Some(1.0));
        assert_eq!(summary.backend_output_token_shares.len(), 1);
    }

    #[test]
    fn summary_computes_cache_metrics() {
        let mut hit = result(0, "a", 10, 20);
        hit.kv_cache_hit = Some(true);
        hit.kv_cache_reused_input_tokens = Some(100_000);
        hit.kv_cache_uncached_input_tokens = Some(2_000);
        let mut miss = result(1, "a", 10, 20);
        miss.kv_cache_hit = Some(false);
        miss.kv_cache_reused_input_tokens = Some(0);
        miss.kv_cache_uncached_input_tokens = Some(100_000);
        miss.kv_cache_evicted_entries = Some(2);
        miss.kv_cache_evicted_tokens = Some(150);

        let summary = summarize_with_capacity(&[hit, miss], BTreeMap::new());

        assert_eq!(
            summary.cache_summary,
            CacheSummary {
                observed_request_count: 2,
                hit_count: 1,
                miss_count: 1,
                hit_rate: Some(0.5),
                eviction_count: 2,
                evicted_tokens: 150,
                reused_input_tokens: 100_000,
                uncached_input_tokens: 102_000,
                input_reuse_rate: Some(100_000.0 / 202_000.0),
            }
        );
    }

    #[test]
    fn summary_computes_token_shares_and_backend_summaries() {
        let mut first = result(0, "a", 10, 20);
        first.input_tokens = 100;
        first.output_tokens = 10;
        first.kv_cache_hit = Some(true);
        let mut second = result(1, "b", 10, 40);
        second.input_tokens = 300;
        second.output_tokens = 30;
        second.kv_cache_hit = Some(false);

        let summary = summarize_with_capacity(&[first, second], BTreeMap::new());

        assert_eq!(summary.backend_input_token_shares["a"], 0.25);
        assert_eq!(summary.backend_output_token_shares["b"], 0.75);
        assert_eq!(summary.backend_summaries["a"].request_count, 1);
        assert_eq!(summary.backend_summaries["a"].cache_hit_rate, Some(1.0));
        assert_eq!(summary.backend_summaries["b"].p95_ttlt_ms, Some(40));
    }

    #[test]
    fn summary_computes_stickiness_and_failures() {
        let mut first = result(0, "a", 10, 20);
        first.cache_affinity_key = Some("cak-a".to_string());
        let mut second = result(1, "b", 10, 20);
        second.cache_affinity_key = Some("cak-a".to_string());
        let mut failed = result(2, "b", 10, 20);
        failed.ok = false;
        failed.status_code = 502;
        failed.error = Some("upstream closed".to_string());

        let summary = summarize_with_capacity(&[first, second, failed], BTreeMap::new());

        assert_eq!(summary.stickiness_summary.observed_cache_key_count, 1);
        assert_eq!(summary.stickiness_summary.moved_cache_key_count, 1);
        assert_eq!(summary.stickiness_summary.movement_rate, Some(1.0));
        assert_eq!(summary.failure_summary.len(), 1);
        assert_eq!(summary.failure_summary[0].status_code, 502);
        assert_eq!(summary.failure_summary[0].count, 1);
    }

    #[test]
    fn parses_queue_admission_and_retry_counters_from_native_metrics() {
        let metrics = r#"
pylon_queue_admission_decisions_total{inference_server_id="backend-0",model_id="dummy-model",result="rejected"} 2
pylon_queue_admission_decisions_total{inference_server_id="backend-1",model_id="dummy-model",result="disabled"} 4
stargate_proxy_retries_total{model="dummy-model",reason="queue_estimate_mismatch",routing_key=""} 2
stargate_proxy_retry_exhausted_total{model="dummy-model",reason="retry_budget_exhausted",routing_key=""} 1
"#;

        let summary = queue_admission_summary_from_prometheus(metrics);

        assert_eq!(summary.pylon_rejected_count, 2.0);
        assert_eq!(summary.pylon_disabled_count, 4.0);
        assert_eq!(summary.stargate_queue_mismatch_retry_count, 2.0);
        assert_eq!(summary.stargate_retry_exhausted_count, 1.0);
        assert_eq!(
            summary.stargate_retry_exhausted_by_reason["retry_budget_exhausted"],
            1.0
        );
    }

    #[test]
    fn parses_collector_renamed_counter_metrics() {
        let metrics = r#"
pylon_queue_admission_decisions_total_total{inference_server_id="backend-0",model_id="dummy-model",result="rejected"} 3
pylon_queue_admission_decisions_total_total{inference_server_id="backend-1",model_id="dummy-model",result="disabled"} 7
stargate_proxy_retries_total_total{model="dummy-model",reason="queue_estimate_mismatch",routing_key=""} 3
stargate_proxy_retry_exhausted_total_total{model="dummy-model",reason="queue_estimate_mismatch",routing_key=""} 2
"#;

        let summary = queue_admission_summary_from_prometheus(metrics);

        assert_eq!(summary.pylon_rejected_count, 3.0);
        assert_eq!(summary.pylon_disabled_count, 7.0);
        assert_eq!(summary.stargate_queue_mismatch_retry_count, 3.0);
        assert_eq!(summary.stargate_retry_exhausted_count, 2.0);
        assert_eq!(
            summary.stargate_retry_exhausted_by_reason["queue_estimate_mismatch"],
            2.0
        );
    }

    #[test]
    fn queue_admission_delta_excludes_pre_replay_probe_counters() {
        let baseline = r#"
pylon_queue_admission_decisions_total_total{inference_server_id="backend-0",model_id="dummy-model",result="disabled"} 1
stargate_proxy_retries_total_total{model="dummy-model",reason="queue_estimate_mismatch",routing_key=""} 2
stargate_proxy_retry_exhausted_total_total{model="dummy-model",reason="retry_budget_exhausted",routing_key=""} 1
"#;
        let post_replay = r#"
pylon_queue_admission_decisions_total_total{inference_server_id="backend-0",model_id="dummy-model",result="disabled"} 97
stargate_proxy_retries_total_total{model="dummy-model",reason="queue_estimate_mismatch",routing_key=""} 28
stargate_proxy_retry_exhausted_total_total{model="dummy-model",reason="retry_budget_exhausted",routing_key=""} 4
"#;

        let summary = queue_admission_summary_delta_from_prometheus(baseline, post_replay);

        assert_eq!(summary.pylon_disabled_count, 96.0);
        assert_eq!(summary.stargate_queue_mismatch_retry_count, 26.0);
        assert_eq!(summary.stargate_retry_exhausted_count, 3.0);
        assert_eq!(
            summary.stargate_retry_exhausted_by_reason["retry_budget_exhausted"],
            3.0
        );
    }

    #[test]
    fn routing_selection_delta_excludes_pre_replay_probe_counters() {
        let baseline = r#"
stargate_routing_selections_total_total{algorithm="pulsar-multiregion",model="dummy-model",routing_key="",selection="primary"} 3
stargate_routing_selections_total_total{algorithm="pulsar-multiregion",model="dummy-model",routing_key="",selection="fallback"} 1
stargate_routing_kv_free_token_fallback_selections_total_total{algorithm="pulsar-multiregion",model="dummy-model",routing_key=""} 1
"#;
        let post_replay = r#"
stargate_routing_selections_total_total{algorithm="pulsar-multiregion",model="dummy-model",routing_key="",selection="primary"} 13
stargate_routing_selections_total_total{algorithm="pulsar-multiregion",model="dummy-model",routing_key="",selection="fallback"} 5
stargate_routing_kv_free_token_fallback_selections_total_total{algorithm="pulsar-multiregion",model="dummy-model",routing_key=""} 4
"#;

        let summary = routing_selection_summary_delta_from_prometheus(baseline, post_replay);

        assert_eq!(summary.primary_count, 10.0);
        assert_eq!(summary.fallback_count, 4.0);
        assert_eq!(summary.kv_free_token_fallback_count, 3.0);
    }
}

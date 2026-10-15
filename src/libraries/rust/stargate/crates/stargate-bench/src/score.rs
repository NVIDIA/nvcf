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
    pub successful_requests_per_second: Option<f64>,
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
    pub capacity_balance_score: Option<f64>,
    pub cluster_balance_score: Option<f64>,
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
    let mut summary = SummaryAccumulator::default();
    for result in results {
        summary.observe(result, topology);
    }
    summary.finish(results.len(), topology)
}

#[derive(Default)]
struct SummaryAccumulator {
    successes: usize,
    successful_output_tokens: u64,
    share_totals: [u64; 3],
    run_window_ms: Option<(u64, u64)>,
    ttft: Vec<u64>,
    ttlt: Vec<u64>,
    backends: BTreeMap<String, GroupAccumulator>,
    clusters: BTreeMap<String, GroupAccumulator>,
    clusters_by_cache_key: BTreeMap<String, BTreeSet<String>>,
    failures: BTreeMap<(u16, Option<String>, Option<String>), usize>,
    cache: CacheSummary,
}

#[derive(Default)]
struct GroupAccumulator {
    success_count: usize,
    input_tokens: u64,
    output_tokens: u64,
    successful_output_tokens: u64,
    ttlt: Vec<u64>,
    observed_cache: usize,
    cache_hits: usize,
    cache_eviction_count: u64,
    cache_evicted_tokens: u64,
}

impl SummaryAccumulator {
    fn observe(&mut self, result: &RequestResult, topology: &RoutingTopology) {
        let dispatch_ms = result.dispatch_offset_ms;
        let completion_ms = dispatch_ms.saturating_add(result.completion_ms);
        let run_window = self
            .run_window_ms
            .get_or_insert((dispatch_ms, completion_ms));
        run_window.0 = run_window.0.min(dispatch_ms);
        run_window.1 = run_window.1.max(completion_ms);
        self.ttlt.push(result.completion_ms);
        self.ttft.extend(result.first_output_ms);
        self.cache.observed_request_count += usize::from(result.kv_cache_hit.is_some());
        self.cache.hit_count += usize::from(result.kv_cache_hit == Some(true));
        self.cache.miss_count += usize::from(result.kv_cache_hit == Some(false));
        self.cache.eviction_count += result.kv_cache_evicted_entries.unwrap_or_default();
        self.cache.evicted_tokens += result.kv_cache_evicted_tokens.unwrap_or_default();
        self.cache.reused_input_tokens += result.kv_cache_reused_input_tokens.unwrap_or_default();
        self.cache.uncached_input_tokens +=
            result.kv_cache_uncached_input_tokens.unwrap_or_default();

        if result.ok {
            self.successes += 1;
            self.successful_output_tokens += result.output_tokens;
        } else {
            let failure = (
                result.status_code,
                result.selected_backend_id.clone(),
                result.error.clone(),
            );
            *self.failures.entry(failure).or_default() += 1;
        }

        let Some(backend_id) = &result.selected_backend_id else {
            return;
        };
        let [requests, input_tokens, output_tokens] = &mut self.share_totals;
        *input_tokens = input_tokens.saturating_add(result.input_tokens);
        if result.ok {
            *requests = requests.saturating_add(1);
            *output_tokens = output_tokens.saturating_add(result.output_tokens);
        }
        let cluster_id = topology
            .backend_cluster_ids
            .get(backend_id)
            .map_or(backend_id.as_str(), String::as_str);
        for (groups, id) in [
            (&mut self.backends, backend_id.as_str()),
            (&mut self.clusters, cluster_id),
        ] {
            groups.entry(id.to_string()).or_default().observe(result);
        }
        if result.ok
            && let Some(cache_key) = &result.cache_affinity_key
        {
            self.clusters_by_cache_key
                .entry(cache_key.clone())
                .or_default()
                .insert(cluster_id.to_string());
        }
    }

    fn finish(mut self, request_count: usize, topology: &RoutingTopology) -> RunSummary {
        self.ttft.sort_unstable();
        self.ttlt.sort_unstable();
        let total_length_ms = self
            .run_window_ms
            .map_or(0, |(first, last)| last.saturating_sub(first));
        let (backend_request_shares, backend_input_token_shares, backend_output_token_shares) =
            group_shares(&self.backends, self.share_totals);
        let (cluster_request_shares, cluster_input_token_shares, cluster_output_token_shares) =
            group_shares(&self.clusters, self.share_totals);
        let observed_input_tokens =
            self.cache.reused_input_tokens + self.cache.uncached_input_tokens;
        self.cache.hit_rate = ratio(self.cache.hit_count, self.cache.observed_request_count);
        self.cache.input_reuse_rate = (observed_input_tokens > 0)
            .then_some(self.cache.reused_input_tokens as f64 / observed_input_tokens as f64);
        let observed_cache_key_count = self.clusters_by_cache_key.len();
        let moved_cache_key_count = self
            .clusters_by_cache_key
            .values()
            .filter(|clusters| clusters.len() > 1)
            .count();

        RunSummary {
            request_count,
            success_rate: ratio(self.successes, request_count).unwrap_or_default(),
            successful_requests_per_second: per_second(self.successes as u64, total_length_ms),
            successful_output_tokens_per_second: per_second(
                self.successful_output_tokens,
                total_length_ms,
            ),
            avg_ttft_ms: average(&self.ttft),
            p50_ttft_ms: percentile(&self.ttft, 0.50),
            p95_ttft_ms: percentile(&self.ttft, 0.95),
            p99_ttft_ms: percentile(&self.ttft, 0.99),
            avg_ttlt_ms: average(&self.ttlt).unwrap_or(0.0),
            p50_ttlt_ms: percentile(&self.ttlt, 0.50).unwrap_or(0),
            p95_ttlt_ms: percentile(&self.ttlt, 0.95).unwrap_or(0),
            p99_ttlt_ms: percentile(&self.ttlt, 0.99).unwrap_or(0),
            max_ttlt_ms: self.ttlt.last().copied().unwrap_or_default(),
            total_length_ms,
            balance_score: (!backend_request_shares.is_empty())
                .then(|| equal_share_balance_score(&backend_request_shares)),
            capacity_balance_score: compared_balance_score(
                &backend_request_shares,
                &topology.backend_capacity_shares,
                expected_share_balance_score,
            ),
            cluster_balance_score: compared_balance_score(
                &cluster_request_shares,
                &topology.cluster_capacity_shares,
                equal_expected_share_balance_score,
            ),
            cluster_capacity_balance_score: compared_balance_score(
                &cluster_input_token_shares,
                &topology.cluster_capacity_shares,
                expected_share_balance_score,
            ),
            backend_request_shares,
            backend_capacity_shares: topology.backend_capacity_shares.clone(),
            backend_input_token_shares,
            backend_output_token_shares,
            backend_summaries: finish_groups(self.backends),
            cluster_request_shares,
            cluster_capacity_shares: topology.cluster_capacity_shares.clone(),
            cluster_input_token_shares,
            cluster_output_token_shares,
            cluster_summaries: finish_groups(self.clusters),
            cache_summary: self.cache,
            stickiness_summary: StickinessSummary {
                observed_cache_key_count,
                sticky_cache_key_count: observed_cache_key_count - moved_cache_key_count,
                moved_cache_key_count,
                movement_rate: ratio(moved_cache_key_count, observed_cache_key_count),
            },
            failure_summary: self
                .failures
                .into_iter()
                .map(
                    |((status_code, selected_backend_id, error), count)| FailureSummary {
                        status_code,
                        selected_backend_id,
                        error,
                        count,
                    },
                )
                .collect(),
            queue_admission_summary: QueueAdmissionSummary::default(),
            routing_selection_summary: RoutingSelectionSummary::default(),
        }
    }
}

impl GroupAccumulator {
    fn observe(&mut self, result: &RequestResult) {
        self.success_count += usize::from(result.ok);
        self.input_tokens += result.input_tokens;
        self.output_tokens += result.output_tokens;
        if result.ok {
            self.successful_output_tokens += result.output_tokens;
        }
        self.ttlt.push(result.completion_ms);
        self.observed_cache += usize::from(result.kv_cache_hit.is_some());
        self.cache_hits += usize::from(result.kv_cache_hit == Some(true));
        self.cache_eviction_count += result.kv_cache_evicted_entries.unwrap_or_default();
        self.cache_evicted_tokens += result.kv_cache_evicted_tokens.unwrap_or_default();
    }

    fn finish(mut self) -> BackendSummary {
        self.ttlt.sort_unstable();
        BackendSummary {
            request_count: self.ttlt.len(),
            success_count: self.success_count,
            input_tokens: self.input_tokens,
            output_tokens: self.output_tokens,
            avg_ttlt_ms: average(&self.ttlt),
            p95_ttlt_ms: percentile(&self.ttlt, 0.95),
            cache_hit_rate: ratio(self.cache_hits, self.observed_cache),
            cache_eviction_count: self.cache_eviction_count,
            cache_evicted_tokens: self.cache_evicted_tokens,
        }
    }
}

type GroupShares = BTreeMap<String, f64>;

fn shares(
    groups: &BTreeMap<String, GroupAccumulator>,
    total: u64,
    weight: fn(&GroupAccumulator) -> Option<u64>,
) -> GroupShares {
    if total == 0 {
        return BTreeMap::new();
    }
    groups
        .iter()
        .filter_map(|(id, group)| {
            weight(group).map(|weight| (id.clone(), weight as f64 / total as f64))
        })
        .collect()
}

fn group_shares(
    groups: &BTreeMap<String, GroupAccumulator>,
    totals: [u64; 3],
) -> (GroupShares, GroupShares, GroupShares) {
    let [requests, input_tokens, output_tokens] = totals;
    (
        shares(groups, requests, |group| {
            (group.success_count > 0).then_some(group.success_count as u64)
        }),
        shares(groups, input_tokens, |group| Some(group.input_tokens)),
        shares(groups, output_tokens, |group| {
            (group.success_count > 0).then_some(group.successful_output_tokens)
        }),
    )
}

fn finish_groups(groups: BTreeMap<String, GroupAccumulator>) -> BTreeMap<String, BackendSummary> {
    groups
        .into_iter()
        .map(|(id, group)| (id, group.finish()))
        .collect()
}

pub fn queue_admission_summary_from_prometheus(metrics: &str) -> QueueAdmissionSummary {
    let mut summary = QueueAdmissionSummary::default();
    for (name, series, value) in prometheus_counter_samples(metrics) {
        match name {
            "pylon_queue_admission_decisions" => {
                match prometheus_label_value(series, r#"result=""#) {
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
            "stargate_proxy_retries"
                if prometheus_label_value(series, r#"reason=""#)
                    == Some("queue_estimate_mismatch") =>
            {
                summary.stargate_queue_mismatch_retry_count += value;
            }
            "stargate_proxy_retry_exhausted" => {
                summary.stargate_retry_exhausted_count += value;
                let reason = prometheus_label_value(series, r#"reason=""#).unwrap_or("unlabeled");
                *summary
                    .stargate_retry_exhausted_by_reason
                    .entry(reason.to_string())
                    .or_default() += value;
            }
            _ => {}
        }
    }
    summary
}

macro_rules! counter_deltas {
    ($delta:ident, $baseline:ident, $($field:ident),+ $(,)?) => {
        $($delta.$field = counter_delta($delta.$field, $baseline.$field);)+
    };
}

pub fn queue_admission_summary_delta_from_prometheus(
    baseline_metrics: &str,
    post_replay_metrics: &str,
) -> QueueAdmissionSummary {
    let baseline = queue_admission_summary_from_prometheus(baseline_metrics);
    let mut delta = queue_admission_summary_from_prometheus(post_replay_metrics);
    counter_deltas!(
        delta,
        baseline,
        pylon_accepted_count,
        pylon_rejected_count,
        pylon_disabled_count,
        pylon_missing_estimate_count,
        pylon_unknown_local_estimate_count,
        stargate_queue_mismatch_retry_count,
        stargate_retry_exhausted_count,
    );
    let baseline_reasons = &baseline.stargate_retry_exhausted_by_reason;
    delta
        .stargate_retry_exhausted_by_reason
        .retain(|reason, count| {
            *count = counter_delta(
                *count,
                baseline_reasons.get(reason).copied().unwrap_or_default(),
            );
            *count > 0.0
        });
    delta
}

pub fn routing_selection_summary_from_prometheus(metrics: &str) -> RoutingSelectionSummary {
    let mut summary = RoutingSelectionSummary::default();
    for (name, series, value) in prometheus_counter_samples(metrics) {
        match name {
            "stargate_routing_selections" => match prometheus_label_value(series, r#"selection=""#)
            {
                Some("primary") => summary.primary_count += value,
                Some("fallback") => summary.fallback_count += value,
                _ => {}
            },
            "stargate_routing_kv_free_token_fallback_selections" => {
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
    let mut delta = routing_selection_summary_from_prometheus(post_replay_metrics);
    counter_deltas!(
        delta,
        baseline,
        primary_count,
        fallback_count,
        kv_free_token_fallback_count,
    );
    delta
}

fn counter_delta(post_replay: f64, baseline: f64) -> f64 {
    (post_replay - baseline).max(0.0)
}

fn prometheus_counter_samples(metrics: &str) -> impl Iterator<Item = (&str, &str, f64)> {
    metrics.lines().filter_map(|line| {
        let mut fields = line.split_whitespace();
        let series = fields.next().filter(|series| !series.starts_with('#'))?;
        let value = fields.next()?.parse::<f64>().ok()?;
        let name = series.split_once('{').map_or(series, |(name, _)| name);
        let name = name.strip_suffix("_total")?;
        Some((name.strip_suffix("_total").unwrap_or(name), series, value))
    })
}

fn prometheus_label_value<'a>(series: &'a str, needle: &str) -> Option<&'a str> {
    Some(series.split_once(needle)?.1.split_once('"')?.0)
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
    for capacity in backend_capacities
        .values_mut()
        .chain(cluster_capacities.values_mut())
    {
        *capacity /= total_capacity;
    }
    RoutingTopology {
        backend_capacity_shares: backend_capacities,
        cluster_capacity_shares: cluster_capacities,
        backend_cluster_ids,
    }
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

fn compared_balance_score(
    observed: &BTreeMap<String, f64>,
    expected: &BTreeMap<String, f64>,
    score: fn(&BTreeMap<String, f64>, &BTreeMap<String, f64>) -> f64,
) -> Option<f64> {
    (!observed.is_empty() && !expected.is_empty()).then(|| score(observed, expected))
}

fn equal_expected_share_balance_score(
    observed: &BTreeMap<String, f64>,
    expected_ids: &BTreeMap<String, f64>,
) -> f64 {
    let expected_share = 1.0 / expected_ids.len() as f64;
    let expected = expected_ids
        .keys()
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

fn per_second(value: u64, total_length_ms: u64) -> Option<f64> {
    (total_length_ms > 0).then_some(value as f64 * 1000.0 / total_length_ms as f64)
}

fn ratio(numerator: usize, denominator: usize) -> Option<f64> {
    (denominator > 0).then_some(numerator as f64 / denominator as f64)
}

fn average(values: &[u64]) -> Option<f64> {
    (!values.is_empty())
        .then(|| values.iter().map(|value| *value as f64).sum::<f64>() / values.len() as f64)
}

fn percentile(values: &[u64], q: f64) -> Option<u64> {
    let index = (values.len().checked_sub(1)? as f64 * q).round() as usize;
    values.get(index).copied()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{
        BackendProfile, BackendProfileGroup, RegistrationConfig, ServiceTimeConfig,
    };

    fn result(backend: &str, ttft: u64, ttlt: u64) -> RequestResult {
        RequestResult {
            request_index: 0,
            request_id: String::new(),
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

    fn with_tokens(
        mut result: RequestResult,
        input_tokens: u64,
        output_tokens: u64,
    ) -> RequestResult {
        result.input_tokens = input_tokens;
        result.output_tokens = output_tokens;
        result
    }

    fn clustered_backends(
        count: usize,
        pylons_per_cluster: usize,
        default_tps: f64,
        groups: &[(usize, f64)],
    ) -> BackendConfig {
        let profile = |last_mean_input_tps| BackendProfile {
            name: "test".to_string(),
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
                last_mean_input_tps,
            },
        };
        BackendConfig {
            count,
            cluster_id_template: Some("cluster-{cluster_index}".to_string()),
            pylons_per_cluster,
            profile: profile(default_tps),
            profiles: groups
                .iter()
                .map(|&(count, tps)| BackendProfileGroup {
                    count,
                    profile: profile(tps),
                })
                .collect(),
        }
    }

    #[test]
    fn summary_computes_basic_metrics() {
        let summary = summarize_with_capacity(
            &[
                result("a", 10, 20),
                result("b", 30, 40),
                result("a", 20, 50),
                result("b", 40, 60),
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
        let mut first = result("a", 10, 20);
        first.dispatch_offset_ms = 100;
        let mut second = result("a", 10, 40);
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
                result("a", 10, 20),
                result("a", 10, 20),
                result("a", 10, 20),
                result("b", 10, 20),
            ],
            expected,
        );

        assert_eq!(summary.capacity_balance_score, Some(1.0));
    }

    #[test]
    fn summary_computes_grouped_cluster_balance_and_goodput() {
        let backends = clustered_backends(4, 2, 1.0, &[(2, 100.0), (2, 50.0)]);
        let mut first = with_tokens(result("backend-0", 10, 1000), 100, 10);
        first.cache_affinity_key = Some("shared-prefix".to_string());
        let mut second = with_tokens(result("backend-1", 10, 1000), 100, 10);
        second.cache_affinity_key = Some("shared-prefix".to_string());
        let third = with_tokens(result("backend-2", 10, 1000), 50, 10);
        let fourth = with_tokens(result("backend-3", 10, 1000), 50, 10);

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
        let backends = clustered_backends(2, 1, 100.0, &[]);
        let served = with_tokens(result("backend-0", 10, 1000), 100, 10);
        let mut rejected = with_tokens(result("backend-1", 10, 1000), 100, 0);
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
    fn summary_saturates_input_share_denominator_across_backends() {
        let max = with_tokens(result("max", 10, 20), u64::MAX, 1);
        let one = with_tokens(result("one", 10, 20), 1, 1);

        let summary = summarize_with_topology(&[max, one], &RoutingTopology::default());

        assert_eq!(summary.backend_input_token_shares["max"], 1.0);
        assert_eq!(
            summary.backend_input_token_shares["one"],
            1.0 / u64::MAX as f64
        );
    }

    #[test]
    fn summary_computes_cache_metrics() {
        let mut hit = result("a", 10, 20);
        hit.kv_cache_hit = Some(true);
        hit.kv_cache_reused_input_tokens = Some(100_000);
        hit.kv_cache_uncached_input_tokens = Some(2_000);
        let mut miss = result("a", 10, 20);
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
        let mut first = with_tokens(result("a", 10, 20), 100, 10);
        first.kv_cache_hit = Some(true);
        let mut second = with_tokens(result("b", 10, 40), 300, 30);
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
        let mut first = result("a", 10, 20);
        first.cache_affinity_key = Some("cak-a".to_string());
        let mut second = result("b", 10, 20);
        second.cache_affinity_key = Some("cak-a".to_string());
        let mut failed = result("b", 10, 20);
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

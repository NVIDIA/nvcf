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

use std::path::PathBuf;
use std::process::Command as ProcessCommand;
use std::time::{Duration, Instant};

use anyhow::Context;

use crate::config::{
    AlgorithmConfig, BenchmarkConfig, DegradationActionConfig, DegradationActionKind,
};
use crate::driver::{DriveConfig, drive_manifest, load_manifest};
use crate::k8s::{
    apply as apply_k8s, collect_logs, delete as delete_k8s, delete_backend_pod,
    prepare_benchmark_k8s_run, scale_backend, stargate_metrics_endpoints, wait_ready,
};
use crate::manifest::{Manifest, ManifestRequest, write_manifest_json};
use crate::metadata::{
    BenchmarkTier, DriverMode, ReliabilityMode, collect_run_metadata, write_run_metadata,
};
use crate::report::{ReportContext, ReportEntry, render_markdown_report};
use crate::score::{
    RoutingTopology, RunSummary, queue_admission_summary_delta_from_prometheus,
    routing_selection_summary_delta_from_prometheus, summarize_with_topology, topology_for,
};

const COLLECTOR_SCRAPE_SETTLE_DELAY: Duration = Duration::from_millis(1_100);

pub fn run_k8s_benchmark(
    config: BenchmarkConfig,
    manifest: Manifest,
    output_dir: PathBuf,
    keep_resources_on_failure: bool,
    reliability_mode: ReliabilityMode,
) -> anyhow::Result<()> {
    std::fs::create_dir_all(&output_dir)
        .with_context(|| format!("failed to create output dir {}", output_dir.display()))?;
    let metadata = collect_run_metadata(
        BenchmarkTier::LocalK8sSmoke,
        reliability_mode,
        DriverMode::ExternalNodePort,
    );
    write_run_metadata(&output_dir.join("run-metadata.json"), &metadata)?;
    if metadata.preflight.should_fail {
        anyhow::bail!(
            "strict reliability preflight failed with {} failure(s); inspect {}",
            metadata.preflight.failure_count,
            output_dir.join("run-metadata.json").display()
        );
    }
    ensure_k8s_context()?;
    let manifest_path = output_dir.join("manifest.json");
    write_manifest_json(&manifest_path, &manifest)?;
    println!(
        "running benchmark '{}' with {} request(s), {} backend(s), {} stargate(s)",
        config.name, config.request_count, config.backends.count, config.stargates.count
    );
    println!("output directory: {}", output_dir.display());
    println!(
        "algorithms: {}",
        config
            .algorithms
            .iter()
            .map(|algorithm| algorithm.name.as_str())
            .collect::<Vec<_>>()
            .join(", ")
    );

    let mut comparison = Vec::with_capacity(config.algorithms.len());
    let mut report_entries = Vec::with_capacity(config.algorithms.len());
    let topology = topology_for(&config.backends);
    for (run_index, algorithm) in config.algorithms.iter().enumerate() {
        println!(
            "starting algorithm {}/{}: {}",
            run_index + 1,
            config.algorithms.len(),
            algorithm.name
        );
        let run =
            prepare_benchmark_k8s_run(&config, algorithm, &manifest_path, &output_dir, run_index)?;
        apply_k8s(&run)?;
        let run_result = run_single_k8s(
            &run,
            manifest.max_concurrency,
            config.backends.count,
            &topology,
            &config.degradation.actions,
        );
        if (run_result.is_err() || keep_resources_on_failure)
            && let Err(error) = collect_logs(&run)
        {
            eprintln!(
                "warning: failed to collect k8s benchmark logs for {}: {error}",
                run.algorithm_name
            );
        }
        if keep_resources_on_failure && run_result.is_err() {
            eprintln!(
                "keeping k8s benchmark resources for failed run {}",
                run.algorithm_name
            );
        } else {
            let teardown_result = delete_k8s(&run);
            if let Err(error) = teardown_result {
                eprintln!(
                    "warning: failed to delete k8s benchmark resources for {}: {error}",
                    run.algorithm_name,
                );
            }
        }
        let summary = run_result?;
        println!(
            "finished {}: success_rate={:.3}, avg_ttlt_ms={:.1}, run_dir={}",
            run.algorithm_name,
            summary.success_rate,
            summary.avg_ttlt_ms,
            run.run_dir.display()
        );
        let algorithm_name = run.algorithm_name.clone();
        comparison.push(comparison_entry(algorithm, &summary));
        report_entries.push(ReportEntry {
            algorithm_name,
            pylon_queue_admission: algorithm.pylon_queue_admission.clone(),
            summary,
        });
    }

    let comparison_path = output_dir.join("comparison.json");
    let comparison_bytes = serde_json::to_vec_pretty(&comparison)
        .context("failed to serialize benchmark comparison")?;
    std::fs::write(&comparison_path, comparison_bytes)
        .with_context(|| format!("failed to write {}", comparison_path.display()))?;
    let report_path = output_dir.join("report.md");
    let report = render_markdown_report(&ReportContext::from_config(&config), &report_entries);
    std::fs::write(&report_path, report)
        .with_context(|| format!("failed to write {}", report_path.display()))?;
    println!("completed {} algorithm runs", comparison.len());
    Ok(())
}

pub(crate) fn comparison_entry(
    algorithm: &AlgorithmConfig,
    summary: &RunSummary,
) -> serde_json::Value {
    serde_json::json!({
        "algorithm_name": algorithm.name,
        "pylon_queue_admission": algorithm.pylon_queue_admission,
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

fn ensure_k8s_context() -> anyhow::Result<()> {
    let output = ProcessCommand::new("kubectl")
        .arg("config")
        .arg("current-context")
        .output()
        .context("failed to query current kubectl context")?;
    if !output.status.success() || String::from_utf8_lossy(&output.stdout).trim().is_empty() {
        anyhow::bail!(
            "no active kubectl context; configure access to a Kubernetes cluster before running Kubernetes benchmarks"
        );
    }
    Ok(())
}

fn run_single_k8s(
    run: &crate::k8s::BenchmarkK8sRun,
    concurrency_limit: usize,
    backend_count: usize,
    topology: &RoutingTopology,
    degradation_actions: &[DegradationActionConfig],
) -> anyhow::Result<RunSummary> {
    wait_ready(run, backend_count)?;
    let runtime = tokio::runtime::Runtime::new().context("failed to create tokio runtime")?;
    let manifest = load_manifest(&run.manifest_path)?;
    let routing_probe_request = manifest
        .requests
        .first()
        .ok_or_else(|| anyhow::anyhow!("benchmark manifest must contain at least one request"))?;
    runtime.block_on(wait_for_http_ok(
        &format!("{}/healthz", run.stargate_http_endpoint),
        Duration::from_secs(60),
    ))?;
    let metrics_endpoints = stargate_metrics_endpoints(run)?;
    runtime.block_on(wait_for_active_backend_counts(
        &metrics_endpoints,
        &manifest.model,
        routing_probe_request.routing_key.as_deref(),
        backend_count,
        Duration::from_secs(60),
    ))?;
    runtime.block_on(wait_for_routing(
        &format!("{}/v1/chat/completions", run.stargate_http_endpoint),
        &manifest.model,
        routing_probe_request,
        Duration::from_secs(60),
    ))?;
    let collector_baseline = runtime.block_on(wait_for_scraped_benchmark_metrics(
        &run.collector_metrics_endpoint,
        Duration::from_secs(60),
    ))?;
    let baseline_request_totals = scraped_request_totals(&collector_baseline)
        .context("collector baseline did not expose Stargate and Pylon request counters")?;
    let collector_baseline_path = run.run_dir.join("collector-baseline-metrics.prom");
    std::fs::write(&collector_baseline_path, &collector_baseline)
        .with_context(|| format!("failed to write {}", collector_baseline_path.display()))?;

    let results_path = run.run_dir.join("requests.jsonl");
    let degradation_handles =
        start_degradation_actions(run, &manifest.requests, degradation_actions);
    let results = runtime.block_on(drive_manifest(
        DriveConfig {
            endpoint: format!("{}/v1/chat/completions", run.stargate_http_endpoint),
            output_path: results_path,
            concurrency_limit,
        },
        manifest,
    ));
    join_degradation_actions(degradation_handles);
    let results = results?;
    let successful_request_count = results.iter().filter(|result| result.ok).count();

    let mut summary = summarize_with_topology(&results, topology);

    if let Ok(metrics) = runtime.block_on(fetch_text(&run.stargate_metrics_endpoint)) {
        let metrics_path = run.run_dir.join("metrics.prom");
        std::fs::write(&metrics_path, metrics)
            .with_context(|| format!("failed to write {}", metrics_path.display()))?;
    }

    let collector_metrics = runtime.block_on(wait_for_post_replay_scraped_benchmark_metrics(
        &run.collector_metrics_endpoint,
        baseline_request_totals,
        results.len(),
        successful_request_count,
        Duration::from_secs(60),
    ))?;
    let collector_metrics_path = run.run_dir.join("collector-metrics.prom");
    std::fs::write(&collector_metrics_path, &collector_metrics)
        .with_context(|| format!("failed to write {}", collector_metrics_path.display()))?;
    summary.queue_admission_summary =
        queue_admission_summary_delta_from_prometheus(&collector_baseline, &collector_metrics);
    summary.routing_selection_summary =
        routing_selection_summary_delta_from_prometheus(&collector_baseline, &collector_metrics);
    let summary_path = run.run_dir.join("summary.json");
    let summary_bytes =
        serde_json::to_vec_pretty(&summary).context("failed to serialize run summary")?;
    std::fs::write(&summary_path, summary_bytes)
        .with_context(|| format!("failed to write {}", summary_path.display()))?;

    Ok(summary)
}

fn start_degradation_actions(
    run: &crate::k8s::BenchmarkK8sRun,
    requests: &[ManifestRequest],
    actions: &[DegradationActionConfig],
) -> Vec<std::thread::JoinHandle<()>> {
    actions
        .iter()
        .map(|action| {
            let action = action.clone();
            let run = run.clone();
            let delay = requests
                .get(action.at_request)
                .map(|request| Duration::from_millis(request.scheduled_offset_ms))
                .unwrap_or_default();
            std::thread::spawn(move || {
                std::thread::sleep(delay);
                let result = match action.action {
                    DegradationActionKind::DeleteBackendPod => {
                        delete_backend_pod(&run, action.backend_index)
                    }
                    DegradationActionKind::ScaleBackend { replicas } => {
                        scale_backend(&run, action.backend_index, replicas)
                    }
                };
                if let Err(error) = result {
                    eprintln!(
                        "warning: degradation action failed for backend-{} in {}: {error}",
                        action.backend_index, run.algorithm_name
                    );
                }
            })
        })
        .collect()
}

fn join_degradation_actions(handles: Vec<std::thread::JoinHandle<()>>) {
    for handle in handles {
        let _ = handle.join();
    }
}

async fn wait_for_http_ok(url: &str, timeout: Duration) -> anyhow::Result<()> {
    let deadline = Instant::now() + timeout;
    let client = reqwest::Client::new();
    loop {
        if let Ok(response) = client.get(url).send().await
            && response.status().is_success()
        {
            return Ok(());
        }
        if Instant::now() >= deadline {
            anyhow::bail!("timed out waiting for {}", url);
        }
        tokio::time::sleep(Duration::from_millis(250)).await;
    }
}

async fn fetch_text(url: &str) -> anyhow::Result<String> {
    let client = reqwest::Client::new();
    let response = client
        .get(url)
        .send()
        .await
        .with_context(|| format!("failed to fetch {}", url))?;
    response
        .text()
        .await
        .with_context(|| format!("failed to read response body from {}", url))
}

async fn wait_for_scraped_benchmark_metrics(
    collector_metrics_endpoint: &str,
    timeout: Duration,
) -> anyhow::Result<String> {
    let deadline = Instant::now() + timeout;
    let mut last_metrics_len = 0usize;
    loop {
        if let Ok(metrics) = fetch_text(collector_metrics_endpoint).await {
            last_metrics_len = metrics.len();
            if has_scraped_benchmark_metrics(&metrics) {
                return Ok(metrics);
            }
        }
        if Instant::now() >= deadline {
            anyhow::bail!(
                "timed out waiting for OTel collector to scrape benchmark metrics from {} (last_response_bytes={})",
                collector_metrics_endpoint,
                last_metrics_len
            );
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
}

async fn wait_for_post_replay_scraped_benchmark_metrics(
    collector_metrics_endpoint: &str,
    baseline: ScrapedRequestTotals,
    replay_request_count: usize,
    replay_success_count: usize,
    timeout: Duration,
) -> anyhow::Result<String> {
    let started_at = Instant::now();
    let deadline = started_at + timeout;
    let mut last_metrics_len = 0usize;
    loop {
        if let Ok(metrics) = fetch_text(collector_metrics_endpoint).await {
            last_metrics_len = metrics.len();
            if started_at.elapsed() >= COLLECTOR_SCRAPE_SETTLE_DELAY
                && has_post_replay_scraped_benchmark_metrics(
                    &metrics,
                    baseline,
                    replay_request_count,
                    replay_success_count,
                )
            {
                return Ok(metrics);
            }
        }
        if Instant::now() >= deadline {
            anyhow::bail!(
                "timed out waiting for post-replay OTel collector metrics from {} (last_response_bytes={})",
                collector_metrics_endpoint,
                last_metrics_len
            );
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
}

pub(crate) fn has_scraped_benchmark_metrics(metrics: &str) -> bool {
    has_any_metric(
        metrics,
        &["stargate_requests_total", "stargate_requests_total_total"],
    ) && has_any_metric(
        metrics,
        &["pylon_requests_total", "pylon_requests_total_total"],
    )
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub(crate) struct ScrapedRequestTotals {
    stargate: f64,
    pylon: f64,
}

pub(crate) fn scraped_request_totals(metrics: &str) -> Option<ScrapedRequestTotals> {
    if !has_scraped_benchmark_metrics(metrics) {
        return None;
    }
    Some(ScrapedRequestTotals {
        stargate: metric_total(
            metrics,
            &["stargate_requests_total", "stargate_requests_total_total"],
        ),
        pylon: metric_total(
            metrics,
            &["pylon_requests_total", "pylon_requests_total_total"],
        ),
    })
}

pub(crate) fn has_post_replay_scraped_benchmark_metrics(
    metrics: &str,
    baseline: ScrapedRequestTotals,
    replay_request_count: usize,
    replay_success_count: usize,
) -> bool {
    let Some(current) = scraped_request_totals(metrics) else {
        return false;
    };
    current.stargate >= baseline.stargate + replay_request_count as f64
        && current.pylon >= baseline.pylon + replay_success_count as f64
}

fn metric_total(metrics: &str, names: &[&str]) -> f64 {
    metrics
        .lines()
        .filter_map(|line| {
            let mut fields = line.split_whitespace();
            let series = fields.next()?;
            if !names
                .iter()
                .any(|name| series.starts_with(&format!("{name}{{")) || series == *name)
            {
                return None;
            }
            fields.next()?.parse::<f64>().ok()
        })
        .sum()
}

fn has_any_metric(metrics: &str, names: &[&str]) -> bool {
    metrics.lines().any(|line| {
        names.iter().any(|name| {
            line.starts_with(&format!("{name}{{")) || line.starts_with(&format!("{name} "))
        })
    })
}

async fn wait_for_active_backend_counts(
    metrics_endpoints: &[String],
    model: &str,
    routing_key: Option<&str>,
    expected_count: usize,
    timeout: Duration,
) -> anyhow::Result<()> {
    let deadline = Instant::now() + timeout;
    let mut last_counts = Vec::new();
    loop {
        last_counts.clear();
        for metrics_endpoint in metrics_endpoints {
            let count = match fetch_text(metrics_endpoint).await {
                Ok(metrics) => active_backend_count(&metrics, model, routing_key),
                Err(_) => None,
            };
            last_counts.push(count);
        }
        if active_backend_counts_ready(&last_counts, expected_count) {
            return Ok(());
        }
        if Instant::now() >= deadline {
            anyhow::bail!(
                "timed out waiting for {expected_count} active benchmark backends on every stargate metrics endpoint {:?} (model={}, routing_key={:?}, last_counts={:?})",
                metrics_endpoints,
                model,
                routing_key,
                last_counts
            );
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
}

pub(crate) fn active_backend_counts_ready(counts: &[Option<usize>], expected_count: usize) -> bool {
    !counts.is_empty()
        && counts
            .iter()
            .all(|count| count.is_some_and(|count| count >= expected_count))
}

pub(crate) fn active_backend_count(
    metrics: &str,
    model: &str,
    routing_key: Option<&str>,
) -> Option<usize> {
    metrics.lines().find_map(|line| {
        if !line.starts_with("stargate_active_inference_servers{") {
            return None;
        }
        let (metric, value) = line.rsplit_once(' ')?;
        let metric_model = prometheus_label_value(metric, "model")?;
        let metric_routing_key = prometheus_label_value(metric, "routing_key").unwrap_or("");
        if metric_model != model || metric_routing_key != routing_key.unwrap_or("") {
            return None;
        }
        value.parse::<f64>().ok().map(|value| value as usize)
    })
}

fn prometheus_label_value<'a>(metric: &'a str, label: &str) -> Option<&'a str> {
    let needle = format!(r#"{label}=""#);
    let start = metric.find(&needle)? + needle.len();
    let rest = &metric[start..];
    let end = rest.find('"')?;
    Some(&rest[..end])
}

async fn wait_for_routing(
    endpoint: &str,
    model: &str,
    request: &ManifestRequest,
    timeout: Duration,
) -> anyhow::Result<()> {
    let deadline = Instant::now() + timeout;
    let client = reqwest::Client::new();
    let body = serde_json::json!({
        "model": model,
        "messages": [{"role": "user", "content": "benchmark-ready"}],
        "max_tokens": 1,
        "stream": true,
    });
    let mut last_status = None;
    let probe_cache_affinity_key = routing_probe_cache_affinity_key(request);
    loop {
        let mut builder = client
            .post(endpoint)
            .header("content-type", "application/json")
            .header("x-model", model)
            .header(
                "x-request-id",
                format!("benchmark-ready-probe-{}", request.request_index),
            )
            .header("x-input-tokens", "1")
            .header("x-output-tokens", "1");
        if let Some(routing_key) = &request.routing_key {
            builder = builder.header("x-routing-key", routing_key);
        }
        if let Some(cache_affinity_key) = &probe_cache_affinity_key {
            builder = builder.header("x-cache-affinity-key", cache_affinity_key);
        }
        match builder.json(&body).send().await {
            Ok(response) if response.status().is_success() => return Ok(()),
            Ok(response) => {
                last_status = Some(response.status());
            }
            Err(_) => {}
        }
        if Instant::now() >= deadline {
            anyhow::bail!(
                "timed out waiting for routable benchmark traffic on {} (model={}, routing_key={:?}, cache_affinity_key={:?}, last_status={:?})",
                endpoint,
                model,
                request.routing_key,
                probe_cache_affinity_key,
                last_status
            );
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
}

pub(crate) fn routing_probe_cache_affinity_key(request: &ManifestRequest) -> Option<String> {
    request.cache_affinity_key.as_ref().map(|_| {
        format!(
            "__stargate_bench_benchmark-ready-probe-{}",
            request.request_index
        )
    })
}

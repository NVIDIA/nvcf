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

use std::path::{Path, PathBuf};
use std::process::Command as ProcessCommand;
use std::time::{Duration, Instant};

use anyhow::Context;

use crate::config::{BenchmarkConfig, DegradationActionConfig, DegradationActionKind};
use crate::driver::{DriveConfig, RequestResult, drive_manifest, load_manifest};
use crate::k8s::{BenchmarkK8sRun, Kubectl, prepare_benchmark_k8s_run};
use crate::manifest::{Manifest, ManifestRequest, write_manifest_json};
use crate::metadata::{
    BenchmarkTier, DriverMode, ReliabilityMode, collect_run_metadata, write_run_metadata,
};
use crate::report::{ReportContext, ReportEntry, write_benchmark_report_artifacts};
use crate::score::{
    RoutingTopology, RunSummary, queue_admission_summary_delta_from_prometheus,
    routing_selection_summary_delta_from_prometheus, summarize_with_topology, topology_for,
};

const COLLECTOR_SCRAPE_SETTLE_DELAY: Duration = Duration::from_millis(1_100);
const K8S_READINESS_TIMEOUT: Duration = Duration::from_secs(60);

struct K8sRunArtifacts<'a>(&'a Path);

impl K8sRunArtifacts<'_> {
    fn path(&self, filename: &str) -> PathBuf {
        self.0.join(filename)
    }

    fn write(&self, filename: &str, content: impl AsRef<[u8]>) -> anyhow::Result<()> {
        let path = self.path(filename);
        std::fs::write(&path, content)
            .with_context(|| format!("failed to write {}", path.display()))
    }
}

fn load_k8s_replay(manifest_path: &Path) -> anyhow::Result<(Manifest, ManifestRequest)> {
    let manifest = load_manifest(manifest_path)?;
    let probe = manifest
        .requests
        .first()
        .cloned()
        .context("benchmark manifest must contain at least one request")?;
    Ok((manifest, probe))
}

struct K8sCollectorBaseline {
    metrics: String,
    request_totals: ScrapedRequestTotals,
}

pub fn run_k8s_benchmark(
    config: BenchmarkConfig,
    manifest: Manifest,
    output_dir: PathBuf,
    keep_resources_on_failure: bool,
    reliability_mode: ReliabilityMode,
) -> anyhow::Result<()> {
    let metadata = collect_run_metadata(
        BenchmarkTier::LocalK8sSmoke,
        reliability_mode,
        DriverMode::ExternalNodePort,
    );
    run_k8s_benchmark_with_context(
        config,
        manifest,
        output_dir,
        keep_resources_on_failure,
        metadata,
        ensure_k8s_context,
    )
}

fn run_k8s_benchmark_with_context(
    config: BenchmarkConfig,
    manifest: Manifest,
    output_dir: PathBuf,
    keep_resources_on_failure: bool,
    metadata: crate::metadata::RunMetadata,
    ensure_context: impl FnOnce() -> anyhow::Result<()>,
) -> anyhow::Result<()> {
    std::fs::create_dir_all(&output_dir)
        .with_context(|| format!("failed to create output dir {}", output_dir.display()))?;
    let metadata_path = output_dir.join("run-metadata.json");
    write_run_metadata(&metadata_path, &metadata)?;
    if metadata.preflight.should_fail {
        anyhow::bail!(
            "strict reliability preflight failed with {} failure(s); inspect {}",
            metadata.preflight.failure_count,
            metadata_path.display()
        );
    }
    ensure_context()?;
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
        Kubectl::default().apply(&run)?;
        let run_result = run_single_k8s(
            &run,
            manifest.max_concurrency,
            config.backends.count,
            &topology,
            &config.degradation.actions,
        );
        finalize_k8s_run_with_actions(
            &run.algorithm_name,
            run_result.is_err(),
            keep_resources_on_failure,
            || Kubectl::default().collect_logs(&run),
            || Kubectl::default().delete(&run),
        );
        let summary = run_result?;
        println!(
            "finished {}: success_rate={:.3}, avg_ttlt_ms={:.1}, run_dir={}",
            run.algorithm_name,
            summary.success_rate,
            summary.avg_ttlt_ms,
            run.run_dir.display()
        );
        report_entries.push(ReportEntry {
            algorithm_name: run.algorithm_name.clone(),
            pylon_queue_admission: algorithm.pylon_queue_admission.clone(),
            summary,
        });
    }
    write_benchmark_report_artifacts(
        &output_dir,
        &ReportContext::from_config(&config),
        &report_entries,
    )?;
    println!("completed {} algorithm runs", report_entries.len());
    Ok(())
}

fn finalize_k8s_run_with_actions(
    algorithm_name: &str,
    run_failed: bool,
    keep_resources_on_failure: bool,
    mut collect_logs_action: impl FnMut() -> anyhow::Result<()>,
    mut delete_resources_action: impl FnMut() -> anyhow::Result<()>,
) {
    if (run_failed || keep_resources_on_failure)
        && let Err(error) = collect_logs_action()
    {
        eprintln!("warning: failed to collect k8s benchmark logs for {algorithm_name}: {error}");
    }

    if run_failed && keep_resources_on_failure {
        eprintln!("keeping k8s benchmark resources for failed run {algorithm_name}");
    } else if let Err(error) = delete_resources_action() {
        eprintln!(
            "warning: failed to delete k8s benchmark resources for {algorithm_name}: {error}"
        );
    }
}

fn ensure_k8s_context() -> anyhow::Result<()> {
    let output = ProcessCommand::new("kubectl")
        .args(["config", "current-context"])
        .output()
        .context("failed to query current kubectl context")?;
    anyhow::ensure!(
        output.status.success() && !String::from_utf8_lossy(&output.stdout).trim().is_empty(),
        "no active kubectl context; configure access to a Kubernetes cluster before running Kubernetes benchmarks"
    );
    Ok(())
}

fn run_single_k8s(
    run: &BenchmarkK8sRun,
    concurrency_limit: usize,
    backend_count: usize,
    topology: &RoutingTopology,
    degradation_actions: &[DegradationActionConfig],
) -> anyhow::Result<RunSummary> {
    Kubectl::default()
        .wait_ready(run, backend_count)
        .and_then(|()| {
            execute_k8s_replay(
                run,
                concurrency_limit,
                backend_count,
                topology,
                degradation_actions,
            )
        })
}

fn execute_k8s_replay(
    run: &BenchmarkK8sRun,
    concurrency_limit: usize,
    backend_count: usize,
    topology: &RoutingTopology,
    degradation_actions: &[DegradationActionConfig],
) -> anyhow::Result<RunSummary> {
    let runtime = tokio::runtime::Runtime::new().context("failed to create tokio runtime")?;
    let (manifest, routing_probe) = load_k8s_replay(&run.manifest_path)?;
    let metrics_endpoints = Kubectl::default().stargate_metrics_endpoints(run)?;
    runtime.block_on(wait_for_k8s_readiness(
        &format!("{}/healthz", run.stargate_http_endpoint),
        &metrics_endpoints,
        &format!("{}/v1/chat/completions", run.stargate_http_endpoint),
        &manifest.model,
        backend_count,
        &routing_probe,
        K8S_READINESS_TIMEOUT,
    ))?;
    let artifacts = K8sRunArtifacts(&run.run_dir);
    let baseline = capture_collector_baseline(&runtime, run, &artifacts)?;
    let results = drive_k8s_replay(
        &runtime,
        run,
        manifest,
        concurrency_limit,
        degradation_actions,
        &artifacts,
    )?;
    write_k8s_run_summary(&runtime, run, topology, &results, baseline, &artifacts)
}

async fn wait_for_k8s_readiness(
    health_url: &str,
    metrics_endpoints: &[String],
    routing_url: &str,
    model: &str,
    expected_backend_count: usize,
    routing_probe: &ManifestRequest,
    timeout: Duration,
) -> anyhow::Result<()> {
    wait_for_http_ok(health_url, timeout).await?;
    wait_for_active_backend_counts(
        metrics_endpoints,
        model,
        routing_probe.routing_key.as_deref(),
        expected_backend_count,
        timeout,
    )
    .await?;
    wait_for_routing(routing_url, model, routing_probe, timeout).await
}

fn capture_collector_baseline(
    runtime: &tokio::runtime::Runtime,
    run: &BenchmarkK8sRun,
    artifacts: &K8sRunArtifacts,
) -> anyhow::Result<K8sCollectorBaseline> {
    let metrics = runtime.block_on(wait_for_collector_metrics(
        &run.collector_metrics_endpoint,
        Duration::from_secs(60),
        "OTel collector to scrape benchmark metrics",
        |metrics, _| has_scraped_benchmark_metrics(metrics),
    ))?;
    let request_totals = scraped_request_totals(&metrics)
        .context("collector baseline did not expose Stargate and Pylon request counters")?;
    artifacts.write("collector-baseline-metrics.prom", &metrics)?;
    Ok(K8sCollectorBaseline {
        metrics,
        request_totals,
    })
}

fn drive_k8s_replay(
    runtime: &tokio::runtime::Runtime,
    run: &BenchmarkK8sRun,
    manifest: Manifest,
    concurrency_limit: usize,
    degradation_actions: &[DegradationActionConfig],
    artifacts: &K8sRunArtifacts,
) -> anyhow::Result<Vec<RequestResult>> {
    let degradation_handles =
        start_degradation_actions(run, &manifest.requests, degradation_actions);
    let results = runtime.block_on(drive_manifest(
        DriveConfig {
            endpoint: format!("{}/v1/chat/completions", run.stargate_http_endpoint),
            output_path: artifacts.path("requests.jsonl"),
            concurrency_limit,
        },
        manifest,
    ));
    for handle in degradation_handles {
        let _ = handle.join();
    }
    results
}

fn write_k8s_run_summary(
    runtime: &tokio::runtime::Runtime,
    run: &BenchmarkK8sRun,
    topology: &RoutingTopology,
    results: &[RequestResult],
    collector_baseline: K8sCollectorBaseline,
    artifacts: &K8sRunArtifacts,
) -> anyhow::Result<RunSummary> {
    let successful_request_count = results.iter().filter(|result| result.ok).count();
    let mut summary = summarize_with_topology(results, topology);

    if let Ok(metrics) = runtime.block_on(fetch_text(&run.stargate_metrics_endpoint)) {
        artifacts.write("metrics.prom", metrics)?;
    }

    let collector_metrics = runtime.block_on(wait_for_collector_metrics(
        &run.collector_metrics_endpoint,
        Duration::from_secs(60),
        "post-replay OTel collector metrics",
        |metrics, elapsed| {
            elapsed >= COLLECTOR_SCRAPE_SETTLE_DELAY
                && has_post_replay_scraped_benchmark_metrics(
                    metrics,
                    collector_baseline.request_totals,
                    results.len(),
                    successful_request_count,
                )
        },
    ))?;
    artifacts.write("collector-metrics.prom", &collector_metrics)?;
    summary.queue_admission_summary = queue_admission_summary_delta_from_prometheus(
        &collector_baseline.metrics,
        &collector_metrics,
    );
    summary.routing_selection_summary = routing_selection_summary_delta_from_prometheus(
        &collector_baseline.metrics,
        &collector_metrics,
    );
    artifacts.write(
        "summary.json",
        serde_json::to_vec_pretty(&summary).context("failed to serialize run summary")?,
    )?;

    Ok(summary)
}

fn start_degradation_actions(
    run: &BenchmarkK8sRun,
    requests: &[ManifestRequest],
    actions: &[DegradationActionConfig],
) -> Vec<std::thread::JoinHandle<()>> {
    actions
        .iter()
        .cloned()
        .map(|action| {
            let run = run.clone();
            let delay = requests
                .get(action.at_request)
                .map(|request| Duration::from_millis(request.scheduled_offset_ms))
                .unwrap_or_default();
            std::thread::spawn(move || {
                std::thread::sleep(delay);
                let result = match action.action {
                    DegradationActionKind::DeleteBackendPod => {
                        Kubectl::default().delete_backend_pod(&run, action.backend_index)
                    }
                    DegradationActionKind::ScaleBackend { replicas } => {
                        Kubectl::default().scale_backend(&run, action.backend_index, replicas)
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
    reqwest::get(url)
        .await
        .with_context(|| format!("failed to fetch {}", url))?
        .text()
        .await
        .with_context(|| format!("failed to read response body from {}", url))
}

async fn wait_for_collector_metrics(
    endpoint: &str,
    timeout: Duration,
    description: &str,
    ready: impl Fn(&str, Duration) -> bool,
) -> anyhow::Result<String> {
    let started = Instant::now();
    let mut last_metrics_len = 0usize;
    loop {
        if let Ok(metrics) = fetch_text(endpoint).await {
            last_metrics_len = metrics.len();
            if ready(&metrics, started.elapsed()) {
                return Ok(metrics);
            }
        }
        if started.elapsed() >= timeout {
            anyhow::bail!(
                "timed out waiting for {description} from {endpoint} (last_response_bytes={last_metrics_len})"
            );
        }
        tokio::time::sleep(Duration::from_millis(500)).await;
    }
}

pub(crate) fn has_scraped_benchmark_metrics(metrics: &str) -> bool {
    scraped_request_totals(metrics).is_some()
}

#[derive(Debug, Clone, Copy, PartialEq)]
pub(crate) struct ScrapedRequestTotals {
    stargate: f64,
    pylon: f64,
}

pub(crate) fn scraped_request_totals(metrics: &str) -> Option<ScrapedRequestTotals> {
    let (has_stargate, stargate) = metric_total(
        metrics,
        &["stargate_requests_total", "stargate_requests_total_total"],
    );
    let (has_pylon, pylon) = metric_total(
        metrics,
        &["pylon_requests_total", "pylon_requests_total_total"],
    );
    (has_stargate && has_pylon).then_some(ScrapedRequestTotals { stargate, pylon })
}

pub(crate) fn has_post_replay_scraped_benchmark_metrics(
    metrics: &str,
    baseline: ScrapedRequestTotals,
    replay_request_count: usize,
    replay_success_count: usize,
) -> bool {
    scraped_request_totals(metrics).is_some_and(|current| {
        current.stargate >= baseline.stargate + replay_request_count as f64
            && current.pylon >= baseline.pylon + replay_success_count as f64
    })
}

fn metric_total(metrics: &str, names: &[&str]) -> (bool, f64) {
    let mut found = false;
    let mut total = 0.0;
    for line in metrics.lines() {
        found |= names.iter().any(|name| {
            line.strip_prefix(name)
                .is_some_and(|rest| rest.starts_with('{') || rest.starts_with(' '))
        });
        let mut fields = line.split_whitespace();
        let Some(series) = fields.next() else {
            continue;
        };
        let name = series.split_once('{').map_or(series, |(name, _)| name);
        if names.contains(&name) {
            total += fields
                .next()
                .and_then(|value| value.parse::<f64>().ok())
                .unwrap_or_default();
        }
    }
    (found, total)
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
            let count = fetch_text(metrics_endpoint)
                .await
                .ok()
                .and_then(|metrics| active_backend_count(&metrics, model, routing_key));
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
        line.strip_prefix("stargate_active_inference_servers{")?;
        let (metric, value) = line.rsplit_once(' ')?;
        let matches = prometheus_label_value(metric, "model")? == model
            && prometheus_label_value(metric, "routing_key").unwrap_or("")
                == routing_key.unwrap_or("");
        matches
            .then_some(value)?
            .parse()
            .ok()
            .map(|value: f64| value as usize)
    })
}

fn prometheus_label_value<'a>(metric: &'a str, label: &str) -> Option<&'a str> {
    let needle = format!(r#"{label}=""#);
    let (_, rest) = metric.split_once(&needle)?;
    Some(rest.split_once('"')?.0)
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
            .header("x-input-tokens", "15")
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

#[cfg(test)]
mod tests {
    use std::cell::RefCell;
    use std::path::Path;

    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    #[test]
    fn k8s_run_finalization_policy_matches_existing_cleanup_truth_table() {
        for (failed, keep, expected) in [
            (true, false, &["logs", "delete"][..]),
            (true, true, &["logs"][..]),
            (false, true, &["logs", "delete"][..]),
            (false, false, &["delete"][..]),
        ] {
            assert_eq!(recorded_finalization_actions(failed, keep), expected);
        }
    }

    #[test]
    fn k8s_run_finalization_executes_selected_cleanup_actions() {
        assert_eq!(
            recorded_finalization_actions(true, false),
            ["logs", "delete"]
        );
    }

    #[test]
    fn k8s_run_finalization_keeps_cleanup_errors_non_fatal() {
        let actions = RefCell::new(Vec::new());

        finalize_k8s_run_with_actions(
            "power-of-two",
            true,
            false,
            || {
                actions.borrow_mut().push("logs");
                Err(anyhow::anyhow!("log collection failed"))
            },
            || {
                actions.borrow_mut().push("delete");
                Err(anyhow::anyhow!("resource deletion failed"))
            },
        );

        assert_eq!(actions.into_inner(), ["logs", "delete"]);
    }

    fn recorded_finalization_actions(
        run_failed: bool,
        keep_resources_on_failure: bool,
    ) -> Vec<&'static str> {
        let actions = RefCell::new(Vec::new());
        finalize_k8s_run_with_actions(
            "power-of-two",
            run_failed,
            keep_resources_on_failure,
            || {
                actions.borrow_mut().push("logs");
                Ok(())
            },
            || {
                actions.borrow_mut().push("delete");
                Ok(())
            },
        );
        actions.into_inner()
    }

    #[test]
    fn k8s_benchmark_command_writes_metadata_before_strict_preflight_failure() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let error = run_k8s_benchmark_with_context(
            benchmark_config_for_test(),
            manifest_with_requests(vec![manifest_request(0, None)]),
            tempdir.path().to_path_buf(),
            false,
            test_metadata(true),
            || panic!("kubectl context must not be checked after failed preflight"),
        )
        .expect_err("strict preflight should fail before kubectl context checks");

        let metadata_path = tempdir.path().join("run-metadata.json");
        assert!(metadata_path.exists());
        assert!(!tempdir.path().join("manifest.json").exists());
        let metadata = serde_json::from_slice::<crate::metadata::RunMetadata>(
            &std::fs::read(&metadata_path).expect("metadata should read"),
        )
        .expect("metadata should parse");
        assert_eq!(metadata.preflight.failure_count, 1);
        assert!(
            error
                .to_string()
                .contains("strict reliability preflight failed with 1 failure(s)")
        );
    }

    #[test]
    fn k8s_benchmark_command_success_writes_manifest_and_empty_report_without_algorithms() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let mut config = benchmark_config_for_test();
        config.algorithms.clear();
        let manifest_path = tempdir.path().join("manifest.json");
        run_k8s_benchmark_with_context(
            config,
            manifest_with_requests(vec![manifest_request(0, None)]),
            tempdir.path().to_path_buf(),
            false,
            test_metadata(false),
            || {
                assert!(!manifest_path.exists());
                Ok(())
            },
        )
        .expect("empty algorithm run should write command artifacts");

        assert!(tempdir.path().join("run-metadata.json").exists());
        assert!(manifest_path.exists());
        let comparison: serde_json::Value = serde_json::from_slice(
            &std::fs::read(tempdir.path().join("comparison.json")).expect("comparison should read"),
        )
        .expect("comparison should parse");
        assert_eq!(comparison.as_array().expect("comparison is array").len(), 0);
        assert!(tempdir.path().join("report.md").exists());
    }

    #[test]
    fn k8s_replay_plan_loads_manifest_and_clones_first_request_for_probe() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let manifest_path = tempdir.path().join("manifest.json");
        let first_request = manifest_request(0, Some("tenant-a"));
        let second_request = manifest_request(1, Some("tenant-b"));
        write_manifest_json(
            &manifest_path,
            &manifest_with_requests(vec![first_request.clone(), second_request]),
        )
        .expect("manifest should write");

        let (manifest, routing_probe) =
            load_k8s_replay(&manifest_path).expect("replay plan should load");

        assert_eq!(manifest.requests.len(), 2);
        assert_eq!(routing_probe.request_id, first_request.request_id);
        assert_eq!(routing_probe.routing_key.as_deref(), Some("tenant-a"));
    }

    #[test]
    fn k8s_replay_plan_rejects_empty_manifest() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let manifest_path = tempdir.path().join("empty-manifest.json");
        write_manifest_json(&manifest_path, &manifest_with_requests(Vec::new()))
            .expect("manifest should write");

        let error = load_k8s_replay(&manifest_path)
            .expect_err("empty manifests should not produce a replay plan");

        assert!(
            error
                .to_string()
                .contains("benchmark manifest must contain at least one request")
        );
    }

    #[test]
    fn k8s_replay_artifacts_use_canonical_run_filenames() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let run_dir = tempdir.path().join("run-power-of-two");

        let artifacts = K8sRunArtifacts(&run_dir);

        for filename in [
            "collector-baseline-metrics.prom",
            "requests.jsonl",
            "metrics.prom",
            "collector-metrics.prom",
            "summary.json",
        ] {
            assert_eq!(artifacts.path(filename), run_dir.join(filename));
        }
    }

    #[test]
    fn k8s_readiness_gate_checks_health_metrics_and_routing_with_local_servers() {
        let runtime = tokio::runtime::Runtime::new().expect("runtime should create");
        let metrics = "\nstargate_active_inference_servers{model=\"dummy-model\",routing_key=\"tenant-a\"} 1\n";
        let stargate_server = FixedTextServer::spawn(&runtime, "", 2);
        let stargate_base = stargate_server
            .endpoint
            .strip_suffix("/metrics")
            .expect("test endpoint should include metrics suffix");
        let metrics_server = FixedTextServer::spawn(&runtime, metrics, 1);
        runtime
            .block_on(wait_for_k8s_readiness(
                &format!("{stargate_base}/healthz"),
                std::slice::from_ref(&metrics_server.endpoint),
                &format!("{stargate_base}/v1/chat/completions"),
                "dummy-model",
                1,
                &manifest_request(7, Some("tenant-a")),
                Duration::from_secs(1),
            ))
            .expect("readiness gate should pass");
        stargate_server.finish(&runtime);
        metrics_server.finish(&runtime);
    }

    #[test]
    fn k8s_replay_collector_baseline_writes_metrics_and_totals() {
        let runtime = tokio::runtime::Runtime::new().expect("runtime should create");
        let metrics = r#"
stargate_requests_total_total{model="dummy-model",status="ok"} 2
pylon_requests_total_total{model="dummy-model",status="complete"} 3
"#;
        let server = FixedTextServer::spawn(&runtime, metrics, 1);
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let run = benchmark_run_for_test(
            tempdir.path(),
            "http://127.0.0.1:9/metrics",
            &server.endpoint,
        );
        let artifacts = K8sRunArtifacts(&run.run_dir);

        let baseline = capture_collector_baseline(&runtime, &run, &artifacts)
            .expect("baseline should capture");

        assert_eq!(
            baseline.request_totals,
            ScrapedRequestTotals {
                stargate: 2.0,
                pylon: 3.0,
            }
        );
        assert_file(&artifacts.path("collector-baseline-metrics.prom"), metrics);
        server.finish(&runtime);
    }

    #[test]
    fn k8s_run_summary_writes_post_replay_artifacts_and_metric_deltas() {
        let runtime = tokio::runtime::Runtime::new().expect("runtime should create");
        let stargate_metrics =
            "\nstargate_active_inference_servers{model=\"dummy-model\",routing_key=\"\"} 1\n";
        let collector_baseline_metrics = r#"
stargate_requests_total_total{model="dummy-model",status="ok"} 2
pylon_requests_total_total{model="dummy-model",status="complete"} 2
pylon_queue_admission_decisions_total_total{model_id="dummy-model",result="accepted"} 1
stargate_routing_selections_total_total{model="dummy-model",selection="primary"} 1
stargate_routing_selections_total_total{model="dummy-model",selection="fallback"} 1
"#;
        let collector_post_metrics = r#"
stargate_requests_total_total{model="dummy-model",status="ok"} 3
pylon_requests_total_total{model="dummy-model",status="complete"} 3
pylon_queue_admission_decisions_total_total{model_id="dummy-model",result="accepted"} 4
stargate_routing_selections_total_total{model="dummy-model",selection="primary"} 6
stargate_routing_selections_total_total{model="dummy-model",selection="fallback"} 3
"#;
        let stargate_server = FixedTextServer::spawn(&runtime, stargate_metrics, 1);
        let collector_server = FixedTextServer::spawn(&runtime, collector_post_metrics, 0);
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let run = benchmark_run_for_test(
            tempdir.path(),
            &stargate_server.endpoint,
            &collector_server.endpoint,
        );
        let artifacts = K8sRunArtifacts(&run.run_dir);
        let config = benchmark_config_for_test();
        let topology = topology_for(&config.backends);

        let summary = write_k8s_run_summary(
            &runtime,
            &run,
            &topology,
            &[successful_request_result()],
            K8sCollectorBaseline {
                metrics: collector_baseline_metrics.to_string(),
                request_totals: ScrapedRequestTotals {
                    stargate: 2.0,
                    pylon: 2.0,
                },
            },
            &artifacts,
        )
        .expect("summary artifacts should write");

        assert_eq!(summary.request_count, 1);
        assert_eq!(summary.queue_admission_summary.pylon_accepted_count, 3.0);
        assert_eq!(summary.routing_selection_summary.primary_count, 5.0);
        assert_eq!(summary.routing_selection_summary.fallback_count, 2.0);
        assert_file(&artifacts.path("metrics.prom"), stargate_metrics);
        assert_file(
            &artifacts.path("collector-metrics.prom"),
            collector_post_metrics,
        );
        let summary_artifact = serde_json::from_slice::<RunSummary>(
            &std::fs::read(artifacts.path("summary.json")).expect("summary artifact should read"),
        )
        .expect("summary artifact should parse");
        assert_eq!(
            summary_artifact
                .queue_admission_summary
                .pylon_accepted_count,
            3.0
        );
        stargate_server.finish(&runtime);
        collector_server.finish(&runtime);
    }

    #[test]
    fn k8s_replay_drives_empty_manifest_to_requests_artifact_without_network() {
        let runtime = tokio::runtime::Runtime::new().expect("runtime should create");
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let run = benchmark_run_for_test(
            tempdir.path(),
            "http://127.0.0.1:9/metrics",
            "http://127.0.0.1:9/metrics",
        );
        let artifacts = K8sRunArtifacts(&run.run_dir);

        let results = drive_k8s_replay(
            &runtime,
            &run,
            manifest_with_requests(Vec::new()),
            1,
            &[],
            &artifacts,
        )
        .expect("empty replay should complete without requests");

        assert!(results.is_empty());
        assert_file(&artifacts.path("requests.jsonl"), "");
    }

    fn manifest_with_requests(requests: Vec<ManifestRequest>) -> Manifest {
        Manifest {
            manifest_version: 1,
            benchmark_name: "k8s-replay-test".to_string(),
            metadata: crate::config::ScenarioMetadata::default(),
            model: "dummy-model".to_string(),
            seed: 7,
            request_count: requests.len(),
            max_concurrency: 1,
            stargate_count: 1,
            backend_count: 1,
            cluster_count: 1,
            pylons_per_cluster: 1,
            requests,
        }
    }

    fn benchmark_config_for_test() -> BenchmarkConfig {
        serde_yaml_ng::from_str(
            r#"
name: k8s-command-test
model: dummy-model
seed: 7
request_count: 1
max_concurrency: 1
backends:
  count: 1
  profile:
    name: balanced
    service_time_ms: { ttft_mean: 150, ttft_jitter_ms: 10, decode_tokens_per_s: 50 }
    registration: { last_mean_input_tps: 100.0 }
traffic_pattern:
  kind: uniform
  routing_keys: 1
  cache_affinity_keys: 1
  input_tokens: { distribution: constant, value: 100 }
  output_tokens: { distribution: constant, value: 20 }
  arrival: { distribution: constant, interval_ms: 10 }
algorithms:
  - name: power-of-two
    config: { default: power-of-two }
"#,
        )
        .expect("benchmark config fixture should parse")
    }

    fn test_metadata(should_fail: bool) -> crate::metadata::RunMetadata {
        let checks = should_fail
            .then(|| crate::metadata::PreflightCheck {
                name: "release-binary".to_string(),
                level: crate::metadata::PreflightLevel::Failure,
                message: "debug build".to_string(),
            })
            .into_iter()
            .collect();
        crate::metadata::RunMetadata {
            schema_version: 3,
            benchmark_tier: BenchmarkTier::LocalK8sSmoke,
            reliability_mode: ReliabilityMode::Strict,
            driver_mode: DriverMode::ExternalNodePort,
            command_line: vec!["stargate-bench".to_string(), "run".to_string()],
            started_at_unix_seconds: 0,
            current_exe: None,
            working_dir: None,
            git: crate::metadata::GitMetadata::default(),
            rust: crate::metadata::RustMetadata::default(),
            host: crate::metadata::HostMetadata::default(),
            kubernetes: crate::metadata::KubernetesMetadata::default(),
            preflight: crate::metadata::PreflightReport {
                checks,
                warning_count: 0,
                failure_count: usize::from(should_fail),
                should_fail,
            },
            known_limitations: Vec::new(),
        }
    }

    fn manifest_request(request_index: usize, routing_key: Option<&str>) -> ManifestRequest {
        ManifestRequest {
            request_index,
            request_id: format!("request-{request_index}"),
            scheduled_offset_ms: request_index as u64,
            routing_key: routing_key.map(str::to_string),
            cache_affinity_key: Some(format!("cache-key-{request_index}")),
            input_tokens: 128,
            output_tokens: 16,
            backend_behavior_class: "default".to_string(),
        }
    }

    fn benchmark_run_for_test(
        run_dir: &Path,
        stargate_metrics_endpoint: &str,
        collector_metrics_endpoint: &str,
    ) -> BenchmarkK8sRun {
        std::fs::create_dir_all(run_dir).expect("run dir should create");
        BenchmarkK8sRun {
            algorithm_name: "power-of-two".to_string(),
            manifest_path: run_dir.join("manifest.json"),
            run_dir: run_dir.to_path_buf(),
            stargate_ns: "sgbench-sg-power-of-two".to_string(),
            backends_ns: "sgbench-be-power-of-two".to_string(),
            stargate_count: 1,
            nodeport_host: "node.test".to_string(),
            stargate_http_endpoint: "http://node.test:30080".to_string(),
            stargate_metrics_endpoint: stargate_metrics_endpoint.to_string(),
            collector_metrics_endpoint: collector_metrics_endpoint.to_string(),
            backend_upstream_indices: vec![0],
            upstream_backend_indices: vec![0],
        }
    }

    fn successful_request_result() -> RequestResult {
        RequestResult {
            request_index: 0,
            request_id: "request-0".to_string(),
            routing_key: None,
            cache_affinity_key: Some("cache-key-0".to_string()),
            input_tokens: 128,
            output_tokens: 16,
            scheduled_offset_ms: 0,
            status_code: 200,
            selected_backend_id: Some("backend-0".to_string()),
            dispatch_offset_ms: 0,
            response_headers_ms: Some(10),
            first_output_ms: Some(20),
            completion_ms: 40,
            kv_cache_hit: Some(false),
            kv_cache_reused_input_tokens: Some(0),
            kv_cache_uncached_input_tokens: Some(128),
            kv_cache_evicted_entries: Some(0),
            kv_cache_evicted_tokens: Some(0),
            ok: true,
            error: None,
        }
    }

    fn assert_file(path: &Path, expected: &str) {
        assert_eq!(
            std::fs::read_to_string(path).expect("artifact should read"),
            expected
        );
    }

    struct FixedTextServer {
        endpoint: String,
        stop: tokio::sync::oneshot::Sender<()>,
        task: tokio::task::JoinHandle<()>,
    }

    impl FixedTextServer {
        fn spawn(
            runtime: &tokio::runtime::Runtime,
            body: &'static str,
            request_count: usize,
        ) -> Self {
            let listener = runtime
                .block_on(tokio::net::TcpListener::bind("127.0.0.1:0"))
                .expect("test metrics server should bind");
            let addr = listener.local_addr().expect("local addr should exist");
            let (stop_tx, mut stop_rx) = tokio::sync::oneshot::channel();
            let task = runtime.spawn(async move {
                let mut served = 0;
                loop {
                    let accepted = tokio::select! {
                        _ = &mut stop_rx, if request_count == 0 => break,
                        accepted = listener.accept() => accepted,
                    };
                    let (mut socket, _) = accepted.expect("request should connect");
                    write_fixed_response(&mut socket, body).await;
                    served += 1;
                    if served == request_count {
                        break;
                    }
                }
            });
            Self {
                endpoint: format!("http://{addr}/metrics"),
                stop: stop_tx,
                task,
            }
        }

        fn finish(self, runtime: &tokio::runtime::Runtime) {
            let _ = self.stop.send(());
            runtime
                .block_on(self.task)
                .expect("metrics server should finish");
        }
    }

    async fn write_fixed_response(socket: &mut tokio::net::TcpStream, body: &str) {
        let mut request = Vec::new();
        let mut buffer = [0; 1024];
        while !request.windows(4).any(|window| window == b"\r\n\r\n") {
            let read = socket.read(&mut buffer).await.expect("request should read");
            if read == 0 {
                break;
            }
            request.extend_from_slice(&buffer[..read]);
        }
        let response = format!(
            "HTTP/1.1 200 OK\r\ncontent-length: {}\r\nconnection: close\r\n\r\n{body}",
            body.len()
        );
        socket
            .write_all(response.as_bytes())
            .await
            .expect("response should write");
    }
}

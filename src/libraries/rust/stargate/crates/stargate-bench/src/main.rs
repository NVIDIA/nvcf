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

mod config;
mod driver;
mod k8s;
mod k8s_run;
mod manifest;
mod metadata;
mod microbench;
mod orchestrator;
mod report;
mod runtime;
mod score;
mod statistics;
mod transport;

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

use anyhow::Context;
use clap::{Args, Parser, Subcommand};

use crate::config::{BenchmarkConfig, PylonQueueAdmissionConfig};
use crate::driver::{DriveConfig, drive_manifest, load_manifest};
use crate::manifest::{generate_manifest, write_manifest_json};
use crate::metadata::{
    BenchmarkTier, DriverMode, ReliabilityMode, collect_run_metadata, write_run_metadata,
};
use crate::microbench::{
    BodyBufferMicrobenchConfig, HeaderFilterMicrobenchConfig, LbMicrobenchConfig,
    LbMicrobenchScenario, render_body_buffer_microbench_report,
    render_header_filter_microbench_report, run_body_buffer_microbench,
    run_header_filter_microbench, run_lb_microbench, write_lb_microbench_csv,
};
use crate::orchestrator::prepare_suite;
use crate::report::{ReportContext, ReportEntry, render_markdown_report};
use crate::score::RunSummary;
use crate::transport::{
    TransportBenchConfig, render_transport_benchmark_report, run_transport_benchmark,
    write_transport_benchmark_artifacts,
};

const BENCHES_DIR: &str = "benches";

#[derive(Parser, Debug)]
#[command(name = "stargate-bench")]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand, Debug)]
enum Command {
    /// List benchmark scenario aliases from benches/*.yaml
    ListScenarios,
    /// Generate a deterministic benchmark manifest and print it to stdout
    InspectManifest {
        #[command(flatten)]
        source: BenchmarkSourceArgs,
        #[arg(long, value_name = "SEED")]
        seed: Option<u64>,
    },
    /// Materialize a deterministic benchmark manifest and input artifacts
    Materialize {
        #[command(flatten)]
        source: BenchmarkSourceArgs,
        #[arg(long, value_name = "SEED")]
        seed: Option<u64>,
        #[arg(long = "algorithm", value_name = "NAME")]
        algorithms: Vec<String>,
        #[arg(long, value_name = "PATH")]
        output_dir: Option<PathBuf>,
    },
    /// Prepare per-algorithm local run directories with docker-compose and LB configs
    PrepareRun {
        #[command(flatten)]
        source: BenchmarkSourceArgs,
        #[arg(long, value_name = "SEED")]
        seed: Option<u64>,
        #[arg(long = "algorithm", value_name = "NAME")]
        algorithms: Vec<String>,
        #[arg(long, value_name = "PATH")]
        output_dir: Option<PathBuf>,
    },
    /// Replay a manifest against a running stargate endpoint and record per-request results
    Drive {
        #[arg(long, value_name = "PATH")]
        manifest: PathBuf,
        #[arg(long, value_name = "URL")]
        endpoint: String,
        #[arg(long, value_name = "PATH")]
        output: PathBuf,
        #[arg(long, value_name = "N")]
        concurrency_limit: Option<usize>,
    },
    /// Regenerate report.md for an existing benchmark output directory
    Report {
        #[arg(long, value_name = "PATH")]
        output_dir: PathBuf,
    },
    /// Run benchmark suites against Kubernetes using configured benchmark images
    Run {
        #[command(flatten)]
        source: BenchmarkSourceArgs,
        #[arg(long, value_name = "SEED")]
        seed: Option<u64>,
        #[arg(long = "algorithm", value_name = "NAME")]
        algorithms: Vec<String>,
        #[arg(long, value_name = "PATH")]
        output_dir: Option<PathBuf>,
        #[arg(long)]
        keep_resources_on_failure: bool,
        #[arg(long, value_enum, default_value_t = ReliabilityMode::Smoke, value_name = "MODE")]
        reliability_mode: ReliabilityMode,
    },
    /// Compare custom, HTTP/3, and WebTransport tunnel transports on loopback
    TransportBench {
        #[arg(long, default_value_t = 20_000, value_name = "N")]
        requests: usize,
        #[arg(long, default_value_t = 256, value_name = "N")]
        concurrency: usize,
        #[arg(long, default_value_t = 1, value_name = "N")]
        quic_connections: usize,
        #[arg(long, default_value_t = 1_000, value_name = "N")]
        warmup_requests: usize,
        #[arg(long, default_value_t = 1024, value_name = "BYTES")]
        request_body_bytes: usize,
        #[arg(long, default_value_t = 1024, value_name = "BYTES")]
        response_body_bytes: usize,
        #[arg(long, default_value_t = 16 * 1024, value_name = "BYTES")]
        request_chunk_bytes: usize,
        #[arg(long, default_value_t = 16 * 1024, value_name = "BYTES")]
        response_chunk_bytes: usize,
        #[arg(long)]
        disable_quic_send_fairness: bool,
        #[arg(long)]
        disable_http3_grease: bool,
        #[arg(long, default_value_t = 1, value_name = "N")]
        trials: usize,
        #[arg(long, default_value_t = 0, value_name = "N")]
        warmup_trials: usize,
        #[arg(long, default_value_t = 0, value_name = "MS")]
        cooldown_ms: u64,
        #[arg(long)]
        randomize_order: bool,
        #[arg(long, default_value_t = 0.02, value_name = "CV")]
        noise_threshold_cv: f64,
        #[arg(long, default_value_t = 1.0, value_name = "PERCENT")]
        min_effect_size_percent: f64,
        #[arg(long, value_enum, default_value_t = ReliabilityMode::Smoke, value_name = "MODE")]
        reliability_mode: ReliabilityMode,
        #[arg(long, value_name = "PATH")]
        output_dir: Option<PathBuf>,
    },
    /// Measure in-process groq-multiregion/pulsar load-balancer choose-path overhead
    LbMicrobench {
        #[arg(long, default_value_t = 100_000, value_name = "N")]
        iterations: usize,
        #[arg(long, default_value_t = 10_000, value_name = "N")]
        warmup_iterations: usize,
        #[arg(long, default_value_t = 1, value_name = "N")]
        concurrency: usize,
        #[arg(long, default_value_t = 64, value_name = "N")]
        candidates: usize,
        #[arg(long, default_value_t = 1024, value_name = "N")]
        cache_key_count: usize,
        #[arg(long = "scenario", value_enum, value_name = "NAME")]
        scenarios: Vec<LbMicrobenchScenario>,
    },
    /// Compare lowercasing header filters against allocation-free static matchers
    HeaderFilterMicrobench {
        #[arg(long, default_value_t = 1_000_000, value_name = "N")]
        iterations: usize,
        #[arg(long, default_value_t = 100_000, value_name = "N")]
        warmup_iterations: usize,
        #[arg(long, default_value_t = 128, value_name = "N")]
        header_count: usize,
    },
    /// Compare Pylon-style request body buffering copy strategies
    BodyBufferMicrobench {
        #[arg(long, default_value_t = 20_000, value_name = "N")]
        iterations: usize,
        #[arg(long, default_value_t = 2_000, value_name = "N")]
        warmup_iterations: usize,
        #[arg(long, default_value_t = 65_536, value_name = "BYTES")]
        body_bytes: usize,
        #[arg(long, default_value_t = 1_024, value_name = "BYTES")]
        chunk_bytes: usize,
    },
}

#[derive(Args, Debug, Clone)]
struct BenchmarkSourceArgs {
    /// Path to a benchmark YAML/JSON config
    #[arg(long, conflicts_with = "scenario", value_name = "PATH")]
    config: Option<PathBuf>,
    /// Scenario alias from benches/*.yaml, for example hotset-8-backends
    #[arg(long, conflicts_with = "config", value_name = "NAME")]
    scenario: Option<String>,
}

#[derive(Debug, Clone)]
struct Scenario {
    name: String,
    path: PathBuf,
}

#[derive(serde::Deserialize)]
struct RunInfo {
    algorithm_name: String,
    #[serde(default)]
    pylon_queue_admission: Option<PylonQueueAdmissionConfig>,
}

fn main() -> anyhow::Result<()> {
    let cli = Cli::parse();
    match cli.command {
        Command::ListScenarios => list_scenarios(),
        Command::InspectManifest { source, seed } => inspect_manifest(source, seed),
        Command::Materialize {
            source,
            seed,
            algorithms,
            output_dir,
        } => materialize(source, seed, algorithms, output_dir),
        Command::PrepareRun {
            source,
            seed,
            algorithms,
            output_dir,
        } => prepare_run(source, seed, algorithms, output_dir),
        Command::Drive {
            manifest,
            endpoint,
            output,
            concurrency_limit,
        } => drive(&manifest, &endpoint, &output, concurrency_limit),
        Command::Report { output_dir } => regenerate_report(&output_dir),
        Command::Run {
            source,
            seed,
            algorithms,
            output_dir,
            keep_resources_on_failure,
            reliability_mode,
        } => run(
            source,
            seed,
            algorithms,
            output_dir,
            keep_resources_on_failure,
            reliability_mode,
        ),
        Command::TransportBench {
            requests,
            concurrency,
            quic_connections,
            warmup_requests,
            request_body_bytes,
            response_body_bytes,
            request_chunk_bytes,
            response_chunk_bytes,
            disable_quic_send_fairness,
            disable_http3_grease,
            trials,
            warmup_trials,
            cooldown_ms,
            randomize_order,
            noise_threshold_cv,
            min_effect_size_percent,
            reliability_mode,
            output_dir,
        } => transport_bench(
            TransportBenchConfig {
                request_count: requests,
                concurrency,
                quic_connections,
                warmup_requests,
                request_body_bytes,
                response_body_bytes,
                request_chunk_bytes,
                response_chunk_bytes,
                quic_send_fairness: !disable_quic_send_fairness,
                http3_send_grease: !disable_http3_grease,
                trials,
                warmup_trials,
                cooldown_ms,
                randomize_order,
                noise_threshold_cv,
                min_effect_size_percent,
            },
            reliability_mode,
            output_dir,
        ),
        Command::LbMicrobench {
            iterations,
            warmup_iterations,
            concurrency,
            candidates,
            cache_key_count,
            scenarios,
        } => lb_microbench(LbMicrobenchConfig {
            iterations,
            warmup_iterations,
            concurrency,
            candidates,
            cache_key_count,
            scenarios,
        }),
        Command::HeaderFilterMicrobench {
            iterations,
            warmup_iterations,
            header_count,
        } => header_filter_microbench(HeaderFilterMicrobenchConfig {
            iterations,
            warmup_iterations,
            header_count,
        }),
        Command::BodyBufferMicrobench {
            iterations,
            warmup_iterations,
            body_bytes,
            chunk_bytes,
        } => body_buffer_microbench(BodyBufferMicrobenchConfig {
            iterations,
            warmup_iterations,
            body_bytes,
            chunk_bytes,
        }),
    }
}

fn body_buffer_microbench(config: BodyBufferMicrobenchConfig) -> anyhow::Result<()> {
    let outcome = run_body_buffer_microbench(config)?;
    print!("{}", render_body_buffer_microbench_report(&outcome));
    Ok(())
}

fn header_filter_microbench(config: HeaderFilterMicrobenchConfig) -> anyhow::Result<()> {
    let outcome = run_header_filter_microbench(config)?;
    print!("{}", render_header_filter_microbench_report(&outcome));
    Ok(())
}

fn resolve_config_path(source: BenchmarkSourceArgs) -> anyhow::Result<PathBuf> {
    match (source.config, source.scenario) {
        (Some(config), None) => Ok(config),
        (None, Some(scenario)) => scenario_config_path(&scenario),
        (None, None) => anyhow::bail!("provide either --config <path> or --scenario <name>"),
        (Some(_), Some(_)) => anyhow::bail!("provide only one of --config or --scenario"),
    }
}

fn scenario_config_path(scenario: &str) -> anyhow::Result<PathBuf> {
    let name = scenario
        .strip_suffix(".yaml")
        .or_else(|| scenario.strip_suffix(".yml"))
        .unwrap_or(scenario);
    if name.contains('/') || name.contains('\\') || name.is_empty() {
        anyhow::bail!("scenario names must be file stems from benches/*.yaml");
    }

    let path = benches_dir().join(format!("{name}.yaml"));
    if path.exists() {
        return Ok(path);
    }

    let available = discover_scenarios()?
        .into_iter()
        .map(|scenario| scenario.name)
        .collect::<Vec<_>>()
        .join(", ");
    anyhow::bail!("unknown benchmark scenario '{name}'. Available scenarios: {available}")
}

fn discover_scenarios() -> anyhow::Result<Vec<Scenario>> {
    let mut scenarios = Vec::new();
    let entries = match std::fs::read_dir(benches_dir()) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(scenarios),
        Err(error) => return Err(error).context("failed to read benches directory"),
    };
    for entry in entries {
        let entry = entry.context("failed to read benches directory entry")?;
        let path = entry.path();
        if path.extension().and_then(|ext| ext.to_str()) != Some("yaml") {
            continue;
        }
        let Some(name) = path.file_stem().and_then(|stem| stem.to_str()) else {
            continue;
        };
        scenarios.push(Scenario {
            name: name.to_string(),
            path,
        });
    }
    scenarios.sort_by(|a, b| a.name.cmp(&b.name));
    Ok(scenarios)
}

fn filter_algorithms(config: &mut BenchmarkConfig, requested: &[String]) -> anyhow::Result<()> {
    if requested.is_empty() {
        return Ok(());
    }

    let requested = requested.iter().cloned().collect::<BTreeSet<_>>();
    let available = config
        .algorithms
        .iter()
        .map(|algorithm| algorithm.name.clone())
        .collect::<BTreeSet<_>>();
    let unknown = requested
        .difference(&available)
        .cloned()
        .collect::<Vec<_>>();
    if !unknown.is_empty() {
        anyhow::bail!(
            "unknown algorithm(s): {}. Available algorithms: {}",
            unknown.join(", "),
            available.into_iter().collect::<Vec<_>>().join(", ")
        );
    }

    config
        .algorithms
        .retain(|algorithm| requested.contains(&algorithm.name));
    Ok(())
}

fn default_output_dir(config: &BenchmarkConfig) -> PathBuf {
    Path::new(".bench-out").join(&config.name)
}

fn benches_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(2)
        .unwrap_or_else(|| Path::new("."))
        .join(BENCHES_DIR)
}

fn list_scenarios() -> anyhow::Result<()> {
    let scenarios = discover_scenarios()?;
    if scenarios.is_empty() {
        println!("no benchmark scenarios found under benches/");
        return Ok(());
    }
    println!(
        "{:<28} {:>8} {:>9} {:>8}  {:<24}  Algorithms",
        "Scenario", "Requests", "Backends", "Stargates", "Tags"
    );
    for scenario in scenarios {
        let config = BenchmarkConfig::load(&scenario.path)?;
        let algorithms = config
            .algorithms
            .iter()
            .map(|algorithm| algorithm.name.as_str())
            .collect::<Vec<_>>()
            .join(",");
        println!(
            "{:<28} {:>8} {:>9} {:>8}  {:<24}  {}",
            scenario.name,
            config.request_count,
            config.backends.count,
            config.stargates.count,
            config.metadata.tags.join(","),
            algorithms
        );
    }
    Ok(())
}

fn inspect_manifest(source: BenchmarkSourceArgs, seed: Option<u64>) -> anyhow::Result<()> {
    let config_path = resolve_config_path(source)?;
    let config = BenchmarkConfig::load(&config_path)?;
    let manifest = generate_manifest(&config, seed)?;
    let rendered =
        serde_json::to_string_pretty(&manifest).context("failed to render manifest as JSON")?;
    println!("{rendered}");
    Ok(())
}

fn materialize(
    source: BenchmarkSourceArgs,
    seed: Option<u64>,
    algorithms: Vec<String>,
    output_dir: Option<PathBuf>,
) -> anyhow::Result<()> {
    let config_path = resolve_config_path(source)?;
    let mut config = BenchmarkConfig::load(&config_path)?;
    filter_algorithms(&mut config, &algorithms)?;
    let manifest = generate_manifest(&config, seed)?;
    let output_dir = output_dir.unwrap_or_else(|| default_output_dir(&config));
    std::fs::create_dir_all(&output_dir)
        .with_context(|| format!("failed to create output dir {}", output_dir.display()))?;

    let effective_seed = manifest.seed;
    let manifest_path = output_dir.join("manifest.json");
    let config_copy_path = output_dir.join("benchmark-config.json");
    let summary_path = output_dir.join("summary.json");

    write_manifest_json(&manifest_path, &manifest)?;
    let config_bytes =
        serde_json::to_vec_pretty(&config).context("failed to serialize normalized config")?;
    std::fs::write(&config_copy_path, config_bytes)
        .with_context(|| format!("failed to write {}", config_copy_path.display()))?;

    let summary = serde_json::json!({
        "benchmark_name": config.name,
        "metadata": config.metadata.clone(),
        "model": config.model,
        "seed": effective_seed,
        "request_count": manifest.request_count,
        "stargate_count": manifest.stargate_count,
        "backend_count": manifest.backend_count,
        "algorithm_names": config.algorithms.iter().map(|algorithm| algorithm.name.clone()).collect::<Vec<_>>(),
    });
    let summary_bytes =
        serde_json::to_vec_pretty(&summary).context("failed to serialize summary")?;
    std::fs::write(&summary_path, summary_bytes)
        .with_context(|| format!("failed to write {}", summary_path.display()))?;

    println!(
        "materialized benchmark input at {}",
        output_dir
            .canonicalize()
            .unwrap_or_else(|_| output_dir.to_path_buf())
            .display()
    );
    Ok(())
}

fn prepare_run(
    source: BenchmarkSourceArgs,
    seed: Option<u64>,
    algorithms: Vec<String>,
    output_dir: Option<PathBuf>,
) -> anyhow::Result<()> {
    let config_path = resolve_config_path(source)?;
    let mut config = BenchmarkConfig::load(&config_path)?;
    filter_algorithms(&mut config, &algorithms)?;
    let manifest = generate_manifest(&config, seed)?;
    let output_dir = output_dir.unwrap_or_else(|| default_output_dir(&config));
    let prepared = prepare_suite(&config, &manifest, &output_dir)?;
    println!(
        "prepared {} algorithm runs at {}",
        prepared.algorithm_runs.len(),
        output_dir
            .canonicalize()
            .unwrap_or_else(|_| output_dir.to_path_buf())
            .display()
    );
    for run in prepared.algorithm_runs {
        println!(
            "{}: compose={} http={} grpc={}",
            run.algorithm_name,
            run.compose_path.display(),
            run.stargate_http_endpoint,
            run.stargate_grpc_endpoint
        );
    }
    Ok(())
}

fn drive(
    manifest_path: &Path,
    endpoint: &str,
    output: &Path,
    concurrency_limit: Option<usize>,
) -> anyhow::Result<()> {
    let manifest = load_manifest(manifest_path)?;
    let concurrency_limit = concurrency_limit.unwrap_or(manifest.max_concurrency);
    let runtime = tokio::runtime::Runtime::new().context("failed to create tokio runtime")?;
    let results = runtime.block_on(drive_manifest(
        DriveConfig {
            endpoint: endpoint.to_string(),
            output_path: output.to_path_buf(),
            concurrency_limit,
        },
        manifest,
    ))?;
    println!(
        "wrote {} request results to {}",
        results.len(),
        output.display()
    );
    Ok(())
}

fn transport_bench(
    config: TransportBenchConfig,
    reliability_mode: ReliabilityMode,
    output_dir: Option<PathBuf>,
) -> anyhow::Result<()> {
    let metadata = collect_run_metadata(
        BenchmarkTier::TransportLoopback,
        reliability_mode,
        DriverMode::LocalProcess,
    );
    if let Some(output_dir) = &output_dir {
        std::fs::create_dir_all(output_dir)
            .with_context(|| format!("failed to create {}", output_dir.display()))?;
        write_run_metadata(&output_dir.join("run-metadata.json"), &metadata)?;
    }
    if metadata.preflight.should_fail {
        anyhow::bail!(
            "strict reliability preflight failed with {} failure(s); inspect run-metadata.json when --output-dir is set",
            metadata.preflight.failure_count
        );
    }

    let runtime = tokio::runtime::Runtime::new().context("failed to create tokio runtime")?;
    let outcome = runtime.block_on(run_transport_benchmark(config))?;
    let report = render_transport_benchmark_report(&outcome);
    println!("{report}");
    if let Some(output_dir) = output_dir {
        write_transport_benchmark_artifacts(&output_dir, &outcome)?;
        println!(
            "wrote transport benchmark artifacts to {}",
            output_dir.display()
        );
    }
    Ok(())
}

fn lb_microbench(config: LbMicrobenchConfig) -> anyhow::Result<()> {
    let rows = run_lb_microbench(&config)?;
    write_lb_microbench_csv(std::io::stdout(), &rows).context("failed to write lb microbench CSV")
}

fn regenerate_report(output_dir: &Path) -> anyhow::Result<()> {
    let manifest = load_manifest(&output_dir.join("manifest.json"))?;
    let context = ReportContext::from_manifest(&manifest);
    let mut entries = Vec::new();
    let dirs = std::fs::read_dir(output_dir)
        .with_context(|| format!("failed to read output dir {}", output_dir.display()))?;
    for entry in dirs {
        let entry = entry.context("failed to read output dir entry")?;
        let path = entry.path();
        if !path.is_dir()
            || !path
                .file_name()
                .and_then(|name| name.to_str())
                .is_some_and(|name| name.starts_with("run-"))
        {
            continue;
        }
        let summary_path = path.join("summary.json");
        if !summary_path.exists() {
            continue;
        }
        let summary = read_json::<RunSummary>(&summary_path)?;
        let run_info = read_run_info(&path)?;
        entries.push(ReportEntry {
            algorithm_name: run_info.algorithm_name,
            pylon_queue_admission: run_info.pylon_queue_admission,
            summary,
        });
    }
    entries.sort_by(|a, b| a.algorithm_name.cmp(&b.algorithm_name));
    let report = render_markdown_report(&context, &entries);
    let report_path = output_dir.join("report.md");
    std::fs::write(&report_path, report)
        .with_context(|| format!("failed to write {}", report_path.display()))?;
    println!("wrote {}", report_path.display());
    Ok(())
}

fn read_json<T: serde::de::DeserializeOwned>(path: &Path) -> anyhow::Result<T> {
    let bytes =
        std::fs::read(path).with_context(|| format!("failed to read {}", path.display()))?;
    serde_json::from_slice(&bytes).with_context(|| format!("failed to parse {}", path.display()))
}

fn read_run_info(run_dir: &Path) -> anyhow::Result<RunInfo> {
    let run_info_path = run_dir.join("run-info.json");
    if run_info_path.exists() {
        return read_json::<RunInfo>(&run_info_path);
    }
    let name = run_dir
        .file_name()
        .and_then(|name| name.to_str())
        .and_then(|name| name.strip_prefix("run-"))
        .context("run directory must be named run-<algorithm>")?;
    Ok(RunInfo {
        algorithm_name: name.to_string(),
        pylon_queue_admission: None,
    })
}

fn run(
    source: BenchmarkSourceArgs,
    seed: Option<u64>,
    algorithms: Vec<String>,
    output_dir: Option<PathBuf>,
    keep_resources_on_failure: bool,
    reliability_mode: ReliabilityMode,
) -> anyhow::Result<()> {
    let config_path = resolve_config_path(source)?;
    let mut config = BenchmarkConfig::load(&config_path)?;
    filter_algorithms(&mut config, &algorithms)?;
    let manifest = generate_manifest(&config, seed)?;
    let output_dir = output_dir.unwrap_or_else(|| default_output_dir(&config));
    crate::k8s_run::run_k8s_benchmark(
        config,
        manifest,
        output_dir,
        keep_resources_on_failure,
        reliability_mode,
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::k8s_run::{
        active_backend_count, active_backend_counts_ready, comparison_entry,
        has_post_replay_scraped_benchmark_metrics, has_scraped_benchmark_metrics,
        routing_probe_cache_affinity_key, scraped_request_totals,
    };
    use crate::manifest::ManifestRequest;
    use crate::score::summarize_with_capacity;

    #[test]
    fn parses_active_backend_count_metric() {
        let metrics = r#"# HELP stargate_active_inference_servers Active inference servers available for a routing target
# TYPE stargate_active_inference_servers gauge
stargate_active_inference_servers{model="dummy-model",routing_key=""} 8
"#;

        assert_eq!(active_backend_count(metrics, "dummy-model", None), Some(8));
    }

    #[test]
    fn recognizes_collector_scraped_benchmark_metrics() {
        let metrics = r#"# HELP stargate_requests_total total
# TYPE stargate_requests_total counter
stargate_requests_total{model="dummy-model",routing_key="",status="ok"} 3
# HELP pylon_requests_total total
# TYPE pylon_requests_total counter
pylon_requests_total{model="dummy-model",routing_key="",status="complete"} 3
"#;

        assert!(has_scraped_benchmark_metrics(metrics));
    }

    #[test]
    fn rejects_collector_metrics_without_pylon_request_metrics() {
        let metrics = r#"# HELP stargate_requests_total total
# TYPE stargate_requests_total counter
stargate_requests_total{model="dummy-model",routing_key="",status="ok"} 3
"#;

        assert!(!has_scraped_benchmark_metrics(metrics));
    }

    #[test]
    fn post_replay_metrics_require_request_counter_progress_beyond_readiness_probe() {
        let baseline_metrics = r#"
stargate_requests_total_total{model="dummy-model",status="ok"} 1
pylon_requests_total_total{model="dummy-model",status="complete"} 1
"#;
        let stale_metrics = baseline_metrics;
        let updated_metrics = r#"
stargate_requests_total_total{model="dummy-model",status="ok"} 4
pylon_requests_total_total{model="dummy-model",status="complete"} 3
"#;
        let baseline = scraped_request_totals(baseline_metrics).expect("baseline should parse");

        assert!(!has_post_replay_scraped_benchmark_metrics(
            stale_metrics,
            baseline,
            3,
            2,
        ));
        assert!(has_post_replay_scraped_benchmark_metrics(
            updated_metrics,
            baseline,
            3,
            2,
        ));
    }

    #[test]
    fn active_backend_readiness_requires_every_metrics_endpoint() {
        assert!(active_backend_counts_ready(&[Some(4), Some(4)], 4));
        assert!(!active_backend_counts_ready(&[Some(4), Some(3)], 4));
        assert!(!active_backend_counts_ready(&[Some(4), None], 4));
    }

    #[test]
    fn routing_probe_uses_synthetic_cache_key() {
        let request = ManifestRequest {
            request_index: 7,
            request_id: "req-7".to_string(),
            scheduled_offset_ms: 0,
            routing_key: None,
            cache_affinity_key: Some("real-cache-key".to_string()),
            input_tokens: 128,
            output_tokens: 16,
            backend_behavior_class: "default".to_string(),
        };

        let probe_key = routing_probe_cache_affinity_key(&request)
            .expect("probe should include a cache key when the benchmark request has one");
        assert_ne!(probe_key, "real-cache-key");
        assert!(probe_key.contains("benchmark-ready-probe"));
    }

    #[test]
    fn run_info_reader_ignores_extra_run_metadata_fields() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let run_dir = tempdir.path().join("run-power-of-two");
        std::fs::create_dir(&run_dir).expect("run dir should create");
        std::fs::write(
            run_dir.join("run-info.json"),
            r#"{
                "algorithm_name": "groq-multiregion",
                "stargate_http_endpoint": "http://127.0.0.1:8000",
                "run_dir": "/tmp/stargate-bench/run-groq-multiregion",
                "backends_namespace": "stargate-bench-backends"
            }"#,
        )
        .expect("run-info should write");

        let algorithm_name = read_run_info(&run_dir)
            .expect("run-info extra fields should be ignored")
            .algorithm_name;

        assert_eq!(algorithm_name, "groq-multiregion");
    }

    #[test]
    fn scenario_name_resolves_to_bench_yaml() {
        assert_eq!(
            scenario_config_path("uniform-4-backends")
                .expect("scenario should resolve")
                .file_name()
                .and_then(|name| name.to_str()),
            Some("uniform-4-backends.yaml")
        );
        assert_eq!(
            scenario_config_path("uniform-4-backends.yaml")
                .expect("scenario with extension should resolve")
                .file_name()
                .and_then(|name| name.to_str()),
            Some("uniform-4-backends.yaml")
        );
    }

    #[test]
    fn queue_mismatch_ab_scenario_changes_only_admission_enabled_behavior() {
        let config =
            BenchmarkConfig::load(&scenario_config_path("queue-mismatch-retry-ab").unwrap())
                .expect("A/B scenario config should load");
        assert!(
            config.request_count >= 2048,
            "queue mismatch A/B evidence needs at least 2048 requests per arm"
        );
        assert_eq!(config.algorithms.len(), 2);
        assert_eq!(config.algorithms[0].config, config.algorithms[1].config);
        let enabled = config.algorithms[0]
            .pylon_queue_admission
            .as_ref()
            .expect("enabled variant should specify admission");
        let disabled = config.algorithms[1]
            .pylon_queue_admission
            .as_ref()
            .expect("disabled variant should specify admission");

        assert!(enabled.enabled);
        assert!(!disabled.enabled);
        assert_eq!(enabled.min_delta_ms, disabled.min_delta_ms);
        assert_eq!(enabled.tolerance_factor, disabled.tolerance_factor);
        assert_eq!(enabled.retry_after_ms, disabled.retry_after_ms);
    }

    #[test]
    fn algorithm_filter_keeps_requested_algorithms_in_config_order() {
        let mut config =
            BenchmarkConfig::load(&scenario_config_path("uniform-4-backends").unwrap())
                .expect("scenario config should load");

        filter_algorithms(
            &mut config,
            &["random".to_string(), "power-of-two".to_string()],
        )
        .expect("algorithm filter should succeed");

        assert_eq!(
            config
                .algorithms
                .iter()
                .map(|algorithm| algorithm.name.as_str())
                .collect::<Vec<_>>(),
            vec!["power-of-two", "random"]
        );
    }

    #[test]
    fn algorithm_filter_rejects_unknown_algorithms() {
        let mut config =
            BenchmarkConfig::load(&scenario_config_path("uniform-4-backends").unwrap())
                .expect("scenario config should load");

        let error = filter_algorithms(&mut config, &["missing".to_string()])
            .expect_err("unknown algorithm should fail");

        assert!(error.to_string().contains("unknown algorithm"));
    }

    #[test]
    fn load_balance_sweep_scenarios_cover_grouped_topologies_and_all_algorithms() {
        for (scenario, clusters, pylons_per_cluster, stargates) in [
            ("lb-balance-smoke-2c2p-1s", 2, 2, 1),
            ("lb-balance-bursty-4c2p-2s", 4, 2, 2),
            ("lb-balance-hotset-8c2p-4s", 8, 2, 4),
            ("lb-balance-prefix-reuse-smoke-2c2p-1s", 2, 2, 1),
            ("lb-balance-prefix-reuse-4c2p-2s", 4, 2, 2),
        ] {
            let config = BenchmarkConfig::load(&scenario_config_path(scenario).unwrap())
                .unwrap_or_else(|error| panic!("{scenario} should load: {error:#}"));
            config
                .validate()
                .unwrap_or_else(|error| panic!("{scenario} should validate: {error:#}"));
            assert_eq!(config.backends.cluster_count(), clusters, "{scenario}");
            assert_eq!(
                config.backends.pylons_per_cluster, pylons_per_cluster,
                "{scenario}"
            );
            assert_eq!(config.stargates.count, stargates, "{scenario}");
            let expected_algorithms = if scenario.contains("prefix-reuse") {
                vec![
                    "power-of-two",
                    "round-robin",
                    "random",
                    "groq-multiregion",
                    "groq-multiregion-affinity",
                    "pulsar",
                    "pulsar-consider-kv-free-tokens",
                    "pulsar-multiregion",
                    "pulsar-multiregion-consider-kv-free-tokens",
                ]
            } else {
                vec![
                    "power-of-two",
                    "round-robin",
                    "random",
                    "groq-multiregion",
                    "pulsar",
                ]
            };
            assert_eq!(
                config
                    .algorithms
                    .iter()
                    .map(|algorithm| algorithm.name.as_str())
                    .collect::<Vec<_>>(),
                expected_algorithms,
                "{scenario}"
            );
            if scenario.contains("prefix-reuse") {
                let affinity_gmr = config
                    .algorithms
                    .iter()
                    .find(|algorithm| algorithm.name == "groq-multiregion-affinity")
                    .expect("prefix-reuse scenario should include cache-affine GMR");
                let model_config = &affinity_gmr.config["models"]["dummy-model"];
                assert_eq!(model_config["algorithm"], "groq-multiregion", "{scenario}");
                assert_eq!(
                    model_config["require_cache_affinity_key"], true,
                    "{scenario}"
                );
                assert_eq!(
                    model_config["cache_affinity_backend_selection_count"], 1,
                    "{scenario}"
                );
                let pulsar_multiregion = config
                    .algorithms
                    .iter()
                    .find(|algorithm| algorithm.name == "pulsar-multiregion")
                    .expect("prefix-reuse scenario should include pulsar-multiregion");
                let model_config = &pulsar_multiregion.config["models"]["dummy-model"];
                assert_eq!(
                    model_config["algorithm"], "pulsar-multiregion",
                    "{scenario}"
                );
                assert_eq!(
                    model_config["require_cache_affinity_key"], true,
                    "{scenario}"
                );
                assert!(
                    model_config.get("queue_slo_ms").is_none(),
                    "{scenario} should not use the removed queue-SLO alias"
                );
                let pulsar = config
                    .algorithms
                    .iter()
                    .find(|algorithm| algorithm.name == "pulsar")
                    .expect("prefix-reuse scenario should include pulsar");
                assert_eq!(
                    model_config["seed"], pulsar.config["models"]["dummy-model"]["seed"],
                    "{scenario} should compare PULSAR fallback modes over the same cache-owner ranking"
                );
                for (name, algorithm) in [
                    ("pulsar-consider-kv-free-tokens", "pulsar"),
                    (
                        "pulsar-multiregion-consider-kv-free-tokens",
                        "pulsar-multiregion",
                    ),
                ] {
                    let kv_aware = config
                        .algorithms
                        .iter()
                        .find(|candidate| candidate.name == name)
                        .unwrap_or_else(|| panic!("{scenario} should include {name}"));
                    let kv_model = &kv_aware.config["models"]["dummy-model"];
                    assert_eq!(kv_model["algorithm"], algorithm, "{scenario}");
                    assert_eq!(kv_model["consider_kv_free_tokens"], true, "{scenario}");
                    assert_eq!(
                        kv_model["seed"], pulsar.config["models"]["dummy-model"]["seed"],
                        "{scenario} should compare KV consideration over the same cache-owner ranking"
                    );
                }
            }
        }
    }

    #[test]
    fn comparison_entry_contains_admission_configuration_and_proof_counters() {
        let mut config =
            BenchmarkConfig::load(&scenario_config_path("uniform-4-backends").unwrap())
                .expect("scenario config should load");
        let mut algorithm = config.algorithms.remove(0);
        algorithm.pylon_queue_admission = Some(crate::config::PylonQueueAdmissionConfig {
            enabled: true,
            min_delta_ms: Some(0),
            tolerance_factor: Some(1.0),
            retry_after_ms: Some(5),
        });
        let mut summary = summarize_with_capacity(&[], std::collections::BTreeMap::new());
        summary.p95_ttft_ms = Some(17);
        summary.queue_admission_summary = crate::score::QueueAdmissionSummary {
            pylon_rejected_count: 4.0,
            stargate_queue_mismatch_retry_count: 3.0,
            ..Default::default()
        };
        summary.routing_selection_summary = crate::score::RoutingSelectionSummary {
            primary_count: 5.0,
            fallback_count: 2.0,
            kv_free_token_fallback_count: 1.0,
        };

        let entry = comparison_entry(&algorithm, &summary);

        assert_eq!(entry["pylon_queue_admission"]["enabled"], true);
        assert_eq!(entry["p95_ttft_ms"], 17);
        assert_eq!(entry["queue_admission"]["pylon_rejected_count"], 4.0);
        assert_eq!(
            entry["queue_admission"]["stargate_queue_mismatch_retry_count"],
            3.0
        );
        assert_eq!(entry["routing_selection"]["fallback_count"], 2.0);
        assert_eq!(
            entry["routing_selection"]["kv_free_token_fallback_count"],
            1.0
        );
    }

    #[test]
    fn pulsar_multiregion_slo_scenario_is_a_controlled_fallback_comparison() {
        let config = BenchmarkConfig::load(
            &scenario_config_path("lb-balance-prefix-reuse-pmr-slo-4c2p-1s").unwrap(),
        )
        .expect("controlled PMR fallback scenario should load");

        config
            .validate()
            .expect("controlled PMR fallback scenario should validate");
        assert_eq!(config.stargates.count, 1);
        assert_eq!(config.backends.cluster_count(), 4);
        assert_eq!(config.backends.pylons_per_cluster, 2);
        assert_eq!(
            config
                .algorithms
                .iter()
                .map(|algorithm| algorithm.name.as_str())
                .collect::<Vec<_>>(),
            vec!["groq-multiregion-affinity", "pulsar", "pulsar-multiregion"]
        );
        assert!(config.algorithms.iter().all(|algorithm| {
            algorithm
                .pylon_queue_admission
                .as_ref()
                .is_some_and(|admission| !admission.enabled)
        }));
        match &config.traffic_pattern {
            crate::config::TrafficPatternConfig::PrefixReuse(prefix) => assert_eq!(
                prefix.arrival,
                crate::config::ArrivalPatternConfig::Poisson { target_rps: 8.0 },
                "controlled PMR fallback evidence must not overload every candidate at once"
            ),
            _ => panic!("controlled PMR scenario must use growing prefixes"),
        }
        let pmr = config
            .algorithms
            .iter()
            .find(|algorithm| algorithm.name == "pulsar-multiregion")
            .expect("scenario should include pulsar-multiregion");
        assert_eq!(
            pmr.config["models"]["dummy-model"]["max_queue_time_floor_ms"], 4000,
            "controlled PMR scenario must leave enough queue budget for useful fallback routing"
        );
        assert_eq!(
            pmr.config["models"]["dummy-model"]["max_queue_time_ceil_ms"], 4000,
            "controlled PMR scenario must leave enough queue budget for useful fallback routing"
        );
    }
}

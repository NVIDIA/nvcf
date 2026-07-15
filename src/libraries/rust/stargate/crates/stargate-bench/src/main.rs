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

use std::collections::{BTreeMap, BTreeSet};
use std::fmt::Write;
use std::path::{Path, PathBuf};

use anyhow::Context;
use clap::{Args, Parser, Subcommand};

use crate::config::{BenchmarkConfig, PylonQueueAdmissionConfig};
use crate::driver::{DriveConfig, drive_manifest, load_manifest};
use crate::manifest::{generate_manifest, write_manifest_json};
use crate::metadata::ReliabilityMode;
use crate::microbench::{
    BodyBufferMicrobenchConfig, HeaderFilterMicrobenchConfig, LbMicrobenchConfig,
    LbMicrobenchScenario, render_body_buffer_microbench_report,
    render_header_filter_microbench_report, run_body_buffer_microbench,
    run_header_filter_microbench, run_lb_microbench, write_lb_microbench_csv,
};
use crate::orchestrator::prepare_suite;
use crate::report::{ReportContext, ReportEntry, write_markdown_report_artifact};
use crate::score::RunSummary;
use crate::transport::{TransportBenchConfig, run_transport_benchmark_command};

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
    Materialize(BenchmarkInputArgs),
    /// Prepare per-algorithm local run directories with docker-compose and LB configs
    PrepareRun(BenchmarkInputArgs),
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
    Run(RunArgs),
    /// Compare Raw QUIC, HTTP/3, and WebTransport tunnel transports on loopback
    TransportBench(TransportBenchArgs),
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
    HeaderFilterMicrobench(HeaderFilterMicrobenchConfig),
    /// Compare Pylon-style request body buffering copy strategies
    BodyBufferMicrobench(BodyBufferMicrobenchConfig),
}

#[derive(Args, Debug, Clone)]
#[cfg_attr(test, derive(serde::Serialize))]
struct BenchmarkSourceArgs {
    /// Path to a benchmark YAML/JSON config
    #[arg(long, conflicts_with = "scenario", value_name = "PATH")]
    config: Option<PathBuf>,
    /// Scenario alias from benches/*.yaml, for example hotset-8-backends
    #[arg(long, conflicts_with = "config", value_name = "NAME")]
    scenario: Option<String>,
}

#[derive(Args, Debug)]
#[cfg_attr(test, derive(serde::Serialize))]
struct BenchmarkInputArgs {
    #[command(flatten)]
    source: BenchmarkSourceArgs,
    #[arg(long, value_name = "SEED")]
    seed: Option<u64>,
    #[arg(long = "algorithm", value_name = "NAME")]
    algorithms: Vec<String>,
    #[arg(long, value_name = "PATH")]
    output_dir: Option<PathBuf>,
}

#[derive(Args, Debug)]
#[cfg_attr(test, derive(serde::Serialize))]
struct RunArgs {
    #[command(flatten)]
    input: BenchmarkInputArgs,
    #[arg(long)]
    keep_resources_on_failure: bool,
    #[arg(long, value_enum, default_value_t = ReliabilityMode::Smoke, value_name = "MODE")]
    reliability_mode: ReliabilityMode,
}

#[derive(Args, Debug)]
#[cfg_attr(test, derive(serde::Serialize))]
struct TransportBenchArgs {
    #[command(flatten)]
    config: TransportBenchConfig,
    #[arg(long, value_enum, default_value_t = ReliabilityMode::Smoke, value_name = "MODE")]
    reliability_mode: ReliabilityMode,
    #[arg(long, value_name = "PATH")]
    output_dir: Option<PathBuf>,
}

struct Scenario {
    name: String,
    path: PathBuf,
}

fn render_scenario_list(scenarios: Vec<Scenario>) -> anyhow::Result<String> {
    if scenarios.is_empty() {
        return Ok("no benchmark scenarios found under benches/\n".to_string());
    }
    let mut rendered = format!(
        "{:<28} {:>8} {:>9} {:>8}  {:<24}  Algorithms\n",
        "Scenario", "Requests", "Backends", "Stargates", "Tags"
    );
    for scenario in scenarios {
        let config = BenchmarkConfig::load(&scenario.path)?;
        writeln!(
            rendered,
            "{:<28} {:>8} {:>9} {:>8}  {:<24}  {}",
            scenario.name,
            config.request_count,
            config.backends.count,
            config.stargates.count,
            config.metadata.tags.join(","),
            config
                .algorithms
                .iter()
                .map(|algorithm| algorithm.name.as_str())
                .collect::<Vec<_>>()
                .join(",")
        )?;
    }
    Ok(rendered)
}

#[derive(serde::Deserialize)]
struct RunInfo {
    algorithm_name: String,
    #[serde(default)]
    pylon_queue_admission: Option<PylonQueueAdmissionConfig>,
}

fn load_benchmark_input(
    source: BenchmarkSourceArgs,
    seed: Option<u64>,
    algorithms: &[String],
) -> anyhow::Result<(BenchmarkConfig, crate::manifest::Manifest)> {
    let mut config = BenchmarkConfig::load(&resolve_config_path(source)?)?;
    filter_algorithms(&mut config, algorithms)?;
    let manifest = generate_manifest(&config, seed)?;
    Ok((config, manifest))
}

impl BenchmarkInputArgs {
    fn load(self) -> anyhow::Result<(BenchmarkConfig, crate::manifest::Manifest, PathBuf)> {
        let (config, manifest) = load_benchmark_input(self.source, self.seed, &self.algorithms)?;
        let output_dir = self
            .output_dir
            .unwrap_or_else(|| Path::new(".bench-out").join(&config.name));
        Ok((config, manifest, output_dir))
    }
}

fn main() -> anyhow::Result<()> {
    Cli::parse().command.execute()
}

impl Command {
    fn execute(self) -> anyhow::Result<()> {
        match self {
            Command::ListScenarios => {
                print!(
                    "{}",
                    render_scenario_list(discover_scenarios_in(&benches_dir())?)?
                );
                Ok(())
            }
            Command::InspectManifest { source, seed } => {
                let (_, manifest) = load_benchmark_input(source, seed, &[])?;
                let rendered = serde_json::to_string_pretty(&manifest)
                    .context("failed to render manifest as JSON")?;
                println!("{rendered}");
                Ok(())
            }
            Command::Materialize(args) => materialize(args),
            Command::PrepareRun(args) => prepare_run(args),
            Command::Drive {
                manifest,
                endpoint,
                output,
                concurrency_limit,
            } => drive(&manifest, &endpoint, &output, concurrency_limit),
            Command::Report { output_dir } => regenerate_report(&output_dir),
            Command::Run(args) => {
                let (config, manifest, output_dir) = args.input.load()?;
                crate::k8s_run::run_k8s_benchmark(
                    config,
                    manifest,
                    output_dir,
                    args.keep_resources_on_failure,
                    args.reliability_mode,
                )
            }
            Command::TransportBench(args) => {
                run_transport_benchmark_command(args.config, args.reliability_mode, args.output_dir)
            }
            Command::LbMicrobench {
                iterations,
                warmup_iterations,
                concurrency,
                candidates,
                cache_key_count,
                scenarios,
            } => {
                let rows = run_lb_microbench(&LbMicrobenchConfig {
                    iterations,
                    warmup_iterations,
                    concurrency,
                    candidates,
                    cache_key_count,
                    scenarios,
                })?;
                write_lb_microbench_csv(std::io::stdout(), &rows)
                    .context("failed to write lb microbench CSV")
            }
            Command::HeaderFilterMicrobench(config) => {
                let outcome = run_header_filter_microbench(config)?;
                print!("{}", render_header_filter_microbench_report(&outcome));
                Ok(())
            }
            Command::BodyBufferMicrobench(config) => {
                let outcome = run_body_buffer_microbench(config)?;
                print!("{}", render_body_buffer_microbench_report(&outcome));
                Ok(())
            }
        }
    }
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
    scenario_config_path_in(&benches_dir(), scenario)
}

fn scenario_config_path_in(benches_dir: &Path, scenario: &str) -> anyhow::Result<PathBuf> {
    let name = normalize_scenario_name(scenario)?;
    for path in [
        benches_dir.join(format!("{name}.yaml")),
        benches_dir.join(format!("{name}.yml")),
    ] {
        if path.exists() {
            return Ok(path);
        }
    }

    let available = discover_scenarios_in(benches_dir)?
        .into_iter()
        .map(|scenario| scenario.name)
        .collect::<Vec<_>>()
        .join(", ");
    anyhow::bail!("unknown benchmark scenario '{name}'. Available scenarios: {available}")
}

fn normalize_scenario_name(scenario: &str) -> anyhow::Result<&str> {
    let name = scenario
        .strip_suffix(".yaml")
        .or_else(|| scenario.strip_suffix(".yml"))
        .unwrap_or(scenario);
    if name.contains('/') || name.contains('\\') || name.is_empty() {
        anyhow::bail!("scenario names must be file stems from benches/*.yaml");
    }
    Ok(name)
}

fn discover_scenarios_in(benches_dir: &Path) -> anyhow::Result<Vec<Scenario>> {
    let mut scenarios = BTreeMap::new();
    let entries = match std::fs::read_dir(benches_dir) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(error).context("failed to read benches directory"),
    };
    for entry in entries {
        let entry = entry.context("failed to read benches directory entry")?;
        let path = entry.path();
        let Some(extension @ ("yaml" | "yml")) = path.extension().and_then(|ext| ext.to_str())
        else {
            continue;
        };
        let Some(name) = path.file_stem().and_then(|stem| stem.to_str()) else {
            continue;
        };
        if extension == "yaml" || !scenarios.contains_key(name) {
            scenarios.insert(name.to_string(), path);
        }
    }
    Ok(scenarios
        .into_iter()
        .map(|(name, path)| Scenario { name, path })
        .collect())
}

fn filter_algorithms(config: &mut BenchmarkConfig, requested: &[String]) -> anyhow::Result<()> {
    if requested.is_empty() {
        return Ok(());
    }

    let requested = requested
        .iter()
        .map(String::as_str)
        .collect::<BTreeSet<_>>();
    let available = config
        .algorithms
        .iter()
        .map(|algorithm| algorithm.name.as_str())
        .collect::<BTreeSet<_>>();
    let unknown = requested
        .difference(&available)
        .copied()
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
        .retain(|algorithm| requested.contains(algorithm.name.as_str()));
    Ok(())
}

fn benches_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .ancestors()
        .nth(2)
        .unwrap_or_else(|| Path::new("."))
        .join(BENCHES_DIR)
}

fn materialize(args: BenchmarkInputArgs) -> anyhow::Result<()> {
    let (config, manifest, output_dir) = args.load()?;
    std::fs::create_dir_all(&output_dir)
        .with_context(|| format!("failed to create output dir {}", output_dir.display()))?;

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
        "metadata": config.metadata,
        "model": config.model,
        "seed": manifest.seed,
        "request_count": manifest.request_count,
        "stargate_count": manifest.stargate_count,
        "backend_count": manifest.backend_count,
        "algorithm_names": config.algorithms.iter().map(|algorithm| algorithm.name.clone()).collect::<Vec<_>>(),
    });
    let summary_bytes =
        serde_json::to_vec_pretty(&summary).context("failed to serialize summary")?;
    std::fs::write(&summary_path, summary_bytes)
        .with_context(|| format!("failed to write {}", summary_path.display()))?;

    let rendered_output_dir = output_dir
        .canonicalize()
        .unwrap_or_else(|_| output_dir.to_path_buf());
    println!(
        "materialized benchmark input at {}",
        rendered_output_dir.display()
    );
    Ok(())
}

fn prepare_run(args: BenchmarkInputArgs) -> anyhow::Result<()> {
    let (config, manifest, output_dir) = args.load()?;
    let prepared = prepare_suite(&config, &manifest, &output_dir)?;
    let rendered_output_dir = output_dir
        .canonicalize()
        .unwrap_or_else(|_| output_dir.to_path_buf());
    println!(
        "prepared {} algorithm runs at {}",
        prepared.algorithm_runs.len(),
        rendered_output_dir.display()
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

fn regenerate_report(output_dir: &Path) -> anyhow::Result<()> {
    let manifest = load_manifest(&output_dir.join("manifest.json"))?;
    let context = ReportContext::from_manifest(&manifest);
    let mut entries = Vec::new();
    for entry in std::fs::read_dir(output_dir)
        .with_context(|| format!("failed to read output dir {}", output_dir.display()))?
    {
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
    let report_path = output_dir.join("report.md");
    write_markdown_report_artifact(&report_path, &context, &entries)?;
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::k8s_run::{
        active_backend_count, active_backend_counts_ready,
        has_post_replay_scraped_benchmark_metrics, has_scraped_benchmark_metrics,
        routing_probe_cache_affinity_key, scraped_request_totals,
    };
    use crate::manifest::Manifest;
    use crate::score::summarize_with_capacity;
    use clap::error::ErrorKind;
    const TRANSPORT_DISABLED: &str =
        "stargate-bench transport-bench --disable-quic-send-fairness --disable-http3-grease";
    const MATERIALIZE_ARGS: &str = "stargate-bench materialize --scenario uniform-4-backends --seed 7 --algorithm power-of-two --output-dir out";
    const PREPARE_ARGS: &str = "stargate-bench prepare-run --config config.yaml";
    const RUN_ARGS: &str = "stargate-bench run --scenario uniform-4-backends --keep-resources-on-failure --reliability-mode controlled";
    const STARGATE_REQUEST_METRIC: &str = concat!(
        "# HELP stargate_requests_total total\n# TYPE stargate_requests_total counter\n",
        "stargate_requests_total{model=\"dummy-model\",routing_key=\"\",status=\"ok\"} 3\n"
    );
    const PYLON_REQUEST_METRIC: &str = concat!(
        "# HELP pylon_requests_total total\n# TYPE pylon_requests_total counter\n",
        "pylon_requests_total{model=\"dummy-model\",routing_key=\"\",status=\"complete\"} 3\n"
    );
    macro_rules! command_json {
        ($command:expr, $variant:ident) => {{
            let Command::$variant(args) = Cli::try_parse_from($command.split_ascii_whitespace())
                .expect("command should parse")
                .command
            else {
                panic!("unexpected command variant");
            };
            serde_json::to_string(&args).expect("command args should serialize")
        }};
    }

    #[test]
    fn cli_preserves_transport_defaults_and_disable_flags() {
        assert_eq!(
            command_json!(TRANSPORT_DISABLED, TransportBench),
            r#"{"config":{"request_count":20000,"concurrency":256,"quic_connections":1,"warmup_requests":1000,"request_body_bytes":1024,"response_body_bytes":1024,"request_chunk_bytes":16384,"response_chunk_bytes":16384,"quic_send_fairness":false,"http3_send_grease":false,"trials":1,"warmup_trials":0,"cooldown_ms":0,"randomize_order":false,"noise_threshold_cv":0.02,"min_effect_size_percent":1.0},"reliability_mode":"smoke","output_dir":null}"#
        );
        let defaults = command_json!("stargate-bench transport-bench", TransportBench);
        assert!(
            defaults.contains(r#""quic_send_fairness":true"#)
                && defaults.contains(r#""http3_send_grease":true"#)
        );
    }

    #[test]
    fn cli_preserves_shared_materialize_prepare_and_run_payloads() {
        let conflict = Cli::try_parse_from(
            "stargate-bench materialize --config config.yaml --scenario uniform-4-backends"
                .split_ascii_whitespace(),
        )
        .expect_err("config and scenario should conflict");
        assert_eq!(conflict.kind(), ErrorKind::ArgumentConflict);
        assert_eq!(
            command_json!(MATERIALIZE_ARGS, Materialize),
            r#"{"source":{"config":null,"scenario":"uniform-4-backends"},"seed":7,"algorithms":["power-of-two"],"output_dir":"out"}"#
        );
        assert_eq!(
            command_json!(PREPARE_ARGS, PrepareRun),
            r#"{"source":{"config":"config.yaml","scenario":null},"seed":null,"algorithms":[],"output_dir":null}"#
        );
        assert_eq!(
            command_json!(RUN_ARGS, Run),
            r#"{"input":{"source":{"config":null,"scenario":"uniform-4-backends"},"seed":null,"algorithms":[],"output_dir":null},"keep_resources_on_failure":true,"reliability_mode":"controlled"}"#
        );
    }

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
        assert!(has_scraped_benchmark_metrics(&format!(
            "{STARGATE_REQUEST_METRIC}{PYLON_REQUEST_METRIC}"
        )));
    }

    #[test]
    fn rejects_collector_metrics_without_pylon_request_metrics() {
        assert!(!has_scraped_benchmark_metrics(STARGATE_REQUEST_METRIC));
    }

    #[test]
    fn post_replay_metrics_require_request_counter_progress_beyond_readiness_probe() {
        let baseline_metrics = r#"
stargate_requests_total_total{model="dummy-model",status="ok"} 1
pylon_requests_total_total{model="dummy-model",status="complete"} 1
"#;
        let updated_metrics = r#"
stargate_requests_total_total{model="dummy-model",status="ok"} 4
pylon_requests_total_total{model="dummy-model",status="complete"} 3
"#;
        let baseline = scraped_request_totals(baseline_metrics).expect("baseline should parse");

        for (metrics, expected) in [(baseline_metrics, false), (updated_metrics, true)] {
            assert_eq!(
                has_post_replay_scraped_benchmark_metrics(metrics, baseline, 3, 2),
                expected
            );
        }
    }

    #[test]
    fn active_backend_readiness_requires_every_metrics_endpoint() {
        assert!(active_backend_counts_ready(&[Some(4), Some(4)], 4));
        assert!(!active_backend_counts_ready(&[Some(4), Some(3)], 4));
        assert!(!active_backend_counts_ready(&[Some(4), None], 4));
    }

    #[test]
    fn routing_probe_uses_synthetic_cache_key() {
        let request = generate_manifest(&load_scenario("sticky-hot-prefix"), Some(7))
            .expect("cache scenario manifest should generate")
            .requests
            .into_iter()
            .next()
            .expect("cache scenario should contain a request");
        let cache_key = request
            .cache_affinity_key
            .as_deref()
            .expect("cache scenario request should include a cache key");

        let probe_key = routing_probe_cache_affinity_key(&request)
            .expect("probe should include a cache key when the benchmark request has one");
        assert_ne!(probe_key, cache_key);
        assert!(probe_key.contains("benchmark-ready-probe"));
    }

    #[test]
    fn real_command_execution_runs_low_cost_microbench_commands() {
        Command::ListScenarios
            .execute()
            .expect("scenario listing should run");
        Command::LbMicrobench {
            iterations: 1,
            warmup_iterations: 0,
            concurrency: 1,
            candidates: 2,
            cache_key_count: 1,
            scenarios: vec![LbMicrobenchScenario::PowerOfTwo],
        }
        .execute()
        .expect("real command should run lb microbench");

        Command::HeaderFilterMicrobench(HeaderFilterMicrobenchConfig {
            iterations: 1,
            warmup_iterations: 0,
            header_count: 1,
        })
        .execute()
        .expect("real command should run header filter microbench");

        Command::BodyBufferMicrobench(BodyBufferMicrobenchConfig {
            iterations: 1,
            warmup_iterations: 0,
            body_bytes: 1,
            chunk_bytes: 1,
        })
        .execute()
        .expect("real command should run body buffer microbench");
    }

    #[test]
    fn run_info_reader_ignores_extra_run_metadata_fields() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let run_dir = tempdir.path().join("run-power-of-two");
        std::fs::create_dir(&run_dir).expect("run dir should create");
        std::fs::write(
            run_dir.join("run-info.json"),
            r#"{"algorithm_name":"groq-multiregion","extra_metadata":"ignored"}"#,
        )
        .expect("run-info should write");

        assert_eq!(
            read_run_info(&run_dir)
                .expect("run-info extra fields should be ignored")
                .algorithm_name,
            "groq-multiregion"
        );
    }

    #[test]
    fn regenerate_report_writes_markdown_from_run_entries() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        write_manifest_json(
            &tempdir.path().join("manifest.json"),
            &manifest_for_report_test(),
        )
        .expect("manifest should write");
        let run_dir = tempdir.path().join("run-groq-admission-enabled");
        std::fs::create_dir(&run_dir).expect("run dir should create");
        let summary = summarize_with_capacity(&[], std::collections::BTreeMap::new());
        std::fs::write(
            run_dir.join("summary.json"),
            serde_json::to_vec_pretty(&summary).expect("summary should serialize"),
        )
        .expect("summary should write");
        std::fs::write(
            run_dir.join("run-info.json"),
            r#"{
                "algorithm_name": "groq-admission-enabled",
                "pylon_queue_admission": {
                    "enabled": true,
                    "min_delta_ms": 0,
                    "tolerance_factor": 1.0,
                    "retry_after_ms": 5
                }
            }"#,
        )
        .expect("run-info should write");
        std::fs::create_dir(tempdir.path().join("run-missing-summary"))
            .expect("ignored run dir should create");

        regenerate_report(tempdir.path()).expect("report should regenerate");

        let report =
            std::fs::read_to_string(tempdir.path().join("report.md")).expect("report should read");
        assert!(report.contains("# Benchmark Report: report-regeneration-test"));
        assert!(report.contains("| groq-admission-enabled | enabled"));
        assert!(!report.contains("run-missing-summary"));
    }

    fn write_scenario_config(path: &Path) {
        let mut config = load_scenario("sticky-hot-prefix");
        config.name = "alpha".to_string();
        config.request_count = 7;
        config.backends.count = 3;
        config.stargates.count = 2;
        config.metadata.tags = ["smoke", "cache"].map(str::to_string).to_vec();
        config
            .algorithms
            .retain(|candidate| ["round-robin", "pulsar"].contains(&candidate.name.as_str()));
        config.validate().expect("scenario fixture should validate");
        std::fs::write(
            path,
            serde_yaml_ng::to_string(&config).expect("scenario config should serialize"),
        )
        .expect("scenario config should write");
    }

    fn manifest_for_report_test() -> Manifest {
        let mut manifest = generate_manifest(&load_scenario("uniform-4-backends"), Some(7))
            .expect("report manifest should generate");
        manifest.benchmark_name = "report-regeneration-test".to_string();
        manifest.metadata = Default::default();
        manifest.request_count = 0;
        manifest.requests.clear();
        manifest
    }

    fn load_scenario(name: &str) -> BenchmarkConfig {
        BenchmarkConfig::load(&scenario_config_path(name).expect("scenario should resolve"))
            .unwrap_or_else(|error| panic!("{name} should load: {error:#}"))
    }

    fn source(config: Option<&str>, scenario: Option<&str>) -> BenchmarkSourceArgs {
        BenchmarkSourceArgs {
            config: config.map(PathBuf::from),
            scenario: scenario.map(str::to_string),
        }
    }

    fn benchmark_args(output_dir: Option<PathBuf>) -> BenchmarkInputArgs {
        BenchmarkInputArgs {
            source: source(None, Some("uniform-4-backends")),
            seed: Some(123),
            algorithms: ["random", "power-of-two"].map(str::to_string).to_vec(),
            output_dir,
        }
    }

    fn algorithm_names(config: &BenchmarkConfig) -> Vec<&str> {
        config
            .algorithms
            .iter()
            .map(|algorithm| algorithm.name.as_str())
            .collect()
    }

    fn model_config<'a>(
        config: &'a BenchmarkConfig,
        algorithm_name: &str,
    ) -> &'a serde_json::Value {
        &config
            .algorithms
            .iter()
            .find(|algorithm| algorithm.name == algorithm_name)
            .unwrap_or_else(|| panic!("scenario should include {algorithm_name}"))
            .config["models"]["dummy-model"]
    }

    fn topology(config: &BenchmarkConfig) -> (usize, usize, usize) {
        (
            config.backends.cluster_count(),
            config.backends.pylons_per_cluster,
            config.stargates.count,
        )
    }

    #[test]
    fn scenario_name_resolves_to_bench_yaml() {
        for name in ["uniform-4-backends", "uniform-4-backends.yaml"] {
            assert!(
                scenario_config_path(name)
                    .unwrap_or_else(|error| panic!("{name} should resolve: {error:#}"))
                    .ends_with("uniform-4-backends.yaml")
            );
        }
    }

    #[test]
    fn scenario_yml_name_resolves_in_injected_benches_dir() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let scenario_path = tempdir.path().join("raw-quic-scenario.yml");
        std::fs::write(&scenario_path, "name: raw-quic-scenario\n")
            .expect("scenario file should write");

        for name in ["raw-quic-scenario", "raw-quic-scenario.yml"] {
            assert_eq!(
                scenario_config_path_in(tempdir.path(), name)
                    .unwrap_or_else(|error| panic!("{name} should resolve: {error:#}")),
                scenario_path
            );
        }
    }

    #[test]
    fn scenario_discovery_lists_yaml_and_yml_configs() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        for file in ["alpha.yml", "beta.yml", "beta.yaml", "ignored.json"] {
            std::fs::write(tempdir.path().join(file), "")
                .unwrap_or_else(|error| panic!("{file} should write: {error}"));
        }

        let scenarios =
            discover_scenarios_in(tempdir.path()).expect("scenario discovery should work");

        assert_eq!(
            scenarios
                .iter()
                .map(|scenario| scenario.name.as_str())
                .collect::<Vec<_>>(),
            vec!["alpha", "beta"]
        );
        assert_eq!(
            scenarios[1].path.file_name().and_then(|name| name.to_str()),
            Some("beta.yaml")
        );
    }

    #[test]
    fn scenario_list_renders_empty_message_and_loaded_rows() {
        assert_eq!(
            render_scenario_list(Vec::new()).expect("empty list should render"),
            "no benchmark scenarios found under benches/\n"
        );

        let tempdir = tempfile::tempdir().expect("tempdir should create");
        write_scenario_config(&tempdir.path().join("alpha.yaml"));
        let scenarios =
            discover_scenarios_in(tempdir.path()).expect("scenario discovery should work");

        let expected_header = format!(
            "{:<28} {:>8} {:>9} {:>8}  {:<24}  Algorithms",
            "Scenario", "Requests", "Backends", "Stargates", "Tags"
        );
        let expected_row = format!(
            "{:<28} {:>8} {:>9} {:>8}  {:<24}  {}",
            "alpha", 7, 3, 2, "smoke,cache", "round-robin,pulsar"
        );
        assert_eq!(
            render_scenario_list(scenarios).expect("scenario list should render"),
            format!("{expected_header}\n{expected_row}\n")
        );
    }

    #[test]
    fn resolve_config_path_handles_direct_scenario_missing_and_ambiguous_sources() {
        let direct_config = PathBuf::from("does-not-need-to-exist.json");
        assert_eq!(
            resolve_config_path(source(Some("does-not-need-to-exist.json"), None))
                .expect("direct config path should pass through"),
            direct_config
        );
        assert!(
            resolve_config_path(source(None, Some("uniform-4-backends")))
                .expect("scenario should resolve")
                .ends_with("uniform-4-backends.yaml")
        );

        let missing =
            resolve_config_path(source(None, None)).expect_err("missing source should fail");
        assert!(missing.to_string().contains("provide either"));

        let ambiguous =
            resolve_config_path(source(Some("config.yaml"), Some("uniform-4-backends")))
                .expect_err("ambiguous source should fail");
        assert!(ambiguous.to_string().contains("provide only one"));
    }

    #[test]
    fn benchmark_input_filters_algorithms_generates_seed_and_derives_output_dir() {
        let (config, manifest, output_dir) = benchmark_args(None)
            .load()
            .expect("benchmark input should load");

        assert_eq!(manifest.seed, 123);
        assert_eq!(algorithm_names(&config), ["power-of-two", "random"]);
        assert_eq!(output_dir, Path::new(".bench-out").join(&config.name));
    }

    #[test]
    fn materialize_uses_benchmark_input_for_filtered_manifest_and_summary() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");

        materialize(benchmark_args(Some(tempdir.path().to_path_buf())))
            .expect("materialize should complete");

        let manifest =
            load_manifest(&tempdir.path().join("manifest.json")).expect("manifest should load");
        let config_copy =
            read_json::<BenchmarkConfig>(&tempdir.path().join("benchmark-config.json"))
                .expect("config copy should load");
        let summary = read_json::<serde_json::Value>(&tempdir.path().join("summary.json"))
            .expect("summary should load");

        assert_eq!(manifest.seed, 123);
        assert_eq!(algorithm_names(&config_copy), ["power-of-two", "random"]);
        assert_eq!(summary["seed"], 123);
        assert_eq!(
            summary["algorithm_names"],
            serde_json::json!(["power-of-two", "random"])
        );
    }

    #[test]
    fn queue_mismatch_ab_scenario_changes_only_admission_enabled_behavior() {
        let config = load_scenario("queue-mismatch-retry-ab");
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

        let mut expected_disabled = enabled.clone();
        assert!(expected_disabled.enabled && !disabled.enabled);
        expected_disabled.enabled = false;
        assert_eq!(&expected_disabled, disabled);
    }

    #[test]
    fn algorithm_filter_keeps_requested_algorithms_in_config_order() {
        let mut config = load_scenario("uniform-4-backends");

        filter_algorithms(&mut config, &benchmark_args(None).algorithms)
            .expect("algorithm filter should succeed");

        assert_eq!(algorithm_names(&config), ["power-of-two", "random"]);
    }

    #[test]
    fn algorithm_filter_rejects_unknown_algorithms() {
        let mut config = load_scenario("uniform-4-backends");

        let error = filter_algorithms(&mut config, &["missing".to_string()])
            .expect_err("unknown algorithm should fail");

        assert!(error.to_string().contains("unknown algorithm"));
    }

    #[test]
    fn load_balance_sweep_scenarios_cover_grouped_topologies_and_all_algorithms() {
        const STANDARD_ALGORITHMS: &[&str] = &[
            "power-of-two",
            "round-robin",
            "random",
            "groq-multiregion",
            "pulsar",
        ];
        const PREFIX_ALGORITHMS: &[&str] = &[
            "power-of-two",
            "round-robin",
            "random",
            "groq-multiregion",
            "groq-multiregion-affinity",
            "pulsar",
            "pulsar-consider-kv-free-tokens",
            "pulsar-multiregion",
            "pulsar-multiregion-consider-kv-free-tokens",
        ];
        for (scenario, clusters, pylons_per_cluster, stargates) in [
            ("lb-balance-smoke-2c2p-1s", 2, 2, 1),
            ("lb-balance-bursty-4c2p-2s", 4, 2, 2),
            ("lb-balance-hotset-8c2p-4s", 8, 2, 4),
            ("lb-balance-prefix-reuse-smoke-2c2p-1s", 2, 2, 1),
            ("lb-balance-prefix-reuse-4c2p-2s", 4, 2, 2),
        ] {
            let config = load_scenario(scenario);
            config
                .validate()
                .unwrap_or_else(|error| panic!("{scenario} should validate: {error:#}"));
            assert_eq!(
                topology(&config),
                (clusters, pylons_per_cluster, stargates),
                "{scenario}"
            );
            let prefix_reuse = scenario.contains("prefix-reuse");
            let expected_algorithms = if prefix_reuse {
                PREFIX_ALGORITHMS
            } else {
                STANDARD_ALGORITHMS
            };
            assert_eq!(algorithm_names(&config), expected_algorithms, "{scenario}");
            if prefix_reuse {
                let affinity = model_config(&config, "groq-multiregion-affinity");
                assert_eq!(
                    (
                        affinity["algorithm"].as_str(),
                        affinity["require_cache_affinity_key"].as_bool(),
                        affinity["cache_affinity_backend_selection_count"].as_u64(),
                    ),
                    (Some("groq-multiregion"), Some(true), Some(1)),
                    "{scenario}"
                );
                let multiregion = model_config(&config, "pulsar-multiregion");
                assert_eq!(
                    (
                        multiregion["algorithm"].as_str(),
                        multiregion["require_cache_affinity_key"].as_bool(),
                    ),
                    (Some("pulsar-multiregion"), Some(true)),
                    "{scenario}"
                );
                let pulsar = model_config(&config, "pulsar");
                assert_eq!(
                    multiregion["seed"], pulsar["seed"],
                    "{scenario} should compare PULSAR fallback modes over the same cache-owner ranking"
                );
                for (name, algorithm) in [
                    ("pulsar-consider-kv-free-tokens", "pulsar"),
                    (
                        "pulsar-multiregion-consider-kv-free-tokens",
                        "pulsar-multiregion",
                    ),
                ] {
                    let kv_model = model_config(&config, name);
                    assert_eq!(
                        (
                            kv_model["algorithm"].as_str(),
                            kv_model["consider_kv_free_tokens"].as_bool(),
                            kv_model["seed"].as_u64(),
                        ),
                        (Some(algorithm), Some(true), pulsar["seed"].as_u64()),
                        "{scenario}"
                    );
                }
            }
        }
    }

    #[test]
    fn pulsar_multiregion_slo_scenario_is_a_controlled_fallback_comparison() {
        let config = load_scenario("lb-balance-prefix-reuse-pmr-slo-4c2p-1s");

        config
            .validate()
            .expect("controlled PMR fallback scenario should validate");
        assert_eq!(topology(&config), (4, 2, 1));
        assert_eq!(
            algorithm_names(&config),
            ["groq-multiregion-affinity", "pulsar", "pulsar-multiregion"]
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
        let pmr = model_config(&config, "pulsar-multiregion");
        for key in ["max_queue_time_floor_ms", "max_queue_time_ceil_ms"] {
            assert_eq!(
                pmr[key], 4000,
                "controlled PMR scenario must leave enough queue budget for useful fallback routing"
            );
        }
    }
}

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

mod backend;
mod config;
mod drive;
mod fleet;
mod metrics;
mod sim;
mod time;
mod workload;

use std::collections::HashMap;
use std::num::NonZeroUsize;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::sync::atomic::{AtomicUsize, Ordering};

use anyhow::Context;
use clap::{Parser, Subcommand};

use crate::config::SimConfig;
use crate::drive::{DriveArgs, FleetRecord};
use crate::metrics::RunSummary;
use crate::sim::RunSpec;

#[derive(Parser, Debug)]
#[command(name = "stargate-routing-sim")]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand, Debug)]
enum Command {
    /// Simulate every seed, rate, and policy in a config.
    Simulate {
        /// Simulation config (JSON).
        #[arg(long)]
        config: PathBuf,
        /// Write every run summary to this JSON file.
        #[arg(long)]
        output: Option<PathBuf>,
        /// Parallel worker threads. Defaults to available parallelism.
        #[arg(long)]
        jobs: Option<NonZeroUsize>,
    },
    /// Send one region's share of a config's workload to a deployed Stargate.
    Drive {
        /// Simulation config (JSON) that defines the workload.
        #[arg(long)]
        config: PathBuf,
        /// Stargate HTTP base URL, for example http://router:8000.
        #[arg(long)]
        endpoint: String,
        /// Topology region whose Stargates this process represents.
        #[arg(long)]
        region: String,
        /// Fleet-wide offered rate. Use the same value for every region.
        #[arg(long)]
        rate_rps: f64,
        /// Workload seed. Use the same value for every region.
        #[arg(long)]
        seed: u64,
        /// Per-request algorithm sent as x-routing-method.
        #[arg(long)]
        routing_method: Option<String>,
        /// Unique label for this run; prefixes request IDs and cache keys.
        #[arg(long)]
        run_label: String,
        /// Shared start time for all regions, in Unix milliseconds.
        #[arg(long)]
        start_at_unix_ms: u64,
        /// Output file with one JSON record per request.
        #[arg(long)]
        records: PathBuf,
        /// Client token, sent as a bearer token.
        #[arg(long, env = "STARGATE_API_KEY", hide_env_values = true)]
        api_key: Option<String>,
    },
    /// Summarize fleet driver records from every region of one or more runs.
    SummarizeFleet {
        /// Simulation config (JSON) that defined the driven workload.
        #[arg(long)]
        config: PathBuf,
        /// Record files written by `drive`.
        #[arg(long, required = true, num_args = 1..)]
        records: Vec<PathBuf>,
        /// JSON object mapping backend cluster IDs to GPU worker counts.
        #[arg(long)]
        backend_gpus: Option<PathBuf>,
        /// Write the summaries to this JSON file instead of stdout.
        #[arg(long)]
        output: Option<PathBuf>,
    },
}

fn main() -> anyhow::Result<()> {
    match Cli::parse().command {
        Command::Simulate {
            config,
            output,
            jobs,
        } => simulate(&load_config(&config)?, output, jobs),
        Command::Drive {
            config,
            endpoint,
            region,
            rate_rps,
            seed,
            routing_method,
            run_label,
            start_at_unix_ms,
            records,
            api_key,
        } => tokio::runtime::Builder::new_multi_thread()
            .enable_all()
            .build()?
            .block_on(drive::drive(DriveArgs {
                config: load_config(&config)?,
                endpoint,
                region,
                rate_rps,
                seed,
                routing_method,
                run_label,
                start_at_unix_ms,
                records,
                api_key,
            })),
        Command::SummarizeFleet {
            config,
            records,
            backend_gpus,
            output,
        } => summarize_fleet(
            &load_config(&config)?,
            &records,
            backend_gpus.as_deref(),
            output,
        ),
    }
}

fn read_json<T: serde::de::DeserializeOwned>(path: &Path) -> anyhow::Result<T> {
    let raw =
        std::fs::read_to_string(path).with_context(|| format!("reading {}", path.display()))?;
    serde_json::from_str(&raw).with_context(|| format!("parsing {}", path.display()))
}

fn load_config(path: &Path) -> anyhow::Result<SimConfig> {
    let config: SimConfig = read_json(path)?;
    config.validate()?;
    Ok(config)
}

fn summarize_fleet(
    config: &SimConfig,
    paths: &[PathBuf],
    backend_gpus: Option<&Path>,
    output: Option<PathBuf>,
) -> anyhow::Result<()> {
    let mut records: Vec<FleetRecord> = Vec::new();
    for path in paths {
        let raw =
            std::fs::read_to_string(path).with_context(|| format!("reading {}", path.display()))?;
        for (line, text) in raw.lines().enumerate() {
            if text.trim().is_empty() {
                continue;
            }
            records.push(
                serde_json::from_str(text)
                    .with_context(|| format!("{}:{}", path.display(), line + 1))?,
            );
        }
    }
    let gpus: HashMap<String, usize> = backend_gpus.map(read_json).transpose()?.unwrap_or_default();
    let window = fleet::Window::new(
        config.workload.warmup_s,
        config.workload.measure_s,
        config.client.ttft_slo_ms,
    );
    let summaries = fleet::summarize(&records, &window, &gpus);
    let text = serde_json::to_string_pretty(&summaries)?;
    match output {
        Some(output) => std::fs::write(&output, text)
            .with_context(|| format!("writing {}", output.display()))?,
        None => println!("{text}"),
    }
    Ok(())
}

fn simulate(
    config: &SimConfig,
    output: Option<PathBuf>,
    jobs: Option<NonZeroUsize>,
) -> anyhow::Result<()> {
    let mut specs = Vec::new();
    for seed in &config.seeds {
        for rate_rps in &config.workload.rates_rps {
            for policy in &config.policies {
                specs.push(RunSpec {
                    config,
                    policy,
                    rate_rps: *rate_rps,
                    seed: *seed,
                });
            }
        }
    }
    let jobs = jobs
        .or_else(|| std::thread::available_parallelism().ok())
        .map_or(1, NonZeroUsize::get)
        .min(specs.len());
    let next = AtomicUsize::new(0);
    let results: Mutex<Vec<Option<anyhow::Result<RunSummary>>>> =
        Mutex::new(specs.iter().map(|_| None).collect());
    std::thread::scope(|scope| {
        for _ in 0..jobs {
            scope.spawn(|| {
                loop {
                    let index = next.fetch_add(1, Ordering::Relaxed);
                    let Some(spec) = specs.get(index) else {
                        break;
                    };
                    let summary = sim::run(spec).with_context(|| {
                        format!(
                            "policy {} at {} rps with seed {}",
                            spec.policy.name, spec.rate_rps, spec.seed
                        )
                    });
                    results.lock().expect("results lock")[index] = Some(summary);
                }
            });
        }
    });
    let summaries = results
        .into_inner()
        .expect("results lock")
        .into_iter()
        .map(|result| result.expect("every run completes"))
        .collect::<anyhow::Result<Vec<_>>>()?;

    print_table(&config.name, &summaries);
    if let Some(output) = output {
        std::fs::write(&output, serde_json::to_string_pretty(&summaries)?)
            .with_context(|| format!("writing {}", output.display()))?;
    }
    Ok(())
}

fn print_table(name: &str, summaries: &[RunSummary]) {
    println!("{name}");
    println!(
        "{:<34} {:>6} {:>5} {:>8} {:>8} {:>6} {:>6} {:>6} {:>6} {:>9} {:>9} {:>9} {:>6} {:>6} {:>6} {:>6} {:>6} {:>7}",
        "policy",
        "rps",
        "seed",
        "goodput",
        "thruput",
        "slo%",
        "noRte",
        "rtyEx",
        "tmout",
        "ttft50",
        "ttft99",
        "route99",
        "hit%",
        "reuse%",
        "xreg%",
        "off1%",
        "peak",
        "wall_ms"
    );
    for summary in summaries {
        println!(
            "{:<34} {:>6.1} {:>5} {:>8.2} {:>8.2} {:>6.2} {:>6} {:>6} {:>6} {:>9.1} {:>9.1} {:>9.1} {:>6.1} {:>6.1} {:>6.1} {:>6.1} {:>6.2} {:>7.0}",
            summary.policy,
            summary.rate_rps,
            summary.seed,
            summary.goodput_rps,
            summary.throughput_rps,
            summary.slo_attainment * 100.0,
            summary.failed_no_route,
            summary.failed_retries_exhausted,
            summary.failed_timeout,
            summary.ttft_ms.p50,
            summary.ttft_ms.p99,
            summary.routing_delay_ms.p99,
            summary.cache_hit_rate * 100.0,
            summary.reused_input_token_fraction * 100.0,
            summary.cross_region_fraction * 100.0,
            summary.off_primary_fraction * 100.0,
            summary.backend_load_peak_to_mean,
            summary.wall_clock_ms,
        );
    }
}

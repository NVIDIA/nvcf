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
mod metrics;
mod sim;
mod time;
mod workload;

use std::path::PathBuf;
use std::sync::Mutex;
use std::sync::atomic::{AtomicUsize, Ordering};

use anyhow::Context;
use clap::Parser;

use crate::config::SimConfig;
use crate::metrics::RunSummary;
use crate::sim::RunSpec;

#[derive(Parser, Debug)]
#[command(name = "stargate-routing-sim")]
struct Cli {
    /// Simulation config (JSON).
    #[arg(long)]
    config: PathBuf,
    /// Write every run summary to this JSON file.
    #[arg(long)]
    output: Option<PathBuf>,
    /// Parallel worker threads. Defaults to available parallelism.
    #[arg(long)]
    jobs: Option<usize>,
}

fn main() -> anyhow::Result<()> {
    let cli = Cli::parse();
    let raw = std::fs::read_to_string(&cli.config)
        .with_context(|| format!("reading {}", cli.config.display()))?;
    let config: SimConfig =
        serde_json::from_str(&raw).with_context(|| format!("parsing {}", cli.config.display()))?;
    config.validate()?;

    let mut specs = Vec::new();
    for seed in &config.seeds {
        for rate_rps in &config.workload.rates_rps {
            for policy in &config.policies {
                specs.push(RunSpec {
                    config: &config,
                    policy,
                    rate_rps: *rate_rps,
                    seed: *seed,
                });
            }
        }
    }
    let jobs = cli
        .jobs
        .or_else(|| std::thread::available_parallelism().ok().map(Into::into))
        .unwrap_or(1)
        .min(specs.len())
        .max(1);
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
                    let summary = sim::run(spec);
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
    if let Some(output) = cli.output {
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
        "429",
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

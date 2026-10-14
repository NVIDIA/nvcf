// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

mod artifact;
mod campaign;
mod cluster;
mod pod;
mod process;
mod report;
mod schedule;
mod suite;
mod workload;

#[cfg(test)]
#[path = "../tests/support/mod.rs"]
mod test_support;

use anyhow::{Context, Result, ensure};
use clap::{Parser, Subcommand, ValueEnum};
use std::io::Write;
use std::path::PathBuf;
use std::process::ExitCode;
use suite::{Algorithm, Suite};

#[derive(Parser)]
#[command(version, about = "Run controlled Spark campaigns for Stargate")]
struct Cli {
    #[arg(
        long,
        global = true,
        help = "Override the embedded canonical workload suite"
    )]
    suite_file: Option<PathBuf>,
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    /// List the named workload suites without contacting a cluster.
    List,
    /// Resolve a suite and print its plan.
    Plan {
        #[arg(long)]
        suite: String,
        #[arg(long, value_enum, default_value = "both")]
        algorithm: Algorithms,
    },
    /// Run or resume a controlled Spark campaign.
    Run {
        #[arg(long)]
        suite: String,
        #[arg(long, value_enum, default_value = "both")]
        algorithm: Algorithms,
        #[command(flatten)]
        options: campaign::Options,
    },
    /// Regenerate accepted results without cluster access or traffic.
    Render {
        #[arg(long)]
        output: PathBuf,
    },
    /// Stop and reconcile processes owned by a campaign.
    Reconcile {
        #[arg(long)]
        output: PathBuf,
    },
    /// Verify the deployed development topology.
    Verify {
        #[arg(long)]
        region: String,
        #[arg(long)]
        peer_region: Vec<String>,
    },
}

#[derive(Clone, Copy, ValueEnum)]
enum Algorithms {
    /// WaitAndWiden and PowerOfN; excludes Pulsar-WaW.
    Both,
    WaitAndWiden,
    PowerOfN,
    PulsarWaitAndWiden,
}

impl Algorithms {
    fn selected(self) -> &'static [Algorithm] {
        match self {
            Self::Both => &Algorithm::DEFAULT_PAIR,
            Self::WaitAndWiden => &[Algorithm::WaitAndWiden],
            Self::PowerOfN => &[Algorithm::PowerOfN],
            Self::PulsarWaitAndWiden => &[Algorithm::PulsarWaitAndWiden],
        }
    }
}

async fn execute(cli: Cli) -> Result<()> {
    let suite = || match &cli.suite_file {
        Some(path) => Suite::load(path),
        None => Suite::from_yaml(include_str!("../../loadtest/suite.yaml")),
    };
    if matches!(
        &cli.command,
        Command::Render { .. } | Command::Reconcile { .. } | Command::Verify { .. }
    ) {
        ensure!(
            cli.suite_file.is_none(),
            "--suite-file applies only to list, plan and run"
        );
    }
    match cli.command {
        Command::List => {
            let suite = suite()?;
            for (name, scenarios) in &suite.suites {
                let plan = suite.plan(name, &Algorithm::DEFAULT_PAIR)?;
                writeln!(
                    std::io::stdout().lock(),
                    "{name}: at least {:.1} measured minutes, scenarios: {}",
                    plan.minimum_measured_minutes(),
                    scenarios.join(", ")
                )?;
            }
            Ok(())
        }
        Command::Plan {
            suite: name,
            algorithm,
        } => {
            let plan = suite()?.plan(&name, algorithm.selected())?;
            let mut output = std::io::stdout().lock();
            serde_json::to_writer_pretty(&mut output, &plan)
                .context("serialize resolved benchmark plan")?;
            writeln!(output)?;
            Ok(())
        }
        Command::Run {
            suite: name,
            algorithm,
            options,
        } => {
            let plan = suite()?.plan(&name, algorithm.selected())?;
            campaign::run(options, plan).await
        }
        Command::Render { output } => campaign::render(&output),
        Command::Reconcile { output } => campaign::reconcile(&output).await,
        Command::Verify {
            region,
            peer_region,
        } => {
            let topology = cluster::Topology::load(&region, &peer_region)?;
            tokio::select! {
                biased;
                interrupted = process::interrupted() => interrupted?,
                verified = topology.verify_region(0) => { verified?; },
            }
            writeln!(std::io::stdout().lock(), "verified {region}")?;
            Ok(())
        }
    }
}

#[tokio::main(flavor = "current_thread")]
async fn main() -> ExitCode {
    match execute(Cli::parse()).await {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            let _ = writeln!(std::io::stderr().lock(), "error: {error:#}");
            ExitCode::from(if error.is::<process::Interrupted>() {
                130
            } else {
                1
            })
        }
    }
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

mod suite;
mod workload;

use anyhow::{Context, Result};
use clap::{Parser, Subcommand, ValueEnum};
use std::fs;
use std::io::Write;
use std::os::unix::fs::DirBuilderExt;
use std::path::{Path, PathBuf};
use suite::{Algorithm, Suite};

#[derive(Parser)]
#[command(version, about = "Plan controlled Spark campaigns for Stargate")]
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
    /// Resolve a suite and optionally write its immutable plan and workloads.
    Plan {
        #[arg(long)]
        suite: String,
        #[arg(long, value_enum, default_value = "both")]
        algorithm: Algorithms,
        #[arg(long)]
        output: Option<PathBuf>,
    },
}

#[derive(Clone, Copy, ValueEnum)]
enum Algorithms {
    Both,
    WaitAndWiden,
    PowerOfN,
}

fn write_plan(
    suite: &Suite,
    name: &str,
    algorithm: Algorithms,
    output: Option<&Path>,
) -> Result<()> {
    let algorithms: &[Algorithm] = match algorithm {
        Algorithms::Both => &Algorithm::ALL,
        Algorithms::WaitAndWiden => &[Algorithm::WaitAndWiden],
        Algorithms::PowerOfN => &[Algorithm::PowerOfN],
    };
    let plan = suite.plan(name, algorithms)?;
    let encoded =
        serde_json::to_string_pretty(&plan).context("serialize resolved benchmark plan")?;
    if let Some(output) = output {
        if let Some(parent) = output.parent().filter(|path| !path.as_os_str().is_empty()) {
            fs::DirBuilder::new()
                .recursive(true)
                .mode(0o700)
                .create(parent)
                .with_context(|| format!("create plan parent {}", parent.display()))?;
        }
        fs::DirBuilder::new()
            .mode(0o700)
            .create(output)
            .with_context(|| format!("create new plan directory {}", output.display()))?;
        let lock = fs::File::create_new(output.join(".lock")).context("create plan lock")?;
        lock.try_lock().context("lock plan directory")?;
        let directory = output.join("workloads");
        fs::create_dir(&directory).context("create workload directory")?;
        for (name, recipe) in &plan.workloads {
            recipe.write(&directory.join(name))?;
        }
        let mut temporary =
            tempfile::NamedTempFile::new_in(output).context("create plan staging file")?;
        temporary
            .write_all(encoded.as_bytes())
            .context("write resolved plan")?;
        temporary
            .as_file()
            .sync_all()
            .context("sync resolved plan")?;
        temporary
            .persist(output.join("plan.json"))
            .context("commit resolved plan")?;
        fs::File::open(output)?
            .sync_all()
            .context("sync plan directory")?;
        writeln!(
            std::io::stdout().lock(),
            "{}",
            output.join("plan.json").display()
        )?;
    } else {
        writeln!(std::io::stdout().lock(), "{encoded}")?;
    }
    Ok(())
}

fn main() -> Result<()> {
    let cli = Cli::parse();
    let suite = match cli.suite_file {
        Some(path) => Suite::load(&path)?,
        None => Suite::from_yaml(include_str!("../../loadtest/suite.yaml"))?,
    };
    match cli.command {
        Command::List => {
            for (name, scenarios) in &suite.suites {
                let plan = suite.plan(name, &Algorithm::ALL)?;
                writeln!(
                    std::io::stdout().lock(),
                    "{name}: at least {:.1} measured minutes at rate caps: {}",
                    plan.minimum_measured_minutes(),
                    scenarios.join(", ")
                )?;
            }
            Ok(())
        }
        Command::Plan {
            suite: name,
            algorithm,
            output,
        } => write_plan(&suite, &name, algorithm, output.as_deref()),
    }
}

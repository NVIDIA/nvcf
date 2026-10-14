// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, BTreeSet};
use std::fs::{self, File, OpenOptions};
use std::io::Write;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::time::Duration;

use anyhow::{Context, Result, bail, ensure};
use clap::Args;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use tokio::time::{Instant, sleep, timeout_at};
use uuid::Uuid;

use crate::artifact::{atomic_json, below, hash_file, read_json};
use crate::cluster::Topology;
use crate::pod;
use crate::process;
use crate::report;
use crate::suite::{Arm, ArmKind, Plan, RunLimit, Stream};
use crate::workload::Fingerprint;

const VERSION: u32 = 1;
const ENDPOINT: &str = "http://llm-request-router:8000/v1";
const POD_CHANGED: &str = "Spark Pod environment changed; use a new campaign directory";

#[derive(Debug, Args)]
pub struct Options {
    #[arg(long)]
    pub region: String,
    #[arg(long)]
    pub peer_region: Vec<String>,
    #[arg(long)]
    pub output: PathBuf,
    /// Existing Pod in the primary region with a container named `spark`.
    #[arg(long)]
    pub spark_pod: String,
    /// Require this load-balancer JSON configuration in every selected region.
    #[arg(long, value_name = "PATH", value_parser = parse_expected_config)]
    pub expected_config: Option<Value>,
    #[arg(long)]
    pub resume: bool,
}

#[derive(Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Settings {
    region: String,
    peer_regions: Vec<String>,
    spark_pod: String,
    expected_config: Option<Value>,
}

impl Options {
    fn settings(&self) -> Settings {
        Settings {
            region: self.region.clone(),
            peer_regions: self.peer_region.clone(),
            spark_pod: self.spark_pod.clone(),
            expected_config: self.expected_config.clone(),
        }
    }
}

fn parse_expected_config(path: &str) -> Result<Value, String> {
    let value: Value = read_json(Path::new(path)).map_err(|error| format!("{error:#}"))?;
    if !value.is_object() {
        return Err("expected configuration must be a JSON object".into());
    }
    Ok(value)
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Manifest {
    version: u32,
    id: String,
    settings: Settings,
    plan: Plan,
    topology: Value,
    environment: pod::Environment,
    controls: Value,
    workloads: BTreeMap<String, Fingerprint>,
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct AcceptedReport {
    stream: Option<String>,
    path: PathBuf,
    sha256: String,
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Receipt {
    arm: PathBuf,
    attempt: PathBuf,
    started_at: String,
    ended_at: String,
    warmup: Option<AcceptedReport>,
    reports: Vec<AcceptedReport>,
    evidence: BTreeMap<PathBuf, String>,
}

struct AcceptedArm {
    receipt: Receipt,
    metrics: Vec<report::Metrics>,
}

/// Run a plan from `Suite::plan`, which validates it.
pub async fn run(options: Options, plan: Plan) -> Result<()> {
    for arm in &plan.arms {
        for stream in arm.warmup.iter().chain(&arm.streams) {
            deadline(stream)?;
        }
    }
    let topology = Topology::load(&options.region, &options.peer_region)?;
    let runner = pod::Runner::new(&topology, &options.spark_pod)?;
    let output = prepare_directory(&options.output)?;
    let lock = lock_output(&output)?;
    let mut campaign = None;
    let result = tokio::select! {
        biased;
        signal = process::interrupted() => signal,
        result = execute(&output, &options, plan, &topology, &runner, &mut campaign) => result,
    };
    let cleanup = match campaign {
        Some(campaign) => cleanup(&runner, &output, &campaign).await,
        None => Ok(()),
    };
    drop(lock);
    match (result, cleanup) {
        (Err(error), Err(cleanup)) => {
            return Err(error.context(format!("cleanup also failed: {cleanup:#}")));
        }
        (Err(error), _) => return Err(error),
        (_, Err(error)) => return Err(error),
        _ => {}
    }
    render(&output)
}

/// Stop owned Spark work. A second interrupt abandons cleanup so the operator
/// is never trapped behind an unreachable API server.
async fn cleanup(runner: &pod::Runner<'_>, output: &Path, campaign: &str) -> Result<()> {
    tokio::select! {
        biased;
        signal = process::interrupted() => signal,
        result = runner.reconcile(output, campaign) => result,
    }
    .context("campaign cleanup is unconfirmed; run reconcile before starting more traffic")
}

async fn execute(
    output: &Path,
    options: &Options,
    plan: Plan,
    topology: &Topology,
    runner: &pod::Runner<'_>,
    campaign: &mut Option<String>,
) -> Result<()> {
    let (manifest, resumed) = prepare_manifest(output, options, plan, topology, runner).await?;
    *campaign = Some(manifest.id.clone());
    if resumed {
        // Stop owned work before judging the current environment.
        runner.reconcile(output, &manifest.id).await?;
        ensure!(runner.inspect().await? == manifest.environment, POD_CHANGED);
        verify_controls(&manifest, topology, runner).await?;
    }
    // Validate every accepted arm before any reset or new traffic.
    let accepted: Vec<bool> = manifest
        .plan
        .arms
        .iter()
        .map(|arm| load_receipt(output, arm).map(|receipt| receipt.is_some()))
        .collect::<Result<_>>()?;
    if accepted.iter().all(|accepted| *accepted) {
        return Ok(());
    }
    topology.wait_ready().await?;
    let artifact_root = PathBuf::from(
        runner
            .prepare(&manifest.id, output, &manifest.workloads)
            .await?,
    );
    let execution = Execution {
        output,
        manifest: &manifest,
        topology,
        runner,
        artifact_root: &artifact_root,
    };
    let mut first_pending = true;
    for (arm, accepted) in manifest.plan.arms.iter().zip(accepted) {
        if accepted {
            continue;
        }
        verify_controls(&manifest, topology, runner).await?;
        let attempt = Path::new("attempts").join(Uuid::new_v4().to_string());
        fs::create_dir_all(output.join(&attempt))?;
        atomic_json(
            &output.join(&attempt).join("attempt.json"),
            &json!({"arm":arm.directory,"createdAt":now()?}),
        )?;
        let mut evidence = BTreeMap::new();
        let reset = arm.reset_cache
            || (first_pending && matches!(arm.kind, ArmKind::Smoke | ArmKind::Capacity));
        let baseline = if reset {
            let reset = topology.reset_caches().await?;
            evidence_file(output, &attempt, "cache-reset.json", &reset, &mut evidence)?;
            Some(reset.after)
        } else if matches!(
            arm.kind,
            ArmKind::Capacity | ArmKind::Affinity | ArmKind::Mixed
        ) {
            Some(topology.cache_stats().await?)
        } else {
            None
        };
        first_pending = false;
        let before = topology.pod_state().await?;
        evidence_file(output, &attempt, "pods.before.json", &before, &mut evidence)?;
        if let Some(baseline) = &baseline {
            evidence_file(
                output,
                &attempt,
                "cache.before.json",
                baseline,
                &mut evidence,
            )?;
        }
        let started_at = now()?;
        let warmup = if let Some(warmup) = &arm.warmup {
            Some(
                execution
                    .streams(arm, std::slice::from_ref(warmup), &attempt, true)
                    .await?
                    .remove(0),
            )
        } else {
            None
        };
        let reports = execution
            .streams(arm, &arm.streams, &attempt, false)
            .await?;
        for (stream, report) in arm.streams.iter().zip(&reports) {
            if !stream.rate_steps.is_empty() {
                let path = report.path.with_extension("requests.jsonl");
                evidence.insert(path.clone(), hash_file(&output.join(path))?);
            }
        }
        let ended_at = now()?;
        let after = topology.pod_state().await?;
        evidence_file(output, &attempt, "pods.after.json", &after, &mut evidence)?;
        ensure!(
            before == after,
            "Pod replacement or restart invalidated {}",
            arm.directory.display()
        );
        verify_controls(&manifest, topology, runner).await?;
        if let Some(baseline) = baseline {
            let after = topology.cache_stats().await?;
            let delta = cache_delta(&baseline, &after)?;
            evidence_file(output, &attempt, "cache.after.json", &after, &mut evidence)?;
            evidence_file(output, &attempt, "cache.delta.json", &delta, &mut evidence)?;
        }
        let receipt = Receipt {
            arm: arm.directory.clone(),
            attempt,
            started_at,
            ended_at,
            warmup,
            reports,
            evidence,
        };
        atomic_json(&receipt_path(output, arm), &receipt)?;
        writeln!(
            std::io::stdout().lock(),
            "accepted {}",
            arm.directory.display()
        )?;
        if arm.cooldown_seconds > 0 {
            sleep(Duration::from_secs(arm.cooldown_seconds)).await;
        }
    }
    topology.wait_ready().await?;
    Ok(())
}

async fn prepare_manifest(
    output: &Path,
    options: &Options,
    plan: Plan,
    topology: &Topology,
    runner: &pod::Runner<'_>,
) -> Result<(Manifest, bool)> {
    let path = output.join("campaign.json");
    let topology_value = serde_json::to_value(&topology.regions)?;
    if path.exists() {
        ensure!(
            options.resume,
            "campaign already exists; pass --resume to continue"
        );
        let manifest = load_manifest(output)?;
        ensure!(
            manifest.settings == options.settings()
                && manifest.topology == topology_value
                && serde_json::to_value(&manifest.plan)? == serde_json::to_value(&plan)?,
            "resume inputs differ from the immutable campaign; use a new output directory"
        );
        verify_workloads(output, &manifest.plan, &manifest.workloads)?;
        return Ok((manifest, true));
    }
    for entry in fs::read_dir(output)? {
        let entry = entry?;
        ensure!(
            entry.file_name() == ".lock",
            "output contains foreign or unfinished artifacts: {}",
            entry.path().display()
        );
    }
    let environment = runner.inspect().await?;
    require_open_files(&plan, environment.open_file_limit)?;
    let settings = options.settings();
    let controls = topology.snapshot_controls().await?;
    validate_expected_config(&settings, &controls)?;
    let mut workloads = BTreeMap::new();
    for (name, recipe) in &plan.workloads {
        workloads.insert(
            name.clone(),
            recipe.write(&output.join("workloads").join(name))?,
        );
    }
    let manifest = Manifest {
        version: VERSION,
        id: Uuid::new_v4().to_string(),
        settings,
        plan,
        topology: topology_value,
        environment,
        controls,
        workloads,
    };
    atomic_json(&path, &manifest)?;
    Ok((manifest, false))
}

fn require_open_files(plan: &Plan, limit: Option<u64>) -> Result<()> {
    let Some(limit) = limit else {
        return Ok(());
    };
    let workers = plan
        .arms
        .iter()
        .flat_map(|arm| arm.warmup.iter().chain(&arm.streams))
        .map(|stream| stream.workers)
        .max()
        .context("benchmark plan has no streams")?;
    // Allow active and idle connections per worker, plus resolver and report files.
    let required = u64::try_from(workers)?
        .checked_mul(2)
        .and_then(|files| files.checked_add(64))
        .context("Spark open-file requirement overflow")?;
    ensure!(
        limit >= required,
        "Spark open-file limit {limit} cannot support {workers} workers; require at least {required} descriptors"
    );
    Ok(())
}

fn validate_expected_config(settings: &Settings, controls: &Value) -> Result<()> {
    let Some(expected) = &settings.expected_config else {
        return Ok(());
    };
    let policies = controls
        .get("loadBalancers")
        .and_then(Value::as_object)
        .context("controls do not contain load-balancer policies")?;
    for region in std::iter::once(&settings.region).chain(&settings.peer_regions) {
        let policy = policies.get(region).with_context(|| {
            format!("controls do not contain a load-balancer policy for {region}")
        })?;
        ensure!(
            policy == expected,
            "load-balancer policy for {region} differs from the expected configuration"
        );
    }
    Ok(())
}

/// Compare the Spark Pod object and deployment controls with the frozen
/// campaign. The Spark version and open-file limit come from the image and
/// container runtime, so they hold while the container identity holds; a
/// resumed run checks them once with a full inspection.
async fn verify_controls(
    manifest: &Manifest,
    topology: &Topology,
    runner: &pod::Runner<'_>,
) -> Result<()> {
    ensure!(
        runner.current_container().await? == manifest.environment.container,
        POD_CHANGED
    );
    ensure!(
        topology.snapshot_controls().await? == manifest.controls,
        "deployment resources, images or routing settings changed; use a new campaign directory"
    );
    Ok(())
}

fn verify_workloads(
    output: &Path,
    plan: &Plan,
    fingerprints: &BTreeMap<String, Fingerprint>,
) -> Result<()> {
    ensure!(
        fingerprints.len() == plan.workloads.len(),
        "workload fingerprint set differs from the plan"
    );
    let expected_files: BTreeSet<PathBuf> = plan
        .workloads
        .keys()
        .flat_map(|name| {
            let path = PathBuf::from(name);
            [path.with_extension("json"), path]
        })
        .collect();
    let directory = output.join("workloads");
    ensure!(
        fs::symlink_metadata(&directory)?.file_type().is_dir(),
        "workload directory is not a regular directory"
    );
    for entry in fs::read_dir(&directory)? {
        let entry = entry?;
        ensure!(
            entry.file_type()?.is_file() && expected_files.contains(Path::new(&entry.file_name())),
            "foreign workload artifact: {}",
            entry.path().display()
        );
    }
    for (name, recipe) in &plan.workloads {
        let expected = fingerprints
            .get(name)
            .with_context(|| format!("workload fingerprint missing for {name}"))?;
        ensure!(
            expected.prompt_count == recipe.prompt_count()?,
            "workload prompt count changed: {name}"
        );
        let path = output.join("workloads").join(name);
        ensure!(
            hash_file(&path)? == expected.sha256,
            "workload bytes changed: {name}"
        );
        let sidecar: Fingerprint = read_json(&path.with_extension("json"))?;
        ensure!(sidecar == *expected, "workload fingerprint changed: {name}");
    }
    Ok(())
}

struct Execution<'a> {
    output: &'a Path,
    manifest: &'a Manifest,
    topology: &'a Topology,
    runner: &'a pod::Runner<'a>,
    artifact_root: &'a Path,
}

struct PreparedStream {
    directory: PathBuf,
    arguments: Vec<String>,
    timeout: Duration,
    scheduled: bool,
}

impl Execution<'_> {
    async fn streams(
        &self,
        arm: &Arm,
        streams: &[Stream],
        attempt: &Path,
        warmup: bool,
    ) -> Result<Vec<AcceptedReport>> {
        let mut prepared = Vec::new();
        for stream in streams {
            let directory = attempt.join(stream.name.as_deref().unwrap_or("main"));
            fs::create_dir_all(self.output.join(&directory))?;
            let arguments = spark_arguments(
                stream,
                arm,
                &self.manifest.plan,
                &directory,
                self.artifact_root,
                self.topology.primary(),
            )?;
            atomic_json(
                &self.output.join(&directory).join("command.json"),
                &arguments,
            )?;
            prepared.push(PreparedStream {
                directory,
                arguments,
                timeout: deadline(stream)?,
                scheduled: !stream.rate_steps.is_empty(),
            });
        }
        self.pod_streams(&prepared, attempt).await?;
        streams
            .iter()
            .zip(prepared)
            .map(|(stream, prepared)| {
                let path = prepared.directory.join("spark.json");
                let verified = accept_report(&self.output.join(&path), stream, arm, warmup, None)?;
                Ok(AcceptedReport {
                    stream: stream.name.clone(),
                    path,
                    sha256: verified.sha256,
                })
            })
            .collect()
    }

    async fn pod_streams(&self, streams: &[PreparedStream], attempt: &Path) -> Result<()> {
        let runner = self.runner;
        let states: Vec<_> = streams
            .iter()
            .map(|_| {
                self.output
                    .join(attempt)
                    .join("pod-launches")
                    .join(format!("{}.json", Uuid::new_v4()))
            })
            .collect();
        let directories: Vec<_> = streams
            .iter()
            .map(|stream| {
                self.artifact_root
                    .join(&stream.directory)
                    .to_str()
                    .map(str::to_owned)
                    .context("remote Spark directory is not UTF-8")
            })
            .collect::<Result<_>>()?;
        let maximum = streams
            .iter()
            .map(|stream| stream.timeout)
            .max()
            .context("Pod batch contains no streams")?;
        let end = Instant::now()
            .checked_add(maximum)
            .context("Spark deadline exceeds monotonic clock range")?;
        let start = |index: usize| {
            runner.start(
                &states[index],
                &directories[index],
                &self.manifest.environment.container.identity,
                &streams[index].arguments,
                streams[index].timeout,
            )
        };
        let mut launches = match streams.len() {
            1 => vec![start(0).await?],
            2 => {
                let (hot, short) = tokio::try_join!(start(0), start(1))?;
                vec![hot, short]
            }
            _ => bail!("Pod batches support one stream or the paired mixed streams"),
        };
        loop {
            let mut complete = true;
            for ((launch, state), stream) in launches.iter_mut().zip(&states).zip(streams) {
                let log = self.output.join(&stream.directory).join("spark.log");
                match timeout_at(end, runner.poll(launch, state, &log))
                    .await
                    .context("Spark exceeded its expected duration")??
                {
                    Some(status) => ensure!(
                        status == 0,
                        "Spark Pod process failed with exit status {status}"
                    ),
                    None => complete = false,
                }
            }
            if complete {
                break;
            }
            ensure!(Instant::now() < end, "Spark exceeded its expected duration");
            sleep(Duration::from_secs(2)).await;
        }
        for (launch, stream) in launches.iter().zip(streams) {
            runner
                .download(
                    launch,
                    &self.output.join(&stream.directory).join("spark.json"),
                )
                .await?;
            if stream.scheduled {
                runner
                    .download(
                        launch,
                        &self
                            .output
                            .join(&stream.directory)
                            .join("spark.requests.jsonl"),
                    )
                    .await?;
            }
        }
        Ok(())
    }
}

fn spark_arguments(
    stream: &Stream,
    arm: &Arm,
    plan: &Plan,
    directory: &Path,
    artifact_root: &Path,
    primary: &crate::cluster::Region,
) -> Result<Vec<String>> {
    let mut args = vec![
        "spark".into(),
        "--endpoint".into(),
        ENDPOINT.into(),
        "--model".into(),
        primary.model_name.clone(),
        "--stargate-routing-key".into(),
        primary.routing_key.clone(),
        "--stargate-max-wait-ms".into(),
        stream.limits.max_wait_ms.to_string(),
        "--stargate-request-slo-ms".into(),
        stream.limits.request_slo_ms.to_string(),
        "--workers".into(),
        stream.workers.to_string(),
        "--timeout".into(),
        format!("{}s", stream.limits.timeout_seconds),
        "--max-tokens".into(),
        stream.limits.max_tokens.to_string(),
        "--quiet".into(),
        "--output".into(),
        artifact_root
            .join(directory)
            .join("spark.json")
            .display()
            .to_string(),
        "--workload".into(),
        artifact_root
            .join("workloads")
            .join(&stream.workload)
            .display()
            .to_string(),
        "--scenario".into(),
        "normal".into(),
    ];
    if stream.rate_steps.is_empty() {
        args.extend([
            "--rate-limit".into(),
            stream.rate.to_string(),
            "--warmup".into(),
            "0".into(),
        ]);
        match stream.limit {
            RunLimit::Requests(requests) => {
                args.extend(["--requests".into(), requests.to_string()])
            }
            RunLimit::DurationSeconds(seconds) => {
                args.extend(["--duration".into(), format!("{seconds}s")])
            }
        }
    } else {
        for step in &stream.rate_steps {
            args.extend([
                "--rate-step".into(),
                format!("{}:{}s", step.rate, step.duration_seconds),
            ]);
        }
    }
    if let Some(header) = plan
        .routing_headers
        .get(&arm.algorithm)
        .context("algorithm routing header missing")?
    {
        args.extend(["--stargate-load-balancing-algorithm".into(), header.clone()]);
    }
    Ok(args)
}

fn deadline(stream: &Stream) -> Result<Duration> {
    let drain = if stream.rate_steps.is_empty() {
        600
    } else {
        // Spark permits an initial attempt and three retries, with up to 5s backoff each.
        // Retain the normal 600s controller allowance for launch and polling overhead.
        stream
            .limits
            .timeout_seconds
            .checked_mul(4)
            .and_then(|seconds| seconds.checked_add(615))
            .context("scheduled request drain allowance exceeds u64")?
    };
    let seconds = match stream.limit {
        RunLimit::DurationSeconds(seconds) => seconds,
        RunLimit::Requests(requests) => {
            let rate_bound = u64::try_from(requests.div_ceil(stream.rate))?;
            let worker_bound = u64::try_from(requests.div_ceil(stream.workers))?
                .checked_mul(stream.limits.timeout_seconds)
                .context("fixed-count timeout exceeds u64")?;
            rate_bound.max(worker_bound)
        }
    }
    .checked_add(drain)
    .context("Spark timeout exceeds u64")?;
    Ok(Duration::from_secs(seconds))
}

pub async fn reconcile(output: &Path) -> Result<()> {
    let output = output.canonicalize().context("locate campaign output")?;
    let manifest = load_manifest(&output)?;
    let _lock = lock_output(&output)?;
    let topology = Topology::load(&manifest.settings.region, &manifest.settings.peer_regions)?;
    pod::Runner::new(&topology, &manifest.settings.spark_pod)?
        .reconcile(&output, &manifest.id)
        .await
}

pub fn render(output: &Path) -> Result<()> {
    let output = output.canonicalize().context("locate campaign output")?;
    let _lock = lock_output(&output)?;
    let manifest = load_manifest(&output)?;
    verify_workloads(&output, &manifest.plan, &manifest.workloads)?;
    let mut rows = Vec::new();
    for arm in &manifest.plan.arms {
        let Some(accepted_arm) = load_receipt(&output, arm)? else {
            continue;
        };
        for ((stream, accepted), metrics) in arm
            .streams
            .iter()
            .zip(accepted_arm.receipt.reports)
            .zip(accepted_arm.metrics)
        {
            let case = stream.name.as_ref().map_or_else(
                || arm.scenario.clone(),
                |name| format!("{}-{name}", arm.scenario),
            );
            let pair = arm
                .directory
                .parent()
                .context("arm pair directory missing")?;
            let pair_id = stream
                .name
                .as_ref()
                .map_or_else(|| pair.to_owned(), |name| pair.join(name));
            rows.push(report::Row {
                case,
                pair_id: pair_id.display().to_string(),
                algorithm: arm.algorithm,
                rate: stream.rate,
                rate_steps: stream.rate_steps.clone(),
                workers: stream.workers,
                limit: stream.limit,
                limits: stream.limits,
                workload: manifest.plan.workloads[&stream.workload].clone(),
                source: accepted.path,
                metrics,
            });
        }
    }
    report::render(&output, &rows)
}

/// Accept one Spark report: its counts and cache observations, the recorded
/// checksum when one exists, its rate schedule, and no failed requests in a
/// smoke or warm-up stream.
fn accept_report(
    path: &Path,
    stream: &Stream,
    arm: &Arm,
    warmup: bool,
    expected_sha256: Option<&str>,
) -> Result<report::VerifiedReport> {
    let verified = report::inspect(path, stream.limit, arm.kind == ArmKind::Capacity)?;
    if let Some(expected) = expected_sha256 {
        ensure!(
            verified.sha256 == expected,
            "accepted Spark report checksum changed: {}",
            path.display()
        );
    }
    crate::schedule::verify(path, stream, &verified.metrics)?;
    ensure!(
        !(warmup || arm.kind == ArmKind::Smoke) || verified.metrics.failed == 0,
        "smoke or warm-up requests failed: {}",
        path.display()
    );
    Ok(verified)
}

fn load_receipt(output: &Path, arm: &Arm) -> Result<Option<AcceptedArm>> {
    let path = receipt_path(output, arm);
    if !path.exists() {
        return Ok(None);
    }
    let receipt: Receipt = read_json(&path)?;
    ensure!(
        receipt.arm == arm.directory,
        "accepted receipt belongs to another arm"
    );
    ensure!(
        receipt.attempt.starts_with("attempts") && receipt.attempt.components().count() == 2,
        "invalid accepted attempt path"
    );
    below(output, &receipt.attempt)?;
    ensure!(
        receipt.reports.len() == arm.streams.len(),
        "accepted receipt has incomplete stream results"
    );
    ensure!(
        receipt.warmup.is_some() == arm.warmup.is_some(),
        "accepted receipt has incomplete warm-up evidence"
    );
    let verify = |stream: &Stream, accepted: &AcceptedReport, warmup: bool| {
        ensure!(
            stream.name == accepted.stream
                && accepted.path
                    == receipt
                        .attempt
                        .join(stream.name.as_deref().unwrap_or("main"))
                        .join("spark.json"),
            "accepted report does not match the planned stream"
        );
        let verified = accept_report(
            &below(output, &accepted.path)?,
            stream,
            arm,
            warmup,
            Some(&accepted.sha256),
        )?;
        if !stream.rate_steps.is_empty() {
            ensure!(
                receipt
                    .evidence
                    .contains_key(&accepted.path.with_extension("requests.jsonl")),
                "accepted rate schedule lacks request timeline evidence"
            );
        }
        Ok::<_, anyhow::Error>(verified.metrics)
    };
    if let (Some(stream), Some(accepted)) = (&arm.warmup, &receipt.warmup) {
        verify(stream, accepted, true)?;
    }
    let mut metrics = Vec::new();
    for (stream, accepted) in arm.streams.iter().zip(&receipt.reports) {
        metrics.push(verify(stream, accepted, false)?);
    }
    for name in ["pods.before.json", "pods.after.json"] {
        ensure!(
            receipt.evidence.contains_key(&receipt.attempt.join(name)),
            "accepted receipt lacks {name}"
        );
    }
    if matches!(
        arm.kind,
        ArmKind::Capacity | ArmKind::Affinity | ArmKind::Mixed
    ) {
        ensure!(
            receipt
                .evidence
                .contains_key(&receipt.attempt.join("cache.delta.json")),
            "accepted receipt lacks cache evidence"
        );
    }
    for (relative, hash) in &receipt.evidence {
        ensure!(
            relative.starts_with(&receipt.attempt),
            "accepted evidence escapes its attempt"
        );
        ensure!(
            hash_file(&below(output, relative)?)? == *hash,
            "accepted evidence changed: {}",
            relative.display()
        );
    }
    Ok(Some(AcceptedArm { receipt, metrics }))
}

fn receipt_path(output: &Path, arm: &Arm) -> PathBuf {
    output
        .join("accepted")
        .join(&arm.directory)
        .join("receipt.json")
}

fn load_manifest(output: &Path) -> Result<Manifest> {
    let manifest: Manifest = read_json(&output.join("campaign.json"))?;
    ensure!(
        manifest.version == VERSION,
        "unsupported native campaign version {}",
        manifest.version
    );
    Uuid::parse_str(&manifest.id).context("invalid campaign ID")?;
    manifest.plan.validate()?;
    Ok(manifest)
}

fn prepare_directory(path: &Path) -> Result<PathBuf> {
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(path)
        .with_context(|| format!("create campaign directory {}", path.display()))?;
    path.canonicalize().context("resolve campaign output")
}

fn lock_output(output: &Path) -> Result<File> {
    let lock = OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .mode(0o600)
        .open(output.join(".lock"))?;
    lock.try_lock()
        .context("another benchmark controller is using this output directory")?;
    Ok(lock)
}

fn evidence_file(
    output: &Path,
    attempt: &Path,
    name: &str,
    value: &impl Serialize,
    hashes: &mut BTreeMap<PathBuf, String>,
) -> Result<()> {
    let relative = attempt.join(name);
    let path = output.join(&relative);
    atomic_json(&path, value)?;
    hashes.insert(relative, hash_file(&path)?);
    Ok(())
}

fn cache_delta(before: &Value, after: &Value) -> Result<Value> {
    let before = before
        .as_object()
        .context("cache baseline is not an object")?;
    let after = after
        .as_object()
        .context("cache snapshot is not an object")?;
    ensure!(
        before.keys().eq(after.keys()),
        "cache backend set changed during measurement"
    );
    let mut delta = BTreeMap::new();
    for (backend, counters) in after {
        let mut changes = BTreeMap::new();
        for field in [
            "kv_cache_hit_count",
            "kv_cache_miss_count",
            "kv_cache_eviction_count",
            "kv_cache_evicted_tokens",
        ] {
            let old = before[backend][field]
                .as_u64()
                .with_context(|| format!("missing cache counter {backend}/{field}"))?;
            let new = counters[field]
                .as_u64()
                .with_context(|| format!("missing cache counter {backend}/{field}"))?;
            changes.insert(
                field,
                new.checked_sub(old)
                    .with_context(|| format!("cache counter decreased: {backend}/{field}"))?,
            );
        }
        delta.insert(backend, changes);
    }
    Ok(serde_json::to_value(delta)?)
}

fn now() -> Result<String> {
    OffsetDateTime::now_utc()
        .format(&Rfc3339)
        .context("format UTC timestamp")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::suite::{Algorithm, Suite};
    use crate::test_support::FakeCommand;

    fn plan(name: &str) -> Plan {
        Suite::from_yaml(include_str!("../../loadtest/suite.yaml"))
            .unwrap()
            .plan(name, &Algorithm::DEFAULT_PAIR)
            .unwrap()
    }

    fn options(output: &Path) -> Options {
        Options {
            region: "us-west-2".into(),
            peer_region: Vec::new(),
            output: output.to_owned(),
            spark_pod: "spark-test".into(),
            expected_config: None,
            resume: false,
        }
    }

    fn manifest(output: &Path, plan: Plan) -> Manifest {
        let workloads = plan
            .workloads
            .iter()
            .map(|(name, recipe)| {
                (
                    name.clone(),
                    recipe.write(&output.join("workloads").join(name)).unwrap(),
                )
            })
            .collect();
        Manifest {
            version: VERSION,
            id: Uuid::new_v4().to_string(),
            settings: options(output).settings(),
            plan,
            topology: json!([]),
            environment: pod::Environment {
                container: pod::Container {
                    identity: pod::Identity {
                        uid: "fixture-pod".into(),
                        container_id: "fixture-container".into(),
                    },
                    image: "fixture:image".into(),
                    image_id: "fixture-digest".into(),
                    resources: json!({}),
                    pod_spec_sha256: "0".repeat(64),
                },
                spark_version: "Spark fixture 1".into(),
                open_file_limit: None,
            },
            controls: json!({}),
            workloads,
        }
    }

    #[test]
    fn expected_configuration_is_an_object_read_at_the_input_boundary() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let path = directory.path().join("expected.json");
        let input = path.to_str().unwrap();
        let policy = json!({"max_queued":4});
        atomic_json(&path, &policy)?;
        let mut options = options(directory.path());
        options.expected_config = Some(parse_expected_config(input).map_err(anyhow::Error::msg)?);
        fs::remove_file(&path)?;
        assert_eq!(options.settings().expected_config, Some(policy));
        assert!(parse_expected_config(input).is_err());
        for invalid in ["null", "[]", "1", "true", "\"policy\"", "{"] {
            fs::write(&path, invalid)?;
            assert!(parse_expected_config(input).is_err(), "accepted {invalid}");
        }
        Ok(())
    }

    #[test]
    fn pod_file_budget_covers_each_stream_and_rejects_insufficient_limits() -> Result<()> {
        let mut plan = plan("canonical");
        require_open_files(&plan, Some(320))?;
        assert!(require_open_files(&plan, Some(319)).is_err());
        plan.arms[0].streams[0].workers = 1280;
        assert!(require_open_files(&plan, Some(1024)).is_err());
        require_open_files(&plan, Some(2624))?;
        require_open_files(&plan, None)?;
        Ok(())
    }

    #[test]
    fn deadlines_and_headers_follow_the_resolved_plan() -> Result<()> {
        let topology = Topology::load("us-west-2", &[])?;
        let plan = plan("canonical");
        let smoke = &plan.arms[0];
        assert_eq!(deadline(&smoke.streams[0])?, Duration::from_secs(720));
        let long = plan
            .arms
            .iter()
            .find(|arm| arm.scenario == "long-context-affinity")
            .unwrap();
        assert_eq!(deadline(&long.streams[0])?, Duration::from_secs(2760));
        let capacity = plan
            .arms
            .iter()
            .find(|arm| arm.kind == ArmKind::Capacity)
            .unwrap();
        assert_eq!(deadline(&capacity.streams[0])?, Duration::from_secs(720));
        let arguments = spark_arguments(
            &smoke.streams[0],
            smoke,
            &plan,
            Path::new("attempts/id/main"),
            Path::new("/campaign"),
            topology.primary(),
        )?;
        assert!(
            !arguments
                .iter()
                .any(|argument| argument == "--stargate-load-balancing-algorithm")
        );
        assert!(
            arguments
                .windows(2)
                .any(|pair| pair == ["--output", "/campaign/attempts/id/main/spark.json"])
        );
        let power = &plan.arms[1];
        let arguments = spark_arguments(
            &power.streams[0],
            power,
            &plan,
            Path::new("attempts/id/main"),
            Path::new("/campaign"),
            topology.primary(),
        )?;
        assert!(
            arguments
                .windows(2)
                .any(|pair| pair == ["--stargate-load-balancing-algorithm", "powerOfN"])
        );
        let mut impossible = smoke.streams[0].clone();
        impossible.limits.timeout_seconds = u64::MAX;
        assert!(deadline(&impossible).is_err());
        Ok(())
    }

    #[test]
    fn rate_schedule_arguments_omit_conflicting_flags_and_allow_final_drain() -> Result<()> {
        let topology = Topology::load("us-west-2", &[])?;
        let plan = Suite::from_yaml(include_str!("../../loadtest/session-ramp.yaml"))?
            .plan("session-ramp", &Algorithm::DEFAULT_PAIR)?;
        for arm in &plan.arms {
            let stream = &arm.streams[0];
            let arguments = spark_arguments(
                stream,
                arm,
                &plan,
                Path::new("attempts/id/main"),
                Path::new("/campaign"),
                topology.primary(),
            )?;
            for flag in ["--requests", "--duration", "--rate-limit", "--warmup"] {
                assert!(!arguments.iter().any(|argument| argument == flag));
            }
            assert_eq!(
                arguments
                    .windows(2)
                    .filter(|pair| pair[0] == "--rate-step")
                    .map(|pair| pair[1].as_str())
                    .collect::<Vec<_>>(),
                ["160:180s", "180:180s", "200:180s", "180:180s", "160:180s"]
            );
            assert_eq!(deadline(stream)?, Duration::from_secs(1635));
            let mut slow = stream.clone();
            slow.limits.timeout_seconds = 1000;
            assert_eq!(deadline(&slow)?, Duration::from_secs(5515));
            slow.limits.timeout_seconds = u64::MAX;
            assert!(deadline(&slow).is_err());
        }
        Ok(())
    }

    #[tokio::test]
    async fn foreign_outputs_and_changed_resume_inputs_fail_before_external_commands() -> Result<()>
    {
        let directory = tempfile::tempdir()?;
        let fake = FakeCommand::new();
        let topology = Topology::load("us-west-2", &[])?.with_executable(fake.executable());
        let runner = pod::Runner::new(&topology, "spark-test")?;
        let mut options = options(directory.path());
        fs::write(directory.path().join("foreign"), "keep me")?;
        assert!(
            prepare_manifest(
                directory.path(),
                &options,
                plan("smoke"),
                &topology,
                &runner
            )
            .await
            .is_err()
        );
        assert_eq!(
            fs::read_to_string(directory.path().join("foreign"))?,
            "keep me"
        );
        let frozen = manifest(directory.path(), plan("smoke"));
        atomic_json(&directory.path().join("campaign.json"), &frozen)?;
        let original = fs::read(directory.path().join("campaign.json"))?;
        options.resume = true;
        options.spark_pod = "different-pod".into();
        assert!(
            prepare_manifest(
                directory.path(),
                &options,
                plan("smoke"),
                &topology,
                &runner
            )
            .await
            .is_err()
        );
        assert_eq!(fs::read(directory.path().join("campaign.json"))?, original);
        assert!(fake.calls().is_empty());
        Ok(())
    }

    #[test]
    fn accepted_reports_render_offline_and_detect_raw_or_evidence_changes() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let output = directory.path().canonicalize()?;
        let manifest = manifest(&output, plan("smoke"));
        atomic_json(&output.join("campaign.json"), &manifest)?;
        let arm = &manifest.plan.arms[0];
        let attempt = Path::new("attempts").join(Uuid::new_v4().to_string());
        let raw_path = attempt.join("main/spark.json");
        let distribution = json!({"min":0,"max":0,"mean":0,"p50":0,"p90":0,"p95":0,"p99":0});
        atomic_json(
            &output.join(&raw_path),
            &json!({
                "summary":{"total_requests":32,"successful":32,"failed":0,"total_retries":0,"retried_requests":0,"total_duration_ms":1000},
                "throughput":{"kv_cache_hit_requests":0,"kv_cache_observed_requests":0,"total_cached_tokens":0},
                "ttft_ms":distribution,"latency_ms":distribution,"itl_ms":distribution,
            }),
        )?;
        let mut evidence = BTreeMap::new();
        for name in ["pods.before.json", "pods.after.json"] {
            evidence_file(&output, &attempt, name, &json!({}), &mut evidence)?;
        }
        let receipt = Receipt {
            arm: arm.directory.clone(),
            attempt: attempt.clone(),
            started_at: now()?,
            ended_at: now()?,
            warmup: None,
            reports: vec![AcceptedReport {
                stream: None,
                path: raw_path.clone(),
                sha256: hash_file(&output.join(&raw_path))?,
            }],
            evidence,
        };
        atomic_json(&receipt_path(&output, arm), &receipt)?;
        render(&output)?;
        let original = fs::read(output.join("summary.json"))?;
        let summary: Value = serde_json::from_slice(&original)?;
        assert_eq!(summary["rows"].as_array().unwrap().len(), 1);
        fs::write(output.join(&attempt).join("pods.after.json"), "changed")?;
        assert!(render(&output).is_err());
        assert_eq!(fs::read(output.join("summary.json"))?, original);
        atomic_json(&output.join(&attempt).join("pods.after.json"), &json!({}))?;
        OpenOptions::new()
            .append(true)
            .open(output.join(raw_path))?
            .write_all(b"\n")?;
        assert!(render(&output).is_err());
        assert_eq!(fs::read(output.join("summary.json"))?, original);
        Ok(())
    }

    #[test]
    fn cache_delta_requires_stable_backends_and_monotonic_counters() -> Result<()> {
        let before = json!({"backend":{"kv_cache_hit_count":1,"kv_cache_miss_count":2,"kv_cache_eviction_count":0,"kv_cache_evicted_tokens":0}});
        let mut after = before.clone();
        after["backend"]["kv_cache_hit_count"] = json!(4);
        assert_eq!(
            cache_delta(&before, &after)?["backend"]["kv_cache_hit_count"],
            3
        );
        after["backend"]["kv_cache_hit_count"] = json!(0);
        assert!(cache_delta(&before, &after).is_err());
        assert!(cache_delta(&before, &json!({})).is_err());
        Ok(())
    }
}

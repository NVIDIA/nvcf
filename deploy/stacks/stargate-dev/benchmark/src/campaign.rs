// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, BTreeSet};
use std::fs::{self, File, OpenOptions};
use std::io::Write;
use std::num::NonZeroU16;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::time::Duration;

use anyhow::{Context, Result, bail, ensure};
use clap::Args;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use tokio::net::TcpStream;
use tokio::process::Command;
use tokio::time::{Instant, sleep, timeout, timeout_at};
use uuid::Uuid;

use crate::artifact::{atomic_json, below, hash_file, read_json};
use crate::cluster::Topology;
use crate::pod;
use crate::process::{self, Process, capture};
use crate::report;
use crate::suite::{Arm, ArmKind, Plan, RunLimit, Stream};
use crate::workload::Fingerprint;

const VERSION: u32 = 2;
const OWNER_LABEL: &str = "nvcf.stargate-bench.owner";
const CAMPAIGN_LABEL: &str = "nvcf.stargate-bench.campaign";
const CONTROL_TIMEOUT: Duration = Duration::from_secs(30);

#[derive(Debug, Args)]
pub struct Options {
    #[arg(long)]
    pub region: String,
    #[arg(long)]
    pub peer_region: Vec<String>,
    #[arg(long)]
    pub output: PathBuf,
    #[arg(long, env = "STARGATE_SPARK_IMAGE")]
    pub spark_image: String,
    #[arg(long)]
    pub spark_pod: Option<String>,
    #[arg(long)]
    pub endpoint: Option<String>,
    /// Require this load-balancer JSON configuration in every selected region.
    #[arg(long, value_name = "PATH", value_parser = parse_expected_config)]
    pub expected_config: Option<Value>,
    #[arg(long, default_value = "18000")]
    pub local_port: NonZeroU16,
    #[arg(long, default_value = "http://localhost:3000")]
    pub grafana_url: String,
    #[arg(long)]
    pub resume: bool,
}

#[derive(Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Settings {
    region: String,
    peer_regions: Vec<String>,
    spark_image: String,
    spark_pod: Option<String>,
    endpoint: Option<String>,
    expected_config: Option<Value>,
    local_port: NonZeroU16,
}

impl Options {
    fn settings(&self) -> Settings {
        Settings {
            region: self.region.clone(),
            peer_regions: self.peer_region.clone(),
            spark_image: self.spark_image.clone(),
            spark_pod: self.spark_pod.clone(),
            endpoint: self.endpoint.clone(),
            expected_config: self.expected_config.clone(),
            local_port: self.local_port,
        }
    }

    fn endpoint(&self) -> String {
        self.endpoint.clone().unwrap_or_else(|| {
            if self.spark_pod.is_some() {
                "http://llm-request-router:8000/v1".into()
            } else {
                format!("http://127.0.0.1:{}/v1", self.local_port)
            }
        })
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
    output: PathBuf,
    binary_sha256: String,
    settings: Settings,
    plan: Plan,
    topology: Value,
    engine: EngineIdentity,
    controls: Value,
    workloads: BTreeMap<String, Fingerprint>,
}

#[derive(Debug, Serialize, Deserialize, PartialEq)]
#[serde(
    tag = "kind",
    rename_all = "kebab-case",
    rename_all_fields = "camelCase",
    deny_unknown_fields
)]
enum EngineIdentity {
    Docker { image_id: String },
    Pod { environment: pod::Environment },
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

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
enum LaunchPhase {
    Creating,
    Ready,
    Running,
    Removed,
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Launch {
    campaign: String,
    owner: String,
    name: String,
    cidfile: PathBuf,
    container_id: Option<String>,
    phase: LaunchPhase,
}

#[derive(Deserialize)]
struct Container {
    #[serde(rename = "Id")]
    id: String,
    #[serde(rename = "Name")]
    name: String,
    #[serde(rename = "Config")]
    config: ContainerConfig,
    #[serde(rename = "State")]
    state: ContainerState,
}

#[derive(Deserialize)]
struct ContainerConfig {
    #[serde(rename = "Labels")]
    labels: BTreeMap<String, String>,
}

#[derive(Deserialize)]
struct ContainerState {
    #[serde(rename = "Status")]
    status: String,
    #[serde(rename = "Running")]
    running: bool,
    #[serde(rename = "ExitCode")]
    exit_code: i32,
}

struct Docker {
    executable: PathBuf,
}

impl Docker {
    fn command(&self) -> Command {
        Command::new(&self.executable)
    }

    async fn image_id(&self, reference: &str) -> Result<String> {
        let mut command = self.command();
        command.args(["image", "inspect", "--format", "{{.Id}}", reference]);
        let output = capture(command, None, CONTROL_TIMEOUT).await?;
        ensure!(
            output.status.success(),
            "inspect Spark image: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        );
        let id = String::from_utf8(output.stdout)
            .context("Spark image ID is not UTF-8")?
            .trim()
            .to_owned();
        ensure!(
            id.strip_prefix("sha256:").is_some_and(valid_id),
            "Docker did not return an immutable Spark image ID"
        );
        Ok(id)
    }

    async fn inspect(&self, name: &str) -> Result<Option<Container>> {
        let mut command = self.command();
        command.args(["container", "inspect", name]);
        let output = capture(command, None, CONTROL_TIMEOUT).await?;
        if !output.status.success() {
            let error = String::from_utf8_lossy(&output.stderr);
            if error.contains("No such container") || error.contains("No such object") {
                return Ok(None);
            }
            bail!("inspect Docker container {name}: {}", error.trim());
        }
        // Deserialize only ownership and state; Config.Env contains the API key.
        let mut containers: Vec<Container> = serde_json::from_slice(&output.stdout)
            .context("parse Docker container ownership and state")?;
        ensure!(
            containers.len() == 1,
            "Docker inspection did not return exactly one container"
        );
        Ok(containers.pop())
    }

    async fn remove(&self, id: &str) -> Result<()> {
        let mut command = self.command();
        command.args(["container", "rm", "--force", id]);
        let output = capture(command, None, CONTROL_TIMEOUT).await?;
        ensure!(
            output.status.success(),
            "remove owned Docker container {id}: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        );
        Ok(())
    }
}

enum Engine<'a> {
    Docker(Docker),
    Pod(pod::Runner<'a>),
}

impl<'a> Engine<'a> {
    fn new(topology: &'a Topology, name: Option<&'a str>) -> Result<Self> {
        match name {
            Some(name) => Ok(Self::Pod(pod::Runner::new(topology, name)?)),
            None => Ok(Self::Docker(Docker {
                executable: PathBuf::from("docker"),
            })),
        }
    }

    async fn identity(&self, reference: &str) -> Result<EngineIdentity> {
        match self {
            Self::Docker(docker) => Ok(EngineIdentity::Docker {
                image_id: docker.image_id(reference).await?,
            }),
            Self::Pod(runner) => Ok(EngineIdentity::Pod {
                environment: runner.inspect(reference).await?,
            }),
        }
    }

    async fn prepare(&self, output: &Path, manifest: &Manifest) -> Result<PathBuf> {
        match self {
            Self::Docker(_) => Ok(PathBuf::from("/campaign")),
            Self::Pod(runner) => Ok(PathBuf::from(
                runner
                    .prepare(&manifest.id, output, &manifest.workloads)
                    .await?,
            )),
        }
    }

    async fn reconcile(&self, output: &Path, manifest: &Manifest) -> Result<()> {
        match self {
            Self::Docker(docker) => reconcile_launches(output, manifest, docker).await,
            Self::Pod(runner) => runner.reconcile(output, &manifest.id).await,
        }
    }
}

struct Resources<'a> {
    engine: Engine<'a>,
    clients: Vec<Process>,
    forward: Option<Process>,
    validated: bool,
}

impl Resources<'_> {
    async fn stop_forward(&mut self) -> Result<()> {
        if let Some(mut forward) = self.forward.take() {
            forward.stop().await.context("stop owned port-forward")?;
        }
        Ok(())
    }

    async fn cleanup(&mut self, output: &Path) -> Result<()> {
        let mut errors = Vec::new();
        if let Err(error) = stop_clients(&mut self.clients).await {
            errors.push(format!("{error:#}"));
        }
        if self.validated && output.join("campaign.json").is_file() {
            match load_manifest(output) {
                Ok(manifest) => {
                    if let Err(error) = self.engine.reconcile(output, &manifest).await {
                        errors.push(format!("{error:#}"));
                    }
                }
                Err(error) => errors.push(format!("{error:#}")),
            }
        }
        if let Err(error) = self.stop_forward().await {
            errors.push(format!("{error:#}"));
        }
        ensure!(
            errors.is_empty(),
            "campaign cleanup is unconfirmed: {}",
            errors.join("; ")
        );
        Ok(())
    }

    async fn start_forward(
        &mut self,
        topology: &Topology,
        options: &Options,
        output: &Path,
    ) -> Result<()> {
        self.stop_forward().await?;
        let port = options.local_port.get();
        let probe = std::net::TcpListener::bind(("127.0.0.1", port))
            .with_context(|| format!("local port {port} is already in use"))?;
        drop(probe);
        let log = OpenOptions::new()
            .create(true)
            .append(true)
            .mode(0o600)
            .open(output.join("port-forward.log"))?;
        let primary = topology.primary();
        let mut command = topology.kubectl(
            &primary.clusters.stargate.kube_context,
            &primary.namespace,
            Duration::ZERO,
        );
        command.args([
            "port-forward",
            "--address",
            "127.0.0.1",
            "service/llm-request-router",
            &format!("{port}:8000"),
        ]);
        command.env("KUBECTL_PORT_FORWARD_WEBSOCKETS", "true");
        command
            .stdout(Stdio::from(log.try_clone()?))
            .stderr(Stdio::from(log));
        self.forward = Some(Process::spawn(command)?);
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            check_forward(&mut self.forward)?;
            if matches!(
                timeout(
                    Duration::from_secs(1),
                    TcpStream::connect(("127.0.0.1", port))
                )
                .await,
                Ok(Ok(_))
            ) {
                check_forward(&mut self.forward)?;
                return Ok(());
            }
            ensure!(
                Instant::now() < deadline,
                "Stargate port-forward did not become ready"
            );
            sleep(Duration::from_millis(200)).await;
        }
    }
}

fn check_forward(forward: &mut Option<Process>) -> Result<()> {
    if let Some(forward) = forward {
        ensure!(
            forward.try_wait()?.is_none(),
            "owned port-forward exited; measurement is invalid"
        );
    }
    Ok(())
}

async fn stop_clients(clients: &mut Vec<Process>) -> Result<()> {
    let mut errors = Vec::new();
    for client in clients.iter_mut() {
        if let Err(error) = client.stop().await {
            errors.push(format!("{error:#}"));
        }
    }
    clients.clear();
    ensure!(
        errors.is_empty(),
        "stop Docker clients: {}",
        errors.join("; ")
    );
    Ok(())
}

pub async fn run(options: Options, plan: Plan) -> Result<()> {
    plan.validate()?;
    for arm in &plan.arms {
        for stream in arm.warmup.iter().chain(&arm.streams) {
            deadline(stream)?;
        }
    }
    ensure!(
        !options.spark_image.is_empty(),
        "Spark image must not be empty"
    );
    ensure!(
        options
            .endpoint
            .as_ref()
            .is_none_or(|endpoint| !endpoint.is_empty()),
        "endpoint must not be empty"
    );
    let topology = Topology::load(&options.region, &options.peer_region)?;
    let engine = Engine::new(&topology, options.spark_pod.as_deref())?;
    let output = prepare_directory(&options.output)?;
    let lock = lock_output(&output)?;
    let mut resources = Resources {
        engine,
        clients: Vec::new(),
        forward: None,
        validated: false,
    };
    let result = tokio::select! {
        biased;
        signal = process::interrupted() => signal,
        result = execute(&output, &options, plan, &topology, &mut resources) => result,
    };
    let cleanup = resources.cleanup(&output).await;
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

async fn execute(
    output: &Path,
    options: &Options,
    plan: Plan,
    topology: &Topology,
    resources: &mut Resources<'_>,
) -> Result<()> {
    let manifest = prepare_manifest(output, options, plan, topology, &resources.engine).await?;
    resources.validated = true;
    resources.engine.reconcile(output, &manifest).await?;
    verify_controls(&manifest, topology, &resources.engine).await?;
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
    let token = topology.load_token().await?;
    let artifact_root = resources.engine.prepare(output, &manifest).await?;
    let execution = Execution {
        output,
        options,
        manifest: &manifest,
        topology,
        token: &token,
        artifact_root: &artifact_root,
    };
    let mut first_pending = true;
    for (arm, accepted) in manifest.plan.arms.iter().zip(accepted) {
        if accepted {
            continue;
        }
        verify_controls(&manifest, topology, &resources.engine).await?;
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
            resources.stop_forward().await?;
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
        if options.spark_pod.is_none() && options.endpoint.is_none() && resources.forward.is_none()
        {
            resources.start_forward(topology, options, output).await?;
        }
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
                    .streams(arm, std::slice::from_ref(warmup), &attempt, true, resources)
                    .await?
                    .remove(0),
            )
        } else {
            None
        };
        let reports = execution
            .streams(arm, &arm.streams, &attempt, false, resources)
            .await?;
        let ended_at = now()?;
        let after = topology.pod_state().await?;
        evidence_file(output, &attempt, "pods.after.json", &after, &mut evidence)?;
        ensure!(
            before == after,
            "Pod replacement or restart invalidated {}",
            arm.directory.display()
        );
        verify_controls(&manifest, topology, &resources.engine).await?;
        if let Some(baseline) = baseline {
            let after = topology.cache_stats().await?;
            let delta = cache_delta(&baseline, &after)?;
            evidence_file(output, &attempt, "cache.after.json", &after, &mut evidence)?;
            evidence_file(output, &attempt, "cache.delta.json", &delta, &mut evidence)?;
        }
        atomic_json(
            &output.join(&attempt).join("grafana.json"),
            &grafana_links(&options.grafana_url, &started_at, &ended_at)?,
        )?;
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
    engine: &Engine<'_>,
) -> Result<Manifest> {
    let path = output.join("campaign.json");
    let binary_sha256 =
        hash_file(&std::env::current_exe().context("locate benchmark executable")?)?;
    let topology_value = serde_json::to_value(&topology.regions)?;
    if path.exists() {
        ensure!(
            options.resume,
            "campaign already exists; pass --resume to continue"
        );
        let manifest = load_manifest(output)?;
        ensure!(
            manifest.settings == options.settings()
                && manifest.binary_sha256 == binary_sha256
                && manifest.topology == topology_value
                && serde_json::to_value(&manifest.plan)? == serde_json::to_value(&plan)?,
            "resume inputs differ from the immutable campaign; use a new output directory"
        );
        verify_workloads(output, &manifest.plan, &manifest.workloads)?;
        return Ok(manifest);
    }
    for entry in fs::read_dir(output)? {
        let entry = entry?;
        ensure!(
            match entry.file_name().to_str() {
                Some(".lock" | "plan.json") => entry.file_type()?.is_file(),
                Some("workloads") => entry.file_type()?.is_dir(),
                _ => false,
            },
            "output contains foreign or unfinished artifacts: {}",
            entry.path().display()
        );
    }
    let plan_path = output.join("plan.json");
    let workloads = if plan_path.is_file() {
        let existing: Plan = read_json(&plan_path)?;
        existing.validate()?;
        ensure!(
            serde_json::to_value(existing)? == serde_json::to_value(&plan)?,
            "existing plan does not match requested campaign"
        );
        let mut fingerprints = BTreeMap::new();
        for (name, recipe) in &plan.workloads {
            fingerprints.insert(name.clone(), recipe.fingerprint()?);
        }
        verify_workloads(output, &plan, &fingerprints)?;
        fingerprints
    } else {
        ensure!(
            !output.join("workloads").exists(),
            "unfinished workload directory exists; use a new output directory"
        );
        let mut fingerprints = BTreeMap::new();
        for (name, recipe) in &plan.workloads {
            fingerprints.insert(
                name.clone(),
                recipe.write(&output.join("workloads").join(name))?,
            );
        }
        atomic_json(&plan_path, &plan)?;
        fingerprints
    };
    let manifest = Manifest {
        version: VERSION,
        id: Uuid::new_v4().to_string(),
        output: output.to_owned(),
        binary_sha256,
        settings: options.settings(),
        plan,
        topology: topology_value,
        engine: engine.identity(&options.spark_image).await?,
        controls: topology.snapshot_controls().await?,
        workloads,
    };
    validate_expected_config(&manifest.settings, &manifest.controls)?;
    atomic_json(&path, &manifest)?;
    Ok(manifest)
}

fn validate_expected_config(settings: &Settings, controls: &Value) -> Result<()> {
    let Some(expected) = &settings.expected_config else {
        return Ok(());
    };
    ensure!(
        expected.is_object(),
        "expected configuration must be a JSON object"
    );
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

async fn verify_controls(
    manifest: &Manifest,
    topology: &Topology,
    engine: &Engine<'_>,
) -> Result<()> {
    ensure!(
        engine.identity(&manifest.settings.spark_image).await? == manifest.engine,
        "Spark engine identity changed; use a new campaign directory"
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
    options: &'a Options,
    manifest: &'a Manifest,
    topology: &'a Topology,
    token: &'a str,
    artifact_root: &'a Path,
}

struct PreparedStream {
    directory: PathBuf,
    arguments: Vec<String>,
    timeout: Duration,
}

impl Execution<'_> {
    async fn streams(
        &self,
        arm: &Arm,
        streams: &[Stream],
        attempt: &Path,
        warmup: bool,
        resources: &mut Resources<'_>,
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
                &self.options.endpoint(),
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
            });
        }
        match &resources.engine {
            Engine::Docker(docker) => {
                self.docker_streams(
                    &prepared,
                    attempt,
                    docker,
                    &mut resources.clients,
                    &mut resources.forward,
                )
                .await?
            }
            Engine::Pod(runner) => self.pod_streams(&prepared, attempt, runner).await?,
        }
        streams
            .iter()
            .zip(prepared)
            .map(|(stream, prepared)| {
                let path = prepared.directory.join("spark.json");
                let verified = report::inspect(
                    &self.output.join(&path),
                    stream.limit,
                    arm.kind == ArmKind::Capacity,
                )?;
                if warmup || arm.kind == ArmKind::Smoke {
                    ensure!(
                        verified.metrics.failed == 0,
                        "smoke or warm-up requests failed: {}",
                        path.display()
                    );
                }
                Ok(AcceptedReport {
                    stream: stream.name.clone(),
                    path,
                    sha256: verified.sha256,
                })
            })
            .collect()
    }

    async fn docker_streams(
        &self,
        streams: &[PreparedStream],
        attempt: &Path,
        docker: &Docker,
        clients: &mut Vec<Process>,
        forward: &mut Option<Process>,
    ) -> Result<()> {
        let Self {
            output,
            manifest,
            token,
            ..
        } = self;
        ensure!(
            clients.is_empty(),
            "previous Docker clients are still owned"
        );
        let mut launches = Vec::new();
        let mut maximum = Duration::ZERO;
        let EngineIdentity::Docker { image_id } = &manifest.engine else {
            bail!("Docker execution does not match the frozen engine");
        };
        for stream in streams {
            let owner = Uuid::new_v4().simple().to_string();
            let launch_path = output
                .join(attempt)
                .join("launches")
                .join(format!("{owner}.json"));
            let mut launch = Launch {
                campaign: manifest.id.clone(),
                name: format!("stargate-bench-{owner}"),
                cidfile: attempt.join("launches").join(format!("{owner}.cid")),
                owner,
                container_id: None,
                phase: LaunchPhase::Creating,
            };
            atomic_json(&launch_path, &launch)?;
            let mut command = docker.command();
            command
                .args([
                    "container",
                    "create",
                    "--pull",
                    "never",
                    "--name",
                    &launch.name,
                    "--cidfile",
                ])
                .arg(output.join(&launch.cidfile))
                .args([
                    "--label",
                    &format!("{OWNER_LABEL}={}", launch.owner),
                    "--label",
                    &format!("{CAMPAIGN_LABEL}={}", manifest.id),
                    "--network",
                    "host",
                    "-e",
                    "OPENAI_API_KEY",
                    "-v",
                ])
                .arg(format!("{}:/campaign", output.display()))
                .arg(image_id);
            // The Docker image supplies Spark as its entry point.
            command.args(&stream.arguments[1..]);
            command.env("OPENAI_API_KEY", token);
            let created = capture(command, None, CONTROL_TIMEOUT)
                .await
                .context("create owned Spark container")?;
            ensure!(
                created.status.success(),
                "create owned Spark container: {}",
                String::from_utf8_lossy(&created.stderr)
                    .replace(*token, "[REDACTED]")
                    .trim()
            );
            let id = String::from_utf8(created.stdout)
                .context("Docker creation ID is not UTF-8")?
                .trim()
                .to_owned();
            ensure!(valid_id(&id), "Docker create did not return a container ID");
            let container = docker
                .inspect(&id)
                .await?
                .context("created container is missing")?;
            verify_owner(&launch, &manifest.id, &container)?;
            ensure!(
                !container.state.running && container.state.status == "created",
                "newly created Spark container is not in created state"
            );
            launch.container_id = Some(id);
            launch.phase = LaunchPhase::Ready;
            atomic_json(&launch_path, &launch)?;
            launches.push((launch_path, launch));
            maximum = maximum.max(stream.timeout);
        }
        check_forward(forward)?;
        let deadline = Instant::now()
            .checked_add(maximum)
            .context("Spark deadline exceeds monotonic clock range")?;
        for ((launch_path, launch), stream) in launches.iter_mut().zip(streams) {
            let id = launch
                .container_id
                .as_deref()
                .context("ready Docker launch has no container ID")?;
            let log = File::create(output.join(&stream.directory).join("spark.log"))?;
            let mut start = docker.command();
            start
                .args(["container", "start", "--attach", id])
                .stdin(Stdio::null())
                .stdout(Stdio::from(log.try_clone()?))
                .stderr(Stdio::from(log));
            launch.phase = LaunchPhase::Running;
            atomic_json(launch_path, launch)?;
            match Process::spawn(start) {
                Ok(process) => clients.push(process),
                Err(error) => {
                    // No start process exists, so this container cannot have started.
                    launch.phase = LaunchPhase::Ready;
                    atomic_json(launch_path, launch)?;
                    return Err(error);
                }
            }
        }
        loop {
            check_forward(forward)?;
            let mut complete = true;
            for client in clients.iter_mut() {
                match client.try_wait()? {
                    Some(status) => {
                        ensure!(status.success(), "Spark Docker client failed with {status}")
                    }
                    None => complete = false,
                }
            }
            if complete {
                break;
            }
            ensure!(
                Instant::now() < deadline,
                "Spark exceeded its expected duration"
            );
            sleep(Duration::from_millis(200)).await;
        }
        check_forward(forward)?;
        for (path, launch) in &mut launches {
            let container = docker.inspect(&launch.name).await?.context(
                "completed Docker client has no container; launch completion is unconfirmed",
            )?;
            verify_owner(launch, &manifest.id, &container)?;
            ensure!(
                !container.state.running
                    && container.state.status == "exited"
                    && container.state.exit_code == 0,
                "Spark container {} did not exit successfully",
                launch.name
            );
            launch.container_id = Some(container.id);
            atomic_json(path, launch)?;
        }
        stop_clients(clients).await?;
        for (path, _) in launches {
            reconcile_launch(output, &manifest.id, docker, &path).await?;
        }
        Ok(())
    }

    async fn pod_streams(
        &self,
        streams: &[PreparedStream],
        attempt: &Path,
        runner: &pod::Runner<'_>,
    ) -> Result<()> {
        let EngineIdentity::Pod { environment } = &self.manifest.engine else {
            bail!("Pod execution does not match the frozen engine");
        };
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
                &environment.identity,
                self.token,
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
    endpoint: &str,
    primary: &crate::cluster::Region,
) -> Result<Vec<String>> {
    let mut args = vec![
        "spark".into(),
        "--endpoint".into(),
        endpoint.into(),
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
        "--rate-limit".into(),
        stream.rate.to_string(),
        "--timeout".into(),
        format!("{}s", stream.limits.timeout_seconds),
        "--max-tokens".into(),
        stream.limits.max_tokens.to_string(),
        "--warmup".into(),
        "0".into(),
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
    match stream.limit {
        RunLimit::Requests(requests) => args.extend(["--requests".into(), requests.to_string()]),
        RunLimit::DurationSeconds(seconds) => {
            args.extend(["--duration".into(), format!("{seconds}s")])
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
    .checked_add(600)
    .context("Spark timeout exceeds u64")?;
    let duration = Duration::from_secs(seconds);
    ensure!(
        Instant::now().checked_add(duration).is_some(),
        "Spark deadline exceeds monotonic clock range"
    );
    Ok(duration)
}

pub async fn reconcile(output: &Path) -> Result<()> {
    let output = output.canonicalize().context("locate campaign output")?;
    let manifest = load_manifest(&output)?;
    let _lock = lock_output(&output)?;
    let topology = Topology::load(&manifest.settings.region, &manifest.settings.peer_regions)?;
    Engine::new(&topology, manifest.settings.spark_pod.as_deref())?
        .reconcile(&output, &manifest)
        .await
}

async fn reconcile_launches(output: &Path, manifest: &Manifest, docker: &Docker) -> Result<()> {
    let attempts = output.join("attempts");
    if !attempts.exists() {
        return Ok(());
    }
    let mut records = Vec::new();
    for attempt in fs::read_dir(attempts)? {
        let attempt = attempt?;
        ensure!(
            attempt.file_type()?.is_dir(),
            "unexpected attempt artifact {}",
            attempt.path().display()
        );
        Uuid::parse_str(
            attempt
                .file_name()
                .to_str()
                .context("attempt ID is not UTF-8")?,
        )
        .context("invalid attempt ID")?;
        let launches = attempt.path().join("launches");
        if !launches.exists() {
            continue;
        }
        for entry in fs::read_dir(launches)? {
            let entry = entry?;
            if entry
                .path()
                .extension()
                .is_some_and(|extension| extension == "json")
            {
                ensure!(
                    entry.file_type()?.is_file(),
                    "launch record is not a regular file"
                );
                records.push(entry.path());
            }
        }
    }
    records.sort();
    let mut errors = Vec::new();
    for path in records {
        if let Err(error) = reconcile_launch(output, &manifest.id, docker, &path).await {
            errors.push(format!("{}: {error:#}", path.display()));
        }
    }
    ensure!(
        errors.is_empty(),
        "owned Docker cleanup is unconfirmed; no new traffic may start:\n{}",
        errors.join("\n")
    );
    Ok(())
}

async fn reconcile_launch(
    output: &Path,
    campaign: &str,
    docker: &Docker,
    path: &Path,
) -> Result<()> {
    let mut launch: Launch = read_json(path)?;
    ensure!(
        launch.campaign == campaign,
        "launch belongs to a different campaign"
    );
    Uuid::parse_str(&launch.owner).context("invalid Docker owner marker")?;
    ensure!(
        launch.name == format!("stargate-bench-{}", launch.owner),
        "Docker launch name does not match its owner marker"
    );
    if launch.phase == LaunchPhase::Removed {
        return Ok(());
    }
    if let Some(container) = docker.inspect(&launch.name).await? {
        verify_owner(&launch, campaign, &container)?;
        if let Some(id) = read_cid(output, &launch.cidfile)? {
            ensure!(
                id == container.id,
                "Docker CID file does not match the owned container"
            );
        }
        launch.container_id = Some(container.id.clone());
        atomic_json(path, &launch)?;
        let removed = docker.remove(&container.id).await;
        if docker.inspect(&container.id).await?.is_some() {
            removed?;
            bail!("owned Docker container still exists after removal");
        }
    } else {
        if launch.phase == LaunchPhase::Creating {
            // An uncertain create may materialize later, but this controller has
            // never issued start. Keep the record for subsequent cleanup scans.
            return Ok(());
        }
        let id = launch
            .container_id
            .clone()
            .or(read_cid(output, &launch.cidfile)?);
        let id = id.context(
            "launch was not acknowledged and its container is absent; cleanup remains unconfirmed",
        )?;
        ensure!(valid_id(&id), "invalid Docker container ID");
        if let Some(container) = docker.inspect(&id).await? {
            verify_owner(&launch, campaign, &container)?;
            docker.remove(&id).await?;
            ensure!(
                docker.inspect(&id).await?.is_none(),
                "owned Docker container still exists"
            );
        }
    }
    launch.phase = LaunchPhase::Removed;
    atomic_json(path, &launch)
}

fn verify_owner(launch: &Launch, campaign: &str, container: &Container) -> Result<()> {
    ensure!(
        valid_id(&container.id),
        "Docker returned an invalid container ID"
    );
    ensure!(
        container.name == format!("/{}", launch.name)
            && container.config.labels.get(OWNER_LABEL) == Some(&launch.owner)
            && container
                .config
                .labels
                .get(CAMPAIGN_LABEL)
                .map(String::as_str)
                == Some(campaign),
        "Docker ownership mismatch; refusing to stop container {}",
        launch.name
    );
    if let Some(expected) = &launch.container_id {
        ensure!(
            expected == &container.id,
            "Docker container ID changed for owned launch"
        );
    }
    Ok(())
}

fn valid_id(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|byte| byte.is_ascii_hexdigit())
}

fn read_cid(output: &Path, relative: &Path) -> Result<Option<String>> {
    let path = below(output, relative)?;
    match fs::read_to_string(&path) {
        Ok(id) => {
            let id = id.trim().to_owned();
            ensure!(valid_id(&id), "invalid Docker CID file {}", path.display());
            Ok(Some(id))
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(error) => Err(error).context("read Docker CID file"),
    }
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
    let verify = |stream: &Stream, accepted: &AcceptedReport| -> Result<report::Metrics> {
        ensure!(
            stream.name == accepted.stream
                && accepted.path
                    == receipt
                        .attempt
                        .join(stream.name.as_deref().unwrap_or("main"))
                        .join("spark.json"),
            "accepted report does not match the planned stream"
        );
        let verified = report::verify_accepted(
            &below(output, &accepted.path)?,
            &accepted.sha256,
            stream.limit,
            arm.kind == ArmKind::Capacity,
        )?;
        Ok(verified.metrics)
    };
    if let (Some(stream), Some(accepted)) = (&arm.warmup, &receipt.warmup) {
        ensure!(
            verify(stream, accepted)?.failed == 0,
            "accepted warm-up report contains failures"
        );
    }
    let mut metrics = Vec::new();
    for (stream, accepted) in arm.streams.iter().zip(&receipt.reports) {
        let measured = verify(stream, accepted)?;
        if arm.kind == ArmKind::Smoke {
            ensure!(
                measured.failed == 0,
                "accepted smoke report contains failures"
            );
        }
        metrics.push(measured);
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
    ensure!(
        manifest.output == output,
        "campaign output moved; use its original directory"
    );
    Uuid::parse_str(&manifest.id).context("invalid campaign ID")?;
    ensure!(
        matches!(&manifest.engine, EngineIdentity::Pod { .. })
            == manifest.settings.spark_pod.is_some(),
        "frozen engine does not match the selected transport"
    );
    manifest.plan.validate()?;
    validate_expected_config(&manifest.settings, &manifest.controls)?;
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

fn grafana_links(base: &str, started_at: &str, ended_at: &str) -> Result<Value> {
    let start =
        OffsetDateTime::parse(started_at, &Rfc3339)?.unix_timestamp_nanos() / 1_000_000 - 60_000;
    let end =
        OffsetDateTime::parse(ended_at, &Rfc3339)?.unix_timestamp_nanos() / 1_000_000 + 60_000;
    let base = base.trim_end_matches('/');
    Ok(json!({
        "stargate": format!("{base}/d/stargate-dev-services/stargate?from={start}&to={end}"),
        "backendBalance": format!("{base}/d/stargate-backend-balance/stargate-backend-balance?from={start}&to={end}"),
    }))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::suite::{Algorithm, Suite};
    use crate::test_support::FakeCommand;

    fn plan(name: &str) -> Plan {
        Suite::from_yaml(include_str!("../../loadtest/suite.yaml"))
            .unwrap()
            .plan(name, &Algorithm::ALL)
            .unwrap()
    }

    fn options(output: &Path) -> Options {
        Options {
            region: "us-west-2".into(),
            peer_region: Vec::new(),
            output: output.to_owned(),
            spark_image: "spark:test".into(),
            spark_pod: None,
            endpoint: Some("http://test.invalid/v1".into()),
            expected_config: None,
            local_port: NonZeroU16::new(18000).unwrap(),
            grafana_url: "http://grafana.invalid".into(),
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
            output: output.to_owned(),
            binary_sha256: "test-binary".into(),
            settings: options(output).settings(),
            plan,
            topology: json!([]),
            engine: EngineIdentity::Docker {
                image_id: format!("sha256:{}", "a".repeat(64)),
            },
            controls: json!({}),
            workloads,
        }
    }

    fn launch(output: &Path, phase: LaunchPhase) -> (PathBuf, Launch) {
        let owner = Uuid::new_v4().simple().to_string();
        let directory = Path::new("attempts")
            .join(Uuid::new_v4().to_string())
            .join("launches");
        let path = output.join(&directory).join(format!("{owner}.json"));
        let launch = Launch {
            campaign: "test-campaign".into(),
            name: format!("stargate-bench-{owner}"),
            cidfile: directory.join(format!("{owner}.cid")),
            owner,
            container_id: if phase == LaunchPhase::Creating {
                None
            } else {
                Some("a".repeat(64))
            },
            phase,
        };
        atomic_json(&path, &launch).unwrap();
        (path, launch)
    }

    fn container(launch: &Launch, owner: &str) -> String {
        json!([{
            "Id":"a".repeat(64), "Name":format!("/{}",launch.name),
            "Config":{"Labels":{OWNER_LABEL:owner,CAMPAIGN_LABEL:launch.campaign},"Env":["OPENAI_API_KEY=do-not-save"]},
            "State":{"Status":"running","Running":true,"ExitCode":0}
        }]).to_string()
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
    fn frozen_expected_policy_must_match_every_selected_region() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let path = directory.path().join("campaign.json");
        let mut frozen = manifest(directory.path(), plan("smoke"));
        atomic_json(&path, &frozen)?;
        load_manifest(directory.path())?;

        let policy = json!({"max_queued":4});
        frozen.settings.peer_regions = vec!["us-east-1".into()];
        frozen.settings.expected_config = Some(policy.clone());
        frozen.controls = json!({"loadBalancers":{
            "us-west-2":policy, "us-east-1":policy
        }});
        atomic_json(&path, &frozen)?;
        load_manifest(directory.path())?;

        frozen.controls["loadBalancers"]["us-east-1"]["max_queued"] = json!(8);
        atomic_json(&path, &frozen)?;
        assert!(
            load_manifest(directory.path())
                .unwrap_err()
                .to_string()
                .contains("us-east-1")
        );
        frozen.controls["loadBalancers"] = json!({"us-west-2":policy});
        atomic_json(&path, &frozen)?;
        assert!(
            load_manifest(directory.path())
                .unwrap_err()
                .to_string()
                .contains("us-east-1")
        );
        frozen.controls = json!({});
        atomic_json(&path, &frozen)?;
        assert!(load_manifest(directory.path()).is_err());
        frozen.settings.expected_config = Some(json!([]));
        atomic_json(&path, &frozen)?;
        assert!(
            load_manifest(directory.path())
                .unwrap_err()
                .to_string()
                .contains("JSON object")
        );
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
            "http://test.invalid/v1",
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
            "http://test.invalid/v1",
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
    fn pod_defaults_and_command_paths_do_not_use_the_local_forward() -> Result<()> {
        let mut options = options(Path::new("unused"));
        options.endpoint = None;
        assert_eq!(options.endpoint(), "http://127.0.0.1:18000/v1");
        options.spark_pod = Some("spark-worker".into());
        assert_eq!(options.endpoint(), "http://llm-request-router:8000/v1");
        let topology = Topology::load("us-west-2", &[])?;
        let plan = plan("smoke");
        let arm = &plan.arms[0];
        let root = Path::new("/tmp/stargate-bench/campaign-id");
        let arguments = spark_arguments(
            &arm.streams[0],
            arm,
            &plan,
            Path::new("attempts/id/main"),
            root,
            &options.endpoint(),
            topology.primary(),
        )?;
        assert_eq!(arguments[0], "spark");
        assert!(arguments.windows(2).any(|pair| pair
            == [
                "--output",
                "/tmp/stargate-bench/campaign-id/attempts/id/main/spark.json"
            ]));
        assert!(arguments.windows(2).any(|pair| pair
            == [
                "--workload",
                "/tmp/stargate-bench/campaign-id/workloads/smoke.yaml"
            ]));
        options.endpoint = Some("http://custom.invalid/v1".into());
        assert_eq!(options.endpoint(), "http://custom.invalid/v1");
        Ok(())
    }

    #[test]
    fn manifest_cannot_switch_transport_by_changing_only_settings() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let mut frozen = manifest(directory.path(), plan("smoke"));
        frozen.settings.spark_pod = Some("spark-worker".into());
        atomic_json(&directory.path().join("campaign.json"), &frozen)?;
        assert!(load_manifest(directory.path()).is_err());
        Ok(())
    }

    #[tokio::test]
    async fn reconciliation_never_removes_a_container_without_owner_proof() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let (path, launch) = launch(directory.path(), LaunchPhase::Running);
        let fake = FakeCommand::new();
        let response = container(&launch, "another-owner");
        fake.respond(
            &["container", "inspect", &launch.name],
            &[(0, &response, "")],
        );
        let docker = Docker {
            executable: fake.executable(),
        };
        assert!(
            reconcile_launch(directory.path(), &launch.campaign, &docker, &path)
                .await
                .is_err()
        );
        assert_eq!(fake.calls().len(), 1);
        assert_eq!(read_json::<Launch>(&path)?.phase, LaunchPhase::Running);
        Ok(())
    }

    #[tokio::test]
    async fn reconciliation_verifies_removal_even_if_the_acknowledgement_fails() -> Result<()> {
        for status in [0, 1] {
            let directory = tempfile::tempdir()?;
            let (path, launch) = launch(directory.path(), LaunchPhase::Running);
            let fake = FakeCommand::new();
            let response = container(&launch, &launch.owner);
            let id = "a".repeat(64);
            fake.respond(
                &["container", "inspect", &launch.name],
                &[(0, &response, "")],
            );
            fake.respond(
                &["container", "rm", "--force", &id],
                &[(status, "", "lost acknowledgement")],
            );
            fake.respond(
                &["container", "inspect", &id],
                &[(1, "", "No such container")],
            );
            let docker = Docker {
                executable: fake.executable(),
            };
            reconcile_launch(directory.path(), &launch.campaign, &docker, &path).await?;
            assert_eq!(read_json::<Launch>(&path)?.phase, LaunchPhase::Removed);
            assert_eq!(fake.calls().len(), 3);
            assert!(!fs::read_to_string(&path)?.contains("do-not-save"));
        }
        Ok(())
    }

    #[tokio::test]
    async fn missing_creation_cannot_have_traffic_but_missing_start_proof_is_rejected() -> Result<()>
    {
        let directory = tempfile::tempdir()?;
        let (path, mut launch) = launch(directory.path(), LaunchPhase::Creating);
        let fake = FakeCommand::new();
        fake.respond(
            &["container", "inspect", &launch.name],
            &[(1, "", "No such container")],
        );
        let docker = Docker {
            executable: fake.executable(),
        };
        reconcile_launch(directory.path(), &launch.campaign, &docker, &path).await?;
        assert_eq!(read_json::<Launch>(&path)?.phase, LaunchPhase::Creating);
        launch.phase = LaunchPhase::Running;
        atomic_json(&path, &launch)?;
        assert!(
            reconcile_launch(directory.path(), &launch.campaign, &docker, &path)
                .await
                .is_err()
        );
        assert_eq!(read_json::<Launch>(&path)?.phase, LaunchPhase::Running);
        Ok(())
    }

    #[tokio::test]
    async fn foreign_outputs_and_changed_resume_inputs_fail_before_external_commands() -> Result<()>
    {
        let directory = tempfile::tempdir()?;
        let fake = FakeCommand::new();
        let engine = Engine::Docker(Docker {
            executable: fake.executable(),
        });
        let topology = Topology::load("us-west-2", &[])?;
        let mut options = options(directory.path());
        fs::write(directory.path().join("foreign"), "keep me")?;
        assert!(
            prepare_manifest(
                directory.path(),
                &options,
                plan("smoke"),
                &topology,
                &engine
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
        options.spark_image = "different:image".into();
        assert!(
            prepare_manifest(
                directory.path(),
                &options,
                plan("smoke"),
                &topology,
                &engine
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

    #[tokio::test]
    async fn plan_adoption_rejects_a_rehashed_but_different_workload() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let frozen = manifest(directory.path(), plan("smoke"));
        atomic_json(&directory.path().join("plan.json"), &frozen.plan)?;
        let path = directory.path().join("workloads/smoke.yaml");
        fs::write(&path, "different prompt bytes")?;
        atomic_json(
            &path.with_extension("json"),
            &Fingerprint {
                prompt_count: 32,
                sha256: hash_file(&path)?,
            },
        )?;
        let fake = FakeCommand::new();
        let engine = Engine::Docker(Docker {
            executable: fake.executable(),
        });
        let topology = Topology::load("us-west-2", &[])?;
        let error = prepare_manifest(
            directory.path(),
            &options(directory.path()),
            plan("smoke"),
            &topology,
            &engine,
        )
        .await
        .unwrap_err();
        assert!(format!("{error:#}").contains("workload bytes changed"));
        assert!(fake.calls().is_empty());
        assert!(!directory.path().join("campaign.json").exists());
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

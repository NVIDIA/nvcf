// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::BTreeMap;
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::Path;
use std::process::Output;
use std::time::Duration;

use anyhow::{Context, Result, bail, ensure};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use tokio::process::Command;
use tokio::time::{Instant, sleep};
use uuid::Uuid;

use crate::artifact::{atomic_json, below, hash_file, read_json};
use crate::cluster::{Topology, authentication_error, transient_control_error};
use crate::process::capture;
use crate::workload::Fingerprint;

const CONTROL_TIMEOUT: Duration = Duration::from_secs(30);
const COPY_TIMEOUT: Duration = Duration::from_secs(300);
const REMOTE_BASE: &str = "/tmp/stargate-bench";
const CONTAINER: &str = "spark";
const LAUNCH: &str = r#"IFS= read -r OPENAI_API_KEY || exit 1; export OPENAI_API_KEY; run_dir=$1; shift; nohup setsid bash -c "$@" > "$run_dir/spark.log" 2>&1 < /dev/null &"#;
const WAIT: &str = r#"run_dir=$1; shift; exec 9>"$run_dir/spark.lock" || exit 1; flock -x -w 5 9 || exit 1; if test -e "$run_dir/spark.cancelled"; then exit 143; fi; printf "%s\n" "$$" > "$run_dir/spark.pid.tmp" && mv "$run_dir/spark.pid.tmp" "$run_dir/spark.pid" || exit 1; flock -u 9; exec 9>&-; "$@"; run_status=$?; printf "%s\n" "$run_status" > "$run_dir/spark.exit.tmp"; mv "$run_dir/spark.exit.tmp" "$run_dir/spark.exit"; exit "$run_status""#;
const CANCEL: &str = r#"run_dir=$1; mkdir -p "$run_dir" || exit 1; exec 9>"$run_dir/spark.lock" || exit 1; flock -x -w 5 9 || exit 1; : > "$run_dir/spark.cancelled""#;
const STOP: &str =
    r#"if tr "\0" "\n" < "/proc/$1/cmdline" | grep -Fxq -- "$2"; then kill -KILL -- "-$1"; fi"#;
const GROUP_STATS: &str = r#"for stat_file in /proc/[0-9]*/stat; do if stat_value=$(cat "$stat_file"); then printf "%s\0" "$stat_value"; elif test -e "$stat_file"; then exit 1; fi; done"#;
const REQUIRE_TOOLS: &str = "for tool in bash nohup setsid timeout flock grep tr tail cat sha256sum tar mkdir mv; do command -v \"$tool\" >/dev/null || exit 1; done; test -r /proc/self/stat";

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Identity {
    pub uid: String,
    pub container_id: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Environment {
    pub identity: Identity,
    pub image: String,
    pub image_id: String,
    pub resources: Value,
    pub pod_spec_sha256: String,
    pub spark_version: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Target {
    context: String,
    namespace: String,
    pod: String,
    container: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case", deny_unknown_fields)]
enum Phase {
    Pending,
    Exited { code: u8 },
    Cancelled,
    CancelUnconfirmed,
}

#[derive(Debug, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Launch {
    protocol_version: u32,
    campaign: String,
    target: Target,
    directory: String,
    marker: String,
    identity: Identity,
    phase: Phase,
    #[serde(skip, default = "Instant::now")]
    started: Instant,
}

pub struct Runner<'a> {
    topology: &'a Topology,
    pod: &'a str,
}

impl<'a> Runner<'a> {
    pub fn new(topology: &'a Topology, pod: &'a str) -> Result<Self> {
        validate_name(pod)?;
        Ok(Self { topology, pod })
    }

    fn target(&self) -> Target {
        let primary = self.topology.primary();
        Target {
            context: primary.clusters.stargate.kube_context.clone(),
            namespace: primary.namespace.clone(),
            pod: self.pod.to_owned(),
            container: CONTAINER.to_owned(),
        }
    }

    fn exec_command(&self, target: &Target, arguments: &[&str], input: bool) -> Command {
        let mut command =
            self.topology
                .kubectl(&target.context, &target.namespace, CONTROL_TIMEOUT);
        command.arg("exec");
        if input {
            command.arg("-i");
        }
        command.args([&target.pod, "-c", &target.container, "--"]);
        command.args(arguments);
        command
    }

    async fn exec(
        &self,
        target: &Target,
        arguments: &[&str],
        input: Option<&[u8]>,
    ) -> Result<Output> {
        capture(
            self.exec_command(target, arguments, input.is_some()),
            input,
            CONTROL_TIMEOUT,
        )
        .await
    }

    async fn read_exec(&self, target: &Target, arguments: &[&str]) -> Result<Output> {
        for attempt in 0..3 {
            match self.exec(target, arguments, None).await {
                Ok(output) => {
                    let message = String::from_utf8_lossy(&output.stderr);
                    if output.status.success()
                        || authentication_error(&message)
                        || !transient_control_error(&message)
                        || attempt == 2
                    {
                        return Ok(output);
                    }
                }
                Err(error) if retryable(&error) && attempt < 2 => {}
                Err(error) => return Err(error),
            }
            sleep(Duration::from_secs(attempt + 1)).await;
        }
        unreachable!("last read attempt returns")
    }

    async fn pod(&self, target: &Target) -> Result<Option<Value>> {
        let value = self
            .topology
            .read(
                &target.context,
                &target.namespace,
                &[
                    "get",
                    "pod",
                    &target.pod,
                    "--ignore-not-found",
                    "-o",
                    "json",
                ],
                CONTROL_TIMEOUT,
            )
            .await?;
        if value.trim().is_empty() {
            return Ok(None);
        }
        Ok(Some(
            serde_json::from_str(&value).context("parse Spark Pod")?,
        ))
    }

    async fn identity(&self, target: &Target) -> Result<Identity> {
        let pod = self
            .pod(target)
            .await?
            .context("Spark Pod does not exist")?;
        selected_identity(&pod)
    }

    pub async fn inspect(&self, expected_image: &str) -> Result<Environment> {
        let target = self.target();
        let pod = self
            .pod(&target)
            .await?
            .context("Spark Pod does not exist")?;
        let identity = selected_identity(&pod)?;
        let selected = selected_container(&pod)?;
        let image = selected
            .get("image")
            .and_then(Value::as_str)
            .context("Spark Pod image missing")?;
        ensure!(
            image == expected_image,
            "selected Spark Pod image differs from --spark-image"
        );
        checked(
            self.read_exec(&target, &["bash", "-c", REQUIRE_TOOLS])
                .await?,
            "check Spark Pod tools",
        )?;
        let version = checked(
            self.read_exec(&target, &["spark", "--version"]).await?,
            "read Spark version",
        )?;
        let spark_version = String::from_utf8(version)
            .context("Spark version is not UTF-8")?
            .trim()
            .to_owned();
        ensure!(!spark_version.is_empty(), "Spark version is empty");
        ensure!(
            self.identity(&target).await? == identity,
            "Spark container changed during inspection"
        );
        Ok(Environment {
            identity,
            image: image.to_owned(),
            image_id: selected_status(&pod)?
                .get("imageID")
                .and_then(Value::as_str)
                .filter(|value| !value.is_empty())
                .context("Spark runtime image ID missing")?
                .to_owned(),
            resources: selected
                .get("resources")
                .cloned()
                .unwrap_or_else(|| json!({})),
            pod_spec_sha256: format!(
                "{:x}",
                Sha256::digest(serde_json::to_vec(
                    pod.get("spec").context("Spark Pod spec missing")?
                )?)
            ),
            spark_version,
        })
    }

    pub async fn prepare(
        &self,
        campaign_id: &str,
        output: &Path,
        fingerprints: &BTreeMap<String, Fingerprint>,
    ) -> Result<String> {
        let campaign = Uuid::parse_str(campaign_id).context("invalid campaign ID")?;
        let remote = format!("{REMOTE_BASE}/{campaign}");
        for name in fingerprints.keys() {
            ensure!(
                Path::new(name).components().count() == 1 && !name.starts_with('.'),
                "invalid workload filename"
            );
        }
        let target = self.target();
        let identity = self.identity(&target).await?;
        let directory = format!("{remote}/workloads");
        checked(
            self.exec(&target, &["mkdir", "-p", &directory], None)
                .await?,
            "create remote campaign directory",
        )?;
        for (name, fingerprint) in fingerprints {
            let final_path = format!("{directory}/{name}");
            let exists = self
                .read_exec(
                    &target,
                    &["bash", "-c", "test -e \"$1\"", "--", &final_path],
                )
                .await?;
            if !exists.status.success() {
                ensure!(
                    exists.status.code() == Some(1) && exists.stderr.is_empty(),
                    "cannot inspect remote workload: {}",
                    String::from_utf8_lossy(&exists.stderr).trim()
                );
                let staged = format!("{directory}/.upload-{}", Uuid::new_v4());
                let local = below(output, &Path::new("workloads").join(name))?;
                let mut copy =
                    self.topology
                        .kubectl(&target.context, &target.namespace, COPY_TIMEOUT);
                copy.args(["cp", "-c", CONTAINER, "--retries=0"])
                    .arg(local)
                    .arg(format!("{}:{staged}", target.pod));
                checked(
                    capture(copy, None, COPY_TIMEOUT).await?,
                    "stage Spark workload upload",
                )?;
                ensure!(
                    self.remote_hash(&target, &staged).await? == fingerprint.sha256,
                    "staged workload checksum differs: {name}"
                );
                checked(
                    self.exec(
                        &target,
                        &["mv", "-T", "-n", "--", &staged, &final_path],
                        None,
                    )
                    .await?,
                    "commit remote workload",
                )?;
            }
            let actual = self.remote_hash(&target, &final_path).await?;
            ensure!(
                actual == fingerprint.sha256,
                "uploaded workload checksum differs: {name}"
            );
        }
        ensure!(
            self.identity(&target).await? == identity,
            "Spark container changed during upload"
        );
        Ok(remote)
    }

    pub async fn start(
        &self,
        state_path: &Path,
        remote_directory: &str,
        expected: &Identity,
        token: &str,
        arguments: &[String],
        timeout: Duration,
    ) -> Result<Launch> {
        ensure!(
            !token.is_empty() && !token.contains(['\r', '\n']),
            "Pod API key must be a nonempty single line"
        );
        ensure!(
            arguments
                .first()
                .is_some_and(|argument| argument == "spark"),
            "remote command must start with spark"
        );
        ensure!(
            !arguments
                .iter()
                .any(|argument| argument == "--api-key" || argument.starts_with("--api-key=")),
            "provide the Pod API key through stdin only"
        );
        ensure!(
            timeout.as_secs() > 0 && timeout.subsec_nanos() == 0,
            "Pod timeout must be positive whole seconds"
        );
        ensure!(
            !state_path.exists(),
            "Pod launch record already exists; reconcile it before starting again"
        );
        let owner = Uuid::parse_str(
            state_path
                .file_stem()
                .and_then(|name| name.to_str())
                .context("Pod launch filename is not a UUID")?,
        )?;
        let campaign = remote_campaign(remote_directory)?;
        let target = self.target();
        ensure!(
            self.identity(&target).await? == *expected,
            "Spark container changed before launch"
        );
        let launch = Launch {
            protocol_version: 2,
            campaign,
            target,
            directory: remote_directory.to_owned(),
            marker: format!("spark-run-{}", owner.simple()),
            identity: expected.clone(),
            phase: Phase::Pending,
            started: Instant::now(),
        };
        atomic_json(state_path, &launch)?;
        checked(
            self.exec(&launch.target, &["mkdir", "-p", &launch.directory], None)
                .await?,
            "create remote run directory",
        )?;
        let duration = format!("{}s", timeout.as_secs());
        let mut command = vec![
            "bash",
            "-c",
            LAUNCH,
            "--",
            &launch.directory,
            WAIT,
            &launch.marker,
            &launch.directory,
            "timeout",
            "--foreground",
            "--signal=TERM",
            "--kill-after=10s",
            &duration,
        ];
        command.extend(arguments.iter().map(String::as_str));
        let input = format!("{token}\n");
        let acknowledged = self
            .exec(&launch.target, &command, Some(input.as_bytes()))
            .await;
        match acknowledged {
            Ok(output) if output.status.success() => {}
            Ok(output) => {
                writeln!(
                    std::io::stderr().lock(),
                    "Pod launch acknowledgement unavailable; checking the existing run: {}",
                    String::from_utf8_lossy(&output.stderr)
                        .replace(token, "[REDACTED]")
                        .trim()
                )?;
            }
            Err(error) => {
                writeln!(
                    std::io::stderr().lock(),
                    "Pod launch acknowledgement unavailable; checking the existing run: {}",
                    format!("{error:#}").replace(token, "[REDACTED]")
                )?;
            }
        }
        Ok(launch)
    }

    pub async fn poll(
        &self,
        launch: &mut Launch,
        state_path: &Path,
        log_path: &Path,
    ) -> Result<Option<i32>> {
        match self.poll_once(launch, state_path, log_path).await {
            Err(error) if retryable(&error) => {
                writeln!(
                    std::io::stderr().lock(),
                    "Pod control read failed; detached Spark remains independent: {error:#}"
                )?;
                Ok(None)
            }
            outcome => outcome,
        }
    }

    async fn poll_once(
        &self,
        launch: &mut Launch,
        state_path: &Path,
        log_path: &Path,
    ) -> Result<Option<i32>> {
        validate_launch(launch, state_path)?;
        match launch.phase {
            Phase::Exited { code } => return Ok(Some(i32::from(code))),
            Phase::Pending => {}
            Phase::Cancelled | Phase::CancelUnconfirmed => bail!("Pod launch is not pollable"),
        }
        ensure!(
            self.identity(&launch.target).await? == launch.identity,
            "Spark container changed during the run"
        );
        let mut code = self.exit_code(launch).await?;
        if code.is_none()
            && launch.started.elapsed() >= Duration::from_secs(30)
            && self.owned_pid(launch).await?.is_none()
        {
            code = self.exit_code(launch).await?;
            ensure!(
                code.is_some(),
                "detached Spark exited without an exit-status file"
            );
        }
        self.append_log(launch, log_path).await?;
        if let Some(code) = code {
            let pid = self
                .published_pid(launch)
                .await?
                .context("completed Spark has no published process ID")?;
            if self.group_running(&launch.target, pid).await? {
                return Ok(None);
            }
            ensure!(
                self.identity(&launch.target).await? == launch.identity,
                "Spark container changed during completion verification"
            );
            launch.phase = Phase::Exited { code };
            atomic_json(state_path, launch)?;
        }
        Ok(code.map(i32::from))
    }

    async fn append_log(&self, launch: &Launch, path: &Path) -> Result<()> {
        let offset = match fs::metadata(path) {
            Ok(metadata) => metadata.len(),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => 0,
            Err(error) => return Err(error).context("read local Spark log length"),
        };
        let start = format!(
            "+{}",
            offset.checked_add(1).context("Spark log offset overflow")?
        );
        let remote = format!("{}/spark.log", launch.directory);
        let output = self
            .read_exec(&launch.target, &["tail", "-c", &start, &remote])
            .await?;
        if !output.status.success()
            && String::from_utf8_lossy(&output.stderr).contains("No such file or directory")
        {
            return Ok(());
        }
        let bytes = checked(output, "read Spark log")?;
        fs::create_dir_all(path.parent().context("Spark log has no parent")?)?;
        OpenOptions::new()
            .create(true)
            .append(true)
            .open(path)?
            .write_all(&bytes)
            .context("append Spark log")
    }

    async fn read_file(&self, target: &Target, path: &str) -> Result<Option<Vec<u8>>> {
        let output = self.read_exec(target, &["cat", path]).await?;
        if !output.status.success()
            && String::from_utf8_lossy(&output.stderr).contains("No such file or directory")
        {
            return Ok(None);
        }
        Ok(Some(checked(output, "read remote Spark state")?))
    }

    async fn published_pid(&self, launch: &Launch) -> Result<Option<u32>> {
        self.read_file(&launch.target, &format!("{}/spark.pid", launch.directory))
            .await?
            .map(|bytes| parse_pid(&bytes))
            .transpose()
    }

    async fn owned_pid(&self, launch: &Launch) -> Result<Option<u32>> {
        let Some(pid) = self.published_pid(launch).await? else {
            return Ok(None);
        };
        let Some(command) = self
            .read_file(&launch.target, &format!("/proc/{pid}/cmdline"))
            .await?
        else {
            return Ok(None);
        };
        if !command
            .split(|byte| *byte == 0)
            .any(|argument| argument == launch.marker.as_bytes())
        {
            return Ok(None);
        }
        let Some(stat) = self
            .read_file(&launch.target, &format!("/proc/{pid}/stat"))
            .await?
        else {
            return Ok(None);
        };
        let (state, group) = process_stat(&stat)?;
        if matches!(state, "Z" | "X") {
            return Ok(None);
        }
        ensure!(group == pid, "detached Spark process group is not ready");
        Ok(Some(pid))
    }

    async fn exit_code(&self, launch: &Launch) -> Result<Option<u8>> {
        self.read_file(&launch.target, &format!("{}/spark.exit", launch.directory))
            .await?
            .map(|bytes| {
                let code: u8 = std::str::from_utf8(&bytes)
                    .context("Spark exit status is not UTF-8")?
                    .trim()
                    .parse()
                    .context("invalid Spark exit status")?;
                Ok(code)
            })
            .transpose()
    }

    async fn group_running(&self, target: &Target, pid: u32) -> Result<bool> {
        let output = checked(
            self.read_exec(target, &["bash", "-c", GROUP_STATS]).await?,
            "inspect Spark process group",
        )?;
        group_running(&output, pid)
    }

    async fn remote_hash(&self, target: &Target, path: &str) -> Result<String> {
        let output = checked(
            self.read_exec(target, &["sha256sum", path]).await?,
            "hash remote Spark artifact",
        )?;
        let text = std::str::from_utf8(&output).context("remote checksum is not UTF-8")?;
        let hash = text
            .split_whitespace()
            .next()
            .context("remote checksum is empty")?;
        ensure!(
            hash.len() == 64 && hash.bytes().all(|byte| byte.is_ascii_hexdigit()),
            "invalid remote SHA-256"
        );
        Ok(hash.to_owned())
    }

    pub async fn download(&self, launch: &Launch, local_report: &Path) -> Result<()> {
        ensure!(
            launch.phase == Phase::Exited { code: 0 },
            "Spark report is not complete"
        );
        ensure!(
            self.identity(&launch.target).await? == launch.identity,
            "Spark container changed before report download"
        );
        let remote = format!("{}/spark.json", launch.directory);
        let expected = self.remote_hash(&launch.target, &remote).await?;
        let parent = local_report.parent().context("report has no parent")?;
        fs::create_dir_all(parent)?;
        let temporary = tempfile::NamedTempFile::new_in(parent)?;
        let mut copy = self.topology.kubectl(
            &launch.target.context,
            &launch.target.namespace,
            COPY_TIMEOUT,
        );
        copy.args(["cp", "-c", CONTAINER, "--retries=0"])
            .arg(format!("{}:{remote}", launch.target.pod))
            .arg(temporary.path());
        checked(
            capture(copy, None, COPY_TIMEOUT).await?,
            "download Spark report",
        )?;
        ensure!(
            hash_file(temporary.path())? == expected
                && self.remote_hash(&launch.target, &remote).await? == expected,
            "downloaded Spark report checksum differs"
        );
        ensure!(
            self.identity(&launch.target).await? == launch.identity,
            "Spark container changed during report download"
        );
        temporary.as_file().sync_all()?;
        temporary
            .persist(local_report)
            .context("commit downloaded Spark report")?;
        Ok(())
    }

    async fn cancel(&self, launch: &mut Launch, path: &Path) -> Result<()> {
        validate_launch(launch, path)?;
        if matches!(launch.phase, Phase::Exited { .. } | Phase::Cancelled) {
            return Ok(());
        }
        let mut failure = String::from("cancellation has not completed");
        for _ in 0..3 {
            match self.cancel_once(launch, path).await {
                Ok(true) => return Ok(()),
                Ok(false) => {
                    failure =
                        "owned Spark process group is still present or its owner cannot be proved"
                            .to_owned()
                }
                Err(error) => failure = format!("{error:#}"),
            }
            sleep(Duration::from_secs(1)).await;
        }
        launch.phase = Phase::CancelUnconfirmed;
        atomic_json(path, launch)?;
        bail!(
            "cannot confirm detached Spark stopped; reconcile before starting more traffic: {failure}"
        )
    }

    async fn cancel_once(&self, launch: &mut Launch, path: &Path) -> Result<bool> {
        let current = self.identity(&launch.target).await?;
        checked(
            self.exec(
                &launch.target,
                &["bash", "-c", CANCEL, "--", &launch.directory],
                None,
            )
            .await?,
            "fence delayed Spark launch",
        )?;
        let owned = self.owned_pid(launch).await?;
        if let Some(pid) = owned {
            if current != launch.identity {
                // One launch was issued. Seeing its unique marker establishes
                // which replacement container actually received that launch.
                launch.identity = current.clone();
                atomic_json(path, launch)?;
            }
            checked(
                self.exec(
                    &launch.target,
                    &["bash", "-c", STOP, "--", &pid.to_string(), &launch.marker],
                    None,
                )
                .await?,
                "stop owned Spark process group",
            )?;
        } else if current != launch.identity {
            return Ok(false);
        }
        if let Some(pid) = self.published_pid(launch).await?
            && self.group_running(&launch.target, pid).await?
        {
            return Ok(false);
        }
        if self.identity(&launch.target).await? != current {
            return Ok(false);
        }
        launch.phase = Phase::Cancelled;
        atomic_json(path, launch)?;
        Ok(true)
    }

    pub async fn reconcile(&self, output: &Path, campaign_id: &str) -> Result<()> {
        Uuid::parse_str(campaign_id).context("invalid campaign ID")?;
        let attempts = below(output, Path::new("attempts"))?;
        if !attempts.exists() {
            return Ok(());
        }
        let mut records = Vec::new();
        for entry in fs::read_dir(attempts)? {
            let entry = entry?;
            ensure!(
                entry.file_type()?.is_dir(),
                "unexpected Pod attempt artifact"
            );
            Uuid::parse_str(
                entry
                    .file_name()
                    .to_str()
                    .context("attempt ID is not UTF-8")?,
            )
            .context("invalid attempt ID")?;
            let relative = Path::new("attempts")
                .join(entry.file_name())
                .join("pod-launches");
            let directory = below(output, &relative)?;
            if !directory.exists() {
                continue;
            }
            for state in fs::read_dir(directory)? {
                let state = state?;
                if state
                    .path()
                    .extension()
                    .is_some_and(|extension| extension == "json")
                {
                    ensure!(
                        state.file_type()?.is_file(),
                        "Pod launch record is not a regular file"
                    );
                    records.push(state.path());
                }
            }
        }
        records.sort();
        let mut failures = Vec::new();
        for path in records {
            let result = async {
                let mut launch: Launch = read_json(&path)?;
                ensure!(
                    launch.campaign == campaign_id,
                    "Pod launch belongs to another campaign"
                );
                self.cancel(&mut launch, &path).await
            }
            .await;
            if let Err(error) = result {
                failures.push(format!("{}: {error:#}", path.display()));
            }
        }
        ensure!(
            failures.is_empty(),
            "Pod cleanup remains unconfirmed:\n{}",
            failures.join("\n")
        );
        Ok(())
    }
}

fn checked(output: Output, operation: &str) -> Result<Vec<u8>> {
    ensure!(
        output.status.success(),
        "{operation}: {}",
        String::from_utf8_lossy(&output.stderr).trim()
    );
    Ok(output.stdout)
}

fn retryable(error: &anyhow::Error) -> bool {
    if error.is::<tokio::time::error::Elapsed>() {
        return true;
    }
    let message = format!("{error:#}");
    !authentication_error(&message) && transient_control_error(&message)
}

fn validate_name(name: &str) -> Result<()> {
    ensure!(
        !name.is_empty()
            && name.len() <= 253
            && name.split('.').all(|label| {
                !label.is_empty()
                    && label.len() <= 63
                    && label
                        .as_bytes()
                        .first()
                        .is_some_and(u8::is_ascii_alphanumeric)
                    && label
                        .as_bytes()
                        .last()
                        .is_some_and(u8::is_ascii_alphanumeric)
                    && label.bytes().all(|byte| {
                        byte.is_ascii_lowercase() || byte.is_ascii_digit() || byte == b'-'
                    })
            }),
        "invalid Kubernetes Pod or namespace name"
    );
    Ok(())
}

fn selected_container(pod: &Value) -> Result<&Value> {
    pod.pointer("/spec/containers")
        .and_then(Value::as_array)
        .context("Spark Pod containers missing")?
        .iter()
        .find(|container| container.get("name").and_then(Value::as_str) == Some(CONTAINER))
        .context("Pod has no spark container")
}

fn selected_status(pod: &Value) -> Result<&Value> {
    pod.pointer("/status/containerStatuses")
        .and_then(Value::as_array)
        .context("Spark Pod statuses missing")?
        .iter()
        .find(|container| container.get("name").and_then(Value::as_str) == Some(CONTAINER))
        .context("Spark container status missing")
}

fn selected_identity(pod: &Value) -> Result<Identity> {
    selected_container(pod)?;
    let status = selected_status(pod)?;
    ensure!(
        status
            .pointer("/state/running")
            .is_some_and(Value::is_object),
        "Spark container is not running"
    );
    Ok(Identity {
        uid: pod
            .pointer("/metadata/uid")
            .and_then(Value::as_str)
            .filter(|value| !value.is_empty())
            .context("Spark Pod UID missing")?
            .to_owned(),
        container_id: status
            .get("containerID")
            .and_then(Value::as_str)
            .filter(|value| !value.is_empty())
            .context("Spark container ID missing")?
            .to_owned(),
    })
}

fn remote_campaign(directory: &str) -> Result<String> {
    let relative = Path::new(directory)
        .strip_prefix(REMOTE_BASE)
        .context("remote run directory is outside the campaign root")?;
    let parts: Vec<_> = relative.components().collect();
    ensure!(
        parts.len() == 4
            && parts
                .iter()
                .all(|part| matches!(part, std::path::Component::Normal(_))),
        "invalid remote run directory"
    );
    let campaign = Uuid::parse_str(
        parts[0]
            .as_os_str()
            .to_str()
            .context("remote campaign ID is not UTF-8")?,
    )?;
    ensure!(
        parts[1].as_os_str() == "attempts",
        "remote run directory lacks attempts"
    );
    Uuid::parse_str(
        parts[2]
            .as_os_str()
            .to_str()
            .context("remote attempt ID is not UTF-8")?,
    )?;
    ensure!(
        matches!(
            parts[3].as_os_str().to_str(),
            Some("main" | "warm" | "hot" | "short")
        ),
        "invalid remote stream directory"
    );
    Ok(campaign.to_string())
}

fn validate_launch(launch: &Launch, path: &Path) -> Result<()> {
    ensure!(
        launch.protocol_version == 2,
        "unsupported Pod launch protocol; cleanup remains unconfirmed"
    );
    let owner = Uuid::parse_str(
        path.file_stem()
            .and_then(|name| name.to_str())
            .context("launch filename is not a UUID")?,
    )?;
    ensure!(
        launch.marker == format!("spark-run-{}", owner.simple()),
        "Pod marker differs from its launch record"
    );
    ensure!(
        launch.campaign == remote_campaign(&launch.directory)?,
        "remote Pod directory belongs to another campaign"
    );
    ensure!(
        !launch.target.context.is_empty() && launch.target.container == CONTAINER,
        "invalid saved Pod target"
    );
    validate_name(&launch.target.pod)?;
    validate_name(&launch.target.namespace)?;
    ensure!(
        !launch.identity.uid.is_empty() && !launch.identity.container_id.is_empty(),
        "Pod launch identity is missing"
    );
    Ok(())
}

fn parse_pid(bytes: &[u8]) -> Result<u32> {
    let pid = std::str::from_utf8(bytes)
        .context("Spark PID is not UTF-8")?
        .trim()
        .parse::<u32>()
        .context("invalid Spark PID")?;
    ensure!(
        pid > 1 && pid <= i32::MAX as u32,
        "invalid detached Spark process ID"
    );
    Ok(pid)
}

fn process_stat(stat: &[u8]) -> Result<(&str, u32)> {
    let offset = stat
        .windows(2)
        .rposition(|bytes| bytes == b") ")
        .context("invalid process stat record")?
        + 2;
    let mut fields = std::str::from_utf8(&stat[offset..])
        .context("invalid process stat fields")?
        .split_whitespace();
    let state = fields.next().context("process state missing")?;
    fields.next().context("parent process ID missing")?;
    let group = fields
        .next()
        .context("process group ID missing")?
        .parse()
        .context("invalid process group ID")?;
    Ok((state, group))
}

fn group_running(stats: &[u8], pid: u32) -> Result<bool> {
    ensure!(
        !stats.is_empty() && stats.last() == Some(&0),
        "incomplete process group snapshot"
    );
    for stat in stats[..stats.len() - 1].split(|byte| *byte == 0) {
        let (state, group) = process_stat(stat)?;
        if group == pid && !matches!(state, "Z" | "X") {
            return Ok(true);
        }
    }
    Ok(false)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::process::Process;
    use crate::test_support::FakeCommand;
    use std::path::PathBuf;

    fn pod(identity: &str) -> Value {
        json!({
            "metadata": {"uid": "fixture-pod"},
            "spec": {"containers": [{"name": "spark", "image": "fixture:image"}]},
            "status": {"containerStatuses": [
                {"name": "sidecar", "containerID": "sidecar-a", "state": {"running": {}}},
                {"name": "spark", "containerID": identity, "imageID": "digest", "state": {"running": {}}}
            ]}
        })
    }

    fn fixture() -> Result<(FakeCommand, Topology, PathBuf)> {
        let fake = FakeCommand::new();
        let directory = fake.executable().parent().unwrap().to_owned();
        fs::write(directory.join("pod-fixture"), "")?;
        fs::write(directory.join("pod.json"), pod("container-a").to_string())?;
        std::os::unix::fs::symlink("/bin/sleep", directory.join("spark"))?;
        let topology = Topology::load("us-west-2", &[])?.with_executable(fake.executable());
        Ok((fake, topology, directory))
    }

    fn record(runner: &Runner<'_>, local: &Path) -> (PathBuf, Launch) {
        let campaign = Uuid::new_v4();
        let attempt = Uuid::new_v4();
        let owner = Uuid::new_v4();
        let path = local.join(format!("attempts/{attempt}/pod-launches/{owner}.json"));
        let launch = Launch {
            protocol_version: 2,
            campaign: campaign.to_string(),
            target: runner.target(),
            directory: format!("{REMOTE_BASE}/{campaign}/attempts/{attempt}/main"),
            marker: format!("spark-run-{}", owner.simple()),
            identity: selected_identity(&pod("container-a")).unwrap(),
            phase: Phase::Pending,
            started: Instant::now(),
        };
        (path, launch)
    }

    fn local_remote(root: &Path, launch: &Launch) -> PathBuf {
        root.join("remote").join(
            launch
                .directory
                .strip_prefix(&format!("{REMOTE_BASE}/"))
                .unwrap(),
        )
    }

    async fn wait_for_pid(runner: &Runner<'_>, launch: &Launch) -> Result<u32> {
        tokio::time::timeout(Duration::from_secs(5), async {
            loop {
                if let Some(pid) = runner.owned_pid(launch).await? {
                    return Ok(pid);
                }
                sleep(Duration::from_millis(10)).await;
            }
        })
        .await?
    }

    #[test]
    fn selected_identity_ignores_sidecar_restart_but_tracks_spark() -> Result<()> {
        let mut current = pod("container-a");
        let initial = selected_identity(&current)?;
        current["status"]["containerStatuses"][0]["containerID"] = json!("sidecar-b");
        assert_eq!(selected_identity(&current)?, initial);
        current["status"]["containerStatuses"][1]["containerID"] = json!("container-b");
        assert_ne!(selected_identity(&current)?, initial);
        current["status"]["containerStatuses"][1]["state"] = json!({"terminated": {}});
        assert!(selected_identity(&current).is_err());
        Ok(())
    }

    #[test]
    fn launch_state_cannot_describe_conflicting_or_out_of_range_exit_statuses() -> Result<()> {
        let topology = Topology::load("us-west-2", &[])?;
        let runner = Runner::new(&topology, "spark-test")?;
        let (_, mut launch) = record(&runner, Path::new("unused"));
        for phase in [
            Phase::Pending,
            Phase::Exited { code: 0 },
            Phase::Exited { code: 255 },
            Phase::Cancelled,
            Phase::CancelUnconfirmed,
        ] {
            launch.phase = phase;
            let encoded = serde_json::to_value(&launch)?;
            let decoded: Launch = serde_json::from_value(encoded)?;
            assert_eq!(decoded.phase, phase);
        }
        let mut invalid = serde_json::to_value(&launch)?;
        invalid["exitCode"] = json!(0);
        assert!(serde_json::from_value::<Launch>(invalid).is_err());
        for phase in [
            json!({"exited": {"code": 256}}),
            json!({"exited": {"code": 0, "failed": true}}),
            json!({"exited": {}}),
            json!("complete"),
            json!("running"),
        ] {
            let mut invalid = serde_json::to_value(&launch)?;
            invalid["phase"] = phase;
            assert!(serde_json::from_value::<Launch>(invalid).is_err());
        }
        Ok(())
    }

    #[test]
    fn process_snapshot_rejects_truncation_and_counts_live_descendants() -> Result<()> {
        let records = b"22 (worker ) name) Z 1 22 0\0 23 (child) S 1 22 0\0";
        assert!(group_running(records, 22)?);
        assert!(!group_running(b"22 (worker) Z 1 22 0\0", 22)?);
        assert!(!group_running(b"23 (child) R 1 23 0\0", 22)?);
        assert!(group_running(b"22 (worker) S 1 22 0", 22).is_err());
        assert!(group_running(b"", 22).is_err());
        for pid in ["0", "1", "-1", "2147483648", "2 3"] {
            assert!(parse_pid(pid.as_bytes()).is_err());
        }
        Ok(())
    }

    #[tokio::test]
    async fn cancellation_fences_a_launch_that_has_not_arrived() -> Result<()> {
        let (_fake, topology, root) = fixture()?;
        let runner = Runner::new(&topology, "spark-test")?;
        let (path, mut launch) = record(&runner, &root);
        atomic_json(&path, &launch)?;
        assert!(runner.cancel_once(&mut launch, &path).await?);
        assert_eq!(launch.phase, Phase::Cancelled);
        let remote = local_remote(&root, &launch);
        assert!(remote.join("spark.cancelled").exists());
        // Run the delayed wrapper itself after cancellation has committed.
        let output = runner
            .exec(
                &launch.target,
                &[
                    "bash",
                    "-c",
                    WAIT,
                    &launch.marker,
                    &launch.directory,
                    "bash",
                    "-c",
                    ": > \"$1\"",
                    "--",
                    &format!("{}/workload-started", launch.directory),
                ],
                None,
            )
            .await?;
        assert_eq!(output.status.code(), Some(143));
        assert!(!remote.join("workload-started").exists());
        assert!(!remote.join("spark.pid").exists());
        Ok(())
    }

    #[tokio::test]
    async fn cancellation_stops_marker_proven_replacement_group() -> Result<()> {
        let (_fake, topology, root) = fixture()?;
        let runner = Runner::new(&topology, "spark-test")?;
        let (path, pending) = record(&runner, &root);
        let mut launch = runner
            .start(
                &path,
                &pending.directory,
                &pending.identity,
                "secret",
                &["spark".to_owned(), "30".to_owned()],
                Duration::from_secs(35),
            )
            .await?;
        let pid = wait_for_pid(&runner, &launch).await?;
        fs::write(root.join("pod.json"), pod("container-b").to_string())?;
        runner.cancel(&mut launch, &path).await?;
        assert_eq!(launch.phase, Phase::Cancelled);
        assert_eq!(launch.identity.container_id, "container-b");
        assert!(!runner.group_running(&launch.target, pid).await?);
        let saved: Launch = read_json(&path)?;
        assert_eq!(saved.phase, Phase::Cancelled);
        assert_eq!(saved.identity, launch.identity);
        assert!(!fs::read_to_string(&path)?.contains("secret"));
        Ok(())
    }

    #[tokio::test]
    async fn replacement_without_marker_never_uses_a_stale_pid_as_ownership() -> Result<()> {
        let (_fake, topology, root) = fixture()?;
        let runner = Runner::new(&topology, "spark-test")?;
        let (path, mut launch) = record(&runner, &root);
        // Publish this unrelated leader's PID through a directly owned test child.
        let mut probe = Command::new("bash");
        probe.args(["-c", "printf '%s\\n' \"$$\" > \"$1\"; exec sleep 30", "--"]);
        let remote = local_remote(&root, &launch);
        fs::create_dir_all(&remote)?;
        probe.arg(remote.join("spark.pid"));
        let mut leader = Process::spawn(probe)?;
        tokio::time::timeout(Duration::from_secs(5), async {
            while !remote.join("spark.pid").exists() {
                sleep(Duration::from_millis(10)).await;
            }
        })
        .await?;
        atomic_json(&path, &launch)?;
        fs::write(root.join("pod.json"), pod("container-b").to_string())?;
        assert!(!runner.cancel_once(&mut launch, &path).await?);
        assert!(leader.try_wait()?.is_none());
        leader.stop().await?;
        // Even group absence in a replacement cannot prove the original stopped.
        assert!(!runner.cancel_once(&mut launch, &path).await?);
        assert_eq!(launch.identity.container_id, "container-a");
        Ok(())
    }

    #[tokio::test]
    async fn wrapper_death_does_not_confirm_live_descendants_stopped() -> Result<()> {
        let (_fake, topology, root) = fixture()?;
        let runner = Runner::new(&topology, "spark-test")?;
        let (path, mut launch) = record(&runner, &root);
        let remote = local_remote(&root, &launch);
        fs::create_dir_all(&remote)?;
        let mut command = Command::new("bash");
        let child_ready = remote.join("child.ready");
        command
            .args(["-c", WAIT, &launch.marker])
            .arg(&remote)
            .args(["bash", "-c", ": > \"$1\"; exec sleep 30", "--"])
            .arg(&child_ready);
        let mut wrapper = Process::spawn(command)?;
        let pid = wait_for_pid(&runner, &launch).await?;
        tokio::time::timeout(Duration::from_secs(5), async {
            while !child_ready.exists() {
                sleep(Duration::from_millis(10)).await;
            }
        })
        .await?;
        // Keep the leader unreaped so its PID remains ours until cleanup.
        rustix::process::kill_process(
            rustix::process::Pid::from_raw(i32::try_from(pid)?).unwrap(),
            rustix::process::Signal::KILL,
        )?;
        tokio::time::timeout(Duration::from_secs(5), async {
            while runner.owned_pid(&launch).await?.is_some() {
                sleep(Duration::from_millis(10)).await;
            }
            Result::<()>::Ok(())
        })
        .await??;
        let result = runner.cancel_once(&mut launch, &path).await;
        wrapper.stop().await?;
        assert!(!result?);
        assert_ne!(launch.phase, Phase::Cancelled);
        assert!(!runner.group_running(&launch.target, pid).await?);
        Ok(())
    }

    #[tokio::test]
    async fn upload_recovers_partial_staging_but_preserves_committed_inputs() -> Result<()> {
        let (_fake, topology, root) = fixture()?;
        let runner = Runner::new(&topology, "spark-test")?;
        let output = tempfile::tempdir()?;
        fs::create_dir(output.path().join("workloads"))?;
        let local = output.path().join("workloads/input.yaml");
        fs::write(&local, "tasks: []\n")?;
        let fingerprints = BTreeMap::from([(
            "input.yaml".to_owned(),
            Fingerprint {
                prompt_count: 1,
                sha256: hash_file(&local)?,
            },
        )]);
        let campaign = Uuid::new_v4().to_string();
        let remote = root.join("remote").join(&campaign).join("workloads");
        fs::create_dir_all(&remote)?;
        fs::write(remote.join(".upload-interrupted"), "partial")?;
        runner
            .prepare(&campaign, output.path(), &fingerprints)
            .await?;
        assert_eq!(
            hash_file(&remote.join("input.yaml"))?,
            fingerprints["input.yaml"].sha256
        );
        assert!(remote.join(".upload-interrupted").exists());
        fs::write(remote.join("input.yaml"), "changed")?;
        let error = runner
            .prepare(&campaign, output.path(), &fingerprints)
            .await
            .unwrap_err();
        assert!(error.to_string().contains("checksum differs"));
        assert_eq!(fs::read_to_string(remote.join("input.yaml"))?, "changed");
        Ok(())
    }

    #[tokio::test]
    async fn group_snapshot_is_complete_during_process_churn() -> Result<()> {
        let mut churn_command = Command::new("bash");
        churn_command.args(["-c", "for ((i=0;i<300;i++)); do /bin/true & wait $!; done"]);
        let mut churn = Process::spawn(churn_command)?;
        let mut scan = Command::new("bash");
        scan.args(["-c", GROUP_STATS]);
        let output = capture(scan, None, Duration::from_secs(10)).await?;
        let bytes = checked(output, "scan local test process groups")?;
        assert!(group_running(&bytes, u32::MAX).is_ok());
        churn.stop().await?;
        Ok(())
    }

    #[tokio::test]
    async fn invalid_token_is_rejected_before_effects() -> Result<()> {
        let (fake, topology, root) = fixture()?;
        let runner = Runner::new(&topology, "spark-test")?;
        let (path, launch) = record(&runner, &root);
        for token in ["", "secret\nnext", "secret\rnext"] {
            let error = runner
                .start(
                    &path,
                    &launch.directory,
                    &launch.identity,
                    token,
                    &["spark".to_owned()],
                    Duration::from_secs(30),
                )
                .await
                .unwrap_err();
            assert!(!error.to_string().contains("secret"));
            assert!(!path.exists());
        }
        assert!(fake.calls().is_empty());
        Ok(())
    }
}

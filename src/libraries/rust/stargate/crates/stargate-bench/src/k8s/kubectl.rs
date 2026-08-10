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

#[cfg(test)]
use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::thread::sleep;
use std::time::{Duration, Instant};

use anyhow::{Context, bail};

use super::render::{StargatePod, render_stargate_external_services};
use super::run::BenchmarkK8sRun;

#[derive(Clone, Debug)]
pub(crate) struct Kubectl {
    program: PathBuf,
    #[cfg(test)]
    base_args: Vec<OsString>,
}

impl Default for Kubectl {
    fn default() -> Self {
        Self::new("kubectl")
    }
}

impl Kubectl {
    pub(super) fn new(program: impl Into<PathBuf>) -> Self {
        Self {
            program: program.into(),
            #[cfg(test)]
            base_args: Vec::new(),
        }
    }

    #[cfg(test)]
    pub(super) fn with_args<I, A>(program: impl Into<PathBuf>, args: I) -> Self
    where
        I: IntoIterator<Item = A>,
        A: Into<OsString>,
    {
        Self {
            program: program.into(),
            base_args: args.into_iter().map(Into::into).collect(),
        }
    }

    fn command(&self) -> Command {
        let command = Command::new(&self.program);
        #[cfg(test)]
        let command = {
            let mut command = command;
            command.args(&self.base_args);
            command
        };
        command
    }

    fn command_with_args(&self, args: &[&str]) -> Command {
        let mut command = self.command();
        command.args(args);
        command
    }

    fn run_status(
        mut command: Command,
        start_error: impl FnOnce() -> String,
        failure: impl FnOnce() -> String,
    ) -> anyhow::Result<()> {
        let status = command.status().with_context(start_error)?;
        if !status.success() {
            bail!(failure());
        }
        Ok(())
    }

    pub(crate) fn apply(&self, run: &BenchmarkK8sRun) -> anyhow::Result<()> {
        self.wait_for_namespace_reuse(&run.stargate_ns, Duration::from_secs(60))?;
        self.wait_for_namespace_reuse(&run.backends_ns, Duration::from_secs(60))?;
        self.kubectl_file_action(
            "apply",
            &run.run_dir.join("k8s-stargate-manifest.yaml"),
            &[],
            || format!("stargate resources for {}", run.algorithm_name),
        )
    }

    pub(crate) fn delete(&self, run: &BenchmarkK8sRun) -> anyhow::Result<()> {
        for path in [
            run.run_dir.join("k8s-backends-manifest.yaml"),
            run.run_dir.join("stargate-external-services.yaml"),
            run.run_dir.join("k8s-stargate-manifest.yaml"),
        ] {
            if path.exists() {
                self.kubectl_file_action("delete", &path, &["--ignore-not-found=true"], || {
                    format!("k8s benchmark resources for {}", run.algorithm_name)
                })?;
            }
        }
        Ok(())
    }

    pub(crate) fn collect_logs(&self, run: &BenchmarkK8sRun) -> anyhow::Result<()> {
        let logs_dir = run.run_dir.join("logs");
        fs::create_dir_all(&logs_dir)
            .with_context(|| format!("failed to create logs dir {}", logs_dir.display()))?;

        self.collect_namespace_snapshot(&logs_dir, "stargate", &run.stargate_ns)?;
        self.collect_namespace_snapshot(&logs_dir, "backends", &run.backends_ns)?;
        self.collect_labeled_logs(&logs_dir, "stargate", &run.stargate_ns, "app=stargate")?;
        for backend_index in 0.. {
            let inference_server = format!("backend-{backend_index}-inference-server");
            let pylon = format!("backend-{backend_index}-pylon");
            let inference = self.collect_labeled_logs(
                &logs_dir,
                &inference_server,
                &run.backends_ns,
                &format!("app={inference_server}"),
            )?;
            let client = self.collect_labeled_logs(
                &logs_dir,
                &pylon,
                &run.backends_ns,
                &format!("app={pylon}"),
            )?;
            if !inference && !client {
                break;
            }
        }
        Ok(())
    }

    pub(crate) fn delete_backend_pod(
        &self,
        run: &BenchmarkK8sRun,
        backend_index: usize,
    ) -> anyhow::Result<()> {
        let upstream_index = backend_upstream_index(run, backend_index)?;
        let selector = format!("app=backend-{upstream_index}-inference-server");
        Self::run_status(
            self.command_with_args(&["-n", &run.backends_ns, "delete", "pod", "-l", &selector]),
            || format!("failed to delete backend pod for backend-{backend_index}"),
            || format!("kubectl delete pod failed for selector {selector}"),
        )
    }

    pub(crate) fn scale_backend(
        &self,
        run: &BenchmarkK8sRun,
        backend_index: usize,
        replicas: u32,
    ) -> anyhow::Result<()> {
        let upstream_index = backend_upstream_index(run, backend_index)?;
        let deployment = format!("deployment/backend-{upstream_index}-inference-server");
        let replicas = format!("--replicas={replicas}");
        Self::run_status(
            self.command_with_args(&["-n", &run.backends_ns, "scale", &deployment, &replicas]),
            || format!("failed to scale {deployment}"),
            || format!("kubectl scale failed for {deployment}"),
        )
    }

    fn kubectl_file_action(
        &self,
        action: &str,
        path: &Path,
        extra_args: &[&str],
        description: impl FnOnce() -> String,
    ) -> anyhow::Result<()> {
        let mut command = self.command();
        command.arg(action).arg("-f").arg(path).args(extra_args);
        Self::run_status(
            command,
            || format!("failed to run kubectl {action}"),
            || format!("kubectl {action} failed for {}", description()),
        )
    }

    fn collect_namespace_snapshot(
        &self,
        logs_dir: &Path,
        name: &str,
        namespace: &str,
    ) -> anyhow::Result<()> {
        for (suffix, args) in [
            ("pods", &["get", "pods", "-o", "wide"][..]),
            ("describe-pods", &["describe", "pods"]),
            ("events", &["get", "events", "--sort-by=.lastTimestamp"]),
        ] {
            write_kubectl_output(
                logs_dir.join(format!("{name}-{suffix}.txt")),
                self.command_with_args(&["-n", namespace])
                    .args(args)
                    .output(),
            )?;
        }
        Ok(())
    }

    fn collect_labeled_logs(
        &self,
        logs_dir: &Path,
        name: &str,
        namespace: &str,
        selector: &str,
    ) -> anyhow::Result<bool> {
        let pods = self.pods_for_selector(namespace, selector)?;
        if pods.is_empty() {
            return Ok(false);
        }
        write_kubectl_output(
            logs_dir.join(format!("{name}.log")),
            self.command_with_args(&["-n", namespace, "logs", "-l", selector])
                .args(["--all-containers=true", "--prefix=true", "--tail=-1"])
                .output(),
        )?;
        Ok(true)
    }

    fn pods_for_selector(&self, namespace: &str, selector: &str) -> anyhow::Result<Vec<String>> {
        let output = self
            .command_with_args(&["-n", namespace, "get", "pods", "-l", selector, "-o"])
            .arg("jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
            .output()
            .with_context(|| format!("failed to query pods for selector {selector}"))?;
        if !output.status.success() {
            return Ok(Vec::new());
        }
        Ok(kubectl_output_lines(&output.stdout))
    }

    pub(crate) fn wait_ready(
        &self,
        run: &BenchmarkK8sRun,
        backend_count: usize,
    ) -> anyhow::Result<()> {
        self.rollout("statefulset", "stargate", &run.stargate_ns)?;
        self.apply_stargate_external_services(run)?;
        self.kubectl_file_action(
            "apply",
            &run.run_dir.join("k8s-backends-manifest.yaml"),
            &[],
            || format!("backend resources for {}", run.algorithm_name),
        )?;
        for backend_index in &run.upstream_backend_indices {
            self.rollout(
                "deployment",
                &format!("backend-{backend_index}-inference-server"),
                &run.backends_ns,
            )?;
        }
        for backend_index in 0..backend_count {
            self.rollout(
                "deployment",
                &format!("backend-{backend_index}-pylon"),
                &run.backends_ns,
            )?;
        }
        Ok(())
    }

    pub(crate) fn stargate_metrics_endpoints(
        &self,
        run: &BenchmarkK8sRun,
    ) -> anyhow::Result<Vec<String>> {
        let output = self
            .command_with_args(&[
                "-n",
                &run.stargate_ns,
                "get",
                "services",
                "-l",
                "benchmark.stargate/role=pod-metrics",
                "-o",
            ])
            .arg("jsonpath={range .items[*]}{.metadata.name}{\" \"}{.spec.ports[0].nodePort}{\"\\n\"}{end}")
            .output()
            .context("failed to query per-stargate metrics services")?;
        if !output.status.success() {
            bail!("kubectl get services failed while querying per-stargate metrics endpoints");
        }

        let mut endpoints = String::from_utf8_lossy(&output.stdout)
            .lines()
            .map(str::trim)
            .filter(|line| !line.is_empty())
            .map(|line| {
                let mut fields = line.split_whitespace();
                let name = fields
                    .next()
                    .context("missing per-stargate metrics service name")?;
                let node_port = fields
                    .next()
                    .context("missing per-stargate metrics NodePort")?;
                if fields.next().is_some() {
                    bail!("unexpected per-stargate metrics service record: {line}");
                }
                Ok((
                    name.to_string(),
                    format!("http://{}:{node_port}/metrics", run.nodeport_host),
                ))
            })
            .collect::<anyhow::Result<Vec<_>>>()?;
        endpoints.sort_by(|left, right| left.0.cmp(&right.0));

        if endpoints.len() != run.stargate_count {
            bail!(
                "expected {} per-stargate metrics endpoints but found {}",
                run.stargate_count,
                endpoints.len()
            );
        }

        Ok(endpoints
            .into_iter()
            .map(|(_, endpoint)| endpoint)
            .collect())
    }

    fn apply_stargate_external_services(&self, run: &BenchmarkK8sRun) -> anyhow::Result<()> {
        let pods = self.list_stargate_pods(run)?;
        if pods.len() != run.stargate_count {
            bail!(
                "expected {} stargate pods but found {}",
                run.stargate_count,
                pods.len()
            );
        }

        let manifest = render_stargate_external_services(&run.stargate_ns, &pods);

        let manifest_path = run.run_dir.join("stargate-external-services.yaml");
        fs::write(&manifest_path, manifest)
            .with_context(|| format!("failed to write {}", manifest_path.display()))?;

        self.apply_external_services_manifest(&manifest_path)
    }

    fn apply_external_services_manifest(&self, manifest_path: &Path) -> anyhow::Result<()> {
        let mut command = self.command();
        command.arg("apply").arg("-f").arg(manifest_path);
        Self::run_status(
            command,
            || "failed to apply stargate external services".to_string(),
            || "kubectl apply failed for stargate external services".to_string(),
        )
    }

    fn list_stargate_pods(&self, run: &BenchmarkK8sRun) -> anyhow::Result<Vec<StargatePod>> {
        let output = self
            .command_with_args(&[
                "-n",
                &run.stargate_ns,
                "get",
                "pods",
                "-l",
                "app=stargate",
                "-o",
            ])
            .arg("jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
            .output()
            .context("failed to query stargate pods")?;
        if !output.status.success() {
            bail!("kubectl get pods failed while querying stargate pods");
        }

        Ok(kubectl_output_lines(&output.stdout)
            .into_iter()
            .map(|line| StargatePod { name: line })
            .collect())
    }

    fn rollout(&self, kind: &str, name: &str, namespace: &str) -> anyhow::Result<()> {
        let resource = format!("{kind}/{name}");
        Self::run_status(
            self.command_with_args(&[
                "-n",
                namespace,
                "rollout",
                "status",
                &resource,
                "--timeout=180s",
            ]),
            || format!("failed waiting for rollout of {kind}/{name}"),
            || format!("rollout failed for {kind}/{name} in namespace {namespace}"),
        )
    }

    fn wait_for_namespace_reuse(&self, namespace: &str, timeout: Duration) -> anyhow::Result<()> {
        let deadline = Instant::now() + timeout;
        loop {
            let output = self
                .command_with_args(&["get", "namespace", namespace, "-o"])
                .arg("jsonpath={.status.phase}")
                .output()
                .with_context(|| format!("failed to query namespace {namespace}"))?;
            if !output.status.success() {
                return Ok(());
            }

            let phase = String::from_utf8_lossy(&output.stdout).trim().to_string();
            if phase != "Terminating" {
                return Ok(());
            }

            if Instant::now() >= deadline {
                bail!("timed out waiting for namespace {namespace} to finish terminating");
            }
            sleep(Duration::from_millis(500));
        }
    }

    pub(super) fn resolve_nodeport_host(&self) -> anyhow::Result<String> {
        self.resolve_nodeport_host_with_override(std::env::var("STARGATE_BENCH_NODE_HOST").ok())
    }

    pub(super) fn resolve_nodeport_host_with_override(
        &self,
        host_override: Option<String>,
    ) -> anyhow::Result<String> {
        if let Some(host) = host_override
            .as_deref()
            .map(str::trim)
            .filter(|host| !host.is_empty())
        {
            return Ok(host.to_string());
        }

        let context = self
            .command_with_args(&["config", "current-context"])
            .output()
            .context("failed to query current kubectl context")?;
        if context.status.success()
            && String::from_utf8_lossy(&context.stdout).trim() == "docker-desktop"
        {
            return Ok("127.0.0.1".to_string());
        }

        for address_type in ["ExternalIP", "InternalIP"] {
            if let Some(address) = self.query_first_node_address(address_type)? {
                return Ok(address);
            }
        }
        bail!(
            "failed to resolve Kubernetes node address for NodePort access; set STARGATE_BENCH_NODE_HOST"
        )
    }

    fn query_first_node_address(&self, address_type: &str) -> anyhow::Result<Option<String>> {
        let jsonpath = format!(
            "jsonpath={{.items[0].status.addresses[?(@.type==\"{address_type}\")].address}}"
        );
        let output = self
            .command_with_args(&["get", "nodes", "-o", &jsonpath])
            .output()
            .with_context(|| format!("failed to query Kubernetes node {address_type} address"))?;
        if !output.status.success() {
            bail!("kubectl get nodes failed while resolving NodePort host");
        }
        let address = String::from_utf8_lossy(&output.stdout).trim().to_string();
        Ok((!address.is_empty()).then_some(address))
    }
}

fn backend_upstream_index(run: &BenchmarkK8sRun, backend_index: usize) -> anyhow::Result<usize> {
    run.backend_upstream_indices
        .get(backend_index)
        .copied()
        .with_context(|| format!("unknown benchmark backend index {backend_index}"))
}

fn kubectl_output_lines(stdout: &[u8]) -> Vec<String> {
    String::from_utf8_lossy(stdout)
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .map(str::to_string)
        .collect()
}

fn write_kubectl_output(
    path: impl AsRef<Path>,
    output: std::io::Result<Output>,
) -> anyhow::Result<()> {
    let path = path.as_ref();
    let output = output.with_context(|| format!("failed to run kubectl for {}", path.display()))?;
    let mut text = String::new();
    text.push_str(&format!("status: {}\n\n", output.status));
    if !output.stdout.is_empty() {
        text.push_str("stdout:\n");
        text.push_str(&String::from_utf8_lossy(&output.stdout));
        text.push('\n');
    }
    if !output.stderr.is_empty() {
        text.push_str("stderr:\n");
        text.push_str(&String::from_utf8_lossy(&output.stderr));
        text.push('\n');
    }
    fs::write(path, text).with_context(|| format!("failed to write {}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn external_service_apply_spawn_failure_preserves_specific_context() {
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let error = Kubectl::new(tempdir.path().join("missing-kubectl"))
            .apply_external_services_manifest(&tempdir.path().join("services.yaml"))
            .unwrap_err();

        assert_eq!(
            error.to_string(),
            "failed to apply stargate external services"
        );
    }
}

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

use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::thread::sleep;
use std::time::{Duration, Instant};

use anyhow::{Context, bail};

use super::render::{StargatePod, render_stargate_external_services};
use super::run::BenchmarkK8sRun;

pub fn apply(run: &BenchmarkK8sRun) -> anyhow::Result<()> {
    Kubectl::default().apply(run)
}

pub fn delete(run: &BenchmarkK8sRun) -> anyhow::Result<()> {
    Kubectl::default().delete(run)
}

pub fn collect_logs(run: &BenchmarkK8sRun) -> anyhow::Result<()> {
    Kubectl::default().collect_logs(run)
}

pub fn delete_backend_pod(run: &BenchmarkK8sRun, backend_index: usize) -> anyhow::Result<()> {
    Kubectl::default().delete_backend_pod(run, backend_index)
}

pub fn scale_backend(
    run: &BenchmarkK8sRun,
    backend_index: usize,
    replicas: u32,
) -> anyhow::Result<()> {
    Kubectl::default().scale_backend(run, backend_index, replicas)
}

pub fn wait_ready(run: &BenchmarkK8sRun, backend_count: usize) -> anyhow::Result<()> {
    Kubectl::default().wait_ready(run, backend_count)
}

pub fn stargate_metrics_endpoints(run: &BenchmarkK8sRun) -> anyhow::Result<Vec<String>> {
    Kubectl::default().stargate_metrics_endpoints(run)
}

pub(super) fn resolve_nodeport_host() -> anyhow::Result<String> {
    Kubectl::default().resolve_nodeport_host()
}

#[derive(Clone, Debug)]
pub(super) struct Kubectl {
    program: PathBuf,
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
        }
    }

    fn command(&self) -> Command {
        Command::new(&self.program)
    }

    pub(super) fn apply(&self, run: &BenchmarkK8sRun) -> anyhow::Result<()> {
        self.wait_for_namespace_reuse(&run.stargate_ns, Duration::from_secs(60))?;
        self.wait_for_namespace_reuse(&run.backends_ns, Duration::from_secs(60))?;
        self.kubectl_apply(&run.run_dir.join("k8s-stargate-manifest.yaml"), || {
            format!("stargate resources for {}", run.algorithm_name)
        })
    }

    pub(super) fn delete(&self, run: &BenchmarkK8sRun) -> anyhow::Result<()> {
        for path in [
            run.run_dir.join("k8s-backends-manifest.yaml"),
            run.run_dir.join("stargate-external-services.yaml"),
            run.run_dir.join("k8s-stargate-manifest.yaml"),
        ] {
            self.kubectl_delete(&path, || {
                format!("k8s benchmark resources for {}", run.algorithm_name)
            })?;
        }
        Ok(())
    }

    pub(super) fn collect_logs(&self, run: &BenchmarkK8sRun) -> anyhow::Result<()> {
        let logs_dir = run.run_dir.join("logs");
        fs::create_dir_all(&logs_dir)
            .with_context(|| format!("failed to create logs dir {}", logs_dir.display()))?;

        self.collect_namespace_snapshot(&logs_dir, "stargate", &run.stargate_ns)?;
        self.collect_namespace_snapshot(&logs_dir, "backends", &run.backends_ns)?;
        self.collect_labeled_logs(&logs_dir, "stargate", &run.stargate_ns, "app=stargate")?;
        for backend_index in 0.. {
            let inference_selector = format!("app=backend-{backend_index}-inference-server");
            let client_selector = format!("app=backend-{backend_index}-pylon");
            let inference = self.collect_labeled_logs(
                &logs_dir,
                &format!("backend-{backend_index}-inference-server"),
                &run.backends_ns,
                &inference_selector,
            )?;
            let client = self.collect_labeled_logs(
                &logs_dir,
                &format!("backend-{backend_index}-pylon"),
                &run.backends_ns,
                &client_selector,
            )?;
            if !inference && !client {
                break;
            }
        }
        Ok(())
    }

    pub(super) fn delete_backend_pod(
        &self,
        run: &BenchmarkK8sRun,
        backend_index: usize,
    ) -> anyhow::Result<()> {
        let upstream_index = run
            .backend_upstream_indices
            .get(backend_index)
            .copied()
            .with_context(|| format!("unknown benchmark backend index {backend_index}"))?;
        let selector = format!("app=backend-{upstream_index}-inference-server");
        let status = self
            .command()
            .arg("-n")
            .arg(&run.backends_ns)
            .arg("delete")
            .arg("pod")
            .arg("-l")
            .arg(&selector)
            .status()
            .with_context(|| format!("failed to delete backend pod for backend-{backend_index}"))?;
        if !status.success() {
            bail!("kubectl delete pod failed for selector {selector}");
        }
        Ok(())
    }

    pub(super) fn scale_backend(
        &self,
        run: &BenchmarkK8sRun,
        backend_index: usize,
        replicas: u32,
    ) -> anyhow::Result<()> {
        let upstream_index = run
            .backend_upstream_indices
            .get(backend_index)
            .copied()
            .with_context(|| format!("unknown benchmark backend index {backend_index}"))?;
        let deployment = format!("deployment/backend-{upstream_index}-inference-server");
        let status = self
            .command()
            .arg("-n")
            .arg(&run.backends_ns)
            .arg("scale")
            .arg(&deployment)
            .arg(format!("--replicas={replicas}"))
            .status()
            .with_context(|| format!("failed to scale {deployment}"))?;
        if !status.success() {
            bail!("kubectl scale failed for {deployment}");
        }
        Ok(())
    }

    fn kubectl_apply(
        &self,
        path: &Path,
        description: impl FnOnce() -> String,
    ) -> anyhow::Result<()> {
        let status = self
            .command()
            .arg("apply")
            .arg("-f")
            .arg(path)
            .status()
            .context("failed to run kubectl apply")?;
        if !status.success() {
            bail!("kubectl apply failed for {}", description());
        }
        Ok(())
    }

    fn kubectl_delete(
        &self,
        path: &Path,
        description: impl FnOnce() -> String,
    ) -> anyhow::Result<()> {
        if !path.exists() {
            return Ok(());
        }

        let status = self
            .command()
            .arg("delete")
            .arg("-f")
            .arg(path)
            .arg("--ignore-not-found=true")
            .status()
            .context("failed to run kubectl delete")?;
        if !status.success() {
            bail!("kubectl delete failed for {}", description());
        }
        Ok(())
    }

    fn collect_namespace_snapshot(
        &self,
        logs_dir: &Path,
        name: &str,
        namespace: &str,
    ) -> anyhow::Result<()> {
        write_kubectl_output(
            logs_dir.join(format!("{name}-pods.txt")),
            self.command()
                .arg("-n")
                .arg(namespace)
                .arg("get")
                .arg("pods")
                .arg("-o")
                .arg("wide")
                .output(),
        )?;
        write_kubectl_output(
            logs_dir.join(format!("{name}-describe-pods.txt")),
            self.command()
                .arg("-n")
                .arg(namespace)
                .arg("describe")
                .arg("pods")
                .output(),
        )?;
        write_kubectl_output(
            logs_dir.join(format!("{name}-events.txt")),
            self.command()
                .arg("-n")
                .arg(namespace)
                .arg("get")
                .arg("events")
                .arg("--sort-by=.lastTimestamp")
                .output(),
        )?;
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
            self.command()
                .arg("-n")
                .arg(namespace)
                .arg("logs")
                .arg("-l")
                .arg(selector)
                .arg("--all-containers=true")
                .arg("--prefix=true")
                .arg("--tail=-1")
                .output(),
        )?;
        Ok(true)
    }

    fn pods_for_selector(&self, namespace: &str, selector: &str) -> anyhow::Result<Vec<String>> {
        let output = self
            .command()
            .arg("-n")
            .arg(namespace)
            .arg("get")
            .arg("pods")
            .arg("-l")
            .arg(selector)
            .arg("-o")
            .arg("jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
            .output()
            .with_context(|| format!("failed to query pods for selector {selector}"))?;
        if !output.status.success() {
            return Ok(Vec::new());
        }
        Ok(String::from_utf8_lossy(&output.stdout)
            .lines()
            .map(str::trim)
            .filter(|line| !line.is_empty())
            .map(str::to_string)
            .collect())
    }

    pub(super) fn wait_ready(
        &self,
        run: &BenchmarkK8sRun,
        backend_count: usize,
    ) -> anyhow::Result<()> {
        self.rollout("statefulset", "stargate", &run.stargate_ns)?;
        self.apply_stargate_external_services(run)?;
        self.kubectl_apply(&run.run_dir.join("k8s-backends-manifest.yaml"), || {
            format!("backend resources for {}", run.algorithm_name)
        })?;
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

    pub(super) fn stargate_metrics_endpoints(
        &self,
        run: &BenchmarkK8sRun,
    ) -> anyhow::Result<Vec<String>> {
        let output = self
            .command()
            .arg("-n")
            .arg(&run.stargate_ns)
            .arg("get")
            .arg("services")
            .arg("-l")
            .arg("benchmark.stargate/role=pod-metrics")
            .arg("-o")
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

        let status = self
            .command()
            .arg("apply")
            .arg("-f")
            .arg(&manifest_path)
            .status()
            .context("failed to apply stargate external services")?;
        if !status.success() {
            bail!("kubectl apply failed for stargate external services");
        }
        Ok(())
    }

    fn list_stargate_pods(&self, run: &BenchmarkK8sRun) -> anyhow::Result<Vec<StargatePod>> {
        let output = self
            .command()
            .arg("-n")
            .arg(&run.stargate_ns)
            .arg("get")
            .arg("pods")
            .arg("-l")
            .arg("app=stargate")
            .arg("-o")
            .arg("jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
            .output()
            .context("failed to query stargate pods")?;
        if !output.status.success() {
            bail!("kubectl get pods failed while querying stargate pods");
        }

        String::from_utf8_lossy(&output.stdout)
            .lines()
            .map(str::trim)
            .filter(|line| !line.is_empty())
            .map(|line| {
                Ok(StargatePod {
                    name: line.to_string(),
                })
            })
            .collect()
    }

    fn rollout(&self, kind: &str, name: &str, namespace: &str) -> anyhow::Result<()> {
        let status = self
            .command()
            .arg("-n")
            .arg(namespace)
            .arg("rollout")
            .arg("status")
            .arg(format!("{kind}/{name}"))
            .arg("--timeout=180s")
            .status()
            .with_context(|| format!("failed waiting for rollout of {kind}/{name}"))?;
        if !status.success() {
            bail!("rollout failed for {kind}/{name} in namespace {namespace}");
        }
        Ok(())
    }

    fn wait_for_namespace_reuse(&self, namespace: &str, timeout: Duration) -> anyhow::Result<()> {
        let deadline = Instant::now() + timeout;
        loop {
            let output = self
                .command()
                .arg("get")
                .arg("namespace")
                .arg(namespace)
                .arg("-o")
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
        if let Some(host) = host_override {
            let host = host.trim();
            if !host.is_empty() {
                return Ok(host.to_string());
            }
        }

        let context = self
            .command()
            .arg("config")
            .arg("current-context")
            .output()
            .context("failed to query current kubectl context")?;
        if context.status.success()
            && String::from_utf8_lossy(&context.stdout).trim() == "docker-desktop"
        {
            return Ok("127.0.0.1".to_string());
        }

        let external = self.query_first_node_address("ExternalIP")?;
        if let Some(address) = external {
            return Ok(address);
        }

        let internal = self.query_first_node_address("InternalIP")?;
        internal.ok_or_else(|| anyhow::anyhow!("failed to resolve Kubernetes node address for NodePort access; set STARGATE_BENCH_NODE_HOST"))
    }

    fn query_first_node_address(&self, address_type: &str) -> anyhow::Result<Option<String>> {
        let output = self
            .command()
            .arg("get")
            .arg("nodes")
            .arg("-o")
            .arg(format!(
                "jsonpath={{.items[0].status.addresses[?(@.type==\"{address_type}\")].address}}"
            ))
            .output()
            .with_context(|| format!("failed to query Kubernetes node {address_type} address"))?;
        if !output.status.success() {
            bail!("kubectl get nodes failed while resolving NodePort host");
        }
        let address = String::from_utf8_lossy(&output.stdout).trim().to_string();
        Ok((!address.is_empty()).then_some(address))
    }
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

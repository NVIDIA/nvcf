// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::collections::{BTreeMap, BTreeSet};
use std::fmt;
use std::path::PathBuf;
use std::process::Output;
use std::time::Duration;

use anyhow::{Context, Result, bail, ensure};
use base64::{Engine as _, engine::general_purpose::STANDARD};
use clap::ValueEnum;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use tokio::process::Command;
use tokio::time::{Instant, sleep, timeout_at};

use crate::process::capture;

const READ_TIMEOUT: Duration = Duration::from_secs(30);
const ROUTER_SELECTOR: &str =
    "app.kubernetes.io/name=llm-request-router,app.kubernetes.io/instance=llm-request-router";

#[derive(Debug, Clone, Copy, ValueEnum)]
pub enum Phase {
    Stargate,
    Mockdc,
    Observability,
    Regional,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Region {
    pub region: String,
    pub namespace: String,
    pub model_name: String,
    pub routing_key: String,
    pub clusters: Clusters,
    observability: Observability,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct Clusters {
    pub stargate: Cluster,
    pub mockdcs: Vec<Cluster>,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Cluster {
    pub name: String,
    pub kube_context: String,
}

#[derive(Debug, Deserialize, Serialize)]
struct Observability {
    namespace: String,
}

#[derive(Debug)]
pub struct Topology {
    pub regions: Vec<Region>,
    executable: PathBuf,
}

#[derive(Debug, Serialize)]
pub struct CacheReset {
    pub before: Value,
    pub after: Value,
    pub calibration: Vec<Value>,
}

#[derive(Debug)]
struct AuthenticationFailure(String);

impl fmt::Display for AuthenticationFailure {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.0)
    }
}

impl std::error::Error for AuthenticationFailure {}

impl Topology {
    #[cfg(test)]
    pub(crate) fn with_executable(mut self, executable: PathBuf) -> Self {
        self.executable = executable;
        self
    }

    pub fn load(primary: &str, peers: &[String]) -> Result<Self> {
        let mut names = BTreeSet::new();
        let mut contexts = BTreeSet::new();
        let mut regions = Vec::new();
        for name in std::iter::once(primary).chain(peers.iter().map(String::as_str)) {
            ensure!(
                names.insert(name),
                "region and peer regions must be distinct"
            );
            let source = match name {
                "us-west-2" => include_str!("../../environments/us-west-2.yaml"),
                "us-east-1" => include_str!("../../environments/us-east-1.yaml"),
                _ => bail!("unsupported region {name:?}"),
            };
            let region: Region = serde_yaml_ng::from_str(source)
                .with_context(|| format!("parse embedded region {name}"))?;
            ensure!(
                region.region == name,
                "embedded region does not match {name}"
            );
            ensure!(
                region.clusters.mockdcs.len() == 2,
                "region {name} must define two MockDCs"
            );
            for cluster in
                std::iter::once(&region.clusters.stargate).chain(&region.clusters.mockdcs)
            {
                ensure!(
                    contexts.insert(cluster.kube_context.clone()),
                    "duplicate Kubernetes context {}",
                    cluster.kube_context
                );
            }
            regions.push(region);
        }
        let topology = Self {
            regions,
            executable: PathBuf::from("kubectl"),
        };
        for peer in &topology.regions[1..] {
            let primary = topology.primary();
            ensure!(
                peer.namespace == primary.namespace
                    && peer.model_name == primary.model_name
                    && peer.routing_key == primary.routing_key,
                "connected benchmark regions must share namespace, model and routing key"
            );
        }
        Ok(topology)
    }

    pub fn primary(&self) -> &Region {
        &self.regions[0]
    }

    pub fn kubectl(&self, context: &str, namespace: &str, timeout: Duration) -> Command {
        let mut command = Command::new(&self.executable);
        command.args(["--context", context, "-n", namespace]);
        let request_seconds = if timeout.is_zero() {
            0
        } else {
            timeout.as_secs().saturating_sub(5).max(1)
        };
        command.arg(format!("--request-timeout={}s", request_seconds));
        command
    }

    async fn read_output(
        &self,
        context: &str,
        namespace: &str,
        arguments: &[&str],
        timeout: Duration,
    ) -> Result<Output> {
        ensure!(
            matches!(arguments.first(), Some(&"get" | &"logs"))
                || arguments.starts_with(&["rollout", "status"]),
            "only Kubernetes reads may use automatic retries"
        );
        for attempt in 0..3 {
            let mut command = self.kubectl(context, namespace, timeout);
            command.args(arguments);
            match capture(command, None, timeout).await {
                Ok(output) => {
                    let message = String::from_utf8_lossy(&output.stderr);
                    if !output.status.success() && authentication_error(&message) {
                        return Err(AuthenticationFailure(format!(
                            "Kubernetes authentication failed for {context}: {}",
                            message.trim()
                        ))
                        .into());
                    }
                    if output.status.success() || !transient_control_error(&message) || attempt == 2
                    {
                        return Ok(output);
                    }
                }
                Err(error) => {
                    let timed_out = error
                        .downcast_ref::<tokio::time::error::Elapsed>()
                        .is_some()
                        || error
                            .downcast_ref::<std::io::Error>()
                            .is_some_and(|error| error.kind() == std::io::ErrorKind::TimedOut);
                    if !timed_out || attempt == 2 {
                        return Err(error)
                            .with_context(|| format!("Kubernetes read failed for {context}"));
                    }
                }
            }
            sleep(Duration::from_secs(attempt + 1)).await;
        }
        unreachable!("the final read attempt always returns")
    }

    pub async fn read(
        &self,
        context: &str,
        namespace: &str,
        arguments: &[&str],
        timeout: Duration,
    ) -> Result<String> {
        let output = self
            .read_output(context, namespace, arguments, timeout)
            .await?;
        command_stdout(output, context)
    }

    pub async fn write(
        &self,
        context: &str,
        namespace: &str,
        arguments: &[&str],
        timeout: Duration,
    ) -> Result<String> {
        let mut command = self.kubectl(context, namespace, timeout);
        command.args(arguments);
        let output = capture(command, None, timeout)
            .await
            .with_context(|| format!("Kubernetes command failed for {context}"))?;
        command_stdout(output, context)
    }

    async fn read_json(&self, context: &str, namespace: &str, arguments: &[&str]) -> Result<Value> {
        let source = self
            .read(context, namespace, arguments, READ_TIMEOUT)
            .await?;
        serde_json::from_str(&source)
            .with_context(|| format!("parse Kubernetes response from {context}"))
    }

    pub async fn verify_region(&self, index: usize, phase: Phase) -> Result<Vec<Value>> {
        let region = self.regions.get(index).context("invalid region index")?;
        let mut calibration = Vec::new();
        if matches!(phase, Phase::Stargate | Phase::Regional) {
            let context = &region.clusters.stargate.kube_context;
            for (name, replicas) in [
                ("stargate-dev-auth", 1),
                ("llm-request-router", 3),
                ("llm-request-router-backend-router", 3),
            ] {
                self.require_deployment(context, &region.namespace, name, replicas)
                    .await?;
            }
            let service = self
                .read_json(
                    context,
                    &region.namespace,
                    &[
                        "get",
                        "service",
                        "llm-request-router-backend-router",
                        "-o",
                        "json",
                    ],
                )
                .await?;
            check_registration_service(&service)?;
            if matches!(phase, Phase::Regional) {
                let pods = self
                    .read_json(
                        context,
                        &region.namespace,
                        &["get", "pods", "-l", ROUTER_SELECTOR, "-o", "json"],
                    )
                    .await?;
                ensure!(
                    items(&pods)?.len() == 3,
                    "found {} Stargate Pods; expected 3",
                    items(&pods)?.len()
                );
                let expected = (self.regions.len() * 4) as f64;
                for pod in items(&pods)? {
                    let name = required_str(pod, "/metadata/name")?;
                    let path = format!(
                        "--raw=/api/v1/namespaces/{}/pods/{name}:9090/proxy/metrics",
                        region.namespace
                    );
                    let metrics = self
                        .read(context, &region.namespace, &["get", &path], READ_TIMEOUT)
                        .await?;
                    check_router_metrics(
                        &metrics,
                        &region.routing_key,
                        &region.model_name,
                        expected,
                    )
                    .with_context(|| format!("Stargate Pod {name} membership is not ready"))?;
                }
            }
        }
        if matches!(phase, Phase::Mockdc | Phase::Regional) {
            for mockdc in &region.clusters.mockdcs {
                let pods = self.mockdc_pods(region, mockdc).await?;
                check_mockdc(&pods, &mockdc.name)?;
                for pod in active_pods(&pods)? {
                    if let Some(evidence) = self.calibration_evidence(region, mockdc, pod).await? {
                        calibration.push(evidence);
                    }
                }
            }
        }
        if matches!(phase, Phase::Observability | Phase::Regional) {
            for cluster in
                std::iter::once(&region.clusters.stargate).chain(&region.clusters.mockdcs)
            {
                self.require_deployment(
                    &cluster.kube_context,
                    &region.observability.namespace,
                    "stargate-dev-alloy",
                    1,
                )
                .await?;
                let account = self
                    .read_json(
                        &cluster.kube_context,
                        &region.observability.namespace,
                        &["get", "serviceaccount", "stargate-dev-alloy", "-o", "json"],
                    )
                    .await?;
                let arn = account
                    .pointer("/metadata/annotations/eks.amazonaws.com~1role-arn")
                    .and_then(Value::as_str)
                    .unwrap_or("");
                ensure!(
                    writer_role_matches(arn, &region.region),
                    "Alloy in {} does not have the AMP writer role",
                    cluster.name
                );
            }
        }
        Ok(calibration)
    }

    async fn require_deployment(
        &self,
        context: &str,
        namespace: &str,
        name: &str,
        replicas: u64,
    ) -> Result<()> {
        let deployment = self
            .read_json(
                context,
                namespace,
                &["get", "deployment", name, "-o", "json"],
            )
            .await?;
        check_deployment(&deployment, name, replicas)
    }

    pub async fn wait_ready(&self) -> Result<Vec<Value>> {
        let deadline = Instant::now() + Duration::from_secs(300);
        let mut last_error = String::from("verification has not completed");
        loop {
            let verification = async {
                let mut evidence = Vec::new();
                for index in 0..self.regions.len() {
                    evidence.extend(self.verify_region(index, Phase::Regional).await?);
                }
                Ok::<_, anyhow::Error>(evidence)
            };
            match timeout_at(deadline, verification).await {
                Ok(Ok(evidence)) => return Ok(evidence),
                Ok(Err(error)) => {
                    if error.downcast_ref::<AuthenticationFailure>().is_some()
                        || error
                            .downcast_ref::<crate::process::Interrupted>()
                            .is_some()
                    {
                        return Err(error);
                    }
                    last_error = format!("{error:#}");
                }
                Err(_) => {
                    bail!("regional health did not converge within 300 seconds: {last_error}")
                }
            }
            if timeout_at(deadline, sleep(Duration::from_secs(5)))
                .await
                .is_err()
            {
                bail!("regional health did not converge within 300 seconds: {last_error}");
            }
        }
    }

    pub async fn snapshot_controls(&self) -> Result<Value> {
        let mut deployments = BTreeMap::new();
        let mut policies = BTreeMap::new();
        for region in &self.regions {
            for cluster in
                std::iter::once(&region.clusters.stargate).chain(&region.clusters.mockdcs)
            {
                let listing = self
                    .read_json(
                        &cluster.kube_context,
                        &region.namespace,
                        &["get", "deployments", "-o", "json"],
                    )
                    .await?;
                let mut controls = BTreeMap::new();
                for deployment in items(&listing)? {
                    let name = required_str(deployment, "/metadata/name")?;
                    controls.insert(name, json!({
                        "replicas": deployment.pointer("/spec/replicas").context("deployment replicas missing")?,
                        "podSpec": deployment.pointer("/spec/template/spec").context("deployment Pod spec missing")?,
                    }));
                }
                deployments.insert(
                    cluster.kube_context.clone(),
                    serde_json::to_value(controls)?,
                );
            }
            let policy = self
                .read_json(
                    &region.clusters.stargate.kube_context,
                    &region.namespace,
                    &["get", "configmap", "llm-request-router-lb", "-o", "json"],
                )
                .await?;
            let source = required_str(&policy, "/data/lb-config.json")?;
            policies.insert(
                region.region.clone(),
                serde_json::from_str::<Value>(source)
                    .context("parse deployed load-balancer configuration")?,
            );
        }
        Ok(json!({"deployments": deployments, "loadBalancers": policies}))
    }

    pub async fn pod_state(&self) -> Result<Value> {
        let mut pods = BTreeMap::new();
        for region in &self.regions {
            for cluster in
                std::iter::once(&region.clusters.stargate).chain(&region.clusters.mockdcs)
            {
                let listing = self
                    .read_json(
                        &cluster.kube_context,
                        &region.namespace,
                        &["get", "pods", "-o", "json"],
                    )
                    .await?;
                for pod in active_pods(&listing)? {
                    let name = required_str(pod, "/metadata/name")?;
                    let statuses = pod
                        .pointer("/status/containerStatuses")
                        .and_then(Value::as_array)
                        .map(Vec::as_slice)
                        .unwrap_or(&[]);
                    let mut containers = BTreeMap::new();
                    for container in statuses {
                        containers.insert(
                            required_str(container, "/name")?,
                            json!({
                                "containerID": container.get("containerID"),
                                "imageID": container.get("imageID"),
                                "restartCount": container.get("restartCount"),
                            }),
                        );
                    }
                    pods.insert(
                        format!("{}/{name}", cluster.kube_context),
                        json!({
                            "uid": required_str(pod, "/metadata/uid")?, "containers": containers,
                        }),
                    );
                }
            }
        }
        Ok(serde_json::to_value(pods)?)
    }

    fn backend_targets(&self) -> impl Iterator<Item = (&Region, &Cluster, String)> {
        self.regions.iter().flat_map(|region| {
            region.clusters.mockdcs.iter().flat_map(move |cluster| {
                (0..2).map(move |index| {
                    (
                        region,
                        cluster,
                        format!("{}-stargate-dev-mockdc-backend-{index}", cluster.name),
                    )
                })
            })
        })
    }

    pub async fn cache_stats(&self) -> Result<Value> {
        let mut stats = BTreeMap::new();
        for (region, cluster, service) in self.backend_targets() {
            let path = format!(
                "--raw=/api/v1/namespaces/{}/services/http:{service}:http/proxy/kv-cache/stats",
                region.namespace
            );
            let snapshot = self
                .read_json(&cluster.kube_context, &region.namespace, &["get", &path])
                .await?;
            ensure!(
                snapshot.is_object(),
                "cache stats for {service} are not an object"
            );
            stats.insert(service, snapshot);
        }
        Ok(serde_json::to_value(stats)?)
    }

    pub async fn reset_caches(&self) -> Result<CacheReset> {
        let before = self.cache_stats().await?;
        for (region, cluster, deployment) in self.backend_targets() {
            self.write(
                &cluster.kube_context,
                &region.namespace,
                &["rollout", "restart", &format!("deployment/{deployment}")],
                Duration::from_secs(120),
            )
            .await?;
        }
        for (region, cluster, deployment) in self.backend_targets() {
            self.read(
                &cluster.kube_context,
                &region.namespace,
                &[
                    "rollout",
                    "status",
                    &format!("deployment/{deployment}"),
                    "--timeout=5m",
                ],
                Duration::from_secs(330),
            )
            .await?;
        }
        for region in &self.regions {
            self.write(
                &region.clusters.stargate.kube_context,
                &region.namespace,
                &["rollout", "restart", "deployment/llm-request-router"],
                Duration::from_secs(120),
            )
            .await?;
        }
        for region in &self.regions {
            self.read(
                &region.clusters.stargate.kube_context,
                &region.namespace,
                &[
                    "rollout",
                    "status",
                    "deployment/llm-request-router",
                    "--timeout=5m",
                ],
                Duration::from_secs(330),
            )
            .await?;
        }
        let calibration = self.wait_ready().await?;
        let after = self.cache_stats().await?;
        check_empty_caches(&after)?;
        Ok(CacheReset {
            before,
            after,
            calibration,
        })
    }

    pub async fn load_token(&self) -> Result<String> {
        match std::env::var("OPENAI_API_KEY") {
            Ok(token) if !token.is_empty() => return Ok(token),
            Err(std::env::VarError::NotUnicode(_)) => bail!("OPENAI_API_KEY is not UTF-8"),
            _ => {}
        }
        let primary = self.primary();
        let encoded = self
            .read(
                &primary.clusters.stargate.kube_context,
                &primary.namespace,
                &[
                    "get",
                    "secret",
                    "stargate-dev-auth-credentials",
                    "-o",
                    "jsonpath={.data.config\\.json}",
                ],
                READ_TIMEOUT,
            )
            .await?;
        decode_token(encoded.trim())
    }

    async fn mockdc_pods(&self, region: &Region, mockdc: &Cluster) -> Result<Value> {
        self.read_json(
            &mockdc.kube_context,
            &region.namespace,
            &[
                "get",
                "pods",
                "-l",
                &format!("app.kubernetes.io/instance={}", mockdc.name),
                "-o",
                "json",
            ],
        )
        .await
    }

    async fn calibration_evidence(
        &self,
        region: &Region,
        mockdc: &Cluster,
        pod: &Value,
    ) -> Result<Option<Value>> {
        let Some((engine_limit, calibration_limit)) = calibration_limits(pod)? else {
            return Ok(None);
        };
        let name = required_str(pod, "/metadata/name")?;
        let path = format!(
            "--raw=/api/v1/namespaces/{}/pods/{name}:8090/proxy/pylon/v1/stats/stream",
            region.namespace
        );
        let response = self
            .read_output(
                &mockdc.kube_context,
                &region.namespace,
                &["get", &path],
                READ_TIMEOUT,
            )
            .await?;
        let message = String::from_utf8_lossy(&response.stderr);
        ensure!(
            !response.status.success() && (message.contains("NotFound") || message.contains("404")),
            "disabled engine stats endpoint on {name} did not return 404"
        );
        let path = format!(
            "--raw=/api/v1/namespaces/{}/pods/{name}:9089/proxy/metrics",
            region.namespace
        );
        let metrics = self
            .read(
                &mockdc.kube_context,
                &region.namespace,
                &["get", &path],
                READ_TIMEOUT,
            )
            .await?;
        let mut evidence = check_fallback_metrics(&metrics, &region.model_name)
            .with_context(|| format!("startup calibration is not ready for {name}"))?;
        let logs = self
            .read(
                &mockdc.kube_context,
                &region.namespace,
                &["logs", name, "-c", "pylon", "--timestamps", "--tail=5000"],
                READ_TIMEOUT,
            )
            .await?;
        let observed = logs.contains(&format!("max_engine_concurrency: Some({engine_limit})"));
        let row = evidence
            .as_object_mut()
            .context("fallback evidence must be an object")?;
        row.insert("context".into(), json!(mockdc.kube_context));
        row.insert("pod".into(), json!(name));
        row.insert("statsEndpointStatus".into(), json!(404));
        row.insert("configuredEngineConcurrency".into(), json!(engine_limit));
        row.insert("calibrationMaxConcurrency".into(), json!(calibration_limit));
        row.insert(
            "concurrencyObservedInCalibrationLog".into(),
            json!(observed),
        );
        row.insert("metrics".into(), json!(metrics));
        Ok(Some(evidence))
    }
}

fn command_stdout(output: Output, context: &str) -> Result<String> {
    ensure!(
        output.status.success(),
        "kubectl failed for {context}: {}",
        String::from_utf8_lossy(&output.stderr).trim()
    );
    String::from_utf8(output.stdout)
        .with_context(|| format!("kubectl returned non-UTF-8 output for {context}"))
}

pub(crate) fn authentication_error(message: &str) -> bool {
    let message = message.to_ascii_lowercase();
    [
        "unauthorized",
        "forbidden",
        "provide credentials",
        "must be logged in",
        "accessdenied",
        "expiredtoken",
        "token has expired",
        "error loading sso token",
        "sso session",
        "refresh cached sso",
        "getting credentials",
        "exec plugin",
        "exec: executable",
    ]
    .iter()
    .any(|pattern| message.contains(pattern))
}

pub(crate) fn transient_control_error(message: &str) -> bool {
    let message = message.to_ascii_lowercase();
    [
        "tls handshake timeout",
        "connection reset",
        "http2: client connection lost",
        "http2: server sent goaway and closed the connection",
        "use of closed network connection",
        "unexpected eof",
        "i/o timeout",
        "context deadline exceeded",
        "unable to connect to the server",
        "serviceunavailable",
        "too many requests",
        "toomanyrequests",
    ]
    .iter()
    .any(|pattern| message.contains(pattern))
}

fn items(listing: &Value) -> Result<&[Value]> {
    listing
        .get("items")
        .and_then(Value::as_array)
        .map(Vec::as_slice)
        .context("Kubernetes response lacks an items array")
}

fn active_pods(listing: &Value) -> Result<impl Iterator<Item = &Value>> {
    Ok(items(listing)?.iter().filter(|pod| {
        pod.pointer("/metadata/deletionTimestamp")
            .is_none_or(Value::is_null)
    }))
}

fn required_str<'a>(value: &'a Value, pointer: &str) -> Result<&'a str> {
    value
        .pointer(pointer)
        .and_then(Value::as_str)
        .with_context(|| format!("Kubernetes response lacks string {pointer}"))
}

fn check_deployment(deployment: &Value, name: &str, replicas: u64) -> Result<()> {
    ensure!(
        deployment.pointer("/spec/replicas").and_then(Value::as_u64) == Some(replicas),
        "Deployment {name} has the wrong configured replica count"
    );
    for field in ["readyReplicas", "updatedReplicas", "availableReplicas"] {
        let count = deployment
            .get("status")
            .and_then(|status| status.get(field))
            .and_then(Value::as_u64)
            .unwrap_or(0);
        ensure!(
            count == replicas,
            "Deployment {name} has {count} {field}; expected {replicas}"
        );
    }
    let generation = deployment
        .pointer("/metadata/generation")
        .and_then(Value::as_u64)
        .context("deployment generation missing")?;
    ensure!(
        deployment
            .pointer("/status/observedGeneration")
            .and_then(Value::as_u64)
            .unwrap_or(0)
            >= generation,
        "Deployment {name} has not observed its current generation"
    );
    Ok(())
}

fn check_registration_service(service: &Value) -> Result<()> {
    ensure!(
        service.pointer("/spec/type").and_then(Value::as_str) == Some("LoadBalancer"),
        "backend-router Service is not a LoadBalancer"
    );
    let ports = service
        .pointer("/spec/ports")
        .and_then(Value::as_array)
        .context("backend-router ports missing")?;
    let actual: BTreeSet<_> = ports
        .iter()
        .map(|port| {
            (
                port.get("port").and_then(Value::as_u64),
                port.get("protocol").and_then(Value::as_str),
            )
        })
        .collect();
    ensure!(
        actual == BTreeSet::from([(Some(50071), Some("TCP")), (Some(50072), Some("UDP"))]),
        "backend-router Service exposes unexpected ports"
    );
    ensure!(
        service
            .pointer("/status/loadBalancer/ingress")
            .and_then(Value::as_array)
            .is_some_and(|ingress| !ingress.is_empty()),
        "backend-router LoadBalancer has no ingress endpoint"
    );
    Ok(())
}

fn container_args<'a>(pod: &'a Value, name: &str) -> Result<Vec<&'a str>> {
    let containers = pod
        .pointer("/spec/containers")
        .and_then(Value::as_array)
        .context("Pod containers missing")?;
    let container = containers
        .iter()
        .find(|container| container.get("name").and_then(Value::as_str) == Some(name))
        .with_context(|| format!("Pod lacks {name} container"))?;
    container
        .get("args")
        .and_then(Value::as_array)
        .context("container arguments missing")?
        .iter()
        .map(|argument| {
            argument
                .as_str()
                .context("container argument is not a string")
        })
        .collect()
}

fn argument<'a>(arguments: &[&'a str], name: &str) -> Option<&'a str> {
    let prefix = format!("{name}=");
    arguments.iter().enumerate().find_map(|(index, argument)| {
        argument.strip_prefix(&prefix).or_else(|| {
            (*argument == name)
                .then(|| arguments.get(index + 1).copied())
                .flatten()
        })
    })
}

fn check_mockdc(listing: &Value, name: &str) -> Result<()> {
    let pods: Vec<_> = active_pods(listing)?.collect();
    ensure!(
        pods.len() == 2,
        "MockDC {name} has {} Pods; expected 2",
        pods.len()
    );
    let mut identifiers = BTreeSet::new();
    for pod in pods {
        let statuses = pod
            .pointer("/status/containerStatuses")
            .and_then(Value::as_array)
            .context("MockDC container statuses missing")?;
        let ready: BTreeMap<_, _> = statuses
            .iter()
            .map(|status| {
                (
                    status["name"].as_str().unwrap_or(""),
                    status["ready"].as_bool().unwrap_or(false),
                )
            })
            .collect();
        ensure!(
            ready == BTreeMap::from([("mock-dynamo", true), ("pylon", true)]),
            "MockDC Pod is not fully ready"
        );
        let arguments = container_args(pod, "pylon")?;
        identifiers.insert(
            argument(&arguments, "--inference-server-id")
                .context("Pylon inference-server-id missing")?,
        );
    }
    let expected = BTreeSet::from([format!("{name}-backend-0"), format!("{name}-backend-1")]);
    ensure!(
        identifiers
            .iter()
            .copied()
            .eq(expected.iter().map(String::as_str)),
        "MockDC {name} has unexpected Pylon identities"
    );
    Ok(())
}

fn writer_role_matches(arn: &str, region: &str) -> bool {
    let Some(tail) = arn.strip_prefix("arn:aws:iam::") else {
        return false;
    };
    let Some((account, role)) = tail.split_once(":role/") else {
        return false;
    };
    let expected = if region == "us-west-2" {
        "stargate-dev-amp-writer".to_owned()
    } else {
        format!("stargate-dev-{region}-amp-writer")
    };
    account.len() == 12 && account.bytes().all(|byte| byte.is_ascii_digit()) && role == expected
}

fn metric_values(source: &str, name: &str, filters: &[(&str, &str)]) -> Result<Vec<f64>> {
    let mut values = Vec::new();
    for line in source.lines().map(str::trim) {
        let Some(mut tail) = line.strip_prefix(name) else {
            continue;
        };
        let mut labels = BTreeMap::new();
        if let Some(rest) = tail.strip_prefix('{') {
            tail = rest.trim_start();
            while !tail.starts_with('}') {
                let (key, value) = tail
                    .split_once('=')
                    .context("metric label lacks equals sign")?;
                let mut strings = serde_json::Deserializer::from_str(value).into_iter::<String>();
                let value = strings
                    .next()
                    .context("metric label lacks quoted value")??;
                let consumed = strings.byte_offset();
                ensure!(
                    labels.insert(key.trim(), value).is_none(),
                    "duplicate metric label"
                );
                tail = tail[key.len() + 1 + consumed..].trim_start();
                if let Some(rest) = tail.strip_prefix(',') {
                    tail = rest.trim_start();
                } else {
                    ensure!(tail.starts_with('}'), "metric label lacks delimiter");
                }
            }
            tail = &tail[1..];
        } else if !tail.starts_with(char::is_whitespace) {
            continue;
        }
        if filters
            .iter()
            .all(|(key, expected)| labels.get(key).map(String::as_str) == Some(*expected))
        {
            let value: f64 = tail
                .split_whitespace()
                .next()
                .context("metric sample value missing")?
                .parse()
                .context("invalid metric sample value")?;
            ensure!(value.is_finite(), "metric {name} is not finite");
            values.push(value);
        }
    }
    Ok(values)
}

fn check_router_metrics(
    metrics: &str,
    routing_key: &str,
    model: &str,
    expected: f64,
) -> Result<()> {
    let values = metric_values(
        metrics,
        "stargate_active_inference_servers",
        &[("routing_key", routing_key), ("model", model)],
    )?;
    ensure!(
        values == [expected],
        "router reports {values:?} active backends for model {model} and routing key {routing_key}; expected {expected}"
    );
    Ok(())
}

fn calibration_limits(pod: &Value) -> Result<Option<(u64, u64)>> {
    let mock = container_args(pod, "mock-dynamo")?;
    let pylon = container_args(pod, "pylon")?;
    if !mock.contains(&"--disable-stats-stream") || !pylon.contains(&"--do-calibration") {
        return Ok(None);
    }
    ensure!(
        argument(&pylon, "--engine-stats-stream") == Some("auto"),
        "calibrated fallback requires engine-stats-stream=auto"
    );
    ensure!(
        argument(&pylon, "--active-canary-interval-ms") == Some("0"),
        "calibrated benchmark must disable active canary traffic"
    );
    ensure!(
        argument(&pylon, "--initial-input-tps").is_none(),
        "startup calibration must not use initial input TPS"
    );
    let engine = argument(&pylon, "--max-engine-concurrency")
        .context("engine concurrency missing")?
        .parse::<u64>()?;
    let calibration = argument(&pylon, "--calibration-max-concurrency")
        .context("calibration concurrency missing")?
        .parse::<u64>()?;
    ensure!(
        engine > 0 && calibration > 0,
        "calibration concurrency must be positive"
    );
    Ok(Some((engine, calibration)))
}

fn check_fallback_metrics(metrics: &str, model: &str) -> Result<Value> {
    let events: f64 = metric_values(metrics, "pylon_engine_stats_stream_events_total", &[])?
        .iter()
        .sum();
    let connected: f64 = metric_values(metrics, "pylon_engine_stats_stream_connected", &[])?
        .iter()
        .sum();
    let transitions: f64 = metric_values(
        metrics,
        "pylon_engine_stats_source_transitions_total",
        &[("to", "openai_fallback")],
    )?
    .iter()
    .sum();
    let calibrations: f64 = metric_values(
        metrics,
        "pylon_model_calibration_duration_ms_count",
        &[("model", model), ("outcome", "completed")],
    )?
    .iter()
    .sum();
    let input_tps = metric_values(
        metrics,
        "pylon_model_last_mean_input_tps",
        &[("model", model)],
    )?;
    let engine_sources: f64 = metric_values(
        metrics,
        "pylon_model_stats_source",
        &[("model", model), ("source", "engine_stats_stream")],
    )?
    .iter()
    .sum();
    ensure!(
        events == 0.0 && connected == 0.0 && engine_sources == 0.0,
        "engine stats stream is active during fallback calibration"
    );
    ensure!(
        transitions >= 1.0 && calibrations >= 1.0,
        "fallback transition or completed model calibration is missing"
    );
    ensure!(
        input_tps.len() == 1 && input_tps[0] > 0.0,
        "calibrated input TPS is missing"
    );
    Ok(
        json!({"engineEvents": events, "connectedStreams": connected, "fallbackTransitions": transitions,
        "calibrationsCompleted": calibrations, "inputTps": input_tps[0], "engineStatsSource": engine_sources}),
    )
}

fn check_empty_caches(stats: &Value) -> Result<()> {
    for (backend, snapshot) in stats
        .as_object()
        .context("cache snapshot is not an object")?
    {
        for field in ["kv_cache_entries", "kv_cache_used_tokens"] {
            ensure!(
                snapshot.get(field).and_then(Value::as_u64) == Some(0),
                "backend {backend} cache field {field} is not zero"
            );
        }
    }
    Ok(())
}

fn decode_token(encoded: &str) -> Result<String> {
    let decoded = STANDARD
        .decode(encoded)
        .context("dev auth Secret is not valid base64")?;
    let document: Value =
        serde_json::from_slice(&decoded).context("dev auth Secret is not valid JSON")?;
    document
        .get("clientToken")
        .and_then(Value::as_str)
        .filter(|token| !token.is_empty())
        .map(str::to_owned)
        .context("dev auth Secret has no clientToken")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_support::FakeCommand;

    fn respond(
        fake: &FakeCommand,
        context: &str,
        namespace: &str,
        arguments: &[&str],
        responses: &[(i32, &str, &str)],
    ) {
        let mut full = vec![
            "--context",
            context,
            "-n",
            namespace,
            "--request-timeout=25s",
        ];
        full.extend_from_slice(arguments);
        fake.respond(&full, responses);
    }

    fn ready_deployment(replicas: u64) -> Value {
        json!({"metadata": {"generation": 1}, "spec": {"replicas": replicas}, "status": {
            "observedGeneration": 1, "readyReplicas": replicas, "updatedReplicas": replicas, "availableReplicas": replicas
        }})
    }

    fn pod(name: &str, identity: &str, terminating: bool) -> Value {
        let mut pod = json!({
            "metadata": {"name": name},
            "spec": {"containers": [
                {"name": "mock-dynamo", "args": ["--disable-stats-stream"]},
                {"name": "pylon", "args": [format!("--inference-server-id={identity}"),
                    "--engine-stats-stream=auto", "--max-engine-concurrency=17", "--do-calibration",
                    "--calibration-max-concurrency=13", "--active-canary-interval-ms=0"]}
            ]},
            "status": {"containerStatuses": [
                {"name": "mock-dynamo", "ready": true}, {"name": "pylon", "ready": true}
            ]}
        });
        if terminating {
            pod["metadata"]["deletionTimestamp"] = json!("2026-09-17T00:00:00Z");
        }
        pod
    }

    #[test]
    fn topology_loads_single_or_connected_regions_and_rejects_duplicate_selection() -> Result<()> {
        let west = Topology::load("us-west-2", &[])?;
        assert_eq!(west.backend_targets().count(), 4);
        assert_eq!(
            west.primary().clusters.stargate.kube_context,
            "stargate-usw2"
        );
        let connected = Topology::load("us-east-1", &["us-west-2".into()])?;
        assert_eq!(connected.backend_targets().count(), 8);
        assert_eq!(connected.primary().region, "us-east-1");
        assert!(Topology::load("us-west-2", &["us-west-2".into()]).is_err());
        assert!(Topology::load("../us-west-2", &[]).is_err());
        Ok(())
    }

    #[tokio::test]
    async fn reads_retry_only_transient_failures_and_writes_are_never_retried() -> Result<()> {
        let fake = FakeCommand::new();
        let mut topology = Topology::load("us-west-2", &[])?;
        topology.executable = fake.executable();
        respond(
            &fake,
            "test",
            "test",
            &["get", "pods"],
            &[
                (1, "", "TLS handshake timeout"),
                (1, "", "Error from server (TooManyRequests)"),
                (0, "ready", ""),
            ],
        );
        assert_eq!(
            topology
                .read("test", "test", &["get", "pods"], READ_TIMEOUT)
                .await?,
            "ready"
        );
        assert_eq!(fake.calls().len(), 3);

        respond(
            &fake,
            "test",
            "test",
            &["get", "nodes"],
            &[(1, "", "connection reset")],
        );
        assert!(
            topology
                .read("test", "test", &["get", "nodes"], READ_TIMEOUT)
                .await
                .is_err()
        );
        assert_eq!(fake.calls().len(), 6);

        respond(
            &fake,
            "test",
            "test",
            &["get", "services"],
            &[
                (1, "", "Unable to connect to the server: Forbidden"),
                (0, "must not retry", ""),
            ],
        );
        let denied = topology
            .read("test", "test", &["get", "services"], READ_TIMEOUT)
            .await
            .unwrap_err();
        assert!(denied.downcast_ref::<AuthenticationFailure>().is_some());
        assert_eq!(fake.calls().len(), 7);

        respond(
            &fake,
            "test",
            "test",
            &["rollout", "restart", "deployment/test"],
            &[(1, "", "connection reset"), (0, "must not retry", "")],
        );
        assert!(
            topology
                .write(
                    "test",
                    "test",
                    &["rollout", "restart", "deployment/test"],
                    READ_TIMEOUT
                )
                .await
                .is_err()
        );
        assert_eq!(fake.calls().len(), 8);
        assert!(
            topology
                .read("test", "test", &["delete", "pod", "test"], READ_TIMEOUT)
                .await
                .is_err()
        );
        assert_eq!(fake.calls().len(), 8);

        let streaming = topology.kubectl("test", "test", Duration::ZERO);
        assert!(
            streaming
                .as_std()
                .get_args()
                .any(|arg| arg == "--request-timeout=0s")
        );
        Ok(())
    }

    #[tokio::test]
    async fn readiness_does_not_retry_authentication_failures() -> Result<()> {
        for message in [
            "Unable to connect to the server: Token has expired and refresh failed",
            "Unable to connect to the server: getting credentials: exec: executable aws failed with exit code 253",
        ] {
            let fake = FakeCommand::new();
            fake.default_response(&[(1, "", message)]);
            let mut topology = Topology::load("us-west-2", &[])?;
            topology.executable = fake.executable();
            let error = tokio::time::timeout(Duration::from_secs(2), topology.wait_ready())
                .await?
                .unwrap_err();
            assert!(error.downcast_ref::<AuthenticationFailure>().is_some());
            assert_eq!(fake.calls().len(), 1);
        }
        Ok(())
    }

    #[tokio::test]
    async fn snapshots_ignore_rollout_annotations_but_record_images_and_restarts() -> Result<()> {
        let fake = FakeCommand::new();
        let mut topology = Topology::load("us-west-2", &[])?;
        topology.executable = fake.executable();
        let region = topology.primary();
        for cluster in std::iter::once(&region.clusters.stargate).chain(&region.clusters.mockdcs) {
            let before = json!({"items":[{"metadata":{"name":"service", "generation":1}, "spec":{
                "replicas":1, "template":{"metadata":{"annotations":{"restartedAt":"before"}}, "spec":{
                    "containers":[{"name":"service","image":"old","resources":{"limits":{"cpu":"1"}}, "args":["--fixed"],"env":[]}]
                }}
            }}]});
            let mut restarted = before.clone();
            restarted["items"][0]["metadata"]["generation"] = json!(2);
            restarted["items"][0]["spec"]["template"]["metadata"]["annotations"]["restartedAt"] =
                json!("after");
            let mut changed = restarted.clone();
            changed["items"][0]["spec"]["template"]["spec"]["containers"][0]["image"] =
                json!("new");
            respond(
                &fake,
                &cluster.kube_context,
                &region.namespace,
                &["get", "deployments", "-o", "json"],
                &[
                    (0, &before.to_string(), ""),
                    (0, &restarted.to_string(), ""),
                    (0, &changed.to_string(), ""),
                ],
            );
            let pods = json!({"items":[
                {"metadata":{"name":"ready","uid":"pod-id"},"status":{"containerStatuses":[{"name":"worker","containerID":"first","imageID":"digest","restartCount":0}]}},
                {"metadata":{"name":"pending","uid":"pending-id"},"status":{"phase":"Pending"}}
            ]});
            let mut restarted = pods.clone();
            restarted["items"][0]["status"]["containerStatuses"][0]["containerID"] =
                json!("second");
            restarted["items"][0]["status"]["containerStatuses"][0]["restartCount"] = json!(1);
            respond(
                &fake,
                &cluster.kube_context,
                &region.namespace,
                &["get", "pods", "-o", "json"],
                &[(0, &pods.to_string(), ""), (0, &restarted.to_string(), "")],
            );
        }
        respond(
            &fake,
            &region.clusters.stargate.kube_context,
            &region.namespace,
            &["get", "configmap", "llm-request-router-lb", "-o", "json"],
            &[(0, r#"{"data":{"lb-config.json":"{\"n\":2}"}}"#, "")],
        );
        let before = topology.snapshot_controls().await?;
        assert_eq!(before, topology.snapshot_controls().await?);
        assert_ne!(before, topology.snapshot_controls().await?);
        let before = topology.pod_state().await?;
        assert_eq!(before["stargate-usw2/pending"]["containers"], json!({}));
        assert_ne!(before, topology.pod_state().await?);
        Ok(())
    }

    #[tokio::test]
    async fn cache_reset_waits_for_backends_before_restarting_routers() -> Result<()> {
        let fake = FakeCommand::new();
        fake.default_response(&[(0, "", "")]);
        let mut topology = Topology::load("us-west-2", &[])?;
        topology.executable = fake.executable();
        let region = topology.primary();
        let context = &region.clusters.stargate.kube_context;
        let namespace = &region.namespace;
        for (name, replicas) in [
            ("stargate-dev-auth", 1),
            ("llm-request-router", 3),
            ("llm-request-router-backend-router", 3),
        ] {
            respond(
                &fake,
                context,
                namespace,
                &["get", "deployment", name, "-o", "json"],
                &[(0, &ready_deployment(replicas).to_string(), "")],
            );
        }
        let service = json!({"spec":{"type":"LoadBalancer","ports":[{"port":50071,"protocol":"TCP"},{"port":50072,"protocol":"UDP"}]},
            "status":{"loadBalancer":{"ingress":[{"hostname":"example.invalid"}]}}});
        respond(
            &fake,
            context,
            namespace,
            &[
                "get",
                "service",
                "llm-request-router-backend-router",
                "-o",
                "json",
            ],
            &[(0, &service.to_string(), "")],
        );
        let routers = json!({"items":[{"metadata":{"name":"router-0"}}, {"metadata":{"name":"router-1"}}, {"metadata":{"name":"router-2"}}]});
        respond(
            &fake,
            context,
            namespace,
            &["get", "pods", "-l", ROUTER_SELECTOR, "-o", "json"],
            &[(0, &routers.to_string(), "")],
        );
        for index in 0..3 {
            let path = format!(
                "--raw=/api/v1/namespaces/{namespace}/pods/router-{index}:9090/proxy/metrics"
            );
            let metrics = format!(
                "stargate_active_inference_servers{{model=\"{}\",routing_key=\"{}\"}} 4\n",
                region.model_name, region.routing_key
            );
            respond(
                &fake,
                context,
                namespace,
                &["get", &path],
                &[(0, &metrics, "")],
            );
        }
        for mockdc in &region.clusters.mockdcs {
            let pods = json!({"items":[pod("backend-0", &format!("{}-backend-0", mockdc.name), false), pod("backend-1", &format!("{}-backend-1", mockdc.name), false)]});
            let selector = format!("app.kubernetes.io/instance={}", mockdc.name);
            respond(
                &fake,
                &mockdc.kube_context,
                namespace,
                &["get", "pods", "-l", &selector, "-o", "json"],
                &[(0, &pods.to_string(), "")],
            );
            for index in 0..2 {
                let stats = format!(
                    "--raw=/api/v1/namespaces/{namespace}/pods/backend-{index}:8090/proxy/pylon/v1/stats/stream"
                );
                respond(
                    &fake,
                    &mockdc.kube_context,
                    namespace,
                    &["get", &stats],
                    &[(1, "", "NotFound")],
                );
                let path = format!(
                    "--raw=/api/v1/namespaces/{namespace}/pods/backend-{index}:9089/proxy/metrics"
                );
                let metrics = format!(
                    concat!(
                        "pylon_engine_stats_stream_events_total 0\n",
                        "pylon_engine_stats_stream_connected 0\n",
                        "pylon_engine_stats_source_transitions_total{{to=\"openai_fallback\"}} 1\n",
                        "pylon_model_calibration_duration_ms_count{{model=\"{}\",outcome=\"completed\"}} 1\n",
                        "pylon_model_last_mean_input_tps{{model=\"{}\"}} 321\n",
                        "pylon_model_stats_source{{model=\"{}\",source=\"engine_stats_stream\"}} 0\n",
                    ),
                    region.model_name, region.model_name, region.model_name
                );
                respond(
                    &fake,
                    &mockdc.kube_context,
                    namespace,
                    &["get", &path],
                    &[(0, &metrics, "")],
                );
                respond(
                    &fake,
                    &mockdc.kube_context,
                    namespace,
                    &[
                        "logs",
                        &format!("backend-{index}"),
                        "-c",
                        "pylon",
                        "--timestamps",
                        "--tail=5000",
                    ],
                    &[(0, "max_engine_concurrency: Some(17)", "")],
                );
            }
        }
        for cluster in std::iter::once(&region.clusters.stargate).chain(&region.clusters.mockdcs) {
            respond(
                &fake,
                &cluster.kube_context,
                &region.observability.namespace,
                &["get", "deployment", "stargate-dev-alloy", "-o", "json"],
                &[(0, &ready_deployment(1).to_string(), "")],
            );
            let account = json!({"metadata":{"annotations":{"eks.amazonaws.com/role-arn":"arn:aws:iam::123456789012:role/stargate-dev-amp-writer"}}});
            respond(
                &fake,
                &cluster.kube_context,
                &region.observability.namespace,
                &["get", "serviceaccount", "stargate-dev-alloy", "-o", "json"],
                &[(0, &account.to_string(), "")],
            );
        }
        for (region, cluster, service) in topology.backend_targets() {
            let path = format!(
                "--raw=/api/v1/namespaces/{}/services/http:{service}:http/proxy/kv-cache/stats",
                region.namespace
            );
            respond(
                &fake,
                &cluster.kube_context,
                &region.namespace,
                &["get", &path],
                &[
                    (0, r#"{"kv_cache_entries":3,"kv_cache_used_tokens":99}"#, ""),
                    (0, r#"{"kv_cache_entries":0,"kv_cache_used_tokens":0}"#, ""),
                ],
            );
        }
        let reset =
            tokio::time::timeout(Duration::from_secs(10), topology.reset_caches()).await??;
        assert_eq!(reset.before.as_object().unwrap().len(), 4);
        for snapshot in reset.before.as_object().unwrap().values() {
            assert_eq!(snapshot["kv_cache_entries"], 3);
        }
        check_empty_caches(&reset.after)?;
        assert_eq!(reset.calibration.len(), 4);
        for evidence in &reset.calibration {
            assert_eq!(evidence["inputTps"], 321.0);
            assert_eq!(evidence["configuredEngineConcurrency"], 17);
            assert_eq!(evidence["calibrationMaxConcurrency"], 13);
            assert_eq!(evidence["concurrencyObservedInCalibrationLog"], true);
        }
        let calls: Vec<Vec<String>> = fake
            .calls()
            .into_iter()
            .map(|arguments| {
                arguments
                    .into_iter()
                    .map(|argument| argument.into_string().unwrap())
                    .collect()
            })
            .collect();
        assert_eq!(
            calls
                .iter()
                .filter(|arguments| arguments
                    .iter()
                    .any(|argument| argument.ends_with("/pylon/v1/stats/stream")))
                .count(),
            4
        );
        assert_eq!(
            calls
                .iter()
                .filter(|arguments| arguments
                    .iter()
                    .any(|argument| argument.contains(":9089/proxy/metrics")))
                .count(),
            4
        );
        assert_eq!(
            calls
                .iter()
                .filter(|arguments| arguments.iter().any(|argument| argument == "logs"))
                .count(),
            4
        );
        let rollouts: Vec<_> = calls
            .iter()
            .filter_map(|arguments| {
                arguments
                    .iter()
                    .position(|argument| argument == "rollout")
                    .map(|index| (&arguments[index + 1], &arguments[index + 2]))
            })
            .collect();
        assert_eq!(rollouts.len(), 10);
        assert!(
            rollouts[..4]
                .iter()
                .all(|(operation, name)| *operation == "restart" && name.contains("mockdc"))
        );
        assert!(
            rollouts[4..8]
                .iter()
                .all(|(operation, name)| *operation == "status" && name.contains("mockdc"))
        );
        assert_eq!(
            rollouts[8],
            (
                &"restart".to_owned(),
                &"deployment/llm-request-router".to_owned()
            )
        );
        assert_eq!(
            rollouts[9],
            (
                &"status".to_owned(),
                &"deployment/llm-request-router".to_owned()
            )
        );
        Ok(())
    }

    #[test]
    fn deployment_requires_current_generation_and_all_ready_replicas() -> Result<()> {
        let mut deployment = json!({
            "metadata": {"generation": 2}, "spec": {"replicas": 3},
            "status": {"observedGeneration": 2, "readyReplicas": 3, "updatedReplicas": 3, "availableReplicas": 3}
        });
        check_deployment(&deployment, "router", 3)?;
        for key in [
            "observedGeneration",
            "readyReplicas",
            "updatedReplicas",
            "availableReplicas",
        ] {
            let original = deployment["status"][key].take();
            assert!(
                check_deployment(&deployment, "router", 3).is_err(),
                "missing {key} accepted"
            );
            deployment["status"][key] = original;
        }
        deployment["status"]["observedGeneration"] = json!(1);
        assert!(check_deployment(&deployment, "router", 3).is_err());
        Ok(())
    }

    #[test]
    fn registration_service_requires_both_protocols_and_ingress() -> Result<()> {
        let mut service = json!({"spec": {"type": "LoadBalancer", "ports": [
            {"port":50071,"protocol":"TCP"}, {"port":50072,"protocol":"UDP"}
        ]}, "status": {"loadBalancer":{"ingress":[{"hostname":"example.invalid"}]}}});
        check_registration_service(&service)?;
        service["spec"]["ports"][1]["protocol"] = json!("TCP");
        assert!(check_registration_service(&service).is_err());
        service["spec"]["ports"][1]["protocol"] = json!("UDP");
        service["status"]["loadBalancer"]["ingress"] = json!([]);
        assert!(check_registration_service(&service).is_err());
        Ok(())
    }

    #[test]
    fn mockdc_ignores_terminating_pods_but_requires_both_ready_identities() -> Result<()> {
        let mut pods = json!({"items": [pod("old", "mock-backend-0", true),
            pod("new-0", "mock-backend-0", false), pod("new-1", "mock-backend-1", false)]});
        check_mockdc(&pods, "mock")?;
        pods["items"][2]["status"]["containerStatuses"][1]["ready"] = json!(false);
        assert!(check_mockdc(&pods, "mock").is_err());
        pods["items"][2] = pod("new-1", "mock-backend-0", false);
        assert!(check_mockdc(&pods, "mock").is_err());
        Ok(())
    }

    #[test]
    fn metrics_use_exact_names_and_decode_escaped_labels() -> Result<()> {
        let metrics = concat!(
            "# gauge help\n",
            "stargate_active_inference_servers_other{model=\"m\",routing_key=\"r\"} 999\n",
            "stargate_active_inference_servers{model=\"other\",routing_key=\"r\"} 900\n",
            "stargate_active_inference_servers{ model=\"m\", routing_key=\"r\" } 8 12345\n",
            "escaped{key=\"a,}\\\"b\\n\\\\\"} 4\n",
        );
        check_router_metrics(metrics, "r", "m", 8.0)?;
        assert!(check_router_metrics(metrics, "r", "m", 4.0).is_err());
        assert!(check_router_metrics(metrics, "missing", "m", 8.0).is_err());
        assert_eq!(
            metric_values(metrics, "escaped", &[("key", "a,}\"b\n\\")])?,
            [4.0]
        );
        Ok(())
    }

    #[test]
    fn observability_roles_preserve_west_role_and_bind_other_regions() {
        assert!(writer_role_matches(
            "arn:aws:iam::123456789012:role/stargate-dev-amp-writer",
            "us-west-2"
        ));
        assert!(writer_role_matches(
            "arn:aws:iam::123456789012:role/stargate-dev-us-east-1-amp-writer",
            "us-east-1"
        ));
        for arn in [
            "arn:aws:iam::123:role/stargate-dev-amp-writer",
            "arn:aws:iam::123456789012:role/stargate-dev-amp-writer-extra",
        ] {
            assert!(!writer_role_matches(arn, "us-west-2"));
        }
        assert!(!writer_role_matches(
            "arn:aws:iam::123456789012:role/stargate-dev-amp-writer",
            "us-east-1"
        ));
    }

    #[test]
    fn fallback_configuration_is_inferred_and_metrics_are_model_scoped() -> Result<()> {
        let pod = pod("worker", "backend-0", false);
        assert_eq!(calibration_limits(&pod)?, Some((17, 13)));
        let metrics = concat!(
            "pylon_engine_stats_stream_events_total{kind=\"sample\"} 0\n",
            "pylon_engine_stats_stream_connected{mode=\"auto\"} 0\n",
            "pylon_engine_stats_source_transitions_total{to=\"openai_fallback\",reason=\"unsupported\"} 1\n",
            "pylon_model_calibration_duration_ms_count{model=\"wanted\",outcome=\"completed\"} 1\n",
            "pylon_model_last_mean_input_tps{model=\"wanted\"} 321\n",
            "pylon_model_stats_source{model=\"wanted\",source=\"engine_stats_stream\"} 0\n",
            "pylon_model_calibration_duration_ms_count{model=\"other\",outcome=\"completed\"} 100\n",
            "pylon_model_last_mean_input_tps{model=\"other\"} 999\n",
        );
        assert_eq!(
            check_fallback_metrics(metrics, "wanted")?["inputTps"],
            321.0
        );
        assert!(check_fallback_metrics(metrics, "missing").is_err());
        let incomplete = metrics.replace(
            "model=\"wanted\",outcome=\"completed\"} 1",
            "model=\"wanted\",outcome=\"completed\"} 0",
        );
        assert!(check_fallback_metrics(&incomplete, "wanted").is_err());
        let active = metrics.replace("mode=\"auto\"} 0", "mode=\"auto\"} 1");
        assert!(check_fallback_metrics(&active, "wanted").is_err());
        Ok(())
    }

    #[test]
    fn reset_requires_explicit_zero_cache_fields_and_token_errors_do_not_echo_values() -> Result<()>
    {
        check_empty_caches(&json!({"backend": {"kv_cache_entries": 0,"kv_cache_used_tokens": 0}}))?;
        assert!(check_empty_caches(&json!({"backend": {"kv_cache_entries": 0}})).is_err());
        assert!(
            check_empty_caches(
                &json!({"backend": {"kv_cache_entries": 1,"kv_cache_used_tokens": 0}})
            )
            .is_err()
        );
        assert_eq!(
            decode_token(&STANDARD.encode(br#"{"clientToken":"fixture-secret"}"#))?,
            "fixture-secret"
        );
        let error = decode_token(&STANDARD.encode(br#"{"clientToken": {"fixture-secret":true}}"#))
            .unwrap_err();
        assert!(!format!("{error:#}").contains("fixture-secret"));
        Ok(())
    }
}

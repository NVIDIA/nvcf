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

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};

use anyhow::{Context, ensure};
use serde::Serialize;

use crate::config::{AlgorithmConfig, BenchmarkConfig};
use crate::manifest::{Manifest, write_manifest_json};
use crate::runtime::{BackendRuntimeSpec, PylonRuntimeSpec, slugify};

const STARGATE_GRPC_PORT: u16 = 50071;
const STARGATE_HTTP_PORT: u16 = 8000;
const STARGATE_METRICS_PORT: u16 = 9090;
const MOCK_DYNAMO_HTTP_PORT: u16 = 8090;

macro_rules! command {
    ($($flag:expr $(=> $value:expr)?),* $(,)?) => {
        [$(String::from($flag), $(String::from($value),)?)*]
    };
}

#[derive(Serialize)]
pub struct PreparedSuite {
    pub output_dir: PathBuf,
    pub benchmark_name: String,
    pub seed: u64,
    pub manifest_path: PathBuf,
    pub algorithm_runs: Vec<PreparedAlgorithmRun>,
}

#[derive(Serialize)]
pub struct PreparedAlgorithmRun {
    pub algorithm_name: String,
    pub run_dir: PathBuf,
    pub compose_path: PathBuf,
    pub lb_config_path: PathBuf,
    pub run_info_path: PathBuf,
    pub stargate_http_endpoint: String,
    pub stargate_grpc_endpoint: String,
    pub stargate_metrics_endpoint: String,
}

pub fn prepare_suite(
    config: &BenchmarkConfig,
    manifest: &Manifest,
    output_dir: &Path,
) -> anyhow::Result<PreparedSuite> {
    ensure!(
        !config.algorithms.is_empty(),
        "benchmark config must define at least one algorithm"
    );
    config.validate()?;

    std::fs::create_dir_all(output_dir)
        .with_context(|| format!("failed to create output dir {}", output_dir.display()))?;

    let manifest_path = output_dir.join("manifest.json");
    write_manifest_json(&manifest_path, manifest)?;

    write_pretty_json(
        &output_dir.join("benchmark-config.json"),
        config,
        "benchmark config",
    )?;

    let mut runs = Vec::with_capacity(config.algorithms.len());
    for (index, algorithm) in config.algorithms.iter().enumerate() {
        let run_dir = output_dir.join(format!("run-{}", slugify(&algorithm.name)));
        std::fs::create_dir_all(&run_dir)
            .with_context(|| format!("failed to create run dir {}", run_dir.display()))?;

        let lb_config_path = run_dir.join("lb-config.json");
        write_pretty_json(
            &lb_config_path,
            &algorithm.config,
            &format!("LB config for {}", algorithm.name),
        )?;

        let host_port_offset = (index as u16) * 10;
        let stargate_grpc_host_port = STARGATE_GRPC_PORT + host_port_offset;
        let stargate_http_host_port = STARGATE_HTTP_PORT + host_port_offset;
        let stargate_metrics_host_port = STARGATE_METRICS_PORT + host_port_offset;

        let compose = build_compose_spec(
            config,
            algorithm,
            &lb_config_path,
            stargate_grpc_host_port,
            stargate_http_host_port,
            stargate_metrics_host_port,
        )?;
        let compose_path = run_dir.join("docker-compose.yaml");
        let compose_yaml =
            serde_yaml_ng::to_string(&compose).context("failed to serialize compose yaml")?;
        std::fs::write(&compose_path, compose_yaml)
            .with_context(|| format!("failed to write {}", compose_path.display()))?;

        let stargate_http_endpoint = format!("http://127.0.0.1:{stargate_http_host_port}");
        let stargate_grpc_endpoint = format!("127.0.0.1:{stargate_grpc_host_port}");
        let stargate_metrics_endpoint =
            format!("http://127.0.0.1:{stargate_metrics_host_port}/metrics");
        let run_info = serde_json::json!({
            "algorithm_name": algorithm.name,
            "pylon_queue_admission": algorithm.pylon_queue_admission,
            "stargate_http_endpoint": &stargate_http_endpoint,
            "stargate_grpc_endpoint": &stargate_grpc_endpoint,
            "stargate_metrics_endpoint": &stargate_metrics_endpoint,
            "compose_path": compose_path,
            "lb_config_path": lb_config_path,
            "manifest_path": manifest_path,
        });
        let run_info_path = run_dir.join("run-info.json");
        write_pretty_json(&run_info_path, &run_info, "run info")?;

        runs.push(PreparedAlgorithmRun {
            algorithm_name: algorithm.name.clone(),
            run_dir,
            compose_path,
            lb_config_path,
            run_info_path,
            stargate_http_endpoint,
            stargate_grpc_endpoint,
            stargate_metrics_endpoint,
        });
    }

    let summary_path = output_dir.join("prepared-suite.json");
    let summary = PreparedSuite {
        output_dir: output_dir.to_path_buf(),
        benchmark_name: config.name.clone(),
        seed: manifest.seed,
        manifest_path,
        algorithm_runs: runs,
    };
    write_pretty_json(&summary_path, &summary, "prepared suite")?;

    Ok(summary)
}

fn write_pretty_json(path: &Path, value: &impl Serialize, description: &str) -> anyhow::Result<()> {
    let bytes = serde_json::to_vec_pretty(value)
        .with_context(|| format!("failed to serialize {description}"))?;
    std::fs::write(path, bytes).with_context(|| format!("failed to write {}", path.display()))
}

#[derive(Serialize)]
struct ComposeSpec {
    services: BTreeMap<String, ComposeService>,
}

#[derive(Serialize)]
struct ComposeService {
    build: ComposeBuild,
    command: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    ports: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    volumes: Vec<String>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    depends_on: BTreeMap<String, ComposeDependency>,
}

#[derive(Serialize)]
struct ComposeBuild {
    context: String,
    dockerfile: String,
    target: &'static str,
}

#[derive(Serialize)]
struct ComposeDependency {
    condition: &'static str,
}

const SERVICE_STARTED: ComposeDependency = ComposeDependency {
    condition: "service_started",
};

fn compose_build(repo_root: &Path, dockerfile: &Path, target: &'static str) -> ComposeBuild {
    ComposeBuild {
        context: repo_root.display().to_string(),
        dockerfile: dockerfile.display().to_string(),
        target,
    }
}

fn build_compose_spec(
    config: &BenchmarkConfig,
    algorithm: &AlgorithmConfig,
    lb_config_path: &Path,
    stargate_grpc_host_port: u16,
    stargate_http_host_port: u16,
    stargate_metrics_host_port: u16,
) -> anyhow::Result<ComposeSpec> {
    let repo_root = repo_root();
    let dockerfile = repo_root.join("Dockerfile");
    let mut services = BTreeMap::new();
    let stargate_lb_config_container_path = "/config/lb-config.json";

    services.insert(
        "stargate".to_string(),
        ComposeService {
            build: compose_build(&repo_root, &dockerfile, "stargate-runtime"),
            command: Vec::from(command![
                "--stargate-id" => "benchmark-stargate",
                "--listen-addr" => format!("0.0.0.0:{STARGATE_GRPC_PORT}"),
                "--http-listen-addr" => format!("0.0.0.0:{STARGATE_HTTP_PORT}"),
                "--advertise-addr" => format!("127.0.0.1:{STARGATE_GRPC_PORT}"),
                "--stargate-discovery-dns-name" => "stargate",
                "--metrics-port" => STARGATE_METRICS_PORT.to_string(),
                "--lb-config-path" => stargate_lb_config_container_path,
                "--backend-connectivity=reverse",
                "--reverse-tunnel-listen-addr" => "0.0.0.0:50072",
                "--advertised-hostname-template" => "stargate",
                "--tunnel-protocol" => config.tunnel_protocol.to_string(),
            ]),
            ports: vec![
                format!("{stargate_grpc_host_port}:{STARGATE_GRPC_PORT}"),
                format!("{stargate_http_host_port}:{STARGATE_HTTP_PORT}"),
                format!("{stargate_metrics_host_port}:{STARGATE_METRICS_PORT}"),
            ],
            volumes: vec![format!(
                "{}:{}:ro",
                absolute_bind_path(lb_config_path)?.display(),
                stargate_lb_config_container_path
            )],
            depends_on: BTreeMap::new(),
        },
    );

    for backend_index in 0..config.backends.count {
        let pylon = PylonRuntimeSpec::for_backend(config, backend_index);
        if pylon.owns_upstream_backend() {
            let backend = BackendRuntimeSpec::for_upstream(config, pylon.upstream_index);
            services.insert(
                backend.name,
                ComposeService {
                    build: compose_build(&repo_root, &dockerfile, "mock-dynamo-runtime"),
                    command: Vec::from(command![
                        "--http-listen-addr" => format!("0.0.0.0:{MOCK_DYNAMO_HTTP_PORT}"),
                        "--model-name" => config.model.clone(),
                        "--num-tokens" => "32",
                        "--token-delay-ms" => backend.per_token_delay_ms.to_string(),
                        "--decode-jitter-ms" => backend.decode_jitter_ms.to_string(),
                        "--ttft-ms" => backend.ttft_ms.to_string(),
                        "--ttft-jitter-ms" => backend.ttft_jitter_ms.to_string(),
                        "--prefill-tokens-per-s" => backend.prefill_tokens_per_s.to_string(),
                        "--max-concurrent-requests" => backend.max_concurrent_requests.to_string(),
                        "--kv-cache-capacity-tokens" => backend.kv_cache_capacity_tokens.to_string(),
                    ]),
                    ports: Vec::new(),
                    volumes: Vec::new(),
                    depends_on: BTreeMap::new(),
                },
            );
        }

        let depends_on = BTreeMap::from([
            ("stargate".to_string(), SERVICE_STARTED),
            (pylon.upstream_backend_name.clone(), SERVICE_STARTED),
        ]);

        let mut client_command = Vec::from(command![
            "--upstream-http-base-url" => format!(
                "http://{}:{MOCK_DYNAMO_HTTP_PORT}",
                pylon.upstream_backend_name
            ),
            "--model-name" => config.model.clone(),
            "--stargate-address" => format!("stargate:{STARGATE_GRPC_PORT}"),
            "--inference-server-id" => pylon.inference_server_id,
        ]);
        if let Some(cluster_id) = pylon.cluster_id {
            client_command.extend(command!["--cluster-id" => cluster_id]);
        }
        client_command.extend(command![
            "--backend-connectivity=reverse",
            "--quic-insecure",
            "--tunnel-protocol" => config.tunnel_protocol.to_string(),
            "--kv-cache-stats-path" => "/kv-cache/stats",
            "--min-update-interval-ms" => "100",
            "--disable-bringup",
            "--active-canary-interval-ms=0",
            "--initial-input-tps" => pylon.last_mean_input_tps.to_string(),
            "--benchmark-pin-input-tps",
        ]);
        if let Some(pylon_queue_admission) = &algorithm.pylon_queue_admission {
            client_command.extend(pylon_queue_admission.pylon_args());
        }

        services.insert(
            format!("client-{backend_index}"),
            ComposeService {
                build: compose_build(&repo_root, &dockerfile, "pylon-runtime"),
                command: client_command,
                ports: Vec::new(),
                volumes: Vec::new(),
                depends_on,
            },
        );
    }

    Ok(ComposeSpec { services })
}

fn absolute_bind_path(path: &Path) -> anyhow::Result<PathBuf> {
    if path.is_absolute() {
        return Ok(path.to_path_buf());
    }
    Ok(std::env::current_dir()
        .context("failed to resolve current directory for compose bind path")?
        .join(path))
}

fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .and_then(Path::parent)
        .expect("crate should live under repo_root/crates/stargate-bench")
        .to_path_buf()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::manifest::generate_manifest;

    fn config() -> BenchmarkConfig {
        serde_yaml_ng::from_str(
            r#"
name: prepare
model: dummy-model
seed: 42
request_count: 5
max_concurrency: 2
backends:
  count: 2
  profile:
    name: balanced
    service_time_ms: { ttft_mean: 150, ttft_jitter_ms: 10, decode_tokens_per_s: 50 }
    registration: { last_mean_input_tps: 100.0 }
traffic_pattern:
  kind: uniform
  routing_keys: 2
  cache_affinity_keys: 2
  input_tokens: { distribution: constant, value: 100 }
  output_tokens: { distribution: constant, value: 20 }
  arrival: { distribution: constant, interval_ms: 10 }
algorithms:
  - { name: power-of-two, config: { default: power-of-two } }
  - { name: random, config: { default: random } }
"#,
        )
        .expect("benchmark config fixture should parse")
    }

    fn command_value<'a>(command: &'a [String], flag: &str) -> Option<&'a str> {
        command
            .windows(2)
            .find(|args| args[0] == flag)
            .map(|args| args[1].as_str())
    }

    fn service<'a>(compose: &'a ComposeSpec, name: &str) -> &'a ComposeService {
        compose
            .services
            .get(name)
            .unwrap_or_else(|| panic!("{name} service should exist"))
    }

    fn compose(config: &BenchmarkConfig) -> ComposeSpec {
        build_compose_spec(
            config,
            &config.algorithms[0],
            Path::new("/tmp/lb-config.json"),
            STARGATE_GRPC_PORT,
            STARGATE_HTTP_PORT,
            STARGATE_METRICS_PORT,
        )
        .expect("compose spec should build")
    }

    fn queue_admission_config() -> crate::config::PylonQueueAdmissionConfig {
        crate::config::PylonQueueAdmissionConfig {
            enabled: false,
            min_delta_ms: Some(0),
            tolerance_factor: Some(1.0),
            retry_after_ms: Some(5),
        }
    }

    #[test]
    fn prepare_suite_writes_per_algorithm_run_dirs() {
        let config = config();
        let manifest = generate_manifest(&config, None).expect("manifest should generate");
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let prepared =
            prepare_suite(&config, &manifest, tempdir.path()).expect("suite should prepare");
        assert_eq!(prepared.algorithm_runs.len(), 2);
        for run in prepared.algorithm_runs {
            assert!(run.compose_path.exists(), "compose file should exist");
            assert!(run.lb_config_path.exists(), "lb config should exist");
            assert!(run.run_info_path.exists(), "run info should exist");
        }
    }

    #[test]
    fn prepare_suite_run_info_preserves_queue_admission_configuration() {
        let mut config = config();
        config.algorithms[0].pylon_queue_admission = Some(queue_admission_config());
        let manifest = generate_manifest(&config, None).expect("manifest should generate");
        let tempdir = tempfile::tempdir().expect("tempdir should create");
        let prepared =
            prepare_suite(&config, &manifest, tempdir.path()).expect("suite should prepare");
        let run = prepared
            .algorithm_runs
            .iter()
            .find(|run| run.algorithm_name == "power-of-two")
            .expect("configured run should exist");
        let run_info: serde_json::Value = serde_json::from_slice(
            &std::fs::read(&run.run_info_path).expect("run info should read"),
        )
        .expect("run info should parse");

        assert_eq!(run_info["pylon_queue_admission"]["enabled"], false);
        assert_eq!(run_info["pylon_queue_admission"]["min_delta_ms"], 0);
        assert_eq!(run_info["pylon_queue_admission"]["tolerance_factor"], 1.0);
        assert_eq!(run_info["pylon_queue_admission"]["retry_after_ms"], 5);
    }

    #[test]
    fn compose_uses_absolute_lb_config_bind_path() {
        let config = config();
        let compose = build_compose_spec(
            &config,
            &config.algorithms[0],
            Path::new(".bench-out/prepare/run-power-of-two/lb-config.json"),
            STARGATE_GRPC_PORT,
            STARGATE_HTTP_PORT,
            STARGATE_METRICS_PORT,
        )
        .expect("compose spec should build");
        let stargate = service(&compose, "stargate");
        let volume = stargate
            .volumes
            .first()
            .expect("stargate should mount lb config");
        let (source, _) = volume
            .split_once(':')
            .expect("bind volume should include source and target");

        assert!(
            Path::new(source).is_absolute(),
            "compose bind source should be absolute, got {source}"
        );
    }

    #[test]
    fn compose_clients_use_explicit_reverse_connectivity() {
        let config = config();
        let compose = compose(&config);
        let stargate = service(&compose, "stargate");
        let client = service(&compose, "client-0");

        assert!(
            stargate
                .command
                .iter()
                .any(|arg| arg == "--backend-connectivity=reverse"),
            "compose stargate should explicitly own the reverse listener"
        );
        assert_eq!(
            command_value(&stargate.command, "--reverse-tunnel-listen-addr"),
            Some("0.0.0.0:50072")
        );
        assert!(
            client
                .command
                .iter()
                .any(|arg| arg == "--backend-connectivity=reverse"),
            "compose pylon should use reverse tunnel so stargate does not connect to container loopback"
        );
        assert!(
            !client
                .command
                .iter()
                .any(|arg| arg.starts_with("--pylon-queue-mismatch-")),
            "unconfigured algorithms should preserve pylon queue admission defaults"
        );
    }

    #[test]
    fn compose_grouped_pylons_share_one_scaled_mock_backend() {
        let mut config = config();
        config.backends.cluster_id_template = Some("cluster-{cluster_index}".to_string());
        config.backends.pylons_per_cluster = 2;
        config.backends.profile.max_concurrent_requests = Some(3);
        config.backends.profile.kv_cache_capacity_tokens = 11;
        let compose = compose(&config);

        let backend = service(&compose, "backend-0");
        assert!(!compose.services.contains_key("backend-1"));
        assert_eq!(
            command_value(&backend.command, "--max-concurrent-requests"),
            Some("6")
        );
        assert_eq!(
            command_value(&backend.command, "--kv-cache-capacity-tokens"),
            Some("22")
        );

        let second_client = service(&compose, "client-1");
        let expected_upstream = format!("http://backend-0:{MOCK_DYNAMO_HTTP_PORT}");
        assert_eq!(
            command_value(&second_client.command, "--upstream-http-base-url"),
            Some(expected_upstream.as_str())
        );
    }

    #[test]
    fn compose_services_include_tunnel_protocol() {
        let mut config = config();
        config.tunnel_protocol = stargate_protocol::TunnelTransportProtocol::WebTransport;
        let compose = compose(&config);
        for name in ["stargate", "client-0"] {
            assert_eq!(
                command_value(&service(&compose, name).command, "--tunnel-protocol"),
                Some("webtransport")
            );
        }
    }

    #[test]
    fn compose_pylons_include_per_algorithm_queue_admission_args() {
        let mut config = config();
        config.algorithms[0].pylon_queue_admission = Some(queue_admission_config());
        let compose = compose(&config);
        let client = service(&compose, "client-0");

        for arg in [
            "--pylon-queue-mismatch-retry-enabled=false",
            "--pylon-queue-mismatch-min-delta-ms=0",
            "--disable-bringup",
            "--active-canary-interval-ms=0",
            "--pylon-queue-mismatch-tolerance-factor=1",
            "--pylon-queue-mismatch-retry-after-ms=5",
        ] {
            assert!(client.command.iter().any(|candidate| candidate == arg));
        }
        assert_eq!(
            command_value(&client.command, "--initial-input-tps"),
            Some("100")
        );
        assert!(
            client
                .command
                .iter()
                .any(|candidate| candidate == "--benchmark-pin-input-tps")
        );
    }
}

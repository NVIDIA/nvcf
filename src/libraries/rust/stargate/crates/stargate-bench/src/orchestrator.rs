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

#[derive(Debug, Clone, Serialize)]
pub struct PreparedSuite {
    pub output_dir: PathBuf,
    pub benchmark_name: String,
    pub seed: u64,
    pub manifest_path: PathBuf,
    pub algorithm_runs: Vec<PreparedAlgorithmRun>,
}

#[derive(Debug, Clone, Serialize)]
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

    let config_path = output_dir.join("benchmark-config.json");
    let config_bytes =
        serde_json::to_vec_pretty(config).context("failed to serialize benchmark config")?;
    std::fs::write(&config_path, config_bytes)
        .with_context(|| format!("failed to write {}", config_path.display()))?;

    let mut runs = Vec::with_capacity(config.algorithms.len());
    for (index, algorithm) in config.algorithms.iter().enumerate() {
        let run_slug = format!("run-{}", slugify(&algorithm.name));
        let run_dir = output_dir.join(run_slug);
        std::fs::create_dir_all(&run_dir)
            .with_context(|| format!("failed to create run dir {}", run_dir.display()))?;

        let lb_config_path = run_dir.join("lb-config.json");
        let lb_config_bytes = serde_json::to_vec_pretty(&algorithm.config)
            .with_context(|| format!("failed to serialize LB config for {}", algorithm.name))?;
        std::fs::write(&lb_config_path, lb_config_bytes)
            .with_context(|| format!("failed to write {}", lb_config_path.display()))?;

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

        let run_info = serde_json::json!({
            "algorithm_name": algorithm.name,
            "pylon_queue_admission": algorithm.pylon_queue_admission,
            "stargate_http_endpoint": format!("http://127.0.0.1:{stargate_http_host_port}"),
            "stargate_grpc_endpoint": format!("127.0.0.1:{stargate_grpc_host_port}"),
            "stargate_metrics_endpoint": format!("http://127.0.0.1:{stargate_metrics_host_port}/metrics"),
            "compose_path": compose_path,
            "lb_config_path": lb_config_path,
            "manifest_path": manifest_path,
        });
        let run_info_path = run_dir.join("run-info.json");
        let run_info_bytes =
            serde_json::to_vec_pretty(&run_info).context("failed to serialize run info")?;
        std::fs::write(&run_info_path, run_info_bytes)
            .with_context(|| format!("failed to write {}", run_info_path.display()))?;

        runs.push(PreparedAlgorithmRun {
            algorithm_name: algorithm.name.clone(),
            run_dir,
            compose_path,
            lb_config_path,
            run_info_path,
            stargate_http_endpoint: format!("http://127.0.0.1:{stargate_http_host_port}"),
            stargate_grpc_endpoint: format!("127.0.0.1:{stargate_grpc_host_port}"),
            stargate_metrics_endpoint: format!(
                "http://127.0.0.1:{stargate_metrics_host_port}/metrics"
            ),
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
    let summary_bytes =
        serde_json::to_vec_pretty(&summary).context("failed to serialize prepared suite")?;
    std::fs::write(&summary_path, summary_bytes)
        .with_context(|| format!("failed to write {}", summary_path.display()))?;

    Ok(summary)
}

#[derive(Debug, Clone, Serialize)]
struct ComposeSpec {
    services: BTreeMap<String, ComposeService>,
}

#[derive(Debug, Clone, Serialize)]
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

#[derive(Debug, Clone, Serialize)]
struct ComposeBuild {
    context: String,
    dockerfile: String,
    target: String,
}

#[derive(Debug, Clone, Serialize)]
struct ComposeDependency {
    condition: String,
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
    let lb_config_bind_path = absolute_bind_path(lb_config_path)?;

    services.insert(
        "stargate".to_string(),
        ComposeService {
            build: ComposeBuild {
                context: repo_root.display().to_string(),
                dockerfile: dockerfile.display().to_string(),
                target: "stargate-runtime".to_string(),
            },
            command: vec![
                "--stargate-id".to_string(),
                "benchmark-stargate".to_string(),
                "--listen-addr".to_string(),
                format!("0.0.0.0:{STARGATE_GRPC_PORT}"),
                "--http-listen-addr".to_string(),
                format!("0.0.0.0:{STARGATE_HTTP_PORT}"),
                "--advertise-addr".to_string(),
                format!("127.0.0.1:{STARGATE_GRPC_PORT}"),
                "--stargate-discovery-dns-name".to_string(),
                "stargate".to_string(),
                "--metrics-port".to_string(),
                STARGATE_METRICS_PORT.to_string(),
                "--lb-config-path".to_string(),
                stargate_lb_config_container_path.to_string(),
                "--tunnel-protocol".to_string(),
                config.tunnel_protocol.to_string(),
            ],
            ports: vec![
                format!("{stargate_grpc_host_port}:{STARGATE_GRPC_PORT}"),
                format!("{stargate_http_host_port}:{STARGATE_HTTP_PORT}"),
                format!("{stargate_metrics_host_port}:{STARGATE_METRICS_PORT}"),
            ],
            volumes: vec![format!(
                "{}:{}:ro",
                lb_config_bind_path.display(),
                stargate_lb_config_container_path
            )],
            depends_on: BTreeMap::new(),
        },
    );

    for backend_index in 0..config.backends.count {
        let pylon = PylonRuntimeSpec::for_backend(config, backend_index);
        let client_name = format!("client-{backend_index}");
        if pylon.owns_upstream_backend() {
            let backend = BackendRuntimeSpec::for_upstream(config, pylon.upstream_index);
            services.insert(
                backend.name.clone(),
                ComposeService {
                    build: ComposeBuild {
                        context: repo_root.display().to_string(),
                        dockerfile: dockerfile.display().to_string(),
                        target: "mock-dynamo-runtime".to_string(),
                    },
                    command: vec![
                        "--http-listen-addr".to_string(),
                        format!("0.0.0.0:{MOCK_DYNAMO_HTTP_PORT}"),
                        "--model-name".to_string(),
                        config.model.clone(),
                        "--num-tokens".to_string(),
                        "32".to_string(),
                        "--token-delay-ms".to_string(),
                        backend.per_token_delay_ms.to_string(),
                        "--decode-jitter-ms".to_string(),
                        backend.decode_jitter_ms.to_string(),
                        "--ttft-ms".to_string(),
                        backend.ttft_ms.to_string(),
                        "--ttft-jitter-ms".to_string(),
                        backend.ttft_jitter_ms.to_string(),
                        "--prefill-tokens-per-s".to_string(),
                        backend.prefill_tokens_per_s.to_string(),
                        "--max-concurrent-requests".to_string(),
                        backend.max_concurrent_requests.to_string(),
                        "--kv-cache-capacity-tokens".to_string(),
                        backend.kv_cache_capacity_tokens.to_string(),
                    ],
                    ports: Vec::new(),
                    volumes: Vec::new(),
                    depends_on: BTreeMap::new(),
                },
            );
        }

        let mut depends_on = BTreeMap::new();
        depends_on.insert(
            "stargate".to_string(),
            ComposeDependency {
                condition: "service_started".to_string(),
            },
        );
        depends_on.insert(
            pylon.upstream_backend_name.clone(),
            ComposeDependency {
                condition: "service_started".to_string(),
            },
        );

        let mut client_command = vec![
            "--upstream-http-base-url".to_string(),
            format!(
                "http://{}:{MOCK_DYNAMO_HTTP_PORT}",
                pylon.upstream_backend_name
            ),
            "--model-name".to_string(),
            config.model.clone(),
            "--stargate-address".to_string(),
            format!("stargate:{STARGATE_GRPC_PORT}"),
            "--inference-server-id".to_string(),
            pylon.inference_server_id,
        ];
        if let Some(cluster_id) = pylon.cluster_id {
            client_command.extend(["--cluster-id".to_string(), cluster_id]);
        }
        client_command.extend([
            "--reverse-tunnel".to_string(),
            "--quic-insecure".to_string(),
            "--tunnel-protocol".to_string(),
            config.tunnel_protocol.to_string(),
            "--kv-cache-stats-path".to_string(),
            "/kv-cache/stats".to_string(),
            "--min-update-interval-ms".to_string(),
            "100".to_string(),
            "--disable-bringup".to_string(),
            "--active-canary-interval-ms=0".to_string(),
            "--benchmark-fixed-last-mean-input-tps".to_string(),
            pylon.last_mean_input_tps.to_string(),
        ]);
        if let Some(pylon_queue_admission) = &algorithm.pylon_queue_admission {
            client_command.extend(pylon_queue_admission.pylon_args());
        }

        services.insert(
            client_name,
            ComposeService {
                build: ComposeBuild {
                    context: repo_root.display().to_string(),
                    dockerfile: dockerfile.display().to_string(),
                    target: "pylon-runtime".to_string(),
                },
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
    use crate::config::{
        AlgorithmConfig, ArrivalPatternConfig, BackendConfig, BackendProfile, DegradationConfig,
        RegistrationConfig, ScenarioMetadata, ServiceTimeConfig, StargateConfig,
        TokenDistributionConfig, TrafficPatternConfig, UniformTrafficConfig,
    };
    use crate::manifest::generate_manifest;

    fn config() -> BenchmarkConfig {
        BenchmarkConfig {
            name: "prepare".to_string(),
            metadata: ScenarioMetadata::default(),
            model: "dummy-model".to_string(),
            seed: Some(42),
            request_count: 5,
            max_concurrency: 2,
            tunnel_protocol: stargate_protocol::TunnelTransportProtocol::Custom,
            stargates: StargateConfig { count: 1 },
            backends: BackendConfig {
                count: 2,
                cluster_id_template: None,
                pylons_per_cluster: 1,
                profiles: Vec::new(),
                profile: BackendProfile {
                    name: "balanced".to_string(),
                    weight: 1.0,
                    max_concurrent_requests: None,
                    kv_cache_capacity_tokens: 0,
                    service_time_ms: ServiceTimeConfig {
                        ttft_mean: 150,
                        ttft_jitter_ms: 10,
                        decode_tokens_per_s: 50,
                        decode_jitter_ms: 0,
                        prefill_tokens_per_s: None,
                    },
                    registration: RegistrationConfig {
                        last_mean_input_tps: 100.0,
                    },
                },
            },
            traffic_pattern: TrafficPatternConfig::Uniform(UniformTrafficConfig {
                routing_keys: 2,
                cache_affinity_keys: 2,
                input_tokens: TokenDistributionConfig::Constant { value: 100 },
                output_tokens: TokenDistributionConfig::Constant { value: 20 },
                arrival: ArrivalPatternConfig::Constant { interval_ms: 10 },
            }),
            degradation: DegradationConfig::default(),
            algorithms: vec![
                AlgorithmConfig {
                    name: "power-of-two".to_string(),
                    config: serde_json::json!({"default": "power-of-two"}),
                    pylon_queue_admission: None,
                },
                AlgorithmConfig {
                    name: "random".to_string(),
                    config: serde_json::json!({"default": "random"}),
                    pylon_queue_admission: None,
                },
            ],
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
        config.algorithms[0].pylon_queue_admission =
            Some(crate::config::PylonQueueAdmissionConfig {
                enabled: false,
                min_delta_ms: Some(0),
                tolerance_factor: Some(1.0),
                retry_after_ms: Some(5),
            });
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
        let stargate = compose
            .services
            .get("stargate")
            .expect("stargate service should exist");
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
    fn compose_clients_use_reverse_tunnel() {
        let config = config();
        let compose = build_compose_spec(
            &config,
            &config.algorithms[0],
            Path::new("/tmp/lb-config.json"),
            STARGATE_GRPC_PORT,
            STARGATE_HTTP_PORT,
            STARGATE_METRICS_PORT,
        )
        .expect("compose spec should build");
        let client = compose
            .services
            .get("client-0")
            .expect("client service should exist");

        assert!(
            client.command.iter().any(|arg| arg == "--reverse-tunnel"),
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
        let compose = build_compose_spec(
            &config,
            &config.algorithms[0],
            Path::new("/tmp/lb-config.json"),
            STARGATE_GRPC_PORT,
            STARGATE_HTTP_PORT,
            STARGATE_METRICS_PORT,
        )
        .expect("compose spec should build");

        let backend = compose
            .services
            .get("backend-0")
            .expect("shared backend service should exist");
        assert!(!compose.services.contains_key("backend-1"));
        assert!(
            backend
                .command
                .windows(2)
                .any(|args| { args[0] == "--max-concurrent-requests" && args[1] == "6" })
        );
        assert!(
            backend
                .command
                .windows(2)
                .any(|args| { args[0] == "--kv-cache-capacity-tokens" && args[1] == "22" })
        );

        let second_client = compose
            .services
            .get("client-1")
            .expect("both pylons should still be rendered");
        assert!(second_client.command.windows(2).any(|args| {
            args[0] == "--upstream-http-base-url"
                && args[1] == format!("http://backend-0:{MOCK_DYNAMO_HTTP_PORT}")
        }));
    }

    #[test]
    fn compose_services_include_tunnel_protocol() {
        let mut config = config();
        config.tunnel_protocol = stargate_protocol::TunnelTransportProtocol::WebTransport;
        let compose = build_compose_spec(
            &config,
            &config.algorithms[0],
            Path::new("/tmp/lb-config.json"),
            STARGATE_GRPC_PORT,
            STARGATE_HTTP_PORT,
            STARGATE_METRICS_PORT,
        )
        .expect("compose spec should build");
        let stargate = compose
            .services
            .get("stargate")
            .expect("stargate service should exist");
        let client = compose
            .services
            .get("client-0")
            .expect("client service should exist");

        assert!(
            stargate
                .command
                .windows(2)
                .any(|args| args[0] == "--tunnel-protocol" && args[1] == "webtransport")
        );
        assert!(
            client
                .command
                .windows(2)
                .any(|args| args[0] == "--tunnel-protocol" && args[1] == "webtransport")
        );
    }

    #[test]
    fn compose_pylons_include_per_algorithm_queue_admission_args() {
        let mut config = config();
        config.algorithms[0].pylon_queue_admission =
            Some(crate::config::PylonQueueAdmissionConfig {
                enabled: false,
                min_delta_ms: Some(0),
                tolerance_factor: Some(1.0),
                retry_after_ms: Some(5),
            });
        let compose = build_compose_spec(
            &config,
            &config.algorithms[0],
            Path::new("/tmp/lb-config.json"),
            STARGATE_GRPC_PORT,
            STARGATE_HTTP_PORT,
            STARGATE_METRICS_PORT,
        )
        .expect("compose spec should build");
        let client = compose
            .services
            .get("client-0")
            .expect("client service should exist");

        assert!(
            client
                .command
                .contains(&"--pylon-queue-mismatch-retry-enabled=false".to_string())
        );
        assert!(
            client
                .command
                .contains(&"--pylon-queue-mismatch-min-delta-ms=0".to_string())
        );
        assert!(client.command.contains(&"--disable-bringup".to_string()));
        assert!(
            client
                .command
                .contains(&"--active-canary-interval-ms=0".to_string())
        );
        assert!(
            client
                .command
                .windows(2)
                .any(|args| args[0] == "--benchmark-fixed-last-mean-input-tps" && args[1] == "100")
        );
        assert!(
            client
                .command
                .contains(&"--pylon-queue-mismatch-tolerance-factor=1".to_string())
        );
        assert!(
            client
                .command
                .contains(&"--pylon-queue-mismatch-retry-after-ms=5".to_string())
        );
    }
}

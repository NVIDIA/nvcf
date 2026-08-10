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
use std::io::Write;
use std::path::{Path, PathBuf};

use super::images::{ImageRefs, tilt_image_matches};
use super::kubectl::Kubectl;
use super::render::{
    RenderManifestConfig, RenderedManifests, StargatePod, render_manifest,
    render_stargate_external_services,
};
use super::run::{BenchmarkK8sRun, prepare_benchmark_k8s_run_with_resolved_dependencies};
use crate::config::{
    AlgorithmConfig, ArrivalPatternConfig, BackendConfig, BackendProfile, BenchmarkConfig,
    DegradationConfig, RegistrationConfig, ScenarioMetadata, ServiceTimeConfig, StargateConfig,
    TokenDistributionConfig, TrafficPatternConfig, UniformTrafficConfig,
};
use serde::Deserialize;
use serde_yaml_ng::Value;

fn config() -> BenchmarkConfig {
    BenchmarkConfig {
        name: "collector".to_string(),
        metadata: ScenarioMetadata::default(),
        model: "dummy-model".to_string(),
        seed: Some(42),
        request_count: 5,
        max_concurrency: 2,
        tunnel_protocol: stargate_protocol::TunnelTransportProtocol::RawQuic,
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
        algorithms: vec![AlgorithmConfig {
            name: "power-of-two".to_string(),
            config: serde_json::json!({"default": "power-of-two"}),
            pylon_queue_admission: None,
        }],
    }
}

fn render_test_manifest(
    config: &BenchmarkConfig,
    algorithm: &AlgorithmConfig,
    stargate_ns: &str,
    backends_ns: &str,
    lb_config_json: &str,
) -> RenderedManifests {
    let images = ImageRefs {
        stargate: "stargate-dev:tilt-test".to_string(),
        mock_dynamo: "mock-dynamo-dev:tilt-test".to_string(),
        pylon: "pylon-dev:tilt-test".to_string(),
    };
    render_manifest(RenderManifestConfig {
        config,
        algorithm,
        image_refs: &images,
        stargate_ns,
        backends_ns,
        lb_config_json,
        http_node_port: 30080,
        metrics_node_port: 31080,
        collector_metrics_node_port: 32080,
    })
}

fn render_default_test_manifest(config: &BenchmarkConfig) -> RenderedManifests {
    render_test_manifest(
        config,
        &config.algorithms[0],
        "sgbench-sg-power",
        "sgbench-be-power",
        r#"{"default":"power-of-two"}"#,
    )
}

fn parse_yaml_documents(manifest: &str) -> Vec<Value> {
    serde_yaml_ng::Deserializer::from_str(manifest)
        .map(|doc| Value::deserialize(doc).expect("manifest document should deserialize"))
        .filter(|doc| !doc.is_null())
        .collect()
}

fn yaml_str_at_path<'a>(value: &'a Value, path: &[&str]) -> Option<&'a str> {
    let mut current = value;
    for segment in path {
        current = current.get(*segment)?;
    }
    current.as_str()
}

fn find_doc_by_kind_and_name<'a>(docs: &'a [Value], kind: &str, name: &str) -> &'a Value {
    docs.iter()
        .find(|doc| {
            yaml_str_at_path(doc, &["kind"]) == Some(kind)
                && yaml_str_at_path(doc, &["metadata", "name"]) == Some(name)
        })
        .unwrap_or_else(|| panic!("expected {kind}/{name} in manifest"))
}

fn first_metric_relabel_regex(job: &Value) -> Option<&str> {
    job.get("metric_relabel_configs")
        .and_then(Value::as_sequence)
        .and_then(|configs| configs.first())
        .and_then(|config| config.get("regex"))
        .and_then(Value::as_str)
}

fn service_port_names(service: &Value) -> Vec<&str> {
    service
        .get("spec")
        .and_then(|spec| spec.get("ports"))
        .and_then(Value::as_sequence)
        .expect("service should contain ports")
        .iter()
        .map(|port| yaml_str_at_path(port, &["name"]).expect("service port should have a name"))
        .collect()
}

struct FakeKubectl {
    _tempdir: tempfile::TempDir,
    program: PathBuf,
    log_path: PathBuf,
}

impl FakeKubectl {
    fn new() -> Self {
        Self::with_body(
            r#"if [[ "$ARGS" == *"get namespace "* ]]; then
  exit 1
elif [[ "$ARGS" == *"get services -l benchmark.stargate/role=pod-metrics"* ]]; then
  printf 'stargate-1-metrics 31082\nstargate-0-metrics 31081\n'
elif [[ "$ARGS" == *"get pods -l app=stargate"* ]]; then
  printf 'stargate-1\nstargate-0\n'
elif [[ "$ARGS" == *"get pods -l app=backend-0-inference-server"* ]]; then
  printf 'backend-0-inference\n'
elif [[ "$ARGS" == *"get pods -l app=backend-0-pylon"* ]]; then
  printf 'backend-0-pylon\n'
elif [[ "$ARGS" == *"get pods -l app=backend-1-inference-server"* ]]; then
  :
elif [[ "$ARGS" == *"get pods -l app=backend-1-pylon"* ]]; then
  :
elif [[ "$ARGS" == *"get pods -o wide"* ]]; then
  printf 'NAME READY\nstargate-0 1/1\n'
elif [[ "$ARGS" == *"describe pods"* ]]; then
  printf 'pod description\n'
elif [[ "$ARGS" == *"get events --sort-by=.lastTimestamp"* ]]; then
  printf 'event stream\n'
elif [[ "$ARGS" == *"logs -l "* ]]; then
  printf 'logs for %s\n' "$ARGS"
elif [[ "$ARGS" == "config current-context" ]]; then
  printf 'kind-kind\n'
elif [[ "$ARGS" == *"get nodes -o "*"ExternalIP"* ]]; then
  printf '203.0.113.9\n'
elif [[ "$ARGS" == *"get nodes -o "*"InternalIP"* ]]; then
  printf '10.0.0.4\n'
fi
"#,
        )
    }

    fn with_body(body: &str) -> Self {
        let tempdir = tempfile::tempdir().expect("fake kubectl tempdir should create");
        let program = tempdir.path().join("kubectl");
        let pending_program = tempdir.path().join("kubectl.pending");
        let log_path = tempdir.path().join("kubectl.log");
        let mut script = String::from("#!/usr/bin/env bash\nset -euo pipefail\n");
        script.push_str(&format!(
            "LOG={}\n",
            shell_single_quote(&log_path.display().to_string())
        ));
        script.push_str("ARGS=\"$*\"\nprintf '%s\\n' \"$ARGS\" >> \"$LOG\"\n");
        script.push_str(body);
        {
            let mut file =
                fs::File::create(&pending_program).expect("fake kubectl script should create");
            file.write_all(script.as_bytes())
                .expect("fake kubectl script should write");
            file.sync_all().expect("fake kubectl script should sync");
        }
        fs::rename(&pending_program, &program).expect("fake kubectl script should publish");
        Self {
            _tempdir: tempdir,
            program,
            log_path,
        }
    }

    fn runner(&self) -> Kubectl {
        Kubectl::with_args("bash", [self.program.clone()])
    }

    fn command_log(&self) -> String {
        read_utf8(&self.log_path, "fake kubectl log should read")
    }
}

fn shell_single_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\\''"))
}

fn read_utf8(path: &Path, context: &str) -> String {
    fs::read_to_string(path).expect(context)
}

fn assert_contains_all(haystack: &str, needles: &[&str]) {
    for needle in needles {
        assert!(haystack.contains(needle), "missing `{needle}`");
    }
}

fn assert_error_contains<T, E: std::fmt::Display>(result: Result<T, E>, expected: &str) {
    let error = result.err().expect("operation should fail");
    assert!(
        error.to_string().contains(expected),
        "unexpected error: {error}"
    );
}

fn resolve_nodeport_host(body: &str, override_host: Option<&str>) -> String {
    FakeKubectl::with_body(body)
        .runner()
        .resolve_nodeport_host_with_override(override_host.map(str::to_owned))
        .expect("nodeport host should resolve")
}

fn benchmark_run(run_dir: &Path) -> BenchmarkK8sRun {
    fs::create_dir_all(run_dir).expect("run dir should create");
    for manifest in [
        "k8s-stargate-manifest.yaml",
        "k8s-backends-manifest.yaml",
        "stargate-external-services.yaml",
    ] {
        fs::write(run_dir.join(manifest), "kind: List\n").expect("manifest should write");
    }
    BenchmarkK8sRun {
        algorithm_name: "power-of-two".to_string(),
        manifest_path: run_dir.join("manifest.json"),
        run_dir: run_dir.to_path_buf(),
        stargate_ns: "sgbench-sg-power-of-two".to_string(),
        backends_ns: "sgbench-be-power-of-two".to_string(),
        stargate_count: 2,
        nodeport_host: "node.test".to_string(),
        stargate_http_endpoint: "http://node.test:30080".to_string(),
        stargate_metrics_endpoint: "http://node.test:31080/metrics".to_string(),
        collector_metrics_endpoint: "http://node.test:32080/metrics".to_string(),
        backend_upstream_indices: vec![0, 1],
        upstream_backend_indices: vec![0, 1],
    }
}

#[test]
fn prepare_benchmark_k8s_run_writes_split_manifests_and_run_info() {
    let tempdir = tempfile::tempdir().expect("tempdir should create");
    let manifest_path = tempdir.path().join("manifest.json");
    fs::write(&manifest_path, "{}\n").expect("manifest placeholder should write");
    let mut config = config();
    config.stargates.count = 2;
    config.backends.pylons_per_cluster = 2;
    config.backends.cluster_id_template = Some("cluster-{cluster_index}".to_string());
    let images = ImageRefs {
        stargate: "registry.test/stargate-dev:tilt-test".to_string(),
        mock_dynamo: "registry.test/mock-dynamo-dev:tilt-test".to_string(),
        pylon: "registry.test/pylon-dev:tilt-test".to_string(),
    };

    let run = prepare_benchmark_k8s_run_with_resolved_dependencies(
        &config,
        &config.algorithms[0],
        &manifest_path,
        tempdir.path(),
        2,
        "node.example".to_string(),
        images,
    )
    .expect("benchmark k8s run should prepare");

    assert_eq!(run.stargate_ns, "sgbench-sg-power-of-two");
    assert_eq!(run.backends_ns, "sgbench-be-power-of-two");
    assert_eq!(run.stargate_http_endpoint, "http://node.example:30082");
    assert_eq!(
        run.stargate_metrics_endpoint,
        "http://node.example:31082/metrics"
    );
    assert_eq!(
        run.collector_metrics_endpoint,
        "http://node.example:32082/metrics"
    );
    assert_eq!(run.backend_upstream_indices, vec![0, 0]);
    assert_eq!(run.upstream_backend_indices, vec![0]);

    let full_manifest = read_utf8(
        &run.run_dir.join("k8s-manifest.yaml"),
        "manifest should read",
    );
    let stargate_manifest = read_utf8(
        &run.run_dir.join("k8s-stargate-manifest.yaml"),
        "stargate manifest should read",
    );
    let backends_manifest = read_utf8(
        &run.run_dir.join("k8s-backends-manifest.yaml"),
        "backend manifest should read",
    );
    assert_contains_all(&full_manifest, &["registry.test/stargate-dev:tilt-test"]);
    assert_contains_all(&stargate_manifest, &["nodePort: 30082", "nodePort: 32082"]);
    assert!(backends_manifest.contains("- --cluster-id=cluster-0"));
    assert!(!backends_manifest.contains("- --cluster-id=cluster-1"));

    let run_info: serde_json::Value = serde_json::from_str(&read_utf8(
        &run.run_dir.join("run-info.json"),
        "run info should read",
    ))
    .expect("run info should parse");
    assert_eq!(run_info["algorithm_name"], "power-of-two");
    assert_eq!(run_info["http_node_port"], 30082);
    assert_eq!(
        run_info["stargate_http_endpoint"],
        "http://node.example:30082"
    );
    assert_eq!(
        run_info["manifest_path"],
        manifest_path.display().to_string()
    );
}

#[test]
fn kubectl_runner_executes_readiness_and_maintenance_commands() {
    let fake = FakeKubectl::new();
    let kubectl = fake.runner();
    let tempdir = tempfile::tempdir().expect("tempdir should create");
    let run = benchmark_run(&tempdir.path().join("run-power-of-two"));

    kubectl.apply(&run).expect("stargate manifest should apply");
    kubectl
        .wait_ready(&run, 2)
        .expect("benchmark resources should become ready");
    let endpoints = kubectl
        .stargate_metrics_endpoints(&run)
        .expect("metrics endpoints should resolve");
    kubectl
        .delete_backend_pod(&run, 1)
        .expect("backend pod delete should run");
    kubectl
        .scale_backend(&run, 0, 3)
        .expect("backend scale should run");
    kubectl
        .collect_logs(&run)
        .expect("logs should collect from discovered pods");
    kubectl.delete(&run).expect("manifests should delete");

    assert_eq!(
        endpoints,
        vec![
            "http://node.test:31081/metrics".to_string(),
            "http://node.test:31082/metrics".to_string()
        ]
    );
    let external_services = read_utf8(
        &run.run_dir.join("stargate-external-services.yaml"),
        "external services should read",
    );
    let docs = parse_yaml_documents(&external_services);
    find_doc_by_kind_and_name(&docs, "Service", "stargate-0-external");
    find_doc_by_kind_and_name(&docs, "Service", "stargate-1-metrics");

    let stargate_log = read_utf8(
        &run.run_dir.join("logs/stargate.log"),
        "stargate log should read",
    );
    assert_contains_all(
        &stargate_log,
        &[
            "status: exit status: 0",
            "logs for -n sgbench-sg-power-of-two logs",
        ],
    );
    let backend_log = read_utf8(
        &run.run_dir.join("logs/backend-0-inference-server.log"),
        "backend log should read",
    );
    assert!(backend_log.contains("app=backend-0-inference-server"));

    let command_log = fake.command_log();
    assert_contains_all(
        &command_log,
        &[
            "rollout status statefulset/stargate --timeout=180s",
            "delete pod -l app=backend-1-inference-server",
            "scale deployment/backend-0-inference-server --replicas=3",
            "delete -f",
        ],
    );
}

#[test]
fn kubectl_runner_reports_failed_operations_and_inconsistent_snapshots() {
    let fake = FakeKubectl::with_body(
        r#"if [[ "$ARGS" == *"get namespace "* ]]; then
  exit 1
elif [[ "$ARGS" == "apply -f "* ]]; then
  exit 1
elif [[ "$ARGS" == "delete -f "* ]]; then
  exit 1
elif [[ "$ARGS" == *"delete pod -l "* ]]; then
  exit 1
elif [[ "$ARGS" == *"scale deployment/"* ]]; then
  exit 1
elif [[ "$ARGS" == *"get services -l benchmark.stargate/role=pod-metrics"* ]]; then
  printf 'stargate-0-metrics 31081\n'
elif [[ "$ARGS" == *"get pods -l app=stargate"* ]]; then
  printf 'stargate-0\n'
fi
"#,
    );
    let kubectl = fake.runner();
    let tempdir = tempfile::tempdir().expect("tempdir should create");
    let run = benchmark_run(&tempdir.path().join("run-power-of-two"));

    assert_error_contains(kubectl.apply(&run), "kubectl apply failed");
    assert_error_contains(kubectl.delete(&run), "kubectl delete failed");
    assert_error_contains(
        kubectl.delete_backend_pod(&run, 0),
        "kubectl delete pod failed",
    );
    assert_error_contains(kubectl.scale_backend(&run, 0, 3), "kubectl scale failed");
    assert_error_contains(
        kubectl.stargate_metrics_endpoints(&run),
        "expected 2 per-stargate metrics endpoints but found 1",
    );
    assert_error_contains(
        kubectl.wait_ready(&run, 2),
        "expected 2 stargate pods but found 1",
    );
}

#[test]
fn kubectl_resolves_nodeport_host_from_override_context_or_node_addresses() {
    assert_eq!(
        resolve_nodeport_host("", Some(" node.override.test ")),
        "node.override.test"
    );

    assert_eq!(
        resolve_nodeport_host(
            r#"if [[ "$ARGS" == "config current-context" ]]; then
  printf 'docker-desktop\n'
fi
"#,
            None,
        ),
        "127.0.0.1"
    );

    assert_eq!(
        resolve_nodeport_host(
            r#"if [[ "$ARGS" == "config current-context" ]]; then
  printf 'kind-kind\n'
elif [[ "$ARGS" == *"get nodes -o "*"ExternalIP"* ]]; then
  printf '203.0.113.10\n'
fi
"#,
            None,
        ),
        "203.0.113.10"
    );

    assert_eq!(
        resolve_nodeport_host(
            r#"if [[ "$ARGS" == "config current-context" ]]; then
  printf 'kind-kind\n'
elif [[ "$ARGS" == *"get nodes -o "*"InternalIP"* ]]; then
  printf '10.0.0.8\n'
fi
"#,
            None,
        ),
        "10.0.0.8"
    );
}

#[test]
fn rendered_benchmark_manifest_includes_otel_prometheus_scraper() {
    let config = config();
    let rendered = render_default_test_manifest(&config);

    let docs = parse_yaml_documents(&rendered.stargate);
    let collector_sa = find_doc_by_kind_and_name(&docs, "ServiceAccount", "otel-collector");
    assert_eq!(
        yaml_str_at_path(collector_sa, &["metadata", "namespace"]),
        Some("sgbench-sg-power")
    );

    let collector_service = find_doc_by_kind_and_name(&docs, "Service", "otel-collector");
    assert_eq!(
        yaml_str_at_path(collector_service, &["spec", "type"]),
        Some("NodePort")
    );

    let collector_config = find_doc_by_kind_and_name(&docs, "ConfigMap", "otel-collector-config");
    let collector_yaml = yaml_str_at_path(collector_config, &["data", "otel-collector.yaml"])
        .expect("collector config should include otel-collector.yaml");
    let collector_cfg: Value =
        serde_yaml_ng::from_str(collector_yaml).expect("collector config yaml should parse");
    let scrape_configs = collector_cfg
        .get("receivers")
        .and_then(|receivers| receivers.get("prometheus"))
        .and_then(|prometheus| prometheus.get("config"))
        .and_then(|config| config.get("scrape_configs"))
        .and_then(Value::as_sequence)
        .expect("collector config should contain scrape_configs");
    let stargate_job = scrape_configs
        .iter()
        .find(|job| yaml_str_at_path(job, &["job_name"]) == Some("stargate"))
        .expect("collector config should include stargate scrape job");
    assert_eq!(
        first_metric_relabel_regex(stargate_job),
        Some(
            "stargate_requests_total|stargate_proxy_retries_total|stargate_proxy_retry_exhausted_total|stargate_routing_selections_total|stargate_routing_kv_free_token_fallback_selections_total|stargate_proxy_duration_seconds_.+|stargate_routing_duration_seconds_.+|stargate_active_inference_servers"
        )
    );
    let client_job = scrape_configs
        .iter()
        .find(|job| yaml_str_at_path(job, &["job_name"]) == Some("pylon"))
        .expect("collector config should include pylon scrape job");
    assert_eq!(
        first_metric_relabel_regex(client_job),
        Some("target_info|pylon_requests_total|pylon_.+")
    );
}

#[test]
fn rendered_benchmark_manifest_does_not_expose_list_models_via_headless_service() {
    let config = config();
    let rendered = render_default_test_manifest(&config);

    let docs = parse_yaml_documents(&rendered.stargate);
    let headless = find_doc_by_kind_and_name(&docs, "Service", "stargate-headless");
    let ports = service_port_names(headless);
    assert!(
        !ports.contains(&"model-discovery"),
        "benchmark headless service must not expose local-only ListModels"
    );
}

#[test]
fn tilt_image_match_accepts_unprefixed_and_prefixed_repositories() {
    assert!(tilt_image_matches("stargate-dev:tilt-123", "stargate-dev"));
    assert!(tilt_image_matches(
        "stargate-dev:tilt-bench-abc123-20260422000000",
        "stargate-dev"
    ));
    assert!(tilt_image_matches(
        "localhost:5001/stargate-dev:tilt-123",
        "stargate-dev"
    ));
    assert!(tilt_image_matches(
        "gcr.io/project/stargate-dev:tilt-bench-123",
        "stargate-dev"
    ));
    assert!(!tilt_image_matches(
        "localhost:5001/not-stargate-dev:tilt-123",
        "stargate-dev"
    ));
    assert!(!tilt_image_matches("stargate-dev:latest", "stargate-dev"));
}

#[test]
fn external_services_include_per_pod_metrics_nodeport() {
    let manifest = render_stargate_external_services(
        "sgbench-sg-test",
        &[StargatePod {
            name: "stargate-0".to_string(),
        }],
    );

    let docs = parse_yaml_documents(&manifest);
    assert_eq!(
        docs.len(),
        2,
        "expected one external and one metrics service"
    );
    let external = find_doc_by_kind_and_name(&docs, "Service", "stargate-0-external");
    let metrics = find_doc_by_kind_and_name(&docs, "Service", "stargate-0-metrics");
    assert_eq!(
        yaml_str_at_path(
            external,
            &["spec", "selector", "statefulset.kubernetes.io/pod-name"]
        ),
        Some("stargate-0")
    );
    assert_eq!(
        yaml_str_at_path(metrics, &["metadata", "labels", "benchmark.stargate/role"]),
        Some("pod-metrics")
    );
    assert_eq!(
        yaml_str_at_path(metrics, &["spec", "type"]),
        Some("NodePort")
    );
    let external_ports = service_port_names(external);
    assert!(
        !external_ports.contains(&"model-discovery"),
        "per-pod external services must not expose local-only ListModels"
    );

    let has_metrics_port = metrics
        .get("spec")
        .and_then(|spec| spec.get("ports"))
        .and_then(Value::as_sequence)
        .map(|ports| {
            ports.iter().any(|port| {
                yaml_str_at_path(port, &["name"]) == Some("metrics")
                    && yaml_str_at_path(port, &["targetPort"]) == Some("metrics")
            })
        })
        .unwrap_or(false);
    assert!(
        has_metrics_port,
        "metrics service should expose targetPort=metrics"
    );
}

#[test]
fn rendered_grouped_pylons_share_one_scaled_mock_backend() {
    let mut config = config();
    config.backends.cluster_id_template = Some("cluster-{cluster_index}".to_string());
    config.backends.pylons_per_cluster = 2;
    config.backends.profile.max_concurrent_requests = Some(3);
    config.backends.profile.kv_cache_capacity_tokens = 11;
    let rendered = render_default_test_manifest(&config);

    assert_contains_all(
        &rendered.backends,
        &[
            "- --cluster-id=cluster-0",
            "name: backend-0-http",
            "- --upstream-http-base-url=http://backend-0-http.sgbench-be-power.svc.cluster.local:8090",
            "- --max-concurrent-requests=6",
            "- --kv-cache-capacity-tokens=22",
        ],
    );
    assert!(!rendered.backends.contains("- --cluster-id=cluster-1"));
    assert!(!rendered.backends.contains("name: backend-1-http"));
    assert!(!rendered.backends.contains(
        "- --upstream-http-base-url=http://backend-1-http.sgbench-be-power.svc.cluster.local:8090"
    ));
}

#[test]
fn rendered_manifests_include_tunnel_protocol() {
    let mut config = config();
    config.tunnel_protocol = stargate_protocol::TunnelTransportProtocol::WebTransport;
    let rendered = render_default_test_manifest(&config);

    assert_contains_all(&rendered.stargate, &["- --tunnel-protocol=webtransport"]);
    assert_contains_all(&rendered.backends, &["- --tunnel-protocol=webtransport"]);
}

#[test]
fn rendered_pylons_include_per_algorithm_queue_admission_args() {
    let config = config();
    let algorithm = AlgorithmConfig {
        name: "queue-admission-enabled".to_string(),
        config: serde_json::json!({"default": "groq-multiregion"}),
        pylon_queue_admission: Some(crate::config::PylonQueueAdmissionConfig {
            enabled: true,
            min_delta_ms: Some(0),
            tolerance_factor: Some(1.0),
            retry_after_ms: Some(5),
        }),
    };
    let rendered = render_test_manifest(
        &config,
        &algorithm,
        "sgbench-sg-queue",
        "sgbench-be-queue",
        r#"{"default":"groq-multiregion"}"#,
    );

    assert_contains_all(
        &rendered.backends,
        &[
            "- --pylon-queue-mismatch-retry-enabled=true",
            "- --pylon-queue-mismatch-min-delta-ms=0",
            "- --disable-bringup",
            "- --active-canary-interval-ms=0",
            "- --initial-input-tps=100",
            "- --benchmark-pin-input-tps",
            "- --pylon-queue-mismatch-tolerance-factor=1",
            "- --pylon-queue-mismatch-retry-after-ms=5",
        ],
    );
}

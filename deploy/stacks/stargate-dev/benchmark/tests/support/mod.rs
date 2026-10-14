// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

mod command;

use serde_json::{Value, json};
use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::OnceLock;

pub struct FakeCommand {
    directory: tempfile::TempDir,
}

/// Compile the stand-in once per test binary, next to that binary. The rustup
/// proxy keeps the toolchain Cargo selected through `RUSTUP_TOOLCHAIN`.
fn compiled() -> &'static Path {
    static COMPILED: OnceLock<PathBuf> = OnceLock::new();
    COMPILED.get_or_init(|| {
        let test_binary = std::env::current_exe().unwrap();
        let name = test_binary.file_name().unwrap().to_string_lossy();
        let compiled = test_binary.with_file_name(format!("{name}-fake-command"));
        let staged = test_binary.with_file_name(format!("{name}-fake-command.tmp"));
        let source = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/support/command.rs");
        let output = Command::new("rustc")
            .args(["--edition=2024", "-C", "debuginfo=0"])
            .arg(source)
            .arg("-o")
            .arg(&staged)
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        fs::rename(&staged, &compiled).unwrap();
        compiled
    })
}

impl FakeCommand {
    pub fn new() -> Self {
        // The stand-in keeps per-test state beside its own path, so each test
        // needs a private link. A hard link avoids holding a writable descriptor
        // that a concurrently forked child could inherit (ETXTBSY).
        let compiled = compiled();
        let directory = tempfile::Builder::new()
            .prefix("fake-command-")
            .tempdir_in(compiled.parent().unwrap())
            .unwrap();
        fs::hard_link(compiled, directory.path().join("command")).unwrap();
        Self { directory }
    }

    pub fn executable(&self) -> PathBuf {
        self.directory.path().join("command")
    }

    pub fn respond(&self, arguments: &[&str], responses: &[(i32, &str, &str)]) {
        assert!(!responses.is_empty());
        let arguments: Vec<OsString> = arguments.iter().map(OsString::from).collect();
        let directory = self
            .directory
            .path()
            .join("responses")
            .join(command::key(&arguments));
        fs::create_dir_all(&directory).unwrap();
        fs::write(
            directory.join("arguments"),
            command::arguments_bytes(&arguments),
        )
        .unwrap();
        Self::write_responses(&directory, responses);
    }

    pub fn default_response(&self, responses: &[(i32, &str, &str)]) {
        assert!(!responses.is_empty());
        let directory = self.directory.path().join("responses/default");
        fs::create_dir_all(&directory).unwrap();
        Self::write_responses(&directory, responses);
    }

    fn write_responses(directory: &Path, responses: &[(i32, &str, &str)]) {
        fs::write(directory.join("count"), responses.len().to_string()).unwrap();
        for (index, (status, stdout, stderr)) in responses.iter().enumerate() {
            fs::write(directory.join(format!("{index}.out")), stdout).unwrap();
            fs::write(directory.join(format!("{index}.err")), stderr).unwrap();
            fs::write(directory.join(format!("{index}.code")), status.to_string()).unwrap();
        }
    }

    pub fn calls(&self) -> Vec<Vec<OsString>> {
        use std::os::unix::ffi::OsStringExt;
        let mut paths: Vec<_> = fs::read_dir(self.directory.path())
            .unwrap()
            .map(|entry| entry.unwrap().path())
            .filter(|path| {
                path.file_name()
                    .unwrap()
                    .to_string_lossy()
                    .starts_with("call-")
            })
            .collect();
        paths.sort();
        paths
            .into_iter()
            .map(|path| {
                let bytes = fs::read(path).unwrap();
                if bytes.is_empty() {
                    return Vec::new();
                }
                bytes[..bytes.len() - 1]
                    .split(|byte| *byte == 0)
                    .map(|part| OsString::from_vec(part.to_vec()))
                    .collect()
            })
            .collect()
    }
}

/// Respond to a `kubectl` read made with the controller's 30-second timeout.
pub fn kubectl(
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

/// A ready MockDC Pod whose Pylon has the given inference-server ID.
pub fn mockdc_pod(name: &str, identity: &str) -> Value {
    json!({
        "metadata": {"name": name},
        "spec": {"containers": [
            {"name": "mock-dynamo", "args": []},
            {"name": "pylon", "args": [format!("--inference-server-id={identity}"),
                "--engine-stats-stream=off", "--max-engine-concurrency=17", "--do-calibration",
                "--calibration-max-concurrency=13", "--active-canary-interval-ms=0"]}
        ]},
        "status": {"containerStatuses": [
            {"name": "mock-dynamo", "ready": true}, {"name": "pylon", "ready": true}
        ]}
    })
}

/// Respond to `verify --region us-west-2` reads for a healthy region: the hub
/// Deployments and registration Service, three routers that each see the
/// four backends, and two calibrated MockDC Pods per MockDC cluster.
pub fn healthy_west_region(fake: &FakeCommand) {
    let (hub, namespace, model) = ("stargate-usw2", "stargate-dev", "stargate-dev-model");
    for (name, replicas) in [
        ("stargate-dev-auth", 1),
        ("llm-request-router", 3),
        ("llm-request-router-backend-router", 3),
    ] {
        let deployment = json!({
            "metadata": {"generation": 1}, "spec": {"replicas": replicas},
            "status": {"observedGeneration": 1, "readyReplicas": replicas,
                "updatedReplicas": replicas, "availableReplicas": replicas}
        });
        kubectl(
            fake,
            hub,
            namespace,
            &["get", "deployment", name, "-o", "json"],
            &[(0, &deployment.to_string(), "")],
        );
    }
    let service = json!({
        "spec": {"type": "LoadBalancer", "ports": [
            {"port": 50071, "protocol": "TCP"}, {"port": 50072, "protocol": "UDP"}
        ]},
        "status": {"loadBalancer": {"ingress": [{"hostname": "example.invalid"}]}}
    });
    kubectl(
        fake,
        hub,
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
    let routers: Vec<_> = (0..3)
        .map(|index| json!({"metadata": {"name": format!("router-{index}")}}))
        .collect();
    kubectl(
        fake,
        hub,
        namespace,
        &[
            "get",
            "pods",
            "-l",
            "app.kubernetes.io/name=llm-request-router,app.kubernetes.io/instance=llm-request-router",
            "-o",
            "json",
        ],
        &[(0, &json!({"items": routers}).to_string(), "")],
    );
    let membership = format!(
        "stargate_active_inference_servers{{model=\"{model}\",routing_key=\"stargate-dev\"}} 4\n"
    );
    for index in 0..3 {
        let path =
            format!("--raw=/api/v1/namespaces/{namespace}/pods/router-{index}:9090/proxy/metrics");
        kubectl(
            fake,
            hub,
            namespace,
            &["get", &path],
            &[(0, &membership, "")],
        );
    }
    let calibration = format!(
        concat!(
            "pylon_engine_stats_stream_events_total 0\n",
            "pylon_engine_stats_stream_connected 0\n",
            "pylon_model_calibration_duration_ms_count{{model=\"{0}\",outcome=\"completed\"}} 1\n",
            "pylon_model_last_mean_input_tps{{model=\"{0}\"}} 321\n",
            "pylon_model_stats_source{{model=\"{0}\",source=\"engine_stats_stream\"}} 0\n",
        ),
        model
    );
    for mockdc in ["mockdc-usw2-a", "mockdc-usw2-b"] {
        let pods: Vec<_> = (0..2)
            .map(|index| {
                mockdc_pod(
                    &format!("backend-{index}"),
                    &format!("{mockdc}-backend-{index}"),
                )
            })
            .collect();
        let selector = format!("app.kubernetes.io/instance={mockdc}");
        kubectl(
            fake,
            mockdc,
            namespace,
            &["get", "pods", "-l", &selector, "-o", "json"],
            &[(0, &json!({"items": pods}).to_string(), "")],
        );
        for index in 0..2 {
            let path = format!(
                "--raw=/api/v1/namespaces/{namespace}/pods/backend-{index}:9089/proxy/metrics"
            );
            kubectl(
                fake,
                mockdc,
                namespace,
                &["get", &path],
                &[(0, &calibration, "")],
            );
        }
    }
}

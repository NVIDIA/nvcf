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

use std::net::TcpListener;
use std::path::Path;
use std::process::Command;

use serde::Deserialize;

#[allow(dead_code)]
mod built_info {
    include!(concat!(env!("OUT_DIR"), "/built.rs"));
}

#[test]
fn native_stargate_version_reports_source_identity() {
    let expected_version = built_info::GIT_COMMIT_HASH.unwrap_or("unknown");

    let output = Command::new(env!("CARGO_BIN_EXE_stargate"))
        .arg("--version")
        .output()
        .expect("stargate process should start");

    assert!(
        output.status.success(),
        "stargate --version failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    assert_eq!(
        String::from_utf8(output.stdout).expect("version output should be UTF-8"),
        format!("stargate {expected_version}\n"),
    );
}

#[test]
fn stargate_help_presents_config_file_as_a_complete_source() {
    let output = Command::new(env!("CARGO_BIN_EXE_stargate"))
        .arg("--help")
        .output()
        .expect("stargate process should start");

    assert!(output.status.success());
    let stdout = String::from_utf8(output.stdout).expect("help output should be UTF-8");
    assert!(
        stdout.starts_with("Usage: stargate [OPTIONS]\n"),
        "{stdout}"
    );
    assert!(stdout.contains("--config-file <PATH>"), "{stdout}");
    assert!(
        stdout.contains("Legacy CLI and environment configuration is deprecated"),
        "{stdout}"
    );
}

#[test]
fn config_file_resolves_environment_and_ignores_legacy_inputs() {
    let occupied_listener = TcpListener::bind("127.0.0.1:0").expect("listener should bind");
    let occupied_addr = occupied_listener
        .local_addr()
        .expect("listener address should resolve");
    let config = tempfile::NamedTempFile::new().expect("config file should be created");
    std::fs::write(
        config.path(),
        format!(
            r#"schema_version = 1

[stargate_identity]
id = {{ env = "STARGATE_CONFIG_TEST_ID" }}

[stargate_network]
grpc_listen_addr = "{occupied_addr}"
model_discovery_listen_addr = "127.0.0.1:0"
http_listen_addr = "127.0.0.1:0"
advertise_addr = {{ env = "STARGATE_CONFIG_TEST_ADVERTISE_ADDR" }}

[observability.metrics]
listen_addr = "127.0.0.1:0"
"#
        ),
    )
    .expect("config file should be writable");

    let output = Command::new(env!("CARGO_BIN_EXE_stargate"))
        .arg("--config-file")
        .arg(config.path())
        .arg("--unknown-legacy-option")
        .env("STARGATE_CONFIG_TEST_ID", "from-environment")
        .env("STARGATE_CONFIG_TEST_ADVERTISE_ADDR", "127.0.0.1:50071")
        .env("STARGATE_DIRECT_QUIC_CONNECTIONS", "0")
        .output()
        .expect("stargate process should start");

    assert!(
        !output.status.success(),
        "occupied listener should stop startup"
    );
    let stderr = String::from_utf8(output.stderr).expect("stderr should be UTF-8");
    assert!(stderr.contains("failed to bind gRPC listener"), "{stderr}");
    assert!(!stderr.contains("deprecated"), "{stderr}");
}

#[test]
fn checked_in_helm_manifest_contains_valid_stargate_configuration() {
    let repository_root = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../../../../..")
        .canonicalize()
        .expect("repository root should resolve");
    let manifest_path = repository_root.join("deploy/helm/llm-request-router/bin/manifest.yaml");
    let manifest = std::fs::read_to_string(&manifest_path)
        .unwrap_or_else(|error| panic!("failed to read {}: {error}", manifest_path.display()));
    let mut rendered_config = None;
    for document in serde_yaml_ng::Deserializer::from_str(&manifest) {
        let document = serde_yaml_ng::Value::deserialize(document)
            .expect("rendered manifest should be valid YAML");
        let is_stargate_config = document.get("kind").and_then(|value| value.as_str())
            == Some("ConfigMap")
            && document
                .get("metadata")
                .and_then(|metadata| metadata.get("name"))
                .and_then(|value| value.as_str())
                == Some("llm-request-router-stargate");
        if is_stargate_config {
            assert_eq!(
                document.get("apiVersion").and_then(|value| value.as_str()),
                Some("v1"),
                "Stargate ConfigMap should retain its API version"
            );
            rendered_config = document
                .get("data")
                .and_then(|data| data.get("stargate.toml"))
                .and_then(|value| value.as_str())
                .map(str::to_owned);
            break;
        }
    }
    let rendered_config = rendered_config.expect("Stargate ConfigMap should contain stargate.toml");
    let resolved_config = rendered_config
        .replace(r#"{ env = "POD_NAME" }"#, r#""llm-request-router-0""#)
        .replace(r#"{ env = "POD_NAMESPACE" }"#, r#""nvcf""#)
        .replace(
            r#"{ env = "STARGATE_ADVERTISE_ADDR" }"#,
            r#""10.0.0.1:50071""#,
        );

    let config = stargate::config::StargateConfig::from_toml_str(&resolved_config)
        .expect("rendered Stargate configuration should satisfy the Rust schema");
    assert!(config.stargate_discovery.kubernetes_pods.is_some());
    assert!(config.pylon_transport.reverse.is_some());
}

#[test]
fn invalid_stargate_runtime_listen_addr_exits_nonzero() {
    let status = Command::new(env!("CARGO_BIN_EXE_stargate"))
        .args([
            "--stargate-id",
            "test-stargate",
            "--advertise-addr",
            "127.0.0.1:50071",
            "--stargate-discovery-dns-name",
            "stargate-headless",
            "--listen-addr",
            "not-a-socket-addr",
        ])
        .status()
        .expect("stargate process should start");

    assert!(
        !status.success(),
        "stargate should reject invalid runtime listen addresses"
    );
}

#[test]
fn list_models_probe_invalid_endpoint_exits_nonzero() {
    let status = Command::new(env!("CARGO_BIN_EXE_stargate-list-models-probe"))
        .args([
            "--addr",
            "http://[",
            "--expect",
            "model-a",
            "--attempts",
            "1",
            "--interval-ms",
            "0",
        ])
        .status()
        .expect("stargate-list-models-probe process should start");

    assert!(
        !status.success(),
        "list-models probe should reject invalid discovery endpoints"
    );
}

#[test]
fn watch_stargates_probe_invalid_endpoint_exits_nonzero() {
    let status = Command::new(env!("CARGO_BIN_EXE_stargate-watch-stargates-probe"))
        .args([
            "--addr",
            "http://[",
            "--expect-id",
            "stargate-a",
            "--attempts",
            "1",
            "--interval-ms",
            "0",
        ])
        .status()
        .expect("stargate-watch-stargates-probe process should start");

    assert!(
        !status.success(),
        "watch-stargates probe should reject invalid control-plane endpoints"
    );
}

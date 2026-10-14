// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use serde_json::Value;
use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::process::Command;

fn benchmark() -> Command {
    let mut command = Command::new(env!("CARGO_BIN_EXE_stargate-dev-bench"));
    command.env_clear();
    command
}

#[test]
fn embedded_suite_can_be_listed_without_external_tools() {
    let output = benchmark().arg("list").output().unwrap();
    assert!(output.status.success(), "{:?}", output);
    assert!(
        String::from_utf8(output.stdout)
            .unwrap()
            .lines()
            .any(|line| line.starts_with("canonical: at least "))
    );
}

#[test]
fn invalid_selection_is_rejected() {
    let temporary = tempfile::tempdir().unwrap();
    let output = benchmark()
        .args(["plan", "--suite", "../escape"])
        .output()
        .unwrap();
    assert!(!output.status.success());

    let mut invalid: serde_yaml_ng::Value =
        serde_yaml_ng::from_str(include_str!("../../loadtest/suite.yaml")).unwrap();
    invalid["scenarios"]["smoke"]["workers"] =
        serde_yaml_ng::Value::Number((i64::from(i32::MAX) + 1).into());
    let suite = temporary.path().join("invalid.yaml");
    fs::write(&suite, serde_yaml_ng::to_string(&invalid).unwrap()).unwrap();
    let rejected = benchmark()
        .arg("--suite-file")
        .arg(&suite)
        .args(["plan", "--suite", "smoke"])
        .output()
        .unwrap();
    assert!(!rejected.status.success());
    assert!(String::from_utf8_lossy(&rejected.stderr).contains("workers"));
}

#[test]
fn pulsar_ramp_selection_is_explicit_and_missing_configuration_fails() {
    let suite =
        std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../loadtest/session-ramp.yaml");
    let output = benchmark()
        .arg("--suite-file")
        .arg(&suite)
        .args([
            "plan",
            "--suite",
            "session-ramp",
            "--algorithm",
            "pulsar-wait-and-widen",
        ])
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
    let plan: Value = serde_json::from_slice(&output.stdout).unwrap();
    assert_eq!(plan["arms"].as_array().unwrap().len(), 1);
    assert_eq!(plan["arms"][0]["algorithm"], "pulsar-wait-and-widen");
    assert_eq!(
        plan["arms"][0]["directory"],
        "session-ramp/pulsar-wait-and-widen"
    );
    assert_eq!(
        plan["routingHeaders"]["pulsar-wait-and-widen"],
        "pulsar-wait-and-widen"
    );

    let default = benchmark()
        .arg("--suite-file")
        .arg(suite)
        .args(["plan", "--suite", "session-ramp"])
        .output()
        .unwrap();
    assert!(default.status.success());
    let default: Value = serde_json::from_slice(&default.stdout).unwrap();
    assert_eq!(default["arms"].as_array().unwrap().len(), 2);
    assert!(
        default["arms"]
            .as_array()
            .unwrap()
            .iter()
            .all(|arm| arm["algorithm"] != "pulsar-wait-and-widen")
    );

    let rejected = benchmark()
        .args([
            "plan",
            "--suite",
            "smoke",
            "--algorithm",
            "pulsar-wait-and-widen",
        ])
        .output()
        .unwrap();
    assert!(!rejected.status.success());
    assert!(
        String::from_utf8_lossy(&rejected.stderr)
            .contains("missing algorithm pulsar-wait-and-widen")
    );
}

#[test]
fn every_shipped_suite_plans_each_pair_once_per_default_algorithm() {
    let loadtest = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../loadtest");
    for file in ["suite.yaml", "reduced.yaml", "session-ramp.yaml"] {
        let suite_file = loadtest.join(file);
        let list = benchmark()
            .arg("--suite-file")
            .arg(&suite_file)
            .arg("list")
            .output()
            .unwrap();
        assert!(list.status.success(), "{file}: {list:?}");
        let listed = String::from_utf8(list.stdout).unwrap();
        assert!(!listed.trim().is_empty(), "{file}");
        for line in listed.lines() {
            let (name, rest) = line.split_once(": ").unwrap();
            let scenarios: BTreeSet<_> = rest
                .split_once("scenarios: ")
                .unwrap()
                .1
                .split(", ")
                .collect();
            let output = benchmark()
                .arg("--suite-file")
                .arg(&suite_file)
                .args(["plan", "--suite", name])
                .output()
                .unwrap();
            assert!(
                output.status.success(),
                "{file} {name}: {}",
                String::from_utf8_lossy(&output.stderr)
            );
            let plan: Value = serde_json::from_slice(&output.stdout).unwrap();
            let mut pairs = BTreeMap::<String, BTreeSet<String>>::new();
            for arm in plan["arms"].as_array().unwrap() {
                let directory = arm["directory"].as_str().unwrap();
                let (pair, algorithm) = directory.rsplit_once('/').unwrap();
                assert_eq!(arm["algorithm"], algorithm);
                assert!(
                    pairs
                        .entry(pair.to_owned())
                        .or_default()
                        .insert(algorithm.to_owned()),
                    "{file} {name}: {directory} repeated"
                );
            }
            let planned: BTreeSet<_> = plan["arms"]
                .as_array()
                .unwrap()
                .iter()
                .map(|arm| arm["scenario"].as_str().unwrap())
                .collect();
            assert_eq!(planned, scenarios, "{file} {name}");
            for (pair, algorithms) in pairs {
                assert_eq!(
                    algorithms,
                    BTreeSet::from(["power-of-n".to_owned(), "wait-and-widen".to_owned()]),
                    "{file} {name} {pair}"
                );
            }
        }
    }
}

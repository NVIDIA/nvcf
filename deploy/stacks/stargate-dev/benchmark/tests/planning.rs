// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use serde_json::Value;
use sha2::{Digest, Sha256};
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
            .contains("canonical: at least 86.5 measured minutes at rate caps: smoke, saturation")
    );
}

#[test]
fn plan_materializes_verified_workloads_without_overwriting_an_existing_output() {
    let temporary = tempfile::tempdir().unwrap();
    let directory = temporary.path().join("nested/plan");
    let output = benchmark()
        .args(["plan", "--suite", "smoke", "--output"])
        .arg(&directory)
        .output()
        .unwrap();
    assert!(output.status.success(), "{:?}", output);
    let original = fs::read(directory.join("plan.json")).unwrap();
    let plan: Value = serde_json::from_slice(&original).unwrap();
    assert_eq!(plan["arms"].as_array().unwrap().len(), 2);
    let workload = fs::read(directory.join("workloads/smoke.yaml")).unwrap();
    let fingerprint: Value =
        serde_json::from_slice(&fs::read(directory.join("workloads/smoke.json")).unwrap()).unwrap();
    assert_eq!(
        fingerprint["sha256"],
        format!("{:x}", Sha256::digest(&workload))
    );
    assert_eq!(fingerprint["promptCount"], 32);
    fs::write(directory.join("keep"), "existing artifact").unwrap();
    let rejected = benchmark()
        .args(["plan", "--suite", "smoke", "--output"])
        .arg(&directory)
        .output()
        .unwrap();
    assert!(!rejected.status.success());
    assert_eq!(fs::read(directory.join("plan.json")).unwrap(), original);
    assert_eq!(
        fs::read_to_string(directory.join("keep")).unwrap(),
        "existing artifact"
    );
}

#[test]
fn invalid_selection_has_no_output_side_effects() {
    let temporary = tempfile::tempdir().unwrap();
    let directory = temporary.path().join("must-not-exist");
    let output = benchmark()
        .args(["plan", "--suite", "../escape", "--output"])
        .arg(&directory)
        .output()
        .unwrap();
    assert!(!output.status.success());
    assert!(!directory.exists());

    let mut invalid: serde_yaml_ng::Value =
        serde_yaml_ng::from_str(include_str!("../../loadtest/suite.yaml")).unwrap();
    invalid["scenarios"]["smoke"]["workers"] =
        serde_yaml_ng::Value::Number((i64::from(i32::MAX) + 1).into());
    let suite = temporary.path().join("invalid.yaml");
    fs::write(&suite, serde_yaml_ng::to_string(&invalid).unwrap()).unwrap();
    let rejected = benchmark()
        .arg("--suite-file")
        .arg(&suite)
        .args(["plan", "--suite", "smoke", "--output"])
        .arg(&directory)
        .output()
        .unwrap();
    assert!(!rejected.status.success());
    assert!(String::from_utf8_lossy(&rejected.stderr).contains("workers"));
    assert!(!directory.exists());
}

#[test]
fn selected_algorithm_is_the_only_algorithm_in_the_plan() {
    let output = benchmark()
        .args(["plan", "--suite", "capacity", "--algorithm", "power-of-n"])
        .output()
        .unwrap();
    assert!(output.status.success(), "{:?}", output);
    let plan: Value = serde_json::from_slice(&output.stdout).unwrap();
    for arm in plan["arms"].as_array().unwrap() {
        assert_eq!(arm["algorithm"], "power-of-n");
    }
}

#[test]
fn reduced_suite_selects_only_the_requested_pairs_and_each_pair_is_individually_available() {
    let suite = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../loadtest/reduced.yaml");
    for (name, expected_scenarios) in [
        (
            "reduced",
            vec!["session-affinity", "mixed-sessions", "saturation"],
        ),
        ("session-affinity", vec!["session-affinity"]),
        ("mixed-sessions", vec!["mixed-sessions"]),
        ("capacity", vec!["saturation"]),
    ] {
        let output = benchmark()
            .arg("--suite-file")
            .arg(&suite)
            .args(["plan", "--suite", name])
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        let plan: Value = serde_json::from_slice(&output.stdout).unwrap();
        let arms = plan["arms"].as_array().unwrap();
        assert_eq!(arms.len(), expected_scenarios.len() * 2);
        for scenario in expected_scenarios {
            for algorithm in ["wait-and-widen", "power-of-n"] {
                let selected: Vec<_> = arms
                    .iter()
                    .filter(|arm| arm["scenario"] == scenario && arm["algorithm"] == algorithm)
                    .collect();
                assert_eq!(selected.len(), 1);
                let streams = selected[0]["streams"].as_array().unwrap();
                match scenario {
                    "session-affinity" => {
                        assert_eq!(
                            streams[0]["limit"],
                            serde_json::json!({"kind":"requests","value":6144})
                        );
                        assert_eq!(streams[0]["rate"], 24);
                    }
                    "mixed-sessions" => {
                        assert_eq!(streams.len(), 2);
                        assert_eq!(streams[0]["name"], "hot");
                        assert_eq!(streams[1]["name"], "short");
                        assert_eq!(streams[0]["rate"], 8);
                        assert_eq!(streams[1]["rate"], 4);
                        for stream in streams {
                            assert_eq!(
                                stream["limit"],
                                serde_json::json!({"kind":"duration-seconds","value":600})
                            );
                        }
                    }
                    "saturation" => {
                        assert_eq!(streams[0]["rate"], 96);
                        assert_eq!(streams[0]["workers"], 192);
                        assert_eq!(
                            streams[0]["limit"],
                            serde_json::json!({"kind":"duration-seconds","value":120})
                        );
                    }
                    _ => unreachable!(),
                }
            }
        }
    }
}

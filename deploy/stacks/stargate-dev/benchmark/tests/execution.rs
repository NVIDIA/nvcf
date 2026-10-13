// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// These tests run detached Spark stand-ins and need /proc, setsid and flock.
#![cfg(target_os = "linux")]

mod support;

use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

use serde_json::{Value, json};
use support::FakeCommand;

const SUITE: &str = r#"
version: 1
defaults: {maxTokens: 2, timeoutSeconds: 1, requestSloMs: 1000, maxWaitMs: 1000, cooldownSeconds: 0}
algorithms:
  wait-and-widen: {routingHeader: null}
  power-of-n: {routingHeader: powerOfN}
suites: {small: [session]}
scenarios:
  session: {kind: session-affinity, sessions: 1, turnsPerSession: 2, repeats: 1, rate: 1, workers: 1, stablePrefixBytes: 64, turnBytes: 64}
"#;

fn mixed_suite() -> String {
    SUITE.split("scenarios:").next().unwrap().to_owned()
        + r#"scenarios:
  session:
    kind: mixed-sessions
    durationSeconds: 1
    repeats: 1
    hot: {rate: 2, workers: 2, minPromptBytes: 256, maxPromptBytes: 512}
    short: {rate: 1, workers: 1, promptBytesByTurn: [256, 512]}
"#
}

fn kubectl(
    fake: &FakeCommand,
    context: &str,
    namespace: &str,
    args: &[&str],
    responses: &[(i32, &str, &str)],
) {
    let mut command = vec![
        "--context",
        context,
        "-n",
        namespace,
        "--request-timeout=25s",
    ];
    command.extend_from_slice(args);
    fake.respond(&command, responses);
}

/// Simulate a one-region deployment and a Spark Pod named `spark-test`.
fn environment(fake: &FakeCommand) -> PathBuf {
    let executable = fake.executable();
    let root = executable.parent().unwrap().to_owned();
    std::os::unix::fs::symlink(&executable, root.join("kubectl")).unwrap();
    std::os::unix::fs::symlink(&executable, root.join("spark")).unwrap();
    fs::write(root.join("pod-fixture"), "").unwrap();
    fs::write(root.join("pod.json"), json!({
        "metadata":{"name":"spark-test","namespace":"stargate-dev","uid":"00000000-0000-4000-8000-000000000001"},
        "spec":{"containers":[{"name":"spark","image":"fixture:image","resources":{"requests":{"cpu":"1","memory":"128Mi"},"limits":{"cpu":"1","memory":"128Mi"}}}]},
        "status":{"phase":"Running","containerStatuses":[{"name":"spark","ready":true,"restartCount":0,"containerID":"containerd://fixture-spark","imageID":format!("fixture@sha256:{}","b".repeat(64)),"state":{"running":{"startedAt":"2026-09-17T00:00:00Z"}}}]}
    }).to_string()).unwrap();
    let distribution = json!({"min":1,"max":2,"mean":1.5,"p50":1,"p90":1,"p95":1,"p99":1});
    let mut report = json!({
        "summary":{"total_requests":2,"successful":2,"failed":0,"total_retries":0,"retried_requests":0,"total_duration_ms":1000},
        "throughput":{"kv_cache_hit_requests":0,"kv_cache_observed_requests":2,"total_cached_tokens":0},
        "ttft_ms":distribution,"latency_ms":distribution,"itl_ms":distribution,
    });
    fs::write(root.join("report.json"), report.to_string()).unwrap();
    report["summary"]["total_requests"] = json!(1);
    report["summary"]["successful"] = json!(1);
    report["throughput"]["kv_cache_observed_requests"] = json!(1);
    fs::write(root.join("warm-report.json"), report.to_string()).unwrap();
    fake.default_response(&[(1, "", "Forbidden: unexpected fixture command")]);
    let namespace = "stargate-dev";
    for context in ["stargate-usw2", "mockdc-usw2-a", "mockdc-usw2-b"] {
        let deployments = if context == "stargate-usw2" {
            vec!["llm-request-router".to_owned()]
        } else {
            (0..2)
                .map(|index| format!("{context}-stargate-dev-mockdc-backend-{index}"))
                .collect()
        };
        for deployment in deployments {
            for (operation, timeout) in [("restart", "115s"), ("status", "325s")] {
                let request_timeout = format!("--request-timeout={timeout}");
                let target = format!("deployment/{deployment}");
                let mut args = vec![
                    "--context",
                    context,
                    "-n",
                    namespace,
                    &request_timeout,
                    "rollout",
                    operation,
                    &target,
                ];
                if operation == "status" {
                    args.push("--timeout=5m");
                }
                fake.respond(&args, &[(0, "", "")]);
            }
        }
        for (name, replicas) in [
            ("stargate-dev-auth", 1),
            ("llm-request-router", 3),
            ("llm-request-router-backend-router", 3),
        ] {
            kubectl(fake, context, namespace, &["get", "deployment", name, "-o", "json"], &[(0, &json!({
                "metadata":{"generation":1},"spec":{"replicas":replicas},
                "status":{"readyReplicas":replicas,"updatedReplicas":replicas,"availableReplicas":replicas,"observedGeneration":1}
            }).to_string(), "")]);
        }
        kubectl(
            fake,
            context,
            namespace,
            &["get", "deployments", "-o", "json"],
            &[(
                0,
                r#"{"items":[{"metadata":{"name":"service"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"fixed"}]}}}}]}"#,
                "",
            )],
        );
        kubectl(
            fake,
            context,
            namespace,
            &["get", "pods", "-o", "json"],
            &[(
                0,
                r#"{"items":[{"metadata":{"name":"service","uid":"original"},"status":{"containerStatuses":[{"name":"service","restartCount":0,"containerID":"original","imageID":"fixed"}]}}]}"#,
                "",
            )],
        );
        if context.starts_with("mockdc") {
            let pods: Vec<_> = (0..2).map(|index| json!({
                "metadata":{"name":format!("backend-{index}")},
                "spec":{"containers":[{"name":"mock-dynamo","args":[]},{"name":"pylon","args":[
                    format!("--inference-server-id={context}-backend-{index}"),
                    "--engine-stats-stream=off", "--active-canary-interval-ms=0", "--do-calibration",
                    "--max-engine-concurrency=25", "--calibration-max-concurrency=25"
                ]}]},
                "status":{"containerStatuses":[{"name":"mock-dynamo","ready":true},{"name":"pylon","ready":true}]}
            })).collect();
            kubectl(
                fake,
                context,
                namespace,
                &[
                    "get",
                    "pods",
                    "-l",
                    &format!("app.kubernetes.io/instance={context}"),
                    "-o",
                    "json",
                ],
                &[(0, &json!({"items":pods}).to_string(), "")],
            );
            for index in 0..2 {
                kubectl(
                    fake,
                    context,
                    namespace,
                    &[
                        "get",
                        &format!(
                            "--raw=/api/v1/namespaces/{namespace}/pods/backend-{index}:9089/proxy/metrics"
                        ),
                    ],
                    &[(
                        0,
                        concat!(
                            "pylon_engine_stats_stream_events_total 0\n",
                            "pylon_engine_stats_stream_connected 0\n",
                            "pylon_model_calibration_duration_ms_count{model=\"stargate-dev-model\",outcome=\"completed\"} 1\n",
                            "pylon_model_last_mean_input_tps{model=\"stargate-dev-model\"} 321\n",
                        ),
                        "",
                    )],
                );
                kubectl(fake, context, namespace, &["get", &format!("--raw=/api/v1/namespaces/{namespace}/services/http:{context}-stargate-dev-mockdc-backend-{index}:http/proxy/kv-cache/stats")], &[(0, &json!({"kv_cache_entries":0,"kv_cache_used_tokens":0,"kv_cache_hit_count":0,"kv_cache_miss_count":0,"kv_cache_eviction_count":0,"kv_cache_evicted_tokens":0}).to_string(), "")]);
            }
        }
    }
    let context = "stargate-usw2";
    kubectl(
        fake,
        context,
        namespace,
        &["get", "configmap", "llm-request-router-lb", "-o", "json"],
        &[(0, r#"{"data":{"lb-config.json":"{\"n\":2}"}}"#, "")],
    );
    kubectl(fake, context, namespace, &["get", "service", "llm-request-router-backend-router", "-o", "json"], &[(0, &json!({"spec":{"type":"LoadBalancer","ports":[{"port":50071,"protocol":"TCP"},{"port":50072,"protocol":"UDP"}]},"status":{"loadBalancer":{"ingress":[{"hostname":"test.invalid"}]}}}).to_string(), "")]);
    let pods: Vec<_> = (0..3)
        .map(|index| json!({"metadata":{"name":format!("router-{index}")}}))
        .collect();
    kubectl(
        fake,
        context,
        namespace,
        &[
            "get",
            "pods",
            "-l",
            "app.kubernetes.io/name=llm-request-router,app.kubernetes.io/instance=llm-request-router",
            "-o",
            "json",
        ],
        &[(0, &json!({"items":pods}).to_string(), "")],
    );
    for index in 0..3 {
        kubectl(
            fake,
            context,
            namespace,
            &[
                "get",
                &format!(
                    "--raw=/api/v1/namespaces/{namespace}/pods/router-{index}:9090/proxy/metrics"
                ),
            ],
            &[(
                0,
                "stargate_active_inference_servers{model=\"stargate-dev-model\",routing_key=\"stargate-dev\"} 4\n",
                "",
            )],
        );
    }
    root
}

fn benchmark(fake: &FakeCommand) -> Command {
    let mut command = Command::new(env!("CARGO_BIN_EXE_stargate-dev-bench"));
    command
        .env_clear()
        .env("PATH", fake.executable().parent().unwrap());
    command
}

fn run_command(fake: &FakeCommand, directory: &Path, resume: bool) -> Command {
    let mut command = benchmark(fake);
    command
        .arg("--suite-file")
        .arg(directory.join("suite.yaml"))
        .args([
            "run",
            "--suite",
            "small",
            "--region",
            "us-west-2",
            "--spark-pod",
            "spark-test",
            "--output",
        ])
        .arg(directory.join("results"));
    if resume {
        command.arg("--resume");
    }
    command
}

fn run(fake: &FakeCommand, directory: &Path, resume: bool) -> Output {
    run_command(fake, directory, resume).output().unwrap()
}

fn successful(output: Output) {
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
}

/// Count Spark invocations; the fixture runs Spark with `--endpoint` first.
fn starts(fake: &FakeCommand) -> usize {
    fake.calls()
        .iter()
        .filter(|args| args.first().is_some_and(|arg| arg == "--endpoint"))
        .count()
}

#[test]
fn pod_campaign_accepts_pairs_resumes_without_traffic_and_preserves_failed_attempts() {
    for mixed in [false, true] {
        let directory = tempfile::tempdir().unwrap();
        let fake = FakeCommand::new();
        let root = environment(&fake);
        if mixed {
            fs::write(root.join("lose-launch-ack"), "").unwrap();
            fs::write(directory.path().join("suite.yaml"), mixed_suite()).unwrap();
        } else {
            fs::write(directory.path().join("suite.yaml"), SUITE).unwrap();
            kubectl(
                &fake,
                "stargate-usw2",
                "stargate-dev",
                &["get", "pods", "-o", "json"],
                &[
                    (
                        0,
                        r#"{"items":[{"metadata":{"name":"service","uid":"old"}}]}"#,
                        "",
                    ),
                    (
                        0,
                        r#"{"items":[{"metadata":{"name":"service","uid":"new"}}]}"#,
                        "",
                    ),
                ],
            );
            let failed = run(&fake, directory.path(), false);
            assert!(!failed.status.success());
            assert!(String::from_utf8_lossy(&failed.stderr).contains("Pod replacement"));
            assert_eq!(starts(&fake), 1);
            assert!(!directory.path().join("results/accepted").exists());
        }
        let first = run(&fake, directory.path(), !mixed);
        let lost_acknowledgements = String::from_utf8_lossy(&first.stderr)
            .matches("Pod launch acknowledgement unavailable")
            .count();
        successful(first);
        if mixed {
            assert_eq!(lost_acknowledgements, 6);
        }
        let before = starts(&fake);
        assert_eq!(before, if mixed { 6 } else { 3 });
        successful(run(&fake, directory.path(), true));
        assert_eq!(starts(&fake), before);
        let attempts = fs::read_dir(directory.path().join("results/attempts"))
            .unwrap()
            .count();
        assert_eq!(attempts, if mixed { 2 } else { 3 });
        let summary: Value = serde_json::from_slice(
            &fs::read(directory.path().join("results/summary.json")).unwrap(),
        )
        .unwrap();
        assert_eq!(
            summary["rows"].as_array().unwrap().len(),
            if mixed { 4 } else { 2 }
        );
        if mixed {
            assert_eq!(summary["comparisons"].as_array().unwrap().len(), 2);
        }
    }
}

#[test]
fn scheduled_pod_pair_retains_timeline_and_rejects_changed_evidence_before_resume() {
    let directory = tempfile::tempdir().unwrap();
    let fake = FakeCommand::new();
    let root = environment(&fake);
    let suite = SUITE.split("scenarios:").next().unwrap().to_owned()
        + "scenarios:\n  session: {kind: session-ramp, sessions: 1, stablePrefixBytes: 64, turnBytes: 64, steps: [{rate: 2, durationSeconds: 1}]}\n";
    fs::write(directory.path().join("suite.yaml"), suite).unwrap();
    let steps = json!([{"rate_limit":2.0,"duration_ms":1000}]);
    let report_path = root.join("report.json");
    let mut report: Value = serde_json::from_slice(&fs::read(&report_path).unwrap()).unwrap();
    report["config"] = json!({"workers":1,"rate_steps":steps,"duration_ms":1000});
    fs::write(report_path, report.to_string()).unwrap();
    let mut timeline = json!({"kind":"schedule","workers":1,"rate_steps":steps}).to_string() + "\n";
    for id in 1..=2 {
        timeline.push_str(&json!({"id":id,"timing":{"admitted_offset_ms":(id-1)*500,"completed_offset_ms":(id-1)*500+250},"success":true,"http_error_status":null,"retries":0,"ttft_ms":1.0,"latency_ms":2.0,"cached_tokens":0,"cache_observations":1,"cache_hits":0}).to_string());
        timeline.push('\n');
    }
    fs::write(root.join("spark.requests.jsonl"), &timeline).unwrap();
    successful(run(&fake, directory.path(), false));
    assert_eq!(starts(&fake), 2);
    successful(run(&fake, directory.path(), true));
    assert_eq!(starts(&fake), 2);
    let output = directory.path().join("results");
    let receipt_path = output.join("accepted/session/wait-and-widen/receipt.json");
    let receipt: Value = serde_json::from_slice(&fs::read(&receipt_path).unwrap()).unwrap();
    let path =
        Path::new(receipt["reports"][0]["path"].as_str().unwrap()).with_extension("requests.jsonl");
    assert!(receipt["evidence"][path.to_str().unwrap()].is_string());
    assert_eq!(fs::read_to_string(output.join(&path)).unwrap(), timeline);
    fs::write(output.join(&path), timeline + "\n").unwrap();
    let rejected = run(&fake, directory.path(), true);
    assert!(!rejected.status.success());
    assert_eq!(starts(&fake), 2);
}

#[test]
fn pulsar_pod_arm_preserves_routing_header_and_reports_its_actual_algorithm() {
    let directory = tempfile::tempdir().unwrap();
    let fake = FakeCommand::new();
    environment(&fake);
    let suite = SUITE.replace(
        "  power-of-n: {routingHeader: powerOfN}",
        "  power-of-n: {routingHeader: powerOfN}\n  pulsar-wait-and-widen: {routingHeader: pulsar-wait-and-widen}",
    );
    fs::write(directory.path().join("suite.yaml"), suite).unwrap();
    successful(
        run_command(&fake, directory.path(), false)
            .args(["--algorithm", "pulsar-wait-and-widen"])
            .output()
            .unwrap(),
    );
    let calls = fake.calls();
    let starts: Vec<_> = calls
        .iter()
        .filter(|args| args.first().is_some_and(|arg| arg == "--endpoint"))
        .collect();
    assert_eq!(starts.len(), 1);
    assert!(starts[0].windows(2).any(|pair| pair
        == [
            "--stargate-load-balancing-algorithm",
            "pulsar-wait-and-widen"
        ]));
    let summary: Value =
        serde_json::from_slice(&fs::read(directory.path().join("results/summary.json")).unwrap())
            .unwrap();
    assert_eq!(summary["rows"].as_array().unwrap().len(), 1);
    assert_eq!(summary["rows"][0]["algorithm"], "pulsar-wait-and-widen");
    assert!(summary["comparisons"].as_array().unwrap().is_empty());
    assert_eq!(summary["unpaired"].as_array().unwrap().len(), 1);
}

#[test]
fn hangup_is_ignored_under_nohup_and_a_rejected_resume_still_stops_live_streams() {
    use rustix::fd::OwnedFd;
    use rustix::process::{Pid, PidfdFlags, Signal, kill_process, pidfd_open, pidfd_send_signal};
    use std::collections::BTreeMap;
    use std::process::{Child, Stdio};
    use std::time::{Duration, Instant};

    struct Controller(Child);
    impl Drop for Controller {
        fn drop(&mut self) {
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }
    struct OwnedProcess(OwnedFd);
    impl Drop for OwnedProcess {
        fn drop(&mut self) {
            let _ = pidfd_send_signal(&self.0, Signal::KILL);
        }
    }

    let directory = tempfile::tempdir().unwrap();
    let fake = FakeCommand::new();
    let root = environment(&fake);
    fs::write(root.join("hold-mixed-launches"), "").unwrap();
    fs::write(root.join("hold-cancel"), "").unwrap();
    fs::write(directory.path().join("suite.yaml"), mixed_suite()).unwrap();
    // Start the controller with SIGHUP ignored, as `nohup` would.
    let inner = run_command(&fake, directory.path(), false);
    let mut controller = Controller(
        Command::new("/bin/sh")
            .args(["-c", "trap '' HUP; exec \"$0\" \"$@\""])
            .arg(inner.get_program())
            .args(inner.get_args())
            .env_clear()
            .envs(
                inner
                    .get_envs()
                    .filter_map(|(key, value)| Some((key, value?))),
            )
            .stdout(Stdio::null())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap(),
    );
    let interrupt = |controller: &Controller| {
        kill_process(
            Pid::from_raw(i32::try_from(controller.0.id()).unwrap()).unwrap(),
            Signal::INT,
        )
        .unwrap();
    };
    let deadline = Instant::now() + Duration::from_secs(25);
    let mut processes = BTreeMap::new();
    while processes.len() < 4 {
        for marker in [
            "spark-running-hot",
            "spark-running-short",
            "ack-held-hot",
            "ack-held-short",
        ] {
            if processes.contains_key(marker) {
                continue;
            }
            if let Some(pid) = fs::read_to_string(root.join(marker))
                .ok()
                .and_then(|text| text.parse::<u32>().ok())
            {
                let handle = pidfd_open(
                    Pid::from_raw(i32::try_from(pid).unwrap()).unwrap(),
                    PidfdFlags::empty(),
                )
                .unwrap();
                processes.insert(marker, (pid, OwnedProcess(handle)));
            }
        }
        assert!(
            controller.0.try_wait().unwrap().is_none(),
            "controller exited before both launch acknowledgements were held"
        );
        assert!(
            Instant::now() < deadline,
            "both mixed streams did not start"
        );
        std::thread::sleep(Duration::from_millis(10));
    }
    kill_process(
        Pid::from_raw(i32::try_from(controller.0.id()).unwrap()).unwrap(),
        Signal::HUP,
    )
    .unwrap();
    std::thread::sleep(Duration::from_millis(300));
    assert!(
        controller.0.try_wait().unwrap().is_none() && !root.join("cancel-held").exists(),
        "an ignored hangup interrupted the campaign"
    );
    interrupt(&controller);
    while !root.join("cancel-held").exists() {
        assert!(
            controller.0.try_wait().unwrap().is_none(),
            "controller exited before cleanup started"
        );
        assert!(Instant::now() < deadline, "cleanup did not start");
        std::thread::sleep(Duration::from_millis(10));
    }
    // A second interrupt must not wait for the hung cleanup.
    interrupt(&controller);
    let abandoned = Instant::now() + Duration::from_secs(5);
    loop {
        if let Some(status) = controller.0.try_wait().unwrap() {
            assert_eq!(status.code(), Some(130));
            break;
        }
        assert!(
            Instant::now() < abandoned,
            "second interrupt did not abandon cleanup"
        );
        std::thread::sleep(Duration::from_millis(10));
    }
    let mut stderr = String::new();
    std::io::Read::read_to_string(controller.0.stderr.as_mut().unwrap(), &mut stderr).unwrap();
    assert!(stderr.contains("cleanup is unconfirmed"), "{stderr}");

    fs::remove_file(root.join("hold-cancel")).unwrap();
    // A resume that rejects changed controls must still stop owned work.
    kubectl(
        &fake,
        "stargate-usw2",
        "stargate-dev",
        &["get", "deployments", "-o", "json"],
        &[(
            0,
            r#"{"items":[{"metadata":{"name":"service"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"changed"}]}}}}]}"#,
            "",
        )],
    );
    let rejected = run(&fake, directory.path(), true);
    assert!(!rejected.status.success());
    assert!(String::from_utf8_lossy(&rejected.stderr).contains("routing settings changed"));
    let output = directory.path().join("results");
    for (marker, (pid, _guard)) in &processes {
        match fs::read_to_string(format!("/proc/{pid}/stat")) {
            Ok(stat) => assert!(
                matches!(
                    stat.rsplit_once(") ").unwrap().1.split_whitespace().next(),
                    Some("Z" | "X")
                ),
                "fixture {marker} remains active"
            ),
            Err(error) => assert_eq!(error.kind(), std::io::ErrorKind::NotFound),
        }
    }
    assert!(!output.join("accepted").exists());
    let attempt = fs::read_dir(output.join("attempts"))
        .unwrap()
        .next()
        .unwrap()
        .unwrap()
        .path();
    let records: Vec<Value> = fs::read_dir(attempt.join("pod-launches"))
        .unwrap()
        .map(|entry| serde_json::from_slice(&fs::read(entry.unwrap().path()).unwrap()).unwrap())
        .collect();
    assert_eq!(records.len(), 3);
    assert_eq!(
        records
            .iter()
            .filter(|record| record["phase"] == "cancelled")
            .count(),
        2
    );
    assert_eq!(
        records
            .iter()
            .filter(|record| record["phase"] == json!({"exited":{"code":0}}))
            .count(),
        1
    );
    assert_eq!(starts(&fake), 3);
    successful(
        benchmark(&fake)
            .args(["reconcile", "--output"])
            .arg(&output)
            .output()
            .unwrap(),
    );
}

#[test]
fn spark_pod_changes_reject_the_arm_and_resume_without_traffic() {
    let directory = tempfile::tempdir().unwrap();
    let fake = FakeCommand::new();
    let root = environment(&fake);
    fs::write(directory.path().join("suite.yaml"), SUITE).unwrap();
    let original = fs::read_to_string(root.join("pod.json")).unwrap();
    fs::write(
        root.join("resized-pod.json"),
        original.replace("128Mi", "256Mi"),
    )
    .unwrap();
    let changed = |output: Output| {
        assert!(!output.status.success());
        assert!(
            String::from_utf8_lossy(&output.stderr).contains("Spark Pod environment changed"),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(starts(&fake), 1);
        assert!(!directory.path().join("results/accepted").exists());
    };
    // The container survives an in-place resize, so the arm check must see it.
    changed(run(&fake, directory.path(), false));
    changed(run(&fake, directory.path(), true));
    // Only the full inspection on resume sees a different Spark build.
    fs::write(root.join("pod.json"), &original).unwrap();
    fs::write(root.join("spark-version"), "Spark fixture 2").unwrap();
    changed(run(&fake, directory.path(), true));
    fs::remove_file(root.join("spark-version")).unwrap();
    successful(run(&fake, directory.path(), true));
    assert_eq!(starts(&fake), 3);
}

#[test]
fn expected_configuration_and_controls_are_checked_before_traffic_and_bound_to_resume() {
    let directory = tempfile::tempdir().unwrap();
    let fake = FakeCommand::new();
    environment(&fake);
    fs::write(directory.path().join("suite.yaml"), SUITE).unwrap();
    let policy = directory.path().join("expected.json");
    let run = |resume| {
        run_command(&fake, directory.path(), resume)
            .arg("--expected-config")
            .arg(&policy)
            .output()
            .unwrap()
    };
    fs::write(&policy, "[]").unwrap();
    let invalid = run(false);
    assert_eq!(invalid.status.code(), Some(2));
    assert!(!directory.path().join("results").exists());
    assert!(fake.calls().is_empty());

    fs::write(&policy, r#"{"n":3}"#).unwrap();
    let mismatch = run(false);
    assert!(!mismatch.status.success());
    assert!(String::from_utf8_lossy(&mismatch.stderr).contains("configuration"));
    assert_eq!(starts(&fake), 0);
    assert!(!directory.path().join("results/campaign.json").exists());
    assert!(
        fake.calls()
            .iter()
            .all(|args| !args.windows(2).any(|pair| pair == ["rollout", "restart"]))
    );

    fs::write(&policy, r#"{"n":2}"#).unwrap();
    successful(run(false));
    let manifest = fs::read(directory.path().join("results/campaign.json")).unwrap();
    let calls_before = fake.calls().len();
    fs::write(&policy, r#"{"n":3}"#).unwrap();
    let changed = run(true);
    assert!(!changed.status.success());
    assert!(String::from_utf8_lossy(&changed.stderr).contains("resume inputs differ"));
    assert_eq!(fake.calls().len(), calls_before);
    assert_eq!(
        fs::read(directory.path().join("results/campaign.json")).unwrap(),
        manifest
    );

    fs::write(&policy, r#"{"n":2}"#).unwrap();
    kubectl(
        &fake,
        "stargate-usw2",
        "stargate-dev",
        &["get", "deployments", "-o", "json"],
        &[(
            0,
            r#"{"items":[{"metadata":{"name":"service"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"changed"}]}}}}]}"#,
            "",
        )],
    );
    let starts_before = starts(&fake);
    let rejected = run(true);
    assert!(!rejected.status.success());
    assert!(String::from_utf8_lossy(&rejected.stderr).contains("routing settings changed"));
    assert_eq!(starts(&fake), starts_before);
}

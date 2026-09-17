// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

mod support;

use std::fs;
use std::path::Path;
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

fn environment(fake: &FakeCommand) {
    let executable = fake.executable();
    let root = executable.parent().unwrap();
    for name in ["kubectl", "docker"] {
        std::os::unix::fs::symlink(&executable, root.join(name)).unwrap();
    }
    let distribution = json!({"min":1,"max":2,"mean":1.5,"p50":1,"p90":1,"p95":1,"p99":1});
    let mut report = json!({
        "summary":{"total_requests":2,"successful":2,"failed":0,"total_retries":0,"retried_requests":0,"total_duration_ms":1000},
        "throughput":{"kv_cache_hit_requests":0,"kv_cache_observed_requests":2,"total_cached_tokens":0},
        "ttft_ms":distribution,"latency_ms":distribution,"itl_ms":distribution,
    });
    fs::write(root.join("docker-report.json"), report.to_string()).unwrap();
    report["summary"]["total_requests"] = json!(1);
    report["summary"]["successful"] = json!(1);
    report["throughput"]["kv_cache_observed_requests"] = json!(1);
    fs::write(root.join("docker-warm-report.json"), report.to_string()).unwrap();
    fake.default_response(&[(0, "", "")]); // rollout restart/status
    let namespace = "stargate-dev";
    for context in ["stargate-usw2", "mockdc-usw2-a", "mockdc-usw2-b"] {
        for (name, replicas) in [
            ("stargate-dev-auth", 1),
            ("llm-request-router", 3),
            ("llm-request-router-backend-router", 3),
            ("stargate-dev-alloy", 1),
        ] {
            let ns = if name == "stargate-dev-alloy" {
                "stargate-dev-observability"
            } else {
                namespace
            };
            kubectl(fake, context, ns, &["get", "deployment", name, "-o", "json"], &[(0, &json!({
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
        kubectl(fake, context, "stargate-dev-observability", &["get", "serviceaccount", "stargate-dev-alloy", "-o", "json"], &[(0, &json!({"metadata":{"annotations":{"eks.amazonaws.com/role-arn":"arn:aws:iam::123456789012:role/stargate-dev-amp-writer"}}}).to_string(), "")]);
        if context.starts_with("mockdc") {
            let pods: Vec<_> = (0..2).map(|index| json!({
                "metadata":{"name":format!("backend-{index}")},
                "spec":{"containers":[{"name":"mock-dynamo","args":[]},{"name":"pylon","args":["--inference-server-id",format!("{context}-backend-{index}")]}]},
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
}

fn run_command(fake: &FakeCommand, directory: &Path, resume: bool) -> Command {
    let mut command = Command::new(env!("CARGO_BIN_EXE_stargate-dev-bench"));
    command
        .env_clear()
        .env("PATH", fake.executable().parent().unwrap())
        .env("OPENAI_API_KEY", "fixture-token")
        .arg("--suite-file")
        .arg(directory.join("suite.yaml"))
        .args([
            "run",
            "--suite",
            "small",
            "--region",
            "us-west-2",
            "--spark-image",
            "fixture:image",
            "--endpoint",
            "http://fixture.invalid/v1",
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

fn starts(fake: &FakeCommand) -> usize {
    fake.calls()
        .iter()
        .filter(|args| args.starts_with(&["container".into(), "start".into()]))
        .count()
}

#[test]
fn cli_accepts_a_pair_resumes_without_traffic_and_preserves_failed_validation_attempts() {
    for changed_pod in [false, true] {
        let directory = tempfile::tempdir().unwrap();
        let fake = FakeCommand::new();
        environment(&fake);
        fs::write(directory.path().join("suite.yaml"), SUITE).unwrap();
        if changed_pod {
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
        successful(run(&fake, directory.path(), changed_pod));
        let before = starts(&fake);
        assert_eq!(before, if changed_pod { 3 } else { 2 });
        successful(run(&fake, directory.path(), true));
        assert_eq!(starts(&fake), before);
        let attempts = fs::read_dir(directory.path().join("results/attempts"))
            .unwrap()
            .count();
        assert_eq!(attempts, before);
        let summary: Value = serde_json::from_slice(
            &fs::read(directory.path().join("results/summary.json")).unwrap(),
        )
        .unwrap();
        assert_eq!(summary["rows"].as_array().unwrap().len(), 2);
        assert_eq!(
            fs::read_dir(fake.executable().parent().unwrap().join("containers"))
                .unwrap()
                .count(),
            0
        );
        for call in fake.calls() {
            assert!(!call.iter().any(|arg| arg == "fixture-token"));
        }
    }
}

#[test]
fn mixed_pair_creates_both_streams_before_start_and_excludes_warmup_from_comparison() {
    let directory = tempfile::tempdir().unwrap();
    let fake = FakeCommand::new();
    environment(&fake);
    let suite = SUITE.split("scenarios:").next().unwrap().to_owned()
        + r#"scenarios:
  session:
    kind: mixed-sessions
    durationSeconds: 1
    repeats: 1
    hot: {rate: 2, workers: 2, minPromptBytes: 256, maxPromptBytes: 512}
    short: {rate: 1, workers: 1, promptBytesByTurn: [256, 512]}
"#;
    fs::write(directory.path().join("suite.yaml"), suite).unwrap();
    successful(run(&fake, directory.path(), false));
    let operations: Vec<_> = fake
        .calls()
        .into_iter()
        .filter_map(|args| {
            if args.first().is_some_and(|arg| arg == "container")
                && args
                    .get(1)
                    .is_some_and(|arg| arg == "create" || arg == "start")
            {
                Some(args[1].clone())
            } else {
                None
            }
        })
        .collect();
    assert_eq!(
        operations,
        ["create", "start", "create", "create", "start", "start"].repeat(2)
    );
    let summary: Value =
        serde_json::from_slice(&fs::read(directory.path().join("results/summary.json")).unwrap())
            .unwrap();
    assert_eq!(summary["rows"].as_array().unwrap().len(), 4);
    assert_eq!(summary["comparisons"].as_array().unwrap().len(), 2);
    assert_eq!(starts(&fake), 6);
}

#[test]
fn interruption_removes_owned_container_and_resume_cleans_up_before_rejecting_changed_controls() {
    use rustix::process::{Pid, Signal, kill_process};
    use std::process::Stdio;
    use std::time::{Duration, Instant};

    let directory = tempfile::tempdir().unwrap();
    let fake = FakeCommand::new();
    environment(&fake);
    let root = fake.executable().parent().unwrap().to_owned();
    fs::write(root.join("hold-start"), "").unwrap();
    fs::write(directory.path().join("suite.yaml"), SUITE).unwrap();
    let mut child = run_command(&fake, directory.path(), false)
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let deadline = Instant::now() + Duration::from_secs(10);
    while !root.join("started").exists() {
        if child.try_wait().unwrap().is_some() || Instant::now() >= deadline {
            let _ = child.kill();
            panic!(
                "fixture never started: {}",
                String::from_utf8_lossy(&child.wait_with_output().unwrap().stderr)
            );
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    let id = fs::read_to_string(root.join("started")).unwrap();
    let owned = fs::read(root.join("containers").join(&id)).unwrap();
    kill_process(
        Pid::from_raw(i32::try_from(child.id()).unwrap()).unwrap(),
        Signal::INT,
    )
    .unwrap();
    while child.try_wait().unwrap().is_none() {
        if Instant::now() >= deadline {
            let _ = child.kill();
            let _ = child.wait();
            panic!("controller did not finish cancellation");
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    assert_eq!(child.wait().unwrap().code(), Some(130));
    assert!(!root.join("containers").join(&id).exists());
    assert!(!directory.path().join("results/accepted").exists());

    // Recreate the precise recoverable state left by an abrupt controller exit.
    let attempt = fs::read_dir(directory.path().join("results/attempts"))
        .unwrap()
        .next()
        .unwrap()
        .unwrap()
        .path();
    let launch = fs::read_dir(attempt.join("launches"))
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .find(|path| {
            path.extension()
                .is_some_and(|extension| extension == "json")
        })
        .unwrap();
    let mut record: Value = serde_json::from_slice(&fs::read(&launch).unwrap()).unwrap();
    record["phase"] = json!("running");
    fs::write(&launch, record.to_string()).unwrap();
    fs::write(root.join("containers").join(&id), owned).unwrap();
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
    assert!(!root.join("containers").join(&id).exists());
    assert_eq!(starts(&fake), 1);
}

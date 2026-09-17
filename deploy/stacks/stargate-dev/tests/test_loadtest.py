#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import hashlib
import copy
import io
import importlib.util
import json
import sys
import tempfile
import tarfile
import unittest
from pathlib import Path
from subprocess import CompletedProcess
from types import SimpleNamespace
from unittest import mock

import yaml


STACK_DIR = Path(__file__).resolve().parents[1]
LOADTEST_PATH = STACK_DIR / "scripts" / "loadtest.py"
sys.path.insert(0, str(LOADTEST_PATH.parent))
SPEC = importlib.util.spec_from_file_location("stargate_dev_loadtest", LOADTEST_PATH)
LOADTEST = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = LOADTEST
assert SPEC.loader is not None
SPEC.loader.exec_module(LOADTEST)


class CanonicalSuiteTests(unittest.TestCase):
    def test_peer_topology_is_part_of_campaign_resume_identity(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            args = SimpleNamespace(
                region="us-west-2",
                peer_region=["us-east-1"],
                output=Path(directory) / "campaign",
                endpoint=None,
                spark_pod=None,
                algorithm="both",
                suite="smoke",
                spark_image="spark:test",
                local_port=18000,
                resume=False,
            )
            campaign = LOADTEST.Campaign(args, LOADTEST.load_suite())
            self.assertEqual(len(campaign.backend_targets()), 8)
            self.assertEqual(campaign.identity["peerRegions"], ["us-east-1"])
            campaign.state["currentRuns"] = ["interrupted-run"]
            campaign.save_state()
            args.resume = True
            with self.assertRaisesRegex(LOADTEST.LoadTestError, "another campaign"):
                LOADTEST.Campaign(args, LOADTEST.load_suite())
            campaign.close()
            resumed = LOADTEST.Campaign(args, LOADTEST.load_suite())
            self.assertNotIn("currentRuns", resumed.state)
            resumed.close()
            args.peer_region = []
            with self.assertRaisesRegex(LOADTEST.LoadTestError, "resume arguments"):
                LOADTEST.Campaign(args, LOADTEST.load_suite())

    def test_canonical_suite_covers_capacity_and_cache_behavior(
        self,
    ) -> None:
        suite = LOADTEST.load_suite()
        self.assertEqual(
            suite["suites"]["session-affinity"], ["smoke", "session-affinity"]
        )

        self.assertEqual(
            suite["suites"]["canonical"],
            [
                "smoke",
                "saturation",
                "session-affinity",
                "long-context-affinity",
                "mixed-sessions",
            ],
        )
        self.assertLess(LOADTEST.minimum_measured_minutes(suite, "canonical"), 120)
        self.assertEqual(
            suite["scenarios"]["long-context-affinity"],
            {
                "kind": "session-workers",
                "repeats": 1,
                "rate": 32,
                "workers": 32,
                "sessionTasks": [
                    [80_000, 100_000, 120_000],
                    [80_000, 100_000, 120_000],
                    [8_000, 16_000, 24_000],
                    [80_000, 100_000, 120_000],
                    [80_000, 100_000, 120_000],
                    [1_000, 2_000, 4_000],
                    [80_000, 100_000, 120_000],
                    [80_000, 100_000, 120_000],
                ],
                "requestSloMs": 60_000,
                "maxWaitMs": 60_000,
                "timeoutSeconds": 90,
            },
        )

    def test_generated_sessions_have_the_intended_affinity_keys(self) -> None:
        unique = list(LOADTEST.unique_prompts(3, 512))
        self.assertEqual(len({prompt[:256] for prompt in unique}), 3)

        sessions = list(LOADTEST.session_prompts(2, 3, 512, 128))
        self.assertEqual(len(sessions), 6)
        self.assertEqual(sessions[0][:256], sessions[2][:256])
        self.assertEqual(sessions[1][:256], sessions[3][:256])
        self.assertNotEqual(sessions[0][:256], sessions[1][:256])
        self.assertTrue(sessions[2].startswith(sessions[0]))
        self.assertTrue(sessions[4].startswith(sessions[2]))

        session_workers = list(
            LOADTEST.session_worker_prompts(
                2,
                [
                    [80_000, 100_000],
                    [8_000, 16_000],
                ],
            )
        )
        first_worker = session_workers[0::2]
        second_worker = session_workers[1::2]
        self.assertEqual(
            len({prompt[:256] for prompt in session_workers}),
            4,
        )
        self.assertEqual(
            [5 + len(prompt) // 4 for prompt in first_worker],
            [80_000, 100_000, 8_000, 16_000],
        )
        self.assertEqual(
            [5 + len(prompt) // 4 for prompt in second_worker],
            [8_000, 16_000, 80_000, 100_000],
        )
        self.assertEqual(first_worker[0][:256], first_worker[1][:256])
        self.assertEqual(first_worker[2][:256], first_worker[3][:256])
        self.assertNotEqual(first_worker[0][:256], first_worker[2][:256])
        self.assertEqual(second_worker[0][:256], second_worker[1][:256])
        self.assertEqual(second_worker[2][:256], second_worker[3][:256])

        hot = list(LOADTEST.mixed_hot_prompts(4, 512, 1024))
        self.assertEqual(len({prompt[:256] for prompt in hot}), 1)
        self.assertEqual([len(prompt) for prompt in hot], [512, 682, 853, 1024])

        short = list(LOADTEST.mixed_short_prompts(6, [512, 768, 1024]))
        self.assertEqual(len(short), 6)
        self.assertEqual(short[1][:256], short[2][:256])
        self.assertNotEqual(short[0][:256], short[1][:256])

    def test_workload_is_valid_yaml_and_records_its_fingerprint(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "workload.yaml"
            LOADTEST.write_workload(path, ["one", "two"], "test")

            workload = yaml.safe_load(path.read_text(encoding="utf-8"))
            self.assertEqual(workload["scenarios"][0]["prompts"], ["one", "two"])
            metadata = json.loads(path.with_suffix(".json").read_text(encoding="utf-8"))
            self.assertEqual(metadata["promptCount"], 2)
            self.assertEqual(
                metadata["sha256"],
                hashlib.sha256(path.read_bytes()).hexdigest(),
            )

    def test_spark_command_uses_gateway_headers_and_only_overrides_power_of_n(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            workload = root / "workloads" / "smoke.yaml"
            workload.parent.mkdir()
            workload.write_text("version: 1\n", encoding="utf-8")
            campaign = object.__new__(LOADTEST.Campaign)
            campaign.output = root
            campaign.runs = root / "runs"
            campaign.suite = LOADTEST.load_suite()
            campaign.args = SimpleNamespace(spark_image="spark:test", spark_pod=None)
            campaign.endpoint = "http://127.0.0.1:18000/v1"
            campaign.region = {
                "modelName": "test-model",
                "routingKey": "test-routing-key",
            }

            wait = campaign.spark_run(
                Path("smoke/wait-and-widen"),
                "wait-and-widen",
                scenario="smoke",
                rate=8,
                workers=8,
                workload=workload,
                requests=32,
            ).command
            power = campaign.spark_run(
                Path("smoke/power-of-n"),
                "power-of-n",
                scenario="smoke",
                rate=8,
                workers=8,
                workload=workload,
                requests=32,
            ).command

            for option, value in (
                ("--stargate-routing-key", "test-routing-key"),
                ("--stargate-max-wait-ms", "10000"),
                ("--stargate-request-slo-ms", "10000"),
            ):
                self.assertEqual(wait[wait.index(option) + 1], value)
            self.assertNotIn("--stargate-load-balancing-algorithm", wait)
            override = power.index("--stargate-load-balancing-algorithm")
            self.assertEqual(power[override + 1], "powerOfN")

            long_run = campaign.spark_run(
                Path("long-context/wait-and-widen"),
                "wait-and-widen",
                scenario="long-context-affinity",
                rate=32,
                workers=32,
                workload=workload,
                requests=768,
                request_slo_ms=60_000,
                max_wait_ms=60_000,
                timeout_seconds=90,
            )
            long_context = long_run.command
            self.assertEqual(long_run.expected_seconds, 2160)
            self.assertEqual(
                long_context[long_context.index("--stargate-request-slo-ms") + 1],
                "60000",
            )
            self.assertEqual(
                long_context[long_context.index("--stargate-max-wait-ms") + 1],
                "60000",
            )
            self.assertEqual(
                long_context[long_context.index("--timeout") + 1],
                "90s",
            )

            campaign.args.spark_pod = "spark-worker"
            campaign.stargate_context = "stargate"
            campaign.namespace = "test"
            remote = campaign.spark_run(
                Path("smoke/power-of-n"),
                "power-of-n",
                scenario="smoke",
                rate=8,
                workers=8,
                workload=workload,
                requests=32,
            ).command
            self.assertEqual(remote[:2], ["python3", str(LOADTEST.POD_RUNNER_SCRIPT)])
            self.assertEqual(remote[remote.index("--context") + 1], "stargate")
            self.assertEqual(remote[remote.index("--namespace") + 1], "test")
            self.assertEqual(remote[remote.index("--timeout-seconds") + 1], "720")
            self.assertIn("spark-worker", remote)
            self.assertEqual(
                remote[remote.index("--workload") + 1],
                f"/campaign/{root.name}/workloads/smoke.yaml",
            )
            self.assertEqual(
                remote[remote.index("--stargate-load-balancing-algorithm") + 1],
                "powerOfN",
            )

    def test_regional_health_waits_for_backend_registration(self) -> None:
        campaign = object.__new__(LOADTEST.Campaign)
        campaign.regions = [{"region": "us-west-2"}, {"region": "us-east-1"}]
        attempts = [
            CompletedProcess([], 1, "three backends"),
            CompletedProcess([], 0, "verified"),
            CompletedProcess([], 0, "verified"),
        ]
        with (
            mock.patch.object(campaign, "command", side_effect=attempts) as command,
            mock.patch.object(LOADTEST.time, "sleep") as sleep,
        ):
            campaign.verify_region()

        self.assertEqual(command.call_count, 3)
        for call, region, peer in [
            (command.call_args_list[1], "us-west-2", "us-east-1"),
            (command.call_args_list[2], "us-east-1", "us-west-2"),
        ]:
            arguments = call.args[0]
            self.assertEqual(arguments[arguments.index("--region") + 1], region)
            self.assertEqual(arguments[arguments.index("--peer-region") + 1], peer)
        sleep.assert_called_once_with(5)

    def test_regional_health_can_recover_after_three_minutes(self) -> None:
        campaign = object.__new__(LOADTEST.Campaign)
        campaign.regions = [{"region": "us-west-2"}, {"region": "us-east-1"}]
        now = 0.0

        def sleep(seconds):
            nonlocal now
            now += seconds

        def command(arguments, **kwargs):
            if now < 200:
                return CompletedProcess(arguments, 1, "seven active backends")
            return CompletedProcess(arguments, 0, "eight active backends")

        with (
            mock.patch.object(LOADTEST.time, "monotonic", side_effect=lambda: now),
            mock.patch.object(LOADTEST.time, "sleep", side_effect=sleep),
            mock.patch.object(campaign, "command", side_effect=command) as checks,
        ):
            campaign.verify_region()

        self.assertEqual(now, 200)
        self.assertEqual(
            {
                call.args[0][call.args[0].index("--region") + 1]
                for call in checks.call_args_list[-2:]
            },
            {"us-west-2", "us-east-1"},
        )

    def test_regional_health_stops_retrying_when_membership_stays_incomplete(
        self,
    ) -> None:
        campaign = object.__new__(LOADTEST.Campaign)
        campaign.regions = [{"region": "us-west-2"}, {"region": "us-east-1"}]
        now = 0.0

        def sleep(seconds):
            nonlocal now
            now += seconds

        with (
            mock.patch.object(LOADTEST.time, "monotonic", side_effect=lambda: now),
            mock.patch.object(LOADTEST.time, "sleep", side_effect=sleep),
            mock.patch.object(
                campaign,
                "command",
                return_value=CompletedProcess([], 1, "seven active backends"),
            ),
            self.assertRaisesRegex(LOADTEST.LoadTestError, "seven active backends"),
        ):
            campaign.verify_region()

        self.assertGreaterEqual(now, 200)
        self.assertLessEqual(now, 300)

    def test_cache_reset_restarts_stargate_after_backends(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            campaign = object.__new__(LOADTEST.Campaign)
            campaign.output = Path(directory)
            campaign.stargate_context = "stargate"
            campaign.regions = [
                LOADTEST.load_region(region) for region in ("us-west-2", "us-east-1")
            ]
            campaign.args = SimpleNamespace(endpoint=None, spark_pod=None)
            targets = campaign.backend_targets()
            self.assertEqual(len(targets), 8)
            empty = {
                backend: {"kv_cache_entries": 0, "kv_cache_used_tokens": 0}
                for _, backend in targets
            }
            with (
                mock.patch.object(campaign, "cache_stats", side_effect=[empty, empty]),
                mock.patch.object(campaign, "kubectl", return_value="") as kubectl,
                mock.patch.object(campaign, "verify_region") as verify,
                mock.patch.object(campaign, "stop_port_forward") as stop_forward,
                mock.patch.object(campaign, "start_port_forward") as start_forward,
                mock.patch.object(campaign, "log"),
            ):
                campaign.reset_caches("test")

        for context, backend in targets:
            self.assertIn(
                mock.call(context, ["rollout", "restart", f"deployment/{backend}"]),
                kubectl.call_args_list,
            )
        last_backend_ready = max(
            index
            for index, call in enumerate(kubectl.call_args_list)
            if call.args[1][:2] == ["rollout", "status"] and "mockdc" in call.args[1][2]
        )
        for context in ("stargate-usw2", "stargate-ue1"):
            restart = mock.call(
                context, ["rollout", "restart", "deployment/llm-request-router"]
            )
            self.assertGreater(
                kubectl.call_args_list.index(restart), last_backend_ready
            )
        verify.assert_called_once_with()
        stop_forward.assert_called_once_with()
        start_forward.assert_called_once_with()

    def test_port_forward_uses_websocket_transport(self) -> None:
        with LOADTEST.socket.socket() as listener:
            listener.setsockopt(
                LOADTEST.socket.SOL_SOCKET, LOADTEST.socket.SO_REUSEADDR, 1
            )
            listener.bind(("127.0.0.1", 0))
            listener.listen()
            port = listener.getsockname()[1]
            with LOADTEST.socket.create_connection(("127.0.0.1", port)) as client:
                connection, _ = listener.accept()
                connection.close()
                self.assertEqual(client.recv(1), b"")
        with tempfile.TemporaryDirectory() as directory:
            campaign = object.__new__(LOADTEST.Campaign)
            campaign.output = Path(directory)
            campaign.args = SimpleNamespace(local_port=port)
            campaign.stargate_context = "stargate"
            campaign.namespace = "test"
            campaign.endpoint = "http://127.0.0.1:18000/v1"
            campaign.port_forward = None
            campaign.port_forward_log = None
            with (
                mock.patch.object(LOADTEST.socket, "create_connection"),
                mock.patch.object(LOADTEST.subprocess, "Popen") as popen,
                mock.patch.object(campaign, "log"),
            ):
                popen.return_value.poll.return_value = None
                campaign.start_port_forward()

            environment = popen.call_args.kwargs["env"]
            self.assertEqual(environment["KUBECTL_PORT_FORWARD_WEBSOCKETS"], "true")
            campaign.port_forward_log.close()

    def test_dead_forwarder_never_produces_a_completed_arm(self) -> None:
        for spark_status in (None, 0):
            with (
                self.subTest(spark_status=spark_status),
                tempfile.TemporaryDirectory() as directory,
            ):
                campaign = object.__new__(LOADTEST.Campaign)
                campaign.output = Path(directory)
                campaign.token = "test-token"
                campaign.args = SimpleNamespace(spark_pod=None)
                campaign.state = {}
                campaign.children = []
                campaign.port_forward = mock.Mock()
                campaign.port_forward.poll.return_value = 1
                run = LOADTEST.SparkRun(
                    directory=campaign.output / "arm",
                    command=["spark"],
                    expected_seconds=1,
                    metadata={"scenario": "smoke"},
                )
                process = mock.Mock()
                process.poll.return_value = spark_status
                with (
                    mock.patch.object(campaign, "verify_environment"),
                    mock.patch.object(campaign, "pod_state", return_value={}),
                    mock.patch.object(campaign, "save_state"),
                    mock.patch.object(campaign, "stop_processes") as stop,
                    mock.patch.object(
                        LOADTEST.subprocess, "Popen", return_value=process
                    ),
                    self.assertRaisesRegex(
                        LOADTEST.LoadTestError, "port-forward exited"
                    ),
                ):
                    campaign.execute([run])
                stop.assert_called_once_with([process])
                metadata = json.loads((run.directory / "metadata.json").read_text())
                self.assertNotEqual(metadata["status"], "complete")
                self.assertFalse((run.directory / "spark.txt").exists())

    def campaign(self, output, suite=None):
        args = SimpleNamespace(
            region="us-west-2",
            peer_region=[],
            output=output,
            endpoint=None,
            spark_pod=None,
            algorithm="both",
            suite="capacity",
            spark_image="spark:test",
            local_port=18000,
            resume=False,
            grafana_url="http://grafana.invalid",
        )
        return LOADTEST.Campaign(args, suite or LOADTEST.load_suite())

    def local_run(
        self,
        campaign,
        *,
        cache_hits=0,
        scenario="saturation",
        relative="saturation/r08/wait-and-widen",
    ):
        workload = campaign.workload_dir / "test.yaml"
        LOADTEST.write_workload(workload, ["unique prompt"], "test")
        run = campaign.spark_run(
            Path(relative),
            "wait-and-widen",
            scenario=scenario,
            rate=8,
            workers=1,
            workload=workload,
            requests=1,
        )
        report = {
            "summary": {"total_requests": 1, "successful": 1, "failed": 0},
            "throughput": {
                "kv_cache_hit_requests": cache_hits,
                "kv_cache_observed_requests": 1,
            },
        }
        script = (
            "from pathlib import Path; "
            f"Path({str(run.directory / 'spark.json')!r}).write_text({json.dumps(report)!r})"
        )
        run.command = [sys.executable, "-c", script]
        run.metadata["command"] = run.command
        return run

    def test_capacity_prompts_are_disjoint_from_smoke_and_every_arm(self):
        suite = LOADTEST.load_suite()
        suite["scenarios"]["smoke"]["requests"] = 2
        suite["scenarios"]["saturation"].update(rates=[2, 4], durationSeconds=1)
        with tempfile.TemporaryDirectory() as temporary:
            campaign = self.campaign(Path(temporary) / "campaign", suite)
            try:
                campaign.prepare_workloads()
                prompts = []
                for path in campaign.workload_dir.glob("*.yaml"):
                    prompts.extend(
                        yaml.safe_load(path.read_text())["scenarios"][0]["prompts"]
                    )
                self.assertEqual(len(prompts), 18)
                self.assertEqual(
                    len({prompt[:256] for prompt in prompts}), len(prompts)
                )
                arms = []
                with (
                    mock.patch.object(
                        campaign, "execute", side_effect=lambda runs: arms.extend(runs)
                    ),
                    mock.patch.object(campaign, "cooldown"),
                ):
                    campaign.run_saturation(suite["scenarios"]["saturation"])
                self.assertEqual(len({run.metadata["workload"] for run in arms}), 4)
            finally:
                campaign.close()

    def test_resume_checks_effective_suite_and_preserves_environment_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            campaign = self.campaign(Path(temporary) / "campaign")
            deployment = {
                "items": [
                    {
                        "metadata": {"name": "router"},
                        "spec": {
                            "replicas": 1,
                            "template": {
                                "spec": {
                                    "containers": [{"name": "router", "image": "old"}]
                                }
                            },
                        },
                    }
                ]
            }
            policy = {"n": 2}
            campaign.args.spark_pod = "spark"
            spark_pod = {
                "spec": {
                    "containers": [
                        {
                            "name": "spark",
                            "image": "pinned",
                            "resources": {"limits": {"cpu": "2"}},
                        }
                    ]
                },
                "status": {
                    "containerStatuses": [{"name": "spark", "imageID": "old-digest"}]
                },
            }

            def read(context, arguments):
                if arguments[1] == "configmap":
                    return json.dumps({"data": {"lb-config.json": json.dumps(policy)}})
                if arguments[1] == "pod":
                    return json.dumps(spark_pod)
                return json.dumps(deployment)

            with mock.patch.object(campaign, "kubectl", side_effect=read):
                campaign.snapshot_environment({"Id": "spark-digest"})
                path = campaign.output / "environment.json"
                original = path.read_bytes()
                for changed in ("policy", "image", "spark-image", "spark-resources"):
                    with self.subTest(changed=changed):
                        policy["n"] = 4 if changed == "policy" else 2
                        deployment["items"][0]["spec"]["template"]["spec"][
                            "containers"
                        ][0]["image"] = "new" if changed == "image" else "old"
                        spark_pod["status"]["containerStatuses"][0]["imageID"] = (
                            "new-digest" if changed == "spark-image" else "old-digest"
                        )
                        spark_pod["spec"]["containers"][0]["resources"]["limits"][
                            "cpu"
                        ] = "4" if changed == "spark-resources" else "2"
                        with self.assertRaisesRegex(
                            LOADTEST.LoadTestError, "settings changed"
                        ):
                            campaign.snapshot_environment({"Id": "spark-digest"})
                        self.assertEqual(path.read_bytes(), original)
            campaign.close()
            args = campaign.args
            args.resume = True
            changed_suite = copy.deepcopy(campaign.suite)
            changed_suite["defaults"]["maxTokens"] += 1
            with self.assertRaisesRegex(LOADTEST.LoadTestError, "resume arguments"):
                LOADTEST.Campaign(args, changed_suite)

    def test_mixed_cache_failure_rejects_both_reports(self):
        with tempfile.TemporaryDirectory() as temporary:
            campaign = self.campaign(Path(temporary) / "campaign")
            root = Path("mixed-sessions/repeat-01/wait-and-widen")
            runs = [
                self.local_run(
                    campaign,
                    scenario=f"mixed-sessions-{stream}",
                    relative=root / stream,
                )
                for stream in ("hot", "short")
            ]
            before_warmup = {"backend": {"kv_cache_hit_count": 0}}
            try:
                with (
                    mock.patch.object(campaign, "verify_environment"),
                    mock.patch.object(campaign, "pod_state", return_value={}),
                    mock.patch.object(
                        campaign,
                        "record_cache_delta",
                        side_effect=RuntimeError("cache API failed"),
                    ) as record,
                    self.assertRaisesRegex(RuntimeError, "cache API failed"),
                ):
                    campaign.execute(runs, cache_before=before_warmup)
                record.assert_called_once_with(campaign.runs / root, before_warmup)
                for run in runs:
                    self.assertIsNone(campaign.completed_report(run))
            finally:
                campaign.close()

    def test_post_run_validation_failure_never_commits_completion(self):
        for failure in ("pod", "cache", "cache-hit"):
            with (
                self.subTest(failure=failure),
                tempfile.TemporaryDirectory() as temporary,
            ):
                campaign = self.campaign(Path(temporary) / "campaign")
                run = self.local_run(campaign, cache_hits=int(failure == "cache-hit"))
                try:
                    with (
                        mock.patch.object(campaign, "verify_environment"),
                        mock.patch.object(
                            campaign,
                            "pod_state",
                            side_effect=[{}, RuntimeError("pod API failed")]
                            if failure == "pod"
                            else [{}, {}],
                        ),
                        mock.patch.object(campaign, "cache_stats", return_value={}),
                        mock.patch.object(
                            campaign,
                            "record_cache_delta",
                            side_effect=RuntimeError("cache API failed")
                            if failure == "cache"
                            else None,
                        ),
                        self.assertRaises((RuntimeError, LOADTEST.LoadTestError)),
                    ):
                        campaign.execute([run])
                    self.assertIsNone(campaign.completed_report(run))
                    self.assertFalse((run.directory / "validation.json").exists())
                finally:
                    campaign.close()

    def test_offline_render_failure_cannot_relaunch_accepted_measurement(self):
        with tempfile.TemporaryDirectory() as temporary:
            campaign = self.campaign(Path(temporary) / "campaign")
            run = self.local_run(campaign)
            try:
                with (
                    mock.patch.object(campaign, "verify_environment"),
                    mock.patch.object(campaign, "pod_state", return_value={}),
                    mock.patch.object(campaign, "cache_stats", return_value={}),
                    mock.patch.object(campaign, "record_cache_delta"),
                ):
                    reports = campaign.execute([run])
                    with mock.patch.object(
                        LOADTEST.subprocess,
                        "run",
                        return_value=CompletedProcess([], 1, "", "renderer failed"),
                    ):
                        with self.assertRaisesRegex(
                            LOADTEST.LoadTestError, "offline report rendering failed"
                        ):
                            LOADTEST.render_reports(campaign.output, "spark:test")
                    self.assertEqual(campaign.completed_report(run), reports[0])
                    with mock.patch.object(
                        LOADTEST.subprocess,
                        "Popen",
                        side_effect=AssertionError("traffic must not restart"),
                    ):
                        self.assertEqual(campaign.execute([run]), reports)
                    with mock.patch.object(
                        LOADTEST.subprocess,
                        "run",
                        return_value=CompletedProcess([], 0, "rendered", ""),
                    ):
                        LOADTEST.render_reports(campaign.output, "spark:test")
                    self.assertEqual(
                        (run.directory / "spark.txt").read_text(), "rendered"
                    )
                    (run.directory / "spark.json").write_text('{"summary": {}}')
                    with self.assertRaisesRegex(
                        LOADTEST.LoadTestError, "accepted report changed"
                    ):
                        LOADTEST.render_reports(campaign.output, "spark:test")
                    with self.assertRaisesRegex(
                        LOADTEST.LoadTestError, "artifacts changed"
                    ):
                        campaign.completed_report(run)
            finally:
                campaign.close()

    def test_remote_workload_mismatch_stops_before_traffic(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            campaign = object.__new__(LOADTEST.Campaign)
            campaign.output = Path(directory)
            campaign.workload_dir = campaign.output / "workloads"
            campaign.args = SimpleNamespace(
                spark_pod="worker", spark_image="spark:test"
            )
            campaign.stargate_context = "hub"
            campaign.namespace = "test"
            original_pod = '{"metadata":{"uid":"original-pod"}}'
            (campaign.output / "spark-pod.json").write_text(original_pod)
            LOADTEST.write_workload(
                campaign.workload_dir / "smoke.yaml", ["test"], "test"
            )

            def command(args, **kwargs):
                output = (
                    "wrong-hash  smoke.yaml" if "sha256sum" in args else "spark test"
                )
                return CompletedProcess(args, 0, output)

            def upload(args, **kwargs):
                self.assertEqual(kwargs["stdin"].read(2), b"\x1f\x8b")
                return CompletedProcess(args, 0, b"", b"")

            with (
                mock.patch.object(campaign, "kubectl", return_value="{}"),
                mock.patch.object(campaign, "command", side_effect=command),
                mock.patch.object(LOADTEST.subprocess, "run", side_effect=upload),
                self.assertRaisesRegex(LOADTEST.LoadTestError, "fingerprint differs"),
            ):
                campaign.prepare_spark_pod()
            self.assertEqual(
                (campaign.output / "spark-pod.json").read_text(), original_pod
            )

    def test_download_extracts_and_verifies_the_remote_report(self):
        contents = b'{"summary":{"total_requests":1}}\n'
        for valid_checksum in (True, False):
            with (
                self.subTest(valid_checksum=valid_checksum),
                tempfile.TemporaryDirectory() as temporary,
            ):
                campaign = self.campaign(Path(temporary) / "campaign")
                campaign.args.spark_pod = "spark"
                run = LOADTEST.SparkRun(campaign.runs / "arm", [], 1, {})
                run.directory.mkdir(parents=True)
                (run.directory / "spark.json").write_text("stale report")

                def download(command, *, stdout, **kwargs):
                    with tarfile.open(fileobj=stdout, mode="w:gz") as archive:
                        entry = tarfile.TarInfo("spark.json")
                        entry.size = len(contents)
                        archive.addfile(entry, io.BytesIO(contents))
                    return CompletedProcess(command, 0, b"", b"")

                checksum = (
                    hashlib.sha256(contents).hexdigest() if valid_checksum else "0" * 64
                )
                try:
                    with (
                        mock.patch.object(
                            LOADTEST.subprocess, "run", side_effect=download
                        ),
                        mock.patch.object(
                            campaign,
                            "command",
                            return_value=CompletedProcess(
                                [], 0, checksum + " spark.json"
                            ),
                        ),
                    ):
                        if valid_checksum:
                            campaign.download_report(run)
                            self.assertEqual(
                                (run.directory / "spark.json").read_bytes(), contents
                            )
                        else:
                            with self.assertRaisesRegex(
                                LOADTEST.LoadTestError, "checksum differs"
                            ):
                                campaign.download_report(run)
                finally:
                    campaign.close()


if __name__ == "__main__":
    unittest.main()

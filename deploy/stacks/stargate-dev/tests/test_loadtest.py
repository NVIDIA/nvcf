#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import hashlib
import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path
from subprocess import CompletedProcess
from types import SimpleNamespace
from unittest import mock

import yaml


STACK_DIR = Path(__file__).resolve().parents[1]
LOADTEST_PATH = STACK_DIR / "scripts" / "loadtest.py"
SPEC = importlib.util.spec_from_file_location("stargate_dev_loadtest", LOADTEST_PATH)
LOADTEST = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = LOADTEST
assert SPEC.loader is not None
SPEC.loader.exec_module(LOADTEST)


class CanonicalSuiteTests(unittest.TestCase):
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
            self.assertEqual(
                remote[:6], ["kubectl", "--context", "stargate", "-n", "test", "exec"]
            )
            self.assertIn("-i", remote)
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
        campaign.args = SimpleNamespace(region="us-west-2")
        attempts = [
            CompletedProcess([], 1, "three backends"),
            CompletedProcess([], 0, "verified"),
        ]
        with (
            mock.patch.object(campaign, "command", side_effect=attempts) as command,
            mock.patch.object(LOADTEST.time, "sleep") as sleep,
        ):
            campaign.verify_region()

        self.assertEqual(command.call_count, 2)
        sleep.assert_called_once_with(5)

    def test_cache_reset_restarts_stargate_after_backends(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            campaign = object.__new__(LOADTEST.Campaign)
            campaign.output = Path(directory)
            campaign.stargate_context = "stargate"
            campaign.args = SimpleNamespace(endpoint=None, spark_pod=None)
            empty = {
                "backend-0": {"kv_cache_entries": 0, "kv_cache_used_tokens": 0},
                "backend-1": {"kv_cache_entries": 0, "kv_cache_used_tokens": 0},
            }
            with (
                mock.patch.object(
                    campaign,
                    "backend_targets",
                    return_value=[("mock-a", "backend-0"), ("mock-b", "backend-1")],
                ),
                mock.patch.object(campaign, "cache_stats", side_effect=[empty, empty]),
                mock.patch.object(campaign, "kubectl", return_value="") as kubectl,
                mock.patch.object(campaign, "verify_region") as verify,
                mock.patch.object(campaign, "stop_port_forward") as stop_forward,
                mock.patch.object(campaign, "start_port_forward") as start_forward,
                mock.patch.object(campaign, "log"),
            ):
                campaign.reset_caches("test")

        self.assertIn(
            mock.call(
                "stargate", ["rollout", "restart", "deployment/llm-request-router"]
            ),
            kubectl.call_args_list,
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
                    metadata={},
                )
                process = mock.Mock()
                process.poll.return_value = spark_status
                with (
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


if __name__ == "__main__":
    unittest.main()

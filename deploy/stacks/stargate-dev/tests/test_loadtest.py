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
    def test_canonical_suite_covers_capacity_and_cache_behavior_under_two_hours(
        self,
    ) -> None:
        suite = LOADTEST.load_suite()

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
        self.assertLess(LOADTEST.measured_minutes(suite, "canonical"), 120)
        self.assertEqual(
            suite["scenarios"]["long-context-affinity"],
            {
                "kind": "long-context-affinity",
                "sessions": 32,
                "sessionBatchSize": 8,
                "inputTokensByTurn": [80_000, 100_000, 120_000],
                "repeats": 1,
                "rate": 1,
                "workers": 4,
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

        long_context = list(
            LOADTEST.long_context_prompts(3, 2, [80_000, 100_000, 120_000])
        )
        self.assertEqual(
            [5 + len(prompt) // 4 for prompt in long_context],
            [
                80_000,
                80_000,
                100_000,
                100_000,
                120_000,
                120_000,
                80_000,
                100_000,
                120_000,
            ],
        )
        self.assertEqual(long_context[0][:256], long_context[2][:256])
        self.assertEqual(long_context[2][:256], long_context[4][:256])
        self.assertEqual(long_context[6][:256], long_context[7][:256])
        self.assertNotEqual(long_context[0][:256], long_context[1][:256])
        self.assertNotEqual(long_context[0][:256], long_context[6][:256])

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
            campaign.args = SimpleNamespace(spark_image="spark:test")
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

            long_context = campaign.spark_run(
                Path("long-context/wait-and-widen"),
                "wait-and-widen",
                scenario="long-context-affinity",
                rate=1,
                workers=1,
                workload=workload,
                requests=24,
                request_slo_ms=60_000,
                max_wait_ms=60_000,
                timeout_seconds=90,
            ).command
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
            campaign.args = SimpleNamespace(endpoint=None)
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
        with tempfile.TemporaryDirectory() as directory:
            campaign = object.__new__(LOADTEST.Campaign)
            campaign.output = Path(directory)
            campaign.args = SimpleNamespace(local_port=18000)
            campaign.stargate_context = "stargate"
            campaign.namespace = "test"
            campaign.endpoint = "http://127.0.0.1:18000/v1"
            campaign.port_forward = None
            campaign.port_forward_log = None
            with (
                mock.patch.object(LOADTEST.socket, "socket"),
                mock.patch.object(LOADTEST.socket, "create_connection"),
                mock.patch.object(LOADTEST.subprocess, "Popen") as popen,
                mock.patch.object(campaign, "log"),
            ):
                popen.return_value.poll.return_value = None
                campaign.start_port_forward()

            environment = popen.call_args.kwargs["env"]
            self.assertEqual(environment["KUBECTL_PORT_FORWARD_WEBSOCKETS"], "true")
            campaign.port_forward_log.close()


if __name__ == "__main__":
    unittest.main()

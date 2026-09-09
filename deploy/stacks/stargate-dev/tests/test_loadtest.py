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
            ["smoke", "saturation", "session-affinity", "mixed-sessions"],
        )
        self.assertLess(LOADTEST.measured_minutes(suite, "canonical"), 120)

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


if __name__ == "__main__":
    unittest.main()

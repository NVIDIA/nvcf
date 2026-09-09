#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import json
import unittest
from pathlib import Path
from unittest import mock


VERIFY_PATH = Path(__file__).resolve().parents[1] / "scripts" / "verify.py"
SPEC = importlib.util.spec_from_file_location("stargate_dev_verify", VERIFY_PATH)
VERIFY = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(VERIFY)


class MetricParsingTests(unittest.TestCase):
    def test_active_backend_metrics_are_keyed_by_routing_key_and_model(self) -> None:
        metrics = """
# HELP stargate_active_inference_servers Active inference servers
stargate_active_inference_servers{model="dev-model",routing_key="stargate-dev"} 4
"""

        self.assertEqual(
            VERIFY.parse_active_backends(metrics),
            {("stargate-dev", "dev-model"): 4.0},
        )


class MockDcVerificationTests(unittest.TestCase):
    @staticmethod
    def pod(name: str, server_id: str, terminating: bool = False) -> dict:
        metadata = {"name": name}
        if terminating:
            metadata["deletionTimestamp"] = "2026-09-09T17:00:00Z"
        return {
            "metadata": metadata,
            "status": {
                "containerStatuses": [
                    {"name": "mock-dynamo", "ready": not terminating},
                    {"name": "pylon", "ready": not terminating},
                ]
            },
            "spec": {
                "containers": [
                    {"name": "mock-dynamo", "args": []},
                    {
                        "name": "pylon",
                        "args": [f"--inference-server-id={server_id}"],
                    },
                ]
            },
        }

    def test_rollout_verification_ignores_terminating_pods(self) -> None:
        mockdc = {"name": "mockdc-test", "kubeContext": "mockdc-test"}
        active = [
            self.pod("backend-0-new", "mockdc-test-backend-0"),
            self.pod("backend-1-new", "mockdc-test-backend-1"),
        ]
        terminating = [
            self.pod("backend-0-old", "mockdc-test-backend-0", terminating=True),
            self.pod("backend-1-old", "mockdc-test-backend-1", terminating=True),
        ]
        with mock.patch.object(
            VERIFY,
            "kubectl",
            return_value=json.dumps({"items": active + terminating}),
        ):
            VERIFY.verify_mockdc({"namespace": "test"}, mockdc)


if __name__ == "__main__":
    unittest.main()

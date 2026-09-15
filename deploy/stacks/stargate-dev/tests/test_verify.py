#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import json
import sys
import subprocess
import unittest
from pathlib import Path
from unittest import mock


VERIFY_PATH = Path(__file__).resolve().parents[1] / "scripts" / "verify.py"
sys.path.insert(0, str(VERIFY_PATH.parent))
SPEC = importlib.util.spec_from_file_location("stargate_dev_verify", VERIFY_PATH)
VERIFY = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(VERIFY)


class MetricParsingTests(unittest.TestCase):
    def test_transient_read_failure_is_retried_but_forbidden_is_not(self) -> None:
        for error in (
            "TLS handshake timeout",
            "http2: client connection lost",
            "http2: server sent GOAWAY and closed the connection",
            "use of closed network connection",
            "unexpected EOF",
        ):
            with (
                self.subTest(error=error),
                mock.patch.object(
                    VERIFY.subprocess,
                    "run",
                    side_effect=[
                        subprocess.CompletedProcess([], 1, "", error),
                        subprocess.CompletedProcess([], 0, "verified", ""),
                    ],
                ) as run,
                mock.patch.object(VERIFY.time, "sleep"),
            ):
                self.assertEqual(VERIFY.kubectl("test", "get", "pods"), "verified")
                self.assertEqual(run.call_count, 2)
        with mock.patch.object(
            VERIFY.subprocess,
            "run",
            return_value=subprocess.CompletedProcess([], 1, "", "Forbidden"),
        ) as run:
            with self.assertRaisesRegex(VERIFY.VerificationError, "Forbidden"):
                VERIFY.kubectl("test", "get", "pods")
            self.assertEqual(run.call_count, 1)

    def test_active_backend_metrics_are_keyed_by_routing_key_and_model(self) -> None:
        metrics = """
# HELP stargate_active_inference_servers Active inference servers
stargate_active_inference_servers{model="dev-model",routing_key="stargate-dev"} 4
"""

        self.assertEqual(
            VERIFY.parse_active_backends(metrics),
            {("stargate-dev", "dev-model"): 4.0},
        )

    def test_router_requires_backends_from_connected_regions(self) -> None:
        west = VERIFY.load_region("us-west-2")
        east = VERIFY.load_region("us-east-1")
        pods = {"items": [{"metadata": {"name": f"router-{i}"}} for i in range(3)]}
        for peers, active, expected_success in [
            ((), 4, True),
            ((east,), 8, True),
            ((east,), 4, False),
        ]:
            with self.subTest(peers=bool(peers), active=active):
                metrics = (
                    'stargate_active_inference_servers{model="stargate-dev-model",'
                    f'routing_key="stargate-dev"}} {active}\n'
                )
                with mock.patch.object(
                    VERIFY,
                    "kubectl",
                    side_effect=[json.dumps(pods), metrics, metrics, metrics],
                ):
                    if expected_success:
                        VERIFY.verify_router_metrics(west, peers)
                    else:
                        with self.assertRaisesRegex(
                            VERIFY.VerificationError, "expected 8"
                        ):
                            VERIFY.verify_router_metrics(west, peers)


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

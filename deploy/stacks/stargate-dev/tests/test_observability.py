#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import json
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import yaml


STACK_DIR = Path(__file__).resolve().parents[1]
SCRIPTS_DIR = STACK_DIR / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))
OBSERVABILITY_PATH = SCRIPTS_DIR / "observability.py"
SPEC = importlib.util.spec_from_file_location(
    "stargate_dev_observability", OBSERVABILITY_PATH
)
OBSERVABILITY = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(OBSERVABILITY)


class ObservabilityProvisioningTests(unittest.TestCase):
    def test_writer_trust_is_limited_to_each_alloy_service_account(self) -> None:
        config = {
            "observability": {"namespace": "metrics"},
            "clusters": {
                "stargate": {"name": "hub"},
                "mockdcs": [{"name": "a"}, {"name": "b"}],
            },
        }

        def provider(_config: dict, cluster: dict, account: str) -> tuple[str, str]:
            issuer = f"oidc.eks.example/{cluster['name']}"
            return issuer, f"arn:aws:iam::{account}:oidc-provider/{issuer}"

        with mock.patch.object(OBSERVABILITY, "oidc_provider", side_effect=provider):
            policy = OBSERVABILITY.writer_trust_policy(config, "123456789012")

        statements = policy["Statement"]
        self.assertEqual(len(statements), 3)
        for statement in statements:
            conditions = statement["Condition"]["StringEquals"]
            self.assertIn("sts.amazonaws.com", conditions.values())
            self.assertIn(
                "system:serviceaccount:metrics:stargate-dev-alloy",
                conditions.values(),
            )

    def test_values_update_is_atomic_private_and_preserves_other_values(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "values.yaml"
            path.write_text(
                "deploymentReady: true\nrouter:\n  hostname: example\n",
                encoding="utf-8",
            )
            path.chmod(0o644)

            OBSERVABILITY.write_observability_values(
                path,
                "ws-12345678-1234-1234-1234-123456789012",
                "arn:aws:iam::123456789012:role/stargate-dev-amp-writer",
                {
                    "id": "g-0123456789",
                    "endpoint": "g-0123456789.grafana-workspace.us-west-2.amazonaws.com",
                },
            )

            values = yaml.safe_load(path.read_text(encoding="utf-8"))
            self.assertTrue(values["deploymentReady"])
            self.assertEqual(values["router"]["hostname"], "example")
            self.assertEqual(
                values["observability"]["amp"]["workspaceId"],
                "ws-12345678-1234-1234-1234-123456789012",
            )
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_grant_admin_accepts_assignment_when_aws_reports_partial_failure(
        self,
    ) -> None:
        permission_checks = 0

        def fake_aws(service: str, operation: str, *_arguments: str) -> dict:
            nonlocal permission_checks
            if (service, operation) == ("sso-admin", "list-instances"):
                return {"Instances": [{"IdentityStoreId": "store"}]}
            if (service, operation) == ("identitystore", "list-users"):
                return {"Users": [{"UserId": "user"}]}
            if (service, operation) == ("grafana", "list-permissions"):
                permission_checks += 1
                if permission_checks == 1:
                    return {"permissions": []}
                return {"permissions": [{"user": {"id": "user"}, "role": "ADMIN"}]}
            if (service, operation) == ("grafana", "update-permissions"):
                raise OBSERVABILITY.deploy.DeploymentError("partial AWS failure")
            self.fail(f"unexpected AWS call: {service} {operation}")

        with mock.patch.object(OBSERVABILITY, "aws", side_effect=fake_aws):
            OBSERVABILITY.grant_admin("us-west-2", "g-0123456789", "user@example.com")

        self.assertEqual(permission_checks, 2)

    def test_wait_for_metrics_uses_the_region_label(self) -> None:
        response = {"data": {"result": [{"value": [0, "3"]}]}}
        with mock.patch.object(
            OBSERVABILITY, "grafana_request", return_value=response
        ) as request:
            OBSERVABILITY.wait_for_metrics("grafana.example.com", "token", "us-west-2")

        path = request.call_args.args[3]
        self.assertIn("count%28up%7Bregion%3D%22us-west-2%22%7D%29", path)


class DashboardTests(unittest.TestCase):
    def test_backend_balance_uses_existing_backend_request_and_token_counters(
        self,
    ) -> None:
        path = STACK_DIR / "dashboards" / "stargate-backend-balance.json"
        dashboard = json.loads(path.read_text(encoding="utf-8"))
        self.assertEqual(dashboard["uid"], "stargate-backend-balance")
        self.assertEqual(len(dashboard["panels"]), 2)

        request_query = dashboard["panels"][0]["targets"][0]["expr"]
        self.assertIn("stargate_requests_total", request_query)
        self.assertIn("sum by (inference_server_id)", request_query)
        self.assertIn("scalar(clamp_min", request_query)

        token_query = dashboard["panels"][1]["targets"][0]["expr"]
        self.assertIn("pylon_request_input_tokens_total", token_query)
        self.assertIn("pylon_request_output_tokens_total", token_query)
        self.assertIn("sum by (backend)", token_query)
        self.assertIn("scalar(clamp_min", token_query)

        self.assertEqual(
            [variable["name"] for variable in dashboard["templating"]["list"]],
            ["region", "model", "routing_key"],
        )


if __name__ == "__main__":
    unittest.main()

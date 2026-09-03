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

    def test_grafana_trust_is_limited_to_the_stargate_service_account(
        self,
    ) -> None:
        config = {
            "observability": {"namespace": "metrics"},
            "clusters": {"stargate": {"name": "hub"}},
        }
        with mock.patch.object(
            OBSERVABILITY,
            "oidc_provider",
            return_value=(
                "oidc.eks.example/hub",
                "arn:aws:iam::123456789012:oidc-provider/oidc.eks.example/hub",
            ),
        ):
            policy = OBSERVABILITY.grafana_trust_policy(config, "123456789012")

        statement = policy["Statement"][0]
        self.assertEqual(statement["Action"], "sts:AssumeRoleWithWebIdentity")
        self.assertEqual(
            statement["Condition"]["StringEquals"],
            {
                "oidc.eks.example/hub:aud": "sts.amazonaws.com",
                "oidc.eks.example/hub:sub": (
                    "system:serviceaccount:metrics:stargate-dev-grafana"
                ),
            },
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
                "arn:aws:iam::123456789012:role/stargate-dev-grafana",
            )

            values = yaml.safe_load(path.read_text(encoding="utf-8"))
            self.assertTrue(values["deploymentReady"])
            self.assertEqual(values["router"]["hostname"], "example")
            self.assertEqual(
                values["observability"]["amp"]["workspaceId"],
                "ws-12345678-1234-1234-1234-123456789012",
            )
            self.assertEqual(
                values["observability"]["grafana"]["readerRoleArn"],
                "arn:aws:iam::123456789012:role/stargate-dev-grafana",
            )
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)


class DashboardTests(unittest.TestCase):
    def test_stargate_dashboard_uses_sectioned_layout_with_live_metrics(
        self,
    ) -> None:
        path = STACK_DIR / "dashboards" / "stargate-services.json"
        dashboard = json.loads(path.read_text(encoding="utf-8"))
        panels = dashboard["panels"]

        self.assertEqual([panel["type"] for panel in panels[:2]], ["bargauge"] * 2)
        self.assertEqual(
            [panel["title"] for panel in panels if panel["type"] == "row"],
            ["Traffic", "Latency", "Health Check"],
        )
        request_share = next(
            panel
            for panel in panels
            if panel["title"] == "% Requests sent to each backend"
        )
        self.assertEqual(
            request_share["fieldConfig"]["defaults"]["custom"]["stacking"]["mode"],
            "percent",
        )

        queries = "\n".join(
            target["expr"] for panel in panels for target in panel.get("targets", [])
        )
        for metric in (
            "stargate_requests_total",
            "pylon_request_input_tokens_total",
            "pylon_request_output_tokens_total",
            "stargate_routing_duration_seconds_bucket",
            "stargate_proxy_duration_seconds_bucket",
            "pylon_request_time_to_first_token_seconds_bucket",
            "pylon_reverse_tunnel_connected",
        ):
            self.assertIn(metric, queries)

        self.assertEqual(
            [variable["name"] for variable in dashboard["templating"]["list"]],
            ["region", "model", "routing_key"],
        )

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

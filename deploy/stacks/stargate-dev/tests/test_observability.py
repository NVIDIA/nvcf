#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import base64
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


STACK_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(STACK_DIR / "scripts"))

import deploy  # noqa: E402
import observability as OBSERVABILITY  # noqa: E402


ACCOUNT = "123456789012"


class ObservabilityProvisioningTests(unittest.TestCase):
    CONFIG = {
        "observability": {"namespace": "metrics"},
        "clusters": {
            "stargate": {"name": "hub"},
            "mockdcs": [{"name": "a"}, {"name": "b"}],
        },
    }

    @staticmethod
    def provider(_config: dict, cluster: dict, account: str) -> tuple[str, str]:
        issuer = f"oidc.eks.example/{cluster['name']}"
        return issuer, f"arn:aws:iam::{account}:oidc-provider/{issuer}"

    def test_role_names_are_scoped_to_the_region(self) -> None:
        self.assertEqual(
            deploy.role_names("us-east-1"),
            ("stargate-dev-us-east-1-amp-writer", "stargate-dev-us-east-1-grafana"),
        )

    def test_writer_trust_is_limited_to_each_alloy_service_account(self) -> None:
        clusters = [
            self.CONFIG["clusters"]["stargate"],
            *self.CONFIG["clusters"]["mockdcs"],
        ]
        with mock.patch.object(
            OBSERVABILITY, "oidc_provider", side_effect=self.provider
        ):
            policy = OBSERVABILITY.trust_policy(
                self.CONFIG, "123456789012", clusters, "stargate-dev-alloy"
            )

        statements = policy["Statement"]
        self.assertEqual(len(statements), 3)
        for statement, cluster in zip(statements, ("hub", "a", "b")):
            self.assertEqual(
                statement["Condition"]["StringEquals"],
                {
                    f"oidc.eks.example/{cluster}:aud": "sts.amazonaws.com",
                    f"oidc.eks.example/{cluster}:sub": (
                        "system:serviceaccount:metrics:stargate-dev-alloy"
                    ),
                },
            )

    def test_grafana_trust_is_limited_to_the_stargate_service_account(
        self,
    ) -> None:
        with mock.patch.object(
            OBSERVABILITY, "oidc_provider", side_effect=self.provider
        ):
            policy = OBSERVABILITY.trust_policy(
                self.CONFIG,
                "123456789012",
                [self.CONFIG["clusters"]["stargate"]],
                "stargate-dev-grafana",
            )

        (statement,) = policy["Statement"]
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

    def provision(self, **patches: mock.Mock) -> dict:
        defaults = {
            "oidc_provider": mock.Mock(side_effect=self.provider),
            "ensure_amp": mock.Mock(
                return_value={"arn": "arn:aws:aps:amp", "workspaceId": "ws-1"}
            ),
            "ensure_role": mock.Mock(),
        }
        defaults.update(patches)
        with (
            mock.patch.object(deploy, "require_tools"),
            mock.patch.object(
                deploy, "load_deployment_values", return_value=self.CONFIG
            ),
            mock.patch.object(deploy, "aws_account", return_value="123456789012"),
            mock.patch.multiple(OBSERVABILITY, **defaults),
        ):
            return OBSERVABILITY.provision("us-west-2", Path("values.yaml"))

    def test_missing_oidc_provider_fails_before_creating_resources(self) -> None:
        ensure_amp = mock.Mock()
        ensure_role = mock.Mock()
        with self.assertRaises(deploy.DeploymentError):
            self.provision(
                oidc_provider=mock.Mock(
                    side_effect=deploy.DeploymentError("no provider")
                ),
                ensure_amp=ensure_amp,
                ensure_role=ensure_role,
            )
        ensure_amp.assert_not_called()
        ensure_role.assert_not_called()

    def test_local_regions_are_refused_before_any_aws_call(self) -> None:
        aws = mock.Mock(side_effect=AssertionError("must not call AWS"))
        with (
            tempfile.TemporaryDirectory() as directory,
            mock.patch.object(deploy, "require_tools", aws),
            mock.patch.object(deploy, "aws_account", aws),
        ):
            values = Path(directory) / "values.yaml"
            values.write_text("{}\n", encoding="utf-8")
            with self.assertRaisesRegex(deploy.DeploymentError, "local"):
                OBSERVABILITY.provision("local-a", values)

    def test_provision_returns_values_without_writing_files(self) -> None:
        ensure_role = mock.Mock()
        self.assertEqual(
            self.provision(ensure_role=ensure_role),
            {"observability": {"amp": {"workspaceId": "ws-1"}}},
        )
        self.assertEqual(
            [call.args[0] for call in ensure_role.call_args_list],
            ["stargate-dev-us-west-2-amp-writer", "stargate-dev-us-west-2-grafana"],
        )


class DeployPreflightTests(unittest.TestCase):
    CONFIG = {
        "namespace": "stargate-dev",
        "clusters": {"stargate": {"kubeContext": "hub"}, "mockdcs": []},
    }

    @staticmethod
    def secret(*keys: str) -> subprocess.CompletedProcess:
        data = {key: base64.b64encode(b"pem").decode() for key in keys}
        return subprocess.CompletedProcess(
            [], 0, stdout=json.dumps({"data": data}), stderr=""
        )

    def test_router_tls_secret_requires_the_upstream_ca(self) -> None:
        with mock.patch.object(
            deploy, "kubectl", return_value=self.secret("tls.crt", "tls.key")
        ):
            with self.assertRaisesRegex(deploy.DeploymentError, "ca.crt"):
                deploy.verify_prerequisite_resources(self.CONFIG, "stargate")

        with mock.patch.object(
            deploy,
            "kubectl",
            return_value=self.secret("tls.crt", "tls.key", "ca.crt"),
        ):
            deploy.verify_prerequisite_resources(self.CONFIG, "stargate")

    @staticmethod
    def ready_config() -> dict:
        config = deploy.load_region("us-west-2")
        deploy.merge_values(
            config, deploy.load_yaml(STACK_DIR / "values" / "versions.yaml")
        )
        config["deploymentReady"] = True
        return config

    def test_observability_phase_rejects_checked_in_placeholders(self) -> None:
        config = self.ready_config()
        with self.assertRaisesRegex(deploy.DeploymentError, "workspaceId"):
            deploy.validate_deployment_inputs(config, "observability", ACCOUNT)

        config["observability"]["amp"]["workspaceId"] = "ws-1234"
        deploy.validate_deployment_inputs(config, "observability", ACCOUNT)

    def test_observability_phase_requires_the_workspace_and_roles(self) -> None:
        config = self.ready_config()
        config["observability"]["amp"]["workspaceId"] = "ws-1234"

        def aws(status: str = "ACTIVE", missing_role: str = ""):
            def run(command, **_kwargs):
                if command[1:3] == ["amp", "describe-workspace"]:
                    workspace = {"workspace": {"status": {"statusCode": status}}}
                    return subprocess.CompletedProcess(
                        command, 0, stdout=json.dumps(workspace), stderr=""
                    )
                if missing_role and missing_role in command:
                    raise deploy.DeploymentError("aws failed: NoSuchEntity")
                return subprocess.CompletedProcess(command, 0, stdout="{}", stderr="")

            return mock.Mock(side_effect=run)

        with mock.patch.object(deploy, "run", aws()) as run:
            deploy.verify_prerequisite_resources(config, "observability")
        self.assertEqual(
            [call.args[0][4] for call in run.call_args_list[1:]],
            ["stargate-dev-us-west-2-amp-writer", "stargate-dev-us-west-2-grafana"],
        )
        for fake in (
            aws(status="CREATING"),
            aws(missing_role="stargate-dev-us-west-2-grafana"),
        ):
            with mock.patch.object(deploy, "run", fake):
                with self.assertRaisesRegex(
                    deploy.DeploymentError, "scripts/observability.py"
                ):
                    deploy.verify_prerequisite_resources(config, "observability")

    def test_stargate_phase_rejects_placeholder_images_and_hostname(self) -> None:
        config = self.ready_config()
        config["router"].update(
            {
                "acmCertificateArn": (
                    "arn:aws:acm:us-west-2:123456789012:certificate/abc"
                ),
                "loadBalancerSourceRanges": ["8.8.8.8/32"],
            }
        )
        with self.assertRaisesRegex(deploy.DeploymentError, "digest"):
            deploy.validate_deployment_inputs(config, "stargate", ACCOUNT)

        for image in config["images"].values():
            image["digest"] = "sha256:" + "a" * 64
        with self.assertRaisesRegex(deploy.DeploymentError, "hostname"):
            deploy.validate_deployment_inputs(config, "stargate", ACCOUNT)

        config["router"]["hostname"] = "router.example.com"
        deploy.validate_deployment_inputs(config, "stargate", ACCOUNT)
        with self.assertRaisesRegex(deploy.DeploymentError, "acmCertificateArn"):
            deploy.validate_deployment_inputs(config, "stargate", "210987654321")

    def test_account_is_checked_before_the_certificate_arn(self) -> None:
        with (
            tempfile.TemporaryDirectory() as directory,
            mock.patch.object(deploy, "require_tools"),
            mock.patch.object(
                deploy, "run", return_value=subprocess.CompletedProcess([], 0)
            ),
            mock.patch.object(deploy, "check_credentials"),
            mock.patch.object(
                deploy, "load_deployment_values", return_value=self.ready_config()
            ),
            mock.patch.dict("os.environ", {deploy.ACCOUNT_ENV: "not-an-account"}),
        ):
            with self.assertRaisesRegex(deploy.DeploymentError, "12-digit"):
                deploy.apply(
                    "us-west-2", "stargate", Path(directory), Path("values.yaml")
                )

    def test_context_lookup_does_not_load_raw_credentials(self) -> None:
        kubeconfig = {
            "clusters": [{"name": "c", "cluster": {"server": "https://hub/"}}],
            "contexts": [{"name": "hub", "context": {"cluster": "c"}}],
        }
        result = subprocess.CompletedProcess(
            [], 0, stdout=json.dumps(kubeconfig), stderr=""
        )
        with mock.patch.object(deploy, "run", return_value=result) as run:
            self.assertEqual(deploy.configured_contexts(), {"hub": "https://hub"})
        self.assertNotIn("--raw", run.call_args.args[0])


class DashboardTests(unittest.TestCase):
    def test_stargate_dashboard_queries_live_metrics(
        self,
    ) -> None:
        path = STACK_DIR / "dashboards" / "stargate-services.json"
        dashboard = json.loads(path.read_text(encoding="utf-8"))
        panels = dashboard["panels"]
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


if __name__ == "__main__":
    unittest.main()

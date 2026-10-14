#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import contextlib
import importlib.util
import io
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import yaml

from test_invariants import helmfile, render


STACK_DIR = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "stargate_dev_deploy_local", STACK_DIR / "scripts" / "deploy.py"
)
DEPLOY = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(DEPLOY)

LOCAL_REGIONS = ("local-a", "local-b")
HUB_ADDRESS = "172.30.0.3"
DIGEST = "sha256:" + "a" * 64
CREDENTIALS = {
    "serviceToken": "service-test-token-that-is-long-enough",
    "workerToken": "shared-worker-test-token-that-is-long-enough",
}


def without_account(environment: dict[str, str]) -> dict[str, str]:
    environment = dict(environment)
    environment.pop(DEPLOY.ACCOUNT_ENV, None)
    return environment


class LocalDeployTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        root = Path(self.temporary.name)
        self.credentials = root / "credentials.json"
        self.credentials.write_text(json.dumps(CREDENTIALS), encoding="utf-8")
        self.credentials.chmod(0o600)
        self.values = root / "values.yaml"
        self.write_values(HUB_ADDRESS)

    def write_values(self, address: str) -> None:
        values = {
            "deploymentReady": True,
            "images": {
                name: {"repository": f"registry.test/{name}", "digest": DIGEST}
                for name in ("stargate", "auth", "mockDynamo", "pylon")
            },
            "router": {"address": address},
        }
        self.values.write_text(yaml.safe_dump(values), encoding="utf-8")

    def fake_run(
        self, contexts: list[str], ingress_ip: str = HUB_ADDRESS, nodes: int = 3
    ):
        calls = []

        def run(command, *, env=None, check=True):
            calls.append(command)
            if command[0] == "aws":
                raise AssertionError("local mode must not call the AWS CLI")
            if command[:2] == ["helm", "diff"]:
                stdout = "3.15.0"
            elif command[1:3] == ["config", "view"]:
                stdout = json.dumps(
                    {
                        "clusters": [
                            {"name": name, "cluster": {"server": "https://0.0.0.0:1"}}
                            for name in contexts
                        ],
                        "contexts": [
                            {"name": name, "context": {"cluster": name}}
                            for name in contexts
                        ],
                    }
                )
            elif "secret" in command:
                stdout = json.dumps(
                    {
                        "data": {
                            key: "cGVt" for key in ("tls.crt", "tls.key", "ca.crt")
                        }
                    }
                )
            elif "nodes" in command:
                ready = {"conditions": [{"type": "Ready", "status": "True"}]}
                stdout = json.dumps(
                    {"items": [{"spec": {}, "status": ready}] * nodes}
                )
            elif "service" in command:
                stdout = json.dumps(
                    {"status": {"loadBalancer": {"ingress": [{"ip": ingress_ip}]}}}
                )
            else:
                raise AssertionError(f"unexpected command: {command}")
            return subprocess.CompletedProcess(command, 0, stdout=stdout, stderr="")

        return run, calls

    def apply(self, phase: str, contexts: list[str], **kwargs):
        run, calls = self.fake_run(contexts, **kwargs)
        helmfile = mock.Mock(return_value=subprocess.CompletedProcess([], 0))
        with (
            mock.patch.object(DEPLOY, "run", side_effect=run),
            mock.patch.object(DEPLOY.shutil, "which", return_value="/usr/bin/tool"),
            mock.patch.object(DEPLOY.subprocess, "run", helmfile),
            mock.patch.dict(os.environ, without_account(os.environ), clear=True),
            contextlib.redirect_stdout(io.StringIO()),
        ):
            DEPLOY.apply("local-a", phase, self.credentials, self.values)
        return calls, helmfile

    def contexts(self) -> list[str]:
        region = DEPLOY.load_region("local-a")
        return [
            cluster["kubeContext"]
            for cluster in [
                region["clusters"]["stargate"],
                *region["clusters"]["mockdcs"],
            ]
        ]

    def test_local_regions_use_k3d_contexts_and_skip_observability(self) -> None:
        for name in LOCAL_REGIONS:
            with self.subTest(region=name):
                region = DEPLOY.load_region(name)
                self.assertTrue(DEPLOY.is_local(region))
                clusters = [
                    region["clusters"]["stargate"],
                    *region["clusters"]["mockdcs"],
                ]
                # k3d names each context k3d-<cluster name>.
                for cluster in clusters:
                    self.assertEqual(cluster["kubeContext"], f"k3d-{cluster['name']}")
                self.assertNotIn("observability", region)
                self.assertNotIn("acmCertificateArn", region["router"])

    def test_aws_regions_default_to_the_aws_platform(self) -> None:
        self.assertFalse(DEPLOY.is_local(DEPLOY.load_region("us-west-2")))

    def test_values_file_cannot_change_the_platform_or_topology(self) -> None:
        for key, value in (
            ("platform", "local"),
            ("platform", "aws"),
            ("region", "us-east-1"),
            ("namespace", "other"),
            ("modelName", "other"),
            ("routingKey", "other"),
            ("clusters", {"stargate": {"kubeContext": "other"}}),
        ):
            with self.subTest(key=key, value=value):
                self.values.write_text(yaml.safe_dump({key: value}), encoding="utf-8")
                with self.assertRaisesRegex(DEPLOY.DeploymentError, key):
                    DEPLOY.load_deployment_values("us-west-2", self.values)

    def test_stargate_phase_runs_without_aws(self) -> None:
        calls, helmfile = self.apply("stargate", self.contexts())
        self.assertFalse(any(command[0] == "aws" for command in calls))
        self.assertFalse(any("auth" in command for command in calls))
        (call,) = helmfile.call_args_list
        self.assertEqual(
            call.args[0],
            [
                "helmfile",
                "--environment",
                "local-a",
                "--selector",
                "phase=stargate",
                "apply",
                "--suppress-secrets",
            ],
        )
        self.assertNotIn(DEPLOY.ACCOUNT_ENV, call.kwargs["env"])
        self.assertEqual(
            call.kwargs["env"]["STARGATE_DEV_VALUES_FILE"], str(self.values.resolve())
        )

    def test_mockdc_phase_requires_the_service_on_router_address(self) -> None:
        self.apply("mockdc", self.contexts())
        with self.assertRaisesRegex(
            DEPLOY.DeploymentError,
            rf"router.address {HUB_ADDRESS} \(ingress IPs: 172.30.0.9\)"
            ".*local.py images",
        ):
            self.apply("mockdc", self.contexts(), ingress_ip="172.30.0.9")

    def test_mockdc_phase_requires_two_schedulable_nodes(self) -> None:
        with self.assertRaisesRegex(DEPLOY.DeploymentError, "1 Ready schedulable"):
            self.apply("mockdc", self.contexts(), nodes=1)

    def test_missing_context_names_the_local_bootstrap(self) -> None:
        with self.assertRaisesRegex(DEPLOY.DeploymentError, "local.py up"):
            self.apply("stargate", [])

    def test_observability_phase_is_rejected_before_any_command(self) -> None:
        with self.assertRaisesRegex(DEPLOY.DeploymentError, "not supported"):
            self.apply("observability", self.contexts())

    def test_router_address_must_be_a_real_ip(self) -> None:
        for address in ("", "hub.local", "192.0.2.1"):
            with self.subTest(address=address):
                self.write_values(address)
                with self.assertRaisesRegex(DEPLOY.DeploymentError, "router.address"):
                    self.apply("stargate", self.contexts())


@unittest.skipUnless(
    shutil.which("helmfile") and shutil.which("helm"), "helmfile and helm are required"
)
class LocalRenderTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.temporary = tempfile.TemporaryDirectory()
        cls.credential_path = Path(cls.temporary.name) / "credentials.json"
        cls.credential_path.write_text(json.dumps(CREDENTIALS), encoding="utf-8")
        cls.credential_path.chmod(0o600)

    @classmethod
    def tearDownClass(cls) -> None:
        cls.temporary.cleanup()

    def test_local_regions_render_without_aws(self) -> None:
        for region_name in LOCAL_REGIONS:
            with self.subTest(region=region_name):
                region = DEPLOY.load_region(region_name)
                peer = f"http://{HUB_ADDRESS}:50071"
                resources = render(
                    region_name,
                    {"router": {"address": HUB_ADDRESS, "remoteWatchUrls": [peer]}},
                    self.credential_path,
                    account=None,
                )
                text = yaml.safe_dump_all(resources)
                self.assertNotIn("amazonaws", text)
                self.assertNotIn("eks.amazonaws.com", text)
                self.assertNotIn("observability", text)

                (service,) = [
                    resource
                    for resource in resources
                    if resource["kind"] == "Service"
                    and resource["spec"].get("type") == "LoadBalancer"
                ]
                self.assertEqual(
                    service["metadata"]["name"], "llm-request-router-backend-router"
                )
                self.assertNotIn("annotations", service["metadata"])
                self.assertNotIn("loadBalancerClass", service["spec"])
                self.assertNotIn("loadBalancerSourceRanges", service["spec"])
                self.assertEqual(
                    {(port["port"], port["protocol"]) for port in service["spec"]["ports"]},
                    {(50071, "TCP"), (50072, "UDP")},
                )

                deployments = {
                    resource["metadata"]["name"]: resource
                    for resource in resources
                    if resource["kind"] == "Deployment"
                }
                for name in ("llm-request-router", "llm-request-router-backend-router"):
                    pod = deployments[name]["spec"]["template"]["spec"]
                    args = pod["containers"][0]["args"]
                    self.assertIn(
                        f"--grpc-pylon-dial-addr=http://{HUB_ADDRESS}:50071", args
                    )
                    self.assertIn(f"--remote-stargate-url={peer}", args)
                    self.assertIn("--allow-insecure-remote-watch-http", args)
                    self.assertIn(
                        "--advertised-hostname-template="
                        f"{{pod_name}}.{region['router']['hostname']}",
                        args,
                    )
                    secrets = [
                        volume.get("secret", {}).get("secretName")
                        for volume in pod["volumes"]
                    ]
                    self.assertIn("stargate-router-tls", secrets)
                router_args = deployments["llm-request-router"]["spec"]["template"][
                    "spec"
                ]["containers"][0]["args"]
                self.assertIn(
                    f"--reverse-tunnel-pylon-dial-addr={HUB_ADDRESS}:50072",
                    router_args,
                )

                pylons = [
                    container
                    for deployment in deployments.values()
                    for container in deployment["spec"]["template"]["spec"][
                        "containers"
                    ]
                    if container["name"] == "pylon"
                ]
                self.assertEqual(len(pylons), 4)
                for pylon in pylons:
                    self.assertIn(
                        f"--stargate-address=http://{HUB_ADDRESS}:50071", pylon["args"]
                    )
                    self.assertIn(
                        "--tls-cert-path=/var/run/stargate/tls/ca.crt", pylon["args"]
                    )
                    self.assertFalse(
                        any(
                            argument.startswith("--grpc-tls-ca-cert-path")
                            for argument in pylon["args"]
                        )
                    )

    def test_local_releases_target_k3d_contexts_without_observability(self) -> None:
        state = helmfile("local-a", {}, self.credential_path, "build", account=None)
        releases = next(
            document["releases"]
            for document in yaml.safe_load_all(state)
            if document and "releases" in document
        )
        clusters = DEPLOY.load_region("local-a")["clusters"]
        hub = clusters["stargate"]["kubeContext"]
        self.assertEqual(
            {release["name"]: release["kubeContext"] for release in releases},
            {
                "stargate-dev-auth": hub,
                "llm-request-router": hub,
                **{
                    mockdc["name"]: mockdc["kubeContext"]
                    for mockdc in clusters["mockdcs"]
                },
            },
        )


if __name__ == "__main__":
    unittest.main()

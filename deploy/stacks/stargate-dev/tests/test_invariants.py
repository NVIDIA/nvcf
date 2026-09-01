#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import json
import os
import re
import shutil
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml


STACK_DIR = Path(__file__).resolve().parents[1]
DIGEST_IMAGE = re.compile(r"^[^@]+@sha256:[a-f0-9]{64}$")


class DeploymentInitTests(unittest.TestCase):
    def test_init_creates_one_reusable_mode_0600_bundle(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "credentials.json"
            command = [
                "python3",
                str(STACK_DIR / "scripts" / "deploy.py"),
                "init",
                "--region",
                "us-west-2",
                "--credentials",
                str(path),
            ]
            subprocess.run(
                command, cwd=STACK_DIR, check=True, capture_output=True, text=True
            )
            first = path.read_bytes()
            mode = stat.S_IMODE(path.stat().st_mode)
            self.assertEqual(mode, 0o600)

            credentials = json.loads(first)
            self.assertEqual(credentials["region"], "us-west-2")
            self.assertEqual(
                set(credentials["workers"]), {"mockdc-usw2-a", "mockdc-usw2-b"}
            )
            self.assertEqual(len(set(credentials["workers"].values())), 2)

            repeated = subprocess.run(
                command, cwd=STACK_DIR, capture_output=True, text=True
            )
            self.assertNotEqual(repeated.returncode, 0)
            self.assertEqual(path.read_bytes(), first)


@unittest.skipUnless(
    shutil.which("helmfile") and shutil.which("helm"), "helmfile and helm are required"
)
class RenderedStackTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.temporary = tempfile.TemporaryDirectory()
        root = Path(cls.temporary.name)
        cls.credentials = {
            "region": "us-west-2",
            "serviceToken": "service-test-token-that-is-long-enough",
            "clientToken": "client-test-token-that-is-long-enough-00",
            "workers": {
                "mockdc-usw2-a": "worker-a-test-token-that-is-long-enough",
                "mockdc-usw2-b": "worker-b-test-token-that-is-long-enough",
            },
        }
        credential_path = root / "credentials.json"
        credential_path.write_text(json.dumps(cls.credentials), encoding="utf-8")
        credential_path.chmod(0o600)
        cls.output_dir = root / "rendered"
        cls.output_dir.mkdir(mode=0o700)
        values_path = root / "values.yaml"
        values_path.write_text("{}\n", encoding="utf-8")
        environment = os.environ.copy()
        environment["STARGATE_DEV_CREDENTIALS_FILE"] = str(credential_path)
        environment["STARGATE_DEV_VALUES_FILE"] = str(values_path)

        subprocess.run(
            [
                "helmfile",
                "--environment",
                "us-west-2",
                "template",
                "--output-dir",
                str(cls.output_dir),
            ],
            cwd=STACK_DIR,
            env=environment,
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
            text=True,
        )
        state = subprocess.run(
            ["helmfile", "--environment", "us-west-2", "build"],
            cwd=STACK_DIR,
            env=environment,
            check=True,
            capture_output=True,
            text=True,
        ).stdout
        cls.state_documents = [
            document for document in yaml.safe_load_all(state) if document
        ]

        cls.rendered: list[tuple[Path, dict]] = []
        cls.contents: dict[Path, str] = {}
        for path in cls.output_dir.rglob("*.yaml"):
            text = path.read_text(encoding="utf-8")
            cls.contents[path] = text
            for document in yaml.safe_load_all(text):
                if isinstance(document, dict) and document.get("kind"):
                    cls.rendered.append((path, document))

    @classmethod
    def tearDownClass(cls) -> None:
        cls.temporary.cleanup()

    def resources(self, kind: str) -> list[tuple[Path, dict]]:
        return [
            (path, resource)
            for path, resource in self.rendered
            if resource["kind"] == kind
        ]

    def test_every_release_has_the_intended_explicit_context(self) -> None:
        releases = next(
            document["releases"]
            for document in self.state_documents
            if "releases" in document
        )
        actual = {release["name"]: release.get("kubeContext") for release in releases}
        self.assertEqual(
            actual,
            {
                "stargate-dev-auth": "stargate-usw2",
                "llm-request-router": "stargate-usw2",
                "mockdc-usw2-a": "mockdc-usw2-a",
                "mockdc-usw2-b": "mockdc-usw2-b",
                "metrics-stargate-usw2": "stargate-usw2",
                "metrics-mockdc-usw2-a": "mockdc-usw2-a",
                "metrics-mockdc-usw2-b": "mockdc-usw2-b",
            },
        )

    def test_fixed_topology_and_derived_backend_identities(self) -> None:
        deployments = self.resources("Deployment")
        replicas = {
            resource["metadata"]["name"]: resource["spec"]["replicas"]
            for _, resource in deployments
        }
        self.assertEqual(replicas["stargate-dev-auth"], 1)
        self.assertEqual(replicas["llm-request-router"], 3)
        self.assertEqual(replicas["llm-request-router-backend-router"], 3)

        backend_router = next(
            resource
            for _, resource in deployments
            if resource["metadata"]["name"] == "llm-request-router-backend-router"
        )
        backend_router_pod = backend_router["spec"]["template"]["spec"]
        self.assertIn(
            "--upstream-tls-cert-path=/var/run/stargate/tls/ca.crt",
            backend_router_pod["containers"][0]["args"],
        )
        tls_volume = next(
            volume
            for volume in backend_router_pod["volumes"]
            if volume["name"] == "stargate-tls"
        )
        self.assertIn(
            {"key": "ca.crt", "path": "ca.crt"},
            tls_volume["secret"]["items"],
        )

        inference_server_ids = set()
        cluster_ids = set()
        for path, deployment in deployments:
            pylon = next(
                (
                    container
                    for container in deployment["spec"]["template"]["spec"][
                        "containers"
                    ]
                    if container["name"] == "pylon"
                ),
                None,
            )
            if pylon is None:
                continue
            arguments = pylon["args"]
            inference_server_ids.add(
                next(
                    value.split("=", 1)[1]
                    for value in arguments
                    if value.startswith("--inference-server-id=")
                )
            )
            cluster_ids.add(
                next(
                    value.split("=", 1)[1]
                    for value in arguments
                    if value.startswith("--cluster-id=")
                )
            )
            self.assertFalse(
                any(value.startswith("--initial-input-tps") for value in arguments)
            )
            self.assertIn(
                "--grpc-tls-ca-cert-path=/var/run/stargate/tls/ca.crt", arguments
            )
            self.assertNotIn("--do-calibration", arguments)
            self.assertIn("mockdc-usw2", str(path))
            self.assertEqual(
                deployment["spec"]["template"]["metadata"]["labels"][
                    "stargate.nvidia.com/backend-id"
                ],
                next(
                    value.split("=", 1)[1]
                    for value in arguments
                    if value.startswith("--inference-server-id=")
                ),
            )

        self.assertEqual(cluster_ids, {"mockdc-usw2-a", "mockdc-usw2-b"})
        self.assertEqual(
            inference_server_ids,
            {
                "mockdc-usw2-a-backend-0",
                "mockdc-usw2-a-backend-1",
                "mockdc-usw2-b-backend-0",
                "mockdc-usw2-b-backend-1",
            },
        )

    def test_images_secrets_and_metrics_follow_one_contract(self) -> None:
        for _, deployment in self.resources("Deployment"):
            pod = deployment["spec"]["template"]
            if not any(
                container["name"] == "alloy" for container in pod["spec"]["containers"]
            ):
                self.assertEqual(
                    pod["metadata"]["annotations"]["prometheus.io/scrape"],
                    "true",
                )
            for container in pod["spec"]["containers"]:
                self.assertRegex(container["image"], DIGEST_IMAGE)

        secret_files = {path for path, resource in self.resources("Secret")}
        for path, text in self.contents.items():
            for token in [
                self.credentials["serviceToken"],
                self.credentials["clientToken"],
                *self.credentials["workers"].values(),
            ]:
                if token in text:
                    self.assertIn(path, secret_files)

        router_deployment = next(
            resource
            for _, resource in self.resources("Deployment")
            if resource["metadata"]["name"] == "llm-request-router"
        )
        volumes = router_deployment["spec"]["template"]["spec"]["volumes"]
        self.assertIn(
            "stargate-dev-auth",
            [volume.get("secret", {}).get("secretName") for volume in volumes],
        )
        self.assertFalse(
            any("vault.hashicorp.com" in text for text in self.contents.values())
        )

    def test_only_router_registration_and_quic_are_load_balanced(self) -> None:
        load_balancers = [
            resource
            for _, resource in self.resources("Service")
            if resource["spec"].get("type") == "LoadBalancer"
        ]
        self.assertEqual(len(load_balancers), 1)
        service = load_balancers[0]
        self.assertEqual(
            service["metadata"]["name"], "llm-request-router-backend-router"
        )
        self.assertEqual(service["spec"]["loadBalancerClass"], "service.k8s.aws/nlb")
        self.assertEqual(
            {(port["port"], port["protocol"]) for port in service["spec"]["ports"]},
            {(50071, "TCP"), (50072, "UDP")},
        )
        self.assertEqual(
            service["metadata"]["annotations"][
                "service.beta.kubernetes.io/aws-load-balancer-scheme"
            ],
            "internet-facing",
        )
        self.assertEqual(
            service["metadata"]["annotations"][
                "service.beta.kubernetes.io/aws-load-balancer-backend-protocol"
            ],
            "tcp",
        )
        self.assertEqual(
            service["metadata"]["annotations"][
                "service.beta.kubernetes.io/aws-load-balancer-alpn-policy"
            ],
            "HTTP2Only",
        )

    def test_observability_is_one_namespace_scoped_collector_per_cluster(self) -> None:
        alloy_deployments = [
            resource
            for _, resource in self.resources("Deployment")
            if resource["metadata"]["name"] == "stargate-dev-alloy"
        ]
        self.assertEqual(len(alloy_deployments), 3)
        for deployment in alloy_deployments:
            self.assertEqual(
                deployment["metadata"]["namespace"], "stargate-dev-observability"
            )
            self.assertEqual(deployment["spec"]["replicas"], 1)
            pod = deployment["spec"]["template"]["spec"]
            self.assertTrue(pod["securityContext"]["runAsNonRoot"])
            self.assertEqual(pod["securityContext"]["runAsUser"], 473)
            self.assertEqual(pod["securityContext"]["runAsGroup"], 473)
            self.assertEqual(pod["securityContext"]["fsGroup"], 473)
            alloy = next(
                container
                for container in pod["containers"]
                if container["name"] == "alloy"
            )
            self.assertTrue(alloy["securityContext"]["readOnlyRootFilesystem"])
            self.assertEqual(alloy["securityContext"]["runAsUser"], 473)
            self.assertEqual(alloy["securityContext"]["runAsGroup"], 473)

        service_accounts = [
            resource
            for _, resource in self.resources("ServiceAccount")
            if resource["metadata"]["name"] == "stargate-dev-alloy"
        ]
        self.assertEqual(len(service_accounts), 3)
        for service_account in service_accounts:
            self.assertEqual(
                service_account["metadata"]["annotations"][
                    "eks.amazonaws.com/role-arn"
                ],
                "arn:aws:iam::000000000000:role/stargate-dev-amp-writer",
            )

        self.assertFalse(self.resources("ClusterRole"))
        alloy_services = [
            resource
            for _, resource in self.resources("Service")
            if resource["metadata"]["name"] == "stargate-dev-alloy"
        ]
        self.assertFalse(alloy_services)

        configs = [
            resource["data"]["config.alloy"]
            for _, resource in self.resources("ConfigMap")
            if "config.alloy" in resource.get("data", {})
        ]
        self.assertEqual(len(configs), 3)
        for config in configs:
            self.assertIn('names = ["stargate-dev"]', config)
            self.assertIn('action        = "keepequal"', config)
            self.assertIn('target_label  = "backend"', config)
            self.assertIn('cluster_role = "', config)
            self.assertIn('prometheus.remote_write "amp"', config)
            self.assertIn("sigv4", config)


if __name__ == "__main__":
    unittest.main()

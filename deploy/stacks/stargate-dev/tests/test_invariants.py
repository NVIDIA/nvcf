#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import hashlib
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
ACCOUNT_ID = "123456789012"
DIGEST_IMAGE = re.compile(r"^[^@]+@sha256:[a-f0-9]{64}$")


def helmfile(
    region: str,
    values: dict,
    credential_path: Path,
    *arguments: str,
    account: str | None = ACCOUNT_ID,
) -> str:
    """Run helmfile for a region with values written beside the credentials."""
    values_path = credential_path.parent / f"{region}-values.yaml"
    values_path.write_text(yaml.safe_dump(values), encoding="utf-8")
    environment = dict(
        os.environ,
        STARGATE_DEV_VALUES_FILE=str(values_path),
        STARGATE_DEV_CREDENTIALS_FILE=str(credential_path),
    )
    environment.pop("STARGATE_DEV_AWS_ACCOUNT_ID", None)
    if account:
        environment["STARGATE_DEV_AWS_ACCOUNT_ID"] = account
    return subprocess.run(
        ["helmfile", "-e", region, *arguments],
        cwd=STACK_DIR,
        env=environment,
        check=True,
        capture_output=True,
        text=True,
    ).stdout


def render(
    region: str,
    values: dict,
    credential_path: Path,
    *selector: str,
    account: str | None = ACCOUNT_ID,
) -> list[dict]:
    rendered = helmfile(
        region,
        values,
        credential_path,
        *selector,
        "template",
        "--skip-deps",
        account=account,
    )
    return [
        resource
        for resource in yaml.safe_load_all(rendered)
        if isinstance(resource, dict) and resource.get("kind")
    ]


class DeploymentInitTests(unittest.TestCase):
    def test_init_creates_one_reusable_mode_0600_bundle(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "credentials.json"
            command = [
                "python3",
                str(STACK_DIR / "scripts" / "deploy.py"),
                "init",
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
            self.assertEqual(set(credentials), {"serviceToken", "workerToken"})
            self.assertGreaterEqual(len(credentials["workerToken"]), 32)
            self.assertNotEqual(
                credentials["serviceToken"], credentials["workerToken"]
            )

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
            "serviceToken": "service-test-token-that-is-long-enough",
            "workerToken": "shared-worker-test-token-that-is-long-enough",
        }
        credential_path = cls.credential_path = root / "credentials.json"
        credential_path.write_text(json.dumps(cls.credentials), encoding="utf-8")
        credential_path.chmod(0o600)
        cls.output_dir = root / "rendered"
        cls.output_dir.mkdir(mode=0o700)
        values_path = root / "values.yaml"
        values_path.write_text("{}\n", encoding="utf-8")
        environment = os.environ.copy()
        environment["STARGATE_DEV_CREDENTIALS_FILE"] = str(credential_path)
        environment["STARGATE_DEV_VALUES_FILE"] = str(values_path)
        environment["STARGATE_DEV_AWS_ACCOUNT_ID"] = ACCOUNT_ID

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

    def render(
        self, region_name: str, values: dict, *selector: str
    ) -> list[dict]:
        # setUpClass already added the chart repositories.
        return render(region_name, values, self.credential_path, *selector)

    def test_routing_policy_override_preserves_omitted_queue_bounds(self) -> None:
        policy = {
            "default": "power-of-n",
            "models": {
                "stargate-dev-model": {
                    "algorithm": "wait-and-widen",
                    "comparator": "utilization",
                    "cache_affinity_backend_selection_count": 2,
                    "cache_affinity_wait_ms": 350,
                    "max_queued": 8,
                    "ttft_bucket_size_ms": 100,
                }
            },
        }
        resources = self.render(
            "us-west-2",
            {"router": {"loadBalancerConfig": policy}},
            "-l",
            "phase=stargate",
        )
        configmap = next(
            resource
            for resource in resources
            if resource.get("kind") == "ConfigMap"
            and resource["metadata"]["name"] == "llm-request-router-lb"
        )
        self.assertEqual(json.loads(configmap["data"]["lb-config.json"]), policy)

    def test_auth_pod_rolls_when_its_credential_bundle_changes(self) -> None:
        def auth_checksum(values: dict) -> str:
            resources = self.render("us-west-2", values, "-l", "component=auth")
            config = next(
                resource
                for resource in resources
                if resource.get("kind") == "Secret"
            )["stringData"]["config.json"]
            annotation = next(
                resource
                for resource in resources
                if resource.get("kind") == "Deployment"
            )["spec"]["template"]["metadata"]["annotations"]["checksum/credentials"]
            self.assertEqual(annotation, hashlib.sha256(config.encode()).hexdigest())
            return annotation

        self.assertNotEqual(
            auth_checksum({}), auth_checksum({"routingKey": "stargate-dev-rotated"})
        )

    def test_peer_regions_render_remote_discovery_and_regional_identities(
        self,
    ) -> None:
        # Rendering also checks every region file against the chart schemas.
        # Peer-region Pylons register with the us-west-2 hub, so its dev auth
        # must accept their worker token and routing key.
        west_auth = json.loads(
            next(
                resource
                for _, resource in self.resources("Secret")
                if resource["metadata"]["name"] == "stargate-dev-auth-credentials"
            )["stringData"]["config.json"]
        )
        remote = "https://router.usw2.stargate-dev.example.invalid:50071"
        for region_name, code in [
            ("us-east-1", "ue1"),
            ("eu-west-1", "ew1"),
            ("ap-northeast-1", "an1"),
            ("ap-southeast-2", "as2"),
        ]:
            with self.subTest(region=region_name):
                resources = self.render(
                    region_name, {"router": {"remoteWatchUrls": [remote]}}
                )
                deployments = {
                    resource["metadata"]["name"]: resource
                    for resource in resources
                    if resource.get("kind") == "Deployment"
                }
                router = deployments["llm-request-router"]["spec"]["template"][
                    "spec"
                ]["containers"][0]
                self.assertIn(f"--remote-stargate-url={remote}", router["args"])
                pylon_tokens = {
                    resource["stringData"]["token"]
                    for resource in resources
                    if resource.get("kind") == "Secret"
                    and resource["metadata"]["name"].endswith(
                        "-mockdc-worker-credentials"
                    )
                }
                self.assertEqual(pylon_tokens, {west_auth["workerToken"]})
                peer_auth = json.loads(
                    next(
                        resource
                        for resource in resources
                        if resource.get("kind") == "Secret"
                        and resource["metadata"]["name"]
                        == "stargate-dev-auth-credentials"
                    )["stringData"]["config.json"]
                )
                self.assertEqual(peer_auth, west_auth)
                actual = set()
                for name, deployment in deployments.items():
                    if not name.startswith("mockdc-"):
                        continue
                    pylon = next(
                        container
                        for container in deployment["spec"]["template"]["spec"][
                            "containers"
                        ]
                        if container["name"] == "pylon"
                    )
                    actual.update(
                        argument.split("=", 1)[1]
                        for argument in pylon["args"]
                        if argument.startswith("--inference-server-id=")
                    )
                self.assertEqual(
                    actual,
                    {
                        f"mockdc-{code}-{dc}-backend-{index}"
                        for dc in ("a", "b")
                        for index in range(2)
                    },
                )
                roles = {
                    resource["metadata"]
                    .get("annotations", {})
                    .get("eks.amazonaws.com/role-arn")
                    for resource in resources
                    if resource.get("kind") == "ServiceAccount"
                }
                self.assertIn(
                    f"arn:aws:iam::{ACCOUNT_ID}:role/stargate-dev-{region_name}-amp-writer",
                    roles,
                )

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
                "stargate-dev-grafana": "stargate-usw2",
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

        auth_secret = next(
            resource
            for _, resource in self.resources("Secret")
            if resource["metadata"]["name"] == "stargate-dev-auth-credentials"
        )
        auth_config = json.loads(auth_secret["stringData"]["config.json"])
        self.assertEqual(
            auth_config,
            {**self.credentials, "routingKey": "stargate-dev"},
        )

        mockdc_worker_secrets = [
            resource
            for _, resource in self.resources("Secret")
            if resource["metadata"]["name"].endswith("-mockdc-worker-credentials")
        ]
        self.assertEqual(len(mockdc_worker_secrets), 2)
        self.assertEqual(
            {secret["stringData"]["token"] for secret in mockdc_worker_secrets},
            {self.credentials["workerToken"]},
        )

        router = next(
            resource
            for _, resource in deployments
            if resource["metadata"]["name"] == "llm-request-router"
        )
        router_args = router["spec"]["template"]["spec"]["containers"][0]["args"]
        self.assertIn(
            "--lb-config-path=/etc/llm-request-router/lb-config.json",
            router_args,
        )
        self.assertFalse(
            any(arg.startswith("--readiness-stabilization-") for arg in router_args)
        )
        load_balancer_config = next(
            resource
            for _, resource in self.resources("ConfigMap")
            if resource["metadata"]["name"] == "llm-request-router-lb"
        )
        raw_load_balancer_config = load_balancer_config["data"]["lb-config.json"]
        model_policy = json.loads(raw_load_balancer_config)["models"][
            "stargate-dev-model"
        ]
        self.assertEqual(model_policy["algorithm"], "wait-and-widen")
        self.assertEqual(model_policy["cache_affinity_wait_ms"], 200)
        self.assertEqual(model_policy["max_queued"], 1)
        self.assertEqual(model_policy["max_queue_time_floor_ms"], 100)
        self.assertEqual(model_policy["max_queue_time_ceil_ms"], 5000)
        self.assertEqual(
            router["spec"]["template"]["metadata"]["annotations"][
                "checksum/load-balancer-config"
            ],
            hashlib.sha256(raw_load_balancer_config.encode()).hexdigest(),
        )

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
        self.assertEqual(tls_volume["secret"]["secretName"], "stargate-router-tls")
        self.assertIn(
            {"key": "ca.crt", "path": "ca.crt"},
            tls_volume["secret"]["items"],
        )
        for deployment in (router, backend_router):
            terms = deployment["spec"]["template"]["spec"]["affinity"][
                "podAntiAffinity"
            ]["preferredDuringSchedulingIgnoredDuringExecution"]
            self.assertEqual(
                {term["podAffinityTerm"]["topologyKey"] for term in terms},
                {"kubernetes.io/hostname", "topology.kubernetes.io/zone"},
            )

        region = yaml.safe_load(
            (STACK_DIR / "environments" / "us-west-2.yaml").read_text()
        )
        backend_settings = {
            f"{mockdc['name']}-{backend.replace('backend', 'backend-')}": settings
            for mockdc in region["clusters"]["mockdcs"]
            for backend, settings in mockdc["mockDynamo"].items()
        }
        backend_ids = set()
        for path, deployment in deployments:
            pod = deployment["spec"]["template"]
            containers = {
                container["name"]: container for container in pod["spec"]["containers"]
            }
            if "pylon" not in containers:
                continue
            self.assertIn("mockdc-usw2", str(path))
            backend_id = pod["metadata"]["labels"]["stargate.nvidia.com/backend-id"]
            backend_ids.add(backend_id)
            gpu_workers = backend_settings[backend_id]["numGpuWorkers"]
            # mock-dynamo's dev_chart_argv_shapes_parse test parses this argv.
            self.assertEqual(
                containers["mock-dynamo"]["args"],
                [
                    "--http-listen-addr=0.0.0.0:8090",
                    "--model-name=stargate-dev-model",
                    "--engine-model=batched",
                    f"--num-gpu-workers={gpu_workers}",
                    "--kv-cache-capacity-tokens=1000000",
                    "--context-length-tokens=131072",
                    "--output-tokens-min=128",
                    "--output-tokens-max=8192",
                    "--output-token-distribution=gaussian",
                ],
            )
            self.assertEqual(
                containers["pylon"]["args"],
                [
                    "--upstream-http-base-url=http://127.0.0.1:8090",
                    "--upstream-health-path=/health",
                    "--model-name=stargate-dev-model",
                    f"--stargate-address=https://{region['router']['hostname']}:50071",
                    f"--inference-server-id={backend_id}",
                    f"--cluster-id={backend_id}",
                    "--backend-connectivity=reverse",
                    "--tunnel-protocol=raw-quic",
                    "--tls-cert-path=/var/run/stargate/tls/ca.crt",
                    "--grpc-tls-ca-cert-path=/var/run/stargate/tls/ca.crt",
                    "--auth-token-file=/var/run/stargate/auth/token",
                    "--kv-cache-stats-path=/kv-cache/stats",
                    "--active-canary-interval-ms=0",
                    "--engine-stats-stream=off",
                    f"--max-engine-concurrency={gpu_workers * 25}",
                    "--do-calibration",
                    "--calibration-max-concurrency=25",
                ],
            )
            self.assertEqual(deployment["spec"]["strategy"], {"type": "Recreate"})
            self.assertIn(
                "stargate-router-ca",
                [
                    volume.get("secret", {}).get("secretName")
                    for volume in pod["spec"]["volumes"]
                ],
            )
            anti_affinity = pod["spec"]["affinity"]["podAntiAffinity"]
            self.assertEqual(
                anti_affinity["requiredDuringSchedulingIgnoredDuringExecution"][0][
                    "topologyKey"
                ],
                "kubernetes.io/hostname",
            )

        self.assertEqual(
            backend_ids,
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
            # Alloy scrapes the stack namespace; it and Grafana are not scraped.
            if deployment["metadata"]["name"] not in {
                "stargate-dev-alloy",
                "stargate-dev-grafana",
            }:
                self.assertEqual(
                    pod["metadata"]["annotations"]["prometheus.io/scrape"],
                    "true",
                )
            containers = pod["spec"]["containers"] + pod["spec"].get(
                "initContainers", []
            )
            for container in containers:
                self.assertRegex(container["image"], DIGEST_IMAGE)

        secret_files = {path for path, resource in self.resources("Secret")}
        for path, text in self.contents.items():
            for token in self.credentials.values():
                if token in text:
                    self.assertIn(path, secret_files)

        router_deployment = next(
            resource
            for _, resource in self.resources("Deployment")
            if resource["metadata"]["name"] == "llm-request-router"
        )
        volumes = router_deployment["spec"]["template"]["spec"]["volumes"]
        self.assertIn(
            "stargate-dev-auth-credentials",
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
        region = yaml.safe_load(
            (STACK_DIR / "environments" / "us-west-2.yaml").read_text()
        )
        self.assertEqual(
            service["spec"]["loadBalancerSourceRanges"],
            region["router"]["loadBalancerSourceRanges"],
        )
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
                f"arn:aws:iam::{ACCOUNT_ID}:role/stargate-dev-us-west-2-amp-writer",
            )

        self.assertFalse(self.resources("ClusterRole"))
        alloy_services = [
            resource
            for _, resource in self.resources("Service")
            if resource["metadata"]["name"] == "stargate-dev-alloy"
        ]
        self.assertFalse(alloy_services)

        grafana = next(
            resource
            for _, resource in self.resources("Deployment")
            if resource["metadata"]["name"] == "stargate-dev-grafana"
        )
        self.assertEqual(grafana["metadata"]["namespace"], "stargate-dev-observability")
        self.assertEqual(grafana["spec"]["replicas"], 1)
        self.assertFalse(
            grafana["spec"]["template"]["spec"]["automountServiceAccountToken"]
        )
        grafana_service_account = next(
            resource
            for _, resource in self.resources("ServiceAccount")
            if resource["metadata"]["name"] == "stargate-dev-grafana"
        )
        self.assertEqual(
            grafana_service_account["metadata"]["annotations"][
                "eks.amazonaws.com/role-arn"
            ],
            f"arn:aws:iam::{ACCOUNT_ID}:role/stargate-dev-us-west-2-grafana",
        )
        grafana_service = next(
            resource
            for _, resource in self.resources("Service")
            if resource["metadata"]["name"] == "stargate-dev-grafana"
        )
        self.assertEqual(grafana_service["spec"]["type"], "ClusterIP")

        grafana_config = next(
            resource
            for _, resource in self.resources("ConfigMap")
            if "datasources.yaml" in resource.get("data", {})
        )
        datasource = yaml.safe_load(grafana_config["data"]["datasources.yaml"])[
            "datasources"
        ][0]
        self.assertEqual(datasource["uid"], "stargate-dev-amp")
        self.assertEqual(datasource["type"], "grafana-amazonprometheus-datasource")
        self.assertEqual(datasource["jsonData"]["sigV4AuthType"], "default")

        dashboard_names = {
            key
            for _, resource in self.resources("ConfigMap")
            for key in resource.get("binaryData", {})
            if key.endswith(".json")
        }
        self.assertEqual(
            dashboard_names,
            {"stargate-services.json"},
        )

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

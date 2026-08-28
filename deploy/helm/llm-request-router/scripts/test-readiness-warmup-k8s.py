#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import argparse
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import time
from pathlib import Path


RELEASE = "readiness-test"
NAMESPACE = "stargate-readiness"
STARGATE_SERVICE = "llm-request-router"
HEADLESS_SERVICE = "llm-request-router-headless"
BACKEND_ROUTER_SERVICE = "llm-request-router-backend-router"
MODEL = "warmup-model"
CURL_IMAGE = "curlimages/curl:8.12.1"


class TestFailure(RuntimeError):
    pass


def command_text(command: list[str]) -> str:
    return shlex.join(command)


def run(
    command: list[str],
    *,
    input_text: str | None = None,
    capture: bool = False,
    check: bool = True,
) -> subprocess.CompletedProcess[str]:
    print(f"+ {command_text(command)}", flush=True)
    result = subprocess.run(
        command,
        input=input_text,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
        check=False,
    )
    if check and result.returncode != 0:
        output = "\n".join(
            part.strip()
            for part in (result.stdout, result.stderr)
            if part and part.strip()
        )
        suffix = f"\n{output}" if output else ""
        raise TestFailure(
            f"command failed with exit code {result.returncode}: "
            f"{command_text(command)}{suffix}"
        )
    return result


def output(command: list[str]) -> str:
    return run(command, capture=True).stdout.strip()


def require_tools() -> None:
    missing = [
        tool
        for tool in ("docker", "helm", "kind", "kubectl")
        if shutil.which(tool) is None
    ]
    if missing:
        raise TestFailure(f"missing required tools: {', '.join(missing)}")


def retry(description: str, timeout_seconds: float, check) -> object:
    deadline = time.monotonic() + timeout_seconds
    last_error: Exception | None = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result is not None and result is not False:
                return result
        except (TestFailure, json.JSONDecodeError) as error:
            last_error = error
        time.sleep(0.5)
    detail = f": {last_error}" if last_error else ""
    raise TestFailure(f"timed out waiting for {description}{detail}")


class KubernetesTest:
    def __init__(self, cluster_name: str, warmup_ms: int, keep_cluster: bool):
        self.cluster_name = cluster_name
        self.context = f"kind-{cluster_name}"
        self.warmup_ms = warmup_ms
        self.keep_cluster = keep_cluster
        self.cluster_created = False
        self.repo_root = Path(__file__).resolve().parents[4]
        self.chart_dir = (
            self.repo_root / "deploy/helm/llm-request-router/llm-request-router"
        )
        self.image_context = self.repo_root / "src/libraries/rust/stargate"
        image_suffix = cluster_name.removeprefix("stargate-readiness-")
        self.image_tag = image_suffix
        self.stargate_repository = "stargate-readiness-stargate"
        self.pylon_repository = "stargate-readiness-pylon"
        self.mock_repository = "stargate-readiness-mock-dynamo"
        self.curl_repository = "stargate-readiness-curl"
        self.stargate_image = f"{self.stargate_repository}:{self.image_tag}"
        self.pylon_image = f"{self.pylon_repository}:{self.image_tag}"
        self.mock_image = f"{self.mock_repository}:{self.image_tag}"
        self.curl_image = f"{self.curl_repository}:{self.image_tag}"

    def kubectl(
        self,
        *args: str,
        input_text: str | None = None,
        capture: bool = False,
        check: bool = True,
    ):
        return run(
            ["kubectl", "--context", self.context, *args],
            input_text=input_text,
            capture=capture,
            check=check,
        )

    def kubectl_output(self, *args: str) -> str:
        return self.kubectl(*args, capture=True).stdout.strip()

    def kubectl_json(self, *args: str) -> dict:
        raw = self.kubectl_output(*args)
        try:
            return json.loads(raw)
        except json.JSONDecodeError as error:
            raise TestFailure(f"invalid kubectl JSON: {raw}") from error

    def build_images(self) -> None:
        for target, image in (
            ("stargate-runtime", self.stargate_image),
            ("pylon-runtime", self.pylon_image),
            ("mock-dynamo-runtime", self.mock_image),
        ):
            run(
                [
                    "docker",
                    "build",
                    "--provenance=false",
                    "--build-arg",
                    "CARGO_PROFILE=integration",
                    "--target",
                    target,
                    "--tag",
                    image,
                    str(self.image_context),
                ]
            )
        run(
            [
                "docker",
                "build",
                "--provenance=false",
                "--tag",
                self.curl_image,
                "-",
            ],
            input_text=f"FROM {CURL_IMAGE}\n",
        )

    def create_cluster(self) -> None:
        existing = set(output(["kind", "get", "clusters"]).splitlines())
        if self.cluster_name in existing:
            raise TestFailure(
                f"refusing to reuse existing kind cluster {self.cluster_name}"
            )
        self.cluster_created = True
        run(
            ["kind", "create", "cluster", "--name", self.cluster_name, "--wait", "120s"]
        )
        for image in (
            self.stargate_image,
            self.pylon_image,
            self.mock_image,
            self.curl_image,
        ):
            run(
                [
                    "kind",
                    "load",
                    "docker-image",
                    "--name",
                    self.cluster_name,
                    image,
                ]
            )
        version = self.kubectl_json("version", "-o", "json")["serverVersion"][
            "gitVersion"
        ]
        print(f"Kubernetes server: {version}", flush=True)

    def install_chart(self) -> None:
        run(
            [
                "helm",
                "upgrade",
                "--install",
                RELEASE,
                str(self.chart_dir),
                "--kube-context",
                self.context,
                "--namespace",
                NAMESPACE,
                "--create-namespace",
                "--set",
                "llmRequestRouter.replicaCount=1",
                "--set",
                "llmRequestRouter.backendRouter.enabled=true",
                "--set",
                "llmRequestRouter.backendRouter.replicaCount=1",
                "--set",
                "llmRequestRouter.backendRouter.podDisruptionBudget.enabled=false",
                "--set-string",
                f"llmRequestRouter.image.repository={self.stargate_repository}",
                "--set-string",
                f"llmRequestRouter.image.tag={self.image_tag}",
                "--set",
                "llmRequestRouter.image.pullPolicy=Never",
                "--set",
                "llmRequestRouter.vault.noVaultAnnotations=true",
                "--set-string",
                "llmRequestRouter.auth.workerAuthEndpoint=",
                "--set",
                f"llmRequestRouter.readiness.warmupMs={self.warmup_ms}",
            ]
        )
        self.kubectl(
            "-n",
            NAMESPACE,
            "rollout",
            "status",
            f"deployment/{BACKEND_ROUTER_SERVICE}",
            "--timeout=60s",
        )

    def workload_pod(self) -> dict:
        pods = self.kubectl_json(
            "-n",
            NAMESPACE,
            "get",
            "pods",
            "-l",
            f"app.kubernetes.io/name={STARGATE_SERVICE},app.kubernetes.io/instance={RELEASE}",
            "-o",
            "json",
        )["items"]
        if len(pods) != 1:
            return {}
        return pods[0]

    def wait_for_running_workload(self) -> dict:
        def running_pod():
            pod = self.workload_pod()
            if pod and pod.get("status", {}).get("phase") == "Running":
                return pod
            return None

        return retry("Stargate pod to be running", 30, running_pod)

    def endpoint_conditions(self, service: str, pod_name: str) -> dict | None:
        slices = self.kubectl_json(
            "-n",
            NAMESPACE,
            "get",
            "endpointslices",
            "-l",
            f"kubernetes.io/service-name={service}",
            "-o",
            "json",
        )["items"]
        for endpoint_slice in slices:
            for endpoint in endpoint_slice.get("endpoints", []):
                target = endpoint.get("targetRef", {})
                if target.get("kind") == "Pod" and target.get("name") == pod_name:
                    return endpoint.get("conditions", {})
        return None

    def assert_warming_endpoint_slices(self, pod_name: str) -> None:
        def warming_conditions():
            normal = self.endpoint_conditions(STARGATE_SERVICE, pod_name)
            headless = self.endpoint_conditions(HEADLESS_SERVICE, pod_name)
            if normal is None or headless is None:
                return None
            if normal.get("ready") is not False or normal.get("serving") is not False:
                return None
            if (
                headless.get("ready") is not True
                or headless.get("serving") is not False
            ):
                return None
            if normal.get("terminating") is True or headless.get("terminating") is True:
                return None
            return normal, headless

        normal, headless = retry(
            "warming EndpointSlice conditions for both Services", 20, warming_conditions
        )
        print(
            "Warming EndpointSlices: "
            f"normal={json.dumps(normal, sort_keys=True)} "
            f"headless={json.dumps(headless, sort_keys=True)}",
            flush=True,
        )

    def apply_test_clients(self) -> None:
        manifest = {
            "apiVersion": "v1",
            "kind": "Pod",
            "metadata": {"name": "readiness-probe", "namespace": NAMESPACE},
            "spec": {
                "containers": [
                    {
                        "name": "curl",
                        "image": self.curl_image,
                        "imagePullPolicy": "Never",
                        "command": ["sleep", "600"],
                    }
                ],
                "restartPolicy": "Never",
            },
        }
        self.kubectl("apply", "-f", "-", input_text=json.dumps(manifest))

        pylon_manifest = {
            "apiVersion": "apps/v1",
            "kind": "Deployment",
            "metadata": {"name": "readiness-pylon", "namespace": NAMESPACE},
            "spec": {
                "replicas": 1,
                "selector": {"matchLabels": {"app": "readiness-pylon"}},
                "template": {
                    "metadata": {"labels": {"app": "readiness-pylon"}},
                    "spec": {
                        "containers": [
                            {
                                "name": "pylon",
                                "image": self.pylon_image,
                                "imagePullPolicy": "Never",
                                "args": [
                                    "--upstream-http-base-url=http://127.0.0.1:8090",
                                    f"--model-name={MODEL}",
                                    f"--stargate-address={BACKEND_ROUTER_SERVICE}:50071",
                                    "--inference-server-id=readiness-pylon",
                                    "--backend-connectivity=reverse",
                                    "--quic-insecure",
                                    "--disable-bringup",
                                    "--active-canary-interval-ms=0",
                                    "--engine-stats-stream=off",
                                    "--initial-input-tps=100",
                                    "--min-update-interval-ms=100",
                                ],
                            },
                            {
                                "name": "mock-dynamo",
                                "image": self.mock_image,
                                "imagePullPolicy": "Never",
                                "args": [
                                    "--http-listen-addr=0.0.0.0:8090",
                                    f"--model-name={MODEL}",
                                    "--token-delay-ms=1",
                                ],
                            },
                        ]
                    },
                },
            },
        }
        self.kubectl("apply", "-f", "-", input_text=json.dumps(pylon_manifest))
        self.kubectl(
            "-n",
            NAMESPACE,
            "wait",
            "--for=condition=Ready",
            "pod/readiness-probe",
            "--timeout=30s",
        )
        self.kubectl(
            "-n",
            NAMESPACE,
            "rollout",
            "status",
            "deployment/readiness-pylon",
            "--timeout=30s",
        )

    def curl(self, url: str, *args: str, check: bool = True):
        return self.kubectl(
            "-n",
            NAMESPACE,
            "exec",
            "readiness-probe",
            "--",
            "curl",
            "--silent",
            "--show-error",
            "--connect-timeout",
            "1",
            "--max-time",
            "3",
            *args,
            url,
            capture=True,
            check=check,
        )

    def status(self, url: str) -> str:
        return self.curl(
            url, "--output", "/dev/null", "--write-out", "%{http_code}"
        ).stdout

    def models(self, url: str) -> list[str]:
        result = self.curl(url)
        try:
            body = json.loads(result.stdout)
        except json.JSONDecodeError as error:
            raise TestFailure(
                f"invalid model response from {url}: {result.stdout}"
            ) from error
        return body.get("model_ids", [])

    def assert_registration_during_warmup(self, pod_ip: str) -> None:
        direct_url = f"http://{pod_ip}:8000"
        ready_status = self.status(f"{direct_url}/readyz")
        if ready_status != "503":
            raise TestFailure(
                f"expected direct /readyz status 503 during warmup, got {ready_status}"
            )

        normal_service_health = self.curl(
            f"http://{STARGATE_SERVICE}:8000/healthz", "--fail", check=False
        )
        if normal_service_health.returncode == 0:
            raise TestFailure(
                "normal Service routed traffic to the warming Stargate pod"
            )

        def registered_model():
            models = self.models(f"{direct_url}/v1/models")
            return models if MODEL in models else None

        models = retry(
            "Pylon registration through the backend router", 25, registered_model
        )
        ready_status = self.status(f"{direct_url}/readyz")
        if ready_status != "503":
            raise TestFailure(
                "Stargate became ready before Pylon registration was observed; "
                f"expected 503, got {ready_status}"
            )
        print(
            f"Pylon registered {models} while direct /readyz remained 503 and the normal Service rejected traffic",
            flush=True,
        )

    def assert_ready_transition(self, pod_name: str) -> None:
        self.kubectl(
            "-n",
            NAMESPACE,
            "wait",
            "--for=condition=Ready",
            f"pod/{pod_name}",
            "--timeout=90s",
        )

        def ready_conditions():
            normal = self.endpoint_conditions(STARGATE_SERVICE, pod_name)
            headless = self.endpoint_conditions(HEADLESS_SERVICE, pod_name)
            if not normal or not headless:
                return None
            if normal.get("ready") is not True or normal.get("serving") is not True:
                return None
            if headless.get("ready") is not True or headless.get("serving") is not True:
                return None
            return normal, headless

        normal, headless = retry(
            "ready EndpointSlice conditions for both Services", 20, ready_conditions
        )
        service_url = f"http://{STARGATE_SERVICE}:8000"
        if self.status(f"{service_url}/readyz") != "200":
            raise TestFailure(
                "normal Service did not return 200 from /readyz after warmup"
            )
        models = self.models(f"{service_url}/v1/models")
        if MODEL not in models:
            raise TestFailure(
                f"normal Service model response omitted {MODEL}: {models}"
            )
        print(
            "Ready EndpointSlices: "
            f"normal={json.dumps(normal, sort_keys=True)} "
            f"headless={json.dumps(headless, sort_keys=True)}",
            flush=True,
        )
        print(
            f"Normal Service returned /readyz=200 and models={models} after warmup",
            flush=True,
        )

    def collect_diagnostics(self) -> None:
        if not self.cluster_created:
            return
        print("Collecting Kubernetes diagnostics", file=sys.stderr, flush=True)
        commands = [
            ("get", "pods", "-o", "wide"),
            ("get", "services", "-o", "wide"),
            ("get", "endpointslices", "-o", "yaml"),
            ("describe", "pods"),
            ("logs", "deployment/readiness-pylon", "-c", "pylon"),
            ("logs", f"deployment/{STARGATE_SERVICE}"),
            ("logs", f"deployment/{BACKEND_ROUTER_SERVICE}"),
        ]
        for command in commands:
            self.kubectl("-n", NAMESPACE, *command, check=False)

    def cleanup(self) -> None:
        if not self.cluster_created:
            return
        if self.keep_cluster:
            print(
                f"Keeping kind cluster {self.cluster_name}; delete it with: "
                f"kind delete cluster --name {self.cluster_name}",
                flush=True,
            )
            return
        run(["kind", "delete", "cluster", "--name", self.cluster_name], check=False)

    def execute(self) -> None:
        self.build_images()
        try:
            self.create_cluster()
            self.install_chart()
            pod = self.wait_for_running_workload()
            pod_name = pod["metadata"]["name"]
            pod_ip = pod["status"]["podIP"]
            self.assert_warming_endpoint_slices(pod_name)
            self.apply_test_clients()
            self.assert_registration_during_warmup(pod_ip)
            self.assert_ready_transition(pod_name)
            print("Kubernetes readiness warmup integration test passed", flush=True)
        except Exception:
            self.collect_diagnostics()
            raise
        finally:
            self.cleanup()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Test Stargate readiness warmup against a disposable kind cluster."
    )
    parser.add_argument(
        "--cluster-name",
        default=f"stargate-readiness-{os.getpid()}",
        help="unique kind cluster name",
    )
    parser.add_argument(
        "--warmup-ms",
        type=int,
        default=60_000,
        help="Stargate readiness warmup used by the test",
    )
    parser.add_argument(
        "--keep-cluster",
        action="store_true",
        help="keep the test-created cluster for debugging",
    )
    args = parser.parse_args()
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,40}", args.cluster_name):
        parser.error(
            "--cluster-name must be a lowercase DNS label of at most 41 characters"
        )
    if args.warmup_ms < 30_000:
        parser.error(
            "--warmup-ms must be at least 30000 to leave time for warming assertions"
        )
    return args


def main() -> int:
    args = parse_args()
    try:
        require_tools()
        KubernetesTest(args.cluster_name, args.warmup_ms, args.keep_cluster).execute()
    except (OSError, TestFailure) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())

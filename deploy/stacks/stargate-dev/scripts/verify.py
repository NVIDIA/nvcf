#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

import yaml


STACK_DIR = Path(__file__).resolve().parents[1]
ACTIVE_BACKENDS = "stargate_active_inference_servers"


class VerificationError(RuntimeError):
    pass


def load_region(region: str) -> dict:
    path = STACK_DIR / "environments" / f"{region}.yaml"
    if not path.is_file():
        raise VerificationError(f"unsupported region: {region}")
    with path.open(encoding="utf-8") as file:
        config = yaml.safe_load(file)
    if not isinstance(config, dict) or config.get("region") != region:
        raise VerificationError(f"invalid region file: {path}")
    return config


def kubectl(context: str, *arguments: str) -> str:
    result = subprocess.run(
        ["kubectl", "--context", context, *arguments],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if result.returncode:
        raise VerificationError(
            result.stderr.strip() or f"kubectl failed for context {context}"
        )
    return result.stdout


def require_deployment(context: str, namespace: str, name: str, replicas: int) -> None:
    deployment = json.loads(
        kubectl(context, "-n", namespace, "get", "deployment", name, "-o", "json")
    )
    status = deployment.get("status", {})
    if deployment["spec"]["replicas"] != replicas:
        raise VerificationError(
            f"Deployment {name} is configured for the wrong replica count"
        )
    for field in ("readyReplicas", "updatedReplicas", "availableReplicas"):
        if status.get(field, 0) != replicas:
            raise VerificationError(
                f"Deployment {name} has {status.get(field, 0)} {field}; expected {replicas}"
            )
    if status.get("observedGeneration", 0) < deployment["metadata"]["generation"]:
        raise VerificationError(
            f"Deployment {name} has not observed its current generation"
        )


def parse_active_backends(metrics: str) -> dict[tuple[str, str], float]:
    values = {}
    for line in metrics.splitlines():
        if not line.startswith(f"{ACTIVE_BACKENDS}{'{'}"):
            continue
        labels_text, value_text = line.split("}", 1)
        labels = dict(re.findall(r'(\w+)="([^"]*)"', labels_text))
        values[(labels.get("routing_key", ""), labels.get("model", ""))] = float(
            value_text.strip()
        )
    return values


def verify_router_metrics(config: dict) -> None:
    context = config["clusters"]["stargate"]["kubeContext"]
    namespace = config["namespace"]
    pod_list = json.loads(
        kubectl(
            context,
            "-n",
            namespace,
            "get",
            "pods",
            "-l",
            "app.kubernetes.io/name=llm-request-router,app.kubernetes.io/instance=llm-request-router",
            "-o",
            "json",
        )
    )["items"]
    if len(pod_list) != 3:
        raise VerificationError(f"found {len(pod_list)} Stargate Pods; expected 3")
    expected = {
        (mockdc["name"], config["modelName"]): 2.0
        for mockdc in config["clusters"]["mockdcs"]
    }
    for pod in pod_list:
        name = pod["metadata"]["name"]
        path = f"/api/v1/namespaces/{namespace}/pods/{name}:9090/proxy/metrics"
        active = parse_active_backends(kubectl(context, "get", f"--raw={path}"))
        for key, count in expected.items():
            if active.get(key) != count:
                raise VerificationError(
                    f"Stargate Pod {name} reports {active.get(key, 0)} active backends for {key}; expected {count}"
                )


def verify_registration_endpoint(config: dict) -> None:
    context = config["clusters"]["stargate"]["kubeContext"]
    namespace = config["namespace"]
    service = json.loads(
        kubectl(
            context,
            "-n",
            namespace,
            "get",
            "service",
            "llm-request-router-backend-router",
            "-o",
            "json",
        )
    )
    if service["spec"].get("type") != "LoadBalancer":
        raise VerificationError("backend-router Service is not a LoadBalancer")
    ports = {(port["port"], port["protocol"]) for port in service["spec"]["ports"]}
    if ports != {(50071, "TCP"), (50072, "UDP")}:
        raise VerificationError(
            f"backend-router Service exposes unexpected ports: {ports}"
        )
    if not service.get("status", {}).get("loadBalancer", {}).get("ingress"):
        raise VerificationError(
            "backend-router LoadBalancer does not have an ingress endpoint"
        )


def verify_stargate(config: dict, require_backends: bool) -> None:
    context = config["clusters"]["stargate"]["kubeContext"]
    namespace = config["namespace"]
    require_deployment(context, namespace, "stargate-dev-auth", 1)
    require_deployment(context, namespace, "llm-request-router", 3)
    require_deployment(context, namespace, "llm-request-router-backend-router", 3)
    verify_registration_endpoint(config)
    if require_backends:
        verify_router_metrics(config)


def verify_mockdc(config: dict, mockdc: dict) -> None:
    context = mockdc["kubeContext"]
    namespace = config["namespace"]
    pods = json.loads(
        kubectl(
            context,
            "-n",
            namespace,
            "get",
            "pods",
            "-l",
            f"app.kubernetes.io/instance={mockdc['name']}",
            "-o",
            "json",
        )
    )["items"]
    if len(pods) != 2:
        raise VerificationError(
            f"MockDC {mockdc['name']} has {len(pods)} Pods; expected 2"
        )

    actual_ids = set()
    for pod in pods:
        statuses = {
            status["name"]: status["ready"]
            for status in pod["status"].get("containerStatuses", [])
        }
        if statuses != {"mock-dynamo": True, "pylon": True}:
            raise VerificationError(
                f"MockDC Pod {pod['metadata']['name']} is not fully ready"
            )
        pylon = next(
            container
            for container in pod["spec"]["containers"]
            if container["name"] == "pylon"
        )
        actual_ids.add(
            next(
                argument.split("=", 1)[1]
                for argument in pylon["args"]
                if argument.startswith("--inference-server-id=")
            )
        )
    expected_ids = {f"{mockdc['name']}-backend-0", f"{mockdc['name']}-backend-1"}
    if actual_ids != expected_ids:
        raise VerificationError(
            f"MockDC {mockdc['name']} has unexpected Pylon identities"
        )


def verify_observability(config: dict) -> None:
    namespace = config["observability"]["namespace"]
    clusters = [config["clusters"]["stargate"], *config["clusters"]["mockdcs"]]
    for cluster in clusters:
        context = cluster["kubeContext"]
        require_deployment(context, namespace, "stargate-dev-alloy", 1)
        service_account = json.loads(
            kubectl(
                context,
                "-n",
                namespace,
                "get",
                "serviceaccount",
                "stargate-dev-alloy",
                "-o",
                "json",
            )
        )
        role_arn = (
            service_account["metadata"]
            .get("annotations", {})
            .get("eks.amazonaws.com/role-arn", "")
        )
        if not re.fullmatch(
            r"arn:aws:iam::[0-9]{12}:role/stargate-dev-amp-writer", role_arn
        ):
            raise VerificationError(
                f"Alloy in {cluster['name']} does not have the AMP writer role"
            )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Verify one deployed Stargate dev region"
    )
    parser.add_argument("--region", required=True)
    parser.add_argument(
        "--phase",
        choices=("stargate", "mockdc", "observability", "regional"),
        required=True,
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        config = load_region(args.region)
        if args.phase in ("stargate", "regional"):
            verify_stargate(config, require_backends=args.phase == "regional")
        if args.phase in ("mockdc", "regional"):
            for mockdc in config["clusters"]["mockdcs"]:
                verify_mockdc(config, mockdc)
        if args.phase in ("observability", "regional"):
            verify_observability(config)
        print(f"verified {args.phase} phase for {args.region}")
    except (
        VerificationError,
        FileNotFoundError,
        json.JSONDecodeError,
        OSError,
        yaml.YAMLError,
    ) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

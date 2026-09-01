#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import argparse
import base64
import ipaddress
import json
import os
import re
import secrets
import shutil
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml


STACK_DIR = Path(__file__).resolve().parents[1]
SHA256_DIGEST = re.compile(r"^sha256:[a-f0-9]{64}$")
AMP_WORKSPACE_ID = re.compile(
    r"^ws-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
)
GRAFANA_WORKSPACE_ID = re.compile(r"^g-[0-9a-f]{10}$")


class DeploymentError(RuntimeError):
    pass


def load_yaml(path: Path) -> dict:
    with path.open(encoding="utf-8") as file:
        value = yaml.safe_load(file)
    if not isinstance(value, dict):
        raise DeploymentError(f"{path} must contain a YAML object")
    return value


def load_region(region: str) -> dict:
    path = STACK_DIR / "environments" / f"{region}.yaml"
    if not path.is_file():
        raise DeploymentError(f"unsupported region: {region}")
    config = load_yaml(path)
    if config.get("region") != region:
        raise DeploymentError(f"{path} region does not match {region}")
    mockdcs = config.get("clusters", {}).get("mockdcs", [])
    names = [mockdc.get("name") for mockdc in mockdcs]
    if len(names) != 2 or any(not name for name in names) or len(set(names)) != 2:
        raise DeploymentError(f"{path} must define two distinct MockDC clusters")
    if config.get("clusters", {}).get("stargate", {}).get("name") in names:
        raise DeploymentError("the Stargate and MockDC cluster names must be distinct")
    return config


def run(
    command: list[str],
    *,
    env: dict[str, str] | None = None,
    check: bool = True,
) -> subprocess.CompletedProcess[str]:
    result = subprocess.run(
        command,
        cwd=STACK_DIR,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if check and result.returncode:
        message = result.stderr.strip() or result.stdout.strip() or "command failed"
        raise DeploymentError(f"{command[0]} failed: {message}")
    return result


def require_tools(*names: str) -> None:
    missing = [name for name in names if shutil.which(name) is None]
    if missing:
        raise DeploymentError(f"required command not found: {', '.join(missing)}")


def credential_values(config: dict, credentials: dict) -> list[str]:
    values = [credentials.get("serviceToken"), credentials.get("clientToken")]
    workers = credentials.get("workers")
    if not isinstance(workers, dict):
        raise DeploymentError("credential bundle workers must be an object")
    expected_workers = {mockdc["name"] for mockdc in config["clusters"]["mockdcs"]}
    if set(workers) != expected_workers:
        raise DeploymentError("credential bundle worker names do not match the region")
    values.extend(workers[name] for name in sorted(workers))
    if any(not isinstance(value, str) or len(value) < 32 for value in values):
        raise DeploymentError("credential bundle contains an invalid token")
    if len(set(values)) != len(values):
        raise DeploymentError("credential bundle tokens must be distinct")
    return values


def load_credentials(path: Path, config: dict) -> dict:
    path = path.expanduser().resolve()
    info = path.stat()
    if not stat.S_ISREG(info.st_mode):
        raise DeploymentError(f"credential path is not a regular file: {path}")
    if info.st_uid != os.getuid() or info.st_mode & 0o077:
        raise DeploymentError(
            f"credential file must be owned by the current user with mode 0600: {path}"
        )
    with path.open(encoding="utf-8") as file:
        credentials = json.load(file)
    if not isinstance(credentials, dict):
        raise DeploymentError("credential bundle must contain a JSON object")
    if credentials.get("region") != config["region"]:
        raise DeploymentError("credential bundle region does not match")
    credential_values(config, credentials)
    return credentials


def init_credentials(region: str, path: Path) -> None:
    config = load_region(region)
    path = path.expanduser().resolve()
    if not path.parent.is_dir():
        raise DeploymentError(
            f"credential parent directory does not exist: {path.parent}"
        )
    credentials = {
        "region": region,
        "serviceToken": secrets.token_urlsafe(32),
        "clientToken": secrets.token_urlsafe(32),
        "workers": {
            mockdc["name"]: secrets.token_urlsafe(32)
            for mockdc in config["clusters"]["mockdcs"]
        },
    }
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as file:
            json.dump(credentials, file, indent=2)
            file.write("\n")
    except BaseException:
        path.unlink(missing_ok=True)
        raise
    print(f"created protected regional credential bundle: {path}")


def merge_values(base: dict, override: dict) -> None:
    for key, value in override.items():
        if isinstance(value, dict) and isinstance(base.get(key), dict):
            merge_values(base[key], value)
        else:
            base[key] = value


def load_deployment_values(region: str, values_path: Path) -> dict:
    config = load_region(region)
    merge_values(config, load_yaml(STACK_DIR / "values" / "versions.yaml"))
    merge_values(config, load_yaml(values_path.expanduser().resolve()))
    return config


def validate_deployment_inputs(config: dict, phase: str) -> None:
    if not config.get("deploymentReady"):
        raise DeploymentError(
            "regional values are placeholders; set deploymentReady after filling them"
        )
    for name, image in config.get("images", {}).items():
        repository = image.get("repository", "")
        digest = image.get("digest", "")
        if ".invalid" in repository or not SHA256_DIGEST.fullmatch(digest):
            raise DeploymentError(
                f"image {name} is not configured with a deployable digest reference"
            )
        if set(digest.removeprefix("sha256:")) == {"0"}:
            raise DeploymentError(f"image {name} still uses the static-render digest")

    router = config.get("router", {})
    if not router.get("hostname") or router["hostname"].endswith(".invalid"):
        raise DeploymentError("router.hostname is still a static-render placeholder")
    if not router.get("tlsSecretName"):
        raise DeploymentError("router.tlsSecretName is required")
    if not router.get("loadBalancerSourceRanges"):
        raise DeploymentError("router.loadBalancerSourceRanges must be restricted")
    account = os.environ.get(config["awsAccountIdEnv"], "")
    expected_certificate_prefix = (
        f"arn:aws:acm:{config['region']}:{account}:certificate/"
    )
    certificate_arn = router.get("acmCertificateArn", "")
    if not certificate_arn.startswith(
        expected_certificate_prefix
    ) or not certificate_arn.removeprefix(expected_certificate_prefix):
        raise DeploymentError(
            "router.acmCertificateArn does not match the configured region and account"
        )
    for source_range in router["loadBalancerSourceRanges"]:
        try:
            network = ipaddress.ip_network(source_range)
        except ValueError as error:
            raise DeploymentError(
                f"invalid router source range: {source_range}"
            ) from error
        if network.version != 4 or network.prefixlen != 32 or not network.is_global:
            raise DeploymentError(
                "router source ranges must be public IPv4 /32 MockDC egress addresses"
            )

    if phase != "observability":
        return

    observability = config.get("observability", {})
    amp = observability.get("amp", {})
    grafana = observability.get("grafana", {})
    if not observability.get("namespace"):
        raise DeploymentError("observability.namespace is required")
    amp_workspace_id = amp.get("workspaceId", "")
    if not AMP_WORKSPACE_ID.fullmatch(amp_workspace_id) or set(
        amp_workspace_id.removeprefix("ws-").replace("-", "")
    ) == {"0"}:
        raise DeploymentError("observability.amp.workspaceId is invalid")
    account = os.environ.get(config["awsAccountIdEnv"], "")
    if amp.get("writerRoleArn") != (
        f"arn:aws:iam::{account}:role/stargate-dev-amp-writer"
    ):
        raise DeploymentError("observability.amp.writerRoleArn is invalid")
    grafana_workspace_id = grafana.get("workspaceId", "")
    if not GRAFANA_WORKSPACE_ID.fullmatch(grafana_workspace_id) or set(
        grafana_workspace_id.removeprefix("g-")
    ) == {"0"}:
        raise DeploymentError("observability.grafana.workspaceId is invalid")
    endpoint = grafana.get("endpoint", "")
    if not re.fullmatch(
        rf"{re.escape(grafana['workspaceId'])}\.grafana-workspace\."
        rf"{re.escape(config['region'])}\.amazonaws\.com",
        endpoint,
    ):
        raise DeploymentError("observability.grafana.endpoint is invalid")


def aws_account(config: dict) -> str:
    environment_name = config["awsAccountIdEnv"]
    expected = os.environ.get(environment_name, "")
    if not re.fullmatch(r"[0-9]{12}", expected):
        raise DeploymentError(
            f"{environment_name} must contain the expected 12-digit AWS account ID"
        )
    identity = json.loads(
        run(["aws", "sts", "get-caller-identity", "--output", "json"]).stdout
    )
    if identity.get("Account") != expected:
        raise DeploymentError("active AWS identity does not match the expected account")
    return expected


def kubectl(
    context: str, *arguments: str, check: bool = True
) -> subprocess.CompletedProcess[str]:
    return run(["kubectl", "--context", context, *arguments], check=check)


def configured_contexts() -> dict[str, str]:
    kubeconfig = json.loads(
        run(["kubectl", "config", "view", "--raw", "-o", "json"]).stdout
    )
    servers = {
        cluster["name"]: cluster["cluster"]["server"].rstrip("/")
        for cluster in kubeconfig.get("clusters", [])
    }
    return {
        context["name"]: servers.get(context["context"]["cluster"], "")
        for context in kubeconfig.get("contexts", [])
    }


def verify_cluster(
    cluster: dict, config: dict, account: str, contexts: dict[str, str]
) -> None:
    name = cluster["name"]
    context = cluster["kubeContext"]
    if context not in contexts:
        raise DeploymentError(
            f"missing kube context {context}; create it with aws eks update-kubeconfig --alias {context}"
        )
    describe = json.loads(
        run(
            [
                "aws",
                "eks",
                "describe-cluster",
                "--region",
                config["region"],
                "--name",
                name,
                "--output",
                "json",
            ]
        ).stdout
    )["cluster"]
    expected_arn = f"arn:aws:eks:{config['region']}:{account}:cluster/{name}"
    if describe.get("arn") != expected_arn or describe.get("status") != "ACTIVE":
        raise DeploymentError(f"EKS cluster {name} identity or status does not match")
    if contexts[context] != describe.get("endpoint", "").rstrip("/"):
        raise DeploymentError(
            f"kube context {context} does not point at EKS cluster {name}"
        )
    if describe.get("version") != config["kubernetesVersion"]:
        raise DeploymentError(
            f"EKS cluster {name} does not run Kubernetes {config['kubernetesVersion']}"
        )

    version = json.loads(kubectl(context, "get", "--raw=/version").stdout)["gitVersion"]
    if not version.startswith(f"v{config['kubernetesVersion']}"):
        raise DeploymentError(
            f"kube context {context} points at an unexpected Kubernetes version"
        )
    if (
        kubectl(context, "auth", "can-i", "*", "*", "--all-namespaces").stdout.strip()
        != "yes"
    ):
        raise DeploymentError(
            f"kube context {context} does not have cluster-admin access"
        )

    nodes = json.loads(kubectl(context, "get", "nodes", "-o", "json").stdout)["items"]
    if len(nodes) != cluster["nodeCount"]:
        raise DeploymentError(
            f"cluster {name} has {len(nodes)} nodes; expected {cluster['nodeCount']}"
        )
    for node in nodes:
        labels = node["metadata"].get("labels", {})
        if (
            labels.get("node.kubernetes.io/instance-type")
            != cluster["nodeInstanceType"]
        ):
            raise DeploymentError(
                f"cluster {name} has an unexpected node instance type"
            )
        ready = next(
            (
                condition["status"]
                for condition in node["status"]["conditions"]
                if condition["type"] == "Ready"
            ),
            "False",
        )
        if ready != "True":
            raise DeploymentError(f"cluster {name} has a node that is not ready")


def get_secret(context: str, namespace: str, name: str) -> dict | None:
    result = kubectl(
        context, "-n", namespace, "get", "secret", name, "-o", "json", check=False
    )
    if result.returncode:
        if "NotFound" in result.stderr or "not found" in result.stderr:
            return None
        raise DeploymentError(f"could not read Secret {name} through context {context}")
    return json.loads(result.stdout)


def decoded_secret_data(secret: dict, key: str) -> bytes:
    try:
        return base64.b64decode(secret["data"][key], validate=True)
    except (KeyError, ValueError) as error:
        raise DeploymentError(
            f"Secret {secret['metadata']['name']} is missing valid data key {key}"
        ) from error


def require_secret(context: str, namespace: str, name: str, keys: set[str]) -> None:
    secret = get_secret(context, namespace, name)
    if secret is None:
        raise DeploymentError(
            f"required Secret {name} does not exist in {context}/{namespace}"
        )
    for key in keys:
        decoded_secret_data(secret, key)


def verify_chart_secret_immutability(
    config: dict, credentials: dict, phase: str
) -> None:
    namespace = config["namespace"]
    stargate_context = config["clusters"]["stargate"]["kubeContext"]
    if phase == "stargate":
        auth = get_secret(stargate_context, namespace, "stargate-dev-auth")
        if auth is not None:
            if auth.get("immutable") is not True:
                raise DeploymentError(
                    "existing stargate-dev-auth Secret is not immutable"
                )
            actual_config = json.loads(decoded_secret_data(auth, "config.json"))
            expected_config = {
                "serviceToken": credentials["serviceToken"],
                "clientToken": credentials["clientToken"],
                "workers": [
                    {
                        "token": credentials["workers"][mockdc["name"]],
                        "routingKey": mockdc["name"],
                    }
                    for mockdc in config["clusters"]["mockdcs"]
                ],
            }
            actual_service = json.loads(decoded_secret_data(auth, "service-token.json"))
            if actual_config != expected_config or actual_service != {
                "nvcfApiToken": credentials["serviceToken"]
            }:
                raise DeploymentError(
                    "existing stargate-dev-auth Secret does not match the credential bundle"
                )
        return

    if phase != "mockdc":
        return

    for mockdc in config["clusters"]["mockdcs"]:
        name = f"{mockdc['name']}-stargate-dev-mockdc-worker"
        worker = get_secret(mockdc["kubeContext"], namespace, name)
        if worker is None:
            continue
        if worker.get("immutable") is not True:
            raise DeploymentError(f"existing {name} Secret is not immutable")
        if (
            decoded_secret_data(worker, "token").decode()
            != credentials["workers"][mockdc["name"]]
        ):
            raise DeploymentError(
                f"existing {name} Secret does not match the credential bundle"
            )


def verify_prerequisite_resources(config: dict, phase: str) -> None:
    namespace = config["namespace"]
    stargate = config["clusters"]["stargate"]
    if phase == "stargate":
        controller = kubectl(
            stargate["kubeContext"],
            "-n",
            "kube-system",
            "get",
            "deployment",
            "aws-load-balancer-controller",
            "-o",
            "json",
            check=False,
        )
        if controller.returncode:
            raise DeploymentError(
                "AWS Load Balancer Controller is not installed in the Stargate cluster"
            )
        if (
            json.loads(controller.stdout).get("status", {}).get("availableReplicas", 0)
            < 1
        ):
            raise DeploymentError(
                "AWS Load Balancer Controller is not ready in the Stargate cluster"
            )
        require_secret(
            stargate["kubeContext"],
            namespace,
            config["router"]["tlsSecretName"],
            {"tls.crt", "tls.key"},
        )
    elif phase == "mockdc":
        for mockdc in config["clusters"]["mockdcs"]:
            require_secret(
                mockdc["kubeContext"],
                namespace,
                mockdc["quicCaSecretName"],
                {"ca.crt"},
            )


def verify_stargate_endpoint(config: dict) -> None:
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
        ).stdout
    )
    ingress = service.get("status", {}).get("loadBalancer", {}).get("ingress", [])
    if not any(item.get("hostname") for item in ingress):
        raise DeploymentError("backend-router NLB does not have a hostname")


def helmfile_environment(credentials_path: Path, values_path: Path) -> dict[str, str]:
    environment = os.environ.copy()
    environment["STARGATE_DEV_CREDENTIALS_FILE"] = str(
        credentials_path.expanduser().resolve()
    )
    environment["STARGATE_DEV_VALUES_FILE"] = str(values_path.expanduser().resolve())
    return environment


def static_validate(
    region: str, phase: str, credentials_path: Path, values_path: Path
) -> None:
    environment = helmfile_environment(credentials_path, values_path)
    selector = f"phase={phase}"
    kubernetes_version = f"{load_region(region)['kubernetesVersion']}.0"
    run(
        ["helmfile", "--environment", region, "--selector", selector, "lint"],
        env=environment,
    )
    with tempfile.TemporaryDirectory(prefix="stargate-dev-render-") as output_dir:
        os.chmod(output_dir, 0o700)
        run(
            [
                "helmfile",
                "--environment",
                region,
                "--selector",
                selector,
                "template",
                "--output-dir",
                output_dir,
            ],
            env=environment,
        )
        manifests = sorted(str(path) for path in Path(output_dir).rglob("*.yaml"))
        run(
            [
                "kubeconform",
                "-strict",
                "-kubernetes-version",
                kubernetes_version,
                "-summary",
                *manifests,
            ]
        )


def apply(region: str, phase: str, credentials_path: Path, values_path: Path) -> None:
    require_tools("aws", "helm", "helmfile", "kubeconform", "kubectl")
    if run(["helm", "diff", "version"], check=False).returncode:
        raise DeploymentError("the helm-diff plugin is required by helmfile apply")
    config = load_deployment_values(region, values_path)
    credentials = load_credentials(credentials_path, config)
    validate_deployment_inputs(config, phase)
    account = aws_account(config)
    contexts = configured_contexts()
    clusters = [config["clusters"]["stargate"]]
    if phase in ("mockdc", "observability"):
        clusters.extend(config["clusters"]["mockdcs"])
    for cluster in clusters:
        verify_cluster(cluster, config, account, contexts)
    verify_chart_secret_immutability(config, credentials, phase)
    verify_prerequisite_resources(config, phase)
    if phase == "mockdc":
        verify_stargate_endpoint(config)
    static_validate(region, phase, credentials_path, values_path)

    environment = helmfile_environment(credentials_path, values_path)
    command = [
        "helmfile",
        "--environment",
        region,
        "--selector",
        f"phase={phase}",
        "apply",
        "--suppress-secrets",
    ]
    result = subprocess.run(command, cwd=STACK_DIR, env=environment, check=False)
    if result.returncode:
        raise DeploymentError("helmfile apply failed")
    print(f"applied {phase} phase for {region}")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Initialize or apply one Stargate dev region"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    init_parser = subparsers.add_parser(
        "init", help="create a protected regional credential bundle"
    )
    init_parser.add_argument("--region", required=True)
    init_parser.add_argument("--credentials", type=Path, required=True)

    apply_parser = subparsers.add_parser(
        "apply", help="validate and apply one deployment phase"
    )
    apply_parser.add_argument("--region", required=True)
    apply_parser.add_argument(
        "--phase", choices=("stargate", "mockdc", "observability"), required=True
    )
    apply_parser.add_argument("--credentials", type=Path, required=True)
    apply_parser.add_argument("--values", type=Path, required=True)
    return parser.parse_args()


def main() -> int:
    os.umask(0o077)
    args = parse_args()
    try:
        if args.command == "init":
            init_credentials(args.region, args.credentials)
        else:
            apply(args.region, args.phase, args.credentials, args.values)
    except (
        DeploymentError,
        FileExistsError,
        json.JSONDecodeError,
        OSError,
        yaml.YAMLError,
    ) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

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
from pathlib import Path

import yaml


STACK_DIR = Path(__file__).resolve().parents[1]
SHA256_DIGEST = re.compile(r"^sha256:[a-f0-9]{64}$")
ACCOUNT_ENV = "STARGATE_DEV_AWS_ACCOUNT_ID"
ROUTER_TLS_SECRET = "stargate-router-tls"
ROUTER_CA_SECRET = "stargate-router-ca"


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


def credential_values(credentials: dict) -> list[str]:
    values = [credentials.get("serviceToken"), credentials.get("workerToken")]
    if any(not isinstance(value, str) or len(value) < 32 for value in values):
        raise DeploymentError("credential bundle contains an invalid token")
    if len(set(values)) != len(values):
        raise DeploymentError("credential bundle tokens must be distinct")
    return values


def load_credentials(path: Path) -> dict:
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
    credential_values(credentials)
    return credentials


def init_credentials(path: Path) -> None:
    path = path.expanduser().resolve()
    credentials = {
        "serviceToken": secrets.token_urlsafe(32),
        "workerToken": secrets.token_urlsafe(32),
    }
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as file:
            json.dump(credentials, file, indent=2)
            file.write("\n")
    except BaseException:
        path.unlink(missing_ok=True)
        raise
    print(f"created protected credential bundle: {path}")


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
    account = os.environ.get(ACCOUNT_ENV, "")
    if phase == "observability":
        amp = config.get("observability", {}).get("amp", {})
        workspace_id = amp.get("workspaceId", "")
        if set(workspace_id.removeprefix("ws-").replace("-", "")) <= {"0"}:
            raise DeploymentError("observability.amp.workspaceId is a placeholder")
        return
    for name, image in config.get("images", {}).items():
        digest = image.get("digest", "")
        if not SHA256_DIGEST.fullmatch(digest) or set(digest[7:]) == {"0"}:
            raise DeploymentError(
                f"image {name} is not configured with a deployable digest reference"
            )

    router = config.get("router", {})
    if router.get("hostname", "").endswith(".invalid"):
        raise DeploymentError("router.hostname is a placeholder")
    if not router.get("loadBalancerSourceRanges"):
        raise DeploymentError("router.loadBalancerSourceRanges must be restricted")
    prefix = f"arn:aws:acm:{config['region']}:{account}:certificate/"
    certificate_arn = router.get("acmCertificateArn", "")
    if not certificate_arn.startswith(prefix) or certificate_arn == prefix:
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
                "router source ranges must be public IPv4 /32 egress addresses"
            )


def aws_account() -> str:
    expected = os.environ.get(ACCOUNT_ENV, "")
    if not re.fullmatch(r"[0-9]{12}", expected):
        raise DeploymentError(
            f"{ACCOUNT_ENV} must contain the expected 12-digit AWS account ID"
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
    kubeconfig = json.loads(run(["kubectl", "config", "view", "-o", "json"]).stdout)
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

    if (
        kubectl(context, "auth", "can-i", "*", "*", "--all-namespaces").stdout.strip()
        != "yes"
    ):
        raise DeploymentError(
            f"kube context {context} does not have cluster-admin access"
        )


def require_secret(context: str, namespace: str, name: str, keys: set[str]) -> None:
    result = kubectl(
        context, "-n", namespace, "get", "secret", name, "-o", "json", check=False
    )
    if result.returncode:
        raise DeploymentError(
            f"required Secret {name} is not readable in {context}/{namespace}"
        )
    data = json.loads(result.stdout).get("data", {})
    for key in sorted(keys):
        try:
            base64.b64decode(data[key], validate=True)
        except (KeyError, ValueError) as error:
            raise DeploymentError(
                f"Secret {name} is missing valid data key {key}"
            ) from error


def verify_prerequisite_resources(config: dict, phase: str) -> None:
    namespace = config["namespace"]
    stargate = config["clusters"]["stargate"]
    if phase == "stargate":
        require_secret(
            stargate["kubeContext"],
            namespace,
            ROUTER_TLS_SECRET,
            {"tls.crt", "tls.key", "ca.crt"},
        )
    elif phase == "mockdc":
        for mockdc in config["clusters"]["mockdcs"]:
            require_secret(
                mockdc["kubeContext"],
                namespace,
                ROUTER_CA_SECRET,
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


def helmfile_environment(
    credentials_path: Path | None, values_path: Path
) -> dict[str, str]:
    environment = os.environ.copy()
    if credentials_path is None:
        environment.pop("STARGATE_DEV_CREDENTIALS_FILE", None)
    else:
        environment["STARGATE_DEV_CREDENTIALS_FILE"] = str(
            credentials_path.expanduser().resolve()
        )
    environment["STARGATE_DEV_VALUES_FILE"] = str(values_path.expanduser().resolve())
    return environment


def apply(
    region: str,
    phase: str,
    credentials_path: Path | None,
    values_path: Path,
) -> None:
    require_tools("aws", "helm", "helmfile", "kubectl")
    if run(["helm", "diff", "version"], check=False).returncode:
        raise DeploymentError("the helm-diff plugin is required by helmfile apply")
    config = load_deployment_values(region, values_path)
    if phase != "observability":
        if credentials_path is None:
            raise DeploymentError(f"--credentials is required for the {phase} phase")
        load_credentials(credentials_path)
    validate_deployment_inputs(config, phase)
    account = aws_account()
    contexts = configured_contexts()
    clusters = [config["clusters"]["stargate"]]
    if phase in ("mockdc", "observability"):
        clusters.extend(config["clusters"]["mockdcs"])
    for cluster in clusters:
        verify_cluster(cluster, config, account, contexts)
    verify_prerequisite_resources(config, phase)
    if phase == "mockdc":
        verify_stargate_endpoint(config)

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
        "init", help="create the protected credential bundle shared by all regions"
    )
    init_parser.add_argument("--credentials", type=Path, required=True)

    apply_parser = subparsers.add_parser(
        "apply", help="validate and apply one deployment phase"
    )
    apply_parser.add_argument("--region", required=True)
    apply_parser.add_argument(
        "--phase", choices=("stargate", "mockdc", "observability"), required=True
    )
    apply_parser.add_argument("--credentials", type=Path)
    apply_parser.add_argument("--values", type=Path, required=True)
    return parser.parse_args()


def main() -> int:
    os.umask(0o077)
    args = parse_args()
    try:
        if args.command == "init":
            init_credentials(args.credentials)
        else:
            apply(args.region, args.phase, args.credentials, args.values)
    except (
        DeploymentError,
        json.JSONDecodeError,
        OSError,
        yaml.YAMLError,
    ) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

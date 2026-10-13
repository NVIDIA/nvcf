#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Create, populate, and remove the local mock multi-cluster environment.

Each local region is one hub k3d cluster and two MockDC k3d clusters, named in
environments/<region>.yaml. Every cluster joins one Docker network and pulls
images from one k3d registry. Pylons and peer hubs dial each hub's
registration Service at the hub node's address on that network, which the
images command records in each region's values file. Run images again after
the hub clusters are recreated.
"""

import argparse
import json
import os
import secrets
import shutil
import socket
import sys
import tempfile
from pathlib import Path

import yaml

import deploy
from deploy import DeploymentError

STACK_DIR = deploy.STACK_DIR
REPO_ROOT = STACK_DIR.parents[2]
STATE_DIR = STACK_DIR / ".local"

REGIONS = ("local-a", "local-b")
NETWORK = "stargate-dev-local"
REGISTRY = "stargate-dev-registry"
REGISTRY_CONTAINER = f"k3d-{REGISTRY}"
REGISTRY_PORT = 5000
REGISTRY_ADDRESS = f"{REGISTRY_CONTAINER}:{REGISTRY_PORT}"
MOCKDC_AGENTS = 2
CERTIFICATE_DAYS = 365
NEW_KEY = ["-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes"]
IMAGE_TAG = "local"

# Values key -> (Bazel oci_load target, tag it loads, local repository name).
# The stargate and dev auth images load under the same package tag, so they
# are built and pushed one at a time.
STARGATE_PACKAGE = "src/libraries/rust/stargate/crates/stargate"
IMAGES = {
    "stargate": (
        f"//{STARGATE_PACKAGE}:image_load",
        f"{STARGATE_PACKAGE}:latest",
        "stargate",
    ),
    "auth": (
        f"//{STARGATE_PACKAGE}:dev_auth_image_load",
        f"{STARGATE_PACKAGE}:latest",
        "stargate-dev-auth",
    ),
    "mockDynamo": (
        "//src/libraries/rust/stargate/crates/mock-dynamo:image_load",
        "src/libraries/rust/stargate/crates/mock-dynamo:latest",
        "mock-dynamo",
    ),
    "pylon": (
        "//src/libraries/rust/stargate/crates/pylon:image_load",
        "src/libraries/rust/stargate/crates/pylon:latest",
        "pylon",
    ),
}


def load_regions() -> dict[str, dict]:
    return {region: deploy.load_region(region) for region in REGIONS}


def region_clusters(config: dict) -> list[tuple[dict, int]]:
    """Return each cluster in a region with its k3d agent count.

    A hub is one server node, so k3s ServiceLB exposes the registration
    Service on that node's address. MockDC backends need two nodes.
    """
    clusters = config["clusters"]
    return [(clusters["stargate"], 0)] + [
        (mockdc, MOCKDC_AGENTS) for mockdc in clusters["mockdcs"]
    ]


def cluster_names(configs: dict[str, dict]) -> list[str]:
    return [
        cluster["name"]
        for config in configs.values()
        for cluster, _ in region_clusters(config)
    ]


def server_container(cluster: str) -> str:
    return f"k3d-{cluster}-server-0"


def bazel_command() -> str:
    for name in ("bazel", "bazelisk"):
        if shutil.which(name):
            return name
    raise DeploymentError("required command not found: bazel or bazelisk")


def docker_exists(kind: str, name: str) -> bool:
    return deploy.run(["docker", kind, "inspect", name], check=False).returncode == 0


def existing_clusters() -> set[str]:
    output = deploy.run(["k3d", "cluster", "list", "-o", "json"]).stdout
    return {cluster["name"] for cluster in json.loads(output or "[]")}


def free_local_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


def reusing(what: str) -> None:
    print(f"reusing existing {what}; down deletes it by name")


def ensure_network() -> None:
    if docker_exists("network", NETWORK):
        reusing(f"Docker network {NETWORK}")
        return
    deploy.run(["docker", "network", "create", NETWORK])
    print(f"created Docker network {NETWORK}")


def ensure_registry() -> None:
    if docker_exists("container", REGISTRY_CONTAINER):
        reusing(f"registry {REGISTRY_CONTAINER}")
        return
    deploy.run(
        [
            "k3d",
            "registry",
            "create",
            REGISTRY,
            "--port",
            f"127.0.0.1:{free_local_port()}",
            "--default-network",
            NETWORK,
            "--no-help",
        ]
    )
    print(f"created registry {REGISTRY_CONTAINER}")


def registry_host_address() -> str:
    output = deploy.run(
        ["docker", "port", REGISTRY_CONTAINER, f"{REGISTRY_PORT}/tcp"]
    ).stdout
    for line in output.splitlines():
        host, _, port = line.strip().rpartition(":")
        if host == "127.0.0.1" and port.isdigit():
            return f"127.0.0.1:{port}"
    raise DeploymentError(f"{REGISTRY_CONTAINER} does not publish a 127.0.0.1 port")


def cluster_create_command(name: str, agents: int) -> list[str]:
    return [
        "k3d",
        "cluster",
        "create",
        name,
        "--agents",
        str(agents),
        "--api-port",
        f"127.0.0.1:{free_local_port()}",
        "--network",
        NETWORK,
        "--registry-use",
        REGISTRY_ADDRESS,
        "--k3s-arg",
        "--disable=traefik@server:*",
        "--kubeconfig-update-default",
        "--kubeconfig-switch-context=false",
        "--wait",
    ]


def container_ip(container: str) -> str:
    inspected = json.loads(deploy.run(["docker", "inspect", container]).stdout)
    networks = inspected[0]["NetworkSettings"]["Networks"]
    address = networks.get(NETWORK, {}).get("IPAddress", "")
    if not address:
        raise DeploymentError(
            f"{container} has no address on Docker network {NETWORK}"
        )
    return address


def ensure_clusters(configs: dict[str, dict]) -> None:
    existing = existing_clusters()
    for config in configs.values():
        for cluster, agents in region_clusters(config):
            name = cluster["name"]
            if name in existing:
                reusing(f"k3d cluster {name}")
                # The cluster may predate the current kubeconfig.
                deploy.run(
                    [
                        "k3d",
                        "kubeconfig",
                        "merge",
                        name,
                        "--kubeconfig-merge-default",
                        "--kubeconfig-switch-context=false",
                    ]
                )
            else:
                deploy.run(cluster_create_command(name, agents), capture=False)


def openssl_config(hostname: str | None) -> str:
    lines = [
        "[req]",
        "distinguished_name = dn",
        "[dn]",
        "[ca]",
        "basicConstraints = critical,CA:TRUE",
        "keyUsage = critical,keyCertSign,cRLSign",
        "subjectKeyIdentifier = hash",
    ]
    if hostname:
        lines += [
            "[router]",
            "basicConstraints = critical,CA:FALSE",
            "keyUsage = critical,digitalSignature",
            "extendedKeyUsage = serverAuth",
            f"subjectAltName = DNS:{hostname},DNS:*.{hostname}",
            "subjectKeyIdentifier = hash",
            "authorityKeyIdentifier = keyid",
        ]
    return "\n".join(lines) + "\n"


def write_config(directory: Path, hostname: str | None) -> Path:
    descriptor, name = tempfile.mkstemp(dir=directory, suffix=".cnf")
    with os.fdopen(descriptor, "w", encoding="utf-8") as file:
        file.write(openssl_config(hostname))
    return Path(name)


def ensure_ca(state_dir: Path) -> None:
    certificate, key = state_dir / "ca.crt", state_dir / "ca.key"
    if certificate.is_file() and key.is_file():
        expiry = ["openssl", "x509", "-checkend", "0", "-noout"]
        if deploy.run([*expiry, "-in", str(certificate)], check=False).returncode:
            raise DeploymentError(
                f"the local CA has expired; delete {certificate} and {key}, "
                "then run up and every deploy phase again"
            )
        return
    config = write_config(state_dir, None)
    try:
        deploy.run(
            [
                "openssl",
                "req",
                "-x509",
                *NEW_KEY,
                "-config",
                str(config),
                "-extensions",
                "ca",
                "-subj",
                "/CN=stargate-dev local CA",
                "-sha256",
                "-days",
                str(CERTIFICATE_DAYS),
                "-keyout",
                str(key),
                "-out",
                str(certificate),
            ]
        )
    finally:
        config.unlink(missing_ok=True)
    print(f"created local CA: {certificate}")


def router_files(state_dir: Path, region: str) -> tuple[Path, Path]:
    return state_dir / f"router-{region}.crt", state_dir / f"router-{region}.key"


def router_certificate_is_current(
    state_dir: Path, certificate: Path, hostname: str
) -> bool:
    command = [
        "openssl",
        "verify",
        "-CAfile",
        str(state_dir / "ca.crt"),
        "-verify_hostname",
        hostname,
        str(certificate),
    ]
    return deploy.run(command, check=False).returncode == 0


def ensure_router_certificate(state_dir: Path, region: str, hostname: str) -> None:
    certificate, key = router_files(state_dir, region)
    if (
        key.is_file()
        and certificate.is_file()
        and router_certificate_is_current(state_dir, certificate, hostname)
    ):
        return
    config = write_config(state_dir, hostname)
    request = state_dir / f"router-{region}.csr"
    try:
        deploy.run(
            [
                "openssl",
                "req",
                "-new",
                *NEW_KEY,
                "-config",
                str(config),
                "-subj",
                f"/CN={hostname}",
                "-keyout",
                str(key),
                "-out",
                str(request),
            ]
        )
        deploy.run(
            [
                "openssl",
                "x509",
                "-req",
                "-in",
                str(request),
                "-CA",
                str(state_dir / "ca.crt"),
                "-CAkey",
                str(state_dir / "ca.key"),
                "-set_serial",
                str(secrets.randbits(63) + 1),
                "-sha256",
                "-days",
                str(CERTIFICATE_DAYS),
                "-extfile",
                str(config),
                "-extensions",
                "router",
                "-out",
                str(certificate),
            ]
        )
    finally:
        config.unlink(missing_ok=True)
        request.unlink(missing_ok=True)
    print(f"created router certificate for {hostname}: {certificate}")


def apply_manifest(context: str, resource: str, create: list[str]) -> None:
    try:
        manifest = deploy.run(
            ["kubectl", "--context", context, *create, "--dry-run=client", "-o", "json"]
        ).stdout
        deploy.run(
            ["kubectl", "--context", context, "apply", "-f", "-"], input_text=manifest
        )
    except DeploymentError as error:
        raise DeploymentError(
            f"cannot apply {resource} in {context}: {error}"
        ) from error


def apply_namespace(context: str, namespace: str) -> None:
    command = ["create", "namespace", namespace]
    apply_manifest(context, f"namespace {namespace}", command)


def apply_secret(
    context: str, namespace: str, name: str, files: dict[str, Path]
) -> None:
    command = ["-n", namespace, "create", "secret", "generic", name]
    command += [f"--from-file={key}={path}" for key, path in files.items()]
    apply_manifest(context, f"Secret {namespace}/{name}", command)


def apply_secrets(state_dir: Path, configs: dict[str, dict]) -> None:
    ca = state_dir / "ca.crt"
    for region, config in configs.items():
        namespace = config["namespace"]
        certificate, key = router_files(state_dir, region)
        hub = config["clusters"]["stargate"]["kubeContext"]
        apply_namespace(hub, namespace)
        apply_secret(
            hub,
            namespace,
            deploy.ROUTER_TLS_SECRET,
            {"tls.crt": certificate, "tls.key": key, "ca.crt": ca},
        )
        for mockdc in config["clusters"]["mockdcs"]:
            context = mockdc["kubeContext"]
            apply_namespace(context, namespace)
            apply_secret(context, namespace, deploy.ROUTER_CA_SECRET, {"ca.crt": ca})


def up(state_dir: Path) -> None:
    deploy.require_tools("docker", "k3d", "kubectl", "openssl")
    configs = load_regions()
    state_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    ensure_network()
    ensure_registry()
    ensure_clusters(configs)
    ensure_ca(state_dir)
    for region, config in configs.items():
        ensure_router_certificate(state_dir, region, config["router"]["hostname"])
    apply_secrets(state_dir, configs)
    credentials = state_dir / "credentials.json"
    if not credentials.exists():
        deploy.init_credentials(credentials)
    print("local clusters are ready; next run: python3 scripts/local.py images")


def pushed_digest(registry: str, repository: str) -> str:
    pushed = f"{registry}/{repository}:{IMAGE_TAG}"
    output = deploy.run(
        ["docker", "image", "inspect", "--format", "{{json .RepoDigests}}", pushed]
    ).stdout
    prefix = f"{registry}/{repository}@"
    digests = {
        entry.removeprefix(prefix)
        for entry in json.loads(output or "null") or []
        if entry.startswith(prefix)
    }
    if len(digests) != 1 or not deploy.SHA256_DIGEST.fullmatch(next(iter(digests))):
        raise DeploymentError(f"cannot read one registry digest for {pushed}")
    return digests.pop()


def publish_image(
    bazel: str, registry: str, target: str, loaded: str, repository: str
) -> str:
    deploy.run([bazel, "run", target], cwd=REPO_ROOT, capture=False)
    pushed = f"{registry}/{repository}:{IMAGE_TAG}"
    deploy.run(["docker", "tag", loaded, pushed])
    deploy.run(["docker", "push", pushed], capture=False)
    return pushed_digest(registry, repository)


def values_path(state_dir: Path, region: str) -> Path:
    return state_dir / f"values-{region}.yaml"


def values_document(digests: dict[str, str], address: str, peer: str) -> str:
    values = {
        "deploymentReady": True,
        "images": {
            key: {
                "repository": f"{REGISTRY_ADDRESS}/{repository}",
                "digest": digests[key],
            }
            for key, (_, _, repository) in IMAGES.items()
        },
        "router": {
            "address": address,
            "remoteWatchUrls": [f"http://{peer}:50071"],
        },
    }
    header = "# Generated by scripts/local.py images. Do not commit.\n"
    return header + yaml.safe_dump(values, sort_keys=False)


def images(state_dir: Path) -> None:
    deploy.require_tools("docker")
    bazel = bazel_command()
    configs = load_regions()
    state_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    if not docker_exists("container", REGISTRY_CONTAINER):
        raise DeploymentError(f"{REGISTRY_CONTAINER} is missing; run local.py up first")
    registry = registry_host_address()
    addresses = {
        region: container_ip(server_container(config["clusters"]["stargate"]["name"]))
        for region, config in configs.items()
    }
    digests = {
        key: publish_image(bazel, registry, target, loaded, repository)
        for key, (target, loaded, repository) in IMAGES.items()
    }
    # Each hub watches the other, so both regions see every backend.
    for region, peer in zip(REGIONS, reversed(REGIONS)):
        path = values_path(state_dir, region)
        path.write_text(
            values_document(digests, addresses[region], addresses[peer]),
            encoding="utf-8",
        )
        print(f"wrote {region} values: {path}")


def down() -> None:
    deploy.require_tools("docker", "k3d")
    existing = existing_clusters()
    for name in cluster_names(load_regions()):
        if name in existing:
            deploy.run(["k3d", "cluster", "delete", name], capture=False)
    if docker_exists("container", REGISTRY_CONTAINER):
        deploy.run(["k3d", "registry", "delete", REGISTRY_CONTAINER], capture=False)
    if docker_exists("network", NETWORK):
        deploy.run(["docker", "network", "rm", NETWORK])
    print("removed local clusters, registry, and network; the state directory is kept")


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run the Stargate dev stack as local k3d clusters"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)
    subparsers.add_parser(
        "up",
        help="create the network, registry, clusters, certificates, and Secrets",
    )
    subparsers.add_parser(
        "images",
        help="build and push images, then write each region's values file",
    )
    subparsers.add_parser(
        "down", help="delete the clusters, registry, and network by their local names"
    )
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    os.umask(0o077)
    args = parse_args(argv)
    try:
        if args.command == "up":
            up(STATE_DIR)
        elif args.command == "images":
            images(STATE_DIR)
        else:
            down()
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

#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import io
import json
import re
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import yaml

STACK_DIR = Path(__file__).resolve().parents[1]
SCRIPTS_DIR = STACK_DIR / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))
SPEC = importlib.util.spec_from_file_location(
    "stargate_dev_local", SCRIPTS_DIR / "local.py"
)
LOCAL = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(LOCAL)

DNS_LABEL = re.compile(r"^[a-z0-9]([a-z0-9-]*[a-z0-9])?$")
DIGEST = "sha256:" + "ab" * 32
CONFIGS = LOCAL.load_regions()


class FakeRun:
    """Records commands and answers them from (argv prefix, stdout) rules."""

    def __init__(self, responses: list[tuple[list[str], str]] | None = None):
        self.commands: list[list[str]] = []
        self.inputs: list[str | None] = []
        self.responses = responses or []

    def __call__(self, command, *, input_text=None, check=True, **_kwargs):
        self.commands.append(list(command))
        self.inputs.append(input_text)
        for prefix, stdout in self.responses:
            if command[: len(prefix)] == prefix:
                return subprocess.CompletedProcess(command, 0, stdout, "")
        return subprocess.CompletedProcess(command, 0, "", "")

    def matching(self, *prefix: str) -> list[list[str]]:
        return [c for c in self.commands if c[: len(prefix)] == list(prefix)]


def flag(command: list[str], name: str) -> str:
    return command[command.index(name) + 1]


def quiet():
    return redirect_stdout(io.StringIO())


def patch_run(fake):
    return mock.patch.object(LOCAL.deploy, "run", fake)


def hub(region: str) -> dict:
    return CONFIGS[region]["clusters"]["stargate"]


class TopologyTests(unittest.TestCase):
    def test_cluster_names_fit_k3d_limits(self) -> None:
        names = LOCAL.cluster_names(CONFIGS)
        self.assertEqual(len(names), 6)
        for name in names:
            self.assertLessEqual(len(name), 32, name)
            self.assertRegex(name, DNS_LABEL)

    def test_router_certificate_names_the_hostname_and_a_wildcard(self) -> None:
        for config in CONFIGS.values():
            hostname = config["router"]["hostname"]
            # Wildcard SANs need at least two labels after the wildcard.
            self.assertGreaterEqual(hostname.count("."), 2)
            config = LOCAL.openssl_config(hostname)
            self.assertIn(f"subjectAltName = DNS:{hostname},DNS:*.{hostname}", config)
            self.assertIn("extendedKeyUsage = serverAuth", config)
        self.assertNotIn("[router]", LOCAL.openssl_config(None))


class ClusterCreationTests(unittest.TestCase):
    def test_api_server_listens_on_loopback_without_switching_context(self) -> None:
        command = LOCAL.cluster_create_command("cluster", 2)
        self.assertRegex(flag(command, "--api-port"), r"^127\.0\.0\.1:[0-9]+$")
        self.assertIn("--kubeconfig-switch-context=false", command)

    def test_missing_clusters_are_created_with_single_node_hubs(self) -> None:
        names = LOCAL.cluster_names(CONFIGS)
        fake = FakeRun()
        with (
            patch_run(fake),
            mock.patch.object(
                LOCAL, "existing_clusters", return_value={names[-1], "unrelated"}
            ),
            quiet() as output,
        ):
            LOCAL.ensure_clusters(CONFIGS)

        created = {
            command[3]: flag(command, "--agents")
            for command in fake.matching("k3d", "cluster", "create")
        }
        hubs = {hub(region)["name"] for region in LOCAL.REGIONS}
        self.assertEqual(set(created), set(names[:-1]))
        for name, agents in created.items():
            self.assertEqual(agents, "0" if name in hubs else "2", name)
        self.assertIn(f"reusing existing k3d cluster {names[-1]}", output.getvalue())
        # A reused cluster's context is restored without switching to it.
        (merge,) = fake.matching("k3d", "kubeconfig", "merge")
        self.assertEqual(merge[3], names[-1])
        self.assertIn("--kubeconfig-switch-context=false", merge)

    def test_hub_address_is_the_single_node_internal_ip(self) -> None:
        def nodes(*addresses: str) -> str:
            return json.dumps(
                {
                    "items": [
                        {
                            "status": {
                                "addresses": [
                                    {"type": "Hostname", "address": "node"},
                                    {"type": "InternalIP", "address": address},
                                ]
                            }
                        }
                        for address in addresses
                    ]
                }
            )

        with patch_run(FakeRun([(["kubectl"], nodes("172.30.0.4"))])) as fake:
            self.assertEqual(LOCAL.hub_address("k3d-hub"), "172.30.0.4")
        (command,) = fake.matching("kubectl")
        self.assertEqual(command[:3], ["kubectl", "--context", "k3d-hub"])

        for output in (nodes(), nodes("172.30.0.4", "172.30.0.5")):
            with patch_run(FakeRun([(["kubectl"], output)])):
                with self.assertRaises(LOCAL.DeploymentError):
                    LOCAL.hub_address("k3d-hub")


class SecretTests(unittest.TestCase):
    def test_hubs_get_router_tls_and_mockdcs_get_only_the_ca(self) -> None:
        fake = FakeRun([(["kubectl"], "{}")])
        with patch_run(fake):
            LOCAL.apply_secrets(Path("/state"), CONFIGS)

        secrets = [command for command in fake.commands if "secret" in command]
        by_context = {command[2]: command for command in secrets}
        self.assertEqual(len(secrets), 6)
        for region, config in CONFIGS.items():
            namespace = config["namespace"]
            tls = by_context[hub(region)["kubeContext"]]
            self.assertIn("stargate-router-tls", tls)
            self.assertEqual(flag(tls, "-n"), namespace)
            self.assertIn(f"--from-file=tls.crt=/state/router-{region}.crt", tls)
            self.assertIn(f"--from-file=tls.key=/state/router-{region}.key", tls)
            self.assertIn("--from-file=ca.crt=/state/ca.crt", tls)
            for mockdc in config["clusters"]["mockdcs"]:
                ca = by_context[mockdc["kubeContext"]]
                self.assertIn("stargate-router-ca", ca)
                self.assertEqual(flag(ca, "-n"), namespace)
                self.assertEqual(
                    [arg for arg in ca if arg.startswith("--from-file")],
                    ["--from-file=ca.crt=/state/ca.crt"],
                )
        for command in fake.commands:
            if "create" in command:
                self.assertIn("--dry-run=client", command)

    def test_apply_errors_name_the_resource_and_context(self) -> None:
        def fail(command, **_kwargs):
            raise LOCAL.DeploymentError("kubectl failed: forbidden")

        with patch_run(fail):
            with self.assertRaisesRegex(
                LOCAL.DeploymentError,
                "cannot apply Secret ns/name in ctx: kubectl failed: forbidden",
            ):
                LOCAL.apply_secret("ctx", "ns", "name", {"ca.crt": Path("/ca")})

    def test_router_certificate_is_reissued_unless_it_verifies_for_the_hostname(
        self,
    ) -> None:
        hostname = "router.local-a.stargate-dev.test"
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            for name in ("router-local-a.crt", "router-local-a.key"):
                (state / name).write_text("existing")
            fake = FakeRun()
            with patch_run(fake):
                LOCAL.ensure_router_certificate(state, "local-a", hostname)
            (verify,) = fake.commands
            self.assertEqual(flag(verify, "-verify_hostname"), hostname)

            stale = mock.Mock(
                side_effect=[
                    subprocess.CompletedProcess([], 1, "", "hostname mismatch"),
                    subprocess.CompletedProcess([], 0, "", ""),
                    subprocess.CompletedProcess([], 0, "", ""),
                ]
            )
            with patch_run(stale), quiet():
                LOCAL.ensure_router_certificate(state, "local-a", hostname)
            (request,) = [
                call.args[0]
                for call in stale.call_args_list
                if call.args[0][:2] == ["openssl", "req"]
            ]
            self.assertEqual(flag(request, "-subj"), f"/CN={hostname}")
            self.assertEqual(
                sorted(path.name for path in state.iterdir()),
                ["router-local-a.crt", "router-local-a.key"],
            )

    def test_an_expired_ca_names_the_files_to_delete(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            for name in ("ca.crt", "ca.key"):
                (state / name).write_text("existing")
            with patch_run(FakeRun()) as fake:
                LOCAL.ensure_ca(state)
            self.assertFalse(fake.matching("openssl", "req"))
            expired = mock.Mock(
                return_value=subprocess.CompletedProcess([], 1, "", "")
            )
            with patch_run(expired):
                with self.assertRaisesRegex(
                    LOCAL.DeploymentError, re.escape(str(state / "ca.key"))
                ):
                    LOCAL.ensure_ca(state)


class ImageTests(unittest.TestCase):
    def test_registry_host_address_prefers_the_loopback_binding(self) -> None:
        with patch_run(FakeRun([(["docker"], "127.0.0.1:41234\n")])):
            self.assertEqual(LOCAL.registry_host_address(), "127.0.0.1:41234")
        with patch_run(FakeRun([(["docker"], "[::]:5\n")])):
            with self.assertRaises(LOCAL.DeploymentError):
                LOCAL.registry_host_address()

    def test_pushed_digest_reads_the_entry_for_the_pushed_repository(self) -> None:
        registry = "127.0.0.1:41234"
        other = "sha256:" + "cd" * 32
        digests = json.dumps(
            [f"{registry}/stargate@{other}", f"{registry}/pylon@{DIGEST}"]
        )
        fake = FakeRun([(["docker", "image", "inspect"], digests)])
        with patch_run(fake):
            self.assertEqual(LOCAL.pushed_digest(registry, "pylon"), DIGEST)
        self.assertEqual(fake.commands[0][-1], f"{registry}/pylon:local")
        for output in ("null", "[]", json.dumps([f"{registry}/pylon@sha256:x"])):
            with patch_run(FakeRun([(["docker"], output)])):
                with self.assertRaises(LOCAL.DeploymentError):
                    LOCAL.pushed_digest(registry, "pylon")

    def test_images_push_each_load_target_before_the_next_and_write_values(
        self,
    ) -> None:
        fake = FakeRun()
        addresses = {"local-a": "172.30.0.2", "local-b": "172.30.0.9"}
        hubs = {
            hub(region)["kubeContext"]: address
            for region, address in addresses.items()
        }
        with (
            tempfile.TemporaryDirectory() as directory,
            patch_run(fake),
            mock.patch.object(LOCAL.deploy, "require_tools"),
            mock.patch.object(LOCAL, "bazel_command", return_value="bazel"),
            mock.patch.object(LOCAL, "docker_exists", return_value=True),
            mock.patch.object(
                LOCAL, "registry_host_address", return_value="127.0.0.1:41234"
            ),
            mock.patch.object(LOCAL, "pushed_digest", return_value=DIGEST),
            mock.patch.object(LOCAL, "hub_address", side_effect=hubs.__getitem__),
            quiet(),
        ):
            LOCAL.images(Path(directory))
            documents = {
                region: (Path(directory) / f"values-{region}.yaml").read_text()
                for region in LOCAL.REGIONS
            }

        # Two targets load the same tag, so each image is pushed before the
        # next build replaces that tag.
        builds = [
            index
            for index, command in enumerate(fake.commands)
            if command[:2] == ["bazel", "run"]
        ]
        self.assertEqual(len(builds), len(LOCAL.IMAGES))
        for index in builds:
            tag, push = fake.commands[index + 1 : index + 3]
            self.assertEqual(push[:2], ["docker", "push"])
            self.assertEqual(push[2], tag[-1])
        for region, peer in (("local-a", "local-b"), ("local-b", "local-a")):
            values = yaml.safe_load(documents[region])
            self.assertIs(values["deploymentReady"], True)
            self.assertEqual(
                values["router"],
                {
                    "address": addresses[region],
                    "remoteWatchUrls": [f"http://{addresses[peer]}:50071"],
                },
            )
            repositories = set()
            for image in values["images"].values():
                self.assertEqual(image["digest"], DIGEST)
                # Clusters pull through the registry's network name.
                self.assertTrue(
                    image["repository"].startswith(f"{LOCAL.REGISTRY_CONTAINER}:")
                )
                repositories.add(image["repository"])
            self.assertEqual(len(repositories), len(values["images"]))

    def test_images_require_the_registry(self) -> None:
        with (
            tempfile.TemporaryDirectory() as directory,
            mock.patch.object(LOCAL.deploy, "require_tools"),
            mock.patch.object(LOCAL, "bazel_command", return_value="bazel"),
            mock.patch.object(LOCAL, "docker_exists", return_value=False),
        ):
            with self.assertRaises(LOCAL.DeploymentError):
                LOCAL.images(Path(directory))


class DownTests(unittest.TestCase):
    def test_down_deletes_only_the_local_names(self) -> None:
        names = LOCAL.cluster_names(CONFIGS)
        fake = FakeRun()
        existing = {names[0], names[-1], "stargate-dev-local-c-hub", "someone-else"}
        with (
            patch_run(fake),
            mock.patch.object(LOCAL.deploy, "require_tools"),
            mock.patch.object(LOCAL, "existing_clusters", return_value=existing),
            mock.patch.object(LOCAL, "docker_exists", return_value=True),
            quiet(),
        ):
            LOCAL.down()

        self.assertEqual(
            fake.commands,
            [
                ["k3d", "cluster", "delete", names[0]],
                ["k3d", "cluster", "delete", names[-1]],
                ["k3d", "registry", "delete", LOCAL.REGISTRY_CONTAINER],
                ["docker", "network", "rm", LOCAL.NETWORK],
            ],
        )


class CommandLineTests(unittest.TestCase):
    def test_state_dir_is_ignored_by_git(self) -> None:
        relative = LOCAL.STATE_DIR.relative_to(STACK_DIR)
        ignored = (STACK_DIR / ".gitignore").read_text().splitlines()
        self.assertIn(f"/{relative}/", ignored)

    def test_errors_are_reported_without_a_traceback(self) -> None:
        stderr = io.StringIO()
        with (
            mock.patch.object(
                LOCAL.deploy,
                "require_tools",
                side_effect=LOCAL.DeploymentError("missing k3d"),
            ),
            mock.patch.object(LOCAL.os, "umask"),
            redirect_stderr(stderr),
        ):
            self.assertEqual(LOCAL.main(["down"]), 1)
        self.assertEqual(stderr.getvalue(), "error: missing k3d\n")


if __name__ == "__main__":
    unittest.main()

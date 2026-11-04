# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SOURCE = Path(__file__).resolve().parents[1]
PLUGIN = "vault-plugin-secrets-jwt"
VERSIONS = dict(
    line.split("=", 1)
    for line in (SOURCE / "jwt-plugin-versions.env").read_text().splitlines()
    if line and not line.startswith("#")
)
OLD = VERSIONS["JWT_PLUGIN_OLD_VERSION"]
TARGET = VERSIONS["JWT_PLUGIN_TARGET_VERSION"]

FAKE_BAO = r"""#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
path = Path(os.environ["FAKE_BAO_STATE"])
state = json.loads(path.read_text())
args = sys.argv[1:]
if state.get("fail_reads") and args[:2] in (["secrets", "list"], ["plugin", "info"]):
    sys.exit(2)
if args[:2] == ["plugin", "info"] and state.get("not_ready_reads", 0):
    state["not_ready_reads"] -= 1
    path.write_text(json.dumps(state))
    sys.exit(2)
if args[:2] == ["secrets", "list"]:
    print(json.dumps(state["mounts"]))
elif args[:2] == ["plugin", "info"]:
    version = next((x.split("=", 1)[1] for x in args if x.startswith("-version=")), "")
    if version not in state["catalog"]:
        sys.exit(2)
    print(json.dumps(state["catalog"][version]))
elif args[:2] == ["secrets", "tune"]:
    state["writes"].append(args)
    version = next(x.split("=", 1)[1] for x in args if x.startswith("-plugin-version="))
    state["mounts"][args[-1]]["plugin_version"] = version
elif args[:2] == ["plugin", "deregister"]:
    state["writes"].append(args)
    version = next((x.split("=", 1)[1] for x in args if x.startswith("-version=")), "")
    if state.get("fail_delete_version") == version:
        state.pop("fail_delete_version")
        path.write_text(json.dumps(state))
        sys.exit(2)
    state["catalog"].pop(version, None)
else:
    sys.exit("unexpected bao arguments: " + repr(args))
path.write_text(json.dumps(state))
"""


class LifecycleTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.legacy = self.root / "legacy"
        self.target = self.root / "target"
        self.legacy.write_bytes(b"previously published binary\n")
        self.target.write_bytes(b"target binary\n")
        self.output = self.root / "package"
        self.env = {
            **os.environ,
            "JWT_VERSIONS_FILE": str(SOURCE / "jwt-plugin-versions.env"),
            "JWT_LEGACY_BINARY": str(self.legacy),
            "JWT_TARGET_BINARY": str(self.target),
            "JWT_PACKAGE_OUTPUT": str(self.output),
            "JWT_INCLUDE_LEGACY": "true",
            "JWT_PLUGIN_DIR": str(self.output / "plugins"),
            "JWT_PLUGIN_CONFIG_DIR": str(self.output / "plugin-config"),
            "JWT_PLUGIN_READY_ATTEMPTS": "3",
            "JWT_PLUGIN_READY_DELAY": "0",
            "FAKE_BAO_STATE": str(self.root / "state.json"),
            "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
        }
        self.package()
        fake = self.root / "bao"
        fake.write_text(FAKE_BAO)
        fake.chmod(0o755)
        self.old_sha = hashlib.sha256(self.legacy.read_bytes()).hexdigest()
        self.target_sha = hashlib.sha256(self.target.read_bytes()).hexdigest()
        self.state = {
            "catalog": {
                "": {"command": PLUGIN, "version": "", "builtin": False, "sha256": self.old_sha},
                OLD: {"command": f"{PLUGIN}-{OLD}", "version": OLD, "builtin": False, "sha256": self.old_sha},
                TARGET: {"command": f"{PLUGIN}-{TARGET}", "version": TARGET, "builtin": False, "sha256": self.target_sha},
            },
            "mounts": {
                "services/example/jwt/": {
                    "type": PLUGIN,
                    "plugin_version": "",
                    "running_plugin_version": "",
                    "running_sha256": self.old_sha,
                },
                "auth-like-jwt/": {"type": "jwt"},
                "kv/": {"type": "kv"},
            },
            "writes": [],
        }

    def package(self):
        subprocess.run(["sh", str(SOURCE / "scripts/package-jwt-plugins.sh")], env=self.env, check=True)

    def run_driver(self, action, success=True):
        state_file = self.root / "state.json"
        state_file.write_text(json.dumps(self.state))
        result = subprocess.run(
            ["bash", str(SOURCE / "scripts/jwt-plugin-lifecycle.sh"), action],
            env=self.env, text=True, capture_output=True,
        )
        self.state = json.loads(state_file.read_text())
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        return result

    def mount(self):
        return self.state["mounts"]["services/example/jwt/"]

    def reload(self):
        version = self.mount()["plugin_version"]
        self.mount()["running_plugin_version"] = version
        self.mount()["running_sha256"] = self.state["catalog"][version]["sha256"]

    def test_bridge_preserves_bytes_and_architecture_specific_hashes(self):
        self.assertEqual((self.output / "plugins" / PLUGIN).read_bytes(), self.legacy.read_bytes())
        config = (self.output / "plugin-config/jwt.hcl").read_text()
        for version, binary in ((OLD, self.legacy), (TARGET, self.target)):
            self.assertIn(f'command = "{PLUGIN}-{version}"', config)
            self.assertIn(hashlib.sha256(binary.read_bytes()).hexdigest(), config)
        self.assertIn("plugin_auto_register = true", config)
        self.target.write_bytes(b"other architecture")
        self.output = self.root / "other-architecture"
        self.env["JWT_PACKAGE_OUTPUT"] = str(self.output)
        self.package()
        self.assertIn(hashlib.sha256(self.target.read_bytes()).hexdigest(),
                      (self.output / "plugin-config/jwt.hcl").read_text())

    def test_upgrade_rollback_and_separate_cleanup(self):
        self.run_driver("preflight")
        self.run_driver("pin-old")
        self.assertEqual(self.mount()["plugin_version"], OLD)
        self.assertEqual(self.mount()["running_plugin_version"], "")
        self.run_driver("pin-old")
        self.assertEqual(len(self.state["writes"]), 1)
        self.run_driver("upgrade", success=False)
        self.reload()
        self.run_driver("upgrade")
        self.assertEqual(self.mount()["running_plugin_version"], OLD)
        self.run_driver("check-cleanup", success=False)
        self.run_driver("rollback")
        self.assertEqual(self.mount()["plugin_version"], OLD)
        self.run_driver("upgrade")
        self.reload()
        self.run_driver("verify")
        self.run_driver("check-cleanup")
        self.run_driver("cleanup", success=False)
        (self.output / "plugins" / PLUGIN).unlink()
        (self.output / "plugins" / f"{PLUGIN}-{OLD}").unlink()
        self.run_driver("cleanup")
        self.assertEqual(set(self.state["catalog"]), {TARGET})
        self.assertIn("auth-like-jwt/", self.state["mounts"])

    def test_cleanup_refuses_either_old_version_and_unknown_running_digest(self):
        for configured, running in (("", ""), (OLD, TARGET), (TARGET, OLD), (TARGET, None)):
            with self.subTest(configured=configured, running=running):
                self.mount().update(plugin_version=configured, running_plugin_version=running,
                                    running_sha256=self.target_sha)
                self.run_driver("cleanup", success=False)
                self.assertEqual(self.state["writes"], [])

    def test_preflight_checks_all_mounts_before_any_tune(self):
        self.state["mounts"]["unexpected/"] = {
            "type": PLUGIN, "plugin_version": "v99.0.0",
            "running_plugin_version": "v99.0.0", "running_sha256": self.old_sha,
        }
        self.run_driver("pin-old", success=False)
        self.assertEqual(self.state["writes"], [])

    def test_catalog_and_inventory_failures_do_not_mutate_state(self):
        self.state["catalog"][""]["sha256"] = self.target_sha
        self.run_driver("pin-old", success=False)
        self.state["fail_reads"] = True
        self.run_driver("cleanup", success=False)
        self.assertEqual(self.state["writes"], [])

    def test_missing_or_changed_target_binary_blocks_cleanup(self):
        self.mount()["plugin_version"] = TARGET
        self.reload()
        binary = self.output / "plugins" / f"{PLUGIN}-{TARGET}"
        binary.chmod(0o755)
        binary.write_bytes(b"unexpected binary")
        self.run_driver("cleanup", success=False)
        binary.unlink()
        self.run_driver("cleanup", success=False)
        self.assertEqual(self.state["writes"], [])

    def test_legacy_plugin_mount_representation_is_included(self):
        self.mount().update(type="plugin", options={"plugin_name": PLUGIN})
        self.run_driver("pin-old")
        self.assertEqual(self.mount()["plugin_version"], OLD)

    def test_target_only_package_has_no_legacy_paths_or_stanzas(self):
        self.output = self.root / "target-only"
        self.env.update(JWT_INCLUDE_LEGACY="false", JWT_PACKAGE_OUTPUT=str(self.output))
        self.package()
        self.assertEqual([p.name for p in (self.output / "plugins").iterdir()], [f"{PLUGIN}-{TARGET}"])
        config = (self.output / "plugin-config/jwt.hcl").read_text()
        self.assertNotIn(f'version = "{OLD}"', config)

    def test_empty_and_unidentified_inventories_fail_closed(self):
        for mounts in ({}, {"kv/": {"type": "kv"}}, {"mystery/": {"type": "plugin"}}):
            with self.subTest(mounts=mounts):
                self.state["mounts"] = mounts
                for action in ("verify", "check-cleanup", "cleanup"):
                    self.run_driver(action, success=False)
                self.assertEqual(self.state["writes"], [])

    def test_deployment_can_recover_before_first_mount_but_cleanup_stays_closed(self):
        self.state["mounts"] = {"kv/": {"type": "kv"}}
        self.run_driver("deployment-preflight")
        self.run_driver("verify", success=False)
        self.run_driver("cleanup", success=False)
        self.state["catalog"][TARGET]["sha256"] = self.old_sha
        self.run_driver("deployment-preflight", success=False)
        self.assertEqual(self.state["writes"], [])

    def test_deployment_preflight_checks_nonempty_bridge_and_target_only_states(self):
        self.run_driver("deployment-preflight")
        self.mount()["plugin_version"] = TARGET
        self.reload()
        (self.output / "plugins" / PLUGIN).unlink()
        (self.output / "plugins" / f"{PLUGIN}-{OLD}").unlink()
        self.run_driver("deployment-preflight")
        self.mount()["running_plugin_version"] = OLD
        self.run_driver("deployment-preflight", success=False)
        self.assertEqual(self.state["writes"], [])

    def test_digest_identifies_a_mount_with_an_unexpected_type(self):
        self.mount()["type"] = "unexpected-alias"
        self.run_driver("cleanup", success=False)
        self.assertEqual(self.state["writes"], [])

    def test_bridge_preflight_checks_legacy_bytes_before_version_registration(self):
        self.state["catalog"] = {"": self.state["catalog"][""]}
        self.run_driver("bridge-preflight")
        self.state["catalog"][""]["sha256"] = self.target_sha
        self.run_driver("bridge-preflight", success=False)
        self.assertEqual(self.state["writes"], [])

    def test_ready_check_retries_and_times_out_without_writes(self):
        self.state["not_ready_reads"] = 2
        self.run_driver("wait-ready")
        self.state["not_ready_reads"] = 3
        self.run_driver("wait-ready", success=False)
        self.assertEqual(self.state["writes"], [])

    def test_cleanup_resumes_after_failure_and_handles_missing_entries(self):
        self.mount()["plugin_version"] = TARGET
        self.reload()
        (self.output / "plugins" / PLUGIN).unlink()
        (self.output / "plugins" / f"{PLUGIN}-{OLD}").unlink()
        self.state["fail_delete_version"] = ""
        self.run_driver("cleanup", success=False)
        self.assertNotIn(OLD, self.state["catalog"])
        self.run_driver("cleanup")
        self.run_driver("cleanup")
        self.assertEqual(set(self.state["catalog"]), {TARGET})

    def test_image_check_needs_no_server_and_entrypoint_keeps_required_config(self):
        self.state["fail_reads"] = True
        self.run_driver("image-check")
        upstream = self.root / "upstream"
        upstream.write_text("#!/usr/bin/env python3\nimport json, sys\nprint(json.dumps(sys.argv[1:]))\n")
        upstream.chmod(0o755)
        self.env["OPENBAO_UPSTREAM_ENTRYPOINT"] = str(upstream)
        for args in (("server", "-log-level=debug"), ("bao", "server", "-config=/custom"),
                     ("bao", "version"), ("-config=/x.hcl",), ("bao", "-log-level=debug", "server")):
            result = subprocess.run(["sh", str(SOURCE / "scripts/docker-entrypoint.sh"), *args],
                                    env=self.env, capture_output=True, text=True, check=True)
            actual = json.loads(result.stdout)
            if args[0] == "server" or args[:2] == ("bao", "server"):
                self.assertIn("-config=/openbao/plugin-config", actual)
                self.assertEqual(actual[:len(args)], list(args))
            else:
                self.assertEqual(actual, list(args))


if __name__ == "__main__":
    unittest.main()

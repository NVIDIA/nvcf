# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest


REPO = Path(__file__).resolve().parents[3]
SCRIPTS = REPO / "deploy/helm/openbao/helm/scripts"
SHA = "1" * 64

FAKE_KUBECTL = r'''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
p = Path(os.environ["FAKE_KUBE_STATE"])
s = json.loads(p.read_text())
a = sys.argv[1:]
s["calls"].append(a)
p.write_text(json.dumps(s))
if "statefulset" in a:
    print(json.dumps(s["statefulset"]))
elif "pods" in a:
    labels = dict(x.split("=", 1) for x in a[a.index("-l") + 1].split(","))
    print(json.dumps({"items": [pod for pod in s["pods"]
        if all(pod["metadata"]["labels"].get(k) == v for k, v in labels.items())]}))
elif "exec" in a:
    remote = a[a.index("--") + 1:]
    if any("test ! -r" in x for x in remote):
        sys.exit(1 if s.get("versioned_image") else 0)
    if remote[:2] == ["test", "-x"]:
        sys.exit(0 if s.get("legacy_binary", True) else 1)
    if remote[0] == "sha256sum":
        print("1" * 64 + "  plugin")
    elif "info" in remote:
        print(json.dumps(s["catalog"]))
    elif "register" in remote:
        s["writes"] += 1
        p.write_text(json.dumps(s))
    elif any("actual=" in x for x in remote):
        sys.exit(1 if a[a.index("exec") + 1] == s.get("bad_member") else 0)
else:
    sys.exit(2)
'''


class ChartLifecycleTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        fake = self.root / "kubectl"
        fake.write_text(FAKE_KUBECTL)
        fake.chmod(0o755)
        self.state = {"calls": [], "writes": 0, "catalog": {
            "version": "", "command": "vault-plugin-secrets-jwt", "sha256": SHA}}
        versions = REPO / "infra/openbao/jwt-plugin-versions.env"
        (self.root / "manifest.env").write_text(versions.read_text())
        (self.root / "target.sha").write_text(SHA)
        self.env = {**os.environ, "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
                    "FAKE_KUBE_STATE": str(self.root / "state.json"),
                    "JWT_SERVER_NAMESPACE": "tenant", "JWT_SERVER_STATEFULSET": "renamed-bao",
                    "JWT_SHARED_VERSIONS_DIR": str(self.root), "JWT_PLUGIN_LIFECYCLE_MODE": "legacy"}

    def run_script(self, args):
        path = self.root / "state.json"
        path.write_text(json.dumps(self.state))
        result = subprocess.run(args, env=self.env, text=True, capture_output=True)
        self.state = json.loads(path.read_text())
        return result

    def bootstrap(self):
        return self.run_script(["bash", "-c", '''
            source "$1"
            log_error() { echo "$*" >&2; }
            log_success() { :; }
            get_root_token() { echo test-token; }
            register_and_enable_jwt_plugin tenant renamed-bao
        ''', "sh", str(SCRIPTS / "utils/jwt-plugin.sh")])

    def test_legacy_bootstrap_refuses_bridge_and_target_only_images_without_writes(self):
        for legacy_binary in (True, False):
            with self.subTest(legacy_binary=legacy_binary):
                self.state.update(versioned_image=True, legacy_binary=legacy_binary)
                result = self.bootstrap()
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("require bridge or versioned", result.stderr)
                self.assertEqual(self.state["writes"], 0)

    def test_versioned_catalog_entry_does_not_satisfy_legacy_bootstrap(self):
        self.state["catalog"]["version"] = "v99.0.0"
        result = self.bootstrap()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.state["writes"], 1)

    def test_matching_unversioned_catalog_is_idempotent(self):
        result = self.bootstrap()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.state["writes"], 0)

    def test_versioned_bootstrap_only_waits_for_declarative_registration(self):
        self.env["JWT_PLUGIN_LIFECYCLE_MODE"] = "versioned"
        self.state["versioned_image"] = True
        result = self.bootstrap()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.state["writes"], 0)
        self.assertTrue(any("wait-ready" in c for c in self.state["calls"]))

    def seed_members(self):
        labels = {"app.kubernetes.io/name": "custom-name", "app.kubernetes.io/instance": "this-release",
                  "component": "server"}
        self.state["statefulset"] = {"spec": {"replicas": 2, "selector": {"matchLabels": labels}}}
        self.state["pods"] = [
            {"metadata": {"name": name, "labels": dict(labels)}, "status": {"phase": "Running"}}
            for name in ("renamed-bao-0", "renamed-bao-1")]

    def test_member_inventory_uses_live_selector_and_ignores_other_and_retired_pods(self):
        self.seed_members()
        for name, phase, terminating, other_release in (
                ("other-0", "Running", False, True), ("evicted", "Failed", False, False),
                ("completed", "Succeeded", False, False), ("terminating", "Running", True, False)):
            pod = json.loads(json.dumps(self.state["pods"][0]))
            pod["metadata"]["name"] = name
            pod["status"]["phase"] = phase
            if terminating:
                pod["metadata"]["deletionTimestamp"] = "2026-01-01T00:00:00Z"
            if other_release:
                pod["metadata"]["labels"]["app.kubernetes.io/instance"] = "other-release"
            self.state["pods"].append(pod)
        result = self.run_script(["sh", str(SCRIPTS / "check-jwt-members.sh")])
        self.assertEqual(result.returncode, 0, result.stderr)
        inspected = [c[c.index("exec") + 1] for c in self.state["calls"] if "exec" in c]
        self.assertEqual(inspected, ["renamed-bao-0", "renamed-bao-1"])

    def test_missing_and_incompatible_members_stop_upgrade(self):
        self.seed_members()
        self.state["bad_member"] = "renamed-bao-1"
        result = self.run_script(["sh", str(SCRIPTS / "check-jwt-members.sh")])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Rotate member renamed-bao-1", result.stderr)
        self.state["pods"].pop()
        result = self.run_script(["sh", str(SCRIPTS / "check-jwt-members.sh")])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("found 1", result.stderr)

    def test_legacy_baseline_matches_chart_and_manifest(self):
        dockerfile = (REPO / "infra/openbao/Dockerfile").read_text()
        image = re.search(r"^ARG JWT_LEGACY_IMAGE=(.+)$", dockerfile, re.M)[1]
        baseline = re.search(r":([^:@]+)@sha256:[0-9a-f]{64}$", image)[1]
        manifest = (self.root / "manifest.env").read_text()
        self.assertIn("JWT_PLUGIN_OLD_VERSION=v" + baseline.split("-nv-")[1], manifest)
        values = (REPO / "deploy/helm/openbao/helm/values.yaml").read_text()
        mode = re.search(r"jwtPluginLifecycle:\s*\n\s+mode:\s+(\S+)", values)[1]
        if mode == "legacy":
            chart = (REPO / "deploy/helm/openbao/helm/Chart.yaml").read_text()
            self.assertEqual(baseline, re.search(r"^appVersion: (\S+)", chart, re.M)[1])


if __name__ == "__main__":
    unittest.main()

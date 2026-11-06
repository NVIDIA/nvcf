# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from test_jwt_mounts_upgrade_check import BEFORE, CATALOG
from test_receipt_upgrade_check import GUIDE
from test_recovery_upgrade_check import block_containing


class BridgeCommands(unittest.TestCase):
    def run_stage(self, marker, fail="", changed=False, baseline=True):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            current = copy.deepcopy(BEFORE)
            if changed:
                current["existing/jwt/"]["accessor"] = "replacement-accessor"
            if baseline:
                (root / "jwt-before.json").write_text(json.dumps(BEFORE))
            (root / "current-fixture.json").write_text(json.dumps(current))
            (root / "catalog-fixture.json").write_text(json.dumps(CATALOG))
            script = r'''
set -euo pipefail
export CONTROL_STACK="$UPGRADE_RECORD/control"
export HELMFILE_ENV=production
export CONTROL_VALUES="$CONTROL_STACK/environments/$HELMFILE_ENV.yaml"
yq() { printf 'mode %s\n' "$*" >> "$UPGRADE_RECORD/actions"; }
check_openbao() { test "$FAIL" != health; }
bao_admin() {
  printf 'bao %s\n' "$*" >> "$UPGRADE_RECORD/actions"
  test "$*" != "$FAIL"
  case "$*" in
    "bao secrets list -detailed -format=json") cat "$UPGRADE_RECORD/current-fixture.json" ;;
    sh*) cat "$UPGRADE_RECORD/catalog-fixture.json" ;;
  esac
}
control_helmfile() {
  printf 'helmfile %s\n' "$*" >> "$UPGRADE_RECORD/actions"
  case "$*" in *apply*) test "$FAIL" != apply ;; esac
}
'''
            script += block_containing(GUIDE, marker) + "\nprintf 'CONTINUED\\n'"
            result = subprocess.run(["bash", "-c", script], capture_output=True, text=True,
                                    env={**os.environ, "UPGRADE_RECORD": str(root), "FAIL": fail,
                                         "UPGRADE_SCRIPTS": str(Path(__file__).parent.resolve())})
            log = root / "actions"
            return result, log.read_text() if log.exists() else ""

    def test_bridge_apply_is_isolated_and_keeps_hooks(self):
        result, actions = self.run_stage("export CONTROL_VALUES=")
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertIn('jwtPluginLifecycle.mode = "bridge"', actions)
        self.assertIn("--selector name=openbao-server apply --skip-needs", actions)
        self.assertNotIn("--no-hooks", actions)

    def test_failed_bridge_apply_stops(self):
        result, _ = self.run_stage("export CONTROL_VALUES=", fail="apply")
        self.assertNotEqual(0, result.returncode)
        self.assertNotIn("CONTINUED", result.stdout)

    def test_failed_health_or_preflight_cannot_enable_migrations(self):
        for failure in ("health", "jwt-plugin-lifecycle preflight"):
            with self.subTest(failure=failure):
                result, actions = self.run_stage('jwtPluginLifecycle.mode = "versioned"', fail=failure)
                self.assertNotEqual(0, result.returncode)
                self.assertNotIn("mode ", actions)
                self.assertNotIn("helmfile ", actions)
                self.assertNotIn("CONTINUED", result.stdout)

    def test_changed_mount_cannot_enable_migrations(self):
        result, actions = self.run_stage('jwtPluginLifecycle.mode = "versioned"',
                                         changed=True)
        self.assertNotEqual(0, result.returncode)
        self.assertNotIn("mode ", actions)
        self.assertNotIn("helmfile ", actions)

    def test_versioned_mode_is_saved_before_apply(self):
        result, actions = self.run_stage('jwtPluginLifecycle.mode = "versioned"')
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertLess(actions.index("bao jwt-plugin-lifecycle preflight"), actions.index("mode "))
        self.assertLess(actions.index("mode "), actions.index("helmfile "))

    def test_baseline_capture_passes_once_and_refuses_overwrite(self):
        result, _ = self.run_stage("set -o noclobber", baseline=False)
        self.assertEqual(0, result.returncode, result.stderr)
        result, _ = self.run_stage("set -o noclobber")
        self.assertNotEqual(0, result.returncode)
        self.assertNotIn("CONTINUED", result.stdout)

    def test_guide_does_not_tune_or_require_target_only_mounts(self):
        text = GUIDE.read_text()
        for command in ("pin-old", "upgrade", "rollback", "verify", "cleanup"):
            self.assertNotIn(f"bao_admin jwt-plugin-lifecycle {command}", text)


if __name__ == "__main__":
    unittest.main()

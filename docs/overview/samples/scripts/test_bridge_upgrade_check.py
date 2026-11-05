# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from test_receipt_upgrade_check import GUIDE
from test_recovery_upgrade_check import block_containing


class BridgeCommands(unittest.TestCase):
    def run_stage(self, marker, fail=""):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
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
}
control_helmfile() {
  printf 'helmfile %s\n' "$*" >> "$UPGRADE_RECORD/actions"
  case "$*" in *apply*) test "$FAIL" != apply ;; esac
}
'''
            script += block_containing(GUIDE, marker) + "\nprintf 'CONTINUED\\n'"
            result = subprocess.run(["bash", "-c", script], capture_output=True, text=True,
                                    env={**os.environ, "UPGRADE_RECORD": str(root), "FAIL": fail})
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

    def test_failed_health_or_preflight_cannot_tune(self):
        for failure in ("health", "jwt-plugin-lifecycle preflight"):
            with self.subTest(failure=failure):
                result, actions = self.run_stage("bao_admin jwt-plugin-lifecycle pin-old", fail=failure)
                self.assertNotEqual(0, result.returncode)
                self.assertNotIn("pin-old", actions)
                self.assertNotIn("CONTINUED", result.stdout)

    def test_failed_verification_cannot_enable_migrations(self):
        result, actions = self.run_stage('jwtPluginLifecycle.mode = "versioned"',
                                         fail="jwt-plugin-lifecycle verify")
        self.assertNotEqual(0, result.returncode)
        self.assertNotIn("mode ", actions)
        self.assertNotIn("helmfile ", actions)

    def test_versioned_mode_is_saved_before_apply(self):
        result, actions = self.run_stage('jwtPluginLifecycle.mode = "versioned"')
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertLess(actions.index("bao jwt-plugin-lifecycle verify"), actions.index("mode "))
        self.assertLess(actions.index("mode "), actions.index("helmfile "))


if __name__ == "__main__":
    unittest.main()

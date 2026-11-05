# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

from test_recovery_upgrade_check import UPGRADE_11, block_containing

GUIDE = Path(__file__).parents[2] / "release-notes/1.0.2-to-1.0.4-upgrade.md"


class ReceiptChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.control = self.root / "deploy/stacks/self-managed"
        self.compute = self.root / "deploy/stacks/nvcf-compute-plane"
        self.observability = self.root / "deploy/stacks/observability"
        text = GUIDE.read_text(encoding="utf-8")
        blocks = re.findall(r"```bash\n(.*?)\n```", text, re.DOTALL)
        self.preflight = next(block for block in blocks if "check-upgrade-packages.py" in block and "export EXPECTED_CONTROL_STACK_VERSION" in block)
        self.receipt = next(block for block in blocks if "get job upgrade-receipt" in block)
        self.selected = {}
        for name, stack in (("Self-Managed", self.control), ("Compute Plane", self.compute),
                            ("Observability", self.observability)):
            version = re.search(rf"^\| {name} \| [^|]+ \| ([^|]+) \|$", text, re.MULTILINE)[1].strip()
            self.selected[stack] = version
            stack.mkdir(parents=True)
            (stack / "VERSION").write_text(version + "\n", encoding="utf-8")
        self.env = {**os.environ, "CONTROL_STACK": str(self.control), "COMPUTE_STACK": str(self.compute),
                    "TARGET_BUNDLE": str(self.root), "UPGRADE_RECORD": str(self.root),
                    "CONTROL_KUBECONFIG": "synthetic-config", "CONTROL_CONTEXT": "synthetic-context",
                    "UPGRADE_SCRIPTS": str(Path(__file__).parent.resolve()),
                    "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
                    "RECEIPT_VERSION": self.selected[self.control]}
        stub = self.root / "kubectl"
        stub.write_text('#!/bin/sh\ncase "$*" in *jsonpath*) printf "%s" "$RECEIPT_VERSION" ;; esac\n')
        stub.chmod(0o700)

    def run_blocks(self, receipt=False, env=None, before_receipt=""):
        # Only these two adapters replace cluster access. Execute the guide's
        # actual shell checks, including errexit and command substitution.
        stubs = r'''
control_helmfile() { printf '%s\n' "$*" >> "$UPGRADE_RECORD/apply.log"; }
'''
        script = "set -euo pipefail\n" + stubs + self.preflight
        if receipt:
            script += "\n" + before_receipt + "\n" + self.receipt
        return subprocess.run(["bash", "-c", script], env=env or self.env,
                              capture_output=True, text=True)

    def test_selected_packages_and_receipt_pass(self):
        result = self.run_blocks(receipt=True)
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertIn("04-upgrade-receipt.yaml.gotmpl apply", (self.root / "apply.log").read_text())

    def test_preflight_uses_selected_release_table(self):
        for stack, variable in ((self.control, "CONTROL"), (self.compute, "COMPUTE"),
                                (self.observability, "OBSERVABILITY")):
            self.assertIn(f'export EXPECTED_{variable}_STACK_VERSION="{self.selected[stack]}"', self.preflight)

    def test_wrong_or_missing_package_stamps_stop_before_receipt_write(self):
        for stack, selected in self.selected.items():
            for wrong in (None, "", "1.0.0" if selected != "1.0.0" else "1.0.1", "1.0.4-rc.1"):
                with self.subTest(stack=stack.name, stamp=wrong):
                    stamp = stack / "VERSION"
                    if wrong is None:
                        stamp.unlink()
                    else:
                        stamp.write_text(wrong + "\n", encoding="utf-8")
                    result = self.run_blocks(receipt=True)
                    self.assertNotEqual(0, result.returncode)
                    self.assertFalse((self.root / "apply.log").exists())
                    stamp.write_text(selected + "\n", encoding="utf-8")

    def test_correct_packages_do_not_excuse_a_wrong_receipt(self):
        for wrong in ("", "1.0.0", self.selected[self.compute], "1.0.8-rc.1"):
            with self.subTest(receipt=wrong):
                result = self.run_blocks(receipt=True, env={**self.env, "RECEIPT_VERSION": wrong})
                self.assertNotEqual(0, result.returncode)

    def test_shared_wrong_stamp_and_receipt_cannot_pass(self):
        (self.control / "VERSION").write_text("1.0.0\n", encoding="utf-8")
        result = self.run_blocks(receipt=True, env={**self.env, "RECEIPT_VERSION": "1.0.0"})
        self.assertNotEqual(0, result.returncode)
        self.assertFalse((self.root / "apply.log").exists())

    def test_receipt_stage_rechecks_package_after_preflight(self):
        result = self.run_blocks(receipt=True, before_receipt='printf "1.0.0\\n" > "$CONTROL_STACK/VERSION"')
        self.assertNotEqual(0, result.returncode)
        self.assertFalse((self.root / "apply.log").exists())

    def test_resumed_session_preserves_independent_expectations(self):
        result = self.run_blocks()
        self.assertEqual(0, result.returncode, result.stderr)
        script = 'set -euo pipefail\nsource "$UPGRADE_RECORD/session.sh"\npython3 "$UPGRADE_SCRIPTS/check-upgrade-packages.py"\n'
        result = subprocess.run(["bash", "-c", script], env=self.env, capture_output=True, text=True)
        self.assertEqual(0, result.returncode, result.stderr)
        (self.control / "VERSION").write_text("1.0.0\n", encoding="utf-8")
        result = subprocess.run(["bash", "-c", script], env=self.env, capture_output=True, text=True)
        self.assertNotEqual(0, result.returncode)


class Receipt11Checks(unittest.TestCase):
    def run_checks(self, control_stamp, compute_stamp, receipt, after_preflight=""):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            control, compute = root / "control", root / "compute"
            for stack, stamp in ((control, control_stamp), (compute, compute_stamp)):
                stack.mkdir()
                if stamp is not None:
                    (stack / "VERSION").write_text(stamp)
            preflight = block_containing(UPGRADE_11, "check_selected_stack_versions() {")
            final = block_containing(UPGRADE_11, "get job upgrade-receipt")
            script = r'''
set -euo pipefail
control_helmfile() { printf 'APPLIED\n'; }
kc() { case "$*" in *jsonpath*) printf '%s' "$RECEIPT_VERSION" ;; esac; }
'''
            script += preflight + "\n" + after_preflight + "\n" + final + "\nprintf 'CONTINUED\\n'"
            env = {**os.environ, "CONTROL_STACK": str(control), "COMPUTE_STACK": str(compute),
                   "STACK_VERSION": "1.1.2", "COMPUTE_STACK_VERSION": "1.1.0",
                   "WORKDIR": str(root), "RECEIPT_VERSION": receipt}
            return subprocess.run(["bash", "-c", script], env=env,
                                  capture_output=True, text=True)

    def test_correct_selection_passes(self):
        result = self.run_checks("1.1.2", "1.1.0", "1.1.2")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("CONTINUED", result.stdout)

    def test_missing_wrong_or_shared_wrong_versions_stop(self):
        for control, compute, receipt in ((None, "1.1.0", "1.1.2"),
                                          ("1.1.2", "", "1.1.2"),
                                          ("1.1.0", "1.1.0", "1.1.0"),
                                          ("1.1.2", "1.1.0", "1.1.0")):
            with self.subTest(control=control, compute=compute, receipt=receipt):
                result = self.run_checks(control, compute, receipt)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("CONTINUED", result.stdout)
                if control != "1.1.2" or compute != "1.1.0":
                    self.assertNotIn("APPLIED", result.stdout)

    def test_changed_package_stops_before_receipt_apply(self):
        result = self.run_checks("1.1.2", "1.1.0", "1.1.2",
                                 'printf "1.1.0\\n" > "$CONTROL_STACK/VERSION"')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("APPLIED", result.stdout)


if __name__ == "__main__":
    unittest.main()

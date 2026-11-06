# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

from test_recovery_upgrade_check import block_containing

GUIDE = Path(__file__).parents[2] / "release-notes/1.0.2-to-1.0.4-upgrade.md"


class ReceiptChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        text = GUIDE.read_text(encoding="utf-8")
        blocks = re.findall(r"```bash\n(.*?)\n```", text, re.DOTALL)
        self.receipt = next(block for block in blocks if "get job upgrade-receipt" in block)
        self.selected = re.search(r"^\|\s*Self-Managed\s*\|[^|]+\|\s*([^|]+?)\s*\|$",
                                  text, re.MULTILINE)[1]
        self.env = {**os.environ, "UPGRADE_RECORD": str(self.root),
                    "CONTROL_KUBECONFIG": "synthetic-config", "CONTROL_CONTEXT": "synthetic-context",
                    "UPGRADE_SCRIPTS": str(Path(__file__).parent.resolve()),
                    "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
                    "RECEIPT_VERSION": self.selected}
        stub = self.root / "kubectl"
        stub.write_text('#!/bin/sh\ncase "$*" in *jsonpath*) printf "%s" "$RECEIPT_VERSION" ;; esac\n')
        stub.chmod(0o700)

    def run_receipt(self, env=None):
        # Replace cluster access while executing the guide's actual receipt commands.
        stubs = r'''
control_helmfile() { printf '%s\n' "$*" >> "$UPGRADE_RECORD/apply.log"; }
'''
        script = "set -euo pipefail\n" + stubs + self.receipt
        return subprocess.run(["bash", "-c", script], env=env or self.env,
                              capture_output=True, text=True)

    def test_selected_receipt_passes(self):
        result = self.run_receipt()
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertIn("04-upgrade-receipt.yaml.gotmpl apply", (self.root / "apply.log").read_text())

    def test_receipt_uses_selected_release_table(self):
        self.assertIn(f'export EXPECTED_CONTROL_STACK_VERSION="{self.selected}"', self.receipt)

    def test_wrong_receipt_fails(self):
        for wrong in ("", "1.0.0", "1.0.10-rc.1"):
            with self.subTest(receipt=wrong):
                result = self.run_receipt(env={**self.env, "RECEIPT_VERSION": wrong})
                self.assertNotEqual(0, result.returncode)

    def test_receipt_replaces_stale_expected_version(self):
        env = {**self.env, "EXPECTED_CONTROL_STACK_VERSION": "9.9.9"}
        result = self.run_receipt(env=env)
        self.assertEqual(0, result.returncode, result.stderr)
        result = self.run_receipt(env={**env, "RECEIPT_VERSION": "9.9.9"})
        self.assertNotEqual(0, result.returncode)


if __name__ == "__main__":
    unittest.main()

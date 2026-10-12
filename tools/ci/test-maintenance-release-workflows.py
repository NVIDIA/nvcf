#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Run the actual workflow tag-selection scripts without publishing anything."""

import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class MaintenanceWorkflowTest(unittest.TestCase):
    def select_tag(self, workflow, tag, manual):
        text = (ROOT / ".github/workflows" / workflow).read_text()
        step = text.split("      - name: Select the tag\n", 1)[1].split("      - name:", 1)[0]
        script = step.split("        run: |\n", 1)[1]
        script = "\n".join(line[10:] for line in script.splitlines() if line.strip())
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "output"
            env = dict(os.environ, GITHUB_OUTPUT=str(output), INPUT_TAG=tag if manual else "",
                       RELEASE_TAG="" if manual else tag)
            subprocess.run(["bash", "-c", script], env=env, check=True, capture_output=True, text=True)
            return dict(line.split("=", 1) for line in output.read_text().splitlines())

    def test_maintenance_tags_never_start_normal_pin_bumps(self):
        cases = (
            ("chart-version-bump.yml", "src/example/v1.2.3-sm.1"),
            ("stack-pin-bump.yml", "deploy/helm/example/v1.2.3-sm.1"),
        )
        for workflow, tag in cases:
            for manual in (False, True):
                with self.subTest(workflow=workflow, manual=manual):
                    self.assertEqual(self.select_tag(workflow, tag, manual)["applies"], "false")

    def test_stable_tags_keep_their_existing_bump_path(self):
        cases = (
            ("chart-version-bump.yml", "src/example/v1.2.3"),
            ("stack-pin-bump.yml", "deploy/helm/example/v1.2.3"),
        )
        for workflow, tag in cases:
            for manual in (False, True):
                with self.subTest(workflow=workflow, manual=manual):
                    self.assertEqual(self.select_tag(workflow, tag, manual)["applies"], "true")


if __name__ == "__main__":
    unittest.main()

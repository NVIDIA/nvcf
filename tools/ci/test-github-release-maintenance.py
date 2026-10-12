#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Portable tag-publication checks for historical maintenance source refs."""

import contextlib
import importlib.machinery
import importlib.util
import io
import subprocess
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("github-release")
SERVICE = {
    "id": "example",
    "path": "src/example",
    "service_name": "example",
    "legacy_tag_prefix": "example-v",
}
METADATA = {"services": [SERVICE]}


class MaintenanceTagTest(unittest.TestCase):
    def setUp(self):
        loader = importlib.machinery.SourceFileLoader("maintenance_release", str(SCRIPT))
        spec = importlib.util.spec_from_loader(loader.name, loader)
        self.release = importlib.util.module_from_spec(spec)
        loader.exec_module(self.release)

    def test_path_and_legacy_tags_keep_the_baseline_and_counter(self):
        for prefix in ("src/example/v", "example-v"):
            for version in ("1.2.3-sm.0", "1.2.3-sm.1", "1.2.3-sm.12"):
                with self.subTest(prefix=prefix, version=version):
                    parsed = self.release.parse_release_tag(prefix + version, METADATA)
                    self.assertEqual(parsed["version"], version)
                    self.assertTrue(self.release.release_branch(parsed).endswith("v1.2"))

    def test_malformed_maintenance_tags_are_rejected(self):
        for suffix in ("sm", "sm.-1", "sm.01", "sm.1.2", "SM.1", "sm.1+build"):
            with self.subTest(suffix=suffix):
                self.assertEqual(
                    self.release.parse_release_tag("src/example/v1.2.3-" + suffix, METADATA), {}
                )

    def test_tag_publication_marks_maintenance_as_prerelease_not_latest(self):
        calls = []
        tag = "src/example/v1.2.3-sm.1"
        with tempfile.TemporaryDirectory() as tmp, contextlib.ExitStack() as stack:
            stack.enter_context(mock.patch.object(self.release, "repo_root", return_value=Path(tmp)))
            stack.enter_context(mock.patch.object(self.release, "load_metadata", return_value=METADATA))
            stack.enter_context(mock.patch.object(self.release, "github_release_mode", return_value=(True, False)))
            stack.enter_context(mock.patch.object(self.release, "bool_env", return_value=False))
            stack.enter_context(mock.patch.object(self.release, "run", side_effect=lambda args, **kw: calls.append(args)))
            stack.enter_context(mock.patch.object(self.release.subprocess, "run", return_value=subprocess.CompletedProcess([], 1)))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            self.release.tag_release(types.SimpleNamespace(tag=tag, metadata="metadata.json"))
        self.assertEqual(len(calls), 1)
        command = calls[0]
        self.assertEqual(command[:4], ["gh", "release", "create", tag])
        self.assertIn("--verify-tag", command)
        self.assertIn("--prerelease", command)
        self.assertIn("--latest=false", command)
        self.assertIn("Version: 1.2.3-sm.1", command[command.index("--notes") + 1])

    def test_stable_release_behavior_is_unchanged(self):
        calls = []
        with mock.patch.object(self.release, "run", side_effect=lambda args: calls.append(args)), \
                mock.patch.object(self.release.subprocess, "run", return_value=subprocess.CompletedProcess([], 1)):
            self.release.create_release("src/example/v1.2.3", "title", "notes", False, False)
        self.assertNotIn("--prerelease", calls[0])
        self.assertNotIn("--latest=false", calls[0])

    def test_existing_maintenance_release_is_not_rewritten(self):
        with mock.patch.object(self.release, "run") as run, \
                mock.patch.object(self.release.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)), \
                contextlib.redirect_stdout(io.StringIO()):
            self.release.create_release("src/example/v1.2.3-sm.1", "title", "notes", False, False)
        run.assert_not_called()

    def test_existing_tag_cannot_move_to_another_commit(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            def git(*args):
                subprocess.run(["git", *args], cwd=root, check=True, capture_output=True)
            git("init")
            git("config", "user.name", "Release Test")
            git("config", "user.email", "test@example.test")
            (root / "file").write_text("baseline\n")
            git("add", ".")
            git("commit", "-m", "chore: baseline")
            git("tag", "src/example/v1.2.3-sm.1")
            (root / "file").write_text("changed\n")
            git("commit", "-am", "fix(example): change")
            with self.assertRaisesRegex(SystemExit, "already exists at"):
                self.release.publish_tag_for_version(
                    root, SERVICE, "1.2.3-sm.1", dry_run=False, draft=False, reason="test"
                )


if __name__ == "__main__":
    unittest.main()

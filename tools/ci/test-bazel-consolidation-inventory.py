#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Unit tests for tools/ci/bazel-consolidation-inventory.
#
# The end-to-end fixture suite lives at
# tools/scripts/test/test-bazel-consolidation-inventory and exercises the tool
# as a process. These tests exercise the parsing and error semantics directly,
# because those are where the shell implementation repeatedly went wrong: a
# failure or a no-match had to be distinguished from a legitimate zero, and in
# shell that distinction is carried by exit statuses that are easy to lose.

import importlib.machinery
import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT_PATH = Path(__file__).with_name("bazel-consolidation-inventory")


def load_inventory():
    loader = importlib.machinery.SourceFileLoader(
        "bazel_consolidation_inventory", str(SCRIPT_PATH)
    )
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


inventory = load_inventory()


def git(root, *args):
    subprocess.run(
        ["git", *args], cwd=root, check=True, capture_output=True, text=True
    )


class FakeTree:
    """A Tree stand-in holding file contents directly.

    The parsing functions only need select() and read(), so testing them does
    not require a git repository.
    """

    def __init__(self, files):
        self.files = files

    def select(self, pattern, include_vendor=False):
        return sorted(
            path
            for path in self.files
            if pattern.search(path)
            and (include_vendor or not inventory.VENDOR_RE.search(path))
        )

    def read(self, path):
        return self.files[path]


class BazelVersionTests(unittest.TestCase):
    def test_absent_reports_none(self):
        # Zero subtree files is the intended post-consolidation state.
        tree = FakeTree({"MODULE.bazel": "module(name='root')\n"})
        self.assertIn("none", inventory.bazel_versions(tree))

    def test_distribution_is_ordered_by_version(self):
        tree = FakeTree(
            {
                "src/a/.bazelversion": "9.1.1\n",
                "src/b/.bazelversion": "8.6.0\n",
                "src/c/.bazelversion": "8.6.0\n",
            }
        )
        self.assertEqual(inventory.bazel_versions(tree), "2 on 8.6.0  1 on 9.1.1  ")

    def test_malformed_file_is_an_error_not_an_absence(self):
        # Reporting this as "none" would announce the plan's success condition
        # on the strength of a parse failure.
        tree = FakeTree({"src/a/.bazelversion": "not-a-version\n"})
        with self.assertRaises(inventory.InventoryError) as ctx:
            inventory.bazel_versions(tree)
        self.assertIn("malformed", str(ctx.exception))

    def test_empty_file_is_an_error(self):
        tree = FakeTree({"src/a/.bazelversion": "\n"})
        with self.assertRaises(inventory.InventoryError):
            inventory.bazel_versions(tree)

    def test_vendored_versions_are_excluded(self):
        tree = FakeTree(
            {
                "src/a/.bazelversion": "8.6.0\n",
                "src/a/vendor/x/.bazelversion": "7.0.0\n",
            }
        )
        self.assertEqual(inventory.bazel_versions(tree), "1 on 8.6.0  ")


class GoSdkTests(unittest.TestCase):
    def test_absent_reports_none(self):
        tree = FakeTree({"MODULE.bazel": "module(name='root')\n"})
        self.assertEqual(inventory.go_sdk_versions(tree, False, "first-party"), "none")

    def test_single_line_declaration(self):
        tree = FakeTree({"src/a/MODULE.bazel": 'go_sdk.download(version = "1.25.11")\n'})
        self.assertEqual(
            inventory.go_sdk_versions(tree, False, "first-party"), "1.25.11 "
        )

    def test_multiline_declaration_is_parsed(self):
        # The shell implementation rejected this. That was a capability gap,
        # not correct strictness.
        tree = FakeTree(
            {"src/a/MODULE.bazel": 'go_sdk.download(\n    version = "1.25.0",\n)\n'}
        )
        self.assertEqual(
            inventory.go_sdk_versions(tree, False, "first-party"), "1.25.0 "
        )

    def test_non_literal_version_is_an_error(self):
        tree = FakeTree(
            {"src/a/MODULE.bazel": "GO = 'x'\ngo_sdk.download(version = GO)\n"}
        )
        with self.assertRaises(inventory.InventoryError) as ctx:
            inventory.go_sdk_versions(tree, False, "first-party")
        self.assertIn("not understood", str(ctx.exception))

    def test_versions_sort_numerically_not_lexically(self):
        tree = FakeTree(
            {
                "src/a/MODULE.bazel": 'go_sdk.download(version = "1.25.10")\n'
                'go_sdk.download(version = "1.25.6")\n'
            }
        )
        self.assertEqual(
            inventory.go_sdk_versions(tree, False, "first-party"), "1.25.6 1.25.10 "
        )

    def test_vendored_pin_excluded_from_first_party(self):
        files = {
            "src/a/MODULE.bazel": 'go_sdk.download(version = "1.25.11")\n',
            "src/a/vendor/x/MODULE.bazel": 'go_sdk.download(version = "1.23.0")\n',
        }
        tree = FakeTree(files)
        self.assertEqual(
            inventory.go_sdk_versions(tree, False, "first-party"), "1.25.11 "
        )
        self.assertEqual(
            inventory.go_sdk_versions(tree, True, "vendored-inclusive"),
            "1.23.0 1.25.11 ",
        )


class OciPullTests(unittest.TestCase):
    def test_counts_images_digests_and_tag_only(self):
        tree = FakeTree(
            {
                "src/a/MODULE.bazel": (
                    "oci.pull(\n"
                    '    name = "a",\n'
                    '    image = "nvcr.io/nvidia/distroless/go",\n'
                    '    digest = "sha256:aaaa",\n'
                    ")\n"
                    "oci.pull(\n"
                    '    name = "b",\n'
                    '    image = "public.ecr.aws/docker/library/eclipse-temurin",\n'
                    '    tag = "21-jre",\n'
                    ")\n"
                )
            }
        )
        result = inventory.oci_pulls(tree)
        self.assertEqual(result["declarations"], 2)
        self.assertEqual(result["from_nvcr"], 1)
        self.assertEqual(result["images"], 2)
        self.assertEqual(result["digests"], 1)
        self.assertEqual(
            result["tag_only"], ["public.ecr.aws/docker/library/eclipse-temurin"]
        )

    def test_vendored_pulls_are_excluded(self):
        tree = FakeTree(
            {
                "src/a/vendor/x/MODULE.bazel": (
                    "oci.pull(\n"
                    '    name = "v",\n'
                    '    image = "docker.io/library/vendored",\n'
                    '    tag = "latest",\n'
                    ")\n"
                )
            }
        )
        self.assertEqual(inventory.oci_pulls(tree)["declarations"], 0)


class GitFailureTests(unittest.TestCase):
    def test_git_failure_raises_rather_than_returning_empty(self):
        # An empty result from a failed git call is indistinguishable from a
        # repository with no tracked files, and every count would read zero.
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(inventory.InventoryError) as ctx:
                inventory.git(tmp, "rev-parse", "--short", "HEAD")
            self.assertIn("failed with status", str(ctx.exception))

    def test_dirty_tree_is_rejected_by_default(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            git(root, "init", "-q")
            git(root, "config", "user.email", "t@example.com")
            git(root, "config", "user.name", "T")
            (root / "MODULE.bazel").write_text("module(name='root')\n")
            git(root, "add", "-A")
            git(root, "commit", "-qm", "initial")
            (root / "MODULE.bazel").write_text("module(name='changed')\n")

            with self.assertRaises(inventory.InventoryError) as ctx:
                inventory.Tree(root)
            self.assertIn("dirty", str(ctx.exception))

            tree = inventory.Tree(root, allow_dirty=True)
            self.assertTrue(tree.dirty)

    def test_missing_tracked_file_raises(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            git(root, "init", "-q")
            git(root, "config", "user.email", "t@example.com")
            git(root, "config", "user.name", "T")
            (root / "MODULE.bazel").write_text("module(name='root')\n")
            git(root, "add", "-A")
            git(root, "commit", "-qm", "initial")
            (root / "MODULE.bazel").unlink()

            tree = inventory.Tree(root, allow_dirty=True)
            with self.assertRaises(inventory.InventoryError) as ctx:
                tree.read("MODULE.bazel")
            self.assertIn("cannot read tracked file", str(ctx.exception))


if __name__ == "__main__":
    unittest.main()

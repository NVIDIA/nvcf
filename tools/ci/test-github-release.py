#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import subprocess
import tempfile
import types
import unittest
from pathlib import Path


SCRIPT_PATH = Path(__file__).with_name("github-release")


def load_github_release():
    loader = importlib.machinery.SourceFileLoader("github_release", str(SCRIPT_PATH))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


def git(root, *args):
    subprocess.run(["git", *args], cwd=root, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)


@contextlib.contextmanager
def chdir(path):
    old_cwd = os.getcwd()
    os.chdir(path)
    try:
        yield
    finally:
        os.chdir(old_cwd)


class GithubReleaseTest(unittest.TestCase):
    def setUp(self):
        self.github_release = load_github_release()

    def init_repo(self, root):
        git(root, "init")
        git(root, "config", "user.email", "test@example.com")
        git(root, "config", "user.name", "Test User")

    def write_service_version(self, root, version):
        service_dir = root / "src/compute-plane-services/byoo-otel-collector"
        service_dir.mkdir(parents=True, exist_ok=True)
        (service_dir / "VERSION").write_text(f"{version}\n")
        (service_dir / "otel-collector-build.yaml").write_text(f"version: v{version}\n")
        (service_dir / "README.md").write_text("test\n")

    def write_nvca_version(self, root, version):
        service_dir = root / "src/compute-plane-services/nvca"
        service_dir.mkdir(parents=True, exist_ok=True)
        (service_dir / "VERSION").write_text(f"{version}\n")
        (service_dir / "README.md").write_text("test\n")

    def commit_all(self, root, message):
        git(root, "add", ".")
        git(root, "commit", "-m", message)

    def test_version_file_release_skips_existing_current_tag_on_previous_commit(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            self.write_service_version(root, "0.153.6")
            self.commit_all(root, "release byoo")
            git(root, "tag", "src/compute-plane-services/byoo-otel-collector/v0.153.6")
            (root / "src/compute-plane-services/byoo-otel-collector" / "README.md").write_text("later change\n")
            self.commit_all(root, "later byoo change")

            service = {
                "id": "byoo-otel-collector",
                "path": "src/compute-plane-services/byoo-otel-collector",
                "service_name": "byoo-otel-collector",
                "legacy_tag_prefix": "byoo-otel-collector-v",
                "version_file": "VERSION",
                "version_major_minor_source_file": "otel-collector-build.yaml",
            }

            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.github_release.publish_version_file_release(root, service, dry_run=True, draft=False)

            self.assertIn("src/compute-plane-services/byoo-otel-collector/v0.153.6 already exists", output.getvalue())
            self.assertIn("skipping", output.getvalue())

    def test_version_file_release_skips_existing_legacy_tag(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            self.write_service_version(root, "0.153.6")
            self.commit_all(root, "release byoo")
            git(root, "tag", "byoo-otel-collector-v0.153.6")

            service = {
                "id": "byoo-otel-collector",
                "path": "src/compute-plane-services/byoo-otel-collector",
                "service_name": "byoo-otel-collector",
                "legacy_tag_prefix": "byoo-otel-collector-v",
                "version_file": "VERSION",
            }

            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.github_release.publish_version_file_release(root, service, dry_run=True, draft=False)

            self.assertIn("byoo-otel-collector-v0.153.6 already exists", output.getvalue())
            self.assertIn("skipping", output.getvalue())

    def test_release_floor_anchor_uses_the_highest_internal_history_version(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            service_dir = root / "src/control-plane-services/helm-reval"
            service_dir.mkdir(parents=True)
            (service_dir / "README.md").write_text("initial\n")
            self.commit_all(root, "initial helm-reval")
            old_sha = subprocess.run(
                ["git", "rev-parse", "HEAD"], cwd=root, check=True, text=True, stdout=subprocess.PIPE
            ).stdout.strip()
            git(root, "tag", "src/control-plane-services/helm-reval/v0.4.0")
            (service_dir / "README.md").write_text("next\n")
            self.commit_all(root, "next helm-reval change")

            service = {
                "id": "helm-reval",
                "path": "src/control-plane-services/helm-reval",
                "service_name": "nvcf-helm-reval-api",
                "release_floor_version": "0.17.0",
            }

            with chdir(root):
                self.github_release.synthesize_release_floor_anchor(root, service)

            floor_tag = "src/control-plane-services/helm-reval/v0.17.0"
            self.assertEqual(
                subprocess.run(
                    ["git", "rev-list", "-n", "1", floor_tag], cwd=root, check=True, text=True, stdout=subprocess.PIPE
                ).stdout.strip(),
                old_sha,
            )
            with chdir(root):
                self.assertEqual(self.github_release.latest_service_tag(service), floor_tag)

    def test_helm_reval_cutover_is_a_stable_version_above_its_history_floor(self):
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        service = next(service for service in metadata["services"] if service["id"] == "helm-reval")

        floor = self.github_release.configured_stable_version(service, "release_floor_version")
        cutover = self.github_release.configured_stable_version(service, "cutover_version")

        self.assertEqual(floor, "0.17.0")
        self.assertEqual(cutover, "0.18.0")
        self.assertGreater(
            self.github_release.stable_semver_key(cutover),
            self.github_release.stable_semver_key(floor),
        )

    def test_cutover_release_dry_run_creates_the_configured_tag_once(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            service_dir = root / "src/control-plane-services/helm-reval"
            service_dir.mkdir(parents=True)
            (service_dir / "README.md").write_text("cutover\n")
            self.commit_all(root, "release configuration")
            service = {
                "id": "helm-reval",
                "path": "src/control-plane-services/helm-reval",
                "service_name": "nvcf-helm-reval-api",
                "cutover_version": "0.18.0",
            }

            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                self.assertTrue(self.github_release.publish_cutover_release(root, service, dry_run=True))

            self.assertIn("would create cutover tag src/control-plane-services/helm-reval/v0.18.0", output.getvalue())

    def test_nvca_branch_cut_uses_path_scoped_release_branch(self):
        service = {
            "id": "nvca",
            "path": "src/compute-plane-services/nvca",
            "service_name": "nvca",
            "legacy_tag_prefix": "nvca-v",
            "version_file": "VERSION",
            "dev_prerelease": True,
        }

        self.assertEqual(
            self.github_release.service_release_branch(service, "3.1.0"),
            "release-src/compute-plane-services/nvca/v3.1",
        )
        self.assertEqual(
            self.github_release.service_version_bump_branch(service, "3.1.0"),
            "release-bump/nvca/v3.1-to-v3.2",
        )
        self.assertEqual(self.github_release.next_release_train_version("3.1.0"), "3.2.0")

    def test_dev_prerelease_metadata_supports_branch_cut(self):
        root = SCRIPT_PATH.parents[2]
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        services = [service for service in metadata["services"] if service.get("dev_prerelease")]
        self.assertGreater(len(services), 0)

        for service in services:
            with self.subTest(service=service["id"]):
                self.assertTrue(service.get("version_file"))
                version = self.github_release.validate_version_file(root, service)
                self.assertNotIn("-", version)
                self.assertTrue(self.github_release.service_release_branch(service, version).startswith("release-"))
                self.assertTrue(self.github_release.service_version_bump_branch(service, version).startswith("release-bump/"))

    def test_release_branch_push_only_processes_matching_dev_prerelease_service(self):
        nvca = {
            "id": "nvca",
            "path": "src/compute-plane-services/nvca",
            "service_name": "nvca",
            "legacy_tag_prefix": "nvca-v",
            "version_file": "VERSION",
            "dev_prerelease": True,
        }
        compute_stack = {
            "id": "nvcf-compute-plane-stack",
            "path": "deploy/stacks/nvcf-compute-plane",
            "service_name": "nvcf-compute-plane-stack",
            "version_file": "VERSION",
            "dev_prerelease": True,
        }
        grpc_proxy = {
            "id": "grpc-proxy",
            "path": "src/invocation-plane-services/grpc-proxy",
            "service_name": "nvcf-grpc-proxy",
            "legacy_tag_prefix": "nvcf-grpc-proxy-v",
        }
        branch = "release-src/compute-plane-services/nvca/v3.1"

        self.assertTrue(self.github_release.should_process_auto_service(nvca, "", branch, "main"))
        self.assertFalse(self.github_release.should_process_auto_service(compute_stack, "", branch, "main"))
        self.assertFalse(self.github_release.should_process_auto_service(grpc_proxy, "", branch, "main"))
        self.assertTrue(self.github_release.should_process_auto_service(grpc_proxy, "", "main", "main"))
        self.assertFalse(self.github_release.should_process_auto_service(nvca, "grpc-proxy", branch, "main"))

    def test_branch_cut_dry_run_reports_release_branch_and_bump_pr(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            self.write_nvca_version(root, "3.1.0")
            metadata = {
                "version": 1,
                "services": [
                    {
                        "id": "nvca",
                        "path": "src/compute-plane-services/nvca",
                        "service_name": "nvca",
                        "legacy_tag_prefix": "nvca-v",
                        "version_file": "VERSION",
                        "dev_prerelease": True,
                    }
                ],
            }
            metadata_path = root / "metadata.json"
            metadata_path.write_text(json.dumps(metadata))
            self.commit_all(root, "seed nvca")

            args = types.SimpleNamespace(
                metadata=str(metadata_path),
                service="nvca",
                ref="HEAD",
                target_branch="main",
                dry_run=True,
            )
            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                self.github_release.branch_cut(args)

            text = output.getvalue()
            self.assertIn("release-src/compute-plane-services/nvca/v3.1", text)
            self.assertIn("release-bump/nvca/v3.1-to-v3.2", text)
            self.assertIn("src/compute-plane-services/nvca/VERSION=3.1.0->3.2.0", text)

    def test_branch_cut_requires_dev_prerelease_service(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            self.write_nvca_version(root, "3.1.0")
            metadata = {
                "version": 1,
                "services": [
                    {
                        "id": "nvca",
                        "path": "src/compute-plane-services/nvca",
                        "service_name": "nvca",
                        "version_file": "VERSION",
                    }
                ],
            }
            metadata_path = root / "metadata.json"
            metadata_path.write_text(json.dumps(metadata))
            self.commit_all(root, "seed nvca")

            args = types.SimpleNamespace(
                metadata=str(metadata_path),
                service="nvca",
                ref="HEAD",
                target_branch="main",
                dry_run=True,
            )
            with chdir(root), self.assertRaisesRegex(SystemExit, "branch-cut requires release.dev_prerelease"):
                self.github_release.branch_cut(args)


if __name__ == "__main__":
    unittest.main()

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


class SubprocessShim:
    """Stands in for the module's `subprocess`, intercepting only `gh` calls.

    Every other attribute, `run` included, falls through to the real module so the
    git plumbing under test keeps working.
    """

    def __init__(self, fake_run):
        self._fake_run = fake_run

    def __getattr__(self, name):
        return getattr(subprocess, name)

    @property
    def run(self):
        return self._fake_run


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

    def write_java_component(self, root, path, component_id, component_kind):
        component_dir = root / path
        component_dir.mkdir(parents=True, exist_ok=True)
        (component_dir / "bazel-java-ci.json").write_text(
            json.dumps(
                {
                    "ci_lane": "build-container",
                    "component_kind": component_kind,
                    "id": component_id,
                    "tests_skip": False,
                },
                indent=2,
            )
            + "\n"
        )
        (component_dir / "README.md").write_text(f"{component_id}\n")

    JAVA_FRAMEWORK_PATH = "src/libraries/java/nv-boot-parent"
    JAVA_SERVICES = (
        ("cloud-tasks", "src/control-plane-services/cloud-tasks", "1.6.2"),
        ("notary", "src/control-plane-services/notary", "1.8.4"),
    )

    def java_service_metadata(self, service_id, path):
        return {
            "id": service_id,
            "path": path,
            "service_name": f"nvcf-{service_id}",
        }

    def init_java_repo(self, root):
        """Repo with one Java framework, two Java services, and a release tag per service."""
        self.init_repo(root)
        self.write_java_component(root, self.JAVA_FRAMEWORK_PATH, "nv-boot-parent", "java-framework")
        for service_id, path, _version in self.JAVA_SERVICES:
            self.write_java_component(root, path, service_id, "java-service")
        self.commit_all(root, "seed java components")
        for _service_id, path, version in self.JAVA_SERVICES:
            git(root, "tag", f"{path}/v{version}")

    def commit_framework_change(self, root, message="fix(nv-boot): bump shared framework"):
        (root / self.JAVA_FRAMEWORK_PATH / "README.md").write_text(f"{message}\n")
        self.commit_all(root, message)

    def fanout_dry_run(self, root, service):
        components = self.github_release.java_ci_components(root)
        output = io.StringIO()
        with chdir(root), contextlib.redirect_stdout(output):
            created = self.github_release.publish_framework_dependency_release(
                root, service, components, dry_run=True, draft=False
            )
        return created, output.getvalue()

    def test_java_ci_components_match_registered_subprojects(self):
        root = SCRIPT_PATH.parents[2]
        components = self.github_release.java_ci_components(root)
        kinds = {component["path"]: component["component_kind"] for component in components}
        self.assertEqual(kinds.get("src/libraries/java/nv-boot-parent"), "java-framework")
        self.assertTrue(self.github_release.java_framework_paths(components))

        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        registered = {service["path"] for service in metadata["services"]}
        services = [c for c in components if c["component_kind"] == "java-service"]
        self.assertGreater(len(services), 0)
        for component in services:
            with self.subTest(component=component["id"]):
                # A java-service that is not a registered subproject can never
                # receive a dependency-triggered release.
                self.assertIn(component["path"], registered)
                self.assertTrue(
                    self.github_release.is_java_service(
                        components, {"id": component["id"], "path": component["path"]}
                    )
                )

    def test_framework_change_fans_out_a_patch_release_to_every_dependent_service(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_java_repo(root)
            self.commit_framework_change(root)

            for service_id, path, version in self.JAVA_SERVICES:
                with self.subTest(service=service_id):
                    service = self.java_service_metadata(service_id, path)
                    created, text = self.fanout_dry_run(root, service)
                    self.assertTrue(created)
                    expected = self.github_release.next_patch_version(version)
                    self.assertIn(f"would create {path}/v{expected}", text)
                    self.assertIn("dependency-triggered release", text)
                    self.assertIn(self.JAVA_FRAMEWORK_PATH, text)
                    self.assertIn("fix(nv-boot): bump shared framework", text)

    def test_framework_fanout_skips_a_component_that_is_not_a_java_service(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_java_repo(root)
            self.commit_framework_change(root)

            framework = {
                "id": "nv-boot-parent",
                "path": self.JAVA_FRAMEWORK_PATH,
                "service_name": "nv-boot-parent",
            }
            created, _text = self.fanout_dry_run(root, framework)
            self.assertFalse(created)

            go_service = {
                "id": "ratelimiter",
                "path": "src/invocation-plane-services/ratelimiter",
                "service_name": "nvcf-ratelimiter",
            }
            created, _text = self.fanout_dry_run(root, go_service)
            self.assertFalse(created)

    def test_no_framework_change_since_the_last_service_tag_releases_nothing(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_java_repo(root)
            self.commit_framework_change(root)
            # Release each service at the fanned-out patch, then assert a rerun
            # over the same framework commit is a no-op.
            for service_id, path, version in self.JAVA_SERVICES:
                git(root, "tag", f"{path}/v{self.github_release.next_patch_version(version)}")

            for service_id, path, _version in self.JAVA_SERVICES:
                with self.subTest(service=service_id):
                    service = self.java_service_metadata(service_id, path)
                    created, text = self.fanout_dry_run(root, service)
                    self.assertFalse(created)
                    self.assertIn("no release-worthy Java framework commits since", text)
                    self.assertNotIn("would create", text)

    def test_non_release_worthy_framework_commits_release_nothing(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_java_repo(root)
            for message in (
                "docs(nv-boot): document the shared framework",
                "chore(nv-boot): reformat",
                "refactor(nv-boot): extract a helper",
                "no conventional prefix at all",
            ):
                self.commit_framework_change(root, message)

            for service_id, path, _version in self.JAVA_SERVICES:
                with self.subTest(service=service_id):
                    service = self.java_service_metadata(service_id, path)
                    created, text = self.fanout_dry_run(root, service)
                    self.assertFalse(created)
                    self.assertIn("no release-worthy Java framework commits since", text)

            # One release-worthy framework commit is enough to fan out, and the
            # notes quote only the release-worthy ones.
            self.commit_framework_change(root, "perf(nv-boot): trim startup work")
            service_id, path, version = self.JAVA_SERVICES[0]
            created, text = self.fanout_dry_run(root, self.java_service_metadata(service_id, path))
            self.assertTrue(created)
            self.assertIn(f"would create {path}/v{self.github_release.next_patch_version(version)}", text)
            self.assertIn("perf(nv-boot): trim startup work", text)
            self.assertNotIn("chore(nv-boot): reformat", text)

    def test_releases_a_version_follows_the_configured_release_rules(self):
        releases_a_version = self.github_release.releases_a_version
        for subject in ("feat: x", "fix(scope): x", "perf: x", "chore(scope)!: x", "FIX: x"):
            self.assertTrue(releases_a_version(subject), subject)
        for subject in ("chore: x", "ci(scope): x", "docs: x", "style: x", "refactor: x",
                        "test: x", "build: x", "not a conventional commit"):
            self.assertFalse(releases_a_version(subject), subject)

    def test_framework_fanout_needs_an_existing_service_release_tag(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            self.write_java_component(root, self.JAVA_FRAMEWORK_PATH, "nv-boot-parent", "java-framework")
            self.write_java_component(root, "src/control-plane-services/notary", "notary", "java-service")
            self.commit_all(root, "seed java components")
            self.commit_framework_change(root)

            service = self.java_service_metadata("notary", "src/control-plane-services/notary")
            created, text = self.fanout_dry_run(root, service)
            self.assertFalse(created)
            self.assertIn("no existing release tag to bump from", text)

    def test_framework_fanout_dry_run_creates_no_tag_and_no_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_java_repo(root)
            self.commit_framework_change(root)
            before = self.list_tags(root)

            releases = []
            self.github_release.create_release = lambda *a, **k: releases.append(a)
            for service_id, path, _version in self.JAVA_SERVICES:
                created, _text = self.fanout_dry_run(root, self.java_service_metadata(service_id, path))
                self.assertTrue(created)

            self.assertEqual(self.list_tags(root), before)
            self.assertEqual(releases, [])

    def test_framework_fanout_publish_mode_tags_pushes_and_releases(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            remote = Path(tmp) / "remote.git"
            root.mkdir()
            subprocess.run(
                ["git", "init", "--bare", "--initial-branch=main", str(remote)],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.init_java_repo(root)
            git(root, "remote", "add", "origin", str(remote))
            git(root, "push", "origin", "HEAD")
            self.commit_framework_change(root)

            releases = []
            self.github_release.create_release = lambda tag, title, notes, draft, dry_run: releases.append(
                (tag, notes, draft, dry_run)
            )
            components = self.github_release.java_ci_components(root)
            service_id, path, version = self.JAVA_SERVICES[0]
            service = self.java_service_metadata(service_id, path)
            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                created = self.github_release.publish_framework_dependency_release(
                    root, service, components, dry_run=False, draft=False
                )

            expected_tag = f"{path}/v{self.github_release.next_patch_version(version)}"
            self.assertTrue(created)
            self.assertIn(expected_tag, self.list_tags(root))
            self.assertIn(expected_tag, self.list_tags(remote))
            self.assertEqual(len(releases), 1)
            self.assertEqual(releases[0][0], expected_tag)
            self.assertIn("dependency-triggered release", releases[0][1])

    def test_semantic_release_version_wins_over_dependency_fanout(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_java_repo(root)
            self.commit_framework_change(root)
            service_id, path, version = self.JAVA_SERVICES[0]
            (root / path / "README.md").write_text("service change\n")
            self.commit_all(root, "fix(cloud-tasks): service change")
            before = self.list_tags(root)

            releases = []
            self.github_release.create_release = lambda *a, **k: releases.append(a)
            components = self.github_release.java_ci_components(root)
            service = self.java_service_metadata(service_id, path)
            semantic_release_output = (
                "[semantic-release] > Analyzing commit: fix(cloud-tasks): service change\n"
                "[semantic-release] > The next release version is 1.6.3\n"
            )

            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                outcome = self.github_release.finish_semantic_release(
                    root, service, components, 0, semantic_release_output, dry_run=False, draft=False
                )

            text = output.getvalue()
            self.assertEqual(outcome, "released")
            self.assertIn(f"semantic-release created {path}/v1.6.3", text)
            self.assertNotIn("dependency-triggered", text)
            self.assertNotIn("dependency patch release", text)
            # No extra tag: the fan-out would have proposed the same patch line
            # and double-tagged the push.
            self.assertEqual(self.list_tags(root), before)
            self.assertEqual(releases, [])
            self.assertEqual(
                self.github_release.next_patch_version(version), "1.6.3", "fan-out would collide"
            )

    def test_semantic_release_no_release_falls_through_to_dependency_fanout(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_java_repo(root)
            self.commit_framework_change(root)
            components = self.github_release.java_ci_components(root)
            service_id, path, version = self.JAVA_SERVICES[0]
            service = self.java_service_metadata(service_id, path)
            no_release_output = "[semantic-release] > There are no relevant changes, so no new version is released.\n"

            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                outcome = self.github_release.finish_semantic_release(
                    root, service, components, 0, no_release_output, dry_run=True, draft=False
                )

            text = output.getvalue()
            self.assertEqual(outcome, "no-release")
            expected = self.github_release.next_patch_version(version)
            self.assertIn(f"would create {path}/v{expected}", text)
            self.assertIn("dependency-triggered release", text)

    def test_resolve_release_outcome_classifies_semantic_release_runs(self):
        self.assertEqual(
            self.github_release.resolve_release_outcome(0, "The next release version is 2.4.0"),
            "released",
        )
        self.assertEqual(
            self.github_release.resolve_release_outcome(
                0, "There are no relevant changes, so no new version is released."
            ),
            "no-release",
        )
        self.assertEqual(self.github_release.resolve_release_outcome(1, "boom"), "unknown")
        # A run that printed a version and then died is not trustworthy: the
        # publish run may not reproduce it, so it must be reported rather than
        # previewed as a tag.
        self.assertEqual(
            self.github_release.resolve_release_outcome(1, "The next release version is 2.4.0"),
            "unknown",
        )
        self.assertEqual(
            self.github_release.resolve_release_outcome(
                137, "There are no relevant changes, so no new version is released."
            ),
            "unknown",
        )

    def test_stale_checkout_is_not_classified_as_no_release(self):
        # Regression guard for the ess v0.4.10 miss: semantic-release printed
        # "is behind the remote", the helper called that a no-release, and the
        # run went green having tagged nothing.
        behind = (
            "[semantic-release] i The local branch main is behind the remote one, "
            "therefore a new version won't be published.\n"
        )
        self.assertEqual(
            self.github_release.resolve_release_outcome(0, behind), "stale-checkout"
        )
        self.assertFalse(self.github_release.no_release_output(behind))
        self.assertTrue(self.github_release.stale_checkout_output(behind))
        # A genuine no-release must stay a no-release.
        self.assertEqual(
            self.github_release.resolve_release_outcome(
                0, "There are no relevant changes, so no new version is released."
            ),
            "no-release",
        )
        # A stale checkout that also died is still just untrustworthy.
        self.assertEqual(self.github_release.resolve_release_outcome(1, behind), "unknown")

    def test_stale_checkout_does_not_fan_out_a_dependency_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_java_repo(root)
            self.commit_framework_change(root)
            components = self.github_release.java_ci_components(root)
            service_id, path, _version = self.JAVA_SERVICES[0]
            service = self.java_service_metadata(service_id, path)
            behind = (
                "[semantic-release] i The local branch main is behind the remote one, "
                "therefore a new version won't be published.\n"
            )

            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                outcome = self.github_release.finish_semantic_release(
                    root, service, components, 0, behind, dry_run=True, draft=False
                )

            text = output.getvalue()
            self.assertEqual(outcome, "stale-checkout")
            # The no-release path fans out a dependency release; this one must not.
            self.assertNotIn("would create", text)
            self.assertNotIn("dependency-triggered", text)
            self.assertIn("race, not a no-release", text)

    def test_failed_semantic_release_run_does_not_fan_out(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_java_repo(root)
            self.commit_framework_change(root)
            components = self.github_release.java_ci_components(root)
            service_id, path, _version = self.JAVA_SERVICES[0]
            service = self.java_service_metadata(service_id, path)

            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                outcome = self.github_release.finish_semantic_release(
                    root, service, components, 137, "killed mid-run\n", dry_run=True, draft=False
                )

            self.assertEqual(outcome, "unknown")
            self.assertNotIn("would create", output.getvalue())
            self.assertNotIn("dependency-triggered", output.getvalue())

    def list_tags(self, root):
        result = subprocess.run(
            ["git", "tag", "-l"], cwd=root, check=True, stdout=subprocess.PIPE, text=True
        )
        return sorted(line.strip() for line in result.stdout.splitlines() if line.strip())

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

    def _make_service_repo(self, root):
        self.init_repo(root)
        (root / "README.md").write_text("root\n")
        self.commit_all(root, "chore: init")
        service_dir = root / "deploy/helm/encrypted-secret-store"
        service_dir.mkdir(parents=True, exist_ok=True)
        (service_dir / "Chart.yaml").write_text("name: helm-nvcf-ess-api\n")
        self.commit_all(root, "feat: import ess chart")

    def _tags(self, root):
        result = subprocess.run(
            ["git", "tag"], cwd=root, check=True, stdout=subprocess.PIPE, text=True
        )
        return sorted(line.strip() for line in result.stdout.splitlines() if line.strip())

    def test_initial_version_anchor_defaults_to_floor(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_service_repo(root)
            service = {
                "id": "ess-helm",
                "path": "deploy/helm/encrypted-secret-store",
                "service_name": "helm-nvcf-ess-api",
            }
            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.synthesize_initial_version_anchor(root, service)
            self.assertIn("deploy/helm/encrypted-secret-store/v0.0.0", self._tags(root))

    def test_initial_version_anchor_honors_metadata(self):
        service = {
            "id": "ess-helm",
            "path": "deploy/helm/encrypted-secret-store",
            "service_name": "helm-nvcf-ess-api",
            "initial_version": "1.7.0",
        }
        expected_tag = self.github_release.tag_for_version(service, service["initial_version"])
        default_floor_tag = self.github_release.tag_for_version(
            service, self.github_release.INITIAL_RELEASE_FLOOR_VERSION
        )
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_service_repo(root)
            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.synthesize_initial_version_anchor(root, service)
            tags = self._tags(root)
            self.assertIn(expected_tag, tags)
            self.assertNotIn(default_floor_tag, tags)

    def test_initial_version_anchor_rejects_bad_semver(self):
        service = {
            "id": "ess-helm",
            "path": "deploy/helm/encrypted-secret-store",
            "service_name": "helm-nvcf-ess-api",
            "initial_version": "not-a-version",
        }
        with self.assertRaises(SystemExit):
            self.github_release.initial_floor_version(service)

    def test_initial_version_anchor_rejects_empty_string(self):
        service = {
            "id": "ess-helm",
            "path": "deploy/helm/encrypted-secret-store",
            "service_name": "helm-nvcf-ess-api",
            "initial_version": "",
        }
        with self.assertRaises(SystemExit):
            self.github_release.initial_floor_version(service)

    def _make_ct_service_repo(self, root):
        self.init_repo(root)
        (root / "README.md").write_text("root\n")
        self.commit_all(root, "chore: init")
        service_dir = root / "deploy/helm/cloud-tasks"
        service_dir.mkdir(parents=True, exist_ok=True)
        (service_dir / "Chart.yaml").write_text("name: helm-nvcf-nvct-api\n")
        self.commit_all(root, "feat: import cloud tasks chart")

    def test_cf_initial_version_anchor_defaults_to_floor(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_ct_service_repo(root)
            service = {
                "id": "cloud-tasks-helm",
                "path": "deploy/helm/cloud-tasks",
                "service_name": "helm-nvcf-nvct-api",
            }
            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.synthesize_initial_version_anchor(root, service)
            self.assertIn("deploy/helm/cloud-tasks/v0.0.0", self._tags(root))

    def test_cf_initial_version_anchor_honors_metadata(self):
        service = {
                "id": "cloud-tasks-helm",
                "path": "deploy/helm/cloud-tasks",
                "service_name": "helm-nvcf-nvct-api",
                "initial_version": "1.4.4",
        }
        expected_tag = self.github_release.tag_for_version(service, service["initial_version"])
        default_floor_tag = self.github_release.tag_for_version(
            service, self.github_release.INITIAL_RELEASE_FLOOR_VERSION
        )
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_ct_service_repo(root)
            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.synthesize_initial_version_anchor(root, service)
            tags = self._tags(root)
            self.assertIn(expected_tag, tags)
            self.assertNotIn(default_floor_tag, tags)

    def test_cf_initial_version_anchor_rejects_bad_semver(self):
        service = {
            "id": "cloud-tasks-helm",
            "path": "deploy/helm/cloud-tasks",
            "service_name": "helm-nvcf-nvct-api",
            "initial_version": "not-a-version",
        }
        with self.assertRaises(SystemExit):
            self.github_release.initial_floor_version(service)

    def test_cf_initial_version_anchor_rejects_empty_string(self):
        service = {
            "id": "cloud-tasks-helm",
            "path": "deploy/helm/cloud-tasks",
            "service_name": "helm-nvcf-nvct-api",
            "initial_version": "",
        }
        with self.assertRaises(SystemExit):
            self.github_release.initial_floor_version(service)

    def composite_byoo_service(self):
        return {
            "id": "byoo-otel-collector",
            "path": "src/compute-plane-services/byoo-otel-collector",
            "service_name": "byoo-otel-collector",
            "tag_format": "src/compute-plane-services/byoo-otel-collector/v${upstream_version}-nv-${version}",
            "tag_upstream_version_file": "otel-collector-build.yaml",
            "tag_upstream_version_pattern": "(?m)^\\s*otelcol_version:\\s*(?P<version>(?:0|[1-9]\\d*)\\.(?:0|[1-9]\\d*)\\.(?:0|[1-9]\\d*))\\s*$",
            "initial_version": "0.0.0",
            "reset_release_history": True,
            "release_history_marker_file": "RELEASE_SERIES_START",
            "legacy_tag_prefixes": [
                "src/compute-plane-services/byoo-otel-collector/v",
                "byoo-otel-collector-v",
            ],
        }

    def write_composite_byoo_source(self, root, upstream_version):
        service_dir = root / "src/compute-plane-services/byoo-otel-collector"
        service_dir.mkdir(parents=True, exist_ok=True)
        (service_dir / "otel-collector-build.yaml").write_text(
            f"dist:\n  module: example.test/byoo\n  otelcol_version: {upstream_version}\n"
            "exporters:\n  - gomod: example.test/incidental v9.9.9\n"
        )
        (service_dir / "README.md").write_text("BYOO\n")

    def test_composite_tag_starts_a_wrapper_series_without_using_legacy_tags(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            self.write_composite_byoo_source(root, "0.157.0")
            self.commit_all(root, "feat(byoo): import collector")
            git(root, "tag", "src/compute-plane-services/byoo-otel-collector/v0.157.19")
            marker = root / "src/compute-plane-services/byoo-otel-collector" / "RELEASE_SERIES_START"
            marker.write_text("semantic-release wrapper series\n")
            self.commit_all(root, "feat(byoo): adopt wrapper semantic releases")
            service = self.composite_byoo_service()

            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.synthesize_current_prefix_anchor(root, service)
                self.github_release.synthesize_initial_version_anchor(root, service)

            self.assertIn(
                "src/compute-plane-services/byoo-otel-collector/v0.157.0-nv-0.0.0",
                self._tags(root),
            )
            self.assertEqual(
                self.github_release.tag_sha(
                    root,
                    "src/compute-plane-services/byoo-otel-collector/v0.157.0-nv-0.0.0",
                ),
                self.github_release.run(["git", "rev-parse", "HEAD^"], cwd=root, capture=True).strip(),
            )
            self.assertEqual(
                self.github_release.tag_for_version(service, "0.1.0", root),
                "src/compute-plane-services/byoo-otel-collector/v0.157.0-nv-0.1.0",
            )
            parsed = self.github_release.parse_release_tag(
                "src/compute-plane-services/byoo-otel-collector/v0.157.0-nv-0.1.0",
                {"version": 1, "services": [service]},
                root,
            )
            self.assertEqual(parsed["package"], "byoo-otel-collector")
            self.assertEqual(parsed["version"], "0.1.0")

    def test_composite_tag_keeps_the_wrapper_series_across_an_upstream_bump(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            self.write_composite_byoo_source(root, "0.157.0")
            self.commit_all(root, "feat(byoo): import collector")
            service = self.composite_byoo_service()
            git(root, "tag", self.github_release.tag_for_version(service, "0.1.0", root))

            self.write_composite_byoo_source(root, "1.0.0")
            self.commit_all(root, "feat(byoo): update collector")
            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.synthesize_current_prefix_anchor(root, service)

            self.assertIn(
                "src/compute-plane-services/byoo-otel-collector/v1.0.0-nv-0.1.0",
                self._tags(root),
            )

    def test_byoo_release_metadata_uses_composite_tags_without_a_version_file(self):
        root = SCRIPT_PATH.parents[2]
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        service = next(s for s in metadata["services"] if s["id"] == "byoo-otel-collector")
        self.assertNotIn("version_file", service)
        self.assertNotIn("version_major_minor_source_file", service)
        self.assertEqual(service["initial_version"], "0.0.0")
        self.assertTrue(service["reset_release_history"])
        self.assertEqual(service["release_history_marker_file"], "RELEASE_SERIES_START")
        self.assertEqual(
            service["tag_format"],
            "src/compute-plane-services/byoo-otel-collector/v${upstream_version}-nv-${version}",
        )
        self.assertEqual(
            self.github_release.tag_for_version(service, "0.1.0", root),
            "src/compute-plane-services/byoo-otel-collector/v0.157.0-nv-0.1.0",
        )

    def test_cloud_tasks_chart_continues_its_published_lineage(self):
        # This chart migrated in from its own colocated-deploy repo with 11
        # versions already published as helm-nvcf-nvct-api, the newest 1.4.4.
        #
        # Both fields below are load-bearing and both have a plausible wrong
        # value. Without initial_version the floor is 0.0.0, so the first
        # release computed here would land below everything already published.
        # And 1.6.x, the number the chart's own appVersion carries, belongs to
        # the cloud-tasks service, not to the chart.
        #
        # service_name is what the chart is published as. A service-shaped
        # name would open an empty second chart repo and strand all 11
        # existing versions while the pipeline still reported success.
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        service = next(s for s in metadata["services"] if s["id"] == "cloud-tasks-helm")
        self.assertEqual(service["service_name"], "helm-nvcf-nvct-api")
        self.assertEqual(service["initial_version"], "1.4.4")
        self.assertEqual(
            self.github_release.tag_for_version(service, service["initial_version"]),
            "deploy/helm/cloud-tasks/v1.4.4",
        )

    def test_http_invocation_chart_uses_its_published_lineage(self):
        root = SCRIPT_PATH.parents[2]
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        service = next(s for s in metadata["services"] if s["id"] == "http-invocation-helm")

        self.assertEqual(service["path"], "deploy/helm/http-invocation")
        self.assertEqual(service["service_name"], "helm-nvcf-invocation-service")
        self.assertEqual(service["deploys"], ["http-invocation"])
        self.assertEqual(
            self.github_release.tag_for_version(service, "1.5.6", root),
            "deploy/helm/http-invocation/v1.5.6",
        )

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

    def test_linear_release_branch_base_preserves_the_selected_tree(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            self.write_nvca_version(root, "3.2.0")
            self.commit_all(root, "seed nvca")
            main_branch = self.github_release.run(
                ["git", "branch", "--show-current"], cwd=root, capture=True
            ).strip()

            git(root, "switch", "-c", "merged-change")
            (root / "merged.txt").write_text("merged change\n")
            self.commit_all(root, "fix: merged change")

            git(root, "switch", main_branch)
            (root / "main.txt").write_text("main change\n")
            self.commit_all(root, "fix: main change")
            git(root, "merge", "--no-ff", "merged-change", "-m", "Merge merged-change")
            (root / "src/compute-plane-services/nvca" / "README.md").write_text("release head\n")
            self.commit_all(root, "fix(nvca): prepare release")

            base_sha = self.github_release.run(
                ["git", "rev-parse", "HEAD"], cwd=root, capture=True
            ).strip()
            release_base = self.github_release.linear_release_branch_base(root, base_sha)

            self.assertNotEqual(release_base, base_sha)
            self.assertEqual(
                self.github_release.commit_tree(root, release_base),
                self.github_release.commit_tree(root, base_sha),
            )
            self.assertEqual(
                self.github_release.run(
                    ["git", "rev-list", "--merges", release_base], cwd=root, capture=True
                ).strip(),
                "",
            )

            git(root, "switch", "-c", "release-bump/nvca/v3.2-to-v3.3", release_base)
            (root / "src/compute-plane-services/nvca" / "VERSION").write_text("3.3.0\n")
            self.commit_all(root, "chore(nvca): advance release train to v3.3.0")
            bump_head = self.github_release.run(
                ["git", "rev-parse", "HEAD"], cwd=root, capture=True
            ).strip()

            self.assertEqual(
                self.github_release.run(
                    ["git", "rev-parse", f"{bump_head}^"], cwd=root, capture=True
                ).strip(),
                release_base,
            )
            self.assertEqual(
                self.github_release.run(
                    ["git", "diff", "--name-only", base_sha, bump_head], cwd=root, capture=True
                ).strip(),
                "src/compute-plane-services/nvca/VERSION",
            )
            self.assertEqual(
                self.github_release.run(
                    ["git", "diff", "--name-only", f"{release_base}...{bump_head}"], cwd=root, capture=True
                ).strip(),
                "src/compute-plane-services/nvca/VERSION",
            )
            self.assertEqual(
                self.github_release.run(
                    ["git", "rev-list", "--merges", bump_head], cwd=root, capture=True
                ).strip(),
                "",
            )

    def test_linear_release_branch_base_keeps_a_linear_base(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            self.write_nvca_version(root, "3.2.0")
            self.commit_all(root, "seed nvca")
            base_sha = self.github_release.run(
                ["git", "rev-parse", "HEAD"], cwd=root, capture=True
            ).strip()

            self.assertEqual(self.github_release.linear_release_branch_base(root, base_sha), base_sha)

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


    NVCA_SERVICE = {
        "id": "nvca",
        "path": "src/compute-plane-services/nvca",
        "service_name": "nvca",
        "legacy_tag_prefix": "nvca-v",
        "version_file": "VERSION",
        "dev_prerelease": True,
    }

    def stub_gh_comments(self, pull_requests, failing=()):
        """Route `gh pr comment` to a recorder and stub the two API lookups.

        Returns the list that collects (pull request number, comment body).
        """
        real_run = subprocess.run
        posted = []

        def fake_run(args, *rest, **kwargs):
            if list(args[:3]) == ["gh", "pr", "comment"]:
                number = args[3]
                body = args[args.index("--body") + 1]
                if number in failing:
                    return subprocess.CompletedProcess(args, 1, stdout="pull request is locked")
                posted.append((number, body))
                return subprocess.CompletedProcess(args, 0, stdout="")
            return real_run(args, *rest, **kwargs)

        self.github_release.subprocess = SubprocessShim(fake_run)
        self.github_release.repo_slug = lambda: "NVIDIA/nvcf"
        self.github_release.pull_requests_for_commit = lambda slug, sha: pull_requests.get(sha, [])
        return posted

    def nvca_repo_with_tag(self, root, version="3.2.0"):
        """Seed an nvca repo whose HEAD carries the service tag for `version`."""
        self.init_repo(root)
        self.write_nvca_version(root, version)
        self.commit_all(root, "seed nvca")
        git(root, "tag", f"src/compute-plane-services/nvca/v{version}")

    def commit_backport(self, root, message):
        (root / "src/compute-plane-services/nvca/README.md").write_text(f"{message}\n")
        self.commit_all(root, message)
        return self.github_release.run(["git", "rev-parse", "HEAD"], cwd=root, capture=True).strip()

    def test_ancestor_service_tag_ignores_a_higher_tag_off_the_branch(self):
        # A release branch must bound its range by what it actually contains. The
        # highest-sorting tag can be a main-line tag the branch never had.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.nvca_repo_with_tag(root, "3.2.0")
            self.commit_backport(root, "feat(nvca): main only")
            git(root, "tag", "src/compute-plane-services/nvca/v3.3.0-dev.0")
            git(root, "checkout", "-b", "release-src/compute-plane-services/nvca/v3.2", "HEAD~1")
            self.commit_backport(root, "fix(nvca): backport")

            self.assertEqual(
                self.github_release.ancestor_service_tag(root, self.NVCA_SERVICE),
                "src/compute-plane-services/nvca/v3.2.0",
            )
            self.assertEqual(
                self.github_release.latest_service_tag(self.NVCA_SERVICE, root),
                "src/compute-plane-services/nvca/v3.3.0-dev.0",
                "the version sort would have bounded the range with an unreachable tag",
            )

    def test_ancestor_service_tag_prefers_the_closest_of_several_prefixes(self):
        # Services carry legacy prefixes alongside the current one, and the newest
        # release can sit on either. Taking the first prefix to match would reach
        # past a closer tag and re-resolve commits an earlier release covered.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.nvca_repo_with_tag(root, "3.2.0")
            self.commit_backport(root, "fix(nvca): released under the legacy prefix")
            git(root, "tag", "nvca-v3.2.1")
            self.commit_backport(root, "fix(nvca): not yet released")

            self.assertEqual(
                self.github_release.ancestor_service_tag(root, self.NVCA_SERVICE), "nvca-v3.2.1"
            )
            self.assertEqual(
                self.github_release.tag_prefixes(self.NVCA_SERVICE, root),
                ["src/compute-plane-services/nvca/v", "nvca-v"],
                "the current prefix is checked first, so a closer legacy tag must still win",
            )

    def test_released_commits_covers_every_merge_since_the_previous_tag(self):
        # The concurrency group cancels queued runs, so one tag can carry several
        # merges. All of them have to be resolved, not just HEAD.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.nvca_repo_with_tag(root)
            first = self.commit_backport(root, "fix(nvca): first backport (#1249)")
            second = self.commit_backport(root, "fix(nvca): second backport (#1250)")

            commits = self.github_release.released_commits(root, "src/compute-plane-services/nvca/v3.2.0")
            self.assertEqual(commits, [second, first])

    def test_released_commits_without_a_previous_tag_resolves_only_head(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            self.write_nvca_version(root, "3.2.0")
            self.commit_all(root, "seed nvca")
            head = self.commit_backport(root, "fix(nvca): first ever release")

            self.assertEqual(self.github_release.released_commits(root, ""), [head])

    def test_released_commits_reports_a_truncated_range(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.nvca_repo_with_tag(root)
            self.github_release.MAX_RELEASE_COMMENT_COMMITS = 2
            for index in range(4):
                self.commit_backport(root, f"fix(nvca): backport {index}")

            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                commits = self.github_release.released_commits(root, "src/compute-plane-services/nvca/v3.2.0")

            self.assertEqual(len(commits), 2)
            self.assertIn("only the newest 2 are resolved", output.getvalue())

    def test_comment_release_posts_once_per_pull_request(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.nvca_repo_with_tag(root)
            first = self.commit_backport(root, "fix(nvca): first backport")
            second = self.commit_backport(root, "fix(nvca): second backport")
            # The same PR can own more than one commit in the range.
            posted = self.stub_gh_comments({first: ["1249"], second: ["1250", "1249"]})

            with contextlib.redirect_stdout(io.StringIO()):
                commented = self.github_release.comment_release_on_pull_requests(
                    root,
                    self.NVCA_SERVICE,
                    "src/compute-plane-services/nvca/v3.2.1",
                    "3.2.1",
                    "src/compute-plane-services/nvca/v3.2.0",
                )

            self.assertEqual(commented, ["1250", "1249"])
            self.assertEqual([number for number, _body in posted], ["1250", "1249"])
            body = posted[0][1]
            self.assertIn("This PR is included in version 3.2.1.", body)
            self.assertIn(
                "https://github.com/NVIDIA/nvcf/releases/tag/src/compute-plane-services/nvca/v3.2.1",
                body,
            )

    def test_comment_release_survives_a_failed_comment(self):
        # The tag, the push, and the GitHub release already succeeded. A comment
        # that cannot be posted must not turn a shipped release into a failure.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.nvca_repo_with_tag(root)
            first = self.commit_backport(root, "fix(nvca): first backport")
            posted = self.stub_gh_comments({first: ["1249", "1250"]}, failing=("1249",))

            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                commented = self.github_release.comment_release_on_pull_requests(
                    root,
                    self.NVCA_SERVICE,
                    "src/compute-plane-services/nvca/v3.2.1",
                    "3.2.1",
                    "src/compute-plane-services/nvca/v3.2.0",
                )

            self.assertEqual(commented, ["1250"])
            self.assertEqual([number for number, _body in posted], ["1250"])
            self.assertIn("could not comment", output.getvalue())

    def test_create_release_reports_whether_it_created_the_release(self):
        existing = {"seen": False}

        def fake_run(args, *rest, **kwargs):
            if list(args[:3]) == ["gh", "release", "view"]:
                return subprocess.CompletedProcess(args, 0 if existing["seen"] else 1)
            raise AssertionError(f"unexpected call: {args}")

        self.github_release.subprocess = SubprocessShim(fake_run)
        self.github_release.run = lambda *a, **k: ""

        with contextlib.redirect_stdout(io.StringIO()):
            self.assertTrue(self.github_release.create_release("t", "t", "n", draft=False, dry_run=False))
            existing["seen"] = True
            self.assertFalse(self.github_release.create_release("t", "t", "n", draft=False, dry_run=False))
            self.assertFalse(self.github_release.create_release("t", "t", "n", draft=False, dry_run=True))

    def publish_and_capture_comments(self, version):
        """Publish a tag for `version` from a release branch, recording any comments.

        The repo is shaped like the real thing: a release branch holding the 3.2.x
        line, with a higher dev tag on the default branch that this branch never
        contained.
        """
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            remote = Path(tmp) / "remote.git"
            root.mkdir()
            subprocess.run(
                ["git", "init", "--bare", "--initial-branch=main", str(remote)],
                check=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.nvca_repo_with_tag(root)
            git(root, "remote", "add", "origin", str(remote))
            git(root, "push", "origin", "HEAD")
            self.commit_backport(root, "feat(nvca): default branch only")
            git(root, "tag", "src/compute-plane-services/nvca/v3.3.0-dev.0")
            git(root, "checkout", "-b", "release-src/compute-plane-services/nvca/v3.2", "HEAD~1")
            self.commit_backport(root, "fix(nvca): backport")

            comments = []
            self.github_release.create_release = lambda *a, **k: True
            self.github_release.comment_release_on_pull_requests = (
                lambda root, service, tag, version, since_tag: comments.append((tag, version, since_tag))
            )
            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.publish_tag_for_version(
                    root, self.NVCA_SERVICE, version, dry_run=False, draft=False, reason="VERSION"
                )
            return comments

    def test_publish_tag_comments_on_a_stable_release(self):
        comments = self.publish_and_capture_comments("3.2.1")
        self.assertEqual(
            comments,
            [("src/compute-plane-services/nvca/v3.2.1", "3.2.1", "src/compute-plane-services/nvca/v3.2.0")],
            "the range must be bounded by the newest tag on this branch, not the highest tag overall",
        )

    def test_publish_tag_stays_quiet_for_a_dev_prerelease(self):
        # Dev prereleases are internal checkpoints on the default branch, not
        # something to announce on a pull request.
        self.assertEqual(self.publish_and_capture_comments("3.3.0-dev.4"), [])


if __name__ == "__main__":
    unittest.main()

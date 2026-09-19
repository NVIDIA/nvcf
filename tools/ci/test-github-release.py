#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import shutil
import subprocess
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock


SCRIPT_PATH = Path(__file__).with_name("github-release")


def load_github_release():
    loader = importlib.machinery.SourceFileLoader("github_release", str(SCRIPT_PATH))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


def git(root, *args):
    subprocess.run(["git", *args], cwd=root, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)


def git_out(root, *args):
    """git, returning stdout. The plain helper above discards it."""
    return subprocess.run(
        ["git", *args], cwd=root, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True
    ).stdout


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
        # `current_branch` prefers GITHUB_REF_TYPE/GITHUB_REF_NAME over the
        # checked-out branch, and both are set on every Actions runner. A test
        # that builds a repo in a temp directory would otherwise be told it is
        # on the pull request's merge ref, which is how this suite passed
        # locally and failed in CI. Any test that wants them sets them itself.
        env = mock.patch.dict(os.environ, {}, clear=False)
        env.start()
        self.addCleanup(env.stop)
        os.environ.pop("GITHUB_REF_TYPE", None)
        os.environ.pop("GITHUB_REF_NAME", None)

    def init_repo(self, root):
        git(root, "init")
        git(root, "config", "user.email", "test@example.com")
        git(root, "config", "user.name", "Test User")

    def seed_nvca_service(self, root):
        service_dir = root / "src/compute-plane-services/nvca"
        service_dir.mkdir(parents=True, exist_ok=True)
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

    def _make_prerelease_service_repo(self, root):
        """A service whose only tags are prereleases, like nvca before the migration."""
        self.init_repo(root)
        (root / "README.md").write_text("root\n")
        self.commit_all(root, "chore: init")
        service_dir = root / "src/compute-plane-services/nvca"
        service_dir.mkdir(parents=True, exist_ok=True)
        (service_dir / "README.md").write_text("nvca\n")
        self.commit_all(root, "feat(nvca): import service")

    NVCA_FLOOR_SERVICE = {
        "id": "nvca",
        "path": "src/compute-plane-services/nvca",
        "service_name": "nvca",
        "initial_version": "3.3.0",
    }

    def test_floor_applies_when_only_prerelease_tags_exist(self):
        # The migration case: hundreds of -dev.N tags used to suppress the floor
        # entirely, so semantic-release saw no baseline and restarted the line.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_prerelease_service_repo(root)
            git(root, "tag", "src/compute-plane-services/nvca/v3.3.0-dev.1")
            (root / "src/compute-plane-services/nvca" / "README.md").write_text("more\n")
            self.commit_all(root, "fix(nvca): later change")
            git(root, "tag", "src/compute-plane-services/nvca/v3.3.0-dev.2")

            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.synthesize_initial_version_anchor(root, self.NVCA_FLOOR_SERVICE)

            self.assertIn("src/compute-plane-services/nvca/v3.3.0", self._tags(root))

    def test_floor_applies_when_the_stable_line_is_not_reachable(self):
        # nvca's 3.2 line lives on a maintenance branch cut with a synthetic root,
        # so it is not an ancestor of the default branch and is not a baseline.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_prerelease_service_repo(root)
            main_branch = self.github_release.run(
                ["git", "branch", "--show-current"], cwd=root, capture=True
            ).strip()

            git(root, "switch", "-c", "release-nvca-3.2")
            (root / "src/compute-plane-services/nvca" / "README.md").write_text("on the train\n")
            self.commit_all(root, "fix(nvca): patch on the release train")
            git(root, "tag", "src/compute-plane-services/nvca/v3.2.17")
            git(root, "switch", main_branch)

            # Checked before synthesis: afterwards the floor tag itself is a
            # reachable stable tag and would be reported as the baseline.
            self.assertEqual(self.github_release.release_baseline_version(root, self.NVCA_FLOOR_SERVICE), "")

            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.synthesize_initial_version_anchor(root, self.NVCA_FLOOR_SERVICE)

            self.assertIn("src/compute-plane-services/nvca/v3.3.0", self._tags(root))
            # The floor must land in HEAD's history. Anchoring it on the
            # maintenance-branch tag would put it outside the history
            # semantic-release walks, so the floor would be ignored entirely.
            self.assertTrue(
                self.github_release.tag_is_reachable(root, "src/compute-plane-services/nvca/v3.3.0"),
                "the synthesized floor anchor must be reachable from HEAD",
            )

    def test_floor_is_ignored_once_a_reachable_stable_tag_catches_up(self):
        # The floor is a floor, not an override: a real release at or above it wins.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_prerelease_service_repo(root)
            git(root, "tag", "src/compute-plane-services/nvca/v3.4.0")

            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.synthesize_initial_version_anchor(root, self.NVCA_FLOOR_SERVICE)

            self.assertEqual(
                self.github_release.release_baseline_version(root, self.NVCA_FLOOR_SERVICE), "3.4.0"
            )
            self.assertNotIn("src/compute-plane-services/nvca/v3.3.0", self._tags(root))

    def test_floor_anchor_lands_on_the_newest_tag_not_the_start_of_history(self):
        # Anchoring at the start of the subtree would hand semantic-release every
        # commit the service ever had, so one historical `feat!` could force a major.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._make_prerelease_service_repo(root)
            git(root, "tag", "src/compute-plane-services/nvca/v3.3.0-dev.1")
            newest = self.github_release.run(
                ["git", "rev-parse", "HEAD"], cwd=root, capture=True
            ).strip()
            (root / "src/compute-plane-services/nvca" / "README.md").write_text("after\n")
            self.commit_all(root, "fix(nvca): after the last dev tag")

            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.synthesize_initial_version_anchor(root, self.NVCA_FLOOR_SERVICE)

            anchored = self.github_release.tag_sha(root, "src/compute-plane-services/nvca/v3.3.0")
            self.assertEqual(anchored, newest)

    def test_nvca_resolves_a_floor_above_its_baseline(self):
        # Guards nvca's cutover onto semantic-release: if it stopped needing its
        # floor, its next automatic release would silently restart the line. The
        # three stacks migrated with it and have since moved back to the VERSION
        # file, so they have no floor to check.
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        by_id = {s["id"]: s for s in metadata["services"]}
        self.assertEqual(self.github_release.initial_floor_version(by_id["nvca"]), "3.3.0")

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
        upstream_version = self.github_release.tag_upstream_version(service, root)
        self.assertEqual(
            self.github_release.tag_for_version(service, "0.1.0", root),
            f"src/compute-plane-services/byoo-otel-collector/v{upstream_version}-nv-0.1.0",
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

    def test_reval_chart_continues_its_published_lineage(self):
        metadata = json.loads(
            SCRIPT_PATH.with_name("github-release-subprojects.json").read_text()
        )
        service = next(s for s in metadata["services"] if s["id"] == "reval-helm")

        self.assertEqual(service["path"], "deploy/helm/helm-reval")
        self.assertEqual(service["service_name"], "helm-reval")
        self.assertEqual(service["initial_version"], "1.3.8")
        self.assertEqual(service["deploys"], ["helm-reval"])
        self.assertEqual(
            self.github_release.tag_for_version(service, service["initial_version"]),
            "deploy/helm/helm-reval/v1.3.8",
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

    NVCA_MAIN_SERVICE = {
        "id": "nvca",
        "path": "src/compute-plane-services/nvca",
        "service_name": "nvca",
        "legacy_tag_prefix": "nvca-v",
        "initial_version": "3.3.0",
    }
    GRPC_PROXY_SERVICE = {
        "id": "grpc-proxy",
        "path": "src/invocation-plane-services/grpc-proxy",
        "service_name": "nvcf-grpc-proxy",
        "legacy_tag_prefix": "nvcf-grpc-proxy-v",
    }
    SELF_MANAGED_STACK_SERVICE = {
        "id": "nvcf-self-managed-stack",
        "path": "deploy/stacks/self-managed",
        "service_name": "nvcf-self-managed-stack",
        "tag_format": "deploy/stacks/self-managed/v${version}",
        "version_file": "VERSION",
        "release_branch_only": True,
    }

    def test_only_the_default_branch_releases_a_semantic_release_service(self):
        release_branch = "release-src/compute-plane-services/nvca/v3.1"

        # Maintenance branches for these still build and test but do not
        # release: a tag on one of them is cut by hand.
        for service in (self.NVCA_MAIN_SERVICE, self.GRPC_PROXY_SERVICE):
            with self.subTest(service=service["id"]):
                self.assertTrue(self.github_release.should_process_auto_service(service, "", "main", "main"))
                self.assertFalse(
                    self.github_release.should_process_auto_service(service, "", release_branch, "main")
                )

        # The service filter still scopes a run to one service.
        self.assertFalse(
            self.github_release.should_process_auto_service(self.NVCA_MAIN_SERVICE, "grpc-proxy", "main", "main")
        )
        self.assertTrue(
            self.github_release.should_process_auto_service(self.GRPC_PROXY_SERVICE, "grpc-proxy", "main", "main")
        )

    def test_a_release_branch_only_service_never_releases_from_main(self):
        stack = self.SELF_MANAGED_STACK_SERVICE
        own_branch = "release-deploy/stacks/self-managed/v0.21"

        self.assertTrue(self.github_release.should_process_auto_service(stack, "", own_branch, "main"))
        # Including a detached checkout, which `auto` treats as the default
        # branch for every other service.
        for branch in ("main", ""):
            with self.subTest(branch=branch or "<detached>"):
                self.assertFalse(self.github_release.should_process_auto_service(stack, "", branch, "main"))

        # Another subproject's maintenance branch, and a branch whose suffix is
        # not an X.Y train, are both somebody else's push.
        for branch in (
            "release-src/compute-plane-services/nvca/v3.1",
            "release-deploy/stacks/observability/v0.3",
            "release-deploy/stacks/self-managed/v0.21.4",
            "release-deploy/stacks/self-managed/vnext",
        ):
            with self.subTest(branch=branch):
                self.assertFalse(self.github_release.should_process_auto_service(stack, "", branch, "main"))

        # A push to a stack's maintenance branch must not release anything else.
        for service in (self.NVCA_MAIN_SERVICE, self.GRPC_PROXY_SERVICE):
            with self.subTest(service=service["id"]):
                self.assertFalse(
                    self.github_release.should_process_auto_service(service, "", own_branch, "main")
                )

    def _seed_stack_release_branch(self, root, version, branch):
        """A stack repo checked out on `branch` with VERSION set to `version`."""
        self.init_repo(root)
        stack_dir = root / "deploy/stacks/self-managed"
        stack_dir.mkdir(parents=True, exist_ok=True)
        (stack_dir / "VERSION").write_text(f"{version}\n")
        (stack_dir / "README.md").write_text("stack\n")
        self.commit_all(root, "chore: seed the self-managed stack")
        if branch:
            git(root, "switch", "-c", branch)

    def test_release_branch_cuts_the_trains_first_version_then_patches(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            branch = "release-deploy/stacks/self-managed/v0.21"
            self._seed_stack_release_branch(root, "0.21.0", branch)

            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                self.github_release.publish_release_branch_release(
                    root, self.SELF_MANAGED_STACK_SERVICE, dry_run=True, draft=False
                )
            self.assertIn("would create deploy/stacks/self-managed/v0.21.0", output.getvalue())

            git(root, "tag", "deploy/stacks/self-managed/v0.21.0")
            (root / "deploy/stacks/self-managed/README.md").write_text("backport\n")
            self.commit_all(root, "fix(self-managed): backport")

            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                self.github_release.publish_release_branch_release(
                    root, self.SELF_MANAGED_STACK_SERVICE, dry_run=True, draft=False
                )
            self.assertIn("would create deploy/stacks/self-managed/v0.21.1", output.getvalue())

    def _run_auto(self, root, service, expected_branch, default_branch):
        """`auto` as the workflow runs it, on the checked-out branch."""
        metadata_path = root / "metadata.json"
        metadata_path.write_text(json.dumps({"version": 1, "services": [service]}))
        env = {
            "NVCF_GITHUB_AUTO_TAGGING_ENABLED": "true",
            "NVCF_GITHUB_RELEASE_DRY_RUN": "true",
            "GITHUB_DEFAULT_BRANCH": default_branch,
        }
        args = types.SimpleNamespace(metadata=str(metadata_path), service=service["id"])
        output = io.StringIO()
        with mock.patch.dict(os.environ, env, clear=False):
            with chdir(root), contextlib.redirect_stdout(output):
                self.github_release.auto_release(args)
        self.assertIn(f"branch={expected_branch}", output.getvalue())
        return output.getvalue()

    def test_auto_releases_a_stack_from_its_branch_and_not_from_the_default_branch(self):
        # The whole point of the model: a merge to the default branch must leave
        # the stack's version alone, and the maintenance branch is what moves it.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "0.21.0", "")
            # Read rather than assume: `git init` names the first branch from
            # the machine's init.defaultBranch, which is not `main` everywhere.
            default_branch = self.github_release.run(
                ["git", "branch", "--show-current"], cwd=root, capture=True
            ).strip()
            (root / "deploy/stacks/self-managed/README.md").write_text("a customer fix\n")
            self.commit_all(root, "fix(self-managed): a change that would release anywhere else")

            on_default = self._run_auto(
                root, self.SELF_MANAGED_STACK_SERVICE, default_branch, default_branch
            )
            self.assertNotIn("would create", on_default)

            release_branch = "release-deploy/stacks/self-managed/v0.21"
            git(root, "switch", "-c", release_branch)
            on_branch = self._run_auto(
                root, self.SELF_MANAGED_STACK_SERVICE, release_branch, default_branch
            )
            self.assertIn("would create deploy/stacks/self-managed/v0.21.0", on_branch)

    def test_release_branch_skips_a_push_that_does_not_touch_the_stack(self):
        # Backporting CI, tooling, or anything outside the stack's own tree is
        # not a reason to publish a stack version.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "1.0.0", "release-deploy/stacks/self-managed/v1.0")
            git(root, "tag", "deploy/stacks/self-managed/v1.0.0")
            (root / "tools").mkdir(parents=True, exist_ok=True)
            (root / "tools" / "ci-helper").write_text("backported tooling\n")
            self.commit_all(root, "ci(self-managed): backport the release tooling")

            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                self.github_release.publish_release_branch_release(
                    root, self.SELF_MANAGED_STACK_SERVICE, dry_run=True, draft=False
                )
            self.assertIn("nothing changed under deploy/stacks/self-managed", output.getvalue())
            self.assertNotIn("would create", output.getvalue())

    def test_release_branch_skips_a_version_file_only_change(self):
        # The exact bootstrap case: a branch cut from a commit that predates the
        # VERSION file needs one commit to add it, and that commit ships nothing.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            stack_dir = root / "deploy/stacks/self-managed"
            stack_dir.mkdir(parents=True, exist_ok=True)
            (stack_dir / "helmfile.yaml").write_text("qualified content\n")
            self.commit_all(root, "chore: the qualified commit, with no VERSION file")
            git(root, "tag", "deploy/stacks/self-managed/v1.0.0")

            git(root, "switch", "-c", "release-deploy/stacks/self-managed/v1.0")
            (stack_dir / "VERSION").write_text("1.0.0\n")
            self.commit_all(root, "chore(self-managed): seed VERSION for the 1.0 train")

            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                self.github_release.publish_release_branch_release(
                    root, self.SELF_MANAGED_STACK_SERVICE, dry_run=True, draft=False
                )
            self.assertNotIn("would create", output.getvalue())

            # A real backport on top still releases, and it is the next patch.
            (stack_dir / "helmfile.yaml").write_text("qualified content plus a backported fix\n")
            self.commit_all(root, "fix(self-managed): backport the fix")
            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                self.github_release.publish_release_branch_release(
                    root, self.SELF_MANAGED_STACK_SERVICE, dry_run=True, draft=False
                )
            self.assertIn("would create deploy/stacks/self-managed/v1.0.1", output.getvalue())

    def test_stack_change_detection_fails_closed_when_the_diff_cannot_run(self):
        # A diff that does not run must not read as "the stack changed".
        # run(capture=True) folds stderr into stdout, so an unreadable tag would
        # come back as `fatal: bad revision ...`, parse as one changed path, and
        # publish a version off an error message.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "1.0.0", "release-deploy/stacks/self-managed/v1.0")

            with self.assertRaisesRegex(SystemExit, "cannot compare deploy/stacks/self-managed"):
                self.github_release.stack_content_changed_since(
                    root, self.SELF_MANAGED_STACK_SERVICE, "deploy/stacks/self-managed/v9.9.9"
                )

    def test_stack_change_detection_survives_a_synthetic_branch_root(self):
        # A release branch is rooted at a synthetic commit, so the train's tags
        # are not ancestors of HEAD. Comparison must be by tree, not by ancestry.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            stack_dir = root / "deploy/stacks/self-managed"
            stack_dir.mkdir(parents=True, exist_ok=True)
            (stack_dir / "helmfile.yaml").write_text("qualified content\n")
            (stack_dir / "VERSION").write_text("1.0.0\n")
            self.commit_all(root, "chore: qualified")
            git(root, "tag", "deploy/stacks/self-managed/v1.0.0")
            qualified = self.github_release.run(
                ["git", "rev-parse", "HEAD"], cwd=root, capture=True
            ).strip()

            # Graft: same tree, unrelated parentage.
            base = self.github_release.linear_release_branch_base(root, qualified)
            orphan = self.github_release.run(
                ["git", "commit-tree", self.github_release.commit_tree(root, qualified), "-m", "snapshot"],
                cwd=root,
                capture=True,
            ).strip()
            git(root, "switch", "-c", "release-deploy/stacks/self-managed/v1.0", orphan)

            self.assertFalse(
                self.github_release.tag_is_reachable(root, "deploy/stacks/self-managed/v1.0.0"),
                "the tag must not be an ancestor, or this test is not exercising the graft",
            )
            self.assertFalse(
                self.github_release.stack_content_changed_since(
                    root, self.SELF_MANAGED_STACK_SERVICE, "deploy/stacks/self-managed/v1.0.0"
                ),
                "identical trees across a graft must read as unchanged",
            )
            self.assertEqual(self.github_release.commit_tree(root, base), self.github_release.commit_tree(root, qualified))

    def test_release_branch_release_is_idempotent_at_head(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "0.21.0", "release-deploy/stacks/self-managed/v0.21")
            git(root, "tag", "deploy/stacks/self-managed/v0.21.0")

            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                self.github_release.publish_release_branch_release(
                    root, self.SELF_MANAGED_STACK_SERVICE, dry_run=True, draft=False
                )
            self.assertIn("already points at HEAD", output.getvalue())

    def test_release_branch_release_refuses_a_version_from_another_train(self):
        # The branch names the train and VERSION states it. Disagreeing means
        # the branch would publish a version for a train it does not hold.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "0.22.0", "release-deploy/stacks/self-managed/v0.21")
            with chdir(root), self.assertRaisesRegex(SystemExit, "does not match release branch train 0.21"):
                self.github_release.publish_release_branch_release(
                    root, self.SELF_MANAGED_STACK_SERVICE, dry_run=True, draft=False
                )

    def test_release_candidate_counts_up_from_the_published_rcs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "0.21.0", "kpathak/some-pull-request")
            git(root, "tag", "deploy/stacks/self-managed/v0.21.0-rc.0")
            git(root, "tag", "deploy/stacks/self-managed/v0.21.0-rc.1")
            metadata_path = root / "metadata.json"
            metadata_path.write_text(
                json.dumps({"version": 1, "services": [self.SELF_MANAGED_STACK_SERVICE]})
            )

            args = types.SimpleNamespace(
                metadata=str(metadata_path), service="nvcf-self-managed-stack", dry_run=True
            )
            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                self.github_release.release_candidate(args)

            text = output.getvalue()
            self.assertIn("would create deploy/stacks/self-managed/v0.21.0-rc.2", text)
            # Cutting an rc must not advance the stable line.
            self.assertNotIn("deploy/stacks/self-managed/v0.21.0 ", text)

    def test_release_candidate_requires_a_version_file_service(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "0.21.0", "")
            metadata_path = root / "metadata.json"
            metadata_path.write_text(
                json.dumps({"version": 1, "services": [self.NVCA_MAIN_SERVICE]})
            )

            args = types.SimpleNamespace(metadata=str(metadata_path), service="nvca", dry_run=True)
            with chdir(root), self.assertRaisesRegex(SystemExit, "requires release.version_file"):
                self.github_release.release_candidate(args)

    def test_branch_cut_dry_run_reports_release_branch_and_bump_pr(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "0.21.0", "")
            metadata_path = root / "metadata.json"
            metadata_path.write_text(
                json.dumps({"version": 1, "services": [self.SELF_MANAGED_STACK_SERVICE]})
            )

            args = types.SimpleNamespace(
                metadata=str(metadata_path),
                service="nvcf-self-managed-stack",
                ref="HEAD",
                target_branch="main",
                dry_run=True,
            )
            output = io.StringIO()
            with chdir(root), contextlib.redirect_stdout(output):
                self.github_release.branch_cut(args)

            text = output.getvalue()
            self.assertIn("release-deploy/stacks/self-managed/v0.21", text)
            self.assertIn("release-bump/nvcf-self-managed-stack/v0.21-to-v0.22", text)
            self.assertIn("deploy/stacks/self-managed/VERSION=0.21.0->0.22.0", text)

    def test_branch_cut_requires_a_release_branch_only_service(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "0.21.0", "")
            service = dict(self.SELF_MANAGED_STACK_SERVICE)
            del service["release_branch_only"]
            metadata_path = root / "metadata.json"
            metadata_path.write_text(json.dumps({"version": 1, "services": [service]}))

            args = types.SimpleNamespace(
                metadata=str(metadata_path),
                service="nvcf-self-managed-stack",
                ref="HEAD",
                target_branch="main",
                dry_run=True,
            )
            with chdir(root), self.assertRaisesRegex(SystemExit, "requires release.release_branch_only"):
                self.github_release.branch_cut(args)

    def test_version_file_must_hold_a_stable_version(self):
        # A prerelease here would be cut verbatim as the train's first release.
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "0.21.0-rc.1", "")
            with self.assertRaisesRegex(SystemExit, "does not contain a stable"):
                self.github_release.validate_version_file(root, self.SELF_MANAGED_STACK_SERVICE)

    def test_linear_release_branch_base_preserves_the_selected_tree(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "0.21.0", "")
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

    def test_linear_release_branch_base_keeps_an_already_linear_base(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self._seed_stack_release_branch(root, "0.21.0", "")
            base_sha = self.github_release.run(
                ["git", "rev-parse", "HEAD"], cwd=root, capture=True
            ).strip()

            self.assertEqual(self.github_release.linear_release_branch_base(root, base_sha), base_sha)

    # Image sources that live beside a chart of the same name. Each pair
    # releases independently: the chart from deploy/helm/<name>, the image
    # from infra/<name>. The image tag prefix is what the internal publishing
    # configuration dispatches on, so it is pinned here.
    INFRA_IMAGE_SOURCES = (
        ("openbao-image", "infra/openbao", "openbao", "deploy/helm/openbao"),
        ("cassandra-image", "infra/cassandra", "cassandra", "deploy/helm/cassandra"),
    )

    def test_infra_image_sources_release_independently_of_their_charts(self):
        root = SCRIPT_PATH.parents[2]
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        by_id = {service["id"]: service for service in metadata["services"]}
        self.assertEqual(len(by_id), len(metadata["services"]), "service ids must be unique")

        for image_id, image_path, chart_id, chart_path in self.INFRA_IMAGE_SOURCES:
            with self.subTest(image=image_id):
                image, chart = by_id[image_id], by_id[chart_id]
                self.assertEqual(image["path"], image_path)
                self.assertEqual(chart["path"], chart_path)
                self.assertNotEqual(image["service_name"], chart["service_name"])
                # Default tag format from the path: this exact prefix is what
                # the internal image lane is configured to dispatch on.
                self.assertEqual(self.github_release.tag_prefix(image, root), f"{image_path}/v")
                self.assertEqual(self.github_release.tag_prefix(chart, root), f"{chart_path}/v")

    def test_release_worthy_infra_commit_touches_the_image_stream_only(self):
        # A fix under infra/openbao must release infra/openbao/v* and leave the
        # deploy/helm/openbao stream untouched, and the reverse.
        image = {"id": "openbao-image", "path": "infra/openbao", "service_name": "nvcf-openbao"}
        chart = {"id": "openbao", "path": "deploy/helm/openbao", "service_name": "helm-nvcf-openbao-server"}
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_repo(root)
            for path in (image["path"], chart["path"]):
                (root / path).mkdir(parents=True)
                (root / path / "README.md").write_text(f"{path}\n")
            self.commit_all(root, "seed openbao chart and image")
            git(root, "tag", "infra/openbao/v1.3.1")
            git(root, "tag", "deploy/helm/openbao/v0.32.2")

            (root / image["path"] / "README.md").write_text("fix(openbao): bump grpc\n")
            self.commit_all(root, "fix(openbao): bump grpc")

            self.assertTrue(self.github_release.releases_a_version("fix(openbao): bump grpc"))
            with chdir(root), contextlib.redirect_stdout(io.StringIO()):
                self.assertFalse(self.github_release.only_generated_changes(root, image, []))
                self.assertTrue(self.github_release.only_generated_changes(root, chart, []))
                self.assertEqual(
                    self.github_release.latest_service_tag(image, root), "infra/openbao/v1.3.1"
                )

    # The subprojects that release from a maintenance branch instead of from
    # main, and the VERSION each one's next branch cut will open.
    RELEASE_BRANCH_STACKS = (
        ("nvcf-self-managed-stack", "deploy/stacks/self-managed"),
        ("nvcf-compute-plane-stack", "deploy/stacks/nvcf-compute-plane"),
        ("nvcf-observability-stack", "deploy/stacks/observability"),
    )

    def test_the_stacks_release_from_a_branch_and_nothing_else_does(self):
        root = SCRIPT_PATH.parents[2]
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        by_id = {service["id"]: service for service in metadata["services"]}
        expected = {stack_id for stack_id, _path in self.RELEASE_BRANCH_STACKS}

        declared = {s["id"] for s in metadata["services"] if s.get("release_branch_only")}
        self.assertEqual(declared, expected)

        # version_file and release_branch_only are one model, not two settings.
        # A version_file without the branch rule releases from main off a file
        # nothing advances; the branch rule without a file has no version to cut.
        for service in metadata["services"]:
            with self.subTest(service=service["id"]):
                self.assertEqual(
                    bool(service.get("version_file")), bool(service.get("release_branch_only"))
                )
                # The retired spelling. A `dev_prerelease` entry would be read by
                # nothing and the subproject would silently stop releasing.
                self.assertNotIn("dev_prerelease", service)

        for stack_id, path in self.RELEASE_BRANCH_STACKS:
            with self.subTest(service=stack_id):
                service = by_id[stack_id]
                self.assertEqual(service["path"], path)
                self.assertEqual(service["version_file"], "VERSION")
                # A floor is for a semantic-release subproject. Leaving one here
                # would state a second, contradictory source for the version.
                self.assertNotIn("initial_version", service)
                version = self.github_release.validate_version_file(root, service)
                self.assertTrue(
                    self.github_release.service_release_branch(service, version, root).startswith("release-")
                )
                self.assertTrue(
                    self.github_release.service_version_bump_branch(service, version).startswith("release-bump/")
                )

    def test_nvca_still_releases_from_main(self):
        # nvca migrated to semantic-release alongside the stacks and stays there.
        root = SCRIPT_PATH.parents[2]
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        nvca = {service["id"]: service for service in metadata["services"]}["nvca"]

        self.assertEqual(nvca.get("initial_version"), "3.3.0")
        self.assertNotIn("version_file", nvca)
        self.assertNotIn("release_branch_only", nvca)
        self.assertFalse((root / nvca["path"] / "VERSION").exists())

    NVCA_SERVICE = {
        "id": "nvca",
        "path": "src/compute-plane-services/nvca",
        "service_name": "nvca",
        "legacy_tag_prefix": "nvca-v",
        "initial_version": "3.3.0",
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
        self.seed_nvca_service(root)
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
            self.seed_nvca_service(root)
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

    def stack_release_metadata(self, asset_name="inventory.json"):
        return {
            "version": 1,
            "services": [
                {
                    "id": "nvcf-self-managed-stack",
                    "path": "deploy/stacks/self-managed",
                    "service_name": "nvcf-self-managed-stack",
                    "tag_format": "deploy/stacks/self-managed/v${version}",
                    "resolved_inventory_asset": asset_name,
                }
            ],
        }

    def test_release_metadata_publishes_one_inventory_per_stack(self):
        metadata_path = SCRIPT_PATH.with_name("github-release-subprojects.json")
        metadata = json.loads(metadata_path.read_text())
        expected = {
            "deploy/stacks/self-managed/v1.2.3": "nvcf-self-managed-stack-inventory.json",
            "deploy/stacks/nvcf-compute-plane/v1.2.3": "nvcf-compute-plane-stack-inventory.json",
            "deploy/stacks/observability/v1.2.3": "nvcf-observability-stack-inventory.json",
        }
        for tag, asset_name in expected.items():
            with self.subTest(tag=tag):
                service = self.github_release.release_asset_service(metadata, tag, SCRIPT_PATH.parents[2])
                self.assertIsNotNone(service)
                self.assertEqual(service["resolved_inventory_asset"], asset_name)

    STACK_INVENTORY_PUBLISHERS = {
        "self-managed": ("nvcf-self-managed-stack", "nvcf-self-managed-stack-inventory.json"),
        "nvcf-compute-plane": ("nvcf-compute-plane-stack", "nvcf-compute-plane-stack-inventory.json"),
        "observability": ("nvcf-observability-stack", "nvcf-observability-stack-inventory.json"),
    }

    def test_release_branch_tags_keep_the_same_inventory_publisher(self):
        # Inventory attachment is keyed by the tag alone. A tag cut from a
        # release train branch must resolve exactly as one cut from main did
        # before the stacks moved to release branching.
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        root = SCRIPT_PATH.parents[2]
        for stack, (service_id, asset_name) in self.STACK_INVENTORY_PUBLISHERS.items():
            tag = f"deploy/stacks/{stack}/v1.1.0"
            with self.subTest(tag=tag):
                service = self.github_release.release_asset_service(metadata, tag, root)
                self.assertIsNotNone(service)
                self.assertEqual(service["id"], service_id)
                self.assertEqual(service["resolved_inventory_asset"], asset_name)
                self.assertEqual(self.github_release.version_from_tag(service, tag, root), "1.1.0")

    def test_compute_plane_legacy_prefix_tag_still_resolves_its_inventory_publisher(self):
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        root = SCRIPT_PATH.parents[2]
        tag = "nvcf-compute-plane-stack-v0.2.0"
        service = self.github_release.release_asset_service(metadata, tag, root)
        self.assertIsNotNone(service)
        self.assertEqual(service["id"], "nvcf-compute-plane-stack")
        self.assertEqual(service["resolved_inventory_asset"], "nvcf-compute-plane-stack-inventory.json")
        self.assertEqual(self.github_release.version_from_tag(service, tag, root), "0.2.0")

    def test_release_train_branch_name_round_trips_for_every_stack(self):
        metadata = json.loads(SCRIPT_PATH.with_name("github-release-subprojects.json").read_text())
        root = SCRIPT_PATH.parents[2]
        for stack, (service_id, _asset_name) in self.STACK_INVENTORY_PUBLISHERS.items():
            with self.subTest(stack=stack):
                service = self.github_release.find_service(metadata, service_id)
                branch = self.github_release.service_release_branch(service, "1.1.0", root)
                self.assertEqual(branch, f"release-deploy/stacks/{stack}/v1.1")
                self.assertEqual(self.github_release.release_branch_train(service, branch, root), "1.1")
                self.assertEqual(
                    self.github_release.tag_for_version(service, "1.1.0", root),
                    f"deploy/stacks/{stack}/v1.1.0",
                )
                self.assertTrue(service.get("release_branch_only"))

    def chart_release_metadata(self):
        """Return minimal release metadata for the chart publication tests."""
        return {
            "version": 1,
            "services": [
                {
                    "id": "nats-auth-callout-helm",
                    "path": "deploy/helm/nats-auth-callout",
                    "service_name": "helm-nvcf-nats-auth-callout-service",
                }
            ],
        }

    def test_chart_release_tag_resolves_registered_publisher(self):
        """Chart tags must resolve exactly one registered Helm publisher."""
        service = self.github_release.release_chart_service(
            self.chart_release_metadata(),
            "deploy/helm/nats-auth-callout/v1.2.0",
            Path("."),
        )
        self.assertEqual(service["id"], "nats-auth-callout-helm")
        self.assertIsNone(
            self.github_release.release_chart_service(
                self.chart_release_metadata(),
                "src/control-plane-services/nats-auth-callout/v0.8.3",
                Path("."),
            )
        )
        with self.assertRaisesRegex(SystemExit, "no matching service"):
            self.github_release.release_chart_service(
                self.chart_release_metadata(),
                "deploy/helm/unregistered/v1.0.0",
                Path("."),
            )

    def test_chart_release_directory_ignores_vendored_dependency(self):
        """Chart discovery must not treat a vendored dependency as the owning chart."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            chart_dir = root / "deploy/helm/nats-auth-callout"
            dependency = chart_dir / "charts/dependency"
            dependency.mkdir(parents=True)
            (chart_dir / "Chart.yaml").write_text(
                "name: helm-nvcf-nats-auth-callout-service\n"
            )
            (dependency / "Chart.yaml").write_text("name: dependency\n")

            self.assertEqual(
                self.github_release.release_chart_directory(
                    root, self.chart_release_metadata()["services"][0]
                ),
                chart_dir,
            )

    def test_registered_chart_release_metadata_matches_chart_sources(self):
        """Every registered Helm publisher must resolve to a valid source chart."""
        root = SCRIPT_PATH.parents[2]
        metadata = json.loads(
            SCRIPT_PATH.with_name("github-release-subprojects.json").read_text()
        )
        charts = [
            service
            for service in metadata["services"]
            if service["path"].startswith("deploy/helm/")
        ]
        self.assertGreater(len(charts), 0)
        for service in charts:
            with self.subTest(service=service["id"]):
                self.assertTrue(
                    self.github_release.release_chart_directory(root, service).is_dir()
                )

    def test_chart_release_refuses_metadata_name_mismatch(self):
        """Publishing must reject chart names that disagree with release metadata."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            chart_dir = root / "deploy/helm/nats-auth-callout"
            chart_dir.mkdir(parents=True)
            (chart_dir / "Chart.yaml").write_text("name: another-chart\n")

            with self.assertRaisesRegex(SystemExit, "does not match"):
                self.github_release.release_chart_directory(
                    root, self.chart_release_metadata()["services"][0]
                )

    def test_chart_dependencies_require_lock_file(self):
        """Dependency-bearing charts must pin resolution with a lock file."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            chart_dir = root / "chart"
            chart_dir.mkdir()
            (chart_dir / "Chart.yaml").write_text(
                "name: example\n"
                "dependencies:\n"
                "  - name: dependency\n"
                "    version: 1.0.0\n"
                "    repository: https://example.invalid/charts\n"
            )

            with self.assertRaisesRegex(SystemExit, "dependencies require Chart.lock"):
                self.github_release.package_release_chart(
                    chart_dir, "1.2.0", root / "output"
                )

    def test_indented_chart_dependencies_require_lock_file(self):
        """An indented dependencies key must still activate the lock-file guard."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            chart_dir = root / "chart"
            chart_dir.mkdir()
            (chart_dir / "Chart.yaml").write_text(
                "  name: example\n"
                "  dependencies:\n"
                "    - name: dependency\n"
                "      version: 1.0.0\n"
                "      repository: https://example.invalid/charts\n"
            )

            with self.assertRaisesRegex(SystemExit, "dependencies require Chart.lock"):
                self.github_release.package_release_chart(
                    chart_dir, "1.2.0", root / "output"
                )

    def test_chart_is_published_before_github_release(self):
        """The immutable chart must exist before its GitHub release becomes visible."""
        root = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: shutil.rmtree(root))
        tag = "deploy/helm/nats-auth-callout/v1.2.0"
        calls = []
        self.github_release.repo_root = lambda: root
        self.github_release.load_metadata = lambda *_args: self.chart_release_metadata()
        self.github_release.github_release_mode = lambda: (True, False)
        self.github_release.publish_release_chart = (
            lambda _root, service, version, dry_run: calls.append(
                ("chart", service["id"], version, dry_run)
            )
        )
        self.github_release.create_release = (
            lambda *_args, **_kwargs: calls.append(("release",))
        )

        with contextlib.redirect_stdout(io.StringIO()):
            self.github_release.tag_release(
                types.SimpleNamespace(tag=tag, metadata="metadata.json")
            )

        self.assertEqual(
            calls,
            [("chart", "nats-auth-callout-helm", "1.2.0", False), ("release",)],
        )

    def test_chart_publish_failure_prevents_github_release(self):
        """A chart publication failure must prevent the GitHub release event."""
        root = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: shutil.rmtree(root))
        self.github_release.repo_root = lambda: root
        self.github_release.load_metadata = lambda *_args: self.chart_release_metadata()
        self.github_release.github_release_mode = lambda: (True, False)
        self.github_release.publish_release_chart = lambda *_args: (_ for _ in ()).throw(
            RuntimeError("push failed")
        )
        released = []
        self.github_release.create_release = lambda *_args, **_kwargs: released.append(True)

        with self.assertRaisesRegex(RuntimeError, "push failed"):
            with contextlib.redirect_stdout(io.StringIO()):
                self.github_release.tag_release(
                    types.SimpleNamespace(
                        tag="deploy/helm/nats-auth-callout/v1.2.0",
                        metadata="metadata.json",
                    )
                )
        self.assertEqual(released, [])

    def test_missing_chart_is_pushed_with_exact_tag_version(self):
        """A missing chart must be pushed with the version encoded in its tag."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            chart_dir = root / "chart"
            chart_dir.mkdir()
            package = root / "chart-1.2.0.tgz"
            package.write_bytes(b"package")
            calls = []
            self.github_release.release_chart_directory = lambda *_args: chart_dir
            self.github_release.helm_registry_settings = lambda: (
                "nvcr.io/example/ncp-dev",
                "secret",
            )
            self.github_release.package_release_chart = lambda *_args: package
            self.github_release.helm_registry_login = (
                lambda registry, _key: calls.append(("login", registry))
            )
            self.github_release.pull_release_chart = (
                lambda *_args: (1, "manifest unknown: not found", [])
            )
            self.github_release.run = lambda args, **_kwargs: calls.append(tuple(args))

            with contextlib.redirect_stdout(io.StringIO()):
                self.github_release.publish_release_chart(
                    root, self.chart_release_metadata()["services"][0], "1.2.0", False
                )

            self.assertIn(("login", "nvcr.io/example/ncp-dev"), calls)
            self.assertIn(
                ("helm", "push", str(package), "oci://nvcr.io/example/ncp-dev"),
                calls,
            )

    def test_chart_publish_uses_replayed_tag_source(self):
        """Manual replay must package chart content from the selected tag worktree."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "current"
            replay_root = Path(tmp) / "tagged"
            chart_dir = replay_root / "deploy/helm/nats-auth-callout"
            chart_dir.mkdir(parents=True)
            selected_roots = []

            def select_chart(selected_root, _service):
                """Record the source root chosen by the publisher."""
                selected_roots.append(selected_root)
                return chart_dir

            self.github_release.release_chart_directory = select_chart
            with mock.patch.dict(
                os.environ,
                {"NVCF_RELEASE_SOURCE_ROOT": str(replay_root)},
            ), contextlib.redirect_stdout(io.StringIO()):
                self.github_release.publish_release_chart(
                    root,
                    self.chart_release_metadata()["services"][0],
                    "1.2.0",
                    True,
                )

            self.assertEqual(selected_roots, [replay_root])

    def test_only_registry_missing_signals_allow_a_chart_push(self):
        """Only explicit missing-artifact responses may permit a chart push."""
        self.assertTrue(self.github_release.missing_helm_chart_output("manifest unknown"))
        self.assertTrue(self.github_release.missing_helm_chart_output("status code: 404"))
        self.assertFalse(
            self.github_release.missing_helm_chart_output("credentials file not found")
        )

    def test_existing_chart_is_only_accepted_when_content_matches(self):
        """An existing immutable version is reusable only when its content matches."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            chart_dir = root / "chart"
            chart_dir.mkdir()
            local = root / "local.tgz"
            remote = root / "remote.tgz"
            local.write_bytes(b"local")
            remote.write_bytes(b"remote")
            self.github_release.release_chart_directory = lambda *_args: chart_dir
            self.github_release.helm_registry_settings = lambda: ("nvcr.io/example", "secret")
            self.github_release.package_release_chart = lambda *_args: local
            self.github_release.helm_registry_login = lambda *_args: None
            self.github_release.pull_release_chart = lambda *_args: (0, "", [remote])
            self.github_release.helm_archive_signature = lambda package: package.name

            with self.assertRaisesRegex(SystemExit, "different content"):
                self.github_release.publish_release_chart(
                    root, self.chart_release_metadata()["services"][0], "1.2.0", False
                )

            self.github_release.helm_archive_signature = lambda _package: "same"
            calls = []
            self.github_release.run = lambda args, **_kwargs: calls.append(args)
            with contextlib.redirect_stdout(io.StringIO()):
                self.github_release.publish_release_chart(
                    root, self.chart_release_metadata()["services"][0], "1.2.0", False
                )
            self.assertEqual(calls, [])

    def test_registry_error_does_not_get_mistaken_for_missing_chart(self):
        """Registry failures must not be treated as proof that a chart is absent."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            chart_dir = root / "chart"
            chart_dir.mkdir()
            package = root / "local.tgz"
            package.write_bytes(b"local")
            self.github_release.release_chart_directory = lambda *_args: chart_dir
            self.github_release.helm_registry_settings = lambda: ("nvcr.io/example", "secret")
            self.github_release.package_release_chart = lambda *_args: package
            self.github_release.helm_registry_login = lambda *_args: None
            self.github_release.pull_release_chart = lambda *_args: (
                1,
                "unauthorized: authentication required",
                [],
            )

            with self.assertRaisesRegex(SystemExit, "could not determine"):
                with contextlib.redirect_stdout(io.StringIO()):
                    self.github_release.publish_release_chart(
                        root,
                        self.chart_release_metadata()["services"][0],
                        "1.2.0",
                        False,
                    )

    def test_publish_release_makes_the_exact_draft_public(self):
        calls = []
        self.github_release.run = lambda args, **_kwargs: calls.append(args)

        with contextlib.redirect_stdout(io.StringIO()):
            self.github_release.publish_release(
                "deploy/stacks/self-managed/v1.2.3", dry_run=False
            )

        self.assertEqual(
            calls,
            [
                [
                    "gh",
                    "release",
                    "edit",
                    "deploy/stacks/self-managed/v1.2.3",
                    "--draft=false",
                ]
            ],
        )

    def test_stack_tag_generates_inventory_before_release_and_uploads_after(self):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: shutil.rmtree(root))
        tag = "deploy/stacks/self-managed/v1.2.3"
        commit = "a" * 40
        metadata = self.stack_release_metadata("nvcf-self-managed-stack-inventory.json")
        calls = []
        self.github_release.repo_root = lambda: root
        self.github_release.load_metadata = lambda *_args: metadata
        self.github_release.github_release_mode = lambda: (True, False)
        self.github_release.tag_sha = lambda *_args: commit
        self.github_release.generate_resolved_stack_inventory = (
            lambda _root, _tag, _version, _commit, path: calls.append(("generate", Path(path).name))
        )
        self.github_release.create_release = (
            lambda _tag, _title, _notes, draft, dry_run: calls.append(("release", draft, dry_run)) or True
        )
        self.github_release.publish_resolved_stack_inventory = (
            lambda _tag, path: calls.append(("upload", Path(path).name))
        )
        self.github_release.publish_release = (
            lambda _tag, dry_run: calls.append(("publish", dry_run))
        )

        with contextlib.redirect_stdout(io.StringIO()):
            self.github_release.tag_release(types.SimpleNamespace(tag=tag, metadata="metadata.json"))

        self.assertEqual(
            calls,
            [
                ("generate", "nvcf-self-managed-stack-inventory.json"),
                ("release", True, False),
                ("upload", "nvcf-self-managed-stack-inventory.json"),
                ("publish", False),
            ],
        )

    def test_stack_tag_preserves_explicit_draft_after_inventory_upload(self):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: shutil.rmtree(root))
        tag = "deploy/stacks/self-managed/v1.2.3"
        calls = []
        self.github_release.repo_root = lambda: root
        self.github_release.load_metadata = lambda *_args: self.stack_release_metadata()
        self.github_release.github_release_mode = lambda: (True, False)
        self.github_release.bool_env = lambda *_args: True
        self.github_release.tag_sha = lambda *_args: "a" * 40
        self.github_release.generate_resolved_stack_inventory = lambda *_args: calls.append("generate")
        self.github_release.create_release = (
            lambda _tag, _title, _notes, draft, dry_run: calls.append(("release", draft, dry_run)) or True
        )
        self.github_release.publish_resolved_stack_inventory = lambda *_args: calls.append("upload")
        self.github_release.publish_release = lambda *_args: calls.append("publish")

        with contextlib.redirect_stdout(io.StringIO()):
            self.github_release.tag_release(types.SimpleNamespace(tag=tag, metadata="metadata.json"))

        self.assertEqual(calls, ["generate", ("release", True, False), "upload"])

    def test_stack_inventory_generation_failure_prevents_partial_release(self):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: shutil.rmtree(root))
        tag = "deploy/stacks/self-managed/v1.2.3"
        metadata = self.stack_release_metadata()
        released = []
        self.github_release.repo_root = lambda: root
        self.github_release.load_metadata = lambda *_args: metadata
        self.github_release.github_release_mode = lambda: (True, False)
        self.github_release.tag_sha = lambda *_args: "b" * 40
        self.github_release.generate_resolved_stack_inventory = lambda *_args: (_ for _ in ()).throw(
            RuntimeError("render failed")
        )
        self.github_release.create_release = lambda *_args, **_kwargs: released.append(True)

        with self.assertRaisesRegex(RuntimeError, "render failed"):
            with contextlib.redirect_stdout(io.StringIO()):
                self.github_release.tag_release(types.SimpleNamespace(tag=tag, metadata="metadata.json"))
        self.assertEqual(released, [])

    def test_stack_inventory_upload_failure_is_not_suppressed(self):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(lambda: shutil.rmtree(root))
        tag = "deploy/stacks/self-managed/v1.2.3"
        metadata = self.stack_release_metadata()
        self.github_release.repo_root = lambda: root
        self.github_release.load_metadata = lambda *_args: metadata
        self.github_release.github_release_mode = lambda: (True, False)
        self.github_release.tag_sha = lambda *_args: "c" * 40
        for created in (False, True):
            with self.subTest(created=created):
                calls = []
                self.github_release.generate_resolved_stack_inventory = lambda *_args: calls.append("generate")

                def create(*_args, **_kwargs):
                    calls.append("release")
                    return created

                self.github_release.create_release = create
                self.github_release.publish_resolved_stack_inventory = lambda *_args: (
                    _ for _ in ()
                ).throw(RuntimeError("upload failed"))
                self.github_release.delete_release_after_inventory_failure = lambda *_args: calls.append(
                    "delete"
                )

                with self.assertRaisesRegex(RuntimeError, "upload failed"):
                    with contextlib.redirect_stdout(io.StringIO()):
                        self.github_release.tag_release(
                            types.SimpleNamespace(tag=tag, metadata="metadata.json")
                        )
                expected = ["generate", "release"]
                if created:
                    expected.append("delete")
                self.assertEqual(calls, expected)

    def test_resolved_stack_inventory_identity_must_match_release(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "inventory.json"
            path.write_text(
                json.dumps(
                    {
                        "schema_version": 1,
                        "source": {
                            "version": "1.2.3",
                            "tag": "deploy/stacks/self-managed/v1.2.3",
                            "commit": "d" * 40,
                        },
                        "releases": [{}],
                        "artifacts": [{}],
                    }
                )
            )
            self.github_release.validate_resolved_stack_inventory(
                path, "deploy/stacks/self-managed/v1.2.3", "1.2.3", "d" * 40
            )
            with self.assertRaisesRegex(SystemExit, "inventory source"):
                self.github_release.validate_resolved_stack_inventory(
                    path, "deploy/stacks/self-managed/v1.2.3", "1.2.3", "e" * 40
                )

    def test_existing_release_inventory_is_left_unchanged(self):
        with tempfile.TemporaryDirectory() as tmp:
            asset = Path(tmp) / "inventory.json"
            asset.write_text("same\n")

            def fake_run(args, **_kwargs):
                if args[:3] == ["gh", "release", "view"]:
                    return "inventory.json\n"
                raise AssertionError(f"unexpected call: {args}")

            self.github_release.run = fake_run
            with contextlib.redirect_stdout(io.StringIO()):
                self.github_release.publish_resolved_stack_inventory("stack/v1.2.3", asset)

    def test_missing_release_inventory_is_uploaded(self):
        with tempfile.TemporaryDirectory() as tmp:
            asset = Path(tmp) / "inventory.json"
            asset.write_text("inventory\n")
            calls = []

            def fake_run(args, **_kwargs):
                calls.append(args)
                if args[:3] == ["gh", "release", "view"]:
                    return ""
                if args[:3] == ["gh", "release", "upload"]:
                    return ""
                raise AssertionError(f"unexpected call: {args}")

            self.github_release.run = fake_run
            with contextlib.redirect_stdout(io.StringIO()):
                self.github_release.publish_resolved_stack_inventory("stack/v1.2.3", asset)

            self.assertEqual(
                calls,
                [
                    [
                        "gh",
                        "release",
                        "view",
                        "stack/v1.2.3",
                        "--json",
                        "assets",
                        "--jq",
                        ".assets[].name",
                    ],
                    ["gh", "release", "upload", "stack/v1.2.3", str(asset)],
                ],
            )

    def test_incomplete_release_cleanup_uses_exact_tag(self):
        calls = []
        self.github_release.run = lambda args, **_kwargs: calls.append(args)
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertTrue(
                self.github_release.delete_release_after_inventory_failure(
                    "deploy/stacks/self-managed/v1.2.3"
                )
            )
        self.assertEqual(
            calls,
            [["gh", "release", "delete", "deploy/stacks/self-managed/v1.2.3", "--yes"]],
        )

    def test_stack_tag_workflow_installs_pinned_inventory_tools(self):
        """The release workflow must install pinned tools for each artifact type."""
        workflow = (SCRIPT_PATH.parents[2] / ".github/workflows/release-tags.yml").read_text()
        workflow_env = workflow.split("\njobs:\n", 1)[0]
        self.assertIn("actions/setup-go@v5", workflow)
        for pin in (
            'HELM_VERSION: "3.21.4"',
            'HELM_SHA256: "61f88ab166748cb19604d7884cb100ae9ccb13804ddeb98e08af167eacbb6a14"',
            'HELMFILE_VERSION: "1.1.9"',
            'HELMFILE_SHA256: "ee71196bb12460905b8cbe0ef67b28db51ef681b777cc212d8c0956475b51905"',
        ):
            self.assertIn(pin, workflow_env)
            self.assertEqual(workflow.count(pin), 1)
        self.assertIn(
            "NVCF_RELEASE_HELM_REGISTRY: ${{ secrets.NCP_DEV_REGISTRY }}",
            workflow,
        )
        self.assertEqual(workflow.count("Install inventory rendering tools"), 1)
        self.assertIn("Install Helm release tool", workflow)
        self.assertIn("Install Helmfile inventory tool", workflow)
        self.assertIn("inputs.release_tag || github.ref_name", workflow)
        self.assertIn("Prepare existing chart tag for replay", workflow)
        self.assertIn("NVCF_RELEASE_SOURCE_ROOT=", workflow)
        self.assertIn("HELM_REGISTRY_CONFIG: ${{ runner.temp }}/helm-registry-config.json", workflow)
        self.assertIn('release_tag must be a deploy/helm/*/v* tag', workflow)
        self.assertLess(
            workflow.index("Install Helm release tool"),
            workflow.index("Validate tag and create release notes"),
        )

    def test_stack_inventory_preflight_renders_without_release_publication(self):
        workflow = (SCRIPT_PATH.parents[2] / ".github/workflows/release-tags.yml").read_text()
        self.assertIn("inventory_tag:", workflow)
        self.assertIn("name: stack inventory preflight", workflow)
        self.assertIn("inputs.inventory_tag != ''", workflow)
        self.assertIn('token: ${{ github.token }}', workflow)
        self.assertIn("Render tagged inventory without publishing", workflow)
        self.assertIn("--generate-stack-inventory", workflow)
        self.assertIn("deploy/stacks/nvcf-compute-plane/v*", workflow)
        self.assertIn("deploy/stacks/observability/v*", workflow)
        preflight = workflow.index("inventory-preflight:")
        tag_release = workflow.index("tag-release-notes:")
        preflight_workflow = workflow[preflight:tag_release]
        self.assertIn("--inventory-config", preflight_workflow)
        self.assertIn("grep -q '^states:'", preflight_workflow)
        self.assertIn("predates its per-stack inventory states", preflight_workflow)
        self.assertIn("--allow-unavailable-source-charts", preflight_workflow)
        self.assertIn("actions/upload-artifact@v4", preflight_workflow)
        self.assertIn("if-no-files-found: error", preflight_workflow)
        self.assertNotIn("github-release tag", preflight_workflow)

    def test_release_replay_requires_an_exact_tag_ref(self):
        """A tag-shaped branch must not satisfy manual replay validation."""
        workflow = (SCRIPT_PATH.parents[2] / ".github/workflows/release-tags.yml").read_text()
        tag_check = 'git show-ref --verify --quiet "refs/tags/${RELEASE_TAG}"'
        exact_checkout = '"refs/tags/${RELEASE_TAG}"'
        replay_step = workflow.split("- name: Prepare existing chart tag for replay", 1)[1]
        replay_step = replay_step.split("- uses: actions/setup-go@v5", 1)[0]

        self.assertIn(tag_check, replay_step)
        self.assertGreaterEqual(replay_step.count(exact_checkout), 2)
        self.assertLess(replay_step.index(tag_check), replay_step.rindex(exact_checkout))

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            release_tag = "deploy/helm/example/v1.2.3"
            self.init_repo(root)
            git(root, "commit", "--allow-empty", "-m", "seed")
            git(root, "branch", release_tag)
            verify_tag = [
                "git",
                "show-ref",
                "--verify",
                "--quiet",
                f"refs/tags/{release_tag}",
            ]

            branch_only = subprocess.run(verify_tag, cwd=root, check=False)
            self.assertNotEqual(branch_only.returncode, 0)
            git(root, "tag", release_tag)
            tagged = subprocess.run(verify_tag, cwd=root, check=False)
            self.assertEqual(tagged.returncode, 0)

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
                    root, self.NVCA_SERVICE, version, dry_run=False, draft=False, reason="test"
                )
            return comments

    def test_publish_tag_comments_on_a_stable_release(self):
        comments = self.publish_and_capture_comments("3.2.1")
        self.assertEqual(
            comments,
            [("src/compute-plane-services/nvca/v3.2.1", "3.2.1", "src/compute-plane-services/nvca/v3.2.0")],
            "the range must be bounded by the newest tag on this branch, not the highest tag overall",
        )

    def test_publish_tag_stays_quiet_for_a_prerelease(self):
        # A prerelease is an internal checkpoint, not something to announce on
        # a pull request. Nothing publishes one automatically now that the
        # dev-prerelease model is retired, but a hand-cut rc still reaches
        # publish_tag_for_version through the release-candidate path.
        self.assertEqual(self.publish_and_capture_comments("3.4.0-rc.1"), [])


class SuccessCommentTest(unittest.TestCase):
    """The comment semantic-release posts must name the tag it actually created.

    semantic-release-monorepo wraps the `success` step with a transform that
    rewrites `nextRelease.version` to `<package.json name>-v<version>`, ignoring
    the `tagFormat` configured here. byoo-otel-collector is the one service whose
    tag_format injects an upstream prefix, so the default comment advertises
    `byoo-otel-collector-v0.2.5` for a tag that is really
    `.../v0.160.0-nv-0.2.5`. The transform leaves `nextRelease.gitTag` alone.
    """

    TAG_FORMAT = "src/compute-plane-services/byoo-otel-collector/v0.160.0-nv-${version}"

    def setUp(self):
        self.github_release = load_github_release()

    def github_plugin_options(self):
        service_dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, service_dir, ignore_errors=True)
        with mock.patch.dict(os.environ, {"GITHUB_REPOSITORY": "NVIDIA/nvcf"}):
            self.github_release.write_semantic_release_files(
                service_dir,
                "byoo-otel-collector",
                self.TAG_FORMAT,
                publish=True,
                draft=False,
            )
        config = json.loads((service_dir / ".releaserc.json").read_text())
        for plugin in config["plugins"]:
            if isinstance(plugin, list) and plugin[0] == "@semantic-release/github":
                return plugin[1]
        self.fail("@semantic-release/github is not configured")

    def test_success_comment_names_the_published_git_tag(self):
        self.assertIn("${nextRelease.gitTag}", self.github_plugin_options()["successComment"])

    def test_success_comment_does_not_name_the_rewritten_version(self):
        self.assertNotIn("${nextRelease.version}", self.github_plugin_options()["successComment"])

    def test_success_comment_links_the_release_it_announces(self):
        self.assertIn(
            "https://github.com/NVIDIA/nvcf/releases/tag/${nextRelease.gitTag}",
            self.github_plugin_options()["successComment"],
        )

    def release_notes_writer_opts(self):
        service_dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, service_dir, ignore_errors=True)
        with mock.patch.dict(os.environ, {"GITHUB_REPOSITORY": "NVIDIA/nvcf"}):
            self.github_release.write_semantic_release_files(
                service_dir,
                "byoo-otel-collector",
                self.TAG_FORMAT,
                publish=True,
                draft=False,
            )
        config = json.loads((service_dir / ".releaserc.json").read_text())
        for plugin in config["plugins"]:
            if isinstance(plugin, list) and plugin[0] == "@semantic-release/release-notes-generator":
                return plugin[1]["writerOpts"]
        self.fail("@semantic-release/release-notes-generator has no writerOpts")

    def test_release_notes_heading_names_the_published_git_tag(self):
        self.assertIn("{{currentTag}}", self.release_notes_writer_opts()["headerPartial"])

    def test_release_notes_heading_does_not_name_the_rewritten_version(self):
        # `version` is what semantic-release-monorepo rewrites, and the upstream
        # template references it twice: once linked, once bare as `{{~version}}`
        # with a whitespace-control tilde. Checking only `{{version}}` would miss
        # the second and leave the unlinked heading still wrong.
        partial = self.release_notes_writer_opts()["headerPartial"]
        for token in ("{{version}}", "{{~version}}"):
            self.assertNotIn(token, partial)

    def test_release_notes_heading_still_links_the_compare_range(self):
        partial = self.release_notes_writer_opts()["headerPartial"]
        self.assertIn("/compare/{{previousTag}}...{{currentTag}}", partial)

    def test_success_comment_wording_matches_the_kind_of_thing_it_is_posted_on(self):
        # Pins both arms and their order. Asserting only that `issue.pull_request`
        # appears would still pass with the arms swapped, which would tell every
        # reader the opposite of the truth. Rendering the template for real needs
        # a Lodash engine, and this suite runs on python3 alone.
        self.assertIn(
            "${issue.pull_request ? 'PR is included' : 'issue has been resolved'}",
            self.github_plugin_options()["successComment"],
        )


class MultiPathReleaseTest(unittest.TestCase):
    """One release version covering a service directory and a path outside it."""

    setUp = GithubReleaseTest.setUp
    init_repo = GithubReleaseTest.init_repo
    seed_nvca_service = GithubReleaseTest.seed_nvca_service
    commit_all = GithubReleaseTest.commit_all

    CHART_PATH = "deploy/helm/nvca-operator/nvca-operator"

    def nvca_metadata(self, owns_paths=True):
        service = {
            "id": "nvca",
            "path": "src/compute-plane-services/nvca",
            "service_name": "nvca",
        }
        if owns_paths:
            service["owns_paths"] = [{"path": self.CHART_PATH, "packaged": True}]
        return service

    def seed_chart(self, root):
        chart_dir = root / self.CHART_PATH
        chart_dir.mkdir(parents=True, exist_ok=True)
        (chart_dir / "Chart.yaml").write_text("name: helm-nvca-operator\n")

    def init_multi_path_repo(self, root, remote=None):
        self.init_repo(root)
        self.seed_nvca_service(root)
        self.seed_chart(root)
        self.commit_all(root, "seed")
        git(root, "tag", "src/compute-plane-services/nvca/v3.12.1")
        if remote is not None:
            subprocess.run(
                ["git", "init", "--bare", "--initial-branch=main", str(remote)],
                check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            git(root, "remote", "add", "origin", str(remote))
            git(root, "push", "origin", "HEAD")
        self.github_release.create_release = lambda tag, title, notes, draft, dry_run: None

    def touch(self, root, relative, message):
        target = root / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(target.read_text() + "x\n" if target.exists() else "x\n")
        self.commit_all(root, message)

    def test_release_level_follows_the_configured_rules(self):
        level = self.github_release.release_level
        self.assertEqual(level("feat(nvca): add a thing"), "minor")
        self.assertEqual(level("fix(nvca): correct a thing"), "patch")
        self.assertEqual(level("perf(nvca): speed a thing"), "patch")
        self.assertIsNone(level("chore(nvca): tidy"))
        self.assertIsNone(level("docs: explain"))
        self.assertEqual(level("fix(nvca)!: break a thing"), "major")
        self.assertEqual(level("feat!: break a thing"), "major")
        # Not a Conventional Commit, so it releases nothing, the same as it
        # would inside a service directory.
        self.assertIsNone(level("Merge branch 'main' into topic"))

    def test_releases_a_version_still_agrees_with_release_level(self):
        for subject in ("feat: a", "fix: b", "chore: c", "docs!: d", "not conventional"):
            self.assertEqual(
                self.github_release.releases_a_version(subject),
                self.github_release.release_level(subject) is not None,
                subject,
            )

    def test_bump_version_applies_the_level(self):
        bump = self.github_release.bump_version
        self.assertEqual(bump("3.12.1", "patch"), "3.12.2")
        self.assertEqual(bump("3.12.1", "minor"), "3.13.0")
        self.assertEqual(bump("3.12.1", "major"), "4.0.0")
        self.assertIsNone(bump("3.12.1", None))

    def test_packaged_path_applies_a_patch_floor(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_multi_path_repo(root)
            # A chore releases nothing on its own, but it changed bytes that
            # enter the published artifact, so it must still ship.
            self.touch(root, f"{self.CHART_PATH}/values.yaml", "chore(chart): retune a default")
            level, reasons = self.github_release.owned_paths_release_level(
                root, self.nvca_metadata(), "src/compute-plane-services/nvca/v3.12.1"
            )
            self.assertEqual(level, "patch")
            self.assertEqual(len(reasons), 1)

    def test_unpackaged_path_gets_no_floor(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_multi_path_repo(root)
            service = self.nvca_metadata()
            service["owns_paths"] = [{"path": self.CHART_PATH, "packaged": False}]
            self.touch(root, f"{self.CHART_PATH}/values.yaml", "chore(chart): retune a default")
            level, _ = self.github_release.owned_paths_release_level(
                root, service, "src/compute-plane-services/nvca/v3.12.1"
            )
            self.assertIsNone(level)

    def test_owned_path_feat_outranks_a_service_fix(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_multi_path_repo(root)
            self.touch(root, f"{self.CHART_PATH}/values.yaml", "feat(chart): expose a setting")
            version, source = self.github_release.multi_path_release_version(
                root, self.nvca_metadata(), "3.12.2"
            )
            # semantic-release saw only a patch in the service directory; the
            # chart's feat is the higher level and wins.
            self.assertEqual(version, "3.13.0")
            self.assertEqual(source, "owned-paths")

    def test_service_level_wins_when_it_is_higher(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_multi_path_repo(root)
            self.touch(root, f"{self.CHART_PATH}/values.yaml", "fix(chart): correct a default")
            version, source = self.github_release.multi_path_release_version(
                root, self.nvca_metadata(), "3.13.0"
            )
            self.assertEqual(version, "3.13.0")
            self.assertEqual(source, "semantic-release")

    def test_no_owned_paths_leaves_semantic_release_alone(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.init_multi_path_repo(root)
            self.touch(root, f"{self.CHART_PATH}/values.yaml", "feat(chart): expose a setting")
            version, source = self.github_release.multi_path_release_version(
                root, self.nvca_metadata(owns_paths=False), "3.12.2"
            )
            self.assertEqual(version, "3.12.2")
            self.assertEqual(source, "semantic-release")


class FollowerReleaseTest(unittest.TestCase):
    """A follower tag lands on the leader's commit, not on HEAD."""

    setUp = GithubReleaseTest.setUp
    init_repo = GithubReleaseTest.init_repo
    seed_nvca_service = GithubReleaseTest.seed_nvca_service
    commit_all = GithubReleaseTest.commit_all

    CHART_PATH = MultiPathReleaseTest.CHART_PATH
    nvca_metadata = MultiPathReleaseTest.nvca_metadata
    seed_chart = MultiPathReleaseTest.seed_chart
    init_multi_path_repo = MultiPathReleaseTest.init_multi_path_repo
    touch = MultiPathReleaseTest.touch

    def follower_metadata(self):
        return {
            "id": "nvca-operator",
            "path": "deploy/helm/nvca-operator",
            "service_name": "helm-nvca-operator",
            "version_follows": "nvca",
        }

    def metadata(self):
        return {"services": [self.nvca_metadata(), self.follower_metadata()]}

    def test_follower_tags_the_leaders_commit_after_main_advances(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            root.mkdir()
            self.init_multi_path_repo(root, remote=Path(tmp) / "remote.git")
            self.touch(root, "src/compute-plane-services/nvca/a.go", "fix(nvca): correct a thing")
            git(root, "tag", "src/compute-plane-services/nvca/v3.12.2")
            leader_commit = git_out(root, "rev-parse", "HEAD").strip()

            # main moves on before the follower is created. Tagging HEAD here
            # would put one release on two different trees.
            self.touch(root, "unrelated.txt", "docs: something else entirely")
            self.assertNotEqual(git_out(root, "rev-parse", "HEAD").strip(), leader_commit)

            self.github_release.publish_follower_release(
                root, self.follower_metadata(), self.metadata(), dry_run=False, draft=False
            )

            tag = "deploy/helm/nvca-operator/v3.12.2"
            self.assertEqual(git_out(root, "rev-parse", f"{tag}^{{commit}}").strip(), leader_commit)

    def test_follower_is_idempotent(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            root.mkdir()
            self.init_multi_path_repo(root, remote=Path(tmp) / "remote.git")
            git(root, "tag", "src/compute-plane-services/nvca/v3.12.2")
            for _ in range(2):
                self.github_release.publish_follower_release(
                    root, self.follower_metadata(), self.metadata(), dry_run=False, draft=False
                )
            tags = git_out(root, "tag", "-l", "deploy/helm/nvca-operator/v*").split()
            self.assertEqual(tags, ["deploy/helm/nvca-operator/v3.12.2"])

    def test_follower_refuses_a_conflicting_existing_tag(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            root.mkdir()
            self.init_multi_path_repo(root, remote=Path(tmp) / "remote.git")
            git(root, "tag", "src/compute-plane-services/nvca/v3.12.2")
            # Someone already created the follower tag somewhere else.
            self.touch(root, "unrelated.txt", "docs: elsewhere")
            git(root, "tag", "deploy/helm/nvca-operator/v3.12.2")
            with self.assertRaises(SystemExit):
                self.github_release.publish_follower_release(
                    root, self.follower_metadata(), self.metadata(), dry_run=False, draft=False
                )

    def test_follower_follows_the_stable_tag_under_a_newer_prerelease(self):
        """A prerelease must not hide the stable release the follower owes a tag."""
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "repo"
            root.mkdir()
            self.init_multi_path_repo(root, remote=Path(tmp) / "remote.git")
            self.touch(root, "src/compute-plane-services/nvca/a.go", "fix(nvca): correct a thing")
            git(root, "tag", "src/compute-plane-services/nvca/v3.12.2")
            stable_commit = git_out(root, "rev-parse", "HEAD").strip()
            # A release candidate cut afterwards sorts above the stable release.
            self.touch(root, "src/compute-plane-services/nvca/b.go", "feat(nvca): start the next line")
            git(root, "tag", "src/compute-plane-services/nvca/v3.13.0-rc.1")

            self.github_release.publish_follower_release(
                root, self.follower_metadata(), self.metadata(), dry_run=False, draft=False
            )

            tag = "deploy/helm/nvca-operator/v3.12.2"
            self.assertEqual(
                git_out(root, "tag", "-l", "deploy/helm/nvca-operator/v*").split(), [tag]
            )
            self.assertEqual(git_out(root, "rev-parse", f"{tag}^{{commit}}").strip(), stable_commit)


class PackagedAppVersionTest(unittest.TestCase):
    """A follower chart publishes the operator version its release carries."""

    setUp = GithubReleaseTest.setUp

    def chart(self, tmp, app_version="3.10.0"):
        chart_dir = Path(tmp) / "nvca-operator"
        chart_dir.mkdir()
        (chart_dir / "Chart.yaml").write_text(
            "apiVersion: v2\n"
            "name: helm-nvca-operator\n"
            "version: 0.0.0\n"
            f'appVersion: "{app_version}"\n'
        )
        (chart_dir / "values.yaml").write_text("image:\n  tag: \"\"\n")
        return chart_dir

    def packaged_app_version(self, package):
        out = subprocess.run(
            ["helm", "show", "chart", str(package)],
            check=True, stdout=subprocess.PIPE, text=True,
        ).stdout
        for line in out.splitlines():
            if line.startswith("appVersion:"):
                return line.split(":", 1)[1].strip().strip('"')
        return ""

    def test_follower_package_carries_the_release_version(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "out"
            out.mkdir()
            package = self.github_release.package_release_chart(
                self.chart(tmp), "3.13.0", out, app_version="3.13.0"
            )
            # Committed appVersion was 3.10.0. Publishing it unchanged would ship
            # a chart that resolves the operator image to a superseded release.
            self.assertEqual(self.packaged_app_version(package), "3.13.0")

    def test_other_charts_keep_their_committed_app_version(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "out"
            out.mkdir()
            package = self.github_release.package_release_chart(
                self.chart(tmp), "1.28.4", out
            )
            self.assertEqual(self.packaged_app_version(package), "3.10.0")


if __name__ == "__main__":
    unittest.main()

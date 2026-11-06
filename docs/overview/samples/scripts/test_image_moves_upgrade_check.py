# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

SCRIPTS = Path(__file__).parent.resolve()
SCRIPT = SCRIPTS / "check-upgrade-image-moves.py"
GUIDE = SCRIPTS.parents[1] / "release-notes/1.0.x-to-1.1.0-upgrade.md"


def deployment(namespace, name, *images):
    return {"metadata": {"namespace": namespace, "name": name},
            "spec": {"template": {"spec": {"containers": [
                {"name": f"c{index}", "image": image} for index, image in enumerate(images)]}}}}


class ImageMoveChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.render = self.root / "render"
        self.render.mkdir()
        self.before = self.root / "images-before.tsv"

    def run_script(self, *args, env=None):
        return subprocess.run(["python3", str(SCRIPT), *args], text=True,
                              capture_output=True, env=env or os.environ)

    def write_before(self, *pairs):
        self.before.write_text("".join(f"ns/{name}/c\t{image}\n" for name, image in pairs))

    def write_render(self, *images):
        body = "".join(f"        - name: c{i}\n          image: {image}\n"
                       for i, image in enumerate(images))
        (self.render / "all.yaml").write_text(f"spec:\n  containers:\n{body}")

    def test_newer_or_equal_images_pass(self):
        self.write_before(("nats", "docker.io/synadia/nats-server:2.14.6-alpine3.22"),
                          ("api", "nvcr.io/nvidia/nvcf/nvcf-service-oss:1.18.0"))
        self.write_render("docker.io/synadia/nats-server:2.14.6-alpine3.22",
                          "nvcr.io/nvidia/nvcf/nvcf-service-oss:1.28.4")
        result = self.run_script("compare", str(self.before), str(self.render))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("No image moves to an older version (2 compared)", result.stdout)

    def test_a_backward_move_stops(self):
        self.write_before(("nats", "docker.io/synadia/nats-server:2.14.9-alpine3.22"))
        self.write_render("docker.io/synadia/nats-server:2.14.6-alpine3.22")
        result = self.run_script("compare", str(self.before), str(self.render))
        self.assertEqual(result.returncode, 1)
        self.assertIn("BACKWARD nats-server: running 2.14.9-alpine3.22, the render pulls "
                      "2.14.6-alpine3.22", result.stdout)

    def test_flavoured_tags_compare_by_every_number(self):
        self.write_before(("openbao", "nvcr.io/nvidia/nvcf/nvcf-openbao:2.6.3-nv-1.3.9"))
        self.write_render("nvcr.io/nvidia/nvcf/nvcf-openbao:2.6.3-nv-1.3.7")
        self.assertEqual(self.run_script("compare", str(self.before), str(self.render)).returncode, 1)

    def test_the_newest_rendered_version_is_the_one_compared(self):
        # Two migrations images render; the older one belongs to an optional add-on.
        self.write_before(("migrations", "nvcr.io/nvidia/nvcf/nvcf-openbao-migrations:0.19.10"))
        self.write_render("nvcr.io/nvidia/nvcf/nvcf-openbao-migrations:0.19.10",
                          "nvcr.io/nvidia/nvcf/nvcf-openbao-migrations:0.23.1")
        result = self.run_script("compare", str(self.before), str(self.render))
        self.assertEqual(result.returncode, 0, result.stdout)

    def test_digest_pinned_and_untagged_images_are_skipped(self):
        self.write_before(("a", "registry.example/a@sha256:" + "0" * 64), ("b", "registry.example/b:latest"))
        self.write_render("registry.example/a:1.0.0", "registry.example/b:0.0.1")
        result = self.run_script("compare", str(self.before), str(self.render))
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("(0 compared)", result.stdout)

    def test_a_mirror_registry_does_not_hide_a_backward_move(self):
        self.write_before(("api", "mirror.example/team/nvcf-service-oss:1.28.4"))
        self.write_render("nvcr.io/nvidia/nvcf/nvcf-service-oss:1.28.2")
        self.assertEqual(self.run_script("compare", str(self.before), str(self.render)).returncode, 1)

    def test_before_records_every_container_including_init(self):
        items = {"items": [deployment("nvcf", "api", "nvcr.io/x/api:1.0.0")]}
        items["items"][0]["spec"]["template"]["spec"]["initContainers"] = [
            {"name": "init", "image": "nvcr.io/x/init:2.0.0"}]
        stub = self.root / "kubectl"
        stub.write_text(f"#!/bin/sh\ncat <<'JSON'\n{json.dumps(items)}\nJSON\n")
        stub.chmod(0o700)
        env = {**os.environ, "PATH": f"{self.root}{os.pathsep}{os.environ['PATH']}"}
        result = self.run_script("before", "synthetic-config", str(self.before), env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.before.read_text().splitlines(),
                         ["nvcf/api/c0\tnvcr.io/x/api:1.0.0", "nvcf/api/init\tnvcr.io/x/init:2.0.0"])

    def test_usage_errors_exit_nonzero(self):
        self.assertEqual(self.run_script().returncode, 2)
        self.assertEqual(self.run_script("compare", str(self.before)).returncode, 2)

    def test_the_guide_runs_the_documented_commands(self):
        # Run the guide's own check commands. The surrounding blocks also use a live cluster.
        text = GUIDE.read_text(encoding="utf-8")
        found = {mode: re.search(r'^python3 "\$UPGRADE_SCRIPTS/check-upgrade-image-moves\.py" '
                                 + mode + r'(?: \\\n[^\n]*|[^\n]*)$', text, re.MULTILINE)
                 for mode in ("before", "compare")}
        self.assertTrue(all(found.values()), found)
        record, compare = found["before"][0], found["compare"][0]
        self.assertIn("images-before.tsv", record)
        stub = self.root / "kubectl"
        items = {"items": [deployment("nvcf", "nats", "docker.io/synadia/nats-server:2.14.9-alpine3.22")]}
        stub.write_text(f"#!/bin/sh\ncat <<'JSON'\n{json.dumps(items)}\nJSON\n")
        stub.chmod(0o700)
        render = self.root / "control-render"
        render.mkdir()
        (render / "nats.yaml").write_text("image: docker.io/synadia/nats-server:2.14.6-alpine3.22\n")
        env = {**os.environ, "UPGRADE_SCRIPTS": str(SCRIPTS), "WORKDIR": str(self.root),
               "CONTROL_KUBECONFIG": "synthetic-config",
               "PATH": f"{self.root}{os.pathsep}{os.environ['PATH']}"}
        ran = subprocess.run(["bash", "-c", "set -euo pipefail\n" + record],
                             env=env, text=True, capture_output=True)
        self.assertEqual(ran.returncode, 0, ran.stderr)
        self.assertTrue((self.root / "images-before.tsv").exists())
        stopped = subprocess.run(["bash", "-c", "set -euo pipefail\n" + compare],
                                 env=env, text=True, capture_output=True)
        self.assertEqual(stopped.returncode, 1)
        self.assertIn("BACKWARD nats-server", stopped.stdout)


if __name__ == "__main__":
    unittest.main()

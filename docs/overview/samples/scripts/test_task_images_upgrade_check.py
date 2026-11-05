# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Exercise task image resolution and pull probes from the upgrade guide."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from test_recovery_upgrade_check import RECOVERY, block_containing


class TaskImageChecks(unittest.TestCase):
    def resolve(self, sidecars, hostname="mirror.example", repository="tasks"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            # JSON is valid YAML; keep fixtures independent of a Python YAML library.
            (root / "configmap-env.yaml").write_text(json.dumps({"data": {
                "NVCT_SIDECARS_HOSTNAME": hostname,
                "NVCT_SIDECARS_REPOSITORY": repository,
            }}))
            (root / "configmap-remote-config.yaml").write_text(json.dumps({"data": {
                "nvct-api.yaml": json.dumps({"nvct": {"sidecars": sidecars}}),
            }}))
            command = block_containing(RECOVERY, "TASK_IMAGES=$(").replace(
                "/absolute/path/to/target-render/helm-nvcf-nvct-api/templates", str(root))
            return subprocess.run(["bash", "-euo", "pipefail", "-c", command],
                                  capture_output=True, text=True)

    def test_target_images_resolve_mirror_without_evaluating_shell(self):
        result = self.resolve({
            "init-container": "${nvct.sidecars.hostname}/${nvct.sidecars.repository}/init:4",
            "utils-container": "${nvct.sidecars.hostname}/${nvct.sidecars.repository}/task:5",
        }, repository="tasks/$(printf unsafe)")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("mirror.example/tasks/$(printf unsafe)/init:4", result.stdout)
        self.assertIn("mirror.example/tasks/$(printf unsafe)/task:5", result.stdout)

    def test_explicit_image_overrides_keep_registry_and_digest(self):
        images = {"init-container": "private.example/init@sha256:abc",
                  "utils-container": "private.example/tasks/worker:next"}
        result = self.resolve(images, hostname="", repository="")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), list(images.values()))

    def test_missing_or_unresolved_image_blocks_progress(self):
        for image in (None, "", "${custom.image}"):
            with self.subTest(image=image):
                result = self.resolve({"init-container": "mirror.example/init:4",
                                       "utils-container": image})
                self.assertNotEqual(result.returncode, 0)

    def test_missing_registry_blocks_templated_reference(self):
        for hostname, repository in (("", "tasks"), ("mirror.example", "")):
            with self.subTest(hostname=hostname, repository=repository):
                result = self.resolve({
                    "init-container": "${nvct.sidecars.hostname}/${nvct.sidecars.repository}/init:4",
                    "utils-container": "mirror.example/task:5",
                }, hostname=hostname, repository=repository)
                self.assertNotEqual(result.returncode, 0)

    def run_probe(self, wait_status, phase="Succeeded"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            command = block_containing(RECOVERY, "TASK_PULL_SECRETS=")
            stub = r'''
kubectl() {
  printf '%s\n' "$*" >> "$CALL_LOG"
  case "$*" in
    *'create -f -'*)
      cat >> "$POD_LOG"
      printf '%s\n' 'task-image-check-test'
      ;;
    *'wait '*)
      case "$*" in
        *containerStatuses*imageID*) return "$WAIT_STATUS" ;;
        *status.phase*Succeeded*) test "$POD_PHASE" = Succeeded ;;
        *) return 99 ;;
      esac ;;
    *'get pod/'*) printf '%s\n' 'node-a sha256:pulled' ;;
    *'delete pod/'*) ;;
    *) return 99 ;;
  esac
}
'''
            env = {**os.environ, "CALL_LOG": str(root / "calls"),
                   "POD_LOG": str(root / "pods"), "WAIT_STATUS": str(wait_status), "POD_PHASE": phase,
                   "TASK_IMAGES": "mirror.example/init:4\nmirror.example/task:5"}
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", stub + command],
                                    env=env, capture_output=True, text=True)
            return result, (root / "calls").read_text().splitlines(), [
                json.loads(line) for line in (root / "pods").read_text().splitlines()]

    def test_probe_uses_selected_cluster_node_credentials_and_help(self):
        result, calls, pods = self.run_probe(0)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(pods), 2)
        prefix = ("--kubeconfig /absolute/path/to/compute-kubeconfig "
                  "--context compute-context -n existing-task-namespace ")
        self.assertTrue(all(call.startswith(prefix) for call in calls))
        for pod, image in zip(pods, ["mirror.example/init:4", "mirror.example/task:5"]):
            spec = pod["spec"]
            self.assertEqual(spec["nodeName"], "existing-task-node")
            self.assertEqual(spec["serviceAccountName"], "task-service-account")
            self.assertEqual(spec["imagePullSecrets"], [{"name": "task-registry-secret"}])
            self.assertFalse(spec["automountServiceAccountToken"])
            self.assertEqual(spec["restartPolicy"], "Never")
            container = spec["containers"][0]
            self.assertEqual(container["image"], image)
            self.assertEqual(container["imagePullPolicy"], "Always")
            self.assertEqual(container["args"], ["--help"])
            self.assertNotIn("command", container)
        self.assertEqual(sum("wait --for=jsonpath={.status.containerStatuses[0].imageID}" in c for c in calls), 2)
        self.assertEqual(sum("delete pod/task-image-check-test" in c for c in calls), 2)

    def test_failed_pull_stops_before_next_image_and_preserves_probe(self):
        result, calls, pods = self.run_probe(1)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(pods), 1)
        self.assertFalse(any("delete " in call for call in calls))

    def test_pulled_image_does_not_require_application_success(self):
        for phase in ("Running", "Failed"):
            with self.subTest(phase=phase):
                result, calls, _ = self.run_probe(0, phase=phase)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(sum("delete pod/" in call for call in calls), 2)


if __name__ == "__main__":
    unittest.main()

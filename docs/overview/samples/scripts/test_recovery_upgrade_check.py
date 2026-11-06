# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Execute upgrade recovery commands with synthetic messages and CLI output."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest


DOCS = Path(__file__).resolve().parents[3]
RECOVERY = DOCS / "self-managed/upgrade-recovery.md"


def block_containing(path, marker):
    blocks = re.findall(r"```bash\n(.*?)```", path.read_text(), re.DOTALL)
    matches = [block for block in blocks if marker in block]
    if len(matches) != 1:
        raise AssertionError(f"Expected one command block containing {marker}")
    return textwrap.dedent(matches[0])


class UpgradeRecoveryChecks(unittest.TestCase):
    def test_nats_inspection_uses_selected_context_without_mutation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = {**os.environ, "CALL_LOG": str(root / "calls"),
                   "NATS_CONTEXT": "site-context"}
            stub = r'''
kubectl() {
  printf '%s\n' "$*" >> "$CALL_LOG"
  case "$*" in
    *'get pod'*)
      case "$*" in
        *'-o json') printf '%s\n' '{"items":[{"metadata":{"name":"test-box"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}' ;;
        *) printf '%s\n' 'test-box' ;;
      esac ;;
    *'stream info'*) printf '%s\n' '{"state":{"messages":1,"first_seq":7}}' ;;
    *'consumer list'*) printf '%s\n' 'creation-consumer' ;;
    *) return 99 ;;
  esac
}
'''
            command = block_containing(RECOVERY, 'NATS_BOX_POD=')
            command = command.replace('/absolute/path/to/new-recovery-record',
                                      str(root / "record"))
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", stub + command],
                                    env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = (root / "calls").read_text().splitlines()
            nats_calls = [call for call in calls if ' -- nats ' in call]
            prefix = ('--kubeconfig /absolute/path/to/control-plane-kubeconfig '
                      '--context control-plane-context -n nats-system exec test-box '
                      '-- nats --context site-context ')
            self.assertEqual(nats_calls, [
                prefix + 'stream info CreateNvcaFunctionTaskStream --json',
                prefix + 'consumer list CreateNvcaFunctionTaskStream'])
            self.assertEqual(json.loads((root / "record/stream-before.json").read_text())
                             ['state']['messages'], 1)

    def test_nats_inspection_skips_pending_and_terminating_pods(self):
        for ready in (True, False):
            with self.subTest(ready=ready), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                pods = [
                    {"metadata": {"name": "pending"}, "status": {}},
                    {"metadata": {"name": "terminating", "deletionTimestamp": "2026-01-01T00:00:00Z"},
                     "status": {"conditions": [{"type": "Ready", "status": "True"}]}},
                ]
                if ready:
                    pods.append({"metadata": {"name": "ready"},
                                 "status": {"conditions": [{"type": "Ready", "status": "True"}]}})
                env = {**os.environ, "PODS": json.dumps({"items": pods}),
                       "CALL_LOG": str(root / "calls")}
                stub = r'''
kubectl() {
  case "$*" in
    *'get pod'*'-o json') printf '%s\n' "$PODS" ;;
    *'get pod'*) printf '%s\n' 'pending' ;;
    *'exec '*) printf '%s\n' "$*" >> "$CALL_LOG" ;;
    *) return 99 ;;
  esac
}
'''
                command = block_containing(RECOVERY, 'NATS_BOX_POD=')
                command = command.replace('/absolute/path/to/new-recovery-record',
                                          str(root / "record"))
                result = subprocess.run(["bash", "-euo", "pipefail", "-c", stub + command],
                                        env=env, capture_output=True, text=True)
                if ready:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    calls = (root / "calls").read_text().splitlines()
                    self.assertEqual(len(calls), 2)
                    self.assertTrue(all('exec ready -- nats ' in call for call in calls))
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse((root / "calls").exists())

    def test_compute_evidence_preserves_each_cluster_and_repeat_capture(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for cluster in ("compute-a", "compute-b", "compute-a"):
                result = self.run_compute_capture(root, cluster)
                self.assertEqual(result.returncode, 0, result.stderr)
            records = list(root.glob("compute-*"))
            self.assertEqual(len(records), 3)
            self.assertEqual(len(list(root.glob("compute-compute-a.*"))), 2)
            self.assertEqual(len(list(root.glob("compute-compute-b.*"))), 1)
            for record in records:
                expected = "compute-a" if record.name.startswith("compute-compute-a.") else "compute-b"
                self.assertEqual(json.loads(record.read_text())["items"][0]["metadata"]["name"], expected)

    def test_failed_compute_capture_preserves_previous_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = self.run_compute_capture(root, "compute-a")
            self.assertEqual(result.returncode, 0, result.stderr)
            before = {p: p.read_bytes() for p in root.iterdir()}
            result = self.run_compute_capture(root, "compute-a", status=1)
            self.assertNotEqual(result.returncode, 0)
            for path, contents in before.items():
                self.assertEqual(path.read_bytes(), contents)

    def run_compute_capture(self, root, cluster, status=0):
        env = {**os.environ, "RECOVERY_RECORD": str(root), "CAPTURE_STATUS": str(status),
               "CAPTURE_CLUSTER": cluster,
               "CAPTURE_JSON": json.dumps({"items": [{"metadata": {"name": cluster}}]})}
        stub = r'''
kubectl() {
  test "$*" = "--kubeconfig /site/$CAPTURE_CLUSTER --context $CAPTURE_CLUSTER get icmsrequests.nvca.nvcf.nvidia.io -A -o json"
  if [ "$CAPTURE_STATUS" != 0 ]; then return "$CAPTURE_STATUS"; fi
  printf '%s\n' "$CAPTURE_JSON"
}
'''
        command = block_containing(RECOVERY, 'get icmsrequests.nvca.nvcf.nvidia.io')
        command = command.replace('COMPUTE_CLUSTER="compute-cluster-name"',
                                  f'COMPUTE_CLUSTER="{cluster}"')
        command = command.replace('/absolute/path/to/compute-kubeconfig', f'/site/{cluster}')
        command = command.replace('COMPUTE_CONTEXT="compute-context"',
                                  f'COMPUTE_CONTEXT="{cluster}"')
        return subprocess.run(["bash", "-euo", "pipefail", "-c", stub + command],
                              env=env, capture_output=True, text=True)


if __name__ == "__main__":
    unittest.main()

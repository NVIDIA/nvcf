# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Run the downloadable checks with synthetic, read-only command adapters."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPTS = Path(__file__).parent.resolve()
ADAPTER = r'''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
a = sys.argv[1:]
with open(os.environ['CALLS'], 'a') as f:
    f.write(json.dumps([Path(sys.argv[0]).name, *a]) + '\n')
if os.environ.get('READ_ERROR'):
    print('synthetic-sensitive-server-error', file=sys.stderr)
    sys.exit(1)
assert '--kubeconfig' in a and '--context' in a
if 'nvcf-upgrade-receipt' in a:
    print(os.environ.get('RECEIPT', 'selected-version'))
elif 'deployments,statefulsets,daemonsets' in a:
    items = [{'kind':'Deployment', 'metadata':{'name':'api'}, 'spec':{}},
             {'kind':'StatefulSet', 'metadata':{'name':'openbao'}, 'spec':{'updateStrategy':{'type':'OnDelete'}}}]
    if os.environ.get('DAEMONSET_ONLY'):
        items = [{'kind':'DaemonSet', 'metadata':{'name':'collector'}, 'spec':{}}]
    print(json.dumps({'items':[] if os.environ.get('EMPTY_WORKLOADS') else items}))
elif 'rollout' in a:
    sys.exit(1 if os.environ.get('ROLLOUT_ERROR') else 0)
elif 'pods' in a:
    print('pod-status')
else:
    raise AssertionError('Unexpected command: ' + repr(a))
'''


class DownloadedChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        adapter = self.root / "kubectl"
        adapter.write_text(ADAPTER)
        adapter.chmod(0o700)
        self.env = {**os.environ, "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
                    "CALLS": str(self.root / "calls"), "CONTROL_KUBECONFIG": "/control",
                    "CONTROL_CONTEXT": "control", "COMPUTE_KUBECONFIG": "/compute",
                    "COMPUTE_CONTEXT": "compute", "EXPECTED_CONTROL_STACK_VERSION": "selected-version"}

    def run_script(self, name, *args, **env):
        return subprocess.run([sys.executable, str(SCRIPTS / name), *map(str, args)],
                              env={**self.env, **env}, capture_output=True, text=True)

    def test_workloads_wait_for_rolling_updates_but_leave_ondelete_explicit(self):
        result = self.run_script("wait-upgrade-workloads.py", "/control", "control", "nvcf")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("explicit pod-replacement", result.stdout)
        calls = [json.loads(line) for line in (self.root / "calls").read_text().splitlines()]
        rollouts = [call for call in calls if "rollout" in call]
        self.assertEqual(len(rollouts), 1)
        self.assertIn("Deployment/api", rollouts[0])

    def test_empty_workloads_or_failed_rollout_stop(self):
        for key in ("EMPTY_WORKLOADS", "ROLLOUT_ERROR", "READ_ERROR"):
            with self.subTest(key=key):
                result = self.run_script("wait-upgrade-workloads.py", "/control", "control", "nvcf", **{key: "1"})
                self.assertNotEqual(result.returncode, 0)

    def test_daemonset_only_namespace_is_checked(self):
        result = self.run_script("wait-upgrade-workloads.py", "/control", "control", "monitoring", DAEMONSET_ONLY="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = [json.loads(line) for line in (self.root / "calls").read_text().splitlines()]
        self.assertTrue(any("rollout" in call and "DaemonSet/collector" in call for call in calls))

    def test_receipt_compares_independent_selection_and_requires_context(self):
        result = self.run_script("check-upgrade-receipt.py")
        self.assertEqual(result.returncode, 0, result.stderr)
        for env in ({"RECEIPT": "old-version"}, {"RECEIPT": " selected-version "}, {"RECEIPT": ""},
                    {"CONTROL_CONTEXT": ""}, {"READ_ERROR": "1"}):
            with self.subTest(env=env):
                result = self.run_script("check-upgrade-receipt.py", **env)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("synthetic-sensitive", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()

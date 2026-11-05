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
if Path(sys.argv[0]).name == 'helm':
    assert '--kubeconfig' in a and '--kube-context' in a and 'list' in a
    offset = int(a[a.index('--offset') + 1])
    if os.environ.get('REPEATED_PAGE'):
        offset = 0
    count = 257 if os.environ.get('PAGED_RELEASES') else 1
    page = [{'name':f'api-{i}', 'namespace':'nvcf', 'status':'deployed'} for i in range(offset, min(offset+256, count))]
    print(json.dumps([] if os.environ.get('EMPTY_RELEASES') else page))
elif Path(sys.argv[0]).name == 'yq':
    print(sys.stdin.read())
else:
    assert '--kubeconfig' in a and '--context' in a
    context = a[a.index('--context') + 1]
    if 'current-context' in a:
        print('wrong-context' if os.environ.get('WRONG_CONTEXT') else context)
    elif 'namespace' in a:
        print('cluster-' + context)
    elif 'nvcfbackend-self-managed' in a:
        identity = {'clusterId':'id', 'clusterGroupId':'group', 'clusterName':'compute', 'ncaID':'account'}
        if os.environ.get('BAD_IDENTITY'):
            identity['clusterId'] = ''
        print(json.dumps({'data':{'cluster-dto.yaml':json.dumps(identity)}}))
    elif 'nvcf-upgrade-receipt' in a:
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
        for tool in ("kubectl", "helm", "yq"):
            path = self.root / tool
            path.write_text(ADAPTER)
            path.chmod(0o700)
        self.env = {**os.environ, "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
                    "CALLS": str(self.root / "calls"), "CONTROL_KUBECONFIG": "/control",
                    "CONTROL_CONTEXT": "control", "COMPUTE_KUBECONFIG": "/compute",
                    "COMPUTE_CONTEXT": "compute", "EXPECTED_CONTROL_STACK_VERSION": "selected-version"}

    def run_script(self, name, *args, **env):
        return subprocess.run([sys.executable, str(SCRIPTS / name), *map(str, args)],
                              env={**self.env, **env}, capture_output=True, text=True)

    def test_collection_is_private_and_never_overwrites_baseline(self):
        output = self.root / "before"
        result = self.run_script("collect-upgrade-state.py", output)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((output / "complete").is_file())
        self.assertEqual(output.stat().st_mode & 0o777, 0o700)
        original = {p.name: p.read_bytes() for p in output.iterdir()}
        for path in output.iterdir():
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        calls = (self.root / "calls").read_text()
        result = self.run_script("collect-upgrade-state.py", output)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(original, {p.name: p.read_bytes() for p in output.iterdir()})
        self.assertEqual(calls, (self.root / "calls").read_text())

    def test_missing_context_does_not_use_ambient_cluster(self):
        result = self.run_script("collect-upgrade-state.py", self.root / "before", CONTROL_CONTEXT="")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / "calls").exists())

    def test_failed_incomplete_or_wrong_cluster_reads_cannot_complete(self):
        for key in ("READ_ERROR", "EMPTY_RELEASES", "BAD_IDENTITY", "WRONG_CONTEXT"):
            with self.subTest(key=key):
                output = self.root / key
                result = self.run_script("collect-upgrade-state.py", output, **{key: "1"})
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((output / "complete").exists())
                self.assertNotIn("synthetic-sensitive", result.stderr + result.stdout)

    def test_distinct_captures_preserve_cluster_and_registration_identity(self):
        for name in ("before", "after"):
            result = self.run_script("collect-upgrade-state.py", self.root / name)
            self.assertEqual(result.returncode, 0, result.stderr)
        for name in ("identity.json", "clusters.json"):
            self.assertEqual((self.root / "before" / name).read_bytes(),
                             (self.root / "after" / name).read_bytes())
        clusters = json.loads((self.root / "before/clusters.json").read_text())
        self.assertNotEqual(clusters["control"], clusters["compute"])

    def test_release_collection_reads_all_pages_and_rejects_repeated_pages(self):
        output = self.root / "paged"
        result = self.run_script("collect-upgrade-state.py", output, PAGED_RELEASES="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(json.loads((output / "control-releases.json").read_text())), 257)
        output = self.root / "repeated"
        result = self.run_script("collect-upgrade-state.py", output, PAGED_RELEASES="1", REPEATED_PAGE="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((output / "complete").exists())

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

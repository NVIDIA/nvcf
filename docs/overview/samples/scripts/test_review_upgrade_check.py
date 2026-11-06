# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Execute the reviewed upgrade gates with synthetic external command adapters."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

from test_receipt_upgrade_check import GUIDE
from test_recovery_upgrade_check import UPGRADE_11, block_containing


class ReviewedUpgradeGates(unittest.TestCase):
    def test_missing_artifacts_stop_after_reporting_all_checks(self):
        script = r'''
helm() { test "$MISSING_KIND" != chart; }
docker() { test "$MISSING_KIND" != image; }
'''
        command = block_containing(UPGRADE_11, 'artifact_missing=0')
        for kind in ("none", "chart", "image"):
            with self.subTest(kind=kind):
                result = subprocess.run(["bash", "-euo", "pipefail", "-c",
                                         script + command + "\necho CONTINUED"],
                                        env={**os.environ, "MISSING_KIND": kind}, capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, kind == "none", result.stderr)
                self.assertEqual("CONTINUED" in result.stdout, kind == "none")
                if kind != "none":
                    self.assertIn("MISSING", result.stdout)

    def test_starting_state_requires_ready_pods_and_rejects_empty_namespaces(self):
        ready = {"metadata": {}, "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}]}}
        completed = {"metadata": {}, "status": {"phase": "Succeeded"}}
        cases = [(True, [ready]), (True, [ready, completed]), (False, []),
                 (False, [{"metadata": {}, "status": {"phase": "Pending"}}]),
                 (False, [ready, {"metadata": {}, "status": {"phase": "Failed"}}]),
                 (False, [{**ready, "metadata": {"deletionTimestamp": "2026-01-01"}}])]
        command = block_containing(UPGRADE_11, "# Releases deployed")
        stubs = r'''
helm() { printf '%s\n' '[{"name":"api","status":"deployed"}]'; }
python3() { return 0; }
kc() {
  case "$*" in
    *'get pods'*'jsonpath'*) printf '%s\n' cassandra-0 ;;
    *'get pods'*'-o json'*) printf '%s\n' "$PODS" ;;
  esac
}
'''
        for expected, pods in cases:
            with self.subTest(pods=pods), tempfile.TemporaryDirectory() as directory:
                result = subprocess.run(["bash", "-euo", "pipefail", "-c", stubs + command + "\necho CONTINUED"],
                                        env={**os.environ, "PODS": json.dumps({"items": pods}),
                                             "WORKDIR": directory, "CONTROL_KUBECONFIG": "synthetic",
                                             "UPGRADE_SCRIPTS": directory},
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, expected, result.stderr)
                self.assertEqual("CONTINUED" in result.stdout, expected)

    def test_nats_request_has_timeouts_and_stops_on_transport_failure(self):
        block = block_containing(GUIDE, "NATS_MONITOR_PORT=")
        command = re.search(r'(?m)^    curl .*?(?=\n    jq)', block, re.DOTALL)[0]
        stubs = r'''
curl() {
  case " $* " in *' --connect-timeout 5 '*) ;; *) return 99 ;; esac
  case " $* " in *' --max-time 30 '*) return 28 ;; *) return 99 ;; esac
}
'''
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, "nats-drain").mkdir()
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", stubs + command + "\necho CONTINUED"],
                                    env={**os.environ, "UPGRADE_RECORD": directory, "pod": "test", "NATS_MONITOR_PORT": "12345"},
                                    capture_output=True, text=True)
            self.assertEqual(28, result.returncode, result.stderr)
            self.assertNotIn("CONTINUED", result.stdout)

    def test_identity_discovery_precedes_registration_file_gate(self):
        command = block_containing(UPGRADE_11, "record_identity() {")
        stubs = r'''
kubectl() {
  case "$*" in
    *configmap*) printf '%s\n' '{"clusterId":"id","clusterGroupId":"group","clusterName":"actual-name","ncaID":"account"}' ;;
  esac
}
'''
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", stubs + command.replace("/secure/path/to/existing-registration", str(Path(directory) / "missing"))],
                                    env={**os.environ, "WORKDIR": directory}, capture_output=True, text=True)
            self.assertEqual(0, result.returncode, result.stderr)
            self.assertIn("actual-name", result.stdout)
            self.assertFalse((Path(directory) / "missing").exists())


class CassandraReplicationCommands(unittest.TestCase):
    def run_replication(self, configured="custom_dc", live="custom_dc"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "cqlsh"
            binary.write_text('''#!/usr/bin/env python3
import json, os, pathlib, stat, sys
args = sys.argv[1:]
credentials = pathlib.Path(next(a.split('=', 1)[1] for a in args if a.startswith('--credentials=')))
assert stat.S_IMODE(credentials.stat().st_mode) == 0o600
assert 'password = fixture-secret' in credentials.read_text()
assert all('fixture-secret' not in a for a in args)
with open(os.environ['CQL_LOG'], 'a') as log: log.write(json.dumps(args) + '\\n')
query = args[-1]
if 'SELECT JSON data_center' in query: print(json.dumps({'data_center': os.environ['LIVE_DC']}))
elif 'SELECT keyspace_name FROM' in query: print('nvcf_api')
''')
            binary.chmod(0o755)
            stubs = r'''
kc() {
  case "$*" in
    *'get statefulset'*) printf '%s\n' "$STATEFULSET" ;;
    *'exec '*' -- sh '*)
      while [ "$1" != -- ]; do shift; done
      shift
      "$@" ;;
    *'nodetool repair'*) echo repair >> "$CQL_LOG" ;;
    *) return 99 ;;
  esac
}
'''
            statefulset = {"spec": {"template": {"spec": {"containers": [{"name": "cassandra", "env": [{"name": "CASSANDRA_DC", "value": configured}]}]}}}}
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"], "TMPDIR": directory,
                   "CQL_LOG": str(root / "calls"), "LIVE_DC": live, "STATEFULSET": json.dumps(statefulset)}
            command = block_containing(UPGRADE_11, "CASSANDRA_DC=$(")
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", stubs + command + "\necho CONTINUED"],
                                    env=env, input="fixture-secret\n", capture_output=True, text=True)
            calls = (root / "calls").read_text() if (root / "calls").exists() else ""
            leftovers = [path for path in root.iterdir() if path.is_dir()]
            return result, calls, leftovers

    def test_custom_live_datacenter_is_used_and_password_stays_out_of_arguments(self):
        result, calls, leftovers = self.run_replication()
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertIn("'custom_dc': '3'", calls)
        self.assertNotIn("'ncp': '3'", calls)
        self.assertNotIn("fixture-secret", calls + result.stdout + result.stderr)
        self.assertEqual(3, calls.count("repair\n"))
        self.assertEqual([], leftovers)

    def test_datacenter_mismatch_or_unsafe_name_stops_before_alter_and_repair(self):
        for configured, live in (("custom_dc", "other"), ("bad'name", "bad'name")):
            with self.subTest(configured=configured):
                result, calls, leftovers = self.run_replication(configured, live)
                self.assertNotEqual(0, result.returncode)
                self.assertNotIn("ALTER", calls)
                self.assertNotIn("repair", calls)
                self.assertNotIn("CONTINUED", result.stdout)
                self.assertEqual([], leftovers)


if __name__ == "__main__":
    unittest.main()

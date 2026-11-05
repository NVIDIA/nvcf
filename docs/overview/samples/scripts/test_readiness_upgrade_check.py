# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Exercise the documented OpenBao transition gate without a cluster."""

import os
from pathlib import Path
import re
import signal
import subprocess
import tempfile
import unittest


UPGRADE = (Path(__file__).resolve().parents[3]
           / "overview/release-notes/0.6.1-to-1.0.1-upgrade.md")


class UpgradeReadinessChecks(unittest.TestCase):
    def run_wait(self, scenario):
        blocks = re.findall(r"```bash\n(.*?)```", UPGRADE.read_text(), re.DOTALL)
        matches = [block for block in blocks if "grep -Eq ':0[.]19[.]5$'" in block]
        self.assertEqual(len(matches), 1)
        command = matches[0]
        # The historical target version is the condition this gate must enforce.
        version = re.search(r"grep -Eq ':([^']+)\$'", command)[1].replace("[.]", ".")
        stub = r'''
kcp() {
  printf '%s\n' "$*" >> "$CALL_LOG"
  count=$(cat "$COUNT_FILE")
  printf '%s\n' "$((count + 1))" > "$COUNT_FILE"
  image="registry.example/openbao-migrations:$TARGET_VERSION"
  failed=
  case "$SCENARIO" in
    timeout) image=registry.example/openbao-migrations:source ;;
    failed) failed=True ;;
    read-error) return 9 ;;
    pending)
      if [ "$count" -eq 0 ]; then image=; fi ;;
    old-failed)
      if [ "$count" -eq 0 ]; then
        image=registry.example/openbao-migrations:source
        failed=True
      fi ;;
    wrong-tag) image="${image}-candidate" ;;
  esac
  case "$*" in
    *status.conditions*) printf '%s|%s\n' "$image" "$failed" ;;
    *) printf '%s\n' "$image" ;;
  esac
}
sleep() { SECONDS=$((SECONDS + 900)); }
'''
        # Pending jobs should get another poll before the synthetic deadline.
        if scenario in ("pending", "old-failed"):
            stub = stub.replace("SECONDS + 900", "SECONDS + 1")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "count").write_text("0\n")
            env = {**os.environ, "SCENARIO": scenario, "TARGET_VERSION": version,
                   "CALL_LOG": str(root / "calls"), "COUNT_FILE": str(root / "count")}
            with subprocess.Popen(["bash", "-euo", "pipefail", "-c", stub + command],
                                  env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                  text=True, start_new_session=True) as process:
                try:
                    stdout, stderr = process.communicate(timeout=2)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.communicate()
                    self.fail("The documented wait did not stop at its deadline")
                result = subprocess.CompletedProcess(process.args, process.returncode,
                                                     stdout, stderr)
            return result, (root / "calls").read_text().splitlines()

    def test_ready_target_job_allows_the_required_second_rotation(self):
        result, calls = self.run_wait("ready")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 1)

    def test_missing_job_is_polled_until_target_appears(self):
        result, calls = self.run_wait("pending")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 2)

    def test_failed_target_job_stops_without_waiting_for_timeout(self):
        result, calls = self.run_wait("failed")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("failed", result.stderr)
        self.assertEqual(len(calls), 1)

    def test_failed_source_job_does_not_replace_target_gate(self):
        result, calls = self.run_wait("old-failed")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 2)

    def test_missing_target_times_out(self):
        result, calls = self.run_wait("timeout")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Timed out", result.stderr)
        self.assertEqual(len(calls), 1)

    def test_similarly_named_image_does_not_pass(self):
        result, _ = self.run_wait("wrong-tag")
        self.assertNotEqual(result.returncode, 0)

    def test_api_error_stops_without_waiting_for_timeout(self):
        result, calls = self.run_wait("read-error")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len(calls), 1)


if __name__ == "__main__":
    unittest.main()

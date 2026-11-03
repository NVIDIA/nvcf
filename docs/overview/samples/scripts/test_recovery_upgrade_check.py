# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Execute upgrade recovery commands with synthetic messages and CLI output."""

import base64
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest


DOCS = Path(__file__).resolve().parents[3]
RECOVERY = DOCS / "self-managed/upgrade-recovery.md"
UPGRADE = DOCS / "overview/release-notes/0.6.1-to-1.0.1-upgrade.md"


def block_containing(path, marker):
    blocks = re.findall(r"```bash\n(.*?)```", path.read_text(), re.DOTALL)
    matches = [block for block in blocks if marker in block]
    if len(matches) != 1:
        raise AssertionError(f"Expected one command block containing {marker}")
    return matches[0]


class UpgradeRecoveryChecks(unittest.TestCase):
    def test_changed_message_stops_before_removal(self):
        for field, value in (("data", "bmV3LXJlcXVlc3Q="),
                             ("subject", "another.queue"),
                             ("time", "2026-11-04T01:00:00Z")):
            with self.subTest(field=field):
                self.run_removal(changed={field: value}, allowed=False)

    def test_identical_message_removes_only_selected_sequence_interactively(self):
        self.run_removal(allowed=True)

    def test_failed_reread_stops_before_removal(self):
        self.run_removal(read_status=1, allowed=False)

    def run_removal(self, changed=None, read_status=0, allowed=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            original = {"seq": 7, "subject": "creation.queue",
                        "time": "2026-11-03T01:00:00Z",
                        "data": base64.b64encode(b'{"requestId":"old-request"}').decode()}
            current = {**original, **(changed or {})}
            (root / "message-7.json").write_text(json.dumps(original))
            (root / "current.json").write_text(json.dumps(current))
            env = {**os.environ, "RECOVERY_RECORD": directory, "SEQ": "7",
                   "STREAM": "CreateNvcaFunctionTaskStream", "NATS_BOX_POD": "test-box",
                   "READ_STATUS": str(read_status)}
            stub = r'''
nats_read() {
  if [ "$1 $2" = 'stream get' ]; then
    if [ "$READ_STATUS" != 0 ]; then return "$READ_STATUS"; fi
    cat "$RECOVERY_RECORD/current.json"
  else
    printf '%s\n' '{"state":{"messages":0}}'
  fi
}
kcp() { printf '%s\n' "$*" >> "$RECOVERY_RECORD/removals"; }
'''
            command = block_containing(RECOVERY, 'if ! cmp -s')
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", stub + command],
                                    env=env, capture_output=True, text=True)
            removals = root / "removals"
            if allowed:
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(removals.read_text().splitlines(), [
                    "-n nats-system exec -it test-box -- nats --context default "
                    "stream rmm CreateNvcaFunctionTaskStream 7"])
                self.assertTrue((root / "stream-after.json").is_file())
            else:
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(removals.exists())
                self.assertFalse((root / "stream-after.json").exists())

    def test_profile_export_uses_explicit_config_and_context(self):
        self.run_profile_export(export_status=0)

    def test_failed_profile_export_does_not_validate_stale_profile(self):
        self.run_profile_export(export_status=1)

    def test_wrong_control_context_stops_before_cli_commands(self):
        self.run_profile_export(export_status=0, actual_context="another-context")

    def run_profile_export(self, export_status, actual_context="selected-context"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cli = root / "cli"
            cli.write_text("""#!/usr/bin/env bash
printf '%s|%s\\n' "${KUBECONFIG:-}" "$*" >> "$CALL_LOG"
case "$*" in *'profile export') exit "$EXPORT_STATUS" ;; esac
""")
            cli.chmod(0o755)
            env = {**os.environ, "NVCF_CLI": str(cli),
                   "NVCF_CLI_CONFIG": str(root / "site-config"),
                   "CONTROL_KUBECONFIG": str(root / "control-kubeconfig"),
                   "CONTROL_CONTEXT": "selected-context", "HELMFILE_ENV": "site",
                   "ACTUAL_CONTEXT": actual_context,
                   "TARGET_CONTROL": str(root / "target"),
                   "CALL_LOG": str(root / "calls"), "EXPORT_STATUS": str(export_status)}
            context_check = '''
kubectl() {
  test "$*" = "--kubeconfig $CONTROL_KUBECONFIG config current-context"
  printf '%s\\n' "$ACTUAL_CONTEXT"
}
'''
            command = context_check + block_containing(UPGRADE, 'control-plane profile export')
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", command],
                                    env=env, capture_output=True, text=True)
            call_log = root / "calls"
            if actual_context != env["CONTROL_CONTEXT"]:
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(call_log.exists())
                return
            calls = call_log.read_text().splitlines()
            self.assertEqual(len(calls), 3 if export_status == 0 else 2)
            self.assertEqual(result.returncode, export_status)
            self.assertTrue(all(f'--config {env["NVCF_CLI_CONFIG"]}' in call for call in calls))
            self.assertTrue(calls[1].startswith(env["CONTROL_KUBECONFIG"] + "|"))
            self.assertIn('--control-plane-stack ' + env["TARGET_CONTROL"], calls[1])
            if export_status == 0:
                self.assertIn('--require compute-reachable', calls[2])


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
from pod_runner import CANCEL, LAUNCH, WAIT, PodRunner


class PodRunnerTests(unittest.TestCase):
    def runner(self, root):
        directory = root / "remote run"
        directory.mkdir()
        return PodRunner(
            root / "state.json",
            {
                "context": "test",
                "namespace": "test",
                "pod": "spark",
                "directory": str(directory),
                "marker": "spark-test-owned-marker",
                "podIdentity": ["pod-id", ["container-id"]],
                "protocolVersion": 2,
            },
        )

    @staticmethod
    def local_exec(*command, token=None):
        return subprocess.run(
            command,
            input=token.encode() if token is not None else None,
            capture_output=True,
            timeout=5,
        )

    def test_lost_launch_ack_and_poll_do_not_restart_the_workload(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runner = self.runner(root)
            output = root / "token.txt"
            launches = 0
            failed_poll = False

            def execute(*command, token=None):
                nonlocal launches, failed_poll
                if command[:3] == ("bash", "-c", LAUNCH):
                    launches += 1
                    self.local_exec(*command, token=token)
                    return subprocess.CompletedProcess(
                        command, 1, b"", b"connection reset"
                    )
                if (
                    command[:1] == ("cat",)
                    and command[1].endswith("spark.exit")
                    and not failed_poll
                ):
                    failed_poll = True
                    return subprocess.CompletedProcess(
                        command, 1, b"", b"connection reset"
                    )
                return self.local_exec(*command, token=token)

            token = "literal-$HOME-$(false)"
            command = [
                sys.executable,
                "-c",
                "import os,pathlib,sys; pathlib.Path(sys.argv[1]).write_text(os.environ['OPENAI_API_KEY'])",
                str(output),
            ]
            with (
                mock.patch.object(runner, "exec", side_effect=execute),
                mock.patch.object(
                    runner, "pod_identity", return_value=("pod-id", ["container-id"])
                ),
                mock.patch.object(runner, "print_log"),
            ):
                self.assertEqual(runner.run(token, command, 10), 0)
            self.assertEqual(launches, 1)
            self.assertTrue(failed_poll)
            self.assertEqual(output.read_text(), token)
            state = json.loads(runner.state_file.read_text())
            self.assertEqual(state["phase"], "complete")
            self.assertNotIn(token, runner.state_file.read_text())

    def test_remote_exit_code_is_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = self.runner(Path(directory))
            with (
                mock.patch.object(runner, "exec", side_effect=self.local_exec),
                mock.patch.object(
                    runner, "pod_identity", return_value=("pod-id", ["container-id"])
                ),
                mock.patch.object(runner, "print_log"),
            ):
                self.assertEqual(
                    runner.run(
                        "test", [sys.executable, "-c", "raise SystemExit(7)"], 10
                    ),
                    7,
                )
            self.assertEqual(
                json.loads(runner.state_file.read_text())["phase"], "failed"
            )

    def test_cancellation_stops_the_owned_process_group(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runner = self.runner(root)
            child_file = root / "child.pid"
            command = [
                sys.executable,
                "-c",
                "import os,pathlib,sys,time; pathlib.Path(sys.argv[1]).write_text(str(os.getpid())); time.sleep(30)",
                str(child_file),
            ]
            with (
                mock.patch.object(runner, "exec", side_effect=self.local_exec),
                mock.patch.object(
                    runner, "pod_identity", return_value=("pod-id", ["container-id"])
                ),
            ):
                runner.exec(
                    "bash",
                    "-c",
                    LAUNCH,
                    "--",
                    runner.state["directory"],
                    WAIT,
                    runner.state["marker"],
                    runner.state["directory"],
                    "timeout",
                    "--foreground",
                    "10s",
                    *command,
                    token="test\n",
                )
                deadline = time.monotonic() + 5
                while not child_file.exists() and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertTrue(child_file.exists())
                runner.cancel()
                self.assertIsNone(runner.process_id())
            child = Path(f"/proc/{child_file.read_text()}/stat")
            self.assertTrue(
                not child.exists()
                or child.read_text().rsplit(") ", 1)[1].split()[0] == "Z"
            )

    def test_stale_pid_does_not_signal_an_unrelated_process(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = self.runner(Path(directory))
            unrelated = subprocess.Popen(["sleep", "30"], start_new_session=True)
            try:
                (Path(runner.state["directory"]) / "spark.pid").write_text(
                    str(unrelated.pid)
                )
                with (
                    mock.patch.object(runner, "exec", side_effect=self.local_exec),
                    mock.patch.object(
                        runner,
                        "pod_identity",
                        return_value=("pod-id", ["container-id"]),
                    ),
                ):
                    runner.cancel()
                self.assertIsNone(unrelated.poll())
            finally:
                os.killpg(unrelated.pid, signal.SIGTERM)
                unrelated.wait(timeout=5)

    def test_cancellation_prevents_a_delayed_workload_from_starting(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runner = self.runner(root)
            runner.state["directory"] = str(root / "not-created-yet")
            output = root / "started"
            with (
                mock.patch.object(runner, "exec", side_effect=self.local_exec),
                mock.patch.object(
                    runner, "pod_identity", return_value=("pod-id", ["container-id"])
                ),
            ):
                runner.cancel()
                result = self.local_exec(
                    "bash",
                    "-c",
                    WAIT,
                    runner.state["marker"],
                    runner.state["directory"],
                    sys.executable,
                    "-c",
                    "import pathlib,sys; pathlib.Path(sys.argv[1]).touch()",
                    str(output),
                )
            self.assertEqual(result.returncode, 143)
            self.assertFalse(output.exists())
            self.assertEqual(runner.state["phase"], "cancelled")

    def test_failed_launch_without_pid_can_be_reconciled(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = self.runner(Path(directory))
            clock = [0.0]
            launches = 0

            def execute(*command, token=None):
                nonlocal launches
                if command[:3] == ("bash", "-c", LAUNCH):
                    launches += 1
                    return subprocess.CompletedProcess(
                        command, 1, b"", b"launch transport unavailable"
                    )
                return self.local_exec(*command, token=token)

            def missing_exit_status():
                clock[0] += 31
                return None

            with (
                mock.patch.object(runner, "exec", side_effect=execute),
                mock.patch.object(runner, "exit_code", side_effect=missing_exit_status),
                mock.patch.object(
                    runner, "pod_identity", return_value=("pod-id", ["container-id"])
                ),
                mock.patch("pod_runner.time.monotonic", side_effect=lambda: clock[0]),
            ):
                with self.assertRaisesRegex(ValueError, "without an exit-status file"):
                    runner.run("unused", ["true"], 60)
            self.assertEqual(launches, 1)
            self.assertEqual(runner.state["phase"], "cancelled")

    def test_workload_cannot_start_if_its_pid_cannot_be_recorded(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runner = self.runner(root)
            (Path(runner.state["directory"]) / "spark.pid.tmp").mkdir()
            output = root / "started"
            result = self.local_exec(
                "bash",
                "-c",
                WAIT,
                runner.state["marker"],
                runner.state["directory"],
                sys.executable,
                "-c",
                "import pathlib,sys; pathlib.Path(sys.argv[1]).touch()",
                str(output),
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(output.exists())

    def test_failed_cancellation_fence_does_not_confirm_a_stop(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = self.runner(Path(directory))
            with (
                mock.patch.object(
                    runner,
                    "exec",
                    return_value=subprocess.CompletedProcess(
                        ["bash", "-c", CANCEL],
                        1,
                        b"",
                        b"cannot write cancellation marker",
                    ),
                ),
                mock.patch.object(
                    runner, "pod_identity", return_value=("pod-id", ["container-id"])
                ),
                mock.patch("pod_runner.time.sleep"),
            ):
                with self.assertRaisesRegex(RuntimeError, "Cannot confirm"):
                    runner.cancel()
            self.assertEqual(runner.state["phase"], "cancel-unconfirmed")

    def test_legacy_unstarted_launch_still_requires_reconciliation(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = self.runner(Path(directory))
            runner.state.pop("protocolVersion")
            with (
                mock.patch.object(runner, "exec") as execute,
                mock.patch.object(runner, "process_id", return_value=None),
                mock.patch.object(runner, "read_file", return_value=None),
                mock.patch.object(
                    runner, "pod_identity", return_value=("pod-id", ["container-id"])
                ),
                mock.patch("pod_runner.time.sleep"),
            ):
                with self.assertRaisesRegex(RuntimeError, "Cannot confirm"):
                    runner.cancel()
                execute.assert_not_called()
            self.assertEqual(runner.state["phase"], "cancel-unconfirmed")


if __name__ == "__main__":
    unittest.main()

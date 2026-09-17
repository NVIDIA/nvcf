#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import json
import os
from pathlib import Path
import shlex
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

    def wait_for_pid(self, path):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if path.exists() and (value := path.read_text().strip()):
                return int(value)
            time.sleep(0.01)
        self.fail(f"Process did not publish its PID: {path}")

    @staticmethod
    def cleanup_group(runner):
        path = Path(runner.state["directory"]) / "spark.pid"
        if path.exists():
            try:
                os.killpg(int(path.read_text()), signal.SIGKILL)
            except ProcessLookupError:
                pass

    def test_process_scan_skips_only_files_that_disappear_during_read(self):
        for failure in ("disappear", "unreadable"):
            with (
                self.subTest(failure=failure),
                tempfile.TemporaryDirectory() as directory,
            ):
                root = Path(directory)
                runner = self.runner(root)
                proc = root / "proc"
                for pid, content in ((1, failure), (2, "2 (worker) S 1 42 42\n")):
                    path = proc / str(pid) / "stat"
                    path.parent.mkdir(parents=True)
                    path.write_text(content)
                tools = root / "tools"
                tools.mkdir()
                cat = tools / "cat"
                cat.write_text(
                    f"#!{sys.executable}\n"
                    "from pathlib import Path\n"
                    "import sys\n"
                    "path = Path(sys.argv[1])\n"
                    "content = path.read_text()\n"
                    "if path.parent.name == '1':\n"
                    "    sys.stdout.write('partial stat record')\n"
                    "    if content == 'disappear':\n"
                    "        path.unlink()\n"
                    "    sys.stderr.write('stat read failed\\n')\n"
                    "    raise SystemExit(1)\n"
                    "sys.stdout.write(content)\n"
                )
                cat.chmod(0o700)

                def execute(*command):
                    script = command[2].replace(
                        "/proc/[0-9]*/stat", shlex.quote(str(proc)) + "/[0-9]*/stat"
                    )
                    return subprocess.run(
                        ["bash", "-c", script],
                        capture_output=True,
                        timeout=5,
                        env={
                            **os.environ,
                            "PATH": str(tools) + os.pathsep + os.environ["PATH"],
                        },
                    )

                with mock.patch.object(runner, "exec", side_effect=execute):
                    if failure == "disappear":
                        self.assertTrue(runner.process_group_running(42))
                    else:
                        with self.assertRaisesRegex(RuntimeError, "stat read failed"):
                            runner.process_group_running(42)

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

    def test_cancellation_stops_the_owned_group_even_if_it_ignores_term(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runner = self.runner(root)
            child_file = root / "child.pid"
            command = [
                sys.executable,
                "-c",
                "import os,pathlib,signal,sys,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); pathlib.Path(sys.argv[1]).write_text(str(os.getpid())); time.sleep(30)",
                str(child_file),
            ]
            with (
                mock.patch.object(runner, "exec", side_effect=self.local_exec),
                mock.patch.object(
                    runner, "pod_identity", return_value=("pod-id", ["container-id"])
                ),
            ):
                try:
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
                        "--kill-after=10s",
                        "10s",
                        *command,
                        token="test\n",
                    )
                    child_pid = self.wait_for_pid(child_file)
                    runner.cancel()
                    self.assertIsNone(runner.process_id())
                    child = Path(f"/proc/{child_pid}/stat")
                    self.assertTrue(
                        not child.exists()
                        or child.read_text().rsplit(") ", 1)[1].split()[0] == "Z"
                    )
                finally:
                    self.cleanup_group(runner)

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
                    with self.assertRaisesRegex(RuntimeError, "Cannot confirm"):
                        runner.cancel()
                self.assertIsNone(unrelated.poll())
                self.assertEqual(runner.state["phase"], "cancel-unconfirmed")
            finally:
                os.killpg(unrelated.pid, signal.SIGTERM)
                unrelated.wait(timeout=5)

    def test_replacement_during_launch_still_stops_the_launched_group(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runner = self.runner(root)
            child_file = root / "child.pid"
            identity = ("pod-id", ["container-id"])

            def execute(*command, token=None):
                nonlocal identity
                if command[:3] == ("bash", "-c", LAUNCH):
                    identity = ("replacement", ["replacement-container"])
                    result = self.local_exec(*command, token=token)
                    self.wait_for_pid(child_file)
                    return result
                return self.local_exec(*command, token=token)

            command = [
                sys.executable,
                "-c",
                "import os,pathlib,sys,time; pathlib.Path(sys.argv[1]).write_text(str(os.getpid())); time.sleep(30)",
                str(child_file),
            ]
            with (
                mock.patch.object(runner, "exec", side_effect=execute),
                mock.patch.object(runner, "pod_identity", side_effect=lambda: identity),
                mock.patch.object(runner, "print_log"),
            ):
                try:
                    with self.assertRaisesRegex(ValueError, "container changed"):
                        runner.run("test", command, 10)
                    pid = int(
                        (Path(runner.state["directory"]) / "spark.pid").read_text()
                    )
                    self.assertFalse(runner.process_group_running(pid))
                    self.assertTrue(
                        (Path(runner.state["directory"]) / "spark.cancelled").exists()
                    )
                    self.assertEqual(runner.state["phase"], "cancelled")
                finally:
                    self.cleanup_group(runner)

    def test_missing_wrapper_does_not_prove_its_children_stopped(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runner = self.runner(root)
            runner.state.pop("protocolVersion")
            child_file = root / "child.pid"
            with (
                mock.patch.object(runner, "exec", side_effect=self.local_exec),
                mock.patch.object(
                    runner, "pod_identity", return_value=("pod-id", ["container-id"])
                ),
            ):
                try:
                    runner.exec(
                        "bash",
                        "-c",
                        LAUNCH,
                        "--",
                        runner.state["directory"],
                        WAIT,
                        runner.state["marker"],
                        runner.state["directory"],
                        sys.executable,
                        "-c",
                        "import os,pathlib,sys,time; pathlib.Path(sys.argv[1]).write_text(str(os.getpid())); time.sleep(30)",
                        str(child_file),
                        token="test\n",
                    )
                    child = self.wait_for_pid(child_file)
                    pid = runner.process_id()
                    self.assertIsNotNone(pid)
                    os.kill(pid, signal.SIGKILL)
                    deadline = time.monotonic() + 5
                    while (
                        runner.process_id() is not None and time.monotonic() < deadline
                    ):
                        time.sleep(0.01)
                    self.assertIsNone(runner.process_id())
                    with self.assertRaisesRegex(RuntimeError, "Cannot confirm"):
                        runner.cancel()
                    self.assertEqual(runner.state["phase"], "cancel-unconfirmed")
                    self.assertNotEqual(
                        Path(f"/proc/{child}/stat")
                        .read_text()
                        .rsplit(") ", 1)[1]
                        .split()[0],
                        "Z",
                    )
                finally:
                    self.cleanup_group(runner)

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

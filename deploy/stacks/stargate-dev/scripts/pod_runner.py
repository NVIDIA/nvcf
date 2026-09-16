#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import argparse
import json
import signal
import subprocess
import sys
import time
import uuid
from pathlib import Path

# Keep shell use to process-launch/status primitives; orchestration stays in Python.
LAUNCH = 'IFS= read -r OPENAI_API_KEY || exit 1; export OPENAI_API_KEY; run_dir=$1; shift; nohup setsid bash -c "$@" > "$run_dir/spark.log" 2>&1 < /dev/null &'
WAIT = 'run_dir=$1; shift; exec 9>"$run_dir/spark.lock" || exit 1; flock -x -w 5 9 || exit 1; if test -e "$run_dir/spark.cancelled"; then exit 143; fi; printf "%s\\n" "$$" > "$run_dir/spark.pid.tmp" && mv "$run_dir/spark.pid.tmp" "$run_dir/spark.pid" || exit 1; flock -u 9; exec 9>&-; "$@"; run_status=$?; printf "%s\\n" "$run_status" > "$run_dir/spark.exit.tmp"; mv "$run_dir/spark.exit.tmp" "$run_dir/spark.exit"; exit "$run_status"'
CANCEL = 'run_dir=$1; mkdir -p "$run_dir" || exit 1; exec 9>"$run_dir/spark.lock" || exit 1; flock -x -w 5 9 || exit 1; : > "$run_dir/spark.cancelled"'
STOP = 'if tr "\\0" "\\n" < "/proc/$1/cmdline" | grep -Fxq -- "$2"; then kill -TERM -- "-$1"; fi'


class PodRunner:
    def __init__(self, state_file: Path, state: dict):
        self.state_file = state_file
        self.state = state
        self.prefix = [
            "kubectl",
            "--context",
            state["context"],
            "--request-timeout=20s",
            "-n",
            state["namespace"],
        ]
        self.log_offset = 1

    def save(self, **updates) -> None:
        self.state.update(updates)
        self.state_file.parent.mkdir(parents=True, exist_ok=True)
        temporary = self.state_file.with_suffix(".tmp")
        temporary.write_text(json.dumps(self.state, indent=2) + "\n")
        temporary.replace(self.state_file)

    def exec(self, *command: str, token: str | None = None):
        return subprocess.run(
            [
                *self.prefix,
                "exec",
                *(["-i"] if token is not None else []),
                self.state["pod"],
                "--",
                *command,
            ],
            input=token.encode() if token is not None else None,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=30,
        )

    def pod_identity(self) -> tuple[str, list[str]] | None:
        result = subprocess.run(
            [
                *self.prefix,
                "get",
                "pod",
                self.state["pod"],
                "--ignore-not-found",
                "-o",
                "json",
            ],
            text=True,
            capture_output=True,
            timeout=30,
            check=True,
        )
        if not result.stdout.strip():
            return None
        pod = json.loads(result.stdout)
        statuses = pod["status"].get("containerStatuses", [])
        if not statuses or any("running" not in c["state"] for c in statuses):
            return None
        return pod["metadata"]["uid"], sorted(c["containerID"] for c in statuses)

    def read_file(self, path: str) -> bytes | None:
        result = self.exec("cat", path)
        if result.returncode:
            error = result.stderr.decode(errors="replace")
            if "No such file or directory" in error:
                return None
            raise RuntimeError(error.strip() or "Cannot read remote Spark state")
        return result.stdout

    def process_id(self) -> int | None:
        value = self.read_file(f"{self.state['directory']}/spark.pid")
        if value is None:
            return None
        pid = int(value)
        if pid <= 1:
            raise RuntimeError("Invalid detached Spark process ID")
        command = self.read_file(f"/proc/{pid}/cmdline")
        if command is None or self.state["marker"].encode() not in command.split(b"\0"):
            return None
        stat = self.read_file(f"/proc/{pid}/stat")
        if not stat or b") " not in stat:
            return None
        fields = stat.rsplit(b") ", 1)[1].split()
        if fields[0] == b"Z":
            return None
        if int(fields[2]) != pid:
            raise RuntimeError("Detached Spark process group is not ready")
        return pid

    def exit_code(self) -> int | None:
        value = self.read_file(f"{self.state['directory']}/spark.exit")
        if value is not None:
            code = int(value)
            if not 0 <= code <= 255:
                raise ValueError("Spark returned an invalid exit status")
            return code
        return None

    def print_log(self) -> None:
        result = self.exec(
            "tail", "-c", f"+{self.log_offset}", f"{self.state['directory']}/spark.log"
        )
        if result.returncode == 0:
            sys.stdout.buffer.write(result.stdout)
            sys.stdout.buffer.flush()
            self.log_offset += len(result.stdout)

    def cancel(self) -> None:
        for attempt in range(3):
            try:
                identity = self.pod_identity()
                if identity is None or list(identity) != self.state.get("podIdentity"):
                    self.save(
                        phase="cancelled",
                        reason="original Spark container no longer exists",
                    )
                    return
                fenced = self.state.get("protocolVersion", 1) == 2
                if fenced:
                    # Serialize cancellation with PID publication so a delayed
                    # launcher cannot start work after cancellation is confirmed.
                    result = self.exec(
                        "bash", "-c", CANCEL, "--", self.state["directory"]
                    )
                    if result.returncode:
                        raise RuntimeError(
                            result.stderr.decode(errors="replace").strip()
                            or "Cannot prevent a delayed Spark launch"
                        )
                pid = self.process_id()
                if pid is None:
                    if (
                        fenced
                        or self.read_file(f"{self.state['directory']}/spark.pid")
                        is not None
                        or self.exit_code() is not None
                    ):
                        self.save(phase="cancelled")
                        return
                    time.sleep(1)
                    continue
                result = self.exec(
                    "bash", "-c", STOP, "--", str(pid), self.state["marker"]
                )
                if result.returncode:
                    raise RuntimeError(result.stderr.decode(errors="replace").strip())
                time.sleep(1)
                if self.process_id() is None:
                    self.save(phase="cancelled")
                    return
            except (RuntimeError, subprocess.SubprocessError, OSError):
                time.sleep(1)
        self.save(phase="cancel-unconfirmed")
        raise RuntimeError(
            "Cannot confirm detached Spark stopped; reconcile this run before starting more traffic"
        )

    def run(self, token: str, command: list[str], timeout: int) -> int:
        identity = self.pod_identity()
        if identity is None:
            raise RuntimeError("Spark Pod is not running")
        self.save(
            phase="starting",
            protocolVersion=2,
            podIdentity=list(identity),
            command=command,
        )
        created = self.exec("mkdir", "-p", self.state["directory"])
        if created.returncode:
            raise RuntimeError(created.stderr.decode(errors="replace").strip())
        finished = False
        try:
            try:
                launched = self.exec(
                    "bash",
                    "-c",
                    LAUNCH,
                    "--",
                    self.state["directory"],
                    WAIT,
                    self.state["marker"],
                    self.state["directory"],
                    "timeout",
                    "--foreground",
                    "--signal=TERM",
                    "--kill-after=10s",
                    f"{timeout}s",
                    *command,
                    token=token + "\n",
                )
                acknowledged = launched.returncode == 0
                launch_error = launched.stderr.decode(errors="replace").strip()
                if not launch_error:
                    launch_error = f"exit status {launched.returncode}"
            except (OSError, subprocess.SubprocessError) as error:
                acknowledged = False
                launch_error = str(error)
            if not acknowledged:
                # A lost acknowledgement does not prove the launch failed. Never launch twice.
                print(
                    "Launch acknowledgement unavailable; checking the existing run: "
                    + launch_error.replace(token, "[REDACTED]"),
                    file=sys.stderr,
                )
            self.save(phase="running")
            started = time.monotonic()
            deadline = started + timeout + 30
            checked_identity_at = 0.0
            checked_process_at = started
            while time.monotonic() < deadline:
                try:
                    if time.monotonic() - checked_identity_at >= 30:
                        if self.pod_identity() != identity:
                            raise ValueError("Spark container changed during the run")
                        checked_identity_at = time.monotonic()
                    code = self.exit_code()
                    if code is None and time.monotonic() - checked_process_at >= 30:
                        if self.process_id() is None:
                            code = self.exit_code()
                            if code is None:
                                raise ValueError(
                                    "Detached Spark exited without an exit-status file"
                                )
                        checked_process_at = time.monotonic()
                    self.print_log()
                except (RuntimeError, subprocess.SubprocessError, OSError) as error:
                    print(
                        f"Control read failed; Spark remains independent: {error}",
                        file=sys.stderr,
                        flush=True,
                    )
                    time.sleep(2)
                    continue
                if code is not None:
                    self.save(
                        phase="complete" if code == 0 else "failed", exitCode=code
                    )
                    finished = True
                    return code
                time.sleep(2)
            raise RuntimeError("Timed out waiting for detached Spark completion")
        finally:
            if not finished:
                self.cancel()


def reconcile(directory: Path) -> None:
    for path in directory.glob("*.json"):
        state = json.loads(path.read_text())
        if state["phase"] not in {"complete", "failed", "cancelled"}:
            PodRunner(path, state).cancel()


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Run Spark independently of the kubectl exec connection"
    )
    commands = parser.add_subparsers(dest="mode", required=True)
    cleanup = commands.add_parser("reconcile")
    cleanup.add_argument("directory", type=Path)
    run = commands.add_parser("run")
    run.add_argument("--context", required=True)
    run.add_argument("--namespace", required=True)
    run.add_argument("--pod", required=True)
    run.add_argument("--directory", required=True)
    run.add_argument("--state-file", type=Path, required=True)
    run.add_argument("--timeout-seconds", type=int, required=True)
    run.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.mode == "reconcile":
        reconcile(args.directory)
        return 0
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if any(value == "--api-key" or value.startswith("--api-key=") for value in command):
        parser.error("supply the API key on stdin, not in command arguments")
    if args.timeout_seconds <= 0:
        parser.error("timeout must be positive")
    if not command or command[0] != "spark":
        parser.error("the remote command must start with spark")
    marker = "spark-run-" + uuid.uuid4().hex
    state = {
        "context": args.context,
        "namespace": args.namespace,
        "pod": args.pod,
        "directory": str(Path(args.directory) / marker),
        "marker": marker,
    }
    runner = PodRunner(args.state_file, state)
    token = sys.stdin.readline().rstrip("\n")
    if not token:
        parser.error("a nonempty API key must be supplied on stdin")

    def interrupt(_signal, _frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupt)
    try:
        return runner.run(token, command, args.timeout_seconds)
    except KeyboardInterrupt:
        return 130
    except (RuntimeError, ValueError, OSError, subprocess.SubprocessError) as error:
        print(f"Spark Pod runner failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())

#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import argparse
import base64
import hashlib
import json
import math
import os
import re
import shlex
import shutil
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable

import yaml


STACK_DIR = Path(__file__).resolve().parents[1]
SUITE_PATH = STACK_DIR / "loadtest" / "suite.yaml"
VERIFY_SCRIPT = STACK_DIR / "scripts" / "verify.py"
ALGORITHM_ORDER = ("wait-and-widen", "power-of-n")
ANSI_ESCAPE = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


class LoadTestError(RuntimeError):
    pass


def utc_now() -> str:
    return (
        datetime.now(timezone.utc)
        .replace(microsecond=0)
        .isoformat()
        .replace("+00:00", "Z")
    )


def atomic_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(
        json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    temporary.replace(path)


def load_yaml(path: Path) -> dict:
    with path.open(encoding="utf-8") as stream:
        value = yaml.safe_load(stream)
    if not isinstance(value, dict):
        raise LoadTestError(f"{path} must contain a YAML object")
    return value


def load_suite(path: Path = SUITE_PATH) -> dict:
    suite = load_yaml(path)
    if suite.get("version") != 1:
        raise LoadTestError("load-test suite version must be 1")
    for key in ("defaults", "algorithms", "suites", "scenarios"):
        if not isinstance(suite.get(key), dict) or not suite[key]:
            raise LoadTestError(f"suite must define {key}")
    valid_kinds = {
        "requests",
        "sweep",
        "session-affinity",
        "session-workers",
        "mixed-sessions",
    }
    for name, scenario in suite["scenarios"].items():
        if not isinstance(scenario, dict) or scenario.get("kind") not in valid_kinds:
            raise LoadTestError(f"scenario {name} has an invalid kind")
    for name, scenarios in suite["suites"].items():
        unknown = set(scenarios) - set(suite["scenarios"])
        if not scenarios or unknown:
            raise LoadTestError(
                f"suite {name} has invalid scenarios: {sorted(unknown)}"
            )
    return suite


def load_region(region: str) -> dict:
    path = STACK_DIR / "environments" / f"{region}.yaml"
    if not path.is_file():
        raise LoadTestError(f"unsupported region: {region}")
    config = load_yaml(path)
    if config.get("region") != region:
        raise LoadTestError(f"{path} region does not match {region}")
    if len(config.get("clusters", {}).get("mockdcs", [])) != 2:
        raise LoadTestError(f"{path} must define two MockDC clusters")
    return config


def fixed_size_text(prefix: str, phrase: str, size: int) -> str:
    if len(prefix) > size:
        raise LoadTestError(f"prefix exceeds requested prompt size {size}")
    repeats = (size - len(prefix) + len(phrase) - 1) // len(phrase)
    return (prefix + phrase * repeats)[:size]


def unique_prompts(count: int, size: int) -> Iterable[str]:
    for index in range(count):
        yield fixed_size_text(
            f"canonical-unique-session={index:08d}; ",
            "Unique request context that must not share a cache affinity key. ",
            size,
        )


def session_prompts(
    sessions: int, turns: int, stable_prefix_bytes: int, turn_bytes: int
) -> Iterable[str]:
    histories = [
        fixed_size_text(
            f"canonical-session={session:08d}; ",
            "Stable system and document context for this conversation. ",
            stable_prefix_bytes,
        )
        for session in range(sessions)
    ]
    for turn in range(turns):
        for session, history in enumerate(histories):
            histories[session] = history + fixed_size_text(
                f" turn={turn:04d}; ",
                "New information appended during this turn. ",
                turn_bytes,
            )
            yield histories[session]


def session_worker_prompts(
    workers: int, session_tasks: list[list[int]]
) -> Iterable[str]:
    def prompts_for_worker(worker: int) -> Iterable[str]:
        offset = worker % len(session_tasks)
        ordered_tasks = session_tasks[offset:] + session_tasks[:offset]
        for task, input_tokens_by_turn in enumerate(ordered_tasks):
            session_id = hashlib.sha256(
                f"canonical-session-worker={worker};task={task}".encode()
            ).hexdigest()
            history = fixed_size_text(
                f"canonical-session={session_id}; ",
                "Stable repository and coding context. ",
                256,
            )
            for input_tokens in input_tokens_by_turn:
                history = fixed_size_text(
                    history,
                    " Additional source code, build output, and conversation context. ",
                    (input_tokens - 5) * 4,
                )
                yield history

    worker_prompts = [prompts_for_worker(worker) for worker in range(workers)]
    for prompts in zip(*worker_prompts, strict=True):
        yield from prompts


def mixed_hot_prompts(count: int, minimum: int, maximum: int) -> Iterable[str]:
    history = fixed_size_text(
        "canonical-hot-session; ",
        "Stable conversation identity retained across every turn. ",
        256,
    )
    for index in range(count):
        size = minimum + (maximum - minimum) * index // max(1, count - 1)
        history = fixed_size_text(
            history,
            f" Long-session turn {index:05d} adds durable conversation history. ",
            size,
        )
        yield history


def mixed_short_prompts(count: int, sizes: list[int]) -> Iterable[str]:
    emitted = 0
    session = 0
    while emitted < count:
        history = fixed_size_text(
            f"canonical-short-session={session:08d}; ",
            "Stable short conversation identity. ",
            256,
        )
        for turn in range(1 + session % len(sizes)):
            if emitted == count:
                return
            history = fixed_size_text(
                history,
                f" Short-session turn {turn + 1} contains a user exchange. ",
                sizes[turn],
            )
            yield history
            emitted += 1
        session += 1


def write_workload(path: Path, prompts: Iterable[str], kind: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(".yaml.tmp")
    count = 0
    with temporary.open("w", encoding="utf-8") as stream:
        stream.write(
            "version: 1\nscenarios:\n  - name: normal\n    mode: normal\n    prompts:\n"
        )
        for prompt in prompts:
            stream.write(f"      - {json.dumps(prompt)}\n")
            count += 1
    temporary.replace(path)
    atomic_json(
        path.with_suffix(".json"),
        {
            "kind": kind,
            "promptCount": count,
            "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
        },
    )


@dataclass
class SparkRun:
    directory: Path
    command: list[str]
    expected_seconds: float
    metadata: dict


class Campaign:
    def __init__(self, args: argparse.Namespace, suite: dict):
        self.args = args
        self.suite = suite
        self.region = load_region(args.region)
        self.output = args.output.expanduser().resolve()
        self.runs = self.output / "runs"
        self.workload_dir = self.output / "workloads"
        self.namespace = self.region["namespace"]
        self.stargate_context = self.region["clusters"]["stargate"]["kubeContext"]
        self.endpoint = args.endpoint or (
            "http://llm-request-router:8000/v1"
            if args.spark_pod
            else f"http://127.0.0.1:{args.local_port}/v1"
        )
        self.algorithms = (
            list(ALGORITHM_ORDER) if args.algorithm == "both" else [args.algorithm]
        )
        self.scenarios = suite["suites"][args.suite]
        self.identity = {
            "region": args.region,
            "suite": args.suite,
            "suiteSha256": hashlib.sha256(SUITE_PATH.read_bytes()).hexdigest(),
            "sparkImage": args.spark_image,
            "algorithms": self.algorithms,
            "endpoint": self.endpoint
            if args.spark_pod
            else args.endpoint or "kubectl-port-forward",
            "sparkPod": args.spark_pod,
        }
        self.children: list[subprocess.Popen] = []
        self.port_forward: subprocess.Popen | None = None
        self.port_forward_log = None
        self.token = ""
        self.workloads: dict[str, dict[str, Path]] = {}
        self.state = self.prepare_output()
        self.save_state()

    def prepare_output(self) -> dict:
        state_path = self.output / "campaign-state.json"
        if state_path.exists():
            if not self.args.resume:
                raise LoadTestError(
                    f"{self.output} already has a campaign; pass --resume to continue"
                )
            state = json.loads(state_path.read_text(encoding="utf-8"))
            if state.get("identity") != self.identity:
                raise LoadTestError(
                    "resume arguments do not match the existing campaign"
                )
            state["status"] = "initializing"
            state.pop("error", None)
            return state
        if self.output.exists() and any(self.output.iterdir()):
            raise LoadTestError(f"output directory is not empty: {self.output}")
        self.output.mkdir(parents=True, exist_ok=True)
        return {
            "identity": self.identity,
            "startedAt": utc_now(),
            "status": "initializing",
        }

    def save_state(self) -> None:
        atomic_json(self.output / "campaign-state.json", self.state)

    def log(self, message: str) -> None:
        line = f"{utc_now()} {message}"
        print(line, flush=True)
        with (self.output / "campaign.log").open("a", encoding="utf-8") as stream:
            stream.write(line + "\n")

    def command(
        self,
        command: list[str],
        *,
        timeout: float | None = 120,
        check: bool = True,
        env: dict[str, str] | None = None,
    ) -> subprocess.CompletedProcess[str]:
        result = subprocess.run(
            command,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            timeout=timeout,
            check=False,
            env=env,
        )
        if check and result.returncode:
            raise LoadTestError(
                f"command failed ({result.returncode}): {shlex.join(command)}\n{result.stdout}"
            )
        return result

    def kubectl(
        self, context: str, arguments: list[str], *, timeout: float = 120
    ) -> str:
        return self.command(
            ["kubectl", "--context", context, "-n", self.namespace, *arguments],
            timeout=timeout,
        ).stdout

    def verify_region(self) -> None:
        command = [
            "python3",
            str(VERIFY_SCRIPT),
            "--region",
            self.args.region,
            "--phase",
            "regional",
        ]
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            result = self.command(command, timeout=360, check=False)
            if result.returncode == 0:
                return
            time.sleep(5)
        raise LoadTestError(f"regional health did not converge:\n{result.stdout}")

    def initialize(self) -> None:
        missing = [name for name in ("docker", "kubectl") if not shutil.which(name)]
        if missing:
            raise LoadTestError(f"required commands not found: {', '.join(missing)}")
        image = json.loads(
            self.command(["docker", "image", "inspect", self.args.spark_image]).stdout
        )[0]
        self.verify_region()
        self.token = self.load_token()
        self.snapshot_environment(image)
        self.prepare_workloads()
        if self.args.spark_pod:
            self.prepare_spark_pod()
        elif not self.args.endpoint:
            self.start_port_forward()
        self.state["status"] = "running"
        self.save_state()

    @property
    def spark_directory(self) -> Path:
        root = Path("/campaign")
        return root / self.output.name if self.args.spark_pod else root

    def pod_exec(self, *command: str, stdin: bool = False) -> list[str]:
        return [
            "kubectl",
            "--context",
            self.stargate_context,
            "-n",
            self.namespace,
            "exec",
            *(["-i"] if stdin else []),
            self.args.spark_pod,
            "--",
            *command,
        ]

    def prepare_spark_pod(self) -> None:
        pod = json.loads(
            self.kubectl(
                self.stargate_context, ["get", "pod", self.args.spark_pod, "-o", "json"]
            )
        )
        atomic_json(self.output / "spark-pod.json", pod)
        local_version = self.command(
            ["docker", "run", "--rm", self.args.spark_image, "--version"]
        ).stdout.strip()
        remote_version = self.command(
            self.pod_exec("spark", "--version")
        ).stdout.strip()
        if local_version != remote_version:
            raise LoadTestError("local and remote Spark versions differ")
        self.command(self.pod_exec("mkdir", "-p", str(self.spark_directory)))
        with tempfile.TemporaryFile() as archive:
            with tarfile.open(fileobj=archive, mode="w:gz") as stream:
                stream.add(self.workload_dir, arcname="workloads")
            archive.seek(0)
            result = subprocess.run(
                self.pod_exec(
                    "tar", "-xzf", "-", "-C", str(self.spark_directory), stdin=True
                ),
                stdin=archive,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=300,
            )
        if result.returncode:
            raise LoadTestError(f"workload upload failed: {result.stderr.decode()}")
        for workload in self.workload_dir.glob("*.yaml"):
            expected = json.loads(workload.with_suffix(".json").read_text())["sha256"]
            actual = self.command(
                self.pod_exec(
                    "sha256sum", str(self.spark_directory / "workloads" / workload.name)
                )
            ).stdout.split()[0]
            if actual != expected:
                raise LoadTestError(
                    f"remote workload fingerprint differs: {workload.name}"
                )

    def download_report(self, run: SparkRun) -> None:
        directory = self.spark_directory / run.directory.relative_to(self.output)
        with tempfile.TemporaryFile() as archive:
            result = subprocess.run(
                self.pod_exec("tar", "-czf", "-", "-C", str(directory), "spark.json"),
                stdout=archive,
                stderr=subprocess.PIPE,
                timeout=300,
            )
            if result.returncode:
                raise LoadTestError(f"report download failed: {result.stderr.decode()}")
            archive.seek(0)
            with tarfile.open(fileobj=archive, mode="r:gz") as stream:
                report = stream.extractfile("spark.json")
                if report is None:
                    raise LoadTestError("remote Spark report is missing")
                with (run.directory / "spark.json").open("wb") as output:
                    shutil.copyfileobj(report, output)

    def load_token(self) -> str:
        if token := os.environ.get("OPENAI_API_KEY"):
            return token
        encoded = self.kubectl(
            self.stargate_context,
            [
                "get",
                "secret",
                "stargate-dev-auth-credentials",
                "-o",
                "jsonpath={.data.config\\.json}",
            ],
        ).strip()
        try:
            config = json.loads(base64.b64decode(encoded, validate=True))
        except (ValueError, json.JSONDecodeError) as error:
            raise LoadTestError("dev auth Secret contains invalid config") from error
        token = config.get("clientToken")
        if not isinstance(token, str) or not token:
            raise LoadTestError("dev auth Secret does not contain clientToken")
        return token

    def backend_targets(self) -> list[tuple[str, str]]:
        return [
            (
                mockdc["kubeContext"],
                f"{mockdc['name']}-stargate-dev-mockdc-backend-{index}",
            )
            for mockdc in self.region["clusters"]["mockdcs"]
            for index in range(2)
        ]

    def snapshot_environment(self, image: dict) -> None:
        snapshots = {}
        for cluster in [
            self.region["clusters"]["stargate"],
            *self.region["clusters"]["mockdcs"],
        ]:
            context = cluster["kubeContext"]
            snapshots[context] = json.loads(
                self.kubectl(context, ["get", "deployment", "-o", "json"])
            )
        load_balancer = json.loads(
            self.kubectl(
                self.stargate_context,
                ["get", "configmap", "llm-request-router-lb", "-o", "json"],
            )
        )
        atomic_json(
            self.output / "environment.json",
            {
                "capturedAt": utc_now(),
                "loadBalancerConfig": json.loads(
                    load_balancer["data"]["lb-config.json"]
                ),
                "deployments": snapshots,
                "sparkImage": {
                    "id": image.get("Id"),
                    "repoDigests": image.get("RepoDigests", []),
                    "repoTags": image.get("RepoTags", []),
                },
            },
        )

    def start_port_forward(self) -> None:
        with socket.socket() as probe:
            probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                probe.bind(("127.0.0.1", self.args.local_port))
            except OSError as error:
                raise LoadTestError(
                    f"local port {self.args.local_port} is already in use"
                ) from error
        self.port_forward_log = (self.output / "port-forward.log").open(
            "a", encoding="utf-8"
        )
        environment = os.environ.copy()
        environment["KUBECTL_PORT_FORWARD_WEBSOCKETS"] = "true"
        self.port_forward = subprocess.Popen(
            [
                "kubectl",
                "--context",
                self.stargate_context,
                "-n",
                self.namespace,
                "port-forward",
                "--address",
                "127.0.0.1",
                "service/llm-request-router",
                f"{self.args.local_port}:8000",
            ],
            stdout=self.port_forward_log,
            stderr=subprocess.STDOUT,
            env=environment,
        )
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if self.port_forward.poll() is not None:
                raise LoadTestError("Stargate port-forward exited during startup")
            try:
                with socket.create_connection(
                    ("127.0.0.1", self.args.local_port), timeout=1
                ):
                    self.log(f"Stargate is available at {self.endpoint}")
                    return
            except OSError:
                time.sleep(1)
        raise LoadTestError("timed out waiting for Stargate port-forward")

    def stop_port_forward(self) -> None:
        if self.port_forward is not None:
            self.stop_processes([self.port_forward])
            self.port_forward = None
        if self.port_forward_log is not None:
            self.port_forward_log.close()
            self.port_forward_log = None

    def prepare_workloads(self) -> None:
        for name in self.scenarios:
            scenario = self.suite["scenarios"][name]
            kind = scenario["kind"]
            if kind == "requests":
                path = self.workload_dir / f"{name}.yaml"
                write_workload(
                    path,
                    unique_prompts(scenario["requests"], scenario["promptBytes"]),
                    kind,
                )
                self.workloads[name] = {"main": path}
            elif kind == "sweep":
                count = math.ceil(
                    max(scenario["rates"]) * scenario["durationSeconds"] * 1.05
                )
                path = self.workload_dir / f"{name}.yaml"
                write_workload(
                    path, unique_prompts(count, scenario["promptBytes"]), kind
                )
                self.workloads[name] = {"main": path}
            elif kind == "session-affinity":
                path = self.workload_dir / f"{name}.yaml"
                write_workload(
                    path,
                    session_prompts(
                        scenario["sessions"],
                        scenario["turnsPerSession"],
                        scenario["stablePrefixBytes"],
                        scenario["turnBytes"],
                    ),
                    kind,
                )
                self.workloads[name] = {"main": path}
            elif kind == "session-workers":
                path = self.workload_dir / f"{name}.yaml"
                write_workload(
                    path,
                    session_worker_prompts(
                        scenario["workers"], scenario["sessionTasks"]
                    ),
                    kind,
                )
                self.workloads[name] = {"main": path}
            elif kind == "mixed-sessions":
                duration = scenario["durationSeconds"]
                hot = scenario["hot"]
                short = scenario["short"]
                hot_path = self.workload_dir / f"{name}-hot.yaml"
                short_path = self.workload_dir / f"{name}-short.yaml"
                write_workload(
                    hot_path,
                    mixed_hot_prompts(
                        math.ceil(duration * hot["rate"] * 1.05),
                        hot["minPromptBytes"],
                        hot["maxPromptBytes"],
                    ),
                    "mixed-hot",
                )
                write_workload(
                    short_path,
                    mixed_short_prompts(
                        math.ceil(duration * short["rate"] * 1.05),
                        short["promptBytesByTurn"],
                    ),
                    "mixed-short",
                )
                self.workloads[name] = {"hot": hot_path, "short": short_path}

    def spark_run(
        self,
        relative: Path,
        algorithm: str,
        *,
        scenario: str,
        rate: int,
        workers: int,
        workload: Path,
        duration: int | None = None,
        requests: int | None = None,
        measured: bool = True,
        request_slo_ms: int | None = None,
        max_wait_ms: int | None = None,
        timeout_seconds: int | None = None,
    ) -> SparkRun:
        if (duration is None) == (requests is None):
            raise LoadTestError("Spark run must set duration or requests")
        directory = self.runs / relative
        defaults = self.suite["defaults"]
        command = [
            "docker",
            "run",
            "--rm",
            "--network",
            "host",
            "-e",
            "OPENAI_API_KEY",
            "-v",
            f"{self.output}:/campaign",
            self.args.spark_image,
            "--endpoint",
            self.endpoint,
            "--model",
            self.region["modelName"],
            "--stargate-routing-key",
            self.region["routingKey"],
            "--stargate-max-wait-ms",
            str(max_wait_ms or defaults["maxWaitMs"]),
            "--stargate-request-slo-ms",
            str(request_slo_ms or defaults["requestSloMs"]),
            "--workers",
            str(workers),
            "--rate-limit",
            str(rate),
            "--timeout",
            f"{timeout_seconds or defaults['timeoutSeconds']}s",
            "--max-tokens",
            str(defaults["maxTokens"]),
            "--warmup",
            "0",
            "--quiet",
            "--output",
            str(
                self.spark_directory
                / (directory / "spark.json").relative_to(self.output)
            ),
            "--workload",
            str(self.spark_directory / workload.relative_to(self.output)),
            "--scenario",
            "normal",
        ]
        command.extend(
            ["--duration", f"{duration}s"]
            if duration is not None
            else ["--requests", str(requests)]
        )
        if header := self.suite["algorithms"][algorithm]["routingHeader"]:
            command.extend(["--stargate-load-balancing-algorithm", header])
        if self.args.spark_pod:
            arguments = command[command.index("--endpoint") :]
            pid_file = (
                self.spark_directory / directory.relative_to(self.output) / "spark.pid"
            )
            command = self.pod_exec(
                "bash",
                "-c",
                'IFS= read -r OPENAI_API_KEY; export OPENAI_API_KEY; echo $$ > "$1"; shift; exec "$@"',
                "--",
                str(pid_file),
                "spark",
                *arguments,
                stdin=True,
            )
        return SparkRun(
            directory=directory,
            command=command,
            expected_seconds=float(
                duration
                if duration is not None
                else max(
                    requests / rate,
                    math.ceil(requests / workers)
                    * (timeout_seconds or defaults["timeoutSeconds"]),
                )
            ),
            metadata={
                "scenario": scenario,
                "algorithm": algorithm,
                "rate": rate,
                "workers": workers,
                "durationSeconds": duration,
                "requests": requests,
                "workload": str(workload.relative_to(self.output)),
                "measured": measured,
                "command": command,
            },
        )

    def completed_report(self, run: SparkRun) -> dict | None:
        metadata_path = run.directory / "metadata.json"
        report_path = run.directory / "spark.json"
        if not metadata_path.exists() or not report_path.exists():
            return None
        metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
        if metadata.get("status") != "complete":
            return None
        if metadata.get("command") != run.command:
            raise LoadTestError(f"completed run command changed: {run.directory}")
        return json.loads(report_path.read_text(encoding="utf-8"))

    def execute(self, runs: list[SparkRun], *, reuse: bool = True) -> list[dict]:
        completed = [self.completed_report(run) for run in runs]
        if reuse and all(report is not None for report in completed):
            for run in runs:
                self.log(f"reusing {run.directory.relative_to(self.output)}")
            return completed

        environment = os.environ.copy()
        environment["OPENAI_API_KEY"] = self.token
        metadata = []
        start_ms = int(time.time() * 1000)
        outputs = []
        processes = []
        for run in runs:
            run.directory.mkdir(parents=True, exist_ok=True)
            value = {**run.metadata, "status": "running", "startedAt": utc_now()}
            atomic_json(run.directory / "metadata.json", value)
            (run.directory / "command.txt").write_text(
                shlex.join(run.command) + "\n", encoding="utf-8"
            )
            metadata.append(value)
            outputs.append(
                (run.directory / "spark.raw.log").open("w", encoding="utf-8")
            )
        self.state["currentRuns"] = [
            str(run.directory.relative_to(self.output)) for run in runs
        ]
        self.save_state()

        started = time.monotonic()
        try:
            for run, output in zip(runs, outputs):
                if self.args.spark_pod:
                    remote = self.spark_directory / run.directory.relative_to(
                        self.output
                    )
                    self.command(self.pod_exec("mkdir", "-p", str(remote)))
                process = subprocess.Popen(
                    run.command,
                    stdin=subprocess.PIPE if self.args.spark_pod else None,
                    stdout=output,
                    stderr=subprocess.STDOUT,
                    env=environment,
                )
                processes.append(process)
                self.children.append(process)
                if self.args.spark_pod:
                    process.stdin.write((self.token + "\n").encode())
                    process.stdin.close()
            while True:
                if (
                    self.port_forward is not None
                    and self.port_forward.poll() is not None
                ):
                    raise LoadTestError(
                        "Stargate port-forward exited during the run; "
                        "the interrupted arm is not valid for comparison"
                    )
                statuses = [process.poll() for process in processes]
                if all(status is not None for status in statuses):
                    break
                if any(status not in (None, 0) for status in statuses):
                    raise LoadTestError("a concurrent Spark process failed")
                if (
                    time.monotonic() - started
                    > max(run.expected_seconds for run in runs) + 600
                ):
                    raise LoadTestError("Spark exceeded its expected duration")
                time.sleep(2)
        except BaseException:
            try:
                if self.args.spark_pod:
                    pid_files = [
                        str(
                            self.spark_directory
                            / run.directory.relative_to(self.output)
                            / "spark.pid"
                        )
                        for run, process in zip(runs, processes)
                        if process.poll() is None
                    ]
                    if pid_files:
                        self.command(
                            self.pod_exec(
                                "bash",
                                "-c",
                                'for file in "$@"; do if [ -f "$file" ]; then read -r pid < "$file"; kill -INT "$pid" 2>/dev/null || true; fi; done',
                                "--",
                                *pid_files,
                            ),
                            timeout=30,
                            check=False,
                        )
            finally:
                self.stop_processes(processes)
            raise
        finally:
            for output in outputs:
                output.close()
            for process in processes:
                if process in self.children:
                    self.children.remove(process)

        reports = []
        for run, value, process in zip(runs, metadata, processes):
            value["endedAt"] = utc_now()
            value["exitCode"] = process.returncode
            if process.returncode:
                value["status"] = "failed"
                atomic_json(run.directory / "metadata.json", value)
                raise LoadTestError(f"Spark failed: {run.directory}")
            report_path = run.directory / "spark.json"
            if self.args.spark_pod:
                self.download_report(run)
            if not report_path.is_file():
                raise LoadTestError(f"Spark did not write {report_path}")
            report = json.loads(report_path.read_text(encoding="utf-8"))
            rendered = self.command(
                [
                    "docker",
                    "run",
                    "--rm",
                    "-v",
                    f"{self.output}:/campaign:ro",
                    self.args.spark_image,
                    "show",
                    f"/campaign/{report_path.relative_to(self.output)}",
                ]
            )
            (run.directory / "spark.txt").write_text(
                ANSI_ESCAPE.sub("", rendered.stdout), encoding="utf-8"
            )
            value["status"] = "complete"
            value["summary"] = report.get("summary")
            atomic_json(run.directory / "metadata.json", value)
            self.write_grafana_links(run.directory, start_ms, int(time.time() * 1000))
            self.log(
                f"completed {run.directory.relative_to(self.output)}: "
                f"{report.get('summary')}"
            )
            reports.append(report)
        self.state.pop("currentRuns", None)
        self.save_state()
        return reports

    @staticmethod
    def stop_processes(processes: list[subprocess.Popen]) -> None:
        for process in processes:
            if process.poll() is None:
                process.terminate()
        for process in processes:
            if process.poll() is None:
                try:
                    process.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    process.kill()

    def write_grafana_links(self, directory: Path, start_ms: int, end_ms: int) -> None:
        start = start_ms - 60_000
        end = end_ms + 60_000
        base = self.args.grafana_url.rstrip("/")
        atomic_json(
            directory / "grafana.json",
            {
                "stargate": (
                    f"{base}/d/stargate-dev-services/stargate?from={start}&to={end}"
                ),
                "backendBalance": (
                    f"{base}/d/stargate-backend-balance/"
                    f"stargate-backend-balance?from={start}&to={end}"
                ),
            },
        )

    def cache_stats(self) -> dict:
        stats = {}
        for context, service in self.backend_targets():
            path = (
                f"/api/v1/namespaces/{self.namespace}/services/"
                f"http:{service}:http/proxy/kv-cache/stats"
            )
            stats[service] = json.loads(self.kubectl(context, ["get", f"--raw={path}"]))
        return stats

    def reset_caches(self, label: str) -> dict:
        directory = self.output / "cache-resets" / label
        atomic_json(directory / "before.json", self.cache_stats())
        for context, deployment in self.backend_targets():
            self.kubectl(context, ["rollout", "restart", f"deployment/{deployment}"])
        for context, deployment in self.backend_targets():
            self.kubectl(
                context,
                ["rollout", "status", f"deployment/{deployment}", "--timeout=5m"],
                timeout=330,
            )
        self.kubectl(
            self.stargate_context,
            ["rollout", "restart", "deployment/llm-request-router"],
        )
        self.kubectl(
            self.stargate_context,
            [
                "rollout",
                "status",
                "deployment/llm-request-router",
                "--timeout=5m",
            ],
            timeout=330,
        )
        self.verify_region()
        if not self.args.endpoint and not self.args.spark_pod:
            self.stop_port_forward()
            self.start_port_forward()
        stats = self.cache_stats()
        if any(
            value.get("kv_cache_entries") or value.get("kv_cache_used_tokens")
            for value in stats.values()
        ):
            raise LoadTestError(f"backend cache reset failed: {stats}")
        atomic_json(directory / "after.json", stats)
        self.log(f"verified empty backend caches for {label}")
        return stats

    def record_cache_delta(self, directory: Path, before: dict) -> None:
        after = self.cache_stats()
        fields = (
            "kv_cache_hit_count",
            "kv_cache_miss_count",
            "kv_cache_eviction_count",
            "kv_cache_evicted_tokens",
        )
        atomic_json(directory / "cache-stats.after.json", after)
        atomic_json(
            directory / "cache-stats.delta.json",
            {
                backend: {
                    field: stats.get(field, 0) - before[backend].get(field, 0)
                    for field in fields
                }
                for backend, stats in after.items()
            },
        )

    def compare(self, directory: Path, left: Path, right: Path) -> None:
        if len(self.algorithms) != 2:
            return
        directory.mkdir(parents=True, exist_ok=True)
        result = self.command(
            [
                "docker",
                "run",
                "--rm",
                "-v",
                f"{self.output}:/campaign:ro",
                self.args.spark_image,
                "compare",
                f"/campaign/{left.relative_to(self.output)}",
                f"/campaign/{right.relative_to(self.output)}",
            ]
        )
        (directory / "wait-and-widen-vs-power-of-n.txt").write_text(
            ANSI_ESCAPE.sub("", result.stdout), encoding="utf-8"
        )

    def cooldown(self) -> None:
        seconds = self.suite["defaults"]["cooldownSeconds"]
        self.log(f"cooldown for {seconds} seconds")
        time.sleep(seconds)

    def algorithm_order(self, reverse: bool = False) -> list[str]:
        order = reversed(ALGORITHM_ORDER) if reverse else ALGORITHM_ORDER
        return [algorithm for algorithm in order if algorithm in self.algorithms]

    def run_smoke(self, scenario: dict) -> None:
        root = self.runs / "smoke"
        for algorithm in self.algorithm_order():
            report = self.execute(
                [
                    self.spark_run(
                        Path("smoke") / algorithm,
                        algorithm,
                        scenario="smoke",
                        rate=scenario["rate"],
                        workers=scenario["workers"],
                        workload=self.workloads["smoke"]["main"],
                        requests=scenario["requests"],
                    )
                ]
            )[0]
            if report["summary"]["failed"]:
                raise LoadTestError(f"smoke failed for {algorithm}")
        self.compare(
            root,
            root / "wait-and-widen" / "spark.json",
            root / "power-of-n" / "spark.json",
        )

    def run_saturation(self, scenario: dict) -> None:
        for rate_index, rate in enumerate(scenario["rates"]):
            root = self.runs / "saturation" / f"r{rate:02d}"
            order = self.algorithm_order(reverse=rate_index % 2 == 1)
            for algorithm in order:
                self.execute(
                    [
                        self.spark_run(
                            root.relative_to(self.runs) / algorithm,
                            algorithm,
                            scenario="saturation",
                            rate=rate,
                            workers=max(
                                scenario["minimumWorkers"],
                                rate * scenario["workersPerRps"],
                            ),
                            workload=self.workloads["saturation"]["main"],
                            duration=scenario["durationSeconds"],
                        )
                    ]
                )
                self.cooldown()
            self.compare(
                root,
                root / "wait-and-widen" / "spark.json",
                root / "power-of-n" / "spark.json",
            )

    def run_affinity(self, name: str, scenario: dict) -> None:
        if scenario["kind"] == "session-workers":
            requests = scenario["workers"] * sum(
                len(task) for task in scenario["sessionTasks"]
            )
        else:
            requests = scenario["sessions"] * scenario["turnsPerSession"]
        for repeat in range(1, scenario["repeats"] + 1):
            root = self.runs / name / f"repeat-{repeat:02d}"
            for algorithm in self.algorithm_order(reverse=repeat % 2 == 0):
                run = self.spark_run(
                    root.relative_to(self.runs) / algorithm,
                    algorithm,
                    scenario=name,
                    rate=scenario["rate"],
                    workers=scenario["workers"],
                    workload=self.workloads[name]["main"],
                    requests=requests,
                    request_slo_ms=scenario.get("requestSloMs"),
                    max_wait_ms=scenario.get("maxWaitMs"),
                    timeout_seconds=scenario.get("timeoutSeconds"),
                )
                delta = root / algorithm / "cache-stats.delta.json"
                if self.completed_report(run) is not None and delta.is_file():
                    self.log(f"reusing clean-cache arm {run.directory}")
                    continue
                before = self.reset_caches(f"{name}-repeat-{repeat:02d}-{algorithm}")
                self.execute([run], reuse=False)
                self.record_cache_delta(root / algorithm, before)
            self.compare(
                root,
                root / "wait-and-widen" / "spark.json",
                root / "power-of-n" / "spark.json",
            )

    def run_mixed_sessions(self, scenario: dict) -> None:
        hot = scenario["hot"]
        short = scenario["short"]
        for repeat in range(1, scenario["repeats"] + 1):
            root = self.runs / "mixed-sessions" / f"repeat-{repeat:02d}"
            for algorithm in self.algorithm_order(reverse=repeat % 2 == 1):
                measured_runs = [
                    self.spark_run(
                        root.relative_to(self.runs) / algorithm / "hot",
                        algorithm,
                        scenario="mixed-sessions-hot",
                        rate=hot["rate"],
                        workers=hot["workers"],
                        workload=self.workloads["mixed-sessions"]["hot"],
                        duration=scenario["durationSeconds"],
                    ),
                    self.spark_run(
                        root.relative_to(self.runs) / algorithm / "short",
                        algorithm,
                        scenario="mixed-sessions-short",
                        rate=short["rate"],
                        workers=short["workers"],
                        workload=self.workloads["mixed-sessions"]["short"],
                        duration=scenario["durationSeconds"],
                    ),
                ]
                delta = root / algorithm / "cache-stats.delta.json"
                if (
                    all(self.completed_report(run) is not None for run in measured_runs)
                    and delta.is_file()
                ):
                    self.log(f"reusing clean-cache mixed arm for {algorithm}")
                    continue
                before = self.reset_caches(
                    f"mixed-sessions-repeat-{repeat:02d}-{algorithm}"
                )
                self.execute(
                    [
                        self.spark_run(
                            root.relative_to(self.runs) / algorithm / "warm",
                            algorithm,
                            scenario="mixed-sessions-warm",
                            rate=1,
                            workers=1,
                            workload=self.workloads["mixed-sessions"]["hot"],
                            requests=1,
                            measured=False,
                        )
                    ],
                    reuse=False,
                )
                self.execute(measured_runs, reuse=False)
                self.record_cache_delta(root / algorithm, before)
            for stream in ("hot", "short"):
                self.compare(
                    root / stream,
                    root / "wait-and-widen" / stream / "spark.json",
                    root / "power-of-n" / stream / "spark.json",
                )

    def write_summary(self) -> None:
        runs = []
        for metadata_path in sorted(self.runs.glob("**/metadata.json")):
            metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
            if metadata.get("status") != "complete" or not metadata.get("measured"):
                continue
            report = json.loads(
                (metadata_path.parent / "spark.json").read_text(encoding="utf-8")
            )
            runs.append(
                {
                    "path": str(metadata_path.parent.relative_to(self.output)),
                    "scenario": metadata["scenario"],
                    "algorithm": metadata["algorithm"],
                    "summary": report.get("summary"),
                    "ttftMs": report.get("ttft_ms"),
                    "latencyMs": report.get("latency_ms"),
                    "itlMs": report.get("itl_ms"),
                    "throughput": report.get("throughput"),
                    "errors": report.get("errors"),
                }
            )
        atomic_json(
            self.output / "results.json",
            {"identity": self.identity, "completedAt": utc_now(), "runs": runs},
        )

    def run(self) -> None:
        self.initialize()
        runners = {
            "requests": self.run_smoke,
            "sweep": self.run_saturation,
            "mixed-sessions": self.run_mixed_sessions,
        }
        for name in self.scenarios:
            self.log(f"starting canonical scenario {name}")
            scenario = self.suite["scenarios"][name]
            if scenario["kind"] in ("session-affinity", "session-workers"):
                self.run_affinity(name, scenario)
            else:
                runners[scenario["kind"]](scenario)
        self.write_summary()
        self.verify_region()
        self.state["status"] = "complete"
        self.state["completedAt"] = utc_now()
        self.state.pop("currentRuns", None)
        self.save_state()
        self.log(f"campaign complete: {self.output}")

    def close(self) -> None:
        self.stop_processes(self.children)
        self.stop_port_forward()


def minimum_measured_minutes(suite: dict, suite_name: str) -> float:
    seconds = 0.0
    for name in suite["suites"][suite_name]:
        scenario = suite["scenarios"][name]
        if scenario["kind"] == "requests":
            seconds += scenario["requests"] / scenario["rate"]
        elif scenario["kind"] == "sweep":
            seconds += len(scenario["rates"]) * scenario["durationSeconds"]
        elif scenario["kind"] == "session-affinity":
            seconds += (
                scenario["sessions"]
                * scenario["turnsPerSession"]
                * scenario["repeats"]
                / scenario["rate"]
            )
        elif scenario["kind"] == "session-workers":
            requests = scenario["workers"] * sum(
                len(task) for task in scenario["sessionTasks"]
            )
            seconds += requests * scenario["repeats"] / scenario["rate"]
        else:
            seconds += scenario["durationSeconds"] * scenario["repeats"]
    return seconds * 2 / 60


def parse_args(suite: dict) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run canonical Spark workloads against Stargate dev"
    )
    parser.add_argument("--list", action="store_true")
    parser.add_argument("--suite", choices=sorted(suite["suites"]))
    parser.add_argument("--region")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--spark-image", default=os.environ.get("STARGATE_SPARK_IMAGE"))
    parser.add_argument(
        "--algorithm", choices=("both", *ALGORITHM_ORDER), default="both"
    )
    parser.add_argument("--endpoint")
    parser.add_argument(
        "--spark-pod",
        help="Run traffic in a dedicated hub Pod with matching Spark, bash, tar, and writable /campaign",
    )
    parser.add_argument("--local-port", type=int, default=18000)
    parser.add_argument("--grafana-url", default="http://localhost:3000")
    parser.add_argument("--resume", action="store_true")
    return parser.parse_args()


def main() -> int:
    campaign = None
    try:
        suite = load_suite()
        args = parse_args(suite)
        if args.list:
            for name, scenarios in suite["suites"].items():
                print(
                    f"{name}: at least {minimum_measured_minutes(suite, name):.1f} measured minutes at rate caps: "
                    f"{', '.join(scenarios)}"
                )
            return 0
        missing = [
            name
            for name, value in (
                ("--suite", args.suite),
                ("--region", args.region),
                ("--output", args.output),
                ("--spark-image or STARGATE_SPARK_IMAGE", args.spark_image),
            )
            if value is None
        ]
        if missing:
            raise LoadTestError(f"required arguments: {', '.join(missing)}")
        if not 1 <= args.local_port <= 65535:
            raise LoadTestError("--local-port must be between 1 and 65535")
        campaign = Campaign(args, suite)
        campaign.run()
        return 0
    except (
        LoadTestError,
        OSError,
        ValueError,
        KeyError,
        yaml.YAMLError,
        subprocess.TimeoutExpired,
    ) as error:
        if campaign is not None:
            campaign.state["status"] = "failed"
            campaign.state["error"] = str(error)
            campaign.save_state()
        print(f"error: {error}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        if campaign is not None:
            campaign.state["status"] = "interrupted"
            campaign.save_state()
        print("interrupted", file=sys.stderr)
        return 130
    finally:
        if campaign is not None:
            campaign.close()


if __name__ == "__main__":
    raise SystemExit(main())

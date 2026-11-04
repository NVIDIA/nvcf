# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Exercise the real benchmark driver against its built-in mock backend."""

import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time


def test_usage(mock_binary, bench_binary):
    with tempfile.TemporaryDirectory(dir=os.environ.get("TEST_TMPDIR")) as directory:
        root = Path(directory)
        server_log = root / "mock.log"
        with server_log.open("w") as log:
            server = subprocess.Popen(
                [
                    str(mock_binary), "--http-listen-addr", "127.0.0.1:0",
                    "--model-name", "model-a", "--num-tokens", "100",
                    "--context-length-tokens", "5", "--token-delay-ms", "1",
                ],
                env=dict(os.environ, RUST_LOG="info"),
                stdout=log,
                stderr=subprocess.STDOUT,
            )
            try:
                deadline = time.monotonic() + 5
                while True:
                    output = server_log.read_text()
                    address = re.search(r"http://127\.0\.0\.1:\d+/v1/chat/completions", output)
                    if address:
                        break
                    if server.poll() is not None or time.monotonic() >= deadline:
                        raise AssertionError(f"mock did not become ready:\n{output}")
                    time.sleep(0.01)

                manifest = root / "manifest.json"
                manifest.write_text(json.dumps({
                    "manifest_version": 1,
                    "benchmark_name": "streamed-usage",
                    "metadata": {},
                    "model": "model-a",
                    "seed": 1,
                    "request_count": 1,
                    "max_concurrency": 1,
                    "stargate_count": 1,
                    "backend_count": 1,
                    "requests": [{
                        "request_index": 0,
                        "request_id": "usage-request",
                        "scheduled_offset_ms": 0,
                        "routing_key": None,
                        "cache_affinity_key": None,
                        "input_tokens": 2,
                        "output_tokens": 100,
                        "backend_behavior_class": "uniform",
                    }],
                }))
                results = root / "results.jsonl"
                driven = subprocess.run(
                    [str(bench_binary), "drive", "--manifest", str(manifest),
                     "--endpoint", address.group(), "--output", str(results)],
                    capture_output=True, text=True, timeout=10,
                )
                assert driven.returncode == 0, driven.stdout + driven.stderr
                result = json.loads(results.read_text())
                assert result["ok"], result
                assert result["output_tokens"] == 100, result
                # The five-token context leaves three generated tokens after the prompt.
                assert result["observed_output_tokens"] == 3, result
                assert result["first_output_ms"] is not None, result
            finally:
                if server.poll() is None:
                    server.terminate()
                    try:
                        server.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        server.kill()
                        server.wait()


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("usage: benchmark_usage_test.py MOCK_BINARY BENCH_BINARY")
    test_usage(*(Path(argument).resolve() for argument in sys.argv[1:]))

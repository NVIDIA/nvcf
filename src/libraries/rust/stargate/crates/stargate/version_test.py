# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import pathlib
import subprocess
import sys


def main() -> int:
    binary_path = pathlib.Path(sys.argv[1])
    version_env_path = pathlib.Path(sys.argv[2])

    key, version = version_env_path.read_text(encoding="utf-8").strip().split("=", 1)
    if key != "STARGATE_BUILD_VERSION":
        raise ValueError(f"unexpected version key: {key}")

    completed = subprocess.run(
        [binary_path, "--version"],
        check=False,
        capture_output=True,
    )
    expected_stdout = f"stargate {version}\n".encode()
    if completed.returncode != 0:
        sys.stderr.buffer.write(completed.stderr)
        return completed.returncode
    if completed.stdout != expected_stdout:
        raise AssertionError(
            f"unexpected version output: {completed.stdout!r}; expected {expected_stdout!r}"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())

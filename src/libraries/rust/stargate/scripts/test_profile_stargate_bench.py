#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


import os
import subprocess
import unittest
from pathlib import Path
from tempfile import TemporaryDirectory


REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts" / "profile_stargate_bench.sh"


class ProfileStargateBenchTest(unittest.TestCase):
    def run_profile_script(
        self,
        args: list[str],
        output_dir: Path,
        extra_env: dict[str, str] | None = None,
        cwd: Path = REPO_ROOT,
    ) -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        env.update(
            {
                "STARGATE_BENCH_PROFILE_DRY_RUN": "1",
                "STARGATE_BENCH_PROFILE_NOW": "20260519T010203Z",
                "STARGATE_BENCH_PROFILE_OUTPUT_DIR": str(output_dir),
            }
        )
        if extra_env:
            env.update(extra_env)
        return subprocess.run(
            [str(SCRIPT), *args],
            cwd=cwd,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
        )

    def test_dry_run_records_benchmark_command(self) -> None:
        with TemporaryDirectory() as temp:
            output_dir = Path(temp)

            result = self.run_profile_script(
                ["transport-bench", "--requests", "1"],
                output_dir,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            run_dir = output_dir / "20260519T010203Z-transport-bench"
            command_text = (run_dir / "command.txt").read_text()
            self.assertEqual(
                command_text,
                "target/release/stargate-bench transport-bench --requests 1\n",
            )
            self.assertIn("dry-run: wrote profile metadata only", result.stdout)
            self.assertTrue((run_dir / "environment.txt").exists())

    def test_relative_output_dir_is_resolved_before_repo_chdir(self) -> None:
        with TemporaryDirectory() as temp:
            caller_cwd = Path(temp) / "caller"
            caller_cwd.mkdir()

            result = self.run_profile_script(
                ["transport-bench", "--requests", "1"],
                Path("profiles"),
                cwd=caller_cwd,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            run_dir = caller_cwd / "profiles" / "20260519T010203Z-transport-bench"
            self.assertTrue((run_dir / "command.txt").exists())
            self.assertIn(f"profile output: {run_dir}", result.stdout)

    def test_profile_name_overrides_directory_slug(self) -> None:
        with TemporaryDirectory() as temp:
            output_dir = Path(temp)

            result = self.run_profile_script(
                [
                    "--profile-name",
                    "lb/pulsar profile",
                    "lb-microbench",
                    "--scenario",
                    "pulsar",
                ],
                output_dir,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((output_dir / "20260519T010203Z-lb-pulsar-profile").is_dir())

    def test_requires_benchmark_args(self) -> None:
        with TemporaryDirectory() as temp:
            result = self.run_profile_script([], Path(temp))

            self.assertEqual(result.returncode, 2)
            self.assertIn("Usage:", result.stderr)


if __name__ == "__main__":
    unittest.main()

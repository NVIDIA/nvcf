# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

"""Guard development runtime contents without downloading or compiling binaries.

Run with python3 tools/ci/runtime_packaging_test.py Dockerfile from the
Stargate directory, or through the Bazel runtime_packaging_test target.
Image builds and runtime checks remain necessary when changing packaging.
"""

from pathlib import Path
import shlex
import sys
import unittest


DOCKERFILE = Path(sys.argv.pop(1))


class RuntimePackagingTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.stages = {}
        # Join Dockerfile continuations before inspecting instructions. Include
        # inherited instructions so moving a helper into runtime-base fails too.
        for line in DOCKERFILE.read_text().replace("\\\n", " ").splitlines():
            words = shlex.split(line, comments=True)
            if not words:
                continue
            instruction = words[0].upper()
            if instruction == "FROM":
                cls.stages[words[-1]] = (words[-3], [])
                instructions = cls.stages[words[-1]][1]
            elif cls.stages:
                instructions.append((instruction, words[1:]))

    def instructions(self, name):
        parent, instructions = self.stages[name]
        inherited = self.instructions(parent) if parent in self.stages else []
        return inherited + instructions

    def test_pylon_contains_only_its_binary(self):
        instructions = self.instructions("pylon-runtime")
        copies = [args for op, args in instructions if op == "COPY"]
        self.assertEqual(copies, [[
            "--from=binary-builder", "/out/pylon", "/usr/local/bin/pylon",
        ]])
        # Adding a RUN or ADD could reintroduce a helper outside COPY.
        self.assertFalse([op for op, _ in instructions if op in {"ADD", "RUN"}])

    def test_other_probe_targets_keep_the_executable(self):
        for name in ("stargate-runtime", "mock-dynamo-runtime", "integration-runtime"):
            with self.subTest(stage=name):
                self.assertIn(("COPY", [
                    "--from=health-probe-downloader",
                    "/usr/local/bin/grpc_health_probe",
                    "/usr/local/bin/grpc_health_probe",
                ]), self.instructions(name))
        downloader = self.instructions("health-probe-downloader")
        self.assertTrue(any(
            op == "RUN" and "chmod +x /usr/local/bin/grpc_health_probe" in " ".join(args)
            for op, args in downloader
        ))

    def test_runtimes_preserve_non_root_identity_and_entrypoints(self):
        for name, binary in (
            ("pylon-runtime", "pylon"),
            ("stargate-runtime", "stargate"),
            ("stargate-k8s-router-runtime", "stargate-k8s-router"),
            ("mock-dynamo-runtime", "mock-dynamo"),
            ("integration-runtime", ""),
        ):
            with self.subTest(stage=name):
                instructions = self.instructions(name)
                users = [args for op, args in instructions if op == "USER"]
                self.assertEqual(users[-1], ["1000:1000"])
                entrypoints = [args for op, args in instructions if op == "ENTRYPOINT"]
                self.assertEqual(entrypoints[-1], [f"[{binary}]"])


if __name__ == "__main__":
    unittest.main()

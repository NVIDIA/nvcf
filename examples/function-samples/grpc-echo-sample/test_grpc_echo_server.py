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

import ast
from pathlib import Path
import unittest


class GRPCEchoServerHealthTest(unittest.TestCase):
    def test_server_sets_default_and_echo_grpc_health_services(self):
        source = (Path(__file__).parent / "grpc_echo_server.py").read_text(encoding="utf-8")
        tree = ast.parse(source)

        services = set()
        for node in ast.walk(tree):
            if not isinstance(node, ast.Call):
                continue
            if not isinstance(node.func, ast.Attribute):
                continue
            if node.func.attr != "set" or len(node.args) < 2:
                continue
            if not isinstance(node.args[0], ast.Constant):
                continue
            status = node.args[1]
            if (
                isinstance(status, ast.Attribute)
                and status.attr == "SERVING"
                and isinstance(status.value, ast.Attribute)
                and status.value.attr == "HealthCheckResponse"
            ):
                services.add(node.args[0].value)

        self.assertIn("", services)
        self.assertIn("Echo", services)


if __name__ == "__main__":
    unittest.main()

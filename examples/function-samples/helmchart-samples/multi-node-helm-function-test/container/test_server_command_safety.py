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
import unittest
from unittest import mock

from fastapi import HTTPException

import server


class CommandSafetyTest(unittest.TestCase):
    def test_rejects_shell_metacharacters_in_size_parameter(self):
        params = server.TestParameters(
            e="16G; touch /tmp/pwned",
            cluster_type="ncp-mlx5",
        )

        with mock.patch.object(server, "check_gpu_availability"), \
                mock.patch.object(server.subprocess, "check_output") as check_output:
            with self.assertRaises(HTTPException) as raised:
                server.nccl_test(params)

        self.assertEqual(raised.exception.status_code, 400)
        check_output.assert_not_called()

    def test_aws_gb300_uses_argv_without_shell(self):
        params = server.TestParameters(
            np=8,
            e="16G",
            npernode=4,
            cluster_type="aws-gb300",
        )

        with mock.patch.dict(os.environ, {"HOSTFILE": "/tmp/hostfile"}), \
                mock.patch.object(server, "check_gpu_availability"), \
                mock.patch.object(server.subprocess, "check_output", return_value="ok") as check_output:
            result = server.nccl_test(params)

        command = check_output.call_args.args[0]
        kwargs = check_output.call_args.kwargs
        self.assertIsInstance(command, list)
        self.assertEqual(command[0], "/usr/bin/env")
        self.assertIn("-u", command)
        self.assertIn("NCCL_NET_PLUGIN", command)
        self.assertIn("--hostfile", command)
        self.assertNotIn("shell", kwargs)
        self.assertEqual(result["status"], "success")

    def test_bandwidth_test_uses_argv_without_shell(self):
        params = server.BandwidthTestParameters(testcase="host_to_device_memcpy")

        with mock.patch.object(server, "check_gpu_availability"), \
                mock.patch.object(server.subprocess, "check_output", return_value="ok") as check_output:
            result = server.bandwidth_test(params)

        command = check_output.call_args.args[0]
        kwargs = check_output.call_args.kwargs
        self.assertIsInstance(command, list)
        self.assertEqual(command[0], server.NVBANDWIDTH_PATH)
        self.assertIn("-t", command)
        self.assertNotIn("shell", kwargs)
        self.assertEqual(result["status"], "success")


if __name__ == "__main__":
    unittest.main()

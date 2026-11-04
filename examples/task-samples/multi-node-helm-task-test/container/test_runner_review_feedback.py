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

import unittest
from unittest import mock

import runner


class RunnerReviewFeedbackTest(unittest.TestCase):
    def test_gpu_availability_accepts_matching_gpu_count(self):
        with mock.patch.object(
            runner.subprocess,
            "check_output",
            side_effect=["driver ok", "GPU 0: test\nGPU 1: test\n"],
        ) as check_output:
            output = runner.check_gpu_availability(2)

        self.assertEqual(output, "driver ok")
        self.assertEqual(
            [call.args[0] for call in check_output.call_args_list],
            ["nvidia-smi", "nvidia-smi -L"],
        )

    def test_gpu_availability_rejects_mismatched_gpu_count(self):
        with mock.patch.object(
            runner.subprocess,
            "check_output",
            side_effect=["driver ok", "GPU 0: test\n"],
        ):
            with self.assertRaises(Exception) as raised:
                runner.check_gpu_availability(2)

        self.assertIn("Number of GPUs is not equal to 2", str(raised.exception))

    def test_env_flag_enabled_defaults_true(self):
        with mock.patch.object(runner.os, "getenv", return_value=None):
            self.assertTrue(runner.env_flag_enabled("DEBUG"))

    def test_env_flag_enabled_parses_false_values(self):
        for value in ("0", "false", "False", "no", ""):
            with self.subTest(value=value):
                with mock.patch.object(runner.os, "getenv", return_value=value):
                    self.assertFalse(runner.env_flag_enabled("DEBUG"))

    def test_env_flag_enabled_parses_true_values(self):
        for value in ("1", "true", "yes", "anything"):
            with self.subTest(value=value):
                with mock.patch.object(runner.os, "getenv", return_value=value):
                    self.assertTrue(runner.env_flag_enabled("DEBUG"))


if __name__ == "__main__":
    unittest.main()

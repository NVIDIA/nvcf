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

from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parent


class ChartReviewFeedbackTest(unittest.TestCase):
    def test_chart_injects_runner_gpu_environment(self):
        values = (ROOT / "multi-node-task-test" / "values.yaml").read_text()
        override = (ROOT / "gb200-override.yaml").read_text()
        statefulset = (
            ROOT / "multi-node-task-test" / "templates" / "statefulset.yaml"
        ).read_text()

        self.assertIn("gpusPerNode: 1", values)
        self.assertIn("gpusPerNode: 4", override)
        self.assertIn("- name: TOTAL_NUM_GPUS", statefulset)
        self.assertIn(
            "mul (int .Values.gpusPerNode) (int .Values.nodesPerInstance)",
            statefulset,
        )
        self.assertIn("- name: NPERNODE", statefulset)
        self.assertNotIn("- name: NUM_GPUS\n", statefulset)
        self.assertNotIn("- name: NUM_NODES\n", statefulset)


if __name__ == "__main__":
    unittest.main()

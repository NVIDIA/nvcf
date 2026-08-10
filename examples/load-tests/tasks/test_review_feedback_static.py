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


REPO_ROOT = Path(__file__).resolve().parents[3]
LOAD_TEST_TASKS = REPO_ROOT / "examples" / "load-tests" / "tasks"
MULTI_NODE_TASK = REPO_ROOT / "examples" / "task-samples" / "multi-node-helm-task-test"


class ReviewFeedbackStaticTest(unittest.TestCase):
    def test_task_examples_do_not_embed_internal_or_staging_values(self):
        forbidden = (
            "qdrlnbkss8u1",
            "stg.api.nvct.nvidia.com",
            "stg.nvcr.io",
        )
        offenders = []

        for root in (LOAD_TEST_TASKS, MULTI_NODE_TASK):
            for path in root.rglob("*"):
                if path.is_dir() or path.name.startswith("test_"):
                    continue
                text = path.read_text(errors="ignore")
                for value in forbidden:
                    if value in text:
                        offenders.append(f"{path.relative_to(REPO_ROOT)}: {value}")

        self.assertEqual(offenders, [])

    def test_health_load_test_uses_base_url_env(self):
        script = (LOAD_TEST_TASKS / "nvct_health_load_test.js").read_text()

        self.assertIn("__ENV.BASE_URL", script)
        self.assertIn("/health", script)

    def test_create_task_load_test_uses_container_image_env(self):
        script = (LOAD_TEST_TASKS / "nvct_create_task_load_test.js").read_text()

        self.assertIn("__ENV.CONTAINER_IMAGE", script)

    def test_multi_node_worker_does_not_exit_after_fixed_sleep(self):
        script = (MULTI_NODE_TASK / "container" / "run.sh").read_text()

        self.assertNotIn("sleep 600", script)


if __name__ == "__main__":
    unittest.main()

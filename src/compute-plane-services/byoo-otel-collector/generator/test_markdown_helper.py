# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import unittest

from markdown_helper import MetricsSectionGenerator


class MetricsSectionGeneratorTest(unittest.TestCase):
    def test_category_heading_is_one_level_below_job_heading(self) -> None:
        rendered = MetricsSectionGenerator().generate(
            [
                {
                    "function_type": "helm",
                    "jobs": [
                        {
                            "name": "kubernetes-cadvisor",
                            "metric_allow_list": [
                                {
                                    "catagory": "CPU",
                                    "list": [{"name": "container_cpu_usage_seconds_total"}],
                                }
                            ],
                        }
                    ],
                }
            ]
        )

        self.assertIn("### kubernetes-cadvisor\n#### CPU", rendered)
        self.assertNotIn("##### CPU", rendered)


if __name__ == "__main__":
    unittest.main()

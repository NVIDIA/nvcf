#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import unittest
from pathlib import Path


VERIFY_PATH = Path(__file__).resolve().parents[1] / "scripts" / "verify.py"
SPEC = importlib.util.spec_from_file_location("stargate_dev_verify", VERIFY_PATH)
VERIFY = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(VERIFY)


class MetricParsingTests(unittest.TestCase):
    def test_active_backend_metrics_are_keyed_by_routing_key_and_model(self) -> None:
        metrics = """
# HELP stargate_active_inference_servers Active inference servers
stargate_active_inference_servers{model="dev-model",routing_key="stargate-dev"} 4
"""

        self.assertEqual(
            VERIFY.parse_active_backends(metrics),
            {("stargate-dev", "dev-model"): 4.0},
        )


if __name__ == "__main__":
    unittest.main()

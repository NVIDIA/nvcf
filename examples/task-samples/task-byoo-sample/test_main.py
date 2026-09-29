# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import importlib.util
import sys
import unittest
from pathlib import Path
from unittest import mock


MODULE_PATH = Path(__file__).with_name("main.py")
SPEC = importlib.util.spec_from_file_location("task_byoo_sample_main", MODULE_PATH)
task_main = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = task_main
SPEC.loader.exec_module(task_main)


class PrepareTaskContainerTest(unittest.TestCase):
    def test_signals_running_before_checking_collector_health(self):
        events = []

        with (
            mock.patch.object(task_main.os.path, "exists", return_value=True),
            mock.patch.object(
                task_main,
                "update_task_progress_file",
                side_effect=lambda *args: events.append("progress"),
            ) as update_progress,
            mock.patch.object(task_main, "Thread") as thread,
            mock.patch.object(
                task_main,
                "check_collector_health",
                side_effect=lambda: events.append("health") or True,
            ),
        ):
            thread.return_value.start.side_effect = lambda: events.append("heartbeat")

            self.assertTrue(task_main.prepare_task_container())

        update_progress.assert_called_once_with(0, task_main.PROGRESS_FILE, "")
        self.assertEqual(events, ["progress", "heartbeat", "health"])


if __name__ == "__main__":
    unittest.main()

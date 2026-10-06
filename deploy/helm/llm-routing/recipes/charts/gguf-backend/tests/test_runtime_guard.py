# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import importlib.util
import json
import pathlib
import sys
import tempfile
import unittest
from unittest.mock import patch

source = pathlib.Path(__file__).resolve().parents[1] / 'files/runtime_guard.py'
spec = importlib.util.spec_from_file_location('runtime_guard', source)
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


class RuntimeGuardTests(unittest.TestCase):
    def setUp(self):
        self.cgroup_patch = patch.object(guard, 'cgroup_memory', return_value={'currentBytes': 0, 'swapBytes': 0, 'events': {}})
        self.process_patch = patch.object(guard, 'process_memory', return_value={'VmRSS': 0, 'VmSwap': 0})
        self.cgroup_patch.start()
        self.process_patch.start()
        self.addCleanup(self.cgroup_patch.stop)
        self.addCleanup(self.process_patch.stop)

    def test_normal_child_completes(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(guard, 'memory', return_value=(2*1024**3, 4096)), patch.object(guard.signal, 'signal'):
            marker = pathlib.Path(directory) / 'stop.json'
            self.assertEqual(guard.supervise([sys.executable, '-c', 'print("preserved output")'], marker), 0)
            self.assertFalse(marker.exists())
            self.assertEqual(marker.with_name('runtime-output.log').read_text(), 'preserved output\n')

    def test_low_memory_stops_child_and_latches_failure(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(guard, 'memory', side_effect=[(2*1024**3, 0), (512*1024**2, 0)]), patch.object(guard.signal, 'signal'):
            marker = pathlib.Path(directory) / 'stop.json'
            self.assertEqual(guard.supervise([sys.executable, '-c', 'import time; time.sleep(60)'], marker), 78)
            self.assertEqual(json.loads(marker.read_text())['result'], 'MEMORY_GUARD_STOP')
            with patch.object(guard.subprocess, 'Popen') as launch:
                self.assertEqual(guard.supervise(['must-not-start'], marker), 78)
                launch.assert_not_called()

    def test_low_memory_prevents_start(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(guard, 'memory', return_value=(512*1024**2, 0)), patch.object(guard.subprocess, 'Popen') as launch:
            self.assertEqual(guard.supervise(['must-not-start'], pathlib.Path(directory) / 'stop.json'), 78)
            launch.assert_not_called()

    def test_runtime_swap_stops_child(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(guard, 'memory', return_value=(2*1024**3, 4096)), patch.object(guard, 'process_memory', return_value={'VmSwap': 4096}), patch.object(guard.signal, 'signal'):
            marker = pathlib.Path(directory) / 'stop.json'
            self.assertEqual(guard.supervise([sys.executable, '-c', 'import time; time.sleep(60)'], marker), 78)
            self.assertEqual(json.loads(marker.read_text())['reason'], 'process_swap')

    def test_cgroup_swap_stops_child(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(guard, 'memory', return_value=(2*1024**3, 4096)), patch.object(guard, 'cgroup_memory', return_value={'currentBytes': 0, 'swapBytes': 4096, 'events': {}}), patch.object(guard.signal, 'signal'):
            marker = pathlib.Path(directory) / 'stop.json'
            self.assertEqual(guard.supervise([sys.executable, '-c', 'import time; time.sleep(60)'], marker), 78)
            self.assertEqual(json.loads(marker.read_text())['reason'], 'cgroup_swap')


if __name__ == '__main__':
    unittest.main()

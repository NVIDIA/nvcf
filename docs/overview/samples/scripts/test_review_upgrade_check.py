# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Execute the reviewed upgrade gates with synthetic external command adapters."""

import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

from test_receipt_upgrade_check import GUIDE
from test_recovery_upgrade_check import block_containing


class ReviewedUpgradeGates(unittest.TestCase):
    def test_nats_request_has_timeouts_and_stops_on_transport_failure(self):
        block = block_containing(GUIDE, "NATS_MONITOR_PORT=")
        command = re.search(r'(?m)^    curl .*?(?=\n    jq)', block, re.DOTALL)[0]
        stubs = r'''
curl() {
  case " $* " in *' --connect-timeout 5 '*) ;; *) return 99 ;; esac
  case " $* " in *' --max-time 30 '*) return 28 ;; *) return 99 ;; esac
}
'''
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, "nats-drain").mkdir()
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", stubs + command + "\necho CONTINUED"],
                                    env={**os.environ, "UPGRADE_RECORD": directory, "pod": "test", "NATS_MONITOR_PORT": "12345"},
                                    capture_output=True, text=True)
            self.assertEqual(28, result.returncode, result.stderr)
            self.assertNotIn("CONTINUED", result.stdout)


if __name__ == "__main__":
    unittest.main()

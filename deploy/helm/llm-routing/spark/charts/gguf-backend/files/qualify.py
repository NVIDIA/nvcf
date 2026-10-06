# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import json
import os
import socket
import subprocess
import time

endpoints = os.environ['RPC_ENDPOINTS'].split(',')
assert len(endpoints) == 2
for endpoint in endpoints:
    host, port = endpoint.rsplit(':', 1)
    for attempt in range(120):
        try:
            with socket.create_connection((host, int(port)), timeout=2):
                pass
            break
        except OSError:
            if attempt == 119:
                raise
            time.sleep(2)
subprocess.run(['/artifacts/runtime/test-rpc-multi-server', *endpoints], check=True, timeout=60)
subprocess.run(['/artifacts/runtime/rpc-gpu-check', *endpoints], check=True, timeout=120)
print(json.dumps({'result': 'PASS', 'transport': 'TCP', 'tests': ['upstream-rpc-buffer-isolation', 'two-gpu-f32-matmul-three-repeats']}), flush=True)

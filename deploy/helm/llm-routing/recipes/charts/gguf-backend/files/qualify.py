# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import json
import os
import socket
import subprocess
import time

endpoints = [item for item in os.environ['RPC_ENDPOINTS'].split(',') if item]
assert endpoints, 'At least one RPC endpoint is required'
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
tests = []
if len(endpoints) > 1:
    subprocess.run(['/artifacts/runtime/test-rpc-multi-server', *endpoints], check=True, timeout=60)
    tests.append('upstream-rpc-buffer-isolation')
subprocess.run(['/artifacts/runtime/rpc-gpu-check', *endpoints], check=True, timeout=120)
tests.append(str(len(endpoints)) + '-gpu-f32-matmul-three-repeats')
print(json.dumps({'result': 'PASS', 'transport': 'TCP', 'gpus': len(endpoints), 'tests': tests}), flush=True)

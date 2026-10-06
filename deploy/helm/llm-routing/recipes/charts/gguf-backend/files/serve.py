# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import hashlib
import json
import os
import pathlib
import socket
import time
from runtime_guard import supervise

root = pathlib.Path('/artifacts')
lock = json.loads(pathlib.Path('/checks/model-lock.json').read_text())
verified = json.loads((root / 'model/download-complete.json').read_text())
assert verified['result'] == 'PASS'
assert verified['revision'] == lock['revision']
assert verified['verifiedBytes'] == lock['weightFileBytes']
for record in lock['files']:
    assert (root / 'model' / record['rfilename']).stat().st_size == record['size']
manifest = json.loads((root / 'runtime/build-manifest.json').read_text())
binary = root / 'runtime/llama-server'
assert hashlib.file_digest(binary.open('rb'), 'sha256').hexdigest() == manifest['binaries']['llama-server']
# Empty for a single model node; otherwise one RPC worker endpoint per additional node.
endpoints = [item for item in os.environ.get('RPC_ENDPOINTS', '').split(',') if item]
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
args = [str(binary), '--model', str(root / 'model' / os.environ['FIRST_SHARD']),
        '--alias', os.environ['SERVED_MODEL'], '--host', '0.0.0.0', '--port', '8000']
if endpoints:
    args += ['--rpc', ','.join(endpoints)]
args += json.loads(os.environ['SERVER_ARGS'])
print(json.dumps({'verifiedModel': verified, 'runtimeRevision': manifest['revision'], 'command': args}), flush=True)
raise SystemExit(supervise(args))

# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import os
import hashlib
import json
import pathlib
import shlex
import subprocess

build = pathlib.Path('/artifacts/build')
source = pathlib.Path('/artifacts/llama.cpp-' + os.environ['LLAMA_REVISION'])
command = subprocess.check_output(['ninja', '-C', str(build), '-t', 'commands', 'rpc-gpu-check'], text=True).splitlines()[-1]
tokens = shlex.split(command)
assert tokens[:2] == [':', '&&'] and tokens[-2:] == ['&&', ':']
args = tokens[2:-2]
args = [token for token in args if not token.startswith('-Wl,--dependency-file=')]
object_index = next(i for i, token in enumerate(args) if token.endswith('/rpc-gpu-check.cpp.o'))
args[object_index] = '/checks/rpc-chain-check.cpp'
output = pathlib.Path('/artifacts/checks/rpc-chain-check')
output.parent.mkdir(exist_ok=True)
args[args.index('-o') + 1] = str(output)
args.extend(['-std=c++17', '-I' + str(source / 'ggml/include'), '-I' + str(source / 'ggml/src')])
subprocess.run(args, cwd=build, check=True, timeout=120)
subprocess.run([str(output), *os.environ['RPC_ENDPOINTS'].split(',')], check=True, timeout=120)
print(json.dumps({'binary': str(output), 'sha256': hashlib.file_digest(output.open('rb'), 'sha256').hexdigest()}), flush=True)

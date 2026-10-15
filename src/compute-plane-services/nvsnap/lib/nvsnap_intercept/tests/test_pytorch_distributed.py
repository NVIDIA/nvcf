#!/usr/bin/env python3
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

"""
Test PyTorch distributed init with libgpucr_intercept.so

This isolates the exact code path that segfaults in vLLM:
- torch.distributed.init_process_group()
- _create_c10d_store() for TCP store

Run with:
  # Without intercept (baseline)
  python3 test_pytorch_distributed.py

  # With intercept library
  LD_PRELOAD=../libgpucr_intercept.so python3 test_pytorch_distributed.py

  # With intercept + debug
  GPUCR_LOG_LEVEL=4 LD_PRELOAD=../libgpucr_intercept.so python3 test_pytorch_distributed.py

  # Under gdb
  GPUCR_LOG_LEVEL=4 gdb -ex run --args python3 test_pytorch_distributed.py
"""

import os
import sys
import socket

print(f"PID: {os.getpid()}")
print(f"LD_PRELOAD: {os.environ.get('LD_PRELOAD', 'not set')}")
print()

# Check if CUDA is available
try:
    import torch
    print(f"PyTorch version: {torch.__version__}")
    print(f"CUDA available: {torch.cuda.is_available()}")
    if torch.cuda.is_available():
        print(f"CUDA device: {torch.cuda.get_device_name(0)}")
except ImportError:
    print("ERROR: PyTorch not installed")
    sys.exit(1)

print()

# Find a free port
def find_free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]

port = find_free_port()
print(f"Using port: {port}")

# This is the exact code path that segfaults
print()
print("=== Testing torch.distributed.init_process_group() ===")
print("This creates a TCP store - the exact point where vLLM segfaults")
print()

try:
    # Set up environment for single-process distributed
    os.environ['MASTER_ADDR'] = '127.0.0.1'
    os.environ['MASTER_PORT'] = str(port)
    os.environ['RANK'] = '0'
    os.environ['WORLD_SIZE'] = '1'
    
    print("Calling torch.distributed.init_process_group('gloo')...")
    
    # This is where vLLM segfaults
    torch.distributed.init_process_group(
        backend='gloo',
        init_method=f'tcp://127.0.0.1:{port}',
        rank=0,
        world_size=1
    )
    
    print("SUCCESS: init_process_group completed!")
    
    # Clean up
    torch.distributed.destroy_process_group()
    print("Cleaned up process group")
    
except Exception as e:
    print(f"FAILED: {type(e).__name__}: {e}")
    import traceback
    traceback.print_exc()
    sys.exit(1)

print()
print("=== All tests passed ===")

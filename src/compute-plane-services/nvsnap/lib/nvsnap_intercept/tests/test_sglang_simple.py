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
Simple SGLang import test - check if it loads with our intercept library.
"""
import os
import sys

print(f"PID: {os.getpid()}")
print(f"LD_PRELOAD: {os.environ.get('LD_PRELOAD', 'not set')}")
print()

print("Importing sglang...")
try:
    import sglang as sgl
    print(f"SGLang version: {sgl.__version__}")
    print("SUCCESS: SGLang imported!")
except Exception as e:
    print(f"FAILED: {type(e).__name__}: {e}")
    sys.exit(1)

print()
print("Checking torch...")
try:
    import torch
    print(f"PyTorch version: {torch.__version__}")
    print(f"CUDA available: {torch.cuda.is_available()}")
    if torch.cuda.is_available():
        print(f"GPU: {torch.cuda.get_device_name(0)}")
except Exception as e:
    print(f"PyTorch check failed: {e}")

print()
print("All imports successful!")

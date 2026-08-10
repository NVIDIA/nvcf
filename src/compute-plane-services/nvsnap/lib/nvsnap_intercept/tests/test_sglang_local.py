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
Local SGLang test for checkpoint/restore debugging.

SGLang has different characteristics from vLLM:
- Different scheduler
- Different memory management
- May use different async patterns

Run with:
    LD_PRELOAD=/path/to/libgpucr_intercept.so python3 test_sglang_local.py
"""
import os
import sys
import time
import signal

# NOTE: Don't close FDs here - multiprocessing spawn needs them
# FD closing should only be done for simple single-process tests

# Write PID
with open("/tmp/sglang_local_pid", "w") as f:
    f.write(str(os.getpid()))

print(f"[SGLANG-TEST] PID={os.getpid()}", flush=True)
print(f"[SGLANG-TEST] GPUCR_RESTORED={os.environ.get('GPUCR_RESTORED', 'not set')}", flush=True)
print(f"[SGLANG-TEST] LD_PRELOAD={os.environ.get('LD_PRELOAD', 'not set')}", flush=True)

# Signal handlers
def signal_handler(sig, frame):
    print(f"[SGLANG-TEST] Received signal {sig}", flush=True)

signal.signal(signal.SIGUSR1, signal_handler)
signal.signal(signal.SIGUSR2, signal_handler)

print("[SGLANG-TEST] Importing sglang...", flush=True)

try:
    import sglang as sgl
    from sglang import RuntimeEndpoint
    
    print(f"[SGLANG-TEST] SGLang version: {sgl.__version__}", flush=True)
    
    # Check if we can use the simple generation API
    # SGLang has different API patterns than vLLM
    print("[SGLANG-TEST] Testing SGLang Engine...", flush=True)
    
    # Use the Engine class directly for offline inference
    from sglang import Engine
    
    print("[SGLANG-TEST] Creating Engine with TinyLlama...", flush=True)
    print("[SGLANG-TEST] This may take a minute to load...", flush=True)
    
    # SGLang Engine for offline inference
    engine = Engine(
        model_path="TinyLlama/TinyLlama-1.1B-Chat-v1.0",
        mem_fraction_static=0.5,  # Use less GPU memory
    )
    
    print("[SGLANG-TEST] Engine created successfully!", flush=True)
    
    # Do a simple generation
    print("[SGLANG-TEST] Running inference...", flush=True)
    prompts = ["Hello, how are you?"]
    
    outputs = engine.generate(prompts, max_new_tokens=50)
    
    for i, output in enumerate(outputs):
        print(f"[SGLANG-TEST] Prompt {i}: {prompts[i]!r}", flush=True)
        print(f"[SGLANG-TEST] Output {i}: {output!r}", flush=True)
    
    print("[SGLANG-TEST] Inference complete!", flush=True)
    print("[SGLANG-TEST] Entering idle loop (ready for checkpoint)...", flush=True)
    
    # Keep running for checkpoint
    counter = 0
    while True:
        counter += 1
        if counter % 30 == 0:
            print(f"[SGLANG-TEST] Idle, counter={counter}", flush=True)
        time.sleep(1)

except Exception as e:
    print(f"[SGLANG-TEST] ERROR: {type(e).__name__}: {e}", flush=True)
    import traceback
    traceback.print_exc()
    sys.exit(1)

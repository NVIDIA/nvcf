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
Local vLLM test for checkpoint/restore debugging.

This script starts vLLM with a small model, suitable for local testing.
Run with:
    LD_PRELOAD=/path/to/libgpucr_intercept.so python3 test_vllm_local.py
"""
import os
import sys
import time
import signal

# Close inherited FDs (important for CRIU)
for fd in range(3, 256):
    try:
        os.close(fd)
    except:
        pass

# Write PID
with open("/tmp/vllm_local_pid", "w") as f:
    f.write(str(os.getpid()))

print(f"[vLLM-TEST] PID={os.getpid()}", flush=True)
print(f"[vLLM-TEST] GPUCR_RESTORED={os.environ.get('GPUCR_RESTORED', 'not set')}", flush=True)
print(f"[vLLM-TEST] LD_PRELOAD={os.environ.get('LD_PRELOAD', 'not set')}", flush=True)

# Signal handlers
def signal_handler(sig, frame):
    print(f"[vLLM-TEST] Received signal {sig}", flush=True)

signal.signal(signal.SIGUSR1, signal_handler)
signal.signal(signal.SIGUSR2, signal_handler)

print("[vLLM-TEST] Importing vLLM...", flush=True)
from vllm import LLM, SamplingParams

print("[vLLM-TEST] Initializing model (TinyLlama)...", flush=True)
print("[vLLM-TEST] This may take a minute...", flush=True)

try:
    # Use a small model for testing
    llm = LLM(
        model="TinyLlama/TinyLlama-1.1B-Chat-v1.0",
        max_model_len=512,
        gpu_memory_utilization=0.5,
        trust_remote_code=True,
    )
    print("[vLLM-TEST] Model loaded successfully!", flush=True)
    
    # Do a simple inference
    sampling_params = SamplingParams(temperature=0.8, top_p=0.95, max_tokens=50)
    prompts = ["Hello, how are you?"]
    
    print("[vLLM-TEST] Running inference...", flush=True)
    outputs = llm.generate(prompts, sampling_params)
    
    for output in outputs:
        print(f"[vLLM-TEST] Prompt: {output.prompt!r}", flush=True)
        print(f"[vLLM-TEST] Output: {output.outputs[0].text!r}", flush=True)
    
    print("[vLLM-TEST] Inference complete!", flush=True)
    print("[vLLM-TEST] Entering idle loop (ready for checkpoint)...", flush=True)
    
    # Keep running for checkpoint
    counter = 0
    while True:
        counter += 1
        if counter % 30 == 0:
            print(f"[vLLM-TEST] Idle, counter={counter}", flush=True)
        time.sleep(1)
        
except Exception as e:
    print(f"[vLLM-TEST] ERROR: {type(e).__name__}: {e}", flush=True)
    import traceback
    traceback.print_exc()
    sys.exit(1)

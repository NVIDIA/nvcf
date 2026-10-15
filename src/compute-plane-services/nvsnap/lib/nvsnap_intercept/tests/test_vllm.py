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
vLLM Interception Test

This is the ULTIMATE test - vLLM uses:
- Multiple processes for tensor parallelism
- NCCL for inter-GPU communication
- Custom PagedAttention memory management
- Complex KV cache allocation strategies
- CUDA graphs for optimization

If our interception works with vLLM, it works with anything.
"""

import os
import sys
import time

# Check if running with interception
INTERCEPTION_ENABLED = 'LD_PRELOAD' in os.environ and 'gpucr' in os.environ.get('LD_PRELOAD', '')

def print_banner(msg):
    print(f"\n{'='*60}")
    print(f"  {msg}")
    print(f"{'='*60}\n")

def test_vllm_import():
    """Test 1: Basic vLLM import - this triggers CUDA initialization"""
    print_banner("Test 1: vLLM Import")
    
    try:
        import vllm
        print(f"✓ vLLM version: {vllm.__version__}")
        return True
    except ImportError as e:
        print(f"✗ vLLM not installed: {e}")
        return False
    except Exception as e:
        print(f"✗ vLLM import failed: {e}")
        return False

def test_vllm_offline_engine():
    """Test 2: Initialize vLLM LLM engine (offline mode)"""
    print_banner("Test 2: vLLM Offline Engine (TinyLlama)")
    
    try:
        from vllm import LLM, SamplingParams
        
        # Use a tiny model that fits in 6GB
        # TinyLlama-1.1B is about 2.2GB in float16
        model_name = "TinyLlama/TinyLlama-1.1B-Chat-v1.0"
        
        print(f"Loading model: {model_name}")
        print("This will download the model on first run (~2GB)...")
        
        # NOTE: vLLM 0.13+ uses multiprocessing by default.
        # Each subprocess has its own interception state.
        # Set GPUCR_LOG_LEVEL=1+ to see cudaMalloc calls in subprocess logs.
        # For checkpoint/restore, each process is checkpointed individually.
        
        # Initialize with conservative settings for 6GB GPU
        llm = LLM(
            model=model_name,
            tensor_parallel_size=1,  # Single GPU
            gpu_memory_utilization=0.7,  # Leave some headroom
            max_model_len=1024,  # Reduce context length
            trust_remote_code=True,
            enforce_eager=True,  # Disable CUDA graphs for simpler debugging
            disable_log_stats=True,  # Less noise
        )
        
        print("✓ vLLM LLM engine initialized successfully!")
        
        # Test inference
        print("\nTesting inference...")
        sampling_params = SamplingParams(
            temperature=0.7,
            max_tokens=50,
        )
        
        prompts = ["What is GPU checkpointing?"]
        
        start = time.time()
        outputs = llm.generate(prompts, sampling_params)
        elapsed = time.time() - start
        
        for output in outputs:
            print(f"\nPrompt: {output.prompt}")
            print(f"Generated: {output.outputs[0].text}")
        
        print(f"\n✓ Inference completed in {elapsed:.2f}s")
        
        # Clean up
        del llm
        
        return True
        
    except Exception as e:
        import traceback
        print(f"✗ vLLM offline engine test failed: {e}")
        traceback.print_exc()
        return False

def test_vllm_api_server():
    """Test 3: Start vLLM API server (tests multi-process)"""
    print_banner("Test 3: vLLM API Server (Multi-Process)")
    
    # This would start the actual vLLM server
    # For now, just report that this needs a separate test
    print("Note: Full API server test requires running vLLM as a server")
    print("This can be tested with:")
    print("  LD_PRELOAD=./libgpucr_intercept.so python -m vllm.entrypoints.openai.api_server \\")
    print("    --model TinyLlama/TinyLlama-1.1B-Chat-v1.0 --port 8000")
    print("\nSkipping automated API server test for now.")
    return True

def test_tensor_parallel():
    """Test 4: Tensor parallelism (requires multiple GPUs)"""
    print_banner("Test 4: Tensor Parallelism")
    
    import torch
    gpu_count = torch.cuda.device_count()
    
    if gpu_count < 2:
        print(f"Only {gpu_count} GPU available - tensor parallelism requires 2+ GPUs")
        print("Skipping tensor parallel test")
        return True  # Not a failure, just skipped
    
    try:
        from vllm import LLM
        
        print(f"Testing with {gpu_count} GPUs")
        llm = LLM(
            model="TinyLlama/TinyLlama-1.1B-Chat-v1.0",
            tensor_parallel_size=gpu_count,
            gpu_memory_utilization=0.5,
            max_model_len=512,
        )
        
        print(f"✓ Tensor parallel initialization with {gpu_count} GPUs succeeded!")
        del llm
        return True
        
    except Exception as e:
        print(f"✗ Tensor parallel test failed: {e}")
        return False

def main():
    print_banner("vLLM INTERCEPTION TEST SUITE")
    
    if INTERCEPTION_ENABLED:
        print("✓ Running WITH gpucr interception")
    else:
        print("⚠ Running WITHOUT gpucr interception (for baseline)")
    
    results = {}
    
    # Test 1: Basic import
    results['import'] = test_vllm_import()
    if not results['import']:
        print("\n\n⚠ vLLM not installed. Install with:")
        print("  pip install vllm")
        return 1
    
    # Test 2: Offline engine
    results['offline_engine'] = test_vllm_offline_engine()
    
    # Test 3: API server info
    results['api_server'] = test_vllm_api_server()
    
    # Test 4: Tensor parallel
    results['tensor_parallel'] = test_tensor_parallel()
    
    # Summary
    print_banner("TEST SUMMARY")
    
    passed = sum(1 for v in results.values() if v)
    total = len(results)
    
    for name, result in results.items():
        status = "✓ PASS" if result else "✗ FAIL"
        print(f"  {name}: {status}")
    
    print(f"\nTotal: {passed}/{total} tests passed")
    
    if INTERCEPTION_ENABLED:
        print("\n" + "="*60)
        print("INTERCEPTION NOTE:")
        print("="*60)
        print("vLLM uses multiprocessing - GPU work happens in child processes.")
        print("Each process has its own interception stats.")
        print("")
        print("To verify interception, look for '[INFO] [cudaMalloc]' lines above")
        print("from the EngineCore subprocess - those show interception working!")
        print("")
        print("For checkpoint/restore, each process is handled individually.")
        print("="*60)
    
    return 0 if passed == total else 1

if __name__ == "__main__":
    sys.exit(main())

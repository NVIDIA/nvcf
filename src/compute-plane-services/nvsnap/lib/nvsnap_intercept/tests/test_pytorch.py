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
Test: PyTorch Interception

This is the CRITICAL test - if interception works here, it works for real workloads.

This test progressively verifies:
1. Basic tensor creation (cudaMalloc)
2. Tensor operations (kernel launches)
3. cuBLAS operations (torch.mm)
4. cuDNN operations (torch.nn.Conv2d)
5. Multiple allocations and frees
6. Autograd backward pass

Run with:
    GPUCR_LOG_LEVEL=3 LD_PRELOAD=./libgpucr_intercept.so python3 test_pytorch.py
"""

import sys
import os

def check_gpucr_loaded():
    """Check if GPUCR interception library is loaded."""
    preload = os.environ.get('LD_PRELOAD', '')
    if 'libgpucr_intercept' not in preload:
        print("WARNING: GPUCR interception library not loaded!")
        print("Run with: LD_PRELOAD=./libgpucr_intercept.so python3 test_pytorch.py")
        return False
    return True

def test_basic_tensor():
    """Test 1: Basic tensor allocation."""
    print("\n=== Test 1: Basic Tensor Allocation ===")
    import torch
    
    print(f"PyTorch version: {torch.__version__}")
    print(f"CUDA available: {torch.cuda.is_available()}")
    
    if not torch.cuda.is_available():
        print("CUDA not available, skipping GPU tests")
        return False
    
    print(f"CUDA device: {torch.cuda.get_device_name(0)}")
    
    # Allocate tensor
    x = torch.zeros(1024, 1024, device='cuda')
    print(f"Allocated tensor: shape={x.shape}, dtype={x.dtype}, device={x.device}")
    print(f"Memory: {x.numel() * x.element_size()} bytes")
    
    # Verify it works
    x.fill_(42.0)
    assert x.mean().item() == 42.0, "Tensor verification failed"
    print("Tensor verification: PASSED")
    
    del x
    torch.cuda.synchronize()
    print("Freed tensor")
    
    return True

def test_tensor_operations():
    """Test 2: Tensor operations (kernel launches)."""
    print("\n=== Test 2: Tensor Operations ===")
    import torch
    
    a = torch.randn(1000, 1000, device='cuda')
    b = torch.randn(1000, 1000, device='cuda')
    
    # Element-wise operations
    c = a + b
    d = a * b
    e = torch.relu(c)
    
    print(f"Element-wise ops: shape={e.shape}")
    
    # Reduction
    mean = e.mean()
    print(f"Mean: {mean.item():.4f}")
    
    del a, b, c, d, e
    torch.cuda.synchronize()
    print("Tensor operations: PASSED")
    
    return True

def test_cublas():
    """Test 3: cuBLAS operations (matrix multiply)."""
    print("\n=== Test 3: cuBLAS Operations (torch.mm) ===")
    import torch
    
    # Matrix multiply uses cuBLAS
    a = torch.randn(512, 512, device='cuda')
    b = torch.randn(512, 512, device='cuda')
    
    c = torch.mm(a, b)
    print(f"Matrix multiply: {a.shape} x {b.shape} = {c.shape}")
    
    # Batched matmul
    a_batch = torch.randn(8, 64, 128, device='cuda')
    b_batch = torch.randn(8, 128, 64, device='cuda')
    c_batch = torch.bmm(a_batch, b_batch)
    print(f"Batched matmul: {a_batch.shape} x {b_batch.shape} = {c_batch.shape}")
    
    del a, b, c, a_batch, b_batch, c_batch
    torch.cuda.synchronize()
    print("cuBLAS operations: PASSED")
    
    return True

def test_cudnn():
    """Test 4: cuDNN operations (convolution)."""
    print("\n=== Test 4: cuDNN Operations (Conv2d) ===")
    import torch
    import torch.nn as nn
    
    # Convolution uses cuDNN
    conv = nn.Conv2d(3, 64, kernel_size=3, padding=1).cuda()
    x = torch.randn(1, 3, 224, 224, device='cuda')
    
    y = conv(x)
    print(f"Conv2d: {x.shape} -> {y.shape}")
    
    # BatchNorm also uses cuDNN
    bn = nn.BatchNorm2d(64).cuda()
    y = bn(y)
    print(f"BatchNorm2d: {y.shape}")
    
    # MaxPool
    pool = nn.MaxPool2d(2).cuda()
    y = pool(y)
    print(f"MaxPool2d: {y.shape}")
    
    del conv, bn, pool, x, y
    torch.cuda.synchronize()
    print("cuDNN operations: PASSED")
    
    return True

def test_autograd():
    """Test 5: Autograd backward pass."""
    print("\n=== Test 5: Autograd Backward Pass ===")
    import torch
    import torch.nn as nn
    
    # Simple model
    model = nn.Sequential(
        nn.Linear(100, 50),
        nn.ReLU(),
        nn.Linear(50, 10),
    ).cuda()
    
    x = torch.randn(32, 100, device='cuda', requires_grad=True)
    y = torch.randint(0, 10, (32,), device='cuda')
    
    # Forward
    output = model(x)
    loss = nn.functional.cross_entropy(output, y)
    print(f"Forward pass: loss={loss.item():.4f}")
    
    # Backward
    loss.backward()
    print(f"Backward pass completed")
    print(f"Input gradient shape: {x.grad.shape}")
    
    del model, x, y, output, loss
    torch.cuda.synchronize()
    print("Autograd: PASSED")
    
    return True

def test_memory_stress():
    """Test 6: Memory allocation stress test."""
    print("\n=== Test 6: Memory Allocation Stress ===")
    import torch
    
    tensors = []
    total_bytes = 0
    
    # Allocate many tensors
    for i in range(100):
        size = (256 + i * 10, 256 + i * 10)
        t = torch.randn(*size, device='cuda')
        tensors.append(t)
        total_bytes += t.numel() * t.element_size()
    
    print(f"Allocated {len(tensors)} tensors, total {total_bytes / 1024 / 1024:.1f} MB")
    
    # Free half
    for t in tensors[:50]:
        del t
    tensors = tensors[50:]
    torch.cuda.synchronize()
    print(f"Freed 50 tensors, {len(tensors)} remaining")
    
    # Allocate more
    for i in range(50):
        t = torch.randn(512, 512, device='cuda')
        tensors.append(t)
    print(f"Allocated 50 more, {len(tensors)} total")
    
    # Free all
    del tensors
    torch.cuda.synchronize()
    torch.cuda.empty_cache()
    print("Freed all tensors")
    
    print("Memory stress test: PASSED")
    return True

def print_memory_stats():
    """Print CUDA memory statistics."""
    import torch
    if torch.cuda.is_available():
        print("\n=== CUDA Memory Stats ===")
        print(f"Allocated: {torch.cuda.memory_allocated() / 1024 / 1024:.1f} MB")
        print(f"Cached: {torch.cuda.memory_reserved() / 1024 / 1024:.1f} MB")
        print(f"Max allocated: {torch.cuda.max_memory_allocated() / 1024 / 1024:.1f} MB")

def main():
    print("=" * 60)
    print("GPUCR PyTorch Interception Test")
    print("=" * 60)
    
    gpucr_loaded = check_gpucr_loaded()
    
    tests = [
        ("Basic Tensor", test_basic_tensor),
        ("Tensor Operations", test_tensor_operations),
        ("cuBLAS (torch.mm)", test_cublas),
        ("cuDNN (Conv2d)", test_cudnn),
        ("Autograd", test_autograd),
        ("Memory Stress", test_memory_stress),
    ]
    
    results = {}
    
    for name, test_fn in tests:
        try:
            if test_fn():
                results[name] = "PASSED"
            else:
                results[name] = "SKIPPED"
        except Exception as e:
            results[name] = f"FAILED: {e}"
            import traceback
            traceback.print_exc()
    
    print_memory_stats()
    
    print("\n" + "=" * 60)
    print("SUMMARY")
    print("=" * 60)
    for name, result in results.items():
        status = "✓" if result == "PASSED" else ("○" if result == "SKIPPED" else "✗")
        print(f"  {status} {name}: {result}")
    
    failed = sum(1 for r in results.values() if r.startswith("FAILED"))
    if failed > 0:
        print(f"\n{failed} test(s) FAILED")
        return 1
    
    if gpucr_loaded:
        print("\n✓ All tests passed with GPUCR interception!")
        print("  Check the logs above to verify allocations were tracked.")
    else:
        print("\n○ Tests passed but GPUCR was not loaded.")
        print("  Run with LD_PRELOAD to verify interception.")
    
    return 0

if __name__ == "__main__":
    sys.exit(main())

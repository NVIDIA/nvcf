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
Full feasibility test for CUDA interposition checkpoint/restore.

Must be run with LD_PRELOAD=./libcuda_intercept.so

Simulates the real checkpoint/restore flow:
  1. PyTorch creates tensors (calls cuMemAlloc_v2 internally)
  2. Our interceptor tracks all allocations
  3. We save GPU memory to host via cuMemcpyDtoH
  4. We free via cuMemFree_v2 (NOT through PyTorch — it doesn't know)
  5. We re-reserve at same VAs via cuMemAddressReserve + cuMemCreate + cuMemMap
  6. We restore data via cuMemcpyHtoD
  7. PyTorch tensors (still pointing to same VAs) should work

Run:
  cd tests/cuda_interpose && make
  LD_PRELOAD=./libcuda_intercept.so python3 test_full.py
"""

import ctypes
import os
import sys
import time

import torch

# Load our interception library
for path in ["libcuda_intercept.so", "./libcuda_intercept.so", "/tmp/libcuda_intercept.so"]:
    try:
        lib = ctypes.CDLL(path)
        break
    except OSError:
        continue
else:
    print("FAIL: Cannot load libcuda_intercept.so")
    sys.exit(1)

libcuda = ctypes.CDLL("libcuda.so.1")

# ── VMM structures ─────────────────────────────────────────────────────────

class CUmemLocation(ctypes.Structure):
    _fields_ = [("type", ctypes.c_int), ("id", ctypes.c_int)]

class CUmemAllocationProp(ctypes.Structure):
    _fields_ = [
        ("type", ctypes.c_int),
        ("requestedHandleTypes", ctypes.c_int),
        ("location", CUmemLocation),
        ("win32HandleMetaData", ctypes.c_void_p),
        ("allocFlags", ctypes.c_uint64),
    ]

class CUmemAccessDesc(ctypes.Structure):
    _fields_ = [("location", CUmemLocation), ("flags", ctypes.c_int)]

def check_cu(ret, msg):
    if ret != 0:
        raise RuntimeError(f"{msg}: CUDA driver error {ret}")

def get_granularity(device_id):
    prop = CUmemAllocationProp()
    prop.type = 1; prop.location.type = 1; prop.location.id = device_id
    g = ctypes.c_size_t()
    check_cu(libcuda.cuMemGetAllocationGranularity(
        ctypes.byref(g), ctypes.byref(prop), 1), "granularity")
    return g.value

def align_up(x, a): return ((x + a - 1) // a) * a
def align_down(x, a): return (x // a) * a

def get_live_allocs():
    max_n = 4096
    ptrs = (ctypes.c_uint64 * max_n)()
    sizes = (ctypes.c_uint64 * max_n)()
    devices = (ctypes.c_int * max_n)()
    n = lib.nvsnap_gpu_get_live_allocs(ptrs, sizes, devices, max_n)
    return [(ptrs[i], sizes[i], devices[i]) for i in range(n)]

# ── Checkpoint / Restore helpers ───────────────────────────────────────────

def checkpoint_gpu(allocs):
    """Save all GPU allocations to host, free GPU memory.
    Returns saved data for restore."""
    saved = []
    for addr, size, device in allocs:
        host_buf = (ctypes.c_ubyte * size)()
        check_cu(libcuda.cuMemcpyDtoH_v2(
            ctypes.byref(host_buf), ctypes.c_uint64(addr),
            ctypes.c_size_t(size)), f"D2H 0x{addr:x}")
        saved.append((addr, size, device, host_buf))
    return saved


def restore_gpu(saved):
    """Free each allocation and immediately re-reserve at the same VA.

    Key insight: if we batch all frees, the CUDA driver coalesces VA ranges
    and cuMemAddressReserve can't get specific sub-ranges back. By freeing
    and reserving one block at a time, each VA is available immediately.
    """
    if not saved:
        return 0, 0, []

    handles = []
    total_restored = 0
    total_failed = 0

    for addr, size, device, host_buf in saved:
        # Set CUDA context for this device
        cuda_dev = ctypes.c_int()
        libcuda.cuDeviceGet(ctypes.byref(cuda_dev), device)
        ctx = ctypes.c_void_p()
        libcuda.cuDevicePrimaryCtxRetain(ctypes.byref(ctx), cuda_dev)
        libcuda.cuCtxSetCurrent(ctx)

        granularity = get_granularity(device)
        aa = align_down(addr, granularity)
        asz = align_up(size + (addr - aa), granularity)

        # Free this specific allocation
        ret = libcuda.cuMemFree_v2(ctypes.c_uint64(addr))
        if ret != 0:
            print(f"  WARNING: cuMemFree_v2 0x{addr:x} failed: {ret}")

        # Immediately reserve at same address
        reserved = ctypes.c_uint64()
        ret = libcuda.cuMemAddressReserve(
            ctypes.byref(reserved), ctypes.c_size_t(asz),
            ctypes.c_size_t(granularity), ctypes.c_uint64(aa),
            ctypes.c_uint64(0))

        if ret != 0 or reserved.value != aa:
            actual = reserved.value if ret == 0 else 0
            print(f"  FAIL: reserve 0x{aa:x} size={asz} ret={ret} got=0x{actual:x}")
            if ret == 0:
                libcuda.cuMemAddressFree(reserved, ctypes.c_size_t(asz))
            total_failed += 1
            continue

        # Create physical backing + map + set access
        prop = CUmemAllocationProp()
        prop.type = 1; prop.location.type = 1; prop.location.id = device

        handle = ctypes.c_uint64()
        check_cu(libcuda.cuMemCreate(ctypes.byref(handle), ctypes.c_size_t(asz),
                 ctypes.byref(prop), ctypes.c_uint64(0)), "cuMemCreate")
        check_cu(libcuda.cuMemMap(reserved, ctypes.c_size_t(asz),
                 ctypes.c_size_t(0), handle, ctypes.c_uint64(0)), "cuMemMap")

        access = CUmemAccessDesc()
        access.location.type = 1; access.location.id = device; access.flags = 3
        check_cu(libcuda.cuMemSetAccess(reserved, ctypes.c_size_t(asz),
                 ctypes.byref(access), ctypes.c_size_t(1)), "cuMemSetAccess")

        # Restore data
        check_cu(libcuda.cuMemcpyHtoD_v2(
            ctypes.c_uint64(addr), ctypes.byref(host_buf),
            ctypes.c_size_t(size)), f"H2D restore 0x{addr:x}")

        handles.append((reserved.value, asz, handle))
        total_restored += 1

    return total_restored, total_failed, handles


# ── Tests ──────────────────────────────────────────────────────────────────

def test_1_basic_tensor():
    """Create tensor, checkpoint, restore, read data back."""
    print("=" * 60)
    print("TEST 1: Basic tensor checkpoint/restore")
    print("=" * 60)

    t = torch.randn(2048, 2048, device="cuda:0")
    expected_sum = t.sum().item()
    expected_first = t[0, :5].tolist()
    ptr = t.data_ptr()
    print(f"  Tensor: {t.shape} at 0x{ptr:x}, sum={expected_sum:.4f}")

    allocs = get_live_allocs()
    print(f"  Live allocations: {len(allocs)}, total {sum(s for _,s,_ in allocs)/1024/1024:.1f} MB")

    saved = checkpoint_gpu(allocs)
    print(f"  Saved {len(saved)} blocks, freed GPU memory")

    restored, failed, handles = restore_gpu(saved)
    print(f"  Restored {restored}/{len(saved)} (failed: {failed})")

    if failed > 0:
        print("  TEST 1: FAIL (not all blocks restored)")
        return False

    # Read data at original pointer
    actual_sum = t.sum().item()
    actual_first = t[0, :5].tolist()
    sum_match = abs(actual_sum - expected_sum) < 0.01
    first_match = all(abs(a - e) < 1e-5 for a, e in zip(actual_first, expected_first))

    print(f"  After restore: sum={actual_sum:.4f} (match: {sum_match})")
    print(f"  First 5 vals match: {first_match}")

    # Don't cleanup — leave VMM memory in place so PyTorch can continue using it.
    # In real C/R, CRIU handles process lifecycle.
    ok = sum_match and first_match
    print(f"  TEST 1: {'PASS' if ok else 'FAIL'}")
    return ok


def test_2_matmul():
    """Checkpoint, restore, then do matmul — tests kernel launch on restored memory."""
    print("\n" + "=" * 60)
    print("TEST 2: Matrix multiply after restore")
    print("=" * 60)

    w = torch.randn(1024, 1024, device="cuda:0")
    x = torch.randn(64, 1024, device="cuda:0")
    y_expected = (x @ w).clone()
    print(f"  w at 0x{w.data_ptr():x}, x at 0x{x.data_ptr():x}")
    print(f"  y_expected[0,:3]: {y_expected[0,:3].tolist()}")

    allocs = get_live_allocs()
    saved = checkpoint_gpu(allocs)
    restored, failed, handles = restore_gpu(saved)
    print(f"  Restored {restored}/{len(saved)} (failed: {failed})")

    if failed > 0:
        print("  TEST 2: FAIL")
        return False

    try:
        y_actual = x @ w
        match = torch.allclose(y_expected, y_actual, atol=1e-4)
        print(f"  y_actual[0,:3]: {y_actual[0,:3].tolist()}")
        print(f"  Matmul result matches: {match}")
    except Exception as e:
        print(f"  Matmul FAILED: {e}")
        print("  TEST 2: FAIL")
        return False

    print(f"  TEST 2: {'PASS' if match else 'FAIL'}")
    return match


def test_3_new_tensor():
    """After restore, create a NEW tensor and compute — tests allocator still works."""
    print("\n" + "=" * 60)
    print("TEST 3: New tensor allocation after restore")
    print("=" * 60)

    existing = torch.randn(512, 512, device="cuda:0")
    existing_sum = existing.sum().item()

    allocs = get_live_allocs()
    saved = checkpoint_gpu(allocs)
    restored, failed, handles = restore_gpu(saved)
    print(f"  Restored {restored}/{len(saved)} (failed: {failed})")

    if failed > 0:
        print("  TEST 3: FAIL")
        return False

    try:
        # Can we allocate NEW memory and compute?
        new_t = torch.randn(512, 512, device="cuda:0")
        result = existing @ new_t
        print(f"  New alloc + matmul: shape={result.shape}, norm={result.norm().item():.4f}")
        # Existing tensor data preserved?
        actual_sum = existing.sum().item()
        sum_ok = abs(actual_sum - existing_sum) < 0.01
        print(f"  Existing tensor sum: {actual_sum:.4f} (match: {sum_ok})")
        ok = sum_ok
    except Exception as e:
        print(f"  New tensor/compute FAILED: {e}")
        ok = False

    print(f"  TEST 3: {'PASS' if ok else 'FAIL'}")
    return ok


def test_4_large_model():
    """Simulate model weights: 8 layers x 64MB = 512MB."""
    print("\n" + "=" * 60)
    print("TEST 4: Large model simulation (512MB)")
    print("=" * 60)

    layers = [torch.randn(4096, 4096, device="cuda:0") for _ in range(8)]
    total_mb = sum(l.nelement() * 4 for l in layers) / 1024 / 1024
    print(f"  {len(layers)} layers, {total_mb:.0f} MB")

    # Forward pass before checkpoint
    x = torch.randn(32, 4096, device="cuda:0")
    y = x
    for w in layers:
        y = y @ w
    expected_norm = y.norm().item()
    print(f"  Forward pass norm: {expected_norm:.4f}")

    allocs = get_live_allocs()
    print(f"  Allocations: {len(allocs)}, total {sum(s for _,s,_ in allocs)/1024/1024:.0f} MB")

    t0 = time.time()
    saved = checkpoint_gpu(allocs)
    save_time = time.time() - t0

    t0 = time.time()
    restored, failed, handles = restore_gpu(saved)
    restore_time = time.time() - t0

    print(f"  Save: {save_time:.2f}s, Restore: {restore_time:.2f}s")
    print(f"  Restored {restored}/{len(saved)} (failed: {failed})")

    if failed > 0:
        print(f"  WARNING: {failed} blocks could not be restored (likely cuBLAS workspace)")
        print(f"  Testing if computation still works with {restored} restored blocks...")

    try:
        y2 = x
        for w in layers:
            y2 = y2 @ w
        actual_norm = y2.norm().item()
        rel_err = abs(actual_norm - expected_norm) / max(abs(expected_norm), 1e-10)
        match = rel_err < 0.01
        print(f"  Restored norm: {actual_norm:.4f} (rel_err: {rel_err:.6f}, match: {match})")
    except Exception as e:
        print(f"  Forward pass FAILED: {e}")
        print("  TEST 4: FAIL")
        return False

    print(f"  TEST 4: {'PASS' if match else 'FAIL'}")
    return match


def test_5_multi_gpu():
    """Tensors on multiple GPUs, checkpoint all, restore all."""
    print("\n" + "=" * 60)
    print("TEST 5: Multi-GPU checkpoint/restore")
    print("=" * 60)

    n_gpus = min(torch.cuda.device_count(), 4)
    if n_gpus < 2:
        print("  SKIP: need >= 2 GPUs")
        return True

    # Create one tensor per GPU
    tensors = []
    for i in range(n_gpus):
        t = torch.randn(1024, 1024, device=f"cuda:{i}")
        tensors.append((t, t.sum().item(), t.data_ptr()))
        print(f"  GPU {i}: 0x{t.data_ptr():x} sum={t.sum().item():.2f}")

    allocs = get_live_allocs()
    print(f"  Total allocations across {n_gpus} GPUs: {len(allocs)}")

    saved = checkpoint_gpu(allocs)
    restored, failed, handles = restore_gpu(saved)
    print(f"  Restored {restored}/{len(saved)} (failed: {failed})")

    if failed > 0:
        print("  TEST 5: FAIL")
        return False

    ok = True
    for i, (t, expected_sum, ptr) in enumerate(tensors):
        try:
            actual = t.sum().item()
            match = abs(actual - expected_sum) < 0.01
            print(f"  GPU {i}: sum={actual:.2f} (expected {expected_sum:.2f}) match={match}")
            if not match:
                ok = False
        except Exception as e:
            print(f"  GPU {i}: FAILED: {e}")
            ok = False

    print(f"  TEST 5: {'PASS' if ok else 'FAIL'}")
    return ok


def test_6_streams():
    """CUDA streams after restore."""
    print("\n" + "=" * 60)
    print("TEST 6: CUDA stream operations after restore")
    print("=" * 60)

    t = torch.randn(1024, 1024, device="cuda:0")
    expected = t.sum().item()
    stream = torch.cuda.Stream()

    allocs = get_live_allocs()
    saved = checkpoint_gpu(allocs)
    restored, failed, handles = restore_gpu(saved)
    print(f"  Restored {restored}/{len(saved)} (failed: {failed})")

    if failed > 0:
        print("  TEST 6: FAIL")
        return False

    try:
        with torch.cuda.stream(stream):
            result = t.sum().item()
        stream.synchronize()
        match = abs(result - expected) < 0.01
        print(f"  Stream compute: {result:.4f} (expected {expected:.4f}) match={match}")
    except Exception as e:
        print(f"  Stream compute FAILED: {e}")
        print("  TEST 6: FAIL")
        return False

    print(f"  TEST 6: {'PASS' if match else 'FAIL'}")
    return match


# ── Main ───────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    print(f"PyTorch {torch.__version__}, CUDA {torch.version.cuda}")
    print(f"GPUs: {torch.cuda.device_count()}\n")

    # Each test must run in a separate process because:
    # 1. After checkpoint/restore, VMM-backed memory confuses PyTorch's allocator
    # 2. cuMemFree_v2 can't free VMM-backed blocks from a previous test
    # 3. In real C/R, each checkpoint is a separate process lifecycle
    import subprocess
    tests = [
        "1_basic_tensor",
        "2_matmul",
        "3_new_tensor",
        "4_large_model",
        "5_multi_gpu",
        "6_streams",
    ]
    test_fn_map = {
        "1_basic_tensor": "test_1_basic_tensor",
        "2_matmul": "test_2_matmul",
        "3_new_tensor": "test_3_new_tensor",
        "4_large_model": "test_4_large_model",
        "5_multi_gpu": "test_5_multi_gpu",
        "6_streams": "test_6_streams",
    }

    # If a specific test is requested via argv, run it directly
    if len(sys.argv) > 1 and sys.argv[1].startswith("test_"):
        fn = globals()[sys.argv[1]]
        ok = fn()
        sys.exit(0 if ok else 1)

    # Otherwise, run each test as a subprocess
    results = {}
    for name in tests:
        fn_name = test_fn_map[name]
        env = dict(os.environ)
        env.setdefault("LD_PRELOAD", "/tmp/libcuda_intercept.so")
        proc = subprocess.run(
            [sys.executable, __file__, fn_name],
            env=env, capture_output=False, timeout=300,
        )
        results[name] = proc.returncode == 0

    print("\n" + "=" * 60)
    print("RESULTS")
    print("=" * 60)
    all_pass = True
    for name, ok in results.items():
        print(f"  {name}: {'PASS' if ok else 'FAIL'}")
        if not ok:
            all_pass = False
    print(f"\n  {'ALL PASS' if all_pass else 'SOME FAILED'}")
    sys.exit(0 if all_pass else 1)

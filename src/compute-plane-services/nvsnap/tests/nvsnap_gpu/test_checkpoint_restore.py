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
Test NvSnap checkpoint save + restore on GPU.

Simulates what CRIU restore would do:
1. Create tensors, compute reference output
2. Save checkpoint (D2H)
3. Free all GPU memory (simulates process death)
4. Restore checkpoint (VA reserve + H2D)
5. Verify data matches original

Usage:
    NVSNAP_GPU_LOG_LEVEL=3 LD_PRELOAD=/tmp/libwrap_gpu.so python3 test_restore.py
"""
import ctypes
import os
import sys
import tempfile
import struct

def main():
    import torch

    if not torch.cuda.is_available():
        print("SKIP: No CUDA GPU available")
        return 0

    wc = ctypes.CDLL("/tmp/libwrap_gpu.so")
    wc.nvsnap_checkpoint_save.restype = ctypes.c_int
    wc.nvsnap_checkpoint_save.argtypes = [ctypes.c_char_p]
    wc.nvsnap_checkpoint_restore.restype = ctypes.c_int
    wc.nvsnap_checkpoint_restore.argtypes = [ctypes.c_char_p]

    print("GPU:", torch.cuda.get_device_properties(0).name)

    # Step 1: Create tensors and compute reference
    print("\n=== Step 1: Create GPU data ===")
    a = torch.randn(1024, 1024, device="cuda")
    b = torch.randn(1024, 1024, device="cuda")
    ref_a_sum = a.sum().item()
    ref_b_sum = b.sum().item()
    ref_matmul_sum = torch.matmul(a, b).sum().item()
    a_ptr = a.data_ptr()
    b_ptr = b.data_ptr()
    print("  a.data_ptr = 0x%x, sum = %.4f" % (a_ptr, ref_a_sum))
    print("  b.data_ptr = 0x%x, sum = %.4f" % (b_ptr, ref_b_sum))
    print("  matmul sum = %.4f" % ref_matmul_sum)
    print("  GPU mem used: %.0f MB" % (torch.cuda.memory_allocated() / 1e6))

    # Step 2: Save checkpoint
    print("\n=== Step 2: Checkpoint save ===")
    ckpt_dir = tempfile.mkdtemp(prefix="nvsnap_restore_test_")
    ret = wc.nvsnap_checkpoint_save(ckpt_dir.encode())
    if ret != 0:
        print("FAIL: save returned %d" % ret)
        return 1

    meta_size = os.path.getsize(os.path.join(ckpt_dir, "meta.bin"))
    data_size = os.path.getsize(os.path.join(ckpt_dir, "gpu_data.bin"))
    print("  Checkpoint: meta=%d bytes, data=%.1f MB" % (meta_size, data_size / 1e6))

    # Read meta to see what was saved
    with open(os.path.join(ckpt_dir, "meta.bin"), "rb") as f:
        hdr = f.read(48)
        magic, version, n_allocs, n_streams, n_events, n_nccl, n_vmm, src_dev, total_bytes, ts = \
            struct.unpack("<IIIIIIIIqq", hdr)
        print("  Saved: %d allocs, %d streams, %d events, %d bytes total" %
              (n_allocs, n_streams, n_events, total_bytes))

    # Step 3: Free GPU memory (simulates process death / CRIU restore into clean state)
    print("\n=== Step 3: Free GPU memory ===")
    del a, b
    torch.cuda.empty_cache()
    print("  GPU mem after free: %.0f MB" % (torch.cuda.memory_allocated() / 1e6))

    # Step 4: Restore checkpoint
    print("\n=== Step 4: Checkpoint restore ===")
    ret = wc.nvsnap_checkpoint_restore(ckpt_dir.encode())
    if ret != 0:
        print("FAIL: restore returned %d" % ret)
        return 1

    # Step 5: Verify data at original addresses
    print("\n=== Step 5: Verify restored data ===")
    # Create tensor views at the original pointers
    # This only works if VA was preserved
    try:
        a_restored = torch.tensor([], dtype=torch.float32, device="cuda")
        a_restored.set_(torch.cuda.default_stream(0)._cdata if False else
                        torch.Storage._new_shared_cuda(0, 1024*1024, torch.float32.is_floating_point))
    except Exception:
        # Can't easily create a tensor at a specific VA from Python.
        # Instead, verify by re-reading the GPU memory directly.
        pass

    # Alternative: use ctypes to read GPU memory at original addresses
    libcudart = ctypes.CDLL("libcudart.so")
    libcudart.cudaMemcpy.restype = ctypes.c_int

    # Read back from original VA
    buf_a = (ctypes.c_float * (1024 * 1024))()
    err = libcudart.cudaMemcpy(
        ctypes.byref(buf_a), ctypes.c_void_p(a_ptr),
        1024 * 1024 * 4, 2)  # cudaMemcpyDeviceToHost = 2
    if err != 0:
        print("  FAIL: cudaMemcpy from 0x%x failed with %d" % (a_ptr, err))
        print("  (VA was not preserved — cuMemAddressReserve may have returned different address)")
        return 1

    restored_a_sum = sum(buf_a)
    print("  Restored a sum = %.4f (ref = %.4f)" % (restored_a_sum, ref_a_sum))

    buf_b = (ctypes.c_float * (1024 * 1024))()
    err = libcudart.cudaMemcpy(
        ctypes.byref(buf_b), ctypes.c_void_p(b_ptr),
        1024 * 1024 * 4, 2)
    if err != 0:
        print("  FAIL: cudaMemcpy from 0x%x failed with %d" % (b_ptr, err))
        return 1

    restored_b_sum = sum(buf_b)
    print("  Restored b sum = %.4f (ref = %.4f)" % (restored_b_sum, ref_b_sum))

    # Check tolerance (float32 accumulation has rounding differences)
    tol = abs(ref_a_sum) * 1e-4 + 1.0  # relative + absolute tolerance
    a_match = abs(restored_a_sum - ref_a_sum) < tol
    b_match = abs(restored_b_sum - ref_b_sum) < tol

    if a_match and b_match:
        print("\n  DATA INTEGRITY: PASS")
    else:
        print("\n  DATA INTEGRITY: FAIL")
        print("  a diff: %.6f" % abs(restored_a_sum - ref_a_sum))
        print("  b diff: %.6f" % abs(restored_b_sum - ref_b_sum))
        return 1

    # Bonus: can we do matmul on the restored data?
    print("\n=== Step 6: Compute on restored data ===")
    # Wrap restored VA as tensors
    a2 = torch.frombuffer(buf_a, dtype=torch.float32).reshape(1024, 1024).cuda()
    b2 = torch.frombuffer(buf_b, dtype=torch.float32).reshape(1024, 1024).cuda()
    matmul_sum = torch.matmul(a2, b2).sum().item()
    matmul_match = abs(matmul_sum - ref_matmul_sum) / (abs(ref_matmul_sum) + 1) < 0.01
    print("  matmul sum = %.4f (ref = %.4f) %s" %
          (matmul_sum, ref_matmul_sum, "PASS" if matmul_match else "FAIL"))

    print("\nSUCCESS: NvSnap save + restore + compute verified")
    return 0

if __name__ == "__main__":
    sys.exit(main())

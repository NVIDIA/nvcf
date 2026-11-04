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
Feasibility test: Can we save GPU memory, free it, and re-allocate at the same
virtual address using CUDA VMM APIs?

Uses only the CUDA driver API (libcuda.so) — no runtime needed.

Run on any machine with an NVIDIA GPU:
  python3 tests/cuda_va_preserve_test.py
"""

import ctypes
import sys

def check(ret, msg):
    if ret != 0:
        print(f"FAIL: {msg}: error {ret}")
        sys.exit(1)

def main():
    libcuda = ctypes.CDLL("libcuda.so.1")

    # Init
    check(libcuda.cuInit(0), "cuInit")

    # Get device
    device = ctypes.c_int()
    check(libcuda.cuDeviceGet(ctypes.byref(device), 0), "cuDeviceGet")

    name = (ctypes.c_char * 256)()
    libcuda.cuDeviceGetName(name, 256, device)
    print(f"Device: {name.value.decode()}")

    # Check VMM support
    vmm = ctypes.c_int()
    check(libcuda.cuDeviceGetAttribute(ctypes.byref(vmm), 102, device), "VMM check")
    print(f"VMM supported: {vmm.value}")
    if not vmm.value:
        print("FAIL: VMM not supported")
        sys.exit(1)

    # Create context
    ctx = ctypes.c_void_p()
    check(libcuda.cuCtxCreate_v2(ctypes.byref(ctx), 0, device), "cuCtxCreate")

    # --- Step 1: cuMemAlloc ---
    SIZE = 1024 * 1024  # 1 MB
    dev_ptr = ctypes.c_uint64()
    check(libcuda.cuMemAlloc_v2(ctypes.byref(dev_ptr), ctypes.c_size_t(SIZE)), "cuMemAlloc")
    original_addr = dev_ptr.value
    print(f"\nStep 1: cuMemAlloc address: 0x{original_addr:x}")

    # --- Step 1.5: Write pattern ---
    pattern = (ctypes.c_ubyte * SIZE)(*[i % 256 for i in range(SIZE)])
    check(libcuda.cuMemcpyHtoD_v2(dev_ptr, ctypes.byref(pattern), ctypes.c_size_t(SIZE)), "H2D write")
    print("Step 1.5: Wrote test pattern")

    # --- Step 2: Save to host ---
    saved = (ctypes.c_ubyte * SIZE)()
    check(libcuda.cuMemcpyDtoH_v2(ctypes.byref(saved), dev_ptr, ctypes.c_size_t(SIZE)), "D2H save")
    print("Step 2: Saved to host")

    # --- Step 3: Free ---
    check(libcuda.cuMemFree_v2(dev_ptr), "cuMemFree")
    print("Step 3: Freed GPU memory")

    # --- Step 4: Reserve same address via VMM ---
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

    prop = CUmemAllocationProp()
    prop.type = 1  # PINNED
    prop.location.type = 1  # DEVICE
    prop.location.id = device.value

    granularity = ctypes.c_size_t()
    check(libcuda.cuMemGetAllocationGranularity(
        ctypes.byref(granularity), ctypes.byref(prop), 1), "granularity")
    g = granularity.value
    print(f"\nStep 4: Granularity: {g} bytes ({g // 1024 // 1024} MB)")

    aligned_size = ((SIZE + g - 1) // g) * g
    aligned_addr = (original_addr // g) * g

    if aligned_addr != original_addr:
        print(f"  Original 0x{original_addr:x} not aligned, using 0x{aligned_addr:x}")

    reserved = ctypes.c_uint64()
    ret = libcuda.cuMemAddressReserve(
        ctypes.byref(reserved),
        ctypes.c_size_t(aligned_size),
        ctypes.c_size_t(g),
        ctypes.c_uint64(aligned_addr),
        ctypes.c_uint64(0),
    )

    if ret != 0:
        print(f"  cuMemAddressReserve at 0x{aligned_addr:x} FAILED (error {ret})")

        # Try without hint
        ret2 = libcuda.cuMemAddressReserve(
            ctypes.byref(reserved),
            ctypes.c_size_t(aligned_size),
            ctypes.c_size_t(g),
            ctypes.c_uint64(0),
            ctypes.c_uint64(0),
        )
        if ret2 == 0:
            print(f"  Without hint: got 0x{reserved.value:x} (different from 0x{original_addr:x})")
        print("\n  RESULT: VA preservation FAILED — interposition approach blocked")
        sys.exit(1)

    print(f"Step 4: Reserved 0x{reserved.value:x} (requested 0x{aligned_addr:x})")
    addr_match = reserved.value == aligned_addr
    print(f"  Address match: {addr_match}")

    # --- Step 5: Create physical allocation + map ---
    handle = ctypes.c_uint64()
    check(libcuda.cuMemCreate(
        ctypes.byref(handle), ctypes.c_size_t(aligned_size),
        ctypes.byref(prop), ctypes.c_uint64(0)), "cuMemCreate")

    check(libcuda.cuMemMap(
        reserved, ctypes.c_size_t(aligned_size),
        ctypes.c_size_t(0), handle, ctypes.c_uint64(0)), "cuMemMap")

    class CUmemAccessDesc(ctypes.Structure):
        _fields_ = [("location", CUmemLocation), ("flags", ctypes.c_int)]

    access = CUmemAccessDesc()
    access.location.type = 1
    access.location.id = device.value
    access.flags = 3  # READ_WRITE

    check(libcuda.cuMemSetAccess(
        reserved, ctypes.c_size_t(aligned_size),
        ctypes.byref(access), ctypes.c_size_t(1)), "cuMemSetAccess")
    print("Step 5: Physical allocation mapped")

    # --- Step 6: Restore data ---
    check(libcuda.cuMemcpyHtoD_v2(
        ctypes.c_uint64(reserved.value),
        ctypes.byref(saved), ctypes.c_size_t(SIZE)), "H2D restore")
    print("Step 6: Data restored to GPU")

    # --- Step 7: Verify ---
    verify = (ctypes.c_ubyte * SIZE)()
    check(libcuda.cuMemcpyDtoH_v2(
        ctypes.byref(verify),
        ctypes.c_uint64(reserved.value), ctypes.c_size_t(SIZE)), "D2H verify")

    mismatches = sum(1 for i in range(SIZE) if verify[i] != pattern[i])

    print(f"\nStep 7: Verification: {SIZE - mismatches}/{SIZE} bytes match")

    if mismatches == 0 and addr_match:
        print("\n" + "=" * 60)
        print("RESULT: GPU VA PRESERVATION IS FEASIBLE")
        print(f"  cuMemAlloc address:       0x{original_addr:x}")
        print(f"  cuMemAddressReserve addr: 0x{reserved.value:x}")
        print(f"  Address preserved: YES")
        print(f"  Data preserved:    YES")
        print("=" * 60)
    elif mismatches == 0:
        print("\n" + "=" * 60)
        print("RESULT: DATA PRESERVATION WORKS, BUT ADDRESS CHANGED")
        print(f"  Original: 0x{original_addr:x}")
        print(f"  New:      0x{reserved.value:x}")
        print("  Pointer remapping would be needed")
        print("=" * 60)
    else:
        print(f"\nFAIL: {mismatches} mismatches")
        sys.exit(1)

    # Cleanup
    libcuda.cuMemUnmap(reserved, ctypes.c_size_t(aligned_size))
    libcuda.cuMemRelease(handle)
    libcuda.cuMemAddressFree(reserved, ctypes.c_size_t(aligned_size))
    libcuda.cuCtxDestroy_v2(ctx)


if __name__ == "__main__":
    main()

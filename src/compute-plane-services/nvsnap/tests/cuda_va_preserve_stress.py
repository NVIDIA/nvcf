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
Stress test: GPU VA preservation with realistic allocation patterns.

Tests:
  1. Many allocations (100+) of varying sizes
  2. Large allocations (256MB+)
  3. Interleaved alloc/free (fragmented VA space)
  4. Re-reserve ALL freed addresses at once
  5. Verify all data intact

Run: python3 tests/cuda_va_preserve_stress.py
"""

import ctypes
import random
import sys
import time

# ── CUDA driver API setup ──────────────────────────────────────────────────

libcuda = ctypes.CDLL("libcuda.so.1")

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

def check(ret, msg):
    if ret != 0:
        print(f"FAIL: {msg}: CUDA error {ret}")
        sys.exit(1)

def get_granularity(device_id):
    prop = CUmemAllocationProp()
    prop.type = 1
    prop.location.type = 1
    prop.location.id = device_id
    g = ctypes.c_size_t()
    check(libcuda.cuMemGetAllocationGranularity(
        ctypes.byref(g), ctypes.byref(prop), 1), "granularity")
    return g.value

def align_up(size, alignment):
    return ((size + alignment - 1) // alignment) * alignment

def align_down(addr, alignment):
    return (addr // alignment) * alignment


# ── Allocation tracking ────────────────────────────────────────────────────

class TrackedAlloc:
    def __init__(self, dev_ptr, size, seed):
        self.dev_ptr = dev_ptr
        self.size = size
        self.seed = seed  # deterministic pattern
        self.host_backup = None

    def write_pattern(self):
        """Write a deterministic byte pattern based on seed."""
        random.seed(self.seed)
        # Only write first and last 4KB + random samples for large allocs
        chunk = min(self.size, 4096)
        pattern = (ctypes.c_ubyte * chunk)(*[random.randint(0, 255) for _ in range(chunk)])
        check(libcuda.cuMemcpyHtoD_v2(
            ctypes.c_uint64(self.dev_ptr), ctypes.byref(pattern),
            ctypes.c_size_t(chunk)), f"H2D pattern @0x{self.dev_ptr:x}")
        if self.size > 4096:
            # Write last 4KB too
            random.seed(self.seed + 1)
            tail = (ctypes.c_ubyte * chunk)(*[random.randint(0, 255) for _ in range(chunk)])
            check(libcuda.cuMemcpyHtoD_v2(
                ctypes.c_uint64(self.dev_ptr + self.size - chunk),
                ctypes.byref(tail), ctypes.c_size_t(chunk)),
                f"H2D tail @0x{self.dev_ptr:x}")

    def save_to_host(self):
        """Copy entire GPU allocation to host."""
        self.host_backup = (ctypes.c_ubyte * self.size)()
        check(libcuda.cuMemcpyDtoH_v2(
            ctypes.byref(self.host_backup),
            ctypes.c_uint64(self.dev_ptr),
            ctypes.c_size_t(self.size)), f"D2H save @0x{self.dev_ptr:x}")

    def verify_pattern(self, addr):
        """Verify first and last 4KB match expected pattern."""
        chunk = min(self.size, 4096)
        buf = (ctypes.c_ubyte * chunk)()
        check(libcuda.cuMemcpyDtoH_v2(
            ctypes.byref(buf), ctypes.c_uint64(addr),
            ctypes.c_size_t(chunk)), f"D2H verify @0x{addr:x}")

        random.seed(self.seed)
        expected = [random.randint(0, 255) for _ in range(chunk)]
        head_ok = all(buf[i] == expected[i] for i in range(chunk))

        tail_ok = True
        if self.size > 4096:
            tail = (ctypes.c_ubyte * chunk)()
            check(libcuda.cuMemcpyDtoH_v2(
                ctypes.byref(tail), ctypes.c_uint64(addr + self.size - chunk),
                ctypes.c_size_t(chunk)), f"D2H verify tail @0x{addr:x}")
            random.seed(self.seed + 1)
            expected_tail = [random.randint(0, 255) for _ in range(chunk)]
            tail_ok = all(tail[i] == expected_tail[i] for i in range(chunk))

        return head_ok and tail_ok


def main():
    check(libcuda.cuInit(0), "cuInit")

    device = ctypes.c_int()
    check(libcuda.cuDeviceGet(ctypes.byref(device), 0), "cuDeviceGet")

    name = (ctypes.c_char * 256)()
    libcuda.cuDeviceGetName(name, 256, device)
    print(f"Device: {name.value.decode()}")

    mem_total = ctypes.c_size_t()
    libcuda.cuDeviceTotalMem_v2(ctypes.byref(mem_total), device)
    print(f"Memory: {mem_total.value // 1024 // 1024} MB")

    ctx = ctypes.c_void_p()
    check(libcuda.cuCtxCreate_v2(ctypes.byref(ctx), 0, device), "cuCtxCreate")

    granularity = get_granularity(device.value)
    print(f"Granularity: {granularity // 1024} KB")

    # ── Test 1: Many small allocations ──────────────────────────────────
    print(f"\n{'='*60}")
    print("TEST 1: 100 allocations of varying sizes (1KB - 64MB)")
    print(f"{'='*60}")

    allocs = []
    sizes = [1024, 4096, 65536, 1024*1024, 4*1024*1024, 16*1024*1024, 64*1024*1024]

    for i in range(100):
        size = sizes[i % len(sizes)]
        ptr = ctypes.c_uint64()
        check(libcuda.cuMemAlloc_v2(ctypes.byref(ptr), ctypes.c_size_t(size)), f"alloc #{i}")
        a = TrackedAlloc(ptr.value, size, seed=i*7+42)
        a.write_pattern()
        allocs.append(a)

    print(f"  Allocated {len(allocs)} buffers, total {sum(a.size for a in allocs) / 1024 / 1024:.0f} MB")
    print(f"  VA range: 0x{min(a.dev_ptr for a in allocs):x} - 0x{max(a.dev_ptr + a.size for a in allocs):x}")

    # Save all to host
    t0 = time.time()
    for a in allocs:
        a.save_to_host()
    save_time = time.time() - t0
    print(f"  Saved to host in {save_time:.2f}s")

    # Free all
    for a in allocs:
        check(libcuda.cuMemFree_v2(ctypes.c_uint64(a.dev_ptr)), f"free 0x{a.dev_ptr:x}")
    print(f"  Freed all {len(allocs)} allocations")

    # Reserve all at original addresses
    restored = 0
    failed = 0
    t0 = time.time()
    handles = []

    for a in allocs:
        aligned_addr = align_down(a.dev_ptr, granularity)
        aligned_size = align_up(a.size + (a.dev_ptr - aligned_addr), granularity)

        reserved = ctypes.c_uint64()
        ret = libcuda.cuMemAddressReserve(
            ctypes.byref(reserved), ctypes.c_size_t(aligned_size),
            ctypes.c_size_t(granularity), ctypes.c_uint64(aligned_addr),
            ctypes.c_uint64(0))

        if ret != 0:
            print(f"  FAIL: cuMemAddressReserve 0x{aligned_addr:x} (size={aligned_size}) -> error {ret}")
            failed += 1
            handles.append(None)
            continue

        if reserved.value != aligned_addr:
            print(f"  WARN: got 0x{reserved.value:x} instead of 0x{aligned_addr:x}")
            failed += 1
            libcuda.cuMemAddressFree(reserved, ctypes.c_size_t(aligned_size))
            handles.append(None)
            continue

        # Create + map
        prop = CUmemAllocationProp()
        prop.type = 1
        prop.location.type = 1
        prop.location.id = device.value

        handle = ctypes.c_uint64()
        check(libcuda.cuMemCreate(
            ctypes.byref(handle), ctypes.c_size_t(aligned_size),
            ctypes.byref(prop), ctypes.c_uint64(0)), "cuMemCreate")

        check(libcuda.cuMemMap(
            reserved, ctypes.c_size_t(aligned_size),
            ctypes.c_size_t(0), handle, ctypes.c_uint64(0)), "cuMemMap")

        access = CUmemAccessDesc()
        access.location.type = 1
        access.location.id = device.value
        access.flags = 3

        check(libcuda.cuMemSetAccess(
            reserved, ctypes.c_size_t(aligned_size),
            ctypes.byref(access), ctypes.c_size_t(1)), "cuMemSetAccess")

        # Restore data
        check(libcuda.cuMemcpyHtoD_v2(
            ctypes.c_uint64(a.dev_ptr),
            ctypes.byref(a.host_backup),
            ctypes.c_size_t(a.size)), "H2D restore")

        handles.append((reserved.value, aligned_size, handle))
        restored += 1

    restore_time = time.time() - t0
    print(f"  Restored {restored}/{len(allocs)} allocations in {restore_time:.2f}s (failed: {failed})")

    # Verify
    verified = 0
    for i, a in enumerate(allocs):
        if handles[i] is None:
            continue
        if a.verify_pattern(a.dev_ptr):
            verified += 1
        else:
            print(f"  DATA MISMATCH at alloc #{i} (0x{a.dev_ptr:x}, {a.size} bytes)")

    print(f"  Verified: {verified}/{restored}")

    # Cleanup
    for h in handles:
        if h is not None:
            addr, size, handle = h
            libcuda.cuMemUnmap(ctypes.c_uint64(addr), ctypes.c_size_t(size))
            libcuda.cuMemRelease(handle)
            libcuda.cuMemAddressFree(ctypes.c_uint64(addr), ctypes.c_size_t(size))

    # ── Test 2: Large allocation (256MB) ────────────────────────────────
    print(f"\n{'='*60}")
    print("TEST 2: Single large allocation (256 MB)")
    print(f"{'='*60}")

    BIG = 256 * 1024 * 1024
    ptr = ctypes.c_uint64()
    check(libcuda.cuMemAlloc_v2(ctypes.byref(ptr), ctypes.c_size_t(BIG)), "big alloc")
    big = TrackedAlloc(ptr.value, BIG, seed=9999)
    big.write_pattern()
    print(f"  Allocated at 0x{ptr.value:x}")

    t0 = time.time()
    big.save_to_host()
    print(f"  Saved {BIG // 1024 // 1024} MB in {time.time() - t0:.2f}s")

    check(libcuda.cuMemFree_v2(ctypes.c_uint64(ptr.value)), "big free")

    aligned_addr = align_down(ptr.value, granularity)
    aligned_size = align_up(BIG + (ptr.value - aligned_addr), granularity)

    reserved = ctypes.c_uint64()
    ret = libcuda.cuMemAddressReserve(
        ctypes.byref(reserved), ctypes.c_size_t(aligned_size),
        ctypes.c_size_t(granularity), ctypes.c_uint64(aligned_addr),
        ctypes.c_uint64(0))

    if ret != 0:
        print(f"  FAIL: cuMemAddressReserve for 256MB at 0x{aligned_addr:x}: error {ret}")
    else:
        print(f"  Reserved at 0x{reserved.value:x} (match: {reserved.value == aligned_addr})")

        prop = CUmemAllocationProp()
        prop.type = 1
        prop.location.type = 1
        prop.location.id = device.value
        handle = ctypes.c_uint64()
        check(libcuda.cuMemCreate(ctypes.byref(handle), ctypes.c_size_t(aligned_size),
              ctypes.byref(prop), ctypes.c_uint64(0)), "big create")
        check(libcuda.cuMemMap(reserved, ctypes.c_size_t(aligned_size),
              ctypes.c_size_t(0), handle, ctypes.c_uint64(0)), "big map")
        access = CUmemAccessDesc()
        access.location.type = 1
        access.location.id = device.value
        access.flags = 3
        check(libcuda.cuMemSetAccess(reserved, ctypes.c_size_t(aligned_size),
              ctypes.byref(access), ctypes.c_size_t(1)), "big access")

        t0 = time.time()
        check(libcuda.cuMemcpyHtoD_v2(ctypes.c_uint64(ptr.value),
              ctypes.byref(big.host_backup), ctypes.c_size_t(BIG)), "big H2D")
        print(f"  Restored {BIG // 1024 // 1024} MB in {time.time() - t0:.2f}s")

        if big.verify_pattern(ptr.value):
            print("  Data verified OK")
        else:
            print("  DATA MISMATCH")

        libcuda.cuMemUnmap(reserved, ctypes.c_size_t(aligned_size))
        libcuda.cuMemRelease(handle)
        libcuda.cuMemAddressFree(reserved, ctypes.c_size_t(aligned_size))

    # ── Test 3: Fragmented VA space ─────────────────────────────────────
    print(f"\n{'='*60}")
    print("TEST 3: Fragmented VA space (alloc A B C, free B, free A C)")
    print(f"{'='*60}")

    ptrs = []
    for i in range(10):
        p = ctypes.c_uint64()
        check(libcuda.cuMemAlloc_v2(ctypes.byref(p), ctypes.c_size_t(4*1024*1024)), f"frag alloc {i}")
        ptrs.append(p.value)

    # Free odd indices first, then even
    for i in [1, 3, 5, 7, 9]:
        check(libcuda.cuMemFree_v2(ctypes.c_uint64(ptrs[i])), f"frag free {i}")
    for i in [0, 2, 4, 6, 8]:
        check(libcuda.cuMemFree_v2(ctypes.c_uint64(ptrs[i])), f"frag free {i}")

    # Try to reserve all original addresses
    success = 0
    for i, addr in enumerate(ptrs):
        aa = align_down(addr, granularity)
        asz = align_up(4*1024*1024 + (addr - aa), granularity)
        r = ctypes.c_uint64()
        ret = libcuda.cuMemAddressReserve(
            ctypes.byref(r), ctypes.c_size_t(asz),
            ctypes.c_size_t(granularity), ctypes.c_uint64(aa), ctypes.c_uint64(0))
        if ret == 0 and r.value == aa:
            success += 1
            libcuda.cuMemAddressFree(r, ctypes.c_size_t(asz))
        elif ret == 0:
            libcuda.cuMemAddressFree(r, ctypes.c_size_t(asz))

    print(f"  Reserved {success}/{len(ptrs)} original addresses after fragmented free")

    # ── Summary ─────────────────────────────────────────────────────────
    print(f"\n{'='*60}")
    print("SUMMARY")
    print(f"{'='*60}")
    print(f"  Test 1 (100 allocs):   {restored}/{len(allocs)} restored, {verified}/{restored} verified")
    print(f"  Test 2 (256MB):        {'PASS' if ret == 0 else 'FAIL'}")
    print(f"  Test 3 (fragmented):   {success}/10 addresses recovered")

    libcuda.cuCtxDestroy_v2(ctx)


if __name__ == "__main__":
    main()

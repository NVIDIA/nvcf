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

"""Debug which allocations can/cannot be re-reserved after free."""
import ctypes
import torch

libcuda = ctypes.CDLL("libcuda.so.1")
lib = ctypes.CDLL("/tmp/libcuda_intercept.so")

# Create tensors + trigger cuBLAS
w = torch.randn(4096, 4096, device="cuda:0")
x = torch.randn(32, 4096, device="cuda:0")
y = x @ w

max_n = 4096
ptrs = (ctypes.c_uint64 * max_n)()
sizes = (ctypes.c_uint64 * max_n)()
devices = (ctypes.c_int * max_n)()
n = lib.nvsnap_gpu_get_live_allocs(ptrs, sizes, devices, max_n)

GRAN = 2 * 1024 * 1024

print(f"Live allocs: {n}")
for i in range(n):
    addr = ptrs[i]
    size = sizes[i]
    print(f"\n  [{i}] 0x{addr:x} size={size/1024/1024:.1f}MB dev={devices[i]}")

    # Try free
    ret = libcuda.cuMemFree_v2(ctypes.c_uint64(addr))
    print(f"      cuMemFree_v2: {'OK' if ret == 0 else f'err {ret}'}")

    # Try reserve
    aa = (addr // GRAN) * GRAN
    asz = ((size + (addr - aa) + GRAN - 1) // GRAN) * GRAN
    reserved = ctypes.c_uint64()
    ret2 = libcuda.cuMemAddressReserve(
        ctypes.byref(reserved), ctypes.c_size_t(asz),
        ctypes.c_size_t(GRAN), ctypes.c_uint64(aa), ctypes.c_uint64(0))

    if ret2 == 0 and reserved.value == aa:
        print(f"      cuMemAddressReserve: OK at 0x{aa:x}")
        libcuda.cuMemAddressFree(reserved, ctypes.c_size_t(asz))
    elif ret2 == 0:
        print(f"      cuMemAddressReserve: WRONG ADDR (wanted 0x{aa:x}, got 0x{reserved.value:x})")
        libcuda.cuMemAddressFree(reserved, ctypes.c_size_t(asz))
    else:
        print(f"      cuMemAddressReserve: FAILED err {ret2}")

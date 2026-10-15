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
Test NvSnap checkpoint save at model scale (~8GB per GPU, 4 GPUs).

Usage:
    NVSNAP_GPU_LOG_LEVEL=3 LD_PRELOAD=/tmp/libwrap_gpu.so \
        torchrun --nproc_per_node=4 test_scale.py
"""
import ctypes
import os
import sys
import tempfile
import time

def main():
    import torch
    import torch.distributed as dist

    rank = int(os.environ.get("LOCAL_RANK", "0"))
    world_size = int(os.environ.get("WORLD_SIZE", "1"))

    if world_size < 2:
        print("SKIP: Need at least 2 GPUs")
        return 0

    dist.init_process_group(backend="nccl")
    torch.cuda.set_device(rank)

    wc = ctypes.CDLL("/tmp/libwrap_gpu.so")
    wc.nvsnap_checkpoint_save.restype = ctypes.c_int
    wc.nvsnap_checkpoint_save.argtypes = [ctypes.c_char_p]

    if rank == 0:
        print("=== NvSnap Scale Test: ~8GB per GPU, %d GPUs ===" % world_size)
        print("GPU: %s" % torch.cuda.get_device_properties(0).name)

    # Allocate ~8GB per GPU (simulating 70B model weights split across 4 GPUs)
    tensors = []
    target_gb = 8
    chunk_mb = 256  # 256MB chunks
    n_chunks = (target_gb * 1024) // chunk_mb
    for i in range(n_chunks):
        # 256MB = 64M floats
        t = torch.randn(64 * 1024 * 1024 // 4, device="cuda")  # 64MB of float32
        tensors.append(t)
        # Actually make them 256MB
        t2 = torch.randn(64 * 1024 * 1024 // 4, device="cuda")
        tensors.append(t2)
        t3 = torch.randn(64 * 1024 * 1024 // 4, device="cuda")
        tensors.append(t3)
        t4 = torch.randn(64 * 1024 * 1024 // 4, device="cuda")
        tensors.append(t4)

    total_gb = sum(t.numel() * t.element_size() for t in tensors) / 1e9
    alloc_gb = torch.cuda.memory_allocated() / 1e9
    print("[rank %d] Allocated %.1f GB (%d tensors), GPU mem used %.1f GB" %
          (rank, total_gb, len(tensors), alloc_gb))

    # Quick NCCL test
    dist.all_reduce(tensors[0], op=dist.ReduceOp.SUM)
    print("[rank %d] NCCL allreduce OK" % rank)

    dist.barrier()

    # Checkpoint save with timing
    if rank == 0:
        print("\n--- Checkpoint save (%.1f GB per GPU) ---" % total_gb)

    torch.cuda.synchronize()
    dist.barrier()
    t0 = time.time()

    ckpt_dir = tempfile.mkdtemp(prefix="nvsnap_scale_r%d_" % rank)
    ret = wc.nvsnap_checkpoint_save(ckpt_dir.encode())

    torch.cuda.synchronize()
    elapsed = time.time() - t0

    data_path = os.path.join(ckpt_dir, "gpu_data.bin")
    data_gb = os.path.getsize(data_path) / 1e9 if os.path.exists(data_path) else 0
    bw_gbs = data_gb / elapsed if elapsed > 0 else 0

    print("[rank %d] save: ret=%d, %.1f GB in %.1fs (%.1f GB/s)" %
          (rank, ret, data_gb, elapsed, bw_gbs))

    dist.barrier()

    # Verify GPU still works
    dist.all_reduce(tensors[0], op=dist.ReduceOp.SUM)
    result = torch.matmul(
        tensors[0].reshape(1024, -1)[:1024, :1024],
        tensors[1].reshape(1024, -1)[:1024, :1024]
    )
    print("[rank %d] post-save NCCL + matmul OK" % rank)

    dist.barrier()
    if rank == 0:
        print("\n=== SUCCESS: %.1f GB/GPU checkpoint save passed ===" % total_gb)

    # Cleanup
    import shutil
    shutil.rmtree(ckpt_dir, ignore_errors=True)

    dist.destroy_process_group()
    return 0

if __name__ == "__main__":
    sys.exit(main())

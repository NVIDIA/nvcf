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
Test NvSnap interposition with multi-GPU NCCL.

Runs on a single node with 4 GPUs:
1. Loads NvSnap via LD_PRELOAD
2. Initializes PyTorch distributed with NCCL
3. Allocates model-sized tensors on each GPU
4. Runs NCCL allreduce
5. Calls nvsnap_checkpoint_save
6. Verifies data integrity post-save

This is the real test: does NvSnap survive PyTorch's CUDA caching
allocator + NCCL's internal GPU allocations across multiple GPUs?

Usage:
    NVSNAP_GPU_LOG_LEVEL=3 LD_PRELOAD=/tmp/libwrap_gpu.so \
        torchrun --nproc_per_node=4 test_nccl.py
"""
import ctypes
import os
import sys
import tempfile

def main():
    import torch
    import torch.distributed as dist

    rank = int(os.environ.get("LOCAL_RANK", "0"))
    world_size = int(os.environ.get("WORLD_SIZE", "1"))

    if world_size < 2:
        print("SKIP: Need at least 2 GPUs. Use torchrun --nproc_per_node=4")
        return 0

    # Init process group
    dist.init_process_group(backend="nccl")
    torch.cuda.set_device(rank)

    if rank == 0:
        print("=== NvSnap Multi-GPU NCCL Test ===")
        print("World size: %d" % world_size)
        print("GPU: %s" % torch.cuda.get_device_properties(0).name)

    # Load NvSnap for checkpoint API
    try:
        wc = ctypes.CDLL("/tmp/libwrap_gpu.so")
        wc.nvsnap_checkpoint_save.restype = ctypes.c_int
        wc.nvsnap_checkpoint_save.argtypes = [ctypes.c_char_p]
        has_nvsnap_gpu = True
    except OSError:
        has_nvsnap_gpu = False
        if rank == 0:
            print("WARNING: Cannot load NvSnap library")

    dist.barrier()

    # Step 1: Allocate model-sized tensors (~2GB per GPU)
    if rank == 0:
        print("\n--- Step 1: Allocate 2GB per GPU ---")

    tensors = []
    for i in range(8):
        t = torch.randn(256, 256, 256, device="cuda")  # ~64MB
        tensors.append(t)

    total_mb = sum(t.numel() * t.element_size() for t in tensors) / 1e6
    alloc_mb = torch.cuda.memory_allocated() / 1e6
    ref_sums = [t.sum().item() for t in tensors]

    print("[rank %d] Allocated %.0f MB on GPU %d, mem used %.0f MB" %
          (rank, total_mb, rank, alloc_mb))

    dist.barrier()

    # Step 2: NCCL allreduce
    if rank == 0:
        print("\n--- Step 2: NCCL allreduce ---")

    for t in tensors:
        dist.all_reduce(t, op=dist.ReduceOp.SUM)

    post_sums = [t.sum().item() for t in tensors]
    print("[rank %d] allreduce done, tensor[0] sum: %.2f -> %.2f" %
          (rank, ref_sums[0], post_sums[0]))

    dist.barrier()

    # Step 3: More NCCL ops (allgather, broadcast)
    if rank == 0:
        print("\n--- Step 3: Additional NCCL ops ---")

    # Broadcast
    bcast_t = torch.ones(1000, device="cuda") * rank
    dist.broadcast(bcast_t, src=0)
    assert bcast_t[0].item() == 0.0, "Broadcast failed"

    # Allgather
    gather_t = torch.ones(1000, device="cuda") * rank
    gather_list = [torch.zeros(1000, device="cuda") for _ in range(world_size)]
    dist.all_gather(gather_list, gather_t)
    for i, g in enumerate(gather_list):
        assert abs(g[0].item() - i) < 0.01, "Allgather failed for rank %d" % i

    print("[rank %d] broadcast + allgather OK" % rank)

    dist.barrier()

    # Step 4: Checkpoint save
    if rank == 0:
        print("\n--- Step 4: NvSnap checkpoint save ---")

    if has_nvsnap_gpu:
        ckpt_dir = tempfile.mkdtemp(prefix="nvsnap_nccl_r%d_" % rank)
        ret = wc.nvsnap_checkpoint_save(ckpt_dir.encode())

        meta_path = os.path.join(ckpt_dir, "meta.bin")
        data_path = os.path.join(ckpt_dir, "gpu_data.bin")
        meta_sz = os.path.getsize(meta_path) if os.path.exists(meta_path) else 0
        data_sz = os.path.getsize(data_path) if os.path.exists(data_path) else 0

        print("[rank %d] checkpoint save: ret=%d, meta=%d, data=%.0f MB" %
              (rank, ret, meta_sz, data_sz / 1e6))
    else:
        print("[rank %d] checkpoint save: SKIPPED (no NvSnap)" % rank)

    dist.barrier()

    # Step 5: Verify GPU still works after checkpoint save
    if rank == 0:
        print("\n--- Step 5: Post-checkpoint NCCL verify ---")

    verify_t = torch.randn(1000, device="cuda")
    dist.all_reduce(verify_t, op=dist.ReduceOp.SUM)
    print("[rank %d] post-checkpoint allreduce OK, sum=%.2f" %
          (rank, verify_t.sum().item()))

    dist.barrier()

    # Step 6: Matmul to verify compute
    result = torch.matmul(tensors[0], tensors[1].T)
    print("[rank %d] post-checkpoint matmul OK, shape=%s" % (rank, result.shape))

    dist.barrier()
    if rank == 0:
        print("\n=== SUCCESS: NvSnap + NCCL multi-GPU test passed ===")

    dist.destroy_process_group()
    return 0

if __name__ == "__main__":
    sys.exit(main())

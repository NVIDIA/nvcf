<!--
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
-->

# gpushare: checkpointing GPU memory shared between processes

`cuda-checkpoint` cannot checkpoint a process that maps GPU memory imported from
another process. That rules out multi-GPU (tensor-parallel) workloads in their
default configuration, because the following all share memory across processes:

- NCCL peer-to-peer over cuMem imports;
- NCCL NVLS multicast objects;
- CUDA IPC, as used by vLLM custom all-reduce and PyTorch;
- fabric handles;
- page-locked host memory on Grace.

`libnvsnap_gpushare.so` is an LD_PRELOAD shim that tracks this memory. It
releases the memory before the driver checkpoint and re-creates it at the same
virtual addresses after restore, so pointers and captured CUDA graphs stay
valid. `nvsnap-gpu-suspend` drives the shim and the CUDA checkpoint API.

The sources are under `docker/agent/gpushare/`. Both binaries are built in the
agent base image (`Dockerfile.base`, `gpushare-builder` stage, CUDA 13 headers)
and installed in `/criu-bundle/`. Tests are under `tests/gpushare/`.

## How it works

Each process that loads the shim runs a control thread on the abstract socket
`@nvsnap-gpushare.<pid>`. `nvsnap-gpu-suspend` sends it these commands
(details in `gpushare.h`):

| Command | Who | What it does |
| --- | --- | --- |
| `quiesce` | all pids, before any release | Holds kernel and graph launches, drains the GPU, checks that everything shared can be released. |
| `release` | all pids at once | Unmaps imported memory and multicast objects and unregisters host memory. Saves the shim's own cuMem allocations, to host memory or to a chunk store. |
| *(driver)* | | `cuCheckpointProcessLock` / `Checkpoint`, in parallel. |
| `load` | all pids at once, after restore | Re-creates the saved allocations at the same addresses. |
| `remap` | each pid | Re-imports peers' memory and rebuilds multicast objects. |
| `resume` | each pid | Binds multicast memory and reopens launches. |

The shim backs `cuMemAlloc` of 2 MiB or more with exportable cuMem allocations,
so that CUDA IPC works across a restore.

## Usage

The workload runs with `LD_PRELOAD=/path/to/libnvsnap_gpushare.so`. Pass
`nvsnap-gpu-suspend` every pid of the workload: the API server, the engine and
all tensor-parallel ranks.

```sh
# In place: free the GPUs, keep the processes paused, then resume.
nvsnap-gpu-suspend suspend <pids...>
nvsnap-gpu-suspend resume <pids...>

# For CRIU: save GPU memory to a chunk store, hand off to CRIU, and restore
# elsewhere.
nvsnap-gpu-suspend --store /ckpt/store --ckpt-dir /ckpt/<id> --cache /cache suspend <pids...>
nvsnap-gpu-suspend stop <pids...>          # then: criu dump
# ... criu restore (in a new pod or on another node) ...
nvsnap-gpu-suspend --gpu-map /ckpt/<id>/gpus --cache /cache resume <pids...>

# Node cache.
nvsnap-gpu-suspend cache-prefetch /ckpt/store /ckpt/<id> /cache
nvsnap-gpu-suspend cache-gc /cache <max-GiB>
```

### Chunk store

The shim stores the GPU memory it saves as 64 MiB content-addressed chunks
under `<store>/chunks/xx/<hash>`:

- A chunk that is already in the store is not written again. Model weights
  repeat across checkpoints of the same model, so they are stored once.
- All-zero chunks are not stored at all.
- Each chunk is written to a temporary file, flushed with `fdatasync`, then
  renamed, so a crash never leaves a chunk name pointing at partial data.
- Chunk files use `O_DIRECT`.

`<ckpt-dir>/gpu-<pid>.chunks` lists the chunks each checkpoint uses, for
accounting and garbage collection. The CRIU image then holds only host process
memory.

The optional node cache (`--cache`) is a local directory with the same layout.
Saves copy chunks into it. Loads read the cache first and fill it from the
store. `cache-gc` evicts least-recently-used chunks.

## Requirements and limits

- **Driver.** NVLS multicast restore on GB300 needs a driver that lets a
  restored process create and join multicast objects. 610.57.04 does;
  580.173.02 does not.
- **IMEX / fabric handles.** Pods with an IMEX channel do not restore yet:
  after a restore, the driver refuses to export fabric-capable memory.
- **Scope.** Memory shared across nodes (multi-node NVLink) is not tracked, so
  its checkpoint is refused. Exporters are found among the processes on the
  same node.
- **glibc.** The shim is built on Ubuntu 22.04 and needs glibc 2.35 or later in
  the workload image.
- **Host memory.** Without `--store`, the GPU memory the shim saves is kept in
  host RAM. Size the pod's memory limit for it.

## Validation

All results below are for vLLM 0.20.0 at default flags, Qwen2.5-72B-Instruct,
TP=4 on 4x GB300, driver 610.57.04. In every case the output after restore
matched the output before checkpoint byte for byte.

**In-place suspend/resume (Qwen2.5-7B, TP=4):** 3 out of 3 cycles passed.

**Checkpoint on one node, restore on another, from a network-block-storage
PVC:**

| Phase | Time |
| --- | --- |
| New pod to first token | 87.6 s |
| Same, with the restore node's cache prefetched | ~56 s |
| GPU chunk load, from the PVC | 29.2 s |
| GPU chunk load, from the node cache | 7.6 s |

For comparison, a cold start on a fresh node, including the model download,
took 1350 s.

**Store growth:** the first checkpoint wrote 159 GiB to the store. A second
checkpoint of the same model wrote 20 GiB.

To reproduce, run `tests/gpushare/k8s/vllm_criu_bench.sh` (see its header) and
`tests/gpushare/k8s/vllm_ckpt_cycle.sh`.
